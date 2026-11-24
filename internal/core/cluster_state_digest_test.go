// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// 本件是增量摄取架构批（P2b/P3b）的等价器材：为 rmClusterState 提供固定顺序的
// canonical digest，并钉住 append/delete 后与全量重建逐字段一致。digest 只覆盖
// 派生状态内容，不覆盖 builtIndex/builtIndexGen 生命周期字段。
package core

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math"
	"path/filepath"
	"sort"
	"testing"
)

type digestWriter struct {
	h [32]byte
	b [8]byte
}

func (d *digestWriter) u64(v uint64) {
	binary.LittleEndian.PutUint64(d.b[:], v)
	d.write(d.b[:])
}
func (d *digestWriter) i64(v int64)   { d.u64(uint64(v)) }
func (d *digestWriter) u32(v uint32)  { d.u64(uint64(v)) }
func (d *digestWriter) i32(v int32)   { d.u64(uint64(uint32(v))) }
func (d *digestWriter) u16(v uint16)  { d.u64(uint64(v)) }
func (d *digestWriter) u8(v uint8)    { d.u64(uint64(v)) }
func (d *digestWriter) f64(v float64) { d.u64(math.Float64bits(v)) }
func (d *digestWriter) boolean(v bool) {
	var b uint8
	if v {
		b = 1
	}
	d.u8(b)
}
func (d *digestWriter) write(p []byte) {
	next := sha256.Sum256(append(d.h[:], p...))
	d.h = next
}
func (d *digestWriter) tag(s string) {
	d.u64(uint64(len(s)))
	d.write([]byte(s))
}

func (d *digestWriter) u32s(s []uint32) {
	d.u64(uint64(len(s)))
	for _, v := range s {
		d.u32(v)
	}
}
func (d *digestWriter) u8s(s []uint8) {
	d.u64(uint64(len(s)))
	for _, v := range s {
		d.u8(v)
	}
}
func (d *digestWriter) i32s(s []int32) {
	d.u64(uint64(len(s)))
	for _, v := range s {
		d.i32(v)
	}
}
func (d *digestWriter) u64s(s []uint64) {
	d.u64(uint64(len(s)))
	for _, v := range s {
		d.u64(v)
	}
}
func (d *digestWriter) i64s(s []int64) {
	d.u64(uint64(len(s)))
	for _, v := range s {
		d.i64(v)
	}
}
func (d *digestWriter) f64s(s []float64) {
	d.u64(uint64(len(s)))
	for _, v := range s {
		d.f64(v)
	}
}

