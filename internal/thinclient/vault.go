// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rocareer/rolling-memory/internal/episodic"
	"github.com/rocareer/rolling-memory/internal/episodicunit"
	"github.com/rocareer/rolling-memory/internal/zhao/rm"
	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

// ============================================================================
// Rolling-Memory Ultra-Thin Local Client (`internal/thinclient`)
//
// Strict Security & Anti-Reverse-Engineering Invariants:
//   1. ZERO Proprietary Math/Formulas on Client:
//      This package imports NO `math.Log`, NO `math.Exp`, NO `math.Pow`, NO `internal/rolling`,
//      and NO `internal/memcore`. It contains zero waveform formulas, zero constants,
//      zero SimHash128, zero KNN, and zero C6 cell/bubble algorithms.
//   2. ZERO Plaintext Egress:
//      All raw text and sentence arrays remain locked inside the local SQLite table `thin_vault_docs`.
//      Only `{H, RawBitsX10, ClassFlags}` integer atoms leave the vault over the wire.
// ============================================================================

// Character class bitmask flags inspected mechanically on the thin client.
const (
	FlagHasUpper uint8 = 1 << 0
	FlagHasCamel uint8 = 1 << 1
	FlagHasDigit uint8 = 1 << 2
	FlagHasConn  uint8 = 1 << 3

	// The local client exclusively maintains 3 memory types:
	//   1. LayerBackground ("background"): Reserved for future user knowledge bases, codebases, and document substrates.
	//   2. LayerEpisodic   ("episodic"):   User Harness dialogue stream (User <-> Agent turns).
	//   3. LayerNoetic     ("noetic"):     Agent retrieval handles (cited_unit_ids[]) + answer (Q, A) stored in local noetic table.
	LayerBackground = "background"
	LayerEpisodic   = "episodic"
	LayerNoetic     = "noetic"
)

// BlindTokenAtom is the irreversible blind integer tuple emitted per token slot (canonical alias to ramesdk.BlindTokenAtom).
type BlindTokenAtom = ramesdk.BlindTokenAtom

// BlindPulsePacket is the wire request sent from the thin client to the remote Cloud Blind Brain.
// All raw plaintext stays on the client (`thin_vault_docs` / `thin_vault_noetic`); the engine computes
// manifold resonance, dynamic cell containers, and graph topology across the three cognitive layers
// ("background" | "episodic" | "noetic").
type BlindPulsePacket struct {
	TenantID     string   `json:"tenant_id,omitempty"`
	Scope        string   `json:"scope,omitempty"`
	UserID       string   `json:"user_id,omitempty"`
	UnitID       string   `json:"id"`
	Layer        string   `json:"layer,omitempty"`          // "background" | "episodic" | "noetic"
	CitedUnitIDs []string `json:"cited_unit_ids,omitempty"` // Cited unit IDs for cross-layer provenance edges
	// AssistantID / IncarnationID / Locus are the answering persona, its concrete lifetime
	// instance, and the workspace locus it works in. They travel with every pulse so parallel
	// incarnations stay attributable, and they default from the local vault identity (never
	// invented per call). Plaintext-free, like every other field on the wire.
	AssistantID      string             `json:"assistant_id,omitempty"`
	IncarnationID    string             `json:"incarnation_id,omitempty"`
	Locus            string             `json:"locus,omitempty"`
	OccurredAt       int64              `json:"ts,omitempty"`
	NarratedAt       int64              `json:"narrated_at,omitempty"`
	IngestedAt       int64              `json:"ingested_at,omitempty"`
	AuthorHash       uint64             `json:"author_h,omitempty"`
	DocHash          uint64             `json:"doc_h,omitempty"`
	BlockHash        uint64             `json:"block_h,omitempty"`
	LineStart        int                `json:"line_start,omitempty"`
	LineEnd          int                `json:"line_end,omitempty"`
	Slots            []BlindTokenAtom   `json:"slots,omitempty"`
	SentenceFrames   [][]BlindTokenAtom `json:"sents,omitempty"`
	DenoiseRatio     *float64           `json:"denoise_ratio,omitempty"`      // 0 = output verbatim full text; (0,1) = fractal dry-sentence discard ratio
	MinKeepSentences int                `json:"min_keep_sentences,omitempty"` // Minimum kept sentences when denoise_ratio > 0
	WaveRadix        int                `json:"wave_radix,omitempty"`         // Optional quantization radix {0, 2, 4, 8, 16, 32, 64, 128, 256}
	// AtomsB64 carries the blind-pulse payload: the single on-wire encoding (see blindwire.go)
	// that replaces the `slots` + `sents` JSON duplication with one compact, lossless column of
	// atoms plus sentence boundaries. It carries no version identity — there is exactly one
	// payload format, so no negotiation field exists; the server expands it back into
	// Slots/SentenceFrames before handing the pulse to the engine.
	AtomsB64 string `json:"atoms_b64,omitempty"`
}

// BlindQueryOptions defines the explicit control parameters for `/open/memory/v1/query`.
type BlindQueryOptions struct {
	DeliveryBudget   string             `json:"delivery_budget,omitempty"`    // Default "auto" (adaptive 0-96); if TopK is set, TopK (1-256) takes precedence
	TopK             int                `json:"top_k,omitempty"`              // Explicit cap [1, 256]; when 0, uses delivery_budget="auto" (0-96)
	Layers           []string           `json:"layers,omitempty"`             // Optional filter: ["background", "episodic", "noetic"]
	LayerWeights     map[string]float64 `json:"layer_weights,omitempty"`      // Optional per-layer weight multipliers: {"background":1.0,"episodic":1.0,"noetic":1.2}
	SpreadRadius     *int               `json:"spread_radius,omitempty"`      // nil/<0=adaptive, 0=single-hop direct only, 1=1-hop, 2=2-hop, 3=3-hop
	SpreadGain       *float64           `json:"spread_gain,omitempty"`        // [0.0, 2.0] associative gain multiplier
	DenoiseRatio     *float64           `json:"denoise_ratio,omitempty"`      // 0 = output full raw text; (0, 1) = fractal dry-sentence discard ratio
	MinKeepSentences int                `json:"min_keep_sentences,omitempty"` // Minimum sentences kept when denoise_ratio > 0
	AsOf             int64              `json:"as_of,omitempty"`              // Light-cone horizon timestamp (0 = no cutoff)
	TimeRef          int64              `json:"time_ref,omitempty"`           // Reference timestamp for temporal proximity
	WaveRadix        int                `json:"wave_radix,omitempty"`         // Optional wave radix {0, 2..256}
	ClusterGroups    bool               `json:"cluster_groups,omitempty"`     // Request the cognitive-cluster delivery view (read-only regrouping; hits unchanged)
}

// CloudHitClusterGroup is one cognitive-cluster delivery group returned by the
// Cloud Blind Brain when BlindQueryOptions.ClusterGroups is set: a read-only
// regrouping of the ranked hits for downstream LLM consumption (protocol
// noetic-cluster-delivery-protocol-v1; hits themselves stay untouched).
type CloudHitClusterGroup struct {
	GroupID         int      `json:"group_id"`
	Day             string   `json:"day,omitempty"`
	UnitIDs         []string `json:"unit_ids"`
	PeakUnitID      string   `json:"peak_unit_id"`
	JointEnergyNats float64  `json:"joint_energy_nats"`
}

// CloudOrdinalAnnotation is the delivery-layer ordinal span day fact statement
// (first/last family) resolved from corpus time geometry under the single-run
// high-confidence gate. Informational only; it never alters ranking.
type CloudOrdinalAnnotation struct {
	Polarity    string `json:"polarity"`
	ResolvedDay string `json:"resolved_day"`
	SingleRun   bool   `json:"single_run"`
	RunSpanDays int    `json:"run_span_days"`
	AnchorDF    int    `json:"anchor_df"`
}

// CloudDeliveryView bundles the optional cluster delivery artifacts of the most
// recent envelope-mode cloud query (empty when the view was not requested).
type CloudDeliveryView struct {
	ClusterGroups     []CloudHitClusterGroup  `json:"cluster_groups,omitempty"`
	OrdinalAnnotation *CloudOrdinalAnnotation `json:"ordinal_annotation,omitempty"`
}

// BlindDistillRequest is the wire request for L1 Background document dry-sentence distillation.
type BlindDistillRequest struct {
	TenantID         string            `json:"tenant_id,omitempty"`
	Scope            string            `json:"scope,omitempty"`
	UserID           string            `json:"user_id,omitempty"`
	Document         BlindPulsePacket  `json:"document"`
	Query            *BlindPulsePacket `json:"query,omitempty"`
	DenoiseRatio     *float64          `json:"denoise_ratio,omitempty"` // 0 = output full raw text; (0, 1) = discard ratio
	MaxKeepRatio     float64           `json:"max_keep_ratio,omitempty"`
	MinKeep          int               `json:"min_keep,omitempty"`
	MinKeepSentences int               `json:"min_keep_sentences,omitempty"`
}

// CloudSlotWeight is a per-slot weight returned by the Cloud Blind Brain.
type CloudSlotWeight struct {
	SlotIndex int     `json:"slot_index"`
	Weight    float64 `json:"weight"`
	Nibble    uint8   `json:"nibble"`
}

// CloudIngestResponse is the numerical metadata returned by the Cloud Blind Brain upon ingest/distill.
type CloudIngestResponse struct {
	TenantID            string               `json:"tenant_id,omitempty"`
	Scope               string               `json:"scope,omitempty"`
	UnitID              string               `json:"id"`
	Layer               string               `json:"layer,omitempty"`
	CitedUnitIDs        []string             `json:"cited_unit_ids,omitempty"`
	Epoch               int                  `json:"epoch"`
	SlotWeights         []CloudSlotWeight    `json:"slot_weights,omitempty"`
	WaveformHex         string               `json:"waveform_hex"`
	WaveformBar         string               `json:"waveform_bar"`
	ConservedMass       float64              `json:"conserved_mass"`
	QualityMult         float64              `json:"quality_mult"`
	AddressURI          string               `json:"address_uri"`
	AddressCoord64      uint64               `json:"address_coord64"`
	ContainerID         uint64               `json:"container_id"`
	ParentID            uint64               `json:"parent_id"`
	ShiftScore          float64              `json:"shift_score"`
	DocMacroWaveform    string               `json:"doc_macro_waveform,omitempty"`
	DocMacroHex         string               `json:"doc_macro_hex,omitempty"`
	KeptSentenceIndices []int                `json:"kept_sentence_indices,omitempty"`
	CompressionRatio    float64              `json:"compression_ratio,omitempty"`
	PulseSignalScore    int                  `json:"pulse_signal_score,omitempty"`
	PulseSignalGrade    string               `json:"pulse_signal_grade,omitempty"`
	PulseSignalWarnings []PulseSignalWarning `json:"pulse_signal_warnings,omitempty"`
}

// CloudQueryHit is a single ranked document ID and weight breakdown returned by the Cloud Blind Brain.
type CloudQueryHit struct {
	UnitID              string   `json:"id"`
	Layer               string   `json:"layer,omitempty"`
	CitedUnitIDs        []string `json:"cited_unit_ids,omitempty"`
	OccurredAt          int64    `json:"occurred_at,omitempty"`
	NarratedAt          int64    `json:"narrated_at,omitempty"`
	IngestedAt          int64    `json:"ingested_at,omitempty"`
	AuthorHash          uint64   `json:"author_h,omitempty"`
	DocHash             uint64   `json:"doc_h,omitempty"`
	BlockHash           uint64   `json:"block_h,omitempty"`
	LineStart           int      `json:"line_start,omitempty"`
	LineEnd             int      `json:"line_end,omitempty"`
	LayerWeight         float64  `json:"layer_weight,omitempty"`
	AddressURI          string   `json:"address_uri"`
	AddressCoord64      uint64   `json:"address_coord64"`
	WaveformBar         string   `json:"waveform_bar"`
	WaveScore           float64  `json:"wave_score"`
	CellWormhole        float64  `json:"cell_wormhole"`
	ManifoldGain        float64  `json:"manifold_gain"`
	FinalWeight         float64  `json:"final_weight"`
	PlasticityGate      float64  `json:"plasticity_gate,omitempty"`
	KeptSentenceIndices []int    `json:"kept_sentence_indices,omitempty"`
	CompressionRatio    float64  `json:"compression_ratio,omitempty"`
}

// BlindGraphRequest is the wire request for source-agnostic unified graph topology (`POST /open/memory/v1/graph`).
type BlindGraphRequest struct {
	TenantID     string             `json:"tenant_id,omitempty"`
	Scope        string             `json:"scope,omitempty"`
	UserID       string             `json:"user_id,omitempty"`
	Layers       []string           `json:"layers,omitempty"`         // ["background", "episodic", "noetic"]
	LayerWeights map[string]float64 `json:"layer_weights,omitempty"`  // {"background": 1.0, "episodic": 1.0, "noetic": 1.2}
	CenterUnitID string             `json:"center_unit_id,omitempty"` // Optional focal unit/cell ID
	IncludeCells *bool              `json:"include_cells,omitempty"`
	IncludeUnits *bool              `json:"include_units,omitempty"`
	MaxNodes     int                `json:"max_nodes,omitempty"`
	MaxEdges     int                `json:"max_edges,omitempty"`
}

// CloudGraphNode is a single topology node returned by `/open/memory/v1/graph`.
type CloudGraphNode struct {
	ID           string   `json:"id"`
	RawID        string   `json:"raw_id"`
	NodeType     string   `json:"node_type"` // "super_cell" | "cell" | "unit"
	Layer        string   `json:"layer"`     // "background" | "episodic" | "noetic"
	LayerWeight  float64  `json:"layer_weight,omitempty"`
	ParentID     string   `json:"parent_id,omitempty"`
	AddressURI   string   `json:"address_uri,omitempty"`
	WaveformBar  string   `json:"waveform_bar,omitempty"`
	Weight       float64  `json:"weight"`
	MemberCount  int      `json:"member_count,omitempty"`
	OccurredAt   int64    `json:"occurred_at,omitempty"`
	CitedUnitIDs []string `json:"cited_unit_ids,omitempty"`
	Degree       int      `json:"degree"`
}

// CloudGraphEdge is a directed topology edge returned by `/open/memory/v1/graph`.
type CloudGraphEdge struct {
	Source string  `json:"source"`
	Target string  `json:"target"`
	Kind   string  `json:"kind"` // "hierarchy" | "member_of" | "weak_edge" | "cooccur_bridge" | "cites"
	Layer  string  `json:"layer,omitempty"`
	Weight float64 `json:"weight"`
}

// CloudGraphStats holds node and edge distribution statistics across the three layers.
type CloudGraphStats struct {
	TotalNodes      int            `json:"total_nodes"`
	TotalEdges      int            `json:"total_edges"`
	CellCount       int            `json:"cell_count"`
	UnitCount       int            `json:"unit_count"`
	LayerNodeCounts map[string]int `json:"layer_node_counts"`
}

// CloudGraphResponse is the topology envelope returned by `/open/memory/v1/graph`.
type CloudGraphResponse struct {
	Success      bool               `json:"success"`
	Version      string             `json:"version"`
	TenantID     string             `json:"tenant_id,omitempty"`
	Scope        string             `json:"scope,omitempty"`
	UserID       string             `json:"user_id,omitempty"`
	Layers       []string           `json:"layers"`
	LayerWeights map[string]float64 `json:"layer_weights"`
	Nodes        []CloudGraphNode   `json:"nodes"`
	Edges        []CloudGraphEdge   `json:"edges"`
	Stats        CloudGraphStats    `json:"stats"`
}

// LocalResolvedGraphNode pairs a CloudGraphNode with locally resolved plaintext from the client's SQLite Vault.
type LocalResolvedGraphNode struct {
	CloudGraphNode
	Summary            string `json:"summary"`
	RawPlaintext       string `json:"raw_plaintext,omitempty"`
	DistilledPlaintext string `json:"distilled_plaintext,omitempty"`
}

// LocalResolvedGraph holds the complete client-resolved three-layer cognitive graph.
type LocalResolvedGraph struct {
	Layers       []string                 `json:"layers"`
	LayerWeights map[string]float64       `json:"layer_weights"`
	Nodes        []LocalResolvedGraphNode `json:"nodes"`
	Edges        []CloudGraphEdge         `json:"edges"`
	Stats        CloudGraphStats          `json:"stats"`
}

// LocalNoeticRecord represents a downstream-maintained L3 Noetic memory record (Query + Retrieved Sentences + Answer)
// stored in the client's own `thin_vault_noetic` table.
type LocalNoeticRecord struct {
	NoeticID       string   `json:"noetic_id"`
	OccurredAt     int64    `json:"occurred_at"`
	Status         string   `json:"status"` // "active" | "stale"
	QueryText      string   `json:"query_text"`
	AnswerText     string   `json:"answer_text"`
	CitedUnitIDs   []string `json:"cited_unit_ids"`
	CitedSentences []string `json:"cited_sentences"`
	MatchScore     float64  `json:"match_score,omitempty"`
	AddressURI     string   `json:"address_uri,omitempty"`
	Coord64        int64    `json:"coord64,omitempty"`
	WaveformBar    string   `json:"waveform_bar,omitempty"`
	// AssistantID / IncarnationID pin the crystallizing persona and its lifetime instance;
	// empty values fall back to the vault identity at save time.
	AssistantID   string `json:"assistant_id,omitempty"`
	IncarnationID string `json:"incarnation_id,omitempty"`
}

// LocalResolvedHit pairs the Cloud Blind Brain's numerical ranking with the locally resolved plaintext!
type LocalResolvedHit struct {
	CloudQueryHit
	RawPlaintext       string   `json:"raw_plaintext"`
	DistilledPlaintext string   `json:"distilled_plaintext"`
	KeptSentences      []string `json:"kept_sentences"`
	SourceUnavailable  bool     `json:"source_unavailable,omitempty"`
	// Authors lists the speakers recorded in the unit, in first-appearance order. Episodic turns
	// carry role tags (`[user] …`, `[assistant] …`); documents carry none. Memory reports who spoke
	// verbatim and never guesses — an empty list means "no speaker attribution".
	Authors []string `json:"authors,omitempty"`
}

// RoleTaggedAuthors extracts the role tags a unit carries, in first-appearance order, from lines
// shaped `[role] body`. Pure surface parse: no word list, no guessing, no inference — a unit without
// role tags (a document chunk, a bare note) simply reports no speaker.
func RoleTaggedAuthors(text string) []string {
	var out []string
	seen := make(map[string]bool, 4)
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "[") {
			continue
		}
		end := strings.Index(trimmed, "]")
		if end <= 1 || strings.TrimSpace(trimmed[end+1:]) == "" {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(trimmed[1:end]))
		if role == "" || seen[role] {
			continue
		}
		seen[role] = true
		out = append(out, role)
	}
	return out
}

type vaultCachedDocSlots struct {
	rawText    string
	termCounts map[uint64]int
	sentHashes [][]uint64
	docLen     int
	// allHashes keeps every packed atom hash (unfiltered) so the local transport can
	// score a document without re-running the segmenter on each query.
	allHashes []uint64
	// pktSlots caches packed BlindTokenAtom slices for L3 Noetic handles.
	pktSlots []BlindTokenAtom
}

type vaultDocEntry struct {
	docID         string
	layer         string
	occ           int64
	addrURI       string
	coord64       uint64
	waveBar       string
	locus         string
	refPointer    string
	contentDigest []byte
	termCounts    map[uint64]int
	sentHashes    [][]uint64
	allHashes     []uint64
	docLen        int
}

type vaultCorpusSnapshot struct {
	docCnt           int64
	docRowSum        int64
	maxRowID         int64
	docTextLen       int64
	episodicRevision int64
	episodicCodecKey string
	bgCnt            int64
	bgMaxRowID       int64
	bgOccSum         int64
	bgSlotsLen       int64
	inlineCount      int
	totalDocLen      int
	docs             []vaultDocEntry
	episodicDocs     []vaultDocEntry
	validEpisodicIDs map[string]struct{}
	docIdxByID       map[string]int
	ftsRowIDToDocID  map[int64]string
	globalDF         map[uint64]int
	postingsMu       sync.Mutex
	postings         map[uint64][]int
	// postingsComplete marks postings as the full inverted index built at
	// snapshot assembly (every hash with df>0 has its ascending-doc-index list).
	// Lookups then skip the lazy O(N·|terms|) corpus scan; a missing hash means
	// df==0. When false, queryPostings falls back to the lazy scan fill.
	postingsComplete bool
	effDF            map[uint64]int
	zhaoOnce         sync.Once
	zhaoMu           sync.Mutex
	zhaoEngine       *rm.Engine
}

func (s *vaultCorpusSnapshot) queryEffDF(h uint64, posting []int, totalDocs int, occAt func(int) int64, termsAt func(int) map[uint64]int) int {
	if s == nil {
		return len(posting)
	}
	s.postingsMu.Lock()
	if s.effDF == nil {
		s.effDF = make(map[uint64]int, 256)
	}
	if val, ok := s.effDF[h]; ok {
		s.postingsMu.Unlock()
		return val
	}
	s.postingsMu.Unlock()

	computed := SubmodularCloneFoldedDF(posting, len(posting), totalDocs, occAt, termsAt)

	s.postingsMu.Lock()
	s.effDF[h] = computed
	s.postingsMu.Unlock()
	return computed
}

func (s *vaultCorpusSnapshot) queryPostings(qHashes map[uint64]uint16) map[uint64][]int {
	if s == nil || len(qHashes) == 0 {
		return nil
	}
	s.postingsMu.Lock()
	defer s.postingsMu.Unlock()
	if s.postingsComplete {
		// Full inverted index: direct lookups, absent hash truly has df==0.
		out := make(map[uint64][]int, len(qHashes))
		for h := range qHashes {
			if list, ok := s.postings[h]; ok {
				out[h] = list
			}
		}
		return out
	}
	if s.postings == nil {
		s.postings = make(map[uint64][]int, 256)
	}
	var missing []uint64
	for h := range qHashes {
		if s.globalDF[h] <= 0 {
			continue
		}
		if _, ok := s.postings[h]; !ok {
			missing = append(missing, h)
			s.postings[h] = make([]int, 0, s.globalDF[h])
		}
	}
	if len(missing) > 0 {
		if len(s.docs) >= 2048 {
			const workers = 8
			type partHits struct {
				lists [][]int
			}
			var parts [workers]partHits
			step := (len(s.docs) + workers - 1) / workers
			var wg sync.WaitGroup
			for w := 0; w < workers; w++ {
				start := w * step
				if start >= len(s.docs) {
					break
				}
				end := start + step
				if end > len(s.docs) {
					end = len(s.docs)
				}
				wg.Add(1)
				go func(wIdx, st, en int) {
					defer wg.Done()
					localLists := make([][]int, len(missing))
					for idx := st; idx < en; idx++ {
						tc := s.docs[idx].termCounts
						if len(tc) == 0 {
							continue
						}
						for mIdx, h := range missing {
							if tc[h] > 0 {
								localLists[mIdx] = append(localLists[mIdx], idx)
							}
						}
					}
					parts[wIdx].lists = localLists
				}(w, start, end)
			}
			wg.Wait()
			for mIdx, h := range missing {
				dst := s.postings[h]
				for w := 0; w < workers; w++ {
					if len(parts[w].lists) > mIdx && len(parts[w].lists[mIdx]) > 0 {
						dst = append(dst, parts[w].lists[mIdx]...)
					}
				}
				s.postings[h] = dst
			}
		} else {
			for idx, d := range s.docs {
				tc := d.termCounts
				if len(tc) == 0 {
					continue
				}
				for _, h := range missing {
					if tc[h] > 0 {
						s.postings[h] = append(s.postings[h], idx)
					}
				}
			}
		}
	}
	out := make(map[uint64][]int, len(qHashes))
	for h := range qHashes {
		if list := s.postings[h]; len(list) > 0 {
			out[h] = list
		}
	}
	return out
}

// CorpusSize returns the number of documents in the current cached corpus snapshot (0 if unloaded).
func (v *Vault) CorpusSize() int {
	if v == nil || v.slotCacheMu == nil || v.snapRef == nil {
		return 0
	}
	v.slotCacheMu.RLock()
	defer v.slotCacheMu.RUnlock()
	if v.snapRef.snap == nil {
		return 0
	}
	return len(v.snapRef.snap.docs)
}

// Vault manages the local SQLite plaintext table, the built-in FTS5 trigram lexical-temporal index
// (Client SDK Default Recall), and the user's local secret HMAC key.
type Vault struct {
	db        *sql.DB
	secretKey [32]byte
	hasFTS5   bool
	// identity is the local persona coordinate stamped onto every saved row: the answering
	// assistant, its concrete lifetime incarnation, and the workspace locus. Set once by the
	// caller (`SetIdentity`); empty values are stored as '' and never block a write.
	identity VaultIdentity
	// segOpts holds the optional custom word segmenter and adaptive harmonic configuration.
	segOpts  SegmenterPackOptions
	harmOpts ramesdk.CanonicalHarmonicOptions

	slotCacheMu *sync.RWMutex
	slotCache   map[string]vaultCachedDocSlots
	// snapRef is shared by the Vault and every clone of it: requests get a per-call clone
	// (persona identity must stay independent), so the corpus snapshot has to live behind a
	// shared handle or each request would re-pack the whole vault.
	snapRef *vaultSnapshotRef
	// bgRef is the same idea for the pre-packed L1 background chunks: their atoms are
	// stored once and matched in place, so a query must not re-read the blobs.
	bgRef *vaultBgSlotsRef
}

