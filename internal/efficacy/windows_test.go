// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package efficacy_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rocareer/rolling-memory/internal/api"
	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/efficacy"
	"github.com/rocareer/rolling-memory/internal/store"
)

// seedMonitorTurn writes one user prompt + assistant reply pair at a controlled recorded_at.
func seedMonitorTurn(t *testing.T, mem *api.Memory, session string, recordedAt int64, prompt, reply string) {
	t.Helper()
	at := recordedAt
	if _, _, err := mem.Store.AddTrace("mem:self", prompt, store.TraceOptions{
		Session:    session,
		Turn:       1,
		Kind:       "message",
		Author:     "user",
		Audience:   "shared",
		OccurredAt: &at,
		RecordedAt: &at,
		Meta:       map[string]any{"world": "harness:zcode", "locus": "rocareer/rolling-memory"},
	}); err != nil {
		t.Fatalf("seed prompt trace: %v", err)
	}
	if _, _, err := mem.Store.AddTrace("mem:self", reply, store.TraceOptions{
		Session:    session,
		Turn:       2,
		Kind:       "message",
		Author:     "assistant",
		Audience:   "shared",
		OccurredAt: &at,
		RecordedAt: &at,
		Meta:       map[string]any{"world": "harness:zcode", "locus": "rocareer/rolling-memory"},
	}); err != nil {
		t.Fatalf("seed assistant trace: %v", err)
	}
}

