// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package localvault

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocareer/rolling-memory/internal/episodicunit"
	plain "github.com/rocareer/rolling-memory/sdk/go/pulse/plain"
	_ "modernc.org/sqlite"
)

func TestEpisodicSaveHonorsTombstones(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"user","message":{"role":"user","content":"tombstone fixture"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	db := migrationTestDB(t)
	v, err := NewVault(db, []byte("tombstone-fixture"))
	if err != nil {
		t.Fatal(err)
	}
	ref, text := "@ref:jsonl:"+path+":0#0", "[user] tombstone fixture"
	for _, tc := range []struct {
		id, layer string
		deleted   bool
	}{
		{"episodic", "episodic", true},
		{"all-layers", "", true},
		{"other-layer", "background", false},
		{"no-tombstone", "none", false},
	} {
		t.Run(tc.id, func(t *testing.T) {
			if tc.layer != "none" {
				if _, err := db.Exec(`INSERT INTO rm_deleted_units(unit_id, layer, deleted_at) VALUES (?, ?, 1)`, tc.id, tc.layer); err != nil {
					t.Fatal(err)
				}
			}
			pkt, _ := v.PackBlindPacket(tc.id, text, 1)
			err := v.SaveLocalEpisodicPointerIndexed(tc.id, 1, ref, "", text, pkt, IngestResponse{})
			if tc.deleted {
				if !errors.Is(err, ErrEpisodicUnitDeleted) {
					t.Fatalf("tombstoned unit error = %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM rm_units WHERE doc_id = ?`, tc.id).Scan(&count); err != nil || (count != 0) == tc.deleted {
				t.Fatalf("document count = %d, deleted=%t err=%v", count, tc.deleted, err)
			}
		})
	}
}

func TestEpisodicSaveFailsClosedWhenTombstoneUnavailable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"user","message":{"role":"user","content":"read failure"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	db := migrationTestDB(t)
	v, err := NewVault(db, []byte("tombstone-failure"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE rm_deleted_units`); err != nil {
		t.Fatal(err)
	}
	ref, text := "@ref:jsonl:"+path+":0#0", "[user] read failure"
	pkt, _ := v.PackBlindPacket("doc", text, 1)
	if err := v.SaveLocalEpisodicPointerIndexed("doc", 1, ref, "", text, pkt, IngestResponse{}); err == nil || errors.Is(err, ErrEpisodicUnitDeleted) {
		t.Fatalf("tombstone lookup failure = %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM rm_units WHERE doc_id = 'doc'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial write: %d %v", count, err)
	}
}

func TestEpisodicLegacyPointerHonorsTombstone(t *testing.T) {
	db := migrationTestDB(t)
	v, err := NewVault(db, []byte("legacy-tombstone"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO rm_deleted_units(unit_id, layer, deleted_at) VALUES ('doc', 'episodic', 1)`); err != nil {
		t.Fatal(err)
	}
	if err := v.SaveLocalEpisodicPointer("doc", 1, "@ref:jsonl:/synthetic:0#0", "@ref:jsonl:/synthetic:0", IngestResponse{}); !errors.Is(err, ErrEpisodicUnitDeleted) {
		t.Fatalf("legacy pointer error = %v", err)
	}
	var docs, lineage int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM rm_units WHERE doc_id='doc'), (SELECT count(*) FROM rm_episodic_lineage WHERE doc_id='doc')`).Scan(&docs, &lineage); err != nil || docs != 0 || lineage != 0 {
		t.Fatalf("legacy partial write docs=%d lineage=%d err=%v", docs, lineage, err)
	}
}

func TestEpisodicClientFallbackHonorsTombstones(t *testing.T) {
	db := migrationTestDB(t)
	v, err := NewVault(db, []byte("tombstone-fallback"))
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(v, nil)
	missing := "@ref:jsonl:" + filepath.Join(t.TempDir(), "missing.jsonl") + ":0#0"
	for _, tc := range []struct{ id, ref string }{{"inline", ""}, {"missing", missing}} {
		if _, err := db.Exec(`INSERT INTO rm_deleted_units(unit_id, layer, deleted_at) VALUES (?, 'episodic', 1)`, tc.id); err != nil {
			t.Fatal(err)
		}
		if _, err := client.RememberEpisodicByRef(context.Background(), tc.id, tc.ref, "", "[user] fallback", 1); !errors.Is(err, ErrEpisodicUnitDeleted) {
			t.Fatalf("%s fallback error = %v", tc.id, err)
		}
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM rm_units WHERE doc_id = ?`, tc.id).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s fallback wrote %d rows: %v", tc.id, count, err)
		}
	}
}

func TestEpisodicClientVerifiedPointerHonorsTombstone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"user","message":{"role":"user","content":"verified"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	db := migrationTestDB(t)
	v, err := NewVault(db, []byte("verified-tombstone"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO rm_deleted_units(unit_id, layer, deleted_at) VALUES ('doc', 'episodic', 1)`); err != nil {
		t.Fatal(err)
	}
	client := NewClient(v, nil)
	ref := "@ref:jsonl:" + path + ":0#0"
	if _, err := client.RememberEpisodicByRef(context.Background(), "doc", ref, "", "[user] verified", 1); !errors.Is(err, ErrEpisodicUnitDeleted) {
		t.Fatalf("verified pointer tombstone error = %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM rm_units WHERE doc_id = 'doc'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("verified pointer wrote %d rows: %v", count, err)
	}
}

func TestEpisodicClientSourceFailureDoesNotMaskDatabaseError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"user","message":{"role":"user","content":"source"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	db := migrationTestDB(t)
	v, err := NewVault(db, []byte("source-db-error"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE rm_deleted_units`); err != nil {
		t.Fatal(err)
	}
	client := NewClient(v, nil)
	ref := "@ref:jsonl:" + path + ":0#0"
	if _, err := client.RememberEpisodicByRef(context.Background(), "doc", ref, "", "[user] source", 1); err == nil || errors.Is(err, ErrEpisodicUnitDeleted) {
		t.Fatalf("database lookup failure = %v", err)
	}
}

func TestEpisodicClientFallbackFailsClosedOnTombstoneError(t *testing.T) {
	db := migrationTestDB(t)
	v, err := NewVault(db, []byte("fallback-tombstone-error"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE rm_deleted_units`); err != nil {
		t.Fatal(err)
	}
	client := NewClient(v, nil)
	missing := "@ref:jsonl:" + filepath.Join(t.TempDir(), "missing.jsonl") + ":0#0"
	for _, tc := range []struct{ id, ref string }{{"inline", ""}, {"missing", missing}} {
		if _, err := client.RememberEpisodicByRef(context.Background(), tc.id, tc.ref, "", "[user] fallback", 1); err == nil || errors.Is(err, ErrEpisodicUnitDeleted) {
			t.Fatalf("%s tombstone lookup failure = %v", tc.id, err)
		}
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM rm_units WHERE doc_id = ?`, tc.id).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s fallback wrote %d rows: %v", tc.id, count, err)
		}
	}
}

func TestEpisodicBackfillAndLineageHonorTombstone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"user","message":{"role":"user","content":"backfill"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	db := migrationTestDB(t)
	v, err := NewVault(db, []byte("backfill-tombstone"))
	if err != nil {
		t.Fatal(err)
	}
	text := "[user] backfill"
	id := episodicunit.DocID("mem:backfill-tombstone", text)
	turn := "@ref:jsonl:" + path + ":0"
	ref := turn + "#0"
	if err := v.SaveLocalEpisodicPointer(id, 1, ref, "", IngestResponse{}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO rm_deleted_units(unit_id, layer, deleted_at) VALUES (?, 'episodic', 1)`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := v.EnsureLocalEpisodicIndex("mem:backfill-tombstone", id, ref, text); !errors.Is(err, ErrEpisodicUnitDeleted) {
		t.Fatalf("backfill error = %v", err)
	}
	if err := v.RecordEpisodicLineage(turn, id, 1); !errors.Is(err, ErrEpisodicUnitDeleted) {
		t.Fatalf("lineage error = %v", err)
	}
	var indexes, lineage int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM rm_episodic_index WHERE doc_id = ?), (SELECT count(*) FROM rm_episodic_lineage WHERE doc_id = ?)`, id, id).Scan(&indexes, &lineage); err != nil || indexes != 0 || lineage != 0 {
		t.Fatalf("tombstoned backfill wrote index=%d lineage=%d err=%v", indexes, lineage, err)
	}
	if _, err := db.Exec(`DELETE FROM rm_deleted_units WHERE unit_id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if err := v.RecordEpisodicLineage(turn, id, 1); err != nil {
		t.Fatalf("live document lineage: %v", err)
	}
	if err := v.RecordEpisodicLineage(turn, "missing", 1); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM rm_episodic_lineage WHERE doc_id = 'missing'`).Scan(&lineage); err != nil || lineage != 0 {
		t.Fatalf("orphan lineage = %d %v", lineage, err)
	}
}

