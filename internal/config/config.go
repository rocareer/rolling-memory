// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package config implements configuration, paths, modes and the network
// egress policy.
//
// The policy is deliberately small and enforced at every network call:
//
//   - local  -- product runtime reaches only loopback (localhost / 127.0.0.1 /
//     ::1) through CheckEgress.
//   - local  -- evaluation LLM calls (`rm eval` answerer/judge) use the
//     user-configured OpenAI-compatible endpoint, which is an explicit
//     per-run choice (config file or --eval-base-url), so it may leave the
//     machine to any http(s) host (see CheckEvalEgress).
//
// No telemetry, no background uploads.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"
	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/updatechannel"
	"github.com/rocareer/rolling-memory/internal/updatesource"
)

const (
	// DefaultSnapshotInterval is the default cadence of the background
	// published generation. Daily harness work rarely needs immediate
	// visibility: the current turn is usually still in the model's context, so
	// memory is for older material.
	DefaultSnapshotInterval = 60 * time.Second
	// SnapshotIntervalFloor keeps a misconfigured interval from turning the
	// builder into a continuous process; the ceiling bounds staleness.
	SnapshotIntervalFloor   = 10 * time.Second
	SnapshotIntervalCeiling = time.Hour
)

const (
	// DefaultScope is the default personal/workspace memory scope.
	DefaultScope = "default"
	// LegacyDefaultScope is the default scope from before the memory-engine era kept for
	// zero-migration compatibility with existing SQLite vaults and configs.
	LegacyDefaultScope = "mem:self"
)

// WorkspaceScopeName derives a per-workspace scope name: the
// ROLLING_MEMORY_LOCUS override wins, otherwise the working directory basename
// (the same rule the persona locus uses, so scope and locus never disagree).
// Returns "" when nothing usable is derivable (callers keep their scope).
func WorkspaceScopeName(override string, wd string) string {
	name := strings.TrimSpace(override)
	if name == "" {
		name = strings.TrimSpace(filepath.Base(strings.TrimSpace(wd)))
	}
	name = strings.ReplaceAll(name, ":", "-")
	if name == "" || name == "." || name == string(filepath.Separator) {
		return ""
	}
	if !scopeRE.MatchString(name) {
		return ""
	}
	return name
}

// DefaultConfigText is the literal default configuration file.
const DefaultConfigText = `# Rolling-Memory configuration (TOML).
# Secrets are read from environment variables or 0600 files, never from here.

[core]
db = "~/.local/share/rolling-memory/memory.db"
scope = "default"
reader = "deliverable"
log = "~/.local/state/rolling-memory/rolling-memory.log"

[mode]
# local = no non-loopback network request at all (the only supported mode)
name = "local"
allow_loopback_llm = true

[update]
# stable = published release tags only (default for published installs)
# beta   = release tags including prereleases
# dev    = latest main commit (source rebuild; no automatic binary replacement)
channel = "stable"
# github = official GitHub Releases (default)
# gitee  = Gitee mirror, for networks that reach Gitee faster than GitHub
source = "github"
# off    = never check in the background
# notify = check in the background and notify (default)
# apply  = download, verify and install updates automatically
auto = "notify"

[daemon]
# Background engine daemon memory cap (Go soft memory limit).
# auto = 1/8 of physical RAM, clamped to 1GiB-4GiB.
# off  = no explicit cap (a GOMEMLIMIT environment value still applies).
# Explicit sizes accept B / KiB / MiB / GiB / TiB, e.g. "512MiB" or "2GiB".
memory_limit = "auto"
# Fixed cadence of the background published generation. Daily harness work
# usually does not need immediate visibility: the current turn is already in the
# model's context, so a short publish delay is a deliberate trade for stable
# recall. Accepts Go durations ("60s", "5m") or plain seconds ("60").
snapshot_interval = "60s"

[recall]
chars = 16000
budget_ms = 500

[hooks]
ledger = true

[eval]
# Bundled self-evaluation (rmem eval): tiers 100 / 200 / 1540 ship under evaldata/.
# answerer: none = retrieval metrics only (no model call) | llm = frozen answer protocol
# judge:    off  | llm = frozen judge protocol (closed set CORRECT / WRONG)
# The two roles share one OpenAI-compatible /chat/completions endpoint; their prompts are
# cryptographically pinned by sha1 in internal/eval/frozen.go and are not configurable.
answerer = "none"
judge = "off"
base_url = ""
model = ""
api_key_env = "ROLLING_MEMORY_EVAL_API_KEY"
api_key_file = ""
timeout_sec = 60
# max_tokens 0 = no cap (allows reasoning models to finish thinking before answering).
# Set a positive cap only for non-reasoning endpoints.
max_tokens = 0
answer_temperature = 0.2
judge_temperature = 0.0
# Thinking switch and effort: DeepSeek wire form (thinking.type / reasoning_effort);
# defaults are on + "low".
thinking = true
reasoning_effort = "low"
workers = 8
`

