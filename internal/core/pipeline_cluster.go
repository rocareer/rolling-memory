// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package core

import (
	"math"
	"sort"
)

// gateAt 返回单元 u 的结构门；门表缺失或越界时恒 1.0（无锚信息不衰减）。
// 此前评分、筛选、背书与外挂钩五处内联的带守卫取门式收敛到本函数；
// 门表已按 idx.N 截断现场的无守卫直接索引不受影响。
func gateAt(unitStructGate []float64, u int32) float64 {
	if len(unitStructGate) > int(u) {
		return unitStructGate[u]
	}
	return 1.0
}

// docRepeatTable holds log(max(1, tf)) clamped to one for the common term
// frequencies; only tf==2 survives the clamp unchanged.
var docRepeatTable = func() [256]float64 {
	var t [256]float64
	t[2] = math.Log(2)
	for i := 3; i < len(t); i++ {
		t[i] = 1
	}
	return t
}()

// docRepeats converts a term frequency to log(max(1, tf)) clamped to one.
func docRepeats(tf int32) float64 {
	if tf >= 0 && tf < int32(len(docRepeatTable)) {
		return docRepeatTable[tf]
	}
	if tf < 0 {
		return 0
	}
	return 1
}

// proximityEntityDispatch is installed by the runtime adapter; nil keeps the
// portable serial path. Candidates are independent: the dispatch writes only
// candidates[u].entity, so parallel and serial paths are bit-identical.
var proximityEntityDispatch func(units []int32, coreLex []float64, candidates []rmCandidate, gates []float64, st *rmClusterState, groups map[uint64]float64, total, residual float64)

// spreadDirtyMissProbe 仅在测量器材安装时非 nil：每次「写 stSpread 的单元尚未
// 登记进本轮 dirty 集」触发一次，用于量化登记缺口。生产路径只多一次 nil 分支。
var spreadDirtyMissProbe func()

// SetSpreadDirtyMissProbe 安装上述探针（nil 卸载）。
func SetSpreadDirtyMissProbe(fn func()) { spreadDirtyMissProbe = fn }

// bgMatch carries the already-computed query-side mass for a background term.
// The anchor gate skips its posting scan (no candidate generation) and replays
// the same match formula over the anchor candidates in a second phase.
type bgMatch struct {
	group    uint64
	weight   float64
	qTerm    Term
	detached bool
	alt      []uint64
}

