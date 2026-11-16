// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package e2e_test

import (
	"path/filepath"
	"strings"
	"testing"
)

// hookTimeouts are the per-event timeout seconds install writes.
// 现役 HookEvents 仅 UserPromptSubmit（SessionStart / Stop 已停用）。
var hookTimeouts = map[string]float64{
	"UserPromptSubmit": 15,
}

// TestHooksContractReplay wires the hooks through `install --with-hooks`, then
// replays the recorded command line with harness-shaped payloads: the
// UserPromptSubmit injection attempt（薄客户端离线下 absent ⇒ 静默）、
// the silent empty payload and the fail-open malformed input.
// SessionStart 引导注入与 Stop 收尾已随事件面停用（现役仅 UserPromptSubmit）。
func TestHooksContractReplay(t *testing.T) {
	sb := newSandbox(t)
	// boot_context is off by default; turn it on so SessionStart injects.
	writeFile(t, sb.config, "[hooks]\nboot_context = true\n")

	installed := sb.run("install", "zcode", "--binary", binaryPath,
		"--skill-file", sb.skillFile(), "--with-hooks", "--json")
	assertExit(t, installed, 0, "install --with-hooks")

	commands := hookCommandsFromConfig(t, filepath.Join(sb.home, ".zcode", "cli", "config.json"))
	wantCommands := map[string]string{
		"UserPromptSubmit": binaryPath + " hook recall --json",
	}
	for event, want := range wantCommands {
		if got := commands[event]; got != want {
			t.Fatalf("%s command = %q, want %q", event, got, want)
		}
	}

	// 播种经现役写侧（import 转录）：离线（无云引擎）时 recall 必为 absent。
	transcript := writeTranscript(t, filepath.Join(sb.base, "session.jsonl"))
	seeded := sb.run("import", transcript, "--json")
	assertExit(t, seeded, 0, "import transcript")

	recallPayload := jsonPayload(t, map[string]any{
		"session_id": "e2e", "prompt": "我的猫叫什么？", "cwd": sb.base,
		"hook_event_name": "UserPromptSubmit",
	})
	recall := sb.runCommandLine(commands["UserPromptSubmit"], recallPayload)
	assertExit(t, recall, 0, "hook recall")
	// 薄客户端：无云引擎即无注入（absent），stdout 保持空。
	if strings.TrimSpace(recall.stdout) != "" {
		t.Fatalf("offline hook recall injected %q", recall.stdout)
	}

	// An identical prompt inside the ledger window is an echo: still silent.
	repeated := sb.runCommandLine(commands["UserPromptSubmit"], recallPayload)
	assertExit(t, repeated, 0, "hook recall (repeat)")
	if strings.TrimSpace(repeated.stdout) != "" {
		t.Fatalf("repeat injected %q", repeated.stdout)
	}

	// No prompt, no injection; malformed input stays fail-open.
	empty := sb.runInput("{}", "hook", "recall", "--json")
	assertExit(t, empty, 0, "hook recall with an empty payload")
	if strings.TrimSpace(empty.stdout) != "" {
		t.Fatalf("empty payload injected %q", empty.stdout)
	}
	garbage := sb.runInput("not json", "hook", "recall", "--json")
	assertExit(t, garbage, 0, "hook recall with malformed input")
	if strings.TrimSpace(garbage.stdout) != "" {
		t.Fatalf("malformed input injected %q", garbage.stdout)
	}

	// The main log carries one sorted JSON line per recall attempt, with the
	// absent / zero-injection receipt.
	events := map[string][]map[string]any{}
	for _, line := range jsonLines(readFile(t, sb.log)) {
		entry := jsonObject(t, line)
		event := stringField(t, entry, "event")
		events[event] = append(events[event], entry)
	}
	if len(events["recall"]) == 0 {
		t.Fatalf("recall log lines = %#v", events)
	}
	absent, injected := 0, 0
	for _, entry := range events["recall"] {
		if numberOr(t, entry, "absent", 0) == 1 {
			absent++
		}
		if numberOr(t, entry, "injected", 0) == 1 {
			injected++
		}
	}
	if absent == 0 || injected != 0 {
		t.Fatalf("recall log lines = %#v（离线应为 absent 且零注入）", events["recall"])
	}
}

// hookCommandsFromConfig reads the three event → command entries install wrote
// into the zcode config, checking the group shape on the way.
func hookCommandsFromConfig(t *testing.T, path string) map[string]string {
	t.Helper()
	events := objectAt(t, jsonObject(t, readFile(t, path)), "hooks", "events")
	commands := map[string]string{}
	for event, timeout := range hookTimeouts {
		groups := arrayField(t, events, event)
		if len(groups) != 1 {
			t.Fatalf("%s groups = %#v", event, groups)
		}
		group, ok := groups[0].(map[string]any)
		if !ok {
			t.Fatalf("%s group is not an object: %#v", event, groups[0])
		}
		hooks := arrayField(t, group, "hooks")
		if len(hooks) != 1 {
			t.Fatalf("%s hooks = %#v", event, hooks)
		}
		hook, ok := hooks[0].(map[string]any)
		if !ok {
			t.Fatalf("%s hook is not an object: %#v", event, hooks[0])
		}
		if hook["type"] != "command" {
			t.Fatalf("%s hook type = %#v", event, hook["type"])
		}
		if got := numberField(t, hook, "timeout"); got != timeout {
			t.Fatalf("%s timeout = %v, want %v", event, got, timeout)
		}
		commands[event] = stringField(t, hook, "command")
	}
	return commands
}

// writeTranscript writes the two synthetic turns one coding session imports.
func writeTranscript(t *testing.T, path string) string {
	t.Helper()
	content := strings.Join([]string{
		`{"type":"user","sessionId":"e2e","timestamp":"2026-10-20T10:00:00",` +
			`"message":{"role":"user","content":[{"type":"text","text":"我喜欢猫。我的猫叫小白。"}]}}`,
		`{"type":"assistant","sessionId":"e2e","timestamp":"2026-10-20T10:00:10",` +
			`"message":{"role":"assistant","content":[{"type":"text","text":"记住了。"}]}}`,
		"",
	}, "\n")
	writeFile(t, path, content)
	return path
}
