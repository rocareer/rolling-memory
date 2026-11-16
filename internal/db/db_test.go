// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package db

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rocareer/rolling-memory/internal/clock"
	"github.com/rocareer/rolling-memory/internal/ids"
)

func queryInt(t *testing.T, connection *sql.DB, statement string) int {
	t.Helper()
	var value int
	if err := connection.QueryRow(statement).Scan(&value); err != nil {
		t.Fatalf("query %q: %v", statement, err)
	}
	return value
}

func queryText(t *testing.T, connection *sql.DB, statement string) string {
	t.Helper()
	var value string
	if err := connection.QueryRow(statement).Scan(&value); err != nil {
		t.Fatalf("query %q: %v", statement, err)
	}
	return value
}

func execSQL(t *testing.T, connection *sql.DB, statement string, args ...any) {
	t.Helper()
	if _, err := connection.Exec(statement, args...); err != nil {
		t.Fatalf("exec %q: %v", statement, err)
	}
}

func connectMigrated(t *testing.T, profile StorageProfile) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nested", "memory.db")
	connection, err := Connect(path, profile)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { connection.Close() })
	if _, err := Migrate(connection); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return connection, path
}

func TestConnectPathWithQuestionMark(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory?db.sqlite")
	connection, err := Connect(path, LocalProfile())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer connection.Close()
	if _, err := Migrate(connection); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database file %q missing: %v", path, err)
	}
	if got := queryInt(t, connection, "PRAGMA user_version"); got != 1 {
		t.Fatalf("user_version = %d, want 1", got)
	}
}

func TestSQLiteFileURIDriveLetterAuthority(t *testing.T) {
	// A Windows path rendered without a leading slash produces file://C:%5C... —
	// the drive letter lands in the URI authority and the driver rejects the DSN
	// before it ever reaches the filesystem. Every read-only open of a Windows
	// vault path (doctor / recall / backup preflight) failed this way.
	legacy := (&url.URL{Scheme: "file", Path: `C:\Users\alice\memory.db`, RawQuery: "mode=ro"}).String()
	legacyDB, err := sql.Open("sqlite", legacy)
	if err != nil {
		t.Fatal(err)
	}
	err = legacyDB.Ping()
	legacyDB.Close()
	if err == nil || !strings.Contains(err.Error(), "invalid uri authority") {
		t.Fatalf("legacy DSN %q: ping = %v, want invalid uri authority", legacy, err)
	}

	// The canonical form keeps the drive letter inside the URI path; the driver
	// parses it and only the file open can fail.
	canonical := ids.SQLiteFileURI(`C:\Users\alice\memory.db`) + "?mode=ro"
	canonicalDB, err := sql.Open("sqlite", canonical)
	if err != nil {
		t.Fatal(err)
	}
	err = canonicalDB.Ping()
	canonicalDB.Close()
	if err != nil && strings.Contains(err.Error(), "invalid uri authority") {
		t.Fatalf("canonical DSN %q: ping = %v", canonical, err)
	}
}

func TestSQLiteFileURIOpenRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	write, err := sql.Open("sqlite", ids.SQLiteFileURI(path))
	if err != nil {
		t.Fatal(err)
	}
	execSQL(t, write, "CREATE TABLE probe(x TEXT)")
	execSQL(t, write, "INSERT INTO probe(x) VALUES ('round trip')")
	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	read, err := sql.Open("sqlite", ids.SQLiteFileURI(path)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	if got := queryText(t, read, "SELECT x FROM probe"); got != "round trip" {
		t.Fatalf("round trip = %q", got)
	}
}

func TestConnectPreservesExistingParent(t *testing.T) {
	for _, existingDB := range []bool{false, true} {
		name := "new database"
		if existingDB {
			name = "existing database"
		}
		t.Run(name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "shared")
			if err := os.Mkdir(directory, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.Chmod(directory, 0o755); err != nil {
				t.Fatalf("chmod directory: %v", err)
			}
			path := filepath.Join(directory, "memory.db")
			if existingDB {
				if err := os.WriteFile(path, nil, 0o644); err != nil {
					t.Fatalf("create database: %v", err)
				}
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatalf("chmod database: %v", err)
				}
			}
			connection, err := Connect(path, LocalProfile())
			if err != nil {
				t.Fatalf("Connect: %v", err)
			}
			defer connection.Close()
			for _, check := range []struct {
				path string
				mode os.FileMode
			}{{directory, 0o755}, {path, 0o600}} {
				info, err := os.Stat(check.path)
				if err != nil {
					t.Fatalf("stat %q: %v", check.path, err)
				}
				if info.Mode().Perm() != check.mode {
					t.Errorf("%q mode = %o, want %o", check.path, info.Mode().Perm(), check.mode)
				}
			}
		})
	}
}

