// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package store

import (
	"testing"
)

func TestRawTracesFTSAndLikePaths(t *testing.T) {
	store := newTestStore(t, testScope)
	march := int64(1772409600)
	if _, _, err := store.AddTrace(testScope, "老张在旧桥边看见了那只猫。", TraceOptions{
		Session: "s1", Turn: 1, Author: "user", OccurredAt: &march,
	}); err != nil {
		t.Fatalf("add long trace: %v", err)
	}
	if _, _, err := store.AddTrace(testScope, "小猫在窗台。", TraceOptions{
		Session: "s1", Turn: 2, Author: "user", OccurredAt: &march,
	}); err != nil {
		t.Fatalf("add short trace: %v", err)
	}

	longHits, err := store.RawTraces("旧桥边", RawOptions{Scope: testScope, Reader: "deliverable", Limit: 5})
	if err != nil {
		t.Fatalf("fts raw: %v", err)
	}
	if len(longHits) != 1 || longHits[0].Session != "s1" {
		t.Fatalf("fts hits = %v", longHits)
	}

	shortHits, err := store.RawTraces("小猫", RawOptions{Scope: testScope, Reader: "deliverable", Limit: 5})
	if err != nil {
		t.Fatalf("like raw: %v", err)
	}
	if len(shortHits) != 1 {
		t.Fatalf("like hits = %v", shortHits)
	}

	session := "s2"
	none, err := store.RawTraces("旧桥边", RawOptions{
		Scope: testScope, Reader: "deliverable", Limit: 5, Session: &session,
	})
	if err != nil || len(none) != 0 {
		t.Fatalf("session hits = %v err=%v", none, err)
	}
	marchMonth := 3
	monthHit, err := store.RawTraces("旧桥边", RawOptions{
		Scope: testScope, Reader: "deliverable", Limit: 5, Month: &marchMonth,
	})
	if err != nil || len(monthHit) != 1 {
		t.Fatalf("month hit = %v err=%v", monthHit, err)
	}
	mayMonth := 5
	monthMiss, err := store.RawTraces("旧桥边", RawOptions{
		Scope: testScope, Reader: "deliverable", Limit: 5, Month: &mayMonth,
	})
	if err != nil || len(monthMiss) != 0 {
		t.Fatalf("month miss = %v err=%v", monthMiss, err)
	}
	excluded := []string{longHits[0].Trace}
	none, err = store.RawTraces("旧桥边", RawOptions{
		Scope: testScope, Reader: "deliverable", Limit: 5, ExcludeHashes: excluded,
	})
	if err != nil || len(none) != 0 {
		t.Fatalf("excluded hits = %v err=%v", none, err)
	}
	windowed, err := store.RawTraces("旧桥边", RawOptions{
		Scope: testScope, Reader: "deliverable", Limit: 5, WindowStart: march + 3600,
	})
	if err != nil || len(windowed) != 0 {
		t.Fatalf("window hits = %v err=%v", windowed, err)
	}
}

func TestRawTracesPrivateAudience(t *testing.T) {
	store := newTestStore(t, testScope)
	if _, _, err := store.AddTrace(testScope, "绝密旧桥边的暗号。", TraceOptions{
		Author: "user", Audience: "private",
	}); err != nil {
		t.Fatalf("add private trace: %v", err)
	}
	deliverable, err := store.RawTraces("旧桥边", RawOptions{Scope: testScope, Reader: "deliverable", Limit: 5})
	if err != nil || len(deliverable) != 0 {
		t.Fatalf("deliverable hits = %v err=%v", deliverable, err)
	}
	owner, err := store.RawTraces("旧桥边", RawOptions{Scope: testScope, Reader: "owner", Limit: 5})
	if err != nil || len(owner) != 1 {
		t.Fatalf("owner hits = %v err=%v", owner, err)
	}
}

