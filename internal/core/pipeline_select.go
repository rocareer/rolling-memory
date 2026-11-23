// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package core

import (
	"math"
	"sort"
)

func (st *rmClusterState) closestFamilySlot(entries []rmDocEntry, a, b rmFeatureMatch) int {
	if a.entry < 0 || b.entry < 0 {
		// Mass-only synthetic matches carry no positions: no family proximity.
		return -1
	}
	if st.hasWidePos() {
		return st.closestFamilySlotExact(entries, a, b)
	}
	if len(a.positions) <= 1 && len(b.positions) <= 1 {
		return closestPackedSlot(st.entryPositionsOf(entries[a.entry]), st.entryPositionsOf(entries[b.entry]))
	}
	var aFixed, bFixed [1]int32
	leftEntries := a.positions
	if len(leftEntries) == 0 {
		aFixed[0] = a.entry
		leftEntries = aFixed[:]
	}
	rightEntries := b.positions
	if len(rightEntries) == 0 {
		bFixed[0] = b.entry
		rightEntries = bFixed[:]
	}
	best := -1
	for _, le := range leftEntries {
		lPos := st.entryPositionsOf(entries[le])
		for _, re := range rightEntries {
			d := closestPackedSlot(lPos, st.entryPositionsOf(entries[re]))
			if d >= 0 && (best < 0 || d < best) {
				best = d
				if best == 0 {
					return 0
				}
			}
		}
	}
	return best
}

// closestFamilySlotExact 是 closestFamilySlot 的精确坐标分支：宽语料下打包钳位会让
// 远端句折叠为同句并伪造最小槽距，此处以 (句, 槽) 原值计算；窄语料永不进入。
func (st *rmClusterState) closestFamilySlotExact(entries []rmDocEntry, a, b rmFeatureMatch) int {
	if a.entry < 0 || b.entry < 0 {
		// Mass-only synthetic matches carry no positions: no family proximity.
		return -1
	}
	if len(a.positions) <= 1 && len(b.positions) <= 1 {
		return closestExactSlot(st.entryPositionsExactOf(entries[a.entry]), st.entryPositionsExactOf(entries[b.entry]))
	}
	var aFixed, bFixed [1]int32
	leftEntries := a.positions
	if len(leftEntries) == 0 {
		aFixed[0] = a.entry
		leftEntries = aFixed[:]
	}
	rightEntries := b.positions
	if len(rightEntries) == 0 {
		bFixed[0] = b.entry
		rightEntries = bFixed[:]
	}
	best := -1
	for _, le := range leftEntries {
		lPos := st.entryPositionsExactOf(entries[le])
		for _, re := range rightEntries {
			d := closestExactSlot(lPos, st.entryPositionsExactOf(entries[re]))
			if d >= 0 && (best < 0 || d < best) {
				best = d
				if best == 0 {
					return 0
				}
			}
		}
	}
	return best
}

// proxPairProbe and structureCallProbe are installed only by measurement
// tests; the nil checks keep production cost at one branch per call.
var (
	proxPairProbe      func(pairs int)
	structureCallProbe func(entries int)
)

// SetProxPairProbe installs the pair-count hook; nil removes it.
func SetProxPairProbe(fn func(pairs int)) { proxPairProbe = fn }

// SetStructureCallProbe installs the structure-call hook; nil removes it.
func SetStructureCallProbe(fn func(entries int)) { structureCallProbe = fn }

// ChannelBreakdown is a read-only snapshot of the seven scoring channels for
// one selected hit. It is populated only while SetChannelBreakdownProbe is
// installed; production leaves the probe nil and pays one branch per hit.
type ChannelBreakdown struct {
	UnitID       string
	Lex          float64
	Phrase       float64
	Entity       float64
	Structure    float64
	Remaining    float64
	StSpread     float64
	StCorrob     float64
	ClonePenalty float64
	Scale        float64
	Energy       float64
}

var channelBreakdownProbe func(ChannelBreakdown)

// SetChannelBreakdownProbe installs the selection-breakdown hook; nil removes it.
func SetChannelBreakdownProbe(fn func(ChannelBreakdown)) { channelBreakdownProbe = fn }

