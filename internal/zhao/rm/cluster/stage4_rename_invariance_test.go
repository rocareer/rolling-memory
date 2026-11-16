// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cluster

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

// stage4RenameInvarianceDoc builds one synthetic record whose physical content
// (terms, counts, positions, timestamp) is fully determined by the arguments;
// the UnitID string is the only caller-controlled identity.
func stage4RenameInvarianceDoc(id string, occurredAt int64, terms ...uint64) Doc {
	termSet := make(map[uint64]Term, len(terms))
	counts := make(map[uint64]int, len(terms))
	positions := make(map[uint64][]TermPosition, len(terms))
	for i, hash := range terms {
		termSet[hash] = Term{
			Hash:  hash,
			Bits:  10,
			Role:  RolePrimary,
			Group: hash,
			Sent:  1,
			Slot:  uint16(i + 1),
		}
		counts[hash] = 1
		positions[hash] = []TermPosition{{Sentence: 0, Slot: i}}
	}
	return Doc{
		UnitID:     id,
		OccurredAt: occurredAt,
		Terms:      termSet,
		Counts:     counts,
		Positions:  positions,
	}
}

// stage4RenameInvarianceCorpus reproduces the F-31 counterexample geometry: a
// dominant leader burst three days after an orthogonal bystander pair that
// shares the leader's bucket neighborhood, with month-spaced noise inflating
// the endogenous mean gap so temporal affinity stays above 1-1/e across the
// three-day gap. bysPrefix controls only the two bystander UnitID strings,
// steering their unit indices to either side of the leader burst.
func stage4RenameInvarianceCorpus(bysPrefix string) ([]Doc, []uint64) {
	const day = int64(86400)
	leadTS := int64(1000) + 3*day
	lead := stage4RenameInvarianceDoc("scene-lead", leadTS, 1, 2, 3, 50, 51, 52, 53)
	for _, h := range []uint64{1, 2, 3} {
		lead.Counts[h] = 2
		lead.Positions[h] = append(lead.Positions[h], TermPosition{Sentence: 0, Slot: lead.Positions[h][0].Slot + 8})
	}
	docs := []Doc{
		lead,
		stage4RenameInvarianceDoc("twin-a", leadTS+1, 1, 2, 3, 50, 51, 54, 55),
		stage4RenameInvarianceDoc("twin-b", leadTS+2, 1, 2, 3, 52, 53, 56, 57),
		stage4RenameInvarianceDoc(bysPrefix+"-bystander", 1000, 3, 60, 61, 62, 63, 64, 65, 66, 67),
		stage4RenameInvarianceDoc(bysPrefix+"-bystander-echo", 1001, 3, 60, 61, 62, 63, 64, 68),
		stage4RenameInvarianceDoc("filler-q1", leadTS+30*day, 1, 70),
		stage4RenameInvarianceDoc("filler-q2", leadTS+60*day, 2, 71),
		stage4RenameInvarianceDoc("filler-q3", leadTS+90*day, 3, 72),
	}
	base := leadTS + 120*day
	for i := 0; i < 25; i++ {
		docs = append(docs, stage4RenameInvarianceDoc(
			fmt.Sprintf("noise-%02d", i),
			base+int64(i)*30*day,
			uint64(100+2*i), uint64(101+2*i),
		))
	}
	return docs, []uint64{1, 2, 3}
}

func stage4RenameInvarianceCanonicalKey(id string) string {
	if i := strings.Index(id, "-bystander"); i >= 0 {
		return "bystander" + id[i+len("-bystander"):]
	}
	return id
}

// TestStage4UnitIDRenameEnergyInvarianceAdversarial pins the F-31 fix: two
// corpora that differ only in the bystander pair's UnitID strings (and hence
// only in unit-index assignment order) must deliver bit-identical energies
// for every physically identical unit. Before the fix, the bystander borrow
// damping read a half-materialized structure term whose materialization
// depended on the endorsement pre-pass scan order, yielding a deterministic
// ~0.04 Nats delivered-energy drift under a pure rename.
func TestStage4UnitIDRenameEnergyInvarianceAdversarial(t *testing.T) {
	run := func(prefix string) map[string]Hit {
		docs, query := stage4RenameInvarianceCorpus(prefix)
		idx := BuildIndex(docs)
		eng := NewEngine(idx)
		eng.ensureClusterState()
		if !eng.canUseClusterPipeline() {
			t.Fatalf("prefix %q: cluster pipeline not active (vacuous arm)", prefix)
		}
		qTerms := make(map[uint64]Term, len(query))
		qPositions := make(map[uint64][]TermPosition, len(query))
		for i, hash := range query {
			qTerms[hash] = Term{
				Hash:  hash,
				Bits:  10,
				Role:  RolePrimary,
				Group: hash,
				Sent:  1,
				Slot:  uint16(i + 1),
			}
			qPositions[hash] = []TermPosition{{Sentence: 0, Slot: i}}
		}
		hits := eng.SearchWithPositions(qTerms, qPositions, 0)
		if len(hits) == 0 {
			t.Fatalf("prefix %q: no hits", prefix)
		}
		byKey := make(map[string]Hit, len(hits))
		for _, h := range hits {
			byKey[stage4RenameInvarianceCanonicalKey(h.UnitID)] = h
		}
		return byKey
	}

	// "a" sorts the bystanders before the leader burst; "zz" sorts them after.
	hitsA := run("a")
	hitsZ := run("zz")

	if len(hitsA) != len(hitsZ) {
		t.Fatalf("hit count differs under pure rename: a=%d zz=%d", len(hitsA), len(hitsZ))
	}
	for key, ha := range hitsA {
		hz, ok := hitsZ[key]
		if !ok {
			t.Fatalf("unit %q missing in zz arm", key)
		}
		if math.Float64bits(ha.Energy) != math.Float64bits(hz.Energy) ||
			math.Float64bits(ha.Direct) != math.Float64bits(hz.Direct) ||
			math.Float64bits(ha.Hop1) != math.Float64bits(hz.Hop1) ||
			math.Float64bits(ha.Hop2) != math.Float64bits(hz.Hop2) {
			t.Fatalf("UnitID rename broke energy invariance for %q:\n  a : Energy=%.12f Direct=%.12f Hop1=%.12f Hop2=%.12f\n  zz: Energy=%.12f Direct=%.12f Hop1=%.12f Hop2=%.12f",
				key,
				ha.Energy, ha.Direct, ha.Hop1, ha.Hop2,
				hz.Energy, hz.Direct, hz.Hop1, hz.Hop2)
		}
	}
}
