// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// 本件是簇状态 append-only 快路径的充分统计量与增量维护：把 buildEffectiveWeightsFromDyads
// 内每词族的聚合（df/multiDocCount/簇多重集/伙伴多重集）压缩持久化为「升序键 + 整数计数」，
// 既作为全量构建的中间产物（与内联计算逐位等价），也作为后续纯追加重建的更新基座；
// 同时提供短语倒排的旧表复用合并（旧 (pair,unit) 全序 + 新文档 postings 归并）。
package core

import (
	"math"
	"slices"
	"sort"
)

// incrementalDeltaProbe 仅由测量器材安装；nil 时生产路径只多一次分支。
var incrementalDeltaProbe func(hit bool)

// termPartnerStats 选择伙伴统计的键语义：false = 族根（默认），
// true = 宿主词项 + 伙伴词项（测量期用于评估「族根移动不再重键」）。
var termPartnerStats bool

// SetTermPartnerStats 切换伙伴统计键语义；测量期由评测器材设置，生产默认 false。
func SetTermPartnerStats(v bool) { termPartnerStats = v }

// SetIncrementalDeltaProbe 安装增量簇扩展命中探针（nil 卸载）。
func SetIncrementalDeltaProbe(fn func(hit bool)) {
	incrementalDeltaProbe = fn
}

// clusterFamilyInfo 是全量构建期每词族的累加器（原 buildEffectiveWeightsFromDyads 内部类型外提）。
type clusterFamilyInfo struct {
	lastUnit      int32
	unitCount     int
	multiDocCount int
	clusters      []int32
	partners      []uint64
	weight        float64
}

// clusterFamilyAgg 是单族压缩统计：升序键 + 出现计数。簇键为时间桶时刻，
// 0 表示无时间簇（按出现次数计独立簇）；伙伴键为族根键。
type clusterFamilyAgg struct {
	df            int32
	multiDocCount int32
	weight        float64
	clusterKey    []int64
	clusterCnt    []int32
	partnerKey    []uint64
	partnerCnt    []int32
}

// clusterAggregates 是词族有效权重的增量充分统计量（仅内存态；.zcsr 水合状态不带）。
// clone 复制族聚合结构体并**共享**簇键/伙伴计数切片：所有更新路径（mergeInt64Counts /
// mergeUint64Counts / compress*）都生成新切片而从不原地写元素，因此共享是只读安全的；
// 代发布克隆因此不必复制每个族的计数数组。唯一被修改的标量字段（df/multiDocCount/
// weight）随结构体副本隔离。
func (f *clusterFamilyAgg) clone() *clusterFamilyAgg {
	if f == nil {
		return nil
	}
	c := *f
	return &c
}

// Clone 深拷贝整份族聚合。
func (a *clusterAggregates) Clone() *clusterAggregates {
	if a == nil {
		return nil
	}
	c := &clusterAggregates{
		unitIDs:        append([]string(nil), a.unitIDs...),
		fams:           make(map[uint64]*clusterFamilyAgg, len(a.fams)),
		clusterN:       a.clusterN,
		termParentKeys: append([]uint64(nil), a.termParentKeys...),
		termParentVals: append([]uint64(nil), a.termParentVals...),
		extended:       a.extended,
	}
	if a.termPartners != nil {
		c.termPartners = make(map[uint64][]uint64, len(a.termPartners))
		c.termPartnerCnt = make(map[uint64][]int32, len(a.termPartners))
		for k, v := range a.termPartners {
			c.termPartners[k] = v
			c.termPartnerCnt[k] = a.termPartnerCnt[k]
		}
	}
	for k, v := range a.fams {
		c.fams[k] = v.clone()
	}
	return c
}

// CloneForPublish 浅拷贝簇状态（共享只读 CSR/邻域/日余弦）并深拷贝族聚合，
// 使发布克隆可以走增量扩展而不再整建簇状态。
func (st *rmClusterState) CloneForPublish() *rmClusterState {
	if st == nil {
		return nil
	}
	c := *st
	c.agg = st.agg.Clone()
	return &c
}

type clusterAggregates struct {
	unitIDs  []string // 统计所属索引的单元顺序（append 重映射校验）
	fams     map[uint64]*clusterFamilyAgg
	clusterN int32
	// 族根映射快照（升序词项键 + 族根键）：追加时若任一旧词项的族根变化，
	// 统计量失效，必须回退全量重建。
	termParentKeys []uint64
	termParentVals []uint64
	// termPartners/termPartnerCnt 在词项模式下保存「宿主词项 → 伙伴词项 + 计数」：
	// 族根移动不改变这些键，族权重在计算时按成员词项汇总到族根。
	termPartners   map[uint64][]uint64
	termPartnerCnt map[uint64][]int32
	// extended 标记这份统计量由 append-only 快路径扩展而来（测试观测点）。
	extended bool
}

func familyEffectiveWeight(df, multiDocCount, clusters int, total, entropy float64, clusterN int) float64 {
	effDF := 1.0
	if df > 1 {
		if clusters < 1 {
			clusters = 1
		}
		if clusters > df {
			clusters = df
		}
		d := float64(df - 1)
		indep := math.Pow(float64(clusters-1)/d, 1/eulerNumber)
		disp := 1.0
		if total > 0 || multiDocCount > 0 {
			disp = familyDispersion(df, entropy)
		}
		effDF = 1 + d*indep*disp
	}
	return math.Log1p((float64(clusterN) + 1) / (effDF + 1))
}

// familyDispersion is the partner-entropy share H/ln(df) of one family, referenced
// to its document count: a family whose df documents each name a different partner
// has H = ln(df) and reaches 1, the independent-event limit. Partner counts beyond
// df saturate at 1, since no family can carry more effective events than documents.
// A family with fewer than two documents carries no dispersion.
func familyDispersion(df int, entropy float64) float64 {
	if df < 2 {
		return 0
	}
	return math.Min(1, math.Max(0, entropy/math.Log(float64(df))))
}

// familyEffectiveWeightFromAgg 由压缩统计重算族权重（熵按升序键 + 计数求和，
// 与全量构建的升序游程逐位一致）。
func familyEffectiveWeightFromAgg(f *clusterFamilyAgg, clusterN int) float64 {
	clusters := familyClusterCount(f.clusterKey, f.clusterCnt)
	total, entropy := familyEntropy(f.partnerKey, f.partnerCnt)
	return familyEffectiveWeight(int(f.df), int(f.multiDocCount), clusters, total, entropy, clusterN)
}

