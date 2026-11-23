// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package localvault

import (
	"context"
	"os"
	"strings"
	"time"

	core "github.com/rocareer/rolling-memory/internal/core"
)

func (s *vaultCorpusSnapshot) acquireReader() (func(), bool) {
	if s == nil {
		return nil, false
	}
	s.lifecycleMu.Lock()
	if s.retired {
		s.lifecycleMu.Unlock()
		return nil, false
	}
	s.readers++
	s.lifecycleMu.Unlock()
	return s.releaseReader, true
}

func (s *vaultCorpusSnapshot) releaseReader() {
	if s == nil {
		return
	}
	s.lifecycleMu.Lock()
	if s.readers > 0 {
		s.readers--
	}
	closeNow := s.retired && s.readers == 0
	s.lifecycleMu.Unlock()
	if closeNow {
		s.closeOwnedEngine()
	}
}

func (s *vaultCorpusSnapshot) retire() {
	if s == nil {
		return
	}
	s.lifecycleMu.Lock()
	if s.retired {
		s.lifecycleMu.Unlock()
		return
	}
	s.retired = true
	closeNow := s.readers == 0
	s.lifecycleMu.Unlock()
	if closeNow {
		s.closeOwnedEngine()
	}
}

func (s *vaultCorpusSnapshot) closeOwnedEngine() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		if s.baseRelease != nil {
			s.baseRelease()
		}
		if s.ownsCoreEngine && s.coreEngine != nil {
			_ = s.coreEngine.Close()
		}
	})
}

func (v *Vault) installPublishedSnapshot(snap *vaultCorpusSnapshot, slot string) bool {
	if v == nil || v.slotCacheMu == nil || v.snapRef == nil || snap == nil {
		return false
	}
	v.slotCacheMu.Lock()
	if v.snapRef.runtimeClosed {
		v.slotCacheMu.Unlock()
		return false
	}
	previous := v.snapRef.publishedSnap
	v.snapRef.publishedSnap = snap
	v.snapRef.publishedSlot = slot
	v.snapRef.lastSnapTime = time.Now()
	v.slotCacheMu.Unlock()
	if previous != nil && previous != snap {
		previous.retire()
	}
	return true
}

func (v *Vault) acquireCorpusSnapshot(ctx context.Context) (*vaultCorpusSnapshot, func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		snap, err := v.loadCorpusSnapshot(ctx)
		if err != nil || snap == nil {
			return snap, nil, err
		}
		if release, ok := snap.acquireReader(); ok {
			return snap, release, nil
		}
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
	}
}

func (v *Vault) acquireCachedCorpusSnapshot() (*vaultCorpusSnapshot, func(), bool) {
	if v == nil || v.slotCacheMu == nil || v.snapRef == nil {
		return nil, nil, false
	}
	v.slotCacheMu.RLock()
	if v.snapRef.runtimeClosed {
		v.slotCacheMu.RUnlock()
		return nil, nil, false
	}
	snap := v.snapRef.snap
	if snap == nil {
		v.slotCacheMu.RUnlock()
		return nil, nil, false
	}
	release, ok := snap.acquireReader()
	v.slotCacheMu.RUnlock()
	if !ok {
		return nil, nil, false
	}
	return snap, release, true
}

// PublishedSlot reports the generation name CURRENT points at, without opening
// or loading anything. It is the cheap change probe a background warmer uses.
func (v *Vault) PublishedSlot() (string, bool) {
	if v == nil || v.db == nil {
		return "", false
	}
	dbPath := v.mainDBPath()
	if strings.TrimSpace(dbPath) == "" {
		return "", false
	}
	_, _, _, _, slot, ok := resolvePublishedSnapshotPaths(dbPath)
	return slot, ok
}

// PublishedGenerationResident reports whether CURRENT's generation is already
// opened and mapped in this process, i.e. a query would not pay the open cost.
func (v *Vault) PublishedGenerationResident() bool {
	if v == nil || v.slotCacheMu == nil || v.snapRef == nil {
		return false
	}
	slot, ok := v.PublishedSlot()
	if !ok {
		return false
	}
	v.slotCacheMu.RLock()
	defer v.slotCacheMu.RUnlock()
	snap := v.snapRef.publishedSnap
	return v.snapRef.publishedSlot == slot && snap != nil && snap.coreEngine != nil && snap.coreEngine.HasMmapSegment()
}

// WarmPublishedGeneration opens the current published generation ahead of the
// first query. Opening a .zcsr segment costs hundreds of milliseconds on a
// large corpus, so paying it on the query path turns every first recall after a
// publication into a slow request. changed reports whether this call opened a
// generation that was not already the resident one.
func (v *Vault) WarmPublishedGeneration(ctx context.Context) (slot string, changed bool, err error) {
	slot, ok := v.PublishedSlot()
	if !ok {
		return "", false, nil
	}
	if v.slotCacheMu != nil && v.snapRef != nil {
		v.slotCacheMu.RLock()
		resident := v.snapRef.publishedSlot == slot &&
			v.snapRef.publishedSnap != nil &&
			v.snapRef.publishedSnap.coreEngine != nil
		v.slotCacheMu.RUnlock()
		if resident {
			return slot, false, nil
		}
	}
	snap, release, err := v.acquireCorpusSnapshot(ctx)
	if err != nil {
		return slot, false, err
	}
	if snap == nil {
		if release != nil {
			release()
		}
		return slot, false, nil
	}
	defer release()
	if len(snap.docs) == 0 {
		return slot, false, nil
	}
	if eng := snap.ensureSnapshotCoreEngine(v); eng == nil {
		return slot, false, nil
	}
	return slot, true, nil
}

