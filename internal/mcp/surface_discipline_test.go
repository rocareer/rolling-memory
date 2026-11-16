// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package mcp

// Surface-discipline pins: the single-tool contract and the behavior guidance
// embedded in the tools/list payload and the initialize instructions. Names and
// schema keys are covered by the golden scenarios (220-mcp / 209-mcp-caps);
// these markers cover the prose that steers the calling agent, which the
// goldens do not treat as contract.

import (
	"strings"
	"testing"
)

func TestToolsListIsExactlyRecall(t *testing.T) {
	if len(tools) != 1 {
		t.Fatalf("tools = %d, want exactly 1", len(tools))
	}
	tool, ok := tools[0].(map[string]any)
	if !ok {
		t.Fatalf("tool payload is %T, want map[string]any", tools[0])
	}
	if name := stringField(tool, "name"); name != "recall" {
		t.Fatalf("tool name = %q, want recall", name)
	}
	// Client-facing display title — harness UIs read tool.title as the label.
	if title := stringField(tool, "title"); title != "回忆" {
		t.Fatalf("tool title = %q, want 回忆", title)
	}
}

func TestToolDescriptionsCarryBehaviorGuidance(t *testing.T) {
	description := stringField(recallTool, "description")
	for _, marker := range []string{"zero model calls", "absent=true", "[<workspace>]", "exact literal tokens"} {
		if !strings.Contains(description, marker) {
			t.Errorf("recall description is missing marker %q", marker)
		}
	}
}

func TestInstructionsCarryRecallDoctrine(t *testing.T) {
	for _, marker := range []string{
		"persistent memory",
		"Task start (mandatory)",
		"Task stuck (mandatory)",
		"How to write `cue`",
		"[<workspace>]",
		"call recall",
		"absent: true",
		"traceable lead",
	} {
		if !strings.Contains(instructions, marker) {
			t.Errorf("initialize instructions are missing marker %q", marker)
		}
	}
}

func stringField(tool map[string]any, key string) string {
	value, _ := tool[key].(string)
	return value
}
