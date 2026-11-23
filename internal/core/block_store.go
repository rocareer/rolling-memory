// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package core

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"sort"
)

const (
	// v2：parentGrp 列由块局部 min-hash 改为全局 canonical 族根（key-invariance），
	// 旧块值语义过期，拒读并重建。
	blockStoreVersion = 2
	blockTermMagic    = "ZTB1"
	blockDocMagic     = "ZDB1"
	blockManifestName = "manifest.json"
	blockManifestTmp  = "manifest.json.tmp"
	blockManifestLock = "manifest.lock"
	blockStoreFile    = 0o600
	blockStoreDir     = 0o700
)

// blockFileRef 是 manifest 里单个不可变块文件的指针。Lo/Hi 是该块的 unit 区间，
// CRC 是整块文件（含头）的校验和。
type blockFileRef struct {
	File string `json:"file"`
	Lo   int32  `json:"lo"`
	Hi   int32  `json:"hi"`
	CRC  uint32 `json:"crc"`
}

// blockManifest 是分块状态的原子发布单元：块文件只追加、永不就地改，manifest
// 用 tmp+fsync+rename 发布；加载按 manifest 的块表顺序拼接即得全局视图。
type blockManifest struct {
	Version int            `json:"version"`
	N       int            `json:"n"`
	Gen     uint64         `json:"gen"`
	Term    []blockFileRef `json:"term_blocks"`
	Doc     []blockFileRef `json:"doc_blocks"`
}

// appendBlockStore 把新块写成不可变文件并原子发布新 manifest。prev 为 nil 时
// 是一次全新发布；否则新块的区间必须从 prev 的末块继续并恰好覆盖到 n。
// 旧块文件不会被改写，旧 manifest 在 rename 前仍然有效。
func appendBlockStore(dir string, prev *blockManifest, newTerm []*termBlock, newDoc []*docBlock, n int, gen uint64) (*blockManifest, error) {
	if dir == "" {
		return nil, errors.New("core: empty block store directory")
	}
	if n < 0 {
		return nil, fmt.Errorf("core: negative block store N=%d", n)
	}
	if err := os.MkdirAll(dir, blockStoreDir); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, blockStoreDir); err != nil {
		return nil, err
	}
	unlock, err := lockBlockStore(dir)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err := verifyBlockStoreBase(dir, prev); err != nil {
		return nil, err
	}
	manifest := &blockManifest{Version: blockStoreVersion, N: n, Gen: gen}
	if prev != nil {
		if prev.Version != blockStoreVersion {
			return nil, fmt.Errorf("core: unsupported block store version %d", prev.Version)
		}
		manifest.Term = append([]blockFileRef(nil), prev.Term...)
		manifest.Doc = append([]blockFileRef(nil), prev.Doc...)
	}
	termLo, err := nextBlockStart(manifest.Term, n, "term")
	if err != nil {
		return nil, err
	}
	docLo, err := nextBlockStart(manifest.Doc, n, "doc")
	if err != nil {
		return nil, err
	}
	for i, block := range newTerm {
		if block == nil {
			return nil, errors.New("core: nil term block")
		}
		if block.lo != termLo {
			return nil, fmt.Errorf("core: term block %d starts at %d, want %d", i, block.lo, termLo)
		}
		ref, err := writeBlockFile(dir, blockFileName("t", gen, len(manifest.Term)), encodeTermBlock(block))

		if err != nil {
			return nil, err
		}
		ref.Lo, ref.Hi = block.lo, block.hi
		manifest.Term = append(manifest.Term, ref)
		termLo = block.hi
	}
	for i, block := range newDoc {
		if block == nil {
			return nil, errors.New("core: nil doc block")
		}
		if block.lo != docLo {
			return nil, fmt.Errorf("core: doc block %d starts at %d, want %d", i, block.lo, docLo)
		}
		ref, err := writeBlockFile(dir, blockFileName("d", gen, len(manifest.Doc)), encodeDocBlock(block))

		if err != nil {
			return nil, err
		}
		ref.Lo, ref.Hi = block.lo, block.hi
		manifest.Doc = append(manifest.Doc, ref)
		docLo = block.hi
	}
	if int(termLo) != n {
		return nil, fmt.Errorf("core: term blocks cover to %d, want %d", termLo, n)
	}
	if len(manifest.Doc) > 0 && int(docLo) != n {
		return nil, fmt.Errorf("core: doc blocks cover to %d, want %d", docLo, n)
	}
	if err := writeBlockManifest(dir, manifest); err != nil {
		return nil, err
	}
	return manifest, nil
}

