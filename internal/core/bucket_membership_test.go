// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// This file pins the Stage 2a neighborhood representation: in-memory and disk
// states store exact timestamp-bucket membership once, while visitNeighbors
// preserves the logical expanded row sequence without materializing it.
package core

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func bucketMembershipDoc(id string, ts int64, hash uint64) Doc {
	return Doc{
		UnitID:     id,
		OccurredAt: ts,
		Terms: map[uint64]Term{
			hash: {Hash: hash, Bits: 40, Role: RolePrimary, ExtFlags: ExtFlagSeenPrimary, Group: hash, Sent: 1, Slot: 1},
		},
		Counts:    map[uint64]int{hash: 1},
		Positions: map[uint64][]TermPosition{hash: {{Sentence: 0, Slot: 1}}},
	}
}

func transparentBucketDocs() []Doc {
	const base = int64(1700000000)
	const shared = uint64(0xB0C0)
	return []Doc{
		bucketMembershipDoc("transparent-00-seed", base, shared),
		bucketMembershipDoc("transparent-01-orthogonal", base+1, 0xB0C1),
		bucketMembershipDoc("transparent-02-coherent", base+2, shared),
		bucketMembershipDoc("transparent-03-extended", base+3, shared),
		// The distant gap raises meanGap enough for the first four buckets to pass
		// the continuous affinity check, while endogenous gap-splitting keeps the
		// decoy in another episode.
		bucketMembershipDoc("transparent-04-decoy", base+103, 0xB0C2),
	}
}

func collectVisitedNeighbors(st *rmClusterState, u int32) []int32 {
	var out []int32
	st.visitNeighbors(u, func(neighbor int32) bool {
		out = append(out, neighbor)
		return true
	})
	return out
}

// legacyNeighborOracle reproduces the pre-Stage-2a-0B row materialization in
// test code: base buckets are emitted self, -1, +1, -2, +2; then coherent
// buckets made reachable by orthogonal-interleaver transparency are appended
// with first-occurrence deduplication.
func legacyNeighborOracle(st *rmClusterState, idx *Index, u int32) []int32 {
	if st == nil || idx == nil || u < 0 || int(u) >= idx.N || int(u) >= len(st.ordPos) {
		return nil
	}
	bUnits := make([][]int32, len(st.bucketTimes))
	for unit := 0; unit < idx.N && unit < len(st.ordPos); unit++ {
		if bucket := st.ordPos[unit]; bucket >= 0 && int(bucket) < len(bUnits) {
			bUnits[bucket] = append(bUnits[bucket], int32(unit))
		}
	}
	bIdx := int(st.ordPos[u])
	if bIdx < 0 || bIdx >= len(bUnits) {
		return nil
	}

	const neighborSpan = 2
	const orthoFloor = transmissionPerHop * transmissionPerHop
	subFloor := 1.0 - transmissionPerHop
	seen := make(map[int32]bool)
	var out []int32
	addUnit := func(v int32) {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	for _, v := range bUnits[bIdx] {
		addUnit(v)
	}
	for span := 1; span <= neighborSpan; span++ {
		for _, adj := range [2]int{bIdx - span, bIdx + span} {
			if adj >= 0 && adj < len(bUnits) {
				for _, v := range bUnits[adj] {
					addUnit(v)
				}
			}
		}
	}

	epU := st.episodeOf[u]
	if epU < 0 {
		return out
	}
	lookupCos := func(a, b int32) float64 {
		if a == b {
			return 1
		}
		return st.docSubjectCos(idx, nil, a, b)
	}
	for _, dir := range [2]int{-1, 1} {
		coherentCnt := 0
		orthoSkipped := 0
		for step := 1; step <= 2*neighborSpan && coherentCnt < neighborSpan; step++ {
			adj := bIdx + dir*step
			if adj < 0 || adj >= len(bUnits) || len(bUnits[adj]) == 0 {
				break
			}
			v0 := bUnits[adj][0]
			if st.episodeOf[v0] != epU || st.temporalAffinity(idx, u, v0) < subFloor {
				break
			}
			hasCoherent := false
			for _, v := range bUnits[adj] {
				if lookupCos(u, v) >= orthoFloor {
					hasCoherent = true
					break
				}
			}
			if hasCoherent {
				coherentCnt++
				for _, v := range bUnits[adj] {
					addUnit(v)
				}
			} else {
				orthoSkipped++
				if orthoSkipped > neighborSpan {
					break
				}
			}
		}
	}
	return out
}

func assertNeighborSequence(t *testing.T, label string, got, want []int32) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s neighbor count = %d, want %d: got=%v want=%v", label, len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s neighbor[%d] = %d, want %d: got=%v want=%v", label, i, got[i], want[i], got, want)
		}
	}
}

