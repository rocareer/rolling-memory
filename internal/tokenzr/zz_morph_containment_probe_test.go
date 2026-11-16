// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package tokenzr

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"testing"
)

// TestZZMorphContainmentProbe 表示层探针（设计研判「先测表示，不先改排序」Step 2）。
//
// 目的：核对 tokenz-r 128k BPE 对「屈折/构词变体」实际产生的**结构子块**，
// 并量化「结构子块包含通道」对以下五类的覆盖与风险，只打印读数，不做任何生产行为变更：
//  1. 词形联系正例（hotel/hotels、run/running、study/studies、CJK 复合、标识符前缀、驼峰后缀）；
//  2. 包含假阳性陷阱（art/party、car/carpet、there/he）；
//  3. 互不包含的词形变化（go/went、mouse/mice，形式通道预期不可达）；
//  4. 多语种（CJK / 日文 / 韩文）；
//  5. 数字与标识符（containers.go / 驼峰标识符）。
//
// 发射规则（探针与生产实验通道同源，见 service 侧同规则实现）：
//
//	pieces = SplitSubwords(" "+w) ∪ SplitSubwords(w)（含空格契约形态 ∪ 词内形态）
//	emit   = 单个 tokenz-r 词表片段（去掉整词自身），按 TokenPriorBits 降序取 ≤2 个，
//	         且片段先验位宽 ≥ TokenPriorBits(w)/e（1/e 普适常数地板）。
func TestZZMorphContainmentProbe(t *testing.T) {
	pairs := []struct {
		q, d string
		want string // MATCH = 形式通道应可达；NOMATCH = 应不可达（假阳性陷阱）；OOS = 超出形式通道能力（异干屈折）
		note string
	}{
		{"hotel", "hotels", "MATCH", "英-复数"},
		{"run", "running", "MATCH", "英-进行时"},
		{"study", "studies", "MATCH", "英-y→ies"},
		{"art", "party", "NOMATCH", "包含假阳性陷阱"},
		{"car", "carpet", "NOMATCH", "包含假阳性陷阱"},
		{"he", "there", "NOMATCH", "包含假阳性陷阱"},
		{"go", "went", "OOS", "异干屈折（形式通道不可达）"},
		{"mouse", "mice", "OOS", "异干屈折（形式通道不可达）"},
		{"电脑", "笔记本电脑", "MATCH", "CJK 后缀概念"},
		{"笔记本", "笔记本电脑", "MATCH", "CJK 前缀概念"},
		{"记忆", "记忆系统", "MATCH", "CJK 前缀概念"},
		{"containers", "containers.go", "MATCH", "标识符前缀"},
		{"CosineThreshold", "EndogenousL2MergeCosineThreshold", "MATCH", "驼峰后缀复合"},
		{"東京", "東京都", "MATCH", "日文复合"},
		{"한국", "한국어", "MATCH", "韩文复合"},
	}

	printed := 0
	for _, p := range pairs {
		q := strings.ToLower(strings.TrimSpace(p.q))
		d := strings.ToLower(strings.TrimSpace(p.d))
		emQ := BPEContainedSubblocks(q)
		emD := BPEContainedSubblocks(d)
		emQ2 := probeSubblockTagsV2(q)
		emD2 := probeSubblockTagsV2(d)
		// 三条真实引擎级匹配路径：
		//   F 查询精确 → 文档子块（u.fullStem[hExact_q] 探针路径）
		//   R 文档精确 → 查询子块（qt.hStem 探针 u.fullStem 路径）
		//   S 双方共享同一子块（stem 槽 ↔ 文档 stem 注册表交集路径）
		matchF := containsStr(emD, q)
		matchR := containsStr(emQ, d)
		matchS := intersectsStr(emQ, emD)
		match := matchF || matchR || matchS
		matchV2 := containsStr(emD2, q) || containsStr(emQ2, d) || intersectsStr(emQ2, emD2)
		t.Logf("q=%-14q d=%-38q | emitQ(v1)=%-20v emitD(v1)=%-26v => v1=%-5v | emitQ(v2)=%-20v emitD(v2)=%-26v => v2=%-5v | want=%-7s (%s)",
			q, d, emQ, emD, match, emQ2, emD2, matchV2, p.want, p.note)
		printed++
	}
	if printed != len(pairs) {
		t.Fatalf("probe observability violated: printed=%d pairs=%d", printed, len(pairs))
	}

	// 候选片段明细（bits 与载体占比），供两规则选型对照
	for _, w := range []string{"hotels", "containers.go", "endogenousl2mergecosinethreshold", "mice", "running", "study"} {
		wp := probeCandidateDetail(w)
		t.Logf("[候选明细] w=%-38q wordBits=%.2f wordCarry=%.2f cands=%v", w, TokenPriorBits(w), probeCarryBits(w), wp)
	}

	// CJK/日/韩：SplitSubwordConstituents 拆包形态对照（leaves / phrases），供发射规则选型参考
	for _, w := range []string{"笔记本电脑", "记忆系统", "東京都", "한국어"} {
		leaves, phrases := SplitSubwordConstituents(w)
		sp := SplitSubwords(" " + w)
		rw := SplitSubwords(w)
		t.Logf("[CJK对照] w=%-12q spaced=%-24v raw=%-24v leaves=%-24v phrases=%v", w, sp, rw, leaves, phrases)
	}
}

