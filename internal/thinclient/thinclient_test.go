// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/rocareer/rolling-memory/internal/cloudcred"
	"github.com/rocareer/rolling-memory/internal/thinclient"
	_ "modernc.org/sqlite"
)

// TestHTTPBrainTransportDefaultsToSingleIncarnation pins the ordinary Harness
// query boundary: sibling fan-out is an explicit opt-in, never an engine default
// that can hydrate unrelated evaluation generations into a user recall.
func TestHTTPBrainTransportDefaultsToSingleIncarnation(t *testing.T) {
	var got []any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		got = append(got, payload["cross_incarnation"])
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]thinclient.CloudQueryHit{})
	}))
	defer srv.Close()

	transport := thinclient.NewHTTPBrainTransport(srv.URL, "test-token")
	if _, err := transport.QueryBlindManifold(context.Background(), thinclient.BlindPulsePacket{UnitID: "Q"}, 1); err != nil {
		t.Fatalf("default query: %v", err)
	}
	allow := true
	transport.CrossIncarnation = &allow
	if _, err := transport.QueryBlindManifold(context.Background(), thinclient.BlindPulsePacket{UnitID: "Q"}, 1); err != nil {
		t.Fatalf("opt-in query: %v", err)
	}
	if len(got) != 2 || got[0] != false || got[1] != true {
		t.Fatalf("cross_incarnation sequence = %#v, want [false true]", got)
	}
}

// TestRecallNoeticMatchesOriginalCueNotEvidence proves that a long answer or
// cited evidence cannot smuggle an unrelated Noetic record into a later query.
func TestRecallNoeticMatchesOriginalCueNotEvidence(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "noetic-cue.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	vault, err := thinclient.NewVault(db, []byte("noetic-cue-secret"))
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}
	client := thinclient.NewClient(vault, nil)
	const cue = "启动后台 admin 本地开发 vite 后端端口"
	if err := client.RememberNoetic("NT-CUE", cue, nil, nil, "原始检索句柄", 100); err != nil {
		t.Fatalf("RememberNoetic NT-CUE: %v", err)
	}
	if err := client.RememberNoetic(
		"NT-EVIDENCE-LEAK", "开箱默认账号 登录页", nil, nil,
		cue+" 以及几千字与当前检索无关的旧巡检答案", 101,
	); err != nil {
		t.Fatalf("RememberNoetic NT-EVIDENCE-LEAK: %v", err)
	}
	hits, err := client.RecallNoetic(cue, 10)
	if err != nil {
		t.Fatalf("RecallNoetic: %v", err)
	}
	if len(hits) != 1 || hits[0].NoeticID != "NT-CUE" || hits[0].MatchScore != 1 {
		t.Fatalf("Noetic cue-only matches = %+v, want only NT-CUE with Dice 1", hits)
	}
}

// TestRememberPreservesVaultWhenCloudIngestFails proves the local-first write
// contract: remote indexing failure cannot erase the only local plaintext copy.
func TestRememberPreservesVaultWhenCloudIngestFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "engine unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "local-first.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	vault, err := thinclient.NewVault(db, []byte("local-first-secret"))
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}
	client := thinclient.NewClient(vault, thinclient.NewHTTPBrainTransport(srv.URL, "test-token"))
	resp, err := client.Remember(context.Background(), "DOC-LOCAL-FIRST", "plaintext survives a cloud ingest failure", 100)
	if err == nil {
		t.Fatalf("expected remote ingest error, got %+v", resp)
	}
	raw, distilled, _, err := vault.LoadLocalDocument("DOC-LOCAL-FIRST")
	if err != nil || raw != "plaintext survives a cloud ingest failure" || distilled != raw {
		t.Fatalf("local-first document = raw %q distilled %q err %v", raw, distilled, err)
	}
	ref := "@ref:jsonl:/tmp/no-such-episodic-file:0#0"
	if _, err := client.RememberEpisodicByRef(context.Background(), "EP-LOCAL-FIRST", ref, "@ref:jsonl:/tmp/no-such-episodic-file:0", "pointer survives too", 101); err == nil || !strings.Contains(err.Error(), "engine unavailable") {
		t.Fatalf("expected original remote ingest error, got %v", err)
	}
	raw, _, _, err = vault.LoadLocalDocument("EP-LOCAL-FIRST")
	if err != nil || raw != "pointer survives too" {
		t.Fatalf("local-first plaintext = %q, err %v", raw, err)
	}

	offline := thinclient.NewClient(vault, nil)
	if _, err := offline.RememberWithLayer(context.Background(), "BG-OFFLINE", "offline background remains readable", 102, thinclient.LayerBackground, nil); err != nil {
		t.Fatalf("nil-transport local write: %v", err)
	}
	if got := vault.LoadLocalDocumentLayer("BG-OFFLINE"); got != thinclient.LayerBackground {
		t.Fatalf("offline layer = %q", got)
	}
}

