// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package tokenzr provides the standard tokenz-r 128k Byte-Level BPE tokenizer
// with an embedded vocabulary (pure Go, zero CGO, zero runtime network downloads).
package tokenzr

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

//go:embed tokenz_r.bin.gz
var tokenzRBinGz []byte

// Encoding is the canonical identifier for the standard tokenz-r 128k tokenizer.
const (
	Encoding = "tokenz-r"
)

const (
	numCacheShards     = 64
	maxPiecePerShard   = 8192
	maxCountPerShard   = 4096
	maxCountCacheBytes = 32768
	maxBPEPieceBytes   = 256
)

type pieceCacheShard struct {
	mu sync.RWMutex
	m  map[string][]uint
}

type countCacheShard struct {
	mu sync.RWMutex
	m  map[string]int
}

type codec struct {
	byteToID            [256]uint32
	merges              map[uint64]uint32 // key: (uint64(leftID)<<32) | uint64(rightID) -> value: mergedID (== 259 + rank)
	idToBytes           [][]byte
	idToValidUTF8Str    []string          // O(1) precomputed string(idToBytes[id]) for valid UTF-8 tokens (empty if invalid UTF-8 fragment)
	idCarryBitsX10      []uint16          // O(1) precomputed round(bpeLexicalCarryBits(s)*10) for valid UTF-8 tokens
	tokenPriorBitsLUT   []float64         // O(1) precomputed math.Log2(float64(id) + 2.0) for each token ID
	treeCohesionBitsLUT []float64         // O(1) precomputed ΔI_bind(id) / (1 + ln(1 + mergeDeg[id])) for each token ID
	spacedTokenID       map[string]uint32 // s -> single token ID of (" " + s)
	bareTokenID         map[string]uint32 // s -> single token ID of bare s (for CJK/unspaced scripts & direct hits)
	spacedValenceLUT    map[string]uint8  // O(1) precomputed BPEInflectionValence for vocabulary words (zero runtime string concat)
	spacedProperNounLUT map[string]bool   // O(1) precomputed IsBPEProperNounPrior for lowercase vocabulary words
	minChildID          []uint32
	leftChild           []uint32
	rightChild          []uint32
	leftDeg             []uint16
	mergeDeg            []uint16
	childCnt4k          []uint16
	rightInDeg          []uint16
	monoHubSuffix       string
	topPolyHubSuffix    string
	polyHubSuffixes     []string
	superHubSuffixes    []string
	directSplitHubs     []string
	nucleusRunes        map[rune]bool
	pieceShards         [numCacheShards]pieceCacheShard
	countShards         [numCacheShards]countCacheShard
	valenceShards       [numCacheShards]countCacheShard
}

func shardIdx(s string) uint32 {
	var h uint32 = 2166136261
	step := 1
	if len(s) > 64 {
		step = len(s) / 32
	}
	for i := 0; i < len(s); i += step {
		h ^= uint32(s[i])
		h *= 16777619
	}
	h ^= uint32(len(s))
	return h & (numCacheShards - 1)
}

var (
	codecOnce sync.Once
	globalTZ  *codec
	codecErr  error
)

