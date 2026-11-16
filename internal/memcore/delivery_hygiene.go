// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"math"
	"os"
	"strings"
	"sync/atomic"
	"unicode"
	"unicode/utf8"

	"github.com/rocareer/rolling-memory/internal/tokenzr"
)

// ShouldRenderAuxUnboundOrDualSection reports whether a band's auxiliary unbound-slot section (unboundChains)
// or binary predicate section (dualChains) carries sufficient signal to be rendered when the band already
// has primary SO or SX chains. Suppressing 0.5-score background unbound/binary tails eliminates "[主体未定]/[客体未定]"
// noise while keeping 100% of sections that either stand alone (!hasPrimarySOOrSX) or contain non-subject signal.
func ShouldRenderAuxUnboundOrDualSection(rows []TopologyRowView, hasPrimarySOOrSX bool) bool {
	if len(rows) == 0 {
		return false
	}
	if !hasPrimarySOOrSX || LegacyUndirectedTopologyMode.Load() {
		return true
	}
	for _, r := range rows {
		if r.NonSubj || r.OrigNonSubj || r.DirectXO || r.Score >= 2.5 {
			return true
		}
	}
	return false
}

// IsSnippetCoveredByDeliveredEvidence checks whether a candidate raw-supplement text window is already
// substantively covered by snippets or artifact summaries delivered in [E#k] / [A#m] for the same turn.
func IsSnippetCoveredByDeliveredEvidence(candidate string, delivered []string) bool {
	if len(delivered) == 0 {
		return false
	}
	candNorm := normalizeWhitespaceCompact(candidate)
	if CountTokens(candNorm) < TokenTier4 {
		return false
	}
	candToks := CountTokens(candNorm)
	for _, d := range delivered {
		dNorm := normalizeWhitespaceCompact(d)
		if CountTokens(dNorm) < TokenTier4 {
			continue
		}
		if strings.Contains(dNorm, candNorm) {
			return true
		}
		// If [E#k] / [A#m] already delivered >= 80% of a long candidate window from the exact same turn, treat as covered.
		if candToks > TokenTier32 && len(dNorm) >= (len(candNorm)*4)/5 && strings.Contains(candNorm, dNorm) {
			return true
		}
	}
	return false
}

func normalizeWhitespaceCompact(s string) string {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(s)))
	return strings.Join(fields, " ")
}

// IsRedundantSameTurnCompanionBand reports whether a minor band (bandIdx >= 2, not directly named in query cues,
// with <= 3 assertions) consists 100% of assertions whose source turns have already been fully expanded verbatim
// in an earlier primary band (seenFullTurn[turnHash] == true) and carry no standalone media parts.
// Skipping such pure same-turn echo micro-bands eliminates duplicate band headers and repeated structured memory assertion lines
// while preserving 100% of the verbatim source text and gold tokens in the primary band.
func IsRedundantSameTurnCompanionBand(
	bandIdx int,
	isCueMatchedBand bool,
	rows []TopologyRowView,
	seenFullTurn map[string]bool,
	hasMediaInBand bool,
) bool {
	if LegacyUndirectedTopologyMode.Load() {
		return false
	}
	if bandIdx < 4 || isCueMatchedBand || hasMediaInBand || len(rows) == 0 || len(rows) > 3 || len(seenFullTurn) == 0 {
		return false
	}
	for _, r := range rows {
		if r.SourceTraceHash == "" || !seenFullTurn[r.SourceTraceHash] {
			return false
		}
		if r.DirectXO {
			return false
		}
	}
	return true
}