// lockBlockStore acquires an advisory whole-store lock. A writer may block
// briefly behind another publisher, but it cannot race the base-manifest check
// and rename sequence. The operating-system releases the lock if a process dies.
func lockBlockStore(dir string) (func(), error) {
	path := filepath.Join(dir, blockManifestLock)
	handle, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, blockStoreFile)
	if err != nil {
		return nil, err
	}
	if err := lockBlockStoreHandle(handle); err != nil {
		_ = handle.Close()
		return nil, err
	}
	return func() {
		_ = unlockBlockStoreHandle(handle)
		_ = handle.Close()
	}, nil
}

// verifyBlockStoreBase rejects stale publication bases before creating any block
// file. A nil base is valid only for an empty directory; a non-nil base must be
// byte-for-byte the manifest currently published in that directory.
func verifyBlockStoreBase(dir string, prev *blockManifest) error {
	path := filepath.Join(dir, blockManifestName)
	data, err := os.ReadFile(path)
	if prev == nil {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if len(data) > 0 {
			return errors.New("core: block store already has a manifest")
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("core: read current block manifest: %w", err)
	}
	current, err := json.MarshalIndent(prev, "", "  ")
	if err != nil {
		return err
	}
	current = append(current, '\n')
	if string(data) != string(current) {
		return errors.New("core: stale block manifest")
	}
	return nil
}

// nextBlockStart 校验已有块表连续覆盖的终点，返回新块的起始 unit。
func nextBlockStart(refs []blockFileRef, n int, kind string) (int32, error) {
	if len(refs) == 0 {
		return 0, nil
	}
	var want int32
	for i, ref := range refs {
		if ref.Lo != want {
			return 0, fmt.Errorf("core: %s block %d starts at %d, want %d", kind, i, ref.Lo, want)
		}
		if ref.Hi <= ref.Lo {
			return 0, fmt.Errorf("core: %s block %d has empty range [%d,%d)", kind, i, ref.Lo, ref.Hi)
		}
		want = ref.Hi
	}
	if int(want) > n {
		return 0, fmt.Errorf("core: %s blocks cover %d > N=%d", kind, want, n)
	}
	return want, nil
}

// readBlockStore 读取并校验 manifest 与全部块文件，按块表顺序重建块视图。
// 校验失败返回错误，绝不返回部分结果。
func readBlockStore(dir string) (*termBlocks, *docBlocks, error) {
	if dir == "" {
		return nil, nil, errors.New("core: empty block store directory")
	}
	data, err := os.ReadFile(filepath.Join(dir, blockManifestName))
	if err != nil {
		return nil, nil, err
	}
	var manifest blockManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, nil, fmt.Errorf("core: decode block manifest: %w", err)
	}
	if manifest.Version != blockStoreVersion {
		return nil, nil, fmt.Errorf("core: unsupported block store version %d", manifest.Version)
	}
	if manifest.N < 0 {
		return nil, nil, fmt.Errorf("core: negative block store N=%d", manifest.N)
	}
	var tb *termBlocks
	if len(manifest.Term) > 0 {
		tb = &termBlocks{n: manifest.N, gen: manifest.Gen, bounds: make([]int32, 1, len(manifest.Term)+1), blocks: make([]*termBlock, 0, len(manifest.Term))}
		for i, ref := range manifest.Term {
			if i > 0 && ref.Lo != manifest.Term[i-1].Hi {
				return nil, nil, fmt.Errorf("core: term block %d has non-contiguous range [%d,%d)", i, ref.Lo, ref.Hi)
			}
			body, err := readBlockFile(dir, ref, blockTermMagic)
			if err != nil {
				return nil, nil, err
			}
			block, err := decodeTermBlock(body)
			if err != nil {
				return nil, nil, fmt.Errorf("core: term block %d: %w", i, err)
			}
			if block.lo != ref.Lo || block.hi != ref.Hi {
				return nil, nil, fmt.Errorf("core: term block %d range [%d,%d) != manifest [%d,%d)", i, block.lo, block.hi, ref.Lo, ref.Hi)
			}
			tb.bounds = append(tb.bounds, block.hi)
			tb.blocks = append(tb.blocks, block)
		}
		if tb.bounds[0] != 0 || int(tb.bounds[len(tb.bounds)-1]) != manifest.N {
			return nil, nil, fmt.Errorf("core: term blocks cover %v, want [0,%d]", tb.bounds, manifest.N)
		}
	}
	var db *docBlocks
	if len(manifest.Doc) > 0 {
		db = &docBlocks{n: manifest.N, gen: manifest.Gen, bounds: make([]int32, 1, len(manifest.Doc)+1), blocks: make([]*docBlock, 0, len(manifest.Doc))}
		for i, ref := range manifest.Doc {
			if i > 0 && ref.Lo != manifest.Doc[i-1].Hi {
				return nil, nil, fmt.Errorf("core: doc block %d has non-contiguous range [%d,%d)", i, ref.Lo, ref.Hi)
			}
			body, err := readBlockFile(dir, ref, blockDocMagic)
			if err != nil {
				return nil, nil, err
			}
			block, err := decodeDocBlock(body)
			if err != nil {
				return nil, nil, fmt.Errorf("core: doc block %d: %w", i, err)
			}
			if block.lo != ref.Lo || block.hi != ref.Hi {
				return nil, nil, fmt.Errorf("core: doc block %d range [%d,%d) != manifest [%d,%d)", i, block.lo, block.hi, ref.Lo, ref.Hi)
			}
			db.bounds = append(db.bounds, block.hi)
			db.blocks = append(db.blocks, block)
		}
		if db.bounds[0] != 0 || int(db.bounds[len(db.bounds)-1]) != manifest.N {
			return nil, nil, fmt.Errorf("core: doc blocks cover %v, want [0,%d]", db.bounds, manifest.N)
		}
	}
	return tb, db, nil
}

