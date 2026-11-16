// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"fmt"
	"strings"
	"unicode"
)

// isNumericTabularDataRow 0-LLM 纯字符拓扑与列转换判定（零词表、零语种假设）：
// 判断单行是否为密集数值测量矩阵的数据行（如 TSV/CSV/空格对齐的多列实验数据行）。
func isNumericTabularDataRow(line string) bool {
	var (
		tabOrPipeCnt    int
		digitRunes      int
		letterRunes     int
		cellTransitions int
		inWhitespace    = true
	)
	for _, r := range line {
		if r == '\t' || r == '|' {
			tabOrPipeCnt++
		}
		if unicode.IsSpace(r) || r == '|' {
			inWhitespace = true
			continue
		}
		if inWhitespace {
			cellTransitions++
			inWhitespace = false
		}
		if unicode.IsDigit(r) {
			digitRunes++
		} else if unicode.IsLetter(r) {
			letterRunes++
		}
	}
	if cellTransitions < 5 || digitRunes < 8 {
		return false
	}
	if tabOrPipeCnt < 2 && cellTransitions < 6 {
		return false
	}
	// 数字码点数超过文字码点数的 2.5 倍（即纯数值/科学计数法/百分比占绝对主导的数据行，排除含大量自然语言单词的散文句子）
	return digitRunes*2 > letterRunes*5
}

func isBlankOrTabOnlyLine(line string) bool {
	for _, r := range line {
		if !unicode.IsSpace(r) && r != '|' {
			return false
		}
	}
	return true
}

// extractDenseTabularParts 从原始文本中提取内联高密度数值表格（TSV/CSV/实验测量矩阵）作为一等公民虚拟证据件（virtual://tabular_data#k）。
func extractDenseTabularParts(content string) []VirtualPart {
	if !strings.Contains(content, "\n") {
		return nil
	}
	lines := strings.Split(content, "\n")
	n := len(lines)
	if n < 4 {
		return nil
	}
	isNumRow := make([]bool, n)
	numTotal := 0
	for i, ln := range lines {
		if isNumericTabularDataRow(ln) {
			isNumRow[i] = true
			numTotal++
		}
	}
	if numTotal < 4 {
		return nil
	}

	var out []VirtualPart
	seq := 0
	i := 0
	for i < n {
		if !isNumRow[i] {
			i++
			continue
		}
		// 向上吸收紧邻的 1~3 行表头/实验条件行
		startLine := i
		for back := 1; back <= 3 && i-back >= 0; back++ {
			prev := lines[i-back]
			if isBlankOrTabOnlyLine(prev) {
				continue
			}
			if strings.Count(prev, "\t") >= 1 || strings.Count(prev, "|") >= 2 || CountTokens(prev) <= TokenTier64 {
				startLine = i - back
			} else {
				break
			}
		}
		endLine := i
		numCnt := 0
		j := i
		for j < n {
			if isNumRow[j] {
				numCnt++
				endLine = j
				j++
				continue
			}
			if isBlankOrTabOnlyLine(lines[j]) {
				j++
				continue
			}
			// 允许中间夹杂最多 4 行短表头/子表条件（如 pH / Temperature / 列名行），若后续紧跟数值行则合并为同一表格件
			lookaheadNum := false
			for k := 1; k <= 5 && j+k < n; k++ {
				if isNumRow[j+k] {
					lookaheadNum = true
					break
				}
			}
			if lookaheadNum && CountTokens(lines[j]) <= TokenTier128 {
				j++
				continue
			}
			break
		}
		if numCnt >= 4 {
			block := strings.TrimSpace(strings.Join(lines[startLine:endLine+1], "\n"))
			if CountTokens(block) >= minVirtualPartTokens {
				seq++
				out = append(out, VirtualPart{
					Kind:    "file",
					SubKind: "tabular_data",
					URI:     fmt.Sprintf("virtual://tabular_data#%d", seq),
					Locator: fmt.Sprintf("L%d-L%d", startLine+1, endLine+1),
					Text:    block,
					Tool:    "tabular_data",
				})
			}
		}
		if j > i {
			i = j
		} else {
			i++
		}
	}
	return out
}

// CompactTurnSlicesForEncoder 从送往写侧编码器的源轮切片中剥离已存入 virtual://tabular_data 的连续纯数值数据行（≥3 行），
// 仅保留自然语言叙述段落、实验条件与表头行（每个返回片段 100% 保持为原轮文本的连续逐字子串，零破坏 anchor_start/end 字节偏移）。
func CompactTurnSlicesForEncoder(slices []string) []string {
	if len(slices) == 0 {
		return slices
	}
	var out []string
	for _, s := range slices {
		compacted := splitSliceOutNumericBlocks(s)
		out = append(out, compacted...)
	}
	if len(out) == 0 {
		// 若整轮 100% 都是纯数值行（无任何自然语言或表头行），保留首行前 TokenTier64 作为最小锚点切片
		first := slices[0]
		if nl := strings.IndexByte(first, '\n'); nl > 0 {
			first = first[:nl]
		}
		return []string{TruncateToTokens(first, TokenTier128)}
	}
	return out
}

func splitSliceOutNumericBlocks(s string) []string {
	if !strings.Contains(s, "\n") {
		if isNumericTabularDataRow(s) {
			return nil
		}
		return []string{s}
	}
	type lineSpan struct {
		start int
		end   int
		isNum bool
		blank bool
	}
	var spans []lineSpan
	pos := 0
	for pos < len(s) {
		nl := strings.IndexByte(s[pos:], '\n')
		var end int
		var lineText string
		if nl < 0 {
			end = len(s)
			lineText = s[pos:end]
		} else {
			end = pos + nl + 1
			lineText = s[pos : pos+nl]
		}
		spans = append(spans, lineSpan{
			start: pos,
			end:   end,
			isNum: isNumericTabularDataRow(lineText),
			blank: isBlankOrTabOnlyLine(lineText),
		})
		pos = end
	}

	n := len(spans)
	skipLine := make([]bool, n)
	i := 0
	hasSkipped := false
	for i < n {
		if !spans[i].isNum {
			i++
			continue
		}
		j := i
		numCnt := 0
		for j < n && (spans[j].isNum || spans[j].blank) {
			if spans[j].isNum {
				numCnt++
			}
			j++
		}
		if numCnt >= 3 {
			for k := i; k < j; k++ {
				skipLine[k] = true
			}
			hasSkipped = true
		}
		i = j
	}
	if !hasSkipped {
		return []string{s}
	}

	var kept []string
	curStart := -1
	curEnd := -1
	for idx, sp := range spans {
		if skipLine[idx] {
			if curStart >= 0 {
				sub := strings.TrimSpace(s[curStart:curEnd])
				if StringShannonBits(sub) >= 8.0 {
					kept = append(kept, sub)
				}
				curStart = -1
			}
			continue
		}
		if curStart < 0 {
			curStart = sp.start
		}
		curEnd = sp.end
	}
	if curStart >= 0 {
		sub := strings.TrimSpace(s[curStart:curEnd])
		if StringShannonBits(sub) >= 8.0 {
			kept = append(kept, sub)
		}
	}
	return kept
}