// vaultSnapshotRef holds the corpus snapshot behind a pointer shared across clones.
// Reads and writes happen under the owning Vault's slotCacheMu, which clones share too.
type vaultSnapshotRef struct {
	buildMu         sync.Mutex
	refreshing      bool
	faultRefreshing bool
	snap            *vaultCorpusSnapshot
	// snapWriteGen counts same-process vault document writes; snapVerifiedGen records
	// the write generation the published snapshot was last fingerprint-verified
	// against. A gap between the two sends the next load through
	// buildCorpusSnapshot's exact fingerprint re-check without discarding the
	// snapshot itself, so the pure-INSERT episodic delta path and the @ref:-only
	// inline fast path stay reachable after same-process writes (a nil snap would
	// force the full table rescan instead).
	snapWriteGen    uint64
	snapVerifiedGen uint64
	// bgRevReliable reports that the thin_vault_corpus_revision bg triggers are
	// installed, so an unchanged bg_rev can gate out the O(column) bg fingerprint
	// aggregate. docs_rev needs no flag: its triggers install with the table.
	bgRevReliable bool
	// lastDocsRev / lastBgRev are the revision counters the current snapshot's
	// fingerprints were computed against; inlineDocsRev / bgCachedRev anchor the
	// component caches the same way. The four-value aggregate fingerprints are
	// blind to length-preserving UPDATEs (COUNT, SUM(rowid), MAX(rowid) and
	// SUM(LENGTH) all unchanged), so when the trigger-backed counters are
	// readable they are the primary identity and the aggregates only confirm.
	lastDocsRev      int64
	lastBgRev        int64
	inlineDocsRev    int64
	bgCachedRev      int64
	lastSnapTime     time.Time
	inlineLoaded     bool
	inlineDocCnt     int64
	inlineDocRowSum  int64
	inlineMaxRowID   int64
	inlineDocTextLen int64
	inlineCodecKey   string
	inlineTotalLen   int
	inlineDocs       []vaultDocEntry
	epiLoaded        bool
	epiCodecKey      string
	epiLastRev       int64
	epiLastCnt       int64
	epiLastMaxRowID  int64
	epiDocCnt        int64
	epiDocMaxRowID   int64
	epiTotalLen      int
	epiDocs          []vaultDocEntry
	epiByMAC         map[string]vaultDocEntry
	bgLoaded         bool
	bgCachedCnt      int64
	bgCachedMaxRowID int64
	bgCachedOccSum   int64
	bgCachedSlotsLen int64
	bgTotalLen       int
	bgDocs           []vaultDocEntry
	bgBaseDF         map[uint64]int
	faultReady       bool
	lastFaultTime    time.Time
	faultDocCnt      int64
	faultDocRowSum   int64
	faultMaxRowID    int64
	faultDocTextLen  int64
	faultEpisodicRev int64
	faultIdxCnt      int64
	faultCodecKey    string
	faultReport      EpisodicIndexFaultReport
	boundScope       string
	noeticReady      bool
	noeticRefreshing bool
	lastNoeticTime   time.Time
	noeticCnt        int64
	noeticMaxRowID   int64
	noeticOccSum     int64
	activeNoetics    []LocalNoeticRecord
	lastDataVersion  int64
	lastDataVerCheck time.Time
}

// vaultBgSlotsRef caches the background chunk atoms so a local query scans one resident
// copy instead of re-reading and re-decoding `thin_vault_bg_chunks` on every call.
type vaultBgSlotsRef struct {
	loaded     bool
	cnt        int64
	maxRowID   int64
	totalBytes int64
	rows       []vaultBgSlotRow
}

// vaultBgSlotRow is one background chunk in its stored, still-packed form.
type vaultBgSlotRow struct {
	unitID  string
	occ     int64
	addrURI string
	waveBar string
	coord64 uint64
	slots   []byte
}

// scheduleSnapshotRefresh re-validates the corpus snapshot in the background
// right after a same-process vault write, so the next foreground recall finds a
// freshly fingerprint-verified snapshot instead of paying the re-check (and, for
// non-append writes, the full rebuild) inline. Single-flight via snapRef.refreshing;
// a short debounce coalesces bursts of consecutive writes into one rebuild.
func (v *Vault) scheduleSnapshotRefresh() {
	if v == nil || v.db == nil || v.slotCacheMu == nil || v.snapRef == nil {
		return
	}
	v.slotCacheMu.Lock()
	if v.snapRef.refreshing {
		v.slotCacheMu.Unlock()
		return
	}
	v.snapRef.refreshing = true
	v.slotCacheMu.Unlock()
	go func() {
		defer func() {
			v.slotCacheMu.Lock()
			v.snapRef.refreshing = false
			v.slotCacheMu.Unlock()
		}()
		time.Sleep(120 * time.Millisecond)
		_, _ = v.buildCorpusSnapshot(context.Background())
	}()
}

func (v *Vault) clearSlotCache() {
	if v != nil && v.slotCacheMu != nil {
		v.slotCacheMu.Lock()
		v.slotCache = make(map[string]vaultCachedDocSlots, 256)
		if v.snapRef != nil {
			v.snapRef.snap = nil
			v.snapRef.inlineLoaded = false
			v.snapRef.epiLoaded = false
			v.snapRef.bgLoaded = false
			v.snapRef.faultReady = false
			v.snapRef.noeticReady = false
		}
		if v.bgRef != nil {
			*v.bgRef = vaultBgSlotsRef{}
		}
		v.slotCacheMu.Unlock()
	}
	clearBackgroundChunkCaches()
}

// InvalidateRuntimeCaches marks the in-memory corpus, Noetic, fault, and live pointer/chunk
// caches as stale so the next query immediately re-verifies SQLite table fingerprints
// (`docCnt/docRowSum/docTextLen`, `episodicRevision`, `bgCnt/bgMaxRowID/bgOccSum/bgSlotsLen`,
// and `noeticCnt/noeticMaxRowID/noeticOccSum`) while preserving content-addressed slot encodings
// (`slotCache[docID]` verified against `rawText` and `epiByMAC` verified against HMAC).
func (v *Vault) InvalidateRuntimeCaches() {
	if v != nil && v.slotCacheMu != nil {
		v.slotCacheMu.Lock()
		for k := range v.slotCache {
			if strings.HasPrefix(k, "@zptr:") || strings.HasPrefix(k, "@jptr:") || strings.HasPrefix(k, "@unavail_doc:") {
				delete(v.slotCache, k)
			}
		}
		if v.snapRef != nil {
			v.snapRef.lastSnapTime = time.Time{}
			v.snapRef.faultReady = false
			v.snapRef.noeticReady = false
		}
		if v.bgRef != nil {
			*v.bgRef = vaultBgSlotsRef{}
		}
		v.slotCacheMu.Unlock()
	}
	clearBackgroundChunkCaches()
}

// checkExternalDBMutation probes SQLite's connection-local `PRAGMA data_version`, which
// the SQLite pager increments whenever any other connection or process commits a write
// to the database file. Probing is throttled to at most once every 150ms on warm
// snapshots and costs a single in-memory header read (<5us, zero disk I/O). When a
// cross-process write is detected, it expires the 5-minute snapshot timer so
// `buildCorpusSnapshot` and `queryAndCacheActiveNoeticRecords` immediately re-check
// their exact table fingerprints without discarding unchanged layer caches.
func (v *Vault) checkExternalDBMutation() bool {
	if v == nil || v.db == nil || v.slotCacheMu == nil || v.snapRef == nil {
		return false
	}
	now := time.Now()
	v.slotCacheMu.RLock()
	lastCheck := v.snapRef.lastDataVerCheck
	prevVer := v.snapRef.lastDataVersion
	v.slotCacheMu.RUnlock()
	if !lastCheck.IsZero() && now.Sub(lastCheck) < 150*time.Millisecond {
		return false
	}
	var curVer int64
	if err := v.db.QueryRow(`PRAGMA data_version`).Scan(&curVer); err != nil || curVer <= 0 {
		return false
	}
	v.slotCacheMu.Lock()
	v.snapRef.lastDataVerCheck = now
	if prevVer == 0 {
		v.snapRef.lastDataVersion = curVer
		v.slotCacheMu.Unlock()
		return false
	}
	if curVer == v.snapRef.lastDataVersion {
		v.slotCacheMu.Unlock()
		return false
	}
	v.snapRef.lastDataVersion = curVer
	v.snapRef.lastSnapTime = time.Time{}
	if v.snapRef.snap == nil || len(v.snapRef.snap.docs) < 2048 {
		v.snapRef.faultReady = false
	}
	v.snapRef.noeticReady = false
	v.slotCacheMu.Unlock()
	return true
}

// docSlotHashes returns every packed atom hash of one document, reusing the shared slot
// cache so the local transport never re-runs the segmenter for the same text.
func (v *Vault) docSlotHashes(docID, rawText string) []uint64 {
	if v == nil {
		return nil
	}
	if v.slotCacheMu != nil {
		v.slotCacheMu.RLock()
		if entry, ok := v.slotCache[docID]; ok && entry.rawText == rawText && entry.allHashes != nil {
			hashes := entry.allHashes
			v.slotCacheMu.RUnlock()
			return hashes
		}
		v.slotCacheMu.RUnlock()
	}
	pkt, _ := v.PackBlindPacket(docID, rawText, 0)
	hashes := make([]uint64, 0, len(pkt.Slots))
	for _, slot := range pkt.Slots {
		hashes = append(hashes, slot.H)
	}
	if v.slotCacheMu != nil {
		v.slotCacheMu.Lock()
		entry := v.slotCache[docID]
		entry.rawText = rawText
		entry.allHashes = hashes
		v.slotCache[docID] = entry
		v.slotCacheMu.Unlock()
	}
	return hashes
}

// noeticBlindPacket returns the packed blind pulse for a Noetic handle, caching its
// slots in `slotCache` so repeated `RecallNoetic` calls never re-run BPE harmonic packing.
func (v *Vault) noeticBlindPacket(noeticID, queryText string, occurredAt int64) BlindPulsePacket {
	if v == nil {
		return BlindPulsePacket{UnitID: noeticID, OccurredAt: occurredAt}
	}
	cacheKey := "@noetic:" + noeticID
	if v.slotCacheMu != nil {
		v.slotCacheMu.RLock()
		if entry, ok := v.slotCache[cacheKey]; ok && entry.rawText == queryText && entry.pktSlots != nil {
			slots := entry.pktSlots
			v.slotCacheMu.RUnlock()
			return BlindPulsePacket{UnitID: noeticID, OccurredAt: occurredAt, Slots: slots}
		}
		v.slotCacheMu.RUnlock()
	}
	pkt, _ := v.PackBlindPacket(noeticID, queryText, occurredAt)
	slots := pkt.Slots
	if slots == nil {
		slots = []BlindTokenAtom{}
	}
	if v.slotCacheMu != nil {
		v.slotCacheMu.Lock()
		if v.slotCache == nil {
			v.slotCache = make(map[string]vaultCachedDocSlots, 256)
		}
		entry := v.slotCache[cacheKey]
		entry.rawText = queryText
		entry.pktSlots = slots
		v.slotCache[cacheKey] = entry
		v.slotCacheMu.Unlock()
	}
	return pkt
}

// DocAtomCoverageWithCueHashes computes the fraction of uniqueCueHashes present in docID,
// reusing the in-memory corpus snapshot's `termCounts` / `allHashes` (or `slotCache`) so
// post-recall coverage and Noetic citation selection never re-run BPE harmonic packing
// across 30 hydrated long documents on the foreground thread.
func (v *Vault) DocAtomCoverageWithCueHashes(docID, fallbackText string, uniqueCueHashes []uint64) float64 {
	if v == nil || len(uniqueCueHashes) == 0 {
		return 0
	}
	if v.slotCacheMu != nil && v.snapRef != nil {
		v.slotCacheMu.RLock()
		snap := v.snapRef.snap
		v.slotCacheMu.RUnlock()
		if snap != nil {
			if idx, ok := snap.docIdxByID[docID]; ok && idx >= 0 && idx < len(snap.docs) {
				doc := snap.docs[idx]
				var allSet map[uint64]bool
				if len(doc.allHashes) > 0 {
					allSet = make(map[uint64]bool, len(doc.allHashes))
					for _, h := range doc.allHashes {
						allSet[h] = true
					}
				}
				matched := 0
				for _, h := range uniqueCueHashes {
					if doc.termCounts[h] > 0 || (allSet != nil && allSet[h]) {
						matched++
					}
				}
				return float64(matched) / float64(len(uniqueCueHashes))
			}
		}
	}
	if strings.TrimSpace(fallbackText) == "" {
		return 0
	}
	hashes := v.docSlotHashes(docID, fallbackText)
	if len(hashes) == 0 {
		return 0
	}
	seen := make(map[uint64]bool, len(hashes))
	for _, h := range hashes {
		seen[h] = true
	}
	matched := 0
	for _, h := range uniqueCueHashes {
		if seen[h] {
			matched++
		}
	}
	return float64(matched) / float64(len(uniqueCueHashes))
}

// backgroundSlotRows returns the stored background chunks, loaded once per fingerprint.
// The fingerprint covers inserts, deletes and slot rewrites of the chunk table only, so
// unrelated writes (episodic imports, Noetic crystallization) keep the cache hot.
func (v *Vault) backgroundSlotRows() []vaultBgSlotRow {
	if v == nil || v.db == nil {
		return nil
	}
	var cnt, maxRowID, totalBytes int64
	if err := v.db.QueryRow(
		`SELECT COUNT(*), COALESCE(MAX(rowid), 0), COALESCE(SUM(LENGTH(slots_json)), 0) FROM thin_vault_bg_chunks`,
	).Scan(&cnt, &maxRowID, &totalBytes); err != nil {
		return nil
	}
	if v.slotCacheMu != nil && v.bgRef != nil {
		v.slotCacheMu.RLock()
		if v.bgRef.loaded && v.bgRef.cnt == cnt && v.bgRef.maxRowID == maxRowID && v.bgRef.totalBytes == totalBytes {
			rows := v.bgRef.rows
			v.slotCacheMu.RUnlock()
			return rows
		}
		v.slotCacheMu.RUnlock()
	}
	rows, err := v.db.Query(`SELECT unit_id, occurred_at, address_uri, coord64, waveform_bar, slots_json FROM thin_vault_bg_chunks`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	stored := make([]vaultBgSlotRow, 0, cnt)
	for rows.Next() {
		var row vaultBgSlotRow
		var coord64 int64
		if err := rows.Scan(&row.unitID, &row.occ, &row.addrURI, &coord64, &row.waveBar, &row.slots); err != nil {
			continue
		}
		row.coord64 = uint64(coord64)
		stored = append(stored, row)
	}
	if v.slotCacheMu != nil && v.bgRef != nil {
		v.slotCacheMu.Lock()
		*v.bgRef = vaultBgSlotsRef{loaded: true, cnt: cnt, maxRowID: maxRowID, totalBytes: totalBytes, rows: stored}
		v.slotCacheMu.Unlock()
	}
	return stored
}

// CloneWithSharedSlotCache forks a request-local Vault: plaintext storage,
// key material and the synchronized blind-slot cache are shared, while the
// persona identity starts empty and remains independent on every clone.
func (v *Vault) CloneWithSharedSlotCache() *Vault {
	if v == nil {
		return nil
	}
	return &Vault{
		db: v.db, secretKey: v.secretKey, hasFTS5: v.hasFTS5, segOpts: v.segOpts, harmOpts: v.harmOpts,
		slotCacheMu: v.slotCacheMu, slotCache: v.slotCache, snapRef: v.snapRef, bgRef: v.bgRef,
	}
}

// VaultIdentity is the local persona coordinate persisted alongside each plaintext row.
type VaultIdentity struct {
	AssistantID   string
	IncarnationID string
	Locus         string
}

// SetIdentity records the persona coordinate used by subsequent local saves, so a shared
// workspace with unlimited parallel incarnations keeps every row attributable.
func (v *Vault) SetIdentity(id VaultIdentity) {
	if v == nil {
		return
	}
	v.identity = VaultIdentity{
		AssistantID:   strings.TrimSpace(id.AssistantID),
		IncarnationID: strings.TrimSpace(id.IncarnationID),
		Locus:         strings.TrimSpace(id.Locus),
	}
}

// Identity returns the persona coordinate currently stamped onto local saves.
func (v *Vault) Identity() VaultIdentity {
	if v == nil {
		return VaultIdentity{}
	}
	return v.identity
}

// episodicLineageDDL records every provenance occurrence of a content-addressed
// unit: identical speech is ingested once (one blind pulse), but each turn it was
// said in stays queryable.
const episodicLineageDDL = `
CREATE TABLE IF NOT EXISTS thin_vault_episodic_lineage (
	turn_ref TEXT NOT NULL,
	doc_id TEXT NOT NULL,
	occurred_at INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (turn_ref, doc_id)
);
CREATE INDEX IF NOT EXISTS idx_thin_vault_episodic_lineage_doc ON thin_vault_episodic_lineage(doc_id);
CREATE TABLE IF NOT EXISTS thin_vault_deleted_units (
	unit_id TEXT PRIMARY KEY,
	layer TEXT NOT NULL DEFAULT '',
	deleted_at INTEGER NOT NULL DEFAULT 0
);`

// RecordDeletedUnits records user-deleted unit IDs as tombstones so background
// directory sync and harness dialogue reconciliation do not re-import them.
func (v *Vault) RecordDeletedUnits(layer string, unitIDs ...string) {
	if v == nil || v.db == nil || len(unitIDs) == 0 {
		return
	}
	_, _ = v.db.Exec(`CREATE TABLE IF NOT EXISTS thin_vault_deleted_units (unit_id TEXT PRIMARY KEY, layer TEXT NOT NULL DEFAULT '', deleted_at INTEGER NOT NULL DEFAULT 0)`)
	now := time.Now().Unix()
	lyr := strings.ToLower(strings.TrimSpace(layer))
	for _, id := range unitIDs {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			_, _ = v.db.Exec(`INSERT OR REPLACE INTO thin_vault_deleted_units(unit_id, layer, deleted_at) VALUES (?, ?, ?)`, trimmed, lyr, now)
		}
	}
}

// ClearDeletedUnits removes tombstones for units that are explicitly re-added.
func (v *Vault) ClearDeletedUnits(unitIDs ...string) {
	if v == nil || v.db == nil || len(unitIDs) == 0 {
		return
	}
	for _, id := range unitIDs {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			_, _ = v.db.Exec(`DELETE FROM thin_vault_deleted_units WHERE unit_id = ?`, trimmed)
		}
	}
}

// DeletedUnitSet returns the set of tombstoned unit IDs (filtered by layer when non-empty).
func (v *Vault) DeletedUnitSet(layer string) map[string]bool {
	if v == nil || v.db == nil {
		return nil
	}
	lyr := strings.ToLower(strings.TrimSpace(layer))
	var rows *sql.Rows
	var err error
	if lyr == "" {
		rows, err = v.db.Query(`SELECT unit_id FROM thin_vault_deleted_units`)
	} else {
		rows, err = v.db.Query(`SELECT unit_id FROM thin_vault_deleted_units WHERE layer = ? OR layer = ''`, lyr)
	}
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make(map[string]bool)
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil && strings.TrimSpace(id) != "" {
			out[strings.TrimSpace(id)] = true
		}
	}
	return out
}

// NewVault initializes the local SQLite tables (`thin_vault_meta` and `thin_vault_docs`)
// and loads or generates the user's 256-bit local secret key (which never leaves the machine).
// corpusRevisionDDL mirrors the episodic-revision trigger pattern for the two
// remaining fingerprinted tables. Row-level triggers live in the database file,
// so every writer process bumps them; an unchanged revision therefore proves no
// INSERT/UPDATE/DELETE touched the table since the published snapshot, letting
// buildCorpusSnapshot skip that table's O(N) aggregate fingerprint scan
// (SUM(LENGTH(slots_json)) over thin_vault_bg_chunks reads the whole packed
// column: ~305MB on a grown vault, 18-650ms per build depending on page cache).
const corpusRevisionDDL = `CREATE TABLE IF NOT EXISTS thin_vault_corpus_revision (id INTEGER PRIMARY KEY CHECK (id = 1), docs_rev INTEGER NOT NULL DEFAULT 0, bg_rev INTEGER NOT NULL DEFAULT 0);
INSERT OR IGNORE INTO thin_vault_corpus_revision(id, docs_rev, bg_rev) VALUES (1, 0, 0);
CREATE TRIGGER IF NOT EXISTS thin_docs_rev_insert AFTER INSERT ON thin_vault_docs BEGIN UPDATE thin_vault_corpus_revision SET docs_rev = docs_rev + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS thin_docs_rev_update AFTER UPDATE ON thin_vault_docs BEGIN UPDATE thin_vault_corpus_revision SET docs_rev = docs_rev + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS thin_docs_rev_delete AFTER DELETE ON thin_vault_docs BEGIN UPDATE thin_vault_corpus_revision SET docs_rev = docs_rev + 1 WHERE id = 1; END;`

const corpusBgRevisionDDL = `CREATE TRIGGER IF NOT EXISTS thin_bg_rev_insert AFTER INSERT ON thin_vault_bg_chunks BEGIN UPDATE thin_vault_corpus_revision SET bg_rev = bg_rev + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS thin_bg_rev_update AFTER UPDATE ON thin_vault_bg_chunks BEGIN UPDATE thin_vault_corpus_revision SET bg_rev = bg_rev + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS thin_bg_rev_delete AFTER DELETE ON thin_vault_bg_chunks BEGIN UPDATE thin_vault_corpus_revision SET bg_rev = bg_rev + 1 WHERE id = 1; END;`

