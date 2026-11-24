// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package facade

import "github.com/rocareer/rolling-memory/internal/core"

// deliveredCandidate is one band-level delivery candidate ahead of the adaptive
// energy window. score is the total collision energy; direct is the candidate's
// own collision energy without borrowed neighbour corroboration. text is the
// full delivered body before any character-budget decision; standalone tells the
// budget pass whether the body carries the cell-band overhead.
type deliveredCandidate struct {
	band       map[string]any
	rawHit     map[string]any
	text       string
	standalone bool
	score      float64
	direct     float64
	locusMatch bool
	sessionKey string
	occurredAt int64
	order      int
	truncated  bool
}

// peakDecayReference returns the top total score of a ranked candidate list and its
// zero-floor natural decay reference. The reference is the peak's own direct
// collision energy (core.DecayReferenceOf), not its total energy: the total includes
// corroboration borrowed from same-cluster neighbours, and a record with
// complementary evidence on an isolated date must not be cut against that mass.
func peakDecayReference(candidates []deliveredCandidate) (float64, float64) {
	peakScore, peakDirect := 0.0, 0.0
	for _, cand := range candidates {
		if cand.score > peakScore {
			peakScore, peakDirect = cand.score, cand.direct
		}
	}
	return peakScore, core.DecayReferenceOf(peakDirect, peakScore)
}
