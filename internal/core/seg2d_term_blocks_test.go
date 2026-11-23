// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package core

import (
	"fmt"
	"math"
	"math/rand"
	"sync"
	"testing"
	"time"
)

// The block fixture is deliberately independent of the 2d scale fixture: a few
// hundred units with a df=N hub, per-unit flag/width/group overrides and a
// power-law tail exercise every block semigroup while keeping the parity gates
// fast enough to run on every core package test invocation.
const (
	seg2dBlockUnits       = 600
	seg2dBlockVocab       = 400
	seg2dBlockTermsPerDoc = 40
	seg2dBlockBase        = uint64(0x2D000000)
	seg2dBlockHubHash     = uint64(0x2DB10C00)
	seg2dBlockSeed        = int64(0x2D1A)
)

// seg2dBlockCorpus builds the block fixture. Every unit carries the hub term
// plus distinct terms drawn from a small vocabulary. Term width, flags and
// family group are deterministic functions of the hash and the unit, so blocks
// see max/OR/min inputs that differ across blocks.
func seg2dBlockCorpus(n int) []Doc {
	rng := rand.New(rand.NewSource(seg2dBlockSeed))
	docs := make([]Doc, n)
	for i := range docs {
		doc := Doc{
			UnitID:     fmt.Sprintf("unit-%06d", i),
			OccurredAt: 1700000000 + int64(i)*60,
			Terms:      make(map[uint64]Term, seg2dBlockTermsPerDoc+1),
			Counts:     make(map[uint64]int, seg2dBlockTermsPerDoc+1),
		}
		doc.Terms[seg2dBlockHubHash] = Term{Hash: seg2dBlockHubHash, Bits: 40, Role: RolePrimary}
		doc.Counts[seg2dBlockHubHash] = 1
		for len(doc.Terms) < seg2dBlockTermsPerDoc {
			h := seg2dBlockBase + uint64(1+rng.Intn(seg2dBlockVocab))
			if _, dup := doc.Terms[h]; dup {
				continue
			}
			bits := uint16(8 + (h%7)*4)
			if h%11 == 0 {
				bits = 0
			}
			flags := uint8(h % 3)
			if h%5 == 0 {
				flags |= 1 << 4
			}
			var group uint64
			if h%4 == 0 {
				// The group of a term depends on the unit too, so the block
				// merge has to pick a real minimum across blocks.
				group = seg2dBlockBase + (h &^ 0x0F) + uint64(i%3)
			}
			occ := 1 + int(h%5)
			if h%9 == 0 {
				occ = 0 // BuildIndex clamps occ<1 to 1; cf must follow.
			}
			doc.Terms[h] = Term{Hash: h, Bits: bits, Flags: flags, Role: RolePrimary, Group: group}
			doc.Counts[h] = occ
		}
		docs[i] = doc
	}
	return docs
}

// seg2dBlockSparseCorpus has empty units and a term that lives in one unit, so
// the degenerate-block gate can assert that no phantom rows are written.
func seg2dBlockSparseCorpus() []Doc {
	docs := make([]Doc, 8)
	for i := range docs {
		docs[i] = Doc{UnitID: fmt.Sprintf("unit-%06d", i), Terms: map[uint64]Term{}}
	}
	put := func(i int, h uint64, bits uint16, flags uint8) {
		docs[i].Terms[h] = Term{Hash: h, Bits: bits, Flags: flags, Role: RolePrimary}
	}
	put(0, 0xAA1, 12, 0)
	put(1, 0xAA1, 9, 1)
	put(1, 0xAA2, 7, 0)
	for i := 4; i < 8; i++ {
		put(i, 0xAA1, uint16(10+i), 2)
	}
	put(7, 0xAB7, 30, 0)
	return docs
}

// seg2dBlockMaxUnits returns the per-block unit cap that yields exactly k
// blocks for n units (k <= n).
func seg2dBlockMaxUnits(n, k int) int { return (n + k - 1) / k }

// seg2dBlockHashes returns the sorted term key column of the index.
func seg2dBlockHashes(idx *Index) []uint64 {
	keys := make([]uint64, 0, len(idx.DF))
	for h := range idx.DF {
		keys = append(keys, h)
	}
	sortUint64Asc(keys)
	return keys
}

// seg2dBlockFullUnits collects the full posting list through the index's own
// iterator, which is the reference the block concatenation is compared to.
func seg2dBlockFullUnits(idx *Index, hash uint64) []int32 {
	out := make([]int32, 0, idx.TermDF(hash))
	idx.ForEachPostingUnit(hash, func(unit int32) {
		out = append(out, unit)
	})
	return out
}

// seg2dBlockVisited collects the cross-block posting sequence.
func seg2dBlockVisited(tb *termBlocks, hash uint64) []int32 {
	out := make([]int32, 0, tb.termPostingCount(hash))
	tb.visitTermPostings(hash, func(unit int32) bool {
		out = append(out, unit)
		return true
	})
	return out
}

