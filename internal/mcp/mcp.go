// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package mcp runs the stdio MCP server that exposes exactly two tools,
// remember and recall.
//
// The server owns stdout: every byte written there is one newline-delimited
// JSON-RPC 2.0 message. Diagnostics only reach stderr when
// ROLLING_MEMORY_MCP_DEBUG=1. The database opens lazily on the first tools/call,
// so initialize and tools/list work without touching the store.
//
// Sessions also retire themselves: the process exits when the parent harness
// is gone, when the image on disk no longer backs the running build, or when
// input stays silent past ROLLING_MEMORY_MCP_IDLE_EXIT. stdio hosts respawn a
// fresh server on the next call, so retirement is cleanup, not failure.
//
// Outbound objects are canonically encoded with sorted keys: standard preserves
// dict insertion order, but ordered nesting is not part of the wire contract and
// the receipt maps come from api as unordered Go maps. Object keys inside a
// probe text therefore sort as well.
//
// Deviations are limited to arguments that violate the published inputSchema.
// standard passes them through and fails deeper (AttributeError) or echoes the
// raw value; this adapter rejects string slots it cannot represent with a Go
// error, stringifies scope/reader (standard echoes the original), and drops
// non-object `aliases` entries, which api.Memory cannot carry (standard records
// one "invalid alias entry" skip per entry). NaN/Infinity are rejected here,
// invalid UTF-8 aborts the session, and a lone surrogate escape decodes to
// U+FFFD because Go strings cannot hold one.
package mcp

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/rocareer/rolling-memory/internal/api"
	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/text"
	"github.com/rocareer/rolling-memory/internal/version"
)

const (
	protocolVersion20241105 = "2024-11-05"
	protocolVersion20250618 = "2025-06-18"
	defaultProtocolVersion  = protocolVersion20241105
	serverName              = "rolling-memory"
	debugEnv                = "ROLLING_MEMORY_MCP_DEBUG"
	idleExitEnv             = "ROLLING_MEMORY_MCP_IDLE_EXIT"
)

// JSON-RPC error codes used by the adapter.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

var protocolVersions = []string{protocolVersion20241105, protocolVersion20250618}

// LazyMemory opens one Memory on first use and caches a construction failure,
// so every later call reports the same error. It also tracks the opened SQLite
// file's os.FileInfo (dev/ino) so if the database file on disk is atomically
// replaced while the long-lived MCP daemon is running, the next Get() transparently
// reopens the new database file instead of writing to an unlinked orphan inode.
type LazyMemory struct {
	mu      sync.Mutex
	memory  *api.Memory
	retired []*api.Memory
	err     error
	dbPath  string
	dbInfo  os.FileInfo
}

// NewLazyMemory returns an unopened lazy handle.
func NewLazyMemory() *LazyMemory {
	return &LazyMemory{}
}

// Get returns the shared Memory, opening it on first use (and reopening if the
// underlying database file on disk was replaced with a new inode).
func (l *LazyMemory) Get() (*api.Memory, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.memory != nil && l.dbPath != "" && l.dbInfo != nil {
		if curInfo, statErr := os.Stat(l.dbPath); statErr == nil && curInfo != nil && !os.SameFile(l.dbInfo, curInfo) {
			// Retire the replaced handle instead of closing it immediately under
			// concurrent in-flight requests: active recalls finish against the old
			// inode within their recall budget, and the handle is closed after a
			// grace window (or on explicit LazyMemory.Close).
			old := l.memory
			l.retired = append(l.retired, old)
			l.memory = nil
			l.dbInfo = nil
			time.AfterFunc(30*time.Second, func() {
				l.closeRetiredOne(old)
			})
		}
	}
	if l.memory == nil && l.err == nil {
		l.memory, l.err = openMemory()
		if l.memory != nil && l.memory.Config != nil && l.memory.Config.DB != "" && l.memory.Config.DB != ":memory:" {
			l.dbPath = l.memory.Config.DB
			l.dbInfo, _ = os.Stat(l.dbPath)
		}
	}
	return l.memory, l.err
}

func (l *LazyMemory) closeRetiredOne(target *api.Memory) {
	if target == nil {
		return
	}
	l.mu.Lock()
	found := false
	for i, m := range l.retired {
		if m == target {
			l.retired = append(l.retired[:i], l.retired[i+1:]...)
			found = true
			break
		}
	}
	l.mu.Unlock()
	if found {
		_ = target.Close()
	}
}

// Close releases a memory opened through Get. The stdio server keeps it open
// for the process lifetime; tests and embedders call Close explicitly.
func (l *LazyMemory) Close() error {
	l.mu.Lock()
	cur := l.memory
	l.memory = nil
	l.dbInfo = nil
	retired := l.retired
	l.retired = nil
	l.mu.Unlock()
	for _, m := range retired {
		if m != nil {
			_ = m.Close()
		}
	}
	if cur == nil {
		return nil
	}
	return cur.Close()
}

