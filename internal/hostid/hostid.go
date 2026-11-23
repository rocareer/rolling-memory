// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package hostid is the single identity registry for coding harnesses and the
// presence ledger that records live client attachments.
//
// Identity answers one question only: which host owns the process that is
// talking to Rolling Memory right now. It is derived from session-scoped
// environment markers and, when those are ambiguous, from the process
// ancestry. Plain shell exports (project directories, home directories) are
// workspace hints, not identity: they are inherited by every descendant and
// therefore cannot distinguish a nested host from the terminal that started
// it.
package hostid

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ID is a closed-set harness identifier. The empty string is never used:
// unknown hosts carry Unknown so callers cannot mistake "not detected" for a
// concrete host.
type ID string

const (
	Unknown    ID = "unknown"
	CLI        ID = "cli"
	ZCode      ID = "zcode"
	ClaudeCode ID = "claude-code"
	Codex      ID = "codex"
	DSH        ID = "dsh"
	OpenCode   ID = "opencode"
	Cursor     ID = "cursor"
	Windsurf   ID = "windsurf"
	Cline      ID = "cline"
	CodeBuddy  ID = "codebuddy"
	WorkBuddy  ID = "workbuddy"
	Doubao     ID = "doubao"
)

// envRule is one environment marker. Strength 2 markers are stamped by the
// host on the processes it owns; strength 1 markers are host-specific but can
// be exported by a shell; strength 0 markers are workspace hints only and
// never establish identity.
type envRule struct {
	key      string
	value    string
	strength int
}

type hostDef struct {
	id         ID
	label      string
	sessionEnv []string
	rules      []envRule
	processes  []string
}

// registry order is the tie-breaker when two hosts carry equally strong
// markers and the process ancestry cannot separate them. Nested-session
// markers come first, matching the historical precedence.
var registry = []hostDef{
	{
		id: DSH, label: "DeepSeek Harness (dsh)",
		sessionEnv: []string{"DSH_SESSION_ID"},
		rules:      []envRule{{"DSH_SESSION_ID", "", 2}, {"DSH_SHELL", "", 2}},
		processes:  []string{"dsh", "deepseek-harness", "deepseek"},
	},
	{
		id: ZCode, label: "ZCode",
		sessionEnv: []string{"ZCODE_SESSION_ID"},
		rules: []envRule{
			{"ZCODE_SESSION_ID", "", 2},
			{"ZCODE_WORKSPACE", "", 1},
			{"ZCODE_PROJECT_DIR", "", 0},
			{"TERM_PROGRAM", "zcode", 0},
		},
		processes: []string{"zcode"},
	},
	{
		id: ClaudeCode, label: "Claude Code",
		sessionEnv: []string{"CLAUDE_SESSION_ID"},
		rules: []envRule{
			{"CLAUDE_SESSION_ID", "", 2},
			{"CLAUDE_CODE_ENTRYPOINT", "", 2},
			{"CLAUDECODE", "", 2},
			{"CLAUDE_PROJECT_DIR", "", 0},
		},
		processes: []string{"claude"},
	},
	{
		id: OpenCode, label: "OpenCode",
		sessionEnv: []string{"OPENCODE_SESSION_ID"},
		rules: []envRule{
			{"OPENCODE_SESSION_ID", "", 2},
			{"OPENCODE", "", 0},
			{"OPENCODE_CLIENT", "", 0},
			{"OPENCODE_TERMINAL", "", 0},
		},
		processes: []string{"opencode", "opencode-cli"},
	},
	{
		id: Codex, label: "Codex",
		sessionEnv: []string{"CODEX_THREAD_ID"},
		rules:      []envRule{{"CODEX_THREAD_ID", "", 2}, {"CODEX_HOME", "", 0}},
		processes:  []string{"codex"},
	},
	{
		id: Cursor, label: "Cursor",
		sessionEnv: []string{"CURSOR_TRACE_ID"},
		rules:      []envRule{{"CURSOR_TRACE_ID", "", 2}},
		processes:  []string{"cursor"},
	},
	{
		id: Windsurf, label: "Windsurf",
		sessionEnv: []string{"WINDSURF_SESSION"},
		rules:      []envRule{{"WINDSURF_SESSION", "", 2}},
		processes:  []string{"windsurf", "devin"},
	},
	{
		id: CodeBuddy, label: "CodeBuddy",
		sessionEnv: []string{"CODEBUDDY_SESSION_ID"},
		rules:      []envRule{{"CODEBUDDY_SESSION_ID", "", 2}, {"CODEBUDDY_HOME", "", 0}},
		processes:  []string{"codebuddy"},
	},
	{
		id: WorkBuddy, label: "WorkBuddy",
		sessionEnv: []string{"WORKBUDDY_SESSION_ID"},
		rules:      []envRule{{"WORKBUDDY_SESSION_ID", "", 2}, {"WORKBUDDY_HOME", "", 0}},
		processes:  []string{"workbuddy"},
	},
	{
		id: Doubao, label: "Doubao (豆包)",
		sessionEnv: []string{"DOUBAO_SESSION_ID"},
		rules:      []envRule{{"DOUBAO_SESSION_ID", "", 2}, {"DOUBAO_HOME", "", 0}},
		processes:  []string{"doubao"},
	},
	{
		id: Cline, label: "Cline / Roo Code",
		sessionEnv: []string{"CLINE_SESSION_ID"},
		rules:      []envRule{{"CLINE_SESSION_ID", "", 2}, {"CLINE_HOME", "", 0}},
		processes:  []string{"cline", "roo"},
	},
}

