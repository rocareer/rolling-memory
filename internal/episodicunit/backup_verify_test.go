// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package episodicunit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/episodic"
	"github.com/rocareer/rolling-memory/internal/ids"
	_ "modernc.org/sqlite"
)

// verifyBackupUnit is intentionally test-only. It returns no source text and
// never opens the live ZCode database or writes to either database.
func verifyBackupUnit(ctx context.Context, source *sql.DB, scope, docID, pointer string) (bool, error) {
	if ctx == nil || source == nil || strings.TrimSpace(scope) == "" || docID == "" {
		return false, errors.New("backup verification requires context, source, scope and document ID")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !strings.HasPrefix(pointer, "@ref:zcode:") {
		return false, errors.New("unsupported backup reference")
	}
	rest := strings.TrimPrefix(pointer, "@ref:zcode:")
	parts := strings.SplitN(rest, ":", 2)
	if len(parts) != 2 || parts[0] == "" {
		return false, errors.New("invalid ZCode reference")
	}
	pieces := strings.Split(parts[1], "#")
	if len(pieces) != 2 || pieces[0] == "" {
		return false, errors.New("reference does not identify a segment")
	}
	indexes := strings.Split(pieces[1], ".")
	if len(indexes) < 1 || len(indexes) > 2 {
		return false, errors.New("invalid segment reference")
	}
	for _, value := range indexes {
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 || strconv.Itoa(n) != value {
			return false, errors.New("invalid segment reference")
		}
	}
	segments, err := episodic.ZCodeTurnSegments(source, parts[0], pieces[0])
	if err != nil {
		return false, fmt.Errorf("read backup turn: %w", err)
	}
	segmentIndex, _ := strconv.Atoi(indexes[0])
	if segmentIndex >= len(segments) {
		return false, errors.New("backup segment not found")
	}
	prefix := "@ref:zcode:" + parts[0] + ":" + pieces[0]
	for _, unit := range SegmentUnits(scope, prefix, segments[segmentIndex], 1, true) {
		if unit.RefPointer == pointer {
			return unit.DocID == docID, nil
		}
	}
	return false, errors.New("backup chunk not found")
}

func openBackupFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "backup?#.sqlite")
	dsn := ids.SQLiteFileURI(path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT, sequence INTEGER, time_created INTEGER, data TEXT)`,
		`CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT, session_id TEXT, sequence INTEGER, data TEXT)`,
		`INSERT INTO message VALUES ('m1','s1',1,1,'{"role":"user"}')`,
		`INSERT INTO message VALUES ('m2','s1',2,2,'{"role":"assistant"}')`,
		`INSERT INTO part VALUES ('p1','m1','s1',1,'{"type":"text","text":"fixture input"}')`,
		`INSERT INTO part VALUES ('p2','m2','s1',1,'{"type":"text","text":"fixture answer"}')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	longText := strings.Repeat("fixture sentence. ", TokenBudget)
	if _, err := db.Exec(`INSERT INTO message VALUES ('m3','s1',3,3,'{"role":"user"}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO part VALUES ('p3','m3','s1',1,json_object('type','text','text',?))`, longText); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func openBackupReadOnly(path string) (*sql.DB, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || !filepath.IsAbs(path) {
		return nil, errors.New("backup must be an existing absolute regular file")
	}
	dsn := ids.SQLiteFileURI(path) + "?mode=ro&immutable=1"
	return sql.Open("sqlite", dsn)
}

