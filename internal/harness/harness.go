// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package harness installs the Rolling-Memory skill, MCP registration, hook
// events and instruction block into the five supported harnesses.
//
// The mechanical core (CLI / MCP / hooks) is harness-neutral; the only
// harness-specific knowledge is where each harness keeps four surfaces:
//
//  1. skill root -- dropping <name>/SKILL.md there makes the agent discover it;
//  2. MCP registration -- a JSON file, a TOML section, or a dsh profile patch;
//  3. hook events -- only some harnesses declare hooks; others degrade to the
//     agent-driven remember / recall path;
//  4. instruction file -- an AGENTS.md / CLAUDE.md / CODEBUDDY.md marker block.
//
// All writes are conservative: every modified file is backed up first
// (<file>.rolling-memory.<ts>.bak), a same-named entry that is not ours is a
// conflict (--force overrides), and re-running install is idempotent.
package harness

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"unicode"

	"github.com/rocareer/rolling-memory/internal/clock"
	"github.com/rocareer/rolling-memory/internal/config"
)

const (
	// DefaultName is the default MCP server name.
	DefaultName = "rolling-memory"
	// BackupTag marks the backup file prefix (<file>.rolling-memory.<ts>.bak).
	BackupTag = "rolling-memory"
	// InstructionBegin opens the instruction marker block.
	InstructionBegin = "<!-- rolling-memory:begin -->"
	// InstructionEnd closes the instruction marker block.
	InstructionEnd = "<!-- rolling-memory:end -->"
	// ShimName is the POSIX launcher filename.
	ShimName = "rolling-memory.sh"
	// DefaultDSHProfile is the dsh profile that receives the MCP patch.
	DefaultDSHProfile = "web"

	windowsShimName = "rolling-memory.cmd"
)

// HookEvent is one (event, hook subcommand, timeout seconds) triple.
type HookEvent struct {
	Event   string
	Sub     string
	Timeout int
}

// HookEvents are the session points the install wires. Memory capture and
// crystallization are self-managing (background ingest + recall-time Noetic),
// so only the per-prompt recall nudge is wired; the historical SessionStart /
// Stop hooks are retired.
var HookEvents = []HookEvent{
	{Event: "UserPromptSubmit", Sub: "recall", Timeout: 15},
}

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// Action statuses (closed set).
const (
	StatusPlanned   = "planned"
	StatusInstalled = "installed"
	StatusUpdated   = "updated"
	StatusUnchanged = "unchanged"
	StatusRemoved   = "removed"
	StatusSkipped   = "skipped"
	StatusConflict  = "conflict"
	StatusError     = "error"
	StatusMissing   = "missing"
	StatusOK        = "ok"
)

func failedStatus(status string) bool {
	return status == StatusConflict || status == StatusError || status == StatusMissing
}

// Harness describes where one harness keeps its skills, MCP servers, hooks and
// instructions.
type Harness struct {
	ID                  string
	Label               string
	Detect              string
	SkillUser           *string
	SkillUserAlt        *string // doubao only: second user skill root (client-reported local_skill_paths)
	SkillProject        string
	MCPKind             string // "json" | "toml" | "dsh"
	MCPPath             string
	MCPLegacyPath       string // workbuddy only: the host's dotted .mcp.json kept for hosts that already read it
	MCPKeys             []string
	MCPProjectPath      *string
	MCPSkipReason       string // no writable MCP target (host-side registration); reported instead of a write
	HooksKind           string // "claude" = event -> groups style JSON
	HooksPath           *string
	HooksKeys           []string
	HooksEnabledKeys    []string
	HooksExtras         map[string]any
	InstructionsUser    *string
	InstructionsProject string
	ProfilesDir         *string // dsh only: ~/.dsh/profiles
	Notes               string
}

func stringPtr(value string) *string { return &value }