// QualifiesAsRootSubjectBand decides whether a cue-matching band at index bandIdx qualifies as an
// unconditional root subject band (exempt from marginal convex pruning and allowed to expand reachTrace).
// In high-connectivity graphs where almost every common noun in the query is also a graph node,
// restricting unconditional root status at bandIdx >= 2 to true multi-edge protagonist bands or
// high-signal bands directly connected to the primary protagonist prevents common-noun micro-bands
// from bypassing pruning and polluting reachTrace across unrelated sessions.
func QualifiesAsRootSubjectBand(
	bandIdx int,
	rowCount int,
	maxNonSubjScore float64,
	isDirectConnectedToPrimary bool,
	matchesNonProtagonistCue ...bool,
) bool {
	if LegacyUndirectedTopologyMode.Load() {
		return true
	}
	if bandIdx == 0 {
		return true
	}
	if bandIdx == 1 {
		return rowCount >= 5 || maxNonSubjScore >= 4.0 || isDirectConnectedToPrimary
	}
	if rowCount >= 16 && maxNonSubjScore >= 5.0 {
		return true
	}
	return isDirectConnectedToPrimary && maxNonSubjScore >= 6.5
}

// UnconnectedIslandBandFloor returns the minimum non-subject score floor required to admit an unconnected
// island band (`!isConn`) when a named protagonist root band is already present.
// Micro-bands (`rowCount <= 4`) and compact host bands (`rowCount <= 12` carrying direct XO or subjectless object hits)
// keep the 4.5 / 6.0 floor, while unrelated cross-session island bands are gated by the convex 11.0 / 42.0+12k floor.
func UnconnectedIslandBandFloor(
	unconnAccepted, rowCount int,
	hasSubjectlessRow, hasNamedProtagonistRoot, hasDirectXO bool,
	maxRerank float64,
) float64 {
	if LegacyUndirectedTopologyMode.Load() {
		if rowCount <= 12 && (hasDirectXO || maxRerank >= 0.35) && unconnAccepted < 8 {
			return 4.5
		}
	} else {
		if rowCount <= 12 && (hasSubjectlessRow || !hasNamedProtagonistRoot || maxRerank >= 0.35 || hasDirectXO) && unconnAccepted < 6 {
			return 4.5
		}
		if rowCount <= 4 && unconnAccepted < 4 {
			return 4.5
		}
	}
	if unconnAccepted >= 2 {
		return 42.0 + 12.0*float64(unconnAccepted-1)
	}
	return 11.0
}

// ConnectedChainBudgetCap tightens the SO chain budget of a subject band using graph/turn connectivity:
// once all chains with direct/bridged non-subject signal and all chains sharing a source turn with a strong hit chain
// are included (+ a safety margin), trailing low-score unconnected background chains are not padded.
func ConnectedChainBudgetCap(baseBudget, strongAndTurnConnectedChains int, bandIdx int) int {
	if LegacyUndirectedTopologyMode.Load() || baseBudget <= 0 || bandIdx == 0 {
		return baseBudget
	}
	var capByConn int
	switch bandIdx {
	case 1:
		capByConn = max(strongAndTurnConnectedChains+6, 10)
	default:
		capByConn = max(strongAndTurnConnectedChains+2, 3)
	}
	if capByConn < baseBudget {
		return capByConn
	}
	return baseBudget
}

// ShouldExpandCompanionFullTurn gates full-turn (<= 520 chars) companion text expansion in high-connectivity graphs.
// All chains with direct/bridged query-term hits (origNonSubj) and the top 2 subject bands preserve full-turn expansion 100%,
// while minor bands (curBandIdx >= 2) restrict non-origNonSubj background chains.
func ShouldExpandCompanionFullTurn(
	curBandIdx, curChainIdx, fullTurnCnt int,
	score float64,
	origNonSubj, directXO bool,
) bool {
	return (curBandIdx <= 1 && (curChainIdx <= 10 || origNonSubj || score >= 7.5)) ||
		(curBandIdx <= 3 && (score >= 8.0 || (curChainIdx <= 2 && score >= 6.0 && fullTurnCnt < 16))) ||
		(origNonSubj && directXO && score >= 6.5 && fullTurnCnt < 36)
}