// seg2dBlockRefStats recomputes the block semigroup from the in-memory index
// maps with a second implementation of the documented §4b rule.
func seg2dBlockRefStats(idx *Index, hash uint64) termBlockStats {
	var s termBlockStats
	posts := idx.Postings[hash]
	s.df = len(posts)
	for _, p := range posts {
		u := p.Unit
		tf := int64(p.Occ)
		if tf < 1 {
			tf = 1
		}
		s.cf += tf

		fl := idx.TermFlags[hash]
		if int(u) < len(idx.UnitFlags) && idx.UnitFlags[u] != nil {
			if f, ok := idx.UnitFlags[u][hash]; ok {
				fl = f
			}
		}
		if fl&termBlockProperMask != 0 {
			s.propCnt++
		}
		if fl&rmFlagCompanion == 0 {
			s.isPrimary = true
		}
		s.flags |= fl

		bits := idx.TermBits[hash]
		if int(u) < len(idx.UnitBits) && idx.UnitBits[u] != nil {
			if b, ok := idx.UnitBits[u][hash]; ok {
				bits = b
			}
		}
		if bits > s.bits {
			s.bits = bits
		}

		group := hash
		if int(u) < len(idx.UnitGroups) && idx.UnitGroups[u] != nil {
			if g := idx.UnitGroups[u][hash]; g != 0 {
				group = g
			}
		}
		if s.parentGroup == 0 || canonicalHostPreferred(idx, group, s.parentGroup) {
			s.parentGroup = group
		}
	}
	return s
}

// seg2dBlockUnionKeys merges the per-block key columns into the global sorted
// key column.
func seg2dBlockUnionKeys(tb *termBlocks) []uint64 {
	out := make([]uint64, 0, tb.n)
	for _, b := range tb.blocks {
		out = append(out, b.keys...)
	}
	sortUint64Asc(out)
	uniq := 0
	for i, h := range out {
		if i == 0 || h != out[uniq-1] {
			out[uniq] = h
			uniq++
		}
	}
	return out[:uniq]
}

// seg2dBlockAssertIntervals hard-asserts the key-domain contract: the blocks are
// disjoint, ascending and their union is exactly [0, N).
func seg2dBlockAssertIntervals(t *testing.T, tb *termBlocks, maxUnits int) {
	t.Helper()
	if tb == nil {
		t.Fatal("nil block snapshot")
	}
	if len(tb.bounds) != len(tb.blocks)+1 {
		t.Fatalf("bounds=%d blocks=%d", len(tb.bounds), len(tb.blocks))
	}
	if tb.bounds[0] != 0 || int(tb.bounds[len(tb.bounds)-1]) != tb.n {
		t.Fatalf("bounds do not cover [0,%d): %v..%v", tb.n,
			tb.bounds[0], tb.bounds[len(tb.bounds)-1])
	}
	for i, b := range tb.blocks {
		if int(b.lo) != int(tb.bounds[i]) || int(b.hi) != int(tb.bounds[i+1]) {
			t.Fatalf("block %d interval [%d,%d) != bounds [%d,%d)",
				i, b.lo, b.hi, tb.bounds[i], tb.bounds[i+1])
		}
		if b.hi <= b.lo {
			t.Fatalf("block %d is empty interval [%d,%d)", i, b.lo, b.hi)
		}
		if size := int(b.hi - b.lo); size > maxUnits {
			t.Fatalf("block %d size %d > cap %d", i, size, maxUnits)
		}
		if i > 0 && tb.blocks[i-1].hi != b.lo {
			t.Fatalf("blocks %d and %d are not contiguous", i-1, i)
		}
	}
}

// seg2dBlockAssertShape hard-asserts the per-block row invariants: prefix-sum
// rowPtr, df>0 for every key, posting rows strictly ascending inside [lo,hi)
// and every key covered by its prefix bucket.
func seg2dBlockAssertShape(t *testing.T, tb *termBlocks) {
	t.Helper()
	for bi, b := range tb.blocks {
		if len(b.keys) == 0 {
			if len(b.units) != 0 || len(b.rowPtr) != 0 {
				t.Fatalf("block %d empty keys but rows/units present", bi)
			}
			continue
		}
		if len(b.rowPtr) != len(b.keys)+1 {
			t.Fatalf("block %d rowPtr=%d keys=%d", bi, len(b.rowPtr), len(b.keys))
		}
		if b.rowPtr[0] != 0 || int(b.rowPtr[len(b.rowPtr)-1]) != len(b.units) {
			t.Fatalf("block %d rowPtr is not a prefix sum of the unit column", bi)
		}
		for i := range b.keys {
			if i > 0 && b.keys[i-1] >= b.keys[i] {
				t.Fatalf("block %d keys not strictly ascending at %d", bi, i)
			}
			df := int(b.rowPtr[i+1] - b.rowPtr[i])
			if df <= 0 {
				t.Fatalf("block %d term %x phantom row df=%d", bi, b.keys[i], df)
			}
			if df != int(b.df[i]) {
				t.Fatalf("block %d term %x df column %d != row width %d", bi, b.keys[i], b.df[i], df)
			}
			bucket := b.keys[i] >> 52
			if i < int(b.prefixPtr[bucket]) || i >= int(b.prefixPtr[bucket+1]) {
				t.Fatalf("block %d term %x outside prefix bucket %d", bi, b.keys[i], bucket)
			}
			for j := int(b.rowPtr[i]); j < int(b.rowPtr[i+1]); j++ {
				unit := b.units[j]
				if unit < b.lo || unit >= b.hi {
					t.Fatalf("block %d term %x posting unit %d outside [%d,%d)",
						bi, b.keys[i], unit, b.lo, b.hi)
				}
				if j > int(b.rowPtr[i]) && b.units[j-1] >= unit {
					t.Fatalf("block %d term %x postings not strictly ascending", bi, b.keys[i])
				}
			}
		}
	}
}

