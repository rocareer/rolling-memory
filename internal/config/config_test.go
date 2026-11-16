// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg-config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg-data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "xdg-state"))
	t.Setenv("ROLLING_MEMORY_CONFIG", "")
	t.Setenv("ROLLING_MEMORY_DB", "")
	t.Setenv("ROLLING_MEMORY_LOG", "")
	t.Setenv("ROLLING_MEMORY_SCOPE", "")
	t.Setenv("ROLLING_MEMORY_MODE", "")
	t.Setenv("ROLLING_MEMORY_READER", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ROLLING_MEMORY_CLOUD_KEY", "")
	return home
}

func writeConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func stringPtr(value string) *string { return &value }

func TestDefaultsAndEnvOverrides(t *testing.T) {
	isolateHome(t)
	tmp := t.TempDir()
	path := filepath.Join(tmp, "missing.toml")
	env := map[string]string{
		"ROLLING_MEMORY_MODE":  "cloud",
		"ROLLING_MEMORY_SCOPE": "mem:task:demo",
		"ROLLING_MEMORY_DB":    filepath.Join(tmp, "x.db"),
	}
	cfg, err := Load(LoadOptions{Path: &path, Env: env})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Mode != "cloud" || cfg.Scope != "mem:task:demo" || cfg.DB != filepath.Join(tmp, "x.db") {
		t.Fatalf("unexpected overrides: %+v", cfg)
	}
	if cfg.LLM.Provider != "none" {
		t.Fatalf("unexpected provider: %+v", cfg.LLM)
	}
	if cfg.Reader != "deliverable" || cfg.AllowLoopbackLLM != true {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.Path != path {
		t.Fatalf("Path = %q, want %q", cfg.Path, path)
	}
	if cfg.Storage.Profile != "local" {
		t.Fatalf("Storage.Profile = %q, want local", cfg.Storage.Profile)
	}
}

func TestPriorityExplicitEnvFileDefault(t *testing.T) {
	isolateHome(t)
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	writeConfig(t, path, `[core]
db = "file.db"
scope = "mem:file"
reader = "owner"
log = "file.log"

[mode]
name = "cloud"
`)
	env := map[string]string{
		"ROLLING_MEMORY_DB":     "env.db",
		"ROLLING_MEMORY_SCOPE":  "mem:env",
		"ROLLING_MEMORY_READER": "deliverable",
		"ROLLING_MEMORY_MODE":   "local",
		"ROLLING_MEMORY_LOG":    "env.log",
	}
	cfg, err := Load(LoadOptions{
		Path:  &path,
		DB:    stringPtr("explicit.db"),
		Mode:  stringPtr("cloud"),
		Scope: stringPtr("mem:explicit"),
		Env:   env,
	})
	if err != nil {
		t.Fatalf("explicit Load: %v", err)
	}
	if cfg.DB != "explicit.db" || cfg.Mode != "cloud" || cfg.Scope != "mem:explicit" {
		t.Fatalf("explicit priority broken: %+v", cfg)
	}
	if cfg.Reader != "deliverable" || cfg.Log != "env.log" {
		t.Fatalf("env priority broken: %+v", cfg)
	}

	cfg, err = Load(LoadOptions{Path: &path, Env: env})
	if err != nil {
		t.Fatalf("env Load: %v", err)
	}
	if cfg.DB != "env.db" || cfg.Scope != "mem:env" || cfg.Reader != "deliverable" ||
		cfg.Mode != "local" || cfg.Log != "env.log" {
		t.Fatalf("env priority broken: %+v", cfg)
	}

	empty := ""
	cfg, err = Load(LoadOptions{Path: &path, DB: &empty, Mode: &empty, Scope: &empty, Reader: &empty, Env: env})
	if err != nil {
		t.Fatalf("empty explicit Load: %v", err)
	}
	if cfg.DB != "env.db" || cfg.Scope != "mem:env" || cfg.Reader != "deliverable" || cfg.Mode != "local" {
		t.Fatalf("empty explicit value did not fall through: %+v", cfg)
	}

	cfg, err = Load(LoadOptions{Path: &path, Env: map[string]string{}})
	if err != nil {
		t.Fatalf("file Load: %v", err)
	}
	if cfg.DB != "file.db" || cfg.Scope != "mem:file" || cfg.Reader != "owner" ||
		cfg.Mode != "cloud" || cfg.Log != "file.log" {
		t.Fatalf("file priority broken: %+v", cfg)
	}

	missing := filepath.Join(tmp, "missing.toml")
	cfg, err = Load(LoadOptions{Path: &missing, Env: map[string]string{}})
	if err != nil {
		t.Fatalf("default Load: %v", err)
	}
	if cfg.Scope != DefaultScope || cfg.Reader != "deliverable" || cfg.Mode != "local" {
		t.Fatalf("default priority broken: %+v", cfg)
	}
	if cfg.DB != DefaultDBPath() {
		t.Fatalf("DB = %q, want default %q", cfg.DB, DefaultDBPath())
	}
}

func TestPathResolution(t *testing.T) {
	home := isolateHome(t)
	if got, want := DefaultConfigPath(), filepath.Join(home, "xdg-config", "rolling-memory", "config.toml"); got != want {
		t.Fatalf("DefaultConfigPath = %q, want %q", got, want)
	}
	if got, want := DefaultDBPath(), filepath.Join(home, "xdg-data", "rolling-memory", "memory.db"); got != want {
		t.Fatalf("DefaultDBPath = %q, want %q", got, want)
	}
	if got, want := DefaultStateDir(), filepath.Join(home, "xdg-state", "rolling-memory"); got != want {
		t.Fatalf("DefaultStateDir = %q, want %q", got, want)
	}

	custom := filepath.Join(t.TempDir(), "custom.toml")
	env := map[string]string{
		"ROLLING_MEMORY_CONFIG": custom,
		"ROLLING_MEMORY_DB":     "~/data/memory.db",
	}
	cfg, err := Load(LoadOptions{Env: env})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Path != custom {
		t.Fatalf("Path = %q, want %q", cfg.Path, custom)
	}
	if want := filepath.Join(home, "data", "memory.db"); cfg.DB != want {
		t.Fatalf("DB = %q, want %q", cfg.DB, want)
	}

	explicit := "~/explicit.toml"
	cfg, err = Load(LoadOptions{Path: &explicit, Env: map[string]string{}})
	if err != nil {
		t.Fatalf("Load explicit tilde: %v", err)
	}
	if want := filepath.Join(home, "explicit.toml"); cfg.Path != want {
		t.Fatalf("Path = %q, want %q", cfg.Path, want)
	}

	t.Setenv("ROLLING_MEMORY_CONFIG", custom)
	cfg, err = Load(LoadOptions{})
	if err != nil {
		t.Fatalf("Load process env: %v", err)
	}
	if cfg.Path != custom {
		t.Fatalf("process env Path = %q, want %q", cfg.Path, custom)
	}

	// Passing an explicit Env map replaces the environment mapping used for
	// ROLLING_MEMORY_CONFIG too; the process variable is not consulted.
	cfg, err = Load(LoadOptions{Env: map[string]string{}})
	if err != nil {
		t.Fatalf("Load empty env: %v", err)
	}
	if cfg.Path != DefaultConfigPath() {
		t.Fatalf("empty env Path = %q, want default %q", cfg.Path, DefaultConfigPath())
	}
}

func TestValidation(t *testing.T) {
	isolateHome(t)
	tmp := t.TempDir()
	path := filepath.Join(tmp, "c.toml")
	cases := []struct {
		name   string
		opts   LoadOptions
		substr string
	}{
		{
			name:   "mode",
			opts:   LoadOptions{Path: &path, Mode: stringPtr("sideways"), Env: map[string]string{}},
			substr: "mode must be one of ('local', 'cloud'), got 'sideways'",
		},
		{
			name:   "reader",
			opts:   LoadOptions{Path: &path, Reader: stringPtr("everyone"), Env: map[string]string{}},
			substr: "reader must be one of ('owner', 'deliverable'), got 'everyone'",
		},
		{
			name:   "scope",
			opts:   LoadOptions{Path: &path, Scope: stringPtr("bad scope"), Env: map[string]string{}},
			substr: "invalid scope 'bad scope'",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := Load(testCase.opts)
			if !errors.Is(err, ErrConfig) {
				t.Fatalf("error = %v, want ErrConfig", err)
			}
			if !strings.Contains(err.Error(), testCase.substr) {
				t.Fatalf("error = %q, want substring %q", err, testCase.substr)
			}
		})
	}

	invalidConfigs := []struct {
		name    string
		content string
		substr  string
	}{
		{"provider", "[llm]\nprovider = \"other\"\n", "llm.provider must be 'none' or 'openai'"},
		{"timeout", "[llm]\ntimeout_sec = 0\n", "timeouts must be positive"},
		{"budget", "[recall]\nbudget_ms = 0\n", "recall.budget_ms must be positive"},
		{"caps", "[hooks]\nmax_traces = 0\n", "caps and batch sizes must be positive"},
	}
	for _, testCase := range invalidConfigs {
		t.Run(testCase.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config.toml")
			writeConfig(t, configPath, testCase.content)
			_, err := Load(LoadOptions{Path: &configPath, Env: map[string]string{}})
			if !errors.Is(err, ErrConfig) || !strings.Contains(err.Error(), testCase.substr) {
				t.Fatalf("error = %v, want ErrConfig containing %q", err, testCase.substr)
			}
		})
	}

	// A stale profile key in the config file changes nothing: the tier axis is
	// retired, so local mode stays local.
	localProPath := filepath.Join(t.TempDir(), "local-pro.toml")
	writeConfig(t, localProPath, "[mode]\nname = \"local\"\nprofile = \"pro\"\n")
	localProCfg, err := Load(LoadOptions{Path: &localProPath, Env: map[string]string{}})
	if err != nil {
		t.Fatalf("local+profile Load failed: %v", err)
	}
	if localProCfg.Mode != "local" {
		t.Fatalf("local+profile got Mode=%q, want local", localProCfg.Mode)
	}
}

