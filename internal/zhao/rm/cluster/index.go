// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cluster

import (
	"math"
	"sort"
)

// TermRole 表示词项（Term）在所属物理词槽偏序格 L(s) 中的代数角色（8-bit 扩展域：低位 0..4 为现役闭集，高 5 bits 预留未来扩展）。
type TermRole uint8

// AtomRole 是 TermRole 的向后兼容类型别名（标准术语统一为 TermRole）。
type AtomRole = TermRole

const (
	RolePrimary         TermRole = 0 // 原词表面核（精确词面，独占 1 个物理词槽）
	RoleMorphStem       TermRole = 1 // 一阶同根词干（屈折/派生词干、撇号剥离、变音折叠、游程坍缩）
	RoleSubwordPiece    TermRole = 2 // 二阶复合子词（次级切分块、剥离数字的前导字母块等）
	RoleClosedBeacon    TermRole = 3 // 闭集世界知识装饰（@*）与日历信标（cal:*）
	RoleBiwordHomomorph TermRole = 4 // 相邻双词合成闭集同态修饰（如 "New Zealand" -> @geo:cc:nz）
)

// Term.ExtFlags（16-bit 扩展声明标志位域）低 6 位记录词项在单元内跨词槽出现的角色分布掩码，
// 高 10 位预留未来扩展（零结构体内存膨胀）：
const (
	ExtFlagSeenPrimary     uint16 = 1 << 0 // 在单元内曾作为原词表面核 (RolePrimary) 出现
	ExtFlagSeenMorphOrClos uint16 = 1 << 1 // 在单元内曾作为一阶词干 (RoleMorphStem) 或闭集信标 (RoleClosedBeacon) 出现
	ExtFlagSeenSubword     uint16 = 1 << 2 // 在单元内曾作为二阶子词 (RoleSubwordPiece / RoleBiwordHomomorph) 出现
)

// Term 表示键控词项（特征项）：身份哈希 + 声明式载体元数据 {b, f}（Bits 为载体最大
// 宽度、Flags 为 8 位 ClassFlags 并集、Role 为词槽偏序格角色、ExtFlags 为 16 位预留扩展标志位域）
// 以及所属主词族 Group 与首现句槽坐标 (Sent, Slot)。全部为平波声明字段，零先验能量，
// 不参与词项身份计算（身份唯一由 Hash 决定；利用原结构体对齐空洞内嵌 Role 与 ExtFlags，总宽恒定 32B）。
type Term struct {
	Hash     uint64
	Bits     uint16
	Flags    uint8
	Role     TermRole
	ExtFlags uint16
	Group    uint64
	Sent     uint16
	Slot     uint16
}

// Atom 是 Term 的向后兼容类型别名（zhao 架构去伪物理化标准术语统一为 Term）。
type Atom = Term

func packTermRoleExt(role TermRole, ext uint16) uint8 {
	return (uint8(role) & 0x1F) | uint8((ext&0x07)<<5)
}

func unpackTermRoleExt(re uint8) (TermRole, uint16) {
	return TermRole(re & 0x1F), uint16((re >> 5) & 0x07)
}

func clampPosU16(v int) uint16 {
	if v <= 0 {
		return 0
	}
	if v > 0xFFFF {
		return 0xFFFF
	}
	return uint16(v)
}

// InferSlotRole 推导词项相对于宿主主词 host 的槽内偏序格角色：
//  1. 双词闭集同态修饰（RoleBiwordHomomorph）与单/双词闭集信标（RoleClosedBeacon）具有代数封闭性，恒保持各自闭集角色；
//  2. 剥离尾部数字的前导字母块伴随（ExtFlagSeenSubword != 0 且 host 含数字而自身不含数字）恒为 RoleSubwordPiece（免疫跨槽 max(Bits) 冲刷）；
//  3. 其余跨槽或合成夹具情况按声明位与宿主宽度差（host.Bits >= a.Bits+3）无损推导。
func (a Term) InferSlotRole(host Term) TermRole {
	const (
		flagDigit     uint8 = 1 << 2
		flagCompanion uint8 = 1 << 4
	)
	if a.Role == RoleBiwordHomomorph && (host.Hash == 0 || host.Hash == a.Group) {
		return RoleBiwordHomomorph
	}
	if a.Role == RoleClosedBeacon {
		return RoleClosedBeacon
	}
	if a.ExtFlags&ExtFlagSeenSubword != 0 && (a.ExtFlags&(ExtFlagSeenPrimary|ExtFlagSeenMorphOrClos) == 0 || (host.Hash != 0 && host.Hash != a.Hash && host.Flags&flagDigit != 0 && a.Flags&flagDigit == 0)) {
		return RoleSubwordPiece
	}
	if host.Hash != 0 && host.Hash != a.Hash && int(host.Bits) >= int(a.Bits)+3 {
		return RoleSubwordPiece
	}
	if a.Flags&flagCompanion == 0 || ((host.Hash == 0 || host.Hash == a.Hash) && a.Role == RolePrimary && a.ExtFlags&ExtFlagSeenPrimary != 0) {
		return RolePrimary
	}
	return RoleMorphStem
}

