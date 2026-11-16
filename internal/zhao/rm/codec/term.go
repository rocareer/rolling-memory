// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package rm 实现 zhao 四步双簇检索流水线（term / index / expand / rank）的唯一自包含检索内核真源：
// 单文件 SQLite 与 .zcsr 磁盘 mmap 持久化 + 键控词项与词元装饰（TokenDecoration）+
// 倒排精确匹配 + EntityCluster × SpacetimeCluster 双簇扩展 + 绝对能量与条件边际筛选。
package codec

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/rocareer/rolling-memory/internal/zhao/rm/closedset"
	"github.com/rocareer/rolling-memory/internal/zhao/rm/shaping"
)

// DemoSecret 是单测与基准评测共用的标准键控哈希密钥：文档侧与查询侧共用同一字节串，非评分参数。
// 键控哈希只决定词项身份承诺，不影响任何能量或排序计算。
var DemoSecret = []byte("zhao-rm-mvp-2026-11-13")

// TermExtractor 把明文切分为键控词项（Term）与词元装饰（TokenDecoration）；
// 文档侧与查询侧必须共用同一密钥与同一张词元装饰表。
type TermExtractor struct {
	enc *BlindEncoder
}

// NewTermExtractor 构造去伪物理化标准词项抽取器，装载词根/词形装饰守恒（TokenDecoration）的伴随词项全集：
// 主词项与伴随词项（纯确定性字符几何规整 / 闭集槽位装饰 / 日期校准饰标）均以平波身份入库，
// 标签集合见 CanonicalCompanionTags。
// 同一次倒排构建与查询必须共用同一张表，故文档侧与查询侧都经本构造函数取抽取器。
func NewTermExtractor(secret []byte) *TermExtractor {
	az := &TermExtractor{enc: NewBlindEncoder(secret)}
	az.enc.SetCanonicalHarmonics(CanonicalHarmonicOptions{
		EnableStructuredDates: true,
		Expander:              CanonicalCompanionTags,
	})
	return az
}

// NewBaselineTermExtractor 构造只产出主词项的抽取器（无伴随词项通道），
// 用于复算伴随词项接入之前登记的读数，不参与生产取数路径。
func NewBaselineTermExtractor(secret []byte) *TermExtractor {
	return &TermExtractor{enc: NewBlindEncoder(secret)}
}

// ExtractTerms 返回去重词项表（哈希 -> 词项 Term）与逐词项出现次数（零先验能量，平波入库）。
// 同一身份的重复出现折叠为最大载体宽度 Bits 与并集结构位 Flags（{b, f} 声明式元数据）。
func (a *TermExtractor) ExtractTerms(text string) (map[uint64]Term, map[uint64]int) {
	terms, counts, _ := a.ExtractTermsWithPositions(text)
	return terms, counts
}

// ExtractTermsWithPositions 返回去重词项表、出现次数及每个词项的 (句, 词槽) 位置表。
// 主词项递增词槽 Slot，伴随修饰词项与所属主词项共享同一 (Sentence, Slot) 坐标，
// 从而保持相邻主词距离恒为 1 且同槽词族可精确识别。
func (a *TermExtractor) ExtractTermsWithPositions(text string) (map[uint64]Term, map[uint64]int, map[uint64][]TermPosition) {
	terms, counts, positions, _ := a.atomizeSentences(CanonicalizeApostrophe(text), false)
	return terms, counts, positions
}

func (a *TermExtractor) atomizeSentences(canonical string, collectFrames bool) (map[uint64]Term, map[uint64]int, map[uint64][]TermPosition, [][]TermSample) {
	return a.atomizeSentenceList(splitSentences(canonical), collectFrames)
}

