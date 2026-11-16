// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"strings"
	"testing"
)

// 追问续答条帽两档：现役（关 · 出货逐字）必须保留；紧档（开 · RM_RECALL_FOLLOWUP_TIGHT=1）只压条帽、不改任何判据结构。
// 卷面结论：200 题 A/B（实证对比）⇒ 按架构契约回退为缺省关，本档仅作器材保留。
func TestShouldAttachFollowupClipDefaultAndTight(t *testing.T) {
	cases := []struct {
		name                   string
		bandIdx, chainIdx, cnt int
		score                  float64
		nonSubj, origNonSubj   bool
		wantOff, wantOn        bool // 关档帽 12/10/28/34 · 开档帽 4/3/6/8
	}{
		// 主带（bandIdx=0/1）无原词命中：缺省帽 10 → 9 真 / 10 假；紧档帽 4 → 3 真 / 4 假
		{"band0-plain-9", 0, 3, 9, 6.0, true, false, true, false},
		{"band0-plain-10", 0, 3, 10, 6.0, true, false, false, false},
		// 原词命中（origNonSubj）中等分：缺省帽 28 → 27 真 / 28 假；紧档帽 6 → 5 真 / 6 假
		{"band0-orig-27", 0, 3, 27, 6.0, true, true, true, false},
		{"band0-orig-5", 0, 3, 5, 6.0, true, true, true, true},
		// 高分长链（bandIdx=1, chainIdx<=12, score>=12）：缺省帽 34 → 33 真 / 34 假；紧档帽 8
		{"band1-hot-33", 1, 3, 33, 12.0, true, true, true, false},
		{"band1-hot-7", 1, 3, 7, 12.0, true, true, true, true},
		// 深尾带非原词命中：两档都恒假（判据结构未动）
		{"deep-tail", 9, 20, 1, 6.0, true, false, false, false},
		// 非 nonSubj：恒假
		{"non-subj-false", 0, 1, 1, 6.0, false, true, false, false},
		// 低分：恒假
		{"low-score", 0, 1, 1, 4.9, true, true, false, false},
	}
	for _, c := range cases {
		FollowupTightMode.Store(false)
		if got := ShouldAttachFollowupClip(c.bandIdx, c.chainIdx, c.cnt, c.score, c.nonSubj, c.origNonSubj); got != c.wantOff {
			t.Fatalf("%s 现役(关)：got=%v want=%v", c.name, got, c.wantOff)
		}
		FollowupTightMode.Store(true)
		if got := ShouldAttachFollowupClip(c.bandIdx, c.chainIdx, c.cnt, c.score, c.nonSubj, c.origNonSubj); got != c.wantOn {
			t.Fatalf("%s 紧档(开)：got=%v want=%v", c.name, got, c.wantOn)
		}
	}
	FollowupTightMode.Store(false)
}

// 帽位映射必须单调收紧且只认四档（34→8 / 28→6 / 12→4 / 其余→3）；关档时原样返回。
func TestTightenFollowupCapMapping(t *testing.T) {
	FollowupTightMode.Store(false)
	for _, c := range []int{34, 28, 12, 10, 3} {
		if got := tightenFollowupCap(c); got != c {
			t.Fatalf("关档应原样返回：cap=%d got=%d", c, got)
		}
	}
	FollowupTightMode.Store(true)
	defer FollowupTightMode.Store(false)
	for in, want := range map[int]int{34: 8, 28: 6, 12: 4, 10: 3, 3: 3} {
		if got := tightenFollowupCap(in); got != want {
			t.Fatalf("开档映射：cap=%d got=%d want=%d", in, got, want)
		}
	}
	seenFull := map[string]bool{"h1": true}
	rows := []TopologyRowView{{SourceTraceHash: "h1", Score: 38.0}}
	if !IsRedundantSameTurnCompanionBand(4, false, rows, seenFull, false) {
		t.Fatalf("expected bandIdx=4 same-turn companion micro-band without DirectXO to be redundant")
	}
	if IsRedundantSameTurnCompanionBand(2, false, rows, seenFull, false) {
		t.Fatalf("expected bandIdx=2 core band not to be skipped as redundant")
	}

}

// 对症版（FollowupNoveltyMode · **缺省关** · 单变量臂器材）：追问段只挂「含 ≥20.0b 未被覆盖新语素」的新事实。
// 200 题 A/B：#8635 关 181 vs #8636 开 179（Δ −2 且 `gold_lex_hit` 掉 2 题，问答桥接句如 "What else helps you?" 被误杀）
// ⇒ 守 §4.3 回归不变量保持缺省关，以 `RM_RECALL_FOLLOWUP_NOVELTY=1` 显式调用。
// 本测试显式开关两态：关档必须对同一输入保留旧行为；开档丢复述/寒暄、留新语素。
func TestDeduplicateFollowupSegmentsNovelty(t *testing.T) {
	main := []string{"Nespresso machine hit the account for exactly $150.00 on 2024-04-29"}
	restate := "yes the Nespresso machine was $150"   // 复述：内容全覆盖、无新语素
	chatter := "oh wow that is really quite nice"     // 寒暄：常见词堆叠、单 token 均 <8b
	novel := "then the Breville bes870xl rang up $89" // 新事实：breville/bes870xl 未覆盖高先验
	segs := restate + " → " + chatter + " → " + novel

	FollowupNoveltyMode.Store(false)
	gotOff := DeduplicateFollowupSegments(segs, main, nil)
	if !strings.Contains(gotOff, restate) || !strings.Contains(gotOff, novel) {
		t.Fatalf("关档应保留复述与新事实段：%q", gotOff)
	}
	FollowupNoveltyMode.Store(true)
	gotOn := DeduplicateFollowupSegments(segs, main, nil)
	if strings.Contains(gotOn, restate) || strings.Contains(gotOn, chatter) {
		t.Fatalf("开档应丢复述/寒暄段：%q", gotOn)
	}
	if !strings.Contains(gotOn, "Breville") {
		t.Fatalf("开档应保留新事实段：%q", gotOn)
	}
	// 全部无新事实 ⇒ 空串（调用方据此跳过附加）
	if got := DeduplicateFollowupSegments(restate+" → "+chatter, main, nil); got != "" {
		t.Fatalf("全复述应整段不落：%q", got)
	}
	FollowupNoveltyMode.Store(false)
}
