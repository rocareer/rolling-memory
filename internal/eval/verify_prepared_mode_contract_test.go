// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package eval

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocareer/rolling-memory/internal/config"
)

func TestDefaultEvalPulseModeIsLocalPlain(t *testing.T) {
	t.Setenv("ROLLING_MEMORY_EVAL_PULSE_MODE", "")
	t.Setenv("ROLLING_MEMORY_BRAIN_URL", "http://127.0.0.1:1/open/memory/v1")
	engine, err := NewCloudEngine(&config.Config{Mode: "cloud"}, SuiteLoCoMo, filepath.Join(t.TempDir(), "eval.json"))
	if err != nil {
		t.Fatal(err)
	}
	if NormalizePulseMode("") != PulseModeLocalPlain || engine.PulseMode != PulseModeLocalPlain ||
		TransportForPulseMode("") != LocalPlainTransport || engine.Client.Endpoint != "" {
		t.Fatalf("default mode must be local_plain and offline: %+v", engine.Metadata())
	}
}

func TestVerifyPrepareFailureDoesNotRetireGenerations(t *testing.T) {
	for _, suite := range []string{SuiteLoCoMo, SuiteATM} {
		t.Run(suite, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			t.Setenv("HOME", t.TempDir())
			t.Setenv("ROLLING_MEMORY_EVAL_PULSE_MODE", "")
			t.Setenv("ROLLING_MEMORY_USER_ID", "10")
			storePath := filepath.Join(t.TempDir(), "eval.json")
			engine, err := NewCloudEngine(&config.Config{Mode: "local"}, suite, storePath)
			if err != nil {
				t.Fatal(err)
			}
			oldStore := filepath.Join(t.TempDir(), "old.json")
			sentinel := oldStore + ".local-vault.sqlite"
			if err := os.WriteFile(sentinel, []byte("untouched"), 0o600); err != nil {
				t.Fatal(err)
			}
			gen := EvalGeneration{Scope: engine.Scope + "-old", Suite: suite, UserID: engine.UserID,
				Endpoint: engine.Endpoint, StorePath: oldStore, Role: evalGenerationRoleIdle,
				HeartbeatAt: time.Now().Add(-2 * evalGenerationLiveTTL).UTC().Format(time.RFC3339)}
			if err := SaveEvalGeneration(gen); err != nil {
				t.Fatal(err)
			}
			dataDir := buildTinyData(t)
			tier := "100"
			if suite == SuiteATM {
				dataDir, err = ResolveATMDataDir("")
				if err != nil {
					t.Skipf("bundled ATM dataset unavailable: %v", err)
				}
				tier = TierATMHard
			}
			_, err = Run(&config.Config{Mode: "local"}, Options{Suite: suite, Tier: tier, DataDir: dataDir,
				StorePath: storePath, OutDir: t.TempDir(), Answerer: "none", Judge: "off", NoVerify: true, VerifyPrepare: true})
			if err == nil {
				t.Fatal("verification without prepared data must fail")
			}
			if raw, err := os.ReadFile(sentinel); err != nil || string(raw) != "untouched" {
				t.Fatalf("verification retired existing generation: contents=%q err=%v", raw, err)
			}
			if _, ok := LoadEvalGeneration(gen.Scope); !ok {
				t.Fatal("verification removed existing generation lease")
			}
		})
	}
}

func TestVerifyPreparedRequiresExplicitMatchingPulseMode(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	t.Setenv("ROLLING_MEMORY_BRAIN_URL", server.URL+"/open/memory/v1")
	t.Setenv("ROLLING_MEMORY_BRAIN_KEY", "mem_test_mode_contract")
	documents := []CloudDocument{{ID: "doc-a", Text: "mode contract", OccurredAt: 1}}

	for _, mode := range []string{PulseModeLocalPlain, PulseModePulsePlain, PulseModePulseCipher} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			t.Setenv("HOME", t.TempDir())
			engine, err := NewCloudEngine(&config.Config{Mode: "cloud"}, SuiteLoCoMo,
				filepath.Join(t.TempDir(), "eval.json"), WithPulseMode(mode))
			if err != nil {
				t.Fatal(err)
			}
			digest, err := CloudCorpusDigest(documents)
			if err != nil {
				t.Fatal(err)
			}
			manifest := CloudManifest{Transport: TransportForPulseMode(mode), Scope: engine.Scope, Suite: engine.Suite, CorpusSHA256: digest, Documents: len(documents)}
			oldStore := filepath.Join(t.TempDir(), "old.json")
			sentinel := oldStore + ".local-vault.sqlite"
			if err := os.WriteFile(sentinel, []byte("untouched"), 0o600); err != nil {
				t.Fatal(err)
			}
			old := EvalGeneration{Scope: engine.Scope + "-old", Suite: engine.Suite, UserID: engine.UserID,
				Endpoint: engine.Endpoint, StorePath: oldStore, Role: evalGenerationRoleIdle,
				HeartbeatAt: time.Now().Add(-2 * evalGenerationLiveTTL).UTC().Format(time.RFC3339)}
			if err := SaveEvalGeneration(old); err != nil {
				t.Fatal(err)
			}
			if mode == PulseModeLocalPlain {
				prepared, err := engine.Ingest(t.Context(), documents, false, 1, nil)
				if err != nil {
					t.Fatal(err)
				}
				if verified, err := engine.VerifyPrepared(t.Context(), documents); err != nil || !verified.Reused {
					t.Fatalf("valid local manifest rejected: %+v, %v", verified, err)
				}
				if verified, err := engine.Prepare(t.Context(), documents, false, true, 1, nil); err != nil || !verified.Reused {
					t.Fatalf("valid local reuse rejected: %+v, %v", verified, err)
				}
				manifest = prepared
			}
			for _, manifestMode := range []string{"", "invalid", PulseModeLocalPlain, PulseModePulsePlain, PulseModePulseCipher} {
				if manifestMode == mode {
					continue
				}
				manifest.PulseMode = manifestMode
				if err := writeJSON(engine.StorePath+".manifest.json", manifest); err != nil {
					t.Fatal(err)
				}
				if _, err := engine.VerifyPrepared(t.Context(), documents); err == nil || !strings.Contains(err.Error(), "manifest pulse mode") {
					t.Fatalf("mode %q accepted manifest mode %q: %v", mode, manifestMode, err)
				}
				for _, reuse := range []bool{false, true} {
					if _, err := engine.Prepare(t.Context(), documents, false, reuse, 1, nil); err == nil || !strings.Contains(err.Error(), "manifest pulse mode") {
						t.Fatalf("prepare mode %q reuse=%v accepted manifest mode %q: %v", mode, reuse, manifestMode, err)
					}
				}
			}
			if raw, err := os.ReadFile(sentinel); err != nil || string(raw) != "untouched" {
				t.Fatalf("invalid manifest retired generation: contents=%q err=%v", raw, err)
			}
			if _, ok := LoadEvalGeneration(old.Scope); !ok {
				t.Fatal("invalid manifest removed generation lease")
			}
		})
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("invalid manifests triggered %d HTTP requests", got)
	}
}