// BuildHarnesses returns the adapter registry with paths resolved under home.
func BuildHarnesses(home, dshHome string) []Harness {
	if home == "" {
		if resolved, err := os.UserHomeDir(); err == nil {
			home = resolved
		}
	}
	if dshHome == "" {
		dshHome = ResolveDSHHome(home)
	}
	profiles := filepath.Join(dshHome, "profiles")
	return []Harness{
		{
			ID:                  "zcode",
			Label:               "ZCode",
			Detect:              filepath.Join(home, ".zcode"),
			SkillUser:           stringPtr(filepath.Join(home, ".zcode", "skills")),
			SkillProject:        ".zcode/skills",
			MCPKind:             "json",
			MCPPath:             filepath.Join(home, ".zcode", "cli", "config.json"),
			MCPKeys:             []string{"mcp", "servers"},
			MCPProjectPath:      stringPtr(".zcode/config.json"),
			HooksKind:           "claude",
			HooksPath:           stringPtr(filepath.Join(home, ".zcode", "cli", "config.json")),
			HooksKeys:           []string{"hooks", "events"},
			HooksEnabledKeys:    []string{"hooks", "enabled"},
			HooksExtras:         map[string]any{"enabled": true, "statusMessage": "Rolling-Memory"},
			InstructionsUser:    stringPtr(filepath.Join(home, ".zcode", "AGENTS.md")),
			InstructionsProject: "AGENTS.md",
			Notes:               "user config: mcp.servers + hooks.events (hooks.enabled must be true)",
		},
		{
			ID:                  "claude-code",
			Label:               "Claude Code",
			Detect:              filepath.Join(home, ".claude"),
			SkillUser:           stringPtr(filepath.Join(home, ".claude", "skills")),
			SkillProject:        ".claude/skills",
			MCPKind:             "json",
			MCPPath:             filepath.Join(home, ".claude.json"),
			MCPKeys:             []string{"mcpServers"},
			MCPProjectPath:      stringPtr(".mcp.json"),
			HooksKind:           "claude",
			HooksPath:           stringPtr(filepath.Join(home, ".claude", "settings.json")),
			HooksKeys:           []string{"hooks"},
			InstructionsUser:    stringPtr(filepath.Join(home, ".claude", "CLAUDE.md")),
			InstructionsProject: "CLAUDE.md",
			Notes:               "project scope uses .mcp.json (committable)",
		},
		{
			ID:                  "codex",
			Label:               "Codex",
			Detect:              filepath.Join(home, ".codex"),
			SkillUser:           stringPtr(filepath.Join(home, ".codex", "skills")),
			MCPKind:             "toml",
			MCPPath:             filepath.Join(home, ".codex", "config.toml"),
			MCPKeys:             []string{"mcp_servers"},
			InstructionsUser:    stringPtr(filepath.Join(home, ".codex", "AGENTS.md")),
			InstructionsProject: "AGENTS.md",
			Notes:               "no hooks; skills live next to the built-in .system/ dir",
		},
		{
			ID:                  "dsh",
			Label:               "DeepSeek Harness (dsh)",
			Detect:              dshHome,
			SkillUser:           stringPtr(filepath.Join(dshHome, "skills")),
			SkillProject:        ".dsh/skills",
			MCPKind:             "dsh",
			MCPPath:             profiles,
			ProfilesDir:         stringPtr(profiles),
			InstructionsUser:    stringPtr(filepath.Join(dshHome, "AGENTS.md")),
			InstructionsProject: "AGENTS.md",
			Notes:               "MCP goes into the profile patch; per-profile, effective for new sessions",
		},
		{
			ID:                  "codebuddy",
			Label:               "CodeBuddy (WorkBuddy)",
			Detect:              filepath.Join(home, ".codebuddy"),
			SkillUser:           stringPtr(filepath.Join(home, ".codebuddy", "skills")),
			SkillProject:        ".codebuddy/skills",
			MCPKind:             "json",
			MCPPath:             filepath.Join(home, ".codebuddy", "mcp.json"),
			MCPKeys:             []string{"mcpServers"},
			InstructionsProject: "CODEBUDDY.md",
			Notes:               "no verified hook surface; instruction file is project-level CODEBUDDY.md",
		},
	}
}

func harnessIndex(harnesses []Harness) map[string]Harness {
	index := make(map[string]Harness, len(harnesses))
	for _, harness := range harnesses {
		index[harness.ID] = harness
	}
	return index
}

