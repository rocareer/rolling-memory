// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package episodicunit

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareZCodeBackupRecoveryReadOnly(t *testing.T) {
	dir := t.TempDir()
	backupPath := openBackupFixture(t)
	vaultPath := filepath.Join(dir, "vault.sqlite")
	vault, err := sql.Open("sqlite", vaultPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE thin_vault_docs (doc_id TEXT PRIMARY KEY, layer TEXT, raw_text TEXT)`,
		`PRAGMA journal_mode=WAL`,
	} {
		if _, err := vault.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	id := DocID("mem:test", Text("assistant", "fixture answer"))
	for _, row := range [][2]string{
		{id, "@ref:zcode:s1:m1#1"},
		{"ep_wrong", "@ref:zcode:s1:m1#0"},
		{"ep_missing", "@ref:zcode:s1:missing#0"},
		{"ep_legacy", "@ref:zcode:s1:m1"},
	} {
		if _, err := vault.Exec(`INSERT INTO thin_vault_docs VALUES (?, 'episodic', ?)`, row[0], row[1]); err != nil {
			t.Fatal(err)
		}
	}
	defer vault.Close()
	livePath := filepath.Join(dir, "live.sqlite")
	if err := os.WriteFile(livePath, []byte("live"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := PrepareZCodeBackupRecovery(context.Background(), backupPath, vaultPath, livePath, "mem:test")
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Verified != 1 || receipt.Mismatch != 1 || receipt.Unavailable != 1 || receipt.Legacy != 1 || receipt.ApplyAllowed || receipt.BackupSHA256 == "" {
		t.Fatalf("unexpected receipt: %+v", receipt)
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"fixture answer", "@ref:", "mem:test", id, dir, backupPath} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("receipt exposed source data")
		}
	}
	wrong, err := PrepareZCodeBackupRecovery(context.Background(), backupPath, vaultPath, livePath, "mem:other")
	if err != nil || wrong.Verified != 0 || wrong.Mismatch != 2 {
		t.Fatalf("wrong scope receipt: %+v, %v", wrong, err)
	}
	if _, err := PrepareZCodeBackupRecovery(context.Background(), backupPath, vaultPath, backupPath, "mem:test"); err == nil {
		t.Fatal("live source accepted as backup")
	}
	alias := filepath.Join(dir, "backup-hardlink.sqlite")
	if err := os.Link(backupPath, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareZCodeBackupRecovery(context.Background(), backupPath, vaultPath, alias, "mem:test"); err == nil {
		t.Fatal("hard-linked live source accepted as backup")
	}
	if _, err := PrepareZCodeBackupRecovery(context.Background(), backupPath, backupPath, livePath, "mem:test"); err == nil {
		t.Fatal("backup accepted as vault")
	}
	if err := os.WriteFile(backupPath+"-wal", []byte("uncheckpointed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareZCodeBackupRecovery(context.Background(), backupPath, vaultPath, livePath, "mem:test"); err == nil {
		t.Fatal("backup with sidecar accepted")
	}
	after, err := os.ReadFile(backupPath)
	if err != nil || string(before) != string(after) {
		t.Fatal("backup changed")
	}
	var count int
	if err := vault.QueryRow(`SELECT count(*) FROM thin_vault_docs`).Scan(&count); err != nil || count != 4 {
		t.Fatal("vault changed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := PrepareZCodeBackupRecovery(ctx, backupPath, vaultPath, livePath, "mem:test"); err == nil {
		t.Fatal("cancelled context accepted")
	}
}
