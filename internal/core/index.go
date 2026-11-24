// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package core

import (
	"math"
	"slices"
	"sort"
)

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
	sortInt64Asc(times)
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
	csrPostingUnits   func(hash uint64, fn func(unit int32))
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

const (
	// [calibrated] HighDFFraction — share of N (dimensionless) above which a term is
	// treated as high-frequency background rather than a specific content anchor.
	// Fitted on the reference corpus (≈13.53%); the value coincides with e^-2 within
	// one ulp but is an empirical bound, not a derived quantity. Recalibrate when the
	// reference corpus or tokenizer changes; do not re-derive it from e.
	HighDFFraction = 0.1353352832366127
	// [calibrated] minHighDFFloor — absolute document-count floor that keeps the
	// high-DF threshold non-degenerate for micro-corpora (n < e^4 ≈ 54.6 documents;
	// above that the ratio branch is analytically inert). The value coincides with e^2
	// but is an engineering bound; keep the literal (eulerNumber*eulerNumber evaluates
	// one ulp lower) and do not single-source it from HighDFFraction (1/HighDFFraction
	// also lands one ulp below).
	minHighDFFloor = 7.38905609893065
)

func HighDFThresholdForN(n int) int {
	return int(math.Max(minHighDFFloor, float64(n)*HighDFFraction))
}

// highDFThresholdGuarded 保证被判词项的高 DF 阈值至少为 1：n·fraction 在小 n 上经
// int() 截断可下溢到 0，而 df>=1 恒大于 0，会把微簿记的全部词项错置为非背景。
func highDFThresholdGuarded(n int) int {
	if t := HighDFThresholdForN(n); t >= 1 {
		return t
	}
	return 1
}

