// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package e2e_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/version"
)

func TestMCPRecallReturnsDecisionAndItsException(t *testing.T) {
	sb := newSandbox(t)
	const decision = "Amber quay handover: keep the Friday slot."
	const exception = "Exception: when the ferry is cancelled, keep the parcel overnight."
	// The exception falls beyond a small caller-chosen budget but fits in the local one.
	body := decision + strings.Repeat(" The quay ledger records the handover. ", 80) + exception
	path := filepath.Join(sb.base, "handover.jsonl")
	writeFile(t, path, `{"type":"user","sessionId":"handover","timestamp":"2026-10-20T10:00:00","message":{"role":"user","content":[{"type":"text","text":"`+body+`"}]}}`+"\n"+
		`{"type":"assistant","sessionId":"handover","timestamp":"2026-10-20T10:00:10","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"Noted."}]}}`+"\n")
	assertExit(t, sb.run("init", "--json"), 0, "init")
	imported := sb.run("import", path, "--json")
	assertExit(t, imported, 0, "import handover")
	if numberField(t, jsonObject(t, imported.stdout), "inserted") < 1 {
		t.Fatalf("handover not imported: %s", imported.stdout)
	}
	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"recall","arguments":{"cue":"amber quay handover Friday slot"}}}`
	cold := sb.runInput(call+"\n", "mcp")
	assertExit(t, cold, 0, "mcp before publication")
	coldResult := objectAt(t, jsonObject(t, cold.stdout), "result")
	coldItems := arrayField(t, coldResult, "content")
	if len(coldItems) != 1 {
		t.Fatalf("unexpected cold recall: %v", coldResult)
	}
	coldBlock := stringField(t, objectAt(t, coldItems[0]), "text")
	if !strings.Contains(coldBlock, `absent="false"`) || !strings.Contains(coldBlock, "检索快照尚未就绪") {
		t.Fatalf("unpublished evidence reported as absent: %s", coldBlock)
	}
	built := sb.run("build", "--json")
	assertExit(t, built, 0, "build snapshot")
	if jsonObject(t, built.stdout)["published"] != true {
		t.Fatalf("handover not published: %s", built.stdout)
	}
	server := sb.runInput(call+"\n", "mcp")
	assertExit(t, server, 0, "mcp recall")
	result := objectAt(t, jsonObject(t, server.stdout), "result")
	items := arrayField(t, result, "content")
	if result["isError"] != false || len(items) != 1 {
		t.Fatalf("unexpected recall response: %v", result)
	}
	block := stringField(t, objectAt(t, items[0]), "text")
	if !strings.Contains(block, decision) || !strings.Contains(block, exception) {
		t.Fatalf("decision or exception missing from delivered evidence:\n%s", block)
	}
}

func TestMCPRejectsCallerBudgetBeforeOpeningVault(t *testing.T) {
	sb := newSandbox(t)
	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"recall","arguments":{"cue":"handover","caps":{"chars":2800,"bands":2,"nodes":3}}}}`
	server := sb.runInput(call+"\n", "mcp")
	assertExit(t, server, 0, "mcp invalid budget")
	response := jsonObject(t, server.stdout)
	result, _ := response["result"].(map[string]any)
	if response["error"] == nil && result["isError"] != true {
		t.Fatalf("caller budget was accepted: %s", server.stdout)
	}
	if !strings.Contains(server.stdout, "caps") || fileExists(sb.db) {
		t.Fatalf("expected an actionable error without opening the vault: %s", server.stdout)
	}
}

