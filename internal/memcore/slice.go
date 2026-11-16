// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rocareer/rolling-memory/internal/text"
	"github.com/rocareer/rolling-memory/internal/tokenzr"
)

// 本文件 = 记忆生产 1024 三位一体源轮切片与历史/工作上下文收敛算子（0-LLM · 零外部依赖 · 100% 逐字子串保真）：
//   1. 单轮切片（SplitTurnIntoVerbatimSlices）：
//      - 不压缩、不超不切：单轮 tokens ≤ SliceTargetTokens (1024) 时整轮原样保持；
//      - 超长单轮（> 1024 tokens）按自然句界/换行切分，目标片长 1024 tokens、跨片重叠 128 tokens、
//        尾片若 < 256 tokens 则不单独成片直接随前片合并；
//      - 每个切片均为原轮文本的 100% 逐字子串，落图 structured assertion 通过 (anchor_trace_hash, anchor_start, anchor_end) 精确绑回原轮字节区间。
//   2. 历史上下文（CapContextTurnsByTokens）：
//      - 紧前最多 ContextWindowTurns (4) 轮，单轮 ≤ 1024 tokens 时不截断直接给完整原始原文（巨型轮取最末 1024 切片）；
//      - 从紧邻前 1 轮往回累加，累计 ≤ ContextTotalTokenBudget (1024 tokens) 即止（长则 1 轮完整原文，短则 4 轮完整原文）。

const (
	// 标准 2 的幂次 Token 阶梯（全链路统一使用 tokenz-r 计数，严格使用 4 / 8 / 16 / 32 / 64 / 128 / 256 / 512 / 1024 / 2048 / 4096 / 8192 阶梯）。
	TokenTier4    = 4
	TokenTier8    = 8
	TokenTier16   = 16
	TokenTier32   = 32
	TokenTier64   = 64
	TokenTier128  = 128
	TokenTier256  = 256
	TokenTier512  = 512
	TokenTier1024 = 1024
	TokenTier2048 = 2048
	TokenTier4096 = 4096
	TokenTier8192 = 8192

	// SliceTargetTokens 单轮触发切片的阈值与单片目标上限（≤1024 不切，>1024 按 1024 切）。
	SliceTargetTokens = TokenTier1024
	// SliceOverlapTokens 相邻切片之间的句界重叠窗口（保持上下文与指代连贯）。
	SliceOverlapTokens = TokenTier128
	// SliceMinTailTokens 尾片最小独立阈值（尾片不足此值时不单独切片，直接并入前片）。
	SliceMinTailTokens = TokenTier256
	// BatchTokenBudget 单批次工作上下文待编码源轮正文最大 Token 预算（与单片 1024 对齐）。
	BatchTokenBudget = TokenTier1024
	// BatchMaxTurns 单批次最大片段条数。
	BatchMaxTurns = 8
	// ContextWindowTurns 紧前历史原文窗口最大轮数。
	ContextWindowTurns = 4
	// ContextSingleTurnTokens 历史上下文单轮最大 Token 数（≤1024 直接给完整原始原文）。
	ContextSingleTurnTokens = TokenTier1024
	// ContextTotalTokenBudget 紧前 1~4 轮历史原文的累计最大 Token 预算。
	ContextTotalTokenBudget = TokenTier1024
	// RecentAssertionWindow 本会话热区近期已落图 structured assertion 保留窗口。
	RecentAssertionWindow = 16
	// PreProbeCap 写前图谱预探针既有活跃断言上限。
	PreProbeCap = 64
)

// TokenCounterFunc 词元计数函数签名（唯一正本 internal/text，本包仅转发）。
type TokenCounterFunc = text.TokenCounterFunc

// SetDefaultTokenCounter 注册全局默认 tokenz-r 词元计数器（转发 internal/text）。
func SetDefaultTokenCounter(fn TokenCounterFunc) { text.SetDefaultTokenCounter(fn) }

// CountTokens 使用 tokenz-r 词元计数器计算文本 Token 数（转发 internal/text）。
func CountTokens(s string) int { return text.CountTokens(s) }