// TestSeg2DBlockPostingConcatenation is §7-①: concatenating the per-block rows
// in block order must reproduce the full posting list element by element for
// every block count.
func TestSeg2DBlockPostingConcatenation(t *testing.T) {
	idx := BuildIndex(seg2dBlockCorpus(seg2dBlockUnits))
	hashes := seg2dBlockHashes(idx)
	if len(hashes) == 0 {
		t.Fatal("empty fixture")
	}
	for _, k := range []int{1, 2, 4, 8, 16, 32} {
		maxUnits := seg2dBlockMaxUnits(idx.N, k)
		tb := buildTermBlocks(idx, maxUnits)
		if tb == nil {
			t.Fatalf("K=%d build returned nil", k)
		}
		if len(tb.blocks) != k {
			t.Fatalf("K=%d produced %d blocks", k, len(tb.blocks))
		}
		seg2dBlockAssertIntervals(t, tb, maxUnits)
		seg2dBlockAssertShape(t, tb)
		visited := 0
		for _, h := range hashes {
			want := seg2dBlockFullUnits(idx, h)
			got := seg2dBlockVisited(tb, h)
			if len(got) != len(want) {
				t.Fatalf("K=%d term %x concatenated %d postings, want %d", k, h, len(got), len(want))
			}
			for i := range got {
				if got[i] != want[i] {
					t.Fatalf("K=%d term %x first divergence at %d: got=%d want=%d",
						k, h, i, got[i], want[i])
				}
			}
			if n := tb.termPostingCount(h); n != len(want) {
				t.Fatalf("K=%d term %x postingCount=%d df=%d", k, h, n, len(want))
			}
			visited += len(got)
		}
		t.Logf("K=%2d blocks=%2d terms=%d concatenated postings=%d sequences equal", k, k, len(hashes), visited)
	}
}

// TestSeg2DBlockIntegerSemigroup is §7-②: Σdf_i == df, block intervals are
// disjoint with union N, and every integer column matches the §4b rule.
func TestSeg2DBlockIntegerSemigroup(t *testing.T) {
	idx := BuildIndex(seg2dBlockCorpus(seg2dBlockUnits))
	hashes := seg2dBlockHashes(idx)
	for _, k := range []int{2, 8, 32} {
		maxUnits := seg2dBlockMaxUnits(idx.N, k)
		tb := buildTermBlocks(idx, maxUnits)
		seg2dBlockAssertIntervals(t, tb, maxUnits)
		seg2dBlockAssertShape(t, tb)
		dfSum := 0
		for _, h := range hashes {
			s, ok := tb.termStats(h)
			if !ok {
				t.Fatalf("K=%d term %x missing from block stats", k, h)
			}
			want := seg2dBlockRefStats(idx, h)
			if s != want {
				t.Fatalf("K=%d term %x semigroup=%+v want %+v", k, h, s, want)
			}
			if s.df != idx.TermDF(h) {
				t.Fatalf("K=%d term %x Σdf_i=%d df=%d", k, h, s.df, idx.TermDF(h))
			}
			if s.flags != idx.TermFlagsOf(h) {
				t.Fatalf("K=%d term %x OR flags=%d want %d", k, h, s.flags, idx.TermFlagsOf(h))
			}
			if s.bits != idx.TermBitsOf(h) {
				t.Fatalf("K=%d term %x max bits=%d want %d", k, h, s.bits, idx.TermBitsOf(h))
			}
			if s.df != tb.termPostingCount(h) {
				t.Fatalf("K=%d term %x df=%d postingCount=%d", k, h, s.df, tb.termPostingCount(h))
			}
			dfSum += s.df
		}
		wantPosts := 0
		for _, df := range idx.DF {
			wantPosts += df
		}
		if dfSum != wantPosts {
			// Every posting must be counted exactly once across blocks.
			t.Fatalf("K=%d Σdf over terms = %d, index postings = %d", k, dfSum, wantPosts)
		}
		if tb.n != idx.N {
			t.Fatalf("K=%d snapshot N=%d want %d", k, tb.n, idx.N)
		}
		t.Logf("K=%2d blocks=%2d terms=%d Σdf=%d intervals disjoint union=%d", k, len(tb.blocks), len(hashes), dfSum, tb.n)
	}
}

type seg2dBlockRow struct {
	blk    int32
	lo, hi int32
}

