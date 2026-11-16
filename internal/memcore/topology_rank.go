// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"math"
	"strings"
	"sync/atomic"
	"unicode"
	"unicode/utf8"
)

// LegacyUndirectedTopologyMode is a test-only A/B toggle (default false).
// When true, closure traversal reverts to undirected 2-hop BFS and disables topology bridge / band gating.
var LegacyUndirectedTopologyMode atomic.Bool

// HierarchyMetaEdge represents a structural meta-graph edge between two uint64 node IDs in memory_meta.
// For Kind == "subtype_of", LHS is the specific hyponym (child phrase) and RHS is the general hypernym (parent head concept).
type HierarchyMetaEdge struct {
	LHS      uint64 `gorm:"column:lhs"`
	RHS      uint64 `gorm:"column:rhs"`
	Kind     string `gorm:"column:meta_kind"`
	Evidence string `gorm:"column:evidence"`
}

// ExpandDirectedHierarchyClosure performs a bounded, direction-aware graph closure walk over structural meta edges:
//  1. Equivalence edges ("same_as", "inverse_of") are traversed bidirectionally and preserve direct-seed status.
//  2. Subsumption edges ("subtype_of": LHS=child -> RHS=parent) follow a strict partial-order lattice policy:
//     - Downward instantiation (RHS -> LHS, parent to specific child) is allowed up to `depth` hops so queries for a
//     general category recall specific instances;
//     - Upward generalization (LHS -> RHS, specific child to parent) is allowed at most 1 hop from 0-hop seeds, and
//     an ascended parent is forbidden from reversing direction (RHS -> LHS) into sibling branches, eliminating
//     "child -> parent -> 32 unrelated siblings" fanout pollution.
//
// It returns:
//   - expanded: deterministically ordered seed IDs (original seeds first);
//   - closureOnly: node IDs reached exclusively via subtype_of hierarchy hops (excluding 0-hop seeds and same_as equivalents).
func ExpandDirectedHierarchyClosure(
	seeds []uint64,
	depth, fanout, capLimit int,
	fetchEdges func(seedSet []uint64, limit int) ([]HierarchyMetaEdge, error),
) ([]uint64, map[uint64]bool, error) {
	if depth <= 0 || depth > 2 {
		depth = 2
	}
	if fanout <= 0 {
		fanout = 32
	}
	if capLimit <= 0 || capLimit > 512 {
		capLimit = 256
	}

	const (
		stateDirect   uint8 = 1 // 0-hop seed or same_as/inverse_of equivalent
		stateDownward uint8 = 2 // reached via parent -> child (RHS -> LHS) instantiation
		stateAscended uint8 = 3 // reached via child -> parent (LHS -> RHS) generalization (cannot descend to siblings)
	)

	out := make([]uint64, 0, len(seeds)+fanout)
	state := make(map[uint64]uint8, len(seeds)+fanout)
	for _, id := range seeds {
		if id > 0 && state[id] == 0 {
			state[id] = stateDirect
			out = append(out, id)
		}
	}
	if len(out) == 0 || fetchEdges == nil {
		return out, nil, nil
	}

	cur := append([]uint64(nil), out...)
	for hop := 0; hop < depth && len(cur) > 0 && len(out) < capLimit; hop++ {
		edgeLimit := fanout * 4
		if !LegacyUndirectedTopologyMode.Load() {
			edgeLimit = max(fanout*12, 384)
			if edgeLimit > 512 {
				edgeLimit = 512
			}
		}
		edges, err := fetchEdges(cur, edgeLimit)
		if err != nil {
			return out, nil, err
		}
		if len(edges) == 0 {
			break
		}
		inCur := make(map[uint64]bool, len(cur))
		for _, id := range cur {
			inCur[id] = true
		}
		nxt := make([]uint64, 0, fanout)
		if LegacyUndirectedTopologyMode.Load() {
			for _, e := range edges {
				if e.LHS == 0 || e.RHS == 0 || e.LHS == e.RHS || e.Evidence == "stem-entity" || e.Evidence == "colloquial-host" {
					continue
				}
				for _, peer := range []uint64{e.LHS, e.RHS} {
					if peer == 0 || state[peer] != 0 {
						continue
					}
					state[peer] = stateDirect
					out = append(out, peer)
					nxt = append(nxt, peer)
					if len(nxt) >= fanout || len(out) >= capLimit {
						break
					}
				}
				if len(nxt) >= fanout || len(out) >= capLimit {
					break
				}
			}
			cur = nxt
			continue
		}

		type dirPair struct {
			from uint64
			to   uint64
		}
		var downEdges []dirPair
		var upEdges []dirPair
		downParents := make(map[uint64]map[uint64]bool, 32)
		var downOrder []uint64

		for _, e := range edges {
			if e.LHS == 0 || e.RHS == 0 || e.LHS == e.RHS || e.Evidence == "colloquial-host" {
				continue
			}
			kind := NormalizeMetaKind(e.Kind)
			if kind == "subtype_of" && (e.Evidence == "possessive-owner" || e.Evidence == "possessive-role" || e.Evidence == "spatial-locus-tree") {
				kind = "part_of"
			}
			switch kind {
			case "same_as", "inverse_of":
				// Equivalence edges traverse both ways and inherit the stronger state.
				if inCur[e.LHS] && state[e.RHS] == 0 {
					state[e.RHS] = state[e.LHS]
					out = append(out, e.RHS)
					nxt = append(nxt, e.RHS)
				} else if inCur[e.LHS] && state[e.LHS] == stateDirect && state[e.RHS] != stateDirect {
					state[e.RHS] = stateDirect
				}
				if len(nxt) >= fanout || len(out) >= capLimit {
					break
				}
				if inCur[e.RHS] && state[e.LHS] == 0 {
					state[e.LHS] = state[e.RHS]
					out = append(out, e.LHS)
					nxt = append(nxt, e.LHS)
				} else if inCur[e.RHS] && state[e.RHS] == stateDirect && state[e.LHS] != stateDirect {
					state[e.LHS] = stateDirect
				}
			case "subtype_of":
				// Direction 1: Downward instantiation (Parent RHS -> Child LHS).
				if inCur[e.RHS] && (state[e.RHS] == stateDirect || state[e.RHS] == stateDownward) {
					if e.Evidence != "stem-entity" && e.Evidence != "colloquial-host" {
						downEdges = append(downEdges, dirPair{from: e.RHS, to: e.LHS})
					}
					pm := downParents[e.LHS]
					if pm == nil {
						pm = make(map[uint64]bool, 2)
						downParents[e.LHS] = pm
						downOrder = append(downOrder, e.LHS)
					}
					pm[e.RHS] = true
				}
				// Direction 2: Upward generalization (Child LHS -> Parent RHS) at hop 0 from direct seeds.
				if hop == 0 && inCur[e.LHS] && state[e.LHS] == stateDirect {
					upEdges = append(upEdges, dirPair{from: e.LHS, to: e.RHS})
				}
			case "part_of":
				// Meronymy / possessive-dependence / spatial-containment (LHS=part/dependent -> RHS=whole/owner/container):
				//  1. Downward whole -> part (RHS -> LHS):
				//     - Spatial/structural containment ("spatial-locus-tree" or general meronymy) is transitive downward
				//       from direct or downward seeds (e.g. Province -> City -> Town);
				//     - Possessive ownership ("possessive-owner" / "possessive-role") only steps 1 hop from a 0-hop direct
				//       owner seed (stateDirect) and NEVER chains after a subtype_of descent (preventing "Person -> Audrey -> Audrey's dog"
				//       cross-sort category pollution).
				isPossessive := e.Evidence == "possessive-owner" || e.Evidence == "possessive-role"
				if inCur[e.RHS] && (state[e.RHS] == stateDirect || (!isPossessive && state[e.RHS] == stateDownward)) {
					downEdges = append(downEdges, dirPair{from: e.RHS, to: e.LHS})
					pm := downParents[e.LHS]
					if pm == nil {
						pm = make(map[uint64]bool, 2)
						downParents[e.LHS] = pm
						downOrder = append(downOrder, e.LHS)
					}
					pm[e.RHS] = true
				}
				//  2. Upward part -> whole/owner (LHS -> RHS): at most 1 hop from 0-hop direct seeds, landing in
				//     stateAscended so it never reverses into sibling parts/possessions.
				if hop == 0 && inCur[e.LHS] && state[e.LHS] == stateDirect {
					upEdges = append(upEdges, dirPair{from: e.LHS, to: e.RHS})
				}
			}
		}

		// Tier 1: Multi-seed convergent sentence/phrase hubs (reached downward from >= 2 distinct seeds in cur).
		// Prioritized first before single-seed children so fanout never drops multi-seed intersections,
		// while keeping stateDownward so ShouldAdmitClosureSeedBand still filters 0.5-score empty shells.
		for _, child := range downOrder {
			if len(nxt) >= fanout || len(out) >= capLimit {
				break
			}
			if len(downParents[child]) >= 2 && state[child] == 0 {
				state[child] = stateDownward
				out = append(out, child)
				nxt = append(nxt, child)
			}
		}

		// Tier 2: Single-seed downward instantiation with per-parent fair quota (prevents one broad concept from hogging fanout).
		const perParentDownCap = 16
		parentUsed := make(map[uint64]int, len(cur))
		for _, de := range downEdges {
			if len(nxt) >= fanout || len(out) >= capLimit {
				break
			}
			if state[de.to] != 0 || parentUsed[de.from] >= perParentDownCap {
				continue
			}
			parentUsed[de.from]++
			state[de.to] = stateDownward
			out = append(out, de.to)
			nxt = append(nxt, de.to)
		}

		// Tier 3: Upward generalization (1 hop from 0-hop seeds to Hasse parent phrases/entities, forbidden from descending to siblings).
		const perChildUpCap = 4
		childUpUsed := make(map[uint64]int, len(cur))
		for _, ue := range upEdges {
			if len(nxt) >= fanout || len(out) >= capLimit {
				break
			}
			if state[ue.to] != 0 || childUpUsed[ue.from] >= perChildUpCap {
				continue
			}
			childUpUsed[ue.from]++
			state[ue.to] = stateAscended
			out = append(out, ue.to)
			nxt = append(nxt, ue.to)
		}

		cur = nxt
	}

	var closureOnly map[uint64]bool
	for id, st := range state {
		if st != stateDirect {
			if closureOnly == nil {
				closureOnly = make(map[uint64]bool)
			}
			closureOnly[id] = true
		}
	}
	return out, closureOnly, nil
}

