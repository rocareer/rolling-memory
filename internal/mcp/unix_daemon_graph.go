// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package mcp

import (
	"encoding/json"
	"io"
	"net/http"
	"sync"

	"github.com/rocareer/rolling-memory/internal/graphmodel"
	"github.com/rocareer/rolling-memory/internal/memory/graph"
	"github.com/rocareer/rolling-memory/internal/memory/localvault"
)

// graphBuilderStore keeps one graph snapshot builder per active vault. The
// daemon may swap the vault on a configuration reload, so the store re-binds
// when the pointer changes.
type graphBuilderStore struct {
	mu      sync.Mutex
	vault   *localvault.Vault
	builder *graph.Builder
}

func (s *graphBuilderStore) forVault(vault *localvault.Vault) *graph.Builder {
	if s == nil || vault == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// LocalVault() returns a fresh request-local clone on every call; the
	// clone shares the SQLite handle and snapshot slots with the daemon's
	// base vault. Comparing store identity keeps one builder (and its
	// snapshot/view caches) alive across requests instead of rebuilding the
	// whole projection per view.
	if s.builder == nil || !s.vault.SharesStore(vault) {
		if s.builder != nil {
			// Releasing the previous builder retires its cached snapshot so
			// the retired corpus generation cannot stay mapped forever.
			s.builder.Invalidate()
		}
		s.vault = vault
		s.builder = graph.NewBuilder(vault)
	}
	return s.builder
}

func (s *graphBuilderStore) invalidate(vault *localvault.Vault) {
	if s == nil || vault == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.builder != nil && s.vault.SharesStore(vault) {
		s.builder.Invalidate()
	}
}

// handleUnixGraph serves one read-only engine graph projection over the local
// Unix socket. It never touches a remote endpoint; all data comes from the
// daemon's local vault and its immutable core snapshot.
func handleUnixGraph(w http.ResponseWriter, r *http.Request, lazy *LazyMemory, store *graphBuilderStore, health UnixHealthResponse) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxHTTPBody))
	if err != nil {
		http.Error(w, "read graph request failed", http.StatusBadRequest)
		return
	}
	var req graphmodel.ViewRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid graph request", http.StatusBadRequest)
		return
	}
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
	tc, tcErr := current.LocalVault()
	if tcErr != nil || tc == nil || tc.Vault == nil {
		http.Error(w, "daemon memory unavailable", http.StatusServiceUnavailable)
		return
	}
	builder := store.forVault(tc.Vault)
	if builder == nil {
		http.Error(w, "graph builder unavailable", http.StatusServiceUnavailable)
		return
	}
	resp, viewErr := builder.View(r.Context(), req)
	if viewErr != nil {
		http.Error(w, viewErr.Error(), http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
