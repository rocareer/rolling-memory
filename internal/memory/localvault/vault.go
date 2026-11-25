// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package localvault

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
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rocareer/rolling-memory/internal/core"
	"github.com/rocareer/rolling-memory/internal/episodic"
	"github.com/rocareer/rolling-memory/internal/episodicunit"
	"github.com/rocareer/rolling-memory/internal/harness"
	"github.com/rocareer/rolling-memory/internal/ids"
	plain "github.com/rocareer/rolling-memory/sdk/go/pulse/plain"
)

// ============================================================================
// Rolling-Memory Ultra-Thin Local Client (`internal/localvault`)
//
// Strict Security & Anti-Reverse-Engineering Invariants:
//   1. Lightweight Local Client:
//      This package keeps raw text and sentence arrays inside the local SQLite table `rm_units`.
//   2. Local Data Boundary:
//      All raw text and sentence arrays remain locked inside the local SQLite table `rm_units`.
//      Only `{H, RawBitsX10, ClassFlags}` integer atoms leave the vault when transport is enabled.
// ============================================================================

// Character class bitmask flags inspected mechanically on the local vault.
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

// BlindTokenAtom is the irreversible blind integer tuple emitted per token slot (canonical alias to plain.BlindTokenAtom).
type BlindTokenAtom = plain.BlindTokenAtom

// BlindPulsePacket is the wire request sent from the local vault.
// All raw plaintext stays on the client (`rm_units` / `rm_noetic`) across the three cognitive layers
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
	// RawText / PulseMode mirror the canonical sdk/go/local packet fields. Plain
	// mode keeps the text on the client so query extraction uses the same
	// TermExtractor as ingestion instead of reconstructing terms from atoms.
	RawText   string `json:"raw_text,omitempty"`
	PulseMode string `json:"pulse_mode,omitempty"`
}

// BlindQueryOptions defines the explicit control parameters for the local plaintext query.
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

// HitClusterGroup is one cognitive-cluster delivery group: a read-only
// regrouping of the ranked hits for downstream LLM consumption (protocol
// noetic-cluster-delivery-protocol-v1; hits themselves stay untouched).
type HitClusterGroup struct {
	GroupID         int      `json:"group_id"`
	Day             string   `json:"day,omitempty"`
	UnitIDs         []string `json:"unit_ids"`
	PeakUnitID      string   `json:"peak_unit_id"`
	JointEnergyNats float64  `json:"joint_energy_nats"`
}

// OrdinalAnnotation is the delivery-layer ordinal span day fact statement
// (first/last family) resolved from corpus time geometry under the single-run
// high-confidence gate. Informational only; it never alters ranking.
type OrdinalAnnotation struct {
	Polarity    string `json:"polarity"`
	ResolvedDay string `json:"resolved_day"`
	SingleRun   bool   `json:"single_run"`
	RunSpanDays int    `json:"run_span_days"`
	AnchorDF    int    `json:"anchor_df"`
}

