// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package mcp runs the stdio MCP server that exposes the single recall tool.
//
// The server owns stdout: every byte written there is one newline-delimited
// JSON-RPC 2.0 message. Diagnostics only reach stderr when
// ROLLING_MEMORY_MCP_DEBUG=1. The database opens lazily on the first tools/call,
// so initialize and tools/list work without touching the store.
//
// A session stays attached to the host: the process exits only when the parent
// harness is gone, or when an in-process server's on-disk image no longer backs
// the running build. Input silence past ROLLING_MEMORY_MCP_IDLE_RELEASE closes
// the vault handle and lets the next tool call reopen it, because hosts keep a
// quiet connection open and do not necessarily respawn a process that exits.
// A daemon-proxy sidecar holds no vault, so neither silence nor a replaced
// build retires it; the shared daemon owns upgrade retirement.
//
// Outbound objects are canonically encoded with sorted keys: standard preserves
// dict insertion order, but ordered nesting is not part of the wire contract and
// the receipt maps come from the memory facade as unordered Go maps. Object keys
// inside a probe text therefore sort as well.
//
// Deviations are limited to arguments that violate the published inputSchema.
// standard passes them through and fails deeper (AttributeError) or echoes the
// raw value; this adapter rejects string slots it cannot represent with a Go
// error, stringifies scope/reader (standard echoes the original), and drops
// non-object `aliases` entries, which facade.Memory cannot carry (standard records
// one "invalid alias entry" skip per entry). NaN/Infinity are rejected here,
// invalid UTF-8 aborts the session, and a lone surrogate escape decodes to
// U+FFFD because Go strings cannot hold one.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/harness"
	"github.com/rocareer/rolling-memory/internal/hostid"
	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/memory/facade"
	"github.com/rocareer/rolling-memory/internal/text"
	"github.com/rocareer/rolling-memory/internal/updatecheck"
	"github.com/rocareer/rolling-memory/internal/version"
)

const (
	protocolVersion20241105 = "2024-11-05"
	protocolVersion20250618 = "2025-06-18"
	defaultProtocolVersion  = protocolVersion20241105
	serverName              = "rolling-memory"
	debugEnv                = "ROLLING_MEMORY_MCP_DEBUG"
	idleReleaseEnv          = "ROLLING_MEMORY_MCP_IDLE_RELEASE"
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
	memory  *facade.Memory
	retired []*facade.Memory
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
func (l *LazyMemory) Get() (*facade.Memory, error) {
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
		l.memory, l.err = openRecallMemory()
		if l.memory != nil && l.memory.Config != nil && l.memory.Config.DB != "" && l.memory.Config.DB != ":memory:" {
			l.dbPath = l.memory.Config.DB
			l.dbInfo, _ = os.Stat(l.dbPath)
		}
	}
	return l.memory, l.err
}

func (l *LazyMemory) closeRetiredOne(target *facade.Memory) {
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

// Opened reports whether a vault handle is currently open. The watchdog uses it
// to tell a stateless daemon-proxy sidecar from a session that has touched the
// vault itself.
func (l *LazyMemory) Opened() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.memory != nil
}

// openMemory mirrors Memory() with no arguments: configuration from the
// environment, then the store.
func openMemory() (*facade.Memory, error) {
	cfg, err := config.Load(config.LoadOptions{})
	if err != nil {
		return nil, err
	}
	return facade.New(cfg)
}

// openRecallMemory opens the MCP surface memory in read-only snapshot mode: a
// missing or stale disk cluster degrades to a warming recall instead of a
// minute-scale build inside the tool call. The background builder publishes the
// cluster in its own process.
func openRecallMemory() (*facade.Memory, error) {
	memory, err := openMemory()
	if err != nil || memory == nil {
		return memory, err
	}
	memory.SetSnapshotOnly(true)
	return memory, nil
}

