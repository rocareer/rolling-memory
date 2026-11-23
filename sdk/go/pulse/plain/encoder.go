// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package plain self-maintains the complete Step-1 text normalization, Unicode
// geometric sentence/word segmentation, 8-bit structural form-flag marking,
// structured ISO date extraction, closed-set decoration table (ISO-3166
// geography, kinship, roles, calendar, biology, venues, food, and units), and
// keyed 64-bit HMAC-SHA256 encoding.
package plain

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rocareer/rolling-memory/sdk/go/public/closedset"
	"github.com/rocareer/rolling-memory/sdk/go/public/shaping"
	"github.com/rocareer/rolling-memory/sdk/go/public/tokenzr"
)

const (
	FlagHasUpper            uint8 = 1 << 0
	FlagHasCamel            uint8 = 1 << 1
	FlagHasDigit            uint8 = 1 << 2
	FlagHasConn             uint8 = 1 << 3
	FlagIsHarmonicCompanion uint8 = 1 << 4
	FlagIsFocalHeader       uint8 = 1 << 5
	FlagHasBoundaryQuote    uint8 = 1 << 6
	FlagIsQuantityOrUnit    uint8 = 1 << 7
)

type UnitMeta struct {
	Author     string `json:"author,omitempty"`
	NarratedAt int64  `json:"narrated_at,omitempty"`
	OccurredAt int64  `json:"occurred_at,omitempty"`
	IngestedAt int64  `json:"ingested_at,omitempty"`
	FileURL    string `json:"file_url,omitempty"`
	FileParse  string `json:"file_parse,omitempty"`
	BlockID    string `json:"block_id,omitempty"`
	DocHash    uint64 `json:"doc_h,omitempty"`
	BlockHash  uint64 `json:"block_h,omitempty"`
	LineStart  int    `json:"line_start,omitempty"`
	LineEnd    int    `json:"line_end,omitempty"`
}

func FormatPlaintextWithMeta(rawText string, meta UnitMeta) string {
	text := strings.TrimSpace(rawText)
	fileParse := strings.TrimSpace(meta.FileParse)
	if text == "" && fileParse != "" {
		text = fileParse
		rawText = fileParse
	} else if text == "" {
		return rawText
	} else if fileParse != "" && !strings.Contains(text, fileParse) {
		text = text + "\n" + fileParse
		rawText = text
	}
	author := strings.TrimSpace(meta.Author)
	if author == "" && meta.NarratedAt <= 0 && meta.OccurredAt <= 0 && meta.IngestedAt <= 0 {
		return rawText
	}
	fmtDay := func(ts int64) string {
		if ts <= 0 {
			return ""
		}
		return time.Unix(ts, 0).UTC().Format("2006-01-02")
	}
	occDay := fmtDay(meta.OccurredAt)
	narDay := fmtDay(meta.NarratedAt)
	ingDay := fmtDay(meta.IngestedAt)
	var timeParts []string
	primaryDay := occDay
	if primaryDay == "" {
		primaryDay = narDay
	}
	if primaryDay != "" {
		timeParts = append(timeParts, primaryDay)
	}
	if narDay != "" && narDay != primaryDay {
		timeParts = append(timeParts, "narrated:"+narDay)
	}
	if ingDay != "" && ingDay != primaryDay && ingDay != narDay {
		timeParts = append(timeParts, "ingested:"+ingDay)
	}
	timeTag := ""
	if len(timeParts) > 0 {
		timeTag = "[" + strings.Join(timeParts, "|") + "]"
	}
	prefix := ""
	if timeTag != "" && author != "" {
		prefix = timeTag + " " + author + ": "
	} else if timeTag != "" {
		prefix = timeTag + " "
	} else if author != "" {
		prefix = author + ": "
	}
	if prefix == "" || strings.HasPrefix(text, prefix) {
		return text
	}
	return prefix + text
}

type BlindTokenTerm struct {
	H          uint64   `json:"h"`
	RawBitsX10 uint16   `json:"b"`
	ClassFlags uint8    `json:"f"`
	Role       TermRole `json:"role,omitempty"`
}

type BlindTokenAtom = BlindTokenTerm

type HarmonicExpander func(token string) []string

type CanonicalHarmonicOptions struct {
	EnableStructuredDates bool
	Expander              HarmonicExpander
	// RunFolder is the optional pluggable document-ingestion run-folding rule
	// chain; when nil, DefaultRunFoldRule (all calibrated rules) is used.
	RunFolder RunFoldRule
}

