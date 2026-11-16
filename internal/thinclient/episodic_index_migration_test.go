// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient

import (
	"bytes"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

const legacyEpisodicIndexDDL = `CREATE TABLE thin_vault_episodic_index (doc_id TEXT PRIMARY KEY, ref_pointer TEXT NOT NULL, codec_key BLOB NOT NULL, payload BLOB NOT NULL)`

func migrationTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "vault.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestEpisodicMigrationLegacy(t *testing.T) {
	for _, fast := range []bool{false, true} {
		t.Run(fmt.Sprintf("fast=%t", fast), func(t *testing.T) {
			db := migrationTestDB(t)
			if fast {
				if _, err := NewVault(db, nil); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`DROP TABLE thin_vault_episodic_index`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec(legacyEpisodicIndexDDL + `; INSERT INTO thin_vault_episodic_index VALUES ('one', '@ref:one', x'000102', x'ff0003'), ('two', '@ref:two', x'0405', x'0600')`); err != nil {
				t.Fatal(err)
			}
			var secret []byte
			if !fast {
				secret = []byte("migration-key")
			}
			v, err := NewVault(db, secret)
			if err != nil {
				t.Fatal(err)
			}
			rows, err := db.Query(`SELECT doc_id, ref_pointer, codec_key, payload, content_digest FROM thin_vault_episodic_index ORDER BY doc_id`)
			if err != nil {
				t.Fatal(err)
			}
			type row struct {
				id, ref      string
				key, payload []byte
			}
			want := []row{{"one", "@ref:one", []byte{0, 1, 2}, []byte{255, 0, 3}}, {"two", "@ref:two", []byte{4, 5}, []byte{6, 0}}}
			var got []row
			for rows.Next() {
				var r row
				var digest []byte
				if err := rows.Scan(&r.id, &r.ref, &r.key, &r.payload, &digest); err != nil {
					t.Fatal(err)
				}
				if digest != nil {
					t.Fatalf("legacy digest should be NULL: %x", digest)
				}
				got = append(got, r)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			rows.Close()
			if len(got) != len(want) {
				t.Fatalf("rows: %d", len(got))
			}
			for i := range want {
				if got[i].id != want[i].id || got[i].ref != want[i].ref || !bytes.Equal(got[i].key, want[i].key) || !bytes.Equal(got[i].payload, want[i].payload) {
					t.Fatalf("row %d changed: %+v", i, got[i])
				}
			}
			if _, err := db.Exec(`INSERT INTO thin_vault_episodic_index(doc_id, ref_pointer, codec_key, payload, content_digest) VALUES ('one', '@ref:one', x'07', x'08', NULL)`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO thin_vault_docs(doc_id,layer,raw_text,sentences_json) VALUES ('one','episodic','@ref:one','[]')`); err != nil {
				t.Fatal(err)
			}
			key, err := v.episodicCodecKey()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO thin_vault_episodic_index(doc_id, ref_pointer, codec_key, payload, content_digest) VALUES ('one', '@ref:one', ?, x'00', NULL)`, key); err != nil {
				t.Fatal(err)
			}
			if _, err := v.LoadVerifiedEpisodicPointer("one", "@ref:one"); err == nil {
				t.Fatal("NULL digest accepted as verified")
			}
			docs, err := v.indexedEpisodicDocs()
			if err != nil || len(docs) != 0 {
				t.Fatalf("NULL digest indexed: %v %v", docs, err)
			}
		})
	}
}

func TestEpisodicMigrationCurrentPreserved(t *testing.T) {
	db := migrationTestDB(t)
	if _, err := NewVault(db, []byte("key")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE INDEX user_index ON thin_vault_episodic_index(ref_pointer); INSERT INTO thin_vault_episodic_index(doc_id, ref_pointer, codec_key, payload, content_digest) VALUES ('x', '@ref:x', x'01', x'02', x'03')`); err != nil {
		t.Fatal(err)
	}
	var rootBefore, schemaBefore int
	if err := db.QueryRow(`SELECT rootpage FROM sqlite_master WHERE name='thin_vault_episodic_index'`).Scan(&rootBefore); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`PRAGMA schema_version`).Scan(&schemaBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := NewVault(db, []byte("key")); err != nil {
		t.Fatal(err)
	}
	var rootAfter, schemaAfter, count int
	if err := db.QueryRow(`SELECT rootpage FROM sqlite_master WHERE name='thin_vault_episodic_index'`).Scan(&rootAfter); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`PRAGMA schema_version`).Scan(&schemaAfter); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM thin_vault_episodic_index WHERE content_digest = x'03'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if rootBefore != rootAfter || schemaBefore != schemaAfter || count != 1 {
		t.Fatalf("current table changed: root %d/%d schema %d/%d rows %d", rootBefore, rootAfter, schemaBefore, schemaAfter, count)
	}
}

func TestEpisodicMigrationAddsMACOnBothVaultPaths(t *testing.T) {
	for _, fast := range []bool{false, true} {
		t.Run(fmt.Sprintf("fast=%t", fast), func(t *testing.T) {
			db := migrationTestDB(t)
			if fast {
				if _, err := NewVault(db, nil); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`DROP TABLE thin_vault_episodic_index`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec(`CREATE TABLE thin_vault_episodic_index (doc_id TEXT NOT NULL, ref_pointer TEXT NOT NULL, codec_key BLOB NOT NULL, payload BLOB NOT NULL, content_digest BLOB, PRIMARY KEY (doc_id, codec_key)); INSERT INTO thin_vault_episodic_index VALUES ('old', '@ref:old', x'01', x'02', x'03')`); err != nil {
				t.Fatal(err)
			}
			var secret []byte
			if !fast {
				secret = []byte("migration-key")
			}
			if _, err := NewVault(db, secret); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM thin_vault_episodic_index WHERE doc_id='old' AND payload=x'02' AND content_digest=x'03' AND payload_mac IS NULL`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("old row changed: %d %v", count, err)
			}
		})
	}
}