// TestSeg2DBlockQueryAllocation is §7-③: for K in {2,4,8,16,32} a full query
// over the sampled terms allocates 0 B, and the probe (split) and the
// concatenation (merge) phases are timed separately.
func TestSeg2DBlockQueryAllocation(t *testing.T) {
	idx := BuildIndex(seg2dBlockCorpus(seg2dBlockUnits))
	hashes := seg2dBlockHashes(idx)
	totalPostings := 0
	for _, h := range hashes {
		totalPostings += idx.TermDF(h)
	}
	for _, k := range []int{2, 4, 8, 16, 32} {
		maxUnits := seg2dBlockMaxUnits(idx.N, k)
		tb := buildTermBlocks(idx, maxUnits)
		if len(tb.blocks) != k {
			t.Fatalf("K=%d produced %d blocks", k, len(tb.blocks))
		}

		// Query-side zero allocation: the iterator must not materialize the
		// cross-block posting list.
		visited := 0
		queryBytes := medianQueryAlloc(func() {
			visited = 0
			for _, h := range hashes {
				visited += tb.visitTermPostings(h, func(unit int32) bool { return true })
			}
		})
		if queryBytes != 0 {
			t.Fatalf("K=%d iterator allocated %d B/query", k, queryBytes)
		}
		if visited != totalPostings {
			t.Fatalf("K=%d visited %d postings, want %d", k, visited, totalPostings)
		}
		queryAllocs := testing.AllocsPerRun(3, func() {
			for _, h := range hashes {
				tb.visitTermPostings(h, func(unit int32) bool { return true })
			}
		})
		if queryAllocs != 0 {
			t.Fatalf("K=%d iterator allocated %.1f objects/query", k, queryAllocs)
		}

		// Split phase: K prefix-bucket probes per term plus the row ranges.
		// Every buffer is preallocated, so the phase itself allocates nothing.
		rows := make([]seg2dBlockRow, 0, k)
		splitNS := int64(0)
		splitVisits := 0
		splitDur := seg2dBlockMedianDuration(7, func() {
			splitVisits = 0
			for _, h := range hashes {
				rows = rows[:0]
				for bi, b := range tb.blocks {
					col, ok := b.index(h)
					if !ok {
						continue
					}
					rows = append(rows, seg2dBlockRow{
						blk: int32(bi),
						lo:  int32(b.rowPtr[col]),
						hi:  int32(b.rowPtr[col+1]),
					})
					splitVisits += int(b.df[col])
				}
			}
		})
		splitAllocs := testing.AllocsPerRun(3, func() {
			rows = rows[:0]
			for _, h := range hashes {
				rows = rows[:0]
				for _, b := range tb.blocks {
					if col, ok := b.index(h); ok {
						rows = append(rows, seg2dBlockRow{lo: int32(b.rowPtr[col]), hi: int32(b.rowPtr[col+1])})
					}
				}
			}
		})
		if splitAllocs != 0 {
			t.Fatalf("K=%d split phase allocated %.1f objects/query", k, splitAllocs)
		}

		// Pre-resolve all ranges once, then time the pure concatenation: the
		// merge phase compares nothing, dedupes nothing and allocates nothing.
		flat := make([]seg2dBlockRow, 0, k*len(hashes))
		offsets := make([]int32, 0, len(hashes)+1)
		for _, h := range hashes {
			offsets = append(offsets, int32(len(flat)))
			for bi, b := range tb.blocks {
				if col, ok := b.index(h); ok {
					flat = append(flat, seg2dBlockRow{
						blk: int32(bi),
						lo:  int32(b.rowPtr[col]),
						hi:  int32(b.rowPtr[col+1]),
					})
				}
			}
		}
		offsets = append(offsets, int32(len(flat)))
		mergeVisits := 0
		var mergeSum uint64
		mergeDur := seg2dBlockMedianDuration(7, func() {
			mergeVisits = 0
			mergeSum = 0
			for ti, h := range hashes {
				_ = h
				for _, r := range flat[offsets[ti]:offsets[ti+1]] {
					units := tb.blocks[r.blk].units
					for j := r.lo; j < r.hi; j++ {
						mergeVisits++
						mergeSum += uint64(uint32(units[j]))
					}
				}
			}
		})
		mergeAllocs := testing.AllocsPerRun(3, func() {
			for _, r := range flat {
				for j := r.lo; j < r.hi; j++ {
					mergeSum += uint64(uint32(tb.blocks[r.blk].units[j]))
				}
			}
		})
		if mergeAllocs != 0 {
			t.Fatalf("K=%d merge phase allocated %.1f objects/query", k, mergeAllocs)
		}
		splitNS = splitDur.Nanoseconds()
		t.Logf("K=%2d split=%s (%d units) merge=%s (%d units) B/query=%d allocs/op=%.1f split-ns/unit=%.2f merge-ns/unit=%.2f sum=%d",
			k, splitDur.Round(time.Microsecond), splitVisits,
			mergeDur.Round(time.Microsecond), mergeVisits,
			queryBytes, queryAllocs,
			float64(splitNS)/float64(splitVisits),
			float64(mergeDur.Nanoseconds())/float64(mergeVisits), mergeSum)
	}
}