func (a *TermExtractor) atomizeSentenceList(sents []string, collectFrames bool) (map[uint64]Term, map[uint64]int, map[uint64][]TermPosition, [][]TermSample) {
	atoms := make(map[uint64]Term, 64)
	counts := make(map[uint64]int, 64)
	positions := make(map[uint64][]TermPosition, 64)
	posBacking := make([]TermPosition, 0, len(sents)*24+16)
	var primaryFrames [][]TermSample
	if collectFrames {
		primaryFrames = make([][]TermSample, 0, len(sents))
	}
	var docSlot int
	for sentenceAt, sent := range sents {
		words := SplitWords(sent)
		rawSlots := a.enc.packSentenceWords(sent, words)
		var framePrimaries []TermSample
		if collectFrames {
			framePrimaries = make([]TermSample, 0, len(words))
		}
		wordIndex := 0
		dateSlotOpened := false
		var currentGroup uint64
		var currentHostBits uint16
		var pos TermPosition
		for _, raw := range rawSlots {
			if raw.H == 0 {
				continue
			}
			isPrimary := raw.ClassFlags&FlagIsHarmonicCompanion == 0
			grp := currentGroup
			slotRole := raw.Role
			if isPrimary {
				slotRole = RolePrimary
				currentHostBits = raw.RawBitsX10
				if wordIndex >= len(words) {
					if !dateSlotOpened {
						docSlot++
						dateSlotOpened = true
					}
					currentGroup = raw.H
					grp = currentGroup
					pos = TermPosition{Sentence: sentenceAt, Slot: docSlot}
				} else {
					docSlot++
					pos = TermPosition{Sentence: sentenceAt, Slot: docSlot}
					wordIndex++
					currentGroup = raw.H
					grp = currentGroup
				}
				if collectFrames {
					framePrimaries = append(framePrimaries, TermSample{
						H: raw.H,
						W: 1.0,
					})
				}
			} else if slotRole == 0 {
				if int(currentHostBits) >= int(raw.RawBitsX10)+3 {
					slotRole = RoleSubwordPiece
				} else {
					slotRole = RoleMorphStem
				}
			}
			var extBit uint16
			switch {
			case isPrimary:
				extBit = ExtFlagSeenPrimary
			case slotRole == RoleSubwordPiece:
				extBit = ExtFlagSeenSubword
			default:
				extBit = ExtFlagSeenMorphOrClos
			}
			atom, seen := atoms[raw.H]
			if !seen {
				atom = Term{
					Hash:     raw.H,
					Role:     slotRole,
					ExtFlags: extBit,
					Group:    grp,
					Sent:     clampPosU16(sentenceAt + 1),
					Slot:     clampPosU16(pos.Slot),
				}
			} else {
				atom.ExtFlags |= extBit
				if isPrimary {
					atom.Group = raw.H
					atom.Role = RolePrimary
				} else if atom.Role != RolePrimary && slotRole < atom.Role {
					atom.Role = slotRole
				}
			}
			if raw.RawBitsX10 > atom.Bits {
				atom.Bits = raw.RawBitsX10
			}
			atom.Flags |= raw.ClassFlags
			atoms[raw.H] = atom
			if prev := positions[raw.H]; len(prev) == 0 {
				counts[raw.H] = 1
				idx := len(posBacking)
				posBacking = append(posBacking, pos)
				positions[raw.H] = posBacking[idx : idx+1 : idx+1]
			} else if prev[len(prev)-1] != pos {
				counts[raw.H]++
				positions[raw.H] = append(prev, pos)
			}
		}
		if collectFrames && len(framePrimaries) >= 2 {
			primaryFrames = append(primaryFrames, framePrimaries)
		}
	}
	return atoms, counts, positions, primaryFrames
}

// ExtractDoc 将明文切分为携带词项（Terms）、频次与句槽位置表的完整待入库 Doc。
func (a *TermExtractor) ExtractDoc(unitID string, occurredAt int64, text string) Doc {
	sents := splitSentences(CanonicalizeApostrophe(shaping.CollapseDegenerateTokenRuns(text)))
	terms, counts, positions, _ := a.atomizeSentenceList(sents, false)
	a.mergeDocNaturalDateBeaconsSents(occurredAt, sents, terms, counts, positions)
	return Doc{
		UnitID:     unitID,
		OccurredAt: occurredAt,
		Terms:      terms,
		Counts:     counts,
		Positions:  positions,
	}
}

