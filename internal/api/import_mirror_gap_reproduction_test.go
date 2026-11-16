// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package api_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/rocareer/rolling-memory/internal/store"
	"github.com/rocareer/rolling-memory/internal/version"
)

// This verifies that JSON export import with replay mirrors shared traces into
// thin_vault_docs and remains idempotent across subsequent transcript imports.
func TestJSONImportReplayMirrorGapReproduction(t *testing.T) {
	source := newMemory(t)
	text := "synthetic JSON mirror gap witness"
	if _, inserted, err := source.Store.AddTrace("mem:test", text, store.TraceOptions{Session: "json-gap", Audience: "shared"}); err != nil || !inserted {
		t.Fatalf("source trace: inserted=%v err=%v", inserted, err)
	}
	payload, err := source.Store.ExportScope("mem:test")
	if err != nil {
		t.Fatal(err)
	}
	payload["format"] = version.ExportFormat
	payload["scope"] = "mem:test"
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "export.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	target := newMemory(t)
	if _, err := target.ImportFile(path, nil, nil, "shared", true); err != nil {
		t.Fatal(err)
	}
	var processed, docs int
	if err := target.Store.DB().QueryRow(`SELECT processed FROM trace WHERE text = ?`, text).Scan(&processed); err != nil {
		t.Fatal(err)
	}
	if _, err := target.ImportFile(writeTranscript(t, "later.txt", "later synthetic witness\n"), nil, nil, "shared", false); err != nil {
		t.Fatal(err)
	}
	if err := target.Store.DB().QueryRow(`SELECT count(*) FROM thin_vault_docs WHERE raw_text = ?`, text).Scan(&docs); err != nil {
		t.Fatal(err)
	}
	if processed != 1 || docs != 1 {
		t.Fatalf("JSON import mirror failed: processed=%d docs=%d, want 1/1", processed, docs)
	}
}

// This verifies that a shared trace inserted between a mirror snapshot and
// replay's MarkProcessed is still mirrored on the next import pass.
func TestMirrorSnapshotReplayInterleavingGapReproduction(t *testing.T) {
	memory := newMemory(t)
	snapshot, err := memory.Store.PendingVaultTraces("mem:test", nil)
	if err != nil || len(snapshot) != 0 {
		t.Fatalf("initial snapshot = %v, %v", snapshot, err)
	}
	text := "synthetic interleaving gap witness"
	hash, inserted, err := memory.Store.AddTrace("mem:test", text, store.TraceOptions{Session: "interleaving", Audience: "shared"})
	if err != nil || !inserted {
		t.Fatalf("late trace: inserted=%v err=%v", inserted, err)
	}
	pending, err := memory.Store.PendingTraces("mem:test", 10, nil)
	if err != nil || len(pending) != 1 || pending[0].Hash != hash {
		t.Fatalf("replay snapshot = %v, %v", pending, err)
	}
	if err := memory.Store.MarkProcessed(hash); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.ImportFile(writeTranscript(t, "later.txt", "later synthetic witness\n"), nil, nil, "shared", false); err != nil {
		t.Fatal(err)
	}
	var docs int
	if err := memory.Store.DB().QueryRow(`SELECT count(*) FROM thin_vault_docs WHERE raw_text = ?`, text).Scan(&docs); err != nil || docs != 1 {
		t.Fatalf("interleaved trace mirror failed: docs=%d err=%v, want 1", docs, err)
	}
}