// blockFileName derives a generation-scoped immutable block name. A full
// replacement can restart the ordinal at zero without reusing a prior generation's
// file, while a repeated publication of the same generation fails closed at create.
func blockFileName(kind string, gen uint64, ordinal int) string {
	return fmt.Sprintf("%s-%020d-%06d.bin", kind, gen, ordinal)
}

// writeBlockFile 写一个不可变块文件：块体尾部追加 CRC、写满、fsync、关闭，
// 返回文件名与块体 CRC。O_EXCL makes a repeated or stale publication fail before
// it can alter a file still referenced by an older manifest.
func writeBlockFile(dir, name string, body []byte) (blockFileRef, error) {
	if filepath.Base(name) != name || name == "." || name == ".." {
		return blockFileRef{}, fmt.Errorf("core: invalid block file name %q", name)
	}
	path := filepath.Join(dir, name)
	handle, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, blockStoreFile)
	if err != nil {
		return blockFileRef{}, err
	}
	crc := crc32.ChecksumIEEE(body)
	payload := binary.LittleEndian.AppendUint32(body, crc)
	if _, err := handle.Write(payload); err != nil {
		_ = handle.Close()
		return blockFileRef{}, err
	}
	if err := handle.Sync(); err != nil {
		_ = handle.Close()
		return blockFileRef{}, err
	}
	if err := handle.Close(); err != nil {
		return blockFileRef{}, err
	}
	return blockFileRef{File: name, CRC: crc}, nil
}

// readBlockFile 读块文件、校验 magic 与 CRC，返回去掉尾部校验和的块体。
func readBlockFile(dir string, ref blockFileRef, magic string) ([]byte, error) {
	if ref.File == "" || filepath.Base(ref.File) != ref.File || ref.File == "." || ref.File == ".." {
		return nil, fmt.Errorf("core: invalid block file name %q", ref.File)
	}
	data, err := os.ReadFile(filepath.Join(dir, ref.File))
	if err != nil {
		return nil, err
	}
	if len(data) < 8 || string(data[:4]) != magic {
		return nil, fmt.Errorf("core: block %s has a bad header", ref.File)
	}
	body := data[:len(data)-4]
	if got := crc32.ChecksumIEEE(body); got != ref.CRC {
		return nil, fmt.Errorf("core: block %s crc %08x != manifest %08x", ref.File, got, ref.CRC)
	}
	return body, nil
}

