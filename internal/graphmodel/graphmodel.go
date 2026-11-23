// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package graphmodel defines the read-only memory-graph wire contract shared by
// the local engine, the daemon graph endpoint and the admin console relay.
//
// The graph is a derived projection: it stores no new vault rows. Nodes are
// grouped on two orthogonal axes — the cognitive layer (background / episodic /
// noetic) and the structural level (word → phrase → cluster, plus unit and
// free_unit). Clusters are containers; a cluster_type distinguishes a time
// episode from an entity/word-family group or a background file group.
package graphmodel

// GraphSchemaVersion identifies the word/phrase/cluster graph contract. The
// admin console uses it to distinguish the engine projection from the earlier
// evaluation-preview shape that lacked cluster containers.
const GraphSchemaVersion = 2

// Node and edge kinds emitted by the engine projection.
const (
	KindCluster = "cluster"
	KindUnit    = "unit"
	KindWord    = "word"
	KindPhrase  = "phrase"
	KindFree    = "free_unit"
)

// Cluster types emitted by the engine projection.
const (
	ClusterEpisode = "episode"
	ClusterEntity  = "entity"
	ClusterSource  = "source"
)

// Edge relations emitted by the engine projection.
const (
	RelationMemberOf      = "member_of"
	RelationWordOf        = "word_of"
	RelationPhraseOf      = "phrase_of"
	RelationPhraseHasWord = "phrase_has_word"
	RelationCites         = "cites"
	RelationCoCites       = "co_cites"
	RelationSimilar       = "similar"
	RelationComplementary = "complementary"
	RelationExplains      = "explains"
)

// Unit sources read by the graph builder.
const (
	SourceDoc    = "doc"
	SourceBG     = "bg"
	SourceNoetic = "noetic"
)

// ViewRequest is the graph/expand request relayed by the admin console.
type ViewRequest struct {
	Op           string             `json:"op,omitempty"` // view | expand; empty = view
	Scope        string             `json:"scope,omitempty"`
	Limit        int                `json:"limit,omitempty"`
	Keyword      string             `json:"keyword,omitempty"`
	Layers       []string           `json:"layers,omitempty"`
	ClusterTypes []string           `json:"cluster_types,omitempty"`
	LayerWeights map[string]float64 `json:"layer_weights,omitempty"`
	SeedKeys     []string           `json:"seed_keys,omitempty"`
	Depth        int                `json:"depth,omitempty"`
}

// UnitRow is the plaintext metadata the graph builder reads from the local
// vault. It is not part of the wire contract.
type UnitRow struct {
	UnitID       string
	Source       string // doc | bg | noetic
	Layer        string
	OccurredAt   int64
	InsertedAt   int64
	RawText      string
	RefPointer   string
	RelPath      string
	AbsPath      string
	Anchor       string
	AddressURI   string
	Locus        string
	Status       string
	QueryText    string
	AnswerText   string
	CitedUnitIDs []string
}

// WordParticle is a word-family projection attached to a unit, phrase or
// cluster node.
type WordParticle struct {
	Token    string  `json:"token"`
	GroupHex string  `json:"group_hex"`
	DF       int     `json:"df"`
	MassNats float64 `json:"mass_nats"`
	Weight   float64 `json:"weight"`
	UnitSpan int     `json:"unit_span,omitempty"`
}

// PhraseFragment is a two-term phrase projection attached to a unit node.
// LeftDF/RightDF are the member document frequencies, Containment is
// DF/min(LeftDF, RightDF) and Lift is DF*N/(LeftDF*RightDF): together they
// separate a genuine collocation from a pair that merely co-occurs because
// both members are rare or because one text run was projected twice.
type PhraseFragment struct {
	Left               string  `json:"left"`
	Right              string  `json:"right"`
	DF                 int     `json:"df"`
	LeftDF             int     `json:"left_df,omitempty"`
	RightDF            int     `json:"right_df,omitempty"`
	Containment        float64 `json:"containment,omitempty"`
	Lift               float64 `json:"lift,omitempty"`
	JointSurprisalNats float64 `json:"joint_surprisal_nats"`
	BindingEnergyNats  float64 `json:"binding_energy_nats"`
	UnitSpan           int     `json:"unit_span,omitempty"`
}

