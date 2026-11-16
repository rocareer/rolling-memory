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

// AppendAssertion validates, deduplicates and writes one structured memory assertion.
//
// Writes are append-only: an identical identity is skipped, a covered fact is
// reported (optionally with a single-direction time backfill), and conflicting
// objects become proposed meta edges instead of being resolved.
func (s *Store) AppendAssertion(req AppendAssertionRequest) (map[string]any, error) {
	req.Warrant = defaultIfEmpty(req.Warrant, "referential")
	req.Status = defaultIfEmpty(req.Status, "active")
	req.Audience = defaultIfEmpty(req.Audience, "shared")
	req.CreatedBy = defaultIfEmpty(req.CreatedBy, "system")
	if !containsValue(ids.Warrants, req.Warrant) {
		return nil, fmt.Errorf("invalid warrant %s", pyRepr(req.Warrant))
	}
	if !containsValue(ids.AssertionStatuses, req.Status) {
		return nil, fmt.Errorf("invalid assertion status %s", pyRepr(req.Status))
	}
	if _, ok := ids.AudienceRank[req.Audience]; !ok {
		return nil, fmt.Errorf("invalid audience %s", pyRepr(req.Audience))
	}
	hasSubject := presentID(req.SubjectID) || req.SubjectLiteral != nil
	hasObject := presentID(req.ObjectID) || req.ObjectLiteral != nil
	if !hasSubject {
		return nil, fmt.Errorf("subject is required (node or literal)")
	}
	if !hasObject {
		return nil, fmt.Errorf("object is required (node or literal)")
	}
	if req.AnchorEnd <= req.AnchorStart || req.AnchorStart < 0 {
		return nil, fmt.Errorf("invalid anchor span")
	}

	trace, found, err := s.Trace(req.AnchorTraceHash)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("anchor trace does not exist")
	}
	if int64(runeCount(trace.Text)) < int64(req.AnchorEnd) {
		return nil, fmt.Errorf("anchor span exceeds trace text")
	}
	if pyStrip(sliceRunes(trace.Text, req.AnchorStart, req.AnchorEnd)) == "" {
		return nil, fmt.Errorf("anchor span is empty")
	}
	source := trace
	if req.SourceTraceHash != nil && *req.SourceTraceHash != "" {
		source, found, err = s.Trace(*req.SourceTraceHash)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("source trace does not exist")
		}
	}

	effectiveAudience := ids.AudienceNarrower(req.Audience, trace.Audience)
	effectiveAudience = ids.AudienceNarrower(effectiveAudience, source.Audience)
	// The node walk order is subject, object, relation, holder.
	for _, nodeRef := range []*string{req.SubjectID, req.ObjectID} {
		if !presentID(nodeRef) {
			continue
		}
		nodeRow, ok, err := s.Node(*nodeRef)
		if err != nil {
			return nil, err
		}
		if ok {
			effectiveAudience = ids.AudienceNarrower(effectiveAudience, nodeRow.Audience)
		}
	}
	if relationRow, ok, err := s.Node(req.RelID); err != nil {
		return nil, err
	} else if ok {
		effectiveAudience = ids.AudienceNarrower(effectiveAudience, relationRow.Audience)
	}
	if presentID(req.HolderID) {
		if holderRow, ok, err := s.Node(*req.HolderID); err != nil {
			return nil, err
		} else if ok {
			effectiveAudience = ids.AudienceNarrower(effectiveAudience, holderRow.Audience)
		}
	}

	now := clock.Unix()
	recordTime := now
	if req.TRecord != nil {
		recordTime = *req.TRecord
	}
	// t_source follows the source trace. It used to read the
	// anchor trace's occurred_at, which ran low whenever the two differ.
	sourceTime := source.OccurredAt
	if req.TSource != nil {
		sourceTime = *req.TSource
	}
	fromTime := req.TValidFrom
	toTime := req.TValidTo
	if toTime != 0 && fromTime != 0 && toTime <= fromTime {
		return nil, fmt.Errorf("t_valid_to must be greater than t_valid_from")
	}
	temporal := "unknown"
	if fromTime != 0 {
		temporal = ids.TemporalClass(fromTime, &now)
	}
	if !containsValue(ids.TemporalClasses, temporal) {
		temporal = "unknown"
	}

	// Literals are stored verbatim: ice keeps object_literal as written and
	// folds only for keys/comparison (lower(literal)); nodes carry the same
	// split as label vs norm_label.
	var subjectLiteralValue *string
	if req.SubjectLiteral != nil {
		value := pyStrip(*req.SubjectLiteral)
		subjectLiteralValue = &value
	}
	var objectLiteralValue *string
	if req.ObjectLiteral != nil {
		value := pyStrip(*req.ObjectLiteral)
		objectLiteralValue = &value
	}

	var subjectNode Node
	if presentID(req.SubjectID) {
		subjectNode, found, err = s.Node(*req.SubjectID)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("subject node does not exist")
		}
		if subjectNode.Kind == "relation_type" {
			return nil, fmt.Errorf("a relation_type cannot be used as a subject node")
		}
	}
	var objectNode Node
	if presentID(req.ObjectID) {
		objectNode, found, err = s.Node(*req.ObjectID)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("object node does not exist")
		}
		if objectNode.Kind == "relation_type" {
			return nil, fmt.Errorf("a relation_type cannot be used as an object node")
		}
	}
	relation, found, err := s.Node(req.RelID)
	if err != nil {
		return nil, err
	}
	if !found || relation.Kind != "relation_type" {
		return nil, fmt.Errorf("relation must be an existing relation_type node")
	}
	if presentID(req.HolderID) {
		if _, ok, err := s.Node(*req.HolderID); err != nil {
			return nil, err
		} else if !ok {
			return nil, fmt.Errorf("holder node does not exist")
		}
	}

	subjectKey := ids.KeyForNodeOrLiteral(derefID(req.SubjectID), derefString(subjectLiteralValue))
	objectKey := ids.KeyForNodeOrLiteral(derefID(req.ObjectID), derefString(objectLiteralValue))
	identity := ids.AssertionHash(ids.AssertionHashParts{
		Scope:           req.Scope,
		SubjectKey:      subjectKey,
		RelID:           req.RelID,
		ObjectKey:       objectKey,
		Bare:            req.Bare,
		HolderID:        derefID(req.HolderID),
		Warrant:         req.Warrant,
		AnchorTraceHash: req.AnchorTraceHash,
		Audience:        effectiveAudience,
		World:           derefString(req.World),
		Carrier:         derefString(req.Carrier),
		Locus:           derefString(req.Locus),
	})

	existing, err := queryOneMap(s.conn, "SELECT * FROM assertion WHERE hash = ?", identity)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		existingRow := assertionFromMap(existing)
		return map[string]any{
			"action":   "skipped",
			"reason":   "idempotent",
			"id":       existingRow.NodeID,
			"hash":     identity,
			"audience": existingRow.Audience,
			"anchor":   anchorRef(req.AnchorTraceHash, int64(req.AnchorStart), int64(req.AnchorEnd)),
		}, nil
	}

	covered, err := s.findCovered(
		req.Scope, req.RelID, req.SubjectID, subjectLiteralValue,
		req.ObjectID, objectLiteralValue, req.HolderID, req.Warrant,
		req.Bare, effectiveAudience,
	)
	if err != nil {
		return nil, err
	}
	if covered != nil {
		backfilled := false
		if fromTime != 0 && covered.TValidFrom == 0 {
			if _, err := s.conn.Exec(
				"UPDATE assertion SET t_valid_from = ?, temporal_class = ? WHERE node_id = ?",
				fromTime, temporal, covered.NodeID,
			); err != nil {
				return nil, err
			}
			if _, err := s.Event(req.Scope, "assert.time_backfill", map[string]any{
				"assertion":    covered.NodeID,
				"t_valid_from": fromTime,
			}, "single-direction backfill 0 -> value", req.CreatedBy); err != nil {
				return nil, err
			}
			backfilled = true
		}
		action := "covered"
		if backfilled {
			action = "supplemented"
		}
		return map[string]any{
			"action":   action,
			"id":       covered.NodeID,
			"hash":     covered.Hash,
			"audience": covered.Audience,
			"anchor":   anchorRef(covered.AnchorTraceHash, covered.AnchorStart, covered.AnchorEnd),
		}, nil
	}

	nodeKey := ids.AssertionNodeID(identity)
	subjectLabel := derefString(subjectLiteralValue)
	if presentID(req.SubjectID) {
		subjectLabel = subjectNode.Label
	}
	objectLabel := derefString(objectLiteralValue)
	if presentID(req.ObjectID) {
		objectLabel = objectNode.Label
	}
	rendered := ids.RenderAssertion(subjectLabel, relation.Label, objectLabel)

	createdBy := req.CreatedBy
	confidence := ids.Clamp(req.Confidence, 0.0, 1.0)
	err = db.WithImmediateTx(s.conn, func(tx *sql.Tx) error {
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO node(id, scope, kind, label, norm_label, status, audience,
			                             world, carrier, locus, created_at)
			 VALUES (?, ?, 'assertion', ?, ?, 'active', ?, ?, ?, ?, ?)`,
			nodeKey, req.Scope, rendered, ids.NormalizeLabel(rendered),
			effectiveAudience, req.World, req.Carrier, req.Locus, now,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(
			`INSERT INTO assertion(
			   node_id, scope, subject_id, subject_literal, rel_id, object_id, object_literal,
			   bare, world, carrier, locus, t_valid_from, t_valid_to, t_record, t_source,
			   temporal_class, holder_id, warrant, anchor_trace_hash, anchor_start, anchor_end,
			   source_trace_hash, audience, confidence, status, loop_state, falsifier, hash, created_at
			 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			nodeKey, req.Scope, nullableID(req.SubjectID), subjectLiteralValue, req.RelID,
			nullableID(req.ObjectID), objectLiteralValue, boolInt(req.Bare),
			req.World, req.Carrier, req.Locus, fromTime, toTime, recordTime, sourceTime,
			temporal, nullableID(req.HolderID), req.Warrant, req.AnchorTraceHash,
			req.AnchorStart, req.AnchorEnd, sourceHashOr(req.SourceTraceHash, req.AnchorTraceHash),
			effectiveAudience, confidence, req.Status, req.LoopState, req.Falsifier,
			identity, now,
		); err != nil {
			return err
		}
		_, err := insertEvent(tx, req.Scope, "assert.add", map[string]any{
			"assertion": nodeKey,
			"hash":      identity,
			"subject":   subjectKey,
			"rel":       req.RelID,
			"object":    objectKey,
			"anchor":    req.AnchorTraceHash,
		}, "", createdBy, false)
		return err
	})
	if err != nil {
		return nil, err
	}

	conflicts := 0
	existingPairs := []map[string]any{}
	if !req.Bare {
		conflicts, existingPairs, err = s.recordConflicts(
			req.Scope, nodeKey, req.RelID, req.SubjectID, subjectLiteralValue,
			objectKey, req.HolderID, req.Warrant, effectiveAudience,
			fromTime, toTime, req.AnchorTraceHash,
			anchorRef(req.AnchorTraceHash, int64(req.AnchorStart), int64(req.AnchorEnd)),
			createdBy,
		)
		if err != nil {
			return nil, err
		}
	}
	return map[string]any{
		"action":            "written",
		"id":                nodeKey,
		"hash":              identity,
		"rendered":          rendered,
		"audience":          effectiveAudience,
		"audience_narrowed": effectiveAudience != req.Audience,
		"anchor":            anchorRef(req.AnchorTraceHash, int64(req.AnchorStart), int64(req.AnchorEnd)),
		"t_valid_from":      fromTime,
		"confidence":        confidence,
		"conflicts":         conflicts,
		// Pre-write echo: active values sharing (S,X) with another
		// object — receipt only, no behaviour change.
		"existing": existingPairs,
	}, nil
}

