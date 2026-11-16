// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package store

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/rocareer/rolling-memory/internal/clock"
	"github.com/rocareer/rolling-memory/internal/db"
	"github.com/rocareer/rolling-memory/internal/ids"
)

// AddTrace inserts one raw evidence row and reports whether it was new.
func (s *Store) AddTrace(scope, text string, o TraceOptions) (string, bool, error) {
	clean := pyStrip(strings.ReplaceAll(text, "\x00", " "))
	if clean == "" {
		return "", false, fmt.Errorf("empty trace text")
	}
	session := defaultIfEmpty(o.Session, "default")
	kind := defaultIfEmpty(o.Kind, "message")
	audience := defaultIfEmpty(o.Audience, "shared")
	if _, ok := ids.AudienceRank[audience]; !ok {
		return "", false, fmt.Errorf("invalid audience %s", pyRepr(audience))
	}
	turn := ids.NormalizeTurn(o.Turn)
	now := clock.Unix()
	occurred := now
	if o.OccurredAt != nil {
		occurred = *o.OccurredAt
	}
	recorded := now
	if o.RecordedAt != nil {
		recorded = *o.RecordedAt
	}
	digest := ids.TraceHash(scope, session, turn, clean)
	meta := o.Meta
	if meta == nil {
		meta = map[string]any{}
	}
	metaJSON, err := ids.PyJSONDump(meta, false, true, false)
	if err != nil {
		return "", false, err
	}

	inserted := false
	err = db.WithImmediateTx(s.conn, func(tx *sql.Tx) error {
		result, execErr := tx.Exec(
			`INSERT OR IGNORE INTO trace
			   (hash, scope, session, turn, kind, author, audience, text,
			    occurred_at, recorded_at, meta_json, processed)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`,
			digest, scope, session, turn, kind, o.Author, audience, clean,
			occurred, recorded, metaJSON,
		)
		if execErr != nil {
			return execErr
		}
		affected, execErr := result.RowsAffected()
		if execErr != nil {
			return execErr
		}
		inserted = affected > 0
		if !inserted {
			var existingAudience string
			if execErr := tx.QueryRow(`SELECT audience FROM trace WHERE hash = ?`, digest).Scan(&existingAudience); execErr != nil {
				return execErr
			}
			if existingAudience != audience {
				return fmt.Errorf("duplicate trace audience conflict: existing vault copies may require review")
			}
		}
		if inserted && s.fts {
			if _, execErr := tx.Exec(
				"INSERT INTO trace_fts(text, trace_hash) VALUES (?, ?)", clean, digest,
			); execErr != nil {
				return execErr
			}
		}
		if inserted {
			createdBy := o.Author
			if createdBy == "" {
				createdBy = "system"
			}
			_, execErr := insertEvent(tx, scope, "trace.add", map[string]any{
				"trace":   digest,
				"session": session,
				"turn":    turn,
			}, "", createdBy, true)
			return execErr
		}
		return nil
	})
	if err != nil {
		return "", false, err
	}
	return digest, inserted, nil
}

// Trace returns one trace row.
func (s *Store) Trace(hash string) (Trace, bool, error) {
	row, err := queryOneMap(s.conn, "SELECT * FROM trace WHERE hash = ?", hash)
	if err != nil {
		return Trace{}, false, err
	}
	if row == nil {
		return Trace{}, false, nil
	}
	return traceFromMap(row), true, nil
}

// PendingTraces lists unprocessed traces in recorded/turn order.
func (s *Store) PendingTraces(scope string, limit int, session *string) ([]Trace, error) {
	if limit <= 0 {
		limit = 60
	}
	var rows []map[string]any
	var err error
	if session != nil && *session != "" {
		rows, err = queryMaps(s.conn,
			"SELECT * FROM trace WHERE scope = ? AND session = ? AND processed = 0 ORDER BY recorded_at, turn LIMIT ?",
			scope, *session, limit)
	} else {
		rows, err = queryMaps(s.conn,
			"SELECT * FROM trace WHERE scope = ? AND processed = 0 ORDER BY recorded_at, turn LIMIT ?",
			scope, limit)
	}
	if err != nil {
		return nil, err
	}
	result := make([]Trace, 0, len(rows))
	for _, row := range rows {
		result = append(result, traceFromMap(row))
	}
	return result, nil
}

