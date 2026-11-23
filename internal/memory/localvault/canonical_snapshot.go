// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package localvault

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/rocareer/rolling-memory/internal/config"
	core "github.com/rocareer/rolling-memory/internal/core"
)

// canonicalCorpusState is the O(1) freshness identity of the canonical store.
// docs_rev/terms_rev/meta_rev move with every trigger-backed write, so a
// signature match proves the mmap baseline still describes this corpus.
type canonicalCorpusState struct {
	docsRev    int64
	termsRev   int64
	metaRev    int64
	docCnt     int64
	termCnt    int64
	changeSeq  int64
	purgeEpoch int64
	codecKey   string
	signature  string
}

// canonicalCorpusState reads the revision counters and corpus counts. The
// revision row is maintained by triggers on the canonical core tables.
func (v *Vault) canonicalCorpusState(ctx context.Context) (canonicalCorpusState, bool) {
	var state canonicalCorpusState
	if v == nil || v.db == nil {
		return state, false
	}
	if err := v.db.QueryRowContext(ctx, `SELECT docs_rev, terms_rev, meta_rev, docs_cnt, terms_cnt, change_seq, purge_epoch FROM rm_revision WHERE id = 1`).
		Scan(&state.docsRev, &state.termsRev, &state.metaRev, &state.docCnt, &state.termCnt, &state.changeSeq, &state.purgeEpoch); err != nil {
		return state, false
	}
	key, err := v.episodicCodecKey()
	if err != nil {
		return state, false
	}
	state.codecKey = hex.EncodeToString(key)
	state.signature = fmt.Sprintf("v5|%d|%d|%d|%d|%d|%s",
		state.docsRev, state.termsRev, state.metaRev, state.docCnt, state.termCnt, state.codecKey)
	return state, true
}