// TestSeg2DBlockMassParity is §7-④: Mass, termProperRatio and MeanBits must be
// rebuilt query-side from the block integer semigroup and match BuildIndex /
// MaterializeClusterState bit for bit; the block key union equals TermKeys.
func TestSeg2DBlockMassParity(t *testing.T) {
	idx := BuildIndex(seg2dBlockCorpus(seg2dBlockUnits))
	tb := buildTermBlocks(idx, seg2dBlockMaxUnits(idx.N, 8))
	if tb == nil {
		t.Fatal("build returned nil")
	}
	eng := NewEngine(idx)
	st := eng.ensureClusterState()
	if st == nil {
		t.Fatal("cluster state")
	}
	if len(idx.TermKeys) != len(idx.DF) {
		t.Fatalf("TermKeys=%d DF=%d", len(idx.TermKeys), len(idx.DF))
	}

	union := seg2dBlockUnionKeys(tb)
	if len(union) != len(idx.TermKeys) {
		t.Fatalf("block key union=%d TermKeys=%d", len(union), len(idx.TermKeys))
	}
	for i := range union {
		if union[i] != idx.TermKeys[i] {
			t.Fatalf("key union diverges at %d: %x vs %x", i, union[i], idx.TermKeys[i])
		}
	}

	var bitSum float64
	bitCount := 0
	checked := 0
	for _, h := range idx.TermKeys {
		mass, ok := tb.termMass(h)
		wantMass, wantOK := idx.TermMassOf(h)
		if !ok || !wantOK {
			t.Fatalf("term %x mass ok=%v index ok=%v", h, ok, wantOK)
		}
		if math.Float64bits(mass) != math.Float64bits(wantMass) {
			t.Fatalf("term %x mass bits %016x != %016x", h,
				math.Float64bits(mass), math.Float64bits(wantMass))
		}
		ratio := tb.termProperRatio(h)
		wantRatio := st.properRatioOf(h)
		if math.Float64bits(ratio) != math.Float64bits(wantRatio) {
			t.Fatalf("term %x properRatio bits %016x != %016x", h,
				math.Float64bits(ratio), math.Float64bits(wantRatio))
		}
		s, _ := tb.termStats(h)
		if pg, _ := st.parentGroupOf(h); pg != s.parentGroup {
			t.Fatalf("term %x parentGroup min %x != %x", h, s.parentGroup, pg)
		}
		if st.isPrimaryOf(h) != s.isPrimary {
			t.Fatalf("term %x isPrimary %v != %v", h, s.isPrimary, st.isPrimaryOf(h))
		}
		if idx.TermBitsOf(h) != s.bits {
			t.Fatalf("term %x term bits %d != %d", h, s.bits, idx.TermBitsOf(h))
		}
		if idx.TermFlagsOf(h) != s.flags {
			t.Fatalf("term %x term flags %d != %d", h, s.flags, idx.TermFlagsOf(h))
		}
		if s.bits > 0 {
			bitSum += float64(s.bits)
			bitCount++
		}
		checked++
	}
	meanBits := 0.0
	if bitCount > 0 {
		meanBits = bitSum / float64(bitCount)
	}
	if math.Float64bits(meanBits) != math.Float64bits(idx.MeanBits) {
		t.Fatalf("MeanBits bits %016x != %016x", math.Float64bits(meanBits), math.Float64bits(idx.MeanBits))
	}
	t.Logf("Float64bits parity: terms=%d mass/properRatio/MeanBits equal, key union=%d", checked, len(union))
}

// TestSeg2DBlockAppendPredicate is §7-⑤: a tail append reuses the old blocks;
// a mid-rank UnitID insertion fails the predicate and forces a full rebuild.
func TestSeg2DBlockAppendPredicate(t *testing.T) {
	const baseUnits = 200
	corpus := seg2dBlockCorpus(seg2dBlockUnits)
	idxBase := BuildIndex(corpus[:baseUnits])
	idxFull := BuildIndex(corpus)
	maxUnits := seg2dBlockMaxUnits(idxFull.N, 8)
	tbBase := buildTermBlocks(idxBase, maxUnits)
	if tbBase == nil {
		t.Fatal("base build returned nil")
	}
	oldBlocks := append([]*termBlock(nil), tbBase.blocks...)
	oldN := tbBase.n

	next, ok := tbBase.appendTail(idxFull, maxUnits, corpus[baseUnits:], false)
	if !ok || next == nil {
		t.Fatal("padded tail append rejected")
	}
	if len(next.blocks) <= len(oldBlocks) || next.n != idxFull.N {
		t.Fatalf("tail snapshot blocks=%d n=%d", len(next.blocks), next.n)
	}
	for i := range oldBlocks {
		if next.blocks[i] != oldBlocks[i] {
			t.Fatalf("tail append rebuilt old block %d instead of reusing it", i)
		}
	}
	if tbBase.n != oldN || len(tbBase.blocks) != len(oldBlocks) {
		t.Fatal("tail append mutated the source snapshot")
	}
	if same, ok := tbBase.appendTail(idxBase, maxUnits, nil, false); !ok || same != tbBase {
		t.Fatal("no-op append must return the same snapshot")
	}

	// Query-level parity with a from-scratch snapshot over the same index.
	fresh := buildTermBlocks(idxFull, maxUnits)
	if fresh == nil {
		t.Fatal("fresh build returned nil")
	}
	seg2dBlockAssertSnapshotParity(t, next, fresh, idxFull)

	// Mid-rank insertion: the new UnitID sorts between unit-000049 and
	// unit-000050, so every old rank from 50 on shifts by one.
	mid := append(append([]Doc(nil), corpus[:100]...), Doc{
		UnitID: "unit-0000495",
		Terms:  map[uint64]Term{seg2dBlockHubHash: {Hash: seg2dBlockHubHash, Bits: 40, Role: RolePrimary}},
	})
	idxMid := BuildIndex(mid)
	tbMidBase := buildTermBlocks(BuildIndex(corpus[:100]), seg2dBlockMaxUnits(idxMid.N, 4))
	if tbMidBase == nil {
		t.Fatal("mid base build returned nil")
	}
	if idxMid.UnitIDs[50] != "unit-0000495" {
		t.Fatalf("fixture did not insert at rank 50: %q", idxMid.UnitIDs[50])
	}
	if idxMid.UnitIDs[50] == tbMidBase.unitIDs[50] {
		t.Fatal("fixture failed to shift an existing rank")
	}
	if extended, ok := tbMidBase.appendTail(idxMid, seg2dBlockMaxUnits(idxMid.N, 4), mid[100:], false); ok || extended != nil {
		t.Fatal("mid-rank insertion passed the tail-append predicate")
	}
	rebuilt := tbMidBase.refresh(idxMid, seg2dBlockMaxUnits(idxMid.N, 4), mid[100:], false)
	seg2dBlockAssertSnapshotParity(t, rebuilt, buildTermBlocks(idxMid, seg2dBlockMaxUnits(idxMid.N, 4)), idxMid)

	// Predicate trigger rate: every padded prefix is a tail append; the single
	// mid-rank insertion is not. This is the pre-registered §4a measurement.
	tailHits, tailTotal := 0, 0
	for _, m := range []int{1, 2, 5, 50, 100, 599} {
		if m >= seg2dBlockUnits {
			continue
		}
		snap := buildTermBlocks(BuildIndex(corpus[:m]), maxUnits)
		tailTotal++
		if _, ok := snap.appendTail(idxFull, maxUnits, corpus[m:], false); ok {
			tailHits++
		}
	}
	t.Logf("tail-append predicate: padded prefixes %d/%d ok, mid-rank insertion 0/1 ok (shift detected at rank 50)", tailHits, tailTotal)
}