func familyEntropy(keys []uint64, counts []int32) (total, entropy float64) {
	for _, c := range counts {
		total += float64(c)
	}
	if total <= 0 {
		return 0, 0
	}
	// 熵按计数升序累加：同计数的伙伴根给出逐位相同的被加项（p·ln p 只依赖
	// count/total），其内部顺序对浮和无影响；计数序与 atomizer key 无关。
	// 早期实现按 hash 键序累加——同一多重集在不同 key 下排成不同顺序，使熵值
	// 差 ULP（key-invariance 缺陷）。
	order := make([]int32, len(counts))
	for i := range order {
		order[i] = int32(i)
	}
	slices.SortFunc(order, func(x, y int32) int {
		if counts[x] != counts[y] {
			if counts[x] < counts[y] {
				return -1
			}
			return 1
		}
		if keys[x] < keys[y] {
			return -1
		}
		if keys[x] > keys[y] {
			return 1
		}
		return 0
	})
	for _, i := range order {
		p := float64(counts[i]) / total
		entropy -= p * math.Log(p)
	}
	return total, entropy
}

// familyClusterCount 统计族内唯一簇数：有时间键按唯一时刻计，无时间键（0）
// 按出现次数计（每个无时间文档各自独立成簇）。
func familyClusterCount(keys []int64, counts []int32) int {
	n := 0
	for i, k := range keys {
		if k == 0 {
			n += int(counts[i])
		} else {
			n++
		}
	}
	return n
}

// clusterKeyOf 把簇序号映射为稳定键：时间桶序号 → 桶时刻；无时间簇 → 0。
func clusterKeyOf(cl int32, bucketTimes []int64) int64 {
	if int(cl) >= 0 && int(cl) < len(bucketTimes) {
		return bucketTimes[cl]
	}
	return 0
}

// compressInt64Keys 把升序键压缩为键 + 计数。
func compressInt64Keys(src []int64, keys []int64, counts []int32) ([]int64, []int32) {
	for i := 0; i < len(src); {
		j := i + 1
		for j < len(src) && src[j] == src[i] {
			j++
		}
		keys = append(keys, src[i])
		counts = append(counts, int32(j-i))
		i = j
	}
	return keys, counts
}

func compressUint64Keys(src []uint64, keys []uint64, counts []int32) ([]uint64, []int32) {
	for i := 0; i < len(src); {
		j := i + 1
		for j < len(src) && src[j] == src[i] {
			j++
		}
		keys = append(keys, src[i])
		counts = append(counts, int32(j-i))
		i = j
	}
	return keys, counts
}

// mergeInt64Counts 把增量（已压缩、升序）并入旧键计数表。
// mergeInt64Counts 把增量计数（可含负值，已按升序键压缩）并入旧键计数表；
// 合并后计数 ≤0 的键被丢弃（扣减归零等价于从未出现）。
func mergeInt64Counts(oldKeys []int64, oldCnt []int32, addKeys []int64, addCnt []int32) ([]int64, []int32) {
	if len(addKeys) == 0 {
		return oldKeys, oldCnt
	}
	keys := make([]int64, 0, len(oldKeys)+len(addKeys))
	counts := make([]int32, 0, len(oldKeys)+len(addKeys))
	i, j := 0, 0
	for i < len(oldKeys) && j < len(addKeys) {
		var k int64
		var c int32
		switch {
		case oldKeys[i] < addKeys[j]:
			k, c = oldKeys[i], oldCnt[i]
			i++
		case oldKeys[i] > addKeys[j]:
			k, c = addKeys[j], addCnt[j]
			j++
		default:
			k, c = oldKeys[i], oldCnt[i]+addCnt[j]
			i++
			j++
		}
		if c > 0 {
			keys = append(keys, k)
			counts = append(counts, c)
		}
	}
	for ; i < len(oldKeys); i++ {
		if oldCnt[i] > 0 {
			keys = append(keys, oldKeys[i])
			counts = append(counts, oldCnt[i])
		}
	}
	for ; j < len(addKeys); j++ {
		if addCnt[j] > 0 {
			keys = append(keys, addKeys[j])
			counts = append(counts, addCnt[j])
		}
	}
	return keys, counts
}

// mergeUint64Counts 把增量计数（可含负值，已按升序键压缩）并入旧键计数表；
// 合并后计数 ≤0 的键被丢弃。
func mergeUint64Counts(oldKeys []uint64, oldCnt []int32, addKeys []uint64, addCnt []int32) ([]uint64, []int32) {
	if len(addKeys) == 0 {
		return oldKeys, oldCnt
	}
	keys := make([]uint64, 0, len(oldKeys)+len(addKeys))
	counts := make([]int32, 0, len(oldKeys)+len(addKeys))
	i, j := 0, 0
	for i < len(oldKeys) && j < len(addKeys) {
		var k uint64
		var c int32
		switch {
		case oldKeys[i] < addKeys[j]:
			k, c = oldKeys[i], oldCnt[i]
			i++
		case oldKeys[i] > addKeys[j]:
			k, c = addKeys[j], addCnt[j]
			j++
		default:
			k, c = oldKeys[i], oldCnt[i]+addCnt[j]
			i++
			j++
		}
		if c > 0 {
			keys = append(keys, k)
			counts = append(counts, c)
		}
	}
	for ; i < len(oldKeys); i++ {
		if oldCnt[i] > 0 {
			keys = append(keys, oldKeys[i])
			counts = append(counts, oldCnt[i])
		}
	}
	for ; j < len(addKeys); j++ {
		if addCnt[j] > 0 {
			keys = append(keys, addKeys[j])
			counts = append(counts, addCnt[j])
		}
	}
	return keys, counts
}

