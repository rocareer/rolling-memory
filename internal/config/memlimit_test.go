// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseMemoryLimit(t *testing.T) {
	cases := []struct {
		raw       string
		canonical string
		bytes     int64
		ok        bool
	}{
		{"", MemoryLimitAuto, 0, true},
		{"auto", MemoryLimitAuto, 0, true},
		{" AUTO ", MemoryLimitAuto, 0, true},
		{"off", MemoryLimitOff, 0, true},
		{"512MiB", "512MiB", 512 << 20, true},
		{"512mib", "512MiB", 512 << 20, true},
		{"512 MB", "512MiB", 512 << 20, true},
		{"2GiB", "2GiB", 2 << 30, true},
		{"2g", "2GiB", 2 << 30, true},
		{"1048576KiB", "1GiB", 1 << 30, true},
		{"1073741824", "1GiB", 1 << 30, true},
		{"1500B", "", 0, false},            // below the 64MiB explicit floor
		{"1024", "", 0, false},             // bare digits are bytes; too small
		{"0", "", 0, false},                // zero is not a size
		{"-2GiB", "", 0, false},            // negative rejected
		{"2.5GiB", "", 0, false},           // fractions rejected
		{"2XiB", "", 0, false},             // unknown unit
		{"1099511627777GiB", "", 0, false}, // overflows int64
		{"1025TiB", "", 0, false},          // above the 1TiB explicit ceiling
		{"64MiB", "64MiB", 64 << 20, true}, // exact floor accepted
		{"1TiB", "1TiB", 1 << 40, true},    // exact ceiling accepted
	}
	for _, tc := range cases {
		canonical, bytes, ok := ParseMemoryLimit(tc.raw)
		if ok != tc.ok || canonical != tc.canonical || bytes != tc.bytes {
			t.Fatalf("ParseMemoryLimit(%q) = (%q, %d, %v), want (%q, %d, %v)",
				tc.raw, canonical, bytes, ok, tc.canonical, tc.bytes, tc.ok)
		}
	}
}

func TestResolveMemoryLimit(t *testing.T) {
	const gib = int64(1 << 30)
	cases := []struct {
		raw      string
		totalRAM int64
		bytes    int64
		source   string
	}{
		// 16 GiB machine: 1/8 = 2 GiB.
		{"auto", 16 * gib, 2 * gib, "auto"},
		// 8 GiB machine: 1/8 = 1 GiB, exactly at the floor.
		{"auto", 8 * gib, 1 * gib, "auto"},
		// 4 GiB machine: 1/8 = 512 MiB, clamped up to the 1 GiB floor.
		{"auto", 4 * gib, 1 * gib, "auto"},
		// 128 GiB machine: 1/8 = 16 GiB, clamped down to the 4 GiB ceiling.
		{"auto", 128 * gib, 4 * gib, "auto"},
		// Failed probe: no cap instead of a guessed value.
		{"auto", 0, 0, "unavailable"},
		{"off", 16 * gib, 0, "off"},
		{"512MiB", 128 * gib, 512 << 20, "explicit"},
	}
	for _, tc := range cases {
		got := ResolveMemoryLimit(tc.raw, tc.totalRAM)
		if got.Bytes != tc.bytes || got.Source != tc.source {
			t.Fatalf("ResolveMemoryLimit(%q, %d) = (%d, %q), want (%d, %q)",
				tc.raw, tc.totalRAM, got.Bytes, got.Source, tc.bytes, tc.source)
		}
	}
}

func TestDaemonMemoryLimitConfigRoundTrip(t *testing.T) {
	home := isolateHome(t)
	t.Setenv("ROLLING_MEMORY_DAEMON_MEMORY_LIMIT", "")
	configPath := filepath.Join(home, "config.toml")
	if _, err := WriteDefault(configPath); err != nil {
		t.Fatalf("write default: %v", err)
	}
	cfg, err := Load(LoadOptions{Path: stringPtr(configPath)})
	if err != nil {
		t.Fatalf("load default: %v", err)
	}
	if cfg.Daemon.MemoryLimit != MemoryLimitAuto {
		t.Fatalf("default daemon.memory_limit = %q, want %q", cfg.Daemon.MemoryLimit, MemoryLimitAuto)
	}
	if _, err := SetConfigValue(configPath, "daemon.memory_limit", "512MiB"); err != nil {
		t.Fatalf("set memory limit: %v", err)
	}
	cfg, err = Load(LoadOptions{Path: stringPtr(configPath)})
	if err != nil {
		t.Fatalf("load after set: %v", err)
	}
	if cfg.Daemon.MemoryLimit != "512MiB" {
		t.Fatalf("daemon.memory_limit = %q, want 512MiB", cfg.Daemon.MemoryLimit)
	}
	if _, err := SetConfigValue(configPath, "daemon.memory_limit", "nonsense"); err == nil {
		t.Fatal("expected invalid memory limit to be rejected")
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(raw), `memory_limit = "512MiB"`) {
		t.Fatalf("config file lost the valid value after a rejected set:\n%s", raw)
	}
}

