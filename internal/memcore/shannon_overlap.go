// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"strings"
)

// ShannonTokenOverlapSim computes the Shannon self-information-weighted token overlap
// ratio in [0, 1] between `text` and `tokens`.
//
// Unlike uniform hit-count ratios (`hits / len(tokens)`), each distinct query token `t`
// is weighted by `max(StringShannonBits(t), 1.0)` so high-entropy proper nouns, rare
// identifiers, and specific multi-morpheme terms strictly dominate low-entropy common words,
// while remaining 100% language-agnostic (zero character-count thresholds, zero language branches).
func ShannonTokenOverlapSim(text string, tokens []string) float64 {
	if text == "" || len(tokens) == 0 {
		return 0
	}
	hayLow := strings.ToLower(text)
	seen := make(map[string]bool, len(tokens))
	totalBits := 0.0
	hitBits := 0.0
	for _, raw := range tokens {
		tl := strings.ToLower(strings.TrimSpace(raw))
		if tl == "" || seen[tl] {
			continue
		}
		seen[tl] = true
		w := StringShannonBits(tl)
		if w < 1.0 {
			w = 1.0
		}
		totalBits += w
		if strings.Contains(hayLow, tl) {
			hitBits += w
			continue
		}
		st := StemToken(tl)
		if m := MatchTokenOrStemWithStem(hayLow, tl, st, false); m >= 0.75 {
			hitBits += w * m
		}
	}
	if totalBits <= 0 {
		return 0
	}
	sim := hitBits / totalBits
	if sim > 1.0 {
		sim = 1.0
	}
	return sim
}