// buildClusterAggregates 从全量构建期的族累加器生成压缩统计量（调用前 clusters/partners 已排序）。
func buildClusterAggregates(idx *Index, families map[uint64]*clusterFamilyInfo, st *rmClusterState, termPartners map[uint64][]uint64) *clusterAggregates {
	agg := &clusterAggregates{
		unitIDs:  append([]string(nil), idx.UnitIDs...),
		fams:     make(map[uint64]*clusterFamilyAgg, len(families)),
		clusterN: int32(st.clusterN),
	}
	for k, f := range families {
		fa := &clusterFamilyAgg{df: int32(f.unitCount), multiDocCount: int32(f.multiDocCount)}
		keys64 := make([]int64, 0, len(f.clusters))
		for _, cl := range f.clusters {
			keys64 = append(keys64, clusterKeyOf(cl, st.bucketTimes))
		}
		sort.Slice(keys64, func(i, j int) bool { return keys64[i] < keys64[j] })
		fa.clusterKey, fa.clusterCnt = compressInt64Keys(keys64, nil, nil)
		fa.partnerKey, fa.partnerCnt = compressUint64Keys(f.partners, nil, nil)
		if termPartnerStats {
			// 权重在整表构建后由 refreshTermKeyedWeights 统一计算。
			fa.weight = 0
		} else {
			fa.weight = familyEffectiveWeightFromAgg(fa, st.clusterN)
		}
		agg.fams[k] = fa
	}
	if termPartnerStats && termPartners != nil {
		agg.termPartners = make(map[uint64][]uint64, len(termPartners))
		agg.termPartnerCnt = make(map[uint64][]int32, len(termPartners))
		for host, partners := range termPartners {
			if len(partners) == 0 {
				continue
			}
			sortUint64Asc(partners)
			keys, cnts := compressUint64Keys(partners, nil, nil)
			agg.termPartners[host] = keys
			agg.termPartnerCnt[host] = cnts
		}
	}
	agg.termParentKeys = make([]uint64, len(st.termKeys))
	copy(agg.termParentKeys, st.termKeys)
	agg.termParentVals = make([]uint64, len(st.termKeys))
	for i, h := range st.termKeys {
		agg.termParentVals[i] = st.canonicalParentOf(h)
	}
	return agg
}

// fillEffWeightFromAggregates 用族统计量填充 st.effWeight：每词项取族根统计的权重
// （未在统计中的根沿用全量路径的零权重），再按同族覆盖取最小值。
func (st *rmClusterState) fillEffWeightFromAggregates(idx *Index, agg *clusterAggregates) {
	// Do not seed from parentGroup: that map stores the smallest hash label and
	// therefore changes when the atomizer key changes. The effective weight is
	// the minimum over the families the term actually occupies in documents;
	// that set is key-invariant, so this value is key-invariant as well.
	for h := range idx.DF {
		st.effWeight[h] = 0
	}
	for u := 0; u < idx.N; u++ {
		for _, entry := range st.docEntriesOf(int32(u)) {
			g := entry.group
			if g == 0 {
				g = entry.id
			}
			if fa, ok := agg.fams[g]; ok {
				if st.effWeight[entry.id] == 0 || fa.weight < st.effWeight[entry.id] {
					st.effWeight[entry.id] = fa.weight
				}
			}
		}
	}
}

// incrementalDelta 是通过校验的增量重建描述：旧单元 → 新单元重映射（-1 = 已移除）、
// 新单元标记（delta：新增与覆盖）、待扣减旧单元（dropUnits）与旧状态引用。
// rekeyUnits 是幸存单元中含族根已变化词项的单元（新索引号，升序）：其成员与句内 dyad
// 不变，只需按旧根扣减、按新根并入伙伴对；oldOf 把幸存单元的新索引号映回旧索引号。
type incrementalDelta struct {
	prev       *rmClusterState
	remap      []int32
	delta      []bool
	deltaUnits []int32
	dropUnits  []int32
	rekeyUnits []int32
	oldOf      []int32
}

// canonicalHostPreferred reports whether candidate is a better family root than
// current using only key-independent text statistics: the most frequent host
// (larger df) wins, then the wider carrier (larger bits). The atomizer key only
// relabels hashes, so this choice does not change when the key changes.
func canonicalHostPreferred(idx *Index, candidate, current uint64) bool {
	dc, dr := idx.TermDF(candidate), idx.TermDF(current)
	if dc != dr {
		return dc > dr
	}
	bc, br := idx.TermBitsOf(candidate), idx.TermBitsOf(current)
	if bc != br {
		return bc > br
	}
	return false
}

// scanParentGroups 按构建期同式（每文档取 UnitGroups 覆盖值）预扫描族根映射。
// 跨文档不再取最小哈希，而是取 canonicalHostPreferred 的稳定宿主。
func scanParentGroups(idx *Index) map[uint64]uint64 {
	pg := make(map[uint64]uint64, len(idx.DF))
	for u := 0; u < idx.N; u++ {
		var ug map[uint64]uint64
		if u < len(idx.UnitGroups) {
			ug = idx.UnitGroups[u]
		}
		for _, h := range idx.DocTerms[u] {
			g := h
			if ug != nil {
				if v := ug[h]; v != 0 {
					g = v
				}
			}
			if prev, ok := pg[h]; !ok || canonicalHostPreferred(idx, g, prev) {
				pg[h] = g
			}
		}
	}
	return pg
}

// takeIncrementalDelta 校验增量门槛：旧状态带统计量与短语倒排、幸存旧单元顺序保持。
// 被删除/覆盖的旧单元记入 dropUnits；族根变化不再回退整建，而是把含移动词项的幸存单元
// 记入 rekeyUnits 按受影响单元重键。校验失败返回 nil（回退全量重建）。
func (e *Engine) takeIncrementalDelta(idx *Index) *incrementalDelta {
	inc := e.takeIncrementalDeltaInner(idx)
	if incrementalDeltaProbe != nil {
		incrementalDeltaProbe(inc != nil)
	}
	return inc
}