// ExtractTermsDoc 是 ExtractDoc 的向后兼容方法别名。
func (a *TermExtractor) ExtractTermsDoc(unitID string, occurredAt int64, text string) Doc {
	return a.ExtractDoc(unitID, occurredAt, text)
}

// ExtractQueryWithPositions 提取查询词项表、出现次数与句槽位置，并并入空格容错双轨粘合候选与句尾自然语言日期信标。
func (a *TermExtractor) ExtractQueryWithPositions(idx *Index, text string) (map[uint64]Term, map[uint64]int, map[uint64][]TermPosition) {
	canonical := CanonicalizeApostrophe(text)
	sents := splitSentences(canonical)
	qTerms, counts, qPositions, _ := a.atomizeSentenceList(sents, false)
	a.mergeQuerySpaceFaultFusionSents(idx, canonical, sents, qTerms)
	a.mergeQueryNaturalDateBeaconsSents(canonical, sents, qTerms, qPositions)
	a.mergeQueryOrdinalSpanBeacons(idx, sents, qTerms, qPositions)
	return qTerms, counts, qPositions
}

// 查询侧在主体词项之外并入空格容错双轨外挂的去空格粘合候选
// （space_tolerance_plugin.go）：用户误插空格的输入（如 "山 东今天"、"a n  app le"）
// 以修复后的真词探测倒排字典；伪粘合项因语料库无匹配（df = 0）一次哈希查找即跳过。

func stripDatePunct(s string) string {
	return strings.Trim(s, ",.;:!?()[]{}\"'")
}

func stripDateOrdNum(tok string) string {
	t := stripDatePunct(tok)
	dLen := 0
	for dLen < len(t) && t[dLen] >= '0' && t[dLen] <= '9' {
		dLen++
	}
	if dLen >= 1 && dLen <= 2 && len(t) > dLen && len(t)-dLen <= 2 {
		return t[:dLen]
	}
	return t
}

func tryEmitNaturalDateSpan(m1, d1, m2, d2, yStr string, sentIdx, slotIdx int, emit func(iso string, sentIdx, slotIdx int)) bool {
	c1 := m1 + " " + d1 + " " + yStr
	c2 := m2 + " " + d2 + " " + yStr
	for _, l1 := range []string{"January 2 2006", "Jan 2 2006"} {
		p1, e1 := time.Parse(l1, c1)
		if e1 != nil {
			continue
		}
		for _, l2 := range []string{"January 2 2006", "Jan 2 2006"} {
			p2, e2 := time.Parse(l2, c2)
			if e2 == nil && p1.Year() >= 1900 && p1.Year() <= 2199 && !p2.Before(p1) && p2.Sub(p1) <= 14*24*time.Hour {
				for cur := p1; !cur.After(p2); cur = cur.AddDate(0, 0, 1) {
					emit(cur.Format("2006-01-02"), sentIdx, slotIdx)
				}
				return true
			}
		}
	}
	return false
}

func isAsciiDigitDay(s string) bool {
	return len(s) >= 1 && len(s) <= 2 && s[0] >= '0' && s[0] <= '9' && (len(s) == 1 || (s[1] >= '0' && s[1] <= '9'))
}