// seg2dBlockAssertSnapshotParity asserts query equivalence: same term stats,
// same posting sequence and the same rebuilt mass for every term.
func seg2dBlockAssertSnapshotParity(t *testing.T, got, want *termBlocks, idx *Index) {
	t.Helper()
	if got == nil || want == nil {
		t.Fatalf("nil snapshot: got=%v want=%v", got, want)
	}
	if got.n != want.n {
		t.Fatalf("snapshot N %d != %d", got.n, want.n)
	}
	keys := seg2dBlockUnionKeys(want)
	for _, h := range keys {
		gs, gok := got.termStats(h)
		ws, wok := want.termStats(h)
		if gok != wok || gs != ws {
			t.Fatalf("term %x stats %+v (ok=%v) != %+v (ok=%v)", h, gs, gok, ws, wok)
		}
		gseq := seg2dBlockVisited(got, h)
		wseq := seg2dBlockVisited(want, h)
		if len(gseq) != len(wseq) {
			t.Fatalf("term %x postings %d != %d", h, len(gseq), len(wseq))
		}
		for i := range gseq {
			if gseq[i] != wseq[i] {
				t.Fatalf("term %x posting divergence at %d: %d != %d", h, i, gseq[i], wseq[i])
			}
		}
		gm, _ := got.termMass(h)
		wm, _ := want.termMass(h)
		if math.Float64bits(gm) != math.Float64bits(wm) {
			t.Fatalf("term %x rebuilt mass bits %016x != %016x", h, math.Float64bits(gm), math.Float64bits(wm))
		}
	}
}

// TestSeg2DBlockDegenerateBlocks covers §六红队-1: empty corpora, empty blocks
// and single-unit blocks must not produce phantom rows.
func TestSeg2DBlockDegenerateBlocks(t *testing.T) {
	idx := BuildIndex(seg2dBlockSparseCorpus())
	tb := buildTermBlocks(idx, 1)
	if tb == nil {
		t.Fatal("sparse build returned nil")
	}
	if len(tb.blocks) != idx.N {
		t.Fatalf("blocks=%d N=%d", len(tb.blocks), idx.N)
	}
	seg2dBlockAssertIntervals(t, tb, 1)
	seg2dBlockAssertShape(t, tb)
	for _, bi := range []int{2, 3} {
		b := tb.blocks[bi]
		if len(b.keys) != 0 || len(b.units) != 0 || len(b.rowPtr) != 0 {
			t.Fatalf("unit %d has no terms but block holds keys=%d units=%d", bi, len(b.keys), len(b.units))
		}
	}
	// Single-unit blocks: one row per key, pointing at exactly that unit.
	for _, bi := range []int{0, 1, 4, 7} {
		b := tb.blocks[bi]
		for i := range b.keys {
			if b.rowPtr[i+1]-b.rowPtr[i] != 1 {
				t.Fatalf("single-unit block %d term %x row width %d", bi, b.keys[i], b.rowPtr[i+1]-b.rowPtr[i])
			}
			if b.units[b.rowPtr[i]] != int32(bi) {
				t.Fatalf("single-unit block %d term %x posting %d", bi, b.keys[i], b.units[b.rowPtr[i]])
			}
		}
	}
	if got := seg2dBlockVisited(tb, 0xAB7); len(got) != 1 || got[0] != 7 {
		t.Fatalf("single-unit term postings=%v, want [7]", got)
	}
	if s, ok := tb.termStats(0xAB7); !ok || s.df != 1 {
		t.Fatalf("single-unit term stats=%+v ok=%v", s, ok)
	}
	if _, ok := tb.termStats(0xDEAD); ok {
		t.Fatal("absent term reported stats")
	}
	if n := tb.termPostingCount(0xDEAD); n != 0 {
		t.Fatalf("absent term postingCount=%d", n)
	}

	// A zero-width range builds an explicit empty block with no false rows.
	empty := buildTermBlock(idx, 2, 2, scanParentGroups(idx))
	if empty == nil {
		t.Fatal("empty range returned nil")
	}
	if len(empty.keys) != 0 || len(empty.units) != 0 || len(empty.rowPtr) != 0 {
		t.Fatalf("empty range produced rows: keys=%d units=%d", len(empty.keys), len(empty.units))
	}

	// Zero-unit corpus: buildTermBlocks refuses (no blocks to publish) and the
	// zero-value snapshot stays inert.
	idx0 := BuildIndex(nil)
	if idx0.N != 0 {
		t.Fatalf("empty index N=%d", idx0.N)
	}
	if got := buildTermBlocks(idx0, 4); got != nil {
		t.Fatalf("zero-unit build returned %d blocks", len(got.blocks))
	}
	var nilTB *termBlocks
	if n := nilTB.termPostingCount(0xAA1); n != 0 {
		t.Fatalf("nil snapshot postingCount=%d", n)
	}
	if s, ok := nilTB.termStats(0xAA1); ok {
		t.Fatalf("nil snapshot stats=%+v", s)
	}
	if n := nilTB.visitTermPostings(0xAA1, func(int32) bool { return true }); n != 0 {
		t.Fatalf("nil snapshot visited=%d", n)
	}
	if m, ok := nilTB.termMass(0xAA1); ok || m != 0 {
		t.Fatalf("nil snapshot mass=%v ok=%v", m, ok)
	}
}