func (e *Engine) takeIncrementalDeltaInner(idx *Index) *incrementalDelta {
	prev := e.prevClusterState
	drops := e.prevDrops
	e.prevClusterState = nil
	e.prevDrops = nil
	if prev == nil || prev.agg == nil || idx == nil || prev.builtIndex == nil {
		return nil
	}
	if len(prev.phraseKeys) != len(prev.phraseRowPtr)-1 || len(prev.phraseUnits) != int(prev.phraseRowPtr[len(prev.phraseRowPtr)-1]) {
		return nil
	}
	oldIDs := prev.agg.unitIDs
	remap := make([]int32, len(oldIDs))
	oldSet := make(map[string]struct{}, len(oldIDs))
	dropUnits := make([]int32, 0, len(drops))
	for i, id := range oldIDs {
		oldSet[id] = struct{}{}
		if _, gone := drops[id]; gone {
			remap[i] = -1
			dropUnits = append(dropUnits, int32(i))
			continue
		}
		u, ok := idx.LookupUnit(id)
		if !ok {
			remap[i] = -1
			dropUnits = append(dropUnits, int32(i))
			continue
		}
		remap[i] = u
	}
	last := int32(-1)
	for _, u := range remap {
		if u < 0 {
			continue
		}
		if u <= last {
			return nil
		}
		last = u
	}
	delta := make([]bool, idx.N)
	deltaUnits := make([]int32, 0, idx.N)
	for u := 0; u < idx.N; u++ {
		id := idx.UnitIDs[u]
		_, wasOld := oldSet[id]
		_, dropped := drops[id]
		if !wasOld || dropped {
			delta[u] = true
			deltaUnits = append(deltaUnits, int32(u))
		}
	}
	if len(deltaUnits) == 0 && len(dropUnits) == 0 {
		return nil
	}
	now := scanParentGroups(idx)
	var moved []uint64
	for i, h := range prev.agg.termParentKeys {
		nowVal, still := now[h]
		if !still {
			// 词项随被删单元整体消失：其族贡献由扣减逻辑按旧族根移除。
			continue
		}
		if nowVal != prev.agg.termParentVals[i] {
			moved = append(moved, h)
		}
	}
	oldOf := make([]int32, idx.N)
	for u := range oldOf {
		oldOf[u] = -1
	}
	for i, u := range remap {
		if u >= 0 {
			oldOf[u] = int32(i)
		}
	}
	return &incrementalDelta{
		prev:       prev,
		remap:      remap,
		delta:      delta,
		deltaUnits: deltaUnits,
		dropUnits:  dropUnits,
		rekeyUnits: rekeyUnitsOf(idx, moved, delta),
		oldOf:      oldOf,
	}
}

// rekeyUnitsOf 收集幸存单元中含族根已变化词项的单元（新索引号，升序）。
// 族根只经伙伴对 root(a)/root(b) 进入族统计：单元的成员与句内 dyad 不因族根变化而变，
// 故只有含移动词项的幸存单元需要按旧根扣减、按新根并入伙伴对。
func rekeyUnitsOf(idx *Index, moved []uint64, delta []bool) []int32 {
	if len(moved) == 0 {
		return nil
	}
	mark := make([]bool, idx.N)
	var units []int32
	for _, h := range moved {
		for _, p := range idx.Postings[h] {
			u := p.Unit
			if delta[u] || mark[u] {
				continue
			}
			mark[u] = true
			units = append(units, u)
		}
	}
	sortInt32Asc(units)
	return units
}

// rootLookup 按旧快照建表并返回族根查询闭包（快照未记录或根为 0 时沿用恒等根，与
// rmClusterState 内 root 闭包语义一致）。须在快照刷新前调用。
func (a *clusterAggregates) rootLookup() func(uint64) uint64 {
	roots := make(map[uint64]uint64, len(a.termParentKeys))
	for i, h := range a.termParentKeys {
		roots[h] = a.termParentVals[i]
	}
	return func(id uint64) uint64 {
		if g := roots[id]; g != 0 {
			return g
		}
		return id
	}
}

// docDyadsInto 按本状态的位置表抽取单篇文档的句内 dyad；宽坐标语料走精确分支，
// 与全量构建同式。
func (st *rmClusterState) docDyadsInto(entries []rmDocEntry, occs []rmPackedOccurrence, out []rmDyad) ([]rmDyad, []rmPackedOccurrence) {
	if st.hasWidePos() {
		return st.extractDocDyadsExactInto(entries, out), occs
	}
	return st.extractDocDyadsInto(entries, occs, out)
}

// mergePhraseCSR 复用旧短语倒排（(pair, unit) 全序）与 delta postings 归并：
// 旧单元按单调 remap 重映射后仍升序，与已排序的 delta postings 做双路归并。
func mergePhraseCSR(prev *rmClusterState, remap []int32, delta []dyadPosting) ([]rmDyad, []uint32, []int32) {
	keys := make([]rmDyad, 0, len(prev.phraseKeys)+len(delta))
	units := make([]int32, 0, len(prev.phraseUnits)+len(delta))
	rowPtr := make([]uint32, 0, len(prev.phraseKeys)+1)
	emit := func(p rmDyad, u int32) {
		if len(keys) == 0 || keys[len(keys)-1] != p {
			keys = append(keys, p)
			rowPtr = append(rowPtr, uint32(len(units)))
		}
		units = append(units, u)
	}
	di := 0
	for oi := 0; oi < len(prev.phraseKeys); oi++ {
		op := prev.phraseKeys[oi]
		oUnits := prev.phraseUnits[prev.phraseRowPtr[oi]:prev.phraseRowPtr[oi+1]]
		for di < len(delta) && dyadLess(delta[di].pair, op) {
			emit(delta[di].pair, delta[di].unit)
			di++
		}
		ui := 0
		for ui < len(oUnits) && di < len(delta) && delta[di].pair == op {
			nu := remap[oUnits[ui]]
			if nu < 0 {
				ui++
				continue
			}
			du := delta[di].unit
			switch {
			case nu < du:
				emit(op, nu)
				ui++
			case du < nu:
				emit(op, du)
				di++
			default:
				emit(op, nu)
				ui++
				di++
			}
		}
		for ; ui < len(oUnits); ui++ {
			if nu := remap[oUnits[ui]]; nu >= 0 {
				emit(op, nu)
			}
		}
		for di < len(delta) && delta[di].pair == op {
			emit(op, delta[di].unit)
			di++
		}
	}
	for ; di < len(delta); di++ {
		emit(delta[di].pair, delta[di].unit)
	}
	rowPtr = append(rowPtr, uint32(len(units)))
	return keys, rowPtr, units
}

// dyadLess 比较 (a,b) 字典序（与 dyadPostingSort 的 pair 顺序一致）。
// appendOnlyIncremental reports whether every old unit keeps its index, so the immutable
// base phrase CSR can be shared and only the delta block needs rebuilding.
func appendOnlyIncremental(inc *incrementalDelta) bool {
	if inc == nil || len(inc.dropUnits) != 0 {
		return false
	}
	for i, r := range inc.remap {
		if r != int32(i) {
			return false
		}
	}
	return true
}