func scanSentenceNaturalDates(ws []string, startWi, sentNum int, isQuery, leadWordRepeated bool, refYear string, emit func(iso string, sentIdx, slotIdx int)) {
	for wi := startWi; wi+1 < len(ws); wi++ {
		if isQuery && wi+2 >= len(ws) {
			break
		}
		if isQuery && !leadWordRepeated && strings.HasSuffix(ws[wi+2], ",") && wi+6 < len(ws) {
			continue
		}
		slotIdx := wi + 1
		w0 := strings.TrimSuffix(stripDatePunct(ws[wi]), ".")
		t1Raw := stripDatePunct(ws[wi+1])
		if dashIdx := strings.IndexByte(t1Raw, '-'); dashIdx > 0 && dashIdx+1 < len(t1Raw) && wi+3 < len(ws) {
			d1 := stripDateOrdNum(t1Raw[:dashIdx])
			if isAsciiDigitDay(d1) {
				m2 := strings.TrimSuffix(t1Raw[dashIdx+1:], ".")
				d2 := stripDateOrdNum(ws[wi+2])
				yStr := stripDatePunct(ws[wi+3])
				if tryEmitNaturalDateSpan(w0, d1, m2, d2, yStr, sentNum, slotIdx, emit) {
					continue
				}
			}
		}
		w1 := stripDateOrdNum(ws[wi+1])
		w0Day := isAsciiDigitDay(w0)
		w1Day := isAsciiDigitDay(w1)
		if !w0Day && !w1Day {
			continue
		}
		if w1Day && wi+4 < len(ws) {
			w2 := stripDatePunct(ws[wi+2])
			w3 := stripDateOrdNum(ws[wi+3])
			w4 := stripDatePunct(ws[wi+4])
			if w2 != "" && isAsciiDigitDay(w3) && tryEmitNaturalDateSpan(w0, w1, w0, w3, w4, sentNum, slotIdx, emit) {
				continue
			}
		}
		if w1Day && wi+5 < len(ws) {
			w3 := strings.TrimSuffix(stripDatePunct(ws[wi+3]), ".")
			w4 := stripDateOrdNum(ws[wi+4])
			w5 := stripDatePunct(ws[wi+5])
			if isAsciiDigitDay(w4) && (!isQuery || strings.EqualFold(w0, w3)) && tryEmitNaturalDateSpan(w0, w1, w3, w4, w5, sentNum, slotIdx, emit) {
				continue
			}
		}
		if wi+2 < len(ws) {
			w2 := stripDatePunct(ws[wi+2])
			if len(w2) == 4 && w2[0] >= '1' && w2[0] <= '2' {
				cand3 := w0 + " " + w1 + " " + w2
				matched3 := false
				for _, layout := range []string{"January 2 2006", "Jan 2 2006", "2 January 2006", "2 Jan 2006"} {
					if parsed, err := time.Parse(layout, cand3); err == nil && parsed.Year() >= 1900 && parsed.Year() <= 2199 {
						emit(parsed.Format("2006-01-02"), sentNum, slotIdx)
						matched3 = true
						break
					}
				}
				if matched3 {
					continue
				}
			}
		}
		if w1Day && refYear != "" && w1 != t1Raw {
			cand2 := w0 + " " + w1 + " " + refYear
			for _, layout := range []string{"January 2 2006", "Jan 2 2006"} {
				if parsed, err := time.Parse(layout, cand2); err == nil && parsed.Year() >= 1900 && parsed.Year() <= 2199 {
					emit(parsed.Format("2006-01-02"), sentNum, slotIdx)
					break
				}
			}
		}
	}
}

func (a *TermExtractor) upsertNaturalDateBeaconTerms(iso string, sentIdx, slotIdx int, isQuery bool, terms map[uint64]Term, counts map[uint64]int, positions map[uint64][]TermPosition) {
	sentU16 := clampPosU16(sentIdx)
	slotU16 := clampPosU16(slotIdx)
	pos := TermPosition{Sentence: sentIdx - 1, Slot: slotIdx}
	var extFlags uint16
	calFlags := FlagHasDigit | FlagHasConn
	if isQuery {
		calFlags |= FlagHasUpper
	} else {
		extFlags = ExtFlagSeenMorphOrClos
	}
	for _, spec := range [2]struct {
		key   string
		bits  uint16
		flags uint8
	}{
		{key: "cal:" + iso, bits: calBeaconCarrierBitsX10, flags: calFlags},
		{key: iso, bits: isoDateCarrierBitsX10, flags: FlagHasDigit | FlagHasConn},
	} {
		h := a.enc.KeyedHash64(spec.key)
		if _, exists := terms[h]; !exists {
			terms[h] = Term{
				Hash:     h,
				Role:     RoleClosedBeacon,
				ExtFlags: extFlags,
				Group:    h,
				Bits:     spec.bits,
				Flags:    spec.flags,
				Sent:     sentU16,
				Slot:     slotU16,
			}
			if counts != nil {
				counts[h] = 1
			}
			if positions != nil {
				positions[h] = []TermPosition{pos}
			}
		} else if positions != nil {
			prev := positions[h]
			if len(prev) == 0 || prev[len(prev)-1] != pos {
				positions[h] = append(prev, pos)
				if counts != nil {
					counts[h]++
				}
			}
		}
	}
}

