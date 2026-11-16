// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rocareer/rolling-memory/internal/clock"
	"github.com/rocareer/rolling-memory/internal/db"
	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/memcore"
)

// NormalizeHierarchicalPath normalizes a slash-separated spatial locus path
// (whether a code repository path like "rocareer/rolling-memory/internal/store"
// or a geographic path like "中国/浙江/杭州/西湖") without any domain-specific heuristics.
func NormalizeHierarchicalPath(raw string) string {
	return memcore.NormalizeHierarchicalPath(raw)
}

// HierarchicalLCPScore computes the language-agnostic, domain-neutral Longest Common Prefix (LCP)
// tree affinity score in [0, 1]:
//
//	K_space(A, B) = 2 * LCP(seg(A), seg(B)) / (|seg(A)| + |seg(B)|)
//
// Disjoint or empty paths return 0.0 (zero penalty for far-field / global memories).
func HierarchicalLCPScore(pathA, pathB string) float64 {
	return memcore.HierarchicalLCPScore(pathA, pathB)
}

// IncarnationGenealogyScore computes the incarnation role hierarchy affinity score in [0, 1]:
//
//	K_inc(I_q, I_a) = LCP(seg(I_q), seg(I_a)) / max(|seg(I_q)|, |seg(I_a)|)
func IncarnationGenealogyScore(incQuery, incAssertion string) float64 {
	return memcore.IncarnationGenealogyScore(incQuery, incAssertion)
}

// EnsureDualTrackLocus materializes Track 2 of Dual-Track Spatial Encoding:
//   - Ensures up to 4 hierarchical locus prefixes (e.g. "org", "org/repo",
//     "org/repo/pkg", "org/repo/pkg/hooks") as referent nodes with origin='world';
//   - Links each child spatial node to its parent via an active meta_edge(subtype-of);
//   - Registers the leaf segment short name (e.g. "hooks") as an alias on the leaf
//     spatial node when the alias is not already bound to another live node in scope.
func (s *Store) EnsureDualTrackLocus(scope, rawLocus, audience string) (string, []string, error) {
	prefixes, leafShort := memcore.DualTrackLocusPrefixes(rawLocus)
	if len(prefixes) == 0 {
		return "", nil, nil
	}
	if audience == "" || audience == "private" {
		audience = "shared"
	}
	_ = db.EnsureMemcoreSchema(s.conn)
	world := "actual"
	now := clock.Unix()
	nodeIDs := make([]string, 0, len(prefixes))
	var parentID, leafID string
	for _, p := range prefixes {
		locusCopy := p
		nid, err := s.EnsureNode(scope, "referent", p, NodeOptions{
			Audience: audience,
			World:    &world,
			Locus:    &locusCopy,
			Status:   "active",
		})
		if err != nil || nid == "" {
			continue
		}
		_ = s.setNodeOriginFillEmpty(scope, nid, "world")
		if parentID != "" && parentID != nid {
			edgeKind := "part-of"
			if strings.HasPrefix(scope, "mem:golden:") {
				edgeKind = "subtype-of"
			}
			_ = s.ensureActiveMetaEdge(scope, edgeKind, nid, parentID, "spatial-locus-tree", now)
		}
		parentID = nid
		leafID = nid
		nodeIDs = append(nodeIDs, nid)
	}
	if leafID != "" && leafShort != "" {
		normAlias := ids.NormalizeLabel(leafShort)
		if normAlias != "" {
			conflictRow, err := queryOneMap(s.conn,
				`SELECT id FROM node WHERE scope = ? AND norm_label = ? AND id <> ? AND status IN ('provisional','active')
				 UNION ALL
				 SELECT a.node_id AS id FROM node_alias a JOIN node n ON n.id = a.node_id
				 WHERE n.scope = ? AND a.norm_alias = ? AND a.node_id <> ? AND n.status IN ('provisional','active')
				 LIMIT 1`,
				scope, normAlias, leafID, scope, normAlias, leafID)
			if err == nil && conflictRow == nil {
				_, _ = s.AddAlias(leafID, leafShort, "spatial")
			}
		}
	}
	return leafID, nodeIDs, nil
}

// EnsureIncarnationHierarchy materializes a hierarchical incarnation role URI
// (e.g. "coding/zcode/main" -> "coding" -> "coding/zcode" -> "coding/zcode/main")
// as incarnation nodes linked by active subtype-of meta edges, returning the leaf node id.
func (s *Store) EnsureIncarnationHierarchy(scope, rawIncarnation, audience string) (string, error) {
	prefixes := memcore.IncarnationHierarchyPrefixes(rawIncarnation)
	if len(prefixes) == 0 {
		return "", nil
	}
	if audience == "" || audience == "private" {
		audience = "shared"
	}
	_ = db.EnsureMemcoreSchema(s.conn)
	now := clock.Unix()
	var parentID, leafID string
	for _, prefix := range prefixes {
		nid, err := s.EnsureNode(scope, "incarnation", prefix, NodeOptions{
			Audience: audience,
			Status:   "active",
		})
		if err != nil {
			return leafID, err
		}
		if parentID != "" && nid != "" && parentID != nid {
			_ = s.ensureActiveMetaEdge(scope, "subtype-of", nid, parentID, "incarnation-hierarchy", now)
		}
		parentID = nid
		leafID = nid
	}
	return leafID, nil
}

