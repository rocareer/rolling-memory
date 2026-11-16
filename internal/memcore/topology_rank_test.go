// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import "testing"

func TestExpandDirectedHierarchyClosureBlocksSiblingPollution(t *testing.T) {
	// Graph setup:
	//   10 ("oil painting")       --subtype_of--> 100 ("painting")
	//   20 ("watercolor painting") --subtype_of--> 100 ("painting")
	//   30 ("acrylic painting")    --subtype_of--> 100 ("painting")
	//   100 ("painting")          --subtype_of--> 1000 ("art")
	//   10 ("oil painting")       --same_as-----> 11 ("oil paintings")
	edges := []HierarchyMetaEdge{
		{LHS: 10, RHS: 100, Kind: "subtype_of"},
		{LHS: 20, RHS: 100, Kind: "subtype_of"},
		{LHS: 30, RHS: 100, Kind: "subtype_of"},
		{LHS: 100, RHS: 1000, Kind: "subtype_of"},
		{LHS: 10, RHS: 11, Kind: "same_as"},
	}
	fetch := func(seedSet []uint64, limit int) ([]HierarchyMetaEdge, error) {
		inF := map[uint64]bool{}
		for _, id := range seedSet {
			inF[id] = true
		}
		var out []HierarchyMetaEdge
		for _, e := range edges {
			if inF[e.LHS] || inF[e.RHS] {
				out = append(out, e)
			}
		}
		return out, nil
	}

	// Case 1: Query hits specific child 10 ("oil painting").
	// Should expand to same_as 11 (direct equivalent) and 1-hop parent 100 ("painting"),
	// but MUST NOT fan out downward from 100 to sibling 20 ("watercolor") or 30 ("acrylic"),
	// nor ascend a 2nd hop to 1000 ("art").
	expChild, closureOnlyChild, err := ExpandDirectedHierarchyClosure([]uint64{10}, 2, 32, 256, fetch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	gotSet := map[uint64]bool{}
	for _, id := range expChild {
		gotSet[id] = true
	}
	if !gotSet[10] || !gotSet[11] || !gotSet[100] {
		t.Fatalf("expected {10, 11, 100} in expanded set, got %v", expChild)
	}
	if gotSet[20] || gotSet[30] {
		t.Fatalf("sibling pollution detected: child 10 expanded into unrelated siblings 20/30: %v", expChild)
	}
	if gotSet[1000] {
		t.Fatalf("2-hop upward hypernym drift detected: child 10 expanded to 1000: %v", expChild)
	}
	if closureOnlyChild[10] || closureOnlyChild[11] {
		t.Fatalf("direct seed 10 and same_as equivalent 11 must not be marked closureOnly: %v", closureOnlyChild)
	}
	if !closureOnlyChild[100] {
		t.Fatalf("ascended parent 100 must be marked closureOnly: %v", closureOnlyChild)
	}

	// Case 2: Query hits general parent 1000 ("art").
	// Downward instantiation (RHS -> LHS) MUST traverse 2 hops: 1000 -> 100 -> {10, 20, 30} (+ same_as 11).
	expParent, _, err := ExpandDirectedHierarchyClosure([]uint64{1000}, 2, 32, 256, fetch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	parentSet := map[uint64]bool{}
	for _, id := range expParent {
		parentSet[id] = true
	}
	for _, wantID := range []uint64{1000, 100, 10, 20, 30} {
		if !parentSet[wantID] {
			t.Fatalf("downward instantiation from 1000 missed %d, got %v", wantID, expParent)
		}
	}
}

func TestComputeGraphBridgeBoostsAndClosureBandGate(t *testing.T) {
	// Primary subject = 1 (Alice).
	// Hub 50 ("sunrise landscape") has degree 2:
	//   Row 0: Alice (1) — painted → sunrise landscape (50) [score=1.5, nonSubj=false, turn="t1"]
	//   Row 1: Melanie (2) — praised → sunrise landscape (50) [score=9.5, lex=9.5, origNonSubj=true, directXO=true, turn="t2"]
	// Unconnected background Row 2: Alice (1) — drank → coffee (60) [score=0.5, nonSubj=false, turn="t3"]
	rows := []TopologyRowView{
		{NodeID: 101, SubjectID: 1, RelID: 10, ObjectID: 50, SourceTraceHash: "t1", Status: "active", Score: 1.5, LexScore: 1.5},
		{NodeID: 102, SubjectID: 2, RelID: 11, ObjectID: 50, SourceTraceHash: "t2", Status: "active", Score: 9.5, LexScore: 9.5, NonSubj: true, OrigNonSubj: true, DirectXO: true},
		{NodeID: 103, SubjectID: 1, RelID: 12, ObjectID: 60, SourceTraceHash: "t3", Status: "active", Score: 0.5, LexScore: 0.5},
	}
	boosts := ComputeGraphBridgeBoosts(rows, map[uint64]bool{1: true}, nil)
	if boosts[0].Delta <= 0 || !boosts[0].PromoteNS {
		t.Fatalf("expected bridge row 0 (Alice -> hub 50) to receive positive topology boost and PromoteNS, got %+v", boosts[0])
	}
	if boosts[1].Delta <= 0 {
		t.Fatalf("expected bridge row 1 (Melanie -> hub 50) to receive positive topology boost, got %+v", boosts[1])
	}
	if boosts[2].Delta != 0 {
		t.Fatalf("expected unbridged background row 2 to receive 0 boost, got %+v", boosts[2])
	}

	// Closure-only seed band admission gate:
	closureOnly := map[uint64]bool{100: true}
	directSeeds := map[uint64]bool{1: true}
	emptyShellRows := []TopologyRowView{
		{NodeID: 201, SubjectID: 100, RelID: 10, ObjectID: 999, Score: 0.5, NonSubj: false},
	}
	if ShouldAdmitClosureSeedBand(100, closureOnly, directSeeds, emptyShellRows, nil) {
		t.Fatal("expected closure-only seed 100 with only 0.5-score background rows to be rejected from forming a subject band")
	}
	activeClosureRows := []TopologyRowView{
		{NodeID: 202, SubjectID: 100, RelID: 10, ObjectID: 999, Score: 4.5, NonSubj: true},
	}
	if !ShouldAdmitClosureSeedBand(100, closureOnly, directSeeds, activeClosureRows, nil) {
		t.Fatal("expected closure-only seed 100 with nonSubj score 4.5 to be admitted")
	}
}

func TestFoldRedundantSupertypeRows(t *testing.T) {
	rows := []TopologyRowView{
		{NodeID: 301, SubjectID: 1, RelID: 10, ObjectID: 10, SourceTraceHash: "turn_a", Status: "active", Score: 8.0},
		{NodeID: 302, SubjectID: 1, RelID: 10, ObjectID: 100, SourceTraceHash: "turn_a", Status: "active", Score: 9.0},
		{NodeID: 303, SubjectID: 1, RelID: 10, ObjectID: 100, SourceTraceHash: "turn_b", Status: "active", Score: 6.0},
	}
	labels := map[uint64]string{
		10:  "oil painting",
		100: "painting",
	}
	folded, promoted := FoldRedundantSupertypeRows(rows, labels, map[[2]uint64]bool{{10, 100}: true}, nil)
	if !folded[302] {
		t.Fatalf("expected same-turn supertype row 302 ('painting') to be folded into specific row 301 ('oil painting'), got %v", folded)
	}
	if folded[301] || folded[303] {
		t.Fatalf("unexpected folding of specific row 301 or different-turn row 303: %v", folded)
	}
	if promoted[301] != 9.0 {
		t.Fatalf("expected specific row 301 to inherit max score 9.0, got %v", promoted[301])
	}
	folded2, _ := FoldRedundantSupertypeRows(rows, labels, nil, nil)
	if !folded2[302] {
		t.Fatalf("expected formal suffix subsumption ('oil painting' -> 'painting') to fold row 302, got %v", folded2)
	}
}

func TestBuildHasseSubphraseLinksAndMultiSeedConvergence(t *testing.T) {
	// Multi-level sentence hub:
	//   700: "to get Joanna's another movie script on the big screen" (stems: get joanna anoth movi script big screen)
	//   300: "another movie script" (stems: anoth movi script)
	//   200: "movie script"         (stems: movi script)
	//   100: "script"               (stems: script)
	//   210: "big screen"           (stems: big screen)
	//   110: "screen"               (stems: screen)
	//   10:  "Joanna"               (stems: joanna, in authorStemSet)
	nodes := []SubphraseNodeInput{
		{ID: 10, Label: "Joanna", Stems: []string{"joanna"}, FieldCount: 1, IsPure: true},
		{ID: 100, Label: "script", Stems: []string{"script"}, FieldCount: 1, IsPure: true},
		{ID: 110, Label: "screen", Stems: []string{"screen"}, FieldCount: 1, IsPure: true},
		{ID: 200, Label: "movie script", Stems: []string{"movi", "script"}, FieldCount: 2, IsPure: true},
		{ID: 210, Label: "big screen", Stems: []string{"big", "screen"}, FieldCount: 2, IsPure: true},
		{ID: 300, Label: "another movie script", Stems: []string{"anoth", "movi", "script"}, FieldCount: 3, IsPure: true},
		{ID: 700, Label: "to get Joanna's another movie script on the big screen", Stems: []string{"get", "joanna", "anoth", "movi", "script", "big", "screen"}, FieldCount: 8, IsPure: false, IsPoss: true},
	}
	links := BuildHasseSubphraseLinks(nodes, map[string]bool{"joanna": true})
	edgeSet := make(map[[2]uint64]string, len(links))
	for _, lk := range links {
		edgeSet[[2]uint64{lk.ChildID, lk.ParentID}] = lk.Evidence
	}
	// Sentence 700 must link directly to immediate Hasse children (210 "big screen", 300 "another movie script"),
	// and NOT skip-link directly to 200 ("movie script") or 100 ("script") or 110 ("screen"), nor pollute author 10 ("Joanna") via stem-subseq.
	if edgeSet[[2]uint64{700, 210}] != "stem-suffix" {
		t.Fatalf("expected 700 -> 210 stem-suffix, got %q (links=%+v)", edgeSet[[2]uint64{700, 210}], links)
	}
	if edgeSet[[2]uint64{700, 300}] != "stem-subseq" {
		t.Fatalf("expected 700 -> 300 stem-subseq, got %q (links=%+v)", edgeSet[[2]uint64{700, 300}], links)
	}
	if edgeSet[[2]uint64{700, 200}] != "" || edgeSet[[2]uint64{700, 100}] != "" || edgeSet[[2]uint64{700, 110}] != "" {
		t.Fatalf("Hasse transitive reduction violated: 700 should not skip-link to 200/100/110, got %+v", links)
	}
	// Intermediate phrase 300 -> 200 -> 100 forms the multi-level chain:
	if edgeSet[[2]uint64{300, 200}] == "" || edgeSet[[2]uint64{200, 100}] == "" {
		t.Fatalf("expected multi-level chain 300 -> 200 -> 100, got %+v", links)
	}

	// Verify multi-seed convergence in ExpandDirectedHierarchyClosure prioritizes 700 first after direct seeds
	var metaEdges []HierarchyMetaEdge
	for _, lk := range links {
		metaEdges = append(metaEdges, HierarchyMetaEdge{LHS: lk.ChildID, RHS: lk.ParentID, Kind: "subtype_of"})
	}
	exp, cOnly, err := ExpandDirectedHierarchyClosure([]uint64{300, 210}, 2, 16, 64, func(cur []uint64, limit int) ([]HierarchyMetaEdge, error) {
		return metaEdges, nil
	})
	if err != nil {
		t.Fatalf("ExpandDirectedHierarchyClosure error: %v", err)
	}
	found700 := false
	for _, id := range exp {
		if id == 700 {
			found700 = true
		}
	}
	if !found700 {
		t.Fatalf("expected sentence hub 700 to be reached from seeds [300, 210], got %v", exp)
	}
	if !cOnly[700] {
		t.Fatalf("expected downward sentence hub 700 to remain in closureOnly for band gating, got %v", cOnly)
	}

	// Verify Laws 1, 2, and 3:
	//  - Law 1: Stopword-wrapped 1-stem phrase (801: "what the screenplay is about" -> 800: "screenplay")
	//  - Law 2: 6<->7 char inflectional parity (811: "trying different flavors" [stems: try differ flavo] -> 810: "flavor" [stem: flav])
	//  - Law 3: IsSlotProp proposition sentence hub (900: "John saw river in canyon" -> 820: "canyon"), never acts as ParentID
	extNodes := []SubphraseNodeInput{
		{ID: 800, Label: "screenplay", Stems: []string{"screenplay"}, FieldCount: 1, IsPure: true},
		{ID: 801, Label: "what the screenplay is about", Stems: []string{"screenplay"}, FieldCount: 5, IsPure: false},
		{ID: 810, Label: "flavor", Stems: []string{"flav"}, FieldCount: 1, IsPure: true},
		{ID: 811, Label: "trying different flavors", Stems: []string{"try", "differ", "flavo"}, FieldCount: 3, IsPure: true},
		{ID: 820, Label: "canyon", Stems: []string{"canyon"}, FieldCount: 1, IsPure: true},
		{ID: 900, Label: "John saw river in canyon", Stems: []string{"john", "saw", "river", "canyon"}, FieldCount: 5, IsPure: false, IsSlotProp: true},
		{ID: 901, Label: "John saw river in canyon", Stems: []string{"john", "saw", "river", "canyon"}, FieldCount: 4, IsPure: true},
	}
	extLinks := BuildHasseSubphraseLinks(extNodes, map[string]bool{"john": true})
	extSet := make(map[[2]uint64]string, len(extLinks))
	for _, lk := range extLinks {
		if lk.ParentID == 900 {
			t.Fatalf("IsSlotProp node 900 must never act as ParentID, got link %+v", lk)
		}
		extSet[[2]uint64{lk.ChildID, lk.ParentID}] = lk.Evidence
	}
	if extSet[[2]uint64{801, 800}] != "stem-subseq" {
		t.Fatalf("Law 1 failed: expected 801 ('what the screenplay is about') -> 800 ('screenplay'), got %+v", extLinks)
	}
	if extSet[[2]uint64{811, 810}] != "stem-suffix" {
		t.Fatalf("Law 2 failed: expected 811 ('trying different flavors') -> 810 ('flavor'), got %+v", extLinks)
	}
	if extSet[[2]uint64{900, 820}] == "" {
		t.Fatalf("Law 3 failed: expected IsSlotProp sentence hub 900 -> 820 ('canyon'), got %+v", extLinks)
	}

	// Verify 3-level transitive folding (A "watercolor landscape painting" -> B "landscape painting" -> C "painting")
	// regardless of row ordering, folding both B and C while promoting highest score to most specific leaf A.
	chainRows := []TopologyRowView{
		{NodeID: 1, SubjectID: 10, RelID: 20, ObjectID: 301, SourceTraceHash: "t1", Status: "active", Score: 5.0},
		{NodeID: 2, SubjectID: 10, RelID: 20, ObjectID: 302, SourceTraceHash: "t1", Status: "active", Score: 7.0},
		{NodeID: 3, SubjectID: 10, RelID: 20, ObjectID: 303, SourceTraceHash: "t1", Status: "active", Score: 9.5},
	}
	chainLabels := map[uint64]string{
		301: "watercolor landscape painting",
		302: "landscape painting",
		303: "painting",
	}
	folded, promoted := FoldRedundantSupertypeRows(chainRows, chainLabels, nil, nil)
	if !folded[2] || !folded[3] || folded[1] {
		t.Fatalf("expected intermediate #2 and general #3 to be folded into specific #1, got folded=%v", folded)
	}
	if promoted[1] < 9.5 {
		t.Fatalf("expected specific leaf #1 to inherit peak score 9.5 from #3, got %v", promoted[1])
	}
}

func TestColloquialFragmentClassificationAndTopologyHubbing(t *testing.T) {
	colloquialMustHit := []string{
		"who they are",
		"worth it",
		"switch off",
		"hear more",
		"its due",
		"it daily",
		"them grow",
		"to save",
		"to try it out",
		"are you excited",
		"what's been going on",
		"what has been going on",
		"I won't quit",
		"how it affected you",
		"what I accomplished",
		"in any way",
		"all that mattered",
		"How did you feel when you found out",
		"What do you enjoy about them so much",
		"get it to where I wanted it",
	}
	for _, s := range colloquialMustHit {
		if !IsColloquialFragmentLabel(s) {
			t.Fatalf("expected IsColloquialFragmentLabel(%q) == true, got false", s)
		}
	}

	conceptMustNotHit := []string{
		"Super Mario",
		"Athey Thompson",
		"roller coasters",
		"where Joanna went",
		"CLUB (2)",
	}
	for _, s := range conceptMustNotHit {
		if IsColloquialFragmentLabel(s) {
			t.Fatalf("expected IsColloquialFragmentLabel(%q) == false, got true", s)
		}
	}

	nodes := []SubphraseNodeInput{
		{ID: 10, Label: "Joanna", Stems: OrderedStemSeq("Joanna"), FieldCount: 1, IsPure: true},
		{ID: 11, Label: "Calvin", Stems: OrderedStemSeq("Calvin"), FieldCount: 1, IsPure: true},
		{ID: 12, Label: "Dave", Stems: OrderedStemSeq("Dave"), FieldCount: 1, IsPure: true},
		{ID: 20, Label: "the script", Stems: OrderedStemSeq("the script"), FieldCount: 2, IsPure: false},
		{ID: 21, Label: "second script", Stems: OrderedStemSeq("second script"), FieldCount: 2, IsPure: true},
		{ID: 22, Label: "what about your script now", Stems: OrderedStemSeq("what about your script now"), FieldCount: 5, IsPure: false},
		{ID: 30, Label: "sense of purpose", Stems: OrderedStemSeq("sense of purpose"), FieldCount: 3, IsPure: false},
		{ID: 31, Label: "clearer sense of purpose", Stems: OrderedStemSeq("clearer sense of purpose"), FieldCount: 4, IsPure: false},
		{ID: 40, Label: "where Joanna went", Stems: OrderedStemSeq("where Joanna went"), FieldCount: 3, IsPure: false},
		{ID: 41, Label: "Calvin and Dave", Stems: OrderedStemSeq("Calvin and Dave"), FieldCount: 3, IsPure: false},
		{ID: 50, Label: "gold ring", Stems: OrderedStemSeq("gold ring"), FieldCount: 2, IsPure: true},
		{ID: 51, Label: "silver ring", Stems: OrderedStemSeq("silver ring"), FieldCount: 2, IsPure: true},
	}
	links := BuildHasseSubphraseLinks(nodes, map[string]bool{"joann": true, "calv": true, "dave": true})
	got := make(map[[2]uint64]string, len(links))
	for _, lk := range links {
		got[[2]uint64{lk.ChildID, lk.ParentID}] = lk.Evidence
	}
	if got[[2]uint64{21, 20}] != "stem-entity" {
		t.Fatalf("expected 'second script'(21) -> 'the script'(20) via stem-entity, got %q", got[[2]uint64{21, 20}])
	}
	if got[[2]uint64{22, 20}] != "stem-entity" {
		t.Fatalf("expected 'what about your script now'(22) -> 'the script'(20) via stem-entity, got %q", got[[2]uint64{22, 20}])
	}
	if got[[2]uint64{31, 30}] != "stem-entity" {
		t.Fatalf("expected 'clearer sense of purpose'(31) -> 'sense of purpose'(30) via stem-entity, got %q", got[[2]uint64{31, 30}])
	}
	if got[[2]uint64{40, 10}] != "stem-entity" {
		t.Fatalf("expected 'where Joanna went'(40) -> 'Joanna'(10) via stem-entity, got %q", got[[2]uint64{40, 10}])
	}
	if got[[2]uint64{41, 11}] != "stem-entity" || got[[2]uint64{41, 12}] != "stem-entity" {
		t.Fatalf("expected 'Calvin and Dave'(41) -> 'Calvin'(11) and 'Dave'(12) via stem-entity, got %v", got)
	}
	if got[[2]uint64{51, 50}] != "stem-entity" {
		t.Fatalf("expected 'silver ring'(51) -> 'gold ring'(50) via shared tail-cluster stem-entity, got %q", got[[2]uint64{51, 50}])
	}

	// Verify stem-entity edge directionality in ExpandDirectedHierarchyClosure:
	//  - Single seed [10] (Joanna) does NOT fan out downward into colloquial clause 40 ("where Joanna went") in Tier 2;
	//  - Direct seed [40] DOES ascend upward to protagonist 10 (Joanna) in Tier 3;
	//  - Dual seeds [11, 12] (Calvin, Dave) DO converge downward onto 41 ("Calvin and Dave") in Tier 1.
	metaEdges := []HierarchyMetaEdge{
		{LHS: 40, RHS: 10, Kind: "subtype_of", Evidence: "stem-entity"},
		{LHS: 41, RHS: 11, Kind: "subtype_of", Evidence: "stem-entity"},
		{LHS: 41, RHS: 12, Kind: "subtype_of", Evidence: "stem-entity"},
	}
	fetch := func(cur []uint64, limit int) ([]HierarchyMetaEdge, error) {
		return metaEdges, nil
	}
	expSingle, _, _ := ExpandDirectedHierarchyClosure([]uint64{10}, 2, 16, 64, fetch)
	if len(expSingle) != 1 || expSingle[0] != 10 {
		t.Fatalf("expected single protagonist seed [10] not to fan out into stem-entity clause 40, got %v", expSingle)
	}
	expUp, _, _ := ExpandDirectedHierarchyClosure([]uint64{40}, 2, 16, 64, fetch)
	if len(expUp) != 2 || expUp[1] != 10 {
		t.Fatalf("expected clause seed [40] to ascend to protagonist 10, got %v", expUp)
	}
	expDual, _, _ := ExpandDirectedHierarchyClosure([]uint64{11, 12}, 2, 16, 64, fetch)
	if len(expDual) != 3 || expDual[2] != 41 {
		t.Fatalf("expected dual seeds [11, 12] to converge onto 41 ('Calvin and Dave') in Tier 1, got %v", expDual)
	}
}

func TestCrossTurnHypernymEchoAndEvidenceDedupGates(t *testing.T) {
	// 1. Cross-turn zero-signal hypernym echo folding:
	// Turn A: specific row 401 ("golden retriever dog", NonSubj=true, Score=8.5)
	// Turn B: zero-signal generalized echo 402 ("dog", NonSubj=false, Score=0.5)
	// Turn C: active signal row 403 ("dog", NonSubj=true, Score=5.0) -> MUST NOT be folded across turns.
	rows := []TopologyRowView{
		{NodeID: 401, SubjectID: 1, RelID: 10, ObjectID: 201, SourceTraceHash: "turn_a", Status: "active", Score: 8.5, NonSubj: true},
		{NodeID: 402, SubjectID: 1, RelID: 10, ObjectID: 202, SourceTraceHash: "turn_b", Status: "active", Score: 0.5, NonSubj: false},
		{NodeID: 403, SubjectID: 1, RelID: 10, ObjectID: 202, SourceTraceHash: "turn_c", Status: "active", Score: 5.0, NonSubj: true},
	}
	labels := map[uint64]string{
		201: "golden retriever dog",
		202: "dog",
	}
	folded, _ := FoldRedundantSupertypeRows(rows, labels, nil, nil)
	if !folded[402] {
		t.Fatalf("expected zero-signal cross-turn generalized echo 402 ('dog') to be folded into specific 401, got %v", folded)
	}
	if folded[401] || folded[403] {
		t.Fatalf("expected signal-bearing rows 401 and 403 to be preserved, got %v", folded)
	}

	// 2. Auxiliary unbound/dual section signal gate:
	bgUnbound := []TopologyRowView{
		{NodeID: 501, SubjectID: 1, RelID: 20, Score: 0.5, NonSubj: false},
	}
	if ShouldRenderAuxUnboundOrDualSection(bgUnbound, true) {
		t.Fatal("expected 0.5-score background unbound section to be suppressed when primary SO/SX chains exist")
	}
	if !ShouldRenderAuxUnboundOrDualSection(bgUnbound, false) {
		t.Fatal("expected unbound section to be rendered when no primary SO/SX chains exist")
	}
	sigUnbound := []TopologyRowView{
		{NodeID: 502, SubjectID: 1, RelID: 20, Score: 4.5, NonSubj: true},
	}
	if !ShouldRenderAuxUnboundOrDualSection(sigUnbound, true) {
		t.Fatal("expected signal-bearing unbound section to be rendered even when primary SO/SX chains exist")
	}

	// 3. Cross-band snippet coverage check ([E#k]/[A#m] vs src2):
	delivered := []string{
		"[代码件:src/validator.py L45-L68]\ndef _check_required_columns(df, cols):\n    missing = [c for c in cols if c not in df.columns]\n    return missing",
	}
	dupWindow := "def _check_required_columns(df, cols):\n    missing = [c for c in cols if c not in df.columns]"
	if !IsSnippetCoveredByDeliveredEvidence(dupWindow, delivered) {
		t.Fatal("expected duplicate code window already present in [E#k]/[A#m] to be detected as covered")
	}
	novelWindow := "def transform_pipeline(df):\n    return df.dropna()"
	if IsSnippetCoveredByDeliveredEvidence(novelWindow, delivered) {
		t.Fatal("expected novel code window to not be marked as covered")
	}

	// 4. Suffix-prefix overlapping snippet stitch inside [E#k]:
	span1 := "I bought the vintage camera last week and it hit my account"
	span2 := "last week and it hit my account for $150.00 in total."
	merged, ok := MergeOverlappingSnippet(span1, span2)
	if !ok || merged != "I bought the vintage camera last week and it hit my account for $150.00 in total." {
		t.Fatalf("expected overlapping spans to stitch seamlessly, got ok=%v merged=%q", ok, merged)
	}
	if _, okDisjoint := MergeOverlappingSnippet("First unrelated clause here.", "Second completely separate clause."); okDisjoint {
		t.Fatal("expected disjoint clauses not to merge")
	}
}

func TestClassifySpecificInstanceLabelLanguageAgnostic(t *testing.T) {
	cues := map[string]bool{
		"the":  true,
		"new":  true,
		"game": true,
		"play": true,
		"游戏":   true,
	}
	if spec, comp := ClassifySpecificInstanceLabel("The Witcher 3", cues); !spec || !comp {
		t.Fatalf("expected 'The Witcher 3' to be compound specific despite stopword 'the', got spec=%v comp=%v", spec, comp)
	}
	if spec, comp := ClassifySpecificInstanceLabel("塞尔达传说", cues); !spec || !comp {
		t.Fatalf("expected CJK entity '塞尔达传说' to be compound specific, got spec=%v comp=%v", spec, comp)
	}
	if spec, _ := ClassifySpecificInstanceLabel("RPG games", cues); spec {
		t.Fatal("expected 'RPG games' (sharing stem 'game' with cue) to be rejected as generic category")
	}
	if spec, _ := ClassifySpecificInstanceLabel("a new RPG", cues); spec {
		t.Fatal("expected lowercase-article phrase 'a new RPG' to be rejected")
	}
}

func TestCognitiveAlgebraEightKindsPartOfAndSubtypeSeparation(t *testing.T) {
	// Setup:
	//   - Category: 100 ("person") <--subtype_of-- 10 ("Audrey")
	//   - Possessive part_of: 10 ("Audrey") <--part_of(possessive-owner)-- 11 ("Audrey's dog")
	//   - Sibling possession: 10 ("Audrey") <--part_of(possessive-owner)-- 12 ("Audrey's painting")
	//   - Spatial containment tree: 500 ("Zhejiang") <--part_of(spatial-locus-tree)-- 50 ("Hangzhou") <--part_of(spatial-locus-tree)-- 5 ("Yunqi")
	edges := []HierarchyMetaEdge{
		{LHS: 10, RHS: 100, Kind: "subtype_of", Evidence: "inline-category"},
		{LHS: 11, RHS: 10, Kind: "part_of", Evidence: "possessive-owner"},
		{LHS: 12, RHS: 10, Kind: "part_of", Evidence: "possessive-owner"},
		{LHS: 50, RHS: 500, Kind: "part_of", Evidence: "spatial-locus-tree"},
		{LHS: 5, RHS: 50, Kind: "part_of", Evidence: "spatial-locus-tree"},
	}
	fetch := func(seedSet []uint64, limit int) ([]HierarchyMetaEdge, error) {
		inF := map[uint64]bool{}
		for _, id := range seedSet {
			inF[id] = true
		}
		var out []HierarchyMetaEdge
		for _, e := range edges {
			if inF[e.LHS] || inF[e.RHS] {
				out = append(out, e)
			}
		}
		return out, nil
	}

	// 1. Adversarial cross-sort guard: Querying category 100 ("person") descends to 10 ("Audrey"),
	//    but MUST NOT chain into possessive 11 ("Audrey's dog") — a dog is not a person!
	expCat, _, err := ExpandDirectedHierarchyClosure([]uint64{100}, 2, 32, 256, fetch)
	if err != nil {
		t.Fatalf("ExpandDirectedHierarchyClosure(100): %v", err)
	}
	catSet := map[uint64]bool{}
	for _, id := range expCat {
		catSet[id] = true
	}
	if !catSet[100] || !catSet[10] {
		t.Fatalf("expected {100, 10} when querying category 100, got %v", expCat)
	}
	if catSet[11] || catSet[12] {
		t.Fatalf("cross-sort pollution: category 100 ('person') leaked into possessions 11/12 ('Audrey's dog/painting'): %v", expCat)
	}

	// 2. Direct owner seed 10 ("Audrey") CAN reach its 1-hop possessions {11, 12}:
	expOwner, _, _ := ExpandDirectedHierarchyClosure([]uint64{10}, 2, 32, 256, fetch)
	ownerSet := map[uint64]bool{}
	for _, id := range expOwner {
		ownerSet[id] = true
	}
	if !ownerSet[11] || !ownerSet[12] {
		t.Fatalf("expected direct owner 10 to reach possessions 11 and 12, got %v", expOwner)
	}

	// 3. Querying possession 11 ("Audrey's dog") ascends 1 hop to owner 10 ("Audrey"),
	//    but MUST NOT reverse into sibling possession 12 ("Audrey's painting"):
	expPoss, _, _ := ExpandDirectedHierarchyClosure([]uint64{11}, 2, 32, 256, fetch)
	possSet := map[uint64]bool{}
	for _, id := range expPoss {
		possSet[id] = true
	}
	if !possSet[11] || !possSet[10] {
		t.Fatalf("expected possession 11 to reach owner 10, got %v", expPoss)
	}
	if possSet[12] {
		t.Fatalf("sibling possession pollution: 11 ('Audrey's dog') leaked into 12 ('Audrey's painting'): %v", expPoss)
	}

	// 4. Spatial containment tree 500 ("Zhejiang") -> 50 ("Hangzhou") -> 5 ("Yunqi") traverses 2 hops downward:
	expSpatial, _, _ := ExpandDirectedHierarchyClosure([]uint64{500}, 2, 32, 256, fetch)
	spSet := map[uint64]bool{}
	for _, id := range expSpatial {
		spSet[id] = true
	}
	if !spSet[500] || !spSet[50] || !spSet[5] {
		t.Fatalf("expected spatial container 500 to transitively reach 50 and 5, got %v", expSpatial)
	}
}

func TestBuildHasseSubphraseLinksNoPossessiveHomonymLeakOrPrefixCollision(t *testing.T) {
	// 1. "Persian cat" must NOT link to "person" via 4-letter root "pers" in left-modifier prefix
	// 2. "Deborah's Luna" (IsPoss=true) must link to owner "Deborah", NOT back to bare proper individual "Luna"
	nodes := []SubphraseNodeInput{
		{ID: 1, Label: "person", Stems: OrderedStemSeq("person"), IsPure: true},
		{ID: 2, Label: "Persian cat", Stems: OrderedStemSeq("Persian cat"), IsPure: true},
		{ID: 3, Label: "Deborah", Stems: OrderedStemSeq("Deborah"), IsPure: true},
		{ID: 4, Label: "Luna", Stems: OrderedStemSeq("Luna"), IsPure: true},
		{ID: 5, Label: "Deborah's Luna", Stems: OrderedStemSeq("Deborah's Luna"), IsPure: true, IsPoss: true},
	}
	links := BuildHasseSubphraseLinks(nodes, nil)
	for _, lk := range links {
		if lk.ChildID == 2 && lk.ParentID == 1 {
			t.Fatalf("unexpected false-positive link: 'Persian cat'(2) -> 'person'(1)")
		}
		if lk.ChildID == 5 && lk.ParentID == 4 {
			t.Fatalf("unexpected cross-circle homonym leak: 'Deborah's Luna'(5) -> bare 'Luna'(4)")
		}
	}
	if alive, st, _ := ProjectAssertionAsOf("superseded", "referential", "", 999999, 100, 200, 150, 0, 100); !alive || st != "active" {
		t.Fatalf("expected historical replay row (tSource=100 <= asOf=150 < tValidTo=200, wall tRecord=999999) to be alive & active at asOf=150")
	}
	if alive, _, _ := ProjectAssertionAsOf("active", "referential", "", 999999, 100, 0, 150, 0, 180); alive {
		t.Fatalf("expected future source turn (tSource=180 > asOf=150) to be rejected at asOf=150")
	}
	// 3. CJK author "苏黎" must NEVER act as parent concept of "苏黎世" or "苏黎世联邦理工学院"
	cjkNodes := []SubphraseNodeInput{
		{ID: 10, Label: "苏黎", Stems: OrderedStemSeq("苏黎"), IsPure: true},
		{ID: 11, Label: "苏黎世", Stems: OrderedStemSeq("苏黎世"), IsPure: true},
		{ID: 12, Label: "苏黎世联邦理工学院", Stems: OrderedStemSeq("苏黎世联邦理工学院"), IsPure: true},
	}
	for _, lk := range BuildHasseSubphraseLinks(cjkNodes, map[string]bool{"苏黎": true}) {
		if lk.ParentID == 10 {
			t.Fatalf("unexpected false CJK prefix link to author '苏黎'(10): %+v", lk)
		}
	}
}

func TestExpandCausalDerivationGraphAndPauliExclusionAdversarial(t *testing.T) {
	// Graph:
	//   100 --[causes (llm-closed-set)]--> 200 --[derived_from (llm-closed-set)]<-- 300
	//   100 --[derived_from (sx-sxo-refinement)]--> 999 (noise: morphological refinement must be ignored)
	//   200 --[causes]--> 100 (adversarial cycle: must not loop infinitely)
	allEdges := []CausalDerivationEdge{
		{LHS: 100, RHS: 200, MetaKind: "causes", Evidence: "llm-closed-set"},
		{LHS: 300, RHS: 200, MetaKind: "derived_from", Evidence: "llm-closed-set"},
		{LHS: 100, RHS: 999, MetaKind: "derived_from", Evidence: "sx-sxo-refinement"},
		{LHS: 200, RHS: 100, MetaKind: "causes", Evidence: "llm-closed-set"},
	}
	exp, hops, roots, err := ExpandCausalDerivationGraph([]uint64{100}, 2, 16, 64, func(cur []uint64, _ int) ([]CausalDerivationEdge, error) {
		inCur := make(map[uint64]bool, len(cur))
		for _, id := range cur {
			inCur[id] = true
		}
		var out []CausalDerivationEdge
		for _, e := range allEdges {
			if inCur[e.LHS] || inCur[e.RHS] {
				out = append(out, e)
			}
		}
		return out, nil
	})
	if err != nil {
		t.Fatalf("ExpandCausalDerivationGraph error: %v", err)
	}
	if len(exp) != 2 || hops[200] != 1 || hops[300] != 2 || roots[200] != 100 || roots[300] != 100 {
		t.Fatalf("expected 2-hop causal/derivation expansion [200(hop=1), 300(hop=2)] rooted at 100, got exp=%v hops=%v roots=%v", exp, hops, roots)
	}
	if _, ok := hops[999]; ok {
		t.Fatalf("sx-sxo-refinement edge to 999 must be ignored in propositional causal chase")
	}

	// Pauli Exclusion inside C6 weak container:
	//   501 --[supersedes]--> 500 (500 must be excluded)
	//   100 --[conflicts_with]--> 600 (100 is in active primary set => 600 must be excluded)
	//   700 --[conflicts_with]--> 701 (both in primary set => neither auto-excluded)
	negEdges := []CausalDerivationEdge{
		{LHS: 501, RHS: 500, MetaKind: "supersedes", Evidence: "closes_prior"},
		{LHS: 100, RHS: 600, MetaKind: "conflicts_with", Evidence: "llm-closed-set"},
		{LHS: 700, RHS: 701, MetaKind: "conflicts_with", Evidence: "llm-closed-set"},
	}
	excl := ComputeStrongNegativeExcludedSet([]uint64{100, 700, 701}, negEdges)
	if !excl[500] || !excl[600] {
		t.Fatalf("expected 500 (superseded) and 600 (conflicting with primary 100) to be excluded, got %v", excl)
	}
	if excl[100] || excl[501] || excl[700] || excl[701] {
		t.Fatalf("unexpected exclusion of primary assertions: %v", excl)
	}
}