// Identity is one resolved host attachment.
type Identity struct {
	ID        ID
	Label     string
	SessionID string
	Evidence  string
	// Strong is true when the identity came from an explicit stamp, a
	// session-scoped marker, or the process ancestry. Weak shell exports
	// leave it false and must not be presented as the current host.
	Strong bool
}

// IsKnown reports whether id belongs to the registry.
func IsKnown(id ID) bool {
	if id == Unknown || id == CLI {
		return true
	}
	for _, h := range registry {
		if h.id == id {
			return true
		}
	}
	return false
}

// Normalize maps aliases and case variants onto registry IDs. Unknown values
// are preserved so an explicit ROLLING_MEMORY_HARNESS override can name a
// host this build does not know.
func Normalize(value string) ID {
	v := strings.ToLower(strings.TrimSpace(value))
	switch v {
	case "":
		return Unknown
	case "unknown", "none":
		return Unknown
	case "claude", "claude-code", "claude_code":
		return ClaudeCode
	case "deepseek", "deepseek-harness", "deepseek_harness", "dsh":
		return DSH
	case "opencode", "open-code", "open_code":
		return OpenCode
	case "codex", "codex-cli":
		return Codex
	case "cursor":
		return Cursor
	case "windsurf", "devin":
		return Windsurf
	case "cline", "roo", "roo-code", "roo_code":
		return Cline
	case "codebuddy", "code-buddy":
		return CodeBuddy
	case "workbuddy", "work-buddy":
		return WorkBuddy
	case "doubao", "豆包":
		return Doubao
	case "zcode":
		return ZCode
	case "cli", "shell":
		return CLI
	}
	return ID(v)
}

// Label returns the display name stored in the attachment ledger. Custom
// explicit IDs keep their raw value so the console never invents a brand.
func Label(id ID) string {
	if id == Unknown {
		return "Unknown"
	}
	if id == CLI {
		return "CLI"
	}
	for _, h := range registry {
		if h.id == id {
			return h.label
		}
	}
	if strings.TrimSpace(string(id)) == "" {
		return "Unknown"
	}
	return string(id)
}

// Detect resolves the current process identity. It consults the environment
// first and walks the process ancestry only when the environment carries no
// conclusive session marker.
func Detect() Identity {
	env := environment()
	var resolve func() []Process
	if os.Getenv("ROLLING_MEMORY_DISABLE_PROCESS_ANCESTRY") != "1" {
		resolve = Ancestry
	}
	if identity, ok := detectFromEnv(env, resolve); ok {
		return identity
	}
	if resolve != nil {
		if identity, ok := detectFromAncestry(resolve(), env); ok {
			return identity
		}
	}
	return Identity{ID: Unknown, Label: Label(Unknown)}
}

// DetectFromEnvOnly resolves identity without consulting the process
// ancestry. It is the contract behind text.DetectHarnessFromEnv and the
// one-shot CLI fallback: a surprising parent process in a test or a wrapper
// must not turn an otherwise unknown invocation into a concrete host.
func DetectFromEnvOnly() Identity {
	if identity, ok := detectFromEnv(environment(), nil); ok {
		return identity
	}
	return Identity{ID: Unknown, Label: Label(Unknown)}
}

// DetectFromEnv returns the resolved host ID or "" when no host marker is
// present. It exists for callers that only need the machine identifier.
func DetectFromEnv() string {
	id := DetectFromEnvOnly().ID
	if id == Unknown || id == CLI {
		return ""
	}
	return string(id)
}

// DetectWith resolves identity against an explicit environment and ancestry.
// It is the deterministic seam used by tests.
func DetectWith(env map[string]string, ancestry []Process) Identity {
	resolve := func() []Process { return ancestry }
	if identity, ok := detectFromEnv(env, resolve); ok {
		return identity
	}
	if identity, ok := detectFromAncestry(ancestry, env); ok {
		return identity
	}
	return Identity{ID: Unknown, Label: Label(Unknown)}
}