func TestDaemonMemoryLimitEnvOverride(t *testing.T) {
	home := isolateHome(t)
	configPath := filepath.Join(home, "config.toml")
	if _, err := WriteDefault(configPath); err != nil {
		t.Fatalf("write default: %v", err)
	}
	t.Setenv("ROLLING_MEMORY_DAEMON_MEMORY_LIMIT", "1GiB")
	cfg, err := Load(LoadOptions{Path: stringPtr(configPath)})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Daemon.MemoryLimit != "1GiB" {
		t.Fatalf("env override failed: %q", cfg.Daemon.MemoryLimit)
	}
	t.Setenv("ROLLING_MEMORY_DAEMON_MEMORY_LIMIT", "bad-value")
	if _, err := Load(LoadOptions{Path: stringPtr(configPath)}); err == nil {
		t.Fatal("expected invalid env memory limit to fail validation")
	}
}

// TestWriteDefaultKeepsXDGResolvedPaths pins the property the daemon identity
// relies on: creating the config file must not move the vault or the log when
// XDG_DATA_HOME / XDG_STATE_HOME are set, otherwise the next daemon handshake
// sees a different database path and refuses to restart.
func TestWriteDefaultKeepsXDGResolvedPaths(t *testing.T) {
	home := isolateHome(t)
	configPath := filepath.Join(home, "config.toml")
	before, err := Load(LoadOptions{Path: stringPtr(configPath)})
	if err != nil {
		t.Fatalf("load before write: %v", err)
	}
	if _, err := WriteDefault(configPath); err != nil {
		t.Fatalf("write default: %v", err)
	}
	after, err := Load(LoadOptions{Path: stringPtr(configPath)})
	if err != nil {
		t.Fatalf("load after write: %v", err)
	}
	if before.DB != after.DB {
		t.Fatalf("database path moved when the config file was created: %q -> %q", before.DB, after.DB)
	}
	if before.Log != after.Log {
		t.Fatalf("log path moved when the config file was created: %q -> %q", before.Log, after.Log)
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(raw), tomlBasicString(before.DB)) {
		t.Fatalf("config file does not record the XDG database path %q (TOML form %q):\n%s",
			before.DB, tomlBasicString(before.DB), raw)
	}
}

// TestWriteDefaultEscapesXDGPathsInBasicStrings covers the Windows path shape:
// the XDG defaults contain backslashes there, and an unescaped `\U...` inside a
// TOML basic string is read as a unicode escape, so the config that WriteDefault
// wrote could not be loaded back. A backslash in a POSIX directory name
// reproduces the same text on every platform.
func TestWriteDefaultEscapesXDGPathsInBasicStrings(t *testing.T) {
	home := isolateHome(t)
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, `data\Uhome`))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, `state\Uhome`))
	configPath := filepath.Join(home, "config.toml")
	before, err := Load(LoadOptions{Path: stringPtr(configPath)})
	if err != nil {
		t.Fatalf("load before write: %v", err)
	}
	if _, err := WriteDefault(configPath); err != nil {
		t.Fatalf("write default: %v", err)
	}
	after, err := Load(LoadOptions{Path: stringPtr(configPath)})
	if err != nil {
		t.Fatalf("config written by WriteDefault does not load back: %v", err)
	}
	if before.DB != after.DB {
		t.Fatalf("database path moved when the config file was created: %q -> %q", before.DB, after.DB)
	}
	if before.Log != after.Log {
		t.Fatalf("log path moved when the config file was created: %q -> %q", before.Log, after.Log)
	}
}