type BlindEncoder struct {
	secretKey [32]byte
	segOpts   SegmenterPackOptions
	harmOpts  CanonicalHarmonicOptions
}

func (e *BlindEncoder) SetWordSegmenter(seg WordSegmenter) {
	if e == nil {
		return
	}
	e.segOpts = SegmenterPackOptions{Segmenter: seg, HarmonicOverlay: true}
}

func (e *BlindEncoder) SetSegmenterOptions(opts SegmenterPackOptions) {
	if e == nil {
		return
	}
	e.segOpts = opts
}

// CanonicalizeApostrophe normalizes curly apostrophes, backticks, and fullwidth
// ASCII alphanumerics/connectors into canonical halfwidth ASCII and strips
// invisible format characters (soft hyphen U+00AD, ZWSP, BOM, ZWNJ/ZWJ).
func CanonicalizeApostrophe(s string) string {
	return shaping.CanonicalizeApostrophe(s)
}

// IsContinuousSyllabicOrIdeographic reports whether r belongs to a script without word spaces.
func IsContinuousSyllabicOrIdeographic(r rune) bool {
	return r == 'ー' ||
		unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r) ||
		unicode.Is(unicode.Thai, r) ||
		unicode.Is(unicode.Lao, r) ||
		unicode.Is(unicode.Khmer, r) ||
		unicode.Is(unicode.Myanmar, r) ||
		unicode.Is(unicode.Tibetan, r)
}

func (e *BlindEncoder) SetCanonicalHarmonics(opts CanonicalHarmonicOptions) {
	if e == nil {
		return
	}
	e.harmOpts = opts
}

func NewBlindEncoder(secret []byte) *BlindEncoder {
	enc := &BlindEncoder{}
	if len(secret) > 0 {
		enc.secretKey = sha256.Sum256(secret)
	} else {
		_, _ = rand.Read(enc.secretKey[:])
	}
	return enc
}

// CloneWithPepper 以新 pepper 派生键控编码器并完整保留 segOpts/harmOpts 配置；
// pepper 为空时沿用原键控密钥（不触发随机换键）。nil 接收者等价于 NewBlindEncoder。
func (e *BlindEncoder) CloneWithPepper(pepper []byte) *BlindEncoder {
	if e == nil {
		return NewBlindEncoder(pepper)
	}
	clone := &BlindEncoder{segOpts: e.segOpts, harmOpts: e.harmOpts}
	if len(pepper) > 0 {
		clone.secretKey = sha256.Sum256(pepper)
	} else {
		clone.secretKey = e.secretKey
	}
	return clone
}

func NewBlindEncoderFromRawKey(rawKey [32]byte) *BlindEncoder {
	return &BlindEncoder{secretKey: rawKey}
}

func (e *BlindEncoder) KeyedHash64(token string) uint64 {
	if len(token) <= 64 {
		var inBuf [128]byte
		var outBuf [96]byte
		for i := 0; i < 32; i++ {
			k := e.secretKey[i]
			inBuf[i] = k ^ 0x36
			outBuf[i] = k ^ 0x5c
		}
		for i := 32; i < 64; i++ {
			inBuf[i] = 0x36
			outBuf[i] = 0x5c
		}
		copy(inBuf[64:], token)
		inner := sha256.Sum256(inBuf[:64+len(token)])
		copy(outBuf[64:], inner[:])
		outer := sha256.Sum256(outBuf[:])
		return binary.LittleEndian.Uint64(outer[:8])
	}
	mac := hmac.New(sha256.New, e.secretKey[:])
	mac.Write([]byte(token))
	return binary.LittleEndian.Uint64(mac.Sum(nil)[:8])
}

func isHanRune(r rune) bool           { return shaping.IsHanRune(r) }
func isHangulRune(r rune) bool        { return shaping.IsHangulRune(r) }
func isKanaOrSEAsianRune(r rune) bool { return shaping.IsKanaOrSEAsianRune(r) }
func isKatakanaRune(r rune) bool {
	return r == 0x30FC || (r >= 0x30A0 && r <= 0x30FF) || (r >= 0xFF66 && r <= 0xFF9F)
}
func alphaScriptFamily(r rune) uint8 {
	switch {
	case isHangulRune(r):
		return 2
	case r >= 0x0400 && r <= 0x052F: // Cyrillic
		return 3
	case r >= 0x0370 && r <= 0x03FF: // Greek
		return 4
	case r >= 0x0600 && r <= 0x06FF: // Arabic
		return 5
	case r >= 0x0590 && r <= 0x05FF: // Hebrew
		return 6
	case r >= 0x0900 && r <= 0x0DFF: // Indic
		return 7
	default:
		return 1
	}
}
func isVariationSelector(r rune) bool {
	return shaping.IsVariationSelector(r)
}
func isLetterOrNumber(r rune) bool {
	return !isVariationSelector(r) && (unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r))
}
func isUnspacedIdeographic(r rune) bool { return shaping.IsUnspacedIdeographic(r) }
func isWordConn(r rune) bool            { return r == '-' || r == '_' || r == '.' }
func isInlineApostrophe(r rune) bool    { return r == '\'' || r == '’' || r == '‘' }
func runeAt(s string, byteIdx int) rune {
	r, _ := utf8.DecodeRuneInString(s[byteIdx:])
	return r
}