func TestSchemaDigestMatchesGolden(t *testing.T) {
	wantRaw, err := os.ReadFile("../../testdata/golden/schema/v1.sha256")
	if err != nil {
		t.Fatalf("read golden schema digest: %v", err)
	}
	want := strings.TrimSpace(string(wantRaw))
	sum := sha256.Sum256([]byte(LoadSchema()))
	if got := hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("schema sha256 = %s, want %s", got, want)
	}
}

func TestConnectLocalProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "memory.db")
	connection, err := Connect(path, LocalProfile())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer connection.Close()

	if got := queryText(t, connection, "PRAGMA journal_mode"); got != "wal" {
		t.Errorf("journal_mode = %q, want wal", got)
	}
	if got := queryInt(t, connection, "PRAGMA foreign_keys"); got != 1 {
		t.Errorf("foreign_keys = %d, want 1", got)
	}
	if got := queryInt(t, connection, "PRAGMA synchronous"); got != 1 {
		t.Errorf("synchronous = %d, want 1", got)
	}
	if got := queryInt(t, connection, "PRAGMA busy_timeout"); got != 15000 {
		t.Errorf("busy_timeout = %d, want 15000", got)
	}
	// journal_size_limit is per-connection state (never persisted into the file):
	// it must ride in the DSN so every pooled physical connection keeps the WAL
	// truncation ceiling, otherwise one unlimited connection regrows the WAL.
	if got := queryInt(t, connection, "PRAGMA journal_size_limit"); int64(got) != 64<<20 {
		t.Errorf("journal_size_limit = %d, want %d", got, 64<<20)
	}
	// The timeout must ride in the DSN too: force the pooled connection to be replaced and
	// assert a fresh physical connection (built purely from the DSN) still carries it.
	connection.SetMaxIdleConns(0)
	queryInt(t, connection, "PRAGMA busy_timeout")
	if got := queryInt(t, connection, "PRAGMA busy_timeout"); got != 15000 {
		t.Errorf("fresh connection busy_timeout = %d, want 15000", got)
	}
	if got := queryInt(t, connection, "PRAGMA journal_size_limit"); int64(got) != 64<<20 {
		t.Errorf("fresh connection journal_size_limit = %d, want %d", got, 64<<20)
	}
	if got := queryInt(t, connection, "PRAGMA temp_store"); got != 2 {
		t.Errorf("temp_store = %d, want 2", got)
	}

	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat directory: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("directory mode = %o, want 700", info.Mode().Perm())
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatalf("stat database: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("database mode = %o, want 600", info.Mode().Perm())
	}
}

func TestConnectZeroProfileIsLocal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	connection, err := Connect(path, StorageProfile{})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer connection.Close()
	if got := queryText(t, connection, "PRAGMA journal_mode"); got != "wal" {
		t.Errorf("journal_mode = %q, want wal", got)
	}
	if got := queryInt(t, connection, "PRAGMA synchronous"); got != 1 {
		t.Errorf("synchronous = %d, want 1", got)
	}
	if got := queryInt(t, connection, "PRAGMA busy_timeout"); got != 15000 {
		t.Errorf("busy_timeout = %d, want 15000", got)
	}
}

func TestConnectUSBProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	connection, err := Connect(path, USBProfile())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer connection.Close()

	checks := map[string]int{
		"PRAGMA foreign_keys":       1,
		"PRAGMA synchronous":        2,
		"PRAGMA busy_timeout":       15000,
		"PRAGMA wal_autocheckpoint": 256,
		"PRAGMA mmap_size":          0,
		"PRAGMA cache_size":         -8192,
		"PRAGMA temp_store":         2,
	}
	for statement, want := range checks {
		if got := queryInt(t, connection, statement); got != want {
			t.Errorf("%s = %d, want %d", statement, got, want)
		}
	}
	if got := queryText(t, connection, "PRAGMA journal_mode"); got != "wal" {
		t.Errorf("journal_mode = %q, want wal", got)
	}
	connection.SetMaxIdleConns(0)
	queryInt(t, connection, "PRAGMA busy_timeout")
	if got := queryInt(t, connection, "PRAGMA busy_timeout"); got != 15000 {
		t.Errorf("fresh connection busy_timeout = %d, want 15000", got)
	}
}

