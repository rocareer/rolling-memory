// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// seedDSHSession writes one DeepSeek Harness transcript at the layout the host
// actually uses:
//
//	<DSH_HOME>/sessions/--<cwd>--/<session-dir>/session.v<N>.jsonl.zstd
//
// where <session-dir> is observed both as session-<uuid> and as a bare <uuid>
// — the scan must count both, and only these.
func seedDSHSession(t *testing.T, dshHome, slug, sessionDir string, mtime time.Time) {
	t.Helper()
	path := filepath.Join(dshHome, "sessions", slug, sessionDir, "session.v4.jsonl.zstd")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir dsh session: %v", err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write dsh transcript: %v", err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes dsh transcript: %v", err)
	}
}

// TestSyncStatusCountsDSHSessionsFromRealLayout locks the console regression:
// the environment panel reported DeepSeek Harness as INSTALLED with Sessions 0
// while the host's sessions were on disk, because the scanner globbed the flat
// <home>/.dsh/sessions/*.jsonl layout that DSH never writes. The inventory now
// reads the host's own store, and the other harnesses' counts are unchanged.
func TestSyncStatusCountsDSHSessionsFromRealLayout(t *testing.T) {
	env := newEnv(t)
	dshHome := os.Getenv("DSH_HOME")
	if dshHome == "" {
		t.Fatal("test env must isolate DSH_HOME")
	}
	older := time.Now().Add(-2 * time.Hour)
	seedDSHSession(t, dshHome, "--Users-alice-Workspace--", "session-7e44024f-e910-4705-8d47-fdd31f7815e1", older)
	seedDSHSession(t, dshHome, "--Users-alice-Workspace--", "0ab8902b-726b-431f-ab01-30c524e9794e", older)
	seedDSHSession(t, dshHome, "--private-tmp-hq-work--", "session-18c611a6-5368-4596-a7b7-ab66485f48a4", time.Now())
	// A file DSH never writes must not be counted.
	if err := os.WriteFile(filepath.Join(dshHome, "sessions", "stray.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write stray transcript: %v", err)
	}

	// Hosts that already had a working inventory keep theirs: one Claude Code
	// transcript and one Codex transcript, both older than the DSH tail so the
	// recency claim below can only come from DSH.
	claudePath := filepath.Join(os.Getenv("HOME"), ".claude", "projects", "demo", "s1.jsonl")
	if err := os.MkdirAll(filepath.Dir(claudePath), 0o755); err != nil {
		t.Fatalf("mkdir claude: %v", err)
	}
	claudeLines := "{\"type\":\"user\",\"sessionId\":\"s1\",\"timestamp\":\"2026-11-12T00:00:00\",\"message\":{\"role\":\"user\",\"content\":[{\"type\":\"text\",\"text\":\"检查宿主扫描。\"}]}}\n" +
		"{\"type\":\"assistant\",\"sessionId\":\"s1\",\"timestamp\":\"2026-11-12T00:00:10\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"已修复并复验。\"}]}}\n"
	if err := os.WriteFile(claudePath, []byte(claudeLines), 0o644); err != nil {
		t.Fatalf("write claude transcript: %v", err)
	}
	codexPath := filepath.Join(os.Getenv("HOME"), ".codex", "sessions", "2026", "11", "13", "c1.jsonl")
	if err := os.MkdirAll(filepath.Dir(codexPath), 0o755); err != nil {
		t.Fatalf("mkdir codex: %v", err)
	}
	if err := os.WriteFile(codexPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write codex transcript: %v", err)
	}
	for _, path := range []string{claudePath, codexPath} {
		if err := os.Chtimes(path, older, older); err != nil {
			t.Fatalf("chtimes %s: %v", path, err)
		}
	}

	payload := runStatusJSON(t, filepath.Join(env.base, "dsh.db"))

	dsh := harnessItem(t, payload, "dsh")
	if got := dsh["sessions_count"]; got != float64(3) {
		t.Fatalf("dsh sessions_count = %v, want 3 (two session-<uuid> dirs + one bare <uuid> dir)", got)
	}
	if dsh["sessions_scan_supported"] != true {
		t.Fatalf("dsh sessions_scan_supported = %v, want true", dsh["sessions_scan_supported"])
	}
	if dsh["turns_scan_supported"] != false {
		t.Fatalf("dsh turns_scan_supported = %v, want false: zstd turn content is not read yet, so coupling the session count to it would hide a real number", dsh["turns_scan_supported"])
	}
	if got := dsh["local_turns_count"]; got != float64(0) {
		t.Fatalf("dsh local_turns_count = %v, want 0 (not counted, labelled as such)", got)
	}
	if got, ok := dsh["last_active_at"].(float64); !ok || got <= 0 {
		t.Fatalf("dsh last_active_at = %v, want the newest transcript mtime", dsh["last_active_at"])
	}
	if dsh["active"] != true {
		t.Fatalf("dsh item = %+v, want active: the newest activity on this machine is a DSH transcript", dsh)
	}
	if payload["active_harness_id"] != "dsh" {
		t.Fatalf("active_harness_id = %v, want dsh", payload["active_harness_id"])
	}
	if payload["detection_source"] != "recent_activity" {
		t.Fatalf("detection_source = %v, want recent_activity", payload["detection_source"])
	}

	if got := harnessItem(t, payload, "claude-code")["sessions_count"]; got != float64(1) {
		t.Fatalf("claude-code sessions_count = %v, want 1 (unchanged inventory)", got)
	}
	if got := harnessItem(t, payload, "codex")["sessions_count"]; got != float64(1) {
		t.Fatalf("codex sessions_count = %v, want 1 (unchanged inventory)", got)
	}
	if got := harnessItem(t, payload, "zcode")["sessions_count"]; got != float64(0) {
		t.Fatalf("zcode sessions_count = %v, want 0 (no ZCode store in this home)", got)
	}
}

