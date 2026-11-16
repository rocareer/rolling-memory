// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package recall

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/memcore"
	"github.com/rocareer/rolling-memory/internal/store"
)

func TestFlatItemDeliveryResidualSubtractionAndRankSwitch(t *testing.T) {
	// 1. ReconcileClauseEventDate calibrates relative clause date only when turn already has a cross-day shift
	if got := ReconcileClauseEventDate("Last Friday, I got great news about the program!", "2024-02-01", "2024-01-07"); got != "2024-01-05" {
		t.Fatalf("expected 'Last Friday' spoken on 2024-01-07 (with turn shift 2024-02-01) to calibrate to 2024-01-05, got %q", got)
	}
	if got := ReconcileClauseEventDate("New York City trip on July 2", "2024-01-07", "2024-01-07"); got != "2024-01-07" {
		t.Fatalf("expected occDate==spkDate regular clause to stay 2024-01-07, got %q", got)
	}

	// 2. Subordinate structured memory assertion folding + Text(t) \ structured memory assertion(t) local residual subtraction + rank=true vs rank=false
	bands := []map[string]any{
		{
			"id":      "mem:band:1",
			"kind":    "subject",
			"subject": "John",
			"nodes": []map[string]any{
				{
					"id":           "a_1",
					"content":      "John —says→ Jill will be delighted",
					"anchor":       "trace:t1#0-35",
					"status":       "active",
					"t_valid_from": int64(1704585600), // 2024-01-07
					"t_source":     int64(1704585600),
					"time":         "2024-01-07",
					"score":        0.95,
				},
				{
					"id":           "a_2",
					"content":      "Jill will be delighted",
					"anchor":       "trace:t1#10-35",
					"status":       "active",
					"t_valid_from": int64(1704585600),
					"t_source":     int64(1704585600),
					"time":         "2024-01-07",
					"score":        0.80,
				},
				{
					"id":           "a_3",
					"content":      "a woman and two dogs —includes→ two dogs",
					"anchor":       "trace:t1#0-10",
					"status":       "active",
					"t_valid_from": int64(1704585600),
					"t_source":     int64(1704585600),
					"time":         "2024-01-07",
					"score":        0.75,
				},
			},
		},
	}
	usable := []store.Candidate{
		{
			Data: store.CandidateRow{
				Assertion: store.Assertion{
					NodeID:          "a_1",
					Status:          "active",
					AnchorTraceHash: "t1_hash_12345678",
					TSource:         1704585600,
					TValidFrom:      1704585600,
				},
				TraceText: "John: Jill will be delighted. For both you and your Jill, I also booked the Chicago concert!",
			},
			Score: 0.95,
		},
	}
	rawHits := []map[string]any{
		{
			"trace":       "t2_code",
			"session":     "s1",
			"turn":        int64(2),
			"occurred_at": int64(1704672000),
			"text":        "func VerifyEd25519JWT(token string) bool {\n    return ed25519.Verify(pubKey, msg, sig)\n}",
		},
	}

	grouped := BuildFlatItemDelivery(bands, rawHits, usable, "VerifyEd25519JWT ed25519 Chicago", 0, 10, true)
	if len(grouped) != 3 {
		t.Fatalf("expected 3 flat items (1 folded structured memory assertion + 1 uncovered unit residual + 1 code block), got %d: %#v", len(grouped), grouped)
	}
	// Under rankGroup=true, structured memory assertion comes first, then unit residual, then code block
	if !strings.Contains(asString(grouped[0]["title"]), "John —says→ Jill will be delighted") {
		t.Fatalf("expected item[0] to be folded upper structured memory assertion, got %q", asString(grouped[0]["title"]))
	}
	if !strings.Contains(asString(grouped[1]["title"]), "Chicago concert") || strings.Contains(asString(grouped[1]["title"]), "Jill will be delighted.") {
		t.Fatalf("expected item[1] to be uncovered residual (with covered sentence stripped), got %q", asString(grouped[1]["title"]))
	}
	if !strings.Contains(asString(grouped[2]["title"]), "VerifyEd25519JWT") {
		t.Fatalf("expected item[2] to be code block residual, got %q", asString(grouped[2]["title"]))
	}

	// Under rankGroup=false with query focused on VerifyEd25519JWT, relevance order puts the matching item ahead of unrelated structured memory assertion
	unranked := BuildFlatItemDelivery(bands, rawHits, usable, "VerifyEd25519JWT ed25519", 0, 10, false)
	if len(unranked) != 3 || !strings.Contains(asString(unranked[0]["title"]), "VerifyEd25519JWT") {
		t.Fatalf("expected rankGroup=false to place highest-relevance code block first, got %#v", unranked)
	}

	// 3. AsOf > 0 guard: when asOf > 0 and active structured memory assertion candidates exist, do not resurrect full-turn residuals
	asOfItems := BuildFlatItemDelivery(bands, rawHits, usable, "Chicago", 1704585600, 10, true)
	if len(asOfItems) != 1 {
		t.Fatalf("expected AsOf > 0 with active structured memory assertion to suppress full-turn residual resurrection, got %d: %#v", len(asOfItems), asOfItems)
	}
}