func detectFromEnv(env map[string]string, resolveAncestry func() []Process) (Identity, bool) {
	if explicit := strings.TrimSpace(env["ROLLING_MEMORY_HARNESS"]); explicit != "" {
		id := Normalize(explicit)
		if id == Unknown {
			id = ID(strings.ToLower(explicit))
		}
		return Identity{
			ID: id, Label: Label(id), SessionID: sessionFor(id, env),
			Evidence: "env:ROLLING_MEMORY_HARNESS", Strong: true,
		}, true
	}
	type candidate struct {
		id       ID
		evidence string
		session  string
	}
	byID := map[ID]candidate{}
	for _, h := range registry {
		for _, rule := range h.rules {
			if rule.strength < 2 {
				continue
			}
			value := strings.TrimSpace(env[rule.key])
			if value == "" {
				continue
			}
			if rule.value != "" && !strings.EqualFold(value, rule.value) {
				continue
			}
			session := sessionFor(h.id, env)
			if prev, ok := byID[h.id]; ok && prev.session != "" {
				continue
			}
			byID[h.id] = candidate{id: h.id, evidence: "env:" + rule.key, session: session}
			break
		}
	}
	if agent := Normalize(env["AI_AGENT"]); agent != Unknown && IsKnown(agent) {
		if _, ok := byID[agent]; !ok {
			byID[agent] = candidate{id: agent, evidence: "env:AI_AGENT", session: sessionFor(agent, env)}
		}
	}
	if len(byID) == 0 {
		return Identity{}, false
	}
	ids := make([]ID, 0, len(byID))
	for _, h := range registry {
		if _, ok := byID[h.id]; ok {
			ids = append(ids, h.id)
		}
	}
	// CLI is a known environment identity that intentionally has no registry
	// entry. Appending it keeps ids non-empty whenever byID is non-empty, so a
	// CLI-only environment cannot fall through to the slice index below.
	if _, ok := byID[CLI]; ok {
		ids = append(ids, CLI)
	}
	if len(ids) == 1 {
		c := byID[ids[0]]
		return Identity{ID: c.id, Label: Label(c.id), SessionID: c.session, Evidence: c.evidence, Strong: true}, true
	}
	if resolveAncestry != nil {
		if id, name, ok := matchAncestry(resolveAncestry(), ids); ok {
			c := byID[id]
			return Identity{ID: id, Label: Label(id), SessionID: c.session, Evidence: "process:" + name, Strong: true}, true
		}
	}
	c := byID[ids[0]]
	return Identity{ID: c.id, Label: Label(c.id), SessionID: c.session, Evidence: c.evidence, Strong: true}, true
}

func detectFromAncestry(ancestry []Process, env map[string]string) (Identity, bool) {
	if id, name, ok := matchAncestry(ancestry, nil); ok {
		return Identity{
			ID: id, Label: Label(id), SessionID: sessionFor(id, env),
			Evidence: "process:" + name, Strong: true,
		}, true
	}
	return Identity{}, false
}

func sessionFor(id ID, env map[string]string) string {
	for _, h := range registry {
		if h.id != id {
			continue
		}
		for _, key := range h.sessionEnv {
			if value := strings.TrimSpace(env[key]); value != "" {
				return value
			}
		}
	}
	return ""
}

func environment() map[string]string {
	out := map[string]string{}
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		out[key] = value
	}
	return out
}

// Process is one entry in the parent chain, nearest first.
type Process struct {
	PID     int
	PPID    int
	Command string
}

var (
	ancestryOnce sync.Once
	ancestryList []Process
)

// Ancestry returns the current process's parent chain, nearest first. The
// result is cached for the process lifetime; it is only consulted when the
// environment alone cannot name the host.
func Ancestry() []Process {
	ancestryOnce.Do(func() {
		ancestryList = readAncestry()
	})
	return ancestryList
}

func matchAncestry(ancestry []Process, candidates []ID) (ID, string, bool) {
	for _, p := range ancestry {
		base := processBase(p.Command)
		if base == "" {
			continue
		}
		for _, h := range registry {
			if len(candidates) > 0 && !containsID(candidates, h.id) {
				continue
			}
			for _, name := range h.processes {
				if base == strings.ToLower(name) {
					return h.id, base, true
				}
			}
		}
	}
	return Unknown, "", false
}

func containsID(ids []ID, want ID) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// processBase reduces one ps command field to the executable's base name.
// macOS prints the application bundle path (which may contain spaces), while
// other platforms print the short process name; both collapse to a lowercase
// basename that can be compared with the registry.
func processBase(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}
	base := filepath.Base(command)
	if strings.Contains(base, " ") {
		base = strings.Fields(base)[0]
	}
	base = strings.TrimSuffix(base, ".exe")
	return strings.ToLower(strings.TrimSpace(base))
}

// RegistryIDs returns every registered host ID in declaration order. Tests
// and the install-registry parity check use it.
func RegistryIDs() []ID {
	out := make([]ID, 0, len(registry))
	for _, h := range registry {
		out = append(out, h.id)
	}
	return out
}