// ensureCorpusRevisionSchema installs the revision table and triggers. The bg
// trigger trio is only installed when thin_vault_bg_chunks already exists
// (background schema is created lazily); bgReliable reports whether bg_rev is
// trustworthy for gating. When it is not, the bg aggregate fingerprint keeps
// running exactly as before, so correctness never depends on the gate.
func ensureCorpusRevisionSchema(db *sql.DB) (bgReliable bool) {
	if _, err := db.Exec(corpusRevisionDDL); err != nil {
		return false
	}
	var hasBG int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'thin_vault_bg_chunks'`).Scan(&hasBG); err != nil || hasBG == 0 {
		return false
	}
	if _, err := db.Exec(corpusBgRevisionDDL); err != nil {
		return false
	}
	return true
}

func NewVault(db *sql.DB, explicitSecret []byte) (*Vault, error) {
	if db == nil {
		return nil, errors.New("thinclient: nil sql.DB")
	}
	if len(explicitSecret) == 0 {
		var stored []byte
		var storageType string
		if err := db.QueryRow(`SELECT v, typeof(v) FROM thin_vault_meta WHERE k = 'user_secret_key'`).Scan(&stored, &storageType); err == nil && storageType == "blob" && len(stored) == 32 {
			var readyCnt int
			_ = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name IN ('thin_vault_docs', 'thin_vault_noetic', 'thin_vault_docs_fts')`).Scan(&readyCnt)
			if readyCnt == 3 {
				var hasLineage int
				if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name IN ('thin_vault_episodic_lineage', 'thin_vault_deleted_units')`).Scan(&hasLineage); err != nil || hasLineage < 2 {
					_, _ = db.Exec(episodicLineageDDL)
				}
				if err := initEpisodicIndex(db); err != nil {
					return nil, err
				}
				ensureInsertedAtColumns(db)
				_ = migrateBackgroundSlotBlobs(db)
				_ = EnsureZhaoRMSQLiteSchema(db)
				ZhaoRMSchemaContracts()
				bgRevReliable := ensureCorpusRevisionSchema(db)
				v := &Vault{
					db: db, hasFTS5: ensureFTS(db),
					slotCacheMu: &sync.RWMutex{},
					slotCache:   make(map[string]vaultCachedDocSlots, 256),
					snapRef:     &vaultSnapshotRef{bgRevReliable: bgRevReliable},
					bgRef:       &vaultBgSlotsRef{},
				}
				copy(v.secretKey[:], stored)
				return v, nil
			}
		}
	}
	ddl := `
	CREATE TABLE IF NOT EXISTS thin_vault_meta (
		k TEXT PRIMARY KEY,
		v BLOB NOT NULL
	);
				CREATE TABLE IF NOT EXISTS thin_vault_docs (
					doc_id TEXT PRIMARY KEY,
					layer TEXT NOT NULL DEFAULT 'episodic',
					occurred_at INTEGER NOT NULL DEFAULT 0,
					inserted_at INTEGER NOT NULL DEFAULT 0,
					raw_text TEXT NOT NULL,
					sentences_json TEXT NOT NULL,
					address_uri TEXT NOT NULL DEFAULT '',
					coord64 INTEGER NOT NULL DEFAULT 0,
					waveform_bar TEXT NOT NULL DEFAULT '',
					doc_macro_wave TEXT NOT NULL DEFAULT '',
					kept_indices_json TEXT NOT NULL DEFAULT '[]',
					assistant_id TEXT NOT NULL DEFAULT '',
					incarnation_id TEXT NOT NULL DEFAULT '',
					locus TEXT NOT NULL DEFAULT ''
				);
					CREATE TABLE IF NOT EXISTS thin_vault_noetic (
						noetic_id TEXT PRIMARY KEY,
						occurred_at INTEGER NOT NULL DEFAULT 0,
						inserted_at INTEGER NOT NULL DEFAULT 0,
						status TEXT NOT NULL DEFAULT 'active',
						query_text TEXT NOT NULL,
						answer_text TEXT NOT NULL,
						cited_unit_ids_json TEXT NOT NULL DEFAULT '[]',
						cited_sentences_json TEXT NOT NULL DEFAULT '[]',
						address_uri TEXT NOT NULL DEFAULT '',
						coord64 INTEGER NOT NULL DEFAULT 0,
						waveform_bar TEXT NOT NULL DEFAULT '',
						assistant_id TEXT NOT NULL DEFAULT '',
						incarnation_id TEXT NOT NULL DEFAULT ''
					);
				CREATE INDEX IF NOT EXISTS idx_thin_vault_noetic_status_ts ON thin_vault_noetic(status, occurred_at DESC);`
	if _, err := db.Exec(ddl); err != nil {
		return nil, err
	}
	if _, err := db.Exec(episodicLineageDDL); err != nil {
		return nil, err
	}
	if err := initEpisodicIndex(db); err != nil {
		return nil, err
	}
	if err := EnsureZhaoRMSQLiteSchema(db); err != nil {
		return nil, err
	}
	ZhaoRMSchemaContracts()
	_, _ = db.Exec(`ALTER TABLE thin_vault_docs ADD COLUMN layer TEXT NOT NULL DEFAULT 'episodic'`)
	for _, stmt := range []string{
		`ALTER TABLE thin_vault_docs ADD COLUMN assistant_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE thin_vault_docs ADD COLUMN incarnation_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE thin_vault_docs ADD COLUMN locus TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE thin_vault_noetic ADD COLUMN assistant_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE thin_vault_noetic ADD COLUMN incarnation_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE thin_vault_noetic ADD COLUMN address_uri TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE thin_vault_noetic ADD COLUMN coord64 INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE thin_vault_noetic ADD COLUMN waveform_bar TEXT NOT NULL DEFAULT ''`,
	} {
		_, _ = db.Exec(stmt)
	}
	ensureInsertedAtColumns(db)
	// Contentless FTS5 index (postings only) + rowid↔doc_id map: the local lexical
	// cache never keeps a second readable copy of any background/episodic text.
	hasFTS := ensureFTS(db)
	// Legacy JSON slot rows are rewritten into the packed form once (idempotent).
	_ = migrateBackgroundSlotBlobs(db)
	bgRevReliable := ensureCorpusRevisionSchema(db)

	v := &Vault{
		db: db, hasFTS5: hasFTS,
		slotCacheMu: &sync.RWMutex{},
		slotCache:   make(map[string]vaultCachedDocSlots, 256),
		snapRef:     &vaultSnapshotRef{bgRevReliable: bgRevReliable},
		bgRef:       &vaultBgSlotsRef{},
	}
	if len(explicitSecret) > 0 {
		h := sha256.Sum256(ramesdk.DeriveBlindPepper(explicitSecret))
		v.secretKey = h
		return v, nil
	}

	var stored []byte
	var storageType string
	err := db.QueryRow(`SELECT v, typeof(v) FROM thin_vault_meta WHERE k = 'user_secret_key'`).Scan(&stored, &storageType)
	if errors.Is(err, sql.ErrNoRows) {
		var fresh [32]byte
		if _, err := rand.Read(fresh[:]); err != nil {
			return nil, err
		}
		if _, err := db.Exec(`INSERT OR IGNORE INTO thin_vault_meta(k, v) VALUES ('user_secret_key', ?)`, fresh[:]); err != nil {
			return nil, err
		}
		err = db.QueryRow(`SELECT v, typeof(v) FROM thin_vault_meta WHERE k = 'user_secret_key'`).Scan(&stored, &storageType)
	}
	if err != nil {
		return nil, err
	}
	if storageType != "blob" || len(stored) != len(v.secretKey) {
		return nil, fmt.Errorf("invalid vault user secret key format")
	}
	copy(v.secretKey[:], stored)
	return v, nil
}

// ensureInsertedAtColumns idempotently adds `inserted_at` (first-ingest timestamp) to
// `thin_vault_docs`, `thin_vault_noetic`, and `thin_vault_bg_chunks`, and ensures
// secondary time/layer indexes exist with a read-only O(1) `sqlite_master` probe so
// steady-state opens take zero write locks.
func ensureInsertedAtColumns(db *sql.DB) {
	if db == nil {
		return
	}
	var docsHas, noeticHas, bgTable, bgHas, idxCnt int
	_ = db.QueryRow(`
		SELECT
			COALESCE(SUM(CASE WHEN type = 'table' AND name = 'thin_vault_docs' AND sql LIKE '%inserted_at%' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN type = 'table' AND name = 'thin_vault_noetic' AND sql LIKE '%inserted_at%' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN type = 'table' AND name = 'thin_vault_bg_chunks' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN type = 'table' AND name = 'thin_vault_bg_chunks' AND sql LIKE '%inserted_at%' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN type = 'index' AND name IN (
				'idx_thin_vault_docs_occurred_at',
				'idx_thin_vault_docs_layer_occurred',
				'idx_thin_vault_docs_inserted_at',
				'idx_thin_vault_noetic_inserted_at',
				'idx_thin_vault_bg_chunks_occurred_at',
				'idx_thin_vault_bg_chunks_inserted_at',
				'idx_thin_vault_bg_chunks_meta'
			) THEN 1 ELSE 0 END), 0)
		FROM sqlite_master
		WHERE name IN (
			'thin_vault_docs', 'thin_vault_noetic', 'thin_vault_bg_chunks',
			'idx_thin_vault_docs_occurred_at',
			'idx_thin_vault_docs_layer_occurred',
			'idx_thin_vault_docs_inserted_at',
			'idx_thin_vault_noetic_inserted_at',
			'idx_thin_vault_bg_chunks_occurred_at',
			'idx_thin_vault_bg_chunks_inserted_at',
			'idx_thin_vault_bg_chunks_meta'
		)
	`).Scan(&docsHas, &noeticHas, &bgTable, &bgHas, &idxCnt)
	wantIdx := 4
	if bgTable == 1 {
		wantIdx = 7
	}
	if docsHas == 1 && noeticHas == 1 && (bgTable == 0 || bgHas == 1) && idxCnt >= wantIdx {
		return
	}
	if docsHas == 0 {
		_, _ = db.Exec(`ALTER TABLE thin_vault_docs ADD COLUMN inserted_at INTEGER NOT NULL DEFAULT 0`)
		_, _ = db.Exec(`UPDATE thin_vault_docs SET inserted_at = occurred_at WHERE inserted_at = 0 AND occurred_at > 0`)
	}
	if noeticHas == 0 {
		_, _ = db.Exec(`ALTER TABLE thin_vault_noetic ADD COLUMN inserted_at INTEGER NOT NULL DEFAULT 0`)
		_, _ = db.Exec(`UPDATE thin_vault_noetic SET inserted_at = occurred_at WHERE inserted_at = 0 AND occurred_at > 0`)
	}
	if bgTable == 1 && bgHas == 0 {
		_, _ = db.Exec(`ALTER TABLE thin_vault_bg_chunks ADD COLUMN inserted_at INTEGER NOT NULL DEFAULT 0`)
		_, _ = db.Exec(`
			UPDATE thin_vault_bg_chunks
			SET inserted_at = COALESCE(
				(SELECT MIN(c2.occurred_at) FROM thin_vault_bg_chunks c2 WHERE c2.abs_path = thin_vault_bg_chunks.abs_path AND c2.occurred_at > 0),
				occurred_at
			)
			WHERE inserted_at = 0 AND occurred_at > 0
		`)
	}
	_, _ = db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_thin_vault_docs_occurred_at ON thin_vault_docs(occurred_at DESC, doc_id ASC);
		CREATE INDEX IF NOT EXISTS idx_thin_vault_docs_layer_occurred ON thin_vault_docs(layer, occurred_at DESC, doc_id ASC);
		CREATE INDEX IF NOT EXISTS idx_thin_vault_docs_inserted_at ON thin_vault_docs(inserted_at DESC);
		CREATE INDEX IF NOT EXISTS idx_thin_vault_noetic_inserted_at ON thin_vault_noetic(inserted_at DESC);
	`)
	if bgTable == 1 {
		_, _ = db.Exec(`
			CREATE INDEX IF NOT EXISTS idx_thin_vault_bg_chunks_occurred_at ON thin_vault_bg_chunks(occurred_at DESC, unit_id ASC);
			CREATE INDEX IF NOT EXISTS idx_thin_vault_bg_chunks_inserted_at ON thin_vault_bg_chunks(inserted_at DESC);
			CREATE INDEX IF NOT EXISTS idx_thin_vault_bg_chunks_meta ON thin_vault_bg_chunks(occurred_at DESC, unit_id ASC, rel_path, anchor, abs_path, address_uri);
		`)
	}
}

// LegacyRetiredTables identifies historical tables that must be preserved.
var LegacyRetiredTables = []string{
	"assertion_ext",
	"participant",
	"restate",
	"node_ext",
	"node_vector",
	"episode_member",
	"episode",
	"memory_hot",
	"noetic",
	"recall_decision_cache",
	"activation",
	"sync_state",
	"replay_state",
	"vector",
	"event",
	"meta_edge",
	"meta_edge_v2",
	"mention",
	"assertion",
	"node_alias",
	"node_fts",
	"node",
	"trace_fts",
	"trace",
}

// PurgeLegacyTables preserves historical tables. Automatic deletion is unsafe.
func PurgeLegacyTables(_ *sql.DB) {}

// KeyedHash64 computes a cryptographic HMAC-SHA256 truncated to 64 bits using the vault's private key via ramesdk.
func (v *Vault) KeyedHash64(token string) uint64 {
	return ramesdk.NewBlindEncoderFromRawKey(v.secretKey).KeyedHash64(token)
}

// mechanicalCarrierUnits delegates character carrier width measurement to ramesdk.CarrierUnits.
func mechanicalCarrierUnits(r rune) uint16 {
	return ramesdk.CarrierUnits(r)
}

// SplitSentences delegates sentence boundary segmentation to ramesdk.SplitSentences.
func SplitSentences(text string) []string {
	return ramesdk.SplitSentences(text)
}

// SplitWords delegates language-agnostic surface word and CJK dual-track segmentation to ramesdk.SplitWords.
func SplitWords(sent string) []string {
	return ramesdk.SplitWords(sent)
}

// PackBlindPacket splits text into sentences and packs a zero-plaintext BlindPulsePacket via ramesdk.
func (v *Vault) PackBlindPacket(docID string, text string, occurredAt int64) (BlindPulsePacket, []string) {
	if v != nil && (v.segOpts.Segmenter != nil || v.segOpts.SentenceAtomPacker != nil) {
		return v.PackBlindPacketWithSegmenter(docID, text, occurredAt, v.segOpts)
	}
	var key [32]byte
	if v != nil {
		key = v.secretKey
	}
	enc := ramesdk.NewBlindEncoderFromRawKey(key)
	if v != nil {
		enc.SetCanonicalHarmonics(v.harmOpts)
	}
	sdkPkt, sents := enc.PackBlindPacket(docID, text, occurredAt)
	return BlindPulsePacket{
		UnitID:         sdkPkt.UnitID,
		OccurredAt:     sdkPkt.OccurredAt,
		Slots:          sdkPkt.Slots,
		SentenceFrames: sdkPkt.SentenceFrames,
	}, sents
}

// SaveLocalPlaintext persists the document's plaintext and cloud-assigned metadata in local SQLite.
func (v *Vault) SaveLocalPlaintext(docID string, occurredAt int64, rawText string, sents []string, resp CloudIngestResponse) error {
	sentsJSON, err := json.Marshal(sents)
	if err != nil {
		return err
	}
	keptJSON, err := json.Marshal(resp.KeptSentenceIndices)
	if err != nil {
		return err
	}
	layer := strings.ToLower(strings.TrimSpace(resp.Layer))
	if layer != "background" && layer != "noetic" {
		layer = "episodic"
	}
	_, err = v.db.Exec(`
			INSERT INTO thin_vault_docs(
				doc_id, layer, occurred_at, inserted_at, raw_text, sentences_json, address_uri, coord64, waveform_bar, doc_macro_wave, kept_indices_json,
				assistant_id, incarnation_id, locus
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(doc_id) DO UPDATE SET
				layer = excluded.layer,
				occurred_at = excluded.occurred_at,
				inserted_at = CASE WHEN thin_vault_docs.inserted_at > 0 THEN thin_vault_docs.inserted_at ELSE excluded.inserted_at END,
				raw_text = excluded.raw_text,
				sentences_json = excluded.sentences_json,
				address_uri = excluded.address_uri,
				coord64 = excluded.coord64,
				waveform_bar = excluded.waveform_bar,
				doc_macro_wave = excluded.doc_macro_wave,
				kept_indices_json = excluded.kept_indices_json,
				assistant_id = excluded.assistant_id,
				incarnation_id = excluded.incarnation_id,
				locus = excluded.locus`,
		docID, layer, occurredAt, occurredAt, rawText, string(sentsJSON),
		resp.AddressURI, int64(resp.AddressCoord64), resp.WaveformBar, resp.DocMacroWaveform, string(keptJSON),
		v.identity.AssistantID, v.identity.IncarnationID, v.identity.Locus,
	)
	if err == nil && v.slotCacheMu != nil {
		v.slotCacheMu.Lock()
		delete(v.slotCache, docID)
		if v.snapRef != nil {
			v.snapRef.snapWriteGen++
		}
		v.slotCacheMu.Unlock()
		v.scheduleSnapshotRefresh()
	}
	if err != nil {
		return err
	}
	if v.hasFTS5 {
		// The FTS mirror is the recall candidate leg; a silent failure would leave the
		// unit permanently unfindable, so the write is reported and retried by callers.
		if ftsErr := ftsReplaceRow(v.db, docID, rawText); ftsErr != nil {
			return fmt.Errorf("thinclient: FTS mirror write for %s: %w", docID, ftsErr)
		}
	}
	return nil
}

// SaveLocalEpisodicPointer stores ONLY a zero-copy pointer (`@ref:zcode:...` or `@ref:jsonl:...`)
// and the cloud-assigned C6 cell address / waveform in `thin_vault_docs`, without duplicating the
// Harness's conversation plaintext into SQLite. `turnRef` records the provenance of the unit (a
// content-addressed unit can be said in several turns; the row keeps one pointer, the lineage
// table keeps every occurrence).
func (v *Vault) SaveLocalEpisodicPointer(docID string, occurredAt int64, refPointer, turnRef string, resp CloudIngestResponse) error {
	keptJSON, err := json.Marshal(resp.KeptSentenceIndices)
	if err != nil {
		return err
	}
	_, err = v.db.Exec(`
			INSERT INTO thin_vault_docs(
				doc_id, layer, occurred_at, inserted_at, raw_text, sentences_json, address_uri, coord64, waveform_bar, doc_macro_wave, kept_indices_json,
				assistant_id, incarnation_id, locus
			) VALUES (?, 'episodic', ?, ?, ?, '[]', ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(doc_id) DO UPDATE SET
				layer = excluded.layer,
				occurred_at = excluded.occurred_at,
				inserted_at = CASE WHEN thin_vault_docs.inserted_at > 0 THEN thin_vault_docs.inserted_at ELSE excluded.inserted_at END,
				raw_text = excluded.raw_text,
				sentences_json = excluded.sentences_json,
				address_uri = excluded.address_uri,
				coord64 = excluded.coord64,
				waveform_bar = excluded.waveform_bar,
				doc_macro_wave = excluded.doc_macro_wave,
				kept_indices_json = excluded.kept_indices_json,
				assistant_id = excluded.assistant_id,
				incarnation_id = excluded.incarnation_id,
				locus = excluded.locus`,
		docID, occurredAt, occurredAt, strings.TrimSpace(refPointer),
		resp.AddressURI, int64(resp.AddressCoord64), resp.WaveformBar, resp.DocMacroWaveform, string(keptJSON),
		v.identity.AssistantID, v.identity.IncarnationID, v.identity.Locus,
	)
	if err == nil && v.slotCacheMu != nil {
		v.slotCacheMu.Lock()
		delete(v.slotCache, docID)
		if v.snapRef != nil {
			v.snapRef.snapWriteGen++
		}
		v.slotCacheMu.Unlock()
		v.scheduleSnapshotRefresh()
	}
	if err != nil {
		return err
	}
	if v.hasFTS5 {
		if ftsErr := ftsDeleteRow(v.db, docID); ftsErr != nil {
			return fmt.Errorf("thinclient: FTS mirror delete for %s: %w", docID, ftsErr)
		}
	}
	_ = v.RecordEpisodicLineage(turnRef, docID, occurredAt)
	return nil
}

// RecordEpisodicLineage records one provenance occurrence of a content-addressed
// unit (same speech → one unit, many turns). Idempotent per (turn_ref, doc_id).
func (v *Vault) RecordEpisodicLineage(turnRef, docID string, occurredAt int64) error {
	if v == nil || v.db == nil {
		return nil
	}
	turnRef = strings.TrimSpace(turnRef)
	docID = strings.TrimSpace(docID)
	if turnRef == "" || docID == "" {
		return nil
	}
	_, err := v.db.Exec(
		`INSERT OR IGNORE INTO thin_vault_episodic_lineage(turn_ref, doc_id, occurred_at) VALUES (?, ?, ?)`,
		turnRef, docID, occurredAt,
	)
	return err
}

// LoadLocalDocumentLayer retrieves the stored cognitive layer ("background" | "episodic" | "noetic") for docID.
func (v *Vault) LoadLocalDocumentLayer(docID string) string {
	if strings.HasPrefix(docID, "bg:") {
		return LayerBackground
	}
	var layer string
	if err := v.db.QueryRow(`SELECT layer FROM thin_vault_docs WHERE doc_id = ?`, docID).Scan(&layer); err == nil && strings.TrimSpace(layer) != "" {
		return strings.ToLower(strings.TrimSpace(layer))
	}
	return LayerEpisodic
}

