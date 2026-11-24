// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocareer/rolling-memory/internal/clock"
	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/memory/facade"
	"github.com/rocareer/rolling-memory/internal/text"
	"github.com/rocareer/rolling-memory/internal/version"
)

func TestHTTPHostGuard(t *testing.T) {
	server := &HTTPServer{}
	for _, path := range []string{"/mcp", "/sse", "/messages"} {
		for _, host := range []string{"evil.example:8765", "localhost.evil.example", "127.0.0.1.evil.example", "192.168.1.1:8765", "", "localhost:abc", "localhost:0", "localhost:8765@evil.example"} {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Host = host
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s Host %q: got %d, want 403", path, host, rec.Code)
			}
		}
	}
	for _, host := range []string{"localhost", "LOCALHOST:8765", "localhost.:8765", "127.0.0.1:8765", "127.0.0.2", "[::1]:8765", "[::1]"} {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
		req.Host = host
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"result"`) {
			t.Fatalf("Host %q: got %d %s", host, rec.Code, rec.Body.String())
		}
	}
}

func TestHTTPOriginGuard(t *testing.T) {
	for _, testCase := range []struct {
		origin string
		want   bool
	}{
		{"", true}, {"http://localhost:8765", true}, {"https://[::1]", true},
		{"http://127.0.0.2:1234", true}, {"http://evil.example", false},
		{"http://localhost:8765@evil.example", false}, {"http://localhost:abc", false},
		{"http://localhost:8765/path", false}, {"null", false},
	} {
		if got := originAllowed(testCase.origin); got != testCase.want {
			t.Errorf("originAllowed(%q) = %v, want %v", testCase.origin, got, testCase.want)
		}
	}
}

func TestHTTPRejectsMultipleOrigins(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	req.Host = "localhost:8765"
	req.Header.Add("Origin", "http://localhost:8765")
	req.Header.Add("Origin", "http://evil.example")
	rec := httptest.NewRecorder()
	(&HTTPServer{}).Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("multiple origins: got %d, want 403", rec.Code)
	}
}

func TestHTTPRejectsNonLoopbackListeners(t *testing.T) {
	for _, address := range []string{":0", "0.0.0.0:0", "[::]:0", "192.0.2.1:0", "evil.example:0"} {
		server := &HTTPServer{Addr: address}
		if err := server.ListenAndServe(); err == nil || !strings.Contains(err.Error(), "loopback") {
			t.Errorf("ListenAndServe(%q) = %v, want loopback error", address, err)
		}
	}
	listener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := (&HTTPServer{}).Serve(listener); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("Serve(wildcard) = %v, want loopback error", err)
	}
}

func TestHTTPAcceptsLoopbackListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &HTTPServer{Err: io.Discard}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	response, err := http.Post("http://"+listener.Addr().String()+"/mcp", "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	if err != nil {
		listener.Close()
		<-done
		t.Fatal(err)
	}
	response.Body.Close()
	listener.Close()
	<-done
	if response.StatusCode != http.StatusOK {
		t.Fatalf("loopback listener: %s", response.Status)
	}
}

func TestHTTPStreamableAndLegacySSE(t *testing.T) {
	server := &HTTPServer{}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	response, err := http.Post(httpServer.URL+"/mcp", "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || response.Header.Get(HTTPSessionHeader) == "" {
		t.Fatalf("initialize: %s, session %q", response.Status, response.Header.Get(HTTPSessionHeader))
	}
	response.Body.Close()

	response, err = http.Get(httpServer.URL + "/sse")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	event, err := reader.ReadString('\n')
	if err != nil || event != "event: endpoint\n" {
		t.Fatalf("SSE event = %q, err = %v", event, err)
	}
	line, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "data: "+httpServer.URL+"/messages?sessionId=") {
		t.Fatalf("SSE endpoint = %q, err = %v", line, err)
	}
	endpoint := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
	posted, err := http.Post(endpoint, "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"ping"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer posted.Body.Close()
	if posted.StatusCode != http.StatusAccepted {
		t.Fatalf("SSE post: %s", posted.Status)
	}
	for {
		line, err = reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(line, "data: ") {
			if !strings.Contains(line, `"id":2`) || !strings.Contains(line, `"result"`) {
				t.Fatalf("SSE response = %q", line)
			}
			break
		}
	}
}

const testNow = int64(1793102400)

func TestMain(m *testing.M) {
	time.Local = time.UTC
	os.Exit(m.Run())
}

// rawLine is written to the server verbatim (malformed input coverage).
type rawLine string

func newMemory(t *testing.T) *facade.Memory {
	return newVaultMemory(t)
}

func newVaultMemory(t *testing.T) *facade.Memory {
	t.Helper()
	restore := clock.SetForTest(time.Unix(testNow, 0), 1_000_000_000)
	t.Cleanup(restore)
	directory := t.TempDir()
	dbPath := filepath.Join(directory, "memory.db")
	configPath := filepath.Join(directory, "config.toml")
	mode := "local"
	scope := "mem:test"
	reader := "deliverable"
	cfg, err := config.Load(config.LoadOptions{
		Path: &configPath, DB: &dbPath, Mode: &mode, Scope: &scope, Reader: &reader,
		Env: map[string]string{},
	})
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	cfg.Log = filepath.Join(directory, "state", "rolling-memory.log")
	memory, err := facade.New(cfg)
	if err != nil {
		t.Fatalf("api: %v", err)
	}
	t.Cleanup(func() { memory.Close() })
	return memory
}

func runSession(t *testing.T, memory *facade.Memory, messages ...any) []any {
	t.Helper()
	var input strings.Builder
	for _, message := range messages {
		if raw, ok := message.(rawLine); ok {
			input.WriteString(string(raw))
		} else {
			encoded, err := json.Marshal(message)
			if err != nil {
				t.Fatalf("encode request: %v", err)
			}
			input.WriteString(string(encoded))
		}
		input.WriteString("\n")
	}
	var out bytes.Buffer
	if err := Serve(strings.NewReader(input.String()), &out, memory); err != nil {
		t.Fatalf("serve: %v", err)
	}
	return decodeLines(t, out.String())
}

func decodeLines(t *testing.T, text string) []any {
	t.Helper()
	responses := []any{}
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			continue
		}
		decoder := json.NewDecoder(strings.NewReader(line))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			t.Fatalf("response is not JSON: %v (line %q)", err, line)
		}
		responses = append(responses, value)
	}
	return responses
}

func asMap(t *testing.T, value any) map[string]any {
	t.Helper()
	typed, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("expected an object, got %T", value)
	}
	return typed
}

func resultOf(t *testing.T, response any) map[string]any {
	t.Helper()
	result, ok := asMap(t, response)["result"]
	if !ok {
		t.Fatalf("response has no result: %#v", response)
	}
	return asMap(t, result)
}

func errorOf(t *testing.T, response any) map[string]any {
	t.Helper()
	code, ok := asMap(t, response)["error"]
	if !ok {
		t.Fatalf("response has no error: %#v", response)
	}
	return asMap(t, code)
}

func errorCode(t *testing.T, response any) int {
	t.Helper()
	return asInt(t, errorOf(t, response)["code"])
}

func asInt(t *testing.T, value any) int {
	t.Helper()
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			t.Fatalf("not an integer: %v", value)
		}
		return int(parsed)
	case int:
		return typed
	}
	t.Fatalf("not a number: %#v", value)
	return 0
}

func asString(t *testing.T, value any) string {
	t.Helper()
	typed, ok := value.(string)
	if !ok {
		t.Fatalf("not a string: %#v", value)
	}
	return typed
}

func structuredOf(t *testing.T, response any) map[string]any {
	t.Helper()
	return asMap(t, resultOf(t, response)["structuredContent"])
}

func textOf(t *testing.T, response any) string {
	t.Helper()
	content, ok := resultOf(t, response)["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("response has no content: %#v", response)
	}
	entry := asMap(t, content[0])
	if asString(t, entry["type"]) != "text" {
		t.Fatalf("content type: %#v", entry["type"])
	}
	return asString(t, entry["text"])
}

func isError(t *testing.T, response any) bool {
	t.Helper()
	value, ok := resultOf(t, response)["isError"].(bool)
	if !ok {
		t.Fatalf("response has no isError: %#v", response)
	}
	return value
}

func toolCallMessage(id int, name string, arguments map[string]any) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": arguments},
	}
}

func TestLocalRecallNeverContactsBrain(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer server.Close()
	t.Setenv("ROLLING_MEMORY_BRAIN_URL", server.URL+"/open/memory/v1")
	t.Setenv("ROLLING_MEMORY_BRAIN_KEY", "test-key")
	memory := newMemory(t)
	responses := runSession(t, memory, toolCallMessage(1, "recall", map[string]any{"cue": "local sentinel"}))
	if len(responses) != 1 || isError(t, responses[0]) {
		t.Fatalf("recall responses: %#v", responses)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("local recall made %d remote requests", got)
	}
}

func TestProtocolAndSingleTool(t *testing.T) {
	memory := newMemory(t)
	responses := runSession(t, memory,
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize",
			"params": map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}}},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"},
		toolCallMessage(3, "recall", map[string]any{"cue": "老张 小白"}),
		toolCallMessage(4, "recall", map[string]any{"cue": "zzzqqqxxx", "mode": "probe"}),
		map[string]any{"jsonrpc": "2.0", "id": 6, "method": "nope"},
		map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"},
		map[string]any{"jsonrpc": "2.0", "id": 7, "method": "ping"},
		rawLine("{"),
	)
	if len(responses) != 7 {
		t.Fatalf("responses: got %d, want 7 (notification must not answer)", len(responses))
	}

	init := resultOf(t, responses[0])
	serverInfo := asMap(t, init["serverInfo"])
	if asString(t, serverInfo["name"]) != "rolling-memory" {
		t.Fatalf("serverInfo.name = %v", serverInfo["name"])
	}
	if asString(t, serverInfo["version"]) != version.Version {
		t.Fatalf("serverInfo.version = %v", serverInfo["version"])
	}
	if asString(t, init["protocolVersion"]) != "2024-11-05" {
		t.Fatalf("protocolVersion = %v", init["protocolVersion"])
	}
	capabilities := asMap(t, asMap(t, init["capabilities"])["tools"])
	if changed, _ := capabilities["listChanged"].(bool); changed {
		t.Fatalf("capabilities.tools.listChanged = %v", capabilities["listChanged"])
	}
	if got := asString(t, init["instructions"]); got != instructions {
		t.Fatalf("initialize instructions = %q, want current instructions", got)
	}

	list := resultOf(t, asMap(t, responses[1]))
	listed, ok := list["tools"].([]any)
	if !ok || len(listed) != 1 {
		t.Fatalf("tools: %#v", list["tools"])
	}
	if name := asString(t, asMap(t, listed[0])["name"]); name != "recall" {
		t.Fatalf("tool name: %v", name)
	}
	assertRecallSchema(t, asMap(t, asMap(t, listed[0])["inputSchema"]))

	recalled := responses[2]
	if isError(t, recalled) {
		t.Fatalf("recall failed: %q", textOf(t, recalled))
	}
	if absent, ok := structuredOf(t, recalled)["absent"].(bool); !ok || !absent {
		t.Fatalf("recall absent on empty memory: %#v", structuredOf(t, recalled)["absent"])
	}

	probe := responses[3]
	if isError(t, probe) {
		t.Fatalf("probe failed: %q", textOf(t, probe))
	}
	if absent, ok := structuredOf(t, probe)["absent"].(bool); !ok || !absent {
		t.Fatalf("probe absent: %#v", structuredOf(t, probe)["absent"])
	}
	var probeText map[string]any
	if err := json.Unmarshal([]byte(textOf(t, probe)), &probeText); err != nil {
		t.Fatalf("probe text is not JSON: %v (%q)", err, textOf(t, probe))
	}
	if _, ok := probeText["hits"]; !ok {
		t.Fatalf("probe text hits: %q", textOf(t, probe))
	}
	if !strings.HasPrefix(textOf(t, probe), "{\n  ") || !strings.Contains(textOf(t, probe), "\n  \"absent\": true") {
		t.Fatalf("probe text must be indent=2 JSON: %q", textOf(t, probe))
	}

	if code := errorCode(t, responses[4]); code != codeMethodNotFound {
		t.Fatalf("unknown method code: %d", code)
	}
	if message := asString(t, errorOf(t, responses[4])["message"]); message != "method not found: nope" {
		t.Fatalf("unknown method message: %q", message)
	}

	ping := responses[5]
	if id := asInt(t, asMap(t, ping)["id"]); id != 7 {
		t.Fatalf("ping id: %v", asMap(t, ping)["id"])
	}
	if len(resultOf(t, ping)) != 0 {
		t.Fatalf("ping result: %#v", resultOf(t, ping))
	}

	malformed := responses[6]
	if code := errorCode(t, malformed); code != codeParseError {
		t.Fatalf("parse error code: %d", code)
	}
	if message := asString(t, errorOf(t, malformed)["message"]); message != "parse error: Expecting property name enclosed in double quotes" {
		t.Fatalf("parse error message: %q", message)
	}
	if id, present := asMap(t, malformed)["id"]; !present || id != nil {
		t.Fatalf("parse error id: %#v", id)
	}
}

func assertRecallSchema(t *testing.T, schema map[string]any) {
	t.Helper()
	properties := asMap(t, schema["properties"])
	assertKeys(t, "recall properties", properties, []string{
		"cue", "scope", "reader", "mode", "delivery_window", "offset",
	})
	if keys := stringList(t, asMap(t, properties["mode"])["enum"]); strings.Join(keys, ",") != "full,probe,recent" {
		t.Fatalf("mode enum: %v", keys)
	}
	if keys := stringList(t, asMap(t, properties["reader"])["enum"]); strings.Join(keys, ",") != "owner,deliverable" {
		t.Fatalf("reader enum: %v", keys)
	}
}

func assertKeys(t *testing.T, label string, values map[string]any, want []string) {
	t.Helper()
	found := make([]string, 0, len(values))
	for key := range values {
		found = append(found, key)
	}
	if len(found) != len(want) {
		t.Fatalf("%s: got %d keys %v, want %v", label, len(found), found, want)
	}
	for _, key := range want {
		if _, ok := values[key]; !ok {
			t.Fatalf("%s: missing %q (have %v)", label, key, found)
		}
	}
}

func stringList(t *testing.T, value any) []string {
	t.Helper()
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("not an array: %#v", value)
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		result = append(result, asString(t, item))
	}
	return result
}

func TestUnknownToolAndParamErrors(t *testing.T) {
	memory := newMemory(t)
	responses := runSession(t, memory,
		toolCallMessage(1, "unknown", map[string]any{}),
		toolCallMessage(2, "remember", map[string]any{"content": "我喜欢猫。"}),
		map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": 5},
	)
	if len(responses) != 3 {
		t.Fatalf("responses: %d", len(responses))
	}
	if code := errorCode(t, responses[0]); code != codeInvalidParams {
		t.Fatalf("unknown tool code: %d", code)
	}
	if message := asString(t, errorOf(t, responses[0])["message"]); message != "unknown tool: unknown" {
		t.Fatalf("unknown tool message: %q", message)
	}
	// The retired write tool is gone: the surface exposes recall only.
	if message := asString(t, errorOf(t, responses[1])["message"]); message != "unknown tool: remember" {
		t.Fatalf("remember tool message: %q", message)
	}
	if code := errorCode(t, responses[2]); code != codeInvalidParams {
		t.Fatalf("params code: %d", code)
	}
	if message := asString(t, errorOf(t, responses[2])["message"]); message != "params must be an object" {
		t.Fatalf("params message: %q", message)
	}
}

func TestRecallRejectsRetiredCaps(t *testing.T) {
	memory := newMemory(t)
	responses := runSession(t, memory,
		toolCallMessage(1, "recall", map[string]any{"cue": "x", "caps": 5}),
		toolCallMessage(2, "recall", map[string]any{"cue": "x", "caps": "wide"}),
		toolCallMessage(3, "recall", map[string]any{"cue": "x", "caps": nil}),
		toolCallMessage(4, "recall", map[string]any{"cue": "x", "caps": map[string]any{}}),
		toolCallMessage(5, "recall", map[string]any{"cue": "x", "caps": map[string]any{"chars": "bad"}}),
		toolCallMessage(6, "recall", map[string]any{"cue": "x", "caps": map[string]any{"chars": 0}}),
		toolCallMessage(7, "recall", map[string]any{"cue": "x", "offset": "abc"}),
		toolCallMessage(8, "recall", map[string]any{"cue": "x", "mode": "probe", "caps": 5}),
	)
	if len(responses) != 8 {
		t.Fatalf("responses: %d", len(responses))
	}
	for i, response := range responses {
		if !isError(t, response) {
			t.Fatalf("response %d accepted invalid arguments: %q", i, textOf(t, response))
		}
		want := capsRetiredMessage
		if i == 6 {
			want = "invalid literal for int() with base 10: 'abc'"
		}
		if text := textOf(t, response); !strings.Contains(text, want) {
			t.Fatalf("response %d: got %q; want %q", i, text, want)
		}
	}
}

func TestRecallRetiredDeliverySwitchesIgnored(t *testing.T) {
	request, err := recallRequest(map[string]any{
		"cue":         "x",
		"result_unit": "ITEM",
		"rank":        false,
	})
	if err != nil {
		t.Fatalf("recallRequest: %v", err)
	}
	if request.Cue != "x" {
		t.Fatalf("cue = %q", request.Cue)
	}
}

func TestBatchAndRequestValidation(t *testing.T) {
	memory := newMemory(t)
	responses := runSession(t, memory,
		rawLine(`[{"jsonrpc":"2.0","id":1,"method":"initialize"},{"jsonrpc":"2.0","method":"notifications/initialized"},{"jsonrpc":"2.0","id":2,"method":"ping"}]`),
		rawLine(`[5]`),
		rawLine(`[]`),
		rawLine(``),
		rawLine(`{"jsonrpc":"1.0","id":3,"method":"ping"}`),
		rawLine(`{"id":4,"method":"ping"}`),
		rawLine(`{"jsonrpc":"2.0","id":null,"method":"ping"}`),
		rawLine(`{"jsonrpc":"2.0","id":5,"method":5}`),
		rawLine(`not json`),
		rawLine(`{"id":6,"method":"ping"} x`),
		rawLine(`"abc`),
	)
	if len(responses) != 9 {
		t.Fatalf("responses: got %d, want 9 (empty batch and blank line stay silent)", len(responses))
	}
	batch, ok := responses[0].([]any)
	if !ok || len(batch) != 2 {
		t.Fatalf("batch: %#v", responses[0])
	}
	if asInt(t, asMap(t, batch[0])["id"]) != 1 || asInt(t, asMap(t, batch[1])["id"]) != 2 {
		t.Fatalf("batch ids: %#v", batch)
	}
	invalidBatch, ok := responses[1].([]any)
	if !ok || len(invalidBatch) != 1 {
		t.Fatalf("invalid batch: %#v", responses[1])
	}
	if code := errorCode(t, invalidBatch[0]); code != codeInvalidRequest {
		t.Fatalf("invalid batch code: %d", code)
	}
	if id := asMap(t, invalidBatch[0])["id"]; id != nil {
		t.Fatalf("invalid batch id: %#v", id)
	}
	if code := errorCode(t, responses[2]); code != codeInvalidRequest {
		t.Fatalf("jsonrpc code: %d", code)
	}
	if len(resultOf(t, responses[3])) != 0 {
		t.Fatalf("missing jsonrpc must be accepted: %#v", responses[3])
	}
	if id, present := asMap(t, responses[4])["id"]; !present || id != nil {
		t.Fatalf("null id: %#v", asMap(t, responses[4])["id"])
	}
	if code := errorCode(t, responses[5]); code != codeInvalidRequest {
		t.Fatalf("non-string method code: %d", code)
	}
	if message := asString(t, errorOf(t, responses[6])["message"]); message != "parse error: Expecting value" {
		t.Fatalf("parse error message: %q", message)
	}
	if message := asString(t, errorOf(t, responses[7])["message"]); message != "parse error: Extra data" {
		t.Fatalf("extra data message: %q", message)
	}
	if message := asString(t, errorOf(t, responses[8])["message"]); message != "parse error: Unterminated string starting at" {
		t.Fatalf("unterminated string message: %q", message)
	}
}