// mergeDyadPostings merges two (pair, unit)-sorted posting streams.
func mergeDyadPostings(a, b []dyadPosting) []dyadPosting {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}
	out := make([]dyadPosting, 0, len(a)+len(b))
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		x, y := a[i], b[j]
		if dyadLess(y.pair, x.pair) || (y.pair == x.pair && y.unit < x.unit) {
			out = append(out, y)
			j++
		} else {
			out = append(out, x)
			i++
		}
	}
	out = append(out, a[i:]...)
	out = append(out, b[j:]...)
	return out
}

// remapDeltaPostings applies the unit remap to a previous delta block and drops rows whose
// unit disappeared; the remap is monotonic on the incremental path, so the result stays
// sorted, and the defensive sort keeps the fallback correct if that ever changes.
func remapDeltaPostings(prevDelta []dyadPosting, remap []int32) []dyadPosting {
	if len(prevDelta) == 0 {
		return nil
	}
	out := make([]dyadPosting, 0, len(prevDelta))
	for _, p := range prevDelta {
		if int(p.unit) >= len(remap) {
			continue
		}
		nu := remap[p.unit]
		if nu < 0 {
			continue
		}
		p.unit = nu
		out = append(out, p)
	}
	slices.SortFunc(out, dyadPostingCompare)
	return out
}

// mergePhraseCSRWithDelta folds a previous state's delta block into the incoming delta
// postings before the global CSR merge, so fallback paths never lose appended rows.
func mergePhraseCSRWithDelta(prev *rmClusterState, remap []int32, delta []dyadPosting) ([]rmDyad, []uint32, []int32) {
	if prev != nil && len(prev.phraseDelta) > 0 {
		delta = mergeDyadPostings(remapDeltaPostings(prev.phraseDelta, remap), delta)
	}
	return mergePhraseCSR(prev, remap, delta)
}

// phraseDeltaOverBudget reports whether the append delta has grown past 1/10 of the
// immutable base CSR bytes (24 B per delta posting vs 16 B/key + 4 B/unit).
func phraseDeltaOverBudget(deltaLen int, prev *rmClusterState) bool {
	if prev == nil {
		return true
	}
	baseBytes := len(prev.phraseKeys)*16 + len(prev.phraseUnits)*4
	return deltaLen*24*10 > baseBytes
}

// compactPhraseDelta folds the append delta back into the base CSR. The v7 disk format
// stores a single phrase CSR, so publishers call this before serialization.
func (st *rmClusterState) compactPhraseDelta() {
	if len(st.phraseDelta) == 0 {
		return
	}
	remap := make([]int32, len(st.phraseKeys))
	for i := range remap {
		remap[i] = int32(i)
	}
	st.phraseKeys, st.phraseRowPtr, st.phraseUnits = mergePhraseCSR(st, remap, st.phraseDelta)
	st.phraseDelta = nil
}

// dyadPostingCompare orders by pair then unit.
func dyadPostingCompare(a, b dyadPosting) int {
	if c := rmDyadCompare(a.pair, b.pair); c != 0 {
		return c
	}
	if a.unit < b.unit {
		return -1
	}
	if a.unit > b.unit {
		return 1
	}
	return 0
}

// rmDyadCompare orders (a, b) lexicographically for slices.SortFunc.
func rmDyadCompare(x, y rmDyad) int {
	if x.a != y.a {
		if x.a < y.a {
			return -1
		}
		return 1
	}
	if x.b != y.b {
		if x.b < y.b {
			return -1
		}
		return 1
	}
	return 0
}

func dyadLess(x, y rmDyad) bool {
	if x.a != y.a {
		return x.a < y.a
	}
	return x.b < y.b
}

// docFamilySet 按构建期同式取单篇文档的首见族集合（文档内去重，保序）。
func docFamilySet(entries []rmDocEntry, root func(uint64) uint64, seen map[uint64]struct{}, dst []uint64) ([]uint64, map[uint64]struct{}) {
	dst = dst[:0]
	if seen == nil {
		seen = make(map[uint64]struct{})
	} else {
		for k := range seen {
			delete(seen, k)
		}
	}
	for _, entry := range entries {
		g := entry.group
		if g == 0 {
			g = root(entry.id)
		}
		if _, dup := seen[g]; dup {
			continue
		}
		seen[g] = struct{}{}
		dst = append(dst, g)
	}
	return dst, seen
}

// rootPairSet 把文档 dyad 归约为去重、升序的族根对（剔除自配对，统一 a<b），追加进 dst。
// 同一文档内的重复根对只计一次，与全量构建的伙伴去重同式。
func rootPairSet(dyads []rmDyad, root func(uint64) uint64, dst []rmDyad, scratch []rmDyad) ([]rmDyad, []rmDyad) {
	scratch = scratch[:0]
	for _, pair := range dyads {
		a, b := root(pair.a), root(pair.b)
		if a == b {
			continue
		}
		if a > b {
			a, b = b, a
		}
		scratch = append(scratch, rmDyad{a: a, b: b})
	}
	if len(scratch) > 1 {
		slices.SortFunc(scratch, rmDyadCompare)
	}
	for i, pair := range scratch {
		if i > 0 && pair == scratch[i-1] {
			continue
		}
		dst = append(dst, pair)
	}
	return dst, scratch
}

// appendPairEntries 把无向族根对展开为双向 (族, 伙伴) 条目。
func appendPairEntries(dst []rmDyad, p rmDyad) []rmDyad {
	return append(dst, rmDyad{a: p.a, b: p.b}, rmDyad{a: p.b, b: p.a})
}

// appendPairDiffEntries 比较同一文档在旧/新族根下的去重族根对（均升序）：仅旧有的对进入扣减，
// 仅新有的对进入并入；两侧共有的对相互抵消，不产生条目。
func appendPairDiffEntries(sub, add []rmDyad, oldPairs, newPairs []rmDyad) ([]rmDyad, []rmDyad) {
	i, j := 0, 0
	for i < len(oldPairs) || j < len(newPairs) {
		switch {
		case j == len(newPairs) || (i < len(oldPairs) && dyadLess(oldPairs[i], newPairs[j])):
			sub = appendPairEntries(sub, oldPairs[i])
			i++
		case i == len(oldPairs) || dyadLess(newPairs[j], oldPairs[i]):
			add = appendPairEntries(add, newPairs[j])
			j++
		default:
			i++
			j++
		}
	}
	return sub, add
}

