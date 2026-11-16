// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package e2e_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// TestIntegrityCheckAfterWrites writes through the CLI 现役写侧（import 转录）与
// recall 钩子，然后要求沙箱库 PRAGMA integrity_check 为 ok 且 CLI 自身 check 一致。
func TestIntegrityCheckAfterWrites(t *testing.T) {
	sb := newSandbox(t)
	transcript := writeTranscript(t, filepath.Join(sb.base, "integrity.jsonl"))
	seeded := sb.run("import", transcript, "--json")
	assertExit(t, seeded, 0, "import")

	hook := sb.runInput(`{"session_id":"e2e","prompt":"老张 猫"}`, "hook", "recall", "--json")
	assertExit(t, hook, 0, "hook recall")

	connection, err := sql.Open("sqlite", sb.db)
	if err != nil {
		t.Fatalf("open %s: %v", sb.db, err)
	}
	defer connection.Close()

	var integrity string
	if err := connection.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil {
		t.Fatalf("PRAGMA integrity_check: %v", err)
	}
	if integrity != "ok" {
		t.Fatalf("PRAGMA integrity_check = %q", integrity)
	}

	// The import landed in the sandbox file: two traces / two events, no assertions
	//（写侧停用后 CLI 不再生成断言）。
	var traces, events, assertions int
	if err := connection.QueryRow("SELECT count(*) FROM trace").Scan(&traces); err != nil {
		t.Fatalf("count trace: %v", err)
	}
	if traces != 2 {
		t.Fatalf("trace rows = %d, want 2", traces)
	}
	if err := connection.QueryRow("SELECT count(*) FROM event").Scan(&events); err != nil {
		t.Fatalf("count event: %v", err)
	}
	if events != 2 {
		t.Fatalf("event rows = %d, want 2", events)
	}
	if err := connection.QueryRow("SELECT count(*) FROM assertion").Scan(&assertions); err != nil {
		t.Fatalf("count assertion: %v", err)
	}
	if assertions != 0 {
		t.Fatalf("assertion rows = %d, want 0", assertions)
	}

	check := sb.run("check", "--json")
	assertExit(t, check, 0, "check")
	if checked := jsonObject(t, check.stdout); checked["ok"] != true {
		t.Fatalf("check = %s", check.stdout)
	}
}
