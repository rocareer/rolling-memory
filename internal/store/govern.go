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

// NodeLabels resolves node ids to labels.
func (s *Store) NodeLabels(nodeIDs []string) (map[string]string, error) {
	values := []string{}
	for _, value := range ids.UniqueOrdered(nodeIDs) {
		if value != "" {
			values = append(values, value)
		}
	}
	result := map[string]string{}
	if len(values) == 0 {
		return result, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(values)), ",")
	args := make([]any, 0, len(values))
	for _, value := range values {
		args = append(args, value)
	}
	rows, err := queryMaps(s.conn,
		"SELECT id, label FROM node WHERE id IN ("+placeholders+")", args...)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[mapString(row, "id")] = mapString(row, "label")
	}
	return result, nil
}

// CanonicalMap maps same-as components to a stable canonical node id.
//
// The representative prefers a live (active/provisional) node over a merged
// one, then the smallest id, so read-side grouping is deterministic.
func (s *Store) CanonicalMap(scope string, nodeIDs []string) (map[string]string, error) {
	values := []string{}
	for _, value := range ids.UniqueOrdered(nodeIDs) {
		if value != "" {
			values = append(values, value)
		}
	}
	if len(values) == 0 {
		return map[string]string{}, nil
	}
	edges, err := queryMaps(s.conn,
		`SELECT lhs_id, rhs_id FROM meta_edge
		 WHERE scope = ? AND meta_kind = 'same-as' AND status = 'active'
		 ORDER BY id`, scope)
	if err != nil {
		return nil, err
	}
	adjacency := map[string][]string{}
	for _, row := range edges {
		left := mapString(row, "lhs_id")
		right := mapString(row, "rhs_id")
		adjacency[left] = append(adjacency[left], right)
		adjacency[right] = append(adjacency[right], left)
	}
	mapping := map[string]string{}
	for _, start := range values {
		if _, ok := mapping[start]; ok {
			continue
		}
		component := []string{}
		queue := []string{start}
		seen := map[string]struct{}{}
		for len(queue) > 0 {
			current := queue[0]
			queue = queue[1:]
			if _, ok := seen[current]; ok {
				continue
			}
			seen[current] = struct{}{}
			component = append(component, current)
			queue = append(queue, adjacency[current]...)
		}
		if len(component) == 1 {
			mapping[component[0]] = component[0]
			continue
		}
		placeholders := strings.TrimRight(strings.Repeat("?,", len(component)), ",")
		args := make([]any, 0, len(component))
		for _, value := range component {
			args = append(args, value)
		}
		rows, err := queryMaps(s.conn,
			"SELECT id, status FROM node WHERE id IN ("+placeholders+")", args...)
		if err != nil {
			return nil, err
		}
		statuses := map[string]string{}
		for _, row := range rows {
			statuses[mapString(row, "id")] = mapString(row, "status")
		}
		live := []string{}
		for _, value := range component {
			status := statuses[value]
			if status == "active" || status == "provisional" {
				live = append(live, value)
			}
		}
		candidates := live
		if len(candidates) == 0 {
			candidates = component
		}
		canonical := candidates[0]
		for _, value := range candidates[1:] {
			if canonicalRank(statuses[canonical], canonical) > canonicalRank(statuses[value], value) {
				canonical = value
			}
		}
		for _, value := range component {
			mapping[value] = canonical
		}
	}
	return mapping, nil
}

// canonicalRank implements min(key=(status != "active", id)).
func canonicalRank(status, id string) string {
	prefix := "0"
	if status != "active" {
		prefix = "1"
	}
	return prefix + "\x00" + id
}