// TestSeg2DBlockHubIterator is §六-10 / §7-⑧: a df=N term walks the whole
// corpus through the iterator with 0 B/query and no N-long slice.
func TestSeg2DBlockHubIterator(t *testing.T) {
	idx := BuildIndex(seg2dBlockCorpus(seg2dBlockUnits))
	tb := buildTermBlocks(idx, seg2dBlockMaxUnits(idx.N, 16))
	if tb == nil {
		t.Fatal("build returned nil")
	}
	s, ok := tb.termStats(seg2dBlockHubHash)
	if !ok || s.df != idx.N {
		t.Fatalf("hub stats=%+v ok=%v, want df=N=%d", s, ok, idx.N)
	}
	visited := 0
	bytes := medianQueryAlloc(func() {
		visited = 0
		var cur termPostingCursor
		cur.reset(tb, seg2dBlockHubHash)
		for {
			u, ok := cur.next()
			if !ok {
				return
			}
			if int(u) != visited {
				t.Fatalf("hub cursor unit %d at position %d", u, visited)
			}
			visited++
		}
	})
	if visited != idx.N {
		t.Fatalf("hub visited %d units, want %d", visited, idx.N)
	}
	if bytes != 0 {
		t.Fatalf("hub cursor allocated %d B/query", bytes)
	}
	allocs := testing.AllocsPerRun(50, func() {
		tb.visitTermPostings(seg2dBlockHubHash, func(int32) bool { return true })
	})
	if allocs != 0 {
		t.Fatalf("hub iterator allocated %.1f objects/query", allocs)
	}
	t.Logf("hub df=N: N=%d visited=%d B/query=%d allocs/op=%.1f", idx.N, visited, bytes, allocs)
}

// TestSeg2DBlockConcurrentReaders is the query-side concurrence contract from
// RFC §六-5: blocks are immutable, every reader owns its cursor and no shared
// scratch is written.
func TestSeg2DBlockConcurrentReaders(t *testing.T) {
	idx := BuildIndex(seg2dBlockCorpus(seg2dBlockUnits))
	tb := buildTermBlocks(idx, seg2dBlockMaxUnits(idx.N, 8))
	const readers = 8
	wantSum := uint64(idx.N * (idx.N - 1) / 2)
	var wg sync.WaitGroup
	errs := make(chan string, readers)
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var sum uint64
			visited := tb.visitTermPostings(seg2dBlockHubHash, func(u int32) bool {
				sum += uint64(uint32(u))
				return true
			})
			if visited != idx.N || sum != wantSum {
				errs <- fmt.Sprintf("reader visited=%d sum=%d, want %d/%d", visited, sum, idx.N, wantSum)
				return
			}
			if n := tb.termPostingCount(seg2dBlockHubHash); n != idx.N {
				errs <- fmt.Sprintf("reader postingCount=%d, want %d", n, idx.N)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}
}

// TestSeg2DBlockBuildBudget measures the pre-registered 5 ms single-block
// budget on the reference-scale fixture (seg2d report §3.2 / §4d). The hard gate is
// the foreground append case (one block of one appended unit); the 32/256/1024
// rows are the pre-registered B_max curve points and are reported with their
// margin. The §4b integer columns (df/cf/propCnt/min/max/OR) cost per-posting
// flag/width/group lookups that the 2.599 ms key-only stub did not pay, so the
// per-batch curve is a lower bound for this builder, not a pass line.
func TestSeg2DBlockBuildBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("measurement")
	}
	idx := BuildIndex(seg2dCorpus())
	if idx.N != seg2dScaleUnits {
		t.Fatalf("fixture N=%d want %d", idx.N, seg2dScaleUnits)
	}
	boundary := 0
	var oneDocMedian time.Duration
	roots := scanParentGroups(idx)
	for _, b := range seg2dBuildB {
		times := make([]time.Duration, 5)
		var entries, keys int
		for i := range times {
			start := time.Now()
			block := buildTermBlock(idx, 0, b, roots)
			times[i] = time.Since(start)
			if block == nil {
				t.Fatalf("batch %d build returned nil", b)
			}
			entries, keys = len(block.units), len(block.keys)
		}
		median := percentileDuration(times, 0.50)
		margin := 100 * float64(seg2dBlockBudget-median) / float64(seg2dBlockBudget)
		if median <= seg2dBlockBudget {
			boundary = b
		}
		if b == 1 {
			oneDocMedian = median
		}
		t.Logf("block build: docs=%4d entries=%7d keys=%6d median=%s budget=%s margin=%+.1f%%",
			b, entries, keys, median.Round(time.Microsecond), seg2dBlockBudget, margin)
	}
	if oneDocMedian <= 0 || oneDocMedian > seg2dBlockBudget {
		t.Fatalf("one-append block build median=%s exceeds budget %s", oneDocMedian, seg2dBlockBudget)
	}
	t.Logf("5 ms single-block boundary: %d docs (fixture curve; one append = %s)",
		boundary, oneDocMedian.Round(time.Microsecond))
}

