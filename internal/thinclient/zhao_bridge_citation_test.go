// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient

import (
	"testing"

	"github.com/rocareer/rolling-memory/internal/zhao/rm"
)

// TestZhaoFusionNoeticCitationCarrier pins the fusion-layer projection of a
// matched Layer-3 consolidation into the mini-index:
//  1. noeticFusionDoc emits a term-less citation carrier (identity + clock +
//     layer + CitedIDs) and rejects consolidations without citations;
//  2. a mini-index built with the carrier exposes the citation provenance edge
//     to the kernel: the cited episodic unit and the consolidation couple at
//     the 1/e subject-cos floor with zero shared vocabulary;
//  3. fusion ranking with the carrier present never ranks the term-less
//     carrier itself above lexical candidates (carriers must not steal seats).
func TestZhaoFusionNoeticCitationCarrier(t *testing.T) {
	// 1. Projection contract.
	if _, ok := noeticFusionDoc(CloudQueryHit{UnitID: "no_1", Layer: LayerNoetic}); ok {
		t.Fatal("consolidation without citations must not become a carrier")
	}
	nd, ok := noeticFusionDoc(CloudQueryHit{
		UnitID:       "no_1",
		Layer:        LayerNoetic,
		OccurredAt:   1760000500,
		CitedUnitIDs: []string{"ep_a", "ep_b"},
	})
	if !ok || nd.Layer != LayerNoetic || len(nd.CitedIDs) != 2 || len(nd.Terms) != 0 {
		t.Fatalf("unexpected carrier projection: %+v", nd)
	}

	// 2. Kernel-side citation coupling with zero shared vocabulary.
	az := rm.NewTermExtractor(rm.DemoSecret)
	docs := []rm.Doc{
		az.ExtractTermsDoc("ep_a", 1760000000, "Quartz ledger 4417 stamped at harbor gate."),
		az.ExtractTermsDoc("ep_c", 1760000300, "Granite manifest 9920 sealed at the yard."),
		nd,
	}
	for i := 0; i < 8; i++ {
		docs = append(docs, az.ExtractTermsDoc(
			"bg_"+string(rune('a'+i)), 1760000000-int64((i+2)*86400),
			"Background chores journal entry with routine notes."))
	}
	eng := rm.NewEngine(rm.BuildIndex(docs))
	rel, ok := eng.EvaluateUnitPairRelation("no_1", "ep_a")
	if !ok {
		t.Fatal("EvaluateUnitPairRelation failed for citation pair")
	}
	const invE = 0.36787944117144233
	if rel.SubjectCosine < invE-1e-12 {
		t.Fatalf("citation carrier lost the 1/e subject-cos floor: %.6f", rel.SubjectCosine)
	}
	relOrphan, _ := eng.EvaluateUnitPairRelation("no_1", "ep_c")
	if relOrphan.SubjectCosine >= invE-1e-12 {
		t.Fatalf("non-cited unit must not inherit the citation floor: %.6f", relOrphan.SubjectCosine)
	}

	// 3. Term-less carrier must not outrank lexical candidates.
	q, _, qPos := az.ExtractQueryWithPositions(eng.Index, "quartz ledger harbor")
	hits := eng.SearchWithPositions(q, qPos, 5)
	for _, h := range hits {
		if h.UnitID == "no_1" && len(hits) > 1 && hits[0].UnitID == "no_1" {
			t.Fatalf("term-less citation carrier stole the top seat: %+v", hits)
		}
	}
}