func rawCompoundSubwords(tok string) []string {
	var parts []string
	var cur strings.Builder
	var prevR rune
	hasPrev := false
	for idx, r := range tok {
		if isWordConn(r) {
			if cur.Len() > 0 {
				parts = append(parts, cur.String())
				cur.Reset()
			}
			hasPrev = false
			continue
		}
		w := utf8.RuneLen(r)
		nextIsLower := false
		if idx+w < len(tok) {
			if nr, nw := utf8.DecodeRuneInString(tok[idx+w:]); unicode.IsLower(nr) && alphaScriptFamily(r) == alphaScriptFamily(nr) {
				if !(idx+w+nw == len(tok) && (nr == 's' || nr == 'S')) {
					nextIsLower = true
				}
			}
		}
		if hasPrev && cur.Len() > 0 && ((unicode.IsLower(prevR) && unicode.IsUpper(r) && !(idx+w == len(tok) && r == 'S')) ||
			(unicode.IsUpper(prevR) && unicode.IsUpper(r) && nextIsLower) ||
			(unicode.IsUpper(prevR) && (r == 's') && idx+w == len(tok) && cur.Len() >= 2) ||
			(unicode.IsNumber(prevR) != unicode.IsNumber(r)) ||
			(unicode.IsLetter(prevR) && unicode.IsLetter(r) && alphaScriptFamily(prevR) != alphaScriptFamily(r))) {
			parts = append(parts, cur.String())
			cur.Reset()
		}
		cur.WriteRune(r)
		prevR, hasPrev = r, true
	}
	if cur.Len() > 0 {
		parts = append(parts, cur.String())
	}
	if len(parts) < 2 {
		return nil
	}
	return parts
}

func isAccidentalMidCaseSingleWord(tok string) bool {
	hasLower := false
	hasMidUpper := false
	for idx, r := range tok {
		if isWordConn(r) || unicode.IsNumber(r) || isInlineApostrophe(r) {
			return false
		}
		if unicode.IsLower(r) {
			hasLower = true
		} else if unicode.IsUpper(r) && idx > 0 {
			hasMidUpper = true
		}
	}
	if !hasLower || !hasMidUpper {
		return false
	}
	low := strings.ToLower(strings.TrimSpace(tok))
	if low == "" || len(tokenzr.SplitSubwords(" "+low)) > 1 {
		return false
	}
	parts := rawCompoundSubwords(tok)
	if len(parts) < 2 {
		return true
	}
	if len(parts) == 2 && parts[1] == "s" {
		return false
	}
	bpePieces := tokenzr.SplitBPEDirectPieces(low)
	if len(bpePieces) == 0 {
		return true
	}
	return len(parts[0]) < len(bpePieces[0])
}

func splitCompoundSubwords(tok string) []string {
	if isAccidentalMidCaseSingleWord(tok) {
		return nil
	}
	return rawCompoundSubwords(tok)
}

func CarrierUnits(r rune) uint16 {
	if r < 0x80 {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return 20
		}
		if r >= '0' && r <= '9' {
			return 33
		}
		return 10
	}
	if isHanRune(r) {
		return 100
	}
	if isHangulRune(r) {
		return 70
	}
	if isKanaOrSEAsianRune(r) {
		return 42
	}
	if unicode.IsDigit(r) {
		return 33
	}
	if unicode.IsLetter(r) {
		return 20
	}
	return 10
}

func clampBitsX10(v uint32) uint16 {
	if v < 10 {
		return 10
	}
	if v > 65535 {
		return 65535
	}
	return uint16(v)
}

// harmonicGlueWidthX10 is the carrier width at which a long token is handed to
// the adaptive harmonic glue path instead of being decomposed into sub-terms.
const harmonicGlueWidthX10 uint16 = 420

