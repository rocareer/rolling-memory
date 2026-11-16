// plugin: kb v1.0.0
package ingest

import (
	"math"
	"strings"
	"testing"
)

func TestEstimateTokensCoefficients(t *testing.T) {
	// 缺省系数：CJK 0.6 token/字、非 CJK 0.25 token/字符
	got := EstimateTokens(100, 40, BudgetLimits{})
	want := 100*0.6 + 40*0.25
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("token 估算不符: got=%v want=%v", got, want)
	}
	// 显式系数覆盖
	got = EstimateTokens(10, 10, BudgetLimits{CJKTokenPerChar: 1, NonCJKTokenPerChar: 1})
	if math.Abs(got-20) > 1e-9 {
		t.Fatalf("显式系数未生效: %v", got)
	}
	// 费用估算（0.125 元/百万 token）
	cost := EstimateCostCNY(1_000_000, BudgetLimits{})
	if math.Abs(cost-0.125) > 1e-9 {
		t.Fatalf("费用估算不符: %v", cost)
	}
}

func TestCheckBudgetSuspension(t *testing.T) {
	in := BudgetInput{Docs: 1, Chunks: 10, Chars: 1000, CJK: 600, Other: 400}

	// 0 = 不设限：永不挂起
	if s, err := CheckBudget(in, BudgetLimits{}); err != nil || s != nil {
		t.Fatalf("不设限应放行: %v %v", s, err)
	}

	cases := []struct {
		name  string
		limit BudgetLimits
		what  string
	}{
		{"docs", BudgetLimits{MaxDocsPerJob: 0}, ""},
		{"chunks 超限", BudgetLimits{MaxChunksPerJob: 9}, "chunks"},
		{"chars 超限", BudgetLimits{MaxCharsPerJob: 999}, "chars"},
		{"est_tokens 超限", BudgetLimits{MaxEstTokensPerJob: 1}, "est_tokens"},
	}
	for _, c := range cases {
		s, err := CheckBudget(in, c.limit)
		if err != nil {
			t.Fatalf("%s: 不应报错: %v", c.name, err)
		}
		if c.what == "" {
			if s != nil {
				t.Fatalf("%s: 期望放行，实际挂起 %v", c.name, s)
			}
			continue
		}
		if s == nil || s.What != c.what {
			t.Fatalf("%s: 期望挂起维度 %q，实际 %v", c.name, c.what, s)
		}
		if !strings.Contains(s.Error(), "预算超限待批") {
			t.Fatalf("%s: 文案缺「挂起待批」: %s", c.name, s.Error())
		}
		if s.EstTokens <= 0 {
			t.Fatalf("%s: 挂起应带 token 估算读数", c.name)
		}
	}
}

func TestDocsLimitSuspends(t *testing.T) {
	s, err := CheckBudget(BudgetInput{Docs: 5}, BudgetLimits{MaxDocsPerJob: 3})
	if err != nil || s == nil || s.What != "docs" {
		t.Fatalf("文档数超限应挂起: %v %v", s, err)
	}
}

// TestResolveFTSConfig tsv 查询配置单点：空 / simple ⇒ simple；
// 其余值（含原 english 档）⇒ 响亮拒绝——tsv 已切换为 DB 生成列、生成侧口径自 DDL 冻结为 simple
// （FTSConfigFixed 与match_text 派生列表达式同值），查询侧取别值会与生成列错位（静默失召回），
// 错位不许静默（english 档随生成列切换收窄）。
func TestResolveFTSConfig(t *testing.T) {
	ok := map[string]string{
		"":         "simple",
		"simple":   "simple",
		"Simple":   "simple",
		" SIMPLE ": "simple",
	}
	for in, want := range ok {
		got, err := ResolveFTSConfig(in)
		if err != nil || got != want {
			t.Fatalf("ResolveFTSConfig(%q)=%q,%v 期望 %q,nil", in, got, err, want)
		}
	}
	for _, bad := range []string{"english", "ENGLISH", "chinese", " 'english"} {
		if got, err := ResolveFTSConfig(bad); err == nil || got != "" {
			t.Fatalf("ResolveFTSConfig(%q) 应响亮拒绝（生成列口径冻结 %s）：got=%q err=%v", bad, FTSConfigFixed, got, err)
		}
	}
	if FTSConfigFixed != "simple" {
		t.Fatalf("tsv 生成列配置单点须与match_text 派生列的 DDL 表达式同值：%s", FTSConfigFixed)
	}
}

func TestDocLocatorAnchors(t *testing.T) {
	if got := DocLocator(42); got != "kb:doc:42" {
		t.Fatalf("文档锚前缀不符: %s", got)
	}
	if got := DocLocator(0); got != "kb:doc:0" {
		t.Fatalf("文档锚前缀不符（0）: %s", got)
	}
}

func TestBudgetLimitsWithDefaults(t *testing.T) {
	l := BudgetLimits{}.WithDefaults()
	if l.CJKTokenPerChar != DefaultCJKTokenPerChar ||
		l.NonCJKTokenPerChar != DefaultNonCJKTokenPerChar ||
		l.PricePerMillion != DefaultEmbedPricePerMillionTokens {
		t.Fatalf("系数缺省回落不符: %+v", l)
	}
	// 显式值不被覆盖
	l = BudgetLimits{CJKTokenPerChar: 0.5, NonCJKTokenPerChar: 0.1, PricePerMillion: 1.0}.WithDefaults()
	if l.CJKTokenPerChar != 0.5 || l.NonCJKTokenPerChar != 0.1 || l.PricePerMillion != 1.0 {
		t.Fatalf("显式系数被误覆盖: %+v", l)
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("中文测试", 2); got != "中文" {
		t.Fatalf("截断不符: %q", got)
	}
	if got := truncateRunes("  padded  ", 20); got != "padded" {
		t.Fatalf("应去首尾空白: %q", got)
	}
}