func TestEpisodicLineageAndDeleteSerialize(t *testing.T) {
	db := migrationTestDB(t)
	v, err := NewVault(db, []byte("lineage-delete"))
	if err != nil {
		t.Fatal(err)
	}
	if err := v.SaveLocalPlaintext("doc", 1, "fixture", []string{"fixture"}, IngestResponse{Layer: LayerEpisodic}); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`DELETE FROM rm_units WHERE doc_id='doc'`,
		`DELETE FROM rm_episodic_lineage WHERE doc_id='doc'`,
		`INSERT INTO rm_deleted_units(unit_id, layer, deleted_at) VALUES ('doc', 'episodic', 1)`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		close(started)
		result <- v.RecordEpisodicLineage("@ref:jsonl:/fixture:0", "doc", 1)
	}()
	<-started
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, ErrEpisodicUnitDeleted) {
		t.Fatalf("lineage after delete: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM rm_episodic_lineage WHERE doc_id='doc'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("orphan lineage count=%d err=%v", count, err)
	}
	if _, err := db.Exec(`DELETE FROM rm_deleted_units WHERE unit_id='doc'`); err != nil {
		t.Fatal(err)
	}
	if err := v.SaveLocalPlaintext("doc", 1, "fixture", []string{"fixture"}, IngestResponse{Layer: LayerEpisodic}); err != nil {
		t.Fatal(err)
	}
	if err := v.RecordEpisodicLineage("@ref:jsonl:/fixture:0", "doc", 1); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM rm_episodic_lineage WHERE doc_id='doc'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("restored lineage count=%d err=%v", count, err)
	}
}

func TestEpisodicTombstoneSeparatesScopes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"user","message":{"role":"user","content":"scoped"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	db := migrationTestDB(t)
	v, err := NewVault(db, []byte("scope-tombstone"))
	if err != nil {
		t.Fatal(err)
	}
	text := "[user] scoped"
	deletedID, otherID := episodicunit.DocID("mem:one", text), episodicunit.DocID("mem:two", text)
	if _, err := db.Exec(`INSERT INTO rm_deleted_units(unit_id, layer, deleted_at) VALUES (?, 'episodic', 1)`, deletedID); err != nil {
		t.Fatal(err)
	}
	ref := "@ref:jsonl:" + path + ":0#0"
	for _, tc := range []struct {
		id      string
		deleted bool
	}{{deletedID, true}, {otherID, false}} {
		pkt, _ := v.PackBlindPacket(tc.id, text, 1)
		err := v.SaveLocalEpisodicPointerIndexed(tc.id, 1, ref, "", text, pkt, IngestResponse{})
		if tc.deleted && !errors.Is(err, ErrEpisodicUnitDeleted) || !tc.deleted && err != nil {
			t.Fatalf("scope id %s: %v", tc.id, err)
		}
	}
}

func TestEpisodicPlaintextTombstoneLayerIsolation(t *testing.T) {
	db := migrationTestDB(t)
	v, err := NewVault(db, []byte("plaintext-layer-tombstone"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO rm_deleted_units(unit_id, layer, deleted_at) VALUES ('doc', 'episodic', 1)`); err != nil {
		t.Fatal(err)
	}
	if err := v.SaveLocalPlaintext("doc", 1, "background", []string{"background"}, IngestResponse{Layer: LayerBackground}); err != nil {
		t.Fatalf("episodic tombstone blocked background: %v", err)
	}
	if err := v.SaveLocalPlaintext("doc", 1, "episodic", []string{"episodic"}, IngestResponse{Layer: LayerEpisodic}); !errors.Is(err, ErrEpisodicUnitDeleted) {
		t.Fatalf("episodic tombstone error = %v", err)
	}
	var raw string
	if err := db.QueryRow(`SELECT raw_text FROM rm_units WHERE doc_id = 'doc'`).Scan(&raw); err != nil || raw != "background" {
		t.Fatalf("background overwritten: %q %v", raw, err)
	}
}

func TestEpisodicDeleteAndSaveSerialize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"user","message":{"role":"user","content":"serialization"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	db := migrationTestDB(t)
	v, err := NewVault(db, []byte("serialize-tombstone"))
	if err != nil {
		t.Fatal(err)
	}
	ref, text := "@ref:jsonl:"+path+":0#0", "[user] serialization"
	id := episodicunit.DocID("mem:serialize", text)
	pkt, _ := v.PackBlindPacket(id, text, 1)
	if err := v.SaveLocalEpisodicPointerIndexed(id, 1, ref, "", text, pkt, IngestResponse{}); err != nil {
		t.Fatal(err)
	}
	deleteTx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deleteTx.Exec(`DELETE FROM rm_units WHERE doc_id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := deleteTx.Exec(`INSERT INTO rm_deleted_units(unit_id, layer, deleted_at) VALUES (?, 'episodic', 1)`, id); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		close(started)
		result <- v.SaveLocalEpisodicPointerIndexed(id, 1, ref, "", text, pkt, IngestResponse{})
	}()
	<-started
	if err := deleteTx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, ErrEpisodicUnitDeleted) {
		t.Fatalf("save after deletion = %v", err)
	}
	var docs, indexes int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM rm_units WHERE doc_id = ?), (SELECT count(*) FROM rm_episodic_index WHERE doc_id = ?)`, id, id).Scan(&docs, &indexes); err != nil || docs != 0 || indexes != 0 {
		t.Fatalf("deleted unit revived: docs=%d indexes=%d err=%v", docs, indexes, err)
	}
	if _, err := db.Exec(`DELETE FROM rm_deleted_units WHERE unit_id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if err := v.SaveLocalEpisodicPointerIndexed(id, 1, ref, "", text, pkt, IngestResponse{}); err != nil {
		t.Fatalf("save after explicit tombstone removal: %v", err)
	}
	deleteTx, err = db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deleteTx.Exec(`DELETE FROM rm_units WHERE doc_id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := deleteTx.Exec(`INSERT INTO rm_deleted_units(unit_id, layer, deleted_at) VALUES (?, 'episodic', 2)`, id); err != nil {
		t.Fatal(err)
	}
	if err := deleteTx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM rm_units WHERE doc_id = ?), (SELECT count(*) FROM rm_episodic_index WHERE doc_id = ?)`, id, id).Scan(&docs, &indexes); err != nil || docs != 0 || indexes != 0 {
		t.Fatalf("delete after save: docs=%d indexes=%d err=%v", docs, indexes, err)
	}
}

