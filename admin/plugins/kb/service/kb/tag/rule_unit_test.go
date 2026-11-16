// plugin: kb v1.0.0
package tag

import (
	"strings"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
)

// 规则打标求值引擎单测（无库； ·T3）：四类 match_type 正反例、主体推导、
// 优先级/closeness 排序与去重、跳过原因（不静默）、入参不被改写。

// subject 常用主体（路径/文件名/后缀/元信息四类各取所需）
func testSubject() Subject {
	return Subject{Title: "回退标题", Path: "docs/合同/2026-价款.md", MIME: "text/markdown; charset=utf-8", SourceKind: "file"}
}

// TestRuleMatchTypesUnit 四类 match_type 正反例（含边界与跳过原因）。
func TestRuleMatchTypesUnit(t *testing.T) {
	s := testSubject()

	// ① path_glob：`*` 任意长（含路径分隔符）/ `?` 恰一 rune / 全串锚定 / 大小写敏感 / 字面量转义
	globCases := []struct {
		pattern string
		name    string
		want    bool
	}{
		{"*.md", "docs/合同/2026-价款.md", true}, // `*` 跨分隔符
		{"docs/*.md", "docs/合同/2026-价款.md", true},
		{"*.md", "docs/合同/2026-价款.mdx", false}, // 结尾锚定
		{"*.MD", "notes.md", false},            // 大小写敏感（路径语义）
		{"*价款.?d", "docs/合同/2026-价款.md", true}, // `?` 恰一 rune
		{"合同/*", "合同/", true},                  // `*` 可匹配空序列
		{"a[1].md", "a1.md", false},            // 字面量：括号不当字符类
		{"a[1].md", "a[1].md", true},
	}
	for _, c := range globCases {
		got := globMatch(c.name, c.pattern)
		if got != c.want {
			t.Fatalf("path_glob %q vs %q: got=%v want=%v", c.pattern, c.name, got, c.want)
		}
	}

	// ② filename_regex：对基名（非全路径）非锚定匹配 / 非法正则跳过
	if !regexpRuleMatches(t, `^2026-价款\.md$`, "docs/合同/2026-价款.md") {
		t.Fatal("filename_regex 应只比基名：^2026-价款.md$ 应命中")
	}
	if !regexpRuleMatches(t, `价款`, "docs/合同/2026-价款.md") {
		t.Fatal("filename_regex 非锚定：子串应命中")
	}
	if regexpRuleMatches(t, `^docs/`, "docs/合同/2026-价款.md") {
		t.Fatal("filename_regex 不得比全路径（^docs/ 不应命中基名）")
	}
	if _, reason := matchRule(s, Rule{MatchType: model.KBTagRuleMatchFilenameRegex, Pattern: "([a-z"}); reason == "" {
		t.Fatal("非法正则应给跳过原因（不静默不命中）")
	}

	// ③ suffix：大小写不敏感 / 前导点可带可不带 / 多段后缀 / 反例
	suffixCases := []struct {
		pattern, filename string
		want              bool
	}{
		{"md", "2026-价款.MD", true},
		{".md", "a/b/c.md", true},
		{"tar.gz", "backup.tar.gz", true},
		{"pdf", "a.pdfx", false},       // 不得后缀误命中
		{"pdf", "apdf", false},         // 无点边界
		{"md", "readme.md.bak", false}, // 只认结尾
	}
	for _, c := range suffixCases {
		got, reason := matchRule(Subject{Path: c.filename}, Rule{MatchType: model.KBTagRuleMatchSuffix, Pattern: c.pattern})
		if reason != "" {
			t.Fatalf("suffix %q/%q 不应跳过: %s", c.pattern, c.filename, reason)
		}
		if got != c.want {
			t.Fatalf("suffix %q vs %q: got=%v want=%v", c.pattern, c.filename, got, c.want)
		}
	}

	// ④ meta_eq：field=value / mime 只比基础类型 / 大小写不敏感 / 闭集外字段与缺值跳过
	metaCases := []struct {
		pattern string
		want    bool
	}{
		{"mime=text/markdown", true}, // 基础类型比对（主体 mime 带 charset 参数）
		{"mime=TEXT/Markdown", true}, // 大小写不敏感
		{"source_kind=file", true},
		{"SOURCE_KIND=FILE", true},
		{"source_kind=url", false},
		{"mime=application/pdf", false},
	}
	for _, c := range metaCases {
		got, reason := matchRule(s, Rule{MatchType: model.KBTagRuleMatchMetaEq, Pattern: c.pattern})
		if reason != "" {
			t.Fatalf("meta_eq %q 不应跳过: %s", c.pattern, reason)
		}
		if got != c.want {
			t.Fatalf("meta_eq %q: got=%v want=%v", c.pattern, got, c.want)
		}
	}
	for _, bad := range []string{"author=张三", "mime=", "mime", "=x"} {
		if _, reason := matchRule(s, Rule{MatchType: model.KBTagRuleMatchMetaEq, Pattern: bad}); reason == "" {
			t.Fatalf("meta_eq 非法式 %q 应给跳过原因", bad)
		}
	}

	// ⑤ 空匹配式与未登记类型：跳过并给原因（不把空式当通配）
	if _, reason := matchRule(s, Rule{MatchType: model.KBTagRuleMatchSuffix, Pattern: "   "}); reason == "" {
		t.Fatal("空匹配式应给跳过原因")
	}
	if _, reason := matchRule(s, Rule{MatchType: "glob", Pattern: "*.md"}); reason == "" {
		t.Fatal("未登记 match_type 应给跳过原因")
	}
}