func TestLazyMemoryReportsCachedFailure(t *testing.T) {
	directory := t.TempDir()
	// A directory cannot be opened as a SQLite database, so Memory() fails.
	databasePath := filepath.Join(directory, "not-a-database")
	if err := os.MkdirAll(databasePath, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv("ROLLING_MEMORY_CONFIG", filepath.Join(directory, "config.toml"))
	t.Setenv("ROLLING_MEMORY_DB", databasePath)
	t.Setenv("ROLLING_MEMORY_LOG", filepath.Join(directory, "state", "rolling-memory.log"))
	t.Setenv("ROLLING_MEMORY_SCOPE", "mem:test")
	t.Setenv("ROLLING_MEMORY_MODE", "local")
	t.Setenv("ROLLING_MEMORY_READER", "deliverable")
	t.Setenv("ROLLING_MEMORY_MCP_DEBUG", "")

	var input strings.Builder
	input.WriteString(`{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n")
	input.WriteString(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n")
	input.WriteString(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"recall","arguments":{"cue":"x"}}}` + "\n")
	input.WriteString(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"recall","arguments":{"cue":"y"}}}` + "\n")
	var out bytes.Buffer
	if err := Serve(strings.NewReader(input.String()), &out, nil); err != nil {
		t.Fatalf("serve: %v", err)
	}
	responses := decodeLines(t, out.String())
	if len(responses) != 4 {
		t.Fatalf("responses: %d", len(responses))
	}
	// initialize and tools/list must not open the database.
	if _, ok := resultOf(t, responses[0])["serverInfo"]; !ok {
		t.Fatalf("initialize: %#v", responses[0])
	}
	if _, ok := resultOf(t, responses[1])["tools"]; !ok {
		t.Fatalf("tools/list: %#v", responses[1])
	}
	for _, response := range responses[2:] {
		if !isError(t, response) {
			t.Fatalf("tool call must fail: %#v", response)
		}
		if text := textOf(t, response); !strings.HasPrefix(text, "rolling-memory error: ") {
			t.Fatalf("tool error text: %q", text)
		}
	}
}

func TestLazyMemoryOpensOnFirstToolCall(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("ROLLING_MEMORY_CONFIG", filepath.Join(directory, "config.toml"))
	t.Setenv("ROLLING_MEMORY_DB", filepath.Join(directory, "lazy", "memory.db"))
	t.Setenv("ROLLING_MEMORY_LOG", filepath.Join(directory, "state", "rolling-memory.log"))
	t.Setenv("ROLLING_MEMORY_SCOPE", "mem:test")
	t.Setenv("ROLLING_MEMORY_MODE", "local")
	t.Setenv("ROLLING_MEMORY_READER", "deliverable")
	t.Setenv("ROLLING_MEMORY_MCP_DEBUG", "")
	restore := clock.SetForTest(time.Unix(testNow, 0), 1_000_000_000)
	t.Cleanup(restore)

	lazy := NewLazyMemory()
	t.Cleanup(func() { lazy.Close() })
	var input strings.Builder
	input.WriteString(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n")
	input.WriteString(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"recall","arguments":{"cue":"我喜欢猫。","mode":"probe"}}}` + "\n")
	var out bytes.Buffer
	server := &Server{In: strings.NewReader(input.String()), Out: &out, Lazy: lazy}
	if err := server.Run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	responses := decodeLines(t, out.String())
	if len(responses) != 2 {
		t.Fatalf("responses: %d", len(responses))
	}
	recalled := responses[1]
	if isError(t, recalled) {
		t.Fatalf("recall failed: %q", textOf(t, recalled))
	}
	if absent, ok := structuredOf(t, recalled)["absent"].(bool); !ok || !absent {
		t.Fatalf("absent: %#v", structuredOf(t, recalled)["absent"])
	}
	if _, err := os.Stat(filepath.Join(directory, "lazy", "memory.db")); err != nil {
		t.Fatalf("database was not created: %v", err)
	}
}

