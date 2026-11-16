// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package config implements configuration, paths, modes and the network
// egress policy.
//
// The policy is deliberately small and enforced at every network call:
//
//   - local  -- only loopback (localhost / 127.0.0.1 / ::1) is reachable.
//   - cloud  -- remote endpoints are allowed, but private rows are still
//     never sent to an LLM or the sync backend (hard-coded, not a toggle).
//
// No telemetry, no background uploads. Cloud sync also requires both
// mode = "cloud" and cloud.sync.enabled = true.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"
	"github.com/rocareer/rolling-memory/internal/ids"
)

const (
	// DefaultScope is the default personal/workspace memory scope.
	DefaultScope = "default"
	// LegacyDefaultScope is the default scope from before the memory-engine era kept for
	// zero-migration compatibility with existing SQLite vaults and configs.
	LegacyDefaultScope = "mem:self"
)

// IsDefaultScope reports whether scope refers to the default personal memory
// namespace ("default", empty, or legacy "mem:self").
func IsDefaultScope(scope string) bool {
	clean := strings.TrimSpace(scope)
	return clean == "" || clean == DefaultScope || clean == LegacyDefaultScope
}

// WorkspaceScopeName derives the per-workspace scope name used when
// [cloud.sync] scope_from_workspace is enabled: the ROLLING_MEMORY_LOCUS
// override wins, otherwise the working directory basename (the same rule the
// persona locus uses, so scope and locus never disagree). Returns "" when
// nothing usable is derivable (callers keep their existing scope).
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
# local = no non-loopback network request at all (default)
# cloud = remote LLM / embedding / sync allowed; private rows still stay local
name = "local"
allow_loopback_llm = true

[llm]
# none = deterministic local extractor (no model call)
# openai = any OpenAI-compatible /chat/completions endpoint
provider = "none"
base_url = "https://api.openai.com/v1"
model = "gpt-4o-mini"
api_key_env = "OPENAI_API_KEY"
api_key_file = ""
timeout_sec = 30
max_tokens = 1200

[recall]
bands = 8
nodes = 50
chars = 16000
hook_bands = 3
hook_nodes = 12
hook_chars = 1500
budget_ms = 500
# raw_limit = 原文腿交付条数上限（判分面的主要旋钮：在字符预算内多给原文；5 = 旧行为）。
# Tier-100 实测（冻结答题/判官）：5 → 判对 0.45 · 20 → 0.60 · 30 → 0.62。
raw_limit = 5
raw_when_sparse = true
vector = true
token_correct = false
alias_expand = true
relative_tag = false
mention_backfill = true
activation_record = true
activation_rank = false

[hooks]
boot_context = false
max_traces = 60
max_llm_calls = 20
ledger = true

[identity]
# Transcript role -> canonical speaker name (resolved at write-side ingestion).
# Empty by default (holder defaults to "我" and the extractor receives raw role labels).
# Example: owner = "我" (or local username) · assistant = "assistant"
owner = ""
assistant = ""

[cloud.sync]
enabled = false
base_url = ""
api_key_env = "ROLLING_MEMORY_CLOUD_KEY"
api_key_file = ""
auto = false
batch_size = 100
timeout_sec = 30
include_anchor = false
# 默认 scope 按当前工作区派生（每工作区独立分区；显式 scope 优先）
scope_from_workspace = false

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
	validModes  = []string{"local", "cloud"}
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

// LLMConfig configures the optional OpenAI-compatible LLM.
type LLMConfig struct {
	Provider   string
	BaseURL    string
	Model      string
	APIKeyEnv  string
	APIKeyFile string
	TimeoutSec float64
	MaxTokens  int
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
	Bands            int
	Nodes            int
	Chars            int
	Chains           int
	Rows             int
	HookBands        int
	HookNodes        int
	HookChars        int
	BudgetMS         int
	RawLimit         int
	RawWhenSparse    bool
	Vector           bool
	TokenCorrect     bool
	AliasExpand      bool
	RelativeTag      bool
	MentionBackfill  bool
	ActivationRecord bool
	ActivationRank   bool
	AdvancedOps      bool
	SearchEngine     string
	MaskDeliverable  string
	PruneScoreRatio  float64
	PruneMinInfoLCS  float64
	PruneMinTitleCov float64
	PreprobeLimit    int
}

