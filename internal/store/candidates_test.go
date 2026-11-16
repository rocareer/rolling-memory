// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package store

import (
	"math"
	"testing"
)

func TestCandidatesScoreBoostsAndFilters(t *testing.T) {
	store := newTestStore(t, testScope)
	addFact(t, store, testScope, "老张养了一只猫。", "老张", "养", "猫", "老张养了一只猫", factOptions{
		TValidFrom: 1772409600, Holder: "我",
	})
	cat, _, _ := store.NodeByLabel(testScope, "猫", "")
	candidates, gaps, err := store.Candidates([]string{cat.ID}, CandidateOptions{
		Scope: testScope, Reader: "owner", Depth: 2, Limit: 256,
	})
	if err != nil {
		t.Fatalf("candidates: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %d", len(candidates))
	}
	if gaps["seeds"] != 1 || gaps["neighbors"] != 2 || gaps["candidates"] != 1 {
		t.Fatalf("gaps = %v", gaps)
	}
	data := candidates[0].Data
	if data.SLabel != "老张" || data.XLabel != "养" || data.OLabel != "猫" {
		t.Fatalf("join labels = %v", data)
	}
	if data.TraceText != "老张养了一只猫。" {
		t.Fatalf("trace text = %q", data.TraceText)
	}
	baseScore := candidates[0].Score

	boosted, boostedGaps, err := store.Candidates([]string{cat.ID}, CandidateOptions{
		Scope: testScope, Reader: "owner", Depth: 2, Limit: 256,
		BoostWindow: &Window{Start: 1772409600, End: 1775001600},
	})
	if err != nil {
		t.Fatalf("window candidates: %v", err)
	}
	if math.Abs(boosted[0].Score-(baseScore+0.15)) > 1e-9 || boostedGaps["window_boosted"] != 1 {
		t.Fatalf("window score = %v gaps=%v", boosted[0].Score, boostedGaps)
	}
	month := 3
	monthBoosted, monthGaps, err := store.Candidates([]string{cat.ID}, CandidateOptions{
		Scope: testScope, Reader: "owner", Depth: 2, Limit: 256, BoostMonth: &month,
	})
	if err != nil {
		t.Fatalf("month candidates: %v", err)
	}
	if math.Abs(monthBoosted[0].Score-(baseScore+0.10)) > 1e-9 || monthGaps["month_boosted"] != 1 {
		t.Fatalf("month score = %v gaps=%v", monthBoosted[0].Score, monthGaps)
	}
	none, _, err := store.Candidates([]string{cat.ID}, CandidateOptions{
		Scope: testScope, Reader: "owner", Depth: 2, Limit: 256, AsOf: testNow - 1,
	})
	if err != nil || len(none) != 0 {
		t.Fatalf("asof candidates = %d err=%v", len(none), err)
	}
	none, _, err = store.Candidates([]string{cat.ID}, CandidateOptions{
		Scope: testScope, Reader: "owner", Depth: 2, Limit: 256, WindowStart: testNow + 1,
	})
	if err != nil || len(none) != 0 {
		t.Fatalf("window-start candidates = %d err=%v", len(none), err)
	}
}

func TestCandidatesEmptySeeds(t *testing.T) {
	store := newTestStore(t, testScope)
	candidates, gaps, err := store.Candidates(nil, CandidateOptions{Scope: testScope, Reader: "owner"})
	if err != nil {
		t.Fatalf("candidates: %v", err)
	}
	if len(candidates) != 0 || gaps["seeds"] != 0 {
		t.Fatalf("candidates=%d gaps=%v", len(candidates), gaps)
	}
}

