// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cluster

import (
	"math"
	"sort"
)

// applyPhase3ClusterHook 在前两簇（EntityCluster + SpacetimeCluster）结算完毕后，
// 构造只读候选快照并调用已挂载的 Phase3Plugin 外挂，将返回的非负关系簇增益
// （Hop1Nats / Hop2Nats）注入候选能量场。
func (e *Engine) applyPhase3ClusterHook(idx *Index, st *rmClusterState, sc *rmQueryScratch, candidates []rmCandidate, unitStructGate []float64, qTerms map[uint64]Term, groups map[uint64]float64, total float64) {
	stage2 := make([]Phase3Candidate, 0, len(sc.dirtyUnits))
	for _, u := range sc.dirtyUnits {
		c := &candidates[u]
		if c.lex+c.stSpread+c.stCorrob <= 0 {
			continue
		}
		gate := 1.0
		if len(unitStructGate) > int(u) {
			gate = unitStructGate[u]
		}
		if !c.structureSet {
			c.structure = gate * st.computeStructuredIncrement(idx, sc, c, groups, total)
			c.structureSet = true
		}
		e2 := c.lex + c.phrase + c.entity + c.structure + c.stSpread + c.stCorrob
		if e2 <= 0 {
			continue
		}
		stage2 = append(stage2, Phase3Candidate{
			Unit:        u,
			DocID:       idx.UnitIDs[u],
			Stage2Nats:  e2,
			CoreLexNats: c.coreLex,
			StructGate:  gate,
		})
	}
	if len(stage2) == 0 {
		return
	}
	sort.Slice(stage2, func(i, j int) bool {
		if stage2[i].Stage2Nats != stage2[j].Stage2Nats {
			return stage2[i].Stage2Nats > stage2[j].Stage2Nats
		}
		return stage2[i].Unit < stage2[j].Unit
	})
	addedNew := false
	dGen := sc.dirtyGen
	for _, delta := range e.Phase3Plugin.AugmentRMStage2(idx, qTerms, groups, total, stage2) {
		if delta.Unit < 0 || int(delta.Unit) >= len(candidates) {
			continue
		}
		h1 := !math.IsNaN(delta.Hop1Nats) && !math.IsInf(delta.Hop1Nats, 0) && delta.Hop1Nats > 0
		h2 := !math.IsNaN(delta.Hop2Nats) && !math.IsInf(delta.Hop2Nats, 0) && delta.Hop2Nats > 0
		if !h1 && !h2 {
			continue
		}
		if sc.unitDirtyGen[delta.Unit] != dGen {
			sc.unitDirtyGen[delta.Unit] = dGen
			sc.dirtyUnits = append(sc.dirtyUnits, delta.Unit)
			unitStructGate[delta.Unit] = 1.0
			addedNew = true
		}
		c := &candidates[delta.Unit]
		if h1 {
			c.stSpread += delta.Hop1Nats
		}
		if h2 {
			c.stCorrob += delta.Hop2Nats
		}
	}
	if addedNew {
		sort.Slice(sc.dirtyUnits, func(i, j int) bool { return sc.dirtyUnits[i] < sc.dirtyUnits[j] })
	}
}