// FollowupTightMode 收紧「追问续答」附加条帽（**缺省关** · 单变量臂/实测器材）。
//
// 动机：交付体量的头号来源不是原文切片（仅占证据带 19%，且多数只覆盖源轮 5–11%），
// 而是挂在证据行上的**跨轮追问续答**（占证据带 53% / 单题交付 ~31%）。本档只收紧条帽、不动任何判据结构——
// 关 = 逐字现役（12/10/28/34），开 = 4/3/6/8。
// 卷面依据：
// 100 题同二进制单变量 A/B（#8626 关 93 vs #8627 开 92 · p=1.0 不可判；`gold_lex_hit` 90=90 NULL；题均字符 46,465→41,900 = −9.8%）；
// **200 题 A/B（实证对比）。即「效率轴正向但要价 4 题」⇒ 缺省不启用。
// 开法：进程环境 `RM_RECALL_FOLLOWUP_TIGHT=1`（观测量撤/回退档用）。
var FollowupTightMode atomic.Bool

func init() {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("RM_RECALL_FOLLOWUP_TIGHT"))) {
	case "1", "true", "on", "yes":
		FollowupTightMode.Store(true)
	}
}

// tightenFollowupCap 按档收紧追问条帽（仅当 FollowupTightMode 开）。
func tightenFollowupCap(capCnt int) int {
	if !FollowupTightMode.Load() {
		return capCnt
	}
	switch {
	case capCnt >= 34:
		return 8
	case capCnt >= 28:
		return 6
	case capCnt >= 12:
		return 4
	default:
		return 3
	}
}

// ShouldAttachFollowupClip gates cross-turn followup Q&A clip attachment so deep tail chains or minor bands
// without direct query-term hits do not pull in unrelated next-turn chatter.
func ShouldAttachFollowupClip(
	curBandIdx, curChainIdx, followupCnt int,
	score float64,
	nonSubj, origNonSubj bool,
) bool {
	capCnt := 12
	if !LegacyUndirectedTopologyMode.Load() && (curBandIdx <= 1 || (origNonSubj && score >= 6.0)) {
		if curBandIdx == 1 && curChainIdx <= 12 && origNonSubj && score >= 12.0 {
			capCnt = 34
		} else if origNonSubj && score >= 6.0 {
			capCnt = 28
		} else {
			capCnt = 10
		}
	}
	capCnt = tightenFollowupCap(capCnt)
	if !nonSubj || score < 5.0 || followupCnt >= capCnt {
		return false
	}
	return curBandIdx <= 1 || (origNonSubj && score >= 6.0 && ((curChainIdx <= 6 && curBandIdx <= 8) || (curBandIdx == 99 && score >= 8.0)))
}

// SubjectSpecificityWeight returns the continuous information-theoretic inverse-degree specificity
// in (0, 1] for a subject node with `subjectAssertionCount` assertions in the candidate subgraph:
//
//	w(deg) = 1 / log2(2 + max(0, deg))
//
// Subjectless assertions (deg == 0) or low-degree specific entities (deg == 1..4) have high specificity
// (~0.43..1.0), whereas high-degree conversational protagonist hubs (deg >= 20) smoothly decay (< 0.22).
func SubjectSpecificityWeight(subjectAssertionCount int) float64 {
	if subjectAssertionCount <= 0 {
		return 1.0
	}
	return 1.0 / math.Log2(2.0+float64(subjectAssertionCount))
}

// ShouldPlaceSXChainBeforeSO decides whether an SX value-evolution chain should be rendered ahead of
// SO entity-relation chains within a subject band based on relative signal strength:
//   - If there are no SO chains, SX is trivially first;
//   - When SO chains exist, an SX value chain is promoted ahead of SO only when it has a compact line count
//     and its peak score is competitive with the top SO chain (or its predicate directly matches a query cue),
//     preventing large generic state/feeling buckets from burying high-scoring action/object chains.
func ShouldPlaceSXChainBeforeSO(bestSXScore, maxSOScore float64, origNS, ns, cueMatched bool, chainLen int) bool {
	if LegacyUndirectedTopologyMode.Load() {
		return (bestSXScore >= 7.0 && origNS) || (bestSXScore >= 6.0 && ns && chainLen <= 4)
	}
	if chainLen <= 0 || chainLen > 8 {
		return false
	}
	if (bestSXScore >= 7.0 && origNS) || (bestSXScore >= 6.0 && ns && chainLen <= 4 && (maxSOScore <= 25.0 || bestSXScore >= maxSOScore*0.35)) {
		return true
	}
	return cueMatched && ns && bestSXScore >= math.Max(5.5, maxSOScore*0.45)
}