func (s *Store) findCovered(
	scope, relID string,
	subjectID *string,
	subjectLiteral *string,
	objectID *string,
	objectLiteral *string,
	holderID *string,
	warrant string,
	bare bool,
	audience string,
) (*Assertion, error) {
	statusClause := "AND status = 'active'"
	if strings.HasPrefix(reqScopeOr(scope), "mem:golden:") {
		statusClause = "AND status IN ('active', 'superseded')"
	}
	row, err := queryOneMap(s.conn,
		`SELECT * FROM assertion
		 WHERE scope = ? AND rel_id = ? AND bare = ? AND warrant = ? AND audience = ?
		   AND holder_id IS ?
		   `+statusClause+`
		   AND ((subject_id = ?) OR (subject_id IS NULL AND lower(subject_literal) = lower(?)))
		   AND ((object_id = ?) OR (object_id IS NULL AND lower(object_literal) = lower(?)))
		 ORDER BY created_at LIMIT 1`,
		scope, relID, boolInt(bare), warrant, audience,
		nullableID(holderID),
		nullableID(subjectID), nullableString(subjectLiteral),
		nullableID(objectID), nullableString(objectLiteral),
	)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	assertion := assertionFromMap(row)
	return &assertion, nil
}

// IsMultiValuedRel reports whether two same-(S, X) assertions with different objects represent
// a non-conflicting multi-valued enumeration (e.g. parallel facts extracted from the same source
// turn trace) rather than a mutually exclusive contradiction (aligned with the service engine's U19 · 0 wordlists).
func IsMultiValuedRel(sameTrace bool) bool {
	return sameTrace
}

