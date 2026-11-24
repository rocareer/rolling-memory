// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package core

import (
	"math"
	"sort"
)

// sortUint64Asc sorts ascending: typed insertion sort on short slices, the
// concrete comparison adapter below the radix crossover, and an 8-bit LSD
// radix sort for large slices (measured ~11x faster at 100k elements, while
// small slices lose to the 8 fixed passes plus scratch allocation).
func sortUint64Asc(s []uint64) {
	if len(s) < 2 {
		return
	}
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
	if len(s) < 4096 {
		sort.Sort(uint64AscSort(s))
		return
	}
	// 8 passes of 8 bits ping-pong between s and one scratch buffer; with an
	// even pass count the sorted data lands back in s, so no final copy runs.
	const (
		radixBits = 8
		radixSize = 1 << radixBits
	)
	scratch := make([]uint64, len(s))
	src, dst := s, scratch
	var count [radixSize]int
	for shift := uint(0); shift < 64; shift += radixBits {
		for i := range count {
			count[i] = 0
		}
		for _, v := range src {
			count[(v>>shift)&(radixSize-1)]++
		}
		sum := 0
		for i, c := range count {
			count[i] = sum
			sum += c
		}
		for _, v := range src {
			d := (v >> shift) & (radixSize - 1)
			dst[count[d]] = v
			count[d]++
		}
		src, dst = dst, src
	}
}

// sortUint32Asc sorts ascending with a typed insertion sort on short slices to
// avoid reflection; larger inputs use the concrete sort.Interface adapter.
func sortUint32Asc(s []uint32) {
	if len(s) < 2 {
		return
	}
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
	sort.Sort(uint32AscSort(s))
}

// sortInt32Asc is the []int32 twin of sortUint32Asc.
func sortInt32Asc(s []int32) {
	if len(s) < 2 {
		return
	}
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
	sort.Sort(int32AscSort(s))
}

// sortUnitGroupItemByGroup orders items by ascending group id, matching the
// previous comparator exactly while avoiding reflection on short slices.
func sortUnitGroupItemByGroup(items []unitGroupItem) {
	if len(items) < 2 {
		return
	}
	if len(items) <= 32 {
		for i := 1; i < len(items); i++ {
			v := items[i]
			j := i - 1
			for j >= 0 && v.group < items[j].group {
				items[j+1] = items[j]
				j--
			}
			items[j+1] = v
		}
		return
	}
	sort.Sort(unitGroupItemSort(items))
}

// Concrete sort.Interface adapters for the typed helpers above and the
// remaining large-slice call sites: sort.Sort runs the same pdqsort with
// direct Swap/Less dispatch instead of the reflection-based sort.Slice path.
type uint64AscSort []uint64

func (s uint64AscSort) Len() int           { return len(s) }
func (s uint64AscSort) Less(i, j int) bool { return s[i] < s[j] }
func (s uint64AscSort) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

type uint32AscSort []uint32

func (s uint32AscSort) Len() int           { return len(s) }
func (s uint32AscSort) Less(i, j int) bool { return s[i] < s[j] }
func (s uint32AscSort) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

type int32AscSort []int32

func (s int32AscSort) Len() int           { return len(s) }
func (s int32AscSort) Less(i, j int) bool { return s[i] < s[j] }
func (s int32AscSort) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

type unitGroupItemSort []unitGroupItem

func (s unitGroupItemSort) Len() int           { return len(s) }
func (s unitGroupItemSort) Less(i, j int) bool { return s[i].group < s[j].group }
func (s unitGroupItemSort) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

type scoredEventUnitSort []scoredEventUnit

func (s scoredEventUnitSort) Len() int           { return len(s) }
func (s scoredEventUnitSort) Less(i, j int) bool { return lessScoredEventUnit(s[i], s[j]) }
func (s scoredEventUnitSort) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

// localSlotZ is the closed-form single-sided geometric series sum of the
// intra-sentence slot kernel exp(-(d-1)/e) over d >= 1:
// Z_local = 1 / (1 - exp(-1/e)) ≈ 3.24886952.
var localSlotZ = 1.0 / (1.0 - math.Exp(-transmissionPerHop))

// harmonicCoverage 是双向覆盖的调和平均 2·C_self·C_peer/(C_self+C_peer)；
// 自和大于 0 才计算（NaN 比较恒假落回 0，与各现场原守卫方向一致），
// 关系构建、事件簇背书与交付边补滤共用本函数。
func harmonicCoverage(selfCov, peerCov float64) float64 {
	if selfCov+peerCov > 0 {
		return 2.0 * selfCov * peerCov / (selfCov + peerCov)
	}
	return 0
}

// UnitRelationPeer represents one peer unit v evaluated against a focal unit u
// in the five-question autonoetic relation space (Identity, Similarity,
// Complementarity, I-Explain, Explains-Me). All energy values are measured in
// Nats (-ln P(Event | H_0)) and probabilities/ratios lie in [0, 1].
type UnitRelationPeer struct {
	UnitID              string  // Peer unit identifier Index.UnitIDs[Unit]
	Unit                int32   // Internal unit index in [0, Index.N)
	ScoreNats           float64 // Primary relation energy for the specific relation channel (Nats)
	SharedNats          float64 // Intersection core anchor mass I(u ∩ v) (Nats)
	SelfExcessNats      float64 // Focal unit's unique mass I(u \ v) (Nats)
	PeerExcessNats      float64 // Peer unit's unique mass I(v \ u) (Nats)
	SelfSalientExcess   float64 // Focal unit's salient unique mass I_{>μ}(u \ v) (Nats)
	PeerSalientExcess   float64 // Peer unit's salient unique mass I_{>μ}(v \ u) (Nats)
	SelfCoverage        float64 // Forward coverage C_{u->v} = I(u ∩ v) / E(u) in [0, 1]
	PeerCoverage        float64 // Reverse coverage C_{v->u} = I(u ∩ v) / E(v) in [0, 1]
	SubjectCosine       float64 // Self-information-squared cosine similarity cos_subj(u, v) in [0, 1]
	TemporalAffinity    float64 // Dual-clock spacetime/provenance affinity in [0, 1]
	SelfExplainWorkNats float64 // Directed explanatory work E(u ▷ v): u explains v via local slot kernel (Nats)
	PeerExplainWorkNats float64 // Directed explanatory work E(v ▷ u): v explains u via local slot kernel (Nats)
}

// UnitRelationProfile holds the complete five-question relation neighborhood of
// a focal document u in the corpus:
//  1. Identical (u ≡ v): "Who is identical to me?" — symmetric echo/clone equivalence;
//  2. Similar (u ~ v): "Who is similar to me?" — symmetric thematic/companion resonance;
//  3. Complementary (u ⊕ v): "Who complements me?" — orthogonal residual puzzle across spacetime/provenance;
//  4. IExplain (u ▷ v): "Whom can I explain?" — downward subsumption where u contains v's anchors and reveals local details;
//  5. ExplainsMe (v ▷ u): "Who can explain me?" — upward subsumption where v contains u's anchors and reveals local details.
type UnitRelationProfile struct {
	UnitID        string             // Focal unit identifier
	Unit          int32              // Focal unit index
	TotalMassNats float64            // Focal unit's total core family mass E(u) (Nats)
	MeanMassNats  float64            // Second-order Rényi characteristic energy scale μ(u) = ∑w² / ∑w (Nats)
	Identical     []UnitRelationPeer // 1. 谁跟我一样 (Identity / Echo Clones)
	Similar       []UnitRelationPeer // 2. 谁跟我相似 (Similarity / Thematic Resonance)
	Complementary []UnitRelationPeer // 3. 谁跟我互补 (Complementarity / Spacetime-Provenance Puzzle)
	IExplain      []UnitRelationPeer // 4. 我能解释谁 (u ▷ v: Downward Subsumption & Local Explanation)
	ExplainsMe    []UnitRelationPeer // 5. 谁能解释我 (v ▷ u: Upward Subsumption & Source Explanation)
}

type unitGroupItem struct {
	group    uint64
	weight   float64
	isCore   bool
	pos      []uint32
	posExact []TermPosition
}