// BindAssertionIncarnation records the leaf incarnation node ID on an assertion node in node_ext.
func (s *Store) BindAssertionIncarnation(scope, assertionNodeID, incarnationNodeID string) error {
	if assertionNodeID == "" || incarnationNodeID == "" {
		return nil
	}
	_ = db.EnsureMemcoreSchema(s.conn)
	return db.WithImmediateTx(s.conn, func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO node_ext(node_id, scope, incarnation_id, ext_ref, origin)
			 VALUES (?, ?, ?, '', '')
			 ON CONFLICT(node_id) DO UPDATE SET
			   incarnation_id = CASE WHEN node_ext.incarnation_id = '' THEN excluded.incarnation_id ELSE node_ext.incarnation_id END`,
			assertionNodeID, scope, incarnationNodeID,
		)
		return err
	})
}

// AssertionIncarnationLabels batch-resolves assertion node IDs to their bound incarnation node labels.
func (s *Store) AssertionIncarnationLabels(scope string, assertionNodeIDs []string) (map[string]string, error) {
	out := map[string]string{}
	if len(assertionNodeIDs) == 0 {
		return out, nil
	}
	_ = db.EnsureMemcoreSchema(s.conn)
	unique := ids.UniqueOrdered(assertionNodeIDs)
	placeholders := strings.TrimRight(strings.Repeat("?,", len(unique)), ",")
	args := make([]any, 0, 1+len(unique))
	args = append(args, scope)
	for _, id := range unique {
		args = append(args, id)
	}
	rows, err := queryMaps(s.conn,
		`SELECT ne.node_id, n.label AS inc_label
		 FROM node_ext ne
		 JOIN node n ON n.id = ne.incarnation_id
		 WHERE ne.scope = ? AND ne.node_id IN (`+placeholders+`) AND ne.incarnation_id != ''`,
		args...)
	if err != nil {
		return out, nil
	}
	for _, r := range rows {
		aid := mapString(r, "node_id")
		lb := mapString(r, "inc_label")
		if aid != "" && lb != "" {
			out[aid] = lb
		}
	}
	return out, nil
}

// ObserveSpatialEpisode binds a newly written assertion into its corresponding L1 Episode Universe container
// keyed by (scope, world, carrier, locus, period).
func (s *Store) ObserveSpatialEpisode(scope, assertionNodeID, world, carrier, locus, session string, tValidFrom, tSource int64) {
	if assertionNodeID == "" || strings.TrimSpace(locus) == "" {
		return
	}
	_ = db.EnsureMemcoreSchema(s.conn)
	w := defaultIfEmpty(strings.TrimSpace(world), "actual")
	c := defaultIfEmpty(strings.TrimSpace(carrier), "virtual")
	loc := memcore.NormalizeHierarchicalPath(locus)
	if loc == "" {
		loc = strings.TrimSpace(locus)
	}
	ts := tValidFrom
	if ts <= 0 {
		ts = tSource
	}
	if ts <= 0 {
		ts = clock.Unix()
	}
	period := time.Unix(ts, 0).UTC().Format("2006-01")
	epKey := fmt.Sprintf("ep:l1:%s:%s:%s:%s:%s", scope, w, c, period, loc)
	now := clock.Unix()
	_ = s.MemcoreBackend().UpsertEpisodeContainer(context.Background(), memcore.EpisodeContainer{
		Scope:       scope,
		EpKey:       epKey,
		Generation:  1,
		Level:       1,
		World:       w,
		Carrier:     c,
		Locus:       loc,
		SessionKey:  defaultIfEmpty(session, "default"),
		TStart:      ts,
		TEnd:        ts,
		TSourceMin:  tSource,
		TSourceMax:  tSource,
		MemberCount: 1,
		UpdatedAt:   now,
	}, []memcore.EpisodeMember{
		{
			Scope:      scope,
			MemberID:   assertionNodeID,
			MemberKind: "assertion",
			Facet:      "scene",
			CreatedAt:  now,
		},
	})
}

func (s *Store) setNodeOriginFillEmpty(scope, nodeID, origin string) error {
	if nodeID == "" || origin == "" {
		return nil
	}
	_ = db.EnsureMemcoreSchema(s.conn)
	return db.WithImmediateTx(s.conn, func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO node_ext(node_id, scope, incarnation_id, ext_ref, origin)
			 VALUES (?, ?, '', '', ?)
			 ON CONFLICT(node_id) DO UPDATE SET
			   origin = CASE WHEN node_ext.origin = '' OR excluded.origin = 'self' THEN excluded.origin ELSE node_ext.origin END`,
			nodeID, scope, origin,
		)
		return err
	})
}

