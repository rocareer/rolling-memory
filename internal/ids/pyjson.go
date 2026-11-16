// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package ids

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// PyJSONDump mirrors standard json.dumps with the requested separators and
// escaping. It exists because encoding/json differs on HTML characters,
// U+2028/U+2029 and float formatting, and those bytes are persistent. Map
// iteration order is undefined when sortKeys is false; pass OrderedMap when
// insertion order must be preserved.
func PyJSONDump(value any, ensureASCII, sortKeys, compact bool) (string, error) {
	options := jsonOptions{
		ensureASCII: ensureASCII,
		sortKeys:    sortKeys,
		compact:     compact,
	}
	var out strings.Builder
	if err := encodePyJSON(&out, value, options, 0); err != nil {
		return "", err
	}
	return out.String(), nil
}

// PyJSONDumpIndent mirrors json.dumps(..., indent=indent, sort_keys=sortKeys).
func PyJSONDumpIndent(value any, ensureASCII, sortKeys bool, indent int) (string, error) {
	if indent < 0 {
		indent = 0
	}
	options := jsonOptions{
		ensureASCII: ensureASCII,
		sortKeys:    sortKeys,
		indent:      indent,
	}
	var out strings.Builder
	if err := encodePyJSON(&out, value, options, 0); err != nil {
		return "", err
	}
	return out.String(), nil
}

type jsonOptions struct {
	ensureASCII bool
	sortKeys    bool
	compact     bool
	indent      int
}

// JSONPair is one entry of an insertion-ordered JSON object.
type JSONPair struct {
	Key   string
	Value any
}

// OrderedMap preserves standard dict insertion order for encoders that need it.
type OrderedMap []JSONPair