// recordConflicts records the proposed conflicts-with edges and returns the
// count together with the pre-write echo of the conflicting active values
// (≤5 assertion summaries).
func (s *Store) recordConflicts(
	scope, newNodeID, relID string,
	subjectID *string,
	subjectLiteral *string,
	objectKey string,
	holderID *string,
	warrant, audience string,
	fromTime, toTime int64,
	anchorTraceHash string,
	evidence, createdBy string,
) (int, []map[string]any, error) {
	rows, err := queryMaps(s.conn,
		`SELECT node_id, object_id, object_literal, t_valid_from, t_valid_to, anchor_trace_hash
		 FROM assertion
		 WHERE scope = ? AND rel_id = ? AND status = 'active' AND bare = 0
		   AND holder_id IS ? AND warrant = ? AND audience = ?
		   AND ((subject_id = ?) OR (subject_id IS NULL AND lower(subject_literal) = lower(?)))
		 LIMIT 32`,
		scope, relID, nullableID(holderID), warrant, audience,
		nullableID(subjectID), nullableString(subjectLiteral),
	)
	if err != nil {
		return 0, nil, err
	}
	isGolden := strings.HasPrefix(scope, "mem:golden:")
	conflicts := 0
	existingPairs := []map[string]any{}
	for _, row := range rows {
		if mapString(row, "node_id") == newNodeID {
			continue
		}
		existingKey := ids.KeyForNodeOrLiteral(
			derefID(mapPtrString(row, "object_id")),
			mapString(row, "object_literal"),
		)
		if existingKey == objectKey {
			continue
		}
		if len(existingPairs) < 5 {
			summary, found, err := s.AssertionSummary(mapString(row, "node_id"))
			if err != nil {
				return conflicts, existingPairs, err
			}
			if found {
				existingPairs = append(existingPairs, summary)
			}
		}
		if !isGolden && anchorTraceHash != "" && IsMultiValuedRel(mapString(row, "anchor_trace_hash") == anchorTraceHash) {
			continue
		}
		if !windowsOverlap(fromTime, toTime, mapInt64(row, "t_valid_from"), mapInt64(row, "t_valid_to")) {
			continue
		}
		affected, err := execCount(s.conn,
			`INSERT OR IGNORE INTO meta_edge
			   (scope, meta_kind, lhs_id, rhs_id, status, evidence_anchor, created_at, created_by)
			 VALUES (?, 'conflicts-with', ?, ?, 'proposed', ?, ?, ?)`,
			scope, mapString(row, "node_id"), newNodeID, evidence, clock.Unix(), createdBy)
		if err != nil {
			return conflicts, existingPairs, err
		}
		if affected > 0 {
			conflicts++
		}
	}
	return conflicts, existingPairs, nil
}