func TestEgressPolicy(t *testing.T) {
	isolateHome(t)
	tmp := t.TempDir()
	path := filepath.Join(tmp, "c.toml")
	local, err := Load(LoadOptions{Path: &path, Mode: stringPtr("local"), Env: map[string]string{}})
	if err != nil {
		t.Fatalf("local Load: %v", err)
	}
	for _, target := range []string{
		"http://127.0.0.1:11434/v1/chat/completions",
		"http://localhost:8080/v1",
		"http://[::1]:8080/v1",
		"http://foo.localhost/v1",
		"HTTP://LOCALHOST:8080/v1",
	} {
		if err := local.CheckEgress(target); err != nil {
			t.Errorf("local CheckEgress(%q) = %v, want nil", target, err)
		}
	}
	err = local.CheckEgress("https://api.openai.com/v1/chat/completions")
	if !errors.Is(err, ErrEgressDenied) {
		t.Fatalf("remote egress error = %v, want ErrEgressDenied", err)
	}
	if !strings.Contains(err.Error(), "local mode blocks non-loopback egress to api.openai.com") {
		t.Errorf("unexpected egress message: %q", err)
	}
	if err := local.CheckEgress("ftp://127.0.0.1/x"); !errors.Is(err, ErrEgressDenied) {
		t.Errorf("ftp scheme error = %v, want ErrEgressDenied", err)
	}

	cloud, err := Load(LoadOptions{Path: &path, Mode: stringPtr("cloud"), Env: map[string]string{}})
	if err != nil {
		t.Fatalf("cloud Load: %v", err)
	}
	if err := cloud.CheckEgress("https://api.openai.com/v1/chat/completions"); err != nil {
		t.Errorf("cloud CheckEgress = %v, want nil", err)
	}

	noLoopbackPath := filepath.Join(tmp, "no-loopback.toml")
	writeConfig(t, noLoopbackPath, "[mode]\nallow_loopback_llm = false\n")
	noLoopback, err := Load(LoadOptions{Path: &noLoopbackPath, Mode: stringPtr("local"), Env: map[string]string{}})
	if err != nil {
		t.Fatalf("no-loopback Load: %v", err)
	}
	if err := noLoopback.CheckEgress("http://127.0.0.1:11434"); !errors.Is(err, ErrEgressDenied) {
		t.Errorf("loopback with allow_loopback_llm=false = %v, want ErrEgressDenied", err)
	}
}

