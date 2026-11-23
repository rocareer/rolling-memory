// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package core

import (
	"math"
	"slices"
	"sort"
)

type rmDyad struct {
	a, b uint64
}

type rmDocEntry struct {
	id      uint64
	group   uint64
	posOff  uint32
	posLen  uint32
	tf      int32
	bits    uint16
	flags   uint8
	roleExt uint8
}

type rmPostingEntry struct {
	unit  int32
	entry int32
}

type rmFeatureMatch struct {
	group     uint64
	mass      float64
	primMass  float64
	compMass  float64
	primEntry int32
	entry     int32
	positions []int32
}

type rmCandidate struct {
	unit         int32
	matches      []rmFeatureMatch
	lex          float64
	coreLex      float64
	phrase       float64
	entity       float64
	stSpread     float64
	stCorrob     float64
	structure    float64
	clonePenalty float64
	structureSet bool
}

func (c *rmCandidate) hasGroup(g uint64) bool {
	for i := range c.matches {
		if c.matches[i].group == g {
			return true
		}
	}
	return false
}

func (c *rmCandidate) upsertMatch(sc *rmQueryScratch, group uint64, mass float64, entry int32, newBits uint16, unitEntries []rmDocEntry, tieBreakBits bool) {
	// entry < 0 marks a mass-only match without local term evidence (synthetic
	// day-dispatch beacons): no unitEntries dereference, no match positions.
	isExactGroup := tieBreakBits && entry >= 0 && unitEntries[entry].id == group
	for i := range c.matches {
		if c.matches[i].group == group {
			if isExactGroup {
				if mass > c.matches[i].primMass {
					c.matches[i].primMass = mass
					c.matches[i].primEntry = entry
				}
			} else if tieBreakBits {
				if mass > c.matches[i].compMass {
					c.matches[i].compMass = mass
				}
			}
			var oldBits uint16
			if c.matches[i].entry >= 0 {
				oldBits = unitEntries[c.matches[i].entry].bits
			}
			if entry >= 0 {
				if len(c.matches[i].positions) == 0 {
					if c.matches[i].entry >= 0 {
						if sc != nil {
							// Reserve a 4-slot segment so follow-up repeats can append in
							// place, matching the make([]int32, 2, 4) profile without a
							// per-match allocation.
							base := len(sc.matchPosPool)
							sc.matchPosPool = append(sc.matchPosPool, c.matches[i].entry, entry, 0, 0)
							c.matches[i].positions = sc.matchPosPool[base : base+2 : base+4]
						} else {
							pos := make([]int32, 2, 4)
							pos[0] = c.matches[i].entry
							pos[1] = entry
							c.matches[i].positions = pos
						}
					} else if sc != nil {
						base := len(sc.matchPosPool)
						sc.matchPosPool = append(sc.matchPosPool, entry, 0, 0, 0)
						c.matches[i].positions = sc.matchPosPool[base : base+1 : base+1]
					} else {
						c.matches[i].positions = []int32{entry}
					}
				} else {
					c.matches[i].positions = append(c.matches[i].positions, entry)
				}
			}
			if mass > c.matches[i].mass || (tieBreakBits && mass == c.matches[i].mass && newBits > oldBits) {
				c.matches[i].mass = mass
				if entry >= 0 {
					c.matches[i].entry = entry
				}
			}
			return
		}
	}
	var pm, cm float64
	var pe int32
	if isExactGroup {
		pm = mass
		pe = entry
	} else if tieBreakBits {
		cm = mass
	}
	if len(c.matches) == cap(c.matches) && cap(c.matches) < 8 {
		grown := make([]rmFeatureMatch, len(c.matches), 8)
		copy(grown, c.matches)
		c.matches = grown
	}
	c.matches = append(c.matches, rmFeatureMatch{
		group:     group,
		mass:      mass,
		primMass:  pm,
		compMass:  cm,
		primEntry: pe,
		entry:     entry,
	})
}

func effectiveEntryGroup(entry rmDocEntry, idx *Index, highDFThresh int) uint64 {
	g := entry.group
	if g == 0 || (entry.id != g && idx.TermDF(entry.id) > highDFThresh) {
		return entry.id
	}
	return g
}

type rmFastDocStruct struct {
	docTotal   float64
	day        int64
	subjNormSq float64
}

type rmFocalTermWeight struct {
	group  uint64
	weight float64
	localW float64
}

type rmStructGroupW struct {
	group  uint64
	weight float64
}

func upsertStructGroupMax(list []rmStructGroupW, group uint64, weight float64) []rmStructGroupW {
	for i := range list {
		if list[i].group == group {
			if weight > list[i].weight {
				list[i].weight = weight
			}
			return list
		}
	}
	return append(list, rmStructGroupW{group: group, weight: weight})
}

type rmStructSentGroupW struct {
	sent   int
	group  uint64
	weight float64
}

// sortAndFoldSentGroups sorts (sent, group, weight) occurrences in-place and
// folds identical (sent, group) runs to their maximum weight.
func sortAndFoldSentGroups(sentGW []rmStructSentGroupW) []rmStructSentGroupW {
	if len(sentGW) <= 1 {
		return sentGW
	}
	sortSentGroupW(sentGW)
	w := 0
	for i := 0; i < len(sentGW); {
		cur := sentGW[i]
		i++
		for i < len(sentGW) && sentGW[i].sent == cur.sent && sentGW[i].group == cur.group {
			if sentGW[i].weight > cur.weight {
				cur.weight = sentGW[i].weight
			}
			i++
		}
		sentGW[w] = cur
		w++
	}
	return sentGW[:w]
}

// foldSentGroups folds (sentence, group, weight) occurrences to one maximum
// weight per (sentence, group), ordered by ascending sentence then ascending
// group. When the sentence span is proportional to the input size it buckets by
// sentence with a counting scatter, avoiding a global comparison sort; the
// folded sequence is identical either way.
func (sc *rmQueryScratch) foldSentGroups(src []rmStructSentGroupW) []rmStructSentGroupW {
	if len(src) <= 1 {
		return src
	}
	minSent, maxSent := src[0].sent, src[0].sent
	for _, item := range src[1:] {
		if item.sent < minSent {
			minSent = item.sent
		}
		if item.sent > maxSent {
			maxSent = item.sent
		}
	}
	span := maxSent - minSent + 1
	// A counting scatter costs O(span + n); a comparison sort costs O(n log n).
	if span <= 0 || span > 4*len(src) {
		return sortAndFoldSentGroups(src)
	}
	counts := sc.sentSortCounts
	if cap(counts) < span+1 {
		counts = make([]int32, span+1)
	} else {
		counts = counts[:span+1]
		clear(counts)
	}
	for _, item := range src {
		counts[item.sent-minSent+1]++
	}
	for i := 1; i < len(counts); i++ {
		counts[i] += counts[i-1]
	}
	cursor := sc.sentSortCursor
	if cap(cursor) < span {
		cursor = make([]int32, span)
	} else {
		cursor = cursor[:span]
	}
	copy(cursor, counts[:span])
	dst := sc.sentSorted
	if cap(dst) < len(src) {
		dst = make([]rmStructSentGroupW, len(src))
	} else {
		dst = dst[:len(src)]
	}
	for _, item := range src {
		bucket := item.sent - minSent
		dst[cursor[bucket]] = item
		cursor[bucket]++
	}
	w := 0
	for bucket := 0; bucket < span; bucket++ {
		part := dst[counts[bucket]:cursor[bucket]]
		if len(part) > 1 {
			sortSentGroupW(part)
		}
		for i := 0; i < len(part); {
			cur := part[i]
			i++
			for i < len(part) && part[i].group == cur.group {
				if part[i].weight > cur.weight {
					cur.weight = part[i].weight
				}
				i++
			}
			dst[w] = cur
			w++
		}
	}
	sc.sentSortCounts = counts
	sc.sentSortCursor = cursor
	sc.sentSorted = dst
	return dst[:w]
}

// lessSentGroupW orders by ascending sent, then ascending group, then
// descending weight, matching the previous comparator exactly.
func lessSentGroupW(a, b rmStructSentGroupW) bool {
	if a.sent != b.sent {
		return a.sent < b.sent
	}
	if a.group != b.group {
		return a.group < b.group
	}
	return a.weight > b.weight
}

// sortSentGroupW is a typed insertion sort over small slices (falling back to
// sort.Slice for larger inputs), avoiding reflection overhead on the hot path.
// sentGroupWCompare mirrors lessSentGroupW as a typed comparator.
func sentGroupWCompare(a, b rmStructSentGroupW) int {
	if a.sent != b.sent {
		if a.sent < b.sent {
			return -1
		}
		return 1
	}
	if a.group != b.group {
		if a.group < b.group {
			return -1
		}
		return 1
	}
	if a.weight > b.weight {
		return -1
	}
	if a.weight < b.weight {
		return 1
	}
	return 0
}

func sortSentGroupW(s []rmStructSentGroupW) {
	if len(s) <= 32 {
		for i := 1; i < len(s); i++ {
			v := s[i]
			j := i - 1
			for j >= 0 && lessSentGroupW(v, s[j]) {
				s[j+1] = s[j]
				j--
			}
			s[j+1] = v
		}
		return
	}
	slices.SortFunc(s, sentGroupWCompare)
}

func sortInt64Asc(s []int64) {
	if len(s) <= 32 {
		for i := 1; i < len(s); i++ {
			v := s[i]
			j := i - 1
			for j >= 0 && v < s[j] {
				s[j+1] = s[j]
				j--
			}
			s[j+1] = v
		}
		return
	}
	sort.Sort(int64AscSort(s))
}

// structGroupWCompare mirrors the insertion-sort order above: group ascending then
// weight descending.
func structGroupWCompare(a, b rmStructGroupW) int {
	if a.group != b.group {
		if a.group < b.group {
			return -1
		}
		return 1
	}
	if a.weight > b.weight {
		return -1
	}
	if a.weight < b.weight {
		return 1
	}
	return 0
}

func sortStructGroupW(s []rmStructGroupW) {
	if len(s) <= 32 {
		for i := 1; i < len(s); i++ {
			v := s[i]
			j := i - 1
			for j >= 0 && (v.group < s[j].group || (v.group == s[j].group && v.weight > s[j].weight)) {
				s[j+1] = s[j]
				j--
			}
			s[j+1] = v
		}
		return
	}
	slices.SortFunc(s, structGroupWCompare)
}

// Concrete sort.Interface adapters for the typed helpers above and for large
// build-slice call sites: sort.Sort runs the same pdqsort with direct
// Swap/Less dispatch instead of the reflection-based sort.Slice path.
type sentGroupWSort []rmStructSentGroupW

func (s sentGroupWSort) Len() int           { return len(s) }
func (s sentGroupWSort) Less(i, j int) bool { return lessSentGroupW(s[i], s[j]) }
func (s sentGroupWSort) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

type int64AscSort []int64

func (s int64AscSort) Len() int           { return len(s) }
func (s int64AscSort) Less(i, j int) bool { return s[i] < s[j] }
func (s int64AscSort) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

type structGroupWSort []rmStructGroupW

func (s structGroupWSort) Len() int { return len(s) }
func (s structGroupWSort) Less(i, j int) bool {
	if s[i].group != s[j].group {
		return s[i].group < s[j].group
	}
	return s[i].weight > s[j].weight
}
func (s structGroupWSort) Swap(i, j int) { s[i], s[j] = s[j], s[i] }

type rmOccurrenceSort []rmOccurrence

func (s rmOccurrenceSort) Len() int           { return len(s) }
func (s rmOccurrenceSort) Less(i, j int) bool { return lessRmOccurrence(s[i], s[j]) }
func (s rmOccurrenceSort) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

type rmDyadSort []rmDyad

func (s rmDyadSort) Len() int           { return len(s) }
func (s rmDyadSort) Less(i, j int) bool { return lessRmDyad(s[i], s[j]) }
func (s rmDyadSort) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

type termPositionSort []TermPosition

func (s termPositionSort) Len() int { return len(s) }
func (s termPositionSort) Less(i, j int) bool {
	if s[i].Sentence != s[j].Sentence {
		return s[i].Sentence < s[j].Sentence
	}
	return s[i].Slot < s[j].Slot
}
func (s termPositionSort) Swap(i, j int) { s[i], s[j] = s[j], s[i] }

type focalTermWeightSort []rmFocalTermWeight

func (s focalTermWeightSort) Len() int           { return len(s) }
func (s focalTermWeightSort) Less(i, j int) bool { return s[i].group < s[j].group }
func (s focalTermWeightSort) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

// lessFeatureMatchByMassGroup orders matches by ascending mass, then ascending
// group, matching the previous canonical-order comparator exactly.
func lessFeatureMatchByMassGroup(a, b rmFeatureMatch) bool {
	if a.mass != b.mass {
		return a.mass < b.mass
	}
	return a.group < b.group
}