func uniqueTrimmedIDs(docIDs []string) []string {
	out := make([]string, 0, len(docIDs))
	seen := make(map[string]bool, len(docIDs))
	for _, id := range docIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func inPlaceholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// DocLoci returns the workspace locus (`thin_vault_docs.locus`, stamped at ingest from the
// repository directory name) for each docID that carries one; ids with no row or an empty
// locus are absent from the result. It feeds the retrieval-scope anchor's delivery ordering
// (the `[<workspace>]` cue head) and never widens any other read path.
func (v *Vault) DocLoci(docIDs []string) map[string]string {
	if v == nil || v.db == nil || len(docIDs) == 0 {
		return nil
	}
	uniq := uniqueTrimmedIDs(docIDs)
	if len(uniq) == 0 {
		return nil
	}
	loci := make(map[string]string, len(uniq))
	var missing []string
	if v.slotCacheMu != nil && v.snapRef != nil {
		v.slotCacheMu.RLock()
		snap := v.snapRef.snap
		if snap != nil && len(snap.docs) >= 2048 {
			for _, id := range uniq {
				if dIdx, ok := snap.docIdxByID[id]; ok && dIdx >= 0 && dIdx < len(snap.docs) {
					if loc := snap.docs[dIdx].locus; loc != "" {
						loci[id] = loc
					}
					continue
				}
				missing = append(missing, id)
			}
			v.slotCacheMu.RUnlock()
			if len(missing) == 0 {
				return loci
			}
			uniq = missing
		} else {
			v.slotCacheMu.RUnlock()
		}
	}
	const batchSize = 256
	for start := 0; start < len(uniq); start += batchSize {
		end := start + batchSize
		if end > len(uniq) {
			end = len(uniq)
		}
		chunk := uniq[start:end]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := v.db.Query(`SELECT doc_id, locus FROM thin_vault_docs WHERE doc_id IN (`+inPlaceholders(len(chunk))+`)`, args...)
		if err != nil {
			continue
		}
		for rows.Next() {
			var id, locus string
			if rows.Scan(&id, &locus) == nil {
				if locus = strings.TrimSpace(locus); locus != "" {
					loci[id] = locus
				}
			}
		}
		rows.Close()
	}
	return loci
}

// DocTimes returns the stored `occurred_at` Unix timestamp (seconds) for each docID that
// carries a positive timestamp in `thin_vault_docs`, `thin_vault_bg_chunks`, or `thin_vault_noetic`.
// Zero or missing timestamps are omitted so callers never fabricate a date.
func (v *Vault) DocTimes(docIDs []string) map[string]int64 {
	if v == nil || v.db == nil || len(docIDs) == 0 {
		return nil
	}
	uniq := uniqueTrimmedIDs(docIDs)
	if len(uniq) == 0 {
		return nil
	}
	times := make(map[string]int64, len(uniq))
	var missing []string
	if v.slotCacheMu != nil && v.snapRef != nil {
		v.slotCacheMu.RLock()
		snap := v.snapRef.snap
		noetics := v.snapRef.activeNoetics
		if snap != nil && len(snap.docs) >= 2048 {
			for _, id := range uniq {
				if dIdx, ok := snap.docIdxByID[id]; ok && dIdx >= 0 && dIdx < len(snap.docs) {
					if occ := snap.docs[dIdx].occ; occ > 0 {
						times[id] = occ
					}
					continue
				}
				foundNoetic := false
				for i := range noetics {
					if noetics[i].NoeticID == id {
						if noetics[i].OccurredAt > 0 {
							times[id] = noetics[i].OccurredAt
						}
						foundNoetic = true
						break
					}
				}
				if !foundNoetic {
					missing = append(missing, id)
				}
			}
			v.slotCacheMu.RUnlock()
			if len(missing) == 0 {
				return times
			}
			uniq = missing
		} else {
			v.slotCacheMu.RUnlock()
		}
	}
	const batchSize = 256
	for start := 0; start < len(uniq); start += batchSize {
		end := start + batchSize
		if end > len(uniq) {
			end = len(uniq)
		}
		chunk := uniq[start:end]
		args := make([]any, len(chunk))
		var bgArgs []any
		for i, id := range chunk {
			args[i] = id
			if strings.HasPrefix(id, "bg:") {
				bgArgs = append(bgArgs, id)
			}
		}
		ph := inPlaceholders(len(chunk))
		if rows, err := v.db.Query(`SELECT doc_id, occurred_at FROM thin_vault_docs WHERE doc_id IN (`+ph+`)`, args...); err == nil {
			for rows.Next() {
				var id string
				var occ int64
				if rows.Scan(&id, &occ) == nil && occ > 0 {
					times[id] = occ
				}
			}
			rows.Close()
		}
		if len(bgArgs) > 0 {
			if rows, err := v.db.Query(`SELECT unit_id, occurred_at FROM thin_vault_bg_chunks WHERE unit_id IN (`+inPlaceholders(len(bgArgs))+`)`, bgArgs...); err == nil {
				for rows.Next() {
					var id string
					var occ int64
					if rows.Scan(&id, &occ) == nil && occ > 0 && times[id] <= 0 {
						times[id] = occ
					}
				}
				rows.Close()
			}
		}
		if len(times) < len(chunk) {
			if rows, err := v.db.Query(`SELECT noetic_id, occurred_at FROM thin_vault_noetic WHERE noetic_id IN (`+ph+`)`, args...); err == nil {
				for rows.Next() {
					var id string
					var occ int64
					if rows.Scan(&id, &occ) == nil && occ > 0 && times[id] <= 0 {
						times[id] = occ
					}
				}
				rows.Close()
			}
		}
	}
	return times
}

// LoadLocalDocument retrieves the locally stored plaintext and cloud-distilled dry sentences for docID
// (supporting `background` mounted directory files read LIVE on-demand from disk via `thin_vault_bg_chunks`,
// zero-copy `episodic` pointers `@ref:...` read LIVE on-demand from the Harness DB/JSONL,
// `episodic`/`background` in `thin_vault_docs`, and local `noetic` records in `thin_vault_noetic`).
func (v *Vault) LoadLocalDocument(docID string) (rawText string, distilledText string, keptSents []string, err error) {
	if strings.HasPrefix(docID, "bg:") {
		if raw, dist, kept, bgErr := v.loadBackgroundChunkLive(docID); bgErr == nil {
			return raw, dist, kept, nil
		}
	}
	if v.slotCacheMu != nil && v.snapRef != nil {
		v.slotCacheMu.RLock()
		snap := v.snapRef.snap
		var fastPtr string
		var matchedNoetic *LocalNoeticRecord
		cachedUnavail := false
		if snap != nil && len(snap.docs) >= 2048 {
			if dIdx, ok := snap.docIdxByID[docID]; ok && dIdx >= 0 && dIdx < len(snap.docs) {
				fastPtr = snap.docs[dIdx].refPointer
			} else {
				for i := range v.snapRef.activeNoetics {
					if v.snapRef.activeNoetics[i].NoeticID == docID {
						recCopy := v.snapRef.activeNoetics[i]
						matchedNoetic = &recCopy
						break
					}
				}
				if matchedNoetic == nil && v.slotCache != nil {
					if entry, ok := v.slotCache["@unavail_doc:"+docID]; ok && entry.docLen < 0 {
						cachedUnavail = true
					}
				}
			}
		}
		v.slotCacheMu.RUnlock()
		if cachedUnavail {
			return "", "", nil, errors.New("thinclient: cached unavailable local document")
		}
		if matchedNoetic != nil {
			combined := strings.TrimSpace("Q: " + matchedNoetic.QueryText + "\nA: " + matchedNoetic.AnswerText)
			citedSents := matchedNoetic.CitedSentences
			if len(citedSents) == 0 {
				citedSents = []string{combined}
			}
			return combined, combined, citedSents, nil
		}
		if strings.HasPrefix(fastPtr, "@ref:") {
			liveText, rErr := v.LoadVerifiedEpisodicPointer(docID, fastPtr)
			if rErr != nil {
				return "", "", nil, rErr
			}
			allSents := SplitSentences(liveText)
			if len(allSents) == 0 {
				allSents = []string{liveText}
			}
			return liveText, liveText, allSents, nil
		}
	}
	var sentsJSON, keptJSON string
	err = v.db.QueryRow(`SELECT raw_text, sentences_json, kept_indices_json FROM thin_vault_docs WHERE doc_id = ?`, docID).
		Scan(&rawText, &sentsJSON, &keptJSON)
	if err != nil {
		if raw, dist, kept, bgErr := v.loadBackgroundChunkLive(docID); bgErr == nil {
			return raw, dist, kept, nil
		}
		var qTxt, aTxt, citedSentsJSON string
		nErr := v.db.QueryRow(`SELECT query_text, answer_text, cited_sentences_json FROM thin_vault_noetic WHERE noetic_id = ? AND status = 'active'`, docID).
			Scan(&qTxt, &aTxt, &citedSentsJSON)
		if nErr != nil {
			if v.slotCacheMu != nil && v.CorpusSize() >= 2048 {
				v.slotCacheMu.Lock()
				if v.slotCache != nil {
					v.slotCache["@unavail_doc:"+docID] = vaultCachedDocSlots{docLen: -1}
				}
				v.slotCacheMu.Unlock()
			}
			return "", "", nil, err
		}
		var citedSents []string
		_ = json.Unmarshal([]byte(citedSentsJSON), &citedSents)
		combined := strings.TrimSpace("Q: " + qTxt + "\nA: " + aTxt)
		if len(citedSents) == 0 {
			citedSents = []string{combined}
		}
		return combined, combined, citedSents, nil
	}
	var keptIdx []int
	_ = json.Unmarshal([]byte(keptJSON), &keptIdx)

	// Pointer writers store no sentences. Nonempty inline sentence evidence is
	// usable only when neither an index nor lineage asserts a conflicting source.
	var inlineSents []string
	inlineEvidence := json.Unmarshal([]byte(sentsJSON), &inlineSents) == nil && len(inlineSents) > 0
	for _, sent := range inlineSents {
		inlineEvidence = inlineEvidence && sent != "" && strings.Contains(rawText, sent)
	}
	if strings.HasPrefix(strings.TrimSpace(rawText), "@ref:") && inlineEvidence {
		var indexed int
		if err := v.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM thin_vault_episodic_index WHERE doc_id = ?) OR EXISTS(SELECT 1 FROM thin_vault_episodic_lineage WHERE doc_id = ? AND turn_ref = ?)`, docID, docID, rawText).Scan(&indexed); err != nil {
			return "", "", nil, err
		}
		if indexed != 0 {
			return "", "", nil, fmt.Errorf("thinclient: ambiguous episodic document identity for %s", docID)
		}
	}
	if strings.HasPrefix(strings.TrimSpace(rawText), "@ref:") && !inlineEvidence {
		liveText, rErr := v.LoadVerifiedEpisodicPointer(docID, strings.TrimSpace(rawText))
		if rErr != nil {
			if v.slotCacheMu != nil && v.CorpusSize() >= 2048 {
				v.slotCacheMu.Lock()
				if v.slotCache != nil {
					v.slotCache["@unavail_doc:"+docID] = vaultCachedDocSlots{docLen: -1}
				}
				v.slotCacheMu.Unlock()
			}
			return "", "", nil, rErr
		}
		rawText = liveText
		allSents := SplitSentences(rawText)
		if len(allSents) == 0 {
			allSents = []string{rawText}
		}
		dist, kept := ReconstructDistilled(rawText, allSents, keptIdx)
		return rawText, dist, kept, nil
	}

	var allSents []string
	_ = json.Unmarshal([]byte(sentsJSON), &allSents)
	if len(allSents) == 0 {
		allSents = SplitSentences(rawText)
	}
	dist, kept := ReconstructDistilled(rawText, allSents, keptIdx)
	return rawText, dist, kept, nil
}

// EpisodicPointerFor returns the zero-copy harness pointer stored for one L2 unit
// ("" when the unit keeps its plaintext locally). Delivery uses it to find the
// turn a segment belongs to.
func (v *Vault) EpisodicPointerFor(docID string) string {
	if v == nil || v.db == nil || strings.TrimSpace(docID) == "" {
		return ""
	}
	if v.slotCacheMu != nil && v.snapRef != nil {
		v.slotCacheMu.RLock()
		snap := v.snapRef.snap
		if snap != nil && len(snap.docs) >= 2048 {
			if dIdx, ok := snap.docIdxByID[docID]; ok && dIdx >= 0 && dIdx < len(snap.docs) {
				if ptr := snap.docs[dIdx].refPointer; strings.HasPrefix(ptr, "@ref:") {
					v.slotCacheMu.RUnlock()
					return ptr
				}
			}
		}
		v.slotCacheMu.RUnlock()
	}
	var raw string
	if err := v.db.QueryRow(`SELECT raw_text FROM thin_vault_docs WHERE doc_id = ?`, docID).Scan(&raw); err != nil {
		return ""
	}
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "@ref:") {
		return ""
	}
	return raw
}

// LoadEpisodicTurnSegments resolves the ordered speech segments of one harness turn
// as rendered `[role] text` lines. The reference is a turn-level pointer; a `#<k>`
// segment suffix (or `#<k>.<c>` chunk suffix) is stripped and ignored. Enumeration is
// delegated to `internal/episodic`, so the window a caller renders is exactly the
// segmentation the pulses were built from.
func LoadEpisodicTurnSegments(turnRef string) ([]string, error) {
	trimmed := strings.TrimSpace(turnRef)
	if index := strings.LastIndex(trimmed, "#"); index > 0 {
		trimmed = trimmed[:index]
	}
	if strings.HasPrefix(trimmed, "@ref:zcode:") {
		rest := strings.TrimPrefix(trimmed, "@ref:zcode:")
		parts := strings.SplitN(rest, ":", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return nil, fmt.Errorf("invalid zcode turn reference %q", turnRef)
		}
		zdb, err := openZCodeReadOnly()
		if err != nil {
			return nil, err
		}
		segments, err := episodic.ZCodeTurnSegments(zdb, parts[0], parts[1])
		if err != nil {
			return nil, err
		}
		return renderEpisodicSegments(segments), nil
	}
	if strings.HasPrefix(trimmed, "@ref:jsonl:") {
		rest := strings.TrimPrefix(trimmed, "@ref:jsonl:")
		lastColon := strings.LastIndex(rest, ":")
		if lastColon <= 0 {
			return nil, fmt.Errorf("invalid jsonl turn reference %q", turnRef)
		}
		turnIndex, err := strconv.Atoi(strings.TrimSpace(rest[lastColon+1:]))
		if err != nil || turnIndex < 0 {
			return nil, fmt.Errorf("invalid jsonl turn index in %q", turnRef)
		}
		segments, err := episodic.JSONLTurnSegments(rest[:lastColon], turnIndex)
		if err != nil {
			return nil, err
		}
		return renderEpisodicSegments(segments), nil
	}
	return nil, fmt.Errorf("unsupported turn reference %q", turnRef)
}

// renderEpisodicSegments renders segments as `[role] text` in index order.
func renderEpisodicSegments(segments []episodic.Segment) []string {
	out := make([]string, 0, len(segments))
	for _, segment := range segments {
		out = append(out, "["+segment.Role+"] "+segment.Text)
	}
	return out
}

// ResolveEpisodicPointerLive resolves a zero-copy episodic pointer LIVE on-demand
// from the underlying Harness database or JSONL transcript on disk.
//
// Three forms are valid:
//   - `@ref:zcode:<session>:<msg_id>#<k>` / `@ref:jsonl:<abs_path>:<turn_idx>#<k>`
//     names one speech segment (the L2 unit is a segment, not a whole turn);
//   - `@ref:...#<k>.<c>` names chunk c of one segment: a long segment is split at
//     sentence boundaries under the same tokenz-r budget the ingest used, so the
//     split is deterministic and resolves to the chunk it was built from;
//   - `@ref:zcode:<session>:<msg_id>` / `@ref:jsonl:<abs_path>:<turn_idx>` names
//     the whole turn (legacy turn-level rows written before segment units).
//
// Segment enumeration is delegated to `internal/episodic`, the same package the
// ingest scanner uses, so `#<k>` always resolves to the segment it was built from.
func ResolveEpisodicPointerLive(refPointer string) (string, error) {
	trimmed := strings.TrimSpace(refPointer)
	if strings.HasPrefix(trimmed, "@ref:zcode:") {
		rest := strings.TrimPrefix(trimmed, "@ref:zcode:")
		parts := strings.SplitN(rest, ":", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return "", fmt.Errorf("invalid zcode pointer %q", refPointer)
		}
		msgID, segmentIndex, chunkIndex, hasSegment, hasChunk := splitPointerSegment(parts[1])
		if hasSegment {
			return resolveZCodeSegmentLive(parts[0], msgID, segmentIndex, chunkIndex, hasChunk)
		}
		return resolveZCodeTurnLive(parts[0], parts[1])
	}
	if strings.HasPrefix(trimmed, "@ref:jsonl:") {
		rest := strings.TrimPrefix(trimmed, "@ref:jsonl:")
		lastColon := strings.LastIndex(rest, ":")
		if lastColon <= 0 {
			return "", fmt.Errorf("invalid jsonl pointer %q", refPointer)
		}
		absPath := rest[:lastColon]
		turnStr, segmentIndex, chunkIndex, hasSegment, hasChunk := splitPointerSegment(rest[lastColon+1:])
		if hasSegment {
			return resolveJSONLSegmentLive(absPath, turnStr, segmentIndex, chunkIndex, hasChunk)
		}
		return resolveJSONLTurnLive(absPath, turnStr)
	}
	return trimmed, nil
}

// splitPointerSegment splits an optional suffix off a pointer tail:
//   - `#<k>` names segment k of the turn (the whole segment);
//   - `#<k>.<c>` names chunk c of segment k (long segments are re-split live under
//     the same deterministic tokenz-r budget the ingest used).
func splitPointerSegment(tail string) (rest string, segmentIndex, chunkIndex int, hasSegment, hasChunk bool) {
	index := strings.LastIndex(tail, "#")
	if index <= 0 || index == len(tail)-1 {
		return tail, 0, 0, false, false
	}
	suffix := tail[index+1:]
	segmentPart, chunkPart := suffix, ""
	if dot := strings.Index(suffix, "."); dot >= 0 {
		segmentPart, chunkPart = suffix[:dot], suffix[dot+1:]
	}
	segment, err := strconv.Atoi(segmentPart)
	if err != nil || segment < 0 {
		return tail, 0, 0, false, false
	}
	if chunkPart == "" {
		return tail[:index], segment, 0, true, false
	}
	chunk, err := strconv.Atoi(chunkPart)
	if err != nil || chunk < 0 {
		return tail, 0, 0, false, false
	}
	return tail[:index], segment, chunk, true, true
}

// episodicSegmentChunk returns the whole rendered segment when the pointer carries
// no chunk suffix, else the chunk at chunkIndex of the deterministic re-split.
func episodicSegmentChunk(rendered string, segmentIndex, chunkIndex int, hasChunk bool, source string) (string, error) {
	if !hasChunk {
		return rendered, nil
	}
	chunks := episodicunit.Chunk(rendered, episodicunit.TokenBudget)
	if chunkIndex < 0 || chunkIndex >= len(chunks) {
		return "", fmt.Errorf("thinclient: chunk %d out of range for segment %d of %s", chunkIndex, segmentIndex, source)
	}
	return chunks[chunkIndex], nil
}

type zcodeTurnCacheItem struct {
	segs     []episodic.Segment
	complete bool
}

var (
	zcodeTurnSegsMu       sync.RWMutex
	zcodeTurnResolveLocks [32]sync.Mutex
	zcodeTurnSegsCache    = make(map[string]zcodeTurnCacheItem, 128)
)

// resolveZCodeSegmentLive hydrates one speech segment (or one chunk of it) of a ZCode turn.
func resolveZCodeSegmentLive(sessionID, userMsgID string, segmentIndex, chunkIndex int, hasChunk bool) (string, error) {
	home, _ := os.UserHomeDir()
	turnKey := home + "\x00" + sessionID + "\x00" + userMsgID
	zcodeTurnSegsMu.RLock()
	item, cached := zcodeTurnSegsCache[turnKey]
	zcodeTurnSegsMu.RUnlock()
	if !cached || (segmentIndex >= len(item.segs) && !item.complete) {
		var h uint32 = 2166136261
		for i := 0; i < len(turnKey); i++ {
			h = (h ^ uint32(turnKey[i])) * 16777619
		}
		lock := &zcodeTurnResolveLocks[h&31]
		lock.Lock()
		zcodeTurnSegsMu.RLock()
		item, cached = zcodeTurnSegsCache[turnKey]
		zcodeTurnSegsMu.RUnlock()
		if !cached || (segmentIndex >= len(item.segs) && !item.complete) {
			zdb, err := openZCodeReadOnly()
			if err != nil {
				lock.Unlock()
				return "", err
			}
			segs, complete, err := episodic.ZCodeTurnSegmentsUpTo(zdb, sessionID, userMsgID, segmentIndex+1)
			if err != nil {
				lock.Unlock()
				return "", err
			}
			if len(segs) > 0 {
				item = zcodeTurnCacheItem{segs: segs, complete: complete}
				zcodeTurnSegsMu.Lock()
				if len(zcodeTurnSegsCache) >= 8192 {
					for k := range zcodeTurnSegsCache {
						delete(zcodeTurnSegsCache, k)
						if len(zcodeTurnSegsCache) <= 6144 {
							break
						}
					}
				}
				zcodeTurnSegsCache[turnKey] = item
				zcodeTurnSegsMu.Unlock()
			}
		}
		lock.Unlock()
	}
	if segmentIndex >= len(item.segs) {
		return "", fmt.Errorf("thinclient: segment %d out of range for %s", segmentIndex, userMsgID)
	}
	rendered := "[" + item.segs[segmentIndex].Role + "] " + item.segs[segmentIndex].Text
	return episodicSegmentChunk(rendered, segmentIndex, chunkIndex, hasChunk, userMsgID)
}

// openZCodeReadOnly resolves the shared read-only ZCode session handle.
func openZCodeReadOnly() (*sql.DB, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil, errors.New("thinclient: cannot resolve user home directory")
	}
	zdbPath := filepath.Join(home, ".zcode", "cli", "db", "db.sqlite")
	if _, err := os.Stat(zdbPath); err != nil {
		return nil, err
	}
	return zcodeReadHandle(zdbPath)
}

var (
	jsonlTurnSegsMu    sync.RWMutex
	jsonlTurnSegsCache = make(map[string][]episodic.Segment, 128)
)

// resolveJSONLSegmentLive hydrates one speech segment (or one chunk of it) of a JSONL turn.
func resolveJSONLSegmentLive(absPath, turnStr string, segmentIndex, chunkIndex int, hasChunk bool) (string, error) {
	targetTurn, err := strconv.Atoi(strings.TrimSpace(turnStr))
	if err != nil || targetTurn < 0 {
		return "", fmt.Errorf("invalid turn index %q", turnStr)
	}
	var segments []episodic.Segment
	if st, statErr := os.Stat(absPath); statErr == nil && st.Size() >= 65536 {
		cacheKey := fmt.Sprintf("%s\x00%d\x00%d\x00%d", absPath, st.Size(), st.ModTime().UnixNano(), targetTurn)
		jsonlTurnSegsMu.RLock()
		cachedSegs, ok := jsonlTurnSegsCache[cacheKey]
		jsonlTurnSegsMu.RUnlock()
		if ok {
			segments = cachedSegs
		} else {
			segments, err = episodic.JSONLTurnSegments(absPath, targetTurn)
			if err != nil {
				return "", err
			}
			jsonlTurnSegsMu.Lock()
			if len(jsonlTurnSegsCache) >= 256 {
				jsonlTurnSegsCache = make(map[string][]episodic.Segment, 128)
			}
			jsonlTurnSegsCache[cacheKey] = segments
			jsonlTurnSegsMu.Unlock()
		}
	} else {
		segments, err = episodic.JSONLTurnSegments(absPath, targetTurn)
		if err != nil {
			return "", err
		}
	}
	if segmentIndex >= len(segments) {
		return "", fmt.Errorf("thinclient: segment %d out of range for %s", segmentIndex, absPath)
	}
	rendered := "[" + segments[segmentIndex].Role + "] " + segments[segmentIndex].Text
	return episodicSegmentChunk(rendered, segmentIndex, chunkIndex, hasChunk, absPath)
}

// RecentDoc is one time-ordered vault entry (the `recent` recall primitive): a
// stored document row (L1/L2) or a crystallized Noetic record (L3), with its
// layer label and persona/time coordinates. The primitive is deliberately
// semantic-free — it orders by time only and never leaves the machine.
type RecentDoc struct {
	ID            string
	Layer         string
	OccurredAt    int64
	Text          string
	AssistantID   string
	IncarnationID string
	Locus         string
}

// RecentDocs returns the newest memory entries across the local plaintext
// vault, newest first: document rows plus active Noetic records. It performs no
// semantic matching and issues no network traffic.
func (v *Vault) RecentDocs(limit int) ([]RecentDoc, error) {
	if limit <= 0 {
		limit = 10
	}
	if v == nil || v.db == nil {
		return nil, nil
	}
	entries := []RecentDoc{}
	rows, err := v.db.Query(
		`SELECT doc_id, layer, occurred_at, raw_text, assistant_id, incarnation_id, locus
		   FROM thin_vault_docs
		  ORDER BY occurred_at DESC, doc_id DESC
		  LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var entry RecentDoc
		if err := rows.Scan(&entry.ID, &entry.Layer, &entry.OccurredAt, &entry.Text,
			&entry.AssistantID, &entry.IncarnationID, &entry.Locus); err != nil {
			rows.Close()
			return nil, err
		}
		entries = append(entries, entry)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Close the result set before identity checks so a single-connection vault can
	// query its index without waiting on its own open cursor.
	for i := range entries {
		v.hydrateRecentPointer(&entries[i])
	}
	noeticRows, err := v.db.Query(
		`SELECT noetic_id, occurred_at, query_text, cited_unit_ids_json, assistant_id, incarnation_id
		   FROM thin_vault_noetic
		  WHERE status = 'active'
		  ORDER BY occurred_at DESC, noetic_id DESC
		  LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	for noeticRows.Next() {
		var id, query, citedJSON, assistant, incarnation string
		var occurredAt int64
		if err := noeticRows.Scan(&id, &occurredAt, &query, &citedJSON, &assistant, &incarnation); err != nil {
			noeticRows.Close()
			return nil, err
		}
		// A Noetic row is a retrieval handle, not evidence: its answer_text is the
		// agent's own past synthesis, which may be arbitrarily long and may predate the
		// current question. The recent view therefore shows the cue plus the units it
		// cites and leaves the answer out, exactly like the recall band projection.
		rendered := "Cue: " + strings.TrimSpace(query)
		var cited []string
		if json.Unmarshal([]byte(citedJSON), &cited) == nil && len(cited) > 0 {
			rendered += "\nCited units: " + strings.Join(cited, ", ")
		}
		entries = append(entries, RecentDoc{
			ID:            id,
			Layer:         "noetic",
			OccurredAt:    occurredAt,
			Text:          rendered,
			AssistantID:   assistant,
			IncarnationID: incarnation,
		})
	}
	noeticRows.Close()
	if err := noeticRows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].OccurredAt != entries[j].OccurredAt {
			return entries[i].OccurredAt > entries[j].OccurredAt
		}
		return entries[i].ID < entries[j].ID
	})
	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, nil
}

// RecentSourceDoc returns the newest stored (non-Noetic) document entry, i.e. a
// source memory rather than a derived retrieval handle. It exists so a time window
// flooded by derived Noetic rows can still show the layer those rows were derived
// from; ordering and identity are the same as RecentDocs.
func (v *Vault) RecentSourceDoc(scan int) (*RecentDoc, error) {
	if scan <= 0 {
		scan = 10
	}
	if v == nil || v.db == nil {
		return nil, nil
	}
	rows, err := v.db.Query(
		`SELECT doc_id, layer, occurred_at, raw_text, assistant_id, incarnation_id, locus
		   FROM thin_vault_docs
		  WHERE LOWER(COALESCE(layer, '')) <> 'noetic'
		  ORDER BY occurred_at DESC, doc_id DESC
		  LIMIT ?`, scan)
	if err != nil {
		return nil, err
	}
	var entry RecentDoc
	if !rows.Next() {
		err := rows.Err()
		rows.Close()
		return nil, err
	}
	if err := rows.Scan(&entry.ID, &entry.Layer, &entry.OccurredAt, &entry.Text,
		&entry.AssistantID, &entry.IncarnationID, &entry.Locus); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	v.hydrateRecentPointer(&entry)
	return &entry, nil
}

func (v *Vault) hydrateRecentPointer(entry *RecentDoc) {
	pointer := strings.TrimSpace(entry.Text)
	if !strings.HasPrefix(pointer, "@ref:") {
		return
	}
	if resolved, err := v.LoadVerifiedEpisodicPointer(entry.ID, pointer); err == nil {
		entry.Text = resolved
	} else {
		entry.Text = "[来源缺口：指针身份未验证或内容不可用]"
	}
}

// SaveLocalNoetic persists a downstream L3 Noetic record (Query + Retrieved Sentences + Answer)
// into the client's own `thin_vault_noetic` table without sending it to the cloud engine.
func (v *Vault) SaveLocalNoetic(rec LocalNoeticRecord) error {
	if strings.TrimSpace(rec.NoeticID) == "" {
		return errors.New("thinclient: noetic_id is required")
	}
	if strings.TrimSpace(rec.QueryText) == "" && strings.TrimSpace(rec.AnswerText) == "" {
		return errors.New("thinclient: query_text or answer_text is required")
	}
	status := strings.TrimSpace(rec.Status)
	if status == "" {
		status = "active"
	}
	// If CitedSentences is empty but CitedUnitIDs are provided, automatically hydrate distilled sentences from thin_vault_docs
	citedSents := rec.CitedSentences
	if len(citedSents) == 0 && len(rec.CitedUnitIDs) > 0 {
		for _, uid := range rec.CitedUnitIDs {
			_, distTxt, _, err := v.LoadLocalDocument(uid)
			if err == nil && strings.TrimSpace(distTxt) != "" {
				citedSents = append(citedSents, distTxt)
			}
		}
	}
	unitIDsJSON, err := json.Marshal(rec.CitedUnitIDs)
	if err != nil {
		return err
	}
	sentsJSON, err := json.Marshal(citedSents)
	if err != nil {
		return err
	}
	_, err = v.db.Exec(`
			INSERT INTO thin_vault_noetic(
				noetic_id, occurred_at, inserted_at, status, query_text, answer_text, cited_unit_ids_json, cited_sentences_json,
				address_uri, coord64, waveform_bar, assistant_id, incarnation_id
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(noetic_id) DO UPDATE SET
				occurred_at = excluded.occurred_at,
				inserted_at = CASE WHEN thin_vault_noetic.inserted_at > 0 THEN thin_vault_noetic.inserted_at ELSE excluded.inserted_at END,
				status = excluded.status,
				query_text = excluded.query_text,
				answer_text = excluded.answer_text,
				cited_unit_ids_json = excluded.cited_unit_ids_json,
				cited_sentences_json = excluded.cited_sentences_json,
				address_uri = excluded.address_uri,
				coord64 = excluded.coord64,
				waveform_bar = excluded.waveform_bar,
				assistant_id = excluded.assistant_id,
				incarnation_id = excluded.incarnation_id`,
		rec.NoeticID, rec.OccurredAt, rec.OccurredAt, status, rec.QueryText, rec.AnswerText, string(unitIDsJSON), string(sentsJSON),
		rec.AddressURI, rec.Coord64, rec.WaveformBar,
		firstNonEmpty(rec.AssistantID, v.identity.AssistantID), firstNonEmpty(rec.IncarnationID, v.identity.IncarnationID),
	)
	if err == nil && v.slotCacheMu != nil && v.snapRef != nil {
		v.slotCacheMu.Lock()
		v.snapRef.noeticReady = false
		v.slotCacheMu.Unlock()
	}
	return err
}

// UpdateLocalNoeticManifold updates the engine-allocated manifold address for an existing Noetic record.
func (v *Vault) UpdateLocalNoeticManifold(noeticID, addressURI string, coord64 int64, waveformBar string) error {
	_, err := v.db.Exec(`
		UPDATE thin_vault_noetic 
		SET address_uri = ?, coord64 = ?, waveform_bar = ? 
		WHERE noetic_id = ?`,
		addressURI, coord64, waveformBar, noeticID,
	)
	if err == nil && v.slotCacheMu != nil && v.snapRef != nil {
		v.slotCacheMu.Lock()
		v.snapRef.noeticReady = false
		v.slotCacheMu.Unlock()
	}
	return err
}

// firstNonEmpty returns the first trimmed non-empty value, else "".
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

type localNoeticExecutor interface {
	Query(query string, args ...any) (*sql.Rows, error)
	Exec(query string, args ...any) (sql.Result, error)
}

// localNoeticCitationIndex is a one-scan inverted view (unit_id -> noetic_id) of the active
// Noetic rows' cited units. Bulk purges invalidate every citing record through the index
// instead of rescanning the whole Noetic table once per removed unit, which used to make a
// mount removal quadratic in the mount size and hold the SQLite write lock for minutes.
type localNoeticCitationIndex struct {
	byUnit map[string][]string
}

