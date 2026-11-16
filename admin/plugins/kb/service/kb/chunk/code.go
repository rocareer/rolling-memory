// plugin: kb v1.0.0
// code 策略：代码扩展名（表：max=1024、overlap=0、文件/hunk 粒度（缩进/花括号启发式，不引 AST））。
//
// 判据（启发式，明确不引 AST）：
//   - 文件总长 ≤ max ⇒ 单块（文件粒度）；
//   - 否则求 hunk 边界：花括号深度 0 ∧ 行首缩进 0 ∧（前行空行；首行视为边界）；
//     每个 hunk 一块（**不做跨 hunk 合并**——hunk 粒度即交付粒度）；
//   - 单 hunk > max ⇒ 按 max 步进切分（步长 = max − overlap；overlap 缺省 0）；
//   - 无边界（如无空行的小文件）⇒ 回退按空行分段 + target 打包（同 fixed 口径，target = max）。
//
// 花括号深度为字符串感知的朴素计数（不处理注释块/模板字符串等完整语言语义——启发式边界足够，
// 边界错切最多损失分块规整度，不影响正确性）。
package chunk

import (
	"context"
	"errors"
	"unicode"
)

// codeChunker code 策略
type codeChunker struct{}

// Name 策略名（注册表键）
func (codeChunker) Name() string { return "code" }

// Version 契约版本（口径升级 ⇒ 升版本 + 重跑派生投影）
func (codeChunker) Version() string { return "v1" }

// Chunk 文件/hunk 粒度切分（零文本 ⇒ 响亮报错）
func (codeChunker) Chunk(_ context.Context, in Input) ([]Chunk, error) {
	rs := []rune(in.Text)
	if isBlankRunes(rs) {
		return nil, errors.New("kb code 切片零文本（空/全空白；零文本禁静默入库）")
	}
	max := in.Config.Max
	if max <= 0 {
		max = defaultMax
	}
	overlap := in.Config.Overlap
	if overlap < 0 {
		overlap = 0
	}

	// 文件粒度：整文件一块
	if len(rs) <= max {
		return []Chunk{{CharStart: 0, CharEnd: len(rs), Text: in.Text}}, nil
	}

	hunks := codeHunks(rs)
	if len(hunks) == 0 {
		// 回退：空行分段 + target（= max）打包（同 fixed 口径）
		cfg := in.Config
		cfg.Target = max
		cfg.HardCap = max
		if cfg.TailMerge == 0 {
			cfg.TailMerge = max / 4
		}
		return packUnits(rs, splitParagraphs(rs), cfg), nil
	}

	var out []Chunk
	step := max - overlap
	if step <= 0 {
		step = max
	}
	for _, u := range hunks {
		if u.end-u.start <= max {
			out = append(out, Chunk{CharStart: u.start, CharEnd: u.end, Text: string(rs[u.start:u.end])})
			continue
		}
		for s := u.start; s < u.end; {
			e := s + max
			if e > u.end {
				e = u.end
			}
			out = append(out, Chunk{CharStart: s, CharEnd: e, Text: string(rs[s:e])})
			if e >= u.end {
				break
			}
			s += step
		}
	}
	if len(out) == 0 {
		return nil, errors.New("kb code 切片零产出（零文本禁静默入库）")
	}
	return out, nil
}

// codeHunks 求 hunk 区间（边界 = 花括号深度 0 ∧ 行首缩进 0 ∧ 前行空行；首行视为边界）。
// 无边界 ⇒ 返回 nil（调用方回退）。
func codeHunks(rs []rune) []unit {
	lines := scanLines(rs)
	var bounds []int
	depth := 0
	prevBlank := true
	for i, ln := range lines {
		line := rs[ln.start:ln.end]
		blank := isBlankRunes(line)
		if !blank && depth == 0 && indentWidth(line) == 0 && prevBlank {
			bounds = append(bounds, i)
		}
		if !blank {
			depth += braceDelta(string(line))
			if depth < 0 {
				depth = 0
			}
		}
		prevBlank = blank
	}
	if len(bounds) <= 1 {
		return nil
	}
	var out []unit
	for i, bi := range bounds {
		start := lines[bi].start
		end := len(rs)
		if i+1 < len(bounds) {
			end = lines[bounds[i+1]].start
		}
		u, ok := trimSpan(rs, unit{start, end})
		if !ok {
			continue
		}
		out = append(out, u)
	}
	return out
}

// braceDelta 行内花括号净增量（字符串/字符字面量感知的朴素计数）
func braceDelta(line string) int {
	depth := 0
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0:
			if c == '\\' {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"' || c == '`':
			quote = c
		case c == '{':
			depth++
		case c == '}':
			depth--
		}
	}
	return depth
}

// indentWidth 行首缩进宽度（空格 1、制表符 4；全空白行返回 0）
func indentWidth(line []rune) int {
	w := 0
	for _, r := range line {
		switch r {
		case ' ':
			w++
		case '\t':
			w += 4
		case '\u3000': // 全角空格
			w += 2
		default:
			if unicode.IsSpace(r) {
				w++
				continue
			}
			return w
		}
	}
	return 0 // 全空白行
}
