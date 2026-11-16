// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Command fakemcp is a synthetic MCP stdio server used by the harness
// handshake tests. It answers initialize and tools/list only and never touches
// a database.
package main

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"time"
)

func main() {
	mode := os.Getenv("RM_FAKE_MCP_MODE")
	if mode == "sleep" {
		time.Sleep(5 * time.Second)
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var request map[string]any
		if json.Unmarshal([]byte(line), &request) != nil {
			continue
		}
		if mode == "silent" {
			continue
		}
		switch request["method"] {
		case "initialize":
			_ = encoder.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      request["id"],
				"result": map[string]any{
					"protocolVersion": "2024-11-05",
					"serverInfo":      map[string]any{"name": "rolling-memory", "version": "0.7.2"},
				},
			})
		case "tools/list":
			tools := []map[string]any{{"name": "recall"}}
			if mode == "wrong-tools" {
				tools = []map[string]any{{"name": "remember"}}
			}
			_ = encoder.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      request["id"],
				"result":  map[string]any{"tools": tools},
			})
		}
	}
}
