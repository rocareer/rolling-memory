// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package recall

import (
	"testing"

	"github.com/rocareer/rolling-memory/internal/store"
)

// TestLocusAnchorParsesLeadingWorkspaceBracket pins the retrieval-scope anchor syntax:
// only a leading `[<workspace>]` counts; anything else means "no anchor".
func TestLocusAnchorParsesLeadingWorkspaceBracket(t *testing.T) {
	cases := []struct{ cue, want string }{
		{"[Rolling-Memory] 首次进入控制台卡在 loading", "Rolling-Memory"},
		{"  [third-workspace]  事件桥", "third-workspace"},
		{"无锚 cue", ""},
		{"[, 半角", ""},
		{"[a\nb] 换行", ""},
		{"[] 空锚", ""},
	}
	for _, tc := range cases {
		if got := locusAnchor(tc.cue); got != tc.want {
			t.Fatalf("locusAnchor(%q) = %q, want %q", tc.cue, got, tc.want)
		}
	}
}

// TestOrderCandidatesByLocusAnchorIsStableAndFailOpen pins candidate ordering:
// same-workspace candidates first (stable, nothing dropped), and byte-identical
// behavior when the anchor is absent or matches nobody.
func TestOrderCandidatesByLocusAnchorIsStableAndFailOpen(t *testing.T) {
	ptr := func(value string) *string { return &value }
	row := func(id, locus string) store.Candidate {
		return store.Candidate{Data: store.CandidateRow{Assertion: store.Assertion{NodeID: id, Locus: ptr(locus)}}, Score: 1}
	}
	original := []store.Candidate{
		row("c1", "other-workspace"),
		row("c2", "Rolling-Memory"),
		row("c3", "third-workspace"),
		row("c4", "Rolling-Memory"),
	}
	ids := func(candidates []store.Candidate) []string {
		got := make([]string, 0, len(candidates))
		for _, candidate := range candidates {
			got = append(got, candidate.Data.NodeID)
		}
		return got
	}
	equal := func(got, want []string) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}

	ordered := orderCandidatesByLocusAnchor(original, "[Rolling-Memory] 首次进入控制台卡在 loading")
	if got, want := ids(ordered), []string{"c2", "c4", "c1", "c3"}; !equal(got, want) {
		t.Fatalf("anchored order = %v, want %v", got, want)
	}
	if got, want := ids(original), []string{"c1", "c2", "c3", "c4"}; !equal(got, want) {
		t.Fatalf("input mutated: %v", got)
	}

	for _, cue := range []string{"无锚 cue", "[NoSuchRepo] 谁都没命中"} {
		got := orderCandidatesByLocusAnchor(original, cue)
		if want := []string{"c1", "c2", "c3", "c4"}; !equal(ids(got), want) {
			t.Fatalf("fail-open order for %q = %v, want %v", cue, ids(got), want)
		}
	}
}

// TestPromoteBandsByLocusAnchorGuaranteesDeliveryOrder pins the delivery-level pass:
// bands carrying the anchor workspace come first (stable), untouched without an anchor.
func TestPromoteBandsByLocusAnchorGuaranteesDeliveryOrder(t *testing.T) {
	band := func(id string, loci ...string) map[string]any {
		nodes := make([]map[string]any, 0, len(loci))
		for _, value := range loci {
			nodes = append(nodes, map[string]any{"id": id + ":" + value, "locus": value})
		}
		return map[string]any{"id": id, "nodes": nodes}
	}
	original := []map[string]any{
		band("b1", "other-workspace"),
		band("b2", "Rolling-Memory"),
		band("b3", "third-workspace"),
	}
	order := func(bands []map[string]any) []string {
		got := make([]string, 0, len(bands))
		for _, one := range bands {
			got = append(got, asString(one["id"]))
		}
		return got
	}
	equal := func(got, want []string) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}

	result := &Result{Bands: append([]map[string]any(nil), original...), Meta: map[string]any{}}
	promoteBandsByLocusAnchor(result, "[Rolling-Memory] 首次进入控制台卡在 loading")
	if got, want := order(result.Bands), []string{"b2", "b1", "b3"}; !equal(got, want) {
		t.Fatalf("anchored band order = %v, want %v", got, want)
	}
	if result.Meta["locus_anchor"] != "Rolling-Memory" {
		t.Fatalf("meta locus_anchor = %v", result.Meta["locus_anchor"])
	}

	result = &Result{Bands: append([]map[string]any(nil), original...), Meta: map[string]any{}}
	promoteBandsByLocusAnchor(result, "无锚 cue")
	if got, want := order(result.Bands), []string{"b1", "b2", "b3"}; !equal(got, want) {
		t.Fatalf("fail-open band order = %v, want %v", got, want)
	}
	if _, ok := result.Meta["locus_anchor"]; ok {
		t.Fatalf("meta locus_anchor set without anchor")
	}
}
