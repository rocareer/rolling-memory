// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package store implements the SQLite-backed graph store.
//
// The store is deliberately explicit about three invariants:
//
//  1. Every assertion has a verbatim anchor into a trace row.
//  2. Writes are append-only; the only in-place mutations are status bits and
//     missing-time backfill (single direction: 0 -> value).
//  3. A covered fact is not written twice. Conflicts are reported as
//     meta_edge(conflicts-with, proposed), never silently resolved.
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rocareer/rolling-memory/internal/clock"
	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/db"
	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/memcore"
)

// Seed is one nominated node in the lexical/vector seed leg.
//
// Increment marks an orthogonal vector-seed increment (Pro ⊇ Zero): a node the
// vector leg nominated that the lexical legs did not. Such a seed rides on top
// of the lexical trunk instead of competing for its Limit slots, so the read
// side keeps the trunk identical to the `zero` profile and adds the increment
// strictly after it.
type Seed struct {
	NodeID    string
	Label     string
	Score     float64
	Legs      map[string]struct{}
	Increment bool
}

// Candidate is one scored assertion row with its join columns.
type Candidate struct {
	Data        CandidateRow
	Score       float64
	LexScore    float64
	RerankScore float64
}

// VectorHit is one name-vector similarity hit.
type VectorHit struct {
	NodeID     string
	Label      string
	Similarity float64
}

// Window is an inclusive-start/exclusive-end time boost window.
type Window struct {
	Start int64
	End   int64
	Label string
}

// RawHit is one raw-trace fallback hit.
type RawHit struct {
	Trace      string `json:"trace"`
	Session    string `json:"session"`
	Turn       int64  `json:"turn"`
	Author     string `json:"author"`
	OccurredAt int64  `json:"occurred_at"`
	Text       string `json:"text"`
}

// Trace is a trace row (schema columns 1:1).
type Trace struct {
	Hash        string `json:"hash"`
	Scope       string `json:"scope"`
	Session     string `json:"session"`
	Turn        int64  `json:"turn"`
	Kind        string `json:"kind"`
	Author      string `json:"author"`
	Audience    string `json:"audience"`
	Text        string `json:"text"`
	OccurredAt  int64  `json:"occurred_at"`
	RecordedAt  int64  `json:"recorded_at"`
	MetaJSON    string `json:"meta_json"`
	Processed   int64  `json:"processed"`
	ProcessedAt *int64 `json:"processed_at"`
}

// Node is a node row (schema columns 1:1).
type Node struct {
	ID        string  `json:"id"`
	Scope     string  `json:"scope"`
	Kind      string  `json:"kind"`
	Label     string  `json:"label"`
	NormLabel string  `json:"norm_label"`
	Status    string  `json:"status"`
	Audience  string  `json:"audience"`
	World     *string `json:"world"`
	Carrier   *string `json:"carrier"`
	Locus     *string `json:"locus"`
	CreatedAt int64   `json:"created_at"`
	RetiredAt *int64  `json:"retired_at"`
}

// Assertion is an assertion row (schema columns 1:1) plus the joined labels
// used by candidates. created_by is not a column; the read-out keeps it null.
type Assertion struct {
	NodeID          string  `json:"node_id"`
	Scope           string  `json:"scope"`
	SubjectID       *string `json:"subject_id"`
	SubjectLiteral  *string `json:"subject_literal"`
	RelID           string  `json:"rel_id"`
	ObjectID        *string `json:"object_id"`
	ObjectLiteral   *string `json:"object_literal"`
	Bare            bool    `json:"bare"`
	World           *string `json:"world"`
	Carrier         *string `json:"carrier"`
	Locus           *string `json:"locus"`
	TValidFrom      int64   `json:"t_valid_from"`
	TValidTo        int64   `json:"t_valid_to"`
	TRecord         int64   `json:"t_record"`
	TSource         int64   `json:"t_source"`
	TemporalClass   string  `json:"temporal_class"`
	HolderID        *string `json:"holder_id"`
	Warrant         string  `json:"warrant"`
	AnchorTraceHash string  `json:"anchor_trace_hash"`
	AnchorStart     int64   `json:"anchor_start"`
	AnchorEnd       int64   `json:"anchor_end"`
	SourceTraceHash string  `json:"source_trace_hash"`
	Audience        string  `json:"audience"`
	Confidence      float64 `json:"confidence"`
	Status          string  `json:"status"`
	LoopState       *string `json:"loop_state"`
	Falsifier       *string `json:"falsifier"`
	Hash            string  `json:"hash"`
	CreatedAt       int64   `json:"created_at"`
}