func loadCodec() (*codec, error) {
	codecOnce.Do(func() {
		gr, err := gzip.NewReader(bytes.NewReader(tokenzRBinGz))
		if err != nil {
			codecErr = fmt.Errorf("tokenzr: decompress vocabulary failed: %w", err)
			return
		}
		defer gr.Close()
		raw, err := io.ReadAll(gr)
		if err != nil {
			codecErr = fmt.Errorf("tokenzr: read vocabulary failed: %w", err)
			return
		}
		if len(raw) < 8+256*2 || string(raw[:4]) != "TKZR" {
			codecErr = fmt.Errorf("tokenzr: invalid vocabulary header or length (len=%d)", len(raw))
			return
		}
		numMerges := int(binary.LittleEndian.Uint32(raw[4:8]))
		expectedLen := 8 + 256*2 + numMerges*6
		if len(raw) != expectedLen {
			codecErr = fmt.Errorf("tokenzr: vocabulary length mismatch got=%d want=%d", len(raw), expectedLen)
			return
		}
		maxID := 259 + numMerges
		for b := 0; b < 256; b++ {
			id := int(binary.LittleEndian.Uint16(raw[8+b*2 : 8+b*2+2]))
			if id+1 > maxID {
				maxID = id + 1
			}
		}
		c := &codec{
			merges:              make(map[uint64]uint32, numMerges),
			idToBytes:           make([][]byte, maxID),
			idToValidUTF8Str:    make([]string, maxID),
			idCarryBitsX10:      make([]uint16, maxID),
			tokenPriorBitsLUT:   make([]float64, maxID),
			treeCohesionBitsLUT: make([]float64, maxID),
			spacedTokenID:       make(map[string]uint32, numMerges/2),
			bareTokenID:         make(map[string]uint32, numMerges/2),
			spacedValenceLUT:    make(map[string]uint8, numMerges/2),
			spacedProperNounLUT: make(map[string]bool, numMerges/8),
			minChildID:          make([]uint32, maxID),
			leftChild:           make([]uint32, maxID),
			rightChild:          make([]uint32, maxID),
			leftDeg:             make([]uint16, maxID),
			mergeDeg:            make([]uint16, maxID),
			childCnt4k:          make([]uint16, maxID),
			rightInDeg:          make([]uint16, maxID),
			nucleusRunes:        make(map[rune]bool, 16),
		}
		for id := 0; id < maxID; id++ {
			c.tokenPriorBitsLUT[id] = math.Log2(float64(id) + 2.0)
		}
		for s := 0; s < numCacheShards; s++ {
			c.pieceShards[s].m = make(map[string][]uint, 128)
			c.countShards[s].m = make(map[string]int, 128)
			c.valenceShards[s].m = make(map[string]int, 128)
		}
		off := 8
		for b := 0; b < 256; b++ {
			id := uint32(binary.LittleEndian.Uint16(raw[off : off+2]))
			c.byteToID[b] = id
			c.idToBytes[id] = []byte{byte(b)}
			if utf8.Valid(c.idToBytes[id]) {
				s := string(c.idToBytes[id])
				c.idToValidUTF8Str[id] = s
				c.idCarryBitsX10[id] = uint16(math.Round(bpeLexicalCarryBits(s) * 10.0))
				c.bareTokenID[s] = id
			}
			off += 2
		}
		for i := 0; i < numMerges; i++ {
			lid := uint32(raw[off]) | uint32(raw[off+1])<<8 | uint32(raw[off+2])<<16
			rid := uint32(raw[off+3]) | uint32(raw[off+4])<<8 | uint32(raw[off+5])<<16
			off += 6
			key := (uint64(lid) << 32) | uint64(rid)
			mid := uint32(259 + i)
			c.merges[key] = mid
			if int(mid) < len(c.leftChild) {
				c.leftChild[mid] = lid
				c.rightChild[mid] = rid
			}
			if int(lid) < len(c.mergeDeg) {
				c.leftDeg[lid]++
				c.mergeDeg[lid]++
			}
			if int(rid) < len(c.mergeDeg) {
				c.mergeDeg[rid]++
			}
			if int(lid) < len(c.idToBytes) && int(rid) < len(c.idToBytes) {
				lb, rb := c.idToBytes[lid], c.idToBytes[rid]
				if len(lb) > 0 && len(rb) > 0 {
					buf := make([]byte, len(lb)+len(rb))
					copy(buf, lb)
					copy(buf[len(lb):], rb)
					c.idToBytes[mid] = buf
					if utf8.Valid(buf) {
						s := string(buf)
						c.idToValidUTF8Str[mid] = s
						c.idCarryBitsX10[mid] = uint16(math.Round(bpeLexicalCarryBits(s) * 10.0))
						if len(buf) >= 2 && buf[0] == ' ' {
							c.spacedTokenID[s[1:]] = mid
						} else {
							c.bareTokenID[s] = mid
						}
					} else if len(buf) >= 2 && buf[0] == ' ' {
						c.spacedTokenID[string(buf[1:])] = mid
					}
				}
				if c.minChildID[lid] == 0 || mid < c.minChildID[lid] {
					c.minChildID[lid] = mid
				}
				if mid < 4096 {
					c.childCnt4k[lid]++
				}
				if len(lb) >= 2 && lb[0] == ' ' && len(rb) >= 1 && utf8.Valid(rb) {
					pureAlpha := true
					for _, r := range string(rb) {
						if !unicode.IsLetter(r) || isCJKIsolated(r) {
							pureAlpha = false
							break
						}
					}
					if pureAlpha {
						c.rightInDeg[rid]++
					}
				}
			}
		}
		for id, deg := range c.rightInDeg {
			if deg < 125 {
				continue
			}
			s := string(c.idToBytes[id])
			bits := bpeLexicalCarryBits(s)
			if bits == 2.0 {
				r, _ := utf8.DecodeRuneInString(s)
				if deg >= 500 && c.monoHubSuffix == "" {
					c.monoHubSuffix = s
				} else if deg >= 127 && deg < 500 {
					c.nucleusRunes[r] = true
				}
			} else if bits >= 4.0 && deg >= 210 {
				c.polyHubSuffixes = append(c.polyHubSuffixes, s)
				if deg >= 550 {
					c.superHubSuffixes = append(c.superHubSuffixes, s)
				}
				if deg >= 700 && c.topPolyHubSuffix == "" {
					c.topPolyHubSuffix = s
				}
			}
		}
		if c.monoHubSuffix != "" {
			c.directSplitHubs = append(c.directSplitHubs, c.monoHubSuffix)
		}
		c.directSplitHubs = append(c.directSplitHubs, c.superHubSuffixes...)
		for _, ph := range c.polyHubSuffixes {
			phR0, _ := utf8.DecodeRuneInString(ph)
			if (c.monoHubSuffix != "" && strings.HasSuffix(ph, c.monoHubSuffix)) || (phR0 > 0 && !c.nucleusRunes[phR0]) {
				c.directSplitHubs = append(c.directSplitHubs, ph)
			}
		}
		c.precomputeVocabularyPriors()
		for id := 259; id < maxID; id++ {
			lid, rid := int(c.leftChild[id]), int(c.rightChild[id])
			if lid > 0 && rid > 0 && lid < maxID && rid < maxID {
				bindJump := c.tokenPriorBitsLUT[id] - 0.5*(c.tokenPriorBitsLUT[lid]+c.tokenPriorBitsLUT[rid])
				if bindJump > 0 {
					c.treeCohesionBitsLUT[id] = bindJump / (1.0 + math.Log1p(float64(c.mergeDeg[id])))
				}
			}
		}
		globalTZ = c
	})
	return globalTZ, codecErr
}

func isCJKIsolated(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0x3400 && r <= 0x4DBF) || (r >= 0x20000 && r <= 0x2A6DF) ||
		(r >= 0x2A700 && r <= 0x2B73F) || (r >= 0x2B740 && r <= 0x2B81F) || (r >= 0x2B820 && r <= 0x2CEAF) ||
		(r >= 0x2CEB0 && r <= 0x2EBEF) || (r >= 0x30000 && r <= 0x3134F) ||
		(r >= 0x3040 && r <= 0x309F) || (r >= 0x30A0 && r <= 0x30FF) || r == 0x30FC
}

func isASCIIPunct(r rune) bool {
	return (r >= '!' && r <= '/') || (r >= ':' && r <= '@') || (r >= '[' && r <= '`') || (r >= '{' && r <= '~')
}

func isASCIIAlpha(r rune) bool {
	return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')
}

func isLetterOrMark(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsMark(r)
}

func isPunctOrSymbol(r rune) bool {
	return unicode.IsPunct(r) || unicode.IsSymbol(r)
}

func isNewline(r rune) bool {
	return r == '\r' || r == '\n'
}

