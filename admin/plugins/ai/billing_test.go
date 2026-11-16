// plugin: ai v0.2.0
package ai

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func TestParsePriceMicroValid(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"0", 0},
		{"1", 1_000_000},
		{"1.5", 1_500_000},
		{".5", 500_000},
		{"1.", 1_000_000},
		{"0001.250000", 1_250_000},
		{"+1.5", 1_500_000},
		{" 0.123456 ", 123_456},
		{"999999.999999", MaxPriceMicro},
		// 第 7 位 half-away-from-zero
		{"1.2345674", 1_234_567},
		{"1.2345675", 1_234_568},
		{"0.0000004", 0},
		{"0.0000005", 1},
	}
	for _, tc := range cases {
		got, err := ParsePriceMicro(tc.in)
		if err != nil {
			t.Fatalf("ParsePriceMicro(%q) 错误：%v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("ParsePriceMicro(%q) = %d，期望 %d", tc.in, got, tc.want)
		}
	}
}

func TestParsePriceMicroRejects(t *testing.T) {
	cases := []string{
		"", "   ", "NaN", "nan", "+NaN", "-NaN", "Inf", "inf", "-Inf", "Infinity",
		"-1", "-0.5", "1e3", "1.2.3", ".", "+", "abc", "1,5", "0x10",
		"1000000", // 超出 numeric(12,6)
	}
	for _, in := range cases {
		if got, err := ParsePriceMicro(in); err == nil {
			t.Fatalf("ParsePriceMicro(%q) 应拒绝，实际返回 %d", in, got)
		}
	}
}

func TestFormatMicro(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0.000000"},
		{1, "0.000001"},
		{1_500_000, "1.500000"},
		{123_456, "0.123456"},
		{999_999_999_999, "999999.999999"},
		{-1, "-0.000001"},
	}
	for _, tc := range cases {
		if got := FormatMicro(tc.in); got != tc.want {
			t.Fatalf("FormatMicro(%d) = %q，期望 %q", tc.in, got, tc.want)
		}
	}
}

func TestComputeCost(t *testing.T) {
	cases := []struct {
		name       string
		prompt     int
		completion int
		priceIn    string
		priceOut   string
		want       string
	}{
		{"百万输入+百万输出", 1_000_000, 1_000_000, "1", "2", "3.000000"},
		{"单价小数", 1_000_000, 0, "0.123456", "0", "0.123456"},
		{"1.5 倍", 1_500_000, 0, "1", "0", "1.500000"},
		{"半进位向上", 1, 0, "0.5", "0", "0.000001"},
		{"不足半舍去", 1, 0, "0.499999", "0", "0.000000"},
		{"极小单价", 1_000_000, 0, "0.000001", "0", "0.000001"},
		{"零单价", 500, 500, "0", "0", "0.000000"},
		{"只有 completion", 0, 1_000_000, "0", "2.5", "2.500000"},
		{"两段合并后舍入", 1, 1, "0.3", "0.3", "0.000001"}, // 0.0000003+0.0000003=0.0000006
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ComputeCost(tc.prompt, tc.completion, tc.priceIn, tc.priceOut)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("ComputeCost(%d,%d,%q,%q) = %q，期望 %q", tc.prompt, tc.completion, tc.priceIn, tc.priceOut, got, tc.want)
			}
		})
	}
}

func TestComputeCostRejectsNegativeTokensAndPrices(t *testing.T) {
	if _, err := ComputeCost(-1, 0, "1", "0"); err == nil {
		t.Fatal("负 prompt 应被拒")
	}
	if _, err := ComputeCost(0, -1, "1", "0"); err == nil {
		t.Fatal("负 completion 应被拒")
	}
	if _, err := ComputeCost(1, 0, "NaN", "0"); err == nil {
		t.Fatal("NaN 单价应被拒")
	}
}

func TestEstimateTokensFormula(t *testing.T) {
	zh34 := strings.Repeat("中", 34)
	zh18 := strings.Repeat("中", 18)
	zh68 := strings.Repeat("中", 68)
	en71 := strings.Repeat("a", 71)

	cases := []struct {
		name     string
		encoding string
		text     string
		want     int
	}{
		{"o200k 34 汉字", EncodingTokenzR, zh34, 28},
		{"o200k 18 汉字", EncodingTokenzR, zh18, 15},
		{"o200k 68 汉字", EncodingTokenzR, zh68, 55},
		{"o200k 71 英文", EncodingTokenzR, en71, 18},
		{"空串", EncodingTokenzR, "", 0},
		// 混排 10 汉字 + 40 ASCII：o2 = ceil(40/5)+ceil(40/4)=8+10=18。
		{"o200k 混排 50 字符", EncodingTokenzR, strings.Repeat("中", 10) + strings.Repeat("a", 40), 18},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := EstimateTokens(tc.encoding, tc.text)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("EstimateTokens(%s, len=%d) = %d，期望 %d", tc.encoding, len([]rune(tc.text)), got, tc.want)
			}
		})
	}
}

