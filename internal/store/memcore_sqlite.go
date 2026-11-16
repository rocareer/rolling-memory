// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rocareer/rolling-memory/internal/db"
	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/memcore"
)

// SQLiteBackend implements memcore.StoreBackend on top of the local SQLite Store.
type SQLiteBackend struct {
	store *Store
}

// MemcoreBackend returns the 1:1 memcore.StoreBackend adapter for this SQLite Store.
func (s *Store) MemcoreBackend() *SQLiteBackend {
	_ = db.EnsureMemcoreSchema(s.conn)
	return &SQLiteBackend{store: s}
}

// Dialect returns "sqlite".
func (b *SQLiteBackend) Dialect() string { return "sqlite" }

// EnsureNode finds or creates a node by (scope, kind, label).
func (b *SQLiteBackend) EnsureNode(_ context.Context, scope, kind, label, audience, world string, now int64) (*memcore.Node, bool, error) {
	if existing, found, err := b.store.NodeByLabel(scope, label, kind); err != nil {
		return nil, false, err
	} else if found {
		return toMemcoreNode(existing), false, nil
	}
	var wPtr *string
	if strings.TrimSpace(world) != "" {
		w := strings.TrimSpace(world)
		wPtr = &w
	}
	id, err := b.store.EnsureNode(scope, kind, label, NodeOptions{
		Audience: audience,
		World:    wPtr,
	})
	if err != nil {
		return nil, false, err
	}
	nodeRow, _, err := b.store.Node(id)
	if err != nil {
		return nil, false, err
	}
	return toMemcoreNode(nodeRow), true, nil
}

// FindNodeByName resolves a referent node by exact case-insensitive label or registered alias.
func (b *SQLiteBackend) FindNodeByName(_ context.Context, scope, name string) (*memcore.Node, bool, error) {
	n, found, err := b.store.NodeByLabel(scope, name, "referent")
	if err != nil || !found {
		return nil, found, err
	}
	return toMemcoreNode(n), true, nil
}