func assertBucketVisitorMatchesLegacyOracle(t *testing.T, eng *Engine) {
	t.Helper()
	st := eng.ensureClusterState()
	if len(st.bucketRowPtr) != len(st.bucketTimes)+1 {
		t.Fatalf("bucketRowPtr len = %d, want %d", len(st.bucketRowPtr), len(st.bucketTimes)+1)
	}
	timed := 0
	for _, ord := range st.ordPos {
		if ord >= 0 {
			timed++
		}
	}
	if len(st.bucketUnits) != timed {
		t.Fatalf("bucketUnits len = %d, want %d timed units", len(st.bucketUnits), timed)
	}
	for u := 0; u < eng.Index.N; u++ {
		got := collectVisitedNeighbors(st, int32(u))
		want := legacyNeighborOracle(st, eng.Index, int32(u))
		assertNeighborSequence(t, fmt.Sprintf("unit %d", u), got, want)
	}
}

func TestBucketMembershipVisitorMatchesLegacyOracle(t *testing.T) {
	const base = int64(1700000000)
	spread := make([]Doc, 7)
	for i := range spread {
		spread[i] = bucketMembershipDoc(fmt.Sprintf("spread-%02d", i), base+int64(i)*17, uint64(0x5100+i))
	}
	cases := []struct {
		name string
		docs []Doc
	}{
		{name: "empty"},
		{name: "single", docs: []Doc{bucketMembershipDoc("single", base, 0x5000)}},
		{name: "same-bucket", docs: []Doc{
			bucketMembershipDoc("same-00", base, 0x5001),
			bucketMembershipDoc("same-01", base, 0x5002),
			bucketMembershipDoc("same-02", base, 0x5003),
		}},
		{name: "spread", docs: spread},
		{name: "untimed", docs: []Doc{
			bucketMembershipDoc("timed-00", base, 0x5004),
			bucketMembershipDoc("untimed-00", 0, 0x5005),
			bucketMembershipDoc("untimed-01", 0, 0x5006),
		}},
		{name: "transparent-extension", docs: transparentBucketDocs()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng := NewEngine(BuildIndex(tc.docs))
			assertBucketVisitorMatchesLegacyOracle(t, eng)
		})
	}

	t.Run("bucket-order", func(t *testing.T) {
		eng := NewEngine(BuildIndex(spread))
		st := eng.ensureClusterState()
		u := eng.Index.UnitOf["spread-03"]
		wantIDs := []string{"spread-03", "spread-02", "spread-04", "spread-01", "spread-05"}
		want := make([]int32, len(wantIDs))
		for i, id := range wantIDs {
			want[i] = eng.Index.UnitOf[id]
		}
		assertNeighborSequence(t, "spread-03", collectVisitedNeighbors(st, u), want)
	})

	t.Run("transparent-order", func(t *testing.T) {
		docs := transparentBucketDocs()
		eng := NewEngine(BuildIndex(docs))
		st := eng.ensureClusterState()
		seed := eng.Index.UnitOf["transparent-00-seed"]
		wantIDs := []string{
			"transparent-00-seed",
			"transparent-01-orthogonal",
			"transparent-02-coherent",
			"transparent-03-extended",
		}
		want := make([]int32, len(wantIDs))
		for i, id := range wantIDs {
			want[i] = eng.Index.UnitOf[id]
		}
		assertNeighborSequence(t, "transparent seed", collectVisitedNeighbors(st, seed), want)
		if len(st.extraNeighborBuckets) == 0 {
			t.Fatal("transparent extension did not record a sparse extra bucket edge")
		}
	})
}

