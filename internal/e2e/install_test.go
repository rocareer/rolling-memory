// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package e2e_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInstallCheckAcrossHarnesses installs the skill and the MCP registration
// into zcode / claude-code / codex inside a throwaway HOME and then requires
// `install --check` to pass, including the live MCP handshake against the real
// binary with exactly remember + recall.
func TestInstallCheckHandshakeAndRegistration(t *testing.T) {
	sb := newSandbox(t)
	args := []string{"install", "--binary", binaryPath, "--mcp-only", "--json", "zcode"}
	installed := sb.run(args...)
	assertExit(t, installed, 0, "install")

	check := func(binary string) result {
		return sb.run("install", "--check", "--binary", binary, "--mcp-only", "--json", "zcode")
	}

	success := check(binaryPath)
	assertExit(t, success, 0, "install --check (handshake succeeds)")
	successReport := jsonObject(t, success.stdout)
	if successReport["ok"] != true || objectAt(t, successReport, "server")["ok"] != true {
		t.Fatalf("successful check report = %s", success.stdout)
	}
	if status := actionStatus(t, successReport, "zcode", "mcp"); status != "ok" {
		t.Fatalf("successful check registration = %q", status)
	}

	// Preserve the registration but make the configured launcher stop responding.
	brokenBinary := filepath.Join(sb.base, "missing-launcher")
	if err := os.WriteFile(brokenBinary, []byte("placeholder"), 0o700); err != nil {
		t.Fatalf("create launcher: %v", err)
	}
	installed = sb.run("install", "--binary", brokenBinary, "--mcp-only", "--force", "--json", "zcode")
	assertExit(t, installed, 0, "install broken launcher")
	if err := os.Remove(brokenBinary); err != nil {
		t.Fatalf("remove launcher: %v", err)
	}
	failed := check(brokenBinary)
	assertExit(t, failed, 1, "install --check (handshake fails)")
	failedReport := jsonObject(t, failed.stdout)
	server := objectAt(t, failedReport, "server")
	if failedReport["ok"] != false || server["ok"] != false || !strings.Contains(stringField(t, server, "error"), "launcher not found") {
		t.Fatalf("failed handshake report = %s", failed.stdout)
	}
	if status := actionStatus(t, failedReport, "*", "server"); status != "error" {
		t.Fatalf("failed handshake action = %q", status)
	}
	if status := actionStatus(t, failedReport, "zcode", "mcp"); status != "ok" {
		t.Fatalf("failed handshake registration = %q", status)
	}

	if err := os.Remove(filepath.Join(sb.home, ".zcode", "cli", "config.json")); err != nil {
		t.Fatalf("remove registration: %v", err)
	}
	missing := check(binaryPath)
	assertExit(t, missing, 1, "install --check (missing registration)")
	missingReport := jsonObject(t, missing.stdout)
	if missingReport["ok"] != false || objectAt(t, missingReport, "server")["ok"] != true {
		t.Fatalf("missing registration report = %s", missing.stdout)
	}
	if status := actionStatus(t, missingReport, "zcode", "mcp"); status != "missing" {
		t.Fatalf("missing registration action = %q", status)
	}
}

