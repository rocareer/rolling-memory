// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package codec

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// BlindFormFlagMarks computes the declared form-flag bitmasks aligned 1:1 with words.
func BlindFormFlagMarks(line string, words []string) []uint8 {
	marks, _, _ := blindFormFlagMarksAndMeta(line, words)
	return marks
}

func markBlindInlineQuotedWords(line string, words []string, marks []uint8) {
	if line == "" || len(words) == 0 || !strings.ContainsAny(line, "\"“《「『") {
		return
	}
	runes := []rune(line)
	searchFrom := 0
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		var closeR rune
		switch r {
		case '"':
			closeR = '"'
		case '“':
			closeR = '”'
		case '《':
			closeR = '》'
		case '「':
			closeR = '」'
		case '『':
			closeR = '』'
		default:
			continue
		}
		if i > 0 && isLetterOrNumber(runes[i-1]) {
			continue
		}
		end := -1
		for j := i + 1; j < len(runes); j++ {
			switch runes[j] {
			case '\n', '\r', ',', ';', ':', '!', '?', '，', '；', '：', '！', '？', '。':
				j = len(runes)
				continue
			}
			if runes[j] == closeR {
				if j+1 < len(runes) && isLetterOrNumber(runes[j+1]) {
					continue
				}
				end = j
				break
			}
		}
		if end > i+1 {
			spanWords := SplitWords(string(runes[i+1 : end]))
			if m := len(spanWords); m > 0 {
				startIdx := len(SplitWords(string(runes[:i])))
				if startIdx < searchFrom {
					startIdx = searchFrom
				}
				matched := false
				for _, from := range [2]int{startIdx, searchFrom} {
					for k := from; k+m <= len(words); k++ {
						ok := true
						for t := 0; t < m; t++ {
							if words[k+t] != spanWords[t] {
								ok = false
								break
							}
						}
						if ok {
							for t := 0; t < m; t++ {
								marks[k+t] |= FlagHasBoundaryQuote
							}
							searchFrom = k + m
							matched = true
							break
						}
					}
					if matched {
						break
					}
				}
			}
			i = end
		}
	}
}

func blindFormFlagMarksAndMeta(line string, words []string) ([]uint8, []int, int) {
	if len(words) == 0 {
		return nil, nil, -1
	}
	marks := make([]uint8, len(words))
	if r, ok := firstNonSpaceRune(line); ok && isStrongDelimiterRune(r) {
		marks[0] |= FlagHasBoundaryQuote
	}
	markBlindInlineQuotedWords(line, words, marks)
	bodyIdx := -1
	if isBlindFocalHeaderLine(line) {
		marks[0] |= FlagIsFocalHeader
		bodyIdx = leadingFieldBodyWordIdx(line, words)
		if bodyIdx > 0 && bodyIdx < len(marks) {
			marks[bodyIdx] |= FlagIsFocalHeader
		}
	} else {
		bodyIdx = leadingFieldBodyWordIdx(line, words)
	}
	parents := blindCompoundParentIdx(words)
	var carrierStarts []int
	inited := false
	for i := 1; i < len(words); i++ {
		if !blindNumericAnchorAt(words, i-1) {
			continue
		}
		if !inited {
			carrierStarts = blindAdjacentCarrierStarts(line)
			inited = true
		}
		if !blindUnitCarrierAt(words, parents, i-1, i) || !blindCarrierTextuallyAdjacent(line, carrierStarts, words[i]) {
			continue
		}
		marks[i] |= FlagIsQuantityOrUnit
	}
	return marks, parents, bodyIdx
}

func blindAdjacentCarrierStarts(line string) []int {
	if line == "" {
		return nil
	}
	runes := make([]rune, 0, len(line))
	offsets := make([]int, 0, len(line))
	for i, r := range line {
		runes = append(runes, r)
		offsets = append(offsets, i)
	}
	var starts []int
	for i := 0; i < len(runes); {
		if !unicode.IsNumber(runes[i]) && !isWordConn(runes[i]) {
			i++
			continue
		}
		hasDigit := false
		j := i
		for j < len(runes) {
			if unicode.IsNumber(runes[j]) {
				hasDigit = true
				j++
				continue
			}
			if isWordConn(runes[j]) {
				j++
				continue
			}
			break
		}
		if hasDigit {
			k := j
			for k < len(runes) && unicode.IsSpace(runes[k]) {
				k++
			}
			if k < len(runes) && (unicode.IsLetter(runes[k]) || unicode.IsMark(runes[k])) {
				starts = append(starts, offsets[k])
			}
		}
		i = j
	}
	return starts
}

