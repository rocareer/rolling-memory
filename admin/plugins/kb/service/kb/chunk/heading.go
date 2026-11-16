// plugin: kb v1.0.0
// heading 策略：md/html/docx 标题层级（表：levels=1..6、target=512、heading_prefix=true）。
//
// 判据（本项目落地）：标题行 = ATX（`#{1,6} ` 或行尾）+ Setext（下一行全 `=`⇒1 级 / 全 `-`⇒2 级）。
// html 由 reader 剥脚本/样式后按同规则进本策略（「剥脚本/样式后同 heading」）。
// 分节 = 命中 levels 的标题行起、至下一条命中标题行前；非命中层级的标题行按普通正文处理。
// 每块 HeadingPath = 当前标题栈（祖先链），heading_prefix 时经 ChunkTextOf 前置进派生文本。
package chunk

import (
	"context"
	"errors"
	"strings"
)

// headingChunker heading 策略
type headingChunker struct{}

// Name 策略名（注册表键）
func (headingChunker) Name() string { return "heading" }

// Version 契约版本（口径升级 ⇒ 升版本 + 重跑派生投影）
func (headingChunker) Version() string { return "v1" }

// Chunk 标题分节 + 节内打包（零文本 ⇒ 响亮报错）
func (headingChunker) Chunk(_ context.Context, in Input) ([]Chunk, error) {
	rs := []rune(in.Text)
	if isBlankRunes(rs) {
		return nil, errors.New("kb heading 切片零文本（空/全空白；零文本禁静默入库）")
	}
	levels := map[int]bool{}
	for _, lv := range in.Config.Levels {
		levels[lv] = true
	}
	lines := scanLines(rs)

	type section struct {
		start, end int
		path       []string
	}
	var sections []section
	// 首个标题之前的序言文本自成一段（path 空）；标题命中时收在标题行之前
	cur := section{start: 0, end: -1}
	var stack [7]string // 1..6 级标题栈（cur[0] 不用）

	flushSection := func(end int) {
		if cur.start >= 0 && end > cur.start {
			cur.end = end
			sections = append(sections, cur)
		}
		cur = section{start: -1, end: -1}
	}

	for i := 0; i < len(lines); {
		title, level, consumed := headingAt(rs, lines, i)
		if level > 0 && levels[level] {
			// 标题行（含 setext 下划线行）归入**上一节**内容之后：先收上一节
			secEnd := lines[i].start
			flushSection(secEnd)
			stack[level] = title
			for lv := level + 1; lv <= 6; lv++ {
				stack[lv] = ""
			}
			path := make([]string, 0, level)
			for lv := 1; lv <= level; lv++ {
				if stack[lv] != "" {
					path = append(path, stack[lv])
				}
			}
			cur = section{start: lines[i].start, end: -1, path: path}
			i += consumed
			continue
		}
		i++
	}
	flushSection(len(rs))

	var out []Chunk
	for _, sec := range sections {
		units := splitParagraphsRange(rs, sec.start, sec.end)
		chunks := packUnits(rs, units, in.Config)
		for i := range chunks {
			chunks[i].HeadingPath = append([]string(nil), sec.path...)
		}
		out = append(out, chunks...)
	}
	if len(out) == 0 {
		return nil, errors.New("kb heading 切片零产出（标题分节后无内容；零文本禁静默入库）")
	}
	return out, nil
}

// headingAt 第 i 行是否标题：返回 (标题文本, 层级, 消耗行数)。
//   - ATX：`#{1,6}` 后接空白或行尾；闭合 `#` 串（前置空白）剥离。
//   - Setext：本行为非空文本行、下一行为全 `=`（1 级）或全 `-`（2 级）；标题文本 = 本行。
//
// 非标题 ⇒ level=0、消耗 1 行。
func headingAt(rs []rune, lines []unit, i int) (string, int, int) {
	if i < 0 || i >= len(lines) {
		return "", 0, 1
	}
	line := strings.TrimSpace(string(rs[lines[i].start:lines[i].end]))
	if level, title, ok := atxHeading(line); ok {
		return title, level, 1
	}
	if line == "" || i+1 >= len(lines) {
		return "", 0, 1
	}
	next := strings.TrimSpace(string(rs[lines[i+1].start:lines[i+1].end]))
	if level, ok := setextLevel(next); ok {
		return line, level, 2
	}
	return "", 0, 1
}

// atxHeading 解析 ATX 标题行（`#{1,6}` + 空白/行尾）
func atxHeading(line string) (int, string, bool) {
	n := 0
	for n < len(line) && line[n] == '#' {
		n++
	}
	if n == 0 || n > MaxChunkLevel {
		return 0, "", false
	}
	rest := line[n:]
	if rest != "" {
		if rest[0] != ' ' && rest[0] != '\t' {
			return 0, "", false
		}
		rest = strings.TrimSpace(rest)
	}
	// 闭合 `#` 串剥离（`# 标题 ##` ⇒ 标题）
	for len(rest) > 1 && strings.HasSuffix(rest, "#") {
		trimmed := strings.TrimRight(rest, "#")
		if trimmed == "" || !strings.HasSuffix(trimmed, " ") {
			break
		}
		rest = strings.TrimSpace(trimmed)
	}
	return n, strings.TrimSpace(rest), true
}

// setextLevel Setext 下划线级别（全 `=` ⇒ 1；全 `-` ⇒ 2；否则非下划线）
func setextLevel(line string) (int, bool) {
	if line == "" {
		return 0, false
	}
	ch := line[0]
	if ch != '=' && ch != '-' {
		return 0, false
	}
	for i := 0; i < len(line); i++ {
		if line[i] != ch {
			return 0, false
		}
	}
	if ch == '=' {
		return 1, true
	}
	return 2, true
}
