// plugin: kb v1.0.0
// fixed 策略：无结构纯文本兜底（表：target=512、overlap=64（仅超帽切分启用；结构边界不叠加）、
// hard_cap=1024、tail_merge=target/4）。
//
// 本文件同时承载**共享文本工具**（rune 口径单点：行扫描/分段/贪心打包），供 heading/table/code/semantic
// 复用——打包语义（target 并入、hardCap 硬帽、overlap 仅超帽切分启用、tail_merge 尾块合并）全仓仅此一处。
package chunk

import (
	"context"
	"errors"
	"unicode"
)

// unit rune 区间（半开 [start,end)）
type unit struct{ start, end int }

// scanLines 逐行扫描：返回每行 rune 区间（不含行尾换行；含 `\r` 剥离；空行亦成一项）。
// 末尾无换行且有内容 ⇒ 末行成项；文本以换行结尾 ⇒ 不额外产出空行。
func scanLines(rs []rune) []unit {
	var out []unit
	start := 0
	for i, r := range rs {
		if r != '\n' {
			continue
		}
		end := i
		if end > start && rs[end-1] == '\r' {
			end--
		}
		out = append(out, unit{start, end})
		start = i + 1
	}
	if start < len(rs) {
		end := len(rs)
		if end > start && rs[end-1] == '\r' {
			end--
		}
		out = append(out, unit{start, end})
	}
	return out
}

// isBlankRunes 全空白判定（unicode.IsSpace）
func isBlankRunes(rs []rune) bool {
	for _, r := range rs {
		if !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

// trimSpan 收缩区间首尾空白；全空白 ⇒ ok=false
func trimSpan(rs []rune, u unit) (unit, bool) {
	for u.start < u.end && unicode.IsSpace(rs[u.start]) {
		u.start++
	}
	for u.end > u.start && unicode.IsSpace(rs[u.end-1]) {
		u.end--
	}
	return u, u.start < u.end
}

// splitParagraphsRange 按空行分段（连续非空行 = 一段）：区间 [start,end) 内的段，返回**绝对**区间
// （已收缩首尾空白；全空白段剔除）。
func splitParagraphsRange(rs []rune, start, end int) []unit {
	if start < 0 {
		start = 0
	}
	if end > len(rs) {
		end = len(rs)
	}
	if start >= end {
		return nil
	}
	lines := scanLines(rs[start:end])
	var raw []unit
	cur := unit{-1, -1}
	for _, ln := range lines {
		u := unit{ln.start + start, ln.end + start}
		if isBlankRunes(rs[u.start:u.end]) {
			if cur.start >= 0 {
				raw = append(raw, cur)
				cur = unit{-1, -1}
			}
			continue
		}
		if cur.start < 0 {
			cur = u
			continue
		}
		cur.end = u.end
	}
	if cur.start >= 0 {
		raw = append(raw, cur)
	}
	out := make([]unit, 0, len(raw))
	for _, u := range raw {
		if t, ok := trimSpan(rs, u); ok {
			out = append(out, t)
		}
	}
	return out
}

// splitParagraphs 全文本分段（splitParagraphsRange 的 [0,len) 形态）
func splitParagraphs(rs []rune) []unit { return splitParagraphsRange(rs, 0, len(rs)) }

// packUnits 贪心打包（共享单点）：
//   - 单元 ≤ hardCap：并入当前块直到再并会超 target；超 target ⇒ 起新块；
//   - 单元 > hardCap：先 flush 当前块，再按 hardCap 步进切分（步长 = hardCap − overlap；overlap 仅此处生效，
//     结构边界不叠加——）；
//   - 尾块（末块 < tail_merge）并入前块（并入后 ≤ hardCap；tail_merge ≤ 0 ⇒ 不合并）。
//
// 产出块 Text = 原文本 [CharStart,CharEnd) 的逐字切片（不含块间空白的裁剪 —— 块内空隙原样保留）。
func packUnits(rs []rune, units []unit, cfg Config) []Chunk {
	hardCap := cfg.HardCap
	if hardCap <= 0 {
		hardCap = defaultHardCap
	}
	target := cfg.Target
	if target <= 0 {
		target = defaultTarget
	}
	overlap := cfg.Overlap
	if overlap < 0 {
		overlap = 0
	}

	var chunks []Chunk
	cur := unit{-1, -1}
	flush := func() {
		if cur.start >= 0 {
			chunks = append(chunks, Chunk{CharStart: cur.start, CharEnd: cur.end, Text: string(rs[cur.start:cur.end])})
			cur = unit{-1, -1}
		}
	}
	splitLong := func(u unit) {
		step := hardCap - overlap
		if step <= 0 {
			step = hardCap
		}
		for s := u.start; s < u.end; {
			e := s + hardCap
			if e > u.end {
				e = u.end
			}
			chunks = append(chunks, Chunk{CharStart: s, CharEnd: e, Text: string(rs[s:e])})
			if e >= u.end {
				break
			}
			s += step
		}
	}

	for _, u := range units {
		if u.end <= u.start {
			continue
		}
		if u.end-u.start > hardCap {
			flush()
			splitLong(u)
			continue
		}
		if cur.start < 0 {
			cur = u
			continue
		}
		if u.end-cur.start > target {
			flush()
			cur = u
			continue
		}
		cur.end = u.end
	}
	flush()

	if cfg.TailMerge > 0 && len(chunks) >= 2 {
		last := chunks[len(chunks)-1]
		prev := chunks[len(chunks)-2]
		if last.CharEnd-last.CharStart < cfg.TailMerge && last.CharEnd-prev.CharStart <= hardCap {
			merged := Chunk{
				CharStart: prev.CharStart,
				CharEnd:   last.CharEnd,
				Text:      string(rs[prev.CharStart:last.CharEnd]),
			}
			chunks = append(chunks[:len(chunks)-2], merged)
		}
	}
	return chunks
}

// fixedChunker fixed 策略（无结构纯文本兜底）
type fixedChunker struct{}

// Name 策略名（注册表键）
func (fixedChunker) Name() string { return "fixed" }

// Version 契约版本（口径升级 ⇒ 升版本 + 重跑派生投影）
func (fixedChunker) Version() string { return "v1" }

// Chunk 分段打包（零文本 ⇒ 响亮报错）
func (fixedChunker) Chunk(_ context.Context, in Input) ([]Chunk, error) {
	rs := []rune(in.Text)
	if isBlankRunes(rs) {
		return nil, errors.New("kb fixed 切片零文本（空/全空白；零文本禁静默入库）")
	}
	return packUnits(rs, splitParagraphs(rs), in.Config), nil
}
