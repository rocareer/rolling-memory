// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package efficacy

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rocareer/rolling-memory/internal/db"
)

const ledgerVersion = 1

// LedgerEvent records an observed hook action, not proof that a host consumed it.
// Corrections and retractions are new events referring to the original event ID.
// Text is never stored; TextSHA256 and TextBytes must both be present or absent.
type LedgerEvent struct {
	EventID       string `json:"event_id"`
	ExecutionID   string `json:"execution_id"`
	Session       string `json:"session"` // empty means unknown
	Scope         string `json:"scope"`   // empty means unknown
	Harness       string `json:"harness"` // empty means unknown
	Hook          string `json:"hook"`
	OutputOutcome string `json:"output_outcome"` // not_applicable, output, empty, error, success, failure, short_write, unknown
	Action        string `json:"action"`         // decision, prepared, output_attempt, correction, retraction
	TargetEventID string `json:"target_event_id,omitempty"`
	ObservedAt    int64  `json:"observed_at"` // Unix nanoseconds
	Observation   string `json:"observation"` // observed or unknown
	TextSHA256    string `json:"text_sha256,omitempty"`
	TextBytes     *int64 `json:"text_bytes,omitempty"`
}

// Ledger stores append-only hook observation events in a separate SQLite file.
type Ledger struct {
	db *sql.DB
}

// DefaultLedgerPath returns a separate ledger path for a persistent memory DB.
func DefaultLedgerPath(memoryDBPath string) (string, error) {
	if strings.TrimSpace(memoryDBPath) == "" || memoryDBPath == ":memory:" || strings.HasPrefix(memoryDBPath, "file:") || strings.ContainsAny(memoryDBPath, "?#") {
		return "", errors.New("ledger requires a filesystem memory database path")
	}
	path, err := filepath.Abs(memoryDBPath)
	if err != nil {
		return "", err
	}
	return path + ".efficacy.sqlite", nil
}

