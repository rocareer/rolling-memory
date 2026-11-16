// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package mcp

// This file serves the same JSON-RPC surface as the stdio loop over the two URL
// transports desktop harnesses register: the streamable HTTP endpoint
// (2025-06-18, one path for POST / GET / DELETE) and the legacy HTTP+SSE pair
// (2024-11-05, a GET event stream plus a POST message endpoint).
//
// The listener stays on the loopback interface: the URL transports exist for
// apps that accept an MCP address on the same machine, so no external peer ever
// needs to reach it.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rocareer/rolling-memory/internal/api"
)

const (
	// HTTPSessionHeader carries the streamable-HTTP session id.
	HTTPSessionHeader = "Mcp-Session-Id"
	// HTTPProtocolHeader carries the negotiated protocol version.
	HTTPProtocolHeader = "MCP-Protocol-Version"

	// DefaultHTTPAddr is the loopback listen address of the URL transports.
	DefaultHTTPAddr = "127.0.0.1:8765"
	// DefaultHTTPPath is the streamable-HTTP endpoint.
	DefaultHTTPPath = "/mcp"
	// DefaultSSEPath is the legacy HTTP+SSE stream endpoint.
	DefaultSSEPath = "/sse"
	// DefaultSSEMessagePath is the legacy client-to-server POST endpoint.
	DefaultSSEMessagePath = "/messages"

	// maxHTTPBody caps one request body; tool payloads are text, so 8 MiB is
	// far above any real memory unit.
	maxHTTPBody = 8 << 20

	// httpReadHeaderTimeout bounds slow-header peers on the loopback socket.
	httpReadHeaderTimeout = 10 * time.Second

	// sseKeepAlive keeps the legacy stream observable through idle periods.
	sseKeepAlive = 30 * time.Second

	// sseWriteTimeout bounds one stream write so a peer that stops reading cannot
	// park the handler goroutine (and its session) forever.
	sseWriteTimeout = 2 * sseKeepAlive

	// maxHTTPSessions bounds the in-memory session table: streamable-HTTP clients
	// that never send DELETE would otherwise grow it without limit on a
	// long-lived daemon.
	maxHTTPSessions = 256
)

// HTTPServer serves one memory over the URL transports. Zero fields fall back to
// the defaults above and to the lazily opened process memory.
type HTTPServer struct {
	Addr        string
	Path        string
	SSEPath     string
	MessagePath string
	Memory      *api.Memory
	Lazy        *LazyMemory
	Err         io.Writer

	mu       sync.Mutex
	sessions map[string]*httpSession
	lazyMu   sync.Mutex
}

// httpSession tracks one initialized client. The streamable transport only
// needs the id; the legacy SSE transport also owns the response channel that
// its GET stream drains.
type httpSession struct {
	events    chan []byte
	closed    chan struct{}
	createdAt time.Time
}

func (s *HTTPServer) addr() string {
	if s.Addr != "" {
		return s.Addr
	}
	return DefaultHTTPAddr
}

func (s *HTTPServer) path() string {
	if s.Path != "" {
		return s.Path
	}
	return DefaultHTTPPath
}

func (s *HTTPServer) ssePath() string {
	if s.SSEPath != "" {
		return s.SSEPath
	}
	return DefaultSSEPath
}

func (s *HTTPServer) messagePath() string {
	if s.MessagePath != "" {
		return s.MessagePath
	}
	return DefaultSSEMessagePath
}

func (s *HTTPServer) lazy() *LazyMemory {
	s.lazyMu.Lock()
	defer s.lazyMu.Unlock()
	if s.Lazy == nil {
		s.Lazy = NewLazyMemory()
	}
	return s.Lazy
}

func (s *HTTPServer) debugWriter() io.Writer {
	if s.Err != nil {
		return s.Err
	}
	return os.Stderr
}

