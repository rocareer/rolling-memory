// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package mcp

import (
	"bufio"
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
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/hostid"
	"github.com/rocareer/rolling-memory/internal/memory/facade"
	"github.com/rocareer/rolling-memory/internal/memory/localvault"
	"github.com/rocareer/rolling-memory/internal/text"
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
	// daemonIdentityVersion identifies the verified MCP forwarding protocol.
	daemonIdentityVersion = 1
)

// errUnixDaemonExecutableMismatch marks a same-owner socket served by a
// different binary. It is recoverable through the explicit daemon restart path,
// which authenticates the stale process by socket ownership and vault identity
// before asking it to step down.
var errUnixDaemonExecutableMismatch = errors.New("daemon executable mismatch")

// errUnixDaemonIdentityMismatch marks a same-owner daemon that is healthy but
// was started under a different configuration identity, so a request can only
// proceed after the daemon is retired and restarted under the current config.
var errUnixDaemonIdentityMismatch = errors.New("daemon mode or configuration identity mismatch")

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
//  2. Platforms without peer verification never route through the daemon.
//  3. Explicit `ROLLING_MEMORY_DAEMON_SOCKET` enables daemon routing on supported platforms.
//  4. Inside `go test` binaries (`*.test`) without an explicit socket override, daemon routing is
//     disabled so unit and golden tests stay 100% isolated in-process.
//  5. Only requests targeting the default primary vault (`config.DefaultDBPath()`) route to the
//     shared daemon; custom `--db` paths (such as evaluation stores) run in-process.
func ShouldUseUnixDaemon(dbPath string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(DaemonEnableEnv))) {
	case "0", "false", "off", "no":
		return false
	}
	if !unixDaemonPeerVerificationAvailable() {
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
	OK              bool   `json:"ok"`
	PID             int    `json:"pid"`
	DB              string `json:"db"`
	ExeModTimeNS    int64  `json:"exe_mtime_ns"`
	IdentityVersion int    `json:"identity_version"`
	Mode            string `json:"mode"`
	PulseMode       string `json:"pulse_mode"`
	ConfigID        string `json:"config_id"`
	// Runtime footprint and memory-limit reporting for the system console.
	// MemLimitBytes is 0 when no explicit soft limit is in force;
	// MemLimitSource is one of auto / explicit / env / off / unavailable.
	StartedAtUnix  int64  `json:"started_at"`
	UptimeSec      int64  `json:"uptime_sec"`
	HeapAllocBytes uint64 `json:"heap_alloc_bytes"`
	HeapSysBytes   uint64 `json:"heap_sys_bytes"`
	TotalRAMBytes  int64  `json:"total_ram_bytes"`
	MemLimitBytes  int64  `json:"mem_limit_bytes"`
	MemLimitSource string `json:"mem_limit_source"`
	MemLimitConfig string `json:"mem_limit_config"`
	// Attachments exposes the live client sessions for diagnostics. The
	// daemon is shared by every host on the machine, so this is the only
	// place where its attachments can be observed without reading state.
	Attachments []hostid.Attachment `json:"attachments,omitempty"`
}

// DaemonConfigID exposes the daemon configuration identity so diagnostics can
// compare a caller's resolved configuration with the running daemon's.
func DaemonConfigID(cfg *config.Config) (string, error) {
	return unixDaemonConfigID(cfg)
}