func TestEpisodicMigrationRejectsUnknownWithoutChanges(t *testing.T) {
	for _, ddl := range []string{
		`CREATE TABLE thin_vault_episodic_index (doc_id TEXT PRIMARY KEY, ref_pointer TEXT NOT NULL, codec_key BLOB NOT NULL, payload BLOB NOT NULL, extra TEXT)`,
		`CREATE TABLE thin_vault_episodic_index (doc_id TEXT PRIMARY KEY, ref_pointer TEXT NOT NULL, codec_key BLOB NOT NULL, payload BLOB NOT NULL); CREATE INDEX user_index ON thin_vault_episodic_index(ref_pointer)`,
	} {
		t.Run(fmt.Sprint(len(ddl)), func(t *testing.T) {
			db := migrationTestDB(t)
			if _, err := db.Exec(`CREATE TABLE thin_vault_docs (doc_id TEXT PRIMARY KEY); ` + ddl); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO thin_vault_episodic_index(doc_id, ref_pointer, codec_key, payload) VALUES ('old','@ref:old',x'01',x'02')`); err != nil {
				t.Fatal(err)
			}
			var schemaBefore, schemaAfter int
			if err := db.QueryRow(`PRAGMA schema_version`).Scan(&schemaBefore); err != nil {
				t.Fatal(err)
			}
			err := initEpisodicIndex(db)
			if err == nil || !strings.Contains(err.Error(), "unrecognized episodic index schema") {
				t.Fatalf("error: %v", err)
			}
			if err := db.QueryRow(`PRAGMA schema_version`).Scan(&schemaAfter); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM thin_vault_episodic_index WHERE doc_id='old' AND codec_key=x'01' AND payload=x'02'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if schemaBefore != schemaAfter || count != 1 {
				t.Fatalf("database changed: schema %d/%d rows %d", schemaBefore, schemaAfter, count)
			}
		})
	}
}

func TestEpisodicMigrationPreservesSourceTrigger(t *testing.T) {
	db := migrationTestDB(t)
	if _, err := db.Exec(`CREATE TABLE thin_vault_docs (doc_id TEXT PRIMARY KEY); ` + legacyEpisodicIndexDDL + `;
CREATE TABLE trigger_audit (value TEXT);
CREATE TRIGGER thin_episodic_source_delete AFTER DELETE ON thin_vault_docs BEGIN INSERT INTO trigger_audit VALUES (OLD.doc_id); DELETE FROM thin_vault_episodic_index WHERE doc_id = OLD.doc_id; END;
INSERT INTO thin_vault_docs VALUES ('old'); INSERT INTO thin_vault_episodic_index VALUES ('old','@ref:old',x'01',x'02')`); err != nil {
		t.Fatal(err)
	}
	if err := initEpisodicIndex(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM thin_vault_docs WHERE doc_id='old'`); err != nil {
		t.Fatal(err)
	}
	var audit, remaining int
	if err := db.QueryRow(`SELECT COUNT(*) FROM trigger_audit WHERE value='old'`).Scan(&audit); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM thin_vault_episodic_index`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if audit != 1 || remaining != 0 {
		t.Fatalf("source trigger not preserved: audit=%d remaining=%d", audit, remaining)
	}
}

func TestEpisodicMigrationRollback(t *testing.T) {
	db := migrationTestDB(t)
	if _, err := db.Exec(legacyEpisodicIndexDDL + `; INSERT INTO thin_vault_episodic_index VALUES (NULL, '@ref:old', x'01', x'02')`); err != nil {
		t.Fatal(err)
	}
	if err := initEpisodicIndex(db); err == nil {
		t.Fatal("invalid legacy row accepted")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM thin_vault_episodic_index WHERE doc_id IS NULL AND payload=x'02'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("legacy row lost: %d %v", count, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='thin_vault_episodic_index_migration'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("migration table survived: %d %v", count, err)
	}
}

func TestEpisodicMigrationConcurrentConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.sqlite")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE thin_vault_docs (doc_id TEXT PRIMARY KEY); ` + legacyEpisodicIndexDDL + `; INSERT INTO thin_vault_episodic_index VALUES ('old','@ref:old',x'01',x'02')`); err != nil {
		t.Fatal(err)
	}
	const workers = 8
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; errs <- initEpisodicIndex(db) }()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM thin_vault_episodic_index WHERE doc_id='old' AND codec_key=x'01' AND payload=x'02' AND content_digest IS NULL`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("row: %d %v", count, err)
	}
}