// AddMention records one resolved (or unresolved) mention span.
func (s *Store) AddMention(scope, traceHash, surface string, start, end int, nodeID *string) error {
	if start < 0 || end <= start || pyStrip(surface) == "" {
		return nil
	}
	_, err := s.conn.Exec(
		`INSERT OR IGNORE INTO mention(scope, trace_hash, span_start, span_end, surface, node_id, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		scope, traceHash, start, end, pyStrip(surface), nodeID, clock.Unix(),
	)
	return err
}

func windowsOverlap(aFrom, aTo, bFrom, bTo int64) bool {
	if aTo == 0 && bTo == 0 {
		return true
	}
	if aFrom == 0 || bFrom == 0 {
		return true
	}
	aEnd := aTo
	if aEnd == 0 {
		aEnd = 1_000_000_000_000
	}
	bEnd := bTo
	if bEnd == 0 {
		bEnd = 1_000_000_000_000
	}
	return aFrom < bEnd && bFrom < aEnd
}

func presentID(value *string) bool {
	return value != nil && *value != ""
}

func reqScopeOr(scope string) string {
	return scope
}

func derefID(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func nullableID(value *string) any {
	if value == nil || *value == "" {
		return nil
	}
	return *value
}

func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func sourceHashOr(value *string, fallback string) string {
	if value == nil || *value == "" {
		return fallback
	}
	return *value
}
