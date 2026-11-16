// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package api_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/api"
	"github.com/rocareer/rolling-memory/internal/thinclient"
)

func TestLocalPointerStarvationCorruptIndex(t *testing.T) {
	memory := newMemory(t)
	client, err := memory.ThinClient()
	if err != nil {
		t.Fatal(err)
	}
	cue := "quartz nebula lighthouse"
	dir := t.TempDir()
	for i := 0; i < 33; i++ {
		id := fmt.Sprintf("a%03d", i)
		if i == 32 {
			id = "zhealthy"
		}
		path := filepath.Join(dir, id+".jsonl")
		if err := os.WriteFile(path, []byte(`{"type":"user","message":{"role":"user","content":"quartz nebula lighthouse"}}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		ref := "@ref:jsonl:" + path + ":0#0"
		text, err := thinclient.ResolveEpisodicPointerLive(ref)
		if err != nil {
			t.Fatal(err)
		}
		packet, _ := client.Vault.PackBlindPacket(id, text, testNow)
		if err := client.Vault.SaveLocalEpisodicPointerIndexed(id, testNow, ref, "", text, packet, thinclient.CloudIngestResponse{Layer: thinclient.LayerEpisodic}); err != nil {
			t.Fatal(err)
		}
		if i < 30 {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := memory.Store.DB().Exec(`UPDATE thin_vault_episodic_index SET payload = x'00' WHERE doc_id IN ('a030', 'a031')`); err != nil {
		t.Fatal(err)
	}
	thinclient.PurgeLegacyTables(memory.Store.DB())
	result, err := memory.Recall(api.RecallRequest{Cue: cue, NoCrystallize: true})
	if err != nil || !strings.Contains(result.Render(10000), cue) || result.Gaps["source_unavailable"] != 30 || result.Gaps["index_incomplete"] != 2 {
		t.Fatalf("corrupt index isolation: %+v %v", result, err)
	}
}

func TestLocalPointerStarvation(t *testing.T) {
	for _, n := range []int{0, 20, 29, 30, 31, 96} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			t.Setenv("ROLLING_MEMORY_BRAIN_URL", "")
			memory := newMemory(t)
			client, err := memory.ThinClient()
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			cue := "quartz nebula lighthouse"
			for i := 0; i <= n; i++ {
				id := fmt.Sprintf("a%03d", i)
				if i == n {
					id = "zhealthy"
				}
				path := filepath.Join(dir, id+".jsonl")
				if err := os.WriteFile(path, []byte(`{"type":"user","message":{"role":"user","content":"quartz nebula lighthouse"}}`+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				ref := "@ref:jsonl:" + path + ":0#0"
				text, err := thinclient.ResolveEpisodicPointerLive(ref)
				if err != nil {
					t.Fatal(err)
				}
				packet, _ := client.Vault.PackBlindPacket(id, text, testNow)
				if err := client.Vault.SaveLocalEpisodicPointerIndexed(id, testNow, ref, "", text, packet, thinclient.CloudIngestResponse{Layer: thinclient.LayerEpisodic}); err != nil {
					t.Fatal(err)
				}
				if i < n {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				}
			}
			thinclient.PurgeLegacyTables(memory.Store.DB())
			q, _ := client.Vault.PackBlindPacket("q", cue, 0)
			all, err := thinclient.NewLocalVaultTransport(client.Vault).QueryBlindManifold(t.Context(), q, n+1)
			if err != nil || len(all) != n+1 || all[len(all)-1].UnitID != "zhealthy" {
				t.Fatalf("full ranking=%+v err=%v", all, err)
			}
			result, err := memory.Recall(api.RecallRequest{Cue: cue, NoCrystallize: true})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(result.Render(10000), cue) || result.Gaps["source_unavailable"] != n {
				t.Fatalf("healthy source or gaps missing: gaps=%v result=%s", result.Gaps, result.Render(10000))
			}
			if n == 30 {
				if err := os.Remove(filepath.Join(dir, "zhealthy.jsonl")); err != nil {
					t.Fatal(err)
				}
				exhausted, err := memory.Recall(api.RecallRequest{Cue: cue, NoCrystallize: true})
				if err != nil || exhausted.Gaps["source_unavailable"] != n+1 || len(exhausted.Bands) != 0 {
					t.Fatalf("exhausted sources: %+v %v", exhausted, err)
				}
				for i := 0; i < n; i++ {
					path := filepath.Join(dir, fmt.Sprintf("a%03d.jsonl", i))
					if err := os.WriteFile(path, []byte(`{"type":"user","message":{"role":"user","content":"quartz nebula lighthouse"}}`+"\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				restored, err := memory.Recall(api.RecallRequest{Cue: cue, NoCrystallize: true})
				if err != nil || restored.Gaps["source_unavailable"] != 0 || len(restored.Bands) == 0 {
					t.Fatalf("restored source control: %+v %v", restored, err)
				}
			}
		})
	}
}
