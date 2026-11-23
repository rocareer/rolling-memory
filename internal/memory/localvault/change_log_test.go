// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package localvault

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// The change log is the freshness contract: every doc/meta mutation appends one
// row and advances change_seq to the same value.
func TestChangeLogTracksDocsAndMeta(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "change_log.sqlite")
	db, err := sql.Open("sqlite", vaultTestDSN(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, err := NewVault(db, []byte("change-log-key"))
	if err != nil {
		t.Fatal(err)
	}
	for i, text := range []string{
		"Alice met Bob in Paris on 2026-01-02.",
		"Carol booked a hotel near the harbour.",
		"Dave paid fifty pounds for the boat tour.",
	} {
		if err := v.SaveLocalPlaintext("doc-"+string(rune('a'+i)), 1700000000+int64(i), text, nil, IngestResponse{Layer: "episodic"}); err != nil {
			t.Fatal(err)
		}
	}
	var changeSeq, maxSeq int64
	if err := db.QueryRow(`SELECT change_seq FROM rm_revision WHERE id = 1`).Scan(&changeSeq); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM rm_change`).Scan(&maxSeq); err != nil {
		t.Fatal(err)
	}
	if changeSeq == 0 || changeSeq != maxSeq {
		t.Fatalf("change_seq=%d max(seq)=%d", changeSeq, maxSeq)
	}
	for _, id := range []string{"doc-a", "doc-b", "doc-c"} {
		var upserts int
		if err := db.QueryRow(`SELECT COUNT(*) FROM rm_change WHERE unit_id = ? AND op = 'upsert'`, id).Scan(&upserts); err != nil {
			t.Fatal(err)
		}
		if upserts == 0 {
			t.Fatalf("%s has no upsert change row", id)
		}
	}
	if _, err := db.Exec(`DELETE FROM rm_docs WHERE unit_id = 'doc-a'`); err != nil {
		t.Fatal(err)
	}
	var finalOp string
	if err := db.QueryRow(`SELECT op FROM rm_change WHERE unit_id = 'doc-a' ORDER BY seq DESC LIMIT 1`).Scan(&finalOp); err != nil {
		t.Fatal(err)
	}
	if finalOp != "delete" {
		t.Fatalf("final op for doc-a = %q, want delete", finalOp)
	}
}

// The delta path must serve the cached docs view plus only the touched units,
// without the full canonical table scan.
func TestDeltaSnapshotUsesChangeLog(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "delta_snapshot.sqlite")
	db, err := sql.Open("sqlite", vaultTestDSN(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, err := NewVault(db, []byte("delta-snapshot-key"))
	if err != nil {
		t.Fatal(err)
	}
	for i, text := range []string{
		"Alice met Bob in Paris on 2026-01-02.",
		"Carol booked a hotel near the harbour.",
		"Dave paid fifty pounds for the boat tour.",
	} {
		if err := v.SaveLocalPlaintext("doc-"+string(rune('a'+i)), 1700000000+int64(i), text, nil, IngestResponse{Layer: "episodic"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := v.RebuildCorpusSnapshot(context.Background()); err != nil {
		t.Fatalf("rebuild snapshot: %v", err)
	}
	cached, err := v.buildCorpusSnapshot(context.Background())
	if err != nil || cached == nil || len(cached.docs) != 3 {
		t.Fatalf("cached snapshot: %v docs=%d", err, len(cached.docs))
	}
	if err := v.SaveLocalPlaintext("doc-d", 1700000010, "Erin took the train to Berlin in March.", nil, IngestResponse{Layer: "episodic"}); err != nil {
		t.Fatal(err)
	}
	state, ok := v.canonicalCorpusState(context.Background())
	if !ok {
		t.Fatal("canonical state")
	}
	eng, storedLine, hold, ok := v.acquireCachedCSREngine(dbPath)
	if !ok || eng == nil {
		t.Fatal("base engine")
	}
	defer hold()
	delta, ok := v.buildDeltaSnapshotFromCSR(context.Background(), state, cached, eng, storedLine)
	if !ok || delta == nil {
		t.Fatal("delta path unavailable")
	}
	if len(delta.docs) != 4 || delta.changeSeq != state.changeSeq {
		t.Fatalf("delta docs=%d changeSeq=%d want 4/%d", len(delta.docs), delta.changeSeq, state.changeSeq)
	}
	if delta.coreEngine == nil || !delta.coreEngine.HasDelta() {
		t.Fatal("delta engine must carry the one-unit overlay")
	}
	if _, err := db.Exec(`DELETE FROM rm_docs WHERE unit_id = 'doc-a'`); err != nil {
		t.Fatal(err)
	}
	state, ok = v.canonicalCorpusState(context.Background())
	if !ok {
		t.Fatal("canonical state after delete")
	}
	deleted, ok := v.buildDeltaSnapshotFromCSR(context.Background(), state, delta, eng, storedLine)
	if !ok || deleted == nil {
		t.Fatal("delete delta path unavailable")
	}
	if len(deleted.docs) != 3 {
		t.Fatalf("docs after delete = %d, want 3", len(deleted.docs))
	}
	if _, exists := deleted.docIdxByID["doc-a"]; exists {
		t.Fatal("doc-a still present in the docs index")
	}
	loaded, err := v.loadCorpusSnapshot(context.Background())
	if err != nil || loaded == nil {
		t.Fatalf("load snapshot: %v", err)
	}
	hits, err := v.SearchClientSDKDefault("Erin Berlin train", 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	found := false
	for _, hit := range hits {
		if hit.UnitID == "doc-d" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("new unit not searchable through the delta snapshot: %#v", hits)
	}
	if _, err := db.Exec(`DELETE FROM rm_change`); err != nil {
		t.Fatal(err)
	}
	state, ok = v.canonicalCorpusState(context.Background())
	if !ok {
		t.Fatal("canonical state after prune")
	}
	if _, ok := v.buildDeltaSnapshotFromCSR(context.Background(), state, deleted, eng, storedLine); ok {
		t.Fatal("a pruned change log must fall back to the full scan")
	}
}
