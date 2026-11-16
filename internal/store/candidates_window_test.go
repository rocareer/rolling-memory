// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package store

import (
	"fmt"
	"strings"
	"testing"
)

// TestRawTracesQuestionAnswerPair pins the raw-leg 问答对 expansion: a hit whose
// text is a question carries the next turn of the same session (the answer),
// while a hit that is not a question stays single-turn. The expansion must not
// cross sessions and must not duplicate a turn already delivered.
func TestRawTracesQuestionAnswerPair(t *testing.T) {
	store := newTestStore(t, testScope)
	session := "s1"
	add := func(text string, turn int64) {
		if _, _, err := store.AddTrace(testScope, text, TraceOptions{
			Session: session, Turn: turn, Author: "user",
		}); err != nil {
			t.Fatalf("add trace %q: %v", text, err)
		}
	}
	add("Where did you go last weekend?", 1)
	add("I went hiking near the lake with my kids.", 2)
	add("Did anything funny happen on the trail?", 3)
	add("A squirrel stole my sandwich and the kids loved it.", 4)

	hits, err := store.RawTraces("Where did you go last weekend", RawOptions{
		Scope: testScope, Reader: "deliverable", Limit: 1, Session: &session,
	})
	if err != nil {
		t.Fatalf("raw traces: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %d, want 1", len(hits))
	}
	if !strings.Contains(hits[0].Text, "Where did you go last weekend?") {
		t.Fatalf("anchor missing from hit text: %q", hits[0].Text)
	}
	if !strings.Contains(hits[0].Text, "I went hiking near the lake") {
		t.Fatalf("answer turn missing from hit text: %q", hits[0].Text)
	}

	// 非提问轮：只带自己那一轮（会话里没有更早的提问，也不追加后继）。
	plain, err := store.RawTraces("squirrel", RawOptions{
		Scope: testScope, Reader: "deliverable", Limit: 1, Session: &session,
	})
	if err != nil {
		t.Fatalf("raw traces (plain): %v", err)
	}
	if len(plain) != 1 {
		t.Fatalf("plain hits = %d, want 1", len(plain))
	}
	if strings.Contains(plain[0].Text, "Did anything funny happen") {
		t.Fatalf("non-question hit must stay single-turn: %q", plain[0].Text)
	}

	// 会话隔离：另一个会话的同轮次不得进入窗口。
	other := "s2"
	if _, _, err := store.AddTrace(testScope, "Squirrel sightings elsewhere.", TraceOptions{
		Session: other, Turn: 5, Author: "user",
	}); err != nil {
		t.Fatalf("add other-session trace: %v", err)
	}
	isolated, err := store.RawTraces("squirrel", RawOptions{
		Scope: testScope, Reader: "deliverable", Limit: 1, Session: &session,
	})
	if err != nil {
		t.Fatalf("raw traces (isolated): %v", err)
	}
	for _, hit := range isolated {
		if strings.Contains(hit.Text, "Squirrel sightings elsewhere.") {
			t.Fatalf("window crossed sessions: %q", hit.Text)
		}
	}
}

