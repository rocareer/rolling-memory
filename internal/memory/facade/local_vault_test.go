// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package facade_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rocareer/rolling-memory/internal/episodicunit"
	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/memory/facade"
	"github.com/rocareer/rolling-memory/sdk/go/public/tokenzr"
)

// TestThinBrainRememberStripsProtocolEnvelopes pins the write-side denoise contract:
// a turn handed to `remember` (MCP/CLI) is encoded into the L2 pulse with only what
// the speaker said — `[thinking]` / `[tool_use …]` / `[tool_result]` envelopes and
// injected context never reach the local vault row.
func TestThinBrainRememberStripsProtocolEnvelopes(t *testing.T) {
	memory := newVaultOnlyMemory(t)

	content := "[thinking] 先看 store 包断言。\n[tool_use Bash] {\"cmd\":\"go test ./internal/ids/\"}\n" +
		"[tool_result] ok  github.com/rocareer/rolling-memory/internal/ids\n\n" +
		"用户真正说的话：缓存按 docID 失效。"
	if _, err := memory.Remember(facade.RememberRequest{Content: &content, Session: "s1"}); err != nil {
		t.Fatalf("thin-brain remember: %v", err)
	}
	var stored string
	if err := memory.Store.DB().QueryRow(
		`SELECT raw_text FROM rm_units WHERE layer = 'episodic' ORDER BY occurred_at DESC, doc_id DESC LIMIT 1`,
	).Scan(&stored); err != nil {
		t.Fatalf("local vault episodic row: %v", err)
	}
	if !strings.Contains(stored, "用户真正说的话：缓存按 docID 失效。") {
		t.Fatalf("remember dropped real speech: %q", stored)
	}
	for _, noise := range []string{"[thinking]", "[tool_use", "[tool_result]"} {
		if strings.Contains(stored, noise) {
			t.Fatalf("remember kept protocol envelope %q: %q", noise, stored)
		}
	}
}

// TestThinBrainRecallCrystallizesNoeticInRealTime pins the L3 contract of the read path:
// one agent recall that lands on real units stores the local `rm_noetic` handle
// (query + cited units + sentences) in real time.
func TestThinBrainRecallCrystallizesNoeticInRealTime(t *testing.T) {
	memory := newVaultOnlyMemory(t)

	content := "Glasgow hotel cost 120 GBP per night."
	receipt, err := memory.Remember(facade.RememberRequest{Content: &content, Session: "s1"})
	if err != nil {
		t.Fatalf("thin-brain remember: %v", err)
	}
	noise := "Weekly grocery shopping receipt for milk and bread in London."
	if _, err := memory.Remember(facade.RememberRequest{Content: &noise, Session: "s1"}); err != nil {
		t.Fatalf("thin-brain remember noise: %v", err)
	}
	written, _ := receipt["written"].([]any)
	if len(written) != 1 {
		t.Fatalf("thin-brain remember receipt = %+v", receipt)
	}
	seeded, _ := written[0].(map[string]any)["id"].(string)
	if seeded == "" {
		t.Fatalf("thin-brain remember wrote no unit id: %+v", receipt)
	}

	result, err := memory.Recall(facade.RecallRequest{Cue: "Glasgow hotel cost GBP", RawLimit: 5})
	if err != nil {
		t.Fatalf("thin-brain recall: %v", err)
	}
	if result.Absent || len(result.Bands) == 0 {
		t.Fatalf("expected delivered bands from the ingested unit, got %+v", result)
	}

	if !facade.WaitForPendingNoeticPulses(5 * time.Second) {
		t.Fatalf("crystallization push did not drain within 5s")
	}

	var noetic int
	if err := memory.Store.DB().QueryRow(
		`SELECT count(*) FROM rm_noetic WHERE status = 'active'`).Scan(&noetic); err != nil {
		t.Fatalf("local noetic table: %v", err)
	}
	if noetic != 1 {
		t.Fatalf("expected one local noetic handle, got %d", noetic)
	}

	ordered, err := memory.Recall(facade.RecallRequest{
		Cue: "Glasgow hotel cost GBP", RawLimit: 5, NoCrystallize: true,
	})
	if err != nil {
		t.Fatalf("ordered recall: %v", err)
	}
	if len(ordered.Bands) < 2 || ordered.Bands[0]["kind"] != "cell_band" {
		t.Fatalf("raw evidence must lead delivery, got %+v", ordered.Bands)
	}
	for _, band := range ordered.Bands {
		if band["kind"] != "noetic_band" {
			continue
		}
		nodes := band["nodes"].([]map[string]any)
		rendered := nodes[0]["content"].(string)
		if strings.Contains(rendered, "120 GBP") || !strings.Contains(rendered, "Cited units:") {
			t.Fatalf("Noetic must render as a compact handle, got %q", rendered)
		}
	}

	// Passive/hook callers with NoCrystallize=true must never create additional Noetic records.
	if _, err := memory.Recall(facade.RecallRequest{
		Cue:           "用户的提问 被当成 noetic了? 这合理吗?",
		RawLimit:      5,
		NoCrystallize: true,
	}); err != nil {
		t.Fatalf("passive NoCrystallize recall: %v", err)
	}
	if err := memory.Store.DB().QueryRow(
		`SELECT count(*) FROM rm_noetic WHERE status = 'active'`).Scan(&noetic); err != nil {
		t.Fatalf("local noetic table after NoCrystallize: %v", err)
	}
	if noetic != 1 {
		t.Fatalf("NoCrystallize recall must not write local noetic row, got %d", noetic)
	}
}