func (st *rmClusterState) rmProximityAndSynergy(c *rmCandidate, groups map[uint64]float64, total float64) (float64, float64) {
	if total <= 0 || len(c.matches) < 2 {
		return 0, 0
	}
	if proxPairProbe != nil {
		proxPairProbe(len(c.matches) * (len(c.matches) - 1) / 2)
	}
	entries := st.docEntriesOf(c.unit)
	n := len(c.matches)
	// Resolve each match's position slice once. The pair loop would otherwise
	// rebuild two slice headers per pair; the common single-entry match is also
	// compared inline below instead of re-entering closestPackedSlot.
	wide := st.hasWidePos()
	var posBuf [16][]uint32
	var pos [][]uint32
	if n <= len(posBuf) {
		pos = posBuf[:n]
	} else {
		pos = make([][]uint32, n)
	}
	for i := range c.matches {
		m := &c.matches[i]
		switch {
		case len(m.positions) == 1:
			pos[i] = st.entryPositionsOf(entries[m.positions[0]])
		case len(m.positions) == 0 && m.entry >= 0:
			pos[i] = st.entryPositionsOf(entries[m.entry])
		}
	}
	var gwBuf [32]float64
	var gws []float64
	if n <= len(gwBuf) {
		gws = gwBuf[:n]
	} else {
		gws = make([]float64, n)
	}
	for i := range c.matches {
		gws[i] = groups[c.matches[i].group]
	}
	var prox, syn float64
	for i := range c.matches {
		a := &c.matches[i]
		ma := gws[i]
		for j := i + 1; j < n; j++ {
			b := &c.matches[j]
			mb := gws[j]
			// Both pair channels are exactly zero when neither the group
			// weights nor the two masses can contribute; skip the distance scan.
			if ma+mb <= 0 && (a.mass <= 0 || b.mass <= 0) {
				continue
			}
			var closest int
			if !wide && len(pos[i]) == 1 && len(pos[j]) == 1 {
				li, lj := pos[i][0], pos[j][0]
				if li>>16 == lj>>16 {
					closest = int(li&0xFFFF) - int(lj&0xFFFF)
					if closest < 0 {
						closest = -closest
					}
				} else {
					closest = -1
				}
			} else {
				closest = st.closestFamilySlot(entries, *a, *b)
			}
			if closest < 0 {
				continue
			}
			d := float64(closest)
			if d < 1 {
				d = 1
			}
			if ma+mb > 0 {
				if d == 1 {
					// exp(-(d-1)*c) = exp(-0) = 1 exactly.
					prox += 2 * ma * mb / (ma + mb)
				} else {
					prox += 2 * ma * mb / (ma + mb) * math.Exp(-(d-1)*transmissionPerHop)
				}
			}
			syn += a.mass * (b.mass / total) * math.Exp(-d*transmissionTwoHop)
		}
	}
	return prox, syn
}