// mergePartnerEntries 把展平的 (族, 伙伴) 条目按族与伙伴排序、压成游程计数，以 sign 并入各族的
// 伙伴计数。create 为真时缺失的族先行创建（并入路径）；否则跳过（扣减路径中族已不存在等价于零计数）。
func (agg *clusterAggregates) mergePartnerEntries(entries []rmDyad, sign int32, create bool) {
	if len(entries) == 0 {
		return
	}
	slices.SortFunc(entries, rmDyadCompare)
	var keys []uint64
	var cnts []int32
	for i := 0; i < len(entries); {
		fam := entries[i].a
		keys, cnts = keys[:0], cnts[:0]
		for i < len(entries) && entries[i].a == fam {
			j := i + 1
			for j < len(entries) && entries[j] == entries[i] {
				j++
			}
			keys = append(keys, entries[i].b)
			cnts = append(cnts, sign*int32(j-i))
			i = j
		}
		fa := agg.fams[fam]
		if fa == nil {
			if !create {
				continue
			}
			fa = &clusterFamilyAgg{}
			agg.fams[fam] = fa
		}
		fa.partnerKey, fa.partnerCnt = mergeUint64Counts(fa.partnerKey, fa.partnerCnt, keys, cnts)
	}
}

// extendForDelta 从旧统计量中扣减被删除/覆盖单元的贡献，再并入 delta 单元的贡献，
// 族根变化的幸存单元按旧根扣减、新根并入伙伴对；并只重算受影响族的权重（clusterN 变化时整体重算）。
// 扣减一律用旧快照的族根：词项随被删单元整体消失时，新状态已无该词项的族根记录。
func (agg *clusterAggregates) extendForDelta(st *rmClusterState, idx *Index, inc *incrementalDelta, docDyadRowPtr []uint32, docDyadsFlat []rmDyad) {
	if termPartnerStats {
		agg.extendForDeltaTermKeyed(st, idx, inc, docDyadRowPtr, docDyadsFlat)
		return
	}
	addClusters := make(map[uint64][]int64)
	subClusters := make(map[uint64][]int64)
	var addPartners, subPartners []rmDyad
	getFam := func(id uint64) *clusterFamilyAgg {
		fa := agg.fams[id]
		if fa == nil {
			fa = &clusterFamilyAgg{}
			agg.fams[id] = fa
		}
		return fa
	}
	root := func(id uint64) uint64 {
		return st.canonicalParentOf(id)
	}
	var rootOld func(uint64) uint64
	if len(inc.dropUnits) > 0 || len(inc.rekeyUnits) > 0 {
		rootOld = agg.rootLookup()
	}
	var docFams []uint64
	var seen map[uint64]struct{}
	var pairSet, oldPairs, newPairs, pairScratch []rmDyad
	var occScratch []rmPackedOccurrence
	var dyadScratch []rmDyad

	// 1) 扣减：被删除/覆盖的旧单元按旧状态与旧族根重算贡献，再做负计数合并。
	for _, ou := range inc.dropUnits {
		if int(ou) >= len(inc.prev.clusters) {
			continue
		}
		entries := inc.prev.docEntriesOf(ou)
		dyadScratch, occScratch = inc.prev.docDyadsInto(entries, occScratch, dyadScratch[:0])
		ck := clusterKeyOf(inc.prev.clusters[ou], inc.prev.bucketTimes)
		docFams, seen = docFamilySet(entries, rootOld, seen, docFams)
		for _, g := range docFams {
			if fa := agg.fams[g]; fa != nil {
				fa.df--
			}
			subClusters[g] = append(subClusters[g], ck)
		}
		if len(docFams) > 1 {
			for _, g := range docFams {
				if fa := agg.fams[g]; fa != nil {
					fa.multiDocCount--
				}
			}
		}
		pairSet, pairScratch = rootPairSet(dyadScratch, rootOld, pairSet[:0], pairScratch)
		for _, p := range pairSet {
			subPartners = appendPairEntries(subPartners, p)
		}
	}
	// 1b) 族根变化的幸存单元：成员与句内 dyad 不变（同一批 dyad 抽取一次即可），
	// 只比较同一文档在旧根与新根下的去重族根对，差集分别扣减与并入。
	for _, u := range inc.rekeyUnits {
		dyadScratch, occScratch = st.docDyadsInto(st.docEntriesOf(u), occScratch, dyadScratch[:0])
		oldPairs, pairScratch = rootPairSet(dyadScratch, rootOld, oldPairs[:0], pairScratch)
		newPairs, pairScratch = rootPairSet(dyadScratch, root, newPairs[:0], pairScratch)
		subPartners, addPartners = appendPairDiffEntries(subPartners, addPartners, oldPairs, newPairs)
	}
	for g, list := range subClusters {
		sort.Slice(list, func(i, j int) bool { return list[i] < list[j] })
		keys, cnt := compressInt64Keys(list, nil, nil)
		neg := make([]int32, len(cnt))
		for i := range cnt {
			neg[i] = -cnt[i]
		}
		fa := agg.fams[g]
		if fa == nil {
			continue
		}
		fa.clusterKey, fa.clusterCnt = mergeInt64Counts(fa.clusterKey, fa.clusterCnt, keys, neg)
	}
	agg.mergePartnerEntries(subPartners, -1, false)

	// 2) 并入 delta 单元（新增与覆盖后的新贡献）。
	for _, u := range inc.deltaUnits {
		ck := clusterKeyOf(st.clusters[u], st.bucketTimes)
		docFams, seen = docFamilySet(st.docEntriesOf(u), root, seen, docFams)
		for _, g := range docFams {
			getFam(g).df++
			addClusters[g] = append(addClusters[g], ck)
		}
		if len(docFams) > 1 {
			for _, g := range docFams {
				getFam(g).multiDocCount++
			}
		}
		pairSet, pairScratch = rootPairSet(docDyadsFlat[docDyadRowPtr[u]:docDyadRowPtr[u+1]], root, pairSet[:0], pairScratch)
		for _, p := range pairSet {
			addPartners = appendPairEntries(addPartners, p)
		}
	}
	for g, list := range addClusters {
		sort.Slice(list, func(i, j int) bool { return list[i] < list[j] })
		keys, cnt := compressInt64Keys(list, nil, nil)
		fa := getFam(g)
		fa.clusterKey, fa.clusterCnt = mergeInt64Counts(fa.clusterKey, fa.clusterCnt, keys, cnt)
	}
	agg.mergePartnerEntries(addPartners, +1, true)

	// 3) 权重：受影响族重算；clusterN 变化时整体重算。df 归零的族等价于从未出现，
	// 与全量路径（未观测族在填充时取零权重）保持一致，直接删除。
	touched := make(map[uint64]struct{}, len(addClusters)+len(subClusters)+len(addPartners)+len(subPartners))
	for g := range addClusters {
		touched[g] = struct{}{}
	}
	for g := range subClusters {
		touched[g] = struct{}{}
	}
	for _, e := range addPartners {
		touched[e.a] = struct{}{}
	}
	for _, e := range subPartners {
		touched[e.a] = struct{}{}
	}
	for g := range touched {
		if fa := agg.fams[g]; fa != nil && fa.df <= 0 {
			delete(agg.fams, g)
		}
	}
	if int32(st.clusterN) != agg.clusterN {
		agg.clusterN = int32(st.clusterN)
		for _, fa := range agg.fams {
			fa.weight = familyEffectiveWeightFromAgg(fa, st.clusterN)
		}
	} else {
		for g := range touched {
			if fa := agg.fams[g]; fa != nil {
				fa.weight = familyEffectiveWeightFromAgg(fa, st.clusterN)
			}
		}
	}
	agg.unitIDs = append(agg.unitIDs[:0], idx.UnitIDs...)
	agg.extended = true
	// 族根快照随本次构建刷新。
	agg.termParentKeys = make([]uint64, len(st.termKeys))
	copy(agg.termParentKeys, st.termKeys)
	agg.termParentVals = make([]uint64, len(st.termKeys))
	for i, h := range st.termKeys {
		agg.termParentVals[i] = st.canonicalParentOf(h)
	}
}

