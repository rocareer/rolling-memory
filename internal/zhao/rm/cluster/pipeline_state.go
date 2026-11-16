// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cluster

import (
	"math"
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
	primBits  uint16
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

func (c *rmCandidate) upsertMatch(group uint64, mass float64, entry int32, newBits uint16, unitEntries []rmDocEntry, tieBreakBits bool) {
	isExactGroup := tieBreakBits && unitEntries[entry].id == group
	for i := range c.matches {
		if c.matches[i].group == group {
			if isExactGroup {
				if mass > c.matches[i].primMass {
					c.matches[i].primMass = mass
					c.matches[i].primEntry = entry
					c.matches[i].primBits = newBits
				}
			} else if tieBreakBits {
				if mass > c.matches[i].compMass {
					c.matches[i].compMass = mass
				}
			}
			oldBits := unitEntries[c.matches[i].entry].bits
			if len(c.matches[i].positions) == 0 {
				c.matches[i].positions = []int32{c.matches[i].entry, entry}
			} else {
				c.matches[i].positions = append(c.matches[i].positions, entry)
			}
			if mass > c.matches[i].mass || (tieBreakBits && mass == c.matches[i].mass && newBits > oldBits) {
				c.matches[i].mass = mass
				c.matches[i].entry = entry
			}
			return
		}
	}
	var pm, cm float64
	var pe int32
	var pb uint16
	if isExactGroup {
		pm = mass
		pe = entry
		pb = newBits
	} else if tieBreakBits {
		cm = mass
	}
	c.matches = append(c.matches, rmFeatureMatch{
		group:     group,
		mass:      mass,
		primMass:  pm,
		compMass:  cm,
		primEntry: pe,
		primBits:  pb,
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
	sort.Slice(sentGW, func(i, j int) bool {
		if sentGW[i].sent != sentGW[j].sent {
			return sentGW[i].sent < sentGW[j].sent
		}
		if sentGW[i].group != sentGW[j].group {
			return sentGW[i].group < sentGW[j].group
		}
		return sentGW[i].weight > sentGW[j].weight
	})
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
	matchPool        []rmFeatureMatch
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
	sentGroupW       []rmStructSentGroupW
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
	dirtyGen         uint32
	unitDirtyGen     []uint32
	dirtyUnits       []int32
	lexUnits         []int32
	coreUnits        []int32
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
	if cap(sc.matchPool) < n*2 {
		sc.matchPool = make([]rmFeatureMatch, 0, n*2)
	} else {
		sc.matchPool = sc.matchPool[:0]
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

func (sc *rmQueryScratch) resetDirtyUnits(n int) uint32 {
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

func (sc *rmQueryScratch) markDirty(u int32, dGen uint32) {
	if sc.unitDirtyGen[u] != dGen {
		sc.unitDirtyGen[u] = dGen
		sc.dirtyUnits = append(sc.dirtyUnits, u)
	}
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
	docEntries         [][]rmDocEntry
	docRowPtr          []uint32
	docEntriesFlat     []rmDocEntry
	posPool            []uint32
	postings           map[uint64][]rmPostingEntry
	termKeys           []uint64
	termPrefixPtr      [4097]uint32
	termRowPtr         []uint32
	postingPool        []rmPostingEntry
	termEffWeight      []float64
	termProperRatio    []float64
	termParentGroup    []uint64
	termIsPrimary      []uint8
	phraseKeys         []rmDyad
	phrasePrefixPtr    [4097]uint32
	phraseRowPtr       []uint32
	phraseUnits        []int32
	effWeight          map[uint64]float64
	properRatio        map[uint64]float64
	parentGroup        map[uint64]uint64
	isPrimaryWord      map[uint64]bool
	clusters           []int32
	clusterN           int
	bucketTimes        []int64
	neighborRowPtr     []uint32
	neighborPool       []int32
	refTimes           []int64
	meanGap            float64
	muLogGap           float64
	sigLogGap          float64
	ordPos             []int32
	episodeOf          []int32
	episodeCount       int
	entryMeanBits      float64
	sentenceEnergyMean float64
	totalDaysM         float64
	fastDocs           []rmFastDocStruct
	subjRowPtr         []uint32
	subjIDsPool        []uint64
	subjWeightsPool    []float64
	dayCos             map[uint64]float64
	dayCosRowPtr       []uint32
	dayCosKeys         []uint64
	dayCosVals         []float64
	provKeys           []uint64
	provRowPtr         []uint32
	provUnits          []int32
	citeRowPtr         []uint32
	citePeers          []int32
	authorDF           map[uint64]int32
	widePos            map[uint32]TermPosition

	builtIndexGen uint64
	builtDigest   uint64
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

func (st *rmClusterState) parentGroupOf(h uint64) (uint64, bool) {
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

func (st *rmClusterState) neighborsOf(u int32) []int32 {
	if int(u)+1 >= len(st.neighborRowPtr) {
		return nil
	}
	return st.neighborPool[st.neighborRowPtr[u]:st.neighborRowPtr[u+1]]
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

func (st *rmClusterState) phrasePostings(pair rmDyad) []int32 {
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
	if e.Contraction || e.TimeKernel || e.Energy != EnergyMutualInfo || e.Hop1Budget <= 0 {
		return false
	}
	return true
}

func (e *Engine) ensureClusterState() *rmClusterState {
	idx := e.Index
	if e.clusterState != nil {
		if idx == nil || (e.clusterState.builtIndexGen == idx.stateGen && e.clusterState.builtDigest == idx.PostingDigest) {
			return e.clusterState
		}
		e.clusterState = nil
		e.queryScratch = nil
	}
	if idx == nil {
		return nil
	}
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
		builtIndexGen:  idx.stateGen,
		builtDigest:    idx.PostingDigest,
		docEntries:     make([][]rmDocEntry, idx.N),
		docRowPtr:      make([]uint32, idx.N+1),
		docEntriesFlat: make([]rmDocEntry, totalDocEntries),
		posPool:        make([]uint32, 0, totalPositions),
		postings:       make(map[uint64][]rmPostingEntry, len(idx.DF)),
		termKeys:       make([]uint64, 0, len(idx.DF)),
		termRowPtr:     make([]uint32, len(idx.DF)+1),
		postingPool:    make([]rmPostingEntry, totalDocEntries),
		effWeight:      make(map[uint64]float64, len(idx.DF)),
		parentGroup:    make(map[uint64]uint64, len(idx.DF)),
		isPrimaryWord:  make(map[uint64]bool, len(idx.DF)),
		clusters:       make([]int32, idx.N),
		neighborRowPtr: make([]uint32, idx.N+1),
		refTimes:       make([]int64, idx.N),
	}
	for h := range idx.DF {
		st.termKeys = append(st.termKeys, h)
	}
	sort.Slice(st.termKeys, func(i, j int) bool { return st.termKeys[i] < st.termKeys[j] })

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
		st.postings[h] = st.postingPool[st.termRowPtr[i]:postAcc:postAcc]
	}
	st.termRowPtr[len(st.termKeys)] = postAcc

	type dyadPosting struct {
		pair rmDyad
		unit int32
	}
	docDyadRowPtr := make([]uint32, idx.N+1)
	docDyadsFlat := make([]rmDyad, 0, totalDocEntries*2)
	allDyadPostings := make([]dyadPosting, 0, totalDocEntries*2)
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
						sort.Slice(posScratch, func(a, b int) bool { return posScratch[a] < posScratch[b] })
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
		st.docEntries[u] = entries
		docDyadRowPtr[u] = uint32(len(docDyadsFlat))
		dyadScratch, occScratch = st.extractDocDyadsInto(entries, occScratch, dyadScratch[:0])
		docDyadsFlat = append(docDyadsFlat, dyadScratch...)
		for _, pair := range dyadScratch {
			allDyadPostings = append(allDyadPostings, dyadPosting{pair: pair, unit: int32(u)})
		}
	}
	docDyadRowPtr[idx.N] = uint32(len(docDyadsFlat))

	// Build flat CSR for intra-sentence PhraseDyad postings using 1024-bucket
	// MSB radix partitioning on pair.a >> 54 so each L1-sized bucket sorts in-cache.
	if len(allDyadPostings) > 0 {
		const radixBits = 10
		const numBuckets = 1 << radixBits
		const shift = 64 - radixBits
		var bucketCounts [numBuckets]uint32
		for _, dp := range allDyadPostings {
			bucketCounts[dp.pair.a>>shift]++
		}
		var bucketOffsets [numBuckets + 1]uint32
		for b := 0; b < numBuckets; b++ {
			bucketOffsets[b+1] = bucketOffsets[b] + bucketCounts[b]
		}
		var writeCur [numBuckets]uint32
		copy(writeCur[:], bucketOffsets[:numBuckets])
		partitioned := make([]dyadPosting, len(allDyadPostings))
		for _, dp := range allDyadPostings {
			b := dp.pair.a >> shift
			partitioned[writeCur[b]] = dp
			writeCur[b]++
		}
		allDyadPostings = partitioned
		for b := 0; b < numBuckets; b++ {
			s, e := bucketOffsets[b], bucketOffsets[b+1]
			if e-s <= 1 {
				continue
			}
			sub := allDyadPostings[s:e]
			sort.Slice(sub, func(i, j int) bool {
				x, y := sub[i], sub[j]
				if x.pair.a != y.pair.a {
					return x.pair.a < y.pair.a
				}
				if x.pair.b != y.pair.b {
					return x.pair.b < y.pair.b
				}
				return x.unit < y.unit
			})
		}
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
	var sumLogGap, sumLogGap2 float64
	for i := 1; i < len(buckets); i++ {
		gap := float64(buckets[i].time - buckets[i-1].time)
		st.meanGap += (gap - st.meanGap) / float64(i)
		lg := math.Log1p(gap)
		sumLogGap += lg
		sumLogGap2 += lg * lg
	}
	if len(buckets) > 1 {
		nG := float64(len(buckets) - 1)
		st.muLogGap = sumLogGap / nG
		st.sigLogGap = math.Sqrt(math.Max(0, sumLogGap2/nG-st.muLogGap*st.muLogGap))
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
	const neighborSpan = 2
	bucketNeighbors := make([][]int32, len(buckets))
	var totalNeighborLinks int
	for i, b := range buckets {
		neighbors := append([]int32(nil), b.units...)
		for span := 1; span <= neighborSpan; span++ {
			for _, adj := range [2]int{i - span, i + span} {
				if adj >= 0 && adj < len(buckets) {
					neighbors = append(neighbors, buckets[adj].units...)
				}
			}
		}
		bucketNeighbors[i] = neighbors
		totalNeighborLinks += len(b.units) * len(neighbors)
	}
	st.neighborPool = make([]int32, 0, totalNeighborLinks)
	for u := 0; u < idx.N; u++ {
		st.neighborRowPtr[u] = uint32(len(st.neighborPool))
		if bIdx := st.ordPos[u]; bIdx >= 0 && int(bIdx) < len(bucketNeighbors) {
			st.neighborPool = append(st.neighborPool, bucketNeighbors[bIdx]...)
		}
	}
	st.neighborRowPtr[idx.N] = uint32(len(st.neighborPool))

	// Build effective family weights reusing pre-extracted docDyadsFlat.
	st.buildEffectiveWeightsFromDyads(idx, docDyadRowPtr, docDyadsFlat)
	docDyadsFlat = nil
	docDyadRowPtr = nil

	const rmProperMask uint8 = (1 << 0) | (1 << 1)
	st.properRatio = make(map[uint64]float64, len(st.termKeys))
	for i, h := range st.termKeys {
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
			st.properRatio[h] = float64(propCnt) / float64(len(posts))
		}
	}
	st.sentenceEnergyMean = st.calcMeanSentenceEnergy(idx)
	// Provenance (and citation) CSR plus envelope DF counts must exist before
	// buildFastDocs: the day-cos precompute inside it consults the
	// citation-edge floor and the collision-surprisal-scaled envelope floor.
	st.buildEnvelopeDF(idx)
	st.buildProvenanceCSR(idx)
	st.buildFastDocs(idx)
	st.buildPrefixTables(idx.N)
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
		for _, entry := range entries {
			w := idx.Mass[entry.id]
			if math.IsNaN(w) || math.IsInf(w, 0) || w <= 0 {
				continue
			}
			w2 := w * w
			st.subjIDsPool = append(st.subjIDsPool, entry.id)
			st.subjWeightsPool = append(st.subjWeightsPool, w2)
			subjNormSq += w2
			g := effectiveEntryGroup(entry, idx, highDFThresh)
			docGW = append(docGW, rmStructGroupW{group: g, weight: w})
		}
		st.subjRowPtr[u+1] = uint32(len(st.subjIDsPool))
		if len(docGW) > 1 {
			sort.Slice(docGW, func(i, j int) bool {
				if docGW[i].group != docGW[j].group {
					return docGW[i].group < docGW[j].group
				}
				return docGW[i].weight > docGW[j].weight
			})
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
	// Precomputed subject-cos warm domain: co-episode pairs plus direct time
	// neighbors. Keyed by the endogenous episodic component (phase-free)
	// rather than the calendar-day grid; pairs outside the warm domain fall
	// back to computeDocSubjectCos on demand, so the domain only affects
	// which pairs are cached, never the cosine value itself.
	byEpisode := make(map[int32][]int32, 256)
	for u := 0; u < idx.N; u++ {
		if ep := st.episodeOf[u]; ep >= 0 {
			byEpisode[ep] = append(byEpisode[ep], int32(u))
		}
	}
	st.dayCos = make(map[uint64]float64, 65536)
	for _, units := range byEpisode {
		for i, a := range units {
			for _, b := range units[i+1:] {
				if c := st.computeDocSubjectCos(idx, a, b); c > 0 {
					x, y := uint32(a), uint32(b)
					if x > y {
						x, y = y, x
					}
					st.dayCos[(uint64(x)<<32)|uint64(y)] = c
				}
			}
		}
	}
	for u := 0; u < idx.N; u++ {
		a := int32(u)
		for _, b := range st.neighborsOf(a) {
			if b <= a || st.episodeOf[a] == st.episodeOf[b] {
				continue
			}
			if c := st.computeDocSubjectCos(idx, a, b); c > 0 {
				x, y := uint32(a), uint32(b)
				if x > y {
					x, y = y, x
				}
				st.dayCos[(uint64(x)<<32)|uint64(y)] = c
			}
		}
	}
}

func (st *rmClusterState) docSubjectCos(idx *Index, a, b int32) float64 {
	// Warm-domain gate must stay the exact dual of the precompute population:
	// co-episode pairs (untimed sentinel -1 matches -1, preserving the legacy
	// miss-to-zero semantics for untimed pairs) plus direct time neighbors.
	if st.episodeOf[a] == st.episodeOf[b] || st.isDirectTimeNeighbor(a, b) {
		x, y := uint32(a), uint32(b)
		if x > y {
			x, y = y, x
		}
		key := (uint64(x) << 32) | uint64(y)
		if st.dayCos != nil {
			return st.dayCos[key]
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
			return 0
		}
	}
	return st.computeDocSubjectCos(idx, a, b)
}

// buildEnvelopeDF counts, per distinct envelope commitment key (AuthorHash,
// DocHash, BlockHash), how many units carry it. The count feeds the
// null-hypothesis collision surprisal of "two units share this key", so the
// subject-cosine floor scales with the key's corpus-level discriminative
// power instead of firing unconditionally.
func (st *rmClusterState) buildEnvelopeDF(idx *Index) {
	st.authorDF = nil
	if idx == nil || len(idx.UnitMeta) == 0 {
		return
	}
	df := make(map[uint64]int32, 64)
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
	}
	if len(df) > 0 {
		st.authorDF = df
	}
}

// envelopeKeyGain is the normalized collision surprisal of one shared
// envelope key: -ln(df/N) / ln(N) in [0, 1]. A key carried by every unit
// (df == N) is a certain collision under the null hypothesis and earns zero
// floor; a key carried by a single pair approaches full gain. Both df and N
// are online sufficient statistics, so the gain self-calibrates as the
// corpus grows and stays scale-invariant across N.
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
			if g := st.envelopeKeyGain(ma.DocHash, idx.N); g > gain {
				gain = g
			}
		}
		// A citation edge is an explicit pairwise provenance declaration, not
		// a shared key with corpus-level extension, so it keeps the full floor.
		if st.hasCitationEdge(a, b) {
			gain = 1
		}
		floor = invE * gain
	}
	fa, fb := &st.fastDocs[a], &st.fastDocs[b]
	if fa.subjNormSq <= 0 || fb.subjNormSq <= 0 {
		return floor
	}
	sA, eA := st.subjRowPtr[a], st.subjRowPtr[a+1]
	sB, eB := st.subjRowPtr[b], st.subjRowPtr[b+1]
	ia, ib := st.subjIDsPool[sA:eA], st.subjIDsPool[sB:eB]
	wa := st.subjWeightsPool[sA:eA]
	var dot float64
	for x, y := 0, 0; x < len(ia) && y < len(ib); {
		if ia[x] < ib[y] {
			x++
		} else if ia[x] > ib[y] {
			y++
		} else {
			dot += wa[x]
			x++
			y++
		}
	}
	if dot <= 0 {
		return floor
	}
	return math.Max(floor, math.Min(1.0, dot/math.Sqrt(fa.subjNormSq*fb.subjNormSq)))
}

type rmOccurrence struct {
	id  uint64
	pos TermPosition
}

type rmPackedOccurrence struct {
	pos uint32
	id  uint64
}

// dyadEulerSlotHorizon returns the 1st-order Euler intra-sentence phrase coherence
// slot span 1 + ceil(e): starting from adjacent distance d_0 = 1, two terms remain
// within the local phrase horizon while separated by at most ceil(e) intervening
// modifier/function slots (where exp(-(d-1)/e) >= exp(-ceil(e)/e) ≈ 1/e).
func dyadEulerSlotHorizon() int {
	return 1 + int(math.Ceil(math.E))
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
	sort.Slice(occs, func(i, j int) bool {
		a, b := occs[i], occs[j]
		if a.pos != b.pos {
			return a.pos < b.pos
		}
		return a.id < b.id
	})
	out = out[:0]
	maxSpan := uint32(dyadEulerSlotHorizon())
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
	return out[:w], occs
}

func rmQueryDyadsWithPositions(qTerms map[uint64]Term, qPositions map[uint64][]TermPosition) []rmDyad {
	var occs []rmOccurrence
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
	if len(occs) < 2 {
		return nil
	}
	sort.Slice(occs, func(i, j int) bool {
		a, b := occs[i], occs[j]
		if a.pos.Sentence != b.pos.Sentence {
			return a.pos.Sentence < b.pos.Sentence
		}
		if a.pos.Slot != b.pos.Slot {
			return a.pos.Slot < b.pos.Slot
		}
		return a.id < b.id
	})
	seen := make(map[rmDyad]struct{})
	maxSpan := dyadEulerSlotHorizon()
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
			seen[rmDyad{a: left, b: right}] = struct{}{}
		}
	}
	pairs := make([]rmDyad, 0, len(seen))
	for pair := range seen {
		pairs = append(pairs, pair)
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].a != pairs[j].a {
			return pairs[i].a < pairs[j].a
		}
		return pairs[i].b < pairs[j].b
	})
	return pairs
}

