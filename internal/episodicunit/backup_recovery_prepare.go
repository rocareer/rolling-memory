// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package episodicunit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rocareer/rolling-memory/internal/episodic"
	"github.com/rocareer/rolling-memory/internal/ids"
	_ "modernc.org/sqlite"
)

// BackupRecoveryReceipt contains aggregate evidence only; it is not permission
// to restore data. A match applies only to the supplied scope and snapshot.
type BackupRecoveryReceipt struct {
	BackupSHA256 string `json:"backup_sha256"`
	Verified     int    `json:"verified"`
	Mismatch     int    `json:"mismatch"`
	Unavailable  int    `json:"unavailable"`
	Legacy       int    `json:"legacy"`
	ApplyAllowed bool   `json:"apply_allowed"`
}

// PrepareZCodeBackupRecovery verifies references against an independent, closed
// backup and a consistent read transaction of the live vault. It never writes,
// emits original text, or infers equivalence from summaries or turn anchors.
// The backup must have no SQLite sidecars; a live WAL vault is read normally.
func PrepareZCodeBackupRecovery(ctx context.Context, backupPath, vaultPath, liveZCodePath, scope string) (BackupRecoveryReceipt, error) {
	var result BackupRecoveryReceipt
	if ctx == nil || strings.TrimSpace(scope) == "" {
		return result, errors.New("recovery preparation requires context and scope")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	backupInfo, err := recoveryFileInfo(backupPath)
	if err != nil {
		return result, errors.New("backup must be an existing absolute regular file")
	}
	vaultInfo, err := recoveryFileInfo(vaultPath)
	if err != nil {
		return result, errors.New("vault must be an existing absolute regular file")
	}
	if os.SameFile(backupInfo, vaultInfo) {
		return result, errors.New("backup and vault must be independent")
	}
	if liveZCodePath == "" {
		return result, errors.New("live ZCode path is required to verify backup independence")
	}
	liveInfo, err := recoveryFileInfo(liveZCodePath)
	if err != nil {
		return result, errors.New("live ZCode database must be an existing absolute regular file")
	}
	if os.SameFile(backupInfo, liveInfo) {
		return result, errors.New("backup must be independent of the live ZCode database")
	}
	if err := recoveryNoSidecars(backupPath); err != nil {
		return result, err
	}
	before, err := recoveryFingerprint(ctx, backupPath)
	if err != nil {
		return result, err
	}
	backup, err := recoveryOpen(backupPath, true)
	if err != nil {
		return result, errors.New("cannot open backup read-only")
	}
	defer backup.Close()
	vault, err := recoveryOpen(vaultPath, false)
	if err != nil {
		return result, errors.New("cannot open vault read-only")
	}
	defer vault.Close()
	tx, err := vault.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, errors.New("cannot start vault read transaction")
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT doc_id, raw_text FROM thin_vault_docs WHERE (layer = 'episodic' OR layer = '') AND substr(raw_text, 1, 11) = '@ref:zcode:' ORDER BY doc_id`)
	if err != nil {
		return result, errors.New("cannot read vault references")
	}
	defer rows.Close()
	for rows.Next() {
		var id, pointer string
		if err := rows.Scan(&id, &pointer); err != nil {
			return BackupRecoveryReceipt{}, errors.New("cannot read vault reference")
		}
		if err := ctx.Err(); err != nil {
			return BackupRecoveryReceipt{}, err
		}
		if !strings.Contains(pointer, "#") {
			result.Legacy++
			continue
		}
		matched, err := recoveryVerifyUnit(ctx, backup, scope, id, pointer)
		if ctx.Err() != nil {
			return BackupRecoveryReceipt{}, ctx.Err()
		}
		switch {
		case err != nil:
			result.Unavailable++
		case matched:
			result.Verified++
		default:
			result.Mismatch++
		}
	}
	if err := rows.Err(); err != nil {
		return BackupRecoveryReceipt{}, errors.New("cannot finish reading vault references")
	}
	after, err := recoveryFingerprint(ctx, backupPath)
	if err != nil || before != after {
		return BackupRecoveryReceipt{}, errors.New("backup changed during preparation")
	}
	if err := recoveryNoSidecars(backupPath); err != nil {
		return BackupRecoveryReceipt{}, err
	}
	backupAfter, err := recoveryFileInfo(backupPath)
	if err != nil || !os.SameFile(backupInfo, backupAfter) {
		return BackupRecoveryReceipt{}, errors.New("backup identity changed during preparation")
	}
	result.BackupSHA256 = before
	return result, nil
}

func recoveryFileInfo(path string) (os.FileInfo, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("absolute path required")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("regular file required")
	}
	return info, nil
}

func recoveryNoSidecars(path string) error {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(path + suffix); !os.IsNotExist(err) {
			return errors.New("backup has or may have a SQLite sidecar")
		}
	}
	return nil
}

func recoveryFingerprint(ctx context.Context, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", errors.New("cannot fingerprint backup")
	}
	defer file.Close()
	hash := sha256.New()
	buffer := make([]byte, 128*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, readErr := file.Read(buffer)
		if n > 0 {
			_, _ = hash.Write(buffer[:n])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", errors.New("cannot fingerprint backup")
		}
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func recoveryOpen(path string, immutable bool) (*sql.DB, error) {
	query := "mode=ro"
	if immutable {
		query += "&immutable=1"
	}
	return sql.Open("sqlite", ids.SQLiteFileURI(path)+"?"+query)
}

func recoveryVerifyUnit(ctx context.Context, source *sql.DB, scope, docID, pointer string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !strings.HasPrefix(pointer, "@ref:zcode:") {
		return false, errors.New("unsupported reference")
	}
	parts := strings.SplitN(strings.TrimPrefix(pointer, "@ref:zcode:"), ":", 2)
	if len(parts) != 2 || parts[0] == "" {
		return false, errors.New("invalid reference")
	}
	pieces := strings.Split(parts[1], "#")
	if len(pieces) != 2 || pieces[0] == "" {
		return false, errors.New("invalid reference")
	}
	indexes := strings.Split(pieces[1], ".")
	if len(indexes) < 1 || len(indexes) > 2 {
		return false, errors.New("invalid segment")
	}
	for _, value := range indexes {
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 || strconv.Itoa(n) != value {
			return false, errors.New("invalid segment")
		}
	}
	segments, err := episodic.ZCodeTurnSegments(source, parts[0], pieces[0])
	if err != nil {
		return false, errors.New("backup turn unavailable")
	}
	index, _ := strconv.Atoi(indexes[0])
	if index >= len(segments) {
		return false, errors.New("backup segment unavailable")
	}
	prefix := "@ref:zcode:" + parts[0] + ":" + pieces[0]
	for _, unit := range SegmentUnits(scope, prefix, segments[index], 1, true) {
		if unit.RefPointer == pointer {
			return unit.DocID == docID, nil
		}
	}
	return false, errors.New("backup chunk unavailable")
}