func TestEpisodicOccurrenceAndInsertionTimes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"user","message":{"role":"user","content":"time fixture"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	db := migrationTestDB(t)
	v, err := NewVault(db, []byte("episodic-time"))
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(v, nil)
	ref := "@ref:jsonl:" + path + ":0#0"
	text := "[user] time fixture"
	before := time.Now().Unix()
	for _, tc := range []struct {
		id       string
		ref      string
		text     string
		occurred int64
	}{
		{id: "indexed-unknown", ref: ref, text: text},
		{id: "indexed-known", ref: ref, text: text, occurred: 1700000000},
		{id: "indexed-negative", ref: ref, text: text, occurred: -1},
		{id: "inline-unknown", text: "[user] inline fixture"},
		{id: "inline-known", text: "[user] known inline", occurred: 1700000000},
		{id: "invalid-unknown", ref: "@ref:jsonl:" + filepath.Join(dir, "missing.jsonl") + ":0#0", text: "[user] missing fixture"},
	} {
		if _, err := client.RememberEpisodicByRef(context.Background(), tc.id, tc.ref, "", tc.text, tc.occurred); err != nil {
			t.Fatalf("%s: %v", tc.id, err)
		}
		var occurred, inserted int64
		if err := db.QueryRow(`SELECT occurred_at, inserted_at FROM rm_units WHERE doc_id = ?`, tc.id).Scan(&occurred, &inserted); err != nil {
			t.Fatal(err)
		}
		want := tc.occurred
		if want < 0 {
			want = 0
		}
		if occurred != want || inserted < before || inserted > time.Now().Unix() {
			t.Fatalf("%s occurred=%d inserted=%d, want event=%d and current insertion", tc.id, occurred, inserted, want)
		}
	}
	var firstInserted int64
	if err := db.QueryRow(`SELECT inserted_at FROM rm_units WHERE doc_id = 'indexed-unknown'`).Scan(&firstInserted); err != nil {
		t.Fatal(err)
	}
	if _, err := client.RememberEpisodicByRef(context.Background(), "indexed-unknown", ref, "", text, 1700000001); err != nil {
		t.Fatal(err)
	}
	var occurred, inserted int64
	if err := db.QueryRow(`SELECT occurred_at, inserted_at FROM rm_units WHERE doc_id = 'indexed-unknown'`).Scan(&occurred, &inserted); err != nil {
		t.Fatal(err)
	}
	if occurred != 1700000001 || inserted != firstInserted {
		t.Fatalf("known time correction: occurred=%d inserted=%d, want %d/%d", occurred, inserted, 1700000001, firstInserted)
	}
	if _, err := client.RememberEpisodicByRef(context.Background(), "indexed-unknown", ref, "", text, 0); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT occurred_at, inserted_at FROM rm_units WHERE doc_id = 'indexed-unknown'`).Scan(&occurred, &inserted); err != nil {
		t.Fatal(err)
	}
	if occurred != 1700000001 || inserted != firstInserted {
		t.Fatalf("unknown time overwrote known time: occurred=%d inserted=%d", occurred, inserted)
	}
	for _, tc := range []struct{ id, ref, text string }{
		{"inline-known", "", "[user] known inline"},
		{"invalid-unknown", "@ref:jsonl:" + filepath.Join(dir, "missing.jsonl") + ":0#0", "[user] missing fixture"},
	} {
		if _, err := client.RememberEpisodicByRef(context.Background(), tc.id, tc.ref, "", tc.text, 1700000002); err != nil {
			t.Fatal(err)
		}
		if _, err := client.RememberEpisodicByRef(context.Background(), tc.id, tc.ref, "", tc.text, 0); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT occurred_at FROM rm_units WHERE doc_id = ?`, tc.id).Scan(&occurred); err != nil || occurred != 1700000002 {
			t.Fatalf("%s known event lost: %d %v", tc.id, occurred, err)
		}
	}
	pkt, _ := v.PackBlindPacket("query", "time fixture", 0)
	if pkt.OccurredAt != 0 {
		t.Fatalf("encoder replaced unknown time: %d", pkt.OccurredAt)
	}
	hits, err := v.SearchClientSDKDefault("time fixture", 10)
	if err != nil {
		t.Fatal(err)
	}
	foundUnknown := false
	for _, hit := range hits {
		if hit.UnitID == "indexed-negative" {
			foundUnknown = true
			if hit.OccurredAt != 0 {
				t.Fatalf("query replaced unknown time: %+v", hit)
			}
		}
	}
	if !foundUnknown {
		t.Fatal("unknown-time document was not searchable")
	}
	if times := v.DocTimes([]string{"indexed-negative", "indexed-known"}); times["indexed-negative"] != 0 || times["indexed-known"] != 1700000000 {
		t.Fatalf("document times: %+v", times)
	}
}