func (idx *Index) HighDFThreshold() int {
	if idx == nil {
		return highDFThresholdGuarded(0)
	}
	return highDFThresholdGuarded(idx.N)
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
	// UnitExtFlags is the only authoritative carrier of the high 13 ExtFlags
	// bits (roleExt in CSR persists just Role + the low 3 bits), so it is
	// fidelity-bearing and must survive the transient-map release.
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

// ForEachPostingUnit calls fn for every unit carrying term hash. It serves both
// the in-memory inverted map and disk-opened indexes whose heap postings were
// released (CSR slices via csrPostingUnits), so query-time readers such as the
// ordinal span resolvers observe identical posting populations in both modes.
func (idx *Index) ForEachPostingUnit(hash uint64, fn func(unit int32)) {
	if idx == nil || fn == nil {
		return
	}
	if idx.Postings != nil {
		for _, p := range idx.Postings[hash] {
			fn(p.Unit)
		}
		return
	}
	if idx.csrPostingUnits != nil {
		idx.csrPostingUnits(hash, fn)
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
		idx.UnitMeta[unit] = doc.Meta()
		in := indexDocInserter{idx: idx, unit: int32(unit), termCap: len(doc.Terms)}
		in.explicitPos = len(doc.Positions) > 0
		if in.explicitPos {
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
			in.addTerm(hash, doc.Counts[hash], atom.Bits, atom.Flags, atom.Group, atom.Role, atom.ExtFlags, atom.Sent, atom.Slot)
		}
	}
	idx.finalizeMasses()
	return idx
}

// indexDocInserter applies the single per-term insertion rule shared by the
// in-memory BuildIndex path and the CSR snapshot rebuild path. Keeping the
// postings/DF/flags/role bookkeeping here is what makes append-from-CSR
// bit-identical instead of a second index implementation.
type indexDocInserter struct {
	idx         *Index
	unit        int32
	explicitPos bool
	termCap     int
}

func (in *indexDocInserter) addTerm(hash uint64, occ int, bits uint16, flags uint8, group uint64, role TermRole, extFlags uint16, sent, slot uint16) {
	idx := in.idx
	unit := in.unit
	if occ < 1 {
		occ = 1
	} else if occ > math.MaxInt32 {
		occ = math.MaxInt32
	}
	idx.Postings[hash] = append(idx.Postings[hash], Posting{Unit: unit, Occ: int32(occ)})
	idx.DF[hash]++
	if !in.explicitPos && sent > 0 && slot > 0 {
		if idx.UnitPositions[unit] == nil {
			idx.UnitPositions[unit] = make(map[uint64][]TermPosition, in.termCap)
		}
		idx.UnitPositions[unit][hash] = []TermPosition{{Sentence: int(sent - 1), Slot: int(slot - 1)}}
	}
	if idx.UnitBits[unit] == nil {
		idx.UnitBits[unit] = make(map[uint64]uint16, in.termCap)
	}
	if bits > 0 {
		if bits > idx.TermBits[hash] {
			idx.TermBits[hash] = bits
		}
		if bits > idx.UnitBits[unit][hash] {
			idx.UnitBits[unit][hash] = bits
		}
	} else if _, exists := idx.UnitBits[unit][hash]; !exists {
		idx.UnitBits[unit][hash] = 0
	}
	if idx.UnitFlags[unit] == nil {
		idx.UnitFlags[unit] = make(map[uint64]uint8, in.termCap)
	}
	idx.UnitFlags[unit][hash] |= flags
	if flags != 0 {
		idx.TermFlags[hash] |= flags
	}
	if group != 0 {
		if idx.UnitGroups[unit] == nil {
			idx.UnitGroups[unit] = make(map[uint64]uint64, in.termCap)
		}
		idx.UnitGroups[unit][hash] = group
	}
	if re := PackTermRoleExt(role, extFlags); re != 0 {
		if idx.UnitRoleExt[unit] == nil {
			idx.UnitRoleExt[unit] = make(map[uint64]uint8, in.termCap)
		}
		idx.UnitRoleExt[unit][hash] = re
	}
	if extFlags&^0x07 != 0 {
		if idx.UnitExtFlags[unit] == nil {
			idx.UnitExtFlags[unit] = make(map[uint64]uint16, 4)
		}
		idx.UnitExtFlags[unit][hash] |= extFlags
	}
}

// buildIndexWithDelta rebuilds the index from the live CSR snapshot plus the
// delta docs, without materializing every old unit as a Doc (the ExportDocs
// step). It returns nil when the engine has no cluster state to read, so the
// caller can fall back to the ExportDocs path. Scores stay bit-identical
// because both paths feed the same indexDocInserter rule and finalizeMasses.
func (e *Engine) buildIndexWithDelta(delta []Doc) *Index {
	return e.buildIndexWithDeltaDrop(delta, nil)
}

// buildIndexWithDeltaDrop is the single CSR rebuild entry for appends and
// deletions: delta docs override same-ID old units, drop IDs are excluded from
// both sides. The rebuilt index is exactly what BuildIndex would produce over
// the surviving document set (same inserter and finalizeMasses).
func (e *Engine) buildIndexWithDeltaDrop(delta []Doc, drop map[string]struct{}) *Index {
	if e == nil || e.Index == nil || e.Index.N == 0 {
		return nil
	}
	// The CSR snapshot covers the base index only; an overlay carries documents
	// that are not part of it, so those mutations keep the ExportDocs path.
	if e.HasDelta() {
		return nil
	}
	// 追加式快速路径：新单元 ID 全部排在既有单元之后且无删除时，单元下标不变，
	// 可整段共享旧索引的逐单元映射，只按词追加倒排；与全量重建逐位一致。
	if drop == nil && uniqueDeltaIDs(delta) && appendAtEndApplicable(e.Index, delta) {
		return buildIndexAppendAtEnd(e.Index, delta)
	}
	// 惰性物化：基座缺位或落后于当前索引时，直接以当前索引为源重建，
	// 避免为一次写入触发整个簇状态的全库物化。
	base := e.baseClusterState()
	if indexFromIndexApplicable(e.Index) && (base == nil || len(base.docRowPtr) != e.Index.N+1) {
		return buildIndexFromIndex(e.Index, delta, drop)
	}
	st := e.ensureClusterState()
	if st == nil || len(st.docRowPtr) != e.Index.N+1 {
		return nil
	}
	latestDelta := make(map[string]Doc, len(delta))
	for _, doc := range delta {
		if doc.UnitID == "" {
			continue
		}
		if drop != nil {
			if _, gone := drop[doc.UnitID]; gone {
				continue
			}
		}
		latestDelta[doc.UnitID] = doc
	}
	idSet := make(map[string]struct{}, e.Index.N+len(latestDelta))
	for _, id := range e.Index.UnitIDs {
		if id == "" {
			continue
		}
		if drop != nil {
			if _, gone := drop[id]; gone {
				continue
			}
		}
		idSet[id] = struct{}{}
	}
	for id := range latestDelta {
		idSet[id] = struct{}{}
	}
	ids := make([]string, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	old := e.Index
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
	for unit, rawID := range ids {
		id := rawID
		if doc, ok := latestDelta[id]; ok {
			idx.UnitOf[id] = int32(unit)
			idx.UnitMeta[unit] = doc.Meta()
			in := indexDocInserter{idx: idx, unit: int32(unit), termCap: len(doc.Terms)}
			in.explicitPos = len(doc.Positions) > 0
			if in.explicitPos {
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
				in.addTerm(hash, doc.Counts[hash], atom.Bits, atom.Flags, atom.Group, atom.Role, atom.ExtFlags, atom.Sent, atom.Slot)
			}
			continue
		}
		oldU, ok := old.LookupUnit(id)
		if !ok {
			return nil
		}
		// Clone mmap-backed IDs before the caller may Close the old segment.
		id = string([]byte(rawID))
		idx.UnitIDs[unit] = id
		idx.UnitOf[id] = int32(unit)
		idx.UnitMeta[unit] = cloneSnapshotDocMeta(old.UnitMeta[oldU])
		entries := st.docEntriesOf(oldU)
		var posMap map[uint64][]TermPosition
		for _, entry := range entries {
			packed := st.entryPositionsOf(entry)
			if len(packed) == 0 {
				continue
			}
			if posMap == nil {
				posMap = make(map[uint64][]TermPosition, len(entries))
			}
			pl := make([]TermPosition, len(packed))
			for i, pp := range packed {
				s, sl := int(pp>>16), int(pp&0xFFFF)
				if st.widePos != nil {
					if wp, okPos := st.widePos[entry.posOff+uint32(i)]; okPos {
						s, sl = wp.Sentence, wp.Slot
					}
				}
				pl[i] = TermPosition{Sentence: s, Slot: sl}
			}
			posMap[entry.id] = pl
		}
		if len(posMap) > 0 {
			idx.UnitPositions[unit] = posMap
		}
		in := indexDocInserter{idx: idx, unit: int32(unit), termCap: len(entries)}
		in.explicitPos = len(posMap) > 0
		var hiExt map[uint64]uint16
		if int(oldU) < len(old.UnitExtFlags) {
			hiExt = old.UnitExtFlags[oldU]
		}
		for _, entry := range entries {
			role, extFlags := UnpackTermRoleExt(entry.roleExt)
			if hiExt != nil {
				extFlags |= hiExt[entry.id]
			}
			in.addTerm(entry.id, int(entry.tf), entry.bits, entry.flags, entry.group, role, extFlags, 0, 0)
		}
	}
	idx.finalizeMasses()
	return idx
}

// cloneSnapshotDocMeta detaches a CSR/mmap-backed metadata record from the
// mapped segment so the rebuilt index survives Close().
func cloneSnapshotDocMeta(m DocMeta) DocMeta {
	m.Layer = string([]byte(m.Layer))
	if len(m.CitedIDs) == 0 {
		m.CitedIDs = nil
		return m
	}
	cited := make([]string, len(m.CitedIDs))
	for i, id := range m.CitedIDs {
		cited[i] = string([]byte(id))
	}
	m.CitedIDs = cited
	return m
}

// docTermMass carries the precomputed (hash, mass) pair used to order one document's
// term list without repeated map lookups inside the comparator.
type docTermMass struct {
	h    uint64
	mass float64
}

// docTermMassCompare orders by mass descending then hash ascending.
func docTermMassCompare(a, b docTermMass) int {
	if a.mass != b.mass {
		if a.mass > b.mass {
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
}

type docTermMassSort []docTermMass

func (s docTermMassSort) Len() int { return len(s) }
func (s docTermMassSort) Less(i, j int) bool {
	if s[i].mass != s[j].mass {
		return s[i].mass > s[j].mass
	}
	return s[i].h < s[j].h
}
func (s docTermMassSort) Swap(i, j int) { s[i], s[j] = s[j], s[i] }

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
	// Per-document term order is (mass desc, hash asc); mass is a strict monotone
	// function of df, so precomputing {hash, mass} once per term avoids two map
	// lookups per comparison. The reuse scratch keeps the pass allocation-free.
	docTermScratch := make([]docTermMass, 0, 2048)
	for unit := range idx.DocTerms {
		atoms := idx.DocTerms[unit]
		if cap(docTermScratch) < len(atoms) {
			docTermScratch = make([]docTermMass, len(atoms))
		} else {
			docTermScratch = docTermScratch[:len(atoms)]
		}
		for i, h := range atoms {
			docTermScratch[i] = docTermMass{h: h, mass: idx.Mass[h]}
		}
		slices.SortFunc(docTermScratch, docTermMassCompare)
		for i := range docTermScratch {
			atoms[i] = docTermScratch[i].h
			idx.DocMass[unit] += docTermScratch[i].mass
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
			// slotPrimTied 标记「伴随性与 bits 双双平手」的位置：此时没有物理上更
			// 优的宿主，任何择一都只能靠哈希值，故一律不指派（见下方消费者）。
			slotPrimTied := make(map[TermPosition]bool, 0)
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
					if _, exists := slotPrimary[pos]; !exists {
						slotPrimary[pos] = h
						slotPrimBits[pos] = bits
						slotPrimComp[pos] = isComp
						continue
					}
					prevComp := slotPrimComp[pos]
					prevBits := slotPrimBits[pos]
					switch {
					case prevComp && !isComp:
						slotPrimary[pos] = h
						slotPrimBits[pos] = bits
						slotPrimComp[pos] = isComp
						slotPrimTied[pos] = false
					case prevComp == isComp && bits > prevBits:
						slotPrimary[pos] = h
						slotPrimBits[pos] = bits
						slotPrimComp[pos] = isComp
						slotPrimTied[pos] = false
					case prevComp == isComp && bits == prevBits:
						slotPrimTied[pos] = true
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
						// 并列（伴随性与 bits 全等）时没有物理上更优的宿主，
						// 任一选择都只能由哈希值决定；此处不指派，保留自身为族。
						if prim, ok := slotPrimary[minPos]; ok && prim != 0 && !slotPrimTied[minPos] {
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
	sortUint64Asc(keys)
	var bitSum float64
	for _, h := range keys {
		bitSum += float64(idx.TermBits[h])
	}
	if len(keys) > 0 {
		idx.MeanBits = bitSum / float64(len(keys))
	}
	// 簇状态新鲜度由「索引指针 + 世代号」精确判定（见 ensureClusterState）；
	// PostingDigest 改为按需计算（.zcsr 落盘与 ComputeCorpusDigest 需要时现算），
	// 不再每次重建都对全量倒排再哈希一遍。
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
	sortUint64Asc(tKeys)
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
					sortUint32Asc(posScratch)
				}
				if len(overflowPos) > 1 {
					sortUint64Asc(overflowPos)
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
				role, extFlags = UnpackTermRoleExt(idx.UnitRoleExt[u][h])
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

// CorpusSize 返回语料单元总数 N（nil 接收者返回 0），供只读倒排窄视图消费。
func (idx *Index) CorpusSize() int {
	if idx == nil {
		return 0
	}
	return idx.N
}

// UnitTime 返回单元的 occurredAt/narratedAt/ingestedAt 三时钟坐标；nil 接收者或越界单元返回 ok=false。
func (idx *Index) UnitTime(unit int32) (occurredAt, narratedAt, ingestedAt int64, ok bool) {
	if idx == nil || unit < 0 || int(unit) >= len(idx.UnitMeta) {
		return 0, 0, 0, false
	}
	meta := idx.UnitMeta[unit]
	return meta.OccurredAt, meta.NarratedAt, meta.IngestedAt, true
}

// FinalizeMasses computes IDF masses and per-document term slices after storage loading.
func (idx *Index) FinalizeMasses() {
	idx.finalizeMasses()
}
