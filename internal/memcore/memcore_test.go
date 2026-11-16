// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDeBrandedIdentifiers(t *testing.T) {
	if !IsEpisodeBandID(EpisodeBandID) {
		t.Fatal("expected canonical episode band to be recognized")
	}
	if !IsPrimaryDeliveryBandID(BandIDPrefix + "1") {
		t.Fatal("expected canonical primary band to be recognized")
	}
	if !IsEvidenceDeliveryBandID(BandIDEvidenceArtifacts) {
		t.Fatal("expected canonical evidence-artifacts band to be recognized")
	}
	if !IsMiscDeliveryBandID(BandIDMisc) {
		t.Fatal("expected canonical misc band to be recognized")
	}
	if got := NormalizeMetaKind("same-as"); got != "same_as" {
		t.Fatalf("NormalizeMetaKind got %q, want same_as", got)
	}
	if got := NormalizeMetaKind("supplement"); got != "derived_from" {
		t.Fatalf("NormalizeMetaKind(supplement) got %q, want derived_from", got)
	}
	if got := LegacyHyphenMetaKind("conflicts_with"); got != "conflicts-with" {
		t.Fatalf("LegacyHyphenMetaKind got %q, want conflicts-with", got)
	}
	if len(CanonicalMetaKinds) != 8 {
		t.Fatalf("expected 8 canonical meta kinds, got %d: %v", len(CanonicalMetaKinds), CanonicalMetaKinds)
	}
	for _, k := range []string{"same_as", "inverse_of", "subtype_of", "part_of", "derived_from", "causes", "supersedes", "conflicts_with"} {
		if !IsCanonicalMetaKind(k) {
			t.Fatalf("expected %q to be in 8-kind closed set", k)
		}
	}
	if IsCanonicalMetaKind("unknown_edge") {
		t.Fatal("expected unknown_edge to be rejected by IsCanonicalMetaKind")
	}
}

func TestTimeMicroLanguageAndNaturalLanguage(t *testing.T) {
	ref := time.Date(2023, 5, 15, 12, 0, 0, 0, time.UTC)
	ts, kind := ParseTimeExprAt("2023-05-01 -7d", ref)
	if kind != "relative_resolved" || DateUTC(ts) != "2023-04-24" {
		t.Fatalf("DSL offset got (%s, %q), want 2023-04-24 relative_resolved", DateUTC(ts), kind)
	}

	ts2, kind2 := ResolveAssertionTime("", "", "Back in May 2022, I broke my ankle.", ref.Unix())
	if kind2 != "explicit" || DateUTC(ts2) != "2022-05-01" {
		t.Fatalf("ResolveAssertionTime got (%s, %q), want 2022-05-01 explicit", DateUTC(ts2), kind2)
	}

	sent := ExtractAnchorSentence("Hello there! Back in May 2022, I broke my ankle. It hurt.", 31, 47)
	if sent != "Back in May 2022, I broke my ankle." {
		t.Fatalf("ExtractAnchorSentence got %q", sent)
	}
}

func TestGatesAndNormalization(t *testing.T) {
	fixed := CorrectAliasTargetByPrefix(AliasSpec{Of: "Caroline", Alias: "Mel", Basis: "referral"}, "Caroline", "Melanie")
	if fixed.Of != "Melanie" || fixed.Basis != "shortening" {
		t.Fatalf("CorrectAliasTargetByPrefix got %+v", fixed)
	}

	if reason := ValidateUnboundAndDualForm("Caroline", 0, true, "", "rained", "", "", 0, "", "", ""); reason == "" {
		t.Fatal("expected rejection when both subject and subject_absent are set")
	}
	if reason := ValidateUnboundAndDualForm("", 0, true, "", "rained", "", "heavily", 0, "", "", ""); reason != "" {
		t.Fatalf("unexpected rejection for valid XO binary form: %s", reason)
	}
	if reason := ValidateUnboundAndDualForm("", 0, false, "absent_in_source", "built", "bridge", "", 0, "", "", ""); reason != "" {
		t.Fatalf("unexpected rejection for valid subject_gap form: %s", reason)
	}
}

func TestLexicalStemMatching(t *testing.T) {
	if s := MatchTokenOrStem("she enjoys painting portraits", "painted"); s < 0.85 {
		t.Fatalf("expected stem match >= 0.85, got %v", s)
	}
}

