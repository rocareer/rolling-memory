// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"context"
	"strings"
	"testing"
)

// TestCoreferenceFalsificationBattery preregisters and falsifies the coreference invariants in internal/memcore.
func TestCoreferenceFalsificationBattery(t *testing.T) {
	// ExpandInlineSubAssertions must remap explicit @k indices when earlier items expand inline sub_assertions.
	t.Run("ExpandInlineSubAssertions_AtRefOffsetRemap", func(t *testing.T) {
		batch := []map[string]any{
			{
				"src":    1,
				"s":      "Melanie",
				"x":      "said",
				"anchor": "Melanie said Caroline painted a sunrise.",
				"sub_assertion": map[string]any{
					"s": "Caroline",
					"x": "painted",
					"o": "a sunrise",
				},
			},
			{
				"src":    1,
				"s":      "Caroline",
				"x":      "won",
				"o":      "first prize",
				"anchor": "Caroline won first prize.",
			},
			{
				"src":    1,
				"s":      "Melanie",
				"x":      "celebrated",
				"o":      "@2", // refers to original item #2 ("Caroline won first prize"), which becomes item #3 after item #1 expands!
				"anchor": "Melanie celebrated that Caroline won first prize.",
			},
		}
		flat := ExpandInlineSubAssertions(batch)
		if len(flat) != 4 {
			t.Fatalf("expected 4 flattened assertions, got %d", len(flat))
		}
		// flat[0] = Caroline painted a sunrise (@1)
		// flat[1] = Melanie said @1 (@2, original #1)
		// flat[2] = Caroline won first prize (@3, original #2)
		// flat[3] = Melanie celebrated @3 (@4, original #3)
		if got := flat[1]["o"]; got != "@1" {
			t.Fatalf("flat[1] (outer of item #1) o = %v, want @1", got)
		}
		if got := flat[3]["o"]; got != "@3" {
			t.Fatalf("Invariant FALSIFIED: flat[3] (original item #3 referencing @2) o = %v, want @3 (shifted by inline expansion)", got)
		}
	})

	// F3b: ResolveBatchAtRefString must not blindly attach self-reference @(selfIdx+1) or cross-src out-of-bounds @k
	// to an unrelated assertion from a different src turn.
	t.Run("ResolveBatchAtRefString_NoCrossSrcOrSelfPollution", func(t *testing.T) {
		arr := []map[string]any{
			{"src": 1, "s": "Alice", "x": "moved to", "o": "Boston", "anchor": "Alice moved to Boston."},
			{"src": 2, "s": "Bob", "x": "said", "o": "@99", "anchor": "Bob said something else."},
		}
		batchIDs := []string{"101", ""}
		got := ResolveBatchAtRefString("@99", arr, batchIDs, 1, "Bob said something else.")
		if got == "#101" {
			t.Fatalf("F3b FALSIFIED: cross-src @99 in src=2 falsely attached to src=1 assertion #101")
		}
	})

	// F5（口径已更新 · 架构契约「语义词闸门全拆 · 宁宽勿误拒」）：
	// 语境指代 / 跨类属格 / 指示短语等**语义**判据已从 IsInvalidAliasSurface 拆除，这些表面必须放行；
	// 代码只保留机械契约（空 / 保留标识符 / 非载体 / 超帽与标点），语义资格 100% 归 LLM。
	t.Run("IsInvalidAliasSurface_ChinesePossessiveAndDeictic", func(t *testing.T) {
		semanticCases := []struct {
			of    string
			alias string
		}{
			{"张伟", "他的朋友"},
			{"咪咪", "我的猫"},
			{"李雷", "你的老师"},
			{"王芳", "她的妹妹"},
			{"开发组", "我们的团队"},
			{"张伟", "李四的狗"},
			{"张伟", "张伟的公司"},
			{"Mittens", "my Persian"},
			{"陈若宁", "穿浅色上衣、留长发的女性"},
			{"Emily Katherine Harris", "Harris Family Check-In participant using bright blue message bubbles"},
		}
		for _, tc := range semanticCases {
			if bad, reason := IsInvalidAliasSurface(tc.of, tc.alias); bad {
				t.Fatalf("F5 语义词闸门应已拆除，%q→%q 实测仍被拒收：%s", tc.of, tc.alias, reason)
			}
		}
		for _, badRel := range [][2]string{
			{"Michael Robert Thompson", "Dad"},
			{"Melissa Grant", "Mommy"},
			{"李春霞", "张宏"},
		} {
			if ok, _ := AliasBasisGate(AliasSpec{Of: badRel[0], Alias: badRel[1], Basis: "relation"}); ok {
				t.Fatalf("Alias-surface invariant FALSIFIED: AliasBasisGate(%q, %q, relation) = true, want false", badRel[0], badRel[1])
			}
		}
		// Positive controls: legitimate Chinese nicknames/titles and high-frequency-opener proper aliases must never be false-killed!
		goodCases := [][2]string{
			{"陈建国", "阿国"},
			{"张建国", "老张"},
			{"李晓明", "小明"},
			{"人工智能", "大模型"},
			{"协和医学院", "北京名校"},
			{"复旦大学", "上海高校"},
			{"清华大学", "一流学府"},
		}
		for _, gc := range goodCases {
			if bad, reason := IsInvalidAliasSurface(gc[0], gc[1]); bad {
				t.Fatalf("positive control failed: IsInvalidAliasSurface(%q, %q) = true (%s), want false", gc[0], gc[1], reason)
			}
		}
	})

	// Permissive zero-stopword-table preservation ("代码可以放宽但不能误杀 · 香农只负责增益"):
	// Chinese 2-3 char content words, uppercase acronyms, and TitleCase homograph proper names are 100% preserved by Tokenize and IsHighGainToken.
	t.Run("ZeroStopwordFalseKillPreservation", func(t *testing.T) {
		for _, keep := range []string{"扶他林", "维他命", "成都", "地铁", "过敏", "阳光", "目的", "了解", "哈尔滨", "US", "IT", "WHO", "Will", "Don", "May"} {
			if len(Tokenize(keep)) == 0 {
				t.Fatalf("False kill: Tokenize(%q) = [], want non-empty", keep)
			}
		}
	})

	// Tokenize must NOT false-trigger or leak single-letter 'i' from English words merely containing 'i' or 'I'.
	t.Run("Tokenize_NoLetterIFalsePositive", func(t *testing.T) {
		for _, txt := range []string{
			"Bob went to Paris in 2023.",
			"The service is running smoothly.",
			"In May 2022, built a new bridge downtown.",
			"Alice and David visited Madrid.",
		} {
			for _, tk := range Tokenize(txt) {
				if tk == "i" {
					t.Fatalf("Letter-I invariant FALSIFIED: Tokenize(%q) leaked single-letter 'i'", txt)
				}
			}
		}
	})

	// Engine.RememberBatch coreference normalization, zero-subject ego fill, dangling pronoun gate, and forward @k resolution.
	t.Run("EngineRememberBatch_CoreferenceAndForwardAtRef", func(t *testing.T) {
		mb := newMockMemcoreBackend()
		eng := NewEngine(mb)
		ctx := context.Background()
		res, err := eng.RememberBatch(ctx, Trace{
			Scope:      "mem:self",
			Session:    "s1",
			Turn:       1,
			Author:     "Elena Vance",
			Text:       "I work at Solaris and graduated from MIT. He also said that Marcus joined Solaris.",
			OccurredAt: 1700000000,
		}, []map[string]any{
			// 1) LLM-resolved speaker entity "Elena Vance"
			{"s": "Elena Vance", "x": "works at", "o": "Solaris", "anchor": "I work at Solaris"},
			// 2) Zero-subject omission (s="") -> must fill with speaker "Elena Vance"
			{"s": "", "x": "graduated from", "o": "MIT", "anchor": "graduated from MIT"},
			// 3) Forward @k reference: item #3 references @4 before item #4 is listed
			{"s": "Elena Vance", "x": "reported", "o": "@4", "anchor": "said that Marcus joined Solaris"},
			{"s": "Marcus", "x": "joined", "o": "Solaris", "anchor": "Marcus joined Solaris"},
			// 4) Role placeholders ("user", "assistant") -> cleared to holder_gap
			{"s": "", "holder": "user", "x": "said", "o_literal": "something", "anchor": "He also said"},
		})
		if err != nil {
			t.Fatalf("RememberBatch error: %v", err)
		}
		if len(res.Written) != 4 || len(res.Rejected) != 1 {
			t.Fatalf("Coreference invariant FALSIFIED: expected 4 valid assertions written and 1 unbound role assertion rejected, got written=%d rejected=%v", len(res.Written), res.Rejected)
		}
		for _, node := range mb.nodes {
			if strings.EqualFold(node.Label, "user") || strings.HasPrefix(node.Label, "@") {
				t.Fatalf("Coreference invariant FALSIFIED: polluted role/ref node materialized in graph: %+v", node)
			}
		}
		// Check that item #3 ("Elena Vance reported @4") resolved its ObjectID to item #4's assertion NodeID!
		var reportedObjID, marcusAssertNodeID string
		for _, a := range res.Written {
			if a.RelID != "" && mb.nodes[a.RelID].Label == "joined" {
				marcusAssertNodeID = a.NodeID
			}
			if a.RelID != "" && mb.nodes[a.RelID].Label == "reported" {
				reportedObjID = a.ObjectID
			}
		}
		if marcusAssertNodeID == "" || reportedObjID != marcusAssertNodeID {
			t.Fatalf("F3a FALSIFIED: forward @4 reference did not resolve to Marcus assertion node ID %q, got ObjectID=%q", marcusAssertNodeID, reportedObjID)
		}
	})
}