func encodePyJSON(out *strings.Builder, value any, options jsonOptions, level int) error {
	if value == nil {
		out.WriteString("null")
		return nil
	}
	if number, ok := value.(json.Number); ok {
		out.WriteString(number.String())
		return nil
	}
	if ordered, ok := value.(OrderedMap); ok {
		return encodePyJSONOrderedMap(out, ordered, options, level)
	}
	if _, ok := value.([]byte); ok {
		return fmt.Errorf("Object of type bytes is not JSON serializable")
	}

	reflected := reflect.ValueOf(value)
	for reflected.Kind() == reflect.Pointer || reflected.Kind() == reflect.Interface {
		if reflected.IsNil() {
			out.WriteString("null")
			return nil
		}
		reflected = reflected.Elem()
	}

	switch reflected.Kind() {
	case reflect.Bool:
		if reflected.Bool() {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
	case reflect.String:
		encodePyJSONString(out, reflected.String(), options.ensureASCII)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		out.WriteString(strconv.FormatInt(reflected.Int(), 10))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		out.WriteString(strconv.FormatUint(reflected.Uint(), 10))
	case reflect.Float32, reflect.Float64:
		out.WriteString(pyFloatRepr(reflected.Float()))
	case reflect.Slice, reflect.Array:
		return encodePyJSONArray(out, reflected, options, level)
	case reflect.Map:
		return encodePyJSONMap(out, reflected, options, level)
	default:
		return fmt.Errorf("Object of type %s is not JSON serializable", reflected.Type())
	}
	return nil
}

func encodePyJSONArray(out *strings.Builder, value reflect.Value, options jsonOptions, level int) error {
	if value.Len() == 0 {
		out.WriteString("[]")
		return nil
	}
	out.WriteByte('[')
	for index := 0; index < value.Len(); index++ {
		if index > 0 {
			out.WriteString(itemSeparator(options))
		}
		if options.indent > 0 {
			out.WriteByte('\n')
			out.WriteString(strings.Repeat(" ", options.indent*(level+1)))
		}
		if err := encodePyJSON(out, value.Index(index).Interface(), options, level+1); err != nil {
			return err
		}
	}
	if options.indent > 0 {
		out.WriteByte('\n')
		out.WriteString(strings.Repeat(" ", options.indent*level))
	}
	out.WriteByte(']')
	return nil
}

func encodePyJSONMap(out *strings.Builder, value reflect.Value, options jsonOptions, level int) error {
	if value.Type().Key().Kind() != reflect.String {
		return fmt.Errorf("keys must be strings")
	}
	if value.Len() == 0 {
		out.WriteString("{}")
		return nil
	}
	keys := make([]string, 0, value.Len())
	iterator := value.MapRange()
	for iterator.Next() {
		keys = append(keys, iterator.Key().String())
	}
	if options.sortKeys {
		sort.Strings(keys)
	}
	out.WriteByte('{')
	for index, key := range keys {
		if index > 0 {
			out.WriteString(itemSeparator(options))
		}
		if options.indent > 0 {
			out.WriteByte('\n')
			out.WriteString(strings.Repeat(" ", options.indent*(level+1)))
		}
		encodePyJSONString(out, key, options.ensureASCII)
		out.WriteString(keySeparator(options))
		keyValue := reflect.ValueOf(key).Convert(value.Type().Key())
		if err := encodePyJSON(out, value.MapIndex(keyValue).Interface(), options, level+1); err != nil {
			return err
		}
	}
	if options.indent > 0 {
		out.WriteByte('\n')
		out.WriteString(strings.Repeat(" ", options.indent*level))
	}
	out.WriteByte('}')
	return nil
}

func encodePyJSONOrderedMap(out *strings.Builder, value OrderedMap, options jsonOptions, level int) error {
	if len(value) == 0 {
		out.WriteString("{}")
		return nil
	}
	pairs := value
	if options.sortKeys {
		pairs = append(OrderedMap(nil), value...)
		sort.SliceStable(pairs, func(left, right int) bool {
			return pairs[left].Key < pairs[right].Key
		})
	}
	out.WriteByte('{')
	for index, pair := range pairs {
		if index > 0 {
			out.WriteString(itemSeparator(options))
		}
		if options.indent > 0 {
			out.WriteByte('\n')
			out.WriteString(strings.Repeat(" ", options.indent*(level+1)))
		}
		encodePyJSONString(out, pair.Key, options.ensureASCII)
		out.WriteString(keySeparator(options))
		if err := encodePyJSON(out, pair.Value, options, level+1); err != nil {
			return err
		}
	}
	if options.indent > 0 {
		out.WriteByte('\n')
		out.WriteString(strings.Repeat(" ", options.indent*level))
	}
	out.WriteByte('}')
	return nil
}

func itemSeparator(options jsonOptions) string {
	if options.indent > 0 {
		return ","
	}
	if options.compact {
		return ","
	}
	return ", "
}

func keySeparator(options jsonOptions) string {
	if options.compact && options.indent == 0 {
		return ":"
	}
	return ": "
}

func encodePyJSONString(out *strings.Builder, value string, ensureASCII bool) {
	const hexDigits = "0123456789abcdef"
	out.WriteByte('"')
	for _, char := range value {
		switch char {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			if char < 0x20 {
				out.WriteString(`\u00`)
				out.WriteByte(hexDigits[(char>>4)&0xF])
				out.WriteByte(hexDigits[char&0xF])
				continue
			}
			if ensureASCII && char > 0x7F {
				if char <= 0xFFFF {
					out.WriteString(`\u`)
					writeHex4(out, uint32(char))
					continue
				}
				code := uint32(char) - 0x10000
				high := 0xD800 + (code >> 10)
				low := 0xDC00 + (code & 0x3FF)
				out.WriteString(`\u`)
				writeHex4(out, high)
				out.WriteString(`\u`)
				writeHex4(out, low)
				continue
			}
			out.WriteRune(char)
		}
	}
	out.WriteByte('"')
}

func writeHex4(out *strings.Builder, value uint32) {
	const hexDigits = "0123456789abcdef"
	out.WriteByte(hexDigits[(value>>12)&0xF])
	out.WriteByte(hexDigits[(value>>8)&0xF])
	out.WriteByte(hexDigits[(value>>4)&0xF])
	out.WriteByte(hexDigits[value&0xF])
}

// pyFloatRepr matches standard repr(float): shortest round-trip digits, fixed
// notation while -4 < decpt <= 16, exponent notation otherwise.
func pyFloatRepr(value float64) string {
	if math.IsNaN(value) {
		return "NaN"
	}
	if math.IsInf(value, 1) {
		return "Infinity"
	}
	if math.IsInf(value, -1) {
		return "-Infinity"
	}
	exponentForm := strconv.FormatFloat(value, 'e', -1, 64)
	exponent := 0
	if position := strings.IndexByte(exponentForm, 'e'); position >= 0 {
		exponent, _ = strconv.Atoi(exponentForm[position+1:])
	}
	decimalPoint := exponent + 1
	if decimalPoint <= -4 || decimalPoint > 16 {
		return exponentForm
	}
	fixed := strconv.FormatFloat(value, 'f', -1, 64)
	if !strings.Contains(fixed, ".") {
		fixed += ".0"
	}
	return fixed
}