func (st *rmClusterState) computeStructuredIncrement(idx *Index, sc *rmQueryScratch, c *rmCandidate, groups map[uint64]float64, total float64) float64 {
	if total <= 0 || len(c.matches) == 0 {
		return 0
	}
	fd := &st.fastDocs[c.unit]
	if fd.docTotal <= 0 {
		return 0
	}
	entries := st.docEntriesOf(c.unit)
	if structureCallProbe != nil {
		structureCallProbe(len(entries))
	}
	highDFThresh := idx.HighDFThreshold()
	wide := st.hasWidePos()

	// Prefilling the effective group costs one pass and one array write per
	// entry; computing it inline instead was measured to cost more, because the
	// pass below and the sentence pass both need the value and every inline call
	// re-runs the function (effectiveEntryGroup cum 0.86s -> 1.31s on the ATM
	// query profile).
	effG := sc.docEntryEffGroup
	if cap(effG) < len(entries) {
		effG = make([]uint64, len(entries))
	} else {
		effG = effG[:len(entries)]
	}
	sc.docEntryEffGroup = effG
	for ei := range entries {
		effG[ei] = effectiveEntryGroup(entries[ei], idx, highDFThresh)
	}

	matchedDG := sc.matchedDG[:0]
	maxActiveSent := -1
	addMatchedDG := func(eIdx int32) {
		if int(eIdx) < 0 || int(eIdx) >= len(entries) {
			return
		}
		entry := entries[eIdx]
		posList := st.entryPositionsOf(entry)
		for pi := range posList {
			s := int(posList[pi] >> 16)
			if wide {
				s = st.entrySentExactAt(entry, pi)
			}
			if s > maxActiveSent {
				maxActiveSent = s
			}
		}
		dg := effG[eIdx]
		for _, prev := range matchedDG {
			if prev == dg {
				return
			}
		}
		matchedDG = append(matchedDG, dg)
	}
	var queryMass float64
	for _, m := range c.matches {
		queryMass += groups[m.group]
		addMatchedDG(m.entry)
		for _, eIdx := range m.positions {
			addMatchedDG(eIdx)
		}
	}
	sc.matchedDG = matchedDG
	// Membership is queried once per document entry and once per folded
	// sentence group; a sorted slice with a binary probe keeps that scan
	// logarithmic instead of quadratic in the matched-group count.
	sortUint64Asc(matchedDG)
	isMatchedDG := func(g uint64) bool {
		lo, hi := 0, len(matchedDG)
		for lo < hi {
			mid := int(uint(lo+hi) >> 1)
			if matchedDG[mid] < g {
				lo = mid + 1
			} else {
				hi = mid
			}
		}
		return lo < len(matchedDG) && matchedDG[lo] == g
	}

	// Sentence-Support Pruning: mark sentences containing at least one matched
	// document group and accumulate docGW in a single pass so unrelated
	// background sentences are never collected or sorted.
	docGW := sc.docGroupW[:0]
	activeSentList := sc.anchorPacked[:0]
	for ei := range entries {
		entry := entries[ei]
		g := effG[ei]
		if !isMatchedDG(g) {
			continue
		}
		w, ok := idx.TermMass(entry.id)
		if !ok || math.IsNaN(w) || math.IsInf(w, 0) || w <= 0 {
			continue
		}
		posList := st.entryPositionsOf(entry)
		for pi := range posList {
			s := int(posList[pi] >> 16)
			if wide {
				s = st.entrySentExactAt(entry, pi)
			}
			if s > maxActiveSent {
				maxActiveSent = s
			}
			activeSentList = append(activeSentList, uint32(s))
		}
		docGW = upsertStructGroupMax(docGW, g, w)
	}
	sc.docGroupW = docGW
	sc.anchorPacked = activeSentList

	var docOverlap float64
	for _, dg := range matchedDG {
		for i := range docGW {
			if docGW[i].group == dg {
				docOverlap += docGW[i].weight
				break
			}
		}
	}

	var sentTotals, sentOverlaps []float64
	if maxActiveSent >= 0 && len(activeSentList) > 0 {
		sGen := sc.nextSentGen(maxActiveSent)
		activeSent := sc.activeSentGen[:maxActiveSent+1]
		for _, s := range activeSentList {
			activeSent[s] = sGen
		}
		sentGW := sc.sentGroupW[:0]
		for ei := range entries {
			entry := entries[ei]
			posList := st.entryPositionsOf(entry)
			if len(posList) == 0 {
				continue
			}
			hitsActive := false
			for pi := range posList {
				s := int(posList[pi] >> 16)
				if wide {
					s = st.entrySentExactAt(entry, pi)
				}
				if s <= maxActiveSent && activeSent[s] == sGen {
					hitsActive = true
					break
				}
			}
			if !hitsActive {
				continue
			}
			w, ok := idx.TermMass(entry.id)
			if !ok || math.IsNaN(w) || math.IsInf(w, 0) || w <= 0 {
				continue
			}
			g := effG[ei]
			for pi := range posList {
				s := int(posList[pi] >> 16)
				if wide {
					s = st.entrySentExactAt(entry, pi)
				}
				if s <= maxActiveSent && activeSent[s] == sGen {
					sentGW = append(sentGW, rmStructSentGroupW{sent: s, group: g, weight: w})
				}
			}
		}
		sc.sentGroupW = sentGW
		if len(sentGW) > 0 {
			if cap(sc.sentTotals) < maxActiveSent+1 {
				sc.sentTotals = make([]float64, maxActiveSent+1)
				sc.sentOverlaps = make([]float64, maxActiveSent+1)
			}
			sentTotals = sc.sentTotals[:maxActiveSent+1]
			sentOverlaps = sc.sentOverlaps[:maxActiveSent+1]
			clear(sentTotals)
			clear(sentOverlaps)

			for _, item := range sc.foldSentGroups(sentGW) {
				sentTotals[item.sent] += item.weight
				if isMatchedDG(item.group) {
					sentOverlaps[item.sent] += item.weight
				}
			}
		}
	}

	queryCoverage := math.Min(1.0, queryMass/total)
	prior := st.sentenceEnergyMean
	purity := math.Min(1.0, docOverlap/(fd.docTotal+prior))
	for s, stot := range sentTotals {
		if stot > 0 && sentOverlaps[s] > 0 {
			if p := math.Min(1.0, sentOverlaps[s]/(stot+prior)); p > purity {
				purity = p
			}
		}
	}
	maximum := math.Max(queryCoverage, purity)
	return queryMass * (math.Sqrt(queryCoverage*purity) + maximum*maximum/eulerNumber)
}

