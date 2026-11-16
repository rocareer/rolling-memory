// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient

import (
	"database/sql"
	"errors"
	"strings"
)

// ============================================================================
// Local Lexical Index (`thin_vault_docs_fts`) — contentless FTS5 + rowid map
//
// The local BM25 index is a DERIVED cache: its only job is to rank local rows for
// lexical recall. It therefore indexes tokens WITHOUT storing a readable copy of
// the text (`content=''` + `contentless_delete=1`): the plaintext of background
// chunks and episodic units has exactly one home (the source file / the harness
// transcript / the client-owned transcript store), and only Noetic records keep
// their composed text in SQLite.
//
// FTS5 contentless tables cannot read a column value back, so the doc id lives in
// `thin_vault_fts_map` (rowid ↔ doc_id) and every write/delete goes through the
// helpers below. The dead `doc_id UNINDEXED` column is kept on purpose: processes
// still running the previous build keep INDEXING new text (their insert stays
// valid) and their doc-id reads degrade to NULL instead of erroring, so a rolling
// restart never spams failures. Legacy vaults that still store text in the FTS
// table are migrated in one transaction on open.
// ============================================================================

// ftsExecer is satisfied by both *sql.DB and *sql.Tx, so the helpers work inside
// the background sync's single-writer transaction and on the plain connection.
type ftsExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
}

// ensureFTS creates (or migrates) the contentless lexical index and its rowid map.
// It returns false when FTS5 is unavailable or the legacy migration could not run:
// callers then skip the local lexical index instead of silently writing into a
// table whose schema does not match.
func ensureFTS(db *sql.DB) bool {
	if db == nil {
		return false
	}
	var ftsDDL string
	var hasMap int
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'thin_vault_docs_fts'`).Scan(&ftsDDL); err == nil &&
		(strings.Contains(ftsDDL, "content=''") || strings.Contains(ftsDDL, `content = ''`)) {
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'thin_vault_fts_map'`).Scan(&hasMap); err == nil && hasMap == 1 {
			return true
		}
	}
	if err := migrateLegacyFTS(db); err != nil {
		return false
	}
	if _, err := db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS thin_vault_docs_fts USING fts5(raw_text, doc_id UNINDEXED, content='', contentless_delete=1, tokenize = 'trigram')`); err != nil {
		return false
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS thin_vault_fts_map (rowid INTEGER PRIMARY KEY AUTOINCREMENT, doc_id TEXT NOT NULL UNIQUE)`); err != nil {
		return false
	}
	return true
}

// migrateLegacyFTS rewrites a content-storing FTS table into the contentless shape.
// Rows whose doc id no longer resolves from `thin_vault_docs` or `thin_vault_bg_chunks`
// are orphans and are not carried over (that is what keeps stale index rows out).
func migrateLegacyFTS(db *sql.DB) error {
	var ddl string
	err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'thin_vault_docs_fts'`).Scan(&ddl)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if strings.Contains(ddl, "content=''") || strings.Contains(ddl, `content = ''`) {
		return nil
	}

	type legacyRow struct {
		docID string
		text  string
	}
	rows, err := db.Query(`SELECT doc_id, raw_text FROM thin_vault_docs_fts`)
	if err != nil {
		return err
	}
	seen := make(map[string]bool, 4096)
	legacy := make([]legacyRow, 0, 4096)
	for rows.Next() {
		var row legacyRow
		if err := rows.Scan(&row.docID, &row.text); err != nil {
			rows.Close()
			return err
		}
		row.docID = strings.TrimSpace(row.docID)
		if row.docID == "" || strings.TrimSpace(row.text) == "" || seen[row.docID] {
			continue
		}
		seen[row.docID] = true
		legacy = append(legacy, row)
	}
	if err := rows.Close(); err != nil {
		return err
	}

	live := make(map[string]bool, 16384)
	if docRows, err := db.Query(`SELECT doc_id FROM thin_vault_docs`); err == nil {
		for docRows.Next() {
			var id string
			if docRows.Scan(&id) == nil {
				live[strings.TrimSpace(id)] = true
			}
		}
		docRows.Close()
	}
	if bgRows, err := db.Query(`SELECT unit_id FROM thin_vault_bg_chunks`); err == nil {
		for bgRows.Next() {
			var id string
			if bgRows.Scan(&id) == nil {
				live[strings.TrimSpace(id)] = true
			}
		}
		bgRows.Close()
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DROP TABLE IF EXISTS thin_vault_fts_migrate`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE VIRTUAL TABLE thin_vault_fts_migrate USING fts5(raw_text, doc_id UNINDEXED, content='', contentless_delete=1, tokenize = 'trigram')`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS thin_vault_fts_map (rowid INTEGER PRIMARY KEY AUTOINCREMENT, doc_id TEXT NOT NULL UNIQUE)`); err != nil {
		return err
	}
	var rowID int64
	for _, row := range legacy {
		if !live[row.docID] {
			continue
		}
		rowID++
		if _, err := tx.Exec(`INSERT OR IGNORE INTO thin_vault_fts_map(rowid, doc_id) VALUES (?, ?)`, rowID, row.docID); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO thin_vault_fts_migrate(rowid, raw_text) VALUES (?, ?)`, rowID, row.text); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DROP TABLE thin_vault_docs_fts`); err != nil {
		return err
	}
	if _, err := tx.Exec(`ALTER TABLE thin_vault_fts_migrate RENAME TO thin_vault_docs_fts`); err != nil {
		return err
	}
	return tx.Commit()
}