func TestBucketMembershipLateArrivalDeleteAndApplyDocs(t *testing.T) {
	t.Run("late-arrival", func(t *testing.T) {
		const base = int64(1700000000)
		initial := []Doc{
			bucketMembershipDoc("late-00", base, 0x6100),
			bucketMembershipDoc("late-20", base+20, 0x6101),
			bucketMembershipDoc("late-40", base+40, 0x6102),
		}
		late := bucketMembershipDoc("late-10", base+10, 0x6103)
		wantDocs := append(append([]Doc(nil), initial...), late)
		inc := NewEngine(BuildIndex(initial))
		inc.ensureClusterState()
		inc.AppendDocs([]Doc{late})
		full := NewEngine(BuildIndex(wantDocs))
		assertBucketVisitorMatchesLegacyOracle(t, inc)
		if got, want := clusterStateCanonicalDigest(inc.ensureClusterState()), clusterStateCanonicalDigest(full.ensureClusterState()); got != want {
			t.Fatalf("late-arrival cluster digest mismatch:\n inc =%s\n full=%s", got, want)
		}
		assertSameQuery(t, full, inc, digestProbeQuery(wantDocs))
	})

	t.Run("delete", func(t *testing.T) {
		docs := appendProbeDocs(96)
		const victim = "probe-00047"
		kept := make([]Doc, 0, len(docs)-1)
		for _, doc := range docs {
			if doc.UnitID != victim {
				kept = append(kept, doc)
			}
		}
		inc := NewEngine(BuildIndex(docs))
		inc.ensureClusterState()
		if removed := inc.DeleteDocs(victim); removed != 1 {
			t.Fatalf("DeleteDocs removed %d, want 1", removed)
		}
		full := NewEngine(BuildIndex(kept))
		assertBucketVisitorMatchesLegacyOracle(t, inc)
		if got, want := clusterStateCanonicalDigest(inc.ensureClusterState()), clusterStateCanonicalDigest(full.ensureClusterState()); got != want {
			t.Fatalf("delete cluster digest mismatch:\n inc =%s\n full=%s", got, want)
		}
		assertSameQuery(t, full, inc, digestProbeQuery(kept))
	})

	t.Run("apply-docs", func(t *testing.T) {
		docs := appendProbeDocs(300)
		drops, upserts, wantDocs := applyDocsProbeBatch(docs)
		inc := NewEngine(BuildIndex(docs))
		inc.ensureClusterState()
		if removed := inc.ApplyDocs(drops, upserts); removed != 2 {
			t.Fatalf("ApplyDocs removed %d, want 2", removed)
		}
		full := NewEngine(BuildIndex(wantDocs))
		assertBucketVisitorMatchesLegacyOracle(t, inc)
		if got, want := clusterStateCanonicalDigest(inc.ensureClusterState()), clusterStateCanonicalDigest(full.ensureClusterState()); got != want {
			t.Fatalf("ApplyDocs cluster digest mismatch:\n inc =%s\n full=%s", got, want)
		}
		assertSameQuery(t, full, inc, digestProbeQuery(wantDocs))
	})
}