// CandidateRow embeds Assertion and adds the joined labels and trace columns.
type CandidateRow struct {
	Assertion
	SLabel          string `json:"s_label"`
	SAud            string `json:"s_aud"`
	XLabel          string `json:"x_label"`
	XAud            string `json:"x_aud"`
	OLabel          string `json:"o_label"`
	OAud            string `json:"o_aud"`
	TraceText       string `json:"trace_text"`
	TraceAudience   string `json:"trace_audience"`
	TraceOccurredAt int64  `json:"trace_occurred_at"`
}

// MetaEdge is a meta_edge row (schema columns 1:1).
type MetaEdge struct {
	ID             int64   `json:"id"`
	Scope          string  `json:"scope"`
	MetaKind       string  `json:"meta_kind"`
	LHSID          string  `json:"lhs_id"`
	RHSID          string  `json:"rhs_id"`
	Status         string  `json:"status"`
	EvidenceAnchor *string `json:"evidence_anchor"`
	CreatedAt      int64   `json:"created_at"`
	CreatedBy      string  `json:"created_by"`
	RevertOf       *int64  `json:"revert_of"`
}

// Event is an event row (schema columns 1:1).
type Event struct {
	ID        int64  `json:"id"`
	Scope     string `json:"scope"`
	Op        string `json:"op"`
	RefsJSON  string `json:"refs_json"`
	Note      string `json:"note"`
	CreatedAt int64  `json:"created_at"`
	CreatedBy string `json:"created_by"`
}

// Mention is a mention row (schema columns 1:1).
type Mention struct {
	ID        int64   `json:"id"`
	Scope     string  `json:"scope"`
	TraceHash string  `json:"trace_hash"`
	SpanStart int64   `json:"span_start"`
	SpanEnd   int64   `json:"span_end"`
	Surface   string  `json:"surface"`
	NodeID    *string `json:"node_id"`
	CreatedAt int64   `json:"created_at"`
}

// Alias is a node_alias row (schema columns 1:1).
type Alias struct {
	NodeID    string `json:"node_id"`
	Alias     string `json:"alias"`
	NormAlias string `json:"norm_alias"`
	Source    string `json:"source"`
	CreatedAt int64  `json:"created_at"`
}

