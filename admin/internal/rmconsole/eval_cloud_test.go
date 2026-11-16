// plugin: rolling-memory v0.0.58
package rollingmemory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func cloudTestManifestMap(suite, scope string) map[string]any {
	return map[string]any{
		"transport":      evalCloudTransport,
		"endpoint":       "http://127.0.0.1:18081/open/memory/v1",
		"tenant_id":      "user",
		"user_id":        "10",
		"scope":          scope,
		"suite":          suite,
		"pulse_mode":     EvalPulseModePulseCipher,
		"corpus_sha256":  strings.Repeat("a", 64),
		"documents":      3,
		"local_fallback": false,
	}
}

func writeCloudTestManifest(t *testing.T, storePath, suite, scope string, mutate func(map[string]any)) {
	t.Helper()
	manifest := cloudTestManifestMap(suite, scope)
	if mutate != nil {
		mutate(manifest)
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(storePath), 0o755); err != nil {
		t.Fatalf("mkdir manifest: %v", err)
	}
	if err := os.WriteFile(storePath+".manifest.json", raw, 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

func cloudTestRecallPayload(scope, unitID string) map[string]any {
	return map[string]any{
		"raw_hits": []any{
			map[string]any{
				"unit_id": unitID,
				"session": unitID,
				"trace":   unitID,
				"text":    "cloud evidence",
			},
		},
		"bands":              []any{},
		"formatted":          []string{"cloud evidence"},
		"retrieval_unit_ids": []string{unitID},
		"meta": map[string]any{
			"engine_transport": evalCloudTransport,
			"pulse_mode":       EvalPulseModePulseCipher,
			"engine_scope":     scope,
			"engine_upstream":  "http://127.0.0.1:18081/open/memory/v1",
			"tenant_id":        "user",
			"user_id":          "10",
			"local_fallback":   false,
			"cloud_hits":       1,
			"transport":        evalCloudTransport,
			"endpoint":         "http://127.0.0.1:18081/open/memory/v1",
			"scope":            scope,
		},
	}
}

func cloudTestVerificationPayload(scope string) map[string]any {
	return map[string]any{
		"verified": true, "transport": evalCloudTransport, "scope": scope, "local_fallback": false,
		"engine": map[string]any{
			"tenant_id": "user", "user_id": "10", "endpoint": "http://127.0.0.1:18081/open/memory/v1", "pulse_mode": EvalPulseModePulseCipher,
		},
	}
}

func cloudTestJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	return raw
}

func slicesContain(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func installCloudTestCommand(t *testing.T, command func(context.Context, string, ...string) ([]byte, error)) {
	t.Helper()
	previous := cloudEvalCommand
	cloudEvalCommand = command
	t.Cleanup(func() { cloudEvalCommand = previous })
}

func TestEvalCloudStoreIsolation(t *testing.T) {
	home := t.TempDir()
	locomo := resolveEvalDBPathForSuite(home, SuiteLoCoMo)
	atm := resolveEvalDBPathForSuite(home, SuiteATM)
	if locomo == atm {
		t.Fatal("LoCoMo and ATM must use different stores")
	}
	if filepath.Base(locomo) != "eval-locomo-local-plain.json" || filepath.Base(atm) != "eval-atm-local-plain.json" {
		t.Fatalf("unexpected store names: %s / %s", locomo, atm)
	}
	if EvalScope != "u10-mem-eval-locomo" || ATMEvalScope != "u10-mem-eval-atmbench" {
		t.Fatalf("unexpected cloud scopes: %s / %s", EvalScope, ATMEvalScope)
	}
	if EvalScope == UserScope || ATMEvalScope == UserScope {
		t.Fatal("evaluation scope must never be the real user scope")
	}
}

func TestCloudEvalManifestValidation(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		storePath := filepath.Join(t.TempDir(), "eval-locomo.json")
		writeCloudTestManifest(t, storePath, SuiteLoCoMo, EvalScope, nil)
		manifest, err := readCloudEvalManifest(storePath, SuiteLoCoMo)
		if err != nil {
			t.Fatalf("valid manifest rejected: %v", err)
		}
		if manifest.LocalFallback == nil || *manifest.LocalFallback || manifest.Documents != 3 {
			t.Fatalf("unexpected manifest: %#v", manifest)
		}
	})

	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "missing fallback", mutate: func(m map[string]any) { delete(m, "local_fallback") }},
		{name: "true fallback", mutate: func(m map[string]any) { m["local_fallback"] = true }},
		{name: "wrong transport", mutate: func(m map[string]any) { m["transport"] = "local" }},
		{name: "wrong suite", mutate: func(m map[string]any) { m["suite"] = SuiteATM }},
		{name: "wrong scope", mutate: func(m map[string]any) { m["scope"] = UserScope }},
		{name: "wrong endpoint", mutate: func(m map[string]any) { m["endpoint"] = "http://localhost/legacy" }},
		{name: "missing tenant", mutate: func(m map[string]any) { delete(m, "tenant_id") }},
		{name: "wrong user", mutate: func(m map[string]any) { m["user_id"] = "11" }},
		{name: "wrong tenant", mutate: func(m map[string]any) { m["tenant_id"] = "other" }},
		{name: "short digest", mutate: func(m map[string]any) { m["corpus_sha256"] = "not-a-sha" }},
		{name: "empty content digest", mutate: func(m map[string]any) {
			m["corpus_sha256"] = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			storePath := filepath.Join(t.TempDir(), "eval-locomo.json")
			writeCloudTestManifest(t, storePath, SuiteLoCoMo, EvalScope, tc.mutate)
			if _, err := readCloudEvalManifest(storePath, SuiteLoCoMo); err == nil {
				t.Fatal("invalid manifest was accepted")
			}
		})
	}

	missing := filepath.Join(t.TempDir(), "missing.json")
	if _, err := readCloudEvalManifest(missing, SuiteLoCoMo); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing manifest error = %v, want os.ErrNotExist", err)
	}
}