// wordCarrierWidth mirrors the carrier-width loop used when a word is packed:
// runs of three or more identical letters carry a quarter of their width.
func wordCarrierWidth(w string) uint32 {
	var raw uint32
	var prev rune
	runLen := 0
	for _, r := range w {
		if lr := unicode.ToLower(r); lr == prev {
			runLen++
		} else {
			prev, runLen = lr, 1
		}
		rw := uint32(CarrierUnits(r))
		if runLen >= 3 {
			rw /= 4
		}
		raw += rw
	}
	return raw
}

// isUnspacedNonHanRun reports whether w is one continuous run in a script that
// writes without word spaces and is not an ideograph the bigram path handles.
func isUnspacedNonHanRun(w string) bool {
	if w == "" {
		return false
	}
	runes := 0
	for _, r := range w {
		if isHanRune(r) || !isUnspacedIdeographic(r) {
			return false
		}
		runes++
	}
	return runes >= 2
}

// expandUnspacedRunWords gives a non-Han unspaced run the same adjacent-bigram
// then unigram ladder an ideographic run gets, unless the adaptive harmonic
// glue would take the run instead. Glue needs an encoder without a canonical
// expander and a carrier width above the glue threshold; when it fires, leaving
// the run whole is what keeps the glue contract intact.
//
// Expansion runs before the declarative form marks are computed, so those marks
// and the compound-parent table stay index-aligned with the expanded words.
func (e *BlindEncoder) expandUnspacedRunWords(words []string) []string {
	glueAvailable := e == nil || e.harmOpts.Expander == nil
	var out []string
	changed := false
	for _, w := range words {
		if !isUnspacedNonHanRun(w) {
			out = append(out, w)
			continue
		}
		// 闭集扩展器命中时保留整词：闭集谐波（单位/货币/统计标签）按整词匹配，
		// 先展开会把词拆碎，闭集路径随之永远失效。
		if e != nil && e.harmOpts.Expander != nil {
			if harmonics := e.harmOpts.Expander(w); len(harmonics) > 0 {
				out = append(out, w)
				continue
			}
		}
		glueWillFire := glueAvailable &&
			clampBitsX10(wordCarrierWidth(w)) > harmonicGlueWidthX10 &&
			len(splitCompoundSubwords(w)) < 2
		if glueWillFire {
			out = append(out, w)
			continue
		}
		runes := []rune(w)
		if len(runes) < 2 {
			out = append(out, w)
			continue
		}
		changed = true
		for k := 0; k+1 < len(runes); k++ {
			out = append(out, string(runes[k:k+2]))
		}
		for k := 0; k < len(runes); k++ {
			out = append(out, string(runes[k:k+1]))
		}
	}
	if !changed {
		return words
	}
	return out
}

func classifyRuneFlag(r rune, upperAllowed, hasLower bool) (flags uint8, nextHasLower bool) {
	nextHasLower = hasLower
	if unicode.IsUpper(r) {
		if upperAllowed {
			flags |= FlagHasUpper
		}
		if hasLower {
			flags |= FlagHasCamel
		}
	} else if unicode.IsLower(r) {
		nextHasLower = true
	} else if unicode.IsNumber(r) {
		flags |= FlagHasDigit
	} else if isWordConn(r) {
		flags |= FlagHasConn
	}
	return flags, nextHasLower
}

func SplitSentences(text string) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	var out []string
	var cur strings.Builder
	for i, n := 0, len(text); i < n; {
		r, w := utf8.DecodeRuneInString(text[i:])
		isBound := r == '\n' || r == '\r' || r == '。' || r == '！' || r == '？' || r == '；' || r == ';' || r == '!' || r == '?'
		if !isBound && r == '.' {
			prevAlnum, nextAlnum := false, false
			if i > 0 {
				pr, _ := utf8.DecodeLastRuneInString(text[:i])
				prevAlnum = isLetterOrNumber(pr)
			}
			if i+w < n {
				nr, _ := utf8.DecodeRuneInString(text[i+w:])
				nextAlnum = isLetterOrNumber(nr)
			}
			isBound = !(prevAlnum && nextAlnum)
		}
		if isBound {
			if s := strings.TrimSpace(cur.String()); s != "" {
				out = append(out, s)
			}
			cur.Reset()
		} else {
			cur.WriteRune(r)
		}
		i += w
	}
	if tail := strings.TrimSpace(cur.String()); tail != "" {
		out = append(out, tail)
	}
	return out
}

