// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package efficacy_test

import (
	"path/filepath"
	"testing"

	"github.com/rocareer/rolling-memory/internal/api"
	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/efficacy"
	"github.com/rocareer/rolling-memory/internal/store"
)

func TestEvaluateCrossHarnessEfficacy(t *testing.T) {
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
	defer mem.Close()

	// Seed a high-value assertion with a code coordinate.
	_, err = mem.Remember(api.RememberRequest{
		Content: strPtr("帮助中心路由定为 views/admin/help/index.vue 与 repos.vue。"),
		Assertion: map[string]any{
			"s":          map[string]any{"label": "帮助中心路由"},
			"x":          "定为",
			"o":          map[string]any{"literal": "views/admin/help/index.vue 与 repos.vue"},
			"confidence": 0.99,
			"warrant":    "performative",
		},
		AnchorText: strPtr("views/admin/help/index.vue"),
		Session:    "seed-session",
		Author:     "user",
	})
	if err != nil {
		t.Fatalf("remember seed: %v", err)
	}

	// 1. Simulate a Claude Code session that queries 帮助中心路由 and adopts views/admin/help/index.vue.
	now := int64(1793357000)
	_, _, _ = mem.Store.AddTrace("mem:self", "请修改帮助中心路由的样式", store.TraceOptions{
		Session:    "claude-sess-1",
		Turn:       1,
		Kind:       "message",
		Author:     "user",
		Audience:   "shared",
		OccurredAt: &now,
		Meta:       map[string]any{"world": "harness:claude-code", "locus": "rocareer/rolling-memory"},
	})
	_, _, _ = mem.Store.AddTrace("mem:self", "已直接读取并更新 views/admin/help/index.vue", store.TraceOptions{
		Session:    "claude-sess-1",
		Turn:       2,
		Kind:       "message",
		Author:     "assistant",
		Audience:   "shared",
		OccurredAt: &now,
		Meta:       map[string]any{"world": "harness:claude-code", "locus": "rocareer/rolling-memory"},
	})

	// 2. Simulate a ZCode session with a pure ack prompt ("全部同意").
	now2 := now + 10
	_, _, _ = mem.Store.AddTrace("mem:self", "全部同意", store.TraceOptions{
		Session:    "zcode-sess-2",
		Turn:       1,
		Kind:       "message",
		Author:     "user",
		Audience:   "shared",
		OccurredAt: &now2,
		Meta:       map[string]any{"world": "harness:zcode", "locus": "rocareer/rolling-memory"},
	})

	// 3. Simulate a Codex session with an unrelated external question.
	now3 := now + 20
	_, _, _ = mem.Store.AddTrace("mem:self", "google 提供的 gemini 官方价格是多少? 我有 google 账号 如何购买?", store.TraceOptions{
		Session:    "codex-sess-3",
		Turn:       1,
		Kind:       "message",
		Author:     "user",
		Audience:   "shared",
		OccurredAt: &now3,
		Meta:       map[string]any{"world": "harness:codex", "locus": "rocareer/rolling-memory"},
	})

	// 4. Simulate a prompt-echo turn where the user ALREADY typed views/admin/help/index.vue in the prompt:
	//    L1 Anti-Echo must strip views/admin/help/index.vue from novel code coordinates (echo_excluded >= 1).
	now4 := now + 30
	_, _, _ = mem.Store.AddTrace("mem:self", "请直接检查帮助中心路由 views/admin/help/index.vue 的按钮", store.TraceOptions{
		Session:    "zcode-echo-sess",
		Turn:       1,
		Kind:       "message",
		Author:     "user",
		Audience:   "shared",
		OccurredAt: &now4,
		Meta:       map[string]any{"world": "harness:zcode", "locus": "rocareer/rolling-memory"},
	})
	_, _, _ = mem.Store.AddTrace("mem:self", "已检查 views/admin/help/index.vue", store.TraceOptions{
		Session:    "zcode-echo-sess",
		Turn:       2,
		Kind:       "message",
		Author:     "assistant",
		Audience:   "shared",
		OccurredAt: &now4,
		Meta:       map[string]any{"world": "harness:zcode", "locus": "rocareer/rolling-memory"},
	})

	// 5. Simulate an L2 piggybacked self-healing writeback (`closes_prior: true`) in a live session.
	now5 := now + 40
	_, err = mem.Remember(api.RememberRequest{
		Content: strPtr("帮助中心路由已迁移至 views/admin/help/v2.vue。"),
		Assertion: map[string]any{
			"s":            map[string]any{"label": "帮助中心路由"},
			"x":            "定为",
			"o":            map[string]any{"literal": "views/admin/help/v2.vue"},
			"confidence":   0.99,
			"warrant":      "performative",
			"closes_prior": true,
		},
		AnchorText: strPtr("views/admin/help/v2.vue"),
		Session:    "claude-sess-1",
		Turn:       3,
		Author:     "assistant",
		OccurredAt: now5,
	})
	if err != nil {
		t.Fatalf("remember self-heal: %v", err)
	}

	// 6. Seed a second distinct module constraint (dashboard.vue icon redline) and test
	//    Stateful Delta-Band Carry-Over on a short continuation turn ("同意 实施"):
	//    - Turn 1: user asks general architecture question; assistant proposes editing views/admin/dashboard.vue;
	//    - Turn 2: user types ONLY "同意 实施" (4 chars). Because prior assistant reply named views/admin/dashboard.vue
	//      and its band has NOT yet been injected in this session, Delta-Band Carry-Over injects it!
	_, err = mem.Remember(api.RememberRequest{
		Content: strPtr("实时看板图标规范落位于 views/admin/dashboard.vue 必须使用全小写 el-circle-check。"),
		Assertion: map[string]any{
			"s":          map[string]any{"label": "实时看板图标规范"},
			"x":          "落位于",
			"o":          map[string]any{"literal": "views/admin/dashboard.vue 必须使用全小写 el-circle-check"},
			"confidence": 0.99,
			"warrant":    "performative",
		},
		AnchorText: strPtr("views/admin/dashboard.vue"),
		Session:    "seed-session",
		Author:     "user",
	})
	if err != nil {
		t.Fatalf("remember dashboard seed: %v", err)
	}
	now6 := now + 50
	_, _, _ = mem.Store.AddTrace("mem:self", "为什么状态栏没有刷新", store.TraceOptions{
		Session:    "zcode-carryover-sess",
		Turn:       1,
		Kind:       "message",
		Author:     "user",
		Audience:   "shared",
		OccurredAt: &now6,
		Meta:       map[string]any{"world": "harness:zcode", "locus": "rocareer/rolling-memory"},
	})
	_, _, _ = mem.Store.AddTrace("mem:self", "我们需要修改 views/admin/dashboard.vue 的轮询逻辑", store.TraceOptions{
		Session:    "zcode-carryover-sess",
		Turn:       2,
		Kind:       "message",
		Author:     "assistant",
		Audience:   "shared",
		OccurredAt: &now6,
		Meta:       map[string]any{"world": "harness:zcode", "locus": "rocareer/rolling-memory"},
	})
	now7 := now + 60
	_, _, _ = mem.Store.AddTrace("mem:self", "图标也改", store.TraceOptions{
		Session:    "zcode-carryover-sess",
		Turn:       3,
		Kind:       "message",
		Author:     "user",
		Audience:   "shared",
		OccurredAt: &now7,
		Meta:       map[string]any{"world": "harness:zcode", "locus": "rocareer/rolling-memory"},
	})
	_, _, _ = mem.Store.AddTrace("mem:self", "已直接编辑 views/admin/dashboard.vue 并遵循 el-circle-check 小写规范", store.TraceOptions{
		Session:    "zcode-carryover-sess",
		Turn:       4,
		Kind:       "message",
		Author:     "assistant",
		Audience:   "shared",
		OccurredAt: &now7,
		Meta:       map[string]any{"world": "harness:zcode", "locus": "rocareer/rolling-memory"},
	})

	rep, err := efficacy.Evaluate(mem.Store, cfg, efficacy.Options{
		Scope:        "mem:self",
		Limit:        15,
		HomeDir:      tmp,
		WorkspaceDir: tmp,
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if rep.TotalSessions != 5 || rep.EvaluatedTurns != 6 {
		t.Fatalf("expected 5 sessions / 6 turns, got sessions=%d turns=%d", rep.TotalSessions, rep.EvaluatedTurns)
	}
	if rep.AdoptedCount < 2 || rep.CodeCoordHits < 2 || rep.CausalDirectHits < 2 {
		for i, s := range rep.Sessions {
			t.Logf("session[%d]: id=%s prompt=%q outcome=%s verdict=%q anchors=%v", i, s.SessionID, s.Prompt, s.Outcome, s.VerdictLabel, s.MatchedAnchors)
		}
		t.Fatalf("expected >=2 causal code-coordinate hits (including carry-over turn), got adopted=%d coord=%d causal=%d",
			rep.AdoptedCount, rep.CodeCoordHits, rep.CausalDirectHits)
	}
	if rep.EchoExcludedCount < 1 {
		t.Fatalf("expected prompt-echo anchor to be stripped (echo_excluded_count >= 1), got %d", rep.EchoExcludedCount)
	}
	if rep.SelfHealedCount < 1 || rep.WritebackCount < 1 {
		t.Fatalf("expected L2 piggybacked self-healing count >= 1, got self_healed=%d writebacks=%d", rep.SelfHealedCount, rep.WritebackCount)
	}
	if rep.SilencedCount < 2 {
		t.Fatalf("expected ack and unrelated prompts to be silenced (>=2), got silenced=%d", rep.SilencedCount)
	}
	if rep.TotalSavedRounds < 2 {
		t.Fatalf("expected saved rounds >= 2, got %d", rep.TotalSavedRounds)
	}
	if len(rep.Harnesses) < 8 {
		t.Fatalf("expected all 8 standard harnesses in matrix, got %d", len(rep.Harnesses))
	}

	// Verify funnel conservation sum(Funnel) == EvaluatedTurns.
	funnelSum := rep.Funnel.HighValueAdopted + rep.Funnel.ContextAssisted + rep.Funnel.DedupSuppressed + rep.Funnel.SilenceFiltered + rep.Funnel.NoiseInjected
	if funnelSum != rep.EvaluatedTurns {
		t.Fatalf("expected funnel sum (%d) == evaluated_turns (%d)", funnelSum, rep.EvaluatedTurns)
	}
}

func strPtr(s string) *string { return &s }