// sortFeatureMatchesByMassGroup puts a candidate's matches into canonical
// accumulation order without the reflection-based sort.Slice path on short
// slices. upsertMatch merges by group, so a candidate holds at most one match
// per group and the (mass, group) key is a strict total order: every correct
// sort yields the same permutation, hence the same float accumulation order.
func sortFeatureMatchesByMassGroup(ms []rmFeatureMatch) {
	if len(ms) < 2 {
		return
	}
	if len(ms) <= 32 {
		for i := 1; i < len(ms); i++ {
			v := ms[i]
			j := i - 1
			for j >= 0 && lessFeatureMatchByMassGroup(v, ms[j]) {
				ms[j+1] = ms[j]
				j--
			}
			ms[j+1] = v
		}
		return
	}
	sort.Sort(rmFeatureMatchSort(ms))
}

type rmFeatureMatchSort []rmFeatureMatch

func (s rmFeatureMatchSort) Len() int           { return len(s) }
func (s rmFeatureMatchSort) Less(i, j int) bool { return lessFeatureMatchByMassGroup(s[i], s[j]) }
func (s rmFeatureMatchSort) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

// rmUnitStrength pairs a unit with its precomputed event strength for the
// per-episode strength ordering.
type rmUnitStrength struct {
	unit     int32
	strength float64
}

type rmUnitStrengthSort []rmUnitStrength

func (s rmUnitStrengthSort) Len() int { return len(s) }
func (s rmUnitStrengthSort) Less(i, j int) bool {
	if s[i].strength != s[j].strength {
		return s[i].strength > s[j].strength
	}
	return s[i].unit < s[j].unit
}
func (s rmUnitStrengthSort) Swap(i, j int) { s[i], s[j] = s[j], s[i] }

// dyadPosting is one (phrase dyad, unit) posting in the cluster build.
type dyadPosting struct {
	pair rmDyad
	unit int32
}

type dyadPostingSort []dyadPosting

func (s dyadPostingSort) Len() int { return len(s) }
func (s dyadPostingSort) Less(i, j int) bool {
	x, y := s[i], s[j]
	if x.pair.a != y.pair.a {
		return x.pair.a < y.pair.a
	}
	if x.pair.b != y.pair.b {
		return x.pair.b < y.pair.b
	}
	return x.unit < y.unit
}
func (s dyadPostingSort) Swap(i, j int) { s[i], s[j] = s[j], s[i] }

// radixSortDyadPostings partitions postings in place into 1024 MSB buckets on
// pair.a >> 54 (American flag sort; no second posting array) and then fully
// sorts each L1-sized bucket with dyadPostingSort. Because dyadPostingSort is a
// total order, the result matches an out-of-place stable partition followed by
// the same per-bucket sorts; only the transient second array is avoided.
func radixSortDyadPostings(postings []dyadPosting) {
	const radixBits = 10
	const numBuckets = 1 << radixBits
	const shift = 64 - radixBits
	var bucketCounts [numBuckets]uint32
	for _, dp := range postings {
		bucketCounts[dp.pair.a>>shift]++
	}
	var bucketOffsets [numBuckets + 1]uint32
	for b := 0; b < numBuckets; b++ {
		bucketOffsets[b+1] = bucketOffsets[b] + bucketCounts[b]
	}
	var next [numBuckets]uint32
	copy(next[:], bucketOffsets[:numBuckets])
	for b := 0; b < numBuckets; b++ {
		end := bucketOffsets[b+1]
		for next[b] < end {
			d := postings[next[b]].pair.a >> shift
			if int(d) == b {
				next[b]++
			} else {
				postings[next[b]], postings[next[d]] = postings[next[d]], postings[next[b]]
				next[d]++
			}
		}
	}
	for b := 0; b < numBuckets; b++ {
		s, e := bucketOffsets[b], bucketOffsets[b+1]
		if e-s <= 1 {
			continue
		}
		sub := postings[s:e]
		sort.Sort(dyadPostingSort(sub))
	}
}

func rmPopCount64(x uint64) int {
	x -= (x >> 1) & 0x5555555555555555
	x = (x & 0x3333333333333333) + ((x >> 2) & 0x3333333333333333)
	x = (x + (x >> 4)) & 0x0f0f0f0f0f0f0f0f
	return int((x * 0x0101010101010101) >> 56)
}

type rmFocalCandDist struct {
	group   uint64
	minDist int
}

type rmQueryScratch struct {
	candidates       []rmCandidate
	unitStructGate   []float64
	coreLex          []float64
	phraseWitness    []float64
	clusterSum       []float64
	clusterHits      []int
	activeClusters   []int32
	maxCoreMass      []float64
	anchorGen        uint32
	unitAnchorGen    []uint32
	clusterAnchorGen []uint32
	anchorTimeSlots  []int32
	focalComps       [][]rmFocalTermWeight
	focalPool        []rmFocalTermWeight
	focalRawSum      []float64
	focalBuilt       []bool
	heapQueue        []rmScoredCandidate
	matchedDG        []uint64
	docGroupW        []rmStructGroupW
	docEntryEffGroup []uint64
	sentGroupW       []rmStructSentGroupW
	sentSortCounts   []int32
	sentSortCursor   []int32
	sentSorted       []rmStructSentGroupW
	sentTotals       []float64
	sentOverlaps     []float64
	activeSentGen    []uint32
	sentGen          uint32
	anchorPacked     []uint32
	focalCands       []rmFocalCandDist
	missingBest      []rmStructGroupW
	latticeGen       uint32
	unitLatticeGen   []uint32
	unitLatticeMask  []uint64
	latticeTouched   []int32
	latticeTriUnits  []int32
	// Reused per-query maps for group postings aggregation.
	groupUnits       map[uint64]map[int32]struct{}
	groupAnchorUnits map[uint64]map[int32]struct{}
	groupDays        map[uint64]map[int64]struct{}
	umPool           []map[int32]struct{}
	aumPool          []map[int32]struct{}
	dmPool           []map[int64]struct{}
	byEpisodeActive  map[int32][]int32
	corefByEpisode   map[int32][]int32
	dirtyGen         uint32
	unitDirtyGen     []uint32
	dirtyUnits       []int32
	lexUnits         []int32
	coreUnits        []int32
	// Per-engine memoization for warm-domain cosine misses. ResetScratch gives
	// concurrent shallow engines independent maps, so shared CSR stays read-only.
	dayCos map[uint64]float64
	// Per-query arena for query terms and TermPositions to avoid map/slice allocations.
	qTerms                 map[uint64]Term
	posMap                 map[uint64][]TermPosition
	posPool                []TermPosition
	queryHashes            []uint64
	hashGroup              map[uint64]uint64
	detachedFrag           map[uint64]bool
	detachedSlot2          map[uint64]bool
	famMinDF               map[uint64]int
	properSlots            map[TermPosition]int
	primaryAtPos           map[TermPosition]uint64
	groupRigid             map[uint64]bool
	groupProper            map[uint64]bool
	hasBroadComp           map[uint64]bool
	kappaOcc               map[uint64]float64
	anchorKappa            map[uint64]float64
	isAnchorGroup          map[uint64]bool
	anchorTimes            []int64
	anchorUnitList         []int32
	shellScale             map[uint64]float64
	groups                 map[uint64]float64
	slotPrim               map[TermPosition]uint64
	slotPure               map[TermPosition]bool
	groupKeys              []uint64
	activeEpisodes         []int32
	latticeGKeys           []uint64
	latticeDeltaCache      map[uint64]float64
	latticeGroupDF         map[uint64]int
	latticeGroupW          map[uint64]float64
	latticeHashes          []uint64
	latticeQGroupMid       map[uint64]uint64
	latticeGroupDFMid      map[uint64]int
	latticeGroupWMid       map[uint64]float64
	latticeMidHashes       []uint64
	selectCoverage         map[uint64]int
	selectCompCov          map[uint64]float64
	selectProfCache        map[int32]unitGroupProfile
	endorseActiveSet       map[int32]bool
	endorseQSet            map[uint64]struct{}
	endorseEventUnits      []scoredEventUnit
	queryOccs              []rmOccurrence
	queryDyads             []rmDyad
	matchPosPool           []int32
	explainAnchorPool      []uint32
	explainAnchorExactPool []TermPosition
	unitStrengthPool       []rmUnitStrength
	// Memory pool for buildUnitGroupProfileWithPos (per-query arena).
	profileItemPool     []unitGroupItem
	profilePosPool      []uint32
	profilePosExactPool []TermPosition
}

func (e *Engine) ensureQueryScratch(n, clusterN int) *rmQueryScratch {
	sc := e.queryScratch
	if sc == nil {
		sc = &rmQueryScratch{}
		e.queryScratch = sc
	}
	if len(sc.candidates) < n {
		sc.candidates = make([]rmCandidate, n)
		for u := 0; u < n; u++ {
			sc.candidates[u].unit = int32(u)
		}
		sc.unitStructGate = make([]float64, n)
		sc.coreLex = make([]float64, n)
		sc.phraseWitness = make([]float64, n)
		sc.maxCoreMass = make([]float64, n)
		sc.unitAnchorGen = make([]uint32, n)
		sc.focalComps = make([][]rmFocalTermWeight, n)
		sc.focalRawSum = make([]float64, n)
		sc.focalBuilt = make([]bool, n)
		sc.unitLatticeGen = make([]uint32, n)
		sc.unitLatticeMask = make([]uint64, n)
		sc.unitDirtyGen = make([]uint32, n)
	}
	if len(sc.clusterSum) < clusterN {
		sc.clusterSum = make([]float64, clusterN)
		sc.clusterHits = make([]int, clusterN)
		sc.clusterAnchorGen = make([]uint32, clusterN)
	}
	if cap(sc.focalPool) < 256 {
		sc.focalPool = make([]rmFocalTermWeight, 0, 256)
	} else {
		sc.focalPool = sc.focalPool[:0]
	}
	if cap(sc.heapQueue) < 512 {
		sc.heapQueue = make([]rmScoredCandidate, 0, 512)
	} else {
		sc.heapQueue = sc.heapQueue[:0]
	}
	return sc
}

func (sc *rmQueryScratch) nextAnchorGen() uint32 {
	sc.anchorGen++
	if sc.anchorGen == 0 {
		clear(sc.unitAnchorGen)
		clear(sc.clusterAnchorGen)
		sc.anchorGen = 1
	}
	return sc.anchorGen
}

func (sc *rmQueryScratch) resetProfilePools() {
	if sc == nil {
		return
	}
	sc.profileItemPool = sc.profileItemPool[:0]
	sc.profilePosPool = sc.profilePosPool[:0]
	sc.profilePosExactPool = sc.profilePosExactPool[:0]
}

func (sc *rmQueryScratch) resetDirtyUnits(n int) uint32 {
	sc.resetProfilePools()
	sc.matchPosPool = sc.matchPosPool[:0]
	if len(sc.dirtyUnits) > 0 {
		for _, u := range sc.dirtyUnits {
			prevMatches := sc.candidates[u].matches[:0]
			sc.candidates[u] = rmCandidate{unit: u, matches: prevMatches}
			sc.coreLex[u] = 0
			sc.phraseWitness[u] = 0
			sc.maxCoreMass[u] = 0
			sc.focalBuilt[u] = false
		}
	}
	for _, cl := range sc.activeClusters {
		sc.clusterSum[cl] = 0
		sc.clusterHits[cl] = 0
	}
	sc.activeClusters = sc.activeClusters[:0]
	sc.dirtyUnits = sc.dirtyUnits[:0]
	sc.lexUnits = sc.lexUnits[:0]
	sc.coreUnits = sc.coreUnits[:0]
	sc.dirtyGen++
	if sc.dirtyGen == 0 {
		clear(sc.unitDirtyGen)
		sc.dirtyGen = 1
	}
	return sc.dirtyGen
}

func (sc *rmQueryScratch) nextLatticeGen() uint32 {
	sc.latticeGen++
	if sc.latticeGen == 0 {
		clear(sc.unitLatticeGen)
		sc.latticeGen = 1
	}
	return sc.latticeGen
}

func (sc *rmQueryScratch) nextSentGen(maxSent int) uint32 {
	if len(sc.activeSentGen) < maxSent+1 {
		sc.activeSentGen = make([]uint32, maxSent+1)
	}
	sc.sentGen++
	if sc.sentGen == 0 {
		clear(sc.activeSentGen)
		sc.sentGen = 1
	}
	return sc.sentGen
}

