// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package api_test

import (
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/api"
	"github.com/rocareer/rolling-memory/internal/thinclient"
)

func TestLocalVaultRecallRejectsUnsupportedFilters(t *testing.T) {
	t.Setenv("ROLLING_MEMORY_BRAIN_URL", "")
	memory := newVaultOnlyMemory(t)
	client, err := memory.ThinClient()
	if err != nil {
		t.Fatalf("thinclient: %v", err)
	}
	if err := client.Vault.SaveLocalPlaintext("ep_filter", testNow, "filter witness", nil, thinclient.CloudIngestResponse{Layer: "episodic"}); err != nil {
		t.Fatalf("seed local vault: %v", err)
	}

	session := "different-session"
	emptySession := ""
	for _, mode := range []string{"full", "probe", "recent"} {
		for _, tc := range []struct {
			name string
			req  api.RecallRequest
		}{
			{"session", api.RecallRequest{Session: &session}},
			{"empty-session", api.RecallRequest{Session: &emptySession}},
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
				if err == nil || !strings.Contains(err.Error(), "local_plain") || result != nil {
					t.Fatalf("filtered recall = %#v, %v; want local_plain unsupported error and no result", result, err)
				}
			})
		}
	}
	for _, mode := range []string{"full", "probe", "recent"} {
		result, err := memory.Recall(api.RecallRequest{Cue: "filter witness", Mode: mode, NoCrystallize: true})
		if err != nil || result == nil || len(result.Bands) == 0 {
			t.Fatalf("unfiltered %s recall = %#v, %v; want vault result", mode, result, err)
		}
	}
}

func TestLocalGraphSessionFilterDoesNotFallBackToVault(t *testing.T) {
	t.Setenv("ROLLING_MEMORY_BRAIN_URL", "")
	memory := newMemory(t)
	client, err := memory.ThinClient()
	if err != nil {
		t.Fatalf("thinclient: %v", err)
	}
	if err := client.Vault.SaveLocalPlaintext("ep_filter_fallback", testNow, "fallback witness", nil, thinclient.CloudIngestResponse{Layer: "episodic"}); err != nil {
		t.Fatalf("seed local vault: %v", err)
	}
	if !memory.Store.HasLegacyGraph() {
		t.Fatal("test requires the graph path")
	}
	session := "different-session"
	result, err := memory.Recall(api.RecallRequest{Cue: "fallback witness", Session: &session})
	if err == nil || !strings.Contains(err.Error(), "graph recall cannot enforce") || result != nil {
		t.Fatalf("graph session recall = %#v, %v; want unsupported error and no vault content", result, err)
	}
	unfiltered, err := memory.Recall(api.RecallRequest{Cue: "fallback witness", NoCrystallize: true})
	if err != nil || (len(unfiltered.Bands) == 0 && len(unfiltered.RawHits) == 0) {
		t.Fatalf("unfiltered vault fallback = %#v, %v", unfiltered, err)
	}
}