// OpenLedger opens an explicitly chosen ledger path with a short lock budget.
// It never imports legacy evaluation caches and rejects unsupported versions.
func OpenLedger(path string) (*Ledger, error) {
	if strings.TrimSpace(path) == "" || path == ":memory:" || strings.HasPrefix(path, "file:") || strings.ContainsAny(path, "?#") {
		return nil, errors.New("ledger requires an explicit persistent filesystem path")
	}
	if info, err := os.Stat(path); err == nil && !info.Mode().IsRegular() {
		return nil, errors.New("ledger path is not a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	profile := db.LocalProfile()
	profile.BusyTimeoutMS = 50
	connection, err := db.Connect(path, profile)
	if err != nil {
		return nil, err
	}
	ledger := &Ledger{db: connection}
	if err := ledger.initialize(); err != nil {
		connection.Close()
		return nil, err
	}
	return ledger, nil
}

func (l *Ledger) initialize() error {
	var version int
	if err := l.db.QueryRow("PRAGMA user_version").Scan(&version); err == nil && version == ledgerVersion {
		var readyCnt int
		if l.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name IN ('efficacy_meta', 'efficacy_event_scope_harness', 'efficacy_event_target')").Scan(&readyCnt) == nil && readyCnt == 3 {
			return nil
		}
	}
	tx, err := l.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version != 0 && version != ledgerVersion {
		return fmt.Errorf("unsupported efficacy ledger version %d", version)
	}
	if version == 0 {
		var count int
		if err := tx.QueryRow("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'").Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return errors.New("unversioned nonempty efficacy ledger")
		}
		if _, err := tx.Exec(`CREATE TABLE efficacy_event (
			seq INTEGER PRIMARY KEY AUTOINCREMENT,
			event_id TEXT NOT NULL UNIQUE,
			execution_id TEXT NOT NULL,
			session TEXT NOT NULL,
			scope TEXT NOT NULL,
			harness TEXT NOT NULL,
			hook TEXT NOT NULL,
			output_outcome TEXT NOT NULL,
			action TEXT NOT NULL,
			target_event_id TEXT NOT NULL,
			observed_at INTEGER NOT NULL,
			observation TEXT NOT NULL,
			text_sha256 TEXT NOT NULL,
			text_bytes INTEGER
		);
		CREATE INDEX efficacy_event_execution ON efficacy_event(scope, session, execution_id, seq);
		PRAGMA user_version = 1`); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS efficacy_meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS efficacy_event_scope_harness ON efficacy_event(scope, harness, seq);
			CREATE INDEX IF NOT EXISTS efficacy_event_target ON efficacy_event(target_event_id, seq)`); err != nil {
		return err
	}
	return tx.Commit()
}

// ComputeDBFingerprintFromPath computes the read-only fingerprint for a memory DB file.
func ComputeDBFingerprintFromPath(memoryDBPath, scope string) string {
	if strings.TrimSpace(memoryDBPath) == "" || memoryDBPath == ":memory:" || strings.HasPrefix(memoryDBPath, "file:") || strings.ContainsAny(memoryDBPath, "?#") {
		return ""
	}
	absPath, err := filepath.Abs(memoryDBPath)
	if err != nil {
		return ""
	}
	info, err := os.Stat(absPath)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	conn, err := sql.Open("sqlite", absPath+"?mode=ro&_pragma=busy_timeout(100)")
	if err != nil {
		return ""
	}
	defer conn.Close()
	return ComputeDBFingerprint(conn, scope)
}

// BindDatabaseFingerprint binds the ledger to a specific memory database fingerprint,
// or verifies that an already-bound ledger matches the expected fingerprint.
func (l *Ledger) BindDatabaseFingerprint(fp string) error {
	fp = strings.TrimSpace(fp)
	if fp == "" {
		return errors.New("empty database fingerprint")
	}
	tx, err := l.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var existing string
	err = tx.QueryRow(`SELECT value FROM efficacy_meta WHERE key = 'db_fingerprint'`).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.Exec(`INSERT INTO efficacy_meta(key, value) VALUES ('db_fingerprint', ?)`, fp); err != nil {
			return err
		}
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	if existing != fp {
		return errors.New("ledger database fingerprint mismatch")
	}
	return nil
}

// Close closes the ledger connection.
func (l *Ledger) Close() error { return l.db.Close() }

// OpenBoundLedger opens the ledger bound to fp. The ledger is a derived artifact:
// when the stored fingerprint belongs to another database (or to the same database
// before a schema-era change), the stale ledger is rotated aside and a fresh epoch
// starts instead of mixing unrelated facts under one identity.
func OpenBoundLedger(path, fp string) (*Ledger, error) {
	if strings.TrimSpace(fp) == "" {
		return nil, errors.New("empty database fingerprint")
	}
	ledger, err := OpenLedger(path)
	if err != nil {
		return nil, err
	}
	if err := ledger.BindDatabaseFingerprint(fp); err == nil {
		return ledger, nil
	}
	if err := ledger.Close(); err != nil {
		return nil, err
	}
	if err := rotateLedgerEpoch(path); err != nil {
		return nil, err
	}
	ledger, err = OpenLedger(path)
	if err != nil {
		return nil, err
	}
	if err := ledger.BindDatabaseFingerprint(fp); err != nil {
		ledger.Close()
		return nil, err
	}
	return ledger, nil
}

// rotateLedgerEpoch archives the current ledger files beside themselves (never
// deleting them) so a new fingerprint epoch can start on a clean file.
func rotateLedgerEpoch(path string) error {
	baseSuffix := fmt.Sprintf(".epoch-%d", time.Now().Unix())
	suffix := baseSuffix
	for seq := 1; ; seq++ {
		if _, err := os.Stat(path + suffix); errors.Is(err, os.ErrNotExist) {
			break
		}
		suffix = fmt.Sprintf("%s-%d", baseSuffix, seq)
	}
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		if _, err := os.Stat(candidate); err != nil {
			continue
		}
		if err := os.Rename(candidate, candidate+suffix); err != nil {
			return err
		}
	}
	return nil
}

// Append adds one event atomically. An identical event ID is an idempotent
// retry; reusing an ID with different contents is an error.
func (l *Ledger) Append(event LedgerEvent) error {
	if err := validateLedgerEvent(event); err != nil {
		return err
	}
	tx, err := l.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if event.TargetEventID != "" {
		var execution, session, scope, harness, hook, action string
		err := tx.QueryRow(`SELECT execution_id, session, scope, harness, hook, action FROM efficacy_event WHERE event_id = ?`, event.TargetEventID).Scan(&execution, &session, &scope, &harness, &hook, &action)
		if err != nil {
			return fmt.Errorf("ledger target: %w", err)
		}
		if execution != event.ExecutionID || session != event.Session || scope != event.Scope || harness != event.Harness || hook != event.Hook || (action != "decision" && action != "prepared" && action != "output_attempt" && action != "recall") {
			return errors.New("ledger target does not match event identity or original action")
		}
		if event.Action == "correction" {
			corrected := event
			corrected.Action, corrected.TargetEventID = action, ""
			if err := validateLedgerEvent(corrected); err != nil {
				return fmt.Errorf("ledger correction does not match original action: %w", err)
			}
		}
	}
	result, err := tx.Exec(`INSERT OR IGNORE INTO efficacy_event
		(event_id, execution_id, session, scope, harness, hook, output_outcome, action, target_event_id, observed_at, observation, text_sha256, text_bytes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, event.EventID, event.ExecutionID, event.Session, event.Scope, event.Harness, event.Hook, event.OutputOutcome, event.Action, event.TargetEventID, event.ObservedAt, event.Observation, event.TextSHA256, event.TextBytes)
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if inserted == 0 {
		var stored LedgerEvent
		var textBytes sql.NullInt64
		err := tx.QueryRow(`SELECT event_id, execution_id, session, scope, harness, hook, output_outcome, action, target_event_id, observed_at, observation, text_sha256, text_bytes FROM efficacy_event WHERE event_id = ?`, event.EventID).Scan(
			&stored.EventID, &stored.ExecutionID, &stored.Session, &stored.Scope, &stored.Harness, &stored.Hook, &stored.OutputOutcome, &stored.Action, &stored.TargetEventID, &stored.ObservedAt, &stored.Observation, &stored.TextSHA256, &textBytes)
		if err != nil {
			return err
		}
		if textBytes.Valid {
			stored.TextBytes = &textBytes.Int64
		}
		if !sameLedgerEvent(stored, event) {
			return errors.New("ledger event ID conflicts with existing event")
		}
	}
	return tx.Commit()
}

func sameLedgerEvent(a, b LedgerEvent) bool {
	if (a.TextBytes == nil) != (b.TextBytes == nil) {
		return false
	}
	if a.TextBytes != nil && *a.TextBytes != *b.TextBytes {
		return false
	}
	a.TextBytes, b.TextBytes = nil, nil
	return a == b
}

func validateLedgerEvent(e LedgerEvent) error {
	if strings.TrimSpace(e.EventID) == "" || strings.TrimSpace(e.ExecutionID) == "" || strings.TrimSpace(e.Hook) == "" || e.ObservedAt <= 0 {
		return errors.New("ledger event is missing identity or timestamp")
	}
	switch e.Action {
	case "decision", "prepared", "output_attempt", "recall":
		if e.TargetEventID != "" {
			return errors.New("original event cannot have a target")
		}
	case "correction", "retraction":
		if strings.TrimSpace(e.TargetEventID) == "" || e.TargetEventID == e.EventID {
			return errors.New("correction or retraction requires an original event")
		}
	default:
		return errors.New("invalid ledger action")
	}
	switch e.Action {
	case "decision":
		if e.OutputOutcome != "output" && e.OutputOutcome != "empty" && e.OutputOutcome != "error" && e.OutputOutcome != "unknown" {
			return errors.New("invalid decision outcome")
		}
	case "recall":
		if e.OutputOutcome != "hits" && e.OutputOutcome != "empty" && e.OutputOutcome != "error" && e.OutputOutcome != "unknown" {
			return errors.New("invalid recall outcome")
		}
	case "output_attempt":
		if e.OutputOutcome != "success" && e.OutputOutcome != "failure" && e.OutputOutcome != "short_write" && e.OutputOutcome != "unknown" {
			return errors.New("invalid output attempt outcome")
		}
	case "prepared", "retraction":
		if e.OutputOutcome != "not_applicable" {
			return errors.New("output outcome does not match action")
		}
	case "correction":
		switch e.OutputOutcome {
		case "not_applicable", "output", "hits", "empty", "error", "success", "failure", "short_write", "unknown":
		default:
			return errors.New("invalid correction outcome")
		}
	}
	if e.Observation != "observed" && e.Observation != "unknown" {
		return errors.New("invalid observation state")
	}
	if e.Observation == "unknown" || e.Action == "retraction" {
		if e.TextSHA256 != "" || e.TextBytes != nil {
			return errors.New("unknown or retracted event cannot contain text measurements")
		}
	} else {
		decoded, err := hex.DecodeString(e.TextSHA256)
		if err != nil || len(decoded) != 32 || e.TextSHA256 != strings.ToLower(e.TextSHA256) || e.TextBytes == nil || *e.TextBytes < 0 {
			return errors.New("observed event requires a SHA-256 hash and byte count")
		}
	}
	return nil
}

// LedgerEntry adds a persistent monotonic cursor to an event.
type LedgerEntry struct {
	Sequence int64 `json:"sequence"`
	LedgerEvent
}

// Events returns the next page in commit order, including corrections and
// retractions. The caller persists the returned sequence as its cursor.
func (l *Ledger) Events(after int64, limit int) ([]LedgerEntry, error) {
	if after < 0 || limit <= 0 || limit > 10000 {
		return nil, errors.New("invalid ledger cursor or limit")
	}
	rows, err := l.db.Query(`SELECT seq, event_id, execution_id, session, scope, harness, hook, output_outcome, action, target_event_id, observed_at, observation, text_sha256, text_bytes FROM efficacy_event WHERE seq > ? ORDER BY seq LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []LedgerEntry{}
	for rows.Next() {
		var entry LedgerEntry
		var textBytes sql.NullInt64
		if err := rows.Scan(&entry.Sequence, &entry.EventID, &entry.ExecutionID, &entry.Session, &entry.Scope, &entry.Harness, &entry.Hook, &entry.OutputOutcome, &entry.Action, &entry.TargetEventID, &entry.ObservedAt, &entry.Observation, &entry.TextSHA256, &textBytes); err != nil {
			return nil, err
		}
		if textBytes.Valid {
			entry.TextBytes = &textBytes.Int64
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}
