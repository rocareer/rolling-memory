// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package codec

import (
	"math"
	"strings"
	"time"

	"github.com/rocareer/rolling-memory/internal/zhao/rm/closedset"
)

// OrdinalSpanAnnotation is the delivery-layer fact statement produced by the
// corpus-endogenous ordinal day resolver (protocol slice S3, P-NCD-3). It is
// never consumed by ranking: injecting the resolved day as query mass was
// measured net-negative because day-level signal is common-mode across the
// intra-day population (ledger F-32); as an annotation the cost of a wrong
// inference drops to one ignorable line for the downstream LLM.
type OrdinalSpanAnnotation struct {
	Polarity    string `json:"polarity"`      // "begin" | "end"
	ResolvedDay string `json:"resolved_day"`  // ISO day the span boundary lands on
	SingleRun   bool   `json:"single_run"`    // emission gate: exactly one dense run
	RunSpanDays int    `json:"run_span_days"` // inclusive day span of the resolved run
	AnchorDF    int    `json:"anchor_df"`     // document frequency of the proper anchor
}

// ResolveOrdinalSpanAnnotation detects an ordinal-polarity frame in the query
// text and, when the corpus time geometry is unambiguous, resolves the span
// boundary day as a delivery annotation:
//
//  1. Polarity: the query carries surfaces from exactly one polarity class of
//     the Part III table (closedset.LookupOrdinalPolarity; both or neither ->
//     no frame, nil).
//  2. Anchor: the proper-cased primary query term with df in [2, N/e]
//     (dedup floor = minimum span support; N/e = first-order Euler extension
//     bound) holding maximal group mass.
//  3. Day histogram: the anchor's postings per reference-clock UTC day; days
//     below maxUnits/e^2 (second-order Euler density floor) are sparse-mention
//     noise; survivors merge into runs tolerating one missing day.
//  4. Emission gate: exactly one run (measured family precision 68% single-run
//     vs 25% multi-run, F-27); polarity picks the boundary (begin -> first day,
//     end -> last day).
func (a *TermExtractor) ResolveOrdinalSpanAnnotation(idx *Index, queryText string) *OrdinalSpanAnnotation {
	if a == nil || a.enc == nil || idx == nil || idx.N == 0 || queryText == "" {
		return nil
	}
	var hasBegin, hasEnd bool
	for _, sent := range splitSentences(CanonicalizeApostrophe(queryText)) {
		for _, w := range SplitWords(sent) {
			lw := strings.ToLower(strings.Trim(w, ",.;:!?()[]{}\"'"))
			switch closedset.LookupOrdinalPolarity(lw) {
			case -1:
				hasBegin = true
			case 1:
				hasEnd = true
			}
		}
	}
	if hasBegin == hasEnd {
		return nil
	}
	pole := "begin"
	if hasEnd {
		pole = "end"
	}

	qTerms, _, _ := a.ExtractQueryWithPositions(idx, queryText)
	nOverE := float64(idx.N) / math.E
	var anchorH uint64
	var anchorMass float64
	for h, qa := range qTerms {
		if qa.Role != RolePrimary || qa.Flags&FlagHasUpper == 0 {
			continue
		}
		df := idx.TermDF(h)
		if df < 2 || float64(df) > nOverE {
			continue
		}
		g := qa.Group
		if g == 0 {
			g = h
		}
		if m, ok := idx.TermMass(g); ok && m > anchorMass {
			anchorMass = m
			anchorH = h
		}
	}
	if anchorH == 0 {
		return nil
	}

	const noDay = int64(-1) << 62
	dayUnits := make(map[int64]int, 32)
	maxUnits := 0
	for _, p := range idx.Postings[anchorH] {
		d := noDay
		if int(p.Unit) < len(idx.UnitMeta) {
			if ts := idx.UnitMeta[p.Unit].RefTime(); ts > 0 {
				d = ts / 86400
			}
		}
		if d == noDay {
			continue
		}
		dayUnits[d]++
		if dayUnits[d] > maxUnits {
			maxUnits = dayUnits[d]
		}
	}
	if len(dayUnits) == 0 {
		return nil
	}
	floor := float64(maxUnits) / (math.E * math.E)
	days := make([]int64, 0, len(dayUnits))
	for d, n := range dayUnits {
		if float64(n) >= floor {
			days = append(days, d)
		}
	}
	if len(days) == 0 {
		return nil
	}
	for i := 1; i < len(days); i++ {
		for j := i; j > 0 && days[j] < days[j-1]; j-- {
			days[j], days[j-1] = days[j-1], days[j]
		}
	}
	runLo, runHi := days[0], days[0]
	for _, d := range days[1:] {
		if d <= runHi+2 {
			runHi = d
			continue
		}
		// Second dense run: ambiguous span, the high-confidence gate refuses.
		return nil
	}
	dStar := runLo
	if pole == "end" {
		dStar = runHi
	}
	return &OrdinalSpanAnnotation{
		Polarity:    pole,
		ResolvedDay: time.Unix(dStar*86400, 0).UTC().Format("2006-01-02"),
		SingleRun:   true,
		RunSpanDays: int(runHi-runLo) + 1,
		AnchorDF:    idx.TermDF(anchorH),
	}
}
