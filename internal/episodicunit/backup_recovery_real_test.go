// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package episodicunit

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestPrepareRealZCodeBackup is an opt-in aggregate-only, read-only probe.
func TestPrepareRealZCodeBackup(t *testing.T) {
	backup := os.Getenv("RM_TEST_ZCODE_BACKUP")
	vault := os.Getenv("RM_TEST_VAULT")
	live := os.Getenv("RM_TEST_ZCODE_LIVE")
	scope := os.Getenv("RM_TEST_SCOPE")
	if backup == "" || vault == "" || live == "" || scope == "" {
		t.Skip("real backup probe is opt-in")
	}
	receipt, err := PrepareZCodeBackupRecovery(context.Background(), backup, vault, live, scope)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("aggregate receipt: %s", encoded)
	if os.Getenv("RM_TEST_COMPARE_LIVE") != "1" {
		return
	}
	backupDB, err := recoveryOpen(backup, true)
	if err != nil {
		t.Fatal("cannot open backup")
	}
	defer backupDB.Close()
	liveDB, err := recoveryOpen(live, false)
	if err != nil {
		t.Fatal("cannot open live source")
	}
	defer liveDB.Close()
	vaultDB, err := recoveryOpen(vault, false)
	if err != nil {
		t.Fatal("cannot open vault")
	}
	defer vaultDB.Close()
	tx, err := vaultDB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal("cannot read vault")
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT doc_id, raw_text FROM thin_vault_docs WHERE (layer = 'episodic' OR layer = '') AND substr(raw_text, 1, 11) = '@ref:zcode:'`)
	if err != nil {
		t.Fatal("cannot read references")
	}
	defer rows.Close()
	var missingVerified, missingMismatch, missingUnavailable, liveVerified int
	for rows.Next() {
		var id, pointer string
		if err := rows.Scan(&id, &pointer); err != nil {
			t.Fatal("cannot scan references")
		}
		if !strings.Contains(pointer, "#") {
			continue
		}
		matched, liveErr := recoveryVerifyUnit(context.Background(), liveDB, scope, id, pointer)
		if liveErr == nil && matched {
			liveVerified++
			continue
		}
		matched, backupErr := recoveryVerifyUnit(context.Background(), backupDB, scope, id, pointer)
		switch {
		case backupErr != nil:
			missingUnavailable++
		case matched:
			missingVerified++
		default:
			missingMismatch++
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal("cannot finish references")
	}
	t.Logf("segmented references: live_verified=%d live_unverified_backup_verified=%d live_unverified_backup_mismatch=%d live_unverified_backup_unavailable=%d legacy_not_compared=%d", liveVerified, missingVerified, missingMismatch, missingUnavailable, receipt.Legacy)
}