type mockMemcoreBackend struct {
	seq   int
	nodes map[string]*Node
}

func newMockMemcoreBackend() *mockMemcoreBackend {
	return &mockMemcoreBackend{nodes: make(map[string]*Node)}
}

func (m *mockMemcoreBackend) Dialect() string                              { return "mock" }
func (m *mockMemcoreBackend) PurgeScope(_ context.Context, _ string) error { return nil }
func (m *mockMemcoreBackend) AppendTrace(_ context.Context, t Trace) (*Trace, bool, error) {
	t.Hash = "th_mock_1"
	return &t, true, nil
}
func (m *mockMemcoreBackend) EnsureNode(_ context.Context, scope, kind, label, audience, world string, now int64) (*Node, bool, error) {
	for _, n := range m.nodes {
		if n.Scope == scope && n.Kind == kind && strings.EqualFold(n.Label, label) {
			return n, false, nil
		}
	}
	m.seq++
	id := "n_" + string(rune('0'+m.seq))
	if m.seq >= 10 {
		id = "n_1" + string(rune('0'+(m.seq%10)))
	}
	n := &Node{ID: id, Scope: scope, Kind: kind, Label: label, NormLabel: strings.ToLower(label), Status: "active", Audience: audience, World: world, CreatedAt: now}
	m.nodes[id] = n
	return n, true, nil
}
func (m *mockMemcoreBackend) FindNodeByName(_ context.Context, scope, name string) (*Node, bool, error) {
	for _, n := range m.nodes {
		if n.Scope == scope && n.Kind != "assertion" && strings.EqualFold(n.Label, name) {
			return n, true, nil
		}
	}
	return nil, false, nil
}
func (m *mockMemcoreBackend) BindAlias(_ context.Context, _, _, _, _ string, _ int64) (bool, error) {
	return true, nil
}
func (m *mockMemcoreBackend) AppendAssertion(_ context.Context, a Assertion, label string) (*Assertion, bool, error) {
	m.seq++
	id := "a_" + string(rune('0'+m.seq))
	a.NodeID = id
	m.nodes[id] = &Node{ID: id, Scope: a.Scope, Kind: "assertion", Label: label, Status: "active"}
	return &a, true, nil
}
func (m *mockMemcoreBackend) ClosePriorAssertions(_ context.Context, _, _, _, _, _, _ string, _, _ int64) ([]string, error) {
	return nil, nil
}
func (m *mockMemcoreBackend) RecordRestate(_ context.Context, _ RestateRow) error {
	return nil
}
func (m *mockMemcoreBackend) RecordMemoryHot(_ context.Context, _ MemoryHot) error {
	return nil
}
func (m *mockMemcoreBackend) RecentMemoryHot(_ context.Context, _, _ string, _ int) ([]MemoryHot, error) {
	return nil, nil
}
func (m *mockMemcoreBackend) UpsertEpisodeContainer(_ context.Context, _ EpisodeContainer, _ []EpisodeMember) error {
	return nil
}
func (m *mockMemcoreBackend) ListEpisodeContainers(_ context.Context, _ string, _ int, _ int) ([]EpisodeContainer, error) {
	return nil, nil
}