// openMemory mirrors Memory() with no arguments: configuration from the
// environment, then the store.
func openMemory() (*api.Memory, error) {
	cfg, err := config.Load(config.LoadOptions{})
	if err != nil {
		return nil, err
	}
	return api.New(cfg)
}

// Server is one stdio session. Zero fields fall back to the process streams.
type Server struct {
	In     io.Reader // defaults to os.Stdin
	Out    io.Writer // defaults to os.Stdout
	Err    io.Writer // diagnostics, defaults to os.Stderr
	Memory *api.Memory
	Lazy   *LazyMemory
	// IdleExit bounds the input silence the session tolerates before it exits
	// on its own; the host respawns the server on the next call. Zero falls
	// back to ROLLING_MEMORY_MCP_IDLE_EXIT, then defaultIdleExit; a
	// non-positive resolved budget disables idle exit. Orphan and
	// stale-image retirement always run.
	IdleExit time.Duration
	// ProxyToDaemon routes stdio `tools/call` requests through the shared local
	// Unix domain socket daemon (`~/.local/state/rolling-memory/engine.sock`)
	// when targeting the default primary vault, avoiding per-window 280MB corpus
	// snapshot duplication while falling back to in-process LazyMemory on error.
	ProxyToDaemon bool

	// Test seams; zero values fall back to the process calls.
	exitFn func(code int)
	ppidFn func() int
	statFn func() (os.FileInfo, error)
}