func TestSecretFilePermissions(t *testing.T) {
	isolateHome(t)
	tmp := t.TempDir()
	path := filepath.Join(tmp, "c.toml")
	cfg, err := Load(LoadOptions{Path: &path, Mode: stringPtr("cloud"), Env: map[string]string{}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	secret := filepath.Join(tmp, "key")
	if err := os.WriteFile(secret, []byte("secret-value\n"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	cfg.LLM.APIKeyFile = secret
	if got, err := cfg.LLMKey(); err != nil || got != "secret-value" {
		t.Fatalf("LLMKey = (%q, %v), want secret-value", got, err)
	}
	cfg.LLM.APIKeyEnv = ""
	status := cfg.SecretStatus()
	if status["llm"]["configured"] != true || status["llm"]["source"] != "file" {
		t.Fatalf("file secret status = %+v", status["llm"])
	}
	if err := os.Chmod(secret, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if _, err := cfg.LLMKey(); !errors.Is(err, ErrConfig) ||
		!strings.Contains(err.Error(), "must not be group/other accessible") {
		t.Fatalf("permission error = %v, want ErrConfig", err)
	}

	t.Setenv("RM_TEST_KEY", "from-env")
	cfg.LLM.APIKeyEnv = "RM_TEST_KEY"
	if got, err := cfg.LLMKey(); err != nil || got != "from-env" {
		t.Fatalf("env LLMKey = (%q, %v), want from-env", got, err)
	}

	empty := filepath.Join(tmp, "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatalf("write empty: %v", err)
	}
	cfg.LLM.APIKeyEnv = ""
	cfg.LLM.APIKeyFile = empty
	if _, err := cfg.LLMKey(); !errors.Is(err, ErrConfig) ||
		!strings.Contains(err.Error(), "list index out of range") {
		t.Fatalf("empty secret error = %v, want index error", err)
	}
}

func TestSecretStatusAndPublicDict(t *testing.T) {
	isolateHome(t)
	t.Setenv("OPENAI_API_KEY", "super-secret")
	tmp := t.TempDir()
	path := filepath.Join(tmp, "c.toml")
	cfg, err := Load(LoadOptions{Path: &path, Mode: stringPtr("cloud"), Env: map[string]string{}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	status := cfg.SecretStatus()
	if status["llm"]["configured"] != true || status["llm"]["source"] != "env" {
		t.Fatalf("llm secret status = %+v", status["llm"])
	}
	public := cfg.PublicDict()
	serialized := strings.ToLower(strings.Join(flatten(public), " "))
	if strings.Contains(serialized, "super-secret") {
		t.Fatalf("PublicDict leaked a secret: %v", public)
	}
	if public["egress"] != "remote-allowed" || public["mode"] != "cloud" {
		t.Fatalf("unexpected public dict: %+v", public)
	}
	recall, ok := public["recall"].(map[string]any)
	if !ok || len(recall) != 13 {
		t.Fatalf("recall public shape = %#v", public["recall"])
	}
	llm, ok := public["llm"].(map[string]any)
	if !ok {
		t.Fatalf("llm public shape = %#v", public["llm"])
	}
	if _, ok := llm["key"].(map[string]any); !ok {
		t.Fatalf("llm.key shape = %#v", llm["key"])
	}

	local, err := Load(LoadOptions{Path: &path, Mode: stringPtr("local"), Env: map[string]string{}})
	if err != nil {
		t.Fatalf("local Load: %v", err)
	}
	if local.PublicDict()["egress"] != "loopback-only" {
		t.Fatalf("local egress = %v", local.PublicDict()["egress"])
	}
}

func flatten(value any) []string {
	result := []string{}
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			result = append(result, key)
			result = append(result, flatten(item)...)
		}
	case []any:
		for _, item := range typed {
			result = append(result, flatten(item)...)
		}
	case string:
		result = append(result, typed)
	case bool:
		if typed {
			result = append(result, "true")
		} else {
			result = append(result, "false")
		}
	default:
		result = append(result, fmt.Sprint(typed))
	}
	return result
}

func TestWriteDefaultAndSetConfigValue(t *testing.T) {
	isolateHome(t)
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	written, err := WriteDefault(path)
	if err != nil {
		t.Fatalf("WriteDefault: %v", err)
	}
	if written != path {
		t.Fatalf("WriteDefault = %q, want %q", written, path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read default: %v", err)
	}
	if string(raw) != DefaultConfigText {
		t.Fatalf("default config text mismatch")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat default: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("default mode = %o, want 600", info.Mode().Perm())
	}
	if err := os.WriteFile(path, append(raw, []byte("# marker\n")...), 0o600); err != nil {
		t.Fatalf("append marker: %v", err)
	}
	if _, err := WriteDefault(path); err != nil {
		t.Fatalf("WriteDefault existing: %v", err)
	}
	raw, _ = os.ReadFile(path)
	if !strings.Contains(string(raw), "# marker") {
		t.Fatalf("WriteDefault overwrote an existing file")
	}

	if _, err := SetConfigValue(path, "mode.name", "cloud"); err != nil {
		t.Fatalf("SetConfigValue mode: %v", err)
	}
	raw, _ = os.ReadFile(path)
	if !strings.Contains(string(raw), "# local = no non-loopback network request") {
		t.Fatalf("comments were not preserved:\n%s", raw)
	}
	cfg, err := Load(LoadOptions{Path: &path, Env: map[string]string{}})
	if err != nil {
		t.Fatalf("Load after set: %v", err)
	}
	if cfg.Mode != "cloud" {
		t.Fatalf("mode = %q, want cloud", cfg.Mode)
	}
	if _, err := SetConfigValue(path, "core.scope", "mem:task:demo"); err != nil {
		t.Fatalf("SetConfigValue scope: %v", err)
	}
	cfg, err = Load(LoadOptions{Path: &path, Env: map[string]string{}})
	if err != nil {
		t.Fatalf("Load after scope: %v", err)
	}
	if cfg.Scope != "mem:task:demo" {
		t.Fatalf("scope = %q", cfg.Scope)
	}
	info, _ = os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode after SetConfigValue = %o, want 600", info.Mode().Perm())
	}

	if _, err := SetConfigValue(path, "recall.extra_key", "42"); err != nil {
		t.Fatalf("insert key: %v", err)
	}
	if _, err := SetConfigValue(path, "extra.flag", "true"); err != nil {
		t.Fatalf("append section: %v", err)
	}
	if _, err := SetConfigValue(path, "cloud.sync.enabled", "true"); err != nil {
		t.Fatalf("nested section: %v", err)
	}
	if _, err := SetConfigValue(path, "core.scope", "mem:task:demo2"); err != nil {
		t.Fatalf("quoted value: %v", err)
	}
	if _, err := SetConfigValue(path, "core.log", "with space.log"); err != nil {
		t.Fatalf("spaced value: %v", err)
	}
	raw, _ = os.ReadFile(path)
	text := string(raw)
	if !strings.Contains(text, "extra_key = 42") || !strings.Contains(text, "[extra]\nflag = true") {
		t.Fatalf("unexpected line edits:\n%s", text)
	}
	if !strings.Contains(text, "enabled = true") {
		t.Fatalf("nested section not updated:\n%s", text)
	}
	if !strings.Contains(text, `scope = "mem:task:demo2"`) {
		t.Fatalf("string literal not JSON-quoted:\n%s", text)
	}
	if !strings.Contains(text, `log = "with space.log"`) {
		t.Fatalf("spaced string not JSON-quoted:\n%s", text)
	}
	cfg, err = Load(LoadOptions{Path: &path, Env: map[string]string{}})
	if err != nil {
		t.Fatalf("Load after nested set: %v", err)
	}
	if !cfg.Cloud.Enabled {
		t.Fatalf("cloud.sync.enabled = false, want true")
	}
	if _, err := SetConfigValue(path, "nokey", "x"); !errors.Is(err, ErrConfig) {
		t.Fatalf("bad key error = %v, want ErrConfig", err)
	}
}

func TestRecallPruneKnobsConfig(t *testing.T) {
	isolateHome(t)
	tmp := t.TempDir()
	path := filepath.Join(tmp, "prune.toml")
	writeConfig(t, path, `[recall]
prune_score_ratio = 0.65
prune_min_info_lcs = 3.2
prune_min_title_cov = 0.30
preprobe_limit = 1024
`)
	cfg, err := Load(LoadOptions{Path: &path, Env: map[string]string{}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Recall.PruneScoreRatio != 0.65 || cfg.Recall.PruneMinInfoLCS != 3.2 ||
		cfg.Recall.PruneMinTitleCov != 0.30 || cfg.Recall.PreprobeLimit != 1024 {
		t.Fatalf("unexpected prune knobs: %+v", cfg.Recall)
	}
}

func TestSetConfigValueCreatesDefault(t *testing.T) {
	isolateHome(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	if _, err := SetConfigValue(path, "core.scope", "mem:new"); err != nil {
		t.Fatalf("SetConfigValue: %v", err)
	}
	cfg, err := Load(LoadOptions{Path: &path, Env: map[string]string{}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Scope != "mem:new" {
		t.Fatalf("scope = %q, want mem:new", cfg.Scope)
	}
}

func TestConfigReadError(t *testing.T) {
	isolateHome(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	writeConfig(t, path, "[core\n")
	_, err := Load(LoadOptions{Path: &path, Env: map[string]string{}})
	if !errors.Is(err, ErrConfig) || !strings.Contains(err.Error(), "cannot read config") {
		t.Fatalf("error = %v, want ErrConfig read error", err)
	}
}

func TestBooleanCapsAndStorage(t *testing.T) {
	isolateHome(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	writeConfig(t, path, `[mode]
allow_loopback_llm = "no"

[recall]
raw_when_sparse = "off"
vector = 0
token_correct = "yes"
alias_expand = 1
relative_tag = "false"
mention_backfill = 2

[hooks]
boot_context = "on"
ledger = 0

[storage]
profile = "usb"
`)
	cfg, err := Load(LoadOptions{
		Path: &path,
		Env:  map[string]string{"ROLLING_MEMORY_EMBEDDING_DIM": "64"},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AllowLoopbackLLM || cfg.Recall.RawWhenSparse || cfg.Recall.Vector ||
		!cfg.Recall.TokenCorrect || !cfg.Recall.AliasExpand || cfg.Recall.RelativeTag ||
		!cfg.Recall.MentionBackfill {
		t.Fatalf("unexpected recall booleans: %+v allow=%v", cfg.Recall, cfg.AllowLoopbackLLM)
	}
	if !cfg.Hooks.BootContext || cfg.Hooks.Ledger {
		t.Fatalf("unexpected hooks booleans: %+v", cfg.Hooks)
	}
	if cfg.Storage.Profile != "usb" {
		t.Fatalf("storage profile = %q, want usb", cfg.Storage.Profile)
	}
}

func TestEnsureCreatesParents(t *testing.T) {
	isolateHome(t)
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	dbPath := filepath.Join(tmp, "data", "memory.db")
	logPath := filepath.Join(tmp, "state", "rolling-memory.log")
	cfg, err := Load(LoadOptions{
		Path: &path,
		DB:   &dbPath,
		Env:  map[string]string{"ROLLING_MEMORY_LOG": logPath},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := cfg.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	for _, directory := range []string{filepath.Dir(dbPath), filepath.Dir(logPath)} {
		info, err := os.Stat(directory)
		if err != nil || !info.IsDir() {
			t.Fatalf("directory %q missing: %v", directory, err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("directory %q mode = %o, want 700", directory, info.Mode().Perm())
		}
	}
}

func TestEnsurePreservesExistingParents(t *testing.T) {
	isolateHome(t)
	root := t.TempDir()
	dbDir := filepath.Join(root, "shared-db")
	logDir := filepath.Join(root, "shared-log")
	for _, directory := range []string{dbDir, logDir} {
		if err := os.Mkdir(directory, 0o755); err != nil {
			t.Fatalf("mkdir %q: %v", directory, err)
		}
		if err := os.Chmod(directory, 0o755); err != nil {
			t.Fatalf("chmod %q: %v", directory, err)
		}
	}
	path := filepath.Join(root, "config.toml")
	dbPath := filepath.Join(dbDir, "memory.db")
	logPath := filepath.Join(logDir, "memory.log")
	cfg, err := Load(LoadOptions{Path: &path, DB: &dbPath, Env: map[string]string{"ROLLING_MEMORY_LOG": logPath}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := cfg.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	for _, directory := range []string{dbDir, logDir} {
		info, err := os.Stat(directory)
		if err != nil {
			t.Fatalf("stat %q: %v", directory, err)
		}
		if info.Mode().Perm() != 0o755 {
			t.Errorf("directory %q mode = %o, want 755", directory, info.Mode().Perm())
		}
	}
}

func TestPortableRootAndConfigResolution(t *testing.T) {
	isolateHome(t)
	usbRoot := t.TempDir()
	binDir := filepath.Join(usbRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	fakeExe := filepath.Join(binDir, "rolling-memory")
	if err := os.WriteFile(fakeExe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake exe: %v", err)
	}
	if err := os.WriteFile(filepath.Join(usbRoot, ".portable"), []byte("1\n"), 0o644); err != nil {
		t.Fatalf("write .portable: %v", err)
	}

	gotRoot := PortableRootForExecutable(fakeExe)
	if gotRoot != usbRoot {
		t.Fatalf("PortableRootForExecutable(bundle) = %q, want %q", gotRoot, usbRoot)
	}

	linkDir := t.TempDir()
	symlinkExe := filepath.Join(linkDir, "rolling-memory")
	if err := os.Symlink(fakeExe, symlinkExe); err == nil {
		gotViaSymlink := PortableRootForExecutable(symlinkExe)
		evalWant := usbRoot
		if resolvedRoot, evalErr := filepath.EvalSymlinks(usbRoot); evalErr == nil {
			evalWant = resolvedRoot
		}
		if gotViaSymlink != usbRoot && gotViaSymlink != evalWant {
			t.Fatalf("PortableRootForExecutable(symlink) = %q, want %q", gotViaSymlink, evalWant)
		}
	}

	t.Setenv("ROLLING_MEMORY_PORTABLE", usbRoot)
	cfgPath := DefaultConfigPath()
	if _, err := WriteDefault(cfgPath); err != nil {
		t.Fatalf("WriteDefault portable: %v", err)
	}
	cfg, err := Load(LoadOptions{})
	if err != nil {
		t.Fatalf("Load portable after WriteDefault: %v", err)
	}
	wantDB := filepath.Join(usbRoot, "data", "memory.db")
	if cfg.DB != wantDB {
		t.Fatalf("portable cfg.DB after WriteDefault = %q, want %q", cfg.DB, wantDB)
	}
	if cfg.Storage.Profile != "usb" {
		t.Fatalf("portable cfg.Storage.Profile = %q, want usb", cfg.Storage.Profile)
	}
}

func TestRetiredEmbeddingAndRerankTablesIgnoredAndEgressBlocked(t *testing.T) {
	isolateHome(t)
	tmp := t.TempDir()

	// 1. Stale [embedding] table (even with invalid provider/dim) is ignored by Load
	// and omitted from PublicDict() / SecretStatus().
	staleEmbedPath := filepath.Join(tmp, "stale-embed.toml")
	writeConfig(t, staleEmbedPath, "[mode]\nname = \"local\"\nprofile = \"pro\"\n\n[embedding]\nprovider = \"bogus-provider\"\ndim = 8\n")
	cfgStale, err := Load(LoadOptions{
		Path: &staleEmbedPath,
		Env:  map[string]string{"ROLLING_MEMORY_EMBEDDING_DIM": "8"},
	})
	if err != nil {
		t.Fatalf("expected stale [embedding] table to be ignored by Load, got: %v", err)
	}
	if _, hasEmbed := cfgStale.PublicDict()["embedding"]; hasEmbed {
		t.Fatalf("PublicDict must not expose retired embedding section: %#v", cfgStale.PublicDict())
	}
	if _, hasEmbedSec := cfgStale.SecretStatus()["embedding"]; hasEmbedSec {
		t.Fatalf("SecretStatus must not expose retired embedding key: %#v", cfgStale.SecretStatus())
	}

	// 2. Local mode rejects advanced_ops = true, while Cloud mode allows it
	advLocalPath := filepath.Join(tmp, "adv-local.toml")
	writeConfig(t, advLocalPath, "[mode]\nname = \"local\"\n\n[recall]\nadvanced_ops = true\n")
	if _, err := Load(LoadOptions{Path: &advLocalPath, Env: map[string]string{}}); err == nil {
		t.Fatalf("expected local mode to reject recall.advanced_ops = true")
	}

	advCloudPath := filepath.Join(tmp, "adv-cloud.toml")
	writeConfig(t, advCloudPath, "[mode]\nname = \"cloud\"\n\n[recall]\nadvanced_ops = true\n")
	cfgCloudAdv, err := Load(LoadOptions{Path: &advCloudPath, Env: map[string]string{}})
	if err != nil || !cfgCloudAdv.Recall.AdvancedOps {
		t.Fatalf("expected cloud mode to allow recall.advanced_ops = true, err=%v", err)
	}

	// 3. Retired [rerank] table in TOML is ignored (even with invalid provider) and
	// no longer opens a non-loopback egress hole in local mode.
	rerankLocalPath := filepath.Join(tmp, "rerank-local.toml")
	writeConfig(t, rerankLocalPath, "[mode]\nname = \"local\"\n\n[rerank]\nenabled = true\nprovider = \"bogus-provider\"\nbase_url = \"https://dashscope.aliyuncs.com\"\napi_key_env = \"RM_TEST_RERANK_KEY\"\n")
	t.Setenv("RM_TEST_RERANK_KEY", "sk-user-custom-key")
	cfgLocalRerank, err := Load(LoadOptions{Path: &rerankLocalPath, Env: map[string]string{}})
	if err != nil {
		t.Fatalf("expected stale [rerank] table to be ignored by Load, got: %v", err)
	}
	if err := cfgLocalRerank.CheckEgress("https://dashscope.aliyuncs.com/api/v1/services/rerank/text-rerank/text-rerank"); err == nil {
		t.Fatalf("expected local mode to block retired rerank non-loopback egress")
	}

	// 4. Local mode blocks non-loopback egress for retired remote embedding provider as well.
	embedLocalPath := filepath.Join(tmp, "embed-local.toml")
	writeConfig(t, embedLocalPath, "[mode]\nname = \"local\"\n\n[embedding]\nprovider = \"openai\"\nbase_url = \"https://embed.example.com/v1\"\napi_key_env = \"RM_TEST_EMBED_KEY\"\n")
	t.Setenv("RM_TEST_EMBED_KEY", "sk-user-embed-key")
	cfgLocalEmbed, err := Load(LoadOptions{Path: &embedLocalPath, Env: map[string]string{}})
	if err != nil {
		t.Fatalf("Load local embed: %v", err)
	}
	if err := cfgLocalEmbed.CheckEgress("https://embed.example.com/v1/embeddings"); err == nil {
		t.Fatalf("expected local mode to block retired remote embedding non-loopback egress")
	}
}