type rmScoredCandidate struct {
	unit         int32
	score        float64
	remaining    float64
	epoch        int
	structureSet bool
}

func rmHeapLess(a, b rmScoredCandidate) bool {
	if a.score != b.score {
		return a.score > b.score
	}
	return a.unit < b.unit
}

func rmHeapPush(h []rmScoredCandidate, item rmScoredCandidate) []rmScoredCandidate {
	h = append(h, item)
	i := len(h) - 1
	for i > 0 {
		parent := (i - 1) / 2
		if !rmHeapLess(h[i], h[parent]) {
			break
		}
		h[i], h[parent] = h[parent], h[i]
		i = parent
	}
	return h
}

func rmHeapPop(h []rmScoredCandidate) (rmScoredCandidate, []rmScoredCandidate) {
	top := h[0]
	last := len(h) - 1
	h[0] = h[last]
	h = h[:last]
	i := 0
	for {
		left := 2*i + 1
		if left >= len(h) {
			break
		}
		best := left
		if right := left + 1; right < len(h) && rmHeapLess(h[right], h[left]) {
			best = right
		}
		if !rmHeapLess(h[best], h[i]) {
			break
		}
		h[i], h[best] = h[best], h[i]
		i = best
	}
	return top, h
}

func (c *rmCandidate) stage2Bounds(unitStructGate []float64, u int32, groups map[uint64]float64, total float64) (base, structBound, remBound float64) {
	var matched, qRem float64
	for _, m := range c.matches {
		matched += groups[m.group]
		qRem += m.mass
	}
	qRem /= eulerNumber
	compBound := 0.0
	if c.lex > 0 {
		compBound = transmissionPerHop * transmissionPerHop * c.lex * (1.0 - math.Exp(-c.lex/total))
	}
	structBound = gateAt(unitStructGate, u) * matched * (1.0 + transmissionPerHop)
	if c.structureSet {
		structBound = c.structure
	}
	remBound = qRem + compBound
	base = c.lex + c.phrase + c.entity + c.stSpread + c.stCorrob + remBound
	return base, structBound, remBound
}