// ServeHTTP runs the streamable-HTTP and legacy SSE transports until the
// listener fails; memory may be nil to open one lazily on first tool call.
func ServeHTTP(addr, path string, memory *api.Memory) error {
	server := &HTTPServer{Addr: addr, Path: path, Memory: memory}
	return server.ListenAndServe()
}

// Handler returns the router for both URL transports; tests drive it with
// httptest, ListenAndServe serves it on Addr.
func (s *HTTPServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(s.path(), s.handleStreamable)
	mux.HandleFunc(s.ssePath(), s.handleSSE)
	mux.HandleFunc(s.messagePath(), s.handleSSEMessage)
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !loopbackAuthority(request.Host) {
			http.Error(writer, "host not allowed", http.StatusForbidden)
			return
		}
		origins := request.Header.Values("Origin")
		if len(origins) > 1 || (len(origins) == 1 && !originAllowed(origins[0])) {
			http.Error(writer, "origin not allowed", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(writer, request)
	})
}

// ListenAndServe serves both transports on Addr and blocks until the listener
// returns a fatal error.
func (s *HTTPServer) ListenAndServe() error {
	host, port, err := net.SplitHostPort(s.addr())
	if err != nil || !loopbackHost(host) {
		return fmt.Errorf("mcp http: listen address %q must use a loopback host", s.addr())
	}
	// Pin localhost to a numeric address rather than trusting name resolution.
	address := s.addr()
	if strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") {
		address = net.JoinHostPort("127.0.0.1", port)
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("mcp http: listen %s: %w", s.addr(), err)
	}
	defer listener.Close()
	return s.Serve(listener)
}

// Serve serves both transports on an already-bound listener; ListenAndServe
// calls it, and tests bind their own loopback port.
func (s *HTTPServer) Serve(listener net.Listener) error {
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok || address.IP == nil || !address.IP.IsLoopback() {
		return fmt.Errorf("mcp http: listener address %s must be a loopback TCP address", listener.Addr())
	}
	server := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: httpReadHeaderTimeout,
	}
	fmt.Fprintf(s.debugWriter(), "mcp http: listening on http://%s%s (legacy SSE: %s)\n",
		listener.Addr(), s.path(), s.ssePath())
	return server.Serve(listener)
}

// loopbackHost accepts numeric loopback addresses and localhost, without DNS.
func loopbackHost(host string) bool {
	if strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// loopbackAuthority validates the Host before routing or reflecting it in SSE.
func loopbackAuthority(authority string) bool {
	host := authority
	if strings.Contains(authority, ":") {
		var port string
		var err error
		host, port, err = net.SplitHostPort(authority)
		if err != nil {
			if strings.HasPrefix(authority, "[") && strings.HasSuffix(authority, "]") {
				host = authority[1 : len(authority)-1]
			} else {
				return false
			}
		} else {
			portNumber, err := strconv.ParseUint(port, 10, 16)
			if err != nil || portNumber == 0 {
				return false
			}
		}
	}
	return loopbackHost(host)
}

// originAllowed rejects cross-origin reads; native MCP clients may omit Origin.
func originAllowed(origin string) bool {
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	return loopbackAuthority(parsed.Host)
}

func (s *HTTPServer) newSession() (*httpSession, string) {
	session := &httpSession{events: make(chan []byte, 16), closed: make(chan struct{}), createdAt: time.Now()}
	id := newSessionID()
	s.mu.Lock()
	if s.sessions == nil {
		s.sessions = map[string]*httpSession{}
	}
	if len(s.sessions) >= maxHTTPSessions {
		s.evictOldestSessionLocked()
	}
	s.sessions[id] = session
	s.mu.Unlock()
	return session, id
}

// evictOldestSessionLocked drops the oldest session when the table is at its
// cap; callers hold s.mu. Closing it releases any stream parked on it.
func (s *HTTPServer) evictOldestSessionLocked() {
	oldestID := ""
	var oldest time.Time
	for id, session := range s.sessions {
		if oldestID == "" || session.createdAt.Before(oldest) {
			oldestID, oldest = id, session.createdAt
		}
	}
	if oldestID == "" {
		return
	}
	delete(s.sessions, oldestID)
	close(s.sessions[oldestID].closed)
}

// newSessionID mints an opaque session token; it only has to be unguessable to
// other processes on the same host, since the listener is loopback-only.
func newSessionID() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return "s-" + hex.EncodeToString(buffer)
}

