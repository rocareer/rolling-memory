// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"fmt"
	"testing"
	"time"
)

// 对抗面（ECAS 同格）：显著词选择器的**边界与不变量**——多语种同构、极端长度、重复占位、
// 幂等与确定性。回归守卫：把排序键改错（升序 / 按长度 / 丢保序）或漏去重，下列用例必红。

// TestSelectTopInformationTokensMultiScriptCJK 多书写系统同构：选择只看信息量，不看语种。
// 拉丁 / 汉字 / 阿拉伯 / 西里尔 / 天城文各取样，混合后取 k ⇒ 选中集必须全部来自输入且恰好 k 个，
// 且**任一书写系统的最高信息量词**在足够大的 k 下都不得被系统性排除（这里断言全局最高者入选）。
func TestSelectTopInformationTokensMultiScriptCJK(t *testing.T) {
	in := []string{
		"的", "了", "在", // 汉字虚词（低比特）
		"春节", "寄养", "阿拉斯加",
		"the", "and", "of", // 拉丁虚词
		"kristofer", "albuquerque",
		"и", "в", "на", // 西里尔虚词
		"Константинополь",
		"في", "من", "على", // 阿拉伯虚词
		"القاهرة",
		"का", "में", // 天城文虚词
		"राजस्थान",
	}
	best, bestMass := "", -1.0
	for _, tk := range in {
		if m := TokenInformationMass(tk); m > bestMass {
			best, bestMass = tk, m
		}
	}
	for _, k := range []int{1, 4, 8} {
		out := SelectTopInformationTokens(in, k)
		if len(out) != k {
			t.Fatalf("k=%d 应取满 %d 个，got=%d (%v)", k, k, len(out), out)
		}
		have := make(map[string]bool, len(out))
		for _, tk := range out {
			have[tk] = true
		}
		if k >= 4 && !have[best] {
			t.Fatalf("k=%d 时全局最高信息量词 %q 应入选，got=%v", k, best, out)
		}
	}
}

// TestSelectTopInformationTokensDuplicateHeavy 重复密集输入：去重后不足 k ⇒ 输出=去重数（不得补空、不得重复）。
func TestSelectTopInformationTokensDuplicateHeavy(t *testing.T) {
	in := []string{"caroline", "caroline", "harbour", "caroline", "harbour", "sketchbook"}
	out := SelectTopInformationTokens(in, 5)
	if len(out) != 3 {
		t.Fatalf("去重后仅 3 个实词，应返回 3 个，got=%d (%v)", len(out), out)
	}
	seen := map[string]bool{}
	for _, tk := range out {
		if seen[tk] {
			t.Fatalf("重复词进入输出：%v", out)
		}
		seen[tk] = true
	}
}

// TestSelectTopInformationTokensIdempotent 幂等：对结果再选同 k 次，逐字不变（读链多次调用同一集不得漂移）。
func TestSelectTopInformationTokensIdempotent(t *testing.T) {
	in := make([]string, 0, 64)
	for i := 0; i < 64; i++ {
		in = append(in, fmt.Sprintf("tok%02d", i*37%101))
	}
	once := SelectTopInformationTokens(in, 12)
	twice := SelectTopInformationTokens(once, 12)
	if fmt.Sprint(once) != fmt.Sprint(twice) {
		t.Fatalf("不幂等：once=%v twice=%v", once, twice)
	}
}

// TestSelectTopInformationTokensLargeInputBound 极端长度：4096 词元仍取满 k 且不超时（长问句 / 拼接语料）。
func TestSelectTopInformationTokensLargeInputBound(t *testing.T) {
	in := make([]string, 0, 4096)
	for i := 0; i < 4096; i++ {
		in = append(in, fmt.Sprintf("z%dx%dq", i%97, i))
	}
	start := time.Now()
	out := SelectTopInformationTokens(in, 16)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("4096 词元选择耗时异常：%s", d)
	}
	if len(out) != 16 {
		t.Fatalf("应取满 16，got=%d", len(out))
	}
}

// TestSelectTopInformationTokensNoScriptRules 反特调守卫：实现不得依赖语种词表/字数/词尾规则——
// 同一「形状」的伪词（无意义串）在不同书写系统下的相对排序，只能由信息量决定，
// 即：把同一批伪词按信息量降序，前 k 个必须恰是选择器输出集合。
func TestSelectTopInformationTokensNoScriptRules(t *testing.T) {
	in := []string{
		"qzxv", "фыжэ", "بثجح", "टढण", "龘靐齉",
		"ab", "的", "12", "xyzzy", "плов",
	}
	type pair struct {
		tk   string
		mass float64
	}
	ps := make([]pair, 0, len(in))
	for _, tk := range in {
		ps = append(ps, pair{tk, TokenInformationMass(tk)})
	}
	for i := 0; i < len(ps); i++ {
		for j := i + 1; j < len(ps); j++ {
			if ps[j].mass > ps[i].mass {
				ps[i], ps[j] = ps[j], ps[i]
			}
		}
	}
	const k = 4
	want := map[string]bool{}
	for i := 0; i < k; i++ {
		want[ps[i].tk] = true
	}
	got := SelectTopInformationTokens(in, k)
	if len(got) != k {
		t.Fatalf("应取满 %d，got=%v", k, got)
	}
	for _, tk := range got {
		if !want[tk] {
			t.Fatalf("选中集与信息量排序不一致（疑似语种规则介入）：got=%v want=%v", got, want)
		}
	}
}
