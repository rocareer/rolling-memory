// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package store

import (
	"database/sql"
	"math"
	"strings"

	"github.com/rocareer/rolling-memory/internal/clock"
	"github.com/rocareer/rolling-memory/internal/db"
	"github.com/rocareer/rolling-memory/internal/ids"
)

// ActivationDecayPerHour is the salience decay constant (λ=0.995/h).
const ActivationDecayPerHour = 0.995

// decayedActivation applies the lazy λ^Δt decay at read time (never at write
// time), so a stored score is only ever relaxed, never rewritten.
func (s *Store) decayedActivation(score float64, updatedAt, now int64) float64 {
	hours := math.Max(0.0, float64(now-updatedAt)/3600.0)
	return score * math.Pow(ActivationDecayPerHour, hours)
}

// GetActivation returns the lazily decayed activation scores (λ=0.995/h,
// salience only, never confidence). The id list is deduplicated (≤512).
func (s *Store) GetActivation(scope string, nodeIDs []string, now *int64) (map[string]float64, error) {
	values := []string{}
	for _, value := range ids.UniqueOrdered(nodeIDs) {
		if value != "" {
			values = append(values, value)
		}
	}
	if len(values) > 512 {
		values = values[:512]
	}
	if len(values) == 0 {
		return map[string]float64{}, nil
	}
	reference := clock.Unix()
	if now != nil {
		reference = *now
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(values)), ",")
	args := make([]any, 0, len(values)+1)
	args = append(args, scope)
	for _, value := range values {
		args = append(args, value)
	}
	rows, err := queryMaps(s.conn,
		`SELECT node_id, score, updated_at FROM activation
		 WHERE scope = ? AND node_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	result := make(map[string]float64, len(rows))
	for _, row := range rows {
		result[mapString(row, "node_id")] = s.decayedActivation(
			mapFloat(row, "score"), mapInt64(row, "updated_at"), reference,
		)
	}
	return result, nil
}

// BumpActivation accumulates activation with lazy decay (delivered-hit ledger;
// salience only, never confidence). The id list is deduplicated (≤256); the
// returned count is the number of ids written, not the number of changed rows.
func (s *Store) BumpActivation(scope string, nodeIDs []string, amount float64, now *int64) (int, error) {
	values := []string{}
	for _, value := range ids.UniqueOrdered(nodeIDs) {
		if value != "" {
			values = append(values, value)
		}
	}
	if len(values) > 256 {
		values = values[:256]
	}
	if len(values) == 0 {
		return 0, nil
	}
	reference := clock.Unix()
	if now != nil {
		reference = *now
	}
	bumped := 0
	err := db.WithImmediateTx(s.conn, func(tx *sql.Tx) error {
		for _, nodeID := range values {
			var score float64
			var updatedAt int64
			err := tx.QueryRow(
				"SELECT score, updated_at FROM activation WHERE node_id = ? AND scope = ?",
				nodeID, scope,
			).Scan(&score, &updatedAt)
			base := 0.0
			switch {
			case err == nil:
				base = s.decayedActivation(score, updatedAt, reference)
			case err == sql.ErrNoRows:
				base = 0.0
			default:
				return err
			}
			var exists int
			if err := tx.QueryRow("SELECT 1 FROM node WHERE id = ?", nodeID).Scan(&exists); err != nil {
				if err == sql.ErrNoRows {
					continue
				}
				return err
			}
			if _, err := tx.Exec(
				`INSERT INTO activation(node_id, scope, score, updated_at) VALUES (?, ?, ?, ?)
					 ON CONFLICT(node_id, scope) DO UPDATE SET
					   score = excluded.score, updated_at = excluded.updated_at`,
				nodeID, scope, base+amount, reference,
			); err != nil {
				return err
			}
			bumped++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return bumped, nil
}

// RawError returns the per-read-chain raw-leg failure note (nil when clean).
//
// Loud raw errors: a failure is recorded instead of swallowed, and
// the result still ships (fail-open but visible).
func (s *Store) RawError() *string {
	s.rawErrorMu.Lock()
	defer s.rawErrorMu.Unlock()
	return s.rawError
}

// ClearRawError resets the raw-leg failure note; the read chain calls it once per
// recall because the store is a long-lived object.
func (s *Store) ClearRawError() {
	s.setRawError(nil)
}

func (s *Store) setRawError(msg *string) {
	s.rawErrorMu.Lock()
	s.rawError = msg
	s.rawErrorMu.Unlock()
}
