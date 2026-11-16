// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/eval"
	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

func TestEvalVerifyPrepareFailureDoesNotRetireOrRequest(t *testing.T) {
	for _, suite := range []string{eval.SuiteLoCoMo, eval.SuiteATM} {
		t.Run(suite, func(t *testing.T) {
			env := newEnv(t)
			t.Setenv("ROLLING_MEMORY_USER_ID", "10")
			t.Setenv("ROLLING_MEMORY_EVAL_PULSE_MODE", "")
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			defer server.Close()
			t.Setenv("ROLLING_MEMORY_BRAIN_URL", server.URL+"/open/memory/v1")
			t.Setenv("ROLLING_MEMORY_BRAIN_KEY", "mem_test_mode_contract")
			oldStore := filepath.Join(t.TempDir(), "old.json")
			sentinel := oldStore + ".local-vault.sqlite"
			if err := os.WriteFile(sentinel, []byte("untouched"), 0o600); err != nil {
				t.Fatal(err)
			}
			scope := eval.ScopeForSuiteAndMode(suite, "10", eval.PulseModeLocalPlain) + "-old"
			gen := eval.EvalGeneration{Scope: scope, Suite: suite, UserID: "10", Endpoint: "local://sqlite-fts5-bm25",
				StorePath: oldStore, Role: "idle", HeartbeatAt: time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)}
			if err := eval.SaveEvalGeneration(gen); err != nil {
				t.Fatal(err)
			}
			tier := "100"
			if suite == eval.SuiteATM {
				tier = "hard"
			}
			result := run(t, "eval", "--suite", suite, "--tier", tier, "--verify-prepare", "--answerer", "none", "--judge", "off", "--json")
			if result.code == 0 {
				t.Fatal("verification without prepared data must fail")
			}
			if raw, err := os.ReadFile(sentinel); err != nil || string(raw) != "untouched" {
				t.Fatalf("verification retired existing generation: contents=%q err=%v", raw, err)
			}
			lease := filepath.Join(env.base, "xdg-state", "rolling-memory", "eval", "generations", suite, scope+".json")
			if _, err := os.Stat(lease); err != nil {
				t.Fatalf("verification removed generation lease: %v", err)
			}
			if got := requests.Load(); got != 0 {
				t.Fatalf("local verification triggered %d HTTP requests", got)
			}
		})
	}
}

func TestEvalRecallPulseModeProvenance(t *testing.T) {
	newEnv(t)
	t.Setenv("ROLLING_MEMORY_MODE", "cloud")
	t.Setenv("ROLLING_MEMORY_BRAIN_KEY", "mem_test_mode_contract")
	t.Setenv("ROLLING_MEMORY_USER_ID", "10")
	for _, mode := range []string{eval.PulseModePulsePlain, eval.PulseModePulseCipher} {
		t.Run(mode, func(t *testing.T) {
			scope := eval.ScopeForSuiteAndMode(eval.SuiteLoCoMo, "10", mode)
			storePath := filepath.Join(t.TempDir(), "eval.json")
			store, err := ramesdk.NewJSONFileStore(storePath, []byte("mem_test_mode_contract"))
			if err != nil {
				t.Fatal(err)
			}
			if err := store.SaveLocalPlaintext("doc-a", 1, "memory sentinel", []string{"memory sentinel"}, ramesdk.CloudIngestResponse{}); err != nil {
				t.Fatal(err)
			}
			if err := store.Flush(); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/open/memory/v1/health":
					_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "engine": "autonoetic", "version": "v1"})
				case "/open/memory/v1/auth/verify":
					_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "valid": true, "engine": "autonoetic", "tenant_id": "user", "user_id_prefixes": []string{"10", "u10"}})
				case "/open/memory/v1/query":
					_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "tenant_id": "user", "scope": scope, "engine": "autonoetic", "count": 1, "hits": []map[string]any{{"id": "doc-a", "final_weight": 1.0}}})
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			t.Setenv("ROLLING_MEMORY_BRAIN_URL", server.URL)
			result := run(t, "recall", "--db", storePath, "--scope", scope, "--q", "memory sentinel", "--json")
			if result.code != 0 {
				t.Fatalf("recall failed: %d %s %s", result.code, result.stdout, result.stderr)
			}
			meta := jsonObject(t, result.stdout)["meta"].(map[string]any)
			if meta["engine_transport"] != eval.TransportForPulseMode(mode) || meta["pulse_mode"] != mode || meta["engine_scope"] != scope || meta["local_fallback"] != false {
				t.Fatalf("pulse provenance = %v", meta)
			}
		})
	}
}

func TestEvalRecallLocalPlainProvenanceAndNoRequests(t *testing.T) {
	newEnv(t)
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	t.Setenv("ROLLING_MEMORY_BRAIN_URL", server.URL+"/open/memory/v1")
	t.Setenv("ROLLING_MEMORY_BRAIN_KEY", "mem_test_mode_contract")
	t.Setenv("ROLLING_MEMORY_USER_ID", "10")
	storePath := filepath.Join(t.TempDir(), "eval-locomo-local-plain.json")
	engine, err := eval.NewCloudEngine(&config.Config{Mode: "local"}, eval.SuiteLoCoMo, storePath, eval.WithPulseMode(eval.PulseModeLocalPlain))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Ingest(t.Context(), []eval.CloudDocument{{ID: "doc-a", Text: "local memory sentinel", OccurredAt: 1}}, false, 1, nil); err != nil {
		t.Fatal(err)
	}
	result := run(t, "recall", "--db", storePath, "--scope", engine.Scope, "--q", "local memory sentinel", "--json")
	if result.code != 0 {
		t.Fatalf("local recall failed: %d %s %s", result.code, result.stdout, result.stderr)
	}
	payload := jsonObject(t, result.stdout)
	meta := payload["meta"].(map[string]any)
	if meta["engine_transport"] != eval.LocalPlainTransport || meta["pulse_mode"] != eval.PulseModeLocalPlain ||
		meta["engine_scope"] != engine.Scope || meta["engine_upstream"] != engine.Endpoint || meta["local_fallback"] != false {
		t.Fatalf("local provenance = %v", meta)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("local recall triggered %d HTTP requests", got)
	}
}