func TestCrossEncoderRerankScorePropagatesToItemDelivery(t *testing.T) {
	bands := []map[string]any{
		{
			"id":      "mem:band:alice",
			"kind":    "subject",
			"subject": "Alice",
			"nodes": []map[string]any{
				{
					"id":           "a1",
					"content":      "Alice —works on→ Project Alpha",
					"anchor":       "trace:ta1#0-30",
					"status":       "active",
					"t_valid_from": int64(1704067200),
					"t_source":     int64(1704067200),
					"time":         "2024-01-01",
					"score":        0.50,
				},
				{
					"id":           "a2",
					"content":      "Alice —works on→ Project Beta",
					"anchor":       "trace:ta2#0-30",
					"status":       "active",
					"t_valid_from": int64(1704153600),
					"t_source":     int64(1704153600),
					"time":         "2024-01-02",
					"score":        0.40,
				},
			},
		},
	}
	usable := []store.Candidate{
		{
			Data: store.CandidateRow{
				Assertion: store.Assertion{
					NodeID:     "a1",
					Status:     "active",
					TSource:    1704067200,
					TValidFrom: 1704067200,
				},
				SLabel: "Alice",
				XLabel: "works on",
				OLabel: "Project Alpha",
			},
			Score:       0.50,
			RerankScore: 0.35,
		},
		{
			Data: store.CandidateRow{
				Assertion: store.Assertion{
					NodeID:     "a2",
					Status:     "active",
					TSource:    1704153600,
					TValidFrom: 1704153600,
				},
				SLabel: "Alice",
				XLabel: "works on",
				OLabel: "Project Beta",
			},
			Score:       0.40,
			RerankScore: 0.92,
		},
	}
	items := BuildFlatItemDelivery(bands, nil, usable, "Alice project", 0, 10, false)
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if !strings.Contains(asString(items[0]["title"]), "Project Beta") {
		t.Fatalf("expected higher RerankScore item Project Beta first, got %#v", items[0])
	}
}

// TestFlatItemDeliveryCarriesSourceHead verifies that delivered item capsules retain
// the leading segment of their source text (`srcHead`), allowing callers to resolve
// delivered entries back to their source items.
func TestFlatItemDeliveryCarriesSourceHead(t *testing.T) {
	const head = "ID: 20250221_133131"
	src := head + "\nWe had lunch at Fancett together with Grace."

	usable := []store.Candidate{{
		Data: store.CandidateRow{
			Assertion: store.Assertion{
				NodeID:          "u1",
				Status:          "active",
				TSource:         1740100000,
				TValidFrom:      1740100000,
				AnchorTraceHash: "th1",
			},
			TraceText: src,
		},
		Score: 1.0,
	}}
	items := BuildFlatItemDelivery(nil, nil, usable, "lunch Fancett Grace", 0, 10, false)
	if len(items) == 0 {
		t.Fatal("expected at least one delivered item from the usable leg")
	}
	nodes0, _ := items[0]["nodes"].([]map[string]any)
	if len(nodes0) == 0 || !strings.Contains(asString(nodes0[0]["src_head"]), head) {
		t.Fatalf("delivered item must carry the source head %q, got %#v", head, items[0])
	}

	rawHits := []map[string]any{{
		"text":    src,
		"session": "sess-1",
		"turn":    1,
	}}
	itemsRaw := BuildFlatItemDelivery(nil, rawHits, nil, "lunch Fancett Grace", 0, 10, false)
	if len(itemsRaw) == 0 {
		t.Fatal("expected at least one delivered item from the raw leg")
	}
	nodesRaw, _ := itemsRaw[0]["nodes"].([]map[string]any)
	if len(nodesRaw) == 0 || !strings.Contains(asString(nodesRaw[0]["src_head"]), head) {
		t.Fatalf("raw-leg item must carry the source head %q, got %#v", head, itemsRaw[0])
	}
}