func TestGenerationOwnershipAndSharedStoreProtection(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ROLLING_MEMORY_USER_ID", "10")

	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	t.Setenv("ROLLING_MEMORY_BRAIN_URL", server.URL+"/open/memory/v1")
	t.Setenv("ROLLING_MEMORY_BRAIN_KEY", "mem_test_owner")

	// 1. NewCloudEngineForScope rejects conflicting WithPulseMode before any HTTP traffic.
	if _, err := NewCloudEngineForScope(&config.Config{Mode: "cloud"},
		"u10-mem-eval-locomo-pulse-cipher-20261110t000000z-abcd",
		filepath.Join(t.TempDir(), "eval.json"),
		WithPulseMode(PulseModeLocalPlain)); err == nil {
		t.Fatal("expected conflicting scope/option pulse mode to be rejected")
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("conflicting scope/option triggered %d HTTP requests", got)
	}

	// 2. Non-owner finishGeneration must not mark another writer's active lease idle.
	sharedStore := filepath.Join(t.TempDir(), "shared.json")
	ownerEngine, err := NewCloudEngine(&config.Config{Mode: "local"}, SuiteLoCoMo, sharedStore, WithPulseMode(PulseModeLocalPlain))
	if err != nil {
		t.Fatal(err)
	}
	if err := ownerEngine.startNewGeneration(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	docs := []CloudDocument{{ID: "doc-1", Text: "shared store doc", OccurredAt: 1}}
	if _, err := ownerEngine.Ingest(t.Context(), docs, true, 1, nil); err != nil {
		t.Fatal(err)
	}
	borrower, err := NewCloudEngine(&config.Config{Mode: "local"}, SuiteLoCoMo, sharedStore, WithPulseMode(PulseModeLocalPlain))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := borrower.adoptStoredGeneration(); !ok {
		t.Fatal("expected borrower to adopt stored generation")
	}
	borrower.finishGeneration()
	lease, ok := LoadEvalGeneration(ownerEngine.Scope)
	if !ok || lease.Role != evalGenerationRoleWriter {
		t.Fatalf("non-owner finishGeneration altered active writer lease: %+v ok=%v", lease, ok)
	}

	// 3. ReconcileRetiredGenerations must not delete a SQLite file shared with the active store.
	sqlitePath := sharedStore + ".local-vault.sqlite"
	if _, err := ownerEngine.Recall(t.Context(), "shared", 5); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sqlitePath); err != nil {
		t.Fatalf("expected active sqlite vault to exist: %v", err)
	}
	retired := EvalGeneration{
		Scope:       ScopeForModeGeneration(SuiteLoCoMo, "10", PulseModeLocalPlain, "20261101t000000z-0001"),
		Generation:  "20261101t000000z-0001",
		Suite:       SuiteLoCoMo,
		UserID:      ownerEngine.UserID,
		Endpoint:    ownerEngine.Endpoint,
		StorePath:   sharedStore,
		Role:        evalGenerationRoleIdle,
		HeartbeatAt: time.Now().Add(-2 * evalGenerationLiveTTL).UTC().Format(time.RFC3339),
	}
	if err := SaveEvalGeneration(retired); err != nil {
		t.Fatal(err)
	}
	_ = ownerEngine.ReconcileRetiredGenerations(t.Context(), nil)
	if _, err := os.Stat(sqlitePath); err != nil {
		t.Fatalf("reconcile deleted SQLite file still shared with active generation: %v", err)
	}
}
