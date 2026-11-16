// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package codec

import (
	"encoding/binary"
	"hash/fnv"
	"math"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/rocareer/rolling-memory/internal/tokenzr"
	"github.com/rocareer/rolling-memory/internal/zhao/rm/shaping"
)

// TermSample represents one carrier sample in a sentence frame: H is the keyed
// identity and W is its width/weight.
type TermSample struct {
	H uint64
	W float64
}

// FoldedTerm is the folded projection of same-key samples within a frame or unit.
type FoldedTerm struct {
	Key     uint64
	Bits    float64
	Samples int
}

// PhraseDyad represents a co-occurring ordered bi-term phrase (Left, Right)
// extracted from adjacent distinct primary terms within a sentence frame.
type PhraseDyad struct {
	Left    uint64
	Right   uint64
	Hash    uint64
	Bits    float64
	Samples int
}

// CrestPhrase represents a contiguous caller-supplied frame span or adjacent pair.
type CrestPhrase struct {
	Hash    uint64
	Keys    []uint64
	Bits    float64
	Samples int
}

func carrierPresent(slot TermSample) bool {
	return slot.H != 0 && !math.IsNaN(slot.W) && !math.IsInf(slot.W, 0) && slot.W > 0
}

// InverseSplit retains valid carrier samples with positive finite width in order.
func InverseSplit(slots []TermSample) []TermSample {
	terms := make([]TermSample, 0, len(slots))
	for _, slot := range slots {
		if !carrierPresent(slot) {
			continue
		}
		terms = append(terms, slot)
	}
	return terms
}

// FoldSamples folds samples of the same keyed identity to their minimum width and count.
func FoldSamples(samples []TermSample) []FoldedTerm {
	folded := make(map[uint64]FoldedTerm, len(samples))
	for _, sample := range samples {
		prev, seen := folded[sample.H]
		if !seen {
			folded[sample.H] = FoldedTerm{
				Key:     sample.H,
				Bits:    sample.W,
				Samples: 1,
			}
			continue
		}
		prev.Samples++
		if sample.W < prev.Bits {
			prev.Bits = sample.W
		}
		folded[sample.H] = prev
	}
	terms := make([]FoldedTerm, 0, len(folded))
	for _, foldedTerm := range folded {
		terms = append(terms, foldedTerm)
	}
	sort.Slice(terms, func(a, b int) bool {
		left, right := terms[a], terms[b]
		if left.Bits != right.Bits {
			return left.Bits > right.Bits
		}
		return left.Key < right.Key
	})
	return terms
}

// ExtractTerms filters invalid samples and folds valid samples by key.
func ExtractTerms(slots []TermSample) []FoldedTerm {
	return FoldSamples(InverseSplit(slots))
}

// HashPhraseDyad computes the deterministic 64-bit FNV-1a fingerprint of (left, right).
func HashPhraseDyad(left, right uint64) uint64 {
	hash := fnv.New64a()
	var buf [16]byte
	binary.LittleEndian.PutUint64(buf[0:8], left)
	binary.LittleEndian.PutUint64(buf[8:16], right)
	_, _ = hash.Write(buf[:])
	return hash.Sum64()
}

// ExtractAdjacentPhrases extracts deduplicated adjacent bi-term phrases from
// sentence frames without any window-size magic number. Consecutive identical
// keys within a frame are collapsed before forming adjacent pairs.
func ExtractAdjacentPhrases(frames [][]TermSample) []PhraseDyad {
	type dyadKey struct {
		left, right uint64
	}
	folded := make(map[dyadKey]PhraseDyad)
	for _, frame := range frames {
		samples := InverseSplit(frame)
		if len(samples) < 2 {
			continue
		}
		var prev TermSample
		hasPrev := false
		for _, cur := range samples {
			if !hasPrev {
				prev = cur
				hasPrev = true
				continue
			}
			if cur.H == prev.H {
				if cur.W < prev.W {
					prev.W = cur.W
				}
				continue
			}
			k := dyadKey{left: prev.H, right: cur.H}
			pairBits := prev.W
			if cur.W < pairBits {
				pairBits = cur.W
			}
			if existing, ok := folded[k]; !ok {
				folded[k] = PhraseDyad{
					Left:    prev.H,
					Right:   cur.H,
					Hash:    HashPhraseDyad(prev.H, cur.H),
					Bits:    pairBits,
					Samples: 1,
				}
			} else {
				existing.Samples++
				if pairBits < existing.Bits {
					existing.Bits = pairBits
				}
				folded[k] = existing
			}
			prev = cur
		}
	}
	if len(folded) == 0 {
		return nil
	}
	out := make([]PhraseDyad, 0, len(folded))
	for _, d := range folded {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Bits != b.Bits {
			return a.Bits > b.Bits
		}
		if a.Hash != b.Hash {
			return a.Hash < b.Hash
		}
		if a.Left != b.Left {
			return a.Left < b.Left
		}
		return a.Right < b.Right
	})
	return out
}

