// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package hooks implements the fail-open UserPromptSubmit hook.
//
// The hook only observes the prompt: it records the cadence counter used by the
// four-turn attention anchor. Transcript capture and ingestion belong to the
// background sync path, so the hook never reads or writes memory content and
// never raises into the harness. All failures are logged and exit 0.
package hooks

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/memory/facade"
	"github.com/rocareer/rolling-memory/internal/text"
)

const (
	maxHookInput  = 2 * 1024 * 1024
	maxLogBytes   = 2 * 1024 * 1024
	maxImportLine = 200
)

// Options carries the parsed hook flags.
type Options struct {
	JSONOutput bool
	BudgetMS   *int
}

func runHook(cfg *config.Config, event string, payload map[string]any, o Options) (string, error) {
	memory, err := facade.New(cfg)
	if err != nil {
		return "", err
	}
	defer memory.Close()
	if event == "recall" {
		return recallHook(memory, cfg, payload, o)
	}
	return "", fmt.Errorf("unknown hook event '%s'", event)
}

func recallHook(memory *facade.Memory, cfg *config.Config, payload map[string]any, o Options) (string, error) {
	prompt := ""
	if value := firstTruthy(payload["prompt"], payload["user_prompt"]); value != nil {
		prompt = pyStrip(pyStr(value))
	}
	if prompt == "" {
		return "", nil
	}
	session := "default"
	if value := firstTruthy(payload["session_id"], payload["sessionId"]); value != nil {
		session = pyStr(value)
	}
	project := workspaceProject(payload)
	if project != "" {
		if stripped := pyStrip(ids.StripInjected(prompt)); stripped != "" {
			prompt = stripped
		}
	}
	presence := workspacePresence(payload)
	if presence != nil {
		saveActiveWorkspacePresence(*presence, session)
	}
	if project != "" {
		// Capture only: the raw prompt enters `trace` for episodic history and
		// anchor verification, never as an extracted fact.
		captureUserPrompt(memory, cfg, session, prompt, presence)
		if isCadenceReminderDue(memory, cfg.Scope, session) {
			cadenceText := "[Rolling-Memory 4轮节律锚点] 已知文件或报错行就直接核验。若下一步取决于过去的决定、试过的办法或找不到的入口，用具体 cue 调 MCP `recall`；只采用能核实的新线索。"
			logHook(cfg, "recall", map[string]any{"injected": 0, "absent": 1, "cadence": 1})
			return additionalContext(cadenceText, o.JSONOutput, "UserPromptSubmit")
		}
	}
	logHook(cfg, "recall", map[string]any{"injected": 0, "absent": 1})
	return "", nil
}

// additionalContext wraps text for --json output.
func additionalContext(text string, jsonOutput bool, eventName string) (string, error) {
	if !jsonOutput {
		return text, nil
	}
	payload := ids.OrderedMap{
		{Key: "hookSpecificOutput", Value: ids.OrderedMap{
			{Key: "hookEventName", Value: eventName},
			{Key: "additionalContext", Value: text},
		}},
	}
	return ids.PyJSONDump(payload, false, false, false)
}

func renderIntError(err error, jsonOutput bool, stdout, stderr io.Writer) {
	message := ids.Redact(err.Error())
	if jsonOutput {
		encoded, dumpErr := ids.PyJSONDump(ids.OrderedMap{
			{Key: "ok", Value: false},
			{Key: "error", Value: message},
		}, false, false, false)
		if dumpErr == nil {
			fmt.Fprintln(stdout, encoded)
		}
		return
	}
	fmt.Fprintf(stderr, "error: %s\n", message)
}

