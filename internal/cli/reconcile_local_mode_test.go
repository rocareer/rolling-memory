// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli_test

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestLocalModeSyncMakesZeroCloudRequests is the local-mode no-egress lock: even with a
// cloud base URL and a stored key present in config, `sync status` / `sync reconcile`
// must stay on the local plaintext vault — remote counts are 0, nothing is pushed, and
// the configured endpoint is never dialed.
func TestLocalModeSyncMakesZeroCloudRequests(t *testing.T) {
	env := newEnv(t)
	dbPath := filepath.Join(env.base, "local-mode.db")

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	keyPath := filepath.Join(env.base, "cloud.key")
	if err := os.WriteFile(keyPath, []byte("test-local-key"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	cfg := "[mode]\nname = \"local\"\n\n[cloud.sync]\nbase_url = \"" + srv.URL +
		"/open/memory/v1\"\napi_key_file = \"" + keyPath + "\"\n"
	if err := os.WriteFile(filepath.Join(env.base, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Initialize and bind the empty temporary vault before seeding its fixture.
	init := run(t, "sync", "status", "--db", dbPath, "--json")
	if init.code != 0 {
		t.Fatalf("initialize vault exit=%d stdout=%s stderr=%s", init.code, init.stdout, init.stderr)
	}
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS thin_vault_docs (
			doc_id TEXT PRIMARY KEY, layer TEXT NOT NULL DEFAULT 'episodic', occurred_at INTEGER NOT NULL DEFAULT 0,
			raw_text TEXT NOT NULL, sentences_json TEXT NOT NULL DEFAULT '[]', address_uri TEXT NOT NULL DEFAULT '',
			coord64 INTEGER NOT NULL DEFAULT 0, waveform_bar TEXT NOT NULL DEFAULT '', doc_macro_wave TEXT NOT NULL DEFAULT '',
			kept_indices_json TEXT NOT NULL DEFAULT '[]'
		);
		INSERT OR REPLACE INTO thin_vault_docs
			(doc_id, layer, occurred_at, raw_text, sentences_json, address_uri, coord64, waveform_bar, doc_macro_wave, kept_indices_json)
		VALUES ('local_only_1', 'episodic', ?, '本地模式零外呼探针', '[]', 'C6:L1:B001#00', 1, '', '', '[]');
	`, time.Now().Unix()); err != nil {
		t.Fatalf("seed local doc: %v", err)
	}

	res := run(t, "sync", "status", "--db", dbPath, "--json")
	if res.code != 0 {
		t.Fatalf("sync status exit=%d stdout=%s stderr=%s", res.code, res.stdout, res.stderr)
	}
	var status map[string]any
	if err := json.Unmarshal([]byte(res.stdout), &status); err != nil {
		t.Fatalf("decode status: %v (%s)", err, res.stdout)
	}
	if status["mode"] != "local" {
		t.Fatalf("status mode = %v, want local", status["mode"])
	}
	if status["cloud_online"] != false {
		t.Fatalf("local mode reported cloud_online=%v", status["cloud_online"])
	}
	layers, _ := status["layers"].(map[string]any)
	epi, _ := layers["episodic"].(map[string]any)
	if epi == nil {
		t.Fatalf("missing episodic layer stats: %+v", status["layers"])
	}
	if epi["remote_cloud_count"].(float64) != 0 {
		t.Fatalf("local mode remote_cloud_count = %v, want 0", epi["remote_cloud_count"])
	}
	if epi["local_vault_count"].(float64) != 1 {
		t.Fatalf("local_vault_count = %v, want 1", epi["local_vault_count"])
	}
	if epi["aligned"] != true {
		t.Fatalf("local mode with no pending imports must be aligned: %+v", epi)
	}

	rec := run(t, "sync", "reconcile", "--db", dbPath, "--layer", "all", "--json")
	if rec.code != 0 {
		t.Fatalf("sync reconcile exit=%d stdout=%s stderr=%s", rec.code, rec.stdout, rec.stderr)
	}
	var rep map[string]any
	if err := json.Unmarshal([]byte(rec.stdout), &rep); err != nil {
		t.Fatalf("decode reconcile: %v (%s)", err, rec.stdout)
	}
	for _, key := range []string{"episodic_pushed_to_remote", "background_pushed_to_remote", "noetic_pushed_to_remote"} {
		if rep[key].(float64) != 0 {
			t.Fatalf("local mode %s = %v, want 0: %+v", key, rep[key], rep)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("local mode made %d cloud requests, want 0", got)
	}
}

func TestSyncReportsUnboundVaultWithoutLeakingData(t *testing.T) {
	env := newEnv(t)
	dbPath := filepath.Join(env.base, "unbound.db")
	const secret = "fixture-private-document-secret"
	const key = "fixture-private-api-key"
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("ROLLING_MEMORY_BRAIN_URL", srv.URL)
	t.Setenv("ROLLING_MEMORY_BRAIN_KEY", key)

	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE thin_vault_docs (doc_id TEXT PRIMARY KEY, raw_text TEXT NOT NULL);
		INSERT INTO thin_vault_docs (doc_id, raw_text) VALUES ('unbound-doc', ?)`, secret); err != nil {
		t.Fatalf("seed unbound vault: %v", err)
	}

	for _, command := range []string{"status", "reconcile"} {
		for _, jsonOutput := range []bool{false, true} {
			args := []string{"sync", command, "--db", dbPath}
			if jsonOutput {
				args = append(args, "--json")
			}
			res := run(t, args...)
			output := res.stdout + res.stderr
			if res.code == 0 {
				t.Fatalf("sync %s json=%v unexpectedly succeeded: %s", command, jsonOutput, output)
			}
			for _, clue := range []string{"local_plain vault scope is unbound", "existing data is preserved", "Back up", "verify the ownership", "explicit authorization"} {
				if !strings.Contains(output, clue) {
					t.Errorf("sync %s json=%v missing %q: %s", command, jsonOutput, clue, output)
				}
			}
			for _, sensitive := range []string{secret, key} {
				if strings.Contains(output, sensitive) {
					t.Errorf("sync %s json=%v leaked sensitive fixture", command, jsonOutput)
				}
			}
		}
	}
	var saved string
	if err := db.QueryRow(`SELECT raw_text FROM thin_vault_docs WHERE doc_id = 'unbound-doc'`).Scan(&saved); err != nil || saved != secret {
		t.Fatalf("unbound document changed: value=%q err=%v", saved, err)
	}
	var bindings int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'thin_vault_meta'`).Scan(&bindings); err != nil || bindings != 0 {
		t.Fatalf("unbound vault was bound: tables=%d err=%v", bindings, err)
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("local_plain made %d cloud requests, want 0", got)
	}
}