// TopologyRowView is a host-agnostic view of a scored candidate assertion for graph-topology ranking and deduplication.
type TopologyRowView struct {
	NodeID          uint64
	SubjectID       uint64
	RelID           uint64
	ObjectID        uint64
	ObjectLiteral   string
	SourceTraceHash string
	Status          string
	Score           float64
	LexScore        float64
	NonSubj         bool
	OrigNonSubj     bool
	DirectXO        bool
}

// TopologyBoost holds the additive score delta and signal promotion computed from graph topology.
type TopologyBoost struct {
	Delta       float64
	PromoteNS   bool
	PromoteOrig bool
}

// ComputeGraphBridgeBoosts computes inverse-degree weighted graph-bridge boosts over the candidate assertion subgraph.
// When an intermediate entity/concept hub v (with bounded degree 2 <= deg(v) <= 16, excluding primary query subjects)
// bridges a primary subject (or strong anchor assertion) and a distinct assertion carrying direct non-subject lexical
// signal from another turn or subject, assertions incident to v receive an information-theoretic boost
// proportional to 1 / log2(2 + deg(v)).
func ComputeGraphBridgeBoosts(
	rows []TopologyRowView,
	coSubjIDs map[uint64]bool,
	canon func(uint64) uint64,
) []TopologyBoost {
	boosts := make([]TopologyBoost, len(rows))
	if len(rows) < 2 || LegacyUndirectedTopologyMode.Load() {
		return boosts
	}
	if canon == nil {
		canon = func(id uint64) uint64 { return id }
	}
	maxObservedLex := 0.0
	for _, r := range rows {
		if r.LexScore > maxObservedLex {
			maxObservedLex = r.LexScore
		}
		if r.Score > maxObservedLex {
			maxObservedLex = r.Score
		}
	}
	regime := CalibrateScoreRegime(maxObservedLex)

	type nodeStats struct {
		deg                int
		primSubjs          map[uint64]bool
		hasCrossSubjNSSide bool
		maxNSScore         float64
		nsTurnHash         string
		primTurnHash       string
		crossTurnBridge    bool
	}
	stats := make(map[uint64]*nodeStats, 64)
	touch := func(nid uint64, r TopologyRowView) {
		if nid == 0 || coSubjIDs[nid] {
			return
		}
		st := stats[nid]
		if st == nil {
			st = &nodeStats{}
			stats[nid] = st
		}
		st.deg++
		cSubj := canon(r.SubjectID)
		isPrim := coSubjIDs[r.SubjectID] || coSubjIDs[cSubj]
		if isPrim {
			if st.primSubjs == nil {
				st.primSubjs = make(map[uint64]bool, 2)
			}
			st.primSubjs[cSubj] = true
			if st.primTurnHash == "" {
				st.primTurnHash = r.SourceTraceHash
			} else if r.SourceTraceHash != "" && r.SourceTraceHash != st.primTurnHash {
				st.crossTurnBridge = true
			}
		} else if r.OrigNonSubj && r.DirectXO && r.LexScore >= regime.LeadCompetitionFloor {
			// True 2-hop graph path requires the second leg to come from a non-primary subject
			// (e.g. PrimSubj -> Hub -> Target, or PrimSubj -> Hub <- OtherSubj).
			st.hasCrossSubjNSSide = true
			if r.LexScore > st.maxNSScore {
				st.maxNSScore = r.LexScore
			}
			if st.nsTurnHash == "" {
				st.nsTurnHash = r.SourceTraceHash
			} else if r.SourceTraceHash != "" && r.SourceTraceHash != st.nsTurnHash {
				st.crossTurnBridge = true
			}
		}
		if st.primTurnHash != "" && st.nsTurnHash != "" && st.primTurnHash != st.nsTurnHash {
			st.crossTurnBridge = true
		}
	}

	for _, r := range rows {
		if r.Status != "" && r.Status != "active" {
			continue
		}
		sID := canon(r.SubjectID)
		oID := canon(r.ObjectID)
		if sID > 0 {
			touch(sID, r)
		}
		if oID > 0 && oID != sID {
			touch(oID, r)
		}
	}

	reqPrimCnt := 1
	if len(coSubjIDs) >= 2 {
		reqPrimCnt = 2
	}

	// Identify valid bounded-degree cross-turn, cross-subject 2-hop bridge hubs (2 <= deg <= 10).
	bridgeHubBonus := make(map[uint64]float64, 16)
	bridgeHubMaxNS := make(map[uint64]float64, 16)
	for nid, st := range stats {
		if st.deg < 2 || st.deg > 10 || len(st.primSubjs) < reqPrimCnt || !st.hasCrossSubjNSSide || !st.crossTurnBridge {
			continue
		}
		// Inverse-degree specificity: rarer graph hubs carry higher mutual information.
		invDeg := 1.0 / math.Log2(2.0+float64(st.deg))
		bonus := 5.5 * invDeg
		if bonus > 4.0 {
			bonus = 4.0
		}
		bridgeHubBonus[nid] = bonus
		bridgeHubMaxNS[nid] = st.maxNSScore
	}

	// Same-Turn Directed Object-to-Subject Continuation (S --[h]--> O --[h]--> O' [--[h]--> O'']):
	// When a single turn splits a clause into S -> O and O -> O' (or 2-hop O' -> O''),
	// propagate signal bidirectionally across shared intermediate nodes within turn h (up to 2 hops).
	type turnObjKey struct {
		turn string
		node uint64
	}
	primTurnObj := make(map[turnObjKey]bool, 16)
	for _, r := range rows {
		if (r.Status != "" && r.Status != "active") || r.SourceTraceHash == "" {
			continue
		}
		sID := canon(r.SubjectID)
		oID := canon(r.ObjectID)
		if sID > 0 && oID > 0 && sID != oID && (coSubjIDs[r.SubjectID] || coSubjIDs[sID]) && !coSubjIDs[oID] {
			primTurnObj[turnObjKey{turn: r.SourceTraceHash, node: oID}] = true
		}
	}
	downMaxLex := make(map[turnObjKey]float64, 16)
	downOrig := make(map[turnObjKey]bool, 16)
	upPrimLex := make(map[turnObjKey]float64, 16)
	upPrimOrig := make(map[turnObjKey]bool, 16)
	for _, r := range rows {
		if (r.Status != "" && r.Status != "active") || r.SourceTraceHash == "" {
			continue
		}
		sID := canon(r.SubjectID)
		oID := canon(r.ObjectID)
		if sID > 0 && !coSubjIDs[sID] {
			tk := turnObjKey{turn: r.SourceTraceHash, node: sID}
			minLex := regime.MultiTurnChainFloor
			if primTurnObj[tk] && r.OrigNonSubj && r.DirectXO {
				minLex = regime.IntraChainPurityFloor
			}
			if st := stats[sID]; st != nil && st.deg >= 2 && st.deg <= CompactBandMaxRows && r.NonSubj && r.LexScore >= minLex {
				if r.LexScore > downMaxLex[tk] {
					downMaxLex[tk] = r.LexScore
				}
				if r.OrigNonSubj && r.DirectXO {
					downOrig[tk] = true
				}
			}
		}
		if sID > 0 && oID > 0 && sID != oID && (coSubjIDs[r.SubjectID] || coSubjIDs[sID]) && !coSubjIDs[oID] {
			if st := stats[oID]; st != nil && st.deg >= 2 && st.deg <= CompactBandMaxRows && r.NonSubj && r.LexScore >= regime.UpwardContinuationFloor {
				tk := turnObjKey{turn: r.SourceTraceHash, node: oID}
				if r.LexScore > upPrimLex[tk] {
					upPrimLex[tk] = r.LexScore
				}
				if r.OrigNonSubj {
					upPrimOrig[tk] = true
				}
			}
		}
	}
	// 2nd-hop same-turn relaxation across intermediate nodes within the same turn:
	for _, r := range rows {
		if (r.Status != "" && r.Status != "active") || r.SourceTraceHash == "" {
			continue
		}
		sID := canon(r.SubjectID)
		oID := canon(r.ObjectID)
		if sID > 0 && oID > 0 && sID != oID && !coSubjIDs[sID] {
			tkSubj := turnObjKey{turn: r.SourceTraceHash, node: sID}
			tkObj := turnObjKey{turn: r.SourceTraceHash, node: oID}
			if st := stats[sID]; st != nil && st.deg >= 2 && st.deg <= CompactBandMaxRows {
				if dLex := downMaxLex[tkObj]; dLex >= regime.MultiTurnChainFloor {
					prop := dLex * 0.88
					if prop > downMaxLex[tkSubj] {
						downMaxLex[tkSubj] = prop
					}
					if downOrig[tkObj] {
						downOrig[tkSubj] = true
					}
				}
			}
			if st := stats[oID]; st != nil && st.deg >= 2 && st.deg <= CompactBandMaxRows {
				if uLex := upPrimLex[tkSubj]; uLex >= regime.UpwardContinuationFloor {
					prop := uLex * 0.85
					if prop > upPrimLex[tkObj] {
						upPrimLex[tkObj] = prop
					}
					if upPrimOrig[tkSubj] {
						upPrimOrig[tkObj] = true
					}
				}
			}
		}
	}

	for i, r := range rows {
		if r.Status != "" && r.Status != "active" {
			continue
		}
		sID := canon(r.SubjectID)
		oID := canon(r.ObjectID)
		if r.SourceTraceHash != "" {
			// Upstream S -> O receives boost from same-turn downstream O -> O'
			if oID > 0 && sID != oID {
				tk := turnObjKey{turn: r.SourceTraceHash, node: oID}
				minDLex := regime.MultiTurnChainFloor
				if primTurnObj[tk] && downOrig[tk] {
					minDLex = regime.IntraChainPurityFloor
				}
				if dLex := downMaxLex[tk]; dLex >= minDLex {
					target := dLex * 0.88
					maxDelta := 14.0
					if primTurnObj[tk] && downOrig[tk] && (coSubjIDs[r.SubjectID] || coSubjIDs[sID]) {
						if dLex+7.5 > target {
							target = dLex + 7.5
						}
						if target < 13.5 {
							target = 13.5
						}
						maxDelta = 16.0
					} else if target < 7.5 && downOrig[tk] {
						target = 7.5
					}
					delta := regime.IntraChainPurityFloor
					if target-r.LexScore > delta {
						delta = target - r.LexScore
					}
					if delta > maxDelta {
						delta = maxDelta
					}
					boosts[i] = TopologyBoost{
						Delta:       delta,
						PromoteNS:   true,
						PromoteOrig: r.OrigNonSubj || downOrig[tk],
					}
					continue
				}
			}
			// Downstream O -> O' receives boost from same-turn primary S -> O
			if sID > 0 && !coSubjIDs[sID] {
				tk := turnObjKey{turn: r.SourceTraceHash, node: sID}
				if uLex := upPrimLex[tk]; uLex >= regime.UpwardContinuationFloor {
					target := uLex * 0.85
					delta := regime.ReachExpansionFloor
					if target-r.LexScore > delta {
						delta = target - r.LexScore
					}
					if delta > BaselineDirectXOHostScore {
						delta = BaselineDirectXOHostScore
					}
					boosts[i] = TopologyBoost{
						Delta:       delta,
						PromoteNS:   true,
						PromoteOrig: r.OrigNonSubj || upPrimOrig[tk],
					}
					continue
				}
			}
		}
		if len(bridgeHubBonus) == 0 {
			continue
		}
		bS := bridgeHubBonus[sID]
		bO := bridgeHubBonus[oID]
		bestB := math.Max(bS, bO)
		if bestB <= 0 {
			continue
		}
		hubID := oID
		if bS > bO {
			hubID = sID
		}
		// Only boost assertions that either already have lexical signal or directly connect a primary subject to the bridge hub.
		cSubj := canon(r.SubjectID)
		isPrimConnected := coSubjIDs[r.SubjectID] || coSubjIDs[cSubj] || r.NonSubj
		if !isPrimConnected {
			continue
		}
		delta := bestB
		if !r.NonSubj {
			// Pure structural hop from primary subject into a verified bridge hub: moderate boost.
			delta = bestB * 0.75
		}
		boosts[i] = TopologyBoost{
			Delta:       delta,
			PromoteNS:   true,
			PromoteOrig: r.OrigNonSubj || (r.DirectXO && bridgeHubMaxNS[hubID] >= regime.InverseEpisodeAnchorFloor),
		}
	}
	return boosts
}