func TestDecisionCacheHitAndScopeEpochInvalidation(t *testing.T) {
	DecisionCacheReset()
	t.Setenv("ROLLING_MEMORY_DECISION_CACHE", "1")
	if !DecisionCacheOn() {
		t.Fatalf("expected DecisionCacheOn() = true")
	}
	optsA := Options{
		Reader:     "owner",
		AsOf:       1700000000,
		ResultUnit: "band",
	}
	optsB := optsA
	optsB.ResultUnit = "item"
	keyA := DecisionCacheKey(nil, "mem:cache:test", "where does Alice live", optsA)
	keyB := DecisionCacheKey(nil, "mem:cache:test", "where does Alice live", optsB)
	if keyA == keyB {
		t.Fatalf("expected distinct cache keys for different ResultUnit")
	}
	sample := &Result{
		Absent:  false,
		Bands:   []map[string]any{{"subject": "Alice"}},
		RawHits: []map[string]any{},
		Gaps:    map[string]int{},
	}
	DecisionCachePut(keyA, sample)
	got, ok := DecisionCacheGet(keyA)
	if !ok || got == nil || len(got.Bands) != 1 {
		t.Fatalf("expected cache hit before invalidation, got ok=%v res=%#v", ok, got)
	}
	// Mutate returned copy to verify deep-clone isolation
	got.Bands[0]["subject"] = "Mutated"
	got2, ok2 := DecisionCacheGet(keyA)
	if !ok2 || got2.Bands[0]["subject"] != "Alice" {
		t.Fatalf("expected deep-clone isolation in DecisionCache, got %#v", got2.Bands[0])
	}
	// Scope epoch bump produces a new key that misses old cache entry
	DecisionCacheBumpScope("mem:cache:test")
	keyAfter := DecisionCacheKey(nil, "mem:cache:test", "where does Alice live", optsA)
	if keyAfter == keyA {
		t.Fatalf("expected new cache key after DecisionCacheBumpScope")
	}
	if _, okAfter := DecisionCacheGet(keyAfter); okAfter {
		t.Fatalf("expected cache miss after DecisionCacheBumpScope")
	}
}

func TestDeliverableMaskingAndPremiseAttribution(t *testing.T) {
	bands := []map[string]any{
		{
			"subject": "Alice",
			"nodes": []map[string]any{
				{"id": "n0", "status": "active", "label": "Alice", "content": "Alice —likes→ coffee"},
				{"id": "n1", "status": "active", "label": "Alice", "content": "Alice —reads→ novels"},
				{"id": "n2", "status": "active", "label": "Alice", "content": "Alice —plays→ piano"},
				{"id": "n3", "status": "active", "label": "Alice", "content": "Alice —lives in→ Boston"},
				{"id": "n4", "status": "active", "label": "Alice", "content": "Alice —studies→ physics"},
			},
		},
		{
			"subject": "Bob",
			"nodes": []map[string]any{
				{"id": "b0", "status": "active", "label": "Bob", "content": "Bob —visited→ Tokyo"},
				{"id": "b1", "status": "active", "label": "Bob", "content": "Bob —stayed in→ Tokyo"},
				{"id": "b2", "status": "active", "label": "Bob", "content": "Bob —photographed→ Tokyo"},
				{"id": "b3", "status": "active", "label": "Bob", "content": "Bob —likes→ ramen"},
				{"id": "b4", "status": "active", "label": "Bob", "content": "Bob —speaks→ Japanese"},
			},
		},
	}
	note := buildPremiseAttributionNote("When did Alice visit Tokyo?", bands)
	if note == "" || !strings.Contains(note, "Bob") {
		t.Fatalf("expected premise attribution note highlighting Bob for Alice query, got %q", note)
	}

	// Test applyDeliverableMask ("shadow" vs "on", reader_subject self-exemption, world exemption)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "mask.db")
	cfgPath := filepath.Join(dir, "config.toml")
	mode := "local"
	scope := "mem:mask:test"
	reader := "deliverable"
	cfg, err := config.Load(config.LoadOptions{
		Path: &cfgPath, DB: &dbPath, Mode: &mode, Scope: &scope, Reader: &reader, Env: map[string]string{},
	})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	st, err := store.Open(cfg)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	_, _ = st.EnsureNode(scope, "referent", "Bob", store.NodeOptions{})
	_, _ = st.EnsureNode(scope, "referent", "Charlie", store.NodeOptions{})
	tokyoID, _ := st.EnsureNode(scope, "referent", "Tokyo", store.NodeOptions{})
	_, _ = st.DB().Exec(`INSERT OR REPLACE INTO node_ext(node_id, scope, incarnation_id, ext_ref, origin) VALUES (?, ?, '', '', 'world')`, tokyoID, scope)

	rawHits := []map[string]any{{"trace": "t1", "text": "Bob visited Tokyo with Charlie."}}
	meta := map[string]any{}
	cfg.Recall.MaskDeliverable = "shadow"
	applyDeliverableMask(st, cfg, scope, Options{Reader: "deliverable", ReaderSubject: "Charlie"}, bands, rawHits, meta)
	if bands[1]["subject"] != "Bob" {
		t.Fatalf("shadow mode must not mutate subject, got %v", bands[1]["subject"])
	}
	if meta["mask_delivery"] == nil {
		t.Fatalf("expected mask_delivery telemetry in shadow mode")
	}

	cfg.Recall.MaskDeliverable = "on"
	applyDeliverableMask(st, cfg, scope, Options{Reader: "deliverable", ReaderSubject: "Charlie"}, bands, rawHits, meta)
	if !strings.HasPrefix(asString(bands[1]["subject"]), "[PERSON_") {
		t.Fatalf("expected Bob masked to [PERSON_...], got %v", bands[1]["subject"])
	}
	rawTxt := asString(rawHits[0]["text"])
	if !strings.Contains(rawTxt, "[PERSON_") || !strings.Contains(rawTxt, "Charlie") || !strings.Contains(rawTxt, "Tokyo") {
		t.Fatalf("expected Bob masked while preserving reader Charlie and world Tokyo, got %q", rawTxt)
	}
}