func TestBackupUnitVerificationIsReadOnlyAndFailClosed(t *testing.T) {
	path := openBackupFixture(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	db, err := openBackupReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id := DocID("mem:test", Text("assistant", "fixture answer"))
	for _, tc := range []struct {
		name, scope, id, pointer string
		verified, wantError      bool
	}{
		{"match", "mem:test", id, "@ref:zcode:s1:m1#1", true, false},
		{"wrong scope", "mem:other", id, "@ref:zcode:s1:m1#1", false, false},
		{"wrong id", "mem:test", "ep_wrong", "@ref:zcode:s1:m1#1", false, false},
		{"missing message", "mem:test", id, "@ref:zcode:s1:missing#1", false, true},
		{"wrong session", "mem:test", id, "@ref:zcode:other:m1#1", false, true},
		{"missing segment", "mem:test", id, "@ref:zcode:s1:m1#9", false, true},
		{"bad chunk", "mem:test", id, "@ref:zcode:s1:m1#1.0", false, true},
		{"malformed suffix", "mem:test", id, "@ref:zcode:s1:m1#1.", false, true},
		{"whole turn", "mem:test", id, "@ref:zcode:s1:m1", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			verified, err := verifyBackupUnit(context.Background(), db, tc.scope, tc.id, tc.pointer)
			if verified != tc.verified || (err != nil) != tc.wantError {
				t.Fatalf("verified=%v err=%v", verified, err)
			}
		})
	}
	longSegment := episodic.Segment{Role: episodic.RoleUser, Text: strings.Repeat("fixture sentence. ", TokenBudget), Index: 0}
	units := SegmentUnits("mem:test", "@ref:zcode:s1:m3", longSegment, 1, true)
	if len(units) < 2 {
		t.Fatal("fixture did not produce multiple chunks")
	}
	for _, unit := range units {
		verified, err := verifyBackupUnit(context.Background(), db, "mem:test", unit.DocID, unit.RefPointer)
		if err != nil || !verified {
			t.Fatalf("chunk verification failed: %v", err)
		}
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := verifyBackupUnit(cancelled, db, "mem:test", id, "@ref:zcode:s1:m1#1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled verification: %v", err)
	}
	if _, err := openBackupReadOnly(filepath.Join(t.TempDir(), "missing.db")); err == nil {
		t.Fatal("missing backup accepted")
	}
	if _, err := db.Exec(`DELETE FROM part`); err == nil {
		t.Fatal("read-only backup accepted a write")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatalf("backup changed: %v", err)
	}
}

// This opt-in probe reports aggregate counts only. Paths must name a stable,
// independent backup and an existing vault; no restore or source write occurs.
func TestVerifyRealZCodeBackup(t *testing.T) {
	backupPath := os.Getenv("RM_TEST_ZCODE_BACKUP")
	vaultPath := os.Getenv("RM_TEST_VAULT")
	scope := os.Getenv("RM_TEST_SCOPE")
	if backupPath == "" || vaultPath == "" || scope == "" {
		t.Skip("real backup probe is opt-in")
	}
	livePath := filepath.Join(os.Getenv("HOME"), ".zcode", "cli", "db", "db.sqlite")
	backupInfo, err := os.Stat(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	liveInfo, err := os.Stat(livePath)
	if err == nil && os.SameFile(backupInfo, liveInfo) {
		t.Fatal("source must be an independent backup")
	}
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	backup, err := openBackupReadOnly(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if !filepath.IsAbs(vaultPath) {
		t.Fatal("vault path must be absolute")
	}
	info, err := os.Stat(vaultPath)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatal("vault must be an existing regular file")
	}
	vaultDSN := ids.SQLiteFileURI(vaultPath) + "?mode=ro"
	vault, err := sql.Open("sqlite", vaultDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	rows, err := vault.Query(`SELECT doc_id, raw_text FROM thin_vault_docs WHERE raw_text LIKE '@ref:zcode:%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var total, matched, mismatch, unavailable, legacy int
	for rows.Next() {
		var id, pointer string
		if err := rows.Scan(&id, &pointer); err != nil {
			t.Fatal(err)
		}
		total++
		if !strings.Contains(pointer, "#") {
			legacy++
			continue
		}
		ok, err := verifyBackupUnit(context.Background(), backup, scope, id, pointer)
		if err != nil {
			unavailable++
		} else if ok {
			matched++
		} else {
			mismatch++
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	t.Logf("zcode references=%d verified=%d mismatch=%d unavailable=%d legacy=%d", total, matched, mismatch, unavailable, legacy)
}
