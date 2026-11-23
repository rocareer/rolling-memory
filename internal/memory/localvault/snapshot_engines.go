// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package localvault

import (
	core "github.com/rocareer/rolling-memory/internal/core"
)

// Baseline-engine ownership
//
// The .zcsr baseline engine is shared between the CSR cache and every corpus
// snapshot projected from it. Replacing the cache entry or dropping the cached
// snapshot therefore cannot unmap the previous engine: an in-flight recall may
// still hold a snapshot that reads the same mapping. Every owner retains the
// engine and releases it when it stops using it; the last release unmaps it.
//
// Published generations are handled separately by vaultCorpusSnapshot's
// ownsCoreEngine flag because they open an engine that no cache owns.

// runtimeClosed reports whether the owning Memory has already retired this
// runtime's snapshots. Late loaders must not install another baseline.
func (v *Vault) runtimeClosed() bool {
	if v == nil || v.slotCacheMu == nil || v.snapRef == nil {
		return false
	}
	v.slotCacheMu.RLock()
	closed := v.snapRef.runtimeClosed
	v.slotCacheMu.RUnlock()
	return closed
}

// retainSnapshotEngineLocked records one owner for eng. Caller holds slotCacheMu.
func (v *Vault) retainSnapshotEngineLocked(eng *core.Engine) {
	if v == nil || eng == nil || v.snapRef == nil {
		return
	}
	if v.snapRef.engineRefs == nil {
		v.snapRef.engineRefs = make(map[*core.Engine]int, 4)
	}
	v.snapRef.engineRefs[eng]++
}

// releaseSnapshotEngineLocked drops one owner for eng and reports whether the
// last owner left. Caller holds slotCacheMu.
func (v *Vault) releaseSnapshotEngineLocked(eng *core.Engine) bool {
	if v == nil || eng == nil || v.snapRef == nil || v.snapRef.engineRefs == nil {
		return false
	}
	refs, tracked := v.snapRef.engineRefs[eng]
	if !tracked || refs <= 0 {
		return false
	}
	if refs == 1 {
		delete(v.snapRef.engineRefs, eng)
		return true
	}
	v.snapRef.engineRefs[eng] = refs - 1
	return false
}

// retainSnapshotEngine records one owner for eng.
func (v *Vault) retainSnapshotEngine(eng *core.Engine) {
	if v == nil || eng == nil || v.slotCacheMu == nil {
		return
	}
	v.slotCacheMu.Lock()
	v.retainSnapshotEngineLocked(eng)
	v.slotCacheMu.Unlock()
}

// holdSnapshotEngine retains one temporary owner and returns its release. It
// bridges the gap between opening/reusing a baseline and attaching it to a
// snapshot, so a concurrent cache drop cannot unmap the engine mid-build.
func (v *Vault) holdSnapshotEngine(eng *core.Engine) func() {
	if v == nil || eng == nil {
		return func() {}
	}
	v.retainSnapshotEngine(eng)
	return func() { v.releaseSnapshotEngine(eng) }
}

// releaseSnapshotEngine drops one owner for eng and unmaps it when the last
// owner leaves. The unmap runs outside slotCacheMu.
func (v *Vault) releaseSnapshotEngine(eng *core.Engine) {
	if v == nil || eng == nil || v.slotCacheMu == nil {
		return
	}
	v.slotCacheMu.Lock()
	closeNow := v.releaseSnapshotEngineLocked(eng)
	v.slotCacheMu.Unlock()
	if closeNow {
		_ = eng.Close()
	}
}

// attachSnapshotEngine ties a baseline engine's lifetime to one snapshot: the
// engine stays mapped until the snapshot is retired and its readers return.
func (v *Vault) attachSnapshotEngine(s *vaultCorpusSnapshot, eng *core.Engine) {
	if s == nil || eng == nil || s.baseRelease != nil {
		return
	}
	v.retainSnapshotEngine(eng)
	s.baseEngine = eng
	s.baseRelease = func() { v.releaseSnapshotEngine(eng) }
}

// swapCachedCSREngine installs eng as the shared baseline handle and releases
// the previous cache owner. The previous engine stays mapped while any snapshot
// still leases it.
func (v *Vault) swapCachedCSREngine(eng *core.Engine, csrPath string, mtimeNS, size int64) {
	if v == nil || v.slotCacheMu == nil || v.snapRef == nil || eng == nil {
		return
	}
	v.slotCacheMu.Lock()
	if v.snapRef.runtimeClosed {
		v.slotCacheMu.Unlock()
		return
	}
	previous := v.snapRef.csrEngine
	if previous != eng {
		v.retainSnapshotEngineLocked(eng)
	}
	v.snapRef.csrEngine = eng
	v.snapRef.csrFileMTimeNS = mtimeNS
	v.snapRef.csrFileSize = size
	v.snapRef.csrFilePath = csrPath
	v.slotCacheMu.Unlock()
	if previous != nil && previous != eng {
		v.releaseSnapshotEngine(previous)
	}
}

// dropCachedCSREngine clears the shared baseline handle. The engine unmaps when
// the last snapshot lease returns.
func (v *Vault) dropCachedCSREngine() {
	if v == nil || v.slotCacheMu == nil || v.snapRef == nil {
		return
	}
	v.slotCacheMu.Lock()
	previous := v.snapRef.csrEngine
	v.snapRef.csrEngine = nil
	v.snapRef.csrFileMTimeNS = 0
	v.snapRef.csrFileSize = 0
	v.snapRef.csrFilePath = ""
	v.slotCacheMu.Unlock()
	if previous != nil {
		v.releaseSnapshotEngine(previous)
	}
}

// replaceCachedSnapshotLocked is the in-lock install form; the caller retires
// the returned snapshot after releasing slotCacheMu.
func (v *Vault) replaceCachedSnapshotLocked(snap *vaultCorpusSnapshot) *vaultCorpusSnapshot {
	if v == nil || v.snapRef == nil {
		return nil
	}
	previous := v.snapRef.snap
	v.snapRef.snap = snap
	if previous == snap {
		return nil
	}
	return previous
}