// BuildExtendedHarnesses returns additional open-source coding agent harnesses
// (Cursor, Windsurf, Cline) that can be explicitly targeted by ID without
// altering the frozen 5-harness default registry.
func BuildExtendedHarnesses(home string) []Harness {
	if home == "" {
		if resolved, err := os.UserHomeDir(); err == nil {
			home = resolved
		}
	}
	return []Harness{
		{
			ID:                  "cursor",
			Label:               "Cursor",
			Detect:              filepath.Join(home, ".cursor"),
			SkillUser:           stringPtr(filepath.Join(home, ".cursor", "skills")),
			SkillProject:        ".cursor/skills",
			MCPKind:             "json",
			MCPPath:             filepath.Join(home, ".cursor", "mcp.json"),
			MCPKeys:             []string{"mcpServers"},
			MCPProjectPath:      stringPtr(".cursor/mcp.json"),
			InstructionsUser:    stringPtr(filepath.Join(home, ".cursor", "AGENTS.md")),
			InstructionsProject: "AGENTS.md",
			Notes:               "user config ~/.cursor/mcp.json; project config .cursor/mcp.json",
		},
		{
			ID:                  "windsurf",
			Label:               "Windsurf",
			Detect:              filepath.Join(home, ".codeium", "windsurf"),
			SkillUser:           stringPtr(filepath.Join(home, ".codeium", "windsurf", "skills")),
			SkillProject:        ".windsurf/skills",
			MCPKind:             "json",
			MCPPath:             filepath.Join(home, ".codeium", "windsurf", "mcp_config.json"),
			MCPKeys:             []string{"mcpServers"},
			InstructionsUser:    stringPtr(filepath.Join(home, ".codeium", "windsurf", "AGENTS.md")),
			InstructionsProject: "AGENTS.md",
			Notes:               "user config ~/.codeium/windsurf/mcp_config.json",
		},
		{
			ID:                  "cline",
			Label:               "Cline / Roo Code",
			Detect:              filepath.Join(home, ".cline"),
			SkillUser:           stringPtr(filepath.Join(home, ".cline", "skills")),
			SkillProject:        ".cline/skills",
			MCPKind:             "json",
			MCPPath:             filepath.Join(home, ".cline", "mcp_settings.json"),
			MCPKeys:             []string{"mcpServers"},
			InstructionsProject: ".clinerules",
			Notes:               "MCP settings under ~/.cline/mcp_settings.json; project rules in .clinerules",
		},
		{
			ID:            "workbuddy",
			Label:         "WorkBuddy",
			Detect:        filepath.Join(home, ".workbuddy"),
			SkillUser:     stringPtr(filepath.Join(home, ".workbuddy", "skills")),
			MCPKind:       "json",
			MCPPath:       filepath.Join(home, ".workbuddy", "mcp.json"),
			MCPLegacyPath: filepath.Join(home, ".workbuddy", ".mcp.json"),
			MCPKeys:       []string{"mcpServers"},
			Notes:         "user config ~/.workbuddy/mcp.json (WorkBuddy connector docs); dotted ~/.workbuddy/.mcp.json also written for hosts that read it; no session hooks",
		},
		{
			ID:            "doubao",
			Label:         "Doubao",
			Detect:        filepath.Join(home, "Doubao"),
			SkillUser:     stringPtr(filepath.Join(home, "Doubao", "skills")),
			SkillUserAlt:  stringPtr(filepath.Join(home, ".agents", "skills")),
			MCPKind:       "none",
			MCPSkipReason: "registration lives in the Doubao app; wire the address-style connector (rolling-memory mcp --http)",
			Notes:         "dual skill roots (client-reported local_skill_paths); MCP projected by the host manifest",
		},
	}
}

// InstallOptions carries the install / uninstall / check keyword arguments.
type InstallOptions struct {
	Home            string
	Cwd             string
	Name            string
	SkillName       string
	Binary          string
	SkillFile       *string
	Lang            string
	Scope           string
	DB              string
	Project         bool
	Hooks           bool
	Instructions    bool
	MCPOnly         bool
	Force           bool
	DryRun          bool
	DesktopShortcut bool
	DSHProfile      string
}

// DefaultInstallOptions returns the dataclass defaults (name, skill name,
// dsh profile) resolved against home and cwd.
func DefaultInstallOptions(home, cwd string) InstallOptions {
	if home == "" {
		if resolved, err := os.UserHomeDir(); err == nil {
			home = resolved
		}
	}
	if cwd == "" {
		if resolved, err := os.Getwd(); err == nil {
			cwd = resolved
		}
	}
	return InstallOptions{
		Home:       home,
		Cwd:        cwd,
		Name:       DefaultName,
		SkillName:  DefaultName,
		DSHProfile: DefaultDSHProfile,
	}
}