// HookMain parses argv, reads one JSON payload from stdin and prints the hook
// output. Every failure is logged and exits 0.
func HookMain(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	event := "recall"
	options := Options{}
	remaining := append([]string(nil), args...)
	for len(remaining) > 0 {
		value := remaining[0]
		remaining = remaining[1:]
		switch value {
		case "recall":
			event = value
		case "--json":
			options.JSONOutput = true
		case "--budget-ms":
			options.BudgetMS = nil
			if len(remaining) == 0 {
				continue
			}
			parsed, err := pyInt(remaining[0])
			remaining = remaining[1:]
			if err != nil {
				// Invalid integer flag renders an error (JSON on --json, else stderr)
				// and exits 1.
				renderIntError(err, options.JSONOutput, stdout, stderr)
				return 1
			}
			options.BudgetMS = &parsed
		}
	}
	payload, inputError := readPayload(stdin)
	if inputError != "" {
		fallbackLog("hook input error: " + inputError)
		return 0
	}
	cfg, err := config.Load(config.LoadOptions{})
	if err != nil {
		fallbackLog(err.Error())
		return 0
	}
	output, err := runHook(cfg, event, payload, options)
	if err != nil {
		// Log the error and stay fail-open.
		fallbackLog(err.Error())
		recordHookDecision(cfg, event, payload, "error")
		return 0
	}
	if output != "" {

		if !strings.HasSuffix(output, "\n") {
			output += "\n"
		}
		written, writeErr := io.WriteString(stdout, output)
		var flushErr error
		if flusher, ok := stdout.(interface{ Flush() error }); ok {
			flushErr = flusher.Flush()
		}
		recordHookOutput(cfg, event, payload, output, written, writeErr, flushErr)
	} else {
		recordHookDecision(cfg, event, payload, "empty")
	}
	return 0
}

// readPayload reads the payload, stopping one rune past maxHookInput so
// oversized input is detectable. The returned string is the
// fallback-log message; empty means the payload is usable.
func readPayload(stdin io.Reader) (map[string]any, string) {
	reader := bufio.NewReader(stdin)
	var builder strings.Builder
	count := 0
	for count <= maxHookInput {
		char, size, err := reader.ReadRune()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return map[string]any{}, err.Error()
		}
		if char == utf8.RuneError && size == 1 {
			return map[string]any{}, "invalid UTF-8 in hook input"
		}
		builder.WriteRune(char)
		count++
	}
	if count > maxHookInput {
		return map[string]any{}, "hook input too large"
	}
	raw := pyStrip(builder.String())
	if raw == "" {
		return map[string]any{}, ""
	}
	payload, err := parsePayload(raw)
	if err != nil {
		return map[string]any{}, "invalid JSON: " + ids.PyJSONDecodeMessage(raw, err)
	}
	return payload, ""
}

// parsePayload preserves int/float split via json.Number and drops
// non-object payloads.
func parsePayload(raw string) (map[string]any, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("trailing data after payload")
	}
	payload, ok := value.(map[string]any)
	if !ok {
		return map[string]any{}, nil
	}
	return payload, nil
}

// logHook appends one sorted, redacted JSON line to config.log.
func logHook(cfg *config.Config, event string, data map[string]any) {
	if cfg == nil || cfg.Log == "" {
		return
	}
	if err := ensureHookStateDir(filepath.Dir(cfg.Log)); err != nil {
		return
	}
	rotate(cfg.Log)
	line := map[string]any{"ts": ids.UTCNow(), "event": event}
	if !strings.HasPrefix(cfg.Scope, "mem:golden:") && strings.TrimSpace(cfg.Scope) != "" {
		line["scope"] = cfg.Scope
	}
	for key, value := range data {
		line[key] = value
	}
	encoded, err := ids.PyJSONDump(line, false, true, false)
	if err != nil {
		return
	}
	appendLine(cfg.Log, ids.Redact(encoded))
}

// fallbackLog writes to default_state_dir()/hook.log when the hook cannot use
// the main log.
func fallbackLog(message string) {
	path := filepath.Join(config.DefaultStateDir(), "hook.log")
	if err := ensureHookStateDir(filepath.Dir(path)); err != nil {
		return
	}
	rotate(path)
	appendLine(path, ids.Redact(fmt.Sprintf("%d %s", ids.UTCNow(), message)))
}

// rotate renames an oversized log to <name>.1.
func rotate(path string) {
	info, err := os.Stat(path)
	if err != nil || info.Size() <= maxLogBytes {
		return
	}
	handle, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return
	}
	err = handle.Chmod(0o600)
	closeErr := handle.Close()
	if err != nil || closeErr != nil {
		return
	}
	_ = os.Rename(path, path+".1")
}

// ensureHookStateDir creates the hook state/log directory for first use with
// owner-only access. An existing directory keeps whatever mode its owner chose
// (the permission contract test pins this), so no chmod is applied.
func ensureHookStateDir(dir string) error {
	return os.MkdirAll(dir, 0o700)
}

