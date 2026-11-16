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
	"sync"
	"testing"

	"github.com/rocareer/rolling-memory/internal/episodicunit"
	_ "modernc.org/sqlite"
)

func TestVaultKeyConcurrentInitialization(t *testing.T) {
	const rounds = 12
	const workers = 8
	dir := t.TempDir()
	for round := 0; round < rounds; round++ {
		path := filepath.Join(dir, fmt.Sprintf("vault-%d.sqlite", round))
		dsn := "file:" + path + "?_pragma=busy_timeout(10000)"
		dbs := make([]*sql.DB, workers)
		for i := range dbs {
			var err error
			dbs[i], err = sql.Open("sqlite", dsn)
			if err != nil {
				t.Fatal(err)
			}
			dbs[i].SetMaxOpenConns(1)
		}
		// Initialize the schema without creating a stored key.
		if _, err := NewVault(dbs[0], []byte("schema-initialization")); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		vaults := make([]*Vault, workers)
		errs := make([]error, workers)
		var wg sync.WaitGroup
		for i := range dbs {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				vaults[i], errs[i] = NewVault(dbs[i], nil)
			}(i)
		}
		close(start)
		wg.Wait()
		var stored []byte
		if err := dbs[0].QueryRow(`SELECT v FROM thin_vault_meta WHERE k = 'user_secret_key'`).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		for i := range dbs {
			if errs[i] != nil {
				t.Fatalf("round %d worker %d: %v", round, i, errs[i])
			}
			if !bytes.Equal(vaults[i].secretKey[:], stored) {
				t.Fatalf("round %d worker %d returned a different key", round, i)
			}
			if err := dbs[i].Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestVaultKeyInvalidExistingValue(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
	}{
		{name: "empty blob", value: []byte{}},
		{name: "short blob", value: []byte("invalid")},
		{name: "long blob", value: bytes.Repeat([]byte{1}, 33)},
		{name: "text", value: "12345678901234567890123456789012"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "vault.sqlite")
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := NewVault(db, []byte("schema-initialization")); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO thin_vault_meta(k, v) VALUES ('user_secret_key', ?)`, tc.value); err != nil {
				t.Fatal(err)
			}
			if value, ok := tc.value.(string); ok {
				if _, err := db.Exec(`UPDATE thin_vault_meta SET v = CAST(? AS TEXT) WHERE k = 'user_secret_key'`, value); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := NewVault(db, nil); err == nil {
				t.Fatal("invalid key accepted")
			}
			var stored []byte
			var storageType string
			if err := db.QueryRow(`SELECT v, typeof(v) FROM thin_vault_meta WHERE k = 'user_secret_key'`).Scan(&stored, &storageType); err != nil {
				t.Fatal(err)
			}
			switch value := tc.value.(type) {
			case []byte:
				if storageType != "blob" || !bytes.Equal(stored, value) {
					t.Fatal("invalid key was changed")
				}
			case string:
				if storageType != "text" || string(stored) != value {
					t.Fatal("invalid key was changed")
				}
			}
		})
	}
}

func TestCorpusSnapshotIncrementalAppendConcurrentRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.sqlite")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	vault, err := NewVault(db, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	seedEpisodic := func(i int) string {
		text := fmt.Sprintf("alpha beta gamma delta entry %d", i)
		docID := episodicunit.DocID("default", text)
		ref := fmt.Sprintf("@ref:jsonl:/tmp/synth.jsonl:%d#0", i)
		occ := int64(1700000000 + i)
		if _, err := db.Exec(`INSERT INTO thin_vault_docs(doc_id, layer, occurred_at, inserted_at, raw_text, sentences_json, address_uri, coord64, waveform_bar, doc_macro_wave, kept_indices_json) VALUES (?, 'episodic', ?, ?, ?, '[]', '', 0, '', '', '[]')`, docID, occ, occ, ref); err != nil {
			t.Fatal(err)
		}
		if _, err := vault.ensureLocalEpisodicIndex("default", docID, ref, text, true); err != nil {
			t.Fatal(err)
		}
		return docID
	}
	firstID := seedEpisodic(0)
	for i := 1; i < 4; i++ {
		_ = seedEpisodic(i)
	}
	if _, err := vault.buildCorpusSnapshot(context.Background()); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	cueHashes := []uint64{vault.KeyedHash64("alpha"), vault.KeyedHash64("beta")}
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = vault.DocAtomCoverageWithCueHashes(firstID, "", cueHashes)
				_, _ = vault.SearchClientSDKDefault("alpha entry", 5)
			}
		}()
	}

	for i := 4; i < 20; i++ {
		_ = seedEpisodic(i)
		if _, err := vault.buildCorpusSnapshot(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
}

func TestDirIDCollisionResolution(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	vault, err := NewVault(db, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	if err := vault.ensureBackgroundTables(); err != nil {
		t.Fatal(err)
	}
	dirA := filepath.Join(t.TempDir(), "mountA")
	if err := os.MkdirAll(dirA, 0o755); err != nil {
		t.Fatal(err)
	}
	cleanA, err := canonicalDirPath(dirA)
	if err != nil {
		t.Fatal(err)
	}
	collidingID := computeDirID(cleanA)
	// Seed a synthetic different root_path occupying the same 32-bit default dir_id.
	if _, err := db.Exec(`INSERT INTO thin_vault_bg_dirs(dir_id, root_path, added_at) VALUES (?, ?, 1)`, collidingID, "/synthetic/other/mount"); err != nil {
		t.Fatal(err)
	}
	mount, err := vault.AddBackgroundDir(dirA)
	if err != nil {
		t.Fatal(err)
	}
	if mount.DirID == collidingID || mount.DirID == "" {
		t.Fatalf("expected extended non-colliding dir_id, got %q (colliding=%q)", mount.DirID, collidingID)
	}
}