// EnsureNodeOrigin sets the origin ("self" | "roster" | "world") on a node in node_ext.
func (s *Store) EnsureNodeOrigin(scope, nodeID, origin string) error {
	return s.setNodeOriginFillEmpty(scope, nodeID, origin)
}

// NodeOrigin returns the stored origin ("self" | "roster" | "world" | "") for a node ID.
func (s *Store) NodeOrigin(nodeID string) (string, error) {
	if nodeID == "" {
		return "", nil
	}
	_ = db.EnsureMemcoreSchema(s.conn)
	row, err := queryOneMap(s.conn, `SELECT origin FROM node_ext WHERE node_id = ?`, nodeID)
	if err != nil || row == nil {
		return "", err
	}
	return mapString(row, "origin"), nil
}

// SplitInlineCategoryNames deterministically unpacks an inline category declaration
// (subject_category / object_category) from a string (JSON array or comma/semicolon/spaced-slash
// separated list) or slice into deduplicated, modal-tag-stripped category names.
// Zero stopword tables or semantic guessing; bare slashes inside tokens (e.g. "TCP/IP", "AC/DC") are preserved.
func SplitInlineCategoryNames(raw any) []string {
	var rawItems []any
	switch v := raw.(type) {
	case string:
		s := strings.TrimSpace(memcore.StripModalBracketSlotPrefix(v))
		if s == "" {
			return nil
		}
		if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
			var arr []any
			if err := json.Unmarshal([]byte(s), &arr); err == nil && len(arr) > 0 {
				rawItems = arr
			}
		}
		if rawItems == nil {
			for _, seg := range splitInlineCategorySegment(s) {
				rawItems = append(rawItems, seg)
			}
		}
	case []any:
		rawItems = v
	case []string:
		for _, s := range v {
			rawItems = append(rawItems, s)
		}
	}
	if len(rawItems) == 0 {
		return nil
	}
	out := make([]string, 0, len(rawItems))
	seen := map[string]bool{}
	for _, it := range rawItems {
		for _, seg := range splitInlineCategorySegment(memcore.StripModalBracketSlotPrefix(fmt.Sprint(it))) {
			cat := strings.TrimSpace(memcore.StripModalBracketSlotPrefix(seg))
			if cat == "" || cat == "<nil>" || memcore.CanonicalMultimodalProtocolTag(cat) != "" || memcore.IsRolePlaceholder(cat) {
				continue
			}
			lo := strings.ToLower(cat)
			if seen[lo] {
				continue
			}
			seen[lo] = true
			out = append(out, cat)
		}
	}
	return out
}

func splitInlineCategorySegment(s string) []string {
	for _, pair := range [][2]string{{" / ", ","}, {" | ", ","}, {"/ ", ","}, {" /", ","}, {"| ", ","}, {" |", ","}} {
		s = strings.ReplaceAll(s, pair[0], pair[1])
	}
	var out []string
	for _, seg := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == '，' || r == ';' || r == '；' || r == '、'
	}) {
		if seg = strings.TrimSpace(seg); seg != "" {
			out = append(out, seg)
		}
	}
	return out
}