// TestRawTracesStemAndAuthorCoverage verifies ice-aligned stem fallback,
// speaker (author) attribution and complementary session union bonus.
func TestRawTracesStemAndAuthorCoverage(t *testing.T) {
	store := newTestStore(t, testScope)
	for i := 0; i < 8; i++ {
		if _, _, err := store.AddTrace(testScope, "Filler background conversation turn.", TraceOptions{
			Session: "s_bg", Turn: int64(i + 1), Author: "Alice",
		}); err != nil {
			t.Fatalf("add filler: %v", err)
		}
	}
	// Target turn uses "rejection" and "script" while speaker is in author="Joanna".
	if _, _, err := store.AddTrace(testScope, "Dealing with rejection on my second script was tough, happened twice.", TraceOptions{
		Session: "s_target", Turn: 10, Author: "Joanna",
	}); err != nil {
		t.Fatalf("add target: %v", err)
	}
	hits, err := store.RawTraces("How many times has Joanna's scripts been rejected?", RawOptions{
		Scope: testScope, Reader: "deliverable", Limit: 2,
	})
	if err != nil {
		t.Fatalf("raw traces: %v", err)
	}
	if len(hits) == 0 || hits[0].Session != "s_target" {
		t.Fatalf("expected s_target ranked first via stem+author, got %+v", hits)
	}

	// U4 lexicon retrieval expansion + write-side isolation: "screenplay" matches
	// query "scripts" on read, while EnsureNode keeps "script" and "screenplay"
	// as distinct entity nodes on write.
	n1, err := store.EnsureNode(testScope, "referent", "script", NodeOptions{})
	if err != nil {
		t.Fatalf("ensure script: %v", err)
	}
	n2, err := store.EnsureNode(testScope, "referent", "screenplay", NodeOptions{})
	if err != nil {
		t.Fatalf("ensure screenplay: %v", err)
	}
	if n1 == n2 {
		t.Fatalf("write-side isolation violated: script and screenplay merged into %s", n1)
	}
	// Zero-hit transposition typo ("torunament" -> "tournament")
	if _, _, err := store.AddTrace(testScope, "Nate won a big Valorant tournament last Saturday.", TraceOptions{
		Session: "s_typo", Turn: 20, Author: "Nate",
	}); err != nil {
		t.Fatalf("add typo target: %v", err)
	}
	typoHits, err := store.RawTraces("Which torunament did Nate win?", RawOptions{
		Scope: testScope, Reader: "deliverable", Limit: 2,
	})
	if err != nil {
		t.Fatalf("raw traces typo: %v", err)
	}
	if len(typoHits) == 0 || typoHits[0].Session != "s_typo" {
		t.Fatalf("expected s_typo ranked first via zero-hit typo correction, got %+v", typoHits)
	}

	// Compound subword split (tokenz-r SplitSubwords): a concatenated query token also
	// retrieves the split-written original (roadtrip ⇒ road + trip).
	if _, _, err := store.AddTrace(testScope, "We went on a road trip last weekend and loved it.", TraceOptions{
		Session: "s_compound", Turn: 30, Author: "Nate",
	}); err != nil {
		t.Fatalf("add compound target: %v", err)
	}
	compoundHits, err := store.RawTraces("When did the roadtrip happen?", RawOptions{
		Scope: testScope, Reader: "deliverable", Limit: 2,
	})
	if err != nil {
		t.Fatalf("raw traces compound: %v", err)
	}
	if len(compoundHits) == 0 || compoundHits[0].Session != "s_compound" {
		t.Fatalf("expected s_compound ranked first via compound split, got %+v", compoundHits)
	}
}

func TestRawTracesDialogueSymmetryAndCounterQuestion(t *testing.T) {
	store := newTestStore(t, testScope)
	for i := 0; i < 8; i++ {
		if _, _, err := store.AddTrace(testScope, fmt.Sprintf("Filler %d background conversation about guitar design and creating art.", i), TraceOptions{
			Session: "s_bg", Turn: int64(i + 1), Author: "Alice",
		}); err != nil {
			t.Fatalf("add filler: %v", err)
		}
	}
	// 1. Predecessor turn attachment on listener praise + trailing chitchat question
	if _, _, err := store.AddTrace(testScope, "Yes Dave, I had this custom made! It's got an octopus on it.", TraceOptions{
		Session: "s_guitar", Turn: 10, Author: "Calvin",
	}); err != nil {
		t.Fatalf("add calvin turn: %v", err)
	}
	if _, _, err := store.AddTrace(testScope, "That's a great guitar, Calvin! Love the design, it's so unique. What have you been up to?", TraceOptions{
		Session: "s_guitar", Turn: 11, Author: "Dave",
	}); err != nil {
		t.Fatalf("add dave turn: %v", err)
	}
	hits, err := store.RawTraces("What design is featured on Calvin's guitar?", RawOptions{
		Scope: testScope, Reader: "deliverable", Limit: 2,
	})
	if err != nil {
		t.Fatalf("raw traces guitar: %v", err)
	}
	if len(hits) == 0 || !strings.Contains(hits[0].Text, "octopus") {
		t.Fatalf("expected predecessor protagonist turn with 'octopus' attached, got %+v", hits)
	}
}