func TestDebugSwitch(t *testing.T) {
	memory := newMemory(t)
	request := toolCallMessage(1, "recall", map[string]any{"cue": "x", "caps": 5})

	t.Setenv("ROLLING_MEMORY_MCP_DEBUG", "")
	var quiet bytes.Buffer
	server := &Server{In: strings.NewReader(mustJSON(t, request) + "\n"), Out: &bytes.Buffer{}, Err: &quiet, Memory: memory}
	if err := server.Run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	if quiet.Len() != 0 {
		t.Fatalf("stderr without debug: %q", quiet.String())
	}

	t.Setenv("ROLLING_MEMORY_MCP_DEBUG", "1")
	var noisy bytes.Buffer
	server = &Server{In: strings.NewReader(mustJSON(t, request) + "\n"), Out: &bytes.Buffer{}, Err: &noisy, Memory: memory}
	if err := server.Run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(noisy.String(), capsRetiredMessage) {
		t.Fatalf("stderr with debug: %q", noisy.String())
	}
	if !strings.Contains(noisy.String(), "goroutine ") {
		t.Fatalf("stderr with debug should carry a stack: %q", noisy.String())
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return string(encoded)
}

func TestNormalizeJSONValue(t *testing.T) {
	converted := normalizeJSONValue(map[string]any{
		"turn":       json.Number("5"),
		"confidence": json.Number("0.9"),
		"exponent":   json.Number("1e3"),
		"huge":       json.Number("9223372036854775808"),
		"list":       []any{json.Number("-2"), "x"},
	}).(map[string]any)
	if value, ok := converted["turn"].(int64); !ok || value != 5 {
		t.Fatalf("turn: %#v", converted["turn"])
	}
	if value, ok := converted["confidence"].(float64); !ok || value != 0.9 {
		t.Fatalf("confidence: %#v", converted["confidence"])
	}
	if value, ok := converted["exponent"].(float64); !ok || value != 1000 {
		t.Fatalf("exponent: %#v", converted["exponent"])
	}
	if _, ok := converted["huge"].(json.Number); !ok {
		t.Fatalf("huge: %#v", converted["huge"])
	}
	if value, ok := converted["list"].([]any)[0].(int64); !ok || value != -2 {
		t.Fatalf("list: %#v", converted["list"])
	}
}

func TestInvalidUTF8IsATransportError(t *testing.T) {
	var out bytes.Buffer
	err := Serve(strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\",\"x\":\"\xff\"}\n"), &out, nil)
	if err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("error: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("nothing must be written: %q", out.String())
	}
}

