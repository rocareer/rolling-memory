// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package core

import (
	"math"
	"sort"
)

// termBlockPrefixBuckets is the 4,096-bucket prefix table width used by each
// block (hash>>52). The 2b measurement found that a single block without this
// lookup window already costs 2.6× the global CSR lookup.
const termBlockPrefixBuckets = 4096

const (
	termBlockFlagUpper  uint8 = 1 << 0
	termBlockFlagCamel  uint8 = 1 << 1
	termBlockProperMask       = termBlockFlagUpper | termBlockFlagCamel
)

// termBlocks 是一代不可变的块化 term 倒排（stage 2d-1a）。
//
// 块键域是 unit 连续区间 [lo, hi)：各块区间两两不交、区间升序、并集恰为
// [0, n)，因此按块序拼接 posting 行即得全局 unit 升序，拼接本身零比较、
// 零去重、零分配（seg2d 报告 §4a）。
//
// 块内只存可合并的整数与未归一化计数（§4b）：逐词项 df / cf(Σtf) / propCnt /
// parentGroup(min) / isPrimary(OR) / bits(max) / flags(OR)，逐 posting 只存 unit
// 名次（块内升序）。Mass、DocMass、词项序、entryMeanBits、sentenceEnergyMean、
// termEffWeight、termProperRatio、dayCos 一律不烘进块，全局量查询期用当前
// N 与跨块整数和重标定一次（§4b/§4e）。
type termBlocks struct {
	// unitIDs 快照建块时的 名次→UnitID 映射，供 appendTail 证明旧名次未平移。
	unitIDs []string
	// bounds 长 len(blocks)+1：bounds[i] 是块 i 的首个 unit，末项等于 n。
	bounds []int32
	blocks []*termBlock
	n      int
	gen    uint64
}

// withUnitIDs 给从块存储读回的 posting 快照绑定同一索引的名次→UnitID 映射。
// UnitID 不属于 term block 载荷，调用方必须提供长度一致、严格升序的索引快照；
// 否则拒绝增量复用，让 refresh 回退全量构建而不是猜测旧名次。
func (tb *termBlocks) withUnitIDs(unitIDs []string) *termBlocks {
	if tb == nil || len(unitIDs) != tb.n {
		return nil
	}
	for i := 1; i < len(unitIDs); i++ {
		if unitIDs[i] <= unitIDs[i-1] {
			return nil
		}
	}
	next := *tb
	next.unitIDs = append([]string(nil), unitIDs...)
	return &next
}

// termBlock 是单块的不可变 term CSR：升序键列、指向 posting 行列的 rowPtr、
// 4,096 桶前缀表，以及与键列对齐的整数半群列。
type termBlock struct {
	lo, hi    int32
	keys      []uint64
	rowPtr    []uint32
	units     []int32
	prefixPtr [termBlockPrefixBuckets + 1]uint32

	df        []int32
	cf        []int64
	propCnt   []int32
	parentGrp []uint64
	bits      []uint16
	flags     []uint8
	primary   []uint8
}

// termBlockStats 是逐词项跨块的整数半群合成结果（§4b）。
type termBlockStats struct {
	df          int
	cf          int64
	propCnt     int
	parentGroup uint64
	isPrimary   bool
	bits        uint16
	flags       uint8
}

// buildTermBlocks 把 idx 按 unit 连续区间切块并逐块建不可变 term 倒排，
// 每块最多 maxUnits 个 unit。块数 k=ceil(N/maxUnits)，边界取 i*N/k，
// 因而 k<=N 时每块非空且大小 <=maxUnits。
//
// 前置条件：idx 持有内存倒排（Postings 与 DocTerms）。CSR 升水索引的块化
// 属于 2d 持久化切片，不在此猜测重建，直接返回 nil 由调用方保留全量路径。
func buildTermBlocks(idx *Index, maxUnits int) *termBlocks {
	if idx == nil || idx.N <= 0 || maxUnits < 1 || idx.Postings == nil || len(idx.DocTerms) != idx.N {
		return nil
	}
	k := (idx.N + maxUnits - 1) / maxUnits
	bounds := make([]int32, 1, k+1)
	for i := 1; i <= k; i++ {
		bounds = append(bounds, int32(int64(i)*int64(idx.N)/int64(k)))
	}
	tb := &termBlocks{
		unitIDs: append([]string(nil), idx.UnitIDs...),
		bounds:  bounds,
		blocks:  make([]*termBlock, 0, k),
		n:       idx.N,
		gen:     idx.stateGen,
	}
	// 全局 canonical 族根（与簇状态 parentGroupOf 同式）：每块重复同一值，
	// termStats 的 min 合并恒等于该值，且与 atomizer key 无关。
	roots := scanParentGroups(idx)
	for i := 0; i < k; i++ {
		block := buildTermBlock(idx, int(bounds[i]), int(bounds[i+1]), roots)
		if block == nil {
			return nil
		}
		tb.blocks = append(tb.blocks, block)
	}
	return tb
}

