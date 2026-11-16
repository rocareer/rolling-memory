// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocareer/rolling-memory/internal/api"
	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/episodicunit"
	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/thinclient"
)

// TestLiveUpstreamThinBrainFacade_UserID10 verifies that `api.Memory` in thin-client mode
// (`ROLLING_MEMORY_BRAIN_URL`, `ROLLING_MEMORY_USER_ID="10"`)
// executes `Remember` and `Recall` end-to-end against the live `/open/memory/v1` service under `user_id=10`.
func TestLiveUpstreamThinBrainFacade_UserID10(t *testing.T) {
	baseURL := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_BRAIN_URL"))
	liveKey := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_BRAIN_KEY"))
	if baseURL == "" && liveKey == "" {
		t.Skip("ROLLING_MEMORY_BRAIN_URL / ROLLING_MEMORY_BRAIN_KEY not set; skipping live upstream facade test in local mode")
	}
	if baseURL == "" {
		// The end-side memory plane is the data plane; the retired proxied path
		// and its mem_* keys are gone.
		baseURL = "http://127.0.0.1:" + thinclient.DefaultBrainDataPlanePort + "/open/memory/v1"
	}
	resp, err := http.Get(baseURL + "/health")
	if err != nil {
		t.Skipf("live service %s not reachable (%v); skipping live facade test", baseURL, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Skipf("live service %s returned %d; skipping live facade test", baseURL, resp.StatusCode)
	}

	t.Setenv("ROLLING_MEMORY_BRAIN_URL", baseURL)
	t.Setenv("ROLLING_MEMORY_USER_ID", "10")
	// No key override: the data-plane credential resolves from the machine
	// account state (cloudcred) like every other end-side live path; skip when
	// the host has neither an ambient nor a durable credential.
	t.Setenv("ROLLING_MEMORY_BRAIN_KEY", "")

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "facade_u10.db")
	cfgPath := filepath.Join(dir, "config.toml")
	mode, scope := "cloud", "mem:self"
	cfg, err := config.Load(config.LoadOptions{
		Path:  &cfgPath,
		DB:    &dbPath,
		Mode:  &mode,
		Scope: &scope,
	})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	mem, err := api.New(cfg)
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	defer mem.Close()

	if !mem.IsThinBrainProxy() {
		t.Fatalf("expected mem.IsThinBrainProxy() == true when ROLLING_MEMORY_BRAIN_URL is set")
	}

	content := "开发演示默认绑定 user_id=10，全部记忆计算走 /open/memory/v1 接口。"
	rec, err := mem.Remember(api.RememberRequest{
		Content: &content,
		Session: "u10-demo-session",
	})
	if err != nil {
		t.Fatalf("mem.Remember against live upstream (user_id=10) failed: %v", err)
	}
	if ok, _ := rec["ok"].(bool); !ok {
		t.Fatalf("expected ok=true in receipt, got %+v", rec)
	}

	res, err := mem.Recall(api.RecallRequest{
		Cue:      "开发演示 记忆引擎 user_id=10 /open/memory/v1",
		RawLimit: 5,
	})
	if err != nil {
		t.Fatalf("mem.Recall against live upstream (user_id=10) failed: %v", err)
	}
	if len(res.Bands) == 0 || len(res.RawHits) == 0 {
		t.Fatalf("expected non-empty bands and rawHits from live upstream user_id=10, got %+v", res)
	}
	t.Logf("✓ Live api.Memory -> Upstream /open/memory/v1 (user_id=10) Recall Hit: %v", res.RawHits[0]["text"])
}

// noeticEngineMock is a stand-in for the Autonoetic Engine `/open/memory/v1` service: it
// records every blind `ingest` pulse and answers `query` with the units it has ingested.
type noeticEngineMock struct {
	mu         sync.Mutex
	ingests    []thinclient.BlindPulsePacket
	queryDelay time.Duration
	// ingestDelay models the engine's memory-engine ingest cost (measured 200-1200ms live);
	// it is what a recall must never wait behind.
	ingestDelay time.Duration
}

func (m *noeticEngineMock) ingestsSnapshot() []thinclient.BlindPulsePacket {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]thinclient.BlindPulsePacket(nil), m.ingests...)
}