func (d *digestWriter) entries(s []rmDocEntry) {
	d.u64(uint64(len(s)))
	for _, e := range s {
		d.u64(e.id)
		d.u64(e.group)
		d.u32(e.posOff)
		d.u32(e.posLen)
		d.i32(e.tf)
		d.u16(e.bits)
		d.u8(e.flags)
		d.u8(e.roleExt)
	}
}
func (d *digestWriter) postings(s []rmPostingEntry) {
	d.u64(uint64(len(s)))
	for _, p := range s {
		d.i32(p.unit)
		d.i32(p.entry)
	}
}
func (d *digestWriter) dyads(s []rmDyad) {
	d.u64(uint64(len(s)))
	for _, p := range s {
		d.u64(p.a)
		d.u64(p.b)
	}
}
func (d *digestWriter) fastDocs(s []rmFastDocStruct) {
	d.u64(uint64(len(s)))
	for _, f := range s {
		d.f64(f.docTotal)
		d.i64(f.day)
		d.f64(f.subjNormSq)
	}
}
func (d *digestWriter) positionsMap(m map[uint32]TermPosition) {
	keys := make([]uint32, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	d.u64(uint64(len(keys)))
	for _, k := range keys {
		p := m[k]
		d.u32(k)
		d.u64(uint64(p.Sentence))
		d.u64(uint64(p.Slot))
	}
}

// clusterStateCanonicalDigest hashes every content-bearing rmClusterState field
// in a fixed order (maps by ascending key, floats by Float64bits). Lifecycle
// fields (builtIndex/builtIndexGen) are excluded: two engines built by
// different paths must agree on state content even when their generation
// counters differ.
// logicalPhraseView returns the phrase CSR a full rebuild would produce: the immutable
// base merged with the append delta. The digest hashes this logical view, so an
// incremental base+delta state and a full rebuild stay comparable.
func logicalPhraseView(st *rmClusterState) ([]rmDyad, []uint32, []int32) {
	if len(st.phraseDelta) == 0 {
		return st.phraseKeys, st.phraseRowPtr, st.phraseUnits
	}
	postings := make([]dyadPosting, 0, len(st.phraseUnits)+len(st.phraseDelta))
	for i, k := range st.phraseKeys {
		for _, u := range st.phraseUnits[st.phraseRowPtr[i]:st.phraseRowPtr[i+1]] {
			postings = append(postings, dyadPosting{pair: k, unit: u})
		}
	}
	postings = append(postings, st.phraseDelta...)
	sort.Sort(dyadPostingSort(postings))
	keys := make([]rmDyad, 0, len(st.phraseKeys)+len(st.phraseDelta))
	rowPtr := make([]uint32, 0, len(st.phraseKeys)+len(st.phraseDelta)+1)
	units := make([]int32, 0, len(postings))
	for i, p := range postings {
		if i > 0 && p == postings[i-1] {
			continue
		}
		if len(keys) == 0 || keys[len(keys)-1] != p.pair {
			keys = append(keys, p.pair)
			rowPtr = append(rowPtr, uint32(len(units)))
		}
		units = append(units, p.unit)
	}
	rowPtr = append(rowPtr, uint32(len(units)))
	return keys, rowPtr, units
}

// phrasePrefixOf derives the 4,096-bucket prefix table from a phrase key sequence.
func phrasePrefixOf(keys []rmDyad) [4097]uint32 {
	var prefix [4097]uint32
	cur := uint32(0)
	for b := 0; b < 4096; b++ {
		prefix[b] = cur
		for cur < uint32(len(keys)) && int(keys[cur].a>>52) == b {
			cur++
		}
	}
	prefix[4096] = cur
	return prefix
}

func clusterStateCanonicalDigest(st *rmClusterState) string {
	if st == nil {
		return "nil"
	}
	var d digestWriter
	d.tag("cluster-state-v1")
	d.tag("docRowPtr")
	d.u32s(st.docRowPtr)
	d.tag("docEntriesFlat")
	d.entries(st.docEntriesFlat)
	d.tag("posPool")
	d.u32s(st.posPool)

	d.tag("postings")
	keys := make([]uint64, 0, len(st.postings))
	for k := range st.postings {
		keys = append(keys, k)
	}
	sortUint64Asc(keys)
	d.u64(uint64(len(keys)))
	for _, k := range keys {
		d.u64(k)
		d.postings(st.postings[k])
	}

	d.tag("termKeys")
	d.u64s(st.termKeys)
	d.tag("termPrefixPtr")
	d.u32s(st.termPrefixPtr[:])
	d.tag("termRowPtr")
	d.u32s(st.termRowPtr)
	d.tag("postingPool")
	d.postings(st.postingPool)
	d.tag("termEffWeight")
	d.f64s(st.termEffWeight)
	d.tag("termProperRatio")
	d.f64s(st.termProperRatio)
	d.tag("termParentGroup")
	d.u64s(st.termParentGroup)
	d.tag("termIsPrimary")
	d.u8s(st.termIsPrimary)
	phraseKeys, phraseRowPtr, phraseUnits := logicalPhraseView(st)
	phrasePrefix := phrasePrefixOf(phraseKeys)
	d.tag("phraseKeys")
	d.dyads(phraseKeys)
	d.tag("phrasePrefixPtr")
	d.u32s(phrasePrefix[:])
	d.tag("phraseRowPtr")
	d.u32s(phraseRowPtr)
	d.tag("phraseUnits")
	d.i32s(phraseUnits)

	d.tag("effWeight")
	keys = keys[:0]
	for k := range st.effWeight {
		keys = append(keys, k)
	}
	sortUint64Asc(keys)
	d.u64(uint64(len(keys)))
	for _, k := range keys {
		d.u64(k)
		d.f64(st.effWeight[k])
	}
	d.tag("properRatio")
	keys = keys[:0]
	for k := range st.properRatio {
		keys = append(keys, k)
	}
	sortUint64Asc(keys)
	d.u64(uint64(len(keys)))
	for _, k := range keys {
		d.u64(k)
		d.f64(st.properRatio[k])
	}
	d.tag("parentGroup")
	// 族根只有 canonicalParent 一个来源；按索引词表遍历并经 parentGroupOf 取值，
	// 使内存态与磁盘态（该映射可能来自 termParentGroup 列）摘要一致。
	keys = keys[:0]
	for k := range st.termKeys {
		keys = append(keys, st.termKeys[k])
	}
	sortUint64Asc(keys)
	d.u64(uint64(len(keys)))
	for _, k := range keys {
		d.u64(k)
		if g, ok := st.parentGroupOf(k); ok {
			d.u64(g)
		} else {
			d.u64(0)
		}
	}
	d.tag("isPrimaryWord")
	keys = keys[:0]
	for k := range st.isPrimaryWord {
		keys = append(keys, k)
	}
	sortUint64Asc(keys)
	d.u64(uint64(len(keys)))
	for _, k := range keys {
		d.u64(k)
		d.boolean(st.isPrimaryWord[k])
	}

	d.tag("clusters")
	d.i32s(st.clusters)
	d.tag("clusterN")
	d.i64(int64(st.clusterN))
	d.tag("bucketTimes")
	d.i64s(st.bucketTimes)
	d.tag("neighborRowPtr")
	d.u64(uint64(len(st.clusters) + 1))
	var neighborCount uint32
	d.u32(0)
	for u := range st.clusters {
		st.visitNeighbors(int32(u), func(neighbor int32) bool {
			neighborCount++
			return true
		})
		d.u32(neighborCount)
	}
	d.tag("neighborPool")
	d.u64(uint64(neighborCount))
	for u := range st.clusters {
		st.visitNeighbors(int32(u), func(neighbor int32) bool {
			d.i32(neighbor)
			return true
		})
	}
	d.tag("refTimes")
	d.i64s(st.refTimes)
	d.tag("gaps")
	d.f64(st.meanGap)
	d.f64(st.muLogGap)
	d.f64(st.sigLogGap)
	d.tag("ordPos")
	d.i32s(st.ordPos)
	d.tag("episodeOf")
	d.i32s(st.episodeOf)
	d.tag("episodeCount")
	d.i64(int64(st.episodeCount))
	d.tag("scalars")
	d.f64(st.entryMeanBits)
	d.f64(st.sentenceEnergyMean)
	d.f64(st.totalDaysM)
	d.tag("fastDocs")
	d.fastDocs(st.fastDocs)
	d.tag("subjRowPtr")
	d.u32s(st.subjRowPtr)
	d.tag("subjIDsPool")
	d.u64s(st.subjIDsPool)
	d.tag("subjWeightsPool")
	d.f64s(st.subjWeightsPool)

	d.tag("dayCos")
	keys = keys[:0]
	for k := range st.dayCos {
		keys = append(keys, k)
	}
	sortUint64Asc(keys)
	d.u64(uint64(len(keys)))
	for _, k := range keys {
		d.u64(k)
		d.f64(st.dayCos[k])
	}
	d.tag("dayCosRowPtr")
	d.u32s(st.dayCosRowPtr)
	d.tag("dayCosKeys")
	d.u64s(st.dayCosKeys)
	d.tag("dayCosVals")
	d.f64s(st.dayCosVals)

	d.tag("provKeys")
	d.u64s(st.provKeys)
	d.tag("provRowPtr")
	d.u32s(st.provRowPtr)
	d.tag("provUnits")
	d.i32s(st.provUnits)
	d.tag("citeRowPtr")
	d.u32s(st.citeRowPtr)
	d.tag("citePeers")
	d.i32s(st.citePeers)
	d.tag("cociteRowPtr")
	d.u32s(st.cociteRowPtr)
	d.tag("cocitePeers")
	d.i32s(st.cocitePeers)

	d.tag("authorDF")
	keys = keys[:0]
	for k := range st.authorDF {
		keys = append(keys, k)
	}
	sortUint64Asc(keys)
	d.u64(uint64(len(keys)))
	for _, k := range keys {
		d.u64(k)
		d.i32(st.authorDF[k])
	}
	d.tag("meanLineSpan")
	d.f64(st.meanLineSpan)
	d.tag("widePos")
	d.positionsMap(st.widePos)

	return hex.EncodeToString(d.h[:])
}

func assertSameQuery(t *testing.T, want, got *Engine, q map[uint64]Term) {
	t.Helper()
	wh := want.Search(q, 50)
	gh := got.Search(q, 50)
	if len(wh) != len(gh) {
		t.Fatalf("query hit count = %d, want %d", len(gh), len(wh))
	}
	for i := range wh {
		if wh[i].UnitID != gh[i].UnitID ||
			math.Float64bits(wh[i].Energy) != math.Float64bits(gh[i].Energy) ||
			math.Float64bits(wh[i].Direct) != math.Float64bits(gh[i].Direct) ||
			math.Float64bits(wh[i].Hop1) != math.Float64bits(gh[i].Hop1) ||
			math.Float64bits(wh[i].Hop2) != math.Float64bits(gh[i].Hop2) {
			t.Fatalf("hit %d mismatch: want %+v got %+v", i, wh[i], gh[i])
		}
	}
}

func digestProbeQuery(docs []Doc) map[uint64]Term {
	q := make(map[uint64]Term)
	for _, d := range docs {
		for h, term := range d.Terms {
			if len(q) >= 3 {
				return q
			}
			q[h] = term
		}
	}
	return q
}

func queryHitsCanonicalDigest(hits []Hit) string {
	var d digestWriter
	d.tag("query-hits-v1")
	d.u64(uint64(len(hits)))
	for _, hit := range hits {
		d.tag(hit.UnitID)
		d.f64(hit.Energy)
		d.f64(hit.Direct)
		d.f64(hit.Hop1)
		d.f64(hit.Hop2)
	}
	return hex.EncodeToString(d.h[:])
}

func TestVisitNeighborsPreservesOrderAndShortCircuits(t *testing.T) {
	st := &rmClusterState{
		bucketTimes:          []int64{10, 20, 30, 40},
		bucketRowPtr:         []uint32{0, 2, 3, 4, 6},
		bucketUnits:          []int32{9, 3, 8, 2, 7, 6},
		extraNeighborRowPtr:  []uint32{0, 1},
		extraNeighborBuckets: []int32{3},
		ordPos:               []int32{0},
	}

	var got []int32
	st.visitNeighbors(0, func(neighbor int32) bool {
		got = append(got, neighbor)
		return true
	})
	want := []int32{9, 3, 8, 2, 7, 6}
	if len(got) != len(want) {
		t.Fatalf("visited %d neighbors, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("neighbor %d = %d, want %d; order=%v", i, got[i], want[i], got)
		}
	}

	got = got[:0]
	st.visitNeighbors(0, func(neighbor int32) bool {
		got = append(got, neighbor)
		return neighbor != 3
	})
	want = []int32{9, 3}
	if len(got) != len(want) {
		t.Fatalf("short-circuit visited %d neighbors, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("short-circuit neighbor %d = %d, want %d; order=%v", i, got[i], want[i], got)
		}
	}
}

func TestNeighborVisitorSeamGoldenParity(t *testing.T) {
	docs := appendProbeDocs(64)
	eng := NewEngine(BuildIndex(docs))
	// The dual-order partner index was removed from the state and from this digest's
	// composition (it is a derived permutation of phraseKeys and served only the
	// diagnostic TermEnergyVector profile); phraseKeys/Units still cover the logical state.
	//
	// Re-pin 2026-11-23: the log-gap moments moved to the Welford recurrence
	// (naive sum/n differed by 3 ULP on this constant-3600s fixture: mu bits
	// ...cc9d -> ...cc9a). The digest hashes raw muLogGap bits, so it moves with
	// the estimator even though the episode partition (1 component, 64 units)
	// and the query hit digest below are unchanged; Welford also removes the
	// ULP-decided episode split observed on constant-interval corpora.
	//
	// Re-pin 2026-11-24: the digest's family-root column now hashes the single
	// canonical family root instead of the deleted min-hash map. The min-hash
	// label was never used for scoring (canonicalParent already won in
	// parentGroupOf), so the ranking surface is unchanged; the full-corpus paired
	// baseline and the multi-key floor are the guards for that, and this constant
	// only tracks the digest composition change.
	const wantClusterState = "e9d77cfbb97b543e633b32d30e403894f368e518a94098e8be1c23818ce90586"
	if got := clusterStateCanonicalDigest(eng.ensureClusterState()); got != wantClusterState {
		t.Fatalf("cluster state digest = %s, want %s", got, wantClusterState)
	}

	q := make(map[uint64]Term, 3)
	for _, h := range []uint64{1000, 1013, 1026} {
		q[h] = docs[0].Terms[h]
	}
	const wantQueryHits = "f5f3b497577e357a5bcf4877802f31e70d5b4d9e0795626aa66fb60f41d530b1"
	if got := queryHitsCanonicalDigest(eng.Search(q, 50)); got != wantQueryHits {
		t.Fatalf("query hit digest = %s, want %s", got, wantQueryHits)
	}
}

func TestClusterStateDigestAppendEqualsFullRebuild(t *testing.T) {
	docs := appendProbeDocs(300)
	split := 180
	q := digestProbeQuery(docs)

	full := NewEngine(BuildIndex(docs))
	full.ensureClusterState()
	inc := NewEngine(BuildIndex(docs[:split]))
	inc.ensureClusterState()
	inc.AppendDocs(docs[split:])

	want := clusterStateCanonicalDigest(full.ensureClusterState())
	got := clusterStateCanonicalDigest(inc.ensureClusterState())
	if want != got {
		t.Fatalf("append cluster state digest mismatch:\n full=%s\n inc =%s", want, got)
	}
	if a, b := ComputeCorpusDigest(full.Index), ComputeCorpusDigest(inc.Index); a != b {
		t.Fatalf("append index digest mismatch: full=%x inc=%x", a, b)
	}
	assertSameQuery(t, full, inc, q)

	t.Run("disk", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "append_state.zcsr")
		if err := WriteDiskClusterCSR(path, NewEngine(BuildIndex(docs[:split]))); err != nil {
			t.Fatalf("WriteDiskClusterCSR: %v", err)
		}
		disk, err := OpenDiskClusterEngine(path)
		if err != nil {
			t.Fatalf("OpenDiskClusterEngine: %v", err)
		}
		disk.AppendDocs(docs[split:])
		if got := clusterStateCanonicalDigest(disk.ensureClusterState()); got != want {
			t.Fatalf("disk append digest mismatch:\n full=%s\n disk=%s", want, got)
		}
		assertSameQuery(t, full, disk, q)
	})
}

