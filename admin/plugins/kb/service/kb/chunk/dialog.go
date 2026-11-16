// plugin: kb v1.0.0
// dialog 策略：会话/聊天导入（表：轮=单元、target=512 多轮合并、**禁跨会话合并**、
// speaker_prefix、秒级时间戳）。
//
// 输入契约（ASR 转写 / 会话导入文本）：
//   - 轮首行 = `[<ts>] <speaker>: <text>`；ts ∈ {HH:MM:SS, MM:SS, SS}（可带 .mmm 小数，截整）；
//   - 无前缀行 = 上一轮续行；空行 = **会话边界**（块不跨空行拼接——禁跨会话合并）；
//   - 全文无任何合法轮首行 ⇒ 响亮报错（禁把非会话文本静默按会话切）。
//
// 口径：
//   - 轮 = 单元；同会话内连续轮并入同一块直至超过 target（多轮合并）；会话边界/超 target/超硬帽
//     处断块；单轮自身超硬帽按硬帽步进在轮内切分（块共享该轮时间戳；
//     hard_cap 为全局成本上限——同 page 策略）；
//   - speaker_prefix：轮首行原样保留于块文本（前缀天然在 verbatim 文本内）——本策略**不改写文本**，
//     块 Text = 提取文本区间逐字切片，锚/逐字校验与其余策略同构（VerifyVerbatim 适用；
//     注释里「dialog 只能用 VerifyWithin」的预设不再需要）；
//   - 产出行：TimeFrom = 首轮时间戳（秒），TimeTo = 末轮内最后一个行首时间戳（无 = 0）
//     ——kb_chunk.time_from/time_to 落列（BIGINT 秒）。
package chunk

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

// dialogChunker dialog 策略
type dialogChunker struct{}

// Name 策略名（注册表键）
func (dialogChunker) Name() string { return "dialog" }

// Version 契约版本（口径升级 ⇒ 升版本 + 重跑派生投影）
func (dialogChunker) Version() string { return "v1" }

// dialogTurn 轮单元（rune 区间 + 时间戳读数；boundaryBefore = 轮前有空行（会话边界））
type dialogTurn struct {
	start, end     int
	tsFrom, tsTo   int
	boundaryBefore bool
}

// Chunk 轮分块（零文本 / 无合法轮首 ⇒ 响亮报错）
func (dialogChunker) Chunk(_ context.Context, in Input) ([]Chunk, error) {
	rs := []rune(in.Text)
	if isBlankRunes(rs) {
		return nil, errors.New("kb dialog 切片零文本（空/全空白；零文本禁静默入库）")
	}
	turns := scanDialogTurns(rs)
	if len(turns) == 0 {
		return nil, errors.New("kb dialog 切片无合法轮首行（契约：[HH:MM:SS] speaker: text；禁把非会话文本按会话切）")
	}

	target := in.Config.Target
	if target <= 0 {
		target = defaultTarget
	}
	hardCap := in.Config.HardCap
	if hardCap <= 0 {
		hardCap = defaultHardCap
	}
	overlap := in.Config.Overlap
	if overlap < 0 {
		overlap = 0
	}

	var out []Chunk
	cur := unit{-1, -1}
	curFrom, curTo := 0, 0
	flush := func() {
		if cur.start >= 0 {
			out = append(out, Chunk{
				CharStart: cur.start, CharEnd: cur.end,
				TimeFrom: int64(curFrom), TimeTo: int64(curTo),
				Text: string(rs[cur.start:cur.end]),
			})
			cur = unit{-1, -1}
		}
	}
	// splitLong 轮内超硬帽切分（各子块共享该轮时间戳；步长 = hardCap − overlap）
	splitLong := func(t dialogTurn) {
		step := hardCap - overlap
		if step <= 0 {
			step = hardCap
		}
		for s := t.start; s < t.end; {
			e := s + hardCap
			if e > t.end {
				e = t.end
			}
			out = append(out, Chunk{
				CharStart: s, CharEnd: e,
				TimeFrom: int64(t.tsFrom), TimeTo: int64(t.tsTo),
				Text: string(rs[s:e]),
			})
			if e >= t.end {
				break
			}
			s += step
		}
	}

	for _, t := range turns {
		if t.end-t.start > hardCap {
			flush()
			splitLong(t) // 单轮超帽：轮内切分（时间戳共享）
			continue
		}
		if cur.start < 0 || t.boundaryBefore || t.end-cur.start > target {
			flush() // 会话边界 / 超 target / 首轮：断块（禁跨会话合并）
			cur = unit{t.start, t.end}
			curFrom, curTo = t.tsFrom, t.tsTo
			continue
		}
		cur.end = t.end // 多轮合并（同会话、未超 target）
		curTo = t.tsTo
	}
	flush()
	return out, nil
}

