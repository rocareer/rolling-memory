// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package ids

import (
	"errors"
	"io"
	"strings"
)

// PyJSONDecodeMessage formats a concise diagnostic for one failed JSON decode
// of raw, used in user-visible errors (CLI --*-json prefixes, MCP parse errors,
// hook fail-open logs).
//
// The truncated-input branch reports what token the decoder was expecting when
// the text ended.
func PyJSONDecodeMessage(raw string, err error) string {
	if errors.Is(err, io.EOF) {
		return "Expecting value"
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || strings.Contains(err.Error(), "unexpected EOF") {
		return eofJSONMessage(raw)
	}
	message := err.Error()
	switch {
	case strings.Contains(message, "in string escape code"):
		return `Invalid \escape`
	case strings.Contains(message, "in string literal"):
		return "Invalid control character at"
	case strings.Contains(message, "looking for beginning of object key string"):
		return "Expecting property name enclosed in double quotes"
	case strings.Contains(message, "after object key:value pair"), strings.Contains(message, "after array element"):
		return "Expecting ',' delimiter"
	case strings.Contains(message, "after object key"):
		return "Expecting ':' delimiter"
	case strings.Contains(message, "after top-level value"):
		return "Extra data"
	}
	return "Expecting value"
}

// eofJSONMessage names the chain standard reports for truncated input, based on
// whether the input stopped inside a string or inside an object/array.
func eofJSONMessage(raw string) string {
	inString := false
	escaped := false
	stack := make([]byte, 0, 8)
	for index := 0; index < len(raw); index++ {
		char := raw[index]
		if inString {
			switch {
			case escaped:
				escaped = false
			case char == '\\':
				escaped = true
			case char == '"':
				inString = false
			}
			continue
		}
		switch char {
		case '"':
			inString = true
		case '{', '[':
			stack = append(stack, char)
		case '}', ']':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	if inString {
		return "Unterminated string starting at"
	}
	trimmed := strings.TrimRight(raw, " \t\r\n")
	if trimmed == "" || len(stack) == 0 {
		return "Expecting value"
	}
	last := trimmed[len(trimmed)-1]
	if stack[len(stack)-1] == '{' {
		if last == '{' || last == ',' {
			return "Expecting property name enclosed in double quotes"
		}
	} else if last == '[' || last == ',' {
		return "Expecting value"
	}
	if last == ':' {
		return "Expecting value"
	}
	return "Expecting ',' delimiter"
}
