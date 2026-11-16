// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli_test

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestSyncStatusAndReconcileAllLayersAndPurgeLegacyTables(t *testing.T) {
	env := newEnv(t)
	dbPath := filepath.Join(env.base, "reconcile.db")

	var mu sync.Mutex
	remoteUnits := map[string]string{} // unit_id -> layer
	var ingestedIDs []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body := map[string]any{}
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")

		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/open/memory/v1/units":
			var ids []string
			for id := range remoteUnits {
				ids = append(ids, id)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success":  true,
				"count":    len(ids),
				"unit_ids": ids,
			})
		case "/open/memory/v1/graph":
			var nodes []map[string]any
			for id, lyr := range remoteUnits {
				nodes = append(nodes, map[string]any{
					"id":        "unit:" + id,
					"raw_id":    id,
					"node_type": "unit",
					"layer":     lyr,
					"weight":    1.0,
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"version": "v1",
				"nodes":   nodes,
				"edges":   []any{},
			})
		case "/open/memory/v1/ingest":
			uid, _ := body["id"].(string)
			lyr, _ := body["layer"].(string)
			if lyr == "" {
				lyr = "episodic"
			}
			remoteUnits[uid] = lyr
			ingestedIDs = append(ingestedIDs, uid)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":                    uid,
				"layer":                 lyr,
				"address_uri":           "C6:L1:B001#00",
				"address_coord64":       42,
				"waveform_bar":          "▃▆█▅▂",
				"waveform_hex":          "36AF52",
				"kept_sentence_indices": []int{0},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	t.Setenv("ROLLING_MEMORY_MODE", "cloud")
	t.Setenv("ROLLING_MEMORY_BRAIN_URL", srv.URL+"/open/memory/v1")
	t.Setenv("ROLLING_MEMORY_BRAIN_KEY", "test-u10-key")
	t.Setenv("ROLLING_MEMORY_USER_ID", "10")

	// Seed a Claude Code session transcript under HOME/.claude/projects/demo/s1.jsonl
	home := os.Getenv("HOME")
	claudeSessionDir := filepath.Join(home, ".claude", "projects", "demo")
	if err := os.MkdirAll(claudeSessionDir, 0o755); err != nil {
		t.Fatalf("mkdir claude session dir: %v", err)
	}
	transcriptLines := strings.Join([]string{
		`{"type":"user","sessionId":"s1","timestamp":"2026-11-06T03:00:00","message":{"role":"user","content":[{"type":"text","text":"请检查三层记忆健康度并对齐同步。"}]}}`,
		`{"type":"assistant","sessionId":"s1","timestamp":"2026-11-06T03:00:10","message":{"role":"assistant","content":[{"type":"text","text":"已完成 Episodic / Background / Noetic 对齐。"}]}}`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(claudeSessionDir, "s1.jsonl"), []byte(transcriptLines), 0o644); err != nil {
		t.Fatalf("write claude session: %v", err)
	}

	// Seed local SQLite with 1 background doc, 1 noetic row, and 1 legacy table (`trace`) before remote has them.
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	_, err = db.Exec(`
			CREATE TABLE IF NOT EXISTS thin_vault_meta (k TEXT PRIMARY KEY, v BLOB NOT NULL);
			INSERT OR REPLACE INTO thin_vault_meta(k, v) VALUES ('local_plain_scope', 'mem:test');
			CREATE TABLE IF NOT EXISTS thin_vault_docs (
			doc_id TEXT PRIMARY KEY, layer TEXT NOT NULL DEFAULT 'episodic', occurred_at INTEGER NOT NULL DEFAULT 0,
			raw_text TEXT NOT NULL, sentences_json TEXT NOT NULL DEFAULT '[]', address_uri TEXT NOT NULL DEFAULT '',
			coord64 INTEGER NOT NULL DEFAULT 0, waveform_bar TEXT NOT NULL DEFAULT '', doc_macro_wave TEXT NOT NULL DEFAULT '',
			kept_indices_json TEXT NOT NULL DEFAULT '[]'
		);
		CREATE TABLE IF NOT EXISTS thin_vault_noetic (
			noetic_id TEXT PRIMARY KEY, occurred_at INTEGER NOT NULL DEFAULT 0, status TEXT NOT NULL DEFAULT 'active',
			query_text TEXT NOT NULL, answer_text TEXT NOT NULL, cited_unit_ids_json TEXT NOT NULL DEFAULT '[]',
			cited_sentences_json TEXT NOT NULL DEFAULT '[]'
		);
		INSERT INTO thin_vault_docs(doc_id, layer, occurred_at, raw_text) VALUES ('bg_local_1', 'background', 1762380000, 'local background doc');
		INSERT INTO thin_vault_noetic(noetic_id, occurred_at, status, query_text, answer_text, cited_unit_ids_json)
		VALUES ('nt_local_1', 1762380100, 'active', 'what is RAME?', 'the memory engine', '["bg_local_1"]');
	`)
	if err != nil {
		_ = db.Close()
		t.Fatalf("seed local vault: %v", err)
	}
	_ = db.Close()

	// 1. Check status before reconcile: should report pending sync items across all 3 layers.
	stRes := run(t, "sync", "status", "--db", dbPath, "--harness", "claude-code", "--json")
	if stRes.code != 0 {
		t.Fatalf("sync status exit=%d stdout=%s stderr=%s", stRes.code, stRes.stdout, stRes.stderr)
	}
	var statusPayload map[string]any
	if err := json.Unmarshal([]byte(stRes.stdout), &statusPayload); err != nil {
		t.Fatalf("decode sync status: %v (%s)", err, stRes.stdout)
	}
	if statusPayload["active_harness_id"] != "claude-code" {
		t.Fatalf("expected active_harness_id=claude-code, got %v", statusPayload["active_harness_id"])
	}
	if statusPayload["total_pending_sync"].(float64) < 3 {
		t.Fatalf("expected total_pending_sync >= 3 before reconcile, got %v", statusPayload["total_pending_sync"])
	}

	// 2. Reconcile each layer in isolation. The three cognitive layers are mutually exclusive:
	// Harness turns are L2 episodic only, mounted folder chunks are L1 background only, and
	// Noetic records are recall Q/A consolidations only — a layer-scoped reconcile must never
	// move units across layers.
	runLayerReconcile := func(layer string) map[string]any {
		t.Helper()
		res := run(t, "sync", "reconcile", "--db", dbPath, "--harness", "claude-code", "--layer", layer, "--json")
		if res.code != 0 {
			t.Fatalf("sync reconcile --layer %s exit=%d stdout=%s stderr=%s", layer, res.code, res.stdout, res.stderr)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(res.stdout), &payload); err != nil {
			t.Fatalf("decode sync reconcile --layer %s: %v (%s)", layer, err, res.stdout)
		}
		return payload
	}

	epiPayload := runLayerReconcile("episodic")
	if epiPayload["episodic_imported_from_harness"].(float64) < 1 {
		t.Fatalf("expected Harness turns imported into episodic, got %+v", epiPayload)
	}
	if epiPayload["background_pushed_to_remote"].(float64) != 0 || epiPayload["noetic_pushed_to_remote"].(float64) != 0 {
		t.Fatalf("episodic-only reconcile leaked into other layers: %+v", epiPayload)
	}

	bgPayload := runLayerReconcile("background")
	if bgPayload["background_pushed_to_remote"].(float64) < 1 {
		t.Fatalf("expected mounted background docs pushed, got %+v", bgPayload)
	}
	if bgPayload["episodic_imported_from_harness"].(float64) != 0 || bgPayload["noetic_pushed_to_remote"].(float64) != 0 {
		t.Fatalf("background-only reconcile leaked into other layers: %+v", bgPayload)
	}

	noePayload := runLayerReconcile("noetic")
	if noePayload["noetic_pushed_to_remote"].(float64) < 1 {
		t.Fatalf("expected noetic consolidations pushed, got %+v", noePayload)
	}
	if noePayload["episodic_imported_from_harness"].(float64) != 0 || noePayload["background_pushed_to_remote"].(float64) != 0 {
		t.Fatalf("noetic-only reconcile leaked into other layers: %+v", noePayload)
	}

	// Every ingested unit must keep its layer identity: Harness turns (ep_*) are episodic,
	// mounted folder docs/chunks (bg*) are background, and recall consolidations (nt_*) are noetic.
	mu.Lock()
	if got := remoteUnits["bg_local_1"]; got != "background" {
		t.Fatalf("mounted background doc ingested as %q, want background", got)
	}
	if got := remoteUnits["nt_local_1"]; got != "noetic" {
		t.Fatalf("recall consolidation ingested as %q, want noetic", got)
	}
	episodicTurns := 0
	for id, layer := range remoteUnits {
		switch {
		case strings.HasPrefix(id, "ep_"):
			episodicTurns++
			if layer != "episodic" {
				t.Fatalf("Harness turn %s ingested as %q, want episodic (background is reserved for user-mounted folder files)", id, layer)
			}
		case strings.HasPrefix(id, "bg"):
			if layer != "background" {
				t.Fatalf("mounted folder unit %s ingested as %q, want background", id, layer)
			}
		}
	}
	if episodicTurns == 0 {
		t.Fatal("expected at least one Harness turn unit to be ingested")
	}
	mu.Unlock()

	postStatus, _ := runLayerReconcile("all")["status"].(map[string]any)
	if postStatus["total_pending_sync"].(float64) != 0 {
		t.Fatalf("expected total_pending_sync == 0 after reconcile, got %+v", postStatus)
	}

	// 3. Verify legacy tables (`trace`, `node`, `assertion`, `vector`) are preserved (never automatically dropped).
	dbCheck, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer dbCheck.Close()
	var legacyCnt int
	if err := dbCheck.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('trace','node','assertion','vector','node_vector')`).Scan(&legacyCnt); err != nil {
		t.Fatalf("check legacy tables: %v", err)
	}
	if legacyCnt != 5 {
		t.Fatalf("expected 5 preserved legacy tables in db, found %d", legacyCnt)
	}
}

// TestEpisodicRebuildDryRunThenApply verifies the estimate and the fail-closed apply.
func TestEpisodicRebuildDryRunThenApply(t *testing.T) {
	env := newEnv(t)
	dbPath := filepath.Join(env.base, "memory.db")

	// Initialise the vault through the normal CLI path.
	if res := run(t, "sync", "status", "--json"); res.code != 0 {
		t.Fatalf("sync status exit=%d stderr=%s", res.code, res.stderr)
	}

	home := os.Getenv("HOME")
	sessionDir := filepath.Join(home, ".claude", "projects", "demo")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	lines := strings.Join([]string{
		`{"type":"user","sessionId":"s1","timestamp":"2026-11-06T03:00:00","message":{"role":"user","content":"请检查缓存失效。"}}`,
		`{"type":"assistant","sessionId":"s1","timestamp":"2026-11-06T03:00:10","message":{"role":"assistant","content":[` +
			`{"type":"text","text":"先确认断言。"},` +
			`{"type":"text","text":"结论：缓存按 docID 失效。"}]}}`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(sessionDir, "s1.jsonl"), []byte(lines), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}

	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if _, err := db.Exec(`
		INSERT OR REPLACE INTO thin_vault_docs(doc_id, layer, occurred_at, raw_text, sentences_json)
		VALUES ('ep_legacy_turn', 'episodic', 1762398000, '[user] 请检查缓存失效。
[assistant] 先确认断言。
结论：缓存按 docID 失效。', '[]')`); err != nil {
		_ = db.Close()
		t.Fatalf("seed legacy unit: %v", err)
	}
	_ = db.Close()

	// 1. Dry run: no deletion, plan reported.
	res := run(t, "sync", "reconcile", "--rebuild-episodic", "--json")
	if res.code != 0 {
		t.Fatalf("dry-run exit=%d stdout=%s stderr=%s", res.code, res.stdout, res.stderr)
	}
	dry := jsonObject(t, res.stdout)
	if dry["mode"] != "dry_run" {
		t.Fatalf("expected dry_run mode, got %v", dry["mode"])
	}
	if dry["units_removed"].(float64) < 1 || dry["new_units"].(float64) < 3 {
		t.Fatalf("dry-run plan = %+v", dry)
	}
	db, _ = sql.Open("sqlite", "file:"+dbPath)
	var legacyAfterDry int
	_ = db.QueryRow(`SELECT count(*) FROM thin_vault_docs WHERE doc_id = 'ep_legacy_turn'`).Scan(&legacyAfterDry)
	_ = db.Close()
	if legacyAfterDry != 1 {
		t.Fatalf("dry-run must not delete anything, legacy row count = %d", legacyAfterDry)
	}

	// 2. Apply is refused because source completeness cannot be established.
	res = run(t, "sync", "reconcile", "--rebuild-episodic", "--yes", "--json")
	if res.code == 0 || !strings.Contains(res.stdout, "apply disabled") {
		t.Fatalf("apply exit=%d stdout=%s stderr=%s", res.code, res.stdout, res.stderr)
	}
	db, _ = sql.Open("sqlite", "file:"+dbPath)
	defer db.Close()
	var legacyAfterApply int
	if err := db.QueryRow(`SELECT count(*) FROM thin_vault_docs WHERE doc_id = 'ep_legacy_turn'`).Scan(&legacyAfterApply); err != nil {
		t.Fatalf("count legacy: %v", err)
	}
	if legacyAfterApply != 1 {
		t.Fatalf("apply must preserve legacy unit, count = %d", legacyAfterApply)
	}
}