// BindAlias registers an alias for a node if not already bound to another live node in the same scope.
func (b *SQLiteBackend) BindAlias(_ context.Context, scope, nodeID, alias, source string, now int64) (bool, error) {
	clean := strings.TrimSpace(alias)
	if clean == "" || nodeID == "" {
		return false, nil
	}
	norm := ids.NormalizeLabel(clean)
	// RA09 uniqueness guard: check if norm_alias is already bound to a different live node in scope.
	existingRow, err := queryOneMap(b.store.conn,
		`SELECT a.node_id FROM node_alias a JOIN node n ON n.id = a.node_id
		 WHERE n.scope = ? AND a.norm_alias = ? AND n.status IN ('provisional','active') LIMIT 1`,
		scope, norm)
	if err != nil {
		return false, err
	}
	if existingRow != nil {
		boundID := mapString(existingRow, "node_id")
		if boundID != "" && boundID != nodeID {
			return false, nil
		}
		if boundID == nodeID {
			return false, nil
		}
	}
	src := strings.TrimSpace(source)
	if src == "" {
		src = "remember"
	}
	err = db.WithImmediateTx(b.store.conn, func(tx *sql.Tx) error {
		_, e := tx.Exec(
			`INSERT OR IGNORE INTO node_alias(node_id, alias, norm_alias, source, created_at) VALUES (?, ?, ?, ?, ?)`,
			nodeID, clean, norm, src, now)
		return e
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

// AppendTrace persists an L0 verbatim evidence trace idempotently.
func (b *SQLiteBackend) AppendTrace(_ context.Context, t memcore.Trace) (*memcore.Trace, bool, error) {
	occ := t.OccurredAt
	hash, inserted, err := b.store.AddTrace(t.Scope, t.Text, TraceOptions{
		Session:    t.Session,
		Turn:       t.Turn,
		Kind:       t.Kind,
		Author:     t.Author,
		Audience:   t.Audience,
		OccurredAt: &occ,
	})
	if err != nil {
		return nil, false, err
	}
	t.Hash = hash
	return &t, inserted, nil
}

// AppendAssertion persists a structured memory assertion row (plus 1:1 assertion_ext metadata) idempotently.
func (b *SQLiteBackend) AppendAssertion(_ context.Context, a memcore.Assertion, assertionLabel string) (*memcore.Assertion, bool, error) {
	now := a.CreatedAt
	if now <= 0 {
		now = ids.UTCNow()
	}
	hash := strings.TrimSpace(a.Hash)
	if len(hash) < 16 {
		hash = memcore.AssertionDigestSHA256(
			a.Scope, a.SubjectID, a.SubjectLiteral, a.RelID, a.ObjectID, a.ObjectLiteral,
			a.HolderID, a.Warrant, a.AnchorTraceHash, a.SubjectGap, a.ObjectGap, a.HolderGap,
		)
	}
	existing, err := queryOneMap(b.store.conn, `SELECT node_id FROM assertion WHERE hash = ? LIMIT 1`, hash)
	if err != nil {
		return nil, false, err
	}
	if existing != nil {
		a.NodeID = mapString(existing, "node_id")
		a.NumNodeID = memcore.HashStringIDToUint64(a.NodeID)
		a.Hash = hash
		return &a, false, nil
	}

	nodeID := "a_" + hash[:16]
	label := strings.TrimSpace(assertionLabel)
	if label == "" {
		label = memcore.FormatAssertionLabel(a.SubjectID, a.RelID, a.ObjectID, a.SubjectAbsent, a.SubjectGap, a.ObjectGap)
	}
	normLabel := ids.NormalizeLabel(label)
	audience := defaultIfEmpty(a.Audience, "shared")
	warrant := defaultIfEmpty(a.Warrant, "referential")
	status := defaultIfEmpty(a.Status, "active")
	loopState := memcore.InitialLoopState(warrant, a.LoopState)
	a.LoopState = loopState

	// Map canonical temporal_class ("dated"/"atemporal"/"unknown") to schema.sql v1 CHECK ("past"/"present"/"future"/"unknown").
	dbTemporalClass := "unknown"
	switch strings.ToLower(strings.TrimSpace(a.TemporalClass)) {
	case "past", "present", "future", "unknown":
		dbTemporalClass = strings.ToLower(strings.TrimSpace(a.TemporalClass))
	case "dated":
		dbTemporalClass = "past"
	case "atemporal":
		dbTemporalClass = "present"
	}

	var subjIDPtr, subjLitPtr, objIDPtr, objLitPtr, holderIDPtr *string
	if a.SubjectID != "" {
		s := a.SubjectID
		subjIDPtr = &s
	} else {
		lit := a.SubjectLiteral
		subjLitPtr = &lit
	}
	if a.ObjectID != "" {
		o := a.ObjectID
		objIDPtr = &o
	} else {
		lit := a.ObjectLiteral
		objLitPtr = &lit
	}
	if a.HolderID != "" {
		h := a.HolderID
		holderIDPtr = &h
	}
	bareInt := 0
	if a.Bare {
		bareInt = 1
	}
	subjAbsentInt := 0
	if a.SubjectAbsent {
		subjAbsentInt = 1
	}
	conf := a.Confidence
	if conf <= 0 || conf > 1 {
		conf = 0.7
	}
	srcTrace := defaultIfEmpty(a.SourceTraceHash, a.AnchorTraceHash)
	anchorStart := a.AnchorStart
	anchorEnd := a.AnchorEnd
	if anchorEnd <= anchorStart {
		anchorStart = 0
		anchorEnd = 1
	}

	err = db.WithImmediateTx(b.store.conn, func(tx *sql.Tx) error {
		if _, e := tx.Exec(
			`INSERT OR IGNORE INTO node(id, scope, kind, label, norm_label, status, audience, world, carrier, locus, created_at)
			 VALUES (?, ?, 'assertion', ?, ?, 'active', ?, ?, ?, ?, ?)`,
			nodeID, a.Scope, label, normLabel, audience, nullIfEmpty(a.World), nullIfEmpty(a.Carrier), nullIfEmpty(a.Locus), now,
		); e != nil {
			return e
		}
		if _, e := tx.Exec(
			`INSERT INTO assertion(
				node_id, scope, subject_id, subject_literal, rel_id, object_id, object_literal,
				bare, world, carrier, locus, t_valid_from, t_valid_to, t_record, t_source,
				temporal_class, holder_id, warrant, anchor_trace_hash, anchor_start, anchor_end,
				source_trace_hash, audience, confidence, status, loop_state, falsifier, hash, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			nodeID, a.Scope, subjIDPtr, subjLitPtr, a.RelID, objIDPtr, objLitPtr,
			bareInt, nullIfEmpty(a.World), nullIfEmpty(a.Carrier), nullIfEmpty(a.Locus),
			a.TValidFrom, a.TValidTo, defaultInt64(a.TRecord, now), defaultInt64(a.TSource, now),
			dbTemporalClass, holderIDPtr, warrant, a.AnchorTraceHash, anchorStart, anchorEnd,
			srcTrace, audience, conf, status, nullIfEmpty(a.LoopState), nullIfEmpty(a.Falsifier), hash, now,
		); e != nil {
			return e
		}
		_, e := tx.Exec(
			`INSERT OR REPLACE INTO assertion_ext(
						node_id, scope, subject_absent, subject_gap, object_gap, holder_gap,
						audience_subject, t_event, event_src, read_hits, last_read_at
					) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			nodeID, a.Scope, subjAbsentInt,
			memcore.NormalizeGap(a.SubjectGap), memcore.NormalizeGap(a.ObjectGap), memcore.NormalizeGap(a.HolderGap),
			strings.TrimSpace(a.AudienceSubject), a.TEvent, strings.TrimSpace(a.EventSrc), a.ReadHits, a.LastReadAt,
		)
		return e
	})
	if err != nil {
		return nil, false, err
	}
	a.NodeID = nodeID
	a.NumNodeID = memcore.HashStringIDToUint64(nodeID)
	a.Hash = hash
	return &a, true, nil
}

// ClosePriorAssertions marks older active assertions of the same (scope, subject_id, rel_id) with a different value
// as superseded and sets t_valid_to = validTo, recording supersedes meta_edges.
func (b *SQLiteBackend) ClosePriorAssertions(_ context.Context, scope, newAssertionID, subjectID, relID, objectID, objectLiteral string, validTo, now int64) ([]string, error) {
	if subjectID == "" || relID == "" {
		return nil, nil
	}
	rows, err := queryMaps(b.store.conn,
		`SELECT node_id, object_id, object_literal FROM assertion
		 WHERE scope = ? AND subject_id = ? AND rel_id = ? AND status = 'active' AND node_id <> ?
		 ORDER BY created_at ASC`,
		scope, subjectID, relID, newAssertionID)
	if err != nil {
		return nil, err
	}
	var toClose []string
	for _, r := range rows {
		nid := mapString(r, "node_id")
		oid := mapString(r, "object_id")
		olit := mapString(r, "object_literal")
		sameVal := (objectID != "" && oid == objectID) ||
			(objectID == "" && oid == "" && strings.TrimSpace(olit) == strings.TrimSpace(objectLiteral))
		if !sameVal && nid != "" {
			toClose = append(toClose, nid)
		}
	}
	if len(toClose) == 0 {
		return nil, nil
	}
	err = db.WithImmediateTx(b.store.conn, func(tx *sql.Tx) error {
		for _, oldID := range toClose {
			if _, e := tx.Exec(
				`UPDATE assertion SET status = 'superseded',
					 t_valid_to = CASE WHEN t_valid_to = 0 AND ? > 0 THEN ? ELSE t_valid_to END,
					 loop_state = CASE WHEN loop_state = 'open' THEN 'fulfilled' ELSE loop_state END
					 WHERE node_id = ?`,
				validTo, validTo, oldID,
			); e != nil {
				return e
			}
			if newAssertionID != "" {
				if _, e := tx.Exec(
					`INSERT OR IGNORE INTO meta_edge(scope, meta_kind, lhs_id, rhs_id, status, created_at, created_by)
						 VALUES (?, 'supersedes', ?, ?, 'active', ?, 'closes_prior')`,
					scope, newAssertionID, oldID, now,
				); e != nil {
					return e
				}
				_, _ = tx.Exec(
					`UPDATE meta_edge SET status = 'reverted'
						 WHERE scope = ? AND meta_kind = 'supersedes' AND status = 'active'
						   AND lhs_id = ? AND rhs_id = ?`,
					scope, oldID, newAssertionID,
				)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return toClose, nil
}

// RecordRestate appends an occurrence or mention entry into the restate ledger.
func (b *SQLiteBackend) RecordRestate(_ context.Context, r memcore.RestateRow) error {
	kind := strings.ToLower(strings.TrimSpace(r.Kind))
	if kind != "occurrence" {
		kind = "mention"
	}
	now := defaultInt64(r.CreatedAt, ids.UTCNow())
	occ := defaultInt64(r.OccurredAt, now)
	return db.WithImmediateTx(b.store.conn, func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO restate(scope, assertion_node_id, trace_hash, anchor_start, anchor_end, kind, occurred_at, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			r.Scope, r.AssertionNodeID, r.TraceHash, r.AnchorStart, r.AnchorEnd, kind, occ, now,
		)
		return err
	})
}

// RecordMemoryHot appends a recall or experience entry into the two-zone working hot memory ledger.
func (b *SQLiteBackend) RecordMemoryHot(_ context.Context, h memcore.MemoryHot) error {
	kind := strings.ToLower(strings.TrimSpace(h.Kind))
	if kind != "experience" {
		kind = "recall"
	}
	sess := defaultIfEmpty(h.SessionKey, "default")
	now := defaultInt64(h.CreatedAt, ids.UTCNow())
	nodesJSON, _ := json.Marshal(h.HitNodeIDs)
	assertsJSON, _ := json.Marshal(h.HitAssertionIDs)
	return db.WithImmediateTx(b.store.conn, func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO memory_hot(scope, session_key, kind, query_text, trace_hash, hit_node_ids_json, hit_assertion_ids_json, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			h.Scope, sess, kind, h.QueryText, h.TraceHash, string(nodesJSON), string(assertsJSON), now,
		)
		return err
	})
}

// RecentMemoryHot retrieves the most recent hot memory entries for a scope (and optional sessionKey).
func (b *SQLiteBackend) RecentMemoryHot(_ context.Context, scope, sessionKey string, limit int) ([]memcore.MemoryHot, error) {
	if limit <= 0 {
		limit = 10
	}
	var rows []map[string]any
	var err error
	if strings.TrimSpace(sessionKey) != "" {
		rows, err = queryMaps(b.store.conn,
			`SELECT * FROM memory_hot WHERE scope = ? AND session_key = ? ORDER BY created_at DESC, id DESC LIMIT ?`,
			scope, sessionKey, limit)
	} else {
		rows, err = queryMaps(b.store.conn,
			`SELECT * FROM memory_hot WHERE scope = ? ORDER BY created_at DESC, id DESC LIMIT ?`,
			scope, limit)
	}
	if err != nil {
		return nil, err
	}
	out := make([]memcore.MemoryHot, 0, len(rows))
	for _, r := range rows {
		var nodeIDs, assertIDs []string
		_ = json.Unmarshal([]byte(mapString(r, "hit_node_ids_json")), &nodeIDs)
		_ = json.Unmarshal([]byte(mapString(r, "hit_assertion_ids_json")), &assertIDs)
		out = append(out, memcore.MemoryHot{
			ID:              mapInt64(r, "id"),
			Scope:           mapString(r, "scope"),
			SessionKey:      mapString(r, "session_key"),
			Kind:            mapString(r, "kind"),
			QueryText:       mapString(r, "query_text"),
			TraceHash:       mapString(r, "trace_hash"),
			HitNodeIDs:      nodeIDs,
			HitAssertionIDs: assertIDs,
			CreatedAt:       mapInt64(r, "created_at"),
		})
	}
	return out, nil
}

// UpsertEpisodeContainer creates or updates an episode universe container and its members.
func (b *SQLiteBackend) UpsertEpisodeContainer(_ context.Context, c memcore.EpisodeContainer, members []memcore.EpisodeMember) error {
	now := defaultInt64(c.UpdatedAt, ids.UTCNow())
	gen := c.Generation
	if gen <= 0 {
		gen = 1
	}
	lvl := c.Level
	if lvl <= 0 {
		lvl = 1
	}
	world := defaultIfEmpty(c.World, "actual")
	carrier := defaultIfEmpty(c.Carrier, "virtual")
	return db.WithImmediateTx(b.store.conn, func(tx *sql.Tx) error {
		if _, err := tx.Exec(
			`INSERT INTO episode(scope, ep_key, generation, level, world, carrier, locus, session_key, t_start, t_end, t_source_min, t_source_max, member_count, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(ep_key) DO UPDATE SET
			   t_start = excluded.t_start,
			   t_end = excluded.t_end,
			   t_source_min = excluded.t_source_min,
			   t_source_max = excluded.t_source_max,
			   member_count = excluded.member_count,
			   updated_at = excluded.updated_at`,
			c.Scope, c.EpKey, gen, lvl, world, carrier, c.Locus, c.SessionKey,
			c.TStart, c.TEnd, c.TSourceMin, c.TSourceMax, c.MemberCount, now,
		); err != nil {
			return err
		}
		var epID int64
		if err := tx.QueryRow(`SELECT id FROM episode WHERE ep_key = ?`, c.EpKey).Scan(&epID); err != nil {
			return err
		}
		for _, m := range members {
			mk := defaultIfEmpty(m.MemberKind, "assertion")
			facet := defaultIfEmpty(m.Facet, "event")
			mCreated := defaultInt64(m.CreatedAt, now)
			if _, err := tx.Exec(
				`INSERT OR REPLACE INTO episode_member(episode_id, scope, member_id, member_kind, facet, created_at)
				 VALUES (?, ?, ?, ?, ?, ?)`,
				epID, defaultIfEmpty(m.Scope, c.Scope), m.MemberID, mk, facet, mCreated,
			); err != nil {
				return err
			}
		}
		_, err := tx.Exec(
			`UPDATE episode SET member_count = (SELECT count(*) FROM episode_member WHERE episode_id = ?), updated_at = ? WHERE id = ?`,
			epID, now, epID,
		)
		return err
	})
}

// ListEpisodeContainers lists episode containers for a scope.
func (b *SQLiteBackend) ListEpisodeContainers(_ context.Context, scope string, level int, limit int) ([]memcore.EpisodeContainer, error) {
	if limit <= 0 {
		limit = 50
	}
	var rows []map[string]any
	var err error
	if level > 0 {
		rows, err = queryMaps(b.store.conn,
			`SELECT * FROM episode WHERE scope = ? AND level = ? ORDER BY updated_at DESC, id DESC LIMIT ?`,
			scope, level, limit)
	} else {
		rows, err = queryMaps(b.store.conn,
			`SELECT * FROM episode WHERE scope = ? ORDER BY updated_at DESC, id DESC LIMIT ?`,
			scope, limit)
	}
	if err != nil {
		return nil, err
	}
	out := make([]memcore.EpisodeContainer, 0, len(rows))
	for _, r := range rows {
		out = append(out, memcore.EpisodeContainer{
			ID:          mapInt64(r, "id"),
			Scope:       mapString(r, "scope"),
			EpKey:       mapString(r, "ep_key"),
			Generation:  int(mapInt64(r, "generation")),
			Level:       int(mapInt64(r, "level")),
			World:       mapString(r, "world"),
			Carrier:     mapString(r, "carrier"),
			Locus:       mapString(r, "locus"),
			SessionKey:  mapString(r, "session_key"),
			TStart:      mapInt64(r, "t_start"),
			TEnd:        mapInt64(r, "t_end"),
			TSourceMin:  mapInt64(r, "t_source_min"),
			TSourceMax:  mapInt64(r, "t_source_max"),
			MemberCount: int(mapInt64(r, "member_count")),
			UpdatedAt:   mapInt64(r, "updated_at"),
		})
	}
	return out, nil
}

// PartText holds one parsed sub-part or block artifact linked to a trace.
type PartText struct {
	Seq    int
	Kind   string
	Ref    string
	Parser string
	Text   string
}

// PartTextsOfBlockParents loads block-kind traces (tool_result / thinking / tool_use / code block)
// whose parent_locator (in trace.meta_json) points to one of the given unit trace hashes' locators
// (session#turn), and maps those block parts back to the corresponding unit trace hash
// (aligned with the service engine's repository/memory/source_part_locate.go::PartTextsOfBlockParents · e2aef8546).
func (s *Store) PartTextsOfBlockParents(scope string, unitHashes []string) (map[string][]PartText, error) {
	out := map[string][]PartText{}
	if s == nil || s.conn == nil || len(unitHashes) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(unitHashes))
	args := make([]any, 0, len(unitHashes)+1)
	args = append(args, scope)
	for i, h := range unitHashes {
		placeholders[i] = "?"
		args = append(args, h)
	}
	unitRows, err := queryMaps(s.conn,
		`SELECT hash, session, turn FROM trace WHERE scope = ? AND hash IN (`+strings.Join(placeholders, ",")+`)`,
		args...)
	if err != nil || len(unitRows) == 0 {
		return out, err
	}
	locToUnit := make(map[string]string, len(unitRows))
	for _, r := range unitRows {
		h := mapString(r, "hash")
		sess := mapString(r, "session")
		turn := r["turn"]
		if h != "" && sess != "" && turn != nil {
			locToUnit[fmt.Sprintf("%s#%v", sess, turn)] = h
		}
	}
	if len(locToUnit) == 0 {
		return out, nil
	}
	blockRows, err := queryMaps(s.conn,
		`SELECT hash, text, meta_json FROM trace WHERE scope = ? AND meta_json LIKE '%parent_locator%'`,
		scope)
	if err != nil {
		return out, err
	}
	for idx, br := range blockRows {
		txt := strings.TrimSpace(mapString(br, "text"))
		metaStr := mapString(br, "meta_json")
		if txt == "" || metaStr == "" {
			continue
		}
		var meta map[string]any
		if json.Unmarshal([]byte(metaStr), &meta) != nil {
			continue
		}
		if kind, _ := meta["kind"].(string); kind != "" && kind != "block" {
			continue
		}
		parentLoc, _ := meta["parent_locator"].(string)
		unitHash, ok := locToUnit[strings.TrimSpace(parentLoc)]
		if !ok {
			continue
		}
		partKind, _ := meta["part_kind"].(string)
		if partKind == "" {
			partKind = "file"
		}
		ref, _ := meta["ref"].(string)
		parser, _ := meta["parser"].(string)
		if parser == "" {
			parser = "virtual"
		}
		out[unitHash] = append(out[unitHash], PartText{
			Seq:    idx,
			Kind:   partKind,
			Ref:    ref,
			Parser: parser,
			Text:   txt,
		})
	}
	return out, nil
}

func toMemcoreNode(n Node) *memcore.Node {
	var world, carrier, locus string
	if n.World != nil {
		world = *n.World
	}
	if n.Carrier != nil {
		carrier = *n.Carrier
	}
	if n.Locus != nil {
		locus = *n.Locus
	}
	return &memcore.Node{
		ID:        n.ID,
		NumID:     memcore.HashStringIDToUint64(n.ID),
		Scope:     n.Scope,
		Kind:      n.Kind,
		Label:     n.Label,
		NormLabel: n.NormLabel,
		Status:    n.Status,
		Audience:  n.Audience,
		World:     world,
		Carrier:   carrier,
		Locus:     locus,
		CreatedAt: n.CreatedAt,
		RetiredAt: n.RetiredAt,
	}
}

func nullIfEmpty(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.TrimSpace(s)
}

func defaultInt64(v, fallback int64) int64 {
	if v > 0 {
		return v
	}
	return fallback
}
