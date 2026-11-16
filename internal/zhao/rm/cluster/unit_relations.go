// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cluster

import (
	"math"
	"sort"
)

// localSlotZ is the closed-form single-sided geometric series sum of the
// intra-sentence Euler slot kernel exp(-(d-1)/e) over d >= 1:
// Z_local = 1 / (1 - exp(-1/e)) ≈ 3.24887015.
var localSlotZ = 1.0 / (1.0 - math.Exp(-invE))

// disableMarginalFilter is the ablation kill-switch for the .5 marginal
// filter; declared here so the portable core typechecks standalone, while the
// RB_DISABLE_MARGINAL_FILTER env hookup lives in marginal_filter_config.go.
var disableMarginalFilter = false

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
// a focal document u in the corpus manifold:
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
	group  uint64
	weight float64
	isCore bool
	pos    []uint32
}

type unitGroupProfile struct {
	items     []unitGroupItem
	totalMass float64
	coreMass  float64
	muMass    float64
}

func (e *Engine) buildUnitGroupProfile(st *rmClusterState, u int32, highDFThresh int) unitGroupProfile {
	return st.buildUnitGroupProfileWithPos(e.Index, u, highDFThresh, true)
}

func (st *rmClusterState) buildUnitGroupProfileWithPos(idx *Index, u int32, highDFThresh int, withPos bool) unitGroupProfile {
	entries := st.docEntriesOf(u)
	if len(entries) == 0 {
		return unitGroupProfile{}
	}
	const rmRigidMask uint8 = (1 << 0) | (1 << 1) | (1 << 2) | (1 << 5) | (1 << 6) | (1 << 7)
	items := make([]unitGroupItem, 0, len(entries))

	for _, entry := range entries {
		w, ok := st.effWeightOf(entry.id)
		if !ok || w <= 0 {
			w, _ = idx.TermMass(entry.id)
		}
		if math.IsNaN(w) || math.IsInf(w, 0) || w <= 0 {
			continue
		}
		df := idx.TermDF(entry.id)
		g := entry.group
		if g == 0 {
			g = entry.id
		}
		core := df <= highDFThresh || (entry.flags&rmRigidMask != 0)
		if entry.id != g && df > highDFThresh {
			if entry.flags&rmFlagCompanion != 0 {
				continue
			}
			g = entry.id
		}
		var cp []uint32
		if withPos {
			if pos := st.entryPositionsOf(entry); len(pos) > 0 {
				cp = append([]uint32(nil), pos...)
			}
		}
		items = append(items, unitGroupItem{group: g, weight: w, isCore: core, pos: cp})
	}
	if len(items) == 0 {
		return unitGroupProfile{}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].group < items[j].group })

	// In-place run-length folding by ascending group ID (zero map or index slice allocation).
	outIdx := 0
	var tot, coreTot, sqSum float64
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
				sort.Slice(cur.pos, func(a, b int) bool { return cur.pos[a] < cur.pos[b] })
			}
			i++
		}
		items[outIdx] = cur
		outIdx++
		tot += cur.weight
		if cur.isCore {
			coreTot += cur.weight
		}
		sqSum += cur.weight * cur.weight
	}
	items = items[:outIdx]
	var mu float64
	if tot > 0 {
		mu = sqSum / tot
	}
	return unitGroupProfile{items: items, totalMass: tot, coreMass: coreTot, muMass: mu}
}

type pairIntersectionStats struct {
	sharedU, sharedV, sharedMin  float64
	selfExcess, peerExcess       float64
	selfSalientEx, peerSalientEx float64
	selfCov, peerCov, harmCov    float64
	sharedGroups                 int
	sharedMaskU, sharedMaskV     []bool
}