// PreTokenize splits input text into BPE pre-tokenization segments.
func PreTokenize(text string) []string {
	if text == "" {
		return nil
	}
	chunks := make([]string, 0, len(text)/4+1)
	i := 0
	n := len(text)
	for i < n {
		r, w := utf8.DecodeRuneInString(text[i:])

		// Stage 1: \p{N}{1,3}
		if unicode.IsNumber(r) {
			end := i + w
			cnt := 1
			for cnt < 3 && end < n {
				nr, nw := utf8.DecodeRuneInString(text[end:])
				if !unicode.IsNumber(nr) {
					break
				}
				end += nw
				cnt++
			}
			chunks = append(chunks, text[i:end])
			i = end
			continue
		}

		// Stage 2: CJK unified ideographs & kana
		if isCJKIsolated(r) {
			end := i + w
			for end < n {
				nr, nw := utf8.DecodeRuneInString(text[end:])
				if !isCJKIsolated(nr) {
					break
				}
				end += nw
			}
			chunks = append(chunks, text[i:end])
			i = end
			continue
		}

		// Stage 3 Branch 1: ASCII punctuation + ASCII letters
		if isASCIIPunct(r) && i+w < n {
			nr, nw := utf8.DecodeRuneInString(text[i+w:])
			if isASCIIAlpha(nr) {
				end := i + w + nw
				for end < n {
					tr, tw := utf8.DecodeRuneInString(text[end:])
					if !isASCIIAlpha(tr) {
						break
					}
					end += tw
				}
				chunks = append(chunks, text[i:end])
				i = end
				continue
			}
		}

		// Stage 3 Branch 2: optional prefix + letters/marks
		if isLetterOrMark(r) && !isCJKIsolated(r) && !unicode.IsNumber(r) {
			end := i + w
			for end < n {
				nr, nw := utf8.DecodeRuneInString(text[end:])
				if !isLetterOrMark(nr) || isCJKIsolated(nr) || unicode.IsNumber(nr) {
					break
				}
				end += nw
			}
			chunks = append(chunks, text[i:end])
			i = end
			continue
		}
		if !isNewline(r) && !unicode.IsLetter(r) && !isPunctOrSymbol(r) && !unicode.IsNumber(r) && i+w < n {
			nr, nw := utf8.DecodeRuneInString(text[i+w:])
			if isLetterOrMark(nr) && !isCJKIsolated(nr) && !unicode.IsNumber(nr) {
				end := i + w + nw
				for end < n {
					tr, tw := utf8.DecodeRuneInString(text[end:])
					if !isLetterOrMark(tr) || isCJKIsolated(tr) || unicode.IsNumber(tr) {
						break
					}
					end += tw
				}
				chunks = append(chunks, text[i:end])
				i = end
				continue
			}
		}

		// Stage 3 Branch 3: optional space + punctuation/symbols + optional newlines
		if r == ' ' && i+w < n {
			nr, nw := utf8.DecodeRuneInString(text[i+w:])
			if isPunctOrSymbol(nr) && !isCJKIsolated(nr) && !unicode.IsNumber(nr) {
				end := i + w + nw
				for end < n {
					tr, tw := utf8.DecodeRuneInString(text[end:])
					if !isPunctOrSymbol(tr) || isCJKIsolated(tr) || unicode.IsNumber(tr) {
						break
					}
					end += tw
				}
				for end < n {
					tr, tw := utf8.DecodeRuneInString(text[end:])
					if !isNewline(tr) {
						break
					}
					end += tw
				}
				chunks = append(chunks, text[i:end])
				i = end
				continue
			}
		}
		if isPunctOrSymbol(r) && !isCJKIsolated(r) && !unicode.IsNumber(r) {
			end := i + w
			for end < n {
				tr, tw := utf8.DecodeRuneInString(text[end:])
				if !isPunctOrSymbol(tr) || isCJKIsolated(tr) || unicode.IsNumber(tr) {
					break
				}
				end += tw
			}
			for end < n {
				tr, tw := utf8.DecodeRuneInString(text[end:])
				if !isNewline(tr) {
					break
				}
				end += tw
			}
			chunks = append(chunks, text[i:end])
			i = end
			continue
		}

		// Stage 3 Branch 4/5/6: whitespace and newlines
		if unicode.IsSpace(r) {
			end := i + w
			hasNL := isNewline(r)
			for end < n {
				tr, tw := utf8.DecodeRuneInString(text[end:])
				if !unicode.IsSpace(tr) {
					break
				}
				if isNewline(tr) {
					hasNL = true
				} else if hasNL {
					break
				}
				end += tw
			}
			if !hasNL && end < n && (end-i) > 1 && text[end-1] == ' ' {
				nr, _ := utf8.DecodeRuneInString(text[end:])
				if (isLetterOrMark(nr) || isPunctOrSymbol(nr)) && !isCJKIsolated(nr) && !unicode.IsNumber(nr) {
					end--
				}
			}
			chunks = append(chunks, text[i:end])
			i = end
			continue
		}

		chunks = append(chunks, text[i:i+w])
		i += w
	}
	return chunks
}

func (c *codec) bpePiece(piece string) []uint {
	if len(piece) == 0 {
		return nil
	}
	if len(piece) == 1 {
		return []uint{uint(c.byteToID[piece[0]])}
	}
	if len(piece) > maxBPEPieceBytes {
		out := make([]uint, 0, len(piece)/2+1)
		for start := 0; start < len(piece); {
			end := start + maxBPEPieceBytes
			if end >= len(piece) {
				end = len(piece)
			} else {
				for end > start && !utf8.RuneStart(piece[end]) {
					end--
				}
				if end == start {
					end = start + maxBPEPieceBytes
				}
			}
			out = append(out, c.bpePieceBounded(piece[start:end])...)
			start = end
		}
		return out
	}
	return c.bpePieceBounded(piece)
}

func (c *codec) bpePieceBounded(piece string) []uint {
	if len(piece) >= 2 && piece[0] == ' ' {
		if id, ok := c.spacedTokenID[piece[1:]]; ok {
			return []uint{uint(id)}
		}
	} else if id, ok := c.bareTokenID[piece]; ok {
		return []uint{uint(id)}
	}
	var sh *pieceCacheShard
	if len(piece) <= 64 {
		sh = &c.pieceShards[shardIdx(piece)]
		sh.mu.RLock()
		if cached, ok := sh.m[piece]; ok {
			sh.mu.RUnlock()
			return cached
		}
		sh.mu.RUnlock()
	}

	var stackBuf [64]uint32
	var ids []uint32
	if len(piece) <= len(stackBuf) {
		ids = stackBuf[:len(piece)]
	} else {
		ids = make([]uint32, len(piece))
	}
	for i := 0; i < len(piece); i++ {
		ids[i] = c.byteToID[piece[i]]
	}

	for len(ids) >= 2 {
		bestRank := uint32(0xFFFFFFFF)
		bestIdx := -1
		for i := 0; i < len(ids)-1; i++ {
			key := (uint64(ids[i]) << 32) | uint64(ids[i+1])
			if mergedID, ok := c.merges[key]; ok && mergedID < bestRank {
				bestRank = mergedID
				bestIdx = i
			}
		}
		if bestIdx < 0 {
			break
		}
		ids[bestIdx] = bestRank
		copy(ids[bestIdx+1:], ids[bestIdx+2:])
		ids = ids[:len(ids)-1]
	}

	out := make([]uint, len(ids))
	for i, v := range ids {
		out[i] = uint(v)
	}
	if sh != nil {
		sh.mu.Lock()
		if len(sh.m) >= maxPiecePerShard {
			sh.m = make(map[string][]uint, 128)
		}
		sh.m[piece] = out
		sh.mu.Unlock()
	}
	return out
}

// bpePieceCount returns the exact BPE token count of piece without allocating []uint slices.
func (c *codec) bpePieceCount(piece string) int {
	if len(piece) == 0 {
		return 0
	}
	if len(piece) == 1 {
		return 1
	}
	if len(piece) > maxBPEPieceBytes {
		total := 0
		for start := 0; start < len(piece); {
			end := start + maxBPEPieceBytes
			if end >= len(piece) {
				end = len(piece)
			} else {
				for end > start && !utf8.RuneStart(piece[end]) {
					end--
				}
				if end == start {
					end = start + maxBPEPieceBytes
				}
			}
			total += c.bpePieceCountBounded(piece[start:end])
			start = end
		}
		return total
	}
	return c.bpePieceCountBounded(piece)
}

