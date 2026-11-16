// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package api_test

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rocareer/rolling-memory/internal/api"
	"github.com/rocareer/rolling-memory/internal/config"
)

func TestLocalLegacyDeepEnvironmentDoesNotContactSentinel(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer server.Close()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ROLLING_MEMORY_MODE", "local")
	t.Setenv("ROLLING_MEMORY_LLM_PROVIDER", "openai")
	t.Setenv("ROLLING_MEMORY_LLM_BASE_URL", server.URL)
	t.Setenv("ROLLING_MEMORY_LLM_API_KEY_ENV", "ROLLING_MEMORY_TEST_LLM_KEY")
	t.Setenv("ROLLING_MEMORY_TEST_LLM_KEY", "test-key")
	t.Setenv("ROLLING_MEMORY_BRAIN_URL", server.URL+"/open/memory/v1")
	t.Setenv("ROLLING_MEMORY_BRAIN_KEY", "test-key")
	configPath := filepath.Join(home, "config.toml")
	dbPath := filepath.Join(home, "memory.db")
	scope := "mem:test"
	cfg, err := config.Load(config.LoadOptions{Path: &configPath, DB: &dbPath, Scope: &scope})
	if err != nil {
		t.Fatal(err)
	}
	memory, err := api.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	rememberOne(t, memory, "local sentinel", "local", "contains", map[string]any{"label": "sentinel"})
	if !memory.Store.HasLegacyGraph() || memory.LLM == nil {
		t.Fatal("fixture did not exercise configured LLM and legacy graph")
	}
	result, err := memory.Recall(api.RecallRequest{Cue: "local sentinel", Deep: true})
	if err != nil {
		t.Fatal(err)
	}
	deep, ok := result.Meta["deep"].(map[string]any)
	if !ok || deep["requested"] != true || deep["used"] != false {
		t.Fatalf("deep metadata = %#v", result.Meta["deep"])
	}
	if message, ok := deep["error"].(string); !ok || !strings.Contains(message, "blocks remote query rewriting") {
		t.Fatalf("deep error = %#v", deep["error"])
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("local legacy deep sent %d requests to sentinel", got)
	}
}
