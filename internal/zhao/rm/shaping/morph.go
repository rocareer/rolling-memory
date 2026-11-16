// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package shaping 提供零先验纯字符几何规整（撇号/全角归一、拉长音坍缩、周期叠音坍缩、
// 拉丁变音符折叠、前导字母-尾部数字切分、结构化日期提取）与句内误插空格滑动粘合原语。
package shaping

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

func isVariationSelector(r rune) bool {
	return (r >= 0xFE00 && r <= 0xFE0F) || (r >= 0xE0100 && r <= 0xE01EF)
}

// CanonicalizeApostrophe normalizes curly apostrophes, backticks, and fullwidth
// ASCII alphanumerics/connectors into canonical halfwidth ASCII and strips
// invisible format characters (soft hyphen U+00AD, ZWSP, BOM, ZWNJ/ZWJ) and
// glyph variation selectors (U+FE00..U+FE0F, U+E0100..U+E01EF).
func CanonicalizeApostrophe(s string) string {
	needNorm := false
	for i := 0; i < len(s); i++ {
		if s[i] == '`' || s[i] >= 0x80 {
			needNorm = true
			break
		}
	}
	if !needNorm {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '’' || r == '‘' || r == '`' || r == '＇':
			b.WriteByte('\'')
		case r == '\u2010' || r == '\u2011':
			b.WriteByte('-')
		case (r >= '０' && r <= '９') || (r >= 'Ａ' && r <= 'Ｚ') || (r >= 'ａ' && r <= 'ｚ') ||
			r == '－' || r == '＿' || r == '＃' || r == '．' || r == '＄' || r == '％' || r == '＋' || r == '／':
			b.WriteRune(r - 0xFEE0)
		case r == '\u00ad' || unicode.Is(unicode.Cf, r) || isVariationSelector(r):
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// CollapseElongatedRuns 纯字符几何游程规整（零 BPE 词表查询、零先验）：
// 当纯字母词（不含数字且至少含 2 种不同字母）内存在连续 >=3 个相同字母的口语拉长音
// （如 "coooool", "sooooo"）时，确定性输出压缩为 2 个相同字母与 1 个字母的几何归并形。
func CollapseElongatedRuns(s string) (run2, run1 string) {
	hasRun3 := false
	runLen := 0
	distinct := 0
	var firstR, prev rune
	for _, r := range s {
		if !unicode.IsLetter(r) {
			return "", ""
		}
		if firstR == 0 {
			firstR = r
			distinct = 1
		} else if r != firstR && distinct == 1 {
			distinct = 2
		}
		if r == prev {
			runLen++
			if runLen >= 3 {
				hasRun3 = true
			}
		} else {
			prev, runLen = r, 1
		}
	}
	if !hasRun3 || distinct < 2 {
		return "", ""
	}
	var b2, b1 strings.Builder
	prev, runLen = 0, 0
	for _, r := range s {
		if r == prev {
			runLen++
		} else {
			prev, runLen = r, 1
		}
		if runLen <= 2 {
			b2.WriteRune(r)
		}
		if runLen <= 1 {
			b1.WriteRune(r)
		}
	}
	return b2.String(), b1.String()
}

// SplitAlphaLeadingNumericTail 纯字符几何「字母词干 + 尾部数字编号」切分（零词表、零语种特调）：
// 当词元由 >= 3 个前导字母与尾部数字直接粘连构成（如 "day2" -> "day", "part1" -> "part", "room101" -> "room"）时，
// 确定性提取前导字母词干作为伴随标签（绝不吐出裸数字段）。
func SplitAlphaLeadingNumericTail(s string) string {
	letBytes := 0
	letRunes := 0
	inDigits := false
	for _, r := range s {
		if unicode.IsLetter(r) {
			if inDigits {
				return ""
			}
			letBytes += utf8.RuneLen(r)
			letRunes++
		} else if unicode.IsNumber(r) {
			if letRunes == 0 {
				return ""
			}
			inDigits = true
		} else {
			return ""
		}
	}
	if !inDigits || letRunes < 3 {
		return ""
	}
	return s[:letBytes]
}

// CollapsePeriodicReduplication 纯字符周期几何规整（零 BPE 词表查询、零先验）：
// 当词为最小基本单元 u（长度 >= 2）的 >= 3 次周期重复（如 "hahahaha" = "ha"*4）时，
// 确定性坍缩为双周期基态 u+u（"haha"）。
func CollapsePeriodicReduplication(s string) string {
	runes := []rune(s)
	n := len(runes)
	if n < 6 {
		return ""
	}
	for p := 2; p*3 <= n; p++ {
		if n%p != 0 {
			continue
		}
		match := true
		for i := p; i < n; i++ {
			if runes[i] != runes[i%p] {
				match = false
				break
			}
		}
		if match {
			return string(runes[:2*p])
		}
	}
	return ""
}

func foldExtendedLatinRune(r rune) (byte, bool) {
	switch r {
	case 'à', 'á', 'â', 'ã', 'ä', 'å', 'ā', 'ă', 'ą', 'ǎ', 'ǻ', 'ȁ', 'ȃ', 'ȧ',
		'ạ', 'ả', 'ấ', 'ầ', 'ẩ', 'ẫ', 'ậ', 'ắ', 'ằ', 'ẳ', 'ẵ', 'ặ':
		return 'a', true
	case 'ç', 'ć', 'č', 'ĉ', 'ċ':
		return 'c', true
	case 'ď', 'đ', 'ð', 'ḍ', 'ḏ':
		return 'd', true
	case 'è', 'é', 'ê', 'ë', 'ē', 'ĕ', 'ė', 'ę', 'ě', 'ȅ', 'ȇ', 'ȩ',
		'ẹ', 'ẻ', 'ẽ', 'ế', 'ề', 'ể', 'ễ', 'ệ':
		return 'e', true
	case 'ĝ', 'ğ', 'ġ', 'ģ', 'ǧ', 'ǵ':
		return 'g', true
	case 'ĥ', 'ħ', 'ȟ', 'ḥ', 'ḧ', 'ḩ', 'ḫ':
		return 'h', true
	case 'ì', 'í', 'î', 'ï', 'ĩ', 'ī', 'ĭ', 'į', 'ı', 'ǐ', 'ȉ', 'ȋ', 'ỉ', 'ị':
		return 'i', true
	case 'ĵ', 'ǰ':
		return 'j', true
	case 'ķ', 'ǩ', 'ḳ', 'ḵ':
		return 'k', true
	case 'ĺ', 'ļ', 'ľ', 'ŀ', 'ł', 'ḷ', 'ḹ', 'ḻ':
		return 'l', true
	case 'ñ', 'ń', 'ņ', 'ň', 'ǹ', 'ṅ', 'ṇ', 'ṉ':
		return 'n', true
	case 'ò', 'ó', 'ô', 'õ', 'ö', 'ø', 'ō', 'ŏ', 'ő', 'ơ', 'ǒ', 'ǫ', 'ǭ', 'ǿ', 'ȍ', 'ȏ', 'ȫ', 'ȭ', 'ȯ', 'ȱ',
		'ọ', 'ỏ', 'ố', 'ồ', 'ổ', 'ỗ', 'ộ', 'ớ', 'ờ', 'ở', 'ỡ', 'ợ':
		return 'o', true
	case 'ŕ', 'ŗ', 'ř', 'ȑ', 'ȓ', 'ṛ', 'ṝ', 'ṟ':
		return 'r', true
	case 'ś', 'ŝ', 'ş', 'š', 'ș', 'ṣ', 'ṩ':
		return 's', true
	case 'ţ', 'ť', 'ŧ', 'ț', 'ṭ', 'ṯ', 'ṱ':
		return 't', true
	case 'ù', 'ú', 'û', 'ü', 'ũ', 'ū', 'ŭ', 'ů', 'ű', 'ų', 'ư', 'ǔ', 'ǖ', 'ǘ', 'ǚ', 'ǜ', 'ȕ', 'ȗ',
		'ụ', 'ủ', 'ứ', 'ừ', 'ử', 'ữ', 'ự':
		return 'u', true
	case 'ŵ', 'ẁ', 'ẃ', 'ẅ':
		return 'w', true
	case 'ý', 'ÿ', 'ŷ', 'ȳ', 'ỳ', 'ỵ', 'ỷ', 'ỹ':
		return 'y', true
	case 'ź', 'ż', 'ž', 'ẓ', '\u1e95':
		return 'z', true
	}
	return 0, false
}

// FoldLatinDiacritics 执行纯确定性 Unicode 拉丁变音符几何折叠（零词表、零先验）：
// 将带重音/变音符的拉丁字符（涵盖 Latin-1、Latin Extended-A/B、Latin Extended Additional 越南语/声调预组合字符及 NFD 组合符）映射为无变音基态字母。
func FoldLatinDiacritics(s string) string {
	hasDiacritic := false
	for _, r := range s {
		if (r >= 0x00c0 && r <= 0x024f) || (r >= 0x0300 && r <= 0x036f) || (r >= 0x1e00 && r <= 0x1eff) {
			hasDiacritic = true
			break
		}
	}
	if !hasDiacritic {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	changed := false
	for _, r := range s {
		if r >= 0x0300 && r <= 0x036f {
			changed = true
			continue
		}
		if r == 'æ' {
			b.WriteString("ae")
			changed = true
			continue
		}
		if r == 'œ' {
			b.WriteString("oe")
			changed = true
			continue
		}
		if r == 'ß' {
			b.WriteString("ss")
			changed = true
			continue
		}
		if folded, ok := foldExtendedLatinRune(unicode.ToLower(r)); ok {
			b.WriteByte(folded)
			changed = true
			continue
		}
		b.WriteRune(r)
	}
	if !changed {
		return ""
	}
	return b.String()
}

func isValidCalendarDate(y, m, d int) bool {
	if y < 1900 || y > 2199 || m < 1 || m > 12 || d < 1 || d > 31 {
		return false
	}
	switch m {
	case 4, 6, 9, 11:
		return d <= 30
	case 2:
		leap := (y%4 == 0 && y%100 != 0) || y%400 == 0
		if leap {
			return d <= 29
		}
		return d <= 28
	default:
		return true
	}
}

func isDateSepRune(r rune) bool {
	return r == '-' || r == '/' || r == '.'
}

// ExtractStructuredDateHarmonics extracts normalized ISO-8601 calendar dates ("YYYY-MM-DD") from sent.
func ExtractStructuredDateHarmonics(sent string) []string {
	if len(sent) < 8 {
		return nil
	}
	digitCount := 0
	for i := 0; i < len(sent); i++ {
		if sent[i] >= '0' && sent[i] <= '9' {
			digitCount++
		}
	}
	if digitCount < 6 {
		return nil
	}
	type numSpan struct {
		val       int
		digits    int
		startRune int
		endRune   int
	}
	runes := []rune(sent)
	n := len(runes)
	if n < 8 {
		return nil
	}
	var spans []numSpan
	for i := 0; i < n; {
		if runes[i] >= '0' && runes[i] <= '9' {
			start := i
			val := 0
			for i < n && runes[i] >= '0' && runes[i] <= '9' {
				if i-start < 8 {
					val = val*10 + int(runes[i]-'0')
				}
				i++
			}
			spans = append(spans, numSpan{val: val, digits: i - start, startRune: start, endRune: i})
		} else {
			i++
		}
	}
	if len(spans) < 3 {
		return nil
	}
	var out []string
	for i := 0; i+2 < len(spans); i++ {
		s0, s1, s2 := spans[i], spans[i+1], spans[i+2]
		if s1.startRune-s0.endRune != 1 || s2.startRune-s1.endRune != 1 {
			continue
		}
		sep1, sep2 := runes[s0.endRune], runes[s1.endRune]
		if !((isDateSepRune(sep1) && sep1 == sep2) || (sep1 == '年' && sep2 == '月')) {
			continue
		}
		if i > 0 && s0.startRune-spans[i-1].endRune == 1 && runes[spans[i-1].endRune] == sep1 {
			continue
		}
		if i+3 < len(spans) && spans[i+3].startRune-s2.endRune == 1 && runes[s2.endRune] == sep2 {
			continue
		}
		y, m, d := 0, 0, 0
		if s0.digits == 4 && s1.digits >= 1 && s1.digits <= 2 && s2.digits >= 1 && s2.digits <= 2 {
			y, m, d = s0.val, s1.val, s2.val
		} else if s2.digits == 4 && s0.digits >= 1 && s0.digits <= 2 && s1.digits >= 1 && s1.digits <= 2 && sep1 == '/' {
			y = s2.val
			if s0.val >= 1 && s0.val <= 12 && s1.val > 12 {
				m, d = s0.val, s1.val
			} else {
				d, m = s0.val, s1.val
			}
		}
		if !isValidCalendarDate(y, m, d) {
			continue
		}
		iso := fmt.Sprintf("%04d-%02d-%02d", y, m, d)
		dup := false
		for _, prev := range out {
			if prev == iso {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, iso)
		}
		i += 2
	}
	return out
}