func TestEvalManifestAndVerificationModeIsolation(t *testing.T) {
	cases := []struct {
		mode      string
		transport string
		endpoint  string
	}{
		{EvalPulseModeLocalPlain, evalLocalPlainTransport, "local://sqlite-fts5-bm25"},
		{EvalPulseModePulsePlain, evalPulsePlainTransport, "http://127.0.0.1:18081/open/memory/v1"},
		{EvalPulseModePulseCipher, evalCloudTransport, "http://127.0.0.1:18081/open/memory/v1"},
	}
	for _, suite := range []string{SuiteLoCoMo, SuiteATM} {
		for _, tc := range cases {
			t.Run(suite+"/"+tc.mode, func(t *testing.T) {
				storePath := resolveEvalDBPathForSuiteAndMode(t.TempDir(), suite, tc.mode)
				scope := evalScopeForSuiteAndMode(suite, tc.mode) + "-generation-1"
				write := func(mutate func(map[string]any)) {
					writeCloudTestManifest(t, storePath, suite, scope, func(m map[string]any) {
						m["transport"], m["endpoint"], m["pulse_mode"] = tc.transport, tc.endpoint, tc.mode
						if mutate != nil {
							mutate(m)
						}
					})
				}
				write(nil)
				if _, err := readCloudEvalManifest(storePath, suite); err != nil {
					t.Fatalf("valid manifest rejected: %v", err)
				}
				for _, invalid := range []struct {
					name   string
					mutate func(map[string]any)
				}{
					{"unknown mode", func(m map[string]any) { m["pulse_mode"] = "invalid" }},
					{"empty transport", func(m map[string]any) { m["transport"] = "" }},
					{"empty scope", func(m map[string]any) { m["scope"] = "" }},
					{"wrong suite scope", func(m map[string]any) {
						otherSuite := SuiteATM
						if suite == SuiteATM {
							otherSuite = SuiteLoCoMo
						}
						m["scope"] = evalScopeForSuiteAndMode(otherSuite, tc.mode)
					}},
					{"empty endpoint", func(m map[string]any) { m["endpoint"] = "" }},
					{"invalid endpoint", func(m map[string]any) { m["endpoint"] = "ftp://example.invalid/open/memory/v1" }},
				} {
					write(invalid.mutate)
					if _, err := readCloudEvalManifest(storePath, suite); err == nil {
						t.Errorf("accepted %s", invalid.name)
					}
				}
				for _, mutate := range []func(map[string]any){
					func(m map[string]any) { m["pulse_mode"] = "" },
					func(m map[string]any) { delete(m, "pulse_mode") },
				} {
					write(mutate)
					if _, err := readCloudEvalManifest(storePath, suite); err == nil {
						t.Fatal("accepted empty or missing pulse_mode")
					}
				}
				write(nil)
				args := buildCloudEvalVerifyArgs(suite, "hard", storePath, t.TempDir())
				if !slicesContain(args, "--pulse-mode") || !slicesContain(args, tc.mode) {
					t.Fatalf("verify args do not select %s: %v", tc.mode, args)
				}
				installCloudTestCommand(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
					if !slicesContain(args, "--verify-prepare") || !slicesContain(args, tc.mode) {
						return nil, fmt.Errorf("unexpected command: %v", args)
					}
					payload := cloudTestVerificationPayload(scope)
					payload["transport"] = tc.transport
					payload["engine"].(map[string]any)["endpoint"] = tc.endpoint
					payload["engine"].(map[string]any)["pulse_mode"] = tc.mode
					return cloudTestJSON(t, payload), nil
				})
				if readiness, err := probeCloudEvalStore(context.Background(), "fake-rmem", storePath, suite, "", nil); err != nil || !readiness.Ready {
					t.Fatalf("mode-specific verification failed: readiness=%#v err=%v", readiness, err)
				}
				for _, other := range cases {
					if other.mode == tc.mode {
						continue
					}
					for name, mutate := range map[string]func(map[string]any){
						"transport":  func(m map[string]any) { m["transport"] = other.transport },
						"pulse_mode": func(m map[string]any) { m["pulse_mode"] = other.mode },
						"scope":      func(m map[string]any) { m["scope"] = evalScopeForSuiteAndMode(suite, other.mode) + "-generation-1" },
					} {
						write(mutate)
						if _, err := readCloudEvalManifest(storePath, suite); err == nil {
							t.Errorf("accepted %s from %s", name, other.mode)
						}
					}
				}
				wrongEndpoint := "local://sqlite-fts5-bm25"
				if tc.mode == EvalPulseModeLocalPlain {
					wrongEndpoint = "http://127.0.0.1:18081/open/memory/v1"
				}
				write(func(m map[string]any) { m["endpoint"] = wrongEndpoint })
				if _, err := readCloudEvalManifest(storePath, suite); err == nil {
					t.Fatal("accepted endpoint from another mode")
				}
				verification := cloudTestVerificationPayload(scope)
				verification["transport"] = tc.transport
				verification["engine"].(map[string]any)["endpoint"] = tc.endpoint
				verification["engine"].(map[string]any)["pulse_mode"] = tc.mode
				if err := validateCloudPrepareVerification(verification, scope); err != nil {
					t.Fatalf("valid verification rejected: %v", err)
				}
				verification["engine"].(map[string]any)["endpoint"] = wrongEndpoint
				if err := validateCloudPrepareVerification(verification, scope); err == nil {
					t.Fatal("accepted mismatched verification endpoint")
				}
				verification["engine"].(map[string]any)["endpoint"] = tc.endpoint
				for _, transport := range []string{"", "invalid"} {
					verification["transport"] = transport
					if err := validateCloudPrepareVerification(verification, scope); err == nil {
						t.Errorf("accepted verification transport %q", transport)
					}
				}
				verification["transport"] = tc.transport
				for _, invalidScope := range []string{"", UserScope, scope + "-other"} {
					verification["scope"] = invalidScope
					if err := validateCloudPrepareVerification(verification, scope); err == nil {
						t.Errorf("accepted verification scope %q", invalidScope)
					}
				}
			})
		}
	}
}

