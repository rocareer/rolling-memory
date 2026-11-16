// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package golden

// Enumerated golden deviations.
//
// Every accepted difference between the frozen baseline expectations and the Go
// implementation is declared here by name and checked by value. The comparator
// does not mutate either side (no point deletions): it walks both values and,
// at the declared paths only, tolerates extra keys whose values must equal the
// declaration — and whose presence is required, so a registered deviation
// cannot silently disappear.
//
// Path grammar: "/"-separated steps from the JSON root; `[name=X]` selects the
// element of an array whose "name" field equals X. Purely structural rules
// (embedded JSON documents compared as values, objects compared order-free)
// need no entries: they apply to both sides before the walk and are documented
// at their implementation sites (normalizeProbeText; canonical dumps).

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

type enumeratedDeviation struct {
	Scenario string
	Path     string
	Key      string
	Value    any
	Note     string
}

// enumeratedDeviations is the closed set for the sealed corpus.
var enumeratedDeviations = []enumeratedDeviation{
	{
		Scenario: "220-mcp",
		Path:     "result/tools/[name=remember]",
		Key:      "annotations",
		Value: map[string]any{
			"idempotentHint":  true,
			"openWorldHint":   false,
			"destructiveHint": false,
		},
		Note: "MCP annotations on remember: replay-idempotent append-only writes against the local graph",
	},
	{
		Scenario: "220-mcp",
		Path:     "result/tools/[name=recall]",
		Key:      "annotations",
		Value: map[string]any{
			"readOnlyHint":  true,
			"openWorldHint": false,
		},
		Note: "MCP annotations on recall: read-only reads against the local graph",
	},
	{
		Scenario: "220-mcp",
		Path:     "result/tools/[name=remember]/inputSchema/properties/assertion/properties",
		Key:      "subject_absent",
		Value: map[string]any{
			"type": "boolean",
			"description": "二元-XO 声明（可选）：主体**不适用**（客观事件，如地震 / 比赛取消）置 true——" +
				"与 s 互斥；主体只是未知（未定槽）当前未支持，不要用本键；" +
				"客体不适用（谓词无客体：笑了 / 咳嗽 / 下雨）请省略 o 直接记二元-SX",
		},
		Note: "Dyadic XO form: subject_absent boolean property",
	},
	{
		Scenario: "220-mcp",
		Path:     "result/tools/[name=remember]",
		Key:      "title",
		Value:    "记忆",
		Note:     "MCP tool display title: client-facing label",
	},
	{
		Scenario: "220-mcp",
		Path:     "result/tools/[name=recall]",
		Key:      "title",
		Value:    "回忆",
		Note:     "MCP tool display title: client-facing label",
	},
	{
		Scenario: "220-mcp",
		Path:     "result/tools/[name=recall]/inputSchema/properties/caps/properties",
		Key:      "chains",
		Value:    map[string]any{"type": "integer", "description": "Chains per band (delivery breadth)."},
		Note:     "Delivery-breadth switch: chains per band (mirrors request-level caps_chains)",
	},
	{
		Scenario: "220-mcp",
		Path:     "result/tools/[name=recall]/inputSchema/properties/caps/properties",
		Key:      "rows",
		Value:    map[string]any{"type": "integer", "description": "Rows per chain (delivery breadth)."},
		Note:     "Delivery-breadth switch: rows per chain (mirrors request-level caps_rows)",
	},
	{
		Scenario: "220-mcp",
		Path:     "result/tools/[name=recall]/inputSchema/properties",
		Key:      "result_unit",
		Value:    map[string]any{"enum": []any{"band", "item"}, "description": "Delivery unit: subject bands (default) or flat source items."},
		Note:     "Delivery-unit switch: band (default) or item-level flat delivery",
	},
	{
		Scenario: "220-mcp",
		Path:     "result/tools/[name=recall]/inputSchema/properties",
		Key:      "rank",
		Value:    map[string]any{"type": "boolean", "description": "Item-mode grouping: true (default) groups by sxo->unit->parse->block; false delivers pure relevance order. Distinct from LLM rerank."},
		Note:     "Item-mode grouping switch (orthogonal to LLM rerank)",
	},
}

// compareGoldenValues is the deviation-aware comparison for a replayed MCP
// response: strict equality everywhere except the declared extras above.
func compareGoldenValues(scenario string, expected, actual any) error {
	extras := map[string]map[string]enumeratedDeviation{}
	for _, dev := range enumeratedDeviations {
		if dev.Scenario != scenario {
			continue
		}
		path := dev.Path
		if path != "" && !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		if extras[path] == nil {
			extras[path] = map[string]enumeratedDeviation{}
		}
		extras[path][dev.Key] = dev
	}
	return walkGoldenValue("", expected, actual, extras)
}

func walkGoldenValue(path string, expected, actual any, extras map[string]map[string]enumeratedDeviation) error {
	where := path
	if where == "" {
		where = "(root)"
	}
	switch exp := expected.(type) {
	case map[string]any:
		act, ok := actual.(map[string]any)
		if !ok {
			return fmt.Errorf("type mismatch at %s: want object, got %T", where, actual)
		}
		for key, expValue := range exp {
			actValue, exists := act[key]
			if !exists {
				return fmt.Errorf("missing key %q at %s", key, where)
			}
			if err := walkGoldenValue(path+"/"+key, expValue, actValue, extras); err != nil {
				return err
			}
		}
		declared := extras[path]
		for key, actValue := range act {
			if _, inExpected := exp[key]; inExpected {
				continue
			}
			dev, ok := declared[key]
			if !ok {
				return fmt.Errorf("undeclared extra key %q at %s (all differences must be registered in deviations.go)", key, where)
			}
			if err := scalarEqual(dev.Value, actValue); err != nil {
				return fmt.Errorf("declared deviation %q at %s: %v", key, where, err)
			}
		}
		for key, dev := range declared {
			if _, exists := act[key]; !exists {
				return fmt.Errorf("declared deviation %q missing at %s (%s)", key, where, dev.Note)
			}
		}
		return nil
	case []any:
		act, ok := actual.([]any)
		if !ok {
			return fmt.Errorf("type mismatch at %s: want array, got %T", where, actual)
		}
		if len(exp) != len(act) {
			return fmt.Errorf("array length at %s: want %d, got %d", where, len(exp), len(act))
		}
		for index := range exp {
			step := "/[" + strconv.Itoa(index) + "]"
			if named, ok := exp[index].(map[string]any); ok {
				if name, ok := named["name"].(string); ok {
					step = "/[name=" + name + "]"
				}
			}
			if err := walkGoldenValue(path+step, exp[index], act[index], extras); err != nil {
				return err
			}
		}
		return nil
	default:
		if err := scalarEqual(expected, actual); err != nil {
			return fmt.Errorf("value mismatch at %s: %v", where, err)
		}
		return nil
	}
}

func scalarEqual(expected, actual any) error {
	if reflect.DeepEqual(expected, actual) {
		return nil
	}
	return fmt.Errorf("want %#v, got %#v", expected, actual)
}