// ExtractCrestPhrases retains every adjacent pair in each frame and the complete
// frame span (0-Gate all-candidate retention).
func ExtractCrestPhrases(frames [][]TermSample) []CrestPhrase {
	folded := make(map[uint64]CrestPhrase)
	recordSpan := func(run []TermSample) {
		if len(run) < 2 {
			return
		}
		h := fnv.New64a()
		var buf [8]byte
		keys := make([]uint64, len(run))
		var meanBits float64
		for i, s := range run {
			keys[i] = s.H
			meanBits += (s.W - meanBits) / float64(i+1)
			binary.LittleEndian.PutUint64(buf[:], s.H)
			_, _ = h.Write(buf[:])
		}
		fp := h.Sum64()
		if existing, ok := folded[fp]; !ok {
			folded[fp] = CrestPhrase{
				Hash:    fp,
				Keys:    keys,
				Bits:    meanBits,
				Samples: 1,
			}
		} else {
			existing.Samples++
			if meanBits > existing.Bits {
				existing.Bits = meanBits
			}
			folded[fp] = existing
		}
	}

	for _, frame := range frames {
		start := 0
		for i := 0; i <= len(frame); i++ {
			if i < len(frame) && frame[i].H != 0 && frame[i].W >= 0 && !math.IsNaN(frame[i].W) && !math.IsInf(frame[i].W, 0) {
				continue
			}
			run := frame[start:i]
			if len(run) >= 2 {
				recordSpan(run)
				if len(run) > 2 {
					for k := 0; k+1 < len(run); k++ {
						recordSpan(run[k : k+2])
					}
				}
			}
			start = i + 1
		}
	}
	if len(folded) == 0 {
		return nil
	}
	out := make([]CrestPhrase, 0, len(folded))
	for _, cp := range folded {
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Bits != out[j].Bits {
			return out[i].Bits > out[j].Bits
		}
		return out[i].Hash < out[j].Hash
	})
	return out
}

// FlagCompanionModifier marks a slot as a companion token decoration/modifier
// rather than a primary surface term slot.
const FlagCompanionModifier uint8 = FlagIsHarmonicCompanion

// ExtractPulsePrimaryDyads extracts adjacent bi-term phrases strictly over
// primary term slots (slots without FlagCompanionModifier), preventing
// companion modifiers of a single word from pairing with their own host word.
func ExtractPulsePrimaryDyads(frames [][]PulseSlot) []PhraseDyad {
	primaryFrames := make([][]TermSample, 0, len(frames))
	for _, frame := range frames {
		if len(frame) == 0 {
			continue
		}
		primaries := make([]TermSample, 0, len(frame))
		for _, slot := range frame {
			if slot.Flags&FlagCompanionModifier != 0 {
				continue
			}
			primaries = append(primaries, TermSample{
				H: slot.H,
				W: float64(slot.RawBitsX10) / 10,
			})
		}
		if len(primaries) >= 2 {
			primaryFrames = append(primaryFrames, primaries)
		}
	}
	return ExtractAdjacentPhrases(primaryFrames)
}

// CompanionSlots 是规范修饰通道每个词元承载的伴随修饰槽数，与
// internal/blindkit 的 appendWordWithCanonicalHarmonics 二槽契约（width-1 / width-3）同源。
// 它是结构契约而非评分参数：槽数决定伴随词项个数，不进入任何能量或排序算式。
const CompanionSlots = 2