func TestEvalRecallAndVerificationPulseModeIsolation(t *testing.T) {
	cases := []struct {
		mode      string
		transport string
		endpoint  string
	}{
		{EvalPulseModeLocalPlain, evalLocalPlainTransport, "local://sqlite-fts5-bm25"},
		{EvalPulseModePulsePlain, evalPulsePlainTransport, "http://127.0.0.1:18081/open/memory/v1"},
		{EvalPulseModePulseCipher, evalCloudTransport, "http://127.0.0.1:18081/open/memory/v1"},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			scope := evalScopeForSuiteAndMode(SuiteLoCoMo, tc.mode) + "-generation-1"
			recall := cloudTestRecallPayload(scope, "unit-1")
			meta := recall["meta"].(map[string]any)
			meta["engine_transport"], meta["engine_upstream"], meta["pulse_mode"] = tc.transport, tc.endpoint, tc.mode
			verification := cloudTestVerificationPayload(scope)
			verification["transport"] = tc.transport
			engine := verification["engine"].(map[string]any)
			engine["endpoint"], engine["pulse_mode"] = tc.endpoint, tc.mode
			for _, check := range []struct {
				name     string
				metadata map[string]any
				validate func() error
			}{
				{"recall", meta, func() error { return validateCloudRecallMeta(recall, scope) }},
				{"verification", engine, func() error { return validateCloudPrepareVerification(verification, scope) }},
			} {
				t.Run(check.name, func(t *testing.T) {
					if err := check.validate(); err != nil {
						t.Fatalf("valid mode rejected: %v", err)
					}
					for _, invalid := range []any{nil, "", "invalid", " " + tc.mode, 1} {
						check.metadata["pulse_mode"] = invalid
						if err := check.validate(); err == nil {
							t.Errorf("accepted pulse_mode %#v", invalid)
						}
					}
					delete(check.metadata, "pulse_mode")
					if err := check.validate(); err == nil {
						t.Error("accepted missing pulse_mode")
					}
					for _, other := range cases {
						if other.mode == tc.mode {
							continue
						}
						check.metadata["pulse_mode"] = other.mode
						if err := check.validate(); err == nil {
							t.Errorf("accepted pulse_mode %q", other.mode)
						}
					}
				})
			}
		})
	}
}

