// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package core

import (
	"fmt"
	"math"
	"math/rand"
	"runtime"
	"sort"
	"testing"
	"time"
)

// The fixture constants reproduce the reference measurement footprint recorded
// before 2d: N = 11,034 units and 12.5M postings from the 2c append profile §1,
// and the 77,951-term vocabulary from the chunked-cluster-state RFC (G2). The
// derived per-doc count reproduces the observed 12.5M / 11,034 ≈ 1,133
// (doc,term) entries per unit.
const (
	seg2dScaleUnits    = 11034
	seg2dScalePostings = 12_500_000
	seg2dScaleTerms    = 77_951
	seg2dTermsPerDoc   = (seg2dScalePostings + seg2dScaleUnits - 1) / seg2dScaleUnits
	seg2dSeed          = int64(0x2D5EED)
	seg2dHubTerm       = uint64(0xD2D2D2D2)
	seg2dRankBase      = uint64(0x1000)
	seg2dSampleTerms   = 512
	seg2dHottest       = 8
	seg2dDriftBase     = 10000
	// seg2dBlockBudget is the 2d foreground append budget for one block
	// (chunked-cluster-state v1 §七 2d: 11k + 1 append foreground ≤ 5 ms).
	seg2dBlockBudget = 5 * time.Millisecond
	// seg2dPrefixBuckets is the per-block 4,096-bucket prefix table size used
	// by the 2b measurement corpus.
	seg2dPrefixBuckets = 4096
	// seg2dPersistenceK is the fanout the 2b K-sweep measured for persistence.
	seg2dPersistenceK = 11
)

// seg2dMergeK follows the 2b K-sweep (1/2/4/8) extended to the block-count
// powers of two through 32, so the merge cost can be read at both ends of the
// persistence fanout.
var seg2dMergeK = []int{1, 2, 4, 8, 16, 32}

// seg2dBuildB follows the append batches measured by 2b/2c: one document per
// append, then batched appends of 32/256/1024 documents per block.
var seg2dBuildB = []int{1, 32, 256, 1024}

// seg2dZipf draws ranks from the truncated Zipf(s≈1) law P(rank ≤ r) = ln r / ln V.
// The inverse CDF is r = V^u, so the fixture is deterministic for a fixed Rand.
type seg2dZipf struct {
	rng      *rand.Rand
	vocab    int
	logVocab float64
}

func (z seg2dZipf) rank() int {
	r := int(math.Exp(z.rng.Float64() * z.logVocab))
	if r < 1 {
		return 1
	}
	if r > z.vocab {
		return z.vocab
	}
	return r
}

// seg2dCorpus builds the reference-scale fixture. Every unit carries seg2dTermsPerDoc
// distinct terms drawn without replacement with Zipf(s≈1) weights over the
// 77,951-term vocabulary, plus the explicit df=N hub term. Counts and Positions
// are left empty: BuildIndex defaults occurrences to 1 and positions do not
// enter postings, DF, DocMass or DocTerms order, so omitting them keeps the
// fixture near the reference footprint instead of tripling it.
func seg2dCorpus() []Doc {
	rng := rand.New(rand.NewSource(seg2dSeed))
	z := seg2dZipf{rng: rng, vocab: seg2dScaleTerms, logVocab: math.Log(float64(seg2dScaleTerms))}
	docs := make([]Doc, seg2dScaleUnits)
	for i := range docs {
		doc := Doc{
			UnitID:     fmt.Sprintf("unit-%d", i),
			OccurredAt: 1700000000 + int64(i)*60,
			Terms:      make(map[uint64]Term, seg2dTermsPerDoc),
		}
		doc.Terms[seg2dHubTerm] = Term{Hash: seg2dHubTerm, Bits: 40, Role: RolePrimary}
		for len(doc.Terms) < seg2dTermsPerDoc {
			h := seg2dRankBase + uint64(z.rank())
			if _, dup := doc.Terms[h]; dup {
				continue
			}
			doc.Terms[h] = Term{Hash: h, Bits: 40, Role: RolePrimary}
		}
		docs[i] = doc
	}
	return docs
}