// AssertionSummary is the compact read-out row used by recent/conflicts/why.
func (s *Store) AssertionSummary(nodeID string) (map[string]any, bool, error) {
	row, err := queryOneMap(s.conn,
		`SELECT a.*, coalesce(ns.label, a.subject_literal) AS s_label,
		        nx.label AS x_label, coalesce(no.label, a.object_literal) AS o_label
		 FROM assertion a
		 LEFT JOIN node ns ON ns.id = a.subject_id
		 JOIN node nx ON nx.id = a.rel_id
		 LEFT JOIN node no ON no.id = a.object_id
		 WHERE a.node_id = ?`, nodeID)
	if err != nil {
		return nil, false, err
	}
	if row == nil {
		return nil, false, nil
	}
	data := assertionFromMap(row)
	timeValue := data.TValidFrom
	if timeValue == 0 {
		timeValue = data.TSource
	}
	return map[string]any{
		"id":           data.NodeID,
		"kind":         "memory_unit",
		"content":      ids.RenderAssertion(mapString(row, "s_label"), mapString(row, "x_label"), mapString(row, "o_label")),
		"anchor":       anchorRef(data.AnchorTraceHash, data.AnchorStart, data.AnchorEnd),
		"status":       data.Status,
		"audience":     data.Audience,
		"confidence":   data.Confidence,
		"time":         ids.FormatTime(timeValue, "unknown"),
		"t_valid_from": data.TValidFrom,
		"t_valid_to":   data.TValidTo,
		"t_source":     data.TSource,
	}, true, nil
}

// Recent lists the newest deliverable-visible assertions.
func (s *Store) Recent(scope, reader string, limit int) ([]map[string]any, error) {
	if reader == "" {
		reader = "deliverable"
	}
	if limit == 0 {
		limit = 10
	}
	scanLimit := limit
	if scanLimit < 1 {
		scanLimit = 1
	}
	rows, err := queryMaps(s.conn,
		`SELECT a.node_id, a.audience, a.status
		 FROM assertion a
		 WHERE a.scope = ? AND a.status IN ('active', 'superseded')
		 ORDER BY a.t_source DESC, a.created_at DESC, a.node_id
		 LIMIT ?`, scope, scanLimit*6)
	if err != nil {
		return nil, err
	}
	result := []map[string]any{}
	for _, row := range rows {
		if !ids.AudienceVisible(reader, mapString(row, "audience")) {
			continue
		}
		summary, ok, err := s.AssertionSummary(mapString(row, "node_id"))
		if err != nil {
			return nil, err
		}
		if ok {
			result = append(result, summary)
		}
		if len(result) >= limit {
			break
		}
	}
	return result, nil
}

// ListConflicts lists proposed conflict edges with both assertion summaries.
func (s *Store) ListConflicts(scope string, limit int) ([]map[string]any, error) {
	if limit == 0 {
		limit = 20
	}
	if limit < 1 {
		limit = 1
	}
	rows, err := queryMaps(s.conn,
		`SELECT * FROM meta_edge
		 WHERE scope = ? AND meta_kind = 'conflicts-with' AND status = 'proposed'
		 ORDER BY id DESC LIMIT ?`, scope, limit)
	if err != nil {
		return nil, err
	}
	result := []map[string]any{}
	for _, row := range rows {
		left, leftOK, err := s.AssertionSummary(mapString(row, "lhs_id"))
		if err != nil {
			return nil, err
		}
		right, rightOK, err := s.AssertionSummary(mapString(row, "rhs_id"))
		if err != nil {
			return nil, err
		}
		if leftOK && rightOK {
			result = append(result, map[string]any{
				"edge_id": mapInt64(row, "id"),
				"left":    left,
				"right":   right,
			})
		}
	}
	return result, nil
}

// FindNodes is the only reader that runs the vector leg with no deadline.
func (s *Store) FindNodes(query, scope string, limit int) ([]map[string]any, error) {
	if pyStrip(query) == "" {
		return []map[string]any{}, nil
	}
	if limit == 0 {
		limit = 20
	}
	seeds, _, err := s.SearchSeeds(query, SeedOptions{
		Scope:  scope,
		Reader: "owner",
		Limit:  limit,
	})
	if err != nil {
		return nil, err
	}
	result := []map[string]any{}
	for _, seed := range seeds {
		node, ok, err := s.Node(seed.NodeID)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		aliases, err := s.Aliases(node.ID)
		if err != nil {
			return nil, err
		}
		legs := make([]string, 0, len(seed.Legs))
		for leg := range seed.Legs {
			legs = append(legs, leg)
		}
		sort.Strings(legs)
		result = append(result, map[string]any{
			"id":       node.ID,
			"kind":     node.Kind,
			"label":    node.Label,
			"status":   node.Status,
			"audience": node.Audience,
			"aliases":  aliases,
			"score":    pyRound(seed.Score, 4),
			"legs":     legs,
		})
	}
	return result, nil
}

