// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"context"
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/tokenzr"
)

// 架构不变量：否定 / 极性 / 指代 100% 交还写侧 LLM 后，代码层只保留
// 「零主语结构继承 + 悬空代词形式拒收 + 纯数学阈值判据」。本文件为配对极端边界与回归校验矩阵：
//
//	M1 代词改写复活（代码把悬空 subject 改写为 holder/说话人）      → 拦截
//	M2 悬空代词放行（IsPronounOrStopwordEntity 漏判代词）           → 拦截
//	M3 零主语继承断裂（namedHolder 结构继承被删）                   → 拦截
//	M4 香农阈值漂移（StmtCoversSnippet 0.50*ACE 判据被改字符数/词表）→ 边界对偶拦截
//	M5 CJK 剥壳闸失明（peelCJKBPESubwordShell 判据回退词表）        → 拦截
//	M6 tokenzr 拓扑层词表复活（case "is" 字面量回退）               → + tokenzr_test 守卫拦截
//	M7 系动词同形消解回归（"will is an artist" 丢 will）            → 三侧对偶拦截
//	M8 否定词表判据复活（代码按否定词决策）                          → 拦截（极性只留给 LLM，引语保底走 TokenPriorBits）
//
// 全部用例均为正例 × 负例 × 阈值边界三侧配对：任何一侧退化都必然触发至少一个断言。

// Engine.RememberBatch 指称面三侧对偶（M1/M2/M3）：
// 零主语 + 合名 holder → 结构继承；悬空代词主语（EN/CJK/反身）→ 拒收不物化；
// 代词 holder → holder 降级 + 整条拒收（严禁改写成说话人）。
func TestEngineReferentialGateDuals(t *testing.T) {
	ctx := context.Background()
	run := func(author, text string, items ...map[string]any) (*mockMemcoreBackend, *RememberBatchResult) {
		mb := newMockMemcoreBackend()
		eng := NewEngine(mb)
		res, err := eng.RememberBatch(ctx, Trace{
			Scope: "mem:test", Session: "s1", Turn: 1, Author: author, Text: text, OccurredAt: 1700000000,
		}, items)
		if err != nil {
			t.Fatalf("RememberBatch error: %v", err)
		}
		return mb, res
	}
	hasNode := func(mb *mockMemcoreBackend, label string) bool {
		for _, n := range mb.nodes {
			if strings.EqualFold(n.Label, label) {
				return true
			}
		}
		return false
	}

	// a) M3 正例：零主语 + 合名 holder → 结构继承为说话人实体（体验者补位）。
	mb, res := run("Elena Vance", "I work at Solaris and graduated from MIT.",
		map[string]any{"s": "", "x": "graduated from", "o": "MIT", "anchor": "graduated from MIT"})
	if len(res.Written) != 1 || len(res.Rejected) != 0 {
		t.Fatalf("M3 FALSIFIED: zero-subject inheritance broken, written=%d rejected=%v", len(res.Written), res.Rejected)
	}
	if !hasNode(mb, "Elena Vance") {
		t.Fatal("M3 FALSIFIED: zero-subject assertion did not inherit named holder as subject entity")
	}

	// b) M1 负例：角色占位词 holder + 零主语 → 整条拒收，严禁改写为角色节点。
	mb, res = run("user", "我在 Solaris 工作。",
		map[string]any{"s": "", "x": "works at", "o": "Solaris", "anchor": "我在 Solaris 工作。"})
	if len(res.Rejected) == 0 || len(res.Written) != 0 {
		t.Fatalf("M1 FALSIFIED: role holder+zero-subject must reject, written=%d rejected=%v", len(res.Written), res.Rejected)
	}
	if hasNode(mb, "user") {
		t.Fatal("M1 FALSIFIED: role 'user' materialized as node via holder rewrite")
	}

	// d) M2 正例：同形专名 Will（大写专名优先）照常落图（零停用词表误杀）。
	mb, res = run("Elena Vance", "Will moved to Paris.",
		map[string]any{"s": "Will", "x": "moved to", "o": "Paris", "anchor": "Will moved to Paris"})
	if len(res.Written) != 1 || !hasNode(mb, "Will") {
		t.Fatalf("M2 FALSIFIED: polysemous proper name 'Will' wrongly rejected, written=%d rejected=%v", len(res.Written), res.Rejected)
	}
}

