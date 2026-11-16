// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cluster

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"hash/fnv"
	"math"
	"os"
	"path/filepath"
	"sort"
	"unsafe"
)

const (
	zcsrMagic = "ZCSR"
	// v2: the precomputed day-cos floor switched from an unconditional 1/e
	// envelope floor to the collision-surprisal-scaled envelope floor, so v1
	// snapshots carry stale dayCosVals and must be rebuilt.
	// v3: the subject-cos warm domain switched from calendar-day buckets to
	// endogenous episodic components (phase-free gap split), so v2 snapshots
	// carry a stale dayCosKeys population and must be rebuilt.
	zcsrVersion      = uint16(3)
	zcsrKindRM       = uint16(2)
	zcsrRMHeaderSize = 168
	zcsrMaxBytes     = int64(4) << 30
)

var (
	// ErrInvalidZCSR 表示 .zcsr 磁盘段格式、边界或偏移表非法。
	ErrInvalidZCSR = errors.New("rm/zcsr: invalid segment format")
	// ErrCorruptZCSR 表示 .zcsr 磁盘段头或载荷 CRC32C 校验和不匹配。
	ErrCorruptZCSR = errors.New("rm/zcsr: segment checksum mismatch")

	zcsrCRCTable = crc32.MakeTable(crc32.Castagnoli)
)

func align8(n int) int {
	return (n + 7) &^ 7
}

func isNativeLittleEndian() bool {
	var x uint16 = 1
	return *(*byte)(unsafe.Pointer(&x)) == 1
}

// ComputeCorpusDigest 计算索引 idx 的确定性 64-bit FNV-1a 内容摘要（CorpusDigest）：
// 覆盖文档规模 N、升序 UnitIDs、完整单元元数据（OccurredAt/NarratedAt/IngestedAt/AuthorHash/DocHash/BlockHash/LineStart/LineEnd/Layer）、
// 升序词表 TermKeys 及其 DF/Bits/Flags，以及词项到文档的倒排归属、词频、词族、角色与句槽位置拓扑摘要 PostingDigest。
// 无论索引由内存 BuildIndex、SQLite LoadIndex 还是 OpenDiskClusterEngine 加载，
// 同一语料快照的 ComputeCorpusDigest 恒 100% 逐位全等；文档间词项交换、位置/词频/元数据变更或增删改任一词项/文档时摘要即刻变化。
func ComputeCorpusDigest(idx *Index) uint64 {
	if idx == nil || idx.N <= 0 {
		return 0
	}
	// Citation sections extend the digest only when the corpus actually carries
	// citation edges: zero-citation corpora keep their historical digest value,
	// so every existing .zcsr segment stays loadable without rebuild.
	anyCited := false
	for u := 0; u < idx.N && u < len(idx.UnitMeta); u++ {
		if len(idx.UnitMeta[u].CitedIDs) > 0 {
			anyCited = true
			break
		}
	}
	h := fnv.New64a()
	var b8 [8]byte
	binary.LittleEndian.PutUint64(b8[:], uint64(idx.N))
	_, _ = h.Write(b8[:])
	for u := 0; u < idx.N && u < len(idx.UnitIDs); u++ {
		id := idx.UnitIDs[u]
		binary.LittleEndian.PutUint64(b8[:], uint64(len(id)))
		_, _ = h.Write(b8[:])
		_, _ = h.Write([]byte(id))
		var m DocMeta
		if u < len(idx.UnitMeta) {
			m = idx.UnitMeta[u]
		}
		binary.LittleEndian.PutUint64(b8[:], uint64(m.OccurredAt))
		_, _ = h.Write(b8[:])
		binary.LittleEndian.PutUint64(b8[:], uint64(m.NarratedAt))
		_, _ = h.Write(b8[:])
		binary.LittleEndian.PutUint64(b8[:], uint64(m.IngestedAt))
		_, _ = h.Write(b8[:])
		binary.LittleEndian.PutUint64(b8[:], m.AuthorHash)
		_, _ = h.Write(b8[:])
		binary.LittleEndian.PutUint64(b8[:], m.DocHash)
		_, _ = h.Write(b8[:])
		binary.LittleEndian.PutUint64(b8[:], m.BlockHash)
		_, _ = h.Write(b8[:])
		binary.LittleEndian.PutUint64(b8[:], (uint64(uint32(m.LineStart))<<32)|uint64(uint32(m.LineEnd)))
		_, _ = h.Write(b8[:])
		binary.LittleEndian.PutUint64(b8[:], uint64(len(m.Layer)))
		_, _ = h.Write(b8[:])
		if len(m.Layer) > 0 {
			_, _ = h.Write([]byte(m.Layer))
		}
		if anyCited {
			binary.LittleEndian.PutUint64(b8[:], uint64(len(m.CitedIDs)))
			_, _ = h.Write(b8[:])
			for _, cid := range m.CitedIDs {
				binary.LittleEndian.PutUint64(b8[:], uint64(len(cid)))
				_, _ = h.Write(b8[:])
				_, _ = h.Write([]byte(cid))
			}
		}
	}
	var tKeys []uint64
	if len(idx.TermKeys) > 0 {
		tKeys = idx.TermKeys
	} else if len(idx.DF) > 0 {
		tKeys = make([]uint64, 0, len(idx.DF))
		for k := range idx.DF {
			tKeys = append(tKeys, k)
		}
		sort.Slice(tKeys, func(i, j int) bool { return tKeys[i] < tKeys[j] })
	}
	binary.LittleEndian.PutUint64(b8[:], uint64(len(tKeys)))
	_, _ = h.Write(b8[:])
	for _, tk := range tKeys {
		binary.LittleEndian.PutUint64(b8[:], tk)
		_, _ = h.Write(b8[:])
		binary.LittleEndian.PutUint64(b8[:], uint64(idx.TermDF(tk)))
		_, _ = h.Write(b8[:])
		binary.LittleEndian.PutUint64(b8[:], (uint64(idx.TermBitsOf(tk))<<8)|uint64(idx.TermFlagsOf(tk)))
		_, _ = h.Write(b8[:])
	}
	postDigest := idx.PostingDigest
	if len(idx.Postings) > 0 {
		postDigest = idx.computePostingDigestFromMap()
	}
	binary.LittleEndian.PutUint64(b8[:], postDigest)
	_, _ = h.Write(b8[:])
	return h.Sum64()
}

