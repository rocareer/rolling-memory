// plugin: kb v1.0.0
// table 策略：csv/表结构块（表：rows_per_chunk=50、header_repeat=true、min_rows=3）。
//
// 产出口径：
//   - 记录扫描（引号感知）取每条 CSV 记录的 rune 区间；字段解析用 encoding/csv（权威语义）——
//     条数不一致（如未闭合引号）⇒ 响亮报错，不静默降级。
//   - 首记录 = 表头；RowFrom/RowTo 从 1 起计**数据行**（表头不计）；数据行按 rows_per_chunk 分块；
//     尾块数据行 < min_rows 且并入前块后 ≤ hard_cap ⇒ 并入前块。
//   - 首块区间含表头行（逐字），其余块从各自首数据行起。kb_chunk.text 存 verbatim 记录文本；
//     header_repeat 的表头只进派生文本（ChunkTextOf 前置）——锚契约要求正文逐字可复算，
//     故重复表头不得掺进 text 列；表头原件在 table_meta 留档。
package chunk

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"strings"
)

// tableChunker table 策略
type tableChunker struct{}

// Name 策略名（注册表键）
func (tableChunker) Name() string { return "table" }

// Version 契约版本（口径升级 ⇒ 升版本 + 重跑派生投影）
func (tableChunker) Version() string { return "v1" }

// Chunk CSV 记录分块（零记录/解析不一致 ⇒ 响亮报错）
func (tableChunker) Chunk(_ context.Context, in Input) ([]Chunk, error) {
	rs := []rune(in.Text)
	if isBlankRunes(rs) {
		return nil, errors.New("kb table 切片零文本（空/全空白；零文本禁静默入库）")
	}

	reader := csv.NewReader(strings.NewReader(in.Text))
	reader.FieldsPerRecord = -1 // 行字段数不齐（宽松表）不报错
	reader.LazyQuotes = true    // 容忍裸引号（脏数据不阻断；不一致由下方条数核对暴露）
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("kb table CSV 解析失败: %w", err)
	}

	spans := nonBlankUnits(rs, scanCSVRecords(rs))
	if len(spans) != len(records) {
		return nil, fmt.Errorf("kb table CSV 记录扫描与解析不一致：扫描 %d 条 / 解析 %d 条（引号未闭合或脏数据）", len(spans), len(records))
	}
	if len(records) == 0 {
		return nil, errors.New("kb table 零记录（零文本禁静默入库）")
	}

	rowsPerChunk := in.Config.RowsPerChunk
	if rowsPerChunk < 1 {
		rowsPerChunk = defaultRowsPerChunk
	}
	minRows := in.Config.MinRows
	hardCap := in.Config.HardCap
	if hardCap <= 0 {
		hardCap = defaultHardCap
	}

	header := append([]string(nil), records[0]...)
	columns := len(header)
	headerSpan := spans[0]
	dataSpans := spans[1:]

	var out []Chunk
	if len(dataSpans) == 0 {
		// 仅表头：单块（含表头行）
		text := string(rs[headerSpan.start:headerSpan.end])
		out = append(out, Chunk{
			CharStart: headerSpan.start,
			CharEnd:   headerSpan.end,
			Text:      text,
			TableMeta: &TableMeta{Header: header, Columns: columns},
		})
		return out, nil
	}

	// 数据行分块 [a,b]（0 起闭区间，索引进 dataSpans/records[1:]）
	type block struct{ a, b int }
	var blocks []block
	for a := 0; a < len(dataSpans); a += rowsPerChunk {
		b := a + rowsPerChunk - 1
		if b > len(dataSpans)-1 {
			b = len(dataSpans) - 1
		}
		blocks = append(blocks, block{a, b})
	}
	// 尾块并入前块（min_rows：尾块数据行不足 min_rows 不单独成块）
	if len(blocks) >= 2 {
		last := blocks[len(blocks)-1]
		if last.b-last.a+1 < minRows {
			blocks = blocks[:len(blocks)-1]
			blocks[len(blocks)-1].b = last.b
		}
	}

	for i, blk := range blocks {
		start := dataSpans[blk.a].start
		if i == 0 {
			start = headerSpan.start // 首块含表头行（逐字）
		}
		end := dataSpans[blk.b].end
		if end-start > hardCap {
			return nil, fmt.Errorf("kb table 块超硬帽：行 %d-%d 计 %d rune > hard_cap %d（调小 rows_per_chunk 或调大 hard_cap）",
				blk.a+1, blk.b+1, end-start, hardCap)
		}
		out = append(out, Chunk{
			CharStart: start,
			CharEnd:   end,
			Text:      string(rs[start:end]),
			TableMeta: &TableMeta{Header: header, RowFrom: blk.a + 1, RowTo: blk.b + 1, Columns: columns},
		})
	}
	return out, nil
}

// scanCSVRecords 引号感知逐记录扫描：返回每条记录的 rune 区间（不含记录尾 `\r\n`/`\n`）。
// 语义对齐 encoding/csv（双引号包裹、`""` 转义、引号内可含换行）。
func scanCSVRecords(rs []rune) []unit {
	var out []unit
	start := -1
	inQuotes := false
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if inQuotes {
			if r == '"' {
				if i+1 < len(rs) && rs[i+1] == '"' {
					i++
					continue
				}
				inQuotes = false
			}
			continue
		}
		switch r {
		case '"':
			if start < 0 {
				start = i
			}
			inQuotes = true
		case '\n':
			if start < 0 {
				continue
			}
			end := i
			if end > start && rs[end-1] == '\r' {
				end--
			}
			out = append(out, unit{start, end})
			start = -1
		default:
			if start < 0 {
				start = i
			}
		}
	}
	if start >= 0 {
		end := len(rs)
		if end > start && rs[end-1] == '\r' {
			end--
		}
		out = append(out, unit{start, end})
	}
	return out
}

// nonBlankUnits 剔除空白区间（CSV 空行）并收缩首尾空白
func nonBlankUnits(rs []rune, units []unit) []unit {
	out := make([]unit, 0, len(units))
	for _, u := range units {
		if isBlankRunes(rs[u.start:u.end]) {
			continue
		}
		out = append(out, u)
	}
	return out
}
