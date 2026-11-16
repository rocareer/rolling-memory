// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rocareer/rolling-memory/internal/api"
	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/text"
	"github.com/rocareer/rolling-memory/internal/thinclient"
)

const (
	// DaemonSocketEnv overrides the Unix domain socket path for the local memory daemon.
	DaemonSocketEnv = "ROLLING_MEMORY_DAEMON_SOCKET"
	// DaemonEnableEnv toggles automatic local daemon forwarding ("0"/"false"/"off" disables).
	DaemonEnableEnv = "ROLLING_MEMORY_DAEMON"

	// maxUnixSocketPathLen stays safely below the 104-byte BSD/macOS sun_path limit.
	maxUnixSocketPathLen = 96

	// daemonPingTimeout bounds the fast health probe on the local Unix domain socket.
	daemonPingTimeout = 150 * time.Millisecond
	// daemonStartupWait bounds how long a caller waits for a newly spawned daemon socket to bind.
	daemonStartupWait = 2500 * time.Millisecond
	// daemonIdleBudget retires the shared local daemon after extended inactivity.
	daemonIdleBudget = 2 * time.Hour
	// daemonWatchdogInterval checks whether the on-disk binary was replaced or the daemon went idle.
	daemonWatchdogInterval = 5 * time.Second
)

// DefaultUnixSocketPath returns the canonical Unix domain socket path for the
// shared local memory daemon (`~/.local/state/rolling-memory/engine.sock`).
func DefaultUnixSocketPath() string {
	if override := strings.TrimSpace(os.Getenv(DaemonSocketEnv)); override != "" {
		return normalizeUnixSocketPath(override)
	}
	return normalizeUnixSocketPath(filepath.Join(config.DefaultStateDir(), "engine.sock"))
}

func normalizeUnixSocketPath(cand string) string {
	cand = strings.TrimSpace(cand)
	if cand == "" || len(cand) <= maxUnixSocketPathLen {
		return cand
	}
	// Over-long paths fall back to a stable owner-only directory under the system
	// temp dir: the name is derived from the original path so every process agrees,
	// and ServeUnix creates the directory 0700 so another local user can neither
	// pre-create the socket nor impersonate the daemon. os.TempDir (not a literal
	// "/tmp") keeps the path absolute on Windows and honors TMPDIR; when even that
	// is too long for sun_path, the short /tmp form keeps the daemon functional.
	sum := sha256.Sum256([]byte(cand))
	base := filepath.Join(os.TempDir(), "rm-"+hex.EncodeToString(sum[:6]))
	if len(base)+len("/engine.sock") > maxUnixSocketPathLen {
		base = filepath.Join("/tmp", "rm-"+hex.EncodeToString(sum[:6]))
	}
	return filepath.Join(base, "engine.sock")
}

// ShouldUseUnixDaemon reports whether a caller targeting dbPath should route
// through the shared local Unix domain socket daemon.
//
// Invariants:
//  1. Explicit `ROLLING_MEMORY_DAEMON=0|false|off` always disables daemon routing.
//  2. Explicit `ROLLING_MEMORY_DAEMON_SOCKET` always enables daemon routing (for tests/custom setups).
//  3. Inside `go test` binaries (`*.test`) without an explicit socket override, daemon routing is
//     disabled so unit and golden tests stay 100% isolated in-process.
//  4. Only requests targeting the default primary vault (`config.DefaultDBPath()`) route to the
//     shared daemon; custom `--db` paths (such as evaluation stores) run in-process.
func ShouldUseUnixDaemon(dbPath string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(DaemonEnableEnv))) {
	case "0", "false", "off", "no":
		return false
	}
	if strings.TrimSpace(os.Getenv(DaemonSocketEnv)) != "" {
		return true
	}
	exe := filepath.Base(os.Args[0])
	if strings.HasSuffix(exe, ".test") || strings.Contains(exe, ".test.") {
		return false
	}
	target := strings.TrimSpace(dbPath)
	defaultDB := config.DefaultDBPath()
	if target == "" {
		target = strings.TrimSpace(os.Getenv("ROLLING_MEMORY_DB"))
	}
	if target == "" {
		return true
	}
	return sameFileOrCleanPath(target, defaultDB)
}

