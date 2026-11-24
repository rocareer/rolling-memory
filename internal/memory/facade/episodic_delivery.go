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
package facade

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/rocareer/rolling-memory/internal/memory/localvault"
)

// episodicTurnGroup is one harness turn's hits plus the turn's ordered segments.
type episodicTurnGroup struct {
	ref        string
	rawPointer string
	hits       []map[string]any
	segments   []string
	hitIndex   int
	score      float64
	direct     float64
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

// deliveryCharBudgetCandidates narrows ranked candidates to the nodes the
// delivered page actually carries. Pagination has already selected result bands
// by offset and caps, so matching the remaining node ids reuses that decision
// instead of recomputing a second page window.
func deliveryCharBudgetCandidates(bands []map[string]any, candidates []deliveredCandidate) []deliveredCandidate {
	if len(bands) == 0 || len(candidates) == 0 {
		return nil
	}
	deliveredNodeIDs := make(map[string]int, len(bands))
	for _, band := range bands {
		nodes, _ := band["nodes"].([]map[string]any)
		for _, node := range nodes {
			id, _ := node["id"].(string)
			deliveredNodeIDs[id]++
		}
	}
	delivered := make([]deliveredCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		nodes, _ := candidate.band["nodes"].([]map[string]any)
		if len(nodes) == 0 {
			continue
		}
		id, _ := nodes[0]["id"].(string)
		if deliveredNodeIDs[id] == 0 {
			continue
		}
		deliveredNodeIDs[id]--
		delivered = append(delivered, candidate)
	}
	return delivered
}

// applyDeliveryCharBudget decides the character budget at the delivery boundary.
// The input is the set ranking actually delivers, not every candidate: a candidate
// outside the adaptive window never competes for the cap, and a delivered set whose
// full source text fits the configured cap is kept whole. Only a delivery that
// genuinely exceeds the cap is cut by the existing per-band safety split. The cut is
// mirrored onto the band node and the raw hit so every surface carries the same body.
func applyDeliveryCharBudget(candidates []deliveredCandidate, query string, totalChars int) {
	if totalChars <= 0 || len(candidates) == 0 {
		return
	}
	total := 0
	for i := range candidates {
		total += utf8.RuneCountInString(candidates[i].text)
	}
	if total <= totalChars {
		return
	}
	effective := totalChars
	if len(candidates) > 1 && effective > len(candidates)*160 {
		effective -= len(candidates) * 96
	}
	window := turnWindowBudget(effective, len(candidates))
	if window <= 0 {
		// A positive cap below the selected band count still cuts each band instead
		// of skipping the cut; the caller's render budget stays the final frame cap.
		window = 1
	}
	for i := range candidates {
		candidate := &candidates[i]
		maxChars := window
		if candidate.standalone {
			maxChars = window - 160
			if maxChars < 240 && window > 0 {
				maxChars = window
			}
		}
		if maxChars <= 0 {
			continue
		}
		focused := focusEpisodicDeliveryText(candidate.text, query, maxChars)
		if focused == candidate.text {
			continue
		}
		candidate.text = focused
		candidate.truncated = true
		if nodes, ok := candidate.band["nodes"].([]map[string]any); ok && len(nodes) > 0 {
			nodes[0]["content"] = focused
		}
		candidate.rawHit["text"] = focused
	}
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
	qWords := localvault.SplitWords(strings.ToLower(strings.TrimSpace(query)))
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