// NormalizeTokenTierBudget 将预算值规整到标准 128 / 256 / 512 / 1024 / 2048 / 4096 / 8192 Token 阶梯。
func NormalizeTokenTierBudget(budget int) int {
	switch {
	case budget <= 0:
		return TokenTier256
	case budget <= TokenTier128:
		return TokenTier128
	case budget <= TokenTier256:
		return TokenTier256
	case budget <= TokenTier512:
		return TokenTier512
	case budget <= TokenTier1024:
		return TokenTier1024
	case budget <= TokenTier2048:
		return TokenTier2048
	case budget <= TokenTier4096:
		return TokenTier4096
	default:
		return TokenTier8192
	}
}

// TruncateToTokens 将文本按 tokenz-r 计数截断至不超过 maxTokens（二分查找 UTF-8 字符边界，防在转义符末尾劈断）。
func TruncateToTokens(text string, maxTokens int) string {
	maxTokens = NormalizeTokenTierBudget(maxTokens)
	if CountTokens(text) <= maxTokens {
		return text
	}
	rs := []rune(text)
	lo, hi := 0, len(rs)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if CountTokens(string(rs[:mid])) <= maxTokens {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	for lo > 1 && rs[lo-1] == '\\' {
		lo--
	}
	if lo <= 0 {
		return ""
	}
	return strings.ToValidUTF8(string(rs[:lo]), "") + "…"
}

// SliceRunesBounded 按 rune 坐标提取 [start, end) 严格子串（等价于 []rune(value)[start:end]，
// 但在扫描到 end 时立即早停且零整篇 []rune 拷贝，适用于数百 KB 源轮原文的锚点提取；
// start < 0、end <= start 或 end 超出总 rune 数时严格返回 ""）。
func SliceRunesBounded(value string, start, end int) string {
	if start < 0 || end <= start {
		return ""
	}
	runeIdx := 0
	byteStart := -1
	for byteIdx := range value {
		if runeIdx == start {
			byteStart = byteIdx
		}
		if runeIdx == end {
			if byteStart < 0 {
				return ""
			}
			return value[byteStart:byteIdx]
		}
		runeIdx++
	}
	if byteStart < 0 || runeIdx != end {
		return ""
	}
	return value[byteStart:]
}

// EstimateTokensFallback 使用内嵌 tokenz-r 128k 词表直接计算 Token 数。
func EstimateTokensFallback(text string) int {
	return tokenzr.Count(text)
}

func countWith(fn TokenCounterFunc, text string) int {
	if text == "" {
		return 0
	}
	if fn != nil {
		return fn(text)
	}
	return CountTokens(text)
}

type verbatimSpan struct {
	start  int
	end    int
	tokens int
}

// SplitTurnIntoVerbatimSlices 将单轮文本按 Token 规则切分为 100% 逐字子串切片：
//   - tokens ≤ 1024：不切，原样返回 []string{text}；
//   - tokens > 1024：按句界切 ≤1024 tokens，重叠 128 tokens，尾片 < 256 tokens 随前片合并。
func SplitTurnIntoVerbatimSlices(text string, counter TokenCounterFunc) []string {
	return SplitTurnIntoVerbatimSlicesWithTarget(text, SliceTargetTokens, SliceOverlapTokens, SliceMinTailTokens, counter)
}

// SplitTurnIntoVerbatimSlicesWithTarget 按指定目标片长、重叠窗口与尾片阈值将单轮文本切分为 100% 逐字子串切片。
func SplitTurnIntoVerbatimSlicesWithTarget(text string, targetTokens, overlapTokens, minTailTokens int, counter TokenCounterFunc) []string {
	if targetTokens <= 0 {
		targetTokens = SliceTargetTokens
	}
	if overlapTokens <= 0 {
		overlapTokens = SliceOverlapTokens
	}
	if minTailTokens <= 0 {
		minTailTokens = SliceMinTailTokens
	}
	totalTokens := countWith(counter, text)
	if totalTokens <= targetTokens {
		return []string{text}
	}

	units := splitSentenceUnitsWithTarget(text, targetTokens, counter)
	if len(units) <= 1 {
		return splitHardRunesWithTarget(text, targetTokens, counter)
	}

	var spans []verbatimSpan
	n := len(units)
	idx := 0
	for idx < n {
		startByte := units[idx].start
		endByte := units[idx].end
		curTokens := units[idx].tokens
		nextIdx := idx + 1

		for nextIdx < n {
			u := units[nextIdx]
			if curTokens+u.tokens > targetTokens {
				break
			}
			endByte = u.end
			curTokens += u.tokens
			nextIdx++
		}

		if nextIdx < n {
			remTokens := 0
			for k := nextIdx; k < n; k++ {
				remTokens += units[k].tokens
			}
			if remTokens < minTailTokens {
				endByte = units[n-1].end
				curTokens += remTokens
				spans = append(spans, verbatimSpan{start: startByte, end: endByte, tokens: curTokens})
				break
			}
		}

		spans = append(spans, verbatimSpan{start: startByte, end: endByte, tokens: curTokens})
		if nextIdx >= n {
			break
		}

		curOverlap := 0
		backIdx := nextIdx
		for backIdx-1 > idx {
			prevU := units[backIdx-1]
			if curOverlap+prevU.tokens > overlapTokens {
				break
			}
			curOverlap += prevU.tokens
			backIdx--
		}
		idx = backIdx
	}

	if len(spans) >= 2 && spans[len(spans)-1].tokens < minTailTokens {
		prev := &spans[len(spans)-2]
		last := spans[len(spans)-1]
		prev.end = last.end
		prev.tokens = countWith(counter, text[prev.start:prev.end])
		spans = spans[:len(spans)-1]
	}

	out := make([]string, 0, len(spans))
	for _, sp := range spans {
		sub := text[sp.start:sp.end]
		if strings.TrimSpace(sub) != "" {
			out = append(out, sub)
		}
	}
	if len(out) == 0 {
		return []string{text}
	}
	return out
}

// SplitTurnAdaptiveWholeTurn 按自适应整轮承载律（ComputeAdaptiveTurnSliceSpec）处理单轮文本：
//   - 若单轮 ≤ 1024 tokens：不切，按 1024 基础预算高注意力装箱；
//   - 若单轮 ∈ (1024, 4096] tokens：整轮不切，自适应扩容工作上下文预算为 turnTokens，使工作上下文完整承载一整轮；
//   - 若单轮 > 4096 tokens：按 2048 目标片长句界重叠切分，并对超长工具轮保留首尾关键片同批承载。
func SplitTurnAdaptiveWholeTurn(text string, counter TokenCounterFunc) ([]string, AdaptiveTurnSliceSpec) {
	segments := CompactTurnSlicesForEncoder([]string{text})
	if len(segments) == 1 && segments[0] == text {
		totalTokens := countWith(counter, text)
		spec := ComputeAdaptiveTurnSliceSpec(totalTokens)
		if !spec.ShouldSlice {
			return []string{text}, spec
		}
		slices := SplitTurnIntoVerbatimSlicesWithTarget(text, spec.SliceTarget, spec.OverlapTokens, spec.MinTailTokens, counter)
		slices = FocusOversizedToolTurnSlices(text, slices)
		return slices, spec
	}
	var out []string
	maxToks := 0
	for _, seg := range segments {
		segToks := countWith(counter, seg)
		if segToks > maxToks {
			maxToks = segToks
		}
		segSpec := ComputeAdaptiveTurnSliceSpec(segToks)
		if !segSpec.ShouldSlice {
			out = append(out, seg)
		} else {
			sub := SplitTurnIntoVerbatimSlicesWithTarget(seg, segSpec.SliceTarget, segSpec.OverlapTokens, segSpec.MinTailTokens, counter)
			out = append(out, FocusOversizedToolTurnSlices(seg, sub)...)
		}
	}
	return out, ComputeAdaptiveTurnSliceSpec(maxToks)
}

func splitSentenceUnits(text string, counter TokenCounterFunc) []verbatimSpan {
	return splitSentenceUnitsWithTarget(text, SliceTargetTokens, counter)
}

// VerbatimSpan 单条逐字子串切片的字节区间与 Token 计数（100% 原文连续子串，不跨句界）。
type VerbatimSpan struct {
	Start  int
	End    int
	Tokens int
}

// SplitSentenceSpans 把文本切分为句界对齐的逐字候选窗（读侧「信息量段选」算子的输入）：
//   - 每个窗都是原文本的 100% 逐字连续子串（不摘要、不改写、不拼接）；
//   - 普通句 ≤ targetTokens；超长无句界单元按 Token 阶梯对半硬切（碎片 ≥ TokenTier64）；
//   - targetTokens ≤ 0 取 TokenTier256，候选粒度足够细，段选才能挑出信息最强的某一段。
//
// 与 SplitTurnIntoVerbatimSlices 的区别：后者是写侧切片（1024 目标 + 128 重叠，要覆盖全文）；
// 本函数是读侧候选窗（不重叠、可丢弃），只保证每个窗都是合法原文片段。
func SplitSentenceSpans(text string, targetTokens int) []VerbatimSpan {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if targetTokens <= 0 {
		targetTokens = TokenTier256
	}
	units := splitSentenceUnitsWithTarget(text, targetTokens, nil)
	out := make([]VerbatimSpan, 0, len(units))
	for _, u := range units {
		if u.start < 0 || u.end > len(text) || u.end <= u.start {
			continue
		}
		if strings.TrimSpace(text[u.start:u.end]) == "" {
			continue
		}
		out = append(out, VerbatimSpan{Start: u.start, End: u.end, Tokens: u.tokens})
	}
	return out
}

func splitSentenceUnitsWithTarget(text string, targetTokens int, counter TokenCounterFunc) []verbatimSpan {
	if targetTokens <= 0 {
		targetTokens = SliceTargetTokens
	}
	var raw []verbatimSpan
	start := 0
	for i := 0; i < len(text); {
		r, width := utf8.DecodeRuneInString(text[i:])
		i += width
		if isSentBoundary(r) {
			for i < len(text) {
				nr, nw := utf8.DecodeRuneInString(text[i:])
				if nr == '"' || nr == '\'' || nr == '”' || nr == '’' || nr == ')' || nr == '）' || nr == ']' || nr == '】' || nr == ' ' || nr == '\t' {
					i += nw
					continue
				}
				break
			}
			raw = append(raw, verbatimSpan{start: start, end: i})
			start = i
		}
	}
	if start < len(text) {
		raw = append(raw, verbatimSpan{start: start, end: len(text)})
	}

	var out []verbatimSpan
	for _, sp := range raw {
		sub := text[sp.start:sp.end]
		toks := countWith(counter, sub)
		if toks <= targetTokens {
			out = append(out, verbatimSpan{start: sp.start, end: sp.end, tokens: toks})
			continue
		}
		out = append(out, splitOversizedWithTarget(text, sp.start, sp.end, targetTokens, counter)...)
	}
	return out
}

func isSentBoundary(r rune) bool {
	switch r {
	case '\n', '。', '！', '？', '；', '.', '!', '?', ';':
		return true
	default:
		return false
	}
}

func splitOversized(fullText string, start, end int, counter TokenCounterFunc) []verbatimSpan {
	return splitOversizedWithTarget(fullText, start, end, SliceTargetTokens, counter)
}

func splitOversizedWithTarget(fullText string, start, end, targetTokens int, counter TokenCounterFunc) []verbatimSpan {
	if targetTokens <= 0 {
		targetTokens = SliceTargetTokens
	}
	halfTarget := targetTokens / 2
	if halfTarget < TokenTier64 {
		halfTarget = TokenTier64
	}
	sub := fullText[start:end]
	rs := []rune(sub)
	if len(rs) == 0 {
		return nil
	}
	// 使用 O(K log N) 二分查找替代 O(N^2) 逐 rune 线性重算，并优先对齐邻近空白/标点边界
	var spans []verbatimSpan
	curRuneIdx := 0
	curByteOffset := start
	for curRuneIdx < len(rs) {
		remRunes := rs[curRuneIdx:]
		remStr := string(remRunes)
		remToks := countWith(counter, remStr)
		if remToks <= halfTarget {
			spans = append(spans, verbatimSpan{start: curByteOffset, end: end, tokens: remToks})
			break
		}
		lo, hi := 1, len(remRunes)
		for lo < hi {
			mid := (lo + hi + 1) / 2
			if countWith(counter, string(remRunes[:mid])) <= halfTarget {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		cut := lo
		if cut < len(remRunes) {
			for back := cut; back > (cut*3)/4; back-- {
				ch := remRunes[back-1]
				if ch == ' ' || ch == ',' || ch == ';' || ch == '}' || ch == ')' || ch == '。' || ch == '，' || ch == '；' || ch == '\t' {
					cut = back
					break
				}
			}
		}
		pieceStr := string(remRunes[:cut])
		pieceBytes := len(pieceStr)
		pieceToks := countWith(counter, pieceStr)
		spans = append(spans, verbatimSpan{
			start:  curByteOffset,
			end:    curByteOffset + pieceBytes,
			tokens: pieceToks,
		})
		curByteOffset += pieceBytes
		curRuneIdx += cut
	}
	return spans
}

func splitHardRunes(text string, counter TokenCounterFunc) []string {
	return splitHardRunesWithTarget(text, SliceTargetTokens, counter)
}

func splitHardRunesWithTarget(text string, targetTokens int, counter TokenCounterFunc) []string {
	spans := splitOversizedWithTarget(text, 0, len(text), targetTokens, counter)
	if len(spans) <= 1 {
		return []string{text}
	}
	out := make([]string, 0, len(spans))
	for _, sp := range spans {
		out = append(out, text[sp.start:sp.end])
	}
	return out
}

// CompactContextTurnText 保障历史上下文单轮不超 1024 tokens（≤1024 原样返回完整原始原文；>1024 取最末切片）。
func CompactContextTurnText(text string, counter TokenCounterFunc) string {
	return CompactContextTurnTextWithCap(text, ContextSingleTurnTokens, counter)
}

// CompactContextTurnTextWithCap 保障历史上下文单轮不超 singleCap tokens（≤singleCap 原样返回完整原始原文；>singleCap 取最末切片）。
func CompactContextTurnTextWithCap(text string, singleCap int, counter TokenCounterFunc) string {
	text = strings.TrimSpace(text)
	if singleCap <= 0 {
		singleCap = ContextSingleTurnTokens
	}
	if text == "" || countWith(counter, text) <= singleCap {
		return text
	}
	target := SliceTargetTokens
	if singleCap > target {
		target = OversizedTurnSliceTarget
	}
	slices := SplitTurnIntoVerbatimSlicesWithTarget(text, target, SliceOverlapTokens, SliceMinTailTokens, counter)
	if len(slices) == 0 {
		return text
	}
	return strings.TrimSpace(slices[len(slices)-1])
}

// CapContextTurnsByTokens 对紧前历史原文行（最多 4 轮）按从近到远施加自适应整轮预算：
//   - 若紧邻前 1 轮 ≤ 1024 tokens：按 1024 基础预算从最近 1 轮往回累加完整短轮；
//   - 若紧邻前 1 轮 ∈ (1024, 4096] tokens：自适应扩容以 100% 完整承载该紧邻整轮（工作上下文至少承载一个整轮）。
func CapContextTurnsByTokens(lines []string, counter TokenCounterFunc) []string {
	if len(lines) == 0 {
		return nil
	}
	if len(lines) > ContextWindowTurns {
		lines = lines[len(lines)-ContextWindowTurns:]
	}
	trimmedLines := make([]string, 0, len(lines))
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln != "" {
			trimmedLines = append(trimmedLines, ln)
		}
	}
	if len(trimmedLines) == 0 {
		return nil
	}
	latestLine := trimmedLines[len(trimmedLines)-1]
	latestBody := latestLine
	if colon := strings.Index(latestLine, ": "); colon > 0 {
		latestBody = latestLine[colon+2:]
	} else if strings.HasPrefix(latestLine, "- ") {
		latestBody = latestLine[2:]
	}
	latestSingleCap, totalBudget := ComputeAdaptiveContextBudget(countWith(counter, latestBody))

	compacted := make([]string, 0, len(trimmedLines))
	for idx, ln := range trimmedLines {
		capForLine := ContextSingleTurnTokens
		if idx == len(trimmedLines)-1 {
			capForLine = latestSingleCap
		}
		if colon := strings.Index(ln, ": "); colon > 0 {
			spk := ln[:colon]
			body := CompactContextTurnTextWithCap(ln[colon+2:], capForLine, counter)
			compacted = append(compacted, spk+": "+body)
		} else if strings.HasPrefix(ln, "- ") {
			compacted = append(compacted, "- "+CompactContextTurnTextWithCap(ln[2:], capForLine, counter))
		} else {
			compacted = append(compacted, CompactContextTurnTextWithCap(ln, capForLine, counter))
		}
	}
	if len(compacted) == 0 {
		return nil
	}
	totalToks := 0
	keepFrom := len(compacted) - 1
	for i := len(compacted) - 1; i >= 0; i-- {
		t := countWith(counter, compacted[i])
		if i < len(compacted)-1 && totalToks+t > totalBudget {
			break
		}
		totalToks += t
		keepFrom = i
	}
	return compacted[keepFrom:]
}

// TraceWatermarkRow 表示生产窗口中一条按 ID 升序排列的源语料进度锚点（零宿主依赖通用结构）。
type TraceWatermarkRow struct {
	ID      int64
	Session string
}

// ComputeSafePartialWatermark 计算多会话/分批生产窗口在部分会话失败或流式推进时的最大连续安全域级水位：
// 沿按 ID 升序排列的 rows 扫描，找到第一条尚未完成的行（r.ID > sessDone[r.Session]），
// 返回该行之前已连续完成的最高 trace ID（若首行即未完成则返回 after）。
// 保证任何 <= 返回值的源语料行均已 100% 完成落图，且不会因新会话首轮失败（游标=0）将前序已完成会话的水位归零重扫。
func ComputeSafePartialWatermark(rows []TraceWatermarkRow, after int64, sessDone map[string]int64) int64 {
	safe := after
	for _, r := range rows {
		if r.ID <= after {
			continue
		}
		if r.ID <= sessDone[r.Session] {
			if r.ID > safe {
				safe = r.ID
			}
		} else {
			break
		}
	}
	return safe
}

// WaveCausalAssertion 表示单会话分波并发编码中一条已落图的活跃断言摘要。
type WaveCausalAssertion struct {
	NodeID        uint64
	SubjectID     uint64
	RelID         uint64
	RelLabel      string
	ObjectID      uint64
	ObjectLabel   string
	ObjectLiteral string
	// ObjectIsAssertion 客体为断言节点（外层言说/提问/认知包装行）。
	ObjectIsAssertion bool
	Warrant           string
	World             string
	LoopState         string
	TurnHash          string
	TSource           int64
	TValidFrom        int64
	WaveOrder         int
	// ClosesPriorValue：本条显式关闭点名的旧值 O 槽原文（逐字）；波次补关按此匹配。
	ClosesPriorValue string
}

// WaveSupersedePair 表示经波次因果序对齐后需补齐取代状态的（旧断言 -> 新断言）对。
type WaveSupersedePair struct {
	OlderNodeID  uint64
	CloserNodeID uint64
	ValidTo      int64
}

// PriorTurnCandidate represents an earlier dialogue turn within the same session
// outside the immediate sliding context window, used for 0-LLM causal resonance pre-projection.
type PriorTurnCandidate struct {
	TurnSeq int    // 1-based chronological turn index within the session
	Speaker string // Resolved speaker label
	DateStr string // Formatted UTC date/time string of the turn
	Text    string // Raw turn or slice text
}

// SelectIntraSessionPriorResonance performs 0-LLM, 100% language-agnostic Shannon Self-Information
// Bit Field + IDF morphological resonance over earlier turns of the same session (`priorTurns`)
// against `currentBatchText`.
//
// Why this eliminates intra-session serial LLM waiting from first principles:
//   - Immediate adjacent coreference (<= 4 turns) is already covered by the static verbatim sliding window (`prevCtx`);
//   - Long-range intra-session entity/event recurrence (> 4 turns back in the same wave before earlier batches finish LLM encoding)
//     is captured in <0.3ms directly from the raw source turns using posterior Shannon self-information bits
//     I(w) = H_0(w) * (1 + ln((N+1)/(df(w)+1))) and Morphological Bit-Field similarity (`MorphSimWordPair`),
//     returning the top resonant prior turns in strict chronological order (`TurnSeq ASC`).
func SelectIntraSessionPriorResonance(
	priorTurns []PriorTurnCandidate,
	currentBatchText string,
	maxItems int,
	maxTotalTokens int,
	counter TokenCounterFunc,
) []PriorTurnCandidate {
	if len(priorTurns) == 0 || strings.TrimSpace(currentBatchText) == "" {
		return nil
	}
	if maxItems <= 0 {
		maxItems = 6
	}
	if maxTotalTokens <= 0 {
		maxTotalTokens = 384
	}
	extractSalientWords := func(text string) []string {
		raw := Tokenize(text)
		if len(raw) == 0 {
			return nil
		}
		seen := make(map[string]bool, len(raw))
		out := make([]string, 0, len(raw))
		for _, tk := range raw {
			tl := strings.ToLower(strings.TrimSpace(tk))
			if tl == "" || seen[tl] || IsShortOrDigitsToken(tl) {
				continue
			}
			// Top-K-by-mass：不再按熵阈值杀死低比特词元；全部候选进入，由下游 IDF 权重
			// （常见词 df 高 ⇒ 权重低）与 maxItems/maxTotalTokens 预算完成优选（Gain）。
			seen[tl] = true
			out = append(out, tl)
		}
		return out
	}

	batchWords := extractSalientWords(currentBatchText)
	if len(batchWords) == 0 {
		return nil
	}
	batchRunes := make([][]rune, len(batchWords))
	for i, bw := range batchWords {
		batchRunes[i] = []rune(bw)
	}

	nDocs := len(priorTurns) + 1
	df := make(map[string]int, len(batchWords)*4)
	priorWordSets := make([][]string, len(priorTurns))
	for i, pt := range priorTurns {
		ws := extractSalientWords(pt.Text)
		priorWordSets[i] = ws
		for _, w := range ws {
			df[w]++
		}
	}
	for _, bw := range batchWords {
		df[bw]++
	}

	batchInfos := make([]float64, len(batchWords))
	for i, bw := range batchWords {
		prior := StringShannonBits(bw)
		if prior > 0 {
			if IsPureCJK(bw) {
				if bpePrior := tokenzr.TokenPriorBits(bw); bpePrior > 0 {
					prior = bpePrior
				}
			}
			d := df[bw]
			if d < 1 {
				d = 1
			}
			batchInfos[i] = prior * (1.0 + math.Log(float64(nDocs+1)/float64(d+1)))
		}
	}

	pairSimMemo := make(map[[2]string]float64, len(batchWords)*8)
	type scoredTurn struct {
		idx   int
		score float64
	}
	var scored []scoredTurn
	for i, ws := range priorWordSets {
		if len(ws) == 0 {
			continue
		}
		wSet := make(map[string]bool, len(ws))
		wRunes := make([][]rune, len(ws))
		for wi, w := range ws {
			wSet[w] = true
			wRunes[wi] = []rune(w)
		}
		var sharedBits float64
		var matchedCount int
		for bi, bw := range batchWords {
			info := batchInfos[bi]
			if info <= 0 {
				continue
			}
			if wSet[bw] {
				sharedBits += info
				matchedCount++
				continue
			}
			// Language-agnostic morphological bit-field stem resonance
			br := batchRunes[bi]
			bestSim := 0.0
			for wi, wr := range wRunes {
				pw := ws[wi]
				key := [2]string{bw, pw}
				sim, ok := pairSimMemo[key]
				if !ok {
					sim = MorphSimWordPair(br, br, wr)
					if len(pairSimMemo) < 4096 {
						pairSimMemo[key] = sim
					}
				}
				if sim >= 0.75 && sim > bestSim {
					bestSim = sim
				}
			}
			if bestSim >= 0.75 {
				sharedBits += bestSim * info
				matchedCount++
			}
		}
		// Require >= 14.0 posterior Shannon bits (e.g. a shared specific entity/concept or >=2 content stems)
		if sharedBits >= 14.0 && matchedCount >= 1 {
			scored = append(scored, scoredTurn{idx: i, score: sharedBits})
		}
	}
	if len(scored) == 0 {
		return nil
	}
	sort.Slice(scored, func(i, j int) bool {
		if math.Abs(scored[i].score-scored[j].score) > 1e-6 {
			return scored[i].score > scored[j].score
		}
		return scored[i].idx > scored[j].idx // Prefer more recent prior turn on tie
	})

	var selectedIdx []int
	usedTokens := 0
	for _, sc := range scored {
		if len(selectedIdx) >= maxItems {
			break
		}
		pt := priorTurns[sc.idx]
		compText := CompactContextTurnTextWithCap(pt.Text, 160, counter)
		toks := countWith(counter, compText)
		if len(selectedIdx) > 0 && usedTokens+toks > maxTotalTokens {
			continue
		}
		usedTokens += toks
		selectedIdx = append(selectedIdx, sc.idx)
	}
	sort.Ints(selectedIdx)
	out := make([]PriorTurnCandidate, 0, len(selectedIdx))
	for _, idx := range selectedIdx {
		pt := priorTurns[idx]
		pt.Text = CompactContextTurnTextWithCap(pt.Text, 160, counter)
		out = append(out, pt)
	}
	return out
}

// ExtractInlineTurnSpeaker extracts an explicit inline dialogue speaker prefix from the start of a turn
// (after stripping leading `[...]` metadata tags), returning `""` when no valid person speaker prefix exists
// and `"STAGE"` when the turn is an explicit all-caps stage/structural header.
func ExtractInlineTurnSpeaker(text string) string {
	s := strings.TrimSpace(text)
	for strings.HasPrefix(s, "[") {
		closeIdx := strings.IndexByte(s, ']')
		if closeIdx <= 0 || closeIdx > 80 {
			break
		}
		tag := strings.ToLower(strings.TrimSpace(s[1:closeIdx]))
		if strings.HasPrefix(tag, "tool_") || strings.HasPrefix(tag, "thinking") ||
			strings.HasPrefix(tag, "code") || strings.HasPrefix(tag, "file") ||
			strings.HasPrefix(tag, "image") || strings.HasPrefix(tag, "audio") ||
			strings.HasPrefix(tag, "video") || strings.HasPrefix(tag, "system") {
			return ""
		}
		s = strings.TrimSpace(s[closeIdx+1:])
	}
	if nl := strings.IndexByte(s, '\n'); nl >= 0 {
		s = strings.TrimSpace(s[:nl])
	}
	for pass := 0; pass < 2; pass++ {
		colonIdx := strings.IndexByte(s, ':')
		if colonIdx <= 0 || colonIdx > 32 || colonIdx+1 >= len(s) || s[colonIdx+1] != ' ' {
			return ""
		}
		cand := strings.TrimSpace(s[:colonIdx])
		if IsRolePlaceholder(cand) {
			s = strings.TrimSpace(s[colonIdx+1:])
			continue
		}
		if cand == "" || StringShannonBits(cand) < 4.0 || strings.ContainsAny(cand, "/_()[]{}<>=\"'`\\0123456789") {
			return ""
		}
		words := strings.Fields(cand)
		if len(words) == 0 || CountTokens(cand) > TokenTier4 {
			return ""
		}
		hasUpper, hasLower, hasIdeograph := false, false, false
		for _, w := range words {
			rs := []rune(w)
			if !IsContinuousSyllabicOrIdeographic(rs[0]) && !unicode.IsUpper(rs[0]) {
				return ""
			}
			for _, r := range rs {
				if IsContinuousSyllabicOrIdeographic(r) {
					hasIdeograph = true
				} else if unicode.IsUpper(r) {
					hasUpper = true
				} else if unicode.IsLower(r) {
					hasLower = true
				}
			}
		}
		if hasUpper && !hasLower && !hasIdeograph {
			return "STAGE"
		}
		if !hasIdeograph && !hasLower {
			return ""
		}
		return cand
	}
	return ""
}

// CollapseInlineCodeFences replaces inline ```...``` fenced code blocks with `[...]` while preserving
// all surrounding natural-language prose (speaker header, question, and trailing turn metadata).
func CollapseInlineCodeFences(s string) string {
	trimmed := strings.TrimSpace(s)
	if !strings.Contains(trimmed, "```") {
		return trimmed
	}
	var out strings.Builder
	rem := trimmed
	for {
		openIdx := strings.Index(rem, "```")
		if openIdx < 0 {
			out.WriteString(rem)
			break
		}
		out.WriteString(rem[:openIdx])
		afterOpen := rem[openIdx+3:]
		closeIdx := strings.Index(afterOpen, "```")
		if closeIdx < 0 {
			out.WriteString("[...]")
			break
		}
		out.WriteString("[...]")
		rem = afterOpen[closeIdx+3:]
	}
	return strings.TrimSpace(out.String())
}
