// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// seedUnimportableDSHStore writes DeepSeek Harness transcripts in the host layout
// under a private DSH_HOME and returns the sessions root.
func seedUnimportableDSHStore(t *testing.T, home string, mtime time.Time) string {
	t.Helper()
	dshHome := filepath.Join(home, "dsh-home")
	t.Setenv("DSH_HOME", dshHome)
	sessions := filepath.Join(dshHome, "sessions")
	for _, dir := range []string{"session-aaa", "bbb"} {
		path := filepath.Join(sessions, "--demo-repo--", dir, "session.v4.jsonl.zstd")
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
	return sessions
}

// TestDiscoverEpisodicScanSourcesSkipsHostsWithoutATurnReader pins the scope of
// the importer: a host whose transcripts this build cannot parse (DSH: zstd
// frames) is counted in the console inventory but never enumerated here, because
// reading them would hold the shared scan cursor on files that keep growing and
// starve the hosts that do import. Hosts with a turn reader are untouched.
func TestDiscoverEpisodicScanSourcesSkipsHostsWithoutATurnReader(t *testing.T) {
	home := t.TempDir()
	seedUnimportableDSHStore(t, home, time.Now())
	claudePath := filepath.Join(home, ".claude", "projects", "demo", "s1.jsonl")
	if err := os.MkdirAll(filepath.Dir(claudePath), 0o755); err != nil {
		t.Fatalf("mkdir claude: %v", err)
	}
	if err := os.WriteFile(claudePath, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write claude transcript: %v", err)
	}

	dshSources, err := discoverEpisodicScanSources(context.Background(), home, "dsh")
	if err != nil {
		t.Fatalf("discover dsh: %v", err)
	}
	if len(dshSources) != 0 {
		t.Fatalf("dsh scan sources = %d, want 0 while no turn reader exists: %+v", len(dshSources), dshSources)
	}
	claudeSources, err := discoverEpisodicScanSources(context.Background(), home, "claude-code")
	if err != nil {
		t.Fatalf("discover claude: %v", err)
	}
	if len(claudeSources) != 1 {
		t.Fatalf("claude-code scan sources = %d, want 1", len(claudeSources))
	}
}

// TestHarnessTranscriptPatternsOnlyCoverImportableHosts keeps the change detector
// aligned with the importer: a host that cannot be imported must not keep the
// auto-sync gate dirty with its own writes.
func TestHarnessTranscriptPatternsOnlyCoverImportableHosts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("DSH_HOME", filepath.Join(home, "dsh-home"))
	patterns := harnessTranscriptPatterns(home)
	if len(patterns) == 0 {
		t.Fatal("transcript patterns are empty")
	}
	for _, pattern := range patterns {
		if strings.Contains(pattern, ".dsh") {
			t.Fatalf("non-importable host leaked into the change detector: %q", pattern)
		}
	}
	joined := strings.Join(patterns, "\n")
	for _, want := range []string{".claude", ".codex", ".zcode"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("importable host missing from the change detector: %s", want)
		}
	}
}

// TestLatestHarnessTranscriptMTimeIgnoresUnimportableTranscipts locks the same
// boundary at the mtime level: appending to a DSH transcript cannot arm a
// reconcile pass that has nothing to import.
func TestLatestHarnessTranscriptMTimeIgnoresUnimportableTranscipts(t *testing.T) {
	home := t.TempDir()
	base := time.Unix(1700000000, 0)
	sessions := seedUnimportableDSHStore(t, home, base)
	_ = os.Chtimes(sessions, base, base)
	before := latestHarnessTranscriptMTime(home)
	target := filepath.Join(sessions, "--demo-repo--", "session-aaa", "session.v4.jsonl.zstd")
	newer := base.Add(30 * time.Minute)
	if err := os.Chtimes(target, newer, newer); err != nil {
		t.Fatalf("chtimes dsh transcript: %v", err)
	}
	if after := latestHarnessTranscriptMTime(home); after != before {
		t.Fatalf("latest transcript mtime moved with an unimportable store: %d -> %d", before, after)
	}
}