func (st *rmClusterState) closestFamilySlot(entries []rmDocEntry, a, b rmFeatureMatch) int {
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

func (st *rmClusterState) rmProximityAndSynergy(c *rmCandidate, groups map[uint64]float64, total float64) (float64, float64) {
	if total <= 0 || len(c.matches) < 2 {
		return 0, 0
	}
	entries := st.docEntriesOf(c.unit)
	var prox, syn float64
	const invE2 = 1.0 / (math.E * math.E)
	for i, a := range c.matches {
		ma := groups[a.group]
		for _, b := range c.matches[i+1:] {
			closest := st.closestFamilySlot(entries, a, b)
			if closest < 0 {
				continue
			}
			d := float64(closest)
			if d < 1 {
				d = 1
			}
			mb := groups[b.group]
			if ma+mb > 0 {
				prox += 2 * ma * mb / (ma + mb) * math.Exp(-(d-1)*invE)
			}
			syn += a.mass * (b.mass / total) * math.Exp(-d*invE2)
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
	highDFThresh := idx.HighDFThreshold()

	matchedDG := sc.matchedDG[:0]
	maxActiveSent := -1
	addMatchedDG := func(eIdx int32) {
		if int(eIdx) < 0 || int(eIdx) >= len(entries) {
			return
		}
		entry := entries[eIdx]
		for _, p := range st.entryPositionsOf(entry) {
			if s := int(p >> 16); s > maxActiveSent {
				maxActiveSent = s
			}
		}
		dg := effectiveEntryGroup(entry, idx, highDFThresh)
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
	isMatchedDG := func(g uint64) bool {
		for _, dg := range matchedDG {
			if dg == g {
				return true
			}
		}
		return false
	}

	// Sentence-Support Pruning: mark sentences containing at least one matched
	// document group and accumulate docGW in a single pass so unrelated
	// background sentences are never collected or sorted.
	docGW := sc.docGroupW[:0]
	activeSentList := sc.anchorPacked[:0]
	for _, entry := range entries {
		g := effectiveEntryGroup(entry, idx, highDFThresh)
		if !isMatchedDG(g) {
			continue
		}
		w, ok := idx.TermMass(entry.id)
		if !ok || math.IsNaN(w) || math.IsInf(w, 0) || w <= 0 {
			continue
		}
		for _, p := range st.entryPositionsOf(entry) {
			s := int(p >> 16)
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
		for _, entry := range entries {
			posList := st.entryPositionsOf(entry)
			if len(posList) == 0 {
				continue
			}
			hitsActive := false
			for _, p := range posList {
				if s := int(p >> 16); s <= maxActiveSent && activeSent[s] == sGen {
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
			g := effectiveEntryGroup(entry, idx, highDFThresh)
			for _, p := range posList {
				if s := int(p >> 16); s <= maxActiveSent && activeSent[s] == sGen {
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

			for _, item := range sortAndFoldSentGroups(sentGW) {
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
	return queryMass * (math.Sqrt(queryCoverage*purity) + maximum*maximum/math.E)
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
	qRem /= math.E
	compBound := 0.0
	if c.lex > 0 {
		compBound = invE * invE * c.lex * (1.0 - math.Exp(-c.lex/total))
	}
	gate := 1.0
	if len(unitStructGate) > int(u) {
		gate = unitStructGate[u]
	}
	structBound = gate * matched * (1.0 + invE)
	if c.structureSet {
		structBound = c.structure
	}
	remBound = qRem + compBound
	base = c.lex + c.phrase + c.entity + c.stSpread + c.stCorrob + remBound
	return base, structBound, remBound
}

func (st *rmClusterState) selectClusterHits(idx *Index, sc *rmQueryScratch, candidates []rmCandidate, unitStructGate []float64, detachedFrag map[uint64]bool, qTerms map[uint64]Term, groups map[uint64]float64, total, muQ float64, topK int) []Hit {
	limit := idx.N
	if topK > 0 && topK < limit {
		limit = topK
	}
	coverage := make(map[uint64]int, len(groups))
	compCov := make(map[uint64]float64, 128)
	focalComps := sc.focalComps[:idx.N]
	focalRawSum := sc.focalRawSum[:idx.N]
	focalBuilt := sc.focalBuilt[:idx.N]
	highDFThresh := idx.HighDFThreshold()
	const compScale = invE * invE

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
		for _, m := range c.matches {
			if detachedFrag != nil && detachedFrag[m.group] {
				continue
			}
			if len(m.positions) == 0 {
				anchorPacked = append(anchorPacked, st.entryPositionsOf(entries[m.entry])...)
			} else {
				for _, eIdx := range m.positions {
					anchorPacked = append(anchorPacked, st.entryPositionsOf(entries[eIdx])...)
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
			sort.Slice(anchorPacked, func(i, j int) bool { return anchorPacked[i] < anchorPacked[j] })
		}
		focalCands := sc.focalCands[:0]
		for _, entry := range entries {
			if role, _ := unpackTermRoleExt(entry.roleExt); role == RoleClosedBeacon && entry.flags&rmFlagCompanion == 0 {
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
			minDist := closestPackedSlot(st.entryPositionsOf(entry), anchorPacked)
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
			kern := math.Exp(-float64(fc.minDist-1) / math.E)
			mG, _ := idx.TermMass(fc.group)
			tw := mG * kern
			sc.focalPool = append(sc.focalPool, rmFocalTermWeight{group: fc.group, weight: tw, localW: kern})
			rawSum += tw
		}
		fcs := sc.focalPool[fStart:len(sc.focalPool):len(sc.focalPool)]
		sort.Slice(fcs, func(i, j int) bool { return fcs[i].group < fcs[j].group })
		focalComps[u] = fcs
		focalRawSum[u] = rawSum
		return fcs, rawSum
	}

	evalRemaining := func(u int32, c *rmCandidate) float64 {
		var rem float64
		for _, m := range c.matches {
			rem += m.mass * math.Exp(-float64(coverage[m.group]))
		}
		qRem := rem / math.E
		if c.lex <= 0 {
			return qRem
		}
		fcs, compTot := buildFocal(u, c)
		if len(fcs) == 0 || compTot <= 0 {
			return qRem
		}
		var compNovel float64
		for _, ft := range fcs {
			// G-04: Exclude query anchors from focal novel gain
			if _, isQuery := groups[ft.group]; isQuery {
				continue
			}
			compNovel += ft.weight * math.Exp(-compCov[ft.group])
		}
		pAnchor := 1.0 - math.Exp(-c.lex/total)
		etaNovel := compNovel / (compTot + muQ)
		return qRem + compScale*c.lex*pAnchor*etaNovel
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
	subFloor := 1.0 - invE
	var profCache map[int32]unitGroupProfile
	getLightProf := func(u int32) unitGroupProfile {
		if profCache == nil {
			profCache = make(map[int32]unitGroupProfile, 16)
		}
		if p, ok := profCache[u]; ok {
			return p
		}
		p := st.buildUnitGroupProfileWithPos(idx, u, highDFThresh, false)
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
					gate := 1.0
					if len(unitStructGate) > int(best.unit) {
						gate = unitStructGate[best.unit]
					}
					c.structure = gate * st.computeStructuredIncrement(idx, sc, c, groups, total)
					c.structureSet = true
				}
				best.structureSet = true
			}
			rem := evalRemaining(best.unit, c)
			best.remaining = rem
			rawScore := c.lex + c.phrase + c.entity + c.structure + rem + c.stSpread + c.stCorrob
			if c.clonePenalty > 0 {
				rawScore *= (1.0 - invE*invE*c.clonePenalty)
			}
			best.score = rawScore
			best.epoch = len(out)
			queue = rmHeapPush(queue, best)
			continue
		}
		direct := c.lex + c.phrase + c.entity + c.structure + best.remaining
		hop1 := c.stSpread
		hop2 := c.stCorrob
		if c.clonePenalty > 0 {
			scale := 1.0 - invE*invE*c.clonePenalty
			direct *= scale
			hop1 *= scale
			hop2 *= scale
		}
		energy := direct + hop1 + hop2
		if energy <= 0 {
			continue
		}
		out = append(out, Hit{
			UnitID: string([]byte(idx.UnitIDs[best.unit])),
			Energy: energy,
			Direct: direct,
			Hop1:   hop1,
			Hop2:   hop2,
		})
		for _, m := range c.matches {
			coverage[m.group]++
		}
		for _, ft := range focalComps[best.unit] {
			compCov[ft.group] += ft.localW
		}
		var profBest unitGroupProfile
		var profBestSet bool
		for _, nb := range st.neighborsOf(best.unit) {
			if nb == best.unit {
				continue
			}
			nbC := &candidates[nb]
			if nbC.lex+nbC.stSpread+nbC.stCorrob <= 0 {
				continue
			}
			aff := st.temporalAffinity(idx, best.unit, nb)
			if aff < subFloor {
				continue
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
			if nbC.coreLex < c.coreLex*(1.0-invE*invE) {
				rawCos := st.docSubjectCos(idx, best.unit, nb)
				if rawCos < invE*invE {
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
						gate := 1.0
						if len(unitStructGate) > int(nb) {
							gate = unitStructGate[nb]
						}
						nbC.structure = gate * st.computeStructuredIncrement(idx, sc, nbC, groups, total)
						nbC.structureSet = true
					}
					nbHop := nbC.stSpread + nbC.stCorrob
					nbDirect := nbC.lex + nbC.phrase + nbC.entity + nbC.structure
					if bd := evalUnrelatedBystanderBorrowFromProfiles(profBest, getLightProf(nb), aff, rawCos, c.coreLex, nbC.coreLex, nbDirect, nbHop); bd > nbC.clonePenalty {
						nbC.clonePenalty = bd
					}
				}
			}
		}
	}
	sc.heapQueue = queue[:0]
	return out
}
