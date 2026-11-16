// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"testing"
)

func TestExtractInlineNodeRefsAndUnresolvedPlaceholders(t *testing.T) {
	refs := ExtractInlineNodeRefs("Alice said [#101] and [#202], not ranked #1 or C#10 or #ff00aa or [#000].")
	if len(refs) != 2 || refs[0] != "101" || refs[1] != "202" {
		t.Fatalf("unexpected ExtractInlineNodeRefs for bracketed refs: %v", refs)
	}
	standalone := ExtractInlineNodeRefs("  #303  ")
	if len(standalone) != 1 || standalone[0] != "303" {
		t.Fatalf("unexpected ExtractInlineNodeRefs for standalone ref: %v", standalone)
	}
	if !HasUnresolvedLocalRef("see @1 for details") {
		t.Fatal("expected @1 to be detected as unresolved local ref")
	}
	if !HasUnresolvedLocalRef("target #<id>") {
		t.Fatal("expected #<id> to be detected as unresolved local ref")
	}
	if HasUnresolvedLocalRef("contact user@example.com or #101") {
		t.Fatal("email address and resolved #101 must not trigger HasUnresolvedLocalRef")
	}
}

func TestVerifyGraphConnectivityCleanAllValidScenarios(t *testing.T) {
	// Scenario covering:
	//  - Standard ternary structured memory assertion (#100: Alice #1 --[visited #2]--> West Lake #3, holder=Alice #1, inc=#7)
	//  - Binary SX assertion (#101: Alice #1 --[laughed #4]--> "")
	//  - Binary XO subject_absent assertion (#102: ∅ --[rained #5]--> "heavily")
	//  - Unbound subject_gap assertion (#103: ?(referent_unresolvable) --[knocked #6]--> West Lake #3)
	//  - Proposition reference assertion (#104: Alice #1 --[remembers #8]--> #100) + inline [#101] in literal
	//  - N-ary Participant hyperedge (Assertion #100 -> Target Bob #9, Role companion #10)
	//  - Governed same_as merge: loser node #11 ("Alyce", status="merged") --same_as--> winner #1 ("Alice", status="active"),
	//    where historical assertion #105 is still attached to loser #11!
	//  - Multi-level Hasse subtype_of hierarchy: #3 ("West Lake") --subtype_of--> #12 ("Lake", pure concept parent with no direct assertion)
	//  - Pre-registered roster node #13 (origin="roster", 0 edges, exempted when AllowPreRegisteredRoots=true)
	//  - Superseded assertion #106 and retired node #99 (unreferenced by any active assertion/meta)
	in := GraphConnectivityInput{
		AllowPreRegisteredRoots: true,
		Nodes: []Node{
			{ID: "1", Kind: "referent", Label: "Alice", Status: "active"},
			{ID: "2", Kind: "relation_type", Label: "visited", Status: "active"},
			{ID: "3", Kind: "referent", Label: "West Lake", Status: "active"},
			{ID: "4", Kind: "relation_type", Label: "laughed", Status: "active"},
			{ID: "5", Kind: "relation_type", Label: "rained", Status: "active"},
			{ID: "6", Kind: "relation_type", Label: "knocked", Status: "active"},
			{ID: "7", Kind: "incarnation", Label: "trip-2026", Status: "active"},
			{ID: "8", Kind: "relation_type", Label: "remembers", Status: "active"},
			{ID: "9", Kind: "referent", Label: "Bob", Status: "active"},
			{ID: "10", Kind: "role", Label: "companion", Status: "active"},
			{ID: "11", Kind: "referent", Label: "Alyce", Status: "merged"},
			{ID: "12", Kind: "referent", Label: "Lake", Status: "active"},
			{ID: "13", Kind: "referent", Label: "Carol", Status: "provisional", Origin: "roster"},
			{ID: "99", Kind: "referent", Label: "OldRetired", Status: "retired"},
			{ID: "100", Kind: "assertion", Label: "Alice visited West Lake", Status: "active"},
			{ID: "101", Kind: "assertion", Label: "Alice laughed", Status: "active"},
			{ID: "102", Kind: "assertion", Label: "∅ rained heavily", Status: "active"},
			{ID: "103", Kind: "assertion", Label: "? knocked West Lake", Status: "active"},
			{ID: "104", Kind: "assertion", Label: "Alice remembers #100", Status: "active"},
			{ID: "105", Kind: "assertion", Label: "Alyce visited West Lake", Status: "active"},
			{ID: "106", Kind: "assertion", Label: "Old superseded fact", Status: "active"},
		},
		Assertions: []Assertion{
			{NodeID: "100", SubjectID: "1", RelID: "2", ObjectID: "3", HolderID: "1", Status: "active"},
			{NodeID: "101", SubjectID: "1", RelID: "4", HolderID: "1", Status: "active"},
			{NodeID: "102", SubjectAbsent: true, RelID: "5", ObjectLiteral: "heavily", HolderID: "1", Status: "active"},
			{NodeID: "103", SubjectGap: "referent_unresolvable", RelID: "6", ObjectID: "3", HolderID: "1", Status: "active"},
			{NodeID: "104", SubjectID: "1", RelID: "8", ObjectID: "100", ObjectLiteral: "also [#101]", HolderID: "1", Status: "active"},
			{NodeID: "105", SubjectID: "11", RelID: "2", ObjectID: "3", HolderID: "11", Status: "active"},
			{NodeID: "106", SubjectID: "1", RelID: "4", HolderID: "1", Status: "superseded"},
		},
		AssertionExtRefs: []AssertionExtRef{
			{AssertionNodeID: "100", IncarnationID: "7"},
		},
		Participants: []Participant{
			{AssertionNodeID: "100", Seq: 1, RoleID: "10", TargetID: "9"},
		},
		Aliases: []NodeAlias{
			{NodeID: "1", Alias: "Alyce", Source: "merge:11"},
			{NodeID: "99", Alias: "StaleAlias", Source: "reverted"},
		},
		MetaEdges: []MetaEdge{
			{ID: 1, MetaKind: "same_as", LHSID: "11", RHSID: "1", Status: "active"},
			{ID: 2, MetaKind: "subtype_of", LHSID: "3", RHSID: "12", Status: "active"},
			{ID: 3, MetaKind: "supersedes", LHSID: "106", RHSID: "101", Status: "active"},
		},
		LedgerRows: []RestateRow{
			{ID: 1, AssertionNodeID: "100", Kind: "occurrence"},
			{ID: 2, AssertionNodeID: "101", Kind: "mention"},
		},
	}

	rep := VerifyGraphConnectivity(in)
	if !rep.Clean || rep.TotalDefects != 0 {
		t.Fatalf("expected clean connectivity report on valid multi-scenario graph, got totalDefects=%d defects=%+v", rep.TotalDefects, rep.Defects)
	}
	if rep.NodeConnectedRate != 1.0 || rep.NodeAnchoredRate != 1.0 {
		t.Fatalf("expected 100%% connected & anchored rate, got conn=%.3f anch=%.3f", rep.NodeConnectedRate, rep.NodeAnchoredRate)
	}
	if rep.CanonicalWinnerByNode["11"] != "1" {
		t.Fatalf("expected loser #11 canonical winner to be #1, got %q", rep.CanonicalWinnerByNode["11"])
	}
	// Verify that parent concept #12 ("Lake"), participant #9 ("Bob"), incarnation #7, and merged loser #11 are all marked alive & connected
	for _, mustAlive := range []string{"1", "3", "7", "9", "10", "11", "12"} {
		if !rep.AliveCanonicalAndConnected[mustAlive] {
			t.Fatalf("expected node #%s to be marked AliveCanonicalAndConnected", mustAlive)
		}
	}
	fullMetrics := ComputeFullConnectivityMetrics(in)
	if fullMetrics.EntityNodeCount == 0 || fullMetrics.AvgEntityDegree <= 1.0 {
		t.Fatalf("unexpected ComputeFullConnectivityMetrics: %+v", fullMetrics)
	}
}