func TestCloudRecallMetaFailClosed(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "missing meta", mutate: func(p map[string]any) { delete(p, "meta") }},
		{name: "missing transport", mutate: func(p map[string]any) { delete(p["meta"].(map[string]any), "engine_transport") }},
		{name: "wrong scope", mutate: func(p map[string]any) { p["meta"].(map[string]any)["engine_scope"] = UserScope }},
		{name: "wrong upstream", mutate: func(p map[string]any) { p["meta"].(map[string]any)["engine_upstream"] = "http://localhost/legacy" }},
		{name: "missing tenant", mutate: func(p map[string]any) { delete(p["meta"].(map[string]any), "tenant_id") }},
		{name: "wrong tenant", mutate: func(p map[string]any) { p["meta"].(map[string]any)["tenant_id"] = "other" }},
		{name: "missing user", mutate: func(p map[string]any) { delete(p["meta"].(map[string]any), "user_id") }},
		{name: "missing fallback", mutate: func(p map[string]any) { delete(p["meta"].(map[string]any), "local_fallback") }},
		{name: "true fallback", mutate: func(p map[string]any) { p["meta"].(map[string]any)["local_fallback"] = true }},
		{name: "non bool fallback", mutate: func(p map[string]any) { p["meta"].(map[string]any)["local_fallback"] = "false" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := cloudTestRecallPayload(EvalScope, "unit-1")
			tc.mutate(payload)
			if err := validateCloudRecallMeta(payload, EvalScope); err == nil {
				t.Fatal("invalid recall metadata was accepted")
			}
		})
	}
	if err := validateCloudRecallMeta(cloudTestRecallPayload(EvalScope, "unit-1"), EvalScope); err != nil {
		t.Fatalf("valid metadata rejected: %v", err)
	}
}

