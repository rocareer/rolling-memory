// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package memcore provides the database-agnostic, de-branded 1:1 memory kernel
// shared identically between PostgreSQL and SQLite backends.
//
// Design invariants:
//   - Zero private codenames in public identifiers (standard "memcore" / "memory" /
//     "noetic" / "encoder" terminology, with backward-compatible parsing for
//     legacy prefixes).
//   - 1:1 isomorphic domain structs across PostgreSQL and SQLite.
//   - Pure Go stdlib only: no ORM or host config singletons inside memcore.
package memcore

import (
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
)

// HashStringIDToUint64 deterministically maps a string node/assertion ID (e.g. "n_..." or "a_...")
// into a positive 59-bit uint64 (below EpisodeNodeMask = 1<<60) so uint64-indexed algorithms
// (such as EpisodeUniverse) work identically on string-ID and numeric-ID stores.
func HashStringIDToUint64(id string) uint64 {
	s := strings.TrimSpace(id)
	if s == "" {
		return 0
	}
	if n, err := strconv.ParseUint(strings.TrimPrefix(s, "#"), 10, 64); err == nil && n > 0 {
		return n
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	v := h.Sum64() & ((uint64(1) << 59) - 1)
	if v == 0 {
		v = 1
	}
	return v
}

// Standard de-branded identifiers and backward-compatible aliases.
const (
	// EngineName is the canonical de-branded engine identifier.
	EngineName = "rolling-memory"

	// BandIDPrefix is the single canonical band-id namespace (`mem:band:`).
	// Legacy vendor-prefixed namespaces are retired: only this canonical
	// prefix is produced or recognized (zero-compat standard).
	BandIDPrefix = "mem:band:"
	// ClusterIDPrefix is the canonical cluster-id namespace (`mem:cluster:`).
	ClusterIDPrefix = "mem:cluster:"

	// EpisodeBandID is the canonical band identifier for episode-universe recall bands.
	EpisodeBandID = BandIDPrefix + "episode"

	// DefaultEncoderAgentName is the canonical memory encoder agent name.
	DefaultEncoderAgentName = "memory-encoder"

	// NodeLabelColumnCap is the maximum rune length for node labels, relations, and aliases.
	NodeLabelColumnCap = 512
	// LocusColumnCap is the maximum rune length for spatial locus coordinates.
	LocusColumnCap = 500
	// AudienceSubjectCap is the maximum rune length for audience_subject.
	AudienceSubjectCap = 120
	// RawHitTextCap is the non-truncating per-hit raw trace delivery cap.
	RawHitTextCap = 1200

	// Universal 0–99 Band Delivery Invariant across all scenarios:
	//   - 0–96 Primary Bands (SO/SP subject bands + XO predicate-object band);
	//   - +1 Misc Band (unanchored / companion assertions);
	//   - +2 Evidence Bands (1 Evidence-Lines band [E#k] + 1 Evidence-Artifacts band [A#m]);
	//   - Total = 0–99 Bands.
	MaxPrimaryBands       = 96
	MaxMiscBands          = 1
	MaxEvidenceBands      = 2
	MaxTotalDeliveryBands = MaxPrimaryBands + MaxMiscBands + MaxEvidenceBands // 99

	BandIDXO                = BandIDPrefix + "xo"
	BandIDMisc              = BandIDPrefix + "misc"
	BandIDEvidenceLines     = BandIDPrefix + "evidence"
	BandIDEvidenceArtifacts = BandIDPrefix + "artifacts"
	BandIDHead              = BandIDPrefix + "head"
	BandIDSelfFeed          = BandIDPrefix + "selffeed"
	BandIDSrc               = BandIDPrefix + "src"
	BandIDSrc2              = BandIDPrefix + "src2"
)

// NormalizePrimaryBandCap clamps caller topK into the [1, MaxPrimaryBands] (1..96) primary-band budget.
// Callers passing <=0 or >96 (e.g. gateway default 100 or total cap 99) receive 96 primary bands,
// leaving +1 Misc band and +2 Evidence bands (lines + artifacts) for a total of 0–99 bands.
func NormalizePrimaryBandCap(topK int) int {
	if topK <= 0 || topK > MaxPrimaryBands {
		return MaxPrimaryBands
	}
	return topK
}

// stripBandPrefix strips the canonical `mem:band:` prefix; ids outside the
// canonical namespace are not recognized (legacy vendor prefixes are retired).
func stripBandPrefix(bandID string) (string, bool) {
	b := strings.TrimSpace(bandID)
	if !strings.HasPrefix(b, BandIDPrefix) {
		return "", false
	}
	rest := strings.TrimPrefix(b, BandIDPrefix)
	if at := strings.IndexByte(rest, '@'); at > 0 {
		rest = rest[:at]
	}
	return rest, rest != ""
}

// IsPrimaryDeliveryBandID reports whether bandID belongs to the 0–96 Primary Bands (SO/SP subject bands or XO band).
func IsPrimaryDeliveryBandID(bandID string) bool {
	rest, ok := stripBandPrefix(bandID)
	if !ok {
		return false
	}
	switch rest {
	case "misc", "evidence", "artifacts", "src", "src2", "fallback", "selffeed", "episode", "head":
		return false
	default:
		return !strings.HasPrefix(rest, "episode")
	}
}

// IsMiscDeliveryBandID reports whether bandID is the +1 Misc band.
func IsMiscDeliveryBandID(bandID string) bool {
	rest, ok := stripBandPrefix(bandID)
	return ok && rest == "misc"
}

// IsEvidenceDeliveryBandID reports whether bandID is one of the +2 Evidence bands (evidence lines or evidence artifacts).
func IsEvidenceDeliveryBandID(bandID string) bool {
	rest, ok := stripBandPrefix(bandID)
	return ok && (rest == "evidence" || rest == "artifacts")
}

// IsEpisodeBandID reports whether bandID is the episode universe band.
func IsEpisodeBandID(bandID string) bool {
	b := strings.TrimSpace(bandID)
	return b == EpisodeBandID || b == BandIDMisc
}

// Closed-set enumerations shared 1:1 between PostgreSQL and SQLite schemas.
var (
	NodeKinds = []string{
		"referent",
		"relation_type",
		"assertion",
		"role",
		"expression",
		"incarnation",
	}

	NodeStatuses = []string{
		"provisional",
		"active",
		"merged",
		"retired",
	}

	Audiences = []string{"private", "group", "shared", "public"}

	AudienceRank = map[string]int{
		"private": 0,
		"group":   1,
		"shared":  2,
		"public":  3,
	}

	Warrants = []string{"referential", "performative", "procedural", "experiential"}

	WarrantSet = map[string]bool{
		"referential":  true,
		"performative": true,
		"procedural":   true,
		"experiential": true,
	}

	Worlds = []string{"actual", "fictional", "hypothetical", "dream"}

	WorldSet = map[string]bool{
		"actual":       true,
		"fictional":    true,
		"hypothetical": true,
		"dream":        true,
	}

	Carriers = []string{"real", "virtual"}

	CarrierSet = map[string]bool{
		"real":    true,
		"virtual": true,
	}

	// GapReasons is the 4-value closed set for unbound slots (subject_gap / object_gap / holder_gap).
	GapReasons = []string{
		"absent_in_source",
		"referent_unresolvable",
		"unattributed",
		"out_of_scope",
	}

	GapSet = map[string]bool{
		"absent_in_source":      true,
		"referent_unresolvable": true,
		"unattributed":          true,
		"out_of_scope":          true,
	}

	// TemporalClasses canonical closed set + legacy client values accepted by NormalizeTemporalClass.
	TemporalClasses = []string{"dated", "atemporal", "unknown"}

	TemporalSet = map[string]bool{
		"dated":     true,
		"atemporal": true,
		"unknown":   true,
		// Legacy client values accepted and normalized when mapping to canonical schema:
		"past":    true,
		"present": true,
		"future":  true,
	}

	AssertionStatuses = []string{
		"active",
		"superseded",
		"expired",
		"disputed",
		"retracted",
	}

	LoopStates = []string{"open", "fulfilled", "waived", "broken"}

	EventSources = []string{"", "explicit", "relative_resolved"}

	// CanonicalMetaKinds defines the 8-kind cognitive-algebra closed set for memory_meta.meta_kind
	// (4 concept/relation topology kinds + 4 proposition/epistemic dynamics kinds, 1:1 with PostgreSQL CHECK constraint).
	CanonicalMetaKinds = []string{
		"same_as",
		"inverse_of",
		"subtype_of",
		"part_of",
		"derived_from",
		"causes",
		"supersedes",
		"conflicts_with",
	}

	canonicalMetaKindSet = map[string]bool{
		"same_as":        true,
		"inverse_of":     true,
		"subtype_of":     true,
		"part_of":        true,
		"derived_from":   true,
		"causes":         true,
		"supersedes":     true,
		"conflicts_with": true,
	}
)

// NormalizeMetaKind converts hyphenated or underscore meta_kind into canonical underscore form,
// mapping legacy "supplement" to "derived_from".
func NormalizeMetaKind(kind string) string {
	k := strings.ToLower(strings.TrimSpace(kind))
	k = strings.ReplaceAll(k, "-", "_")
	if k == "supplement" {
		return "derived_from"
	}
	return k
}

// IsCanonicalMetaKind reports whether kind (after normalization) belongs to the 8-kind closed set.
func IsCanonicalMetaKind(kind string) bool {
	return canonicalMetaKindSet[NormalizeMetaKind(kind)]
}

// LegacyHyphenMetaKind converts canonical underscore meta_kind to legacy hyphenated SQLite form.
func LegacyHyphenMetaKind(kind string) string {
	k := NormalizeMetaKind(kind)
	if k == "derived_from" {
		return "supplement"
	}
	return strings.ReplaceAll(k, "_", "-")
}

// NormalizeTemporalClass normalizes temporal_class to the canonical {dated, atemporal, unknown} set,
// enforcing the 4-temporal invariant: non-atemporal assertions with a resolved timestamp (t_valid_from > 0 or t_event > 0)
// are always "dated", while those without any timestamp are always "unknown".
func NormalizeTemporalClass(tc string, hasValidTime bool) string {
	v := strings.ToLower(strings.TrimSpace(tc))
	if v == "atemporal" {
		return "atemporal"
	}
	if hasValidTime {
		return "dated"
	}
	return "unknown"
}

// EntityID represents a universal node/assertion identifier that bridges uint64 (PostgreSQL row ID)
// and string (SQLite deterministic hash ID) without loss.
type EntityID struct {
	Num uint64
	Str string
}

// NewNumID constructs an EntityID from a numeric ID.
func NewNumID(id uint64) EntityID {
	if id == 0 {
		return EntityID{}
	}
	return EntityID{Num: id, Str: strconv.FormatUint(id, 10)}
}

// NewStrID constructs an EntityID from a string ID (parsing numeric strings when applicable).
func NewStrID(id string) EntityID {
	s := strings.TrimSpace(id)
	if s == "" {
		return EntityID{}
	}
	if n, err := strconv.ParseUint(strings.TrimPrefix(s, "#"), 10, 64); err == nil && n > 0 {
		return EntityID{Num: n, Str: s}
	}
	return EntityID{Str: s}
}

// IsZero reports whether the identifier is empty.
func (id EntityID) IsZero() bool {
	return id.Num == 0 && id.Str == ""
}

// Key returns the canonical string key for maps and hashing.
func (id EntityID) Key() string {
	if id.Str != "" {
		return id.Str
	}
	if id.Num > 0 {
		return strconv.FormatUint(id.Num, 10)
	}
	return ""
}

func (id EntityID) String() string { return id.Key() }

// Trace is the 1:1 L0 verbatim evidence record.
type Trace struct {
	Hash        string `json:"hash"`
	Scope       string `json:"scope"`
	Session     string `json:"session"`
	Turn        int64  `json:"turn"`
	Locator     string `json:"locator,omitempty"`
	Kind        string `json:"kind"`
	Author      string `json:"author"`
	Audience    string `json:"audience"`
	Text        string `json:"text"`
	OccurredAt  int64  `json:"occurred_at"`
	RecordedAt  int64  `json:"recorded_at"`
	MetaJSON    string `json:"meta_json"`
	Processed   int    `json:"processed"`
	ProcessedAt *int64 `json:"processed_at,omitempty"`
}

// Node is the 1:1 unified graph node (referent, relation_type, assertion, role, expression, incarnation).
type Node struct {
	ID            string `json:"id"`
	NumID         uint64 `json:"num_id,omitempty"`
	Scope         string `json:"scope"`
	Kind          string `json:"kind"`
	Label         string `json:"label"`
	NormLabel     string `json:"norm_label"`
	Status        string `json:"status"`
	Audience      string `json:"audience"`
	World         string `json:"world,omitempty"`
	Carrier       string `json:"carrier,omitempty"`
	Locus         string `json:"locus,omitempty"`
	IncarnationID string `json:"incarnation_id,omitempty"`
	ExtRef        string `json:"ext_ref,omitempty"`
	Origin        string `json:"origin,omitempty"`
	CreatedAt     int64  `json:"created_at"`
	RetiredAt     *int64 `json:"retired_at,omitempty"`
}

// NodeAlias is the 1:1 alias binding row.
type NodeAlias struct {
	Scope     string `json:"scope"`
	NodeID    string `json:"node_id"`
	NumNodeID uint64 `json:"num_node_id,omitempty"`
	Alias     string `json:"alias"`
	NormAlias string `json:"norm_alias"`
	Source    string `json:"source"`
	CreatedAt int64  `json:"created_at"`
}

// Assertion is the 1:1 structured memory assertion record including dual forms (SX / XO),
// unbound slots (*_gap), event time (t_event / event_src), and audience_subject.
type Assertion struct {
	NodeID          string  `json:"node_id"`
	NumNodeID       uint64  `json:"num_node_id,omitempty"`
	Scope           string  `json:"scope"`
	SubjectID       string  `json:"subject_id,omitempty"`
	NumSubjectID    uint64  `json:"num_subject_id,omitempty"`
	SubjectLiteral  string  `json:"subject_literal,omitempty"`
	SubjectAbsent   bool    `json:"subject_absent,omitempty"`
	SubjectGap      string  `json:"subject_gap,omitempty"`
	RelID           string  `json:"rel_id"`
	NumRelID        uint64  `json:"num_rel_id,omitempty"`
	ObjectID        string  `json:"object_id,omitempty"`
	NumObjectID     uint64  `json:"num_object_id,omitempty"`
	ObjectLiteral   string  `json:"object_literal,omitempty"`
	ObjectGap       string  `json:"object_gap,omitempty"`
	Bare            bool    `json:"bare,omitempty"`
	World           string  `json:"world,omitempty"`
	Carrier         string  `json:"carrier,omitempty"`
	Locus           string  `json:"locus,omitempty"`
	TValidFrom      int64   `json:"t_valid_from"`
	TValidTo        int64   `json:"t_valid_to"`
	TRecord         int64   `json:"t_record"`
	TSource         int64   `json:"t_source"`
	TEvent          int64   `json:"t_event,omitempty"`
	EventSrc        string  `json:"event_src,omitempty"`
	TemporalClass   string  `json:"temporal_class"`
	HolderID        string  `json:"holder_id,omitempty"`
	NumHolderID     uint64  `json:"num_holder_id,omitempty"`
	HolderGap       string  `json:"holder_gap,omitempty"`
	Warrant         string  `json:"warrant"`
	AnchorTraceHash string  `json:"anchor_trace_hash"`
	AnchorStart     int     `json:"anchor_start"`
	AnchorEnd       int     `json:"anchor_end"`
	SourceTraceHash string  `json:"source_trace_hash"`
	Audience        string  `json:"audience"`
	AudienceSubject string  `json:"audience_subject,omitempty"`
	Confidence      float64 `json:"confidence"`
	Status          string  `json:"status"`
	LoopState       string  `json:"loop_state,omitempty"`
	Falsifier       string  `json:"falsifier,omitempty"`
	Hash            string  `json:"hash"`
	ReadHits        int64   `json:"read_hits,omitempty"`
	LastReadAt      int64   `json:"last_read_at,omitempty"`
	CreatedAt       int64   `json:"created_at"`
}

// Participant is the 1:1 n-ary role participant row attached to an assertion.
type Participant struct {
	AssertionNodeID string `json:"assertion_node_id"`
	Seq             int    `json:"seq"`
	RoleID          string `json:"role_id"`
	RoleLabel       string `json:"role_label,omitempty"`
	TargetID        string `json:"target_id,omitempty"`
	Literal         string `json:"literal,omitempty"`
}

// Mention is the 1:1 surface mention span in a trace.
type Mention struct {
	ID         int64  `json:"id"`
	Scope      string `json:"scope"`
	TraceHash  string `json:"trace_hash"`
	SpanStart  int    `json:"span_start"`
	SpanEnd    int    `json:"span_end"`
	Surface    string `json:"surface"`
	NodeID     string `json:"node_id,omitempty"`
	ExprNodeID string `json:"expr_node_id,omitempty"`
	CreatedAt  int64  `json:"created_at"`
}

// MetaEdge is the 1:1 meta-relation row (same_as, conflicts_with, subtype_of, inverse_of, supersedes, derived_from).
type MetaEdge struct {
	ID             int64  `json:"id"`
	Scope          string `json:"scope"`
	MetaKind       string `json:"meta_kind"`
	LHSID          string `json:"lhs_id"`
	RHSID          string `json:"rhs_id"`
	Status         string `json:"status"`
	EvidenceAnchor string `json:"evidence_anchor,omitempty"`
	CreatedAt      int64  `json:"created_at"`
	CreatedBy      string `json:"created_by"`
	RevertOf       *int64 `json:"revert_of,omitempty"`
}

// Event is the 1:1 governance / audit event row.
type Event struct {
	ID        int64  `json:"id"`
	Scope     string `json:"scope"`
	Op        string `json:"op"`
	RefsJSON  string `json:"refs_json"`
	Note      string `json:"note"`
	CreatedAt int64  `json:"created_at"`
	CreatedBy string `json:"created_by"`
}

// NameVector is the 1:1 name-level vector row (one per node label and one per node alias).
type NameVector struct {
	Scope     string    `json:"scope"`
	NodeID    string    `json:"node_id"`
	NameHash  string    `json:"name_hash"`
	Name      string    `json:"name"`
	Model     string    `json:"model"`
	Dim       int       `json:"dim"`
	Vec       []float32 `json:"vec,omitempty"`
	UpdatedAt int64     `json:"updated_at"`
}

// EpisodeContainer is the 1:1 episode universe container row (L1 scene & L2 super-episode).
type EpisodeContainer struct {
	ID          int64  `json:"id"`
	Scope       string `json:"scope"`
	EpKey       string `json:"ep_key"`
	Generation  int    `json:"generation"`
	Level       int    `json:"level"`
	World       string `json:"world"`
	Carrier     string `json:"carrier"`
	Locus       string `json:"locus"`
	SessionKey  string `json:"session_key"`
	TStart      int64  `json:"t_start"`
	TEnd        int64  `json:"t_end"`
	TSourceMin  int64  `json:"t_source_min"`
	TSourceMax  int64  `json:"t_source_max"`
	MemberCount int    `json:"member_count"`
	UpdatedAt   int64  `json:"updated_at"`
}

// EpisodeMember is the 1:1 episode membership row (assertion or child episode container).
type EpisodeMember struct {
	EpisodeID  int64  `json:"episode_id"`
	Scope      string `json:"scope"`
	MemberID   string `json:"member_id"`
	MemberKind string `json:"member_kind"` // "assertion" | "episode"
	Facet      string `json:"facet"`
	CreatedAt  int64  `json:"created_at"`
}

// MemoryHot is the 1:1 two-zone working hot memory pointer ledger row.
type MemoryHot struct {
	ID              int64    `json:"id"`
	Scope           string   `json:"scope"`
	SessionKey      string   `json:"session_key"`
	Kind            string   `json:"kind"` // "recall" | "experience"
	QueryText       string   `json:"query_text,omitempty"`
	TraceHash       string   `json:"trace_hash,omitempty"`
	HitNodeIDs      []string `json:"hit_node_ids,omitempty"`
	HitAssertionIDs []string `json:"hit_assertion_ids,omitempty"`
	CreatedAt       int64    `json:"created_at"`
}

// RestateRow is the 1:1 restatement / occurrence ledger row.
type RestateRow struct {
	ID              int64  `json:"id"`
	Scope           string `json:"scope"`
	AssertionNodeID string `json:"assertion_node_id"`
	TraceHash       string `json:"trace_hash"`
	AnchorStart     int    `json:"anchor_start"`
	AnchorEnd       int    `json:"anchor_end"`
	Kind            string `json:"kind"` // "mention" | "occurrence"
	OccurredAt      int64  `json:"occurred_at"`
	CreatedAt       int64  `json:"created_at"`
}

// SubjectForm classifies an assertion's structural form (ternary, binary_sx, binary_xo, unbound).
func SubjectForm(subjectID, subjectLiteral, subjectGap string, subjectAbsent bool, objectID, objectLiteral, objectGap string) string {
	if subjectGap != "" || objectGap != "" {
		return "unbound"
	}
	if subjectAbsent {
		return "binary_xo"
	}
	if objectID == "" && objectLiteral == "" {
		return "binary_sx"
	}
	return "ternary"
}

// FormatAssertionLabel formats a deterministic human-readable label for an assertion node.
func FormatAssertionLabel(subj, rel, obj string, subjectAbsent bool, subjectGap, objectGap string) string {
	s := strings.TrimSpace(subj)
	if subjectAbsent {
		s = "∅"
	} else if subjectGap != "" && s == "" {
		s = fmt.Sprintf("?(%s)", subjectGap)
	}
	o := strings.TrimSpace(obj)
	if objectGap != "" && o == "" {
		o = fmt.Sprintf("?(%s)", objectGap)
	}
	r := strings.TrimSpace(rel)
	if o == "" {
		return strings.TrimSpace(s + " " + r)
	}
	return strings.TrimSpace(s + " " + r + " " + o)
}
