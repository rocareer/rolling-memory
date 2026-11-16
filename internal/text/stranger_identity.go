// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package text

import (
	"strings"
	"unicode"
)

// ExtractStrangerChannelSuffix extracts the distinct individual suffix from an unnamed
// human interlocutor channel token (e.g., "user" -> ("1", true), "user_2" -> ("2", true),
// "person_2" -> ("2", true), "人2" -> ("2", true), "Person 3" -> ("3", true)).
// Returns ("", false) when raw is not an unnamed human interlocutor (e.g., "assistant", "STAGE", or a proper name).
func ExtractStrangerChannelSuffix(raw string) (string, bool) {
	low := strings.ToLower(strings.TrimSpace(raw))
	if low == "" {
		return "", false
	}
	switch low {
	case "user", "speaker", "reader", "human", "person", "stranger", "人", "陌生人", "对话者", "interlocutor":
		return "1", true
	}
	for _, pfx := range []string{
		"user_", "user#", "user-", "user ",
		"person_", "person#", "person-", "person ",
		"speaker_", "speaker#", "speaker-", "speaker ",
		"reader_", "reader#", "reader-", "reader ",
		"stranger_", "stranger#", "stranger-", "stranger ",
		"visitor_", "visitor#", "visitor-", "visitor ",
		"interlocutor_", "interlocutor#", "interlocutor-", "interlocutor ",
		"人#", "人_", "人-", "人 ", "人",
		"陌生人#", "陌生人_", "陌生人-", "陌生人 ", "陌生人",
		"对话者#", "对话者_", "对话者-", "对话者 ", "对话者",
	} {
		if strings.HasPrefix(low, pfx) {
			rest := strings.TrimSpace(low[len(pfx):])
			if isValidStrangerSuffix(rest) {
				return rest, true
			}
		}
	}
	return "", false
}

func isValidStrangerSuffix(s string) bool {
	if s == "" || len(s) > 8 {
		return false
	}
	allDigits := true
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			allDigits = false
			break
		}
	}
	if allDigits {
		return true
	}
	return len(s) == 1 && s[0] >= 'a' && s[0] <= 'z'
}

// FormatAnonymousStrangerLabel formats a deterministic base-category + ordinal label for an unnamed
// person individual ("人1", "人2" for CJK scripts; "Person 1", "Person 2" otherwise — following the
// universal ontology that any unnamed entity falls back to its base category noun + ordinal ID).
func FormatAnonymousStrangerLabel(suffix string, preferCJK bool) string {
	suf := strings.TrimSpace(suffix)
	if suf == "" {
		suf = "1"
	}
	if preferCJK {
		return "人" + suf
	}
	return "Person " + suf
}

// IsAnonymousStrangerLabel reports whether s is a materialized anonymous stranger individual entity label
// (e.g., "人1", "人2", "Person 1", "Person 2", "陌生人1", "Stranger 1"), distinct from raw unresolved role placeholders.
func IsAnonymousStrangerLabel(s string) bool {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" || IsRolePlaceholder(trimmed) {
		return false
	}
	_, ok := ExtractStrangerChannelSuffix(trimmed)
	return ok
}

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
		case (r >= '０' && r <= '９') || (r >= 'Ａ' && r <= 'Ｚ') || (r >= 'ａ' && r <= 'ｚ') ||
			r == '－' || r == '＿' || r == '＃':
			b.WriteRune(r - 0xFEE0)
		case r == '\u00ad' || unicode.Is(unicode.Cf, r):
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func IsRolePlaceholder(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "user", "assistant", "reader", "speaker", "human", "ai", "bot", "system", "model":
		return true
	}
	return false
}
