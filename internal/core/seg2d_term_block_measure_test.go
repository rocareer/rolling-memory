// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package core

import (
	"fmt"
	"math"
	"sort"
	"testing"
	"time"
)

const (
	seg2dTrueBlockUnits = 600
	seg2dTrueHubHash    = uint64(0x0000000000000101)
	seg2dTrueProperMask = uint8(1<<0 | 1<<1)
)

var seg2dTrueBlockK = []int{2, 4, 8, 16, 32}

// seg2dTrueBlockCorpus distributes terms across the complete prefix-table domain
// and varies each semigroup input across unit intervals. It intentionally avoids
// corpus-specific tokens: the only input is formal term metadata.
func seg2dTrueBlockCorpus(n int) []Doc {
	buckets := [...]uint64{0, 17, 1023, 1024, 2048, 4095}
	docs := make([]Doc, n)
	for i := range docs {
		doc := Doc{
			UnitID:     fmt.Sprintf("term-block-%06d", i),
			OccurredAt: 1700000000 + int64(i)*60,
			Terms:      make(map[uint64]Term, len(buckets)+2),
			Counts:     make(map[uint64]int, len(buckets)+2),
		}
		for j, bucket := range buckets {
			h := bucket<<52 | uint64(0x100+j)
			flags := uint8(0)
			if (i+j)%2 == 0 {
				flags |= 1 << 0
			}
			if (i+j)%3 == 0 {
				flags |= 1 << 1
			}
			if (i+j)%5 == 0 {
				flags |= rmFlagCompanion
			}
			group := h
			if (i+j)%4 == 0 {
				group = bucket<<52 | uint64(j)
			}
			doc.Terms[h] = Term{
				Hash:  h,
				Bits:  uint16(8 + (i+j)%7*8),
				Flags: flags,
				Group: group,
				Role:  RolePrimary,
			}
			doc.Counts[h] = (i + j) % 6
		}
		flags := uint8(0)
		if i%3 == 0 {
			flags |= 1 << 0
		}
		if i%5 == 0 {
			flags |= 1 << 1
		}
		if i%7 == 0 {
			flags |= rmFlagCompanion
		}
		doc.Terms[seg2dTrueHubHash] = Term{
			Hash:  seg2dTrueHubHash,
			Bits:  uint16(12 + i%9*4),
			Flags: flags,
			Group: uint64(i % 4),
			Role:  RolePrimary,
		}
		doc.Counts[seg2dTrueHubHash] = i % 5
		if i%4 == 0 {
			h := buckets[i%len(buckets)]<<52 | uint64(0x8000+i%19)
			doc.Terms[h] = Term{Hash: h, Bits: uint16(16 + i%5*8), Role: RolePrimary}
			doc.Counts[h] = 1 + i%3
		}
		docs[i] = doc
	}
	return docs
}

// seg2dTrueBlocks is a test-only view over the canonical seg2dTermBlock
// representation. It deliberately has no production consumer: 2d-1a validates
// the representation before 2d-1b wires doc entries and the query path.
type seg2dTrueBlocks struct {
	bounds []int32
	blocks []seg2dTermBlock
	n      int
}

func seg2dTrueBlockBounds(begin, end, k int) []int32 {
	if begin < 0 || end < begin || k < 1 || k > end-begin {
		return nil
	}
	bounds := make([]int32, k+1)
	span := end - begin
	for i := range bounds {
		bounds[i] = int32(begin + int(int64(i)*int64(span)/int64(k)))
	}
	return bounds
}