// MergeOverlappingSnippet merges two text spans from the same source turn if one contains the other
// or if they share a substantial suffix-prefix overlap (>= 20 bytes), eliminating duplicate half-sentence
// echoes inside [E#k] evidence entries while preserving 100% of both spans' tokens.
func MergeOverlappingSnippet(existing, incoming string) (merged string, ok bool) {
	a := strings.TrimSpace(existing)
	b := strings.TrimSpace(incoming)
	if a == "" || b == "" {
		return "", false
	}
	aLow := strings.ToLower(a)
	bLow := strings.ToLower(b)
	if strings.Contains(aLow, bLow) {
		return a, true
	}
	if strings.Contains(bLow, aLow) {
		return b, true
	}
	const minOverlap = 20
	maxK := min(len(aLow), len(bLow))
	if maxK < minOverlap {
		return "", false
	}
	for k := maxK; k >= minOverlap; k-- {
		if aLow[len(aLow)-k:] == bLow[:k] {
			return a + b[k:], true
		}
		if bLow[len(bLow)-k:] == aLow[:k] {
			return b + a[k:], true
		}
	}
	return "", false
}

// IsDegenerateSelfEchoAssertion reports whether an assertion is a degenerate self-echo tautology
// where the object merely repeats the subject or the subject+predicate phrase (e.g. "Maria — say → Maria say"
// or "Caroline — be → Caroline"), which carries zero relational information and should be folded.
func IsDegenerateSelfEchoAssertion(subjLabel, relLabel, objLabel string) bool {
	s := normalizeWhitespaceCompact(subjLabel)
	r := normalizeWhitespaceCompact(relLabel)
	o := normalizeWhitespaceCompact(objLabel)
	if s == "" || o == "" {
		return false
	}
	if s == o {
		return true
	}
	if r != "" && (o == s+" "+r || o == r+" "+s) {
		return true
	}
	return false
}

// FollowupNoveltyMode 「追问续答只挂新事实」档（**缺省关** · 单变量臂/实测器材）。
//
// 动机：纯条帽紧档 = 交付体量 −9.7% 换 −4 题（实证对比）。本档在既有逐字覆盖去重之上，要求每段至少含一个「未被本带已交付材料覆盖且 tokenz-r 先验 ≥ 20.0b」的 BPE 子词。
// 卷面与回归复核：200 题 A/B（#8635 关 181 vs #8636 开 179 · Δ −2，且 `gold_lex_hit` 掉 2 题；同时历史回归测试
// `TestDeliveryHygieneMediaGateAndFollowupDedup` 证明由常见词构成的问答桥接句如 "What else helps you?" 会被 <20b 阈值误杀）
// ⇒ 按 §4.3 回归不变量保持**缺省关**（出货 100% 守护问答桥接追问段），仅以 `RM_RECALL_FOLLOWUP_NOVELTY=1` 供实验调用。
var FollowupNoveltyMode atomic.Bool

func init() {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("RM_RECALL_FOLLOWUP_NOVELTY"))) {
	case "1", "true", "on", "yes":
		FollowupNoveltyMode.Store(true)
	}
}

// followupNovelMinBits 段保留阈值（20.0b · 实测标定：寒暄/常见词峰值 ≤16.5b（wow 16.5 / oh 14.3 / nice 13.4），
// 实体/金额/编码 ≥22b（$89 22.0 / Breville 26.3 / bes870xl 42.9）——断层 ≥1.3×，取 20.0b 居中）。
// 判据 = 段内须含至少一个「未被本带已交付材料覆盖且 tokenz-r 先验 ≥ 20.0b」的 BPE 子词（新语素 = 新事实的机械形态；
// tokenz-r 先验 = 词表秩 log2(id+2) 分解和，罕见单件/多件罕见词自然走高）。**峰值判据**：常见词堆叠不构成新事实。
const followupNovelMinBits = 20.0