// noeticPulses filters the recorded ingests down to the L3 crystallization pulses.
func (m *noeticEngineMock) noeticPulses() []thinclient.BlindPulsePacket {
	pulses := []thinclient.BlindPulsePacket{}
	for _, pkt := range m.ingestsSnapshot() {
		if pkt.Layer == thinclient.LayerNoetic {
			pulses = append(pulses, pkt)
		}
	}
	return pulses
}

// newNoeticEngineMock wires one httptest engine and returns its `/open/memory/v1` base URL.
func newNoeticEngineMock(t *testing.T) (*noeticEngineMock, string) {
	t.Helper()
	mock := &noeticEngineMock{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/open/memory/v1/query":
			if mock.queryDelay > 0 {
				time.Sleep(mock.queryDelay)
			}
			hits := []map[string]any{}
			unitIDs := []string{}
			for _, pkt := range mock.ingestsSnapshot() {
				if pkt.Layer != thinclient.LayerEpisodic {
					continue
				}
				hits = append(hits, map[string]any{
					"id": pkt.UnitID, "layer": pkt.Layer, "address_uri": "C6:L1:B001#00",
					"wave_score": 12.5, "final_weight": 22.5,
				})
				unitIDs = append(unitIDs, pkt.UnitID)
			}
			// Envelope mode (cluster_groups delivery view, protocol slice S4): the
			// engine wraps hits in the object shell with the read-only regrouping
			// and the ordinal day annotation riding along.
			if request.URL.Query().Get("envelope") == "1" {
				groups := []map[string]any{}
				if len(unitIDs) > 0 {
					groups = append(groups, map[string]any{
						"group_id": 1, "day": "2024-05-03", "unit_ids": unitIDs,
						"peak_unit_id": unitIDs[0], "joint_energy_nats": 22.5,
					})
				}
				_ = json.NewEncoder(writer).Encode(map[string]any{
					"success": true, "count": len(hits), "hits": hits,
					"cluster_groups": groups,
					"ordinal_annotation": map[string]any{
						"polarity": "begin", "resolved_day": "2024-05-03",
						"single_run": true, "run_span_days": 4, "anchor_df": 59,
					},
				})
				return
			}
			_ = json.NewEncoder(writer).Encode(hits)
		case "/open/memory/v1/ingest":
			if mock.ingestDelay > 0 {
				time.Sleep(mock.ingestDelay)
			}
			var pkt thinclient.BlindPulsePacket
			if err := json.NewDecoder(request.Body).Decode(&pkt); err != nil {
				http.Error(writer, err.Error(), http.StatusBadRequest)
				return
			}
			mock.mu.Lock()
			mock.ingests = append(mock.ingests, pkt)
			mock.mu.Unlock()
			_ = json.NewEncoder(writer).Encode(thinclient.CloudIngestResponse{
				UnitID: pkt.UnitID, Layer: pkt.Layer,
				AddressURI: "C6:L3:B001#01", WaveformBar: "▂▇▂▂▇█▁▆▆█▁█",
				KeptSentenceIndices: []int{0},
			})
		default:
			http.Error(writer, "unexpected path "+request.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return mock, server.URL + "/open/memory/v1"
}

// thinBrainMemory points one test memory at the mock engine (`Mode=cloud` + `/open/memory/v1`
// base URL), which is what `Memory.IsThinBrainProxy()` keys on.
func thinBrainMemory(t *testing.T, baseURL string) *api.Memory {
	t.Helper()
	t.Setenv("ROLLING_MEMORY_BRAIN_KEY", "mem2.test-key")
	memory := newMemory(t)
	memory.Config.Mode = "cloud"
	memory.Config.Cloud.Enabled = true
	memory.Config.Cloud.BaseURL = baseURL
	return memory
}

// TestThinBrainRememberStripsProtocolEnvelopes pins the write-side denoise contract:
// a turn handed to `remember` (MCP/CLI) is encoded into the L2 pulse with only what
// the speaker said — `[thinking]` / `[tool_use …]` / `[tool_result]` envelopes and
// injected context never reach the blind pulse nor the local vault row.
func TestThinBrainRememberStripsProtocolEnvelopes(t *testing.T) {
	_, baseURL := newNoeticEngineMock(t)
	memory := thinBrainMemory(t, baseURL)

	content := "[thinking] 先看 store 包断言。\n[tool_use Bash] {\"cmd\":\"go test ./internal/ids/\"}\n" +
		"[tool_result] ok  github.com/rocareer/rolling-memory/internal/ids\n\n" +
		"用户真正说的话：缓存按 docID 失效。"
	if _, err := memory.Remember(api.RememberRequest{Content: &content, Session: "s1"}); err != nil {
		t.Fatalf("thin-brain remember: %v", err)
	}
	var stored string
	if err := memory.Store.DB().QueryRow(
		`SELECT raw_text FROM thin_vault_docs WHERE layer = 'episodic' ORDER BY occurred_at DESC, doc_id DESC LIMIT 1`,
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
// one agent recall that lands on real units must, in the same breath, store the local
// `thin_vault_noetic` handle (query + cited units + sentences) and push the blind
// `layer="noetic"` pulse with `cited_unit_ids`, so the engine materializes the Noetic
// node and its `cites` edges while the retrieval is still hot.
func TestThinBrainRecallCrystallizesNoeticInRealTime(t *testing.T) {
	mock, baseURL := newNoeticEngineMock(t)
	memory := thinBrainMemory(t, baseURL)

	content := "Glasgow hotel cost 120 GBP per night."
	receipt, err := memory.Remember(api.RememberRequest{Content: &content, Session: "s1"})
	if err != nil {
		t.Fatalf("thin-brain remember: %v", err)
	}
	noise := "Weekly grocery shopping receipt for milk and bread in London."
	if _, err := memory.Remember(api.RememberRequest{Content: &noise, Session: "s1"}); err != nil {
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

	result, err := memory.Recall(api.RecallRequest{Cue: "Glasgow hotel cost GBP", RawLimit: 5})
	if err != nil {
		t.Fatalf("thin-brain recall: %v", err)
	}
	if result.Absent || len(result.Bands) == 0 {
		t.Fatalf("expected delivered bands from the ingested unit, got %+v", result)
	}

	// The crystallization push is scheduled off the read path (a recall must not pay
	// engine ingest latency), so the contract is "same recall, eventual pulse": drain
	// the scheduled work instead of sleeping.
	if !api.WaitForPendingNoeticPulses(5 * time.Second) {
		t.Fatalf("crystallization push did not drain within 5s")
	}
	pulses := mock.noeticPulses()
	if len(pulses) != 1 {
		t.Fatalf("expected exactly one noetic crystallization pulse, got %d (%+v)", len(pulses), pulses)
	}
	pulse := pulses[0]
	if len(pulse.CitedUnitIDs) != 1 || pulse.CitedUnitIDs[0] != seeded {
		t.Fatalf("noetic pulse cited_unit_ids = %v (seeded %q)", pulse.CitedUnitIDs, seeded)
	}
	if strings.HasPrefix(pulse.UnitID, "nt_") {
		// The pulse carries the blind payload in the single on-wire encoding.
		atoms, offsets, err := thinclient.DecodePulsePayloadB64(pulse.AtomsB64)
		if err != nil || len(atoms) == 0 || len(offsets) == 0 {
			t.Fatalf("noetic pulse payload = atoms=%d offsets=%d err=%v", len(atoms), len(offsets), err)
		}
	} else {
		t.Fatalf("noetic pulse unit id = %q", pulse.UnitID)
	}

	var noetic int
	if err := memory.Store.DB().QueryRow(
		`SELECT count(*) FROM thin_vault_noetic WHERE status = 'active'`).Scan(&noetic); err != nil {
		t.Fatalf("local noetic table: %v", err)
	}
	if noetic != 1 {
		t.Fatalf("expected one local noetic handle, got %d", noetic)
	}

	ordered, err := memory.Recall(api.RecallRequest{
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

	// Passive/hook callers with NoCrystallize=true must never create additional Noetic records or pulses.
	if _, err := memory.Recall(api.RecallRequest{
		Cue:           "用户的提问 被当成 noetic了? 这合理吗?",
		RawLimit:      5,
		NoCrystallize: true,
	}); err != nil {
		t.Fatalf("passive NoCrystallize recall: %v", err)
	}
	if len(mock.noeticPulses()) != 1 {
		t.Fatalf("NoCrystallize recall must not emit noetic pulse, got %d", len(mock.noeticPulses()))
	}
	if err := memory.Store.DB().QueryRow(
		`SELECT count(*) FROM thin_vault_noetic WHERE status = 'active'`).Scan(&noetic); err != nil {
		t.Fatalf("local noetic table after NoCrystallize: %v", err)
	}
	if noetic != 1 {
		t.Fatalf("NoCrystallize recall must not write local noetic row, got %d", noetic)
	}
}

// TestThinBrainRecallDoesNotWaitForNoeticPulse pins the read/write split of the L3 loop:
// a recall is a read, so its latency must not include the crystallization ingest. The
// engine answers the query immediately and stalls every ingest; the recall must still
// return promptly, and the pulse must exist once the scheduled work drains.
func TestThinBrainRecallDoesNotWaitForNoeticPulse(t *testing.T) {
	mock, baseURL := newNoeticEngineMock(t)
	mock.ingestDelay = 600 * time.Millisecond
	memory := thinBrainMemory(t, baseURL)

	content := "Glasgow hotel cost 120 GBP per night."
	if _, err := memory.Remember(api.RememberRequest{Content: &content, Session: "s1"}); err != nil {
		t.Fatalf("seed remember: %v", err)
	}
	if !api.WaitForPendingNoeticPulses(5 * time.Second) {
		t.Fatal("seed pulse did not drain")
	}

	started := time.Now()
	result, err := memory.Recall(api.RecallRequest{Cue: "Glasgow hotel cost GBP", RawLimit: 5})
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if result.Absent || len(result.Bands) == 0 {
		t.Fatalf("expected delivered bands, got %+v", result)
	}
	if elapsed >= mock.ingestDelay {
		t.Fatalf("recall waited for the crystallization ingest: elapsed=%s ingest=%s", elapsed, mock.ingestDelay)
	}
	if !api.WaitForPendingNoeticPulses(5 * time.Second) {
		t.Fatal("crystallization pulse did not drain")
	}
	if len(mock.noeticPulses()) != 1 {
		t.Fatalf("expected one noetic pulse after drain, got %d", len(mock.noeticPulses()))
	}
}

// TestThinBrainRecallBudgetFallsBackWithoutNoeticPulse proves that an
// unavailable cloud manifold cannot stall interactive recall for the transport
// timeout or trigger a second blocking Noetic write after local fallback.
func TestThinBrainRecallBudgetFallsBackWithoutNoeticPulse(t *testing.T) {
	mock, baseURL := newNoeticEngineMock(t)
	mock.queryDelay = 500 * time.Millisecond
	memory := thinBrainMemory(t, baseURL)
	memory.Config.Recall.BudgetMS = 40
	content := "Glasgow hotel cost 120 GBP per night."
	if _, err := memory.Remember(api.RememberRequest{Content: &content, Session: "s1"}); err != nil {
		t.Fatalf("seed remember: %v", err)
	}
	started := time.Now()
	result, err := memory.Recall(api.RecallRequest{Cue: "Glasgow hotel cost GBP", RawLimit: 5})
	if err != nil {
		t.Fatalf("budgeted recall: %v", err)
	}
	if elapsed := time.Since(started); elapsed >= 400*time.Millisecond {
		t.Fatalf("recall ignored cloud budget: %s", elapsed)
	}
	if result.Meta["degraded"] != true || result.Meta["degraded_reason"] != "engine_error_fallback_local" {
		t.Fatalf("expected visible local degradation, got %+v", result.Meta)
	}
	if len(result.Bands) == 0 {
		t.Fatalf("local fallback delivered no evidence: %+v", result)
	}
	if pulses := mock.noeticPulses(); len(pulses) != 0 {
		t.Fatalf("degraded recall must not block on a noetic pulse, got %+v", pulses)
	}
}

// TestRememberNoeticPushesBlindPulseToEngine pins the explicit L3 write path: the
// `RememberNoetic` facade keeps the local plaintext handle and forwards the blind
// `layer="noetic"` pulse with its `cited_unit_ids` to the engine.
func TestRememberNoeticPushesBlindPulseToEngine(t *testing.T) {
	mock, baseURL := newNoeticEngineMock(t)
	memory := thinBrainMemory(t, baseURL)

	id, err := memory.RememberNoetic("mem:other", "agent", "Glasgow hotel cost GBP", nil, nil, "secret answer")
	if err == nil || id != "" {
		t.Fatalf("cross-scope noetic = %q, %v; want rejection", id, err)
	}
	if pulses := mock.noeticPulses(); len(pulses) != 0 {
		t.Fatalf("cross-scope noetic emitted %d pulses", len(pulses))
	}
	var tables int
	if err := memory.Store.DB().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'thin_vault_noetic'`).Scan(&tables); err != nil || tables != 0 {
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

	pulses := mock.noeticPulses()
	if len(pulses) != 1 {
		t.Fatalf("expected one noetic ingest pulse, got %d (%+v)", len(pulses), pulses)
	}
	if pulses[0].UnitID != noeticID || len(pulses[0].CitedUnitIDs) != 1 || pulses[0].CitedUnitIDs[0] != "DOC1" {
		t.Fatalf("noetic ingest pulse = %+v", pulses[0])
	}
}

// TestThinClientSharesCacheWithoutSharingPersonaState proves that repeated
// facades reuse blind-slot caching through independent Vault clones instead of
// racing one global persona identity across parallel incarnations.
func TestThinClientSharesCacheWithoutSharingPersonaState(t *testing.T) {
	memory := newMemory(t)
	first, err := memory.ThinClient()
	if err != nil {
		t.Fatalf("first ThinClient: %v", err)
	}
	second, err := memory.ThinClient()
	if err != nil {
		t.Fatalf("second ThinClient: %v", err)
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
			`SELECT incarnation_id FROM thin_vault_noetic WHERE noetic_id = ?`, noeticID,
		).Scan(&got); err != nil {
			t.Fatalf("read %s identity: %v", noeticID, err)
		}
		if got != want {
			t.Fatalf("%s incarnation = %q, want %q", noeticID, got, want)
		}
	}
}

// TestLiveUpstreamRecallCrystallizesNoetic_UserID10 pins the live L3 loop against the real
// `/open/memory/v1` engine (user_id=10): a recall that lands on a real unit crystallizes the
// Noetic handle in the local vault AND pushes the blind `layer="noetic"` pulse, so the engine
// graph shows the new L3 node with its `cites` edge while the retrieval is still hot.
func TestLiveUpstreamRecallCrystallizesNoetic_UserID10(t *testing.T) {
	baseURL := thinclient.DefaultUpstreamBaseURL
	health, err := http.Get(baseURL + "/health")
	if err != nil {
		t.Skipf("live engine %s not reachable (%v); skipping live noetic test", baseURL, err)
	}
	_ = health.Body.Close()
	if health.StatusCode != http.StatusOK {
		t.Skipf("live engine %s returned %d; skipping live noetic test", baseURL, health.StatusCode)
	}

	t.Setenv("ROLLING_MEMORY_BRAIN_URL", baseURL)
	t.Setenv("ROLLING_MEMORY_USER_ID", "10")
	liveKey := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_BRAIN_KEY"))
	if liveKey == "" {
		t.Skip("ROLLING_MEMORY_BRAIN_KEY is not set; skipping live noetic test")
	}
	t.Setenv("ROLLING_MEMORY_BRAIN_KEY", liveKey)

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "noetic_live.db")
	cfgPath := filepath.Join(dir, "config.toml")
	mode := "cloud"
	marker := fmt.Sprintf("noetic-live-%d", time.Now().UnixNano())
	scope := "mem:" + marker
	cfg, err := config.Load(config.LoadOptions{Path: &cfgPath, DB: &dbPath, Mode: &mode, Scope: &scope})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	memory, err := api.New(cfg)
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	defer memory.Close()

	content := marker + " 洛灵自知记忆引擎的 recall 需要实时生产 noetic 认识论节点与 cites 边。"
	receipt, err := memory.Remember(api.RememberRequest{Content: &content, Session: "live-noetic"})
	if err != nil {
		t.Fatalf("live remember: %v", err)
	}
	written, _ := receipt["written"].([]any)
	if len(written) != 1 {
		t.Fatalf("live remember receipt = %+v", receipt)
	}
	seeded, _ := written[0].(map[string]any)["id"].(string)

	result, err := memory.Recall(api.RecallRequest{
		Cue: marker + " 洛灵自知记忆引擎 recall 实时生产 noetic 认识论节点", RawLimit: 5,
	})
	if err != nil {
		t.Fatalf("live recall: %v", err)
	}
	if result.Absent || len(result.Bands) == 0 {
		t.Fatalf("live recall delivered nothing: %+v", result)
	}

	var noeticID string
	if err := memory.Store.DB().QueryRow(
		`SELECT noetic_id FROM thin_vault_noetic WHERE status = 'active' ORDER BY occurred_at DESC LIMIT 1`,
	).Scan(&noeticID); err != nil {
		t.Fatalf("local noetic handle: %v", err)
	}

	client, err := memory.ThinClient()
	if err != nil {
		t.Fatalf("thin client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	includeUnits := true
	graph, err := client.ResolveGraph(ctx, thinclient.BlindGraphRequest{
		UserID: "10", Scope: strings.ReplaceAll(scope, ":", "-"),
		Layers: []string{"noetic"}, IncludeUnits: &includeUnits, MaxNodes: 200, MaxEdges: 300,
	})
	if err != nil {
		t.Fatalf("live graph: %v", err)
	}
	nodeSeen, citesSeen := false, false
	for _, node := range graph.Nodes {
		if node.RawID == noeticID {
			nodeSeen = true
		}
	}
	for _, edge := range graph.Edges {
		if edge.Kind == "cites" && strings.Contains(edge.Source, noeticID) && strings.Contains(edge.Target, seeded) {
			citesSeen = true
		}
	}
	if !nodeSeen || !citesSeen {
		t.Fatalf("engine graph missing noetic node %s (seen=%v) or its cites edge to %s (seen=%v): %+v",
			noeticID, nodeSeen, seeded, citesSeen, graph.Stats)
	}
}

// TestThinBrainRecallAssemblesSegmentHitsPerTurn pins the L2 delivery contract:
// segment units are ingested one per speech part, but recall hands the reader one
// assembled block per conversation turn — speaker order, contiguous window around
// the winning segment — instead of a pile of isolated lines.
func TestThinBrainRecallAssemblesSegmentHitsPerTurn(t *testing.T) {
	_, baseURL := newNoeticEngineMock(t)
	t.Setenv("ROLLING_MEMORY_BRAIN_URL", baseURL)
	t.Setenv("ROLLING_MEMORY_BRAIN_KEY", "test-key")
	memory := thinBrainMemory(t, baseURL)

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
	tc, err := memory.ThinClient()
	if err != nil {
		t.Fatalf("thin client: %v", err)
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

	result, err := memory.Recall(api.RecallRequest{Cue: "缓存按 docID 失效", RawLimit: 10, NoCrystallize: true})
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
	result, err = memory.Recall(api.RecallRequest{Cue: "缓存按 docID 失效", RawLimit: 10, NoCrystallize: true})
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

// TestThinBrainRecallClusterGroupsMeta pins delivery-protocol slice S4b: on the
// HTTP thin-brain proxy arm, recall requests the cognitive-cluster delivery
// view and surfaces it to the caller via Result.Meta (cluster_groups +
// ordinal_annotation), referencing only unit IDs already present in the
// delivery; the hits/bands themselves stay untouched.
func TestThinBrainRecallClusterGroupsMeta(t *testing.T) {
	_, baseURL := newNoeticEngineMock(t)
	memory := thinBrainMemory(t, baseURL)

	content := "Lisbon harbor walk with grilled sardines near the tram stop."
	if _, err := memory.Remember(api.RememberRequest{Content: &content, Session: "s-cg"}); err != nil {
		t.Fatalf("remember: %v", err)
	}

	result, err := memory.Recall(api.RecallRequest{Cue: "first meal in Lisbon", NoCrystallize: true})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	groups, ok := result.Meta["cluster_groups"].([]thinclient.CloudHitClusterGroup)
	if !ok || len(groups) == 0 {
		t.Fatalf("cluster_groups missing from recall meta: %#v", result.Meta["cluster_groups"])
	}
	if groups[0].PeakUnitID == "" || len(groups[0].UnitIDs) == 0 {
		t.Fatalf("malformed cluster group: %+v", groups[0])
	}
	ann, ok := result.Meta["ordinal_annotation"].(*thinclient.CloudOrdinalAnnotation)
	if !ok || ann == nil || ann.Polarity != "begin" || ann.ResolvedDay != "2024-05-03" || !ann.SingleRun {
		t.Fatalf("ordinal annotation missing or drifted: %#v", result.Meta["ordinal_annotation"])
	}
}