func SplitWords(sent string) []string {
	if strings.TrimSpace(sent) == "" {
		return nil
	}
	var units []string
	for i, n := 0, len(sent); i < n; {
		r, w := utf8.DecodeRuneInString(sent[i:])
		if unicode.Is(unicode.Sc, r) || r == '℃' || r == '℉' || (unicode.Is(unicode.So, r) && (r >= 0x1F000 || closedset.HasToken(strings.ToLower(sent[i:i+w])))) || (r == '%' && i > 0 && sent[i-1] >= '0' && sent[i-1] <= '9') {
			units = append(units, sent[i:i+w])
			i += w
			continue
		}
		if !unicode.IsLetter(r) && !unicode.IsNumber(r) {
			i += w
			continue
		}
		if isUnspacedIdeographic(r) {
			runStart := i
			for i < n {
				cr, cw := utf8.DecodeRuneInString(sent[i:])
				if !isUnspacedIdeographic(cr) {
					break
				}
				if isHanRune(cr) {
					var hanOffsets []int
					end := i
					for end < n {
						nr, nw := utf8.DecodeRuneInString(sent[end:])
						if !isHanRune(nr) {
							break
						}
						hanOffsets = append(hanOffsets, end)
						end += nw
					}
					hanOffsets = append(hanOffsets, end)
					if runCount := len(hanOffsets) - 1; runCount == 1 {
						units = append(units, sent[hanOffsets[0]:hanOffsets[1]])
					} else {
						for k := 0; k < runCount-1; k++ {
							units = append(units, sent[hanOffsets[k]:hanOffsets[k+2]])
						}
						for k := 0; k < runCount; k++ {
							units = append(units, sent[hanOffsets[k]:hanOffsets[k+1]])
						}
					}
					i = end
					continue
				}
				end := i + cw
				startKata := isKatakanaRune(cr)
				for end < n {
					nr, nw := utf8.DecodeRuneInString(sent[end:])
					if !isUnspacedIdeographic(nr) || isHanRune(nr) || isKatakanaRune(nr) != startKata {
						break
					}
					end += nw
				}
				units = append(units, sent[i:end])
				i = end
			}
			var ideoOffsets []int
			for pos := runStart; pos < i; {
				ideoOffsets = append(ideoOffsets, pos)
				_, rw := utf8.DecodeRuneInString(sent[pos:])
				pos += rw
			}
			ideoOffsets = append(ideoOffsets, i)
			if runCount := len(ideoOffsets) - 1; runCount >= 2 {
				for a := 0; a < runCount; a++ {
					for b := a + 2; b <= runCount && b-a <= 9; b++ {
						span := strings.ToLower(sent[ideoOffsets[a]:ideoOffsets[b]])
						if closedset.HasToken(span) && (b-a >= 3 || !isHanRune(runeAt(sent, ideoOffsets[a])) || !isHanRune(runeAt(sent, ideoOffsets[a+1]))) {
							units = append(units, sent[ideoOffsets[a]:ideoOffsets[b]])
						}
					}
				}
			}
			continue
		}
		end := i + w
		hasVarSel := false
		mergedDigitComma := false
		for end < n {
			nr, nw := utf8.DecodeRuneInString(sent[end:])
			if isVariationSelector(nr) {
				hasVarSel = true
				end += nw
				continue
			}
			if isLetterOrNumber(nr) && !isUnspacedIdeographic(nr) {
				end += nw
				continue
			}
			if (isWordConn(nr) || isInlineApostrophe(nr)) && end+nw < n {
				if nextR, _ := utf8.DecodeRuneInString(sent[end+nw:]); isLetterOrNumber(nextR) && !isUnspacedIdeographic(nextR) {
					end += nw
					continue
				}
			}
			// 千分位分组几何（digit ',' digit{3} 且第 4 位非数字）：
			// "1,200" / "1,234,567" 粘合并剥除分组逗号，与无分组写法 "1200" 落同一键控身份
			// （跨格式精确桥）。列表逗号（后随空格）与 1-2 位小数逗号（如 "3,50"）不受影响。
			if nr == ',' && end > i && end+nw < n {
				if pr, _ := utf8.DecodeLastRuneInString(sent[:end]); pr >= '0' && pr <= '9' {
					d, j := 0, end+nw
					for j < n && d < 4 && sent[j] >= '0' && sent[j] <= '9' {
						d++
						j++
					}
					if d == 3 {
						mergedDigitComma = true
						end += nw
						continue
					}
				}
			}
			break
		}
		tok := sent[i:end]
		if mergedDigitComma {
			tok = strings.ReplaceAll(tok, ",", "")
		}
		if hasVarSel {
			var clean strings.Builder
			clean.Grow(len(tok))
			for _, tr := range tok {
				if !isVariationSelector(tr) {
					clean.WriteRune(tr)
				}
			}
			tok = clean.String()
		}
		if end < n && sent[end] == '$' && len(tok) <= 3 {
			if closedset.HasToken(strings.ToLower(tok) + "$") {
				end++
				tok = sent[i:end]
			}
		}
		if end+1 < n && sent[end] == '+' && sent[end+1] == '+' && (strings.ToLower(tok) == "c" || closedset.HasToken(strings.ToLower(tok)+"++")) {
			end += 2
			tok = sent[i:end]
		}
		units = append(units, tok)
		if subs := splitCompoundSubwords(tok); len(subs) >= 2 {
			units = append(units, subs...)
		}
		if end < n && len(tok) <= 2 && strings.HasPrefix(sent[end:], "月") {
			if closedset.HasToken(strings.ToLower(tok) + "月") {
				units = append(units, tok+"月")
			}
		}
		i = end
	}
	return units
}