// BindInlineConceptCategory materializes one or more inline parent concept categories (subject_category / object_category)
// declared on an assertion into referent concept hub nodes and active subtype-of meta_edges (entityID -> catID).
func (s *Store) BindInlineConceptCategory(scope, entityID, entityLabel string, rawCategory any, audience string) error {
	if entityID == "" || rawCategory == nil {
		return nil
	}
	cats := SplitInlineCategoryNames(rawCategory)
	if len(cats) == 0 {
		return nil
	}
	if audience == "" || audience == "private" {
		audience = "shared"
	}
	cleanEntityLabel := strings.TrimSpace(entityLabel)
	var firstErr error
	for _, cat := range cats {
		if strings.EqualFold(cat, cleanEntityLabel) {
			continue
		}
		catID, err := s.EnsureNode(scope, "referent", cat, NodeOptions{
			Audience: audience,
			Status:   "active",
		})
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if catID == "" || catID == entityID {
			continue
		}
		if err := s.ensureActiveMetaEdge(scope, "subtype-of", entityID, catID, "inline-category", clock.Unix()); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// BindAssertionParticipants ensures each named co-participant as a referent node with origin='roster'
// (Object Symmetry Law: host User == group-chat peer == reported person) and records their participation
// in the canonical `participant` table.
func (s *Store) BindAssertionParticipants(scope, assertionNodeID, audience string, participants []string) error {
	if assertionNodeID == "" || len(participants) == 0 {
		return nil
	}
	if err := db.EnsureMemcoreSchema(s.conn); err != nil {
		return err
	}
	if audience == "" || audience == "private" {
		audience = "shared"
	}
	roleID, err := s.EnsureNode(scope, "role", "participant", NodeOptions{Audience: audience, Status: "active"})
	if err != nil {
		return err
	}
	seq := 0
	for _, rawName := range participants {
		name := strings.TrimSpace(rawName)
		if name == "" {
			continue
		}
		nid, err := s.EnsureNode(scope, "referent", name, NodeOptions{Audience: audience, Status: "active"})
		if err != nil {
			return err
		}
		if nid == "" {
			continue
		}
		_ = s.setNodeOriginFillEmpty(scope, nid, "roster")
		curSeq := seq
		seq++
		if err := db.WithImmediateTx(s.conn, func(tx *sql.Tx) error {
			_, err := tx.Exec(
				`INSERT OR IGNORE INTO participant(assertion_node_id, seq, role_id, target_id, literal)
				 VALUES (?, ?, ?, ?, NULL)`,
				assertionNodeID, curSeq, roleID, nid,
			)
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

// EnsureExplicitMetaEdge materializes one canonical 8-kind closed-set meta edge in SQLite.
func (s *Store) EnsureExplicitMetaEdge(scope, rawKind, lhsID, rhsID, evidence string) error {
	if lhsID == "" || rhsID == "" || lhsID == rhsID {
		return nil
	}
	canon := memcore.NormalizeMetaKind(rawKind)
	if !memcore.IsCanonicalMetaKind(canon) {
		return nil
	}
	if err := db.EnsureMemcoreSchema(s.conn); err != nil {
		return err
	}
	hyphenKind := strings.ReplaceAll(canon, "_", "-")
	return s.ensureActiveMetaEdge(scope, hyphenKind, lhsID, rhsID, defaultIfEmpty(evidence, "llm-closed-set"), clock.Unix())
}

func (s *Store) ensureActiveMetaEdge(scope, metaKind, lhsID, rhsID, evidence string, now int64) error {
	if lhsID == "" || rhsID == "" || lhsID == rhsID {
		return nil
	}
	existing, err := queryOneMap(s.conn,
		`SELECT id FROM meta_edge WHERE scope = ? AND meta_kind = ? AND lhs_id = ? AND rhs_id = ? AND status = 'active' LIMIT 1`,
		scope, metaKind, lhsID, rhsID)
	if err != nil || existing != nil {
		return err
	}
	return db.WithImmediateTx(s.conn, func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO meta_edge(scope, meta_kind, lhs_id, rhs_id, status, evidence_anchor, created_at, created_by)
			 VALUES (?, ?, ?, ?, 'active', ?, ?, 'system')
			 ON CONFLICT(scope, meta_kind, lhs_id, rhs_id, status) DO NOTHING`,
			scope, metaKind, lhsID, rhsID, evidence, now,
		)
		return err
	})
}

func (s *Store) applyCandidatePresenceResonance(scope string, presence memcore.PresenceCoordinates, candidates []Candidate, gaps map[string]int) {
	if len(candidates) == 0 || presence.IsEmpty() {
		return
	}
	var incLabels map[string]string
	if presence.Incarnation != "" {
		idsToQuery := make([]string, 0, len(candidates))
		for _, c := range candidates {
			if c.Data.NodeID != "" {
				idsToQuery = append(idsToQuery, c.Data.NodeID)
			}
		}
		incLabels, _ = s.AssertionIncarnationLabels(scope, idsToQuery)
	}
	boosted := 0
	for i := range candidates {
		var rowLocus, rowCarrier string
		if candidates[i].Data.Locus != nil {
			rowLocus = *candidates[i].Data.Locus
		}
		if candidates[i].Data.Carrier != nil {
			rowCarrier = *candidates[i].Data.Carrier
		}
		incLabel := ""
		if incLabels != nil {
			incLabel = incLabels[candidates[i].Data.NodeID]
		}
		mult := memcore.SpacetimeResonanceMultiplier(presence, rowLocus, rowCarrier, incLabel)
		if mult > 1.0 {
			candidates[i].Score *= mult
			candidates[i].LexScore *= mult
			boosted++
		}
	}
	if boosted > 0 && gaps != nil {
		gaps["presence_boosted"] = boosted
	}
}

func stringAnySlice(values []string) []any {
	out := make([]any, 0, len(values))
	for _, v := range values {
		out = append(out, v)
	}
	return out
}