// intersectGroupProfiles performs the canonical two-pointer set decomposition
// (u ∩ v, u \ v, v \ u) shared by all five autonoetic questions.
func intersectGroupProfiles(profU, profV unitGroupProfile, recordMasks bool) pairIntersectionStats {
	var st pairIntersectionStats
	iu, iv := profU.items, profV.items
	if recordMasks {
		st.sharedMaskU = make([]bool, len(iu))
		st.sharedMaskV = make([]bool, len(iv))
	}
	i, j := 0, 0
	for i < len(iu) && j < len(iv) {
		gu, gv := iu[i].group, iv[j].group
		if gu < gv {
			w := iu[i].weight
			st.selfExcess += w
			if iu[i].isCore && w > profU.muMass {
				st.selfSalientEx += w - profU.muMass
			}
			i++
		} else if gu > gv {
			w := iv[j].weight
			st.peerExcess += w
			if iv[j].isCore && w > profV.muMass {
				st.peerSalientEx += w - profV.muMass
			}
			j++
		} else {
			st.sharedGroups++
			st.sharedU += iu[i].weight
			st.sharedV += iv[j].weight
			st.sharedMin += math.Min(iu[i].weight, iv[j].weight)
			if recordMasks {
				st.sharedMaskU[i] = true
				st.sharedMaskV[j] = true
			}
			i++
			j++
		}
	}
	for ; i < len(iu); i++ {
		w := iu[i].weight
		st.selfExcess += w
		if iu[i].isCore && w > profU.muMass {
			st.selfSalientEx += w - profU.muMass
		}
	}
	for ; j < len(iv); j++ {
		w := iv[j].weight
		st.peerExcess += w
		if iv[j].isCore && w > profV.muMass {
			st.peerSalientEx += w - profV.muMass
		}
	}
	if profU.totalMass > 0 {
		st.selfCov = math.Min(1.0, st.sharedU/profU.totalMass)
	}
	if profV.totalMass > 0 {
		st.peerCov = math.Min(1.0, st.sharedV/profV.totalMass)
	}
	if st.selfCov+st.peerCov > 0 {
		st.harmCov = 2.0 * st.selfCov * st.peerCov / (st.selfCov + st.peerCov)
	}
	return st
}

// evalBurstCloneRedundancyFromProfiles evaluates the 0-Gate continuous echo-clone
// redundancy (Question 1: Identical u ≡ s) of candidate nb with respect to an
// already selected co-event leader s, guarded by P4 (continuous echo purity) and
// P6 (multi-feature scene support >= ceil(e) shared groups).
func evalBurstCloneRedundancyFromProfiles(profS, profNb unitGroupProfile, aff float64) float64 {
	subFloor := 1.0 - invE
	minCloneGroups := int(math.Ceil(math.E))
	if aff < subFloor || len(profS.items) < minCloneGroups || len(profNb.items) < minCloneGroups || profS.totalMass <= 0 || profNb.totalMass <= 0 {
		return 0
	}
	ix := intersectGroupProfiles(profS, profNb, false)
	sceneFloor := math.E * math.Min(profS.muMass, profNb.muMass)
	if ix.sharedGroups < minCloneGroups || ix.sharedMin < sceneFloor || ix.selfCov < invE || ix.peerCov < subFloor {
		return 0
	}
	novelRatio := math.Min(1.0, ix.peerSalientEx/profNb.totalMass)
	echoPurity := (1.0 - novelRatio) * (1.0 - novelRatio)
	return aff * ix.harmCov * ix.peerCov * echoPurity
}

// evalUnrelatedBystanderBorrowFromProfiles evaluates Negative Duality 2-
// (unrelated same-timestamp bystander loan damping): when candidate nb sits in
// the same tight temporal window as a stronger selected unit s (aff >= 1 - 1/e),
// yet both its thematic cosine cos_subj(s, nb) and reverse coverage C_{nb->s}
// fall below the 2nd-order Euler horizon 1/e^2 while nb relies on borrowed
// spacetime Hop energy (nbHop > nbDirect/e), its borrowed cross-record inflation
// is continuously damped.
func evalUnrelatedBystanderBorrowFromProfiles(profS, profNb unitGroupProfile, aff, rawCos, sStrength, nbStrength, nbDirect, nbHop float64) float64 {
	const invE2 = invE * invE
	subFloor := 1.0 - invE
	if rawCos < 0 {
		rawCos = 0
	}
	if aff < subFloor || rawCos >= invE2 || sStrength <= 0 || nbDirect <= 0 || nbHop <= nbDirect*invE || nbStrength >= sStrength*(1.0-invE2) {
		return 0
	}
	ix := intersectGroupProfiles(profS, profNb, false)
	if ix.peerCov >= invE2 {
		return 0
	}
	nbTot := nbDirect + nbHop
	if nbTot <= 0 {
		return 0
	}
	return math.E * aff * (1.0 - rawCos/invE2) * (nbHop / nbTot)
}

