// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package store

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/rocareer/rolling-memory/internal/clock"
	"github.com/rocareer/rolling-memory/internal/db"
	"github.com/rocareer/rolling-memory/internal/ids"
)

// Node returns one node row.
func (s *Store) Node(id string) (Node, bool, error) {
	row, err := queryOneMap(s.conn, "SELECT * FROM node WHERE id = ?", id)
	if err != nil {
		return Node{}, false, err
	}
	if row == nil {
		return Node{}, false, nil
	}
	return nodeFromMap(row), true, nil
}

// NodeByLabel resolves a live node by normalized label or alias.
func (s *Store) NodeByLabel(scope, label, kind string) (Node, bool, error) {
	norm := ids.NormalizeLabel(label)
	var row map[string]any
	var err error
	if kind != "" {
		if kind != "assertion" {
			row, err = queryOneMap(s.conn,
				"SELECT * FROM node WHERE scope = ? AND kind = ? AND kind != 'assertion' AND norm_label = ? AND status IN ('provisional','active')",
				scope, kind, norm)
		} else {
			row, err = queryOneMap(s.conn,
				"SELECT * FROM node WHERE scope = ? AND kind = ? AND norm_label = ? AND status IN ('provisional','active')",
				scope, kind, norm)
		}
	} else {
		row, err = queryOneMap(s.conn,
			"SELECT * FROM node WHERE scope = ? AND kind != 'assertion' AND norm_label = ? AND status IN ('provisional','active') ORDER BY kind LIMIT 1",
			scope, norm)
		if err == nil && row == nil {
			row, err = queryOneMap(s.conn,
				"SELECT * FROM node WHERE scope = ? AND kind = 'assertion' AND norm_label = ? AND status IN ('provisional','active') LIMIT 1",
				scope, norm)
		}
	}
	if err != nil {
		return Node{}, false, err
	}
	if row != nil {
		return nodeFromMap(row), true, nil
	}
	var kindFilter any
	if kind != "" {
		kindFilter = kind
	}
	row, err = queryOneMap(s.conn,
		`SELECT n.* FROM node_alias a CROSS JOIN node n ON n.id = a.node_id
		 WHERE a.norm_alias = ? AND n.scope = ? AND n.status IN ('provisional','active')
		   AND (? IS NULL OR n.kind = ?)
		 ORDER BY n.kind LIMIT 1`,
		norm, scope, kindFilter, kindFilter)
	if err != nil {
		return Node{}, false, err
	}
	if row == nil {
		return Node{}, false, nil
	}
	return nodeFromMap(row), true, nil
}