func loadLocalNoeticCitationIndex(db localNoeticExecutor) (*localNoeticCitationIndex, error) {
	index := &localNoeticCitationIndex{byUnit: make(map[string][]string, 64)}
	rows, err := db.Query(`SELECT noetic_id, cited_unit_ids_json FROM thin_vault_noetic WHERE status = 'active'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var noeticID, unitIDsJSON string
		if err := rows.Scan(&noeticID, &unitIDsJSON); err != nil {
			return nil, err
		}
		var unitIDs []string
		if err := json.Unmarshal([]byte(unitIDsJSON), &unitIDs); err != nil {
			return nil, fmt.Errorf("thinclient: decode cited units for Noetic %s: %w", noeticID, err)
		}
		for _, unitID := range unitIDs {
			index.byUnit[unitID] = append(index.byUnit[unitID], noeticID)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return index, nil
}

// invalidateCited marks every active Noetic record citing unitID as "stale", at most once per
// record per purge (staled carries the already-marked set of one transaction), and returns the
// records it flipped.
func (idx *localNoeticCitationIndex) invalidateCited(db localNoeticExecutor, unitID string, staled map[string]bool) ([]string, error) {
	var affected []string
	for _, noeticID := range idx.byUnit[unitID] {
		if staled[noeticID] {
			continue
		}
		if _, err := db.Exec(`UPDATE thin_vault_noetic SET status = 'stale' WHERE noetic_id = ?`, noeticID); err != nil {
			return affected, err
		}
		staled[noeticID] = true
		affected = append(affected, noeticID)
	}
	return affected, nil
}

func invalidateLocalNoeticByCitedDoc(db localNoeticExecutor, docID string) ([]string, error) {
	target := strings.TrimSpace(docID)
	if target == "" {
		return nil, nil
	}
	index, err := loadLocalNoeticCitationIndex(db)
	if err != nil {
		return nil, err
	}
	return index.invalidateCited(db, target, make(map[string]bool))
}

// InvalidateLocalNoeticByCitedDoc marks all local Noetic records that cite docID as "stale",
// returning the list of affected noetic_ids.
func (v *Vault) InvalidateLocalNoeticByCitedDoc(docID string) ([]string, error) {
	if v == nil || v.db == nil {
		return nil, errors.New("thinclient: nil Vault db")
	}
	affected, err := invalidateLocalNoeticByCitedDoc(v.db, docID)
	if len(affected) > 0 && v.slotCacheMu != nil && v.snapRef != nil {
		v.slotCacheMu.Lock()
		v.snapRef.noeticReady = false
		v.slotCacheMu.Unlock()
	}
	return affected, err
}

func indexHashUint64(s []uint64, target uint64) int {
	for i, x := range s {
		if x == target {
			return i
		}
	}
	return -1
}

func containsHashUint64(s []uint64, target uint64) bool {
	return indexHashUint64(s, target) >= 0
}

func cloudHitLess(a, b CloudQueryHit) bool {
	if a.FinalWeight != b.FinalWeight {
		return a.FinalWeight > b.FinalWeight
	}
	return a.UnitID < b.UnitID
}

// sortTopCloudHits orders the top k elements of hits in exact (FinalWeight DESC, UnitID ASC)
// order and truncates hits to at most k*2 elements when len(hits) is large, avoiding O(N log N)
// full sorts over tens of thousands of low-weight tail hits that are discarded before fusion.
func sortTopCloudHits(hits []CloudQueryHit, k int) {
	if len(hits) <= k*2 || k <= 0 {
		sort.Slice(hits, func(i, j int) bool {
			return cloudHitLess(hits[i], hits[j])
		})
		return
	}
	left, right := 0, len(hits)-1
	target := k
	for left < right {
		pivotIdx := left + (right-left)/2
		pivot := hits[pivotIdx]
		hits[pivotIdx], hits[right] = hits[right], hits[pivotIdx]
		store := left
		for i := left; i < right; i++ {
			if cloudHitLess(hits[i], pivot) {
				hits[store], hits[i] = hits[i], hits[store]
				store++
			}
		}
		hits[store], hits[right] = hits[right], hits[store]
		if store == target {
			break
		} else if store < target {
			left = store + 1
		} else {
			right = store - 1
		}
	}
	sort.Slice(hits[:k], func(i, j int) bool {
		return cloudHitLess(hits[i], hits[j])
	})
}

// fixedLog2Millibits computes log2(x) * 1024 in pure 64-bit integer arithmetic
// with second-order rational concavity correction (log2(1+m) ≈ m + m*(1-m)/3),
// achieving <0.001 bit resolution with zero floating-point transcendental calls.
func fixedLog2Millibits(x uint64) int64 {
	if x <= 1 {
		return 0
	}
	var k uint
	for tmp := x >> 1; tmp > 0; tmp >>= 1 {
		k++
	}
	base := uint64(1) << k
	frac := int64(((x - base) << 10) >> k)
	concavity := (frac * (1024 - frac)) / 3072
	return int64(k)*1024 + frac + concavity
}

// WarmupCorpusSnapshot eagerly populates the fingerprint-keyed in-memory corpus
// snapshot so subsequent Recall queries on a long-lived daemon hit the warm
// cache without paying the cold SQLite scan cost on the first user query.
func (v *Vault) WarmupCorpusSnapshot(ctx context.Context) error {
	snap, err := v.buildCorpusSnapshot(ctx)
	if err != nil {
		return err
	}
	noetics, _ := v.loadActiveNoeticRecords()
	_, _ = v.EpisodicIndexFaultDiagnostics()
	if zdb, err := openZCodeReadOnly(); err == nil && zdb != nil {
		var one int
		_ = zdb.QueryRowContext(ctx, `SELECT 1 FROM sqlite_master LIMIT 1`).Scan(&one)
	}
	if snap != nil && len(snap.docs) >= 2048 {
		go v.warmupHotZCodeTurns(ctx, snap, noetics)
	}
	return nil
}

func (v *Vault) warmupHotZCodeTurns(ctx context.Context, snap *vaultCorpusSnapshot, noetics []LocalNoeticRecord) {
	if v == nil || snap == nil {
		return
	}
	seenTurn := make(map[string]int, 512)
	var order []string
	recordPtr := func(ptr string) {
		if !strings.HasPrefix(ptr, "@ref:zcode:") {
			return
		}
		rest := strings.TrimPrefix(ptr, "@ref:zcode:")
		parts := strings.SplitN(rest, ":", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return
		}
		msgID, segIdx, _, hasSeg, _ := splitPointerSegment(parts[1])
		if !hasSeg || msgID == "" {
			return
		}
		key := parts[0] + "\x00" + msgID
		if prev, ok := seenTurn[key]; !ok {
			seenTurn[key] = segIdx
			order = append(order, key)
		} else if segIdx > prev {
			seenTurn[key] = segIdx
		}
	}
	for i := range noetics {
		for _, uid := range noetics[i].CitedUnitIDs {
			if dIdx, ok := snap.docIdxByID[uid]; ok && dIdx >= 0 && dIdx < len(snap.docs) {
				recordPtr(snap.docs[dIdx].refPointer)
			}
		}
	}
	for i := len(snap.docs) - 1; i >= 0 && len(order) < 384; i-- {
		recordPtr(snap.docs[i].refPointer)
	}
	for _, key := range order {
		if ctx != nil && ctx.Err() != nil {
			return
		}
		parts := strings.SplitN(key, "\x00", 2)
		if len(parts) == 2 {
			_, _ = resolveZCodeSegmentLive(parts[0], parts[1], seenTurn[key], 0, false)
		}
	}
}

// loadCorpusSnapshot loads or reuses the fingerprint-keyed in-memory corpus snapshot
// across `thin_vault_docs`, `thin_vault_episodic_index`, and `thin_vault_bg_chunks`.
// Sharing this snapshot between `LocalVaultTransport.QueryBlindManifold` and
// `SearchClientSDKDefault` eliminates duplicate SQLite scans and HMAC/wire-decode passes
// on the same recall call.
func (v *Vault) loadCorpusSnapshot(ctx context.Context) (*vaultCorpusSnapshot, error) {
	if v == nil || v.db == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	v.checkExternalDBMutation()
	episodicCodecKey, err := v.episodicCodecKey()
	if v.slotCacheMu != nil && v.snapRef != nil && err == nil {
		v.slotCacheMu.RLock()
		cached := v.snapRef.snap
		lastTime := v.snapRef.lastSnapTime
		alreadyRefreshing := v.snapRef.refreshing
		genVerified := v.snapRef.snapVerifiedGen == v.snapRef.snapWriteGen
		v.slotCacheMu.RUnlock()
		if cached != nil && genVerified && len(cached.docs) >= 2048 && cached.episodicCodecKey == string(episodicCodecKey) {
			if (lastTime.IsZero() || time.Since(lastTime) >= 5*time.Minute) && !alreadyRefreshing {
				v.slotCacheMu.Lock()
				if !v.snapRef.refreshing {
					v.snapRef.refreshing = true
					v.snapRef.lastSnapTime = time.Now()
					go func() {
						defer func() {
							v.slotCacheMu.Lock()
							v.snapRef.refreshing = false
							v.slotCacheMu.Unlock()
						}()
						if !lastTime.IsZero() {
							time.Sleep(200 * time.Millisecond)
						}
						_, _ = v.buildCorpusSnapshot(context.Background())
						_, _ = v.computeEpisodicIndexFaultDiagnostics(episodicCodecKey)
					}()
				}
				v.slotCacheMu.Unlock()
			}
			return cached, nil
		}
	}
	return v.buildCorpusSnapshot(context.WithoutCancel(ctx))
}

// inlineDocScanRow is one freshly inserted non-@ref document row picked up by
// the incremental inline component path.
type inlineDocScanRow struct {
	docID   string
	layer   string
	occ     int64
	rawText string
	addrURI string
	coord64 uint64
	waveBar string
	locus   string
}

// scanInlineDocsSince returns the non-@ref document rows inserted after rowid
// prevMax in rowid order, matching the tail position they would occupy in a
// full inline rescan.
func (v *Vault) scanInlineDocsSince(ctx context.Context, prevMax int64) ([]inlineDocScanRow, error) {
	rows, err := v.db.QueryContext(ctx, `SELECT doc_id, layer, occurred_at, raw_text, address_uri, coord64, waveform_bar, COALESCE(locus, '') FROM thin_vault_docs WHERE rowid > ? AND substr(trim(raw_text), 1, 5) != '@ref:' ORDER BY rowid`, prevMax)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []inlineDocScanRow
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var r inlineDocScanRow
		var coord int64
		if err := rows.Scan(&r.docID, &r.layer, &r.occ, &r.rawText, &r.addrURI, &coord, &r.waveBar, &r.locus); err != nil {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(r.rawText), "@ref:") {
			continue
		}
		r.coord64 = uint64(coord)
		r.locus = strings.TrimSpace(r.locus)
		out = append(out, r)
	}
	return out, rows.Err()
}

// packDocCacheEntry packs one document into its cached slot form. It must stay
// semantically identical to the full-rescan packing in buildCorpusSnapshot so
// incrementally appended entries are indistinguishable from cold-built ones.
func (v *Vault) packDocCacheEntry(docID, rawText string, occ int64) vaultCachedDocSlots {
	pkt, _ := v.PackBlindPacket(docID, rawText, occ)
	allHashes := make([]uint64, 0, len(pkt.Slots))
	for _, ds := range pkt.Slots {
		allHashes = append(allHashes, ds.H)
	}
	tCounts := make(map[uint64]int, len(pkt.Slots))
	docLen := 0
	for _, ds := range pkt.Slots {
		if ds.RawBitsX10 < 20 && len(pkt.Slots) > 2 {
			continue
		}
		tCounts[ds.H]++
		docLen++
	}
	sHashes := make([][]uint64, 0, len(pkt.SentenceFrames))
	for _, sf := range pkt.SentenceFrames {
		if len(sf) == 0 {
			continue
		}
		sDedup := make(map[uint64]bool, len(sf))
		var sh []uint64
		for _, sa := range sf {
			if sa.RawBitsX10 < 20 && len(sf) > 2 {
				continue
			}
			if !sDedup[sa.H] {
				sDedup[sa.H] = true
				sh = append(sh, sa.H)
			}
		}
		if len(sh) > 0 {
			sHashes = append(sHashes, sh)
		}
	}
	if docLen < 1 {
		docLen = 1
	}
	return vaultCachedDocSlots{
		rawText:    rawText,
		termCounts: tCounts,
		sentHashes: sHashes,
		docLen:     docLen,
		allHashes:  allHashes,
	}
}

// buildFullPostings assembles the complete inverted index for docs in one pass:
// ascending doc-index lists per hash, identical content to the lazy per-query
// scan it replaces (both walk docs front-to-back). Costed once per snapshot
// build, it converts every query's posting fill from O(N·|qTerms|)
// map-iteration over all documents to direct lookups. Hash-sharded workers keep
// the build off the critical path's dominant term: shards are hash-disjoint and
// each walks docs in ascending index order, so every list stays ascending.
func buildFullPostings(docs []vaultDocEntry, globalDF map[uint64]int) map[uint64][]int {
	if len(docs) < 2048 {
		postings := make(map[uint64][]int, len(globalDF))
		for idx := range docs {
			for h := range docs[idx].termCounts {
				list, ok := postings[h]
				if !ok {
					list = make([]int, 0, globalDF[h])
				}
				postings[h] = append(list, idx)
			}
		}
		return postings
	}
	const numShards = 8
	var shards [numShards]map[uint64][]int
	var wg sync.WaitGroup
	for s := 0; s < numShards; s++ {
		wg.Add(1)
		go func(shard uint64) {
			defer wg.Done()
			local := make(map[uint64][]int, len(globalDF)/numShards+64)
			for idx := range docs {
				for h := range docs[idx].termCounts {
					if (h & (numShards - 1)) != shard {
						continue
					}
					list, ok := local[h]
					if !ok {
						list = make([]int, 0, globalDF[h])
					}
					local[h] = append(list, idx)
				}
			}
			shards[shard] = local
		}(uint64(s))
	}
	wg.Wait()
	total := 0
	for s := 0; s < numShards; s++ {
		total += len(shards[s])
	}
	postings := make(map[uint64][]int, total)
	for s := 0; s < numShards; s++ {
		for h, list := range shards[s] {
			postings[h] = list
		}
	}
	return postings
}

func (v *Vault) buildCorpusSnapshot(ctx context.Context) (*vaultCorpusSnapshot, error) {
	if v == nil || v.db == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	episodicCodecKey, err := v.episodicCodecKey()
	var genAtStart uint64
	if v.snapRef != nil {
		v.snapRef.buildMu.Lock()
		defer v.snapRef.buildMu.Unlock()
		if v.slotCacheMu != nil {
			v.slotCacheMu.RLock()
			genAtStart = v.snapRef.snapWriteGen
			v.slotCacheMu.RUnlock()
		}
	}
	// O(1) revision gate, read before any aggregate so a write racing this build
	// can only make the stored revisions conservatively stale (next build re-checks).
	var docsRev, bgRev int64
	revGateOK := v.db.QueryRowContext(ctx, `SELECT docs_rev, bg_rev FROM thin_vault_corpus_revision WHERE id = 1`).Scan(&docsRev, &bgRev) == nil

	// Full fast path: both in-database revision counters unchanged proves zero
	// INSERT/UPDATE/DELETE on thin_vault_docs and thin_vault_bg_chunks since the
	// published snapshot (triggers fire for every writer process), which is
	// strictly stronger than the aggregate fingerprints they gate.
	if revGateOK && err == nil && v.slotCacheMu != nil && v.snapRef != nil {
		v.slotCacheMu.RLock()
		cached := v.snapRef.snap
		revMatch := cached != nil && v.snapRef.bgRevReliable &&
			v.snapRef.lastDocsRev == docsRev && v.snapRef.lastBgRev == bgRev &&
			cached.episodicCodecKey == string(episodicCodecKey)
		v.slotCacheMu.RUnlock()
		if revMatch {
			var epiRev int64
			_ = v.db.QueryRowContext(ctx, `SELECT revision FROM thin_vault_episodic_revision WHERE id = 1`).Scan(&epiRev)
			if epiRev == cached.episodicRevision {
				v.slotCacheMu.Lock()
				v.snapRef.lastSnapTime = time.Now()
				if v.snapRef.snapVerifiedGen < genAtStart {
					v.snapRef.snapVerifiedGen = genAtStart
				}
				v.slotCacheMu.Unlock()
				return cached, nil
			}
		}
	}

	var docCnt, docRowSum, docMaxRowID, docTextLen int64
	_ = v.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*), COALESCE(SUM(rowid), 0), COALESCE(MAX(rowid), 0), COALESCE(SUM(LENGTH(raw_text)), 0) FROM thin_vault_docs`,
	).Scan(&docCnt, &docRowSum, &docMaxRowID, &docTextLen)

	var episodicRevision int64
	_ = v.db.QueryRowContext(ctx, `SELECT revision FROM thin_vault_episodic_revision WHERE id = 1`).Scan(&episodicRevision)
	if err != nil && episodicRevision > 0 {
		return nil, err
	}
	// bg fingerprint: an unchanged bg_rev proves the table untouched, so the
	// cached fingerprint values are exact and the O(column) aggregate (reads the
	// whole packed slots column) can be skipped.
	var bgCnt, bgMaxRowID, bgOccSum, bgSlotsLen int64
	bgFromRev := false
	if revGateOK && v.slotCacheMu != nil && v.snapRef != nil {
		v.slotCacheMu.RLock()
		if cached := v.snapRef.snap; cached != nil && v.snapRef.bgRevReliable && v.snapRef.lastBgRev == bgRev {
			bgCnt, bgMaxRowID, bgOccSum, bgSlotsLen = cached.bgCnt, cached.bgMaxRowID, cached.bgOccSum, cached.bgSlotsLen
			bgFromRev = true
		}
		v.slotCacheMu.RUnlock()
	}
	if !bgFromRev {
		_ = v.db.QueryRowContext(
			ctx,
			`SELECT COUNT(*), COALESCE(MAX(rowid), 0), COALESCE(SUM(occurred_at), 0), COALESCE(SUM(LENGTH(slots_json)), 0) FROM thin_vault_bg_chunks`,
		).Scan(&bgCnt, &bgMaxRowID, &bgOccSum, &bgSlotsLen)
	}

	if v.slotCacheMu != nil && v.snapRef != nil {
		v.slotCacheMu.RLock()
		// With a readable revision row, the trigger counters are the primary
		// identity: the aggregates alone are blind to length-preserving UPDATEs.
		revExact := !revGateOK || (v.snapRef.lastDocsRev == docsRev &&
			(!v.snapRef.bgRevReliable || v.snapRef.lastBgRev == bgRev))
		if cached := v.snapRef.snap; cached != nil && revExact &&
			cached.docCnt == docCnt &&
			cached.docRowSum == docRowSum &&
			cached.maxRowID == docMaxRowID &&
			cached.docTextLen == docTextLen &&
			cached.episodicRevision == episodicRevision &&
			cached.episodicCodecKey == string(episodicCodecKey) &&
			cached.bgCnt == bgCnt &&
			cached.bgMaxRowID == bgMaxRowID &&
			cached.bgOccSum == bgOccSum &&
			cached.bgSlotsLen == bgSlotsLen {
			v.slotCacheMu.RUnlock()
			v.slotCacheMu.Lock()
			v.snapRef.lastSnapTime = time.Now()
			if revGateOK {
				v.snapRef.lastDocsRev = docsRev
				v.snapRef.lastBgRev = bgRev
			}
			if v.snapRef.snapVerifiedGen < genAtStart {
				v.snapRef.snapVerifiedGen = genAtStart
			}
			v.slotCacheMu.Unlock()
			return cached, nil
		}
		v.slotCacheMu.RUnlock()
	}

	useCache := v.slotCacheMu != nil
	codecKeyStr := string(episodicCodecKey)

	var inlineDocs []vaultDocEntry
	inlineTotalLen := 0
	inlineFromCache := false
	// inlineGrew marks that the inline component cache was extended with freshly
	// inserted rows this build: the episodic prevSnap shortcuts below must then be
	// skipped, because they reuse prevSnap.docs verbatim and would miss the new
	// inline entries. Full assembly from (incrementally updated) components keeps
	// the cold-rebuild block order inline+bg+episodic, so results stay identical.
	inlineGrew := false
	if useCache && v.snapRef != nil {
		v.slotCacheMu.RLock()
		if v.snapRef.inlineLoaded && v.snapRef.inlineCodecKey == codecKeyStr {
			// Same algebraic invariant as the episodic revision: docs_rev increments
			// once per INSERT/UPDATE/DELETE row, so Δrev == Δcnt proves pure INSERTs
			// and Δrev == 0 proves the table untouched. Length-preserving UPDATEs
			// keep all four aggregates unchanged, so when the revision row is
			// readable it is required, not optional.
			inlineRevSame := !revGateOK || v.snapRef.inlineDocsRev == docsRev
			inlineRevPureInsert := !revGateOK || (docsRev-v.snapRef.inlineDocsRev == docCnt-v.snapRef.inlineDocCnt)
			if inlineRevSame &&
				v.snapRef.inlineDocCnt == docCnt &&
				v.snapRef.inlineDocRowSum == docRowSum &&
				v.snapRef.inlineMaxRowID == docMaxRowID &&
				v.snapRef.inlineDocTextLen == docTextLen {
				inlineDocs = v.snapRef.inlineDocs
				inlineTotalLen = v.snapRef.inlineTotalLen
				inlineFromCache = true
			} else if inlineRevPureInsert &&
				docCnt > v.snapRef.inlineDocCnt &&
				docMaxRowID > v.snapRef.inlineMaxRowID &&
				(docCnt-v.snapRef.inlineDocCnt) == (docMaxRowID-v.snapRef.inlineMaxRowID) {
				prevMax := v.snapRef.inlineMaxRowID
				baseInline := v.snapRef.inlineDocs
				baseInlineLen := v.snapRef.inlineTotalLen
				v.slotCacheMu.RUnlock()
				// Pure INSERTs since prevMax (Δcnt == ΔmaxRowID rules out UPDATE and
				// DELETE on rowid tables). Load only the new non-@ref rows; rowid scan
				// order puts them exactly where a cold rebuild would (inline block tail).
				newRows, scanErr := v.scanInlineDocsSince(ctx, prevMax)
				if scanErr == nil {
					if len(newRows) == 0 {
						inlineDocs = baseInline
						inlineTotalLen = baseInlineLen
						inlineFromCache = true
						v.slotCacheMu.Lock()
						v.snapRef.inlineDocCnt = docCnt
						v.snapRef.inlineDocRowSum = docRowSum
						v.snapRef.inlineMaxRowID = docMaxRowID
						v.snapRef.inlineDocTextLen = docTextLen
						if revGateOK {
							v.snapRef.inlineDocsRev = docsRev
						}
						v.slotCacheMu.Unlock()
					} else {
						appended := make([]vaultDocEntry, 0, len(newRows))
						addedLen := 0
						for _, r := range newRows {
							entry := v.packDocCacheEntry(r.docID, r.rawText, r.occ)
							appended = append(appended, vaultDocEntry{
								docID:      r.docID,
								layer:      r.layer,
								occ:        r.occ,
								addrURI:    r.addrURI,
								coord64:    r.coord64,
								waveBar:    r.waveBar,
								locus:      r.locus,
								termCounts: entry.termCounts,
								sentHashes: entry.sentHashes,
								allHashes:  entry.allHashes,
								docLen:     entry.docLen,
							})
							addedLen += entry.docLen
							v.slotCacheMu.Lock()
							if v.slotCache == nil {
								v.slotCache = make(map[string]vaultCachedDocSlots, 256)
							}
							v.slotCache[r.docID] = entry
							v.slotCacheMu.Unlock()
						}
						// Copy-on-write: the published slice may be referenced by a live
						// snapshot's assembly, so never append into its backing array.
						merged := make([]vaultDocEntry, 0, len(baseInline)+len(appended))
						merged = append(merged, baseInline...)
						merged = append(merged, appended...)
						inlineDocs = merged
						inlineTotalLen = baseInlineLen + addedLen
						inlineFromCache = true
						inlineGrew = true
						v.slotCacheMu.Lock()
						v.snapRef.inlineDocCnt = docCnt
						v.snapRef.inlineDocRowSum = docRowSum
						v.snapRef.inlineMaxRowID = docMaxRowID
						v.snapRef.inlineDocTextLen = docTextLen
						v.snapRef.inlineTotalLen = inlineTotalLen
						v.snapRef.inlineDocs = merged
						if revGateOK {
							v.snapRef.inlineDocsRev = docsRev
						}
						v.slotCacheMu.Unlock()
					}
				}
				goto afterInlineCacheCheck
			}
		}
		v.slotCacheMu.RUnlock()
	}
afterInlineCacheCheck:

	if !inlineFromCache {
		rows, err := v.db.QueryContext(ctx, `SELECT doc_id, layer, occurred_at, raw_text, address_uri, coord64, waveform_bar, COALESCE(locus, '') FROM thin_vault_docs WHERE substr(trim(raw_text), 1, 5) != '@ref:'`)
		if err != nil {
			return nil, err
		}
		type rawDocRow struct {
			docID   string
			layer   string
			occ     int64
			rawText string
			addrURI string
			coord64 uint64
			waveBar string
			locus   string
			cached  bool
			entry   vaultCachedDocSlots
		}
		var rawRows []rawDocRow
		unpackedCount := 0
		for rows.Next() {
			if err := ctx.Err(); err != nil {
				rows.Close()
				return nil, err
			}
			var docID, layer, rawText, addrURI, waveBar, locus string
			var occ, coord64 int64
			if err := rows.Scan(&docID, &layer, &occ, &rawText, &addrURI, &coord64, &waveBar, &locus); err != nil {
				continue
			}
			if strings.HasPrefix(strings.TrimSpace(rawText), "@ref:") {
				continue
			}
			r := rawDocRow{
				docID:   docID,
				layer:   layer,
				occ:     occ,
				rawText: rawText,
				addrURI: addrURI,
				coord64: uint64(coord64),
				waveBar: waveBar,
				locus:   strings.TrimSpace(locus),
			}
			if useCache {
				v.slotCacheMu.RLock()
				if entry, ok := v.slotCache[docID]; ok && entry.rawText == rawText && entry.termCounts != nil {
					r.cached = true
					r.entry = entry
				}
				v.slotCacheMu.RUnlock()
			}
			if !r.cached {
				unpackedCount++
			}
			rawRows = append(rawRows, r)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()

		packOneDoc := v.packDocCacheEntry

		if unpackedCount >= 64 {
			workers := runtime.NumCPU()
			if workers < 2 {
				workers = 2
			}
			if workers > 12 {
				workers = 12
			}
			var wg sync.WaitGroup
			step := (len(rawRows) + workers - 1) / workers
			for w := 0; w < workers; w++ {
				start := w * step
				if start >= len(rawRows) {
					break
				}
				end := start + step
				if end > len(rawRows) {
					end = len(rawRows)
				}
				wg.Add(1)
				go func(s, e int) {
					defer wg.Done()
					for idx := s; idx < e; idx++ {
						if !rawRows[idx].cached {
							rawRows[idx].entry = packOneDoc(rawRows[idx].docID, rawRows[idx].rawText, rawRows[idx].occ)
						}
					}
				}(start, end)
			}
			wg.Wait()
		} else {
			for idx := range rawRows {
				if !rawRows[idx].cached {
					rawRows[idx].entry = packOneDoc(rawRows[idx].docID, rawRows[idx].rawText, rawRows[idx].occ)
				}
			}
		}

		if useCache && unpackedCount > 0 {
			v.slotCacheMu.Lock()
			if v.slotCache == nil {
				v.slotCache = make(map[string]vaultCachedDocSlots, len(rawRows))
			}
			for idx := range rawRows {
				if !rawRows[idx].cached {
					v.slotCache[rawRows[idx].docID] = rawRows[idx].entry
				}
			}
			v.slotCacheMu.Unlock()
		}

		inlineDocs = make([]vaultDocEntry, 0, len(rawRows))
		for idx := range rawRows {
			r := &rawRows[idx]
			inlineTotalLen += r.entry.docLen
			inlineDocs = append(inlineDocs, vaultDocEntry{
				docID:      r.docID,
				layer:      r.layer,
				occ:        r.occ,
				addrURI:    r.addrURI,
				coord64:    r.coord64,
				waveBar:    r.waveBar,
				locus:      r.locus,
				termCounts: r.entry.termCounts,
				sentHashes: r.entry.sentHashes,
				allHashes:  r.entry.allHashes,
				docLen:     r.entry.docLen,
			})
		}
		if useCache && v.snapRef != nil {
			v.slotCacheMu.Lock()
			v.snapRef.inlineLoaded = true
			v.snapRef.inlineDocCnt = docCnt
			v.snapRef.inlineDocRowSum = docRowSum
			v.snapRef.inlineMaxRowID = docMaxRowID
			v.snapRef.inlineDocTextLen = docTextLen
			v.snapRef.inlineCodecKey = codecKeyStr
			v.snapRef.inlineTotalLen = inlineTotalLen
			v.snapRef.inlineDocs = inlineDocs
			if revGateOK {
				v.snapRef.inlineDocsRev = docsRev
			}
			v.slotCacheMu.Unlock()
		}
	}

	inlineCount := len(inlineDocs)

	var bgDocs []vaultDocEntry
	var bgBaseDF map[uint64]int
	bgTotalLen := 0
	bgFromCache := false
	if useCache && v.snapRef != nil {
		v.slotCacheMu.RLock()
		bgRevSame := !revGateOK || !v.snapRef.bgRevReliable || v.snapRef.bgCachedRev == bgRev
		if v.snapRef.bgLoaded && bgRevSame &&
			v.snapRef.bgCachedCnt == bgCnt &&
			v.snapRef.bgCachedMaxRowID == bgMaxRowID &&
			v.snapRef.bgCachedOccSum == bgOccSum &&
			v.snapRef.bgCachedSlotsLen == bgSlotsLen {
			bgDocs = v.snapRef.bgDocs
			bgBaseDF = v.snapRef.bgBaseDF
			bgTotalLen = v.snapRef.bgTotalLen
			bgFromCache = true
		}
		v.slotCacheMu.RUnlock()
	}

	if !bgFromCache {
		bgRows, bgErr := v.db.QueryContext(ctx, `SELECT unit_id, occurred_at, address_uri, coord64, waveform_bar, slots_json FROM thin_vault_bg_chunks`)
		if bgErr == nil {
			type rawBgRow struct {
				unitID   string
				occ      int64
				addrURI  string
				coord64  uint64
				waveBar  string
				slotsRaw []byte
				ok       bool
				entry    vaultDocEntry
			}
			var rawBg []rawBgRow
			for bgRows.Next() {
				var r rawBgRow
				var coord64 int64
				if err := bgRows.Scan(&r.unitID, &r.occ, &r.addrURI, &coord64, &r.waveBar, &r.slotsRaw); err != nil {
					continue
				}
				r.coord64 = uint64(coord64)
				rawBg = append(rawBg, r)
			}
			bgRows.Close()

			decodeBgOne := func(r *rawBgRow) {
				tCounts, dLen, ok := decodeAtomSlotTermCounts(r.slotsRaw)
				if !ok {
					return
				}
				r.entry = vaultDocEntry{
					docID:      r.unitID,
					layer:      LayerBackground,
					occ:        r.occ,
					addrURI:    r.addrURI,
					coord64:    r.coord64,
					waveBar:    r.waveBar,
					termCounts: tCounts,
					docLen:     dLen,
				}
				r.slotsRaw = nil
				r.ok = true
			}

			if len(rawBg) >= 128 {
				workers := runtime.NumCPU()
				if workers < 2 {
					workers = 2
				}
				if workers > 12 {
					workers = 12
				}
				var wg sync.WaitGroup
				step := (len(rawBg) + workers - 1) / workers
				for w := 0; w < workers; w++ {
					start := w * step
					if start >= len(rawBg) {
						break
					}
					end := start + step
					if end > len(rawBg) {
						end = len(rawBg)
					}
					wg.Add(1)
					go func(s, e int) {
						defer wg.Done()
						for idx := s; idx < e; idx++ {
							decodeBgOne(&rawBg[idx])
						}
					}(start, end)
				}
				wg.Wait()
			} else {
				for idx := range rawBg {
					decodeBgOne(&rawBg[idx])
				}
			}

			bgDocs = make([]vaultDocEntry, 0, len(rawBg))
			for idx := range rawBg {
				if rawBg[idx].ok {
					bgTotalLen += rawBg[idx].entry.docLen
					bgDocs = append(bgDocs, rawBg[idx].entry)
				}
			}
			if len(bgDocs) >= 512 {
				const numShards = 16
				type shardMap struct {
					m map[uint64]int
				}
				var shards [numShards]shardMap
				var wgBG sync.WaitGroup
				wgBG.Add(numShards)
				for s := 0; s < numShards; s++ {
					go func(shard uint64) {
						defer wgBG.Done()
						localM := make(map[uint64]int, len(bgDocs)/4+64)
						for i := range bgDocs {
							for h := range bgDocs[i].termCounts {
								if (h & (numShards - 1)) == shard {
									localM[h]++
								}
							}
						}
						shards[shard].m = localM
					}(uint64(s))
				}
				wgBG.Wait()
				totalUnique := 0
				for s := 0; s < numShards; s++ {
					totalUnique += len(shards[s].m)
				}
				bgBaseDF = make(map[uint64]int, totalUnique+1024)
				for s := 0; s < numShards; s++ {
					for h, c := range shards[s].m {
						bgBaseDF[h] = c
					}
				}
			} else {
				bgBaseDF = make(map[uint64]int, len(bgDocs)*4)
				for i := range bgDocs {
					for h := range bgDocs[i].termCounts {
						bgBaseDF[h]++
					}
				}
			}
			if useCache && v.snapRef != nil {
				v.slotCacheMu.Lock()
				v.snapRef.bgLoaded = true
				v.snapRef.bgCachedCnt = bgCnt
				v.snapRef.bgCachedMaxRowID = bgMaxRowID
				v.snapRef.bgCachedOccSum = bgOccSum
				v.snapRef.bgCachedSlotsLen = bgSlotsLen
				v.snapRef.bgTotalLen = bgTotalLen
				v.snapRef.bgDocs = bgDocs
				v.snapRef.bgBaseDF = bgBaseDF
				if revGateOK {
					v.snapRef.bgCachedRev = bgRev
				}
				v.slotCacheMu.Unlock()
			}
		}
	}

	var episodicDocs []vaultDocEntry
	epiTotalLen := 0
	if len(episodicCodecKey) > 0 {
		var epiCnt, epiMaxRowID int64
		// Whole-table fingerprint (no codec_key filter): the snapshot population is
		// the multi-generation union, and every index trigger bumps episodic_revision
		// regardless of generation. A per-generation count would desynchronize the
		// Δrev == Δcnt pure-INSERT invariant the moment historical-generation rows
		// exist, silently demoting every post-write query to a full reload.
		_ = v.db.QueryRowContext(
			ctx,
			`SELECT COUNT(*), COALESCE(MAX(rowid), 0) FROM thin_vault_episodic_index`,
		).Scan(&epiCnt, &epiMaxRowID)

		if useCache && v.snapRef != nil && inlineFromCache && !inlineGrew && bgFromCache {
			v.slotCacheMu.RLock()
			prevSnap := v.snapRef.snap
			// Algebraic append-only invariant: since INSERT, UPDATE and DELETE triggers each
			// increment `episodic_revision` by 1 per row, Δrev - Δcnt = U + 2D + O >= 0, which
			// equals 0 if and only if U == 0, D == 0 and O == 0 (pure INSERTs into this codec_key).
			if prevSnap != nil &&
				v.snapRef.epiLoaded &&
				v.snapRef.epiCodecKey == codecKeyStr &&
				prevSnap.bgCnt == bgCnt &&
				prevSnap.bgMaxRowID == bgMaxRowID &&
				prevSnap.bgOccSum == bgOccSum &&
				prevSnap.bgSlotsLen == bgSlotsLen &&
				epiCnt >= v.snapRef.epiLastCnt &&
				epiMaxRowID >= v.snapRef.epiLastMaxRowID &&
				(episodicRevision-v.snapRef.epiLastRev) == (epiCnt-v.snapRef.epiLastCnt) {
				if epiCnt == v.snapRef.epiLastCnt && epiMaxRowID == v.snapRef.epiLastMaxRowID {
					v.slotCacheMu.RUnlock()
					// A complete inverted index is immutable after publication
					// (queryPostings only reads it), so the unchanged-docs refresh
					// can share it; an incomplete lazy map must not be shared
					// across two snapshots with distinct mutexes.
					sharedPostings := map[uint64][]int{}
					sharedComplete := false
					if prevSnap.postingsComplete {
						sharedPostings = prevSnap.postings
						sharedComplete = true
					}
					nextSnap := &vaultCorpusSnapshot{
						docCnt:           docCnt,
						docRowSum:        docRowSum,
						maxRowID:         docMaxRowID,
						docTextLen:       docTextLen,
						episodicRevision: prevSnap.episodicRevision,
						episodicCodecKey: prevSnap.episodicCodecKey,
						bgCnt:            prevSnap.bgCnt,
						bgMaxRowID:       prevSnap.bgMaxRowID,
						bgOccSum:         prevSnap.bgOccSum,
						bgSlotsLen:       prevSnap.bgSlotsLen,
						inlineCount:      prevSnap.inlineCount,
						totalDocLen:      prevSnap.totalDocLen,
						docs:             prevSnap.docs,
						episodicDocs:     prevSnap.episodicDocs,
						validEpisodicIDs: prevSnap.validEpisodicIDs,
						docIdxByID:       prevSnap.docIdxByID,
						ftsRowIDToDocID:  prevSnap.ftsRowIDToDocID,
						globalDF:         prevSnap.globalDF,
						postings:         sharedPostings,
						postingsComplete: sharedComplete,
					}
					v.slotCacheMu.Lock()
					if v.snapRef.snap == prevSnap {
						v.snapRef.snap = nextSnap
						v.snapRef.lastSnapTime = time.Now()
						if revGateOK {
							v.snapRef.lastDocsRev = docsRev
							v.snapRef.lastBgRev = bgRev
						}
						if v.snapRef.snapVerifiedGen < genAtStart {
							v.snapRef.snapVerifiedGen = genAtStart
						}
					}
					v.slotCacheMu.Unlock()
					return nextSnap, nil
				}
				lastMaxRow := v.snapRef.epiLastMaxRowID
				v.slotCacheMu.RUnlock()
				deltaDocs, newMaxRow, dErr := v.indexedEpisodicDocsSince(ctx, lastMaxRow)
				if dErr == nil {
					v.slotCacheMu.Lock()
					if v.snapRef.snap == prevSnap {
						// Union-read snapshots already contain documents loaded from
						// historical codec generations; a later heal may re-index one
						// of them under the current key and surface it here as a
						// delta. Drop deltas whose document is already in the
						// snapshot so postings/DF never double-count a unit.
						filtered := deltaDocs[:0]
						for _, dd := range deltaDocs {
							if _, exists := prevSnap.docIdxByID[dd.docID]; exists {
								continue
							}
							filtered = append(filtered, dd)
						}
						deltaDocs = filtered
						// Copy-on-write snapshot publication: a published *vaultCorpusSnapshot is
						// read lock-free by concurrent recall/hydration goroutines, so its maps and
						// slices must never be mutated in place after publication.
						prevSnap.postingsMu.Lock()
						baseDocLen := len(prevSnap.docs)
						newDocs := make([]vaultDocEntry, baseDocLen+len(deltaDocs))
						copy(newDocs, prevSnap.docs)
						copy(newDocs[baseDocLen:], deltaDocs)

						newEpiDocs := make([]vaultDocEntry, len(prevSnap.episodicDocs)+len(deltaDocs))
						copy(newEpiDocs, prevSnap.episodicDocs)
						copy(newEpiDocs[len(prevSnap.episodicDocs):], deltaDocs)

						newValidEpi := make(map[string]struct{}, len(prevSnap.validEpisodicIDs)+len(deltaDocs))
						for k, val := range prevSnap.validEpisodicIDs {
							newValidEpi[k] = val
						}
						newDocIdxByID := make(map[string]int, len(prevSnap.docIdxByID)+len(deltaDocs))
						for k, val := range prevSnap.docIdxByID {
							newDocIdxByID[k] = val
						}
						newGlobalDF := make(map[uint64]int, len(prevSnap.globalDF)+len(deltaDocs)*4)
						for k, val := range prevSnap.globalDF {
							newGlobalDF[k] = val
						}
						newPostings := make(map[uint64][]int, len(prevSnap.postings))
						for k, list := range prevSnap.postings {
							// Clip capacity so appending to newPostings[h] never mutates prevSnap's backing array.
							newPostings[k] = list[:len(list):len(list)]
						}
						var newEffDF map[uint64]int
						if len(prevSnap.effDF) > 0 {
							newEffDF = make(map[uint64]int, len(prevSnap.effDF))
							for k, val := range prevSnap.effDF {
								newEffDF[k] = val
							}
						}

						addedLen := 0
						for i, dd := range deltaDocs {
							newDocIdx := baseDocLen + i
							addedLen += dd.docLen
							newValidEpi[dd.docID] = struct{}{}
							newDocIdxByID[dd.docID] = newDocIdx
							for h := range dd.termCounts {
								newGlobalDF[h]++
								if list, ok := newPostings[h]; ok {
									newPostings[h] = append(list, newDocIdx)
								} else if prevSnap.postingsComplete {
									// Complete-index semantics: absence means df was 0,
									// so a first occurrence must create the list or the
									// term would stay invisible to direct lookups.
									newPostings[h] = []int{newDocIdx}
								}
								delete(newEffDF, h)
							}
						}
						nextSnap := &vaultCorpusSnapshot{
							docCnt:           docCnt,
							docRowSum:        docRowSum,
							maxRowID:         docMaxRowID,
							docTextLen:       docTextLen,
							episodicRevision: episodicRevision,
							episodicCodecKey: prevSnap.episodicCodecKey,
							bgCnt:            prevSnap.bgCnt,
							bgMaxRowID:       prevSnap.bgMaxRowID,
							bgOccSum:         prevSnap.bgOccSum,
							bgSlotsLen:       prevSnap.bgSlotsLen,
							inlineCount:      prevSnap.inlineCount,
							totalDocLen:      prevSnap.totalDocLen + addedLen,
							docs:             newDocs,
							episodicDocs:     newEpiDocs,
							validEpisodicIDs: newValidEpi,
							docIdxByID:       newDocIdxByID,
							ftsRowIDToDocID:  prevSnap.ftsRowIDToDocID,
							globalDF:         newGlobalDF,
							postings:         newPostings,
							postingsComplete: prevSnap.postingsComplete,
							effDF:            newEffDF,
						}
						prevSnap.postingsMu.Unlock()
						if newMaxRow < epiMaxRowID {
							newMaxRow = epiMaxRowID
						}
						v.snapRef.snap = nextSnap
						v.snapRef.epiLastRev = episodicRevision
						v.snapRef.epiLastCnt = epiCnt
						v.snapRef.epiLastMaxRowID = newMaxRow
						v.snapRef.epiDocCnt = docCnt
						v.snapRef.epiDocMaxRowID = docMaxRowID
						v.snapRef.epiTotalLen += addedLen
						v.snapRef.epiDocs = newEpiDocs
						v.snapRef.lastSnapTime = time.Now()
						if revGateOK {
							v.snapRef.lastDocsRev = docsRev
							v.snapRef.lastBgRev = bgRev
						}
						if v.snapRef.snapVerifiedGen < genAtStart {
							v.snapRef.snapVerifiedGen = genAtStart
						}
						v.slotCacheMu.Unlock()
						return nextSnap, nil
					}
					v.slotCacheMu.Unlock()
				}
			} else {
				v.slotCacheMu.RUnlock()
			}
		}

		var maxSeenRow int64
		var err error
		// Component-level episodic reuse (mirrors the bg component cache): when the
		// index table fingerprint is bitwise unchanged (count, max rowid, trigger
		// revision, codec key), the cached component rows are exact and the full
		// reload (MAC verification + wire decode over every generation) is skipped.
		// This is the inline-write path: inlineGrew forces full assembly below, but
		// assembly only needs the rows, not a reload.
		epiComponentReused := false
		if useCache && v.snapRef != nil {
			v.slotCacheMu.RLock()
			if v.snapRef.epiLoaded &&
				v.snapRef.epiCodecKey == codecKeyStr &&
				v.snapRef.epiLastCnt == epiCnt &&
				v.snapRef.epiLastMaxRowID >= epiMaxRowID &&
				v.snapRef.epiLastRev == episodicRevision {
				episodicDocs = v.snapRef.epiDocs
				epiTotalLen = v.snapRef.epiTotalLen
				maxSeenRow = v.snapRef.epiLastMaxRowID
				epiComponentReused = true
			}
			v.slotCacheMu.RUnlock()
		}
		if !epiComponentReused {
			episodicDocs, maxSeenRow, err = v.indexedEpisodicDocsSince(ctx, 0)
			if err != nil {
				return nil, err
			}
			for _, doc := range episodicDocs {
				epiTotalLen += doc.docLen
			}
		}
		if maxSeenRow < epiMaxRowID {
			maxSeenRow = epiMaxRowID
		}
		if useCache && v.snapRef != nil {
			v.slotCacheMu.Lock()
			v.snapRef.epiLoaded = true
			v.snapRef.epiCodecKey = codecKeyStr
			v.snapRef.epiLastRev = episodicRevision
			v.snapRef.epiLastCnt = epiCnt
			v.snapRef.epiLastMaxRowID = maxSeenRow
			v.snapRef.epiDocCnt = docCnt
			v.snapRef.epiDocMaxRowID = docMaxRowID
			v.snapRef.epiTotalLen = epiTotalLen
			v.snapRef.epiDocs = episodicDocs
			v.slotCacheMu.Unlock()
		}
	}

	totalDocLen := inlineTotalLen + bgTotalLen + epiTotalLen
	docs := make([]vaultDocEntry, 0, inlineCount+len(bgDocs)+len(episodicDocs))
	docs = append(docs, inlineDocs...)
	docs = append(docs, bgDocs...)
	docs = append(docs, episodicDocs...)

	validEpisodicIDs := make(map[string]struct{}, len(episodicDocs))
	for _, ed := range episodicDocs {
		validEpisodicIDs[ed.docID] = struct{}{}
	}
	docIdxByID := make(map[string]int, len(docs))
	for idx, d := range docs {
		docIdxByID[d.docID] = idx
	}
	globalDF := make(map[uint64]int, len(bgBaseDF)+(inlineCount+len(episodicDocs))*4)
	for h, c := range bgBaseDF {
		globalDF[h] = c
	}
	for idx := 0; idx < inlineCount; idx++ {
		for h := range inlineDocs[idx].termCounts {
			globalDF[h]++
		}
	}
	for idx := range episodicDocs {
		for h := range episodicDocs[idx].termCounts {
			globalDF[h]++
		}
	}
	ftsRowIDToDocID := make(map[int64]string, len(docs))
	if mRows, mErr := v.db.QueryContext(ctx, `SELECT rowid, doc_id FROM thin_vault_fts_map`); mErr == nil {
		for mRows.Next() {
			var rid int64
			var did string
			if mRows.Scan(&rid, &did) == nil && did != "" {
				ftsRowIDToDocID[rid] = did
			}
		}
		mRows.Close()
	}
	snap := &vaultCorpusSnapshot{
		docCnt:           docCnt,
		docRowSum:        docRowSum,
		maxRowID:         docMaxRowID,
		docTextLen:       docTextLen,
		episodicRevision: episodicRevision,
		episodicCodecKey: string(episodicCodecKey),
		bgCnt:            bgCnt,
		bgMaxRowID:       bgMaxRowID,
		bgOccSum:         bgOccSum,
		bgSlotsLen:       bgSlotsLen,
		inlineCount:      inlineCount,
		totalDocLen:      totalDocLen,
		docs:             docs,
		episodicDocs:     episodicDocs,
		validEpisodicIDs: validEpisodicIDs,
		docIdxByID:       docIdxByID,
		ftsRowIDToDocID:  ftsRowIDToDocID,
		globalDF:         globalDF,
		postings:         buildFullPostings(docs, globalDF),
		postingsComplete: true,
	}
	if useCache && v.snapRef != nil {
		v.slotCacheMu.Lock()
		v.snapRef.snap = snap
		v.snapRef.lastSnapTime = time.Now()
		if revGateOK {
			v.snapRef.lastDocsRev = docsRev
			v.snapRef.lastBgRev = bgRev
		}
		if v.snapRef.snapVerifiedGen < genAtStart {
			v.snapRef.snapVerifiedGen = genAtStart
		}
		v.slotCacheMu.Unlock()
	}
	return snap, nil
}

// localQueryClockHeaderExclusiveHashes identifies reference clock-header day/beacon hashes in SentenceFrames[0]
// (e.g. "Today is June 1, 2025. <actual question...>") using pure pulse structure (zero wordlists):
//   - Requires SentenceFrames[0] to contain at least 2 distinct digit slots (day + year) within its first 8 primary slots,
//     followed by >= 2 non-digit salient content tokens in the question body;
//   - Returns the set of clock-header-exclusive calendar beacon hashes (width 588/284) and day-number hashes not repeated in the body,
//     so they are de-rigidified to soft carrier width (18) instead of hijacking the query's salient denominator.
func localQueryClockHeaderExclusiveHashes(pkt BlindPulsePacket) map[uint64]bool {
	if len(pkt.SentenceFrames) == 0 || len(pkt.Slots) == 0 {
		return nil
	}
	sf0 := pkt.SentenceFrames[0]
	isCalBeacon := func(tok BlindTokenAtom) bool {
		return (tok.ClassFlags&(FlagHasDigit|FlagHasConn)) == (FlagHasDigit|FlagHasConn) &&
			(tok.RawBitsX10 == ramesdk.CalendarBeaconWidthX10 || tok.RawBitsX10 == 284)
	}
	isStemHarmonic := func(prev, cur BlindTokenAtom) bool {
		if prev.H == 0 || (cur.RawBitsX10&1) == 0 || (cur.ClassFlags&FlagHasConn) == 0 || (cur.ClassFlags&FlagHasDigit) != 0 {
			return false
		}
		return prev.RawBitsX10 >= cur.RawBitsX10 && (prev.RawBitsX10-cur.RawBitsX10 == 1 || prev.RawBitsX10-cur.RawBitsX10 == 3)
	}
	type pTok struct {
		atom          BlindTokenAtom
		extraDigitH   uint64
		sf0RawIdx     int
		followedByQty bool
	}
	var sf0Beacons []uint64
	var sf0Primary []pTok
	var prevP BlindTokenAtom
	for rawIdx, tok := range sf0 {
		if isStemHarmonic(prevP, tok) {
			continue
		}
		if isCalBeacon(tok) {
			sf0Beacons = append(sf0Beacons, tok.H)
			continue
		}
		if (tok.ClassFlags & ramesdk.FlagIsQuantityOrUnit) != 0 {
			nP := len(sf0Primary)
			if nP >= 2 &&
				(sf0Primary[nP-1].atom.ClassFlags&FlagHasDigit) != 0 &&
				(sf0Primary[nP-2].atom.ClassFlags&FlagHasDigit) != 0 &&
				sf0Primary[nP-2].extraDigitH == 0 {
				sf0Primary[nP-2].extraDigitH = sf0Primary[nP-1].atom.H
				sf0Primary[nP-2].sf0RawIdx = sf0Primary[nP-1].sf0RawIdx
				sf0Primary[nP-2].followedByQty = true
				sf0Primary = sf0Primary[:nP-1]
			} else if nP >= 1 {
				sf0Primary[nP-1].followedByQty = true
			}
			continue
		}
		prevP = tok
		sf0Primary = append(sf0Primary, pTok{atom: tok, sf0RawIdx: rawIdx})
	}
	if len(sf0Primary) < 4 {
		return nil
	}
	scanLim := 8
	if len(sf0Primary) < scanLim {
		scanLim = len(sf0Primary)
	}
	var digits []int
	for i := 0; i < scanLim; i++ {
		fl := sf0Primary[i].atom.ClassFlags
		if (fl&FlagHasDigit) != 0 && (fl&FlagHasConn) == 0 && (fl&FlagHasCamel) == 0 {
			digits = append(digits, i)
		}
	}
	if len(digits) < 2 {
		return nil
	}
	dayIdx := digits[0]
	yearIdx := digits[len(digits)-1]
	if sf0Primary[yearIdx].followedByQty {
		return nil
	}
	if len(pkt.SentenceFrames) == 1 || len(sf0Primary) > 8 {
		if dayIdx < 2 || yearIdx-dayIdx > 2 {
			return nil
		}
	}
	if sf0Primary[dayIdx].atom.H == sf0Primary[yearIdx].atom.H {
		return nil
	}

	bodySeen := make(map[uint64]bool, len(pkt.Slots))
	bodySalient := 0
	afterRawIdx := sf0Primary[yearIdx].sf0RawIdx + 1
	for afterRawIdx < len(sf0) {
		if isStemHarmonic(sf0Primary[yearIdx].atom, sf0[afterRawIdx]) {
			afterRawIdx++
		} else {
			break
		}
	}
	var prevB BlindTokenAtom
	for rawIdx := afterRawIdx; rawIdx < len(sf0); rawIdx++ {
		tok := sf0[rawIdx]
		if isStemHarmonic(prevB, tok) {
			bodySeen[tok.H] = true
			continue
		}
		if isCalBeacon(tok) {
			continue
		}
		prevB = tok
		bodySeen[tok.H] = true
		if tok.RawBitsX10 >= 26 && (tok.ClassFlags&FlagHasDigit) == 0 && (tok.ClassFlags&ramesdk.FlagIsQuantityOrUnit) == 0 {
			bodySalient++
		}
	}
	for si := 1; si < len(pkt.SentenceFrames); si++ {
		var prevS BlindTokenAtom
		for _, tok := range pkt.SentenceFrames[si] {
			if isStemHarmonic(prevS, tok) {
				bodySeen[tok.H] = true
				continue
			}
			if isCalBeacon(tok) {
				bodySeen[tok.H] = true
				continue
			}
			prevS = tok
			bodySeen[tok.H] = true
			if tok.RawBitsX10 >= 26 && (tok.ClassFlags&FlagHasDigit) == 0 && (tok.ClassFlags&ramesdk.FlagIsQuantityOrUnit) == 0 {
				bodySalient++
			}
		}
	}
	if bodySalient < 2 {
		return nil
	}
	clockYearH := sf0Primary[yearIdx].atom.H
	dayExcl := make(map[uint64]bool, len(digits)+len(sf0Beacons)+2)
	if dayIdx > 0 {
		mTok := sf0Primary[dayIdx-1].atom
		if (mTok.ClassFlags&FlagHasDigit) == 0 && !bodySeen[mTok.H] {
			dayExcl[mTok.H] = true
		}
	}
	if !bodySeen[clockYearH] {
		dayExcl[clockYearH] = true
	}
	for _, dIdx := range digits[:len(digits)-1] {
		dh := sf0Primary[dIdx].atom.H
		if dh != clockYearH && !bodySeen[dh] {
			dayExcl[dh] = true
		}
		if exH := sf0Primary[dIdx].extraDigitH; exH != 0 && exH != clockYearH && !bodySeen[exH] {
			dayExcl[exH] = true
		}
	}
	for _, bH := range sf0Beacons {
		if !bodySeen[bH] {
			dayExcl[bH] = true
		}
	}
	return dayExcl
}

// SearchClientSDKDefault implements the built-in Client SDK Default Recall capability on the local SQLite Vault:
//  1. SQLite FTS5 trigram candidate pre-filtering (`thin_vault_docs_fts MATCH ?`) when available;
//  2. Surface & keyed-hash token commitment extraction (`SplitWords` + `packSentenceAtoms`);
//  3. Fixed-point Shannon self-information (Surprisal) + Okapi BM25 TF/length normalization + pairwise Shannon synergy;
//  4. Sentence-window Shannon wave-packet focus & same-day session continuity pairing.
//
// Zero floating-point transcendental math (`math.*`) is used.
func (v *Vault) SearchClientSDKDefault(queryText string, topK int) ([]CloudQueryHit, error) {
	if v == nil || v.db == nil {
		return nil, nil
	}
	if topK <= 0 {
		topK = 10
	}
	qPkt, _ := v.PackBlindPacket("Q-DEFAULT", queryText, 0)
	if len(qPkt.Slots) == 0 {
		return nil, nil
	}
	clockExcl := localQueryClockHeaderExclusiveHashes(qPkt)
	// Identify tokens whose uppercase flag appears in a genuine mid-sentence position
	// (after the first non-clock primary token of a sentence frame), distinguishing true
	// proper nouns from sentence-initial grammatical capitalization ("Today is ... . What/In/During ...").
	midSentUpper := make(map[uint64]bool, len(qPkt.Slots))
	for _, sf := range qPkt.SentenceFrames {
		seenFirstPrimary := false
		var prevPrimary BlindTokenAtom
		for _, tok := range sf {
			if clockExcl[tok.H] {
				continue
			}
			if (tok.ClassFlags&(FlagHasDigit|FlagHasConn)) == (FlagHasDigit|FlagHasConn) &&
				(tok.RawBitsX10 == ramesdk.CalendarBeaconWidthX10 || tok.RawBitsX10 == 284) {
				continue
			}
			isHarm := prevPrimary.H != 0 && (tok.RawBitsX10&1) == 1 && (tok.ClassFlags&FlagHasConn) != 0 &&
				(tok.ClassFlags&FlagHasDigit) == 0 && prevPrimary.RawBitsX10 >= tok.RawBitsX10 &&
				(prevPrimary.RawBitsX10-tok.RawBitsX10 == 1 || prevPrimary.RawBitsX10-tok.RawBitsX10 == 3)
			if isHarm {
				if seenFirstPrimary && (tok.ClassFlags&FlagHasUpper) != 0 {
					midSentUpper[tok.H] = true
				}
				continue
			}
			prevPrimary = tok
			if !seenFirstPrimary {
				seenFirstPrimary = true
				continue
			}
			if (tok.ClassFlags & FlagHasUpper) != 0 {
				midSentUpper[tok.H] = true
			}
		}
	}
	type queryVariant struct {
		h           uint64
		scalePerm   int64 // 1000 = primary exact match, 800 = 1st harmonic (stem), 550 = 2nd harmonic (morph root)
		shannonGain int64
		jSurp       int64
		wSurp       int64
		isSal       bool
	}
	type queryTermGroup struct {
		primaryH uint64
		width    uint16
		flags    uint8
		variants []queryVariant
	}
	var qGroups []queryTermGroup
	groupIdxByPrimaryH := make(map[uint64]int, len(qPkt.Slots))
	qHashes := make(map[uint64]uint16, len(qPkt.Slots))
	qFlags := make(map[uint64]uint8, len(qPkt.Slots))

	for i := 0; i < len(qPkt.Slots); i++ {
		s := qPkt.Slots[i]
		if clockExcl[s.H] {
			s.RawBitsX10 = 18
			s.ClassFlags &^= FlagHasDigit | FlagHasUpper | FlagHasCamel
		}
		if s.RawBitsX10 > qHashes[s.H] {
			qHashes[s.H] = s.RawBitsX10
		}
		qFlags[s.H] |= s.ClassFlags

		// Companion harmonic slot emitted by SetCanonicalHarmonics has odd width (evenW-1 or evenW-3) and FlagHasConn
		isCompanion := len(qGroups) > 0 && (s.RawBitsX10&1) == 1 && (s.ClassFlags&FlagHasConn) != 0 && (s.ClassFlags&FlagHasDigit) == 0
		if isCompanion {
			lastG := &qGroups[len(qGroups)-1]
			if lastG.width >= s.RawBitsX10 && (lastG.width-s.RawBitsX10 == 1 || lastG.width-s.RawBitsX10 == 3) {
				scale := int64(820)
				if lastG.width-s.RawBitsX10 == 3 {
					scale = 580
				}
				alreadyInGroup := false
				for _, v := range lastG.variants {
					if v.h == s.H {
						alreadyInGroup = true
						break
					}
				}
				if !alreadyInGroup {
					lastG.variants = append(lastG.variants, queryVariant{h: s.H, scalePerm: scale})
				}
				continue
			}
		}
		if gIdx, exists := groupIdxByPrimaryH[s.H]; exists {
			if s.RawBitsX10 > qGroups[gIdx].width {
				qGroups[gIdx].width = s.RawBitsX10
			}
			qGroups[gIdx].flags |= s.ClassFlags
			continue
		}
		groupIdxByPrimaryH[s.H] = len(qGroups)
		qGroups = append(qGroups, queryTermGroup{
			primaryH: s.H,
			width:    s.RawBitsX10,
			flags:    s.ClassFlags,
			variants: []queryVariant{{h: s.H, scalePerm: 1000}},
		})
	}

	// NOTE: the local SQLite FTS5 trigram ranker is fused with the blind-slot ranking at the
	// end of this function via reciprocal rank fusion. It is intentionally NOT added here as
	// a raw score addend: its milli-magnitude (~1e2) is six orders of magnitude smaller than
	// the Shannon/BM25 slot score (~1e8), which would make the term behaviourally dead.

	snap, err := v.loadCorpusSnapshot(context.Background())
	if err != nil || snap == nil {
		return nil, err
	}

	docs := snap.docs
	docIdxMap := snap.docIdxByID
	df := snap.globalDF
	totalDocs := len(docs)
	if totalDocs == 0 {
		return nil, nil
	}
	avgDL := snap.totalDocLen / totalDocs
	if avgDL < 8 {
		avgDL = 8
	}

	// Fixed-point Shannon self-information (Surprisal) in millibits: I(h) = log2((N+4)/(effDF+1)) * 1024
	// Uses SubmodularCloneFoldedDF to fold near-clone postings logarithmically.
	qPostings := snap.queryPostings(qHashes)
	log2Total := fixedLog2Millibits(uint64(totalDocs + 4))
	occAt := func(docIdx int) int64 { return docs[docIdx].occ }
	termsAt := func(docIdx int) map[uint64]int { return docs[docIdx].termCounts }
	qSurprisal := make(map[uint64]int64, len(qHashes))
	qEffDF := make(map[uint64]int, len(qHashes))
	if len(qHashes) >= 4 && totalDocs >= 2048 {
		hList := make([]uint64, 0, len(qHashes))
		for h := range qHashes {
			hList = append(hList, h)
		}
		effVals := make([]int, len(hList))
		var wg sync.WaitGroup
		workers := 4
		if len(hList) < workers {
			workers = len(hList)
		}
		step := (len(hList) + workers - 1) / workers
		for w := 0; w < workers; w++ {
			st := w * step
			if st >= len(hList) {
				break
			}
			en := st + step
			if en > len(hList) {
				en = len(hList)
			}
			wg.Add(1)
			go func(s, e int) {
				defer wg.Done()
				for idx := s; idx < e; idx++ {
					h := hList[idx]
					effVals[idx] = snap.queryEffDF(h, qPostings[h], totalDocs, occAt, termsAt)
				}
			}(st, en)
		}
		wg.Wait()
		for idx, h := range hList {
			effDF := effVals[idx]
			qEffDF[h] = effDF
			surp := log2Total - fixedLog2Millibits(uint64(effDF+1))
			if surp < 512 {
				surp = 512
			}
			qSurprisal[h] = surp
		}
	} else {
		for h := range qHashes {
			posting := qPostings[h]
			effDF := snap.queryEffDF(h, posting, totalDocs, occAt, termsAt)
			qEffDF[h] = effDF
			surp := log2Total - fixedLog2Millibits(uint64(effDF+1))
			if surp < 512 {
				surp = 512
			}
			qSurprisal[h] = surp
		}
	}

	grpJointInfo := make([]int64, len(qGroups))
	var salientJList []int64
	var totalQueryInfo int64
	hasHighWidthGroup := false
	for gIdx := range qGroups {
		if qGroups[gIdx].width >= 20 {
			hasHighWidthGroup = true
			break
		}
	}

	// Colloquial Query Condensation & Lexeme-Family Denominator Conservation (pure 64-bit integer arithmetic):
	//  1. Lexeme-Family Surprisal Conservation: when a non-rigid surface inflected word (e.g. "stayed" df=4,
	//     "hotels" df=26, "discovered" df=7, "occasions" df=9) has a much more frequent stem/morph root variant
	//     in the vault (e.g. "stay" df=262, "hotel" df=450), conserve its effective surprisal via the Euler
	//     log-conjugate (632*S_exact + 368*S_root)/1000 so rare surface inflections do not hijack proper nouns;
	//  2. Poisson-Occupancy Excess Temporal Condensation (kappaPerm in [0, 1000]): measures whether a query
	//     term's hits cluster into specific event days (true event/place anchors like Porto/Glasgow/Cairo/Cornwall)
	//     or scatter uniformly across the calendar like Poisson white noise (colloquial meta-shell words like
	//     "took", "remember", "occasions", "discovered", "titles").
	enableColloquialCondense := os.Getenv("ROLLING_MEMORY_COLLOQUIAL_CONDENSE") != "0"
	grpKappaPerm := make([]int64, len(qGroups))
	grpIsEventAnchor := make([]bool, len(qGroups))
	var anchorDocSet map[int]bool
	var anchorDaySet map[int64]bool
	hasEventAnchor := false
	if enableColloquialCondense && totalDocs >= 16 && len(qGroups) >= 2 {
		estTotalDays := int64(totalDocs/9 + 16)
		for gIdx := range qGroups {
			grp := &qGroups[gIdx]
			if (grp.flags&FlagHasUpper) != 0 && (grp.flags&FlagHasCamel) == 0 && !midSentUpper[grp.primaryH] && df[grp.primaryH] > totalDocs/12 {
				// Strip syntactic sentence-initial capitalization on frequent words ("What", "In", "During", "Did").
				grp.flags &^= FlagHasUpper
			}
			isProperRigid := (grp.flags&(FlagHasUpper|FlagHasCamel)) != 0 && (grp.flags&FlagHasDigit) == 0
			posting := qPostings[grp.primaryH]
			nPost := int64(len(posting))
			if nPost >= 2 && nPost <= 512 {
				seenDays := make(map[int64]bool, len(posting))
				for _, dIdx := range posting {
					if dIdx >= 0 && dIdx < len(docs) && docs[dIdx].occ > 0 {
						seenDays[docs[dIdx].occ/86400] = true
					}
				}
				dObs := int64(len(seenDays))
				if dObs >= 1 {
					eNullPerm := (nPost * estTotalDays * 1000) / (estTotalDays + nPost/2)
					if eNullPerm > 1000 && dObs*1000 < eNullPerm {
						rawK := 1000 - ((dObs*1000 - 1000) * 1000 / (eNullPerm - 1000))
						if rawK > 0 {
							conf := ((nPost - 1) * 1000) / (nPost + 2)
							grpKappaPerm[gIdx] = (rawK * conf) / 1000
						}
					}
				}
			}
			if (isProperRigid && grp.width >= 40 && nPost >= 1 && nPost <= int64(totalDocs/3)) || (grpKappaPerm[gIdx] >= 368 && nPost >= 2) {
				grpIsEventAnchor[gIdx] = true
				hasEventAnchor = true
				if anchorDocSet == nil {
					anchorDocSet = make(map[int]bool, 128)
					anchorDaySet = make(map[int64]bool, 64)
				}
				for _, vr := range grp.variants {
					if vr.scalePerm < 800 {
						continue
					}
					vp := qPostings[vr.h]
					if vr.scalePerm < 1000 && len(vp) > 512 {
						continue
					}
					for _, dIdx := range vp {
						anchorDocSet[dIdx] = true
						if dIdx >= 0 && dIdx < len(docs) && docs[dIdx].occ > 0 {
							anchorDaySet[docs[dIdx].occ/86400] = true
						}
					}
				}
			}
		}
	}

	for gIdx := range qGroups {
		grp := &qGroups[gIdx]
		isRigid := (grp.flags & (FlagHasUpper | FlagHasCamel | FlagHasDigit)) != 0
		pSurp := qSurprisal[grp.primaryH]
		if enableColloquialCondense && !isRigid && len(grp.variants) > 1 {
			minFamSurp := pSurp
			for _, vr := range grp.variants[1:] {
				if vr.scalePerm < 800 {
					continue
				}
				if s := qSurprisal[vr.h]; s > 0 && df[vr.h] > df[grp.primaryH] && s < minFamSurp {
					minFamSurp = s
				}
			}
			if minFamSurp < pSurp {
				pSurp = (632*pSurp + 368*minFamSurp) / 1000
			}
		}
		colloquialModPerm := int64(1000)
		if enableColloquialCondense && hasEventAnchor && !isRigid && !grpIsEventAnchor[gIdx] && grpKappaPerm[gIdx] < 135 {
			coHits := 0
			totalVarPost := 0
			for _, vr := range grp.variants {
				if vr.scalePerm < 800 {
					continue
				}
				vp := qPostings[vr.h]
				totalVarPost += len(vp)
				for _, dIdx := range vp {
					if anchorDocSet[dIdx] || (dIdx >= 0 && dIdx < len(docs) && docs[dIdx].occ > 0 && anchorDaySet[docs[dIdx].occ/86400]) {
						coHits++
						break
					}
				}
				if coHits > 0 {
					break
				}
			}
			// Orthogonal Scattered Colloquial Shell 0-Gate Continuous Extinction (aligned with KNN-FP-1):
			// Softly attenuate non-rigid tokens whose primary+stem family (scalePerm >= 800) scatters like
			// Poisson white noise across days (kappaPerm < 135 ≈ e^{-2}) AND has zero document or same-day
			// light-cone co-occurrence with the query's event anchors (coHits == 0), while preserving vr.isSal
			// (0-Gate invariant: never flip boolean salience or zero out synergy).
			if coHits == 0 && totalVarPost >= 2 {
				colloquialModPerm = 632
			}
		}
		effW := int64(grp.width)
		if isRigid {
			effW = effW * 5 / 4
		}
		jInfo := (pSurp * (effW + 40) / 128) * colloquialModPerm / 1000
		grpJointInfo[gIdx] = jInfo
		hasAnyCorpusHit := false
		isLowCarrier := hasHighWidthGroup && grp.width < 20
		for vIdx := range grp.variants {
			vr := &grp.variants[vIdx]
			docFreq := qEffDF[vr.h]
			if df[vr.h] > 0 {
				hasAnyCorpusHit = true
			}
			surp := qSurprisal[vr.h]
			if enableColloquialCondense && !isRigid && vIdx == 0 && pSurp < surp {
				surp = pSurp
			}
			vEffW := int64(grp.width) * vr.scalePerm / 1000
			if vEffW < 10 {
				vEffW = 10
			}
			fl := grp.flags
			if (fl & (FlagHasUpper | FlagHasCamel | FlagHasDigit)) != 0 {
				vEffW = vEffW * 5 / 4
			}
			surp2 := surp * surp / 1024
			g15 := vEffW * (surp * (1024 + surp/2) / 1024) / 32
			g20 := vEffW * surp2 / 64
			sGain := ((g15 + g20) / 2) * colloquialModPerm / 1000
			if totalDocs >= 16 && docFreq > totalDocs/2 && (fl&(FlagHasDigit|FlagHasUpper|FlagHasCamel)) == 0 {
				sGain /= 3
			}
			if isLowCarrier {
				sGain = 1
			}
			vr.shannonGain = sGain
			vr.isSal = !isLowCarrier && (totalDocs < 12 || docFreq <= totalDocs/3)
			vr.jSurp = ((surp * vr.scalePerm / 1000) * (vEffW + 40) / 128) * colloquialModPerm / 1000
			vr.wSurp = (surp * vr.scalePerm / 1000) * colloquialModPerm / 1000
		}
		if !isLowCarrier && hasAnyCorpusHit && (totalDocs < 12 || qEffDF[grp.primaryH] <= totalDocs/3) {
			totalQueryInfo += jInfo
			salientJList = append(salientJList, jInfo)
		}
	}
	if len(salientJList) > 5 {
		sort.Slice(salientJList, func(i, j int) bool { return salientJList[i] > salientJList[j] })
		totalQueryInfo = 0
		for i := 0; i < 5; i++ {
			totalQueryInfo += salientJList[i]
		}
	}

	var hits []CloudQueryHit
	bestDay := int64(-1)
	var bestScore float64

	candSeen := make([]bool, totalDocs)
	candCount := 0
	for _, cutoff := range []int{384, 2048, totalDocs + 1} {
		for _, grp := range qGroups {
			for _, vr := range grp.variants {
				if totalDocs >= 16 && qEffDF[vr.h] > totalDocs/3 && (grp.flags&(FlagHasDigit|FlagHasUpper|FlagHasCamel)) == 0 {
					continue
				}
				if totalDocs >= 2048 && len(qPostings[vr.h]) > cutoff {
					continue
				}
				for _, idx := range qPostings[vr.h] {
					if !candSeen[idx] {
						candSeen[idx] = true
						candCount++
					}
				}
			}
		}
		if candCount >= topK*6 && candCount >= 128 {
			break
		}
	}
	if candCount < topK*2 {
		for _, grp := range qGroups {
			for _, vr := range grp.variants {
				for _, idx := range qPostings[vr.h] {
					if !candSeen[idx] {
						candSeen[idx] = true
						candCount++
					}
				}
			}
		}
	}

	var docSalientBuf [32]int
	var curSalientBuf [32]int
	var prevSalientBuf [32]int
	avgDL64 := int64(avgDL)
	baseDenom := 960 * avgDL64
	numFactor := 2200 * avgDL64 * 1000

	for docIdx, isCand := range candSeen {
		if !isCand {
			continue
		}
		d := docs[docIdx]
		var scoreInt int64
		matchedTerms := 0
		salientMatched := 0
		matchedEventAnchors := 0
		var sumSurprisal, sumSqSurprisal int64
		var matchedQueryInfo int64
		docSalientGrps := docSalientBuf[:0]
		docBaseDenom := baseDenom + 240*int64(d.docLen)

		for gIdx, grp := range qGroups {
			var bestGrpScore int64
			var bestGrpSurp int64
			bestGrpSalient := false
			for _, vr := range grp.variants {
				tf := d.termCounts[vr.h]
				if tf <= 0 {
					continue
				}
				denom := int64(tf)*avgDL64*1000 + docBaseDenom
				tfNormPermille := int64(1000)
				if denom > 0 {
					tfNormPermille = int64(tf) * numFactor / denom
				}
				candScore := vr.shannonGain * tfNormPermille / 1000
				if candScore > bestGrpScore {
					bestGrpScore = candScore + bestGrpScore*350/1000
					bestGrpSurp = vr.jSurp
					bestGrpSalient = vr.isSal
				} else {
					bestGrpScore += candScore * 350 / 1000
				}
			}
			if bestGrpScore > 0 {
				scoreInt += bestGrpScore
				if !(hasHighWidthGroup && grp.width < 20) {
					matchedTerms++
				}
				if grpIsEventAnchor[gIdx] {
					matchedEventAnchors++
				}
				if bestGrpSalient {
					salientMatched++
					sumSurprisal += bestGrpSurp
					sumSqSurprisal += bestGrpSurp * bestGrpSurp
					matchedQueryInfo += bestGrpSurp
					docSalientGrps = append(docSalientGrps, gIdx)
				}
			}
		}
		// Pairwise Joint Shannon Surprisal Synergy: 2 * Σ_{i<j} J(h_i)*J(h_j) = (Σ J)^2 - Σ (J^2)
		inAnchorLightCone := matchedEventAnchors > 0 || (d.occ > 0 && anchorDaySet[d.occ/86400])
		if salientMatched >= 2 {
			pairSynergy := (sumSurprisal*sumSurprisal - sumSqSurprisal) / 2
			synergyBoostPermille := int64(salientMatched-1)*320 + pairSynergy/(4096*4)
			maxSynergyPermille := int64(2400)
			if enableColloquialCondense && hasEventAnchor && !inAnchorLightCone {
				maxSynergyPermille = 883
			}
			if synergyBoostPermille > maxSynergyPermille {
				synergyBoostPermille = maxSynergyPermille
			}
			scoreInt += scoreInt * synergyBoostPermille / 1000
		} else if matchedTerms >= 2 {
			scoreInt += scoreInt * int64(matchedTerms-1) / 5
		}
		// Quadratic Shannon Information Coverage Ratio: (I(Q ∩ d) / H(Q))^2
		if totalQueryInfo > 0 && salientMatched >= 2 {
			covPerm := matchedQueryInfo * 1000 / totalQueryInfo
			if covPerm > 1000 {
				covPerm = 1000
			}
			covQuad := covPerm * covPerm / 1000
			scoreInt += scoreInt * (covQuad * 400 / 1000) / 1000
		}
		// Sentence-window Shannon wave-packet focus & 2-sentence sliding wave-packet synergy
		if salientMatched >= 2 && len(d.sentHashes) > 1 && (totalDocs < 2048 || len(d.sentHashes) <= 96 || matchedEventAnchors > 0) {
			var maxWindowSurp int64
			var maxWin2Synergy int64
			maxSentSalient := 0
			prevSentSurp := int64(0)
			prevSentSalient := prevSalientBuf[:0]
			hasHeaderSalient := false
			hasBodySalient := false
			sentScanLim := len(d.sentHashes)
			if totalDocs >= 2048 && sentScanLim > 96 {
				sentScanLim = 96
			}
			for sIdx := 0; sIdx < sentScanLim; sIdx++ {
				sh := d.sentHashes[sIdx]
				cnt := 0
				var curSentSurp int64
				curSentSalient := curSalientBuf[:0]
				for _, gIdx := range docSalientGrps {
					grp := qGroups[gIdx]
					for _, vr := range grp.variants {
						if vr.isSal && d.termCounts[vr.h] > 0 && containsHashUint64(sh, vr.h) {
							cnt++
							curSentSurp += vr.wSurp
							curSentSalient = append(curSentSalient, gIdx)
							if sIdx <= 4 {
								hasHeaderSalient = true
							} else {
								hasBodySalient = true
							}
							break
						}
					}
				}
				if cnt > maxSentSalient {
					maxSentSalient = cnt
				}
				if winSurp := curSentSurp + prevSentSurp; winSurp > maxWindowSurp {
					maxWindowSurp = winSurp
				}
				if len(curSentSalient)+len(prevSentSalient) >= 2 {
					var wSum, wSq int64
					for _, gIdx := range curSentSalient {
						j := grpJointInfo[gIdx]
						wSum += j
						wSq += j * j
					}
					for _, gIdx := range prevSentSalient {
						dup := false
						for _, cg := range curSentSalient {
							if cg == gIdx {
								dup = true
								break
							}
						}
						if !dup {
							j := grpJointInfo[gIdx]
							wSum += j
							wSq += j * j
						}
					}
					if syn := (wSum*wSum - wSq) / 2; syn > maxWin2Synergy {
						maxWin2Synergy = syn
					}
				}
				prevSentSurp = curSentSurp
				prevSentSalient = append(prevSalientBuf[:0], curSentSalient...)
			}
			if maxSentSalient >= 2 {
				scoreInt += scoreInt * int64(maxSentSalient-1) / 4
			} else if sumSurprisal > 0 && maxWindowSurp*10 >= sumSurprisal*8 {
				// Adjacent-sentence window captures >=80% of matched Shannon surprisal
				scoreInt += scoreInt / 8
			}
			if maxWin2Synergy > 0 {
				winBoost := maxWin2Synergy / (4096 * 6)
				if winBoost > 600 {
					winBoost = 600
				}
				scoreInt += scoreInt * winBoost / 1000
			}
			// Cross-zone resonance: macro-header (sIdx <= 4) + body detail (sIdx > 4) coherence
			if hasHeaderSalient && hasBodySalient {
				scoreInt += scoreInt * 80 / 1000
			}
		}
		if scoreInt > 0 {
			wScore := float64(scoreInt) / 10.0
			if wScore > bestScore && d.occ > 0 {
				bestScore = wScore
				bestDay = d.occ / 86400
			}
			hits = append(hits, CloudQueryHit{
				UnitID:         d.docID,
				Layer:          d.layer,
				OccurredAt:     d.occ,
				AddressURI:     d.addrURI,
				AddressCoord64: d.coord64,
				WaveformBar:    d.waveBar,
				WaveScore:      wScore,
				FinalWeight:    wScore,
			})
		}
	}

	// Same-day session continuity boost for multi-document events
	if bestDay > 0 && len(hits) > 1 {
		for i := range hits {
			if di, ok := docIdxMap[hits[i].UnitID]; ok && docs[di].occ > 0 && (docs[di].occ/86400) == bestDay {
				hits[i].FinalWeight += 1.5
			}
		}
	}

	// Scan local Layer 3 (`thin_vault_noetic`: Agent retrieval handles `cited_unit_ids[]` + Answer stored locally)
	// and automatically boost/hydrate any underlying `episodic` or `background` units cited by matched Noetic records!
	noeticHandles, _ := (&Client{Vault: v}).RecallNoetic(queryText, topK)
	if len(noeticHandles) > 0 {
		hitIdxByID := make(map[string]int, len(hits))
		for i := range hits {
			hitIdxByID[hits[i].UnitID] = i
		}
		noeticScale := bestScore
		if noeticScale < 1.0 {
			noeticScale = 1.0
		}
		for _, rec := range noeticHandles {
			nWeight := rec.MatchScore
			if nWeight <= 0 {
				nPkt := v.noeticBlindPacket(rec.NoeticID, rec.QueryText, rec.OccurredAt)
				nWeight = blindPacketDice(qPkt, nPkt)
			}
			if nWeight > 0 {
				scaledNoeticW := noeticScale * nWeight
				hits = append(hits, CloudQueryHit{
					UnitID:       rec.NoeticID,
					Layer:        LayerNoetic,
					OccurredAt:   rec.OccurredAt,
					CitedUnitIDs: rec.CitedUnitIDs,
					WaveScore:    scaledNoeticW,
					FinalWeight:  scaledNoeticW,
				})
				for _, cid := range rec.CitedUnitIDs {
					if idx, exists := hitIdxByID[cid]; exists {
						hits[idx].FinalWeight += scaledNoeticW * 0.35
					} else if di, ok := docIdxMap[cid]; ok {
						d := docs[di]
						hitIdxByID[cid] = len(hits)
						hits = append(hits, CloudQueryHit{
							UnitID:         d.docID,
							Layer:          d.layer,
							OccurredAt:     d.occ,
							AddressURI:     d.addrURI,
							AddressCoord64: d.coord64,
							WaveformBar:    d.waveBar,
							WaveScore:      scaledNoeticW * 0.35,
							FinalWeight:    scaledNoeticW * 0.35,
						})
					}
				}
			}
		}
	}

	// Deterministic sort by FinalWeight DESC, UnitID ASC.
	// Partition and sort the full prefix consumed by event-chain resonance and post-chain
	// energy-spectral fusion (`fusionDepth*2`, where `fusionDepth = clamp(topK*8, 128, 512)`),
	// so every element entering resonance/fusion is ordered by energy while tens of thousands
	// of low-weight tail items avoid an O(N log N) full sort.
	headSortDepth := topK * 16
	if headSortDepth < 256 {
		headSortDepth = 256
	}
	if headSortDepth > 1024 {
		headSortDepth = 1024
	}
	sortTopCloudHits(hits, headSortDepth)

	// Event-Chain Resonance (Rank-Index Blind Coherent Energy Hybridization):
	// Couples candidate units to higher-energy event anchors via temporal proximity tau(dt),
	// dimensionless Shannon co-occurrence coherence (infoDice, nuEff) and shared rare lead-sentence
	// context tokens. Pure energy evaluation: zero dependence on rank index or fixed slot scores.
	if len(hits) > 2 && totalDocs >= 16 {
		maxW := hits[0].FinalWeight
		if maxW <= 0 {
			maxW = 1.0
		}
		// Pure energy superlevel sets, rank-index blind (zero positional windows, zero slot scores):
		//   core day-saturation set  := { v : E(v) >= e^{-1/2} * E_max }
		//   radiative anchor set     := { v : E(v) >= e^{-1}   * E_max }
		//   candidate horizon        := { v : E(v) >= e^{-5/2} * E_max }
		//   episode mass             := count of anchor-grade frames within +/-4h of the anchor
		// E_max is the continuous argmax energy.
		coreDayFloor := maxW * 0.6065
		highEnergyFloor := maxW * 0.3679
		candidateFloor := maxW * 0.0821
		highEnergyDayMass := make(map[int64]int, 16)
		highIdx := make([]int, 0, 32)
		maxHighAnchors := 24
		if totalDocs >= 16384 {
			maxHighAnchors = 6
		}
		for idx := range hits {
			if len(highIdx) >= maxHighAnchors {
				break
			}
			if hits[idx].FinalWeight < highEnergyFloor {
				continue
			}
			if dIdx, ok := docIdxMap[hits[idx].UnitID]; ok && docs[dIdx].occ > 0 {
				if hits[idx].FinalWeight >= coreDayFloor {
					highEnergyDayMass[docs[dIdx].occ/86400]++
				}
				highIdx = append(highIdx, idx)
			}
		}
		anchorLeadHashes := make(map[uint64]int, 64)
		anchorEpisodeMass := make(map[string]int, 16)
		anchorSet := make([]int, 0, 16)
		for _, a := range highIdx {
			da, ok := docIdxMap[hits[a].UnitID]
			if !ok || docs[da].occ <= 0 {
				continue
			}
			mass := 0
			for _, j := range highIdx {
				dj, okJ := docIdxMap[hits[j].UnitID]
				if !okJ || docs[dj].occ <= 0 {
					continue
				}
				dt := docs[dj].occ - docs[da].occ
				if dt < 0 {
					dt = -dt
				}
				if dt <= 14400 {
					mass++
				}
			}
			anchorEpisodeMass[hits[a].UnitID] = mass
			anchorSet = append(anchorSet, a)
			limS := len(docs[da].sentHashes)
			if limS > 5 {
				limS = 5
			}
			for sIdx := 1; sIdx < limS; sIdx++ {
				for _, h := range docs[da].sentHashes[sIdx] {
					anchorLeadHashes[h] = df[h]
				}
			}
		}
		if len(anchorLeadHashes) > 0 {
			anyBoosted := false
			scanned := 0
			scanCap := len(hits)
			maxScanCap := 256
			if totalDocs >= 16384 {
				maxScanCap = 40
			}
			if scanCap > maxScanCap {
				scanCap = maxScanCap
			}
			// Salient info mass is a pure per-document function of (termCounts, df):
			// cache it per doc index so the anchor×candidate double loop computes
			// each side once instead of |anchorSet|×|scanCap| times. The shared
			// mass stays pairwise (iterating the smaller side), and the final
			// divisions run on the same integers, so the pair (infoDice, nuEff)
			// is bitwise identical to the fused single-call form.
			salientMassByDoc := make(map[int]int64, len(anchorSet)+scanCap)
			salientMassOf := func(dIdx int) int64 {
				if m, ok := salientMassByDoc[dIdx]; ok {
					return m
				}
				m := SalientInfoMass(docs[dIdx].termCounts, df, totalDocs, log2Total)
				salientMassByDoc[dIdx] = m
				return m
			}
			for i := 1; i < scanCap; i++ {
				if hits[i].FinalWeight < candidateFloor {
					continue
				}
				scanned = i + 1
				di, ok := docIdxMap[hits[i].UnitID]
				if !ok || docs[di].occ <= 0 {
					continue
				}
				bestBoosted := hits[i].FinalWeight
				bestAnchorID := ""
				for _, a := range anchorSet {
					if hits[a].FinalWeight <= hits[i].FinalWeight {
						continue
					}
					da, okA := docIdxMap[hits[a].UnitID]
					if !okA || docs[da].occ <= 0 {
						continue
					}
					dt := docs[di].occ - docs[da].occ
					if dt < 0 {
						dt = -dt
					}
					leadRare := 0
					leadUltraRare := 0
					limS := len(docs[di].sentHashes)
					if limS > 5 {
						limS = 5
					}
					for sIdx := 1; sIdx < limS; sIdx++ {
						for _, h := range docs[di].sentHashes[sIdx] {
							if docs[da].termCounts[h] > 0 && anchorLeadHashes[h] >= 2 && anchorLeadHashes[h] <= totalDocs/12 {
								leadRare++
								if anchorLeadHashes[h] <= totalDocs/24 {
									leadUltraRare++
								}
							}
						}
					}
					infoDice, nuEff := CoherenceFromMasses(
						salientMassOf(da),
						salientMassOf(di),
						SharedSalientInfoMass(docs[da].termCounts, docs[di].termCounts, df, totalDocs, log2Total),
						log2Total,
					)
					tau := TemporalProximityKernel(dt, 1800.0)
					underSat := dt > 3600 && dt <= 14400 && nuEff >= 9.5 && highEnergyDayMass[docs[di].occ/86400] <= 1 && anchorEpisodeMass[hits[a].UnitID] <= 3 && hits[i].FinalWeight >= maxW*0.368
					crossDayCoref := dt > 21600 && nuEff >= 35.0 && infoDice >= 0.20 && anchorEpisodeMass[hits[a].UnitID] <= 2
					burstTwin := tau >= 0.93 && nuEff >= 35.0 && infoDice >= 0.20
					burstCluster := tau >= 0.93 && nuEff >= 10.5
					denseSession := dt <= 3600 && nuEff >= 38.0 && hits[i].FinalWeight >= maxW*0.33
					highCoh := underSat || crossDayCoref || burstTwin || burstCluster || denseSession

					if !highCoh {
						if hits[a].FinalWeight < maxW*0.60 || hits[i].FinalWeight < maxW*0.30 {
							continue
						}
						if anchorEpisodeMass[hits[a].UnitID] >= 3 || dt > 21600 {
							continue
						}
						if dt > 3600 {
							if leadUltraRare < 4 || anchorEpisodeMass[hits[a].UnitID] >= 2 || hits[i].FinalWeight < maxW*0.42 {
								continue
							}
						} else if leadRare < 2 {
							continue
						}
					} else if anchorEpisodeMass[hits[a].UnitID] >= 6 || (hits[i].FinalWeight < maxW*0.1353 && anchorEpisodeMass[hits[a].UnitID] >= 3) {
						continue
					}

					var boostPerm float64
					if dt <= 600 {
						boostPerm = 280.0
					} else if dt <= 3600 {
						boostPerm = 160.0
						if highCoh && nuEff >= 38.0 {
							boostPerm = 260.0
						}
					} else if underSat {
						boostPerm = 260.0
					} else {
						boostPerm = 120.0
					}
					if leadRare >= 4 || highCoh {
						boostPerm *= 1.30
					}
					candW := hits[i].FinalWeight * (1.0 + boostPerm/1000.0)
					// Coherent Two-State Energy Hybridization for phase-locked twin / coreferent frames:
					// transmits anchor energy strictly via coherence saturation nuEff/(nuEff+16) and cluster
					// saturation damping 1/(1 + 0.368*m_a), with zero reference to any rank slot.
					if crossDayCoref || burstTwin {
						cohSat := nuEff / (nuEff + 16.0)
						clusterDamp := 1.0 / (1.0 + 0.368*float64(anchorEpisodeMass[hits[a].UnitID]))
						trans := 0.368 * cohSat * clusterDamp
						hybridW := hits[i].FinalWeight*(1.0+0.368*cohSat) + trans*hits[a].FinalWeight
						if hybridW > candW {
							candW = hybridW
						}
					}
					capW := hits[a].FinalWeight * 0.9817
					if candW > capW {
						candW = capW
					}
					if candW > bestBoosted {
						bestBoosted = candW
						bestAnchorID = hits[a].UnitID
					}
				}
				if bestAnchorID != "" && bestBoosted > hits[i].FinalWeight {
					wasBelowHigh := hits[i].FinalWeight < highEnergyFloor
					wasBelowCoreDay := hits[i].FinalWeight < coreDayFloor
					hits[i].FinalWeight = bestBoosted
					hits[i].WaveScore = bestBoosted
					anyBoosted = true
					if bestBoosted >= highEnergyFloor {
						daBest, okBest := docIdxMap[bestAnchorID]
						dtBest := int64(0)
						if okBest {
							dtBest = docs[di].occ - docs[daBest].occ
							if dtBest < 0 {
								dtBest = -dtBest
							}
						}
						if wasBelowHigh || dtBest > 14400 {
							anchorEpisodeMass[bestAnchorID]++
						}
					}
					if wasBelowCoreDay && bestBoosted >= coreDayFloor {
						highEnergyDayMass[docs[di].occ/86400]++
					}
				}
			}
			if anyBoosted {
				sub := hits[:scanned]
				sort.Slice(sub, func(i, j int) bool {
					if sub[i].FinalWeight != sub[j].FinalWeight {
						return sub[i].FinalWeight > sub[j].FinalWeight
					}
					return sub[i].UnitID < sub[j].UnitID
				})
			}
		}
	}

	// Energy-Spectral Fusion over two independent lexical channels of the same vault:
	//   1. the blind-slot ranker (Shannon/BM25 with BPE width + sentence-window boosts) — primary;
	//   2. the SQLite FTS5 trigram BM25 ranker — continuous substring / morphological corroboration.
	// Evaluated purely from normalized primary energy and Michaelis-Menten mean-saturated FTS5 BM25
	// energy (100% rank-index blind: zero discrete rank indices, zero fixed-slot floors).
	if len(hits) > 1 {
		fusionDepth := topK * 8
		if fusionDepth < 128 {
			fusionDepth = 128
		}
		if fusionDepth > 512 {
			fusionDepth = 512
		}
		if len(hits) > fusionDepth*2 {
			hits = hits[:fusionDepth*2]
		}
		var ftsScores map[string]float64
		var maxFTS float64
		if v.HasFTS5BM25() && (totalDocs < 16384 || len(hits) < topK*2) {
			if scores, maxS, ferr := v.queryFTS5BM25ScoresWithDF(queryText, fusionDepth, totalDocs, df, snap.ftsRowIDToDocID); ferr == nil {
				ftsScores = scores
				maxFTS = maxS
			}
		}
		if len(ftsScores) > 0 && maxFTS > 0 {
			maxW := hits[0].FinalWeight
			if maxW <= 0 {
				maxW = 1.0
			}
			// Continuum pedestal subtraction on FTS5 trigram BM25 energy:
			// FTS5 trigram phrase OR-queries sit on a non-zero continuum floor B_min across the candidate window.
			// Subtracting B_min isolates the net excitation energy DeltaB(d) = B(d) - B_min >= 0 above background.
			minFTS := maxFTS
			for _, s := range ftsScores {
				if s > 0 && s < minFTS {
					minFTS = s
				}
			}
			spanFTS := maxFTS - minFTS
			var sumNetFTS, sumNetFTSSq float64
			if spanFTS > 1e-9 {
				for _, s := range ftsScores {
					if dB := s - minFTS; dB > 0 {
						sumNetFTS += dB
						sumNetFTSSq += dB * dB
					}
				}
			}
			charNetFTS := spanFTS * 0.3679
			if sumNetFTS > 0 {
				charNetFTS = sumNetFTSSq / sumNetFTS
			}
			isDocInAnchorCone := func(id string) bool {
				if !enableColloquialCondense || !hasEventAnchor {
					return true
				}
				di, ok := docIdxMap[id]
				if !ok {
					return false
				}
				d := docs[di]
				if d.occ > 0 && anchorDaySet[d.occ/86400] {
					return true
				}
				for gIdx, grp := range qGroups {
					if !grpIsEventAnchor[gIdx] {
						continue
					}
					for _, vr := range grp.variants {
						if d.termCounts[vr.h] > 0 {
							return true
						}
					}
				}
				return false
			}
			var sumSlot, sumSlotSq float64
			for _, h := range hits {
				if h.FinalWeight > 0 {
					sumSlot += h.FinalWeight
					sumSlotSq += h.FinalWeight * h.FinalWeight
				}
			}
			charSlot := maxW * 0.3679
			if sumSlot > 0 {
				charSlot = sumSlotSq / sumSlot
			}
			fused := make(map[string]float64, len(hits)+len(ftsScores))
			for _, h := range hits {
				w := h.FinalWeight
				if w < 0 {
					w = 0
				}
				linSlot := w / maxW
				satSlot := (w * (maxW + charSlot)) / (maxW * (w + charSlot))
				fused[h.UnitID] = 0.6321*linSlot + 0.3679*satSlot
			}
			for id, ftsRaw := range ftsScores {
				var phiFTS float64
				if spanFTS > 1e-9 {
					dB := ftsRaw - minFTS
					if dB < 0 {
						dB = 0
					}
					uFTS := dB / spanFTS
					satFTS := (dB * (spanFTS + charNetFTS)) / (spanFTS * (dB + charNetFTS))
					phiFTS = uFTS * satFTS
				} else {
					phiFTS = 1.0
				}
				slotNorm := fused[id]
				kappa := 0.0498 // e^-3 outside anchor cone
				if isDocInAnchorCone(id) {
					kappa = 0.2707 // 2*e^-2 inside anchor cone
				}
				fused[id] += kappa * phiFTS * (0.3679 + 0.6321*slotNorm)
			}
			merged := make([]CloudQueryHit, 0, len(hits)+len(ftsScores))
			seen := make(map[string]bool, len(fused))
			for _, h := range hits {
				if seen[h.UnitID] {
					continue
				}
				seen[h.UnitID] = true
				fw := fused[h.UnitID] * maxW
				h.FinalWeight = fw
				h.WaveScore = fw
				merged = append(merged, h)
			}
			for id := range ftsScores {
				if seen[id] {
					continue
				}
				di, ok := docIdxMap[id]
				if !ok {
					continue
				}
				seen[id] = true
				d := docs[di]
				fw := fused[id] * maxW
				merged = append(merged, CloudQueryHit{
					UnitID:         d.docID,
					Layer:          d.layer,
					OccurredAt:     d.occ,
					AddressURI:     d.addrURI,
					AddressCoord64: d.coord64,
					WaveformBar:    d.waveBar,
					WaveScore:      fw,
					FinalWeight:    fw,
				})
			}
			sort.Slice(merged, func(i, j int) bool {
				if merged[i].FinalWeight != merged[j].FinalWeight {
					return merged[i].FinalWeight > merged[j].FinalWeight
				}
				return merged[i].UnitID < merged[j].UnitID
			})
			hits = merged
		}
	}

	// Submodular Near-Clone Marginal Novelty: attenuate redundant cross-day (>=85%) and
	// 3rd+ same-day (>=92%) near-clone copies so a single clone cluster cannot monopolize Top-K delivery slots.
	if len(hits) > 1 {
		checkLim := topK * 2
		if checkLim < 24 {
			checkLim = 24
		}
		if len(hits) < checkLim {
			checkLim = len(hits)
		}
		priorAt := func(j int) (map[uint64]int, int64, bool) {
			dj, okJ := docIdxMap[hits[j].UnitID]
			if !okJ {
				return nil, 0, false
			}
			return docs[dj].termCounts, docs[dj].occ, true
		}
		demoted := false
		for i := 1; i < checkLim; i++ {
			di, okI := docIdxMap[hits[i].UnitID]
			if !okI {
				continue
			}
			if atten := SubmodularCloneAttenuation(docs[di].termCounts, docs[di].occ, i, priorAt); atten < 1.0 {
				hits[i].FinalWeight *= atten
				demoted = true
			}
		}
		if demoted {
			sub := hits[:checkLim]
			sort.Slice(sub, func(i, j int) bool {
				if sub[i].FinalWeight != sub[j].FinalWeight {
					return sub[i].FinalWeight > sub[j].FinalWeight
				}
				return sub[i].UnitID < sub[j].UnitID
			})
		}
	}

	if len(hits) > 1 {
		hits = applyZhaoRMEnergyFusion(snap, qPkt, topK, docs, docIdxMap, hits)
	}

	if len(hits) > topK {
		hits = selectKernelSmoothedHits(hits, topK)
	}
	return hits, nil
}

// windowsURIPath reports the file: URI path form of a Windows drive or UNC
// path, rewriting the separators to forward slashes; other paths return
// ok=false unchanged. An unanchored `C:\...` renders as file://C:%5C..., the
// drive letter becomes the URI authority, and the driver rejects the DSN with
// "invalid uri authority".
func windowsURIPath(path string) (string, bool) {
	if len(path) >= 2 && path[1] == ':' {
		if c := path[0]; 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' {
			return "/" + strings.ReplaceAll(path, `\`, "/"), true
		}
	}
	if strings.HasPrefix(path, `\\`) {
		return strings.ReplaceAll(path, `\`, "/"), true
	}
	return "", false
}

// zcodeReadHandle caches one read-only handle on the ZCode session store.
// Resolving a zero-copy pointer used to open (and close) a SQLite connection per
// call: a multi-hundred-unit reconcile pass paid a schema read and a WAL index load
// per pointer, which dominated the pass. The handle is read-only, single-connection
// and short-lived when idle, so it never holds a read mark that would block the
// host's checkpointing.
var (
	zcodeReadOnce sync.Once
	zcodeReadDB   *sql.DB
	zcodeReadErr  error
)

func zcodeReadHandle(path string) (*sql.DB, error) {
	zcodeReadOnce.Do(func() {
		// Paths carrying URI metacharacters must be percent-encoded; otherwise the
		// driver silently parses the remainder as query or fragment and opens a
		// different file. Windows drive paths take the percent-encoded form as well
		// so the drive letter stays in the URI path instead of becoming the
		// authority. Inlined (not ids.SQLiteFileURI) because the ultra-thin client
		// must not import algorithm packages.
		dsnPath := "file:" + path
		if uriPath, ok := windowsURIPath(path); ok {
			dsnPath = (&url.URL{Scheme: "file", Path: uriPath}).String()
		} else if strings.ContainsAny(path, "?#") {
			dsnPath = (&url.URL{Scheme: "file", Path: path}).String()
		}
		handle, err := sql.Open("sqlite", dsnPath+"?mode=ro&_pragma=busy_timeout(1500)&_pragma=cache_size(-32768)&_pragma=temp_store(MEMORY)")
		if err != nil {
			zcodeReadErr = err
			return
		}
		handle.SetMaxOpenConns(2)
		handle.SetMaxIdleConns(2)
		handle.SetConnMaxIdleTime(0)
		handle.SetConnMaxLifetime(0)
		zcodeReadDB = handle
	})
	return zcodeReadDB, zcodeReadErr
}

func resolveZCodeTurnLive(sessionID, userMsgID string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", errors.New("thinclient: cannot resolve user home directory")
	}
	zdbPath := filepath.Join(home, ".zcode", "cli", "db", "db.sqlite")
	if _, err := os.Stat(zdbPath); err != nil {
		return "", err
	}
	zdb, err := zcodeReadHandle(zdbPath)
	if err != nil {
		return "", err
	}

	var startSeq sql.NullInt64
	if err := zdb.QueryRow(`SELECT sequence FROM message WHERE id = ? AND session_id = ?`, userMsgID, sessionID).Scan(&startSeq); err != nil {
		return "", err
	}
	var mRows *sql.Rows
	if startSeq.Valid {
		mRows, err = zdb.Query(`
			SELECT m.id, json_extract(m.data, '$.role') AS role
			FROM message m
			WHERE m.session_id = ? AND m.sequence >= ?
			ORDER BY m.sequence ASC LIMIT 80`, sessionID, startSeq.Int64)
	} else {
		mRows, err = zdb.Query(`
			SELECT m.id, json_extract(m.data, '$.role') AS role
			FROM message m
			WHERE m.session_id = ?
			ORDER BY m.sequence ASC LIMIT 80`, sessionID)
	}
	if err != nil {
		return "", err
	}
	type msgHdr struct{ id, role string }
	var hdrs []msgHdr
	for mRows.Next() {
		var mid, role sql.NullString
		if mRows.Scan(&mid, &role) == nil {
			hdrs = append(hdrs, msgHdr{id: mid.String, role: strings.ToLower(strings.TrimSpace(role.String))})
		}
	}
	mRows.Close()

	var userParts []string
	var assistParts []string
	for _, h := range hdrs {
		pRows, pErr := zdb.Query(`
			SELECT json_extract(p.data, '$.text') AS txt
			FROM part p
			WHERE p.message_id = ?
			  AND `+episodic.ZCodeTextPartFilter+`
			ORDER BY p.sequence ASC LIMIT 80`, h.id)
		if pErr != nil {
			continue
		}
		var parts []string
		for pRows.Next() {
			var txt sql.NullString
			if pRows.Scan(&txt) == nil {
				if cleaned := strings.TrimSpace(txt.String); cleaned != "" {
					parts = append(parts, cleaned)
				}
			}
		}
		pRows.Close()
		if len(parts) == 0 {
			continue
		}
		if h.id == userMsgID || (h.role == "user" && len(userParts) == 0) {
			userParts = append(userParts, parts...)
		} else if h.role == "user" && len(userParts) > 0 {
			break
		} else if h.role == "assistant" {
			assistParts = append(assistParts, parts...)
		}
	}
	if len(userParts) == 0 && len(assistParts) == 0 {
		return "", errors.New("thinclient: empty live zcode turn")
	}
	if len(assistParts) == 0 {
		return "[user] " + strings.Join(userParts, "\n"), nil
	}
	return "[user] " + strings.Join(userParts, "\n") + "\n[assistant] " + strings.Join(assistParts, "\n"), nil
}

// maxLiveJSONLReadBytes caps one live JSONL evidence file read during pointer
// hydration: host transcripts are bounded in practice, and the cap keeps a
// hand-crafted ref (/dev/zero, a huge file) from exhausting memory.
const maxLiveJSONLReadBytes int64 = 256 * 1024 * 1024

func resolveJSONLTurnLive(absPath, turnStr string) (string, error) {
	targetTurn, err := strconv.Atoi(strings.TrimSpace(turnStr))
	if err != nil || targetTurn < 0 {
		return "", fmt.Errorf("invalid turn index %q", turnStr)
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("refusing to hydrate from non-regular file %s", absPath)
	}
	if info.Size() > maxLiveJSONLReadBytes {
		return "", fmt.Errorf("jsonl evidence file exceeds %d bytes: %s", maxLiveJSONLReadBytes, absPath)
	}
	raw, err := os.ReadFile(absPath)
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(raw), "\n")
	var curUser string
	var curAssist []string
	turnIdx := 0

	extractLineRoleAndText := func(line string) (string, string) {
		line = strings.TrimSpace(line)
		if line == "" {
			return "", ""
		}
		var obj map[string]any
		if json.Unmarshal([]byte(line), &obj) != nil || obj == nil {
			return "", ""
		}
		msg, _ := obj["message"].(map[string]any)
		if msg == nil {
			if payload, ok := obj["payload"].(map[string]any); ok && payload != nil {
				if pType, _ := payload["type"].(string); pType == "message" || payload["role"] != nil {
					msg = payload
				}
			}
		}
		if msg == nil {
			msg = obj
		}
		role, _ := msg["role"].(string)
		if role == "" {
			role, _ = obj["type"].(string)
		}
		role = strings.ToLower(strings.TrimSpace(role))
		if role != "user" && role != "assistant" {
			return "", ""
		}
		var parts []string
		switch c := msg["content"].(type) {
		case string:
			parts = append(parts, strings.TrimSpace(c))
		case []any:
			for _, blk := range c {
				if m, ok := blk.(map[string]any); ok {
					t, _ := m["type"].(string)
					if t == "text" || t == "input_text" || t == "output_text" {
						if s, ok := m["text"].(string); ok && strings.TrimSpace(s) != "" {
							parts = append(parts, strings.TrimSpace(s))
						}
					}
				}
			}
		}
		txt := strings.TrimSpace(strings.Join(parts, "\n"))
		if strings.HasPrefix(txt, "<app-context>") || strings.HasPrefix(txt, "# AGENTS.md instructions for ") ||
			strings.HasPrefix(txt, "<environment_context>") {
			return "", ""
		}
		return role, txt
	}

	for _, line := range lines {
		role, txt := extractLineRoleAndText(line)
		if role == "" || txt == "" {
			continue
		}
		if role == "user" {
			if curUser != "" && len(curAssist) > 0 {
				if turnIdx == targetTurn {
					return "[user] " + curUser + "\n[assistant] " + strings.Join(curAssist, "\n"), nil
				}
				turnIdx++
			}
			curUser = txt
			curAssist = nil
		} else if role == "assistant" && curUser != "" {
			curAssist = append(curAssist, txt)
		}
	}
	if curUser != "" && len(curAssist) > 0 && turnIdx == targetTurn {
		return "[user] " + curUser + "\n[assistant] " + strings.Join(curAssist, "\n"), nil
	}
	return "", fmt.Errorf("turn %d not found in %s", targetTurn, absPath)
}