// writeBlockManifest 原子发布 manifest：tmp + fsync + rename + 目录 fsync。
func writeBlockManifest(dir string, manifest *blockManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, blockManifestTmp)
	handle, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, blockStoreFile)
	if err != nil {
		return err
	}
	if _, err := handle.Write(append(data, '\n')); err != nil {
		_ = handle.Close()
		return err
	}
	if err := handle.Sync(); err != nil {
		_ = handle.Close()
		return err
	}
	if err := handle.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, blockManifestName)); err != nil {
		return err
	}
	if dirHandle, err := os.Open(dir); err == nil {
		_ = dirHandle.Sync()
		_ = dirHandle.Close()
	}
	return nil
}

// encodeTermBlock 把一块 term 倒排编码为 ZTB1：整数列原样、prefixPtr 由键列
// 派生不落盘，尾部是整块 CRC。
func encodeTermBlock(block *termBlock) []byte {
	keyN, postN := len(block.keys), len(block.units)
	size := 4 + 4 + 4 + 4 + 4 + 4 +
		keyN*8 + (keyN+1)*4 + postN*4 +
		keyN*4 + keyN*8 + keyN*4 + keyN*8 + keyN*2 + keyN + keyN + 4
	buf := make([]byte, 0, size)
	buf = append(buf, blockTermMagic...)
	buf = binary.LittleEndian.AppendUint32(buf, blockStoreVersion)
	buf = binary.LittleEndian.AppendUint32(buf, uint32(block.lo))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(block.hi))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(keyN))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(postN))
	for _, key := range block.keys {
		buf = binary.LittleEndian.AppendUint64(buf, key)
	}
	for i := 0; i <= keyN; i++ {
		var ptr uint32
		if i < len(block.rowPtr) {
			ptr = block.rowPtr[i]
		}
		buf = binary.LittleEndian.AppendUint32(buf, ptr)
	}
	for _, unit := range block.units {
		buf = binary.LittleEndian.AppendUint32(buf, uint32(unit))
	}
	for _, v := range block.df {
		buf = binary.LittleEndian.AppendUint32(buf, uint32(v))
	}
	for _, v := range block.cf {
		buf = binary.LittleEndian.AppendUint64(buf, uint64(v))
	}
	for _, v := range block.propCnt {
		buf = binary.LittleEndian.AppendUint32(buf, uint32(v))
	}
	for _, v := range block.parentGrp {
		buf = binary.LittleEndian.AppendUint64(buf, v)
	}
	for _, v := range block.bits {
		buf = binary.LittleEndian.AppendUint16(buf, v)
	}
	buf = append(buf, block.flags...)
	buf = append(buf, block.primary...)
	return buf
}

// decodeTermBlock 解码 ZTB1 并重建 prefixPtr；长度/版本/CRC 任一不符即报错。
func decodeTermBlock(body []byte) (*termBlock, error) {
	if len(body) < 24 || string(body[:4]) != blockTermMagic {
		return nil, errors.New("bad term block header")
	}
	version := binary.LittleEndian.Uint32(body[4:8])
	if version != blockStoreVersion {
		return nil, fmt.Errorf("unsupported term block version %d", version)
	}
	lo := int32(binary.LittleEndian.Uint32(body[8:12]))
	hi := int32(binary.LittleEndian.Uint32(body[12:16]))
	keyN := int(binary.LittleEndian.Uint32(body[16:20]))
	postN := int(binary.LittleEndian.Uint32(body[20:24]))
	if lo < 0 || hi < lo || keyN < 0 || postN < 0 {
		return nil, errors.New("bad term block ranges")
	}
	want := 24 + keyN*8 + (keyN+1)*4 + postN*4 + keyN*4 + keyN*8 + keyN*4 + keyN*8 + keyN*2 + keyN + keyN
	if len(body) != want {
		return nil, fmt.Errorf("term block size %d != %d", len(body), want)
	}
	offset := 24
	readU64 := func() uint64 {
		v := binary.LittleEndian.Uint64(body[offset:])
		offset += 8
		return v
	}
	readU32 := func() uint32 {
		v := binary.LittleEndian.Uint32(body[offset:])
		offset += 4
		return v
	}
	readU16 := func() uint16 {
		v := binary.LittleEndian.Uint16(body[offset:])
		offset += 2
		return v
	}
	block := &termBlock{lo: lo, hi: hi}
	block.keys = make([]uint64, keyN)
	for i := range block.keys {
		block.keys[i] = readU64()
	}
	block.rowPtr = make([]uint32, keyN+1)
	for i := range block.rowPtr {
		block.rowPtr[i] = readU32()
	}
	block.units = make([]int32, postN)
	for i := range block.units {
		block.units[i] = int32(readU32())
	}
	block.df = make([]int32, keyN)
	for i := range block.df {
		block.df[i] = int32(readU32())
	}
	block.cf = make([]int64, keyN)
	for i := range block.cf {
		block.cf[i] = int64(readU64())
	}
	block.propCnt = make([]int32, keyN)
	for i := range block.propCnt {
		block.propCnt[i] = int32(readU32())
	}
	block.parentGrp = make([]uint64, keyN)
	for i := range block.parentGrp {
		block.parentGrp[i] = readU64()
	}
	block.bits = make([]uint16, keyN)
	for i := range block.bits {
		block.bits[i] = readU16()
	}
	block.flags = make([]uint8, keyN)
	copy(block.flags, body[offset:offset+keyN])
	offset += keyN
	block.primary = make([]uint8, keyN)
	copy(block.primary, body[offset:offset+keyN])
	if err := verifyTermBlock(block, keyN, postN); err != nil {
		return nil, err
	}
	rebuildTermPrefix(block)
	return block, nil
}

