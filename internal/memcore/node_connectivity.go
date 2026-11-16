// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// ConnectivityDefectKind enumerates the closed set of structural and topological connectivity violations.
type ConnectivityDefectKind string

const (
	DefectMissingAssertionNode ConnectivityDefectKind = "missing_assertion_node"
	DefectOrphanEndpoint       ConnectivityDefectKind = "orphan_endpoint"
	DefectInvalidEndpointKind  ConnectivityDefectKind = "invalid_endpoint_kind"
	DefectPhantomRefLeak       ConnectivityDefectKind = "phantom_ref_leak"
	DefectOrphanParticipant    ConnectivityDefectKind = "orphan_participant"
	DefectOrphanLedgerRef      ConnectivityDefectKind = "orphan_ledger_ref"
	DefectOrphanAlias          ConnectivityDefectKind = "orphan_alias"
	DefectOrphanMetaEdge       ConnectivityDefectKind = "orphan_meta_edge"
	DefectMetaSelfLoop         ConnectivityDefectKind = "meta_self_loop"
	DefectSameAsCycleOrInvalid ConnectivityDefectKind = "same_as_cycle_or_invalid"
	DefectHierarchyCycle       ConnectivityDefectKind = "hierarchy_cycle"
	DefectIsolatedNode         ConnectivityDefectKind = "isolated_node"
)

// ConnectivityDefect describes a single deterministic connectivity or topological violation.
type ConnectivityDefect struct {
	Kind    ConnectivityDefectKind `json:"kind"`
	NodeID  string                 `json:"node_id,omitempty"`
	EdgeID  string                 `json:"edge_id,omitempty"`
	Slot    string                 `json:"slot,omitempty"`
	Target  string                 `json:"target,omitempty"`
	Message string                 `json:"message"`
}

// AssertionExtRef carries optional extended node-reference slots (such as IncarnationID)
// that may accompany an Assertion in multi-incarnation / spacetime-addressed graphs.
type AssertionExtRef struct {
	AssertionNodeID string `json:"assertion_node_id"`
	IncarnationID   string `json:"incarnation_id,omitempty"`
}

// GraphConnectivityInput holds the complete multi-channel graph snapshot for connectivity verification.
type GraphConnectivityInput struct {
	Nodes                   []Node            `json:"nodes"`
	Assertions              []Assertion       `json:"assertions"`
	AssertionExtRefs        []AssertionExtRef `json:"assertion_ext_refs,omitempty"`
	Participants            []Participant     `json:"participants,omitempty"`
	Aliases                 []NodeAlias       `json:"aliases,omitempty"`
	MetaEdges               []MetaEdge        `json:"meta_edges,omitempty"`
	LedgerRows              []RestateRow      `json:"ledger_rows,omitempty"`
	AllowPreRegisteredRoots bool              `json:"allow_pre_registered_roots,omitempty"`
	MaxDefectSamples        int               `json:"max_defect_samples,omitempty"`
}

// GraphConnectivityReport summarizes node-level, edge-level, and component-level connectivity health.
type GraphConnectivityReport struct {
	Clean                      bool                           `json:"clean"`
	TotalNodes                 int                            `json:"total_nodes"`
	LiveNodes                  int                            `json:"live_nodes"`
	MergedNodes                int                            `json:"merged_nodes"`
	RetiredNodes               int                            `json:"retired_nodes"`
	TotalAssertions            int                            `json:"total_assertions"`
	ActiveAssertions           int                            `json:"active_assertions"`
	ConnectedLiveNodes         int                            `json:"connected_live_nodes"`
	AnchoredLiveNodes          int                            `json:"anchored_live_nodes"`
	IsolatedLiveNodes          int                            `json:"isolated_live_nodes"`
	UnanchoredMetaIslandNodes  int                            `json:"unanchored_meta_island_nodes"`
	DanglingMergedNodes        int                            `json:"dangling_merged_nodes"`
	NodeConnectedRate          float64                        `json:"node_connected_rate"`
	NodeAnchoredRate           float64                        `json:"node_anchored_rate"`
	ConnectedComponents        int                            `json:"connected_components"`
	AnchoredComponents         int                            `json:"anchored_components"`
	LargestComponentNodes      int                            `json:"largest_component_nodes"`
	LargestComponentRatio      float64                        `json:"largest_component_ratio"`
	EffectiveEntityCount       int                            `json:"effective_entity_count"`
	MultiEdgeEntityCount       int                            `json:"multi_edge_entity_count"`
	AvgEffectiveEntityDegree   float64                        `json:"avg_effective_entity_degree"`
	MultiEdgeEntityRatio       float64                        `json:"multi_edge_entity_ratio"`
	TotalDefects               int                            `json:"total_defects"`
	DefectCounts               map[ConnectivityDefectKind]int `json:"defect_counts"`
	Defects                    []ConnectivityDefect           `json:"defects,omitempty"`
	CanonicalWinnerByNode      map[string]string              `json:"canonical_winner_by_node,omitempty"`
	AliveCanonicalAndConnected map[string]bool                `json:"alive_canonical_and_connected,omitempty"`
}

func normGraphID(strID string, numID uint64) string {
	s := strings.TrimSpace(strID)
	if s != "" {
		return s
	}
	if numID > 0 {
		return strconv.FormatUint(numID, 10)
	}
	return ""
}