type rmClusterState struct {
	docRowPtr      []uint32
	docEntriesFlat []rmDocEntry
	// entryMass 是每个条目的词项质量快照（按 docEntriesFlat 下标），每代只查一次
	// idx.Mass，供句能均值与 fastDocs 直读，避免数百万次映射查询。
	entryMass       []float64
	posPool         []uint32
	postings        map[uint64][]rmPostingEntry
	termKeys        []uint64
	termPrefixPtr   [4097]uint32
	termRowPtr      []uint32
	postingPool     []rmPostingEntry
	termEffWeight   []float64
	termProperRatio []float64
	termParentGroup []uint64
	termIsPrimary   []uint8
	phraseKeys      []rmDyad
	phrasePrefixPtr [4097]uint32
	phraseRowPtr    []uint32
	phraseUnits     []int32
	// phraseDelta is the append-only delta over the immutable base phrase CSR:
	// sorted (pair, unit) postings for units appended since the last compaction.
	// Empty means the base alone is authoritative.
	phraseDelta []dyadPosting
	effWeight   map[uint64]float64
	properRatio map[uint64]float64
	parentGroup map[uint64]uint64
	// canonicalParent is the key-invariant family root (most frequent host by
	// df, then widest carrier). parentGroupOf serves every scoring/partner/
	// snapshot consumer from it; parentGroup keeps the legacy min-hash label
	// only as a fallback for states materialized before canonicalParent.
	canonicalParent map[uint64]uint64
	isPrimaryWord   map[uint64]bool
	clusters        []int32
	clusterN        int
	bucketTimes     []int64

	// Exact ordered timestamp-bucket membership is the sole neighborhood backing
	// in memory and in .zcsr. Each timed unit appears once in bucketUnits.
	bucketRowPtr []uint32
	bucketUnits  []int32

	// extraNeighborRowPtr/extraNeighborBuckets contain only references to the
	// orthogonal-transparent buckets beyond the base ±2-bucket band. They are
	// appended after the base bucket sequence, preserving the logical visitor order
	// without duplicating shared bucket members per unit.
	extraNeighborRowPtr  []uint32
	extraNeighborBuckets []int32
	refTimes             []int64
	meanGap              float64
	muLogGap             float64
	sigLogGap            float64
	ordPos               []int32
	episodeOf            []int32
	episodeCount         int
	entryMeanBits        float64
	sentenceEnergyMean   float64
	totalDaysM           float64
	kappaOrd             float64
	fastDocs             []rmFastDocStruct
	subjRowPtr           []uint32
	subjIDsPool          []uint64
	subjWeightsPool      []float64
	dayCos               map[uint64]float64
	dayCosRowPtr         []uint32
	dayCosKeys           []uint64
	dayCosVals           []float64
	provKeys             []uint64
	provRowPtr           []uint32
	provUnits            []int32
	citeRowPtr           []uint32
	citePeers            []int32
	cociteRowPtr         []uint32
	cocitePeers          []int32
	// externalCitationSets carries caller-declared co-citation records whose
	// citing unit is not an index node (a client-side recollection trace that
	// cites evidence already in the corpus). The kernel materializes only the
	// pairwise co-citation it asserts, judging no semantics.
	externalCitationSets [][]string
	authorDF             map[uint64]int32
	meanLineSpan         float64
	widePos              map[uint32]TermPosition

	// 新鲜度指纹：索引对象身份（重建会换指针）+ 世代号（原位变更会自增）。
	// 二者组合精确判定缓存是否仍对应当前索引，无需对全量倒排重算内容哈希。
	builtIndex    *Index
	builtIndexGen uint64

	// 词族增量充分统计量（仅内存态；.zcsr 水合状态为 nil ⇒ append 回退全量重建）。
	agg *clusterAggregates
}

func (st *rmClusterState) buildProvenanceCSR(idx *Index) {
	if st == nil || idx == nil || len(idx.UnitMeta) == 0 {
		return
	}
	type provPair struct {
		key  uint64
		unit int32
	}
	var pairs []provPair
	for u := 0; u < idx.N && u < len(idx.UnitMeta); u++ {
		m := idx.UnitMeta[u]
		if m.DocHash != 0 {
			pairs = append(pairs, provPair{key: m.DocHash, unit: int32(u)})
		}
		if m.BlockHash != 0 && m.BlockHash != m.DocHash {
			pairs = append(pairs, provPair{key: m.BlockHash, unit: int32(u)})
		}
	}
	st.buildCitationCSR(idx)
	if len(pairs) == 0 {
		return
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].key != pairs[j].key {
			return pairs[i].key < pairs[j].key
		}
		return pairs[i].unit < pairs[j].unit
	})
	uniqueKeys := 1
	for i := 1; i < len(pairs); i++ {
		if pairs[i].key != pairs[i-1].key {
			uniqueKeys++
		}
	}
	st.provKeys = make([]uint64, 0, uniqueKeys)
	st.provRowPtr = make([]uint32, 0, uniqueKeys+1)
	st.provUnits = make([]int32, 0, len(pairs))
	for i, p := range pairs {
		if i > 0 && p == pairs[i-1] {
			continue
		}
		if len(st.provKeys) == 0 || p.key != st.provKeys[len(st.provKeys)-1] {
			st.provKeys = append(st.provKeys, p.key)
			st.provRowPtr = append(st.provRowPtr, uint32(len(st.provUnits)))
		}
		st.provUnits = append(st.provUnits, p.unit)
	}
	st.provRowPtr = append(st.provRowPtr, uint32(len(st.provUnits)))
}

func (st *rmClusterState) provenanceUnitsByKey(key uint64) []int32 {
	if st == nil || key == 0 || len(st.provKeys) == 0 {
		return nil
	}
	lo, hi := 0, len(st.provKeys)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if st.provKeys[mid] < key {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo < len(st.provKeys) && st.provKeys[lo] == key {
		return st.provUnits[st.provRowPtr[lo]:st.provRowPtr[lo+1]]
	}
	return nil
}

func (st *rmClusterState) forEachProvenancePeer(idx *Index, u int32, fn func(int32)) {
	if st == nil || idx == nil || u < 0 || int(u) >= len(idx.UnitMeta) {
		return
	}
	if len(st.provKeys) > 0 {
		m := idx.UnitMeta[u]
		if m.DocHash != 0 {
			for _, peer := range st.provenanceUnitsByKey(m.DocHash) {
				if peer != u {
					fn(peer)
				}
			}
		}
		if m.BlockHash != 0 && m.BlockHash != m.DocHash {
			for _, peer := range st.provenanceUnitsByKey(m.BlockHash) {
				if peer != u {
					fn(peer)
				}
			}
		}
	}
	for _, peer := range st.citationPeersOf(u) {
		if peer != u {
			fn(peer)
		}
	}
	for _, peer := range st.coCitationPeersOf(u) {
		if peer != u {
			fn(peer)
		}
	}
}

// buildCitationCSR materializes the explicit citation provenance edges carried
// by DocMeta.CitedIDs (noetic consolidation units citing the evidence units an
// external reader confirmed) as a symmetric unit-to-unit CSR: citing unit and
// cited unit become provenance peers in both directions, exactly like sharing
// a DocHash envelope. The kernel performs no semantic judgement here — the
// edge list is declarative input recorded at ingest time.
func (st *rmClusterState) buildCitationCSR(idx *Index) {
	st.citeRowPtr = nil
	st.citePeers = nil
	if idx == nil || idx.N == 0 {
		return
	}
	type edge struct{ from, to int32 }
	var edges []edge
	for u := 0; u < idx.N && u < len(idx.UnitMeta); u++ {
		cited := idx.UnitMeta[u].CitedIDs
		if len(cited) == 0 {
			continue
		}
		for _, id := range cited {
			v, ok := idx.LookupUnit(id)
			if !ok || v == int32(u) {
				continue
			}
			edges = append(edges, edge{from: int32(u), to: v}, edge{from: v, to: int32(u)})
		}
	}
	if len(edges) == 0 {
		// External co-citation records do not need a citing node in the index,
		// so the second-order CSR must still be built when there are no
		// first-order meta edges.
		st.buildCoCitationCSR(idx)
		return
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].from != edges[j].from {
			return edges[i].from < edges[j].from
		}
		return edges[i].to < edges[j].to
	})
	st.citeRowPtr = make([]uint32, idx.N+1)
	st.citePeers = make([]int32, 0, len(edges))
	cur := 0
	for u := 0; u < idx.N; u++ {
		st.citeRowPtr[u] = uint32(len(st.citePeers))
		for cur < len(edges) && edges[cur].from == int32(u) {
			if n := len(st.citePeers); n == 0 || uint32(n) == st.citeRowPtr[u] || st.citePeers[n-1] != edges[cur].to {
				st.citePeers = append(st.citePeers, edges[cur].to)
			}
			cur++
		}
	}
	st.citeRowPtr[idx.N] = uint32(len(st.citePeers))
	st.buildCoCitationCSR(idx)
}

// buildCoCitationCSR derives the second-order co-citation edges from the
// first-order citation CSR: two evidence units u, v become co-citation peers
// when at least one citing unit lists both in its CitedUnitIDs. A citation
// record is a declarative statement that the cited units jointly supported one
// consolidated answer, so the pairing itself was already asserted by the
// writer — the kernel only materializes it, judging no semantics. Pairs are
// expanded per citing unit (quadratic in its out-degree, bounded by the wire
// payload) and deduplicated into a symmetric CSR aligned with citePeers.
func (st *rmClusterState) buildCoCitationCSR(idx *Index) {
	st.cociteRowPtr = nil
	st.cocitePeers = nil
	if idx == nil || idx.N == 0 {
		return
	}
	if len(st.citeRowPtr) == 0 && len(st.externalCitationSets) == 0 {
		return
	}
	type edge struct{ from, to int32 }
	var edges []edge
	for u := 0; u < idx.N && u < len(idx.UnitMeta); u++ {
		cited := idx.UnitMeta[u].CitedIDs
		if len(cited) < 2 {
			continue
		}
		members := make([]int32, 0, len(cited))
		for _, id := range cited {
			if v, ok := idx.LookupUnit(id); ok && v != int32(u) {
				members = append(members, v)
			}
		}
		for i := 0; i < len(members); i++ {
			for j := i + 1; j < len(members); j++ {
				if members[i] == members[j] {
					continue
				}
				edges = append(edges, edge{from: members[i], to: members[j]}, edge{from: members[j], to: members[i]})
			}
		}
	}
	// Caller-declared co-citation records whose citing unit is not an index
	// node (client-side recollection traces). Deduplicate members so a repeated
	// id in the same record cannot manufacture a self-pair.
	for _, cited := range st.externalCitationSets {
		if len(cited) < 2 {
			continue
		}
		members := make([]int32, 0, len(cited))
		seen := make(map[int32]struct{}, len(cited))
		for _, id := range cited {
			v, ok := idx.LookupUnit(id)
			if !ok {
				continue
			}
			if _, dup := seen[v]; dup {
				continue
			}
			seen[v] = struct{}{}
			members = append(members, v)
		}
		for i := 0; i < len(members); i++ {
			for j := i + 1; j < len(members); j++ {
				edges = append(edges, edge{from: members[i], to: members[j]}, edge{from: members[j], to: members[i]})
			}
		}
	}
	if len(edges) == 0 {
		return
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].from != edges[j].from {
			return edges[i].from < edges[j].from
		}
		return edges[i].to < edges[j].to
	})
	st.cociteRowPtr = make([]uint32, idx.N+1)
	st.cocitePeers = make([]int32, 0, len(edges))
	cur := 0
	for u := 0; u < idx.N; u++ {
		st.cociteRowPtr[u] = uint32(len(st.cocitePeers))
		for cur < len(edges) && edges[cur].from == int32(u) {
			if n := len(st.cocitePeers); n == 0 || uint32(n) == st.cociteRowPtr[u] || st.cocitePeers[n-1] != edges[cur].to {
				st.cocitePeers = append(st.cocitePeers, edges[cur].to)
			}
			cur++
		}
	}
	st.cociteRowPtr[idx.N] = uint32(len(st.cocitePeers))
}

// coCitationPeersOf returns the evidence units co-cited with u by at least one
// noetic consolidation unit.
func (st *rmClusterState) coCitationPeersOf(u int32) []int32 {
	if st == nil || len(st.cociteRowPtr) == 0 || u < 0 || int(u)+1 >= len(st.cociteRowPtr) {
		return nil
	}
	return st.cocitePeers[st.cociteRowPtr[u]:st.cociteRowPtr[u+1]]
}