// ShouldAdmitClosureSeedBand decides whether a subject node that was reached ONLY via subtype_of
// hierarchy closure (and not as a 0-hop direct seed or same_as equivalent) qualifies to form its own subject band.
// Requiring at least one non-subject signal row (or a direct active link to a 0-hop seed) prevents
// generalized hypernym hubs from spawning empty-shell noise bands full of 0.5-score background rows.
func ShouldAdmitClosureSeedBand(
	subjectID uint64,
	closureOnlySeeds map[uint64]bool,
	directSeedSet map[uint64]bool,
	rows []TopologyRowView,
	canon func(uint64) uint64,
) bool {
	if len(closureOnlySeeds) == 0 || LegacyUndirectedTopologyMode.Load() {
		return true
	}
	if canon == nil {
		canon = func(id uint64) uint64 { return id }
	}
	cSid := canon(subjectID)
	if directSeedSet[subjectID] || directSeedSet[cSid] {
		return true
	}
	if !closureOnlySeeds[subjectID] && !closureOnlySeeds[cSid] {
		return true
	}
	for _, r := range rows {
		if r.NonSubj && r.Score >= 2.0 {
			return true
		}
		if r.DirectXO && r.Score >= 1.5 {
			return true
		}
		if r.ObjectID > 0 && (directSeedSet[r.ObjectID] || directSeedSet[canon(r.ObjectID)]) && r.Score > 0.5 {
			return true
		}
		if len(rows) <= 3 && r.Score > 0.55 {
			return true
		}
	}
	return false
}

