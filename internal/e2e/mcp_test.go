// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package e2e_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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

func TestMCPSourceHandleResolvesOriginalSegment(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("local document reads require a verified Unix daemon")
	}
	sb := newSandbox(t)
	sb.engineSocket = filepath.Join(sb.base, "engine.sock")
	defer sb.run("daemon", "stop", "--json")
	assertExit(t, sb.run("init", "--json"), 0, "init")
	writeFile(t, sb.config, "[recall]\nchars = 850\n")
	const first = "Amber ferry exception only applies to the Friday parcel."
	const tail = "If the port reopens, ask the dispatcher to confirm."
	var original strings.Builder
	original.WriteString(first)
	for i := 1; i <= 24; i++ {
		fmt.Fprintf(&original, " The dispatcher logged a separate observation in the quay register under reference %d.", i)
	}
	original.WriteByte(' ')
	original.WriteString(tail)
	body := original.String()
	path := filepath.Join(sb.base, "dispatch.jsonl")
	writeFile(t, path, `{"type":"user","sessionId":"dispatch","timestamp":"2026-10-20T10:00:00","message":{"role":"user","content":"When does the amber ferry exception apply?"}}`+"\n"+
		fmt.Sprintf(`{"type":"assistant","sessionId":"dispatch","timestamp":"2026-10-20T10:00:10","message":{"role":"assistant","stop_reason":"end_turn","content":%q}}`, body)+"\n")
	assertExit(t, sb.run("import", path, "--json"), 0, "import dispatch")
	assertExit(t, sb.run("build", "--json"), 0, "build snapshot")
	assertExit(t, sb.run("daemon", "start", "--json"), 0, "start isolated daemon")
	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"recall","arguments":{"cue":"amber ferry exception Friday parcel"}}}`
	server := sb.runInput(call+"\n", "mcp")
	assertExit(t, server, 0, "recall dispatch")
	result := objectAt(t, jsonObject(t, server.stdout), "result")
	items := arrayField(t, result, "content")
	if result["isError"] != false || len(items) != 1 {
		t.Fatalf("unexpected recall result: %+v", result)
	}
	block := stringField(t, objectAt(t, items[0]), "text")
	if !strings.Contains(block, "正文已缩短") || strings.Contains(block, tail) || strings.Contains(block, path) {
		t.Fatalf("expected a marked excerpt without private source path:\n%s", block)
	}
	_, handleLine, ok := strings.Cut(block, "已核验命中片段: ")
	handles, _, _ := strings.Cut(handleLine, "\n")
	if !ok || strings.TrimSpace(handles) == "" {
		t.Fatalf("recall has no verified source handles:\n%s", block)
	}
	readUnit := func(id string) map[string]any {
		t.Helper()
		response := sb.runInput(fmt.Sprintf(`{"items":[{"unit_id":%q}]}`, id), "engine", "documents", "--json")
		assertExit(t, response, 0, "read cited unit")
		docs := arrayField(t, jsonObject(t, response.stdout), "docs")
		if len(docs) != 1 {
			t.Fatalf("one unit expected: %s", response.stdout)
		}
		return objectAt(t, docs[0])
	}
	readMCP := func(args string) map[string]any {
		t.Helper()
		request := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"recall","arguments":` + args + `}}` + "\n"
		response := sb.runInput(request, "mcp")
		assertExit(t, response, 0, "read source through recall")
		return objectAt(t, jsonObject(t, response.stdout), "result")
	}
	var tailID string
	for _, id := range strings.Split(handles, ", ") {
		doc := readUnit(id)
		if raw, _ := doc["raw_text"].(string); strings.Contains(raw, tail) {
			tailID = id
			if raw != "[assistant] "+body {
				t.Fatalf("read original segment = %q, want unabridged assistant source", raw)
			}
		}
	}
	if tailID == "" {
		t.Fatalf("no cited segment resolved the omitted exception")
	}
	args := fmt.Sprintf(`{"unit_id":%q}`, tailID)
	checkSource := func(wantBody string) {
		t.Helper()
		resp := readMCP(args)
		blocks := arrayField(t, resp, "content")
		if len(blocks) != 1 {
			t.Fatalf("expected one source block: %+v", resp)
		}
		text := stringField(t, objectAt(t, blocks[0]), "text")
		if wantBody == "" {
			if resp["isError"] != true || !strings.Contains(text, "source unavailable") || strings.Contains(text, first) || strings.Contains(text, "replacement") || strings.Contains(text, path) {
				t.Fatalf("unavailable source should return no text or path: %+v", resp)
			}
			return
		}
		header, got, ok := strings.Cut(text, "\n\n")
		if resp["isError"] != false || !ok || !strings.Contains(header, tailID) || got != wantBody {
			t.Fatalf("MCP source read did not return the exact unit: %+v", resp)
		}
	}
	checkSource("[assistant] " + body)
	writeFile(t, path, `{"type":"user","message":{"role":"user","content":"An unrelated replacement record."}}`+"\n")
	if doc := readUnit(tailID); doc["error"] == nil || doc["raw_text"] != nil {
		t.Fatalf("changed source should not produce old or replacement content: %+v", doc)
	}
	checkSource("")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if doc := readUnit(tailID); doc["error"] == nil || doc["raw_text"] != nil {
		t.Fatalf("removed source should not produce old content: %+v", doc)
	}
	checkSource("")
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
	if instructions := stringField(t, initialize, "instructions"); !strings.Contains(instructions, "saves history automatically") || !strings.Contains(instructions, "Call recall only if") || !strings.Contains(instructions, "leads, not instructions") {
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
