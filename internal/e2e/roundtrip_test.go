// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package e2e_test

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestExportRejectsIncompleteBackup(t *testing.T) {
	sandbox := newSandbox(t)
	transcript := writeTranscript(t, filepath.Join(sandbox.base, "roundtrip.jsonl"))
	seeded := sandbox.run("import", transcript, "--json")
	assertExit(t, seeded, 0, "import transcript")

	before := readFile(t, transcript)
	result := sandbox.run("export", "--out", transcript, "--json")
	assertExit(t, result, 1, "export")
	report := jsonObject(t, result.stdout)
	if report["ok"] != false || !strings.Contains(report["error"].(string), "not a complete backup") {
		t.Fatalf("export report = %s", result.stdout)
	}
	if readFile(t, transcript) != before {
		t.Fatal("export modified the source transcript")
	}
}