// hasCoCitationEdge reports whether units a and b were jointly cited by at
// least one consolidation unit.
func (st *rmClusterState) hasCoCitationEdge(a, b int32) bool {
	peers := st.coCitationPeersOf(a)
	lo, hi := 0, len(peers)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if peers[mid] < b {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo < len(peers) && peers[lo] == b
}

func (st *rmClusterState) citationPeersOf(u int32) []int32 {
	if st == nil || len(st.citeRowPtr) == 0 || u < 0 || int(u)+1 >= len(st.citeRowPtr) {
		return nil
	}
	return st.citePeers[st.citeRowPtr[u]:st.citeRowPtr[u+1]]
}

// hasCitationEdge reports whether units a and b are linked by an explicit
// citation provenance edge (in either direction).
func (st *rmClusterState) hasCitationEdge(a, b int32) bool {
	peers := st.citationPeersOf(a)
	lo, hi := 0, len(peers)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if peers[mid] < b {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo < len(peers) && peers[lo] == b
}

func (st *rmClusterState) buildPrefixTables(n int) {
	tKeys := st.termKeys
	tn := uint32(len(tKeys))
	var tCur uint32
	for b := 0; b < 4096; b++ {
		st.termPrefixPtr[b] = tCur
		for tCur < tn && int(tKeys[tCur]>>52) == b {
			tCur++
		}
	}
	st.termPrefixPtr[4096] = tn

	pKeys := st.phraseKeys
	pn := uint32(len(pKeys))
	var pCur uint32
	for b := 0; b < 4096; b++ {
		st.phrasePrefixPtr[b] = pCur
		for pCur < pn && int(pKeys[pCur].a>>52) == b {
			pCur++
		}
	}
	st.phrasePrefixPtr[4096] = pn

	// 对偶序 (b, a) 索引已删除：它只服务诊断用的 TermEnergyVector 画像，却让每次
	// 摄取与冷启动支付 O(P log P) 重排；b 侧搭档改为按需 O(P) 扫描主序键集。

	dKeys := st.dayCosKeys
	dn := uint32(len(dKeys))
	if dn > 0 && n > 0 {
		st.dayCosRowPtr = make([]uint32, n+1)
		var dCur uint32
		for u := 0; u < n; u++ {
			st.dayCosRowPtr[u] = dCur
			for dCur < dn && int(dKeys[dCur]>>32) == u {
				dCur++
			}
		}
		st.dayCosRowPtr[n] = dn
	}
}

func (st *rmClusterState) lookupTermCol(h uint64) (int, bool) {
	return lookupPrefixTermCol(st.termKeys, &st.termPrefixPtr, h)
}

func (st *rmClusterState) effWeightOf(h uint64) (float64, bool) {
	if st.effWeight != nil {
		w, ok := st.effWeight[h]
		return w, ok
	}
	if col, ok := st.lookupTermCol(h); ok && col < len(st.termEffWeight) {
		return st.termEffWeight[col], true
	}
	return 0, false
}

func (st *rmClusterState) properRatioOf(h uint64) float64 {
	if st.properRatio != nil {
		return st.properRatio[h]
	}
	if col, ok := st.lookupTermCol(h); ok && col < len(st.termProperRatio) {
		return st.termProperRatio[col]
	}
	return 0
}

// parentGroupOf returns the family root used by scoring and partner paths.
// Key-invariance contract: the root is the canonical host (most frequent host
// by df, then widest carrier), so the value does not change when the atomizer
// key relabels hashes. The legacy min-hash map remains only as a fallback for
// states materialized before canonicalParent existed.
func (st *rmClusterState) parentGroupOf(h uint64) (uint64, bool) {
	if st.canonicalParent != nil {
		if g, ok := st.canonicalParent[h]; ok && g != 0 {
			return g, true
		}
	}
	if st.parentGroup != nil {
		pg, ok := st.parentGroup[h]
		return pg, ok
	}
	if col, ok := st.lookupTermCol(h); ok && col < len(st.termParentGroup) {
		pg := st.termParentGroup[col]
		return pg, pg != 0
	}
	return 0, false
}

// canonicalParentOf returns the key-invariant family root for weight/partner
// purposes. parentGroupOf keeps the legacy min-hash contract for the segment
// block and query layers.
func (st *rmClusterState) canonicalParentOf(h uint64) uint64 {
	if st.canonicalParent != nil {
		if g, ok := st.canonicalParent[h]; ok && g != 0 {
			return g
		}
	}
	if st.parentGroup != nil {
		if g, ok := st.parentGroup[h]; ok && g != 0 {
			return g
		}
	}
	return h
}

func (st *rmClusterState) isPrimaryOf(h uint64) bool {
	if st.isPrimaryWord != nil {
		return st.isPrimaryWord[h]
	}
	if col, ok := st.lookupTermCol(h); ok && col < len(st.termIsPrimary) {
		return st.termIsPrimary[col] != 0
	}
	return false
}

func (st *rmClusterState) docEntriesOf(u int32) []rmDocEntry {
	return st.docEntriesFlat[st.docRowPtr[u]:st.docRowPtr[u+1]]
}

func (st *rmClusterState) entryPositionsOf(e rmDocEntry) []uint32 {
	if e.posLen == 0 {
		return nil
	}
	return st.posPool[e.posOff : e.posOff+e.posLen]
}

func (st *rmClusterState) visitNeighbors(u int32, visit func(int32) bool) {
	if st == nil || visit == nil || u < 0 {
		return
	}
	if int(u) >= len(st.ordPos) || len(st.bucketRowPtr) != len(st.bucketTimes)+1 {
		return
	}
	bIdx := int(st.ordPos[u])
	if bIdx < 0 || bIdx >= len(st.bucketTimes) {
		return
	}
	const neighborSpan = 2
	if !st.visitBucketMembers(int32(bIdx), visit) {
		return
	}
	for span := 1; span <= neighborSpan; span++ {
		for _, adj := range [2]int{bIdx - span, bIdx + span} {
			if adj >= 0 && adj < len(st.bucketTimes) && !st.visitBucketMembers(int32(adj), visit) {
				return
			}
		}
	}
	if int(u)+1 < len(st.extraNeighborRowPtr) {
		for i, end := st.extraNeighborRowPtr[u], st.extraNeighborRowPtr[u+1]; i < end; i++ {
			if !st.visitBucketMembers(st.extraNeighborBuckets[i], visit) {
				return
			}
		}
	}
}

func (st *rmClusterState) visitBucketMembers(bucket int32, visit func(int32) bool) bool {
	if bucket < 0 || int(bucket)+1 >= len(st.bucketRowPtr) {
		return true
	}
	for i, end := st.bucketRowPtr[bucket], st.bucketRowPtr[bucket+1]; i < end; i++ {
		if !visit(st.bucketUnits[i]) {
			return false
		}
	}
	return true
}

func (st *rmClusterState) buildBucketMembership(n int) {
	st.bucketRowPtr = make([]uint32, len(st.bucketTimes)+1)
	for u := 0; u < n && u < len(st.ordPos); u++ {
		if bucket := st.ordPos[u]; bucket >= 0 && int(bucket) < len(st.bucketTimes) {
			st.bucketRowPtr[bucket+1]++
		}
	}
	for bucket := 0; bucket < len(st.bucketTimes); bucket++ {
		st.bucketRowPtr[bucket+1] += st.bucketRowPtr[bucket]
	}
	st.bucketUnits = make([]int32, st.bucketRowPtr[len(st.bucketTimes)])
	writeAt := append([]uint32(nil), st.bucketRowPtr[:len(st.bucketTimes)]...)
	for u := 0; u < n && u < len(st.ordPos); u++ {
		if bucket := st.ordPos[u]; bucket >= 0 && int(bucket) < len(st.bucketTimes) {
			st.bucketUnits[writeAt[bucket]] = int32(u)
			writeAt[bucket]++
		}
	}
}

func (st *rmClusterState) isDirectTimeNeighbor(a, b int32) bool {
	if int(a) >= len(st.ordPos) || int(b) >= len(st.ordPos) {
		return false
	}
	oa, ob := st.ordPos[a], st.ordPos[b]
	if oa < 0 || ob < 0 {
		return false
	}
	dk := oa - ob
	if dk < 0 {
		dk = -dk
	}
	return dk <= 2
}

func (st *rmClusterState) phraseBasePostings(pair rmDyad) []int32 {
	keys := st.phraseKeys
	if len(keys) == 0 {
		return nil
	}
	b := pair.a >> 52
	lo, hi := int(st.phrasePrefixPtr[b]), int(st.phrasePrefixPtr[b+1])
	if hi == 0 && st.phrasePrefixPtr[4096] == 0 {
		lo, hi = 0, len(keys)
	}
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		k := keys[mid]
		if k.a < pair.a || (k.a == pair.a && k.b < pair.b) {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo < len(keys) && keys[lo] == pair {
		return st.phraseUnits[st.phraseRowPtr[lo]:st.phraseRowPtr[lo+1]]
	}
	return nil
}

// phraseDeltaRange returns the half-open range of delta postings for pair. The delta is
// sorted by (pair, unit) and holds only units appended since the last compaction.
func (st *rmClusterState) phraseDeltaRange(pair rmDyad) (int, int) {
	d := st.phraseDelta
	if len(d) == 0 {
		return 0, 0
	}
	lo, hi := 0, len(d)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if dyadLess(d[mid].pair, pair) {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	start := lo
	hi = len(d)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if dyadLess(pair, d[mid].pair) {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	return start, lo
}

// phrasePostingCount returns the global posting count of pair (base + delta) without
// materializing the merged view.
func (st *rmClusterState) phrasePostingCount(pair rmDyad) int {
	lo, hi := st.phraseDeltaRange(pair)
	return len(st.phraseBasePostings(pair)) + hi - lo
}

// visitPhrasePostings walks the global postings of pair in ascending unit order without
// materializing a merged slice. fn returns false to stop; the return value is the number
// of visited postings.
func (st *rmClusterState) visitPhrasePostings(pair rmDyad, fn func(unit int32) bool) int {
	base := st.phraseBasePostings(pair)
	lo, hi := st.phraseDeltaRange(pair)
	visited := 0
	i := 0
	for i < len(base) && lo < hi {
		bu, du := base[i], st.phraseDelta[lo].unit
		switch {
		case bu < du:
			visited++
			if !fn(bu) {
				return visited
			}
			i++
		case du < bu:
			visited++
			if !fn(du) {
				return visited
			}
			lo++
		default:
			visited++
			if !fn(bu) {
				return visited
			}
			i++
			lo++
		}
	}
	for ; i < len(base); i++ {
		visited++
		if !fn(base[i]) {
			return visited
		}
	}
	for ; lo < hi; lo++ {
		visited++
		if !fn(st.phraseDelta[lo].unit) {
			return visited
		}
	}
	return visited
}

func diskPostingUnitIterator(st *rmClusterState) func(hash uint64, fn func(unit int32)) {
	return func(hash uint64, fn func(unit int32)) {
		for _, p := range st.termPostings(hash) {
			fn(p.unit)
		}
	}
}

func (st *rmClusterState) termPostings(h uint64) []rmPostingEntry {
	if st.postings != nil {
		return st.postings[h]
	}
	if col, ok := st.lookupTermCol(h); ok {
		return st.postingPool[st.termRowPtr[col]:st.termRowPtr[col+1]]
	}
	return nil
}

func (e *Engine) canUseClusterPipeline() bool {
	if !e.ClusterPipeline {
		return false
	}
	idx := e.Index
	if idx == nil || idx.N == 0 || (len(idx.TermFlags) == 0 && len(idx.rawTermFlags) == 0 && e.clusterState == nil) {
		return false
	}
	// ClusterPipeline is the only mode switch. Hop1Budget/Hop2Budget are
	// fallback-spread work caps: a zero budget disables that hop, not the
	// cluster pipeline. Keeping the budget out of this predicate prevents a
	// silent full scoring-path change when an experiment sets hop1=0.
	if e.Contraction || e.TimeKernel || e.Energy != EnergyMutualInfo {
		return false
	}
	return true
}

func (e *Engine) ensureClusterState() *rmClusterState {
	idx := e.Index
	if e.clusterState != nil {
		if idx == nil || (e.clusterState.builtIndex == idx && e.clusterState.builtIndexGen == idx.stateGen) {
			return e.clusterState
		}
		e.clusterState = nil
		e.queryScratch = nil
	}
	if idx == nil {
		return nil
	}
	// 写路径只累积挂起基座与扣减集，物化在此一次结算为增量扩展。
	if e.prevClusterState == nil && e.pendingBase != nil {
		e.prevClusterState = e.pendingBase
		e.prevDrops = e.pendingDrops
		e.pendingBase = nil
		e.pendingDrops = nil
	}
	inc := e.takeIncrementalDelta(idx)
	var totalDocEntries int
	var totalPositions int
	for u := 0; u < idx.N; u++ {
		if int(u) < len(idx.DocTerms) {
			totalDocEntries += len(idx.DocTerms[u])
		}

		if u < len(idx.UnitPositions) {
			for _, pl := range idx.UnitPositions[u] {
				totalPositions += len(pl)
			}
		}
	}
	st := &rmClusterState{
		builtIndex:      idx,
		builtIndexGen:   idx.stateGen,
		docRowPtr:       make([]uint32, idx.N+1),
		docEntriesFlat:  make([]rmDocEntry, totalDocEntries),
		posPool:         make([]uint32, 0, totalPositions),
		termKeys:        make([]uint64, 0, len(idx.DF)),
		termRowPtr:      make([]uint32, len(idx.DF)+1),
		postingPool:     make([]rmPostingEntry, totalDocEntries),
		effWeight:       make(map[uint64]float64, len(idx.DF)),
		parentGroup:     make(map[uint64]uint64, len(idx.DF)),
		canonicalParent: make(map[uint64]uint64, len(idx.DF)),
		isPrimaryWord:   make(map[uint64]bool, len(idx.DF)),
		clusters:        make([]int32, idx.N),
		refTimes:        make([]int64, idx.N),
	}
	for h := range idx.DF {
		st.termKeys = append(st.termKeys, h)
	}
	sortUint64Asc(st.termKeys)
	{
		tKeys := st.termKeys
		tn := uint32(len(tKeys))
		var tCur uint32
		for b := 0; b < 4096; b++ {
			st.termPrefixPtr[b] = tCur
			for tCur < tn && int(tKeys[tCur]>>52) == b {
				tCur++
			}
		}
		st.termPrefixPtr[4096] = tn
	}

	var docAcc uint32
	for u := 0; u < idx.N; u++ {
		st.docRowPtr[u] = docAcc
		if int(u) < len(idx.DocTerms) {
			docAcc += uint32(len(idx.DocTerms[u]))
		}

		if u < len(idx.UnitMeta) {
			st.refTimes[u] = idx.UnitMeta[u].RefTime()
		}
	}
	st.docRowPtr[idx.N] = docAcc
	docWriteCur := make([]uint32, idx.N)
	copy(docWriteCur, st.docRowPtr[:idx.N])

	// Single-pass radix distribution in ascending st.termKeys order:
	// Because st.termKeys is strictly ascending, each document u receives its
	// atoms in strictly ascending h order without any per-document sort or occCounts map!
	var postAcc uint32
	for i, h := range st.termKeys {
		st.termRowPtr[i] = postAcc
		posts := idx.Postings[h]
		for _, p := range posts {
			u := p.Unit
			dstEntry := docWriteCur[u]
			docWriteCur[u] = dstEntry + 1
			entryIdx := int32(dstEntry - st.docRowPtr[u])
			tf := p.Occ
			if tf < 1 {
				tf = 1
			}
			st.docEntriesFlat[dstEntry] = rmDocEntry{
				id: h,
				tf: tf,
			}
			st.postingPool[postAcc] = rmPostingEntry{unit: u, entry: entryIdx}
			postAcc++
		}
	}
	st.termRowPtr[len(st.termKeys)] = postAcc

	docDyadRowPtr := make([]uint32, idx.N+1)
	docDyadsFlat := make([]rmDyad, 0, totalDocEntries*2)
	var allDyadPostings []dyadPosting
	var occScratch []rmPackedOccurrence
	var dyadScratch []rmDyad
	var posScratch []uint32

	var bitSum float64
	var bitCount int
	for u := 0; u < idx.N; u++ {
		var posMap map[uint64][]TermPosition
		if u < len(idx.UnitPositions) {
			posMap = idx.UnitPositions[u]
		}
		var uBits map[uint64]uint16
		if u < len(idx.UnitBits) {
			uBits = idx.UnitBits[u]
		}
		var uFlags map[uint64]uint8
		if u < len(idx.UnitFlags) {
			uFlags = idx.UnitFlags[u]
		}
		var uRoleExt map[uint64]uint8
		if u < len(idx.UnitRoleExt) {
			uRoleExt = idx.UnitRoleExt[u]
		}
		var uGroups map[uint64]uint64
		if u < len(idx.UnitGroups) {
			uGroups = idx.UnitGroups[u]
		}
		start, end := st.docRowPtr[u], st.docRowPtr[u+1]
		// 幸存单元：条目与位置与上一代逐字节相同，整段复制并按新位置池重定位 posOff，
		// 省掉逐条目的映射查表与位置重新打包（不改变任何排序）。
		if inc != nil && !inc.delta[u] && inc.prev != nil && !inc.prev.hasWidePos() {
			if oldU := inc.oldOf[u]; oldU >= 0 && int(oldU) < len(inc.prev.docRowPtr)-1 {
				pStart, pEnd := inc.prev.docRowPtr[oldU], inc.prev.docRowPtr[oldU+1]
				n := int(pEnd - pStart)
				if n == int(end-start) {
					copy(st.docEntriesFlat[start:end], inc.prev.docEntriesFlat[pStart:pEnd])
					base := uint32(len(st.posPool))
					for k := 0; k < n; k++ {
						en := &st.docEntriesFlat[start+uint32(k)]
						if en.posLen == 0 {
							en.posOff = base
							continue
						}
						st.posPool = append(st.posPool, inc.prev.posPool[en.posOff:en.posOff+en.posLen]...)
						en.posOff = base
						base += en.posLen
					}
					for k := 0; k < n; k++ {
						en := st.docEntriesFlat[start+uint32(k)]
						if en.flags&rmFlagCompanion == 0 {
							st.isPrimaryWord[en.id] = true
						}
						if prevGrp, ok := st.parentGroup[en.id]; !ok || en.group < prevGrp {
							st.parentGroup[en.id] = en.group
						}
						if prevGrp, ok := st.canonicalParent[en.id]; !ok || canonicalHostPreferred(idx, en.group, prevGrp) {
							st.canonicalParent[en.id] = en.group
						}
						if en.bits > 0 {
							bitSum += float64(en.bits)
							bitCount++
						}
					}
					docDyadRowPtr[u] = uint32(len(docDyadsFlat))
					continue
				}
			}
		}
		entries := st.docEntriesFlat[start:end:end]
		for i := range entries {
			h := entries[i].id
			var fl uint8
			if uFlags != nil {
				if f, ok := uFlags[h]; ok {
					fl = f
				} else {
					fl = idx.TermFlags[h]
				}
			} else {
				fl = idx.TermFlags[h]
			}
			var re uint8
			if uRoleExt != nil {
				re = uRoleExt[h]
			}
			bits := idx.TermBits[h]
			if uBits != nil {
				if b, ok := uBits[h]; ok {
					bits = b
				}
			}
			rawPos := posMap[h]
			posOff := uint32(len(st.posPool))
			posLen := uint32(len(rawPos))
			if posLen > 0 {
				hasWide := false
				for _, rp := range rawPos {
					if rp.Sentence < 0 || rp.Sentence > 0xFFFF || rp.Slot < 0 || rp.Slot > 0xFFFF {
						hasWide = true
						break
					}
				}
				if !hasWide {
					posScratch = posScratch[:0]
					for _, rp := range rawPos {
						posScratch = append(posScratch, packTermPosition(rp.Sentence, rp.Slot))
					}
					if len(posScratch) > 1 {
						sortUint32Asc(posScratch)
					}
					st.posPool = append(st.posPool, posScratch...)
				} else {
					sortedRaw := append([]TermPosition(nil), rawPos...)
					sort.Slice(sortedRaw, func(a, b int) bool {
						pa := packTermPosition(sortedRaw[a].Sentence, sortedRaw[a].Slot)
						pb := packTermPosition(sortedRaw[b].Sentence, sortedRaw[b].Slot)
						if pa != pb {
							return pa < pb
						}
						if sortedRaw[a].Sentence != sortedRaw[b].Sentence {
							return sortedRaw[a].Sentence < sortedRaw[b].Sentence
						}
						return sortedRaw[a].Slot < sortedRaw[b].Slot
					})
					if st.widePos == nil {
						st.widePos = make(map[uint32]TermPosition, len(sortedRaw))
					}
					for pi, rp := range sortedRaw {
						pIdx := uint32(len(st.posPool))
						st.posPool = append(st.posPool, packTermPosition(rp.Sentence, rp.Slot))
						if rp.Sentence < 0 || rp.Sentence > 0xFFFF || rp.Slot < 0 || rp.Slot > 0xFFFF {
							st.widePos[posOff+uint32(pi)] = rp
							_ = pIdx
						}
					}
				}
			}
			group := h
			if uGroups != nil && uGroups[h] != 0 {
				group = uGroups[h]
			}
			if fl&rmFlagCompanion == 0 {
				st.isPrimaryWord[h] = true
			}
			if prev, ok := st.parentGroup[h]; !ok || group < prev {
				st.parentGroup[h] = group
			}
			if prev, ok := st.canonicalParent[h]; !ok || canonicalHostPreferred(idx, group, prev) {
				st.canonicalParent[h] = group
			}
			if bits > 0 {
				bitSum += float64(bits)
				bitCount++
			}
			entries[i].group = group
			entries[i].posOff = posOff
			entries[i].posLen = posLen
			entries[i].bits = bits
			entries[i].flags = fl
			entries[i].roleExt = re
		}
		docDyadRowPtr[u] = uint32(len(docDyadsFlat))
		if inc != nil && !inc.delta[u] {
			// 增量扩展：旧单元的短语倒排由旧状态复用，只有追加单元需要现场抽取。
			continue
		}
		if st.hasWidePos() {
			dyadScratch = st.extractDocDyadsExactInto(entries, dyadScratch[:0])
		} else {
			dyadScratch, occScratch = st.extractDocDyadsInto(entries, occScratch, dyadScratch[:0])
		}
		docDyadsFlat = append(docDyadsFlat, dyadScratch...)
	}
	docDyadRowPtr[idx.N] = uint32(len(docDyadsFlat))
	st.entryMass = make([]float64, len(st.docEntriesFlat))
	for i := range st.docEntriesFlat {
		st.entryMass[i] = idx.Mass[st.docEntriesFlat[i].id]
	}

	// Postings are one per doc dyad in the same order: size the posting slice
	// exactly from the finished docDyadsFlat pass instead of growing both
	// arrays in lockstep (the exponential growth re-copied gigabytes).
	allDyadPostings = make([]dyadPosting, 0, len(docDyadsFlat))
	for u := 0; u < idx.N; u++ {
		for _, pair := range docDyadsFlat[docDyadRowPtr[u]:docDyadRowPtr[u+1]] {
			allDyadPostings = append(allDyadPostings, dyadPosting{pair: pair, unit: int32(u)})
		}
	}

	// Build flat CSR for intra-sentence PhraseDyad postings: in-place 1024-bucket
	// MSB radix partition on pair.a >> 54 followed by a per-bucket sort (see
	// radixSortDyadPostings). In the append-only fast path only delta postings
	// exist here and the old CSR is reused through a two-way merge.
	if inc != nil {
		sort.Sort(dyadPostingSort(allDyadPostings))
		if appendOnlyIncremental(inc) {
			delta := mergeDyadPostings(inc.prev.phraseDelta, allDyadPostings)
			if phraseDeltaOverBudget(len(delta), inc.prev) {
				st.phraseKeys, st.phraseRowPtr, st.phraseUnits = mergePhraseCSRWithDelta(inc.prev, inc.remap, allDyadPostings)
			} else {
				st.phraseKeys, st.phraseRowPtr, st.phraseUnits = inc.prev.phraseKeys, inc.prev.phraseRowPtr, inc.prev.phraseUnits
				st.phraseDelta = delta
			}
		} else {
			st.phraseKeys, st.phraseRowPtr, st.phraseUnits = mergePhraseCSRWithDelta(inc.prev, inc.remap, allDyadPostings)
		}
	} else if len(allDyadPostings) > 0 {
		radixSortDyadPostings(allDyadPostings)
		uniqueKeys := 1
		for i := 1; i < len(allDyadPostings); i++ {
			if allDyadPostings[i].pair != allDyadPostings[i-1].pair {
				uniqueKeys++
			}
		}
		st.phraseKeys = make([]rmDyad, 0, uniqueKeys)
		st.phraseRowPtr = make([]uint32, 0, uniqueKeys+1)
		st.phraseUnits = make([]int32, len(allDyadPostings))
		for i, dp := range allDyadPostings {
			if i == 0 || dp.pair != allDyadPostings[i-1].pair {
				st.phraseKeys = append(st.phraseKeys, dp.pair)
				st.phraseRowPtr = append(st.phraseRowPtr, uint32(i))
			}
			st.phraseUnits[i] = dp.unit
		}
		st.phraseRowPtr = append(st.phraseRowPtr, uint32(len(allDyadPostings)))
	} else {
		st.phraseRowPtr = []uint32{0}
	}
	allDyadPostings = nil

	if bitCount > 0 {
		st.entryMeanBits = bitSum / float64(bitCount)
	} else {
		st.entryMeanBits = idx.MeanBits
	}

	// Build SpacetimeCluster exact-timestamp buckets and neighbor bands (span=2).
	type tsBucket struct {
		time  int64
		units []int32
	}
	byTime := make(map[int64][]int32)
	for u := 0; u < idx.N; u++ {
		st.clusters[u] = -1
		if ts := st.refTimes[u]; ts > 0 {
			byTime[ts] = append(byTime[ts], int32(u))
		}
	}
	buckets := make([]tsBucket, 0, len(byTime))
	for ts, units := range byTime {
		buckets = append(buckets, tsBucket{time: ts, units: units})
	}
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].time < buckets[j].time })
	st.ordPos = make([]int32, idx.N)
	for u := range st.ordPos {
		st.ordPos[u] = -1
	}
	// 对数间隔矩按 Welford 在线递推：朴素 Σx/n − (Σx/n)² 在等间隔退化输入下
	// 的累加舍入会随桶数增长（实测 n=2e4 时 μ 偏 5e-6、经 Expm1 放大为 7.7e-5），
	// 使情节切分的严格大于比较由舍入误差决胜；Welford 对全等输入逐位返回该值
	// 本身，退化输入的 μ+σ 与间隔在比较中精确相等。
	var nG int
	for i := 1; i < len(buckets); i++ {
		gap := float64(buckets[i].time - buckets[i-1].time)
		st.meanGap += (gap - st.meanGap) / float64(i)
		nG++
		lg := math.Log1p(gap)
		delta := lg - st.muLogGap
		st.muLogGap += delta / float64(nG)
		st.sigLogGap += delta * (lg - st.muLogGap)
	}
	if nG > 0 {
		st.sigLogGap = math.Sqrt(math.Max(0, st.sigLogGap/float64(nG)))
	}
	st.clusterN = len(buckets)
	st.bucketTimes = make([]int64, len(buckets))
	for i, b := range buckets {
		st.bucketTimes[i] = b.time
		for _, u := range b.units {
			st.clusters[u] = int32(i)
			st.ordPos[u] = int32(i)
		}
	}
	for u := 0; u < idx.N; u++ {
		if st.clusters[u] < 0 {
			st.clusters[u] = int32(st.clusterN)
			st.clusterN++
		}
	}
	// Episodic components: endogenous gap-split over the bucket timeline.
	// Shared derivation with the disk-hydration path (csr_disk.go) so both
	// state builders agree bit-exactly without serializing a derived column.
	st.episodeOf, st.episodeCount = buildEpisodeComponents(st.bucketTimes, st.ordPos, idx.N, st.meanGap, st.muLogGap, st.sigLogGap)
	// Base spacetime neighbors are an exact projection of ordered timestamp
	// bucket membership. Store each unit once and derive the legacy ±2-bucket
	// sequence in visitNeighbors instead of copying the same bucket rows into
	// every member unit.
	st.buildBucketMembership(idx.N)

	// Build effective family weights: the append-only fast path extends the
	// previous compressed statistics with delta contributions, the full path
	// rebuilds them from the pre-extracted docDyadsFlat.
	if inc != nil {
		inc.prev.agg.extendForDelta(st, idx, inc, docDyadRowPtr, docDyadsFlat)
		st.agg = inc.prev.agg
		st.fillEffWeightFromAggregates(idx, st.agg)
	} else {
		st.buildEffectiveWeightsFromDyads(idx, docDyadRowPtr, docDyadsFlat)
	}
	docDyadsFlat = nil
	docDyadRowPtr = nil

	const rmProperMask uint8 = (1 << 0) | (1 << 1)
	vLen := len(st.termKeys)
	st.termEffWeight = make([]float64, vLen)
	st.termProperRatio = make([]float64, vLen)
	st.termParentGroup = make([]uint64, vLen)
	st.termIsPrimary = make([]uint8, vLen)
	for i, h := range st.termKeys {
		st.termEffWeight[i] = st.effWeight[h]
		st.termParentGroup[i] = st.canonicalParentOf(h)
		if st.isPrimaryWord[h] {
			st.termIsPrimary[i] = 1
		}
		posts := st.postingPool[st.termRowPtr[i]:st.termRowPtr[i+1]]
		if len(posts) == 0 {
			continue
		}
		propCnt := 0
		for _, p := range posts {
			if st.docEntriesFlat[st.docRowPtr[p.unit]+uint32(p.entry)].flags&rmProperMask != 0 {
				propCnt++
			}
		}
		if propCnt > 0 {
			st.termProperRatio[i] = float64(propCnt) / float64(len(posts))
		}
	}
	st.effWeight = nil
	st.parentGroup = nil
	st.isPrimaryWord = nil

	st.sentenceEnergyMean = st.calcMeanSentenceEnergy(idx)
	// Provenance (and citation) CSR plus envelope DF counts must exist before
	// buildFastDocs: the day-cos precompute inside it consults the
	// citation-edge floor and the collision-surprisal-scaled envelope floor.
	st.buildEnvelopeDF(idx)
	st.buildProvenanceCSR(idx)
	st.buildFastDocs(idx)
	if len(st.dayCos) > 0 {
		type dayCosPair struct {
			key uint64
			val float64
		}
		pairs := make([]dayCosPair, 0, len(st.dayCos))
		for k, val := range st.dayCos {
			if val > 0 {
				pairs = append(pairs, dayCosPair{key: k, val: val})
			}
		}
		sort.Slice(pairs, func(i, j int) bool { return pairs[i].key < pairs[j].key })
		st.dayCosKeys = make([]uint64, len(pairs))
		st.dayCosVals = make([]float64, len(pairs))
		for i, kv := range pairs {
			st.dayCosKeys[i] = kv.key
			st.dayCosVals[i] = kv.val
		}
		st.dayCos = nil
	} else {
		st.dayCos = nil
	}
	st.buildPrefixTables(idx.N)
	if len(idx.TermKeys) == 0 && vLen > 0 {
		idx.TermKeys = st.termKeys
		idx.TermPrefixPtr = st.termPrefixPtr
		idx.rawTermDF = make([]int32, vLen)
		idx.rawTermMass = make([]float64, vLen)
		idx.rawTermBits = make([]uint16, vLen)
		idx.rawTermFlags = make([]uint8, vLen)
		for i, h := range st.termKeys {
			idx.rawTermDF[i] = int32(idx.TermDF(h))
			m, _ := idx.TermMass(h)
			idx.rawTermMass[i] = m
			idx.rawTermBits[i] = idx.TermBitsOf(h)
			idx.rawTermFlags[i] = idx.TermFlagsOf(h)
		}
	}
	idx.csrFallbackExport = e.ExportDocs
	idx.csrPostingUnits = diskPostingUnitIterator(st)
	e.clusterState = st
	return st
}

func (st *rmClusterState) buildFastDocs(idx *Index) {
	st.fastDocs = make([]rmFastDocStruct, idx.N)
	st.subjRowPtr = make([]uint32, idx.N+1)
	st.subjIDsPool = make([]uint64, 0, len(st.docEntriesFlat))
	st.subjWeightsPool = make([]float64, 0, len(st.docEntriesFlat))
	activeDays := make(map[int64]struct{}, 256)
	highDFThresh := idx.HighDFThreshold()
	var docGW []rmStructGroupW
	for u := 0; u < idx.N; u++ {
		st.subjRowPtr[u] = uint32(len(st.subjIDsPool))
		entries := st.docEntriesOf(int32(u))
		docGW = docGW[:0]
		var subjNormSq float64
		base := st.docRowPtr[u]
		for ei, entry := range entries {
			w := st.entryMass[base+uint32(ei)]
			if math.IsNaN(w) || math.IsInf(w, 0) || w <= 0 {
				continue
			}
			w2 := w * w
			st.subjIDsPool = append(st.subjIDsPool, entry.id)
			st.subjWeightsPool = append(st.subjWeightsPool, w2)
			// Explicit FMA keeps the norm bit-identical across architectures:
			// arm64 otherwise fuses this accumulation on its own and drifts one
			// ULP from the amd64 rounding, which breaks cross-platform parity.
			subjNormSq = math.FMA(w, w, subjNormSq)
			g := effectiveEntryGroup(entry, idx, highDFThresh)
			docGW = append(docGW, rmStructGroupW{group: g, weight: w})
		}
		st.subjRowPtr[u+1] = uint32(len(st.subjIDsPool))
		if len(docGW) > 1 {
			sortStructGroupW(docGW)
		}
		var docTotal float64
		for i := 0; i < len(docGW); {
			g := docGW[i].group
			maxW := docGW[i].weight
			i++
			for i < len(docGW) && docGW[i].group == g {
				if docGW[i].weight > maxW {
					maxW = docGW[i].weight
				}
				i++
			}
			docTotal += maxW
		}
		const noDay int64 = -1 << 62
		day := noDay
		if ts := st.refTimes[u]; ts > 0 {
			day = ts / 86400
			activeDays[day] = struct{}{}
		}
		st.fastDocs[u] = rmFastDocStruct{
			docTotal:   docTotal,
			day:        day,
			subjNormSq: subjNormSq,
		}
	}
	st.totalDaysM = float64(len(activeDays))
	if st.totalDaysM < 1 {
		st.totalDaysM = 1
	}
	// kappaOrd is a state-level loop invariant of temporalAffinity (identical
	// formula and inputs as the per-call computation it replaces, so the value
	// is bit-for-bit the same); building it here keeps the hot pair loop free
	// of a per-call Sqrt/Max.
	st.kappaOrd = eulerNumber * math.Sqrt(math.Max(1.0, float64(st.clusterN)/st.totalDaysM))
	// 日余弦是查询无关但随 N 变化的派生量：预计算需要遍历全部邻接对，而每次写入都会
	// 改变 N。这里不再按代预生成，改由 docSubjectCos 在查询内按需计算（同一函数、数值
	// 完全一致，按查询的 scratch 有界缓存），发布只维护事实与计数。
	// Preserve orthogonal-interleaver transparency as sparse extra bucket edges:
	// the base bucket sequence stays shared, while only coherent buckets beyond
	// the legacy ±2 band are recorded per unit.
	st.buildOrthogonalTransparentExtras(idx)
}

func (st *rmClusterState) buildOrthogonalTransparentExtras(idx *Index) {
	n := idx.N
	st.extraNeighborRowPtr = make([]uint32, n+1)
	st.extraNeighborBuckets = nil
	if st.clusterN <= 0 || len(st.bucketTimes) == 0 {
		return
	}
	// neighborSpan 是桶时间线上的局部近邻窗口（±2 桶）：工程局部性边界，无分布假设推导。
	const neighborSpan = 2
	const orthoFloor = transmissionPerHop * transmissionPerHop
	subFloor := 1.0 - transmissionPerHop
	lookupCos := func(a, b int32) float64 {
		if a == b {
			return 1.0
		}
		return st.docSubjectCos(idx, nil, a, b)
	}
	for u := 0; u < n; u++ {
		st.extraNeighborRowPtr[u] = uint32(len(st.extraNeighborBuckets))
		bIdx := int(st.ordPos[u])
		if bIdx < 0 || bIdx >= len(st.bucketTimes) {
			continue
		}
		epU := st.episodeOf[u]
		if epU < 0 {
			continue
		}
		for _, dir := range [2]int{-1, 1} {
			coherentCnt := 0
			orthoSkipped := 0
			for step := 1; step <= 2*neighborSpan && coherentCnt < neighborSpan; step++ {
				adj := bIdx + dir*step
				if adj < 0 || adj >= len(st.bucketTimes) || st.bucketRowPtr[adj] == st.bucketRowPtr[adj+1] {
					break
				}
				v0 := st.bucketUnits[st.bucketRowPtr[adj]]
				if st.episodeOf[v0] != epU || st.temporalAffinity(idx, int32(u), v0) < subFloor {
					break
				}
				hasCoherent := false
				st.visitBucketMembers(int32(adj), func(v int32) bool {
					if lookupCos(int32(u), v) >= orthoFloor {
						hasCoherent = true
						return false
					}
					return true
				})
				if hasCoherent {
					coherentCnt++
					if step > neighborSpan {
						st.extraNeighborBuckets = append(st.extraNeighborBuckets, int32(adj))
					}
				} else {
					orthoSkipped++
					if orthoSkipped > neighborSpan {
						break
					}
				}
			}
		}
	}
	st.extraNeighborRowPtr[n] = uint32(len(st.extraNeighborBuckets))
}

// maxQueryDayCosEntries bounds the per-scratch day-cosine memo. The memo is a
// pure cache: dropping it only recomputes exact values. 1<<19 entries is about
// 32 MiB at the map's per-entry cost.
const maxQueryDayCosEntries = 1 << 19

func (st *rmClusterState) docSubjectCos(idx *Index, sc *rmQueryScratch, a, b int32) float64 {
	// The warm domain only chooses which pairs are cached. A missing entry must
	// be computed by the same function as an out-of-domain pair; returning zero
	// on a cache miss would change ranking semantics.
	if ep := st.episodeOf[a]; (ep >= 0 && ep == st.episodeOf[b]) || st.isDirectTimeNeighbor(a, b) {
		x, y := uint32(a), uint32(b)
		if x > y {
			x, y = y, x
		}
		key := (uint64(x) << 32) | uint64(y)
		if st.dayCos != nil {
			if val, ok := st.dayCos[key]; ok {
				return val
			}
		}
		if len(st.dayCosKeys) > 0 {
			lo, hi := 0, len(st.dayCosKeys)
			if int(x)+1 < len(st.dayCosRowPtr) {
				lo, hi = int(st.dayCosRowPtr[x]), int(st.dayCosRowPtr[x+1])
			}
			for lo < hi {
				mid := int(uint(lo+hi) >> 1)
				if st.dayCosKeys[mid] < key {
					lo = mid + 1
				} else {
					hi = mid
				}
			}
			if lo < len(st.dayCosKeys) && st.dayCosKeys[lo] == key {
				return st.dayCosVals[lo]
			}
		}
		if sc != nil {
			if val, ok := sc.dayCos[key]; ok {
				return val
			}
		}
		val := st.computeDocSubjectCos(idx, a, b)
		if st.dayCos != nil {
			st.dayCos[key] = val
		} else if sc != nil {
			if sc.dayCos == nil {
				sc.dayCos = make(map[uint64]float64)
			} else if len(sc.dayCos) >= maxQueryDayCosEntries {
				clear(sc.dayCos)
			}
			sc.dayCos[key] = val
		}
		return val
	}
	return st.computeDocSubjectCos(idx, a, b)
}

// docSubjectCosLocal is the worker-local variant used by the parallel
// intra-event endorsement pass. It keeps the warm-domain episode/time-neighbor
// cache in the caller's scratch and never touches the shared st.dayCos map, so
// concurrent workers cannot race; the value is computed by the same
// computeDocSubjectCos and is bit-identical.
func (st *rmClusterState) docSubjectCosLocal(idx *Index, sc *rmQueryScratch, a, b int32) float64 {
	if ep := st.episodeOf[a]; (ep >= 0 && ep == st.episodeOf[b]) || st.isDirectTimeNeighbor(a, b) {
		x, y := uint32(a), uint32(b)
		if x > y {
			x, y = y, x
		}
		key := (uint64(x) << 32) | uint64(y)
		if sc != nil {
			if val, ok := sc.dayCos[key]; ok {
				return val
			}
		}
		val := st.computeDocSubjectCos(idx, a, b)
		if sc != nil {
			if sc.dayCos == nil {
				sc.dayCos = make(map[uint64]float64)
			} else if len(sc.dayCos) >= maxQueryDayCosEntries {
				clear(sc.dayCos)
			}
			sc.dayCos[key] = val
		}
		return val
	}
	return st.computeDocSubjectCos(idx, a, b)
}

// buildEnvelopeDF counts, per distinct envelope commitment key (AuthorHash,
// DocHash, BlockHash), how many units carry it. The count feeds the
// null-hypothesis collision surprisal of "two units share this key", so the
// subject-cosine floor scales with the key's corpus-level discriminative
// power instead of firing unconditionally. The same pass accumulates the mean
// declared line span (LineEnd - LineStart + 1) over units carrying physical
// line coordinates; it is the natural length unit of the line-distance
// proximity kernel between same-document slices.
func (st *rmClusterState) buildEnvelopeDF(idx *Index) {
	st.authorDF = nil
	st.meanLineSpan = 0
	if idx == nil || len(idx.UnitMeta) == 0 {
		return
	}
	df := make(map[uint64]int32, 64)
	spanSum, spanN := 0.0, 0
	for u := 0; u < idx.N && u < len(idx.UnitMeta); u++ {
		m := idx.UnitMeta[u]
		if m.AuthorHash != 0 {
			df[m.AuthorHash]++
		}
		if m.DocHash != 0 {
			df[m.DocHash]++
		}
		if m.BlockHash != 0 && m.BlockHash != m.DocHash {
			df[m.BlockHash]++
		}
		if m.LineStart > 0 && m.LineEnd >= m.LineStart {
			spanSum += float64(m.LineEnd - m.LineStart + 1)
			spanN++
		}
	}
	if len(df) > 0 {
		st.authorDF = df
	}
	if spanN > 0 {
		st.meanLineSpan = spanSum / float64(spanN)
	}
}

// envelopeKeyGain is the normalized collision surprisal of one shared
// envelope key, -ln(df/N) / ln(N). Since 1 <= df <= N, the value lies in
// [0, 1] by construction: a key carried by every unit (df == N) earns zero,
// and a key carried by a single unit earns the full ln(N). The normalizer is
// the largest attainable surprisal, not a scale-free quantity: at a fixed
// share df/N the gain falls as N grows (0.5 at N=100 and 0.25 at N=10^4 for a
// 10% share), while at fixed df it rises toward 1.
func (st *rmClusterState) envelopeKeyGain(key uint64, n int) float64 {
	if key == 0 || n <= 1 || st.authorDF == nil {
		return 0
	}
	df := st.authorDF[key]
	if df <= 0 {
		return 0
	}
	return -math.Log(float64(df)/float64(n)) / math.Log(float64(n))
}

func (st *rmClusterState) computeDocSubjectCos(idx *Index, a, b int32) float64 {
	var floor float64
	if int(a) < len(idx.UnitMeta) && int(b) < len(idx.UnitMeta) {
		ma, mb := idx.UnitMeta[a], idx.UnitMeta[b]
		var gain float64
		if ma.AuthorHash != 0 && ma.AuthorHash == mb.AuthorHash {
			gain = st.envelopeKeyGain(ma.AuthorHash, idx.N)
		}
		if ma.BlockHash != 0 && ma.BlockHash == mb.BlockHash {
			if g := st.envelopeKeyGain(ma.BlockHash, idx.N); g > gain {
				gain = g
			}
		}
		if ma.DocHash != 0 && ma.DocHash == mb.DocHash {
			g := st.envelopeKeyGain(ma.DocHash, idx.N)
			// Declared line-coordinate continuity: two slices of one physical
			// document interpolate from the plain shared-DocHash gain (distant
			// slices, exp term → 0) up to a full pairwise declaration (adjacent
			// or overlapping line spans, gap = 0 ⟹ gain → 1, the same strength
			// as an explicit citation edge, because contiguous text is the
			// strongest provenance statement a writer can make). The gap is
			// measured in units of e * meanLineSpan, matching the spacetime
			// kernel exp(-Δt/(e·τ̄)); absent line coordinates it degenerates to
			// the existing uniform gain (never below current behavior).
			if st.meanLineSpan > 0 &&
				ma.LineStart > 0 && ma.LineEnd >= ma.LineStart &&
				mb.LineStart > 0 && mb.LineEnd >= mb.LineStart {
				var gap float64
				if mb.LineStart > ma.LineEnd {
					gap = float64(mb.LineStart - ma.LineEnd - 1)
				} else if ma.LineStart > mb.LineEnd {
					gap = float64(ma.LineStart - mb.LineEnd - 1)
				}
				g += (1.0 - g) * math.Exp(-gap/(eulerNumber*st.meanLineSpan))
			}
			if g > gain {
				gain = g
			}
		}
		// A citation edge is an explicit pairwise provenance declaration, not
		// a shared key with corpus-level extension, so it keeps the full floor.
		// A co-citation pair was asserted inside one CitedUnitIDs record — the
		// writer declared both units jointly supported one consolidated answer
		// — hence it carries the same declarative strength.
		if st.hasCitationEdge(a, b) || st.hasCoCitationEdge(a, b) {
			gain = 1
		}
		floor = transmissionPerHop * gain
	}
	return st.subjRowView(a).CosineWithFloor(st.subjRowView(b), floor)
}

type rmOccurrence struct {
	id  uint64
	pos TermPosition
}

type rmPackedOccurrence struct {
	pos uint32
	id  uint64
}

// rmPackedOccurrenceSort orders packed (sentence<<16|slot, term hash) rows
// ascending with direct dispatch; the packed key preserves (sentence, slot)
// lexicographic order because slot occupies the low 16 bits.
type rmPackedOccurrenceSort []rmPackedOccurrence

func (s rmPackedOccurrenceSort) Len() int { return len(s) }
func (s rmPackedOccurrenceSort) Less(i, j int) bool {
	if s[i].pos != s[j].pos {
		return s[i].pos < s[j].pos
	}
	return s[i].id < s[j].id
}
func (s rmPackedOccurrenceSort) Swap(i, j int) { s[i], s[j] = s[j], s[i] }

// rmPackedOccurrenceCompare orders by packed position then term id.
func rmPackedOccurrenceCompare(a, b rmPackedOccurrence) int {
	if a.pos != b.pos {
		if a.pos < b.pos {
			return -1
		}
		return 1
	}
	if a.id < b.id {
		return -1
	}
	if a.id > b.id {
		return 1
	}
	return 0
}

// maxPhraseSlotSpan 定义同句二元短语（dyad）索引的最大词槽跨度：
// 相邻词槽距离为 1，中间最多允许隔 3 个修饰/功能词槽（总跨度 4），控制短语倒排体积 O(L*W)。
// [calibrated] maxPhraseSlotSpan — slot-distance span (slots) of the intra-sentence
// dyad window. An empirical operating point: a span sweep put the resonance peak at 4,
// and both directions were measured to be worse — narrower drops real modifier gaps,
// wider admits accidental co-occurrence across a clause. Change it only together with
// a paired multi-corpus measurement.
const maxPhraseSlotSpan = 4

// hasWidePos 报告语料是否存在超出 16 位打包范围的宽 (句, 槽) 坐标：存在时
// 位置敏感算子（同句邻近、最小槽距）必须改走精确坐标分支，防止 16 位钳位把远端位置折叠为同句相邻。
func (st *rmClusterState) hasWidePos() bool {
	return st != nil && len(st.widePos) > 0
}

// entryPositionsExactOf 返回 entry 的精确 (句, 槽) 坐标表（升序）：
// 宽坐标经 widePos 还原，其余由打包值解包；仅在 hasWidePos 语料上调用（有分配）。
func (st *rmClusterState) entryPositionsExactOf(e rmDocEntry) []TermPosition {
	base := st.posPool[e.posOff : e.posOff+e.posLen]
	if len(base) == 0 {
		return nil
	}
	out := make([]TermPosition, len(base))
	for i, p := range base {
		if wp, ok := st.widePos[e.posOff+uint32(i)]; ok {
			out[i] = wp
			continue
		}
		out[i] = TermPosition{Sentence: int(p >> 16), Slot: int(p & 0xFFFF)}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Sentence != out[j].Sentence {
			return out[i].Sentence < out[j].Sentence
		}
		return out[i].Slot < out[j].Slot
	})
	return out
}

// entryPositionsExactInto 是 entryPositionsExactOf 的池化变体：把精确 (句, 槽) 坐标
// 追加进 out（追加段升序）并返回增长后的池；仅在 hasWidePos 语料上调用。
func (st *rmClusterState) entryPositionsExactInto(e rmDocEntry, out []TermPosition) []TermPosition {
	base := len(out)
	raw := st.posPool[e.posOff : e.posOff+e.posLen]
	if len(raw) == 0 {
		return out
	}
	for i, p := range raw {
		if wp, ok := st.widePos[e.posOff+uint32(i)]; ok {
			out = append(out, wp)
			continue
		}
		out = append(out, TermPosition{Sentence: int(p >> 16), Slot: int(p & 0xFFFF)})
	}
	if len(out)-base > 1 {
		sort.Sort(termPositionSort(out[base:]))
	}
	return out
}

// entrySentExactAt 返回 entry 第 i 个位置的精确句号（宽坐标经 widePos 还原）：
// 句号维度的读取在宽语料上不得使用钳位值，否则所有超界句会并成同一个活跃句桶。
func (st *rmClusterState) entrySentExactAt(e rmDocEntry, i int) int {
	idx := e.posOff + uint32(i)
	if wp, ok := st.widePos[idx]; ok {
		return wp.Sentence
	}
	return int(st.posPool[idx] >> 16)
}

// closestExactSlot 与 closestPackedSlot 同构（输入按 (句, 槽) 升序），
// 以精确坐标工作；仅宽语料分支调用，直接委托同包 closestTermSlot。
func closestExactSlot(left, right []TermPosition) int {
	return closestTermSlot(left, right)
}

func (st *rmClusterState) extractDocDyadsInto(entries []rmDocEntry, occs []rmPackedOccurrence, out []rmDyad) ([]rmDyad, []rmPackedOccurrence) {
	occs = occs[:0]
	for _, entry := range entries {
		for _, p := range st.entryPositionsOf(entry) {
			occs = append(occs, rmPackedOccurrence{pos: p, id: entry.id})
		}
	}
	if len(occs) < 2 {
		return out[:0], occs
	}
	slices.SortFunc(occs, rmPackedOccurrenceCompare)
	out = out[:0]
	const maxSpan = uint32(maxPhraseSlotSpan)
	for i, a := range occs {
		aSent, aSlot := a.pos>>16, a.pos&0xFFFF
		for _, b := range occs[i+1:] {
			bSent, bSlot := b.pos>>16, b.pos&0xFFFF
			if bSent != aSent || bSlot-aSlot > maxSpan {
				break
			}
			if a.id == b.id || aSlot == bSlot {
				continue
			}
			left, right := a.id, b.id
			if left > right {
				left, right = right, left
			}
			out = append(out, rmDyad{a: left, b: right})
		}
	}
	if len(out) <= 1 {
		return out, occs
	}
	return sortAndDedupRmDyads(out), occs
}

// rmExactOccurrence 是宽坐标语料上一次 (句, 槽, 词项) 出现的精确坐标记录。
type rmExactOccurrence struct {
	sent, slot int32
	id         uint64
}

// extractDocDyadsExactInto 是 extractDocDyadsInto 的精确坐标分支：仅当语料携带宽
// (句, 槽) 坐标时启用，用宽坐标原值做同句邻近与共现对判定（打包钳位会把远端句折叠
// 成同句并伪造邻近关系）；窄语料永不进入此路径，产出与打包路径逐位同构。
func (st *rmClusterState) extractDocDyadsExactInto(entries []rmDocEntry, out []rmDyad) []rmDyad {
	var occs []rmExactOccurrence
	for _, entry := range entries {
		for _, tp := range st.entryPositionsExactOf(entry) {
			occs = append(occs, rmExactOccurrence{sent: int32(tp.Sentence), slot: int32(tp.Slot), id: entry.id})
		}
	}
	if len(occs) < 2 {
		return out[:0]
	}
	sort.Slice(occs, func(i, j int) bool {
		a, b := occs[i], occs[j]
		if a.sent != b.sent {
			return a.sent < b.sent
		}
		if a.slot != b.slot {
			return a.slot < b.slot
		}
		return a.id < b.id
	})
	out = out[:0]
	const maxSpan = int32(maxPhraseSlotSpan)
	for i, a := range occs {
		for _, b := range occs[i+1:] {
			if b.sent != a.sent || b.slot-a.slot > maxSpan {
				break
			}
			if a.id == b.id || a.slot == b.slot {
				continue
			}
			left, right := a.id, b.id
			if left > right {
				left, right = right, left
			}
			out = append(out, rmDyad{a: left, b: right})
		}
	}
	if len(out) <= 1 {
		return out
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].a != out[j].a {
			return out[i].a < out[j].a
		}
		return out[i].b < out[j].b
	})
	w := 1
	for r := 1; r < len(out); r++ {
		if out[r] != out[w-1] {
			out[w] = out[r]
			w++
		}
	}
	return out[:w]
}