func sameFileOrCleanPath(a, b string) bool {
	if a == "" || b == "" {
		return a == b
	}
	cleanA := filepath.Clean(a)
	cleanB := filepath.Clean(b)
	if cleanA == cleanB {
		return true
	}
	infoA, errA := os.Stat(cleanA)
	infoB, errB := os.Stat(cleanB)
	if errA == nil && errB == nil && os.SameFile(infoA, infoB) {
		return true
	}
	return false
}

// UnixHealthResponse is returned by GET /v1/health on the local Unix socket.
type UnixHealthResponse struct {
	OK           bool   `json:"ok"`
	PID          int    `json:"pid"`
	DB           string `json:"db"`
	ExeModTimeNS int64  `json:"exe_mtime_ns"`
}

// UnixRecallPayload carries a CLI or Admin Console recall request over the local Unix socket.
type UnixRecallPayload struct {
	DB              string                    `json:"db,omitempty"`
	Cue             string                    `json:"cue"`
	Scope           *string                   `json:"scope,omitempty"`
	Reader          *string                   `json:"reader,omitempty"`
	Mode            string                    `json:"mode,omitempty"`
	AsOf            any                       `json:"asof,omitempty"`
	WindowStart     any                       `json:"window_start,omitempty"`
	WindowEnd       any                       `json:"window_end,omitempty"`
	CapsBands       *int                      `json:"caps_bands,omitempty"`
	CapsNodes       *int                      `json:"caps_nodes,omitempty"`
	CapsChars       *int                      `json:"caps_chars,omitempty"`
	CapsChains      *int                      `json:"caps_chains,omitempty"`
	CapsRows        *int                      `json:"caps_rows,omitempty"`
	ResultUnit      string                    `json:"result_unit,omitempty"`
	Rank            *bool                     `json:"rank,omitempty"`
	Offset          int                       `json:"offset,omitempty"`
	Depth           int                       `json:"depth,omitempty"`
	IncludeRaw      *bool                     `json:"include_raw,omitempty"`
	BudgetMS        *int                      `json:"budget_ms,omitempty"`
	RawLimit        int                       `json:"raw_limit,omitempty"`
	Session         *string                   `json:"session,omitempty"`
	Deep            bool                      `json:"deep,omitempty"`
	TimeRef         any                       `json:"time_ref,omitempty"`
	TokenCorrect    *bool                     `json:"token_correct,omitempty"`
	AliasExpand     *bool                     `json:"alias_expand,omitempty"`
	RelativeTag     *bool                     `json:"relative_tag,omitempty"`
	MentionBackfill *bool                     `json:"mention_backfill,omitempty"`
	Presence        *text.PresenceCoordinates `json:"presence,omitempty"`
	NoCrystallize   bool                      `json:"no_crystallize,omitempty"`
	RenderMaxChars  int                       `json:"render_max_chars,omitempty"`
	NeedRendered    bool                      `json:"need_rendered,omitempty"`
}

// UnixRecallResponse is returned by POST /v1/recall on the local Unix socket.
type UnixRecallResponse struct {
	OK          bool           `json:"ok"`
	EngineError bool           `json:"engine_error,omitempty"`
	Error       string         `json:"error,omitempty"`
	Result      map[string]any `json:"result,omitempty"`
	Probe       map[string]any `json:"probe,omitempty"`
	Rendered    string         `json:"rendered,omitempty"`
}

// UnixHydrateItem names one zero-copy `@ref:` pointer to resolve into speech text.
// DocID is optional: when present the daemon additionally verifies the resolved
// content against the sealed episodic index before answering.
type UnixHydrateItem struct {
	DocID string `json:"doc_id,omitempty"`
	Ref   string `json:"ref"`
}

// UnixHydratePayload carries one display-hydration batch over the local Unix socket.
// Callers pass the pointers exactly as stored in `thin_vault_docs.raw_text`; the daemon
// answers with the canonical turn segmentation (the same resolver the product surfaces
// `recall` / `recent` use), so display layers cannot drift from ingest segmentation.
type UnixHydratePayload struct {
	DB    string            `json:"db,omitempty"`
	Items []UnixHydrateItem `json:"items"`
}