type unitGroupProfile struct {
	items     []unitGroupItem
	totalMass float64
	muMass    float64
}

func (e *Engine) buildUnitGroupProfile(st *rmClusterState, u int32, highDFThresh int) unitGroupProfile {
	sc := e.ensureQueryScratch(e.Index.N, st.clusterN)
	return st.buildUnitGroupProfileWithPos(e.Index, sc, u, highDFThresh, true)
}

func resolveProfileEntryGroupAndWeight(idx *Index, st *rmClusterState, entry rmDocEntry, highDFThresh int) (g uint64, w float64, core bool, ok bool) {
	const rmRigidMask uint8 = (1 << 0) | (1 << 1) | (1 << 2) | (1 << 5) | (1 << 6) | (1 << 7)
	w, ok = st.effWeightOf(entry.id)
	if !ok || w <= 0 {
		w, _ = idx.TermMass(entry.id)
	}
	if math.IsNaN(w) || math.IsInf(w, 0) || w <= 0 {
		return 0, 0, false, false
	}
	df := idx.TermDF(entry.id)
	g = entry.group
	if g == 0 {
		g = entry.id
	}
	core = df <= highDFThresh || (entry.flags&rmRigidMask != 0)
	if entry.id != g && df > highDFThresh {
		if entry.flags&rmFlagCompanion != 0 {
			return 0, 0, false, false
		}
		g = entry.id
	}
	return g, w, core, true
}

func (st *rmClusterState) buildUnitGroupProfileWithPos(idx *Index, sc *rmQueryScratch, u int32, highDFThresh int, withPos bool) unitGroupProfile {
	entries := st.docEntriesOf(u)
	if len(entries) == 0 {
		return unitGroupProfile{}
	}
	wide := st.hasWidePos()

	if sc == nil {
		items := make([]unitGroupItem, 0, len(entries))
		for _, entry := range entries {
			g, w, core, ok := resolveProfileEntryGroupAndWeight(idx, st, entry, highDFThresh)
			if !ok {
				continue
			}
			var cp []uint32
			var cpx []TermPosition
			if withPos {
				if pos := st.entryPositionsOf(entry); len(pos) > 0 {
					cp = append([]uint32(nil), pos...)
					if wide {
						cpx = st.entryPositionsExactOf(entry)
					}
				}
			}
			items = append(items, unitGroupItem{group: g, weight: w, isCore: core, pos: cp, posExact: cpx})
		}
		if len(items) == 0 {
			return unitGroupProfile{}
		}
		sortUnitGroupItemByGroup(items)
		outIdx := 0
		var tot, sqSum float64
		for i := 0; i < len(items); {
			cur := items[i]
			i++
			for i < len(items) && items[i].group == cur.group {
				if items[i].weight > cur.weight {
					cur.weight = items[i].weight
				}
				if items[i].isCore {
					cur.isCore = true
				}
				if withPos && len(items[i].pos) > 0 {
					cur.pos = append(cur.pos, items[i].pos...)
					sortUint32Asc(cur.pos)
				}
				if withPos && len(items[i].posExact) > 0 {
					cur.posExact = append(cur.posExact, items[i].posExact...)
					sort.Sort(termPositionSort(cur.posExact))
				}
				i++
			}
			items[outIdx] = cur
			outIdx++
			tot += cur.weight
			sqSum += cur.weight * cur.weight
		}
		items = items[:outIdx]
		var mu float64
		if tot > 0 {
			mu = sqSum / tot
		}
		return unitGroupProfile{items: items, totalMass: tot, muMass: mu}
	}

	baseItem := len(sc.profileItemPool)
	for _, entry := range entries {
		g, w, core, ok := resolveProfileEntryGroupAndWeight(idx, st, entry, highDFThresh)
		if !ok {
			continue
		}
		var cp []uint32
		var cpx []TermPosition
		if withPos {
			if pos := st.entryPositionsOf(entry); len(pos) > 0 {
				pBase := len(sc.profilePosPool)
				sc.profilePosPool = append(sc.profilePosPool, pos...)
				cp = sc.profilePosPool[pBase:len(sc.profilePosPool):len(sc.profilePosPool)]
				if wide {
					pBaseExact := len(sc.profilePosExactPool)
					sc.profilePosExactPool = st.entryPositionsExactInto(entry, sc.profilePosExactPool)
					cpx = sc.profilePosExactPool[pBaseExact:len(sc.profilePosExactPool):len(sc.profilePosExactPool)]
				}
			}
		}
		sc.profileItemPool = append(sc.profileItemPool, unitGroupItem{group: g, weight: w, isCore: core, pos: cp, posExact: cpx})
	}
	items := sc.profileItemPool[baseItem:]
	if len(items) == 0 {
		return unitGroupProfile{}
	}
	sortUnitGroupItemByGroup(items)

	// In-place run-length folding by ascending group ID (zero map or index slice allocation).
	outIdx := 0
	var tot, sqSum float64
	for i := 0; i < len(items); {
		cur := items[i]
		i++
		if i < len(items) && items[i].group == cur.group {
			mergedPosStart := len(sc.profilePosPool)
			mergedPosExactStart := len(sc.profilePosExactPool)
			if withPos && len(cur.pos) > 0 {
				sc.profilePosPool = append(sc.profilePosPool, cur.pos...)
			}
			if withPos && len(cur.posExact) > 0 {
				sc.profilePosExactPool = append(sc.profilePosExactPool, cur.posExact...)
			}
			for i < len(items) && items[i].group == cur.group {
				if items[i].weight > cur.weight {
					cur.weight = items[i].weight
				}
				if items[i].isCore {
					cur.isCore = true
				}
				if withPos && len(items[i].pos) > 0 {
					sc.profilePosPool = append(sc.profilePosPool, items[i].pos...)
				}
				if withPos && len(items[i].posExact) > 0 {
					sc.profilePosExactPool = append(sc.profilePosExactPool, items[i].posExact...)
				}
				i++
			}
			if withPos && len(sc.profilePosPool) > mergedPosStart {
				cur.pos = sc.profilePosPool[mergedPosStart:len(sc.profilePosPool):len(sc.profilePosPool)]
				sortUint32Asc(cur.pos)
			}
			if withPos && len(sc.profilePosExactPool) > mergedPosExactStart {
				cur.posExact = sc.profilePosExactPool[mergedPosExactStart:len(sc.profilePosExactPool):len(sc.profilePosExactPool)]
				sort.Sort(termPositionSort(cur.posExact))
			}
		}
		items[outIdx] = cur
		outIdx++
		tot += cur.weight
		sqSum += cur.weight * cur.weight
	}
	sc.profileItemPool = sc.profileItemPool[:baseItem+outIdx]
	items = sc.profileItemPool[baseItem : baseItem+outIdx : baseItem+outIdx]
	var mu float64
	if tot > 0 {
		mu = sqSum / tot
	}
	return unitGroupProfile{items: items, totalMass: tot, muMass: mu}
}

// pairIntersectionStats 是 profile 级二项式对偶分解结果 + 逐坐标共享掩码。
// 分解量本身由 sparsePairAccum 唯一公式核结算（与 DecomposeSparsePair 同一套公式）。
type pairIntersectionStats struct {
	VectorPairDecomposition
	sharedMaskU, sharedMaskV []bool
}

