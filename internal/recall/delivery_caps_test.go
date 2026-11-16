// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Delivery cap configuration verification:
// 交付帽原为硬编码（每带链数 ≤8、每链行数 ≤12）；现将两顶帽改为
// [recall] chains / rows 配置（缺省 8 / 12 ⇒ 行为不变，金标安全）。
package recall_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rocareer/rolling-memory/internal/api"
	"github.com/rocareer/rolling-memory/internal/clock"
	"github.com/rocareer/rolling-memory/internal/config"
)

func newMemoryWithToml(t *testing.T, toml string) *api.Memory {
	t.Helper()
	restore := clock.SetForTest(time.Unix(testNow, 0), 1_000_000_000)
	t.Cleanup(restore)
	directory := t.TempDir()
	dbPath := filepath.Join(directory, "memory.db")
	configPath := filepath.Join(directory, "config.toml")
	if err := os.WriteFile(configPath, []byte(toml), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	mode := "local"
	scope := "mem:test"
	reader := "deliverable"
	cfg, err := config.Load(config.LoadOptions{
		Path: &configPath, DB: &dbPath, Mode: &mode, Scope: &scope, Reader: &reader,
		Env: map[string]string{},
	})
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	cfg.Log = filepath.Join(directory, "state", "rolling-memory.log")
	memory, err := api.New(cfg)
	if err != nil {
		t.Fatalf("api: %v", err)
	}
	t.Cleanup(func() { memory.Close() })
	return memory
}

func bandFor(t *testing.T, bands []map[string]any, titlePart string) map[string]any {
	t.Helper()
	for _, band := range bands {
		if title, _ := band["title"].(string); strings.Contains(title, titlePart) {
			return band
		}
	}
	t.Fatalf("band %q not found in %d bands", titlePart, len(bands))
	return nil
}

func TestDeliveryChainCapParameterised(t *testing.T) {
	facts := func(memory *api.Memory) {
		writeFact(t, memory, "老张养了猫。", "老张", "养", map[string]any{"label": "猫"}, nil)
		writeFact(t, memory, "老张养了狗。", "老张", "养", map[string]any{"label": "狗"}, nil)
		writeFact(t, memory, "老张养了鱼。", "老张", "养", map[string]any{"label": "鱼"}, nil)
	}

	capped := newMemoryWithToml(t, "[recall]\nchains = 1\n")
	facts(capped)
	result, err := capped.Recall(api.RecallRequest{Cue: "老张"})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if chains := bandFor(t, result.Bands, "老张")["chains"].([]any); len(chains) != 1 {
		t.Fatalf("chains = %d, want 1 with recall.chains=1", len(chains))
	}

	plain := newMemory(t)
	facts(plain)
	def, err := plain.Recall(api.RecallRequest{Cue: "老张"})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if chains := bandFor(t, def.Bands, "老张")["chains"].([]any); len(chains) < 2 {
		t.Fatalf("default chains = %d, want >= 2 (default cap keeps them)", len(chains))
	}
}

func TestDeliveryRowCapParameterised(t *testing.T) {
	facts := func(memory *api.Memory) {
		writeFact(t, memory, "老张养了猫。", "老张", "养", map[string]any{"label": "猫"}, nil)
		writeFact(t, memory, "老张喜欢猫。", "老张", "喜欢", map[string]any{"label": "猫"}, nil)
		writeFact(t, memory, "老张抱了猫。", "老张", "抱", map[string]any{"label": "猫"}, nil)
	}
	catChain := func(t *testing.T, band map[string]any) []map[string]any {
		t.Helper()
		for _, value := range band["chains"].([]any) {
			chain := value.(map[string]any)
			if label, _ := chain["label"].(string); strings.Contains(label, "猫") {
				return chain["nodes"].([]map[string]any)
			}
		}
		t.Fatalf("cat chain not found in band %#v", band)
		return nil
	}

	capped := newMemoryWithToml(t, "[recall]\nrows = 2\n")
	facts(capped)
	result, err := capped.Recall(api.RecallRequest{Cue: "老张 猫"})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if nodes := catChain(t, bandFor(t, result.Bands, "老张")); len(nodes) != 2 {
		t.Fatalf("chain rows = %d, want 2 with recall.rows=2", len(nodes))
	}

	plain := newMemory(t)
	facts(plain)
	def, err := plain.Recall(api.RecallRequest{Cue: "老张 猫"})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if nodes := catChain(t, bandFor(t, def.Bands, "老张")); len(nodes) != 3 {
		t.Fatalf("default chain rows = %d, want 3 (default cap keeps them)", len(nodes))
	}
	one := 1
	reqCapped, err := plain.Recall(api.RecallRequest{
		Cue: "老张 猫", CapsChains: &one, CapsRows: &one,
	})
	if err != nil {
		t.Fatalf("request-capped recall: %v", err)
	}
	b := bandFor(t, reqCapped.Bands, "老张")
	if chains := b["chains"].([]any); len(chains) != 1 {
		t.Fatalf("request CapsChains=1 got %d chains, want 1", len(chains))
	}
	if nodes := catChain(t, b); len(nodes) != 1 {
		t.Fatalf("request CapsRows=1 got %d rows, want 1", len(nodes))
	}
}