// Run serves lines until EOF. It returns the first transport read/write error;
// protocol failures stay inside the stream as JSON-RPC error responses.
//
// Run also arms the lifecycle watchdog, so the process leaves on its own when
// it outlives its session instead of lingering on a pipe the host abandoned.
func (s *Server) Run() error {
	in := s.In
	if in == nil {
		in = os.Stdin
	}
	out := s.Out
	if out == nil {
		out = os.Stdout
	}
	lazy := s.Lazy
	if lazy == nil {
		lazy = NewLazyMemory()
	}
	var activity, inFlight atomic.Int64
	activity.Store(time.Now().UnixNano())
	stop := make(chan struct{})
	defer close(stop)
	go s.watchdog(stop, &activity, &inFlight, lazy)
	reader := bufio.NewReader(in)
	for {
		line, err := readLineBounded(reader, maxHTTPBody)
		activity.Store(time.Now().UnixNano())
		if line != "" {
			inFlight.Add(1)
			writeErr := s.processLine(line, out, lazy)
			inFlight.Add(-1)
			if writeErr != nil {
				return writeErr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// readLineBounded reads one '\n'-terminated line without letting an
// unterminated line grow without bound; the HTTP transports cap the request body,
// so the stdio loop applies the same cap instead of buffering forever.
func readLineBounded(reader *bufio.Reader, limit int) (string, error) {
	var buffer []byte
	for {
		fragment, err := reader.ReadSlice('\n')
		buffer = append(buffer, fragment...)
		if len(buffer) > limit {
			return "", fmt.Errorf("mcp stdio: request line exceeds %d bytes", limit)
		}
		if err == nil {
			return string(buffer), nil
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err == io.EOF && len(buffer) > 0 {
			return string(buffer), io.EOF
		}
		return string(buffer), err
	}
}

// ServeWithDaemonProxy runs the stdio loop over in/out and routes `tools/call`
// requests through the shared local Unix domain socket daemon when available.
func ServeWithDaemonProxy(in io.Reader, out io.Writer) error {
	return (&Server{In: in, Out: out, ProxyToDaemon: true}).Run()
}

// Serve runs the loop over in/out; memory may be nil to open one lazily.
func Serve(in io.Reader, out io.Writer, memory *api.Memory) error {
	return (&Server{In: in, Out: out, Memory: memory}).Run()
}

// Lifecycle knobs for the stdio daemon. Hosts keep abandoned connections open,
// so sessions accumulate across reconnects; the watchdog retires them and the
// host respawns a fresh process on the next call. The tick and deadline are
// vars so tests can shrink them.
const (
	defaultIdleExit = 30 * time.Minute
	minWatchdogTick = 10 * time.Millisecond
)

var (
	watchdogTick     = 30 * time.Second
	shutdownDeadline = 2 * time.Second
)

// watchdog retires the process when it outlives its usefulness: the parent
// harness is gone (orphan), the image on disk no longer backs this process
// (a deploy replaced the build -- old semantics must not keep writing), or
// input stayed silent past the idle budget (abandoned connection; hosts keep
// the pipe open across reconnects). Orphan and stale-image retirement always
// run; idle exit honors IdleExit.
func (s *Server) watchdog(stop <-chan struct{}, activity *atomic.Int64, inFlight *atomic.Int64, lazy *LazyMemory) {
	idle := s.idleExitBudget()
	birthPPid := s.ppid()
	startImage, hasImage := s.startImage()
	tick := watchdogTick
	if idle > 0 && idle/4 < tick {
		tick = idle / 4
	}
	if tick < minWatchdogTick {
		tick = minWatchdogTick
	}
	timer := time.NewTicker(tick)
	defer timer.Stop()
	for {
		select {
		case <-stop:
			return
		case <-timer.C:
		}
		if s.ppid() != birthPPid {
			s.retire("parent harness exited", inFlight, lazy)
			return
		}
		if hasImage {
			current, err := s.imageInfo()
			if err != nil || imageReplaced(startImage, current) {
				s.retire("running image was replaced on disk", inFlight, lazy)
				return
			}
		}
		if idle > 0 && time.Since(time.Unix(0, activity.Load())) >= idle {
			s.retire("input idle for "+idle.String(), inFlight, lazy)
			return
		}
	}
}

// idleExitBudget resolves the idle budget: the IdleExit field wins, then
// ROLLING_MEMORY_MCP_IDLE_EXIT, then defaultIdleExit. A non-positive budget
// disables idle exit. Unparseable values fail open to the default.
func (s *Server) idleExitBudget() time.Duration {
	if s.IdleExit != 0 {
		return s.IdleExit
	}
	if raw := strings.TrimSpace(os.Getenv(idleExitEnv)); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil {
			return parsed
		}
	}
	return defaultIdleExit
}

// ppid reads the parent process id; tests replace ppidFn.
func (s *Server) ppid() int {
	if s.ppidFn != nil {
		return s.ppidFn()
	}
	return os.Getppid()
}

// startImage stats the executable once so later ticks can tell that the file
// on disk no longer backs this process.
func (s *Server) startImage() (os.FileInfo, bool) {
	info, err := s.imageInfo()
	if err != nil {
		return nil, false
	}
	return info, true
}

// imageInfo stats the process image; tests replace statFn.
func (s *Server) imageInfo() (os.FileInfo, error) {
	if s.statFn != nil {
		return s.statFn()
	}
	path, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return os.Stat(path)
}

// imageReplaced reports whether current no longer backs the build that started
// this process: a different file (dev/ino) or a newer mtime or size. The
// mtime/size guards also catch in-place overwrites that keep the inode.
func imageReplaced(start, current os.FileInfo) bool {
	if !os.SameFile(start, current) {
		return true
	}
	return start.Size() != current.Size() || current.ModTime().After(start.ModTime())
}

// quiescent waits briefly for an in-flight request to finish so a retirement
// never cuts a response mid-write.
func (s *Server) quiescent(inFlight *atomic.Int64) bool {
	deadline := time.Now().Add(shutdownDeadline)
	for inFlight.Load() > 0 {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
	return true
}

// retire closes the store best-effort and exits the process. stdio hosts
// respawn a fresh server on the next call, so exiting is the cleanup. The
// reason lands on stderr only under ROLLING_MEMORY_MCP_DEBUG=1, matching
// debugTrace.
func (s *Server) retire(reason string, inFlight *atomic.Int64, lazy *LazyMemory) {
	s.quiescent(inFlight)
	if os.Getenv(debugEnv) == "1" {
		fmt.Fprintf(s.debugWriter(), "mcp: exiting: %s\n", reason)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if s.Memory != nil {
			s.Memory.Close()
			return
		}
		lazy.Close()
	}()
	select {
	case <-done:
	case <-time.After(shutdownDeadline):
	}
	if s.exitFn != nil {
		s.exitFn(0)
		return
	}
	os.Exit(0)
}

// Handle processes one decoded JSON-RPC message for tests and embedders. A nil
// response means "no response" (notification). Tool exceptions become isError
// results, so the error is always nil in this port.
func Handle(request any, memory *api.Memory, lazy *LazyMemory) (any, error) {
	return handle(request, memory, lazy, os.Stderr), nil
}

func (s *Server) processLine(line string, out io.Writer, lazy *LazyMemory) error {
	stripped := strings.TrimSpace(line)
	if stripped == "" {
		return nil
	}
	if !utf8.ValidString(stripped) {
		return fmt.Errorf("mcp: input is not valid UTF-8")
	}
	request, parseMessage, err := decodeRequest(stripped)
	if err != nil {
		return writeLine(out, errorResponse(nil, codeParseError, "parse error: "+parseMessage))
	}
	if batch, ok := request.([]any); ok {
		responses := make([]any, 0, len(batch))
		for _, item := range batch {
			response := s.dispatchItem(item, lazy)
			if response != nil {
				responses = append(responses, response)
			}
		}
		if len(responses) > 0 {
			return writeLine(out, responses)
		}
		return nil
	}
	response := s.dispatchItem(request, lazy)
	if response == nil {
		return nil
	}
	return writeLine(out, response)
}

func (s *Server) dispatchItem(item any, lazy *LazyMemory) any {
	if s.ProxyToDaemon && s.Memory == nil {
		if resp, ok := s.tryProxyToolCall(item); ok {
			return resp
		}
	}
	return handle(item, s.Memory, lazy, s.debugWriter())
}

func (s *Server) tryProxyToolCall(raw any) (any, bool) {
	normalized := normalizeJSONValue(raw)
	object, ok := normalized.(map[string]any)
	if !ok {
		return nil, false
	}
	if _, hasID := object["id"]; !hasID {
		return nil, false
	}
	if method, _ := object["method"].(string); method != "tools/call" {
		return nil, false
	}
	params, ok := object["params"].(map[string]any)
	if !ok {
		return nil, false
	}
	if name, _ := params["name"].(string); name != "recall" {
		return nil, false
	}
	arguments, _ := params["arguments"].(map[string]any)
	if _, err := recallRequest(arguments); err != nil {
		return nil, false
	}
	if _, err := renderMaxChars(arguments, 16000); err != nil {
		return nil, false
	}
	presence := resolveMCPPresence(nil, arguments, true)
	return ForwardToolCallToUnixDaemon(object["id"], arguments, presence)
}

func (s *Server) debugWriter() io.Writer {
	if s.Err != nil {
		return s.Err
	}
	return os.Stderr
}

// decodeRequest parses one line the way json.loads does: one value, no trailing
// data, and numbers kept as literals so ids echo unchanged.
func decodeRequest(text string) (any, string, error) {
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, pythonParseMessage(text, err), err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, "Extra data", fmt.Errorf("extra data after the JSON value")
	}
	return value, "", nil
}

// pythonParseMessage maps Go decoder failures to json.JSONDecodeError.msg
// wording. Truncated input is classified
// heuristically; the -32700 code and the "parse error: " prefix are the
// contract, the suffix is best-effort.
func pythonParseMessage(text string, err error) string {
	return ids.PyJSONDecodeMessage(text, err)
}

// normalizeJSONValue turns json.Number literals into the values json.loads
// produces: int64 when the literal is integral and fits, float64 otherwise.
// Downstream packages expect those Go types (api/remember read confidence,
// turn and time slots through typed switches), so the adapter must not leak
// json.Number past the protocol boundary.
func normalizeJSONValue(value any) any {
	switch typed := value.(type) {
	case json.Number:
		text := typed.String()
		if !strings.ContainsAny(text, ".eE") {
			if parsed, err := strconv.ParseInt(text, 10, 64); err == nil {
				return parsed
			}
			return typed // standard has unbounded ints; keep the literal as-is.
		}
		parsed, err := typed.Float64()
		if err != nil {
			return typed
		}
		return parsed
	case []any:
		for index, item := range typed {
			typed[index] = normalizeJSONValue(item)
		}
		return typed
	case map[string]any:
		for key, item := range typed {
			typed[key] = normalizeJSONValue(item)
		}
		return typed
	}
	return value
}

// handle dispatches one decoded message; nil means "notification, no response".
func handle(request any, memory *api.Memory, lazy *LazyMemory, debugOut io.Writer) any {
	request = normalizeJSONValue(request)
	object, ok := request.(map[string]any)
	if !ok {
		return errorResponse(nil, codeInvalidRequest, "invalid request")
	}
	requestID := object["id"]
	jsonrpc, hasJSONRPC := object["jsonrpc"]
	method, isMethod := object["method"].(string)
	if hasJSONRPC && jsonrpc != nil && jsonrpc != "2.0" {
		return errorResponse(requestID, codeInvalidRequest, "invalid request")
	}
	if !isMethod {
		return errorResponse(requestID, codeInvalidRequest, "invalid request")
	}
	if _, hasID := object["id"]; !hasID {
		return nil // notification
	}
	switch method {
	case "initialize":
		params, _ := object["params"].(map[string]any)
		protocolVersion := defaultProtocolVersion
		if requested, ok := params["protocolVersion"].(string); ok {
			for _, candidate := range protocolVersions {
				if candidate == requested {
					protocolVersion = requested
				}
			}
		}
		return resultResponse(requestID, map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": serverName, "version": version.Version},
			"instructions":    instructions,
		})
	case "ping", "shutdown":
		return resultResponse(requestID, map[string]any{})
	case "tools/list":
		return resultResponse(requestID, map[string]any{"tools": tools})
	case "tools/call":
		return toolCall(requestID, object["params"], memory, lazy, debugOut)
	}
	return errorResponse(requestID, codeMethodNotFound, "method not found: "+method)
}

// toolCall runs recall and turns every failure into an isError result, never a
// crash.
func toolCall(requestID any, rawParams any, memory *api.Memory, lazy *LazyMemory, debugOut io.Writer) any {
	params, ok := rawParams.(map[string]any)
	if !ok {
		return errorResponse(requestID, codeInvalidParams, "params must be an object")
	}
	name, _ := params["name"].(string)
	arguments, _ := params["arguments"].(map[string]any)
	if name != "recall" {
		return errorResponse(requestID, codeInvalidParams, "unknown tool: "+pyStr(params["name"]))
	}
	active := memory
	liveDaemon := memory == nil
	if active == nil {
		if lazy == nil {
			lazy = NewLazyMemory()
		}
		opened, err := lazy.Get()
		if err != nil {
			return toolError(requestID, err, debugOut)
		}
		active = opened
	}
	presence := resolveMCPPresence(active, arguments, liveDaemon)
	structured, text, err := recallResponse(active, arguments, presence)
	if err != nil {
		return toolError(requestID, err, debugOut)
	}
	mode, _ := arguments["mode"].(string)
	recordRecallFact(active, presence, mode, recallOutcome(structured), text)
	isGoldenScope := active.Config != nil && strings.HasPrefix(active.Config.Scope, "mem:golden:")
	resPayload := map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
		"isError": false,
	}
	// In live MCP daemon mode with full band rendering (mode != "probe"),
	// content[0].text already carries the budget-capped <rolling-memory-recall>
	// bands x chains projection; omitting the duplicate multi-tens-of-KB
	// structuredContent JSON prevents the host from injecting the raw JSON tree
	// a second time into the model context window. Probe mode keeps the compact
	// structured receipt for hosts that consume it programmatically.
	if !liveDaemon || isGoldenScope || mode == "probe" {
		resPayload["structuredContent"] = structured
	}
	return resultResponse(requestID, resPayload)
}

func toolError(requestID any, err error, debugOut io.Writer) any {
	debugTrace(debugOut, err)
	return resultResponse(requestID, map[string]any{
		"content": []any{map[string]any{
			"type": "text",
			"text": "rolling-memory error: " + ids.Redact(err.Error()),
		}},
		"isError": true,
	})
}

// recallRequest maps the tool arguments onto api.RecallRequest. Depth and
// RawLimit carry api.recall's keyword defaults, which mcp never passes.
func recallRequest(arguments map[string]any) (api.RecallRequest, error) {
	window := map[string]any{}
	if typed, ok := arguments["window"].(map[string]any); ok {
		window = typed
	}
	caps := map[string]any{}
	if typed, ok := arguments["caps"].(map[string]any); ok {
		caps = typed
	}
	offset := 0
	if pyTruthy(arguments["offset"]) {
		parsed, err := pyInt(arguments["offset"])
		if err != nil {
			return api.RecallRequest{}, err
		}
		offset = parsed
	}
	rawLimit := 10
	if value := intOrNil(caps["raw"]); value != nil && *value != 0 {
		rawLimit = *value
	}
	if rawLimit < 1 || rawLimit > 64 {
		return api.RecallRequest{}, fmt.Errorf("caps.raw must be between 1 and 64")
	}
	if bands := intOrNil(caps["bands"]); bands != nil && (*bands < 0 || *bands > 64) {
		return api.RecallRequest{}, fmt.Errorf("caps.bands must be between 0 and 64")
	}
	var sessionPtr *string
	if value, ok := arguments["session"]; ok && value != nil {
		session := pyStr(value)
		sessionPtr = &session
	}
	mode := "full"
	if pyTruthy(arguments["mode"]) {
		mode = pyStr(arguments["mode"])
	}
	cue := ""
	if pyTruthy(arguments["cue"]) {
		cue = pyStr(arguments["cue"])
	}
	scope := optionalText(arguments["scope"])
	reader := optionalText(arguments["reader"])
	return api.RecallRequest{
		Cue:         cue,
		Scope:       scope,
		Reader:      reader,
		Mode:        mode,
		AsOf:        arguments["asof"],
		WindowStart: window["start"],
		WindowEnd:   window["end"],
		CapsBands:   intOrNil(caps["bands"]),
		CapsNodes:   intOrNil(caps["nodes"]),
		CapsChars:   intOrNil(caps["chars"]),
		CapsChains:  intOrNil(caps["chains"]),
		CapsRows:    intOrNil(caps["rows"]),
		ResultUnit:  strings.ToLower(textOrEmpty(arguments["result_unit"])),
		Rank:        boolOrNil(arguments["rank"]),
		Offset:      offset,
		Depth:       2,
		RawLimit:    rawLimit,
		Session:     sessionPtr,
		Deep:        pyTruthy(arguments["deep"]),
	}, nil
}

// recallResponse assembles the probe/full payload and its text rendering.
func recallResponse(memory *api.Memory, arguments map[string]any, presence *text.PresenceCoordinates) (any, string, error) {
	request, err := recallRequest(arguments)
	if err != nil {
		return nil, "", err
	}
	request.Presence = presence
	result, err := memory.Recall(request)
	if err != nil {
		return nil, "", err
	}
	mode, _ := arguments["mode"].(string)
	if mode == "probe" {
		structured := result.ToProbe()
		text, err := ids.PyJSONDumpIndent(structured, false, true, 2)
		if err != nil {
			return nil, "", err
		}
		return structured, text, nil
	}
	maxChars, err := renderMaxChars(arguments, memory.Config.Recall.Chars)
	if err != nil {
		return nil, "", err
	}
	text := result.Render(maxChars)
	return result.ToDict(), text, nil
}

// renderMaxChars reads the render budget from the raw caps value:
// a truthy non-object caps becomes isError.
func renderMaxChars(arguments map[string]any, fallback int) (int, error) {
	caps := map[string]any{}
	if value := arguments["caps"]; pyTruthy(value) {
		typed, ok := value.(map[string]any)
		if !ok {
			return 0, fmt.Errorf("'%s' object has no attribute 'get'", pyTypeName(value))
		}
		caps = typed
	}
	chars := caps["chars"]
	if !pyTruthy(chars) {
		return fallback, nil
	}
	return pyInt(chars)
}

func valueOr(values map[string]any, key string, fallback any) any {
	if value, present := values[key]; present {
		return value
	}
	return fallback
}

// optionalString maps one argument onto a *string slot. Non-string values are
// rejected; standard would fail deeper with AttributeError/TypeError.
func optionalString(value any) (*string, error) {
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case string:
		if typed == "" {
			return nil, nil
		}
		return &typed, nil
	}
	return nil, fmt.Errorf("expected a string, got %s", pyTypeName(value))
}

// optionalText maps a slot standard stringifies at first use (anchor_text goes
// through str(), scope/reader flow into text comparisons): falsy values fall
// through to the caller's default, everything else becomes pyStr.
func optionalText(value any) *string {
	if !pyTruthy(value) {
		return nil
	}
	text := pyStr(value)
	return &text
}

func intOrNil(value any) *int {
	if value == nil {
		return nil
	}
	parsed, err := pyInt(value)
	if err != nil {
		return nil
	}
	return &parsed
}

func resultResponse(requestID any, result any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": requestID, "result": result}
}

func errorResponse(requestID any, code int, message string) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      requestID,
		"error":   map[string]any{"code": code, "message": message},
	}
}