func unixDaemonConfigID(cfg *config.Config) (string, error) {
	if cfg == nil || cfg.Mode != "local" {
		return "", errors.New("daemon requires local mode and local_plain")
	}
	resolved := *cfg
	if resolved.Path == "" || resolved.DB == "" || resolved.Log == "" {
		return "", errors.New("daemon configuration identity is incomplete")
	}
	// The daemon memory cap is a process resource knob, not a retrieval-semantics
	// input: exclude it from the identity so that saving a new cap can still
	// reach the running daemon and restart it instead of failing the handshake.
	resolved.Daemon = config.DaemonConfig{}
	// Release-channel/source preferences and evaluation answerer settings are
	// console- and eval-only: the daemon neither fetches releases nor answers
	// eval questions, so persisting them must not invalidate a live daemon.
	resolved.Eval = config.EvalConfig{}
	resolved.Update = config.UpdateConfig{}
	var err error
	if resolved.Path, err = filepath.Abs(resolved.Path); err != nil {
		return "", err
	}
	if resolved.DB, err = filepath.Abs(resolved.DB); err != nil {
		return "", err
	}
	if resolved.Log, err = filepath.Abs(resolved.Log); err != nil {
		return "", err
	}
	raw, err := json.Marshal(resolved)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func unixDaemonHealth(cfg *config.Config, exeMTime int64) (UnixHealthResponse, error) {
	id, err := unixDaemonConfigID(cfg)
	if err != nil {
		return UnixHealthResponse{}, err
	}
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	limit := currentDaemonMemoryState()
	startedAt := limit.StartedAt
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	attachments := hostid.LiveAttachments(hostid.LoadLedger(config.DefaultStateDir()), time.Now().Unix())
	return UnixHealthResponse{
		OK: true, PID: os.Getpid(), DB: cfg.DB, ExeModTimeNS: exeMTime,
		IdentityVersion: daemonIdentityVersion, Mode: cfg.Mode, PulseMode: "local_plain", ConfigID: id,
		StartedAtUnix:  startedAt.Unix(),
		UptimeSec:      int64(time.Since(startedAt).Seconds()),
		HeapAllocBytes: stats.HeapAlloc,
		HeapSysBytes:   stats.Sys,
		TotalRAMBytes:  limit.TotalRAM,
		MemLimitBytes:  limit.LimitBytes,
		MemLimitSource: limit.LimitSource,
		MemLimitConfig: limit.ConfigValue,
		Attachments:    attachments,
	}, nil
}

// UnixRecallPayload carries a CLI or Admin Console recall request over the local Unix socket.
type UnixRecallPayload struct {
	DB             string                    `json:"db,omitempty"`
	Cue            string                    `json:"cue"`
	Scope          *string                   `json:"scope,omitempty"`
	Reader         *string                   `json:"reader,omitempty"`
	Mode           string                    `json:"mode,omitempty"`
	CapsBands      *int                      `json:"caps_bands,omitempty"`
	CapsNodes      *int                      `json:"caps_nodes,omitempty"`
	CapsChars      *int                      `json:"caps_chars,omitempty"`
	Offset         int                       `json:"offset,omitempty"`
	IncludeRaw     *bool                     `json:"include_raw,omitempty"`
	BudgetMS       *int                      `json:"budget_ms,omitempty"`
	RawLimit       int                       `json:"raw_limit,omitempty"`
	TimeRef        any                       `json:"time_ref,omitempty"`
	Presence       *text.PresenceCoordinates `json:"presence,omitempty"`
	NoCrystallize  bool                      `json:"no_crystallize,omitempty"`
	RenderMaxChars int                       `json:"render_max_chars,omitempty"`
	NeedRendered   bool                      `json:"need_rendered,omitempty"`
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

// UnixHydrateItem names an indexed document and its zero-copy `@ref:` pointer.
// Both fields are required to verify the source before returning speech text.
type UnixHydrateItem struct {
	DocID string `json:"doc_id,omitempty"`
	Ref   string `json:"ref"`
}

// UnixHydratePayload carries one display-hydration batch over the local Unix socket.
// Callers pass the pointers exactly as stored in `rm_units.raw_text`; the daemon
// answers with the canonical turn segmentation (the same resolver the product surfaces
// `recall` / `recent` use), so display layers cannot drift from ingest segmentation.
type UnixHydratePayload struct {
	DB    string            `json:"db,omitempty"`
	Items []UnixHydrateItem `json:"items"`
}

// UnixHydrateResult is the canonical text of one requested pointer. Verified is true
// when the sealed episodic index confirmed the content at ActualRef.
type UnixHydrateResult struct {
	DocID     string `json:"doc_id,omitempty"`
	Ref       string `json:"ref"`
	ActualRef string `json:"actual_ref,omitempty"`
	Text      string `json:"text,omitempty"`
	Verified  bool   `json:"verified,omitempty"`
	Error     string `json:"error,omitempty"`
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

// DaemonRecallNotSentError identifies failures before a recall request was written.
// Only this class can be retried in the caller's own process without duplication.
type DaemonRecallNotSentError struct {
	Err error
}

func (e *DaemonRecallNotSentError) Error() string { return e.Err.Error() }
func (e *DaemonRecallNotSentError) Unwrap() error { return e.Err }

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
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
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

func requestUnixDaemonControl(sockPath, path string, cfg *config.Config) error {
	conn, reader, err := verifiedUnixDaemonConn(sockPath, cfg)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, "http://unix"+path, strings.NewReader(`{}`))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := req.Write(conn); err != nil {
		return err
	}
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("daemon control returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPBody+1))
	var result struct {
		OK bool `json:"ok"`
	}
	if err != nil || len(body) > maxHTTPBody || reader.Buffered() != 0 || json.Unmarshal(body, &result) != nil || !result.OK {
		return errors.New("invalid daemon control response")
	}
	return nil
}

func requestUnixDaemonShutdown(sockPath string) {
	cfg, err := config.Load(config.LoadOptions{})
	if err == nil {
		_ = requestUnixDaemonControl(sockPath, "/v1/shutdown", cfg)
	}
}

// ShutdownUnixDaemon asks the local Unix domain socket daemon (if running) to
// step down cleanly so a freshly updated binary takes over on the next request.
// A daemon still serving an older binary is authenticated by socket ownership,
// vault path, and protocol identity, then asked to step down as well.
func ShutdownUnixDaemon(sockPath string) error {
	if sockPath == "" {
		sockPath = DefaultUnixSocketPath()
	} else {
		sockPath = normalizeUnixSocketPath(sockPath)
	}
	info, err := os.Lstat(sockPath)
	if err != nil {
		return err
	}
	cfg, err := config.Load(config.LoadOptions{})
	if err != nil {
		return err
	}
	if err := requestUnixDaemonControl(sockPath, "/v1/shutdown", cfg); err != nil {
		// A daemon can hold this vault and still fail the control handshake: it
		// may be an older executable, or it may predate a configuration change.
		// Both are retired through the ownership-and-vault verification path so
		// the next request starts a daemon under the current configuration.
		if !errors.Is(err, errUnixDaemonExecutableMismatch) && !errors.Is(err, errUnixDaemonIdentityMismatch) {
			return err
		}
		if staleErr := shutdownStaleUnixDaemon(sockPath, info, cfg); staleErr != nil {
			return fmt.Errorf("%w (stale daemon shutdown failed: %v)", err, staleErr)
		}
		return nil
	}
	return waitUnixDaemonSocketGone(info, sockPath)
}

func waitUnixDaemonSocketGone(info os.FileInfo, sockPath string) error {
	deadline := time.Now().Add(daemonStartupWait)
	for time.Now().Before(deadline) {
		current, err := os.Lstat(sockPath)
		if errors.Is(err, os.ErrNotExist) || (err == nil && !os.SameFile(info, current)) {
			return nil
		}
		if err != nil {
			return err
		}
		time.Sleep(30 * time.Millisecond)
	}
	return errors.New("daemon shutdown was not confirmed")
}

func sameVaultPath(left, right string) bool {
	if left == right {
		return true
	}
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	return leftErr == nil && rightErr == nil && os.SameFile(leftInfo, rightInfo)
}

// shutdownStaleUnixDaemon retires a same-owner socket that is served by a
// different executable. It carries no business request: peer ownership is
// checked first, then the health identity must describe this vault and this
// forwarding protocol before a shutdown request is written.
func shutdownStaleUnixDaemon(sockPath string, info os.FileInfo, cfg *config.Config) error {
	conn, err := net.DialTimeout("unix", sockPath, daemonPingTimeout)
	if err != nil {
		return err
	}
	defer conn.Close()
	pid, err := verifyUnixDaemonPeerOwner(conn)
	if err != nil {
		return fmt.Errorf("daemon peer verification: %w", err)
	}
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return err
	}
	reader := bufio.NewReader(conn)
	request, err := http.NewRequest(http.MethodGet, "http://unix/v1/health", nil)
	if err != nil {
		return err
	}
	if err := request.Write(conn); err != nil {
		return err
	}
	response, err := http.ReadResponse(reader, request)
	if err != nil {
		return err
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxHTTPBody+1))
	_ = response.Body.Close()
	if readErr != nil {
		return readErr
	}
	if response.StatusCode != http.StatusOK || len(body) > maxHTTPBody {
		return errors.New("stale daemon health is not readable")
	}
	var health UnixHealthResponse
	if err := json.Unmarshal(body, &health); err != nil {
		return fmt.Errorf("stale daemon health: %w", err)
	}
	if !health.OK || health.PID != pid || !sameVaultPath(health.DB, cfg.DB) ||
		(health.IdentityVersion != 0 && health.IdentityVersion != daemonIdentityVersion) ||
		(health.Mode != "" && health.Mode != "local") {
		return errors.New("stale daemon identity does not match this vault")
	}
	current, err := os.Lstat(sockPath)
	if err != nil || !os.SameFile(info, current) {
		return errors.New("stale daemon socket changed during recovery")
	}
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return err
	}
	shutdown, err := http.NewRequest(http.MethodPost, "http://unix/v1/shutdown", strings.NewReader(`{}`))
	if err != nil {
		return err
	}
	shutdown.Header.Set("Content-Type", "application/json")
	if err := shutdown.Write(conn); err != nil {
		return err
	}
	reply, err := http.ReadResponse(reader, shutdown)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(reply.Body, maxHTTPBody+1))
	_ = reply.Body.Close()
	if reply.StatusCode != http.StatusOK {
		return fmt.Errorf("stale daemon shutdown returned status %d", reply.StatusCode)
	}
	return waitUnixDaemonSocketGone(info, sockPath)
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
	cfg, err := config.Load(config.LoadOptions{})
	if err == nil {
		_ = requestUnixDaemonControl(sockPath, "/v1/invalidate", cfg)
	}
}