// HasUnresolvedLocalRef reports whether s contains an unresolved batch-local reference (@1, @2)
// or template placeholder (#<id>, #<...>).
func HasUnresolvedLocalRef(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if strings.Contains(s, "#<") {
		return true
	}
	for _, tok := range strings.Fields(s) {
		tok = strings.Trim(tok, "[](){},.;:!?\"'")
		if strings.HasPrefix(tok, "@") && len(tok) > 1 {
			allDigits := true
			for _, r := range tok[1:] {
				if !unicode.IsDigit(r) {
					allDigits = false
					break
				}
			}
			if allDigits {
				return true
			}
		}
	}
	return false
}

// ExtractInlineNodeRefs extracts explicit numeric node references from a literal string:
//  1. Bracketed node references `[#123]` anywhere in the string;
//  2. Standalone bare `#123` when the entire trimmed string is a single `#<digits>` token (e.g. "#123").
//
// Natural-language ordinals inside phrases (e.g. "ranked #1 in the tournament", "song #2", "C#10", "#ff00aa")
// are intentionally excluded so they are never confused with graph node pointers.
func ExtractInlineNodeRefs(s string) []string {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "#") {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	addRef := func(raw string) {
		numStr := strings.TrimLeft(raw, "0")
		if numStr != "" && !seen[numStr] {
			seen[numStr] = true
			out = append(out, numStr)
		}
	}

	// Case 1: entire trimmed string is "#<digits>"
	if strings.HasPrefix(s, "#") && len(s) > 1 {
		allDigits := true
		for _, r := range s[1:] {
			if r < '0' || r > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			addRef(s[1:])
			return out
		}
	}

	// Case 2: bracketed "[#<digits>]" anywhere in s
	runes := []rune(s)
	n := len(runes)
	for i := 0; i+3 < n; i++ {
		if runes[i] != '[' || runes[i+1] != '#' {
			continue
		}
		j := i + 2
		for j < n && runes[j] >= '0' && runes[j] <= '9' {
			j++
		}
		if j > i+2 && j < n && runes[j] == ']' {
			addRef(string(runes[i+2 : j]))
			i = j
		}
	}
	return out
}

// ResolveSameAsCanonicalMap resolves multi-hop active `same_as` equivalence chains (LHS=loser -> RHS=winner)
// into a deterministic map `canonOf[nodeID] = ultimateWinnerID`, while detecting any directed or undirected cycles.
func ResolveSameAsCanonicalMap(edges []MetaEdge) (canonOf map[string]string, cycles [][]string) {
	canonOf = make(map[string]string)
	nextOut := make(map[string][]string)
	var activeEdges [][2]string
	for _, e := range edges {
		if !strings.EqualFold(strings.TrimSpace(e.Status), "active") {
			continue
		}
		if NormalizeMetaKind(e.MetaKind) != "same_as" {
			continue
		}
		lhs := strings.TrimSpace(e.LHSID)
		rhs := strings.TrimSpace(e.RHSID)
		if lhs == "" || rhs == "" {
			continue
		}
		if lhs == rhs {
			cycles = append(cycles, []string{lhs, rhs})
			continue
		}
		nextOut[lhs] = append(nextOut[lhs], rhs)
		activeEdges = append(activeEdges, [2]string{lhs, rhs})
	}
	if len(activeEdges) == 0 {
		return canonOf, cycles
	}

	// 1. Detect directed cycles via deterministic 3-color DFS
	dirCycles := DetectDirectedCycles(activeEdges)
	cycles = append(cycles, dirCycles...)
	inCycleNode := make(map[string]bool)
	for _, cyc := range cycles {
		for _, id := range cyc {
			inCycleNode[id] = true
		}
	}

	// 2. Detect undirected multi-path / diamond / multi-winner collisions via Union-Find
	ufParent := make(map[string]string)
	var ufFind func(string) string
	ufFind = func(x string) string {
		p, ok := ufParent[x]
		if !ok {
			ufParent[x] = x
			return x
		}
		if p != x {
			ufParent[x] = ufFind(p)
		}
		return ufParent[x]
	}
	for _, e := range activeEdges {
		rl, rr := ufFind(e[0]), ufFind(e[1])
		if rl == rr {
			if !inCycleNode[e[0]] && !inCycleNode[e[1]] {
				cycles = append(cycles, []string{e[0], e[1]})
			}
			continue
		}
		// Orient union toward RHS (winner)
		ufParent[rl] = rr
	}

	// 3. Resolve deterministic canonical winner along directed LHS -> RHS chain (bounded hops)
	allNodes := make([]string, 0, len(ufParent))
	for id := range ufParent {
		allNodes = append(allNodes, id)
	}
	sort.Strings(allNodes)
	for _, id := range allNodes {
		cur := id
		visited := map[string]bool{cur: true}
		for step := 0; step < 16; step++ {
			outs := nextOut[cur]
			if len(outs) == 0 {
				break
			}
			nxt := outs[0]
			if visited[nxt] {
				break
			}
			visited[nxt] = true
			cur = nxt
		}
		if cur != id {
			canonOf[id] = cur
		} else if root := ufFind(id); root != id {
			canonOf[id] = root
		}
	}
	return canonOf, cycles
}