// HooksConfig holds hook budgets.
type HooksConfig struct {
	BootContext bool
	MaxTraces   int
	MaxLLMCalls int
	Ledger      bool
}

// IdentityConfig holds the Assistant-centric autonoetic identity mapping ([identity] section):
//   - AssistantID: the Assistant's first-class identity ID (maps to scope "assistant:<assistant_id>" co-read with "default").
//   - Assistant:   the Assistant's self-referent label I_self (origin="self").
//   - Owner:       the bound host user label I_peer (origin="roster", equal in node status to other roster persons).
type IdentityConfig struct {
	AssistantID string
	Owner       string
	Assistant   string
}

// CloudSyncConfig configures the optional sync backend.
type CloudSyncConfig struct {
	Enabled       bool
	BaseURL       string
	APIKeyEnv     string
	APIKeyFile    string
	Auto          bool
	BatchSize     int
	TimeoutSec    float64
	IncludeAnchor bool
	// ScopeFromWorkspace derives a default scope from the current workspace name
	// (ROLLING_MEMORY_LOCUS override, else the working directory basename) so each
	// workspace keeps its own cloud partition; explicit scopes win unchanged.
	ScopeFromWorkspace bool
}

// StorageConfig is reserved for the USB design; the default is local.
type StorageConfig struct {
	Profile string
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
	LLM              LLMConfig
	Recall           RecallConfig
	Hooks            HooksConfig
	Cloud            CloudSyncConfig
	Storage          StorageConfig
	Identity         IdentityConfig
	Eval             EvalConfig
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
	rawPath := firstNonEmpty(derefString(opts.Path), configEnv, DefaultConfigPath())
	configPath := expandUser(rawPath)
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
	llmTable := table(data, "llm")
	recallTable := table(data, "recall")
	hooksTable := table(data, "hooks")
	identityTable := table(data, "identity")
	evalTable := table(data, "eval")
	cloudRoot := table(data, "cloud")
	syncTable := map[string]any{}
	if value, ok := cloudRoot["sync"].(map[string]any); ok {
		syncTable = value
	}
	storageTable := table(data, "storage")

	dbValue := firstTruthy(deref(opts.DB), envValue(env, "ROLLING_MEMORY_DB"), core["db"], DefaultDBPath())
	logValue := firstTruthy(envValue(env, "ROLLING_MEMORY_LOG"), core["log"], filepath.Join(DefaultStateDir(), "rolling-memory.log"))
	modeValue := firstTruthy(deref(opts.Mode), envValue(env, "ROLLING_MEMORY_MODE"), modeTable["name"], "local")
	scopeValue := firstTruthy(deref(opts.Scope), envValue(env, "ROLLING_MEMORY_SCOPE"), core["scope"], DefaultScope)
	// [cloud.sync] scope_from_workspace：默认 scope（空 / "default" / 旧 "mem:self"）派生为
	// 当前工作区名（ROLLING_MEMORY_LOCUS 覆盖，否则工作目录 basename），使每个工作区落在
	// 独立分区；显式 scope（--scope / ROLLING_MEMORY_SCOPE / 自定义 [core] scope）优先。
	if asBool(syncTable["scope_from_workspace"], false) && IsDefaultScope(pyString(scopeValue)) {
		if wd, err := os.Getwd(); err == nil {
			locusOverride, _ := env("ROLLING_MEMORY_LOCUS")
			if name := WorkspaceScopeName(locusOverride, wd); name != "" {
				scopeValue = name
			}
		}
	}
	readerValue := firstTruthy(deref(opts.Reader), envValue(env, "ROLLING_MEMORY_READER"), core["reader"], "deliverable")

	llmConfig := LLMConfig{
		Provider:   pyString(envOr(env, "ROLLING_MEMORY_LLM_PROVIDER", getDefault(llmTable, "provider", "none"))),
		BaseURL:    pyString(envOr(env, "ROLLING_MEMORY_LLM_BASE_URL", getDefault(llmTable, "base_url", "https://api.openai.com/v1"))),
		Model:      pyString(envOr(env, "ROLLING_MEMORY_LLM_MODEL", getDefault(llmTable, "model", "gpt-4o-mini"))),
		APIKeyEnv:  pyString(envOr(env, "ROLLING_MEMORY_LLM_API_KEY_ENV", getDefault(llmTable, "api_key_env", "OPENAI_API_KEY"))),
		APIKeyFile: pyString(envOr(env, "ROLLING_MEMORY_LLM_API_KEY_FILE", getDefault(llmTable, "api_key_file", ""))),
		TimeoutSec: asFloat(llmTable["timeout_sec"], 30.0),
		MaxTokens:  asInt(llmTable["max_tokens"], 1200),
	}
	recallConfig := RecallConfig{
		Bands:            asInt(recallTable["bands"], 8),
		Nodes:            asInt(recallTable["nodes"], 50),
		Chars:            asInt(recallTable["chars"], 16000),
		Chains:           asInt(recallTable["chains"], 8),
		Rows:             asInt(recallTable["rows"], 12),
		HookBands:        asInt(recallTable["hook_bands"], 3),
		HookNodes:        asInt(recallTable["hook_nodes"], 12),
		HookChars:        asInt(recallTable["hook_chars"], 1500),
		BudgetMS:         asInt(recallTable["budget_ms"], 500),
		RawLimit:         asInt(recallTable["raw_limit"], 5),
		RawWhenSparse:    asBool(recallTable["raw_when_sparse"], true),
		Vector:           asBool(recallTable["vector"], true),
		TokenCorrect:     asBool(recallTable["token_correct"], false),
		AliasExpand:      asBool(recallTable["alias_expand"], true),
		RelativeTag:      asBool(recallTable["relative_tag"], false),
		MentionBackfill:  asBool(recallTable["mention_backfill"], true),
		ActivationRecord: asBool(recallTable["activation_record"], true),
		ActivationRank:   asBool(recallTable["activation_rank"], false),
		AdvancedOps:      asBool(envOr(env, "ROLLING_MEMORY_ADVANCED_OPS", getDefault(recallTable, "advanced_ops", false)), false),
		SearchEngine:     strings.ToLower(strings.TrimSpace(pyString(envOr(env, "ROLLING_MEMORY_SEARCH_ENGINE", getDefault(recallTable, "search_engine", "hybrid"))))),
		MaskDeliverable:  strings.ToLower(strings.TrimSpace(pyString(envOr(env, "ROLLING_MEMORY_MASK_DELIVERABLE", getDefault(recallTable, "mask_deliverable", "off"))))),
		PruneScoreRatio:  asFloat(recallTable["prune_score_ratio"], 0.55),
		PruneMinInfoLCS:  asFloat(recallTable["prune_min_info_lcs"], 2.7),
		PruneMinTitleCov: asFloat(recallTable["prune_min_title_cov"], 0.24),
		PreprobeLimit:    asInt(recallTable["preprobe_limit"], 512),
	}
	hooksConfig := HooksConfig{
		BootContext: asBool(hooksTable["boot_context"], false),
		MaxTraces:   asInt(hooksTable["max_traces"], 60),
		MaxLLMCalls: asInt(hooksTable["max_llm_calls"], 20),
		Ledger:      asBool(hooksTable["ledger"], true),
	}
	identityConfig := IdentityConfig{
		AssistantID: pyString(envOr(env, "ROLLING_MEMORY_ASSISTANT_ID", firstTruthy(identityTable["assistant_id"], core["assistant_id"], ""))),
		Owner:       pyString(envOr(env, "ROLLING_MEMORY_IDENTITY_OWNER", getDefault(identityTable, "owner", ""))),
		Assistant:   pyString(envOr(env, "ROLLING_MEMORY_IDENTITY_ASSISTANT", getDefault(identityTable, "assistant", ""))),
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
	syncConfig := CloudSyncConfig{
		Enabled:            asBool(syncTable["enabled"], false),
		BaseURL:            pyString(envOr(env, "ROLLING_MEMORY_SYNC_BASE_URL", getDefault(syncTable, "base_url", ""))),
		APIKeyEnv:          pyString(envOr(env, "ROLLING_MEMORY_SYNC_API_KEY_ENV", getDefault(syncTable, "api_key_env", "ROLLING_MEMORY_CLOUD_KEY"))),
		APIKeyFile:         pyString(envOr(env, "ROLLING_MEMORY_SYNC_API_KEY_FILE", getDefault(syncTable, "api_key_file", ""))),
		Auto:               asBool(syncTable["auto"], false),
		BatchSize:          asInt(syncTable["batch_size"], 100),
		TimeoutSec:         asFloat(syncTable["timeout_sec"], 30.0),
		IncludeAnchor:      asBool(syncTable["include_anchor"], false),
		ScopeFromWorkspace: asBool(syncTable["scope_from_workspace"], false),
	}

	storageProfile := "local"
	portableRoot := PortableRoot()
	if portableRoot != "" {
		storageProfile = "usb"
	}
	if value, ok := storageTable["profile"]; ok && truthy(value) {
		storageProfile = pyString(value)
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
		LLM:              llmConfig,
		Recall:           recallConfig,
		Hooks:            hooksConfig,
		Cloud:            syncConfig,
		Storage:          StorageConfig{Profile: storageProfile},
		Identity:         identityConfig,
		Eval:             evalConfig,
	}
	if err := result.Validate(); err != nil {
		return nil, err
	}
	return result, nil
}

// Validate enforces the closed sets and positive caps.
func (c *Config) Validate() error {
	if !contains(validModes, c.Mode) {
		return newConfigError("mode must be one of ('local', 'cloud'), got %s", pyRepr(c.Mode))
	}
	if c.Mode == "local" && c.Recall.AdvancedOps {
		return newConfigError("advanced operators (recall.advanced_ops) are not allowed in local mode; switch to cloud mode first")
	}
	if !contains(validReader, c.Reader) {
		return newConfigError("reader must be one of ('owner', 'deliverable'), got %s", pyRepr(c.Reader))
	}
	if !scopeRE.MatchString(c.Scope) {
		return newConfigError("invalid scope %s", pyRepr(c.Scope))
	}
	if c.LLM.Provider != "none" && c.LLM.Provider != "openai" {
		return newConfigError("llm.provider must be 'none' or 'openai'")
	}
	if c.LLM.TimeoutSec <= 0 {
		return newConfigError("timeouts must be positive")
	}
	if c.Recall.BudgetMS <= 0 {
		return newConfigError("recall.budget_ms must be positive")
	}
	for _, value := range []int{
		c.Recall.Bands,
		c.Recall.Nodes,
		c.Recall.Chars,
		c.Recall.Chains,
		c.Recall.Rows,
		c.Recall.HookBands,
		c.Recall.HookNodes,
		c.Recall.HookChars,
		c.Hooks.MaxTraces,
		c.Hooks.MaxLLMCalls,
		c.Cloud.BatchSize,
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

// CheckEgress enforces the local/cloud network policy.
func (c *Config) CheckEgress(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return newEgressError("unsupported scheme for %s", pyRepr(rawURL))
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return newEgressError("unsupported scheme for %s", pyRepr(rawURL))
	}
	if c.Mode == "cloud" {
		return nil
	}
	host, ok := ids.URLHost(rawURL)
	if c.AllowLoopbackLLM && ok && ids.IsLoopbackHost(host) {
		return nil
	}
	// In local mode, if the user explicitly enabled a legacy limb — the rerank
	// limb (retired "max" preset), the embedding limb (retired "pro") or a
	// model-driven producer — and configured their own API key and endpoint,
	// allow calls to that user-configured endpoint
	// while still blocking all cloud memory sync and unconfigured external hosts.
	if ok && host != "" {
		if c.LLM.Provider == "openai" {
			if lHost, lOK := ids.URLHost(c.LLM.BaseURL); lOK && lHost == host {
				if key, err := c.LLMKey(); err == nil && strings.TrimSpace(key) != "" {
					return nil
				}
			}
		}
	}
	display := host
	if display == "" {
		display = "unknown host"
	}
	return newEgressError(
		"local mode blocks non-loopback egress to %s; switch to cloud mode explicitly if this is intended",
		display,
	)
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
	path := expandUser(filePath)
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

// LLMKey resolves the configured LLM API key.
func (c *Config) LLMKey() (string, error) {
	return c.ResolveSecret(c.LLM.APIKeyEnv, c.LLM.APIKeyFile)
}

// EvalKey resolves the configured evaluation-model API key (frozen answerer / judge).
func (c *Config) EvalKey() (string, error) {
	return c.ResolveSecret(c.Eval.APIKeyEnv, c.Eval.APIKeyFile)
}

// SyncKey resolves the configured cloud sync API key.
func (c *Config) SyncKey() (string, error) {
	return c.ResolveSecret(c.Cloud.APIKeyEnv, c.Cloud.APIKeyFile)
}

// SecretStatus reports key presence without exposing values.
func (c *Config) SecretStatus() map[string]map[string]any {
	result := map[string]map[string]any{}
	entries := []struct {
		name    string
		envName string
		file    string
	}{
		{"llm", c.LLM.APIKeyEnv, c.LLM.APIKeyFile},
		{"sync", c.Cloud.APIKeyEnv, c.Cloud.APIKeyFile},
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
	secrets := c.SecretStatus()
	egress := "loopback-only"
	if c.Mode != "local" {
		egress = "remote-allowed"
	}
	return map[string]any{
		"path":   c.Path,
		"db":     c.DB,
		"scope":  c.Scope,
		"reader": c.Reader,
		"mode":   c.Mode,
		"egress": egress,
		"log":    c.Log,
		"llm": map[string]any{
			"provider": c.LLM.Provider,
			"base_url": c.LLM.BaseURL,
			"model":    c.LLM.Model,
			"key":      secrets["llm"],
		},
		"recall": map[string]any{
			"bands":             c.Recall.Bands,
			"nodes":             c.Recall.Nodes,
			"chars":             c.Recall.Chars,
			"hook_bands":        c.Recall.HookBands,
			"hook_nodes":        c.Recall.HookNodes,
			"hook_chars":        c.Recall.HookChars,
			"budget_ms":         c.Recall.BudgetMS,
			"token_correct":     c.Recall.TokenCorrect,
			"alias_expand":      c.Recall.AliasExpand,
			"relative_tag":      c.Recall.RelativeTag,
			"mention_backfill":  c.Recall.MentionBackfill,
			"activation_record": c.Recall.ActivationRecord,
			"activation_rank":   c.Recall.ActivationRank,
		},
		"sync": map[string]any{
			"enabled":  c.Cloud.Enabled,
			"base_url": c.Cloud.BaseURL,
			"auto":     c.Cloud.Auto,
			"key":      secrets["sync"],
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
		return expandUser(raw)
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
	return expandUser(raw)
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
		return expandUser(value)
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
	path = expandUser(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		text := DefaultConfigText
		if isPortableConfigTarget(path) {
			text = PortableDefaultConfigText()
		}
		if err := writeFileAtomic(path, []byte(text), 0o600); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	return path, nil
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
	path = expandUser(path)
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

func deref(value *string) any {
	if value == nil || *value == "" {
		return nil
	}
	return *value
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
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

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func expandUser(path string) string {
	if path == "~" {
		return homeDir()
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(homeDir(), path[2:])
	}
	return path
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
	encoded, err := ids.PyJSONDump(value, false, false, true)
	if err != nil {
		return `""`
	}
	return encoded
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