func TestUniqueAssistantNameAndEgressViewNeutralProjection(t *testing.T) {
	cases := []struct {
		name string
		rows []AssistantNameEvidence
		want string
	}{
		{name: "self name", rows: []AssistantNameEvidence{{Alias: "Caroline", Source: "remember:self"}}, want: "Caroline"},
		{name: "explicit name", rows: []AssistantNameEvidence{{Alias: "Nova", Source: "remember:explicit"}}, want: "Nova"},
		{name: "self priority", rows: []AssistantNameEvidence{{Alias: "Nova", Source: "remember:explicit"}, {Alias: "Caroline", Source: "remember:self"}}, want: "Caroline"},
		{name: "same name repeated", rows: []AssistantNameEvidence{{Alias: "Caroline", Source: "remember:self"}, {Alias: "caroline", Source: "remember:self"}}, want: "Caroline"},
		{name: "same rank ambiguity", rows: []AssistantNameEvidence{{Alias: "Caroline", Source: "remember:self"}, {Alias: "Melanie", Source: "remember:self"}}, want: ""},
		{name: "referral is not unique real-name evidence", rows: []AssistantNameEvidence{{Alias: "医生", Source: "remember:referral"}}, want: ""},
		{name: "base roles ignored", rows: []AssistantNameEvidence{{Alias: "assistant", Source: "remember:self"}, {Alias: "self", Source: "remember:explicit"}}, want: ""},
		{name: "empty", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := UniqueAssistantNameFromEvidence(tc.rows); got != tc.want {
				t.Fatalf("UniqueAssistantNameFromEvidence() = %q, want %q", got, tc.want)
			}
		})
	}

	labels := map[uint64]string{1: "self", 2: "Caroline", 3: "self-transform"}
	texts := map[string]string{"h1": "self said self-transform and himself stay verbatim."}
	proj := ApplyEgressViewProjection(nil, nil, "mem:test:neutral", memcore.EgressViewPolicy{}, labels, texts)
	if proj[1] != "assistant" || proj[2] != "Caroline" || proj[3] != "self-transform" {
		t.Fatalf("default neutral projected labels = %+v", proj)
	}
	if texts["h1"] != "self said self-transform and himself stay verbatim." {
		t.Fatalf("expected neutral evidence text to stay verbatim, got %q", texts["h1"])
	}

	projNamed := ApplyEgressViewProjection(nil, nil, "mem:test:neutral", memcore.EgressViewPolicy{
		Mode:            memcore.EgressViewNeutral,
		SelfDisplayName: "Nova",
	}, map[uint64]string{1: "self", 2: "Caroline"}, nil)
	if projNamed[1] != "Nova" || projNamed[2] != "Caroline" {
		t.Fatalf("identified neutral projected labels = %+v", projNamed)
	}

	// Verify inline follow-up quote extraction and prevent low-score residual from displacing high-score structured memory assertion (1cb43202d)
	inlineGot := extractInlineQuotesFromLine("发生:2024-01-07 ｜ Alice —answered→ yes ｜ 追问续答:「I finished the migration yesterday」")
	if inlineGot != "I finished the migration yesterday" {
		t.Fatalf("expected inline follow-up quote extracted, got %q", inlineGot)
	}
}