func computePostingDigestFromCSR(termKeys []uint64, termRowPtr []uint32, postingPool []rmPostingEntry, docRowPtr []uint32, docEntriesFlat []rmDocEntry, posPool []uint32, widePos map[uint32]TermPosition) uint64 {
	if len(termKeys) == 0 || len(postingPool) == 0 {
		return 0
	}
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
	h := uint64(offset64)
	var overflowPos []uint64
	for i, tk := range termKeys {
		s, e := termRowPtr[i], termRowPtr[i+1]
		posts := postingPool[s:e]
		h = mix(h, tk)
		h = mix(h, uint64(len(posts)))
		for _, p := range posts {
			var occ int32 = 1
			group := tk
			var bits uint16
			var fl, re uint8
			var posSlice []uint32
			var posOff uint32
			if int(p.unit)+1 < len(docRowPtr) {
				entryPos := int(docRowPtr[p.unit]) + int(p.entry)
				if entryPos >= 0 && entryPos < len(docEntriesFlat) {
					ent := docEntriesFlat[entryPos]
					if ent.tf > 0 {
						occ = ent.tf
					}
					if ent.group != 0 {
						group = ent.group
					}
					bits = ent.bits
					fl = ent.flags
					re = ent.roleExt
					posOff = ent.posOff
					if ent.posLen > 0 && int(ent.posOff+ent.posLen) <= len(posPool) {
						posSlice = posPool[ent.posOff : ent.posOff+ent.posLen]
					}
				}
			}
			h = mix(h, (uint64(uint32(p.unit))<<32)|uint64(uint32(occ)))
			h = mix(h, group)
			h = mix(h, (uint64(bits)<<16)|(uint64(fl)<<8)|uint64(re))
			h = mix(h, uint64(len(posSlice)))
			for _, pp := range posSlice {
				h = mix(h, uint64(pp))
			}
			if len(widePos) > 0 && len(posSlice) > 0 {
				overflowPos = overflowPos[:0]
				for pi := range posSlice {
					if wp, ok := widePos[posOff+uint32(pi)]; ok {
						overflowPos = append(overflowPos, (uint64(uint32(wp.Sentence))<<32)|uint64(uint32(wp.Slot)))
					}
				}
				if len(overflowPos) > 1 {
					sort.Slice(overflowPos, func(a, b int) bool { return overflowPos[a] < overflowPos[b] })
				}
				for _, op := range overflowPos {
					h = mix(h, op)
				}
			}
		}
	}
	return h
}