// ftsLookupRowID resolves a doc id to its index rowid (false when absent).
func ftsLookupRowID(q ftsExecer, docID string) (int64, bool, error) {
	var rowID int64
	err := q.QueryRow(`SELECT rowid FROM thin_vault_fts_map WHERE doc_id = ?`, docID).Scan(&rowID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return rowID, true, nil
}

// ftsRowIDFor resolves (or assigns) the stable rowid of one doc id.
func ftsRowIDFor(q ftsExecer, docID string) (int64, error) {
	if rowID, ok, err := ftsLookupRowID(q, docID); err != nil || ok {
		return rowID, err
	}
	if _, err := q.Exec(`INSERT OR IGNORE INTO thin_vault_fts_map(doc_id) VALUES (?)`, docID); err != nil {
		return 0, err
	}
	rowID, ok, err := ftsLookupRowID(q, docID)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, errors.New("thinclient: fts rowid map insert did not persist")
	}
	return rowID, nil
}

// ftsReplaceRow indexes one document's text (postings only — no stored copy).
func ftsReplaceRow(q ftsExecer, docID, text string) error {
	docID = strings.TrimSpace(docID)
	if docID == "" {
		return nil
	}
	if strings.TrimSpace(text) == "" {
		return ftsDeleteRow(q, docID)
	}
	rowID, err := ftsRowIDFor(q, docID)
	if err != nil {
		return err
	}
	if _, err := q.Exec(`DELETE FROM thin_vault_docs_fts WHERE rowid = ?`, rowID); err != nil {
		return err
	}
	_, err = q.Exec(`INSERT INTO thin_vault_docs_fts(rowid, raw_text) VALUES (?, ?)`, rowID, text)
	return err
}

// ftsDeleteRow removes one document from the index and its rowid map entry.
func ftsDeleteRow(q ftsExecer, docID string) error {
	docID = strings.TrimSpace(docID)
	if docID == "" {
		return nil
	}
	rowID, ok, err := ftsLookupRowID(q, docID)
	if err != nil || !ok {
		return err
	}
	if _, err := q.Exec(`DELETE FROM thin_vault_docs_fts WHERE rowid = ?`, rowID); err != nil {
		return err
	}
	_, err = q.Exec(`DELETE FROM thin_vault_fts_map WHERE rowid = ?`, rowID)
	return err
}

// ftsDeleteRows removes a batch of documents from the FTS index and its rowid map in two set-based
// statements instead of 3*N individual SQL compilations and FTS5 cursor round-trips.
func ftsDeleteRows(q ftsExecer, docIDs []string) error {
	args := make([]any, 0, len(docIDs))
	for _, id := range docIDs {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			args = append(args, trimmed)
		}
	}
	if len(args) == 0 {
		return nil
	}
	if len(args) == 1 {
		return ftsDeleteRow(q, args[0].(string))
	}
	placeholders := strings.Repeat("?,", len(args))
	placeholders = placeholders[:len(placeholders)-1]
	if _, err := q.Exec(`DELETE FROM thin_vault_docs_fts WHERE rowid IN (SELECT rowid FROM thin_vault_fts_map WHERE doc_id IN (`+placeholders+`))`, args...); err != nil {
		return err
	}
	_, err := q.Exec(`DELETE FROM thin_vault_fts_map WHERE doc_id IN (`+placeholders+`)`, args...)
	return err
}