// scanDialogTurns 逐行扫描轮（轮首行开新轮；续行并入；空行记会话边界；空行不改变轮区间）。
// 宽容登记：空行后出现无前缀行（契约外输入）并入当前轮——轮区间跨空行的唯一例外；轮首行前的
// 空行（正常形态）只置 boundaryBefore，块必在此断（禁跨会话合并的主载体）。
func scanDialogTurns(rs []rune) []dialogTurn {
	lines := scanLines(rs)
	var turns []dialogTurn
	blankSeen := false
	cur := -1
	for _, ln := range lines {
		line := string(rs[ln.start:ln.end])
		if strings.TrimSpace(line) == "" {
			if cur >= 0 {
				blankSeen = true // 空行 = 会话边界（轮后空行 ⇒ 下一轮 boundaryBefore）
			}
			continue
		}
		if ts, _, ok := parseDialogTurn(line); ok {
			turns = append(turns, dialogTurn{
				start: ln.start, end: ln.end, tsFrom: ts, tsTo: ts,
				boundaryBefore: blankSeen,
			})
			blankSeen = false
			cur = len(turns) - 1
			continue
		}
		if cur >= 0 {
			turns[cur].end = ln.end // 续行（含其前空白行由 line 定位自然处理）
			if ts, ok := parseLeadClock(line); ok {
				turns[cur].tsTo = ts // 续行行首时间戳（如逐条转写输出）
			}
		}
	}
	return turns
}

// parseDialogTurn 轮首行解析：`[<ts>] <speaker>: <text>`（speaker 非空且不含 `]`；非轮首 ⇒ ok=false）
func parseDialogTurn(line string) (tsSec int, speaker string, ok bool) {
	s := strings.TrimSpace(line)
	if !strings.HasPrefix(s, "[") {
		return 0, "", false
	}
	end := strings.IndexByte(s, ']')
	if end < 0 {
		return 0, "", false
	}
	sec, ok := parseClock(s[1:end])
	if !ok {
		return 0, "", false
	}
	rest := s[end+1:]
	colon := strings.IndexByte(rest, ':')
	if colon <= 0 {
		return 0, "", false
	}
	sp := strings.TrimSpace(rest[:colon])
	if sp == "" || strings.ContainsAny(sp, "]") {
		return 0, "", false
	}
	return sec, sp, true
}

// parseLeadClock 行首 `[<ts>]` 时间戳（不要求 speaker；续行读数用）
func parseLeadClock(line string) (int, bool) {
	s := strings.TrimSpace(line)
	if !strings.HasPrefix(s, "[") {
		return 0, false
	}
	end := strings.IndexByte(s, ']')
	if end <= 1 {
		return 0, false
	}
	return parseClock(s[1:end])
}

// parseClock 时间戳解析：HH:MM:SS / MM:SS / SS（可带 .小数，截整；负号/非数字 ⇒ ok=false）
func parseClock(s string) (int, bool) {
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = s[:i] // 丢小数部分（秒级口径）
	}
	parts := strings.Split(s, ":")
	if len(parts) == 0 || len(parts) > 3 {
		return 0, false
	}
	mult := []int{1, 60, 3600} // 最右 = 秒
	total := 0
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return 0, false
		}
		total += n * mult[len(parts)-1-i]
	}
	return total, true
}