func (e *BlindEncoder) packSymbolTerms(sent string) []BlindTokenTerm {
	var atoms []BlindTokenTerm
	var prevR rune
	runLen, hasLower := 0, false
	for idx, r := range sent {
		if unicode.IsSpace(r) {
			continue
		}
		if lr := unicode.ToLower(r); lr == prevR {
			runLen++
		} else {
			prevR, runLen = lr, 1
		}
		rw := uint32(CarrierUnits(r))
		if runLen >= 3 {
			rw /= 4
		}
		var fl uint8
		fl, hasLower = classifyRuneFlag(r, idx > 0, idx > 0 && hasLower)
		atoms = append(atoms, BlindTokenTerm{H: e.KeyedHash64(strings.ToLower(string(r))), RawBitsX10: clampBitsX10(rw), ClassFlags: fl})
	}
	return atoms
}

func (e *BlindEncoder) PackSentenceTerms(sent string) []BlindTokenTerm {
	return e.packSentenceWords(sent, SplitWords(sent))
}

func (e *BlindEncoder) PackSentenceAtoms(sent string) []BlindTokenTerm {
	return e.PackSentenceTerms(sent)
}

func leadingFieldBodyWordIdx(sent string, words []string) int {
	if len(words) < 2 {
		return -1
	}
	colonIdx := strings.IndexAny(sent, ":：")
	if colonIdx <= 0 {
		return -1
	}
	prefix := sent[:colonIdx]
	if strings.ContainsAny(prefix, ".,;!?。，；！？\n\r") {
		return -1
	}
	prefixWords := SplitWords(prefix)
	if len(prefixWords) >= 1 && len(prefixWords) <= 2 && len(prefixWords) < len(words) {
		return len(prefixWords)
	}
	return -1
}

