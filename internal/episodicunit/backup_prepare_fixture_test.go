// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package episodicunit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureRecoveryReceipt is an in-memory audit receipt, not authorization to write.
// It contains no paths, references, scope, document IDs, or recovered text.
type fixtureRecoveryReceipt struct {
	SourceSHA256 string `json:"source_sha256"`
	VaultSHA256  string `json:"vault_sha256"`
	Verified     int    `json:"verified"`
	Mismatch     int    `json:"mismatch"`
	Unavailable  int    `json:"unavailable"`
	Legacy       int    `json:"legacy"`
	ApplyAllowed bool   `json:"apply_allowed"`
}

// prepareFixtureRecovery is intentionally test-only. Both files must be closed,
// independent SQLite fixture snapshots directly inside the supplied fixture dir.
// The supplied scope is tested via the content-derived ID, not read from the vault.
func prepareFixtureRecovery(ctx context.Context, dir, sourcePath, vaultPath, scope string) (fixtureRecoveryReceipt, error) {
	var receipt fixtureRecoveryReceipt
	if ctx == nil || strings.TrimSpace(scope) == "" || !filepath.IsAbs(dir) {
		return receipt, fmt.Errorf("recovery preparation requires a fixture directory and scope")
	}
	for _, path := range []string{sourcePath, vaultPath} {
		if !filepath.IsAbs(path) || filepath.Dir(path) != dir {
			return receipt, fmt.Errorf("fixture snapshot must be directly inside fixture directory")
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return receipt, fmt.Errorf("fixture snapshot must be a regular file")
		}
		for _, suffix := range []string{"-wal", "-shm", "-journal"} {
			if _, err := os.Lstat(path + suffix); !os.IsNotExist(err) {
				return receipt, fmt.Errorf("fixture snapshot has or may have a SQLite sidecar")
			}
		}
	}
	sourceInfo, err := os.Stat(sourcePath)
	if err != nil {
		return receipt, err
	}
	vaultInfo, err := os.Stat(vaultPath)
	if err != nil || os.SameFile(sourceInfo, vaultInfo) {
		return receipt, fmt.Errorf("fixture snapshots must be independent")
	}
	fingerprint := func(path string) (string, error) {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%x", sha256.Sum256(data)), nil
	}
	if receipt.SourceSHA256, err = fingerprint(sourcePath); err != nil {
		return fixtureRecoveryReceipt{}, err
	}
	if receipt.VaultSHA256, err = fingerprint(vaultPath); err != nil {
		return fixtureRecoveryReceipt{}, err
	}
	source, err := openBackupReadOnly(sourcePath)
	if err != nil {
		return fixtureRecoveryReceipt{}, err
	}
	defer source.Close()
	vault, err := openBackupReadOnly(vaultPath)
	if err != nil {
		return fixtureRecoveryReceipt{}, err
	}
	defer vault.Close()
	rows, err := vault.QueryContext(ctx, `SELECT doc_id, raw_text FROM thin_vault_docs WHERE (layer = 'episodic' OR layer = '') AND substr(raw_text, 1, 11) = '@ref:zcode:' ORDER BY doc_id`)
	if err != nil {
		return fixtureRecoveryReceipt{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, pointer string
		if err := rows.Scan(&id, &pointer); err != nil {
			return fixtureRecoveryReceipt{}, err
		}
		if !strings.Contains(pointer, "#") {
			receipt.Legacy++
			continue
		}
		verified, err := verifyBackupUnit(ctx, source, scope, id, pointer)
		if ctx.Err() != nil {
			return fixtureRecoveryReceipt{}, ctx.Err()
		}
		switch {
		case err != nil:
			receipt.Unavailable++
		case verified:
			receipt.Verified++
		default:
			receipt.Mismatch++
		}
	}
	if err := rows.Err(); err != nil {
		return fixtureRecoveryReceipt{}, err
	}
	for path, expected := range map[string]string{sourcePath: receipt.SourceSHA256, vaultPath: receipt.VaultSHA256} {
		actual, err := fingerprint(path)
		if err != nil || actual != expected {
			return fixtureRecoveryReceipt{}, fmt.Errorf("fixture snapshot changed during preparation")
		}
	}
	return receipt, nil
}

func TestFixtureRecoveryPreparationAuditAndIsolation(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.sqlite")
	data, err := os.ReadFile(openBackupFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	vaultPath := filepath.Join(dir, "vault.sqlite")
	vault, err := sql.Open("sqlite", vaultPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vault.Exec(`CREATE TABLE thin_vault_docs (doc_id TEXT PRIMARY KEY, layer TEXT, raw_text TEXT)`); err != nil {
		t.Fatal(err)
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
	if err := vault.Close(); err != nil {
		t.Fatal(err)
	}
	receipt, err := prepareFixtureRecovery(context.Background(), dir, sourcePath, vaultPath, "mem:test")
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Verified != 1 || receipt.Mismatch != 1 || receipt.Unavailable != 1 || receipt.Legacy != 1 || receipt.ApplyAllowed {
		t.Fatalf("unexpected receipt: %+v", receipt)
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"fixture answer", "@ref:", "mem:test", id, dir} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("audit receipt exposed fixture source data")
		}
	}
	wrongScope, err := prepareFixtureRecovery(context.Background(), dir, sourcePath, vaultPath, "mem:other")
	if err != nil || wrongScope.Verified != 0 || wrongScope.Mismatch != 2 {
		t.Fatalf("wrong scope receipt: %+v, %v", wrongScope, err)
	}
	if _, err := prepareFixtureRecovery(context.Background(), dir, sourcePath, sourcePath, "mem:test"); err == nil {
		t.Fatal("same source and vault accepted")
	}
	if _, err := prepareFixtureRecovery(context.Background(), dir, sourcePath, filepath.Join(t.TempDir(), "outside"), "mem:test"); err == nil {
		t.Fatal("outside fixture accepted")
	}
	if err := os.WriteFile(sourcePath+"-wal", []byte("uncheckpointed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareFixtureRecovery(context.Background(), dir, sourcePath, vaultPath, "mem:test"); err == nil {
		t.Fatal("snapshot with WAL accepted")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := prepareFixtureRecovery(cancelled, dir, vaultPath, vaultPath, "mem:test"); err == nil {
		t.Fatal("cancelled or invalid preparation accepted")
	}
}