// IsSlot2Companion 报告伴随词项相对于宿主主词 host 是否处于二阶复合子词角色（RoleSubwordPiece）。
func (a Term) IsSlot2Companion(host Term) bool {
	return a.InferSlotRole(host) == RoleSubwordPiece
}

// TermPosition locates one carrier occurrence in the caller's sentence stream.
type TermPosition struct {
	Sentence int
	Slot     int
}

// DocMeta carries the optional unit metadata mirrored from the cloud wire.
type DocMeta struct {
	OccurredAt int64
	NarratedAt int64
	IngestedAt int64
	AuthorHash uint64
	DocHash    uint64
	BlockHash  uint64
	LineStart  int
	LineEnd    int
	Layer      string
	CitedIDs   []string
}

func (m DocMeta) RefTime() int64 {
	for _, at := range [3]int64{m.OccurredAt, m.NarratedAt, m.IngestedAt} {
		if at > 0 {
			return at
		}
	}
	return 0
}

// Doc 是一条待入库记忆单元（去重词项表 + 逐词项出现次数 + 可选元信息）。
type Doc struct {
	UnitID     string
	OccurredAt int64
	NarratedAt int64
	IngestedAt int64
	AuthorHash uint64
	DocHash    uint64
	BlockHash  uint64
	LineStart  int
	LineEnd    int
	Layer      string
	CitedIDs   []string
	RawText    string
	Terms      map[uint64]Term
	Counts     map[uint64]int
	Positions  map[uint64][]TermPosition
}

func (d Doc) Meta() DocMeta { return d.meta() }

func (d Doc) meta() DocMeta {
	var cited []string
	if len(d.CitedIDs) > 0 {
		cited = append([]string(nil), d.CitedIDs...)
	}
	return DocMeta{
		OccurredAt: d.OccurredAt,
		NarratedAt: d.NarratedAt,
		IngestedAt: d.IngestedAt,
		AuthorHash: d.AuthorHash,
		DocHash:    d.DocHash,
		BlockHash:  d.BlockHash,
		LineStart:  d.LineStart,
		LineEnd:    d.LineEnd,
		Layer:      d.Layer,
		CitedIDs:   cited,
	}
}