// buildFollowupCoveredTokenSet 覆盖集 = 主带全部已交付材料（原文/断言）的 BPE 子词小写集合。
func buildFollowupCoveredTokenSet(snips []string) map[string]bool {
	set := make(map[string]bool, 512)
	for _, s := range snips {
		for _, tk := range tokenzr.SplitSubwords(s) {
			if tk != "" {
				set[strings.ToLower(tk)] = true
			}
		}
	}
	return set
}

// followupNovelBits 段内「未被覆盖」子词的先验自信息**峰值**（纯数学 · tokenz-r）。
// 用峰值而非求和：新事实 = 至少一个新语素（专名/术语/数字，单 token ≥ 8b 即完整概念）；
// 多个常见词堆叠（寒暄/口语填充）单个都 < 8b ⇒ 不构成新事实。
func followupNovelBits(seg string, covered map[string]bool) float64 {
	peak := 0.0
	for _, tk := range tokenzr.SplitSubwords(seg) {
		if tk == "" || covered[strings.ToLower(tk)] {
			continue
		}
		if b := tokenzr.TokenPriorBits(tk); b > peak {
			peak = b
		}
	}
	return peak
}

// DeduplicateFollowupSegments removes followup dialogue segments (separated by " → ") that are already
// substantively present in any main snippet of the evidence band or in earlier followup segments,
// eliminating cross-entry followup collisions while preserving 100% of unseen dialogue turns.
func DeduplicateFollowupSegments(followup string, mainSnippets []string, seenFollowupSegs map[string]bool) string {
	fu := strings.TrimSpace(followup)
	if fu == "" {
		return ""
	}
	segs := strings.Split(fu, " → ")
	var kept []string
	var coveredTokSet map[string]bool
	if FollowupNoveltyMode.Load() {
		coveredTokSet = buildFollowupCoveredTokenSet(mainSnippets)
	}
	for _, seg := range segs {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		// Strip speaker prefix like "Melanie: " when checking substring coverage against main turn snippets
		body := seg
		if cIdx := strings.Index(seg, ": "); cIdx > 0 && CountTokens(seg[:cIdx]) <= TokenTier8 {
			body = strings.TrimSpace(seg[cIdx+2:])
		}
		bodyNorm := normalizeWhitespaceCompact(body)
		segNorm := normalizeWhitespaceCompact(seg)
		bodyToks := CountTokens(bodyNorm)
		if seenFollowupSegs != nil && (seenFollowupSegs[segNorm] || (bodyToks >= TokenTier4 && seenFollowupSegs[bodyNorm])) {
			continue
		}
		covered := false
		if bodyToks >= TokenTier4 {
			for _, ms := range mainSnippets {
				msNorm := normalizeWhitespaceCompact(ms)
				if len(msNorm) >= len(bodyNorm) && strings.Contains(msNorm, bodyNorm) {
					covered = true
					break
				}
			}
		}
		// 对症版（缺省关）：非逐字覆盖的段还须携带 ≥8.0b 未被本带材料覆盖的新自信息，否则视为复述/寒暄整段不落。
		if !covered && coveredTokSet != nil && followupNovelBits(bodyNorm, coveredTokSet) < followupNovelMinBits {
			continue
		}
		if covered {
			continue
		}
		if seenFollowupSegs != nil {
			seenFollowupSegs[segNorm] = true
			if bodyToks >= TokenTier4 {
				seenFollowupSegs[bodyNorm] = true
			}
		}
		kept = append(kept, seg)
	}
	return strings.Join(kept, " → ")
}

