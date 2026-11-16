// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package harness

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/rocareer/rolling-memory/internal/ids"
)

// The JSON writer mirrors json.dumps(..., ensure_ascii=False, indent=2) + "\n".
// Objects are decoded into ids.OrderedMap so that keys the harness does not own
// keep their original position, exactly like a standard dict round-trip.

func loadJSONFile(path string) (ids.OrderedMap, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ids.OrderedMap{}, nil
	}
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return ids.OrderedMap{}, nil
	}
	value, err := decodeJSON([]byte(text))
	if err != nil {
		return nil, err
	}
	object, ok := asObject(value)
	if !ok {
		return nil, fmt.Errorf("%s: top-level JSON must be an object", path)
	}
	return object, nil
}

func decodeJSON(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeJSONValue(decoder)
	if err != nil {
		return nil, err
	}
	if token, err := decoder.Token(); err != io.EOF {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("extra data after JSON value: %v", token)
	}
	return value, nil
}

func decodeJSONValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return token, nil // string, json.Number, bool or nil
	}
	switch delim {
	case '{':
		object := make(ids.OrderedMap, 0, 4)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, fmt.Errorf("object key is not a string")
			}
			value, err := decodeJSONValue(decoder)
			if err != nil {
				return nil, err
			}
			object = append(object, ids.JSONPair{Key: key, Value: value})
		}
		if _, err := decoder.Token(); err != nil { // consume '}'
			return nil, err
		}
		return object, nil
	case '[':
		items := make([]any, 0, 4)
		for decoder.More() {
			value, err := decodeJSONValue(decoder)
			if err != nil {
				return nil, err
			}
			items = append(items, value)
		}
		if _, err := decoder.Token(); err != nil { // consume ']'
			return nil, err
		}
		return items, nil
	}
	return nil, fmt.Errorf("unexpected JSON delimiter %q", delim)
}

func dumpJSON(data ids.OrderedMap) (string, error) {
	text, err := ids.PyJSONDumpIndent(data, false, false, 2)
	if err != nil {
		return "", err
	}
	return text + "\n", nil
}

// oneLineJSON mirrors _dump_json(...).strip().replace("\n", " ").
func oneLineJSON(text string) string {
	return strings.ReplaceAll(strings.TrimSpace(text), "\n", " ")
}

// asObject accepts the insertion-ordered form and the plain maps produced by
// the TOML decoder.
func asObject(value any) (ids.OrderedMap, bool) {
	switch typed := value.(type) {
	case ids.OrderedMap:
		return typed, true
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		object := make(ids.OrderedMap, 0, len(typed))
		for _, key := range keys {
			object = append(object, ids.JSONPair{Key: key, Value: typed[key]})
		}
		return object, true
	}
	return nil, false
}

func asArray(value any) ([]any, bool) {
	switch typed := value.(type) {
	case []any:
		return typed, true
	case []string:
		items := make([]any, len(typed))
		for index, item := range typed {
			items[index] = item
		}
		return items, true
	}
	reflected := reflect.ValueOf(value)
	if reflected.IsValid() && (reflected.Kind() == reflect.Slice || reflected.Kind() == reflect.Array) {
		items := make([]any, reflected.Len())
		for index := range items {
			items[index] = reflected.Index(index).Interface()
		}
		return items, true
	}
	return nil, false
}

func objectGet(object ids.OrderedMap, key string) (any, bool) {
	for _, pair := range object {
		if pair.Key == key {
			return pair.Value, true
		}
	}
	return nil, false
}

// objectSet replaces an existing key in place or appends a new one at the end,
// matching standard dict assignment order.
func objectSet(object ids.OrderedMap, key string, value any) ids.OrderedMap {
	for index := range object {
		if object[index].Key == key {
			object[index].Value = value
			return object
		}
	}
	return append(object, ids.JSONPair{Key: key, Value: value})
}

