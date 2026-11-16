// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// L2 segment delivery: assemble the hits of one harness turn back into a readable
// block.
//
// Segment units fixed the pulse side (sharp addresses, bounded width) but a naive
// delivery would hand the reader isolated one-sentence fragments in score order —
// the memory analogue of a shredded page. Delivery therefore groups hits by their
// turn reference, orders the turn's segments the way they were spoken, and renders
// a contiguous window around the winning segment. The window is bounded by the
// caller's char budget, so a turn can never monopolise the delivery.
package api

import (
	"strconv"
	"strings"

	"github.com/rocareer/rolling-memory/internal/thinclient"
)

// episodicTurnGroup is one harness turn's hits plus the turn's ordered segments.
type episodicTurnGroup struct {
	ref        string
	rawPointer string
	hits       []map[string]any
	segments   []string
	hitIndex   int
	score      float64
	unitID     string
	authors    []string
}

// renderEpisodicWindow renders a contiguous window of the turn's segments around
// the hit index. The hit segment is always included; neighbours are added in both
// directions while the char budget allows, so the reader sees context rather than
// an isolated line. Returns the text and the inclusive window bounds.
func renderEpisodicWindow(segments []string, hitIndex, maxChars int) (string, int, int) {
	if len(segments) == 0 {
		return "", 0, 0
	}
	if hitIndex < 0 {
		hitIndex = 0
	}
	if hitIndex >= len(segments) {
		hitIndex = len(segments) - 1
	}
	if maxChars <= 0 {
		return segments[hitIndex], hitIndex, hitIndex
	}
	low, high := hitIndex, hitIndex
	used := len(segments[hitIndex])
	for {
		grew := false
		if high+1 < len(segments) && used+1+len(segments[high+1]) <= maxChars {
			high++
			used += 1 + len(segments[high])
			grew = true
		}
		if low-1 >= 0 && used+1+len(segments[low-1]) <= maxChars {
			low--
			used += 1 + len(segments[low])
			grew = true
		}
		if !grew {
			break
		}
	}
	return strings.Join(segments[low:high+1], "\n"), low, high
}

// splitTurnRefSegment splits a `@ref:...#<k>` / `@ref:...#<k>.<c>` pointer into its
// turn reference and segment index (`k`; the optional `.c` chunk suffix stays inside
// the segment). A pointer without a segment suffix names a whole turn (`k = 0`).
func splitTurnRefSegment(pointer string) (string, int) {
	trimmed := strings.TrimSpace(pointer)
	index := strings.LastIndex(trimmed, "#")
	if index <= 0 {
		return trimmed, 0
	}
	tail := trimmed[index+1:]
	digits := 0
	for digits < len(tail) && tail[digits] >= '0' && tail[digits] <= '9' {
		digits++
	}
	if digits == 0 {
		return trimmed, 0
	}
	segmentIndex, err := strconv.Atoi(tail[:digits])
	if err != nil || segmentIndex < 0 {
		return trimmed, 0
	}
	if rest := tail[digits:]; rest != "" {
		if len(rest) < 2 || rest[0] != '.' {
			return trimmed, 0
		}
		for _, char := range rest[1:] {
			if char < '0' || char > '9' {
				return trimmed, 0
			}
		}
	}
	return trimmed[:index], segmentIndex
}

// turnWindowBudget derives one turn's window budget from the request budget and the
// number of turns competing for it (no fixed per-turn cap).
func turnWindowBudget(totalChars, turns int) int {
	if totalChars <= 0 {
		return 0
	}
	if turns <= 1 {
		return totalChars
	}
	return totalChars / turns
}

// shortTurnRef renders a compact band title for a turn reference: the trailing
// identifier (message id / turn index) is enough to tell turns apart.
func shortTurnRef(ref string) string {
	trimmed := strings.TrimSpace(ref)
	if index := strings.LastIndex(trimmed, ":"); index > 0 && index+1 < len(trimmed) {
		return trimmed[index+1:]
	}
	return trimmed
}