func TestPythonValueSemantics(t *testing.T) {
	if pyStr(json.Number("5")) != "5" || pyStr(json.Number("1e3")) != "1000.0" || pyStr(json.Number("1e-7")) != "1e-07" {
		t.Fatalf("pyStr numbers: %q %q %q", pyStr(json.Number("5")), pyStr(json.Number("1e3")), pyStr(json.Number("1e-7")))
	}
	if pyStr(true) != "True" || pyStr(nil) != "None" {
		t.Fatalf("pyStr scalars: %q %q", pyStr(true), pyStr(nil))
	}
	container := map[string]any{"a": json.Number("1"), "b": "x'y"}
	if got := pyStr(container); got != `{'a': 1, 'b': "x'y"}` {
		t.Fatalf("pyStr dict: %q", got)
	}
	if got := pyStr([]any{json.Number("1"), "a", nil}); got != "[1, 'a', None]" {
		t.Fatalf("pyStr list: %q", got)
	}
	if pyTruthy(json.Number("0")) || pyTruthy("") || pyTruthy([]any{}) || pyTruthy(map[string]any{}) || pyTruthy(nil) {
		t.Fatal("falsy values reported truthy")
	}
	if !pyTruthy(json.Number("0.5")) || !pyTruthy("x") || !pyTruthy([]any{1}) {
		t.Fatal("truthy values reported falsy")
	}
	if value, err := pyInt(" 12 "); err != nil || value != 12 {
		t.Fatalf("pyInt spaces: %v %v", value, err)
	}
	if value, err := pyInt("1_000"); err != nil || value != 1000 {
		t.Fatalf("pyInt underscores: %v %v", value, err)
	}
	if value, err := pyInt(json.Number("12.9")); err != nil || value != 12 {
		t.Fatalf("pyInt float: %v %v", value, err)
	}
	if value, err := pyInt(true); err != nil || value != 1 {
		t.Fatalf("pyInt bool: %v %v", value, err)
	}
	if _, err := pyInt("abc"); err == nil || err.Error() != "invalid literal for int() with base 10: 'abc'" {
		t.Fatalf("pyInt error: %v", err)
	}
	if _, err := pyInt([]any{}); err == nil ||
		err.Error() != "int() argument must be a string, a bytes-like object or a real number, not 'list'" {
		t.Fatalf("pyInt type error: %v", err)
	}
	if pyTypeName(json.Number("5")) != "int" || pyTypeName(json.Number("5.0")) != "float" || pyTypeName(nil) != "NoneType" {
		t.Fatalf("pyTypeName: %q %q %q", pyTypeName(json.Number("5")), pyTypeName(json.Number("5.0")), pyTypeName(nil))
	}
	if text.FirstRunes("老张abc", 2) != "老张" || text.FirstRunes("ab", 8) != "ab" {
		t.Fatalf("firstChars: %q %q", text.FirstRunes("老张abc", 2), text.FirstRunes("ab", 8))
	}
}

