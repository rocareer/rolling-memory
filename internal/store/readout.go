// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"

	"github.com/rocareer/rolling-memory/internal/clock"
	"github.com/rocareer/rolling-memory/internal/db"
	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/memcore"
)

// Health is the read-time health projection (zero storage, zero LLM).
//
// Counts gaps (unresolved mentions, rejected writes), convergence
// (provisional -> active/merged), conflict backlog, coordinate completeness
// and volume; every metric is recomputable from the graph itself.
func (s *Store) Health(scope string, since int64) (map[string]any, error) {
	now := clock.Unix()
	if since < 0 {
		since = 0
	}
	mentionTotal, err := queryCount(s.conn,
		"SELECT count(*) FROM mention WHERE scope = ?", scope)
	if err != nil {
		return nil, err
	}
	unresolvedMentions, err := s.UnresolvedMentionCount(scope)
	if err != nil {
		return nil, err
	}

	rejectedEvents := 0
	rejectedAnchor := int64(0)
	rejectedResolve := int64(0)
	rejectedRows, err := queryMaps(s.conn,
		`SELECT refs_json FROM event
		 WHERE scope = ? AND op = 'assert.rejected' AND (? = 0 OR created_at >= ?)`,
		scope, since, since)
	if err != nil {
		return nil, err
	}
	for _, row := range rejectedRows {
		rejectedEvents++
		refs := map[string]any{}
		if raw := mapString(row, "refs_json"); raw != "" {
			_ = json.Unmarshal([]byte(raw), &refs)
		}
		rejectedAnchor += pyIntValue(refs["anchor_failed"])
		rejectedResolve += pyIntValue(refs["resolve_failed"])
	}

	createdNodes := 0
	convergedNodes := 0
	nodeRows, err := queryMaps(s.conn,
		`SELECT status, count(*) AS total FROM node
		 WHERE scope = ? AND kind != 'assertion' AND (? = 0 OR created_at >= ?)
		 GROUP BY status`, scope, since, since)
	if err != nil {
		return nil, err
	}
	for _, row := range nodeRows {
		total := int(mapInt64(row, "total"))
		createdNodes += total
		status := mapString(row, "status")
		if status == "active" || status == "merged" {
			convergedNodes += total
		}
	}

	eventCounts := map[string]int{}
	eventRows, err := queryMaps(s.conn,
		`SELECT op, count(*) AS total FROM event
		 WHERE scope = ? AND op IN ('node.promote', 'node.merge')
		   AND (? = 0 OR created_at >= ?)
		 GROUP BY op`, scope, since, since)
	if err != nil {
		return nil, err
	}
	for _, row := range eventRows {
		eventCounts[mapString(row, "op")] = int(mapInt64(row, "total"))
	}

	var conflictTotal int64
	var oldest sql.NullInt64
	if err := s.conn.QueryRow(
		`SELECT count(*) AS total, min(created_at) AS oldest FROM meta_edge
		 WHERE scope = ? AND meta_kind = 'conflicts-with' AND status = 'proposed'
		   AND (? = 0 OR created_at >= ?)`, scope, since, since,
	).Scan(&conflictTotal, &oldest); err != nil {
		return nil, err
	}
	oldestAgeDays := 0.0
	if oldest.Valid && oldest.Int64 != 0 {
		oldestAgeDays = pyRound(math.Max(0.0, float64(now-oldest.Int64)/86400.0), 1)
	}

	activeAssertions := 0
	unknownTime := 0
	assertionRows, err := queryMaps(s.conn,
		`SELECT temporal_class, count(*) AS total FROM assertion
		 WHERE scope = ? AND status = 'active'
		 GROUP BY temporal_class`, scope)
	if err != nil {
		return nil, err
	}
	for _, row := range assertionRows {
		total := int(mapInt64(row, "total"))
		activeAssertions += total
		if mapString(row, "temporal_class") == "unknown" {
			unknownTime += total
		}
	}

	// structured memory assertion quality probes: unclosed literal brackets, bare-copula relations and
	// mention-floor rows are the three form classes a misbehaving writer leaves
	// behind (2026-10-28 incident: pre-fix literals lost their closing bracket).
	unclosedLiterals := 0
	copulaRelations := 0
	lowConfidence := 0
	qualityRows, err := queryMaps(s.conn,
		`SELECT coalesce(a.object_literal,'') AS object_literal,
		        coalesce(a.subject_literal,'') AS subject_literal,
		        coalesce(n.label,'') AS rel_label,
		        a.confidence AS confidence
		   FROM assertion a LEFT JOIN node n ON n.id = a.rel_id
		  WHERE a.scope = ? AND a.status = 'active'`, scope)
	if err != nil {
		return nil, err
	}
	for _, row := range qualityRows {
		if !bracketsBalanced(mapString(row, "object_literal")) ||
			!bracketsBalanced(mapString(row, "subject_literal")) {
			unclosedLiterals++
		}
		switch mapString(row, "rel_label") {
		case "为", "是", "有":
			copulaRelations++
		}
		if mapFloat(row, "confidence") <= 0.4 {
			lowConfidence++
		}
	}

	createdInWindow := map[string]int{}
	for _, item := range []struct{ table, column string }{
		{"trace", "recorded_at"},
		{"assertion", "created_at"},
		{"mention", "created_at"},
	} {
		var count int64
		var err error
		if since != 0 {
			count, err = queryCount(s.conn,
				"SELECT count(*) FROM "+item.table+" WHERE scope = ? AND "+item.column+" >= ?",
				scope, since)
		} else {
			count, err = queryCount(s.conn,
				"SELECT count(*) FROM "+item.table+" WHERE scope = ?", scope)
		}
		if err != nil {
			return nil, err
		}
		createdInWindow[item.table] = int(count)
	}

	counts, err := db.TableCounts(s.conn, scope)
	if err != nil {
		return nil, err
	}
	unresolvedRatio := 0.0
	if mentionTotal != 0 {
		unresolvedRatio = pyRound(float64(unresolvedMentions)/float64(mentionTotal), 4)
	}
	convergenceRate := 0.0
	if createdNodes != 0 {
		convergenceRate = pyRound(float64(convergedNodes)/float64(createdNodes), 4)
	}
	knownTimeRatio := 1.0
	if activeAssertions != 0 {
		knownTimeRatio = pyRound(1.0-float64(unknownTime)/float64(activeAssertions), 4)
	}
	return map[string]any{
		"ok":    true,
		"scope": scope,
		"window": map[string]any{
			"since": since,
			"until": now,
		},
		"gaps": map[string]any{
			"mentions":                mentionTotal,
			"unresolved_mentions":     unresolvedMentions,
			"unresolved_ratio":        unresolvedRatio,
			"rejected_events":         rejectedEvents,
			"rejected_anchor_failed":  rejectedAnchor,
			"rejected_resolve_failed": rejectedResolve,
		},
		"convergence": map[string]any{
			"created_nodes":    createdNodes,
			"converged_nodes":  convergedNodes,
			"convergence_rate": convergenceRate,
			"promotes":         eventCounts["node.promote"],
			"merges":           eventCounts["node.merge"],
		},
		"conflicts": map[string]any{
			"proposed":        conflictTotal,
			"oldest_age_days": oldestAgeDays,
		},
		"coordinates": map[string]any{
			"active_assertions": activeAssertions,
			"unknown_time":      unknownTime,
			"known_time_ratio":  knownTimeRatio,
		},
		"quality": map[string]any{
			"unclosed_literals": unclosedLiterals,
			"copula_relations":  copulaRelations,
			"low_confidence":    lowConfidence,
		},
		"volume": map[string]any{
			"counts":            counts,
			"created_in_window": createdInWindow,
		},
	}, nil
}