// buildTermBlock 建单块：收集块内 (term, unit) 键、升序去重成键列，
// 再按 Postings 的块内连续区间填 posting 行与整数半群列。
//
// 空区间返回空块（无键、无行、不产生假行）；索引自相矛盾（键在 DocTerms 中
// 出现却无对应 posting，或 posting 非严格升序）返回 nil，不静默修补。
func buildTermBlock(idx *Index, lo, hi int, roots map[uint64]uint64) *termBlock {
	if idx == nil || idx.Postings == nil || len(idx.DocTerms) != idx.N || lo < 0 || lo > hi || hi > idx.N {
		return nil
	}
	block := &termBlock{lo: int32(lo), hi: int32(hi)}
	total := 0
	for u := lo; u < hi; u++ {
		total += len(idx.DocTerms[u])
	}
	if total == 0 {
		return block
	}
	hashes := make([]uint64, 0, total)
	for u := lo; u < hi; u++ {
		hashes = append(hashes, idx.DocTerms[u]...)
	}
	sortUint64Asc(hashes)
	uniq := 0
	for i, h := range hashes {
		if i == 0 || h != hashes[uniq-1] {
			hashes[uniq] = h
			uniq++
		}
	}
	block.keys = hashes[:uniq:uniq]
	block.rowPtr = make([]uint32, 0, uniq+1)
	block.units = make([]int32, 0, total)
	block.df = make([]int32, uniq)
	block.cf = make([]int64, uniq)
	block.propCnt = make([]int32, uniq)
	block.parentGrp = make([]uint64, uniq)
	block.bits = make([]uint16, uniq)
	block.flags = make([]uint8, uniq)
	block.primary = make([]uint8, uniq)
	for i, h := range block.keys {
		posts := idx.Postings[h]
		plo := sort.Search(len(posts), func(j int) bool { return posts[j].Unit >= block.lo })
		phi := sort.Search(len(posts), func(j int) bool { return posts[j].Unit >= block.hi })
		if phi <= plo {
			return nil
		}
		block.rowPtr = append(block.rowPtr, uint32(len(block.units)))
		block.df[i] = int32(phi - plo)
		var cf int64
		var propCnt int32
		var bitsMax uint16
		var flags uint8
		var primary uint8
		// 词项级声明只随键变化：提到 posting 循环外，避免逐 posting 重复查表。
		// 族根同理：全局 canonical 根是词项常量（buildTermBlocks 预计算），
		// 缺表回退自根。
		termBits := idx.TermBits[h]
		termFlags := idx.TermFlags[h]
		grp := h
		if r, ok := roots[h]; ok {
			grp = r
		}
		prevUnit := int32(-1)
		for _, p := range posts[plo:phi] {
			u := p.Unit
			if u <= prevUnit {
				// posting 行必须块内严格升序：乱序/重复是索引自身矛盾。
				return nil
			}
			prevUnit = u
			block.units = append(block.units, u)

			tf := int64(p.Occ)
			if tf < 1 {
				tf = 1
			}
			cf += tf

			fl := termFlags
			if int(u) < len(idx.UnitFlags) && idx.UnitFlags[u] != nil {
				if f, ok := idx.UnitFlags[u][h]; ok {
					fl = f
				}
			}
			if fl&termBlockProperMask != 0 {
				propCnt++
			}
			if fl&rmFlagCompanion == 0 {
				primary = 1
			}
			flags |= fl

			bits := termBits
			if int(u) < len(idx.UnitBits) && idx.UnitBits[u] != nil {
				if b, ok := idx.UnitBits[u][h]; ok {
					bits = b
				}
			}
			if bits > bitsMax {
				bitsMax = bits
			}
		}
		block.cf[i] = cf
		block.propCnt[i] = propCnt
		block.parentGrp[i] = grp
		block.bits[i] = bitsMax
		block.flags[i] = flags
		block.primary[i] = primary
	}
	block.rowPtr = append(block.rowPtr, uint32(len(block.units)))

	pi := 0
	for bucket := 0; bucket < termBlockPrefixBuckets; bucket++ {
		for pi < len(block.keys) && int(block.keys[pi]>>52) <= bucket {
			pi++
		}
		block.prefixPtr[bucket+1] = uint32(pi)
	}
	return block
}

