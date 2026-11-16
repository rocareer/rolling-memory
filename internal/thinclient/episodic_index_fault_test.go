// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/rocareer/rolling-memory/internal/episodicunit"
	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
	_ "modernc.org/sqlite"
)

func TestEpisodicIndexCorruptPayloadIsolation(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "vault.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, err := NewVault(db, []byte("fault-test"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"user","message":{"role":"user","content":"quartz nebula"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ref := "@ref:jsonl:" + path + ":0#0"
	text, err := ResolveEpisodicPointerLive(ref)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"bad", "good"} {
		pkt, _ := v.PackBlindPacket(id, text, 1)
		if err := v.SaveLocalEpisodicPointerIndexed(id, 1, ref, "", text, pkt, CloudIngestResponse{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO thin_vault_episodic_index(doc_id, ref_pointer, codec_key, payload, content_digest) SELECT doc_id, ref_pointer, x'01', payload, content_digest FROM thin_vault_episodic_index WHERE doc_id = 'good'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE thin_vault_episodic_index SET payload = x'00' WHERE doc_id = 'bad'`); err != nil {
		t.Fatal(err)
	}
	report, err := v.EpisodicIndexFaultDiagnostics()
	if err != nil || report.Compatible != 1 || report.Corrupt != 1 || report.Incompatible != 0 || report.Missing != 0 {
		t.Fatalf("diagnostics: %+v %v", report, err)
	}
	compatible, incompatible, missing, err := v.EpisodicIndexDiagnostics()
	if err != nil || compatible != 1 || incompatible != 0 || missing != 0 {
		t.Fatalf("coverage: %d %d %d %v", compatible, incompatible, missing, err)
	}
	assertHits := func(hits []CloudQueryHit, err error) {
		t.Helper()
		if err != nil || len(hits) != 1 || hits[0].UnitID != "good" {
			t.Fatalf("search: %+v %v", hits, err)
		}
	}
	assertHits(v.SearchClientSDKDefault("quartz nebula", 10))
	pkt, _ := v.PackBlindPacket("query", "quartz nebula", 0)
	assertHits(NewLocalVaultTransport(v).QueryBlindManifold(context.Background(), pkt, 10))
	if _, err := db.Exec(`UPDATE thin_vault_episodic_index SET content_digest = NULL WHERE doc_id = 'good' AND codec_key <> x'01'`); err != nil {
		t.Fatal(err)
	}
	report, err = v.EpisodicIndexFaultDiagnostics()
	if err != nil || report.NeedsRebuild != 1 || report.Corrupt != 1 || report.Compatible != 0 {
		t.Fatalf("unverified digest: %+v %v", report, err)
	}
}

func TestEpisodicIndexAuthenticatedPayloadIsolation(t *testing.T) {
	db := migrationTestDB(t)
	v, err := NewVault(db, []byte("authenticated-index"))
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewVault(db, []byte("authenticated-index"))
	if err != nil {
		t.Fatal(err)
	}
	other.SetCanonicalHarmonics(ramesdk.CanonicalHarmonicOptions{EnableStructuredDates: true})
	path := filepath.Join(t.TempDir(), "source.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"user","message":{"role":"user","content":"quartz nebula"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ref := "@ref:jsonl:" + path + ":0#0"
	text, err := ResolveEpisodicPointerLive(ref)
	if err != nil {
		t.Fatal(err)
	}
	const scope = "authenticated-test"
	id := episodicunit.DocID(scope, text)
	pkt, _ := v.PackBlindPacket(id, text, 1)
	if err := v.SaveLocalEpisodicPointerIndexed(id, 1, ref, "", text, pkt, CloudIngestResponse{}); err != nil {
		t.Fatal(err)
	}
	if created, err := other.EnsureLocalEpisodicIndex(scope, id, ref, text); err != nil || !created {
		t.Fatalf("second codec: %v %v", created, err)
	}
	key, _ := v.episodicCodecKey()
	otherKey, _ := other.episodicCodecKey()
	var original, mac []byte
	if err := db.QueryRow(`SELECT payload, payload_mac FROM thin_vault_episodic_index WHERE doc_id = ? AND codec_key = ?`, id, key).Scan(&original, &mac); err != nil {
		t.Fatal(err)
	}
	slots, frames, err := ramesdk.DecodeWireBytes(original)
	if err != nil || len(slots) == 0 {
		t.Fatalf("decode original: %v", err)
	}
	slots[0].H ^= 1
	mutated, err := ramesdk.EncodeWireBytes(slots, frames)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ramesdk.DecodeWireBytes(mutated); err != nil {
		t.Fatalf("tampered payload must remain decodable: %v", err)
	}
	if _, err := db.Exec(`UPDATE thin_vault_episodic_index SET payload = ? WHERE doc_id = ? AND codec_key = ?`, mutated, id, key); err != nil {
		t.Fatal(err)
	}
	if report, err := v.EpisodicIndexFaultDiagnostics(); err != nil || report.Corrupt != 1 {
		t.Fatalf("tamper diagnostics: %+v %v", report, err)
	}
	if _, err := v.LoadVerifiedEpisodicPointer(id, ref); err == nil {
		t.Fatal("tampered payload hydrated")
	}
	if docs, err := v.indexedEpisodicDocs(); err != nil || len(docs) != 0 {
		t.Fatalf("tampered payload indexed: %v %v", docs, err)
	}
	if docs, err := other.indexedEpisodicDocs(); err != nil || len(docs) != 1 {
		t.Fatalf("other codec affected: %v %v", docs, err)
	}
	if _, err := v.EnsureLocalEpisodicIndex(scope, id, ref, text); !errors.Is(err, ErrEpisodicIndexItem) {
		t.Fatalf("tampered payload should be an item error: %v", err)
	}
	var unchanged []byte
	if err := db.QueryRow(`SELECT payload FROM thin_vault_episodic_index WHERE doc_id = ? AND codec_key = ?`, id, key).Scan(&unchanged); err != nil || string(unchanged) != string(mutated) {
		t.Fatalf("tampered payload changed: %v", err)
	}
	if _, err := db.Exec(`UPDATE thin_vault_episodic_index SET payload = ?, payload_mac = NULL WHERE doc_id = ? AND codec_key = ?`, original, id, key); err != nil {
		t.Fatal(err)
	}
	if report, err := v.EpisodicIndexFaultDiagnostics(); err != nil || report.NeedsRebuild != 1 {
		t.Fatalf("missing MAC diagnostics: %+v %v", report, err)
	}
	if _, err := v.LoadVerifiedEpisodicPointer(id, ref); err == nil {
		t.Fatal("missing MAC hydrated")
	}
	if _, err := db.Exec(`UPDATE thin_vault_episodic_index SET payload = ? WHERE doc_id = ? AND codec_key = ?`, mutated, id, key); err != nil {
		t.Fatal(err)
	}
	if _, err := v.EnsureLocalEpisodicIndex(scope, id, ref, text); !errors.Is(err, ErrEpisodicIndexItem) {
		t.Fatalf("unsigned tampered payload should be an item error: %v", err)
	}
	if err := db.QueryRow(`SELECT payload FROM thin_vault_episodic_index WHERE doc_id = ? AND codec_key = ?`, id, key).Scan(&unchanged); err != nil || string(unchanged) != string(mutated) {
		t.Fatalf("unsigned tampered payload changed: %v", err)
	}
	if _, err := db.Exec(`UPDATE thin_vault_episodic_index SET payload = ? WHERE doc_id = ? AND codec_key = ?`, original, id, key); err != nil {
		t.Fatal(err)
	}
	if created, err := v.EnsureLocalEpisodicIndex(scope, id, ref, text); err != nil || !created {
		t.Fatalf("verified rebuild: %v %v", created, err)
	}
	if _, err := db.Exec(`UPDATE thin_vault_episodic_index SET payload_mac = ? WHERE doc_id = ? AND codec_key = ?`, mac, id, otherKey); err != nil {
		t.Fatal(err)
	}
	if report, err := other.EpisodicIndexFaultDiagnostics(); err != nil || report.Corrupt != 1 {
		t.Fatalf("swapped codec MAC: %+v %v", report, err)
	}
	if docs, err := v.indexedEpisodicDocs(); err != nil || len(docs) != 1 {
		t.Fatalf("original codec affected: %v %v", docs, err)
	}
}

func TestEpisodicIndexSwappedRows(t *testing.T) {
	db := migrationTestDB(t)
	v, err := NewVault(db, []byte("swap-index"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "source.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"user","message":{"role":"user","content":"quartz nebula"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ref := "@ref:jsonl:" + path + ":0#0"
	text, err := ResolveEpisodicPointerLive(ref)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"one", "two", "good"} {
		pkt, _ := v.PackBlindPacket(id, text, 1)
		if err := v.SaveLocalEpisodicPointerIndexed(id, 1, ref, "", text, pkt, CloudIngestResponse{}); err != nil {
			t.Fatal(err)
		}
	}
	var mac1, mac2 []byte
	if err := db.QueryRow(`SELECT payload_mac FROM thin_vault_episodic_index WHERE doc_id = 'one'`).Scan(&mac1); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT payload_mac FROM thin_vault_episodic_index WHERE doc_id = 'two'`).Scan(&mac2); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE thin_vault_episodic_index SET payload_mac = CASE doc_id WHEN 'one' THEN ? ELSE ? END WHERE doc_id IN ('one', 'two')`, mac2, mac1); err != nil {
		t.Fatal(err)
	}
	if report, err := v.EpisodicIndexFaultDiagnostics(); err != nil || report.Corrupt != 2 || report.Compatible != 1 {
		t.Fatalf("swapped rows: %+v %v", report, err)
	}
	if hits, err := v.SearchClientSDKDefault("quartz nebula", 10); err != nil || len(hits) != 1 || hits[0].UnitID != "good" {
		t.Fatalf("unaffected row: %+v %v", hits, err)
	}
}

func TestEpisodicIndexSourceDeleteCascade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, err := NewVault(db, []byte("fault-test"))
	if err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(transcript, []byte(`{"type":"user","message":{"role":"user","content":"quartz nebula"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ref := "@ref:jsonl:" + transcript + ":0#0"
	text, err := ResolveEpisodicPointerLive(ref)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"delete", "keep"} {
		pkt, _ := v.PackBlindPacket(id, text, 1)
		if err := v.SaveLocalEpisodicPointerIndexed(id, 1, ref, "", text, pkt, CloudIngestResponse{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := v.SearchClientSDKDefault("quartz", 10); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO thin_vault_episodic_index(doc_id, ref_pointer, codec_key, payload) SELECT 'orphan', ref_pointer, codec_key, payload FROM thin_vault_episodic_index WHERE doc_id = 'keep'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO thin_vault_episodic_index(doc_id, ref_pointer, codec_key, payload, content_digest) SELECT doc_id, ref_pointer, x'01', payload, content_digest FROM thin_vault_episodic_index WHERE doc_id = 'delete'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM thin_vault_docs WHERE doc_id = 'delete'`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM thin_vault_episodic_index WHERE doc_id = 'delete'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("deleted index: %d %v", count, err)
	}
	report, err := v.EpisodicIndexFaultDiagnostics()
	if err != nil || report.Compatible != 1 || report.Orphaned != 1 {
		t.Fatalf("diagnostics: %+v %v", report, err)
	}
	hits, err := v.SearchClientSDKDefault("quartz", 10)
	if err != nil || len(hits) != 1 || hits[0].UnitID != "keep" {
		t.Fatalf("after delete: %+v %v", hits, err)
	}
	if _, err := NewVault(db, []byte("fault-test")); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM thin_vault_episodic_index WHERE doc_id = 'orphan'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("orphan changed on reopen: %d %v", count, err)
	}
}
