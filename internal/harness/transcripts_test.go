// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package harness

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestResolveDSHHomeHonoursEnvOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("DSH_HOME", "")
	if got, want := ResolveDSHHome(home), filepath.Join(home, ".dsh"); got != want {
		t.Fatalf("ResolveDSHHome = %q, want %q", got, want)
	}
	custom := filepath.Join(home, "custom-dsh")
	t.Setenv("DSH_HOME", custom)
	if got := ResolveDSHHome(home); got != custom {
		t.Fatalf("ResolveDSHHome with DSH_HOME = %q, want %q", got, custom)
	}
}

func TestBuildHarnessesDSHHomeMatchesResolvedHome(t *testing.T) {
	home := t.TempDir()
	custom := filepath.Join(home, "custom-dsh")
	t.Setenv("DSH_HOME", custom)
	for _, h := range BuildHarnesses(home, "") {
		if h.ID != "dsh" {
			continue
		}
		// The install target and the transcript scan root must resolve through the
		// same authority, or an install lands where the scanner does not look.
		if h.Detect != custom {
			t.Fatalf("dsh detect = %q, want %q", h.Detect, custom)
		}
		if got := TranscriptGlobs(home, "dsh"); len(got) != 1 || !filepath.HasPrefix(got[0], custom) {
			t.Fatalf("dsh transcript globs = %v, want one pattern under %q", got, custom)
		}
		return
	}
	t.Fatal("dsh harness missing from the registry")
}

// TestTranscriptGlobsDSHMatchesRealLayout pins the host's actual session store:
// <DSH_HOME>/sessions/--<cwd>--/<session-dir>/session.v<N>.jsonl.zstd, with both
// observed session-directory naming forms, and nothing else. A flat
// sessions/*.jsonl pattern matched zero files on a live host and is the defect
// this layout authority replaces.
func TestTranscriptGlobsDSHMatchesRealLayout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("DSH_HOME", "")
	dshSessions := filepath.Join(home, ".dsh", "sessions")
	want := []string{
		filepath.Join(dshSessions, "--Users-alice-Workspace--", "session-7e44024f-e910-4705-8d47-fdd31f7815e1", "session.v4.jsonl.zstd"),
		filepath.Join(dshSessions, "--Users-alice-Workspace--", "0ab8902b-726b-431f-ab01-30c524e9794e", "session.v4.jsonl.zstd"),
		filepath.Join(dshSessions, "--private-tmp-hq-work--", "session-18c611a6-5368-4596-a7b7-ab66485f48a4", "session.v5.jsonl.zstd"),
	}
	for _, path := range want {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	// Noise the pattern must not pick up: a flat transcript at the sessions root,
	// an uncompressed file inside a session directory, and a sibling of the
	// sessions tree.
	noise := []string{
		filepath.Join(dshSessions, "stray.jsonl"),
		filepath.Join(dshSessions, "--Users-alice-Workspace--", "session-7e44024f-e910-4705-8d47-fdd31f7815e1", "other.jsonl"),
		filepath.Join(home, ".dsh", "sessions.bak", "--x--", "y", "session.v4.jsonl.zstd"),
	}
	for _, path := range noise {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	var got []string
	for _, pattern := range TranscriptGlobs(home, "dsh") {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatalf("glob %q: %v", pattern, err)
		}
		got = append(got, matches...)
	}
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("dsh transcript matches = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dsh transcript matches = %v, want %v", got, want)
		}
	}
}

func TestTranscriptGlobsOtherHostsUnchanged(t *testing.T) {
	home := t.TempDir()
	t.Setenv("DSH_HOME", "")
	cases := map[string][]string{
		"zcode":       {filepath.Join(home, ".zcode", "projects", "*", "sessions", "*.jsonl")},
		"claude-code": {filepath.Join(home, ".claude", "projects", "*", "*.jsonl"), filepath.Join(home, ".claude", "projects", "*", "sessions", "*.jsonl")},
		"codex": {
			filepath.Join(home, ".codex", "sessions", "*", "*", "*", "*.jsonl"),
			filepath.Join(home, ".codex", "sessions", "*.jsonl"),
			filepath.Join(home, ".codex", "archived_sessions", "*.jsonl"),
		},
		"codebuddy": {filepath.Join(home, ".codebuddy", "projects", "*", "*.jsonl"), filepath.Join(home, ".codebuddy", "projects", "*", "sessions", "*.jsonl")},
		"workbuddy": {filepath.Join(home, ".workbuddy", "projects", "*", "*.jsonl"), filepath.Join(home, ".workbuddy", "projects", "*", "sessions", "*.jsonl")},
	}
	for id, want := range cases {
		got := TranscriptGlobs(home, id)
		if len(got) != len(want) {
			t.Fatalf("%s globs = %v, want %v", id, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s globs = %v, want %v", id, got, want)
			}
		}
	}
	if got := TranscriptGlobs(home, "doubao"); got != nil {
		t.Fatalf("unsupported host globs = %v, want nil", got)
	}
}

func TestTranscriptSessionIDUsesHostIdentity(t *testing.T) {
	dshPath := filepath.Join("/tmp", ".dsh", "sessions", "--demo--", "session-7e44024f", "session.v4.jsonl.zstd")
	if got, want := TranscriptSessionID("dsh", dshPath), "session-7e44024f"; got != want {
		t.Fatalf("dsh session id = %q, want %q", got, want)
	}
	claudePath := filepath.Join("/tmp", ".claude", "projects", "demo", "abc-123.jsonl")
	if got, want := TranscriptSessionID("claude-code", claudePath), "abc-123"; got != want {
		t.Fatalf("claude session id = %q, want %q", got, want)
	}
	compressed := filepath.Join("/tmp", ".claude", "projects", "demo", "abc-123.jsonl.zstd")
	if got, want := TranscriptSessionID("claude-code", compressed), "abc-123"; got != want {
		t.Fatalf("compressed session id = %q, want %q", got, want)
	}
}