// intersectGroupProfiles performs the canonical two-pointer set decomposition
// (u ∩ v, u \ v, v \ u) shared by all five autonoetic questions. 该适配器只做
// 结构遍历与掩码记录，判据与累积全部委托 sparsePairAccum（零分配、无第二套公式）。
func intersectGroupProfiles(profU, profV unitGroupProfile, recordMasks bool) pairIntersectionStats {
	var st pairIntersectionStats
	iu, iv := profU.items, profV.items
	if recordMasks {
		st.sharedMaskU = make([]bool, len(iu))
		st.sharedMaskV = make([]bool, len(iv))
	}
	var acc sparsePairAccum
	i, j := 0, 0
	for i < len(iu) && j < len(iv) {
		gu, gv := iu[i].group, iv[j].group
		if gu < gv {
			acc.addSelf(iu[i].weight, iu[i].isCore, profU.muMass)
			i++
		} else if gu > gv {
			acc.addPeer(iv[j].weight, iv[j].isCore, profV.muMass)
			j++
		} else {
			acc.addShared(iu[i].weight, iv[j].weight)
			if recordMasks {
				st.sharedMaskU[i] = true
				st.sharedMaskV[j] = true
			}
			i++
			j++
		}
	}
	for ; i < len(iu); i++ {
		acc.addSelf(iu[i].weight, iu[i].isCore, profU.muMass)
	}
	for ; j < len(iv); j++ {
		acc.addPeer(iv[j].weight, iv[j].isCore, profV.muMass)
	}
	// unitGroupProfile 不携带 L2 范数：范数传 0，余弦保持 0（该路径按逐簇相对量比较）。
	st.VectorPairDecomposition = acc.finalize(profU.totalMass, profV.totalMass, 0, 0)
	return st
}

// evalBurstCloneRedundancyFromProfiles evaluates the 0-Gate continuous echo-clone
// redundancy (Question 1: Identical u ≡ s) of candidate nb with respect to an
// already selected co-event leader s, guarded by P4 (continuous echo purity) and
// P6 (multi-feature scene support >= 3 shared groups).
func evalBurstCloneRedundancyFromProfiles(profS, profNb unitGroupProfile, aff float64) float64 {
	// Single-term overlap is coincidence and 2-term overlap is a phrase; at least
	// 3 distinct feature groups must co-occur to treat two records as scene clones.
	// [calibrated] minCloneGroups — distinct shared feature groups (count) required
	// before two records can be treated as scene clones; one term is coincidence and
	// two are a phrase.
	const minCloneGroups = 3
	if aff < subFloor || len(profS.items) < minCloneGroups || len(profNb.items) < minCloneGroups || profS.totalMass <= 0 || profNb.totalMass <= 0 {
		return 0
	}
	ix := intersectGroupProfiles(profS, profNb, false)
	sceneFloor := eulerNumber * math.Min(profS.muMass, profNb.muMass)
	if ix.SharedGroups < minCloneGroups || ix.SharedMinNats < sceneFloor || ix.SelfCoverage < transmissionPerHop || ix.PeerCoverage < subFloor {
		return 0
	}
	novelRatio := math.Min(1.0, ix.PeerSalientExcessNats/profNb.totalMass)
	echoPurity := (1.0 - novelRatio) * (1.0 - novelRatio)
	return aff * ix.HarmonicCoverage * ix.PeerCoverage * echoPurity
}

// evalUnrelatedBystanderBorrowFromProfiles evaluates Negative Duality 2-
// (unrelated same-timestamp bystander loan damping): when candidate nb sits in
// the same tight temporal window as a stronger selected unit s (aff >= 1 - 1/e),
// yet both its thematic cosine cos_subj(s, nb) and reverse coverage C_{nb->s}
// fall below 1/e^2 while nb relies on borrowed
// spacetime Hop energy (nbHop > nbDirect/e), its borrowed cross-record inflation
// is continuously damped.
func evalUnrelatedBystanderBorrowFromProfiles(profS, profNb unitGroupProfile, aff, rawCos, sStrength, nbStrength, nbDirect, nbHop float64) float64 {
	if rawCos < 0 {
		rawCos = 0
	}
	if aff < subFloor || rawCos >= transmissionTwoHop || sStrength <= 0 || nbDirect <= 0 || nbHop <= nbDirect*transmissionPerHop || nbStrength >= sStrength*(1.0-transmissionTwoHop) {
		return 0
	}
	ix := intersectGroupProfiles(profS, profNb, false)
	if ix.PeerCoverage >= transmissionTwoHop {
		return 0
	}
	nbTot := nbDirect + nbHop
	if nbTot <= 0 {
		return 0
	}
	return eulerNumber * aff * (1.0 - rawCos/transmissionTwoHop) * (nbHop / nbTot)
}

// computeDirectedLocalExplainWork calculates how much unknown new information
// unit `explainer` reveals in the same-sentence neighborhood of the shared
// anchors `sharedMask` with `explainee`, bounded by the Anchor-Quota
// Hyperbolic Capacity Envelope min(1, Z_local * A_raw / M_comp,0) and gated by
// the explainee's uncompensated salient missing mass exp(-I_{>μ}(explainee \ explainer) / μ).
func computeDirectedLocalExplainWork(sc *rmQueryScratch, st *rmClusterState, explainer unitGroupProfile, sharedMask []bool, anchorRawNats, explaineeSalientMissing, explaineeMu float64, qTerms map[uint64]struct{}) float64 {
	if anchorRawNats <= 0 || len(explainer.items) == 0 {
		return 0
	}
	wide := st != nil && st.hasWidePos()
	var anchorPacked []uint32
	var anchorExact []TermPosition
	if sc != nil {
		anchorPacked = sc.explainAnchorPool[:0]
		anchorExact = sc.explainAnchorExactPool[:0]
	}
	var coreAnchorNats float64
	hasAnyPos := false
	for i, isShared := range sharedMask {
		if len(explainer.items[i].pos) > 0 {
			hasAnyPos = true
		}
		if isShared && explainer.items[i].isCore {
			coreAnchorNats += explainer.items[i].weight
			if len(explainer.items[i].pos) > 0 {
				anchorPacked = append(anchorPacked, explainer.items[i].pos...)
			}
			if wide && len(explainer.items[i].posExact) > 0 {
				anchorExact = append(anchorExact, explainer.items[i].posExact...)
			}
		}
	}
	if sc != nil {
		sc.explainAnchorPool = anchorPacked
		sc.explainAnchorExactPool = anchorExact
	}
	if coreAnchorNats <= 0 {
		return 0
	}
	if coreAnchorNats < anchorRawNats {
		anchorRawNats = coreAnchorNats
	}
	if len(anchorPacked) > 1 {
		sortUint32Asc(anchorPacked)
	}
	if wide && len(anchorExact) > 1 {
		sort.Sort(termPositionSort(anchorExact))
	}
	// Apply conditional entropy gain: reduce rawCompSum by the probability that
	// the unit's focal features are already implied by the Query-Conditional Anchor.
	// We replace simple weight accumulation with the information gain measure:
	//   Gain(u | s, Q) = Sum[weight * kern * (1 - P(focal_in_Q | anchor))]
	//   Where P(focal_in_Q | anchor) is estimated by the fraction of group in Q.
	var rawCompSum float64
	for i, isShared := range sharedMask {
		it := explainer.items[i]
		if isShared || !it.isCore || it.weight <= explaineeMu*transmissionPerHop {
			continue
		}
		// If the feature itself is in the query, its explanatory gain is zero.
		if _, inQ := qTerms[it.group]; inQ {
			continue
		}

		var kern float64
		if wide {
			if len(anchorExact) > 0 && len(it.posExact) > 0 {
				dist := closestExactSlot(it.posExact, anchorExact)
				if dist < 0 {
					continue
				}
				if dist < 1 {
					dist = 1
				}
				kern = math.Exp(-float64(dist-1) * transmissionPerHop)
			} else if !hasAnyPos {
				kern = transmissionPerHop * transmissionPerHop
			} else {
				continue
			}
		} else if len(anchorPacked) > 0 && len(it.pos) > 0 {
			dist := closestPackedSlot(it.pos, anchorPacked)
			if dist < 0 {
				continue
			}
			if dist < 1 {
				dist = 1
			}
			kern = math.Exp(-float64(dist-1) * transmissionPerHop)
		} else if !hasAnyPos {
			kern = transmissionPerHop * transmissionPerHop
		} else {
			continue
		}
		rawCompSum += it.weight * kern
	}
	if rawCompSum <= 0 {
		return 0
	}
	pAnchor := 1.0 - math.Exp(-anchorRawNats)
	quota := localSlotZ * anchorRawNats
	envelope := 1.0
	if rawCompSum > quota {
		envelope = quota / rawCompSum
	}
	missingGate := 1.0
	if explaineeSalientMissing > 0 && explaineeMu > 0 {
		missingGate = math.Exp(-explaineeSalientMissing / explaineeMu)
	}
	return pAnchor * envelope * missingGate * rawCompSum
}