// verifyTermBlock 交叉校验行指针与 posting 行，拒绝自相矛盾的块文件。
func verifyTermBlock(block *termBlock, keyN, postN int) error {
	if len(block.rowPtr) != keyN+1 || int(block.rowPtr[0]) != 0 || int(block.rowPtr[keyN]) != postN {
		return errors.New("term block rowPtr does not span the posting rows")
	}
	for i := 0; i < keyN; i++ {
		if block.rowPtr[i] > block.rowPtr[i+1] {
			return errors.New("term block rowPtr is not monotonic")
		}
	}
	if len(block.keys) != keyN {
		return errors.New("term block key count mismatch")
	}
	for i := 1; i < keyN; i++ {
		if block.keys[i] <= block.keys[i-1] {
			return errors.New("term block keys are not strictly ascending")
		}
	}
	return nil
}

// rebuildTermPrefix 由键列重建 4,096 桶前缀表（与 buildTermBlock 同式）。
func rebuildTermPrefix(block *termBlock) {
	pi := 0
	for bucket := 0; bucket < termBlockPrefixBuckets; bucket++ {
		for pi < len(block.keys) && int(block.keys[pi]>>52) <= bucket {
			pi++
		}
		block.prefixPtr[bucket+1] = uint32(pi)
	}
}

// encodeDocBlock 把一块文档行编码为 ZDB1：行、块内位置池与宽坐标表；宽坐标
// 按偏移升序写出，保证同样内容产生同样字节。
func encodeDocBlock(block *docBlock) []byte {
	unitN := int(block.hi - block.lo)
	entryN, posN, wideN := len(block.entries), len(block.posPool), len(block.widePos)
	size := 4 + 4 + 4 + 4 + 4 + 4 + 4 + 4 +
		(unitN+1)*4 + entryN*32 + posN*4 + wideN*(4+8+8) + 4
	buf := make([]byte, 0, size)
	buf = append(buf, blockDocMagic...)
	buf = binary.LittleEndian.AppendUint32(buf, blockStoreVersion)
	buf = binary.LittleEndian.AppendUint32(buf, uint32(block.lo))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(block.hi))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(unitN))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(entryN))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(posN))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(wideN))
	for _, ptr := range block.rowPtr {
		buf = binary.LittleEndian.AppendUint32(buf, ptr)
	}
	for _, entry := range block.entries {
		buf = binary.LittleEndian.AppendUint64(buf, entry.id)
		buf = binary.LittleEndian.AppendUint64(buf, entry.group)
		buf = binary.LittleEndian.AppendUint32(buf, entry.posOff)
		buf = binary.LittleEndian.AppendUint32(buf, entry.posLen)
		buf = binary.LittleEndian.AppendUint32(buf, uint32(entry.tf))
		buf = binary.LittleEndian.AppendUint16(buf, entry.bits)
		buf = append(buf, entry.flags, entry.roleExt)
	}
	for _, pos := range block.posPool {
		buf = binary.LittleEndian.AppendUint32(buf, pos)
	}
	offsets := make([]int, 0, wideN)
	for offset := range block.widePos {
		offsets = append(offsets, int(offset))
	}
	sort.Ints(offsets)
	for _, offset := range offsets {
		position := block.widePos[uint32(offset)]
		buf = binary.LittleEndian.AppendUint32(buf, uint32(offset))
		buf = binary.LittleEndian.AppendUint64(buf, uint64(position.Sentence))
		buf = binary.LittleEndian.AppendUint64(buf, uint64(position.Slot))
	}
	return buf
}