func blindCarrierTextuallyAdjacent(line string, starts []int, carrier string) bool {
	if carrier == "" {
		return false
	}
	for _, s := range starts {
		if s <= len(line) && strings.HasPrefix(line[s:], carrier) {
			return true
		}
	}
	return false
}

func blindCompoundParentIdx(words []string) []int {
	var parents []int
	for i := 0; i < len(words); i++ {
		if parents != nil && parents[i] >= 0 {
			continue
		}
		subs := splitCompoundSubwords(words[i])
		if len(subs) < 2 {
			continue
		}
		if parents == nil {
			parents = make([]int, len(words))
			for j := range parents {
				parents[j] = -1
			}
		}
		for k := 1; k <= len(subs) && i+k < len(words); k++ {
			if words[i+k] != subs[k-1] {
				break
			}
			parents[i+k] = i
		}
	}
	return parents
}

func blindNumericAnchorAt(words []string, i int) bool {
	return i >= 0 && i < len(words) && isBlindNumericOnlyToken(words[i])
}

func blindUnitCarrierAt(words []string, parents []int, anchor, i int) bool {
	if !isBlindLetterOnlyToken(words[i]) {
		return false
	}
	if len(parents) == 0 {
		return true
	}
	carrierParent := parents[i]
	if carrierParent >= 0 {
		return carrierParent == parents[anchor] && isBlindDigitLedAlnumToken(words[carrierParent])
	}
	if anchorParent := parents[anchor]; anchorParent >= 0 {
		return isBlindNumericOnlyToken(words[anchorParent])
	}
	return true
}

func isBlindFocalHeaderLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	if isATXHeadingPrefix(trimmed) {
		return true
	}
	return hasLeadingFieldLabelPrefix(trimmed)
}

func isATXHeadingPrefix(trimmed string) bool {
	n := 0
	for _, r := range trimmed {
		if r != '#' {
			break
		}
		n++
		if n > 6 {
			return false
		}
	}
	if n == 0 {
		return false
	}
	rest := trimmed[n:]
	return rest == "" || !strings.HasPrefix(rest, "#")
}

func hasLeadingFieldLabelPrefix(trimmed string) bool {
	colon, colonLen := -1, 0
	for idx, r := range trimmed {
		if isFieldLabelColon(r) {
			colon, colonLen = idx, utf8.RuneLen(r)
			break
		}
	}
	if colon <= 0 {
		return false
	}
	for _, r := range trimmed[:colon] {
		if unicode.IsSpace(r) || isBlindSentenceTerminator(r) {
			return false
		}
	}
	return isBlindNumericLedValue(trimmed[colon+colonLen:])
}

func isBlindNumericLedValue(rest string) bool {
	rest = strings.TrimLeftFunc(rest, unicode.IsSpace)
	if rest == "" {
		return false
	}
	hasDigit := false
	for _, r := range rest {
		switch {
		case unicode.IsNumber(r):
			hasDigit = true
		case isWordConn(r):
		default:
			return hasDigit
		}
	}
	return hasDigit
}

func isFieldLabelColon(r rune) bool { return r == ':' || r == '：' }

func isBlindSentenceTerminator(r rune) bool {
	switch r {
	case '\n', '\r', '.', '。', '！', '？', '；', ';', '!', '?':
		return true
	}
	return false
}

func isStrongDelimiterRune(r rune) bool {
	return unicode.Is(unicode.Ps, r) || unicode.Is(unicode.Pe, r) ||
		unicode.Is(unicode.Pi, r) || unicode.Is(unicode.Pf, r)
}

func isBlindNumericOnlyToken(tok string) bool {
	hasDigit := false
	for _, r := range tok {
		switch {
		case unicode.IsNumber(r):
			hasDigit = true
		case isWordConn(r):
		default:
			return false
		}
	}
	return hasDigit
}

func isBlindLetterOnlyToken(tok string) bool {
	hasLetter := false
	for _, r := range tok {
		switch {
		case unicode.IsNumber(r):
			return false
		case unicode.IsLetter(r) || unicode.IsMark(r):
			hasLetter = true
		}
	}
	return hasLetter
}

func isBlindDigitLedAlnumToken(tok string) bool {
	for _, r := range tok {
		if !unicode.IsNumber(r) {
			return false
		}
		return !isBlindNumericOnlyToken(tok)
	}
	return false
}

func firstNonSpaceRune(line string) (rune, bool) {
	for _, r := range line {
		if !unicode.IsSpace(r) {
			return r, true
		}
	}
	return 0, false
}
