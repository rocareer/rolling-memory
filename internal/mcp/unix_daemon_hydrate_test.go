// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package mcp

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// seedZCodeTurnFixture writes a minimal ZCode host database holding one turn
// (user question + assistant reply with a reasoning part and a text part) under
// `$HOME/.zcode/cli/db/db.sqlite`, the exact location the canonical resolver reads.
func seedZCodeTurnFixture(t *testing.T, home string) {
	t.Helper()
	dbDir := filepath.Join(home, ".zcode", "cli", "db")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatalf("mkdir zcode fixture: %v", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dbDir, "db.sqlite"))
	if err != nil {
		t.Fatalf("open zcode fixture: %v", err)
	}
	defer db.Close()
	stmts := []string{
		`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT, sequence INTEGER, time_created INTEGER, data TEXT)`,
		`CREATE TABLE part (message_id TEXT, sequence INTEGER, data TEXT)`,
		`INSERT INTO message (id, session_id, sequence, time_created, data) VALUES
			('msg_hydrate_u1', 'sess_hydrate_fixture', 1, 1700000000, '{"role":"user"}')`,
		`INSERT INTO part (message_id, sequence, data) VALUES
			('msg_hydrate_u1', 0, '{"type":"text","text":"fixture question"}')`,
		`INSERT INTO message (id, session_id, sequence, time_created, data) VALUES
			('msg_hydrate_a1', 'sess_hydrate_fixture', 2, 1700000001, '{"role":"assistant"}')`,
		`INSERT INTO part (message_id, sequence, data) VALUES
			('msg_hydrate_a1', 0, '{"type":"reasoning","text":"hidden"}'),
			('msg_hydrate_a1', 1, '{"type":"text","text":"fixture reply body"}')`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed zcode fixture (%s): %v", stmt, err)
		}
	}
}

// TestUnixDaemonHydrateCanonicalSegments drives POST /v1/hydrate over the local Unix
// socket against a fixture host database: the endpoint must answer with the canonical
// per-segment text (the same resolver `recall` / `recent` use) and fail closed on a
// database mismatch or a non-POST method.
func TestUnixDaemonHydrateCanonicalSegments(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	seedZCodeTurnFixture(t, home)

	memory := newMemory(t)
	// Unix socket paths are capped at 104 bytes on BSD/macOS; a short /tmp base keeps
	// this test independent of the (long) t.TempDir prefix.
	sockDir, err := os.MkdirTemp("/tmp", "rm-hyd-")
	if err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	sockPath := filepath.Join(sockDir, "engine.sock")
	t.Setenv(DaemonSocketEnv, sockPath)

	done := make(chan error, 1)
	go func() {
		done <- ServeUnix(sockPath, memory, nil)
	}()
	t.Cleanup(func() {
		requestUnixDaemonShutdown(sockPath)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if h, err := PingUnixDaemon(sockPath, 100*time.Millisecond); err == nil && h != nil && h.OK {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}

	client := newUnixHTTPClient(sockPath, 5*time.Second)
	post := func(payload UnixHydratePayload) (int, UnixHydrateResponse) {
		t.Helper()
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal hydrate payload: %v", err)
		}
		resp, err := client.Post("http://unix/v1/hydrate", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("hydrate post: %v", err)
		}
		defer resp.Body.Close()
		var out UnixHydrateResponse
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	// 1. Canonical segmentation: #0 is the user question, #1 the assistant text part
	//    (the reasoning part is not speech and must not shift the index).
	status, out := post(UnixHydratePayload{Items: []UnixHydrateItem{{Ref: "@ref:zcode:sess_hydrate_fixture:msg_hydrate_u1#1"}}})
	if status != http.StatusOK || !out.OK || len(out.Texts) != 1 {
		t.Fatalf("unexpected hydrate response: %d %+v", status, out)
	}
	if got := strings.TrimSpace(out.Texts[0].Text); got != "[assistant] fixture reply body" {
		t.Fatalf("unexpected segment text: %q", got)
	}
	if out.Texts[0].Verified || out.Texts[0].Error != "" {
		t.Fatalf("pointer without document id must be served unverified and without error: %+v", out.Texts[0])
	}

	// 2. A document id whose sealed index is absent must degrade to the live
	//    canonical resolution instead of failing the batch.
	status, out = post(UnixHydratePayload{Items: []UnixHydrateItem{{DocID: "ep_fixture_doc", Ref: "@ref:zcode:sess_hydrate_fixture:msg_hydrate_u1#0"}}})
	if status != http.StatusOK || !out.OK || len(out.Texts) != 1 {
		t.Fatalf("unexpected hydrate response: %d %+v", status, out)
	}
	if got := strings.TrimSpace(out.Texts[0].Text); got != "[user] fixture question" {
		t.Fatalf("unexpected segment text: %q", got)
	}

	// 3. Unknown pointers surface a per-item error without failing the whole batch.
	status, out = post(UnixHydratePayload{Items: []UnixHydrateItem{
		{Ref: "@ref:zcode:sess_hydrate_fixture:msg_hydrate_u1#1"},
		{Ref: "@ref:zcode:sess_hydrate_fixture:msg_missing#0"},
		{Ref: "   "},
	}})
	if status != http.StatusOK || !out.OK || len(out.Texts) != 3 {
		t.Fatalf("unexpected hydrate response: %d %+v", status, out)
	}
	if out.Texts[1].Error == "" || out.Texts[2].Error == "" {
		t.Fatalf("missing pointers must carry per-item errors: %+v", out.Texts)
	}

	// 4. Non-POST methods are rejected.
	resp, err := client.Get("http://unix/v1/hydrate")
	if err != nil {
		t.Fatalf("GET /v1/hydrate: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for GET, got %d", resp.StatusCode)
	}

	// 5. A vault-bound daemon refuses requests targeting another database.
	status, out = post(UnixHydratePayload{DB: filepath.Join(t.TempDir(), "other.db"), Items: []UnixHydrateItem{{Ref: "@ref:zcode:x:y#0"}}})
	if status != http.StatusConflict || out.OK || !strings.Contains(out.Error, "daemon bound to") {
		t.Fatalf("expected db mismatch conflict, got %d %+v", status, out)
	}
}