func TestNoFTSFallsBackToLike(t *testing.T) {
	store := newTestStore(t, testScope)
	if _, _, err := store.AddTrace(testScope, "老张在旧桥边看见了那只猫。", TraceOptions{Author: "user"}); err != nil {
		t.Fatalf("add trace: %v", err)
	}
	if _, err := store.EnsureNode(testScope, "referent", "SQLite", NodeOptions{}); err != nil {
		t.Fatalf("ensure node: %v", err)
	}
	if _, err := store.conn.Exec("DROP TABLE node_fts"); err != nil {
		t.Fatalf("drop node_fts: %v", err)
	}
	if _, err := store.conn.Exec("DROP TABLE trace_fts"); err != nil {
		t.Fatalf("drop trace_fts: %v", err)
	}
	hits, err := store.RawTraces("旧桥边", RawOptions{Scope: testScope, Reader: "deliverable", Limit: 5})
	if err != nil || len(hits) != 1 {
		t.Fatalf("raw hits = %v err=%v", hits, err)
	}
	seeds, legs, err := store.SearchSeeds("SQLite", SeedOptions{Scope: testScope, Reader: "deliverable"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(seeds) == 0 || legs["substring"] == 0 || legs["lexical"] != 0 {
		t.Fatalf("seeds = %v legs = %v", seeds, legs)
	}
}

func TestTokenCorrectionLeg(t *testing.T) {
	store := newTestStore(t, testScope)
	store.Config.Recall.Vector = false
	if _, err := store.EnsureNode(testScope, "referent", "tournament", NodeOptions{}); err != nil {
		t.Fatalf("ensure node: %v", err)
	}
	off, offLegs, err := store.SearchSeeds("torunament", SeedOptions{
		Scope: testScope, Reader: "deliverable",
	})
	if err != nil {
		t.Fatalf("search off: %v", err)
	}
	// With correction off the misspelt token stays a lexical miss; the 16-byte fuzzy leg may
	// still nominate the node, but only inside its low increment band (below every lexical
	// band) and never with the token_correct leg.
	if offLegs["token_correct"] != 0 {
		t.Fatalf("off legs = %v", offLegs)
	}
	for _, seed := range off {
		if seed.Score >= 0.55 {
			t.Fatalf("uncorrected seed reached the lexical band: %+v", seed)
		}
		if _, corrected := seed.Legs["token_correct"]; corrected {
			t.Fatalf("uncorrected seed carries the token_correct leg: %+v", seed)
		}
	}
	on, onLegs, err := store.SearchSeeds("torunament", SeedOptions{
		Scope: testScope, Reader: "deliverable", TokenCorrect: boolPtr(true),
	})
	if err != nil {
		t.Fatalf("search on: %v", err)
	}
	if len(on) == 0 || on[0].Label != "tournament" || onLegs["token_correct"] == 0 {
		t.Fatalf("on seeds = %v legs=%v", on, onLegs)
	}
	if on[0].Score < 0.55 {
		t.Fatalf("corrected seed stayed below the lexical band: %+v", on[0])
	}
}

func TestPreRetrievalAndLexicalScoringShannonBitField(t *testing.T) {
	s := newTestStore(t, testScope)
	s.Config.Recall.Vector = false

	// 1. Pre-retrieval SearchSeeds: 1-char and 2-char Chinese words carry 10..20 Shannon bits (>= 4.0 bits)
	// and receive full entropy-scaled substring seed scores (0.75 for 20-bit 2-char CJK concept).
	if _, err := s.EnsureNode(testScope, "referent", "人工智能峰会", NodeOptions{Audience: "public"}); err != nil {
		t.Fatalf("ensure CJK node: %v", err)
	}
	seeds, legs, err := s.SearchSeeds("峰会", SeedOptions{Scope: testScope, Reader: "deliverable"})
	if err != nil {
		t.Fatalf("SearchSeeds CJK: %v", err)
	}
	if len(seeds) == 0 || legs["substring"] == 0 || seeds[0].Score < 0.74 {
		t.Fatalf("expected 2-char CJK token '峰会' (20 bits) to hit substring seed with score >= 0.74, got seeds=%+v legs=%v", seeds, legs)
	}

	// 2. Lexical scoring via Shannon Bit-Field:
	//   - Prefix derivation ("unhappy" <-> "happy") & Japanese Kanji+Kana ("食べる" <-> "食べた") must outrank
	//   - Over-stemming false positive ("organ" vs "organization") and single-letter onset coincidence ("range" vs "orange").
	subjID := "n_subj"
	cands := []Candidate{
		{
			Data: CandidateRow{
				Assertion: Assertion{NodeID: "a_prefix_hit", Scope: testScope, SubjectID: &subjID, Status: "active"},
				SLabel:    "Alice", XLabel: "feel", OLabel: "very unhappy about the delay",
			},
		},
		{
			Data: CandidateRow{
				Assertion: Assertion{NodeID: "a_overstem_fp", Scope: testScope, SubjectID: &subjID, Status: "active"},
				SLabel:    "Alice", XLabel: "join", OLabel: "the world health organization",
			},
		},
	}
	ScoreCandidatesLexical(cands, "Alice happy organ")
	if cands[0].LexScore <= cands[1].LexScore {
		t.Fatalf("expected prefix derivation hit ('happy'->'unhappy', lex=%.2f) to strictly outscore over-stemming false positive ('organ'->'organization', lex=%.2f)",
			cands[0].LexScore, cands[1].LexScore)
	}
	if s.Warning() != nil {
		t.Fatalf("expected nil Warning() on healthy local store, got %v", s.Warning())
	}
}
