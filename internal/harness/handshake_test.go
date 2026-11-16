// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package harness

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// buildFakeMCP compiles the synthetic MCP server. The handshake must run a real
// child process; the go test binary itself is not a launcher.
func buildFakeMCP(t *testing.T) string {
	t.Helper()
	name := "fakemcp"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	output := filepath.Join(t.TempDir(), name)
	command := exec.Command("go", "build", "-o", output, "./testdata/fakemcp")
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build fake MCP server: %v\n%s", err, data)
	}
	return output
}

func fakeHandshakeOptions(t *testing.T) InstallOptions {
	t.Helper()
	options := DefaultInstallOptions(t.TempDir(), t.TempDir())
	options.Binary = buildFakeMCP(t)
	options.Scope = "mem:test"
	return options
}

func TestHandshakeOK(t *testing.T) {
	t.Setenv("RM_FAKE_MCP_MODE", "ok")
	result := Handshake(fakeHandshakeOptions(t), 10*time.Second)
	if result["ok"] != true {
		t.Fatalf("handshake = %#v", result)
	}
	tools, ok := result["tools"].([]any)
	if !ok || len(tools) != 1 || tools[0] != "recall" {
		t.Fatalf("tools = %#v", result["tools"])
	}
	server, ok := result["server"].(map[string]any)
	if !ok || server["name"] != "rolling-memory" {
		t.Fatalf("server = %#v", result["server"])
	}
	if _, exists := result["error"]; exists {
		t.Fatalf("unexpected error: %v", result["error"])
	}
}

func TestHandshakeUnexpectedTools(t *testing.T) {
	t.Setenv("RM_FAKE_MCP_MODE", "wrong-tools")
	result := Handshake(fakeHandshakeOptions(t), 10*time.Second)
	if result["ok"] != false {
		t.Fatalf("handshake should fail: %#v", result)
	}
	if message, _ := result["error"].(string); !strings.Contains(message, "unexpected tool set: ['remember']") {
		t.Fatalf("error = %v", result["error"])
	}
}

func TestHandshakeNoResponse(t *testing.T) {
	t.Setenv("RM_FAKE_MCP_MODE", "silent")
	result := Handshake(fakeHandshakeOptions(t), 10*time.Second)
	if result["ok"] != false {
		t.Fatalf("handshake should fail: %#v", result)
	}
	if message, _ := result["error"].(string); !strings.Contains(message, "no initialize/tools-list response (exit 0); stderr: -") {
		t.Fatalf("error = %v", result["error"])
	}
}

func TestHandshakeMissingLauncher(t *testing.T) {
	options := DefaultInstallOptions(t.TempDir(), t.TempDir())
	options.Binary = filepath.Join(t.TempDir(), "does-not-exist")
	result := Handshake(options, 10*time.Second)
	if result["ok"] != false {
		t.Fatalf("handshake should fail: %#v", result)
	}
	if message, _ := result["error"].(string); !strings.Contains(message, "launcher not found") {
		t.Fatalf("error = %v", result["error"])
	}
}

func TestHandshakeTimeout(t *testing.T) {
	t.Setenv("RM_FAKE_MCP_MODE", "sleep")
	result := Handshake(fakeHandshakeOptions(t), 100*time.Millisecond)
	if result["ok"] != false {
		t.Fatalf("handshake should time out: %#v", result)
	}
	if message, _ := result["error"].(string); !strings.Contains(message, "timed out") {
		t.Fatalf("error = %v", result["error"])
	}
}

func TestLauncherCommand(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	options := DefaultInstallOptions(t.TempDir(), t.TempDir())
	options.Binary = "/opt/rm/rolling-memory"
	if command := launcherCommand(options); len(command) != 1 || command[0] != options.Binary {
		t.Fatalf("custom binary command = %#v", command)
	}

	options.Binary = ""
	if command := launcherCommand(options); len(command) != 1 || command[0] != executablePath() {
		t.Fatalf("fallback command = %#v", command)
	}

	shim := DefaultShimPath(options.Home)
	if err := os.MkdirAll(filepath.Dir(shim), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(shim, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write shim: %v", err)
	}
	if command := launcherCommand(options); len(command) != 1 || command[0] != shim {
		t.Fatalf("shim command = %#v", command)
	}
}