var (
	scopeRE     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:._\-]{0,127}$`)
	validModes  = []string{"local"}
	validReader = []string{"owner", "deliverable"}
)

// ErrConfig marks configuration errors.
var ErrConfig = errors.New("config error")

// ErrEgressDenied marks a network request blocked by the active mode policy.
var ErrEgressDenied = errors.New("egress denied")

type configError struct{ msg string }

func (e *configError) Error() string { return e.msg }
func (e *configError) Unwrap() error { return ErrConfig }

type egressError struct{ msg string }

func (e *egressError) Error() string { return e.msg }
func (e *egressError) Unwrap() error { return ErrEgressDenied }

func newConfigError(format string, args ...any) error {
	return &configError{msg: fmt.Sprintf(format, args...)}
}

func newEgressError(format string, args ...any) error {
	return &egressError{msg: fmt.Sprintf(format, args...)}
}

// EvalConfig configures the bundled self-evaluation (`rmem eval`).
//
// The answerer and judge share one OpenAI-compatible endpoint; their prompts are the
// frozen benchmark texts (pinned in internal/eval/frozen.go) and are deliberately not
// configurable — a self-test that let callers rewrite the grader would not be auditable.
type EvalConfig struct {
	Answerer        string
	Judge           string
	BaseURL         string
	Model           string
	APIKeyEnv       string
	APIKeyFile      string
	TimeoutSec      float64
	MaxTokens       int
	AnswerTemp      float64
	JudgeTemp       float64
	Thinking        bool
	ReasoningEffort string
	Workers         int
}

// RecallConfig holds read-chain caps and leg toggles.
type RecallConfig struct {
	DeliveryWindow string
	Chars          int
	BudgetMS       int
}

// NormalizeDeliveryWindow normalizes an adaptive delivery window preset ("auto", "0-16",
// "0-32", "0-64") into its canonical label and item ceiling. Empty or "auto" resolves to
// fallback (or "0-16" when fallback is empty).
func NormalizeDeliveryWindow(raw, fallback string) (string, int, bool) {
	v := strings.ToLower(strings.TrimSpace(raw))
	if v == "" || v == "auto" {
		v = strings.ToLower(strings.TrimSpace(fallback))
		if v == "" || v == "auto" {
			v = "0-16"
		}
	}
	switch v {
	case "0-16", "16":
		return "0-16", 16, true
	case "0-32", "32":
		return "0-32", 32, true
	case "0-64", "64":
		return "0-64", 64, true
	default:
		return "", 0, false
	}
}

// HooksConfig holds hook budgets.
type HooksConfig struct {
	Ledger bool
}

// StorageConfig is reserved for the USB design; the default is local.
type StorageConfig struct {
	Profile string
}

// UpdateConfig holds the release channel, source and background policy
// preferences ([update] section).
type UpdateConfig struct {
	Channel string
	Source  string
	Auto    string
}

// DaemonConfig holds background engine daemon settings ([daemon] section).
type DaemonConfig struct {
	// MemoryLimit is the canonical soft memory limit: "auto", "off" or a
	// size such as "2GiB". It is applied when the daemon starts.
	MemoryLimit string
	// SnapshotInterval is the fixed cadence of the background published
	// generation. Zero falls back to DefaultSnapshotInterval for configs built
	// outside Load.
	SnapshotInterval time.Duration
}

// EffectiveSnapshotInterval returns the configured cadence or the default.
func (d DaemonConfig) EffectiveSnapshotInterval() time.Duration {
	if d.SnapshotInterval <= 0 {
		return DefaultSnapshotInterval
	}
	return d.SnapshotInterval
}

// ParseSnapshotInterval accepts a Go duration ("60s", "5m") or a plain integer
// number of seconds, and enforces the operational bounds.
func ParseSnapshotInterval(raw string) (time.Duration, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultSnapshotInterval, true
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		seconds, aerr := strconv.Atoi(raw)
		if aerr != nil {
			return 0, false
		}
		d = time.Duration(seconds) * time.Second
	}
	if d < SnapshotIntervalFloor || d > SnapshotIntervalCeiling {
		return 0, false
	}
	return d, true
}

// Config is the resolved runtime configuration.
type Config struct {
	Path             string
	DB               string
	Scope            string
	Reader           string
	Mode             string
	AllowLoopbackLLM bool
	Log              string
	Recall           RecallConfig
	Hooks            HooksConfig
	Storage          StorageConfig
	Daemon           DaemonConfig
	Eval             EvalConfig
	Update           UpdateConfig
}

// LoadOptions overrides configuration values. Pointer fields represent explicit
// arguments: nil and "" both fall through to env/file/default.
type LoadOptions struct {
	Path   *string
	DB     *string
	Mode   *string
	Scope  *string
	Reader *string
	Env    map[string]string
}

// Load resolves explicit options, environment, TOML file and defaults in that
// order, then validates the result.
func Load(opts LoadOptions) (*Config, error) {
	env := func(name string) (string, bool) {
		if opts.Env != nil {
			value, ok := opts.Env[name]
			return value, ok
		}
		return os.LookupEnv(name)
	}

	configEnv, _ := env("ROLLING_MEMORY_CONFIG")
	rawPath := firstNonEmpty(ids.DerefString(opts.Path), configEnv, DefaultConfigPath())
	configPath := ExpandUserPath(rawPath)
	data := map[string]any{}
	if info, err := os.Stat(configPath); err == nil {
		if info.IsDir() {
			return nil, newConfigError("cannot read config %s: is a directory", configPath)
		}
		raw, err := os.ReadFile(configPath)
		if err != nil {
			return nil, newConfigError("cannot read config %s: %v", configPath, err)
		}
		if err := toml.Unmarshal(raw, &data); err != nil {
			return nil, newConfigError("cannot read config %s: %v", configPath, tomlDecodeMessage(err))
		}
	}

	core := table(data, "core")
	modeTable := table(data, "mode")
	recallTable := table(data, "recall")
	hooksTable := table(data, "hooks")
	daemonTable := table(data, "daemon")
	evalTable := table(data, "eval")
	updateTable := table(data, "update")
	storageTable := table(data, "storage")

	dbValue := firstTruthy(ids.OptionalNonEmptyString(opts.DB), envValue(env, "ROLLING_MEMORY_DB"), core["db"], DefaultDBPath())
	logValue := firstTruthy(envValue(env, "ROLLING_MEMORY_LOG"), core["log"], filepath.Join(DefaultStateDir(), "rolling-memory.log"))
	modeValue := firstTruthy(ids.OptionalNonEmptyString(opts.Mode), envValue(env, "ROLLING_MEMORY_MODE"), modeTable["name"], "local")
	scopeValue := firstTruthy(ids.OptionalNonEmptyString(opts.Scope), envValue(env, "ROLLING_MEMORY_SCOPE"), core["scope"], DefaultScope)
	readerValue := firstTruthy(ids.OptionalNonEmptyString(opts.Reader), envValue(env, "ROLLING_MEMORY_READER"), core["reader"], "deliverable")

	recallWindowRaw := pyString(envOr(env, "ROLLING_MEMORY_DELIVERY_WINDOW", getDefault(recallTable, "delivery_window", "0-16")))
	recallWindowNorm, _, recallWindowOK := NormalizeDeliveryWindow(recallWindowRaw, "0-16")
	if !recallWindowOK {
		recallWindowNorm = strings.TrimSpace(recallWindowRaw)
	}
	recallConfig := RecallConfig{
		DeliveryWindow: recallWindowNorm,
		Chars:          asInt(recallTable["chars"], 16000),
		BudgetMS:       asInt(recallTable["budget_ms"], 500),
	}
	hooksConfig := HooksConfig{
		Ledger: asBool(hooksTable["ledger"], true),
	}
	updateChannel, updateChannelErr := updatechannel.Normalize(pyString(firstTruthy(
		envValue(env, "ROLLING_MEMORY_UPDATE_CHANNEL"), updateTable["channel"], updatechannel.Default)))
	if updateChannelErr != nil {
		return nil, newConfigError("%v", updateChannelErr)
	}
	updateSource, updateSourceErr := updatesource.Normalize(pyString(firstTruthy(
		envValue(env, "ROLLING_MEMORY_UPDATE_SOURCE"), updateTable["source"], updatesource.Default)))
	if updateSourceErr != nil {
		return nil, newConfigError("%v", updateSourceErr)
	}
	updateAuto, updateAutoErr := updatechannel.NormalizeAuto(pyString(firstTruthy(
		envValue(env, "ROLLING_MEMORY_UPDATE_AUTO"), updateTable["auto"], updatechannel.AutoDefault)))
	if updateAutoErr != nil {
		return nil, newConfigError("%v", updateAutoErr)
	}
	evalConfig := EvalConfig{
		Answerer:   pyString(envOr(env, "ROLLING_MEMORY_EVAL_ANSWERER", getDefault(evalTable, "answerer", "none"))),
		Judge:      pyString(envOr(env, "ROLLING_MEMORY_EVAL_JUDGE", getDefault(evalTable, "judge", "off"))),
		BaseURL:    pyString(envOr(env, "ROLLING_MEMORY_EVAL_BASE_URL", getDefault(evalTable, "base_url", ""))),
		Model:      pyString(envOr(env, "ROLLING_MEMORY_EVAL_MODEL", getDefault(evalTable, "model", ""))),
		APIKeyEnv:  pyString(envOr(env, "ROLLING_MEMORY_EVAL_API_KEY_ENV", getDefault(evalTable, "api_key_env", "ROLLING_MEMORY_EVAL_API_KEY"))),
		APIKeyFile: pyString(envOr(env, "ROLLING_MEMORY_EVAL_API_KEY_FILE", getDefault(evalTable, "api_key_file", ""))),
		TimeoutSec: asFloat(evalTable["timeout_sec"], 60.0),
		// 0 = 不设帽（与样例配置 `# max_tokens 0 = no cap (allows reasoning models to finish
		// thinking before answering)` 一致）。缺省回退成正数帽会在「思考开」的默认组合下
		// 让推理 token 吃满预算、正文交白卷（本地臂 30/1013 空正文的成因）。
		MaxTokens:  asInt(evalTable["max_tokens"], 0),
		AnswerTemp: asFloat(evalTable["answer_temperature"], 0.2),
		JudgeTemp:  asFloat(evalTable["judge_temperature"], 0.0),
		Thinking:   asBool(evalTable["thinking"], true),
		ReasoningEffort: pyString(envOr(env, "ROLLING_MEMORY_EVAL_REASONING_EFFORT",
			getDefault(evalTable, "reasoning_effort", "low"))),
		Workers: asInt(evalTable["workers"], 8),
	}
	storageProfile := "local"
	portableRoot := PortableRoot()
	if portableRoot != "" {
		storageProfile = "usb"
	}
	if value, ok := storageTable["profile"]; ok && truthy(value) {
		storageProfile = pyString(value)
	}

	daemonMemoryLimitRaw := pyString(envOr(env, "ROLLING_MEMORY_DAEMON_MEMORY_LIMIT",
		getDefault(daemonTable, "memory_limit", MemoryLimitAuto)))
	daemonMemoryLimit, _, daemonMemoryLimitOK := ParseMemoryLimit(daemonMemoryLimitRaw)
	if !daemonMemoryLimitOK {
		// Keep the raw value so Validate reports it precisely instead of
		// silently falling back to auto.
		daemonMemoryLimit = strings.TrimSpace(daemonMemoryLimitRaw)
	}
	snapshotIntervalRaw := pyString(envOr(env, "ROLLING_MEMORY_SNAPSHOT_INTERVAL",
		getDefault(daemonTable, "snapshot_interval", DefaultSnapshotInterval.String())))
	snapshotInterval, snapshotIntervalOK := ParseSnapshotInterval(snapshotIntervalRaw)
	if !snapshotIntervalOK {
		return nil, newConfigError("daemon.snapshot_interval must be a duration between %s and %s (e.g. '60s', '5m'), got %s",
			SnapshotIntervalFloor, SnapshotIntervalCeiling, pyRepr(snapshotIntervalRaw))
	}

	resolvedDB := resolveConfigPath(pyString(dbValue), portableRoot, DefaultDBPath(), "~/.local/share/rolling-memory/memory.db")
	resolvedLog := resolveConfigPath(pyString(logValue), portableRoot, filepath.Join(DefaultStateDir(), "rolling-memory.log"), "~/.local/state/rolling-memory/rolling-memory.log")

	rawMode := strings.ToLower(strings.TrimSpace(pyString(modeValue)))
	allowLoopback := asBool(modeTable["allow_loopback_llm"], true)

	result := &Config{
		Path:             configPath,
		DB:               resolvedDB,
		Scope:            pyString(scopeValue),
		Reader:           pyString(readerValue),
		Mode:             rawMode,
		AllowLoopbackLLM: allowLoopback,
		Log:              resolvedLog,
		Recall:           recallConfig,
		Hooks:            hooksConfig,
		Storage:          StorageConfig{Profile: storageProfile},
		Daemon:           DaemonConfig{MemoryLimit: daemonMemoryLimit, SnapshotInterval: snapshotInterval},
		Eval:             evalConfig,
		Update:           UpdateConfig{Channel: updateChannel, Source: updateSource, Auto: updateAuto},
	}
	if err := result.Validate(); err != nil {
		return nil, err
	}
	return result, nil
}