// unixDaemonSocketLease pins the socket inode in a private directory. The lock
// covers creation, publication, service, and cleanup across daemon processes.
type unixDaemonSocketLease struct {
	dir        *os.File
	dirPath    string
	anchor     string
	sockPath   string
	parentInfo os.FileInfo
	owned      os.FileInfo
}

var errUnixDaemonStarting = errors.New("daemon socket lease is held")

// unixDaemonLeaseDir resolves the private lease directory for sockPath. It sits
// beside the published socket whenever the anchor fits the path budget, so the
// published path itself is never rewritten to make room for the lease. Only
// when the adjacent anchor would exceed the budget does it fall back to a
// stable short directory under the system temp dir.
func unixDaemonLeaseDir(sockPath string) string {
	sum := sha256.Sum256([]byte(filepath.Base(sockPath)))
	beside := filepath.Join(filepath.Dir(sockPath), ".rm-socket-"+hex.EncodeToString(sum[:8]))
	if len(beside)+len("/socket") <= maxUnixSocketPathLen {
		return beside
	}
	sum = sha256.Sum256([]byte(sockPath))
	base := filepath.Join(os.TempDir(), "rm-"+hex.EncodeToString(sum[:6]))
	if len(base)+len("/.rm-socket-")+16+len("/socket") > maxUnixSocketPathLen {
		base = filepath.Join("/tmp", "rm-"+hex.EncodeToString(sum[:6]))
	}
	return filepath.Join(base, ".rm-socket-"+hex.EncodeToString(sum[:8]))
}

func acquireUnixDaemonSocketLease(sockPath string, create bool) (*unixDaemonSocketLease, error) {
	if !unixDaemonPeerVerificationAvailable() {
		return nil, errors.New("daemon socket lease is unavailable on this platform")
	}
	dirPath := unixDaemonLeaseDir(sockPath)
	if create {
		if err := os.MkdirAll(filepath.Dir(sockPath), 0o700); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(dirPath, 0o700); err != nil {
			return nil, err
		}
	}
	parent, err := os.Stat(filepath.Dir(sockPath))
	if err != nil {
		return nil, err
	}
	if !parent.IsDir() || !unixDaemonFileOwned(parent) || parent.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("unsafe daemon socket parent directory: %s", filepath.Dir(sockPath))
	}
	info, err := os.Lstat(dirPath)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || !unixDaemonFileOwned(info) || info.Mode().Perm() != 0o700 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return nil, fmt.Errorf("unsafe daemon socket lease directory: %s", dirPath)
	}
	dir, err := os.Open(dirPath)
	if err != nil {
		return nil, err
	}
	opened, err := dir.Stat()
	if err != nil || !os.SameFile(info, opened) {
		_ = dir.Close()
		return nil, errors.New("daemon socket lease directory changed")
	}
	if err := unixDaemonLock(dir); err != nil {
		_ = dir.Close()
		if unixDaemonLockBusy(err) {
			return nil, errUnixDaemonStarting
		}
		return nil, err
	}
	current, err := os.Lstat(dirPath)
	if err != nil || !os.SameFile(opened, current) {
		_ = dir.Close()
		return nil, errors.New("daemon socket lease directory changed")
	}
	return &unixDaemonSocketLease{dir: dir, dirPath: dirPath, anchor: filepath.Join(dirPath, "socket"), sockPath: sockPath, parentInfo: parent}, nil
}

func (lease *unixDaemonSocketLease) close() {
	if lease.dir != nil {
		_ = lease.dir.Close()
		lease.dir = nil
	}
}

