// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli_test

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rocareer/rolling-memory/internal/episodicunit"
	"github.com/rocareer/rolling-memory/internal/langpack"
	"github.com/rocareer/rolling-memory/internal/thinclient"
	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

// TestEpisodicPointerIndexReconcile exercises the local_plain sync path with a
// parseable harness transcript, including a pre-existing unindexed pointer.
func TestEpisodicPointerIndexReconcile(t *testing.T) {
	env := newEnv(t)
	dbPath := filepath.Join(env.base, "memory.db")
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("ROLLING_MEMORY_BRAIN_URL", "")
	t.Setenv("ROLLING_MEMORY_BRAIN_KEY", "")
	t.Setenv("ROLLING_MEMORY_LANGPACK", "multi")
	keyPath := filepath.Join(env.base, "cloud.key")
	if err := os.WriteFile(keyPath, []byte("test-local-only-key"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	cfg := "[mode]\nname = \"local\"\n\n[cloud.sync]\nbase_url = \"" + srv.URL +
		"/open/memory/v1\"\napi_key_file = \"" + keyPath + "\"\n"
	if err := os.WriteFile(filepath.Join(env.base, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	sessionDir := filepath.Join(os.Getenv("HOME"), ".claude", "projects", "demo")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	path := filepath.Join(sessionDir, "s1.jsonl")
	transcript := strings.Join([]string{
		`{"type":"user","sessionId":"s1","timestamp":"2026-01-06T03:00:00Z","message":{"role":"user","content":"violet observatory telescope"}}`,
		`{"type":"assistant","sessionId":"s1","timestamp":"2026-01-06T03:00:10Z","message":{"role":"assistant","content":"amber greenhouse seedlings"}}`,
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(transcript), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	reconcile := func() map[string]any {
		t.Helper()
		res := run(t, "sync", "reconcile", "--db", dbPath, "--harness", "claude-code", "--layer", "episodic", "--json")
		if res.code != 0 {
			t.Fatalf("sync reconcile exit=%d stdout=%s stderr=%s", res.code, res.stdout, res.stderr)
		}
		rep := jsonObject(t, res.stdout)
		for _, key := range []string{"episodic_pushed_to_remote", "background_pushed_to_remote", "noetic_pushed_to_remote"} {
			if rep[key] != float64(0) {
				t.Fatalf("local_plain %s = %v", key, rep[key])
			}
		}
		return rep
	}
	first := reconcile()
	if first["episodic_imported_from_harness"].(float64) < 2 {
		t.Fatalf("expected both transcript segments, got %+v", first)
	}

	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open vault: %v", err)
	}
	defer db.Close()
	vault, err := thinclient.NewVault(db, nil)
	if err != nil {
		t.Fatalf("open thinclient vault: %v", err)
	}
	// A raw Vault uses different encoding options from api.Memory.ThinClient.
	// Keep the same multi language pack and date harmonics on both sides.
	if err := langpack.ConfigureVault(vault, "multi"); err != nil {
		t.Fatalf("configure vault language pack: %v", err)
	}
	vault.SetCanonicalHarmonics(ramesdk.CanonicalHarmonicOptions{EnableStructuredDates: true})
	assertSearch := func(query string) string {
		t.Helper()
		hits, err := vault.SearchClientSDKDefault(query, 10)
		if err != nil || len(hits) == 0 {
			t.Fatalf("search %q: hits=%+v err=%v", query, hits, err)
		}
		id := hits[0].UnitID
		text, _, _, err := vault.LoadLocalDocument(id)
		if err != nil || !strings.Contains(text, query) {
			t.Fatalf("hydrate %q (%s): text=%q err=%v", query, id, text, err)
		}
		return id
	}
	userID := assertSearch("violet observatory telescope")
	assistantID := assertSearch("amber greenhouse seedlings")
	if userID == assistantID {
		t.Fatalf("distinct transcript segments have same unit ID %q", userID)
	}
	// A second Vault sharing the persisted secret but opened without the
	// production encoding options must still READ rows written under any codec
	// generation: reads are a union over generations, ordered so the current
	// generation wins per document. An SDK or language-pack bump must never
	// orphan previously indexed speech. The secret remains the access boundary —
	// rows whose MAC fails under this vault's key stay invisible (covered by
	// thinclient codec-isolation tests).
	unconfigured, err := thinclient.NewVault(db, nil)
	if err != nil {
		t.Fatalf("open unconfigured vault: %v", err)
	}
	compatible, incompatible, missing, err := unconfigured.EpisodicIndexDiagnostics()
	if err != nil || compatible != 0 || incompatible < 2 || missing != 0 {
		t.Fatalf("unconfigured index diagnostics: compatible=%d incompatible=%d missing=%d err=%v", compatible, incompatible, missing, err)
	}
	if hits, err := unconfigured.SearchClientSDKDefault("violet observatory telescope", 10); err != nil || len(hits) != 1 || hits[0].UnitID != userID {
		t.Fatalf("unconfigured union read: hits=%+v err=%v want unit=%s", hits, err, userID)
	}
	status := run(t, "sync", "status", "--db", dbPath, "--json")
	if status.code != 0 {
		t.Fatalf("sync status exit=%d stderr=%s", status.code, status.stderr)
	}
	indexStatus, ok := jsonObject(t, status.stdout)["episodic_index"].(map[string]any)
	if !ok || indexStatus["compatible"] != float64(2) || indexStatus["missing"] != float64(0) {
		t.Fatalf("index status = %+v", indexStatus)
	}
	var pointer string
	if err := db.QueryRow(`SELECT raw_text FROM thin_vault_docs WHERE doc_id = ?`, userID).Scan(&pointer); err != nil {
		t.Fatalf("read pointer: %v", err)
	}
	if !strings.HasPrefix(pointer, "@ref:jsonl:") {
		t.Fatalf("stored text = %q, want JSONL pointer", pointer)
	}

	// Simulate a pointer saved before the compact index existed, without changing
	// the authoritative row or the harness transcript.
	if _, err := db.Exec(`DELETE FROM thin_vault_episodic_index WHERE doc_id = ?`, userID); err != nil {
		t.Fatalf("remove test index: %v", err)
	}
	second := reconcile()
	if second["episodic_imported_from_harness"] != float64(0) || second["episodic_indexes_backfilled"] != float64(1) {
		t.Fatalf("existing unit backfill report: %+v", second)
	}
	if got := assertSearch("violet observatory telescope"); got != userID {
		t.Fatalf("backfilled unit = %q, want %q", got, userID)
	}
	var after string
	if err := db.QueryRow(`SELECT raw_text FROM thin_vault_docs WHERE doc_id = ?`, userID).Scan(&after); err != nil || after != pointer {
		t.Fatalf("backfill changed pointer: before=%q after=%q err=%v", pointer, after, err)
	}
	third := reconcile()
	if third["episodic_imported_from_harness"] != float64(0) || third["episodic_indexes_backfilled"] != float64(0) {
		t.Fatalf("repeated reconcile changed units: %+v", third)
	}
	if got := assertSearch("violet observatory telescope"); got != userID {
		t.Fatalf("repeated reconcile changed unit = %q, want %q", got, userID)
	}
	// A pre-existing inline document must remain inline, even when its content
	// matches a scanned source pointer.
	inline := "[assistant] amber greenhouse seedlings"
	if _, err := db.Exec(`UPDATE thin_vault_docs SET raw_text = ?, sentences_json = ? WHERE doc_id = ?`, inline, `["preserve"]`, assistantID); err != nil {
		t.Fatalf("seed inline document: %v", err)
	}
	reconcile()
	var storedInline, sentences string
	if err := db.QueryRow(`SELECT raw_text, sentences_json FROM thin_vault_docs WHERE doc_id = ?`, assistantID).Scan(&storedInline, &sentences); err != nil || storedInline != inline || sentences != `["preserve"]` {
		t.Fatalf("inline document changed: text=%q sentences=%q err=%v", storedInline, sentences, err)
	}
	if got := atomic.LoadInt32(&requests); got != 0 {
		t.Fatalf("local_plain made %d cloud requests", got)
	}
}

func TestEpisodicIndexReconcilePreservesUnverifiedSources(t *testing.T) {
	env := newEnv(t)
	t.Setenv("ROLLING_MEMORY_BRAIN_URL", "")
	t.Setenv("ROLLING_MEMORY_BRAIN_KEY", "")
	dbPath := filepath.Join(env.base, "memory.db")
	sessionDir := filepath.Join(os.Getenv("HOME"), ".claude", "projects", "demo")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	path := filepath.Join(sessionDir, "s1.jsonl")
	transcript := strings.Join([]string{
		`{"type":"user","sessionId":"s1","timestamp":"2026-01-06T03:00:00Z","message":{"role":"user","content":"new source contents"}}`,
		`{"type":"assistant","sessionId":"s1","timestamp":"2026-01-06T03:00:10Z","message":{"role":"assistant","content":"matching source contents"}}`,
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(transcript), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	if res := run(t, "sync", "status", "--db", dbPath, "--json"); res.code != 0 {
		t.Fatalf("init vault exit=%d stderr=%s", res.code, res.stderr)
	}
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open vault: %v", err)
	}
	defer db.Close()
	vault, err := thinclient.NewVault(db, nil)
	if err != nil {
		t.Fatalf("open thinclient vault: %v", err)
	}
	oldID := episodicunit.DocID("mem:test", "[user] original source contents")
	matchingID := episodicunit.DocID("mem:test", "[assistant] matching source contents")
	oldPointer := "@ref:jsonl:" + path + ":0#0"
	missingPointer := "@ref:jsonl:" + filepath.Join(sessionDir, "missing.jsonl") + ":0#0"
	for _, row := range []struct{ id, pointer string }{
		{oldID, oldPointer},
		{matchingID, missingPointer},
		{"ep_unknown_old_id", missingPointer},
	} {
		if err := vault.SaveLocalEpisodicPointer(row.id, 1767668400, row.pointer, "", thinclient.CloudIngestResponse{}); err != nil {
			t.Fatalf("seed pointer %s: %v", row.id, err)
		}
	}
	res := run(t, "sync", "reconcile", "--db", dbPath, "--harness", "claude-code", "--layer", "episodic", "--json")
	if res.code != 0 {
		t.Fatalf("sync reconcile exit=%d stdout=%s stderr=%s", res.code, res.stdout, res.stderr)
	}
	rep := jsonObject(t, res.stdout)
	if rep["episodic_index_source_changed"] != float64(1) || rep["episodic_index_pointer_mismatch"] != float64(1) || rep["episodic_index_scanned"] != float64(2) || rep["episodic_index_unverified"] != float64(3) {
		t.Fatalf("source coverage report: %+v", rep)
	}
	if rep["episodic_indexes_backfilled"] != float64(0) || !strings.Contains(res.stdout, "unavailable or unscanned historical sources are unknown") {
		t.Fatalf("unexpected backfill or coverage report: %+v", rep)
	}
	for _, row := range []struct{ id, pointer string }{
		{oldID, oldPointer},
		{matchingID, missingPointer},
		{"ep_unknown_old_id", missingPointer},
	} {
		var got string
		if err := db.QueryRow(`SELECT raw_text FROM thin_vault_docs WHERE doc_id = ?`, row.id).Scan(&got); err != nil || got != row.pointer {
			t.Fatalf("source %s changed: %q, err=%v", row.id, got, err)
		}
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM thin_vault_episodic_index WHERE doc_id = ?`, row.id).Scan(&count); err != nil || count != 0 {
			t.Fatalf("unverified source %s indexed: %d, err=%v", row.id, count, err)
		}
	}
}