// Validate enforces the closed sets and positive caps.
func (c *Config) Validate() error {
	if !slices.Contains(validModes, c.Mode) {
		return newConfigError("mode must be one of ('local'), got %s", pyRepr(c.Mode))
	}
	if !slices.Contains(validReader, c.Reader) {
		return newConfigError("reader must be one of ('owner', 'deliverable'), got %s", pyRepr(c.Reader))
	}
	if !scopeRE.MatchString(c.Scope) {
		return newConfigError("invalid scope %s", pyRepr(c.Scope))
	}
	if _, err := updatesource.Normalize(c.Update.Source); err != nil {
		return newConfigError("%v", err)
	}
	if _, err := updatechannel.NormalizeAuto(c.Update.Auto); err != nil {
		return newConfigError("%v", err)
	}
	if c.Recall.BudgetMS <= 0 {
		return newConfigError("recall.budget_ms must be positive")
	}
	if _, _, ok := NormalizeDeliveryWindow(c.Recall.DeliveryWindow, "0-16"); !ok {
		return newConfigError("recall.delivery_window must be one of ('auto', '0-16', '0-32', '0-64'), got %s", pyRepr(c.Recall.DeliveryWindow))
	}
	if _, _, ok := ParseMemoryLimit(c.Daemon.MemoryLimit); !ok {
		return newConfigError("daemon.memory_limit must be 'auto', 'off' or a size between %s and %s (e.g. '512MiB', '2GiB'), got %s",
			formatMemorySize(ExplicitMemoryLimitFloor), formatMemorySize(ExplicitMemoryLimitCeiling), pyRepr(c.Daemon.MemoryLimit))
	}
	if c.Daemon.SnapshotInterval != 0 &&
		(c.Daemon.SnapshotInterval < SnapshotIntervalFloor || c.Daemon.SnapshotInterval > SnapshotIntervalCeiling) {
		return newConfigError("daemon.snapshot_interval must be between %s and %s, got %s",
			SnapshotIntervalFloor, SnapshotIntervalCeiling, c.Daemon.SnapshotInterval)
	}
	for _, value := range []int{
		c.Recall.Chars,
		c.Eval.Workers,
	} {
		if value <= 0 {
			return newConfigError("caps and batch sizes must be positive")
		}
	}
	if c.Eval.MaxTokens < 0 {
		return newConfigError("eval.max_tokens must be >= 0 (0 = no cap)")
	}
	if c.Eval.Answerer != "none" && c.Eval.Answerer != "llm" {
		return newConfigError("eval.answerer must be 'none' or 'llm'")
	}
	if c.Eval.Judge != "off" && c.Eval.Judge != "llm" {
		return newConfigError("eval.judge must be 'off' or 'llm'")
	}
	if c.Eval.AnswerTemp < 0 || c.Eval.AnswerTemp > 2 || c.Eval.JudgeTemp < 0 || c.Eval.JudgeTemp > 2 {
		return newConfigError("eval temperatures must be between 0 and 2")
	}
	if c.Eval.TimeoutSec <= 0 {
		return newConfigError("eval.timeout_sec must be positive")
	}
	return nil
}