// UnixHydrateResult is the canonical text of one requested pointer. Verified is true
// when the sealed episodic index confirmed the resolved content digest.
type UnixHydrateResult struct {
	DocID    string `json:"doc_id,omitempty"`
	Ref      string `json:"ref"`
	Text     string `json:"text,omitempty"`
	Verified bool   `json:"verified,omitempty"`
	Error    string `json:"error,omitempty"`
}

// UnixHydrateResponse is returned by POST /v1/hydrate on the local Unix socket.
type UnixHydrateResponse struct {
	OK    bool                `json:"ok"`
	Error string              `json:"error,omitempty"`
	Texts []UnixHydrateResult `json:"texts,omitempty"`
}

// maxUnixHydrateItems caps one hydration batch so a hostile or buggy caller cannot
// pin the daemon on an unbounded resolution loop.
const maxUnixHydrateItems = 256

// UnixDocumentItem names one local document (unit id) whose plaintext body a display
// layer needs. L1 background chunks (`bg:*`) are read LIVE from their source file on disk.
type UnixDocumentItem struct {
	UnitID string `json:"unit_id"`
}

// UnixDocumentPayload carries one local-document read batch over the local Unix socket.
type UnixDocumentPayload struct {
	DB    string             `json:"db,omitempty"`
	Items []UnixDocumentItem `json:"items"`
}

// UnixDocumentResult is the resolved plaintext of one requested local document.
type UnixDocumentResult struct {
	UnitID        string `json:"unit_id"`
	RawText       string `json:"raw_text,omitempty"`
	DistilledText string `json:"distilled_text,omitempty"`
	Error         string `json:"error,omitempty"`
}

// UnixDocumentResponse is returned by POST /v1/documents on the local Unix socket.
type UnixDocumentResponse struct {
	OK    bool                 `json:"ok"`
	Error string               `json:"error,omitempty"`
	Docs  []UnixDocumentResult `json:"docs,omitempty"`
}

// maxUnixDocumentItems caps one document-read batch so a hostile or buggy caller cannot
// pin the daemon on an unbounded file-read loop (each L1 chunk re-reads and re-chunks its
// source file, so the batch cap stays far below the pointer-hydration cap).
const maxUnixDocumentItems = 64

// EngineRecallError wraps an application-level error returned by `Memory.Recall`
// inside the daemon so the caller reports it directly instead of re-running locally.
type EngineRecallError struct {
	Msg string
}

func (e *EngineRecallError) Error() string { return e.Msg }

func currentExeModTimeNS() int64 {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return 0
	}
	info, err := os.Stat(exe)
	if err != nil || info == nil {
		return 0
	}
	return info.ModTime().UnixNano()
}

func newUnixHTTPClient(sockPath string, timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, "unix", sockPath)
			},
			DisableKeepAlives: false,
			MaxIdleConns:      4,
			IdleConnTimeout:   30 * time.Second,
		},
	}
}

// PingUnixDaemon probes GET /v1/health on sockPath.
func PingUnixDaemon(sockPath string, timeout time.Duration) (*UnixHealthResponse, error) {
	if sockPath == "" {
		sockPath = DefaultUnixSocketPath()
	} else {
		sockPath = normalizeUnixSocketPath(sockPath)
	}
	if _, err := os.Stat(sockPath); err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = daemonPingTimeout
	}
	client := newUnixHTTPClient(sockPath, timeout)
	resp, err := client.Get("http://unix/v1/health")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var out UnixHealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if !out.OK {
		return nil, errors.New("daemon reported not ok")
	}
	return &out, nil
}

func requestUnixDaemonShutdown(sockPath string) {
	client := newUnixHTTPClient(sockPath, 500*time.Millisecond)
	resp, err := client.Post("http://unix/v1/shutdown", "application/json", strings.NewReader(`{}`))
	if err == nil && resp != nil {
		_ = resp.Body.Close()
	}
}

// ShutdownUnixDaemon asks the local Unix domain socket daemon (if running) to
// step down cleanly so a freshly updated binary takes over on the next request.
func ShutdownUnixDaemon(sockPath string) {
	if sockPath == "" {
		sockPath = DefaultUnixSocketPath()
	} else {
		sockPath = normalizeUnixSocketPath(sockPath)
	}
	requestUnixDaemonShutdown(sockPath)
}