// CloseRuntimeSnapshots retires the published mmap generation owned by the
// snapshot-only reader. Existing leases release it after their final read; a
// loader already in flight cannot install another generation after this call.
func (v *Vault) CloseRuntimeSnapshots() {
	if v == nil || v.slotCacheMu == nil || v.snapRef == nil {
		return
	}
	v.snapRef.buildMu.Lock()
	v.slotCacheMu.Lock()
	v.snapRef.runtimeClosed = true
	published := v.snapRef.publishedSnap
	v.snapRef.publishedSnap = nil
	v.snapRef.publishedSlot = ""
	cached := v.replaceCachedSnapshotLocked(nil)
	v.slotCacheMu.Unlock()
	v.snapRef.buildMu.Unlock()
	published.retire()
	if cached != nil {
		cached.retire()
	}
	v.dropCachedCSREngine()
}

// cachedPublishedSnapshot returns the snapshot-only read generation when the
// active slot, codec key and purge epoch all still match.
func (v *Vault) cachedPublishedSnapshot(slot, codecKey string, purgeEpoch int64) (*vaultCorpusSnapshot, bool) {
	if v == nil || v.slotCacheMu == nil || v.snapRef == nil {
		return nil, false
	}
	v.slotCacheMu.RLock()
	snap := v.snapRef.publishedSnap
	slotOK := v.snapRef.publishedSlot == slot
	v.slotCacheMu.RUnlock()
	if snap != nil && slotOK && snap.episodicCodecKey == codecKey && snap.purgeEpoch == purgeEpoch {
		return snap, true
	}
	return nil, false
}

// loadPublishedSnapshot serves the active published generation and nothing
// newer. It is the snapshot-only recall path: writes stay in the change log
// until the background builder publishes the next slot, so a query never pays
// the delta-overlay cost and never triggers an in-process build.
//
// The docs sidecar is the generation's own metadata view. When it is missing or
// belongs to another signature, the live metadata view is paired with the
// published index instead; hits map by unit ID, so the index remains the
// authority for what is searchable.
func (v *Vault) loadPublishedSnapshot(ctx context.Context) (*vaultCorpusSnapshot, bool) {
	if v == nil || v.db == nil {
		return nil, false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	dbPath := v.mainDBPath()
	if strings.TrimSpace(dbPath) == "" {
		return nil, false
	}
	csrPath, genPath, _, docsPath, slot, ok := resolvePublishedSnapshotPaths(dbPath)
	if !ok {
		return nil, false
	}
	codecKey, err := v.episodicCodecKey()
	if err != nil {
		return nil, false
	}
	var livePurge int64
	if err := v.db.QueryRowContext(ctx, `SELECT purge_epoch FROM rm_revision WHERE id = 1`).Scan(&livePurge); err != nil {
		return nil, false
	}
	if snap, ok := v.cachedPublishedSnapshot(slot, string(codecKey), livePurge); ok {
		return snap, true
	}
	if livePurge > 0 {
		built, known := v.snapshotBuiltPurgeEpoch(ctx)
		if !known || built != livePurge {
			return nil, false
		}
	}
	if v.snapRef != nil {
		v.snapRef.buildMu.Lock()
		defer v.snapRef.buildMu.Unlock()
	}
	// A publisher may have advanced CURRENT while this caller waited. Re-resolve
	// under the construction lock so the CSR and docs sidecar come from one slot.
	csrPath, genPath, _, docsPath, slot, ok = resolvePublishedSnapshotPaths(dbPath)
	if !ok {
		return nil, false
	}
	if snap, ok := v.cachedPublishedSnapshot(slot, string(codecKey), livePurge); ok {
		return snap, true
	}
	if v.snapshotGenerationRejected(slot) {
		return nil, false
	}
	rawGen, err := os.ReadFile(genPath)
	if err != nil {
		return nil, false
	}
	genLine := strings.TrimSpace(string(rawGen))
	eng, err := core.OpenDiskClusterEngine(csrPath)
	if err != nil {
		if snapshotOpenFailureIsFormat(err) {
			v.markRejectedSnapshotSlot(slot)
		}
		return nil, false
	}
	if eng == nil || eng.Index == nil || eng.Index.N <= 0 {
		if eng != nil {
			_ = eng.Close()
		}
		return nil, false
	}
	keepEngine := false
	defer func() {
		if !keepEngine {
			_ = eng.Close()
		}
	}()
	eng.MaterializeClusterState()
	docs, header, ok := loadCorpusDocsSidecarAt(docsPath)
	if !ok || len(docs) == 0 || header.Count != len(docs) ||
		header.ChangeSeq <= 0 ||
		strings.TrimSpace(header.Signature) != genLine ||
		signatureCodecKey(header.Signature) != string(codecKey) {
		liveDocs, _, docsErr := v.canonicalSnapshotDocs(ctx)
		if docsErr != nil || len(liveDocs) == 0 {
			return nil, false
		}
		docs = liveDocs
		header = corpusDocsSidecarHeader{Signature: genLine, Count: len(liveDocs)}
	}
	state := canonicalCorpusState{
		docCnt:     int64(len(docs)),
		changeSeq:  header.ChangeSeq,
		purgeEpoch: livePurge,
		codecKey:   string(codecKey),
		signature:  genLine,
	}
	snap := canonicalSnapshotFromDocs(state, docs, eng)
	if snap == nil {
		_ = eng.Close()
		return nil, false
	}
	snap.changeSeq = header.ChangeSeq
	snap.ownsCoreEngine = true
	if !v.installPublishedSnapshot(snap, slot) {
		return nil, false
	}
	keepEngine = true
	return snap, true
}