func monitorTestMemory(t *testing.T) (*api.Memory, *config.Config, string) {
	t.Helper()
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "memory.db")
	logPath := filepath.Join(tmp, "rolling-memory.log")
	cfgPath := filepath.Join(tmp, "config.toml")
	cfg, err := config.Load(config.LoadOptions{
		Path:  &cfgPath,
		DB:    &dbPath,
		Scope: strPtr("mem:self"),
	})
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	cfg.Log = logPath
	mem, err := api.New(cfg)
	if err != nil {
		t.Fatalf("new memory: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	seedAt := time.Now().Unix() - 3600
	if _, err := mem.Remember(api.RememberRequest{
		Content: strPtr("监控窗聚合落位于 internal/efficacy/efficacy.go 的 window buckets。"),
		Assertion: map[string]any{
			"s":          map[string]any{"label": "监控窗聚合"},
			"x":          "落位于",
			"o":          map[string]any{"literal": "internal/efficacy/efficacy.go 的 window buckets"},
			"confidence": 0.99,
			"warrant":    "performative",
		},
		AnchorText: strPtr("internal/efficacy/efficacy.go"),
		Session:    "seed-session",
		Author:     "user",
		OccurredAt: seedAt,
	}); err != nil {
		t.Fatalf("remember seed: %v", err)
	}
	// Backdate the seed trace's recorded_at (>30s ago) so QuerySourceWatermark.Settled is true
	// and TryLoadROILedger's fast read path is genuinely exercised.
	_, _ = mem.Store.DB().Exec(`UPDATE trace SET recorded_at = ? WHERE session = 'seed-session'`, seedAt)
	return mem, cfg, tmp
}

// TestEvaluateRollingMonitorWindows pins the 时 / 日 / 周 / 月 rolling windows: each window must
// contain exactly the turns inside its span (1h ⊂ 24h ⊂ 7d ⊂ 30d), stay hour-aligned for the
// hourly series, and keep the "no injection ⇒ no ratio" convention.
func TestEvaluateRollingMonitorWindows(t *testing.T) {
	mem, cfg, tmp := monitorTestMemory(t)
	now := time.Now().Unix()
	// 1h < 3h < 3d < 20d: one turn per window band so the containment is exactly observable.
	seedMonitorTurn(t, mem, "sess-win-hour", now-90, "监控窗 近一小时 校准", "已核对近一小时窗口内轮次。")
	seedMonitorTurn(t, mem, "sess-win-day", now-3*3600, "监控窗 近二十四小时 校准", "已核对近二十四小时窗口内轮次。")
	seedMonitorTurn(t, mem, "sess-win-week", now-3*86400, "监控窗 近七天 校准", "已核对近七天窗口内轮次。")
	seedMonitorTurn(t, mem, "sess-win-month", now-20*86400, "监控窗 近三十天 校准", "已核对近三十天窗口内轮次。")

	rep, err := efficacy.Evaluate(mem.Store, cfg, efficacy.Options{Scope: "mem:self", Limit: 50, HomeDir: tmp, WorkspaceDir: tmp})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	afterNow := time.Now().Unix()
	if rep.EvaluatedTurns != 4 {
		t.Fatalf("expected 4 evaluated turns, got %d", rep.EvaluatedTurns)
	}
	if len(rep.Windows) != 4 {
		t.Fatalf("expected 4 rolling windows, got %d (%+v)", len(rep.Windows), rep.Windows)
	}
	wantTurns := map[string]int{"1h": 1, "24h": 2, "7d": 3, "30d": 4}
	wantSessions := map[string]int{"1h": 1, "24h": 2, "7d": 3, "30d": 4}
	for i, key := range []string{"1h", "24h", "7d", "30d"} {
		w := rep.Windows[i]
		if w.Key != key {
			t.Fatalf("window[%d] key = %q, want %q", i, w.Key, key)
		}
		if w.Turns != wantTurns[key] {
			t.Fatalf("window %s turns = %d, want %d", key, w.Turns, wantTurns[key])
		}
		if w.Sessions != wantSessions[key] {
			t.Fatalf("window %s sessions = %d, want %d", key, w.Sessions, wantSessions[key])
		}
		winEnd := w.SinceUnix + w.Seconds
		if winEnd < now-5 || winEnd > afterNow+5 {
			t.Fatalf("window %s since_unix+seconds (%d) outside [%d, %d]", key, winEnd, now-5, afterNow+5)
		}
		if w.InjectedTokens == 0 && w.NetTokenROI != 0 {
			t.Fatalf("window %s must not report a ROI ratio without injections, got %v", key, w.NetTokenROI)
		}
		if funnelSum := w.FunnelHighValue + w.FunnelAssisted + w.FunnelDedup + w.FunnelSilence + w.FunnelNoise; funnelSum != w.Turns {
			t.Fatalf("window %s funnel buckets (%d) must partition its turns (%d)",
				key, funnelSum, w.Turns)
		}
	}
	if rep.Windows[3].Turns < rep.Windows[2].Turns || rep.Windows[2].Turns < rep.Windows[1].Turns || rep.Windows[1].Turns < rep.Windows[0].Turns {
		t.Fatalf("rolling windows must be monotone (1h ⊆ 24h ⊆ 7d ⊆ 30d): %+v", rep.Windows)
	}
	if rep.Windows[3].Turns != rep.EvaluatedTurns+rep.LegacyCarriedTurns {
		t.Fatalf("30d window (%d) must cover every accounted turn (%d evaluated + %d legacy)",
			rep.Windows[3].Turns, rep.EvaluatedTurns, rep.LegacyCarriedTurns)
	}

	// Hourly series: bounded to the last 48h and hour-aligned; the 3d/20d turns contribute none.
	if len(rep.HourlySavings) == 0 || len(rep.HourlySavings) > 3 {
		t.Fatalf("expected 1..3 hourly buckets for turns inside 48h, got %d", len(rep.HourlySavings))
	}
	for _, hb := range rep.HourlySavings {
		if hb.Unix%3600 != 0 {
			t.Fatalf("hourly bucket %q is not hour-aligned (unix=%d)", hb.Hour, hb.Unix)
		}
		if hb.Unix < now-48*3600-3600 {
			t.Fatalf("hourly bucket %q falls outside the 48h series bound", hb.Hour)
		}
		if _, err := time.Parse("2006-01-02 15:00", hb.Hour); err != nil {
			t.Fatalf("hourly bucket key %q is not a clock hour: %v", hb.Hour, err)
		}
		if hb.Turns < 1 {
			t.Fatalf("hourly bucket %q has no turns", hb.Hour)
		}
	}
}

// TestEvaluatePersistsWindowsAndServesThemFromTheLedger pins the persistence contract the ROI
// screen depends on: the snapshot must carry the rolling windows and be re-readable verbatim
// without re-evaluating the turn ledger.
func TestEvaluatePersistsWindowsAndServesThemFromTheLedger(t *testing.T) {
	mem, cfg, tmp := monitorTestMemory(t)
	now := time.Now().Unix()
	seedMonitorTurn(t, mem, "sess-persist", now-120, "持久化账本 直读 校准", "已核对持久化账本直读。")

	first, err := efficacy.Evaluate(mem.Store, cfg, efficacy.Options{Scope: "mem:self", Limit: 20, HomeDir: tmp, WorkspaceDir: tmp})
	if err != nil {
		t.Fatalf("first Evaluate: %v", err)
	}
	ledgerPath := filepath.Join(tmp, "roi_ledger_v1_mem_self.json")
	raw, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatalf("ROI ledger snapshot not persisted at %s: %v", ledgerPath, err)
	}
	var snap struct {
		Version int `json:"version"`
		Report  struct {
			Windows       []map[string]any `json:"windows"`
			HourlySavings []map[string]any `json:"hourly_savings"`
			Partial       bool             `json:"partial"`
			DeferredTurns int              `json:"deferred_turns"`
		} `json:"report"`
	}
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("snapshot is not valid JSON: %v", err)
	}
	if len(snap.Report.Windows) != 4 {
		t.Fatalf("persisted snapshot must carry all four rolling windows, got %d", len(snap.Report.Windows))
	}
	if snap.Report.HourlySavings == nil {
		t.Fatalf("persisted snapshot must carry the hourly series array (even when empty)")
	}
	if !strings.Contains(string(raw), `"since_unix"`) || !strings.Contains(string(raw), `"billed_net_saved_tokens"`) {
		t.Fatalf("persisted windows lost their per-window metric keys")
	}

	second, err := efficacy.Evaluate(mem.Store, cfg, efficacy.Options{Scope: "mem:self", Limit: 20, HomeDir: tmp, WorkspaceDir: tmp})
	if err != nil {
		t.Fatalf("second Evaluate: %v", err)
	}
	if second.EvaluatedAt != first.EvaluatedAt {
		t.Fatalf("second read re-evaluated instead of serving the persisted ledger (%q != %q)", second.EvaluatedAt, first.EvaluatedAt)
	}
	if len(second.Windows) != len(first.Windows) {
		t.Fatalf("ledger-served report lost the rolling windows: %d != %d", len(second.Windows), len(first.Windows))
	}
	for i := range first.Windows {
		w1 := first.Windows[i]
		w2 := second.Windows[i]
		if abs64(w2.SinceUnix-w1.SinceUnix) > 2 {
			t.Fatalf("window %s SinceUnix drifted too far: %d vs %d", w1.Key, w2.SinceUnix, w1.SinceUnix)
		}
		w2.SinceUnix = w1.SinceUnix
		if w2 != w1 {
			t.Fatalf("ledger-served window %s drifted: %+v != %+v", w1.Key, w2, w1)
		}
	}

	// Direct TryLoadROILedger must hit (ok == true) when settled and Partial == false.
	if fastRep, ok := efficacy.TryLoadROILedger(mem.Store, cfg, efficacy.Options{Scope: "mem:self", WorkspaceDir: tmp}, "mem:self", cfg.Log, tmp, 20); !ok || fastRep == nil {
		t.Fatalf("expected TryLoadROILedger to hit on settled non-partial snapshot")
	}

	// Adversarial check: if the persisted snapshot is marked partial (e.g. budget expired mid-run),
	// TryLoadROILedger MUST NOT short-circuit (must return ok=false so the next run settles the rest).
	mutated := strings.Replace(string(raw), `"partial":false`, `"partial":true`, 1)
	if err := os.WriteFile(ledgerPath, []byte(mutated), 0o600); err != nil {
		t.Fatalf("write mutated snapshot: %v", err)
	}
	if _, ok := efficacy.TryLoadROILedger(mem.Store, cfg, efficacy.Options{Scope: "mem:self", WorkspaceDir: tmp}, "mem:self", cfg.Log, tmp, 20); ok {
		t.Fatalf("TryLoadROILedger must NEVER short-circuit on a partial=true snapshot")
	}
}