func (idx *Index) meanUnitGap() float64 {
	times := make([]int64, 0, len(idx.UnitMeta))
	for _, meta := range idx.UnitMeta {
		if ref := meta.RefTime(); ref > 0 {
			times = append(times, ref)
		}
	}
	if len(times) < 2 {
		return 0
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	total, count := 0.0, 0
	for at := 1; at < len(times); at++ {
		if gap := times[at] - times[at-1]; gap > 0 {
			total += float64(gap)
			count++
		}
	}
	if count == 0 {
		return 0
	}
	return total / float64(count)
}

type Posting struct {
	Unit int32
	Occ  int32
}

// Index 是内存倒排视图；持久化由调用方负责。
type Index struct {
	UnitIDs       []string
	UnitOf        map[string]int32
	DocMass       []float64
	DocTerms      [][]uint64
	Mass          map[uint64]float64
	DF            map[uint64]int
	Postings      map[uint64][]Posting
	N             int
	TermBits      map[uint64]uint16
	TermFlags     map[uint64]uint8
	UnitBits      []map[uint64]uint16
	UnitFlags     []map[uint64]uint8
	UnitRoleExt   []map[uint64]uint8
	UnitExtFlags  []map[uint64]uint16
	UnitGroups    []map[uint64]uint64
	MeanBits      float64
	UnitMeta      []DocMeta
	UnitPositions []map[uint64][]TermPosition
	// ...
	TermKeys      []uint64
	TermPrefixPtr [4097]uint32

	// Internal slices for raw storage
	rawTermDF    []int32
	rawTermMass  []float64
	rawTermBits  []uint16
	rawTermFlags []uint8

	PostingDigest uint64

	stateGen          uint64
	csrFallbackExport func() []Doc
}

func (idx *Index) BuildTermPrefixIndex() {
	keys := idx.TermKeys
	n := uint32(len(keys))
	var cur uint32
	for b := 0; b < 4096; b++ {
		idx.TermPrefixPtr[b] = cur
		for cur < n && int(keys[cur]>>52) == b {
			cur++
		}
	}
	idx.TermPrefixPtr[4096] = n
}

func lookupPrefixTermCol(keys []uint64, prefixPtr *[4097]uint32, hash uint64) (int, bool) {
	if len(keys) == 0 {
		return -1, false
	}
	b := hash >> 52
	lo, hi := int(prefixPtr[b]), int(prefixPtr[b+1])
	if hi == 0 && prefixPtr[4096] == 0 {
		lo, hi = 0, len(keys)
	}
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

func (idx *Index) lookupTermCol(hash uint64) (int, bool) {
	return lookupPrefixTermCol(idx.TermKeys, &idx.TermPrefixPtr, hash)
}

func (idx *Index) LookupUnit(unitID string) (int32, bool) {
	if idx == nil || unitID == "" {
		return 0, false
	}
	if idx.UnitOf != nil {
		u, ok := idx.UnitOf[unitID]
		return u, ok
	}
	lo, hi := 0, len(idx.UnitIDs)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if idx.UnitIDs[mid] < unitID {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo < len(idx.UnitIDs) && idx.UnitIDs[lo] == unitID {
		return int32(lo), true
	}
	return 0, false
}

func HighDFThresholdForN(n int) int {
	return int(math.Max(math.E*math.E, float64(n)/(math.E*math.E)))
}

func (idx *Index) HighDFThreshold() int {
	if idx == nil {
		return HighDFThresholdForN(0)
	}
	return HighDFThresholdForN(idx.N)
}

func (idx *Index) TermMassOf(hash uint64) (float64, bool) {
	if idx.Mass != nil {
		m, ok := idx.Mass[hash]
		return m, ok
	}
	if col, ok := idx.lookupTermCol(hash); ok && col < len(idx.rawTermMass) {
		return idx.rawTermMass[col], true
	}
	return 0, false
}

func (idx *Index) TermMass(hash uint64) (float64, bool) { return idx.TermMassOf(hash) }

func (idx *Index) TermDFOf(hash uint64) int {
	if idx.DF != nil {
		return idx.DF[hash]
	}
	if col, ok := idx.lookupTermCol(hash); ok && col < len(idx.rawTermDF) {
		return int(idx.rawTermDF[col])
	}
	return 0
}

func (idx *Index) TermDF(hash uint64) int { return idx.TermDFOf(hash) }

func (idx *Index) TermBitsOf(hash uint64) uint16 {
	if idx.TermBits != nil {
		return idx.TermBits[hash]
	}
	if col, ok := idx.lookupTermCol(hash); ok && col < len(idx.rawTermBits) {
		return idx.rawTermBits[col]
	}
	return 0
}

func (idx *Index) TermFlagsOf(hash uint64) uint8 {
	if idx.TermFlags != nil {
		return idx.TermFlags[hash]
	}
	if col, ok := idx.lookupTermCol(hash); ok && col < len(idx.rawTermFlags) {
		return idx.rawTermFlags[col]
	}
	return 0
}

func (idx *Index) ReleaseTransientUnitMaps() {
	if idx == nil || (len(idx.TermKeys) == 0 && idx.csrFallbackExport == nil) {
		return
	}
	idx.UnitPositions = nil
	idx.UnitBits = nil
	idx.UnitFlags = nil
	idx.UnitRoleExt = nil
	idx.UnitExtFlags = nil
	idx.UnitGroups = nil
	idx.Postings = nil
	idx.DocTerms = nil
	idx.DocMass = nil
	idx.UnitOf = nil
	if len(idx.TermKeys) > 0 {
		idx.Mass = nil
		idx.DF = nil
		idx.TermBits = nil
		idx.TermFlags = nil
	}
}

func BuildIndex(docs []Doc) *Index {
	latest := make(map[string]Doc, len(docs))
	for _, doc := range docs {
		if doc.UnitID != "" {
			latest[doc.UnitID] = doc
		}
	}
	ids := make([]string, 0, len(latest))
	for id := range latest {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	idx := &Index{
		UnitIDs:       ids,
		UnitOf:        make(map[string]int32, len(ids)),
		DocMass:       make([]float64, len(ids)),
		DocTerms:      make([][]uint64, len(ids)),
		Mass:          make(map[uint64]float64),
		DF:            make(map[uint64]int),
		Postings:      make(map[uint64][]Posting),
		N:             len(ids),
		TermBits:      make(map[uint64]uint16),
		TermFlags:     make(map[uint64]uint8),
		UnitBits:      make([]map[uint64]uint16, len(ids)),
		UnitFlags:     make([]map[uint64]uint8, len(ids)),
		UnitRoleExt:   make([]map[uint64]uint8, len(ids)),
		UnitExtFlags:  make([]map[uint64]uint16, len(ids)),
		UnitGroups:    make([]map[uint64]uint64, len(ids)),
		UnitMeta:      make([]DocMeta, len(ids)),
		UnitPositions: make([]map[uint64][]TermPosition, len(ids)),
	}
	for unit, id := range ids {
		idx.UnitOf[id] = int32(unit)
		doc := latest[id]
		idx.UnitMeta[unit] = doc.meta()
		hasExplicitPos := len(doc.Positions) > 0
		if hasExplicitPos {
			posMap := make(map[uint64][]TermPosition, len(doc.Positions))
			for h, pl := range doc.Positions {
				if len(pl) > 0 {
					cp := make([]TermPosition, len(pl))
					copy(cp, pl)
					posMap[h] = cp
				} else if pl != nil {
					posMap[h] = []TermPosition{}
				}
			}
			idx.UnitPositions[unit] = posMap
		}
		for hash, atom := range doc.Terms {
			occ := doc.Counts[hash]
			if occ < 1 {
				occ = 1
			} else if occ > math.MaxInt32 {
				occ = math.MaxInt32
			}
			idx.Postings[hash] = append(idx.Postings[hash], Posting{Unit: int32(unit), Occ: int32(occ)})
			idx.DF[hash]++
			if !hasExplicitPos && atom.Sent > 0 && atom.Slot > 0 {
				if idx.UnitPositions[unit] == nil {
					idx.UnitPositions[unit] = make(map[uint64][]TermPosition, len(doc.Terms))
				}
				idx.UnitPositions[unit][hash] = []TermPosition{{Sentence: int(atom.Sent - 1), Slot: int(atom.Slot - 1)}}
			}
			if idx.UnitBits[unit] == nil {
				idx.UnitBits[unit] = make(map[uint64]uint16, len(doc.Terms))
			}
			if atom.Bits > 0 {
				if atom.Bits > idx.TermBits[hash] {
					idx.TermBits[hash] = atom.Bits
				}
				if atom.Bits > idx.UnitBits[unit][hash] {
					idx.UnitBits[unit][hash] = atom.Bits
				}
			} else if _, exists := idx.UnitBits[unit][hash]; !exists {
				idx.UnitBits[unit][hash] = 0
			}
			if idx.UnitFlags[unit] == nil {
				idx.UnitFlags[unit] = make(map[uint64]uint8, len(doc.Terms))
			}
			idx.UnitFlags[unit][hash] |= atom.Flags
			if atom.Flags != 0 {
				idx.TermFlags[hash] |= atom.Flags
			}
			if atom.Group != 0 {
				if idx.UnitGroups[unit] == nil {
					idx.UnitGroups[unit] = make(map[uint64]uint64, len(doc.Terms))
				}
				idx.UnitGroups[unit][hash] = atom.Group
			}
			if re := packTermRoleExt(atom.Role, atom.ExtFlags); re != 0 {
				if idx.UnitRoleExt[unit] == nil {
					idx.UnitRoleExt[unit] = make(map[uint64]uint8, len(doc.Terms))
				}
				idx.UnitRoleExt[unit][hash] = re
			}
			if atom.ExtFlags&^0x07 != 0 {
				if idx.UnitExtFlags[unit] == nil {
					idx.UnitExtFlags[unit] = make(map[uint64]uint16, 4)
				}
				idx.UnitExtFlags[unit][hash] |= atom.ExtFlags
			}
		}
	}
	idx.finalizeMasses()
	return idx
}

func (idx *Index) finalizeMasses() {
	for h, df := range idx.DF {
		var mass float64
		if idx.N > 0 && df > 0 {
			mass = math.Log1p((float64(idx.N) - float64(df) + 0.5) / (float64(df) + 0.5))
		}
		idx.Mass[h] = mass
	}
	unitAtomCounts := make([]int, idx.N)
	var totalDocTerms int
	for _, posts := range idx.Postings {
		for _, p := range posts {
			unitAtomCounts[p.Unit]++
			totalDocTerms++
		}
	}
	docAtomsArena := make([]uint64, totalDocTerms)
	var curOff int
	for u := 0; u < idx.N; u++ {
		cnt := unitAtomCounts[u]
		idx.DocTerms[u] = docAtomsArena[curOff : curOff : curOff+cnt]
		curOff += cnt
	}
	for h, posts := range idx.Postings {
		for _, p := range posts {
			idx.DocTerms[p.Unit] = append(idx.DocTerms[p.Unit], h)
		}
	}
	const flagCompanion uint8 = 1 << 4
	for unit := range idx.DocTerms {
		atoms := idx.DocTerms[unit]
		sort.Slice(atoms, func(a, b int) bool {
			if idx.Mass[atoms[a]] == idx.Mass[atoms[b]] {
				return atoms[a] < atoms[b]
			}
			return idx.Mass[atoms[a]] > idx.Mass[atoms[b]]
		})
		for _, h := range atoms {
			idx.DocMass[unit] += idx.Mass[h]
		}
		if unit < len(idx.UnitGroups) && idx.UnitGroups[unit] == nil && unit < len(idx.UnitPositions) && len(idx.UnitPositions[unit]) > 0 {
			posMap := idx.UnitPositions[unit]
			var uBits map[uint64]uint16
			if unit < len(idx.UnitBits) {
				uBits = idx.UnitBits[unit]
			}
			var uFlags map[uint64]uint8
			if unit < len(idx.UnitFlags) {
				uFlags = idx.UnitFlags[unit]
			}
			slotPrimary := make(map[TermPosition]uint64, len(posMap))
			slotPrimBits := make(map[TermPosition]uint16, len(posMap))
			slotPrimComp := make(map[TermPosition]bool, len(posMap))
			for _, h := range atoms {
				fl := idx.TermFlags[h]
				if uFlags != nil {
					fl = uFlags[h]
				}
				bits := idx.TermBits[h]
				if uBits != nil {
					if b, ok := uBits[h]; ok {
						bits = b
					}
				}
				isComp := fl&flagCompanion != 0
				for _, pos := range posMap[h] {
					prevH, exists := slotPrimary[pos]
					if !exists {
						slotPrimary[pos] = h
						slotPrimBits[pos] = bits
						slotPrimComp[pos] = isComp
						continue
					}
					prevComp := slotPrimComp[pos]
					prevBits := slotPrimBits[pos]
					if (prevComp && !isComp) || (prevComp == isComp && (bits > prevBits || (bits == prevBits && h < prevH))) {
						slotPrimary[pos] = h
						slotPrimBits[pos] = bits
						slotPrimComp[pos] = isComp
					}
				}
			}
			uGroups := make(map[uint64]uint64, len(atoms))
			for _, h := range atoms {
				fl := idx.TermFlags[h]
				if uFlags != nil {
					fl = uFlags[h]
				}
				grp := h
				if fl&flagCompanion != 0 {
					if posList := posMap[h]; len(posList) > 0 {
						minPos := posList[0]
						for _, p := range posList[1:] {
							if p.Sentence < minPos.Sentence || (p.Sentence == minPos.Sentence && p.Slot < minPos.Slot) {
								minPos = p
							}
						}
						if prim, ok := slotPrimary[minPos]; ok && prim != 0 {
							grp = prim
						}
					}
				}
				uGroups[h] = grp
			}
			idx.UnitGroups[unit] = uGroups
		}
	}
	keys := make([]uint64, 0, len(idx.TermBits))
	for h, bits := range idx.TermBits {
		if bits > 0 {
			keys = append(keys, h)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	var bitSum float64
	for _, h := range keys {
		bitSum += float64(idx.TermBits[h])
	}
	if len(keys) > 0 {
		idx.MeanBits = bitSum / float64(len(keys))
	}
	idx.PostingDigest = idx.computePostingDigestFromMap()
	if idx.stateGen == 0 {
		idx.stateGen = 1
	}
}

func (idx *Index) computePostingDigestFromMap() uint64 {
	if idx == nil || len(idx.Postings) == 0 {
		return 0
	}
	tKeys := make([]uint64, 0, len(idx.Postings))
	for k := range idx.Postings {
		tKeys = append(tKeys, k)
	}
	sort.Slice(tKeys, func(i, j int) bool { return tKeys[i] < tKeys[j] })
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	mix := func(h, v uint64) uint64 {
		for i := 0; i < 8; i++ {
			h ^= (v >> (8 * i)) & 0xFF
			h *= prime64
		}
		return h
	}
	var posScratch []uint32
	h := uint64(offset64)
	for _, tk := range tKeys {
		posts := idx.Postings[tk]
		h = mix(h, tk)
		h = mix(h, uint64(len(posts)))
		for _, p := range posts {
			u := int(p.Unit)
			occ := p.Occ
			if occ < 1 {
				occ = 1
			}
			h = mix(h, (uint64(uint32(p.Unit))<<32)|uint64(uint32(occ)))
			group := tk
			if u >= 0 && u < len(idx.UnitGroups) && idx.UnitGroups[u] != nil && idx.UnitGroups[u][tk] != 0 {
				group = idx.UnitGroups[u][tk]
			}
			bits := idx.TermBits[tk]
			if u >= 0 && u < len(idx.UnitBits) && idx.UnitBits[u] != nil {
				if b, ok := idx.UnitBits[u][tk]; ok {
					bits = b
				}
			}
			fl := idx.TermFlags[tk]
			if u >= 0 && u < len(idx.UnitFlags) && idx.UnitFlags[u] != nil {
				if f, ok := idx.UnitFlags[u][tk]; ok {
					fl = f
				}
			}
			var re uint8
			if u >= 0 && u < len(idx.UnitRoleExt) && idx.UnitRoleExt[u] != nil {
				re = idx.UnitRoleExt[u][tk]
			}
			h = mix(h, group)
			h = mix(h, (uint64(bits)<<16)|(uint64(fl)<<8)|uint64(re))
			posScratch = posScratch[:0]
			var overflowPos []uint64
			if u >= 0 && u < len(idx.UnitPositions) && idx.UnitPositions[u] != nil {
				for _, rp := range idx.UnitPositions[u][tk] {
					posScratch = append(posScratch, packTermPosition(rp.Sentence, rp.Slot))
					if rp.Sentence < 0 || rp.Sentence > 0xFFFF || rp.Slot < 0 || rp.Slot > 0xFFFF {
						overflowPos = append(overflowPos, (uint64(uint32(rp.Sentence))<<32)|uint64(uint32(rp.Slot)))
					}
				}
				if len(posScratch) > 1 {
					sort.Slice(posScratch, func(a, b int) bool { return posScratch[a] < posScratch[b] })
				}
				if len(overflowPos) > 1 {
					sort.Slice(overflowPos, func(a, b int) bool { return overflowPos[a] < overflowPos[b] })
				}
			}
			h = mix(h, uint64(len(posScratch)))
			for _, pp := range posScratch {
				h = mix(h, uint64(pp))
			}
			for _, op := range overflowPos {
				h = mix(h, op)
			}
		}
	}
	return h
}

func (idx *Index) ExportDocs() []Doc {
	if idx == nil || idx.N == 0 {
		return nil
	}
	if idx.Postings == nil && len(idx.DocTerms) == 0 {
		if idx.csrFallbackExport != nil {
			return idx.csrFallbackExport()
		}
		return nil
	}
	docs := make([]Doc, idx.N)
	for u := 0; u < idx.N; u++ {
		var meta DocMeta
		if u < len(idx.UnitMeta) {
			meta = idx.UnitMeta[u]
		}
		var atoms []uint64
		if u < len(idx.DocTerms) {
			atoms = idx.DocTerms[u]
		}
		doc := Doc{
			UnitID:     idx.UnitIDs[u],
			OccurredAt: meta.OccurredAt,
			NarratedAt: meta.NarratedAt,
			IngestedAt: meta.IngestedAt,
			AuthorHash: meta.AuthorHash,
			DocHash:    meta.DocHash,
			BlockHash:  meta.BlockHash,
			LineStart:  meta.LineStart,
			LineEnd:    meta.LineEnd,
			Layer:      meta.Layer,
			Terms:      make(map[uint64]Atom, len(atoms)),
			Counts:     make(map[uint64]int, len(atoms)),
		}
		if len(meta.CitedIDs) > 0 {
			doc.CitedIDs = append([]string(nil), meta.CitedIDs...)
		}
		if u < len(idx.UnitPositions) && len(idx.UnitPositions[u]) > 0 {
			posMap := make(map[uint64][]TermPosition, len(idx.UnitPositions[u]))
			for h, pl := range idx.UnitPositions[u] {
				if len(pl) > 0 {
					posMap[h] = append([]TermPosition(nil), pl...)
				}
			}
			doc.Positions = posMap
		}
		docs[u] = doc
	}
	for h, posts := range idx.Postings {
		for _, p := range posts {
			u := int(p.Unit)
			if u < 0 || u >= idx.N {
				continue
			}
			bits := idx.TermBits[h]
			if u < len(idx.UnitBits) && idx.UnitBits[u] != nil {
				if b, ok := idx.UnitBits[u][h]; ok {
					bits = b
				}
			}
			flags := idx.TermFlags[h]
			if u < len(idx.UnitFlags) && idx.UnitFlags[u] != nil {
				if fl, ok := idx.UnitFlags[u][h]; ok {
					flags = fl
				}
			}
			grp := h
			if u < len(idx.UnitGroups) && idx.UnitGroups[u] != nil && idx.UnitGroups[u][h] != 0 {
				grp = idx.UnitGroups[u][h]
			}
			var role TermRole
			var extFlags uint16
			if u < len(idx.UnitRoleExt) && idx.UnitRoleExt[u] != nil {
				role, extFlags = unpackTermRoleExt(idx.UnitRoleExt[u][h])
			}
			if u < len(idx.UnitExtFlags) && idx.UnitExtFlags[u] != nil {
				extFlags |= idx.UnitExtFlags[u][h]
			}
			var sent, slot uint16
			if docs[u].Positions != nil {
				if pl := docs[u].Positions[h]; len(pl) > 0 {
					sent = uint16(pl[0].Sentence + 1)
					slot = uint16(pl[0].Slot + 1)
				}
			}
			docs[u].Terms[h] = Atom{
				Hash:     h,
				Bits:     bits,
				Flags:    flags,
				Role:     role,
				ExtFlags: extFlags,
				Group:    grp,
				Sent:     sent,
				Slot:     slot,
			}
			docs[u].Counts[h] = int(p.Occ)
		}
	}
	return docs
}

func (idx *Index) AppendDocs(docs []Doc) {
	if idx == nil || len(docs) == 0 {
		return
	}
	nextGen := idx.stateGen + 1
	existing := idx.ExportDocs()
	combined := make([]Doc, 0, len(existing)+len(docs))
	combined = append(combined, existing...)
	combined = append(combined, docs...)
	rebuilt := BuildIndex(combined)
	rebuilt.stateGen = nextGen
	*idx = *rebuilt
}

func (idx *Index) DeleteDocs(unitIDs ...string) int {
	if idx == nil || idx.N == 0 || len(unitIDs) == 0 {
		return 0
	}
	delSet := make(map[string]struct{}, len(unitIDs))
	for _, id := range unitIDs {
		if id != "" {
			delSet[id] = struct{}{}
		}
	}
	if len(delSet) == 0 {
		return 0
	}
	nextGen := idx.stateGen + 1
	existing := idx.ExportDocs()
	kept := make([]Doc, 0, len(existing))
	removed := 0
	for _, d := range existing {
		if _, drop := delSet[d.UnitID]; drop {
			removed++
			continue
		}
		kept = append(kept, d)
	}
	if removed == 0 {
		return 0
	}
	rebuilt := BuildIndex(kept)
	rebuilt.stateGen = nextGen
	*idx = *rebuilt
	return removed
}
