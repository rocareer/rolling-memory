// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rocareer/rolling-memory/internal/ids"
)

// VaultScopePreflight checks an existing database without assigning its scope.
// Unmarked databases with any user rows must not be adopted automatically.
func VaultScopePreflight(path, scope string) error {
	if strings.TrimSpace(scope) == "" {
		return fmt.Errorf("vault requires a scope")
	}
	if path == ":memory:" {
		return nil
	}
	path = expandHome(path)
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	info, err := os.Stat(absPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("vault database is not a regular file")
	}
	connection, err := sql.Open("sqlite", ids.SQLiteFileURI(absPath)+"?mode=ro&_pragma=busy_timeout(15000)")
	if err != nil {
		return err
	}
	defer connection.Close()
	_, _ = connection.Exec("PRAGMA busy_timeout = 15000")
	tx, err := connection.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("vault scope preflight: %w", err)
	}
	defer tx.Rollback()
	return checkVaultScope(tx, strings.TrimSpace(scope))
}

// BindVaultScope checks and claims an empty vault in the same immediate transaction.
// The caller must use a connection configured with _txlock=immediate.
func BindVaultScope(connection *sql.DB, scope string) error {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return fmt.Errorf("vault requires a scope")
	}
	var metaExists int
	if err := connection.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'thin_vault_meta')`).Scan(&metaExists); err == nil && metaExists != 0 {
		var bound, storageType string
		if err := connection.QueryRow(`SELECT v, typeof(v) FROM thin_vault_meta WHERE k = 'local_plain_scope'`).Scan(&bound, &storageType); err == nil {
			if storageType != "text" || bound != scope {
				return fmt.Errorf("vault scope does not match its binding")
			}
			return nil
		}
	}
	return WithImmediateTx(connection, func(tx *sql.Tx) error {
		if err := checkVaultScope(tx, scope); err != nil {
			return err
		}
		if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS thin_vault_meta (k TEXT PRIMARY KEY, v BLOB NOT NULL)`); err != nil {
			return fmt.Errorf("vault scope preflight: %w", err)
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO thin_vault_meta(k, v) VALUES ('local_plain_scope', ?)`, scope); err != nil {
			return fmt.Errorf("vault scope preflight: %w", err)
		}
		return checkVaultScope(tx, scope)
	})
}

func checkVaultScope(tx *sql.Tx, scope string) error {
	var metaExists int
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'thin_vault_meta')`).Scan(&metaExists); err != nil {
		return fmt.Errorf("vault scope preflight: %w", err)
	}
	if metaExists != 0 {
		var bound, storageType string
		err := tx.QueryRow(`SELECT v, typeof(v) FROM thin_vault_meta WHERE k = 'local_plain_scope'`).Scan(&bound, &storageType)
		if err == nil {
			if storageType != "text" || bound != scope {
				return fmt.Errorf("vault scope does not match its binding")
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("vault scope preflight: %w", err)
		}
	}
	var integrity string
	if err := tx.QueryRow(`PRAGMA quick_check`).Scan(&integrity); err != nil {
		return fmt.Errorf("vault scope preflight: %w", err)
	}
	if integrity != "ok" {
		return fmt.Errorf("vault scope preflight: database integrity check failed")
	}
	if metaExists != 0 {
		// Existing first-party personal vaults (scope "default" or "mem:self") initialized
		// before local_plain_scope existed already carry a 32-byte user_secret_key blob in
		// thin_vault_meta; allow seamless upgrade binding for the default personal scope.
		if scope == "default" || scope == "mem:self" {
			var keyLen int
			var keyType string
			if kErr := tx.QueryRow(`SELECT length(v), typeof(v) FROM thin_vault_meta WHERE k = 'user_secret_key'`).Scan(&keyLen, &keyType); kErr == nil && keyType == "blob" && keyLen == 32 {
				return nil
			}
		}
	}
	hasEngineMeta := false
	var hasDBMeta int
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'db_meta')`).Scan(&hasDBMeta); err == nil && hasDBMeta != 0 {
		var engine string
		if eErr := tx.QueryRow(`SELECT value FROM db_meta WHERE key = 'engine'`).Scan(&engine); eErr == nil && engine == "rolling-memory" {
			hasEngineMeta = true
			if scope == "default" || scope == "mem:self" {
				return nil
			}
		}
	}
	rows, err := tx.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return fmt.Errorf("vault scope preflight: %w", err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	scopedLegacyTables := map[string]bool{
		"trace": true, "node": true, "assertion": true, "event": true,
		"replay_state": true, "session_cursor": true,
		"mention": true, "meta_edge": true, "activation": true,
	}
	unscopedLegacyTables := map[string]bool{
		"db_meta": true, "node_alias": true, "sync_state": true, "injection_ledger": true,
		"vector": true, "transcript_cursor": true,
		"assertion_fts": true, "assertion_fts_data": true, "assertion_fts_idx": true,
		"assertion_fts_content": true, "assertion_fts_docsize": true, "assertion_fts_config": true,
		"trace_fts": true, "trace_fts_data": true, "trace_fts_idx": true,
		"trace_fts_content": true, "trace_fts_docsize": true, "trace_fts_config": true,
		"node_fts": true, "node_fts_data": true, "node_fts_idx": true,
		"node_fts_content": true, "node_fts_docsize": true, "node_fts_config": true,
	}
	hasSecretKeyOnlyMeta := false
	if metaExists != 0 {
		var metaCount, keyLen int
		var keyType string
		if cErr := tx.QueryRow(`SELECT count(*) FROM thin_vault_meta`).Scan(&metaCount); cErr == nil && metaCount == 1 {
			if kErr := tx.QueryRow(`SELECT length(v), typeof(v) FROM thin_vault_meta WHERE k = 'user_secret_key'`).Scan(&keyLen, &keyType); kErr == nil && keyType == "blob" && keyLen == 32 {
				var docsCnt, mapCnt int
				_ = tx.QueryRow(`SELECT count(*) FROM thin_vault_docs`).Scan(&docsCnt)
				_ = tx.QueryRow(`SELECT count(*) FROM thin_vault_fts_map`).Scan(&mapCnt)
				if docsCnt == 0 && mapCnt == 0 {
					hasSecretKeyOnlyMeta = true
				}
			}
		}
	}
	allLegacyScopedMatch := hasEngineMeta
	for _, name := range tables {
		if hasEngineMeta && name == "db_meta" {
			continue
		}
		if hasSecretKeyOnlyMeta && (name == "thin_vault_meta" || strings.HasPrefix(name, "thin_vault_docs_fts")) {
			continue
		}
		quoted := `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
		var populated int
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM ` + quoted + `)`).Scan(&populated); err != nil {
			return fmt.Errorf("vault scope preflight table %q: %w", name, err)
		}
		if populated == 0 {
			continue
		}
		if allLegacyScopedMatch {
			if scopedLegacyTables[name] {
				var mismatch int
				if mErr := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM `+quoted+` WHERE scope != ?)`, scope).Scan(&mismatch); mErr == nil && mismatch == 0 {
					continue
				}
			} else if unscopedLegacyTables[name] {
				continue
			}
		}
		return fmt.Errorf("local_plain vault scope is unbound; existing data is preserved; Back up the database and referenced sources, independently verify the ownership of all records, and obtain explicit authorization before a non-destructive scope binding")
	}
	return nil
}