// Ensure creates missing database and log parent directories with 0700.
func (c *Config) Ensure() error {
	for _, directory := range []string{filepath.Dir(c.DB), filepath.Dir(c.Log)} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// CheckEgress enforces the local-only network boundary: only loopback
// (localhost / 127.0.0.1 / ::1) is reachable through this gate. The one
// explicitly configured non-loopback exception is the evaluation model
// endpoint — the frozen benchmark answerer/judge leg — and it passes only when
// the checked host equals the user's configured `[eval] base_url` host and an
// evaluation API key is present. Every other non-loopback target is refused.
func (c *Config) CheckEgress(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return newEgressError("unsupported scheme for %s", pyRepr(rawURL))
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return newEgressError("unsupported scheme for %s", pyRepr(rawURL))
	}
	host, ok := ids.URLHost(rawURL)
	if c.AllowLoopbackLLM && ok && ids.IsLoopbackHost(host) {
		return nil
	}
	if ok && host != "" {
		if eHost, eOK := ids.URLHost(c.Eval.BaseURL); eOK && eHost == host {
			if key, err := c.EvalKey(); err == nil && strings.TrimSpace(key) != "" {
				return nil
			}
		}
	}
	display := host
	if display == "" {
		display = "unknown host"
	}
	return newEgressError(
		"local mode blocks non-loopback egress to %s; only the configured evaluation model endpoint may leave the machine",
		display,
	)
}