func TestEpisodicPointerIndexJSONL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	body := `{"type":"user","message":{"role":"user","content":"quartz nebula lighthouse"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", vaultTestDSN(filepath.Join(dir, "vault.sqlite")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, err := NewVault(db, []byte("episodic-index-test"))
	if err != nil {
		t.Fatal(err)
	}
	ref := "@ref:jsonl:" + path + ":0#0"
	live, err := ResolveEpisodicPointerLive(ref)
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(v, nil)
	if _, err := c.RememberEpisodicByRef(context.Background(), "doc", ref, "", live, 1); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := db.QueryRow(`SELECT raw_text FROM rm_units WHERE doc_id='doc'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != ref || strings.Contains(stored, "quartz") {
		t.Fatalf("unexpected stored text %q", stored)
	}
	assertHits := func(query string, want bool) {
		t.Helper()
		hits, err := v.SearchClientSDKDefault(query, 10)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, hit := range hits {
			found = found || hit.UnitID == "doc"
		}
		if found != want {
			t.Errorf("default query %q: got %+v", query, hits)
		}
		pkt, _ := v.PackBlindPacket("query", query, 0)
		hits, err = NewLocalVaultTransport(v).QueryBlindManifold(context.Background(), pkt, 10)
		if err != nil {
			t.Fatal(err)
		}
		found = false
		for _, hit := range hits {
			found = found || hit.UnitID == "doc"
		}
		if found != want {
			t.Errorf("transport query %q: got %+v", query, hits)
		}
	}
	assertHits("quartz nebula", true)
	assertHits("unrelatedviolet", false)
	var payload []byte
	if err := db.QueryRow(`SELECT payload FROM rm_episodic_index WHERE doc_id='doc'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payload, []byte("quartz")) {
		t.Fatal("index contains plaintext")
	}
	slots, frames, err := plain.DecodeWireBytes(payload)
	if err != nil {
		t.Fatal(err)
	}
	expected, _ := v.PackBlindPacket("doc", live, 1)
	if !reflect.DeepEqual(slots, expected.Slots) || !reflect.DeepEqual(frames, expected.SentenceFrames) {
		t.Fatal("canonical packet did not round trip")
	}
}

func TestEpisodicIndexEnsureAndCrossInstance(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.sqlite")
	db1, err := sql.Open("sqlite", vaultTestDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db1.Close()
	db2, err := sql.Open("sqlite", vaultTestDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	v1, err := NewVault(db1, []byte("shared-key"))
	if err != nil {
		t.Fatal(err)
	}
	v2, err := NewVault(db2, []byte("shared-key"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "source.jsonl")
	ref := "@ref:jsonl:" + source + ":0#0"
	if _, err := v2.SearchClientSDKDefault("quartz", 10); err != nil {
		t.Fatal(err)
	}
	for _, word := range []string{"quartz", "nebula", "marble", "violet"} {
		if _, err := db1.Exec(`DELETE FROM rm_units WHERE doc_id='doc'`); err != nil {
			t.Fatal(err)
		}
		if err := v1.deleteCanonicalUnits("doc"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(source, []byte(`{"type":"user","message":{"role":"user","content":"`+word+`"}}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		text, err := ResolveEpisodicPointerLive(ref)
		if err != nil {
			t.Fatal(err)
		}
		pkt, _ := v1.PackBlindPacket("doc", text, 1)
		if err := v1.SaveLocalEpisodicPointerIndexed("doc", 1, ref, "", text, pkt, IngestResponse{}); err != nil {
			t.Fatal(err)
		}
		hits, err := v2.SearchClientSDKDefault(text, 10)
		if err != nil || len(hits) != 1 || hits[0].UnitID != "doc" {
			t.Fatalf("cross instance %q: %+v %v", text, hits, err)
		}
	}
	if _, err := db1.Exec(`DELETE FROM rm_units WHERE doc_id='doc'`); err != nil {
		t.Fatal(err)
	}
	if err := v1.deleteCanonicalUnits("doc"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte(`{"type":"user","message":{"role":"user","content":"quartz"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	text, err := ResolveEpisodicPointerLive(ref)
	if err != nil {
		t.Fatal(err)
	}
	const scope = "ensure-test"
	docID := episodicunit.DocID(scope, text)
	if err := v1.SaveLocalEpisodicPointer(docID, 1, ref, "", IngestResponse{}); err != nil {
		t.Fatal(err)
	}
	created, err := v1.EnsureLocalEpisodicIndex(scope, docID, ref, text)
	if err != nil || !created {
		t.Fatalf("ensure: %v %v", created, err)
	}
	created, err = v1.EnsureLocalEpisodicIndex(scope, docID, ref, text)
	if err != nil || created {
		t.Fatalf("idempotent ensure: %v %v", created, err)
	}
	if _, err = v1.EnsureLocalEpisodicIndex(scope, docID, ref+"x", text); err == nil {
		t.Fatal("changed pointer accepted")
	}
	hits, err := v2.SearchClientSDKDefault("quartz", 10)
	if err != nil || len(hits) != 1 {
		t.Fatalf("cross-instance ensure: %+v %v", hits, err)
	}
	other, err := NewVault(db2, []byte("different-key"))
	if err != nil {
		t.Fatal(err)
	}
	compatible, incompatible, missing, err := other.EpisodicIndexDiagnostics()
	if err != nil || compatible != 0 || incompatible != 1 || missing != 0 {
		t.Fatalf("diagnostics %d %d %d %v", compatible, incompatible, missing, err)
	}
	if hits, err := other.SearchClientSDKDefault("quartz", 10); err != nil || len(hits) != 0 {
		t.Fatalf("incompatible index: %+v %v", hits, err)
	}
	// Cross-generation union read: a vault opened with a different encoder
	// configuration but the SAME secret must still read rows indexed by an
	// earlier codec generation — an SDK or language-pack bump must never orphan
	// previously indexed speech (payload is self-describing, MAC-authenticated
	// per row, and the current generation is merely preferred over older ones).
	// The secret remains the access boundary: the different-key vault above
	// still sees nothing.
	clone := v1.CloneWithSharedSlotCache()
	clone.SetCanonicalHarmonics(plain.CanonicalHarmonicOptions{EnableStructuredDates: true})
	if hits, err := clone.SearchClientSDKDefault("quartz", 10); err != nil || len(hits) != 1 {
		t.Fatalf("clone config union read: %+v %v", hits, err)
	}
	if hits, err := v1.SearchClientSDKDefault("quartz", 10); err != nil || len(hits) != 1 {
		t.Fatalf("original config: %+v %v", hits, err)
	}
}

func TestEpisodicIndexContentIdentity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.jsonl")
	writeSource := func(word string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(`{"type":"user","message":{"role":"user","content":"`+word+`"}}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeSource("quartz")
	ref := "@ref:jsonl:" + path + ":0#0"
	db, err := sql.Open("sqlite", vaultTestDSN(filepath.Join(dir, "vault.sqlite")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, err := NewVault(db, []byte("identity-key"))
	if err != nil {
		t.Fatal(err)
	}
	original, err := ResolveEpisodicPointerLive(ref)
	if err != nil {
		t.Fatal(err)
	}
	pkt, _ := v.PackBlindPacket("doc", original, 1)
	if err := v.SaveLocalEpisodicPointerIndexed("doc", 1, ref, "", original, pkt, IngestResponse{}); err != nil {
		t.Fatal(err)
	}
	var digest, payload []byte
	if err := db.QueryRow(`SELECT content_digest, payload FROM rm_episodic_index WHERE doc_id='doc'`).Scan(&digest, &payload); err != nil {
		t.Fatal(err)
	}
	if len(digest) == 0 || bytes.Contains(digest, []byte(original)) {
		t.Fatal("digest is missing or contains source text")
	}
	if got, _, _, err := v.LoadLocalDocument("doc"); err != nil || got != original {
		t.Fatalf("initial hydration %q: %v", got, err)
	}
	writeSource("nebula")
	if got, _, _, err := v.LoadLocalDocument("doc"); err == nil || got != "" {
		t.Fatalf("changed source delivered %q: %v", got, err)
	}
	changed, err := ResolveEpisodicPointerLive(ref)
	if err != nil {
		t.Fatal(err)
	}
	changedPkt, _ := v.PackBlindPacket("doc", changed, 1)
	if err := v.SaveLocalEpisodicPointerIndexed("doc", 1, ref, "", changed, changedPkt, IngestResponse{}); err == nil {
		t.Fatal("changed source overwrote established identity")
	}
	if _, err := v.EnsureLocalEpisodicIndex("identity-test", "doc", ref, changed); err == nil {
		t.Fatal("backfill overwrote established identity")
	}
	wrongPkt, _ := v.PackBlindPacket("new", original, 1)
	if err := v.SaveLocalEpisodicPointerIndexed("new", 1, ref, "", original, wrongPkt, IngestResponse{}); err == nil {
		t.Fatal("mismatched supplied text accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got, _, _, err := v.LoadLocalDocument("doc"); err == nil || got != "" {
		t.Fatalf("missing source delivered %q: %v", got, err)
	}
	if _, err := v.EnsureLocalEpisodicIndex("identity-test", episodicunit.DocID("identity-test", ref), ref, ref); err == nil {
		t.Fatal("unresolved pointer encoded as source")
	}
	var afterDigest, afterPayload []byte
	if err := db.QueryRow(`SELECT content_digest, payload FROM rm_episodic_index WHERE doc_id='doc'`).Scan(&afterDigest, &afterPayload); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(digest, afterDigest) || !bytes.Equal(payload, afterPayload) {
		t.Fatal("failed operation changed existing index")
	}
}

func TestEpisodicEnsureAndRecentIdentity(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.jsonl")
	writeSource := func(text string) {
		t.Helper()
		body := `{"type":"user","message":{"role":"user","content":"` + text + `"}}` + "\n"
		if err := os.WriteFile(source, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeSource("原文 quartz")
	ref := "@ref:jsonl:" + source + ":0#0"
	original, err := ResolveEpisodicPointerLive(ref)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", vaultTestDSN(filepath.Join(dir, "vault.sqlite")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	v, err := NewVault(db, []byte("recent-identity-key"))
	if err != nil {
		t.Fatal(err)
	}
	const scope = "mem:recent-identity"
	id := episodicunit.DocID(scope, original)
	if err := v.SaveLocalEpisodicPointer(id, 1, ref, "", IngestResponse{}); err != nil {
		t.Fatal(err)
	}
	checkRecent := func(want string) {
		t.Helper()
		entries, err := v.RecentDocs(10)
		if err != nil || len(entries) != 1 || entries[0].ID != id || entries[0].Text != want {
			t.Fatalf("RecentDocs: %+v %v, want %q", entries, err, want)
		}
		entry, err := v.RecentSourceDoc(10)
		if err != nil || entry == nil || entry.ID != id || entry.Text != want {
			t.Fatalf("RecentSourceDoc: %+v %v, want %q", entry, err, want)
		}
	}
	// Gap delivery must not expose either the source pointer/path or its content.
	gap := "[来源缺口：指针身份未验证或内容不可用]"
	checkRecent(gap)
	for _, wrongScope := range []string{"", "mem:other"} {
		if _, err := v.EnsureLocalEpisodicIndex(wrongScope, id, ref, original); err == nil {
			t.Fatalf("scope %q accepted", wrongScope)
		}
	}
	writeSource("changed nebula")
	changed, err := ResolveEpisodicPointerLive(ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.EnsureLocalEpisodicIndex(scope, id, ref, changed); err == nil {
		t.Fatal("changed source indexed under old ID")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM rm_episodic_index WHERE doc_id = ?`, id).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed ensure wrote index: %d %v", count, err)
	}
	checkRecent(gap)
	writeSource("原文 quartz")
	if created, err := v.EnsureLocalEpisodicIndex(scope, id, ref, original); err != nil || !created {
		t.Fatalf("ensure original: %v %v", created, err)
	}
	checkRecent(original)
	writeSource("changed nebula")
	checkRecent(gap)
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	checkRecent(gap)
	var pointer string
	if err := db.QueryRow(`SELECT raw_text FROM rm_units WHERE doc_id = ?`, id).Scan(&pointer); err != nil || pointer != ref {
		t.Fatalf("legacy source row changed: %q %v", pointer, err)
	}
}

func TestEpisodicIndexAtomicMismatch(t *testing.T) {
	db, err := sql.Open("sqlite", vaultTestDSN(filepath.Join(t.TempDir(), "vault.sqlite")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, err := NewVault(db, []byte("key"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "source.jsonl")
	if err := os.WriteFile(source, []byte(`{"type":"user","message":{"role":"user","content":"quartz"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ref := "@ref:jsonl:" + source + ":0#0"
	text, err := ResolveEpisodicPointerLive(ref)
	if err != nil {
		t.Fatal(err)
	}
	pkt, _ := v.PackBlindPacket("doc", "different", 1)
	if err := v.SaveLocalEpisodicPointerIndexed("doc", 1, ref, "", text, pkt, IngestResponse{}); err == nil {
		t.Fatal("mismatched packet accepted")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM rm_units`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial write: %d %v", count, err)
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_index BEFORE INSERT ON rm_episodic_index BEGIN SELECT RAISE(ABORT, 'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	pkt, _ = v.PackBlindPacket("doc", text, 1)
	if err := v.SaveLocalEpisodicPointerIndexed("doc", 1, ref, "", text, pkt, IngestResponse{}); err == nil {
		t.Fatal("write failure ignored")
	}
	if err := db.QueryRow(`SELECT count(*) FROM rm_units`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial write: %d %v", count, err)
	}
}

// TestEpisodicPointerRepointSameContent keeps distinct verified occurrences while
// retaining the primary pointer when it still resolves to the indexed content.
func TestEpisodicPointerRepointSameContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	body := `{"type":"user","message":{"role":"user","content":"开始。"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":"同一句。"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":"同一句。"}}` + "\n" +
		`{"type":"user","message":{"role":"user","content":"另一轮。"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":"同一句。"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", vaultTestDSN(filepath.Join(dir, "vault.sqlite")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, err := NewVault(db, []byte("repoint-test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(v, nil)
	turnRef := "@ref:jsonl:" + path + ":0"
	ref1 := turnRef + "#1"
	ref2 := turnRef + "#2"
	text, err := ResolveEpisodicPointerLive(ref1)
	if err != nil {
		t.Fatal(err)
	}
	if text != "[assistant] 同一句。" {
		t.Fatalf("unexpected resolved text %q", text)
	}
	if _, err := client.RememberEpisodicByRef(context.Background(), "doc", ref1, turnRef, text, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := client.RememberEpisodicByRef(context.Background(), "doc", ref2, turnRef, text, 1); err != nil {
		t.Fatalf("re-point same content: %v", err)
	}
	var storedRaw, storedRef string
	if err := db.QueryRow(`SELECT raw_text FROM rm_units WHERE doc_id='doc'`).Scan(&storedRaw); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT ref_pointer FROM rm_episodic_index WHERE doc_id='doc'`).Scan(&storedRef); err != nil {
		t.Fatal(err)
	}
	if storedRaw != ref1 || storedRef != ref1 {
		t.Fatalf("primary pointer changed despite a live source: doc=%q index=%q", storedRaw, storedRef)
	}
	for _, ref := range []string{ref1, ref2} {
		if got, err := v.LoadVerifiedEpisodicPointer("doc", ref); err != nil || got != text {
			t.Fatalf("verified pointer %s: %q %v", ref, got, err)
		}
	}
	if _, err := client.RememberEpisodicByRef(context.Background(), "doc2", ref1, turnRef, text, 1); err != nil {
		t.Fatal(err)
	}
	refOtherTurn := "@ref:jsonl:" + path + ":1#1"
	if _, err := client.RememberEpisodicByRef(context.Background(), "doc2", refOtherTurn, "@ref:jsonl:"+path+":1", text, 2); err != nil {
		t.Fatalf("verified cross-turn source: %v", err)
	}
}

// TestEpisodicPointerRelocatesByContentDigest 锁定「定位下标漂移后按内容身份重定位」：
// 存储的 `#<k>` 是位置量，会随分段过滤演化或宿主追加步骤而平移；docID 与记录摘要都
// 绑定同一条正文，重定位只允许在同一轮内命中摘要完全一致的单元，找不到就 fail-closed。
func TestEpisodicPointerRelocatesByContentDigest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	original := `{"type":"user","message":{"role":"user","content":"开始。"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":"回答一。"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":"回答二。"}}` + "\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", vaultTestDSN(filepath.Join(dir, "vault.sqlite")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, err := NewVault(db, []byte("digest-relocate-secret"))
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(v, nil)
	turnRef := "@ref:jsonl:" + path + ":0"
	keptRef := turnRef + "#2"
	keptText, err := ResolveEpisodicPointerLive(keptRef)
	if err != nil || keptText != "[assistant] 回答二。" {
		t.Fatalf("kept text = %q err=%v", keptText, err)
	}
	droppedRef := turnRef + "#1"
	droppedText, err := ResolveEpisodicPointerLive(droppedRef)
	if err != nil || droppedText != "[assistant] 回答一。" {
		t.Fatalf("dropped text = %q err=%v", droppedText, err)
	}
	for id, ref := range map[string]string{"kept": keptRef, "dropped": droppedRef} {
		text := keptText
		if id == "dropped" {
			text = droppedText
		}
		if _, err := client.RememberEpisodicByRef(context.Background(), id, ref, turnRef, text, 1); err != nil {
			t.Fatal(err)
		}
	}
	// 分段过滤演化：同一轮里前一条旁白不再枚举，`#2` 越界、`#1` 指向他文本。
	if err := os.WriteFile(path, []byte(`{"type":"user","message":{"role":"user","content":"开始。"}}`+"\n"+
		`{"type":"assistant","message":{"role":"assistant","content":"回答二。"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, actualRef, err := v.LoadVerifiedEpisodicPointerSource("kept", keptRef); err != nil || got != keptText || actualRef != droppedRef {
		t.Fatalf("relocated pointer = %q ref=%q err=%v, want %q at %q", got, actualRef, err, keptText, droppedRef)
	}
	if got, err := v.LoadVerifiedEpisodicPointer("kept", keptRef); err == nil || got != "" {
		t.Fatalf("legacy reader attributed relocated content to stale position: %q %v", got, err)
	}
	pkt, _ := v.PackBlindPacket("kept", keptText, 1)
	if err := v.SaveLocalEpisodicPointerIndexed("kept", 1, droppedRef, turnRef, keptText, pkt, IngestResponse{}); err != nil {
		t.Fatalf("record relocated source: %v", err)
	}
	if got, err := v.LoadVerifiedEpisodicPointer("kept", droppedRef); err != nil || got != keptText {
		t.Fatalf("repointed source: %q %v", got, err)
	}
	if _, err := v.LoadVerifiedEpisodicPointer("dropped", droppedRef); err == nil {
		t.Fatal("pointer without a content match must fail closed")
	}
}

func TestZCodePointerReflectsSourceEdits(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".zcode", "cli", "db", "db.sqlite")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	source, err := sql.Open("sqlite", vaultTestDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	for _, statement := range []string{
		`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT, sequence INTEGER, time_created INTEGER, data TEXT)`,
		`CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT, session_id TEXT, sequence INTEGER, data TEXT)`,
		`INSERT INTO message VALUES ('m1', 's1', 1, 1, '{"role":"user"}')`,
		`INSERT INTO part VALUES ('p1', 'm1', 's1', 1, '{"type":"text","text":"alpha"}')`,
	} {
		if _, err := source.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	db := migrationTestDB(t)
	v, err := NewVault(db, []byte("zcode-source-edit"))
	if err != nil {
		t.Fatal(err)
	}
	turn := "@ref:zcode:s1:m1"
	ref := turn + "#0"
	oldText := "[user] alpha"
	const scope = "mem:source-edit"
	oldID := episodicunit.DocID(scope, oldText)
	pkt, _ := v.PackBlindPacket(oldID, oldText, 1)
	if err := v.SaveLocalEpisodicPointerIndexed(oldID, 1, ref, turn, oldText, pkt, IngestResponse{}); err != nil {
		t.Fatal(err)
	}
	if got, err := v.LoadVerifiedEpisodicPointer(oldID, ref); err != nil || got != oldText {
		t.Fatalf("initial source: %q %v", got, err)
	}
	if _, err := source.Exec(`UPDATE part SET data = '{"type":"text","text":"bravo"}' WHERE id = 'p1'`); err != nil {
		t.Fatal(err)
	}
	newText := "[user] bravo"
	if got, err := ResolveEpisodicPointerLive(ref); err != nil || got != newText {
		t.Fatalf("updated source: %q %v, want %q", got, err, newText)
	}
	if got, err := v.LoadVerifiedEpisodicPointer(oldID, ref); err == nil || got != "" {
		t.Fatalf("changed source accepted under old identity: %q %v", got, err)
	}
	newID := episodicunit.DocID(scope, newText)
	pkt, _ = v.PackBlindPacket(newID, newText, 2)
	if err := v.SaveLocalEpisodicPointerIndexed(newID, 2, ref, turn, newText, pkt, IngestResponse{}); err != nil {
		t.Fatalf("import updated source: %v", err)
	}
	if got, err := v.LoadVerifiedEpisodicPointer(newID, ref); err != nil || got != newText {
		t.Fatalf("updated identity: %q %v", got, err)
	}
	if _, err := episodicPointerTurnSegments(ref); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec(`UPDATE part SET data = '{"type":"text","text":"charl"}' WHERE id = 'p1'`); err != nil {
		t.Fatal(err)
	}
	segments, err := episodicPointerTurnSegments(ref)
	if err != nil || len(segments) != 1 || segments[0].Text != "charl" {
		t.Fatalf("full turn after edit: %+v %v", segments, err)
	}
	if got, err := v.LoadVerifiedEpisodicPointer(newID, ref); err == nil || got != "" {
		t.Fatalf("stale full-turn cache accepted: %q %v", got, err)
	}
	if _, err := source.Exec(`UPDATE part SET data = '{"type":"text","text":"bravo"}' WHERE id = 'p1'`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				if got, err := v.LoadVerifiedEpisodicPointer(newID, ref); err != nil || got != newText {
					t.Errorf("concurrent source: %q %v", got, err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// TestZCodeReadHandleKeepsOneConnection pins the invariant the turn-prefix cache
// relies on: PRAGMA data_version counts the commits one connection observed, so a
// second pooled connection could read the same file state under a different count
// and be served a prefix cached before the source changed.
func TestZCodeReadHandleKeepsOneConnection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zcode.db")
	source, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec(`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT, sequence INTEGER, time_created INTEGER, data TEXT)`); err != nil {
		_ = source.Close()
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	handle, err := zcodeReadHandle(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := handle.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("zcode read handle max connections = %d, want 1", got)
	}
}

func TestJSONLPointerSameMetadataEditFailsClosed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.jsonl")
	padding := strings.Repeat(" ", 65536)
	write := func(word string) {
		t.Helper()
		body := `{"type":"user","message":{"role":"user","content":"` + word + `"}}` + "\n" + padding
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("alpha")
	stamp := time.Unix(1700000000, 0)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	db := migrationTestDB(t)
	v, err := NewVault(db, []byte("jsonl-same-metadata"))
	if err != nil {
		t.Fatal(err)
	}
	ref := "@ref:jsonl:" + path + ":0#0"
	text := "[user] alpha"
	id := episodicunit.DocID("mem:jsonl-edit", text)
	pkt, _ := v.PackBlindPacket(id, text, 1)
	if err := v.SaveLocalEpisodicPointerIndexed(id, 1, ref, "", text, pkt, IngestResponse{}); err != nil {
		t.Fatal(err)
	}
	if got, err := v.LoadVerifiedEpisodicPointer(id, ref); err != nil || got != text {
		t.Fatalf("original: %q %v", got, err)
	}
	write("bravo")
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if got, err := ResolveEpisodicPointerLive(ref); err != nil || got != "[user] bravo" {
		t.Fatalf("same-metadata source edit: %q %v", got, err)
	}
	if got, err := v.LoadVerifiedEpisodicPointer(id, ref); err == nil || got != "" {
		t.Fatalf("same-metadata edit accepted under old identity: %q %v", got, err)
	}
}

func TestEpisodicCrossSourceRecoveryAndLineage(t *testing.T) {
	dir := t.TempDir()
	body := `{"type":"user","message":{"role":"user","content":"same speech"}}` + "\n"
	paths := []string{filepath.Join(dir, "first.jsonl"), filepath.Join(dir, "second.jsonl")}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	db := migrationTestDB(t)
	v, err := NewVault(db, []byte("cross-source"))
	if err != nil {
		t.Fatal(err)
	}
	text := "[user] same speech"
	id := episodicunit.DocID("mem:cross-source", text)
	turn1, turn2 := "@ref:jsonl:"+paths[0]+":0", "@ref:jsonl:"+paths[1]+":0"
	ref1, ref2 := turn1+"#0", turn2+"#0"
	pkt, _ := v.PackBlindPacket(id, text, 1)
	v.SetIdentity(VaultIdentity{AssistantID: "first", IncarnationID: "s1", Locus: "one"})
	if err := v.SaveLocalEpisodicPointerIndexed(id, 1, ref1, turn1, text, pkt, IngestResponse{}); err != nil {
		t.Fatal(err)
	}
	v.SetIdentity(VaultIdentity{AssistantID: "second", IncarnationID: "s2", Locus: "two"})
	if err := v.SaveLocalEpisodicPointerIndexed(id, 2, ref2, turn2, text, pkt, IngestResponse{}); err != nil {
		t.Fatalf("record second source: %v", err)
	}
	var raw, assistant, incarnation, locus string
	if err := db.QueryRow(`SELECT raw_text, assistant_id, incarnation_id, locus FROM rm_units WHERE doc_id = ?`, id).Scan(&raw, &assistant, &incarnation, &locus); err != nil {
		t.Fatal(err)
	}
	if raw != ref1 || assistant != "first" || incarnation != "s1" || locus != "one" {
		t.Fatalf("primary source attribution changed: %q %q %q %q", raw, assistant, incarnation, locus)
	}
	if err := v.SaveLocalEpisodicPointerIndexed(id, 3, ref1, turn1, text, pkt, IngestResponse{}); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT assistant_id, incarnation_id, locus FROM rm_units WHERE doc_id = ?`, id).Scan(&assistant, &incarnation, &locus); err != nil {
		t.Fatal(err)
	}
	if assistant != "first" || incarnation != "s1" || locus != "one" {
		t.Fatalf("reingest rewrote source attribution: %q %q %q", assistant, incarnation, locus)
	}
	if docs, err := v.EpisodicTurnDocuments(turn2); err != nil || docs[id] != ref2 {
		t.Fatalf("second turn provenance: %v %v", docs, err)
	}
	if err := os.Remove(paths[0]); err != nil {
		t.Fatal(err)
	}
	if got, source, err := v.LoadVerifiedEpisodicPointerSource(id, ref1); err != nil || got != text || source != ref2 {
		t.Fatalf("verified second-source recovery: %q %q %v", got, source, err)
	}
	if got, err := v.LoadVerifiedEpisodicPointer(id, ref1); err == nil || got != "" {
		t.Fatalf("legacy pointer reader silently changed source: %q %v", got, err)
	}
	if got, _, _, err := v.LoadLocalDocument(id); err == nil || got != "" {
		t.Fatalf("legacy document reader silently changed source: %q %v", got, err)
	}
	if recent, err := v.RecentSourceDoc(10); err != nil || recent == nil || recent.Text == text || recent.AssistantID != "first" {
		t.Fatalf("recent source misattributed recovery: %+v %v", recent, err)
	}
	if status := v.ClassifyAndHealEpisodicUnit("mem:cross-source", id, ref1, 1, true); status != EpisodicContentRelocatable {
		t.Fatalf("live alternate source must prevent pruning: %v", status)
	}
	if _, err := db.Exec(`UPDATE rm_episodic_sources SET ref_pointer = ? WHERE doc_id = ? AND ref_pointer = ?`, turn2+"#1", id, ref2); err != nil {
		t.Fatal(err)
	}
	if got, _, err := v.LoadVerifiedEpisodicPointerSource(id, ref1); err == nil || got != "" {
		t.Fatalf("tampered source permitted recovery: %q %v", got, err)
	}
	if _, err := db.Exec(`UPDATE rm_episodic_sources SET ref_pointer = ? WHERE doc_id = ? AND ref_pointer = ?`, ref2, id, turn2+"#1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths[0], []byte(`{"type":"user","message":{"role":"user","content":"changed"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if status := v.ClassifyAndHealEpisodicUnit("mem:cross-source", id, ref1, 1, true); status != EpisodicContentRelocatable {
		t.Fatalf("absent primary with live alternate can be pruned: %v", status)
	}
	if err := os.Remove(paths[1]); err != nil {
		t.Fatal(err)
	}
	if status := v.ClassifyAndHealEpisodicUnit("mem:cross-source", id, ref1, 1, true); status != EpisodicContentRelocatable {
		t.Fatalf("unavailable alternate can be pruned: %v", status)
	}
	if err := os.WriteFile(paths[1], []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, source, err := v.LoadVerifiedEpisodicPointerSource(id, ref1); err != nil || got != text || source != ref2 {
		t.Fatalf("restored alternate remained unavailable: %q %q %v", got, source, err)
	}
	if _, err := db.Exec(`UPDATE rm_episodic_index SET payload = x'00' WHERE doc_id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if got, _, err := v.LoadVerifiedEpisodicPointerSource(id, ref1); err == nil || got != "" {
		t.Fatalf("corrupt index permitted recovery: %q %v", got, err)
	}
	if status := v.ClassifyAndHealEpisodicUnit("mem:cross-source", id, ref1, 1, true); status != EpisodicContentUnreadable {
		t.Fatalf("corrupt index classified as verified: %v", status)
	}
}

func TestEpisodicSaveItemErrorsAndInfrastructureFailures(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"user","message":{"role":"user","content":"sample"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	db := migrationTestDB(t)
	v, err := NewVault(db, []byte("item-errors"))
	if err != nil {
		t.Fatal(err)
	}
	ref := "@ref:jsonl:" + path + ":0#0"
	text := "[user] sample"
	pkt, _ := v.PackBlindPacket("doc", text, 1)
	for _, tc := range []struct{ ref, turn, text string }{
		{ref: ref, turn: "@ref:jsonl:" + path + ":1", text: text},
		{ref: ref, text: "[user] different"},
		{ref: "@ref:jsonl:" + filepath.Join(dir, "missing.jsonl") + ":0#0", text: text},
	} {
		if err := v.SaveLocalEpisodicPointerIndexed("doc", 1, tc.ref, tc.turn, tc.text, pkt, IngestResponse{}); !errors.Is(err, ErrEpisodicIndexItem) {
			t.Fatalf("item failure not classified: %v", err)
		}
	}
	if err := v.SaveLocalEpisodicPointerIndexed("doc", 1, ref, "", text, pkt, IngestResponse{}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE rm_episodic_index SET payload_mac = x'00' WHERE doc_id = 'doc'`); err != nil {
		t.Fatal(err)
	}
	if err := v.SaveLocalEpisodicPointerIndexed("doc", 1, ref, "", text, pkt, IngestResponse{}); !errors.Is(err, ErrEpisodicIndexItem) {
		t.Fatalf("index integrity failure not classified: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	newPkt, _ := v.PackBlindPacket("new", text, 1)
	if err := v.SaveLocalEpisodicPointerIndexed("new", 1, ref, "", text, newPkt, IngestResponse{}); err == nil || errors.Is(err, ErrEpisodicIndexItem) {
		t.Fatalf("database failure classified as item: %v", err)
	}
}

func TestEpisodicDeleteRetainsConcurrentAlternateSource(t *testing.T) {
	dir := t.TempDir()
	paths := []string{filepath.Join(dir, "first.jsonl"), filepath.Join(dir, "second.jsonl")}
	body := `{"type":"user","message":{"role":"user","content":"recoverable"}}` + "\n"
	for _, path := range paths {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	db := migrationTestDB(t)
	v, err := NewVault(db, []byte("delete-concurrent-source"))
	if err != nil {
		t.Fatal(err)
	}
	text := "[user] recoverable"
	id := episodicunit.DocID("mem:delete-source", text)
	ref1 := "@ref:jsonl:" + paths[0] + ":0#0"
	ref2 := "@ref:jsonl:" + paths[1] + ":0#0"
	pkt, _ := v.PackBlindPacket(id, text, 1)
	if err := v.SaveLocalEpisodicPointerIndexed(id, 1, ref1, "", text, pkt, IngestResponse{}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths[0], []byte(`{"type":"user","message":{"role":"user","content":"changed"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if status := v.ClassifyAndHealEpisodicUnit("mem:delete-source", id, ref1, 1, false); status != EpisodicContentAbsent {
		t.Fatalf("initial status = %v", status)
	}
	if err := v.SaveLocalEpisodicPointerIndexed(id, 1, ref2, "", text, pkt, IngestResponse{}); err != nil {
		t.Fatal(err)
	}
	if deleted, err := v.DeleteLocalEpisodicUnit(id, ref1); err != nil || deleted {
		t.Fatalf("deleted recoverable document: %t %v", deleted, err)
	}
}

func TestEpisodicRepointKeepsCodecGenerations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.jsonl")
	original := `{"type":"user","message":{"role":"user","content":"start"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":"keep"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":"later"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":"other"}}` + "\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	db := migrationTestDB(t)
	v, err := NewVault(db, []byte("repoint-codecs"))
	if err != nil {
		t.Fatal(err)
	}
	other := v.CloneWithSharedSlotCache()
	other.SetCanonicalHarmonics(plain.CanonicalHarmonicOptions{EnableStructuredDates: true})
	oldRef := "@ref:jsonl:" + path + ":0#2"
	newRef := "@ref:jsonl:" + path + ":0#1"
	text := "[assistant] later"
	id := episodicunit.DocID("mem:codec", text)
	pkt, _ := v.PackBlindPacket(id, text, 1)
	if err := v.SaveLocalEpisodicPointerIndexed(id, 1, oldRef, "", text, pkt, IngestResponse{}); err != nil {
		t.Fatal(err)
	}
	if created, err := other.EnsureLocalEpisodicIndex("mem:codec", id, oldRef, text); err != nil || !created {
		t.Fatalf("second codec: %t %v", created, err)
	}
	if err := os.WriteFile(path, []byte(`{"type":"user","message":{"role":"user","content":"start"}}`+"\n"+
		`{"type":"assistant","message":{"role":"assistant","content":"later"}}`+"\n"+
		`{"type":"assistant","message":{"role":"assistant","content":"other"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := v.SaveLocalEpisodicPointerIndexed(id, 1, newRef, "", text, pkt, IngestResponse{}); err != nil {
		t.Fatalf("repoint: %v", err)
	}
	var mismatched int
	if err := db.QueryRow(`SELECT COUNT(*) FROM rm_episodic_index WHERE doc_id = ? AND ref_pointer <> ?`, id, newRef).Scan(&mismatched); err != nil || mismatched != 0 {
		t.Fatalf("codec generation retained stale pointer: %d %v", mismatched, err)
	}
	pktOther, _ := other.PackBlindPacket(id, text, 1)
	if err := other.SaveLocalEpisodicPointerIndexed(id, 1, newRef, "", text, pktOther, IngestResponse{}); err != nil {
		t.Fatalf("save under second codec after repoint: %v", err)
	}
	if got, err := other.LoadVerifiedEpisodicPointer(id, newRef); err != nil || got != text {
		t.Fatalf("second codec hydration: %q %v", got, err)
	}
}

func TestEpisodicRepointRejectsCorruptHistoricalCodec(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.jsonl")
	write := func(middle bool) {
		t.Helper()
		body := `{"type":"user","message":{"role":"user","content":"start"}}` + "\n"
		if middle {
			body += `{"type":"assistant","message":{"role":"assistant","content":"middle"}}` + "\n"
		}
		body += `{"type":"assistant","message":{"role":"assistant","content":"keep"}}` + "\n" +
			`{"type":"assistant","message":{"role":"assistant","content":"other"}}` + "\n"
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(true)
	db := migrationTestDB(t)
	v, err := NewVault(db, []byte("repoint-corrupt-codec"))
	if err != nil {
		t.Fatal(err)
	}
	other := v.CloneWithSharedSlotCache()
	other.SetCanonicalHarmonics(plain.CanonicalHarmonicOptions{EnableStructuredDates: true})
	oldRef, newRef := "@ref:jsonl:"+path+":0#2", "@ref:jsonl:"+path+":0#1"
	text := "[assistant] keep"
	id := episodicunit.DocID("mem:corrupt-codec", text)
	pkt, _ := v.PackBlindPacket(id, text, 1)
	if err := v.SaveLocalEpisodicPointerIndexed(id, 1, oldRef, "", text, pkt, IngestResponse{}); err != nil {
		t.Fatal(err)
	}
	if created, err := other.EnsureLocalEpisodicIndex("mem:corrupt-codec", id, oldRef, text); err != nil || !created {
		t.Fatalf("second codec: %t %v", created, err)
	}
	otherKey, _ := other.episodicCodecKey()
	if _, err := db.Exec(`UPDATE rm_episodic_index SET payload_mac = x'00' WHERE doc_id = ? AND codec_key = ?`, id, otherKey); err != nil {
		t.Fatal(err)
	}
	write(false)
	if err := v.SaveLocalEpisodicPointerIndexed(id, 1, newRef, "", text, pkt, IngestResponse{}); !errors.Is(err, ErrEpisodicIndexItem) {
		t.Fatalf("corrupt codec did not reject repoint: %v", err)
	}
	var raw string
	if err := db.QueryRow(`SELECT raw_text FROM rm_units WHERE doc_id = ?`, id).Scan(&raw); err != nil || raw != oldRef {
		t.Fatalf("failed repoint changed primary: %q %v", raw, err)
	}
	var changed int
	if err := db.QueryRow(`SELECT COUNT(*) FROM rm_episodic_index WHERE doc_id = ? AND ref_pointer <> ?`, id, oldRef).Scan(&changed); err != nil || changed != 0 {
		t.Fatalf("failed repoint changed generations: %d %v", changed, err)
	}
}

func TestEpisodicAlternateSourceIdempotent(t *testing.T) {
	dir := t.TempDir()
	paths := []string{filepath.Join(dir, "first.jsonl"), filepath.Join(dir, "second.jsonl")}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte(`{"type":"user","message":{"role":"user","content":"same"}}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	db := migrationTestDB(t)
	db.SetMaxOpenConns(1)
	v, err := NewVault(db, []byte("source-idempotent"))
	if err != nil {
		t.Fatal(err)
	}
	text := "[user] same"
	id := episodicunit.DocID("mem:idempotent", text)
	pkt, _ := v.PackBlindPacket(id, text, 1)
	refs := []string{"@ref:jsonl:" + paths[0] + ":0#0", "@ref:jsonl:" + paths[1] + ":0#0"}
	for _, ref := range refs {
		if err := v.SaveLocalEpisodicPointerIndexed(id, 1, ref, "", text, pkt, IngestResponse{}); err != nil {
			t.Fatal(err)
		}
	}
	var before, after int64
	if err := db.QueryRow(`SELECT total_changes()`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := v.SaveLocalEpisodicPointerIndexed(id, 1, refs[1], "", text, pkt, IngestResponse{}); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT total_changes()`).Scan(&after); err != nil || after != before {
		t.Fatalf("unchanged source wrote rows: %d -> %d, %v", before, after, err)
	}
	if _, err := db.Exec(`UPDATE rm_episodic_sources SET source_mac = x'00' WHERE doc_id = ? AND ref_pointer = ?`, id, refs[1]); err != nil {
		t.Fatal(err)
	}
	if err := v.SaveLocalEpisodicPointerIndexed(id, 1, refs[1], "", text, pkt, IngestResponse{}); !errors.Is(err, ErrEpisodicIndexItem) {
		t.Fatalf("tampered source silently re-signed: %v", err)
	}
}

func TestEpisodicDeletedSourceCannotAuthorizeReusedID(t *testing.T) {
	dir := t.TempDir()
	paths := []string{filepath.Join(dir, "old.jsonl"), filepath.Join(dir, "new.jsonl")}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte(`{"type":"user","message":{"role":"user","content":"same"}}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	db := migrationTestDB(t)
	v, err := NewVault(db, []byte("deleted-source"))
	if err != nil {
		t.Fatal(err)
	}
	text := "[user] same"
	id := episodicunit.DocID("mem:deleted-source", text)
	oldRef := "@ref:jsonl:" + paths[0] + ":0#0"
	newRef := "@ref:jsonl:" + paths[1] + ":0#0"
	pkt, _ := v.PackBlindPacket(id, text, 1)
	if err := v.SaveLocalEpisodicPointerIndexed(id, 1, oldRef, "", text, pkt, IngestResponse{}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM rm_units WHERE doc_id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM rm_docs WHERE unit_id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if err := v.SaveLocalEpisodicPointerIndexed(id, 2, newRef, "", text, pkt, IngestResponse{}); err != nil {
		t.Fatal(err)
	}
	if got, err := v.LoadVerifiedEpisodicPointer(id, oldRef); err == nil || got != "" {
		t.Fatalf("deleted source authorized reused identity: %q %v", got, err)
	}
}

func TestEpisodicRepeatedFirstSegmentAndAttribution(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "repeated.jsonl")
	body := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"repeat"},{"type":"text","text":"repeat"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	db := migrationTestDB(t)
	v, err := NewVault(db, []byte("repeat-source"))
	if err != nil {
		t.Fatal(err)
	}
	turn := "@ref:jsonl:" + path + ":0"
	text := "[user] repeat"
	id := episodicunit.DocID("mem:repeat", text)
	pkt, _ := v.PackBlindPacket(id, text, 1)
	for _, ref := range []string{turn + "#0", turn + "#1"} {
		if err := v.SaveLocalEpisodicPointerIndexed(id, 1, ref, turn, text, pkt, IngestResponse{}); err != nil {
			t.Fatal(err)
		}
	}
	if got, ok := v.VerifiedTurnFirstSegment(turn); !ok || got != text {
		t.Fatalf("repeated first segment: %q %t", got, ok)
	}
	otherPath := filepath.Join(dir, "other.jsonl")
	if err := os.WriteFile(otherPath, []byte(`{"type":"user","message":{"role":"user","content":"repeat"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	otherTurn := "@ref:jsonl:" + otherPath + ":0"
	if err := v.SaveLocalEpisodicPointerIndexed(id, 2, otherTurn+"#0", otherTurn, text, pkt, IngestResponse{}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"type":"user","message":{"role":"user","content":"changed"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := v.VerifiedTurnFirstSegment(turn); ok || got != "" {
		t.Fatalf("foreign source substituted for turn head: %q %t", got, ok)
	}
	other := "@ref:jsonl:" + path + ":1"
	if err := v.SaveLocalEpisodicPointerIndexed(id, 1, otherTurn+"#0", other, text, pkt, IngestResponse{}); err == nil {
		t.Fatal("pointer accepted with an unrelated turn provenance")
	}
	if docs, err := v.EpisodicTurnDocuments(other); err != nil || len(docs) != 0 {
		t.Fatalf("foreign turn gained provenance: %v %v", docs, err)
	}
}