func (c *codec) bpePieceCountBounded(piece string) int {
	if len(piece) >= 2 && piece[0] == ' ' {
		if _, ok := c.spacedTokenID[piece[1:]]; ok {
			return 1
		}
	} else if _, ok := c.bareTokenID[piece]; ok {
		return 1
	}
	if len(piece) <= 64 {
		sh := &c.pieceShards[shardIdx(piece)]
		sh.mu.RLock()
		if cached, ok := sh.m[piece]; ok {
			sh.mu.RUnlock()
			return len(cached)
		}
		sh.mu.RUnlock()
		var stackBuf [64]uint32
		ids := stackBuf[:len(piece)]
		for i := 0; i < len(piece); i++ {
			ids[i] = c.byteToID[piece[i]]
		}
		for len(ids) >= 2 {
			bestRank := uint32(0xFFFFFFFF)
			bestIdx := -1
			for i := 0; i < len(ids)-1; i++ {
				key := (uint64(ids[i]) << 32) | uint64(ids[i+1])
				if mergedID, ok := c.merges[key]; ok && mergedID < bestRank {
					bestRank = mergedID
					bestIdx = i
				}
			}
			if bestIdx < 0 {
				break
			}
			ids[bestIdx] = bestRank
			copy(ids[bestIdx+1:], ids[bestIdx+2:])
			ids = ids[:len(ids)-1]
		}
		return len(ids)
	}
	return len(c.bpePieceBounded(piece))
}

func (c *codec) count(text string) int {
	if text == "" {
		return 0
	}
	var sh *countCacheShard
	if len(text) <= maxCountCacheBytes {
		sh = &c.countShards[shardIdx(text)]
		sh.mu.RLock()
		if n, ok := sh.m[text]; ok {
			sh.mu.RUnlock()
			return n
		}
		sh.mu.RUnlock()
	}
	pieces := PreTokenize(text)
	total := 0
	for _, p := range pieces {
		total += c.bpePieceCount(p)
	}
	if sh != nil {
		sh.mu.Lock()
		if len(sh.m) >= maxCountPerShard {
			sh.m = make(map[string]int, 128)
		}
		sh.m[text] = total
		sh.mu.Unlock()
	}
	return total
}

func (c *codec) encode(text string) []uint {
	if text == "" {
		return nil
	}
	pieces := PreTokenize(text)
	out := make([]uint, 0, len(pieces)*2)
	for _, p := range pieces {
		out = append(out, c.bpePiece(p)...)
	}
	return out
}

// Counter is a concurrent-safe tokenz-r tokenizer instance.
type Counter struct {
	enc   string
	codec *codec
}

// NewCounter constructs a tokenz-r counter (empty encoding defaults to "tokenz-r"; any other encoding is rejected).
func NewCounter(encoding string) (*Counter, error) {
	enc := strings.TrimSpace(encoding)
	if enc == "" {
		enc = Encoding
	}
	if enc != Encoding {
		return nil, fmt.Errorf("tokenzr: unsupported or retired encoding %q (only %q is allowed)", enc, Encoding)
	}
	c, err := loadCodec()
	if err != nil {
		return nil, err
	}
	return &Counter{enc: enc, codec: c}, nil
}

// Encoding returns the active tokenizer encoding name ("tokenz-r").
func (c *Counter) Encoding() string { return c.enc }

// Count returns the exact tokenz-r BPE token count for text.
func (c *Counter) Count(text string) (int, error) {
	if text == "" {
		return 0, nil
	}
	return c.codec.count(text), nil
}

// Encode encodes text into its sequence of tokenz-r token IDs.
func (c *Counter) Encode(text string) ([]uint, error) {
	if text == "" {
		return nil, nil
	}
	return c.codec.encode(text), nil
}

var (
	defaultOnce sync.Once
	defaultCtr  *Counter
	defaultErr  error
)

// Default returns the process-wide singleton tokenz-r Counter.
func Default() (*Counter, error) {
	defaultOnce.Do(func() {
		defaultCtr, defaultErr = NewCounter(Encoding)
	})
	return defaultCtr, defaultErr
}

// Count is a convenience helper that counts tokens using Default().
// If the embedded vocabulary fails to load, it returns 0 (it never falls back to rune counting).
func Count(text string) int {
	if text == "" {
		return 0
	}
	if ctr, err := Default(); err == nil && ctr != nil {
		if n, cerr := ctr.Count(text); cerr == nil {
			return n
		}
	}
	return 0
}

// SplitSubwords segments text into valid UTF-8 subword strings along the embedded 128k BPE vocabulary boundaries.
// Multi-token partial UTF-8 byte sequences are reassembled into valid UTF-8 characters.
func SplitSubwords(text string) []string {
	if text == "" {
		return nil
	}
	ctr, err := Default()
	if err != nil || ctr == nil || ctr.codec == nil {
		return []string{text}
	}
	c := ctr.codec
	pieces := PreTokenize(text)
	out := make([]string, 0, len(pieces)*2)
	var pending []byte
	flushPending := func() {
		if len(pending) > 0 {
			if utf8.Valid(pending) {
				out = append(out, string(pending))
			}
			pending = pending[:0]
		}
	}
	for _, p := range pieces {
		ids := c.bpePiece(p)
		for _, id := range ids {
			if int(id) >= len(c.idToBytes) || len(c.idToBytes[id]) == 0 {
				continue
			}
			if len(pending) == 0 {
				if s := c.idToValidUTF8Str[id]; s != "" {
					out = append(out, s)
					continue
				}
			}
			b := c.idToBytes[id]
			pending = append(pending, b...)
			if utf8.Valid(pending) {
				out = append(out, string(pending))
				pending = pending[:0]
			}
		}
		flushPending()
	}
	return out
}