var halfOpenNumericRangeRe = regexp.MustCompile(`\([0-9]+,\s*[0-9]+\]|\[[0-9]+,\s*[0-9]+\)`)

// bracketsBalanced reports whether a literal's opening brackets close. The
// 2026-10-28 fidelity bug trimmed trailing 「）」 off object literals, so the
// health readout counts literals that have more opening brackets than closing
// brackets while ignoring valid half-open numeric intervals ("(1024,4096]")
// and list-item markers ("1) ... 2) ...").
func bracketsBalanced(value string) bool {
	cleaned := halfOpenNumericRangeRe.ReplaceAllString(value, "")
	for _, pair := range [][2]string{
		{"(", ")"}, {"（", "）"}, {"「", "」"}, {"『", "』"}, {"【", "】"}, {"[", "]"}, {"《", "》"},
	} {
		if strings.Count(cleaned, pair[0]) > strings.Count(cleaned, pair[1]) {
			return false
		}
	}
	return true
}

// AuditArtifactQuality executes the 0-LLM PMAS-100 (Perfect Memory Artifact Standard)
// and field-completion audit over a scope (aligned with the service engine's U17 & A12):
//   - dangling_first_person: active referent nodes containing unresolved first-person possessive prefixes;
//   - role_placeholders:     active referent nodes whose label is a generic transcript role placeholder;
//   - self_loop_rows:        active assertions where subject_id == object_id;
//   - loop_states:           distribution of prospective loop_state values (open, fulfilled, waived, broken);
//   - connectivity:          graph connectivity metrics (ComputeConnectivityMetrics);
//   - pmas_100:              composite quality score in [0, 100].
func (s *Store) AuditArtifactQuality(scope string) (map[string]any, error) {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = s.Config.Scope
	}
	nodeRows, err := queryMaps(s.conn,
		`SELECT id, kind, label FROM node
		 WHERE scope = ? AND status IN ('provisional', 'active') AND kind != 'assertion'`,
		scope)
	if err != nil {
		return nil, err
	}
	danglingFirstPerson := 0
	rolePlaceholders := 0
	nodeKinds := make(map[string]string, len(nodeRows))
	nodeLabels := make(map[string]string, len(nodeRows))
	for _, r := range nodeRows {
		id := mapString(r, "id")
		kind := mapString(r, "kind")
		lb := strings.TrimSpace(mapString(r, "label"))
		nodeKinds[id] = kind
		nodeLabels[id] = lb
		if kind == "referent" {
			if memcore.HasFirstPersonPossessivePrefix(lb) {
				danglingFirstPerson++
			}
			if memcore.IsRolePlaceholder(lb) {
				rolePlaceholders++
			}
		}
	}

	assertRows, err := queryMaps(s.conn,
		`SELECT node_id, COALESCE(subject_id, '') AS subject_id, rel_id,
		        COALESCE(object_id, '') AS object_id, COALESCE(object_literal, '') AS object_literal,
		        COALESCE(loop_state, '') AS loop_state
		 FROM assertion
		 WHERE scope = ? AND status = 'active'`,
		scope)
	if err != nil {
		return nil, err
	}
	selfLoops := 0
	loopStates := map[string]int{
		"open":      0,
		"fulfilled": 0,
		"waived":    0,
		"broken":    0,
	}
	memAssertions := make([]memcore.Assertion, 0, len(assertRows))
	for _, r := range assertRows {
		sid := mapString(r, "subject_id")
		oid := mapString(r, "object_id")
		rid := mapString(r, "rel_id")
		olit := mapString(r, "object_literal")
		ls := strings.ToLower(strings.TrimSpace(mapString(r, "loop_state")))
		if sid != "" && oid != "" && sid == oid {
			selfLoops++
		}
		if _, ok := loopStates[ls]; ok {
			loopStates[ls]++
		}
		memAssertions = append(memAssertions, memcore.Assertion{
			NodeID:        mapString(r, "node_id"),
			SubjectID:     sid,
			RelID:         rid,
			ObjectID:      oid,
			ObjectLiteral: olit,
			LoopState:     ls,
		})
	}
	connMetrics := memcore.ComputeConnectivityMetrics(memAssertions, nodeKinds, nodeLabels)

	score := 100.0
	totalNodes := len(nodeRows)
	if totalNodes > 0 {
		defectRatio := float64(danglingFirstPerson+rolePlaceholders+selfLoops) / float64(totalNodes)
		score -= math.Min(40.0, defectRatio*200.0)
	}
	if len(memAssertions) >= memcore.MinEvidenceQuorumRows && connMetrics.MultiEdgeEntityRatio < 0.50 {
		score -= (0.50 - connMetrics.MultiEdgeEntityRatio) * 30.0
	}
	score = math.Max(0.0, math.Min(100.0, pyRound(score, 2)))
	grade := "S"
	if score < 95.0 {
		grade = "A"
	}
	if score < 85.0 {
		grade = "B"
	}
	if score < 70.0 {
		grade = "C"
	}

	return map[string]any{
		"ok":                    true,
		"scope":                 scope,
		"pmas_100":              score,
		"grade":                 grade,
		"dangling_first_person": danglingFirstPerson,
		"role_placeholders":     rolePlaceholders,
		"self_loop_rows":        selfLoops,
		"loop_states":           loopStates,
		"connectivity":          connMetrics,
	}, nil
}

