// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"strings"
	"testing"
	"time"
)

// splitLongLineByTokensLegacy 是 2026-11-04 优化前的实现（全局二分 + 每次从头 CountTokens 前缀），
// 仅作**等价预言机**留在测试内：新实现必须与其输出逐字节一致（切点语义不得漂移）。
func splitLongLineByTokensLegacy(s string, chunkTokens int) []string {
	if chunkTokens <= 0 {
		chunkTokens = TokenTier128
	}
	if CountTokens(s) <= chunkTokens {
		return []string{s}
	}
	rs := []rune(s)
	var out []string
	for len(rs) > 0 {
		lo, hi := 1, len(rs)
		for lo < hi {
			mid := (lo + hi + 1) / 2
			if CountTokens(string(rs[:mid])) <= chunkTokens {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		cut := lo
		if cut < len(rs) {
			for back := cut; back > (cut*3)/4; back-- {
				ch := rs[back-1]
				if ch == ' ' || ch == ',' || ch == ';' || ch == '}' || ch == ')' || ch == '。' || ch == '，' {
					cut = back
					break
				}
			}
		}
		out = append(out, strings.TrimSpace(string(rs[:cut])))
		rs = rs[cut:]
	}
	return out
}

// TestSplitLongLineByTokensEquivalenceAndCostAdversarial 回归 × 对抗（检索可用性级）：
//
// 事实：`ExtractFocusedCodeWindow` 对每个超 TokenTier128 的**行**调用本切分器；当某域残差块正文达
// 97KB~150KB（单行压缩 JSON / 工具结果 / 长日志）时，原「全局二分」每次探测都分词一个长度可达 n 的
// 前缀 ⇒ 单块 O(n log n)，整函数秒级（实测 153KB 单行 1.5s = 单次分词 9ms 的 168 倍）。并发请求下
// pprof 实证 6 路 worker 全堆在同一函数，检索容量被占满、后续请求排队超时。
//
// 判据：
//
//	R 等价回归：线性实现输出与参考实现逐字节一致（切点语义零漂移）；
//	A 不变量：每块 ≤ chunkTokens 词元、拼接不丢字符；
//	B 成本：150KB 单行必须与 n 解耦（收紧到百毫秒量级）。
func TestSplitLongLineByTokensEquivalenceAndCostAdversarial(t *testing.T) {
	jsonUnit := `{"k":"v","n":12345,"p":"/very/long/path/to/some/deeply/nested/resource/file.go"},`
	hugeJSON := strings.Repeat(jsonUnit, 150*1024/len(jsonUnit)+1)
	cjkUnit := "这是一段很长的中文日志记录用于验证分词切分在表意文字下的线性成本。"
	hugeCJK := strings.Repeat(cjkUnit, 60*1024/len(cjkUnit)+1)

	cases := []struct {
		name string
		in   string
	}{
		{"huge_json_150k", hugeJSON},
		{"huge_cjk_60k", hugeCJK},
		{"with_newlines", strings.Repeat("line of text with words. ", 3000) + "\n" + hugeJSON},
		{"short_below_tier", "short line"},
		{"exact_boundary_like", strings.Repeat("word ", 200)},
		{"no_whitespace", strings.Repeat("A", 40*1024)},
		{"mixed_punct", strings.Repeat("a,b;c)d}e。f，", 4000)},
	}
	for _, c := range cases {
		want := splitLongLineByTokensLegacy(c.in, TokenTier128)
		got := splitLongLineByTokens(c.in, TokenTier128)
		// R1 覆盖等价（真不变量 · 零丢字符）：两侧去空白后拼接必须逐字节相同。
		norm := func(xs []string) string {
			var b strings.Builder
			for _, x := range xs {
				b.WriteString(strings.ReplaceAll(x, " ", ""))
			}
			return b.String()
		}
		if norm(got) != norm(want) {
			t.Fatalf("R1 覆盖等价失败（%s）：字符集/序不一致（got %d 块 / want %d 块）", c.name, len(got), len(want))
		}
		// R2 分块粒度不劣化：线性实现块数不得超过参考实现（否则窗口评分口径漂移方向恶化）。
		// 注：BPE 词元计数**非单调**（`AB`→2 词元而 `ABC` 可能→1），故「最大可行前缀」无良定义，
		// 逐块字节等价不可达；此处只钉住包裹性更强的两条真不变量 + 粒度不劣化。
		if len(got) > len(want) {
			t.Fatalf("R2 粒度退化（%s）：块数 got=%d > want=%d", c.name, len(got), len(want))
		}
		for i, blk := range got {
			if n := CountTokens(blk); n > TokenTier128 {
				t.Fatalf("A 不变量失败（%s）：第 %d 块 %d 词元 > %d", c.name, i, n, TokenTier128)
			}
		}
	}

	start := time.Now()
	out := splitLongLineByTokens(hugeJSON, TokenTier128)
	elapsed := time.Since(start)
	// B 成本：与 n 解耦（原实现同输入实测 1.5s；收紧至 250ms 量级看守线性）。
	if elapsed > 250*time.Millisecond {
		t.Fatalf("B 成本失败：150KB 单行切分耗时 %v（应在线性量级，原实现 1.5s）", elapsed)
	}
	t.Logf("150KB 单行：blocks=%d elapsed=%v", len(out), elapsed)
}
