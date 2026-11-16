// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package api_test

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/rocareer/rolling-memory/internal/api"
	"github.com/rocareer/rolling-memory/internal/recall"
	"github.com/rocareer/rolling-memory/internal/thinclient"
	"github.com/rocareer/rolling-memory/internal/transcript"
)

// A RAME database keeps only the `thin_vault_*` schema (PurgeLegacyTables retired the
// assertion graph), so local-mode recall must be served from the plaintext vault instead
// of the legacy graph reader that would fail with "no such table: node".
func TestLocalModeRecallServesThinVaultOnlyDatabase(t *testing.T) {
	memory := newVaultOnlyMemory(t)
	client, err := memory.ThinClient()
	if err != nil {
		t.Fatalf("thinclient: %v", err)
	}
	if err := client.Vault.SaveLocalPlaintext(
		"ep_local_recall_anchor",
		testNow,
		"控制台开箱默认账号由宿主 bootstrap 播种，登录页一键填入预置账号。",
		nil,
		thinclient.CloudIngestResponse{Layer: "episodic"},
	); err != nil {
		t.Fatalf("seed local vault: %v", err)
	}
	if memory.Store.HasLegacyGraph() {
		t.Fatal("legacy graph must be retired before the vault recall path is exercised")
	}

	result, err := memory.Recall(api.RecallRequest{Cue: "登录页 预置账号"})
	if err != nil {
		t.Fatalf("Recall on a thin-vault database: %v", err)
	}
	if len(result.RawHits) == 0 && len(result.Bands) == 0 {
		t.Fatalf("expected vault delivery, got bands=%d raw=%d", len(result.Bands), len(result.RawHits))
	}

	// The vault path cannot reconstruct time filters: an explicit as-of must fail loudly
	// instead of silently returning unfiltered rows.
	if _, err := memory.Recall(api.RecallRequest{Cue: "登录页", AsOf: "2026-01-01"}); err == nil {
		t.Fatal("as-of recall on a thin-vault database must report that time filters are unsupported")
	}
}