// bestTurnSegmentIndex selects the best focal segment inside `segments`:
//   - If `rawPointer` carries an explicit `#<k>` segment suffix, `fallbackIdx` (`k`) is used directly;
//   - If `rawPointer` is a legacy turn-level pointer without `#<k>` (where `splitTurnRefSegment`
//     defaults to 0 = the user prompt), it selects the segment with the highest query-token overlap
//     (breaking ties toward the final assistant conclusion segment) so delivery never stops at a
//     bare `[user] 继续` line while dropping the assistant's actual answer.
func bestTurnSegmentIndex(rawPointer string, segments []string, query string, fallbackIdx int) int {
	if len(segments) <= 1 {
		return 0
	}
	trimmed := strings.TrimSpace(rawPointer)
	if sharp := strings.LastIndex(trimmed, "#"); sharp > 0 && sharp < len(trimmed)-1 {
		if fallbackIdx >= 0 && fallbackIdx < len(segments) {
			return fallbackIdx
		}
	}
	qWords := thinclient.SplitWords(strings.ToLower(strings.TrimSpace(query)))
	seenW := make(map[string]bool, len(qWords))
	uniqWords := make([]string, 0, len(qWords))
	for _, w := range qWords {
		w = strings.TrimSpace(w)
		if w == "" || seenW[w] {
			continue
		}
		seenW[w] = true
		uniqWords = append(uniqWords, w)
	}
	bestIdx := len(segments) - 1
	bestScore := -1
	for idx, seg := range segments {
		lowSeg := strings.ToLower(seg)
		hits := 0
		for _, w := range uniqWords {
			if strings.Contains(lowSeg, w) {
				hits++
			}
		}
		// Slight structural preference for assistant conclusion segments (idx > 0) when token overlap ties.
		score := hits * 100
		if idx > 0 {
			score += 10 + idx
		}
		if score > bestScore {
			bestScore = score
			bestIdx = idx
		}
	}
	return bestIdx
}

// distillTurnWindowWithRB uses the cloud engine's query-guided distilled sentences (`rbDistilled`,
// reconstructed from `KeptSentenceIndices`) to filter out low-energy filler lines inside an oversized
// turn window while preserving verbatim line formatting, punctuation, and speaker prefixes.
func distillTurnWindowWithRB(windowText string, rbDistilled string, maxChars int) string {
	if maxChars <= 0 || len(windowText) <= maxChars {
		return windowText
	}
	rbTrim := strings.TrimSpace(rbDistilled)
	if rbTrim == "" || strings.HasPrefix(rbTrim, "@ref:") {
		return windowText
	}
	lines := strings.Split(windowText, "\n")
	if len(lines) <= 3 {
		return windowText
	}
	rbLower := strings.ToLower(rbTrim)
	kept := make([]string, 0, len(lines))
	used := 0
	for idx, line := range lines {
		trimLine := strings.TrimSpace(line)
		if trimLine == "" {
			continue
		}
		isSpeakerHead := idx == 0 || strings.HasPrefix(trimLine, "[user]") || strings.HasPrefix(trimLine, "[assistant]")
		matchedRB := false
		words := thinclient.SplitWords(strings.ToLower(trimLine))
		hitWords := 0
		minHits := 2
		if len(words) <= 1 {
			minHits = 1
		}
		for _, w := range words {
			if w != "" && strings.Contains(rbLower, w) {
				hitWords++
				if hitWords >= minHits {
					matchedRB = true
					break
				}
			}
		}
		if isSpeakerHead || matchedRB {
			if used+len(line)+1 > maxChars && len(kept) >= 2 {
				break
			}
			kept = append(kept, line)
			used += len(line) + 1
		}
	}
	if len(kept) == 0 {
		return windowText
	}
	return strings.Join(kept, "\n")
}
