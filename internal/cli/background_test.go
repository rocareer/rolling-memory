// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli_test

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type backgroundCloudRequest struct {
	header http.Header
	body   map[string]any
	raw    string
}

type backgroundCloudStub struct {
	mu          sync.Mutex
	server      *httptest.Server
	ingests     []backgroundCloudRequest
	purges      []backgroundCloudRequest
	ingestFails bool
	purgeFails  bool
}

func newBackgroundCloudStub(t *testing.T) *backgroundCloudStub {
	t.Helper()
	stub := &backgroundCloudStub{}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body := map[string]any{}
		_ = json.Unmarshal(raw, &body)
		request := backgroundCloudRequest{header: r.Header.Clone(), body: body, raw: string(raw)}
		w.Header().Set("Content-Type", "application/json")

		stub.mu.Lock()
		defer stub.mu.Unlock()
		switch r.URL.Path {
		case "/open/memory/v1/ingest":
			stub.ingests = append(stub.ingests, request)
			if stub.ingestFails {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"ingest unavailable"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":                    body["id"],
				"layer":                 body["layer"],
				"address_uri":           "C6:L1:B001#00",
				"address_coord64":       42,
				"waveform_bar":          "▃▆█▅▂",
				"waveform_hex":          "36AF52",
				"kept_sentence_indices": []int{0},
			})
		case "/open/memory/v1/purge":
			stub.purges = append(stub.purges, request)
			if stub.purgeFails {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"purge unavailable"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true, "mode": "units", "deleted_units": 1, "remaining_units": 0,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *backgroundCloudStub) requests() ([]backgroundCloudRequest, []backgroundCloudRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]backgroundCloudRequest(nil), s.ingests...), append([]backgroundCloudRequest(nil), s.purges...)
}

func (s *backgroundCloudStub) setFailures(ingest, purge bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ingestFails = ingest
	s.purgeFails = purge
}

func configureBackgroundCloud(t *testing.T, stub *backgroundCloudStub) {
	t.Helper()
	t.Setenv("ROLLING_MEMORY_MODE", "cloud")
	t.Setenv("ROLLING_MEMORY_BRAIN_URL", stub.server.URL)
	t.Setenv("ROLLING_MEMORY_BRAIN_KEY", "test-memory-key")
	t.Setenv("ROLLING_MEMORY_OPEN_MEMORY_USER", "10")
}

func TestBackgroundCLILocalModeNeverContactsBrain(t *testing.T) {
	env := newEnv(t)
	stub := newBackgroundCloudStub(t)
	t.Setenv("ROLLING_MEMORY_BRAIN_URL", stub.server.URL)
	t.Setenv("ROLLING_MEMORY_BRAIN_KEY", "test-memory-key")
	root := writeFile(t, filepath.Join(env.base, "mounted", "note.md"), "local background sentinel")
	result := run(t, "background", "add", "--path", filepath.Dir(root), "--json")
	if result.code != 0 {
		t.Fatalf("background add exit %d: %s%s", result.code, result.stdout, result.stderr)
	}
	result = run(t, "recall", "--q", "local background sentinel", "--json")
	if result.code != 0 {
		t.Fatalf("recall exit %d: %s%s", result.code, result.stdout, result.stderr)
	}
	ingests, purges := stub.requests()
	if len(ingests) != 0 || len(purges) != 0 {
		t.Fatalf("local mode sent %d ingests and %d purges", len(ingests), len(purges))
	}
}