func (e *Engine) searchClustersWithPositions(qTerms map[uint64]Term, qPositions map[uint64][]TermPosition, topK int) []Hit {
	idx := e.Index
	st := e.ensureClusterState()
	sc := e.ensureQueryScratch(idx.N, st.clusterN)

	queryHashes := sc.queryHashes[:0]
	for h := range qTerms {
		queryHashes = append(queryHashes, h)
	}
	sortUint64Asc(queryHashes)
	sc.queryHashes = queryHashes
	gateOn := e.AnchorGate
	if gateOn {
		anchors := 0
		for _, h := range queryHashes {
			if m, ok := idx.TermMass(h); ok && m > anchorGateMassNats {
				anchors++
			}
		}
		if anchors == 0 {
			gateOn = false
		}
	}

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

	hashGroup := sc.hashGroup
	if hashGroup == nil {
		hashGroup = make(map[uint64]uint64, len(queryHashes))
		sc.hashGroup = hashGroup
	} else {
		clear(hashGroup)
	}
	detachedFrag := sc.detachedFrag
	if detachedFrag == nil {
		detachedFrag = make(map[uint64]bool, len(queryHashes))
		sc.detachedFrag = detachedFrag
	} else {
		clear(detachedFrag)
	}
	detachedSlot2 := sc.detachedSlot2
	if detachedSlot2 == nil {
		detachedSlot2 = make(map[uint64]bool, len(queryHashes))
		sc.detachedSlot2 = detachedSlot2
	} else {
		clear(detachedSlot2)
	}
	famMinDF := sc.famMinDF
	if famMinDF == nil {
		famMinDF = make(map[uint64]int, len(queryHashes))
		sc.famMinDF = famMinDF
	} else {
		clear(famMinDF)
	}
	for _, h := range queryHashes {
		qTerm := qTerms[h]
		isOrdDay := qTerm.Role == RoleClosedBeacon && qTerm.Flags&rmFlagCompanion == 0 && qTerm.ExtFlags == ExtFlagSeenMorphOrClos
		baseline, ok := idx.TermMass(h)
		if (!ok || baseline <= 0) && !isOrdDay {
			continue
		}
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
		const microCorpusMaxDocs = 20
		isMicroBoundSfx := isSlot2 && !isPrim && idx.N <= microCorpusMaxDocs && df >= 2
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

	properSlots := sc.properSlots
	if properSlots == nil {
		properSlots = make(map[TermPosition]int, 16)
		sc.properSlots = properSlots
	} else {
		clear(properSlots)
	}
	var compoundPartner map[uint64]uint64
	if len(qPositions) > 0 {
		primaryAtPos := sc.primaryAtPos
		if primaryAtPos == nil {
			primaryAtPos = make(map[TermPosition]uint64, len(qTerms)*2)
			sc.primaryAtPos = primaryAtPos
		} else {
			clear(primaryAtPos)
		}
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
			// Compound-partner head/tail shape: the head must be wider than the tail
			// (b0 > b2) with a small width gap versus the corpus mean (meanBits >
			// (b0-b2)*e). Admissions are expressed in inclusion direction.
			if b0 > b2 && meanBits > float64(b0-b2)*eulerNumber {
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

	// Recycle inner group maps into per-query pools, then drop the outer keys so
	// this query observes exactly the keys it inserts while reusing bucket memory.
	groupUnits := sc.groupUnits
	if groupUnits == nil {
		groupUnits = make(map[uint64]map[int32]struct{}, len(queryHashes))
		sc.groupUnits = groupUnits
	}
	for k, m := range groupUnits {
		clear(m)
		sc.umPool = append(sc.umPool, m)
		delete(groupUnits, k)
	}
	groupAnchorUnits := sc.groupAnchorUnits
	if groupAnchorUnits == nil {
		groupAnchorUnits = make(map[uint64]map[int32]struct{}, len(queryHashes))
		sc.groupAnchorUnits = groupAnchorUnits
	}
	for k, m := range groupAnchorUnits {
		clear(m)
		sc.aumPool = append(sc.aumPool, m)
		delete(groupAnchorUnits, k)
	}
	groupDays := sc.groupDays
	if groupDays == nil {
		groupDays = make(map[uint64]map[int64]struct{}, len(queryHashes))
		sc.groupDays = groupDays
	}
	for k, m := range groupDays {
		clear(m)
		sc.dmPool = append(sc.dmPool, m)
		delete(groupDays, k)
	}
	groupRigid := sc.groupRigid
	if groupRigid == nil {
		groupRigid = make(map[uint64]bool, len(queryHashes))
		sc.groupRigid = groupRigid
	} else {
		clear(groupRigid)
	}
	groupProper := sc.groupProper
	if groupProper == nil {
		groupProper = make(map[uint64]bool, len(queryHashes))
		sc.groupProper = groupProper
	} else {
		clear(groupProper)
	}
	hasBroadComp := sc.hasBroadComp
	if hasBroadComp == nil {
		hasBroadComp = make(map[uint64]bool, len(queryHashes))
		sc.hasBroadComp = hasBroadComp
	} else {
		clear(hasBroadComp)
	}
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
		// [count-floor] properGuardMinDF — 文档数下限（篇）。低于它时词项的大写占比不稳，
		// 不启用专名守卫。旧写法 `float64(dfH) > e²` 把无量纲常数接到文档计数上
		// （量纲不齐）；因 df 为整数，`df > e²` ⟺ `df ≥ 8`，故换为整数并逐位不变（A24）。
		const properGuardMinDF = 8
		isTrueProper := qa.Flags&rmProperMask != 0 && float64(bits)*eulerNumber >= meanBits && dfH <= highDFThresh
		if isTrueProper && dfH >= properGuardMinDF && st.properRatioOf(h) < transmissionPerHop && !hasAdjacentProper(h, qa) {
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
			if n := len(sc.umPool); n > 0 {
				um = sc.umPool[n-1]
				sc.umPool = sc.umPool[:n-1]
			} else {
				um = make(map[int32]struct{})
			}
			groupUnits[g] = um
		}
		aum := groupAnchorUnits[g]
		if aum == nil {
			if n := len(sc.aumPool); n > 0 {
				aum = sc.aumPool[n-1]
				sc.aumPool = sc.aumPool[:n-1]
			} else {
				aum = make(map[int32]struct{})
			}
			groupAnchorUnits[g] = aum
		}
		dm := groupDays[g]
		if dm == nil {
			if n := len(sc.dmPool); n > 0 {
				dm = sc.dmPool[n-1]
				sc.dmPool = sc.dmPool[:n-1]
			} else {
				dm = make(map[int64]struct{})
			}
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
		isBroadComp := h != g && baseDF > 0 && (float64(dfH) > eulerNumber*float64(baseDF) || (isSlot2Comp && dfH > dfG))
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
	kappaOcc := sc.kappaOcc
	if kappaOcc == nil {
		kappaOcc = make(map[uint64]float64, len(groupUnits))
		sc.kappaOcc = kappaOcc
	} else {
		clear(kappaOcc)
	}
	anchorKappa := sc.anchorKappa
	if anchorKappa == nil {
		anchorKappa = make(map[uint64]float64, len(groupUnits))
		sc.anchorKappa = anchorKappa
	} else {
		clear(anchorKappa)
	}
	// 两套凝聚度口径（非对称，均承重，改动须配对多语料测量）：
	//   kappaOcc   = 1 - (特异提及的日数) / E0(全部提及数)  —— 分母按总用量计，等价于「特异性
	//                提及之外的总量被视作可摊开的日子」，对高用量词族更宽松（批量特化折扣）。
	//   anchorKappa= 1 - (特异提及的日数) / E0(特异提及数) —— 同一观测集上的严格估计量。
	// 已证伪：把 kappaOcc 换成任一一致估计量（D_all 配 n_all，或 D_spec 配 n_spec）都会显著退化。
	// 原因是两者的分母不同——一致估计量按特异提及计分母，会把词族自身的批量用法当成分散证据，
	// 从而把实际承重的锚点降级；此处的不对称属设计选择，不是笔误，改动须重跑配对多语料测量。
	for g, um := range groupUnits {
		n := float64(len(um))
		dObs := float64(len(groupDays[g]))
		if n > 1 && dObs >= 1 {
			kappaOcc[g] = calendarConcentration(dObs, n, totalDaysM)
		}
		na := float64(len(groupAnchorUnits[g]))
		if na > 1 && dObs >= 1 {
			anchorKappa[g] = calendarConcentration(dObs, na, totalDaysM)
		}
	}

	hasProperAnchor := false
	hasSpecificProper := false
	// specificProperDFFraction (~4.98%, calibrated at 1/e^3) caps the document
	// frequency of a proper noun that still qualifies as a specific entity anchor.
	// 守卫 min 1：N<=20 时 int(N·fraction) 截断为 0 会把专名特异性判定与折叠截止
	// （rareInflectedTermMaxDF 同源上界）整体关停。
	// [calibrated] specificProperDFFraction — share of N (dimensionless) capping the
	// document frequency of a proper noun that still qualifies as a specific anchor
	// (≈4.98%, fitted on the reference corpus; the value coincides with e^-3 within one
	// ulp but is empirical). Recalibrate with the corpus; keep the min-1 guard below.
	const specificProperDFFraction = 0.049787068367863944
	specificPropMax := int(float64(idx.N) * specificProperDFFraction)
	if specificPropMax < 1 {
		specificPropMax = 1
	}
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
	isAnchorGroup := sc.isAnchorGroup
	if isAnchorGroup == nil {
		isAnchorGroup = make(map[uint64]bool, len(groupUnits))
		sc.isAnchorGroup = isAnchorGroup
	} else {
		clear(isAnchorGroup)
	}
	aGen := sc.nextAnchorGen()
	uAnchorGen := sc.unitAnchorGen[:idx.N]
	cAnchorGen := sc.clusterAnchorGen[:st.clusterN]
	anchorTimedSlots := sc.anchorTimeSlots[:0]
	nTimedBuckets := int32(len(st.bucketTimes))
	// Anchor cone co-membership: marginalizing the arbitrary cell phase
	// uniformly over [0, T) turns the boolean day-cell relation into its exact
	// expectation, the triangular profile tri(dt) = max(0, 1 - |dt|/T): the
	// probability that two clocks dt apart share one cell of width T under a
	// uniform phase. The physical period T (Earth's rotation, carried by the
	// corpus' daily rhythm) is kept while the UTC midnight phase is removed,
	// preserving the binary {1/e, 1} contrast. The two consumer polarities take
	// the two canonical cuts of tri: the energy-cutting struct gate uses the
	// equal-measure core tri >= 1/2 (|dt| <= T/2), while amnesty decisions
	// (shell co-hit exemption from damping, stem-fold cone exemption) use the
	// full support tri > 0 (|dt| < T), i.e. fail-open towards not punishing.
	// Time-shift invariant by construction (depends on |t_u - t_a| only), so
	// per-unit wall-clock offsets degrade smoothly.
	const dayPeriod = int64(86400)
	const dayHalfPeriod = dayPeriod / 2
	anchorTimes := sc.anchorTimes[:0]
	anchorUnitList := sc.anchorUnitList[:0]
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
				anchorKappa[g] >= anchorConcentrationFloor && len(groupAnchorUnits[g]) < minClockUnits && float64(gBits)*2.0 >= meanBits {
				isAnchorGroup[g] = true
			}
		} else if kappaOcc[g] >= anchorConcentrationFloor && compoundPartner[g] == 0 {
			isAnchorGroup[g] = true
		}
		if isAnchorGroup[g] {
			hasAnchor = true
			srcUnits := um
			if len(groupAnchorUnits[g]) > 0 {
				srcUnits = groupAnchorUnits[g]
			}
			for u := range srcUnits {
				if uAnchorGen[u] != aGen {
					uAnchorGen[u] = aGen
					anchorUnitList = append(anchorUnitList, u)
				}
				cl := st.clusters[u]
				if cAnchorGen[cl] != aGen {
					cAnchorGen[cl] = aGen
					if cl >= 0 && cl < nTimedBuckets {
						anchorTimedSlots = append(anchorTimedSlots, cl)
					}
				}
				if ts := st.refTimes[u]; ts > 0 {
					anchorTimes = append(anchorTimes, ts)
				}
			}
		}
	}
	sortInt32Asc(anchorUnitList)
	sc.anchorUnitList = anchorUnitList
	sc.anchorTimeSlots = anchorTimedSlots
	sortInt64Asc(anchorTimes)
	sc.anchorTimes = anchorTimes
	nearestAnchorDT := func(u int32) int64 {
		ts := st.refTimes[u]
		if ts <= 0 || len(anchorTimes) == 0 {
			return int64(1) << 62
		}
		k := sort.Search(len(anchorTimes), func(i int) bool { return anchorTimes[i] >= ts })
		dt := int64(1) << 62
		if k < len(anchorTimes) {
			dt = anchorTimes[k] - ts
		}
		if k > 0 && ts-anchorTimes[k-1] < dt {
			dt = ts - anchorTimes[k-1]
		}
		return dt
	}
	inAnchorCone := func(u int32) bool { return nearestAnchorDT(u) <= dayHalfPeriod }
	inAnchorConeSupport := func(u int32) bool { return nearestAnchorDT(u) < dayPeriod }
	tauMacro := math.Expm1(st.muLogGap + st.sigLogGap)
	if tauMacro <= 0 {
		tauMacro = st.meanGap * eulerNumber
	}
	const (
		maxOrdinalNeighborSpan  int32 = 8
		maxAnchorProtectedUnits int   = 7
	)
	ordHorizon := maxOrdinalNeighborSpan
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
	shellScale := sc.shellScale
	if shellScale == nil {
		shellScale = make(map[uint64]float64, len(queryHashes))
		sc.shellScale = shellScale
	} else {
		clear(shellScale)
	}
	if hasAnchor {
		for g, um := range groupUnits {
			if isAnchorGroup[g] || groupRigid[g] || kappaOcc[g] >= shellScatterFloor || len(groupAnchorUnits[g]) <= maxAnchorProtectedUnits {
				continue
			}
			coHits := 0
			for u := range um {
				if uAnchorGen[u] == aGen || inAnchorConeSupport(u) {
					coHits++
					break
				}
			}
			if coHits == 0 {
				shellScale[g] = transmissionPerHop
			}
		}
	}

	groups := sc.groups
	if groups == nil {
		groups = make(map[uint64]float64, len(queryHashes))
		sc.groups = groups
	} else {
		clear(groups)
	}
	var bgInfo map[uint64]bgMatch
	candidates := sc.candidates[:idx.N]
	unitStructGate := sc.unitStructGate[:idx.N]
	dGen := sc.resetDirtyUnits(idx.N)
	activateUnit := func(u int32) {
		if sc.unitDirtyGen[u] != dGen {
			sc.unitDirtyGen[u] = dGen
			sc.dirtyUnits = append(sc.dirtyUnits, u)
			gate := 1.0
			if hasAnchor && uAnchorGen[u] != aGen && cAnchorGen[st.clusters[u]] != aGen && !inAnchorCone(u) {
				gate = transmissionPerHop
			}
			unitStructGate[u] = gate
		}
	}

	var extraGroups map[uint64][]uint64
	if len(qPositions) > 0 {
		slotPrim := sc.slotPrim
		if slotPrim == nil {
			slotPrim = make(map[TermPosition][]uint64, len(queryHashes)*2)
			sc.slotPrim = slotPrim
		} else {
			clear(slotPrim)
		}
		slotPure := sc.slotPure
		if slotPure == nil {
			slotPure = make(map[TermPosition]bool, len(queryHashes)*2)
			sc.slotPure = slotPure
		} else {
			clear(slotPure)
		}
		for _, h := range queryHashes {
			if g := hashGroup[h]; g == h && !detachedFrag[g] {
				isPure := qTerms[h].Flags&rmFlagCompanion == 0
				df := idx.TermDF(g)
				for _, pos := range qPositions[h] {
					// 属主裁决只用品类与语料统计：纯词优先，同纯度取 df 更大者；
					// 纯度与 df 双双平手时**并列共存**（不择一）。历史上这里以
					// `g < prev` 收尾，等价于让 atomizer key 决定槽主，已删除。
					owners, exists := slotPrim[pos]
					switch {
					case !exists:
						slotPrim[pos] = append(owners[:0], g)
						slotPure[pos] = isPure
					case isPure && !slotPure[pos]:
						slotPrim[pos] = append(owners[:0], g)
						slotPure[pos] = isPure
					case isPure == slotPure[pos]:
						prevDF := 0
						if len(owners) > 0 {
							prevDF = idx.TermDF(owners[0])
						}
						switch {
						case df > prevDF:
							slotPrim[pos] = append(owners[:0], g)
						case df == prevDF:
							if len(owners) == 0 || g < owners[0] {
								slotPrim[pos] = append(owners[:0], g)
							}
						}
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
				for _, altG := range slotPrim[pos] {
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
	}

	for _, h := range queryHashes {
		qTerm := qTerms[h]
		isInferredOrdDay := qTerm.Role == RoleClosedBeacon && qTerm.Flags&rmFlagCompanion == 0 && qTerm.ExtFlags == ExtFlagSeenMorphOrClos
		baseline, ok := idx.TermMass(h)
		origMass := baseline
		if !ok || baseline <= 0 {
			if !isInferredOrdDay {
				continue
			}
			ordDay := UnpackInferredOrdinalDay(qTerm.Sent, qTerm.Slot)
			if ordDay <= 0 {
				continue
			}
			var dayUnits []int32
			for u := int32(0); int(u) < idx.N; u++ {
				if st.fastDocs[u].day == ordDay {
					dayUnits = append(dayUnits, u)
				}
			}
			if len(dayUnits) == 0 || len(dayUnits) > highDFThresh {
				continue
			}
			group := hashGroup[h]
			if group == 0 {
				group = h
			}
			dfDay := float64(len(dayUnits))
			baseline = math.Log1p((float64(idx.N) - dfDay + 0.5) / (dfDay + 0.5))
			if len(dayUnits) > 1 && totalDaysM > 0 {
				// The dispatch term is the day itself: every observed unit falls on
				// that single calendar day, so dObs = 1. Reuse the same exact
				// null-occupancy estimator as the term path; a private (n-1)/(n+2)
				// shrink here would be a second implementation of κ_occ.
				kOcc := calendarConcentration(1, dfDay, totalDaysM)
				baseline *= 1.0 + kOcc*transmissionPerHop
			}
			weight := baseline
			if meanBits > 0 && qTerm.Bits > 0 {
				weight *= bitsGainSqrt(float64(qTerm.Bits) / meanBits)
			}
			if weight > groups[group] {
				groups[group] = weight
			}
			hasSameDayAnchor := false
			for _, au := range anchorUnitList {
				if st.fastDocs[au].day == ordDay {
					hasSameDayAnchor = true
					break
				}
			}
			for _, u := range dayUnits {
				coupled := !hasSameDayAnchor || uAnchorGen[u] == aGen
				if !coupled && st.refTimes[u] > 0 {
					tu := st.refTimes[u]
					for _, au := range anchorUnitList {
						ta := st.refTimes[au]
						if ta > 0 && tu-ta < dayPeriod && ta-tu < dayPeriod && st.docSubjectCos(idx, sc, u, au) >= transmissionPerHop*transmissionPerHop {
							coupled = true
							break
						}
					}
				}
				if !coupled {
					continue
				}
				if sc.unitDirtyGen[u] != dGen {
					activateUnit(u)
					sc.lexUnits = append(sc.lexUnits, u)
				}
				matchedMass := weight
				if e.FlagsGain > 0 {
					matchedMass *= 1.0 + e.FlagsGain/eulerNumber
				}
				unitEntries := st.docEntriesOf(u)
				if len(unitEntries) > 0 {
					candidates[u].upsertMatch(sc, group, matchedMass, -1, qTerm.Bits, unitEntries, false)
				}
			}
			continue
		}
		group := hashGroup[h]
		isPrimH := st.isPrimaryOf(h)
		dfH := idx.TermDF(h)
		// The stem-fold cutoff is an independent engineering bound (host-word DF
		// tier eligible for stem folding); specificPropMax bounds proper-anchor
		// specificity. They serve different roles and keep separate thresholds.
		// [calibrated] rareInflectedTermMaxDF — document-count tier (≈21 documents) up to
		// which a stem may fold onto its host word. An empirical operating point: the tier
		// separates inflection variants (few documents each) from vocabulary that is already
		// common enough to stand on its own, and folding the latter attaches a frequent word
		// to whichever host happened to co-occur first. Merging it with the neighbouring
		// cutoffs was measured and rejected — the two bounds answer different questions, so a
		// shared value over-folds one side; keep it independent and re-measure jointly.
		const rareInflectedTermMaxDF = 21
		if h == group && !groupRigid[group] && !groupProper[group] && anchorKappa[group] < anchorConcentrationFloor && (dfH <= rareInflectedTermMaxDF || dfH <= specificPropMax) {
			for _, ch := range queryHashes {
				if ch == h || qTerms[ch].Group != group {
					continue
				}
				if qTerms[ch].IsSlot2Companion(qTerms[group]) || !st.isPrimaryOf(ch) {
					continue
				}
				// Stem-fold host dominance: fold stem mass only when the primary
				// dominates the in-family companion (b >= b_companion) and the host is
				// not over-narrow versus the corpus mean width (e*b >= 2*b_bar).
				if qTerms[group].Bits >= qTerms[ch].Bits && float64(qTerms[group].Bits)*eulerNumber >= meanBits*2.0 {
					dfStem := idx.TermDF(ch)
					if dfH > rareInflectedTermMaxDF && dfStem <= highDFThresh {
						if !hasAnchor || shellScale[group] > 0 {
							continue
						}
						hInCone := false
						for _, p := range st.termPostings(h) {
							if uAnchorGen[p.unit] == aGen || inAnchorConeSupport(p.unit) {
								hInCone = true
								break
							}
						}
						if hInCone {
							continue
						}
					}
					if float64(dfStem) > eulerNumber*float64(dfH) {
						if stemMass, okS := idx.TermMass(ch); okS && stemMass > 0 && stemMass < baseline {
							if dfStem > highDFThresh {
								baseline = math.Sqrt(baseline * stemMass)
							} else {
								baseline = (1.0-transmissionPerHop)*baseline + transmissionPerHop*stemMass
							}
							break
						}
					}
				}
			}
		}
		if sc, ok := shellScale[group]; ok && sc > 0 {
			baseline *= sc
		}
		if !detachedFrag[group] && kappaOcc[group] > 0 {
			baseline *= 1.0 + kappaOcc[group]*transmissionPerHop
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
					if len(extraGroups[h]) == 0 {
						qRep = math.Min(1.0, float64(indepQTF-1))
					}
					specRatio := math.Max(0, 1.0-float64(dfH)/float64(specificPropMax+1))
					qMod := math.Min(1.0, transmissionPerHop+(1.0-transmissionPerHop)*(kappaOcc[group]+specRatio))
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
		if gateOn && !isInferredOrdDay && ok && origMass > 0 && origMass <= anchorGateMassNats {
			if bgInfo == nil {
				bgInfo = make(map[uint64]bgMatch, 16)
			}
			bgInfo[h] = bgMatch{group: group, weight: weight, qTerm: qTerm, detached: detachedFrag[group], alt: extraGroups[h]}
			continue
		}
		ordDay := noDay
		hasSameDayAnchor := false
		if isInferredOrdDay {
			dayCnt := make(map[int64]int, 8)
			bestCnt := 0
			for _, p := range st.termPostings(h) {
				if d := st.fastDocs[p.unit].day; d != noDay {
					c := dayCnt[d] + 1
					dayCnt[d] = c
					if c > bestCnt || (c == bestCnt && d < ordDay) {
						bestCnt = c
						ordDay = d
					}
				}
			}
			if ordDay != noDay {
				for _, au := range anchorUnitList {
					if st.fastDocs[au].day == ordDay {
						hasSameDayAnchor = true
						break
					}
				}
			}
		}
		for _, p := range st.termPostings(h) {
			if sc.unitDirtyGen[p.unit] != dGen {
				activateUnit(p.unit)
				sc.lexUnits = append(sc.lexUnits, p.unit)
			}
			c := &candidates[p.unit]
			unitEntries := st.docEntriesOf(p.unit)
			docEntry := unitEntries[p.entry]
			repeats := docRepeats(docEntry.tf)
			if detachedFrag[group] {
				repeats *= transmissionPerHop
			}
			ordBystanderDamp := false
			if isInferredOrdDay && hasSameDayAnchor {
				coupled := uAnchorGen[p.unit] == aGen
				if !coupled && st.refTimes[p.unit] > 0 {
					tu := st.refTimes[p.unit]
					for _, au := range anchorUnitList {
						ta := st.refTimes[au]
						if ta > 0 && tu-ta < dayPeriod && ta-tu < dayPeriod && st.docSubjectCos(idx, sc, p.unit, au) >= transmissionPerHop*transmissionPerHop {
							coupled = true
							break
						}
					}
				}
				if coupled {
					if ordDay != noDay && st.fastDocs[p.unit].day == ordDay {
						repeats = 1.0
					}
				} else {
					repeats = 0
					ordBystanderDamp = true
				}
			}
			// 声明式按单元标签定价：UnitType/TypeMass 只由测量器材安装（生产恒 nil），
			// 标签是不透明整数 id，内核不解释其含义。缺项时逐位回退池化质量。
			unitWeight := weight
			if e.UnitType != nil {
				if label := e.UnitType[p.unit]; label >= 0 && int(label) < len(e.TypeMass) {
					if typed, ok := e.TypeMass[label][h]; ok && typed > 0 {
						unitWeight = typed
						if meanBits > 0 && bits > 0 {
							unitWeight *= bitsGainSqrt(float64(bits) / meanBits)
						}
					}
				}
			}
			matchedMass := unitWeight * (1.0 + repeats)
			if ordBystanderDamp {
				matchedMass *= transmissionPerHop
			}
			if qTerm.Flags&rmFlagCompanion != 0 && docEntry.flags&rmFlagCompanion != 0 && qTerm.Group != 0 && docEntry.group != 0 && docEntry.group != qTerm.Group {
				if origA, okA := qTerms[qTerm.Group]; okA && qTerm.IsSlot2Companion(origA) {
					if isPrimH {
						matchedMass *= transmissionPerHop
					} else if !detachedFrag[group] {
						matchedMass *= transmissionPerHop * transmissionPerHop
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
				matchedMass *= 1.0 + e.FlagsGain/eulerNumber
			}
			c.upsertMatch(sc, group, matchedMass, p.entry, docEntry.bits, unitEntries, true)
			for _, altG := range extraGroups[h] {
				c.upsertMatch(sc, altG, matchedMass*transmissionPerHop, p.entry, docEntry.bits, unitEntries, false)
			}
		}
	}

	if gateOn && len(bgInfo) > 0 {
		bgHashes := make([]uint64, 0, len(bgInfo))
		for h := range bgInfo {
			bgHashes = append(bgHashes, h)
		}
		sortUint64Asc(bgHashes)
		for _, u := range sc.lexUnits {
			entries := st.docEntriesOf(u)
			if len(entries) == 0 {
				continue
			}
			byHash := make(map[uint64]int32, len(entries))
			for ei := range entries {
				byHash[entries[ei].id] = int32(ei)
			}
			for _, h := range bgHashes {
				ei, ok := byHash[h]
				if !ok {
					continue
				}
				info := bgInfo[h]
				entry := entries[ei]
				repeats := docRepeats(entry.tf)
				if info.detached {
					repeats *= transmissionPerHop
				}
				matchedMass := info.weight * (1.0 + repeats)
				if info.qTerm.Flags&rmFlagCompanion != 0 && entry.flags&rmFlagCompanion != 0 && info.qTerm.Group != 0 && entry.group != 0 && entry.group != info.qTerm.Group {
					if origA, okA := qTerms[info.qTerm.Group]; okA && info.qTerm.IsSlot2Companion(origA) {
						if st.isPrimaryOf(h) {
							matchedMass *= transmissionPerHop
						} else if !info.detached {
							matchedMass *= transmissionPerHop * transmissionPerHop
						}
					}
				}
				if meanBits > 0 && entry.bits > 0 {
					matchedMass *= bitsGainSqrt(float64(entry.bits) / meanBits)
				}
				docGainMask := uint8(1<<2 | 1<<5 | 1<<7)
				if info.qTerm.Flags&rmBitBoundaryQuote != 0 {
					docGainMask |= rmBitBoundaryQuote
				}
				if e.FlagsGain > 0 && entry.flags&docGainMask != 0 {
					matchedMass *= 1.0 + e.FlagsGain/eulerNumber
				}
				c := &candidates[u]
				c.upsertMatch(sc, info.group, matchedMass, ei, entry.bits, entries, true)
				for _, altG := range info.alt {
					c.upsertMatch(sc, altG, matchedMass*transmissionPerHop, ei, entry.bits, entries, false)
				}
			}
		}
	}

	// Canonical match order: every later float sum over a candidate's matches
	// (query mass, missing mass, phrase witness) must add the same magnitudes in
	// the same order, otherwise two hash layouts that attach the same evidence to
	// different group ids differ in the last bits. Sorting by (mass, group) makes
	// the accumulation order independent of the discovery order.
	for _, u := range sc.lexUnits {
		sortFeatureMatchesByMassGroup(candidates[u].matches)
	}
	groupKeys := sc.groupKeys[:0]
	for g := range groups {
		groupKeys = append(groupKeys, g)
	}
	sortUint64Asc(groupKeys)
	sc.groupKeys = groupKeys
	// Sum the query-group masses in value order: the group ids attached to each
	// magnitude swap when two roles exchange hashes, and float addition is
	// commutative but not associative, so key order would leak into the total.
	var total, sumG2 float64
	{
		masses := make([]float64, 0, len(groupKeys))
		for _, g := range groupKeys {
			masses = append(masses, groups[g])
		}
		sort.Float64s(masses)
		for _, m := range masses {
			total += m
			sumG2 += m * m
		}
	}
	if total <= 0 {
		return nil
	}
	muQ := sumG2 / total

	var bestCoverage float64
	coreLex := sc.coreLex[:idx.N]
	sortInt32Asc(sc.lexUnits)
	for _, u := range sc.lexUnits {
		c := &candidates[u]
		gate := unitStructGate[u]
		for i := range c.matches {
			m := &c.matches[i]
			if m.primMass > 0 && m.compMass > 0 && hasBroadComp[m.group] && groupProper[m.group] {
				if bp := m.primMass * (1.0 + transmissionPerHop*transmissionPerHop); bp > m.mass {
					m.mass = bp
					m.entry = m.primEntry
				}
			}
			if partnerG := compoundPartner[m.group]; partnerG != 0 && !c.hasGroup(partnerG) {
				m.mass *= transmissionPerHop
			}
			if hasSpecificProper && gate < 1.0 {
				g := m.group
				// [count-floor] shellGuardMinAnchorUnits — 单元数下限（个）：少于 8 个特异
				// 单元时不施加口语壳消光。旧写法 `len > e²` 同属量纲不齐，整数等价于 ≥ 8（A24）。
				const shellGuardMinAnchorUnits = 8
				if !isAnchorGroup[g] && !groupRigid[g] && kappaOcc[g] < shellScatterFloor && len(groupAnchorUnits[g]) >= shellGuardMinAnchorUnits {
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
			// Mass-only synthetic matches carry no local entry, hence no repetition discount.
			var repeats float64
			if c.matches[0].entry >= 0 {
				unitEntries := st.docEntriesOf(u)
				repeats = docRepeats(unitEntries[c.matches[0].entry].tf)
			}
			if detachedFrag[c.matches[0].group] {
				repeats *= transmissionPerHop
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
			!qa.IsSlot2Companion(qTerms[g]) && dfG > 0 && dfH > dfG && float64(dfH) <= eulerNumber*float64(dfG)
	}
	isCoreClosedBeacon := func(h uint64, qa Term) bool {
		g := hashGroup[h]
		return !detachedFrag[g] && qa.Role == RoleClosedBeacon && idx.TermDF(h) <= highDFThresh
	}
	phraseWitness := sc.phraseWitness[:idx.N]
	for _, pair := range rmQueryDyadsWithPositionsInto(sc, qTerms, qPositions) {
		dfPhrase := st.phrasePostingCount(pair)
		if dfPhrase == 0 {
			continue
		}
		wa, okA := effWForQueryTerm(pair.a)
		wb, okB := effWForQueryTerm(pair.b)
		if !okA || !okB {
			continue
		}
		joint := math.Log1p((float64(idx.N) - float64(dfPhrase) + 0.5) / (float64(dfPhrase) + 0.5))
		// 失真压缩核（joint/(1+joint)，1+Nats 纯量加法）经预注册判负实验确证承重：
		// 保量纲替换 1−exp(−joint) 判负回滚（Main 配对 −0.8144pp，翻转 +1/−11；
		// 正本 2026-11-23-phrase-occupancy-squash-prereg，开关 PhraseOccupancySquash
		// 仅作判别器材保留）。两式单调关系成立（1−e^{−j} ≥ j/(1+j)），判负非实现误差。
		phraseSquash := joint / (1.0 + joint)
		if e.PhraseOccupancySquash {
			phraseSquash = 1 - math.Exp(-joint)
		}
		mass := math.Min(wa, wb) * phraseSquash
		qaA, qaB := qTerms[pair.a], qTerms[pair.b]
		if qaA.Flags&rmFlagCompanion != 0 && qaB.Flags&rmFlagCompanion != 0 {
			orthoCross := (isConservedInflectionLemma(pair.a, qaA) && isCoreClosedBeacon(pair.b, qaB)) ||
				(isConservedInflectionLemma(pair.b, qaB) && isCoreClosedBeacon(pair.a, qaA))
			if !orthoCross {
				mass *= transmissionPerHop
			}
		}
		var sum float64
		st.visitPhrasePostings(pair, func(u int32) bool {
			activateUnit(u)
			candidates[u].phrase += mass * unitStructGate[u]
			sum += candidates[u].lex / total
			return true
		})
		denom := 1.0 + float64(dfPhrase)
		st.visitPhrasePostings(pair, func(u int32) bool {
			c := &candidates[u]
			if witness := sum - c.lex/total; witness > 0 {
				phraseWitness[u] += mass * unitStructGate[u] * math.Log1p(witness) / denom
			}
			return true
		})
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
		st.visitNeighbors(seed, func(neighbor int32) bool {
			if neighbor == seed {
				return true
			}
			aff := st.temporalAffinity(idx, seed, neighbor)
			gain := seedE * aff * residual
			if gain > 0 && coreLex[neighbor] >= seedE {
				if rawCos := st.docSubjectCos(idx, sc, seed, neighbor); rawCos < transmissionPerHop*transmissionPerHop {
					gain *= math.Max(transmissionPerHop, math.Sqrt(math.Max(0, rawCos))/transmissionPerHop)
				}
			}
			if seedE > coreLex[neighbor] && unitStructGate[seed] >= 1.0 && aff > 0 {
				if rawCos := st.docSubjectCos(idx, sc, seed, neighbor); rawCos > 0 {
					if sibGain := (seedE - coreLex[neighbor]) * aff * math.Sqrt(rawCos) * transmissionPerHop * transmissionPerHop; sibGain > gain {
						gain = sibGain
					}
				}
			}
			if gain > candidates[neighbor].stSpread {
				activateUnit(neighbor)
				candidates[neighbor].stSpread = gain
			}
			return true
		})
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
	byEpisodeActive := sc.byEpisodeActive
	if byEpisodeActive == nil {
		byEpisodeActive = make(map[int32][]int32, 32)
		sc.byEpisodeActive = byEpisodeActive
	} else {
		clear(byEpisodeActive)
	}
	corefByEpisode := sc.corefByEpisode
	if corefByEpisode == nil {
		corefByEpisode = make(map[int32][]int32, 64)
		sc.corefByEpisode = corefByEpisode
	} else {
		clear(corefByEpisode)
	}
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
		if ep := st.episodeOf[u]; ep >= 0 {
			corefByEpisode[ep] = append(corefByEpisode[ep], u)
		}
		if ep := st.episodeOf[u]; ep >= 0 && unitStructGate[u] >= 1.0 {
			byEpisodeActive[ep] = append(byEpisodeActive[ep], u)
		}
	}
	activeEpisodes := sc.activeEpisodes[:0]
	for ep := range byEpisodeActive {
		activeEpisodes = append(activeEpisodes, ep)
	}
	sortInt32Asc(activeEpisodes)
	sc.activeEpisodes = activeEpisodes
	// [calibrated] tightBurstAffinityFloor — affinity floor (dimensionless) above which
	// two records count as a near-simultaneous burst. An empirical operating point: the
	// floor separates co-timed records from records that merely share a coarse timestamp,
	// and both sides of it were measured to be worse (below it the burst axis degenerates
	// into same-day adjacency, above it genuine co-timed events drop out).
	// Not exp(-1/e^3) (that double is 0.9514319929004534, 8.3e-8 away); the literal keeps
	// the calibration bit-for-bit and must not be re-derived from e. Change it only
	// together with a paired multi-corpus measurement.
	const tightBurstAffinityFloor = 0.9514319102438668
	burstAffFloor := tightBurstAffinityFloor
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
				rawCos := st.docSubjectCos(idx, sc, seed, nb)
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
					if detachedFrag[sm.group] || idx.TermDF(sm.group) > highDFThresh || sm.entry < 0 {
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
				exScale := transmissionPerHop
				if st.muLogGap > 0 && st.ordPos[seed] >= 0 && st.ordPos[nb] >= 0 {
					tauMicro := math.Max(1.0, math.Expm1(math.Max(1.0, st.muLogGap-st.sigLogGap)))
					dt := math.Abs(float64(st.refTimes[seed] - st.refTimes[nb]))
					dk := math.Abs(float64(st.ordPos[seed] - st.ordPos[nb]))
					if dt <= tauMicro*eulerNumber && dk <= 2 {
						rT := dt / tauMicro
						burstExc := math.Exp(-rT*rT - math.Max(0, dk-1.0))
						exScale = transmissionPerHop * (1.0 + transmissionPerHop*burstExc)
					}
				}
				exGain := excess * aff * cosMod * exScale
				allowOverlay := isSub && (rawCos >= transmissionPerHop || (sharedCoreCount >= 2 && aff >= 1.0-transmissionPerHop*transmissionPerHop && rawCos >= transmissionPerHop*transmissionPerHop))
				if spreadDirtyMissProbe != nil && sc.unitDirtyGen[nb] != dGen {
					spreadDirtyMissProbe()
				}
				if allowOverlay {
					activateUnit(nb)
					candSpread := candidates[nb].stSpread + exGain*math.Sqrt(rawCos)*transmissionPerHop
					if exGain > candSpread {
						candSpread = exGain
					}
					candidates[nb].stSpread = candSpread
				} else if exGain > candidates[nb].stSpread {
					activateUnit(nb)
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
		for u, gain := range e.spread(directSeeds, directMap, qTerms, transmissionPerHop, e.Hop1Budget, 0) {
			if gain > candidates[u].stSpread {
				activateUnit(u)
				candidates[u].stSpread = gain
			}
		}
	}

	sortInt32Asc(sc.dirtyUnits)

	// 3. Intra-sentence pair compactness (residual proximity + pair synergy) and SpacetimeCluster peer corroboration.
	if proximityEntityDispatch != nil {
		proximityEntityDispatch(sc.dirtyUnits, coreLex, candidates, unitStructGate, st, groups, total, residual)
	} else {
		for _, u := range sc.dirtyUnits {
			c := &candidates[u]
			if len(c.matches) >= 2 && coreLex[u] > 0 {
				gate := gateAt(unitStructGate, u)
				prox, syn := st.rmProximityAndSynergy(c, groups, total)
				c.entity += gate * (residual*prox + syn)
			}
		}
	}
	var peak float64
	for _, u := range sc.dirtyUnits {
		if own := candidates[u].lex + candidates[u].phrase + candidates[u].entity; own > peak {
			peak = own
		}
	}
	if peak > 0 {
		strengthOf := func(u int32) float64 {
			o := &candidates[u]
			return coreLex[u] + o.phrase + o.entity
		}
		strengthPool := sc.unitStrengthPool[:0]
		for ep, units := range corefByEpisode {
			strengthPool = strengthPool[:0]
			for _, u := range units {
				strengthPool = append(strengthPool, rmUnitStrength{unit: u, strength: strengthOf(u)})
			}
			sort.Sort(rmUnitStrengthSort(strengthPool))
			for i, it := range strengthPool {
				units[i] = it.unit
			}
			corefByEpisode[ep] = units
		}

		for _, u := range sc.dirtyUnits {
			c := &candidates[u]
			own := coreLex[u] + c.phrase + c.entity
			if own <= 0 {
				continue
			}
			var bestWitness float64
			missingBest := sc.missingBest[:0]
			evalPeerCorrob := func(n int32, affFloor float64) {
				if n == u {
					return
				}
				other := &candidates[n]
				strength := coreLex[n] + other.phrase + other.entity
				if strength <= 0 {
					return
				}
				aff := st.temporalAffinity(idx, u, n)
				if affFloor > aff {
					aff = affFloor
				}
				if aff <= 0 {
					return
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
					if c.coreLex < peakCoreLex*transmissionPerHop {
						rawCos := st.docSubjectCos(idx, sc, u, n)
						w *= math.Max(transmissionPerHop, math.Sqrt(math.Max(0, rawCos)))
					}
					if w > bestWitness {
						bestWitness = w
					}
					return
				}
				if w > bestWitness {
					bestWitness = w
				}
				if unitStructGate[u] < 1.0 || strength+other.stSpread <= own {
					return
				}
				rawCos := st.docSubjectCos(idx, sc, u, n)
				if rawCos <= 0 {
					return
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
			st.visitNeighbors(u, func(n int32) bool {
				evalPeerCorrob(n, 0)
				return true
			})
			if len(st.citePeers) > 0 {
				for _, n := range st.citationPeersOf(u) {
					evalPeerCorrob(n, transmissionPerHop)
				}
				for _, n := range st.coCitationPeersOf(u) {
					evalPeerCorrob(n, transmissionPerHop)
				}
			}
			sc.missingBest = missingBest
			var corefPuzzle float64
			if coreLex[u] > 0 {
				if ep := st.episodeOf[u]; ep >= 0 {
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
					for _, n := range corefByEpisode[ep] {
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
							st.ordPos[u]-st.ordPos[n] <= maxOrdinalNeighborSpan &&
							st.ordPos[n]-st.ordPos[u] <= maxOrdinalNeighborSpan
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
								rScale = res * math.Max(rShare, transmissionPerHop) * lexShare
							}
						}
						if !isCloseOrd && strength <= bestWitness && strength*rScale <= corefPuzzle {
							continue
						}
						aff := st.temporalAffinity(idx, u, n)
						if !isCloseOrd && aff*strength <= bestWitness && aff*strength*rScale <= corefPuzzle {
							continue
						}
						rawCos := st.docSubjectCos(idx, sc, u, n)
						if rawCos <= 0 {
							continue
						}
						cos := math.Sqrt(rawCos)
						cosW := cos
						if isCloseOrd && st.muLogGap > 0 && coreLex[n] >= peakCoreLex*(1.0-transmissionPerHop) {
							tauMeso := math.Max(1.0, math.Expm1(st.muLogGap))
							dt := math.Abs(float64(st.refTimes[u] - st.refTimes[n]))
							dk := math.Abs(float64(st.ordPos[u] - st.ordPos[n]))
							burstCoh := math.Exp(-dt/(tauMeso*eulerNumber) - math.Max(0, dk-2.0)*transmissionPerHop)
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
						if unitStructGate[u] >= 1.0 && peerS > ownS && aff >= transmissionPerHop && rawCos >= transmissionPerHop {
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
				c.stCorrob = math.Log1p(bestWitness) * (coreLex[u] + (transmissionPerHop+(1.0-transmissionPerHop)*ratio)*c.phrase + c.entity)
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
				c.stCorrob += transmissionPerHop * (1.0 + selfMod*(cl/total)) * compSum
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
	e.applyIntraEventExplanatoryEndorsement(idx, st, sc, candidates, unitStructGate, corefByEpisode, groups, total, highDFThresh, qTerms)

	if e.EnableStage3Plugin && e.Stage3Plugin != nil {
		e.applyStage3ClusterHook(idx, st, sc, candidates, unitStructGate, qTerms, groups, total)
	}

	// 4. Prior-smoothed bidirectional structured coverage & set-conditioned selection.
	if queryDistinctProbe != nil {
		queryDistinctProbe(len(sc.coreUnits))
	}
	return st.selectClusterHits(idx, sc, candidates, unitStructGate, detachedFrag, qTerms, groups, total, muQ, topK)
}

// applyHigherOrderLatticeContraction evaluates the telescoping subsumption-lattice
// contraction surprisal ln((minDF + 1) / (df_cluster(M_u) + 1)) for candidates matching
// k >= 3 non-detached core query groups (where 2-ary co-occurrence is already captured
// by PhraseDyad), followed by the rigid digit/quantity pass.
// Coupling scale is 1/e^2.
func (st *rmClusterState) applyHigherOrderLatticeContraction(idx *Index, sc *rmQueryScratch, candidates []rmCandidate, unitStructGate []float64, qTerms map[uint64]Term, hashGroup map[uint64]uint64, extraGroups map[uint64][]uint64, detachedFrag map[uint64]bool, shellScale map[uint64]float64, highDFThresh int) {
	if idx == nil || len(candidates) == 0 || len(qTerms) < 3 {
		return
	}
	evalPass := func(hashes []uint64, groupOf map[uint64]uint64, groupW map[uint64]float64, groupDF map[uint64]int, useExtra bool, minPop int, requireMid bool) {
		if len(groupW) < minPop || sc == nil {
			return
		}
		gKeys := sc.latticeGKeys[:0]
		for g := range groupW {
			gKeys = append(gKeys, g)
		}
		sortUint64Asc(gKeys)
		sc.latticeGKeys = gKeys
		var totW float64
		for _, g := range gKeys {
			totW += groupW[g]
		}
		if totW <= 0 {
			return
		}
		sortUint64Asc(hashes)

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
			sortInt32Asc(activeUnits)
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
							gain = transmissionTwoHop * delta * math.Max(1.0, mSum)
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
		sortInt32Asc(activeUnits)
		deltaCache := sc.latticeDeltaCache
		if deltaCache == nil {
			deltaCache = make(map[uint64]float64, len(activeUnits))
			sc.latticeDeltaCache = deltaCache
		} else {
			clear(deltaCache)
		}
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
						gain = transmissionTwoHop * delta * math.Max(1.0, mSum)
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

	groupDF := sc.latticeGroupDF
	if groupDF == nil {
		groupDF = make(map[uint64]int, len(qTerms))
		sc.latticeGroupDF = groupDF
	} else {
		clear(groupDF)
	}
	groupW := sc.latticeGroupW
	if groupW == nil {
		groupW = make(map[uint64]float64, len(qTerms))
		sc.latticeGroupW = groupW
	} else {
		clear(groupW)
	}
	activeHashes := sc.latticeHashes[:0]
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
		if h != g && dfG > 0 && (float64(dfH) > eulerNumber*float64(dfG) || (qTerms[h].IsSlot2Companion(qTerms[g]) && dfH > dfG)) {
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
	sc.latticeHashes = activeHashes
	evalPass(activeHashes, hashGroup, groupW, groupDF, true, 3, false)

	// highDFMidCeil bounds the mid-band of high-DF terms at N/e. The 1/e factor is a
	// calibrated threshold, not a value derived from the df distribution: terms above
	// highDFThresh and at or below this ceiling may still enter the lattice when they
	// carry rigid quantity markers. Changing the factor changes the mid-band population.
	highDFMidCeil := int(float64(idx.N) / eulerNumber)
	const rmRigidDigitQtyMask uint8 = (1 << 2) | (1 << 7)
	hasMidRigid := false
	qGroupMid := sc.latticeQGroupMid
	if qGroupMid == nil {
		qGroupMid = make(map[uint64]uint64, len(qTerms))
		sc.latticeQGroupMid = qGroupMid
	} else {
		clear(qGroupMid)
	}
	groupDFMid := sc.latticeGroupDFMid
	if groupDFMid == nil {
		groupDFMid = make(map[uint64]int, len(qTerms))
		sc.latticeGroupDFMid = groupDFMid
	} else {
		clear(groupDFMid)
	}
	groupWMid := sc.latticeGroupWMid
	if groupWMid == nil {
		groupWMid = make(map[uint64]float64, len(qTerms))
		sc.latticeGroupWMid = groupWMid
	} else {
		clear(groupWMid)
	}
	midHashes := sc.latticeMidHashes[:0]
	for h, qa := range qTerms {
		m, ok := idx.TermMass(h)
		if !ok || m <= 0 {
			continue
		}
		df := idx.TermDF(h)
		isMidRigid := df > highDFThresh && df <= highDFMidCeil && (qa.Flags&rmRigidDigitQtyMask != 0)
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
	sc.latticeMidHashes = midHashes
	if hasMidRigid {
		evalPass(midHashes, qGroupMid, groupWMid, groupDFMid, false, 4, true)
	}
}
