// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package core

import (
	"fmt"
	"hash/fnv"
	"math"
	"sort"
	"strings"
)

// 诊断专用（临时器材）：把引擎的索引与簇状态逐表摘要成 hex 指纹，供跨引擎
// 逐位对拍定位非确定分歧层。发布链分歧根因隔离完成后本文件整体删除。
//
// DebugParentRootDiff（canonical 族根 vs legacy min-hash 族根的分歧面）已随
// min-hash 回退一并删除：族根现在只有 canonicalParent 一个来源，无第二个可比的
// 实现。

// DebugEpisodeSplitKnifeEdge 返回情节切分阈值在退化输入（σ=0 等间隔）下的
// 刀刃位置：tau = Expm1(mu+sig) 与首个桶间隔 gap 之差，正数表示每个间隔都
// 超过阈值（全分裂），0 或负数表示不分裂。
func DebugEpisodeSplitKnifeEdge(e *Engine) (tau, gap, diff float64) {
	if e == nil || e.clusterState == nil || len(e.clusterState.bucketTimes) < 2 {
		return 0, 0, 0
	}
	st := e.clusterState
	tau = math.Expm1(st.muLogGap + st.sigLogGap)
	if tau <= 0 {
		tau = st.meanGap * eulerNumber
	}
	gap = float64(st.bucketTimes[1] - st.bucketTimes[0])
	return tau, gap, gap - tau
}

// DebugClusterBucketStats 汇总桶/情节结构规模（诊断专用）：桶数、非空桶数、
// 最大桶成员数、情节组件数与最大组件规模。
func DebugClusterBucketStats(e *Engine) (buckets, nonEmpty, maxBucketSize, episodes, maxEpisodeSize int) {
	if e == nil || e.clusterState == nil {
		return 0, 0, 0, 0, 0
	}
	st := e.clusterState
	buckets = len(st.bucketTimes)
	for b := 0; b+1 < len(st.bucketRowPtr); b++ {
		size := int(st.bucketRowPtr[b+1] - st.bucketRowPtr[b])
		if size > 0 {
			nonEmpty++
		}
		if size > maxBucketSize {
			maxBucketSize = size
		}
	}
	epSize := map[int32]int{}
	for _, ep := range st.episodeOf {
		if ep < 0 {
			continue
		}
		epSize[ep]++
		if epSize[ep] > maxEpisodeSize {
			maxEpisodeSize = epSize[ep]
		}
	}
	episodes = len(epSize)
	return buckets, nonEmpty, maxBucketSize, episodes, maxEpisodeSize
}