// GovernanceReport reports zero-LLM governance candidates.
func (s *Store) GovernanceReport(scope string, limit int) (map[string]any, error) {
	if limit == 0 {
		limit = 20
	}
	if limit < 1 {
		limit = 1
	}
	aliasRows, err := queryMaps(s.conn,
		`SELECT a.norm_alias, group_concat(DISTINCT a.node_id) AS ids,
		        count(DISTINCT a.node_id) AS total
		 FROM node_alias a JOIN node n ON n.id = a.node_id
		 WHERE n.scope = ? AND n.status IN ('provisional', 'active')
		 GROUP BY a.norm_alias
		 HAVING total > 1
		 ORDER BY a.norm_alias
		 LIMIT ?`, scope, limit)
	if err != nil {
		return nil, err
	}
	aliasCollisions := []map[string]any{}
	for _, row := range aliasRows {
		idsFound := []string{}
		for _, value := range strings.Split(mapString(row, "ids"), ",") {
			if value != "" {
				idsFound = append(idsFound, value)
			}
		}
		sort.Strings(idsFound)
		labels, err := s.NodeLabels(idsFound)
		if err != nil {
			return nil, err
		}
		nodes := []map[string]any{}
		for _, nodeID := range idsFound {
			nodes = append(nodes, map[string]any{
				"id":    nodeID,
				"label": labels[nodeID],
			})
		}
		aliasCollisions = append(aliasCollisions, map[string]any{
			"alias": mapString(row, "norm_alias"),
			"nodes": nodes,
		})
	}

	labelRows, err := queryMaps(s.conn,
		`SELECT id, label, kind FROM node
		 WHERE scope = ? AND kind IN ('referent', 'relation_type')
		   AND status IN ('provisional', 'active')
		 ORDER BY label LIMIT 400`, scope)
	if err != nil {
		return nil, err
	}
	similar := []map[string]any{}
	for _, kind := range []string{"referent", "relation_type"} {
		group := []map[string]any{}
		for _, row := range labelRows {
			if mapString(row, "kind") == kind {
				group = append(group, row)
			}
		}
		for index, left := range group {
			for _, right := range group[index+1:] {
				if mapString(left, "label") == mapString(right, "label") {
					continue
				}
				ratio := sequenceRatio(
					[]rune(mapString(left, "label")),
					[]rune(mapString(right, "label")),
					true,
				)
				if ratio < 0.70 {
					continue
				}
				similar = append(similar, map[string]any{
					"kind":  kind,
					"ratio": pyRound(ratio, 3),
					"left": map[string]any{
						"id":    mapString(left, "id"),
						"label": mapString(left, "label"),
					},
					"right": map[string]any{
						"id":    mapString(right, "id"),
						"label": mapString(right, "label"),
					},
				})
			}
		}
	}
	sort.SliceStable(similar, func(left, right int) bool {
		return similar[left]["ratio"].(float64) > similar[right]["ratio"].(float64)
	})
	if limit < len(similar) {
		similar = similar[:limit]
	}

	provisionalRows, err := queryMaps(s.conn,
		`SELECT n.id, n.label, n.kind,
		        (SELECT count(*) FROM assertion a
		         WHERE a.scope = n.scope
		           AND (a.subject_id = n.id OR a.object_id = n.id OR a.rel_id = n.id)) AS uses
		 FROM node n
		 WHERE n.scope = ? AND n.status = 'provisional' AND n.kind != 'assertion'
		 ORDER BY uses DESC, n.created_at DESC LIMIT ?`, scope, limit)
	if err != nil {
		return nil, err
	}
	provisional := make([]map[string]any, 0, len(provisionalRows))
	for _, row := range provisionalRows {
		provisional = append(provisional, map[string]any{
			"id":    mapString(row, "id"),
			"kind":  mapString(row, "kind"),
			"label": mapString(row, "label"),
			"uses":  mapInt64(row, "uses"),
		})
	}

	counts := map[string]int64{}
	queries := []struct {
		key   string
		query string
	}{
		{"provisional", "SELECT count(*) FROM node WHERE scope = ? AND status = 'provisional' AND kind != 'assertion'"},
		{"merged", "SELECT count(*) FROM node WHERE scope = ? AND status = 'merged'"},
		{"retracted", "SELECT count(*) FROM assertion WHERE scope = ? AND status = 'retracted'"},
		{"superseded", "SELECT count(*) FROM assertion WHERE scope = ? AND status = 'superseded'"},
	}
	for _, item := range queries {
		count, err := queryCount(s.conn, item.query, scope)
		if err != nil {
			return nil, err
		}
		counts[item.key] = count
	}
	staleVectors, err := queryCount(s.conn,
		`SELECT count(*) FROM vector v LEFT JOIN node n ON n.id = v.owner_hash
		 WHERE v.owner_kind = 'node' AND n.id IS NULL`)
	if err != nil {
		return nil, err
	}
	conflicts, err := s.ListConflicts(scope, limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"ok":               true,
		"scope":            scope,
		"conflicts":        conflicts,
		"alias_collisions": aliasCollisions,
		"similar_labels":   similar,
		"provisional":      provisional,
		"counts":           counts,
		"stale_vectors":    staleVectors,
	}, nil
}