// index 返回词项在该块键列中的列号；块内无该词项返回 ok=false。
// 先按 4,096 桶前缀表缩窗，再在桶内二分升序键列。
func (b *termBlock) index(hash uint64) (int, bool) {
	keys := b.keys
	if len(keys) == 0 {
		return -1, false
	}
	bucket := hash >> 52
	lo, hi := int(b.prefixPtr[bucket]), int(b.prefixPtr[bucket+1])
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if keys[mid] < hash {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo < len(keys) && keys[lo] == hash {
		return lo, true
	}
	return -1, false
}

// termStats 只用整数半群合成词项跨块统计：df/cf/propCnt 相加，parentGroup
// 取 min（0 参与比较；每个块写的是同一词项的全局 canonical 族根，跨块取 min
// 只是把相同值合并，语义上恒等于该值），isPrimary/flags 取 OR，bits 取 max。
// ok=false 表示该词项在所有块中都不存在（df=0）。
func (tb *termBlocks) termStats(hash uint64) (termBlockStats, bool) {
	var s termBlockStats
	if tb == nil {
		return s, false
	}
	found := false
	haveGroup := false
	for _, b := range tb.blocks {
		i, ok := b.index(hash)
		if !ok {
			continue
		}
		found = true
		s.df += int(b.df[i])
		s.cf += b.cf[i]
		s.propCnt += int(b.propCnt[i])
		if g := b.parentGrp[i]; !haveGroup || g < s.parentGroup {
			s.parentGroup = g
			haveGroup = true
		}
		if b.primary[i] != 0 {
			s.isPrimary = true
		}
		if b.bits[i] > s.bits {
			s.bits = b.bits[i]
		}
		s.flags |= b.flags[i]
	}
	return s, found
}

// termPostingCount 返回 df=Σdf_i，不物化 posting。
func (tb *termBlocks) termPostingCount(hash uint64) int {
	if tb == nil {
		return 0
	}
	df := 0
	for _, b := range tb.blocks {
		if i, ok := b.index(hash); ok {
			df += int(b.df[i])
		}
	}
	return df
}

// termPostingCursor 是跨块 posting 的 pull 迭代器。零值即可用，reset 选词项。
// 相邻块承载互不交且升序的 unit 区间，因此跨块推进只做块序拼接：不比较 unit、
// 不去重、不分配合成切片（§4a-2、RFC §五-2）。
//
// 游标持有快照指针，不得在快照被替换后继续使用。
type termPostingCursor struct {
	tb    *termBlocks
	hash  uint64
	blk   int
	row   int
	end   int
	units []int32
}

// reset 把游标定位到 hash 的首个 posting（惰性：next 时才逐块查找）。
func (c *termPostingCursor) reset(tb *termBlocks, hash uint64) {
	c.tb = tb
	c.hash = hash
	c.blk = 0
	c.row = 0
	c.end = 0
	c.units = nil
}

// next 返回下一个 unit；遍历完毕返回 ok=false。每次跨块只在块键列做一次
// 前缀桶 + 二分查找，块内行走是纯下标递增。
func (c *termPostingCursor) next() (int32, bool) {
	for {
		if c.row < c.end {
			u := c.units[c.row]
			c.row++
			return u, true
		}
		if c.tb == nil || c.blk >= len(c.tb.blocks) {
			return 0, false
		}
		b := c.tb.blocks[c.blk]
		c.blk++
		i, ok := b.index(c.hash)
		if !ok {
			continue
		}
		start, end := int(b.rowPtr[i]), int(b.rowPtr[i+1])
		if end <= start {
			continue
		}
		c.units = b.units
		c.row, c.end = start, end
	}
}

// visitTermPostings 按全局 unit 升序遍历 hash 的全部 posting，不物化合并切片；
// fn 返回 false 提前停止，返回值是已访问的 posting 数。fn 内不分配即满足
// 0 B/query 契约。
func (tb *termBlocks) visitTermPostings(hash uint64, fn func(unit int32) bool) int {
	if tb == nil || fn == nil {
		return 0
	}
	var cur termPostingCursor
	cur.reset(tb, hash)
	visited := 0
	for {
		u, ok := cur.next()
		if !ok {
			return visited
		}
		visited++
		if !fn(u) {
			return visited
		}
	}
}

// termMass 用跨块整数和与快照的 N 重算词项 IDF 质量：
// m = log1p((N - df + 0.5) / (df + 0.5))。Mass 从不入块；算术顺序与
// Index.finalizeMasses 一致，故与全量索引 Float64bits 相等（§4e）。
func (tb *termBlocks) termMass(hash uint64) (float64, bool) {
	s, ok := tb.termStats(hash)
	if !ok || tb.n <= 0 || s.df <= 0 {
		return 0, false
	}
	return math.Log1p((float64(tb.n) - float64(s.df) + 0.5) / (float64(s.df) + 0.5)), true
}

// termProperRatio 用 ΣpropCnt/Σdf 在查询期重算 proper 占比；propCnt 为 0 时
// 占比为 0（与簇状态构建同口径）。比率本身不进块。
func (tb *termBlocks) termProperRatio(hash uint64) float64 {
	s, ok := tb.termStats(hash)
	if !ok || s.propCnt == 0 {
		return 0
	}
	return float64(s.propCnt) / float64(s.df)
}

// appendTail 在尾部追加谓词成立时扩展快照：复用全部旧块（字节不变），只为
// 新增的 [tb.n, idx.N) 区间建新块。
//
// 尾部追加谓词是机械判定，不依赖调用方自觉：
//  1. replacedOld 为真即拒绝：调用方从本批 docs 与旧索引的 ID 命中机械得出
//     「存在同名覆盖」（Engine.AppendDocs 的 prevDrops 同一来源），覆盖会让旧块
//     的 df/posting/group 全部 stale；
//  2. added 恰好是自快照以来的全部新文档（len(added) == idx.N - tb.n）；
//  3. added 中没有任何 UnitID 已出现在旧名次表里（双保险：即使调用方漏了
//     replacedOld，覆盖文档本身也会被拒绝）；
//  4. 快照捕获的每个 UnitID 在新索引中名次不变（任何字典序更小的新 UnitID 都会
//     平移旧名次；seg2d 漂移测量：一次中段插入使 10,000/10,000 篇的 DocMass 位
//     模式与词项序同时改变）。
//
// 任一条件不成立返回 (nil,false)，调用方必须整库重建；这里绝不静默拼接或近似。
//
// idx.N == tb.n 且 added 为空时返回 (tb, true)（无新 unit，无事可做）。
func (tb *termBlocks) appendTail(idx *Index, maxUnits int, added []Doc, replacedOld bool) (*termBlocks, bool) {
	if tb == nil || idx == nil || maxUnits < 1 || idx.N < tb.n || replacedOld {
		return nil, false
	}
	if len(tb.unitIDs) != tb.n {
		return nil, false
	}
	if len(added) != idx.N-tb.n {
		return nil, false
	}
	for i := range added {
		if j := sort.SearchStrings(tb.unitIDs, added[i].UnitID); j < len(tb.unitIDs) && tb.unitIDs[j] == added[i].UnitID {
			return nil, false
		}
	}
	for u := 0; u < tb.n; u++ {
		if u >= len(idx.UnitIDs) || idx.UnitIDs[u] != tb.unitIDs[u] {
			return nil, false
		}
	}
	if idx.N == tb.n {
		return tb, true
	}
	k := (idx.N - tb.n + maxUnits - 1) / maxUnits
	bounds := make([]int32, len(tb.bounds), len(tb.bounds)+k)
	copy(bounds, tb.bounds)
	blocks := make([]*termBlock, len(tb.blocks), len(tb.blocks)+k)
	copy(blocks, tb.blocks)
	roots := scanParentGroups(idx)
	for i := 1; i <= k; i++ {
		hi := tb.n + int(int64(i)*int64(idx.N-tb.n)/int64(k))
		block := buildTermBlock(idx, int(bounds[len(bounds)-1]), hi, roots)
		if block == nil {
			return nil, false
		}
		bounds = append(bounds, int32(hi))
		blocks = append(blocks, block)
	}
	return &termBlocks{
		unitIDs: append([]string(nil), idx.UnitIDs...),
		bounds:  bounds,
		blocks:  blocks,
		n:       idx.N,
		gen:     idx.stateGen,
	}, true
}

// refresh 返回 idx 对应的块快照：尾部追加谓词成立时只建新增块并复用旧块，
// 否则整库重建。added 是自快照以来的全部新文档，replacedOld 表示本批含同名
// 覆盖；谓词失败是显式的全量重建信号，不保留任何增量近似。
func (tb *termBlocks) refresh(idx *Index, maxUnits int, added []Doc, replacedOld bool) *termBlocks {
	if next, ok := tb.appendTail(idx, maxUnits, added, replacedOld); ok {
		return next
	}
	return buildTermBlocks(idx, maxUnits)
}
