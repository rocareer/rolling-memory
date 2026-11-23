// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package localvault

import (
	"context"
	"math"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"github.com/rocareer/rolling-memory/internal/config"
	core "github.com/rocareer/rolling-memory/internal/core"
	"github.com/rocareer/rolling-memory/sdk/go/public/closedset"
	"github.com/rocareer/rolling-memory/sdk/go/public/shaping"
	plain "github.com/rocareer/rolling-memory/sdk/go/pulse/plain"
)

// CoreClusterEmbedded reports whether the full 4-step dual-cluster engine
// (`sdk/go/pulse/plain`) is compiled into this binary.
const CoreClusterEmbedded = true

// withinNaturalEnergyEnvelope reports whether score stays within the natural 1/e
// energy envelope of peak (score >= peak / e).
func withinNaturalEnergyEnvelope(score, peak float64) bool {
	return score*math.E >= peak
}

// noeticFusionDoc projects a matched Layer-3 consolidation hit into the
// index as a pure citation carrier: identity, occurrence clock, layer tag
// and the declarative CitedIDs edge list, with zero fabricated terms. The
// kernel registers term-less units in UnitMeta, so the citation provenance
// edges (subject-cos floor + complementary anchor bridge) stay active while
// the carrier itself never competes in the lexical channel.
func noeticFusionDoc(hit QueryHit) (core.Doc, bool) {
	if hit.UnitID == "" || len(hit.CitedUnitIDs) == 0 {
		return core.Doc{}, false
	}
	return core.Doc{
		UnitID:     hit.UnitID,
		OccurredAt: hit.OccurredAt,
		Layer:      LayerNoetic,
		CitedIDs:   append([]string(nil), hit.CitedUnitIDs...),
	}, true
}

// effectiveVaultHarmonics returns the Vault's canonical harmonic configuration
// for the full-corpus core.Engine. Unless the caller explicitly disabled
// harmonics via SetCanonicalHarmonics({}) or DisableAutoHarmonic, core.Engine
// defaults to the full RB canonical companion tags and structured date beacons.
func (v *Vault) effectiveVaultHarmonics() plain.CanonicalHarmonicOptions {
	if v == nil {
		return plain.CanonicalHarmonicOptions{
			EnableStructuredDates: true,
			Expander:              plain.CanonicalCompanionTags,
		}
	}
	if v.harmOptsExplicit {
		if v.harmOpts.EnableStructuredDates && v.harmOpts.Expander == nil && !v.segOpts.DisableAutoHarmonic {
			opts := v.harmOpts
			opts.Expander = plain.CanonicalCompanionTags
			return opts
		}
		return v.harmOpts
	}
	if v.segOpts.DisableAutoHarmonic || v.segOpts.Segmenter != nil {
		return v.harmOpts
	}
	return plain.CanonicalHarmonicOptions{
		EnableStructuredDates: true,
		Expander:              plain.CanonicalCompanionTags,
	}
}

// newVaultTermExtractor constructs a canonical plain.TermExtractor bound to the
// Vault's raw 256-bit secretKey and effective harmonic options.
func (v *Vault) newVaultTermExtractor() *plain.TermExtractor {
	var te *plain.TermExtractor
	if v != nil && v.segOpts.DisableAutoHarmonic {
		te = plain.NewBaselineTermExtractor(nil)
	} else {
		te = plain.NewTermExtractor(nil)
	}
	if v != nil {
		enc := te.Encoder()
		*enc = *plain.NewBlindEncoderFromRawKey(v.secretKey)
		enc.SetCanonicalHarmonics(v.effectiveVaultHarmonics())
		// Do not forward SentenceAtomPacker to TermExtractor's underlying
		// BlindEncoder: ExtractDoc / ExtractQueryWithPositions calls
		// enc.packSentenceWords directly, only consults harmOpts, and does not
		// use SentenceAtomPacker. Only custom Segmenter is forwarded when set.
		if v.segOpts.Segmenter != nil || v.segOpts.DisableAutoHarmonic {
			enc.SetSegmenterOptions(v.segOpts)
		}
	}
	return te
}