// RetractAssertion marks one assertion retracted (append-only audit row).
func (s *Store) RetractAssertion(nodeID, reason, createdBy string) (map[string]any, error) {
	createdBy = defaultIfEmpty(createdBy, "user")
	row, err := queryOneMap(s.conn, "SELECT * FROM assertion WHERE node_id = ?", nodeID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fmt.Errorf("assertion not found")
	}
	if mapString(row, "status") != "retracted" {
		scope := mapString(row, "scope")
		err = db.WithImmediateTx(s.conn, func(tx *sql.Tx) error {
			if _, err := tx.Exec("UPDATE assertion SET status = 'retracted' WHERE node_id = ?", nodeID); err != nil {
				return err
			}
			// Proposed conflicts die with their assertion (mirror ResolveConflict).
			if _, err := tx.Exec(
				`UPDATE meta_edge SET status = 'reverted'
				 WHERE scope = ? AND meta_kind = 'conflicts-with' AND status = 'proposed'
				   AND (lhs_id = ? OR rhs_id = ?)`,
				scope, nodeID, nodeID,
			); err != nil {
				return err
			}
			_, err := insertEvent(tx, scope, "assert.retract", map[string]any{
				"assertion": nodeID,
			}, reason, createdBy, false)
			return err
		})
		if err != nil {
			return nil, err
		}
	}
	summary, ok, err := s.AssertionSummary(nodeID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return map[string]any{}, nil
	}
	return summary, nil
}