func TestEstimateTokensRejectsDisabledAndUnknownEncoding(t *testing.T) {
	if _, err := EstimateTokens("", "中"); !errors.Is(err, ErrEstimateDisabled) {
		t.Fatalf("encoding='' 应返回 ErrEstimateDisabled，实际 %v", err)
	}
	if _, err := EstimateTokens("p50k_base", "中"); err == nil {
		t.Fatal("未知 encoding 应返回错误，而不是静默记 0")
	}
	if _, err := EstimateTokens("cl100k_base", "中"); err == nil {
		t.Fatal("不支持的 cl100k_base encoding 应返回错误")
	}
	if _, err := EstimateTokens("o200k_base", "中"); err == nil {
		t.Fatal("不支持的 o200k_base encoding 应返回错误")
	}
}

func TestEstimateTokensNonEmptyNeverZeroAndMonotonic(t *testing.T) {
	inputs := []string{"a", " ", "中", "🙂", "a中", "hello world"}
	for _, s := range inputs {
		for _, enc := range []string{EncodingTokenzR} {
			n, err := EstimateTokens(enc, s)
			if err != nil {
				t.Fatal(err)
			}
			if n <= 0 {
				t.Fatalf("非空输入 %q（%s）估算为 %d，绝不能为 0", s, enc, n)
			}
		}
	}

	text := ""
	prev := 0
	for i := 0; i < 200; i++ {
		text += "中a"
		n, err := EstimateTokens(EncodingTokenzR, text)
		if err != nil {
			t.Fatal(err)
		}
		if n < prev {
			t.Fatalf("估算不单调：%d < %d", n, prev)
		}
		prev = n
	}
}

func TestResolveUsageCounts(t *testing.T) {
	t.Run("上游 usage 优先", func(t *testing.T) {
		got := ResolveUsageCounts(10, 20, 5, EncodingTokenzR, "中中中中中", "ignored", false)
		if got.Source != TokensSourceUsage || got.PromptTokens != 10 || got.CompletionTokens != 20 || got.TotalTokens != 30 || got.CachedTokens != 5 {
			t.Fatalf("usage 优先生效错误：%+v", got)
		}
	})
	t.Run("仅 completion 非零也算 usage", func(t *testing.T) {
		got := ResolveUsageCounts(0, 7, 0, EncodingTokenzR, "中中中中中", "ignored", false)
		if got.Source != TokensSourceUsage || got.TotalTokens != 7 {
			t.Fatalf("单向非零应视为 usage：%+v", got)
		}
	})
	t.Run("上游全 0 且 encoding 非空则估算", func(t *testing.T) {
		got := ResolveUsageCounts(0, 0, 0, EncodingTokenzR, strings.Repeat("中", 10), "abc", false)
		if got.Source != TokensSourceEstimate {
			t.Fatalf("应标记 estimate：%+v", got)
		}
		if got.PromptTokens != 8 || got.CompletionTokens != 1 || got.TotalTokens != 9 {
			t.Fatalf("估算值错误：%+v", got)
		}
	})
	t.Run("encoding 为空不估算", func(t *testing.T) {
		got := ResolveUsageCounts(0, 0, 0, "", "中中中中中", "abc", false)
		if got.Source != TokensSourceNone || got.TotalTokens != 0 || got.PromptTokens != 0 || got.CompletionTokens != 0 {
			t.Fatalf("encoding='' 应关闭估算：%+v", got)
		}
	})
	t.Run("多模态不估算", func(t *testing.T) {
		got := ResolveUsageCounts(0, 0, 0, EncodingTokenzR, "中中中中中", "abc", true)
		if got.Source != TokensSourceNone || got.TotalTokens != 0 {
			t.Fatalf("多模态应关闭估算：%+v", got)
		}
	})
	t.Run("空文本不估算", func(t *testing.T) {
		got := ResolveUsageCounts(0, 0, 0, EncodingTokenzR, "", "", false)
		if got.Source != TokensSourceNone || got.TotalTokens != 0 {
			t.Fatalf("空文本应无计数：%+v", got)
		}
	})
	t.Run("cached 在估算分支也透传", func(t *testing.T) {
		got := ResolveUsageCounts(0, 0, 9, EncodingTokenzR, "中", "", false)
		if got.CachedTokens != 9 || got.Source != TokensSourceEstimate {
			t.Fatalf("cached 透传错误：%+v", got)
		}
	})
}