// seg2dBlockMedianDuration returns the median wall time of runs executions.
func seg2dBlockMedianDuration(runs int, fn func()) time.Duration {
	samples := make([]time.Duration, runs)
	for i := range samples {
		start := time.Now()
		fn()
		samples[i] = time.Since(start)
	}
	return percentileDuration(samples, 0.5)
}

// TestSeg2DBlockAppendRejectsSameIDOverwrite covers the review gap: a same-ID
// content overwrite keeps N and every old rank unchanged, so the rank-only part
// of the predicate would reuse stale blocks. The caller reports the overwrite
// (Engine.AppendDocs derives prevDrops the same way) and the predicate refuses;
// the ID check is a second guard for a caller that only reports the delta.
//
// Contract: added must be the complete batch passed to AppendDocs. A caller
// that filters an overwrite out of added and does not set replacedOld can still
// be accepted; that is the one case the block layer cannot detect without an
// O(N) content fingerprint, so the engine wiring must derive replacedOld from
// prevDrops.
func TestSeg2DBlockAppendRejectsSameIDOverwrite(t *testing.T) {
	corpus := seg2dBlockCorpus(seg2dBlockUnits)
	base := corpus[:100]
	idxBase := BuildIndex(base)
	maxUnits := seg2dBlockMaxUnits(idxBase.N, 4)
	tb := buildTermBlocks(idxBase, maxUnits)
	if tb == nil {
		t.Fatal("base build returned nil")
	}

	overwritten := append([]Doc(nil), base...)
	overwritten[10] = Doc{
		UnitID:     overwritten[10].UnitID,
		OccurredAt: overwritten[10].OccurredAt,
		Terms:      map[uint64]Term{seg2dBlockHubHash: {Hash: seg2dBlockHubHash, Bits: 40, Role: RolePrimary}},
	}
	idxOver := BuildIndex(overwritten)
	if idxOver.N != idxBase.N || idxOver.UnitIDs[10] != tb.unitIDs[10] {
		t.Fatal("fixture did not keep N and ranks stable under same-ID overwrite")
	}
	if next, ok := tb.appendTail(idxOver, maxUnits, []Doc{overwritten[10]}, true); ok || next != nil {
		t.Fatal("same-ID content overwrite passed the tail-append predicate")
	}
	if next, ok := tb.appendTail(idxOver, maxUnits, nil, true); ok || next != nil {
		t.Fatal("overwrite reported only through replacedOld passed the predicate")
	}
	rebuilt := tb.refresh(idxOver, maxUnits, []Doc{overwritten[10]}, true)
	if rebuilt == nil {
		t.Fatal("refresh must fall back to a full rebuild")
	}
	seg2dBlockAssertSnapshotParity(t, rebuilt, buildTermBlocks(idxOver, maxUnits), idxOver)

	// A genuine tail append mixed with an overwrite is refused too.
	mixed := append(append([]Doc(nil), overwritten...), corpus[100])
	idxMixed := BuildIndex(mixed)
	if next, ok := tb.appendTail(idxMixed, maxUnits, []Doc{overwritten[10], corpus[100]}, true); ok || next != nil {
		t.Fatal("append mixed with same-ID overwrite passed the predicate")
	}
}

// TestSeg2DBlockPrefixBuckets covers the review gap that every main-fixture key
// lands in bucket 0: keys spread over distinct hash>>52 buckets must be found
// inside the per-block prefix window, and keys in empty buckets must miss.
func TestSeg2DBlockPrefixBuckets(t *testing.T) {
	keys := []uint64{
		0x0F00000000000011,
		0x1F00000000000022,
		0x8F00000000000033,
		0xEF00000000000044,
	}
	docs := make([]Doc, 0, len(keys)*2)
	for i := 0; i < len(keys)*2; i++ {
		h := keys[i%len(keys)]
		docs = append(docs, Doc{
			UnitID: fmt.Sprintf("bucket-%02d", i),
			Terms:  map[uint64]Term{h: {Hash: h, Bits: 12, Role: RolePrimary}},
		})
	}
	idx := BuildIndex(docs)
	tb := buildTermBlocks(idx, seg2dBlockMaxUnits(idx.N, 2))
	if tb == nil {
		t.Fatal("build returned nil")
	}
	buckets := make(map[uint64]bool, len(keys))
	for _, block := range tb.blocks {
		for i, h := range block.keys {
			bucket := h >> 52
			buckets[bucket] = true
			lo, hi := int(block.prefixPtr[bucket]), int(block.prefixPtr[bucket+1])
			if i < lo || i >= hi {
				t.Fatalf("key %x col %d outside bucket %d window [%d,%d)", h, i, bucket, lo, hi)
			}
			if col, ok := block.index(h); !ok || col != i {
				t.Fatalf("block.index(%x) = (%d,%v), want (%d,true)", h, col, ok, i)
			}
		}
	}
	if len(buckets) < 2 {
		t.Fatalf("fixture keys landed in %d bucket(s); want at least 2", len(buckets))
	}
	missing := uint64(0x5F00000000000099)
	for i, block := range tb.blocks {
		if _, ok := block.index(missing); ok {
			t.Fatalf("empty-bucket key %x found in block %d", missing, i)
		}
	}
	whole := buildTermBlocks(idx, idx.N)
	for _, h := range idx.TermKeys {
		got := seg2dBlockVisited(tb, h)
		want := seg2dBlockVisited(whole, h)
		if len(got) != len(want) {
			t.Fatalf("term %x postings %d != %d", h, len(got), len(want))
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("term %x posting %d: %d != %d", h, i, got[i], want[i])
			}
		}
	}
}