func TestCloudRecallRejectsUnknownATMUnit(t *testing.T) {
	// scope 取本 store manifest 的世代 scope（每次新评测自动分配独立 scope）。
	storePath := filepath.Join(t.TempDir(), "eval-atm.json")
	scope := "u10-mem-eval-atmbench-20261107t153012z-7f3c"
	writeEvalCloudTestManifest(t, storePath, scope, SuiteATM)
	installCloudTestCommand(t, func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return cloudTestJSON(t, cloudTestRecallPayload(scope, "foreign-unit")), nil
	})
	_, _, err := runStrictCloudEvalRecall(context.Background(), "fake-rmem", storePath, SuiteATM, "question", 10, map[string]atmEvalUnit{
		"known-unit": {ID: "known-unit"},
	})
	if err == nil || !strings.Contains(err.Error(), "未知 ATM UnitID") {
		t.Fatalf("unknown ATM UnitID error = %v", err)
	}
}

func TestCloudEvalLegacyArmRejected(t *testing.T) {
	for _, arm := range []string{"raw", "rule", "noetic", "local", "zero", "pro", "max", "x"} {
		if _, err := normalizeEvalArm(arm); err == nil {
			t.Errorf("legacy arm %q was accepted", arm)
		}
	}
	for _, arm := range []string{"", EvalArmSDKPulseCipher, EvalArmOracle} {
		if _, err := normalizeEvalArm(arm); err != nil {
			t.Errorf("valid arm %q rejected: %v", arm, err)
		}
	}
	if _, err := validateEvalRunRequest(evalRunRequest{ArmMode: EvalArmSDKPulseCipher, Profile: "zero"}); err == nil {
		t.Fatal("legacy profile was accepted")
	}
	var req evalRunRequest
	if err := json.Unmarshal([]byte(`{"arm_mode":"sdk-pulse-cipher","noetic_mode":"standalone"}`), &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if _, err := validateEvalRunRequest(req); err == nil {
		t.Fatal("legacy noetic mode was accepted")
	}
}

func TestNormalizeEvalRunRequestBindsTopKAndIsolatedStore(t *testing.T) {
	home := t.TempDir()
	expected := resolveEvalDBPathForSuite(home, SuiteLoCoMo)
	req := evalRunRequest{Suite: SuiteLoCoMo, TopK: 37, RawLimit: 5, EvalStore: expected}
	if err := normalizeEvalRunRequest(home, &req); err != nil {
		t.Fatalf("valid top_k/store rejected: %v", err)
	}
	if req.RawLimit != 37 {
		t.Fatalf("top_k did not reach execution raw limit: %d", req.RawLimit)
	}
	req.EvalStore = filepath.Join(home, "memory.db")
	if err := normalizeEvalRunRequest(home, &req); err == nil {
		t.Fatal("non-isolated eval_store was accepted")
	}
}

func TestCloudPrepareVerificationFailClosed(t *testing.T) {
	valid := cloudTestVerificationPayload(EvalScope)
	if err := validateCloudPrepareVerification(valid, EvalScope); err != nil {
		t.Fatalf("valid verification rejected: %v", err)
	}
	for _, mutate := range []func(map[string]any){
		func(p map[string]any) { p["verified"] = false },
		func(p map[string]any) { p["scope"] = UserScope },
		func(p map[string]any) { p["local_fallback"] = true },
		func(p map[string]any) { p["engine"].(map[string]any)["tenant_id"] = "other" },
	} {
		payload := cloudTestVerificationPayload(EvalScope)
		mutate(payload)
		if err := validateCloudPrepareVerification(payload, EvalScope); err == nil {
			t.Fatal("invalid verification was accepted")
		}
	}
}

func TestEvalRunNormalizesRequestBeforeStart(t *testing.T) {
	raw, err := os.ReadFile("eval.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	start := strings.Index(source, "func evalRunHandler(")
	end := strings.Index(source[start:], "startStreamingEvalJob(home, req)")
	if start < 0 || end < 0 || !strings.Contains(source[start:start+end], "normalizeEvalRunRequest(home, &req)") {
		t.Fatal("evalRunHandler must normalize top_k/eval_store before starting the job")
	}
	if !strings.Contains(source, `action != "prepare" && action != "verify" && action != "rebuild"`) {
		t.Fatal("Verify action must be accepted by evalIngestHandler")
	}
}

func TestCloudEvalReproAndPrepareFlags(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "eval-locomo.json")
	commands := []string{
		buildReproCLICommand("100", EvalArmSDKPulseCipher, storePath, 20, false),
		buildATMReproCLICommand("hard", EvalArmOracle, storePath, 50, true),
	}
	for _, command := range commands {
		for _, required := range []string{"--eval-store", "--top-k"} {
			if !strings.Contains(command, required) {
				t.Errorf("command %q lacks %s", command, required)
			}
		}
		for _, retired := range []string{"--eval-db", "--retrieval", "--mode", "--raw-limit", "--caps-"} {
			if strings.Contains(command, retired) {
				t.Errorf("command %q emits retired flag %s", command, retired)
			}
		}
	}
	atmArgs := buildCloudEvalPrepareArgs(SuiteATM, "hard", storePath, t.TempDir(), true)
	joined := strings.Join(atmArgs, " ")
	for _, required := range []string{"--suite atm", "--tier hard", "--rebuild", "--eval-store"} {
		if !strings.Contains(joined, required) {
			t.Errorf("prepare args %q lack %q", joined, required)
		}
	}
}

func TestPrepareCloudEvalStoreFastPathAndSingleFlight(t *testing.T) {
	t.Run("valid manifest skips ingest", func(t *testing.T) {
		storePath := filepath.Join(t.TempDir(), "eval-locomo.json")
		writeCloudTestManifest(t, storePath, SuiteLoCoMo, EvalScope, nil)
		var mu sync.Mutex
		evalCalls := 0
		installCloudTestCommand(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if args[0] == "eval" && slicesContain(args, "--verify-prepare") {
				return cloudTestJSON(t, cloudTestVerificationPayload(EvalScope)), nil
			}
			if args[0] == "eval" {
				mu.Lock()
				evalCalls++
				mu.Unlock()
				return nil, errors.New("unexpected ingest")
			}
			return cloudTestJSON(t, cloudTestRecallPayload(EvalScope, "unit-1")), nil
		})
		readiness, err := prepareCloudEvalStore(context.Background(), "fake-rmem", t.TempDir(), SuiteLoCoMo, "100", storePath, t.TempDir(), "question", nil, false)
		if err != nil || !readiness.Ready {
			t.Fatalf("fast-path readiness = %#v, err = %v", readiness, err)
		}
		mu.Lock()
		defer mu.Unlock()
		if evalCalls != 0 {
			t.Fatalf("fast-path ingest calls = %d, want 0", evalCalls)
		}
	})

	t.Run("missing manifest prepares once under lock", func(t *testing.T) {
		storePath := filepath.Join(t.TempDir(), "eval-locomo.json")
		dataDir := t.TempDir()
		var mu sync.Mutex
		evalCalls := 0
		installCloudTestCommand(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if args[0] == "eval" && slicesContain(args, "--verify-prepare") {
				return cloudTestJSON(t, cloudTestVerificationPayload(EvalScope)), nil
			}
			if args[0] == "eval" {
				mu.Lock()
				evalCalls++
				mu.Unlock()
				time.Sleep(25 * time.Millisecond)
				manifest := cloudTestManifestMap(SuiteLoCoMo, EvalScope)
				raw, err := json.Marshal(manifest)
				if err != nil {
					return nil, err
				}
				if err := os.MkdirAll(filepath.Dir(storePath), 0o755); err != nil {
					return nil, err
				}
				if err := os.WriteFile(storePath+".manifest.json", raw, 0o600); err != nil {
					return nil, err
				}
				return []byte(`{"ok":true}`), nil
			}
			if args[0] == "recall" {
				return cloudTestJSON(t, cloudTestRecallPayload(EvalScope, "unit-1")), nil
			}
			return nil, fmt.Errorf("unexpected command: %v", args)
		})

		var wg sync.WaitGroup
		errs := make(chan error, 2)
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := prepareCloudEvalStore(context.Background(), "fake-rmem", t.TempDir(), SuiteLoCoMo, "100", storePath, dataDir, "question", nil, false)
				errs <- err
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("concurrent prepare failed: %v", err)
			}
		}
		mu.Lock()
		defer mu.Unlock()
		if evalCalls != 1 {
			t.Fatalf("concurrent prepare ingest calls = %d, want 1", evalCalls)
		}
	})
}