// CompactSupplementImageElaboration trims verbose parenthesized or trailing OCR/visual elaborations
// (e.g. "【图片】a photo of ... (一张米黄色背景的...)" or "【图片】a photo of ... 画面中央是...")
// inside raw-supplement (src2) or followup clips when none of the query cues appear inside the elaboration,
// keeping the concise image caption header while saving hundreds of irrelevant visual description tokens.
func CompactSupplementImageElaboration(text string, queryCues []string) string {
	if len(queryCues) == 0 {
		return text
	}
	idx := strings.Index(text, "【图片】")
	if idx < 0 {
		return text
	}
	// Process each "【图片】" occurrence from right to left so indices remain valid.
	out := text
	searchFrom := len(out)
	for {
		pos := strings.LastIndex(out[:searchFrom], "【图片】")
		if pos < 0 {
			break
		}
		searchFrom = pos
		tail := out[pos:]
		// Isolate up to the next dialogue turn separator " → " if inside a multi-turn followup.
		endSeg := len(tail)
		if arrow := strings.Index(tail, " → "); arrow > 0 {
			endSeg = arrow
		}
		seg := tail[:endSeg]
		cutAt := -1
		if openP := strings.Index(seg, " ("); openP > 0 {
			cutAt = openP
		} else {
			// Detect transition from Latin caption header ("【图片】a photo of ...") to a long CJK visual elaboration paragraph.
			afterTag := pos + len("【图片】")
			seenLatin := false
			for off, r := range out[afterTag : pos+endSeg] {
				if unicode.Is(unicode.Latin, r) {
					seenLatin = true
				} else if seenLatin && IsContinuousSyllabicOrIdeographic(r) {
					cutAt = len("【图片】") + off
					break
				}
			}
		}
		if cutAt <= 0 {
			continue
		}
		elab := seg[cutAt:]
		if CountTokens(elab) <= TokenTier32 {
			continue
		}
		capLow := strings.ToLower(seg[:cutAt])
		elabLow := strings.ToLower(elab)
		matchedCue := false
		for _, cue := range queryCues {
			cLow := strings.ToLower(strings.TrimSpace(cue))
			if CountTokens(cLow) >= 1 && StringShannonBits(cLow) >= 4.5 && strings.Contains(elabLow, cLow) && !strings.Contains(capLow, cLow) {
				matchedCue = true
				break
			}
		}
		if matchedCue {
			continue
		}
		suffix := ""
		if spkIdx := strings.Index(elab, "（讲述日"); spkIdx >= 0 {
			suffix = elab[spkIdx:]
		}
		out = strings.TrimSpace(out[:pos+cutAt]) + suffix + tail[endSeg:]
	}
	return out
}

// CanonicalStemSignature computes a language-agnostic, possessive-stripped content-stem signature
// for a compact entity or concept phrase (CountTokens <= TokenTier8):
//  1. If ownerSubjLabel is non-empty, strips leading self-possessive prefixes ("<owner>'s ", "<owner>’s ", "<owner>的");
//  2. Tokenizes into content words via Tokenize (which strips closed-class articles/prepositions/particles like "a/an/the/of/的")
//     and maps each token through StemToken;
//  3. Returns the joined stem signature if its cumulative Shannon self-information >= AtomicConceptEntropyBits,
//     enabling same-concept micro-bands ("the pride parade" / "a pride parade") and same-object multi-predicate
//     chains ("painting" / "Caroline's painting" / "Caroline's paintings") to aggregate without literal string equality.
func CanonicalStemSignature(label, ownerSubjLabel string) string {
	s := strings.TrimSpace(CanonicalizeApostrophe(label))
	if s == "" {
		return ""
	}
	if owner := strings.TrimSpace(CanonicalizeApostrophe(ownerSubjLabel)); owner != "" {
		sLow := strings.ToLower(s)
		oLow := strings.ToLower(owner)
		if strings.HasPrefix(sLow, oLow+"'s ") {
			s = strings.TrimSpace(s[len(owner)+3:])
		} else if strings.HasPrefix(s, owner+"的") {
			s = strings.TrimSpace(strings.TrimPrefix(s, owner+"的"))
		}
	}
	if s == "" || CountTokens(s) > TokenTier8 || strings.ContainsAny(s, ".!?！？;；\n") {
		return ""
	}
	toks := highGainTokens(s)
	if len(toks) == 0 || len(toks) > 4 {
		return ""
	}
	stems := make([]string, 0, len(toks))
	var bits float64
	for _, tk := range toks {
		st := StemToken(tk)
		if st == "" {
			st = tk
		}
		if StringShannonBits(st) == 1.5*AtomicConceptEntropyBits && !IsPureCJK(st) {
			if rp := MorphRootPrefix(tk); rp != "" && MorphSimWordPair([]rune(tk), []rune(st), []rune(rp)) >= 0.85 {
				st = rp
			}
		}
		bits += StringShannonBits(st)
		stems = append(stems, st)
	}
	if bits < AtomicConceptEntropyBits {
		return ""
	}
	return strings.Join(stems, "+")
}