// Node is one graph node. The envelope mirrors the admin console DTO; new
// engine-only fields are additive and omitted when empty.
type Node struct {
	ID                int              `json:"id"`
	NodeKey           string           `json:"node_key"`
	Summary           string           `json:"summary"`
	Label             string           `json:"label,omitempty"`
	Kind              string           `json:"kind"`
	Layer             string           `json:"layer"`
	ClusterType       string           `json:"cluster_type,omitempty"`
	MemberCount       int              `json:"member_count,omitempty"`
	Free              bool             `json:"free,omitempty"`
	PrimaryClusterKey string           `json:"primary_cluster_key,omitempty"`
	EnergyNats        float64          `json:"energy_nats,omitempty"`
	Certainty         string           `json:"certainty"`
	Intention         string           `json:"intention"`
	Status            string           `json:"status"`
	Confidence        float64          `json:"confidence"`
	ObservedAt        string           `json:"observed_at"`
	ValidFrom         string           `json:"valid_from"`
	ValidTo           string           `json:"valid_to"`
	Anchor            string           `json:"anchor"`
	Span              string           `json:"span"`
	Aliases           []string         `json:"aliases"`
	Degree            int              `json:"degree"`
	Weight            float64          `json:"weight"`
	LayerMix          map[string]int   `json:"layer_mix,omitempty"`
	Words             []WordParticle   `json:"words,omitempty"`
	Phrases           []PhraseFragment `json:"phrases,omitempty"`
}

// Edge is one directed relation.
type Edge struct {
	ID         int     `json:"id"`
	Assertion  string  `json:"assertion_id"`
	From       int     `json:"from"`
	To         int     `json:"to"`
	FromKey    string  `json:"from_key"`
	ToKey      string  `json:"to_key"`
	FromLabel  string  `json:"from_label"`
	ToLabel    string  `json:"to_label"`
	Relation   string  `json:"relation"`
	Layer      string  `json:"layer,omitempty"`
	Warrant    string  `json:"warrant"`
	Certainty  string  `json:"certainty"`
	Confidence float64 `json:"confidence"`
	Status     string  `json:"status"`
	ObservedAt string  `json:"observed_at"`
	ValidFrom  string  `json:"valid_from"`
	Anchor     string  `json:"anchor"`
	Span       string  `json:"span"`
	Weight     float64 `json:"weight"`
}

// ScopeItem is one graph scope summary row.
type ScopeItem struct {
	Source    string `json:"source"`
	ScopeKey  string `json:"scope_key"`
	Events    int    `json:"events"`
	Links     int    `json:"links"`
	Materials int    `json:"materials"`
}

// RelationClass is a display grouping for one relation.
type RelationClass struct {
	Key     string `json:"key"`
	Display string `json:"display"`
}

// RelationOption is a relation legend/dropdown entry.
type RelationOption struct {
	Relation string          `json:"relation"`
	Count    int             `json:"count"`
	Classes  []RelationClass `json:"classes"`
}

// KindCounts reports node counts by structural kind.
type KindCounts struct {
	Units     int `json:"units"`
	Words     int `json:"words"`
	Phrases   int `json:"phrases"`
	Clusters  int `json:"clusters"`
	FreeUnits int `json:"free_units"`
}

// ClusterTypeCounts reports cluster counts by container type.
type ClusterTypeCounts struct {
	Episode int `json:"episode"`
	Entity  int `json:"entity"`
	Source  int `json:"source"`
}

// Stats is the graph HUD/statistics envelope.
type Stats struct {
	EventTotal             int               `json:"event_total"`
	LinkTotal              int               `json:"link_total"`
	NodeShown              int               `json:"node_shown"`
	EdgeShown              int               `json:"edge_shown"`
	IsolatedShown          int               `json:"isolated_shown"`
	AvgEntityDegree        float64           `json:"avg_entity_degree"`
	MultiEdgeEntityRatio   float64           `json:"multi_edge_entity_ratio"`
	AvgRelationReuse       float64           `json:"avg_relation_reuse"`
	ObjectNodeRatio        float64           `json:"object_node_ratio"`
	LayerNodeCounts        map[string]int    `json:"layer_node_counts,omitempty"`
	WordTotal              int               `json:"word_total,omitempty"`
	PhraseTotal            int               `json:"phrase_total,omitempty"`
	DisplayFilteredWords   int               `json:"display_filtered_words,omitempty"`
	DisplayFilteredPhrases int               `json:"display_filtered_phrases,omitempty"`
	ClusterTotal           int               `json:"cluster_total,omitempty"`
	FreeUnitTotal          int               `json:"free_unit_total,omitempty"`
	KindCounts             KindCounts        `json:"kind_counts"`
	ClusterTypeCounts      ClusterTypeCounts `json:"cluster_type_counts"`
	BuildMS                int64             `json:"build_ms,omitempty"`
	LabelCoveragePct       float64           `json:"label_coverage_pct,omitempty"`
}

// Truncated reports whether the rendered view was capped.
type Truncated struct {
	Nodes bool `json:"nodes"`
	Edges bool `json:"edges"`
}

// Response is the complete graph/expand payload.
type Response struct {
	GraphSchemaVersion int              `json:"graph_schema_version"`
	Source             string           `json:"source"`
	ScopeKey           string           `json:"scope_key"`
	Scopes             []ScopeItem      `json:"scopes"`
	Nodes              []Node           `json:"nodes"`
	Edges              []Edge           `json:"edges"`
	Relations          []RelationOption `json:"relations"`
	Stats              Stats            `json:"stats"`
	Truncated          Truncated        `json:"truncated"`
}
