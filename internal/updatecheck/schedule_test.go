// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package updatecheck

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallRemoveScheduleDryRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file-based scheduler test covers launchd/systemd")
	}
	root := t.TempDir()
	state := t.TempDir()
	t.Setenv("ROLLING_MEMORY_SCHEDULE_DIR", filepath.Join(root, "schedule"))
	t.Setenv("ROLLING_MEMORY_SCHEDULE_DRY_RUN", "1")
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("HOME", root)

	binary := filepath.Join(root, "rolling-memory")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	mirror := "https://mirror.example.com/rolling-memory/releases/latest/download"
	status, err := InstallSchedule(binary, mirror)
	if err != nil {
		t.Fatalf("InstallSchedule: %v", err)
	}
	if !status.Installed || status.Path == "" {
		t.Fatalf("unexpected install status: %#v", status)
	}
	checkPath := status.Path
	if status.Kind == "systemd" {
		checkPath = strings.TrimSuffix(status.Path, ".timer") + ".service"
	}
	raw, err := os.ReadFile(checkPath)
	if err != nil {
		t.Fatalf("scheduler file: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, binary) || !strings.Contains(text, "--auto") || !strings.Contains(text, mirror) {
		t.Fatalf("scheduler file does not reference the binary/auto policy/mirror:\n%s", text)
	}

	current, err := ScheduleStatusInfo()
	if err != nil || !current.Installed {
		t.Fatalf("ScheduleStatusInfo = %#v, %v", current, err)
	}

	removed, err := RemoveSchedule()
	if err != nil {
		t.Fatalf("RemoveSchedule: %v", err)
	}
	if removed.Installed {
		t.Fatalf("unexpected removal status: %#v", removed)
	}
	if _, err := os.Stat(status.Path); !os.IsNotExist(err) {
		t.Fatalf("scheduler file still exists: %v", err)
	}
}

func TestNotifyRecordDeduplicatesByTag(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	if alreadyNotified("0.0.6") {
		t.Fatal("fresh state must not report a notification")
	}
	markNotified("0.0.6")
	if !alreadyNotified("0.0.6") {
		t.Fatal("notification tag was not recorded")
	}
	if alreadyNotified("0.0.7") {
		t.Fatal("a different tag must still notify")
	}
}

func TestFileContentEquals(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plist")
	content := []byte("a\nb\n")
	if fileContentEquals(path, content) {
		t.Fatal("missing file must not equal")
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if !fileContentEquals(path, content) {
		t.Fatal("identical content must equal")
	}
	if fileContentEquals(path, []byte("a\nb\nc\n")) {
		t.Fatal("different content must not equal")
	}
}

func TestLaunchdPlistDeterministic(t *testing.T) {
	binary := "/tmp/rolling-memory"
	plain := launchdPlist(binary, "")
	if !bytes.Equal(plain, launchdPlist(binary, "")) {
		t.Fatal("launchdPlist must be deterministic for identical inputs")
	}
	withBase := launchdPlist(binary, "https://mirror.example.com/rolling-memory/releases/latest/download")
	if bytes.Equal(plain, withBase) {
		t.Fatal("a release base must change the plist")
	}
}