// CheckEvalEgress enforces the evaluation-model boundary. `rm eval` runs with a
// user-configured OpenAI-compatible endpoint (config `[eval] base_url` or the
// per-run `--eval-base-url` override); that endpoint is an explicit user choice
// for the benchmark answerer/judge leg, so it may leave the machine to any
// http(s) host. Only the URL shape is validated here — the local-mode loopback
// rule of CheckEgress does not apply to explicit evaluation LLM calls.
func (c *Config) CheckEvalEgress(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return newEgressError("unsupported scheme for %s", pyRepr(rawURL))
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return newEgressError("unsupported scheme for %s", pyRepr(rawURL))
	}
	host, ok := ids.URLHost(rawURL)
	if !ok || strings.TrimSpace(host) == "" {
		return newEgressError("missing host in %s", pyRepr(rawURL))
	}
	return nil
}

// tomlDecodeMessage maps the TOML parser's wording onto TOML parser's for the
// shapes the frozen config scenario pins: the standard oracle raises ConfigError
// with TOML parser's text verbatim and callers surface it byte for byte.
func tomlDecodeMessage(err error) string {
	if strings.Contains(err.Error(), "array is incomplete") {
		return "Invalid value (at end of document)"
	}
	return err.Error()
}

// ResolveSecret returns an env var value or the first line of a 0600 file.
func (c *Config) ResolveSecret(envName, filePath string) (string, error) {
	if envName != "" && os.Getenv(envName) != "" {
		return strings.TrimSpace(os.Getenv(envName)), nil
	}
	if filePath == "" {
		return "", nil
	}
	path := ExpandUserPath(filePath)
	info, err := os.Stat(path)
	if err != nil {
		return "", newConfigError("cannot read secret file %s: %v", path, err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return "", newConfigError("secret file %s must not be group/other accessible (chmod 600)", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", newConfigError("cannot read secret file %s: %v", path, err)
	}
	lines := pySplitLines(string(raw))
	if len(lines) == 0 {
		return "", newConfigError("cannot read secret file %s: list index out of range", path)
	}
	return strings.TrimSpace(lines[0]), nil
}

// EvalKey resolves the configured evaluation-model API key (frozen answerer / judge).
func (c *Config) EvalKey() (string, error) {
	return c.ResolveSecret(c.Eval.APIKeyEnv, c.Eval.APIKeyFile)
}

// SecretStatus reports key presence without exposing values.
func (c *Config) SecretStatus() map[string]map[string]any {
	result := map[string]map[string]any{}
	entries := []struct {
		name    string
		envName string
		file    string
	}{
		{"eval", c.Eval.APIKeyEnv, c.Eval.APIKeyFile},
	}
	for _, entry := range entries {
		key, err := c.ResolveSecret(entry.envName, entry.file)
		if err != nil {
			result[entry.name] = map[string]any{
				"configured": false,
				"source":     "error",
				"error":      err.Error(),
			}
			continue
		}
		source := "none"
		if key != "" {
			if entry.envName != "" && os.Getenv(entry.envName) != "" {
				source = "env"
			} else {
				source = "file"
			}
		}
		result[entry.name] = map[string]any{
			"configured": key != "",
			"source":     source,
		}
	}
	return result
}

// PublicDict returns the redacted configuration view.
func (c *Config) PublicDict() map[string]any {
	return map[string]any{
		"path":   c.Path,
		"db":     c.DB,
		"scope":  c.Scope,
		"reader": c.Reader,
		"mode":   c.Mode,
		"egress": "loopback-only",
		"log":    c.Log,
		"recall": map[string]any{
			"chars":     c.Recall.Chars,
			"budget_ms": c.Recall.BudgetMS,
		},
		"daemon": map[string]any{
			"memory_limit":      c.Daemon.MemoryLimit,
			"snapshot_interval": c.Daemon.EffectiveSnapshotInterval().String(),
		},
		"update": map[string]any{
			"channel": c.Update.Channel,
			"source":  c.Update.Source,
			"auto":    c.Update.Auto,
		},
	}
}

// PortableRoot returns the portable payload root directory when portable mode
// is active (either via ROLLING_MEMORY_PORTABLE or a .portable marker file next
// to the executable or its parent payload root), or "" in standard host mode.
func PortableRoot() string {
	if raw := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_PORTABLE")); raw != "" && raw != "0" && strings.ToLower(raw) != "false" {
		if raw == "1" || strings.ToLower(raw) == "true" {
			if exe, err := os.Executable(); err == nil {
				if root := PortableRootForExecutable(exe); root != "" {
					return root
				}
				return filepath.Dir(exe)
			}
		}
		return ExpandUserPath(raw)
	}
	if exe, err := os.Executable(); err == nil {
		return PortableRootForExecutable(exe)
	}
	return ""
}

// PortableRootForExecutable resolves the portable payload root for a given
// executable path, checking both the path as-is and its symlink-resolved target.
// It supports both flat layouts (<root>/rolling-memory + <root>/.portable) and
// standard bundle layouts (<root>/bin/rolling-memory + <root>/.portable).
func PortableRootForExecutable(exe string) string {
	if exe == "" {
		return ""
	}
	candidates := []string{exe}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil && resolved != "" && resolved != exe {
		candidates = append(candidates, resolved)
	}
	for _, candidate := range candidates {
		dir := filepath.Dir(candidate)
		parent := filepath.Dir(dir)
		if strings.EqualFold(filepath.Base(dir), "bin") && parent != dir && hasPortableMarker(parent) {
			return parent
		}
		if hasPortableMarker(dir) {
			return dir
		}
	}
	return ""
}

func hasPortableMarker(dir string) bool {
	if dir == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(dir, ".portable"))
	return err == nil && !info.IsDir()
}