func TestRawTracesLongQueryTailAnchorNotTruncated(t *testing.T) {
	scope := "mem:test:long_tail"
	store := newTestStore(t, scope)
	// 填入 10 条含高频背景词的干扰记忆 + 1 条含句尾稀有专名“百花传统甜品店 / Abadia”的目标记忆
	for i := 1; i <= 10; i++ {
		_, _, err := store.AddTrace(scope, "我们在广州出差期间去过很多普通餐厅和咖啡馆，菜单种类丰富，服务体验也很好。", TraceOptions{
			Session: "s_noise", Turn: i, Author: "user",
		})
		if err != nil {
			t.Fatalf("add noise %d: %v", i, err)
		}
	}
	_, _, err := store.AddTrace(scope, "2024年8月19日专门去了百花传统甜品店，点了招牌双皮奶和凤凰奶糊。", TraceOptions{
		Session: "s_target", Turn: 1, Author: "user",
	})
	if err != nil {
		t.Fatalf("add target: %v", err)
	}

	// 超过 35 个汉字的长问句：前面全是背景铺垫（展开的 bigram/trigram 早已超过 48 个），真正关键的专名“百花传统甜品店”在句尾
	longQuery := "我记得之前在广州出差期间去过很多普通餐厅和咖啡馆，菜单种类丰富，服务体验也很好，请问我最后一次去百花传统甜品店是哪一天？"
	hits, err := store.RawTraces(longQuery, RawOptions{
		Scope: scope, Reader: "deliverable", Limit: 3,
	})
	if err != nil {
		t.Fatalf("RawTraces long query: %v", err)
	}
	if len(hits) == 0 || !strings.Contains(hits[0].Text, "百花传统甜品店") {
		t.Fatalf("expected tail proper noun '百花传统甜品店' to rank #1 without 48-term truncation, got %+v", hits)
	}
}

// TestWordBoundaryFalsePositives 验证词边界守卫与次线性词频统计：
// 1. "kids" 绝不伪匹配 "ids"，"Michaelmas" 绝不伪匹配精确 "michael"，"Timestamp" 绝不伪匹配 "st"；
// 2. 合法词边界出现多次时正确返回 tf 与首命中偏移 firstPos。
func TestWordBoundaryFalsePositives(t *testing.T) {
	if tf, _ := countWordPrefixOrExact("kids playing in the park", "ids", true); tf != 0 {
		t.Fatalf("expected 'kids' not to match 'ids', got tf=%d", tf)
	}
	if tf, _ := countWordPrefixOrExact("michaelmas term schedule", "michael", true); tf != 0 {
		t.Fatalf("expected 'michaelmas' not to match exact 'michael', got tf=%d", tf)
	}
	if tf, _ := countWordPrefixOrExact("timestamp: 2024-06-21", "st", true); tf != 0 {
		t.Fatalf("expected 'timestamp' not to match 'st', got tf=%d", tf)
	}
	tf, firstPos := countWordPrefixOrExact("[item_id: img_01] st michael's mount castle at st michael's bay", "michael", true)
	if tf != 2 || firstPos <= 0 || firstPos >= 40 {
		t.Fatalf("expected tf=2 and header-zone firstPos for 'michael', got tf=%d firstPos=%d", tf, firstPos)
	}
}

// TestLexemeFamilyIDFConservation 端到端验证词族守恒定律与首段摘要区共振：
// 当通用高频词干 "hotel" 在全库大量出现，而复数查询词 "hotels" 偶发罕见时，
// 含真专名 "Cairo" + 单数 "hotel" 的目标记忆必须排在仅含复数 "hotels" 的无关文档之前。
func TestLexemeFamilyIDFConservation(t *testing.T) {
	scope := "mem:test:lexeme_idf"
	store := newTestStore(t, scope)

	for i := 1; i <= 25; i++ {
		_, _, err := store.AddTrace(scope, "Booked a standard business hotel room in London with breakfast included.", TraceOptions{
			Session: fmt.Sprintf("s_bg_%d", i), Turn: 1,
		})
		if err != nil {
			t.Fatalf("add bg %d: %v", i, err)
		}
	}
	_, _, err := store.AddTrace(scope, "Newsletter: Top luxury hotels in Paris for winter holidays and spa retreats.", TraceOptions{
		Session: "s_distractor", Turn: 1,
	})
	if err != nil {
		t.Fatalf("add distractor: %v", err)
	}
	_, _, err = store.AddTrace(scope, "[item_id: EMAIL_CAIRO] Reservation confirmed at Steigenberger Hotel El Tahrir in Greater Cairo, Egypt.", TraceOptions{
		Session: "s_cairo", Turn: 1,
	})
	if err != nil {
		t.Fatalf("add target: %v", err)
	}

	hits, err := store.RawTraces("Which hotels did I stay at in Greater Cairo?", RawOptions{
		Scope: scope, Reader: "deliverable", Limit: 5,
	})
	if err != nil {
		t.Fatalf("RawTraces query: %v", err)
	}
	if len(hits) == 0 || !strings.Contains(hits[0].Text, "EMAIL_CAIRO") {
		t.Fatalf("expected Cairo hotel target to rank #1 over Paris hotels distractor, got %+v", hits)
	}
}
