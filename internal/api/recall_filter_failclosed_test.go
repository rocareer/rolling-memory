// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rocareer/rolling-memory/internal/api"
)

func TestPulseRecallRejectsUnsupportedFiltersBeforeTransport(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "transport witness", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	t.Setenv("ROLLING_MEMORY_BRAIN_URL", server.URL+"/open/memory/v1")
	memory := thinBrainMemory(t, server.URL+"/open/memory/v1")
	session, empty := "another-session", ""
	for _, mode := range []string{"full", "probe", "recent"} {
		for _, tc := range []struct {
			name string
			req  api.RecallRequest
		}{
			{"session", api.RecallRequest{Session: &session}},
			{"empty-session", api.RecallRequest{Session: &empty}},
			{"asof", api.RecallRequest{AsOf: "2026-01-01"}},
			{"asof-zero", api.RecallRequest{AsOf: 0}},
			{"window-start", api.RecallRequest{WindowStart: "2026-01-01"}},
			{"window-end", api.RecallRequest{WindowEnd: "2026-01-01"}},
			{"combined", api.RecallRequest{Session: &session, AsOf: "2026-01-01", WindowStart: "2025-01-01", WindowEnd: "2026-01-01"}},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				tc.req.Mode = mode
				tc.req.Cue = "filter witness"
				result, err := memory.Recall(tc.req)
				if err == nil || !strings.Contains(err.Error(), "cannot enforce") || result != nil {
					t.Fatalf("filtered recall = %#v, %v; want unsupported error", result, err)
				}
			})
		}
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("filtered recall made %d transport requests", got)
	}
	result, err := memory.Recall(api.RecallRequest{Mode: "recent", NoCrystallize: true})
	if err != nil || result == nil {
		t.Fatalf("unfiltered recent recall = %#v, %v", result, err)
	}
}

func TestGraphRecallRejectsUnsupportedFilters(t *testing.T) {
	t.Setenv("ROLLING_MEMORY_BRAIN_URL", "")
	memory := newMemory(t)
	if !memory.Store.HasLegacyGraph() {
		t.Fatal("test requires the graph path")
	}
	session, empty := "another-session", ""
	for _, mode := range []string{"full", "probe", "recent"} {
		for _, tc := range []struct {
			name string
			req  api.RecallRequest
		}{
			{"session", api.RecallRequest{Session: &session}},
			{"empty-session", api.RecallRequest{Session: &empty}},
			{"asof", api.RecallRequest{AsOf: "2026-01-01"}},
			{"asof-zero", api.RecallRequest{AsOf: 0}},
			{"window-start", api.RecallRequest{WindowStart: "2026-01-01"}},
			{"window-end", api.RecallRequest{WindowEnd: "2026-01-01"}},
			{"combined", api.RecallRequest{Session: &session, AsOf: "2026-01-01", WindowStart: "2025-01-01", WindowEnd: "2026-01-01"}},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				tc.req.Mode = mode
				tc.req.Cue = "filter witness"
				result, err := memory.Recall(tc.req)
				if err == nil || !strings.Contains(err.Error(), "graph recall cannot enforce") || result != nil {
					t.Fatalf("filtered recall = %#v, %v; want unsupported error", result, err)
				}
			})
		}
	}
	result, err := memory.Recall(api.RecallRequest{Cue: "filter witness", NoCrystallize: true})
	if err != nil || result == nil {
		t.Fatalf("unfiltered graph recall = %#v, %v", result, err)
	}
}