// PendingVaultTraces takes one finite database snapshot of the unmirrored traces
// whose audience can be represented safely in the local vault. Keying off
// thin_vault_docs existence rather than trace.processed decouples vault mirroring
// from assertion-graph replay and avoids re-scanning already-mirrored history.
func (s *Store) PendingVaultTraces(scope string, session *string) ([]Trace, error) {
	var hasVaultDocs int
	if err := s.conn.QueryRow(
		`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'thin_vault_docs')`,
	).Scan(&hasVaultDocs); err != nil {
		return nil, err
	}
	query := "SELECT * FROM trace WHERE scope = ? AND audience IN ('shared', 'public')"
	if hasVaultDocs != 0 {
		query += " AND NOT EXISTS (SELECT 1 FROM thin_vault_docs d WHERE d.doc_id = 'ep_' || trace.hash)"
		// 用户显式删除的单元（thin_vault_deleted_units 墓碑）不得因金库行消失而被判为「待镜像」重新写回：
		// 删除意图优先于 mirror 状态推导，否则下一次 import 会把已删除的记忆整批复活。
		var hasTombstones int
		if err := s.conn.QueryRow(
			`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'thin_vault_deleted_units')`,
		).Scan(&hasTombstones); err != nil {
			return nil, err
		}
		if hasTombstones != 0 {
			query += " AND NOT EXISTS (SELECT 1 FROM thin_vault_deleted_units t WHERE t.unit_id = 'ep_' || trace.hash)"
		}
	} else {
		query += " AND processed = 0"
	}
	args := []any{scope}
	if session != nil && *session != "" {
		query += " AND session = ?"
		args = append(args, *session)
	}
	query += " ORDER BY recorded_at, turn, hash"
	rows, err := queryMaps(s.conn, query, args...)
	if err != nil {
		return nil, err
	}
	result := make([]Trace, 0, len(rows))
	for _, row := range rows {
		result = append(result, traceFromMap(row))
	}
	return result, nil
}

// MarkProcessed flips one trace to processed=1.
func (s *Store) MarkProcessed(hash string) error {
	_, err := s.conn.Exec(
		"UPDATE trace SET processed = 1, processed_at = ? WHERE hash = ? AND processed = 0",
		clock.Unix(), hash,
	)
	return err
}

// UpdateReplayState advances the per-scope replay watermark monotonically.
func (s *Store) UpdateReplayState(scope string, producedThrough int64, lastHash string) error {
	_, err := s.conn.Exec(
		`INSERT INTO replay_state(scope, produced_through, last_trace_hash, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(scope) DO UPDATE SET
		   produced_through = max(replay_state.produced_through, excluded.produced_through),
		   last_trace_hash = CASE
		     WHEN excluded.produced_through >= replay_state.produced_through THEN excluded.last_trace_hash
		     ELSE replay_state.last_trace_hash END,
		   updated_at = excluded.updated_at`,
		scope, producedThrough, lastHash, clock.Unix(),
	)
	return err
}

// UpdateSessionCursor advances the per-session turn cursor monotonically.
func (s *Store) UpdateSessionCursor(scope, session string, turnDone int64) error {
	_, err := s.conn.Exec(
		`INSERT INTO session_cursor(scope, session, turn_done, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(scope, session) DO UPDATE SET
		   turn_done = max(session_cursor.turn_done, excluded.turn_done),
		   updated_at = excluded.updated_at`,
		scope, session, turnDone, clock.Unix(),
	)
	return err
}

// RewindSession is the governed replay rewind: reset the session cursor and
// make its processed traces pending again, keeping the graph untouched.
func (s *Store) RewindSession(scope, session, reason, createdBy string) (map[string]any, error) {
	createdBy = defaultIfEmpty(createdBy, "user")
	if session == "" {
		return nil, fmt.Errorf("rewind requires a session")
	}
	if pyStrip(reason) == "" {
		return nil, fmt.Errorf("rewind requires a reason (append-only audit)")
	}
	now := clock.Unix()
	reset := int64(0)
	err := db.WithImmediateTx(s.conn, func(tx *sql.Tx) error {
		result, execErr := tx.Exec(
			"UPDATE trace SET processed = 0, processed_at = NULL WHERE scope = ? AND session = ? AND processed = 1",
			scope, session,
		)
		if execErr != nil {
			return execErr
		}
		reset, execErr = result.RowsAffected()
		if execErr != nil {
			return execErr
		}
		if _, execErr := tx.Exec(
			`INSERT INTO session_cursor(scope, session, turn_done, updated_at)
			 VALUES (?, ?, -1, ?)
			 ON CONFLICT(scope, session) DO UPDATE SET
			   turn_done = -1,
			   updated_at = excluded.updated_at`,
			scope, session, now,
		); execErr != nil {
			return execErr
		}
		_, execErr = insertEvent(tx, scope, "replay.rewind", map[string]any{
			"session":      session,
			"reset_traces": reset,
		}, reason, createdBy, true)
		return execErr
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"ok":           true,
		"scope":        scope,
		"session":      session,
		"reset_traces": reset,
		"reason":       reason,
	}, nil
}