func TestRememberEpisodicByRefInvalidSourcePreservesPlaintext(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, err := thinclient.NewVault(db, []byte("invalid-source-secret"))
	if err != nil {
		t.Fatal(err)
	}
	client := thinclient.NewClient(vault, nil)
	source := filepath.Join(dir, "source.jsonl")
	if err := os.WriteFile(source, []byte(`{"type":"user","message":{"role":"user","content":"different source text"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ id, ref string }{
		{"missing", "@ref:jsonl:" + filepath.Join(dir, "missing.jsonl") + ":0#0"},
		{"mismatch", "@ref:jsonl:" + source + ":0#0"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			const text = "the supplied user text"
			if _, err := client.RememberEpisodicByRef(context.Background(), tc.id, tc.ref, tc.ref, text, 1); err != nil {
				t.Fatal(err)
			}
			raw, distilled, _, err := vault.LoadLocalDocument(tc.id)
			if err != nil || raw != text || distilled != text {
				t.Fatalf("document = %q, %q, %v", raw, distilled, err)
			}
			var indexes, lineage int
			if err := db.QueryRow(`SELECT count(*) FROM thin_vault_episodic_index WHERE doc_id = ?`, tc.id).Scan(&indexes); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT count(*) FROM thin_vault_episodic_lineage WHERE doc_id = ?`, tc.id).Scan(&lineage); err != nil {
				t.Fatal(err)
			}
			if indexes != 0 || lineage != 0 {
				t.Fatalf("unverified source indexed: index=%d lineage=%d", indexes, lineage)
			}
		})
	}
	if err := vault.SaveLocalPlaintext("existing", 1, "another user's data", []string{"another user's data"}, thinclient.CloudIngestResponse{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.RememberEpisodicByRef(context.Background(), "existing", "@ref:jsonl:"+source+":0#0", "", "new user text", 2); err == nil {
		t.Fatal("expected source identity conflict")
	}
	raw, _, _, err := vault.LoadLocalDocument("existing")
	if err != nil || raw != "another user's data" {
		t.Fatalf("existing document overwritten: %q, %v", raw, err)
	}
	live, err := thinclient.ResolveEpisodicPointerLive("@ref:jsonl:" + source + ":0#0")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.RememberEpisodicByRef(context.Background(), "mismatch", "@ref:jsonl:"+source+":0#0", "", live, 2); err == nil {
		t.Fatal("verified source replaced existing inline identity")
	}
	raw, _, _, err = vault.LoadLocalDocument("mismatch")
	if err != nil || raw != "the supplied user text" {
		t.Fatalf("inline identity overwritten: %q, %v", raw, err)
	}
}

func TestRememberEpisodicByRefPreservesIdenticalInline(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, err := thinclient.NewVault(db, []byte("same-inline-secret"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "source.jsonl")
	if err := os.WriteFile(source, []byte(`{"type":"user","message":{"role":"user","content":"same text"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ref := "@ref:jsonl:" + source + ":0#0"
	text, err := thinclient.ResolveEpisodicPointerLive(ref)
	if err != nil {
		t.Fatal(err)
	}
	_, sents := vault.PackBlindPacket("same", text, 1)
	if err := vault.SaveLocalPlaintext("same", 1, text, sents, thinclient.CloudIngestResponse{Layer: thinclient.LayerEpisodic}); err != nil {
		t.Fatal(err)
	}
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Error(w, "engine unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	client := thinclient.NewClient(vault, thinclient.NewHTTPBrainTransport(srv.URL, "test-token"))
	if _, err := client.RememberEpisodicByRef(context.Background(), "same", ref, ref, text, 2); err == nil || !strings.Contains(err.Error(), "engine unavailable") {
		t.Fatalf("expected transport error after preserved inline, got %v", err)
	}
	var stored string
	var occurred int64
	var indexes int
	if err := db.QueryRow(`SELECT raw_text, occurred_at FROM thin_vault_docs WHERE doc_id = 'same'`).Scan(&stored, &occurred); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM thin_vault_episodic_index WHERE doc_id = 'same'`).Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	if stored != text || occurred != 1 || indexes != 0 || requests != 1 {
		t.Fatalf("inline changed or ingest skipped: text=%q occurred=%d indexes=%d requests=%d", stored, occurred, indexes, requests)
	}
	var completeRequests int
	complete := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		completeRequests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"unit_id":"same","layer":"episodic"}`))
	}))
	defer complete.Close()
	client = thinclient.NewClient(vault, thinclient.NewHTTPBrainTransport(complete.URL, "test-token"))
	if _, err := client.RememberEpisodicByRef(context.Background(), "same", ref, ref, text, 3); err != nil {
		t.Fatalf("successful ingest with identical inline: %v", err)
	}
	if err := db.QueryRow(`SELECT raw_text, occurred_at FROM thin_vault_docs WHERE doc_id = 'same'`).Scan(&stored, &occurred); err != nil {
		t.Fatal(err)
	}
	if stored != text || occurred != 1 || completeRequests != 1 {
		t.Fatalf("successful ingest modified inline: text=%q occurred=%d requests=%d", stored, occurred, completeRequests)
	}
}

func TestLoadLocalDocumentRefPrefixIdentity(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, err := thinclient.NewVault(db, []byte("ref-identity-secret"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "source.jsonl")
	if err := os.WriteFile(source, []byte(`{"type":"user","message":{"role":"user","content":"live text"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ref := "@ref:jsonl:" + source + ":0#0"
	live, err := thinclient.ResolveEpisodicPointerLive(ref)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"@ref: literal user text", ref} {
		id := "inline-" + text
		_, sents := vault.PackBlindPacket(id, text, 1)
		if err := vault.SaveLocalPlaintext(id, 1, text, sents, thinclient.CloudIngestResponse{}); err != nil {
			t.Fatal(err)
		}
		if got, _, _, err := vault.LoadLocalDocument(id); err != nil || got != text {
			t.Fatalf("inline %q read as %q: %v", text, got, err)
		}
	}
	if err := vault.SaveLocalEpisodicPointer("legacy", 1, ref, "", thinclient.CloudIngestResponse{}); err != nil {
		t.Fatal(err)
	}
	if got, _, _, err := vault.LoadLocalDocument("legacy"); err == nil || got != "" {
		t.Fatalf("unverified legacy source delivered %q: %v", got, err)
	}
	if err := vault.SaveLocalEpisodicPointer("lineage", 1, ref, ref, thinclient.CloudIngestResponse{}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE thin_vault_docs SET sentences_json = ? WHERE doc_id = 'lineage'`, `["@ref:jsonl:"]`); err != nil {
		t.Fatal(err)
	}
	if got, _, _, err := vault.LoadLocalDocument("lineage"); err == nil || got != "" {
		t.Fatalf("ambiguous lineage source delivered %q: %v", got, err)
	}
	client := thinclient.NewClient(vault, nil)
	if err := os.WriteFile(source, []byte(`{"type":"user","message":{"role":"user","content":"changed text"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _, _, err := vault.LoadLocalDocument("legacy"); err == nil || got != "" {
		t.Fatalf("changed unverified legacy source delivered %q: %v", got, err)
	}
	if err := os.WriteFile(source, []byte(`{"type":"user","message":{"role":"user","content":"live text"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.RememberEpisodicByRef(context.Background(), "indexed", ref, "", live, 1); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte(`{"type":"user","message":{"role":"user","content":"changed text"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE thin_vault_docs SET sentences_json = ? WHERE doc_id = 'indexed'`, `["@ref:jsonl:"]`); err != nil {
		t.Fatal(err)
	}
	if got, _, _, err := vault.LoadLocalDocument("indexed"); err == nil || got != "" {
		t.Fatalf("changed indexed source delivered %q: %v", got, err)
	}
}

// TestThinClientSQLiteAndHTTPBlindBrainEndToEnd verifies the full production flow of the local
// Ultra-Thin Client without importing any core algorithm package in Rolling-Memory:
//
//	Local SQLite Vault -> Ultra-Thin Client -> HTTP JSON Wire (0 Plaintext, 0 Floats) -> Cloud Endpoint -> Local Resolution.
func TestThinClientSQLiteAndHTTPBlindBrainEndToEnd(t *testing.T) {
	var wireMu sync.Mutex
	var capturedWirePayloads []string
	var ingestedPackets []thinclient.BlindPulsePacket

	// Mock HTTP server representing the remote Autonoetic Engine `/open/memory/v1/*` service.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		wireMu.Lock()
		capturedWirePayloads = append(capturedWirePayloads, string(bodyBytes))
		wireMu.Unlock()

		switch r.URL.Path {
		case "/open/memory/v1/ingest", "/brain/v1/ingest":
			var pkt thinclient.BlindPulsePacket
			if err := json.Unmarshal(bodyBytes, &pkt); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if err := normalizePulseForTest(&pkt); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			wireMu.Lock()
			ingestedPackets = append(ingestedPackets, pkt)
			epoch := len(ingestedPackets)
			wireMu.Unlock()

			// Deduplicate identical blind sentence frames (simulating cloud blind dry-sentence distillation)
			var keptIdx []int
			seenSentHash := make(map[uint64]int)
			for sIdx, sf := range pkt.SentenceFrames {
				var sig uint64
				for _, a := range sf {
					sig = (sig * 1099511628211) ^ a.H
				}
				seenSentHash[sig]++
				if seenSentHash[sig] == 1 {
					keptIdx = append(keptIdx, sIdx)
				}
			}

			out := thinclient.CloudIngestResponse{
				UnitID:              pkt.UnitID,
				Epoch:               epoch,
				WaveformHex:         "2C22DE1AAE1E",
				WaveformBar:         "▂▇▂▂▇█▁▆▆█▁█",
				ConservedMass:       3.28,
				QualityMult:         0.98,
				AddressURI:          fmt.Sprintf("C6:L3:B001/C6:L2:B001/C6:L1:B001#%02d", epoch-1),
				AddressCoord64:      0x1000100010001000 | uint64(epoch-1),
				ContainerID:         0x1100000000000001,
				ParentID:            0x1200000000000001,
				ShiftScore:          0.42,
				KeptSentenceIndices: keptIdx,
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(out)

		case "/open/memory/v1/query", "/brain/v1/query":
			var req struct {
				Query            thinclient.BlindPulsePacket `json:"query"`
				TopK             int                         `json:"top_k"`
				CrossIncarnation *bool                       `json:"cross_incarnation"`
			}
			if err := json.Unmarshal(bodyBytes, &req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if req.CrossIncarnation == nil || *req.CrossIncarnation {
				http.Error(w, "ordinary Harness recall must explicitly lock one incarnation", http.StatusBadRequest)
				return
			}
			if err := normalizePulseForTest(&req.Query); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			out := []thinclient.CloudQueryHit{
				{
					UnitID:         "DOC1",
					AddressURI:     "C6:L3:B001/C6:L2:B001/C6:L1:B001#00",
					AddressCoord64: 0x1000100010001000,
					WaveformBar:    "▂▇▂▂▇█▁▆▆█▁█",
					WaveScore:      22.633,
					CellWormhole:   9.506,
					ManifoldGain:   2.234,
					FinalWeight:    34.374,
				},
				{
					UnitID:         "DOC3",
					AddressURI:     "C6:L3:B001/C6:L2:B001/C6:L1:B001#02",
					AddressCoord64: 0x1000100010001002,
					WaveformBar:    "▁▆▄▅▅▇▁▅▅█▅▁▅",
					WaveScore:      0.000,
					CellWormhole:   9.506,
					ManifoldGain:   1.647,
					FinalWeight:    11.153,
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(out)

		case "/open/memory/v1/graph", "/brain/v1/graph":
			var req thinclient.BlindGraphRequest
			_ = json.Unmarshal(bodyBytes, &req)
			out := thinclient.CloudGraphResponse{
				Success:      true,
				Version:      "v1",
				Layers:       req.Layers,
				LayerWeights: req.LayerWeights,
				Nodes: []thinclient.CloudGraphNode{
					{
						ID:          "cell:C6:L1:B001",
						RawID:       "C6:L1:B001",
						NodeType:    "cell",
						Layer:       "episodic",
						AddressURI:  "C6:L3:B001/C6:L2:B001/C6:L1:B001",
						Weight:      2.5,
						MemberCount: 3,
					},
					{
						ID:          "unit:DOC1",
						RawID:       "DOC1",
						NodeType:    "unit",
						Layer:       "background",
						ParentID:    "cell:C6:L1:B001",
						AddressURI:  "C6:L3:B001/C6:L2:B001/C6:L1:B001#00",
						WaveformBar: "▂▇▂▂▇█▁▆▆█▁█",
						Weight:      1.5,
					},
					{
						ID:          "unit:DOC3",
						RawID:       "DOC3",
						NodeType:    "unit",
						Layer:       "episodic",
						ParentID:    "cell:C6:L1:B001",
						AddressURI:  "C6:L3:B001/C6:L2:B001/C6:L1:B001#02",
						WaveformBar: "▁▆▄▅▅▇▁▅▅█▅▁▅",
						Weight:      1.2,
					},
				},
				Edges: []thinclient.CloudGraphEdge{
					{Source: "unit:DOC1", Target: "cell:C6:L1:B001", Kind: "member_of", Layer: "background", Weight: 1.0},
					{Source: "unit:DOC3", Target: "cell:C6:L1:B001", Kind: "member_of", Layer: "episodic", Weight: 1.0},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(out)

		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dbPath := filepath.Join(t.TempDir(), "thin_vault.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	vault, err := thinclient.NewVault(db, []byte("user-local-master-key-2026"))
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}
	transport := thinclient.NewHTTPBrainTransport(srv.URL, "test-token")
	client := thinclient.NewClient(vault, transport)
	ctx := context.Background()

	docs := []struct {
		id   string
		text string
	}{
		{"DOC1", "Your registration for the BMVC2024 conference in Glasgow starting November24 is confirmed."},
		{"DOC2", "The flight to Glasgow airport arrives on November24 at terminal two."},
		{
			"DOC3",
			"Date: 2024-11-16.\n" +
				"The Hilton Glasgow hotel stay checking in November24 costs 655.59 GBP in total.\n" +
				"A credit card deposit is needed upon check-in.\n" +
				"A credit card deposit is needed upon check-in.\n" +
				"A credit card deposit is needed upon check-in.",
		},
	}

	for _, d := range docs {
		resp, err := client.Remember(ctx, d.id, d.text, 1731794400)
		if err != nil {
			t.Fatalf("Remember(%s): %v", d.id, err)
		}
		t.Logf("  Remember[%s] -> Cloud Addr=%s (Coord64=0x%016x) | KeptSents=%v",
			d.id, resp.AddressURI, resp.AddressCoord64, resp.KeptSentenceIndices)
	}

	secrets := []string{"bmvc2024", "glasgow", "hilton", "655.59", "credit", "deposit"}
	for _, wireJSON := range capturedWirePayloads {
		low := strings.ToLower(wireJSON)
		for _, sec := range secrets {
			if strings.Contains(low, sec) {
				t.Fatalf("PLAINTEXT LEAK ON HTTP WIRE: found %q in %s", sec, wireJSON)
			}
		}
	}
	t.Logf("  ✓ HTTP Wire Audit Passed: %d HTTP payloads inspected, 0.00%% plaintext leakage", len(capturedWirePayloads))

	hits, err := client.Recall(ctx, "BMVC2024 conference registration", 5)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}

	foundDoc3 := false
	for rank, h := range hits {
		t.Logf("  Rank #%d: [%s] Weight=%.3f | Addr=%s | DistilledDryText=%q",
			rank+1, h.UnitID, h.FinalWeight, h.AddressURI, h.DistilledPlaintext)
		if h.UnitID == "DOC3" && strings.Contains(h.DistilledPlaintext, "655.59 GBP") {
			foundDoc3 = true
			// Verify the 3x repeated "A credit card deposit is needed upon check-in" was deduplicated to 1!
			if strings.Count(h.DistilledPlaintext, "credit card deposit") != 1 {
				t.Fatalf("expected repeated loop line to be deduplicated once, got:\n%s", h.DistilledPlaintext)
			}
		}
	}
	if len(hits) < 2 || hits[0].UnitID != "DOC1" || !foundDoc3 {
		t.Fatalf("expected DOC1 at #1 and DOC3 ('655.59 GBP') recalled, got %+v", hits)
	}

	// Verify Downstream Self-Maintained L3 Noetic Table (`thin_vault_noetic`):
	// Agent Harness stores (Query + CitedUnitIDs/Sentences + Answer) locally in `thin_vault_noetic` without sending to cloud!
	wireCountBeforeNoetic := len(capturedWirePayloads)
	err = client.RememberNoetic(
		"NOETIC-1",
		"How much does the Glasgow conference hotel cost?",
		[]string{"DOC1", "DOC3"},
		nil, // auto-hydrated from local DOC1 & DOC3 distilled sentences
		"The Hilton Glasgow hotel for BMVC2024 costs 655.59 GBP in total.",
		1731795000,
	)
	if err != nil {
		t.Fatalf("RememberNoetic failed: %v", err)
	}
	if len(capturedWirePayloads) != wireCountBeforeNoetic {
		t.Fatalf("expected 0 cloud HTTP requests when saving downstream Noetic record, got %d new requests",
			len(capturedWirePayloads)-wireCountBeforeNoetic)
	}

	noeticHits, err := client.RecallNoetic("Glasgow hotel cost GBP", 5)
	if err != nil {
		t.Fatalf("RecallNoetic failed: %v", err)
	}
	if len(noeticHits) != 1 || noeticHits[0].NoeticID != "NOETIC-1" || len(noeticHits[0].CitedSentences) < 2 {
		t.Fatalf("unexpected local Noetic hits: %+v", noeticHits)
	}
	t.Logf("  ✓ Downstream Local Noetic Table Hit: [%s] Score=%.1f Answer=%q CitedSents=%d",
		noeticHits[0].NoeticID, noeticHits[0].MatchScore, noeticHits[0].AnswerText, len(noeticHits[0].CitedSentences))

	// Verify Client-Resolved Unified Three-Layer Graph (`/open/memory/v1/graph` + Local Vault + Local Noetic):
	resolvedGraph, err := client.ResolveGraph(ctx, thinclient.BlindGraphRequest{
		Layers:       []string{"background", "episodic", "noetic"},
		LayerWeights: map[string]float64{"background": 1.1, "episodic": 1.0, "noetic": 1.4},
	})
	if err != nil {
		t.Fatalf("ResolveGraph failed: %v", err)
	}
	if len(resolvedGraph.Nodes) < 4 || len(resolvedGraph.Edges) < 4 {
		t.Fatalf("expected >=4 nodes (cell + DOC1 + DOC3 + NOETIC-1) and >=4 edges, got nodes=%d edges=%d",
			len(resolvedGraph.Nodes), len(resolvedGraph.Edges))
	}

	// Verify cascade invalidation when underlying DOC3 is invalidated
	affected, err := vault.InvalidateLocalNoeticByCitedDoc("DOC3")
	if err != nil || len(affected) != 1 || affected[0] != "NOETIC-1" {
		t.Fatalf("expected NOETIC-1 to be invalidated when DOC3 changes, got affected=%v err=%v", affected, err)
	}
	afterHits, err := client.RecallNoetic("Glasgow hotel cost GBP", 5)
	if err != nil || len(afterHits) != 0 {
		t.Fatalf("expected 0 active Noetic hits after invalidation, got %+v (err=%v)", afterHits, err)
	}
}

// TestLiveUpstreamV1Integration_UserID10 connects the local Ultra-Thin Client (`internal/thinclient`)
// directly to the live Autonoetic Engine `/open/memory/v1/{health,ingest,query,distill}`
// service using the canonical dev/demo `user_id = "10"`, verifying real
// end-to-end persistence, the memory engine synthesis, L3 Noetic reconsolidation, and recall.
func TestLiveUpstreamV1Integration_UserID10(t *testing.T) {
	// 端侧唯一凭据 = 数据面签名钥（`mem2.*`）：仅在显式提供 ROLLING_MEMORY_BRAIN_URL 时才执行远端集成测试。
	baseURL := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_BRAIN_URL"))
	if baseURL == "" {
		t.Skip("ROLLING_MEMORY_BRAIN_URL not set; skipping live upstream integration test in local mode")
	}
	apiKey := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_BRAIN_KEY"))
	if apiKey == "" {
		_, accountKey, accountErr := cloudcred.Ensure(context.Background())
		if accountErr != nil {
			t.Skipf("live data-plane credentials not configured (%v); skipping live integration check", accountErr)
		}
		apiKey = accountKey
	}
	healthURL := strings.TrimRight(baseURL, "/")
	if !strings.HasSuffix(healthURL, "/open/memory/v1") {
		healthURL += "/open/memory/v1"
	}
	resp, err := http.Get(healthURL + "/health")
	if err != nil {
		t.Skipf("live upstream %s not reachable (%v); skipping live integration check", healthURL, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Skipf("live upstream %s returned status %d; skipping live integration check", healthURL, resp.StatusCode)
	}

	dbPath := filepath.Join(t.TempDir(), "live_u10_vault.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	vault, err := thinclient.NewVault(db, []byte("rolling-memory-u10-live-vault-secret"))
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}
	transport := thinclient.NewHTTPBrainTransport(baseURL, apiKey)
	transport.UserID = thinclient.DefaultUpstreamDevUserID // "10"
	client := thinclient.NewClient(vault, transport)
	ctx := context.Background()

	// 1. Ingest two real episodic memories into upstream for user_id=10
	r1, err := client.Remember(ctx, "U10-LIVE-DOC-1", "Rolling-Memory 现已全面切换为薄客户端架构，不再维护本地核心算法，100% 走云端 /open/memory/v1 接口，绑定 user_id=10。", 1730800100)
	if err != nil {
		t.Fatalf("live upstream Remember DOC-1 (user_id=10) failed: %v", err)
	}
	r2, err := client.Remember(ctx, "U10-LIVE-DOC-2", "LoCoMo Tier-100 全池 5882 轮评测在 the memory engine 引擎上取得 84.00% (84/100) 端到端判官总分。", 1730800200)
	if err != nil {
		t.Fatalf("live upstream Remember DOC-2 (user_id=10) failed: %v", err)
	}
	if r1.AddressURI == "" {
		t.Fatalf("expected non-empty upstream the memory engine address for user_id=10, got %+v", r1)
	}
	t.Logf("[Live Upstream Ingest · user_id=10] DOC-1 => Addr=%s Mass=%.3f", r1.AddressURI, r1.ConservedMass)
	t.Logf("[Live Upstream Ingest · user_id=10] DOC-2 => Addr=%s Mass=%.3f", r2.AddressURI, r2.ConservedMass)

	// 2. Query manifold from upstream for user_id=10 and resolve plaintext locally
	hits, err := client.Recall(ctx, "薄客户端架构 云端接口 user_id=10", 5)
	if err != nil {
		t.Fatalf("live upstream Recall (user_id=10) failed: %v", err)
	}
	if len(hits) == 0 {
		t.Fatalf("expected non-empty recall hits from upstream for user_id=10")
	}
	if hits[0].UnitID != "U10-LIVE-DOC-1" || !strings.Contains(hits[0].RawPlaintext, "薄客户端架构") {
		t.Fatalf("unexpected top-1 recall hit for user_id=10: %+v", hits[0])
	}
	t.Logf("[Live Upstream Recall · user_id=10] Top-1 => ID=%s Weight=%.3f Addr=%s Text=%q",
		hits[0].UnitID, hits[0].FinalWeight, hits[0].AddressURI, hits[0].DistilledPlaintext)

	// 3. Verify Bring-Your-Own-Storage (JSONFileStore & DirTextStore) against the exact same live upstream service!
	jsonStore, err := thinclient.NewJSONFileStore(filepath.Join(t.TempDir(), "custom_memories.json"), []byte("rolling-memory-u10-live-vault-secret"))
	if err != nil {
		t.Fatalf("NewJSONFileStore: %v", err)
	}
	jsonClient := thinclient.NewClientWithStore(jsonStore, transport)
	if _, err := jsonClient.Remember(ctx, "U10-JSON-DOC-1", "用户可以使用任意 JSON 文件或纯文本目录存储自己的记忆明文，完全不受限于 SQLite。", 1730800300); err != nil {
		t.Fatalf("jsonClient.Remember: %v", err)
	}
	jsonHits, err := jsonClient.Recall(ctx, "JSON 文件 纯文本目录 存储记忆明文", 3)
	if err != nil || len(jsonHits) == 0 || jsonHits[0].UnitID != "U10-JSON-DOC-1" {
		t.Fatalf("expected U10-JSON-DOC-1 at #1 from JSONFileStore, got %+v (err=%v)", jsonHits, err)
	}

	txtStore, err := thinclient.NewDirTextStore(filepath.Join(t.TempDir(), "txt_vault"), []byte("rolling-memory-u10-live-vault-secret"))
	if err != nil {
		t.Fatalf("NewDirTextStore: %v", err)
	}
	txtClient := thinclient.NewClientWithStore(txtStore, transport)
	if _, err := txtClient.Remember(ctx, "U10-TXT-DOC-1", "纯文本 .txt 目录存储适配器同样支持零明文盲化脉冲与分形干句还原。", 1730800400); err != nil {
		t.Fatalf("txtClient.Remember: %v", err)
	}
	txtHits, err := txtClient.Recall(ctx, "纯文本 txt 目录存储适配器", 3)
	if err != nil || len(txtHits) == 0 || txtHits[0].UnitID != "U10-TXT-DOC-1" {
		t.Fatalf("expected U10-TXT-DOC-1 at #1 from DirTextStore, got %+v (err=%v)", txtHits, err)
	}
	t.Logf("✓ Bring-Your-Own-Storage (SQLite + JSONFileStore + DirTextStore) verified live against /open/memory/v1 (user_id=10)!")
}

// TestClientSDKDefaultRecall_StandaloneAndOfflineFallback verifies that the Client SDK Default Recall
// (`SearchClientSDKDefault` / `RecallDefault` using SQLite FTS5 trigram + integer IDF coverage + same-day continuity)
// works out of the box as a system default capability on the client without requiring an upstream connection.
// TestClientSDKDefaultRecallCacheInvalidatesChangedDocument prevents the
// shared blind-slot cache from serving stale atoms after a docID is rewritten.
func TestClientSDKDefaultRecallCacheInvalidatesChangedDocument(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "cache-invalidation.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	vault, err := thinclient.NewVault(db, []byte("cache-invalidation-secret"))
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}
	save := func(text string) {
		_, sents := vault.PackBlindPacket("DOC-CHANGING", text, 100)
		if err := vault.SaveLocalPlaintext("DOC-CHANGING", 100, text, sents, thinclient.CloudIngestResponse{
			UnitID: "DOC-CHANGING", Layer: thinclient.LayerEpisodic,
		}); err != nil {
			t.Fatalf("SaveLocalPlaintext: %v", err)
		}
	}
	client := thinclient.NewClient(vault, nil)
	save("alpha original marker")
	if hits, err := client.RecallDefault("alpha original marker", 3); err != nil || len(hits) != 1 {
		t.Fatalf("initial recall = %+v, err %v", hits, err)
	}
	save("replacement zeta")
	if hits, err := client.RecallDefault("alpha original marker", 3); err != nil || len(hits) != 0 {
		t.Fatalf("stale atoms survived document replacement: %+v, err %v", hits, err)
	}
	if hits, err := client.RecallDefault("replacement zeta", 3); err != nil || len(hits) != 1 || hits[0].UnitID != "DOC-CHANGING" {
		t.Fatalf("updated document not recalled: %+v, err %v", hits, err)
	}
}

func TestClientSDKDefaultRecall_StandaloneAndOfflineFallback(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sdk_default.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	vault, err := thinclient.NewVault(db, []byte("sdk-default-secret-2026"))
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}
	localClient := thinclient.NewClient(vault, nil) // nil transport => 100% Client SDK Default Recall
	ctx := context.Background()

	seedDocs := []struct {
		id   string
		occ  int64
		text string
	}{
		{"BMVC-REG", 1732440000, "Your registration for the BMVC2024 conference in Glasgow starting November24 is confirmed."},
		{"BMVC-HOTEL", 1732443600, "The Hilton Glasgow hotel stay checking in November24 costs 655.59 GBP in total."},
		{"NOISE-1", 1730000000, "Routine grocery shopping receipt for milk and bread in London."},
	}
	for _, d := range seedDocs {
		_, sents := vault.PackBlindPacket(d.id, d.text, d.occ)
		if err := vault.SaveLocalPlaintext(d.id, d.occ, d.text, sents, thinclient.CloudIngestResponse{
			UnitID: d.id,
			Layer:  "episodic",
		}); err != nil {
			t.Fatalf("SaveLocalPlaintext(%s): %v", d.id, err)
		}
	}

	hits, err := localClient.Recall(ctx, "BMVC2024 Glasgow November24", 3)
	if err != nil {
		t.Fatalf("localClient.Recall: %v", err)
	}
	if len(hits) < 2 || hits[0].UnitID != "BMVC-REG" || hits[1].UnitID != "BMVC-HOTEL" {
		t.Fatalf("expected BMVC-REG (#1) and BMVC-HOTEL (#2) via Client SDK Default Recall, got %+v", hits)
	}

	// Verify Layer 3 (`noetic`: Agent retrieval handles + answer stored locally) handle-linked recall:
	if err := localClient.RememberNoetic(
		"NOETIC-ACCOM",
		"What was the accommodation cost for the Scottish vision symposium?",
		[]string{"BMVC-HOTEL"},
		nil,
		"The accommodation at Hilton cost 655.59 GBP.",
		1732450000,
	); err != nil {
		t.Fatalf("RememberNoetic: %v", err)
	}
	nHits, err := localClient.Recall(ctx, "Scottish vision symposium accommodation", 5)
	if err != nil {
		t.Fatalf("localClient.Recall (Noetic handle link): %v", err)
	}
	foundNoetic, foundLinkedHotel := false, false
	for _, nh := range nHits {
		if nh.UnitID == "NOETIC-ACCOM" && nh.Layer == thinclient.LayerNoetic {
			foundNoetic = true
		}
		if nh.UnitID == "BMVC-HOTEL" && strings.Contains(nh.RawPlaintext, "655.59 GBP") {
			foundLinkedHotel = true
		}
	}
	if !foundNoetic || !foundLinkedHotel {
		t.Fatalf("expected both NOETIC-ACCOM (noetic) and handle-linked BMVC-HOTEL (episodic), got %+v", nHits)
	}
	t.Logf("✓ 3-Only Local Memory Model (background × episodic × noetic handle-linked recall) verified: #1=%s (%.1f), #2=%s (%.1f)",
		hits[0].UnitID, hits[0].FinalWeight, hits[1].UnitID, hits[1].FinalWeight)
}

func TestHTTPBrainTransportDeleteUnitsBatchingAndErrors(t *testing.T) {
	type purgeRequest struct {
		UserID  string   `json:"user_id"`
		Scope   string   `json:"scope"`
		Mode    string   `json:"mode"`
		UnitIDs []string `json:"unit_ids"`
	}
	var batches []int
	var receivedIDs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/open/memory/v1/purge" {
			t.Errorf("unexpected purge path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var req purgeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode purge request: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.UserID != "10" || req.Scope != "purge-scope" || req.Mode != "units" {
			t.Errorf("unexpected purge request: %+v", req)
		}
		if got := r.Header.Get("X-Rolling-Memory-User"); got != "10" {
			t.Errorf("unexpected purge user header: %q", got)
		}
		if got := r.Header.Get("X-Rolling-Memory-Scope"); got != "purge-scope" {
			t.Errorf("unexpected purge scope header: %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer purge-token" {
			t.Errorf("unexpected purge authorization: %q", got)
		}
		batches = append(batches, len(req.UnitIDs))
		receivedIDs = append(receivedIDs, req.UnitIDs...)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true}`))
	}))

	unitIDs := make([]string, 205)
	for i := range unitIDs {
		unitIDs[i] = fmt.Sprintf("unit-%03d", i)
	}
	transport := thinclient.NewHTTPBrainTransport(srv.URL, "purge-token")
	transport.Scope = "purge-scope"
	if err := transport.DeleteUnits(context.Background(), unitIDs); err != nil {
		srv.Close()
		t.Fatalf("DeleteUnits: %v", err)
	}
	srv.Close()
	if len(batches) != 3 || batches[0] != 100 || batches[1] != 100 || batches[2] != 5 {
		t.Fatalf("unexpected purge batches: %v", batches)
	}
	if len(receivedIDs) != len(unitIDs) {
		t.Fatalf("purge lost unit IDs: got=%d want=%d", len(receivedIDs), len(unitIDs))
	}
	for i := range unitIDs {
		if receivedIDs[i] != unitIDs[i] {
			t.Fatalf("purge order mismatch at %d: got=%q want=%q", i, receivedIDs[i], unitIDs[i])
		}
	}

	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "success false", status: http.StatusOK, body: `{"success":false}`},
		{name: "non 2xx", status: http.StatusServiceUnavailable, body: `{"success":false}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			errorSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer errorSrv.Close()
			errorTransport := thinclient.NewHTTPBrainTransport(errorSrv.URL, "purge-token")
			if err := errorTransport.DeleteUnits(context.Background(), []string{"unit-1"}); err == nil {
				t.Fatalf("expected purge error for status=%d body=%s", tc.status, tc.body)
			}
		})
	}
}

// normalizePulseForTest mirrors the server-side pulse expansion so the mock blind brain accepts
// both plaintext slots/sents and the compact atoms_b64 payload (single on-wire encoding).
func normalizePulseForTest(pkt *thinclient.BlindPulsePacket) error {
	if pkt.AtomsB64 == "" {
		return nil
	}
	atoms, offsets, err := thinclient.DecodePulsePayloadB64(pkt.AtomsB64)
	if err != nil {
		return err
	}
	frames := make([][]thinclient.BlindTokenAtom, 0, len(offsets))
	prev := 0
	for _, end := range offsets {
		frames = append(frames, atoms[prev:end])
		prev = end
	}
	pkt.Slots = atoms
	if len(frames) > 0 {
		pkt.SentenceFrames = frames
	}
	pkt.AtomsB64 = ""
	return nil
}

// TestVaultSecretSurvivesMem2KeyRotation verifies that when NewVault is initialized with a
// short-lived `mem2.<payload>.<sig>` signed key, renewing/rotating the signed key (`iat`/`exp`/`sig`
// change for the same tenant) keeps the blind token hashes `{h, b, f}` 100% invariant so previously
// ingested memories remain recallable with 0 re-ingestion (no `rigid_vacuum` false empty).
func TestVaultSecretSurvivesMem2KeyRotation(t *testing.T) {
	keyA := "mem2.eyJ0aWQiOiJ1c2VyIiwidWlkIjoiMTAiLCJpYXQiOjE3MDAwMDAwMDAsImV4cCI6MTcwMDA4NjQwMH0.sig_rotation_alpha_1111"
	keyB := "mem2.eyJ0aWQiOiJ1c2VyIiwidWlkIjoiMTAiLCJpYXQiOjE3MDAwODAwMDAsImV4cCI6MTcwMDE2NjQwMH0.sig_rotation_beta_2222"
	keyOther := "mem2.eyJ0aWQiOiJvdGhlciIsInVpZCI6Ijk5IiwiaWF0IjoxNzAwMDAwMDAwLCJleHAiOjE3MDAwODY0MDB9.sig_other_3333"

	ingestedByUnit := map[string]map[uint64]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open/memory/v1/ingest":
			var pkt thinclient.BlindPulsePacket
			if err := json.NewDecoder(r.Body).Decode(&pkt); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if err := normalizePulseForTest(&pkt); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			set := map[uint64]bool{}
			for _, s := range pkt.Slots {
				set[s.H] = true
			}
			ingestedByUnit[pkt.UnitID] = set
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(thinclient.CloudIngestResponse{
				UnitID: pkt.UnitID,
				Layer:  pkt.Layer,
			})
		case "/open/memory/v1/query":
			var req struct {
				Query thinclient.BlindPulsePacket `json:"query"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if err := normalizePulseForTest(&req.Query); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			var out []thinclient.CloudQueryHit
			for unitID, set := range ingestedByUnit {
				matched := 0
				for _, s := range req.Query.Slots {
					if set[s.H] {
						matched++
					}
				}
				if matched > 0 {
					out = append(out, thinclient.CloudQueryHit{
						UnitID:      unitID,
						Layer:       thinclient.LayerEpisodic,
						AddressURI:  "C6:L3:B001/C6:L2:B001/C6:L1:B001#00",
						WaveScore:   float64(matched),
						FinalWeight: float64(matched),
					})
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(out)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dbPath := filepath.Join(t.TempDir(), "mem2_rotation.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	// Phase 1: Ingest under keyA (initial 24h signed key)
	vaultA, err := thinclient.NewVault(db, []byte(keyA))
	if err != nil {
		t.Fatalf("NewVault(keyA): %v", err)
	}
	clientA := thinclient.NewClient(vaultA, thinclient.NewHTTPBrainTransport(srv.URL, keyA))
	ctx := context.Background()
	if _, err := clientA.Remember(ctx, "ROT-DOC-1", "The Edinburgh robotics workshop registration code is RAME-9042.", 1732500000); err != nil {
		t.Fatalf("clientA.Remember: %v", err)
	}

	// Phase 2: Renew/rotate to keyB (new iat/exp/sig, same tenant user:10) — 0 re-ingestion!
	vaultB, err := thinclient.NewVault(db, []byte(keyB))
	if err != nil {
		t.Fatalf("NewVault(keyB): %v", err)
	}
	clientB := thinclient.NewClient(vaultB, thinclient.NewHTTPBrainTransport(srv.URL, keyB))
	hits, err := clientB.Recall(ctx, "Edinburgh robotics workshop RAME-9042", 3)
	if err != nil {
		t.Fatalf("clientB.Recall after key rotation: %v", err)
	}
	if len(hits) == 0 || hits[0].UnitID != "ROT-DOC-1" || hits[0].AddressURI != "C6:L3:B001/C6:L2:B001/C6:L1:B001#00" || !strings.Contains(hits[0].RawPlaintext, "RAME-9042") {
		t.Fatalf("expected 100%% cloud manifold recall hit for ROT-DOC-1 after mem2 key rotation with 0 re-ingest, got %+v", hits)
	}

	// Phase 3: Rotated vaultB must match vaultA bit-for-bit, while different tenant keyOther must NOT collide
	vaultOther, err := thinclient.NewVault(db, []byte(keyOther))
	if err != nil {
		t.Fatalf("NewVault(keyOther): %v", err)
	}
	pktA, _ := vaultA.PackBlindPacket("probe", "Edinburgh", 1732500000)
	pktB, _ := vaultB.PackBlindPacket("probe", "Edinburgh", 1732500000)
	pktOther, _ := vaultOther.PackBlindPacket("probe", "Edinburgh", 1732500000)
	if len(pktA.Slots) == 0 || pktA.Slots[0] != pktB.Slots[0] {
		t.Fatalf("mem2 key rotation broke NewVault blind token atom: A=%+v B=%+v", pktA.Slots, pktB.Slots)
	}
	if len(pktB.Slots) == 0 || pktB.Slots[0].H == pktOther.Slots[0].H {
		t.Fatalf("different tenant must not share blind hash: B=%+v Other=%+v", pktB.Slots, pktOther.Slots)
	}
}

// TestQueryFTS5BM25RankedOrdersByBM25 pins the contract that the auxiliary FTS5 ranker used by
// reciprocal rank fusion returns a deterministic, relevance-ordered doc list (not the clamped
// score map), and that the fused recall puts a doc corroborated by both rankers ahead of a doc
// only one ranker can see.
func TestQueryFTS5BM25RankedOrdersByBM25(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "fts5-ranked.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	vault, err := thinclient.NewVault(db, []byte("fts5-ranked-secret"))
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}
	docs := []struct {
		id   string
		occ  int64
		text string
	}{
		{"DOC-STRONG", 1732500000, "hotel booking confirmation for the BMVC 2024 conference in Glasgow with total cost 655.59 GBP"},
		{"DOC-WEAK", 1732500100, "invoice for 655.59 GBP"},
		{"DOC-NOISE", 1732500200, "grocery receipt for milk and bread"},
	}
	for _, d := range docs {
		_, sents := vault.PackBlindPacket(d.id, d.text, d.occ)
		if err := vault.SaveLocalPlaintext(d.id, d.occ, d.text, sents, thinclient.CloudIngestResponse{
			UnitID: d.id, Layer: thinclient.LayerEpisodic,
		}); err != nil {
			t.Fatalf("SaveLocalPlaintext(%s): %v", d.id, err)
		}
	}

	ranked, err := vault.QueryFTS5BM25Ranked("BMVC 2024 Glasgow hotel cost", 10)
	if err != nil {
		t.Fatalf("QueryFTS5BM25Ranked: %v", err)
	}
	if len(ranked) == 0 || ranked[0] != "DOC-STRONG" {
		t.Fatalf("expected DOC-STRONG first from FTS5 BM25, got %v", ranked)
	}
	if again, err := vault.QueryFTS5BM25Ranked("BMVC 2024 Glasgow hotel cost", 10); err != nil || len(again) != len(ranked) || again[0] != ranked[0] {
		t.Fatalf("QueryFTS5BM25Ranked must be deterministic: first=%v second=%v err=%v", ranked, again, err)
	}

	hits, err := vault.SearchClientSDKDefault("BMVC 2024 Glasgow hotel cost", 3)
	if err != nil {
		t.Fatalf("SearchClientSDKDefault: %v", err)
	}
	if len(hits) == 0 || hits[0].UnitID != "DOC-STRONG" {
		t.Fatalf("expected DOC-STRONG first after RRF fusion, got %+v", hits)
	}
	for i := 1; i < len(hits); i++ {
		if hits[i-1].FinalWeight < hits[i].FinalWeight {
			t.Fatalf("fused hits must be ordered by FinalWeight DESC: %+v", hits)
		}
	}
}
