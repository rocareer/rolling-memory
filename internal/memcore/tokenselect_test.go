// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"fmt"
	"testing"
)

// TestSelectTopInformationTokensContract 选择器契约（纯函数）：
//   - k<=0 或 len<=k ⇒ **原样返回入参**（同一底层数组，零拷贝零排序）；
//   - 超限 ⇒ 恰好 k 个、去重、保序（输出下标单调递增）；
//   - 平票确定性（同输入两次结果逐字相同）；
//   - 信息量最高者必在选中集内。
func TestSelectTopInformationTokensContract(t *testing.T) {
	t.Run("noop_when_under_cap", func(t *testing.T) {
		in := []string{"caroline", "sketchbook", "harbour"}
		for _, k := range []int{0, -3, 3, 4, 64} {
			out := SelectTopInformationTokens(in, k)
			if len(out) != len(in) || &out[0] != &in[0] {
				t.Fatalf("k=%d 应原样返回入参（同底层数组），got=%v", k, out)
			}
		}
	})
	t.Run("cap_exact_k_dedup_ordered", func(t *testing.T) {
		in := make([]string, 0, 40)
		for i := 0; i < 40; i++ {
			in = append(in, fmt.Sprintf("term%02dzeta", i))
		}
		in = append(in, in[0], in[1]) // 重复词不得占两席
		const k = 7
		out := SelectTopInformationTokens(in, k)
		if len(out) != k {
			t.Fatalf("应恰好取 k=%d 个，got=%d (%v)", k, len(out), out)
		}
		pos := make(map[string]int, len(in))
		for i, tk := range in {
			if _, ok := pos[tk]; !ok {
				pos[tk] = i
			}
		}
		seen := make(map[string]bool, len(out))
		prev := -1
		for _, tk := range out {
			if seen[tk] {
				t.Fatalf("输出含重复词 %q", tk)
			}
			seen[tk] = true
			i, ok := pos[tk]
			if !ok {
				t.Fatalf("输出含输入之外的词 %q", tk)
			}
			if i <= prev {
				t.Fatalf("输出未保持入参相对顺序：%v", out)
			}
			prev = i
		}
	})
	t.Run("deterministic_on_ties", func(t *testing.T) {
		in := []string{"aa", "bb", "cc", "dd", "ee", "ff"}
		a := SelectTopInformationTokens(in, 4)
		b := SelectTopInformationTokens(in, 4)
		if fmt.Sprint(a) != fmt.Sprint(b) {
			t.Fatalf("平票应确定性：%v vs %v", a, b)
		}
	})
	t.Run("top_mass_survives", func(t *testing.T) {
		in := []string{"的", "了", "是", "kristofer", "albuquerque", "zzzzqqq"}
		best, bestMass := "", -1.0
		for _, tk := range in {
			if m := TokenInformationMass(tk); m > bestMass {
				best, bestMass = tk, m
			}
		}
		out := SelectTopInformationTokens(in, 1)
		if len(out) != 1 || out[0] != best {
			t.Fatalf("k=1 应选信息量最高者 %q，got=%v", best, out)
		}
	})
}

// TestSelectTopInformationTokensDropsEmpty 空串不占槽位（调用方可能带 TrimSpace 后的残片）。
func TestSelectTopInformationTokensDropsEmpty(t *testing.T) {
	in := []string{"", "caroline", " ", "harbour", ""}
	out := SelectTopInformationTokens(in, 2)
	for _, tk := range out {
		if tk == "" || tk == " " {
			t.Fatalf("空串/空白不应进入选中集：%v", out)
		}
	}
	if len(out) != 2 {
		t.Fatalf("应取满 2 个实词，got=%v", out)
	}
}