// Server is one stdio session. Zero fields fall back to the process streams.
type Server struct {
	In     io.Reader // defaults to os.Stdin
	Out    io.Writer // defaults to os.Stdout
	Err    io.Writer // diagnostics, defaults to os.Stderr
	Memory *facade.Memory
	Lazy   *LazyMemory
	// IdleRelease bounds the input silence a session tolerates before it closes
	// the vault handle it opened; the stdio loop stays alive and the next call
	// reopens the handle lazily. Zero falls back to
	// ROLLING_MEMORY_MCP_IDLE_RELEASE, then defaultIdleRelease; a non-positive
	// resolved budget disables the release. Orphan and stale-image retirement
	// still run, except in a stateless daemon-proxy sidecar.
	IdleRelease time.Duration
	// ProxyToDaemon routes stdio `tools/call` requests through the shared local
	// Unix domain socket daemon (`~/.local/state/rolling-memory/engine.sock`)
	// when targeting the default primary vault, avoiding per-window 280MB corpus
	// snapshot duplication while falling back to in-process LazyMemory on error.
	ProxyToDaemon bool
	// AttachmentDir, when non-empty, records this sidecar as the live client
	// attachment for the session. The real CLI entry point passes the
	// canonical state directory; unit tests leave it empty so they never
	// rewrite the developer's presence ledger.
	AttachmentDir string

	attachment *hostid.Attachment

	// Test seams; zero values fall back to the process calls.
	exitFn func(code int)
	ppidFn func() int
	statFn func() (os.FileInfo, error)
}