// computeDirectedLocalExplainWork calculates how much unknown new information
// unit `explainer` reveals in the same-sentence neighborhood of the shared
// anchors `sharedMask` with `explainee`, bounded by the Anchor-Quota
// Hyperbolic Capacity Envelope min(1, Z_local * A_raw / M_comp,0) and gated by
// the explainee's uncompensated salient missing mass exp(-I_{>μ}(explainee \ explainer) / μ).
func computeDirectedLocalExplainWork(explainer unitGroupProfile, sharedMask []bool, anchorRawNats, explaineeSalientMissing, explaineeMu float64, qTerms map[uint64]struct{}) float64 {
	if anchorRawNats <= 0 || len(explainer.items) == 0 {
		return 0
	}
	var anchorPacked []uint32
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
		}
	}
	if coreAnchorNats <= 0 {
		return 0
	}
	if coreAnchorNats < anchorRawNats {
		anchorRawNats = coreAnchorNats
	}
	if len(anchorPacked) > 1 {
		sort.Slice(anchorPacked, func(i, j int) bool { return anchorPacked[i] < anchorPacked[j] })
	}
	// Apply conditional entropy gain: reduce rawCompSum by the probability that
	// the unit's focal features are already implied by the Query-Conditional Anchor.
	// We replace simple weight accumulation with the information gain measure:
	//   Gain(u | s, Q) = Sum[weight * kern * (1 - P(focal_in_Q | anchor))]
	//   Where P(focal_in_Q | anchor) is estimated by the fraction of group in Q.
	var rawCompSum float64
	for i, isShared := range sharedMask {
		it := explainer.items[i]
		if isShared || !it.isCore || it.weight <= explaineeMu*invE {
			continue
		}
		// If the feature itself is in the query, its explanatory gain is zero.
		if _, inQ := qTerms[it.group]; inQ {
			continue
		}

		var kern float64
		if len(anchorPacked) > 0 && len(it.pos) > 0 {
			dist := closestPackedSlot(it.pos, anchorPacked)
			if dist < 0 {
				continue
			}
			if dist < 1 {
				dist = 1
			}
			kern = math.Exp(-float64(dist-1) * invE)
		} else if !hasAnyPos {
			kern = invE * invE
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

	subjCos := st.docSubjectCos(idx, u, v)
	tempAff := st.temporalAffinity(idx, u, v)
	if st.refTimes[u] <= 0 || st.refTimes[v] <= 0 {
		if int(u) < len(idx.UnitMeta) && int(v) < len(idx.UnitMeta) {
			mu, mv := idx.UnitMeta[u], idx.UnitMeta[v]
			if (mu.DocHash != 0 && mu.DocHash == mv.DocHash) ||
				(mu.BlockHash != 0 && mu.BlockHash == mv.BlockHash) {
				tempAff = math.Max(tempAff, invE)
			}
		}
	}

	selfExplain := computeDirectedLocalExplainWork(profU, ix.sharedMaskU, ix.sharedMin, ix.peerSalientEx, profV.muMass, qSet)
	peerExplain := computeDirectedLocalExplainWork(profV, ix.sharedMaskV, ix.sharedMin, ix.selfSalientEx, profU.muMass, qSet)

	return UnitRelationPeer{
		UnitID:              idx.UnitIDs[v],
		Unit:                v,
		SharedNats:          ix.sharedMin,
		SelfExcessNats:      ix.selfExcess,
		PeerExcessNats:      ix.peerExcess,
		SelfSalientExcess:   ix.selfSalientEx,
		PeerSalientExcess:   ix.peerSalientEx,
		SelfCoverage:        ix.selfCov,
		PeerCoverage:        ix.peerCov,
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
	for _, nb := range st.neighborsOf(u) {
		if nb != u {
			candSet[nb] = struct{}{}
		}
	}
	st.forEachProvenancePeer(idx, u, func(nb int32) {
		candSet[nb] = struct{}{}
	})

	cands := make([]int32, 0, len(candSet))
	for v := range candSet {
		cands = append(cands, v)
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i] < cands[j] })

	identityFloor := 1.0 - invE*invE

	for _, v := range cands {
		profV := e.buildUnitGroupProfile(st, v, highDFThresh)
		if len(profV.items) == 0 {
			continue
		}
		peer := e.evalPairFromProfiles(st, u, profU, v, profV)
		var harmCov float64
		if peer.SelfCoverage+peer.PeerCoverage > 0 {
			harmCov = 2.0 * peer.SelfCoverage * peer.PeerCoverage / (peer.SelfCoverage + peer.PeerCoverage)
		}

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
		if peer.SharedNats >= profU.muMass*invE && peer.SubjectCosine > 0 && harmCov < identityFloor && maxDirCov < identityFloor {
			cp := peer
			cp.ScoreNats = peer.SharedNats * math.Sqrt(peer.SubjectCosine) * (1.0 - math.Abs(cU-cV))
			out.Similar = append(out.Similar, cp)
		}

		// 3. 谁跟我互补 (Complementary / Orthogonal Puzzle u ⊕ v: R_u*R_v mode)
		// An explicit citation edge (DocMeta.CitedIDs) is a declared shared
		// anchor carrying one characteristic energy quantum μ of grounding:
		// a noetic consolidation citing its evidence couples through the
		// declarative bond even with zero lexical overlap.
		citeBond := st.hasCitationEdge(u, v)
		if (peer.SharedNats > 0 || citeBond) && peer.TemporalAffinity > 0 &&
			peer.SelfExcessNats > profU.muMass && peer.PeerExcessNats > profV.muMass && harmCov < identityFloor {
			muScale := math.Max(1.0, math.Min(profU.muMass, profV.muMass))
			sharedEff := peer.SharedNats
			if citeBond && sharedEff < muScale {
				sharedEff = muScale
			}
			anchorBridge := 1.0 - math.Exp(-sharedEff/muScale)
			coupling := anchorBridge * peer.TemporalAffinity * math.Max(invE, math.Sqrt(peer.SubjectCosine))
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

// intraEventEndorseGain evaluates the rank-index-blind directional explanation
// endorsement from a leading co-event seed s to a candidate u within the primary
// event cluster, shared by both .5 cluster endorsement and marginal filtering.
func intraEventEndorseGain(rel UnitRelationPeer, aff, harmCov, eventRatio, satDecay, gap, microOcc, seedBurstOcc float64) float64 {
	const (
		invE2         = invE * invE
		subFloor      = 1.0 - invE
		identityFloor = 1.0 - invE2
	)
	if gap <= 0 || aff < subFloor || harmCov < invE || harmCov >= identityFloor ||
		rel.SelfCoverage < invE || rel.PeerCoverage < invE || rel.SubjectCosine < invE2 ||
		rel.PeerExplainWorkNats <= math.Sqrt(math.E)*rel.SelfExplainWorkNats || rel.PeerExplainWorkNats <= invE {
		return 0
	}
	if aff >= identityFloor && (microOcc >= 1.0 || seedBurstOcc >= 1.0) {
		if rel.PeerExplainWorkNats <= math.E || rel.PeerExplainWorkNats <= math.E*rel.SelfExplainWorkNats {
			return 0
		}
	}
	netExp := rel.PeerExplainWorkNats - rel.SelfExplainWorkNats
	expAdv := netExp / (rel.PeerExplainWorkNats + rel.SelfExplainWorkNats + 1.0)
	trans := satDecay * eventRatio * aff * harmCov * math.Sqrt(rel.SubjectCosine) * (invE + (1.0-invE)*expAdv)
	if trans > subFloor {
		trans = subFloor
	}
	gain := gap * trans
	if dirGain := satDecay * aff * harmCov * math.Sqrt(rel.SubjectCosine) * netExp; dirGain > gain {
		gain = dirGain
	}
	return gain
}

// applyIntraEventExplanatoryEndorsement executes the two-pass event-cluster
// endorsement before unfolding:
//   - Pass 1 (Primary Event Cluster Identification): identifies the peak
//     query-explaining seed u* and its co-event seed neighborhood in corefByDay;
//   - Pass 2 (Intra-Event Directional Explanation Endorsement): within that
//     primary event cluster, a leading seed s endorses a co-event member u that
//     performs net upward explanatory work W(u ▷ s) > √e * W(s ▷ u).
func (e *Engine) applyIntraEventExplanatoryEndorsement(idx *Index, st *rmClusterState, sc *rmQueryScratch, candidates []rmCandidate, unitStructGate []float64, corefByDay map[int64][]int32, groups map[uint64]float64, total float64, highDFThresh int, qTerms map[uint64]Term) {
	if e == nil || idx == nil || st == nil || sc == nil || len(sc.coreUnits) < 2 || total <= 0 {
		return
	}
	const (
		invE2         = invE * invE
		subFloor      = 1.0 - invE
		identityFloor = 1.0 - invE2
		noDay         = int64(-1 << 62)
	)
	evalStage2E := func(u int32) float64 {
		c := &candidates[u]
		base, _, _ := c.stage2Bounds(unitStructGate, u, groups, total)
		if !c.structureSet {
			gate := 1.0
			if len(unitStructGate) > int(u) {
				gate = unitStructGate[u]
			}
			c.structure = gate * st.computeStructuredIncrement(idx, sc, c, groups, total)
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
	peakDay := st.fastDocs[peakUnit].day
	if peakDay == noDay {
		return
	}

	type scoredEventUnit struct {
		unit     int32
		energy   float64
		microOcc float64
		prof     unitGroupProfile
	}
	seedFloor := subFloor * maxE
	minClusterPeers := int(math.Ceil(math.E)) - 1
	activeSet := make(map[int32]bool, 32)
	for d := peakDay - 1; d <= peakDay+1; d++ {
		for _, u := range corefByDay[d] {
			if u == peakUnit || st.temporalAffinity(idx, peakUnit, u) >= invE {
				activeSet[u] = true
			}
		}
	}
	for _, units := range corefByDay {
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
	var eventUnits []scoredEventUnit
	for u := range activeSet {
		if eu := evalStage2E(u); eu > 0 {
			eventUnits = append(eventUnits, scoredEventUnit{unit: u, energy: eu})
		}
	}
	if len(eventUnits) < 2 {
		return
	}
	sort.Slice(eventUnits, func(i, j int) bool {
		if eventUnits[i].energy != eventUnits[j].energy {
			return eventUnits[i].energy > eventUnits[j].energy
		}
		return eventUnits[i].unit < eventUnits[j].unit
	})
	for i := range eventUnits {
		u := eventUnits[i].unit
		eventUnits[i].prof = e.buildUnitGroupProfile(st, u, highDFThresh)
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
	qSet := buildQueryGroundSet(qTerms)
	for i := 1; i < len(eventUnits); i++ {
		u := eventUnits[i].unit
		profU := eventUnits[i].prof
		if len(profU.items) == 0 {
			continue
		}
		microOcc := eventUnits[i].microOcc
		satDecay := math.Exp(-math.Max(0, microOcc-math.Ceil(math.E)) * math.E)
		var best float64
		for k := 0; k < i; k++ {
			if eventUnits[k].energy <= eventUnits[i].energy || len(eventUnits[k].prof.items) == 0 {
				continue
			}
			s := eventUnits[k].unit
			effSeedE := eventUnits[k].energy
			if candidates[s].coreLex >= candidates[peakUnit].coreLex*invE {
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
			rel := e.evalPairFromProfilesQ(st, s, eventUnits[k].prof, u, profU, qSet)
			harmCov := 0.0
			if rel.SelfCoverage+rel.PeerCoverage > 0 {
				harmCov = 2.0 * rel.SelfCoverage * rel.PeerCoverage / (rel.SelfCoverage + rel.PeerCoverage)
			}
			if gain := intraEventEndorseGain(rel, aff, harmCov, eventUnits[k].energy/maxE, satDecay, eventUnits[k].energy-eventUnits[i].energy, microOcc, eventUnits[k].microOcc); gain > best {
				best = gain
			}
		}
		if best > 0 {
			candidates[u].stCorrob += best
		}
	}
}

// buildQueryGroundSet folds the query term table into the known-information
// ground set used by query-conditioned explanation: both the raw term hash and
// its family group are considered already known to the asker.
func buildQueryGroundSet(qTerms map[uint64]Term) map[uint64]struct{} {
	if len(qTerms) == 0 {
		return nil
	}
	qSet := make(map[uint64]struct{}, len(qTerms)*2)
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
	highDFThresh := idx.HighDFThreshold()
	subFloor := 1.0 - invE
	identityFloor := 1.0 - invE*invE

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
		satDecay := math.Exp(-math.Max(0, microOcc[i]-math.Ceil(math.E)) * math.E)
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
			harmCov := 0.0
			if rel.SelfCoverage+rel.PeerCoverage > 0 {
				harmCov = 2.0 * rel.SelfCoverage * rel.PeerCoverage / (rel.SelfCoverage + rel.PeerCoverage)
			}
			if (hits[i].Hop1 == 0 && hits[i].Hop2 == 0) || (harmCov >= identityFloor && rel.PeerSalientExcess == 0) {
				if sc := evalBurstCloneRedundancyFromProfiles(profiles[j], profU, aff); sc > maxClone {
					maxClone = sc
				}
			}
			if bd := evalUnrelatedBystanderBorrowFromProfiles(profiles[j], profU, aff, rel.SubjectCosine, hits[j].Direct, hits[i].Direct, hits[i].Direct, hits[i].Hop1+hits[i].Hop2); bd > maxClone {
				maxClone = bd
			}
			if (j == peakIdx || (valid[peakIdx] && st.temporalAffinity(idx, units[peakIdx], s) >= invE)) && hits[j].Energy >= seedFloor {
				if gain := intraEventEndorseGain(rel, aff, harmCov, hits[j].Energy/maxE, satDecay, hits[j].Energy-out[i].Energy, microOcc[i], microOcc[j]); gain > bestExplainGain {
					bestExplainGain = gain
				}
			}
		}
		if maxClone > 0 {
			scale := 1.0 - invE*invE*maxClone
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