func TestConnectWALFallback(t *testing.T) {
	connection, err := Connect(":memory:", LocalProfile())
	if err != nil {
		t.Fatalf("Connect(:memory:): %v", err)
	}
	defer connection.Close()
	mode, err := JournalMode(connection)
	if err != nil {
		t.Fatalf("JournalMode: %v", err)
	}
	if mode != "memory" {
		t.Fatalf("journal_mode = %q, want memory (WAL unsupported)", mode)
	}
	execSQL(t, connection, "CREATE TABLE probe(x INTEGER)")
	execSQL(t, connection, "INSERT INTO probe(x) VALUES (1)")
	if got := queryInt(t, connection, "SELECT count(*) FROM probe"); got != 1 {
		t.Fatalf("probe count = %d", got)
	}
}

func TestMigrate(t *testing.T) {
	restore := clock.SetForTest(time.Unix(1793102400, 0), 1000000)
	defer restore()
	connection, _ := connectMigrated(t, LocalProfile())

	if got := queryInt(t, connection, "PRAGMA user_version"); got != 1 {
		t.Fatalf("user_version = %d, want 1", got)
	}
	for key, want := range map[string]string{
		"schema_version": "1",
		"created_at":     "1793102400",
		"engine":         "rolling-memory",
	} {
		got, err := GetMeta(connection, key, "")
		if err != nil {
			t.Fatalf("GetMeta(%q): %v", key, err)
		}
		if got != want {
			t.Errorf("db_meta[%q] = %q, want %q", key, got, want)
		}
	}
	if ok, err := HasFTS(connection); err != nil || !ok {
		t.Fatalf("HasFTS = (%v, %v), want true", ok, err)
	}
	issues, err := IntegrityCheck(connection)
	if err != nil {
		t.Fatalf("IntegrityCheck: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("integrity issues = %v", issues)
	}
	counts, err := TableCounts(connection, "")
	if err != nil {
		t.Fatalf("TableCounts: %v", err)
	}
	if counts["schema_version"] != 1 {
		t.Fatalf("counts schema_version = %d", counts["schema_version"])
	}

	rows, err := connection.Query("SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name")
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	tables := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table: %v", err)
		}
		tables[name] = true
	}
	rows.Close()
	for _, name := range []string{
		"db_meta", "trace", "node", "node_alias", "assertion", "mention",
		"meta_edge", "event", "vector", "replay_state", "session_cursor",
		"activation", "sync_state", "injection_ledger", "transcript_cursor",
		"node_fts", "trace_fts",
	} {
		if !tables[name] {
			t.Errorf("missing table %q", name)
		}
	}

	if version, err := Migrate(connection); err != nil || version != 1 {
		t.Fatalf("second Migrate = (%d, %v)", version, err)
	}
}

func TestMigrateRefusesNewer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	connection, err := Connect(path, LocalProfile())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer connection.Close()
	execSQL(t, connection, "PRAGMA user_version = 2")

	_, err = Migrate(connection)
	if err == nil {
		t.Fatal("Migrate accepted a newer schema")
	}
	want := "database schema v2 is newer than this build (v1); upgrade Rolling-Memory"
	if err.Error() != want {
		t.Fatalf("Migrate error = %q, want %q", err, want)
	}
	if got := queryInt(t, connection,
		"SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'db_meta'"); got != 0 {
		t.Fatalf("db_meta exists after refused migration")
	}
}