// EvaluateUnitPairRelation computes the exact bilateral relation metrics between
// focal unit u and peer unit v in a single rank-index-blind pass.
func (e *Engine) EvaluateUnitPairRelation(unitIDU, unitIDV string) (UnitRelationPeer, bool) {
	if e == nil || e.Index == nil || e.Index.N == 0 {
		return UnitRelationPeer{}, false
	}
	idx := e.Index
	u, okU := idx.LookupUnit(unitIDU)
	v, okV := idx.LookupUnit(unitIDV)
	if !okU || !okV {
		return UnitRelationPeer{}, false
	}
	st := e.ensureClusterState()
	e.ensureQueryScratch(idx.N, st.clusterN).resetProfilePools()
	highDFThresh := idx.HighDFThreshold()
	profU := e.buildUnitGroupProfile(st, u, highDFThresh)
	profV := e.buildUnitGroupProfile(st, v, highDFThresh)
	return e.evalPairFromProfiles(st, u, profU, v, profV), true
}

func (e *Engine) evalPairFromProfiles(st *rmClusterState, u int32, profU unitGroupProfile, v int32, profV unitGroupProfile) UnitRelationPeer {
	return e.evalPairFromProfilesQ(st, u, profU, v, profV, nil)
}

// evalPairFromProfilesQ is the query-conditioned 3-body variant: when qSet is
// non-nil, directed explanatory work counts only unknown payload terms u \ Q
// (terms already present in the query carry zero marginal explanation for the
// asker and are already paid by the lexical channel), while shared anchors
// Q ∪ (s ∩ u) are kept intact as the grounding bridge.
func (e *Engine) evalPairFromProfilesQ(st *rmClusterState, u int32, profU unitGroupProfile, v int32, profV unitGroupProfile, qSet map[uint64]struct{}) UnitRelationPeer {
	idx := e.Index
	ix := intersectGroupProfiles(profU, profV, true)

	subjCos := st.docSubjectCos(idx, e.queryScratch, u, v)
	tempAff := st.temporalAffinity(idx, u, v)
	if st.hasCitationEdge(u, v) || st.hasCoCitationEdge(u, v) {
		tempAff = math.Max(tempAff, transmissionPerHop)
	} else if st.refTimes[u] <= 0 || st.refTimes[v] <= 0 {
		if int(u) < len(idx.UnitMeta) && int(v) < len(idx.UnitMeta) {
			mu, mv := idx.UnitMeta[u], idx.UnitMeta[v]
			if (mu.DocHash != 0 && mu.DocHash == mv.DocHash) ||
				(mu.BlockHash != 0 && mu.BlockHash == mv.BlockHash) {
				tempAff = math.Max(tempAff, transmissionPerHop)
			}
		}
	}

	selfExplain := computeDirectedLocalExplainWork(e.queryScratch, st, profU, ix.sharedMaskU, ix.SharedMinNats, ix.PeerSalientExcessNats, profV.muMass, qSet)
	peerExplain := computeDirectedLocalExplainWork(e.queryScratch, st, profV, ix.sharedMaskV, ix.SharedMinNats, ix.SelfSalientExcessNats, profU.muMass, qSet)

	return UnitRelationPeer{
		UnitID:              idx.UnitIDs[v],
		Unit:                v,
		SharedNats:          ix.SharedMinNats,
		SelfExcessNats:      ix.SelfExcessNats,
		PeerExcessNats:      ix.PeerExcessNats,
		SelfSalientExcess:   ix.SelfSalientExcessNats,
		PeerSalientExcess:   ix.PeerSalientExcessNats,
		SelfCoverage:        ix.SelfCoverage,
		PeerCoverage:        ix.PeerCoverage,
		SubjectCosine:       subjCos,
		TemporalAffinity:    tempAff,
		SelfExplainWorkNats: selfExplain,
		PeerExplainWorkNats: peerExplain,
	}
}

// evalPairFromProfilesQLocal is the worker-local variant used by the parallel
// intra-event endorsement pass. It uses the caller's scratch for the explanation
// pools and the doc-subject cache, so independent workers never share mutable
// state; the arithmetic is identical to evalPairFromProfilesQ.
func (e *Engine) evalPairFromProfilesQLocal(st *rmClusterState, u int32, profU unitGroupProfile, v int32, profV unitGroupProfile, qSet map[uint64]struct{}, sc *rmQueryScratch) UnitRelationPeer {
	idx := e.Index
	ix := intersectGroupProfiles(profU, profV, true)
	// Exact precondition of intraEventEndorseGain's unconditional gates: a pair
	// that already fails the coverage gates contributes 0 regardless of the
	// directed explanation work, so return before the O(positions) kernels.
	harmCov := harmonicCoverage(ix.SelfCoverage, ix.PeerCoverage)
	if harmCov < transmissionPerHop || harmCov >= identityFloor ||
		ix.SelfCoverage < transmissionPerHop || ix.PeerCoverage < transmissionPerHop {
		return UnitRelationPeer{
			UnitID: idx.UnitIDs[v], Unit: v,
			SelfCoverage: ix.SelfCoverage, PeerCoverage: ix.PeerCoverage,
		}
	}

	subjCos := st.docSubjectCosLocal(idx, sc, u, v)
	if subjCos < transmissionTwoHop {
		return UnitRelationPeer{
			UnitID: idx.UnitIDs[v], Unit: v,
			SelfCoverage: ix.SelfCoverage, PeerCoverage: ix.PeerCoverage,
			SubjectCosine: subjCos,
		}
	}
	tempAff := st.temporalAffinity(idx, u, v)
	if st.hasCitationEdge(u, v) || st.hasCoCitationEdge(u, v) {
		tempAff = math.Max(tempAff, transmissionPerHop)
	} else if st.refTimes[u] <= 0 || st.refTimes[v] <= 0 {
		if int(u) < len(idx.UnitMeta) && int(v) < len(idx.UnitMeta) {
			mu, mv := idx.UnitMeta[u], idx.UnitMeta[v]
			if (mu.DocHash != 0 && mu.DocHash == mv.DocHash) ||
				(mu.BlockHash != 0 && mu.BlockHash == mv.BlockHash) {
				tempAff = math.Max(tempAff, transmissionPerHop)
			}
		}
	}

	selfExplain := computeDirectedLocalExplainWork(sc, st, profU, ix.sharedMaskU, ix.SharedMinNats, ix.PeerSalientExcessNats, profV.muMass, qSet)
	peerExplain := computeDirectedLocalExplainWork(sc, st, profV, ix.sharedMaskV, ix.SharedMinNats, ix.SelfSalientExcessNats, profU.muMass, qSet)

	return UnitRelationPeer{
		UnitID:              idx.UnitIDs[v],
		Unit:                v,
		SharedNats:          ix.SharedMinNats,
		SelfExcessNats:      ix.SelfExcessNats,
		PeerExcessNats:      ix.PeerExcessNats,
		SelfSalientExcess:   ix.SelfSalientExcessNats,
		PeerSalientExcess:   ix.PeerSalientExcessNats,
		SelfCoverage:        ix.SelfCoverage,
		PeerCoverage:        ix.PeerCoverage,
		SubjectCosine:       subjCos,
		TemporalAffinity:    tempAff,
		SelfExplainWorkNats: selfExplain,
		PeerExplainWorkNats: peerExplain,
	}
}

