// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rocareer/rolling-memory/internal/api"
	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/episodicunit"
	_ "modernc.org/sqlite"
)

func TestEpisodicSourceScanJSONLResumeAndChange(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".claude", "projects", "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 105; i++ {
		path := filepath.Join(dir, fmt.Sprintf("%03d.jsonl", i))
		if err := os.WriteFile(path, []byte(fmt.Sprintf(`{"type":"user","message":{"role":"user","content":"item %d"}}`+"\n", i)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sources, err := discoverEpisodicScanSources(context.Background(), home, "claude-code")
	if err != nil || len(sources) != 105 {
		t.Fatalf("discovery sources=%d err=%v", len(sources), err)
	}
	seen := map[string]int{}
	cursor := episodicSourceCursor{}
	for {
		var more bool
		cursor, more, err = visitEpisodicSources(context.Background(), sources, "mem:test", cursor, 7, func(turn candidateEpisodicTurn) error {
			seen[turn.RefPointer]++
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	if len(seen) != 105 {
		t.Fatalf("visited %d of 105 files", len(seen))
	}
	for pointer, count := range seen {
		if count != 1 {
			t.Fatalf("%s visited %d times", pointer, count)
		}
	}

	path := sources[0].Path
	var lines strings.Builder
	for i := 0; i < 1001; i++ {
		fmt.Fprintf(&lines, `{"type":"user","message":{"role":"user","content":"turn %d"}}`+"\n", i)
	}
	if err := os.WriteFile(path, []byte(lines.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("save failed")
	cursor, more, err := visitEpisodicSources(context.Background(), sources[:1], "mem:test", episodicSourceCursor{}, 1001, func(turn candidateEpisodicTurn) error {
		if strings.Contains(turn.Text, "turn 7") {
			return failure
		}
		return nil
	})
	if !errors.Is(err, failure) || !more || cursor.Index != 7 {
		t.Fatalf("failure cursor=%+v more=%v err=%v", cursor, more, err)
	}
	if err := os.WriteFile(path, []byte(lines.String()+`{"type":"user","message":{"role":"user","content":"new turn"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var first string
	_, _, err = visitEpisodicSources(context.Background(), sources[:1], "mem:test", cursor, 1, func(turn candidateEpisodicTurn) error { first = turn.Text; return nil })
	if err != nil || !strings.Contains(first, "turn 7") {
		t.Fatalf("changed source did not resume failed turn: first=%q err=%v", first, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	_, more, err = visitEpisodicSources(context.Background(), sources[:1], "mem:test", cursor, 1, func(candidateEpisodicTurn) error { return nil })
	if err == nil || !more {
		t.Fatalf("missing source: more=%v err=%v", more, err)
	}
}

func TestEpisodicSourceScanTickRestartFailureAndFairness(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".claude", "projects", "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "a.jsonl")
	var transcript strings.Builder
	for i := 0; i < reconcileDefaultLimit+2; i++ {
		fmt.Fprintf(&transcript, `{"type":"user","message":{"role":"user","content":"entry %d"}}`+"\n", i)
	}
	original := transcript.String()
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "z.jsonl")
	if err := os.WriteFile(other, []byte(`{"type":"user","message":{"role":"user","content":"other session"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(other, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{DB: filepath.Join(home, "memory.db"), Scope: "mem:scan-test", Mode: "local"}
	memory, err := api.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := runEpisodicSourceScanTick(context.Background(), memory, home); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := memory.Store.DB().QueryRow(`SELECT count(*) FROM thin_vault_docs WHERE layer = 'episodic'`).Scan(&count); err != nil || count != reconcileDefaultLimit {
		t.Fatalf("first page count=%d err=%v", count, err)
	}
	if err := memory.Close(); err != nil {
		t.Fatal(err)
	}
	memory, err = api.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	failedID := episodicunit.DocID(cfg.Scope, fmt.Sprintf("[user] entry %d", reconcileDefaultLimit))
	if _, err := memory.Store.DB().Exec(`CREATE TRIGGER scan_test_failure BEFORE INSERT ON thin_vault_episodic_index WHEN NEW.doc_id = '` + failedID + `' BEGIN SELECT RAISE(ABORT, 'write failed'); END`); err != nil {
		t.Fatal(err)
	}
	if err := runEpisodicSourceScanTick(context.Background(), memory, home); err == nil {
		t.Fatal("expected index failure")
	}
	if err := memory.Store.DB().QueryRow(`SELECT count(*) FROM thin_vault_docs WHERE doc_id = ?`, failedID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed write persisted document count=%d err=%v", count, err)
	}
	if _, err := memory.Store.DB().Exec(`DROP TRIGGER scan_test_failure`); err != nil {
		t.Fatal(err)
	}
	// Appends must not restart the first page or starve the other session.
	for i := 0; i < 3; i++ {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, err = fmt.Fprintf(f, `{"type":"user","message":{"role":"user","content":"new entry %d"}}`+"\n", i)
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if err := runEpisodicSourceScanTick(context.Background(), memory, home); err != nil {
			t.Fatal(err)
		}
	}
	oldTime = time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := runEpisodicSourceScanTick(context.Background(), memory, home); err != nil {
		t.Fatal(err)
	}
	if err := runEpisodicSourceScanTick(context.Background(), memory, home); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{fmt.Sprintf("[user] entry %d", reconcileDefaultLimit), "[user] other session", "[user] new entry 2"} {
		id := episodicunit.DocID(cfg.Scope, text)
		if err := memory.Store.DB().QueryRow(`SELECT count(*) FROM thin_vault_docs WHERE doc_id = ?`, id).Scan(&count); err != nil || count != 1 {
			t.Fatalf("missing %q count=%d err=%v", text, count, err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.HasPrefix(string(data), original) {
		t.Fatalf("source was modified: %v", err)
	}
}

func TestEpisodicSourceScanIndependentScopesAndSources(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".claude", "projects", "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 105; i++ {
		path := filepath.Join(dir, fmt.Sprintf("%03d.jsonl", i))
		if err := os.WriteFile(path, []byte(fmt.Sprintf(`{"type":"user","timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","content":"record %d"}}`+"\n", i)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(home, ".zcode", "cli", "db", "db.sqlite")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("invalid database"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i, scope := range []string{"mem:scan-one", "mem:scan-two"} {
		memory, err := api.New(&config.Config{DB: filepath.Join(home, fmt.Sprintf("memory-%d.db", i)), Scope: scope, Mode: "local"})
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 3; i++ {
			if err := runEpisodicSourceScanTick(context.Background(), memory, home); err == nil {
				t.Fatal("expected unavailable zcode source to be reported")
			}
		}
		var count int
		if err := memory.Store.DB().QueryRow(`SELECT count(*) FROM thin_vault_docs WHERE layer = 'episodic'`).Scan(&count); err != nil || count != 105 {
			t.Fatalf("scope %s: count=%d err=%v", scope, count, err)
		}
		_ = memory.Close()
	}
}

func TestEpisodicSourceScanRetainsDuplicateLineage(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".claude", "projects", "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.jsonl", "b.jsonl"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(`{"type":"user","timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","content":"same record"}}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	memory, err := api.New(&config.Config{DB: filepath.Join(home, "memory.db"), Scope: "mem:lineage", Mode: "local"})
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	if err := runEpisodicSourceScanTick(context.Background(), memory, home); err != nil {
		t.Fatal(err)
	}
	var documents, lineage int
	if err := memory.Store.DB().QueryRow(`SELECT count(*) FROM thin_vault_docs WHERE layer = 'episodic'`).Scan(&documents); err != nil {
		t.Fatal(err)
	}
	if err := memory.Store.DB().QueryRow(`SELECT count(*) FROM thin_vault_episodic_lineage`).Scan(&lineage); err != nil {
		t.Fatal(err)
	}
	if documents != 1 || lineage != 2 {
		t.Fatalf("documents=%d lineage=%d", documents, lineage)
	}
}

func TestEpisodicSourceScanZCodeActiveWAL(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".zcode", "cli", "db", "db.sqlite")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; CREATE TABLE session(id TEXT, parent_id TEXT, task_type TEXT); CREATE TABLE message(id TEXT, session_id TEXT, sequence INTEGER, time_created INTEGER, data TEXT); CREATE TABLE part(message_id TEXT, sequence INTEGER, data TEXT); INSERT INTO session VALUES ('a','','interactive');`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 120; i++ {
		id := fmt.Sprintf("m%03d", i)
		if _, err := db.Exec(`INSERT INTO message VALUES (?, 'a', ?, 1, '{"role":"user"}')`, id, i); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO part VALUES (?, 1, ?)`, id, fmt.Sprintf(`{"type":"text","text":"wal record %d"}`, i)); err != nil {
			t.Fatal(err)
		}
	}
	sources, err := discoverEpisodicScanSources(context.Background(), home, "zcode")
	if err != nil {
		t.Fatal(err)
	}
	cursor := episodicSourceCursor{}
	seen := map[string]bool{}
	for page := 0; page < 3; page++ {
		if _, err := db.Exec(`UPDATE session SET task_type = 'interactive' WHERE id = 'a'`); err != nil {
			t.Fatal(err)
		}
		var more bool
		cursor, more, err = visitEpisodicSources(context.Background(), sources, "mem:test", cursor, 50, func(turn candidateEpisodicTurn) error {
			seen[turn.RefPointer] = true
			return nil
		})
		if err != nil || (page < 2 && !more) {
			t.Fatalf("page %d more=%v err=%v", page, more, err)
		}
	}
	if len(seen) != 120 {
		t.Fatalf("visited %d of 120 records", len(seen))
	}
}

func TestEpisodicSourceScanZCodeSessions(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".zcode", "cli", "db", "db.sqlite")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE session(id TEXT, parent_id TEXT, task_type TEXT); CREATE TABLE message(id TEXT, session_id TEXT, sequence INTEGER, time_created INTEGER, data TEXT); CREATE TABLE part(message_id TEXT, sequence INTEGER, data TEXT);
	INSERT INTO session VALUES ('a', '', 'interactive'), ('b', '', 'interactive');
	INSERT INTO message VALUES ('a1','a',1,1,'{"role":"user"}'), ('b1','b',1,1,'{"role":"user"}');
	INSERT INTO part VALUES ('a1',1,'{"type":"text","text":"first"}'), ('b1',1,'{"type":"text","text":"second"}');`)
	_ = db.Close()
	if err != nil {
		t.Fatal(err)
	}
	sources, err := discoverEpisodicScanSources(context.Background(), home, "zcode")
	if err != nil || len(sources) != 2 {
		t.Fatalf("sources=%+v err=%v", sources, err)
	}
	seen := map[string]bool{}
	cursor := episodicSourceCursor{}
	for {
		var more bool
		cursor, more, err = visitEpisodicSources(context.Background(), sources, "mem:test", cursor, 1, func(turn candidateEpisodicTurn) error { seen[turn.SessionID] = true; return nil })
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	if !seen["a"] || !seen["b"] {
		t.Fatalf("sessions visited: %v", seen)
	}
}

// TestResolveJSONLTranscriptLocusCanonicalIdentity 锁定入库面的工作区身份口径：
// 会话 cwd 解析为仓库根 basename（子目录归仓库），非仓库目录得到已解析的空串
// （而不是目录名），只有读不到 cwd 时才报告未解析（调用方保留存量 locus）。
func TestResolveJSONLTranscriptLocusCanonicalIdentity(t *testing.T) {
	repoRoot := filepath.Join(t.TempDir(), "ws-repo")
	if err := os.MkdirAll(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	plainDir := filepath.Join(t.TempDir(), "codex-smoke")
	if err := os.MkdirAll(plainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTranscript := func(cwd string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "sess.jsonl")
		body := fmt.Sprintf(`{"type":"user","cwd":%q,"message":{"role":"user","content":"hi"}}`+"\n", cwd)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	noCwd := filepath.Join(t.TempDir(), "nocwd.jsonl")
	if err := os.WriteFile(noCwd, []byte(`{"type":"user","message":{"role":"user","content":"hi"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name      string
		path      string
		wantLocus string
		wantOK    bool
	}{
		{"repo root", writeTranscript(repoRoot), "ws-repo", true},
		{"repo subdirectory", writeTranscript(filepath.Join(repoRoot, "admin", "web")), "ws-repo", true},
		{"non-repo directory", writeTranscript(plainDir), "", true},
		{"no cwd line", noCwd, "", false},
		{"missing file", filepath.Join(t.TempDir(), "nope.jsonl"), "", false},
	}
	for _, tc := range cases {
		loc, ok := resolveJSONLTranscriptLocus(tc.path)
		if loc != tc.wantLocus || ok != tc.wantOK {
			t.Errorf("%s: resolveJSONLTranscriptLocus = (%q, %v), want (%q, %v)", tc.name, loc, ok, tc.wantLocus, tc.wantOK)
		}
	}
}