// DeliveryView bundles the optional cluster delivery artifacts of the most
// recent envelope-mode query (empty when the view was not requested).
type DeliveryView struct {
	ClusterGroups     []HitClusterGroup  `json:"cluster_groups,omitempty"`
	OrdinalAnnotation *OrdinalAnnotation `json:"ordinal_annotation,omitempty"`
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

// SlotWeight is a per-slot weight of one packed pulse frame.
type SlotWeight struct {
	SlotIndex int     `json:"slot_index"`
	Weight    float64 `json:"weight"`
	Nibble    uint8   `json:"nibble"`
}

// IngestResponse is the numerical metadata accompanying one ingested document.
type IngestResponse struct {
	TenantID            string               `json:"tenant_id,omitempty"`
	Scope               string               `json:"scope,omitempty"`
	UnitID              string               `json:"id"`
	Layer               string               `json:"layer,omitempty"`
	CitedUnitIDs        []string             `json:"cited_unit_ids,omitempty"`
	Epoch               int                  `json:"epoch"`
	SlotWeights         []SlotWeight         `json:"slot_weights,omitempty"`
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

// QueryHit is a single ranked document ID and weight breakdown.
type QueryHit struct {
	UnitID         string   `json:"id"`
	Layer          string   `json:"layer,omitempty"`
	CitedUnitIDs   []string `json:"cited_unit_ids,omitempty"`
	OccurredAt     int64    `json:"occurred_at,omitempty"`
	NarratedAt     int64    `json:"narrated_at,omitempty"`
	IngestedAt     int64    `json:"ingested_at,omitempty"`
	AuthorHash     uint64   `json:"author_h,omitempty"`
	DocHash        uint64   `json:"doc_h,omitempty"`
	BlockHash      uint64   `json:"block_h,omitempty"`
	LineStart      int      `json:"line_start,omitempty"`
	LineEnd        int      `json:"line_end,omitempty"`
	LayerWeight    float64  `json:"layer_weight,omitempty"`
	AddressURI     string   `json:"address_uri"`
	AddressCoord64 uint64   `json:"address_coord64"`
	WaveformBar    string   `json:"waveform_bar"`
	WaveScore      float64  `json:"wave_score"`
	CellWormhole   float64  `json:"cell_wormhole"`
	ManifoldGain   float64  `json:"manifold_gain"`
	FinalWeight    float64  `json:"final_weight"`
	// DirectWeight is the candidate's own query-collision evidence (Nats), carried
	// from the kernel hit: delivery stopping uses it as the reference scale so the
	// Hop1/Hop2 energy borrowed from same-cluster neighbors cannot raise the
	// natural decay envelope against an isolated-date complementary record.
	DirectWeight        float64 `json:"direct_weight,omitempty"`
	PlasticityGate      float64 `json:"plasticity_gate,omitempty"`
	KeptSentenceIndices []int   `json:"kept_sentence_indices,omitempty"`
	CompressionRatio    float64 `json:"compression_ratio,omitempty"`
}

// LocalNoeticRecord represents a downstream-maintained L3 Noetic memory record (Query + Retrieved Sentences + Answer)
// stored in the client's own `rm_noetic` table.
type LocalNoeticRecord struct {
	NoeticID       string   `json:"noetic_id"`
	OccurredAt     int64    `json:"occurred_at"`
	InsertedAt     int64    `json:"inserted_at,omitempty"`
	Status         string   `json:"status"` // "active" | "stale"
	QueryText      string   `json:"query_text"`
	AnswerText     string   `json:"answer_text"`
	CitedUnitIDs   []string `json:"cited_unit_ids"`
	CitedSentences []string `json:"cited_sentences"`
	MatchScore     float64  `json:"match_score,omitempty"`
	ReuseCount     int      `json:"reuse_count,omitempty"`
	AddressURI     string   `json:"address_uri,omitempty"`
	Coord64        int64    `json:"coord64,omitempty"`
	WaveformBar    string   `json:"waveform_bar,omitempty"`
	// AssistantID / IncarnationID pin the crystallizing persona and its lifetime instance;
	// empty values fall back to the vault identity at save time.
	AssistantID   string `json:"assistant_id,omitempty"`
	IncarnationID string `json:"incarnation_id,omitempty"`
	// ActivatedAt / ActivationCount record real use: the last delivery that
	// actually took part, and how often that has happened. TraceKey is the
	// evidence-set identity; QueryVariants are the retained cue handles.
	ActivatedAt     int64    `json:"activated_at,omitempty"`
	ActivationCount int      `json:"activation_count,omitempty"`
	TraceKey        string   `json:"trace_key,omitempty"`
	QueryVariants   []string `json:"query_variants,omitempty"`
	SupersededBy    string   `json:"superseded_by,omitempty"`
	// MatchedCue is the handle that matched the current query (not persisted).
	MatchedCue string `json:"matched_cue,omitempty"`
}

// VerificationGain delegates the continuous logarithmic reinforcement multiplier
// (1 + ln(1 + ReuseCount)) to the canonical kernel implementation in internal/core.
func (r LocalNoeticRecord) VerificationGain() float64 {
	return core.VerificationGain(r.ReuseCount)
}

// LocalResolvedHit pairs a transport's numerical ranking with the locally resolved plaintext.
type LocalResolvedHit struct {
	QueryHit
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
	rawText       string
	termMeta      map[uint64]core.Term
	termCounts    map[uint64]int
	termPositions map[uint64][]core.TermPosition
	docLen        int
	// allHashes keeps every packed atom hash (unfiltered) so post-recall coverage
	// checks never re-run the segmenter on each query.
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
	docHash       uint64
	blockHash     uint64
	lineStart     int
	lineEnd       int
	termMeta      map[uint64]core.Term
	termCounts    map[uint64]int
	termPositions map[uint64][]core.TermPosition
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
	changeSeq        int64
	purgeEpoch       int64
	// freshnessSignature is the canonical-store identity (v5 line) for snapshots
	// built from rm_*; empty for legacy thin-table snapshots.
	freshnessSignature string
	docsRev            int64
	bgRev              int64
	epiCnt             int64
	bgCnt              int64
	bgMaxRowID         int64
	bgOccSum           int64
	bgSlotsLen         int64
	inlineCount        int
	docs               []vaultDocEntry
	episodicDocs       []vaultDocEntry
	validEpisodicIDs   map[string]struct{}
	docIdxByID         map[string]int
	coreOnce           sync.Once
	coreLoadMu         sync.Mutex
	coreMu             sync.Mutex
	coreEngine         *core.Engine
	// noeticEngine is the base engine with the active recollections' citation
	// records folded into its co-citation CSR. It is derived, immutable and
	// cached by the noetic fingerprint; the base engine stays untouched.
	noeticEngine   *core.Engine
	noeticEdgesKey string
	// coreWorkerPool holds per-caller engine copies. Their query scratch carries
	// the day-cosine memo across queries handled by the same worker; the core
	// bounds that memo's size and the index pointer check drops copies from a
	// previous snapshot generation.
	coreWorkerPool sync.Pool
	// coreWarm marks that one search has materialised the lazily built cluster
	// state. Until then every search runs under coreMu; afterwards the state is
	// immutable for this snapshot and each caller works on a shallow engine copy
	// with its own query scratch.
	coreWarm bool
	// ownsCoreEngine is true for a published generation that opened its own mmap.
	// Derived and delta snapshots share a base engine and never close it.
	ownsCoreEngine bool
	// baseEngine / baseRelease tie the shared .zcsr baseline to this snapshot.
	// The release runs when the snapshot is retired and its last reader returns,
	// so a replaced baseline cannot unmap under a live projection.
	baseEngine  *core.Engine
	baseRelease func()
	lifecycleMu sync.Mutex
	readers     int
	retired     bool
	closeOnce   sync.Once
	// stale marks a snapshot served from the last published cluster while the
	// delta exceeded the in-memory cap; the background builder will republish.
	stale bool
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

// Vault manages the local SQLite plaintext tables, the cursor-free dual-cluster
// retrieval kernel (internal/core) and the user's local secret HMAC key.
type Vault struct {
	db        *sql.DB
	secretKey [32]byte
	// identity is the local persona coordinate stamped onto every saved row: the answering
	// assistant, its concrete lifetime incarnation, and the workspace locus. Set once by the
	// caller (`SetIdentity`); empty values are stored as '' and never block a write.
	identity VaultIdentity
	// segOpts holds the optional custom word segmenter and adaptive harmonic configuration.
	segOpts          SegmenterPackOptions
	harmOpts         plain.CanonicalHarmonicOptions
	harmOptsExplicit bool

	slotCacheMu *sync.RWMutex
	slotCache   map[string]vaultCachedDocSlots
	// snapRef is shared by the Vault and every clone of it: requests get a per-call clone
	// (persona identity must stay independent), so the corpus snapshot has to live behind a
	// shared handle or each request would re-pack the whole vault.
	snapRef *vaultSnapshotRef
	// dbPath/dbPathOnce 记忆化主库文件路径（`PRAGMA database_list`），供 .zcsr 磁盘簇缓存
	// 定位；内存库或查询失败时为空串（此时不做磁盘簇缓存，行为与历史一致）。
	dbPath     string
	dbPathOnce sync.Once
	// coldBuildMemoryLimit 由克隆共享：冷启动全量构建期间临时生效的软内存上限（0 表示
	// 不改动运行时上限）。构建的活集可数倍于守护进程的稳态上限，在上限内构建会退化为
	// GC 空转而非完成，故由调用方按可用物理内存给出构建窗口。
	coldBuildMemoryLimit *int64
}

// vaultSnapshotRef holds the corpus snapshot behind a pointer shared across clones.
// Reads and writes happen under the owning Vault's slotCacheMu, which clones share too.
type vaultSnapshotRef struct {
	buildMu         sync.Mutex
	refreshing      bool
	faultRefreshing bool
	snap            *vaultCorpusSnapshot
	// publishedSnap / publishedSlot cache the snapshot-only read generation.
	// They are keyed by the CURRENT slot name and are never invalidated by
	// in-process writes: a write becomes searchable when the builder publishes
	// the next slot, not before.
	publishedSnap *vaultCorpusSnapshot
	publishedSlot string
	// runtimeClosed prevents a request that was already entering the load path
	// from installing another published mmap after the owning Memory has closed.
	runtimeClosed bool
	// allowBuild is shared by every clone. A long-lived recall surface clears it
	// so a missing or stale disk cluster degrades to a warming snapshot instead
	// of running the minute-scale full-corpus build on the query path.
	allowBuild atomic.Bool
	// snapWriteGen counts same-process vault document writes; snapVerifiedGen records
	// the write generation the published snapshot was last fingerprint-verified
	// against. A gap between the two sends the next load through
	// buildCorpusSnapshot's exact fingerprint re-check without discarding the
	// snapshot itself (a nil snap would force the full table rescan instead).
	snapWriteGen    uint64
	snapVerifiedGen uint64
	// lastDocsRev / lastBgRev are the revision counters the current snapshot's
	// fingerprints were computed against. The four-value aggregate fingerprints are
	// blind to length-preserving UPDATEs (COUNT, SUM(rowid), MAX(rowid) and
	// SUM(LENGTH) all unchanged), so when the trigger-backed counters are
	// readable they are the primary identity and the aggregates only confirm.
	lastDocsRev  int64
	lastBgRev    int64
	lastSnapTime time.Time
	// csrEngine caches the mmap .zcsr baseline engine opened for a freshness-checked
	// corpus state (file identity = .zcsr mtime+size); csrFileMTimeNS / csrFileSize
	// anchor that identity so incremental writes never force a re-mmap.
	csrEngine      *core.Engine
	csrFileMTimeNS int64
	csrFileSize    int64
	csrFilePath    string
	// engineRefs counts the owners of each baseline engine: the CSR cache plus
	// every live corpus snapshot projected from it. A replaced engine unmaps only
	// when the last owner releases, so an in-flight recall never loses its mmap.
	// Guarded by the owning Vault's slotCacheMu.
	engineRefs       map[*core.Engine]int
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
	// noeticCueIndex is the cue inverted index for the cached recollections:
	// blind atom hash -> record positions. A query only scores traces that
	// share at least one cue atom, which keeps cue variants affordable.
	noeticCueIndex   map[uint64][]int32
	lastDataVersion  int64
	lastDataVerCheck time.Time
	// canonicalStore handle wrapping the shared connection pool; the core schema
	// is applied once per vault family.
	canonicalOnce  sync.Once
	canonicalStore *core.Store
	canonicalErr   error
}

func (v *Vault) clearSlotCache() {
	var previous *vaultCorpusSnapshot
	if v != nil && v.slotCacheMu != nil {
		v.slotCacheMu.Lock()
		v.slotCache = make(map[string]vaultCachedDocSlots, 256)
		if v.snapRef != nil {
			previous = v.replaceCachedSnapshotLocked(nil)
			v.snapRef.faultReady = false
			v.snapRef.noeticReady = false
		}
		v.slotCacheMu.Unlock()
	}
	if previous != nil {
		previous.retire()
	}
	v.dropCachedCSREngine()
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
	// A chunk coordinate committed by another process must not stay cached here; the local
	// write paths clear the same caches, so both invalidation classes are covered.
	clearBackgroundChunkCaches()
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
	// One trace keeps several cue handles; the cache key therefore includes the
	// cue, not just the row id, so alternating variants do not evict each other.
	cacheKey := "@noetic:" + noeticID + ":" + ids.SHA256Hex(queryText)[:12]
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
// reusing the in-memory corpus snapshot's `termCounts` / `allHashes`, `core.Engine` CSR
// entries, or `slotCache` so post-recall coverage and Noetic citation selection never
// re-run BPE harmonic packing across 30 hydrated long documents on the foreground thread.
func (v *Vault) DocAtomCoverageWithCueHashes(docID, fallbackText string, uniqueCueHashes []uint64) float64 {
	if v == nil || len(uniqueCueHashes) == 0 {
		return 0
	}
	if snap, release, ok := v.acquireCachedCorpusSnapshot(); ok {
		defer release()
		if idx, ok := snap.docIdxByID[docID]; ok && idx >= 0 && idx < len(snap.docs) {
			doc := snap.docs[idx]
			if len(doc.termCounts) > 0 || len(doc.allHashes) > 0 {
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
			if eng := snap.coreEngine; eng != nil {
				cueSet := make(map[uint64]bool, len(uniqueCueHashes))
				for _, h := range uniqueCueHashes {
					cueSet[h] = true
				}
				matched := 0
				if eng.ForEachUnitDocEntry(docID, func(ev core.DocEntryView) {
					if cueSet[ev.ID] {
						delete(cueSet, ev.ID)
						matched++
					}
				}) {
					return float64(matched) / float64(len(uniqueCueHashes))
				}
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

// CloneWithSharedSlotCache forks a request-local Vault: plaintext storage,
// key material and the synchronized blind-slot cache are shared, while the
// persona identity starts empty and remains independent on every clone.
func (v *Vault) CloneWithSharedSlotCache() *Vault {
	if v == nil {
		return nil
	}
	return &Vault{
		db: v.db, secretKey: v.secretKey,
		segOpts: v.segOpts, harmOpts: v.harmOpts, harmOptsExplicit: v.harmOptsExplicit,
		slotCacheMu: v.slotCacheMu, slotCache: v.slotCache, snapRef: v.snapRef,
		coldBuildMemoryLimit: v.coldBuildMemoryLimit,
	}
}

// SharesStore reports whether two vault handles refer to the same underlying
// SQLite database and shared snapshot slots. Request-local clone handles are
// re-created per call, so callers that cache derived builders must key by store
// identity rather than the handle pointer.
func (v *Vault) SharesStore(other *Vault) bool {
	if v == nil || other == nil {
		return false
	}
	return v.db == other.db && v.snapRef == other.snapRef
}

// SetColdBuildMemoryLimit configures the soft memory limit applied while a cold
// full-corpus engine build runs. Such a build materialises the whole corpus
// index before the .zcsr disk cluster exists, so its live set can exceed the
// daemon's steady-state cap several times over and GC inside that cap never
// finishes. Zero leaves the runtime limit untouched.
func (v *Vault) SetColdBuildMemoryLimit(bytes int64) {
	if v == nil || v.coldBuildMemoryLimit == nil {
		return
	}
	if bytes < 0 {
		bytes = 0
	}
	atomic.StoreInt64(v.coldBuildMemoryLimit, bytes)
}

// newVaultSnapshotRef creates the shared snapshot handle with the historical
// default (builds allowed). Recall surfaces clear the flag before the first use.
func newVaultSnapshotRef() *vaultSnapshotRef {
	ref := &vaultSnapshotRef{engineRefs: make(map[*core.Engine]int, 2)}
	ref.allowBuild.Store(true)
	return ref
}

// SetBuildAllowed controls whether this vault (and its clones) may run the
// full-corpus snapshot build on demand. It must be set before the first recall
// on a long-lived reader surface; short-lived CLI and evaluation paths keep the
// default.
func (v *Vault) SetBuildAllowed(allow bool) {
	if v == nil || v.snapRef == nil {
		return
	}
	v.snapRef.allowBuild.Store(allow)
}

// BuildAllowed reports whether the on-demand full-corpus build is permitted.
func (v *Vault) BuildAllowed() bool {
	if v == nil || v.snapRef == nil {
		return false
	}
	return v.snapRef.allowBuild.Load()
}

// cachedCorpusSnapshot returns the published snapshot without rebuilding it.
func (v *Vault) cachedCorpusSnapshot() *vaultCorpusSnapshot {
	if v == nil || v.slotCacheMu == nil || v.snapRef == nil {
		return nil
	}
	v.slotCacheMu.RLock()
	defer v.slotCacheMu.RUnlock()
	return v.snapRef.snap
}

// coldBuildLimit reads the configured cold-build window (0 = disabled).
func (v *Vault) coldBuildLimit() int64 {
	if v == nil || v.coldBuildMemoryLimit == nil {
		return 0
	}
	return atomic.LoadInt64(v.coldBuildMemoryLimit)
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

// RecordDeletedUnits records user-deleted unit IDs as tombstones so background
// directory sync and harness dialogue reconciliation do not re-import them.
func (v *Vault) RecordDeletedUnits(layer string, unitIDs ...string) {
	if v == nil || v.db == nil || len(unitIDs) == 0 {
		return
	}
	_, _ = v.db.Exec(`CREATE TABLE IF NOT EXISTS rm_deleted_units (unit_id TEXT PRIMARY KEY, layer TEXT NOT NULL DEFAULT '', deleted_at INTEGER NOT NULL DEFAULT 0)`)
	now := time.Now().Unix()
	lyr := strings.ToLower(strings.TrimSpace(layer))
	for _, id := range unitIDs {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			_, _ = v.db.Exec(`INSERT OR REPLACE INTO rm_deleted_units(unit_id, layer, deleted_at) VALUES (?, ?, ?)`, trimmed, lyr, now)
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
			_, _ = v.db.Exec(`DELETE FROM rm_deleted_units WHERE unit_id = ?`, trimmed)
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
		rows, err = v.db.Query(`SELECT unit_id FROM rm_deleted_units`)
	} else {
		rows, err = v.db.Query(`SELECT unit_id FROM rm_deleted_units WHERE layer = ? OR layer = ''`, lyr)
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

// ensureCanonicalUnitView creates the rm_units read model over the canonical
// corpus. It is a projection, not a second copy; there is no legacy schema
// migration and no compatibility branch.
func ensureCanonicalUnitView(db *sql.DB) error {
	if db == nil {
		return nil
	}
	_, err := db.Exec(`
		CREATE VIEW IF NOT EXISTS rm_units AS
		SELECT d.unit_id AS doc_id,
		       d.layer AS layer,
		       d.occurred_at AS occurred_at,
		       COALESCE(NULLIF(m.inserted_at, 0), d.ingested_at, d.occurred_at) AS inserted_at,
		       COALESCE(NULLIF(m.ref_pointer, ''), d.raw_text) AS raw_text,
		       COALESCE(m.sentences_json, '[]') AS sentences_json,
		       COALESCE(m.address_uri, '') AS address_uri,
		       COALESCE(m.coord64, 0) AS coord64,
		       COALESCE(m.waveform_bar, '') AS waveform_bar,
		       COALESCE(m.doc_macro_wave, '') AS doc_macro_wave,
		       COALESCE(m.kept_indices_json, '[]') AS kept_indices_json,
		       COALESCE(m.assistant_id, '') AS assistant_id,
		       COALESCE(m.incarnation_id, '') AS incarnation_id,
		       COALESCE(m.locus, '') AS locus
		  FROM rm_docs d
		  LEFT JOIN rm_unit_meta m ON m.unit_id = d.unit_id;
		CREATE TRIGGER IF NOT EXISTS rm_units_insert INSTEAD OF INSERT ON rm_units
		BEGIN
		  INSERT INTO rm_docs(unit_id, layer, occurred_at, raw_text)
		  VALUES (NEW.doc_id, COALESCE(NULLIF(NEW.layer, ''), 'episodic'), COALESCE(NEW.occurred_at, 0),
		          CASE WHEN substr(trim(COALESCE(NEW.raw_text, '')), 1, 5) = '@ref:' THEN '' ELSE COALESCE(NEW.raw_text, '') END)
		  ON CONFLICT(unit_id) DO UPDATE SET layer = excluded.layer, occurred_at = CASE WHEN excluded.occurred_at > 0 THEN excluded.occurred_at ELSE rm_docs.occurred_at END, raw_text = excluded.raw_text;
		  INSERT INTO rm_unit_meta(unit_id, layer, ref_pointer, sentences_json, kept_indices_json, address_uri, coord64, waveform_bar, doc_macro_wave, assistant_id, incarnation_id, locus, inserted_at, meta_rev)
		  VALUES (NEW.doc_id, COALESCE(NULLIF(NEW.layer, ''), 'episodic'),
		          CASE WHEN substr(trim(COALESCE(NEW.raw_text, '')), 1, 5) = '@ref:' THEN NEW.raw_text ELSE '' END,
		          COALESCE(NEW.sentences_json, '[]'), COALESCE(NEW.kept_indices_json, '[]'),
		          COALESCE(NEW.address_uri, ''), COALESCE(NEW.coord64, 0), COALESCE(NEW.waveform_bar, ''), COALESCE(NEW.doc_macro_wave, ''),
		          COALESCE(NEW.assistant_id, ''), COALESCE(NEW.incarnation_id, ''), COALESCE(NEW.locus, ''),
		          COALESCE(NULLIF(NEW.inserted_at, 0), NEW.occurred_at, 0), 1)
		  ON CONFLICT(unit_id) DO UPDATE SET layer = excluded.layer, ref_pointer = excluded.ref_pointer,
		          sentences_json = excluded.sentences_json, kept_indices_json = excluded.kept_indices_json,
		          address_uri = excluded.address_uri, coord64 = excluded.coord64, waveform_bar = excluded.waveform_bar,
		          doc_macro_wave = excluded.doc_macro_wave,
		          assistant_id = CASE WHEN rm_unit_meta.assistant_id <> '' THEN rm_unit_meta.assistant_id ELSE excluded.assistant_id END,
		          incarnation_id = CASE WHEN rm_unit_meta.incarnation_id <> '' THEN rm_unit_meta.incarnation_id ELSE excluded.incarnation_id END,
		          locus = CASE WHEN rm_unit_meta.locus <> '' THEN rm_unit_meta.locus ELSE excluded.locus END,
		          inserted_at = CASE WHEN rm_unit_meta.inserted_at > 0 THEN rm_unit_meta.inserted_at ELSE excluded.inserted_at END,
		          meta_rev = rm_unit_meta.meta_rev + 1;
		END;
		CREATE TRIGGER IF NOT EXISTS rm_units_update INSTEAD OF UPDATE ON rm_units
		BEGIN
		  UPDATE rm_docs SET layer = COALESCE(NULLIF(NEW.layer, ''), layer), occurred_at = COALESCE(NEW.occurred_at, occurred_at),
		          raw_text = CASE WHEN substr(trim(COALESCE(NEW.raw_text, '')), 1, 5) = '@ref:' THEN '' ELSE COALESCE(NEW.raw_text, raw_text) END
		    WHERE unit_id = OLD.doc_id;
		  UPDATE rm_unit_meta SET layer = COALESCE(NULLIF(NEW.layer, ''), layer),
		          ref_pointer = CASE WHEN substr(trim(COALESCE(NEW.raw_text, '')), 1, 5) = '@ref:' THEN NEW.raw_text ELSE '' END,
		          sentences_json = COALESCE(NEW.sentences_json, sentences_json),
		          kept_indices_json = COALESCE(NEW.kept_indices_json, kept_indices_json),
		          address_uri = COALESCE(NEW.address_uri, address_uri), coord64 = COALESCE(NEW.coord64, coord64),
		          waveform_bar = COALESCE(NEW.waveform_bar, waveform_bar), doc_macro_wave = COALESCE(NEW.doc_macro_wave, doc_macro_wave),
		          assistant_id = COALESCE(NEW.assistant_id, assistant_id), incarnation_id = COALESCE(NEW.incarnation_id, incarnation_id),
		          locus = COALESCE(NEW.locus, locus), inserted_at = COALESCE(NULLIF(NEW.inserted_at, 0), inserted_at),
		          meta_rev = meta_rev + 1
		    WHERE unit_id = OLD.doc_id;
		END;
		CREATE TRIGGER IF NOT EXISTS rm_units_delete INSTEAD OF DELETE ON rm_units
		BEGIN
		  DELETE FROM rm_docs WHERE unit_id = OLD.doc_id;
		END;`)
	return err
}

// NewVault initializes the local SQLite tables (`rm_meta` and `rm_units`)
// and loads or generates the user's 256-bit local secret key (which never leaves the machine).
func NewVault(db *sql.DB, explicitSecret []byte) (*Vault, error) {
	if db == nil {
		return nil, errors.New("localvault: nil sql.DB")
	}
	if len(explicitSecret) == 0 {
		var stored []byte
		var storageType string
		if err := db.QueryRow(`SELECT v, typeof(v) FROM rm_meta WHERE k = 'user_secret_key'`).Scan(&stored, &storageType); err == nil && storageType == "blob" && len(stored) == 32 {
			var readyCnt int
			_ = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name IN ('rm_docs', 'rm_noetic')`).Scan(&readyCnt)
			if readyCnt == 2 {
				_ = EnsureCoreSQLiteSchema(db)
				if err := ensureCanonicalBusinessSchema(db); err != nil {
					return nil, err
				}
				if err := initEpisodicIndex(db); err != nil {
					return nil, err
				}
				if err := ensureCanonicalUnitView(db); err != nil {
					return nil, err
				}
				ensureInsertedAtColumns(db)
				ensureNoeticTraceColumns(db)
				CoreSchemaContracts()
				v := &Vault{
					db:                   db,
					slotCacheMu:          &sync.RWMutex{},
					slotCache:            make(map[string]vaultCachedDocSlots, 256),
					snapRef:              newVaultSnapshotRef(),
					coldBuildMemoryLimit: new(int64),
				}
				copy(v.secretKey[:], stored)
				return v, nil
			}
		}
	}
	ddl := `
	CREATE TABLE IF NOT EXISTS rm_meta (
		k TEXT PRIMARY KEY,
		v BLOB NOT NULL
	);
					CREATE TABLE IF NOT EXISTS rm_noetic (
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
						incarnation_id TEXT NOT NULL DEFAULT '',
						activated_at INTEGER NOT NULL DEFAULT 0,
						activation_count INTEGER NOT NULL DEFAULT 0,
						trace_key TEXT NOT NULL DEFAULT '',
						query_variants_json TEXT NOT NULL DEFAULT '[]',
						superseded_by TEXT NOT NULL DEFAULT ''
					);
				CREATE INDEX IF NOT EXISTS idx_rm_noetic_status_ts ON rm_noetic(status, occurred_at DESC);`
	if _, err := db.Exec(ddl); err != nil {
		return nil, err
	}
	if err := EnsureCoreSQLiteSchema(db); err != nil {
		return nil, err
	}
	if err := ensureCanonicalBusinessSchema(db); err != nil {
		return nil, err
	}
	if err := initEpisodicIndex(db); err != nil {
		return nil, err
	}
	if err := ensureCanonicalUnitView(db); err != nil {
		return nil, err
	}
	CoreSchemaContracts()
	for _, stmt := range []string{
		`ALTER TABLE rm_noetic ADD COLUMN assistant_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE rm_noetic ADD COLUMN incarnation_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE rm_noetic ADD COLUMN address_uri TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE rm_noetic ADD COLUMN coord64 INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE rm_noetic ADD COLUMN waveform_bar TEXT NOT NULL DEFAULT ''`,
	} {
		_, _ = db.Exec(stmt)
	}
	ensureInsertedAtColumns(db)
	ensureNoeticTraceColumns(db)

	v := &Vault{
		db:                   db,
		slotCacheMu:          &sync.RWMutex{},
		slotCache:            make(map[string]vaultCachedDocSlots, 256),
		snapRef:              newVaultSnapshotRef(),
		coldBuildMemoryLimit: new(int64),
	}
	if len(explicitSecret) > 0 {
		h := sha256.Sum256(explicitSecret)
		v.secretKey = h
		return v, nil
	}

	var stored []byte
	var storageType string
	err := db.QueryRow(`SELECT v, typeof(v) FROM rm_meta WHERE k = 'user_secret_key'`).Scan(&stored, &storageType)
	if errors.Is(err, sql.ErrNoRows) {
		var fresh [32]byte
		if _, err := rand.Read(fresh[:]); err != nil {
			return nil, err
		}
		if _, err := db.Exec(`INSERT OR IGNORE INTO rm_meta(k, v) VALUES ('user_secret_key', ?)`, fresh[:]); err != nil {
			return nil, err
		}
		err = db.QueryRow(`SELECT v, typeof(v) FROM rm_meta WHERE k = 'user_secret_key'`).Scan(&stored, &storageType)
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
// `rm_units`, `rm_noetic`, and `rm_bg_chunks`, and ensures
// secondary time/layer indexes exist with a read-only O(1) `sqlite_master` probe so
// steady-state opens take zero write locks.
func ensureInsertedAtColumns(db *sql.DB) {
	if db == nil {
		return
	}
	var metaHas, noeticHas, bgTable, bgHas int
	_ = db.QueryRow(`
		SELECT
			COALESCE(SUM(CASE WHEN type = 'table' AND name = 'rm_unit_meta' AND sql LIKE '%inserted_at%' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN type = 'table' AND name = 'rm_noetic' AND sql LIKE '%inserted_at%' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN type = 'table' AND name = 'rm_bg_chunks' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN type = 'table' AND name = 'rm_bg_chunks' AND sql LIKE '%inserted_at%' THEN 1 ELSE 0 END), 0)
		FROM sqlite_master
		WHERE name IN ('rm_unit_meta', 'rm_noetic', 'rm_bg_chunks')
	`).Scan(&metaHas, &noeticHas, &bgTable, &bgHas)
	if metaHas == 0 {
		_, _ = db.Exec(`ALTER TABLE rm_unit_meta ADD COLUMN inserted_at INTEGER NOT NULL DEFAULT 0`)
		_, _ = db.Exec(`UPDATE rm_unit_meta SET inserted_at = COALESCE((SELECT occurred_at FROM rm_docs WHERE rm_docs.unit_id = rm_unit_meta.unit_id), 0) WHERE inserted_at = 0`)
	}
	if noeticHas == 0 {
		_, _ = db.Exec(`ALTER TABLE rm_noetic ADD COLUMN inserted_at INTEGER NOT NULL DEFAULT 0`)
		_, _ = db.Exec(`UPDATE rm_noetic SET inserted_at = occurred_at WHERE inserted_at = 0 AND occurred_at > 0`)
	}
	if bgTable == 1 && bgHas == 0 {
		_, _ = db.Exec(`ALTER TABLE rm_bg_chunks ADD COLUMN inserted_at INTEGER NOT NULL DEFAULT 0`)
		_, _ = db.Exec(`
			UPDATE rm_bg_chunks
			SET inserted_at = COALESCE(
				(SELECT MIN(c2.occurred_at) FROM rm_bg_chunks c2 WHERE c2.abs_path = rm_bg_chunks.abs_path AND c2.occurred_at > 0),
				occurred_at
			)
			WHERE inserted_at = 0 AND occurred_at > 0
		`)
	}
	_, _ = db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_rm_docs_occurred_at ON rm_docs(occurred_at DESC, unit_id ASC);
		CREATE INDEX IF NOT EXISTS idx_rm_docs_layer_occurred ON rm_docs(layer, occurred_at DESC, unit_id ASC);
		CREATE INDEX IF NOT EXISTS idx_rm_unit_meta_inserted_at ON rm_unit_meta(inserted_at DESC);
		CREATE INDEX IF NOT EXISTS idx_rm_noetic_inserted_at ON rm_noetic(inserted_at DESC);
	`)
	if bgTable == 1 {
		_, _ = db.Exec(`
			CREATE INDEX IF NOT EXISTS idx_rm_bg_chunks_occurred_at ON rm_bg_chunks(occurred_at DESC, unit_id ASC);
			CREATE INDEX IF NOT EXISTS idx_rm_bg_chunks_inserted_at ON rm_bg_chunks(inserted_at DESC);
			CREATE INDEX IF NOT EXISTS idx_rm_bg_chunks_meta ON rm_bg_chunks(occurred_at DESC, unit_id ASC, rel_path, anchor, abs_path, address_uri);
		`)
	}
}

func (v *Vault) KeyedHash64(token string) uint64 {
	return plain.NewBlindEncoderFromRawKey(v.secretKey).KeyedHash64(token)
}

// SplitSentences delegates sentence boundary segmentation to plain.SplitSentences.
func SplitSentences(text string) []string {
	return plain.SplitSentences(text)
}

// SplitWords delegates language-agnostic surface word and CJK dual-track segmentation to plain.SplitWords.
func SplitWords(sent string) []string {
	return plain.SplitWords(sent)
}

// PackBlindPacket splits text into sentences and packs a zero-plaintext BlindPulsePacket via sdk/go/pulse/plain.
func (v *Vault) PackBlindPacket(docID string, text string, occurredAt int64) (BlindPulsePacket, []string) {
	return v.packBlindPacketWithKey(v.secretKey, docID, text, occurredAt)
}

// packBlindPacketWithKey 以给定键控密钥打包，保留 Vault 的 segOpts/harmOpts（统一构造路径）。
func (v *Vault) packBlindPacketWithKey(key [32]byte, docID, text string, occurredAt int64) (BlindPulsePacket, []string) {
	enc := plain.NewBlindEncoderFromRawKey(key)
	if v != nil {
		enc.SetCanonicalHarmonics(v.harmOpts)
	}
	if v != nil && (v.segOpts.Segmenter != nil || v.segOpts.SentenceAtomPacker != nil) {
		rmPkt, sents := enc.PackBlindPacketWithSegmenter(docID, text, occurredAt, v.segOpts)
		return BlindPulsePacket{
			UnitID:         rmPkt.UnitID,
			OccurredAt:     rmPkt.OccurredAt,
			Slots:          rmPkt.Slots,
			SentenceFrames: rmPkt.SentenceFrames,
			AtomsB64:       rmPkt.AtomsB64,
			RawText:        text,
			PulseMode:      "local_plain",
		}, sents
	}
	sdkPkt, sents := enc.PackBlindPacket(docID, text, occurredAt)
	return BlindPulsePacket{
		UnitID:         sdkPkt.UnitID,
		OccurredAt:     sdkPkt.OccurredAt,
		Slots:          sdkPkt.Slots,
		SentenceFrames: sdkPkt.SentenceFrames,
		AtomsB64:       sdkPkt.AtomsB64,
		RawText:        text,
		PulseMode:      "local_plain",
	}, sents
}

// canonicalMetaForResponse derives the persisted metadata for one plaintext
// save from the transport receipt.
func canonicalMetaForResponse(sents []string, resp IngestResponse, identity VaultIdentity, insertedAt int64) (canonicalUnitMeta, error) {
	sentsJSON, err := json.Marshal(sents)
	if err != nil {
		return canonicalUnitMeta{}, err
	}
	keptJSON, err := json.Marshal(resp.KeptSentenceIndices)
	if err != nil {
		return canonicalUnitMeta{}, err
	}
	layer := strings.ToLower(strings.TrimSpace(resp.Layer))
	if layer != "background" && layer != "noetic" {
		layer = "episodic"
	}
	return canonicalUnitMeta{
		Layer:           layer,
		SentencesJSON:   string(sentsJSON),
		KeptIndicesJSON: string(keptJSON),
		AddressURI:      resp.AddressURI,
		Coord64:         int64(resp.AddressCoord64),
		WaveformBar:     resp.WaveformBar,
		DocMacroWave:    resp.DocMacroWaveform,
		AssistantID:     identity.AssistantID,
		IncarnationID:   identity.IncarnationID,
		Locus:           identity.Locus,
		InsertedAt:      insertedAt,
		StoreRawText:    true,
	}, nil
}

// SaveLocalPlaintext persists the document's plaintext and assigned metadata in local SQLite.
func (v *Vault) SaveLocalPlaintext(docID string, occurredAt int64, rawText string, sents []string, resp IngestResponse) error {
	meta, err := canonicalMetaForResponse(sents, resp, v.identity, time.Now().Unix())
	if err != nil {
		return err
	}
	if meta.Layer == LayerEpisodic {
		var deleted int
		if err := v.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM rm_deleted_units WHERE unit_id = ? AND (layer = 'episodic' OR layer = ''))`, strings.TrimSpace(docID)).Scan(&deleted); err != nil {
			return fmt.Errorf("localvault: check episodic tombstone: %w", err)
		}
		if deleted != 0 {
			return fmt.Errorf("%w: %s", ErrEpisodicUnitDeleted, docID)
		}
	}
	if err := v.ingestCanonicalText(docID, occurredAt, rawText, meta); err != nil {
		return err
	}
	if v.slotCacheMu != nil {
		v.slotCacheMu.Lock()
		delete(v.slotCache, docID)
		if v.snapRef != nil {
			v.snapRef.snapWriteGen++
		}
		v.slotCacheMu.Unlock()
	}
	return nil
}

// SaveLocalEpisodicPointer stores ONLY a zero-copy pointer (`@ref:zcode:...`,
// `@ref:opencode:...` or `@ref:jsonl:...`)
// and the assigned memory address / carrier metadata in `rm_units`, without duplicating the
// Harness's conversation plaintext into SQLite. `turnRef` records the provenance of the unit (a
// content-addressed unit can be said in several turns; the row keeps one pointer, the lineage
// table keeps every occurrence).
func (v *Vault) SaveLocalEpisodicPointer(docID string, occurredAt int64, refPointer, turnRef string, resp IngestResponse) error {
	keptJSON, err := json.Marshal(resp.KeptSentenceIndices)
	if err != nil {
		return err
	}
	refPointer = strings.TrimSpace(refPointer)
	var deleted int
	if err := v.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM rm_deleted_units WHERE unit_id = ? AND (layer = 'episodic' OR layer = ''))`, strings.TrimSpace(docID)).Scan(&deleted); err != nil {
		return fmt.Errorf("localvault: check episodic tombstone: %w", err)
	}
	if deleted != 0 {
		return fmt.Errorf("%w: %s", ErrEpisodicUnitDeleted, docID)
	}
	if err := v.ingestCanonicalText(docID, occurredAt, "", canonicalUnitMeta{
		Layer:           "episodic",
		RefPointer:      refPointer,
		KeptIndicesJSON: string(keptJSON),
		AddressURI:      resp.AddressURI,
		Coord64:         int64(resp.AddressCoord64),
		WaveformBar:     resp.WaveformBar,
		DocMacroWave:    resp.DocMacroWaveform,
		AssistantID:     v.identity.AssistantID,
		IncarnationID:   v.identity.IncarnationID,
		Locus:           v.identity.Locus,
		InsertedAt:      time.Now().Unix(),
		StoreRawText:    false,
	}); err != nil {
		return err
	}
	if turnRef = strings.TrimSpace(turnRef); turnRef != "" {
		if _, err := v.db.Exec(`INSERT OR IGNORE INTO rm_episodic_lineage(turn_ref, doc_id, occurred_at) VALUES (?, ?, ?)`, turnRef, docID, occurredAt); err != nil {
			return err
		}
	}
	if v.slotCacheMu != nil {
		v.slotCacheMu.Lock()
		delete(v.slotCache, docID)
		if v.snapRef != nil {
			v.snapRef.snapWriteGen++
		}
		v.slotCacheMu.Unlock()
	}
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
	tx, err := v.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkEpisodicTombstone(tx, docID); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO rm_episodic_lineage(turn_ref, doc_id, occurred_at) SELECT ?, doc_id, ? FROM rm_units WHERE doc_id = ?`, turnRef, occurredAt, docID); err != nil {
		return err
	}
	return tx.Commit()
}

// LoadLocalDocumentLayer retrieves the stored cognitive layer ("background" | "episodic" | "noetic") for docID.
func (v *Vault) LoadLocalDocumentLayer(docID string) string {
	if strings.HasPrefix(docID, "bg:") {
		return LayerBackground
	}
	var layer string
	if err := v.db.QueryRow(`SELECT layer FROM rm_docs WHERE unit_id = ?`, docID).Scan(&layer); err == nil && strings.TrimSpace(layer) != "" {
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

// DocLoci returns the workspace locus (`rm_units.locus`, stamped at ingest from the
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
		rows, err := v.db.Query(`SELECT d.unit_id, COALESCE(m.locus, '') FROM rm_docs d LEFT JOIN rm_unit_meta m ON m.unit_id = d.unit_id WHERE d.unit_id IN (`+inPlaceholders(len(chunk))+`)`, args...)
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
// carries a positive timestamp in `rm_units`, `rm_bg_chunks`, or `rm_noetic`.
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
		if rows, err := v.db.Query(`SELECT unit_id, occurred_at FROM rm_docs WHERE unit_id IN (`+ph+`)`, args...); err == nil {
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
			if rows, err := v.db.Query(`SELECT unit_id, occurred_at FROM rm_bg_chunks WHERE unit_id IN (`+inPlaceholders(len(bgArgs))+`)`, bgArgs...); err == nil {
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
			if rows, err := v.db.Query(`SELECT noetic_id, occurred_at FROM rm_noetic WHERE noetic_id IN (`+ph+`)`, args...); err == nil {
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

// LoadEpisodicSource reads one indexed conversation unit, never a synthesized
// record or an entire turn. A moved source may supply only the same verified text.
func (v *Vault) LoadEpisodicSource(docID string) (string, error) {
	var raw string
	err := v.db.QueryRow(`
		SELECT COALESCE(NULLIF(m.ref_pointer, ''), d.raw_text)
		FROM rm_docs d LEFT JOIN rm_unit_meta m ON m.unit_id = d.unit_id
		WHERE d.unit_id = ? AND d.layer = ?`, docID, LayerEpisodic).Scan(&raw)
	if err != nil {
		return "", err
	}
	if pointer := strings.TrimSpace(raw); strings.HasPrefix(pointer, "@ref:") {
		text, actual, err := v.LoadVerifiedEpisodicPointerSource(docID, pointer)
		if err != nil {
			return "", err
		}
		if stem := pointerStem(pointer); stem == "" || pointerStem(actual) != stem {
			return "", errors.New("localvault: source provenance unavailable")
		}
		return text, nil
	}
	return raw, nil
}

// LoadLocalDocument retrieves the locally stored plaintext and distilled dry sentences for docID
// (supporting `background` mounted directory files read LIVE on-demand from disk via `rm_bg_chunks`,
// zero-copy `episodic` pointers `@ref:...` read LIVE on-demand from the Harness DB/JSONL,
// `episodic`/`background` in `rm_units`, and local `noetic` records in `rm_noetic`).
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
			return "", "", nil, errors.New("localvault: cached unavailable local document")
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
	err = v.db.QueryRow(`
		SELECT COALESCE(NULLIF(m.ref_pointer, ''), d.raw_text),
		       COALESCE(m.sentences_json, '[]'),
		       COALESCE(m.kept_indices_json, '[]')
		  FROM rm_docs d
		  LEFT JOIN rm_unit_meta m ON m.unit_id = d.unit_id
		 WHERE d.unit_id = ?`, docID).
		Scan(&rawText, &sentsJSON, &keptJSON)
	if err != nil {
		if raw, dist, kept, bgErr := v.loadBackgroundChunkLive(docID); bgErr == nil {
			return raw, dist, kept, nil
		}
		var qTxt, aTxt, citedSentsJSON string
		nErr := v.db.QueryRow(`SELECT query_text, answer_text, cited_sentences_json FROM rm_noetic WHERE noetic_id = ? AND status = 'active'`, docID).
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
		if err := v.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM rm_episodic_index WHERE doc_id = ?) OR EXISTS(SELECT 1 FROM rm_episodic_lineage WHERE doc_id = ? AND turn_ref = ?)`, docID, docID, rawText).Scan(&indexed); err != nil {
			return "", "", nil, err
		}
		if indexed != 0 {
			return "", "", nil, fmt.Errorf("localvault: ambiguous episodic document identity for %s", docID)
		}
	}
	if strings.HasPrefix(strings.TrimSpace(rawText), "@ref:") && !inlineEvidence {
		liveText, rErr := v.LoadVerifiedEpisodicPointer(docID, strings.TrimSpace(rawText))
		if rErr != nil {
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
	if err := v.db.QueryRow(`SELECT raw_text FROM rm_units WHERE doc_id = ?`, docID).Scan(&raw); err != nil {
		return ""
	}
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "@ref:") {
		return ""
	}
	return raw
}

// LoadEpisodicTurnSegmentsContext resolves a turn-level pointer while honoring
// cancellation during JSONL source hashing and streaming.
func LoadEpisodicTurnSegmentsContext(ctx context.Context, turnRef string) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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
		segments, _, err := episodic.ZCodeTurnSegmentsUpTo(zdb, parts[0], parts[1], int(^uint(0)>>1))
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
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
		segments, err := episodic.JSONLTurnSegmentsContext(ctx, rest[:lastColon], turnIndex)
		if err != nil {
			return nil, err
		}
		return renderEpisodicSegments(segments), nil
	}
	if strings.HasPrefix(trimmed, "@ref:opencode:") {
		rest := strings.TrimPrefix(trimmed, "@ref:opencode:")
		parts := strings.SplitN(rest, ":", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return nil, fmt.Errorf("invalid opencode turn reference %q", turnRef)
		}
		odb, err := openOpenCodeReadOnly()
		if err != nil {
			return nil, err
		}
		segments, _, err := episodic.OpenCodeTurnSegmentsUpTo(odb, parts[0], parts[1], int(^uint(0)>>1))
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
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
//   - `@ref:zcode:<session>:<msg_id>#<k>` / `@ref:opencode:<session>:<msg_id>#<k>`
//     / `@ref:jsonl:<abs_path>:<turn_idx>#<k>` names one speech segment (the L2
//     unit is a segment, not a whole turn);
//   - `@ref:...#<k>.<c>` names chunk c of one segment: a long segment is split at
//     sentence boundaries under the same tokenz-r budget the ingest used, so the
//     split is deterministic and resolves to the chunk it was built from;
//   - `@ref:zcode:<session>:<msg_id>` / `@ref:opencode:<session>:<msg_id>` /
//     `@ref:jsonl:<abs_path>:<turn_idx>` names the whole turn (legacy turn-level
//     rows written before segment units).
//
// Segment enumeration is delegated to `internal/episodic`, the same package the
// ingest scanner uses, so `#<k>` always resolves to the segment it was built from.
func ResolveEpisodicPointerLive(refPointer string) (string, error) {
	return ResolveEpisodicPointerLiveContext(context.Background(), refPointer)
}

// ResolveEpisodicPointerLiveContext resolves an episodic pointer and stops
// transcript reads when the calling background operation is cancelled.
func ResolveEpisodicPointerLiveContext(ctx context.Context, refPointer string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
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
			return resolveJSONLSegmentLiveContext(ctx, absPath, turnStr, segmentIndex, chunkIndex, hasChunk)
		}
		return resolveJSONLTurnLiveContext(ctx, absPath, turnStr)
	}
	if strings.HasPrefix(trimmed, "@ref:opencode:") {
		rest := strings.TrimPrefix(trimmed, "@ref:opencode:")
		parts := strings.SplitN(rest, ":", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return "", fmt.Errorf("invalid opencode pointer %q", refPointer)
		}
		msgID, segmentIndex, chunkIndex, hasSegment, hasChunk := splitPointerSegment(parts[1])
		if hasSegment {
			return resolveOpenCodeSegmentLive(parts[0], msgID, segmentIndex, chunkIndex, hasChunk)
		}
		return resolveOpenCodeTurnLive(parts[0], parts[1])
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
		return "", fmt.Errorf("localvault: chunk %d out of range for segment %d of %s", chunkIndex, segmentIndex, source)
	}
	return chunks[chunkIndex], nil
}

// resolveZCodeSegmentLive hydrates one speech segment (or one chunk of it) of a ZCode turn.
func resolveZCodeSegmentLive(sessionID, userMsgID string, segmentIndex, chunkIndex int, hasChunk bool) (string, error) {
	zdb, err := openZCodeReadOnly()
	if err != nil {
		return "", err
	}
	segments, _, err := episodic.ZCodeTurnSegmentsUpTo(zdb, sessionID, userMsgID, segmentIndex+1)
	if err != nil {
		return "", err
	}
	if segmentIndex >= len(segments) {
		return "", fmt.Errorf("localvault: segment %d out of range for %s", segmentIndex, userMsgID)
	}
	rendered := episodicunit.Text(segments[segmentIndex].Role, segments[segmentIndex].Text)
	return episodicSegmentChunk(rendered, segmentIndex, chunkIndex, hasChunk, userMsgID)
}

// openZCodeReadOnly resolves the shared read-only ZCode session handle.
func openZCodeReadOnly() (*sql.DB, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil, errors.New("localvault: cannot resolve user home directory")
	}
	zdbPath := harness.SessionStorePath(home, "zcode")
	if zdbPath == "" {
		return nil, errors.New("localvault: zcode session store not configured")
	}
	if _, err := os.Stat(zdbPath); err != nil {
		return nil, err
	}
	return zcodeReadHandle(zdbPath)
}

// openOpenCodeReadOnly resolves the shared read-only OpenCode session handle.
// OpenCode keeps one SQLite store for every session, so pointer hydration reads
// the same file the host is writing (through WAL) without copying it.
func openOpenCodeReadOnly() (*sql.DB, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil, errors.New("localvault: cannot resolve user home directory")
	}
	path := harness.SessionStorePath(home, "opencode")
	if path == "" {
		return nil, errors.New("localvault: opencode session store not configured")
	}
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	return opencodeReadHandle(path)
}

// resolveOpenCodeSegmentLive hydrates one speech segment (or one chunk of it) of
// an OpenCode turn.
func resolveOpenCodeSegmentLive(sessionID, userMsgID string, segmentIndex, chunkIndex int, hasChunk bool) (string, error) {
	odb, err := openOpenCodeReadOnly()
	if err != nil {
		return "", err
	}
	segments, _, err := episodic.OpenCodeTurnSegmentsUpTo(odb, sessionID, userMsgID, segmentIndex+1)
	if err != nil {
		return "", err
	}
	if segmentIndex >= len(segments) {
		return "", fmt.Errorf("localvault: segment %d out of range for %s", segmentIndex, userMsgID)
	}
	rendered := episodicunit.Text(segments[segmentIndex].Role, segments[segmentIndex].Text)
	return episodicSegmentChunk(rendered, segmentIndex, chunkIndex, hasChunk, userMsgID)
}

// resolveOpenCodeTurnLive renders a whole OpenCode turn for a turn-level pointer.
func resolveOpenCodeTurnLive(sessionID, userMsgID string) (string, error) {
	odb, err := openOpenCodeReadOnly()
	if err != nil {
		return "", err
	}
	segments, _, err := episodic.OpenCodeTurnSegmentsUpTo(odb, sessionID, userMsgID, int(^uint(0)>>1))
	if err != nil {
		return "", err
	}
	if len(segments) == 0 {
		return "", errors.New("localvault: empty live opencode turn")
	}
	return strings.Join(renderEpisodicSegments(segments), "\n"), nil
}

func resolveJSONLSegmentLiveContext(ctx context.Context, absPath, turnStr string, segmentIndex, chunkIndex int, hasChunk bool) (string, error) {
	targetTurn, err := strconv.Atoi(strings.TrimSpace(turnStr))
	if err != nil || targetTurn < 0 {
		return "", fmt.Errorf("invalid turn index %q", turnStr)
	}
	segments, err := episodic.JSONLTurnSegmentsContext(ctx, absPath, targetTurn)
	if err != nil {
		return "", err
	}
	if segmentIndex >= len(segments) {
		return "", fmt.Errorf("localvault: segment %d out of range for %s", segmentIndex, absPath)
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
	rows, err := v.db.Query(`
		SELECT d.unit_id, d.layer, d.occurred_at,
		       COALESCE(NULLIF(m.ref_pointer, ''), d.raw_text),
		       COALESCE(m.assistant_id, ''), COALESCE(m.incarnation_id, ''), COALESCE(m.locus, '')
		  FROM rm_docs d
		  LEFT JOIN rm_unit_meta m ON m.unit_id = d.unit_id
		 ORDER BY d.occurred_at DESC, d.unit_id DESC
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
		   FROM rm_noetic
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
		   FROM rm_units
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

// UpdateLocalNoeticManifold updates the engine-allocated manifold address for an existing Noetic record.
func (v *Vault) UpdateLocalNoeticManifold(noeticID, addressURI string, coord64 int64, waveformBar string) error {
	_, err := v.db.Exec(`
		UPDATE rm_noetic 
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
	rows, err := db.Query(`SELECT noetic_id, cited_unit_ids_json FROM rm_noetic WHERE status = 'active'`)
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
			return nil, fmt.Errorf("localvault: decode cited units for Noetic %s: %w", noeticID, err)
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
		if _, err := db.Exec(`UPDATE rm_noetic SET status = 'stale' WHERE noetic_id = ?`, noeticID); err != nil {
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
		return nil, errors.New("localvault: nil Vault db")
	}
	affected, err := invalidateLocalNoeticByCitedDoc(v.db, docID)
	if len(affected) > 0 && v.slotCacheMu != nil && v.snapRef != nil {
		v.slotCacheMu.Lock()
		v.snapRef.noeticReady = false
		v.slotCacheMu.Unlock()
	}
	return affected, err
}

func cloudHitLess(a, b QueryHit) bool {
	if a.FinalWeight != b.FinalWeight {
		return a.FinalWeight > b.FinalWeight
	}
	return a.UnitID < b.UnitID
}

// WarmupCorpusSnapshot eagerly populates the fingerprint-keyed in-memory corpus
// snapshot so subsequent Recall queries on a long-lived daemon hit the warm
// cache without paying the cold SQLite scan cost on the first user query.
func (v *Vault) WarmupCorpusSnapshot(ctx context.Context) error {
	snap, release, err := v.acquireCorpusSnapshot(ctx)
	if err != nil {
		return err
	}
	if snap != nil && release != nil {
		defer release()
	}
	if snap != nil && len(snap.docs) > 0 {
		_ = snap.ensureSnapshotCoreEngine(v)
	}
	noetics, _ := v.loadActiveNoeticRecords()
	if zdb, err := openZCodeReadOnly(); err == nil && zdb != nil {
		var one int
		_ = zdb.QueryRowContext(ctx, `SELECT 1 FROM sqlite_master LIMIT 1`).Scan(&one)
	}
	if snap != nil && len(snap.docs) >= 2048 && v.BuildAllowed() {
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
// across `rm_units`, `rm_episodic_index`, and `rm_bg_chunks`.
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
	if v.runtimeClosed() {
		return nil, nil
	}
	v.checkExternalDBMutation()
	if !v.BuildAllowed() {
		if snap, ok := v.loadPublishedSnapshot(ctx); ok {
			return snap, nil
		}
		return nil, nil
	}
	episodicCodecKey, err := v.episodicCodecKey()
	if v.slotCacheMu != nil && v.snapRef != nil && err == nil {
		v.slotCacheMu.RLock()
		cached := v.snapRef.snap
		lastTime := v.snapRef.lastSnapTime
		genVerified := v.snapRef.snapVerifiedGen == v.snapRef.snapWriteGen
		v.slotCacheMu.RUnlock()
		if cached != nil && genVerified && (len(cached.docs) >= 2048 || cached.stale) && !lastTime.IsZero() && cached.episodicCodecKey == string(episodicCodecKey) {
			return cached, nil
		}
	}
	return v.buildCorpusSnapshot(context.WithoutCancel(ctx))
}
func (v *Vault) packDocCacheEntryWithExtractor(te *plain.TermExtractor, docID, rawText string, occ int64) vaultCachedDocSlots {
	cDoc := v.extractVaultCoreDoc(te, docID, occ, rawText)
	allHashes := make([]uint64, 0, len(cDoc.Terms))
	docLen := 0
	for h, c := range cDoc.Counts {
		allHashes = append(allHashes, h)
		if c > 0 {
			docLen += c
		}
	}
	if docLen < 1 {
		docLen = 1
	}
	return vaultCachedDocSlots{
		rawText:       rawText,
		termMeta:      cDoc.Terms,
		termCounts:    cDoc.Counts,
		termPositions: cDoc.Positions,
		docLen:        docLen,
		allHashes:     allHashes,
	}
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
	// A destructive bulk purge invalidates every pre-purge generation. Readers
	// serve warming until the builder republishes a cluster at the live epoch;
	// build-allowed paths (eval / rmem build) may proceed.
	if !v.BuildAllowed() {
		if state, ok := v.canonicalCorpusState(ctx); ok && state.purgeEpoch > 0 {
			built, known := v.snapshotBuiltPurgeEpoch(ctx)
			if !known || built != state.purgeEpoch {
				return nil, nil
			}
		}
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
	// Canonical snapshot path: the canonical store is the retrieval truth, so the
	// mmap baseline and its bounded delta are resolved before any thin-table scan.
	if canonicalSnap, ok := v.buildCanonicalSnapshotFromCSR(ctx); ok && canonicalSnap != nil {
		var previous *vaultCorpusSnapshot
		if v.slotCacheMu != nil && v.snapRef != nil {
			v.slotCacheMu.Lock()
			if v.snapRef.runtimeClosed {
				v.slotCacheMu.Unlock()
				canonicalSnap.retire()
				return nil, nil
			}
			previous = v.replaceCachedSnapshotLocked(canonicalSnap)
			v.snapRef.lastSnapTime = time.Now()
			if v.snapRef.snapVerifiedGen < genAtStart {
				v.snapRef.snapVerifiedGen = genAtStart
			}
			v.slotCacheMu.Unlock()
		}
		if previous != nil {
			previous.retire()
		}
		return canonicalSnap, nil
	}
	// Read-only recall surfaces never fall through to the legacy rebuild path.
	// Serving the last published snapshot (or warming when none exists) keeps the
	// query path bounded; the background builder republishes the cluster.
	if !v.BuildAllowed() {
		if cached := v.cachedCorpusSnapshot(); cached != nil {
			return cached, nil
		}
		return nil, nil
	}
	// O(1) revision gate, read before any aggregate so a write racing this build
	// can only make the stored revisions conservatively stale (next build re-checks).
	var docsRev, bgRev int64
	revGateOK := v.db.QueryRowContext(ctx, `SELECT docs_rev, bg_rev FROM rm_revision WHERE id = 1`).Scan(&docsRev, &bgRev) == nil

	// Full fast path: both in-database revision counters unchanged proves zero
	// INSERT/UPDATE/DELETE on rm_units and rm_bg_chunks since the
	// published snapshot (triggers fire for every writer process), which is
	// strictly stronger than the aggregate fingerprints they gate.
	if revGateOK && err == nil && v.slotCacheMu != nil && v.snapRef != nil {
		v.slotCacheMu.RLock()
		cached := v.snapRef.snap
		revMatch := cached != nil &&
			v.snapRef.lastDocsRev == docsRev && v.snapRef.lastBgRev == bgRev &&
			cached.episodicCodecKey == string(episodicCodecKey)
		v.slotCacheMu.RUnlock()
		if revMatch {
			var epiRev int64
			_ = v.db.QueryRowContext(ctx, `SELECT episodic_rev FROM rm_revision WHERE id = 1`).Scan(&epiRev)
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

	// rm_units 是 rm_docs × rm_unit_meta 的视图，没有 rowid；写入序指纹取规范表
	// rm_docs.rowid，正文长度仍按视图（ref_pointer 会在视图中替代空正文）。
	var docCnt, docRowSum, docMaxRowID, docTextLen int64
	_ = v.db.QueryRowContext(
		ctx,
		`SELECT
			(SELECT COUNT(*) FROM rm_units),
			(SELECT COALESCE(SUM(rowid), 0) FROM rm_docs),
			(SELECT COALESCE(MAX(rowid), 0) FROM rm_docs),
			(SELECT COALESCE(SUM(LENGTH(raw_text)), 0) FROM rm_units)`,
	).Scan(&docCnt, &docRowSum, &docMaxRowID, &docTextLen)

	var episodicRevision int64
	_ = v.db.QueryRowContext(ctx, `SELECT episodic_rev FROM rm_revision WHERE id = 1`).Scan(&episodicRevision)
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
		if cached := v.snapRef.snap; cached != nil && v.snapRef.lastBgRev == bgRev {
			bgCnt, bgMaxRowID, bgOccSum, bgSlotsLen = cached.bgCnt, cached.bgMaxRowID, cached.bgOccSum, cached.bgSlotsLen
			bgFromRev = true
		}
		v.slotCacheMu.RUnlock()
	}
	if !bgFromRev {
		_ = v.db.QueryRowContext(
			ctx,
			`SELECT COUNT(*), COALESCE(MAX(rowid), 0), COALESCE(SUM(occurred_at), 0), COALESCE(SUM(LENGTH(kept_indices_json)), 0) FROM rm_bg_chunks`,
		).Scan(&bgCnt, &bgMaxRowID, &bgOccSum, &bgSlotsLen)
	}

	if v.slotCacheMu != nil && v.snapRef != nil {
		v.slotCacheMu.RLock()
		// With a readable revision row, the trigger counters are the primary
		// identity: the aggregates alone are blind to length-preserving UPDATEs.
		revExact := !revGateOK || (v.snapRef.lastDocsRev == docsRev && v.snapRef.lastBgRev == bgRev)
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

	var inlineDocs []vaultDocEntry
	{
		rows, err := v.db.QueryContext(ctx, `SELECT doc_id, layer, occurred_at, raw_text, address_uri, coord64, waveform_bar, COALESCE(locus, '') FROM rm_units WHERE substr(trim(raw_text), 1, 5) != '@ref:'`)
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
					localTe := v.newVaultTermExtractor()
					for idx := s; idx < e; idx++ {
						if !rawRows[idx].cached {
							rawRows[idx].entry = v.packDocCacheEntryWithExtractor(localTe, rawRows[idx].docID, rawRows[idx].rawText, rawRows[idx].occ)
						}
					}
				}(start, end)
			}
			wg.Wait()
		} else {
			localTe := v.newVaultTermExtractor()
			for idx := range rawRows {
				if !rawRows[idx].cached {
					rawRows[idx].entry = v.packDocCacheEntryWithExtractor(localTe, rawRows[idx].docID, rawRows[idx].rawText, rawRows[idx].occ)
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
			inlineDocs = append(inlineDocs, vaultDocEntry{
				docID:     r.docID,
				layer:     r.layer,
				occ:       r.occ,
				addrURI:   r.addrURI,
				coord64:   r.coord64,
				waveBar:   r.waveBar,
				locus:     r.locus,
				allHashes: r.entry.allHashes,
				docLen:    1,
			})
		}
	}

	inlineCount := len(inlineDocs)

	var bgDocs []vaultDocEntry
	{
		bgRows, bgErr := v.db.QueryContext(ctx, `SELECT unit_id, occurred_at, address_uri, coord64, waveform_bar, COALESCE(rel_path, ''), COALESCE(abs_path, ''), COALESCE(anchor, '') FROM rm_bg_chunks`)
		if bgErr == nil {
			type rawBgRow struct {
				unitID  string
				occ     int64
				addrURI string
				coord64 uint64
				waveBar string
				relPath string
				absPath string
				anchor  string
				ok      bool
				entry   vaultDocEntry
			}
			var rawBg []rawBgRow
			for bgRows.Next() {
				var r rawBgRow
				var coord64 int64
				if err := bgRows.Scan(&r.unitID, &r.occ, &r.addrURI, &coord64, &r.waveBar, &r.relPath, &r.absPath, &r.anchor); err != nil {
					continue
				}
				r.coord64 = uint64(coord64)
				rawBg = append(rawBg, r)
			}
			bgRows.Close()

			decodeBgOne := func(r *rawBgRow) {
				fileKey := strings.TrimSpace(r.relPath)
				if fileKey == "" {
					fileKey = strings.TrimSpace(r.absPath)
				}
				var docH, blockH uint64
				if fileKey != "" {
					docH = v.KeyedHash64(fileKey)
				}
				if anc := strings.TrimSpace(r.anchor); anc != "" && fileKey != "" {
					blockH = v.KeyedHash64(fileKey + "#" + anc)
				} else if r.unitID != "" {
					blockH = v.KeyedHash64(r.unitID)
				}
				lStart, lEnd := parseBackgroundAnchorLines(r.anchor)
				r.entry = vaultDocEntry{
					docID:     r.unitID,
					layer:     LayerBackground,
					occ:       r.occ,
					addrURI:   r.addrURI,
					coord64:   r.coord64,
					waveBar:   r.waveBar,
					docHash:   docH,
					blockHash: blockH,
					lineStart: lStart,
					lineEnd:   lEnd,
					docLen:    1,
				}
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
					bgDocs = append(bgDocs, rawBg[idx].entry)
				}
			}
		}
	}

	var epiCnt, epiMaxRowID int64
	var episodicDocs []vaultDocEntry
	if len(episodicCodecKey) > 0 {
		// Whole-table fingerprint (no codec_key filter): the snapshot population is
		// the multi-generation union, and every index trigger bumps episodic_revision
		// regardless of generation. A per-generation count would desynchronize the
		// Δrev == Δcnt pure-INSERT invariant the moment historical-generation rows
		// exist, silently demoting every post-write query to a full reload.
		_ = v.db.QueryRowContext(
			ctx,
			`SELECT COUNT(*), COALESCE(MAX(rowid), 0) FROM rm_episodic_index`,
		).Scan(&epiCnt, &epiMaxRowID)

		episodicDocs, _, err = v.indexedEpisodicDocsSince(ctx, 0)
		if err != nil {
			return nil, err
		}
	}

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
	snap := &vaultCorpusSnapshot{
		docCnt:           docCnt,
		docRowSum:        docRowSum,
		maxRowID:         docMaxRowID,
		docTextLen:       docTextLen,
		docsRev:          docsRev,
		bgRev:            bgRev,
		epiCnt:           epiCnt,
		episodicRevision: episodicRevision,
		episodicCodecKey: string(episodicCodecKey),
		bgCnt:            bgCnt,
		bgMaxRowID:       bgMaxRowID,
		bgOccSum:         bgOccSum,
		bgSlotsLen:       bgSlotsLen,
		inlineCount:      inlineCount,
		docs:             docs,
		episodicDocs:     episodicDocs,
		validEpisodicIDs: validEpisodicIDs,
		docIdxByID:       docIdxByID,
	}
	if useCache && v.snapRef != nil {
		var previous *vaultCorpusSnapshot
		v.slotCacheMu.Lock()
		if v.snapRef.runtimeClosed {
			v.slotCacheMu.Unlock()
			snap.retire()
			return nil, nil
		}
		previous = v.replaceCachedSnapshotLocked(snap)
		v.snapRef.lastSnapTime = time.Now()
		if revGateOK {
			v.snapRef.lastDocsRev = docsRev
			v.snapRef.lastBgRev = bgRev
		}
		if v.snapRef.snapVerifiedGen < genAtStart {
			v.snapRef.snapVerifiedGen = genAtStart
		}
		v.slotCacheMu.Unlock()
		if previous != nil {
			previous.retire()
		}
	}
	return snap, nil
}

// SearchClientSDKDefault executes the canonical RB 4-step dual-cluster engine
// (`internal/core` + `sdk/go/pulse/plain`) over the Vault's full in-memory
// corpus snapshot and merges matched Layer-3 Noetic retrieval handles.
func (v *Vault) SearchClientSDKDefault(queryText string, topK int) ([]QueryHit, error) {
	if v == nil || v.db == nil {
		return nil, nil
	}
	if topK <= 0 {
		topK = 10
	}
	if strings.TrimSpace(queryText) == "" {
		return nil, nil
	}
	snap, release, err := v.acquireCorpusSnapshot(context.Background())
	if err != nil || snap == nil {
		return nil, err
	}
	defer release()
	docs := snap.docs
	docIdxMap := snap.docIdxByID
	var hits []QueryHit
	var bestScore float64
	if len(docs) > 0 {
		zHits := snap.searchSnapshotCoreEngines(v, queryText, nil, topK)
		hits = make([]QueryHit, 0, len(zHits))
		for _, zh := range zHits {
			if zh.Energy <= 0 {
				continue
			}
			if zh.Energy > bestScore {
				bestScore = zh.Energy
			}
			if di, ok := docIdxMap[zh.UnitID]; ok && di >= 0 && di < len(docs) {
				d := docs[di]
				hits = append(hits, QueryHit{
					UnitID:         d.docID,
					Layer:          d.layer,
					OccurredAt:     d.occ,
					AddressURI:     d.addrURI,
					AddressCoord64: d.coord64,
					WaveformBar:    d.waveBar,
					WaveScore:      zh.Energy,
					FinalWeight:    zh.Energy,
					DirectWeight:   zh.Direct,
				})
			} else {
				hits = append(hits, QueryHit{
					UnitID:       zh.UnitID,
					Layer:        LayerEpisodic,
					WaveScore:    zh.Energy,
					FinalWeight:  zh.Energy,
					DirectWeight: zh.Direct,
				})
			}
		}
	}

	// Scan local Layer 3 (`rm_noetic`: Agent retrieval handles `cited_unit_ids[]` + Answer stored locally)
	// and apply the cited-set coverage floor to the underlying `episodic` or
	// `background` units cited by matched Noetic records.
	noeticHandles, _ := (&Client{Vault: v}).RecallNoetic(queryText, topK)
	if len(noeticHandles) > 0 {
		qPkt, _ := v.PackBlindPacket("Q-DEFAULT", queryText, 0)
		cueHashes := uniqueAtomHashes(qPkt.Slots)
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
				scaledNoeticW := noeticScale * nWeight * rec.ActivationGain()
				hits = append(hits, QueryHit{
					UnitID:       rec.NoeticID,
					Layer:        LayerNoetic,
					OccurredAt:   rec.OccurredAt,
					CitedUnitIDs: rec.CitedUnitIDs,
					WaveScore:    scaledNoeticW,
					FinalWeight:  scaledNoeticW,
				})
				// The recollection's own evidence may tie the current top score
				// when the cited set jointly covers the cue. Coverage is a
				// physical fraction, not a rank-slot rule: it neither binds a
				// position nor exceeds the corpus top.
				coverage := v.noeticEvidenceCoverageByHashes(rec.CitedUnitIDs, cueHashes)
				target := noeticScale * nWeight * coverage
				for _, cid := range rec.CitedUnitIDs {
					if target <= 0 {
						break
					}
					if idx, exists := hitIdxByID[cid]; exists {
						if hits[idx].FinalWeight < target {
							hits[idx].FinalWeight = target
						}
					} else if di, ok := docIdxMap[cid]; ok {
						d := docs[di]
						hitIdxByID[cid] = len(hits)
						hits = append(hits, QueryHit{
							UnitID:         d.docID,
							Layer:          d.layer,
							OccurredAt:     d.occ,
							AddressURI:     d.addrURI,
							AddressCoord64: d.coord64,
							WaveformBar:    d.waveBar,
							WaveScore:      target,
							FinalWeight:    target,
						})
					}
				}
			}
		}
		sort.SliceStable(hits, func(i, j int) bool {
			return cloudHitLess(hits[i], hits[j])
		})
	}
	if len(hits) > topK {
		hits = hits[:topK]
	}
	return hits, nil
}

// sqliteReadCache caches one read-only handle on a host session store.
// Resolving a zero-copy pointer used to open (and close) a SQLite connection per
// call: a multi-hundred-unit reconcile pass paid a schema read and a WAL index load
// per pointer, which dominated the pass. The read-only handle is shared and has no
// long-lived read transaction, so it does not hold a read mark between queries.
type sqliteReadCache struct {
	mu   sync.Mutex
	path string
	info os.FileInfo
	db   *sql.DB
}

func (c *sqliteReadCache) handle(path string) (*sql.DB, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.db != nil && c.path == path && os.SameFile(c.info, info) {
		return c.db, nil
	}
	// Encode URI metacharacters and Windows drive paths before opening SQLite.
	dsnPath := "file:" + path
	if uriPath, ok := ids.WindowsURIPath(path); ok {
		dsnPath = (&url.URL{Scheme: "file", Path: uriPath}).String()
	} else if strings.ContainsAny(path, "?#") {
		dsnPath = (&url.URL{Scheme: "file", Path: path}).String()
	}
	handle, err := sql.Open("sqlite", dsnPath+"?mode=ro&_pragma=busy_timeout(1500)&_pragma=cache_size(-32768)&_pragma=temp_store(MEMORY)")
	if err != nil {
		return nil, err
	}
	// One connection, kept idle between calls: the turn-prefix cache
	// (internal/episodic) invalidates on PRAGMA data_version, which counts the
	// commits one connection observed. A second pooled connection would read the
	// same file state under a different count and could be served a prefix cached
	// before the source changed, so this handle serves exactly one connection.
	handle.SetMaxOpenConns(1)
	handle.SetMaxIdleConns(1)
	handle.SetConnMaxIdleTime(0)
	handle.SetConnMaxLifetime(0)
	if c.db != nil {
		_ = c.db.Close()
	}
	c.path, c.info, c.db = path, info, handle
	return handle, nil
}

var (
	zcodeReadCache    sqliteReadCache
	opencodeReadCache sqliteReadCache
)

func zcodeReadHandle(path string) (*sql.DB, error) {
	return zcodeReadCache.handle(path)
}

func opencodeReadHandle(path string) (*sql.DB, error) {
	return opencodeReadCache.handle(path)
}

func resolveZCodeTurnLive(sessionID, userMsgID string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", errors.New("localvault: cannot resolve user home directory")
	}
	zdbPath := harness.SessionStorePath(home, "zcode")
	if zdbPath == "" {
		return "", errors.New("localvault: zcode session store not configured")
	}
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
		return "", errors.New("localvault: empty live zcode turn")
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

func resolveJSONLTurnLiveContext(ctx context.Context, absPath, turnStr string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
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
	if err := ctx.Err(); err != nil {
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

	for lineIndex, line := range lines {
		if lineIndex%512 == 0 {
			if err := ctx.Err(); err != nil {
				return "", err
			}
		}
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