// withDefaults fills the dataclass defaults that must exist before paths are
// resolved. SkillName is deliberately left empty so a caller (the CLI) can
// mirror `--skill-name or --name` before the harness applies its own default.
func (o InstallOptions) withDefaults() InstallOptions {
	if o.Name == "" {
		o.Name = DefaultName
	}
	if o.DSHProfile == "" {
		o.DSHProfile = DefaultDSHProfile
	}
	return o
}

func (o InstallOptions) normalized() InstallOptions {
	o = o.withDefaults()
	if o.SkillName == "" {
		o.SkillName = DefaultName
	}
	if norm, err := NormalizeInstallLang(o.Lang); err == nil && norm != "" {
		o.Lang = norm
	}
	return o
}

// NormalizeInstallLang canonicalizes a language code into "en", "zh-CN", or ""
// (unspecified). AdminLangFor maps the canonical install language onto the
// admin console's LangKey ("en" or "zh-cn").
func NormalizeInstallLang(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	switch strings.ToLower(strings.ReplaceAll(trimmed, "_", "-")) {
	case "en", "en-us", "en-gb", "english":
		return "en", nil
	case "zh", "zh-cn", "zh-hans", "cn", "chinese":
		return "zh-CN", nil
	default:
		return "", fmt.Errorf("invalid language %s: expected 'en' or 'zh-CN'", pyRepr(trimmed))
	}
}

// AdminLangFor returns the admin console locale key ("en" or "zh-cn") for a
// canonical install language.
func AdminLangFor(installLang string) string {
	norm, err := NormalizeInstallLang(installLang)
	if err == nil && norm == "en" {
		return "en"
	}
	if err == nil && norm == "zh-CN" {
		return "zh-cn"
	}
	return ""
}

// UILangStatePath returns the shared language state file read by the admin
// console (`~/.config/rolling-memory/ui-lang.json`).
func UILangStatePath(home string) string {
	if home == "" {
		if resolved, err := os.UserHomeDir(); err == nil {
			home = resolved
		}
	}
	return filepath.Join(home, ".config", "rolling-memory", "ui-lang.json")
}

// WriteAdminDefaultLang persists the selected language so the admin console and
// future installs pick up the same default language automatically.
func WriteAdminDefaultLang(home, installLang string) (string, error) {
	norm, err := NormalizeInstallLang(installLang)
	if err != nil || norm == "" {
		return "", err
	}
	adminLang := AdminLangFor(norm)
	path := UILangStatePath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	payload := map[string]string{
		"lang":         norm,
		"default_lang": adminLang,
		"updated_at":   clock.Now().UTC().Format("2006-01-02T15:04:05Z"),
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, append(encoded, '\n'), 0o644); err != nil {
		return "", err
	}
	return adminLang, nil
}

// ReadSavedInstallLang reads the previously persisted install language ("en" or
// "zh-CN") from `~/.config/rolling-memory/ui-lang.json`, returning "" if unset.
func ReadSavedInstallLang(home string) string {
	if home == "" {
		return ""
	}
	raw, err := os.ReadFile(UILangStatePath(home))
	if err != nil {
		return ""
	}
	var payload map[string]string
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	if norm, err := NormalizeInstallLang(firstNonEmptyString(payload["lang"], payload["default_lang"])); err == nil {
		return norm
	}
	return ""
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// ResolvedBinary is the launcher command registered in config files.
func (o InstallOptions) ResolvedBinary() string {
	if o.Binary != "" {
		return expandUserBinary(o.Binary, o.Home)
	}
	return DefaultShimPath(o.Home)
}

// expandUserBinary resolves a leading ~ / $HOME / ${HOME} in a user-supplied
// binary. Host runtimes do not expand those tokens (a literal path fails to
// spawn), so the written entry must already be absolute; bare command names are
// left untouched to keep PATH lookup semantics.
func expandUserBinary(binary, home string) string {
	if home == "" {
		if resolved, err := os.UserHomeDir(); err == nil {
			home = resolved
		}
	}
	if home == "" {
		return binary
	}
	rest := ""
	switch {
	case binary == "~":
		return home
	case strings.HasPrefix(binary, "~/"):
		rest = binary[2:]
	case strings.HasPrefix(binary, "${HOME}/"):
		rest = binary[len("${HOME}/"):]
	case strings.HasPrefix(binary, "$HOME/"):
		rest = binary[len("$HOME/"):]
	default:
		return binary
	}
	return filepath.Join(home, filepath.FromSlash(rest))
}

// DefaultShimPath returns the launcher path under $XDG_DATA_HOME (POSIX) or
// %LOCALAPPDATA% (Windows), mirroring default_shim_path in the standard
// reference.
func DefaultShimPath(home string) string {
	return defaultShimPathFor(runtime.GOOS, home)
}

func defaultShimPathFor(goos, home string) string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		if goos == "windows" {
			base = os.Getenv("LOCALAPPDATA")
		}
		if base == "" {
			base = filepath.Join(home, ".local", "share")
		}
	}
	return filepath.Join(base, "rolling-memory", "bin", shimNameFor(goos))
}