// extractVaultCoreDoc converts a plaintext document into a canonical core.Doc
// using the Vault's TermExtractor (or custom Segmenter when explicitly configured).
func (v *Vault) extractVaultCoreDoc(te *plain.TermExtractor, docID string, occ int64, rawText string) core.Doc {
	if te == nil {
		te = v.newVaultTermExtractor()
	}
	return te.ExtractDoc(docID, occ, rawText)
}

// applyColdBuildWindow raises the runtime soft memory limit to the given build
// window and returns the function that restores the previous limit (and returns
// cached pages). A zero window, or one no larger than the current limit, leaves
// the runtime untouched and returns nil.
func applyColdBuildWindow(limit int64) func() {
	if limit <= 0 {
		return nil
	}
	previous := debug.SetMemoryLimit(-1)
	if previous >= limit {
		return nil
	}
	debug.SetMemoryLimit(limit)
	return func() {
		debug.SetMemoryLimit(previous)
		debug.FreeOSMemory()
	}
}

// ensureSnapshotCoreEngine lazily builds (and caches on the immutable snapshot)
// the full-corpus core.Engine over all snapshot documents.
//
// 冷启动路径：优先按语料签名复用同目录的 .zcsr 磁盘簇（mmap 零拷贝，毫秒级）；
// 缺失/失效时全量构建一次并把结果原子写回磁盘簇，下一次（含后续进程/守护进程重启）
// 即走零拷贝路径。dbPath 为空（内存库等）时不做磁盘缓存，行为与历史一致。
func (s *vaultCorpusSnapshot) ensureSnapshotCoreEngine(v *Vault) *core.Engine {
	if s == nil || len(s.docs) == 0 {
		return nil
	}
	var dbPath string
	if v != nil {
		dbPath = v.mainDBPath()
	}
	s.coreLoadMu.Lock()
	defer s.coreLoadMu.Unlock()
	if s.coreEngine != nil {
		return s.coreEngine
	}
	signature := s.freshnessLine()
	if v != nil {
		if canonical := v.canonicalSignature(); canonical != "" {
			signature = canonical
		}
	}
	// The disk cluster is the only read-path source. Reopening it is a cheap
	// mmap and never advances to the full build below.
	if eng, ok := openSnapshotCoreEngineFromCSR(dbPath, signature); ok {
		s.setBaseCoreEngine(eng)
		v.attachSnapshotEngine(s, eng)
		return s.coreEngine
	}
	// Recall surfaces run with builds disabled: a missing or stale cluster
	// degrades to a warming snapshot and the background builder republishes.
	if v == nil || !v.BuildAllowed() {
		return nil
	}
	s.coreOnce.Do(func() {
		eng, hold := v.buildAndPersistCorpusEngine(context.Background(), signature, []byte(s.episodicCodecKey))
		s.setBaseCoreEngine(eng)
		if eng != nil {
			v.attachSnapshotEngine(s, eng)
		}
		if hold != nil {
			hold()
		}
	})
	return s.coreEngine
}

// setBaseCoreEngine installs a freshly loaded base engine and invalidates any
// derived view built from the previous one. Callers hold coreLoadMu.
func (s *vaultCorpusSnapshot) setBaseCoreEngine(eng *core.Engine) *core.Engine {
	s.coreEngine = eng
	s.noeticEngine = nil
	s.noeticEdgesKey = ""
	return eng
}

