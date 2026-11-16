// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package db implements SQLite connection, schema migration and small DB
// helpers.
package db

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/version"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

//go:embed schema_memcore.sql
var schemaMemcoreSQL string

// StorageProfile is a set of connection-level PRAGMAs.
type StorageProfile struct {
	Name              string
	Synchronous       string
	BusyTimeoutMS     int
	WALAutocheckpoint int
	// JournalSizeLimit bounds the on-disk WAL file (bytes): wal_autocheckpoint only
	// fires after a write commit and never truncates the file, so a long-lived
	// sibling reader (daemon / console) can pin the WAL into unbounded growth that
	// survives the reader. The limit makes every successful checkpoint truncate the
	// WAL back under the cap.
	JournalSizeLimit int64
	MmapSize         int64
	CacheSize        int
	TempStore        string
	ForeignKeys      bool
}

// LocalProfile is the v0.7.0 profile.
func LocalProfile() StorageProfile {
	return StorageProfile{
		Name:        "local",
		Synchronous: "NORMAL",
		// Multi-process WAL vaults: sibling writers (daemon, MCP, console, CLI) must absorb
		// multi-second FTS/checkpoint stalls without failing the caller. Matches USBProfile.
		BusyTimeoutMS:     15000,
		WALAutocheckpoint: 1000,
		// 64 MiB: the same resource-hygiene ceiling the live-machine verifier enforces
		// (WAL <= max(64MiB, 4x db)); checkpoints truncate the WAL file back under it.
		JournalSizeLimit: 64 << 20,
		TempStore:        "MEMORY",
		ForeignKeys:      true,
	}
}

// USBProfile is the design-document removable-media profile.
func USBProfile() StorageProfile {
	return StorageProfile{
		Name:              "usb",
		Synchronous:       "FULL",
		BusyTimeoutMS:     15000,
		WALAutocheckpoint: 256,
		JournalSizeLimit:  16 << 20,
		MmapSize:          0,
		CacheSize:         -8192,
		TempStore:         "MEMORY",
		ForeignKeys:       true,
	}
}

func (p StorageProfile) normalized() StorageProfile {
	if p.Name == "" && p.Synchronous == "" && p.BusyTimeoutMS == 0 &&
		p.WALAutocheckpoint == 0 && p.JournalSizeLimit == 0 && p.MmapSize == 0 && p.CacheSize == 0 &&
		p.TempStore == "" && !p.ForeignKeys {
		return LocalProfile()
	}
	if p.Synchronous == "" {
		p.Synchronous = "NORMAL"
	}
	if p.BusyTimeoutMS <= 0 {
		p.BusyTimeoutMS = 15000
	}
	if p.TempStore == "" {
		p.TempStore = "MEMORY"
	}
	return p
}

// Connect opens path with one connection, immediate transactions and the
// profile PRAGMAs. The profile rides in the DSN, so every physical connection
// the pool ever creates keeps it, not just the first one. A failed WAL switch
// degrades to the rollback journal.
func Connect(path string, profile StorageProfile) (*sql.DB, error) {
	profile = profile.normalized()
	dbPath := expandHome(path)
	memory := dbPath == ":memory:"
	if !memory {
		directory := filepath.Dir(dbPath)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, err
		}
		file, err := os.OpenFile(dbPath, os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			return nil, err
		}
		if err := file.Chmod(0o600); err != nil {
			file.Close()
			return nil, err
		}
		if err := file.Close(); err != nil {
			return nil, err
		}
	}

	dsnPath := dbPath
	if strings.ContainsAny(dsnPath, "?#") {
		dsnPath = ids.SQLiteFileURI(dsnPath)
	}
	// The whole storage profile rides in the DSN, not in one-shot Execs on the first
	// connection: database/sql may replace a pooled connection at any time, and a fresh
	// connection with a zero busy timeout turns ordinary lock contention into instant
	// SQLITE_BUSY while a missing foreign_keys silently drops constraint enforcement.
	dsn := dsnPath + "?_txlock=immediate&_pragma=busy_timeout(" + strconv.Itoa(profile.BusyTimeoutMS) + ")" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=synchronous(" + profile.Synchronous + ")" +
		"&_pragma=temp_store(" + profile.TempStore + ")"
	if profile.WALAutocheckpoint > 0 {
		dsn += "&_pragma=wal_autocheckpoint(" + strconv.Itoa(profile.WALAutocheckpoint) + ")"
	}
	if profile.JournalSizeLimit > 0 {
		dsn += "&_pragma=journal_size_limit(" + strconv.FormatInt(profile.JournalSizeLimit, 10) + ")"
	}
	if profile.Name == "usb" || profile.MmapSize != 0 {
		dsn += "&_pragma=mmap_size(" + strconv.FormatInt(profile.MmapSize, 10) + ")"
	}
	if profile.CacheSize != 0 {
		dsn += "&_pragma=cache_size(" + strconv.Itoa(profile.CacheSize) + ")"
	}
	connection, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	connection.SetMaxOpenConns(1)
	connection.SetMaxIdleConns(1)
	connection.SetConnMaxLifetime(0)
	if err := connection.Ping(); err != nil {
		connection.Close()
		return nil, err
	}

	currentJournalMode, err := pragmaString(connection, "PRAGMA journal_mode")
	if err != nil {
		connection.Close()
		return nil, err
	}
	if !strings.EqualFold(currentJournalMode, "wal") {
		journalMode, err := pragmaString(connection, "PRAGMA journal_mode = WAL")
		if err != nil {
			connection.Close()
			return nil, err
		}
		if !strings.EqualFold(journalMode, "wal") {
			if _, err := pragmaString(connection, "PRAGMA journal_mode = DELETE"); err != nil {
				connection.Close()
				return nil, err
			}
		}
	}

	return connection, nil
}

