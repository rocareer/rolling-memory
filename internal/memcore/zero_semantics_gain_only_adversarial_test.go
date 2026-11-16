// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import "testing"

// 架构契约「代码禁处理语义词 · 香农只做增益不做截断」配对对抗：
// 本文件锁定三处「先验/熵阈值截断阈值」拆除后的放宽不变量。
// 回归守卫：把阈值条件加回（TrimTrailingGeminateRune 的 `StringShannonBits(cand) >= 6.0`、
// CorrectAliasTargetByPrefix 的 `StringShannonBits(alName) < 4.5`）即转红。
func TestZeroSemanticsEntropyGateRemoval(t *testing.T) {
	// ① 叠字剥离只认形式：末两字符相同即剥，残余串比特数不再作截断条件。
	if got := TrimTrailingGeminateRune("aa"); got != "a" {
		t.Fatalf("TrimTrailingGeminateRune(\"aa\") = %q，期望 \"a\"（低比特叠字不得被熵阈值拦下）", got)
	}
	if got := TrimTrailingGeminateRune("mell"); got != "mel" {
		t.Fatalf("TrimTrailingGeminateRune(\"mell\") = %q，期望 \"mel\"", got)
	}
	if got := TrimTrailingGeminateRune("mark"); got != "mark" {
		t.Fatalf("TrimTrailingGeminateRune(\"mark\") = %q，期望原样（末两字符不叠）", got)
	}

	// ② 低比特真前缀昵称（"Jo" < 4.5 bits）必须仍可被校正到全称候选。
	got := CorrectAliasTargetByPrefix(AliasSpec{Of: "Coach", Alias: "Jo"}, "Joanna")
	if got.Of != "Joanna" {
		t.Fatalf("CorrectAliasTargetByPrefix(Jo→?) Of = %q，期望 \"Joanna\"（熵阈值不得拦死真前缀昵称）", got.Of)
	}
	if got.Basis != "shortening" {
		t.Fatalf("CorrectAliasTargetByPrefix Basis = %q，期望 \"shortening\"", got.Basis)
	}
}

// IsLabelExactly 语言无关哨兵：任一交付语种变体精确命中，且禁子串误配。
// 回归守卫：改成 strings.Contains 即被「时间未知（讲述时点）」用例打红。
func TestIsLabelExactlyTimeUnknown(t *testing.T) {
	for _, s := range []string{"时间未知", "time_unknown", "time_unknown/时间未知", " 时间未知 "} {
		if !IsLabelExactly(s, LabelTimeUnknown) {
			t.Fatalf("IsLabelExactly(%q) = false，期望 true", s)
		}
	}
	for _, s := range []string{"", "未知", "时间未知（讲述时点）", "unknown", "time_unknown_x"} {
		if IsLabelExactly(s, LabelTimeUnknown) {
			t.Fatalf("IsLabelExactly(%q) = true，期望 false（精确匹配、禁子串误配）", s)
		}
	}
}