// DetectDirectedCycles performs a deterministic 3-color depth-first search over directed edges (u -> v)
// and returns all elementary cycle paths discovered.
func DetectDirectedCycles(edges [][2]string) [][]string {
	if len(edges) == 0 {
		return nil
	}
	adj := make(map[string][]string)
	nodeSet := make(map[string]bool)
	var cycles [][]string
	seenSelf := make(map[string]bool)
	for _, e := range edges {
		u, v := strings.TrimSpace(e[0]), strings.TrimSpace(e[1])
		if u == "" || v == "" {
			continue
		}
		if u == v {
			if !seenSelf[u] {
				seenSelf[u] = true
				cycles = append(cycles, []string{u, u})
			}
			continue
		}
		adj[u] = append(adj[u], v)
		nodeSet[u] = true
		nodeSet[v] = true
	}
	nodes := make([]string, 0, len(nodeSet))
	for id := range nodeSet {
		nodes = append(nodes, id)
	}
	sort.Strings(nodes)
	for u := range adj {
		sort.Strings(adj[u])
	}

	const (
		colorWhite uint8 = 0
		colorGray  uint8 = 1
		colorBlack uint8 = 2
	)
	color := make(map[string]uint8, len(nodes))
	stack := make([]string, 0, 16)
	stackPos := make(map[string]int, 16)

	var dfs func(u string)
	dfs = func(u string) {
		color[u] = colorGray
		stackPos[u] = len(stack)
		stack = append(stack, u)
		for _, v := range adj[u] {
			switch color[v] {
			case colorWhite:
				dfs(v)
			case colorGray:
				if pos, ok := stackPos[v]; ok && pos >= 0 && pos < len(stack) {
					cyc := make([]string, len(stack)-pos+1)
					copy(cyc, stack[pos:])
					cyc[len(cyc)-1] = v
					cycles = append(cycles, cyc)
				}
			}
		}
		stack = stack[:len(stack)-1]
		delete(stackPos, u)
		color[u] = colorBlack
	}

	for _, u := range nodes {
		if color[u] == colorWhite {
			dfs(u)
		}
	}
	return cycles
}