// probeSubblockTags 探针侧发射规则实现（与 service 侧实验通道 structuralSubblockTags 同规则）。
func probeSubblockTags(w string) []string {
	w = strings.ToLower(strings.TrimSpace(w))
	if w == "" {
		return nil
	}
	wordBits := TokenPriorBits(w)
	if wordBits <= 0 {
		return nil
	}
	floor := wordBits / math.E
	seen := map[string]bool{w: true}
	type cand struct {
		s string
		b float64
	}
	var cands []cand
	add := func(p string) {
		s := strings.ToLower(strings.TrimSpace(p))
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		b := TokenPriorBits(s)
		if b < floor {
			return
		}
		cands = append(cands, cand{s, b})
	}
	for _, p := range SplitSubwords(" " + w) {
		add(p)
	}
	for _, p := range SplitSubwords(w) {
		add(p)
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].b != cands[j].b {
			return cands[i].b > cands[j].b
		}
		return cands[i].s < cands[j].s
	})
	if len(cands) > 2 {
		cands = cands[:2]
	}
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.s)
	}
	return out
}

func containsStr(list []string, s string) bool {
	for _, it := range list {
		if it == s {
			return true
		}
	}
	return false
}

// probeSubblockTagsV2 候选规则 v2（地表自适应占比版）：
// 候选源 = spaced ∪ raw pieces ∪（对无空格表意文字）constituents leaves+phrases；
// 过滤 = 片段载体占比 ≥ 1/(候选数+1)（内生结构量，无固定常数）；排序 = bits 降序；top2。
func probeSubblockTagsV2(w string) []string {
	w = strings.ToLower(strings.TrimSpace(w))
	if w == "" {
		return nil
	}
	seen := map[string]bool{w: true}
	var all []string
	add := func(p string) {
		s := strings.ToLower(strings.TrimSpace(p))
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		all = append(all, s)
	}
	for _, p := range SplitSubwords(" " + w) {
		add(p)
	}
	for _, p := range SplitSubwords(w) {
		add(p)
	}
	// 无条件追加 BPE 二叉合并树拆包输出（leaves + phrases）：拉丁词通常无额外输出，
	// 无空格表意/音节文字获得 CJK 复合子块；零脚本特判、零语种分支。
	leaves, phrases := SplitSubwordConstituents(w)
	for _, p := range leaves {
		add(p)
	}
	for _, p := range phrases {
		add(p)
	}
	wBits := TokenPriorBits(w)
	if wBits <= 0 {
		return nil
	}
	{
		type cn struct {
			s string
			b float64
			r float64
		}
		var cands []cn
		floor := 1.0 / float64(len(all)+1)
		for _, s := range all {
			// 占比 v3 = 片段 Zipfian-BPE 自信息 / 整词自信息（零复刻、零魔数的信息量占比）
			r := TokenPriorBits(s) / wBits
			if r >= floor {
				cands = append(cands, cn{s, TokenPriorBits(s), r})
			}
		}
		// 排序 v3：占比降序（桥接价值正比于片段对整词的覆盖度）→ bits 降序 → 字典序
		sort.SliceStable(cands, func(i, j int) bool {
			if cands[i].r != cands[j].r {
				return cands[i].r > cands[j].r
			}
			if cands[i].b != cands[j].b {
				return cands[i].b > cands[j].b
			}
			return cands[i].s < cands[j].s
		})
		if len(cands) > 2 {
			cands = cands[:2]
		}
		out := make([]string, 0, len(cands))
		for _, c := range cands {
			out = append(out, c.s)
		}
		return out
	}
}

func probeCarryBits(s string) float64 {
	var b float64
	for _, r := range s {
		b += bpeRuneCarryBits(r)
	}
	return b
}

// probeCandidateDetail 打印全部候选片段的 (片段, bits, 载体占比) 明细。
func probeCandidateDetail(w string) []string {
	w = strings.ToLower(strings.TrimSpace(w))
	seen := map[string]bool{w: true}
	var all []string
	add := func(p string) {
		s := strings.ToLower(strings.TrimSpace(p))
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		all = append(all, s)
	}
	for _, p := range SplitSubwords(" " + w) {
		add(p)
	}
	for _, p := range SplitSubwords(w) {
		add(p)
	}
	wCarry := probeCarryBits(w)
	var out []string
	for _, s := range all {
		ratio := 0.0
		if wCarry > 0 {
			ratio = probeCarryBits(s) / wCarry
		}
		out = append(out, fmt.Sprintf("%s(b=%.2f,r=%.2f)", s, TokenPriorBits(s), ratio))
	}
	return out
}

func intersectsStr(a, b []string) bool {
	for _, x := range a {
		if containsStr(b, x) {
			return true
		}
	}
	return false
}

func clip(list []string) []string {
	if len(list) > 5 {
		return list[:5]
	}
	return list
}
