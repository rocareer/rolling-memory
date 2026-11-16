// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rocareer/rolling-memory/internal/ids"
)

// BackupReference describes an external dependency without copying its contents.
// Available only means that the path exists; the referenced content is not verified.
type BackupReference struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Reference string `json:"reference"`
	Path      string `json:"path,omitempty"`
	Status    string `json:"status"`
}

// BackupPreflightReport is an inventory, not a backup or a restore authorization.
type BackupPreflightReport struct {
	SelfContained bool              `json:"self_contained"`
	Complete      bool              `json:"complete"`
	References    []BackupReference `json:"references"`
	Warnings      []string          `json:"warnings"`
}

// BackupPreflight inventories known external references from an existing database.
// It never creates a database or output file, copies plaintext, or changes sources.
// SQLite is opened in read-only mode and the inventory uses one read transaction;
// external paths may change after inspection and their contents are not checked.
func BackupPreflight(ctx context.Context, path, zcodePath string) (BackupPreflightReport, error) {
	report := BackupPreflightReport{
		References: []BackupReference{},
		Warnings: []string{
			"preflight only: no backup or restore was performed",
			"database and source contents were not copied or verified; remote data and configuration are not inventoried",
			"path availability is not reference integrity and may change after inspection",
			"reference IDs and paths may contain sensitive information; handle this report privately",
		},
	}
	if path == "" || path == ":memory:" {
		return report, fmt.Errorf("backup preflight requires an existing database file")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return report, err
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return report, fmt.Errorf("backup preflight database: %w", err)
	}
	if !info.Mode().IsRegular() {
		return report, fmt.Errorf("backup preflight database is not a regular file")
	}
	dsn := ids.SQLiteFileURI(absPath) + "?mode=ro&_pragma=busy_timeout(15000)"
	connection, err := sql.Open("sqlite", dsn)
	if err != nil {
		return report, err
	}
	defer connection.Close()
	tx, err := connection.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return report, fmt.Errorf("backup preflight open database: %w", err)
	}
	defer tx.Rollback()

	for _, item := range []struct {
		table string
		query string
		kind  string
	}{
		{"thin_vault_docs", `SELECT doc_id, raw_text FROM thin_vault_docs WHERE substr(trim(raw_text), 1, 5) = '@ref:' ORDER BY doc_id`, "episodic"},
		{"thin_vault_bg_files", `SELECT abs_path, abs_path FROM thin_vault_bg_files ORDER BY abs_path`, "background_file"},
		{"thin_vault_bg_chunks", `SELECT unit_id, abs_path FROM thin_vault_bg_chunks ORDER BY unit_id`, "background_chunk"},
		{"thin_vault_bg_dirs", `SELECT dir_id, root_path FROM thin_vault_bg_dirs ORDER BY dir_id`, "background_directory"},
	} {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, item.table).Scan(&exists); err != nil {
			return report, fmt.Errorf("backup preflight schema: %w", err)
		}
		if exists == 0 {
			report.Warnings = append(report.Warnings, "table not present: "+item.table)
			continue
		}
		rows, err := tx.QueryContext(ctx, item.query)
		if err != nil {
			return report, fmt.Errorf("backup preflight %s: %w", item.table, err)
		}
		for rows.Next() {
			var id, ref string
			if err := rows.Scan(&id, &ref); err != nil {
				rows.Close()
				return report, fmt.Errorf("backup preflight %s: %w", item.table, err)
			}
			report.References = append(report.References, inspectBackupReference(item.kind, id, ref, zcodePath))
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return report, fmt.Errorf("backup preflight %s: %w", item.table, err)
		}
		if err := rows.Close(); err != nil {
			return report, err
		}
	}
	return report, nil
}

func inspectBackupReference(kind, id, ref, zcodePath string) BackupReference {
	entry := BackupReference{Kind: kind, ID: id, Reference: ref, Status: "unverified"}
	path := ref
	if kind == "episodic" {
		ref = strings.TrimSpace(ref)
		switch {
		case strings.HasPrefix(ref, "@ref:zcode:"):
			parts := strings.SplitN(strings.TrimPrefix(ref, "@ref:zcode:"), ":", 2)
			if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
				entry.Status = "invalid"
				return entry
			}
			path = zcodePath
		case strings.HasPrefix(ref, "@ref:jsonl:"):
			rest := strings.TrimPrefix(ref, "@ref:jsonl:")
			index := strings.LastIndex(rest, ":")
			if index <= 0 {
				entry.Status = "invalid"
				return entry
			}
			turn := strings.SplitN(rest[index+1:], "#", 2)[0]
			if n, err := strconv.Atoi(turn); err != nil || n < 0 {
				entry.Status = "invalid"
				return entry
			}
			path = rest[:index]
		default:
			entry.Status = "unsupported"
			return entry
		}
	}
	entry.Path = path
	if path == "" || !filepath.IsAbs(path) {
		entry.Status = "invalid"
		return entry
	}
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		entry.Status = "missing"
	} else if err != nil {
		entry.Status = "inaccessible"
	} else if (kind == "background_directory" && !info.IsDir()) || (kind != "background_directory" && !info.Mode().IsRegular()) {
		entry.Status = "invalid"
	} else {
		entry.Status = "path_present_unverified"
	}
	return entry
}
