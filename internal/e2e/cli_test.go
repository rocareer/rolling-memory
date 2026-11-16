// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package e2e_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/version"
)

// TestCLISmoke replays the CLI smoke against the real binary:
// init → import → recall → status → check → doctor, all inside the sandbox.
func TestCLISmoke(t *testing.T) {
	sb := newSandbox(t)

	verRun := sb.run("--version")
	assertExit(t, verRun, 0, "--version")
	if verRun.stdout != "rolling-memory "+version.Version+"\n" {
		t.Fatalf("--version stdout = %q, want rolling-memory %s", verRun.stdout, version.Version)
	}

	init := sb.run("init", "--json")
	assertExit(t, init, 0, "init")
	initPayload := jsonObject(t, init.stdout)
	if initPayload["ok"] != true || initPayload["db"] != sb.db || initPayload["config"] != sb.config {
		t.Fatalf("init = %s", init.stdout)
	}
	if initPayload["scope"] != "mem:e2e" || initPayload["mode"] != "local" {
		t.Fatalf("init scope/mode = %#v", initPayload)
	}
	if numberField(t, initPayload, "schema_version") != 1 {
		t.Fatalf("schema_version = %#v", initPayload["schema_version"])
	}

	// 现役写侧 = import 转录（remember 子命令已随本地断言图停用）。
	transcript := writeTranscript(t, filepath.Join(sb.base, "smoke.jsonl"))
	imported := sb.run("import", transcript, "--json")
	assertExit(t, imported, 0, "import")
	importPayload := jsonObject(t, imported.stdout)
	if importPayload["ok"] != true || numberField(t, importPayload, "inserted") < 2 {
		t.Fatalf("import = %s", imported.stdout)
	}

	// 本地明文：import 把 trace 镜像进 thin vault，recall 从本地返回夹具内容。
	const fixture = "我喜欢猫。我的猫叫小白。"
	recall := sb.run("recall", "--q", "小白 猫", "--no-raw", "--json")
	assertExit(t, recall, 0, "recall --no-raw")
	recalled := jsonObject(t, recall.stdout)
	if recalled["absent"] != false || recalled["scope"] != "mem:e2e" {
		t.Fatalf("recall = %s", recall.stdout)
	}
	foundFixture := false
	for _, item := range arrayField(t, recalled, "bands") {
		band, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("recall band is not an object: %#v", item)
		}
		for _, item := range arrayField(t, band, "nodes") {
			node, ok := item.(map[string]any)
			if !ok {
				t.Fatalf("recall node is not an object: %#v", item)
			}
			if content, ok := node["content"].(string); ok && strings.Contains(content, fixture) {
				foundFixture = true
			}
		}
	}
	if !foundFixture {
		t.Fatalf("recall bands missing fixture: %s", recall.stdout)
	}
	if rawHits := arrayField(t, recalled, "raw_hits"); len(rawHits) != 0 {
		t.Fatalf("recall --no-raw raw_hits = %#v, want empty", rawHits)
	}

	withRaw := sb.run("recall", "--q", "小白 猫", "--include-raw", "--json")
	assertExit(t, withRaw, 0, "recall --include-raw")
	withRawPayload := jsonObject(t, withRaw.stdout)
	if withRawPayload["absent"] != false || withRawPayload["scope"] != "mem:e2e" {
		t.Fatalf("recall --include-raw = %s", withRaw.stdout)
	}
	foundFixture = false
	for _, item := range arrayField(t, withRawPayload, "raw_hits") {
		hit, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("recall raw hit is not an object: %#v", item)
		}
		if text, ok := hit["text"].(string); ok && strings.Contains(text, fixture) {
			foundFixture = true
		}
	}
	if !foundFixture {
		t.Fatalf("recall --include-raw raw_hits missing fixture: %s", withRaw.stdout)
	}

	unrelated := sb.run("recall", "--q", "quasarzirconiumnebula", "--include-raw", "--json")
	assertExit(t, unrelated, 0, "recall unrelated cue")
	unrelatedPayload := jsonObject(t, unrelated.stdout)
	if unrelatedPayload["absent"] != true || unrelatedPayload["scope"] != "mem:e2e" ||
		len(arrayField(t, unrelatedPayload, "bands")) != 0 || len(arrayField(t, unrelatedPayload, "raw_hits")) != 0 {
		t.Fatalf("recall unrelated cue = %s", unrelated.stdout)
	}

	status := sb.run("status", "--json")
	assertExit(t, status, 0, "status")
	summary := jsonObject(t, status.stdout)
	if numberField(t, summary, "active_assertions") != 0 || summary["db"] != sb.db || summary["scope"] != "mem:e2e" {
		t.Fatalf("status = %s", status.stdout)
	}

	check := sb.run("check", "--json")
	assertExit(t, check, 0, "check")
	checked := jsonObject(t, check.stdout)
	if checked["ok"] != true || len(arrayField(t, checked, "issues")) != 0 {
		t.Fatalf("check = %s", check.stdout)
	}

	doctor := sb.run("doctor", "--json")
	assertExit(t, doctor, 0, "doctor")
	if integrity := objectAt(t, jsonObject(t, doctor.stdout), "integrity"); integrity["ok"] != true {
		t.Fatalf("doctor integrity = %#v", integrity)
	}
}

// TestCLIExitCodes verifies the three-way exit contract: 0 success,
// 1 processed application error, 2 CLI parser usage error.
func TestCLIExitCodes(t *testing.T) {
	sb := newSandbox(t)

	usage := sb.run("bogus")
	assertExit(t, usage, 2, "unknown command")
	if !strings.HasPrefix(usage.stderr, "usage: rolling-memory") || !strings.Contains(usage.stderr, "invalid choice") {
		t.Fatalf("usage stderr = %q", usage.stderr)
	}

	processed := sb.run("recall", "--q", "x", "--scope", "bad scope", "--json")
	assertExit(t, processed, 1, "invalid scope")
	failure := jsonObject(t, processed.stdout)
	if failure["ok"] != false || !strings.Contains(stringField(t, failure, "error"), "scope") {
		t.Fatalf("failure = %s", processed.stdout)
	}

	plain := sb.run("recall", "--q", "x", "--scope", "bad scope")
	assertExit(t, plain, 1, "invalid scope without --json")
	if plain.stdout != "" || !strings.HasPrefix(plain.stderr, "error: ") {
		t.Fatalf("plain failure exit=%d stdout=%q stderr=%q", plain.code, plain.stdout, plain.stderr)
	}
}