func lessRmOccurrence(a, b rmOccurrence) bool {
	if a.pos.Sentence != b.pos.Sentence {
		return a.pos.Sentence < b.pos.Sentence
	}
	if a.pos.Slot != b.pos.Slot {
		return a.pos.Slot < b.pos.Slot
	}
	return a.id < b.id
}

func sortRmOccurrences(s []rmOccurrence) {
	if len(s) <= 32 {
		for i := 1; i < len(s); i++ {
			v := s[i]
			j := i - 1
			for j >= 0 && lessRmOccurrence(v, s[j]) {
				s[j+1] = s[j]
				j--
			}
			s[j+1] = v
		}
		return
	}
	sort.Sort(rmOccurrenceSort(s))
}

func lessRmDyad(x, y rmDyad) bool {
	if x.a != y.a {
		return x.a < y.a
	}
	return x.b < y.b
}

func sortAndDedupRmDyads(pairs []rmDyad) []rmDyad {
	if len(pairs) <= 1 {
		return pairs
	}
	if len(pairs) <= 64 {
		for i := 1; i < len(pairs); i++ {
			v := pairs[i]
			j := i - 1
			for j >= 0 && lessRmDyad(v, pairs[j]) {
				pairs[j+1] = pairs[j]
				j--
			}
			pairs[j+1] = v
		}
	} else {
		slices.SortFunc(pairs, rmDyadCompare)
	}
	w := 1
	for i := 1; i < len(pairs); i++ {
		if pairs[i] != pairs[w-1] {
			pairs[w] = pairs[i]
			w++
		}
	}
	return pairs[:w]
}