// EnsureNode returns the id of a live node, creating or reviving it.
func (s *Store) EnsureNode(scope, kind, label string, o NodeOptions) (string, error) {
	if !containsValue(ids.NodeKinds, kind) {
		return "", fmt.Errorf("invalid node kind %s", pyRepr(kind))
	}
	clean := pyStrip(label)
	if clean == "" {
		return "", fmt.Errorf("empty node label")
	}
	audience := defaultIfEmpty(o.Audience, "shared")
	status := defaultIfEmpty(o.Status, "provisional")
	norm := ids.NormalizeLabel(clean)
	existing, found, err := s.NodeByLabel(scope, clean, kind)
	if err != nil {
		return "", err
	}
	if found && o.Qualifier == "" {
		// Node audience is provenance from creation; a narrower assertion must
		// not retroactively hide an already-shared node.
		for _, item := range []struct {
			column  string
			value   *string
			current *string
		}{
			{"world", o.World, existing.World},
			{"carrier", o.Carrier, existing.Carrier},
			{"locus", o.Locus, existing.Locus},
		} {
			if item.value == nil || *item.value == "" {
				continue
			}
			if item.current == nil || *item.current == "" {
				if _, err := s.conn.Exec(
					"UPDATE node SET "+item.column+" = ? WHERE id = ?", *item.value, existing.ID,
				); err != nil {
					return "", err
				}
			}
		}
		return existing.ID, nil
	}
	nid := ids.NodeID(scope, kind, norm, o.Qualifier)
	now := clock.Unix()
	revived, err := queryOneMap(s.conn, "SELECT * FROM node WHERE id = ?", nid)
	if err != nil {
		return "", err
	}
	if revived != nil {
		if _, err := s.conn.Exec(
			"UPDATE node SET status = ?, retired_at = NULL WHERE id = ?",
			status, nid,
		); err != nil {
			return "", err
		}
		if err := s.refreshNodeFTS(nid); err != nil {
			return "", err
		}
		return nid, nil
	}
	err = db.WithImmediateTx(s.conn, func(tx *sql.Tx) error {
		if _, err := tx.Exec(
			`INSERT INTO node(id, scope, kind, label, norm_label, status, audience,
			                  world, carrier, locus, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			nid, scope, kind, clean, norm, status, audience,
			o.World, o.Carrier, o.Locus, now,
		); err != nil {
			return err
		}
		if s.fts && kind != "assertion" {
			if _, err := tx.Exec(
				"INSERT INTO node_fts(label, aliases, node_id) VALUES (?, '', ?)",
				clean, nid,
			); err != nil {
				return err
			}
		}
		_, err := insertEvent(tx, scope, "node.add", map[string]any{
			"node":  nid,
			"kind":  kind,
			"label": clean,
		}, "", "system", false)
		return err
	})
	if err != nil {
		existing, found, lookupErr := s.NodeByLabel(scope, clean, kind)
		if lookupErr != nil {
			return "", lookupErr
		}
		if found {
			return existing.ID, nil
		}
		return "", err
	}
	return nid, nil
}

// AddAlias inserts one alias and refreshes the node's FTS row.
func (s *Store) AddAlias(nodeID, alias, source string) (bool, error) {
	clean := pyStrip(alias)
	if clean == "" {
		return false, nil
	}
	source = defaultIfEmpty(source, "manual")
	norm := ids.NormalizeLabel(clean)
	affected, err := execCount(s.conn,
		"INSERT OR IGNORE INTO node_alias(node_id, alias, norm_alias, source, created_at) VALUES (?, ?, ?, ?, ?)",
		nodeID, clean, norm, source, clock.Unix())
	if err != nil {
		return false, err
	}
	inserted := affected > 0
	if inserted {
		if err := s.refreshNodeFTS(nodeID); err != nil {
			return false, err
		}
	}
	return inserted, nil
}

// ReconcileAliasDuplicateNodes automatically folds any pre-existing live non-assertion
// island node whose normalized label equals the newly registered alias into keepNodeID
// within a single immediate transaction (pure graph-theoretic deduplication, 0 wordlists):
//   - re-points assertion subject_id / object_id / holder_id endpoints to keepNodeID (with self-loop guard);
//   - migrates remaining aliases of the duplicate node onto keepNodeID;
//   - records an active same-as meta_edge and marks the duplicate node status='merged'.
func (s *Store) ReconcileAliasDuplicateNodes(scope, keepNodeID, alias, source string) (int, error) {
	clean := pyStrip(alias)
	if strings.TrimSpace(scope) == "" || strings.TrimSpace(keepNodeID) == "" || clean == "" {
		return 0, nil
	}
	source = defaultIfEmpty(source, "remember")
	norm := ids.NormalizeLabel(clean)
	if norm == "" {
		return 0, nil
	}
	// Materialize duplicate node IDs before opening the write transaction (modernc.org/sqlite cursor rule).
	rows, err := queryMaps(s.conn,
		`SELECT id FROM node
		 WHERE scope = ? AND id <> ? AND kind <> 'assertion'
		   AND status IN ('provisional', 'active') AND norm_label = ?`,
		scope, keepNodeID, norm)
	if err != nil || len(rows) == 0 {
		return 0, err
	}
	dupIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		if id := mapString(row, "id"); id != "" && id != keepNodeID {
			dupIDs = append(dupIDs, id)
		}
	}
	if len(dupIDs) == 0 {
		return 0, nil
	}
	now := clock.Unix()
	err = db.WithImmediateTx(s.conn, func(tx *sql.Tx) error {
		for _, dupID := range dupIDs {
			aliasRows, err := queryMaps(tx,
				"SELECT alias, norm_alias, source FROM node_alias WHERE node_id = ? ORDER BY created_at",
				dupID)
			if err != nil {
				return err
			}
			for _, ar := range aliasRows {
				al := pyStrip(mapString(ar, "alias"))
				na := mapString(ar, "norm_alias")
				src := defaultIfEmpty(mapString(ar, "source"), source)
				if al == "" || na == "" {
					continue
				}
				if _, err := tx.Exec(
					`INSERT OR IGNORE INTO node_alias(node_id, alias, norm_alias, source, created_at)
					 SELECT ?, ?, ?, ?, ?
					 WHERE NOT EXISTS (
					   SELECT 1 FROM node tn WHERE tn.id = ? AND tn.norm_label = ?
					 ) AND NOT EXISTS (
					   SELECT 1 FROM node_alias ox JOIN node onode ON onode.id = ox.node_id
					   WHERE onode.scope = ? AND ox.node_id NOT IN (?, ?)
					     AND onode.status IN ('provisional', 'active') AND onode.kind != 'assertion'
					     AND ox.norm_alias = ?
					 )`,
					keepNodeID, al, na, src, now, keepNodeID, na, scope, keepNodeID, dupID, na,
				); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(
				`INSERT OR IGNORE INTO meta_edge
				   (scope, meta_kind, lhs_id, rhs_id, status, evidence_anchor, created_at, created_by)
				 VALUES (?, 'same-as', ?, ?, 'active', ?, ?, ?)`,
				scope, keepNodeID, dupID, "alias-reconcile:"+firstChars(dupID, 8), now, source,
			); err != nil {
				return err
			}
			// Re-point assertion endpoints onto the canonical node (self-loop guard: a row whose
			// other endpoints already reference keepNodeID would collapse into a degenerate
			// subject_id = object_id self-loop, so that endpoint keeps its original target and is
			// left for governed meta reconciliation instead). Nullable columns must use explicit
			// IS NULL checks because NULL <> ? evaluates to UNKNOWN in three-valued SQL logic.
			for _, col := range [...]struct{ name, guard string }{
				{name: "subject_id", guard: "(object_id IS NULL OR object_id <> ?) AND (holder_id IS NULL OR holder_id <> ?)"},
				{name: "object_id", guard: "(subject_id IS NULL OR subject_id <> ?) AND (holder_id IS NULL OR holder_id <> ?)"},
				{name: "holder_id", guard: "(subject_id IS NULL OR subject_id <> ?) AND (object_id IS NULL OR object_id <> ?)"},
			} {
				if _, err := tx.Exec(
					`UPDATE assertion SET `+col.name+` = ?
					 WHERE scope = ? AND `+col.name+` = ?
					   AND (`+col.guard+`)`,
					keepNodeID, scope, dupID, keepNodeID, keepNodeID,
				); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(
				`UPDATE node SET status = 'merged', retired_at = ?
				 WHERE scope = ? AND id = ? AND status IN ('provisional', 'active')`,
				now, scope, dupID,
			); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	for _, dupID := range dupIDs {
		_ = s.refreshNodeFTS(dupID)
	}
	_ = s.refreshNodeFTS(keepNodeID)
	return len(dupIDs), nil
}

// Aliases lists a node's aliases in creation order.
func (s *Store) Aliases(nodeID string) ([]string, error) {
	rows, err := queryMaps(s.conn,
		"SELECT alias FROM node_alias WHERE node_id = ? ORDER BY created_at", nodeID)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(rows))
	for _, row := range rows {
		result = append(result, mapString(row, "alias"))
	}
	return result, nil
}

func (s *Store) refreshNodeFTS(nodeID string) error {
	if !s.fts {
		return nil
	}
	row, err := queryOneMap(s.conn, "SELECT label, status, kind FROM node WHERE id = ?", nodeID)
	if err != nil {
		return err
	}
	if row == nil {
		return nil
	}
	if _, err := s.conn.Exec("DELETE FROM node_fts WHERE node_id = ?", nodeID); err != nil {
		return err
	}
	status := mapString(row, "status")
	kind := mapString(row, "kind")
	if (status == "provisional" || status == "active") && kind != "assertion" {
		aliases, err := s.Aliases(nodeID)
		if err != nil {
			return err
		}
		if _, err := s.conn.Exec(
			"INSERT INTO node_fts(label, aliases, node_id) VALUES (?, ?, ?)",
			mapString(row, "label"), strings.Join(aliases, " "), nodeID,
		); err != nil {
			return err
		}
	}
	return nil
}

// Closure walks same-as / subtype-of / inverse-of nomination edges.
//
// same-as folds identity; subtype-of joins a type with its neighbours;
// inverse-of joins a relation with its inverse reading. Rendering keeps the
// stored wording — the closure only widens nomination.
//
// Closure is undirected, bounded at ≤2 hops and fanout ≤32; canonical/seam
// chains still go through CanonicalMap (the same-as union-find, outside this cap).
func (s *Store) Closure(scope string, nodeIDs []string, kinds []string, limit int) ([]string, error) {
	return s.closureCapped(scope, nodeIDs, kinds, limit, closureMaxHops, closureFanout)
}

const (
	// closureMaxHops caps the walk depth; closureFanout caps the neighbours
	// expanded per node.
	closureMaxHops = 2
	closureFanout  = 32
)

// closureCapped is the parameterised walk behind Closure.
func (s *Store) closureCapped(
	scope string,
	nodeIDs []string,
	kinds []string,
	limit, maxHops, fanout int,
) ([]string, error) {
	if limit == 0 {
		limit = 128
	}
	seeds := make([]string, 0, len(nodeIDs))
	for _, value := range ids.UniqueOrdered(nodeIDs) {
		if value != "" {
			seeds = append(seeds, value)
		}
	}
	if len(seeds) == 0 {
		return []string{}, nil
	}
	activeKinds := make([]string, 0, len(kinds))
	for _, kind := range ids.UniqueOrdered(kinds) {
		if kind != "" {
			activeKinds = append(activeKinds, kind)
		}
	}
	if len(activeKinds) == 0 {
		if limit <= 0 {
			return []string{}, nil
		}
		if limit > len(seeds) {
			limit = len(seeds)
		}
		return seeds[:limit], nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(activeKinds)), ",")
	args := make([]any, 0, len(activeKinds)+1)
	args = append(args, scope)
	for _, kind := range activeKinds {
		args = append(args, kind)
	}
	rows, err := queryMaps(s.conn,
		`SELECT lhs_id, rhs_id FROM meta_edge
		 WHERE scope = ? AND meta_kind IN (`+placeholders+`) AND status = 'active'`,
		args...)
	if err != nil {
		return nil, err
	}
	adjacency := map[string][]string{}
	for _, row := range rows {
		left := mapString(row, "lhs_id")
		right := mapString(row, "rhs_id")
		adjacency[left] = append(adjacency[left], right)
		adjacency[right] = append(adjacency[right], left)
	}
	seen := map[string]struct{}{}
	queue := make([]closureStep, 0, len(seeds))
	for _, seed := range seeds {
		queue = append(queue, closureStep{node: seed})
	}
	result := []string{}
	for len(queue) > 0 && len(result) < limit {
		current := queue[0]
		queue = queue[1:]
		if _, ok := seen[current.node]; ok {
			continue
		}
		seen[current.node] = struct{}{}
		result = append(result, current.node)
		if current.depth >= maxInt(0, maxHops) {
			continue
		}
		neighbours := sortedUnique(adjacency[current.node])
		if len(neighbours) > maxInt(0, fanout) {
			neighbours = neighbours[:maxInt(0, fanout)]
		}
		for _, neighbour := range neighbours {
			if _, ok := seen[neighbour]; ok {
				continue
			}
			queue = append(queue, closureStep{node: neighbour, depth: current.depth + 1})
		}
	}
	return result, nil
}

// closureStep is one BFS entry: the node and its hop distance from a seed.
type closureStep struct {
	node  string
	depth int
}

func sortedUnique(values []string) []string {
	result := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

// SameAsClosure is closure limited to same-as identity edges.
func (s *Store) SameAsClosure(scope string, nodeIDs []string, limit int) ([]string, error) {
	if limit == 0 {
		limit = 128
	}
	return s.Closure(scope, nodeIDs, []string{"same-as"}, limit)
}