func TestBucketMembershipSingleBucket10KUsesLinearBacking(t *testing.T) {
	const n = 10000
	st := &rmClusterState{
		clusterN:    1,
		bucketTimes: []int64{1700000000},
		ordPos:      make([]int32, n),
		episodeOf:   make([]int32, n),
	}
	for u := range st.episodeOf {
		st.episodeOf[u] = -1
	}
	st.buildBucketMembership(n)
	st.buildOrthogonalTransparentExtras(&Index{N: n})

	physicalEntries := len(st.bucketRowPtr) + len(st.bucketUnits) + len(st.extraNeighborRowPtr) + len(st.extraNeighborBuckets)
	wantPhysical := 2*n + 3
	if physicalEntries != wantPhysical {
		t.Fatalf("physical neighbor backing entries = %d, want %d (= 2N+3)", physicalEntries, wantPhysical)
	}
	legacyEntries := int64(n)*int64(n) + int64(n+1)
	if int64(physicalEntries) >= legacyEntries {
		t.Fatalf("compact backing %d entries is not below legacy %d entries", physicalEntries, legacyEntries)
	}
	got := collectVisitedNeighbors(st, n/2)
	if len(got) != n {
		t.Fatalf("single-bucket logical row len = %d, want %d", len(got), n)
	}
	for i, unit := range got {
		if unit != int32(i) {
			t.Fatalf("single-bucket logical row[%d] = %d, want %d", i, unit, i)
		}
	}
	t.Logf("single bucket N=%d: physical backing=%d int32/uint32 entries; legacy=%d entries", n, physicalEntries, legacyEntries)
}

func TestBucketMembershipDiskV7PersistsCompactBacking(t *testing.T) {
	docs := transparentBucketDocs()
	memory := NewEngine(BuildIndex(docs))
	memoryState := memory.ensureClusterState()
	assertBucketVisitorMatchesLegacyOracle(t, memory)

	path := filepath.Join(t.TempDir(), "bucket-membership-v7.zcsr")
	if err := WriteDiskClusterCSR(path, memory); err != nil {
		t.Fatalf("WriteDiskClusterCSR: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if zcsrVersion != 8 {
		t.Fatalf("zcsrVersion = %d, want 8", zcsrVersion)
	}
	if got := binary.LittleEndian.Uint16(data[4:6]); got != zcsrVersion {
		t.Fatalf("disk version = %d, want %d", got, zcsrVersion)
	}
	disk, err := OpenDiskClusterEngine(path)
	if err != nil {
		t.Fatalf("OpenDiskClusterEngine: %v", err)
	}
	defer disk.Close()
	diskState := disk.clusterState
	if len(diskState.bucketRowPtr) != len(memoryState.bucketRowPtr) ||
		len(diskState.bucketUnits) != len(memoryState.bucketUnits) ||
		len(diskState.extraNeighborRowPtr) != len(memoryState.extraNeighborRowPtr) ||
		len(diskState.extraNeighborBuckets) != len(memoryState.extraNeighborBuckets) {
		t.Fatalf("v7 compact backing lengths differ: disk=(%d,%d,%d,%d) memory=(%d,%d,%d,%d)",
			len(diskState.bucketRowPtr), len(diskState.bucketUnits), len(diskState.extraNeighborRowPtr), len(diskState.extraNeighborBuckets),
			len(memoryState.bucketRowPtr), len(memoryState.bucketUnits), len(memoryState.extraNeighborRowPtr), len(memoryState.extraNeighborBuckets))
	}
	for u := 0; u < memory.Index.N; u++ {
		assertNeighborSequence(t, fmt.Sprintf("disk unit %d", u), collectVisitedNeighbors(diskState, int32(u)), collectVisitedNeighbors(memoryState, int32(u)))
	}
	if got, want := clusterStateCanonicalDigest(diskState), clusterStateCanonicalDigest(memoryState); got != want {
		t.Fatalf("disk v7 logical cluster digest mismatch:\n disk  =%s\n memory=%s", got, want)
	}
	assertSameQuery(t, memory, disk, digestProbeQuery(docs))
}