func TestSingleTitleCaseGivenNameShannonBitBounds(t *testing.T) {
	if StringShannonBits("Max") < 0.5*AtomicConceptEntropyBits || StringShannonBits("Christopher") > 3.0*AtomicConceptEntropyBits {
		t.Fatal("架构不变量: valid given name bit bounds failed")
	}
	if StringShannonBits("Internationalization") <= 3.0*AtomicConceptEntropyBits {
		t.Fatal("架构不变量: expected overlong identifier > 3.0*AtomicConceptEntropyBits")
	}
}

func TestWriteSideZeroFalseKillPossessiveAndRelationEquivDuals(t *testing.T) {
	// 1. ExtractPossessiveOwner: 2-letter real names & Japanese 'の' must pass; pronouns & lowercase common nouns must be rejected.
	for _, tc := range [][2]string{
		{"Jo's dog", "Jo"},
		{"Bo's cat", "Bo"},
		{"Al's car", "Al"},
		{"张三的豆豆", "张三"},
		{"太郎の猫", "太郎"},
	} {
		if got := ExtractPossessiveOwner(tc[0]); got != tc[1] {
			t.Fatalf("ExtractPossessiveOwner(%q) = %q, want %q", tc[0], got, tc[1])
		}
	}
	for _, neg := range []string{"my dog", "his cat", "children's hospital", "user's file"} {
		if got := ExtractPossessiveOwner(neg); got != "" {
			t.Fatalf("ExtractPossessiveOwner(%q) = %q, want empty", neg, got)
		}
	}

	// 2. AreRelationsMorphologicallyEquivalent: must not conflate opposing low-gain prepositions/particles ("worked with" vs "worked without").
	if AreRelationsMorphologicallyEquivalent("worked with", "worked without") {
		t.Fatal("AreRelationsMorphologicallyEquivalent falsely equated 'worked with' and 'worked without'")
	}
	if !AreRelationsMorphologicallyEquivalent("worked at", "works at") {
		t.Fatal("AreRelationsMorphologicallyEquivalent failed on inflected 'worked at' vs 'works at'")
	}

	// 3. IsInvalidAliasSurface: 2-char Chinese proper name containing '的' ("美的"), 3-char heteronym alias ("马铃薯"->"洋芋头"),
	// 2-letter prefix shortening ("Evan"->"Ev"), and single uppercase initial ("James"->"J") must pass.
	for _, okPair := range [][2]string{
		{"Midea", "美的"},
		{"马铃薯", "洋芋头"},
		{"Evan", "Ev"},
		{"James", "J"},
	} {
		if bad, reason := IsInvalidAliasSurface(okPair[0], okPair[1]); bad {
			t.Fatalf("IsInvalidAliasSurface(%q, %q) falsely rejected as %q", okPair[0], okPair[1], reason)
		}
	}
	// 4. 上下位嵌入判据（"别名为宿主+小写修饰词/续集标题"）已随语义词闸门一并拆除：宁脏勿误拒、零丢弃。
	if bad, reason := IsInvalidAliasSurface("哈利波特", "哈利波特与混血王子上下册全集"); bad {
		t.Fatalf("上下位嵌入判据应已拆除，实测仍拒收：bad=%v reason=%q", bad, reason)
	}
}
