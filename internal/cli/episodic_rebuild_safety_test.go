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
)

func TestEpisodicRebuildRefusesUnverifiedSourcesWithoutDeletion(t *testing.T) {
	env := newEnv(t)
	dbPath := filepath.Join(env.base, "memory.db")
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	t.Setenv("ROLLING_MEMORY_BRAIN_URL", "")
	t.Setenv("ROLLING_MEMORY_BRAIN_KEY", "")
	keyPath := filepath.Join(env.base, "cloud.key")
	if err := os.WriteFile(keyPath, []byte("test-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := "[mode]\nname = \"local\"\n\n[cloud.sync]\nbase_url = \"" + srv.URL +
		"/open/memory/v1\"\napi_key_file = \"" + keyPath + "\"\n"
	if err := os.WriteFile(filepath.Join(env.base, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if res := run(t, "sync", "status", "--db", dbPath, "--json"); res.code != 0 {
		t.Fatalf("initialize vault: exit=%d stderr=%s", res.code, res.stderr)
	}
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, row := range []struct{ id, text string }{
		{"ep_unscanned", "historical content not present in the scanned sources"},
		{"ep_missing", "@ref:jsonl:/missing/transcript.jsonl:0#0"},
	} {
		if _, err := db.Exec(`INSERT INTO thin_vault_docs(doc_id, layer, occurred_at, raw_text, sentences_json) VALUES (?, 'episodic', 123, ?, '["original"]')`, row.id, row.text); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO thin_vault_episodic_lineage(turn_ref, doc_id, occurred_at) VALUES ('original-turn', ?, 123)`, row.id); err != nil {
			t.Fatal(err)
		}
	}
	assertPreserved := func() {
		t.Helper()
		for _, row := range []struct{ id, text string }{
			{"ep_unscanned", "historical content not present in the scanned sources"},
			{"ep_missing", "@ref:jsonl:/missing/transcript.jsonl:0#0"},
		} {
			var text, sentences string
			var occurred int64
			if err := db.QueryRow(`SELECT raw_text, sentences_json, occurred_at FROM thin_vault_docs WHERE doc_id = ?`, row.id).Scan(&text, &sentences, &occurred); err != nil || text != row.text || sentences != `["original"]` || occurred != 123 {
				t.Fatalf("document %s changed: text=%q sentences=%q occurred=%d err=%v", row.id, text, sentences, occurred, err)
			}
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM thin_vault_episodic_lineage WHERE doc_id = ? AND turn_ref = 'original-turn' AND occurred_at = 123`, row.id).Scan(&count); err != nil || count != 1 {
				t.Fatalf("lineage %s changed: count=%d err=%v", row.id, count, err)
			}
		}
	}
	dry := run(t, "sync", "reconcile", "--db", dbPath, "--rebuild-episodic", "--json")
	if dry.code != 0 {
		t.Fatalf("dry-run: exit=%d stderr=%s", dry.code, dry.stderr)
	}
	report := jsonObject(t, dry.stdout)
	if report["mode"] != "dry_run" || report["units_removed"] != float64(2) || !strings.Contains(dry.stdout, "not evidence of deletion") {
		t.Fatalf("dry-run estimate: %+v", report)
	}
	assertPreserved()
	textDry := run(t, "sync", "reconcile", "--db", dbPath, "--rebuild-episodic")
	if textDry.code != 0 || !strings.Contains(textDry.stdout, "not evidence of deletion") {
		t.Fatalf("text dry-run: exit=%d stdout=%s stderr=%s", textDry.code, textDry.stdout, textDry.stderr)
	}
	apply := run(t, "sync", "reconcile", "--db", dbPath, "--rebuild-episodic", "--yes", "--json")
	if apply.code == 0 || !strings.Contains(apply.stdout, "completeness cannot be verified") {
		t.Fatalf("apply: exit=%d stdout=%s stderr=%s", apply.code, apply.stdout, apply.stderr)
	}
	assertPreserved()
	if got := atomic.LoadInt32(&requests); got != 0 {
		t.Fatalf("rebuild made %d remote requests", got)
	}
}