// decodeDocBlock 解码 ZDB1 并校验行指针、位置池边界与宽偏移。
func decodeDocBlock(body []byte) (*docBlock, error) {
	if len(body) < 32 || string(body[:4]) != blockDocMagic {
		return nil, errors.New("bad doc block header")
	}
	version := binary.LittleEndian.Uint32(body[4:8])
	if version != blockStoreVersion {
		return nil, fmt.Errorf("unsupported doc block version %d", version)
	}
	lo := int32(binary.LittleEndian.Uint32(body[8:12]))
	hi := int32(binary.LittleEndian.Uint32(body[12:16]))
	unitN := int(binary.LittleEndian.Uint32(body[16:20]))
	entryN := int(binary.LittleEndian.Uint32(body[20:24]))
	posN := int(binary.LittleEndian.Uint32(body[24:28]))
	wideN := int(binary.LittleEndian.Uint32(body[28:32]))
	if lo < 0 || hi < lo || unitN != int(hi-lo) || entryN < 0 || posN < 0 || wideN < 0 {
		return nil, errors.New("bad doc block ranges")
	}
	want := 32 + (unitN+1)*4 + entryN*32 + posN*4 + wideN*20
	if len(body) != want {
		return nil, fmt.Errorf("doc block size %d != %d", len(body), want)
	}
	offset := 32
	readU32 := func() uint32 {
		v := binary.LittleEndian.Uint32(body[offset:])
		offset += 4
		return v
	}
	readU64 := func() uint64 {
		v := binary.LittleEndian.Uint64(body[offset:])
		offset += 8
		return v
	}
	readU16 := func() uint16 {
		v := binary.LittleEndian.Uint16(body[offset:])
		offset += 2
		return v
	}
	block := &docBlock{lo: lo, hi: hi}
	block.rowPtr = make([]uint32, unitN+1)
	for i := range block.rowPtr {
		block.rowPtr[i] = readU32()
	}
	block.entries = make([]rmDocEntry, entryN)
	for i := range block.entries {
		entry := &block.entries[i]
		entry.id = readU64()
		entry.group = readU64()
		entry.posOff = readU32()
		entry.posLen = readU32()
		entry.tf = int32(readU32())
		entry.bits = readU16()
		entry.flags = body[offset]
		entry.roleExt = body[offset+1]
		offset += 2
	}
	block.posPool = make([]uint32, posN)
	for i := range block.posPool {
		block.posPool[i] = readU32()
	}
	if wideN > 0 {
		block.widePos = make(map[uint32]TermPosition, wideN)
	}
	for i := 0; i < wideN; i++ {
		at := readU32()
		sentence := int64(readU64())
		slot := int64(readU64())
		block.widePos[at] = TermPosition{Sentence: int(sentence), Slot: int(slot)}
	}
	if err := verifyDocBlock(block, unitN, entryN, posN); err != nil {
		return nil, err
	}
	return block, nil
}

// verifyDocBlock 交叉校验行指针、位置偏移与宽偏移，拒绝越界块文件。
func verifyDocBlock(block *docBlock, unitN, entryN, posN int) error {
	if len(block.rowPtr) != unitN+1 || int(block.rowPtr[0]) != 0 || int(block.rowPtr[unitN]) != entryN {
		return errors.New("doc block rowPtr does not span the entry rows")
	}
	for i := 0; i < unitN; i++ {
		if block.rowPtr[i] > block.rowPtr[i+1] {
			return errors.New("doc block rowPtr is not monotonic")
		}
	}
	for i := range block.entries {
		entry := block.entries[i]
		if int(entry.posOff)+int(entry.posLen) > posN {
			return errors.New("doc block entry positions exceed the pool")
		}
	}
	for offset := range block.widePos {
		if int(offset) >= posN {
			return errors.New("doc block wide offset exceeds the pool")
		}
	}
	return nil
}
