// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/rocareer/rolling-memory/internal/ids"
)

// decodeJSONValue parses one --*-json argument. Integral json.Number values
// are normalized to int64 so ids.ParseTime and the assertion payloads see an
// int/float split rather than float64-only numbers.
func decodeJSONValue(text string) (any, error) {
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, errors.New(ids.PyJSONDecodeMessage(text, err))
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("Extra data")
		}
		return nil, errors.New(ids.PyJSONDecodeMessage(text, err))
	}
	return normalizeJSONNumbers(value), nil
}

func normalizeJSONNumbers(value any) any {
	switch typed := value.(type) {
	case json.Number:
		if integer, err := strconv.ParseInt(typed.String(), 10, 64); err == nil {
			return integer
		}
		if number, err := typed.Float64(); err == nil {
			return number
		}
		return typed.String()
	case map[string]any:
		for key, item := range typed {
			typed[key] = normalizeJSONNumbers(item)
		}
		return typed
	case []any:
		for index, item := range typed {
			typed[index] = normalizeJSONNumbers(item)
		}
		return typed
	default:
		return value
	}
}

// expandUser mirrors pathlib.Path.expanduser for the CLI's config argument.
func expandUser(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}

// ------------------------------------------------------------------ values

// pyStr renders a value for human output: None/True/False, bare strings, and
// shortest-round-trip floats.
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
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return pyFloatText(typed)
	default:
		if isContainer(value) {
			return pyRepr(value)
		}
		return fmt.Sprint(value)
	}
}

// pyRepr renders dict/list values the way standard repr() does, which is what
// an f-string falls back to for containers.
func pyRepr(value any) string {
	if value == nil {
		return "None"
	}
	reflected := reflect.ValueOf(value)
	for reflected.Kind() == reflect.Interface || reflected.Kind() == reflect.Pointer {
		if reflected.IsNil() {
			return "None"
		}
		reflected = reflected.Elem()
	}
	switch reflected.Kind() {
	case reflect.Bool:
		if reflected.Bool() {
			return "True"
		}
		return "False"
	case reflect.String:
		return "'" + strings.ReplaceAll(reflected.String(), "'", "\\'") + "'"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(reflected.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(reflected.Uint(), 10)
	case reflect.Float32, reflect.Float64:
		return pyFloatText(reflected.Float())
	case reflect.Slice, reflect.Array:
		parts := make([]string, 0, reflected.Len())
		for index := 0; index < reflected.Len(); index++ {
			parts = append(parts, pyRepr(reflected.Index(index).Interface()))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case reflect.Map:
		if reflected.Type().Key().Kind() != reflect.String {
			return fmt.Sprint(value)
		}
		keys := make([]string, 0, reflected.Len())
		iterator := reflected.MapRange()
		for iterator.Next() {
			keys = append(keys, iterator.Key().String())
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			keyValue := reflect.ValueOf(key).Convert(reflected.Type().Key())
			parts = append(parts, pyRepr(key)+": "+pyRepr(reflected.MapIndex(keyValue).Interface()))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	default:
		return fmt.Sprint(value)
	}
}

// pyFloatText matches str(float) closely enough for human output.
func pyFloatText(value float64) string {
	switch {
	case math.IsNaN(value):
		return "nan"
	case math.IsInf(value, 1):
		return "inf"
	case math.IsInf(value, -1):
		return "-inf"
	}
	text := strconv.FormatFloat(value, 'g', -1, 64)
	if !strings.ContainsAny(text, ".eE") {
		text += ".0"
	}
	return text
}

// pyFloat mirrors float(value or 0) for the human renderers.
func pyFloat(value any) float64 {
	switch typed := value.(type) {
	case nil:
		return 0
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case bool:
		if typed {
			return 1
		}
		return 0
	case json.Number:
		number, _ := typed.Float64()
		return number
	default:
		return 0
	}
}

// pyInt mirrors int(value or 0) for the human renderers.
func pyInt(value any) int {
	switch typed := value.(type) {
	case nil:
		return 0
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case bool:
		if typed {
			return 1
		}
		return 0
	case json.Number:
		number, _ := typed.Int64()
		return int(number)
	default:
		return 0
	}
}

// truthy mirrors standard truthiness for the "or default" patterns.
func truthy(value any) bool {
	if value == nil {
		return false
	}
	reflected := reflect.ValueOf(value)
	for reflected.Kind() == reflect.Interface || reflected.Kind() == reflect.Pointer {
		if reflected.IsNil() {
			return false
		}
		reflected = reflected.Elem()
	}
	switch reflected.Kind() {
	case reflect.Bool:
		return reflected.Bool()
	case reflect.String:
		return reflected.String() != ""
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return reflected.Int() != 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return reflected.Uint() != 0
	case reflect.Float32, reflect.Float64:
		return reflected.Float() != 0
	case reflect.Slice, reflect.Array, reflect.Map:
		return reflected.Len() > 0
	default:
		return true
	}
}

func isContainer(value any) bool {
	if value == nil {
		return false
	}
	kind := reflect.ValueOf(value).Kind()
	return kind == reflect.Map || kind == reflect.Slice || kind == reflect.Array
}

// valueOr returns value when truthy, otherwise fallback.
func valueOr(value, fallback any) any {
	if truthy(value) {
		return value
	}
	return fallback
}

// getOr mirrors dict.get(key, fallback): only a missing key falls back, so
// 0 / 0.0 / false survive (the health/status renderers depend on that).
func getOr(mapping map[string]any, key string, fallback any) any {
	if value, ok := mapping[key]; ok {
		return value
	}
	return fallback
}

// stringList flattens the []any / []string alias and id collections.
func stringList(value any) []string {
	items := anyList(value)
	if items == nil {
		return nil
	}
	values := make([]string, 0, len(items))
	for _, item := range items {
		values = append(values, pyStr(item))
	}
	return values
}

// asMap converts any string-keyed map (including map[string]int and other
// concrete value types) into the generic shape the renderers inspect.
func asMap(value any) map[string]any {
	if value == nil {
		return nil
	}
	reflected := reflect.ValueOf(value)
	for reflected.Kind() == reflect.Interface || reflected.Kind() == reflect.Pointer {
		if reflected.IsNil() {
			return nil
		}
		reflected = reflected.Elem()
	}
	if reflected.Kind() != reflect.Map || reflected.Type().Key().Kind() != reflect.String {
		return nil
	}
	result := make(map[string]any, reflected.Len())
	iterator := reflected.MapRange()
	for iterator.Next() {
		result[iterator.Key().String()] = iterator.Value().Interface()
	}
	return result
}

// child returns map[key] for the nested receipt shapes.
func child(value any, key string) any {
	if mapping := asMap(value); mapping != nil {
		return mapping[key]
	}
	return nil
}

// childMap returns map[key] when it is an object, otherwise an empty map.
func childMap(value any, key string) map[string]any {
	if mapping := asMap(child(value, key)); mapping != nil {
		return mapping
	}
	return map[string]any{}
}

// childList returns map[key] as a list of objects.
func childList(value any, key string) []map[string]any {
	items := anyList(child(value, key))
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if mapping := asMap(item); mapping != nil {
			result = append(result, mapping)
		}
	}
	return result
}

// percent1 formats a ratio as a one-decimal percentage (e.g. "42.0%").
func percent1(value any) string {
	return fmt.Sprintf("%.1f%%", pyFloat(value)*100)
}
