// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/episodicunit"
	_ "modernc.org/sqlite"
)

func TestEpisodicBackfillPagesAndRetries(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "vault.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, err := NewVault(db, []byte("backfill-key"))
	if err != nil {
		t.Fatal(err)
	}
	const scope = "mem:backfill"
	path := filepath.Join(dir, "source.jsonl")
	body := `{"type":"user","message":{"role":"user","content":"琥珀 مرصد telescope"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ref := "@ref:jsonl:" + path + ":0#0"
	text, err := ResolveEpisodicPointerLive(ref)
	if err != nil {
		t.Fatal(err)
	}
	id := episodicunit.DocID(scope, text)
	missingPath := filepath.Join(dir, "missing.jsonl")
	missingRef := "@ref:jsonl:" + missingPath + ":0#0"
	missingID := episodicunit.DocID(scope, text+" later")
	changedID := episodicunit.DocID(scope, "[user] prior source")
	for _, row := range []struct{ id, ref string }{{id, ref}, {missingID, missingRef}, {changedID, ref}} {
		if err := v.SaveLocalEpisodicPointer(row.id, 1, row.ref, "", CloudIngestResponse{}); err != nil {
			t.Fatal(err)
		}
	}
	inline := "do not replace this original"
	if err := v.SaveLocalPlaintext("inline", 1, inline, []string{inline}, CloudIngestResponse{Layer: "episodic"}); err != nil {
		t.Fatal(err)
	}
	var total EpisodicIndexBackfillReport
	cursor := ""
	for {
		page, err := v.BackfillEpisodicIndexes(context.Background(), scope, cursor, 1)
		if err != nil {
			t.Fatal(err)
		}
		if page.Scanned == 0 {
			break
		}
		if page.Cursor <= cursor {
			t.Fatalf("cursor did not advance: %+v", page)
		}
		cursor = page.Cursor
		total.Scanned += page.Scanned
		total.Indexed += page.Indexed
		total.Unavailable += page.Unavailable
		total.Unverified += page.Unverified
	}
	if total.Scanned != 3 || total.Indexed != 1 || total.Unavailable != 1 || total.Unverified != 1 {
		t.Fatalf("unexpected coverage: %+v", total)
	}
	page, err := v.BackfillEpisodicIndexes(context.Background(), scope, "", 10)
	if err != nil || page.Unchanged != 1 || page.Unavailable != 1 || page.Unverified != 1 || page.Indexed != 0 {
		t.Fatalf("repeat: %+v %v", page, err)
	}
	if err := os.WriteFile(missingPath, []byte(`{"type":"user","message":{"role":"user","content":"琥珀 مرصد telescope later"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	page, err = v.BackfillEpisodicIndexes(context.Background(), scope, "", 10)
	if err != nil || page.Indexed != 1 || page.Unavailable != 0 || page.Unverified != 1 {
		t.Fatalf("retry: %+v %v", page, err)
	}
	var got string
	if err := db.QueryRow(`SELECT raw_text FROM thin_vault_docs WHERE doc_id = 'inline'`).Scan(&got); err != nil || got != inline {
		t.Fatalf("inline changed: %q %v", got, err)
	}
	for _, row := range []struct{ id, ref string }{{id, ref}, {missingID, missingRef}, {changedID, ref}} {
		if err := db.QueryRow(`SELECT raw_text FROM thin_vault_docs WHERE doc_id = ?`, row.id).Scan(&got); err != nil || got != row.ref {
			t.Fatalf("source pointer changed: %q %v", got, err)
		}
	}
	hits, err := v.SearchClientSDKDefault("مرصد telescope", 10)
	if err != nil || len(hits) == 0 {
		t.Fatalf("indexed search: %+v %v", hits, err)
	}
}

func TestEpisodicBackfillEncodingFailureDoesNotAdvancePage(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "vault.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, err := NewVault(db, []byte("backfill-key"))
	if err != nil {
		t.Fatal(err)
	}
	const scope = "mem:backfill"
	// Exceed the wire format's sentence capacity without altering the source.
	badBody := strings.Repeat("x. ", 65536)
	goodBody := "healthy telescope"
	badJSON, err := json.Marshal(badBody)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "source.jsonl")
	body := fmt.Sprintf("{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":%s}}\n{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":%q}}\n", badJSON, goodBody)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	badRef := "@ref:jsonl:" + path + ":0#0"
	goodRef := "@ref:jsonl:" + path + ":1#0"
	badText, err := ResolveEpisodicPointerLive(badRef)
	if err != nil {
		t.Fatal(err)
	}
	goodText, err := ResolveEpisodicPointerLive(goodRef)
	if err != nil {
		t.Fatal(err)
	}
	badID := episodicunit.DocID(scope, badText)
	// Find a deterministic valid ID after the bad one so page order is explicit.
	for i := 0; episodicunit.DocID(scope, goodText) <= badID; i++ {
		goodBody = fmt.Sprintf("healthy telescope %d", i)
		body = fmt.Sprintf("{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":%s}}\n{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":%q}}\n", badJSON, goodBody)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		goodText, err = ResolveEpisodicPointerLive(goodRef)
		if err != nil {
			t.Fatal(err)
		}
	}
	goodID := episodicunit.DocID(scope, goodText)
	for _, row := range []struct{ id, ref string }{{badID, badRef}, {goodID, goodRef}} {
		if err := v.SaveLocalEpisodicPointer(row.id, 1, row.ref, "", CloudIngestResponse{}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := v.BackfillEpisodicIndexes(context.Background(), scope, "", 1)
	if err == nil || page.Scanned != 0 || page.Failed != 0 || page.Cursor != "" {
		t.Fatalf("encoding failure advanced page: %+v %v", page, err)
	}
	page, err = v.BackfillEpisodicIndexes(context.Background(), scope, badID, 1)
	if err != nil || page.Scanned != 1 || page.Indexed != 1 || page.Cursor != goodID {
		t.Fatalf("good page: %+v %v", page, err)
	}
	var badCount, goodCount int
	if err := db.QueryRow(`SELECT count(*) FROM thin_vault_episodic_index WHERE doc_id = ?`, badID).Scan(&badCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM thin_vault_episodic_index WHERE doc_id = ?`, goodID).Scan(&goodCount); err != nil || badCount != 0 || goodCount != 1 {
		t.Fatalf("index counts: bad=%d good=%d err=%v", badCount, goodCount, err)
	}
	page, err = v.BackfillEpisodicIndexes(context.Background(), scope, "", 1)
	if err == nil || page.Scanned != 0 || page.Failed != 0 || page.Cursor != "" {
		t.Fatalf("rescan: %+v %v", page, err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != body {
		t.Fatalf("source changed: %v", err)
	}
}

func TestEpisodicBackfillIntegrityConflictDoesNotBlockNextPage(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "vault.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, err := NewVault(db, []byte("backfill-key"))
	if err != nil {
		t.Fatal(err)
	}
	const scope = "mem:backfill"
	path := filepath.Join(dir, "source.jsonl")
	ref := "@ref:jsonl:" + path + ":0#0"
	writeSource := func(content string) (string, string) {
		t.Helper()
		body := fmt.Sprintf("{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":%q}}\n", content)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		text, err := ResolveEpisodicPointerLive(ref)
		if err != nil {
			t.Fatal(err)
		}
		return body, text
	}
	_, badText := writeSource("damaged index")
	badID := episodicunit.DocID(scope, badText)
	pkt, _ := v.PackBlindPacket(badID, badText, 1)
	if err := v.SaveLocalEpisodicPointerIndexed(badID, 1, ref, "", badText, pkt, CloudIngestResponse{}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE thin_vault_episodic_index SET payload_mac = x'01' WHERE doc_id = ?`, badID); err != nil {
		t.Fatal(err)
	}
	if _, err := v.EnsureLocalEpisodicIndex(scope, badID, ref, badText); !errors.Is(err, ErrEpisodicIndexItem) {
		t.Fatalf("expected item integrity error: %v", err)
	}
	goodPath := filepath.Join(dir, "good.jsonl")
	goodRef := "@ref:jsonl:" + goodPath + ":0#0"
	var goodText, goodID string
	for i := 0; goodID <= badID; i++ {
		body := fmt.Sprintf("{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":%q}}\n", fmt.Sprintf("healthy telescope %d", i))
		if err := os.WriteFile(goodPath, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		goodText, err = ResolveEpisodicPointerLive(goodRef)
		if err != nil {
			t.Fatal(err)
		}
		goodID = episodicunit.DocID(scope, goodText)
	}
	if err := v.SaveLocalEpisodicPointer(goodID, 1, goodRef, "", CloudIngestResponse{}); err != nil {
		t.Fatal(err)
	}
	// Exercise an error inside Ensure after the page query has already succeeded.
	if _, err := db.Exec(`ALTER TABLE thin_vault_episodic_index RENAME TO backfill_test_index`); err != nil {
		t.Fatal(err)
	}
	page, err := v.BackfillEpisodicIndexes(context.Background(), scope, "", 1)
	if err == nil || errors.Is(err, ErrEpisodicIndexItem) || page.Cursor != "" || page.Scanned != 0 || page.Failed != 0 {
		t.Fatalf("index database error: %+v %v", page, err)
	}
	if _, err := db.Exec(`ALTER TABLE backfill_test_index RENAME TO thin_vault_episodic_index`); err != nil {
		t.Fatal(err)
	}
	page, err = v.BackfillEpisodicIndexes(context.Background(), scope, "", 1)
	if err != nil || page.Scanned != 1 || page.Failed != 1 || page.Indexed != 0 || page.Cursor != badID {
		t.Fatalf("conflict page: %+v %v", page, err)
	}
	page, err = v.BackfillEpisodicIndexes(context.Background(), scope, page.Cursor, 1)
	if err != nil || page.Scanned != 1 || page.Indexed != 1 || page.Failed != 0 || page.Cursor != goodID {
		t.Fatalf("next page: %+v %v", page, err)
	}
	var mac []byte
	if err := db.QueryRow(`SELECT payload_mac FROM thin_vault_episodic_index WHERE doc_id = ?`, badID).Scan(&mac); err != nil || string(mac) != "\x01" {
		t.Fatalf("conflicting row changed: %x %v", mac, err)
	}
	var goodCount int
	if err := db.QueryRow(`SELECT count(*) FROM thin_vault_episodic_index WHERE doc_id = ?`, goodID).Scan(&goodCount); err != nil || goodCount != 1 {
		t.Fatalf("good index: %d %v", goodCount, err)
	}
	page, err = v.BackfillEpisodicIndexes(context.Background(), scope, "", 1)
	if err != nil || page.Failed != 1 || page.Cursor != badID {
		t.Fatalf("conflict rescan: %+v %v", page, err)
	}
}

func TestEpisodicBackfillResumeAfterError(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "vault.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, err := NewVault(db, []byte("backfill-key"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	page, err := v.BackfillEpisodicIndexes(ctx, "mem:test", "cursor", 1)
	if !errors.Is(err, context.Canceled) || page.Cursor != "cursor" || page.Scanned != 0 {
		t.Fatalf("cancelled: %+v %v", page, err)
	}
	if _, err := v.BackfillEpisodicIndexes(context.Background(), "mem:test", "", 0); err == nil {
		t.Fatal("zero page size accepted")
	}
	if _, err := db.Exec(`DROP TABLE thin_vault_docs`); err != nil {
		t.Fatal(err)
	}
	page, err = v.BackfillEpisodicIndexes(context.Background(), "mem:test", "cursor", 1)
	if err == nil || page.Cursor != "cursor" || page.Scanned != 0 || page.Failed != 0 {
		t.Fatalf("database failure: %+v %v", page, err)
	}
}