// FoldRedundantSupertypeRows identifies assertions that are redundant generalizations of a more specific
// hyponym assertion within the same source turn, same canonical subject, and same canonical relation.
// When row u (pointing to specific child object C) and row p (pointing to general parent object P)
// share (SourceTraceHash, canon(SubjectID), canon(RelID)) and C's label strictly contains P's label as a
// word-bounded suffix/head concept (or C -> P is in subtypePairs), row p is marked as folded into u
// so the chain renders the specific fact once without duplicating the generalized echo.
func FoldRedundantSupertypeRows(
	rows []TopologyRowView,
	labels map[uint64]string,
	subtypePairs map[[2]uint64]bool,
	canon func(uint64) uint64,
) (foldedNodeIDs map[uint64]bool, promotedScores map[uint64]float64) {
	if len(rows) < 2 || LegacyUndirectedTopologyMode.Load() {
		return nil, nil
	}
	if canon == nil {
		canon = func(id uint64) uint64 { return id }
	}
	type turnSubjRel struct {
		turn string
		subj uint64
		rel  uint64
	}
	groups := make(map[turnSubjRel][]int, len(rows)/2)
	for i, r := range rows {
		if r.SourceTraceHash == "" || r.SubjectID == 0 || r.RelID == 0 || r.ObjectID == 0 {
			continue
		}
		if r.Status != "" && r.Status != "active" {
			continue
		}
		k := turnSubjRel{
			turn: r.SourceTraceHash,
			subj: canon(r.SubjectID),
			rel:  canon(r.RelID),
		}
		groups[k] = append(groups[k], i)
	}

	isSpecificOf := func(childID, parentID uint64) bool {
		if childID == 0 || parentID == 0 || childID == parentID {
			return false
		}
		cChild, cParent := canon(childID), canon(parentID)
		if cChild == cParent {
			return false
		}
		if subtypePairs != nil && (subtypePairs[[2]uint64{childID, parentID}] || subtypePairs[[2]uint64{cChild, cParent}]) {
			return true
		}
		childLb := strings.ToLower(strings.TrimSpace(labels[childID]))
		parentLb := strings.ToLower(strings.TrimSpace(labels[parentID]))
		if childLb == "" || parentLb == "" || childLb == parentLb || !strings.HasSuffix(childLb, parentLb) {
			return false
		}
		// Pure formal head-noun subsumption check (100% language-agnostic):
		// child concept is within TokenTier8 and ends with parent concept (word-bounded for spaced scripts, suffix for CJK).
		if CountTokens(childLb) <= TokenTier8 && StringShannonBits(parentLb) >= 4.0 {
			if IsPureCJK(childLb) && IsPureCJK(parentLb) {
				return true
			}
			prefixLen := len(childLb) - len(parentLb)
			if prefixLen > 0 {
				rPrev, _ := utf8.DecodeLastRuneInString(childLb[:prefixLen])
				if unicode.IsSpace(rPrev) {
					return true
				}
			}
		}
		return false
	}

	for _, idxs := range groups {
		if len(idxs) < 2 {
			continue
		}
		for _, iChild := range idxs {
			rChild := rows[iChild]
			for _, iParent := range idxs {
				if iChild == iParent {
					continue
				}
				rParent := rows[iParent]
				if isSpecificOf(rChild.ObjectID, rParent.ObjectID) {
					if foldedNodeIDs == nil {
						foldedNodeIDs = make(map[uint64]bool)
						promotedScores = make(map[uint64]float64)
					}
					foldedNodeIDs[rParent.NodeID] = true
					childBest := rChild.Score
					if prevChild, ok := promotedScores[rChild.NodeID]; ok && prevChild > childBest {
						childBest = prevChild
					}
					parentBest := rParent.Score
					if prevParent, ok := promotedScores[rParent.NodeID]; ok && prevParent > parentBest {
						parentBest = prevParent
					}
					best := math.Max(childBest, parentBest)
					if prev, ok := promotedScores[rChild.NodeID]; !ok || best > prev {
						promotedScores[rChild.NodeID] = best
					}
				}
			}
		}
	}

	// Cross-turn generalized hypernym echo folding:
	// Within the same (canon(SubjectID), canon(RelID)), if a specific hyponym assertion rChild carries
	// non-subject query signal (NonSubj && Score >= 4.0) while a generalized hypernym assertion rParent
	// carries zero non-subject signal (!NonSubj && Score <= 1.0), fold the zero-signal generalized echo into rChild.
	type subjRelKey struct {
		subj uint64
		rel  uint64
	}
	srGroups := make(map[subjRelKey][]int, len(rows)/2)
	for i, r := range rows {
		if r.SubjectID == 0 || r.RelID == 0 || r.ObjectID == 0 {
			continue
		}
		if r.Status != "" && r.Status != "active" {
			continue
		}
		if foldedNodeIDs != nil && foldedNodeIDs[r.NodeID] {
			continue
		}
		k := subjRelKey{subj: canon(r.SubjectID), rel: canon(r.RelID)}
		srGroups[k] = append(srGroups[k], i)
	}
	for _, idxs := range srGroups {
		if len(idxs) < 2 {
			continue
		}
		for _, iChild := range idxs {
			rChild := rows[iChild]
			if !rChild.NonSubj || rChild.Score < 4.0 {
				continue
			}
			for _, iParent := range idxs {
				if iChild == iParent {
					continue
				}
				rParent := rows[iParent]
				if rParent.NonSubj || rParent.Score > 1.0 {
					continue
				}
				if isSpecificOf(rChild.ObjectID, rParent.ObjectID) {
					if foldedNodeIDs == nil {
						foldedNodeIDs = make(map[uint64]bool)
						promotedScores = make(map[uint64]float64)
					}
					foldedNodeIDs[rParent.NodeID] = true
				}
			}
		}
	}

	// Same-Subject Same-Turn Self-Wrapper Folding:
	// When an outer assertion rOuter cites an inner assertion rInner (rOuter.ObjectID == rInner.NodeID)
	// within the exact same turn (SourceTraceHash) AND shares the exact same canonical SubjectID
	// (e.g., John —[feel/say]→ #[John —[organize]→ online comp]), rOuter is a self-referential wrapper
	// around rInner in the same subject band. Fold rOuter into rInner and promote the maximum score to rInner,
	// while preserving 100% of cross-subject relays (where canon(rOuter.SubjectID) != canon(rInner.SubjectID)).
	byAssertionNodeID := make(map[uint64]int, len(rows))
	for i, r := range rows {
		if r.NodeID > 0 && (r.Status == "" || r.Status == "active") {
			byAssertionNodeID[r.NodeID] = i
		}
	}
	for _, rOuter := range rows {
		if rOuter.NodeID == 0 || rOuter.SubjectID == 0 || rOuter.ObjectID == 0 || rOuter.SourceTraceHash == "" {
			continue
		}
		if rOuter.Status != "" && rOuter.Status != "active" {
			continue
		}
		iInner, ok := byAssertionNodeID[rOuter.ObjectID]
		if !ok {
			continue
		}
		rInner := rows[iInner]
		if rInner.NodeID == rOuter.NodeID || rInner.SubjectID == 0 {
			continue
		}
		if canon(rOuter.SubjectID) == canon(rInner.SubjectID) && rOuter.SourceTraceHash == rInner.SourceTraceHash {
			if foldedNodeIDs == nil {
				foldedNodeIDs = make(map[uint64]bool)
				promotedScores = make(map[uint64]float64)
			}
			foldedNodeIDs[rOuter.NodeID] = true
			innerBest := rInner.Score
			if prev, ok := promotedScores[rInner.NodeID]; ok && prev > innerBest {
				innerBest = prev
			}
			outerBest := rOuter.Score
			if prev, ok := promotedScores[rOuter.NodeID]; ok && prev > outerBest {
				outerBest = prev
			}
			best := math.Max(innerBest, outerBest)
			if prev, ok := promotedScores[rInner.NodeID]; !ok || best > prev {
				promotedScores[rInner.NodeID] = best
			}
		}
	}

	for _, r := range rows {
		if r.NodeID == 0 || (r.Status != "" && r.Status != "active") {
			continue
		}
		subjLb := strings.TrimSpace(labels[r.SubjectID])
		relLb := strings.TrimSpace(labels[r.RelID])
		objLb := strings.TrimSpace(r.ObjectLiteral)
		if objLb == "" && r.ObjectID > 0 {
			objLb = strings.TrimSpace(labels[r.ObjectID])
		}
		if IsDegenerateSelfEchoAssertion(subjLb, relLb, objLb) {
			if foldedNodeIDs == nil {
				foldedNodeIDs = make(map[uint64]bool)
				promotedScores = make(map[uint64]float64)
			}
			foldedNodeIDs[r.NodeID] = true
		}
	}

	return foldedNodeIDs, promotedScores
}