// DebugEngineTableDigests 返回引擎逐表指纹（表名 → "len:hex" 或标量位型）。
func DebugEngineTableDigests(e *Engine) map[string]string {
	if e == nil || e.Index == nil {
		return map[string]string{"engine": "nil"}
	}
	out := map[string]string{}
	idx := e.Index
	out["index.N"] = itoa(int(idx.N))
	out["index.DF"] = digestU64Map(idx.DF)
	out["index.TermBits"] = digestU64MapValues(idx.TermBits)
	out["index.postings.len"] = itoa(len(idx.Postings))
	out["index.DocTerms"] = digestDocTerms(idx)
	out["index.UnitGroups"] = digestUnitGroups(idx)

	st := e.clusterState
	if st == nil {
		out["cluster.state"] = "nil"
		return out
	}
	out["c.termKeys"] = digestU64Slice(st.termKeys)
	out["c.termEffWeight"] = digestF64Slice(st.termEffWeight)
	out["c.termProperRatio"] = digestF64Slice(st.termProperRatio)
	out["c.termParentGroup"] = digestU64Slice(st.termParentGroup)
	out["c.termIsPrimary"] = digestU8Slice(st.termIsPrimary)
	out["c.episodeOf"] = digestI32Slice(st.episodeOf)
	out["c.clusters"] = digestI32Slice(st.clusters)
	out["c.bucketTimes"] = digestI64Slice(st.bucketTimes)
	out["c.bucketUnits"] = digestI32Slice(st.bucketUnits)
	out["c.docEntriesFlat"] = digestDocEntries(st)
	out["c.posPool"] = digestU32Slice(st.posPool)
	out["c.sentenceEnergyMean"] = f64bits(st.sentenceEnergyMean)
	out["c.meanGap"] = f64bits(st.meanGap)
	out["c.muLogGap"] = f64bits(st.muLogGap)
	out["c.sigLogGap"] = f64bits(st.sigLogGap)
	out["c.entryMeanBits"] = f64bits(st.entryMeanBits)
	out["c.fastDocs"] = digestFastDocs(st)
	out["c.dayCos"] = digestU64F64Map(st.dayCos)
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func f64bits(f float64) string {
	return itoa(int(math.Float64bits(f)))
}

func fnvU64(chunks ...[]byte) string {
	h := fnv.New64a()
	for _, c := range chunks {
		_, _ = h.Write(c)
	}
	return itoa(int(h.Sum64()))
}

func digestU64Slice(s []uint64) string {
	b := make([]byte, len(s)*8)
	for i, v := range s {
		putU64(b[i*8:], uint64(v))
	}
	return itoa(len(s)) + ":" + fnvU64(b)
}

func digestI32Slice(s []int32) string  { return digestU64SliceI32(s) }
func digestI64Slice(s []int64) string  { return digestU64SliceI64(s) }
func digestU32Slice(s []uint32) string { return digestU64SliceU32(s) }
func digestU8Slice(s []uint8) string   { return digestU64SliceU8(s) }
func digestF64Slice(s []float64) string {
	b := make([]byte, len(s)*8)
	for i, v := range s {
		putU64(b[i*8:], math.Float64bits(v))
	}
	return itoa(len(s)) + ":" + fnvU64(b)
}

func digestU64SliceI32(s []int32) string {
	b := make([]byte, len(s)*4)
	for i, v := range s {
		putU32(b[i*4:], uint32(v))
	}
	return itoa(len(s)) + ":" + fnvU64(b)
}

func digestU64SliceI64(s []int64) string {
	b := make([]byte, len(s)*8)
	for i, v := range s {
		putU64(b[i*8:], uint64(v))
	}
	return itoa(len(s)) + ":" + fnvU64(b)
}

func digestU64SliceU32(s []uint32) string {
	b := make([]byte, len(s)*4)
	for i, v := range s {
		putU32(b[i*4:], v)
	}
	return itoa(len(s)) + ":" + fnvU64(b)
}

func digestU64SliceU8(s []uint8) string {
	return itoa(len(s)) + ":" + fnvU64(s)
}

func putU64(b []byte, v uint64) {
	for i := 0; i < 8; i++ {
		b[i] = byte(v >> (8 * i))
	}
}

func putU32(b []byte, v uint32) {
	for i := 0; i < 4; i++ {
		b[i] = byte(v >> (8 * i))
	}
}

func digestU64Map(m map[uint64]int) string {
	keys := make([]uint64, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortU64(keys)
	b := make([]byte, 0, len(keys)*12)
	for _, k := range keys {
		var kb [12]byte
		putU64(kb[:8], k)
		putU32(kb[8:], uint32(m[k]))
		b = append(b, kb[:]...)
	}
	return itoa(len(keys)) + ":" + fnvU64(b)
}

func digestU64MapValues(m map[uint64]uint16) string {
	keys := make([]uint64, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortU64(keys)
	b := make([]byte, 0, len(keys)*12)
	for _, k := range keys {
		var kb [12]byte
		putU64(kb[:8], k)
		putU32(kb[8:], uint32(m[k]))
		b = append(b, kb[:]...)
	}
	return itoa(len(keys)) + ":" + fnvU64(b)
}

func digestU64F64Map(m map[uint64]float64) string {
	keys := make([]uint64, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortU64(keys)
	b := make([]byte, 0, len(keys)*16)
	for _, k := range keys {
		var kb [16]byte
		putU64(kb[:8], k)
		putU64(kb[8:], math.Float64bits(m[k]))
		b = append(b, kb[:]...)
	}
	return itoa(len(keys)) + ":" + fnvU64(b)
}

func sortU64(keys []uint64) {
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
}

func digestDocTerms(idx *Index) string {
	h := fnv.New64a()
	var b4 [4]byte
	for u := 0; u < len(idx.DocTerms) && u < int(idx.N); u++ {
		putU32(b4[:], uint32(len(idx.DocTerms[u])))
		_, _ = h.Write(b4[:])
		for _, t := range idx.DocTerms[u] {
			var b8 [8]byte
			putU64(b8[:], t)
			_, _ = h.Write(b8[:])
		}
	}
	return itoa(len(idx.DocTerms)) + ":" + itoa(int(h.Sum64()))
}

func digestUnitGroups(idx *Index) string {
	h := fnv.New64a()
	count := 0
	for u := 0; u < len(idx.UnitGroups) && u < int(idx.N); u++ {
		g := idx.UnitGroups[u]
		if g == nil {
			continue
		}
		count++
		keys := make([]uint64, 0, len(g))
		for k := range g {
			keys = append(keys, k)
		}
		sortU64(keys)
		var b8 [8]byte
		putU32(b8[:4], uint32(len(keys)))
		_, _ = h.Write(b8[:4])
		for _, k := range keys {
			putU64(b8[:], k)
			_, _ = h.Write(b8[:])
			putU64(b8[:], g[k])
			_, _ = h.Write(b8[:])
		}
	}
	return itoa(count) + ":" + itoa(int(h.Sum64()))
}

func digestDocEntries(st *rmClusterState) string {
	b := make([]byte, 0, len(st.docEntriesFlat)*20)
	for _, e := range st.docEntriesFlat {
		var kb [20]byte
		putU64(kb[:8], e.id)
		putU64(kb[8:16], e.group)
		putU32(kb[16:20], e.posOff)
		b = append(b, kb[:]...)
		b = append(b, byte(e.posLen), byte(e.tf), byte(e.bits), byte(e.flags), byte(e.roleExt))
	}
	return itoa(len(st.docEntriesFlat)) + ":" + fnvU64(b)
}

func digestFastDocs(st *rmClusterState) string {
	b := make([]byte, 0, len(st.fastDocs)*24)
	for _, f := range st.fastDocs {
		var kb [24]byte
		putU64(kb[:8], math.Float64bits(f.docTotal))
		putU64(kb[8:16], math.Float64bits(f.subjNormSq))
		putU64(kb[16:24], uint64(f.day))
		b = append(b, kb[:]...)
	}
	return itoa(len(st.fastDocs)) + ":" + fnvU64(b)
}

// ---- 跨 key 结构对拍（临时诊断，key-invariance 收口后随本文件删除）----
// 哈希值本身跨 key 必然不同，因此逐层只比较 key 无关的结构签名：
// df/bits/flags/roleExt/根的(df,bits)/posLen/分区结构/时间桶。第一处出现
// 结构分歧的层即 key 泄漏层。

func diagUnitStructSig(idx *Index, st *rmClusterState, u int32) string {
	entries := st.docEntriesOf(u)
	parts := make([]string, 0, len(entries))
	for i := range entries {
		en := &entries[i]
		rootDF, rootBits := 0, 0
		if g, ok := st.parentGroupOf(en.id); ok && g != 0 {
			rootDF = idx.TermDF(g)
			rootBits = int(idx.TermBitsOf(g))
		}
		parts = append(parts, fmt.Sprintf("%d:%d:%d:%d:%d:%d:%d:%t",
			idx.TermDF(en.id), en.bits, en.flags, en.roleExt,
			rootDF, rootBits, en.posLen, en.group == en.id))
	}
	sort.Strings(parts)
	return fmt.Sprintf("n=%d|%s", len(parts), strings.Join(parts, ","))
}

func diagPartitionSig(assign []int32) string {
	groups := map[int32][]int32{}
	for u, c := range assign {
		groups[c] = append(groups[c], int32(u))
	}
	keys := make([]int32, 0, len(groups))
	for c := range groups {
		keys = append(keys, c)
	}
	sort.Slice(keys, func(a, b int) bool { return keys[a] < keys[b] })
	var b strings.Builder
	for _, c := range keys {
		ms := groups[c]
		sort.Slice(ms, func(a, b int) bool { return ms[a] < ms[b] })
		fmt.Fprintf(&b, "|%d:%d", ms[0], len(ms))
		for _, m := range ms[1:] {
			fmt.Fprintf(&b, ",%d", m)
		}
	}
	return b.String()
}

func diagTrunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// DebugCompareStructure 逐层对比两个引擎的 key 无关结构。层序：单元词项
// 签名、实体簇分区、情节分区、时间桶、effWeight 多重集。返回逐层读数行。
func DebugCompareStructure(a, b *Engine) []string {
	out := []string{}
	if a == nil || b == nil || a.Index == nil || b.Index == nil || a.clusterState == nil || b.clusterState == nil {
		return []string{"engines not materialized"}
	}
	sa, sb := a.clusterState, b.clusterState
	ia, ib := a.Index, b.Index
	if ia.N != ib.N {
		return append(out, fmt.Sprintf("DIVERGES N: %d vs %d", ia.N, ib.N))
	}

	unitDiv, divU := 0, int32(-1)
	var sigA, sigB string
	for u := 0; u < ia.N; u++ {
		s1 := diagUnitStructSig(ia, sa, int32(u))
		s2 := diagUnitStructSig(ib, sb, int32(u))
		if s1 != s2 {
			unitDiv++
			if divU < 0 {
				divU, sigA, sigB = int32(u), s1, s2
			}
		}
	}
	if unitDiv == 0 {
		out = append(out, fmt.Sprintf("identical unit-entries: %d units", ia.N))
	} else {
		out = append(out, fmt.Sprintf("DIVERGES unit-entries: %d/%d units, first=%d (%s)", unitDiv, ia.N, divU, ia.UnitIDs[divU]))
		out = append(out, "  sig-a: "+diagTrunc(sigA, 400))
		out = append(out, "  sig-b: "+diagTrunc(sigB, 400))
	}

	s1, s2 := diagPartitionSig(sa.clusters), diagPartitionSig(sb.clusters)
	if s1 != s2 {
		out = append(out, "DIVERGES cluster partition: "+diagTrunc(s1, 200)+" vs "+diagTrunc(s2, 200))
	} else {
		out = append(out, fmt.Sprintf("identical cluster partition (%d groups)", strings.Count(s1, "|")))
	}
	if i32SlicesEq(sa.clusters, sb.clusters) {
		out = append(out, "identical raw cluster labels")
	} else {
		out = append(out, "DIVERGES raw cluster labels (partition same, label numbering differs)")
	}
	e1, e2 := diagPartitionSig(sa.episodeOf), diagPartitionSig(sb.episodeOf)
	if e1 != e2 {
		out = append(out, "DIVERGES episode partition: "+diagTrunc(e1, 200)+" vs "+diagTrunc(e2, 200))
	} else {
		out = append(out, fmt.Sprintf("identical episode partition (%d groups)", strings.Count(e1, "|")))
	}
	if i32SlicesEq(sa.episodeOf, sb.episodeOf) {
		out = append(out, "identical raw episode labels")
	} else {
		out = append(out, "DIVERGES raw episode labels (partition same, label numbering differs)")
	}
	if stringSlicesEq(sa.bucketTimes, sb.bucketTimes) {
		out = append(out, fmt.Sprintf("identical bucketTimes (%d)", len(sa.bucketTimes)))
	} else {
		out = append(out, fmt.Sprintf("DIVERGES bucketTimes: %d vs %d", len(sa.bucketTimes), len(sb.bucketTimes)))
	}
	if i32SlicesEq(sa.bucketUnits, sb.bucketUnits) {
		out = append(out, fmt.Sprintf("identical bucketUnits (%d)", len(sa.bucketUnits)))
	} else {
		out = append(out, fmt.Sprintf("DIVERGES bucketUnits: %d vs %d", len(sa.bucketUnits), len(sb.bucketUnits)))
	}

	type wrow struct {
		df, bits int
		w        uint64
	}
	wa := make([]wrow, 0, len(ia.DF))
	wb := make([]wrow, 0, len(ib.DF))
	for h := range ia.DF {
		w, _ := sa.effWeightOf(h)
		wa = append(wa, wrow{ia.TermDF(h), int(ia.TermBitsOf(h)), math.Float64bits(w)})
	}
	for h := range ib.DF {
		w, _ := sb.effWeightOf(h)
		wb = append(wb, wrow{ib.TermDF(h), int(ib.TermBitsOf(h)), math.Float64bits(w)})
	}
	sort.Slice(wa, func(i, j int) bool {
		if wa[i].df != wa[j].df {
			return wa[i].df < wa[j].df
		}
		if wa[i].bits != wa[j].bits {
			return wa[i].bits < wa[j].bits
		}
		return wa[i].w < wa[j].w
	})
	sort.Slice(wb, func(i, j int) bool {
		if wb[i].df != wb[j].df {
			return wb[i].df < wb[j].df
		}
		if wb[i].bits != wb[j].bits {
			return wb[i].bits < wb[j].bits
		}
		return wb[i].w < wb[j].w
	})
	wDiv := -1
	if len(wa) != len(wb) {
		wDiv = -2
	} else {
		for i := range wa {
			if wa[i] != wb[i] {
				wDiv = i
				break
			}
		}
	}
	if wDiv == -1 {
		out = append(out, fmt.Sprintf("identical effWeight multiset (%d terms)", len(wa)))
	} else if wDiv == -2 {
		out = append(out, fmt.Sprintf("DIVERGES effWeight multiset size: %d vs %d", len(wa), len(wb)))
	} else {
		out = append(out, fmt.Sprintf("DIVERGES effWeight multiset at %d: (df=%d,bits=%d,w=%016x) vs (df=%d,bits=%d,w=%016x)",
			wDiv, wa[wDiv].df, wa[wDiv].bits, wa[wDiv].w, wb[wDiv].df, wb[wDiv].bits, wb[wDiv].w))
	}
	return out
}

func stringSlicesEq(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func i32SlicesEq(a, b []int32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// DebugLastQueryStructure 返回引擎最近一次查询的 key 无关结构读数（临时诊断，
// key-invariance 收口后随本文件删除）：查询词项分组分区、槽位主词属主、
// detached 碎片集、组质量表。哈希只转成 (df,bits) 签名。
func DebugLastQueryStructure(e *Engine) []string {
	out := []string{}
	if e == nil || e.queryScratch == nil || e.Index == nil {
		return []string{"no query scratch"}
	}
	sc := e.queryScratch
	idx := e.Index
	rootSig := func(h uint64) string {
		df := idx.TermDF(h)
		bits := 0
		if t, ok := sc.qTerms[h]; ok {
			bits = int(t.Bits)
		}
		return fmt.Sprintf("%d/%d", df, bits)
	}
	// 分组分区：成员签名多重集按根签名聚合。
	type gsig struct{ root string }
	groups := map[string][]string{}
	for _, h := range sc.queryHashes {
		g := sc.hashGroup[h]
		member := fmt.Sprintf("%d:%d:%d", idx.TermDF(h), func() int {
			if t, ok := sc.qTerms[h]; ok {
				return int(t.Bits)
			}
			return 0
		}(), func() int {
			if t, ok := sc.qTerms[h]; ok {
				return int(t.Flags)
			}
			return 0
		}())
		if g == 0 {
			groups["self:"+member] = append(groups["self:"+member], member)
		} else {
			groups[rootSig(g)] = append(groups[rootSig(g)], member)
		}
	}
	gkeys := make([]string, 0, len(groups))
	for k := range groups {
		gkeys = append(gkeys, k)
	}
	sort.Strings(gkeys)
	var pb strings.Builder
	for _, k := range gkeys {
		ms := groups[k]
		sort.Strings(ms)
		fmt.Fprintf(&pb, "|%s:[%s]", k, strings.Join(ms, ","))
	}
	out = append(out, "qgroups "+pb.String())
	// 槽位属主与质量表。
	var sb strings.Builder
	posKeys := make([]TermPosition, 0, len(sc.slotPrim))
	for p := range sc.slotPrim {
		posKeys = append(posKeys, p)
	}
	sort.Slice(posKeys, func(i, j int) bool {
		if posKeys[i].Sentence != posKeys[j].Sentence {
			return posKeys[i].Sentence < posKeys[j].Sentence
		}
		return posKeys[i].Slot < posKeys[j].Slot
	})
	for _, p := range posKeys {
		// 并列属主按逻辑名排序后串联，使摘要与哈希序无关。
		sigs := make([]string, 0, len(sc.slotPrim[p]))
		for _, owner := range sc.slotPrim[p] {
			sigs = append(sigs, rootSig(owner))
		}
		sort.Strings(sigs)
		fmt.Fprintf(&sb, "|%d.%d=%s", p.Sentence, p.Slot, strings.Join(sigs, ","))
	}
	out = append(out, "slotprim "+sb.String())
	var gb strings.Builder
	gmass := make([]string, 0, len(sc.groups))
	for g, m := range sc.groups {
		gmass = append(gmass, fmt.Sprintf("%s=%.6f", rootSig(g), m))
	}
	sort.Strings(gmass)
	for _, s := range gmass {
		gb.WriteString("|" + s)
	}
	out = append(out, "groupmass "+gb.String())
	return out
}
