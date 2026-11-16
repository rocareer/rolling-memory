// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package llm

import (
	"reflect"
	"strings"
	"testing"
)

func TestRuleExtractorCJK(t *testing.T) {
	extraction, err := RuleExtractor{}.Extract("我养了一只猫，叫小白。", "user", 1793102400)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(extraction.Assertions) != 1 {
		t.Fatalf("assertions = %#v", extraction.Assertions)
	}
	assertion := extraction.Assertions[0]
	if assertion["s"].(map[string]any)["label"] != "我" {
		t.Fatalf("subject = %#v", assertion["s"])
	}
	if assertion["x"] != "养" {
		t.Fatalf("relation = %#v", assertion["x"])
	}
	if assertion["o"].(map[string]any)["label"] != "猫" {
		t.Fatalf("object = %#v", assertion["o"])
	}
	if assertion["warrant"] != "experiential" {
		t.Fatalf("warrant = %#v", assertion["warrant"])
	}
	if assertion["confidence"] != 0.35 {
		t.Fatalf("confidence = %#v", assertion["confidence"])
	}
	if extraction.Warning == "" {
		t.Fatal("expected the conservative-pattern warning")
	}
}

// TestRuleExtractorChineseExtendedPatterns verifies Chinese rule-based triple extraction.
func TestRuleExtractorChineseExtendedPatterns(t *testing.T) {
	text := "张三出生于1990年。李四搬到了上海。王五毕业于北京大学。"
	extraction, err := RuleExtractor{}.Extract(text, "我", 1790000000)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	triples := map[string]bool{}
	for _, row := range extraction.Assertions {
		triples[tripleKey(t, row)] = true
	}
	for _, want := range []string{"张三|出生于|1990年", "李四|搬到|上海", "王五|毕业于|北京大学"} {
		if !triples[want] {
			t.Errorf("missing triple %q in %v", want, triples)
		}
	}
}

// TestRuleExtractorEnglishExtendedPatterns verifies English rule-based triple extraction.
func TestRuleExtractorEnglishExtendedPatterns(t *testing.T) {
	text := "Alice was born in 1990. Bob moved to Shanghai. Carol graduated from MIT."
	extraction, err := RuleExtractor{}.Extract(text, "我", 1790000000)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	triples := map[string]bool{}
	for _, row := range extraction.Assertions {
		triples[tripleKey(t, row)] = true
	}
	for _, want := range []string{"Alice|was born in|1990", "Bob|moved to|Shanghai", "Carol|graduated from|MIT"} {
		if !triples[want] {
			t.Errorf("missing triple %q in %v", want, triples)
		}
	}
}

// TestRuleExtractorEvidenceIsVerbatim verifies that extracted evidence is a verbatim substring of the source text.
func TestRuleExtractorEvidenceIsVerbatim(t *testing.T) {
	text := "张三出生于1990年。"
	extraction, _ := RuleExtractor{}.Extract(text, "我", 1790000000)
	if len(extraction.Assertions) == 0 {
		t.Fatalf("assertions = %#v", extraction.Assertions)
	}
	evidence, _ := extraction.Assertions[0]["evidence"].(string)
	if evidence == "" || !strings.Contains(text, evidence) {
		t.Fatalf("evidence %q is not a verbatim substring of %q", evidence, text)
	}
}

func tripleKey(t *testing.T, row map[string]any) string {
	t.Helper()
	subject := ""
	if fields, ok := row["s"].(map[string]any); ok {
		subject, _ = fields["label"].(string)
	}
	object := ""
	if fields, ok := row["o"].(map[string]any); ok {
		object, _ = fields["label"].(string)
	}
	relation, _ := row["x"].(string)
	return subject + "|" + relation + "|" + object
}

