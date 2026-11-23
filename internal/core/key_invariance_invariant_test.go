// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package core

// key_invariance_invariant_test.go — 结构级 key 不变量（核心契约守卫）。
//
// 契约：atomizer key 只做标签置换（伪匿名 + 租户隔离），不得参与任何排序或
// 结构裁决。等价形式：对同一「逻辑语料」做任意随机重标（模拟任意 key 的
// keyed hash 输出），族根裁决（canonicalParent）在逻辑空间必须逐项一致。
//
// 本测试不依赖抽取器与语料文件：逻辑语料手造（含多宿主词），重标由测试内
// 确定性 PRNG 产生——任何 keyed hash 的输出都可视为逻辑词表的随机重标，故
// 8 组重标覆盖「任意 key」的等价类。core 镜像进 Rolling-Memory 后本测试随
// 镜像自动生效（客户端不各自重复该不变量）。

import (
	"math/rand"
	"testing"
)

// logicalCorpus 造一份含多宿主词的逻辑语料：同一逻辑词项在不同单元里携带
// 不同的族归属候选，触发族根裁决。
type logicalCorpus struct {
	docs    []map[int]int // unit → (logical term → logical group)
	termN   int
	docN    int
	dfOf    map[int]int // 逻辑词 → 文档频次（决定 canonical 裁决）
	groupOf map[int]int // 逻辑词 → 期望 canonical 根（逻辑 id）
}

func buildLogicalCorpus() logicalCorpus {
	const termN, docN = 40, 36
	lc := logicalCorpus{
		docs:    make([]map[int]int, docN),
		termN:   termN,
		docN:    docN,
		dfOf:    make(map[int]int, termN),
		groupOf: make(map[int]int, termN),
	}
	rng := rand.New(rand.NewSource(20261123))
	for u := 0; u < docN; u++ {
		terms := make(map[int]int, 16)
		// 背景共享词 0..7：全语料出现，族根 = 自身。
		for t := 0; t < 8; t++ {
			terms[t] = t
		}
		// 高载体宿主 10..14：全语料出现且 bits 更大（作为 20..24 的竞争根）。
		for t := 10; t < 15; t++ {
			terms[t] = t
		}
		// 多宿主词 20..24：全语料出现，族归属按单元交替（own 18 / alt 18），
		// canonical 裁决在 df 平手后由 bits 决定 → 稳定选 10..14。
		for t := 20; t < 25; t++ {
			if u%2 == 0 {
				terms[t] = t
			} else {
				terms[t] = t - 10
			}
		}
		// 随机稀词 25..39。
		for k := 0; k < 4; k++ {
			terms[25+rng.Intn(termN-25)] = 25 + rng.Intn(termN-25)
		}
		for t := range terms {
			lc.dfOf[t]++
		}
		lc.docs[u] = terms
	}
	// 期望 canonical 根：df 平手、alt 的 bits 更大（40+）⇒ 选 alt。
	for t := 20; t < 25; t++ {
		lc.groupOf[t] = t - 10
	}
	return lc
}

// relabel 生成逻辑词表的一次随机重标：逻辑 id → 64-bit 标签。同时提供逆映射。
func relabel(termN int, rng *rand.Rand) (map[int]uint64, map[uint64]int) {
	fwd := make(map[int]uint64, termN)
	inv := make(map[uint64]int, termN)
	for t := 0; t < termN; t++ {
		var v uint64
		for {
			v = rng.Uint64()
			if v == 0 {
				continue
			}
			if _, dup := inv[v]; !dup {
				break
			}
		}
		fwd[t] = v
		inv[v] = t
	}
	return fwd, inv
}

func docsUnderRelabel(lc logicalCorpus, fwd map[int]uint64) []Doc {
	docs := make([]Doc, lc.docN)
	for u := 0; u < lc.docN; u++ {
		terms := make(map[uint64]Term, len(lc.docs[u]))
		counts := make(map[uint64]int, len(lc.docs[u]))
		positions := make(map[uint64][]TermPosition, len(lc.docs[u]))
		for l, g := range lc.docs[u] {
			h := fwd[l]
			bits := uint16(24 + l%12)
			if l >= 10 && l < 15 {
				bits = 40 // 竞争宿主：更大载体宽度，保证 canonical 在 df 平手后选它
			}
			terms[h] = Term{Hash: h, Bits: bits, Group: fwd[g]}
			counts[h] = 1
			// 句内结构：每篇 3 句、每句 5 槽，多宿主词与背景词同句出现，
			// 使短语 dyad / 互证通道真正活跃（否则查询级测试恒真）。
			sent := l / 5
			slot := l % 5
			if l >= 20 {
				sent = 1 + (l-20)%2
				slot = 3
			}
			positions[h] = []TermPosition{{Sentence: sent, Slot: slot}}
		}
		docs[u] = Doc{
			UnitID:     unitIDFor(u),
			OccurredAt: 1_700_000_000 + int64(u)*86_400, // 一天一间隔：激活时空簇通道
			Terms:      terms,
			Counts:     counts,
			Positions:  positions,
		}
	}
	return docs
}

func unitIDFor(u int) string {
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	if u == 0 {
		return "u0"
	}
	var b []byte
	for u > 0 {
		b = append([]byte{digits[u%36]}, b...)
		u /= 36
	}
	return "u" + string(b)
}

// TestKeyInvarianceFamilyRootUnderRelabel 断言族根裁决对随机重标不变：
// 8 组独立重标下，每个逻辑词的 canonical 根在逻辑空间一致。
func TestKeyInvarianceFamilyRootUnderRelabel(t *testing.T) {
	lc := buildLogicalCorpus()
	const relabels = 8
	rng := rand.New(rand.NewSource(0x5eed))
	var reference map[int]int
	for r := 0; r < relabels; r++ {
		fwd, inv := relabel(lc.termN, rng)
		docs := docsUnderRelabel(lc, fwd)
		idx := BuildIndex(docs)
		if idx.N != lc.docN {
			t.Fatalf("relabel=%d index N=%d want %d", r, idx.N, lc.docN)
		}
		pg := scanParentGroups(idx)
		logical := make(map[int]int, len(pg))
		for h, root := range pg {
			logical[inv[h]] = inv[root]
		}
		if reference == nil {
			reference = logical
			// 合理自检：多宿主词确实触发了换根（否则语料没造出裁决分歧）。
			differing := 0
			for t := 20; t < 25; t++ {
				if reference[t] != t {
					differing++
				}
			}
			if differing == 0 {
				t.Fatalf("语料未构造出多宿主换根，测试无意义")
			}
			continue
		}
		for l, root := range reference {
			if got := logical[l]; got != root {
				t.Fatalf("relabel=%d 逻辑词 %d 的族根 = %d，与首组 %d 不一致（key 影响了结构裁决）",
					r, l, got, root)
			}
		}
	}
}

// 查询级不变量不在此合成语料上重复：36 篇小语料构造不出 2.7% 边界题翻转
// （红队实测修复前代码亦 PASS，属恒真预言机），按纪律不留。查询级覆盖由
// eval 层全量门禁承担：TestATMAtomizerKeySensitivity（4-key range 0.00）与
// TestSeatSaturationModes*/TestTermArbiterATMRules（self 臂逐位守卫）。