// InvalidateUnixDaemonCaches notifies the running local Unix domain socket daemon
// (if any) to immediately drop its cached in-memory corpus, Noetic, and slot snapshots
// after an out-of-process write (such as CLI background sync, prune, or reconcile).
func InvalidateUnixDaemonCaches(sockPath string) {
	if sockPath == "" {
		sockPath = DefaultUnixSocketPath()
	} else {
		sockPath = normalizeUnixSocketPath(sockPath)
	}
	if _, err := os.Stat(sockPath); err != nil {
		return
	}
	client := newUnixHTTPClient(sockPath, 300*time.Millisecond)
	resp, err := client.Post("http://unix/v1/invalidate", "application/json", strings.NewReader(`{}`))
	if err == nil && resp != nil {
		_ = resp.Body.Close()
	}
}

// EnsureUnixDaemon verifies that a live up-to-date daemon is listening on sockPath,
// spawning one in the background if needed and waiting briefly for the socket to bind.
func EnsureUnixDaemon(sockPath string) error {
	if sockPath == "" {
		sockPath = DefaultUnixSocketPath()
	} else {
		sockPath = normalizeUnixSocketPath(sockPath)
	}
	myMTime := currentExeModTimeNS()
	if health, err := PingUnixDaemon(sockPath, daemonPingTimeout); err == nil && health != nil {
		// Only ask the running daemon to step down when the caller binary is strictly
		// newer by >2s (e.g. a freshly compiled binary invoked before the daemon's 5s
		// self-watchdog tick). Sibling install copies (`~/.local/bin` vs
		// `~/.local/share/rolling-memory/bin`) copied in the same second differ by a few
		// milliseconds and must never evict each other.
		if myMTime == 0 || health.ExeModTimeNS == 0 || myMTime <= health.ExeModTimeNS+int64(2*time.Second) {
			return nil
		}
		requestUnixDaemonShutdown(sockPath)
		time.Sleep(80 * time.Millisecond)
	}
	if err := spawnUnixDaemonProcess(sockPath); err != nil {
		return err
	}
	deadline := time.Now().Add(daemonStartupWait)
	for time.Now().Before(deadline) {
		if _, err := PingUnixDaemon(sockPath, 100*time.Millisecond); err == nil {
			return nil
		}
		time.Sleep(30 * time.Millisecond)
	}
	return fmt.Errorf("local memory daemon did not bind %s within %s", sockPath, daemonStartupWait)
}

func spawnUnixDaemonProcess(sockPath string) error {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return fmt.Errorf("resolve executable: %w", err)
	}
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer devNull.Close()

	cmd := exec.Command(exe, "mcp", "--daemon")
	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = devNull
	cmd.Env = append(os.Environ(), DaemonSocketEnv+"="+sockPath)
	cmd.SysProcAttr = daemonSysProcAttr()
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release()
	return nil
}

// CallUnixDaemonRecall executes a recall request against the shared local Unix socket
// daemon, auto-starting the daemon if it is not yet running.
func CallUnixDaemonRecall(ctx context.Context, sockPath string, payload UnixRecallPayload) (*UnixRecallResponse, error) {
	if sockPath == "" {
		sockPath = DefaultUnixSocketPath()
	} else {
		sockPath = normalizeUnixSocketPath(sockPath)
	}
	if err := EnsureUnixDaemon(sockPath); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	timeout := 65 * time.Second
	if payload.BudgetMS != nil && *payload.BudgetMS > 0 {
		timeout = time.Duration(*payload.BudgetMS)*time.Millisecond + 30*time.Second
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/v1/recall", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := newUnixHTTPClient(sockPath, timeout)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPBody))
	if err != nil {
		return nil, err
	}
	var out UnixRecallResponse
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&out); err != nil {
		return nil, fmt.Errorf("decode daemon response (status %d): %w", resp.StatusCode, err)
	}
	if out.Result != nil {
		if norm, ok := normalizeJSONValue(out.Result).(map[string]any); ok {
			out.Result = norm
		}
	}
	if out.Probe != nil {
		if norm, ok := normalizeJSONValue(out.Probe).(map[string]any); ok {
			out.Probe = norm
		}
	}
	if resp.StatusCode == http.StatusConflict {
		return nil, fmt.Errorf("daemon db mismatch: %s", out.Error)
	}
	if !out.OK {
		if out.EngineError && out.Error != "" {
			return nil, &EngineRecallError{Msg: out.Error}
		}
		if out.Error != "" {
			return nil, errors.New(out.Error)
		}
		return nil, fmt.Errorf("daemon recall failed with status %d", resp.StatusCode)
	}
	return &out, nil
}