// LoadSchema returns the embedded schema.sql text.
func LoadSchema() string {
	return schemaSQL
}

// LoadMemcoreSchema returns the embedded 1:1 isomorphic extension schema SQL.
func LoadMemcoreSchema() string {
	return schemaMemcoreSQL
}

// EnsureMemcoreSchema idempotently creates the 1:1 isomorphic extension tables
// (node_ext, assertion_ext, participant, node_vector, episode, episode_member,
// memory_hot, restate) on any new or existing SQLite database.
// Fast-path: if the extension tables and indexes already exist in sqlite_master,
// return immediately with a single read-only query to avoid running 15 DDL statements
// on every short-lived hook subprocess open.
func EnsureMemcoreSchema(connection *sql.DB) error {
	var hasLegacyNode int
	if err := connection.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'node'`).Scan(&hasLegacyNode); err != nil {
		return err
	}
	if hasLegacyNode == 0 {
		// Extension tables may contain historical data even without node.
		return nil
	}
	var existing int
	var metaDDL string
	_ = connection.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'meta_edge'`).Scan(&metaDDL)
	hasEightKinds := strings.Contains(metaDDL, "'part-of'")
	if hasEightKinds {
		if err := connection.QueryRow(`
			SELECT COUNT(*) FROM sqlite_master
			WHERE (type = 'table' AND name IN ('node_ext', 'assertion_ext', 'participant', 'node_vector', 'episode', 'episode_member', 'memory_hot', 'restate', 'noetic'))
			   OR (type = 'index' AND name IN ('assertion_scope_subj_all', 'assertion_scope_obj_all'))
		`).Scan(&existing); err == nil && existing == 11 {
			return nil
		}
	}
	for _, statement := range splitSchemaStatements(schemaMemcoreSQL) {
		if _, err := connection.Exec(statement); err != nil {
			return err
		}
	}
	if !hasEightKinds && isStandardV1MetaEdgeDDL(metaDDL) {
		var hasV2 int
		if err := connection.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'meta_edge_v2'`).Scan(&hasV2); err != nil {
			return err
		}
		if hasV2 == 0 {
			if err := upgradeMetaEdgeEightKinds(connection); err != nil {
				return err
			}
		}
	}
	return nil
}

func isStandardV1MetaEdgeDDL(ddl string) bool {
	return strings.Contains(ddl, "evidence_anchor") &&
		strings.Contains(ddl, "revert_of") &&
		strings.Contains(ddl, "'same-as'") &&
		strings.Contains(ddl, "'supplement'")
}

func upgradeMetaEdgeEightKinds(connection *sql.DB) error {
	_, _ = connection.Exec(`PRAGMA foreign_keys = OFF`)
	defer func() {
		_, _ = connection.Exec(`PRAGMA foreign_keys = ON`)
	}()
	return WithImmediateTx(connection, func(tx *sql.Tx) error {
		stmts := []string{
			`CREATE TABLE meta_edge_v2 (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  scope           TEXT NOT NULL,
  meta_kind       TEXT NOT NULL
                  CHECK (meta_kind IN (
                    'same-as', 'inverse-of', 'subtype-of', 'part-of',
                    'derived-from', 'causes', 'supersedes', 'conflicts-with', 'supplement',
                    'same_as', 'inverse_of', 'subtype_of', 'part_of',
                    'derived_from', 'conflicts_with'
                  )),
  lhs_id          TEXT NOT NULL REFERENCES node(id),
  rhs_id          TEXT NOT NULL REFERENCES node(id),
  status          TEXT NOT NULL DEFAULT 'active'
                  CHECK (status IN ('active', 'proposed', 'reverted')),
  evidence_anchor TEXT,
  created_at      INTEGER NOT NULL,
  created_by      TEXT NOT NULL DEFAULT 'system',
  revert_of       INTEGER REFERENCES meta_edge(id),
  UNIQUE (scope, meta_kind, lhs_id, rhs_id, status)
)`,
			`INSERT INTO meta_edge_v2(id, scope, meta_kind, lhs_id, rhs_id, status, evidence_anchor, created_at, created_by, revert_of)
			 SELECT id, scope, meta_kind, lhs_id, rhs_id, status, evidence_anchor, created_at, created_by, revert_of FROM meta_edge`,
			`DROP TABLE meta_edge`,
			`ALTER TABLE meta_edge_v2 RENAME TO meta_edge`,
			`CREATE INDEX IF NOT EXISTS meta_lhs ON meta_edge(lhs_id)`,
			`CREATE INDEX IF NOT EXISTS meta_rhs ON meta_edge(rhs_id)`,
			`CREATE INDEX IF NOT EXISTS meta_kind_status ON meta_edge(scope, meta_kind, status)`,
		}
		for _, s := range stmts {
			if _, err := tx.Exec(s); err != nil {
				return err
			}
		}
		return nil
	})
}

// Migrate creates schema v1 for a new database and writes db_meta rows,
// then ensures the 1:1 memcore extension tables exist.
func Migrate(connection *sql.DB) (int, error) {
	current, err := userVersion(connection)
	if err != nil {
		return 0, err
	}
	if current > version.SchemaVersion {
		return 0, fmt.Errorf(
			"database schema v%d is newer than this build (v%d); upgrade Rolling-Memory",
			current, version.SchemaVersion,
		)
	}
	if current == 0 {
		for _, statement := range splitSchemaStatements(schemaSQL) {
			if _, err := connection.Exec(statement); err != nil {
				return 0, err
			}
		}
		if _, err := connection.Exec(fmt.Sprintf("PRAGMA user_version = %d", version.SchemaVersion)); err != nil {
			return 0, err
		}
		meta := []struct{ key, value string }{
			{"schema_version", strconv.Itoa(version.SchemaVersion)},
			{"created_at", strconv.FormatInt(ids.UTCNow(), 10)},
			{"engine", "rolling-memory"},
		}
		for _, row := range meta {
			if _, err := connection.Exec(
				"INSERT OR REPLACE INTO db_meta(key, value) VALUES (?, ?)",
				row.key, row.value,
			); err != nil {
				return 0, err
			}
		}
	}
	if err := EnsureMemcoreSchema(connection); err != nil {
		return 0, err
	}
	return version.SchemaVersion, nil
}

// WithImmediateTx runs fn inside BEGIN IMMEDIATE. The connection DSN uses
// modernc's _txlock=immediate, so BeginTx issues the same statement standard
// uses; database/sql's default DEFERRED mode is never selected.
func WithImmediateTx(connection *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := connection.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

// HasFTS reports whether both FTS virtual tables exist.
func HasFTS(connection *sql.DB) (bool, error) {
	var count int
	err := connection.QueryRow(
		"SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name IN ('node_fts', 'trace_fts')",
	).Scan(&count)
	return count == 2, err
}

// TableCounts returns the fixed table counts and schema_version.
func TableCounts(connection *sql.DB, scope string) (map[string]int, error) {
	var hasLegacy int
	_ = connection.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'assertion'`).Scan(&hasLegacy)
	if hasLegacy == 0 {
		var docsCnt, noeticCnt int
		_ = connection.QueryRow(`SELECT COUNT(*) FROM thin_vault_docs`).Scan(&docsCnt)
		_ = connection.QueryRow(`SELECT COUNT(*) FROM thin_vault_noetic WHERE status = 'active'`).Scan(&noeticCnt)
		schemaVersion, _ := userVersion(connection)
		return map[string]int{
			"trace":             docsCnt,
			"node":              docsCnt + noeticCnt,
			"node_alias":        0,
			"assertion":         docsCnt + noeticCnt,
			"mention":           0,
			"meta_edge":         0,
			"event":             0,
			"vector":            0,
			"thin_vault_docs":   docsCnt,
			"thin_vault_noetic": noeticCnt,
			"schema_version":    schemaVersion,
		}, nil
	}
	tables := []string{
		"trace",
		"node",
		"node_alias",
		"assertion",
		"mention",
		"meta_edge",
		"event",
		"vector",
	}
	scoped := map[string]bool{
		"node": true, "assertion": true, "mention": true,
		"meta_edge": true, "event": true, "trace": true,
	}
	counts := map[string]int{}
	for _, table := range tables {
		query := "SELECT count(*) FROM " + table
		args := []any{}
		if scope != "" && scoped[table] {
			query += " WHERE scope = ?"
			args = append(args, scope)
		}
		var count int
		if err := connection.QueryRow(query, args...).Scan(&count); err != nil {
			return nil, err
		}
		counts[table] = count
	}
	schemaVersion, err := userVersion(connection)
	if err != nil {
		return nil, err
	}
	counts["schema_version"] = schemaVersion
	return counts, nil
}