func TestLocalRecallReportsUnavailableEpisodicSource(t *testing.T) {
	memory := newMemory(t)
	client, err := memory.ThinClient()
	if err != nil {
		t.Fatal(err)
	}
	text := "[user] quartz unavailable source evidence"
	path := filepath.Join(t.TempDir(), "source.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"user","sessionId":"s1","message":{"role":"user","content":"quartz unavailable source evidence"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ref := "@ref:jsonl:" + path + ":0#0"
	packet, _ := client.Vault.PackBlindPacket("ep_missing", text, testNow)
	if err := client.Vault.SaveLocalEpisodicPointerIndexed("ep_missing", testNow, ref, "", text, packet, thinclient.CloudIngestResponse{Layer: "episodic"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	thinclient.PurgeLegacyTables(memory.Store.DB())
	result, err := memory.Recall(api.RecallRequest{Cue: "quartz unavailable source evidence", NoCrystallize: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Gaps["source_unavailable"] != 1 || result.Absent {
		t.Fatalf("missing source must be reported without claiming absence: %+v", result)
	}
	if len(result.Bands) != 0 || len(result.RawHits) != 0 {
		t.Fatalf("unresolved pointer must not be delivered as evidence: %+v", result)
	}
	if gaps, ok := result.ToDict()["gaps"].(map[string]any); !ok || gaps["source_unavailable"] != 1 {
		t.Fatalf("structured gap missing: %#v", result.ToDict())
	}
	if rendered := result.Render(10000); !strings.Contains(rendered, "来源不可回源 1") {
		t.Fatalf("rendered gap missing: %s", rendered)
	}
}

func TestLocalRecallDeliveryCapsAndOffset(t *testing.T) {
	memory := newMemory(t)
	client, err := memory.ThinClient()
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"caps-a", "caps-b", "caps-c"} {
		if err := client.Vault.SaveLocalPlaintext(id, testNow+int64(i), "quartz evidence "+id, nil, thinclient.CloudIngestResponse{Layer: "episodic"}); err != nil {
			t.Fatal(err)
		}
	}
	thinclient.PurgeLegacyTables(memory.Store.DB())
	one, zero := 1, 0
	for _, mode := range []string{"full", "recent"} {
		t.Run(mode, func(t *testing.T) {
			base := api.RecallRequest{Cue: "quartz evidence", Mode: mode, CapsBands: &one, NoCrystallize: true}
			first, err := memory.Recall(base)
			if err != nil || len(first.Bands) != 1 {
				t.Fatalf("first page: %+v, %v", first, err)
			}
			base.Offset = 1
			second, err := memory.Recall(base)
			if err != nil || len(second.Bands) != 1 {
				t.Fatalf("second page: %+v, %v", second, err)
			}
			id := func(r *recall.Result) any { return r.Bands[0]["nodes"].([]map[string]any)[0]["id"] }
			if id(first) == id(second) {
				t.Fatalf("offset repeated source %v", id(first))
			}
			base.CapsNodes = &zero
			empty, err := memory.Recall(base)
			if err != nil || len(empty.Bands) != 1 || len(empty.Bands[0]["nodes"].([]map[string]any)) != 0 || len(empty.RawHits) != 0 {
				t.Fatalf("node cap: %+v, %v", empty, err)
			}
		})
	}
}

// An explicit different scope must not relabel unscoped local vault rows as its own.
func TestLocalVaultRecallRejectsDifferentScope(t *testing.T) {
	memory := newMemory(t)
	client, err := memory.ThinClient()
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Vault.SaveLocalPlaintext("scope-a-doc", testNow, "quartz scope isolation witness", nil, thinclient.CloudIngestResponse{Layer: "episodic"}); err != nil {
		t.Fatal(err)
	}
	thinclient.PurgeLegacyTables(memory.Store.DB())

	otherScope := "mem:other"
	for _, mode := range []string{"full", "probe", "recent"} {
		t.Run(mode, func(t *testing.T) {
			result, err := memory.Recall(api.RecallRequest{Cue: "quartz scope isolation witness", Scope: &otherScope, Mode: mode, NoCrystallize: true})
			if err == nil || result != nil {
				t.Errorf("cross-scope recall = %#v, %v; want an error and no result", result, err)
			}
		})
	}
	items, err := memory.Recent(&otherScope, nil, 10)
	if err == nil || items != nil {
		t.Errorf("cross-scope recent = %#v, %v; want an error and no items", items, err)
	}
}

// A matching request and configuration cannot prove the ownership of old rows when
// the same database is reopened with a different scope.
func TestLocalVaultReopenedWithDifferentScopeDoesNotExposeOldRows(t *testing.T) {
	memory := newMemory(t)
	client, err := memory.ThinClient()
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Vault.SaveLocalPlaintext("scope-a-doc", testNow, "quartz reopen isolation witness", nil, thinclient.CloudIngestResponse{Layer: "episodic"}); err != nil {
		t.Fatal(err)
	}

	cfg := *memory.Config
	cfg.Scope = "mem:other"
	other, err := api.New(&cfg)
	if err != nil {
		// store.Open now rejects the mismatched scope binding before returning a handle.
		return
	}
	t.Cleanup(func() { other.Close() })
	for _, mode := range []string{"full", "probe", "recent"} {
		t.Run(mode, func(t *testing.T) {
			result, err := other.Recall(api.RecallRequest{Cue: "quartz reopen isolation witness", Mode: mode, NoCrystallize: true})
			if err == nil && result != nil && (len(result.Bands) > 0 || len(result.RawHits) > 0) {
				t.Errorf("reopened scope received old vault rows: %#v", result.ToDict())
			}
		})
	}
	items, err := other.Recent(nil, nil, 10)
	if err == nil && len(items) > 0 {
		t.Errorf("reopened scope received old recent rows: %#v", items)
	}
}

func TestLocalVaultScopeBindingFailsClosed(t *testing.T) {
	for _, source := range []string{"docs", "noetic", "background"} {
		t.Run(source, func(t *testing.T) {
			memory := newVaultOnlyMemory(t)
			if _, err := memory.Store.DB().Exec(`DELETE FROM thin_vault_meta WHERE k = 'local_plain_scope'`); err != nil {
				t.Fatal(err)
			}
			client, err := thinclient.NewVault(memory.Store.DB(), nil)
			if err != nil {
				t.Fatal(err)
			}
			switch source {
			case "docs":
				err = client.SaveLocalPlaintext("unbound-doc", testNow, "unbound secret witness", nil, thinclient.CloudIngestResponse{Layer: "episodic"})
			case "noetic":
				err = client.SaveLocalNoetic(thinclient.LocalNoeticRecord{NoeticID: "unbound-noetic", QueryText: "unbound secret witness", AnswerText: "unbound secret answer"})
			case "background":
				_, err = memory.Store.DB().Exec(`CREATE TABLE thin_vault_bg_dirs (dir_id TEXT PRIMARY KEY); INSERT INTO thin_vault_bg_dirs VALUES ('unbound-background')`)
			}
			if err != nil {
				t.Fatal(err)
			}
			bound, populated, err := memory.LocalVaultScopePreflight()
			if err != nil || bound != "" || !populated {
				t.Fatalf("unbound vault preflight = %q, %v, %v", bound, populated, err)
			}
			for _, mode := range []string{"full", "probe", "recent"} {
				includeRaw := true
				result, err := memory.Recall(api.RecallRequest{Cue: "unbound secret witness", Mode: mode, IncludeRaw: &includeRaw, NoCrystallize: true})
				if err == nil || result != nil {
					t.Fatalf("unbound %s recall = %#v, %v", mode, result, err)
				}
			}
			if items, err := memory.Recent(nil, nil, 10); err == nil || items != nil {
				t.Fatalf("unbound recent = %#v, %v", items, err)
			}
			var bindingCount int
			if err := memory.Store.DB().QueryRow(`SELECT COUNT(*) FROM thin_vault_meta WHERE k = 'local_plain_scope'`).Scan(&bindingCount); err != nil || bindingCount != 0 {
				t.Fatalf("unbound vault was claimed: count=%d err=%v", bindingCount, err)
			}
			table := map[string]string{"docs": "thin_vault_docs", "noetic": "thin_vault_noetic", "background": "thin_vault_bg_dirs"}[source]
			var count int
			if err := memory.Store.DB().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 1 {
				t.Fatalf("unbound data changed: count=%d err=%v", count, err)
			}
		})
	}
}

func TestLocalVaultBoundScopeGuardsNoeticAndRaw(t *testing.T) {
	memory := newVaultOnlyMemory(t)
	if _, err := memory.Store.DB().Exec(`DELETE FROM thin_vault_meta WHERE k = 'local_plain_scope'`); err != nil {
		t.Fatal(err)
	}
	bound, populated, err := memory.LocalVaultScopePreflight()
	if err != nil || bound != "" || populated {
		t.Fatalf("empty vault preflight = %q, %v, %v", bound, populated, err)
	}
	client, err := memory.ThinClient()
	if err != nil {
		t.Fatal(err)
	}
	bound, populated, err = memory.LocalVaultScopePreflight()
	if err != nil || bound != memory.Config.Scope || populated {
		t.Fatalf("bound vault preflight = %q, %v, %v", bound, populated, err)
	}
	if err := client.Vault.SaveLocalPlaintext("bound-doc", testNow, "quartz bound scope secret", nil, thinclient.CloudIngestResponse{Layer: "episodic"}); err != nil {
		t.Fatal(err)
	}
	if err := client.Vault.SaveLocalNoetic(thinclient.LocalNoeticRecord{NoeticID: "bound-noetic", QueryText: "quartz bound scope secret", AnswerText: "quartz derived secret", CitedUnitIDs: []string{"bound-doc"}}); err != nil {
		t.Fatal(err)
	}
	otherScope := "mem:other"
	for _, mode := range []string{"full", "probe", "recent"} {
		for _, raw := range []bool{false, true} {
			result, err := memory.Recall(api.RecallRequest{Cue: "quartz bound scope secret", Scope: &otherScope, Mode: mode, IncludeRaw: &raw})
			if err == nil || result != nil {
				t.Fatalf("cross-scope %s raw=%v = %#v, %v", mode, raw, result, err)
			}
		}
	}
	result, err := memory.Recall(api.RecallRequest{Cue: "quartz bound scope secret", NoCrystallize: true})
	if err != nil || result == nil || len(result.Bands) == 0 {
		t.Fatalf("bound scope recall = %#v, %v", result, err)
	}
	cfg := *memory.Config
	cfg.Scope = otherScope
	other, err := api.New(&cfg)
	if err == nil {
		defer other.Close()
		if _, err := other.ThinClient(); err == nil {
			t.Fatal("reopened vault accepted a different scope")
		}
	}
	var binding string
	if err := memory.Store.DB().QueryRow(`SELECT v FROM thin_vault_meta WHERE k = 'local_plain_scope'`).Scan(&binding); err != nil || binding != memory.Config.Scope {
		t.Fatalf("scope binding changed: %q, %v", binding, err)
	}
	var docs, noetic int
	if err := memory.Store.DB().QueryRow(`SELECT (SELECT COUNT(*) FROM thin_vault_docs), (SELECT COUNT(*) FROM thin_vault_noetic)`).Scan(&docs, &noetic); err != nil || docs != 1 || noetic != 1 {
		t.Fatalf("vault rows changed: docs=%d noetic=%d err=%v", docs, noetic, err)
	}
}

func TestLocalVaultImportRejectsScopeBeforeWrites(t *testing.T) {
	for _, scenario := range []string{"explicit", "reopened", "unbound"} {
		t.Run(scenario, func(t *testing.T) {
			memory := newMemory(t)
			if scenario == "unbound" {
				if _, err := memory.Store.DB().Exec(`DELETE FROM thin_vault_meta WHERE k = 'local_plain_scope'`); err != nil {
					t.Fatal(err)
				}
				vault, err := thinclient.NewVault(memory.Store.DB(), nil)
				if err != nil {
					t.Fatal(err)
				}
				if err := vault.SaveLocalPlaintext("unbound-doc", testNow, "unbound witness", nil, thinclient.CloudIngestResponse{Layer: "episodic"}); err != nil {
					t.Fatal(err)
				}
			} else if _, err := memory.ThinClient(); err != nil {
				t.Fatal(err)
			}
			var scope *string
			if scenario == "explicit" {
				scope = strPtr("mem:other")
			} else if scenario == "reopened" {
				cfg := *memory.Config
				cfg.Scope = "mem:other"
				other, err := api.New(&cfg)
				if err != nil {
					var traces int
					if qErr := memory.Store.DB().QueryRow(`SELECT COUNT(*) FROM trace`).Scan(&traces); qErr != nil || traces != 0 {
						t.Fatalf("rejected reopen wrote %d traces: %v", traces, qErr)
					}
					return
				}
				defer other.Close()
				memory = other
			}
			path := writeTranscript(t, "scope.txt", "quartz import isolation witness\n")
			result, err := memory.ImportFile(path, scope, nil, "shared", true)
			if err == nil || result != nil {
				t.Errorf("import = %#v, %v; want rejection", result, err)
			}
			var traces int
			if err := memory.Store.DB().QueryRow(`SELECT COUNT(*) FROM trace`).Scan(&traces); err != nil || traces != 0 {
				t.Fatalf("rejected import wrote %d traces: %v", traces, err)
			}
			if _, found, err := transcript.CursorFor(memory.Store, path, "scope"); err != nil || found {
				t.Fatalf("rejected import advanced cursor: found=%v err=%v", found, err)
			}
		})
	}
}

func TestLocalVaultRememberNoeticRejectsDifferentScope(t *testing.T) {
	for _, initialized := range []bool{false, true} {
		t.Run(map[bool]string{false: "uninitialized", true: "initialized"}[initialized], func(t *testing.T) {
			memory := newMemory(t)
			if initialized {
				if _, err := memory.ThinClient(); err != nil {
					t.Fatal(err)
				}
			}
			id, err := memory.RememberNoetic("mem:other", "session", "quartz cue", nil, nil, "secret answer")
			if err == nil || id != "" {
				t.Fatalf("cross-scope noetic = %q, %v; want rejection", id, err)
			}
			var tables int
			if err := memory.Store.DB().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'thin_vault_noetic'`).Scan(&tables); err != nil {
				t.Fatal(err)
			}
			if !initialized && tables != 0 {
				t.Fatal("cross-scope noetic initialized the vault")
			}
			if initialized {
				var count int
				if err := memory.Store.DB().QueryRow(`SELECT COUNT(*) FROM thin_vault_noetic`).Scan(&count); err != nil || count != 0 {
					t.Fatalf("cross-scope noetic wrote %d rows: %v", count, err)
				}
			}
		})
	}
}

func TestLocalVaultConcurrentScopeBinding(t *testing.T) {
	memory := newVaultOnlyMemory(t)
	if _, err := memory.Store.DB().Exec(`DELETE FROM thin_vault_meta WHERE k = 'local_plain_scope'`); err != nil {
		t.Fatal(err)
	}
	cfg := *memory.Config
	cfg.Scope = "mem:other"
	other, err := api.New(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := memory.Store.DB().Exec(`DELETE FROM thin_vault_meta WHERE k = 'local_plain_scope'`); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]error, 2)
	for i, candidate := range []*api.Memory{memory, other} {
		wg.Add(1)
		go func(i int, candidate *api.Memory) {
			defer wg.Done()
			<-start
			_, results[i] = candidate.ThinClient()
		}(i, candidate)
	}
	close(start)
	wg.Wait()
	if (results[0] == nil) == (results[1] == nil) {
		t.Fatalf("concurrent binding must have one winner: %v, %v", results[0], results[1])
	}
	bound, _, err := memory.LocalVaultScopePreflight()
	if err != nil || (bound != "mem:test" && bound != "mem:other") {
		t.Fatalf("concurrent binding = %q, %v", bound, err)
	}
	var winner *api.Memory
	if bound == "mem:test" {
		winner = memory
	} else {
		winner = other
	}
	if _, err := winner.ThinClient(); err != nil {
		t.Fatalf("winning scope rejected: %v", err)
	}
}

func TestLocalRecallMixedSourceUnifiedRankingWithLocusAnchor(t *testing.T) {
	memory := newVaultOnlyMemory(t)
	client, err := memory.ThinClient()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "turn.jsonl")
	turnLine := `{"type":"user","sessionId":"s1","message":{"role":"user","content":"quartz mixed ranking weak turn witness"}}` + "\n"
	if err := os.WriteFile(path, []byte(turnLine), 0o600); err != nil {
		t.Fatal(err)
	}
	ref := "@ref:jsonl:" + path + ":0#0"
	turnText := "[user] quartz mixed ranking weak turn witness"
	packet, _ := client.Vault.PackBlindPacket("ep_turn_low", turnText, testNow)
	client.SetIdentity("default", "", "Rolling-Memory")
	if err := client.Vault.SaveLocalEpisodicPointerIndexed("ep_turn_low", testNow, ref, "Rolling-Memory", turnText, packet, thinclient.CloudIngestResponse{Layer: "episodic"}); err != nil {
		t.Fatal(err)
	}
	standaloneText := "quartz mixed ranking strong standalone witness crystalline prism"
	if err := client.Vault.SaveLocalPlaintext("doc_standalone_high", testNow+10, standaloneText, nil, thinclient.CloudIngestResponse{Layer: "episodic"}); err != nil {
		t.Fatal(err)
	}
	one := 1
	result, err := memory.Recall(api.RecallRequest{
		Cue:           "quartz mixed ranking strong standalone witness crystalline prism",
		RawLimit:      1,
		CapsBands:     &one,
		NoCrystallize: true,
	})
	if err != nil || len(result.Bands) != 1 {
		t.Fatalf("mixed source recall = %+v, %v", result, err)
	}
	nodes := result.Bands[0]["nodes"].([]map[string]any)
	if got := nodes[0]["id"]; got != "doc_standalone_high" {
		t.Fatalf("top-1 band delivered %v, want higher-scoring standalone doc_standalone_high", got)
	}
}

// 端侧检索范围锚在本地明文管线同样生效：命中锚（= 入库 locus）的候选稳定置前，
// 其余候选原序压后且不删除（fail-open）；无锚交付不携带锚注记。
func TestLocalModeRecallLocusAnchorPromotesSameWorkspace(t *testing.T) {
	memory := newMemory(t)
	client, err := memory.ThinClient()
	if err != nil {
		t.Fatalf("thinclient: %v", err)
	}
	seed := func(docID, locus, text string) {
		t.Helper()
		client.SetIdentity("default", "", locus)
		if err := client.Vault.SaveLocalPlaintext(
			docID, testNow, text, nil,
			thinclient.CloudIngestResponse{Layer: "episodic"},
		); err != nil {
			t.Fatalf("seed %s: %v", docID, err)
		}
	}
	seed("ep_locus_same", "Rolling-Memory", "首次进入控制台卡住要强刷新才能进入的守卫记录。")
	seed("ep_locus_other", "Other-Workspace", "首次进入控制台卡住要强刷新才能进入的审计报告。")
	thinclient.PurgeLegacyTables(memory.Store.DB())

	leadLocus := func(result *recall.Result) string {
		if len(result.Bands) == 0 {
			return ""
		}
		nodes, _ := result.Bands[0]["nodes"].([]map[string]any)
		if len(nodes) == 0 {
			return ""
		}
		locus, _ := nodes[0]["locus"].(string)
		return locus
	}

	for _, anchor := range []string{"Rolling-Memory", "Other-Workspace"} {
		result, err := memory.Recall(api.RecallRequest{Cue: "[" + anchor + "] 首次进入控制台卡住的守卫记录"})
		if err != nil {
			t.Fatalf("recall with anchor %q: %v", anchor, err)
		}
		if len(result.Bands) < 2 {
			t.Fatalf("anchor %q delivered %d bands, want both workspaces", anchor, len(result.Bands))
		}
		if got := leadLocus(result); got != anchor {
			t.Fatalf("anchor %q leads with locus %q, want %q", anchor, got, anchor)
		}
	}

	result, err := memory.Recall(api.RecallRequest{Cue: "首次进入控制台卡住的守卫记录"})
	if err != nil {
		t.Fatalf("recall without anchor: %v", err)
	}
	if _, ok := result.Meta["locus_anchor"]; ok {
		t.Fatalf("meta locus_anchor set without anchor: %#v", result.Meta["locus_anchor"])
	}
}

// `recent` 命令的扁平收据窗口与 recall 的 recent 原语遵守同一条来源诚实规则：
// 窗口被 L3 派生 handle 占满时，用最新源层条目替换最旧派生槽（纯时序不变）。
func TestRecentVaultItemsKeepsSourceVisibleInDerivedWindow(t *testing.T) {
	memory := newMemory(t)
	client, err := memory.ThinClient()
	if err != nil {
		t.Fatalf("thinclient: %v", err)
	}
	if err := client.Vault.SaveLocalPlaintext(
		"ep_recent_source", testNow,
		"控制台守卫记录：首次进入需强刷新才能进入。",
		nil,
		thinclient.CloudIngestResponse{Layer: "episodic"},
	); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	for i, id := range []string{"nt_recent_a", "nt_recent_b", "nt_recent_c"} {
		if err := client.Vault.SaveLocalNoetic(thinclient.LocalNoeticRecord{
			NoeticID:     id,
			OccurredAt:   testNow + int64(10*(i+1)),
			QueryText:    "q:" + id,
			AnswerText:   "a:" + id,
			CitedUnitIDs: []string{"ep_recent_source"},
		}); err != nil {
			t.Fatalf("seed noetic %s: %v", id, err)
		}
	}
	thinclient.PurgeLegacyTables(memory.Store.DB())

	items, err := memory.Recent(nil, nil, 3)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("recent window = %d items, want 3", len(items))
	}
	sourceVisible := false
	for _, item := range items {
		if layer, _ := item["layer"].(string); layer != "noetic" {
			sourceVisible = true
		}
	}
	if !sourceVisible {
		t.Fatalf("derived-only window must surface a source entry, got %#v", items)
	}
}

func TestRecentVaultRecallHonorsWorkspaceLocusAnchor(t *testing.T) {
	memory := newVaultOnlyMemory(t)
	client, err := memory.ThinClient()
	if err != nil {
		t.Fatalf("thinclient: %v", err)
	}
	client.SetIdentity("a", "i", "TargetRepo")
	if err := client.Vault.SaveLocalPlaintext(
		"ep_target_old", testNow,
		"TargetRepo earlier milestone record.",
		nil,
		thinclient.CloudIngestResponse{Layer: "episodic"},
	); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	client.SetIdentity("a", "i", "OtherRepo")
	for i := 0; i < 5; i++ {
		if err := client.Vault.SaveLocalPlaintext(
			"ep_other_"+string(rune('a'+i)), testNow+int64(10*(i+1)),
			"OtherRepo newer chatter.",
			nil,
			thinclient.CloudIngestResponse{Layer: "episodic"},
		); err != nil {
			t.Fatalf("seed other %d: %v", i, err)
		}
	}
	bandsCap := 2
	rawCap := 2
	result, err := memory.Recall(api.RecallRequest{
		Cue:           "[TargetRepo]",
		Mode:          "recent",
		CapsBands:     &bandsCap,
		RawLimit:      rawCap,
		NoCrystallize: true,
	})
	if err != nil {
		t.Fatalf("recent recall: %v", err)
	}
	if len(result.Bands) == 0 {
		t.Fatalf("expected bands, got empty")
	}
	firstSpace, _ := result.Bands[0]["space_label"].(string)
	if !strings.EqualFold(strings.TrimSpace(firstSpace), "TargetRepo") {
		t.Fatalf("expected [TargetRepo] anchor to promote TargetRepo to band[0], got %q (%#v)", firstSpace, result.Bands[0])
	}
}

func TestRecentVaultRecallFoldsRedundantNoeticDerivativesBehindSourceMemories(t *testing.T) {
	memory := newVaultOnlyMemory(t)
	client, err := memory.ThinClient()
	if err != nil {
		t.Fatalf("thinclient: %v", err)
	}
	client.SetIdentity("a", "i", "Rolling-Memory")
	if err := client.Vault.SaveLocalPlaintext(
		"ep_src_1", testNow,
		"First genuine source conversation turn.",
		nil,
		thinclient.CloudIngestResponse{Layer: "episodic"},
	); err != nil {
		t.Fatalf("seed ep_src_1: %v", err)
	}
	if err := client.Vault.SaveLocalPlaintext(
		"ep_src_2", testNow+10,
		"Second genuine source conversation turn.",
		nil,
		thinclient.CloudIngestResponse{Layer: "episodic"},
	); err != nil {
		t.Fatalf("seed ep_src_2: %v", err)
	}
	for i, id := range []string{"nt_dup_1", "nt_dup_2"} {
		if err := client.Vault.SaveLocalNoetic(thinclient.LocalNoeticRecord{
			NoeticID:     id,
			OccurredAt:   testNow + int64(20+i*10),
			QueryText:    "[Rolling-Memory] query " + id,
			AnswerText:   "derived answer",
			CitedUnitIDs: []string{"ep_src_1"},
		}); err != nil {
			t.Fatalf("seed noetic %s: %v", id, err)
		}
	}
	bandsCap := 2
	rawCap := 2
	result, err := memory.Recall(api.RecallRequest{
		Mode:          "recent",
		CapsBands:     &bandsCap,
		RawLimit:      rawCap,
		NoCrystallize: true,
	})
	if err != nil {
		t.Fatalf("recent recall: %v", err)
	}
	if len(result.Bands) != 2 {
		t.Fatalf("expected 2 bands, got %d", len(result.Bands))
	}
	for idx, band := range result.Bands {
		title, _ := band["title"].(string)
		if strings.HasPrefix(title, "[noetic]") {
			t.Fatalf("band[%d] = %q; redundant noetic derivative citing visible source ep_src_1 should be folded behind unshown source memories", idx, title)
		}
	}
}