// coreEngineWithNoeticCitations folds the active recollections' declared
// citation records into a derived engine's co-citation CSR. The base engine
// stays immutable and shared; the derived view is cached by the noetic
// fingerprint, so a recall pays the CSR rebuild only when the trace set
// actually changes. Engines carrying a delta overlay are used unchanged.
func (s *vaultCorpusSnapshot) coreEngineWithNoeticCitations(v *Vault, eng *core.Engine) *core.Engine {
	if s == nil || v == nil || eng == nil {
		return eng
	}
	records, _, ok := v.noeticCacheSnapshot()
	if !ok {
		// A recall must see the trace written by the previous call; load once.
		if _, err := v.loadActiveNoeticRecords(); err != nil {
			return eng
		}
		records, _, ok = v.noeticCacheSnapshot()
	}
	if !ok || len(records) == 0 {
		return eng
	}
	key := noeticCitationSetsKey(records)
	if key == "" {
		return eng
	}
	s.coreLoadMu.Lock()
	defer s.coreLoadMu.Unlock()
	base := s.coreEngine
	if base == nil {
		base = eng
	}
	if s.noeticEngine != nil && s.noeticEdgesKey == key {
		return s.noeticEngine
	}
	sets := make([][]string, 0, len(records))
	for _, rec := range records {
		if len(rec.CitedUnitIDs) > 0 {
			sets = append(sets, rec.CitedUnitIDs)
		}
	}
	if len(sets) == 0 {
		return eng
	}
	derived := base.WithExternalCoCitations(sets)
	s.noeticEngine = derived
	s.noeticEdgesKey = key
	return derived
}

// buildAndPersistCorpusEngine materialises the full corpus engine and writes the
// legacy single-slot cluster. Long-lived readers never call it; the background
// builder publishes a generation directory instead.
func (v *Vault) buildAndPersistCorpusEngine(ctx context.Context, signature string, codecKey []byte) (*core.Engine, func()) {
	eng := v.buildCorpusEngine(ctx)
	if eng == nil {
		return nil, nil
	}
	// Lease the baseline before it becomes reachable through the shared cache;
	// the caller releases the hold after the snapshot owns its own reference.
	hold := v.holdSnapshotEngine(eng)
	dbPath := v.mainDBPath()
	if dbPath == "" {
		return eng, hold
	}
	if csrPath, ok := persistSnapshotCoreCSR(dbPath, signature, eng, v, codecKey); ok {
		_ = core.AttachDiskClusterCSR(csrPath, eng, true)
		_ = v.persistCanonicalUnitRevisions(dbPath)
		if st, sErr := os.Stat(csrPath); sErr == nil && st.Size() > 0 {
			v.swapCachedCSREngine(eng, csrPath, st.ModTime().UnixNano(), st.Size())
		}
	}
	return eng, hold
}

// buildCorpusEngine materialises the engine from the canonical store. It is the
// single full-build primitive shared by the legacy writer and the slot
// publisher.
func (v *Vault) buildCorpusEngine(ctx context.Context) *core.Engine {
	if v == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return nil
	}
	if restore := applyColdBuildWindow(v.coldBuildLimit()); restore != nil {
		defer restore()
	}
	// 全量构建直接以规范存储为源：Store.LoadIndex 在单事务快照内重建倒排，
	// 词项只来自 rm_terms，不再从打包原子重建或推断角色。
	store, err := v.canonicalStore()
	if err != nil {
		return nil
	}
	idx, err := store.LoadIndex()
	if err != nil || idx == nil || idx.N == 0 {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return nil
	}
	eng := core.NewEngine(idx)
	eng.AnchorGate = config.AnchorGateEnabled()
	eng.MaterializeClusterState()
	return eng
}

// extractQueryFromBlindPacket reconstructs query terms and positions when a
// caller invokes LocalVaultTransport.QueryBlindManifold with a pre-packed
// BlindPulsePacket instead of raw query text.
func (v *Vault) extractQueryFromBlindPacket(idx *core.Index, pkt BlindPulsePacket) (map[uint64]core.Term, map[uint64][]core.TermPosition) {
	text := strings.TrimSpace(pkt.RawText)
	if text == "" {
		return nil, nil
	}
	qTerms, _, qPos := v.newVaultTermExtractor().ExtractQueryWithPositions(idx, text)
	return qTerms, qPos
}

// Unused import guards to keep closedset/shaping available when needed.
var (
	_ = strings.TrimSpace
	_ = time.Unix
	_ = closedset.HasToken
	_ = shaping.CanonicalizeApostrophe
)