// TestEvaluateBudgetContract holds the persistence-guarantee invariants of the soft settling
// budget: an unbudgeted run never reports partial work, and a budgeted run always finalizes
// (never gets killed mid-flight) while flagging exactly the turns it deferred.
func TestEvaluateBudgetContract(t *testing.T) {
	mem, cfg, tmp := monitorTestMemory(t)
	now := time.Now().Unix()
	seedMonitorTurn(t, mem, "sess-budget-a", now-120, "预算闸 校准 甲", "已核对预算闸甲。")
	seedMonitorTurn(t, mem, "sess-budget-b", now-180, "预算闸 校准 乙", "已核对预算闸乙。")

	unbounded, err := efficacy.Evaluate(mem.Store, cfg, efficacy.Options{Scope: "mem:self", Limit: 20, HomeDir: tmp, WorkspaceDir: tmp})
	if err != nil {
		t.Fatalf("unbounded Evaluate: %v", err)
	}
	if unbounded.Partial || unbounded.DeferredTurns != 0 || unbounded.BudgetSeconds != 0 {
		t.Fatalf("unbudgeted run must settle everything: partial=%v deferred=%d budget=%d",
			unbounded.Partial, unbounded.DeferredTurns, unbounded.BudgetSeconds)
	}

	tempLedger := filepath.Join(tmp, "budget-case")
	if err := os.MkdirAll(tempLedger, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	bounded, err := efficacy.Evaluate(mem.Store, cfg, efficacy.Options{
		Scope: "mem:self", Limit: 20, HomeDir: tmp, WorkspaceDir: tmp, BudgetSeconds: 1,
	})
	if err != nil {
		t.Fatalf("budgeted Evaluate: %v", err)
	}
	if bounded.BudgetSeconds != 1 {
		t.Fatalf("budgeted run must echo its budget, got %d", bounded.BudgetSeconds)
	}
	if bounded.Partial != (bounded.DeferredTurns > 0) {
		t.Fatalf("partial flag must mirror the deferred turn count: partial=%v deferred=%d", bounded.Partial, bounded.DeferredTurns)
	}
	if bounded.EvaluatedAt == "" {
		t.Fatalf("budgeted run must still finalize a dated report")
	}
	if _, err := os.Stat(filepath.Join(tmp, "roi_ledger_v1_mem_self.json")); err != nil {
		t.Fatalf("budgeted run must persist its snapshot: %v", err)
	}
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