// Run serves lines until EOF. It returns the first transport read/write error;
// protocol failures stay inside the stream as JSON-RPC error responses.
//
// Run also arms the lifecycle watchdog, so an orphaned process leaves instead
// of lingering on a pipe no host owns.
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
	s.recordClientAttachment()
	go s.attachmentHeartbeat(stop)
	go s.watchdog(stop, &activity, &inFlight, lazy)
	reader := bufio.NewReader(in)
	for {
		line, err := readLineBounded(reader, maxHTTPBody)
		activity.Store(time.Now().UnixNano())
		s.touchClientAttachment()
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

// recordClientAttachment writes this sidecar's session into the shared ledger.
// The sidecar is the only process that knows both the host identity and the
// session's working directory; the daemon it forwards to must never guess
// either from its own environment or cwd.
func (s *Server) recordClientAttachment() {
	if s.AttachmentDir == "" || os.Getenv("ROLLING_MEMORY_DISABLE_ATTACHMENT") == "1" {
		return
	}
	identity := hostid.Detect()
	cwd, _ := os.Getwd()
	attachment := hostid.Attachment{
		Host:       string(identity.ID),
		Label:      identity.Label,
		SessionID:  identity.SessionID,
		Role:       "main",
		PID:        os.Getpid(),
		CWD:        cwd,
		Locus:      text.DetectGitWorkspaceName(cwd),
		Evidence:   identity.Evidence,
		Mode:       hostid.ModeProcess,
		LastSeenAt: ids.UTCNow(),
	}
	if attachment.Host == "" || attachment.Host == string(hostid.Unknown) {
		attachment.Host = string(hostid.Unknown)
		attachment.Label = hostid.Label(hostid.Unknown)
	}
	if attachment.Label == "" {
		attachment.Label = hostid.Label(hostid.Normalize(attachment.Host))
	}
	if err := hostid.RecordAt(s.AttachmentDir, attachment, attachment.LastSeenAt); err != nil {
		return
	}
	s.attachment = &attachment
}

func (s *Server) touchClientAttachment() {
	if s.AttachmentDir == "" || s.attachment == nil {
		return
	}
	_ = hostid.Touch(s.AttachmentDir, hostid.Key(*s.attachment), ids.UTCNow())
}

func (s *Server) attachmentHeartbeat(stop <-chan struct{}) {
	if s.AttachmentDir == "" {
		return
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			s.touchClientAttachment()
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

// startUpdateRefresh refreshes the update-state cache in the background when
// the cached snapshot is stale, so a user who only works through an agent still
// sees the new version on the next console visit or `status` call. Test
// binaries are skipped inside MaybeRefresh.
func startUpdateRefresh() {
	go updatecheck.MaybeRefresh(context.Background(), updatecheck.Options{
		ReleaseBase:    updatecheck.ReleaseBaseFromEnv(),
		CurrentVersion: version.Version,
		Timeout:        10 * time.Second,
	})
}

// ServeWithDaemonProxy runs the stdio loop over in/out and routes `tools/call`
// requests through the shared local Unix domain socket daemon when available.
func ServeWithDaemonProxy(in io.Reader, out io.Writer) error {
	return ServeWithDaemonProxyAt(in, out, "")
}

// ServeWithDaemonProxyAt additionally records the sidecar's host identity in
// attachmentDir. The CLI passes the canonical state directory; tests pass an
// empty directory and keep their presence state untouched.
func ServeWithDaemonProxyAt(in io.Reader, out io.Writer, attachmentDir string) error {
	startUpdateRefresh()
	return (&Server{In: in, Out: out, ProxyToDaemon: true, AttachmentDir: attachmentDir}).Run()
}

// Serve runs the loop over in/out; memory may be nil to open one lazily.
func Serve(in io.Reader, out io.Writer, memory *facade.Memory) error {
	return ServeAt(in, out, memory, "")
}

// ServeAt is Serve with an explicit attachment directory.
func ServeAt(in io.Reader, out io.Writer, memory *facade.Memory, attachmentDir string) error {
	startUpdateRefresh()
	return (&Server{In: in, Out: out, Memory: memory, AttachmentDir: attachmentDir}).Run()
}

// Lifecycle knobs for the stdio daemon.
const (
	defaultIdleRelease = 30 * time.Minute
	minWatchdogTick    = 10 * time.Millisecond
)

// watchdogTickNanos holds the watchdog cadence in nanoseconds. It is atomic because
// a test shortens it while a server started by an earlier test may still be reading
// it: a plain global makes those two accesses a data race even when the goroutine
// only passes through once before winding down.
var watchdogTickNanos = func() *atomic.Int64 {
	tick := new(atomic.Int64)
	tick.Store(int64(30 * time.Second))
	return tick
}()

var (
	shutdownDeadline = 2 * time.Second
)

// watchdog keeps a live host connection alive. The parent harness going away
// (orphan) still retires the process, and an in-process server still retires
// when the image on disk no longer backs it (a deploy replaced the build --
// old semantics must not keep writing). Input silence past the idle budget
// releases the vault handle instead of exiting, because hosts keep a quiet
// connection open and may report a clean process exit as a connection error
// without respawning. A daemon-proxy sidecar is a stateless forwarder: it
// holds no vault, and the shared daemon owns upgrade retirement, so neither
// trigger applies.
func (s *Server) watchdog(stop <-chan struct{}, activity *atomic.Int64, inFlight *atomic.Int64, lazy *LazyMemory) {
	idle := s.idleReleaseBudget()
	birthPPid := s.ppid()
	startImage, hasImage := s.startImage()
	tick := time.Duration(watchdogTickNanos.Load())
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
		stateless := s.ProxyToDaemon && s.Memory == nil && !lazy.Opened()
		if hasImage && !stateless {
			current, err := s.imageInfo()
			if err != nil || imageReplaced(startImage, current) {
				s.retire("running image was replaced on disk", inFlight, lazy)
				return
			}
		}
		if idle > 0 && !stateless && time.Since(time.Unix(0, activity.Load())) >= idle {
			if s.releaseIdle(inFlight, lazy) {
				activity.Store(time.Now().UnixNano())
			}
		}
	}
}

// releaseIdle closes the vault handle this session opened and leaves the stdio
// loop serving; the next tool call reopens it lazily. Caller-owned memory is
// left alone because the embedder owns its lifecycle. It reports false when an
// in-flight request kept the handle open, so the next tick retries.
func (s *Server) releaseIdle(inFlight *atomic.Int64, lazy *LazyMemory) bool {
	if s.Memory != nil || lazy == nil || !lazy.Opened() {
		return true
	}
	if !s.quiescent(inFlight) {
		return false
	}
	_ = lazy.Close()
	if os.Getenv(debugEnv) == "1" {
		fmt.Fprintf(s.debugWriter(), "mcp: released idle vault handle\n")
	}
	return true
}

// idleReleaseBudget resolves the idle budget: the IdleRelease field wins, then
// ROLLING_MEMORY_MCP_IDLE_RELEASE, then defaultIdleRelease. A non-positive
// budget disables the release. Unparseable values fail open to the default.
func (s *Server) idleReleaseBudget() time.Duration {
	if s.IdleRelease != 0 {
		return s.IdleRelease
	}
	if raw := strings.TrimSpace(os.Getenv(idleReleaseEnv)); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil {
			return parsed
		}
	}
	return defaultIdleRelease
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

// retire closes the store best-effort and exits the process. It backs the two
// lifecycle events a session cannot survive in place: the parent harness is
// gone, or an in-process server's build was replaced on disk. The reason lands
// on stderr only under ROLLING_MEMORY_MCP_DEBUG=1, matching debugTrace.
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
func Handle(request any, memory *facade.Memory, lazy *LazyMemory) (any, error) {
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
// Downstream packages expect those Go types (facade/remember read confidence,
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
func handle(request any, memory *facade.Memory, lazy *LazyMemory, debugOut io.Writer) any {
	return handleWithMemoryMode(request, memory, lazy, debugOut, memory == nil, true)
}

func handleWithMemoryMode(request any, memory *facade.Memory, lazy *LazyMemory, debugOut io.Writer, liveDaemon bool, clientProcess bool) any {
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
		return toolCall(requestID, object["params"], memory, lazy, debugOut, liveDaemon, clientProcess)
	}
	return errorResponse(requestID, codeMethodNotFound, "method not found: "+method)
}

// toolCall runs recall and turns every failure into an isError result, never a
// crash.
func toolCall(requestID any, rawParams any, memory *facade.Memory, lazy *LazyMemory, debugOut io.Writer, liveDaemon bool, clientProcess bool) any {
	params, ok := rawParams.(map[string]any)
	if !ok {
		return errorResponse(requestID, codeInvalidParams, "params must be an object")
	}
	name, _ := params["name"].(string)
	arguments, _ := params["arguments"].(map[string]any)
	if name != "recall" {
		return errorResponse(requestID, codeInvalidParams, "unknown tool: "+pyStr(params["name"]))
	}
	request, err := recallRequest(arguments)
	if err != nil {
		return toolError(requestID, err, debugOut)
	}
	active := memory
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
	presence := resolveMCPPresence(active, arguments, clientProcess)
	recallStarted := time.Now()
	request.Presence = presence
	structured, text, err := recallResponse(active, request)
	elapsedMS := time.Since(recallStarted).Milliseconds()
	if err != nil {
		return toolError(requestID, err, debugOut)
	}
	mode, _ := arguments["mode"].(string)
	recordRecallFact(active, presence, mode, recallOutcome(structured), text, elapsedMS)
	isGoldenScope := active.Config != nil && strings.HasPrefix(active.Config.Scope, "mem:golden:")
	resPayload := map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
		"isError": false,
	}
	// In live MCP daemon mode with full band rendering (mode != "probe"),
	// content[0].text already carries the budget-capped <rolling-memory-recall>
	// band projection; omitting the duplicate multi-tens-of-KB
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

const capsRetiredMessage = "caps is not supported; omit caps and send the cue"

// recallRequest maps the tool arguments onto facade.RecallRequest.
func recallRequest(arguments map[string]any) (facade.RecallRequest, error) {
	if _, present := arguments["caps"]; present {
		return facade.RecallRequest{}, errors.New(capsRetiredMessage)
	}
	offset := 0
	if pyTruthy(arguments["offset"]) {
		parsed, err := pyInt(arguments["offset"])
		if err != nil {
			return facade.RecallRequest{}, err
		}
		offset = parsed
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
	deliveryWindow := strings.TrimSpace(textOrEmpty(arguments["delivery_window"]))
	if deliveryWindow != "" {
		if _, _, ok := config.NormalizeDeliveryWindow(deliveryWindow, "0-16"); !ok {
			return facade.RecallRequest{}, fmt.Errorf("delivery_window must be one of ('auto', '0-16', '0-32', '0-64')")
		}
	} else {
		deliveryWindow = "auto"
	}
	return facade.RecallRequest{
		Cue:            cue,
		Scope:          scope,
		Reader:         reader,
		Mode:           mode,
		DeliveryWindow: deliveryWindow,
		Offset:         offset,
	}, nil
}

// recallResponse assembles the probe/full payload and its text rendering.
func recallResponse(memory *facade.Memory, request facade.RecallRequest) (any, string, error) {
	result, err := memory.Recall(request)
	if err != nil {
		return nil, "", err
	}
	if request.Mode == "probe" {
		structured := result.ToProbe()
		text, err := ids.PyJSONDumpIndent(structured, false, true, 2)
		if err != nil {
			return nil, "", err
		}
		return structured, text, nil
	}
	maxChars := 0
	if memory.Config != nil {
		maxChars = memory.Config.Recall.Chars
	}
	text := result.Render(maxChars)
	return result.ToDict(), text, nil
}

// optionalText maps a slot the standard client may pass as a non-string value
// (scope/reader go through str()): falsy values fall through to the caller's
// default, everything else becomes pyStr.
func optionalText(value any) *string {
	if !pyTruthy(value) {
		return nil
	}
	text := pyStr(value)
	return &text
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

// resolveMCPPresence builds the presence coordinates for one recall call.
//
// The client process is authoritative. When clientProcess is true the harness
// identity comes from the live process (environment plus ancestry) and the
// workspace from that process's cwd. The shared daemon must never re-derive
// identity from its own environment or cwd; it only honors explicit
// coordinates forwarded by the sidecar. Unknown identity leaves Carrier and
// Incarnation empty so no carrier or incarnation resonance is granted.
func resolveMCPPresence(active *facade.Memory, arguments map[string]any, clientProcess bool) *text.PresenceCoordinates {
	var explicit text.PresenceCoordinates
	if arguments != nil {
		if rawCwd := arguments["cwd"]; pyTruthy(rawCwd) {
			explicit.Locus = text.DetectGitWorkspaceName(pyStr(rawCwd))
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
	if !clientProcess {
		return mergeExplicit(text.PresenceCoordinates{})
	}
	if active != nil && active.Config != nil && strings.HasPrefix(active.Config.Scope, "mem:golden:") {
		return mergeExplicit(text.PresenceCoordinates{})
	}
	identity := hostid.Detect()
	harnessID := ""
	if identity.ID != hostid.Unknown && identity.ID != hostid.CLI {
		harnessID = string(identity.ID)
	}
	if ws := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_WORKSPACE")); ws != "" {
		if merged := mergeExplicit(text.DetectHarnessPresence(ws, harnessID, "")); merged != nil {
			return merged
		}
	}
	cwd, _ := os.Getwd()
	coords := text.DetectHarnessPresence(cwd, harnessID, "")
	// A host whose sidecar is spawned from $HOME may expose the session
	// directory through its own in-flight record. The adapter decides whether it
	// has an answer, so a call from one host can never be stamped as another.
	if inFlight := harness.InFlightWorkspace(string(identity.ID), identity.SessionID, ids.UTCNow()); inFlight != "" {
		if text.DetectGitWorkspaceName(inFlight) != "" {
			coords = text.DetectHarnessPresence(inFlight, harnessID, "")
		}
	}
	return mergeExplicit(coords)
}