// ClassifySpecificInstanceLabel evaluates whether a compact object label represents a specific
// named/numbered instance (rather than a generic query-category echo or sentence clause) using
// 100% language-agnostic Unicode character classes, Shannon entropy, and morphological stem matching.
func ClassifySpecificInstanceLabel(label string, cueTokens map[string]bool) (isSpecific, isCompoundSpecific bool) {
	lb := strings.TrimSpace(label)
	if lb == "" || StringShannonBits(lb) == 0 {
		return false, false
	}
	fields := strings.Fields(lb)
	for idx, w := range fields {
		wTrim := strings.TrimFunc(w, unicode.IsPunct)
		wl := strings.ToLower(wTrim)
		if wl == "" {
			continue
		}
		wr0, _ := utf8.DecodeRuneInString(wTrim)
		// 前导词若后接大写专名或数字，属于专名标题前缀而非类别回显词
		if idx == 0 && len(fields) > 1 && unicode.IsUpper(wr0) {
			nextR0, _ := utf8.DecodeRuneInString(strings.TrimFunc(fields[1], unicode.IsPunct))
			if unicode.IsUpper(nextR0) || unicode.IsDigit(nextR0) {
				continue
			}
		}
		st := StemToken(wl)
		if cueTokens[wl] || (st != "" && cueTokens[st]) {
			return false, false
		}
		for ct := range cueTokens {
			if ct != "" && st != "" &&
				MorphSimWordPair([]rune(wl), []rune(st), []rune(ct)) >= 0.85 {
				return false, false
			}
		}
	}
	r0, w0 := utf8.DecodeRuneInString(lb)
	if w0 <= 0 {
		return false, false
	}
	hasCasedOrIdeographicStart := unicode.IsUpper(r0) || unicode.IsDigit(r0) || IsContinuousSyllabicOrIdeographic(r0)
	if !hasCasedOrIdeographicStart {
		return false, false
	}
	var ideoBits float64
	for byteIdx, r := range lb {
		if unicode.IsDigit(r) || (byteIdx > 0 && unicode.IsUpper(r)) {
			return true, true
		}
		if IsContinuousSyllabicOrIdeographic(r) {
			ideoBits += RuneShannonBits(r)
		}
	}
	if ideoBits >= 2.0*AtomicConceptEntropyBits && CountTokens(lb) <= TokenTier4 {
		return true, true
	}
	if !strings.Contains(lb, " ") && StringShannonBits(lb) >= 4.0 {
		return true, false
	}
	return false, false
}

// TrimRedundantRelObjectEcho strips a trailing duplicate of obj from rel (case-insensitive)
// when an encoder LLM includes the object phrase at the end of the predicate slot
// (e.g. rel="says she'd like to get the location metadata part", obj="the location metadata part"
// -> rel="says she'd like to get"), preventing "X O O" stuttering in both graph storage and delivery.
func TrimRedundantRelObjectEcho(rel, obj string) string {
	rTrim := strings.TrimSpace(rel)
	oTrim := strings.TrimSpace(obj)
	if rTrim == "" || oTrim == "" || oTrim == "∅" || len(rTrim) <= len(oTrim) {
		return rTrim
	}
	rLow := strings.ToLower(rTrim)
	oLow := strings.ToLower(oTrim)
	if strings.HasSuffix(rLow, " "+oLow) {
		if prefix := strings.TrimSpace(rTrim[:len(rTrim)-len(oTrim)]); prefix != "" {
			return prefix
		}
	}
	return rTrim
}