// CausalDerivationEdge represents a directed lower-half cognitive algebra meta-edge
// (causes, derived_from, supersedes, conflicts_with) between assertion/entity nodes.
// Universality: 100% language-agnostic pure graph operator (Axiom 22).
type CausalDerivationEdge struct {
	LHS      uint64 `gorm:"column:lhs"`
	RHS      uint64 `gorm:"column:rhs"`
	MetaKind string `gorm:"column:meta_kind"`
	Evidence string `gorm:"column:evidence"`
}

// ExpandCausalDerivationGraph performs bounded bidirectional graph traversal along
// lower-half propositional edges (`causes` and `derived_from`, up to `depth` hops)
// starting from `seedAssertions`.
//   - Forward/backward along `causes` (cause <-> effect) and `derived_from` (conclusion <-> premise);
//   - Filters out morphological `sx-sxo-refinement` edges so only genuine propositional links traverse;
//   - Returns newly discovered target node IDs (excluding initial seeds), their hop distance (1..depth),
//     and the originating root seed ID `rootSeedOf[target]` for score propagation and C6 wormhole coupling.
func ExpandCausalDerivationGraph(
	seedAssertions []uint64,
	depth, fanout, maxCap int,
	fetch func(cur []uint64, limit int) ([]CausalDerivationEdge, error),
) (expanded []uint64, hopByNode map[uint64]int, rootSeedOf map[uint64]uint64, err error) {
	hopByNode = make(map[uint64]int)
	rootSeedOf = make(map[uint64]uint64)
	if len(seedAssertions) == 0 || depth <= 0 || fetch == nil {
		return nil, hopByNode, rootSeedOf, nil
	}
	if depth > 3 {
		depth = 3
	}
	if fanout <= 0 || fanout > 64 {
		fanout = 24
	}
	if maxCap <= 0 || maxCap > 256 {
		maxCap = 64
	}
	seen := make(map[uint64]bool, len(seedAssertions)*2)
	frontier := make([]uint64, 0, len(seedAssertions))
	for _, id := range seedAssertions {
		if id > 0 && !seen[id] {
			seen[id] = true
			rootSeedOf[id] = id
			frontier = append(frontier, id)
		}
	}
	for hop := 1; hop <= depth && len(frontier) > 0 && len(expanded) < maxCap; hop++ {
		edges, ferr := fetch(frontier, len(frontier)*fanout)
		if ferr != nil {
			return expanded, hopByNode, rootSeedOf, ferr
		}
		if len(edges) == 0 {
			break
		}
		nextFrontier := make([]uint64, 0, len(edges))
		perParent := make(map[uint64]int, len(frontier))
		for _, e := range edges {
			if e.LHS == 0 || e.RHS == 0 || e.LHS == e.RHS {
				continue
			}
			if e.Evidence == "sx-sxo-refinement" || strings.HasPrefix(e.Evidence, "bitfield-") {
				continue
			}
			if e.MetaKind != "causes" && e.MetaKind != "derived_from" {
				continue
			}
			// Bidirectional chase: if LHS is in current frontier, reach RHS; if RHS is in frontier, reach LHS.
			for _, pair := range [][2]uint64{{e.LHS, e.RHS}, {e.RHS, e.LHS}} {
				src, dst := pair[0], pair[1]
				root, okSrc := rootSeedOf[src]
				if !okSrc || seen[dst] || perParent[src] >= fanout {
					continue
				}
				seen[dst] = true
				perParent[src]++
				hopByNode[dst] = hop
				rootSeedOf[dst] = root
				expanded = append(expanded, dst)
				nextFrontier = append(nextFrontier, dst)
				if len(expanded) >= maxCap {
					break
				}
			}
			if len(expanded) >= maxCap {
				break
			}
		}
		frontier = nextFrontier
	}
	return expanded, hopByNode, rootSeedOf, nil
}

