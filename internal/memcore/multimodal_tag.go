// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"math"
	"strings"
	"unicode"
)

// Multimodal part line-start protocol markers synthesized by ComposeTurnText or supplied fixtures.
var multimodalLinePrefixes = []string{
	"【图片】",
	"【音频】",
	"【视频】",
	"【文件】",
	"[image:",
	"[audio:",
	"[video:",
	"[file:",
}

var multimodalInlineBridges = []string{
	" + [image:",
	" + [audio:",
	" + [video:",
	" + [file:",
}

func hasFormalBracketedMediaTagPrefix(line string) bool {
	if !strings.HasPrefix(line, "[") || strings.HasPrefix(line, "[tool_") {
		return false
	}
	colon := strings.IndexByte(line, ':')
	closeBr := strings.IndexByte(line, ']')
	if colon < 4 || colon > 13 || closeBr <= colon {
		return false
	}
	for i := 1; i < colon; i++ {
		c := line[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) {
			return false
		}
	}
	return true
}

// LocateFirstMultimodalPartLine returns the byte offset of the first structural multimodal part segment
// in turnText, or -1 if none exists.
//
// Safety & Reliability Invariant (Anti-In-Band-Injection):
//   - Unlike naive `strings.Index(turnText, "【图片】")`, which falsely triggers when a user writes
//     literal words like "请把【图片】和【文件】发给我" mid-sentence, this function strictly requires
//     the marker to appear at a structural line boundary (`offset == 0` or preceded by `\n`) or as an
//     explicit fixture bridge (` + [image:`).
func LocateFirstMultimodalPartLine(turnText string) int {
	if turnText == "" {
		return -1
	}
	best := -1
	for _, pfx := range multimodalLinePrefixes {
		if strings.HasPrefix(turnText, pfx) {
			return 0
		}
		needle := "\n" + pfx
		if idx := strings.Index(turnText, needle); idx >= 0 {
			pos := idx + 1
			if best < 0 || pos < best {
				best = pos
			}
		}
	}
	if hasFormalBracketedMediaTagPrefix(turnText) {
		return 0
	}
	for off := 0; off < len(turnText); {
		nl := strings.IndexByte(turnText[off:], '\n')
		if nl < 0 {
			break
		}
		pos := off + nl + 1
		if hasFormalBracketedMediaTagPrefix(turnText[pos:]) {
			if best < 0 || pos < best {
				best = pos
			}
			break
		}
		off = pos
	}
	for _, br := range multimodalInlineBridges {
		if idx := strings.Index(turnText, br); idx >= 0 {
			pos := idx + 1
			if best < 0 || pos < best {
				best = pos
			}
		}
	}
	return best
}

// HasStructuralMultimodalPart reports whether turnText contains at least one structural multimodal part line.
func HasStructuralMultimodalPart(turnText string) bool {
	return LocateFirstMultimodalPartLine(turnText) >= 0
}

// StripMultimodalProtocolTags removes structural multimodal protocol prefixes ("【图片】", "【音频】", "【视频】", "【文件】")
// before natural-language script detection (such as batchPrefersCJK and scriptGateReject) so that Chinese characters
// inside the system-injected protocol tag never pollute pure-ASCII/English/Japanese turns.
func StripMultimodalProtocolTags(text string) string {
	if text == "" {
		return ""
	}
	s := text
	for _, tag := range []string{"\n【图片】", "\n【音频】", "\n【视频】", "\n【文件】"} {
		s = strings.ReplaceAll(s, tag, "\n")
	}
	for _, tag := range []string{"【图片】", "【音频】", "【视频】", "【文件】"} {
		if strings.HasPrefix(s, tag) {
			s = strings.TrimPrefix(s, tag)
		}
	}
	return s
}

// CanonicalMultimodalProtocolTag reports whether slotLabel is a bare system-synthesized multimodal
// protocol bracket tag ("【图片】", "【音频】", "【视频】", "【文件】") and returns the canonical protocol tag.
// Natural-language nouns (such as "photo", "screenshot", "document") are never matched here.
func CanonicalMultimodalProtocolTag(slotLabel string) string {
	switch strings.TrimSpace(slotLabel) {
	case "【图片】", "【音频】", "【视频】", "【文件】":
		return strings.TrimSpace(slotLabel)
	default:
		return ""
	}
}