func (e *BlindEncoder) packSentenceWords(sent string, words []string) []BlindTokenTerm {
	if len(words) == 0 {
		return e.packSymbolTerms(sent)
	}
	words = e.expandUnspacedRunWords(words)
	// 声明式高 4 位形态标志（bit5 焦点头 / bit6 定界包络 / bit7 数值-量纲对）与 words 逐位对齐预计算，
	// 见 encoder_flags.go（纯 Unicode 形态、零词表、语种无关）。
	formMarks, compoundParents, bodyStartIdx := blindFormFlagMarksAndMeta(sent, words)
	shieldBodyInitialUpper := false
	if bodyStartIdx > 0 && bodyStartIdx+1 < len(words) {
		if rNext, _ := utf8.DecodeRuneInString(words[bodyStartIdx+1]); unicode.IsLower(rNext) {
			shieldBodyInitialUpper = true
		}
	}
	atoms := make([]BlindTokenTerm, 0, len(words))
	sentScanPos := 0
	for wordIdx, w := range words {
		wStart := -1
		isSubpiece := wordIdx < len(compoundParents) && compoundParents[wordIdx] >= 0
		if !isSubpiece {
			if rel := strings.Index(sent[sentScanPos:], w); rel >= 0 {
				wStart = sentScanPos + rel
				sentScanPos = wStart + len(w)
			}
		}
		low := strings.ToLower(strings.TrimSpace(w))
		if low == "" {
			continue
		}
		suppressMidCase := isAccidentalMidCaseSingleWord(w)
		isSentInitialWord := wordIdx == 0 || (wordIdx == bodyStartIdx && shieldBodyInitialUpper)
		var rawWidth uint32
		var flags uint8
		var prevR rune
		runLen, hasLower := 0, false
		stemAlnum := 0
		seenApostrophe := false
		for idx, r := range w {
			if isInlineApostrophe(r) {
				seenApostrophe = true
			} else if !seenApostrophe && isLetterOrNumber(r) {
				stemAlnum++
			}
			if lr := unicode.ToLower(r); lr == prevR {
				runLen++
			} else {
				prevR, runLen = lr, 1
			}
			rw := uint32(CarrierUnits(r))
			if runLen >= 3 {
				rw /= 4
			}
			rawWidth += rw
			var fl uint8
			upperAllow := (!isSentInitialWord || idx > 0) && !suppressMidCase
			camelAllow := idx > 0 && hasLower && !suppressMidCase
			fl, hasLower = classifyRuneFlag(r, upperAllow, camelAllow)
			flags |= fl
		}
		if stemAlnum == 1 {
			flags &^= FlagHasUpper | FlagHasCamel
		}
		clampedW := clampBitsX10(rawWidth)
		flags |= formMarks[wordIdx]
		if (e == nil || e.harmOpts.Expander == nil) && clampedW > harmonicGlueWidthX10 && len(splitCompoundSubwords(w)) < 2 {
			prevLen := len(atoms)
			atoms = e.appendAdaptiveHarmonicTerms(atoms, w, wordIdx, 420)
			if len(atoms) > prevLen && formMarks[wordIdx] != 0 {
				atoms[prevLen].ClassFlags |= formMarks[wordIdx]
			}
			continue
		}
		slotStart := len(atoms)
		atoms = e.appendWordWithCanonicalHarmonics(atoms, low, clampedW, flags)
		if !isSubpiece && e != nil && e.harmOpts.Expander != nil && stemAlnum >= 3 && wordIdx+1 < len(words) {
			nextIdx := wordIdx + 1
			for nextIdx < len(words) && nextIdx < len(compoundParents) && compoundParents[nextIdx] >= 0 {
				nextIdx++
			}
			if nextIdx < len(words) {
				nextW := words[nextIdx]
				// Only an explicitly verified all-whitespace gap qualifies: when either
				// position is unknown (overlapping tokens or consumed leading
				// substrings make the Index lookups miss), fail closed instead of
				// injecting the biword across an unexamined span.
				pureSpaceGap := false
				if wStart >= 0 {
					if relNext := strings.Index(sent[sentScanPos:], nextW); relNext >= 0 {
						pureSpaceGap = true
						for _, gr := range sent[sentScanPos : sentScanPos+relNext] {
							if !unicode.IsSpace(gr) {
								pureSpaceGap = false
								break
							}
						}
					}
				}
				nextLow := strings.ToLower(strings.TrimSpace(nextW))
				nextAlnum := 0
				if pureSpaceGap {
					for _, nr := range nextLow {
						if isLetterOrNumber(nr) {
							nextAlnum++
						}
					}
				}
				if nextAlnum >= 3 {
					if biTag := lookupFirstClosedSetDictTag(low + nextLow); biTag != "" {
						biH := e.KeyedHash64(strings.ToLower(strings.TrimSpace(biTag)))
						dup := false
						for _, prev := range atoms[slotStart:] {
							if prev.H == biH {
								dup = true
								break
							}
						}
						if !dup {
							atoms = append(atoms, BlindTokenTerm{
								H:          biH,
								RawBitsX10: evenBitsX10(clampedW, 24),
								ClassFlags: FlagHasConn | FlagIsHarmonicCompanion,
								Role:       RoleBiwordHomomorph,
							})
						}
					}
				}
			}
		}
	}
	atoms = e.appendStructuredDateHarmonicTerms(sent, atoms)
	return atoms
}

func ExtractStructuredDateHarmonics(sent string) []string {
	return shaping.ExtractStructuredDateHarmonics(sent)
}

func evenBitsX10(clampedW, minW uint16) uint16 {
	evenW := clampedW
	if (evenW & 1) != 0 {
		if evenW < 65534 {
			evenW++
		} else {
			evenW--
		}
	}
	if evenW < minW {
		evenW = minW
	}
	return evenW
}