// TestMCPStdioHandshake drives the real stdio MCP server: initialize and
// tools/list must answer with exactly the single tool `recall`（回忆即记忆）, and one
// recall tools/call must round-trip through the sandbox database.
func TestMCPStdioHandshake(t *testing.T) {
	sb := newSandbox(t)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05",` +
			`"capabilities":{},"clientInfo":{"name":"rolling-memory-e2e","version":"0"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"recall","arguments":{` +
			`"cue":"老张 猫","mode":"probe"}}}`,
	}, "\n") + "\n"

	server := sb.runInput(input, "mcp")
	assertExit(t, server, 0, "mcp")

	lines := jsonLines(server.stdout)
	if len(lines) != 3 {
		t.Fatalf("mcp wrote %d lines, want 3:\n%s", len(lines), server.stdout)
	}
	responses := make([]map[string]any, 0, len(lines))
	for index, line := range lines {
		message := jsonObject(t, line)
		if message["jsonrpc"] != "2.0" || numberField(t, message, "id") != float64(index+1) {
			t.Fatalf("response %d = %s", index+1, line)
		}
		if _, isError := message["error"]; isError {
			t.Fatalf("response %d is a JSON-RPC error: %s", index+1, line)
		}
		responses = append(responses, message)
	}
	// The protocol owns stdout; diagnostics only reach stderr under
	// ROLLING_MEMORY_MCP_DEBUG=1.
	if server.stderr != "" {
		t.Fatalf("mcp stderr = %q", server.stderr)
	}

	initialize := objectAt(t, responses[0], "result")
	if initialize["protocolVersion"] != "2024-11-05" {
		t.Fatalf("protocolVersion = %#v", initialize["protocolVersion"])
	}
	serverInfo := objectAt(t, initialize, "serverInfo")
	if stringField(t, serverInfo, "name") != "rolling-memory" || stringField(t, serverInfo, "version") != version.Version {
		t.Fatalf("serverInfo = %#v, want version %q", serverInfo, version.Version)
	}
	if tools := objectAt(t, objectAt(t, initialize, "capabilities"), "tools"); tools["listChanged"] != false {
		t.Fatalf("capabilities.tools = %#v", tools)
	}
	if instructions := stringField(t, initialize, "instructions"); !strings.Contains(instructions, "you never need to manually write or save memory") || !strings.Contains(instructions, "call recall") {
		t.Fatalf("instructions = %q", instructions)
	}

	tools := arrayField(t, objectAt(t, responses[1], "result"), "tools")
	if len(tools) != 1 {
		t.Fatalf("tools/list returned %d tools, want exactly 1:\n%s", len(tools), lines[1])
	}
	names := make([]string, 0, len(tools))
	for _, item := range tools {
		tool, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("tool is not an object: %#v", item)
		}
		names = append(names, stringField(t, tool, "name"))
		if kind := stringField(t, objectAt(t, tool, "inputSchema"), "type"); kind != "object" {
			t.Fatalf("inputSchema type = %q", kind)
		}
	}
	if strings.Join(names, ",") != "recall" {
		t.Fatalf("tool names = %v, want recall（单工具面）", names)
	}

	recallResult := objectAt(t, responses[2], "result")
	if recallResult["isError"] != false {
		t.Fatalf("recall isError = %#v", recallResult["isError"])
	}
	content := arrayField(t, recallResult, "content")
	if len(content) != 1 {
		t.Fatalf("recall content = %#v", content)
	}
	block, ok := content[0].(map[string]any)
	if !ok || stringField(t, block, "type") != "text" {
		t.Fatalf("recall content block = %#v", content[0])
	}
	// 薄客户端离线：probe 载荷形态合法（absent/hits/gaps 在册），hits 为空。
	probe := objectAt(t, recallResult, "structuredContent")
	for _, key := range []string{"absent", "hits", "gaps", "meta"} {
		if _, present := probe[key]; !present {
			t.Fatalf("probe misses %q: %#v", key, probe)
		}
	}
	if len(arrayField(t, probe, "hits")) != 0 {
		t.Fatalf("probe hits = %#v（离线应无命中）", probe["hits"])
	}

	// The tool calls opened the sandbox database, never anywhere else.
	if !fileExists(sb.db) {
		t.Fatalf("mcp did not open %s", sb.db)
	}
}