func rmQueryDyadsWithPositionsInto(sc *rmQueryScratch, qTerms map[uint64]Term, qPositions map[uint64][]TermPosition) []rmDyad {
	var occs []rmOccurrence
	if sc != nil {
		occs = sc.queryOccs[:0]
	}
	for h, atom := range qTerms {
		if len(qPositions) > 0 {
			if posList := qPositions[h]; len(posList) > 0 {
				for _, pos := range posList {
					occs = append(occs, rmOccurrence{id: h, pos: pos})
				}
				continue
			}
		}
		if atom.Sent == 0 && atom.Slot == 0 {
			continue
		}
		occs = append(occs, rmOccurrence{
			id:  h,
			pos: TermPosition{Sentence: int(atom.Sent - 1), Slot: int(atom.Slot)},
		})
	}
	if sc != nil {
		sc.queryOccs = occs
	}
	if len(occs) < 2 {
		return nil
	}
	sortRmOccurrences(occs)
	var pairs []rmDyad
	if sc != nil {
		pairs = sc.queryDyads[:0]
	}
	const maxSpan = maxPhraseSlotSpan
	for i, a := range occs {
		for _, b := range occs[i+1:] {
			if b.pos.Sentence != a.pos.Sentence || b.pos.Slot-a.pos.Slot > maxSpan {
				break
			}
			if a.id == b.id || a.pos.Slot == b.pos.Slot {
				continue
			}
			left, right := a.id, b.id
			if left > right {
				left, right = right, left
			}
			pairs = append(pairs, rmDyad{a: left, b: right})
		}
	}
	pairs = sortAndDedupRmDyads(pairs)
	if sc != nil {
		sc.queryDyads = pairs
	}
	return pairs
}