func (e *BlindEncoder) appendWordWithCanonicalHarmonics(atoms []BlindTokenTerm, low string, clampedW uint16, flags uint8) []BlindTokenTerm {
	if e == nil || e.harmOpts.Expander == nil {
		return append(atoms, BlindTokenTerm{H: e.KeyedHash64(low), RawBitsX10: clampedW, ClassFlags: flags})
	}
	tags := e.harmOpts.Expander(low)
	var cleanTags [2]string
	nClean := 0
	for _, t := range tags {
		if tt := strings.ToLower(strings.TrimSpace(t)); tt != "" && tt != low {
			cleanTags[nClean] = tt
			nClean++
			if nClean == 2 {
				break
			}
		}
	}
	if nClean == 0 {
		return append(atoms, BlindTokenTerm{H: e.KeyedHash64(low), RawBitsX10: evenBitsX10(clampedW, 20), ClassFlags: flags})
	}
	evenW := evenBitsX10(clampedW, 24)
	primaryH := e.KeyedHash64(low)
	atoms = append(atoms, BlindTokenTerm{H: primaryH, RawBitsX10: evenW, ClassFlags: flags &^ FlagHasConn, Role: RolePrimary})
	var prevHC uint64
	morphOrd := 0
	for i := 0; i < nClean; i++ {
		ct := cleanTags[i]
		hc := e.KeyedHash64(ct)
		if hc == primaryH || (i > 0 && hc == prevHC) {
			continue
		}
		prevHC = hc
		isClosed := strings.HasPrefix(ct, "@") || strings.HasPrefix(ct, "cal:")
		hasDigitInTag := false
		for _, r := range ct {
			if unicode.IsNumber(r) {
				hasDigitInTag = true
				break
			}
		}
		strippedDigit := !isClosed && (flags&FlagHasDigit != 0) && !hasDigitInTag
		if !isClosed {
			morphOrd++
		}
		wc := evenW - 1
		role := RoleMorphStem
		if isClosed {
			role = RoleClosedBeacon
		} else if morphOrd >= 2 || strippedDigit {
			wc = evenW - 3
			role = RoleSubwordPiece
		}
		atoms = append(atoms, BlindTokenTerm{H: hc, RawBitsX10: wc, ClassFlags: FlagHasConn | FlagIsHarmonicCompanion, Role: role})
	}
	return atoms
}

const (
	// isoDateCarrierBitsX10 是标准 10 字符 ISO-8601 日期 "YYYY-MM-DD" 的精确逐字符载体宽度：
	// 8 个数字字符 (8 * 33 = 264) + 2 个连字符 (2 * 10 = 20) = 284。
	isoDateCarrierBitsX10 uint16 = 8*33 + 2*10
	// calBeaconCarrierBitsX10 是结构化日历主信标 "cal:YYYY-MM-DD" 的声明载体宽度（2 * 284 + 20 = 588）。
	calBeaconCarrierBitsX10 uint16 = 2*isoDateCarrierBitsX10 + 20
)

func (e *BlindEncoder) appendStructuredDateHarmonicTerms(sent string, atoms []BlindTokenTerm) []BlindTokenTerm {
	if e == nil || !e.harmOpts.EnableStructuredDates || len(atoms) == 0 {
		return atoms
	}
	dates := ExtractStructuredDateHarmonics(sent)
	if len(dates) == 0 {
		return atoms
	}
	// 月伴生信标（@cal:month:MM）沿用词槽折叠的既有归属（跟随其前的末位主词项），
	// 与句级结构化主日历信标（cal:YYYY-MM-DD）保持正交。
	existing := make(map[uint64]bool, len(atoms))
	for _, a := range atoms {
		existing[a.H] = true
	}
	for _, iso := range dates {
		calH := e.KeyedHash64("cal:" + iso)
		if !existing[calH] {
			existing[calH] = true
			atoms = append(atoms, BlindTokenTerm{
				H:          calH,
				RawBitsX10: calBeaconCarrierBitsX10,
				ClassFlags: FlagHasDigit | FlagHasConn,
				Role:       RolePrimary,
			})
		}
		h := e.KeyedHash64(iso)
		if !existing[h] {
			existing[h] = true
			atoms = append(atoms, BlindTokenTerm{
				H:          h,
				RawBitsX10: isoDateCarrierBitsX10,
				ClassFlags: FlagHasDigit | FlagHasConn,
				Role:       RolePrimary,
			})
		}
		if e.harmOpts.Expander != nil && len(iso) == 10 && iso[4] == '-' && iso[7] == '-' {
			mH := e.KeyedHash64("@cal:month:" + iso[5:7])
			if !existing[mH] {
				existing[mH] = true
				atoms = append(atoms, BlindTokenTerm{
					H:          mH,
					RawBitsX10: isoDateCarrierBitsX10,
					ClassFlags: FlagHasConn | FlagIsHarmonicCompanion,
					Role:       RoleClosedBeacon,
				})
			}
		}
	}
	return atoms
}

const (
	ISODateCarrierBitsX10   = isoDateCarrierBitsX10
	CalBeaconCarrierBitsX10 = calBeaconCarrierBitsX10
)