func TestCheckContextWindow(t *testing.T) {
	if _, err := CheckContextWindow(EncodingTokenzR, 5, strings.Repeat("中", 10), false); err == nil {
		t.Fatal("超窗应拒绝")
	} else if !strings.Contains(err.Error(), "近似估算") {
		t.Fatalf("错误文案必须含「近似估算」：%v", err)
	}
	if _, err := CheckContextWindow(EncodingTokenzR, 100, strings.Repeat("中", 10), false); err != nil {
		t.Fatalf("未超窗不应拒绝：%v", err)
	}
	if _, err := CheckContextWindow("", 1, strings.Repeat("中", 100), false); err != nil {
		t.Fatalf("encoding='' 应关闭预检：%v", err)
	}
	if _, err := CheckContextWindow(EncodingTokenzR, 1, strings.Repeat("中", 100), true); err != nil {
		t.Fatalf("多模态应豁免预检：%v", err)
	}
	if _, err := CheckContextWindow(EncodingTokenzR, 0, strings.Repeat("中", 100), false); err != nil {
		t.Fatalf("context_window=0 应关闭预检：%v", err)
	}
}

func TestNewRequestID(t *testing.T) {
	seen := make(map[string]struct{}, 100)
	for i := 0; i < 100; i++ {
		id := NewRequestID()
		if !strings.HasPrefix(id, "ai_") {
			t.Fatalf("request_id 前缀错误：%q", id)
		}
		if len(id) != 27 { // ai_（3 字符）+ 24 hex
			t.Fatalf("request_id 长度 = %d，期望 27：%q", len(id), id)
		}
		if _, err := hex.DecodeString(id[3:]); err != nil {
			t.Fatalf("request_id 不是 hex：%q", id)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("request_id 重复：%q", id)
		}
		seen[id] = struct{}{}
	}
}

func TestMessagesTextAndMultimodal(t *testing.T) {
	messages := []Message{
		{Role: "system", Content: "be nice"},
		{Role: "user", Content: []any{
			map[string]any{"type": "text", "text": "看图"},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.com/a.png"}},
		}},
	}
	if text := MessagesText(messages); !strings.Contains(text, "be nice") || !strings.Contains(text, "看图") {
		t.Fatalf("MessagesText 丢内容：%q", text)
	}
	if !HasMultimodalMessages(messages) {
		t.Fatal("含 image_url 应判为多模态")
	}
	if HasMultimodalMessages([]Message{{Role: "user", Content: "纯文本"}}) {
		t.Fatal("纯文本不应判为多模态")
	}
	if !HasMultimodalMessages([]Message{{Role: "user", Content: "data:image/png;base64,xx"}}) {
		t.Fatal("data: 前缀应判为多模态")
	}
	if HasMultimodalMessages([]Message{{Role: "user", Content: map[string]any{"type": "text", "text": "hi"}}}) {
		t.Fatal("type=text 不应判为多模态")
	}
}

func TestContentTextAndEmbeddingInputText(t *testing.T) {
	if got := ContentText(nil); got != "" {
		t.Fatalf("nil content = %q", got)
	}
	if got := ContentText([]any{map[string]any{"text": "a"}, map[string]any{"text": "b"}}); got != "ab" {
		t.Fatalf("多模态文本拼接 = %q", got)
	}
	if got := EmbeddingInputText([]string{"a", "b"}); got != "a\nb" {
		t.Fatalf("embedding 文本 = %q", got)
	}
	if got := EmbeddingInputText([]any{"a", 2}); got != "a\n2" {
		t.Fatalf("embedding 混合文本 = %q", got)
	}
}

// TestComputeCostMicroEnforcesNumericBound numeric(12,6) 上界（999999.999999）必须拒：
// 超出即算不出，若把超界值交给写库层，usage.cost 会整行报 22003 —— 而 usage 写入失败只记日志，
// 计费/审计行就静默丢了。上界本身必须仍可算（不能把合法值误伤）。
func TestComputeCostMicroEnforcesNumericBound(t *testing.T) {
	price := FormatMicro(MaxPriceMicro) // "999999.999999"
	if _, err := ComputeCostMicro(1_000_000, 0, price, "0"); err != nil {
		t.Fatalf("上界单价 ×1e6 token 恰为 numeric(12,6) 上界，应可算：%v", err)
	}
	if got, err := ComputeCostMicro(1_000_001, 0, price, "0"); err == nil {
		t.Fatalf("超出上界必须报错（实际得 %d 微单位）", got)
	}
	if _, err := ComputeCost(1_000_001, 0, price, "0"); err == nil {
		t.Fatal("ComputeCost 也必须拒超界成本（costOrZero 会按 0.000000 落库并记日志）")
	}
	// 输入 + 输出两段合计同样受同一个上界约束（各 1e6 token × 上界单价 = 2e6 计价单位）。
	if _, err := ComputeCostMicro(1_000_000, 1_000_000, price, price); err == nil {
		t.Fatal("输入+输出合计超界也必须拒绝")
	}
}
