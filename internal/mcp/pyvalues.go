// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package mcp

// This file provides the value coercion and formatting helpers the stdio
// adapter relies on: truthiness, string/integer coercion, and error wording
// for JSON-decoded values.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/rocareer/rolling-memory/internal/ids"
)

// pyTruthy mirrors standard truthiness for JSON-decoded values.
func pyTruthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case json.Number:
		return pyNumberTruthy(typed)
	case float64:
		return typed != 0
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	}
	return true
}

func pyNumberTruthy(number json.Number) bool {
	text := number.String()
	if !strings.ContainsAny(text, ".eE") {
		if parsed, err := strconv.ParseInt(text, 10, 64); err == nil {
			return parsed != 0
		}
	}
	value, err := number.Float64()
	if err != nil {
		return true
	}
	return value != 0
}

// pyStr mirrors str() for JSON-decoded values: containers fall through to repr.
func pyStr(value any) string {
	switch typed := value.(type) {
	case nil:
		return "None"
	case string:
		return typed
	case bool:
		if typed {
			return "True"
		}
		return "False"
	case json.Number:
		return pyNumberStr(typed)
	case float64:
		return pyFloatStr(typed)
	case float32:
		return pyFloatStr(float64(typed))
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	}
	return pyRepr(value)
}

// pyRepr mirrors repr() well enough for error messages and str() of containers.
// Object keys are sorted; standard would keep insertion order.
func pyRepr(value any) string {
	switch typed := value.(type) {
	case string:
		return pyQuote(typed)
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			parts = append(parts, pyRepr(item))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, pyQuote(key)+": "+pyRepr(typed[key]))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return pyStr(value)
}

func pyQuote(value string) string {
	quote := byte('\'')
	if strings.Contains(value, "'") && !strings.Contains(value, "\"") {
		quote = '"'
	}
	var out strings.Builder
	out.WriteByte(quote)
	for _, char := range value {
		switch char {
		case '\\':
			out.WriteString(`\\`)
		case rune(quote):
			out.WriteByte('\\')
			out.WriteRune(char)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			if char < 0x20 || char == 0x7f {
				fmt.Fprintf(&out, `\x%02x`, char)
				continue
			}
			out.WriteRune(char)
		}
	}
	out.WriteByte(quote)
	return out.String()
}

func pyNumberStr(number json.Number) string {
	text := number.String()
	if !strings.ContainsAny(text, ".eE") {
		return text
	}
	value, err := number.Float64()
	if err != nil {
		return text
	}
	return pyFloatStr(value)
}

func pyFloatStr(value float64) string {
	text, err := ids.PyJSONDump(value, false, false, true)
	if err != nil {
		return strconv.FormatFloat(value, 'g', -1, 64)
	}
	return text
}

func pyTypeName(value any) string {
	switch typed := value.(type) {
	case nil:
		return "NoneType"
	case bool:
		return "bool"
	case string:
		return "str"
	case json.Number:
		if strings.ContainsAny(typed.String(), ".eE") {
			return "float"
		}
		return "int"
	case float64, float32:
		return "float"
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return "int"
	case []any:
		return "list"
	case map[string]any:
		return "dict"
	}
	return fmt.Sprintf("%T", value)
}

// pyInt coerces a JSON-decoded value to an integer with consistent
// TypeError/ValueError wording for MCP argument validation.
func pyInt(value any) (int, error) {
	switch typed := value.(type) {
	case nil:
		return 0, intTypeError("NoneType")
	case bool:
		if typed {
			return 1, nil
		}
		return 0, nil
	case string:
		return pyIntFromString(typed)
	case json.Number:
		return pyIntFromNumber(typed)
	case float64:
		return pyIntFromFloat(typed)
	case float32:
		return pyIntFromFloat(float64(typed))
	case int:
		return typed, nil
	case int64:
		return int(typed), nil
	}
	return 0, intTypeError(pyTypeName(value))
}

func intTypeError(typeName string) error {
	return fmt.Errorf("int() argument must be a string, a bytes-like object or a real number, not '%s'", typeName)
}

func pyIntFromString(value string) (int, error) {
	text := strings.TrimSpace(value)
	if text == "" {
		return 0, intValueError(value)
	}
	negative := false
	if text[0] == '+' || text[0] == '-' {
		negative = text[0] == '-'
		text = text[1:]
	}
	if text == "" || text[0] == '_' || text[len(text)-1] == '_' {
		return 0, intValueError(value)
	}
	digits := make([]byte, 0, len(text))
	for index := 0; index < len(text); index++ {
		char := text[index]
		if char == '_' {
			if index+1 >= len(text) || text[index+1] == '_' {
				return 0, intValueError(value)
			}
			continue
		}
		if char < '0' || char > '9' {
			return 0, intValueError(value)
		}
		digits = append(digits, char)
	}
	parsed, err := strconv.ParseInt(string(digits), 10, 64)
	if err != nil {
		// Every character is a digit, so only int64 overflow is left.
		return 0, fmt.Errorf("value out of range for a Go int")
	}
	if negative {
		parsed = -parsed
	}
	return int(parsed), nil
}

func intValueError(value string) error {
	return fmt.Errorf("invalid literal for int() with base 10: '%s'", value)
}

func pyIntFromNumber(number json.Number) (int, error) {
	text := number.String()
	if !strings.ContainsAny(text, ".eE") {
		if parsed, err := strconv.ParseInt(text, 10, 64); err == nil {
			return int(parsed), nil
		}
	}
	value, err := number.Float64()
	if err != nil {
		return 0, fmt.Errorf("invalid literal for int() with base 10: '%s'", text)
	}
	return pyIntFromFloat(value)
}

func pyIntFromFloat(value float64) (int, error) {
	switch {
	case value != value:
		return 0, fmt.Errorf("cannot convert float NaN to integer")
	case value > float64(1<<62) || value < -float64(1<<62):
		// standard has unbounded integers here; Go's int is the port's slot.
		return 0, fmt.Errorf("value out of range for a Go int")
	}
	return int(value), nil
}

// pyLen mirrors len() for the receipt collections the summary walks.
func pyLen(value any) int {
	switch typed := value.(type) {
	case string:
		return utf8.RuneCountInString(typed)
	case []any:
		return len(typed)
	case []map[string]any:
		return len(typed)
	case []string:
		return len(typed)
	case map[string]any:
		return len(typed)
	}
	return 0
}

// firstChars truncates text to at most limit Unicode code points (runes, not bytes).
func firstChars(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

// textOrEmpty reads an optional string argument (absent -> "").
func textOrEmpty(value any) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}

// boolOrNil reads an optional boolean argument as a three-state pointer:
// absent -> nil (the engine keeps its default), explicit false survives.
func boolOrNil(value any) *bool {
	switch typed := value.(type) {
	case bool:
		result := typed
		return &result
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true":
			result := true
			return &result
		case "false":
			result := false
			return &result
		}
	}
	return nil
}