// DefaultSkillFile returns the shipped SKILL.md next to the binary
// (<repo>/bin/rolling-memory -> <repo>/skills/rolling-memory/SKILL.md), with
// fallback to sibling ./skills/rolling-memory/SKILL.md or the active portable root.
func DefaultSkillFile() string {
	executable := executablePath()
	primary := filepath.Clean(filepath.Join(filepath.Dir(executable), "..", "skills", "rolling-memory", "SKILL.md"))
	if fileExists(primary) {
		return primary
	}
	sibling := filepath.Clean(filepath.Join(filepath.Dir(executable), "skills", "rolling-memory", "SKILL.md"))
	if fileExists(sibling) {
		return sibling
	}
	if root := config.PortableRoot(); root != "" {
		portableSkill := filepath.Clean(filepath.Join(root, "skills", "rolling-memory", "SKILL.md"))
		if fileExists(portableSkill) {
			return portableSkill
		}
	}
	return primary
}

// WithProjectDefaults applies project scope defaults: a per-project DB and
// scope, README-style. The slug keeps Unicode letters/digits (CJK included).
func WithProjectDefaults(options InstallOptions) InstallOptions {
	options = options.withDefaults()
	if !options.Project {
		return options
	}
	if options.DB == "" {
		options.DB = filepath.Join(options.Cwd, ".rolling-memory", "memory.db")
	}
	if options.Scope == "" {
		slug := projectSlug(filepath.Base(options.Cwd))
		options.Scope = "project:" + slug
	}
	return options
}

// projectSlug keeps alphanumeric runes and ".-_", replaces everything else
// with "-", trims leading/trailing "-", and falls back to "project".
func projectSlug(name string) string {
	var builder strings.Builder
	for _, char := range name {
		if unicode.IsLetter(char) || unicode.IsNumber(char) || char == '.' || char == '_' || char == '-' {
			builder.WriteRune(char)
		} else {
			builder.WriteRune('-')
		}
	}
	slug := strings.Trim(builder.String(), "-")
	if slug == "" {
		return "project"
	}
	return slug
}

// Action is one report line.
type Action struct {
	Harness   string
	Component string // skill | mcp | hooks | instructions | binary
	Path      string
	Status    string
	Detail    string
}

// ToDict returns the JSON shape of the action.
func (a Action) ToDict() map[string]any {
	return map[string]any{
		"harness":   a.Harness,
		"component": a.Component,
		"path":      a.Path,
		"status":    a.Status,
		"detail":    a.Detail,
	}
}

// Report is the install / uninstall / check result.
type Report struct {
	Mode    string
	Options InstallOptions
	Actions []Action
	Extra   map[string]any
}

// Add appends one action line.
func (r *Report) Add(harnessID, component, path, status, detail string) {
	r.Actions = append(r.Actions, Action{
		Harness:   harnessID,
		Component: component,
		Path:      path,
		Status:    status,
		Detail:    detail,
	})
}

// Failures returns the actions whose status is conflict, error or missing.
func (r *Report) Failures() []Action {
	failures := make([]Action, 0)
	for _, action := range r.Actions {
		if failedStatus(action.Status) {
			failures = append(failures, action)
		}
	}
	return failures
}