func TestVerifyGraphConnectivityDetectsAll12DefectClasses(t *testing.T) {
	in := GraphConnectivityInput{
		AllowPreRegisteredRoots: false,
		Nodes: []Node{
			{ID: "1", Kind: "referent", Label: "Alice", Status: "active"},
			{ID: "2", Kind: "relation_type", Label: "knows", Status: "active"},
			{ID: "3", Kind: "referent", Label: "RetiredBob", Status: "retired"},
			{ID: "4", Kind: "referent", Label: "NotRelation", Status: "active"},
			{ID: "5", Kind: "referent", Label: "CycleA", Status: "merged"},
			{ID: "6", Kind: "referent", Label: "CycleB", Status: "merged"},
			{ID: "7", Kind: "referent", Label: "DanglingMerged", Status: "merged"},
			{ID: "8", Kind: "referent", Label: "SubA", Status: "active"},
			{ID: "9", Kind: "referent", Label: "SubB", Status: "active"},
			{ID: "10", Kind: "referent", Label: "ZeroDegOrphan", Status: "active"},
			{ID: "11", Kind: "referent", Label: "IslandA", Status: "active"},
			{ID: "12", Kind: "referent", Label: "IslandB", Status: "active"},
			{ID: "100", Kind: "assertion", Label: "Valid node row", Status: "active"},
			{ID: "102", Kind: "referent", Label: "WrongKindForAssertion", Status: "active"},
		},
		Assertions: []Assertion{
			// #100: missing subject (no gap/absent), object points to retired #3, holder points to missing #999, literal has @1 and missing #888
			{NodeID: "100", SubjectID: "", RelID: "2", ObjectID: "3", HolderID: "999", ObjectLiteral: "see @1 and [#888]", Status: "active"},
			// #101: missing assertion node in Nodes, empty RelID
			{NodeID: "101", SubjectID: "1", RelID: "", Status: "active"},
			// #102: assertion node has kind="referent", RelID points to referent #4, SubjectID points to relation_type #2
			{NodeID: "102", SubjectID: "2", RelID: "4", Status: "active"},
		},
		AssertionExtRefs: []AssertionExtRef{
			// Incarnation points to referent #1 (wrong kind)
			{AssertionNodeID: "100", IncarnationID: "1"},
		},
		Participants: []Participant{
			// Participant target points to retired #3
			{AssertionNodeID: "100", Seq: 1, TargetID: "3", RoleID: "998"},
		},
		Aliases: []NodeAlias{
			// Alias on retired node #3
			{NodeID: "3", Alias: "Bobby", Source: "remember:nickname"},
			// Self-loop alias on #1
			{NodeID: "1", Alias: "Alice", Source: "remember:nickname"},
		},
		MetaEdges: []MetaEdge{
			// Self-loop meta edge
			{ID: 1, MetaKind: "same_as", LHSID: "1", RHSID: "1", Status: "active"},
			// Orphan meta edge
			{ID: 2, MetaKind: "subtype_of", LHSID: "1", RHSID: "777", Status: "active"},
			// same_as 2-cycle (#5 <-> #6)
			{ID: 3, MetaKind: "same_as", LHSID: "5", RHSID: "6", Status: "active"},
			{ID: 4, MetaKind: "same_as", LHSID: "6", RHSID: "5", Status: "active"},
			// subtype_of directed cycle (#8 -> #9 -> #8)
			{ID: 5, MetaKind: "subtype_of", LHSID: "8", RHSID: "9", Status: "active"},
			{ID: 6, MetaKind: "subtype_of", LHSID: "9", RHSID: "8", Status: "active"},
			// Unanchored meta-only island (#11 -> #12) with zero assertions
			{ID: 7, MetaKind: "subtype_of", LHSID: "11", RHSID: "12", Status: "active"},
		},
		LedgerRows: []RestateRow{
			{ID: 1, AssertionNodeID: "997", Kind: "occurrence"},
		},
	}

	rep := VerifyGraphConnectivity(in)
	if rep.Clean {
		t.Fatal("expected dirty graph to fail VerifyGraphConnectivity")
	}
	expectedKinds := []ConnectivityDefectKind{
		DefectMissingAssertionNode,
		DefectOrphanEndpoint,
		DefectInvalidEndpointKind,
		DefectPhantomRefLeak,
		DefectOrphanParticipant,
		DefectOrphanLedgerRef,
		DefectOrphanAlias,
		DefectOrphanMetaEdge,
		DefectMetaSelfLoop,
		DefectSameAsCycleOrInvalid,
		DefectHierarchyCycle,
		DefectIsolatedNode,
	}
	for _, k := range expectedKinds {
		if rep.DefectCounts[k] <= 0 {
			t.Errorf("expected defect kind %q to be detected (>0), got 0 (all counts=%+v)", k, rep.DefectCounts)
		}
	}
	if rep.DanglingMergedNodes == 0 {
		t.Errorf("expected DanglingMergedNodes > 0, got 0")
	}
	if rep.IsolatedLiveNodes == 0 {
		t.Errorf("expected IsolatedLiveNodes > 0, got 0")
	}
	if rep.UnanchoredMetaIslandNodes == 0 {
		t.Errorf("expected UnanchoredMetaIslandNodes > 0, got 0")
	}
}
