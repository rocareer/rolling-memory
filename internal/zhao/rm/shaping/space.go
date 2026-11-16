// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package shaping

import (
	"strings"
	"unicode"
)

// CanonicalPhraseJoint is the language-agnostic canonical single-space phrase delimiter.
const CanonicalPhraseJoint = " "

// CanonicalPhrase joins term faces with the canonical single-space joint.
func CanonicalPhrase(parts ...string) string {
	if len(parts) == 0 {
		return ""
	}
	var joined strings.Builder
	for _, part := range parts {
		for _, face := range strings.Fields(part) {
			if joined.Len() > 0 {
				joined.WriteString(CanonicalPhraseJoint)
			}
			joined.WriteString(face)
		}
	}
	return joined.String()
}

// SpaceElisionScript classifies the script family of a contiguous non-space block.
type SpaceElisionScript uint8

const (
	SpaceElisionScriptNone SpaceElisionScript = iota
	SpaceElisionScriptIdeographic
	SpaceElisionScriptHangul
	SpaceElisionScriptLetter
	SpaceElisionScriptDigit
)

// IsHanRune reports whether r is a CJK Unified Ideograph (including Extensions A..I and Compatibility Ideographs).
func IsHanRune(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0x3400 && r <= 0x4DBF) || (r >= 0xF900 && r <= 0xFAFF) || (r >= 0x20000 && r <= 0x323AF) || unicode.Is(unicode.Han, r)
}

// IsHangulRune reports whether r belongs to the Korean Hangul script.
func IsHangulRune(r rune) bool {
	return (r >= 0xAC00 && r <= 0xD7AF) || (r >= 0x1100 && r <= 0x11FF) || (r >= 0x3130 && r <= 0x318F)
}

// IsKanaOrSEAsianRune reports whether r belongs to Japanese Kana or unspaced Southeast Asian scripts.
func IsKanaOrSEAsianRune(r rune) bool {
	return r == 0x30FC || (r >= 0x3040 && r <= 0x30FF) || (r >= 0xFF66 && r <= 0xFF9F) || (r >= 0x0E00 && r <= 0x0EFF) || (r >= 0x1780 && r <= 0x17FF)
}

// IsUnspacedIdeographic reports whether r belongs to a script without word spaces (Han, Kana, or SE Asian).
func IsUnspacedIdeographic(r rune) bool {
	return IsHanRune(r) || IsKanaOrSEAsianRune(r)
}

func spaceElisionRuneScript(r rune) SpaceElisionScript {
	switch {
	case IsUnspacedIdeographic(r):
		return SpaceElisionScriptIdeographic
	case IsHangulRune(r):
		return SpaceElisionScriptHangul
	case unicode.IsNumber(r):
		return SpaceElisionScriptDigit
	case unicode.IsLetter(r) || unicode.IsMark(r):
		return SpaceElisionScriptLetter
	default:
		return SpaceElisionScriptNone
	}
}

// SpaceElisionBlockScript returns the dominant script of block.
func SpaceElisionBlockScript(block string) SpaceElisionScript {
	for _, r := range block {
		if script := spaceElisionRuneScript(r); script != SpaceElisionScriptNone {
			return script
		}
	}
	return SpaceElisionScriptNone
}

// SpaceElisionBlocks splits sentence into contiguous non-space blocks and records whether each block followed whitespace.
func SpaceElisionBlocks(sentence string) (blocks []string, separated []bool) {
	var cur strings.Builder
	pendingSpace := false
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		blocks = append(blocks, cur.String())
		separated = append(separated, pendingSpace)
		cur.Reset()
		pendingSpace = false
	}
	for _, r := range sentence {
		switch {
		case unicode.IsSpace(r):
			flush()
			pendingSpace = true
		case IsUnspacedIdeographic(r):
			flush()
			blocks = append(blocks, string(r))
			separated = append(separated, pendingSpace)
			pendingSpace = false
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return blocks, separated
}

// SpaceElision is the result of one O(K) single-pass space-elision scan.
type SpaceElision struct {
	Blocks    []string
	Separated []bool
	Spaced    []string
	Tokens    []string
	Phrases   []string
}

// FuseSpaceElision repairs space faults of one sentence with a single pass and zero backtracking.
func FuseSpaceElision(sentence string) SpaceElision {
	blocks, separated := SpaceElisionBlocks(sentence)
	plan := SpaceElision{Blocks: blocks, Separated: separated}
	type acceptedGlue struct {
		boundary int
		token    int
	}
	var accepted []acceptedGlue
	for boundary := 0; boundary+1 < len(blocks); boundary++ {
		if !separated[boundary+1] {
			continue
		}
		left, right := blocks[boundary], blocks[boundary+1]
		script := SpaceElisionBlockScript(left)
		if script == SpaceElisionScriptNone || script != SpaceElisionBlockScript(right) {
			continue
		}
		plan.Spaced = append(plan.Spaced, CanonicalPhrase(left, right))
		plan.Tokens = append(plan.Tokens, left+right)
		token := len(plan.Tokens) - 1
		if last := len(accepted) - 1; last < 0 || boundary >= accepted[last].boundary+2 {
			if last >= 0 {
				plan.Phrases = append(plan.Phrases, CanonicalPhrase(plan.Tokens[accepted[last].token], plan.Tokens[token]))
			}
			accepted = append(accepted, acceptedGlue{boundary: boundary, token: token})
		}
	}
	return plan
}
