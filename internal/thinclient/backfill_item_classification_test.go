// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/rocareer/rolling-memory/internal/episodicunit"
	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
	_ "modernc.org/sqlite"
)

func TestEpisodicBackfillEncodingFailureStopsBeforeCursor(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "vault.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, err := NewVault(db, []byte("backfill-encoding-failure"))
	if err != nil {
		t.Fatal(err)
	}
	v.segOpts.Segmenter = func(text string) []string { return []string{text} }
	v.segOpts.SentenceAtomPacker = func(_ *ramesdk.BlindEncoder, _ string, _ int) []ramesdk.BlindTokenAtom {
		return make([]ramesdk.BlindTokenAtom, (1<<20)+1)
	}
	const scope = "mem:encoding-failure"
	const text = "[user] encoding failure"
	id := episodicunit.DocID(scope, text)
	path := filepath.Join(dir, "source.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"user","message":{"role":"user","content":"encoding failure"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := v.SaveLocalEpisodicPointer(id, 1, "@ref:jsonl:"+path+":0#0", "", CloudIngestResponse{}); err != nil {
		t.Fatal(err)
	}
	page, err := v.BackfillEpisodicIndexes(context.Background(), scope, "", 10)
	if err == nil || page.Cursor != "" || page.Scanned != 0 || page.Failed != 0 {
		t.Fatalf("encoding error was skipped: %+v %v", page, err)
	}
}

func TestEpisodicBackfillCorruptItemDoesNotBlock(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "vault.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, err := NewVault(db, []byte("backfill-item-classification"))
	if err != nil {
		t.Fatal(err)
	}
	const scope = "mem:item-classification"
	var badText, goodText, badID, goodID string
	for i := 0; ; i++ {
		badText = fmt.Sprintf("[user] first source %d", i)
		goodText = fmt.Sprintf("[user] second source %d", i)
		badID = episodicunit.DocID(scope, badText)
		goodID = episodicunit.DocID(scope, goodText)
		if badID < goodID {
			break
		}
	}
	path := filepath.Join(dir, "source.jsonl")
	body := fmt.Sprintf("{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":%q}}\n{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":%q}}\n", badText[len("[user] "):], goodText[len("[user] "):])
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	badRef := "@ref:jsonl:" + path + ":0#0"
	goodRef := "@ref:jsonl:" + path + ":1#0"
	for _, row := range []struct{ id, ref string }{{badID, badRef}, {goodID, goodRef}} {
		if err := v.SaveLocalEpisodicPointer(row.id, 1, row.ref, "", CloudIngestResponse{}); err != nil {
			t.Fatal(err)
		}
	}
	if created, err := v.EnsureLocalEpisodicIndex(scope, badID, badRef, badText); err != nil || !created {
		t.Fatalf("prepare index: %v %v", created, err)
	}
	if _, err := db.Exec(`UPDATE thin_vault_episodic_index SET payload = x'00' WHERE doc_id = ?`, badID); err != nil {
		t.Fatal(err)
	}
	var before []byte
	if err := db.QueryRow(`SELECT payload FROM thin_vault_episodic_index WHERE doc_id = ?`, badID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	page, err := v.BackfillEpisodicIndexes(context.Background(), scope, "", 10)
	if err != nil || page.Scanned != 2 || page.Failed != 1 || page.Indexed != 1 || page.Cursor != goodID {
		t.Fatalf("item isolation: %+v %v", page, err)
	}
	var after []byte
	if err := db.QueryRow(`SELECT payload FROM thin_vault_episodic_index WHERE doc_id = ?`, badID).Scan(&after); err != nil || !bytes.Equal(before, after) {
		t.Fatalf("evidence changed: %x %v", after, err)
	}
	page, err = v.BackfillEpisodicIndexes(context.Background(), scope, "", 10)
	if err != nil || page.Failed != 1 || page.Unchanged != 1 {
		t.Fatalf("retry: %+v %v", page, err)
	}
}
