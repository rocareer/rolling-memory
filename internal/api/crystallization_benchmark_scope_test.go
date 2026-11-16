// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package api_test

import (
	"strings"
	"testing"
	"time"

	"github.com/rocareer/rolling-memory/internal/api"
)

// TestThinBrainRecallNeverCrystallizesInBenchmarkScope pins the product/evaluation boundary
// auto L3 Noetic crystallization is a product-lifecycle behavior and must
// never become a benchmark artifact. A recall whose effective scope lives in the isolated
// benchmark namespace must still deliver evidence, but write zero local `thin_vault_noetic`
// rows and emit zero `layer="noetic"` pulses — otherwise `/units` stops equalling the corpus
// and scoring starts depending on the order the questions happened to run in.
//
// The scope forms covered are exactly the ones a caller can actually reach: the canonical
// generation scope, the plaintext arm, the engine's incarnation-prefixed storage form, and the
// retired bare prefix.
func TestThinBrainRecallNeverCrystallizesInBenchmarkScope(t *testing.T) {
	mock, baseURL := newNoeticEngineMock(t)
	memory := thinBrainMemory(t, baseURL)

	content := "Glasgow hotel cost 120 GBP per night."
	if _, err := memory.Remember(api.RememberRequest{Content: &content, Session: "s1"}); err != nil {
		t.Fatalf("seed remember: %v", err)
	}
	baseline := len(mock.noeticPulses())

	scopes := []string{
		"u10-mem-eval-atmbench-20261107t161739z-187d",
		"u10-mem-eval-atmbench-plaintext-20261109t000232z-7917",
		"u10-mem-eval-locomo-local-20261107t153012z-7f3c",
		"ast:default:inc:u10-mem-eval-atmbench-20261107t161739z-187d",
		"mem-eval-u10-locomo",
	}
	for _, scope := range scopes {
		scope := scope
		result, err := memory.Recall(api.RecallRequest{
			Cue: "Glasgow hotel cost GBP", RawLimit: 5, Scope: &scope,
		})
		if err != nil {
			t.Fatalf("benchmark-scope recall %q: %v", scope, err)
		}
		// The read itself must keep working: the guard suppresses the write, never the retrieval.
		if len(result.Bands) == 0 {
			t.Fatalf("benchmark-scope recall %q delivered no bands", scope)
		}
		if got := result.Meta["crystallization"]; got != "disabled_eval_scope" {
			t.Fatalf("benchmark-scope recall %q meta.crystallization = %v，期望 disabled_eval_scope", scope, got)
		}
		if !api.WaitForPendingNoeticPulses(2 * time.Second) {
			t.Fatalf("benchmark-scope recall %q left a pending pulse", scope)
		}
		if got := len(mock.noeticPulses()); got != baseline {
			t.Fatalf("benchmark-scope recall %q emitted a noetic pulse (pulses=%d, baseline=%d)", scope, got, baseline)
		}
		var noetic int
		if err := memory.Store.DB().QueryRow(
			`SELECT count(*) FROM thin_vault_noetic WHERE status = 'active'`).Scan(&noetic); err != nil {
			t.Fatalf("local noetic table: %v", err)
		}
		if noetic != 0 {
			t.Fatalf("benchmark-scope recall %q wrote a local Noetic row (count=%d)", scope, noetic)
		}
	}

	// 正向对照：同样的调用换成普通用户 scope 必须照常自动结晶，否则这道闸就误伤了产品主线。
	normal, err := memory.Recall(api.RecallRequest{Cue: "Glasgow hotel cost GBP", RawLimit: 5})
	if err != nil {
		t.Fatalf("normal-scope recall: %v", err)
	}
	if _, suppressed := normal.Meta["crystallization"]; suppressed {
		t.Fatalf("normal-scope recall must not be reported as suppressed: %v", normal.Meta)
	}
	if !api.WaitForPendingNoeticPulses(5 * time.Second) {
		t.Fatal("normal-scope crystallization push did not drain")
	}
	if got := len(mock.noeticPulses()); got != baseline+1 {
		t.Fatalf("normal-scope recall must crystallize exactly one pulse, got %d (baseline=%d)", got, baseline)
	}
	var noetic int
	if err := memory.Store.DB().QueryRow(
		`SELECT count(*) FROM thin_vault_noetic WHERE status = 'active'`).Scan(&noetic); err != nil {
		t.Fatalf("local noetic table: %v", err)
	}
	if noetic != 1 {
		t.Fatalf("normal-scope recall must keep one local Noetic row, got %d", noetic)
	}
}

// TestRememberNoeticRefusesBenchmarkScope pins the explicit write path to the same rule:
// an explicit `RememberNoetic` inside the benchmark namespace is fail-closed, not silently
// written and later discovered by a `VerifyPrepared` foreign-unit failure.
func TestRememberNoeticRefusesBenchmarkScope(t *testing.T) {
	mock, baseURL := newNoeticEngineMock(t)
	memory := thinBrainMemory(t, baseURL)

	// Materialize the local vault schema up front, so the assertion below reads "zero rows"
	// rather than "no table yet" (the refused call never reaches ThinClient()).
	if _, err := memory.ThinClient(); err != nil {
		t.Fatalf("materialize thin client: %v", err)
	}

	_, err := memory.RememberNoetic("u10-mem-eval-atmbench-20261107t161739z-187d", "s1",
		"Glasgow hotel cost GBP", []string{"locomo-1"}, []string{"120 GBP"}, "120 GBP per night")
	if err == nil {
		t.Fatal("RememberNoetic accepted a benchmark scope")
	}
	if !strings.Contains(err.Error(), "benchmark namespace") {
		t.Fatalf("RememberNoetic benchmark refusal = %v，期望包含 benchmark namespace", err)
	}
	_ = api.WaitForPendingNoeticPulses(2 * time.Second)
	if got := len(mock.noeticPulses()); got != 0 {
		t.Fatalf("refused RememberNoetic still emitted %d noetic pulses", got)
	}
	var noetic int
	if err := memory.Store.DB().QueryRow(
		`SELECT count(*) FROM thin_vault_noetic WHERE status = 'active'`).Scan(&noetic); err != nil {
		t.Fatalf("local noetic table: %v", err)
	}
	if noetic != 0 {
		t.Fatalf("refused RememberNoetic still wrote %d local rows", noetic)
	}
}