// InspectUnitRelations answers the five fundamental autonoetic questions for any
// unit u in the system using a single unified Nats energy framework (100%
// rank-index-blind, zero magic numbers, O(nnz) candidate discovery).
func (e *Engine) InspectUnitRelations(unitID string, topK int) (UnitRelationProfile, bool) {
	if e == nil || e.Index == nil || e.Index.N == 0 {
		return UnitRelationProfile{}, false
	}
	idx := e.Index
	u, ok := idx.LookupUnit(unitID)
	if !ok {
		return UnitRelationProfile{}, false
	}
	if topK <= 0 {
		topK = 10
	}
	st := e.ensureClusterState()
	e.ensureQueryScratch(idx.N, st.clusterN).resetProfilePools()
	highDFThresh := idx.HighDFThreshold()
	profU := e.buildUnitGroupProfile(st, u, highDFThresh)
	out := UnitRelationProfile{
		UnitID:        unitID,
		Unit:          u,
		TotalMassNats: profU.totalMass,
		MeanMassNats:  profU.muMass,
	}
	if len(profU.items) == 0 {
		return out, true
	}

	candSet := make(map[int32]struct{}, 64)
	for _, entry := range st.docEntriesOf(u) {
		if idx.TermDF(entry.id) > highDFThresh && entry.flags&rmFlagCompanion != 0 {
			continue
		}
		for _, p := range st.termPostings(entry.id) {
			if p.unit != u {
				candSet[p.unit] = struct{}{}
			}
		}
	}
	st.visitNeighbors(u, func(nb int32) bool {
		if nb != u {
			candSet[nb] = struct{}{}
		}
		return true
	})
	st.forEachProvenancePeer(idx, u, func(nb int32) {
		candSet[nb] = struct{}{}
	})

	cands := make([]int32, 0, len(candSet))
	for v := range candSet {
		cands = append(cands, v)
	}
	sortInt32Asc(cands)

	for _, v := range cands {
		profV := e.buildUnitGroupProfile(st, v, highDFThresh)
		if len(profV.items) == 0 {
			continue
		}
		peer := e.evalPairFromProfiles(st, u, profU, v, profV)
		harmCov := harmonicCoverage(peer.SelfCoverage, peer.PeerCoverage)

		// Conditioned Binomial Decomposition over P_couple(u,v) * (C_u + R_u)(C_v + R_v):
		//   C_u = SelfCoverage ∈ [0,1], R_u = 1 - C_u (u's residual fraction)
		//   C_v = PeerCoverage ∈ [0,1], R_v = 1 - C_v (v's residual fraction)
		//   1 ≡ C_u*C_v (Echo/Sim) + C_v*R_u (IExplain u▷v) + C_u*R_v (ExplainsMe v▷u) + R_u*R_v (Complementary u⊕v)
		cU, cV := peer.SelfCoverage, peer.PeerCoverage
		rU, rV := 1.0-cU, 1.0-cV

		// 1. 谁跟我一样 (Identical / Echo Clone: C_u*C_v dominant mode)
		if harmCov >= identityFloor {
			cp := peer
			cp.ScoreNats = peer.SharedNats * harmCov
			out.Identical = append(out.Identical, cp)
		}

		// 2. 谁跟我相似 (Similar / Symmetric Peer Resonance)
		maxDirCov := math.Max(cU, cV)
		if peer.SharedNats >= profU.muMass*transmissionPerHop && peer.SubjectCosine > 0 && harmCov < identityFloor && maxDirCov < identityFloor {
			cp := peer
			cp.ScoreNats = peer.SharedNats * math.Sqrt(peer.SubjectCosine) * (1.0 - math.Abs(cU-cV))
			out.Similar = append(out.Similar, cp)
		}

		// 3. 谁跟我互补 (Complementary / Orthogonal Puzzle u ⊕ v: R_u*R_v mode)
		// An explicit citation or co-citation edge (DocMeta.CitedIDs) is a declared
		// shared anchor carrying one characteristic energy quantum μ of grounding:
		// a noetic consolidation citing its evidence (or two evidence units jointly
		// cited by one consolidation) couples through the declarative bond even with
		// zero lexical overlap.
		citeBond := st.hasCitationEdge(u, v) || st.hasCoCitationEdge(u, v)
		if (peer.SharedNats > 0 || citeBond) && peer.TemporalAffinity > 0 &&
			peer.SelfExcessNats > profU.muMass && peer.PeerExcessNats > profV.muMass && harmCov < identityFloor {
			muScale := math.Max(1.0, math.Min(profU.muMass, profV.muMass))
			sharedEff := peer.SharedNats
			if citeBond && sharedEff < muScale {
				sharedEff = muScale
			}
			anchorBridge := 1.0 - math.Exp(-sharedEff/muScale)
			coupling := anchorBridge * peer.TemporalAffinity * math.Max(transmissionPerHop, math.Sqrt(peer.SubjectCosine))
			if coupling > 0 {
				harmExcess := 2.0 * peer.SelfExcessNats * peer.PeerExcessNats / (peer.SelfExcessNats + peer.PeerExcessNats)
				cp := peer
				cp.ScoreNats = coupling * harmExcess * rU * rV
				if cp.ScoreNats > 0 {
					out.Complementary = append(out.Complementary, cp)
				}
			}
		}

		// 4. 我能解释谁 (IExplain u ▷ v: net downward explanatory work E(u▷v) > E(v▷u), 0-Gate continuous)
		if peer.SelfExplainWorkNats > peer.PeerExplainWorkNats {
			cp := peer
			missingGate := 1.0
			if peer.PeerSalientExcess > 0 && profV.muMass > 0 {
				missingGate = math.Exp(-peer.PeerSalientExcess / profV.muMass)
			}
			concU := 1.0
			if profU.totalMass > 0 {
				concU = math.Min(1.0, (peer.SharedNats+peer.SelfExplainWorkNats)/profU.totalMass)
			}
			cp.ScoreNats = cV * concU * (peer.SharedNats*missingGate + peer.SelfExplainWorkNats)
			out.IExplain = append(out.IExplain, cp)
		}

		// 5. 谁能解释我 (ExplainsMe v ▷ u: net upward explanatory work E(v▷u) > E(u▷v), 0-Gate continuous)
		if peer.PeerExplainWorkNats > peer.SelfExplainWorkNats {
			cp := peer
			missingGate := 1.0
			if peer.SelfSalientExcess > 0 && profU.muMass > 0 {
				missingGate = math.Exp(-peer.SelfSalientExcess / profU.muMass)
			}
			concV := 1.0
			if profV.totalMass > 0 {
				concV = math.Min(1.0, (peer.SharedNats+peer.PeerExplainWorkNats)/profV.totalMass)
			}
			cp.ScoreNats = cU * concV * (peer.SharedNats*missingGate + peer.PeerExplainWorkNats)
			out.ExplainsMe = append(out.ExplainsMe, cp)
		}
	}

	sortPeers := func(list []UnitRelationPeer) []UnitRelationPeer {
		sort.Slice(list, func(i, j int) bool {
			if list[i].ScoreNats != list[j].ScoreNats {
				return list[i].ScoreNats > list[j].ScoreNats
			}
			return list[i].UnitID < list[j].UnitID
		})
		if len(list) > topK {
			list = list[:topK]
		}
		return list
	}

	out.Identical = sortPeers(out.Identical)
	out.Similar = sortPeers(out.Similar)
	out.Complementary = sortPeers(out.Complementary)
	out.IExplain = sortPeers(out.IExplain)
	out.ExplainsMe = sortPeers(out.ExplainsMe)
	return out, true
}

// ExplanationChainStep represents one hop along a monotonic directed
// explanation or declarative citation chain u_0 -> u_1 -> ... -> u_L inside a
// chain-structured cognitive cluster.
type ExplanationChainStep struct {
	UnitID           string  `json:"unit_id"`            // Target unit identifier at this hop
	Unit             int32   `json:"-"`                  // Internal unit index in [0, Index.N)
	Hop              int     `json:"hop"`                // 1-based hop depth from the focal seed u_0
	StepWorkNats     float64 `json:"step_work_nats"`     // Directed explanatory work at this step (Nats)
	ChainNoveltyNats float64 `json:"chain_novelty_nats"` // Salient core mass of u_k unseen in the entire prefix chain u_0..u_{k-1} (Nats)
	StepTransmission float64 `json:"step_transmission"`  // Single-hop coupling transmission η_k in (0, 1]
	CumulativeTrans  float64 `json:"cumulative_trans"`   // Multiplicative DPI transmission Π_{j=1..k} η_j in (0, 1]
	ChainScoreNats   float64 `json:"chain_score_nats"`   // DPI-attenuated step energy contribution (Nats)
	DeclarativeCite  bool    `json:"declarative_cite"`   // True when linked by an explicit citation or co-citation edge
}

