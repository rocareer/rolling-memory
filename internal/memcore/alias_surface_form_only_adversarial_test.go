// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"strings"
	"testing"
)

// TestAliasSurfaceMechanicalContract 锁定别名闸的**机械契约**边界（架构契约「代码里的语义词闸门全拆 ·
// 宁宽勿误拒」）：
//
//	① 必红（机械契约，防错）：空表面 / 保留标识符（角色词与本体代号，防 Assistant 本体标识符冲突）/
//	   非载体表面（零字母数字）/ 超列宽与子句标点；
//	② 必绿（语义词闸门已拆，宁宽勿误拒、零丢弃）：代词 / 反身复合 / 首人称领属 / 「X的Y」跨类属格 /
//	   属主交叉 / 叠字称谓 / 同姓异名 / 大小写专名推定 / 上下位嵌入 —— 一律放行，语义资格 100% 归 LLM；
//	③ 真名回归锁：任何判据改动都不得把真名/昵称/品牌判死。
func TestAliasSurfaceMechanicalContract(t *testing.T) {
	mustReject := []struct {
		of, alias, why string
	}{
		{"Alice", "", "空表面"},
		{"Alice", "user", "角色词保留字"},
		{"Alice", "assistant", "角色词保留字"},
		{"Mr. Sherlock Holmes", "助手", "本体代号保留字（自指保护）"},
		{"Mr. Sherlock Holmes", "self", "本体代号保留字（自指保护）"},
		{"Alice", "---", "非载体表面（纯符号）"},
		{"Alice", "***", "非载体表面（纯符号）"},
		{"Alice", strings.Repeat("长", NodeLabelColumnCap+1), "超帽从句"},
		{"Alice", "did it!", "子句标点"},
	}
	for _, tc := range mustReject {
		if bad, reason := IsInvalidAliasSurface(tc.of, tc.alias); !bad {
			t.Fatalf("LEAK: IsInvalidAliasSurface(%q, %q) = false, want true（%s）", tc.of, tc.alias, tc.why)
		} else if reason == "" {
			t.Fatalf("LEAK: IsInvalidAliasSurface(%q, %q) rejected without reason", tc.of, tc.alias)
		}
	}

	// 语义词闸门已拆除清单：这些用例在旧口径下全部被拒收，新口径（宁宽勿丢）必须放行。
	semanticGatesRemoved := []struct {
		of, alias, oldGate string
	}{
		{"张伟", "他的朋友", "跨类属格"},
		{"张伟", "李四的狗", "属主交叉"},
		{"咪咪", "我的猫", "首人称领属"},
		{"Alice", "my dog", "首人称领属（拉丁）"},
		{"咪咪", "豆豆", "叠字称谓"},
		{"王志诚", "王杰诚", "同姓异名碰撞"},
		{"Alice", "the dog", "大小写专名推定"},
		{"unknown person", "Nova", "大小写专名推定（宿主侧）"},
		{"Elena", "a small dog", "泛称 determiner NP"},
		{"哈利波特", "哈利波特与混血王子上下册全集", "上下位嵌入"},
		{"林晚", "她自己", "代词 / 反身复合"},
	}
	for _, tc := range semanticGatesRemoved {
		if bad, reason := IsInvalidAliasSurface(tc.of, tc.alias); bad {
			t.Fatalf("SEMANTIC GATE REMAINS: %q→%q（%s）仍被拒收：%s", tc.of, tc.alias, tc.oldGate, reason)
		}
	}

	// 真名回归锁：曾是香农/形态阈值误杀重灾区，任何改动都必须 100% 放行。
	mustPassNames := []struct {
		of, alias string
	}{
		{"Joanna", "Jo"},
		{"Evan", "Ev"},
		{"James", "J"},
		{"王羲之", "右军"},
		{"克里斯蒂娜", "Cristina"},
		{"人工智能", "大模型"},
		{"协和医学院", "北京名校"},
		{"复旦大学", "上海高校"},
		{"清华大学", "一流学府"},
		{"品牌", "美的"},
		{"Alice", "art"},
		{"Alice", "cat"},
	}
	for _, tc := range mustPassNames {
		if bad, reason := IsInvalidAliasSurface(tc.of, tc.alias); bad {
			t.Fatalf("FALSE KILL: IsInvalidAliasSurface(%q, %q) = true（%s）", tc.of, tc.alias, reason)
		}
	}
}

// TestAliasSurfaceNoEntropyVerdict 对偶守卫：把「同一结构、不同熵值」的表面成对投喂，
// 判定结果必须与熵值无关——否则说明先验阈值又回到了生死位上。
func TestAliasSurfaceNoEntropyVerdict(t *testing.T) {
	pairs := [][2]string{
		{"art", "qwxz"},
		{"cat", "zzzz"},
		{"gym", "qkjv"},
	}
	for _, p := range pairs {
		a, _ := IsInvalidAliasSurface("Alice", p[0])
		b, bReason := IsInvalidAliasSurface("Alice", p[1])
		if !a && b {
			t.Fatalf("ENTROPY VERDICT: rare surface %q rejected (%s) while common %q passed", p[1], bReason, p[0])
		}
	}
}