func TestPrepareCloudEvalStoreNetworkErrorDoesNotReingest(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "eval-locomo.json")
	writeCloudTestManifest(t, storePath, SuiteLoCoMo, EvalScope, nil)
	evalCalls := 0
	installCloudTestCommand(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "eval" && slicesContain(args, "--verify-prepare") {
			return nil, errors.New("upstream unavailable")
		}
		if args[0] == "eval" {
			evalCalls++
		}
		return nil, errors.New("upstream unavailable")
	})
	_, err := prepareCloudEvalStore(context.Background(), "fake-rmem", t.TempDir(), SuiteLoCoMo, "100", storePath, t.TempDir(), "question", nil, false)
	if err == nil || !strings.Contains(err.Error(), "未触发重灌") {
		t.Fatalf("network error = %v, want explicit no-reingest failure", err)
	}
	if evalCalls != 0 {
		t.Fatalf("network error triggered %d ingest calls", evalCalls)
	}
}

func TestVerifyActionDoesNotPrepareWhenManifestMissing(t *testing.T) {
	// 该用例走真实 handler，而 handler 用 os.UserHomeDir() 解析 store/data 路径；
	// 不隔离 HOME 时，一旦本机存在 eval-locomo.json.manifest.json（真实运行或并行工作流产生），
	// 用例就会读到宿主状态而假红。固定到临时 HOME，保证「缺 manifest」前提成立。
	t.Setenv("HOME", t.TempDir())
	gin.SetMode(gin.TestMode)
	home := t.TempDir()
	storePath := resolveEvalDBPathForSuite(home, SuiteLoCoMo)
	calls := 0
	installCloudTestCommand(t, func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		calls++
		return nil, errors.New("unexpected prepare")
	})
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/eval/ingest", bytes.NewBufferString(`{"suite":"locomo","action":"verify"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	evalIngestHandler(c)
	if calls != 0 {
		t.Fatalf("verify with missing manifest triggered %d prepare calls", calls)
	}
	var receipt map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if code, ok := receipt["code"].(float64); !ok || code == 0 {
		t.Fatalf("verify with missing manifest must fail closed: %s", recorder.Body.String())
	}
	_ = storePath
}