// TraceExplanationChain extracts a monotonic directed explanation chain
// u_0 -> u_1 -> ... -> u_L starting from unitID (upward ExplainsMe chain
// u_0 ◁ u_1 ◁ ... ◁ u_L when upward=true, or downward IExplain chain
// u_0 ▷ u_1 ▷ ... ▷ u_L when upward=false).
//
// Each hop k -> k+1 must satisfy four parameter-free conservation laws:
//  1. Anti-Echo Novelty: harmonic coverage F_1(u_k, u_{k+1}) < 1 - e^-2;
//  2. Directional Dominance: net explanatory work in the chosen direction
//     strictly exceeds the reverse direction (or is bridged by a declarative
//     citation/co-citation edge carrying salient excess);
//  3. Chain-Cumulative Salient Growth: u_{k+1} must contribute salient core
//     family mass I_{>μ}(u_{k+1} \ U_{0..k}) > μ(u_k) unseen across the entire
//     prefix chain U_{0..k}, preventing 2-hop A -> B -> A' oscillation;
//  4. DPI Multiplicative Attenuation: cumulative transmission Π_{j=1..k} η_j >= e^-k.
func (e *Engine) TraceExplanationChain(unitID string, upward bool, maxHops int) ([]ExplanationChainStep, bool) {
	return e.TraceExplanationChainWithQuery(unitID, upward, maxHops, nil)
}

// TraceExplanationChainWithQuery is the query-conditioned 3-body variant of
// TraceExplanationChain: when qTerms is non-empty, both the directed
// explanatory work W(u_k <-> u_{k+1} | Q) and the chain-cumulative salient
// novelty I_{>μ}(u_{k+1} \ (U_{0..k} ∪ Q)) discount terms already present in
// the query ground set Q, so the extracted chain prioritizes steps that reveal
// unknown explanatory payload beyond what the asker already supplied.
func (e *Engine) TraceExplanationChainWithQuery(unitID string, upward bool, maxHops int, qTerms map[uint64]Term) ([]ExplanationChainStep, bool) {
	if e == nil || e.Index == nil || e.Index.N == 0 {
		return nil, false
	}
	idx := e.Index
	u0, ok := idx.LookupUnit(unitID)
	if !ok {
		return nil, false
	}
	if maxHops <= 0 {
		// Engineering recursion cap when the caller omits an explicit hop limit;
		// endogenous stopping inside the loop terminates earlier whenever cumulative
		// transmission or chain-cumulative novelty is exhausted.
		const defaultMaxExplanationChainHops = 3
		maxHops = defaultMaxExplanationChainHops
	}
	st := e.ensureClusterState()
	e.ensureQueryScratch(idx.N, st.clusterN).resetProfilePools()
	highDFThresh := idx.HighDFThreshold()
	profCur := e.buildUnitGroupProfile(st, u0, highDFThresh)
	if len(profCur.items) == 0 {
		return nil, true
	}

	qSet := buildQueryGroundSet(qTerms)
	visited := map[int32]bool{u0: true}
	seenChainGroups := make(map[uint64]bool, len(profCur.items)*2+len(qSet))
	for _, it := range profCur.items {
		if it.isCore {
			seenChainGroups[it.group] = true
		}
	}

	curU := u0
	cumTrans := 1.0
	var chain []ExplanationChainStep

	for hop := 1; hop <= maxHops; hop++ {
		candSet := make(map[int32]struct{}, 64)
		for _, entry := range st.docEntriesOf(curU) {
			if idx.TermDF(entry.id) > highDFThresh && entry.flags&rmFlagCompanion != 0 {
				continue
			}
			for _, p := range st.termPostings(entry.id) {
				if !visited[p.unit] {
					candSet[p.unit] = struct{}{}
				}
			}
		}
		st.visitNeighbors(curU, func(nb int32) bool {
			if !visited[nb] {
				candSet[nb] = struct{}{}
			}
			return true
		})
		st.forEachProvenancePeer(idx, curU, func(nb int32) {
			if !visited[nb] {
				candSet[nb] = struct{}{}
			}
		})
		if len(candSet) == 0 {
			break
		}

		cands := make([]int32, 0, len(candSet))
		for v := range candSet {
			cands = append(cands, v)
		}
		sortInt32Asc(cands)

		dpiFloor := math.Exp(-float64(hop))
		var bestStep ExplanationChainStep
		var bestProf unitGroupProfile
		found := false

		for _, v := range cands {
			profV := e.buildUnitGroupProfile(st, v, highDFThresh)
			if len(profV.items) == 0 {
				continue
			}
			peer := e.evalPairFromProfilesQ(st, curU, profCur, v, profV, qSet)
			var harmCov float64
			if peer.SelfCoverage+peer.PeerCoverage > 0 {
				harmCov = 2.0 * peer.SelfCoverage * peer.PeerCoverage / (peer.SelfCoverage + peer.PeerCoverage)
			}
			if harmCov >= identityFloor {
				continue
			}

			citeBond := st.hasCitationEdge(curU, v) || st.hasCoCitationEdge(curU, v)
			var stepWork float64
			if upward {
				if peer.PeerExplainWorkNats > peer.SelfExplainWorkNats {
					stepWork = peer.PeerExplainWorkNats - peer.SelfExplainWorkNats
				} else if citeBond && peer.PeerSalientExcess > profCur.muMass {
					stepWork = peer.PeerSalientExcess * transmissionPerHop
				}
			} else {
				if peer.SelfExplainWorkNats > peer.PeerExplainWorkNats {
					stepWork = peer.SelfExplainWorkNats - peer.PeerExplainWorkNats
				} else if citeBond && peer.SelfSalientExcess > profV.muMass {
					stepWork = peer.SelfSalientExcess * transmissionPerHop
				}
			}
			if stepWork <= 0 {
				continue
			}

			var chainNovelty float64
			for _, it := range profV.items {
				if !it.isCore || seenChainGroups[it.group] || it.weight <= profV.muMass {
					continue
				}
				if _, inQ := qSet[it.group]; inQ {
					continue
				}
				chainNovelty += it.weight
			}
			if chainNovelty <= profCur.muMass {
				continue
			}

			muScale := math.Max(1.0, math.Min(profCur.muMass, profV.muMass))
			sharedEff := peer.SharedNats
			if citeBond && sharedEff < muScale {
				sharedEff = muScale
			}
			if sharedEff <= 0 {
				continue
			}
			anchorBridge := 1.0 - math.Exp(-sharedEff/muScale)
			affEff := math.Max(transmissionPerHop, peer.TemporalAffinity)
			cosEff := math.Sqrt(math.Max(0, peer.SubjectCosine))
			stepTrans := anchorBridge * affEff * cosEff
			nextCumTrans := cumTrans * stepTrans
			if nextCumTrans < dpiFloor {
				continue
			}

			score := nextCumTrans * (stepWork + transmissionPerHop*chainNovelty)
			if !found || score > bestStep.ChainScoreNats || (score == bestStep.ChainScoreNats && idx.UnitIDs[v] < bestStep.UnitID) {
				found = true
				bestProf = profV
				bestStep = ExplanationChainStep{
					UnitID:           idx.UnitIDs[v],
					Unit:             v,
					Hop:              hop,
					StepWorkNats:     stepWork,
					ChainNoveltyNats: chainNovelty,
					StepTransmission: stepTrans,
					CumulativeTrans:  nextCumTrans,
					ChainScoreNats:   score,
					DeclarativeCite:  citeBond,
				}
			}
		}
		if !found {
			break
		}
		chain = append(chain, bestStep)
		visited[bestStep.Unit] = true
		for _, it := range bestProf.items {
			if it.isCore {
				seenChainGroups[it.group] = true
			}
		}
		curU = bestStep.Unit
		profCur = bestProf
		cumTrans = bestStep.CumulativeTrans
	}
	return chain, true
}

