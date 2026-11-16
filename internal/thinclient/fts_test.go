// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// TestFTSContentlessMigrationAndRoundtrip pins the local lexical index contract:
// a legacy content-storing table is migrated to a contentless index plus a
// rowid↔doc_id map in one pass, rows orphaned from every source table are dropped,
// MATCH still ranks through the map, the indexed text is NOT readable back, and
// replace/delete keep the map and the postings consistent.
func TestFTSContentlessMigrationAndRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	mustExec(`CREATE TABLE thin_vault_docs (doc_id TEXT PRIMARY KEY, raw_text TEXT NOT NULL)`)
	mustExec(`CREATE TABLE thin_vault_bg_chunks (unit_id TEXT PRIMARY KEY)`)
	mustExec(`CREATE VIRTUAL TABLE thin_vault_docs_fts USING fts5(raw_text, doc_id UNINDEXED, tokenize = 'trigram')`)
	mustExec(`INSERT INTO thin_vault_docs(doc_id, raw_text) VALUES ('ep_live', '正文')`)
	mustExec(`INSERT INTO thin_vault_bg_chunks(unit_id) VALUES ('bg:live#0')`)
	mustExec(`INSERT INTO thin_vault_docs_fts(raw_text, doc_id) VALUES ('local lexical 索引正文', 'ep_live')`)
	mustExec(`INSERT INTO thin_vault_docs_fts(raw_text, doc_id) VALUES ('背景分块 索引正文', 'bg:live#0')`)
	mustExec(`INSERT INTO thin_vault_docs_fts(raw_text, doc_id) VALUES ('孤儿行 索引正文', 'ep_orphan')`)

	if !ensureFTS(db) {
		t.Fatal("ensureFTS failed on a legacy vault")
	}
	var ddl string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'thin_vault_docs_fts'`).Scan(&ddl); err != nil {
		t.Fatalf("read fts ddl: %v", err)
	}
	if !strings.Contains(ddl, "content=''") {
		t.Fatalf("fts table is not contentless: %s", ddl)
	}
	var mapped int
	if err := db.QueryRow(`SELECT COUNT(*) FROM thin_vault_fts_map`).Scan(&mapped); err != nil {
		t.Fatalf("count map: %v", err)
	}
	if mapped != 2 {
		t.Fatalf("map rows = %d, want 2 (live doc + live chunk)", mapped)
	}
	var orphan int
	if err := db.QueryRow(`SELECT COUNT(*) FROM thin_vault_fts_map WHERE doc_id = 'ep_orphan'`).Scan(&orphan); err != nil {
		t.Fatalf("count orphan: %v", err)
	}
	if orphan != 0 {
		t.Fatalf("orphan rows must not survive the migration")
	}
	var hit string
	if err := db.QueryRow(`
		SELECT thin_vault_fts_map.doc_id
		FROM thin_vault_docs_fts
		JOIN thin_vault_fts_map ON thin_vault_fts_map.rowid = thin_vault_docs_fts.rowid
		WHERE thin_vault_docs_fts MATCH ?
		ORDER BY bm25(thin_vault_docs_fts) ASC
		LIMIT 1`, `"索引正文"`).Scan(&hit); err != nil {
		t.Fatalf("match through map: %v", err)
	}
	if hit == "" {
		t.Fatalf("match returned no doc id")
	}
	var stored sql.NullString
	if err := db.QueryRow(`SELECT raw_text FROM thin_vault_docs_fts LIMIT 1`).Scan(&stored); err != nil {
		t.Fatalf("read stored column: %v", err)
	}
	if stored.Valid && strings.TrimSpace(stored.String) != "" {
		t.Fatalf("contentless index exposed stored text: %q", stored.String)
	}
	if err := ftsReplaceRow(db, "ep_live", "替换后的 索引正文"); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if err := ftsDeleteRow(db, "bg:live#0"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	var left int
	if err := db.QueryRow(`SELECT COUNT(*) FROM thin_vault_fts_map`).Scan(&left); err != nil {
		t.Fatalf("count map after delete: %v", err)
	}
	if left != 1 {
		t.Fatalf("map rows after delete = %d, want 1", left)
	}
}