func appendLine(path, line string) {
	handle, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer handle.Close()
	if err := handle.Chmod(0o600); err != nil {
		return
	}
	_, _ = handle.WriteString(line + "\n")
}

// HookStats reports the main log's line count and last parsed line; small
// helper for status/doctor, not used on the hot path.
func HookStats(cfg *config.Config) (map[string]any, error) {
	if cfg == nil {
		loaded, err := config.Load(config.LoadOptions{})
		if err != nil {
			return nil, err
		}
		cfg = loaded
	}
	info, err := os.Stat(cfg.Log)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]any{"exists": false, "lines": 0}, nil
		}
		return nil, err
	}
	handle, err := os.Open(cfg.Log)
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	lines := 0
	var last any
	reader := bufio.NewReader(handle)
	for {
		chunk, readErr := reader.ReadString('\n')
		if chunk != "" {
			lines++
			parsed := map[string]any{}
			if json.Unmarshal([]byte(chunk), &parsed) == nil {
				last = parsed
			}
		}
		if readErr != nil {
			break
		}
	}
	return map[string]any{"exists": true, "lines": lines, "last": last, "size": info.Size()}, nil
}

// firstTruthy returns the first non-empty, non-zero value in values.
func firstTruthy(values ...any) any {
	for _, value := range values {
		if pyTruthy(value) {
			return value
		}
	}
	return nil
}

func pyTruthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case json.Number:
		if number, err := strconv.ParseFloat(typed.String(), 64); err == nil {
			return number != 0
		}
		return true
	case float64:
		return typed != 0
	case float32:
		return typed != 0
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

// pyStr formats a JSON-shaped value for human display.
func pyStr(value any) string {
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
		raw := typed.String()
		if !strings.ContainsAny(raw, ".eE") {
			return raw
		}
		number, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return raw
		}
		return pyFloatStr(number)
	case float64:
		return pyFloatStr(typed)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	default:
		return fmt.Sprint(value)
	}
}

// pyFloatStr formats a float64 using shortest round-trip representation (or nan/inf/-inf).
func pyFloatStr(value float64) string {
	switch {
	case math.IsNaN(value):
		return "nan"
	case math.IsInf(value, 1):
		return "inf"
	case math.IsInf(value, -1):
		return "-inf"
	}
	encoded, err := ids.PyJSONDump(value, false, false, true)
	if err != nil {
		return strconv.FormatFloat(value, 'g', -1, 64)
	}
	return encoded
}

// pyStrip trims Unicode whitespace and ASCII file/group/record/unit separators (0x1c–0x1f).
func pyStrip(value string) string {
	return strings.TrimFunc(value, func(char rune) bool {
		return unicode.IsSpace(char) || (char >= 0x1c && char <= 0x1f)
	})
}

// pyInt mirrors int(str): surrounding whitespace, an optional sign and single
// underscores between digits.
func pyInt(raw string) (int, error) {
	value := pyStrip(raw)
	digits := value
	sign := 1
	if strings.HasPrefix(digits, "+") {
		digits = digits[1:]
	} else if strings.HasPrefix(digits, "-") {
		sign = -1
		digits = digits[1:]
	}
	cleaned := strings.Builder{}
	previousUnderscore := false
	for index, char := range digits {
		if char == '_' {
			if index == 0 || previousUnderscore {
				return 0, invalidIntLiteral(value)
			}
			previousUnderscore = true
			continue
		}
		if char < '0' || char > '9' {
			return 0, invalidIntLiteral(value)
		}
		previousUnderscore = false
		cleaned.WriteRune(char)
	}
	if cleaned.Len() == 0 || previousUnderscore {
		return 0, invalidIntLiteral(value)
	}
	parsed, err := strconv.ParseInt(cleaned.String(), 10, 64)
	if err != nil {
		return 0, invalidIntLiteral(value)
	}
	return sign * int(parsed), nil
}

func invalidIntLiteral(value string) error {
	return fmt.Errorf("invalid literal for int() with base 10: '%s'", value)
}