// writeLine appends the newline and flushes. Keys are sorted so each JSON-RPC
// line is byte-stable across runs.
func writeLine(out io.Writer, payload any) error {
	text, err := ids.PyJSONDump(payload, false, true, false)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(out, text+"\n"); err != nil {
		return err
	}
	if flusher, ok := out.(interface{ Flush() error }); ok {
		return flusher.Flush()
	}
	return nil
}

// debugTrace writes a redacted stack trace to stderr, and only when the debug
// switch is on.
func debugTrace(out io.Writer, err error) {
	if out == nil || os.Getenv(debugEnv) != "1" {
		return
	}
	fmt.Fprint(out, ids.Redact(err.Error()+"\n"+string(debug.Stack())))
}

// resolveMCPPresence extracts active workspace PresenceCoordinates for MCP tool calls.
// Explicit arguments ("cwd", "locus", "incarnation") or ROLLING_MEMORY_WORKSPACE always take priority;
// in live stdio daemon mode (liveDaemon=true), it also checks the hook-written active_workspace.json
// or the daemon's own git repository cwd.
func resolveMCPPresence(active *api.Memory, arguments map[string]any, liveDaemon bool) *text.PresenceCoordinates {
	var explicit text.PresenceCoordinates
	if arguments != nil {
		if rawCwd := arguments["cwd"]; pyTruthy(rawCwd) {
			coords := text.DetectHarnessPresence(pyStr(rawCwd), "", "")
			if !coords.IsEmpty() {
				explicit = coords
			}
		}
		if rawLocus := arguments["locus"]; pyTruthy(rawLocus) {
			explicit.Locus = pyStr(rawLocus)
		}
		if rawInc := arguments["incarnation"]; pyTruthy(rawInc) {
			explicit.Incarnation = pyStr(rawInc)
		}
		if rawCarrier := arguments["carrier"]; pyTruthy(rawCarrier) {
			explicit.Carrier = pyStr(rawCarrier)
		}
	}
	mergeExplicit := func(base text.PresenceCoordinates) *text.PresenceCoordinates {
		if explicit.World != "" {
			base.World = explicit.World
		}
		if explicit.Carrier != "" {
			base.Carrier = explicit.Carrier
		}
		if explicit.Locus != "" {
			base.Locus = explicit.Locus
		}
		if explicit.Incarnation != "" {
			base.Incarnation = explicit.Incarnation
		}
		if base.World == "" && !base.IsEmpty() {
			base.World = "actual"
		}
		norm := base.Normalized()
		if norm.IsEmpty() {
			return nil
		}
		return &norm
	}
	if ws := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_WORKSPACE")); ws != "" {
		coords := text.DetectHarnessPresence(ws, "", "")
		if merged := mergeExplicit(coords); merged != nil {
			return merged
		}
	}
	if !liveDaemon {
		return mergeExplicit(text.PresenceCoordinates{})
	}
	if active != nil && active.Config != nil && strings.HasPrefix(active.Config.Scope, "mem:golden:") {
		return mergeExplicit(text.PresenceCoordinates{})
	}
	stateDir := ""
	if active != nil && active.Config != nil && active.Config.Log != "" {
		stateDir = filepath.Dir(active.Config.Log)
	} else {
		stateDir = config.DefaultStateDir()
	}
	readSavedPresence := func() text.PresenceCoordinates {
		if stateDir == "" {
			return text.PresenceCoordinates{}
		}
		raw, err := os.ReadFile(filepath.Join(stateDir, "active_workspace.json"))
		if err != nil {
			return text.PresenceCoordinates{}
		}
		var saved map[string]any
		if json.Unmarshal(raw, &saved) != nil {
			return text.PresenceCoordinates{}
		}
		return text.PresenceCoordinates{
			World:       pyStr(valueOr(saved, "world", "")),
			Carrier:     pyStr(valueOr(saved, "carrier", "")),
			Locus:       pyStr(valueOr(saved, "locus", "")),
			Incarnation: pyStr(valueOr(saved, "incarnation", "")),
			SelfLabel:   pyStr(valueOr(saved, "self_label", "")),
			PeerLabel:   pyStr(valueOr(saved, "peer_label", "")),
		}.Normalized()
	}
	// 1. L1 Per-Process Isolation: the MCP daemon's own working directory (when inside a git repository)
	//    takes priority over the shared active_workspace.json for Locus, while inheriting the active host
	//    harness identity when the child process environment does not carry host-specific variables.
	if cwd, err := os.Getwd(); err == nil && cwd != "" {
		if text.DetectGitWorkspaceName(cwd) != "" {
			harnessHint := ""
			if resolveZCodeInFlightToolDirectory(ids.UTCNow()) != "" {
				harnessHint = "zcode"
			} else if saved := readSavedPresence(); strings.HasPrefix(saved.Carrier, "harness:") {
				if h := strings.TrimPrefix(saved.Carrier, "harness:"); h != "" && h != "cli" {
					harnessHint = h
				}
			}
			coords := text.DetectHarnessPresence(cwd, harnessHint, "")
			if inferred := calibrateLocusFromArguments(arguments, coords.Locus, true); inferred != "" {
				coords.Locus = inferred
			}
			if merged := mergeExplicit(coords); merged != nil {
				return merged
			}
		}
	}
	// 1b. L1.5 In-Flight Tool Call Session Directory Lookup:
	//     When an MCP daemon was spawned with cwd in /tmp or $HOME (outside a git repo), query
	//     ZCode's native `tool_usage JOIN session` for the currently running tool call's session
	//     directory before falling back to the globally shared `active_workspace.json`.
	if inFlightDir := resolveZCodeInFlightToolDirectory(ids.UTCNow()); inFlightDir != "" {
		if text.DetectGitWorkspaceName(inFlightDir) != "" {
			coords := text.DetectHarnessPresence(inFlightDir, "zcode", "")
			if inferred := calibrateLocusFromArguments(arguments, coords.Locus, true); inferred != "" {
				coords.Locus = inferred
			}
			if merged := mergeExplicit(coords); merged != nil {
				return merged
			}
		}
	}
	// 2. L2 Hook State Fallback: read hook-written active_workspace.json when daemon cwd is outside a git repo (e.g. $HOME),
	//    and self-calibrate Locus against explicit repository coordinates in the tool payload so concurrent multi-window
	//    sessions never cross-contaminate each other's repository locus.
	if coords := readSavedPresence(); !coords.IsEmpty() {
		if inferred := calibrateLocusFromArguments(arguments, coords.Locus, false); inferred != "" {
			coords.Locus = inferred
		}
		if merged := mergeExplicit(coords); merged != nil {
			return merged
		}
	}
	return mergeExplicit(text.PresenceCoordinates{})
}

func isRepoIdentByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '-' || b == '_'
}

// indexRepoWordBoundary finds the first occurrence of needle in haystack where neither the
// preceding nor the following byte is an identifier character ([a-zA-Z0-9-_]), preventing
// a short repository stem from matching as a prefix of a longer hyphenated repository name.
func indexRepoWordBoundary(haystack, needle string) int {
	if needle == "" {
		return -1
	}
	offset := 0
	for offset <= len(haystack)-len(needle) {
		idx := strings.Index(haystack[offset:], needle)
		if idx < 0 {
			return -1
		}
		absIdx := offset + idx
		prevOK := absIdx == 0 || !isRepoIdentByte(haystack[absIdx-1])
		endIdx := absIdx + len(needle)
		nextOK := endIdx == len(haystack) || !isRepoIdentByte(haystack[endIdx])
		if prevOK && nextOK {
			return absIdx
		}
		offset = absIdx + 1
	}
	return -1
}

// calibrateLocusFromArguments checks if the tool arguments explicitly reference a
// known local git workspace under $HOME whose name differs from the fallback locus
// (preventing multi-window locus cross-contamination).
func calibrateLocusFromArguments(arguments map[string]any, fallbackLocus string, hasDedicatedRepoCwd bool) string {
	if arguments == nil {
		return fallbackLocus
	}
	// 1. Explicit locus inside assertion / assertions[0] always wins.
	if aMap, ok := arguments["assertion"].(map[string]any); ok {
		if rawLoc := pyStr(aMap["locus"]); strings.Contains(rawLoc, "/") {
			return text.NormalizeHierarchicalPath(rawLoc)
		}
	}
	if aList, ok := arguments["assertions"].([]any); ok && len(aList) > 0 {
		if aMap, ok := aList[0].(map[string]any); ok {
			if rawLoc := pyStr(aMap["locus"]); strings.Contains(rawLoc, "/") {
				return text.NormalizeHierarchicalPath(rawLoc)
			}
		}
	}
	// If the MCP daemon is already running inside a dedicated git repository cwd,
	// keep that repository's locus unless an explicit assertion locus overrode it above.
	if hasDedicatedRepoCwd && fallbackLocus != "" {
		return fallbackLocus
	}
	rawBytes, err := json.Marshal(arguments)
	if err != nil {
		return fallbackLocus
	}
	payloadStr := strings.ToLower(string(rawBytes))
	if fallbackLocus != "" {
		parts := strings.Split(fallbackLocus, "/")
		repoName := strings.ToLower(parts[len(parts)-1])
		if repoName != "" && indexRepoWordBoundary(payloadStr, repoName) >= 0 {
			return fallbackLocus
		}
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return fallbackLocus
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		return fallbackLocus
	}
	bestRepo := ""
	bestLen := 0
	bestIdx := len(payloadStr) + 1
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		nameLower := strings.ToLower(entry.Name())
		if len(nameLower) < 4 {
			continue
		}
		idx := indexRepoWordBoundary(payloadStr, nameLower)
		if idx >= 0 {
			candidateDir := filepath.Join(home, entry.Name())
			if locus := text.DetectGitWorkspaceName(candidateDir); locus != "" {
				if len(nameLower) > bestLen || (len(nameLower) == bestLen && idx < bestIdx) {
					bestLen = len(nameLower)
					bestIdx = idx
					bestRepo = locus
				}
			}
		}
	}
	if bestRepo != "" {
		return bestRepo
	}
	return fallbackLocus
}