func TestExpandInlineSubAssertionsAndBatchAtRef(t *testing.T) {
	batch := []map[string]any{
		{
			"s":           "Melanie",
			"x":           "said",
			"anchor_text": "Melanie said Caroline painted a sunrise.",
			"sub_assertion": map[string]any{
				"s": "Caroline",
				"x": "painted",
				"o": "a sunrise",
			},
		},
	}
	flat := ExpandInlineSubAssertions(batch)
	if len(flat) != 2 {
		t.Fatalf("ExpandInlineSubAssertions len = %d, want 2", len(flat))
	}
	if flat[0]["s"] != "Caroline" || flat[0]["x"] != "painted" || flat[0]["o"] != "a sunrise" {
		t.Fatalf("unexpected inner assertion: %+v", flat[0])
	}
	if flat[0]["anchor_text"] != "Melanie said Caroline painted a sunrise." {
		t.Fatalf("expected inner assertion to inherit parent anchor_text, got %+v", flat[0])
	}
	if flat[1]["s"] != "Melanie" || flat[1]["x"] != "said" || flat[1]["o"] != "@1" {
		t.Fatalf("expected outer assertion o=@1, got %+v", flat[1])
	}

	resolved := ResolveBatchAtRefString("@9", flat, []string{"a_inner123", ""}, 1, "Melanie said Caroline painted a sunrise.")
	if resolved != "#a_inner123" {
		t.Fatalf("ResolveBatchAtRefString out-of-bounds fallback got %q, want #a_inner123", resolved)
	}
}

func TestStmtCoversSnippetAndStripMultimodalParen(t *testing.T) {
	if !StmtCoversSnippet("Caroline —painted→ sunrise", "Caroline painted a sunrise") {
		t.Fatal("expected StmtCoversSnippet=true when all content words/stems are in stmt")
	}
	if StmtCoversSnippet("Caroline —volunteered→ shelter", "Caroline volunteered at a homeless shelter in Boston") {
		t.Fatal("expected StmtCoversSnippet=false when snippet has uncovered locative/modifier words (homeless, Boston)")
	}
	// Affirmation statement must NEVER cover a negated snippet (negation/polarity markers must not be skipped as stopwords).
	for _, negCase := range [][2]string{
		{"Caroline —painted→ sunrise", "Caroline did not paint a sunrise"},
		{"Caroline —painted→ sunrise", "Caroline never painted a sunrise"},
		{"Caroline —went→ Boston", "Caroline went without Melanie to Boston"},
		{"Caroline —voted→ proposal", "Caroline voted against the proposal"},
		{"王小明 —去→ 北京", "王小明没去北京"},
		{"王小明 —喜欢→ 游泳", "王小明不喜欢游泳"},
	} {
		if StmtCoversSnippet(negCase[0], negCase[1]) {
			t.Fatalf("Negation invariant falsified: affirmative stmt %q falsely covered negated/polarity snippet %q", negCase[0], negCase[1])
		}
	}
	longText := "I went to the pottery workshop yesterday and made a bowl! (" +
		"这张图片展示了一个手工制作陶碗的工作台，背景里有陶轮、粘土工具以及窗户透进来的自然光，整体氛围温暖而富有艺术感，细节非常丰富且篇幅超过四百八十字。" +
		"这张图片展示了一个手工制作陶碗的工作台，背景里有陶轮、粘土工具以及窗户透进来的自然光，整体氛围温暖而富有艺术感，细节非常丰富且篇幅超过四百八十字。" +
		"这张图片展示了一个手工制作陶碗的工作台，背景里有陶轮、粘土工具以及窗户透进来的自然光，整体氛围温暖而富有艺术感，细节非常丰富且篇幅超过四百八十字。)"
	stripped := StripMultimodalLongParen(longText, 10, 480)
	if stripped != "I went to the pottery workshop yesterday and made a bowl!" {
		t.Fatalf("StripMultimodalLongParen got %q", stripped)
	}

	if pass, incC, _ := MonotonicConvexBandPass(2, 10, 3, 2, 0, true, false, true, 5.0); !pass || !incC {
		t.Fatal("expected connected micro band with connAccepted=2 and maxNS=5.0 to pass")
	}
	if pass, _, _ := MonotonicConvexBandPass(6, 12, 10, 5, 0, false, true, true, 12.0); pass {
		t.Fatal("expected large trace-connected band at connAccepted=5 with maxNS=12.0 (< 31.0 floor) to be pruned")
	}
	if pass, incC, _ := MonotonicConvexBandPass(6, 12, 3, 5, 0, true, false, true, 12.0); !pass || !incC {
		t.Fatal("expected 1-hop direct-connected micro band at connAccepted=5 with maxNS=12.0 (>= 11.5 floor) to pass")
	}
}