func TestHandleReturnsResponses(t *testing.T) {
	memory := newMemory(t)
	notification, err := Handle(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}, memory, nil)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if notification != nil {
		t.Fatalf("notification answered: %#v", notification)
	}
	response, err := Handle(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "shutdown"}, memory, nil)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(resultOf(t, response)) != 0 {
		t.Fatalf("shutdown: %#v", response)
	}
}

// fastWatchdog shrinks the watchdog tick so retirement tests finish in
// milliseconds instead of waiting out the production tick. The knob is atomic:
// a server started by an earlier test can still read it while this test runs.
func fastWatchdog(t *testing.T) {
	t.Helper()
	previous := watchdogTickNanos.Swap(int64(20 * time.Millisecond))
	t.Cleanup(func() { watchdogTickNanos.Store(previous) })
}

func TestIdleSessionReleasesVaultHandleAndStaysAlive(t *testing.T) {
	t.Setenv(debugEnv, "1")
	fastWatchdog(t)
	pipeReader, pipeWriter := io.Pipe()
	var out, debug bytes.Buffer
	retired := make(chan int, 1)
	server := &Server{
		In:          pipeReader,
		Out:         &out,
		Err:         &debug,
		IdleRelease: 120 * time.Millisecond,
		exitFn:      func(code int) { retired <- code },
	}
	done := make(chan error, 1)
	go func() { done <- server.Run() }()
	// Serve one request so the session looks live, then go silent.
	request := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}` + "\n"
	if _, err := pipeWriter.Write([]byte(request)); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Silence must release the handle, never end the session: hosts keep the
	// pipe open and may report a clean process exit as a transport error.
	select {
	case code := <-retired:
		t.Fatalf("watchdog retired an idle session with code %d", code)
	case <-time.After(600 * time.Millisecond):
	}
	ping := `{"jsonrpc":"2.0","id":2,"method":"ping"}` + "\n"
	if _, err := pipeWriter.Write([]byte(ping)); err != nil {
		t.Fatalf("write after idle: %v", err)
	}
	pipeWriter.Close()
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := strings.Count(out.String(), "\n"); got != 2 {
		t.Fatalf("responses = %d, want 2 (initialize + ping)", got)
	}
	if strings.Contains(debug.String(), "exiting") {
		t.Fatalf("stderr = %q, want no retirement", debug.String())
	}
}

func TestReleaseIdleDropsLazyHandle(t *testing.T) {
	lazy := &LazyMemory{memory: newMemory(t)}
	var inFlight atomic.Int64
	server := &Server{}
	if !server.releaseIdle(&inFlight, lazy) {
		t.Fatalf("releaseIdle = false, want true")
	}
	if lazy.Opened() {
		t.Fatalf("vault handle stayed open after idle release")
	}
}

func TestIdleReleaseResetsOnActivity(t *testing.T) {
	fastWatchdog(t)
	pipeReader, pipeWriter := io.Pipe()
	var out, debug bytes.Buffer
	retired := make(chan int, 1)
	server := &Server{
		In:          pipeReader,
		Out:         &out,
		Err:         &debug,
		IdleRelease: 150 * time.Millisecond,
		exitFn:      func(code int) { retired <- code },
	}
	done := make(chan error, 1)
	go func() { done <- server.Run() }()
	ping := `{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n"
	for i := 0; i < 6; i++ {
		time.Sleep(40 * time.Millisecond)
		if _, err := pipeWriter.Write([]byte(ping)); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	// 240ms of paced activity crossed the 150ms budget several times: the
	// session must still be serving.
	select {
	case <-retired:
		t.Fatalf("watchdog retired an active session")
	case <-time.After(20 * time.Millisecond):
	}
	pipeWriter.Close()
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.Count(out.String(), "\n") != 6 {
		t.Fatalf("responses = %d, want 6", strings.Count(out.String(), "\n"))
	}
}

func TestOrphanRetiresSession(t *testing.T) {
	t.Setenv(debugEnv, "1")
	fastWatchdog(t)
	pipeReader, pipeWriter := io.Pipe()
	var out, debug bytes.Buffer
	retired := make(chan int, 1)
	var ppidCalls atomic.Int64
	server := &Server{
		In:          pipeReader,
		Out:         &out,
		Err:         &debug,
		IdleRelease: -1, // isolate the orphan trigger
		ppidFn: func() int {
			if ppidCalls.Add(1) == 1 {
				return 1000 // birth: harness alive
			}
			return 1 // re-parented: harness died
		},
		exitFn: func(code int) { retired <- code },
	}
	done := make(chan error, 1)
	go func() { done <- server.Run() }()
	select {
	case code := <-retired:
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("watchdog never retired the orphaned session")
	}
	if !strings.Contains(debug.String(), "parent harness exited") {
		t.Fatalf("stderr = %q, want the orphan reason", debug.String())
	}
	pipeWriter.Close()
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
}

func TestImageReplacementRetiresSession(t *testing.T) {
	t.Setenv(debugEnv, "1")
	fastWatchdog(t)
	binary := filepath.Join(t.TempDir(), "rolling-memory")
	writeImage := func(content string) {
		// Remove first so the inode changes, matching an atomic mv swap.
		os.Remove(binary)
		if err := os.WriteFile(binary, []byte(content), 0o755); err != nil {
			t.Fatalf("write image: %v", err)
		}
	}
	writeImage("build one")
	firstStat := make(chan os.FileInfo, 1)
	pipeReader, pipeWriter := io.Pipe()
	var out, debug bytes.Buffer
	retired := make(chan int, 1)
	server := &Server{
		In:          pipeReader,
		Out:         &out,
		Err:         &debug,
		IdleRelease: -1, // isolate the stale-image trigger
		statFn: func() (os.FileInfo, error) {
			info, err := os.Stat(binary)
			if err == nil {
				select {
				case firstStat <- info:
				default:
				}
			}
			return info, err
		},
		exitFn: func(code int) { retired <- code },
	}
	done := make(chan error, 1)
	go func() { done <- server.Run() }()
	<-firstStat // the start image is pinned; same-build ticks must not retire
	select {
	case <-retired:
		t.Fatalf("watchdog retired a session whose image is unchanged")
	case <-time.After(60 * time.Millisecond):
	}
	writeImage("build number two")
	select {
	case code := <-retired:
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("watchdog never retired the replaced image")
	}
	if !strings.Contains(debug.String(), "running image was replaced on disk") {
		t.Fatalf("stderr = %q, want the stale-image reason", debug.String())
	}
	pipeWriter.Close()
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
}

func TestDaemonProxySidecarIgnoresIdle(t *testing.T) {
	fastWatchdog(t)
	pipeReader, pipeWriter := io.Pipe()
	var out, debug bytes.Buffer
	retired := make(chan int, 1)
	server := &Server{
		In:            pipeReader,
		Out:           &out,
		Err:           &debug,
		ProxyToDaemon: true,
		IdleRelease:   100 * time.Millisecond,
		exitFn:        func(code int) { retired <- code },
	}
	done := make(chan error, 1)
	go func() { done <- server.Run() }()
	// A sidecar that forwards to the shared daemon owns no vault handle: a
	// quiet host connection must survive; only EOF or an orphan ends it.
	select {
	case code := <-retired:
		t.Fatalf("watchdog retired a daemon-proxy sidecar with code %d", code)
	case <-time.After(600 * time.Millisecond):
	}
	pipeWriter.Close()
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
}

func TestDaemonProxySidecarIgnoresImageReplacement(t *testing.T) {
	fastWatchdog(t)
	binary := filepath.Join(t.TempDir(), "rolling-memory")
	writeImage := func(content string) {
		os.Remove(binary)
		if err := os.WriteFile(binary, []byte(content), 0o755); err != nil {
			t.Fatalf("write image: %v", err)
		}
	}
	writeImage("build one")
	statCalls := make(chan struct{}, 4)
	pipeReader, pipeWriter := io.Pipe()
	var out, debug bytes.Buffer
	retired := make(chan int, 1)
	server := &Server{
		In:            pipeReader,
		Out:           &out,
		Err:           &debug,
		ProxyToDaemon: true,
		IdleRelease:   -1,
		statFn: func() (os.FileInfo, error) {
			statCalls <- struct{}{}
			return os.Stat(binary)
		},
		exitFn: func(code int) { retired <- code },
	}
	done := make(chan error, 1)
	go func() { done <- server.Run() }()
	<-statCalls // the start image is pinned once
	writeImage("build number two")
	// The sidecar is stateless: a replaced build must not break the host
	// connection. The shared daemon retires itself and restarts on demand.
	select {
	case code := <-retired:
		t.Fatalf("watchdog retired a daemon-proxy sidecar with code %d", code)
	case <-time.After(300 * time.Millisecond):
	}
	select {
	case <-statCalls:
		t.Fatalf("daemon-proxy sidecar kept probing the replaced image")
	default:
	}
	pipeWriter.Close()
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
}

func TestIdleReleaseBudget(t *testing.T) {
	cases := []struct {
		name  string
		field time.Duration
		env   string
		want  time.Duration
	}{
		{"field wins", -1, "30m", -1},
		{"field positive", 5 * time.Minute, "", 5 * time.Minute},
		{"env duration", 0, "90s", 90 * time.Second},
		{"env zero disables", 0, "0", 0},
		{"env garbage fails open", 0, "soon", defaultIdleRelease},
		{"unset defaults", 0, "", defaultIdleRelease},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv(idleReleaseEnv, testCase.env)
			server := &Server{IdleRelease: testCase.field}
			if got := server.idleReleaseBudget(); got != testCase.want {
				t.Fatalf("idleReleaseBudget() = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestRetireWaitsForInFlightRequest(t *testing.T) {
	previous := shutdownDeadline
	shutdownDeadline = 30 * time.Millisecond
	t.Cleanup(func() { shutdownDeadline = previous })
	server := &Server{}
	var inFlight atomic.Int64
	inFlight.Store(1)
	start := time.Now()
	if server.quiescent(&inFlight) {
		t.Fatalf("quiescent = true while a request is in flight")
	}
	if elapsed := time.Since(start); elapsed < 25*time.Millisecond {
		t.Fatalf("quiescent returned after %v, want a full deadline wait", elapsed)
	}
	inFlight.Store(0)
	if !server.quiescent(&inFlight) {
		t.Fatalf("quiescent = false with no request in flight")
	}
}

func TestLazyMemoryReopensWhenDBInodeReplaced(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "memory.db")
	t.Setenv("ROLLING_MEMORY_DB", dbPath)
	t.Setenv("ROLLING_MEMORY_CONFIG", filepath.Join(dir, "config.toml"))
	t.Setenv("ROLLING_MEMORY_LOG", filepath.Join(dir, "rm.log"))

	lazy := NewLazyMemory()
	defer lazy.Close()

	m1, err := lazy.Get()
	if err != nil || m1 == nil {
		t.Fatalf("initial Get: %v", err)
	}
	// Replace the underlying DB file with a brand new inode
	_ = os.Remove(dbPath)
	mAlt, err := openMemory()
	if err != nil {
		t.Fatalf("openMemory replacement: %v", err)
	}
	_ = mAlt.Close()

	m2, err := lazy.Get()
	if err != nil || m2 == nil {
		t.Fatalf("second Get after inode swap: %v", err)
	}
	if m1 == m2 {
		t.Fatalf("expected LazyMemory to reopen a fresh *facade.Memory when DB inode changed")
	}
}

func TestLiveDaemonRecallFullOmitsDuplicateStructuredContent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "memory.db")
	t.Setenv("ROLLING_MEMORY_DB", dbPath)
	t.Setenv("ROLLING_MEMORY_CONFIG", filepath.Join(dir, "config.toml"))
	t.Setenv("ROLLING_MEMORY_LOG", filepath.Join(dir, "rm.log"))

	lazy := NewLazyMemory()
	defer lazy.Close()

	// Seed one memory unit directly through api — the MCP surface is recall-only.
	memory, err := lazy.Get()
	if err != nil {
		t.Fatalf("lazy get: %v", err)
	}
	content := "老张养了一只猫。"
	anchor := "老张养了一只猫"
	if _, err := memory.Remember(facade.RememberRequest{
		Content:    &content,
		AnchorText: &anchor,
		Assertion: map[string]any{
			"s": map[string]any{"label": "老张"},
			"x": "养",
			"o": map[string]any{"label": "猫"},
		},
		Session: "default",
	}); err != nil {
		t.Fatalf("seed remember: %v", err)
	}

	// 1) Full mode in liveDaemon must omit duplicate structuredContent JSON
	fullResp := handle(map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "recall",
			"arguments": map[string]any{
				"cue": "老张 猫",
			},
		},
	}, nil, lazy, nil)
	if isError(t, fullResp) {
		t.Fatalf("recall full failed: %#v", fullResp)
	}
	if txt := textOf(t, fullResp); !strings.Contains(txt, "<rolling-memory-recall") {
		t.Fatalf("expected <rolling-memory-recall> text, got %q", txt)
	}
	if _, hasStruct := resultOf(t, fullResp)["structuredContent"]; hasStruct {
		t.Fatalf("liveDaemon full recall must omit duplicate structuredContent to avoid double token output")
	}

	// 2) Probe mode in liveDaemon still keeps structuredContent
	probeResp := handle(map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "recall",
			"arguments": map[string]any{
				"cue":  "老张 猫",
				"mode": "probe",
			},
		},
	}, nil, lazy, nil)
	if _, hasStruct := resultOf(t, probeResp)["structuredContent"]; !hasStruct {
		t.Fatalf("probe recall should still include structuredContent")
	}
}

func TestHTTPSessionEvictionClosesOldestWithoutPanic(t *testing.T) {
	server := &HTTPServer{}
	firstSession, firstID := server.newSession()
	for i := 1; i < maxHTTPSessions; i++ {
		server.newSession()
	}
	if _, ok := server.session(firstID); !ok {
		t.Fatalf("expected oldest session %q to remain before cap is exceeded", firstID)
	}
	_, newestID := server.newSession()
	if _, ok := server.session(firstID); ok {
		t.Fatalf("expected oldest session %q to be evicted once cap is exceeded", firstID)
	}
	if _, ok := server.session(newestID); !ok {
		t.Fatalf("expected newest session %q to be retained", newestID)
	}
	select {
	case <-firstSession.closed:
	default:
		t.Fatalf("expected evicted session closed channel to be closed")
	}
}