// ForwardToolCallToUnixDaemon forwards an MCP `tools/call` request from a lightweight
// stdio session to the shared local Unix domain socket daemon, injecting the caller
// window's resolved PresenceCoordinates so per-window repository isolation is preserved.
func ForwardToolCallToUnixDaemon(requestID any, arguments map[string]any, presence *text.PresenceCoordinates) (any, bool) {
	sockPath := DefaultUnixSocketPath()
	if err := EnsureUnixDaemon(sockPath); err != nil {
		return nil, false
	}
	forwardedArgs := make(map[string]any, len(arguments)+3)
	for k, v := range arguments {
		forwardedArgs[k] = v
	}
	if presence != nil {
		if presence.Locus != "" && !pyTruthy(forwardedArgs["locus"]) {
			forwardedArgs["locus"] = presence.Locus
		}
		if presence.Incarnation != "" && !pyTruthy(forwardedArgs["incarnation"]) {
			forwardedArgs["incarnation"] = presence.Incarnation
		}
		if presence.Carrier != "" && !pyTruthy(forwardedArgs["carrier"]) {
			forwardedArgs["carrier"] = presence.Carrier
		}
	}
	rpcReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      requestID,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "recall",
			"arguments": forwardedArgs,
		},
	}
	raw, err := json.Marshal(rpcReq)
	if err != nil {
		return nil, false
	}
	client := newUnixHTTPClient(sockPath, 65*time.Second)
	resp, err := client.Post("http://unix/mcp", "application/json", bytes.NewReader(raw))
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	var rpcResp any
	decoder := json.NewDecoder(io.LimitReader(resp.Body, maxHTTPBody))
	decoder.UseNumber()
	if err := decoder.Decode(&rpcResp); err != nil {
		return nil, false
	}
	return normalizeJSONValue(rpcResp), true
}