// seg2dBuildTrueBlocks makes every block through seg2dBuildTermBlock, the
// pre-registered 2d measurement builder. No global posting list is sliced or
// re-merged to construct these rows.
func seg2dBuildTrueBlocks(idx *Index, begin, end, k int) *seg2dTrueBlocks {
	if idx == nil || begin < 0 || end > idx.N {
		return nil
	}
	bounds := seg2dTrueBlockBounds(begin, end, k)
	if len(bounds) == 0 {
		return nil
	}
	out := &seg2dTrueBlocks{
		bounds: bounds,
		blocks: make([]seg2dTermBlock, 0, k),
		n:      idx.N,
	}
	for i := 0; i < k; i++ {
		out.blocks = append(out.blocks, seg2dBuildTermBlock(idx, int(bounds[i]), int(bounds[i+1])))
	}
	return out
}

func seg2dJoinTrueBlocks(n int, left, right *seg2dTrueBlocks) *seg2dTrueBlocks {
	if left == nil || right == nil || len(left.bounds) == 0 || len(right.bounds) == 0 || left.bounds[len(left.bounds)-1] != right.bounds[0] {
		return nil
	}
	bounds := make([]int32, 0, len(left.bounds)+len(right.bounds)-1)
	bounds = append(bounds, left.bounds...)
	bounds = append(bounds, right.bounds[1:]...)
	blocks := make([]seg2dTermBlock, 0, len(left.blocks)+len(right.blocks))
	blocks = append(blocks, left.blocks...)
	blocks = append(blocks, right.blocks...)
	return &seg2dTrueBlocks{bounds: bounds, blocks: blocks, n: n}
}

