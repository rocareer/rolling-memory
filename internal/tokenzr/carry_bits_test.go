// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package tokenzr

// TokenPriorBits 的降级路径（内嵌 128k BPE 词表加载失败，
// 实际不可达）必须使用**逐 rune 载体先验**而不是按字节估算。
//
// 早期实现 `float64(len(s)) * 2.0` 中 len(s) 是字节数，CJK 会被抬到 6.0 bits/字
// （应为 10.0），与全站 AtomicConceptEntropyBits 标尺冲突；注释却写「2.0 bits/字符」。
// 现降级路径直接复用 bpeLexicalCarryBits（Han/假名 10.0、字母 2.0、数字 3.32、其他 0）。
// 本测试锁住该表的取值，防止回退成字节数/字符数估算。

import "testing"

func TestBPELexicalCarryBitsTable(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"", 0},
		{"abc", 6.0},       // 3 字母 × 2.0
		{"数据", 20.0},       // 2 汉字 × 10.0（按字节估算会给 12.0 —— 这正是被修的回归）
		{"공부", 4.0},        // 表只隔离 Han/假名：Hangul 按字母 2.0
		{"食べ", 20.0},       // Han 与假名同记 10.0
		{"ก", 2.0},         // 泰文同理按字母 2.0
		{"a1", 5.32},       // 字母 2.0 + 数字 3.32
		{"...", 0},         // 非载体零贡献
		{"a b", 4.0},       // 空白零贡献
		{"\u200b数据", 20.0}, // 零宽格式符零贡献
	}
	for _, c := range cases {
		if got := bpeLexicalCarryBits(c.in); got != c.want {
			t.Fatalf("bpeLexicalCarryBits(%q) = %v, want %v", c.in, got, c.want)
		}
	}
	// 与降级路径的等价性：TokenPriorBits 在词表可用时走 BPE 合并秩，无法直接断言；
	// 但降级分支必须调用本函数（源码契约，见 bpe_topology.go 的 TokenPriorBits）。
	// 这里再保证「同表逐 rune 累加」不被改成切片/字节路径。
	for _, s := range []string{"数据", "abc def", "กขคง"} {
		var want float64
		for _, r := range s {
			want += bpeRuneCarryBits(r)
		}
		if got := bpeLexicalCarryBits(s); got != want {
			t.Fatalf("bpeLexicalCarryBits(%q) drifted from per-rune sum: got %v want %v", s, got, want)
		}
	}
	// F-W2-3: 连续无空格文字（含泰文/高棉文/全范围 CJK）在 isUnspacedScriptRune 中必须返回 true，不强加 ASCII 前导空格。
	for _, r := range []rune{'中', 'あ', 'ア', 'ก', 'ກ', 'ក', 'က', 'ཀ'} {
		if !isUnspacedScriptRune(r) {
			t.Fatalf("F-W2-3 FALSIFIED: expected isUnspacedScriptRune(%q) == true", r)
		}
	}
	if isUnspacedScriptRune('A') {
		t.Fatalf("expected Latin 'A' to be space-delimited")
	}
}