func TestInstallCheckAcrossHarnesses(t *testing.T) {
	sb := newSandbox(t)
	harnesses := []string{"zcode", "claude-code", "codex"}
	for _, dir := range []string{".zcode", ".claude", ".codex"} {
		if err := os.MkdirAll(filepath.Join(sb.home, dir), 0o700); err != nil {
			t.Fatalf("harness dir %s: %v", dir, err)
		}
	}

	installArgs := append([]string{"install", "--binary", binaryPath, "--skill-file", sb.skillFile(), "--json"}, harnesses...)
	installed := sb.run(installArgs...)
	assertExit(t, installed, 0, "install")
	report := jsonObject(t, installed.stdout)
	if report["ok"] != true || report["mode"] != "install" || report["binary"] != binaryPath {
		t.Fatalf("install report = %s", installed.stdout)
	}
	assertNoFailures(t, report)
	for _, id := range harnesses {
		if status := actionStatus(t, report, id, "skill"); status != "installed" {
			t.Fatalf("%s skill = %q", id, status)
		}
		if status := actionStatus(t, report, id, "mcp"); status != "installed" {
			t.Fatalf("%s mcp = %q", id, status)
		}
	}

	skillRoots := map[string]string{
		"zcode":       filepath.Join(sb.home, ".zcode", "skills"),
		"claude-code": filepath.Join(sb.home, ".claude", "skills"),
		"codex":       filepath.Join(sb.home, ".codex", "skills"),
	}
	for id, root := range skillRoots {
		skill := filepath.Join(root, "rolling-memory", "SKILL.md")
		head := readFile(t, skill)
		if !strings.Contains(head, "name: rolling-memory") || !strings.Contains(head, "description:") {
			t.Fatalf("%s skill frontmatter = %q", id, head)
		}
	}

	zcodeEntry := objectAt(t, jsonObject(t, readFile(t, filepath.Join(sb.home, ".zcode", "cli", "config.json"))),
		"mcp", "servers", "rolling-memory")
	claudeEntry := objectAt(t, jsonObject(t, readFile(t, filepath.Join(sb.home, ".claude.json"))),
		"mcpServers", "rolling-memory")
	codexEntry := objectAt(t, parseTOML(t, readFile(t, filepath.Join(sb.home, ".codex", "config.toml"))),
		"mcp_servers", "rolling-memory")
	for id, entry := range map[string]map[string]any{
		"zcode": zcodeEntry, "claude-code": claudeEntry, "codex": codexEntry,
	} {
		if entry["command"] != binaryPath {
			t.Fatalf("%s command = %#v, want %q", id, entry["command"], binaryPath)
		}
		args := arrayField(t, entry, "args")
		if len(args) != 1 || args[0] != "mcp" {
			t.Fatalf("%s args = %#v", id, args)
		}
	}
	if zcodeEntry["type"] != "stdio" || claudeEntry["type"] != "stdio" {
		t.Fatalf("registry type = %#v / %#v", zcodeEntry["type"], claudeEntry["type"])
	}
	if timeout := numberField(t, codexEntry, "startup_timeout_sec"); timeout != 30 {
		t.Fatalf("codex startup_timeout_sec = %v", timeout)
	}
	// A custom --binary never writes the launcher shim.
	if fileExists(filepath.Join(sb.base, "xdg-data", "rolling-memory", "bin", "rolling-memory.sh")) {
		t.Fatalf("shim written despite --binary")
	}

	checkArgs := append([]string{"install", "--check", "--binary", binaryPath, "--skill-file", sb.skillFile(), "--json"}, harnesses...)
	checked := sb.run(checkArgs...)
	assertExit(t, checked, 0, "install --check")
	checkReport := jsonObject(t, checked.stdout)
	if checkReport["ok"] != true || checkReport["mode"] != "check" {
		t.Fatalf("check report = %s", checked.stdout)
	}
	assertNoFailures(t, checkReport)
	server := objectAt(t, checkReport, "server")
	if server["ok"] != true || stringField(t, objectAt(t, server, "server"), "name") != "rolling-memory" {
		t.Fatalf("handshake = %#v", server)
	}
	tools := arrayField(t, server, "tools")
	if len(tools) != 1 || tools[0] != "recall" {
		t.Fatalf("handshake tools = %#v（单工具面：回忆即记忆）", tools)
	}
	for _, id := range harnesses {
		if status := actionStatus(t, checkReport, id, "skill"); status != "ok" {
			t.Fatalf("%s skill check = %q", id, status)
		}
		if status := actionStatus(t, checkReport, id, "mcp"); status != "ok" {
			t.Fatalf("%s mcp check = %q", id, status)
		}
		if status := actionStatus(t, checkReport, id, "hooks"); status != "skipped" {
			t.Fatalf("%s hooks check = %q", id, status)
		}
	}

	// The check is read-only: running it again keeps the same verdict.
	again := sb.run(checkArgs...)
	assertExit(t, again, 0, "install --check (second)")
	if again.stdout != checked.stdout {
		t.Fatalf("check is not stable:\n--- first ---\n%s\n--- second ---\n%s", checked.stdout, again.stdout)
	}

	// The bare form (no harness names) rediscovers exactly the three
	// installations from the temporary HOME: detection plus the installed filter.
	bare := sb.run("install", "--check", "--binary", binaryPath, "--json")
	assertExit(t, bare, 0, "install --check (detected)")
	bareReport := jsonObject(t, bare.stdout)
	if bareReport["ok"] != true {
		t.Fatalf("bare check report = %s", bare.stdout)
	}
	selected := map[string]bool{}
	for _, item := range arrayField(t, bareReport, "actions") {
		action, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("action is not an object: %#v", item)
		}
		selected[stringField(t, action, "harness")] = true
	}
	if len(selected) != 3 || !selected["zcode"] || !selected["claude-code"] || !selected["codex"] {
		t.Fatalf("detected harnesses = %#v", selected)
	}
}