// ResolveConflict records a winner and demotes the loser; it never deletes.
func (s *Store) ResolveConflict(winnerID, loserID, reason string, retractLoser bool, createdBy string) (map[string]any, error) {
	createdBy = defaultIfEmpty(createdBy, "user")
	winner, err := queryOneMap(s.conn, "SELECT * FROM assertion WHERE node_id = ?", winnerID)
	if err != nil {
		return nil, err
	}
	loser, err := queryOneMap(s.conn, "SELECT * FROM assertion WHERE node_id = ?", loserID)
	if err != nil {
		return nil, err
	}
	if winner == nil || loser == nil {
		return nil, fmt.Errorf("both assertions must exist")
	}
	if winnerID == loserID {
		return nil, fmt.Errorf("winner and loser must differ")
	}
	scope := mapString(winner, "scope")
	newStatus := "superseded"
	if retractLoser {
		newStatus = "retracted"
	}
	now := clock.Unix()
	err = db.WithImmediateTx(s.conn, func(tx *sql.Tx) error {
		if _, err := tx.Exec(
			"UPDATE assertion SET status = ? WHERE node_id = ?", newStatus, loserID,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO meta_edge
			   (scope, meta_kind, lhs_id, rhs_id, status, evidence_anchor, created_at, created_by)
			 VALUES (?, 'supersedes', ?, ?, 'active', ?, ?, ?)`,
			scope, winnerID, loserID,
			fmt.Sprintf("conflict:%s>%s", firstChars(winnerID, 8), firstChars(loserID, 8)),
			now, createdBy,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(
			`UPDATE meta_edge SET status = 'reverted'
			 WHERE scope = ? AND meta_kind = 'conflicts-with' AND status = 'proposed'
			   AND ((lhs_id = ? AND rhs_id = ?) OR (lhs_id = ? AND rhs_id = ?))`,
			scope, winnerID, loserID, loserID, winnerID,
		); err != nil {
			return err
		}
		_, err := insertEvent(tx, scope, "conflict.resolve", map[string]any{
			"winner":     winnerID,
			"loser":      loserID,
			"new_status": newStatus,
		}, reason, createdBy, true)
		return err
	})
	if err != nil {
		return nil, err
	}
	winnerSummary, _, err := s.AssertionSummary(winnerID)
	if err != nil {
		return nil, err
	}
	loserSummary, _, err := s.AssertionSummary(loserID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"ok":           true,
		"winner":       winnerSummary,
		"loser":        loserSummary,
		"loser_status": newStatus,
	}, nil
}

// AddMetaEdge registers subtype-of / inverse-of nomination edges.
func (s *Store) AddMetaEdge(scope, metaKind, lhsID, rhsID, evidence, createdBy string) (map[string]any, error) {
	createdBy = defaultIfEmpty(createdBy, "user")
	if metaKind != "subtype-of" && metaKind != "inverse-of" {
		return nil, fmt.Errorf("meta kind must be one of: subtype-of, inverse-of")
	}
	if lhsID == "" || rhsID == "" || lhsID == rhsID {
		return nil, fmt.Errorf("lhs and rhs must be two distinct node ids")
	}
	left, leftOK, err := s.Node(lhsID)
	if err != nil {
		return nil, err
	}
	right, rightOK, err := s.Node(rhsID)
	if err != nil {
		return nil, err
	}
	if !leftOK || !rightOK {
		return nil, fmt.Errorf("both nodes must exist")
	}
	if left.Scope != scope || right.Scope != scope {
		return nil, fmt.Errorf("nodes are in different scopes")
	}
	now := clock.Unix()
	inserted := false
	err = db.WithImmediateTx(s.conn, func(tx *sql.Tx) error {
		affected, err := execCount(tx,
			`INSERT OR IGNORE INTO meta_edge
			   (scope, meta_kind, lhs_id, rhs_id, status, evidence_anchor, created_at, created_by)
			 VALUES (?, ?, ?, ?, 'active', ?, ?, ?)`,
			scope, metaKind, lhsID, rhsID, evidence, now, createdBy)
		if err != nil {
			return err
		}
		inserted = affected > 0
		if inserted {
			_, err = insertEvent(tx, scope, "meta.add", map[string]any{
				"kind": metaKind,
				"lhs":  lhsID,
				"rhs":  rhsID,
			}, "", createdBy, true)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"ok": true, "kind": metaKind, "lhs": lhsID, "rhs": rhsID, "inserted": inserted,
	}, nil
}

// RemoveMetaEdge reverts an active subtype-of / inverse-of edge.
func (s *Store) RemoveMetaEdge(scope, metaKind, lhsID, rhsID, createdBy string) (map[string]any, error) {
	createdBy = defaultIfEmpty(createdBy, "user")
	if metaKind != "subtype-of" && metaKind != "inverse-of" {
		return nil, fmt.Errorf("meta kind must be one of: subtype-of, inverse-of")
	}
	affected, err := execCount(s.conn,
		`UPDATE meta_edge SET status = 'reverted'
		 WHERE scope = ? AND meta_kind = ? AND status = 'active'
		   AND ((lhs_id = ? AND rhs_id = ?) OR (lhs_id = ? AND rhs_id = ?))`,
		scope, metaKind, lhsID, rhsID, rhsID, lhsID)
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		return nil, fmt.Errorf("no active meta edge between these nodes")
	}
	if _, err := s.Event(scope, "meta.remove", map[string]any{
		"kind": metaKind,
		"lhs":  lhsID,
		"rhs":  rhsID,
	}, "", createdBy); err != nil {
		return nil, err
	}
	return map[string]any{
		"ok": true, "kind": metaKind, "lhs": lhsID, "rhs": rhsID, "reverted": affected,
	}, nil
}

// MergeNodes folds drop into keep with aliases, a same-as edge and a merge bit.
func (s *Store) MergeNodes(keepID, dropID, createdBy string) (map[string]any, error) {
	createdBy = defaultIfEmpty(createdBy, "user")
	keep, keepOK, err := s.Node(keepID)
	if err != nil {
		return nil, err
	}
	drop, dropOK, err := s.Node(dropID)
	if err != nil {
		return nil, err
	}
	if !keepOK || !dropOK {
		return nil, fmt.Errorf("both nodes must exist")
	}
	if keepID == dropID {
		return nil, fmt.Errorf("keep and drop must differ")
	}
	if keep.Kind == "assertion" || drop.Kind == "assertion" {
		return nil, fmt.Errorf("cannot merge assertion nodes")
	}
	if keep.Scope != drop.Scope {
		return nil, fmt.Errorf("nodes are in different scopes")
	}
	scope := keep.Scope
	now := clock.Unix()
	err = db.WithImmediateTx(s.conn, func(tx *sql.Tx) error {
		aliases := []string{drop.Label}
		aliasRows, err := queryMaps(tx,
			"SELECT alias FROM node_alias WHERE node_id = ? ORDER BY created_at", dropID)
		if err != nil {
			return err
		}
		for _, row := range aliasRows {
			aliases = append(aliases, mapString(row, "alias"))
		}
		for _, alias := range aliases {
			clean := pyStrip(alias)
			if clean == "" {
				continue
			}
			norm := ids.NormalizeLabel(clean)
			if norm == "" || norm == keep.NormLabel {
				continue
			}
			if _, err := tx.Exec(
				`INSERT OR IGNORE INTO node_alias(node_id, alias, norm_alias, source, created_at)
				 SELECT ?, ?, ?, 'merge', ?
				 WHERE NOT EXISTS (
				   SELECT 1 FROM node_alias ox JOIN node onode ON onode.id = ox.node_id
				   WHERE onode.scope = ? AND ox.node_id NOT IN (?, ?)
				     AND onode.status IN ('provisional', 'active') AND onode.kind != 'assertion'
				     AND ox.norm_alias = ?
				 )`,
				keepID, clean, norm, now, scope, keepID, dropID, norm,
			); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO meta_edge
			   (scope, meta_kind, lhs_id, rhs_id, status, evidence_anchor, created_at, created_by)
			 VALUES (?, 'same-as', ?, ?, 'active', ?, ?, ?)`,
			scope, keepID, dropID, "merge:"+firstChars(dropID, 8), now, createdBy,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(
			"UPDATE node SET status = 'merged', retired_at = ? WHERE id = ?", now, dropID,
		); err != nil {
			return err
		}
		_, err = insertEvent(tx, scope, "node.merge", map[string]any{
			"keep": keepID,
			"drop": dropID,
		}, "", createdBy, true)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := s.refreshNodeFTS(keepID); err != nil {
		return nil, err
	}
	return map[string]any{
		"ok": true, "keep": keepID, "drop": dropID, "alias": drop.Label,
	}, nil
}

// UnmergeNodes reverts a same-as merge and revives the dropped node.
func (s *Store) UnmergeNodes(keepID, dropID, createdBy string) (map[string]any, error) {
	createdBy = defaultIfEmpty(createdBy, "user")
	err := db.WithImmediateTx(s.conn, func(tx *sql.Tx) error {
		affected, err := execCount(tx,
			`UPDATE meta_edge SET status = 'reverted'
			 WHERE meta_kind = 'same-as' AND status = 'active'
			   AND ((lhs_id = ? AND rhs_id = ?) OR (lhs_id = ? AND rhs_id = ?))`,
			keepID, dropID, dropID, keepID)
		if err != nil {
			return err
		}
		if affected == 0 {
			return fmt.Errorf("no active same-as edge between these nodes")
		}
		if _, err := tx.Exec(
			"UPDATE node SET status = 'active', retired_at = NULL WHERE id = ?", dropID,
		); err != nil {
			return err
		}
		var scope string
		if err := tx.QueryRow("SELECT scope FROM node WHERE id = ?", keepID).Scan(&scope); err != nil {
			return err
		}
		_, err = insertEvent(tx, scope, "node.unmerge", map[string]any{
			"keep": keepID,
			"drop": dropID,
		}, "", createdBy, true)
		return err
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "keep": keepID, "drop": dropID}, nil
}

// SplitNodes performs a governed split of a previously merged node pair (keepID, dropID):
// unlike UnmergeNodes (which treats the merge as a mistake that never held), SplitNodes
// acknowledges that the merge held historically and records a forward split decision
// (deactivating the active same-as identity closure edge with a "split:" anchor marker,
// restoring the dropped node from merged -> active while keeping append-only historical aliases,
// and recording a node.split audit event).
func (s *Store) SplitNodes(keepID, dropID, reason, createdBy string) (map[string]any, error) {
	createdBy = defaultIfEmpty(createdBy, "user")
	reason = strings.TrimSpace(reason)
	edgeRows, err := queryMaps(s.conn,
		`SELECT id, scope, lhs_id, rhs_id, status, COALESCE(evidence_anchor, '') AS evidence_anchor
			 FROM meta_edge
			 WHERE meta_kind = 'same-as'
			   AND ((lhs_id = ? AND rhs_id = ?) OR (lhs_id = ? AND rhs_id = ?))
			 ORDER BY (status = 'active') DESC, id DESC LIMIT 1`,
		keepID, dropID, dropID, keepID)
	if err != nil {
		return nil, err
	}
	if len(edgeRows) == 0 {
		return nil, fmt.Errorf("no same-as merge edge between these nodes")
	}
	edge := edgeRows[0]
	metaID := mapInt64(edge, "id")
	edgeStatus := mapString(edge, "status")
	anchor := mapString(edge, "evidence_anchor")
	if edgeStatus == "reverted" && strings.HasPrefix(anchor, "split:") {
		return map[string]any{
			"ok": true, "split": false, "already": true,
			"meta_id": metaID, "keep": keepID, "drop": dropID,
		}, nil
	}
	if edgeStatus != "active" {
		return nil, fmt.Errorf("same-as edge status is %s (split requires an active merge edge)", edgeStatus)
	}
	dropNode, ok, err := s.Node(dropID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("drop node not found")
	}
	priorStatus := dropNode.Status
	splitAnchor := "split:" + firstChars(dropID, 8)
	if reason != "" {
		splitAnchor = "split:" + reason
	}
	err = db.WithImmediateTx(s.conn, func(tx *sql.Tx) error {
		if _, err := tx.Exec(
			"UPDATE meta_edge SET status = 'reverted', evidence_anchor = ? WHERE id = ? AND status = 'active'",
			splitAnchor, metaID,
		); err != nil {
			return err
		}
		if priorStatus == "merged" {
			if _, err := tx.Exec(
				"UPDATE node SET status = 'active', retired_at = NULL WHERE id = ?", dropID,
			); err != nil {
				return err
			}
		}
		_, err := insertEvent(tx, dropNode.Scope, "node.split", map[string]any{
			"keep":         keepID,
			"drop":         dropID,
			"meta_id":      metaID,
			"prior_status": priorStatus,
		}, reason, createdBy, true)
		return err
	})
	if err != nil {
		return nil, err
	}
	_ = s.refreshNodeFTS(dropID)
	return map[string]any{
		"ok": true, "split": true, "already": false,
		"meta_id": metaID, "keep": keepID, "drop": dropID, "prior_status": priorStatus,
	}, nil
}

// PromoteNode flips a provisional node to active.
func (s *Store) PromoteNode(nodeID, createdBy string) (map[string]any, error) {
	createdBy = defaultIfEmpty(createdBy, "user")
	node, ok, err := s.Node(nodeID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("node not found")
	}
	if node.Kind == "assertion" {
		return map[string]any{
			"ok": true, "id": nodeID, "status": node.Status,
			"note": "assertion nodes have no promotion",
		}, nil
	}
	if _, err := s.conn.Exec("UPDATE node SET status = 'active' WHERE id = ?", nodeID); err != nil {
		return nil, err
	}
	if _, err := s.Event(node.Scope, "node.promote", map[string]any{
		"node": nodeID,
	}, "provisional -> active", createdBy); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "id": nodeID, "status": "active"}, nil
}
