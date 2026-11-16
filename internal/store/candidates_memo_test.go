// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package store

import (
	"strings"
	"testing"
)

// scoreMemoFixture builds n candidates whose anchored turn texts are either one shared text or
// n distinct texts of the same size — the shape that dominates real recall (many assertions
// anchored to the same long turn).
func scoreMemoFixture(n int, shared bool) []Candidate {
	sharedText := strings.Repeat("番茄炒蛋与香椿拌面的做法记录 内存图召回候选打分校准锚点 ", 9000)
	out := make([]Candidate, 0, n)
	for i := 0; i < n; i++ {
		text := sharedText
		if !shared {
			text = sharedText + strings.Repeat("差异后缀甲", i+1)
		}
		out = append(out, Candidate{Data: CandidateRow{
			Assertion: Assertion{AnchorStart: int64(i % 5), AnchorEnd: int64(i%5) + 6},
			SLabel:    "候选打分",
			XLabel:    "落位于",
			OLabel:    "internal/store/candidates.go",
			TraceText: text,
		}})
	}
	return out
}

// TestScoreCandidatesLexicalSharedTraceTextCost pins the raw-trace-text leg optimization: scoring
// candidates that share one anchored turn must be strictly cheaper than scoring candidates with
// per-candidate texts of the same size. The previous implementation lowercased (and rune-copied)
// the whole turn text for every candidate, so mixed/shared fixtures cost the same and recall
// replays took seconds per turn. Scores stay batch-stable (IDF depends on the candidate set by
// design, so this asserts determinism, not cross-batch equality).
func TestScoreCandidatesLexicalSharedTraceTextCost(t *testing.T) {
	const n = 12
	query := "候选打分校准锚点 召回"
	shared := scoreMemoFixture(n, true)
	distinct := scoreMemoFixture(n, false)

	run := func(cands []Candidate) {
		scoreCandidatesLexical(cands, query)
	}
	sharedAllocs := testing.AllocsPerRun(3, func() { run(shared) })
	distinctAllocs := testing.AllocsPerRun(3, func() { run(distinct) })
	if sharedAllocs >= distinctAllocs {
		t.Fatalf("shared-turn scoring must be cheaper than per-candidate texts: shared=%.0f allocs vs distinct=%.0f allocs "+
			"(the lazy lower-case cache / token memo regressed)", sharedAllocs, distinctAllocs)
	}

	// Determinism + no cross-candidate contamination: repeating the same batch is stable, and
	// equal candidate rows receive equal scores.
	first := make([]float64, len(shared))
	for i := range shared {
		first[i] = shared[i].LexScore
	}
	run(shared)
	for i := range shared {
		if shared[i].LexScore != first[i] {
			t.Fatalf("candidate %d scored %v then %v on identical batches (memo is not deterministic)", i, first[i], shared[i].LexScore)
		}
	}
	if shared[0].LexScore != shared[1].LexScore {
		t.Fatalf("identical candidate rows must score equally: %v != %v", shared[0].LexScore, shared[1].LexScore)
	}
}

// TestCandidateAnchorSpanStrictness covers the rune-addressed anchor extraction used by the
// scoring loop: spans must match the `[]rune(text)[start:end]` idiom exactly, including the
// strict empty result for out-of-range ends (which is why the bounded extractor stays separate
// from the clamping sliceRunes helper).
func TestCandidateAnchorSpanStrictness(t *testing.T) {
	text := "a😀b猫c"
	if got := sliceRunesBounded(text, 1, 4); got != "😀b猫" {
		t.Fatalf("mixed astral span = %q, want %q", got, "😀b猫")
	}
	if got := sliceRunesBounded(text, 0, 99); got != "" {
		t.Fatalf("out-of-range end must yield empty, got %q", got)
	}
	if got := sliceRunesBounded(text, 99, 120); got != "" {
		t.Fatalf("start beyond rune count must yield empty, got %q", got)
	}
}