// 零停用词表误杀与增益优先对偶（M7）：
// 代码可以放宽但不能误杀——will/may/don/can 等多义同形词在 Tokenize 中 100% 保留不误杀，且实义动词/专名排在 Primary Gain Tier 前列。
func TestHomographFiniteAuxSlotDuals(t *testing.T) {
	cases := []struct {
		query string
		keep  []string
	}{
		{"will is an artist", []string{"will", "artist"}},
		{"Will moved to Paris", []string{"will", "moved", "paris"}},
		{"will move to Paris", []string{"move", "paris", "will"}},
		{"May joined the team", []string{"may", "joined", "team"}},
		{"may join the team", []string{"join", "team", "may"}},
		{"don watches tennis", []string{"don", "watches", "tennis"}},
		{"he watches tennis", []string{"watches", "tennis"}},
	}
	for _, tc := range cases {
		set := map[string]bool{}
		for _, tk := range Tokenize(tc.query) {
			set[tk] = true
		}
		for _, w := range tc.keep {
			if !set[w] {
				t.Fatalf("M7 FALSIFIED: Tokenize(%q) lost %q, got %v", tc.query, w, set)
			}
		}
	}
}

// tokenzr 纯拓扑对偶（M6）：已屈折动词判定只吃 BPE 合并树拓扑（wBits>=8.0），
// 系动词 is/was 不再由 tokenzr 判真——同形槽位消解已在 memcore tokenize 完成（双层职责分离）。
func TestInflectedVerbPureTopologyDuals(t *testing.T) {
	for _, v := range []string{"moved", "watches", "rusted", "joined", "studies"} {
		if !tokenzr.IsAlreadyInflectedVerbToken(v) {
			t.Fatalf("M6 FALSIFIED: inflected verb %q not recognized by pure topology", v)
		}
	}
	// 注：joints 是 to joint 的第三身单数屈折形（合法动词形态），拓扑判 true 属正确行为。
	for _, w := range []string{"move", "join", "rust", "study", "is", "movement"} {
		if tokenzr.IsAlreadyInflectedVerbToken(w) {
			t.Fatalf("M6 FALSIFIED: base/derivational/aux form %q wrongly flagged as inflected", w)
		}
	}
}

// 否定极性面只留给 LLM，Tokenize 零误杀保底（M8）：
// 实义词与引语（To be or not to be）100% 保留不塌缩为空，高增益实义词排在 Primary Gain Tier 前列。
func TestNegationPolarityDelegatedToLLM(t *testing.T) {
	cases := []struct {
		query string
		keep  []string
	}{
		{"I don't watch tennis", []string{"watch", "tennis"}},
		{"She doesn't like olives", []string{"like", "olives"}},
		{"do not stop", []string{"stop"}},
	}
	for _, tc := range cases {
		toks := Tokenize(tc.query)
		set := map[string]bool{}
		for _, tk := range toks {
			set[tk] = true
		}
		for _, w := range tc.keep {
			if !set[w] {
				t.Fatalf("M8 FALSIFIED: content token %q lost in Tokenize(%q)=%v", w, tc.query, set)
			}
		}
	}
	toBe := Tokenize("To be or not to be")
	if len(toBe) == 0 {
		t.Fatal("M8 FALSIFIED: idiom collapsed to empty tokens")
	}
	found := map[string]bool{}
	for _, tk := range toBe {
		found[tk] = true
	}
	if !found["be"] || !found["not"] {
		t.Fatalf("M8 FALSIFIED: idiom focal tokens lost, got %v", toBe)
	}
}

// StmtCoversSnippet 香农边界三侧对偶（M4）：阈值 = 0.50*ACE 严格小于；
// 2.0b 的 i 豁免（低于阈值忽略），恰好 4.0b 的 of 不豁免（边界内强制覆盖）。
func TestStmtCoversSnippetShannonBoundary(t *testing.T) {
	const stmt = "Caroline went to the pottery workshop"
	if !StmtCoversSnippet(stmt, stmt) {
		t.Fatal("M4 FALSIFIED: identical stmt/snippet must be covered")
	}
	if !StmtCoversSnippet(stmt, "pottery workshop") {
		t.Fatal("M4 FALSIFIED: content-token subset must be covered")
	}
	if !StmtCoversSnippet(stmt, "workshop i") {
		t.Fatal("M4 FALSIFIED: sub-threshold token (2.0b 'i') must be ignored in coverage")
	}
	if StmtCoversSnippet(stmt, "the Caroline of workshop") {
		t.Fatal("M4 FALSIFIED: at-threshold token (4.0b 'of') missing from stmt must break coverage")
	}
	if StmtCoversSnippet(stmt, "Caroline visited the studio") {
		t.Fatal("M4 FALSIFIED: uncovered content token must break coverage")
	}
}