func TestScoreAndAssembleBandsSelfCoverAndConvexBudget(t *testing.T) {
	items := []ScoredAssertion{
		{
			Assertion:   Assertion{SubjectID: "n_caroline", ObjectID: "n_melanie", TEvent: 1683504000, AnchorTraceHash: "t1"},
			SubjectName: "Caroline",
			RelName:     "met",
			ObjectName:  "Melanie",
			Snippet:     "Caroline met Melanie at the coffee shop in Boston",
		},
		{
			Assertion:   Assertion{SubjectID: "n_melanie", ObjectID: "n_sunrise", TEvent: 1683504000, AnchorTraceHash: "t1"},
			SubjectName: "Melanie",
			RelName:     "painted",
			ObjectName:  "sunrise",
			Snippet:     "Melanie painted a sunrise",
		},
	}
	bands := ScoreAndAssembleBands("Where did Caroline meet Melanie and what did Melanie paint?", items, 8)
	if len(bands) != 2 {
		t.Fatalf("expected 2 connected bands, got %d", len(bands))
	}
	// Band 1 snippet has uncovered "coffee shop in Boston" -> snippet must be preserved.
	if !strings.Contains(bands[0].Rendered, "Boston") && !strings.Contains(bands[1].Rendered, "Boston") {
		t.Fatalf("expected uncovered locative 'Boston' to be preserved in rendered bands: %+v", bands)
	}
	// "Melanie painted a sunrise" is 100% covered by "Melanie —painted→ sunrise" -> redundant quote elided.
	for _, b := range bands {
		if b.SubjectName == "Melanie" && strings.Contains(b.Rendered, "「Melanie painted a sunrise」") {
			t.Fatalf("expected fully covered snippet to be elided, got: %s", b.Rendered)
		}
	}
}