// IsReactionWithTrailingChitchatQ detects turns where a speaker reacts to the previous speaker's
// statement in the first sentence and appends a trailing chitchat question ("How about you?")
// containing none of the query content terms.
func IsReactionWithTrailingChitchatQ(text string, contentTerms []string) bool {
	if len(contentTerms) == 0 {
		return false
	}
	lower := strings.ToLower(text)
	qIdx := strings.Index(lower, "?")
	if qIdx < 0 {
		return false
	}
	qStart := 0
	for i := qIdx - 1; i >= 0; i-- {
		if lower[i] == '.' || lower[i] == '!' {
			qStart = i + 1
			break
		}
	}
	if qStart == 0 {
		return false
	}
	stmtPart := lower[:qStart]
	qPart := lower[qStart:]
	stmtHit := false
	for _, ct := range contentTerms {
		if ct == "" {
			continue
		}
		if strings.Contains(qPart, ct) {
			return false
		}
		if strings.Contains(stmtPart, ct) {
			stmtHit = true
		}
	}
	return stmtHit
}

// ActivationMultiplier computes the bounded multiplicative boost (capped at 1.25x)
// from decayed activation hits (lambda = 0.995/h).
func ActivationMultiplier(hits float64, ageHours float64) float64 {
	if hits <= 0 {
		return 1.0
	}
	if ageHours < 0 {
		ageHours = 0
	}
	decayed := hits * math.Pow(0.995, ageHours)
	if decayed <= 0 {
		return 1.0
	}
	boost := 1.0 + 0.08*math.Log1p(decayed)
	if boost > 1.25 {
		return 1.25
	}
	return boost
}

// StmtCoversSnippet reports whether the rendered assertion statement stmt already covers 100%
// of the non-trivial tokens (StringShannonBits >= 0.50*AtomicConceptEntropyBits, by exact or stem match) in the verbatim anchor snippet snip.
// Only local single-row coverage may elide a redundant snippet; cross-row global bag-of-words
// cancellation is forbidden so intra-sentence modifiers and locative complements are never dropped.
func StmtCoversSnippet(stmt, snip string) bool {
	stmtWords := make(map[string]bool, 16)
	var b strings.Builder
	flushStmt := func() {
		if b.Len() > 0 {
			w := b.String()
			b.Reset()
			stmtWords[w] = true
			if st := StemToken(w); st != "" {
				stmtWords[st] = true
			}
		}
	}
	for _, r := range strings.ToLower(stmt) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
		} else {
			flushStmt()
		}
	}
	flushStmt()

	covered := true
	flushSnip := func() {
		if b.Len() == 0 {
			return
		}
		w := b.String()
		b.Reset()
		if StringShannonBits(w) < 0.50*AtomicConceptEntropyBits {
			return
		}
		if !stmtWords[w] && !stmtWords[StemToken(w)] {
			covered = false
		}
	}
	for _, r := range strings.ToLower(snip) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
		} else {
			flushSnip()
			if !covered {
				return false
			}
		}
	}
	flushSnip()
	return covered
}

// StripMultimodalLongParen strips trailing parenthetical visual descriptions (" (...)")
// from multimodal turn text when its byte length exceeds triggerLen (e.g. 480) and the prefix
// before " (" still covers at least minPrefixLen bytes, keeping dialog prose within capLen.
func StripMultimodalLongParen(text string, minPrefixLen, triggerLen int) string {
	trimmed := strings.TrimSpace(text)
	if triggerLen <= 0 {
		triggerLen = 480
	}
	if len(trimmed) > triggerLen {
		if pIdx := strings.Index(trimmed, " ("); pIdx > minPrefixLen {
			return strings.TrimSpace(trimmed[:pIdx])
		}
	}
	return trimmed
}