// StripModalBracketSlotPrefix strips system-synthesized multimodal bracket tags from an entity slot label
// so that entity nodes in the memory graph carry pure referent names rather than text-tag prefixes:
//   - "【图片】风景油画" -> "风景油画"
//   - "【文件：Q3_roadmap.pdf】" -> "Q3_roadmap.pdf"
//   - "【音频: shift_09.mp3】" -> "shift_09.mp3"
//
// If stripping would leave an empty string (i.e. bare tag "【图片】"), returns the original string unchanged
// so the caller can first resolve the bare tag against the structural multimodal segment.
func StripModalBracketSlotPrefix(slotLabel string) string {
	s := strings.TrimSpace(slotLabel)
	if s == "" {
		return slotLabel
	}
	for _, tag := range []string{"【图片】", "【音频】", "【视频】", "【文件】"} {
		if strings.HasPrefix(s, tag) {
			rest := strings.TrimSpace(strings.TrimLeft(strings.TrimPrefix(s, tag), "：:—- "))
			if rest != "" {
				return rest
			}
			return slotLabel
		}
	}
	for _, openPfx := range []string{"【图片：", "【图片:", "【音频：", "【音频:", "【视频：", "【视频:", "【文件：", "【文件:"} {
		if strings.HasPrefix(s, openPfx) && strings.HasSuffix(s, "】") {
			inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(s, openPfx), "】"))
			if inner != "" {
				return inner
			}
		}
	}
	lo := strings.ToLower(s)
	for _, asciiPfx := range []string{"[image]", "[audio]", "[video]", "[file]"} {
		if strings.HasPrefix(lo, asciiPfx) {
			rest := strings.TrimSpace(strings.TrimLeft(s[len(asciiPfx):], "：:—- "))
			if rest != "" {
				return rest
			}
		}
	}
	for _, asciiOpen := range []string{"[image:", "[audio:", "[video:", "[file:"} {
		if strings.HasPrefix(lo, asciiOpen) && strings.HasSuffix(s, "]") {
			inner := strings.TrimSpace(strings.TrimSuffix(s[len(asciiOpen):], "]"))
			if inner != "" {
				return inner
			}
		}
	}
	return slotLabel
}

// LocateStructuralPartTagOffsets returns all line-bounded byte offsets of targetTag (e.g., "【图片】") in turnText.
func LocateStructuralPartTagOffsets(turnText, targetTag string) []int {
	if turnText == "" || targetTag == "" {
		return nil
	}
	var offsets []int
	if strings.HasPrefix(turnText, targetTag) {
		offsets = append(offsets, 0)
	}
	needle := "\n" + targetTag
	searchFrom := 0
	for searchFrom < len(turnText) {
		idx := strings.Index(turnText[searchFrom:], needle)
		if idx < 0 {
			break
		}
		pos := searchFrom + idx + 1
		offsets = append(offsets, pos)
		searchFrom = pos + len(targetTag)
	}
	return offsets
}

// ScriptBitMass measures the Shannon self-information bit mass across orthogonal Unicode script families,
// replacing brittle byte-level `isASCII` checks so that typographic punctuation (“ ” ’ — …), mathematical/Greek
// symbols (μ α β), and accented Latin letters (é ü ñ) never falsely disable script conservation gates.
type ScriptBitMass struct {
	LatinBits         float64
	HanBits           float64
	KanaBits          float64
	HangulBits        float64
	CyrillicBits      float64
	OtherNonLatinBits float64
}

// NonLatinScriptBits returns the total Shannon bit mass of non-Latin scripts.
func (m ScriptBitMass) NonLatinScriptBits() float64 {
	return m.HanBits + m.KanaBits + m.HangulBits + m.CyrillicBits + m.OtherNonLatinBits
}

// IsPureLatinOrNeutralScript reports whether the text carries zero Shannon bit mass in non-Latin scripts
// (allowing Latin, Greek, Common punctuation, numbers, and typographic symbols).
func (m ScriptBitMass) IsPureLatinOrNeutralScript() bool {
	return m.NonLatinScriptBits() == 0
}

// AreScriptsOrthogonal reports whether two script bit-mass profiles carry positive Shannon bit mass
// in mutually disjoint Unicode script families (e.g. Latin query vs Han/Kana/Hangul/Cyrillic/Arabic label),
// enabling zero-wordlist cross-lingual vector seed protection without affecting same-script candidates.
func AreScriptsOrthogonal(a, b ScriptBitMass) bool {
	aTotal := a.LatinBits + a.NonLatinScriptBits()
	bTotal := b.LatinBits + b.NonLatinScriptBits()
	if aTotal <= 0 || bTotal <= 0 {
		return false
	}
	overlap := math.Min(a.LatinBits, b.LatinBits) +
		math.Min(a.HanBits, b.HanBits) +
		math.Min(a.KanaBits, b.KanaBits) +
		math.Min(a.HangulBits, b.HangulBits) +
		math.Min(a.CyrillicBits, b.CyrillicBits) +
		math.Min(a.OtherNonLatinBits, b.OtherNonLatinBits)
	return overlap == 0
}

// ComputeScriptBitMass computes the Unicode script Shannon self-information profile of text
// after stripping system-injected multimodal protocol tags.
func ComputeScriptBitMass(text string) ScriptBitMass {
	var m ScriptBitMass
	clean := StripMultimodalProtocolTags(text)
	for _, r := range clean {
		b := RuneShannonBits(r)
		if b <= 0 {
			continue
		}
		switch {
		case unicode.Is(unicode.Latin, r):
			m.LatinBits += b
		case unicode.Is(unicode.Han, r):
			m.HanBits += b
		case unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r):
			m.KanaBits += b
		case unicode.Is(unicode.Hangul, r):
			m.HangulBits += b
		case unicode.Is(unicode.Cyrillic, r):
			m.CyrillicBits += b
		case unicode.IsLetter(r) && !unicode.Is(unicode.Greek, r):
			m.OtherNonLatinBits += b
		}
	}
	return m
}