func TestClusterStateDigestDeleteEqualsFullRebuild(t *testing.T) {
	docs := appendProbeDocs(240)
	const victim = "probe-00119"
	kept := make([]Doc, 0, len(docs)-1)
	for _, d := range docs {
		if d.UnitID != victim {
			kept = append(kept, d)
		}
	}
	full := NewEngine(BuildIndex(kept))
	full.ensureClusterState()
	inc := NewEngine(BuildIndex(docs))
	inc.ensureClusterState()
	if got := inc.DeleteDocs(victim); got != 1 {
		t.Fatalf("DeleteDocs removed %d, want 1", got)
	}
	if want, got := clusterStateCanonicalDigest(full.ensureClusterState()), clusterStateCanonicalDigest(inc.ensureClusterState()); want != got {
		t.Fatalf("delete cluster state digest mismatch:\n kept=%s\n inc =%s", want, got)
	}
	if a, b := ComputeCorpusDigest(full.Index), ComputeCorpusDigest(inc.Index); a != b {
		t.Fatalf("delete index digest mismatch: kept=%x inc=%x", a, b)
	}
	assertSameQuery(t, full, inc, digestProbeQuery(kept))
}

func TestClusterStateDigestExtFlagsAndWidePositions(t *testing.T) {
	mk := func(id string, ts int64) Doc {
		d := epTestDoc(id, ts)
		const h = uint64(0x5EED)
		d.Terms[h] = Term{Hash: h, Bits: 48, Flags: 0x10, Role: RolePrimary, ExtFlags: 0x0108}
		d.Counts[h] = 1
		d.Positions[h] = []TermPosition{{Sentence: 70000, Slot: 70001}, {Sentence: 70000, Slot: 70002}}
		return d
	}
	docs := []Doc{mk("wide-1", 1700000000), mk("wide-2", 1700000010), mk("wide-3", 1700000030)}
	full := NewEngine(BuildIndex(docs))
	full.ensureClusterState()
	inc := NewEngine(BuildIndex(docs[:2]))
	inc.ensureClusterState()
	inc.AppendDocs(docs[2:])
	if want, got := clusterStateCanonicalDigest(full.ensureClusterState()), clusterStateCanonicalDigest(inc.ensureClusterState()); want != got {
		t.Fatalf("extflags/wide append digest mismatch:\n full=%s\n inc =%s", want, got)
	}
	if got := full.Index.UnitExtFlags[full.Index.UnitOf["wide-1"]][0x5EED]; got != 0x0108 {
		t.Fatalf("full ExtFlags = 0x%04x, want 0x0108", got)
	}
}