// TestRememberNoeticPushesBlindPulseToEngine pins the explicit L3 write path: the
// `RememberNoetic` facade keeps the local plaintext handle and enforces scope isolation.
func TestRememberNoeticPushesBlindPulseToEngine(t *testing.T) {
	memory := newMemory(t)

	id, err := memory.RememberNoetic("mem:other", "agent", "Glasgow hotel cost GBP", nil, nil, "secret answer")
	if err == nil || id != "" {
		t.Fatalf("cross-scope noetic = %q, %v; want rejection", id, err)
	}
	var tables int
	if err := memory.Store.DB().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'rm_noetic'`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("cross-scope noetic initialized vault: tables=%d err=%v", tables, err)
	}

	noeticID, err := memory.RememberNoetic("mem:test", "agent", "Glasgow hotel cost GBP",
		[]string{"DOC1"}, []string{"the Glasgow hotel cost 120 GBP"},
		"the Glasgow hotel cost 120 GBP")
	if err != nil {
		t.Fatalf("RememberNoetic: %v", err)
	}
	if !strings.HasPrefix(noeticID, "nt_") {
		t.Fatalf("noetic id = %q", noeticID)
	}
}

// TestLocalVaultSharesCacheWithoutSharingPersonaState proves that repeated
// facades reuse blind-slot caching through independent Vault clones instead of
// racing one global persona identity across parallel incarnations.
func TestLocalVaultSharesCacheWithoutSharingPersonaState(t *testing.T) {
	memory := newMemory(t)
	first, err := memory.LocalVault()
	if err != nil {
		t.Fatalf("first LocalVault: %v", err)
	}
	second, err := memory.LocalVault()
	if err != nil {
		t.Fatalf("second LocalVault: %v", err)
	}
	if first.Vault == second.Vault {
		t.Fatalf("request Vaults must be independent clones")
	}
	first.SetIdentity("assistant-a", "inc-a", "locus-a")
	second.SetIdentity("assistant-b", "inc-b", "locus-b")
	if got := first.Vault.Identity(); got.IncarnationID != "inc-a" {
		t.Fatalf("first Vault identity = %+v", got)
	}
	if got := second.Vault.Identity(); got.IncarnationID != "inc-b" {
		t.Fatalf("second Vault identity = %+v", got)
	}
	if err := first.RememberNoetic("nt_inc_a", "cue a", nil, nil, "answer a", 100); err != nil {
		t.Fatalf("first RememberNoetic: %v", err)
	}
	if err := second.RememberNoetic("nt_inc_b", "cue b", nil, nil, "answer b", 101); err != nil {
		t.Fatalf("second RememberNoetic: %v", err)
	}
	for noeticID, want := range map[string]string{"nt_inc_a": "inc-a", "nt_inc_b": "inc-b"} {
		var got string
		if err := memory.Store.DB().QueryRow(
			`SELECT incarnation_id FROM rm_noetic WHERE noetic_id = ?`, noeticID,
		).Scan(&got); err != nil {
			t.Fatalf("read %s identity: %v", noeticID, err)
		}
		if got != want {
			t.Fatalf("%s incarnation = %q, want %q", noeticID, got, want)
		}
	}
}

// TestThinBrainRecallAssemblesSegmentHitsPerTurn pins the L2 delivery contract:
// segment units are ingested one per speech part, but recall hands the reader one
// assembled block per conversation turn — speaker order, contiguous window around
// the winning segment — instead of a pile of isolated lines.
func TestThinBrainRecallAssemblesSegmentHitsPerTurn(t *testing.T) {
	memory := newMemory(t)

	path := filepath.Join(t.TempDir(), "turn.jsonl")
	content := strings.Join([]string{
		`{"type":"user","sessionId":"s1","timestamp":"2026-11-06T03:00:00","message":{"role":"user","content":"请检查缓存失效。"}}`,
		`{"type":"assistant","sessionId":"s1","timestamp":"2026-11-06T03:00:10","message":{"role":"assistant","content":[` +
			`{"type":"text","text":"先确认断言。"},` +
			`{"type":"text","text":"结论：缓存按 docID 失效。"}]}}`,
		`{"type":"user","sessionId":"s1","timestamp":"2026-11-06T03:01:00","message":{"role":"user","content":"另一个话题：日志轮转。"}}`,
		`{"type":"assistant","sessionId":"s1","timestamp":"2026-11-06T03:01:10","message":{"role":"assistant","content":"日志按大小轮转。"}}`,
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write turn: %v", err)
	}
	tc, err := memory.LocalVault()
	if err != nil {
		t.Fatalf("local vault: %v", err)
	}
	type seeded struct{ text, pointer, turnRef string }
	seeds := []seeded{
		{"[user] 请检查缓存失效。", "@ref:jsonl:" + path + ":0#0", "@ref:jsonl:" + path + ":0"},
		{"[assistant] 先确认断言。", "@ref:jsonl:" + path + ":0#1", "@ref:jsonl:" + path + ":0"},
		{"[assistant] 结论：缓存按 docID 失效。", "@ref:jsonl:" + path + ":0#2", "@ref:jsonl:" + path + ":0"},
		{"[user] 另一个话题：日志轮转。", "@ref:jsonl:" + path + ":1#0", "@ref:jsonl:" + path + ":1"},
		{"[assistant] 日志按大小轮转。", "@ref:jsonl:" + path + ":1#1", "@ref:jsonl:" + path + ":1"},
	}
	for _, seed := range seeds {
		docID := episodicunit.DocID(memory.Config.Scope, seed.text)
		if _, err := tc.RememberEpisodicByRef(context.Background(), docID, seed.pointer, seed.turnRef, seed.text, 1793102400); err != nil {
			t.Fatalf("seed %q: %v", seed.text, err)
		}
	}

	result, err := memory.Recall(facade.RecallRequest{Cue: "缓存按 docID 失效 另一个话题 日志轮转", RawLimit: 10, NoCrystallize: true})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	turnIDs := make(map[string]int, len(seeds))
	for i, seed := range seeds {
		turn := 0
		if i >= 3 {
			turn = 1
		}
		turnIDs[episodicunit.DocID(memory.Config.Scope, seed.text)] = turn
	}
	turnBands := map[int]map[string]any{}
	for _, band := range result.Bands {
		if band["kind"] != "episodic_turn_band" {
			continue
		}
		id, ok := band["id"].(string)
		turn, known := turnIDs[id]
		if !ok || !known || band["subject"] != id || band["title"] != "轮·"+id {
			t.Fatalf("invalid safe turn identity: %+v", band)
		}
		if turnBands[turn] != nil {
			t.Fatalf("duplicate band for turn %d: %+v", turn, result.Bands)
		}
		turnBands[turn] = band
	}
	if len(turnBands) != 2 {
		t.Fatalf("expected one band per turn (2), got %d bands: %+v", len(turnBands), result.Bands)
	}
	for _, expected := range []struct {
		turn   int
		texts  []string
		window []int
	}{
		{0, []string{"[user] 请检查缓存失效。", "[assistant] 先确认断言。", "[assistant] 结论：缓存按 docID 失效。"}, []int{0, 2}},
		{1, []string{"[user] 另一个话题：日志轮转。", "[assistant] 日志按大小轮转。"}, []int{0, 1}},
	} {
		band := turnBands[expected.turn]
		if band == nil {
			t.Fatalf("missing turn-%d band: %+v", expected.turn, turnBands)
		}
		nodes, _ := band["nodes"].([]map[string]any)
		if len(nodes) != 1 || nodes[0]["id"] != band["id"] {
			t.Fatalf("turn band must carry one node with the matching safe identity: %+v", band)
		}
		text, _ := nodes[0]["content"].(string)
		if text != strings.Join(expected.texts, "\n") {
			t.Fatalf("turn-%d assembled content = %q, want %q", expected.turn, text, strings.Join(expected.texts, "\n"))
		}
		sourceIDs, _ := nodes[0]["source_unit_ids"].([]string)
		for i, source := range expected.texts {
			want := episodicunit.DocID(memory.Config.Scope, source)
			if len(sourceIDs) != len(expected.texts) || sourceIDs[i] != want {
				t.Fatalf("turn-%d source units = %v, want source %d to be %s", expected.turn, sourceIDs, i, want)
			}
		}
		window, _ := nodes[0]["segment_window"].([]int)
		if len(window) != 2 || window[0] != expected.window[0] || window[1] != expected.window[1] {
			t.Fatalf("turn-%d window = %v, want %v", expected.turn, window, expected.window)
		}
		wantDate := ids.FormatTimeSeconds(1793102400, "")
		if band["time_label"] != wantDate || nodes[0]["time"] != wantDate || nodes[0]["occurred_at"] != int64(1793102400) {
			t.Fatalf("turn-%d missing second-precision time provenance: band.time_label=%v node.time=%v node.occurred_at=%v wantDate=%q",
				expected.turn, band["time_label"], nodes[0]["time"], nodes[0]["occurred_at"], wantDate)
		}
	}
	renderedBlock := result.Render(4000)
	if !strings.Contains(renderedBlock, "（"+ids.FormatTimeSeconds(1793102400, "")+" · ") {
		t.Fatalf("rendered episodic turn block missing time in header: %s", renderedBlock)
	}
	payload, err := json.Marshal(result.ToDict())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), path) || strings.Contains(string(payload), "@ref:") {
		t.Fatalf("source pointer leaked: %s", payload)
	}

	// A linked unit whose source changed must not be inherited from its neighbor.
	changed := strings.Replace(content, "先确认断言。", "changed source secret", 1)
	if err := os.WriteFile(path, []byte(changed), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err = memory.Recall(facade.RecallRequest{Cue: "缓存按 docID 失效", RawLimit: 10, NoCrystallize: true})
	if err != nil {
		t.Fatal(err)
	}
	payload, err = json.Marshal(result.ToDict())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "changed source secret") || strings.Contains(string(payload), path) {
		t.Fatalf("unverified source leaked: %s", payload)
	}
	if result.Gaps["source_unavailable"] == 0 {
		t.Fatalf("missing source gap: %+v", result.Gaps)
	}
}

// TestThinBrainRememberChunksLongText pins the write-path unit-size contract: a
// text wider than the tokenz-r chunk rung is ingested as several content-addressed
// units (repeat calls are idempotent), while a text that fits the rung keeps its
// historical single-unit id.
func TestThinBrainRememberChunksLongText(t *testing.T) {
	memory := newVaultOnlyMemory(t)

	var b strings.Builder
	for i := 0; i < 900; i++ {
		fmt.Fprintf(&b, "Chapter %04d: shipment %04d left the depot for route %04d. ", i, i, i)
	}
	long := strings.TrimSpace(b.String())
	if got := tokenzr.Count(long); got <= tokenzr.DefaultChunkTokens {
		t.Fatalf("fixture must exceed the chunk rung: %d tokens", got)
	}
	receipt, err := memory.Remember(facade.RememberRequest{Content: &long, Session: "s1"})
	if err != nil {
		t.Fatalf("remember long text: %v", err)
	}
	written, _ := receipt["written"].([]any)
	if len(written) < 2 {
		t.Fatalf("long text must be split into several units, got %d: %+v", len(written), receipt)
	}
	unitIDs := make(map[string]bool, len(written))
	for _, item := range written {
		unit, _ := item.(map[string]any)
		id, _ := unit["id"].(string)
		if id == "" || unitIDs[id] {
			t.Fatalf("unit ids must be non-empty and distinct: %+v", written)
		}
		unitIDs[id] = true
	}
	trace, _ := receipt["trace"].(map[string]any)
	firstID, _ := written[0].(map[string]any)["id"].(string)
	if trace["hash"] != firstID || trace["chunks"] != len(written) {
		t.Fatalf("trace must point at the first chunk and count all units: %+v", trace)
	}

	placeholders := strings.TrimRight(strings.Repeat("?,", len(unitIDs)), ",")
	args := make([]any, 0, len(unitIDs))
	for id := range unitIDs {
		args = append(args, id)
	}
	rows, err := memory.Store.DB().Query(
		`SELECT raw_text FROM rm_units WHERE doc_id IN (`+placeholders+`)`, args...)
	if err != nil {
		t.Fatalf("read stored units: %v", err)
	}
	defer rows.Close()
	stored := 0
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			t.Fatalf("scan stored unit: %v", err)
		}
		stored++
		if got := tokenzr.Count(raw); got > tokenzr.DefaultChunkTokens {
			t.Fatalf("stored unit exceeds the chunk rung: %d tokens", got)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate stored units: %v", err)
	}
	if stored != len(unitIDs) {
		t.Fatalf("stored %d units for %d ids", stored, len(unitIDs))
	}

	again, err := memory.Remember(facade.RememberRequest{Content: &long, Session: "s1"})
	if err != nil {
		t.Fatalf("remember long text again: %v", err)
	}
	writtenAgain, _ := again["written"].([]any)
	if len(writtenAgain) != len(written) {
		t.Fatalf("repeat ingest changed unit count: %d -> %d", len(written), len(writtenAgain))
	}
	for i := range writtenAgain {
		if writtenAgain[i].(map[string]any)["id"] != written[i].(map[string]any)["id"] {
			t.Fatalf("repeat ingest changed unit id at %d", i)
		}
	}

	short := "Glasgow hotel cost 120 GBP per night."
	shortReceipt, err := memory.Remember(facade.RememberRequest{Content: &short, Session: "s1"})
	if err != nil {
		t.Fatalf("remember short text: %v", err)
	}
	shortWritten, _ := shortReceipt["written"].([]any)
	if len(shortWritten) != 1 {
		t.Fatalf("short text must stay one unit: %+v", shortReceipt)
	}
	scope, _ := shortReceipt["scope"].(string)
	wantID := "rm_" + ids.SHA256Hex(scope + "\x00" + "s1" + "\x00" + short)[:24]
	if got := shortWritten[0].(map[string]any)["id"]; got != wantID {
		t.Fatalf("single-unit id drifted: got %v want %s", got, wantID)
	}
}