// VerifyGraphConnectivity performs a comprehensive, scenario-complete node and edge connectivity verification
// over a memory graph snapshot. It validates:
//  1. structured memory assertion node & 5-slot foreign keys (Subject, Rel, Object, Holder, Incarnation) + SX/XO/Gap exemptions;
//  2. Inline #<id> proposition references and @k / #< placeholder leaks in literals;
//  3. N-ary Participant hyperedges (AssertionNodeID, TargetID, RoleID);
//  4. Companion ledger rows (Occurrence / Restate) and NodeAliases;
//  5. MetaEdges (same_as, subtype_of, inverse_of, supersedes, derived_from, conflicts_with):
//     orphan endpoints, self-loops, same_as equivalence convergence, merged-node reachability, and subtype_of DAG acyclicity;
//  6. Whole-graph connected component decomposition, factual anchoring (every component must be anchored by at least
//     one assertion unless exempted as a pre-registered root), and canonical effective entity degree / hub metrics.
func VerifyGraphConnectivity(in GraphConnectivityInput) GraphConnectivityReport {
	maxSamples := in.MaxDefectSamples
	if maxSamples <= 0 {
		maxSamples = 64
	}
	rep := GraphConnectivityReport{
		DefectCounts:               make(map[ConnectivityDefectKind]int),
		CanonicalWinnerByNode:      make(map[string]string),
		AliveCanonicalAndConnected: make(map[string]bool),
	}
	addDefect := func(kind ConnectivityDefectKind, nodeID, edgeID, slot, target, msg string) {
		rep.TotalDefects++
		rep.DefectCounts[kind]++
		if len(rep.Defects) < maxSamples {
			rep.Defects = append(rep.Defects, ConnectivityDefect{
				Kind:    kind,
				NodeID:  nodeID,
				EdgeID:  edgeID,
				Slot:    slot,
				Target:  target,
				Message: msg,
			})
		}
	}

	// 1. Index all nodes by normalized ID
	nodeByID := make(map[string]Node, len(in.Nodes))
	nodeOrder := make([]string, 0, len(in.Nodes))
	for _, rawN := range in.Nodes {
		id := normGraphID(rawN.ID, rawN.NumID)
		if id == "" {
			continue
		}
		rawN.ID = id
		rawN.Kind = strings.ToLower(strings.TrimSpace(rawN.Kind))
		rawN.Status = strings.ToLower(strings.TrimSpace(rawN.Status))
		if rawN.Status == "" {
			rawN.Status = "active"
		}
		nodeByID[id] = rawN
		nodeOrder = append(nodeOrder, id)
		rep.TotalNodes++
		switch rawN.Status {
		case "retired":
			rep.RetiredNodes++
		case "merged":
			rep.MergedNodes++
		default:
			rep.LiveNodes++
		}
	}
	sort.Strings(nodeOrder)

	// Union-Find for whole-graph undirected connectivity over non-retired nodes
	ufParent := make(map[string]string, len(nodeByID))
	for id, n := range nodeByID {
		if n.Status != "retired" {
			ufParent[id] = id
		}
	}
	var ufFind func(string) string
	ufFind = func(x string) string {
		p, ok := ufParent[x]
		if !ok {
			return ""
		}
		if p != x {
			ufParent[x] = ufFind(p)
		}
		return ufParent[x]
	}
	ufUnion := func(a, b string) {
		if a == "" || b == "" || a == b {
			return
		}
		ra, rb := ufFind(a), ufFind(b)
		if ra == "" || rb == "" || ra == rb {
			return
		}
		if ra < rb {
			ufParent[rb] = ra
		} else {
			ufParent[ra] = rb
		}
	}

	// Track node incident edge counts across channels
	incidentTotal := make(map[string]int, len(nodeByID))
	directFactAnchored := make(map[string]bool, len(nodeByID))
	activeFactAnchored := make(map[string]bool, len(nodeByID))

	// Index optional extended assertion refs (e.g. incarnation_id)
	incByAssertID := make(map[string]string, len(in.AssertionExtRefs))
	for _, ext := range in.AssertionExtRefs {
		aid := strings.TrimSpace(ext.AssertionNodeID)
		inc := strings.TrimSpace(ext.IncarnationID)
		if aid != "" && inc != "" {
			incByAssertID[aid] = inc
		}
	}

	// 2. Resolve and verify MetaEdges (same_as, subtype_of, inverse_of, supersedes, derived_from, conflicts_with)
	canonOf, sameAsCycles := ResolveSameAsCanonicalMap(in.MetaEdges)
	rep.CanonicalWinnerByNode = canonOf
	for _, cyc := range sameAsCycles {
		addDefect(DefectSameAsCycleOrInvalid, cyc[0], "", "same_as", strings.Join(cyc, "->"),
			fmt.Sprintf("same_as equivalence cycle or multi-path collision: %s", strings.Join(cyc, " -> ")))
	}

	canon := func(id string) string {
		if c, ok := canonOf[id]; ok && c != "" {
			return c
		}
		return id
	}

	var subtypeEdges [][2]string
	var partOfEdges [][2]string
	var causesEdges [][2]string
	hasActiveSameAsOut := make(map[string]bool)
	for idx, me := range in.MetaEdges {
		edgeID := strconv.FormatInt(me.ID, 10)
		if me.ID == 0 {
			edgeID = fmt.Sprintf("meta#%d", idx+1)
		}
		status := strings.ToLower(strings.TrimSpace(me.Status))
		if status == "" {
			status = "active"
		}
		if status == "reverted" || status == "split" {
			continue
		}
		kind := NormalizeMetaKind(me.MetaKind)
		lhs := strings.TrimSpace(me.LHSID)
		rhs := strings.TrimSpace(me.RHSID)
		if lhs == "" || rhs == "" {
			addDefect(DefectOrphanMetaEdge, lhs, edgeID, kind, rhs, "meta edge has empty lhs_id or rhs_id")
			continue
		}
		if lhs == rhs {
			addDefect(DefectMetaSelfLoop, lhs, edgeID, kind, rhs, fmt.Sprintf("%s meta edge #%s is a self-loop on node %s", kind, edgeID, lhs))
			continue
		}
		ln, lOK := nodeByID[lhs]
		rn, rOK := nodeByID[rhs]
		if !lOK || !rOK {
			addDefect(DefectOrphanMetaEdge, lhs, edgeID, kind, rhs,
				fmt.Sprintf("%s meta edge #%s references non-existent node (lhs_exists=%v rhs_exists=%v)", kind, edgeID, lOK, rOK))
			continue
		}
		if status == "active" && (kind == "same_as" || kind == "subtype_of" || kind == "part_of" || kind == "inverse_of" || kind == "conflicts_with" || kind == "causes") {
			if ln.Status == "retired" || rn.Status == "retired" {
				addDefect(DefectOrphanMetaEdge, lhs, edgeID, kind, rhs,
					fmt.Sprintf("active %s meta edge #%s endpoints cannot be retired (lhs=%s:%s rhs=%s:%s)", kind, edgeID, lhs, ln.Status, rhs, rn.Status))
				continue
			}
		}

		if status == "active" {
			incidentTotal[lhs]++
			incidentTotal[rhs]++
			ufUnion(lhs, rhs)
			switch kind {
			case "same_as":
				hasActiveSameAsOut[lhs] = true
				if ln.Kind != "" && rn.Kind != "" && ln.Kind != rn.Kind {
					addDefect(DefectSameAsCycleOrInvalid, lhs, edgeID, "same_as.kind", rhs,
						fmt.Sprintf("same_as edge #%s merges incompatible node kinds (%s[%s] -> %s[%s])", edgeID, lhs, ln.Kind, rhs, rn.Kind))
				}
				if ln.Status != "merged" {
					addDefect(DefectSameAsCycleOrInvalid, lhs, edgeID, "same_as.lhs_status", rhs,
						fmt.Sprintf("active same_as edge #%s loser node %s has status %q (expected 'merged')", edgeID, lhs, ln.Status))
				}
			case "subtype_of":
				subtypeEdges = append(subtypeEdges, [2]string{lhs, rhs})
			case "part_of":
				partOfEdges = append(partOfEdges, [2]string{lhs, rhs})
			case "causes":
				causesEdges = append(causesEdges, [2]string{lhs, rhs})
			}
		}
	}

	// Check subtype_of partial-order DAG for directed cycles
	if subCycles := DetectDirectedCycles(subtypeEdges); len(subCycles) > 0 {
		for _, cyc := range subCycles {
			addDefect(DefectHierarchyCycle, cyc[0], "", "subtype_of", strings.Join(cyc, "->"),
				fmt.Sprintf("subtype_of hierarchy cycle detected: %s", strings.Join(cyc, " -> ")))
		}
	}
	// Check part_of meronymic DAG for directed cycles
	if partCycles := DetectDirectedCycles(partOfEdges); len(partCycles) > 0 {
		for _, cyc := range partCycles {
			addDefect(DefectHierarchyCycle, cyc[0], "", "part_of", strings.Join(cyc, "->"),
				fmt.Sprintf("part_of meronymy cycle detected: %s", strings.Join(cyc, " -> ")))
		}
	}
	// Check causes causal DAG for directed cycles
	if causeCycles := DetectDirectedCycles(causesEdges); len(causeCycles) > 0 {
		for _, cyc := range causeCycles {
			addDefect(DefectHierarchyCycle, cyc[0], "", "causes", strings.Join(cyc, "->"),
				fmt.Sprintf("causes causal cycle detected: %s", strings.Join(cyc, " -> ")))
		}
	}

	// Check every `merged` node has a valid active same_as path to a live (`active`/`provisional`) winner
	for _, id := range nodeOrder {
		n := nodeByID[id]
		if n.Status != "merged" {
			continue
		}
		winnerID := canonOf[id]
		wn, wOK := nodeByID[winnerID]
		if !hasActiveSameAsOut[id] || winnerID == "" || !wOK || (wn.Status != "active" && wn.Status != "provisional") {
			rep.DanglingMergedNodes++
			addDefect(DefectSameAsCycleOrInvalid, id, "", "merged_node", winnerID,
				fmt.Sprintf("merged node %s (%q) lacks an active same_as path to a live winner (resolved=%q)", id, n.Label, winnerID))
		}
	}

	// 3. Verify Assertions & structured memory assertion + Holder + Incarnation + Inline #<id> connectivity
	effectiveEntityEdges := make(map[string]int)
	touchEntity := func(rawID string) {
		if rawID == "" {
			return
		}
		cid := canon(rawID)
		if n, ok := nodeByID[cid]; ok && (n.Kind == "referent" || n.Kind == "expression" || n.Kind == "incarnation" || n.Kind == "role") && n.Status != "retired" {
			effectiveEntityEdges[cid]++
		} else if nOrig, okOrig := nodeByID[rawID]; okOrig && (nOrig.Kind == "referent" || nOrig.Kind == "expression" || nOrig.Kind == "incarnation" || nOrig.Kind == "role") && nOrig.Status != "retired" {
			effectiveEntityEdges[rawID]++
		}
	}
	anchorEndpoint := func(aID, slotID string, isHist, isAct, isEntity bool) {
		incidentTotal[slotID]++
		ufUnion(aID, slotID)
		if isHist {
			directFactAnchored[slotID] = true
			directFactAnchored[canon(slotID)] = true
		}
		if isAct {
			activeFactAnchored[slotID] = true
			activeFactAnchored[canon(slotID)] = true
			if isEntity {
				touchEntity(slotID)
			}
		}
	}

	for idx, a := range in.Assertions {
		rep.TotalAssertions++
		aID := normGraphID(a.NodeID, a.NumNodeID)
		sID := normGraphID(a.SubjectID, a.NumSubjectID)
		rID := normGraphID(a.RelID, a.NumRelID)
		oID := normGraphID(a.ObjectID, a.NumObjectID)
		hID := normGraphID(a.HolderID, a.NumHolderID)
		incID := strings.TrimSpace(incByAssertID[aID])

		status := strings.ToLower(strings.TrimSpace(a.Status))
		if status == "" {
			status = "active"
		}
		isActive := status == "active" && !a.Bare
		if isActive {
			rep.ActiveAssertions++
		}
		isHistoricalValid := status == "active" || status == "superseded" || status == "expired" || status == "disputed"

		// (a) Assertion node row itself must exist and have kind == "assertion"
		if aID == "" {
			addDefect(DefectMissingAssertionNode, fmt.Sprintf("assertion#%d", idx+1), "", "node_id", "", "assertion has empty node_id")
			continue
		}
		an, aOK := nodeByID[aID]
		if !aOK || an.Kind != "assertion" || (isActive && an.Status == "retired") {
			addDefect(DefectMissingAssertionNode, aID, aID, "node_id", aID,
				fmt.Sprintf("assertion %s node row invalid (exists=%v kind=%q status=%q)", aID, aOK, an.Kind, an.Status))
		} else {
			incidentTotal[aID]++
			if isHistoricalValid {
				directFactAnchored[aID] = true
			}
			if isActive {
				activeFactAnchored[aID] = true
			}
		}

		// (b) Subject slot check (exempt when SubjectAbsent==true or SubjectGap != "")
		if sID == "" {
			if !a.SubjectAbsent && strings.TrimSpace(a.SubjectGap) == "" {
				addDefect(DefectOrphanEndpoint, aID, aID, "subject_id", "",
					fmt.Sprintf("assertion %s has empty subject_id without subject_absent or subject_gap declaration", aID))
			}
		} else if sn, sOK := nodeByID[sID]; !sOK || (isActive && sn.Status == "retired") {
			addDefect(DefectOrphanEndpoint, aID, aID, "subject_id", sID,
				fmt.Sprintf("assertion %s subject_id=%s unresolvable or retired (exists=%v status=%q)", aID, sID, sOK, sn.Status))
		} else {
			if sn.Kind == "relation_type" {
				addDefect(DefectInvalidEndpointKind, aID, aID, "subject_id", sID,
					fmt.Sprintf("assertion %s subject_id=%s points to relation_type node %q", aID, sID, sn.Label))
			}
			anchorEndpoint(aID, sID, isHistoricalValid, isActive, true)
		}

		// (c) Relation predicate slot check (mandatory on every assertion)
		if rID == "" {
			addDefect(DefectOrphanEndpoint, aID, aID, "rel_id", "", fmt.Sprintf("assertion %s has empty rel_id", aID))
		} else if rn, rOK := nodeByID[rID]; !rOK || (isActive && rn.Status == "retired") {
			addDefect(DefectOrphanEndpoint, aID, aID, "rel_id", rID,
				fmt.Sprintf("assertion %s rel_id=%s unresolvable or retired (exists=%v status=%q)", aID, rID, rOK, rn.Status))
		} else {
			if rn.Kind != "" && rn.Kind != "relation_type" {
				addDefect(DefectInvalidEndpointKind, aID, aID, "rel_id", rID,
					fmt.Sprintf("assertion %s rel_id=%s has kind=%q (expected 'relation_type')", aID, rID, rn.Kind))
			}
			anchorEndpoint(aID, rID, isHistoricalValid, isActive, false)
		}

		// (d) Object slot check (optional on binary SX or object_gap; when present must exist & not be relation_type)
		if oID != "" {
			if on, oOK := nodeByID[oID]; !oOK || (isActive && on.Status == "retired") {
				addDefect(DefectOrphanEndpoint, aID, aID, "object_id", oID,
					fmt.Sprintf("assertion %s object_id=%s unresolvable or retired (exists=%v status=%q)", aID, oID, oOK, on.Status))
			} else {
				if on.Kind == "relation_type" {
					addDefect(DefectInvalidEndpointKind, aID, aID, "object_id", oID,
						fmt.Sprintf("assertion %s object_id=%s points to relation_type node %q", aID, oID, on.Label))
				}
				if sID != "" {
					ufUnion(sID, oID)
				}
				anchorEndpoint(aID, oID, isHistoricalValid, isActive, true)
			}
		}

		// (e) Holder slot check
		if hID != "" {
			if hn, hOK := nodeByID[hID]; !hOK || (isActive && hn.Status == "retired") {
				addDefect(DefectOrphanEndpoint, aID, aID, "holder_id", hID,
					fmt.Sprintf("assertion %s holder_id=%s unresolvable or retired (exists=%v status=%q)", aID, hID, hOK, hn.Status))
			} else {
				if hn.Kind == "relation_type" || hn.Kind == "assertion" {
					addDefect(DefectInvalidEndpointKind, aID, aID, "holder_id", hID,
						fmt.Sprintf("assertion %s holder_id=%s has invalid kind=%q", aID, hID, hn.Kind))
				}
				anchorEndpoint(aID, hID, isHistoricalValid, isActive, false)
			}
		}

		// (f) Incarnation slot check (when bound)
		if incID != "" && incID != "0" {
			if incN, incOK := nodeByID[incID]; !incOK || (isActive && incN.Status == "retired") {
				addDefect(DefectOrphanEndpoint, aID, aID, "incarnation_id", incID,
					fmt.Sprintf("assertion %s incarnation_id=%s unresolvable or retired (exists=%v status=%q)", aID, incID, incOK, incN.Status))
			} else {
				if incN.Kind != "" && incN.Kind != "incarnation" {
					addDefect(DefectInvalidEndpointKind, aID, aID, "incarnation_id", incID,
						fmt.Sprintf("assertion %s incarnation_id=%s has kind=%q (expected 'incarnation')", aID, incID, incN.Kind))
				}
				anchorEndpoint(aID, incID, isHistoricalValid, isActive, false)
			}
		}

		// (g) Check for unresolved @k / #< placeholders and verify inline #<id> references in literals
		for _, pair := range [][2]string{
			{"subject_literal", a.SubjectLiteral},
			{"object_literal", a.ObjectLiteral},
			{"subject_label", nodeByID[sID].Label},
			{"object_label", nodeByID[oID].Label},
		} {
			if HasUnresolvedLocalRef(pair[1]) {
				addDefect(DefectPhantomRefLeak, aID, aID, pair[0], pair[1],
					fmt.Sprintf("assertion %s %s contains unresolved placeholder reference in %q", aID, pair[0], pair[1]))
			}
		}
		for _, refID := range ExtractInlineNodeRefs(a.ObjectLiteral) {
			if rn, rOK := nodeByID[refID]; !rOK || (isActive && rn.Status == "retired") {
				addDefect(DefectPhantomRefLeak, aID, aID, "object_literal.#id", refID,
					fmt.Sprintf("assertion %s object_literal inline ref #%s targets missing or retired node", aID, refID))
			} else {
				anchorEndpoint(aID, refID, isHistoricalValid, isActive, true)
			}
		}
	}

	// 4. Verify Participant hyperedges
	for idx, p := range in.Participants {
		aID := strings.TrimSpace(p.AssertionNodeID)
		tID := strings.TrimSpace(p.TargetID)
		roleID := strings.TrimSpace(p.RoleID)
		edgeID := fmt.Sprintf("part:%s#%d", aID, idx+1)

		an, aOK := nodeByID[aID]
		if aID == "" || !aOK || an.Kind != "assertion" {
			addDefect(DefectOrphanParticipant, aID, edgeID, "assertion_node_id", aID,
				fmt.Sprintf("participant row %s references missing or non-assertion node %q", edgeID, aID))
			continue
		}
		parentActive := activeFactAnchored[aID]
		parentHist := directFactAnchored[aID]
		if tID == "" && strings.TrimSpace(p.Literal) == "" {
			addDefect(DefectOrphanParticipant, aID, edgeID, "target_id", "",
				fmt.Sprintf("participant row %s has both empty target_id and empty literal", edgeID))
		}
		if tID != "" && tID != "0" {
			if tn, tOK := nodeByID[tID]; !tOK || (parentActive && tn.Status == "retired") {
				addDefect(DefectOrphanParticipant, aID, edgeID, "target_id", tID,
					fmt.Sprintf("participant row %s target_id=%s missing or retired (exists=%v status=%q)", edgeID, tID, tOK, tn.Status))
			} else {
				anchorEndpoint(aID, tID, parentHist, parentActive, true)
			}
		}
		if roleID != "" && roleID != "0" {
			if rn, rOK := nodeByID[roleID]; !rOK || (parentActive && rn.Status == "retired") {
				addDefect(DefectOrphanParticipant, aID, edgeID, "role_id", roleID,
					fmt.Sprintf("participant row %s role_id=%s missing or retired (exists=%v status=%q)", edgeID, roleID, rOK, rn.Status))
			} else {
				anchorEndpoint(aID, roleID, parentHist, parentActive, false)
			}
		}
	}

	// 5. Verify Companion Ledger rows (Occurrence / Restate)
	for idx, lr := range in.LedgerRows {
		aID := strings.TrimSpace(lr.AssertionNodeID)
		edgeID := fmt.Sprintf("ledger:%s#%d", lr.Kind, idx+1)
		an, aOK := nodeByID[aID]
		if aID == "" || !aOK || an.Kind != "assertion" || an.Status == "retired" {
			addDefect(DefectOrphanLedgerRef, aID, edgeID, "assertion_node_id", aID,
				fmt.Sprintf("ledger (%s) row %s references missing, retired, or non-assertion node %q", lr.Kind, edgeID, aID))
			continue
		}
		incidentTotal[aID]++
	}

	// 6. Verify NodeAliases
	for idx, al := range in.Aliases {
		if strings.EqualFold(strings.TrimSpace(al.Source), "reverted") {
			continue
		}
		nid := normGraphID(al.NodeID, al.NumNodeID)
		edgeID := fmt.Sprintf("alias#%d", idx+1)
		n, ok := nodeByID[nid]
		if nid == "" || !ok || n.Status == "retired" || n.Kind == "assertion" {
			addDefect(DefectOrphanAlias, nid, edgeID, "node_id", al.Alias,
				fmt.Sprintf("alias %q binds to missing, retired, or assertion node %q (exists=%v kind=%q status=%q)", al.Alias, nid, ok, n.Kind, n.Status))
			continue
		}
		if strings.TrimSpace(al.Alias) != "" && strings.EqualFold(strings.TrimSpace(al.Alias), strings.TrimSpace(n.Label)) {
			addDefect(DefectOrphanAlias, nid, edgeID, "alias_self_loop", al.Alias,
				fmt.Sprintf("alias %q duplicates node %s primary label %q", al.Alias, nid, n.Label))
		}
	}

	// 7. Propagate structural edges (same_as, subtype_of, part_of, inverse_of) into effectiveEntityEdges & activeFactAnchored
	// We iterate up to 4 hops so multi-level Hasse concept hierarchies, dual-track spatial trees,
	// incarnation lineages, and multi-hop same_as equivalence clusters inherit factual anchoring.
	for hop := 0; hop < 4; hop++ {
		changed := false
		for _, me := range in.MetaEdges {
			if !strings.EqualFold(strings.TrimSpace(me.Status), "active") {
				continue
			}
			kind := NormalizeMetaKind(me.MetaKind)
			if kind != "same_as" && kind != "subtype_of" && kind != "part_of" && kind != "inverse_of" {
				continue
			}
			lhs := strings.TrimSpace(me.LHSID)
			rhs := strings.TrimSpace(me.RHSID)
			if lhs == "" || rhs == "" || lhs == rhs {
				continue
			}
			if activeFactAnchored[lhs] && !activeFactAnchored[rhs] {
				activeFactAnchored[rhs] = true
				changed = true
			}
			if activeFactAnchored[rhs] && !activeFactAnchored[lhs] {
				activeFactAnchored[lhs] = true
				changed = true
			}
			if directFactAnchored[lhs] && !directFactAnchored[rhs] {
				directFactAnchored[rhs] = true
				changed = true
			}
			if directFactAnchored[rhs] && !directFactAnchored[lhs] {
				directFactAnchored[lhs] = true
				changed = true
			}
		}
		if !changed {
			break
		}
	}

	// Add active structural edges (subtype_of / part_of / inverse_of) to effectiveEntityEdges for anchored entities
	for _, me := range in.MetaEdges {
		if !strings.EqualFold(strings.TrimSpace(me.Status), "active") {
			continue
		}
		kind := NormalizeMetaKind(me.MetaKind)
		if kind != "subtype_of" && kind != "part_of" && kind != "inverse_of" {
			continue
		}
		lhs := strings.TrimSpace(me.LHSID)
		rhs := strings.TrimSpace(me.RHSID)
		if lhs == "" || rhs == "" || lhs == rhs {
			continue
		}
		if activeFactAnchored[lhs] || activeFactAnchored[rhs] {
			touchEntity(lhs)
			touchEntity(rhs)
		}
	}

	// 8. Component decomposition & isolated / unanchored node detection
	compSize := make(map[string]int)
	compHasFact := make(map[string]bool)
	for _, id := range nodeOrder {
		n := nodeByID[id]
		if n.Status == "retired" {
			continue
		}
		root := ufFind(id)
		if root == "" {
			continue
		}
		compSize[root]++
		if directFactAnchored[id] {
			compHasFact[root] = true
		}
	}

	rep.ConnectedComponents = len(compSize)
	for root, sz := range compSize {
		if compHasFact[root] {
			rep.AnchoredComponents++
		}
		if sz > rep.LargestComponentNodes {
			rep.LargestComponentNodes = sz
		}
	}
	nonRetiredTotal := rep.LiveNodes + rep.MergedNodes
	if nonRetiredTotal > 0 {
		rep.LargestComponentRatio = float64(rep.LargestComponentNodes) / float64(nonRetiredTotal)
	}

	for _, id := range nodeOrder {
		n := nodeByID[id]
		if n.Status == "retired" {
			continue
		}
		if activeFactAnchored[id] || activeFactAnchored[canon(id)] {
			rep.AliveCanonicalAndConnected[id] = true
		}
		if n.Status == "merged" {
			continue
		}
		// Live node (status == "active" or "provisional")
		root := ufFind(id)
		deg := incidentTotal[id]
		anchored := compHasFact[root]
		isPreRegRoot := in.AllowPreRegisteredRoots && (strings.EqualFold(n.Origin, "roster") || strings.EqualFold(n.Origin, "world") || n.Kind == "incarnation")

		if deg > 0 {
			rep.ConnectedLiveNodes++
		}
		if deg > 0 && anchored {
			rep.AnchoredLiveNodes++
		} else if isPreRegRoot {
			if deg == 0 {
				rep.ConnectedLiveNodes++
			}
			rep.AnchoredLiveNodes++
		} else if deg == 0 {
			rep.IsolatedLiveNodes++
			addDefect(DefectIsolatedNode, id, "", "zero_degree", n.Label,
				fmt.Sprintf("live %s node %s (%q) has 0 incident edges across all graph channels", n.Kind, id, n.Label))
		} else if !anchored {
			rep.UnanchoredMetaIslandNodes++
			addDefect(DefectIsolatedNode, id, "", "unanchored_island", n.Label,
				fmt.Sprintf("live %s node %s (%q) belongs to a meta-only island component without any supporting assertion", n.Kind, id, n.Label))
		}
	}

	if rep.LiveNodes > 0 {
		rep.NodeConnectedRate = float64(rep.ConnectedLiveNodes) / float64(rep.LiveNodes)
		rep.NodeAnchoredRate = float64(rep.AnchoredLiveNodes) / float64(rep.LiveNodes)
	} else {
		rep.NodeConnectedRate = 1.0
		rep.NodeAnchoredRate = 1.0
	}

	// 9. Aggregate canonical effective entity degree & multi-edge hub ratio
	rep.EffectiveEntityCount = len(effectiveEntityEdges)
	if rep.EffectiveEntityCount > 0 {
		totalDeg := 0
		multiCnt := 0
		for _, d := range effectiveEntityEdges {
			totalDeg += d
			if d >= 2 {
				multiCnt++
			}
		}
		rep.MultiEdgeEntityCount = multiCnt
		rep.AvgEffectiveEntityDegree = float64(totalDeg) / float64(rep.EffectiveEntityCount)
		rep.MultiEdgeEntityRatio = float64(multiCnt) / float64(rep.EffectiveEntityCount)
	}

	rep.Clean = rep.TotalDefects == 0
	return rep
}

// ComputeFullConnectivityMetrics computes enriched graph connectivity and reuse metrics that account for
// structured memory assertions, inline #<id> references, N-ary participant hyperedges, and active meta edges (same_as, subtype_of, inverse_of).
func ComputeFullConnectivityMetrics(in GraphConnectivityInput) ConnectivityMetrics {
	nodeKinds := make(map[string]string, len(in.Nodes))
	nodeLabels := make(map[string]string, len(in.Nodes))
	for _, n := range in.Nodes {
		id := normGraphID(n.ID, n.NumID)
		if id != "" {
			nodeKinds[id] = strings.ToLower(strings.TrimSpace(n.Kind))
			nodeLabels[id] = n.Label
		}
	}
	base := ComputeConnectivityMetrics(in.Assertions, nodeKinds, nodeLabels)
	rep := VerifyGraphConnectivity(in)
	if rep.EffectiveEntityCount > 0 {
		base.EntityNodeCount = rep.EffectiveEntityCount
		base.MultiEdgeEntityCount = rep.MultiEdgeEntityCount
		base.AvgEntityDegree = rep.AvgEffectiveEntityDegree
		base.MultiEdgeEntityRatio = rep.MultiEdgeEntityRatio
	}
	return base
}