// ServeUnix runs the shared single-instance local memory daemon on sockPath.
// It hosts both `/v1/recall` (for CLI and Admin Console) and `/mcp` (for stdio MCP proxies),
// warms the in-memory corpus snapshot in the background on startup, and runs `onStart`
// (such as the single-instance auto-watcher) until shutdown.
func ServeUnix(sockPath string, memory *api.Memory, onStart func(context.Context)) error {
	if sockPath == "" {
		sockPath = DefaultUnixSocketPath()
	} else {
		sockPath = normalizeUnixSocketPath(sockPath)
	}
	myMTime := currentExeModTimeNS()
	if health, err := PingUnixDaemon(sockPath, daemonPingTimeout); err == nil && health != nil {
		if myMTime == 0 || health.ExeModTimeNS == 0 || myMTime <= health.ExeModTimeNS+int64(2*time.Second) {
			// Another up-to-date daemon is already serving this socket.
			return nil
		}
		requestUnixDaemonShutdown(sockPath)
		time.Sleep(80 * time.Millisecond)
	}
	if err := os.MkdirAll(filepath.Dir(sockPath), 0o700); err != nil {
		return fmt.Errorf("mcp unix: mkdir %s: %w", filepath.Dir(sockPath), err)
	}
	_ = os.Remove(sockPath)
	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		return fmt.Errorf("mcp unix: listen %s: %w", sockPath, err)
	}
	_ = os.Chmod(sockPath, 0o600)
	sockInfo, _ := os.Stat(sockPath)
	defer func() {
		_ = listener.Close()
		if cur, err := os.Stat(sockPath); err == nil && sockInfo != nil && os.SameFile(sockInfo, cur) {
			_ = os.Remove(sockPath)
		}
	}()

	lazy := NewLazyMemory()
	if memory != nil {
		lazy.memory = memory
		if memory.Config != nil {
			lazy.dbPath = memory.Config.DB
			lazy.dbInfo, _ = os.Stat(lazy.dbPath)
		}
	}

	var activity, inFlight atomic.Int64
	activity.Store(time.Now().UnixNano())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Warm the 522MB SQLite corpus snapshot in the background immediately after binding
	// the socket so health probes succeed right away and user queries hit warm RAM.
	go func() {
		if mem, err := lazy.Get(); err == nil && mem != nil {
			_ = mem.Warmup(ctx)
		}
	}()
	if onStart != nil {
		go onStart(ctx)
	}

	httpSrv := &HTTPServer{Memory: memory, Lazy: lazy}
	mcpHandler := httpSrv.Handler()

	var srv *http.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", func(w http.ResponseWriter, r *http.Request) {
		dbPath := config.DefaultDBPath()
		if memory != nil && memory.Config != nil && memory.Config.DB != "" {
			dbPath = memory.Config.DB
		}
		writeJSON(w, http.StatusOK, UnixHealthResponse{
			OK:           true,
			PID:          os.Getpid(),
			DB:           dbPath,
			ExeModTimeNS: myMTime,
		})
	})
	mux.HandleFunc("/v1/shutdown", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Connection", "close")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		go func() {
			time.Sleep(15 * time.Millisecond)
			if cur, err := os.Stat(sockPath); err == nil && sockInfo != nil && os.SameFile(sockInfo, cur) {
				_ = os.Remove(sockPath)
			}
			_ = listener.Close()
			deadline := time.Now().Add(2 * time.Second)
			for inFlight.Load() > 0 && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			cancel()
			if srv != nil {
				_ = srv.Close()
			}
		}()
	})
	mux.HandleFunc("/v1/recall", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		activity.Store(time.Now().UnixNano())
		inFlight.Add(1)
		defer inFlight.Add(-1)
		handleUnixRecall(w, r, memory, lazy)
	})
	mux.HandleFunc("/v1/hydrate", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		activity.Store(time.Now().UnixNano())
		inFlight.Add(1)
		defer inFlight.Add(-1)
		handleUnixHydrate(w, r, memory, lazy)
	})
	mux.HandleFunc("/v1/documents", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		activity.Store(time.Now().UnixNano())
		inFlight.Add(1)
		defer inFlight.Add(-1)
		handleUnixDocuments(w, r, memory, lazy)
	})
	mux.HandleFunc("/v1/invalidate", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		activity.Store(time.Now().UnixNano())
		active := memory
		if active == nil && lazy != nil {
			active, _ = lazy.Get()
		}
		if active != nil {
			if tc, err := active.ThinClient(); err == nil && tc != nil && tc.Vault != nil {
				tc.Vault.InvalidateRuntimeCaches()
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		activity.Store(time.Now().UnixNano())
		inFlight.Add(1)
		defer inFlight.Add(-1)
		r.Host = "127.0.0.1"
		r.Header.Del("Origin")
		mcpHandler.ServeHTTP(w, r)
	})

	srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: httpReadHeaderTimeout,
	}

	// Watchdog: retire when the on-disk binary is replaced or after extended idle time.
	startExe, _ := os.Executable()
	var startExeInfo os.FileInfo
	if startExe != "" {
		startExeInfo, _ = os.Stat(startExe)
	}
	go func() {
		ticker := time.NewTicker(daemonWatchdogInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			if startExeInfo != nil && startExe != "" {
				if cur, err := os.Stat(startExe); err != nil || imageReplaced(startExeInfo, cur) {
					if inFlight.Load() == 0 {
						cancel()
						_ = listener.Close()
						return
					}
				}
			}
			if time.Since(time.Unix(0, activity.Load())) >= daemonIdleBudget && inFlight.Load() == 0 {
				cancel()
				_ = listener.Close()
				return
			}
		}
	}()

	err = srv.Serve(listener)
	_ = srv.Close()
	_ = lazy.Close()
	if errors.Is(err, net.ErrClosed) || errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func handleUnixRecall(w http.ResponseWriter, r *http.Request, memory *api.Memory, lazy *LazyMemory) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxHTTPBody))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, UnixRecallResponse{OK: false, Error: err.Error()})
		return
	}
	var payload UnixRecallPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, UnixRecallResponse{OK: false, Error: err.Error()})
		return
	}
	active := memory
	if active == nil {
		opened, oErr := lazy.Get()
		if oErr != nil {
			writeJSON(w, http.StatusInternalServerError, UnixRecallResponse{OK: false, Error: oErr.Error()})
			return
		}
		active = opened
	}
	if payload.DB != "" && active.Config != nil && active.Config.DB != "" && !sameFileOrCleanPath(payload.DB, active.Config.DB) {
		writeJSON(w, http.StatusConflict, UnixRecallResponse{
			OK:    false,
			Error: fmt.Sprintf("daemon bound to %s, request asked for %s", active.Config.DB, payload.DB),
		})
		return
	}
	var presence *text.PresenceCoordinates
	if payload.Presence != nil {
		norm := payload.Presence.Normalized()
		if !norm.IsEmpty() {
			presence = &norm
		}
	}
	req := api.RecallRequest{
		Cue:             payload.Cue,
		Scope:           payload.Scope,
		Reader:          payload.Reader,
		Mode:            payload.Mode,
		AsOf:            payload.AsOf,
		WindowStart:     payload.WindowStart,
		WindowEnd:       payload.WindowEnd,
		CapsBands:       payload.CapsBands,
		CapsNodes:       payload.CapsNodes,
		CapsChars:       payload.CapsChars,
		CapsChains:      payload.CapsChains,
		CapsRows:        payload.CapsRows,
		ResultUnit:      payload.ResultUnit,
		Rank:            payload.Rank,
		Offset:          payload.Offset,
		Depth:           payload.Depth,
		IncludeRaw:      payload.IncludeRaw,
		BudgetMS:        payload.BudgetMS,
		RawLimit:        payload.RawLimit,
		Session:         payload.Session,
		Deep:            payload.Deep,
		TimeRef:         payload.TimeRef,
		TokenCorrect:    payload.TokenCorrect,
		AliasExpand:     payload.AliasExpand,
		RelativeTag:     payload.RelativeTag,
		MentionBackfill: payload.MentionBackfill,
		Presence:        presence,
		NoCrystallize:   payload.NoCrystallize,
	}
	result, err := active.Recall(req)
	if err != nil {
		writeJSON(w, http.StatusOK, UnixRecallResponse{OK: false, EngineError: true, Error: err.Error()})
		return
	}
	api.WaitForPendingNoeticPulses(api.NoeticPulseBudget)
	out := UnixRecallResponse{OK: true}
	if strings.EqualFold(strings.TrimSpace(payload.Mode), "probe") {
		out.Probe = result.ToProbe()
	} else {
		out.Result = result.ToDict()
	}
	if payload.NeedRendered {
		maxChars := 0
		if active.Config != nil {
			maxChars = active.Config.Recall.Chars
		}
		if payload.RenderMaxChars > 0 {
			maxChars = payload.RenderMaxChars
		} else if payload.CapsChars != nil && *payload.CapsChars > 0 {
			maxChars = *payload.CapsChars
		}
		out.Rendered = result.Render(maxChars)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleUnixHydrate resolves display pointers to their canonical speech text: the same
// `ResolveEpisodicPointerLive` resolver every product surface uses (turn segmentation,
// conversation-noise stripping, segment/chunk addressing), with the sealed episodic
// index as an identity check whenever the caller supplies a document id. Display
// layers (Admin Console panels, layer browsers) therefore never re-implement
// segmentation and can never drift from ingest.
func handleUnixHydrate(w http.ResponseWriter, r *http.Request, memory *api.Memory, lazy *LazyMemory) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxHTTPBody))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, UnixHydrateResponse{OK: false, Error: err.Error()})
		return
	}
	var payload UnixHydratePayload
	if err := json.Unmarshal(body, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, UnixHydrateResponse{OK: false, Error: err.Error()})
		return
	}
	active := memory
	if active == nil {
		opened, oErr := lazy.Get()
		if oErr != nil {
			writeJSON(w, http.StatusInternalServerError, UnixHydrateResponse{OK: false, Error: oErr.Error()})
			return
		}
		active = opened
	}
	if payload.DB != "" && active.Config != nil && active.Config.DB != "" && !sameFileOrCleanPath(payload.DB, active.Config.DB) {
		writeJSON(w, http.StatusConflict, UnixHydrateResponse{
			OK:    false,
			Error: fmt.Sprintf("daemon bound to %s, request asked for %s", active.Config.DB, payload.DB),
		})
		return
	}
	items := payload.Items
	if len(items) > maxUnixHydrateItems {
		items = items[:maxUnixHydrateItems]
	}
	var vault *thinclient.Vault
	if tc, tcErr := active.ThinClient(); tcErr == nil && tc != nil {
		vault = tc.Vault
	}
	out := UnixHydrateResponse{OK: true, Texts: make([]UnixHydrateResult, 0, len(items))}
	for _, item := range items {
		ref := strings.TrimSpace(item.Ref)
		res := UnixHydrateResult{DocID: item.DocID, Ref: ref}
		if ref == "" {
			res.Error = "empty ref"
			out.Texts = append(out.Texts, res)
			continue
		}
		if vault != nil && strings.TrimSpace(item.DocID) != "" {
			if text, vErr := vault.LoadVerifiedEpisodicPointer(item.DocID, ref); vErr == nil && strings.TrimSpace(text) != "" {
				res.Text, res.Verified = text, true
				out.Texts = append(out.Texts, res)
				continue
			}
		}
		text, rErr := thinclient.ResolveEpisodicPointerLive(ref)
		if rErr != nil {
			res.Error = rErr.Error()
		} else if strings.TrimSpace(text) == "" {
			res.Error = "empty content"
		} else {
			res.Text = text
		}
		out.Texts = append(out.Texts, res)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleUnixDocuments resolves local document bodies by unit id through the canonical
// `LoadLocalDocument` reader every product surface uses: L1 background chunks are read
// LIVE from their source file on disk and re-distilled with the stored kept sentence
// indices, so a display layer never re-implements file chunking and can never drift from
// ingest. Display layers (Admin Console lists and detail dialogs) call this instead of
// ever showing path coordinates as if they were the chunk text.
func handleUnixDocuments(w http.ResponseWriter, r *http.Request, memory *api.Memory, lazy *LazyMemory) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxHTTPBody))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, UnixDocumentResponse{OK: false, Error: err.Error()})
		return
	}
	var payload UnixDocumentPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, UnixDocumentResponse{OK: false, Error: err.Error()})
		return
	}
	active := memory
	if active == nil {
		opened, oErr := lazy.Get()
		if oErr != nil {
			writeJSON(w, http.StatusInternalServerError, UnixDocumentResponse{OK: false, Error: oErr.Error()})
			return
		}
		active = opened
	}
	if payload.DB != "" && active.Config != nil && active.Config.DB != "" && !sameFileOrCleanPath(payload.DB, active.Config.DB) {
		writeJSON(w, http.StatusConflict, UnixDocumentResponse{
			OK:    false,
			Error: fmt.Sprintf("daemon bound to %s, request asked for %s", active.Config.DB, payload.DB),
		})
		return
	}
	items := payload.Items
	if len(items) > maxUnixDocumentItems {
		items = items[:maxUnixDocumentItems]
	}
	var vault *thinclient.Vault
	if tc, tcErr := active.ThinClient(); tcErr == nil && tc != nil {
		vault = tc.Vault
	}
	if vault == nil {
		writeJSON(w, http.StatusServiceUnavailable, UnixDocumentResponse{OK: false, Error: "local vault unavailable"})
		return
	}
	out := UnixDocumentResponse{OK: true, Docs: make([]UnixDocumentResult, 0, len(items))}
	for _, item := range items {
		unitID := strings.TrimSpace(item.UnitID)
		res := UnixDocumentResult{UnitID: unitID}
		if unitID == "" {
			res.Error = "empty unit_id"
			out.Docs = append(out.Docs, res)
			continue
		}
		raw, distilled, _, dErr := vault.LoadLocalDocument(unitID)
		if dErr != nil {
			// Source file moved/deleted or identity not verified: the caller shows the
			// source location it already has plus an honest unavailable notice.
			res.Error = dErr.Error()
			out.Docs = append(out.Docs, res)
			continue
		}
		if strings.TrimSpace(raw) == "" {
			res.Error = "empty content"
			out.Docs = append(out.Docs, res)
			continue
		}
		res.RawText, res.DistilledText = raw, distilled
		out.Docs = append(out.Docs, res)
	}
	writeJSON(w, http.StatusOK, out)
}