// intraEventEndorseGain evaluates the rank-index-blind directional explanation
// endorsement from a leading co-event seed s to a candidate u within the primary
// event cluster, shared by both Phase-2.5 cluster endorsement and marginal filtering.
func intraEventEndorseGain(rel UnitRelationPeer, aff, harmCov, eventRatio, satDecay, gap, microOcc, seedBurstOcc float64) float64 {
	if gap <= 0 || aff < subFloor || harmCov < transmissionPerHop || harmCov >= identityFloor ||
		rel.SelfCoverage < transmissionPerHop || rel.PeerCoverage < transmissionPerHop || rel.SubjectCosine < transmissionTwoHop ||
		rel.PeerExplainWorkNats <= math.Sqrt(eulerNumber)*rel.SelfExplainWorkNats || rel.PeerExplainWorkNats <= transmissionPerHop {
		return 0
	}
	if aff >= identityFloor && (microOcc >= 1.0 || seedBurstOcc >= 1.0) {
		if rel.PeerExplainWorkNats <= eulerNumber || rel.PeerExplainWorkNats <= eulerNumber*rel.SelfExplainWorkNats {
			return 0
		}
	}
	netExp := rel.PeerExplainWorkNats - rel.SelfExplainWorkNats
	expAdv := netExp / (rel.PeerExplainWorkNats + rel.SelfExplainWorkNats + 1.0)
	trans := satDecay * eventRatio * aff * harmCov * math.Sqrt(rel.SubjectCosine) * (transmissionPerHop + (1.0-transmissionPerHop)*expAdv)
	if trans > subFloor {
		trans = subFloor
	}
	gain := gap * trans
	if dirGain := satDecay * aff * harmCov * math.Sqrt(rel.SubjectCosine) * netExp; dirGain > gain {
		gain = dirGain
	}
	return gain
}

type scoredEventUnit struct {
	unit     int32
	energy   float64
	microOcc float64
	prof     unitGroupProfile
}

func lessScoredEventUnit(a, b scoredEventUnit) bool {
	if a.energy != b.energy {
		return a.energy > b.energy
	}
	return a.unit < b.unit
}

func sortScoredEventUnits(s []scoredEventUnit) {
	if len(s) <= 32 {
		for i := 1; i < len(s); i++ {
			v := s[i]
			j := i - 1
			for j >= 0 && lessScoredEventUnit(v, s[j]) {
				s[j+1] = s[j]
				j--
			}
			s[j+1] = v
		}
		return
	}
	sort.Sort(scoredEventUnitSort(s))
}

// applyIntraEventExplanatoryEndorsement executes the two-pass event-cluster
// endorsement before Phase-4 unfolding:
//   - Pass 1 (Primary Event Cluster Identification): identifies the peak
//     query-explaining seed u* and its co-event seed neighborhood in corefByEpisode;
//   - Pass 2 (Intra-Event Directional Explanation Endorsement): within that
//     primary event cluster, a leading seed s endorses a co-event member u that
//     performs net upward explanatory work W(u ▷ s) > √e * W(s ▷ u).
//
// endorsePairEval evaluates one event-unit index with worker-local scratch.
type endorsePairEval func(i int, local *rmQueryScratch) float64

// endorsePairDispatch optionally parallelizes the independent event-unit
// evaluations. It is installed by the adapter build; the portable core keeps a
// serial fallback so the computational subset remains adapter-free.
var endorsePairDispatch func(n int, eval endorsePairEval) []float64

// queryHasSpecificTerm reports whether the query carries at least one term
// below the specific-anchor cut N/e³ (one e-fold below the N/e² posting-list
// cut). The intra-event endorsement pass amplifies a specific event cluster; a
// query made only of terms at or above the anchor cut has no term that can
// anchor such a cluster, so the pass is not applicable. The same e^-3 fraction
// already gates specific proper-noun anchors.
func queryHasSpecificTerm(idx *Index, qTerms map[uint64]Term, highDFThresh int) bool {
	if idx == nil || len(qTerms) == 0 {
		return false
	}
	anchorDFMax := int(float64(highDFThresh) / eulerNumber)
	if anchorDFMax < 1 {
		anchorDFMax = 1
	}
	for h := range qTerms {
		df := idx.TermDF(h)
		if df > 0 && df <= anchorDFMax {
			return true
		}
	}
	return false
}

// applyIntraEventExplanatoryEndorsement amplifies a specific event cluster
// around the strongest candidate. A query with no term below the specific-anchor
// cut has no event anchor, so the pass is skipped structurally.
func (e *Engine) applyIntraEventExplanatoryEndorsement(idx *Index, st *rmClusterState, sc *rmQueryScratch, candidates []rmCandidate, unitStructGate []float64, corefByEpisode map[int32][]int32, groups map[uint64]float64, total float64, highDFThresh int, qTerms map[uint64]Term) {
	if e == nil || idx == nil || st == nil || sc == nil || len(sc.coreUnits) < 2 || total <= 0 || !queryHasSpecificTerm(idx, qTerms, highDFThresh) {
		return
	}
	evalStage2E := func(u int32) float64 {
		c := &candidates[u]
		base, _, _ := c.stage2Bounds(unitStructGate, u, groups, total)
		if !c.structureSet {
			c.structure = st.computeStructuredIncrement(idx, sc, c, groups, total, gateAt(unitStructGate, u))
			c.structureSet = true
		}
		return base + c.structure
	}

	peakUnit := int32(-1)
	var maxE float64
	for _, u := range sc.coreUnits {
		c := &candidates[u]
		if base, structBound, _ := c.stage2Bounds(unitStructGate, u, groups, total); base+structBound < maxE {
			continue
		}
		if eu := evalStage2E(u); eu > maxE || (eu == maxE && (peakUnit < 0 || u < peakUnit)) {
			maxE = eu
			peakUnit = u
		}
	}
	if peakUnit < 0 || maxE <= 0 {
		return
	}

	seedFloor := subFloor * maxE
	// A non-trivial multi-record event cluster requires the seed plus at least 2 peers (>= 3 units total).
	const (
		minClusterPeers = 2
		burstFreeQuota  = 3.0
	)
	activeSet := sc.endorseActiveSet
	if activeSet == nil {
		activeSet = make(map[int32]bool, 32)
		sc.endorseActiveSet = activeSet
	} else {
		clear(activeSet)
	}
	// Primary event cluster: the continuous affinity gate aff >= 1/e is the
	// real selector (gap <= meanGap up to the ordinal penalty). The former
	// peakDay±1 calendar pre-filter was a dead guard whenever meanGap < 1d
	// and a phase-dependent cut in sparse corpora, so enumeration runs over
	// the full timed-core population instead of a day grid. An untimed peak
	// has zero affinity to everyone and simply cannot recruit or endorse;
	// the burst-representative pass below still works on timed clusters.
	activeSet[peakUnit] = true
	for _, units := range corefByEpisode {
		for _, u := range units {
			if u == peakUnit || st.temporalAffinity(idx, peakUnit, u) >= transmissionPerHop {
				activeSet[u] = true
			}
		}
	}
	for _, units := range corefByEpisode {
		if len(units) <= minClusterPeers {
			continue
		}
		var bestDayU int32 = -1
		var bestDayE float64
		for _, u := range units {
			c := &candidates[u]
			if base, structBound, _ := c.stage2Bounds(unitStructGate, u, groups, total); base+structBound < seedFloor {
				continue
			}
			if eu := evalStage2E(u); eu >= seedFloor && eu > bestDayE {
				bestDayE = eu
				bestDayU = u
			}
		}
		if bestDayU < 0 {
			continue
		}
		coCnt := 0
		for _, u := range units {
			if u != bestDayU && st.temporalAffinity(idx, bestDayU, u) >= identityFloor {
				coCnt++
			}
		}
		if coCnt >= minClusterPeers {
			for _, u := range units {
				if u == bestDayU || st.temporalAffinity(idx, bestDayU, u) >= identityFloor {
					activeSet[u] = true
				}
			}
		}
	}
	eventUnits := sc.endorseEventUnits[:0]
	for u := range activeSet {
		if eu := evalStage2E(u); eu > 0 {
			eventUnits = append(eventUnits, scoredEventUnit{unit: u, energy: eu})
		}
	}
	sc.endorseEventUnits = eventUnits
	if len(eventUnits) < 2 {
		return
	}
	sortScoredEventUnits(eventUnits)
	for i := range eventUnits {
		u := eventUnits[i].unit
		eventUnits[i].prof = st.buildUnitGroupProfileWithPos(idx, sc, u, highDFThresh, true)
		for k := 0; k < i; k++ {
			if eventUnits[k].energy > eventUnits[i].energy {
				if aff := st.temporalAffinity(idx, eventUnits[k].unit, u); aff >= identityFloor {
					eventUnits[i].microOcc += aff
				}
			}
		}
	}

	// Build the query ground-set once: directed explanatory work inside the
	// endorsement pass is query-conditioned (3-body), counting only unknown
	// payload terms u \ Q as explanation.
	qSet := buildQueryGroundSetInto(sc, qTerms)
	eval := func(i int, local *rmQueryScratch) float64 {
		u := eventUnits[i].unit
		profU := eventUnits[i].prof
		if len(profU.items) == 0 {
			return 0
		}
		microOcc := eventUnits[i].microOcc
		satDecay := math.Exp(-math.Max(0, microOcc-burstFreeQuota) * eulerNumber)
		var best float64
		for k := 0; k < i; k++ {
			if eventUnits[k].energy <= eventUnits[i].energy || len(eventUnits[k].prof.items) == 0 {
				continue
			}
			s := eventUnits[k].unit
			effSeedE := eventUnits[k].energy
			if candidates[s].coreLex >= candidates[peakUnit].coreLex*transmissionPerHop {
				if coupledE := st.temporalAffinity(idx, peakUnit, s) * maxE; coupledE > effSeedE {
					effSeedE = coupledE
				}
			}
			if effSeedE < seedFloor {
				continue
			}
			aff := st.temporalAffinity(idx, s, u)
			if aff < subFloor {
				continue
			}
			rel := e.evalPairFromProfilesQLocal(st, s, eventUnits[k].prof, u, profU, qSet, local)
			harmCov := harmonicCoverage(rel.SelfCoverage, rel.PeerCoverage)
			if gain := intraEventEndorseGain(rel, aff, harmCov, eventUnits[k].energy/maxE, satDecay, eventUnits[k].energy-eventUnits[i].energy, microOcc, eventUnits[k].microOcc); gain > best {
				best = gain
			}
		}
		return best
	}
	var bestByI []float64
	if endorsePairDispatch != nil {
		bestByI = endorsePairDispatch(len(eventUnits), eval)
	} else {
		bestByI = make([]float64, len(eventUnits))
		local := &rmQueryScratch{}
		for i := 1; i < len(eventUnits); i++ {
			bestByI[i] = eval(i, local)
		}
	}
	for i := 1; i < len(eventUnits); i++ {
		if bestByI[i] > 0 {
			candidates[eventUnits[i].unit].stCorrob += bestByI[i]
		}
	}
}

