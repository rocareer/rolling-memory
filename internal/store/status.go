// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/rocareer/rolling-memory/internal/db"
)

// Warning returns the in-memory embedding warning (if any) with 0 SQL queries.
func (s *Store) Warning() any {
	if s == nil || s.warning == nil {
		return nil
	}
	return *s.warning
}

// Status returns the scope counters and runtime handles.
func (s *Store) Status(scope string) (map[string]any, error) {
	if scope == "" {
		scope = s.Config.Scope
	}
	counts, err := db.TableCounts(s.conn, scope)
	if err != nil {
		return nil, err
	}
	var hasLegacy int
	_ = s.conn.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'assertion'`).Scan(&hasLegacy)
	if hasLegacy == 0 {
		active := counts["assertion"]
		schemaRaw, _ := db.GetMeta(s.conn, "schema_version", "1")
		schemaVersion, _ := strconv.ParseInt(schemaRaw, 10, 64)
		egress := "remote-allowed"
		if s.Config.Mode == "local" {
			egress = "loopback-only"
		}
		return map[string]any{
			"scope":                 scope,
			"db":                    s.Config.DB,
			"db_size_bytes":         db.DBSize(s.Config.DB),
			"mode":                  s.Config.Mode,
			"egress":                egress,
			"counts":                counts,
			"active_assertions":     active,
			"private_assertions":    0,
			"retracted_assertions":  0,
			"superseded_assertions": 0,
			"pending_traces":        0,
			"sessions":              1,
			"conflicts_proposed":    0,
			"last_replay":           nil,
			"schema_version":        schemaVersion,
		}, nil
	}
	active, err := queryCount(s.conn,
		"SELECT count(*) FROM assertion WHERE scope = ? AND status = 'active' AND bare = 0", scope)
	if err != nil {
		return nil, err
	}
	private, err := queryCount(s.conn,
		"SELECT count(*) FROM assertion WHERE scope = ? AND audience = 'private'", scope)
	if err != nil {
		return nil, err
	}
	retracted, err := queryCount(s.conn,
		"SELECT count(*) FROM assertion WHERE scope = ? AND status = 'retracted'", scope)
	if err != nil {
		return nil, err
	}
	superseded, err := queryCount(s.conn,
		"SELECT count(*) FROM assertion WHERE scope = ? AND status = 'superseded'", scope)
	if err != nil {
		return nil, err
	}
	pending, err := queryCount(s.conn,
		"SELECT count(*) FROM trace WHERE scope = ? AND processed = 0", scope)
	if err != nil {
		return nil, err
	}
	sessions, err := queryCount(s.conn,
		"SELECT count(DISTINCT session) FROM trace WHERE scope = ?", scope)
	if err != nil {
		return nil, err
	}
	conflicts, err := queryCount(s.conn,
		"SELECT count(*) FROM meta_edge WHERE scope = ? AND meta_kind = 'conflicts-with' AND status = 'proposed'", scope)
	if err != nil {
		return nil, err
	}
	replay, err := queryOneMap(s.conn, "SELECT * FROM replay_state WHERE scope = ?", scope)
	if err != nil {
		return nil, err
	}
	schemaRaw, err := db.GetMeta(s.conn, "schema_version", "0")
	if err != nil {
		return nil, err
	}
	schemaVersion, _ := strconv.ParseInt(schemaRaw, 10, 64)
	egress := "remote-allowed"
	if s.Config.Mode == "local" {
		egress = "loopback-only"
	}
	var lastReplay any
	if replay != nil {
		lastReplay = replay
	}
	return map[string]any{
		"scope":                 scope,
		"db":                    s.Config.DB,
		"db_size_bytes":         db.DBSize(s.Config.DB),
		"mode":                  s.Config.Mode,
		"egress":                egress,
		"counts":                counts,
		"active_assertions":     active,
		"private_assertions":    private,
		"retracted_assertions":  retracted,
		"superseded_assertions": superseded,
		"pending_traces":        pending,
		"sessions":              sessions,
		"conflicts_proposed":    conflicts,
		"last_replay":           lastReplay,
		"schema_version":        schemaVersion,
	}, nil
}

// ExportScope dumps the scope's graph tables as raw row objects.
func (s *Store) ExportScope(scope string) (map[string]any, error) {
	tables := []struct{ name, query string }{
		{"traces", "SELECT * FROM trace WHERE scope = ? ORDER BY recorded_at"},
		{"nodes", "SELECT * FROM node WHERE scope = ? ORDER BY created_at"},
		{"aliases", `SELECT a.* FROM node_alias a JOIN node n ON n.id = a.node_id
		              WHERE n.scope = ? ORDER BY a.created_at`},
		{"assertions", "SELECT * FROM assertion WHERE scope = ? ORDER BY created_at"},
		{"mentions", "SELECT * FROM mention WHERE scope = ? ORDER BY id"},
		{"meta_edges", "SELECT * FROM meta_edge WHERE scope = ? ORDER BY id"},
		{"events", "SELECT * FROM event WHERE scope = ? ORDER BY id"},
	}
	data := map[string]any{}
	for _, table := range tables {
		rows, err := queryMaps(s.conn, table.query, scope)
		if err != nil {
			return nil, err
		}
		data[table.name] = rows
	}
	return data, nil
}

// ImportScope inserts exported rows in FK order and rebuilds FTS.
func (s *Store) ImportScope(scope string, data map[string]any) (map[string]int, error) {
	order := []struct{ key, table string }{
		{"traces", "trace"},
		{"nodes", "node"},
		{"aliases", "node_alias"},
		{"assertions", "assertion"},
		{"mentions", "mention"},
		{"meta_edges", "meta_edge"},
		{"events", "event"},
	}
	inserted := map[string]int{}
	err := db.WithImmediateTx(s.conn, func(tx *sql.Tx) error {
		for _, item := range order {
			rows, ok := importRows(data[item.key])
			if !ok {
				continue
			}
			count := 0
			for _, row := range rows {
				copied := cloneMap(row)
				if item.table != "node_alias" {
					if _, ok := copied["scope"]; ok {
						copied["scope"] = scope
					}
				}
				columns := make([]string, 0, len(copied))
				for name := range copied {
					if isSafeColumn(name) {
						columns = append(columns, name)
					}
				}
				if len(columns) == 0 {
					continue
				}
				if item.table == "trace" {
					hash, ok := copied["hash"].(string)
					if !ok || hash == "" {
						return fmt.Errorf("imported trace requires a hash")
					}
					var existingAudience, existingScope string
					err := tx.QueryRow(`SELECT audience, scope FROM trace WHERE hash = ?`, hash).Scan(&existingAudience, &existingScope)
					if err != nil && !errors.Is(err, sql.ErrNoRows) {
						return err
					}
					if err == nil {
						audience, ok := copied["audience"].(string)
						if !ok || audience != existingAudience || existingScope != scope {
							return fmt.Errorf("duplicate trace audience or scope conflict: existing vault copies may require review")
						}
					}
				}
				sort.Strings(columns)
				placeholders := ""
				args := make([]any, 0, len(columns))
				for index, name := range columns {
					if index > 0 {
						placeholders += ","
					}
					placeholders += "?"
					args = append(args, normalizeSQLValue(copied[name]))
				}
				statement := "INSERT OR IGNORE INTO " + item.table +
					"(" + strings.Join(columns, ",") + ") VALUES (" + placeholders + ")"
				if item.table == "meta_edge" {
					statement = "INSERT INTO " + item.table +
						"(" + strings.Join(columns, ",") + ") VALUES (" + placeholders + ") ON CONFLICT DO NOTHING"
				}
				result, err := tx.Exec(statement, args...)
				if err != nil {
					if item.table == "meta_edge" {
						return err
					}
					continue
				}
				affected, err := result.RowsAffected()
				if err != nil {
					if item.table == "meta_edge" {
						return err
					}
					continue
				}
				if affected > 0 {
					count++
				}
			}
			inserted[item.key] = count
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if _, err := db.RebuildFTS(s.conn); err != nil {
		return nil, err
	}
	return inserted, nil
}

func importRows(value any) ([]map[string]any, bool) {
	switch typed := value.(type) {
	case []map[string]any:
		return typed, true
	case []any:
		rows := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if row, ok := item.(map[string]any); ok {
				rows = append(rows, row)
			}
		}
		return rows, true
	default:
		return nil, false
	}
}

func normalizeSQLValue(value any) any {
	switch typed := value.(type) {
	case json.Number:
		if number, err := typed.Int64(); err == nil {
			return number
		}
		if number, err := typed.Float64(); err == nil {
			return number
		}
		return typed.String()
	case float64:
		if typed == float64(int64(typed)) {
			return int64(typed)
		}
		return typed
	case bool:
		if typed {
			return int64(1)
		}
		return int64(0)
	default:
		return value
	}
}