func (st *rmClusterState) buildEffectiveWeightsFromDyads(idx *Index, docDyadRowPtr []uint32, docDyadsFlat []rmDyad) {
	root := func(id uint64) uint64 {
		if g, ok := st.canonicalParent[id]; ok && g != 0 {
			return g
		}
		if g, ok := st.parentGroup[id]; ok && g != 0 {
			return g
		}
		return id
	}
	families := make(map[uint64]*clusterFamilyInfo, len(idx.DF))
	get := func(id uint64) *clusterFamilyInfo {
		f := families[id]
		if f == nil {
			f = &clusterFamilyInfo{lastUnit: -1}
			families[id] = f
		}
		return f
	}
	var rootPairs []rmDyad
	var docFams []*clusterFamilyInfo
	for u := 0; u < idx.N; u++ {
		u32 := int32(u)
		cl := st.clusters[u]
		entries := st.docEntriesOf(u32)
		docFams = docFams[:0]
		for _, entry := range entries {
			g := entry.group
			if g == 0 {
				g = root(entry.id)
			}
			f := get(g)
			if f.lastUnit != u32 {
				f.lastUnit = u32
				f.unitCount++
				f.clusters = append(f.clusters, cl)
				docFams = append(docFams, f)
			}
		}
		if len(docFams) > 1 {
			for _, f := range docFams {
				f.multiDocCount++
			}
		}
		docPairs := docDyadsFlat[docDyadRowPtr[u]:docDyadRowPtr[u+1]]
		if termPartnerStats {
			// 词项模式下伙伴计数按宿主词项单独维护（见 collectTermPartners），
			// 这里不再构造族根伙伴表。
			continue
		}
		rootPairs = rootPairs[:0]
		for _, pair := range docPairs {
			a, b := root(pair.a), root(pair.b)
			if a == b {
				continue
			}
			if a > b {
				a, b = b, a
			}
			rootPairs = append(rootPairs, rmDyad{a: a, b: b})
		}
		if len(rootPairs) > 1 {
			slices.SortFunc(rootPairs, rmDyadCompare)
		}
		for i, pair := range rootPairs {
			if i > 0 && pair == rootPairs[i-1] {
				continue
			}
			get(pair.a).partners = append(get(pair.a).partners, pair.b)
			get(pair.b).partners = append(get(pair.b).partners, pair.a)
		}
	}
	for _, f := range families {
		sortInt32Asc(f.clusters)
		sortUint64Asc(f.partners)
	}
	var termPartners map[uint64][]uint64
	if termPartnerStats {
		termPartners = collectTermPartners(idx, docDyadRowPtr, docDyadsFlat)
	}
	agg := buildClusterAggregates(idx, families, st, termPartners)
	if termPartnerStats {
		agg.refreshTermKeyedWeights(st, idx)
	}
	st.agg = agg
	st.fillEffWeightFromAggregates(idx, agg)
}