func TestRuleExtractorEnglishAndSpeaker(t *testing.T) {
	extraction, _ := RuleExtractor{}.Extract("I moved to Beijing.", "alice", 1793102400)
	if len(extraction.Assertions) != 1 {
		t.Fatalf("assertions = %#v", extraction.Assertions)
	}
	assertion := extraction.Assertions[0]
	if assertion["s"].(map[string]any)["label"] != "alice" {
		t.Fatalf("speaker was not resolved: %#v", assertion["s"])
	}
	if assertion["x"] != "moved to" {
		t.Fatalf("relation = %#v", assertion["x"])
	}
	if assertion["o"].(map[string]any)["label"] != "Beijing" {
		t.Fatalf("object = %#v", assertion["o"])
	}
}

func TestRuleExtractorSkipsQuestionNegationAndConditional(t *testing.T) {
	for _, text := range []string{
		"老张养了一只猫吗？",
		"老张没有养猫。",
		"如果老张养猫，就告诉我。",
	} {
		extraction, _ := RuleExtractor{}.Extract(text, "user", 1793102400)
		if len(extraction.Assertions) != 0 {
			t.Fatalf("%q produced %#v", text, extraction.Assertions)
		}
	}
}

func TestRuleExtractorCapsAssertions(t *testing.T) {
	lines := []string{}
	for _, name := range []string{"甲", "乙", "丙", "丁", "戊", "己", "庚", "辛", "壬", "癸", "子", "丑", "寅", "卯"} {
		lines = append(lines, name+"喜欢咖啡。")
	}
	extraction, _ := RuleExtractor{}.Extract(strings.Join(lines, ""), "user", 1793102400)
	if len(extraction.Assertions) > 12 {
		t.Fatalf("assertions = %d", len(extraction.Assertions))
	}
}

func TestParseJSONObjectFences(t *testing.T) {
	parsed := ParseJSONObject("```json\n{\"assertions\":[]}\n```")
	if parsed == nil || len(parsed) != 1 {
		t.Fatalf("parsed = %#v", parsed)
	}
	parsed = ParseJSONObject("prefix {\"s\":\"x\"} suffix")
	if parsed["s"] != "x" {
		t.Fatalf("parsed = %#v", parsed)
	}
	if ParseJSONObject("not json") != nil {
		t.Fatal("non-JSON should return nil")
	}
}

func TestNormalizeLLMOutput(t *testing.T) {
	parsed := map[string]any{
		"assertions": []any{
			map[string]any{
				"s": map[string]any{"label": "老张"}, "x": "养",
				"o": map[string]any{"label": "猫"}, "warrant": "made-up",
				"confidence": 2, "evidence": " 老张养了一只猫 ",
			},
			map[string]any{"s": map[string]any{"label": "老张"}, "x": "养"},
		},
		"unresolved": []any{"某处", map[string]any{"surface": "某人"}, map[string]any{"surface": ""}},
	}
	extraction := normalizeLLMOutput(parsed, "openai:test")
	if len(extraction.Assertions) != 1 {
		t.Fatalf("assertions = %#v", extraction.Assertions)
	}
	assertion := extraction.Assertions[0]
	if assertion["warrant"] != "referential" {
		t.Fatalf("warrant = %#v", assertion["warrant"])
	}
	if assertion["confidence"] != 1.0 {
		t.Fatalf("confidence = %#v", assertion["confidence"])
	}
	if assertion["evidence"] != "老张养了一只猫" {
		t.Fatalf("evidence = %#v", assertion["evidence"])
	}
	if extraction.AnchorFailed != 1 {
		t.Fatalf("anchor_failed = %d", extraction.AnchorFailed)
	}
	if !reflect.DeepEqual(extraction.Unresolved, []map[string]any{
		{"surface": "某处"}, {"surface": "某人"},
	}) {
		t.Fatalf("unresolved = %#v", extraction.Unresolved)
	}
}