func resolveConfigPath(raw, portableRoot, portableDefault, hostDefaultLiteral string) string {
	trimmed := strings.TrimSpace(raw)
	if portableRoot != "" {
		if trimmed == "" || trimmed == hostDefaultLiteral {
			return portableDefault
		}
		if !strings.HasPrefix(trimmed, "~") && !filepath.IsAbs(trimmed) {
			return filepath.Join(portableRoot, trimmed)
		}
	}
	return ExpandUserPath(raw)
}

// DefaultConfigPath returns $XDG_CONFIG_HOME/rolling-memory/config.toml (or
// <portable>/config/config.toml in portable mode).
func DefaultConfigPath() string {
	if root := PortableRoot(); root != "" {
		return filepath.Join(root, "config", "config.toml")
	}
	base := xdgHome("XDG_CONFIG_HOME", filepath.Join(homeDir(), ".config"))
	return filepath.Join(base, "rolling-memory", "config.toml")
}

// DefaultDBPath returns $XDG_DATA_HOME/rolling-memory/memory.db (or
// <portable>/data/memory.db in portable mode).
func DefaultDBPath() string {
	if root := PortableRoot(); root != "" {
		return filepath.Join(root, "data", "memory.db")
	}
	base := xdgHome("XDG_DATA_HOME", filepath.Join(homeDir(), ".local", "share"))
	return filepath.Join(base, "rolling-memory", "memory.db")
}

// DefaultStateDir returns $XDG_STATE_HOME/rolling-memory (or <portable>/state
// in portable mode).
func DefaultStateDir() string {
	if root := PortableRoot(); root != "" {
		return filepath.Join(root, "state")
	}
	base := xdgHome("XDG_STATE_HOME", filepath.Join(homeDir(), ".local", "state"))
	return filepath.Join(base, "rolling-memory")
}