func (lease *unixDaemonSocketLease) directoryUnchanged() bool {
	if lease.dir == nil {
		return false
	}
	opened, err := lease.dir.Stat()
	if err != nil {
		return false
	}
	parent, err := os.Stat(filepath.Dir(lease.sockPath))
	if err != nil || !parent.IsDir() || !unixDaemonFileOwned(parent) || parent.Mode().Perm()&0o022 != 0 || !os.SameFile(lease.parentInfo, parent) {
		return false
	}
	current, err := os.Lstat(lease.dirPath)
	return err == nil && current.IsDir() && unixDaemonFileOwned(current) && current.Mode().Perm() == 0o700 &&
		current.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) == 0 && os.SameFile(opened, current)
}

// recover removes only a socket previously published from this lease's pinned
// inode. A refused connection alone is insufficient: a socket can be bound but
// not yet listening. Unmarked and replaced paths are never removed.
func (lease *unixDaemonSocketLease) recover() error {
	if !lease.directoryUnchanged() {
		return errors.New("daemon socket lease directory changed")
	}
	anchor, err := os.Lstat(lease.anchor)
	if errors.Is(err, os.ErrNotExist) {
		if _, err := os.Lstat(lease.sockPath); err == nil {
			return fmt.Errorf("unmarked daemon socket path: %s", lease.sockPath)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err != nil {
		return err
	}
	if anchor.Mode()&os.ModeSocket == 0 || !unixDaemonFileOwned(anchor) {
		return errors.New("invalid daemon socket anchor")
	}
	if lease.owned != nil && !os.SameFile(lease.owned, anchor) {
		return errors.New("daemon socket anchor was replaced")
	}
	current, err := os.Lstat(lease.sockPath)
	if err == nil {
		if current.Mode()&os.ModeSocket == 0 || !unixDaemonFileOwned(current) || !os.SameFile(anchor, current) {
			return errors.New("daemon socket path does not match its anchor")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	conn, dialErr := net.DialTimeout("unix", lease.anchor, daemonPingTimeout)
	if dialErr == nil {
		_ = conn.Close()
		return errors.New("daemon socket anchor is still listening")
	}
	if !unixDaemonConnectionRefused(dialErr) {
		return fmt.Errorf("cannot establish daemon socket state: %w", dialErr)
	}
	if !lease.directoryUnchanged() {
		return errors.New("daemon socket lease directory changed")
	}
	pinned, err := os.Lstat(lease.anchor)
	if err != nil || !os.SameFile(anchor, pinned) {
		return errors.New("daemon socket anchor changed")
	}
	if current != nil {
		if err := lease.removeStaleSocket(anchor); err != nil {
			return err
		}
	}
	return os.Remove(lease.anchor)
}

// removeStaleSocket deletes the published path only after moving it into the
// lease directory and re-verifying the moved entry, so a path replaced by
// another same-user process between the check and the delete is never removed.
func (lease *unixDaemonSocketLease) removeStaleSocket(anchor os.FileInfo) error {
	if !lease.directoryUnchanged() {
		return errors.New("daemon socket lease directory changed")
	}
	latest, err := os.Lstat(lease.sockPath)
	if err != nil || latest.Mode()&os.ModeSocket == 0 || !unixDaemonFileOwned(latest) || !os.SameFile(anchor, latest) {
		return errors.New("daemon socket path changed")
	}
	return lease.stageAndRemoveStaleSocket(anchor)
}

// stageAndRemoveStaleSocket atomically moves the published path entry into the
// lease directory, verifies the moved entry is the pinned socket, and only then
// deletes it. Anything else is moved back to where it was and reported, never
// deleted, so a concurrent replacement can never lose a file.
func (lease *unixDaemonSocketLease) stageAndRemoveStaleSocket(anchor os.FileInfo) error {
	staged := filepath.Join(lease.dirPath, "stale")
	if err := os.Rename(lease.sockPath, staged); err != nil {
		return fmt.Errorf("stage stale daemon socket: %w", err)
	}
	stagedInfo, err := os.Lstat(staged)
	if err != nil || stagedInfo.Mode()&os.ModeSocket == 0 || !unixDaemonFileOwned(stagedInfo) || !os.SameFile(anchor, stagedInfo) {
		if _, statErr := os.Lstat(lease.sockPath); statErr == nil {
			return fmt.Errorf("daemon socket path was replaced during recovery: moved entry kept at %s", staged)
		}
		if restoreErr := os.Rename(staged, lease.sockPath); restoreErr != nil {
			return fmt.Errorf("daemon socket path was replaced and could not be restored: %w", restoreErr)
		}
		return errors.New("daemon socket path was replaced during recovery")
	}
	return os.Remove(staged)
}

// EnsureUnixDaemon validates an existing daemon or starts one when no socket exists.
// Only sockets owned by the lease protocol can be recovered automatically.
func EnsureUnixDaemon(sockPath string) error {
	cfg, err := config.Load(config.LoadOptions{})
	if err != nil {
		return err
	}
	return ensureUnixDaemon(sockPath, cfg)
}

func ensureUnixDaemon(sockPath string, cfg *config.Config) error {
	return ensureUnixDaemonContext(context.Background(), sockPath, cfg)
}

// EnsureUnixDaemonContext is the context-aware variant of EnsureUnixDaemon for
// callers (CLI daemon start, console) that need a bounded wait.
func EnsureUnixDaemonContext(ctx context.Context, sockPath string, cfg *config.Config) error {
	return ensureUnixDaemonContext(ctx, sockPath, cfg)
}

func ensureUnixDaemonContext(ctx context.Context, sockPath string, cfg *config.Config) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := unixDaemonConfigID(cfg); err != nil {
		return err
	}
	if sockPath == "" {
		sockPath = DefaultUnixSocketPath()
	} else {
		sockPath = normalizeUnixSocketPath(sockPath)
	}
	if info, err := os.Lstat(sockPath); err == nil {
		if info.Mode()&os.ModeSocket == 0 || !unixDaemonFileOwned(info) {
			return fmt.Errorf("unsafe daemon socket path: %s", sockPath)
		}
		conn, _, verifyErr := verifiedUnixDaemonConnContext(ctx, sockPath, cfg)
		if verifyErr == nil {
			return conn.Close()
		}
		if errors.Is(verifyErr, errUnixDaemonExecutableMismatch) {
			return fmt.Errorf("%w: an older daemon is running; run `rolling-memory daemon restart` to replace it", verifyErr)
		}
		if !unixDaemonConnectionRefused(verifyErr) {
			return verifyErr
		}
		// Recovery happens in the new daemon while it holds the lease through
		// publication, so competing starters cannot remove each other's socket.
		lease, leaseErr := acquireUnixDaemonSocketLease(sockPath, false)
		if errors.Is(leaseErr, errUnixDaemonStarting) {
			return waitUnixDaemon(ctx, sockPath, cfg)
		}
		if errors.Is(leaseErr, os.ErrNotExist) {
			return fmt.Errorf("unmarked daemon socket %s: after confirming no daemon is running, remove this socket manually and retry", sockPath)
		}
		if leaseErr != nil {
			return fmt.Errorf("daemon socket cannot be safely recovered (%v): %w", verifyErr, leaseErr)
		}
		anchor, anchorErr := os.Lstat(lease.anchor)
		current, currentErr := os.Lstat(sockPath)
		valid := anchorErr == nil && currentErr == nil && anchor.Mode()&os.ModeSocket != 0 &&
			current.Mode()&os.ModeSocket != 0 && unixDaemonFileOwned(anchor) && unixDaemonFileOwned(current) && os.SameFile(anchor, current)
		lease.close()
		if errors.Is(anchorErr, os.ErrNotExist) && currentErr == nil && current.Mode()&os.ModeSocket != 0 && unixDaemonFileOwned(current) && os.SameFile(info, current) {
			return fmt.Errorf("unmarked daemon socket %s: after confirming no daemon is running, remove this socket manually and retry", sockPath)
		}
		if !valid {
			return errors.New("daemon socket is unmarked or has changed; refusing automatic recovery")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := spawnUnixDaemonProcess(sockPath, cfg); err != nil {
		return err
	}
	return waitUnixDaemon(ctx, sockPath, cfg)
}

func waitUnixDaemon(ctx context.Context, sockPath string, cfg *config.Config) error {
	deadline := time.Now().Add(daemonStartupWait)
	for time.Now().Before(deadline) {
		if _, err := os.Lstat(sockPath); err == nil {
			conn, _, verifyErr := verifiedUnixDaemonConnContext(ctx, sockPath, cfg)
			if verifyErr == nil {
				return conn.Close()
			}
		}
		timer := time.NewTimer(30 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return fmt.Errorf("local memory daemon did not bind %s within %s", sockPath, daemonStartupWait)
}

func spawnUnixDaemonProcess(sockPath string, cfg *config.Config) error {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return fmt.Errorf("resolve executable: %w", err)
	}
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer devNull.Close()

	cmd := exec.Command(exe, "mcp", "--daemon", "--config", cfg.Path, "--db", cfg.DB, "--mode", cfg.Mode, "--scope", cfg.Scope)
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

// CallUnixDaemonRecall forwards one recall over the shared daemon socket.
// A daemon can retire between the health probe and the request (binary
// replacement or idle exit), leaving the caller on a closing listener. Recall is
// read-only and the deferred L3 write collapses by noetic id, so one transport
// retry is safe and turns a transient EOF into a served call.
func CallUnixDaemonRecall(ctx context.Context, sockPath string, cfg *config.Config, payload UnixRecallPayload) (*UnixRecallResponse, error) {
	resp, err := callUnixDaemonRecallOnce(ctx, sockPath, cfg, payload)
	if err == nil || ctx.Err() != nil || !retryableDaemonTransport(err) {
		return resp, err
	}
	return callUnixDaemonRecallOnce(ctx, sockPath, cfg, payload)
}

func retryableDaemonTransport(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, net.ErrClosed)
}

func callUnixDaemonRecallOnce(ctx context.Context, sockPath string, cfg *config.Config, payload UnixRecallPayload) (*UnixRecallResponse, error) {
	if sockPath == "" {
		sockPath = DefaultUnixSocketPath()
	} else {
		sockPath = normalizeUnixSocketPath(sockPath)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := unixDaemonConfigID(cfg); err != nil {
		return nil, err
	}
	if err := ensureUnixDaemonContext(ctx, sockPath, cfg); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conn, reader, err := verifiedUnixDaemonConnContext(ctx, sockPath, cfg)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	timeout := 65 * time.Second
	if payload.BudgetMS != nil && *payload.BudgetMS > 0 {
		timeout = time.Duration(*payload.BudgetMS)*time.Millisecond + 30*time.Second
	}
	deadline := time.Now().Add(timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, &DaemonRecallNotSentError{Err: err}
	}
	stopCancel := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopCancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/v1/recall", bytes.NewReader(raw))
	if err != nil {
		return nil, &DaemonRecallNotSentError{Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	if err := req.Write(conn); err != nil {
		return nil, fmt.Errorf("daemon recall outcome unknown: %w", err)
	}
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		return nil, fmt.Errorf("daemon recall outcome unknown: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxHTTPBody || reader.Buffered() != 0 {
		return nil, errors.New("invalid daemon response stream")
	}
	var out UnixRecallResponse
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&out); err != nil {
		return nil, fmt.Errorf("decode daemon response (status %d): %w", resp.StatusCode, err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("invalid trailing daemon response")
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
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("daemon recall failed with status %d", resp.StatusCode)
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

// Peer credentials and health identity protect against accidental reuse of an
// incompatible daemon. They do not authenticate arbitrary code running as the same user.
func verifiedUnixDaemonConn(sockPath string, cfg *config.Config) (net.Conn, *bufio.Reader, error) {
	return verifiedUnixDaemonConnContext(context.Background(), sockPath, cfg)
}

func verifiedUnixDaemonConnContext(ctx context.Context, sockPath string, cfg *config.Config) (net.Conn, *bufio.Reader, error) {
	configID, err := unixDaemonConfigID(cfg)
	if err != nil {
		return nil, nil, err
	}
	dialer := net.Dialer{Timeout: daemonPingTimeout}
	conn, err := dialer.DialContext(ctx, "unix", sockPath)
	if err != nil {
		return nil, nil, err
	}
	stopCancel := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopCancel()
	fail := func(err error) (net.Conn, *bufio.Reader, error) {
		_ = conn.Close()
		return nil, nil, err
	}
	pid, err := verifyUnixDaemonPeer(conn)
	if err != nil {
		return fail(fmt.Errorf("daemon peer verification: %w", err))
	}
	deadline := time.Now().Add(daemonPingTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return fail(err)
	}
	req, err := http.NewRequest(http.MethodGet, "http://unix/v1/health", nil)
	if err != nil {
		return fail(err)
	}
	if err := req.Write(conn); err != nil {
		return fail(err)
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		return fail(err)
	}
	var health UnixHealthResponse
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxHTTPBody+1))
	closeErr := resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Close || readErr != nil || closeErr != nil || len(body) > maxHTTPBody ||
		json.Unmarshal(body, &health) != nil || reader.Buffered() != 0 {
		return fail(errors.New("invalid daemon health response"))
	}
	if !health.OK || health.PID != pid || health.IdentityVersion != daemonIdentityVersion ||
		health.Mode != "local" || health.PulseMode != "local_plain" || health.ConfigID != configID ||
		!sameFileOrCleanPath(health.DB, cfg.DB) {
		return fail(fmt.Errorf("%w: run `rolling-memory daemon restart` to apply the current configuration", errUnixDaemonIdentityMismatch))
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	return conn, reader, nil
}

// ForwardToolCallToUnixDaemon forwards an MCP `tools/call` request from a lightweight
// stdio session to the shared local Unix domain socket daemon, injecting the caller
// window's resolved PresenceCoordinates so per-window repository isolation is preserved.
func ForwardToolCallToUnixDaemon(requestID any, arguments map[string]any, presence *text.PresenceCoordinates) (any, bool) {
	sockPath := DefaultUnixSocketPath()
	fail := func(err error) (any, bool) {
		return toolError(requestID, fmt.Errorf("local daemon: %w", err), io.Discard), true
	}
	cfg, err := config.Load(config.LoadOptions{})
	if err != nil {
		return fail(err)
	}
	if _, err := unixDaemonConfigID(cfg); err != nil {
		return fail(err)
	}
	if err := ensureUnixDaemon(sockPath, cfg); err != nil {
		return fail(err)
	}
	conn, reader, err := verifiedUnixDaemonConn(sockPath, cfg)
	if err != nil {
		return fail(err)
	}
	defer conn.Close()
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
		return fail(err)
	}
	if err := conn.SetDeadline(time.Now().Add(65 * time.Second)); err != nil {
		return fail(err)
	}
	req, err := http.NewRequest(http.MethodPost, "http://unix/mcp", bytes.NewReader(raw))
	if err != nil {
		return fail(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if err := req.Write(conn); err != nil {
		return fail(err)
	}
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		return fail(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fail(fmt.Errorf("daemon returned status %d", resp.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPBody+1))
	if err != nil || len(body) > maxHTTPBody || reader.Buffered() != 0 {
		return fail(errors.New("invalid daemon response stream"))
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil || envelope == nil || string(envelope["jsonrpc"]) != `"2.0"` {
		return fail(errors.New("invalid daemon JSON-RPC response"))
	}
	wantID, err := json.Marshal(requestID)
	if err != nil || !bytes.Equal(bytes.TrimSpace(envelope["id"]), wantID) {
		return fail(errors.New("daemon JSON-RPC response ID mismatch"))
	}
	result, hasResult := envelope["result"]
	rpcError, hasError := envelope["error"]
	if hasResult == hasError {
		return fail(errors.New("invalid daemon JSON-RPC result"))
	}
	var object map[string]json.RawMessage
	if hasResult {
		if json.Unmarshal(result, &object) != nil || object == nil {
			return fail(errors.New("invalid daemon JSON-RPC result"))
		}
		var content []json.RawMessage
		if json.Unmarshal(object["content"], &content) != nil || content == nil {
			return fail(errors.New("invalid daemon tool result"))
		}
		for _, block := range content {
			var item map[string]json.RawMessage
			var kind string
			if json.Unmarshal(block, &item) != nil || item == nil || json.Unmarshal(item["type"], &kind) != nil || kind == "" {
				return fail(errors.New("invalid daemon tool result"))
			}
			if kind == "text" {
				var text string
				if json.Unmarshal(item["text"], &text) != nil || bytes.Equal(bytes.TrimSpace(item["text"]), []byte("null")) {
					return fail(errors.New("invalid daemon tool result"))
				}
			}
		}
		if raw, ok := object["isError"]; ok {
			var isError bool
			if json.Unmarshal(raw, &isError) != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				return fail(errors.New("invalid daemon tool result"))
			}
		}
	} else {
		if json.Unmarshal(rpcError, &object) != nil || object == nil {
			return fail(errors.New("invalid daemon JSON-RPC error"))
		}
		var code int
		var message string
		if json.Unmarshal(object["code"], &code) != nil || json.Unmarshal(object["message"], &message) != nil ||
			bytes.Equal(bytes.TrimSpace(object["code"]), []byte("null")) || bytes.Equal(bytes.TrimSpace(object["message"]), []byte("null")) {
			return fail(errors.New("invalid daemon JSON-RPC error"))
		}
	}
	var rpcResp any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&rpcResp); err != nil {
		return fail(err)
	}
	return normalizeJSONValue(rpcResp), true
}

type unixDaemonMemoryKey struct{}

// ServeUnix runs the shared single-instance local memory daemon on sockPath.
// It hosts both `/v1/recall` (for CLI and Admin Console) and `/mcp` (for stdio MCP proxies),
// warms the in-memory corpus snapshot in the background on startup, and runs `onStart`
// (such as the single-instance auto-watcher) until shutdown.
func ServeUnix(sockPath string, memory *facade.Memory, onStart func(context.Context)) error {
	if sockPath == "" {
		sockPath = DefaultUnixSocketPath()
	} else {
		sockPath = normalizeUnixSocketPath(sockPath)
	}
	myMTime := currentExeModTimeNS()
	var daemonCfg *config.Config
	var err error
	if memory != nil {
		daemonCfg = memory.Config
	} else {
		daemonCfg, err = config.Load(config.LoadOptions{})
		if err != nil {
			return err
		}
	}
	// Apply the configured soft memory limit before the engine snapshot warms
	// up so the collector target covers the whole daemon lifetime.
	memoryState := ApplyMemoryLimit(daemonCfg)
	health, err := unixDaemonHealth(daemonCfg, myMTime)
	if err != nil {
		return err
	}
	lease, err := acquireUnixDaemonSocketLease(sockPath, true)
	if err != nil {
		return fmt.Errorf("mcp unix: socket lease: %w", err)
	}
	defer lease.close()
	if err := lease.recover(); err != nil {
		return fmt.Errorf("mcp unix: socket recovery: %w", err)
	}
	listener, err := net.Listen("unix", lease.anchor)
	if err != nil {
		return fmt.Errorf("mcp unix: listen %s: %w", lease.anchor, err)
	}
	if unixListener, ok := listener.(*net.UnixListener); ok {
		unixListener.SetUnlinkOnClose(false)
	}
	lease.owned, err = os.Lstat(lease.anchor)
	if err != nil || lease.owned.Mode()&os.ModeSocket == 0 || !unixDaemonFileOwned(lease.owned) {
		_ = listener.Close()
		return errors.New("daemon socket anchor unavailable after listen")
	}
	defer func() {
		_ = listener.Close()
		_ = lease.recover()
	}()
	if err := os.Chmod(lease.anchor, 0o600); err != nil {
		return err
	}
	if !lease.directoryUnchanged() {
		return errors.New("daemon socket lease directory changed")
	}
	anchor, err := os.Lstat(lease.anchor)
	if err != nil || !os.SameFile(lease.owned, anchor) {
		return errors.New("daemon socket anchor was replaced")
	}
	if err := os.Link(lease.anchor, sockPath); err != nil {
		return fmt.Errorf("mcp unix: publish %s: %w", sockPath, err)
	}
	published, err := os.Lstat(sockPath)
	if err != nil || published.Mode()&os.ModeSocket == 0 || !unixDaemonFileOwned(published) || !os.SameFile(lease.owned, published) {
		return errors.New("daemon socket changed during publication")
	}

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
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)
	defer signal.Stop(signals)
	go func() {
		select {
		case <-signals:
			_ = listener.Close()
		case <-ctx.Done():
		}
	}()

	// Bind health and requests to the opened memory's effective configuration.
	active, err := lazy.Get()
	if err != nil {
		return err
	}
	activeID, err := unixDaemonConfigID(active.Config)
	if err != nil || activeID != health.ConfigID {
		_ = lazy.Close()
		return errors.New("daemon configuration changed during startup")
	}
	active.SetColdBuildMemoryLimit(coldBuildMemoryLimit(memoryState))
	go func() { _ = active.Warmup(ctx) }()
	// Keep the newest published cluster opened ahead of the first query: the
	// builder republishes while the daemon serves, and paying the segment open
	// inline would turn the first recall after each publication into a slow
	// request.
	if warmClient, warmErr := active.LocalVault(); warmErr == nil && warmClient != nil && warmClient.Vault != nil {
		go warmPublishedGenerations(ctx, warmClient.Vault, publishedWarmInterval)
	}
	if onStart != nil {
		go onStart(ctx)
	}

	httpSrv := &HTTPServer{Memory: memory, Lazy: lazy}
	mcpHandler := httpSrv.Handler()

	var srv *http.Server
	var graphStore graphBuilderStore
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", func(w http.ResponseWriter, r *http.Request) {
		current, getErr := lazy.Get()
		if getErr != nil || current == nil {
			// A pinned daemon keeps serving its validated memory; when the on-disk
			// configuration can no longer be validated or no longer matches the
			// pinned identity (e.g., an obsolete mode left over from an upgrade),
			// that state is a configuration conflict, not an unavailable memory.
			if cfg, cfgErr := config.Load(config.LoadOptions{}); cfgErr != nil {
				http.Error(w, "daemon configuration changed", http.StatusConflict)
				return
			} else if id, idErr := unixDaemonConfigID(cfg); idErr != nil || id != health.ConfigID {
				http.Error(w, "daemon configuration changed", http.StatusConflict)
				return
			}
			http.Error(w, "daemon memory unavailable", http.StatusServiceUnavailable)
			return
		}
		currentHealth, healthErr := unixDaemonHealth(current.Config, myMTime)
		if healthErr != nil || currentHealth.ConfigID != health.ConfigID {
			http.Error(w, "daemon configuration changed", http.StatusConflict)
			return
		}
		writeJSON(w, http.StatusOK, currentHealth)
	})
	mux.HandleFunc("/v1/shutdown", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Connection", "close")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		// Stop accepting new connections once the reply has flushed; the serving
		// loop then drains in-flight requests in one place for every trigger.
		go func() {
			time.Sleep(15 * time.Millisecond)
			_ = listener.Close()
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
		current, getErr := lazy.Get()
		if getErr != nil || current == nil {
			http.Error(w, "daemon memory unavailable", http.StatusServiceUnavailable)
			return
		}
		currentID, configErr := unixDaemonConfigID(current.Config)
		if configErr != nil || currentID != health.ConfigID {
			http.Error(w, "daemon configuration changed", http.StatusConflict)
			return
		}
		handleUnixRecall(w, r, current, nil)
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
	mux.HandleFunc("/v1/graph", func(w http.ResponseWriter, r *http.Request) {
		activity.Store(time.Now().UnixNano())
		inFlight.Add(1)
		defer inFlight.Add(-1)
		handleUnixGraph(w, r, lazy, &graphStore, health)
	})
	mux.HandleFunc("/v1/invalidate", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		activity.Store(time.Now().UnixNano())
		inFlight.Add(1)
		defer inFlight.Add(-1)
		active := memory
		if active == nil && lazy != nil {
			active, _ = lazy.Get()
		}
		if active != nil {
			if tc, err := active.LocalVault(); err == nil && tc != nil && tc.Vault != nil {
				tc.Vault.InvalidateRuntimeCaches()
				graphStore.invalidate(tc.Vault)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		activity.Store(time.Now().UnixNano())
		inFlight.Add(1)
		defer inFlight.Add(-1)
		current, getErr := lazy.Get()
		if getErr != nil || current == nil {
			http.Error(w, "daemon memory unavailable", http.StatusServiceUnavailable)
			return
		}
		currentID, configErr := unixDaemonConfigID(current.Config)
		if configErr != nil || currentID != health.ConfigID {
			http.Error(w, "daemon configuration changed", http.StatusConflict)
			return
		}
		r.Host = "127.0.0.1"
		r.Header.Del("Origin")
		// Bind dispatch to the same memory whose configuration was checked above.
		r = r.WithContext(context.WithValue(r.Context(), unixDaemonMemoryKey{}, current))
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
	quiescentUnixDaemon(&inFlight)
	cancel()
	closeUnixDaemon(srv)
	_ = lazy.Close()
	if errors.Is(err, net.ErrClosed) || errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// quiescentUnixDaemon waits briefly for in-flight requests to finish so a
// retirement never cuts a response mid-write.
func quiescentUnixDaemon(inFlight *atomic.Int64) bool {
	deadline := time.Now().Add(shutdownDeadline)
	for inFlight.Load() > 0 {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
	return true
}

// closeUnixDaemon stops the server after a retirement trigger closed the
// listener. A bounded graceful Shutdown flushes responses still being written,
// and Close bounds the wait so a stuck connection cannot block exit.
func closeUnixDaemon(srv *http.Server) {
	shutdownCtx, stopShutdown := context.WithTimeout(context.Background(), shutdownDeadline)
	defer stopShutdown()
	_ = srv.Shutdown(shutdownCtx)
	_ = srv.Close()
}

func handleUnixRecall(w http.ResponseWriter, r *http.Request, memory *facade.Memory, lazy *LazyMemory) {
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
	req := facade.RecallRequest{
		Cue:           payload.Cue,
		Scope:         payload.Scope,
		Reader:        payload.Reader,
		Mode:          payload.Mode,
		CapsBands:     payload.CapsBands,
		CapsNodes:     payload.CapsNodes,
		CapsChars:     payload.CapsChars,
		Offset:        payload.Offset,
		IncludeRaw:    payload.IncludeRaw,
		BudgetMS:      payload.BudgetMS,
		RawLimit:      payload.RawLimit,
		TimeRef:       payload.TimeRef,
		Presence:      presence,
		NoCrystallize: payload.NoCrystallize,
	}
	result, err := active.Recall(req)
	if err != nil {
		writeJSON(w, http.StatusOK, UnixRecallResponse{OK: false, EngineError: true, Error: err.Error()})
		return
	}
	facade.WaitForPendingNoeticPulses(facade.NoeticPulseBudget)
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

func episodicHydrateTurn(ref string) string {
	ref = strings.TrimSpace(ref)
	index := strings.LastIndex(ref, "#")
	if index <= strings.LastIndex(ref, ":") {
		return ""
	}
	return ref[:index]
}

// handleUnixHydrate resolves display pointers only after the sealed episodic index
// verifies the document, pointer, and source content.
func handleUnixHydrate(w http.ResponseWriter, r *http.Request, memory *facade.Memory, lazy *LazyMemory) {
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
	var vault *localvault.Vault
	if tc, tcErr := active.LocalVault(); tcErr == nil && tc != nil {
		vault = tc.Vault
	}
	if vault == nil {
		writeJSON(w, http.StatusServiceUnavailable, UnixHydrateResponse{OK: false, Error: "local vault unavailable"})
		return
	}
	out := UnixHydrateResponse{OK: true, Texts: make([]UnixHydrateResult, 0, len(items))}
	for _, item := range items {
		ref := strings.TrimSpace(item.Ref)
		docID := strings.TrimSpace(item.DocID)
		res := UnixHydrateResult{DocID: docID, Ref: ref}
		if ref == "" {
			res.Error = "empty ref"
		} else if docID == "" {
			res.Error = "doc_id required"
		} else if vault.EpisodicPointerFor(docID) != ref {
			res.Error = "document pointer mismatch"
		} else if text, actualRef, vErr := vault.LoadVerifiedEpisodicPointerSource(docID, ref); vErr != nil {
			res.Error = vErr.Error()
		} else if strings.TrimSpace(text) == "" {
			res.Error = "empty content"
		} else if primaryTurn, actualTurn := episodicHydrateTurn(ref), episodicHydrateTurn(actualRef); primaryTurn == "" || actualTurn != primaryTurn {
			res.Error = "source provenance unavailable"
		} else {
			res.Text, res.ActualRef, res.Verified = text, actualRef, true
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
func handleUnixDocuments(w http.ResponseWriter, r *http.Request, memory *facade.Memory, lazy *LazyMemory) {
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
	var vault *localvault.Vault
	if tc, tcErr := active.LocalVault(); tcErr == nil && tc != nil {
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