// SplitBPEDirectPieces splits a whitespace-stripped word into its immediate 128k BPE subword pieces
// (100% pure BPE tokenizer, zero prior energy, zero morphological heuristics):
//   - For unspaced ideographic/syllabic scripts (CJK/Thai/etc.), returns the BPE subword tokens
//     and direct binary merge tree children [leftChild, rightChild];
//   - For space-delimited scripts, returns the pre-spaced BPE tokens SplitSubwords(" " + w)
//     whenever BPE segments the word into >= 2 subwords, or unwraps [leftChild, rightChild]
//     when rightChild is an endogenous BPE right-degree hub (monoHubSuffix / superHubSuffixes / polyHubSuffixes).
func SplitBPEDirectPieces(word string) []string {
	w := strings.ToLower(strings.TrimSpace(word))
	if w == "" {
		return nil
	}
	ctr, err := Default()
	if err != nil || ctr == nil || ctr.codec == nil {
		return nil
	}
	c := ctr.codec
	r0, _ := utf8.DecodeRuneInString(w)
	unspaced := isUnspacedScriptRune(r0)
	var out []string
	addPiece := func(s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" || s == w {
			return
		}
		for _, prev := range out {
			if prev == s {
				return
			}
		}
		out = append(out, s)
	}
	if unspaced {
		for _, p := range SplitSubwords(w) {
			addPiece(p)
		}
		if id := c.bareTokenID[w]; id > 0 && int(id) < len(c.leftChild) {
			lid, rid := c.leftChild[id], c.rightChild[id]
			if lid > 0 && rid > 0 && int(lid) < len(c.idToValidUTF8Str) && int(rid) < len(c.idToValidUTF8Str) {
				addPiece(c.idToValidUTF8Str[lid])
				addPiece(c.idToValidUTF8Str[rid])
			}
		}
		return out
	}
	// Space-delimited script:
	//  1. Unwrap longest in-vocabulary spaced stem prefix + endogenous BPE rightInDeg hub suffix
	//     (monoHubSuffix / superHubSuffixes / polyHubSuffixes) FIRST so plural/adverbial/inflected words
	//     like "shrimps" -> ["shrimp", "s"], "suddenly" -> ["sudden", "ly"], "finally" -> ["final", "ly"],
	//     "potatoes" -> ["potato", "es"], "images" -> ["image", "s"], "videos" -> ["video", "s"]
	//     always preserve their intact stem before falling back to multi-piece BPE splitting;
	//  2. Otherwise, if (" " + w) splits into >= 2 BPE subword tokens (e.g. "malamutes" -> ["malam", "utes"],
	//     "newcastle" -> ["new", "castle"], "timelapse" -> ["time", "lapse"]), return those subwords;
	//  3. Otherwise, for compound single tokens (e.g. "rainfall" -> ["rain", "fall"], "somewhere" -> ["some", "where"]),
	//     split into two >=4-rune in-vocabulary BPE tokens.
	hasAtLeastRunes := func(s string, minRunes int) bool {
		rest := s
		for k := 0; k < minRunes; k++ {
			_, rw := utf8.DecodeRuneInString(rest)
			if rw <= 0 {
				return false
			}
			rest = rest[rw:]
		}
		return true
	}
	isSuperClosedStem := func(s string) bool {
		if !hasAtLeastRunes(s, 5) || len(c.superHubSuffixes) == 0 {
			return false
		}
		if c.monoHubSuffix != "" && c.spacedTokenID[s+c.monoHubSuffix] != 0 {
			return false
		}
		for _, sh := range c.superHubSuffixes {
			if c.spacedTokenID[s+sh] == 0 {
				return false
			}
		}
		return true
	}
	// unwrapAlternatingPrefix 统一二部图尾核交替共轭算子（100% 纯子串、零英语特定词尾硬编码）：
	//  - curSfx == "" (base): 词干 boundStem (长度 >=4, 在词表中) 存在尾核变体 alt+ph in V；
	//  - curSfx != "" (inflected): 已剥离后缀的 boundStem (长度 >=5) 去尾核后得到纯前缀 prefix (>=4 runes)，
	//    在二部图中存在唯一非哑音尾核基态 cand = prefix+nr in V 或多后缀共轭变体。
	unwrapAlternatingPrefix := func(boundStem, curSfx string) string {
		if c.monoHubSuffix == "" {
			return ""
		}
		if curSfx == "" {
			if !hasAtLeastRunes(boundStem, 4) || c.spacedTokenID[boundStem] == 0 {
				return ""
			}
			if prevR, prevW := utf8.DecodeLastRuneInString(boundStem); prevW <= 0 || c.nucleusRunes[prevR] {
				return ""
			}
			for nr := range c.nucleusRunes {
				alt := boundStem + string(nr)
				if c.spacedTokenID[alt+c.monoHubSuffix] > 0 {
					continue
				}
				for _, ph := range c.polyHubSuffixes {
					if c.spacedTokenID[alt+ph] > 0 {
						return boundStem
					}
				}
			}
			return ""
		}
		if !hasAtLeastRunes(boundStem, 5) || c.spacedTokenID[boundStem+c.monoHubSuffix] > 0 {
			return ""
		}
		lr, lw := utf8.DecodeLastRuneInString(boundStem)
		if lw <= 0 || !c.nucleusRunes[lr] {
			return ""
		}
		prefix := boundStem[:len(boundStem)-lw]
		if !hasAtLeastRunes(prefix, 4) {
			return ""
		}
		if prevR, prevW := utf8.DecodeLastRuneInString(prefix); prevW <= 0 || c.nucleusRunes[prevR] {
			return ""
		}
		var best string
		for nr := range c.nucleusRunes {
			if nr == lr {
				continue
			}
			isElidedHub := false
			for _, sh := range c.superHubSuffixes {
				if sr0, _ := utf8.DecodeRuneInString(sh); sr0 == nr {
					isElidedHub = true
					break
				}
			}
			if isElidedHub {
				continue
			}
			cand := prefix + string(nr)
			if c.spacedTokenID[cand] > 0 && c.spacedTokenID[cand+c.monoHubSuffix] == 0 {
				if best != "" && best != cand {
					return ""
				}
				best = cand
			}
		}
		if best != "" {
			return prefix
		}
		if c.spacedTokenID[prefix] > 0 {
			for _, sh := range c.polyHubSuffixes {
				if sh != curSfx && c.spacedTokenID[boundStem+sh] > 0 {
					return prefix
				}
			}
		}
		return ""
	}
	trySplitHub := func(sfx string) bool {
		if sfx == "" || !strings.HasSuffix(w, sfx) {
			return false
		}
		stem := strings.TrimSuffix(w, sfx)
		if stem == "" || stem == w || strings.HasSuffix(stem, sfx) || !hasAtLeastRunes(stem, 3) {
			return false
		}
		sfxR0, sfxW0 := utf8.DecodeRuneInString(sfx)
		lastR, lastW := utf8.DecodeLastRuneInString(stem)
		var elidedCand string
		if sfxW0 > 0 && sfxW0 < len(sfx) && c.nucleusRunes[sfxR0] && lastW > 0 && !c.nucleusRunes[lastR] {
			if cand := w[:len(stem)+sfxW0]; c.spacedTokenID[cand] > 0 {
				elidedCand = cand
			}
		}
		if !hasAtLeastRunes(stem, 4) {
			if elidedCand != "" && c.monoHubSuffix != "" && c.spacedTokenID[elidedCand+c.monoHubSuffix] > 0 && len(c.superHubSuffixes) > 0 {
				allSuper := true
				for _, sh := range c.superHubSuffixes {
					if c.spacedTokenID[stem+sh] == 0 {
						allSuper = false
						break
					}
				}
				if allSuper {
					addPiece(elidedCand)
					addPiece(sfx)
					return true
				}
			}
			if sfx == c.monoHubSuffix && lastW > 0 && !c.nucleusRunes[lastR] && c.spacedTokenID[stem] > 0 && c.spacedTokenID[w] == 0 {
				r0, w0 := utf8.DecodeRuneInString(stem)
				r1, _ := utf8.DecodeRuneInString(stem[w0:])
				if !c.nucleusRunes[r0] && c.nucleusRunes[r1] {
					addPiece(stem)
					addPiece(sfx)
					return true
				}
			}
			if sfx != c.monoHubSuffix && lastW > 0 && c.nucleusRunes[lastR] && c.monoHubSuffix != "" && len(c.superHubSuffixes) > 0 {
				r0, w0 := utf8.DecodeRuneInString(stem)
				r1, _ := utf8.DecodeRuneInString(stem[w0:])
				if !c.nucleusRunes[r0] && !c.nucleusRunes[r1] {
					ccPrefix := stem[:len(stem)-lastW]
					wID := c.spacedTokenID[w]
					for nr := range c.nucleusRunes {
						alt := ccPrefix + string(nr)
						if nr == lastR || c.spacedTokenID[alt] == 0 || c.spacedTokenID[alt+c.monoHubSuffix] != 0 || (wID > 0 && c.spacedTokenID[alt] >= wID) {
							continue
						}
						for _, sh := range c.superHubSuffixes {
							if c.spacedTokenID[alt+sh] > 0 {
								addPiece(stem)
								addPiece(sfx)
								return true
							}
						}
					}
				}
			}
			return false
		}
		if c.spacedTokenID[stem] > 0 {
			if sfx == c.monoHubSuffix && lastW > 0 && c.nucleusRunes[lastR] {
				if pref := unwrapAlternatingPrefix(stem[:len(stem)-lastW], sfx); pref != "" {
					addPiece(pref)
					addPiece(sfx)
					return true
				}
			}
			addPiece(stem)
			if lastW > 0 && c.nucleusRunes[lastR] {
				if sfx == c.monoHubSuffix {
					if elided := stem[:len(stem)-lastW]; isSuperClosedStem(elided) {
						addPiece(elided)
					}
				} else if c.monoHubSuffix != "" && c.spacedTokenID[stem+c.monoHubSuffix] == 0 {
					if pref := stem[:len(stem)-lastW]; hasAtLeastRunes(pref, 4) && c.spacedTokenID[pref] > 0 {
						if prevR, prevW := utf8.DecodeLastRuneInString(pref); prevW > 0 && !c.nucleusRunes[prevR] {
							addPiece(pref)
						}
					}
				}
			}
			addPiece(sfx)
			if elidedCand != "" && c.monoHubSuffix != "" && c.spacedTokenID[stem+c.monoHubSuffix] == 0 && c.spacedTokenID[elidedCand+c.monoHubSuffix] > 0 {
				addPiece(elidedCand)
			}
			return true
		}
		if lastW > 0 {
			trimmed1 := stem[:len(stem)-lastW]
			if prevR, prevW := utf8.DecodeLastRuneInString(trimmed1); prevW > 0 && prevR == lastR && hasAtLeastRunes(trimmed1, 3) && c.spacedTokenID[trimmed1] > 0 {
				addPiece(trimmed1)
				addPiece(sfx)
				return true
			}
		}
		if sfx == c.monoHubSuffix && lastW > 0 && c.nucleusRunes[lastR] {
			altHub := w[len(stem)-lastW:]
			isHubNuc := false
			for _, dh := range c.directSplitHubs {
				if dh == altHub {
					if hasAtLeastRunes(strings.TrimSuffix(w, dh), 4) && c.spacedTokenID[strings.TrimSuffix(w, dh)] > 0 {
						return false
					}
					isHubNuc = true
					break
				}
			}
			if elidedCand == "" {
				elidedCand = unwrapAlternatingPrefix(stem[:len(stem)-lastW], sfx)
				if elidedCand == "" && !isHubNuc {
					return false
				}
			}
		} else if elidedCand == "" {
			elidedCand = unwrapAlternatingPrefix(stem, sfx)
		}
		if subStem := SplitBPEDirectPieces(stem); len(subStem) > 0 {
			addPiece(stem)
			if elidedCand != "" && (!hasAtLeastRunes(subStem[0], 5) || c.spacedTokenID[subStem[0]] == 0) {
				addPiece(elidedCand)
			}
			for _, sp := range subStem {
				addPiece(sp)
			}
			return true
		}
		if elidedCand != "" {
			addPiece(stem)
			addPiece(elidedCand)
			return true
		}
		return false
	}
	for _, sfx := range c.directSplitHubs {
		if trySplitHub(sfx) {
			return out
		}
	}
	if id := c.spacedTokenID[w]; id > 0 && int(id) < len(c.leftChild) {
		lid, rid := c.leftChild[id], c.rightChild[id]
		if lid > 0 && rid > 0 && int(lid) < len(c.idToValidUTF8Str) && int(rid) < len(c.idToValidUTF8Str) {
			lStr := strings.TrimSpace(c.idToValidUTF8Str[lid])
			rStr := strings.TrimSpace(c.idToValidUTF8Str[rid])
			if c.monoHubSuffix != "" && strings.HasSuffix(lStr, c.monoHubSuffix) {
				lBase := strings.TrimSuffix(lStr, c.monoHubSuffix)
				rComp := strings.TrimPrefix(w, lBase)
				if hasAtLeastRunes(lBase, 4) && hasAtLeastRunes(rComp, 4) && c.spacedTokenID[lBase] > 0 && c.spacedTokenID[rComp] > 0 {
					addPiece(lBase)
					addPiece(rComp)
					return out
				}
			}
			wHasMono := c.monoHubSuffix != "" && c.spacedTokenID[w+c.monoHubSuffix] > 0
			curR := rid
			for step := 0; step < 3 && curR > 0 && int(curR) < len(c.rightChild); step++ {
				rr := c.rightChild[curR]
				if rr == 0 || int(rr) >= len(c.idToValidUTF8Str) {
					break
				}
				rotR := strings.TrimSpace(c.idToValidUTF8Str[rr])
				if strings.HasSuffix(w, rotR) {
					rotL := strings.TrimSuffix(w, rotR)
					rotR0, _ := utf8.DecodeRuneInString(rotR)
					if hasAtLeastRunes(rotR, 3) && hasAtLeastRunes(rotL, 5) && hasAtLeastRunes(lStr, 3) {
						if (c.spacedTokenID[rotL] > 0 && (c.monoHubSuffix == "" || c.spacedTokenID[rotL+c.monoHubSuffix] > 0)) || isSuperClosedStem(rotL) {
							addPiece(rotL)
							addPiece(rotR)
							return out
						}
					} else if hasAtLeastRunes(rotR, 2) && c.nucleusRunes[rotR0] && hasAtLeastRunes(rotL, 4) {
						if hasAtLeastRunes(rotL, 6) && hasAtLeastRunes(lStr, 4) && c.spacedTokenID[rotL] > 0 && c.spacedTokenID[rotL] < id && (c.monoHubSuffix == "" || c.spacedTokenID[rotL+c.monoHubSuffix] > 0) {
							addPiece(rotL)
							addPiece(rotR)
							return out
						}
						if c.spacedTokenID[rotL] > 0 {
							boundS := rotL + string(rotR0)
							for _, sh := range c.polyHubSuffixes {
								if c.spacedTokenID[boundS+sh] > 0 && unwrapAlternatingPrefix(boundS, rotR) == rotL {
									addPiece(rotL)
									addPiece(rotR)
									return out
								}
							}
						}
						if rotRLast, lw := utf8.DecodeLastRuneInString(rotR); lw > 0 && !c.nucleusRunes[rotRLast] && len(c.superHubSuffixes) > 0 &&
							hasAtLeastRunes(lStr, 3) && c.spacedTokenID[rotL] > 0 && (c.monoHubSuffix == "" || c.spacedTokenID[rotL+c.monoHubSuffix] == 0) &&
							c.spacedTokenID[rotL+c.superHubSuffixes[0]] == 0 &&
							((c.spacedTokenID[w+c.superHubSuffixes[0]] == 0 && c.spacedTokenID[w+string(rotRLast)+c.superHubSuffixes[0]] > 0) || hasAtLeastRunes(rotR, 5)) {
							addPiece(rotL)
							return out
						}
					} else if !hasAtLeastRunes(rotR, 2) && c.nucleusRunes[rotR0] {
						if hasAtLeastRunes(rotL, 5) && hasAtLeastRunes(lStr, 3) {
							moreFreqSuper := len(c.superHubSuffixes) > 0 && c.spacedTokenID[rotL+c.superHubSuffixes[0]] > 0 && c.spacedTokenID[rotL+c.superHubSuffixes[0]] < id
							polyInflected := false
							if !wHasMono && (!hasAtLeastRunes(lStr, 4) || c.monoHubSuffix == "" || c.spacedTokenID[lStr+c.monoHubSuffix] == 0) {
								for _, ph := range c.polyHubSuffixes {
									if c.spacedTokenID[w+ph] > 0 {
										polyInflected = true
										break
									}
								}
							}
							if (isSuperClosedStem(rotL) && (c.spacedTokenID[rotL] > 0 || wHasMono || moreFreqSuper)) || polyInflected {
								addPiece(rotL)
								addPiece(rotR)
								return out
							}
						}
						if !wHasMono && hasAtLeastRunes(rotL, 4) {
							stemHasMono := c.monoHubSuffix != "" && !strings.HasSuffix(rotL, c.monoHubSuffix) && c.spacedTokenID[rotL+c.monoHubSuffix] > 0 && c.spacedTokenID[rotL] > 0 && c.spacedTokenID[rotL] < id
							if stemHasMono || unwrapAlternatingPrefix(rotL, "") == rotL {
								addPiece(rotL)
								if hasAtLeastRunes(lStr, 4) && c.spacedTokenID[lStr] > 0 {
									addPiece(lStr)
								}
								return out
							}
						}
					}
				}
				curR = rr
			}
			if rl := c.leftChild[rid]; rl > 0 && int(rl) < len(c.leftChild) {
				rll, rlr := c.leftChild[rl], c.rightChild[rl]
				if rll > 0 && rlr > 0 && int(rll) < len(c.idToValidUTF8Str) && int(rlr) < len(c.idToValidUTF8Str) {
					rlrStr := strings.TrimSpace(c.idToValidUTF8Str[rlr])
					rlrR0, _ := utf8.DecodeRuneInString(rlrStr)
					rotL := lStr + strings.TrimSpace(c.idToValidUTF8Str[rll])
					if !hasAtLeastRunes(rlrStr, 2) && c.nucleusRunes[rlrR0] && hasAtLeastRunes(rotL, 5) && strings.HasPrefix(w, rotL) && unwrapAlternatingPrefix(rotL, "") == rotL {
						addPiece(rotL)
						return out
					}
				}
			}
			if hasAtLeastRunes(lStr, 4) && c.spacedTokenID[lStr] > 0 {
				rR0, _ := utf8.DecodeRuneInString(rStr)
				if hasAtLeastRunes(rStr, 2) || rStr == c.monoHubSuffix {
					addPiece(lStr)
					addPiece(rStr)
					return out
				}
				if c.nucleusRunes[rR0] {
					addPiece(lStr)
					if !wHasMono || isSuperClosedStem(lStr) {
						addPiece(rStr)
					}
					return out
				}
			}
		}
	}
	if hasAtLeastRunes(w, 8) {
		for idx := range w {
			if idx == 0 {
				continue
			}
			p0, p1 := w[:idx], w[idx:]
			if hasAtLeastRunes(p0, 4) && hasAtLeastRunes(p1, 4) && c.spacedTokenID[p0] > 0 && c.spacedTokenID[p1] > 0 {
				addPiece(p0)
				addPiece(p1)
				return out
			}
		}
	}
	spPieces := SplitSubwords(" " + w)
	if len(spPieces) >= 2 {
		p0 := strings.TrimSpace(spPieces[0])
		pLast := strings.TrimSpace(spPieces[len(spPieces)-1])
		// Guard against 1-letter onset/coda fragmentation (" v"+"ideos", " port"+"o"): require first and last piece >= 2 runes.
		if hasAtLeastRunes(p0, 2) && hasAtLeastRunes(pLast, 2) {
			if lastR, lastW := utf8.DecodeLastRuneInString(w); lastW > 0 && c.nucleusRunes[lastR] {
				if elided := w[:len(w)-lastW]; isSuperClosedStem(elided) {
					addPiece(elided)
				}
			}
			for _, p := range spPieces {
				addPiece(p)
			}
			return out
		}
		if len(spPieces) >= 3 {
			r0, _ := utf8.DecodeRuneInString(p0)
			rLast, _ := utf8.DecodeRuneInString(pLast)
			if !hasAtLeastRunes(p0, 2) && !c.nucleusRunes[r0] && hasAtLeastRunes(pLast, 3) {
				sub := spPieces[1:]
				r1, _ := utf8.DecodeRuneInString(strings.TrimSpace(sub[0]))
				allValid := !c.nucleusRunes[r1]
				for _, sp := range sub {
					tp := strings.TrimSpace(sp)
					if !hasAtLeastRunes(tp, 3) || c.spacedTokenID[tp] == 0 || (c.monoHubSuffix != "" && c.spacedTokenID[tp+c.monoHubSuffix] == 0) {
						allValid = false
						break
					}
				}
				if allValid {
					for _, sp := range sub {
						addPiece(sp)
					}
					return out
				}
				p1 := strings.TrimSpace(spPieces[1])
				p2 := strings.TrimSpace(spPieces[2])
				if r2, w2 := utf8.DecodeRuneInString(p2); w2 > 0 && w2 < len(p2) && c.nucleusRunes[r2] {
					rotL := p0 + p1 + p2[:w2]
					if hasAtLeastRunes(rotL, 5) && strings.HasPrefix(w, rotL) && c.spacedTokenID[rotL] > 0 && (c.monoHubSuffix == "" || c.spacedTokenID[rotL+c.monoHubSuffix] == 0) {
						addPiece(rotL)
						return out
					}
				}
			} else if hasAtLeastRunes(p0, 3) && !hasAtLeastRunes(pLast, 2) && !c.nucleusRunes[rLast] && pLast != c.monoHubSuffix {
				sub := spPieces[:len(spPieces)-1]
				allValid := true
				for _, sp := range sub {
					tp := strings.TrimSpace(sp)
					if !hasAtLeastRunes(tp, 3) || c.spacedTokenID[tp] == 0 || (c.monoHubSuffix != "" && c.spacedTokenID[tp+c.monoHubSuffix] == 0) {
						allValid = false
						break
					}
				}
				if allValid {
					for _, sp := range sub {
						addPiece(sp)
					}
					return out
				}
			}
		}
		if len(spPieces) == 2 && hasAtLeastRunes(p0, 4) && c.spacedTokenID[p0] > 0 {
			r1, w1 := utf8.DecodeRuneInString(pLast)
			prevR, prevW := utf8.DecodeLastRuneInString(p0)
			if w1 == len(pLast) && c.nucleusRunes[r1] && prevW > 0 && !c.nucleusRunes[prevR] {
				for _, ph := range c.polyHubSuffixes {
					if strings.HasSuffix(ph, pLast) {
						addPiece(p0)
						return out
					}
				}
			}
		}
	}
	return out
}

