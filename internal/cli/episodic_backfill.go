// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/rocareer/rolling-memory/internal/api"
	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/thinclient"
)

// The page size reuses the reconciliation budget. It bounds each tick, not the
// history covered: completed cycles restart and retry unavailable references.
func runEpisodicIndexBackfillTick(ctx context.Context, memory *api.Memory) (thinclient.EpisodicIndexBackfillReport, error) {
	var report thinclient.EpisodicIndexBackfillReport
	if memory == nil || memory.Store == nil || memory.Store.DB() == nil {
		return report, errors.New("episodic backfill requires an open local store")
	}
	if ctx == nil {
		return report, errors.New("episodic backfill requires a context")
	}
	scope := config.DefaultScope
	if memory.Config != nil && strings.TrimSpace(memory.Config.Scope) != "" {
		scope = strings.TrimSpace(memory.Config.Scope)
	}
	tc, err := memory.ThinClient()
	if err != nil {
		return report, err
	}
	if tc == nil || tc.Vault == nil {
		return report, errors.New("episodic backfill requires a local vault")
	}
	db := memory.Store.DB()
	// Scope-specific cursor prevents a change of configured scope from skipping
	// earlier references; values contain no source text.
	key := "episodic_index_backfill_cursor:" + scope
	var cursor string
	err = db.QueryRowContext(ctx, `SELECT v FROM thin_vault_meta WHERE k = ?`, key).Scan(&cursor)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return report, err
	}
	exists := err == nil
	passCtx, cancel := context.WithTimeout(ctx, autoWatchReconcileBudget)
	report, backfillErr := tc.Vault.BackfillEpisodicIndexes(passCtx, scope, cursor, reconcileDefaultLimit)
	cancel()
	next := report.Cursor
	if backfillErr == nil && report.Scanned < reconcileDefaultLimit {
		next = ""
	}
	// Persist progress even if processing was interrupted; replay is safe if this
	// write fails. Use the parent context so a timed-out page can still checkpoint.
	if err := saveEpisodicCheckpoint(db, key, []byte(cursor), exists, []byte(next)); err != nil {
		return report, errors.Join(backfillErr, fmt.Errorf("episodic backfill checkpoint: %w", err))
	}
	return report, backfillErr
}

// saveEpisodicCheckpoint prevents a stale pass from overwriting another pass's progress,
// including a completed-cycle reset. A conflict is retried by a later tick.
func saveEpisodicCheckpoint(db *sql.DB, key string, previous []byte, exists bool, next []byte) error {
	var result sql.Result
	var err error
	if exists {
		result, err = db.Exec(`UPDATE thin_vault_meta SET v = ? WHERE k = ? AND CAST(v AS BLOB) = ?`, next, key, previous)
	} else {
		result, err = db.Exec(`INSERT INTO thin_vault_meta(k, v) VALUES (?, ?) ON CONFLICT(k) DO NOTHING`, key, next)
	}
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		return errors.New("checkpoint changed during pass")
	}
	return nil
}