const companionSlots = CompanionSlots

// firstDecorationClosedSetTag 零切片/零哈希表分配查询词元的首个有效一阶装饰闭集标签：
// 跳过 @dim:* 与 @geo:cont:* 二阶宽泛轴及单 ASCII 字母的 @unit:* 标签；若未命中字典且为结构化日期则返 cal:YYYY-MM-DD。
func firstDecorationClosedSetTag(low string) string {
	if tag := lookupFirstClosedSetDictTag(low); tag != "" {
		return tag
	}
	if idx := strings.IndexByte(low, '\''); idx > 0 {
		if tag := lookupFirstClosedSetDictTag(low[:idx]); tag != "" && !strings.HasPrefix(tag, "@unit:") && !strings.HasPrefix(tag, "@cal:") {
			return tag
		}
	}
	if len(low) >= 8 && strings.IndexFunc(low, unicode.IsNumber) >= 0 {
		if isos := ExtractStructuredDateHarmonics(low); len(isos) > 0 {
			return "cal:" + isos[0]
		}
	}
	return ""
}

// ExtractDecorationClosedSet 执行第 2 层「装饰闭集 / 分类闭集（DecorationClosedSet / CategoryClosedSet）」对齐：
// 覆盖 100% 确定性有限世界知识映射（ISO-3166 地名 @geo:cc:*、度量衡与货币单位 @unit:*、
// 亲属/角色 @kin:*/@role:*、场所 @venue:*、类别 @obj:* 及纯形式结构化日期校准饰标 cal:YYYY-MM-DD）。
//
// 排除 @dim:* 与 @geo:cont:* 宽泛二阶大类轴；100% 零香农先验、零 BPE 先验判官调用。
func ExtractDecorationClosedSet(low string) []string {
	if low == "" {
		return nil
	}
	isSingleASCIILetter := len(low) == 1 && low[0] >= 'a' && low[0] <= 'z'
	var out []string
	for _, tag := range LookupClosedSetHarmonics(low) {
		if strings.HasPrefix(tag, "@dim:") || strings.HasPrefix(tag, "@geo:cont:") {
			continue
		}
		if isSingleASCIILetter && strings.HasPrefix(tag, "@unit:") {
			continue
		}
		out = append(out, tag)
	}
	if len(out) == 0 {
		if idx := strings.IndexByte(low, '\''); idx > 0 {
			for _, tag := range LookupClosedSetHarmonics(low[:idx]) {
				if strings.HasPrefix(tag, "@dim:") || strings.HasPrefix(tag, "@geo:cont:") || strings.HasPrefix(tag, "@unit:") || strings.HasPrefix(tag, "@cal:") {
					continue
				}
				out = append(out, tag)
			}
		}
	}
	if len(low) >= 8 && strings.IndexFunc(low, unicode.IsNumber) >= 0 {
		for _, iso := range ExtractStructuredDateHarmonics(low) {
			out = append(out, "cal:"+iso)
			if len(iso) == 10 && iso[4] == '-' && iso[7] == '-' {
				out = append(out, "@cal:month:"+iso[5:7])
			}
		}
	}
	return out
}