func (st *rmClusterState) buildEffectiveWeightsFromDyads(idx *Index, docDyadRowPtr []uint32, docDyadsFlat []rmDyad) {
	root := func(id uint64) uint64 {
		if g, ok := st.parentGroup[id]; ok && g != 0 {
			return g
		}
		return id
	}
	type famInfo struct {
		lastUnit      int32
		unitCount     int
		multiDocCount int
		clusters      map[int32]struct{}
		partners      map[uint64]float64
		weight        float64
	}
	families := make(map[uint64]*famInfo, len(idx.DF))
	get := func(id uint64) *famInfo {
		f := families[id]
		if f == nil {
			f = &famInfo{
				lastUnit: -1,
				clusters: make(map[int32]struct{}),
				partners: make(map[uint64]float64),
			}
			families[id] = f
		}
		return f
	}
	var rootPairs []rmDyad
	var docFams []*famInfo
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
				f.clusters[cl] = struct{}{}
				docFams = append(docFams, f)
			}
		}
		if len(docFams) > 1 {
			for _, f := range docFams {
				f.multiDocCount++
			}
		}
		docPairs := docDyadsFlat[docDyadRowPtr[u]:docDyadRowPtr[u+1]]
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
			sort.Slice(rootPairs, func(i, j int) bool {
				if rootPairs[i].a != rootPairs[j].a {
					return rootPairs[i].a < rootPairs[j].a
				}
				return rootPairs[i].b < rootPairs[j].b
			})
		}
		for i, pair := range rootPairs {
			if i > 0 && pair == rootPairs[i-1] {
				continue
			}
			get(pair.a).partners[pair.b]++
			get(pair.b).partners[pair.a]++
		}
	}
	var keys []uint64
	for _, f := range families {
		keys = keys[:0]
		var total float64
		for k, c := range f.partners {
			keys = append(keys, k)
			total += c
		}
		if len(keys) > 1 {
			sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
		}
		var entropy float64
		if total > 0 {
			for _, k := range keys {
				p := f.partners[k] / total
				entropy -= p * math.Log(p)
			}
		}
		df := f.unitCount
		clusters := len(f.clusters)
		effDF := 1.0
		if df > 1 {
			if clusters < 1 {
				clusters = 1
			}
			if clusters > df {
				clusters = df
			}
			d := float64(df - 1)
			indep := math.Pow(float64(clusters-1)/d, 1/math.E)
			disp := 1.0
			if total > 0 || f.multiDocCount > 0 {
				disp = math.Min(1, math.Max(0, entropy/math.Log(float64(df)+math.E)))
			}
			effDF = 1 + d*indep*disp
		}
		f.weight = math.Log1p((float64(st.clusterN) + 1) / (effDF + 1))
	}
	for h := range idx.DF {
		st.effWeight[h] = get(root(h)).weight
	}
	for u := 0; u < idx.N; u++ {
		for _, entry := range st.docEntriesOf(int32(u)) {
			if entry.group == 0 {
				continue
			}
			if f := families[entry.group]; f != nil && f.weight < st.effWeight[entry.id] {
				st.effWeight[entry.id] = f.weight
			}
		}
	}
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
	tauEpisode := math.Expm1(muLogGap + sigLogGap)
	if tauEpisode <= 0 {
		tauEpisode = meanGap * math.E
	}
	bucketEp := make([]int32, len(bucketTimes))
	epID := int32(0)
	for i := 1; i < len(bucketTimes); i++ {
		if tauEpisode > 0 && float64(bucketTimes[i]-bucketTimes[i-1]) > tauEpisode {
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
	for u := 0; u < idx.N; u++ {
		entries := st.docEntriesOf(int32(u))
		sentGW = sentGW[:0]
		for _, entry := range entries {
			w := idx.Mass[entry.id]
			if math.IsNaN(w) || math.IsInf(w, 0) || w <= 0 {
				continue
			}
			g := effectiveEntryGroup(entry, idx, highDFThresh)
			for _, p := range st.entryPositionsOf(entry) {
				sentGW = append(sentGW, rmStructSentGroupW{sent: int(p >> 16), group: g, weight: w})
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

func (st *rmClusterState) temporalAffinity(idx *Index, a, b int32) float64 {
	if int(a) >= len(st.refTimes) || int(b) >= len(st.refTimes) {
		return 0
	}
	ta, tb := st.refTimes[a], st.refTimes[b]
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
	worldAff := math.Exp(-gap / st.meanGap)
	if st.totalDaysM > 0 && int(a) < len(st.ordPos) && int(b) < len(st.ordPos) {
		oa, ob := st.ordPos[a], st.ordPos[b]
		if oa >= 0 && ob >= 0 {
			dk := math.Abs(float64(oa - ob))
			kappaOrd := math.E * math.Sqrt(math.Max(1.0, float64(st.clusterN)/st.totalDaysM))
			if dk > kappaOrd {
				excessOrd := (dk - kappaOrd) / kappaOrd
				worldAff *= math.Pow(1.0+excessOrd*excessOrd, -invE)
			}
		}
	}
	return worldAff
}