// ToDict returns the JSON report: {ok, mode, name, binary, dry_run, actions,
// ...extra}.
func (r *Report) ToDict() map[string]any {
	actions := make([]any, 0, len(r.Actions))
	for _, action := range r.Actions {
		actions = append(actions, action.ToDict())
	}
	result := map[string]any{
		"ok":      len(r.Failures()) == 0,
		"mode":    r.Mode,
		"name":    r.Options.Name,
		"binary":  r.Options.ResolvedBinary(),
		"dry_run": r.Options.DryRun,
		"actions": actions,
	}
	for key, value := range r.Extra {
		result[key] = value
	}
	return result
}

// ValidateInstallOptions checks the server and skill names (they land in file
// names and TOML/JSON headers, so the alphabet is deliberately narrow).
func ValidateInstallOptions(options InstallOptions) error {
	if _, err := NormalizeInstallLang(options.Lang); err != nil {
		return err
	}
	options = options.normalized()
	if err := validateName(options.Name, "server name"); err != nil {
		return err
	}
	return validateName(options.SkillName, "skill name")
}

func validateName(value, what string) error {
	if !nameRE.MatchString(value) {
		return fmt.Errorf("invalid %s %s: must match %s", what, pyRepr(value), nameRE.String())
	}
	return nil
}

// backupFile copies path aside (file copy semantics) and returns the backup
// path; a missing source is not an error.
func backupFile(path string) (string, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	stamp := clock.Now().Format("20060102150405")
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for sequence := uint64(0); ; sequence++ {
		suffix := stamp
		if sequence > 0 {
			suffix += strconv.FormatUint(sequence, 10)
		}
		target := path + "." + BackupTag + "." + suffix + ".bak"
		file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, writeErr := file.Write(content)
		closeErr := file.Close()
		if writeErr != nil {
			return "", writeErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		_ = os.Chmod(target, info.Mode().Perm())
		_ = os.Chtimes(target, info.ModTime(), info.ModTime())
		return target, nil
	}
}

// writeText backs up an existing file and writes text as UTF-8.
func writeText(path, text string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	backup := ""
	if _, err := os.Stat(path); err == nil {
		var backupErr error
		backup, backupErr = backupFile(path)
		if backupErr != nil {
			return "", backupErr
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.WriteFile(path, []byte(text), 0o666); err != nil {
		return backup, err
	}
	return backup, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// pySplitLines mirrors str.splitlines() for the line boundaries we can meet in
// skill, TOML, Markdown and JSON files.
func pySplitLines(text string) []string {
	if text == "" {
		return nil
	}
	runes := []rune(text)
	lines := make([]string, 0, 8)
	start := 0
	for index := 0; index < len(runes); index++ {
		char := runes[index]
		switch char {
		case '\n', '\v', '\f', '\r', '\u0085', '\u2028', '\u2029', '\u001c', '\u001d', '\u001e':
			lines = append(lines, string(runes[start:index]))
			if char == '\r' && index+1 < len(runes) && runes[index+1] == '\n' {
				index++
			}
			start = index + 1
		}
	}
	if start < len(runes) {
		lines = append(lines, string(runes[start:]))
	}
	return lines
}

// isPySpace mirrors standard str.isspace for the whitespace set in the data we
// touch (rstrip/strip defaults).
func isPySpace(char rune) bool {
	switch char {
	case '\t', '\n', '\v', '\f', '\r', ' ', '\u0085', '\u00a0', '\u1680',
		'\u2028', '\u2029', '\u202f', '\u205f', '\u3000':
		return true
	}
	if char >= '\u001c' && char <= '\u001f' {
		return true
	}
	return char >= '\u2000' && char <= '\u200a'
}

func pyStripRight(text string) string {
	return strings.TrimRightFunc(text, isPySpace)
}

// pyRepr mirrors a simple repr() for error messages.
func pyRepr(value string) string {
	escaped := strings.ReplaceAll(value, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `'`, `\'`)
	return "'" + escaped + "'"
}

// pyString mirrors str() for the scalar values that reach detail strings.
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
	case json.Number:
		return typed.String()
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64)
	}
	return fmt.Sprint(value)
}

// pyListRepr mirrors repr() of a list of tool names ("['remember', 'recall']").
func pyListRepr(values []any) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok {
			parts = append(parts, pyRepr(text))
			continue
		}
		parts = append(parts, pyString(value))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