func (st *rmClusterState) selectClusterHits(idx *Index, sc *rmQueryScratch, candidates []rmCandidate, unitStructGate []float64, detachedFrag map[uint64]bool, qTerms map[uint64]Term, groups map[uint64]float64, total, muQ float64, topK int, saturation SaturationMode) []Hit {
	limit := idx.N
	if topK > 0 && topK < limit {
		limit = topK
	}
	coverage := sc.selectCoverage
	if coverage == nil {
		coverage = make(map[uint64]int, len(groups))
		sc.selectCoverage = coverage
	} else {
		clear(coverage)
	}
	compCov := sc.selectCompCov
	if compCov == nil {
		compCov = make(map[uint64]float64, 128)
		sc.selectCompCov = compCov
	} else {
		clear(compCov)
	}
	focalComps := sc.focalComps[:idx.N]
	focalRawSum := sc.focalRawSum[:idx.N]
	focalBuilt := sc.focalBuilt[:idx.N]
	highDFThresh := idx.HighDFThreshold()

	buildFocal := func(u int32, c *rmCandidate) ([]rmFocalTermWeight, float64) {
		if focalBuilt[u] {
			return focalComps[u], focalRawSum[u]
		}
		focalBuilt[u] = true
		if len(c.matches) == 0 {
			focalComps[u] = nil
			focalRawSum[u] = 0
			return nil, 0
		}
		entries := st.docEntriesOf(u)
		anchorPacked := sc.anchorPacked[:0]
		wideFocal := st.hasWidePos()
		var anchorExact []TermPosition
		for _, m := range c.matches {
			if m.entry < 0 {
				// Mass-only synthetic matches carry no local positions.
				continue
			}
			if detachedFrag != nil && detachedFrag[m.group] {
				continue
			}
			if len(m.positions) == 0 {
				anchorPacked = append(anchorPacked, st.entryPositionsOf(entries[m.entry])...)
				if wideFocal {
					anchorExact = append(anchorExact, st.entryPositionsExactOf(entries[m.entry])...)
				}
			} else {
				for _, eIdx := range m.positions {
					anchorPacked = append(anchorPacked, st.entryPositionsOf(entries[eIdx])...)
					if wideFocal {
						anchorExact = append(anchorExact, st.entryPositionsExactOf(entries[eIdx])...)
					}
				}
			}
		}
		sc.anchorPacked = anchorPacked
		if len(anchorPacked) == 0 {
			focalComps[u] = nil
			focalRawSum[u] = 0
			return nil, 0
		}
		if len(anchorPacked) > 1 {
			sortUint32Asc(anchorPacked)
		}
		if len(anchorExact) > 1 {
			sort.Sort(termPositionSort(anchorExact))
		}
		focalCands := sc.focalCands[:0]
		for _, entry := range entries {
			if role, _ := UnpackTermRoleExt(entry.roleExt); role == RoleClosedBeacon && entry.flags&rmFlagCompanion == 0 {
				continue
			}
			if _, inQ := qTerms[entry.id]; inQ {
				continue
			}
			g := entry.group
			if g == 0 {
				g = entry.id
			}
			if c.hasGroup(g) {
				continue
			}
			w, _ := idx.TermMass(g)
			if w <= muQ {
				continue
			}
			var minDist int
			if wideFocal {
				minDist = closestExactSlot(st.entryPositionsExactOf(entry), anchorExact)
				if minDist < 0 {
					ePos := st.entryPositionsExactOf(entry)
					for _, ep := range ePos {
						for _, ap := range anchorExact {
							if ep.Sentence-ap.Sentence != 1 && ap.Sentence-ep.Sentence != 1 {
								continue
							}
							dSlot := ep.Slot - ap.Slot
							if dSlot < 0 {
								dSlot = -dSlot
							}
							candD := dSlot + 2
							if minDist < 0 || candD < minDist {
								minDist = candD
							}
						}
					}
				}
			} else {
				minDist = closestPackedSlot(st.entryPositionsOf(entry), anchorPacked)
				if minDist < 0 {
					ePos := st.entryPositionsOf(entry)
					for _, ep := range ePos {
						eSent, eSlot := int(ep>>16), int(ep&0xFFFF)
						for _, ap := range anchorPacked {
							aSent, aSlot := int(ap>>16), int(ap&0xFFFF)
							if eSent-aSent != 1 && aSent-eSent != 1 {
								continue
							}
							dSlot := eSlot - aSlot
							if dSlot < 0 {
								dSlot = -dSlot
							}
							candD := dSlot + 2
							if minDist < 0 || candD < minDist {
								minDist = candD
							}
						}
					}
				}
			}
			if minDist >= 0 {
				if minDist < 1 {
					minDist = 1
				}
				foundCG := false
				for i := range focalCands {
					if focalCands[i].group == g {
						if minDist < focalCands[i].minDist {
							focalCands[i].minDist = minDist
						}
						foundCG = true
						break
					}
				}
				if !foundCG {
					focalCands = append(focalCands, rmFocalCandDist{group: g, minDist: minDist})
				}
			}
		}
		sc.focalCands = focalCands
		if len(focalCands) == 0 {
			focalComps[u] = nil
			focalRawSum[u] = 0
			return nil, 0
		}
		fStart := len(sc.focalPool)
		var rawSum float64
		for _, fc := range focalCands {
			kern := math.Exp(-float64(fc.minDist-1) / eulerNumber)
			mG, _ := idx.TermMass(fc.group)
			tw := mG * kern
			sc.focalPool = append(sc.focalPool, rmFocalTermWeight{group: fc.group, weight: tw, localW: kern})
			rawSum += tw
		}
		fcs := sc.focalPool[fStart:len(sc.focalPool):len(sc.focalPool)]
		sort.Sort(focalTermWeightSort(fcs))
		focalComps[u] = fcs
		focalRawSum[u] = rawSum
		return fcs, rawSum
	}

	evalRemaining := func(u int32, c *rmCandidate) float64 {
		var rem float64
		switch saturation {
		case SaturationPool:
			// 池感知饱和：衰减权重取查询面组质量（𝓕 边际），同组多条目的匹配数
			// 由逐条累加隐式计入。
			for _, m := range c.matches {
				rem += groups[m.group] * math.Exp(-float64(coverage[m.group]))
			}
		case SaturationGeometric:
			// 几何中项：同时承载单元证据质量与查询面价值；两组分的量纲均为
			// Nats，几何中项量纲齐次，任一侧为零时该项自然为零。
			for _, m := range c.matches {
				rem += math.Sqrt(m.mass*groups[m.group]) * math.Exp(-float64(coverage[m.group]))
			}
		default:
			for _, m := range c.matches {
				rem += m.mass * math.Exp(-float64(coverage[m.group]))
			}
		}
		qRem := rem / eulerNumber
		if c.lex <= 0 {
			return qRem
		}
		fcs, compTot := buildFocal(u, c)
		if len(fcs) == 0 || compTot <= 0 {
			return qRem
		}
		var compNovel float64
		for _, ft := range fcs {
			// Exclude query anchors from focal novel gain
			if _, isQuery := groups[ft.group]; isQuery {
				continue
			}
			compNovel += ft.weight * math.Exp(-compCov[ft.group])
		}
		pAnchor := 1.0 - math.Exp(-c.lex/total)
		etaNovel := compNovel / (compTot + muQ)
		return qRem + transmissionTwoHop*c.lex*pAnchor*etaNovel
	}
	queue := sc.heapQueue[:0]
	for _, u := range sc.dirtyUnits {
		c := &candidates[u]
		if c.lex+c.stSpread+c.stCorrob <= 0 {
			continue
		}
		base, structBound, remBound := c.stage2Bounds(unitStructGate, u, groups, total)
		queue = rmHeapPush(queue, rmScoredCandidate{
			unit:         u,
			score:        base + structBound,
			remaining:    remBound,
			epoch:        0,
			structureSet: false,
		})
	}
	profCache := sc.selectProfCache
	if profCache != nil {
		clear(profCache)
	}
	getLightProf := func(u int32) unitGroupProfile {
		if profCache == nil {
			profCache = make(map[int32]unitGroupProfile, 16)
			sc.selectProfCache = profCache
		}
		if p, ok := profCache[u]; ok {
			return p
		}
		p := st.buildUnitGroupProfileWithPos(idx, sc, u, highDFThresh, false)
		profCache[u] = p
		return p
	}
	out := make([]Hit, 0, limit)
	for len(out) < limit && len(queue) > 0 {
		var best rmScoredCandidate
		best, queue = rmHeapPop(queue)
		c := &candidates[best.unit]
		if !best.structureSet || best.epoch != len(out) {
			if !best.structureSet {
				if !c.structureSet {
					c.structure = gateAt(unitStructGate, best.unit) * st.computeStructuredIncrement(idx, sc, c, groups, total)
					c.structureSet = true
				}
				best.structureSet = true
			}
			rem := evalRemaining(best.unit, c)
			best.remaining = rem
			rawScore := c.lex + c.phrase + c.entity + c.structure + rem + c.stSpread + c.stCorrob
			if c.clonePenalty > 0 {
				rawScore *= math.Max(0, 1.0-cloneDampingKernel*c.clonePenalty)
			}
			best.score = rawScore
			best.epoch = len(out)
			queue = rmHeapPush(queue, best)
			continue
		}
		direct := c.lex + c.phrase + c.entity + c.structure + best.remaining
		clusterSpread := c.stSpread
		clusterCorroboration := c.stCorrob
		if c.clonePenalty > 0 {
			scale := math.Max(0, 1.0-cloneDampingKernel*c.clonePenalty)
			direct *= scale
			clusterSpread *= scale
			clusterCorroboration *= scale
		}
		energy := direct + clusterSpread + clusterCorroboration
		if energy <= 0 {
			continue
		}
		if channelBreakdownProbe != nil {
			scale := 1.0
			if c.clonePenalty > 0 {
				scale = math.Max(0, 1.0-cloneDampingKernel*c.clonePenalty)
			}
			channelBreakdownProbe(ChannelBreakdown{
				UnitID:       string([]byte(idx.UnitIDs[best.unit])),
				Lex:          c.lex,
				Phrase:       c.phrase,
				Entity:       c.entity,
				Structure:    c.structure,
				Remaining:    best.remaining,
				StSpread:     c.stSpread,
				StCorrob:     c.stCorrob,
				ClonePenalty: c.clonePenalty,
				Scale:        scale,
				Energy:       energy,
			})
		}
		out = append(out, Hit{
			UnitID: string([]byte(idx.UnitIDs[best.unit])),
			Energy: energy,
			Direct: direct,
			Hop1:   clusterSpread,
			Hop2:   clusterCorroboration,
		})
		for _, m := range c.matches {
			coverage[m.group]++
		}
		for _, ft := range focalComps[best.unit] {
			compCov[ft.group] += ft.localW
		}
		var profBest unitGroupProfile
		var profBestSet bool
		st.visitNeighbors(best.unit, func(nb int32) bool {
			if nb == best.unit {
				return true
			}
			nbC := &candidates[nb]
			if nbC.lex+nbC.stSpread+nbC.stCorrob <= 0 {
				return true
			}
			aff := st.temporalAffinity(idx, best.unit, nb)
			if aff < subFloor {
				return true
			}
			// Falsification Guard P6 (Query Match Subsumption Conservation):
			// Candidate nb can only be a redundant echo clone of best.unit if
			// every core query group matched by nb is already covered by best.unit.
			querySubsumed := true
			for _, nm := range nbC.matches {
				if !detachedFrag[nm.group] && !c.hasGroup(nm.group) {
					querySubsumed = false
					break
				}
			}
			if querySubsumed {
				if !profBestSet {
					profBest = getLightProf(best.unit)
					profBestSet = true
				}
				if red := evalBurstCloneRedundancyFromProfiles(profBest, getLightProf(nb), aff); red > nbC.clonePenalty {
					nbC.clonePenalty = red
				}
			}
			// Negative Duality 2- (Unrelated Same-Timestamp Bystander Loan Revocation):
			// Even if nb matched a minor secondary query word, if its core query mass is
			// strictly weaker than best.unit while sitting in the same tight time window
			// with orthogonal subject content (cos_subj < 1/e^2 and C_{nb->s} < 1/e^2),
			// revoke its borrowed spacetime Hop inflation.
			if nbC.coreLex < c.coreLex*(1.0-transmissionPerHop*transmissionPerHop) {
				rawCos := st.docSubjectCos(idx, sc, best.unit, nb)
				if rawCos < transmissionPerHop*transmissionPerHop {
					if !profBestSet {
						profBest = getLightProf(best.unit)
						profBestSet = true
					}
					// Unit-index order blindness: nb's structure term is lazily
					// materialized (the endorsement pre-pass bound filter only
					// materializes candidates scanned before the running peak in
					// unit-index order, and the heap refresh may not have reached
					// nb yet). Reading a half-materialized direct energy would
					// couple the damping dose to the UnitID sort order, so
					// materialize it with the exact refresh formula first.
					if !nbC.structureSet {
						nbC.structure = gateAt(unitStructGate, nb) * st.computeStructuredIncrement(idx, sc, nbC, groups, total)
						nbC.structureSet = true
					}
					nbHop := nbC.stSpread + nbC.stCorrob
					nbDirect := nbC.lex + nbC.phrase + nbC.entity + nbC.structure
					if bd := evalUnrelatedBystanderBorrowFromProfiles(profBest, getLightProf(nb), aff, rawCos, c.coreLex, nbC.coreLex, nbDirect, nbHop); bd > nbC.clonePenalty {
						nbC.clonePenalty = bd
					}
				}
			}
			return true
		})
	}
	sc.heapQueue = queue[:0]
	return out
}