// buildQueryGroundSet folds the query term table into the known-information
// ground set used by query-conditioned explanation: both the raw term hash and
// its family group are considered already known to the asker.
func buildQueryGroundSet(qTerms map[uint64]Term) map[uint64]struct{} {
	return buildQueryGroundSetInto(nil, qTerms)
}

func buildQueryGroundSetInto(sc *rmQueryScratch, qTerms map[uint64]Term) map[uint64]struct{} {
	if len(qTerms) == 0 {
		return nil
	}
	var qSet map[uint64]struct{}
	if sc != nil {
		qSet = sc.endorseQSet
		if qSet == nil {
			qSet = make(map[uint64]struct{}, len(qTerms)*2)
			sc.endorseQSet = qSet
		} else {
			clear(qSet)
		}
	} else {
		qSet = make(map[uint64]struct{}, len(qTerms)*2)
	}
	for h, qa := range qTerms {
		qSet[h] = struct{}{}
		if qa.Group != 0 {
			qSet[qa.Group] = struct{}{}
		}
	}
	return qSet
}

// ApplyUnitRelationMarginalFilter applies the rank-index-blind co-event clone
// redundancy damping derived from Question 1 (Identical: u ≡ s) to a candidate
// hit list, protected by Falsification Guard P4 (0-Gate Continuous Echo Purity)
// and bidirectional explanation compensation.
func (e *Engine) ApplyUnitRelationMarginalFilter(hits []Hit, qTerms map[uint64]Term) []Hit {
	if e == nil || e.Index == nil || len(hits) <= 1 {
		return hits
	}
	idx := e.Index
	st := e.ensureClusterState()
	e.ensureQueryScratch(idx.N, st.clusterN).resetProfilePools()
	highDFThresh := idx.HighDFThreshold()
	units := make([]int32, len(hits))
	profiles := make([]unitGroupProfile, len(hits))
	valid := make([]bool, len(hits))
	microOcc := make([]float64, len(hits))
	for i, h := range hits {
		if u, ok := idx.LookupUnit(h.UnitID); ok {
			units[i] = u
			profiles[i] = e.buildUnitGroupProfile(st, u, highDFThresh)
			valid[i] = len(profiles[i].items) > 0
		}
	}

	var maxE float64
	peakIdx := 0
	for i, h := range hits {
		if h.Energy > maxE {
			maxE = h.Energy
			peakIdx = i
		}
		if !valid[i] {
			continue
		}
		for j := range hits {
			if i != j && valid[j] && hits[j].Energy > h.Energy {
				if aff := st.temporalAffinity(idx, units[j], units[i]); aff >= identityFloor {
					microOcc[i] += aff
				}
			}
		}
	}
	out := make([]Hit, len(hits))
	copy(out, hits)
	if maxE <= 0 {
		return out
	}
	seedFloor := subFloor * maxE
	qSet := buildQueryGroundSet(qTerms)
	for i := range out {
		if !valid[i] {
			continue
		}
		u := units[i]
		profU := profiles[i]
		const burstFreeQuota = 3.0
		satDecay := math.Exp(-math.Max(0, microOcc[i]-burstFreeQuota) * eulerNumber)
		var maxClone, bestExplainGain float64
		for j := range hits {
			if i == j || !valid[j] || hits[j].Energy <= out[i].Energy {
				continue
			}
			s := units[j]
			aff := st.temporalAffinity(idx, s, u)
			if aff <= 0 {
				continue
			}
			rel := e.evalPairFromProfilesQ(st, s, profiles[j], u, profU, qSet)
			harmCov := harmonicCoverage(rel.SelfCoverage, rel.PeerCoverage)
			if (hits[i].Hop1 == 0 && hits[i].Hop2 == 0) || (harmCov >= identityFloor && rel.PeerSalientExcess == 0) {
				if sc := evalBurstCloneRedundancyFromProfiles(profiles[j], profU, aff); sc > maxClone {
					maxClone = sc
				}
			}
			if bd := evalUnrelatedBystanderBorrowFromProfiles(profiles[j], profU, aff, rel.SubjectCosine, hits[j].Direct, hits[i].Direct, hits[i].Direct, hits[i].Hop1+hits[i].Hop2); bd > maxClone {
				maxClone = bd
			}
			if (j == peakIdx || (valid[peakIdx] && st.temporalAffinity(idx, units[peakIdx], s) >= transmissionPerHop)) && hits[j].Energy >= seedFloor {
				if gain := intraEventEndorseGain(rel, aff, harmCov, hits[j].Energy/maxE, satDecay, hits[j].Energy-out[i].Energy, microOcc[i], microOcc[j]); gain > bestExplainGain {
					bestExplainGain = gain
				}
			}
		}
		if maxClone > 0 {
			scale := 1.0 - transmissionPerHop*transmissionPerHop*maxClone
			out[i].Energy *= scale
			out[i].Direct *= scale
			out[i].Hop1 *= scale
			out[i].Hop2 *= scale
		}
		if bestExplainGain > 0 {
			out[i].Energy += bestExplainGain
			out[i].Hop2 += bestExplainGain
		}
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Energy != out[b].Energy {
			return out[a].Energy > out[b].Energy
		}
		return out[a].UnitID < out[b].UnitID
	})
	return out
}