func objectDelete(object ids.OrderedMap, key string) ids.OrderedMap {
	for index, pair := range object {
		if pair.Key == key {
			remaining := make(ids.OrderedMap, 0, len(object)-1)
			remaining = append(remaining, object[:index]...)
			remaining = append(remaining, object[index+1:]...)
			return remaining
		}
	}
	return object
}

// jsonTable mirrors _json_table: walk keys, optionally creating missing
// objects. It returns the (possibly rebuilt) root because OrderedMap values
// are not addressable.
func jsonTable(data ids.OrderedMap, keys []string, create bool) (ids.OrderedMap, ids.OrderedMap, bool) {
	if len(keys) == 0 {
		return data, data, true
	}
	key := keys[0]
	var child ids.OrderedMap
	found := false
	if raw, ok := objectGet(data, key); ok {
		if object, isObject := asObject(raw); isObject {
			child = object
			found = true
		}
	}
	if !found {
		if !create {
			return nil, data, false
		}
		child = ids.OrderedMap{}
	}
	table, newChild, ok := jsonTable(child, keys[1:], create)
	if !ok {
		return nil, data, false
	}
	return table, objectSet(data, key, newChild), true
}

// jsonTableWith is jsonTable plus a mutation applied to the innermost object.
func jsonTableWith(data ids.OrderedMap, keys []string, create bool, mutate func(ids.OrderedMap) ids.OrderedMap) (ids.OrderedMap, bool) {
	if len(keys) == 0 {
		return mutate(data), true
	}
	key := keys[0]
	var child ids.OrderedMap
	found := false
	if raw, ok := objectGet(data, key); ok {
		if object, isObject := asObject(raw); isObject {
			child = object
			found = true
		}
	}
	if !found {
		if !create {
			return data, false
		}
		child = ids.OrderedMap{}
	}
	if len(keys) == 1 {
		return objectSet(data, key, mutate(child)), true
	}
	updated, ok := jsonTableWith(child, keys[1:], create, mutate)
	if !ok {
		return data, false
	}
	return objectSet(data, key, updated), true
}

func jsonLookup(data ids.OrderedMap, keys []string) (any, bool) {
	var node any = data
	for _, key := range keys {
		object, ok := asObject(node)
		if !ok {
			return nil, false
		}
		node, ok = objectGet(object, key)
		if !ok {
			return nil, false
		}
	}
	return node, true
}

// jsonEqual mirrors standard == for decoded JSON: objects compare by key/value
// (order-insensitive), numbers compare by value (True == 1), arrays by item.
func jsonEqual(left, right any) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	if leftObject, ok := asObject(left); ok {
		rightObject, ok := asObject(right)
		if !ok || len(leftObject) != len(rightObject) {
			return false
		}
		for _, pair := range leftObject {
			value, found := objectGet(rightObject, pair.Key)
			if !found || !jsonEqual(pair.Value, value) {
				return false
			}
		}
		return true
	}
	if leftArray, ok := asArray(left); ok {
		rightArray, ok := asArray(right)
		if !ok || len(leftArray) != len(rightArray) {
			return false
		}
		for index := range leftArray {
			if !jsonEqual(leftArray[index], rightArray[index]) {
				return false
			}
		}
		return true
	}
	if leftNumber, ok := numericValue(left); ok {
		rightNumber, ok := numericValue(right)
		return ok && leftNumber == rightNumber
	}
	switch typed := left.(type) {
	case string:
		other, ok := right.(string)
		return ok && typed == other
	case bool:
		other, ok := right.(bool)
		return ok && typed == other
	}
	return reflect.DeepEqual(left, right)
}

func numericValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case json.Number:
		number, err := typed.Float64()
		return number, err == nil
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int8:
		return float64(typed), true
	case int16:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case uint8:
		return float64(typed), true
	case uint16:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case bool:
		if typed {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// pyJSONString quotes a TOML / YAML scalar exactly like json.dumps(s).
func pyJSONString(value string) (string, error) {
	return ids.PyJSONDump(value, false, false, true)
}
