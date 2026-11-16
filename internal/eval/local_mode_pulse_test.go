// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package eval

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rocareer/rolling-memory/internal/config"
)

// TestNewCloudEngineLocalModeOnlyAllowsLocalPlain 锁定纯本地发布面：本地模式下评测只允许
// local_plain 臂；脉冲臂（pulse_plain / pulse_cipher）的协议、SDK 与引擎实现完整保留，
// 但必须显式切换到 cloud 模式才能运行（本地模式在解析端点/凭据之前就 fail-closed）。
func TestNewCloudEngineLocalModeOnlyAllowsLocalPlain(t *testing.T) {
	t.Setenv("ROLLING_MEMORY_EVAL_PULSE_MODE", "")
	t.Setenv("ROLLING_MEMORY_BRAIN_URL", "") // 纯本地：无显式云端覆盖
	storePath := filepath.Join(t.TempDir(), "eval-local-plain.json")

	engine, err := NewCloudEngine(&config.Config{Mode: "local"}, SuiteLoCoMo, storePath, WithPulseMode(PulseModeLocalPlain))
	if err != nil {
		t.Fatalf("local mode + local_plain must build: %v", err)
	}
	if engine.PulseMode != PulseModeLocalPlain {
		t.Fatalf("pulse mode = %q, want local_plain", engine.PulseMode)
	}
	if engine.Client.Endpoint != "" {
		t.Fatalf("local_plain client endpoint = %q, want empty (local vault)", engine.Client.Endpoint)
	}

	for _, mode := range []string{PulseModePulsePlain, PulseModePulseCipher} {
		_, err := NewCloudEngine(&config.Config{Mode: "local"}, SuiteLoCoMo, storePath, WithPulseMode(mode))
		if err == nil {
			t.Fatalf("local mode + %s must fail closed", mode)
		}
		if !strings.Contains(err.Error(), "本地模式仅支持 local_plain") {
			t.Fatalf("local mode + %s error = %v, want the explicit local-mode message", mode, err)
		}
	}
}

// TestNewCloudEngineLocalModeIgnoresRemoteCredentials locks the local no-egress boundary.
func TestNewCloudEngineLocalModeIgnoresRemoteCredentials(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	t.Setenv("ROLLING_MEMORY_EVAL_PULSE_MODE", "")
	t.Setenv("ROLLING_MEMORY_BRAIN_URL", server.URL+"/open/memory/v1")
	t.Setenv("ROLLING_MEMORY_BRAIN_KEY", "mem2.test-key")
	storePath := filepath.Join(t.TempDir(), "eval-local-plain.json")
	cfg := &config.Config{Mode: "local"}

	engine, err := NewCloudEngine(cfg, SuiteLoCoMo, storePath, WithPulseMode(PulseModeLocalPlain))
	if err != nil {
		t.Fatalf("local mode + local_plain: %v", err)
	}
	if engine.Client.Endpoint != "" {
		t.Fatalf("local_plain client endpoint = %q, want empty", engine.Client.Endpoint)
	}
	if err := engine.CheckHealth(context.Background()); err != nil {
		t.Fatalf("local health: %v", err)
	}
	if _, err := engine.Ingest(context.Background(), []CloudDocument{{ID: "local-1", Text: "local isolation sentinel", OccurredAt: 1}}, false, 1, nil); err != nil {
		t.Fatalf("local ingest: %v", err)
	}
	if _, err := engine.Recall(context.Background(), "local isolation sentinel", 1); err != nil {
		t.Fatalf("local recall: %v", err)
	}
	for _, mode := range []string{PulseModePulsePlain, PulseModePulseCipher} {
		if _, err := NewCloudEngine(cfg, SuiteLoCoMo, storePath, WithPulseMode(mode)); err == nil {
			t.Fatalf("local mode + %s must fail closed with remote credentials set", mode)
		}
		scope := ScopeForSuiteAndMode(SuiteLoCoMo, "10", mode)
		if err := engine.BindScope(scope); err == nil {
			t.Fatalf("local engine must reject rebinding to %s", mode)
		}
		if _, err := NewCloudEngineForScope(cfg, scope, storePath, WithPulseMode(PulseModeLocalPlain)); err == nil {
			t.Fatalf("local engine must reject %s scope even with a local option", mode)
		}
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("local mode made %d HTTP requests", got)
	}
}

// TestNewCloudEngineCloudModeStillAllowsPulse 是上面的正对照（防假绿）：
// cloud 模式下脉冲臂保持可用（协议与 SDK 实现未被删除），只是纯本地模式拒绝。
func TestNewCloudEngineCloudModeStillAllowsPulse(t *testing.T) {
	t.Setenv("ROLLING_MEMORY_EVAL_PULSE_MODE", "")
	t.Setenv("ROLLING_MEMORY_BRAIN_URL", "http://127.0.0.1:9/open/memory/v1")
	t.Setenv("ROLLING_MEMORY_BRAIN_KEY", "mem2.test-key")
	storePath := filepath.Join(t.TempDir(), "eval-pulse-cipher.json")

	engine, err := NewCloudEngine(&config.Config{Mode: "cloud"}, SuiteLoCoMo, storePath, WithPulseMode(PulseModePulseCipher))
	if err != nil {
		t.Fatalf("cloud mode + pulse_cipher must build: %v", err)
	}
	if engine.PulseMode != PulseModePulseCipher {
		t.Fatalf("pulse mode = %q, want pulse_cipher", engine.PulseMode)
	}
	if !strings.Contains(engine.Endpoint, "/open/memory/v1") {
		t.Fatalf("cloud pulse endpoint = %q, want the configured data plane", engine.Endpoint)
	}
}
