// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package langpack

import (
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/rocareer/rolling-memory/internal/text"
	"github.com/rocareer/rolling-memory/internal/thinclient"
	"github.com/rocareer/rolling-memory/internal/tokenzr"
	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

// MaxCapabilityLanguage is the sentinel that selects the strongest client-side
// assembly. It is the default for both the product vault (internal/api) and the
// evaluator's product surface, so the daily plugin path and the benchmark path
// share one configuration.
const MaxCapabilityLanguage = "max"

type maxCapLowLexEntry struct {
	isCJK    bool
	bpePrior float64
	effBits  float64
	isHigh   bool
	st       string
	mr       string
	slot1    string
}

var (
	maxCapLowLexCache     sync.Map
	maxCapPropPriorCache  sync.Map
	maxCapOnlineDFMu      sync.RWMutex
	maxCapOnlineDF        map[string]int
	maxCapOnlineTotalDocs int
)

// SetMaxCapOnlineCorpusDF injects or clears the lightweight online document-frequency table (df, totalDocs)
// used by PackMaxCapabilitySentence to wash out static BPE prior inversions without heavy manifold operators.
func SetMaxCapOnlineCorpusDF(df map[string]int, totalDocs int) {
	maxCapOnlineDFMu.Lock()
	defer maxCapOnlineDFMu.Unlock()
	if len(df) == 0 || totalDocs <= 0 {
		maxCapOnlineDF = nil
		maxCapOnlineTotalDocs = 0
		return
	}
	cp := make(map[string]int, len(df))
	for k, v := range df {
		cp[strings.ToLower(strings.TrimSpace(k))] = v
	}
	maxCapOnlineDF = cp
	maxCapOnlineTotalDocs = totalDocs
}

func queryMaxCapOnlineDF(low string) (df, totalDocs int) {
	maxCapOnlineDFMu.RLock()
	defer maxCapOnlineDFMu.RUnlock()
	if maxCapOnlineTotalDocs <= 0 || maxCapOnlineDF == nil {
		return 0, 0
	}
	return maxCapOnlineDF[low], maxCapOnlineTotalDocs
}

func lookupMaxCapLowLex(low string) maxCapLowLexEntry {
	if cached, ok := maxCapLowLexCache.Load(low); ok {
		return cached.(maxCapLowLexEntry)
	}
	isCJK := text.IsPureCJK(low)
	bpePrior := tokenzr.TokenPriorBits(low)
	val := tokenzr.BPEInflectionValence(low)
	st := text.StemToken(low)
	mr := text.MorphRootPrefix(low)
	if apoIdx := strings.IndexByte(low, '\''); apoIdx > 0 {
		base := low[:apoIdx]
		if text.StringShannonBits(base) >= text.AtomicConceptEntropyBits {
			if st == "" || st == low {
				st = base
			}
			if mr == "" || mr == low || mr == st {
				if baseSt := text.StemToken(base); baseSt != "" && baseSt != base {
					mr = baseSt
				}
			}
		}
	}
	if st != "" && st != low {
		if stP := tokenzr.TokenPriorBits(st); stP > bpePrior {
			bpePrior = 0.5*bpePrior + 0.5*stP
		}
		if stV := tokenzr.BPEInflectionValence(st); stV > val {
			val = stV
		}
	}
	effBits := bpePrior + 1.2*float64(val)
	isHigh := text.IsHighGainToken(low)
	slot1 := st
	if slot1 == "" || slot1 == low {
		if subs := tokenzr.SplitSubwords(low); len(subs) == 2 {
			s0 := strings.TrimSpace(subs[0])
			s1 := strings.TrimSpace(subs[1])
			if s0 != "" && s1 != "" && s1 != low && s1 != mr && text.IsHighGainToken(s0) && text.IsHighGainToken(s1) {
				slot1 = s1
			}
		}
	}
	entry := maxCapLowLexEntry{
		isCJK:    isCJK,
		bpePrior: bpePrior,
		effBits:  effBits,
		isHigh:   isHigh,
		st:       st,
		mr:       mr,
		slot1:    slot1,
	}
	maxCapLowLexCache.Store(low, entry)
	return entry
}

func lookupMaxCapPropPrior(wTrim string) bool {
	if cached, ok := maxCapPropPriorCache.Load(wTrim); ok {
		return cached.(bool)
	}
	res := tokenzr.IsBPEProperNounPrior(wTrim)
	maxCapPropPriorCache.Store(wTrim, res)
	return res
}

// PackMaxCapabilitySentence packs one sentence into the strongest blind-token
// atom frame: 128k BPE prior self-information (integer carrier width), stem/root
// harmonic slots, two-piece BPE compound harmonics, inline symbol units, unit
// dimension harmonics and structured ISO date beacons (English month/year
// alignment). Continuous gain only — no boolean gate rejects a token.
//
// This is the single source shared by the product ingest/recall path and the
// evaluator (`--pulse-tokenizer eval-shannon` reuses it for historical parity).
func PackMaxCapabilitySentence(enc *ramesdk.BlindEncoder, sentence string, sentIdx int) []ramesdk.BlindTokenAtom {
	baseWords := ramesdk.SplitWords(sentence)
	if len(baseWords) == 0 {
		return enc.PackSentenceAtoms(sentence)
	}
	baseFormMarks := ramesdk.BlindFormFlagMarks(sentence, baseWords)
	words := make([]string, 0, len(baseWords)+6)
	wordFormMarks := make([]uint8, 0, len(baseWords)+6)
	var beacons []ramesdk.BlindTokenAtom
	seenBeacon := make(map[uint64]bool, 4)
	addDateBeacon := func(iso string) {
		calH := enc.KeyedHash64("cal:" + iso)
		if !seenBeacon[calH] {
			seenBeacon[calH] = true
			beacons = append(beacons, ramesdk.BlindTokenAtom{
				H:          calH,
				RawBitsX10: ramesdk.CalendarBeaconWidthX10,
				ClassFlags: ramesdk.FlagHasDigit | ramesdk.FlagHasConn,
			})
		}
		isoH := enc.KeyedHash64(iso)
		if !seenBeacon[isoH] {
			seenBeacon[isoH] = true
			beacons = append(beacons, ramesdk.BlindTokenAtom{
				H:          isoH,
				RawBitsX10: 284,
				ClassFlags: ramesdk.FlagHasDigit | ramesdk.FlagHasConn,
			})
		}
	}
	seenDate := make(map[string]bool, 2)
	for _, iso := range ramesdk.ExtractStructuredDateHarmonics(sentence) {
		seenDate[iso] = true
		addDateBeacon(iso)
		words = append(words, iso)
		wordFormMarks = append(wordFormMarks, 0)
		if tParsed, err := time.Parse("2006-01-02", iso); err == nil {
			words = append(words, tParsed.Format("January"), tParsed.Format("Jan"), tParsed.Format("2006"))
			wordFormMarks = append(wordFormMarks, 0, 0, 0)
		}
	}
	for bwIdx, w := range baseWords {
		words = append(words, w)
		var fm uint8
		if bwIdx < len(baseFormMarks) {
			fm = baseFormMarks[bwIdx]
		}
		wordFormMarks = append(wordFormMarks, fm)
		wTrim := strings.TrimSpace(w)
		if strings.Count(wTrim, "-") == 2 && !seenDate[wTrim] {
			if tParsed, err := time.Parse("2006-01-02", wTrim); err == nil {
				seenDate[wTrim] = true
				addDateBeacon(wTrim)
				words = append(words, tParsed.Format("January"), tParsed.Format("Jan"), tParsed.Format("2006"))
				wordFormMarks = append(wordFormMarks, 0, 0, 0)
			}
		}
	}
	for _, symUnit := range ramesdk.ExtractInlineSymbolUnits(sentence) {
		words = append(words, symUnit)
		wordFormMarks = append(wordFormMarks, ramesdk.FlagIsQuantityOrUnit)
	}
	stripOrdNum := func(tok string) string {
		t := strings.TrimSpace(tok)
		dLen := 0
		for dLen < len(t) && t[dLen] >= '0' && t[dLen] <= '9' {
			dLen++
		}
		if dLen >= 1 && dLen <= 2 && len(t) > dLen && len(t)-dLen <= 2 {
			return t[:dLen]
		}
		return t
	}
	addDateRangeBeacons := func(monthStr, d1Str, d2Str, yearStr string) bool {
		startCand := monthStr + " " + stripOrdNum(d1Str) + " " + yearStr
		endCand := monthStr + " " + stripOrdNum(d2Str) + " " + yearStr
		for _, layout := range []string{"January 2 2006", "Jan 2 2006"} {
			p1, err1 := time.Parse(layout, startCand)
			p2, err2 := time.Parse(layout, endCand)
			if err1 == nil && err2 == nil && p1.Year() >= 1990 && p1.Year() <= 2100 && !p2.Before(p1) && p2.Sub(p1) <= 14*24*time.Hour {
				for cur := p1; !cur.After(p2); cur = cur.AddDate(0, 0, 1) {
					addDateBeacon(cur.Format("2006-01-02"))
				}
				return true
			}
		}
		return false
	}
	for wi := 0; wi+1 < len(baseWords); wi++ {
		w0, w1 := strings.TrimSpace(baseWords[wi]), strings.TrimSpace(baseWords[wi+1])
		if wi+4 < len(baseWords) {
			w2, w3, w4 := stripOrdNum(baseWords[wi+2]), stripOrdNum(baseWords[wi+3]), stripOrdNum(baseWords[wi+4])
			if addDateRangeBeacons(w0, stripOrdNum(w1), w3, w4) && w2 != "" {
				continue
			}
		}
		if wi+5 < len(baseWords) {
			w3, w4, w5 := stripOrdNum(baseWords[wi+3]), stripOrdNum(baseWords[wi+4]), stripOrdNum(baseWords[wi+5])
			if strings.EqualFold(w0, w3) && addDateRangeBeacons(w0, stripOrdNum(w1), w4, w5) {
				continue
			}
		}
		if wi+2 < len(baseWords) {
			w2 := strings.TrimSpace(baseWords[wi+2])
			cand3 := w0 + " " + w1 + " " + w2
			for _, layout := range []string{"January 2 2006", "Jan 2 2006", "2 January 2006", "2 Jan 2006"} {
				if parsed, err := time.Parse(layout, cand3); err == nil && parsed.Year() >= 1990 && parsed.Year() <= 2100 {
					addDateBeacon(parsed.Format("2006-01-02"))
					break
				}
			}
		}
	}

	headerBoost := 1.0
	if sentIdx <= 4 {
		headerBoost = 1.15
	}
	atoms := append(make([]ramesdk.BlindTokenAtom, 0, len(words)*2+len(beacons)), beacons...)
	for wordIdx, w := range words {
		wTrim := strings.TrimSpace(w)
		low := strings.ToLower(wTrim)
		if low == "" {
			continue
		}
		var flags uint8
		var rawWidth uint32
		hasLower := false
		for idx, r := range wTrim {
			rawWidth += uint32(ramesdk.CarrierUnits(r))
			if unicode.IsUpper(r) {
				if wordIdx > 0 || idx > 0 {
					flags |= ramesdk.FlagHasUpper
				}
				if idx > 0 && hasLower {
					flags |= ramesdk.FlagHasCamel
				}
			} else if unicode.IsLower(r) {
				hasLower = true
			} else if unicode.IsDigit(r) {
				flags |= ramesdk.FlagHasDigit
			} else if r == '-' || r == '_' || r == '.' {
				flags |= ramesdk.FlagHasConn
			}
		}
		if (flags&ramesdk.FlagHasConn) != 0 && (flags&ramesdk.FlagHasDigit) == 0 {
			continue
		}

		isDigit := (flags & ramesdk.FlagHasDigit) != 0
		lex := lookupMaxCapLowLex(low)
		isCJK := lex.isCJK
		isProp := (flags & (ramesdk.FlagHasUpper | ramesdk.FlagHasCamel)) != 0
		if !isProp {
			isProp = lookupMaxCapPropPrior(wTrim)
		}
		bpePrior := lex.bpePrior
		effBits := lex.effBits
		mr := lex.mr
		isHigh := lex.isHigh || isProp || isDigit || isCJK

		if onDF, onTotal := queryMaxCapOnlineDF(low); onTotal >= 16 && onDF > 0 {
			rho := float64(onDF) / float64(onTotal)
			invE := 1.0 / math.E
			// 泛滥套话阻尼起点：1 / (e^2 + e) ≈ 0.0990（约 9.9% 文档覆盖率），或极端刷屏 1/e ≈ 36.8%
			hubHorizon := 1.0 / (math.E*math.E + math.E)
			if rho > invE {
				// 极端全库刷屏模板词（df/N > 1/e ≈ 36.8%）：执行贝叶斯停时冲突洗脱，撤除专名免死金牌并退火至软地板
				bChar := text.StringShannonBits(low)
				bpePrior = text.OnlineDFConjugateBits(bChar, bpePrior, onDF, onTotal)
				effBits = text.OnlineDFConjugateBits(bChar, effBits, onDF, onTotal)
				isProp = false
				isHigh = bpePrior >= 11.20 || isDigit
			} else if isHigh && !isProp && !isDigit && rho > hubHorizon {
				// 单向高频套话连续消光律（One-Sided Online Hub Extinction）：
				// 对文档覆盖率超出 1/(e^2+e) 的非专名高频泛滥词施加连续对数阻尼，
				// 100% 保持专名与实义低频词的内禀相干比例不被破坏。
				damp := invE * math.Log(rho/hubHorizon)
				scale := 1.0 / (1.0 + damp)
				bpePrior *= scale
				effBits *= scale
			}
		}

		var bx10 uint16
		if !isHigh && !isCJK && (rawWidth <= 40 || (rawWidth <= 60 && bpePrior < 11.2)) {
			bx10 = 18
		} else if !isHigh && !isCJK {
			bx10 = 20
		} else if !isProp && !isDigit && !isCJK && bpePrior < 12.65 {
			bx10 = uint16(min(52, max(26, int((bpePrior-9.0)*12.0))))
		} else {
			excess := math.Max(1.2, effBits-9.2)
			superB := int(26.0 * math.Pow(excess, 1.32) * headerBoost)
			if int(rawWidth) > superB {
				superB = int(rawWidth)
			}
			if isProp || isDigit {
				flags |= ramesdk.FlagHasUpper
				superB = int(float64(superB) * 1.28)
			}
			bx10 = uint16(min(640, max(76, superB)))
		}
		bx10 = (bx10 + 1) &^ 1
		if bx10 < 18 {
			bx10 = 18
		}
		primaryFlags := flags &^ ramesdk.FlagHasConn
		if isDigit && (flags&ramesdk.FlagHasConn) != 0 {
			primaryFlags = flags
		}
		if wordIdx < len(wordFormMarks) {
			primaryFlags |= wordFormMarks[wordIdx]
		}
		atoms = append(atoms, ramesdk.BlindTokenAtom{
			H:          enc.KeyedHash64(low),
			RawBitsX10: bx10,
			ClassFlags: primaryFlags,
		})
		if !isDigit && !isCJK && bx10 >= 24 {
			harmFlags := (flags &^ ramesdk.FlagHasDigit) | ramesdk.FlagHasConn | ramesdk.FlagIsHarmonicCompanion
			slot1 := lex.slot1
			if slot1 != "" && slot1 != low {
				atoms = append(atoms, ramesdk.BlindTokenAtom{
					H:          enc.KeyedHash64(slot1),
					RawBitsX10: bx10 - 1,
					ClassFlags: harmFlags,
				})
			} else if len(atoms) == 1 {
				atoms = append(atoms, ramesdk.BlindTokenAtom{
					H:          enc.KeyedHash64(low),
					RawBitsX10: bx10 - 1,
					ClassFlags: harmFlags,
				})
			}
			if mr != "" && mr != low && mr != slot1 {
				atoms = append(atoms, ramesdk.BlindTokenAtom{
					H:          enc.KeyedHash64(mr),
					RawBitsX10: bx10 - 3,
					ClassFlags: harmFlags,
				})
			}
			if rawWidth >= 60 {
				if unitTag, _ := ramesdk.LookupUnitDimensionHarmonicPair(low); strings.HasPrefix(unitTag, "@unit:") && !strings.HasPrefix(unitTag, "@unit:time:") && !strings.HasPrefix(unitTag, "@unit:stat:") {
					atoms = append(atoms, ramesdk.BlindTokenAtom{
						H:          enc.KeyedHash64(unitTag),
						RawBitsX10: bx10 - 3,
						ClassFlags: harmFlags,
					})
				}
			}
		}
	}
	if len(atoms) == 0 {
		return enc.PackSentenceAtoms(sentence)
	}
	return atoms
}

// ConfigureMaxCapability applies the strongest assembly: structured date beacons
// plus PackMaxCapabilitySentence with the harmonic overlay. Ingest and query
// both read this single configuration.
func ConfigureMaxCapability(v *thinclient.Vault) error {
	if v == nil {
		return fmt.Errorf("langpack: nil vault")
	}
	v.SetCanonicalHarmonics(ramesdk.CanonicalHarmonicOptions{EnableStructuredDates: true})
	v.SetSegmenterOptions(ramesdk.SegmenterPackOptions{
		SentenceAtomPacker: PackMaxCapabilitySentence,
		HarmonicOverlay:    true,
	})
	return nil
}

// ConfigureDefault applies the shared default assembly for the product vault and
// the evaluator's product surface. With no explicit selection the strongest
// assembly is used. An explicitly selected language pack (en/zh/ja/ko/sea/de/
// multi) remains available as a diagnostic override, and "off" disables the
// harmonic channel exactly as before.
func ConfigureDefault(v *thinclient.Vault, language string) error {
	lang := strings.ToLower(strings.TrimSpace(language))
	switch lang {
	case "", MaxCapabilityLanguage:
		return ConfigureMaxCapability(v)
	case "off":
		return nil
	default:
		if err := ConfigureVault(v, lang); err != nil {
			return err
		}
		v.SetCanonicalHarmonics(ramesdk.CanonicalHarmonicOptions{EnableStructuredDates: true})
		return nil
	}
}