func TestBackgroundCLIReadsBlindlySyncsAndPurges(t *testing.T) {
	env := newEnv(t)
	stub := newBackgroundCloudStub(t)
	configureBackgroundCloud(t, stub)
	const plaintext = "confidential-background-body-must-stay-local"
	root := writeFile(t, filepath.Join(env.base, "mounted", "nested", "note.md"), "# Private runbook\n"+plaintext)
	dbPath := filepath.Join(env.base, "background.db")

	added := run(t, "background", "add", "--db", dbPath, "--path", filepath.Dir(root), "--json")
	if added.code != 0 {
		t.Fatalf("background add exit %d: %s%s", added.code, added.stdout, added.stderr)
	}
	addedPayload := jsonObject(t, added.stdout)
	syncPayload, _ := addedPayload["sync"].(map[string]any)
	if syncPayload["ingested_files"] != float64(1) || syncPayload["ingested_chunks"] != float64(1) {
		t.Fatalf("unexpected add sync report: %s", added.stdout)
	}
	ingests, _ := stub.requests()
	if len(ingests) != 1 {
		t.Fatalf("ingest requests=%d, want 1", len(ingests))
	}
	ingest := ingests[0]
	if strings.Contains(ingest.raw, plaintext) {
		t.Fatal("cloud ingest leaked source plaintext")
	}
	if ingest.body["layer"] != "background" || ingest.body["user_id"] != "10" {
		t.Fatalf("unexpected ingest identity/layer: %#v", ingest.body)
	}
	if ingest.header.Get("Authorization") != "Bearer test-memory-key" || ingest.header.Get("X-Rolling-Memory-User") != "10" {
		t.Fatalf("unexpected ingest auth headers: %#v", ingest.header)
	}
	if slots, _ := ingest.body["slots"].([]any); len(slots) == 0 {
		if atoms, _ := ingest.body["atoms_b64"].(string); atoms == "" {
			t.Fatalf("ingest has no blind payload (slots/atoms_b64): %#v", ingest.body)
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open background db: %v", err)
	}
	defer db.Close()
	var addressedChunks int
	if err := db.QueryRow(`SELECT COUNT(*) FROM thin_vault_bg_chunks WHERE address_uri != '' AND waveform_bar != '' AND slots_json != '[]'`).Scan(&addressedChunks); err != nil {
		t.Fatalf("count addressed chunks: %v", err)
	}
	if addressedChunks != 1 {
		t.Fatalf("addressed chunks=%d, want 1", addressedChunks)
	}

	synced := run(t, "background", "sync", "--db", dbPath, "--json")
	if synced.code != 0 || jsonObject(t, synced.stdout)["unchanged_files"] != float64(1) {
		t.Fatalf("second sync = %s%s", synced.stdout, synced.stderr)
	}
	ingests, _ = stub.requests()
	if len(ingests) != 1 {
		t.Fatalf("unchanged file triggered another ingest: %d", len(ingests))
	}

	removed := run(t, "background", "remove", "--db", dbPath, "--path", filepath.Dir(root), "--json")
	if removed.code != 0 {
		t.Fatalf("background remove exit %d: %s%s", removed.code, removed.stdout, removed.stderr)
	}
	_, purges := stub.requests()
	if len(purges) != 1 || purges[0].body["mode"] != "units" {
		t.Fatalf("unexpected purge requests: %#v", purges)
	}
	if ids, _ := purges[0].body["unit_ids"].([]any); len(ids) != 1 || ids[0] != ingest.body["id"] {
		t.Fatalf("purge ids=%#v, ingest id=%#v", ids, ingest.body["id"])
	}
}

func TestBackgroundCLICloudFailuresRemainRetryable(t *testing.T) {
	env := newEnv(t)
	stub := newBackgroundCloudStub(t)
	configureBackgroundCloud(t, stub)
	root := writeFile(t, filepath.Join(env.base, "retry", "note.md"), "retryable background document")
	dbPath := filepath.Join(env.base, "background.db")
	stub.setFailures(true, false)

	added := run(t, "background", "add", "--db", dbPath, "--path", filepath.Dir(root), "--json")
	if added.code != 1 || !strings.Contains(added.stdout, "ingest unavailable") {
		t.Fatalf("failed add = code %d stdout %s stderr %s", added.code, added.stdout, added.stderr)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open background db: %v", err)
	}
	defer db.Close()
	var mountCount, fileCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM thin_vault_bg_dirs`).Scan(&mountCount); err != nil {
		t.Fatalf("count mounts: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM thin_vault_bg_files`).Scan(&fileCount); err != nil {
		t.Fatalf("count synced files: %v", err)
	}
	if mountCount != 1 || fileCount != 0 {
		t.Fatalf("failed ingest committed synced state: mounts=%d files=%d", mountCount, fileCount)
	}

	stub.setFailures(false, false)
	synced := run(t, "background", "sync", "--db", dbPath, "--json")
	if synced.code != 0 || jsonObject(t, synced.stdout)["ingested_files"] != float64(1) {
		t.Fatalf("retry sync = %s%s", synced.stdout, synced.stderr)
	}

	stub.setFailures(false, true)
	removed := run(t, "background", "remove", "--db", dbPath, "--path", filepath.Dir(root), "--json")
	if removed.code != 1 {
		t.Fatalf("failed remove = %s%s", removed.stdout, removed.stderr)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM thin_vault_bg_dirs`).Scan(&mountCount); err != nil {
		t.Fatalf("count mounts after failed purge: %v", err)
	}
	if mountCount != 1 {
		t.Fatalf("failed purge removed mount metadata")
	}

	stub.setFailures(false, false)
	removed = run(t, "background", "remove", "--db", dbPath, "--path", filepath.Dir(root), "--json")
	if removed.code != 0 {
		t.Fatalf("retry remove = %s%s", removed.stdout, removed.stderr)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM thin_vault_bg_dirs`).Scan(&mountCount); err != nil {
		t.Fatalf("count mounts after retry purge: %v", err)
	}
	if mountCount != 0 {
		t.Fatalf("mount remains after successful remove")
	}
}
