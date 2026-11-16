// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// initEpisodicIndex serializes schema inspection and migration across connections.
// A competing initializer may receive SQLITE_BUSY and safely retry the whole call.
func initEpisodicIndex(db *sql.DB) error {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if ready, rErr := episodicIndexReadyReadOnly(ctx, conn); rErr != nil {
		return rErr
	} else if ready {
		return nil
	}
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("thinclient: begin episodic index initialization: %w", err)
	}
	defer func() { _, _ = conn.ExecContext(ctx, `ROLLBACK`) }()

	var kind string
	err = conn.QueryRowContext(ctx, `SELECT type FROM sqlite_master WHERE name = 'thin_vault_episodic_index'`).Scan(&kind)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("thinclient: inspect episodic index: %w", err)
	}
	if err == nil {
		if kind != "table" {
			return fmt.Errorf("thinclient: unrecognized episodic index schema: object is %s", kind)
		}
		legacy, err := inspectEpisodicIndexSchema(ctx, conn)
		if err != nil {
			return err
		}
		if legacy {
			// Refuse to discard unknown dependent schema objects while rebuilding.
			var dependent int
			if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE tbl_name = 'thin_vault_episodic_index' AND type IN ('index', 'trigger') AND sql IS NOT NULL`).Scan(&dependent); err != nil {
				return fmt.Errorf("thinclient: inspect episodic index dependencies: %w", err)
			}
			if dependent != 0 {
				return fmt.Errorf("thinclient: unrecognized episodic index schema: dependent indexes or triggers")
			}
			var sourceTrigger sql.NullString
			if err := conn.QueryRowContext(ctx, `SELECT (SELECT sql FROM sqlite_master WHERE type = 'trigger' AND name = 'thin_episodic_source_delete')`).Scan(&sourceTrigger); err != nil {
				return fmt.Errorf("thinclient: inspect episodic source trigger: %w", err)
			}
			for _, stmt := range []string{
				`DROP TRIGGER IF EXISTS thin_episodic_source_delete`,
				`CREATE TABLE thin_vault_episodic_index_migration (doc_id TEXT NOT NULL, ref_pointer TEXT NOT NULL, codec_key BLOB NOT NULL, payload BLOB NOT NULL, content_digest BLOB, PRIMARY KEY (doc_id, codec_key))`,
				`INSERT INTO thin_vault_episodic_index_migration(doc_id, ref_pointer, codec_key, payload, content_digest) SELECT doc_id, ref_pointer, codec_key, payload, NULL FROM thin_vault_episodic_index`,
				`DROP TABLE thin_vault_episodic_index`,
				`ALTER TABLE thin_vault_episodic_index_migration RENAME TO thin_vault_episodic_index`,
			} {
				if _, err := conn.ExecContext(ctx, stmt); err != nil {
					return fmt.Errorf("thinclient: migrate episodic index: %w", err)
				}
			}
			if sourceTrigger.Valid {
				if _, err := conn.ExecContext(ctx, sourceTrigger.String); err != nil {
					return fmt.Errorf("thinclient: restore episodic source trigger: %w", err)
				}
			}
		}
		var hasMAC int
		if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_xinfo('thin_vault_episodic_index') WHERE name = 'payload_mac'`).Scan(&hasMAC); err != nil {
			return fmt.Errorf("thinclient: inspect episodic index MAC: %w", err)
		}
		if hasMAC == 0 {
			if _, err := conn.ExecContext(ctx, `ALTER TABLE thin_vault_episodic_index ADD COLUMN payload_mac BLOB`); err != nil {
				return fmt.Errorf("thinclient: migrate episodic index MAC: %w", err)
			}
		}
	}
	if _, err := conn.ExecContext(ctx, episodicIndexDDL); err != nil {
		return fmt.Errorf("thinclient: initialize episodic index: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("thinclient: commit episodic index initialization: %w", err)
	}
	return nil
}

func inspectEpisodicIndexSchema(ctx context.Context, conn *sql.Conn) (bool, error) {
	rows, err := conn.QueryContext(ctx, `PRAGMA table_xinfo('thin_vault_episodic_index')`)
	if err != nil {
		return false, fmt.Errorf("thinclient: inspect episodic index columns: %w", err)
	}
	defer rows.Close()
	type column struct {
		typ     string
		notNull int
		pk      int
		hidden  int
		def     sql.NullString
	}
	columns := make(map[string]column)
	for rows.Next() {
		var cid int
		var name string
		var c column
		if err := rows.Scan(&cid, &name, &c.typ, &c.notNull, &c.def, &c.pk, &c.hidden); err != nil {
			return false, fmt.Errorf("thinclient: inspect episodic index column: %w", err)
		}
		columns[name] = c
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("thinclient: inspect episodic index columns: %w", err)
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	var foreignKeys int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_list('thin_vault_episodic_index')`).Scan(&foreignKeys); err != nil {
		return false, fmt.Errorf("thinclient: inspect episodic index foreign keys: %w", err)
	}
	if foreignKeys != 0 {
		return false, fmt.Errorf("thinclient: unrecognized episodic index schema: foreign keys")
	}
	matches := func(name, typ string, notNull, pk int) bool {
		c, ok := columns[name]
		return ok && strings.EqualFold(c.typ, typ) && c.notNull == notNull && c.pk == pk && c.hidden == 0 && !c.def.Valid
	}
	common := matches("ref_pointer", "TEXT", 1, 0) && matches("payload", "BLOB", 1, 0)
	legacy := len(columns) == 4 && common && matches("doc_id", "TEXT", 0, 1) && matches("codec_key", "BLOB", 1, 0)
	current := common && matches("doc_id", "TEXT", 1, 1) && matches("codec_key", "BLOB", 1, 2) && matches("content_digest", "BLOB", 0, 0) &&
		(len(columns) == 5 || (len(columns) == 6 && matches("payload_mac", "BLOB", 0, 0)))
	if !legacy && !current {
		return false, fmt.Errorf("thinclient: unrecognized episodic index schema")
	}
	return legacy, nil
}

func episodicIndexReadyReadOnly(ctx context.Context, conn *sql.Conn) (bool, error) {
	var kind string
	err := conn.QueryRowContext(ctx, `SELECT type FROM sqlite_master WHERE name = 'thin_vault_episodic_index'`).Scan(&kind)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, fmt.Errorf("thinclient: inspect episodic index: %w", err)
	}
	if kind != "table" {
		return false, fmt.Errorf("thinclient: unrecognized episodic index schema: object is %s", kind)
	}
	legacy, err := inspectEpisodicIndexSchema(ctx, conn)
	if err != nil {
		return false, err
	}
	if legacy {
		return false, nil
	}
	var hasMAC int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_xinfo('thin_vault_episodic_index') WHERE name = 'payload_mac'`).Scan(&hasMAC); err != nil || hasMAC == 0 {
		return false, nil
	}
	var objCount int
	if err := conn.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sqlite_master
		WHERE (type = 'table' AND name = 'thin_vault_episodic_revision')
		   OR (type = 'trigger' AND name IN ('thin_episodic_index_insert', 'thin_episodic_index_update', 'thin_episodic_index_delete', 'thin_episodic_source_delete'))
	`).Scan(&objCount); err != nil || objCount != 5 {
		return false, nil
	}
	var revRow int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM thin_vault_episodic_revision WHERE id = 1`).Scan(&revRow); err != nil || revRow != 1 {
		return false, nil
	}
	return true, nil
}