func (s *HTTPServer) session(id string) (*httpSession, bool) {
	if id == "" {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[id]
	return session, ok
}

func (s *HTTPServer) dropSession(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[id]
	if !ok {
		return false
	}
	delete(s.sessions, id)
	close(session.closed)
	return true
}

// alwaysJSON reports whether the client accepts an application/json body; when
// it does not, the streamable transport answers with a single SSE event instead.
func alwaysJSON(request *http.Request) bool {
	accept := request.Header.Get("Accept")
	return accept == "" || strings.Contains(accept, "application/json") || strings.Contains(accept, "*/*")
}

// decodeBody reads and decodes one JSON-RPC message (single object or batch).
func decodeBody(request *http.Request) (any, error) {
	body, err := io.ReadAll(io.LimitReader(request.Body, maxHTTPBody))
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(string(body))
	if text == "" {
		return nil, fmt.Errorf("empty body")
	}
	var message any
	if err := json.Unmarshal([]byte(text), &message); err != nil {
		return nil, err
	}
	return message, nil
}

// dispatch runs one decoded message and returns the response payload, or nil
// when the message carried no id (a notification).
func (s *HTTPServer) dispatch(message any) any {
	if batch, ok := message.([]any); ok {
		responses := make([]any, 0, len(batch))
		for _, item := range batch {
			if response := handle(item, s.Memory, s.lazy(), s.debugWriter()); response != nil {
				responses = append(responses, response)
			}
		}
		if len(responses) == 0 {
			return nil
		}
		return responses
	}
	return handle(message, s.Memory, s.lazy(), s.debugWriter())
}

// initialized reports whether a message is the initialize request, whose result
// mints the session id handed back in the response header.
func initialized(message any) bool {
	object, ok := message.(map[string]any)
	if !ok {
		return false
	}
	method, _ := object["method"].(string)
	return method == "initialize"
}

func writeJSON(writer http.ResponseWriter, status int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		http.Error(writer, "encode error", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_, _ = writer.Write(append(body, '\n'))
}

// handleStreamable serves the single-endpoint streamable HTTP transport.
func (s *HTTPServer) handleStreamable(writer http.ResponseWriter, request *http.Request) {
	if !originAllowed(request.Header.Get("Origin")) {
		http.Error(writer, "origin not allowed", http.StatusForbidden)
		return
	}
	switch request.Method {
	case http.MethodPost:
		s.handleStreamablePost(writer, request)
	case http.MethodDelete:
		if sessionID := request.Header.Get(HTTPSessionHeader); sessionID != "" && s.dropSession(sessionID) {
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		http.Error(writer, "unknown session", http.StatusNotFound)
	default:
		// No server-initiated stream and no other verb: MCP streamable HTTP
		// allows a server to answer 405 for both.
		writer.Header().Set("Allow", "POST, DELETE")
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *HTTPServer) handleStreamablePost(writer http.ResponseWriter, request *http.Request) {
	sessionID := request.Header.Get(HTTPSessionHeader)
	if sessionID != "" {
		if _, ok := s.session(sessionID); !ok {
			http.Error(writer, "session not found", http.StatusNotFound)
			return
		}
	}
	message, err := decodeBody(request)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, errorResponse(nil, codeParseError, "parse error: "+err.Error()))
		return
	}
	if sessionID == "" && !initialized(message) {
		// Stateless clients skip initialize; accept them instead of rejecting the
		// request, but do not mint a session for them.
		response := s.dispatch(message)
		if response == nil {
			writer.WriteHeader(http.StatusAccepted)
			return
		}
		writeJSON(writer, http.StatusOK, response)
		return
	}
	if initialized(message) {
		_, id := s.newSession()
		writer.Header().Set(HTTPSessionHeader, id)
	}
	response := s.dispatch(message)
	if response == nil {
		writer.WriteHeader(http.StatusAccepted)
		return
	}
	if !alwaysJSON(request) {
		writeSSEMessage(writer, response)
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func writeSSEMessage(writer http.ResponseWriter, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		http.Error(writer, "encode error", http.StatusInternalServerError)
		return
	}
	stampSSEWriteDeadline(writer)
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(writer, "event: message\ndata: %s\n\n", body)
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

// stampSSEWriteDeadline bounds the next stream write so a peer that stops
// reading cannot park the handler goroutine forever.
func stampSSEWriteDeadline(writer http.ResponseWriter) {
	_ = http.NewResponseController(writer).SetWriteDeadline(time.Now().Add(sseWriteTimeout))
}

// handleSSE serves the legacy HTTP+SSE stream: it announces the message
// endpoint, then forwards every response posted to that endpoint.
func (s *HTTPServer) handleSSE(writer http.ResponseWriter, request *http.Request) {
	if !originAllowed(request.Header.Get("Origin")) {
		http.Error(writer, "origin not allowed", http.StatusForbidden)
		return
	}
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", "GET")
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	flusher, ok := writer.(http.Flusher)
	if !ok {
		http.Error(writer, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	session, id := s.newSession()
	defer s.dropSession(id)
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("Connection", "keep-alive")
	writer.WriteHeader(http.StatusOK)
	// Absolute endpoint: some classic (2024-11-05) clients do not resolve a
	// relative location against the stream URL.
	_, _ = fmt.Fprintf(writer, "event: endpoint\ndata: http://%s%s?sessionId=%s\n\n",
		request.Host, s.messagePath(), id)
	flusher.Flush()

	keepAlive := time.NewTicker(sseKeepAlive)
	defer keepAlive.Stop()
	for {
		select {
		case payload := <-session.events:
			stampSSEWriteDeadline(writer)
			if _, err := fmt.Fprintf(writer, "event: message\ndata: %s\n\n", payload); err != nil {
				return
			}
			flusher.Flush()
		case <-keepAlive.C:
			stampSSEWriteDeadline(writer)
			if _, err := io.WriteString(writer, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-request.Context().Done():
			return
		case <-session.closed:
			return
		}
	}
}

// handleSSEMessage accepts one client-to-server message for a legacy session and
// queues the response on that session's stream.
func (s *HTTPServer) handleSSEMessage(writer http.ResponseWriter, request *http.Request) {
	if !originAllowed(request.Header.Get("Origin")) {
		http.Error(writer, "origin not allowed", http.StatusForbidden)
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", "POST")
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	session, ok := s.session(request.URL.Query().Get("sessionId"))
	if !ok {
		http.Error(writer, "unknown session", http.StatusNotFound)
		return
	}
	message, err := decodeBody(request)
	if err != nil {
		http.Error(writer, "parse error: "+err.Error(), http.StatusBadRequest)
		return
	}
	response := s.dispatch(message)
	if response == nil {
		writer.WriteHeader(http.StatusAccepted)
		return
	}
	body, err := json.Marshal(response)
	if err != nil {
		http.Error(writer, "encode error", http.StatusInternalServerError)
		return
	}
	select {
	case session.events <- body:
		writer.WriteHeader(http.StatusAccepted)
	case <-session.closed:
		http.Error(writer, "session closed", http.StatusGone)
	case <-time.After(5 * time.Second):
		http.Error(writer, "stream stalled", http.StatusServiceUnavailable)
	}
}