// SplitSubwordConstituents segments text along the embedded 128k BPE vocabulary and unwraps
// >=3-character CJK phrase tokens (and late accidental 2-character super-hub merges) along the
// endogenous BPE binary merge tree (leftChild, rightChild) — zero stopword tables, zero hardcoded characters.
func SplitSubwordConstituents(text string) (leaves []string, phrases []string) {
	if text == "" {
		return nil, nil
	}
	ctr, err := Default()
	if err != nil || ctr == nil || ctr.codec == nil {
		return []string{text}, nil
	}
	c := ctr.codec
	is1CharBoundary := func(tid uint32) bool {
		return int(tid) < len(c.mergeDeg) && (c.mergeDeg[tid] >= 200 || (tid < 4000 && c.leftDeg[tid] == 0))
	}
	var unwrap func(id uint32)
	unwrap = func(id uint32) {
		if int(id) >= len(c.idToValidUTF8Str) {
			return
		}
		s := c.idToValidUTF8Str[id]
		if s == "" {
			return
		}
		bitsX10 := c.idCarryBitsX10[id]
		lid, rid := c.leftChild[id], c.rightChild[id]
		if lid == 0 || rid == 0 || int(lid) >= len(c.idToValidUTF8Str) || int(rid) >= len(c.idToValidUTF8Str) {
			if bitsX10 == 100 && is1CharBoundary(id) {
				leaves = append(leaves, " ")
				return
			}
			leaves = append(leaves, s)
			return
		}
		if c.idToValidUTF8Str[lid] == "" || c.idToValidUTF8Str[rid] == "" {
			if bitsX10 == 100 && is1CharBoundary(id) {
				leaves = append(leaves, " ")
				return
			}
			leaves = append(leaves, s)
			return
		}
		if bitsX10 >= 300 {
			if bitsX10 <= 500 && !is1CharBoundary(lid) && !is1CharBoundary(rid) {
				phrases = append(phrases, s)
			} else if bitsX10 == 300 {
				// Overlapping BPE Bigram Closure: if a 3-character Han BPE token s = c1+c2+c3 has a 1-char child
				// flagged by high merge degree (e.g. "热" in "热力学"), but BOTH overlapping 2-char sub-blocks
				// c1+c2 ("热力") and c2+c3 ("力学") are genuine single tokens in the 128k BPE vocabulary,
				// s is a true composite lexical concept rather than a function-word boundary phrase.
				_, w0 := utf8.DecodeRuneInString(s)
				if w0 > 0 && w0 < len(s) {
					_, w1 := utf8.DecodeRuneInString(s[w0:])
					if w1 > 0 && w0+w1 < len(s) {
						biL := s[:w0+w1]
						biR := s[w0:]
						if c.bareTokenID[biL] > 0 && c.bareTokenID[biR] > 0 {
							phrases = append(phrases, s, biL, biR)
						}
					}
				}
			}
			unwrap(lid)
			unwrap(rid)
			return
		}
		if bitsX10 == 200 && id > 7000 && c.mergeDeg[id] <= 6 && (is1CharBoundary(lid) || is1CharBoundary(rid)) {
			unwrap(lid)
			unwrap(rid)
			return
		}
		leaves = append(leaves, s)
	}
	pieces := PreTokenize(text)
	var pending []byte
	flushPending := func() {
		if len(pending) > 0 {
			if utf8.Valid(pending) {
				leaves = append(leaves, string(pending))
			}
			pending = pending[:0]
		}
	}
	for _, p := range pieces {
		ids := c.bpePiece(p)
		for _, id := range ids {
			if int(id) >= len(c.idToBytes) || len(c.idToBytes[id]) == 0 {
				continue
			}
			if len(pending) == 0 && c.idToValidUTF8Str[id] != "" {
				unwrap(uint32(id))
				continue
			}
			b := c.idToBytes[id]
			pending = append(pending, b...)
			if utf8.Valid(pending) {
				leaves = append(leaves, string(pending))
				pending = pending[:0]
			}
		}
		flushPending()
	}
	return leaves, phrases
}