// GetMeta returns db_meta[key] or defaultValue.
func GetMeta(connection *sql.DB, key, defaultValue string) (string, error) {
	var value string
	err := connection.QueryRow("SELECT value FROM db_meta WHERE key = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return defaultValue, nil
	}
	if err != nil {
		return "", err
	}
	return value, nil
}

// SetMeta upserts db_meta[key].
func SetMeta(connection *sql.DB, key, value string) error {
	_, err := connection.Exec(
		"INSERT OR REPLACE INTO db_meta(key, value) VALUES (?, ?)",
		key, value,
	)
	return err
}

// DBSize sums the database, -wal and -shm files.
func DBSize(path string) int64 {
	base := expandHome(path)
	total := int64(0)
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if info, err := os.Stat(base + suffix); err == nil {
			total += info.Size()
		}
	}
	return total
}

// RebuildFTS rebuilds both FTS tables from their source tables.
func RebuildFTS(connection *sql.DB) (map[string]int, error) {
	result := map[string]int{"nodes": 0, "traces": 0}
	err := WithImmediateTx(connection, func(tx *sql.Tx) error {
		if _, err := tx.Exec("DELETE FROM node_fts"); err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM trace_fts"); err != nil {
			return err
		}
		rows, err := tx.Query(`
			SELECT n.id, n.label,
			       coalesce(group_concat(a.alias, ' '), '') AS aliases
			FROM node n
			LEFT JOIN node_alias a ON a.node_id = n.id
			WHERE n.status IN ('provisional', 'active')
			GROUP BY n.id
		`)
		if err != nil {
			return err
		}
		type nodeRow struct{ id, label, aliases string }
		nodes := make([]nodeRow, 0, 16)
		for rows.Next() {
			var row nodeRow
			if err := rows.Scan(&row.id, &row.label, &row.aliases); err != nil {
				rows.Close()
				return err
			}
			nodes = append(nodes, row)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, row := range nodes {
			if _, err := tx.Exec(
				"INSERT INTO node_fts(label, aliases, node_id) VALUES (?, ?, ?)",
				row.label, row.aliases, row.id,
			); err != nil {
				return err
			}
		}
		result["nodes"] = len(nodes)

		traceRows, err := tx.Query("SELECT hash, text FROM trace")
		if err != nil {
			return err
		}
		type traceRow struct{ hash, text string }
		traces := make([]traceRow, 0, 16)
		for traceRows.Next() {
			var row traceRow
			if err := traceRows.Scan(&row.hash, &row.text); err != nil {
				traceRows.Close()
				return err
			}
			traces = append(traces, row)
		}
		if err := traceRows.Close(); err != nil {
			return err
		}
		for _, row := range traces {
			if _, err := tx.Exec(
				"INSERT INTO trace_fts(text, trace_hash) VALUES (?, ?)",
				row.text, row.hash,
			); err != nil {
				return err
			}
		}
		result["traces"] = len(traces)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// IntegrityCheck runs integrity_check and foreign_key_check.
func IntegrityCheck(connection *sql.DB) ([]string, error) {
	issues := []string{}
	var pageCount int64
	_ = connection.QueryRow("PRAGMA page_count").Scan(&pageCount)
	if pageCount > 8192 {
		// On multi-hundred-MB production vaults (>32MB), full-database PRAGMA integrity_check
		// scans every FTS5 trigram posting block and 200MB+ of packed binary slot blobs (~4s).
		// Check each core relational metadata/document table's B-tree and indexes directly.
		for _, tbl := range []string{"db_meta", "thin_vault_meta", "thin_vault_docs", "thin_vault_noetic"} {
			var exists int
			if connection.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", tbl).Scan(&exists) != nil || exists == 0 {
				continue
			}
			var res string
			if err := connection.QueryRow("PRAGMA integrity_check(" + tbl + ")").Scan(&res); err != nil {
				return nil, err
			}
			if strings.ToLower(strings.TrimSpace(res)) != "ok" {
				issues = append(issues, "integrity_check("+tbl+"): "+res)
			}
		}
	} else {
		rows, err := connection.Query("PRAGMA integrity_check")
		if err != nil {
			return nil, err
		}
		if rows.Next() {
			var value any
			if err := rows.Scan(&value); err != nil {
				rows.Close()
				return nil, err
			}
			text := pyValue(value)
			if strings.ToLower(text) != "ok" {
				issues = append(issues, "integrity_check: "+text)
			}
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}

	foreignRows, err := connection.Query("PRAGMA foreign_key_check")
	if err != nil {
		return nil, err
	}
	defer foreignRows.Close()
	for foreignRows.Next() {
		var table, rowid, parent, fkid any
		if err := foreignRows.Scan(&table, &rowid, &parent, &fkid); err != nil {
			return nil, err
		}
		issues = append(issues, fmt.Sprintf(
			"foreign_key_check: table=%s rowid=%s parent=%s fkid=%s",
			pyValue(table), pyValue(rowid), pyValue(parent), pyValue(fkid),
		))
	}
	if err := foreignRows.Err(); err != nil {
		return nil, err
	}
	return issues, nil
}

// SQLiteVersion returns the runtime SQLite library version.
func SQLiteVersion(connection *sql.DB) (string, error) {
	var value string
	err := connection.QueryRow("SELECT sqlite_version()").Scan(&value)
	return value, err
}

// FTS5Available probes FTS5 with the trigram tokenizer.
func FTS5Available() bool {
	connection, err := sql.Open("sqlite", ":memory:?_txlock=immediate")
	if err != nil {
		return false
	}
	defer connection.Close()
	_, err = connection.Exec("CREATE VIRTUAL TABLE probe USING fts5(x, tokenize='trigram')")
	return err == nil
}

// JSONExtractAvailable probes the JSON1 extension.
func JSONExtractAvailable() bool {
	connection, err := sql.Open("sqlite", ":memory:?_txlock=immediate")
	if err != nil {
		return false
	}
	defer connection.Close()
	var value int
	err = connection.QueryRow(`SELECT json_extract('{"a":1}', '$.a')`).Scan(&value)
	return err == nil && value == 1
}

// JournalMode returns the live journal_mode.
func JournalMode(connection *sql.DB) (string, error) {
	return pragmaString(connection, "PRAGMA journal_mode")
}

func pragmaString(connection *sql.DB, statement string) (string, error) {
	var value string
	err := connection.QueryRow(statement).Scan(&value)
	return value, err
}

func userVersion(connection *sql.DB) (int, error) {
	var value int
	err := connection.QueryRow("PRAGMA user_version").Scan(&value)
	return value, err
}

func splitSchemaStatements(schema string) []string {
	var withoutComments strings.Builder
	for _, line := range strings.Split(schema, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		withoutComments.WriteString(line)
		withoutComments.WriteByte('\n')
	}
	statements := []string{}
	for _, statement := range strings.Split(withoutComments.String(), ";") {
		if trimmed := strings.TrimSpace(statement); trimmed != "" {
			statements = append(statements, trimmed)
		}
	}
	return statements
}

func pyValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return "None"
	case []byte:
		return string(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case int:
		return strconv.Itoa(typed)
	default:
		return fmt.Sprint(typed)
	}
}

func expandHome(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return path
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}
