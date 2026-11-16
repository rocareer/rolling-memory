// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli_test

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestEpisodicWatchDrainsUnchangedJSONLBacklog checks that bounded watch passes
// resume without requiring another transcript modification.
func TestEpisodicWatchDrainsUnchangedJSONLBacklog(t *testing.T) {
	env := newEnv(t)
	t.Setenv("ROLLING_MEMORY_BRAIN_URL", "")
	t.Setenv("ROLLING_MEMORY_BRAIN_KEY", "")
	t.Setenv("CLAUDE_PROJECT_DIR", env.base)
	t.Setenv("ZCODE_PROJECT_DIR", "")
	t.Setenv("ROLLING_MEMORY_AUTO_SYNC", "true")
	sessionDir := filepath.Join(os.Getenv("HOME"), ".claude", "projects", "demo")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var transcript strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&transcript, "{\"type\":\"user\",\"timestamp\":\"2026-01-06T03:00:00Z\",\"message\":{\"role\":\"user\",\"content\":\"turn %d\"}}\n", i)
		fmt.Fprintf(&transcript, "{\"type\":\"assistant\",\"timestamp\":\"2026-01-06T03:00:10Z\",\"message\":{\"role\":\"assistant\",\"content\":\"response %d\"}}\n", i)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "long.jsonl"), []byte(transcript.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	older := filepath.Join(sessionDir, "older.jsonl")
	if err := os.WriteFile(older, []byte("{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":\"older session sentinel\"}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(older, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(env.base, "memory.db")
	for i := 0; i < 4; i++ {
		res := run(t, "sync", "watch", "--once", "--db", dbPath, "--json")
		if res.code != 0 {
			t.Fatalf("watch pass %d: exit=%d stdout=%s stderr=%s", i, res.code, res.stdout, res.stderr)
		}
		if i == 0 {
			firstDB, err := sql.Open("sqlite", "file:"+dbPath)
			if err != nil {
				t.Fatal(err)
			}
			var firstCount int
			err = firstDB.QueryRow(`SELECT COUNT(*) FROM thin_vault_docs WHERE layer = 'episodic'`).Scan(&firstCount)
			_ = firstDB.Close()
			if err != nil || firstCount == 0 {
				t.Fatalf("first watch pass did not import the fixture: count=%d err=%v", firstCount, err)
			}
		}
	}
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM thin_vault_docs WHERE layer = 'episodic'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 81 {
		t.Fatalf("unchanged backlog imported %d of 81 segments", count)
	}
}