// collectTermPartners 由文档 dyad 构建「宿主词项 → 伙伴词项」表（构建期使用）。
func collectTermPartners(idx *Index, row []uint32, flat []rmDyad) map[uint64][]uint64 {
	out := make(map[uint64][]uint64, len(idx.DF))
	for u := 0; u < idx.N; u++ {
		for _, p := range flat[row[u]:row[u+1]] {
			out[p.a] = append(out[p.a], p.b)
			out[p.b] = append(out[p.b], p.a)
		}
	}
	return out
}

// applyDocTermPartners 把一篇文档的 dyad 计入（sign>0）或扣出（sign<0）宿主词项的伙伴计数。
func (agg *clusterAggregates) applyDocTermPartners(dyads []rmDyad, sign int32) {
	if len(dyads) == 0 {
		return
	}
	perHost := make(map[uint64][]uint64, len(dyads))
	for _, p := range dyads {
		perHost[p.a] = append(perHost[p.a], p.b)
		perHost[p.b] = append(perHost[p.b], p.a)
	}
	for host, partners := range perHost {
		if len(partners) > 1 {
			sortUint64Asc(partners)
		}
		keys, cnts := compressUint64Keys(partners, nil, nil)
		if sign < 0 {
			for i := range cnts {
				cnts[i] = -cnts[i]
			}
		}
		oldKeys, oldCnt := agg.termPartners[host], agg.termPartnerCnt[host]
		nk, nc := mergeUint64Counts(oldKeys, oldCnt, keys, cnts)
		if len(nk) == 0 {
			delete(agg.termPartners, host)
			delete(agg.termPartnerCnt, host)
			continue
		}
		agg.termPartners[host] = nk
		agg.termPartnerCnt[host] = nc
	}
}

// refreshTermKeyedWeights 由「宿主词项的伙伴计数」重算全部族权重：族内成员词项的
// 伙伴按当前族根汇总后求熵；族根移动只改变映射，不改变已存的伙伴键。
func (agg *clusterAggregates) refreshTermKeyedWeights(st *rmClusterState, idx *Index) {
	agg.refreshTermKeyedWeightsFor(st, idx, nil)
}

// refreshTermKeyedWeightsFor 只重算 touched 中的族（nil 表示全部）。族根未移动时，
// 伙伴根的映射不变，只有成员集合变化的族需要重算。
func (agg *clusterAggregates) refreshTermKeyedWeightsFor(st *rmClusterState, idx *Index, touched map[uint64]struct{}) {
	if agg == nil || st == nil || idx == nil || len(agg.fams) == 0 {
		return
	}
	root := func(h uint64) uint64 {
		return st.canonicalParentOf(h)
	}
	members := make(map[uint64][]uint64, len(agg.fams))
	for h := range idx.DF {
		r := root(h)
		members[r] = append(members[r], h)
	}
	rootCounts := make(map[uint64]int32, 64)
	rootKeys := make([]uint64, 0, 64)
	compute := func(r uint64, fa *clusterFamilyAgg) {
		clear(rootCounts)
		rootKeys = rootKeys[:0]
		for _, h := range members[r] {
			keys, cnts := agg.termPartners[h], agg.termPartnerCnt[h]
			for i, partner := range keys {
				pr := root(partner)
				if pr == r {
					continue
				}
				if _, seen := rootCounts[pr]; !seen {
					rootKeys = append(rootKeys, pr)
				}
				rootCounts[pr] += cnts[i]
			}
		}
		// 熵按计数升序累加：同计数的伙伴根给出逐位相同的被加项（p·ln p 只依赖
		// count/total），其内部顺序对浮点和无影响；计数序与 atomizer key 无关。
		// 早期实现按 hash 升序累加——同一多重集在不同 key 下排成不同顺序，使
		// 熵值差 ULP（key-invariance 缺陷）。
		type diagCntKey struct {
			cnt int32
			h   uint64
		}
		ordered := make([]diagCntKey, 0, len(rootKeys))
		for _, k := range rootKeys {
			ordered = append(ordered, diagCntKey{cnt: rootCounts[k], h: k})
		}
		slices.SortFunc(ordered, func(a, b diagCntKey) int {
			if a.cnt != b.cnt {
				if a.cnt < b.cnt {
					return -1
				}
				return 1
			}
			if a.h < b.h {
				return -1
			}
			if a.h > b.h {
				return 1
			}
			return 0
		})
		var total, entropy float64
		for _, ck := range ordered {
			total += float64(ck.cnt)
		}
		if total > 0 {
			for _, ck := range ordered {
				p := float64(ck.cnt) / total
				entropy -= p * math.Log(p)
			}
		}
		fa.weight = familyEffectiveWeight(int(fa.df), int(fa.multiDocCount), familyClusterCount(fa.clusterKey, fa.clusterCnt), total, entropy, st.clusterN)
	}
	if touched == nil {
		for r, fa := range agg.fams {
			compute(r, fa)
		}
		return
	}
	for r := range touched {
		if fa := agg.fams[r]; fa != nil {
			compute(r, fa)
		}
	}
}