func workspaceProject(payload map[string]any) string {
	raw := firstTruthy(payload["cwd"], payload["workspace"], os.Getenv("ROLLING_MEMORY_WORKSPACE"))
	if raw == nil {
		return ""
	}
	cleaned := filepath.Clean(config.ExpandUserPath(pyStrip(pyStr(raw))))
	if cleaned == "" || cleaned == "." || cleaned == "/" {
		return ""
	}
	base := filepath.Base(cleaned)
	if base == "" || base == "." || base == "/" {
		return ""
	}
	return base
}

func workspacePresence(payload map[string]any) *text.PresenceCoordinates {
	raw := firstTruthy(payload["cwd"], payload["workspace"], os.Getenv("ROLLING_MEMORY_WORKSPACE"))
	if raw == nil {
		return nil
	}
	cleaned := filepath.Clean(config.ExpandUserPath(pyStrip(pyStr(raw))))
	if cleaned == "" || cleaned == "." || cleaned == "/" {
		return nil
	}
	harnessName := ""
	if h := firstTruthy(payload["harness"], payload["client"]); h != nil {
		harnessName = pyStrip(pyStr(h))
	}
	role := ""
	if r := firstTruthy(payload["agent_role"], payload["subagent_type"]); r != nil {
		role = pyStrip(pyStr(r))
	}
	coords := text.DetectHarnessPresence(cleaned, harnessName, role)
	if coords.IsEmpty() {
		return nil
	}
	return &coords
}

func isEphemeralProbeSession(session string) bool {
	ls := strings.ToLower(strings.TrimSpace(session))
	if ls == "" || ls == "default" {
		return true
	}
	for _, p := range []string{"probe", "test", "verify", "eval", "bench", "seed", "locomo", "conv-", "dashboard-", "console-"} {
		if strings.HasPrefix(ls, p) {
			return true
		}
	}
	return false
}

// saveActiveWorkspacePresence records a hook heartbeat. The state directory is
// the canonical one from config, not the log file's directory: the log may be
// redirected while the presence ledger stays beside the rest of the runtime
// state.
func saveActiveWorkspacePresence(coords text.PresenceCoordinates, session string) {
	if coords.IsEmpty() || isEphemeralProbeSession(session) {
		return
	}
	_ = text.SavePresenceFile(config.DefaultStateDir(), coords, session, ids.UTCNow())
}

// captureUserPrompt advances the per-session turn counter used by the four-turn
// cadence reminder. The prompt itself is not stored here; transcripts remain
// the source of episodic history.
func captureUserPrompt(memory *facade.Memory, cfg *config.Config, session, prompt string, presence *text.PresenceCoordinates) int {
	cleaned := ids.StripInjected(pyStrip(prompt))
	if cleaned == "" {
		return 0
	}
	if memory == nil || memory.Store == nil || memory.Store.DB() == nil {
		return 0
	}
	if session != "" && session != "default" {
		db := memory.Store.DB()
		_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS rm_meta (k TEXT PRIMARY KEY, v BLOB NOT NULL)`)
		key := "hook_cadence_turns:" + cfg.Scope + ":" + session
		var raw []byte
		turns := 0
		if err := db.QueryRow(`SELECT v FROM rm_meta WHERE k = ?`, key).Scan(&raw); err == nil {
			if n, pErr := strconv.Atoi(strings.TrimSpace(string(raw))); pErr == nil && n > 0 {
				turns = n
			}
		}
		turns++
		_, _ = db.Exec(`INSERT OR REPLACE INTO rm_meta(k, v) VALUES (?, ?)`, key, []byte(strconv.Itoa(turns)))
	}
	return 0
}

// isCadenceReminderDue reports whether the current session has reached a 4-turn
// cadence boundary (turn 4, 8, 12, ...) so a lightweight (~40-token) attention
// anchor can be emitted on otherwise silent turns.
func isCadenceReminderDue(memory *facade.Memory, scope, session string) bool {
	if memory == nil || memory.Store == nil || memory.Store.DB() == nil || session == "" || session == "default" {
		return false
	}
	key := "hook_cadence_turns:" + scope + ":" + session
	var raw []byte
	if err := memory.Store.DB().QueryRow(`SELECT v FROM rm_meta WHERE k = ?`, key).Scan(&raw); err != nil {
		return false
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || n < 4 {
		return false
	}
	return n%4 == 0
}