// WriteDiskClusterCSR 将 Engine 的四步双簇 CSR 状态（含短语倒排、词项倒排、文档词项、
// 32-bit 位置池、时空邻域与主题稀疏向量）序列化为 8 字节对齐、带头体双 CRC32C 校验的
// .zcsr 只读磁盘段文件（tmp 写入 -> fsync -> rename 原子替换）。
func WriteDiskClusterCSR(path string, eng *Engine) error {
	if eng == nil || eng.Index == nil {
		return fmt.Errorf("%w: nil Engine or Index", ErrInvalidZCSR)
	}
	st := eng.ensureClusterState()
	idx := eng.Index
	n := idx.N
	v := len(st.termKeys)
	numEntries := len(st.docEntriesFlat)
	numPos := len(st.posPool)
	numPhrases := len(st.phraseKeys)
	numPhraseUnits := len(st.phraseUnits)
	numNeighbors := len(st.neighborPool)
	numSubj := len(st.subjIDsPool)

	if len(st.docRowPtr) != n+1 || len(st.termRowPtr) != v+1 || len(st.postingPool) != numEntries ||
		len(st.phraseRowPtr) != numPhrases+1 || len(st.neighborRowPtr) != n+1 || len(st.subjRowPtr) != n+1 ||
		len(st.subjWeightsPool) != numSubj || len(st.fastDocs) != n || len(st.clusters) != n ||
		len(st.refTimes) != n || len(st.ordPos) != n {
		return fmt.Errorf("%w: inconsistent CSR slice lengths in rmClusterState", ErrInvalidZCSR)
	}

	type dayCosPair struct {
		key uint64
		val float64
	}
	dayCosList := make([]dayCosPair, 0, len(st.dayCos))
	for k, val := range st.dayCos {
		if val > 0 {
			dayCosList = append(dayCosList, dayCosPair{key: k, val: val})
		}
	}
	sort.Slice(dayCosList, func(i, j int) bool { return dayCosList[i].key < dayCosList[j].key })
	numDayCos := len(dayCosList)

	var idBlobLen int
	for u, id := range idx.UnitIDs {
		idBlobLen += len(id)
		if u < len(idx.UnitMeta) {
			m := idx.UnitMeta[u]
			idBlobLen += len(m.Layer)
			if m.LineStart != int(int32(m.LineStart)) || m.LineEnd != int(int32(m.LineEnd)) {
				idBlobLen += 16
			}
			if len(m.CitedIDs) > 0 {
				// citation record: u32 count + per-ID (u32 len + bytes)
				idBlobLen += 4
				for _, cid := range m.CitedIDs {
					idBlobLen += 4 + len(cid)
				}
			}
		}
	}
	type widePosRecord struct {
		posIdx uint32
		pos    TermPosition
	}
	var widePosList []widePosRecord
	if len(st.widePos) > 0 && n > 0 {
		widePosList = make([]widePosRecord, 0, len(st.widePos))
		for k, wp := range st.widePos {
			widePosList = append(widePosList, widePosRecord{posIdx: k, pos: wp})
		}
		sort.Slice(widePosList, func(i, j int) bool { return widePosList[i].posIdx < widePosList[j].posIdx })
		idBlobLen += 8 + len(widePosList)*24
	}

	payloadSize := 0
	payloadSize += align8((n + 1) * 4)          // docRowPtr []uint32
	payloadSize += numEntries * 32              // docEntriesFlat []rmDocEntry
	payloadSize += align8(numPos * 4)           // posPool []uint32
	payloadSize += v * 8                        // termKeys []uint64
	payloadSize += align8((v + 1) * 4)          // termRowPtr []uint32
	payloadSize += numEntries * 8               // postingPool []rmPostingEntry
	payloadSize += align8(v * 4)                // TermDF []int32
	payloadSize += v * 8                        // TermMass []float64
	payloadSize += align8(v * 2)                // TermBits []uint16
	payloadSize += align8(v)                    // TermFlags []uint8
	payloadSize += v * 8                        // termEffWeight []float64
	payloadSize += v * 8                        // termProperRatio []float64
	payloadSize += v * 8                        // termParentGroup []uint64
	payloadSize += align8(v)                    // termIsPrimary []uint8
	payloadSize += numPhrases * 16              // phraseKeys []rmDyad
	payloadSize += align8((numPhrases + 1) * 4) // phraseRowPtr []uint32
	payloadSize += align8(numPhraseUnits * 4)   // phraseUnits []int32
	payloadSize += align8(n * 4)                // clusters []int32
	payloadSize += align8((n + 1) * 4)          // neighborRowPtr []uint32
	payloadSize += align8(numNeighbors * 4)     // neighborPool []int32
	payloadSize += n * 8                        // refTimes []int64
	payloadSize += align8(n * 4)                // ordPos []int32
	payloadSize += n * 24                       // fastDocs []rmFastDocStruct
	payloadSize += align8((n + 1) * 4)          // subjRowPtr []uint32
	payloadSize += numSubj * 8                  // subjIDsPool []uint64
	payloadSize += numSubj * 8                  // subjWeightsPool []float64
	payloadSize += numDayCos * 8                // dayCosKeys []uint64
	payloadSize += numDayCos * 8                // dayCosVals []float64
	payloadSize += n * 64                       // unitMetaFull (OccurredAt, NarratedAt, IngestedAt, AuthorHash, DocHash, BlockHash, LineStart, LineEnd, LayerOff, LayerLen)
	payloadSize += align8((n + 1) * 4)          // unitIDOffsets []uint32
	payloadSize += align8(idBlobLen)            // unitID + Layer string blob

	if int64(payloadSize) > zcsrMaxBytes {
		return fmt.Errorf("%w: payload %d exceeds 4 GiB limit", ErrInvalidZCSR, payloadSize)
	}

	buf := make([]byte, zcsrRMHeaderSize+payloadSize)
	pos := zcsrRMHeaderSize

	writeU32Aligned := func(arr []uint32) {
		start := pos
		for _, x := range arr {
			binary.LittleEndian.PutUint32(buf[pos:pos+4], x)
			pos += 4
		}
		pos = start + align8(len(arr)*4)
	}
	writeI32Aligned := func(arr []int32) {
		start := pos
		for _, x := range arr {
			binary.LittleEndian.PutUint32(buf[pos:pos+4], uint32(x))
			pos += 4
		}
		pos = start + align8(len(arr)*4)
	}
	writeU64 := func(arr []uint64) {
		for _, x := range arr {
			binary.LittleEndian.PutUint64(buf[pos:pos+8], x)
			pos += 8
		}
	}
	writeI64 := func(arr []int64) {
		for _, x := range arr {
			binary.LittleEndian.PutUint64(buf[pos:pos+8], uint64(x))
			pos += 8
		}
	}
	writeF64 := func(arr []float64) {
		for _, x := range arr {
			binary.LittleEndian.PutUint64(buf[pos:pos+8], math.Float64bits(x))
			pos += 8
		}
	}

	// 1. docRowPtr & docEntriesFlat & posPool
	writeU32Aligned(st.docRowPtr)
	for _, e := range st.docEntriesFlat {
		binary.LittleEndian.PutUint64(buf[pos:pos+8], e.id)
		binary.LittleEndian.PutUint64(buf[pos+8:pos+16], e.group)
		binary.LittleEndian.PutUint32(buf[pos+16:pos+20], e.posOff)
		binary.LittleEndian.PutUint32(buf[pos+20:pos+24], e.posLen)
		binary.LittleEndian.PutUint32(buf[pos+24:pos+28], uint32(e.tf))
		binary.LittleEndian.PutUint16(buf[pos+28:pos+30], e.bits)
		buf[pos+30] = e.flags
		buf[pos+31] = e.roleExt
		pos += 32
	}
	writeU32Aligned(st.posPool)

	// 2. termKeys, termRowPtr, postingPool, Index term tables, and cluster term attributes
	writeU64(st.termKeys)
	writeU32Aligned(st.termRowPtr)
	for _, p := range st.postingPool {
		binary.LittleEndian.PutUint32(buf[pos:pos+4], uint32(p.unit))
		binary.LittleEndian.PutUint32(buf[pos+4:pos+8], uint32(p.entry))
		pos += 8
	}
	dfStart := pos
	for _, h := range st.termKeys {
		binary.LittleEndian.PutUint32(buf[pos:pos+4], uint32(idx.TermDF(h)))
		pos += 4
	}
	pos = dfStart + align8(v*4)
	for _, h := range st.termKeys {
		m, _ := idx.TermMass(h)
		binary.LittleEndian.PutUint64(buf[pos:pos+8], math.Float64bits(m))
		pos += 8
	}
	bitsStart := pos
	for _, h := range st.termKeys {
		binary.LittleEndian.PutUint16(buf[pos:pos+2], idx.TermBitsOf(h))
		pos += 2
	}
	pos = bitsStart + align8(v*2)
	flagsStart := pos
	for i, h := range st.termKeys {
		buf[flagsStart+i] = idx.TermFlagsOf(h)
	}
	pos = flagsStart + align8(v)

	for _, h := range st.termKeys {
		w, _ := st.effWeightOf(h)
		binary.LittleEndian.PutUint64(buf[pos:pos+8], math.Float64bits(w))
		pos += 8
	}
	for _, h := range st.termKeys {
		binary.LittleEndian.PutUint64(buf[pos:pos+8], math.Float64bits(st.properRatioOf(h)))
		pos += 8
	}
	for _, h := range st.termKeys {
		pg, _ := st.parentGroupOf(h)
		binary.LittleEndian.PutUint64(buf[pos:pos+8], pg)
		pos += 8
	}
	primStart := pos
	for i, h := range st.termKeys {
		if st.isPrimaryOf(h) {
			buf[primStart+i] = 1
		}
	}
	pos = primStart + align8(v)

	// 3. phraseKeys, phraseRowPtr, phraseUnits
	for _, pk := range st.phraseKeys {
		binary.LittleEndian.PutUint64(buf[pos:pos+8], pk.a)
		binary.LittleEndian.PutUint64(buf[pos+8:pos+16], pk.b)
		pos += 16
	}
	writeU32Aligned(st.phraseRowPtr)
	writeI32Aligned(st.phraseUnits)

	// 4. Spacetime & FastDocs CSR
	writeI32Aligned(st.clusters)
	writeU32Aligned(st.neighborRowPtr)
	writeI32Aligned(st.neighborPool)
	writeI64(st.refTimes)
	writeI32Aligned(st.ordPos)
	for _, fd := range st.fastDocs {
		binary.LittleEndian.PutUint64(buf[pos:pos+8], math.Float64bits(fd.docTotal))
		binary.LittleEndian.PutUint64(buf[pos+8:pos+16], uint64(fd.day))
		binary.LittleEndian.PutUint64(buf[pos+16:pos+24], math.Float64bits(fd.subjNormSq))
		pos += 24
	}
	writeU32Aligned(st.subjRowPtr)
	writeU64(st.subjIDsPool)
	writeF64(st.subjWeightsPool)
	for _, kv := range dayCosList {
		binary.LittleEndian.PutUint64(buf[pos:pos+8], kv.key)
		pos += 8
	}
	for _, kv := range dayCosList {
		binary.LittleEndian.PutUint64(buf[pos:pos+8], math.Float64bits(kv.val))
		pos += 8
	}
	var totalUnitIDBytes uint32
	for _, id := range idx.UnitIDs {
		totalUnitIDBytes += uint32(len(id))
	}
	const (
		zcsrWideLineSpanFlag uint32 = 1 << 31
		zcsrWidePosPoolFlag  uint32 = 1 << 30
		zcsrCitedIDsFlag     uint32 = 1 << 29
	)
	layerCurOff := totalUnitIDBytes
	for u := 0; u < n; u++ {
		var m DocMeta
		if u < len(idx.UnitMeta) {
			m = idx.UnitMeta[u]
		}
		wideLine := m.LineStart != int(int32(m.LineStart)) || m.LineEnd != int(int32(m.LineEnd))
		var lOff, lLen uint32
		if len(m.Layer) > 0 || wideLine || len(m.CitedIDs) > 0 {
			lOff = layerCurOff
			lLen = uint32(len(m.Layer))
			layerCurOff += lLen
			if wideLine {
				lLen |= zcsrWideLineSpanFlag
				layerCurOff += 16
			}
			if len(m.CitedIDs) > 0 {
				lLen |= zcsrCitedIDsFlag
				layerCurOff += 4
				for _, cid := range m.CitedIDs {
					layerCurOff += 4 + uint32(len(cid))
				}
			}
		}
		if u == 0 && len(widePosList) > 0 {
			lLen |= zcsrWidePosPoolFlag
		}
		binary.LittleEndian.PutUint64(buf[pos:pos+8], uint64(m.OccurredAt))
		binary.LittleEndian.PutUint64(buf[pos+8:pos+16], uint64(m.NarratedAt))
		binary.LittleEndian.PutUint64(buf[pos+16:pos+24], uint64(m.IngestedAt))
		binary.LittleEndian.PutUint64(buf[pos+24:pos+32], m.AuthorHash)
		binary.LittleEndian.PutUint64(buf[pos+32:pos+40], m.DocHash)
		binary.LittleEndian.PutUint64(buf[pos+40:pos+48], m.BlockHash)
		binary.LittleEndian.PutUint32(buf[pos+48:pos+52], uint32(int32(m.LineStart)))
		binary.LittleEndian.PutUint32(buf[pos+52:pos+56], uint32(int32(m.LineEnd)))
		binary.LittleEndian.PutUint32(buf[pos+56:pos+60], lOff)
		binary.LittleEndian.PutUint32(buf[pos+60:pos+64], lLen)
		pos += 64
	}
	offStart := pos
	var strOff uint32
	for _, id := range idx.UnitIDs {
		binary.LittleEndian.PutUint32(buf[pos:pos+4], strOff)
		pos += 4
		strOff += uint32(len(id))
	}
	binary.LittleEndian.PutUint32(buf[pos:pos+4], strOff)
	pos = offStart + align8((n+1)*4)
	strStart := pos
	for _, id := range idx.UnitIDs {
		copy(buf[pos:], id)
		pos += len(id)
	}
	for u := 0; u < n; u++ {
		if u < len(idx.UnitMeta) {
			m := idx.UnitMeta[u]
			if len(m.Layer) > 0 {
				copy(buf[pos:], m.Layer)
				pos += len(m.Layer)
			}
			if m.LineStart != int(int32(m.LineStart)) || m.LineEnd != int(int32(m.LineEnd)) {
				binary.LittleEndian.PutUint64(buf[pos:pos+8], uint64(m.LineStart))
				binary.LittleEndian.PutUint64(buf[pos+8:pos+16], uint64(m.LineEnd))
				pos += 16
			}
			if len(m.CitedIDs) > 0 {
				binary.LittleEndian.PutUint32(buf[pos:pos+4], uint32(len(m.CitedIDs)))
				pos += 4
				for _, cid := range m.CitedIDs {
					binary.LittleEndian.PutUint32(buf[pos:pos+4], uint32(len(cid)))
					pos += 4
					copy(buf[pos:], cid)
					pos += len(cid)
				}
			}
		}
	}
	if len(widePosList) > 0 && n > 0 {
		binary.LittleEndian.PutUint64(buf[pos:pos+8], uint64(len(widePosList)))
		pos += 8
		for _, wr := range widePosList {
			binary.LittleEndian.PutUint64(buf[pos:pos+8], uint64(wr.posIdx))
			binary.LittleEndian.PutUint64(buf[pos+8:pos+16], uint64(wr.pos.Sentence))
			binary.LittleEndian.PutUint64(buf[pos+16:pos+24], uint64(wr.pos.Slot))
			pos += 24
		}
	}
	pos = strStart + align8(idBlobLen)

	if pos != len(buf) {
		return fmt.Errorf("%w: internal offset mismatch (%d != %d)", ErrInvalidZCSR, pos, len(buf))
	}

	bodyCRC := crc32.Checksum(buf[zcsrRMHeaderSize:], zcsrCRCTable)
	corpusDigest := ComputeCorpusDigest(idx)

	copy(buf[0:4], zcsrMagic)
	binary.LittleEndian.PutUint16(buf[4:6], zcsrVersion)
	binary.LittleEndian.PutUint16(buf[6:8], zcsrKindRM)
	binary.LittleEndian.PutUint64(buf[8:16], uint64(n))
	binary.LittleEndian.PutUint64(buf[16:24], uint64(v))
	binary.LittleEndian.PutUint64(buf[24:32], uint64(numEntries))
	binary.LittleEndian.PutUint64(buf[32:40], uint64(numPos))
	binary.LittleEndian.PutUint64(buf[40:48], uint64(numPhrases))
	binary.LittleEndian.PutUint64(buf[48:56], uint64(numPhraseUnits))
	binary.LittleEndian.PutUint64(buf[56:64], uint64(numNeighbors))
	binary.LittleEndian.PutUint64(buf[64:72], uint64(numSubj))
	binary.LittleEndian.PutUint64(buf[72:80], uint64(numDayCos))
	binary.LittleEndian.PutUint64(buf[80:88], uint64(st.clusterN))
	binary.LittleEndian.PutUint64(buf[88:96], math.Float64bits(st.meanGap))
	binary.LittleEndian.PutUint64(buf[96:104], math.Float64bits(st.muLogGap))
	binary.LittleEndian.PutUint64(buf[104:112], math.Float64bits(st.sigLogGap))
	binary.LittleEndian.PutUint64(buf[112:120], math.Float64bits(st.totalDaysM))
	binary.LittleEndian.PutUint64(buf[120:128], math.Float64bits(st.entryMeanBits))
	binary.LittleEndian.PutUint64(buf[128:136], math.Float64bits(st.sentenceEnergyMean))
	binary.LittleEndian.PutUint64(buf[136:144], uint64(idBlobLen))
	binary.LittleEndian.PutUint64(buf[144:152], uint64(payloadSize))
	binary.LittleEndian.PutUint64(buf[152:160], corpusDigest)
	headCRC := crc32.Checksum(buf[0:160], zcsrCRCTable)
	binary.LittleEndian.PutUint32(buf[160:164], headCRC)
	binary.LittleEndian.PutUint32(buf[164:168], bodyCRC)

	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(buf); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// OpenDiskClusterEngine 直接从 .zcsr 只读磁盘段文件零拷贝打开可独立检索的 Engine（无需 SQLite 或 BuildIndex）。
// 全部词表统计量、倒排、短语对、位置池与单元 ID 字符串均直接映射自 OS 页缓存（mmap），
// Go 堆仅分配极小切片头壳，实现毫秒级瞬时冷启动与近零堆常驻。
func OpenDiskClusterEngine(path string) (*Engine, error) {
	data, unmap, err := mapCSRFileReadOnly(path)
	if err != nil {
		return nil, err
	}
	idx := &Index{}
	st, err := parseDiskClusterCSR(data, idx, true)
	if err != nil {
		_ = unmap()
		return nil, err
	}
	eng := NewEngine(idx)
	eng.clusterState = st
	eng.mmapUnmap = unmap
	idx.csrFallbackExport = eng.ExportDocs
	return eng, nil
}

// AttachDiskClusterCSR 将磁盘上的 .zcsr 段文件通过只读 mmap 零拷贝挂载到 eng 上：
// 所有大体积 CSR 数组（docEntriesFlat、posPool、postingPool、phraseKeys、phraseRowPtr、
// phraseUnits、neighborPool、fastDocs、subjIDsPool、subjWeightsPool）直接映射在 OS 页缓存中，
// 不占用 Go GC 堆；若 releaseHeapDuplicates 为 true，同时释放 Index 上的过渡态逐单元 Map。
func AttachDiskClusterCSR(path string, eng *Engine, releaseHeapDuplicates bool) error {
	if eng == nil || eng.Index == nil {
		return fmt.Errorf("%w: nil Engine or Index", ErrInvalidZCSR)
	}
	data, unmap, err := mapCSRFileReadOnly(path)
	if err != nil {
		return err
	}
	refreshStrings := eng.mmapUnmap != nil || len(eng.Index.UnitIDs) == 0
	st, err := parseDiskClusterCSR(data, eng.Index, refreshStrings)
	if err != nil {
		_ = unmap()
		return err
	}
	if eng.mmapUnmap != nil {
		_ = eng.mmapUnmap()
	}
	eng.clusterState = st
	eng.mmapUnmap = unmap
	eng.Index.csrFallbackExport = eng.ExportDocs
	if releaseHeapDuplicates {
		eng.Index.ReleaseTransientUnitMaps()
	}
	return nil
}

func parseDiskClusterCSR(data []byte, idx *Index, refreshStrings bool) (*rmClusterState, error) {
	if len(data) < zcsrRMHeaderSize {
		return nil, fmt.Errorf("%w: file shorter than header", ErrInvalidZCSR)
	}
	if string(data[0:4]) != zcsrMagic {
		return nil, fmt.Errorf("%w: bad magic", ErrInvalidZCSR)
	}
	ver := binary.LittleEndian.Uint16(data[4:6])
	kind := binary.LittleEndian.Uint16(data[6:8])
	if ver != zcsrVersion || kind != zcsrKindRM {
		return nil, fmt.Errorf("%w: unsupported version=%d kind=%d", ErrInvalidZCSR, ver, kind)
	}
	wantHeadCRC := binary.LittleEndian.Uint32(data[160:164])
	if crc32.Checksum(data[0:160], zcsrCRCTable) != wantHeadCRC {
		return nil, fmt.Errorf("%w: header CRC mismatch", ErrCorruptZCSR)
	}
	idBlobLen := int(binary.LittleEndian.Uint64(data[136:144]))
	payloadSize := int(binary.LittleEndian.Uint64(data[144:152]))
	segCorpusDigest := binary.LittleEndian.Uint64(data[152:160])
	if payloadSize < 0 || zcsrRMHeaderSize+payloadSize != len(data) {
		return nil, fmt.Errorf("%w: payload size mismatch", ErrInvalidZCSR)
	}
	wantBodyCRC := binary.LittleEndian.Uint32(data[164:168])
	if crc32.Checksum(data[zcsrRMHeaderSize:], zcsrCRCTable) != wantBodyCRC {
		return nil, fmt.Errorf("%w: body CRC mismatch", ErrCorruptZCSR)
	}

	n := int(binary.LittleEndian.Uint64(data[8:16]))
	if idx.N != 0 && n != idx.N {
		return nil, fmt.Errorf("%w: doc count mismatch (seg=%d idx=%d)", ErrInvalidZCSR, n, idx.N)
	}
	if idx.N != 0 && (len(idx.UnitIDs) > 0 || len(idx.DF) > 0 || len(idx.TermKeys) > 0) {
		if idxDigest := ComputeCorpusDigest(idx); idxDigest != segCorpusDigest {
			return nil, fmt.Errorf("%w: corpus digest mismatch (seg=%016x idx=%016x)", ErrInvalidZCSR, segCorpusDigest, idxDigest)
		}
	}
	v := int(binary.LittleEndian.Uint64(data[16:24]))
	numEntries := int(binary.LittleEndian.Uint64(data[24:32]))
	numPos := int(binary.LittleEndian.Uint64(data[32:40]))
	numPhrases := int(binary.LittleEndian.Uint64(data[40:48]))
	numPhraseUnits := int(binary.LittleEndian.Uint64(data[48:56]))
	numNeighbors := int(binary.LittleEndian.Uint64(data[56:64]))
	numSubj := int(binary.LittleEndian.Uint64(data[64:72]))
	numDayCos := int(binary.LittleEndian.Uint64(data[72:80]))
	clusterN := int(binary.LittleEndian.Uint64(data[80:88]))
	meanGap := math.Float64frombits(binary.LittleEndian.Uint64(data[88:96]))
	muLogGap := math.Float64frombits(binary.LittleEndian.Uint64(data[96:104]))
	sigLogGap := math.Float64frombits(binary.LittleEndian.Uint64(data[104:112]))
	totalDaysM := math.Float64frombits(binary.LittleEndian.Uint64(data[112:120]))
	entryMeanBits := math.Float64frombits(binary.LittleEndian.Uint64(data[120:128]))
	sentenceEnergyMean := math.Float64frombits(binary.LittleEndian.Uint64(data[128:136]))

	expectedPayload := align8((n+1)*4) + numEntries*32 + align8(numPos*4) +
		v*8 + align8((v+1)*4) + numEntries*8 + align8(v*4) + v*8 + align8(v*2) + align8(v) +
		v*8*3 + align8(v) +
		numPhrases*16 + align8((numPhrases+1)*4) + align8(numPhraseUnits*4) +
		align8(n*4) + align8((n+1)*4) + align8(numNeighbors*4) +
		n*8 + align8(n*4) + n*24 + align8((n+1)*4) + numSubj*16 + numDayCos*16 +
		n*64 + align8((n+1)*4) + align8(idBlobLen)
	if expectedPayload != payloadSize {
		return nil, fmt.Errorf("%w: section layout size mismatch (%d != %d)", ErrInvalidZCSR, expectedPayload, payloadSize)
	}

	pos := zcsrRMHeaderSize
	zeroCopy := isNativeLittleEndian() && (uintptr(unsafe.Pointer(&data[0]))&7 == 0)

	sliceU32Aligned := func(cnt int) []uint32 {
		step := align8(cnt * 4)
		if cnt == 0 {
			pos += step
			return nil
		}
		raw := data[pos : pos+cnt*4]
		pos += step
		if zeroCopy {
			return unsafe.Slice((*uint32)(unsafe.Pointer(&raw[0])), cnt)
		}
		out := make([]uint32, cnt)
		for i := 0; i < cnt; i++ {
			out[i] = binary.LittleEndian.Uint32(raw[i*4 : (i+1)*4])
		}
		return out
	}
	sliceI32Aligned := func(cnt int) []int32 {
		step := align8(cnt * 4)
		if cnt == 0 {
			pos += step
			return nil
		}
		raw := data[pos : pos+cnt*4]
		pos += step
		if zeroCopy {
			return unsafe.Slice((*int32)(unsafe.Pointer(&raw[0])), cnt)
		}
		out := make([]int32, cnt)
		for i := 0; i < cnt; i++ {
			out[i] = int32(binary.LittleEndian.Uint32(raw[i*4 : (i+1)*4]))
		}
		return out
	}
	sliceU64 := func(cnt int) []uint64 {
		if cnt == 0 {
			return nil
		}
		raw := data[pos : pos+cnt*8]
		pos += cnt * 8
		if zeroCopy {
			return unsafe.Slice((*uint64)(unsafe.Pointer(&raw[0])), cnt)
		}
		out := make([]uint64, cnt)
		for i := 0; i < cnt; i++ {
			out[i] = binary.LittleEndian.Uint64(raw[i*8 : (i+1)*8])
		}
		return out
	}
	sliceI64 := func(cnt int) []int64 {
		if cnt == 0 {
			return nil
		}
		raw := data[pos : pos+cnt*8]
		pos += cnt * 8
		if zeroCopy {
			return unsafe.Slice((*int64)(unsafe.Pointer(&raw[0])), cnt)
		}
		out := make([]int64, cnt)
		for i := 0; i < cnt; i++ {
			out[i] = int64(binary.LittleEndian.Uint64(raw[i*8 : (i+1)*8]))
		}
		return out
	}
	sliceF64 := func(cnt int) []float64 {
		if cnt == 0 {
			return nil
		}
		raw := data[pos : pos+cnt*8]
		pos += cnt * 8
		if zeroCopy {
			return unsafe.Slice((*float64)(unsafe.Pointer(&raw[0])), cnt)
		}
		out := make([]float64, cnt)
		for i := 0; i < cnt; i++ {
			out[i] = math.Float64frombits(binary.LittleEndian.Uint64(raw[i*8 : (i+1)*8]))
		}
		return out
	}

	docRowPtr := sliceU32Aligned(n + 1)
	if len(docRowPtr) != n+1 || docRowPtr[0] != 0 || int(docRowPtr[n]) != numEntries {
		return nil, fmt.Errorf("%w: invalid docRowPtr bounds", ErrInvalidZCSR)
	}

	var docEntriesFlat []rmDocEntry
	if numEntries > 0 {
		raw := data[pos : pos+numEntries*32]
		pos += numEntries * 32
		if zeroCopy && unsafe.Sizeof(rmDocEntry{}) == 32 {
			docEntriesFlat = unsafe.Slice((*rmDocEntry)(unsafe.Pointer(&raw[0])), numEntries)
		} else {
			docEntriesFlat = make([]rmDocEntry, numEntries)
			for i := 0; i < numEntries; i++ {
				b := raw[i*32 : (i+1)*32]
				docEntriesFlat[i] = rmDocEntry{
					id:      binary.LittleEndian.Uint64(b[0:8]),
					group:   binary.LittleEndian.Uint64(b[8:16]),
					posOff:  binary.LittleEndian.Uint32(b[16:20]),
					posLen:  binary.LittleEndian.Uint32(b[20:24]),
					tf:      int32(binary.LittleEndian.Uint32(b[24:28])),
					bits:    binary.LittleEndian.Uint16(b[28:30]),
					flags:   b[30],
					roleExt: b[31],
				}
			}
		}
	}
	posPool := sliceU32Aligned(numPos)

	termKeys := sliceU64(v)
	termRowPtr := sliceU32Aligned(v + 1)
	if len(termRowPtr) != v+1 || termRowPtr[0] != 0 || int(termRowPtr[v]) != numEntries {
		return nil, fmt.Errorf("%w: invalid termRowPtr bounds", ErrInvalidZCSR)
	}
	var postingPool []rmPostingEntry
	if numEntries > 0 {
		raw := data[pos : pos+numEntries*8]
		pos += numEntries * 8
		if zeroCopy && unsafe.Sizeof(rmPostingEntry{}) == 8 {
			postingPool = unsafe.Slice((*rmPostingEntry)(unsafe.Pointer(&raw[0])), numEntries)
		} else {
			postingPool = make([]rmPostingEntry, numEntries)
			for i := 0; i < numEntries; i++ {
				b := raw[i*8 : (i+1)*8]
				postingPool[i] = rmPostingEntry{
					unit:  int32(binary.LittleEndian.Uint32(b[0:4])),
					entry: int32(binary.LittleEndian.Uint32(b[4:8])),
				}
			}
		}
	}

	TermDF := sliceI32Aligned(v)
	TermMass := sliceF64(v)
	var TermBits []uint16
	bitsStep := align8(v * 2)
	if v > 0 {
		raw := data[pos : pos+v*2]
		if zeroCopy {
			TermBits = unsafe.Slice((*uint16)(unsafe.Pointer(&raw[0])), v)
		} else {
			TermBits = make([]uint16, v)
			for i := 0; i < v; i++ {
				TermBits[i] = binary.LittleEndian.Uint16(raw[i*2 : (i+1)*2])
			}
		}
	}
	pos += bitsStep
	var TermFlags []uint8
	if v > 0 {
		TermFlags = data[pos : pos+v]
	}
	pos += align8(v)

	termEffW := sliceF64(v)
	termPropR := sliceF64(v)
	termParentG := sliceU64(v)
	primRaw := data[pos : pos+v]
	pos += align8(v)

	var phraseKeys []rmDyad
	if numPhrases > 0 {
		raw := data[pos : pos+numPhrases*16]
		pos += numPhrases * 16
		if zeroCopy && unsafe.Sizeof(rmDyad{}) == 16 {
			phraseKeys = unsafe.Slice((*rmDyad)(unsafe.Pointer(&raw[0])), numPhrases)
		} else {
			phraseKeys = make([]rmDyad, numPhrases)
			for i := 0; i < numPhrases; i++ {
				b := raw[i*16 : (i+1)*16]
				phraseKeys[i] = rmDyad{
					a: binary.LittleEndian.Uint64(b[0:8]),
					b: binary.LittleEndian.Uint64(b[8:16]),
				}
			}
		}
	}
	phraseRowPtr := sliceU32Aligned(numPhrases + 1)
	if len(phraseRowPtr) != numPhrases+1 || phraseRowPtr[0] != 0 || int(phraseRowPtr[numPhrases]) != numPhraseUnits {
		return nil, fmt.Errorf("%w: invalid phraseRowPtr bounds", ErrInvalidZCSR)
	}
	phraseUnits := sliceI32Aligned(numPhraseUnits)

	clusters := sliceI32Aligned(n)
	neighborRowPtr := sliceU32Aligned(n + 1)
	if len(neighborRowPtr) != n+1 || neighborRowPtr[0] != 0 || int(neighborRowPtr[n]) != numNeighbors {
		return nil, fmt.Errorf("%w: invalid neighborRowPtr bounds", ErrInvalidZCSR)
	}
	neighborPool := sliceI32Aligned(numNeighbors)
	refTimes := sliceI64(n)
	ordPos := sliceI32Aligned(n)

	var fastDocs []rmFastDocStruct
	if n > 0 {
		raw := data[pos : pos+n*24]
		pos += n * 24
		if zeroCopy && unsafe.Sizeof(rmFastDocStruct{}) == 24 {
			fastDocs = unsafe.Slice((*rmFastDocStruct)(unsafe.Pointer(&raw[0])), n)
		} else {
			fastDocs = make([]rmFastDocStruct, n)
			for i := 0; i < n; i++ {
				b := raw[i*24 : (i+1)*24]
				fastDocs[i] = rmFastDocStruct{
					docTotal:   math.Float64frombits(binary.LittleEndian.Uint64(b[0:8])),
					day:        int64(binary.LittleEndian.Uint64(b[8:16])),
					subjNormSq: math.Float64frombits(binary.LittleEndian.Uint64(b[16:24])),
				}
			}
		}
	}
	subjRowPtr := sliceU32Aligned(n + 1)
	if len(subjRowPtr) != n+1 || subjRowPtr[0] != 0 || int(subjRowPtr[n]) != numSubj {
		return nil, fmt.Errorf("%w: invalid subjRowPtr bounds", ErrInvalidZCSR)
	}
	subjIDsPool := sliceU64(numSubj)
	subjWeightsPool := sliceF64(numSubj)
	dayCosKeys := sliceU64(numDayCos)
	dayCosVals := sliceF64(numDayCos)

	metaRaw := data[pos : pos+n*64]
	pos += n * 64
	idOffsets := sliceU32Aligned(n + 1)
	if len(idOffsets) != n+1 || idOffsets[0] != 0 || int(idOffsets[n]) > idBlobLen {
		return nil, fmt.Errorf("%w: invalid unitIDOffsets bounds", ErrInvalidZCSR)
	}
	idBytes := data[pos : pos+idBlobLen]
	pos += align8(idBlobLen)

	var parsedUnitIDs []string
	if (refreshStrings || len(idx.UnitIDs) == 0) && n > 0 {
		unitIDs := make([]string, n)
		for u := 0; u < n; u++ {
			s, e := idOffsets[u], idOffsets[u+1]
			if e < s || int(e) > len(idBytes) {
				return nil, fmt.Errorf("%w: unitID offset out of bounds at %d", ErrInvalidZCSR, u)
			}
			if e > s {
				unitIDs[u] = unsafe.String(&idBytes[s], int(e-s))
			}
		}
		parsedUnitIDs = unitIDs
	}
	var parsedMetas []DocMeta
	var parsedWidePos map[uint32]TermPosition
	if n > 0 {
		const (
			zcsrWideLineSpanFlag uint32 = 1 << 31
			zcsrWidePosPoolFlag  uint32 = 1 << 30
			zcsrCitedIDsFlag     uint32 = 1 << 29
		)
		hasWidePosPool := (binary.LittleEndian.Uint32(metaRaw[60:64]) & zcsrWidePosPoolFlag) != 0
		var maxMetaEnd uint64
		if len(idOffsets) > n {
			maxMetaEnd = uint64(idOffsets[n])
		}
		metas := make([]DocMeta, n)
		for u := 0; u < n; u++ {
			b := metaRaw[u*64 : (u+1)*64]
			lOff := binary.LittleEndian.Uint32(b[56:60])
			rawLLen := binary.LittleEndian.Uint32(b[60:64])
			wideLine := (rawLLen & zcsrWideLineSpanFlag) != 0
			hasCited := (rawLLen & zcsrCitedIDsFlag) != 0
			lLen := rawLLen &^ (zcsrWideLineSpanFlag | zcsrWidePosPoolFlag | zcsrCitedIDsFlag)
			extraBytes := uint64(0)
			if wideLine {
				extraBytes = 16
			}
			if uint64(lOff)+uint64(lLen)+extraBytes > uint64(len(idBytes)) {
				return nil, fmt.Errorf("%w: layer offset out of bounds at %d", ErrInvalidZCSR, u)
			}
			if endOff := uint64(lOff) + uint64(lLen) + extraBytes; endOff > maxMetaEnd {
				maxMetaEnd = endOff
			}
			var layer string
			if lLen > 0 {
				layer = unsafe.String(&idBytes[lOff], int(lLen))
			}
			lineStart := int(int32(binary.LittleEndian.Uint32(b[48:52])))
			lineEnd := int(int32(binary.LittleEndian.Uint32(b[52:56])))
			if wideLine {
				widePos := int(lOff + lLen)
				lineStart = int(int64(binary.LittleEndian.Uint64(idBytes[widePos : widePos+8])))
				lineEnd = int(int64(binary.LittleEndian.Uint64(idBytes[widePos+8 : widePos+16])))
			}
			var citedIDs []string
			if hasCited {
				cPos := uint64(lOff) + uint64(lLen) + extraBytes
				if cPos+4 > uint64(len(idBytes)) {
					return nil, fmt.Errorf("%w: citation record out of bounds at %d", ErrInvalidZCSR, u)
				}
				cCount := binary.LittleEndian.Uint32(idBytes[cPos : cPos+4])
				cPos += 4
				if cCount > uint32(n) {
					return nil, fmt.Errorf("%w: citation count %d exceeds corpus size at %d", ErrInvalidZCSR, cCount, u)
				}
				citedIDs = make([]string, 0, cCount)
				for ci := uint32(0); ci < cCount; ci++ {
					if cPos+4 > uint64(len(idBytes)) {
						return nil, fmt.Errorf("%w: citation length out of bounds at %d", ErrInvalidZCSR, u)
					}
					cl := binary.LittleEndian.Uint32(idBytes[cPos : cPos+4])
					cPos += 4
					if cPos+uint64(cl) > uint64(len(idBytes)) {
						return nil, fmt.Errorf("%w: citation bytes out of bounds at %d", ErrInvalidZCSR, u)
					}
					// Clone off the mmap region: citation IDs must survive Close().
					citedIDs = append(citedIDs, string(idBytes[cPos:cPos+uint64(cl)]))
					cPos += uint64(cl)
				}
				if cPos > maxMetaEnd {
					maxMetaEnd = cPos
				}
			}
			metas[u] = DocMeta{
				OccurredAt: int64(binary.LittleEndian.Uint64(b[0:8])),
				NarratedAt: int64(binary.LittleEndian.Uint64(b[8:16])),
				IngestedAt: int64(binary.LittleEndian.Uint64(b[16:24])),
				AuthorHash: binary.LittleEndian.Uint64(b[24:32]),
				DocHash:    binary.LittleEndian.Uint64(b[32:40]),
				BlockHash:  binary.LittleEndian.Uint64(b[40:48]),
				LineStart:  lineStart,
				LineEnd:    lineEnd,
				Layer:      layer,
				CitedIDs:   citedIDs,
			}
		}
		if refreshStrings || len(idx.UnitMeta) == 0 {
			parsedMetas = metas
		}
		if hasWidePosPool {
			if maxMetaEnd+8 > uint64(len(idBytes)) {
				return nil, fmt.Errorf("%w: widePosPool header out of bounds", ErrInvalidZCSR)
			}
			wpCount := binary.LittleEndian.Uint64(idBytes[maxMetaEnd : maxMetaEnd+8])
			wpOff := maxMetaEnd + 8
			if wpCount > uint64(numPos) || wpOff+wpCount*24 > uint64(len(idBytes)) {
				return nil, fmt.Errorf("%w: widePosPool records out of bounds", ErrInvalidZCSR)
			}
			parsedWidePos = make(map[uint32]TermPosition, int(wpCount))
			for i := uint64(0); i < wpCount; i++ {
				pIdx := uint32(binary.LittleEndian.Uint64(idBytes[wpOff : wpOff+8]))
				sent := int(int64(binary.LittleEndian.Uint64(idBytes[wpOff+8 : wpOff+16])))
				slot := int(int64(binary.LittleEndian.Uint64(idBytes[wpOff+16 : wpOff+24])))
				parsedWidePos[pIdx] = TermPosition{Sentence: sent, Slot: slot}
				wpOff += 24
			}
		}
	}

	docEntries := make([][]rmDocEntry, n)
	for u := 0; u < n; u++ {
		s, e := docRowPtr[u], docRowPtr[u+1]
		if e < s || int(e) > len(docEntriesFlat) {
			return nil, fmt.Errorf("%w: docRowPtr out of bounds at %d", ErrInvalidZCSR, u)
		}
		docEntries[u] = docEntriesFlat[s:e:e]
	}
	for i, ent := range docEntriesFlat {
		if ent.tf < 1 || uint64(ent.posOff)+uint64(ent.posLen) > uint64(len(posPool)) {
			return nil, fmt.Errorf("%w: docEntriesFlat[%d] invalid tf=%d or pos bounds (%d+%d > %d)",
				ErrInvalidZCSR, i, ent.tf, ent.posOff, ent.posLen, len(posPool))
		}
	}
	for i := 1; i < len(termKeys); i++ {
		if termKeys[i] <= termKeys[i-1] {
			return nil, fmt.Errorf("%w: termKeys not strictly ascending at %d", ErrInvalidZCSR, i)
		}
	}
	for i := 0; i < v; i++ {
		s, e := termRowPtr[i], termRowPtr[i+1]
		if e < s || int(e) > len(postingPool) {
			return nil, fmt.Errorf("%w: termRowPtr out of bounds at %d", ErrInvalidZCSR, i)
		}
	}
	for i, p := range postingPool {
		if p.unit < 0 || int(p.unit) >= n {
			return nil, fmt.Errorf("%w: postingPool[%d] unit=%d out of bounds [0,%d)", ErrInvalidZCSR, i, p.unit, n)
		}
		uEntries := docRowPtr[p.unit+1] - docRowPtr[p.unit]
		if p.entry < 0 || uint32(p.entry) >= uEntries {
			return nil, fmt.Errorf("%w: postingPool[%d] entry=%d out of bounds [0,%d)", ErrInvalidZCSR, i, p.entry, uEntries)
		}
	}
	for i := 1; i < len(phraseKeys); i++ {
		prev, cur := phraseKeys[i-1], phraseKeys[i]
		if cur.a < prev.a || (cur.a == prev.a && cur.b <= prev.b) {
			return nil, fmt.Errorf("%w: phraseKeys not strictly ascending at %d", ErrInvalidZCSR, i)
		}
	}
	for i := 0; i < numPhrases; i++ {
		s, e := phraseRowPtr[i], phraseRowPtr[i+1]
		if e < s || int(e) > len(phraseUnits) {
			return nil, fmt.Errorf("%w: phraseRowPtr out of bounds at %d", ErrInvalidZCSR, i)
		}
	}
	for i, u := range phraseUnits {
		if u < 0 || int(u) >= n {
			return nil, fmt.Errorf("%w: phraseUnits[%d] unit=%d out of bounds [0,%d)", ErrInvalidZCSR, i, u, n)
		}
	}
	for u := 0; u < n; u++ {
		if neighborRowPtr[u+1] < neighborRowPtr[u] || int(neighborRowPtr[u+1]) > len(neighborPool) {
			return nil, fmt.Errorf("%w: neighborRowPtr out of bounds at %d", ErrInvalidZCSR, u)
		}
		if subjRowPtr[u+1] < subjRowPtr[u] || int(subjRowPtr[u+1]) > len(subjIDsPool) {
			return nil, fmt.Errorf("%w: subjRowPtr out of bounds at %d", ErrInvalidZCSR, u)
		}
	}
	if clusterN < 0 || clusterN > n {
		return nil, fmt.Errorf("%w: invalid clusterN=%d for n=%d", ErrInvalidZCSR, clusterN, n)
	}
	for u := 0; u < n; u++ {
		if clusters[u] < 0 || int(clusters[u]) >= clusterN {
			return nil, fmt.Errorf("%w: clusters[%d]=%d out of bounds [0,%d)", ErrInvalidZCSR, u, clusters[u], clusterN)
		}
		if ordPos[u] < -1 || int(ordPos[u]) >= clusterN {
			return nil, fmt.Errorf("%w: ordPos[%d]=%d out of bounds [-1,%d)", ErrInvalidZCSR, u, ordPos[u], clusterN)
		}
	}
	for i, u := range neighborPool {
		if u < 0 || int(u) >= n {
			return nil, fmt.Errorf("%w: neighborPool[%d] unit=%d out of bounds [0,%d)", ErrInvalidZCSR, i, u, n)
		}
	}
	for i := 1; i < len(dayCosKeys); i++ {
		if dayCosKeys[i] <= dayCosKeys[i-1] {
			return nil, fmt.Errorf("%w: dayCosKeys not strictly ascending at %d", ErrInvalidZCSR, i)
		}
	}

	postingDigest := computePostingDigestFromCSR(termKeys, termRowPtr, postingPool, docRowPtr, docEntriesFlat, posPool, parsedWidePos)
	if segCorpusDigest != 0 {
		checkIdx := *idx
		checkIdx.N = n
		if parsedUnitIDs != nil {
			checkIdx.UnitIDs = parsedUnitIDs
		}
		if parsedMetas != nil {
			checkIdx.UnitMeta = parsedMetas
		}
		checkIdx.TermKeys = termKeys
		checkIdx.rawTermDF = TermDF
		checkIdx.rawTermMass = TermMass
		checkIdx.rawTermBits = TermBits
		checkIdx.rawTermFlags = TermFlags
		checkIdx.PostingDigest = postingDigest
		checkIdx.BuildTermPrefixIndex()
		if gotDigest := ComputeCorpusDigest(&checkIdx); gotDigest != segCorpusDigest {
			return nil, fmt.Errorf("%w: parsed segment digest mismatch (seg=%016x parsed=%016x)", ErrCorruptZCSR, segCorpusDigest, gotDigest)
		}
	}

	idx.N = n
	if parsedUnitIDs != nil {
		idx.UnitIDs = parsedUnitIDs
	}
	if parsedMetas != nil {
		idx.UnitMeta = parsedMetas
	}
	idx.TermKeys = termKeys
	idx.rawTermDF = TermDF
	idx.rawTermMass = TermMass
	idx.rawTermBits = TermBits
	idx.rawTermFlags = TermFlags
	idx.PostingDigest = postingDigest

	if idx.stateGen == 0 {
		idx.stateGen = 1
	}
	idx.BuildTermPrefixIndex()
	if idx.MeanBits == 0 && len(TermBits) > 0 {
		var bitSum float64
		var bitCnt int
		for _, b := range TermBits {
			if b > 0 {
				bitSum += float64(b)
				bitCnt++
			}
		}
		if bitCnt > 0 {
			idx.MeanBits = bitSum / float64(bitCnt)
		}
	}

	var maxOrd int32 = -1
	for _, o := range ordPos {
		if o > maxOrd {
			maxOrd = o
		}
	}
	var bucketTimes []int64
	if maxOrd >= 0 {
		bucketTimes = make([]int64, maxOrd+1)
		for u, o := range ordPos {
			if o >= 0 {
				bucketTimes[o] = refTimes[u]
			}
		}
	}

	st := &rmClusterState{
		builtIndexGen:      idx.stateGen,
		builtDigest:        idx.PostingDigest,
		docEntries:         docEntries,
		docRowPtr:          docRowPtr,
		docEntriesFlat:     docEntriesFlat,
		posPool:            posPool,
		termKeys:           termKeys,
		termRowPtr:         termRowPtr,
		postingPool:        postingPool,
		termEffWeight:      termEffW,
		termProperRatio:    termPropR,
		termParentGroup:    termParentG,
		termIsPrimary:      primRaw,
		phraseKeys:         phraseKeys,
		phraseRowPtr:       phraseRowPtr,
		phraseUnits:        phraseUnits,
		clusters:           clusters,
		clusterN:           clusterN,
		bucketTimes:        bucketTimes,
		neighborRowPtr:     neighborRowPtr,
		neighborPool:       neighborPool,
		refTimes:           refTimes,
		meanGap:            meanGap,
		muLogGap:           muLogGap,
		sigLogGap:          sigLogGap,
		ordPos:             ordPos,
		entryMeanBits:      entryMeanBits,
		sentenceEnergyMean: sentenceEnergyMean,
		totalDaysM:         totalDaysM,
		fastDocs:           fastDocs,
		subjRowPtr:         subjRowPtr,
		subjIDsPool:        subjIDsPool,
		subjWeightsPool:    subjWeightsPool,
		dayCosKeys:         dayCosKeys,
		dayCosVals:         dayCosVals,
		widePos:            parsedWidePos,
	}
	st.buildPrefixTables(n)
	st.buildEnvelopeDF(idx)
	st.buildProvenanceCSR(idx)
	st.episodeOf, st.episodeCount = buildEpisodeComponents(st.bucketTimes, st.ordPos, n, st.meanGap, st.muLogGap, st.sigLogGap)
	return st, nil
}
