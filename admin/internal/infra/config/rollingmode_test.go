package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseRollingConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := `# 注释行
[mode]
name = "local"

[hooks]
boot_context = false

[cloud.sync]
enabled = false
auto_watch = manual
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := ParseRollingConfig(path)
	want := map[string]string{
		"mode.name":             "local",
		"hooks.boot_context":    "false",
		"cloud.sync.enabled":    "false",
		"cloud.sync.auto_watch": "manual",
	}
	for k, v := range want {
		if cfg[k] != v {
			t.Fatalf("cfg[%q]=%q want %q (all=%#v)", k, cfg[k], v, cfg)
		}
	}
	if got := ParseRollingConfig(filepath.Join(dir, "absent.toml")); len(got) != 0 {
		t.Fatalf("missing file must yield empty map, got %#v", got)
	}
}

func TestRollingModeAndCaptchaDefault(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	t.Setenv("ROLLING_MEMORY_CONFIG", cfgPath)
	t.Setenv("ROLLING_MEMORY_MODE", "")

	cases := []struct {
		name     string
		content  string
		envMode  string
		wantMode string
		wantCap  bool
	}{
		{"file_local", "[mode]\nname = \"local\"\n", "", "local", false},
		{"file_cloud", "[mode]\nname = \"cloud\"\n", "", "cloud", true},
		{"file_uppercase_local", "[mode]\nname = \"Local\"\n", "", "local", false},
		{"missing_file_defaults_local", "", "", "local", false},
		{"unknown_value_stays_strict", "[mode]\nname = \"weird\"\n", "", "weird", true},
		{"env_overrides_file", "[mode]\nname = \"cloud\"\n", "local", "local", false},
	}
	for _, tc := range cases {
		if tc.content == "" {
			_ = os.Remove(cfgPath)
		} else if err := os.WriteFile(cfgPath, []byte(tc.content), 0o644); err != nil {
			t.Fatalf("%s: write config: %v", tc.name, err)
		}
		t.Setenv("ROLLING_MEMORY_MODE", tc.envMode)
		if got := RollingMode(); got != tc.wantMode {
			t.Fatalf("%s: RollingMode()=%q want %q", tc.name, got, tc.wantMode)
		}
		if got := AdminLoginCaptchaDefault(); got != tc.wantCap {
			t.Fatalf("%s: AdminLoginCaptchaDefault()=%v want %v", tc.name, got, tc.wantCap)
		}
	}
}