func xdgHome(envName, fallback string) string {
	if value := os.Getenv(envName); value != "" {
		return ExpandUserPath(value)
	}
	return fallback
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// WriteDefault writes the default config if path does not exist (0600).
func WriteDefault(path string) (string, error) {
	path = ExpandUserPath(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		text := DefaultConfigText
		if isPortableConfigTarget(path) {
			text = PortableDefaultConfigText()
		} else {
			text = applyXDGPathDefaults(text)
		}
		if err := writeFileAtomic(path, []byte(text), 0o600); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	return path, nil
}

// applyXDGPathDefaults rewrites the template's `~` database and log paths to
// the XDG-resolved defaults when XDG_DATA_HOME / XDG_STATE_HOME are set. Without
// this, creating the config file would move the vault compared with the
// no-config defaults and change the daemon configuration identity.
func applyXDGPathDefaults(text string) string {
	if os.Getenv("XDG_DATA_HOME") != "" {
		text = strings.Replace(text,
			`db = "~/.local/share/rolling-memory/memory.db"`,
			`db = `+tomlBasicString(DefaultDBPath()), 1)
	}
	if os.Getenv("XDG_STATE_HOME") != "" {
		text = strings.Replace(text,
			`log = "~/.local/state/rolling-memory/rolling-memory.log"`,
			`log = `+tomlBasicString(filepath.Join(DefaultStateDir(), "rolling-memory.log")), 1)
	}
	return text
}

// tomlBasicString renders value as a TOML basic (double-quoted) string. The
// paths interpolated into the template must go through it: Windows paths
// contain backslashes, and a backslash inside a basic string starts an escape
// sequence (`\U...` is a unicode escape), so an unescaped path produced a
// config file that could not be read back.
func tomlBasicString(value string) string {
	encoded, err := ids.PyJSONDump(value, false, false, true)
	if err != nil {
		return `""`
	}
	return encoded
}

// PortableDefaultConfigText returns DefaultConfigText adapted for a portable USB
// payload (relative core.db / core.log paths and storage.profile = "usb").
func PortableDefaultConfigText() string {
	text := strings.Replace(
		DefaultConfigText,
		`db = "~/.local/share/rolling-memory/memory.db"`,
		`db = "data/memory.db"`,
		1,
	)
	text = strings.Replace(
		text,
		`log = "~/.local/state/rolling-memory/rolling-memory.log"`,
		`log = "state/rolling-memory.log"`,
		1,
	)
	if !strings.Contains(text, "[storage]") {
		text = strings.TrimRight(text, "\n") + "\n\n[storage]\nprofile = \"usb\"\n"
	}
	return text
}

func isPortableConfigTarget(path string) bool {
	if PortableRoot() != "" {
		return true
	}
	cfgDir := filepath.Dir(path)
	parent := filepath.Dir(cfgDir)
	return hasPortableMarker(cfgDir) || (parent != cfgDir && hasPortableMarker(parent))
}

// SetConfigValue sets section.key with a minimal line edit that preserves
// comments and layout.
func SetConfigValue(path, dotted, rawValue string) (string, error) {
	path = ExpandUserPath(path)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if _, err := WriteDefault(path); err != nil {
			return "", err
		}
	}
	position := strings.LastIndex(dotted, ".")
	if position <= 0 || position == len(dotted)-1 {
		return "", newConfigError("config key must look like 'section.key'")
	}
	section := dotted[:position]
	key := dotted[position+1:]
	// Typed keys validate and canonicalize before the line edit so an invalid
	// value never reaches the file.
	if section == "daemon" && key == "memory_limit" {
		canonical, _, ok := ParseMemoryLimit(rawValue)
		if !ok {
			return "", newConfigError("daemon.memory_limit must be 'auto', 'off' or a size between %s and %s (e.g. '512MiB', '2GiB'), got %s",
				formatMemorySize(ExplicitMemoryLimitFloor), formatMemorySize(ExplicitMemoryLimitCeiling), pyRepr(trimSpace(rawValue)))
		}
		rawValue = canonical
	}
	if section == "update" && key == "source" {
		canonical, err := updatesource.Normalize(rawValue)
		if err != nil {
			return "", newConfigError("%v", err)
		}
		rawValue = canonical
	}
	if section == "update" && key == "channel" {
		canonical, err := updatechannel.Normalize(rawValue)
		if err != nil {
			return "", newConfigError("%v", err)
		}
		rawValue = canonical
	}
	if section == "update" && key == "auto" {
		canonical, err := updatechannel.NormalizeAuto(rawValue)
		if err != nil {
			return "", newConfigError("%v", err)
		}
		rawValue = canonical
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	lines := pySplitLines(string(raw))
	literal := tomlLiteral(rawValue)

	sectionStart := -1
	sectionEnd := -1
	current := ""
	for index, line := range lines {
		stripped := trimSpace(line)
		if strings.HasPrefix(stripped, "[") && strings.HasSuffix(stripped, "]") {
			current = trimSpace(stripped[1 : len(stripped)-1])
			if current == section {
				sectionStart = index
			} else if sectionStart >= 0 && sectionEnd < 0 {
				sectionEnd = index
			}
		}
	}
	if sectionStart >= 0 && sectionEnd < 0 {
		sectionEnd = len(lines)
	}

	if sectionStart >= 0 && sectionEnd >= 0 {
		replaced := false
		for index := sectionStart + 1; index < sectionEnd; index++ {
			indent, matched := matchKeyLine(lines[index], key)
			if matched {
				lines[index] = indent + key + " = " + literal
				replaced = true
				break
			}
		}
		if !replaced {
			insertAt := sectionEnd
			for insertAt > sectionStart+1 && trimSpace(lines[insertAt-1]) == "" {
				insertAt--
			}
			lines = append(lines, "")
			copy(lines[insertAt+1:], lines[insertAt:])
			lines[insertAt] = key + " = " + literal
		}
	} else {
		if len(lines) > 0 && trimSpace(lines[len(lines)-1]) != "" {
			lines = append(lines, "")
		}
		lines = append(lines, "["+section+"]", key+" = "+literal)
	}

	content := trimRightSpace(strings.Join(lines, "\n")) + "\n"
	if err := writeFileAtomic(path, []byte(content), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// --------------------------------------------------------------------- helpers

// writeFileAtomic replaces path through a same-directory temp file so a crash,
// kill, or full disk can never leave a truncated config behind.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	handle, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := handle.Write(data); err != nil {
		handle.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := handle.Sync(); err != nil {
		handle.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := handle.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	_ = os.Chmod(tmp, perm)
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func table(data map[string]any, key string) map[string]any {
	value, ok := data[key].(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return value
}

func getDefault(data map[string]any, key string, fallback any) any {
	if value, ok := data[key]; ok {
		return value
	}
	return fallback
}

func envValue(env func(string) (string, bool), name string) any {
	value, ok := env(name)
	if !ok || value == "" {
		return nil
	}
	return value
}

func envOr(env func(string) (string, bool), name string, fallback any) any {
	value, ok := env(name)
	if ok && value != "" {
		return value
	}
	return fallback
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func firstTruthy(values ...any) any {
	for _, value := range values {
		if truthy(value) {
			return value
		}
	}
	return nil
}

func truthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case float64:
		return typed != 0
	default:
		return true
	}
}

func pyString(value any) string {
	switch typed := value.(type) {
	case nil:
		return "None"
	case string:
		return typed
	case bool:
		if typed {
			return "True"
		}
		return "False"
	default:
		return fmt.Sprint(typed)
	}
}

func asInt(value any, fallback int) int {
	switch typed := value.(type) {
	case nil:
		return fallback
	case bool:
		if typed {
			return 1
		}
		return 0
	case int:
		return typed
	case int8:
		return int(typed)
	case int16:
		return int(typed)
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case uint:
		return int(typed)
	case uint64:
		return int(typed)
	case float32:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		trimmed := strings.TrimSpace(typed)
		parsed, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil {
			return fallback
		}
		return int(parsed)
	default:
		return fallback
	}
}

func asFloat(value any, fallback float64) float64 {
	switch typed := value.(type) {
	case nil:
		return fallback
	case bool:
		if typed {
			return 1
		}
		return 0
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case float32:
		return float64(typed)
	case float64:
		return typed
	case string:
		trimmed := strings.TrimSpace(typed)
		parsed, err := strconv.ParseFloat(trimmed, 64)
		if err != nil {
			return fallback
		}
		return parsed
	default:
		return fallback
	}
}

func asBool(value any, fallback bool) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
		return fallback
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case float64:
		return typed != 0
	default:
		return fallback
	}
}

// ExpandUserPath expands a leading "~" or "~/" to the current user's home
// directory; any other path passes through unchanged. A failed home probe also
// returns the literal path (fail-open), so callers surface what the user wrote
// instead of an empty string.
func ExpandUserPath(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home := homeDir()
	if home == "" {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}

func pyRepr(value string) string {
	quote := byte('\'')
	if strings.Contains(value, "'") && !strings.Contains(value, `"`) {
		quote = '"'
	}
	var out strings.Builder
	out.WriteByte(quote)
	for _, char := range value {
		switch char {
		case '\\':
			out.WriteString(`\\`)
		case rune(quote):
			out.WriteByte('\\')
			out.WriteRune(char)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			if char < 0x20 || char == 0x7F {
				out.WriteString(fmt.Sprintf(`\x%02x`, char))
				continue
			}
			out.WriteRune(char)
		}
	}
	out.WriteByte(quote)
	return out.String()
}

func matchKeyLine(line, key string) (string, bool) {
	indentLength := 0
	for indentLength < len(line) {
		char, size := utf8.DecodeRuneInString(line[indentLength:])
		if !isSpaceRune(char) {
			break
		}
		indentLength += size
	}
	indent := line[:indentLength]
	rest := line[indentLength:]
	if !strings.HasPrefix(rest, key) {
		return "", false
	}
	rest = rest[len(key):]
	for len(rest) > 0 {
		char, size := utf8.DecodeRuneInString(rest)
		if !isSpaceRune(char) {
			break
		}
		rest = rest[size:]
	}
	if strings.HasPrefix(rest, "=") {
		return indent, true
	}
	return "", false
}

func tomlLiteral(raw string) string {
	value := trimSpace(raw)
	lowered := strings.ToLower(value)
	if lowered == "true" || lowered == "false" {
		return lowered
	}
	if isTOMLNumber(value) {
		return value
	}
	return tomlBasicString(value)
}

func isTOMLNumber(value string) bool {
	if value == "" {
		return false
	}
	position := 0
	if value[position] == '-' {
		position++
	}
	digits := 0
	for position < len(value) && value[position] >= '0' && value[position] <= '9' {
		position++
		digits++
	}
	if digits == 0 {
		return false
	}
	if position == len(value) {
		return true
	}
	if value[position] != '.' {
		return false
	}
	position++
	fraction := 0
	for position < len(value) && value[position] >= '0' && value[position] <= '9' {
		position++
		fraction++
	}
	return fraction > 0 && position == len(value)
}

func isSpaceRune(char rune) bool {
	switch char {
	case '\t', '\n', '\v', '\f', '\r', ' ', '\u0085', '\u00A0',
		'\u1680', '\u2028', '\u2029', '\u202F', '\u205F', '\u3000':
		return true
	}
	return (0x1C <= char && char <= 0x1F) || (0x2000 <= char && char <= 0x200A)
}

func trimSpace(value string) string {
	return strings.TrimFunc(value, isSpaceRune)
}

func trimRightSpace(value string) string {
	return strings.TrimRightFunc(value, isSpaceRune)
}

// pySplitLines mirrors standard str.splitlines: no trailing empty element after
// a final line break, but a leading break yields an empty first line.
func pySplitLines(value string) []string {
	lines := make([]string, 0, 8)
	var current strings.Builder
	runes := []rune(value)
	for index := 0; index < len(runes); index++ {
		char := runes[index]
		boundary := false
		switch char {
		case '\n', '\v', '\f', '\r', '\u0085', '\u2028', '\u2029':
			boundary = true
		case '\x1C', '\x1D', '\x1E':
			boundary = true
		}
		if !boundary {
			current.WriteRune(char)
			continue
		}
		if char == '\r' && index+1 < len(runes) && runes[index+1] == '\n' {
			index++
		}
		lines = append(lines, current.String())
		current.Reset()
	}
	if current.Len() > 0 {
		lines = append(lines, current.String())
	}
	return lines
}

// AnchorGateEnabled reports whether the experimental query anchor gate is on.
// The gate is an A/B arm: unset keeps the frozen product and benchmark behaviour.
func AnchorGateEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("ROLLING_MEMORY_ANCHOR_GATE"))) {
	case "1", "on", "true", "yes":
		return true
	}
	return false
}