type seg2dScaleCounts struct {
	units          int
	distinct       int
	postings       int
	dfEqualsN      int
	docTermEntries int
}

func seg2dCountScale(idx *Index) seg2dScaleCounts {
	counts := seg2dScaleCounts{units: idx.N, distinct: len(idx.DF)}
	for _, df := range idx.DF {
		counts.postings += df
		if df == idx.N {
			counts.dfEqualsN++
		}
	}
	for u := 0; u < idx.N; u++ {
		counts.docTermEntries += len(idx.DocTerms[u])
	}
	return counts
}

type seg2dTermDF struct {
	hash uint64
	df   int
}

// seg2dSampleHashes returns the deterministic term sample: seg2dSampleTerms
// hashes at equal spacing over the sorted term column plus the seg2dHottest
// hottest terms (df desc, hash asc). Terms with a posting population are the
// only candidates, so every sample is traversable through ForEachPostingUnit.
func seg2dSampleHashes(idx *Index) []uint64 {
	keys := idx.TermKeys
	if len(keys) == 0 {
		keys = make([]uint64, 0, len(idx.DF))
		for h := range idx.DF {
			keys = append(keys, h)
		}
		sortUint64Asc(keys)
	}
	picked := make([]uint64, 0, seg2dSampleTerms+seg2dHottest)
	seen := make(map[uint64]struct{}, seg2dSampleTerms+seg2dHottest)
	add := func(h uint64) {
		if _, ok := seen[h]; ok {
			return
		}
		seen[h] = struct{}{}
		picked = append(picked, h)
	}
	if n := len(keys); n <= seg2dSampleTerms {
		for _, h := range keys {
			add(h)
		}
	} else {
		for i := 0; i < seg2dSampleTerms; i++ {
			add(keys[i*n/seg2dSampleTerms])
		}
	}
	byDF := make([]seg2dTermDF, 0, len(keys))
	for _, h := range keys {
		byDF = append(byDF, seg2dTermDF{hash: h, df: idx.TermDF(h)})
	}
	sort.Slice(byDF, func(i, j int) bool {
		if byDF[i].df != byDF[j].df {
			return byDF[i].df > byDF[j].df
		}
		return byDF[i].hash < byDF[j].hash
	})
	for i := 0; i < seg2dHottest && i < len(byDF); i++ {
		add(byDF[i].hash)
	}
	return picked
}

type seg2dTermSample struct {
	hash  uint64
	units []int32
}

func seg2dCollectSamples(idx *Index, hashes []uint64) []seg2dTermSample {
	samples := make([]seg2dTermSample, len(hashes))
	for i, h := range hashes {
		buf := make([]int32, 0, idx.TermDF(h))
		idx.ForEachPostingUnit(h, func(unit int32) {
			buf = append(buf, unit)
		})
		samples[i] = seg2dTermSample{hash: h, units: buf}
	}
	return samples
}

// seg2dBlockBounds splits [0,n) into k contiguous blocks. Every unit belongs to
// exactly one block and the union of the blocks is the whole unit range.
func seg2dBlockBounds(n, k int) [33]int32 {
	var bounds [33]int32
	for i := 0; i <= k; i++ {
		bounds[i] = int32(int64(i) * int64(n) / int64(k))
	}
	return bounds
}