func (a *TermExtractor) resolveNaturalDateMonthSlot(monthWord string, sentIdx, fallbackSlot int, positions map[uint64][]TermPosition) int {
	if a == nil || a.enc == nil || len(positions) == 0 {
		return fallbackSlot
	}
	mh := a.enc.KeyedHash64(strings.ToLower(strings.TrimSpace(monthWord)))
	posList := positions[mh]
	targetSent := sentIdx - 1
	for _, p := range posList {
		if p.Sentence == targetSent && p.Slot >= fallbackSlot {
			return p.Slot
		}
	}
	return fallbackSlot
}

func (a *TermExtractor) mergeQueryNaturalDateBeaconsSents(canonical string, sents []string, qTerms map[uint64]Term, qPositions map[uint64][]TermPosition) {
	if a == nil || a.enc == nil || !a.enc.harmOpts.EnableStructuredDates || len(qTerms) == 0 || len(sents) == 0 {
		return
	}
	var allQueryWords []string
	for _, s := range sents {
		for _, rt := range strings.Split(strings.TrimSpace(s), " ") {
			if w := strings.ToLower(stripDatePunct(rt)); w != "" {
				allQueryWords = append(allQueryWords, w)
			}
		}
	}
	leadWordRepeated := false
	if len(allQueryWords) > 6 && len(allQueryWords[0]) >= 2 {
		leadW := allQueryWords[0]
		for _, w := range allQueryWords[6:] {
			if w == leadW {
				leadWordRepeated = true
				break
			}
		}
	}
	for sIdx, s := range sents {
		sTrim := strings.TrimSpace(s)
		if len(sents) > 1 && sIdx == 0 && !leadWordRepeated {
			firstPunct := strings.IndexAny(canonical, ".。!！?？\n")
			if firstPunct >= 0 && canonical[firstPunct] != '?' {
				continue
			}
		}
		var ws []string
		for _, rt := range strings.Split(sTrim, " ") {
			if rt != "" {
				ws = append(ws, rt)
			}
		}
		startWi := 4
		if sIdx == 0 && leadWordRepeated {
			startWi = 2
		} else if len(sents) == 1 && len(ws) <= 7 {
			startWi = 0
		}
		emitISO := func(iso string, sentIdx, slotIdx int) {
			resolvedSlot := slotIdx
			if slotIdx >= 1 && slotIdx-1 < len(ws) {
				w0 := strings.TrimSuffix(stripDatePunct(ws[slotIdx-1]), ".")
				resolvedSlot = a.resolveNaturalDateMonthSlot(w0, sentIdx, slotIdx, qPositions)
			}
			a.upsertNaturalDateBeaconTerms(iso, sentIdx, resolvedSlot, true, qTerms, nil, qPositions)
		}
		scanSentenceNaturalDates(ws, startWi, sIdx+1, true, leadWordRepeated, "", emitISO)
	}
}

