// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package shaping

import (
	"strings"
	"testing"
)

// 红队反例先行：先构造专门击穿折叠算子的极限输入，再钉常规折叠行为。
func TestCollapseDegenerateTokenRunsAdversarial(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// 零触发恒等（含原始空白排版保真）。
		{"empty", "", ""},
		{"plain", "a quick  brown\tfox", "a quick  brown\tfox"},
		// 合法强调（短跑 ≤ 触发线 8）逐字保留。
		{"emphasis", "very very very good", "very very very good"},
		{"run8-kept", strings.Repeat("go ", 8) + "home", strings.Repeat("go ", 8) + "home"},
		// 行内连跑 9 > 8 折到 3。
		{"run9-folded", strings.TrimSpace(strings.Repeat("KALAS ", 9)), "KALAS KALAS KALAS"},
		// 多语种/符号系统同构：CJK、emoji、纯标点连跑同折。
		{"cjk", strings.TrimSpace(strings.Repeat("火锅 ", 20)), "火锅 火锅 火锅"},
		{"dash", strings.TrimSpace(strings.Repeat("-- ", 935)), "-- -- --"},
		// 散布重复（非连续）不折：A B A B ... 永不触发。
		{"alternating", strings.TrimSpace(strings.Repeat("A B ", 30)), strings.TrimSpace(strings.Repeat("A B ", 30))},
		// 周期 2 的整行交替不折（行级判据同样是连续相等）。
		{"line-alternating", strings.TrimSpace(strings.Repeat("x\ny\n", 20)), strings.TrimSpace(strings.Repeat("x\ny\n", 20))},
		// 跨行同文连跑 10 > 8 折到 3 行。
		{"line-run", strings.TrimSpace(strings.Repeat("KALAS\n", 10)), "KALAS\nKALAS\nKALAS"},
		// 空白行不参与行级折叠（段落结构保真）。
		{"blank-lines", strings.TrimSpace(strings.Repeat("\n\n", 12)), ""},
		// 真实 OCR 形态：前缀字段 + 死循环行 + 尾部真信号，折叠后首尾都在。
		{
			"ocr-shape",
			"OCR: menu\n" + strings.Repeat("KALAS\n", 230) + "price 42",
			"OCR: menu\nKALAS\nKALAS\nKALAS\nprice 42",
		},
		// 行内连跑混合：前后各一段，只折超长段。
		{
			"mixed-runs",
			"start " + strings.TrimSpace(strings.Repeat("w ", 9)) + " mid " + strings.TrimSpace(strings.Repeat("z ", 4)) + " end",
			"start w w w mid z z z z end",
		},
	}
	for _, tc := range cases {
		if got := CollapseDegenerateTokenRuns(tc.in); got != tc.want {
			t.Fatalf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

// 折叠后词项打分通道严格中性：保留份数 ceil(e)=3 使 tf 增益 1+min(1,ln tf)
// 仍处饱和段（ln 3 > 1），与折叠前（tf 更大、同样饱和）逐位同值。
func TestRunFoldKeepsTFGainSaturated(t *testing.T) {
	if runFoldKeep < 3 {
		t.Fatalf("保留数 %d 低于 tf 饱和点 e，折叠将变成对词项的征税", runFoldKeep)
	}
	if runFoldTrigger != 8 {
		t.Fatalf("触发线应为 ceil(e^2)=8，got %d", runFoldTrigger)
	}
}

// 幂等性：折叠输出再过一遍折叠必须不动点。
func TestCollapseDegenerateTokenRunsIdempotent(t *testing.T) {
	in := "OCR: x\n" + strings.Repeat("KALAS\n", 230) + strings.TrimSpace(strings.Repeat("M ", 496))
	once := CollapseDegenerateTokenRuns(in)
	if twice := CollapseDegenerateTokenRuns(once); twice != once {
		t.Fatalf("非幂等：\nonce=%q\ntwice=%q", once, twice)
	}
}