// ExtractMorphShaping 执行纯确定性字符几何规整与 BPE 纯字典切分（100% 移除 BPE 先验与香农先验）：
// 核心哲学：「编码平波、零先验判官、100% 确定性字符几何 + BPE 纯字典切分」——
//  1. 内联撇号附着语素剥离（如 "fancett's" -> "fancett", "hasselhorn's" -> "hasselhorn"）；
//  2. 拉丁变音符纯字符几何折叠（如 "saké" -> "sake", "zürich" -> "zurich"）；
//  3. 连字符/下划线/点号复合结构确定性归并形与子块切分（如 "u.k." -> "uk", "slow-motion" -> "slowmotion", "slow", "motion"）；
//  4. 纯字符游程拉长音与周期叠词几何坍缩（如 "coooool" -> "cool", "hahahaha" -> "haha"）；
//  5. BPE 纯分词器切分（tokenzr.SplitBPEDirectPieces：允许切出碎词，高频碎词进入系统后自动解除伴随并以低动态能量驻留）。
func ExtractMorphShaping(low string) []string {
	if low == "" {
		return nil
	}
	var out []string
	add := func(s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" || s == low {
			return
		}
		for _, prev := range out {
			if prev == s {
				return
			}
		}
		out = append(out, s)
	}
	// 1. 内联撇号附着语素剥离（如 "fancett's" -> "fancett", "hasselhorn's" -> "hasselhorn"）。
	if idx := strings.IndexByte(low, '\''); idx > 0 {
		add(low[:idx])
	}
	// 1b. 拉丁变音符纯字符几何折叠（如 "saké" -> "sake", "café" -> "cafe"）。
	if folded := shaping.FoldLatinDiacritics(low); folded != "" {
		add(folded)
	}
	// 2. 连字符/点号复合结构（遵循 §3.00 编码平波原则：不预判子块能量大小，确定性输出纯字母归并形与各子块）。
	if strings.ContainsAny(low, "-_.") {
		allLettersOrConn := true
		for _, r := range low {
			if r == '-' || r == '_' || r == '.' {
				continue
			}
			if !((r >= 'a' && r <= 'z') || r > 0x7f) {
				allLettersOrConn = false
				break
			}
		}
		if allLettersOrConn {
			var jb strings.Builder
			jb.Grow(len(low))
			for i := 0; i < len(low); i++ {
				if b := low[i]; b != '-' && b != '_' && b != '.' {
					jb.WriteByte(b)
				}
			}
			add(jb.String())
		}
		for _, part := range strings.FieldsFunc(low, func(r rune) bool { return r == '-' || r == '_' || r == '.' }) {
			if part != "" && part != low && !(len(part) == 1 && part[0] >= 'a' && part[0] <= 'z') {
				add(part)
			}
		}
		return out
	}
	// 3. 纯字符几何游程拉长音、周期叠音坍缩与前导字母-尾部数字词干切分（零 BPE 先验能量）。
	r2, r1 := shaping.CollapseElongatedRuns(low)
	add(r2)
	add(r1)
	add(shaping.CollapsePeriodicReduplication(low))
	add(shaping.SplitAlphaLeadingNumericTail(low))
	// 4. BPE 只做纯分词器（tokenzr.SplitBPEDirectPieces：只管切分，零先验能量、零词性判官；
	//    高频游离碎片进入系统后不做伴随，由高 df 语料动态能量自动降级）。
	for _, piece := range tokenzr.SplitBPEDirectPieces(low) {
		add(piece)
	}
	return out
}

const (
	companionCacheShards   = 64
	maxCompanionPerShard   = 2048
	maxCompanionCacheBytes = 96
)

type companionTagShard struct {
	mu sync.RWMutex
	m  map[string][]string
}

var globalCompanionShards [companionCacheShards]companionTagShard

func companionShardIdx(s string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h & (companionCacheShards - 1)
}

// CanonicalCompanionTags 是全域唯一统一的词元装饰与子词切分入口：
// 联合「装饰闭集（DecorationClosedSet）」与「纯确定性字符几何 + BPE 纯切分（MorphShaping）」，
// 返回词元的平波展开标签（最多 CompanionSlots 条）：
//
//   - 槽 1 优先分配给装饰闭集的一阶特异轴（至多占 1 槽，绝不挤死几何整形）；
//   - 剩余槽位由确定性字符几何与 BPE 纯切分（MorphShaping）填充。
func CanonicalCompanionTags(token string) []string {
	low := strings.ToLower(strings.TrimSpace(CanonicalizeApostrophe(token)))
	if low == "" {
		return nil
	}
	var sh *companionTagShard
	if len(low) <= maxCompanionCacheBytes {
		sh = &globalCompanionShards[companionShardIdx(low)]
		sh.mu.RLock()
		if sh.m != nil {
			if cached, ok := sh.m[low]; ok {
				sh.mu.RUnlock()
				return cached
			}
		}
		sh.mu.RUnlock()
	}
	var tags []string
	push := func(tag string) bool {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag == "" || tag == low || len(tags) >= CompanionSlots {
			return false
		}
		for _, prev := range tags {
			if prev == tag {
				return false
			}
		}
		if tags == nil {
			tags = make([]string, 0, CompanionSlots)
		}
		tags = append(tags, tag)
		return true
	}

	// 装饰闭集 / 分类闭集（DecorationClosedSet），零中间切片分配，至多占 1 槽。
	if ct := firstDecorationClosedSetTag(low); ct != "" {
		push(ct)
	}

	// 纯确定性字符几何与 BPE 纯分词切分（MorphShaping）。
	for _, mt := range ExtractMorphShaping(low) {
		if push(mt) && len(tags) >= CompanionSlots {
			break
		}
	}
	if sh != nil {
		sh.mu.Lock()
		if sh.m == nil {
			sh.m = make(map[string][]string, 128)
		}
		if len(sh.m) < maxCompanionPerShard {
			sh.m[low] = tags
		}
		sh.mu.Unlock()
	}
	return tags
}