// canonicalSnapshotDocs builds the lightweight metadata view of the canonical
// corpus. Terms are NOT loaded here: the engine opens them through the .zcsr
// baseline or Store.LoadIndex; per-unit deltas load only the changed units.
func (v *Vault) canonicalSnapshotDocs(ctx context.Context) ([]vaultDocEntry, map[string]int64, error) {
	rows, err := v.db.QueryContext(ctx, `
		SELECT d.unit_id, d.layer, d.occurred_at, d.doc_hash, d.block_hash, d.line_start, d.line_end,
		       COALESCE(m.address_uri, ''), COALESCE(m.coord64, 0), COALESCE(m.waveform_bar, ''),
		       COALESCE(m.locus, ''), COALESCE(m.ref_pointer, ''), COALESCE(m.meta_rev, 0)
		FROM rm_docs d
		LEFT JOIN rm_unit_meta m ON m.unit_id = d.unit_id
		ORDER BY d.unit_id`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	docs := make([]vaultDocEntry, 0, 4096)
	revs := make(map[string]int64, 4096)
	for rows.Next() {
		var entry vaultDocEntry
		var coord64, metaRev, docHash, blockHash int64
		if err := rows.Scan(&entry.docID, &entry.layer, &entry.occ, &docHash, &blockHash,
			&entry.lineStart, &entry.lineEnd, &entry.addrURI, &coord64, &entry.waveBar,
			&entry.locus, &entry.refPointer, &metaRev); err != nil {
			return nil, nil, err
		}
		entry.coord64 = uint64(coord64)
		entry.docHash = uint64(docHash)
		entry.blockHash = uint64(blockHash)
		entry.docLen = 1
		docs = append(docs, entry)
		revs[entry.docID] = metaRev
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return docs, revs, nil
}

// canonicalSnapshotDocsFor loads the metadata view for a bounded set of units.
// It is the delta-path counterpart of canonicalSnapshotDocs: one chunked query
// instead of a full table scan.
func (v *Vault) canonicalSnapshotDocsFor(ctx context.Context, unitIDs []string) ([]vaultDocEntry, bool) {
	if v == nil || v.db == nil {
		return nil, false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if len(unitIDs) == 0 {
		return nil, true
	}
	unique := make([]string, 0, len(unitIDs))
	seen := make(map[string]struct{}, len(unitIDs))
	for _, id := range unitIDs {
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	sort.Strings(unique)
	out := make([]vaultDocEntry, 0, len(unique))
	const chunk = 400
	for start := 0; start < len(unique); start += chunk {
		end := start + chunk
		if end > len(unique) {
			end = len(unique)
		}
		batch := unique[start:end]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
		args := toAnySlice(batch)
		rows, err := v.db.QueryContext(ctx, `
			SELECT d.unit_id, d.layer, d.occurred_at, d.doc_hash, d.block_hash, d.line_start, d.line_end,
			       COALESCE(m.address_uri, ''), COALESCE(m.coord64, 0), COALESCE(m.waveform_bar, ''),
			       COALESCE(m.locus, ''), COALESCE(m.ref_pointer, ''), COALESCE(m.meta_rev, 0)
			FROM rm_docs d
			LEFT JOIN rm_unit_meta m ON m.unit_id = d.unit_id
			WHERE d.unit_id IN (`+placeholders+`)
			ORDER BY d.unit_id`, args...)
		if err != nil {
			return nil, false
		}
		for rows.Next() {
			var entry vaultDocEntry
			var coord64, metaRev, docHash, blockHash int64
			if err := rows.Scan(&entry.docID, &entry.layer, &entry.occ, &docHash, &blockHash,
				&entry.lineStart, &entry.lineEnd, &entry.addrURI, &coord64, &entry.waveBar,
				&entry.locus, &entry.refPointer, &metaRev); err != nil {
				rows.Close()
				return nil, false
			}
			entry.coord64 = uint64(coord64)
			entry.docHash = uint64(docHash)
			entry.blockHash = uint64(blockHash)
			entry.docLen = 1
			out = append(out, entry)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, false
		}
		rows.Close()
	}
	return out, true
}

// buildDeltaSnapshotFromCSR serves the cached docs view plus the change-log
// delta. It only reads touched units; a cold start, a sequence gap, or an
// oversized delta returns false so the caller can take the full scan.
func (v *Vault) buildDeltaSnapshotFromCSR(ctx context.Context, state canonicalCorpusState, cached *vaultCorpusSnapshot, eng *core.Engine, storedLine string) (*vaultCorpusSnapshot, bool) {
	if v == nil || v.db == nil || cached == nil || eng == nil || eng.Index == nil {
		return nil, false
	}
	if cached.changeSeq <= 0 || cached.changeSeq > state.changeSeq {
		return nil, false
	}
	if cached.episodicCodecKey != state.codecKey || len(cached.docs) == 0 || len(cached.docs) > corpusDeltaCopyLimit {
		return nil, false
	}
	if storedCodec := signatureCodecKey(storedLine); storedCodec == "" || storedCodec != state.codecKey {
		return nil, false
	}
	baseSeq, ok := v.snapshotBuiltChangeSeq(ctx)
	if !ok || baseSeq <= 0 || baseSeq > state.changeSeq || cached.changeSeq < baseSeq {
		return nil, false
	}
	changes, complete := v.changeRowsAfter(ctx, baseSeq, state.changeSeq, maxCSRDeltaDocs+1)
	if !complete {
		return nil, false
	}
	if state.changeSeq > baseSeq && len(changes) == 0 {
		// The log was pruned past the cluster watermark; reload the full view.
		return nil, false
	}
	finalEngineOp := make(map[string]string, len(changes))
	finalViewOp := make(map[string]string, len(changes))
	for _, change := range changes {
		finalEngineOp[change.unitID] = change.op
		if change.seq > cached.changeSeq {
			finalViewOp[change.unitID] = change.op
		}
	}
	upserts := make([]string, 0, len(finalEngineOp))
	masked := make([]string, 0, 8)
	for id, op := range finalEngineOp {
		if op == "delete" {
			masked = append(masked, id)
			continue
		}
		upserts = append(upserts, id)
	}
	if len(upserts)+len(masked) > maxCSRDeltaDocs {
		return nil, false
	}
	var snapEng *core.Engine
	if len(upserts) > 0 || len(masked) > 0 {
		deltaDocs, okDocs := v.loadCanonicalDocs(ctx, upserts)
		if !okDocs {
			return nil, false
		}
		snapEng = eng.OverlayDelta(deltaDocs, masked)
	} else {
		snapEng = eng
	}
	docs := make([]vaultDocEntry, len(cached.docs))
	copy(docs, cached.docs)
	viewUpserts := make([]string, 0, len(finalViewOp))
	viewDeletes := make(map[string]struct{}, 8)
	for id, op := range finalViewOp {
		if op == "delete" {
			viewDeletes[id] = struct{}{}
			continue
		}
		viewUpserts = append(viewUpserts, id)
	}
	if len(viewUpserts) > 0 {
		refreshed, okDocs := v.canonicalSnapshotDocsFor(ctx, viewUpserts)
		if !okDocs {
			return nil, false
		}
		found := make(map[string]struct{}, len(refreshed))
		for _, entry := range refreshed {
			found[entry.docID] = struct{}{}
			if idx, exists := cached.docIdxByID[entry.docID]; exists && idx >= 0 && idx < len(docs) {
				docs[idx] = entry
				continue
			}
			docs = append(docs, entry)
		}
		for _, id := range viewUpserts {
			if _, exists := found[id]; !exists {
				viewDeletes[id] = struct{}{}
			}
		}
	}
	if len(viewDeletes) > 0 {
		kept := docs[:0]
		for _, entry := range docs {
			if _, gone := viewDeletes[entry.docID]; gone {
				continue
			}
			kept = append(kept, entry)
		}
		docs = kept
	}
	return canonicalSnapshotFromDocs(state, docs, snapEng), true
}

// canonicalSnapshotFromDocs assembles the snapshot around a ready engine.
func canonicalSnapshotFromDocs(state canonicalCorpusState, docs []vaultDocEntry, eng *core.Engine) *vaultCorpusSnapshot {
	snap := &vaultCorpusSnapshot{
		docCnt:             state.docCnt,
		docsRev:            state.docsRev,
		changeSeq:          state.changeSeq,
		purgeEpoch:         state.purgeEpoch,
		episodicCodecKey:   state.codecKey,
		freshnessSignature: state.signature,
		docs:               docs,
		validEpisodicIDs:   make(map[string]struct{}, 1024),
		docIdxByID:         make(map[string]int, len(docs)),
		coreEngine:         eng,
	}
	for i := range docs {
		snap.docIdxByID[docs[i].docID] = i
		if docs[i].layer == LayerEpisodic {
			snap.validEpisodicIDs[docs[i].docID] = struct{}{}
			snap.episodicDocs = append(snap.episodicDocs, docs[i])
		}
	}
	snap.epiCnt = int64(len(snap.episodicDocs))
	snap.coreOnce.Do(func() {})
	return snap
}

// staleCanonicalSnapshot serves the last complete mmap baseline while the
// background builder catches up. Recent units are visible in the docs view but
// not yet searchable until the next published cluster.
func staleCanonicalSnapshot(state canonicalCorpusState, docs []vaultDocEntry, eng *core.Engine) *vaultCorpusSnapshot {
	snap := canonicalSnapshotFromDocs(state, docs, eng)
	if snap != nil {
		snap.stale = true
	}
	return snap
}

// buildCanonicalSnapshotFromCSR reuses the mmap baseline when the canonical
// revision signature matches, or serves baseline + a bounded delta when only a
// few units changed. Any other drift returns false and the caller rebuilds from
// Store.LoadIndex.
func (v *Vault) buildCanonicalSnapshotFromCSR(ctx context.Context) (outSnap *vaultCorpusSnapshot, found bool) {
	// Every snapshot returned by this function is projected from one shared
	// baseline engine. Hold a lease for the snapshot so replacing the cache
	// cannot unmap the mapping under a live projection.
	var baseEngine *core.Engine
	var baseHold func()
	defer func() {
		if found && outSnap != nil && baseEngine != nil && outSnap.baseRelease == nil {
			v.attachSnapshotEngine(outSnap, baseEngine)
		}
		if baseHold != nil {
			baseHold()
		}
	}()
	state, ok := v.canonicalCorpusState(ctx)
	if !ok {
		return nil, false
	}
	if state.docCnt == 0 {
		return canonicalSnapshotFromDocs(state, nil, nil), true
	}
	var cached *vaultCorpusSnapshot
	if v.slotCacheMu != nil && v.snapRef != nil {
		v.slotCacheMu.RLock()
		cached = v.snapRef.snap
		if cached != nil && !cached.stale && cached.freshnessSignature == state.signature {
			v.slotCacheMu.RUnlock()
			return cached, true
		}
		v.slotCacheMu.RUnlock()
	}
	dbPath := v.mainDBPath()
	if strings.TrimSpace(dbPath) == "" {
		if !v.BuildAllowed() {
			return nil, false
		}
		docs, _, err := v.canonicalSnapshotDocs(ctx)
		if err != nil || len(docs) == 0 {
			return nil, false
		}
		eng := v.loadCanonicalEngine(ctx)
		if eng == nil {
			return nil, false
		}
		baseEngine = eng
		return canonicalSnapshotFromDocs(state, docs, eng), true
	}
	eng, storedLine, engHold, ok := v.acquireCachedCSREngine(dbPath)
	if !ok || eng == nil || eng.Index == nil {
		if engHold != nil {
			engHold()
		}
		// No reusable baseline: build from the canonical store and persist it so
		// the next start opens the mmap path directly. Reader surfaces never take
		// this branch; the background builder publishes the cluster instead.
		if !v.BuildAllowed() {
			return nil, false
		}
		built := v.loadCanonicalEngine(ctx)
		if built == nil {
			return nil, false
		}
		// Lease the freshly built baseline before exposing it to the shared
		// cache, so a concurrent cache drop cannot unmap it mid-projection.
		baseHold = v.holdSnapshotEngine(built)
		if csrPath, okPersist := persistSnapshotCoreCSR(dbPath, state.signature, built, v, []byte(state.codecKey)); okPersist {
			_ = core.AttachDiskClusterCSR(csrPath, built, true)
			_ = v.persistCanonicalUnitRevisions(dbPath)
			v.cacheCSREngineIdentity(csrPath, built)
		}
		docs, _, err := v.canonicalSnapshotDocs(ctx)
		if err != nil || len(docs) == 0 {
			return nil, false
		}
		baseEngine = built
		return canonicalSnapshotFromDocs(state, docs, built), true
	}
	baseEngine = eng
	baseHold = engHold
	// Materialize the mmap baseline's cluster state once. OverlayDelta shares the
	// base engine's state pointer, so every later write produces a delta snapshot
	// that reuses this state instead of rebuilding the whole corpus cluster state
	// on each recall. Without this, continuous background ingestion makes every
	// snapshot pay the full build while the previous build is still in flight.
	eng.MaterializeClusterState()
	// The published docs sidecar is the read-optimised metadata view. It makes
	// cold start O(sidecar) instead of a canonical table scan; the change log
	// supplies everything that happened after the build.
	if cached == nil || cached.stale {
		if sidecarDocs, sidecarHeader, okDocs := loadCorpusDocsSidecar(dbPath); okDocs &&
			signatureCodecKey(sidecarHeader.Signature) == state.codecKey &&
			sidecarHeader.ChangeSeq > 0 && sidecarHeader.ChangeSeq <= state.changeSeq {
			if base := canonicalSnapshotFromDocs(state, sidecarDocs, nil); base != nil {
				base.changeSeq = sidecarHeader.ChangeSeq
				if deltaSnap, okDelta := v.buildDeltaSnapshotFromCSR(ctx, state, base, eng, storedLine); okDelta {
					return deltaSnap, true
				}
				if strings.TrimSpace(storedLine) == state.signature {
					return canonicalSnapshotFromDocs(state, sidecarDocs, eng), true
				}
			}
		}
	}
	// The change log carries every doc/meta mutation since the published
	// cluster. When the cached docs view is usable, only the touched units are
	// read from SQLite; the full canonical scan stays as the fallback for a cold
	// start, a gap, or an oversized delta.
	if cached != nil {
		if deltaSnap, okDelta := v.buildDeltaSnapshotFromCSR(ctx, state, cached, eng, storedLine); okDelta {
			return deltaSnap, true
		}
	}
	docs, revs, err := v.canonicalSnapshotDocs(ctx)
	if err != nil || len(docs) == 0 {
		return nil, false
	}
	isExact := strings.TrimSpace(storedLine) == state.signature
	if isExact {
		if int64(len(docs)) != int64(eng.Index.N) {
			if v.BuildAllowed() {
				return nil, false
			}
			return staleCanonicalSnapshot(state, docs, eng), true
		}
		return canonicalSnapshotFromDocs(state, docs, eng), true
	}
	storedCodec := signatureCodecKey(storedLine)
	if storedCodec == "" || storedCodec != state.codecKey {
		if v.BuildAllowed() {
			return nil, false
		}
		return staleCanonicalSnapshot(state, docs, eng), true
	}
	base, ok := v.loadCanonicalUnitRevisions(dbPath)
	if !ok {
		if v.BuildAllowed() {
			return nil, false
		}
		return staleCanonicalSnapshot(state, docs, eng), true
	}
	deltaIDs := make([]string, 0, 64)
	masked := make([]string, 0, 16)
	for _, doc := range docs {
		prev, seen := base[doc.docID]
		if !seen || prev != revs[doc.docID] {
			deltaIDs = append(deltaIDs, doc.docID)
			if len(deltaIDs) > maxCSRDeltaDocs {
				if v.BuildAllowed() {
					return nil, false
				}
				return staleCanonicalSnapshot(state, docs, eng), true
			}
		}
	}
	for id := range base {
		if _, alive := revs[id]; !alive {
			masked = append(masked, id)
			if len(deltaIDs)+len(masked) > maxCSRDeltaDocs {
				if v.BuildAllowed() {
					return nil, false
				}
				return staleCanonicalSnapshot(state, docs, eng), true
			}
		}
	}
	var snapEng *core.Engine
	if len(deltaIDs) > 0 || len(masked) > 0 {
		deltaDocs, ok := v.loadCanonicalDocs(ctx, deltaIDs)
		if !ok {
			return nil, false
		}
		snapEng = eng.OverlayDelta(deltaDocs, masked)
	} else {
		snapEng = eng
	}
	return canonicalSnapshotFromDocs(state, docs, snapEng), true
}

// canonicalSignature returns the current v5 freshness line when the canonical
// store is populated; empty means the legacy thin fallback is still in charge.
func (v *Vault) canonicalSignature() string {
	state, ok := v.canonicalCorpusState(context.Background())
	if !ok || state.docCnt == 0 {
		return ""
	}
	return state.signature
}

// loadCanonicalEngine builds the engine from the canonical store without any
// disk-cluster persistence (used for in-memory vaults and cold builds).
func (v *Vault) loadCanonicalEngine(ctx context.Context) *core.Engine {
	if v == nil || v.db == nil {
		return nil
	}
	if restore := applyColdBuildWindow(v.coldBuildLimit()); restore != nil {
		defer restore()
	}
	store, err := v.canonicalStore()
	if err != nil {
		return nil
	}
	idx, err := store.LoadIndex()
	if err != nil || idx == nil || idx.N == 0 {
		return nil
	}
	eng := core.NewEngine(idx)
	eng.AnchorGate = config.AnchorGateEnabled()
	eng.MaterializeClusterState()
	return eng
}

// loadCanonicalDocs reconstructs core.Doc values from the canonical tables for
// the incremental overlay. It reads terms as stored by core.Store.Ingest; no
// packed atoms and no role inference are involved.
// loadCanonicalDocs reconstructs the kernel unit representation for a bounded
// delta batch. The hydration itself lives in loadChangedDocs so the builder and
// the overlay share one implementation.
func (v *Vault) loadCanonicalDocs(ctx context.Context, unitIDs []string) ([]core.Doc, bool) {
	docs, err := v.loadChangedDocs(ctx, unitIDs)
	if err != nil {
		return nil, false
	}
	return docs, true
}

// cacheCSREngineIdentity records the freshly persisted baseline on the shared
// snapshot ref so later calls reuse the same mmap handle.
func (v *Vault) cacheCSREngineIdentity(csrPath string, eng *core.Engine) {
	if v == nil || v.slotCacheMu == nil || v.snapRef == nil {
		return
	}
	st, err := os.Stat(csrPath)
	if err != nil || st.Size() == 0 {
		return
	}
	v.swapCachedCSREngine(eng, csrPath, st.ModTime().UnixNano(), st.Size())
}

func clampPosU16(value int) uint16 {
	if value <= 0 {
		return 0
	}
	if value > 65535 {
		return 65535
	}
	return uint16(value)
}

// signatureCodecKey returns the codec-key field of a v5 freshness signature.
func signatureCodecKey(line string) string {
	parts := strings.Split(strings.TrimSpace(line), "|")
	if len(parts) != 7 || parts[0] != "v5" {
		return ""
	}
	return parts[6]
}

// persistCanonicalUnitRevisions writes the unit -> meta_rev sidecar used to
// bound the delta set on the next start.
func (v *Vault) persistCanonicalUnitRevisions(dbPath string) error {
	return v.persistCanonicalUnitRevisionsTo(vaultCSRUnitPaths(dbPath))
}

// persistCanonicalUnitRevisionsTo writes the unit sidecar to an explicit path,
// used by both the legacy single-slot writer and the generation directory.
func (v *Vault) persistCanonicalUnitRevisionsTo(path string) error {
	if v == nil || v.db == nil || strings.TrimSpace(path) == "" {
		return nil
	}
	rows, err := v.db.Query(`SELECT unit_id, meta_rev FROM rm_unit_meta ORDER BY unit_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var unitID string
		var rev int64
		if err := rows.Scan(&unitID, &rev); err != nil {
			return err
		}
		fmt.Fprintf(&b, "%s\t%d\n", unitID, rev)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// loadCanonicalUnitRevisions reads the unit -> meta_rev sidecar.
func (v *Vault) loadCanonicalUnitRevisions(dbPath string) (map[string]int64, bool) {
	_, _, unitsPath, _, _, ok := resolvePublishedSnapshotPaths(dbPath)
	if !ok {
		return nil, false
	}
	raw, err := os.ReadFile(unitsPath)
	if err != nil {
		return nil, false
	}
	out := make(map[string]int64)
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			return nil, false
		}
		var rev int64
		if _, err := fmt.Sscanf(parts[1], "%d", &rev); err != nil {
			return nil, false
		}
		out[parts[0]] = rev
	}
	return out, len(out) > 0
}
