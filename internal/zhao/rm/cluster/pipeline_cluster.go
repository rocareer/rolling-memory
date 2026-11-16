// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cluster

import (
	"math"
	"sort"
)

func (e *Engine) searchClustersWithPositions(qTerms map[uint64]Term, qPositions map[uint64][]TermPosition, topK int) []Hit {
	idx := e.Index
	st := e.ensureClusterState()

	queryHashes := make([]uint64, 0, len(qTerms))
	for h := range qTerms {
		queryHashes = append(queryHashes, h)
	}
	sort.Slice(queryHashes, func(i, j int) bool { return queryHashes[i] < queryHashes[j] })

	const (
		rmBitUpper          uint8 = 1 << 0
		rmBitCamel          uint8 = 1 << 1
		rmBitDigit          uint8 = 1 << 2
		rmBitConn           uint8 = 1 << 3
		rmBitFocal          uint8 = 1 << 5
		rmBitBoundaryQuote  uint8 = 1 << 6
		rmBitQuantity       uint8 = 1 << 7
		rmRigidMask         uint8 = rmBitUpper | rmBitCamel | rmBitDigit | rmBitFocal | rmBitBoundaryQuote | rmBitQuantity
		rmCaseFreeRigidMask uint8 = rmBitDigit | rmBitFocal | rmBitQuantity
		rmProperMask        uint8 = rmBitUpper | rmBitCamel
		noDay               int64 = -1 << 62
	)
	highDFThresh := idx.HighDFThreshold()
	meanBits := st.entryMeanBits

	hashGroup := make(map[uint64]uint64, len(queryHashes))
	detachedFrag := make(map[uint64]bool, len(queryHashes))
	detachedSlot2 := make(map[uint64]bool, len(queryHashes))
	famMinDF := make(map[uint64]int, len(queryHashes))
	for _, h := range queryHashes {
		baseline, ok := idx.TermMass(h)
		if !ok || baseline <= 0 {
			continue
		}
		qTerm := qTerms[h]
		group := qTerm.Group
		if group == 0 {
			if pg, ok := st.parentGroupOf(h); ok && pg != 0 && qTerm.Flags&rmFlagCompanion != 0 {
				group = pg
			} else {
				group = h
			}
		}
		df := idx.TermDF(h)
		isPrim := st.isPrimaryOf(h)
		if df <= highDFThresh && isPrim {
			if prevDF, exists := famMinDF[group]; !exists || df < prevDF {
				famMinDF[group] = df
			}
		}
		origA, okA := qTerms[group]
		isSlot2 := okA && qTerm.IsSlot2Companion(origA)
		isMicroBoundSfx := isSlot2 && !isPrim && float64(idx.N) <= math.E*math.E*math.E && df >= 2
		if h != group && (df > highDFThresh || isMicroBoundSfx) {
			keepInHighDFHost := idx.TermDF(group) > highDFThresh && origA.Flags&rmProperMask == 0 && qTerm.Role != RoleClosedBeacon
			if !keepInHighDFHost {
				group = h
				detachedFrag[group] = true
				if isSlot2 {
					detachedSlot2[group] = true
				}
			}
		}
		hashGroup[h] = group
	}

	properSlots := make(map[TermPosition]int)
	var compoundPartner map[uint64]uint64
	if len(qPositions) > 0 {
		primaryAtPos := make(map[TermPosition]uint64, len(qTerms)*2)
		for h, qa := range qTerms {
			if qa.Role == RolePrimary {
				for _, p := range qPositions[h] {
					if prev, exists := primaryAtPos[p]; !exists || idx.TermDF(prev) > 0 {
						primaryAtPos[p] = h
					}
				}
			}
		}
		for h0, qa0 := range qTerms {
			if qa0.Role != RolePrimary || idx.TermDF(h0) > 0 || qa0.Flags&rmBitConn != 0 || qa0.Flags&(rmBitCamel|rmBitDigit) == 0 || len(qPositions[h0]) == 0 {
				continue
			}
			p0 := qPositions[h0][0]
			h1, ok1 := primaryAtPos[TermPosition{Sentence: p0.Sentence, Slot: p0.Slot + 1}]
			h2, ok2 := primaryAtPos[TermPosition{Sentence: p0.Sentence, Slot: p0.Slot + 2}]
			if !ok1 || !ok2 || h1 == h0 || h2 == h0 || h1 == h2 {
				continue
			}
			if len(qPositions[h1]) < 2 || qPositions[h1][0] != p0 || len(qPositions[h2]) == 0 || qPositions[h2][0] == p0 {
				continue
			}
			g1, g2 := hashGroup[h1], hashGroup[h2]
			if g1 == 0 || g2 == 0 || g1 == g2 {
				continue
			}
			b0 := int(qa0.Bits)
			b2 := int(qTerms[h2].Bits)
			if b2 < b0 && float64(b0-b2)*math.E < meanBits {
				if compoundPartner == nil {
					compoundPartner = make(map[uint64]uint64, 2)
				}
				compoundPartner[g1] = g2
			}
		}
	}
	for h, qa := range qTerms {
		if idx.TermDF(h) > 0 && qa.Flags&rmProperMask != 0 && qa.Flags&rmFlagCompanion == 0 {
			if posList := qPositions[h]; len(posList) > 0 {
				for _, p := range posList {
					properSlots[p]++
				}
			} else if qa.Sent > 0 && qa.Slot > 0 {
				properSlots[TermPosition{Sentence: int(qa.Sent - 1), Slot: int(qa.Slot)}]++
			}
		}
	}
	hasAdjacentProper := func(h uint64, qa Term) bool {
		if qa.Flags&rmBitCamel != 0 || qa.Flags&rmFlagCompanion != 0 {
			return true
		}
		checkPos := func(p TermPosition) bool {
			if properSlots[p] > 1 {
				return true
			}
			for _, d := range [3]int{-2, -1, 1} {
				if properSlots[TermPosition{Sentence: p.Sentence, Slot: p.Slot + d}] > 0 {
					return true
				}
			}
			return false
		}
		if posList := qPositions[h]; len(posList) > 0 {
			for _, p := range posList {
				if checkPos(p) {
					return true
				}
			}
			return false
		}
		if qa.Sent > 0 && qa.Slot > 0 {
			return checkPos(TermPosition{Sentence: int(qa.Sent - 1), Slot: int(qa.Slot)})
		}
		return false
	}

	groupUnits := make(map[uint64]map[int32]struct{}, len(queryHashes))
	groupAnchorUnits := make(map[uint64]map[int32]struct{}, len(queryHashes))
	groupDays := make(map[uint64]map[int64]struct{}, len(queryHashes))
	groupRigid := make(map[uint64]bool, len(queryHashes))
	groupProper := make(map[uint64]bool, len(queryHashes))
	hasBroadComp := make(map[uint64]bool, len(queryHashes))
	for _, h := range queryHashes {
		g, ok := hashGroup[h]
		if !ok {
			continue
		}
		qa := qTerms[h]
		bits := qa.Bits
		if bits == 0 {
			bits = idx.TermBitsOf(h)
		}
		dfH := idx.TermDF(h)
		isTrueProper := qa.Flags&rmProperMask != 0 && float64(bits)*math.E >= meanBits && dfH <= highDFThresh
		if isTrueProper && float64(dfH) > math.E*math.E && st.properRatioOf(h) < invE && !hasAdjacentProper(h, qa) {
			isTrueProper = false
		}
		if qa.Flags&rmCaseFreeRigidMask != 0 || (qa.Flags&rmBitConn != 0 && qa.Flags&rmFlagCompanion == 0) || isTrueProper {
			groupRigid[g] = true
		}
		if isTrueProper {
			groupProper[g] = true
		}
		if dfH > highDFThresh {
			continue
		}
		um := groupUnits[g]
		if um == nil {
			um = make(map[int32]struct{})
			groupUnits[g] = um
		}
		aum := groupAnchorUnits[g]
		if aum == nil {
			aum = make(map[int32]struct{})
			groupAnchorUnits[g] = aum
		}
		dm := groupDays[g]
		if dm == nil {
			dm = make(map[int64]struct{})
			groupDays[g] = dm
		}
		dfG := idx.TermDF(g)
		baseDF := dfG
		if baseDF == 0 {
			baseDF = famMinDF[g]
		}
		gBits := qTerms[g].Bits
		if gBits == 0 {
			gBits = idx.TermBitsOf(g)
		}
		isSlot2Comp := h != g && qa.IsSlot2Companion(qTerms[g])
		isBroadComp := h != g && baseDF > 0 && (float64(dfH) > math.E*float64(baseDF) || (isSlot2Comp && dfH > dfG))
		if isBroadComp {
			hasBroadComp[g] = true
		}
		if !isSlot2Comp && float64(gBits)*2.0 < meanBits && qTerms[g].Flags&rmProperMask != 0 {
			isBroadComp = false
		}
		for _, p := range st.termPostings(h) {
			um[p.unit] = struct{}{}
			if !isBroadComp {
				aum[p.unit] = struct{}{}
				if d := st.fastDocs[p.unit].day; d != noDay {
					dm[d] = struct{}{}
				}
			}
		}
	}

	totalDaysM := st.totalDaysM
	kappaOcc := make(map[uint64]float64, len(groupUnits))
	anchorKappa := make(map[uint64]float64, len(groupUnits))
	for g, um := range groupUnits {
		n := float64(len(um))
		dObs := float64(len(groupDays[g]))
		if n > 1 && dObs >= 1 {
			eNull := totalDaysM * (1.0 - math.Exp(-n/totalDaysM))
			denom := math.Max(1.0, eNull-1.0)
			kappaOcc[g] = ((n - 1.0) / (n + 2.0)) * math.Max(0, 1.0-(dObs-1.0)/denom)
		}
		na := float64(len(groupAnchorUnits[g]))
		if na > 1 && dObs >= 1 {
			eNullA := totalDaysM * (1.0 - math.Exp(-na/totalDaysM))
			denomA := math.Max(1.0, eNullA-1.0)
			anchorKappa[g] = ((na - 1.0) / (na + 2.0)) * math.Max(0, 1.0-(dObs-1.0)/denomA)
		}
	}

	hasProperAnchor := false
	hasSpecificProper := false
	specificPropMax := int(float64(idx.N) / (math.E * math.E * math.E))
	openingBroadClock := make(map[uint64]bool, 4)
	minClockUnits := 0
	hasBodyProper := false
	openingDigits := 0
	maxSlot := 0
	leadTermRepeated := false
	for h, qa := range qTerms {
		if qa.Flags&rmFlagCompanion != 0 {
			continue
		}
		if int(qa.Slot) > maxSlot {
			maxSlot = int(qa.Slot)
		}
		if qa.Sent == 1 && qa.Slot <= 6 && qa.Flags&rmBitDigit != 0 {
			openingDigits++
		}
		if qa.Sent == 1 && qa.Slot == 1 {
			for _, p := range qPositions[h] {
				if p.Slot > 6 || p.Sentence > 0 {
					leadTermRepeated = true
					break
				}
			}
		}
	}
	if openingDigits >= 2 && maxSlot > 8 && !leadTermRepeated {
		for g, isProp := range groupProper {
			if !isProp || len(groupUnits[g]) == 0 || len(groupUnits[g]) > highDFThresh {
				continue
			}
			qa := qTerms[g]
			if qa.Sent == 1 && qa.Slot <= 5 && len(groupAnchorUnits[g]) > specificPropMax {
				openingBroadClock[g] = true
				if minClockUnits == 0 || len(groupAnchorUnits[g]) < minClockUnits {
					minClockUnits = len(groupAnchorUnits[g])
				}
			} else {
				hasBodyProper = true
			}
		}
	}
	for g, isProp := range groupProper {
		if isProp && len(groupUnits[g]) > 0 && len(groupUnits[g]) <= highDFThresh {
			hasProperAnchor = true
			gBits := qTerms[g].Bits
			if gBits == 0 {
				gBits = idx.TermBitsOf(g)
			}
			if len(groupAnchorUnits[g]) <= specificPropMax && float64(gBits)*2.0 >= meanBits {
				hasSpecificProper = true
			}
		}
	}
	sc := e.ensureQueryScratch(idx.N, st.clusterN)
	isAnchorGroup := make(map[uint64]bool, len(groupUnits))
	aGen := sc.nextAnchorGen()
	uAnchorGen := sc.unitAnchorGen[:idx.N]
	cAnchorGen := sc.clusterAnchorGen[:st.clusterN]
	anchorTimedSlots := sc.anchorTimeSlots[:0]
	nTimedBuckets := int32(len(st.bucketTimes))
	anchorDays := make(map[int64]bool, 64)
	hasAnchor := false
	for g, um := range groupUnits {
		if len(um) == 0 || len(um) > highDFThresh {
			continue
		}
		gBits := qTerms[g].Bits
		if gBits == 0 {
			gBits = idx.TermBitsOf(g)
		}
		if hasProperAnchor {
			if groupProper[g] {
				isAnchorGroup[g] = true
			}
			if !hasBodyProper && minClockUnits > 0 && !openingBroadClock[g] &&
				anchorKappa[g] >= invE && len(groupAnchorUnits[g]) < minClockUnits && float64(gBits)*2.0 >= meanBits {
				isAnchorGroup[g] = true
			}
		} else if kappaOcc[g] >= invE && compoundPartner[g] == 0 {
			isAnchorGroup[g] = true
		}
		if isAnchorGroup[g] {
			hasAnchor = true
			srcUnits := um
			if len(groupAnchorUnits[g]) > 0 {
				srcUnits = groupAnchorUnits[g]
			}
			for u := range srcUnits {
				uAnchorGen[u] = aGen
				cl := st.clusters[u]
				if cAnchorGen[cl] != aGen {
					cAnchorGen[cl] = aGen
					if cl >= 0 && cl < nTimedBuckets {
						anchorTimedSlots = append(anchorTimedSlots, cl)
					}
				}
				if d := st.fastDocs[u].day; d != noDay {
					anchorDays[d] = true
				}
			}
		}
	}
	sc.anchorTimeSlots = anchorTimedSlots
	tauMacro := math.Expm1(st.muLogGap + st.sigLogGap)
	if tauMacro <= 0 {
		tauMacro = st.meanGap * math.E
	}
	ordHorizon := int32(math.Ceil(math.E * math.E))
	if hasAnchor {
		for _, ab := range anchorTimedSlots {
			ta := st.bucketTimes[ab]
			loB := ab - ordHorizon
			if loB < 0 {
				loB = 0
			}
			hiB := ab + ordHorizon
			if hiB >= nTimedBuckets {
				hiB = nTimedBuckets - 1
			}
			for b := loB; b <= hiB; b++ {
				if cAnchorGen[b] != aGen && math.Abs(float64(st.bucketTimes[b]-ta)) <= tauMacro {
					cAnchorGen[b] = aGen
				}
			}
		}
	}
	shellScale := make(map[uint64]float64, len(queryHashes))
	if hasAnchor {
		for g, um := range groupUnits {
			if isAnchorGroup[g] || groupRigid[g] || kappaOcc[g] >= invE*invE || float64(len(groupAnchorUnits[g])) <= math.E*math.E {
				continue
			}
			coHits := 0
			for u := range um {
				if uAnchorGen[u] == aGen || (st.fastDocs[u].day != noDay && anchorDays[st.fastDocs[u].day]) {
					coHits++
					break
				}
			}
			if coHits == 0 {
				shellScale[g] = invE
			}
		}
	}

	groups := make(map[uint64]float64, len(queryHashes))
	candidates := sc.candidates[:idx.N]
	unitStructGate := sc.unitStructGate[:idx.N]
	dGen := sc.resetDirtyUnits(idx.N)
	activateUnit := func(u int32) {
		if sc.unitDirtyGen[u] != dGen {
			sc.unitDirtyGen[u] = dGen
			sc.dirtyUnits = append(sc.dirtyUnits, u)
			gate := 1.0
			if hasAnchor && uAnchorGen[u] != aGen && cAnchorGen[st.clusters[u]] != aGen {
				if d := st.fastDocs[u].day; d == noDay || !anchorDays[d] {
					gate = invE
				}
			}
			unitStructGate[u] = gate
		}
	}

	var extraGroups map[uint64][]uint64
	if len(qPositions) > 0 {
		slotPrim := make(map[TermPosition]uint64, len(queryHashes)*2)
		slotPure := make(map[TermPosition]bool, len(queryHashes)*2)
		for _, h := range queryHashes {
			if g := hashGroup[h]; g == h && !detachedFrag[g] {
				isPure := qTerms[h].Flags&rmFlagCompanion == 0
				for _, pos := range qPositions[h] {
					if prev, exists := slotPrim[pos]; !exists || (isPure && !slotPure[pos]) || (isPure == slotPure[pos] && g < prev) {
						slotPrim[pos] = g
						slotPure[pos] = isPure
					}
				}
			}
		}
		for _, h := range queryHashes {
			g := hashGroup[h]
			if g == 0 || detachedFrag[g] || idx.TermDF(h) > highDFThresh || !st.isPrimaryOf(h) {
				continue
			}
			posList := qPositions[h]
			if len(posList) < 2 {
				continue
			}
			for _, pos := range posList {
				altG := slotPrim[pos]
				if altG == 0 || altG == g || detachedFrag[altG] {
					continue
				}
				if origA, okA := qTerms[altG]; okA && qTerms[h].IsSlot2Companion(origA) {
					continue
				}
				if extraGroups == nil {
					extraGroups = make(map[uint64][]uint64, 4)
				}
				dup := false
				for _, prev := range extraGroups[h] {
					if prev == altG {
						dup = true
						break
					}
				}
				if !dup {
					extraGroups[h] = append(extraGroups[h], altG)
				}
			}
		}
	}

	for _, h := range queryHashes {
		baseline, ok := idx.TermMass(h)
		if !ok || baseline <= 0 {
			continue
		}
		qTerm := qTerms[h]
		group := hashGroup[h]
		isPrimH := st.isPrimaryOf(h)
		dfH := idx.TermDF(h)
		e3 := int(math.Ceil(math.E * math.E * math.E))
		if h == group && !groupRigid[group] && !groupProper[group] && anchorKappa[group] < invE && (dfH <= e3 || dfH <= specificPropMax) {
			for _, ch := range queryHashes {
				if ch == h || qTerms[ch].Group != group {
					continue
				}
				if qTerms[ch].IsSlot2Companion(qTerms[group]) || !st.isPrimaryOf(ch) {
					continue
				}
				if qTerms[group].Bits < qTerms[ch].Bits || float64(qTerms[group].Bits)*math.E < meanBits*2.0 {
					continue
				}
				dfStem := idx.TermDF(ch)
				if dfH > e3 && dfStem <= highDFThresh {
					if !hasAnchor || shellScale[group] > 0 {
						continue
					}
					hInCone := false
					for _, p := range st.termPostings(h) {
						if uAnchorGen[p.unit] == aGen || (st.fastDocs[p.unit].day != noDay && anchorDays[st.fastDocs[p.unit].day]) {
							hInCone = true
							break
						}
					}
					if hInCone {
						continue
					}
				}
				if float64(dfStem) > math.E*float64(dfH) {
					if stemMass, okS := idx.TermMass(ch); okS && stemMass > 0 && stemMass < baseline {
						if dfStem > highDFThresh {
							baseline = math.Sqrt(baseline * stemMass)
						} else {
							baseline = (1.0-invE)*baseline + invE*stemMass
						}
						break
					}
				}
			}
		}
		if sc, ok := shellScale[group]; ok && sc > 0 {
			baseline *= sc
		}
		if !detachedFrag[group] && kappaOcc[group] > 0 {
			baseline *= 1.0 + kappaOcc[group]*invE
		}
		bits := qTerm.Bits
		if bits == 0 {
			bits = idx.TermBitsOf(h)
		}
		if !detachedFrag[group] && dfH <= specificPropMax && shellScale[group] == 0 && isPrimH && float64(bits)*2.0 >= meanBits {
			if posList := qPositions[h]; len(posList) > 1 {
				minSlotGap := 2
				if compoundPartner[group] != 0 {
					minSlotGap = 3
				}
				indepQTF := 0
				lastSent, lastSlot := -1, -10
				for _, p := range posList {
					if p.Sentence != lastSent || p.Slot-lastSlot >= minSlotGap {
						indepQTF++
						lastSent, lastSlot = p.Sentence, p.Slot
					}
				}
				if indepQTF > 1 {
					qRep := math.Min(1.0, math.Log(float64(indepQTF)))
					specRatio := math.Max(0, 1.0-float64(dfH)/float64(specificPropMax+1))
					qMod := math.Min(1.0, invE+(1.0-invE)*(kappaOcc[group]+specRatio))
					baseline *= 1.0 + qMod*qRep
				}
			}
		}
		weight := baseline
		if meanBits > 0 && bits > 0 {
			weight *= bitsGainSqrt(float64(bits) / meanBits)
		}
		if weight > groups[group] {
			groups[group] = weight
		}
		for _, p := range st.termPostings(h) {
			if sc.unitDirtyGen[p.unit] != dGen {
				activateUnit(p.unit)
				sc.lexUnits = append(sc.lexUnits, p.unit)
			}
			c := &candidates[p.unit]
			unitEntries := st.docEntriesOf(p.unit)
			docEntry := unitEntries[p.entry]
			repeats := math.Log(math.Max(1, float64(docEntry.tf)))
			if repeats > 1.0 {
				repeats = 1.0
			}
			if detachedFrag[group] {
				repeats *= invE
			}
			matchedMass := weight * (1.0 + repeats)
			if qTerm.Flags&rmFlagCompanion != 0 && docEntry.flags&rmFlagCompanion != 0 && qTerm.Group != 0 && docEntry.group != 0 && docEntry.group != qTerm.Group {
				if origA, okA := qTerms[qTerm.Group]; okA && qTerm.IsSlot2Companion(origA) {
					if isPrimH {
						matchedMass *= invE
					} else if !detachedFrag[group] {
						matchedMass *= invE * invE
					}
				}
			}
			if meanBits > 0 && docEntry.bits > 0 {
				matchedMass *= bitsGainSqrt(float64(docEntry.bits) / meanBits)
			}
			docGainMask := uint8(1<<2 | 1<<5 | 1<<7)
			if qTerm.Flags&rmBitBoundaryQuote != 0 {
				docGainMask |= rmBitBoundaryQuote
			}
			if e.FlagsGain > 0 && docEntry.flags&docGainMask != 0 {
				matchedMass *= 1.0 + e.FlagsGain/math.E
			}
			c.upsertMatch(group, matchedMass, p.entry, docEntry.bits, unitEntries, true)
			for _, altG := range extraGroups[h] {
				c.upsertMatch(altG, matchedMass*invE, p.entry, docEntry.bits, unitEntries, false)
			}
		}
	}

	groupKeys := make([]uint64, 0, len(groups))
	for g := range groups {
		groupKeys = append(groupKeys, g)
	}
	sort.Slice(groupKeys, func(i, j int) bool { return groupKeys[i] < groupKeys[j] })
	var total, sumG2 float64
	for _, g := range groupKeys {
		m := groups[g]
		total += m
		sumG2 += m * m
	}
	if total <= 0 {
		return nil
	}
	muQ := sumG2 / total

	var bestCoverage float64
	coreLex := sc.coreLex[:idx.N]
	sort.Slice(sc.lexUnits, func(i, j int) bool { return sc.lexUnits[i] < sc.lexUnits[j] })
	for _, u := range sc.lexUnits {
		c := &candidates[u]
		gate := unitStructGate[u]
		for i := range c.matches {
			m := &c.matches[i]
			if m.primMass > 0 && m.compMass > 0 && hasBroadComp[m.group] && groupProper[m.group] {
				if bp := m.primMass * (1.0 + invE*invE); bp > m.mass {
					m.mass = bp
					m.entry = m.primEntry
				}
			}
			if partnerG := compoundPartner[m.group]; partnerG != 0 && !c.hasGroup(partnerG) {
				m.mass *= invE
			}
			if hasSpecificProper && gate < 1.0 {
				g := m.group
				if !isAnchorGroup[g] && !groupRigid[g] && kappaOcc[g] < invE*invE && float64(len(groupAnchorUnits[g])) > math.E*math.E {
					if _, alreadyScaled := shellScale[g]; !alreadyScaled {
						m.mass *= gate
					}
				}
			}
			c.lex += m.mass
			if !detachedFrag[m.group] {
				coreLex[u] += m.mass
			}
		}
		c.coreLex = coreLex[u]
		if coreLex[u] > 0 {
			sc.coreUnits = append(sc.coreUnits, u)
		}
		covLex := c.lex
		if len(c.matches) == 1 && len(groupKeys) > 1 {
			unitEntries := st.docEntriesOf(u)
			repeats := math.Log(math.Max(1, float64(unitEntries[c.matches[0].entry].tf)))
			if repeats > 1.0 {
				repeats = 1.0
			}
			if detachedFrag[c.matches[0].group] {
				repeats *= invE
			}
			covLex = c.matches[0].mass / (1.0 + repeats)
		}
		if cov := covLex / total; cov > bestCoverage {
			bestCoverage = cov
		}
	}
	residual := math.Max(0, 1.0-bestCoverage)

	// 1. Intra-sentence PhraseDyad joint surprisal channel & 2nd-order EntityCluster phrase subcluster witness.
	effWForQueryTerm := func(h uint64) (float64, bool) {
		w, ok := st.effWeightOf(h)
		if !ok || w <= 0 {
			return 0, false
		}
		if mH, okM := idx.TermMass(h); okM && detachedSlot2[hashGroup[h]] && mH < w {
			return math.Sqrt(w * mH), mH > 0
		}
		return w, true
	}
	isConservedInflectionLemma := func(h uint64, qa Term) bool {
		g := hashGroup[h]
		dfG := idx.TermDF(g)
		dfH := idx.TermDF(h)
		return !detachedFrag[g] && !groupProper[g] && qa.Role == RoleMorphStem && st.isPrimaryOf(h) &&
			!qa.IsSlot2Companion(qTerms[g]) && dfG > 0 && dfH > dfG && float64(dfH) <= math.E*float64(dfG)
	}
	isCoreClosedBeacon := func(h uint64, qa Term) bool {
		g := hashGroup[h]
		return !detachedFrag[g] && qa.Role == RoleClosedBeacon && idx.TermDF(h) <= highDFThresh
	}
	phraseWitness := sc.phraseWitness[:idx.N]
	for _, pair := range rmQueryDyadsWithPositions(qTerms, qPositions) {
		posts := st.phrasePostings(pair)
		if len(posts) == 0 {
			continue
		}
		wa, okA := effWForQueryTerm(pair.a)
		wb, okB := effWForQueryTerm(pair.b)
		if !okA || !okB {
			continue
		}
		joint := math.Log1p((float64(idx.N) - float64(len(posts)) + 0.5) / (float64(len(posts)) + 0.5))
		mass := math.Min(wa, wb) * joint / (1.0 + joint)
		qaA, qaB := qTerms[pair.a], qTerms[pair.b]
		if qaA.Flags&rmFlagCompanion != 0 && qaB.Flags&rmFlagCompanion != 0 {
			orthoCross := (isConservedInflectionLemma(pair.a, qaA) && isCoreClosedBeacon(pair.b, qaB)) ||
				(isConservedInflectionLemma(pair.b, qaB) && isCoreClosedBeacon(pair.a, qaA))
			if !orthoCross {
				mass *= invE
			}
		}
		var sum float64
		for _, u := range posts {
			activateUnit(u)
			candidates[u].phrase += mass * unitStructGate[u]
			sum += candidates[u].lex / total
		}
		denom := 1.0 + float64(len(posts))
		for _, u := range posts {
			c := &candidates[u]
			if witness := sum - c.lex/total; witness > 0 {
				phraseWitness[u] += mass * unitStructGate[u] * math.Log1p(witness) / denom
			}
		}
	}

	// 2. EntityCluster witness (1st-order term witness) & SpacetimeCluster neighbor spread.
	for _, h := range queryHashes {
		posts := st.termPostings(h)
		if len(posts) == 0 {
			continue
		}
		var sum float64
		for _, p := range posts {
			sum += candidates[p.unit].lex / total
		}
		wEff, _ := st.effWeightOf(h)
		if g, ok := hashGroup[h]; ok {
			if sc, ok := shellScale[g]; ok && sc > 0 {
				wEff *= sc
			}
		}
		denom := 1.0 + float64(len(posts))
		for _, p := range posts {
			c := &candidates[p.unit]
			witness := sum - c.lex/total
			if witness > 0 {
				c.entity += wEff * math.Log1p(witness) / denom
			}
		}
	}

	clusterSum := sc.clusterSum[:st.clusterN]
	clusterHits := sc.clusterHits[:st.clusterN]
	for _, u := range sc.coreUnits {
		cl := st.clusters[u]
		if clusterHits[cl] == 0 {
			sc.activeClusters = append(sc.activeClusters, cl)
		}
		clusterSum[cl] += coreLex[u]
		clusterHits[cl]++
	}
	for _, seed := range sc.coreUnits {
		seedE := coreLex[seed]
		for _, neighbor := range st.neighborsOf(seed) {
			if neighbor == seed {
				continue
			}
			aff := st.temporalAffinity(idx, seed, neighbor)
			gain := seedE * aff * residual
			if gain > 0 && coreLex[neighbor] >= seedE {
				if rawCos := st.docSubjectCos(idx, seed, neighbor); rawCos < invE*invE {
					gain *= math.Max(invE, math.Sqrt(math.Max(0, rawCos))/invE)
				}
			}
			if seedE > coreLex[neighbor] && unitStructGate[seed] >= 1.0 && aff > 0 {
				if rawCos := st.docSubjectCos(idx, seed, neighbor); rawCos > 0 {
					if sibGain := (seedE - coreLex[neighbor]) * aff * math.Sqrt(rawCos) * invE * invE; sibGain > gain {
						gain = sibGain
					}
				}
			}
			if gain > candidates[neighbor].stSpread {
				activateUnit(neighbor)
				candidates[neighbor].stSpread = gain
			}
		}
	}
	// Unified 2x2 EntityCluster x SpacetimeCluster subsumption-lattice conduction:
	// Even when residual -> 0 (the seed itself matches ~100% of query groups),
	// a stronger co-event seed conducts its missing specific group excess into
	// a co-episode peer whose core matched groups form a strict sub-cluster
	// (or tight physical burst <= tau_gap / e^3) under the MAX stSpread envelope.
	// Peer enumeration is keyed by the endogenous episodic component (gap-split,
	// phase-free) rather than any calendar-day grid, so the former cross-midnight
	// bridge patch is subsumed by construction: a gap <= tau_macro never opens a
	// component boundary in the first place.
	var peakCoreLex float64
	maxCoreMass := sc.maxCoreMass[:idx.N]
	byEpisodeActive := make(map[int32][]int32, 32)
	corefByDay := make(map[int64][]int32, 64)
	for _, u := range sc.coreUnits {
		if coreLex[u] > peakCoreLex {
			peakCoreLex = coreLex[u]
		}
		var mm float64
		for _, m := range candidates[u].matches {
			if !detachedFrag[m.group] && idx.TermDF(m.group) <= highDFThresh && m.mass > mm {
				mm = m.mass
			}
		}
		maxCoreMass[u] = mm
		if d := st.fastDocs[u].day; d != noDay {
			corefByDay[d] = append(corefByDay[d], u)
		}
		if ep := st.episodeOf[u]; ep >= 0 && unitStructGate[u] >= 1.0 {
			byEpisodeActive[ep] = append(byEpisodeActive[ep], u)
		}
	}
	activeEpisodes := make([]int32, 0, len(byEpisodeActive))
	for ep := range byEpisodeActive {
		activeEpisodes = append(activeEpisodes, ep)
	}
	sort.Slice(activeEpisodes, func(i, j int) bool { return activeEpisodes[i] < activeEpisodes[j] })
	burstAffFloor := math.Exp(-invE * invE * invE)
	for _, ep := range activeEpisodes {
		units := byEpisodeActive[ep]
		if len(units) < 2 {
			continue
		}
		for _, seed := range units {
			seedE := coreLex[seed]
			for _, nb := range units {
				if nb == seed || seedE <= coreLex[nb] || maxCoreMass[nb] < muQ {
					continue
				}
				aff := st.temporalAffinity(idx, seed, nb)
				if aff <= 0 {
					continue
				}
				rawCos := st.docSubjectCos(idx, seed, nb)
				if rawCos <= 0 {
					continue
				}
				isTightBurst := aff >= burstAffFloor
				isDirectNb := st.isDirectTimeNeighbor(seed, nb)
				nbC := &candidates[nb]
				seedC := &candidates[seed]
				isSub := true
				strictSub := true
				coreMatchCount := 0
				sharedCoreCount := 0
				for _, nm := range nbC.matches {
					if detachedFrag[nm.group] || idx.TermDF(nm.group) > highDFThresh {
						continue
					}
					coreMatchCount++
					if seedC.hasGroup(nm.group) {
						sharedCoreCount++
					} else if groups[nm.group] < muQ {
						strictSub = false
						continue
					} else {
						isSub = false
						strictSub = false
						break
					}
				}
				if sharedCoreCount < 2 && coreMatchCount > sharedCoreCount {
					isSub = false
				}
				if !isSub && !isTightBurst {
					continue
				}
				var missingMass float64
				seedEntries := st.docEntriesOf(seed)
				for _, sm := range seedC.matches {
					if detachedFrag[sm.group] || idx.TermDF(sm.group) > highDFThresh {
						continue
					}
					sEntry := seedEntries[sm.entry]
					if coreMatchCount < 2 {
						if sEntry.flags&rmFlagCompanion != 0 || groups[sm.group] < muQ {
							continue
						}
					} else if sEntry.flags&rmFlagCompanion != 0 && qTerms[sEntry.id].Flags&rmFlagCompanion != 0 {
						continue
					}
					if !nbC.hasGroup(sm.group) {
						missingMass += sm.mass
					}
				}
				excess := math.Min(seedE-coreLex[nb], missingMass)
				if excess <= 0 {
					continue
				}
				var cosMod float64
				if isDirectNb && strictSub {
					cosMod = 1.0
				} else {
					cosMod = math.Sqrt(rawCos)
				}
				exScale := invE
				if st.muLogGap > 0 && st.ordPos[seed] >= 0 && st.ordPos[nb] >= 0 {
					tauMicro := math.Max(1.0, math.Expm1(math.Max(1.0, st.muLogGap-st.sigLogGap)))
					dt := math.Abs(float64(st.refTimes[seed] - st.refTimes[nb]))
					dk := math.Abs(float64(st.ordPos[seed] - st.ordPos[nb]))
					if dt <= tauMicro*math.E && dk <= 2 {
						rT := dt / tauMicro
						burstExc := math.Exp(-rT*rT - math.Max(0, dk-1.0))
						exScale = invE * (1.0 + invE*burstExc)
					}
				}
				exGain := excess * aff * cosMod * exScale
				allowOverlay := isSub && (rawCos >= invE || (sharedCoreCount >= 2 && aff >= 1.0-invE*invE && rawCos >= invE*invE))
				if allowOverlay {
					candSpread := candidates[nb].stSpread + exGain*math.Sqrt(rawCos)*invE
					if exGain > candSpread {
						candSpread = exGain
					}
					candidates[nb].stSpread = candSpread
				} else if exGain > candidates[nb].stSpread {
					candidates[nb].stSpread = exGain
				}
			}
		}
	}
	for _, u := range sc.coreUnits {
		cl := st.clusters[u]
		if clusterHits[cl] > 1 {
			witness := (clusterSum[cl] - coreLex[u]) / total
			candidates[u].stSpread += coreLex[u] * math.Log1p(witness)
		}
	}
	if st.meanGap == 0 && e.Hop1Budget > 0 {
		directMap := make(map[int32]float64, len(sc.lexUnits))
		directSeeds := make([]int32, 0, len(sc.lexUnits))
		for _, u := range sc.lexUnits {
			if candidates[u].lex > 0 {
				directMap[u] = candidates[u].lex
				directSeeds = append(directSeeds, u)
			}
		}
		sortByEnergy(directSeeds, directMap, idx)
		for u, gain := range e.spread(directSeeds, directMap, qTerms, invE, e.Hop1Budget, 0) {
			if gain > candidates[u].stSpread {
				activateUnit(u)
				candidates[u].stSpread = gain
			}
		}
	}

	sort.Slice(sc.dirtyUnits, func(i, j int) bool { return sc.dirtyUnits[i] < sc.dirtyUnits[j] })

	// 3. Intra-sentence pair compactness (residual proximity + pair synergy) and SpacetimeCluster peer corroboration.
	var peak float64
	for _, u := range sc.dirtyUnits {
		c := &candidates[u]
		if len(c.matches) >= 2 && coreLex[u] > 0 {
			gate := unitStructGate[u]
			prox, syn := st.rmProximityAndSynergy(c, groups, total)
			c.entity += gate * (residual*prox + syn)
		}
		if own := c.lex + c.phrase + c.entity; own > peak {
			peak = own
		}
	}
	if peak > 0 {
		strengthOf := func(u int32) float64 {
			o := &candidates[u]
			return coreLex[u] + o.phrase + o.entity
		}
		for day, units := range corefByDay {
			sort.Slice(units, func(i, j int) bool {
				si, sj := strengthOf(units[i]), strengthOf(units[j])
				if si != sj {
					return si > sj
				}
				return units[i] < units[j]
			})
			corefByDay[day] = units
		}

		for _, u := range sc.dirtyUnits {
			c := &candidates[u]
			own := coreLex[u] + c.phrase + c.entity
			if own <= 0 {
				continue
			}
			var bestWitness float64
			missingBest := sc.missingBest[:0]
			for _, n := range st.neighborsOf(u) {
				if n == u {
					continue
				}
				other := &candidates[n]
				strength := coreLex[n] + other.phrase + other.entity
				if strength <= 0 {
					continue
				}
				aff := st.temporalAffinity(idx, u, n)
				if aff <= 0 {
					continue
				}
				w := aff * (strength / peak)
				hasCompNb := false
				for _, om := range other.matches {
					if !c.hasGroup(om.group) && !detachedFrag[om.group] {
						hasCompNb = true
						break
					}
				}
				if !hasCompNb {
					if c.coreLex < peakCoreLex*invE {
						rawCos := st.docSubjectCos(idx, u, n)
						w *= math.Max(invE, math.Sqrt(math.Max(0, rawCos)))
					}
					if w > bestWitness {
						bestWitness = w
					}
					continue
				}
				if w > bestWitness {
					bestWitness = w
				}
				if unitStructGate[u] < 1.0 || strength+other.stSpread <= own {
					continue
				}
				rawCos := st.docSubjectCos(idx, u, n)
				if rawCos <= 0 {
					continue
				}
				cos := math.Sqrt(rawCos)
				for _, om := range other.matches {
					if c.hasGroup(om.group) || detachedFrag[om.group] {
						continue
					}
					gm := groups[om.group]
					if gm <= 0 {
						continue
					}
					wMod := (gm * gm) / (gm*gm + muQ*muQ)
					val := aff * cos * om.mass * wMod
					missingBest = upsertStructGroupMax(missingBest, om.group, val)
				}
			}
			sc.missingBest = missingBest
			var corefPuzzle float64
			if coreLex[u] > 0 {
				if day := st.fastDocs[u].day; day != noDay {
					ownS := strengthOf(u)
					meanOwn := c.lex / float64(len(c.matches))
					coreCnt := 0
					for _, m := range c.matches {
						if !detachedFrag[m.group] {
							coreCnt++
						}
					}
					if coreCnt > 0 {
						meanOwn = coreLex[u] / float64(coreCnt)
					}
					lexShare := c.lex / total
					for _, n := range corefByDay[day] {
						if n == u {
							continue
						}
						peerS := strengthOf(n)
						strength := peerS / peak
						if strength <= bestWitness && peerS <= ownS {
							break
						}
						canInherit := peerS > ownS && maxCoreMass[n] > meanOwn
						isCloseOrd := peerS > ownS && st.ordPos[u] >= 0 && st.ordPos[n] >= 0 &&
							math.Abs(float64(st.ordPos[u]-st.ordPos[n])) <= math.Ceil(math.E*math.E)
						if !isCloseOrd && strength <= bestWitness && (!canInherit || strength*candidates[n].lex*lexShare <= corefPuzzle) {
							continue
						}
						var res, rScale float64
						if canInherit {
							for _, om := range candidates[n].matches {
								if om.mass <= meanOwn || c.hasGroup(om.group) {
									continue
								}
								res += om.mass - meanOwn
							}
							if res > 0 {
								rShare := math.Min(1.0, res/total)
								rScale = res * math.Max(rShare, invE) * lexShare
							}
						}
						if !isCloseOrd && strength <= bestWitness && strength*rScale <= corefPuzzle {
							continue
						}
						aff := st.temporalAffinity(idx, u, n)
						if !isCloseOrd && aff*strength <= bestWitness && aff*strength*rScale <= corefPuzzle {
							continue
						}
						rawCos := st.docSubjectCos(idx, u, n)
						if rawCos <= 0 {
							continue
						}
						cos := math.Sqrt(rawCos)
						cosW := cos
						if isCloseOrd && st.muLogGap > 0 && coreLex[n] >= peakCoreLex*(1.0-invE) {
							tauMeso := math.Max(1.0, math.Expm1(st.muLogGap))
							dt := math.Abs(float64(st.refTimes[u] - st.refTimes[n]))
							dk := math.Abs(float64(st.ordPos[u] - st.ordPos[n]))
							burstCoh := math.Exp(-dt/(tauMeso*math.E) - math.Max(0, dk-2.0)*invE)
							cosW = cos + (1.0-cos)*burstCoh
						}
						w := aff * cosW * strength
						if w > bestWitness {
							bestWitness = w
						}
						if rScale > 0 {
							if p := aff * cos * strength * rScale; p > corefPuzzle {
								corefPuzzle = p
							}
						}
						if unitStructGate[u] >= 1.0 && peerS > ownS && aff >= invE && rawCos >= invE {
							for _, om := range candidates[n].matches {
								if c.hasGroup(om.group) || detachedFrag[om.group] {
									continue
								}
								gm := groups[om.group]
								if gm <= 0 {
									continue
								}
								wMod := (gm * gm) / (gm*gm + muQ*muQ)
								val := aff * cos * om.mass * wMod
								missingBest = upsertStructGroupMax(missingBest, om.group, val)
							}
						}
					}
				}
			}
			if bestWitness > 0 {
				ratio := 1.0
				if peakCoreLex > 0 {
					ratio = math.Min(1.0, coreLex[u]/peakCoreLex)
				}
				c.stCorrob = math.Log1p(bestWitness) * (coreLex[u] + (invE+(1.0-invE)*ratio)*c.phrase + c.entity)
			}
			c.stCorrob += corefPuzzle
			if len(missingBest) > 0 && coreLex[u] > 0 {
				var compSum float64
				for _, g := range groupKeys {
					for mi := range missingBest {
						if missingBest[mi].group == g {
							compSum += missingBest[mi].weight
							break
						}
					}
				}
				cl := coreLex[u]
				selfMod := (cl * cl) / (cl*cl + muQ*muQ)
				c.stCorrob += invE * (1.0 + selfMod*(cl/total)) * compSum
			}
		}
	}
	for _, u := range sc.dirtyUnits {
		if pw := phraseWitness[u]; pw > 0 {
			candidates[u].entity += pw
		}
		if unitStructGate[u] < 1.0 {
			if hasSpecificProper {
				candidates[u].stSpread *= unitStructGate[u]
			}
			candidates[u].stCorrob *= unitStructGate[u]
		}
	}
	st.applyHigherOrderLatticeContraction(idx, sc, candidates, unitStructGate, qTerms, hashGroup, extraGroups, detachedFrag, shellScale, highDFThresh)
	e.applyIntraEventExplanatoryEndorsement(idx, st, sc, candidates, unitStructGate, corefByDay, groups, total, highDFThresh, qTerms)

	// 3.5 可选 Phase-3 关系簇外挂接入埋点（如 RB 云端独占的 relation-cluster extension RelationCluster）：
	// 仅当 EnablePhase3Plugin == true 且 Phase3Plugin != nil 时触发；
	// 默认关闭（EnablePhase3Plugin == false 或 Phase3Plugin == nil）时 100% 零开销直通，不侵入 rm 核心。
	if e.EnablePhase3Plugin && e.Phase3Plugin != nil {
		e.applyPhase3ClusterHook(idx, st, sc, candidates, unitStructGate, qTerms, groups, total)
	}

	// 4. Prior-smoothed bidirectional structured coverage & set-conditioned selection.
	return st.selectClusterHits(idx, sc, candidates, unitStructGate, detachedFrag, qTerms, groups, total, muQ, topK)
}

