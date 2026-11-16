// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package store

import (
	"fmt"
	"strings"

	"github.com/rocareer/rolling-memory/internal/db"
	"github.com/rocareer/rolling-memory/internal/ids"
)

// HasLegacyGraph reports whether the retired assertion graph tables still exist.
// A RAME database keeps only the `thin_vault_*` schema (PurgeLegacyTables drops the
// rest), so every legacy read path must ask this before touching `assertion`/`trace`.
func (s *Store) HasLegacyGraph() bool {
	if s == nil || s.conn == nil {
		return false
	}
	var present int
	if err := s.conn.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'assertion'`,
	).Scan(&present); err != nil {
		return false
	}
	return present > 0
}

// Check runs integrity, anchor, audience, node, vector and cycle checks.
func (s *Store) Check() (map[string]any, error) {
	issues := []map[string]any{}
	integrity, err := db.IntegrityCheck(s.conn)
	if err != nil {
		return nil, err
	}
	for _, message := range integrity {
		issues = append(issues, map[string]any{
			"severity": "error",
			"code":     "integrity",
			"detail":   message,
		})
	}

	var hasLegacy int
	_ = s.conn.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'assertion'`).Scan(&hasLegacy)
	var bare, conflicts int64
	if hasLegacy > 0 {
		spanRows, err := queryMaps(s.conn,
			`SELECT a.node_id, a.anchor_start, a.anchor_end, length(t.text) AS text_len
			 FROM assertion a JOIN trace t ON t.hash = a.anchor_trace_hash
			 WHERE a.anchor_start < 0 OR a.anchor_end <= a.anchor_start OR a.anchor_end > length(t.text)
			 LIMIT 20`)
		if err != nil {
			return nil, err
		}
		for _, row := range spanRows {
			issues = append(issues, map[string]any{
				"severity": "error",
				"code":     "anchor_span",
				"detail": fmt.Sprintf("%s span %d-%d / %d",
					mapString(row, "node_id"),
					mapInt64(row, "anchor_start"),
					mapInt64(row, "anchor_end"),
					mapInt64(row, "text_len")),
			})
		}

		audienceRows, err := queryMaps(s.conn,
			`SELECT a.node_id, a.audience AS assertion_audience, t.audience AS trace_audience
			 FROM assertion a JOIN trace t ON t.hash = a.source_trace_hash
			 LIMIT 20000`)
		if err != nil {
			return nil, err
		}
		for _, row := range audienceRows {
			assertionAudience := mapString(row, "assertion_audience")
			traceAudience := mapString(row, "trace_audience")
			if ids.AudienceRank[assertionAudience] > ids.AudienceRank[traceAudience] {
				issues = append(issues, map[string]any{
					"severity": "error",
					"code":     "audience_monotonic",
					"detail": fmt.Sprintf("%s %s > %s",
						mapString(row, "node_id"), assertionAudience, traceAudience),
				})
			}
		}

		missingNodes, err := queryMaps(s.conn,
			`SELECT a.node_id FROM assertion a LEFT JOIN node n ON n.id = a.node_id
			 WHERE n.id IS NULL LIMIT 20`)
		if err != nil {
			return nil, err
		}
		for _, row := range missingNodes {
			issues = append(issues, map[string]any{
				"severity": "error",
				"code":     "assertion_node",
				"detail":   fmt.Sprintf("missing node row for %s", mapString(row, "node_id")),
			})
		}

		orphanVectors, err := queryMaps(s.conn,
			`SELECT v.owner_hash FROM vector v LEFT JOIN node n ON n.id = v.owner_hash
			 WHERE v.owner_kind = 'node' AND n.id IS NULL LIMIT 20`)
		if err != nil {
			return nil, err
		}
		for _, row := range orphanVectors {
			issues = append(issues, map[string]any{
				"severity": "warning",
				"code":     "orphan_vector",
				"detail":   fmt.Sprintf("vector without node: %s", mapString(row, "owner_hash")),
			})
		}

		cycles := s.sameAsCycles()
		if len(cycles) > 10 {
			cycles = cycles[:10]
		}
		for _, cycle := range cycles {
			issues = append(issues, map[string]any{
				"severity": "error",
				"code":     "same_as_cycle",
				"detail":   strings.Join(cycle, " -> "),
			})
		}

		bare, err = queryCount(s.conn, "SELECT count(*) FROM assertion WHERE bare = 1")
		if err != nil {
			return nil, err
		}
		conflicts, err = queryCount(s.conn,
			"SELECT count(*) FROM meta_edge WHERE meta_kind = 'conflicts-with' AND status = 'proposed'")
		if err != nil {
			return nil, err
		}
	}
	counts, err := db.TableCounts(s.conn, "")
	if err != nil {
		return nil, err
	}
	ok := true
	for _, issue := range issues {
		if mapString(issue, "severity") == "error" {
			ok = false
			break
		}
	}
	return map[string]any{
		"ok":                 ok,
		"issues":             issues,
		"counts":             counts,
		"bare_assertions":    bare,
		"conflicts_proposed": conflicts,
	}, nil
}

// sameAsCycles 检测 same-as 自环与成环（并查集，与引擎 check ⑦ 同判据）：
// 单条 same-as 边 = 等价合并（合法，不判环）；仅当边的两端已在同一等价类（真环）
// 或出现自环时才判失败。无向 DFS 回边判定会把「任意单边」误报为环，故不采用。
func (s *Store) sameAsCycles() [][]string {
	edges, err := queryMaps(s.conn,
		"SELECT lhs_id, rhs_id FROM meta_edge WHERE meta_kind = 'same-as' AND status = 'active'")
	if err != nil {
		return [][]string{}
	}
	parent := map[string]string{}
	var find func(string) string
	find = func(x string) string {
		if _, ok := parent[x]; !ok {
			parent[x] = x
		}
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	cycles := [][]string{}
	for _, row := range edges {
		left := mapString(row, "lhs_id")
		right := mapString(row, "rhs_id")
		if left == "" || right == "" {
			continue
		}
		if left == right {
			cycles = append(cycles, []string{left, left})
			continue
		}
		leftRoot, rightRoot := find(left), find(right)
		if leftRoot == rightRoot {
			cycles = append(cycles, []string{left, right, left})
			continue
		}
		parent[leftRoot] = rightRoot
	}
	if len(cycles) > 10 {
		cycles = cycles[:10]
	}
	return cycles
}