// RecallRecentCitedUnitIDs fetches the unit IDs from active Noetic records in the session.
func (s *Store) RecallRecentCitedUnitIDs(session string) ([]string, error) {
	rows, err := s.DB().Query(`
		SELECT cited_unit_ids_json 
		FROM thin_vault_noetic 
		WHERE status = 'active' 
		ORDER BY occurred_at DESC LIMIT 5
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	seen := make(map[string]bool)
	var all []string
	for rows.Next() {
		var uidsJSON string
		if err := rows.Scan(&uidsJSON); err != nil {
			continue
		}
		var uids []string
		_ = json.Unmarshal([]byte(uidsJSON), &uids)
		for _, uid := range uids {
			if !seen[uid] {
				seen[uid] = true
				all = append(all, uid)
			}
		}
	}
	return all, nil
}

// Map renders the row with the raw schema keys and nulls preserved.
func (t Trace) Map() map[string]any {
	return map[string]any{
		"hash": t.Hash, "scope": t.Scope, "session": t.Session, "turn": t.Turn,
		"kind": t.Kind, "author": t.Author, "audience": t.Audience, "text": t.Text,
		"occurred_at": t.OccurredAt, "recorded_at": t.RecordedAt,
		"meta_json": t.MetaJSON, "processed": t.Processed, "processed_at": t.ProcessedAt,
	}
}

// Map renders the row with the raw schema keys and nulls preserved.
func (n Node) Map() map[string]any {
	return map[string]any{
		"id": n.ID, "scope": n.Scope, "kind": n.Kind, "label": n.Label,
		"norm_label": n.NormLabel, "status": n.Status, "audience": n.Audience,
		"world": n.World, "carrier": n.Carrier, "locus": n.Locus,
		"created_at": n.CreatedAt, "retired_at": n.RetiredAt,
	}
}

// Map renders the row with the raw schema keys and nulls preserved.
func (a Assertion) Map() map[string]any {
	return map[string]any{
		"node_id": a.NodeID, "scope": a.Scope,
		"subject_id": a.SubjectID, "subject_literal": a.SubjectLiteral,
		"rel_id": a.RelID, "object_id": a.ObjectID, "object_literal": a.ObjectLiteral,
		"bare": boolInt(a.Bare), "world": a.World, "carrier": a.Carrier, "locus": a.Locus,
		"t_valid_from": a.TValidFrom, "t_valid_to": a.TValidTo,
		"t_record": a.TRecord, "t_source": a.TSource,
		"temporal_class": a.TemporalClass, "holder_id": a.HolderID, "warrant": a.Warrant,
		"anchor_trace_hash": a.AnchorTraceHash, "anchor_start": a.AnchorStart,
		"anchor_end": a.AnchorEnd, "source_trace_hash": a.SourceTraceHash,
		"audience": a.Audience, "confidence": a.Confidence, "status": a.Status,
		"loop_state": a.LoopState, "falsifier": a.Falsifier, "hash": a.Hash,
		"created_at": a.CreatedAt,
	}
}

// Map renders the joined candidate row with the raw assertion keys.
func (c CandidateRow) Map() map[string]any {
	result := c.Assertion.Map()
	result["s_label"] = c.SLabel
	result["s_aud"] = c.SAud
	result["x_label"] = c.XLabel
	result["x_aud"] = c.XAud
	result["o_label"] = c.OLabel
	result["o_aud"] = c.OAud
	result["trace_text"] = c.TraceText
	result["trace_audience"] = c.TraceAudience
	result["trace_occurred_at"] = c.TraceOccurredAt
	return result
}

// Map renders the row with the raw schema keys and nulls preserved.
func (m MetaEdge) Map() map[string]any {
	return map[string]any{
		"id": m.ID, "scope": m.Scope, "meta_kind": m.MetaKind,
		"lhs_id": m.LHSID, "rhs_id": m.RHSID, "status": m.Status,
		"evidence_anchor": m.EvidenceAnchor, "created_at": m.CreatedAt,
		"created_by": m.CreatedBy, "revert_of": m.RevertOf,
	}
}

// Map renders the row with the raw schema keys and nulls preserved.
func (e Event) Map() map[string]any {
	return map[string]any{
		"id": e.ID, "scope": e.Scope, "op": e.Op, "refs_json": e.RefsJSON,
		"note": e.Note, "created_at": e.CreatedAt, "created_by": e.CreatedBy,
	}
}

// Map renders the row with the raw schema keys and nulls preserved.
func (m Mention) Map() map[string]any {
	return map[string]any{
		"id": m.ID, "scope": m.Scope, "trace_hash": m.TraceHash,
		"span_start": m.SpanStart, "span_end": m.SpanEnd, "surface": m.Surface,
		"node_id": m.NodeID, "created_at": m.CreatedAt,
	}
}

// Map renders the row with the raw schema keys and nulls preserved.
func (a Alias) Map() map[string]any {
	return map[string]any{
		"node_id": a.NodeID, "alias": a.Alias, "norm_alias": a.NormAlias,
		"source": a.Source, "created_at": a.CreatedAt,
	}
}

// TraceOptions carries add_trace keyword arguments.
type TraceOptions struct {
	Session    string
	Turn       any
	Kind       string
	Author     string
	Audience   string
	OccurredAt *int64
	RecordedAt *int64
	Meta       map[string]any
}

// NodeOptions carries ensure_node keyword arguments.
type NodeOptions struct {
	Audience  string
	World     *string
	Carrier   *string
	Locus     *string
	Status    string
	Qualifier string
}

// AppendAssertionRequest carries append_assertion keyword arguments.
type AppendAssertionRequest struct {
	Scope           string
	RelID           string
	AnchorTraceHash string
	AnchorStart     int
	AnchorEnd       int
	SubjectID       *string
	SubjectLiteral  *string
	ObjectID        *string
	ObjectLiteral   *string
	Warrant         string
	Audience        string
	Confidence      float64
	TValidFrom      int64
	TValidTo        int64
	TRecord         *int64
	TSource         *int64
	HolderID        *string
	World           *string
	Carrier         *string
	Locus           *string
	Bare            bool
	Status          string
	LoopState       *string
	Falsifier       *string
	SourceTraceHash *string
	CreatedBy       string
}

// SeedOptions carries search_seeds keyword arguments.
type SeedOptions struct {
	Scope        string
	Reader       string
	Limit        int
	Deadline     *float64
	TokenCorrect *bool
	SearchEngine string
}

// CandidateOptions carries candidates keyword arguments.
type CandidateOptions struct {
	Scope        string
	Reader       string
	AsOf         int64
	WindowStart  int64
	WindowEnd    int64
	Depth        int
	Limit        int
	BoostWindow  *Window
	BoostMonth   *int
	Session      *string
	ClosureKinds []string
	Query        string
	Presence     *memcore.PresenceCoordinates
}

// AnchorOptions carries assertions_by_anchor keyword arguments.
type AnchorOptions struct {
	Scope       string
	Reader      string
	AsOf        int64
	WindowStart int64
	WindowEnd   int64
	Session     *string
	Limit       int
}

// RawOptions carries raw_traces keyword arguments.
type RawOptions struct {
	Scope         string
	Reader        string
	WindowStart   int64
	WindowEnd     int64
	AsOf          int64
	Limit         int
	ExcludeHashes []string
	Session       *string
	Month         *int
	ExtraTerms    []string
}

// Store wraps one SQLite connection.
type Store struct {
	Config *config.Config

	conn    *sql.DB
	fts     bool
	profile db.StorageProfile
	warning *string
	// rawError carries the last raw-leg failure note; the read chain resets it
	// once per recall (loud raw errors).
	rawErrorMu sync.Mutex
	rawError   *string
}

// Open opens (and migrates) the configured database.
func Open(cfg *config.Config) (*Store, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	if err := db.VaultScopePreflight(cfg.DB, cfg.Scope); err != nil {
		return nil, err
	}
	if err := cfg.Ensure(); err != nil {
		return nil, err
	}
	profile := db.LocalProfile()
	switch strings.ToLower(cfg.Storage.Profile) {
	case "usb":
		profile = db.USBProfile()
	case "", "local", "auto":
		profile = db.LocalProfile()
	}
	connection, err := db.Connect(cfg.DB, profile)
	if err != nil {
		return nil, err
	}
	if err := db.BindVaultScope(connection, cfg.Scope); err != nil {
		connection.Close()
		return nil, err
	}
	if _, err := db.Migrate(connection); err != nil {
		connection.Close()
		return nil, err
	}
	hasFTS, err := db.HasFTS(connection)
	if err != nil {
		connection.Close()
		return nil, err
	}
	store := &Store{
		Config:  cfg,
		conn:    connection,
		fts:     hasFTS,
		profile: profile,
	}
	return store, nil
}

// RefreshFTS re-samples FTS availability (node_fts + trace_fts). The flag is
// otherwise sampled only when the store opens; a caller that removes or
// restores the virtual tables out of band refreshes it here so the read and
// write legs see the same state the standard oracle reads through db.has_fts.
func (s *Store) RefreshFTS() error {
	hasFTS, err := db.HasFTS(s.conn)
	if err != nil {
		return err
	}
	s.fts = hasFTS
	return nil
}

// Close closes the connection; the usb profile truncates the WAL first, and the
// local profile runs a non-blocking PASSIVE checkpoint so concurrent multi-instance
// MCP daemons do not let the WAL file grow unboundedly.
func (s *Store) Close() error {
	if s.conn == nil {
		return nil
	}
	if s.profile.Name == "usb" {
		_, _ = s.conn.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	} else {
		_, _ = s.conn.Exec("PRAGMA wal_checkpoint(PASSIVE)")
	}
	_ = s.conn.Close()
	return nil
}

// DB exposes the connection for the fixed SQL owned by sync/hooks/extractor.
func (s *Store) DB() *sql.DB {
	return s.conn
}

// Event appends one audit row and returns its id.
func (s *Store) Event(scope, op string, refs map[string]any, note, createdBy string) (int64, error) {
	createdBy = defaultIfEmpty(createdBy, "system")
	return insertEvent(s.conn, scope, op, refs, note, createdBy, false)
}

// EventTx appends one audit row inside tx and returns its id.
func (s *Store) EventTx(tx *sql.Tx, scope, op string, refs map[string]any, note, createdBy string) (int64, error) {
	createdBy = defaultIfEmpty(createdBy, "system")
	return insertEvent(tx, scope, op, refs, note, createdBy, false)
}

func insertEvent(q querier, scope, op string, refs map[string]any, note, createdBy string, ensureASCII bool) (int64, error) {
	if refs == nil {
		refs = map[string]any{}
	}
	payload, err := ids.PyJSONDump(refs, ensureASCII, true, false)
	if err != nil {
		return 0, err
	}
	result, err := q.Exec(
		"INSERT INTO event(scope, op, refs_json, note, created_at, created_by) VALUES (?, ?, ?, ?, ?, ?)",
		scope, op, payload, note, clock.Unix(), createdBy,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// querier is the subset shared by *sql.DB and *sql.Tx.
type querier interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// queryMaps executes query and scans each row into a column-keyed map.
func queryMaps(q querier, query string, args ...any) ([]map[string]any, error) {
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	result := []map[string]any{}
	for rows.Next() {
		values := make([]any, len(columns))
		targets := make([]any, len(columns))
		for index := range values {
			targets[index] = &values[index]
		}
		if err := rows.Scan(targets...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(columns))
		for index, column := range columns {
			row[column] = values[index]
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// queryOneMap returns the first row, or nil.
func queryOneMap(q querier, query string, args ...any) (map[string]any, error) {
	rows, err := queryMaps(q, query, args...)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0], nil
}

func queryCount(q querier, query string, args ...any) (int64, error) {
	var value int64
	err := q.QueryRow(query, args...).Scan(&value)
	return value, err
}

func execCount(q querier, query string, args ...any) (int64, error) {
	result, err := q.Exec(query, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func nodeFromMap(row map[string]any) Node {
	return Node{
		ID:        mapString(row, "id"),
		Scope:     mapString(row, "scope"),
		Kind:      mapString(row, "kind"),
		Label:     mapString(row, "label"),
		NormLabel: mapString(row, "norm_label"),
		Status:    mapString(row, "status"),
		Audience:  mapString(row, "audience"),
		World:     mapPtrString(row, "world"),
		Carrier:   mapPtrString(row, "carrier"),
		Locus:     mapPtrString(row, "locus"),
		CreatedAt: mapInt64(row, "created_at"),
		RetiredAt: mapPtrInt64(row, "retired_at"),
	}
}

func traceFromMap(row map[string]any) Trace {
	return Trace{
		Hash:        mapString(row, "hash"),
		Scope:       mapString(row, "scope"),
		Session:     mapString(row, "session"),
		Turn:        mapInt64(row, "turn"),
		Kind:        mapString(row, "kind"),
		Author:      mapString(row, "author"),
		Audience:    mapString(row, "audience"),
		Text:        mapString(row, "text"),
		OccurredAt:  mapInt64(row, "occurred_at"),
		RecordedAt:  mapInt64(row, "recorded_at"),
		MetaJSON:    mapString(row, "meta_json"),
		Processed:   mapInt64(row, "processed"),
		ProcessedAt: mapPtrInt64(row, "processed_at"),
	}
}

func assertionFromMap(row map[string]any) Assertion {
	return Assertion{
		NodeID:          mapString(row, "node_id"),
		Scope:           mapString(row, "scope"),
		SubjectID:       mapPtrString(row, "subject_id"),
		SubjectLiteral:  mapPtrString(row, "subject_literal"),
		RelID:           mapString(row, "rel_id"),
		ObjectID:        mapPtrString(row, "object_id"),
		ObjectLiteral:   mapPtrString(row, "object_literal"),
		Bare:            mapBool(row, "bare"),
		World:           mapPtrString(row, "world"),
		Carrier:         mapPtrString(row, "carrier"),
		Locus:           mapPtrString(row, "locus"),
		TValidFrom:      mapInt64(row, "t_valid_from"),
		TValidTo:        mapInt64(row, "t_valid_to"),
		TRecord:         mapInt64(row, "t_record"),
		TSource:         mapInt64(row, "t_source"),
		TemporalClass:   mapString(row, "temporal_class"),
		HolderID:        mapPtrString(row, "holder_id"),
		Warrant:         mapString(row, "warrant"),
		AnchorTraceHash: mapString(row, "anchor_trace_hash"),
		AnchorStart:     mapInt64(row, "anchor_start"),
		AnchorEnd:       mapInt64(row, "anchor_end"),
		SourceTraceHash: mapString(row, "source_trace_hash"),
		Audience:        mapString(row, "audience"),
		Confidence:      mapFloat(row, "confidence"),
		Status:          mapString(row, "status"),
		LoopState:       mapPtrString(row, "loop_state"),
		Falsifier:       mapPtrString(row, "falsifier"),
		Hash:            mapString(row, "hash"),
		CreatedAt:       mapInt64(row, "created_at"),
	}
}

func candidateRowFromMap(row map[string]any) CandidateRow {
	return CandidateRow{
		Assertion:       assertionFromMap(row),
		SLabel:          mapString(row, "s_label"),
		SAud:            mapString(row, "s_aud"),
		XLabel:          mapString(row, "x_label"),
		XAud:            mapString(row, "x_aud"),
		OLabel:          mapString(row, "o_label"),
		OAud:            mapString(row, "o_aud"),
		TraceText:       mapString(row, "trace_text"),
		TraceAudience:   mapString(row, "trace_audience"),
		TraceOccurredAt: mapInt64(row, "trace_occurred_at"),
	}
}

func mapString(row map[string]any, key string) string {
	if value, ok := row[key].(string); ok {
		return value
	}
	return ""
}

func mapPtrString(row map[string]any, key string) *string {
	switch value := row[key].(type) {
	case string:
		result := value
		return &result
	case []byte:
		result := string(value)
		return &result
	default:
		return nil
	}
}

func mapInt64(row map[string]any, key string) int64 {
	switch value := row[key].(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		return int64(value)
	case bool:
		if value {
			return 1
		}
		return 0
	case string:
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err == nil {
			return parsed
		}
	}
	return 0
}

func mapPtrInt64(row map[string]any, key string) *int64 {
	value, ok := row[key]
	if !ok || value == nil {
		return nil
	}
	result := mapInt64(row, key)
	return &result
}

func mapFloat(row map[string]any, key string) float64 {
	switch value := row[key].(type) {
	case float64:
		return value
	case int64:
		return float64(value)
	case int:
		return float64(value)
	case string:
		parsed, err := strconv.ParseFloat(value, 64)
		if err == nil {
			return parsed
		}
	}
	return 0
}

func mapBool(row map[string]any, key string) bool {
	switch value := row[key].(type) {
	case bool:
		return value
	case int64:
		return value != 0
	case int:
		return value != 0
	case float64:
		return value != 0
	}
	return false
}

func mapAny(item map[string]any, key string) any {
	if value, ok := item[key]; ok {
		return value
	}
	return nil
}

// cloneMap mirrors dict(row) so callers can add keys without mutating the row.
func cloneMap(item map[string]any) map[string]any {
	copied := make(map[string]any, len(item)+2)
	for key, value := range item {
		copied[key] = value
	}
	return copied
}

// pyRound rounds value to digits decimal places using half-even float formatting.
func pyRound(value float64, digits int) float64 {
	text := strconv.FormatFloat(value, 'f', digits, 64)
	parsed, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return value
	}
	return parsed
}

// pyStrip mirrors str.strip() for the whitespace set used by ids.
func pyStrip(value string) string {
	return strings.TrimFunc(value, isPySpace)
}

func isPySpace(char rune) bool {
	switch char {
	case '\t', '\n', '\v', '\f', '\r', ' ', '\u0085', '\u00a0', '\u1680',
		'\u2028', '\u2029', '\u202f', '\u205f', '\u3000':
		return true
	}
	if char >= '\u001c' && char <= '\u001f' {
		return true
	}
	if char >= '\u2000' && char <= '\u200a' {
		return true
	}
	return false
}

// pyRepr mirrors a simple repr() for error messages.
func pyRepr(value string) string {
	escaped := strings.ReplaceAll(value, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `'`, `\'`)
	return "'" + escaped + "'"
}

func runeCount(value string) int {
	return utf8.RuneCountInString(value)
}

// sliceRunes mirrors text[start:end] for standard str indices.
func sliceRunes(value string, start, end int) string {
	if start < 0 || end < start {
		return ""
	}
	runes := []rune(value)
	if start >= len(runes) {
		return ""
	}
	if end > len(runes) {
		end = len(runes)
	}
	return string(runes[start:end])
}

// sliceRunesBounded extracts the same rune-addressed span as the inline
// `[]rune(text)[start:end]` idiom but stops scanning at `end` and never materializes the
// whole rune copy, so it stays cheap for the multi-hundred-KB anchored texts that appear in
// the candidate scoring loops. Out-of-range ends yield "" (strict), matching the original
// inline range check instead of sliceRunes' clamp.
func sliceRunesBounded(value string, start, end int) string {
	if start < 0 || end <= start {
		return ""
	}
	runeIdx := 0
	byteStart := -1
	for byteIdx := range value {
		if runeIdx == start {
			byteStart = byteIdx
		}
		if runeIdx == end {
			if byteStart < 0 {
				return ""
			}
			return value[byteStart:byteIdx]
		}
		runeIdx++
	}
	if byteStart < 0 || runeIdx != end {
		return ""
	}
	return value[byteStart:]
}

func firstChars(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func shortHash(value string) string {
	if len(value) <= 8 {
		return value
	}
	return value[:8]
}

func anchorRef(traceHash string, start, end int64) string {
	return fmt.Sprintf("trace:%s#%d-%d", shortHash(traceHash), start, end)
}

func pyIntValue(value any) int64 {
	switch typed := value.(type) {
	case nil:
		return 0
	case int:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	case json.Number:
		parsed, err := typed.Int64()
		if err == nil {
			return parsed
		}
	case bool:
		if typed {
			return 1
		}
		return 0
	}
	return 0
}

func containsValue(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func isSafeColumn(name string) bool {
	if name == "" {
		return false
	}
	for _, char := range strings.ReplaceAll(name, "_", "") {
		if !unicode.IsLetter(char) && !unicode.IsDigit(char) {
			return false
		}
	}
	return true
}

func defaultIfEmpty(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// monthOf mirrors time.localtime(epoch).tm_mon.
func monthOf(epoch int64) int {
	return int(time.Unix(epoch, 0).In(time.Local).Month())
}

func absFloat(value float64) float64 {
	return math.Abs(value)
}