func TestReadinessCacheIncludesEndpoint(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "eval-locomo.json")
	writeCloudTestManifest(t, storePath, SuiteLoCoMo, EvalScope, nil)
	evalReadinessCache = map[string]evalReadinessCacheEntry{}
	t.Cleanup(func() { evalReadinessCache = map[string]evalReadinessCacheEntry{} })
	calls := 0
	installCloudTestCommand(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if slicesContain(args, "--verify-prepare") {
			calls++
			return cloudTestJSON(t, cloudTestVerificationPayload(EvalScope)), nil
		}
		return nil, errors.New("unexpected command")
	})
	if readiness, err := inspectCloudEvalReadiness(context.Background(), "fake-rmem", storePath, SuiteLoCoMo, "memory", nil, false); err != nil || !readiness.Ready {
		t.Fatalf("initial readiness = %#v err=%v", readiness, err)
	}
	writeCloudTestManifest(t, storePath, SuiteLoCoMo, EvalScope, func(m map[string]any) {
		m["endpoint"] = "http://127.0.0.1:18082/open/memory/v1"
	})
	if readiness, err := inspectCloudEvalReadiness(context.Background(), "fake-rmem", storePath, SuiteLoCoMo, "memory", nil, false); err == nil || readiness.Ready {
		t.Fatalf("mismatched verification endpoint must fail closed: readiness=%#v err=%v", readiness, err)
	}
	if calls != 2 {
		t.Fatalf("endpoint change reused stale cache: verify calls = %d, want 2", calls)
	}
}

func TestSingleRequiresPreparation(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "eval-locomo.json")
	called := false
	installCloudTestCommand(t, func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		called = true
		return nil, errors.New("unexpected command")
	})
	_, err := requireCloudEvalManifestForSingle(storePath, SuiteLoCoMo)
	if err == nil || !strings.Contains(err.Error(), "single 模式不会自动 ingest") {
		t.Fatalf("single preparation error = %v", err)
	}
	if called {
		t.Fatal("single mode attempted an unexpected ingest or recall without a manifest")
	}
}