// Real prose fragments (verbatim evidence spans from the live graph) that the
// patterns used to shred into 0.35-confidence triples.
func TestRuleExtractorSkipsProseFragments(t *testing.T) {
	junk := []string{
		"| 中文硬编码文案（JSX 文本 / 属性 / 代码串） | **0**（MemoryManageBar 那 2 处命中核实为**注释**，误报） |",
		"**已完成 1 项**（设置中心手写件六轴核查 + 缺口定位，证据为上面的逐件表）；",
		"数据也都在盘上：题目 /tmp/mem-eval/locomo200-questions-rebuilt-20261019.json（127 KB ✓）、语料 /tmp/mem-eval/corpus.jsonl（1.4 MB ✓）。",
		"tokens = `[我的猫叫什么名字 我的 的猫 猫叫 叫什 么名 名字]`",
		"但有三件事你始终要管：给目标和机械度量、划范围（Off Limits）、跑完收尾（/finalize 或 /off）。",
		"你的记忆系统的 agent 侧就是三件面：**① harness agent + 技能**（「harness agent 是记忆 agent」= ADR-002，skill 承载全部写读纪律）；",
		"系统性缺口，已在真库副本上确定性复现，已交专项维护人员定位（在跑，完成后走全量门禁）。",
		"特殊约束：它是核心仓库且带发版规范（分支检查、发布前扫描）。",
		// Regression guard: markdown/table lines must not be shredded into
		// fragment triples.
		"| 06:33 / 06:46 / 08:48 | 日期窗腿 / 闭包展开（same_as 等结构边）/ 词面三腿（trgm·BM25·FTS） | 已启用 |",
		"而「词面一项就能拿到高分」只在**腿族层面**成立，在**选择/排序层面**恰是反例。",
		"| 阶段一 | 候选排序表 → **机械按词面分排序对比（−14.6pp）** → 读档保持标准机械核 | 表即交付启用、机械排序缺省关 |",
		"| 阶段二 | **紧凑槽位模式 = precise 模式**（structured assertion slot scoring、极小交付）：16/48/96 档 = **35/50/54 vs band 64 对比**，停用为接口 | 缺省回 band |",
		"评测域 `mem:task:bench-1`、`mem:self` 等域在初始状态下为 0 行** ⇒ 向量双腿虽配置为开，实际被空域短路跳过。",
		"单变量三值臂 **fts 73 ≈ hybrid 72（Δ−1pp，p=1.0）**、vector 独面 64（但该卷落在\"死向量窗\"、需按 18:4x 边界复核）；早期向量两腿臂中性（85 vs 83/85/88）。",
		// Live-graph regressions 2026-10-28 (second round): copula/light-verb
		// prose shreds — rule-v1 no longer carries 是/为/在/有 patterns.
		"生产确实在「错着生产」。",
		"两类缺陷均为客户端特有面。",
		"时间腿已经在加分+补取。",
		"本地已有实测曲线可以直接抄。",
		"剩下是你的决定。",
		"结构面是好的：s/o 悬空 0。",
	}
	for _, text := range junk {
		extraction, err := RuleExtractor{}.Extract(text, "user", 1793102400)
		if err != nil {
			t.Fatalf("extract %q: %v", text, err)
		}
		if len(extraction.Assertions) != 0 {
			t.Errorf("%q produced %#v", text, extraction.Assertions)
		}
	}
}

// Label-level halves of the same regression: chopped heads and quoted spans
// are prose fragments even when the sentence around them is markdown-free.
func TestPlausibleLabelRejectsFragmentLabels(t *testing.T) {
	for _, value := range []string{"但该卷落", "\"死向量窗\"、需按"} {
		if plausibleLabel(value) {
			t.Errorf("plausibleLabel(%q) = true, want false", value)
		}
	}
	for _, value := range []string{"张三", "猫", "Alice", "1990年", "mem:task:qa-set-7-g4"} {
		if !plausibleLabel(value) {
			t.Errorf("plausibleLabel(%q) = false, want true", value)
		}
	}
}

func TestRuleExtractorEngineeringDecisions(t *testing.T) {
	text := "ZCode插件升级为自包含完整插件。本地模式不使用双脑noetic。标准市场插件软连接到当前仓库。"
	extraction, err := RuleExtractor{}.Extract(text, "user", 1793102400)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(extraction.Assertions) != 3 {
		t.Fatalf("expected 3 assertions, got %#v", extraction.Assertions)
	}
	for _, row := range extraction.Assertions {
		if row["confidence"] != 0.85 {
			t.Fatalf("expected 0.85 confidence, got %#v", row["confidence"])
		}
	}
}