// Why expands one target's provenance (zero LLM).
//
// An assertion expands to its evidence chain (warrant / holder / verbatim
// anchor / dual times / status / conflicts / supersedes); a node expands to
// its identity closure plus the assertions that mention it.
func (s *Store) Why(targetID string, limit int) (map[string]any, error) {
	if limit == 0 {
		limit = 20
	}
	row, err := queryOneMap(s.conn, "SELECT * FROM assertion WHERE node_id = ?", targetID)
	if err != nil {
		return nil, err
	}
	if row != nil {
		return s.whyAssertion(row)
	}
	node, ok, err := s.Node(targetID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("target not found: %s", targetID)
	}
	scope := node.Scope
	group, err := s.SameAsClosure(scope, []string{targetID}, 128)
	if err != nil {
		return nil, err
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(group)), ",")
	args := make([]any, 0, 1+3*len(group)+1)
	args = append(args, scope)
	for _, value := range group {
		args = append(args, value)
	}
	for _, value := range group {
		args = append(args, value)
	}
	for _, value := range group {
		args = append(args, value)
	}
	limitValue := limit
	if limitValue < 1 {
		limitValue = 1
	}
	if limitValue > 200 {
		limitValue = 200
	}
	args = append(args, limitValue)
	rows, err := queryMaps(s.conn,
		`SELECT node_id FROM assertion
			 WHERE scope = ?
			   AND (subject_id IN (`+placeholders+`) OR object_id IN (`+placeholders+`) OR rel_id IN (`+placeholders+`))
			 ORDER BY t_source DESC, node_id
			 LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	seenRowIDs := map[string]bool{}
	for _, r := range rows {
		seenRowIDs[mapString(r, "node_id")] = true
	}
	// Spatial place node & Incarnation role node expansion in Why/Portrait:
	// If the target node is a spatial locus node (locus == label) or an incarnation role node (kind == 'incarnation'),
	// include assertions anchored at that locus (or its subpaths) or bound to that incarnation hierarchy.
	if node.Locus != nil && *node.Locus != "" && *node.Locus == node.Label && len(rows) < limitValue {
		spLocus := *node.Locus
		spRows, spErr := queryMaps(s.conn,
			`SELECT node_id FROM assertion
				 WHERE scope = ? AND (locus = ? OR locus LIKE ? ESCAPE '\')
				 ORDER BY t_source DESC, node_id
				 LIMIT ?`, scope, spLocus, ids.EscapeLike(spLocus)+"/%", limitValue)
		if spErr == nil {
			for _, sr := range spRows {
				nid := mapString(sr, "node_id")
				if nid != "" && !seenRowIDs[nid] && len(rows) < limitValue {
					seenRowIDs[nid] = true
					rows = append(rows, sr)
				}
			}
		}
	}
	if node.Kind == "incarnation" && node.Label != "" && len(rows) < limitValue {
		incRows, incErr := queryMaps(s.conn,
			`SELECT a.node_id FROM assertion a
				 JOIN node_ext ne ON ne.node_id = a.node_id AND ne.scope = a.scope
				 JOIN node ni ON ni.id = ne.incarnation_id
				 WHERE a.scope = ? AND (ni.label = ? OR ni.label LIKE ? ESCAPE '\')
				 ORDER BY a.t_source DESC, a.node_id
				 LIMIT ?`, scope, node.Label, ids.EscapeLike(node.Label)+"/%", limitValue)
		if incErr == nil {
			for _, ir := range incRows {
				nid := mapString(ir, "node_id")
				if nid != "" && !seenRowIDs[nid] && len(rows) < limitValue {
					seenRowIDs[nid] = true
					rows = append(rows, ir)
				}
			}
		}
	}
	assertions := []map[string]any{}
	for _, item := range rows {
		summary, ok, err := s.AssertionSummary(mapString(item, "node_id"))
		if err != nil {
			return nil, err
		}
		if ok {
			assertions = append(assertions, summary)
		}
	}
	canonical, err := s.CanonicalMap(scope, []string{targetID})
	if err != nil {
		return nil, err
	}
	var mergedInto any
	if value, ok := canonical[targetID]; ok && value != targetID {
		mergedInto = value
	}
	mergedRows, err := queryMaps(s.conn,
		`SELECT rhs_id FROM meta_edge
		 WHERE scope = ? AND meta_kind = 'same-as' AND status = 'active' AND lhs_id = ?
		 ORDER BY id`, scope, targetID)
	if err != nil {
		return nil, err
	}
	mergedFrom := make([]string, 0, len(mergedRows))
	for _, item := range mergedRows {
		mergedFrom = append(mergedFrom, mapString(item, "rhs_id"))
	}
	aliases, err := s.Aliases(node.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"ok":   true,
		"kind": "node",
		"node": map[string]any{
			"id":         node.ID,
			"kind":       node.Kind,
			"label":      node.Label,
			"norm_label": node.NormLabel,
			"status":     node.Status,
			"audience":   node.Audience,
			"world":      node.World,
			"carrier":    node.Carrier,
			"locus":      node.Locus,
			"created_at": node.CreatedAt,
			"retired_at": node.RetiredAt,
			"aliases":    aliases,
		},
		"assertions": assertions,
		"same_as": map[string]any{
			"merges_to":   mergedInto,
			"merged_from": mergedFrom,
		},
	}, nil
}

func (s *Store) whyAssertion(data map[string]any) (map[string]any, error) {
	nodeID := mapString(data, "node_id")
	labelIDs := []string{}
	for _, key := range []string{"subject_id", "rel_id", "object_id", "holder_id"} {
		if value := mapString(data, key); value != "" {
			labelIDs = append(labelIDs, value)
		}
	}
	labels, err := s.NodeLabels(labelIDs)
	if err != nil {
		return nil, err
	}
	subject := labels[mapString(data, "subject_id")]
	if subject == "" {
		subject = mapString(data, "subject_literal")
	}
	relation := labels[mapString(data, "rel_id")]
	object := labels[mapString(data, "object_id")]
	if object == "" {
		object = mapString(data, "object_literal")
	}
	trace := Trace{}
	if hash := mapString(data, "anchor_trace_hash"); hash != "" {
		if found, ok, err := s.Trace(hash); err != nil {
			return nil, err
		} else if ok {
			trace = found
		}
	}
	text := trace.Text
	start := int(mapInt64(data, "anchor_start"))
	end := int(mapInt64(data, "anchor_end"))
	anchorText := ""
	if start >= 0 && start < end && end <= runeCount(text) {
		anchorText = sliceRunes(text, start, end)
	}

	conflicts := []map[string]any{}
	conflictRows, err := queryMaps(s.conn,
		`SELECT id, lhs_id, rhs_id, status FROM meta_edge
		 WHERE meta_kind = 'conflicts-with' AND (lhs_id = ? OR rhs_id = ?)
		 ORDER BY id`, nodeID, nodeID)
	if err != nil {
		return nil, err
	}
	for _, edge := range conflictRows {
		otherID := mapString(edge, "rhs_id")
		if mapString(edge, "lhs_id") != nodeID {
			otherID = mapString(edge, "lhs_id")
		}
		other, _, err := s.AssertionSummary(otherID)
		if err != nil {
			return nil, err
		}
		conflicts = append(conflicts, map[string]any{
			"edge_id": mapInt64(edge, "id"),
			"status":  mapString(edge, "status"),
			"other":   other,
		})
	}

	supersededBy := []map[string]any{}
	supersededRows, err := queryMaps(s.conn,
		`SELECT lhs_id FROM meta_edge
		 WHERE meta_kind = 'supersedes' AND status = 'active' AND rhs_id = ?
		 ORDER BY id`, nodeID)
	if err != nil {
		return nil, err
	}
	for _, edge := range supersededRows {
		summary, ok, err := s.AssertionSummary(mapString(edge, "lhs_id"))
		if err != nil {
			return nil, err
		}
		if ok {
			supersededBy = append(supersededBy, summary)
		}
	}
	supersedes := []map[string]any{}
	supersedeRows, err := queryMaps(s.conn,
		`SELECT rhs_id FROM meta_edge
		 WHERE meta_kind = 'supersedes' AND status = 'active' AND lhs_id = ?
		 ORDER BY id`, nodeID)
	if err != nil {
		return nil, err
	}
	for _, edge := range supersedeRows {
		summary, ok, err := s.AssertionSummary(mapString(edge, "rhs_id"))
		if err != nil {
			return nil, err
		}
		if ok {
			supersedes = append(supersedes, summary)
		}
	}

	holderID := mapString(data, "holder_id")
	var holderLabel any
	if holderID != "" {
		if label, ok := labels[holderID]; ok {
			holderLabel = label
		}
	}
	var holderIDValue any
	if holderID != "" {
		holderIDValue = holderID
	}
	var traceHash, traceSession, traceTurn, traceAuthor, traceAudience any
	if trace.Hash != "" {
		traceHash = trace.Hash
		traceSession = trace.Session
		traceTurn = trace.Turn
		traceAuthor = trace.Author
		traceAudience = trace.Audience
	}
	return map[string]any{
		"ok":       true,
		"kind":     "assertion",
		"id":       nodeID,
		"rendered": ids.RenderAssertion(subject, relation, object),
		"s":        subject,
		"x":        relation,
		"o":        object,
		"warrant":  mapAny(data, "warrant"),
		"holder": map[string]any{
			"id":    holderIDValue,
			"label": holderLabel,
		},
		"confidence":     pyRound(mapFloat(data, "confidence"), 3),
		"status":         mapAny(data, "status"),
		"audience":       mapAny(data, "audience"),
		"bare":           mapBool(data, "bare"),
		"temporal_class": mapAny(data, "temporal_class"),
		"t_valid_from":   mapInt64(data, "t_valid_from"),
		"t_valid_to":     mapInt64(data, "t_valid_to"),
		"t_record":       mapInt64(data, "t_record"),
		"t_source":       mapInt64(data, "t_source"),
		"world":          mapAny(data, "world"),
		"carrier":        mapAny(data, "carrier"),
		"locus":          mapAny(data, "locus"),
		"loop_state":     mapAny(data, "loop_state"),
		"falsifier":      mapAny(data, "falsifier"),
		"anchor":         anchorRef(mapString(data, "anchor_trace_hash"), int64(start), int64(end)),
		"anchor_trace":   mapString(data, "anchor_trace_hash"),
		"anchor_span":    []int{start, end},
		"anchor_text":    anchorText,
		"trace": map[string]any{
			"hash":        traceHash,
			"session":     traceSession,
			"turn":        traceTurn,
			"author":      traceAuthor,
			"audience":    traceAudience,
			"occurred_at": trace.OccurredAt,
			"recorded_at": trace.RecordedAt,
		},
		"conflicts":     conflicts,
		"superseded_by": supersededBy,
		"supersedes":    supersedes,
		"created_at":    mapAny(data, "created_at"),
		"created_by":    nil,
	}, nil
}

// Portrait is the single-node reshape of the recall delivery.
func (s *Store) Portrait(nodeID string, limit int) (map[string]any, error) {
	if limit == 0 {
		limit = 50
	}
	detail, err := s.Why(nodeID, limit)
	if err != nil {
		return nil, err
	}
	if mapString(detail, "kind") != "node" {
		return nil, fmt.Errorf("portrait expects a node id (n_...); use why for assertions")
	}
	assertions := []map[string]any{}
	if items, ok := detail["assertions"].([]map[string]any); ok {
		assertions = items
	}
	stamps := []int64{}
	for _, item := range assertions {
		value := mapInt64(item, "t_valid_from")
		if value == 0 {
			value = mapInt64(item, "t_source")
		}
		if value != 0 {
			stamps = append(stamps, value)
		}
	}
	span := map[string]any{"earliest": 0, "latest": 0, "label": "时间未知"}
	if len(stamps) > 0 {
		earliest := stamps[0]
		latest := stamps[0]
		for _, value := range stamps[1:] {
			if value < earliest {
				earliest = value
			}
			if value > latest {
				latest = value
			}
		}
		label := ids.FormatTime(earliest, "unknown")
		if earliest != latest {
			label = ids.FormatTime(earliest, "unknown") + " ~ " + ids.FormatTime(latest, "unknown")
		}
		span = map[string]any{"earliest": earliest, "latest": latest, "label": label}
	}
	noTime := 0
	for _, item := range assertions {
		if mapInt64(item, "t_valid_from") == 0 {
			noTime++
		}
	}
	return map[string]any{
		"ok":         true,
		"kind":       "portrait",
		"node":       detail["node"],
		"same_as":    detail["same_as"],
		"assertions": assertions,
		"span":       span,
		"gaps": map[string]any{
			"no_time":    noTime,
			"assertions": len(assertions),
		},
	}, nil
}

// Changed is the version sequence for one node's subject assertions.
func (s *Store) Changed(nodeID string, limit int) (map[string]any, error) {
	if limit == 0 {
		limit = 50
	}
	node, ok, err := s.Node(nodeID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("node not found: %s", nodeID)
	}
	limitValue := limit
	if limitValue < 1 {
		limitValue = 1
	}
	if limitValue > 200 {
		limitValue = 200
	}
	rows, err := queryMaps(s.conn,
		`SELECT node_id FROM assertion
		 WHERE scope = ? AND subject_id = ?
		 ORDER BY (CASE WHEN t_valid_from > 0 THEN t_valid_from ELSE t_source END) ASC, node_id
		 LIMIT ?`, node.Scope, nodeID, limitValue)
	if err != nil {
		return nil, err
	}
	values := make([]string, 0, len(rows))
	for _, row := range rows {
		values = append(values, mapString(row, "node_id"))
	}
	idSet := map[string]struct{}{}
	for _, value := range values {
		idSet[value] = struct{}{}
	}
	supersedeMap := map[string]string{}
	conflictCounts := map[string]int{}
	if len(values) > 0 {
		placeholders := strings.TrimRight(strings.Repeat("?,", len(values)), ",")
		args := make([]any, 0, len(values))
		for _, value := range values {
			args = append(args, value)
		}
		edgeRows, err := queryMaps(s.conn,
			`SELECT lhs_id, rhs_id FROM meta_edge
			 WHERE meta_kind = 'supersedes' AND status = 'active' AND rhs_id IN (`+placeholders+`)`,
			args...)
		if err != nil {
			return nil, err
		}
		for _, edge := range edgeRows {
			supersedeMap[mapString(edge, "rhs_id")] = mapString(edge, "lhs_id")
		}
		doubleArgs := make([]any, 0, 2*len(values))
		for _, value := range values {
			doubleArgs = append(doubleArgs, value)
		}
		for _, value := range values {
			doubleArgs = append(doubleArgs, value)
		}
		conflictRows, err := queryMaps(s.conn,
			`SELECT lhs_id, rhs_id FROM meta_edge
			 WHERE meta_kind = 'conflicts-with' AND status = 'proposed'
			   AND (lhs_id IN (`+placeholders+`) OR rhs_id IN (`+placeholders+`))`,
			doubleArgs...)
		if err != nil {
			return nil, err
		}
		for _, edge := range conflictRows {
			for _, key := range []string{mapString(edge, "lhs_id"), mapString(edge, "rhs_id")} {
				if _, ok := idSet[key]; ok {
					conflictCounts[key]++
				}
			}
		}
	}
	sequence := []map[string]any{}
	for _, itemID := range values {
		summary, ok, err := s.AssertionSummary(itemID)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		reason := ""
		if winnerID, ok := supersedeMap[itemID]; ok {
			winner, winnerOK, err := s.AssertionSummary(winnerID)
			if err != nil {
				return nil, err
			}
			reason = "superseded_by " + winnerID
			if winnerOK {
				reason += " (" + mapString(winner, "content") + ")"
			}
		} else if mapString(summary, "status") == "retracted" {
			reason = "retracted"
		}
		conflicts := conflictCounts[itemID]
		if conflicts > 0 && reason == "" {
			reason = fmt.Sprintf("conflicts proposed: %d", conflicts)
		}
		item := cloneMap(summary)
		item["reason"] = reason
		item["conflicts"] = conflicts
		sequence = append(sequence, item)
	}
	return map[string]any{
		"ok": true,
		"node": map[string]any{
			"id":     node.ID,
			"kind":   node.Kind,
			"label":  node.Label,
			"status": node.Status,
		},
		"sequence": sequence,
		"count":    len(sequence),
	}, nil
}
