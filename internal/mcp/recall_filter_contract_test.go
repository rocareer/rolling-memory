// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package mcp

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/api"
	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/db"
	"github.com/rocareer/rolling-memory/internal/version"
)

func TestRecallFilterSchemaReportsFailClosed(t *testing.T) {
	properties := recallTool["inputSchema"].(map[string]any)["properties"].(map[string]any)
	for _, name := range []string{"session", "asof", "window"} {
		field := properties[name].(map[string]any)
		description, _ := field["description"].(string)
		if !strings.Contains(description, "fail closed") {
			t.Fatalf("%s schema must disclose unsupported filtering: %q", name, description)
		}
	}
}

func TestRecallSessionPresence(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
		want *string
	}{
		{name: "omitted", args: map[string]any{}},
		{name: "null", args: map[string]any{"session": nil}},
		{name: "empty", args: map[string]any{"session": ""}, want: stringPointer("")},
		{name: "named", args: map[string]any{"session": "one"}, want: stringPointer("one")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := recallRequest(tc.args)
			if err != nil {
				t.Fatal(err)
			}
			if (req.Session == nil) != (tc.want == nil) || (req.Session != nil && *req.Session != *tc.want) {
				t.Fatalf("session = %#v, want %#v", req.Session, tc.want)
			}
		})
	}
}

func newVaultOnlyMemory(t *testing.T) *api.Memory {
	t.Helper()
	directory := t.TempDir()
	dbPath := filepath.Join(directory, "memory.db")
	configPath := filepath.Join(directory, "config.toml")
	connection, err := db.Connect(dbPath, db.LocalProfile())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(fmt.Sprintf("PRAGMA user_version = %d", version.SchemaVersion)); err != nil {
		connection.Close()
		t.Fatal(err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	mode, scope, reader := "local", "mem:test", "deliverable"
	cfg, err := config.Load(config.LoadOptions{
		Path: &configPath, DB: &dbPath, Mode: &mode, Scope: &scope, Reader: &reader,
		Env: map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Log = filepath.Join(directory, "state", "rolling-memory.log")
	memory, err := api.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { memory.Close() })
	if memory.Store.HasLegacyGraph() {
		t.Fatal("vault-only fixture unexpectedly contains a legacy graph")
	}
	return memory
}

func TestRecallLocalVaultFiltersFailClosedFromMCP(t *testing.T) {
	t.Setenv("ROLLING_MEMORY_BRAIN_URL", "")
	memory := newVaultOnlyMemory(t)
	for _, mode := range []string{"full", "probe", "recent"} {
		for _, filter := range []struct {
			name string
			args map[string]any
		}{
			{"empty-session", map[string]any{"session": ""}},
			{"session", map[string]any{"session": "other"}},
			{"asof-zero", map[string]any{"asof": 0}},
			{"window-start", map[string]any{"window": map[string]any{"start": "2026-01-01"}}},
			{"window-end", map[string]any{"window": map[string]any{"end": "2026-01-01"}}},
		} {
			t.Run(mode+"/"+filter.name, func(t *testing.T) {
				filter.args["cue"] = "witness"
				filter.args["mode"] = mode
				structured, rendered, err := recallResponse(memory, filter.args, nil)
				if err == nil || !strings.Contains(err.Error(), "local_plain") || structured != nil || rendered != "" {
					t.Fatalf("filtered recall = %#v, %q, %v; want unsupported error", structured, rendered, err)
				}
			})
		}
	}
}

func TestRecallGraphFiltersFailClosedFromMCP(t *testing.T) {
	t.Setenv("ROLLING_MEMORY_BRAIN_URL", "")
	memory := newMemory(t)
	if !memory.Store.HasLegacyGraph() {
		t.Fatal("test requires the graph path")
	}
	for _, mode := range []string{"full", "probe", "recent"} {
		for _, filter := range []struct {
			name string
			args map[string]any
		}{
			{"empty-session", map[string]any{"session": ""}},
			{"session", map[string]any{"session": "other"}},
			{"asof-zero", map[string]any{"asof": 0}},
			{"window-start", map[string]any{"window": map[string]any{"start": "2026-01-01"}}},
			{"window-end", map[string]any{"window": map[string]any{"end": "2026-01-01"}}},
		} {
			t.Run(mode+"/"+filter.name, func(t *testing.T) {
				filter.args["cue"] = "witness"
				filter.args["mode"] = mode
				structured, rendered, err := recallResponse(memory, filter.args, nil)
				if err == nil || !strings.Contains(err.Error(), "graph recall cannot enforce") || structured != nil || rendered != "" {
					t.Fatalf("filtered graph recall = %#v, %q, %v; want unsupported error", structured, rendered, err)
				}
			})
		}
	}
}

func stringPointer(value string) *string { return &value }