// buildEpisodeComponents partitions the sorted bucket timeline into episodic
// components by endogenous gap-splitting. Membership structures must stay
// phase-free (time-shift invariant): a component boundary opens only where the
// inter-bucket gap strictly exceeds tau_episode = e^{mu+sigma}-1 (fallback
// e*meanGap). The partition depends on gaps alone — never on any calendar-grid
// origin — and degenerates to a single component when no split is confident
// (fail-open towards continuity). Untimed units keep the -1 sentinel. Shared
// by the in-memory builder and the disk CSR hydration path so both derive the
// identical partition from the same persisted inputs.
func buildEpisodeComponents(bucketTimes []int64, ordPos []int32, n int, meanGap, muLogGap, sigLogGap float64) ([]int32, int) {
	episodeOf := make([]int32, n)
	for u := range episodeOf {
		episodeOf[u] = -1
	}
	if len(bucketTimes) == 0 {
		return episodeOf, 0
	}
	// 切分判据在 θ 空间（Log1p 严格单调双射）比较：gap > Expm1(μ+σ) ⟺
	// Log1p(gap) > μ+σ。原式把均值的末位舍入经 Expm1 放大到阈值量级（等间隔
	// 退化输入下随桶数漂移 1e-11…1e-4），会让严格大于比较由累加舍入决胜——
	// 实测同一语料 N=10,000 时整库塌缩为单例情节、N=5,000/8,000/12,000 时为
	// 单一组件。θ 空间比较保持数学等价且不受该放大影响。
	logThreshold := muLogGap + sigLogGap
	tauFallback := 0.0
	if logThreshold <= 0 {
		tauFallback = meanGap * eulerNumber
	}
	bucketEp := make([]int32, len(bucketTimes))
	epID := int32(0)
	for i := 1; i < len(bucketTimes); i++ {
		gap := float64(bucketTimes[i] - bucketTimes[i-1])
		split := false
		if logThreshold > 0 {
			split = math.Log1p(gap) > logThreshold
		} else if tauFallback > 0 {
			split = gap > tauFallback
		}
		if split {
			epID++
		}
		bucketEp[i] = epID
	}
	for u := 0; u < n && u < len(ordPos); u++ {
		if o := ordPos[u]; o >= 0 && int(o) < len(bucketEp) {
			episodeOf[u] = bucketEp[o]
		}
	}
	return episodeOf, int(epID) + 1
}

func (st *rmClusterState) calcMeanSentenceEnergy(idx *Index) float64 {
	var mean float64
	var count int
	highDFThresh := idx.HighDFThreshold()
	var sentGW []rmStructSentGroupW
	wide := st.hasWidePos()
	for u := 0; u < idx.N; u++ {
		entries := st.docEntriesOf(int32(u))
		sentGW = sentGW[:0]
		base := st.docRowPtr[u]
		for ei, entry := range entries {
			w := st.entryMass[base+uint32(ei)]
			if math.IsNaN(w) || math.IsInf(w, 0) || w <= 0 {
				continue
			}
			g := effectiveEntryGroup(entry, idx, highDFThresh)
			posList := st.entryPositionsOf(entry)
			// 同一词项的同一句内多位置产生完全相同的 (句, 组, 权重)，折叠前只需一份：
			// 每 (文档, 词项, 句) 保留一个条目，结果与逐位置完全一致。
			lastSent := -1
			for pi := range posList {
				sent := int(posList[pi] >> 16)
				if wide {
					sent = st.entrySentExactAt(entry, pi)
				}
				if sent == lastSent {
					continue
				}
				lastSent = sent
				sentGW = append(sentGW, rmStructSentGroupW{sent: sent, group: g, weight: w})
			}
		}
		if len(sentGW) == 0 {
			continue
		}
		sentGW = sortAndFoldSentGroups(sentGW)
		for i := 0; i < len(sentGW); {
			s := sentGW[i].sent
			var energy float64
			for ; i < len(sentGW) && sentGW[i].sent == s; i++ {
				energy += sentGW[i].weight
			}
			count++
			mean += (energy - mean) / float64(count)
		}
	}
	return mean
}

// temporalAffinityProbe is installed only by measurement tests; the nil check
// keeps production cost at one branch per call.
var temporalAffinityProbe func(a, b int32, ta, tb int64, oa, ob int32)

// temporalAffinityUnderflowGapRatio = 1075·ln2 is the |Δt|/meanGap ratio past
// which Exp(-x) flushes to exact +0 under float64 round-to-nearest (1074
// subnormal significand bits plus one half-ULP); past it every downstream
// product of the affinity is an exact zero, so the early return is
// bit-for-bit identical to the full computation.
const temporalAffinityUnderflowGapRatio = 1075 * math.Ln2

// SetTemporalAffinityProbe installs the measurement hook; nil removes it.
func SetTemporalAffinityProbe(fn func(a, b int32, ta, tb int64, oa, ob int32)) {
	temporalAffinityProbe = fn
}

func (st *rmClusterState) temporalAffinity(idx *Index, a, b int32) float64 {
	if int(a) >= len(st.refTimes) || int(b) >= len(st.refTimes) {
		return 0
	}
	ta, tb := st.refTimes[a], st.refTimes[b]
	if temporalAffinityProbe != nil {
		var oa, ob int32 = -1, -1
		if int(a) < len(st.ordPos) {
			oa = st.ordPos[a]
		}
		if int(b) < len(st.ordPos) {
			ob = st.ordPos[b]
		}
		temporalAffinityProbe(a, b, ta, tb, oa, ob)
	}
	if ta <= 0 || tb <= 0 {
		return 0
	}
	gap := math.Abs(float64(ta - tb))
	if gap == 0 {
		return 1
	}
	if st.meanGap <= 0 {
		return 0
	}
	// float64 舍入下溢点：x = gap/meanGap > 1075·ln2（1074 个亚正规位 + 半 ULP
	// 舍入）后 Exp(-x) 精确等于 +0，下游乘积（含 ordinal 幂因子）全为精确零浮点，
	// 由此提前返回 0 与原路径逐位相同。
	if gap > temporalAffinityUnderflowGapRatio*st.meanGap {
		return 0
	}
	// 近核取常数危险率 1/meanGap 的指数生存函数 e^{-x}（x = |Δt|/meanGap，即
	// Poisson 零假设下间隔超过 |Δt| 的概率）；硬窗阶跃替换经同池配对判负：
	// 阶跃把「渐近变远」压成「过窗即断」，相邻而非同期的事件在同一阶跃两侧被
	// 判成完全无关，连续形态承重，勿以阶跃/窗口替代。
	worldAff := math.Exp(-gap / st.meanGap)
	if st.totalDaysM > 0 && int(a) < len(st.ordPos) && int(b) < len(st.ordPos) {
		oa, ob := st.ordPos[a], st.ordPos[b]
		if oa >= 0 && ob >= 0 {
			dk := math.Abs(float64(oa - ob))
			kappaOrd := st.kappaOrd
			if kappaOrd <= 0 {
				kappaOrd = eulerNumber * math.Sqrt(math.Max(1.0, float64(st.clusterN)/st.totalDaysM))
			}
			if dk > kappaOrd {
				excessOrd := (dk - kappaOrd) / kappaOrd
				worldAff *= math.Pow(1.0+excessOrd*excessOrd, -transmissionPerHop)
			}
		}
	}
	return worldAff
}
