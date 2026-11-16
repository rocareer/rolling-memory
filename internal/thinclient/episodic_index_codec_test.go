// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/rocareer/rolling-memory/internal/episodicunit"
	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
	_ "modernc.org/sqlite"
)

func TestEpisodicIndexCodecIsolation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.sqlite")
	source := filepath.Join(dir, "source.jsonl")
	body := `{"type":"user","message":{"role":"user","content":"琥珀 مرصد quartz 2026-11-11"}}` + "\n"
	if err := os.WriteFile(source, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ref := "@ref:jsonl:" + source + ":0#0"
	text, err := ResolveEpisodicPointerLive(ref)
	if err != nil {
		t.Fatal(err)
	}
	db1, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db1.Close()
	db2, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	v1, err := NewVault(db1, []byte("codec-isolation-key"))
	if err != nil {
		t.Fatal(err)
	}
	v2, err := NewVault(db2, []byte("codec-isolation-key"))
	if err != nil {
		t.Fatal(err)
	}
	v2.SetCanonicalHarmonics(ramesdk.CanonicalHarmonicOptions{EnableStructuredDates: true})
	const scope = "codec-test"
	docID := episodicunit.DocID(scope, text)
	pkt, _ := v1.PackBlindPacket(docID, text, 1)
	if err := v1.SaveLocalEpisodicPointerIndexed(docID, 1, ref, "", text, pkt, CloudIngestResponse{}); err != nil {
		t.Fatal(err)
	}
	if _, err := v2.SearchClientSDKDefault("quartz", 10); err != nil {
		t.Fatal(err)
	}
	created, err := v2.EnsureLocalEpisodicIndex(scope, docID, ref, text)
	if err != nil || !created {
		t.Fatalf("ensure second codec: %v %v", created, err)
	}
	created, err = v1.EnsureLocalEpisodicIndex(scope, docID, ref, text)
	if err != nil || created {
		t.Fatalf("ensure original codec: %v %v", created, err)
	}
	var count int
	if err := db1.QueryRow(`SELECT COUNT(*) FROM thin_vault_episodic_index WHERE doc_id = ?`, docID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("codec rows: %d %v", count, err)
	}
	rows, err := db1.Query(`SELECT ref_pointer, payload FROM thin_vault_episodic_index WHERE doc_id = ?`, docID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var pointer string
		var payload []byte
		if err := rows.Scan(&pointer, &payload); err != nil {
			t.Fatal(err)
		}
		if pointer != ref || bytes.Contains(payload, []byte(text)) {
			t.Fatalf("unsafe index row: %q %x", pointer, payload)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	var stored string
	if err := db1.QueryRow(`SELECT raw_text FROM thin_vault_docs WHERE doc_id = ?`, docID).Scan(&stored); err != nil || stored != ref {
		t.Fatalf("pointer: %q %v", stored, err)
	}
	for _, v := range []*Vault{v1, v2} {
		compatible, incompatible, missing, err := v.EpisodicIndexDiagnostics()
		if err != nil || compatible != 1 || incompatible != 0 || missing != 0 {
			t.Fatalf("codec diagnostics: compatible=%d incompatible=%d missing=%d: %v", compatible, incompatible, missing, err)
		}
		hits, err := v.SearchClientSDKDefault("quartz", 10)
		if err != nil || len(hits) != 1 || hits[0].UnitID != docID {
			t.Fatalf("codec search: %+v %v", hits, err)
		}
	}
	v3, err := NewVault(db2, []byte("other-codec-key"))
	if err != nil {
		t.Fatal(err)
	}
	compatible, incompatible, missing, err := v3.EpisodicIndexDiagnostics()
	if err != nil || compatible != 0 || incompatible != 1 || missing != 0 {
		t.Fatalf("unmatched codec diagnostics: compatible=%d incompatible=%d missing=%d: %v", compatible, incompatible, missing, err)
	}
	pkt, _ = v1.PackBlindPacket(docID, text, 1)
	if err := v1.SaveLocalEpisodicPointerIndexed(docID, 1, ref, "", text, pkt, CloudIngestResponse{}); err != nil {
		t.Fatal(err)
	}
	if err := db1.QueryRow(`SELECT COUNT(*) FROM thin_vault_episodic_index WHERE doc_id = ?`, docID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("save removed another codec: %d %v", count, err)
	}
}

func TestEpisodicIndexLegacySchemaMigratedWithoutLoss(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "vault.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE thin_vault_episodic_index (doc_id TEXT PRIMARY KEY, ref_pointer TEXT NOT NULL, codec_key BLOB NOT NULL, payload BLOB NOT NULL);
INSERT INTO thin_vault_episodic_index VALUES ('old', '@ref:old', x'0102', x'0304');`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewVault(db, []byte("legacy-key")); err != nil {
		t.Fatalf("migrate legacy schema: %v", err)
	}
	var pointer string
	var key, payload, digest []byte
	if err := db.QueryRow(`SELECT ref_pointer, codec_key, payload, content_digest FROM thin_vault_episodic_index WHERE doc_id = 'old'`).Scan(&pointer, &key, &payload, &digest); err != nil {
		t.Fatal(err)
	}
	if pointer != "@ref:old" || !bytes.Equal(key, []byte{1, 2}) || !bytes.Equal(payload, []byte{3, 4}) || digest != nil {
		t.Fatalf("legacy row changed: %q %x %x %x", pointer, key, payload, digest)
	}
}