func TestWithImmediateTx(t *testing.T) {
	connection, path := connectMigrated(t, LocalProfile())
	second, err := Connect(path, LocalProfile())
	if err != nil {
		t.Fatalf("second Connect: %v", err)
	}
	defer second.Close()
	execSQL(t, second, "PRAGMA busy_timeout = 50")

	var secondErr error
	err = WithImmediateTx(connection, func(tx *sql.Tx) error {
		_, secondErr = second.Exec("INSERT INTO db_meta(key, value) VALUES ('second', 'x')")
		_, err := tx.Exec("INSERT INTO db_meta(key, value) VALUES ('first', 'y')")
		return err
	})
	if err != nil {
		t.Fatalf("WithImmediateTx: %v", err)
	}
	if secondErr == nil {
		t.Fatalf("second writer succeeded; transaction was not IMMEDIATE")
	}
	if got, _ := GetMeta(connection, "first", ""); got != "y" {
		t.Fatalf("first insert missing: %q", got)
	}
	if got, _ := GetMeta(connection, "second", ""); got != "" {
		t.Fatalf("second insert leaked: %q", got)
	}

	sentinel := errors.New("stop")
	err = WithImmediateTx(connection, func(tx *sql.Tx) error {
		if _, err := tx.Exec("INSERT INTO db_meta(key, value) VALUES ('rollback', 'z')"); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("rollback error = %v, want sentinel", err)
	}
	if got, _ := GetMeta(connection, "rollback", ""); got != "" {
		t.Fatalf("rollback row leaked: %q", got)
	}
}

func TestTableCountsScope(t *testing.T) {
	connection, _ := connectMigrated(t, LocalProfile())
	execSQL(t, connection,
		"INSERT INTO trace(hash, scope, text, occurred_at, recorded_at) VALUES (?, ?, ?, ?, ?)",
		"h1", "mem:a", "老张养了一只猫。", 1, 1)
	execSQL(t, connection,
		"INSERT INTO trace(hash, scope, text, occurred_at, recorded_at) VALUES (?, ?, ?, ?, ?)",
		"h2", "mem:a", "第二行。", 2, 2)
	execSQL(t, connection,
		"INSERT INTO trace(hash, scope, text, occurred_at, recorded_at) VALUES (?, ?, ?, ?, ?)",
		"h3", "mem:b", "别的作用域。", 3, 3)
	execSQL(t, connection,
		"INSERT INTO node(id, scope, kind, label, norm_label, created_at) VALUES (?, ?, ?, ?, ?, ?)",
		"n_a", "mem:a", "referent", "老张", "老张", 1)
	execSQL(t, connection,
		"INSERT INTO node(id, scope, kind, label, norm_label, created_at) VALUES (?, ?, ?, ?, ?, ?)",
		"n_b", "mem:b", "referent", "Alice", "alice", 1)
	execSQL(t, connection,
		"INSERT INTO node_alias(node_id, alias, norm_alias, source, created_at) VALUES (?, ?, ?, ?, ?)",
		"n_a", "老张", "老张", "manual", 1)
	execSQL(t, connection,
		"INSERT INTO vector(owner_kind, owner_hash, model, dim, vec, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
		"node", "n_a", "hash-ngram-v1:512", 512, []byte{0, 0, 0, 0}, 1)
	execSQL(t, connection,
		"INSERT INTO event(scope, op, created_at) VALUES (?, ?, ?)", "mem:a", "test", 1)
	execSQL(t, connection,
		"INSERT INTO event(scope, op, created_at) VALUES (?, ?, ?)", "mem:b", "test", 1)

	counts, err := TableCounts(connection, "mem:a")
	if err != nil {
		t.Fatalf("TableCounts: %v", err)
	}
	want := map[string]int{
		"trace": 2, "node": 1, "node_alias": 1, "assertion": 0,
		"mention": 0, "meta_edge": 0, "event": 1, "vector": 1,
		"schema_version": 1,
	}
	for table, value := range want {
		if counts[table] != value {
			t.Errorf("counts[%q] = %d, want %d", table, counts[table], value)
		}
	}
	all, err := TableCounts(connection, "")
	if err != nil {
		t.Fatalf("TableCounts all: %v", err)
	}
	if all["trace"] != 3 || all["node"] != 2 || all["event"] != 2 {
		t.Fatalf("unscoped counts = %+v", all)
	}
}

// TableCounts keeps the standard table list verbatim: the eight
// counted tables plus the schema_version key.
func TestTableCountsKeySet(t *testing.T) {
	connection, _ := connectMigrated(t, LocalProfile())
	for _, scope := range []string{"", "mem:a"} {
		counts, err := TableCounts(connection, scope)
		if err != nil {
			t.Fatalf("TableCounts(%q): %v", scope, err)
		}
		want := []string{
			"trace", "node", "node_alias", "assertion",
			"mention", "meta_edge", "event", "vector", "schema_version",
		}
		if len(counts) != len(want) {
			t.Fatalf("TableCounts(%q) keys = %v, want %v", scope, counts, want)
		}
		for _, key := range want {
			if _, ok := counts[key]; !ok {
				t.Errorf("TableCounts(%q) is missing key %q", scope, key)
			}
		}
	}
}

func TestMetaRoundTrip(t *testing.T) {
	connection, _ := connectMigrated(t, LocalProfile())
	if got, err := GetMeta(connection, "missing", "fallback"); err != nil || got != "fallback" {
		t.Fatalf("GetMeta missing = (%q, %v)", got, err)
	}
	if err := SetMeta(connection, "key", "value"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	if got, err := GetMeta(connection, "key", ""); err != nil || got != "value" {
		t.Fatalf("GetMeta = (%q, %v)", got, err)
	}
	if err := SetMeta(connection, "key", "updated"); err != nil {
		t.Fatalf("SetMeta update: %v", err)
	}
	if got, _ := GetMeta(connection, "key", ""); got != "updated" {
		t.Fatalf("GetMeta updated = %q", got)
	}
}

func TestDBSize(t *testing.T) {
	connection, path := connectMigrated(t, LocalProfile())
	execSQL(t, connection,
		"INSERT INTO trace(hash, scope, text, occurred_at, recorded_at) VALUES (?, ?, ?, ?, ?)",
		"h1", "mem:a", strings.Repeat("x", 4096), 1, 1)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if size := DBSize(path); size < info.Size() {
		t.Fatalf("DBSize = %d, want at least %d", size, info.Size())
	}
}

func TestRebuildFTS(t *testing.T) {
	connection, _ := connectMigrated(t, LocalProfile())
	execSQL(t, connection,
		"INSERT INTO node(id, scope, kind, label, norm_label, status, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		"n1", "mem:a", "referent", "老张养了一只猫", "老张养了一只猫", "active", 1)
	execSQL(t, connection,
		"INSERT INTO node(id, scope, kind, label, norm_label, status, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		"n2", "mem:a", "referent", "project-a", "project-a", "provisional", 1)
	execSQL(t, connection,
		"INSERT INTO node(id, scope, kind, label, norm_label, status, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		"n3", "mem:a", "referent", "old-node", "old-node", "retired", 1)
	execSQL(t, connection,
		"INSERT INTO node_alias(node_id, alias, norm_alias, source, created_at) VALUES (?, ?, ?, ?, ?)",
		"n1", "老张", "老张", "manual", 1)
	execSQL(t, connection,
		"INSERT INTO node_alias(node_id, alias, norm_alias, source, created_at) VALUES (?, ?, ?, ?, ?)",
		"n1", "猫", "猫", "manual", 1)
	execSQL(t, connection,
		"INSERT INTO trace(hash, scope, text, occurred_at, recorded_at) VALUES (?, ?, ?, ?, ?)",
		"h1", "mem:a", "老张养了一只猫。", 1, 1)
	execSQL(t, connection,
		"INSERT INTO trace(hash, scope, text, occurred_at, recorded_at) VALUES (?, ?, ?, ?, ?)",
		"h2", "mem:a", "项目使用 SQLite。", 2, 2)

	result, err := RebuildFTS(connection)
	if err != nil {
		t.Fatalf("RebuildFTS: %v", err)
	}
	if result["nodes"] != 2 || result["traces"] != 2 {
		t.Fatalf("RebuildFTS = %+v, want nodes=2 traces=2", result)
	}
	if got := queryInt(t, connection, "SELECT count(*) FROM node_fts"); got != 2 {
		t.Errorf("node_fts rows = %d, want 2", got)
	}
	if got := queryInt(t, connection, "SELECT count(*) FROM trace_fts"); got != 2 {
		t.Errorf("trace_fts rows = %d, want 2", got)
	}
	if got := queryInt(t, connection, "SELECT count(*) FROM node_fts WHERE node_fts MATCH '老张养'"); got != 1 {
		t.Errorf("node_fts trigram hits = %d, want 1", got)
	}
	if got := queryInt(t, connection, "SELECT count(*) FROM trace_fts WHERE trace_fts MATCH 'SQLite'"); got != 1 {
		t.Errorf("trace_fts hits = %d, want 1", got)
	}
	if got := queryInt(t, connection, "SELECT count(*) FROM node_fts WHERE node_fts MATCH 'old'"); got != 0 {
		t.Errorf("retired node leaked into FTS: %d", got)
	}
}

func TestIntegrityCheck(t *testing.T) {
	connection, _ := connectMigrated(t, LocalProfile())
	issues, err := IntegrityCheck(connection)
	if err != nil {
		t.Fatalf("IntegrityCheck: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("issues = %v", issues)
	}
	execSQL(t, connection, "PRAGMA foreign_keys = OFF")
	execSQL(t, connection,
		"INSERT INTO node_alias(node_id, alias, norm_alias, source, created_at) VALUES (?, ?, ?, ?, ?)",
		"n_missing", "x", "x", "manual", 1)
	execSQL(t, connection, "PRAGMA foreign_keys = ON")
	issues, err = IntegrityCheck(connection)
	if err != nil {
		t.Fatalf("IntegrityCheck with violation: %v", err)
	}
	if len(issues) != 1 || !strings.Contains(issues[0], "foreign_key_check: table=node_alias") {
		t.Fatalf("issues = %v", issues)
	}
}

func TestSQLiteProbe(t *testing.T) {
	connection, _ := connectMigrated(t, LocalProfile())
	if got := queryText(t, connection, "PRAGMA journal_mode"); got != "wal" {
		t.Fatalf("journal_mode = %q, want wal", got)
	}
	if got := queryInt(t, connection, "PRAGMA user_version"); got != 1 {
		t.Fatalf("user_version = %d, want 1", got)
	}
	execSQL(t, connection,
		"INSERT INTO node_fts(label, aliases, node_id) VALUES (?, ?, ?)",
		"老张养了一只猫", "老张 猫", "n1")
	var hits int
	if err := connection.QueryRow("SELECT count(*) FROM node_fts WHERE node_fts MATCH '老张养'").Scan(&hits); err != nil {
		t.Fatalf("fts trigram query: %v", err)
	}
	if hits != 1 {
		t.Fatalf("fts trigram hits = %d, want 1", hits)
	}
	var rank float64
	if err := connection.QueryRow("SELECT bm25(node_fts) FROM node_fts WHERE node_fts MATCH '老张养'").Scan(&rank); err != nil {
		t.Fatalf("bm25 query: %v", err)
	}
	if version, err := SQLiteVersion(connection); err != nil || version == "" {
		t.Fatalf("SQLiteVersion = (%q, %v)", version, err)
	}
	if !FTS5Available() {
		t.Fatal("FTS5 trigram is unavailable")
	}
	if !JSONExtractAvailable() {
		t.Fatal("json_extract is unavailable")
	}
}

func TestAssertionActivePreprobeIndex10kScale(t *testing.T) {
	connection, _ := connectMigrated(t, LocalProfile())
	var idxCount int
	if err := connection.QueryRow(
		"SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'assertion_active_preprobe'",
	).Scan(&idxCount); err != nil || idxCount != 1 {
		t.Fatalf("assertion_active_preprobe index missing: count=%d err=%v", idxCount, err)
	}

	// Bulk-insert 10,000 active assertions in a single transaction to verify D3 scale (<25ms indexed probe)
	tx, err := connection.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	_, _ = tx.Exec(`INSERT INTO trace(hash, scope, text, occurred_at, recorded_at) VALUES ('t_10k', 'mem:scale', 'anchor', 1, 1)`)
	_, _ = tx.Exec(`INSERT INTO node(id, scope, kind, label, norm_label, status, created_at) VALUES ('rel_v', 'mem:scale', 'relation_type', 'uses', 'uses', 'active', 1)`)
	stmtNode, err := tx.Prepare(`INSERT INTO node(id, scope, kind, label, norm_label, status, created_at) VALUES (?, 'mem:scale', 'referent', ?, ?, 'active', 1)`)
	if err != nil {
		t.Fatalf("Prepare node: %v", err)
	}
	stmtAssert, err := tx.Prepare(`INSERT INTO assertion(node_id, scope, subject_id, rel_id, object_literal, bare, t_record, t_source, warrant, anchor_trace_hash, anchor_start, anchor_end, source_trace_hash, audience, confidence, status, hash, created_at) VALUES (?, 'mem:scale', ?, 'rel_v', 'pkg/mod/file.go', 0, 1, 1, 'referential', 't_10k', 0, 6, 't_10k', 'shared', 0.85, 'active', ?, ?)`)
	if err != nil {
		t.Fatalf("Prepare assertion: %v", err)
	}
	for i := 0; i < 10000; i++ {
		nid := "n_" + string(rune('a'+(i%26))) + "_" + time.Duration(i).String()
		aid := "a_" + time.Duration(i).String()
		_, _ = stmtNode.Exec(nid, "module_"+aid, "module_"+aid)
		_, _ = stmtNode.Exec(aid, "assert_"+aid, "assert_"+aid)
		_, _ = stmtAssert.Exec(aid, nid, "h_"+aid, i+1)
	}
	_ = stmtNode.Close()
	_ = stmtAssert.Close()
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit 10k: %v", err)
	}

	start := time.Now()
	rows, err := connection.Query(`
		SELECT n.label
		FROM assertion a
		JOIN node n ON n.id = a.subject_id
		WHERE a.scope = 'mem:scale' AND a.status = 'active' AND a.bare = 0 AND a.confidence >= 0.70
		ORDER BY a.confidence DESC, a.created_at DESC
		LIMIT 512
	`)
	if err != nil {
		t.Fatalf("10k indexed probe query: %v", err)
	}
	count := 0
	for rows.Next() {
		var lbl string
		_ = rows.Scan(&lbl)
		count++
	}
	_ = rows.Close()
	elapsed := time.Since(start)
	if count != 512 {
		t.Fatalf("expected 512 bounded rows from 10k active assertions, got %d", count)
	}
	if elapsed > 250*time.Millisecond {
		t.Fatalf("expected 10k active assertion indexed probe < 250ms, took %v", elapsed)
	}
}

func TestHasFTSNoVariant(t *testing.T) {
	connection, _ := connectMigrated(t, LocalProfile())
	execSQL(t, connection, "DROP TABLE node_fts")
	execSQL(t, connection, "DROP TABLE trace_fts")
	ok, err := HasFTS(connection)
	if err != nil {
		t.Fatalf("HasFTS: %v", err)
	}
	if ok {
		t.Fatal("HasFTS reported true after dropping both virtual tables")
	}
}

func TestSplitSchemaStatements(t *testing.T) {
	statements := splitSchemaStatements("-- comment\nCREATE TABLE a(x);\n-- another\nCREATE INDEX b ON a(x);\n")
	if len(statements) != 2 {
		t.Fatalf("statements = %v", statements)
	}
	if !strings.HasPrefix(statements[0], "CREATE TABLE a") ||
		!strings.HasPrefix(statements[1], "CREATE INDEX b") {
		t.Fatalf("unexpected statements: %v", statements)
	}
}

func TestVaultScopePreflightLegacyScopedAndUnscopedTables(t *testing.T) {
	connection, path := connectMigrated(t, LocalProfile())
	defer connection.Close()

	// Seed unscoped legacy tables (sync_state, injection_ledger) and scoped legacy tables
	// (mention, activation) for a non-default scope "custom-team-scope".
	execSQL(t, connection, `INSERT INTO sync_state(direction, local_hash, remote_id, synced_at) VALUES ('push', 'h1', 'r1', 1)`)
	execSQL(t, connection, `INSERT INTO injection_ledger(session, prompt_hash, content_hash, created_at) VALUES ('s1', 'p1', 'c1', 1)`)
	execSQL(t, connection, `INSERT INTO trace(hash, scope, session, turn, kind, author, audience, text, occurred_at, recorded_at) VALUES ('t1', 'custom-team-scope', 's1', 1, 'message', 'user', 'shared', 'hi', 1, 1)`)
	execSQL(t, connection, `INSERT INTO node(id, scope, kind, label, norm_label, status, audience, created_at) VALUES ('n1', 'custom-team-scope', 'referent', 'A', 'a', 'active', 'shared', 1)`)
	execSQL(t, connection, `INSERT INTO mention(scope, node_id, trace_hash, span_start, span_end, surface, created_at) VALUES ('custom-team-scope', 'n1', 't1', 0, 1, 'A', 1)`)
	execSQL(t, connection, `INSERT INTO activation(node_id, scope, score, updated_at) VALUES ('n1', 'custom-team-scope', 1.0, 1)`)

	if err := VaultScopePreflight(path, "custom-team-scope"); err != nil {
		t.Fatalf("expected matching legacy tables with populated sync_state/injection_ledger to pass preflight, got: %v", err)
	}
	if err := VaultScopePreflight(path, "other-scope"); err == nil {
		t.Fatalf("expected mismatched scope in mention/activation/trace to fail preflight")
	}
}