func TestAssertionsByAnchorBackfill(t *testing.T) {
	store := newTestStore(t, testScope)
	receipt := addFact(t, store, testScope, "老张养了一只猫。", "老张", "养", "猫", "老张养了一只猫", factOptions{})
	traceHash := queryText(t, store, "SELECT hash FROM trace")
	candidates, gaps, err := store.AssertionsByAnchor([]string{traceHash}, AnchorOptions{
		Scope: testScope, Reader: "deliverable", Limit: 64,
	})
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if len(candidates) != 1 || candidates[0].Data.NodeID != receipt["id"] {
		t.Fatalf("backfill candidates = %v", candidates)
	}
	if gaps["backfill_traces"] != 1 || gaps["backfill_candidates"] != 1 {
		t.Fatalf("gaps = %v", gaps)
	}
	if math.Abs(candidates[0].Score-0.69) > 1e-9 {
		t.Fatalf("score = %v, want 0.69", candidates[0].Score)
	}
	session := "other"
	none, noneGaps, err := store.AssertionsByAnchor([]string{traceHash}, AnchorOptions{
		Scope: testScope, Reader: "deliverable", Limit: 64, Session: &session,
	})
	if err != nil || len(none) != 0 || noneGaps["backfill_candidates"] != 0 {
		t.Fatalf("session backfill = %v gaps=%v err=%v", none, noneGaps, err)
	}
	empty, emptyGaps, err := store.AssertionsByAnchor(nil, AnchorOptions{Scope: testScope, Reader: "deliverable"})
	if err != nil || len(empty) != 0 || emptyGaps["backfill_traces"] != 0 {
		t.Fatalf("empty backfill = %v gaps=%v err=%v", empty, emptyGaps, err)
	}
}

func TestRecentListAndMentionSeeds(t *testing.T) {
	store := newTestStore(t, testScope)
	addFact(t, store, testScope, "老张养了一只猫。", "老张", "养", "猫", "老张养了一只猫", factOptions{})
	privateReceipt := addFact(t, store, testScope, "绝密信息：老张的暗号是蓝鸟。", "老张", "暗号", "蓝鸟", "老张的暗号是蓝鸟", factOptions{
		Audience: "private",
	})
	deliverable, err := store.Recent(testScope, "deliverable", 10)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	for _, item := range deliverable {
		if item["id"] == privateReceipt["id"] {
			t.Fatalf("private assertion leaked into deliverable recent")
		}
	}
	if len(deliverable) != 1 {
		t.Fatalf("deliverable recent = %d", len(deliverable))
	}
	owner, err := store.Recent(testScope, "owner", 10)
	if err != nil || len(owner) != 2 {
		t.Fatalf("owner recent = %d err=%v", len(owner), err)
	}

	traceHash := queryText(t, store, "SELECT hash FROM trace LIMIT 1")
	cat, _, _ := store.NodeByLabel(testScope, "猫", "")
	if err := store.AddMention(testScope, traceHash, "猫", 3, 4, &cat.ID); err != nil {
		t.Fatalf("mention with node: %v", err)
	}
	if err := store.AddMention(testScope, traceHash, "某人", 0, 2, nil); err != nil {
		t.Fatalf("mention without node: %v", err)
	}
	seeds, err := store.MentionSeedIDs([]string{traceHash}, testScope, 16)
	if err != nil || len(seeds) != 1 || seeds[0] != cat.ID {
		t.Fatalf("mention seeds = %v err=%v", seeds, err)
	}
	unresolved, err := store.UnresolvedMentionCount(testScope)
	if err != nil || unresolved != 1 {
		t.Fatalf("unresolved = %d err=%v", unresolved, err)
	}
	low, high, err := store.TimeRange(testScope)
	if err != nil || low != testNow || high != testNow {
		t.Fatalf("time range = %d..%d err=%v", low, high, err)
	}
	emptyStore := newTestStore(t, testScope)
	low, high, err = emptyStore.TimeRange(testScope)
	if err != nil || low != 0 || high != 0 {
		t.Fatalf("empty time range = %d..%d err=%v", low, high, err)
	}
}

func TestRawTracesExtraTerms(t *testing.T) {
	store := newTestStore(t, testScope)
	if _, _, err := store.AddTrace(testScope, "窗台上落了一只蓝鸟。", TraceOptions{Author: "user"}); err != nil {
		t.Fatalf("add trace: %v", err)
	}
	hits, err := store.RawTraces("", RawOptions{
		Scope: testScope, Reader: "deliverable", Limit: 5, ExtraTerms: []string{"蓝鸟", "蓝"},
	})
	if err != nil {
		t.Fatalf("raw traces: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("extra-term hits = %v", hits)
	}
	none, err := store.RawTraces("", RawOptions{Scope: testScope, Reader: "deliverable", Limit: 5})
	if err != nil || len(none) != 0 {
		t.Fatalf("empty cue hits = %v err=%v", none, err)
	}
}