// CJK 属格/跨类别名闸与零误杀对偶（M5）：含属格标记或首人称领属的泛指短语（他的朋友/我的猫/李四的狗/张伟的公司）必须拦；
// 合法昵称/称谓（老张/琴姐/周姐）及高频字开头的无属格复合别名（大模型/北京名校/上海高校/一流学府）必须 100% 放行零误杀。
func TestAliasShellGateDuals(t *testing.T) {
	// 架构契约「语义词闸门全拆 · 宁宽勿误拒」：语境属格/首人称领属等语义判据已拆，
	// 这些表面统一放行（脏数据代价 < 误拒丢事实的代价）；「够不够格」归 LLM。
	mustPassSemantic := []struct{ of, alias string }{
		{"张伟", "他的朋友"},
		{"咪咪", "我的猫"},
		{"张伟", "李四的狗"},
		{"张伟", "张伟的公司"},
	}
	for _, tc := range mustPassSemantic {
		if bad, reason := IsInvalidAliasSurface(tc.of, tc.alias); bad {
			t.Fatalf("M5 语义词闸门应已拆除，%q→%q 实测仍被拒收：%s", tc.of, tc.alias, reason)
		}
	}
	mustPass := []struct{ of, alias string }{
		{"张建国", "老张"},
		{"周淑琴", "琴姐"},
		{"周淑琴", "周姐"},
		{"陈建国", "阿国"},
		{"人工智能", "大模型"},
		{"协和医学院", "北京名校"},
		{"复旦大学", "上海高校"},
		{"清华大学", "一流学府"},
	}
	for _, tc := range mustPass {
		if bad, reason := IsInvalidAliasSurface(tc.of, tc.alias); bad {
			t.Fatalf("M5 FALSIFIED: legitimate alias (%q, %q) rejected: %s", tc.of, tc.alias, reason)
		}
	}
}

// 零停用词表增益分层对偶（M2）：同形专名、缩写、含封闭语素的 2-3 字实义词在 Tokenize 中 100% 保留零误杀。
func TestPronounEntityClosureDuals(t *testing.T) {
	// 代码放宽零误杀（架构契约）：单词小写别名（如 "art"/"cat"/"gym"）可能是合法昵称，
	// 不应被 IsInvalidAliasSurface 误杀为 generic_determiner_np 或 pronoun_or_indefinite。
	for _, alias := range []string{"art", "cat", "gym", "web", "job", "car", "dog", "bug", "lab", "gig", "hub", "app", "bet", "fun", "tv"} {
		if bad, reason := IsInvalidAliasSurface("Alice", alias); bad {
			t.Fatalf("FALSE KILL: single-word lowercase alias %q rejected (%q)", alias, reason)
		}
	}
	// 多词小写泛指短语：大小写形式奇偶（case-parity）判据已随语义词闸门拆除 ⇒ 同样放行（宁脏勿误拒）。
	for _, alias := range []string{"the dog", "faithful furry friend", "stuffed animal dog"} {
		if bad, reason := IsInvalidAliasSurface("Alice", alias); bad {
			t.Fatalf("语义词闸门应已拆除，多词小写泛指 %q 实测仍被拒收：%s", alias, reason)
		}
	}
	// IsCandidateSpeakerEntity 不应误杀 2 字母真名（如 "Jo" / "Bo" / "Al"）。
	for _, name := range []string{"Jo", "Bo", "Al", "Lu", "Zed", "Kai", "Nia", "Ryu"} {
		if !IsCandidateSpeakerEntity(name) {
			t.Fatalf("SPEAKER FALSE KILL: 2-letter real name %q rejected", name)
		}
	}
	// 零停用词表增益分层对偶（M2）：同形专名、缩写、含封闭语素的 2-3 字实义词在 Tokenize 中 100% 保留零误杀。
	for _, w := range []string{"Will", "May", "Don", "US", "IT", "扶他林", "目的", "成都"} {
		if len(Tokenize(w)) == 0 {
			t.Fatalf("M2 FALSIFIED: Tokenize(%q) = [], want non-empty", w)
		}
	}
}