// seg2dLowerBound is a closure-free binary search so the measured traversal
// stays allocation-free.
func seg2dLowerBound(units []int32, x int32) int {
	lo, hi := 0, len(units)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if units[mid] < x {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

type seg2dHeapItem struct {
	unit int32
	list int32
	pos  int32
}

func seg2dHeapLess(a, b seg2dHeapItem) bool {
	if a.unit != b.unit {
		return a.unit < b.unit
	}
	return a.list < b.list
}

// seg2dMergeScratch owns the per-query block lists, the fixed-size merge heap
// and its length. Callers reuse it across terms and K values, so a query-time
// K-way merge needs no per-term allocation.
type seg2dMergeScratch struct {
	lists [32][]int32
	heap  [32]seg2dHeapItem
	hlen  int
}

func (s *seg2dMergeScratch) split(units []int32, bounds *[33]int32, k int) {
	for i := 0; i < k; i++ {
		lo := seg2dLowerBound(units, bounds[i])
		hi := seg2dLowerBound(units, bounds[i+1])
		s.lists[i] = units[lo:hi]
	}
}

func (s *seg2dMergeScratch) push(it seg2dHeapItem) {
	i := s.hlen
	s.heap[i] = it
	s.hlen++
	for i > 0 {
		parent := (i - 1) / 2
		if !seg2dHeapLess(s.heap[i], s.heap[parent]) {
			break
		}
		s.heap[i], s.heap[parent] = s.heap[parent], s.heap[i]
		i = parent
	}
}

func (s *seg2dMergeScratch) pop() seg2dHeapItem {
	top := s.heap[0]
	s.hlen--
	if s.hlen == 0 {
		return top
	}
	s.heap[0] = s.heap[s.hlen]
	i := 0
	for {
		child := 2*i + 1
		if child >= s.hlen {
			break
		}
		if right := child + 1; right < s.hlen && seg2dHeapLess(s.heap[right], s.heap[child]) {
			child = right
		}
		if !seg2dHeapLess(s.heap[child], s.heap[i]) {
			break
		}
		s.heap[i], s.heap[child] = s.heap[child], s.heap[i]
		i = child
	}
	return top
}

// mergeCount walks the K-way ascending merge and returns the number of visited
// units plus their sum. The sum keeps the walk observable without materializing
// an output slice.
func (s *seg2dMergeScratch) mergeCount(k int) (visited int, sum uint64) {
	s.hlen = 0
	for i := 0; i < k; i++ {
		if len(s.lists[i]) > 0 {
			s.push(seg2dHeapItem{unit: s.lists[i][0], list: int32(i), pos: 0})
		}
	}
	for s.hlen > 0 {
		it := s.pop()
		visited++
		sum += uint64(uint32(it.unit))
		it.pos++
		if int(it.pos) < len(s.lists[it.list]) {
			it.unit = s.lists[it.list][it.pos]
			s.push(it)
		}
	}
	return visited, sum
}

// mergeInto is the materializing twin used only by the parity assertions; out
// must have enough capacity so the comparison never allocates.
func (s *seg2dMergeScratch) mergeInto(k int, out []int32) []int32 {
	s.hlen = 0
	for i := 0; i < k; i++ {
		if len(s.lists[i]) > 0 {
			s.push(seg2dHeapItem{unit: s.lists[i][0], list: int32(i), pos: 0})
		}
	}
	for s.hlen > 0 {
		it := s.pop()
		out = append(out, it.unit)
		it.pos++
		if int(it.pos) < len(s.lists[it.list]) {
			it.unit = s.lists[it.list][it.pos]
			s.push(it)
		}
	}
	return out
}

type seg2dBlockEntry struct {
	hash uint64
	unit int32
}

type seg2dBlockEntrySort []seg2dBlockEntry

func (s seg2dBlockEntrySort) Len() int { return len(s) }
func (s seg2dBlockEntrySort) Less(i, j int) bool {
	if s[i].hash != s[j].hash {
		return s[i].hash < s[j].hash
	}
	return s[i].unit < s[j].unit
}
func (s seg2dBlockEntrySort) Swap(i, j int) { s[i], s[j] = s[j], s[i] }

// seg2dTermBlock is the block-local term CSR: sorted keys, a row pointer into
// the unit column, and the 4,096-bucket prefix table the 2b measurement found
// necessary to keep a single-block lookup at the global-CSR cost.
type seg2dTermBlock struct {
	keys      []uint64
	rowPtr    []uint32
	units     []int32
	prefixPtr []uint32
}

// seg2dBuildTermBlock builds one block from the first end-begin documents of
// the index: collect (term,unit), sort by (term,unit), then derive rowPtr and
// the prefix buckets. The call includes collection, sorting and table building,
// which is the work an append would pay for the block.
func seg2dBuildTermBlock(idx *Index, begin, end int) seg2dTermBlock {
	entries := make([]seg2dBlockEntry, 0, (end-begin)*seg2dTermsPerDoc)
	for u := begin; u < end; u++ {
		for _, h := range idx.DocTerms[u] {
			entries = append(entries, seg2dBlockEntry{hash: h, unit: int32(u)})
		}
	}
	sort.Sort(seg2dBlockEntrySort(entries))
	block := seg2dTermBlock{
		keys:      make([]uint64, 0, len(entries)),
		rowPtr:    make([]uint32, 0, len(entries)+1),
		units:     make([]int32, 0, len(entries)),
		prefixPtr: make([]uint32, seg2dPrefixBuckets+1),
	}
	for _, e := range entries {
		if len(block.keys) == 0 || block.keys[len(block.keys)-1] != e.hash {
			block.keys = append(block.keys, e.hash)
			block.rowPtr = append(block.rowPtr, uint32(len(block.units)))
		}
		block.units = append(block.units, e.unit)
	}
	block.rowPtr = append(block.rowPtr, uint32(len(block.units)))
	pi := 0
	for bucket := 0; bucket < seg2dPrefixBuckets; bucket++ {
		for pi < len(block.keys) && int(block.keys[pi]>>52) <= bucket {
			pi++
		}
		block.prefixPtr[bucket+1] = uint32(pi)
	}
	return block
}

type seg2dDocSnapshot struct {
	massBits uint64
	terms    []uint64
}

// TestSeg2DMeasureBlocks is the pre-implementation measurement for 2d blocking:
// can the term inverted list be partitioned into contiguous unit-interval
// blocks with a query-time K-way merge, and can document entries be blocked?
// Every reading is logged with its unit and condition; the only hard assertions
// are the K=1 merge parity and the zero-allocation hub traversal.
func TestSeg2DMeasureBlocks(t *testing.T) {
	if testing.Short() {
		t.Skip("measurement")
	}

	corpus := seg2dCorpus()
	idx := BuildIndex(corpus)

	// 1. Scale recap against the fixture targets.
	counts := seg2dCountScale(idx)
	targetPerDoc := float64(seg2dScalePostings) / float64(seg2dScaleUnits)
	t.Logf("scale: N=%d (target %d, dev %+d) distinctTerms=%d (target %d, dev %+.2f%%) postings=%d (target %d, dev %+.3f%%) meanDF=%.2f entriesPerDoc=%.1f (target %.1f) dfEqualsN=%d docTermsTotal=%d",
		counts.units, seg2dScaleUnits, counts.units-seg2dScaleUnits,
		counts.distinct, seg2dScaleTerms, 100*float64(counts.distinct-seg2dScaleTerms)/float64(seg2dScaleTerms),
		counts.postings, seg2dScalePostings, 100*float64(counts.postings-seg2dScalePostings)/float64(seg2dScalePostings),
		float64(counts.postings)/float64(counts.distinct), float64(counts.postings)/float64(counts.units), targetPerDoc,
		counts.dfEqualsN, counts.docTermEntries)
	if counts.docTermEntries != counts.postings {
		t.Fatalf("DocTerms total %d != postings %d", counts.docTermEntries, counts.postings)
	}
	if got := idx.TermDF(seg2dHubTerm); got != idx.N {
		t.Fatalf("hub term df=%d, want N=%d", got, idx.N)
	}

	// 2. K-way merge sweep over the sampled terms.
	hashes := seg2dSampleHashes(idx)
	samples := seg2dCollectSamples(idx, hashes)
	maxDF := 0
	totalUnits := 0
	for _, sm := range samples {
		if len(sm.units) > maxDF {
			maxDF = len(sm.units)
		}
		totalUnits += len(sm.units)
	}

	// Baseline: one sequential pass over the full posting list per term.
	var baselineSum uint64
	baselineStart := time.Now()
	baselineVisits := 0
	for _, sm := range samples {
		for _, unit := range sm.units {
			baselineVisits++
			baselineSum += uint64(uint32(unit))
		}
	}
	baselineElapsed := time.Since(baselineStart)
	baselineNsPerUnit := float64(baselineElapsed.Nanoseconds()) / float64(baselineVisits)
	baselineAllocs := testing.AllocsPerRun(3, func() {
		for _, sm := range samples {
			for _, unit := range sm.units {
				baselineSum += uint64(uint32(unit))
			}
		}
	})
	t.Logf("single-block sequential: terms=%d units=%d time=%s ns/unit=%.2f allocs/op=%.1f",
		len(samples), totalUnits, baselineElapsed.Round(time.Microsecond), baselineNsPerUnit, baselineAllocs)

	// K=1 parity is the hard correctness gate: the merge over a single block
	// must reproduce the full posting sequence element by element.
	var scratch seg2dMergeScratch
	mergedBuf := make([]int32, 0, maxDF)
	bounds1 := seg2dBlockBounds(idx.N, 1)
	for _, sm := range samples {
		scratch.split(sm.units, &bounds1, 1)
		merged := scratch.mergeInto(1, mergedBuf[:0])
		if len(merged) != len(sm.units) {
			t.Fatalf("K=1 term %x merged %d units, want %d", sm.hash, len(merged), len(sm.units))
		}
		for i := range merged {
			if merged[i] != sm.units[i] {
				t.Fatalf("K=1 term %x first divergence at %d: merged=%d full=%d",
					sm.hash, i, merged[i], sm.units[i])
			}
		}
	}
	t.Logf("K=1 parity: terms=%d sequences equal element-wise", len(samples))

	for _, k := range seg2dMergeK {
		bounds := seg2dBlockBounds(idx.N, k)
		for _, sm := range samples {
			scratch.split(sm.units, &bounds, k)
			merged := scratch.mergeInto(k, mergedBuf[:0])
			if len(merged) != len(sm.units) {
				t.Fatalf("K=%d term %x merged %d units, want %d", k, sm.hash, len(merged), len(sm.units))
			}
			for i := range merged {
				if merged[i] != sm.units[i] {
					t.Fatalf("K=%d term %x first divergence at %d: merged=%d full=%d",
						k, sm.hash, i, merged[i], sm.units[i])
				}
			}
		}
		var sum uint64
		start := time.Now()
		visits := 0
		for _, sm := range samples {
			scratch.split(sm.units, &bounds, k)
			v, s := scratch.mergeCount(k)
			visits += v
			sum += s
		}
		elapsed := time.Since(start)
		nsPerUnit := float64(elapsed.Nanoseconds()) / float64(visits)
		allocs := testing.AllocsPerRun(3, func() {
			for _, sm := range samples {
				scratch.split(sm.units, &bounds, k)
				scratch.mergeCount(k)
			}
		})
		t.Logf("K=%2d blocks=%2d units=%d merge=%s ns/unit=%.2f allocs/op=%.1f vs-single=%.2fx sequence=equal visits=%d sum=%d",
			k, k, totalUnits, elapsed.Round(time.Microsecond), nsPerUnit, allocs,
			nsPerUnit/baselineNsPerUnit, visits, sum)
	}

	// 3. Hub term: a df=N merge at K=16 must be allocation-free.
	hubUnits := make([]int32, 0, idx.N)
	idx.ForEachPostingUnit(seg2dHubTerm, func(unit int32) {
		hubUnits = append(hubUnits, unit)
	})
	if len(hubUnits) != idx.N {
		t.Fatalf("hub postings=%d, want N=%d", len(hubUnits), idx.N)
	}
	bounds16 := seg2dBlockBounds(idx.N, 16)
	var hubScratch seg2dMergeScratch
	var hubVisits int
	var hubSum uint64
	hubAllocs := testing.AllocsPerRun(200, func() {
		hubScratch.split(hubUnits, &bounds16, 16)
		hubVisits, hubSum = hubScratch.mergeCount(16)
	})
	if hubAllocs != 0 {
		t.Fatalf("hub df=N K=16 merge allocated %.1f objects per run, want 0", hubAllocs)
	}
	hubBytes := medianQueryAlloc(func() {
		hubScratch.split(hubUnits, &bounds16, 16)
		hubVisits, hubSum = hubScratch.mergeCount(16)
	})
	t.Logf("hub df=N K=16: df=%d units=%d allocs/op=%.1f B/query=%d visited=%d sum=%d",
		idx.TermDF(seg2dHubTerm), len(hubUnits), hubAllocs, hubBytes, hubVisits, hubSum)

	// 4. Block build cost for one append batch, against the 5 ms red line.
	for _, b := range seg2dBuildB {
		docs := b
		if docs > idx.N {
			docs = idx.N
		}
		times := make([]time.Duration, 3)
		var entries, keys int
		for i := range times {
			start := time.Now()
			block := seg2dBuildTermBlock(idx, 0, docs)
			times[i] = time.Since(start)
			entries, keys = len(block.units), len(block.keys)
		}
		median := percentileDuration(times, 0.50)
		margin := 100 * float64(seg2dBlockBudget-median) / float64(seg2dBlockBudget)
		t.Logf("block build: docs=%4d entries=%7d keys=%6d median=%s budget=%s margin=%+.1f%%",
			docs, entries, keys, median.Round(time.Microsecond), seg2dBlockBudget, margin)
	}

	// Release the full-scale index and its sample buffers before the drift
	// measurement builds its own two indexes.
	samples = nil
	scratch = seg2dMergeScratch{}
	idx = nil
	runtime.GC()

	// 5. Document entry drift: does adding one unit reorder or re-mass the
	// existing DocTerms entries? Alignment is by UnitID because BuildIndex
	// orders units lexicographically, not by input position.
	idxOld := BuildIndex(corpus[:seg2dDriftBase])
	snap := make(map[string]seg2dDocSnapshot, idxOld.N)
	for u := 0; u < idxOld.N; u++ {
		terms := make([]uint64, len(idxOld.DocTerms[u]))
		copy(terms, idxOld.DocTerms[u])
		snap[idxOld.UnitIDs[u]] = seg2dDocSnapshot{
			massBits: math.Float64bits(idxOld.DocMass[u]),
			terms:    terms,
		}
	}
	baseUnits := idxOld.N
	idxOld = nil
	runtime.GC()

	idxNew := BuildIndex(corpus[:seg2dDriftBase+1])
	massChanged, termsChanged, aligned := 0, 0, 0
	for u := 0; u < idxNew.N; u++ {
		old, ok := snap[idxNew.UnitIDs[u]]
		if !ok {
			continue
		}
		aligned++
		if math.Float64bits(idxNew.DocMass[u]) != old.massBits {
			massChanged++
		}
		cur := idxNew.DocTerms[u]
		if len(cur) != len(old.terms) {
			termsChanged++
			continue
		}
		for i := range cur {
			if cur[i] != old.terms[i] {
				termsChanged++
				break
			}
		}
	}
	if aligned != baseUnits {
		t.Fatalf("drift alignment matched %d units, want %d", aligned, baseUnits)
	}
	newUnit := corpus[seg2dDriftBase].UnitID
	t.Logf("doc entry drift: base=%d new=%d aligned=%d newUnit=%q newUnitIndex=%d docMassBitsChanged=%d (%.1f%%) termSequenceChanged=%d (%.1f%%)",
		baseUnits, idxNew.N, aligned, newUnit, idxNew.UnitOf[newUnit],
		massChanged, 100*float64(massChanged)/float64(aligned),
		termsChanged, 100*float64(termsChanged)/float64(aligned))
	snap = nil
	idxNew = nil
	corpus = nil
	runtime.GC()

	// 6. Footprint extrapolated from the scale counts, plus the per-block
	// prefix-bucket overhead at the persistence fanout.
	const bucketBytes = (seg2dPrefixBuckets + 1) * 4
	termKeysB := counts.distinct * 8
	rowPtrB := (counts.distinct + 1) * 4
	unitsB := counts.postings * 4
	docTermsB := counts.postings * 8
	docMassB := counts.units * 8
	totalB := termKeysB + rowPtrB + unitsB + docTermsB + docMassB
	t.Logf("footprint: termKeys=%d B rowPtr=%d B units=%d B docTerms=%d B docMass=%d B total=%d B (%.1f MiB)",
		termKeysB, rowPtrB, unitsB, docTermsB, docMassB, totalB, float64(totalB)/(1<<20))
	t.Logf("footprint: prefixBuckets perBlock=%d B K=%d extra=%d B (%.1f KiB)",
		bucketBytes, seg2dPersistenceK, bucketBytes*seg2dPersistenceK,
		float64(bucketBytes*seg2dPersistenceK)/1024)
}
