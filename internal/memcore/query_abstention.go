// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

// SharesEdgeIdeograph reports whether two tokens containing ideographs overlap by sharing
// the last rune of one with the first rune of the other (sliding 2-gram overlap of a single 3-rune word).
func SharesEdgeIdeograph(a, b string) bool {
	if StringShannonBits(a) < AtomicConceptEntropyBits || StringShannonBits(b) < AtomicConceptEntropyBits {
		return false
	}
	firstA, wFirstA := utf8.DecodeRuneInString(a)
	lastA, _ := utf8.DecodeLastRuneInString(a)
	firstB, wFirstB := utf8.DecodeRuneInString(b)
	lastB, _ := utf8.DecodeLastRuneInString(b)
	if wFirstA <= 0 || wFirstA >= len(a) || wFirstB <= 0 || wFirstB >= len(b) {
		return false
	}
	hasIdeo := false
	for _, r := range a {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) {
			hasIdeo = true
			break
		}
	}
	if !hasIdeo {
		return false
	}
	return lastA == firstB || lastB == firstA
}

// ReduceIndependentQueryConcepts collapses overlapping sub-tokens (e.g. CamelCase subwords or sliding 2-grams)
// into non-overlapping independent concept slots, returning the total independent concept count, the number of
// hit concepts, and the maximum consecutive missed concept streak.
// This places the denominator and numerator of token coverage on the exact same scale as assertion-level
// co-occurrence across all scripts without language-specific threshold ladders.
func ReduceIndependentQueryConcepts(origTokens []string, corpusHitTokens map[string]bool) (conceptCnt, hitConceptCnt, maxMissStreak int) {
	type conceptSlot struct {
		rep        string
		hit        bool
		subHitUsed bool
	}
	slots := make([]conceptSlot, 0, len(origTokens))
	for _, tk := range origTokens {
		hit := corpusHitTokens[tk]
		merged := false
		for i := range slots {
			prev := slots[i].rep
			if (len(prev) > len(tk) && strings.Contains(prev, tk)) ||
				(len(tk) > len(prev) && strings.Contains(tk, prev)) {
				// If an un-hit compound slot (e.g. "春节前后", hit=false) contains a shorter sub-token that matched
				// (e.g. "前后", hit=true) with >= 2*AtomicConceptEntropyBits (16 bits) of unmatched stem entropy remaining,
				// keep the missing concept as an un-hit slot instead of erasing it (zero character-count magic numbers).
				prevBits := StringShannonBits(prev)
				tkBits := StringShannonBits(tk)
				if prevBits >= tkBits+2.0*AtomicConceptEntropyBits && !slots[i].hit && hit {
					if slots[i].subHitUsed {
						merged = true
						break
					}
					slots[i].subHitUsed = true
					continue
				}
				if tkBits >= prevBits+2.0*AtomicConceptEntropyBits && slots[i].hit && !hit {
					continue
				}
				if len(tk) > len(prev) {
					slots[i].rep = tk
				}
				if hit {
					slots[i].hit = true
				}
				merged = true
				break
			}
		}
		if !merged && len(slots) > 0 && slots[len(slots)-1].hit == hit && SharesEdgeIdeograph(slots[len(slots)-1].rep, tk) {
			prevRep := slots[len(slots)-1].rep
			lastPrev, _ := utf8.DecodeLastRuneInString(prevRep)
			firstCur, wFirstCur := utf8.DecodeRuneInString(tk)
			if StringShannonBits(prevRep) <= 3.0*AtomicConceptEntropyBits &&
				StringShannonBits(tk) <= 3.0*AtomicConceptEntropyBits &&
				wFirstCur > 0 && lastPrev == firstCur {
				slots[len(slots)-1].rep = prevRep + tk[wFirstCur:]
				merged = true
			}
		}
		if !merged {
			slots = append(slots, conceptSlot{rep: tk, hit: hit})
		}
	}
	conceptCnt = len(slots)
	curMiss := 0
	for _, s := range slots {
		if s.hit {
			hitConceptCnt++
			curMiss = 0
		} else {
			curMiss++
			if curMiss > maxMissStreak {
				maxMissStreak = curMiss
			}
		}
	}
	return conceptCnt, hitConceptCnt, maxMissStreak
}