// applyHigherOrderLatticeContraction evaluates the telescoping subsumption-lattice
// contraction surprisal ln((minDF + 1) / (df_cluster(M_u) + 1)) for candidates matching
// k >= 3 non-detached core query groups (where 2-ary co-occurrence is already captured
// by PhraseDyad), followed by the 1st-order Euler horizon rigid digit/quantity pass.
// Coupling scale is 1/e^2 (2nd-order Euler constant).
func (st *rmClusterState) applyHigherOrderLatticeContraction(idx *Index, sc *rmQueryScratch, candidates []rmCandidate, unitStructGate []float64, qTerms map[uint64]Term, hashGroup map[uint64]uint64, extraGroups map[uint64][]uint64, detachedFrag map[uint64]bool, shellScale map[uint64]float64, highDFThresh int) {
	if idx == nil || len(candidates) == 0 || len(qTerms) < 3 {
		return
	}
	const invE2 = 1.0 / (math.E * math.E)

	evalPass := func(hashes []uint64, groupOf map[uint64]uint64, groupW map[uint64]float64, groupDF map[uint64]int, useExtra bool, minPop int, requireMid bool) {
		if len(groupW) < minPop || sc == nil {
			return
		}
		gKeys := make([]uint64, 0, len(groupW))
		for g := range groupW {
			gKeys = append(gKeys, g)
		}
		sort.Slice(gKeys, func(i, j int) bool { return gKeys[i] < gKeys[j] })
		var totW float64
		for _, g := range gKeys {
			totW += groupW[g]
		}
		if totW <= 0 {
			return
		}
		sort.Slice(hashes, func(i, j int) bool { return hashes[i] < hashes[j] })

		if len(gKeys) > 64 {
			numWords := (len(gKeys) + 63) / 64
			gMassSlice := make([]float64, len(gKeys))
			gDFSlice := make([]int, len(gKeys))
			for i, g := range gKeys {
				gMassSlice[i] = groupW[g]
				gDFSlice[i] = groupDF[g]
			}
			idxOfG := func(target uint64) int {
				lo, hi := 0, len(gKeys)
				for lo < hi {
					mid := int(uint(lo+hi) >> 1)
					if gKeys[mid] < target {
						lo = mid + 1
					} else {
						hi = mid
					}
				}
				if lo < len(gKeys) && gKeys[lo] == target {
					return lo
				}
				return -1
			}
			gen := sc.nextLatticeGen()
			uGen := sc.unitLatticeGen[:idx.N]
			touched := sc.latticeTouched[:0]
			unitSlot := make(map[int32]int, 64)
			var maskPool []uint64
			setBit := func(words []uint64, idxBit int) {
				if idxBit >= 0 {
					words[idxBit>>6] |= uint64(1) << uint(idxBit&63)
				}
			}
			hWords := make([]uint64, numWords)
			for _, h := range hashes {
				for w := range hWords {
					hWords[w] = 0
				}
				setBit(hWords, idxOfG(groupOf[h]))
				if useExtra {
					for _, altG := range extraGroups[h] {
						if !detachedFrag[altG] && shellScale[altG] == 0 {
							setBit(hWords, idxOfG(altG))
						}
					}
				}
				anyBit := false
				for _, w := range hWords {
					if w != 0 {
						anyBit = true
						break
					}
				}
				if !anyBit {
					continue
				}
				for _, p := range st.termPostings(h) {
					u := p.unit
					slotIdx, ok := unitSlot[u]
					if uGen[u] != gen || !ok {
						uGen[u] = gen
						slotIdx = len(touched)
						unitSlot[u] = slotIdx
						touched = append(touched, u)
						maskPool = append(maskPool, hWords...)
					} else {
						base := slotIdx * numWords
						for w := 0; w < numWords; w++ {
							maskPool[base+w] |= hWords[w]
						}
					}
				}
			}
			sc.latticeTouched = touched
			activeUnits := sc.latticeTriUnits[:0]
			for i, u := range touched {
				base := i * numWords
				pop := 0
				for w := 0; w < numWords; w++ {
					pop += rmPopCount64(maskPool[base+w])
				}
				if pop >= minPop && (candidates[u].lex > 0 || candidates[u].stSpread > 0 || candidates[u].stCorrob > 0) {
					activeUnits = append(activeUnits, u)
				}
			}
			sc.latticeTriUnits = activeUnits
			if len(activeUnits) == 0 {
				return
			}
			sort.Slice(activeUnits, func(i, j int) bool { return activeUnits[i] < activeUnits[j] })
			deltaCache := make(map[string]float64, len(activeUnits))
			for _, u := range activeUnits {
				slotU := unitSlot[u]
				uWords := maskPool[slotU*numWords : (slotU+1)*numWords]
				key := matchedSetKey(uWords)
				gain, ok := deltaCache[key]
				if !ok {
					hasMidRigid := false
					coreCnt := 0
					minDF := idx.N + 1
					var mSum float64
					for b := 0; b < len(gKeys); b++ {
						if (uWords[b>>6] & (uint64(1) << uint(b&63))) != 0 {
							mSum += gMassSlice[b]
							if df := gDFSlice[b]; df > 0 && df < minDF {
								minDF = df
							}
							if gDFSlice[b] > highDFThresh {
								hasMidRigid = true
							} else {
								coreCnt++
							}
						}
					}
					valid := (!requireMid && minDF > 1) || (requireMid && hasMidRigid && coreCnt >= 3 && minDF > 1 && minDF <= highDFThresh)
					if valid {
						dfClust := 0
						for otherIdx := range touched {
							oWords := maskPool[otherIdx*numWords : (otherIdx+1)*numWords]
							sub := true
							for w := 0; w < numWords; w++ {
								if (uWords[w] &^ oWords[w]) != 0 {
									sub = false
									break
								}
							}
							if sub {
								dfClust++
							}
						}
						if dfClust > 0 && dfClust < minDF {
							delta := math.Log(float64(minDF+1)/float64(dfClust+1)) * (mSum / totW)
							gain = invE2 * delta * math.Max(1.0, mSum)
						}
					}
					deltaCache[key] = gain
				}
				if gain > 0 {
					gate := 1.0
					if int(u) < len(unitStructGate) {
						gate = unitStructGate[u]
					}
					candidates[u].entity += gate * gain
				}
			}
			return
		}

		var gMass [64]float64
		var gDF [64]int
		for i, g := range gKeys {
			gMass[i] = groupW[g]
			gDF[i] = groupDF[g]
		}
		bitOfG := func(target uint64) uint64 {
			lo, hi := 0, len(gKeys)
			for lo < hi {
				mid := int(uint(lo+hi) >> 1)
				if gKeys[mid] < target {
					lo = mid + 1
				} else {
					hi = mid
				}
			}
			if lo < len(gKeys) && gKeys[lo] == target {
				return uint64(1) << uint(lo)
			}
			return 0
		}
		gen := sc.nextLatticeGen()
		uGen := sc.unitLatticeGen[:idx.N]
		uMask := sc.unitLatticeMask[:idx.N]
		touched := sc.latticeTouched[:0]
		for _, h := range hashes {
			mask := bitOfG(groupOf[h])
			if useExtra {
				for _, altG := range extraGroups[h] {
					if !detachedFrag[altG] && shellScale[altG] == 0 {
						mask |= bitOfG(altG)
					}
				}
			}
			if mask == 0 {
				continue
			}
			for _, p := range st.termPostings(h) {
				u := p.unit
				if uGen[u] != gen {
					uGen[u] = gen
					uMask[u] = mask
					touched = append(touched, u)
				} else {
					uMask[u] |= mask
				}
			}
		}
		sc.latticeTouched = touched
		activeUnits := sc.latticeTriUnits[:0]
		for _, u := range touched {
			if rmPopCount64(uMask[u]) >= minPop && (candidates[u].lex > 0 || candidates[u].stSpread > 0 || candidates[u].stCorrob > 0) {
				activeUnits = append(activeUnits, u)
			}
		}
		sc.latticeTriUnits = activeUnits
		if len(activeUnits) == 0 {
			return
		}
		sort.Slice(activeUnits, func(i, j int) bool { return activeUnits[i] < activeUnits[j] })
		deltaCache := make(map[uint64]float64, len(activeUnits))
		for _, u := range activeUnits {
			mask := uMask[u]
			gain, ok := deltaCache[mask]
			if !ok {
				hasMidRigid := false
				coreCnt := 0
				minDF := idx.N + 1
				var mSum float64
				for b := 0; b < len(gKeys); b++ {
					if (mask & (uint64(1) << uint(b))) != 0 {
						mSum += gMass[b]
						if df := gDF[b]; df > 0 && df < minDF {
							minDF = df
						}
						if gDF[b] > highDFThresh {
							hasMidRigid = true
						} else {
							coreCnt++
						}
					}
				}
				valid := (!requireMid && minDF > 1) || (requireMid && hasMidRigid && coreCnt >= 3 && minDF > 1 && minDF <= highDFThresh)
				if valid {
					dfClust := 0
					for _, otherU := range touched {
						if (mask &^ uMask[otherU]) == 0 {
							dfClust++
						}
					}
					if dfClust > 0 && dfClust < minDF {
						delta := math.Log(float64(minDF+1)/float64(dfClust+1)) * (mSum / totW)
						gain = invE2 * delta * math.Max(1.0, mSum)
					}
				}
				deltaCache[mask] = gain
			}
			if gain > 0 {
				gate := 1.0
				if int(u) < len(unitStructGate) {
					gate = unitStructGate[u]
				}
				candidates[u].entity += gate * gain
			}
		}
	}

	groupDF := make(map[uint64]int, len(qTerms))
	groupW := make(map[uint64]float64, len(qTerms))
	activeHashes := make([]uint64, 0, len(qTerms))
	for h := range qTerms {
		m, ok := idx.TermMass(h)
		dfH := idx.TermDF(h)
		if !ok || m <= 0 || dfH > highDFThresh {
			continue
		}
		g := hashGroup[h]
		if g == 0 || detachedFrag[g] || shellScale[g] > 0 {
			continue
		}
		dfG := idx.TermDF(g)
		if h != g && dfG > 0 && (float64(dfH) > math.E*float64(dfG) || (qTerms[h].IsSlot2Companion(qTerms[g]) && dfH > dfG)) {
			continue
		}
		activeHashes = append(activeHashes, h)
		if m > groupW[g] {
			groupW[g] = m
		}
		if prev, exists := groupDF[g]; !exists || dfH < prev {
			groupDF[g] = dfH
		}
	}
	evalPass(activeHashes, hashGroup, groupW, groupDF, true, 3, false)

	eulerFirstHorizon := int(float64(idx.N) / math.E)
	const rmRigidDigitQtyMask uint8 = (1 << 2) | (1 << 7)
	hasMidRigid := false
	qGroupMid := make(map[uint64]uint64, len(qTerms))
	groupDFMid := make(map[uint64]int, len(qTerms))
	groupWMid := make(map[uint64]float64, len(qTerms))
	midHashes := make([]uint64, 0, len(qTerms))
	for h, qa := range qTerms {
		m, ok := idx.TermMass(h)
		if !ok || m <= 0 {
			continue
		}
		df := idx.TermDF(h)
		isMidRigid := df > highDFThresh && df <= eulerFirstHorizon && (qa.Flags&rmRigidDigitQtyMask != 0)
		if df > highDFThresh && !isMidRigid {
			continue
		}
		g := qa.Group
		if g == 0 {
			g = h
		}
		qGroupMid[h] = g
		midHashes = append(midHashes, h)
		if m > groupWMid[g] {
			groupWMid[g] = m
		}
		if prev, exists := groupDFMid[g]; !exists || df < prev {
			groupDFMid[g] = df
		}
		if isMidRigid {
			hasMidRigid = true
		}
	}
	if hasMidRigid {
		evalPass(midHashes, qGroupMid, groupWMid, groupDFMid, false, 4, true)
	}
}