// TestSyncStatusPrefersDSHSessionMarkerOverInheritedHostEnv locks the precedence
// a live host needs: DSH exports DSH_SESSION_ID to the processes it owns, while
// ZCODE_PROJECT_DIR / CLAUDE_PROJECT_DIR are plain shell exports that every
// descendant inherits — a DSH session started from a ZCode terminal carries
// both, and reading the inherited one first reports that session as ZCode.
func TestSyncStatusPrefersDSHSessionMarkerOverInheritedHostEnv(t *testing.T) {
	t.Run("dsh session marker wins over inherited zcode export", func(t *testing.T) {
		env := newEnv(t)
		t.Setenv("DSH_SESSION_ID", "0ab8902b-726b-431f-ab01-30c524e9794e")
		t.Setenv("DSH_SHELL", "1")
		t.Setenv("ZCODE_PROJECT_DIR", "/tmp/demo-workspace")
		t.Setenv("CLAUDE_PROJECT_DIR", "/tmp/demo-workspace")

		payload := runStatusJSON(t, filepath.Join(env.base, "dsh-env.db"))
		if payload["active_harness_id"] != "dsh" {
			t.Fatalf("active_harness_id = %v, want dsh", payload["active_harness_id"])
		}
		if payload["detection_source"] != "env:DSH_SESSION_ID" {
			t.Fatalf("detection_source = %v, want env:DSH_SESSION_ID", payload["detection_source"])
		}
	})

	t.Run("zcode export still wins when no dsh marker is present", func(t *testing.T) {
		env := newEnv(t)
		t.Setenv("ZCODE_PROJECT_DIR", "/tmp/demo-workspace")

		payload := runStatusJSON(t, filepath.Join(env.base, "zcode-env.db"))
		if payload["active_harness_id"] != "zcode" {
			t.Fatalf("active_harness_id = %v, want zcode", payload["active_harness_id"])
		}
		if payload["detection_source"] != "env:ZCODE_PROJECT_DIR" {
			t.Fatalf("detection_source = %v, want env:ZCODE_PROJECT_DIR", payload["detection_source"])
		}
	})

	t.Run("explicit identity stamp outranks inference", func(t *testing.T) {
		env := newEnv(t)
		t.Setenv("ROLLING_MEMORY_HARNESS", "codex")
		t.Setenv("ZCODE_PROJECT_DIR", "/tmp/demo-workspace")

		payload := runStatusJSON(t, filepath.Join(env.base, "stamp.db"))
		if payload["active_harness_id"] != "codex" {
			t.Fatalf("active_harness_id = %v, want codex", payload["active_harness_id"])
		}
		if payload["detection_source"] != "env:ROLLING_MEMORY_HARNESS" {
			t.Fatalf("detection_source = %v, want env:ROLLING_MEMORY_HARNESS", payload["detection_source"])
		}
	})
}