// ShouldAbstainByTokenCoverage implements a language-agnostic, purely statistical information-coverage gate
// to detect out-of-scope abstention queries where core high-specificity query concepts are absent from the scope
// and only generic background tokens matched.
func ShouldAbstainByTokenCoverage(
	origTokens []string,
	corpusHitTokens map[string]bool,
	maxOrigNonSubjHits int,
	maxRerankScore float64,
	rerankRan bool,
) bool {
	if len(origTokens) < 3 {
		return false
	}
	conceptN, conceptHits, maxMissStreak := ReduceIndependentQueryConcepts(origTokens, corpusHitTokens)
	if conceptN < 3 {
		return false
	}
	// Short-to-medium query complete vacuum guard (3 <= conceptN < 5):
	// When 100% of independent query concepts are absent from both graph assertions and source texts
	// (conceptHits == 0 && maxOrigNonSubjHits == 0) and Cross-Encoder did not confirm relevance (< 0.25),
	// any candidate rows came solely from low-similarity dense-vector fallback noise and must abstain.
	if conceptN < 5 {
		if conceptHits == 0 && maxOrigNonSubjHits == 0 && (!rerankRan || maxRerankScore < 0.25) {
			return true
		}
		return false
	}
	if maxOrigNonSubjHits >= 3 {
		return false
	}
	cov := float64(conceptHits) / float64(conceptN)
	rowCov := float64(maxOrigNonSubjHits) / float64(conceptN)
	unhitRatio := 1.0 - cov
	streakRatio := float64(maxMissStreak) / float64(conceptN)

	isLowCohesion := maxOrigNonSubjHits <= 2 &&
		((rowCov <= 0.25 && cov <= 0.50 && streakRatio >= 0.20) ||
			(rowCov <= 0.35 && unhitRatio >= 0.45 && streakRatio >= 0.28))

	if rerankRan {
		rerankCeiling := 0.25
		if conceptN < len(origTokens) {
			rerankCeiling = 0.78
		}
		if isLowCohesion {
			rerankCeiling = 0.88
			if (cov <= 0.35 && streakRatio >= 0.32) || (unhitRatio >= 0.45 && streakRatio >= 0.28) {
				rerankCeiling = 0.95
			}
		}
		if maxRerankScore >= rerankCeiling {
			return false
		}
	}

	if conceptHits == 0 && maxOrigNonSubjHits == 0 {
		return true
	}
	if isLowCohesion {
		return true
	}
	return false
}

// StripTrailingFormatInstruction removes trailing output-format directive clauses appended after a question mark
// when the trailing clause contains no question mark of its own and embeds a parenthesized quoted exemplar
// (e.g., "\nAnswer with the exact amount (e.g., \"$45.00\") only.").
// Purely formal punctuation/structure check (zero semantic wordlist, 100% language-agnostic).
func StripTrailingFormatInstruction(query string) string {
	q := strings.TrimSpace(query)
	if q == "" {
		return ""
	}
	qIdx := -1
	qLen := 0
	for i, r := range q {
		if r == '?' || r == '？' {
			qIdx = i
			qLen = utf8.RuneLen(r)
			break
		}
	}
	if qIdx <= 0 {
		return q
	}
	stem := strings.TrimSpace(q[:qIdx+qLen])
	if StringShannonBits(stem) < AtomicConceptEntropyBits*1.5 {
		return q
	}
	tail := strings.TrimSpace(q[qIdx+qLen:])
	if tail == "" || strings.Contains(tail, "?") || strings.Contains(tail, "？") {
		return q
	}
	openP := strings.IndexByte(tail, '(')
	closeP := strings.LastIndexByte(tail, ')')
	if openP >= 0 && closeP > openP+2 {
		inParen := tail[openP+1 : closeP]
		if strings.ContainsAny(inParen, "\"'“”") {
			return stem
		}
	}
	return q
}

// StripInlineMCQOptions removes trailing inline multiple-choice option blocks (e.g. "A. ... B. ... C. ... D. ...")
// from a query string so distractor option tokens do not dilute stem concept coverage in abstention checks.
func StripInlineMCQOptions(query string) string {
	q := strings.TrimSpace(query)
	if q == "" {
		return ""
	}
	idxA := -1
	for i := 0; i+2 < len(q); i++ {
		if (q[i] == 'A' || q[i] == 'a') && (q[i+1] == '.' || q[i+1] == ')' || strings.HasPrefix(q[i+1:], "、")) {
			if i == 0 || q[i-1] == ' ' || q[i-1] == '\n' || q[i-1] == '\t' || q[i-1] > 127 {
				idxA = i
				break
			}
		}
	}
	if idxA <= 0 {
		return q
	}
	tail := q[idxA:]
	hasB := strings.Contains(tail, "B.") || strings.Contains(tail, "B)") || strings.Contains(tail, "B、") ||
		strings.Contains(tail, "b.") || strings.Contains(tail, "b)")
	if !hasB {
		return q
	}
	stem := strings.TrimSpace(q[:idxA])
	if StringShannonBits(stem) < AtomicConceptEntropyBits {
		return q
	}
	return stem
}