// extendForDeltaTermKeyed 是词项模式的增量扩展：族根移动只迁移族集合（df/多文档/簇），
// 伙伴计数按词项键保持不变；随后重算全部族权重。
func (agg *clusterAggregates) extendForDeltaTermKeyed(st *rmClusterState, idx *Index, inc *incrementalDelta, docDyadRowPtr []uint32, docDyadsFlat []rmDyad) {
	addClusters := make(map[uint64][]int64)
	subClusters := make(map[uint64][]int64)
	getFam := func(id uint64) *clusterFamilyAgg {
		fa := agg.fams[id]
		if fa == nil {
			fa = &clusterFamilyAgg{}
			agg.fams[id] = fa
		}
		return fa
	}
	root := func(id uint64) uint64 {
		return st.canonicalParentOf(id)
	}
	rootOld := agg.rootLookup()
	var docFams []uint64
	var seen map[uint64]struct{}
	var dyadScratch []rmDyad
	var occScratch []rmPackedOccurrence

	for _, ou := range inc.dropUnits {
		entries := inc.prev.docEntriesOf(ou)
		ck := clusterKeyOf(inc.prev.clusters[ou], inc.prev.bucketTimes)
		docFams, seen = docFamilySet(entries, rootOld, seen, docFams)
		for _, g := range docFams {
			if fa := agg.fams[g]; fa != nil {
				fa.df--
			}
			subClusters[g] = append(subClusters[g], ck)
		}
		if len(docFams) > 1 {
			for _, g := range docFams {
				if fa := agg.fams[g]; fa != nil {
					fa.multiDocCount--
				}
			}
		}
		dyadScratch, occScratch = inc.prev.docDyadsInto(entries, occScratch, dyadScratch[:0])
		agg.applyDocTermPartners(dyadScratch, -1)
	}
	for _, u := range inc.rekeyUnits {
		oldU := inc.oldOf[u]
		oldEntries := inc.prev.docEntriesOf(oldU)
		ckOld := clusterKeyOf(inc.prev.clusters[oldU], inc.prev.bucketTimes)
		docFams, seen = docFamilySet(oldEntries, rootOld, seen, docFams)
		for _, g := range docFams {
			if fa := agg.fams[g]; fa != nil {
				fa.df--
			}
			subClusters[g] = append(subClusters[g], ckOld)
		}
		if len(docFams) > 1 {
			for _, g := range docFams {
				if fa := agg.fams[g]; fa != nil {
					fa.multiDocCount--
				}
			}
		}
		newEntries := st.docEntriesOf(u)
		ckNew := clusterKeyOf(st.clusters[u], st.bucketTimes)
		docFams, seen = docFamilySet(newEntries, root, seen, docFams)
		for _, g := range docFams {
			getFam(g).df++
			addClusters[g] = append(addClusters[g], ckNew)
		}
		if len(docFams) > 1 {
			for _, g := range docFams {
				getFam(g).multiDocCount++
			}
		}
	}
	for g, list := range subClusters {
		sort.Slice(list, func(i, j int) bool { return list[i] < list[j] })
		keys, cnt := compressInt64Keys(list, nil, nil)
		neg := make([]int32, len(cnt))
		for i := range cnt {
			neg[i] = -cnt[i]
		}
		fa := agg.fams[g]
		if fa == nil {
			continue
		}
		fa.clusterKey, fa.clusterCnt = mergeInt64Counts(fa.clusterKey, fa.clusterCnt, keys, neg)
	}
	for _, u := range inc.deltaUnits {
		ck := clusterKeyOf(st.clusters[u], st.bucketTimes)
		docFams, seen = docFamilySet(st.docEntriesOf(u), root, seen, docFams)
		for _, g := range docFams {
			getFam(g).df++
			addClusters[g] = append(addClusters[g], ck)
		}
		if len(docFams) > 1 {
			for _, g := range docFams {
				getFam(g).multiDocCount++
			}
		}
		agg.applyDocTermPartners(docDyadsFlat[docDyadRowPtr[u]:docDyadRowPtr[u+1]], +1)
	}
	for g, list := range addClusters {
		sort.Slice(list, func(i, j int) bool { return list[i] < list[j] })
		keys, cnt := compressInt64Keys(list, nil, nil)
		fa := getFam(g)
		fa.clusterKey, fa.clusterCnt = mergeInt64Counts(fa.clusterKey, fa.clusterCnt, keys, cnt)
	}
	touched := make(map[uint64]struct{}, len(addClusters)+len(subClusters))
	for g := range addClusters {
		touched[g] = struct{}{}
	}
	for g := range subClusters {
		touched[g] = struct{}{}
	}
	for g := range touched {
		if fa := agg.fams[g]; fa != nil && fa.df <= 0 {
			delete(agg.fams, g)
		}
	}
	// 族根移动会影响任意族的伙伴根映射；clusterN（时间桶数）变化会改变每个族的
	// 归一化分母。两者任一发生时都必须整体重算，否则只重算受影响的族。
	fullRefresh := len(inc.rekeyUnits) > 0 || int32(st.clusterN) != agg.clusterN
	agg.clusterN = int32(st.clusterN)
	if fullRefresh {
		agg.refreshTermKeyedWeights(st, idx)
	} else {
		touched := make(map[uint64]struct{}, len(addClusters)+len(subClusters))
		for g := range addClusters {
			touched[g] = struct{}{}
		}
		for g := range subClusters {
			touched[g] = struct{}{}
		}
		agg.refreshTermKeyedWeightsFor(st, idx, touched)
	}
	agg.unitIDs = append(agg.unitIDs[:0], idx.UnitIDs...)
	agg.extended = true
	agg.termParentKeys = make([]uint64, len(st.termKeys))
	copy(agg.termParentKeys, st.termKeys)
	agg.termParentVals = make([]uint64, len(st.termKeys))
	for i, h := range st.termKeys {
		agg.termParentVals[i] = st.canonicalParentOf(h)
	}
}