// ComputeStrongNegativeExcludedSet computes the Pauli exclusion set of assertion node IDs
// that are superseded or contradicted by the active primary assertions `activePrimaryIDs`.
//   - For `supersedes` (LHS=new supersedes RHS=old): RHS is always excluded whenever an active supersedes edge targets it;
//   - For `conflicts_with` (symmetric exclusion): if one endpoint is in `activePrimaryIDs` and the other is not, the non-primary endpoint is excluded from weak C6 companion slots.
func ComputeStrongNegativeExcludedSet(activePrimaryIDs []uint64, edges []CausalDerivationEdge) map[uint64]bool {
	excluded := make(map[uint64]bool)
	if len(edges) == 0 {
		return excluded
	}
	prim := make(map[uint64]bool, len(activePrimaryIDs))
	for _, id := range activePrimaryIDs {
		if id > 0 {
			prim[id] = true
		}
	}
	for _, e := range edges {
		if e.LHS == 0 || e.RHS == 0 || e.LHS == e.RHS {
			continue
		}
		switch e.MetaKind {
		case "supersedes":
			// LHS (new) supersedes RHS (old): old assertion must never resurrect via weak C6 container.
			excluded[e.RHS] = true
		case "conflicts_with":
			if prim[e.LHS] && !prim[e.RHS] {
				excluded[e.RHS] = true
			} else if prim[e.RHS] && !prim[e.LHS] {
				excluded[e.LHS] = true
			}
		}
	}
	return excluded
}
