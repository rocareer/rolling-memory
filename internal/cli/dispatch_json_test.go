// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

import (
	"bytes"
	"strings"
	"testing"
)

type jsonIntSample struct {
	Count  int     `json:"count"`
	Chunks int64   `json:"chunks"`
	Label  string  `json:"label"`
	Ratio  float64 `json:"ratio"`
}

// TestPrintJSONKeepsIntegerLiteralsForStructs 锁定结构体回退路径的整数保真：
// PyJSON 不直接支持结构体，经 encoding/json 归一后必须保留数字字面（json.Number），
// 否则 int 字段会渲染成 "0.0"，破坏管理后台与脚本按整数解析 CLI --json 输出的契约
// （曾致后台「同步挂载目录」报 cannot unmarshal number 0.0 into Go value of type int）。
func TestPrintJSONKeepsIntegerLiteralsForStructs(t *testing.T) {
	var out bytes.Buffer
	app := &application{stdout: &out}
	if err := app.printJSON(jsonIntSample{Count: 7, Chunks: 42, Label: "x", Ratio: 0.5}); err != nil {
		t.Fatalf("printJSON: %v", err)
	}
	text := out.String()
	for _, want := range []string{`"chunks": 42`, `"count": 7`, `"label": "x"`, `"ratio": 0.5`} {
		if !strings.Contains(text, want) {
			t.Fatalf("printJSON output missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "7.0") || strings.Contains(text, "42.0") {
		t.Fatalf("integer fields degraded to float literals:\n%s", text)
	}
}

func TestPrintEfficacyCardRendersShareableReceipt(t *testing.T) {
	var out bytes.Buffer
	app := &application{stdout: &out}
	app.printEfficacyCard(map[string]any{
		"scope":                  "default",
		"grade":                  "A",
		"efficacy_score":         92,
		"total_sessions":         15,
		"evaluated_turns":        45,
		"adopt_rate":             86.7,
		"silence_rate":           40.0,
		"avg_round_cost_tokens":  271100,
		"today_net_saved_tokens": 1355500,
		"today_saved_rounds":     5,
		"net_saved_tokens":       27110000,
		"total_saved_rounds":     100,
	})
	card := out.String()
	for _, want := range []string{
		"ROLLING-MEMORY · LOCAL MEMORY ROI RECEIPT (0-Vector · 0-LLM)",
		"default · Grade A (92/100)",
		"15 sessions · 45 evaluated turns",
		"86.7% adopted · 40.0% noise silenced",
		"271100 tok/turn",
		"27110000 tok (100 blind turns avoided)",
		"Verified locally · github.com/rocareer/rolling-memory",
	} {
		if !strings.Contains(card, want) {
			t.Fatalf("efficacy card missing %q:\n%s", want, card)
		}
	}
}