// DocumentFeatures holds the standard de-physicized Step-1 extraction output
// in the Zhao domain: deduplicated Terms (with TokenDecoration family conservation),
// sentence-local adjacent Phrases (bi-term dyads), and zero-gate CrestPhrases
// delegated to the engine term frame contract (src/zhao/engine/term/phrase.go).
type DocumentFeatures struct {
	Terms        map[uint64]Term
	Counts       map[uint64]int
	Phrases      []PhraseDyad
	CrestPhrases []CrestPhrase
	Positions    map[uint64][]TermPosition
	SpaceFusion  SpaceFaultFusion
}

// sentenceEnergySegments splits a sentence into natural sequential lexical units
// for flat-wave span encoding using 100% deterministic Unicode geometry (zero BPE prior):
//   - Space-delimited tokens are kept in sequential order;
//   - Unspaced CJK runs are segmented into deterministic non-overlapping 2-character blocks
//     (with any trailing single character preserved), so "他说山东苹果不好吃" yields
//     ["他说", "山东", "苹果", "不好", "吃"] with zero BPE vocabulary dependency.
func sentenceEnergySegments(sent string) []string {
	var out []string
	var cur []rune
	lastCJK := false
	flush := func() {
		if len(cur) == 0 {
			return
		}
		run := cur
		cur = nil
		if !lastCJK {
			seg := strings.ToLower(strings.TrimSpace(string(run)))
			if seg != "" {
				out = append(out, seg)
			}
			return
		}
		for i := 0; i+1 < len(run); i += 2 {
			out = append(out, string(run[i:i+2]))
		}
		if len(run)%2 == 1 {
			out = append(out, string(run[len(run)-1:]))
		}
	}
	for _, r := range sent {
		isCJK := IsContinuousSyllabicOrIdeographic(r)
		isAlnum := isLetterOrNumber(r) || r == '-' || r == '_' || r == '\''
		if !isCJK && !isAlnum {
			flush()
			continue
		}
		if len(cur) > 0 && isCJK != lastCJK {
			flush()
		}
		lastCJK = isCJK
		cur = append(cur, r)
	}
	flush()
	return out
}

// ExtractFeatures executes the unified Zhao Step-1 extraction over raw text.
func (a *TermExtractor) ExtractFeatures(text string) DocumentFeatures {
	canonical := CanonicalizeApostrophe(text)
	terms, counts, positions, primaryFrames := a.atomizeSentences(canonical, true)
	sents := splitSentences(canonical)
	crestFrames := make([][]TermSample, 0, len(sents))

	for _, sent := range sents {
		segs := sentenceEnergySegments(sent)
		if len(segs) >= 2 {
			cFrame := make([]TermSample, 0, len(segs))
			for _, seg := range segs {
				cFrame = append(cFrame, TermSample{
					H: a.enc.KeyedHash64(seg),
					W: 1.0,
				})
			}
			crestFrames = append(crestFrames, cFrame)
		}
	}

	phrases := ExtractAdjacentPhrases(primaryFrames)
	crests := ExtractCrestPhrases(crestFrames)

	return DocumentFeatures{
		Terms:        terms,
		Counts:       counts,
		Phrases:      phrases,
		CrestPhrases: crests,
		Positions:    positions,
		SpaceFusion:  a.ExtractSpaceFaultFusion(canonical),
	}
}

func SentenceEnergySegments(sent string) []string { return sentenceEnergySegments(sent) }