// resolveZCodeInFlightToolDirectory recovers the directory of the session that
// is currently running (or just ran) a Rolling-Memory tool call, so an MCP
// daemon spawned outside a git repository can still calibrate its workspace
// locus instead of falling back to the globally shared active_workspace.json.
func resolveZCodeInFlightToolDirectory(nowSec int64) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	zdbPath := filepath.Join(home, ".zcode", "cli", "db", "db.sqlite")
	if _, sErr := os.Stat(zdbPath); sErr != nil {
		return ""
	}
	db, oErr := sql.Open("sqlite", zdbPath+"?mode=ro&_pragma=busy_timeout(600)")
	if oErr != nil {
		return ""
	}
	defer db.Close()
	runningCutoffMS := (nowSec - 180) * 1000
	recentCutoffMS := (nowSec - 30) * 1000
	var dir string
	_ = db.QueryRow(`
		SELECT coalesce(s.directory, '')
		FROM tool_usage t
		JOIN session s ON s.id = t.session_id
		WHERE (t.tool_name LIKE '%rolling-memory%' OR t.tool_name LIKE '%recall%')
		  AND ((t.status = 'running' AND t.started_at >= ?) OR t.started_at >= ?)
		  AND coalesce(s.directory, '') != ''
		ORDER BY (t.status = 'running') DESC, t.started_at DESC
		LIMIT 1
	`, runningCutoffMS, recentCutoffMS).Scan(&dir)
	return strings.TrimSpace(dir)
}