func TestSpacetimeIncarnationAddressingAndResonance(t *testing.T) {
	// 1. Hierarchical LCP spatial kernel
	if sc := HierarchicalLCPScore("org/repo/pkg/hooks", "org/repo/pkg/hooks"); sc != 1.0 {
		t.Fatalf("exact locus LCP score = %v, want 1.0", sc)
	}
	if sc := HierarchicalLCPScore("org/repo/pkg/hooks", "org/repo/pkg/store"); sc < 0.7 || sc > 0.8 {
		t.Fatalf("same-repo sibling dir LCP score = %v, want 0.75", sc)
	}
	if sc := HierarchicalLCPScore("org/repo-a/pkg/hooks", "org/repo-b/pkg/hooks"); sc < 0.2 || sc > 0.3 {
		t.Fatalf("same-org cross-repo LCP score = %v, want 0.25", sc)
	}
	if sc := HierarchicalLCPScore("org/repo-a", ""); sc != 0 {
		t.Fatalf("empty locus LCP score = %v, want 0 (zero penalty)", sc)
	}

	// 2. Incarnation genealogy kernel
	if inc := IncarnationGenealogyScore("coding/zcode/main", "coding/zcode/main"); inc != 1.0 {
		t.Fatalf("exact incarnation score = %v, want 1.0", inc)
	}
	if inc := IncarnationGenealogyScore("coding/zcode/main", "coding/claude-code/main"); inc <= 0.3 || inc >= 0.4 {
		t.Fatalf("cross-harness coding incarnation score = %v, want 1/3", inc)
	}
	if inc := IncarnationGenealogyScore("coding/zcode/main", "dialog/main"); inc != 0 {
		t.Fatalf("disjoint modality incarnation score = %v, want 0", inc)
	}

	// 3. Dual-track prefixes & incarnation hierarchy prefixes
	prefixes, shortAlias := DualTrackLocusPrefixes("org/repo/pkg/sub/leaf")
	if len(prefixes) != 4 || prefixes[0] != "org" || prefixes[1] != "org/repo" || prefixes[2] != "org/repo/pkg" || prefixes[3] != "org/repo/pkg/sub/leaf" {
		t.Fatalf("unexpected DualTrackLocusPrefixes: %v", prefixes)
	}
	if shortAlias != "leaf" {
		t.Fatalf("unexpected shortAlias: %q, want leaf", shortAlias)
	}
	incPrefixes := IncarnationHierarchyPrefixes("coding/zcode/main")
	if len(incPrefixes) != 3 || incPrefixes[0] != "coding" || incPrefixes[1] != "coding/zcode" || incPrefixes[2] != "coding/zcode/main" {
		t.Fatalf("unexpected IncarnationHierarchyPrefixes: %v", incPrefixes)
	}

	// 4. Event Locus > Presence Locus priority
	if eff := ResolveEffectiveLocus("country/tokyo", "org/repo/pkg", 120); eff != "country/tokyo" {
		t.Fatalf("explicit event locus should win, got %q", eff)
	}
	if eff := ResolveEffectiveLocus("", "org/repo/pkg", 120); eff != "org/repo/pkg" {
		t.Fatalf("empty event locus should inherit presence locus, got %q", eff)
	}

	// 5. Soft potential field multiplier (>= 1.0)
	q := PresenceCoordinates{
		World:       "actual",
		Carrier:     "harness:zcode",
		Locus:       "org/repo/pkg/hooks",
		Incarnation: "coding/zcode/main",
	}
	sameDirMult := SpacetimeResonanceMultiplier(q, "org/repo/pkg/hooks", "virtual", "coding/zcode/main")
	sameRepoMult := SpacetimeResonanceMultiplier(q, "org/repo/pkg/store", "virtual", "coding/zcode/main")
	globalMult := SpacetimeResonanceMultiplier(q, "", "", "dialog/main")
	if !(sameDirMult > sameRepoMult && sameRepoMult > globalMult && globalMult == 1.0) {
		t.Fatalf("expected sameDir(%v) > sameRepo(%v) > global(%v==1.0)", sameDirMult, sameRepoMult, globalMult)
	}

	// 6. Git workspace locus detection
	tmp := t.TempDir()
	repoDir := filepath.Join(tmp, "my-repo")
	subDir := filepath.Join(repoDir, "pkg", "engine")
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitCfg := "[remote \"origin\"]\n\turl = https://gitee.com/acme/my-repo.git\n[user]\n\tname = AliceDev\n"
	if err := os.WriteFile(filepath.Join(repoDir, ".git", "config"), []byte(gitCfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if gotLocus := DetectGitWorkspaceLocus(subDir); gotLocus != "acme/my-repo/pkg/engine" {
		t.Fatalf("DetectGitWorkspaceLocus got %q, want acme/my-repo/pkg/engine", gotLocus)
	}
	if gotUser := DetectGitWorkspaceUser(subDir); gotUser != "AliceDev" {
		t.Fatalf("DetectGitWorkspaceUser got %q, want AliceDev", gotUser)
	}
	variants := CanonicalAssistantScopeVariants("mem:self", "hupan")
	if len(variants) != 3 || variants[0] != "mem:self" || variants[1] != "default" || variants[2] != "assistant:hupan" {
		t.Fatalf("CanonicalAssistantScopeVariants(mem:self, hupan) = %v", variants)
	}
	variants2 := CanonicalAssistantScopeVariants("assistant:front-5085", "")
	if len(variants2) != 4 || variants2[0] != "assistant:front-5085" || variants2[1] != "default" || variants2[2] != "mem:self" || variants2[3] != "person:front-5085" {
		t.Fatalf("CanonicalAssistantScopeVariants(assistant:front-5085) = %v", variants2)
	}

	// 7. Connectivity metrics, candidate speaker entity & pre-probe hints
	if !IsCandidateSpeakerEntity("Alice") || !IsCandidateSpeakerEntity("Melanie Smith") {
		t.Fatal("expected Alice and Melanie Smith to be valid speaker entities")
	}
	if IsCandidateSpeakerEntity("SymPyPrinter") || IsCandidateSpeakerEntity("internal/memcore") || IsCandidateSpeakerEntity("user") {
		t.Fatal("expected camelCase / path / role placeholder to be rejected as speaker entity")
	}
	cm := ComputeConnectivityMetrics([]Assertion{
		{SubjectID: "n1", RelID: "r1", ObjectID: "n2"},
		{SubjectID: "n1", RelID: "r1", ObjectID: "n3"},
		{SubjectID: "n2", RelID: "r2", ObjectLiteral: "val"},
	}, map[string]string{"n1": "referent", "n2": "referent", "n3": "referent"}, map[string]string{
		"n1": "Alice",
		"n2": "Bob",
		"n3": "one two three four five six seven eight nine ten eleven",
	})
	if cm.TotalAssertions != 3 || cm.ObjectNodeCount != 2 || cm.MultiEdgeEntityCount != 2 || cm.ClauseReferentSlots != 1 || cm.AvgRelationReuse != 1.5 {
		t.Fatalf("unexpected ConnectivityMetrics: %+v", cm)
	}
	ents, rels := SelectPreProbeConnectivityHints([]string{"Alice", "alice", "Bob"}, map[string]int{"uses": 2, "likes": 5}, 20, 16)
	if len(ents) != 2 || len(rels) != 2 || rels[0] != "likes" {
		t.Fatalf("unexpected SelectPreProbeConnectivityHints: ents=%v rels=%v", ents, rels)
	}
}

func TestSliceRunesBoundedAndScoreAndAssembleBandsSlotMemo(t *testing.T) {
	cases := []struct {
		name       string
		value      string
		start, end int
		want       string
	}{
		{name: "ascii inner span", value: "abcdef", start: 1, end: 3, want: "bc"},
		{name: "ascii full span", value: "abcdef", start: 0, end: 6, want: "abcdef"},
		{name: "strict end beyond rune count", value: "abcdef", start: 0, end: 7, want: ""},
		{name: "start beyond rune count", value: "abcdef", start: 6, end: 7, want: ""},
		{name: "empty span", value: "abcdef", start: 3, end: 3, want: ""},
		{name: "reversed span", value: "abcdef", start: 4, end: 2, want: ""},
		{name: "negative start", value: "abcdef", start: -1, end: 3, want: ""},
		{name: "cjk multi-byte", value: "老张养了一只猫。", start: 2, end: 5, want: "养了一"},
		{name: "mixed emoji astral", value: "a😀b猫c", start: 1, end: 4, want: "😀b猫"},
		{name: "astral beyond end", value: "a😀b猫c", start: 4, end: 9, want: ""},
	}
	for _, tc := range cases {
		if got := SliceRunesBounded(tc.value, tc.start, tc.end); got != tc.want {
			t.Fatalf("%s: SliceRunesBounded(%q, %d, %d) = %q, want %q", tc.name, tc.value, tc.start, tc.end, got, tc.want)
		}
	}

	items := make([]ScoredAssertion, 16)
	for i := range items {
		items[i] = ScoredAssertion{
			Assertion:   Assertion{NodeID: "a_1", SubjectID: "s_1", RelID: "r_1", ObjectID: "o_1", Status: "active", TSource: 1700000000},
			SubjectName: "UnifiedMemoryKernel",
			RelName:     "optimizes",
			ObjectName:  "CandidateLexicalScoring",
			Snippet:     "UnifiedMemoryKernel optimizes CandidateLexicalScoring with slot memoization",
		}
	}
	bands := ScoreAndAssembleBands("UnifiedMemoryKernel CandidateLexicalScoring", items, 12)
	if len(bands) != 1 || len(bands[0].Rows) == 0 || bands[0].Rows[0].Score <= 0 {
		t.Fatalf("expected ScoreAndAssembleBands to score shared-slot items > 0, got %+v", bands)
	}
	for i := 1; i < len(bands[0].Rows); i++ {
		if bands[0].Rows[i].Score != bands[0].Rows[0].Score {
			t.Fatalf("expected identical candidate items to receive identical score: %v vs %v", bands[0].Rows[i].Score, bands[0].Rows[0].Score)
		}
	}

	// Verify ShouldAttachFollowupClip second-protagonist band quota protection (curBandIdx==1, curChainIdx<=12, origNonSubj, score>=12.0)
	if !ShouldAttachFollowupClip(1, 5, 30, 15.0, true, true) {
		t.Fatalf("expected curBandIdx=1 top chain with origNonSubj and score>=12.0 to retain followup quota up to 34")
	}
	if ShouldAttachFollowupClip(0, 5, 30, 15.0, true, true) {
		t.Fatalf("expected curBandIdx=0 to stop at capCnt=28 when followupCnt=30")
	}
}