// ExtractNonInlineQueryOptions filters top-level MCQ options, returning cleaned option strings (bounded at <=16 items,
// <=TokenTier256 tokens each) that are not already inlined verbatim inside query.
func ExtractNonInlineQueryOptions(query string, options []string) []string {
	if len(options) == 0 {
		return nil
	}
	cleaned := make([]string, 0, min(len(options), TokenTier16))
	inlineCnt := 0
	for _, raw := range options {
		o := strings.TrimSpace(raw)
		if o == "" {
			continue
		}
		if CountTokens(o) > TokenTier256 {
			o = strings.TrimSuffix(TruncateToTokens(o, TokenTier256), "…")
		}
		cleaned = append(cleaned, o)
		pfx := strings.TrimSuffix(TruncateToTokens(o, TokenTier4), "…")
		if pfx != "" && strings.Contains(query, pfx) {
			inlineCnt++
		}
		if len(cleaned) >= TokenTier16 {
			break
		}
	}
	if len(cleaned) == 0 || inlineCnt == len(cleaned) {
		return nil
	}
	return cleaned
}

// CalibrateDeliverableItemScores maps raw band/supplemental scores into calibrated similarity scores in [0, 1]
// while enforcing monotonic non-increasing order along the delivered topological sequence.
//   - Status notes ("note:*") always receive 0.
//   - Subject and head bands use the rational saturation kernel 0.35 + 0.63 * s / (s + 12.0), mapping
//     0.5 base-only noise to ~0.375, moderate hits (s=12) to ~0.665, and strong multi-signal bands (s>=60) to 0.88..0.98.
//   - Walk-ordered follower bands and tail supplemental bands (evidence / xo / misc / selffeed / src / src2 / episode / cluster)
//     are monotonically envelope-clamped below their predecessor so Engine.Search strictly satisfies descending score order.
func CalibrateDeliverableItemScores(rawScores []float64, itemIDs []string) []float64 {
	n := len(rawScores)
	if n == 0 {
		return nil
	}
	out := make([]float64, n)
	prev := 0.99
	for i := 0; i < n; i++ {
		id := ""
		if i < len(itemIDs) {
			id = strings.TrimSpace(itemIDs[i])
		}
		if strings.HasPrefix(id, "note:") {
			out[i] = 0
			continue
		}
		raw := rawScores[i]
		if math.IsNaN(raw) || raw < 0 {
			raw = 0
		}
		isSupplement := id == BandIDEvidenceLines || id == BandIDEvidenceArtifacts || id == BandIDXO || id == BandIDMisc ||
			id == BandIDSelfFeed || id == BandIDSrc || id == BandIDSrc2 ||
			strings.HasPrefix(id, EpisodeBandID) || strings.HasPrefix(id, ClusterIDPrefix)
		var cal float64
		if id == BandIDHead {
			cal = 0.98
		} else if isSupplement {
			switch {
			case id == BandIDEvidenceLines:
				cal = math.Min(prev-0.005, 0.88)
			case id == BandIDEvidenceArtifacts:
				cal = math.Min(prev-0.005, 0.87)
			case id == BandIDSelfFeed:
				cal = math.Min(prev-0.005, 0.86)
			case id == BandIDXO:
				cal = math.Min(prev-0.008, 0.78)
			case id == BandIDMisc:
				cal = math.Min(prev-0.008, 0.65)
			case id == BandIDSrc:
				if i == 0 {
					cal = 0.72
				} else {
					cal = math.Min(prev-0.008, 0.62)
				}
			case id == BandIDSrc2:
				cal = math.Min(prev-0.008, 0.58)
			case strings.HasPrefix(id, EpisodeBandID):
				epSim := 0.35 + 0.50*(raw/(raw+12.0))
				cal = math.Min(prev-0.005, epSim)
			default:
				cal = math.Min(prev-0.008, 0.50)
			}
		} else {
			cal = 0.35 + 0.63*(raw/(raw+12.0))
		}
		if i > 0 && cal > prev-0.002 {
			cal = prev - 0.002
		}
		if cal < 0.05 {
			cal = 0.05
		}
		if cal > 0.98 {
			cal = 0.98
		}
		cal = math.Round(cal*10000) / 10000
		out[i] = cal
		prev = cal
	}
	return out
}