// seg2dLookupTrueBlock matches the global index lookup shape: first restrict
// the sorted key column with the 4,096-entry prefix table, then binary search
// only that bucket's half-open key range.
func seg2dLookupTrueBlock(block *seg2dTermBlock, hash uint64) (int, bool) {
	if block == nil || len(block.keys) == 0 || len(block.prefixPtr) != seg2dPrefixBuckets+1 {
		return -1, false
	}
	bucket := hash >> 52
	lo, hi := int(block.prefixPtr[bucket]), int(block.prefixPtr[bucket+1])
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if block.keys[mid] < hash {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo < len(block.keys) && block.keys[lo] == hash {
		return lo, true
	}
	return -1, false
}

type seg2dTrueBlockCursor struct {
	blocks *seg2dTrueBlocks
	hash   uint64
	at     int
	pos    int
	end    int
	units  []int32
}

func (c *seg2dTrueBlockCursor) reset(blocks *seg2dTrueBlocks, hash uint64) {
	c.blocks = blocks
	c.hash = hash
	c.at = 0
	c.pos = 0
	c.end = 0
	c.units = nil
}

func (c *seg2dTrueBlockCursor) next() (int32, bool) {
	for {
		if c.pos < c.end {
			unit := c.units[c.pos]
			c.pos++
			return unit, true
		}
		if c.blocks == nil || c.at >= len(c.blocks.blocks) {
			return 0, false
		}
		block := &c.blocks.blocks[c.at]
		c.at++
		col, ok := seg2dLookupTrueBlock(block, c.hash)
		if !ok {
			continue
		}
		start, end := int(block.rowPtr[col]), int(block.rowPtr[col+1])
		if start == end {
			continue
		}
		c.units = block.units
		c.pos, c.end = start, end
	}
}

func seg2dTrueBlockKeys(blocks *seg2dTrueBlocks) []uint64 {
	if blocks == nil {
		return nil
	}
	keys := make([]uint64, 0)
	for i := range blocks.blocks {
		keys = append(keys, blocks.blocks[i].keys...)
	}
	sortUint64Asc(keys)
	unique := 0
	for i, h := range keys {
		if i == 0 || h != keys[unique-1] {
			keys[unique] = h
			unique++
		}
	}
	return keys[:unique:unique]
}

type seg2dTrueBlockStats struct {
	df          int
	cf          int64
	propCnt     int
	parentGroup uint64
	isPrimary   bool
	bits        uint16
	flags       uint8
}

func seg2dTrueTermValues(idx *Index, hash uint64, unit int32) (uint16, uint8, uint64) {
	bits := idx.TermBits[hash]
	if int(unit) < len(idx.UnitBits) && idx.UnitBits[unit] != nil {
		if value, ok := idx.UnitBits[unit][hash]; ok {
			bits = value
		}
	}
	flags := idx.TermFlags[hash]
	if int(unit) < len(idx.UnitFlags) && idx.UnitFlags[unit] != nil {
		if value, ok := idx.UnitFlags[unit][hash]; ok {
			flags = value
		}
	}
	group := hash
	if int(unit) < len(idx.UnitGroups) && idx.UnitGroups[unit] != nil {
		if value := idx.UnitGroups[unit][hash]; value != 0 {
			group = value
		}
	}
	return bits, flags, group
}

// seg2dTrueBlockStatsFromRows derives the permitted integer semigroups from
// actual block rows and checks their posting identity against Index.Postings.
func seg2dTrueBlockStatsFromRows(idx *Index, blocks *seg2dTrueBlocks, hash uint64) (seg2dTrueBlockStats, bool) {
	var stats seg2dTrueBlockStats
	if idx == nil || blocks == nil {
		return stats, false
	}
	found, haveGroup := false, false
	posts := idx.Postings[hash]
	for bi := range blocks.blocks {
		block := &blocks.blocks[bi]
		col, ok := seg2dLookupTrueBlock(block, hash)
		if !ok {
			continue
		}
		start, end := int(block.rowPtr[col]), int(block.rowPtr[col+1])
		postAt := sort.Search(len(posts), func(i int) bool { return posts[i].Unit >= blocks.bounds[bi] })
		if end-start == 0 || postAt+end-start > len(posts) {
			return seg2dTrueBlockStats{}, false
		}
		found = true
		for row := start; row < end; row++ {
			post := posts[postAt+row-start]
			unit := block.units[row]
			if post.Unit != unit {
				return seg2dTrueBlockStats{}, false
			}
			stats.df++
			tf := int64(post.Occ)
			if tf < 1 {
				tf = 1
			}
			stats.cf += tf
			bits, flags, group := seg2dTrueTermValues(idx, hash, unit)
			if flags&seg2dTrueProperMask != 0 {
				stats.propCnt++
			}
			if flags&rmFlagCompanion == 0 {
				stats.isPrimary = true
			}
			if bits > stats.bits {
				stats.bits = bits
			}
			stats.flags |= flags
			if !haveGroup || canonicalHostPreferred(idx, group, stats.parentGroup) {
				stats.parentGroup = group
				haveGroup = true
			}
		}
	}
	return stats, found
}

func seg2dTrueTermMass(blocks *seg2dTrueBlocks, stats seg2dTrueBlockStats) float64 {
	return math.Log1p((float64(blocks.n) - float64(stats.df) + 0.5) / (float64(stats.df) + 0.5))
}

func seg2dAssertTrueBlockShape(t *testing.T, blocks *seg2dTrueBlocks) {
	t.Helper()
	if blocks == nil || len(blocks.bounds) != len(blocks.blocks)+1 || len(blocks.blocks) == 0 {
		t.Fatalf("invalid block view: %#v", blocks)
	}
	if blocks.bounds[0] != 0 || int(blocks.bounds[len(blocks.bounds)-1]) != blocks.n {
		t.Fatalf("block intervals do not cover [0,%d): %v", blocks.n, blocks.bounds)
	}
	for bi := range blocks.blocks {
		block := &blocks.blocks[bi]
		if blocks.bounds[bi] >= blocks.bounds[bi+1] {
			t.Fatalf("block %d is an empty interval [%d,%d)", bi, blocks.bounds[bi], blocks.bounds[bi+1])
		}
		if len(block.prefixPtr) != seg2dPrefixBuckets+1 || block.prefixPtr[0] != 0 || int(block.prefixPtr[seg2dPrefixBuckets]) != len(block.keys) {
			t.Fatalf("block %d invalid prefix table: len=%d first=%d last=%d keys=%d", bi, len(block.prefixPtr), block.prefixPtr[0], block.prefixPtr[len(block.prefixPtr)-1], len(block.keys))
		}
		for i := 1; i < len(block.prefixPtr); i++ {
			if block.prefixPtr[i] < block.prefixPtr[i-1] {
				t.Fatalf("block %d prefix pointer descends at bucket %d", bi, i)
			}
		}
		if len(block.rowPtr) != len(block.keys)+1 || block.rowPtr[0] != 0 || int(block.rowPtr[len(block.rowPtr)-1]) != len(block.units) {
			t.Fatalf("block %d invalid row pointer: rows=%d keys=%d units=%d", bi, len(block.rowPtr), len(block.keys), len(block.units))
		}
		for i, hash := range block.keys {
			if i > 0 && block.keys[i-1] >= hash {
				t.Fatalf("block %d key order breaks at %d", bi, i)
			}
			bucket := hash >> 52
			if i < int(block.prefixPtr[bucket]) || i >= int(block.prefixPtr[bucket+1]) {
				t.Fatalf("block %d key %x lies outside bucket %d", bi, hash, bucket)
			}
			if col, ok := seg2dLookupTrueBlock(block, hash); !ok || col != i {
				t.Fatalf("block %d lookup %x=(%d,%v), want (%d,true)", bi, hash, col, ok, i)
			}
			start, end := int(block.rowPtr[i]), int(block.rowPtr[i+1])
			if start == end {
				t.Fatalf("block %d key %x has a phantom row", bi, hash)
			}
			for at := start; at < end; at++ {
				unit := block.units[at]
				if unit < blocks.bounds[bi] || unit >= blocks.bounds[bi+1] {
					t.Fatalf("block %d key %x unit %d outside [%d,%d)", bi, hash, unit, blocks.bounds[bi], blocks.bounds[bi+1])
				}
				if at > start && block.units[at-1] >= unit {
					t.Fatalf("block %d key %x postings are not strictly ascending", bi, hash)
				}
			}
		}
	}
}

func seg2dAssertTrueBlockParity(t *testing.T, idx *Index, blocks *seg2dTrueBlocks, checkDocMass bool) {
	t.Helper()
	eng := NewEngine(idx)
	state := eng.ensureClusterState()
	keys := seg2dTrueBlockKeys(blocks)
	if len(keys) != len(idx.TermKeys) {
		t.Fatalf("block key union=%d index term keys=%d", len(keys), len(idx.TermKeys))
	}
	var meanBitsSum float64
	meanBitsCount := 0
	for i, hash := range keys {
		if idx.TermKeys[i] != hash {
			t.Fatalf("key union diverges at %d: block=%x index=%x", i, hash, idx.TermKeys[i])
		}
		stats, ok := seg2dTrueBlockStatsFromRows(idx, blocks, hash)
		if !ok || stats.df != idx.TermDF(hash) {
			t.Fatalf("term %x block df=%d ok=%v index df=%d", hash, stats.df, ok, idx.TermDF(hash))
		}
		if stats.bits != idx.TermBitsOf(hash) || stats.flags != idx.TermFlagsOf(hash) {
			t.Fatalf("term %x block bits/flags=%d/%d index=%d/%d", hash, stats.bits, stats.flags, idx.TermBitsOf(hash), idx.TermFlagsOf(hash))
		}
		parent, _ := state.parentGroupOf(hash)
		if stats.parentGroup != parent || stats.isPrimary != state.isPrimaryOf(hash) {
			t.Fatalf("term %x block parent/primary=%x/%v state=%x/%v", hash, stats.parentGroup, stats.isPrimary, parent, state.isPrimaryOf(hash))
		}
		mass := seg2dTrueTermMass(blocks, stats)
		wantMass, haveMass := idx.TermMassOf(hash)
		if !haveMass || math.Float64bits(mass) != math.Float64bits(wantMass) {
			t.Fatalf("term %x mass bits=%016x index=%016x", hash, math.Float64bits(mass), math.Float64bits(wantMass))
		}
		ratio := 0.0
		if stats.propCnt > 0 {
			ratio = float64(stats.propCnt) / float64(stats.df)
		}
		if math.Float64bits(ratio) != math.Float64bits(state.properRatioOf(hash)) {
			t.Fatalf("term %x proper ratio bits=%016x state=%016x", hash, math.Float64bits(ratio), math.Float64bits(state.properRatioOf(hash)))
		}
		var cursor seg2dTrueBlockCursor
		cursor.reset(blocks, hash)
		csr := state.termPostings(hash)
		for at := 0; ; at++ {
			unit, haveUnit := cursor.next()
			if !haveUnit {
				if at != len(csr) {
					t.Fatalf("term %x block postings=%d CSR=%d", hash, at, len(csr))
				}
				break
			}
			if at >= len(csr) || unit != csr[at].unit {
				t.Fatalf("term %x CSR posting divergence at %d: block=%d CSR=%v", hash, at, unit, csr)
			}
		}
		if stats.bits > 0 {
			meanBitsSum += float64(stats.bits)
			meanBitsCount++
		}
	}
	meanBits := 0.0
	if meanBitsCount > 0 {
		meanBits = meanBitsSum / float64(meanBitsCount)
	}
	if math.Float64bits(meanBits) != math.Float64bits(idx.MeanBits) {
		t.Fatalf("MeanBits bits=%016x index=%016x", math.Float64bits(meanBits), math.Float64bits(idx.MeanBits))
	}
	if !checkDocMass {
		return
	}
	rebuilt := make([][]uint64, idx.N)
	for _, hash := range keys {
		var cursor seg2dTrueBlockCursor
		cursor.reset(blocks, hash)
		for {
			unit, ok := cursor.next()
			if !ok {
				break
			}
			rebuilt[unit] = append(rebuilt[unit], hash)
		}
	}
	for unit, hashes := range rebuilt {
		terms := make([]docTermMass, len(hashes))
		for i, hash := range hashes {
			mass, _ := idx.TermMassOf(hash)
			terms[i] = docTermMass{h: hash, mass: mass}
		}
		sort.Sort(docTermMassSort(terms))
		var docMass float64
		for i := range terms {
			hashes[i] = terms[i].h
			docMass += terms[i].mass
		}
		if len(hashes) != len(idx.DocTerms[unit]) {
			t.Fatalf("unit %d reconstructed terms=%d index=%d", unit, len(hashes), len(idx.DocTerms[unit]))
		}
		for i := range hashes {
			if hashes[i] != idx.DocTerms[unit][i] {
				t.Fatalf("unit %d term order diverges at %d: block=%x index=%x", unit, i, hashes[i], idx.DocTerms[unit][i])
			}
		}
		if math.Float64bits(docMass) != math.Float64bits(idx.DocMass[unit]) {
			t.Fatalf("unit %d DocMass bits=%016x index=%016x", unit, math.Float64bits(docMass), math.Float64bits(idx.DocMass[unit]))
		}
	}
}

// TestSeg2DTrueBlockParity is the 2d-1a structural gate. Every K>1 view is
// built by seg2dBuildTermBlock and compared with both Index.Postings and the
// cluster state's term CSR, not with a split copy of one global posting list.
func TestSeg2DTrueBlockParity(t *testing.T) {
	idx := BuildIndex(seg2dTrueBlockCorpus(seg2dTrueBlockUnits))
	for _, k := range seg2dTrueBlockK {
		blocks := seg2dBuildTrueBlocks(idx, 0, idx.N, k)
		seg2dAssertTrueBlockShape(t, blocks)
		seg2dAssertTrueBlockParity(t, idx, blocks, false)
		t.Logf("K=%d terms=%d blocks=%d true rows match index and term CSR", k, len(seg2dTrueBlockKeys(blocks)), len(blocks.blocks))
	}
}

// TestSeg2DTrueBlockQueryAllocation separates K prefix probes from block-order
// concatenation. Both paths operate only on true block rows, including misses;
// neither may allocate a merged posting slice.
func TestSeg2DTrueBlockQueryAllocation(t *testing.T) {
	idx := BuildIndex(seg2dTrueBlockCorpus(seg2dTrueBlockUnits))
	keys := seg2dTrueBlockKeys(seg2dBuildTrueBlocks(idx, 0, idx.N, 2))
	for _, k := range seg2dTrueBlockK {
		blocks := seg2dBuildTrueBlocks(idx, 0, idx.N, k)
		var visited int
		var sum uint64
		bytes := medianQueryAlloc(func() {
			visited, sum = 0, 0
			for _, hash := range keys {
				var cursor seg2dTrueBlockCursor
				cursor.reset(blocks, hash)
				for {
					unit, ok := cursor.next()
					if !ok {
						break
					}
					visited++
					sum += uint64(uint32(unit))
				}
			}
		})
		if bytes != 0 {
			t.Fatalf("K=%d true-block query allocated %d B", k, bytes)
		}
		allocs := testing.AllocsPerRun(10, func() {
			for _, hash := range keys {
				var cursor seg2dTrueBlockCursor
				cursor.reset(blocks, hash)
				for {
					_, ok := cursor.next()
					if !ok {
						break
					}
				}
			}
		})
		if allocs != 0 {
			t.Fatalf("K=%d true-block query allocated %.1f objects", k, allocs)
		}

		type rowRange struct {
			block int
			lo    int
			hi    int
		}
		rows := make([]rowRange, 0, k)
		probeCount := 0
		split := seg2dBlockMedianDuration(7, func() {
			probeCount = 0
			for _, hash := range keys {
				rows = rows[:0]
				for bi := range blocks.blocks {
					probeCount++
					if col, ok := seg2dLookupTrueBlock(&blocks.blocks[bi], hash); ok {
						rows = append(rows, rowRange{block: bi, lo: int(blocks.blocks[bi].rowPtr[col]), hi: int(blocks.blocks[bi].rowPtr[col+1])})
					}
				}
			}
		})
		splitAllocs := testing.AllocsPerRun(10, func() {
			for _, hash := range keys {
				rows = rows[:0]
				for bi := range blocks.blocks {
					if col, ok := seg2dLookupTrueBlock(&blocks.blocks[bi], hash); ok {
						rows = append(rows, rowRange{block: bi, lo: int(blocks.blocks[bi].rowPtr[col]), hi: int(blocks.blocks[bi].rowPtr[col+1])})
					}
				}
			}
		})
		if splitAllocs != 0 {
			t.Fatalf("K=%d split allocated %.1f objects", k, splitAllocs)
		}

		flat := make([]rowRange, 0, len(keys)*k)
		offsets := make([]int, 0, len(keys)+1)
		for _, hash := range keys {
			offsets = append(offsets, len(flat))
			for bi := range blocks.blocks {
				if col, ok := seg2dLookupTrueBlock(&blocks.blocks[bi], hash); ok {
					flat = append(flat, rowRange{block: bi, lo: int(blocks.blocks[bi].rowPtr[col]), hi: int(blocks.blocks[bi].rowPtr[col+1])})
				}
			}
		}
		offsets = append(offsets, len(flat))
		concatVisited := 0
		var concatSum uint64
		concat := seg2dBlockMedianDuration(7, func() {
			concatVisited, concatSum = 0, 0
			for term := range keys {
				for _, row := range flat[offsets[term]:offsets[term+1]] {
					units := blocks.blocks[row.block].units
					for at := row.lo; at < row.hi; at++ {
						concatVisited++
						concatSum += uint64(uint32(units[at]))
					}
				}
			}
		})
		measuredConcatVisited, measuredConcatSum := concatVisited, concatSum
		var concatSink uint64
		concatAllocs := testing.AllocsPerRun(10, func() {
			concatSink = 0
			for _, row := range flat {
				for at := row.lo; at < row.hi; at++ {
					concatSink += uint64(uint32(blocks.blocks[row.block].units[at]))
				}
			}
		})
		if concatAllocs != 0 {
			t.Fatalf("K=%d concatenation allocated %.1f objects", k, concatAllocs)
		}
		if measuredConcatVisited != visited || measuredConcatSum != sum {
			t.Fatalf("K=%d concatenation visits/sum=%d/%d query=%d/%d", k, measuredConcatVisited, measuredConcatSum, visited, sum)
		}
		t.Logf("K=%2d B/query=%d allocs/op=%.1f split=%s probes=%d ns/probe=%.2f concat=%s postings=%d ns/posting=%.2f",
			k, bytes, allocs, split.Round(time.Microsecond), probeCount, float64(split.Nanoseconds())/float64(probeCount),
			concat.Round(time.Microsecond), concatVisited, float64(concat.Nanoseconds())/float64(concatVisited))
	}
}

// TestSeg2DTrueBlockDFDriftParity proves that base rows plus newly built tail
// rows reconstruct current global weights and per-document mass/order after N
// and df change. DocMass is recomputed from the rows; it is never stored in a
// block.
func TestSeg2DTrueBlockDFDriftParity(t *testing.T) {
	fullDocs := seg2dTrueBlockCorpus(660)
	const baseN = 600
	baseIdx := BuildIndex(fullDocs[:baseN])
	fullIdx := BuildIndex(fullDocs)
	for unit := 0; unit < baseN; unit++ {
		if baseIdx.UnitIDs[unit] != fullIdx.UnitIDs[unit] {
			t.Fatalf("tail fixture moved old rank %d: %q != %q", unit, baseIdx.UnitIDs[unit], fullIdx.UnitIDs[unit])
		}
	}
	base := seg2dBuildTrueBlocks(baseIdx, 0, baseIdx.N, 8)
	tail := seg2dBuildTrueBlocks(fullIdx, baseN, fullIdx.N, 2)
	joined := seg2dJoinTrueBlocks(fullIdx.N, base, tail)
	seg2dAssertTrueBlockShape(t, joined)
	seg2dAssertTrueBlockParity(t, fullIdx, joined, true)
	t.Logf("df drift: base=%d tail=%d blocks=%d current Mass/properRatio/DocMass Float64bits equal", baseN, fullIdx.N-baseN, len(joined.blocks))
}

// TestSeg2DTrueBlockHubIterator proves the hub case without creating a
// pre-materialized N-long output slice before the allocation measurement.
func TestSeg2DTrueBlockHubIterator(t *testing.T) {
	idx := BuildIndex(seg2dTrueBlockCorpus(seg2dTrueBlockUnits))
	blocks := seg2dBuildTrueBlocks(idx, 0, idx.N, 16)
	visited := 0
	bytes := medianQueryAlloc(func() {
		visited = 0
		var cursor seg2dTrueBlockCursor
		cursor.reset(blocks, seg2dTrueHubHash)
		for {
			unit, ok := cursor.next()
			if !ok {
				break
			}
			if int(unit) != visited {
				t.Fatalf("hub unit=%d at position=%d", unit, visited)
			}
			visited++
		}
	})
	if visited != idx.N || bytes != 0 {
		t.Fatalf("hub visited=%d/%d bytes=%d", visited, idx.N, bytes)
	}
	allocs := testing.AllocsPerRun(50, func() {
		var cursor seg2dTrueBlockCursor
		cursor.reset(blocks, seg2dTrueHubHash)
		for {
			if _, ok := cursor.next(); !ok {
				break
			}
		}
	})
	if allocs != 0 {
		t.Fatalf("hub iterator allocated %.1f objects", allocs)
	}
	t.Logf("hub df=N: N=%d B/query=%d allocs/op=%.1f", idx.N, bytes, allocs)
}

// TestSeg2DTrueBlockDegenerates rejects phantom rows for empty ranges, sparse
// blocks and singleton unit intervals.
func TestSeg2DTrueBlockDegenerates(t *testing.T) {
	docs := []Doc{
		{UnitID: "sparse-0", Terms: map[uint64]Term{}},
		{UnitID: "sparse-1", Terms: map[uint64]Term{0x11: {Hash: 0x11, Bits: 8, Role: RolePrimary}}},
		{UnitID: "sparse-2", Terms: map[uint64]Term{}},
		{UnitID: "sparse-3", Terms: map[uint64]Term{0x11: {Hash: 0x11, Bits: 16, Role: RolePrimary}}},
	}
	idx := BuildIndex(docs)
	blocks := seg2dBuildTrueBlocks(idx, 0, idx.N, idx.N)
	seg2dAssertTrueBlockShape(t, blocks)
	for _, unit := range []int{0, 2} {
		block := blocks.blocks[unit]
		if len(block.keys) != 0 || len(block.units) != 0 || len(block.rowPtr) != 1 || block.rowPtr[0] != 0 {
			t.Fatalf("empty singleton %d has keys=%d units=%d rowPtr=%v", unit, len(block.keys), len(block.units), block.rowPtr)
		}
	}
	empty := seg2dBuildTermBlock(idx, 2, 2)
	if len(empty.keys) != 0 || len(empty.units) != 0 || len(empty.rowPtr) != 1 || empty.rowPtr[0] != 0 {
		t.Fatalf("empty range has keys=%d units=%d rowPtr=%v", len(empty.keys), len(empty.units), empty.rowPtr)
	}
	if _, ok := seg2dLookupTrueBlock(&empty, 0x11); ok {
		t.Fatal("empty range has a lookup row")
	}
	var cursor seg2dTrueBlockCursor
	cursor.reset(blocks, 0x11)
	var got []int32
	for {
		unit, ok := cursor.next()
		if !ok {
			break
		}
		got = append(got, unit)
	}
	if len(got) != 2 || got[0] != 1 || got[1] != 3 {
		t.Fatalf("singleton concatenation=%v, want [1 3]", got)
	}
}

// TestSeg2DTrueBlockPrefixBuckets makes the 4,096-bucket lookup contract
// explicit at low, adjacent, middle and final bucket positions, including a
// miss in a bucket with no row.
func TestSeg2DTrueBlockPrefixBuckets(t *testing.T) {
	buckets := []uint64{0, 17, 1023, 1024, 2048, 4095}
	docs := make([]Doc, 0, len(buckets)*2)
	for i := 0; i < len(buckets)*2; i++ {
		h := buckets[i%len(buckets)]<<52 | uint64(0x400+i%len(buckets))
		docs = append(docs, Doc{
			UnitID: fmt.Sprintf("bucket-%02d", i),
			Terms:  map[uint64]Term{h: {Hash: h, Bits: 12, Role: RolePrimary}},
		})
	}
	idx := BuildIndex(docs)
	blocks := seg2dBuildTrueBlocks(idx, 0, idx.N, 2)
	seg2dAssertTrueBlockShape(t, blocks)
	for _, block := range blocks.blocks {
		for _, hash := range block.keys {
			if _, ok := seg2dLookupTrueBlock(&block, hash); !ok {
				t.Fatalf("bucket lookup missed present key %x", hash)
			}
		}
		missing := uint64(3000)<<52 | 0x55
		if _, ok := seg2dLookupTrueBlock(&block, missing); ok {
			t.Fatalf("bucket lookup found absent key %x", missing)
		}
	}
}
