// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package api_test

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/api"
	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/db"
	"github.com/rocareer/rolling-memory/internal/thinclient"
	"github.com/rocareer/rolling-memory/internal/version"
)

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

func TestLocalRecallEpisodicIndexDiagnostics(t *testing.T) {
	tests := []struct {
		name     string
		mutation string
		want     map[string]int
		gap      int
		hit      bool
	}{
		{name: "compatible", want: map[string]int{"compatible": 1, "missing": 0, "incompatible": 0, "corrupt": 0, "needs_rebuild": 0}, hit: true},
		{name: "missing", mutation: `DELETE FROM thin_vault_episodic_index`, want: map[string]int{"compatible": 0, "missing": 1, "incompatible": 0, "corrupt": 0, "needs_rebuild": 0}, gap: 1},
		{name: "incompatible", mutation: `UPDATE thin_vault_episodic_index SET codec_key = x'01'`, want: map[string]int{"compatible": 0, "missing": 0, "incompatible": 1, "corrupt": 0, "needs_rebuild": 0}, gap: 1},
		{name: "corrupt", mutation: `UPDATE thin_vault_episodic_index SET payload = x'00'`, want: map[string]int{"compatible": 0, "missing": 0, "incompatible": 0, "corrupt": 1, "needs_rebuild": 0}, gap: 1},
		{name: "needs_rebuild", mutation: `UPDATE thin_vault_episodic_index SET content_digest = NULL`, want: map[string]int{"compatible": 0, "missing": 0, "incompatible": 0, "corrupt": 0, "needs_rebuild": 1}, gap: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			memory := newVaultOnlyMemory(t)
			client, err := memory.ThinClient()
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "source.jsonl")
			if err := os.WriteFile(path, []byte(`{"type":"user","message":{"role":"user","content":"quartz nebula evidence"}}`+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			ref := "@ref:jsonl:" + path + ":0#0"
			text := "[user] quartz nebula evidence"
			packet, _ := client.Vault.PackBlindPacket("ep_index", text, testNow)
			if err := client.Vault.SaveLocalEpisodicPointerIndexed("ep_index", testNow, ref, "", text, packet, thinclient.CloudIngestResponse{Layer: thinclient.LayerEpisodic}); err != nil {
				t.Fatal(err)
			}
			if tt.mutation != "" {
				if _, err := memory.Store.DB().Exec(tt.mutation); err != nil {
					t.Fatal(err)
				}
			}
			result, err := memory.Recall(api.RecallRequest{Cue: "quartz nebula evidence", NoCrystallize: true})
			if err != nil {
				t.Fatal(err)
			}
			if got := result.Meta["episodic_index"]; !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("episodic index = %#v, want %#v", got, tt.want)
			}
			if result.Gaps["index_incomplete"] != tt.gap || result.Absent {
				t.Fatalf("gap or absence = %+v", result)
			}
			if got := len(result.Bands) > 0; got != tt.hit {
				t.Fatalf("delivered hit = %v, want %v: %+v", got, tt.hit, result)
			}
			var noetic int
			if err := memory.Store.DB().QueryRow(`SELECT count(*) FROM thin_vault_noetic`).Scan(&noetic); err != nil || noetic != 0 {
				t.Fatalf("recall wrote L3: count=%d err=%v", noetic, err)
			}
		})
	}
}

func TestLocalRecallEmptyIndexDiagnostics(t *testing.T) {
	memory := newVaultOnlyMemory(t)
	if _, err := memory.ThinClient(); err != nil {
		t.Fatal(err)
	}
	result, err := memory.Recall(api.RecallRequest{Cue: "quartz nebula", NoCrystallize: true})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"compatible": 0, "missing": 0, "incompatible": 0, "corrupt": 0, "needs_rebuild": 0}
	if !result.Absent || result.Gaps["index_incomplete"] != 0 || !reflect.DeepEqual(result.Meta["episodic_index"], want) {
		t.Fatalf("empty index: %+v", result)
	}
}

func TestRecallIndexDiagnosticsIsLocalTransportOnly(t *testing.T) {
	t.Run("HTTP transport", func(t *testing.T) {
		_, baseURL := newNoeticEngineMock(t)
		memory := thinBrainMemory(t, baseURL)
		client, err := memory.ThinClient()
		if err != nil {
			t.Fatal(err)
		}
		if err := client.Vault.SaveLocalPlaintext("ep_missing", testNow, "@ref:jsonl:/sensitive/path:0#0", nil, thinclient.CloudIngestResponse{Layer: thinclient.LayerEpisodic}); err != nil {
			t.Fatal(err)
		}
		result, err := memory.Recall(api.RecallRequest{Cue: "quartz nebula", NoCrystallize: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := result.Meta["episodic_index"]; ok || result.Gaps["index_incomplete"] != 0 || !result.Absent {
			t.Fatalf("local index attributed to HTTP transport: %+v", result)
		}
		var noetic int
		if err := memory.Store.DB().QueryRow(`SELECT count(*) FROM thin_vault_noetic`).Scan(&noetic); err != nil || noetic != 0 {
			t.Fatalf("recall wrote L3: count=%d err=%v", noetic, err)
		}
	})
}

func TestLocalRecallIndexSchemaFailurePrecedesDiagnostics(t *testing.T) {
	memory := newVaultOnlyMemory(t)
	if _, err := memory.ThinClient(); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.Store.DB().Exec(`ALTER TABLE thin_vault_episodic_index RENAME COLUMN content_digest TO hidden_digest`); err != nil {
		t.Fatal(err)
	}
	result, err := memory.Recall(api.RecallRequest{Cue: "quartz nebula", NoCrystallize: true})
	if err == nil || result != nil || !strings.Contains(err.Error(), "content_digest") {
		t.Fatalf("retrieval must report its schema failure before diagnostics: result=%+v err=%v", result, err)
	}
}
