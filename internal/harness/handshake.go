// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/rocareer/rolling-memory/internal/version"
)

// Handshake starts "<launcher> mcp" on a temporary database and verifies that
// initialize and tools/list answer with exactly remember + recall.
func Handshake(options InstallOptions, timeout time.Duration) map[string]any {
	options = options.normalized()
	result := map[string]any{"ok": false}
	request, err := handshakeRequest()
	if err != nil {
		result["error"] = "MCP handshake failed: " + err.Error()
		return result
	}
	temp, err := os.MkdirTemp("", "rm-check.")
	if err != nil {
		result["error"] = "MCP handshake failed: " + err.Error()
		return result
	}
	defer os.RemoveAll(temp)

	env := append(launcherEnv(),
		"ROLLING_MEMORY_CONFIG="+filepath.Join(temp, "config.toml"),
		"ROLLING_MEMORY_DB="+filepath.Join(temp, "memory.db"),
		"ROLLING_MEMORY_LOG="+filepath.Join(temp, "rolling-memory.log"),
		"ROLLING_MEMORY_MODE=local",
		"ROLLING_MEMORY_SCOPE="+handshakeScope(options),
	)
	command := append(launcherCommand(options), "mcp")

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	process := exec.CommandContext(ctx, command[0], command[1:]...)
	process.Dir = options.Cwd
	process.Env = env
	process.Stdin = strings.NewReader(request)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	process.Stdout = &stdout
	process.Stderr = &stderr

	runErr := process.Run()
	if runErr != nil {
		if ctx.Err() == context.DeadlineExceeded {
			result["error"] = fmt.Sprintf("MCP handshake timed out after %.0fs", timeout.Seconds())
			return result
		}
		if errors.Is(runErr, fs.ErrNotExist) {
			result["error"] = "launcher not found: " + runErr.Error()
			return result
		}
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			result["error"] = "MCP handshake failed: " + runErr.Error()
			return result
		}
	}

	responses := decodeHandshakeResponses(stdout.String())
	initialize := findResponse(responses, 1)
	tools := findResponse(responses, 2)
	if initialize == nil || tools == nil {
		exitCode := 0
		if process.ProcessState != nil {
			exitCode = process.ProcessState.ExitCode()
		}
		lastStderr := "-"
		if trimmed := strings.TrimSpace(stderr.String()); trimmed != "" {
			lines := strings.Split(trimmed, "\n")
			lastStderr = strings.TrimSpace(lines[len(lines)-1])
		}
		result["error"] = fmt.Sprintf("no initialize/tools-list response (exit %d); stderr: %s", exitCode, lastStderr)
		return result
	}

	server := map[string]any{}
	if payload, ok := initialize["result"].(map[string]any); ok {
		if info, ok := payload["serverInfo"].(map[string]any); ok {
			server = info
		}
	}
	names := make([]any, 0, 2)
	if payload, ok := tools["result"].(map[string]any); ok {
		if list, ok := payload["tools"].([]any); ok {
			for _, item := range list {
				if object, ok := item.(map[string]any); ok {
					names = append(names, object["name"])
				} else {
					names = append(names, nil)
				}
			}
		}
	}
	ok := toolNamesAreStandard(names)
	result["ok"] = ok
	result["server"] = server
	result["tools"] = names
	if !ok {
		result["error"] = "unexpected tool set: " + pyListRepr(names)
	}
	return result
}

func handshakeScope(options InstallOptions) string {
	if options.Scope != "" {
		return options.Scope
	}
	return "default"
}

func handshakeRequest() (string, error) {
	initialize := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "rolling-memory-check",
				"version": version.Version,
			},
		},
	}
	list := map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"}
	first, err := json.Marshal(initialize)
	if err != nil {
		return "", err
	}
	second, err := json.Marshal(list)
	if err != nil {
		return "", err
	}
	return string(first) + "\n" + string(second) + "\n", nil
}

func decodeHandshakeResponses(output string) []map[string]any {
	responses := make([]map[string]any, 0, 2)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var payload map[string]any
		if json.Unmarshal([]byte(line), &payload) != nil || payload == nil {
			continue
		}
		responses = append(responses, payload)
	}
	return responses
}

func findResponse(responses []map[string]any, id float64) map[string]any {
	for _, payload := range responses {
		if value, ok := payload["id"]; ok && idEquals(value, id) {
			return payload
		}
	}
	return nil
}

func idEquals(value any, want float64) bool {
	switch typed := value.(type) {
	case float64:
		return typed == want
	case float32:
		return float64(typed) == want
	case int:
		return float64(typed) == want
	case int64:
		return float64(typed) == want
	case json.Number:
		parsed, err := typed.Float64()
		return err == nil && parsed == want
	}
	return false
}

func toolNamesAreStandard(names []any) bool {
	if len(names) != 1 {
		return false
	}
	only, ok := names[0].(string)
	return ok && only == "recall"
}
