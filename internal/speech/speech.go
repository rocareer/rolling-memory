// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package speech holds the form-only conversation envelope vocabulary shared by
// transcript denoise, corpus partitioning and episodic pulse packing.
//
// Everything here is protocol shape, never meaning:
//   - harness-injected tag blocks (`<system-reminder>`, `<rolling-memory-*-recall>`,
//     `<thinking>`, …) are matched by exact tag name and must be closed;
//   - flattened non-speech envelopes (`[thinking]`, `[tool_use <Name>]`,
//     `[tool_result]`) are matched by a line-anchored marker only;
//   - nothing depends on natural language: zero word lists, zero scripts, zero
//     character-length caps, zero Shannon gating.
//
// The package has no internal dependencies so the thin client can reuse the exact
// same rules as the ingest paths (one vocabulary, one behaviour).
package speech

import (
	"regexp"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// injectedTags are the harness context envelopes stripped from speech. Each one is
// only removed as a complete "<tag …>…</tag>" pair, so a quoted mention survives.
var injectedTags = []string{
	"rolling-memory-recall",
	"rolling-memory-boot",
	"system-reminder",
	"untrusted_gen_ui_state",
	"thinking",
}

var recallTagPrefixRE = regexp.MustCompile(`(?i)^[a-z0-9][a-z0-9-]*-recall`)

// EnvelopeSpec is one flattened non-speech protocol block class: a name plus the
// RE2 pattern that anchors its start. Patterns are line-anchored and capture-free.
type EnvelopeSpec struct {
	Name    string
	Pattern string
}

// ProtocolEnvelopeSpecs returns the canonical `[thinking]` / `[tool_use <Name>]` /
// `[tool_result]` block vocabulary (the single source for both evidence
// partitioning and conversation denoise).
func ProtocolEnvelopeSpecs() []EnvelopeSpec {
	return []EnvelopeSpec{
		{Name: "thinking", Pattern: `(?m)^[ \t]*\[thinking\]`},
		{Name: "tool_use", Pattern: `(?m)^[ \t]*\[tool_use[ \t]`},
		{Name: "tool_result", Pattern: `(?m)^[ \t]*\[tool_result\]`},
	}
}

// speakerEnvelopeTags are the speaker envelopes the episodic renderer writes
// (`renderEpisodicTurn` / reconcile assembly). They are protocol markers too: a
// non-speech block never extends past the next speaker turn.
var speakerEnvelopeTags = []string{"[user]", "[assistant]", "[message]"}

var (
	envelopeOnce sync.Once
	envelopeREs  []*regexp.Regexp
)

func envelopeRegexps() []*regexp.Regexp {
	envelopeOnce.Do(func() {
		for _, spec := range ProtocolEnvelopeSpecs() {
			re, err := regexp.Compile(spec.Pattern)
			if err != nil || re.NumSubexp() != 0 {
				continue
			}
			envelopeREs = append(envelopeREs, re)
		}
	})
	return envelopeREs
}

// StripConversationNoise removes harness-injected context blocks and non-speech
// protocol envelopes from one conversation turn, returning what the speaker said.
//
// Envelope bodies end at the next protocol envelope, the next speaker envelope or
// a blank line — never at end-of-text blindly, because dropping a following
// paragraph would delete real speech (fidelity first). The surviving text is kept
// verbatim: no rewriting, no truncation, no language assumptions.
func StripConversationNoise(text string) string {
	cleaned := text
	for _, tag := range injectedTags {
		cleaned = stripNamedTagBlocks(cleaned, tag)
	}
	cleaned = stripRecallTagBlocks(cleaned)
	cleaned = stripToolLines(cleaned)
	cleaned = StripProtocolEnvelopes(cleaned)
	return TrimSpace(cleaned)
}

// StripProtocolEnvelopes removes the flattened `[thinking]` / `[tool_use …]` /
// `[tool_result]` blocks from a conversation turn and returns the remaining speech.
// Bodies end at the next protocol envelope, the next speaker envelope or a blank
// line; every other line is preserved byte-for-byte.
func StripProtocolEnvelopes(body string) string {
	if body == "" || (!strings.Contains(body, "[thinking]") && !strings.Contains(body, "[tool_use") && !strings.Contains(body, "[tool_result]")) {
		return body
	}
	regexps := envelopeRegexps()
	if len(regexps) == 0 {
		return body
	}
	lines := strings.Split(body, "\n")
	out := make([]string, 0, len(lines))
	skipping := false
	for _, line := range lines {
		if matchesEnvelope(regexps, line) {
			skipping = true
			continue
		}
		if skipping {
			// Blank line = paragraph boundary: the envelope ends here (the blank line
			// itself belongs to the envelope separator and is dropped with it).
			if TrimSpace(line) == "" {
				skipping = false
				continue
			}
			// Speaker envelope = next turn: end the envelope and keep the line.
			if !isSpeakerEnvelopeLine(line) {
				continue
			}
			skipping = false
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func matchesEnvelope(regexps []*regexp.Regexp, line string) bool {
	for _, re := range regexps {
		if re.MatchString(line) {
			return true
		}
	}
	return false
}

func isSpeakerEnvelopeLine(line string) bool {
	trimmed := strings.TrimLeft(line, " \t")
	for _, tag := range speakerEnvelopeTags {
		if !strings.HasPrefix(trimmed, tag) {
			continue
		}
		rest := trimmed[len(tag):]
		if rest == "" || rest[0] == ' ' || rest[0] == '\t' {
			return true
		}
	}
	return false
}

func stripNamedTagBlocks(text, name string) string {
	return stripTagBlocks(text, func(value string, position int) (string, int, bool) {
		prefix := "<" + name
		if len(value)-position < len(prefix) {
			return "", 0, false
		}
		if !strings.EqualFold(value[position:position+len(prefix)], prefix) {
			return "", 0, false
		}
		end := position + len(prefix)
		if end < len(value) {
			next, _ := utf8.DecodeRuneInString(value[end:])
			if IsPyWord(next) {
				return "", 0, false
			}
		}
		return name, end, true
	})
}

func stripRecallTagBlocks(text string) string {
	if !strings.Contains(text, "<") {
		return text
	}
	return stripTagBlocks(text, func(value string, position int) (string, int, bool) {
		if position >= len(value) || value[position] != '<' {
			return "", 0, false
		}
		rest := value[position+1:]
		if len(rest) < 8 {
			return "", 0, false
		}
		c0 := rest[0]
		if !((c0 >= 'a' && c0 <= 'z') || (c0 >= 'A' && c0 <= 'Z') || (c0 >= '0' && c0 <= '9')) {
			return "", 0, false
		}
		i := 1
		for i < len(rest) {
			c := rest[i]
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' {
				i++
			} else {
				break
			}
		}
		matchedLen := 0
		for j := 1; j+7 <= i; j++ {
			if strings.EqualFold(rest[j:j+7], "-recall") {
				matchedLen = j + 7
				break
			}
		}
		if matchedLen == 0 {
			return "", 0, false
		}
		name := rest[:matchedLen]
		end := position + 1 + matchedLen
		if end < len(value) {
			next, _ := utf8.DecodeRuneInString(value[end:])
			if IsPyWord(next) {
				return "", 0, false
			}
		}
		return name, end, true
	})
}

// stripTagBlocks removes matched <name ...>...</name> blocks, where locate
// returns the captured tag name and the position right after it.
func stripTagBlocks(text string, locate func(string, int) (string, int, bool)) string {
	firstLt := strings.IndexByte(text, '<')
	if firstLt < 0 {
		return text
	}
	var out strings.Builder
	last := 0
	position := firstLt
	for position < len(text) {
		if text[position] != '<' {
			rel := strings.IndexByte(text[position:], '<')
			if rel < 0 {
				break
			}
			position += rel
		}
		name, afterName, ok := locate(text, position)
		if !ok {
			position++
			continue
		}
		openEnd := strings.IndexByte(text[afterName:], '>')
		if openEnd < 0 {
			position++
			continue
		}
		openEnd += afterName + 1
		closeTag := "</" + name + ">"
		closeIndex := indexFold(text, closeTag, openEnd)
		if closeIndex < 0 {
			position++
			continue
		}
		closeEnd := closeIndex + len(closeTag)
		if position > 0 && closeEnd < len(text) {
			quote := text[position-1]
			if (quote == '`' || quote == '"' || quote == '\'') && text[closeEnd] == quote {
				position = closeEnd
				continue
			}
		}
		if last == 0 && out.Len() == 0 {
			out.Grow(len(text))
		}
		out.WriteString(text[last:position])
		out.WriteByte(' ')
		position = closeEnd
		last = position
	}
	if last == 0 {
		return text
	}
	out.WriteString(text[last:])
	return out.String()
}

func indexFold(haystack, needle string, start int) int {
	if start > len(haystack) {
		return -1
	}
	lowerNeedle := strings.ToLower(needle)
	if idx := strings.Index(haystack[start:], lowerNeedle); idx >= 0 {
		return idx + start
	}
	index := strings.Index(strings.ToLower(haystack[start:]), lowerNeedle)
	if index < 0 {
		return -1
	}
	return index + start
}

func stripToolLines(text string) string {
	if !strings.Contains(text, "[tool]") {
		return text
	}
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		if strings.HasPrefix(strings.TrimLeftFunc(line, IsPySpace), "[tool]") {
			lines[index] = " "
		}
	}
	return strings.Join(lines, "\n")
}

// IsPySpace mirrors Python's str.isspace() set (used for byte-stable trimming).
func IsPySpace(char rune) bool {
	switch char {
	case '\t', '\n', '\v', '\f', '\r', ' ', '\u0085', '\u00a0', '\u1680',
		'\u2028', '\u2029', '\u202f', '\u205f', '\u3000':
		return true
	}
	if char >= '\u001c' && char <= '\u001f' {
		return true
	}
	if char >= '\u2000' && char <= '\u200a' {
		return true
	}
	return false
}

// IsPyWord reports whether the rune continues a Python-style identifier word.
func IsPyWord(char rune) bool {
	return char == '_' || unicode.IsLetter(char) || unicode.IsNumber(char)
}

// TrimSpace trims the Python whitespace set from both ends.
func TrimSpace(value string) string {
	return strings.TrimFunc(value, IsPySpace)
}