// regexpRuleMatches 便捷断言（filename_regex 命中与否；跳过视为失败）
func regexpRuleMatches(t *testing.T, pattern, path string) bool {
	t.Helper()
	got, reason := matchRule(Subject{Path: path}, Rule{MatchType: model.KBTagRuleMatchFilenameRegex, Pattern: pattern})
	if reason != "" {
		t.Fatalf("正则 %q 不应跳过: %s", pattern, reason)
	}
	return got
}

// TestRuleSubjectDerivationUnit 主体推导：Name = source_ref 优先、空则回落 title；Filename 认 `/` 与 `\`。
func TestRuleSubjectDerivationUnit(t *testing.T) {
	s := Subject{Title: " 备用标题.md ", Path: " docs/a.pdf "}
	if got := s.Name(); got != "docs/a.pdf" {
		t.Fatalf("Name 应以 source_ref 优先（去空白）: %q", got)
	}
	if got := s.Filename(); got != "a.pdf" {
		t.Fatalf("Filename 应为基名: %q", got)
	}
	s = Subject{Title: "notes/readme.md"}
	if got := s.Name(); got != "notes/readme.md" {
		t.Fatalf("Name 应回落 title: %q", got)
	}
	if got := s.Filename(); got != "readme.md" {
		t.Fatalf("回落标题后 Filename 应取基名: %q", got)
	}
	if got := (Subject{Path: `C:\docs\c.md`}).Filename(); got != "c.md" {
		t.Fatalf("反斜杠分隔符应识别: %q", got)
	}
	if got := (Subject{}).Filename(); got != "" {
		t.Fatalf("空主体基名应为空: %q", got)
	}
}

// TestEvaluateRulesOrderDedupUnit 求值顺序 = (priority,id)（引擎内重排，不依赖入参顺序）、
// 同标签跨规则去重保序、跳过项不阻断其余规则、入参不被改写（排序的是副本）。
func TestEvaluateRulesOrderDedupUnit(t *testing.T) {
	s := testSubject()
	rules := []Rule{
		{ID: 30, Name: "c", MatchType: model.KBTagRuleMatchSuffix, Pattern: "md", TagID: 300, Priority: 30},
		{ID: 10, Name: "a", MatchType: model.KBTagRuleMatchPathGlob, Pattern: "*.md", TagID: 100, Priority: 10},
		{ID: 11, Name: "a2", MatchType: model.KBTagRuleMatchSuffix, Pattern: "md", TagID: 100, Priority: 10}, // 同标签去重
		{ID: 20, Name: "b", MatchType: model.KBTagRuleMatchMetaEq, Pattern: "source_kind=file", TagID: 200, Priority: 20},
		{ID: 40, Name: "bad", MatchType: model.KBTagRuleMatchFilenameRegex, Pattern: "([", TagID: 400, Priority: 40},
	}
	res := EvaluateRules(s, rules)
	if res.Rules != len(rules) {
		t.Fatalf("参与求值规则数不符: %d", res.Rules)
	}
	wantOrder := []uint{10, 11, 20, 30}
	if len(res.Hits) != len(wantOrder) {
		t.Fatalf("命中数不符: %+v", res.Hits)
	}
	for i, id := range wantOrder {
		if res.Hits[i].RuleID != id {
			t.Fatalf("命中顺序应 (priority,id): %+v", res.Hits)
		}
	}
	if len(res.TagIDs) != 3 || res.TagIDs[0] != 100 || res.TagIDs[1] != 200 || res.TagIDs[2] != 300 {
		t.Fatalf("标签集应去重保序: %+v", res.TagIDs)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].RuleID != 40 {
		t.Fatalf("非法正则项应进跳过: %+v", res.Skipped)
	}
	if rules[0].ID != 30 {
		t.Fatal("求值不得改写入参顺序（须排副本）")
	}
	if notes := res.SkipNotes(); len(notes) != 1 || !strings.Contains(notes[0], "正则非法") {
		t.Fatalf("跳过文案不符: %+v", notes)
	}
}

// TestEvalSkipNotesUnit 告警帽：超过 MaxSkipNotes 只报计数，跳过项不静默。
func TestEvalSkipNotesUnit(t *testing.T) {
	res := EvalResult{}
	for i := 0; i < MaxSkipNotes+3; i++ {
		res.Skipped = append(res.Skipped, RuleSkip{RuleID: uint(i + 1), RuleName: "r", Reason: "原因"})
	}
	notes := res.SkipNotes()
	if len(notes) != MaxSkipNotes+1 {
		t.Fatalf("告警条数应为帽 + 计数行: %d", len(notes))
	}
	if !strings.Contains(notes[len(notes)-1], "另有 3 条") {
		t.Fatalf("溢出应报计数: %q", notes[len(notes)-1])
	}
	empty := EvalResult{}
	if notes := empty.SkipNotes(); len(notes) != 0 {
		t.Fatalf("无跳过不应有告警: %+v", notes)
	}
}