func (a *TermExtractor) mergeDocNaturalDateBeaconsSents(occurredAt int64, sents []string, atoms map[uint64]Term, counts map[uint64]int, positions map[uint64][]TermPosition) {
	if a == nil || a.enc == nil || !a.enc.harmOpts.EnableStructuredDates || len(atoms) == 0 || len(sents) == 0 {
		return
	}
	refYear := ""
	if occurredAt > 0 {
		refYear = time.Unix(occurredAt, 0).UTC().Format("2006")
	}
	for sIdx, s := range sents {
		var ws []string
		for _, rt := range strings.Split(strings.TrimSpace(s), " ") {
			if rt != "" {
				ws = append(ws, rt)
			}
		}
		emitDocISO := func(iso string, sentIdx, slotIdx int) {
			resolvedSlot := slotIdx
			if slotIdx >= 1 && slotIdx-1 < len(ws) {
				w0 := strings.TrimSuffix(stripDatePunct(ws[slotIdx-1]), ".")
				resolvedSlot = a.resolveNaturalDateMonthSlot(w0, sentIdx, slotIdx, positions)
			}
			a.upsertNaturalDateBeaconTerms(iso, sentIdx, resolvedSlot, false, atoms, counts, positions)
		}
		scanSentenceNaturalDates(ws, 0, sIdx+1, false, false, refYear, emitDocISO)
	}
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

func splitSentences(text string) []string {
	sents := SplitSentences(text)
	if len(sents) > 0 {
		return sents
	}
	if trimmed := strings.TrimSpace(text); trimmed != "" {
		return []string{trimmed}
	}
	return nil
}

// Encoder returns the underlying BlindEncoder.
func (a *TermExtractor) Encoder() *BlindEncoder {
	if a == nil {
		return nil
	}
	return a.enc
}

func (a *TermExtractor) KeyedCompanionCount(text string) int {
	n := 0
	for _, sent := range splitSentences(text) {
		for _, atom := range a.enc.PackSentenceTerms(sent) {
			if atom.ClassFlags&FlagIsHarmonicCompanion != 0 {
				n++
			}
		}
	}
	return n
}

func ClampPosU16(v int) uint16 { return clampPosU16(v) }

// mergeQueryOrdinalSpanBeacons resolves ordinal span-polarity questions
// ("first/earliest ... in <Anchor>" / "last/latest ... in <Anchor>") into a
// concrete calendar-day beacon, reusing the natural-date beacon carrier.
//
// The polarity words are ISO-8601-adjacent closed-set calendar classes
// (closedset.OrdinalPolarityClosedSetTable), the same tier as month or
// day-of-week names; no open-vocabulary semantics are involved. The target day
// is derived entirely from the corpus posting histogram of the strongest
// proper anchor term:
//
//  1. abort when the query already carries an explicit cal: date beacon
//     (deictic "Today is X" / literal dates own the day channel), when any
//     polarity word is directly followed by a closed-set calendar period
//     (@cal:month/@cal:season/@unit:time: e.g. "last summer", "last month" —
//     relative-window arithmetic, not a span boundary), or when begin and end
//     polarities co-occur;
//  2. anchor = the RolePrimary proper-cased query term with maximal corpus
//     mass, df ∈ [2, N/e] (hapax noise and corpus-wide shells excluded),
//     never itself a closed-set token;
//  3. its per-day distinct-unit histogram is floored at max/e² and clustered
//     into runs tolerating a single-day gap; only an unambiguous single-run
//     histogram may emit (multi-trip anchors abstain);
//  4. begin polarity emits the run's first day, end polarity its last day.
func (a *TermExtractor) mergeQueryOrdinalSpanBeacons(idx *Index, sents []string, qTerms map[uint64]Term, qPositions map[uint64][]TermPosition) {
	if a == nil || a.enc == nil || !a.enc.harmOpts.EnableStructuredDates || idx == nil || idx.N == 0 || len(qTerms) == 0 {
		return
	}
	for _, tm := range qTerms {
		if tm.Role == RoleClosedBeacon && tm.Bits == calBeaconCarrierBitsX10 {
			return
		}
	}
	polarity := int8(0)
	polSent, polSlot := 0, 0
	for sIdx, s := range sents {
		var ws []string
		for _, rt := range strings.Split(strings.TrimSpace(s), " ") {
			if rt != "" {
				ws = append(ws, rt)
			}
		}
		for wi, rt := range ws {
			w := strings.ToLower(stripDatePunct(rt))
			pol := closedset.LookupOrdinalPolarity(w)
			if pol == 0 {
				continue
			}
			if wi+1 < len(ws) {
				nextTag := closedset.LookupFirstTag(strings.ToLower(stripDatePunct(ws[wi+1])))
				// A calendar period at any scale other than the histogram's own
				// one-day quantum ("last month", "last summer", "last Monday")
				// denotes a now-relative window, not a span boundary. "first/last
				// day" names the quantum itself and therefore selects a boundary
				// element of the anchor run.
				if (strings.HasPrefix(nextTag, "@cal:") || strings.HasPrefix(nextTag, "@unit:time")) && nextTag != "@unit:time:day" {
					return
				}
			}
			if polarity != 0 && pol != polarity {
				return
			}
			if polarity == 0 {
				polSent, polSlot = sIdx+1, wi
			}
			polarity = pol
		}
	}
	if polarity == 0 {
		return
	}
	var anchorH uint64
	var anchorMass float64
	dfCeil := float64(idx.N) / math.E
	for h, tm := range qTerms {
		if tm.Role != RolePrimary || tm.Flags&FlagHasUpper == 0 {
			continue
		}
		df := idx.TermDFOf(h)
		if df < 2 || float64(df) > dfCeil {
			continue
		}
		g := tm.Group
		if g == 0 {
			g = h
		}
		m, ok := idx.TermMassOf(g)
		if !ok || m <= anchorMass {
			continue
		}
		anchorMass = m
		anchorH = h
	}
	if anchorH == 0 {
		return
	}
	dayOf := func(u int32) int64 {
		if int(u) >= len(idx.UnitMeta) {
			return -1 << 62
		}
		ts := idx.UnitMeta[u].OccurredAt
		if ts == 0 {
			ts = idx.UnitMeta[u].NarratedAt
		}
		if ts == 0 {
			return -1 << 62
		}
		return ts / 86400
	}
	dayUnits := make(map[int64]int, 32)
	for _, p := range idx.Postings[anchorH] {
		if d := dayOf(p.Unit); d > (-1 << 61) {
			dayUnits[d]++
		}
	}
	if len(dayUnits) == 0 {
		return
	}
	maxUnits := 0
	for _, c := range dayUnits {
		if c > maxUnits {
			maxUnits = c
		}
	}
	floor := float64(maxUnits) / (math.E * math.E)
	days := make([]int64, 0, len(dayUnits))
	for d, c := range dayUnits {
		if float64(c) >= floor {
			days = append(days, d)
		}
	}
	if len(days) == 0 {
		return
	}
	sort.Slice(days, func(i, j int) bool { return days[i] < days[j] })
	runLo, runHi := days[0], days[0]
	for _, d := range days[1:] {
		if d > runHi+2 {
			return // multi-run anchor histogram: span boundary is ambiguous
		}
		runHi = d
	}
	dStar := runLo
	if polarity > 0 {
		dStar = runHi
	}
	iso := time.Unix(dStar*86400, 0).UTC().Format("2006-01-02")
	// Continuous-energy dose only: emit the bare ISO day term (284-bit carrier,
	// document-side flags). The 588-bit "cal:" master beacon with query flags is
	// deliberately withheld — an inferred day must never enter the proper-anchor
	// day-gating machinery (isAnchorGroup/anchorDays), where a wrong or
	// mention-skewed day hard-gates the true gold day by 1/e.
	h := a.enc.KeyedHash64(iso)
	if _, exists := qTerms[h]; !exists {
		qTerms[h] = Term{
			Hash:     h,
			Role:     RoleClosedBeacon,
			ExtFlags: ExtFlagSeenMorphOrClos,
			Group:    h,
			Bits:     isoDateCarrierBitsX10,
			Flags:    FlagHasDigit | FlagHasConn,
			Sent:     clampPosU16(polSent),
			Slot:     clampPosU16(polSlot),
		}
		if qPositions != nil {
			qPositions[h] = []TermPosition{{Sentence: polSent - 1, Slot: polSlot}}
		}
	}
}
