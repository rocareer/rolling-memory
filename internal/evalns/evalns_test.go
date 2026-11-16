// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package evalns_test

import (
	"testing"

	"github.com/rocareer/rolling-memory/internal/evalns"
)

// TestSplitCanonicalBenchmarkScopes pins the canonical scope grammar that the evaluation
// pipelines build: every generation/mode form must resolve to the same suite axis.
func TestSplitCanonicalBenchmarkScopes(t *testing.T) {
	cases := []struct {
		scope      string
		wantSuite  string
		wantMode   string
		wantGen    string
		wantAccept bool
	}{
		{"u10-mem-eval-locomo", evalns.SuiteLoCoMo, evalns.ModePulseCipher, "", true},
		{"u10-mem-eval-atmbench", evalns.SuiteATMWire, evalns.ModePulseCipher, "", true},
		{"u10-mem-eval-locomo-pulse-cipher", evalns.SuiteLoCoMo, evalns.ModePulseCipher, "", true},
		{"u10-mem-eval-locomo-pulse-plain", evalns.SuiteLoCoMo, evalns.ModePulsePlain, "", true},
		{"u10-mem-eval-atmbench-local-plain", evalns.SuiteATMWire, evalns.ModeLocalPlain, "", true},
		{"u10-mem-eval-locomo-pulse-plain", evalns.SuiteLoCoMo, evalns.ModePulsePlain, "", true},
		{"u10-mem-eval-atmbench-local-plain", evalns.SuiteATMWire, evalns.ModeLocalPlain, "", true},
		{"u10-mem-eval-locomo-20261107t153012z-7f3c", evalns.SuiteLoCoMo, evalns.ModePulseCipher, "20261107t153012z-7f3c", true},
		{"u10-mem-eval-atmbench-pulse-plain-20261109t000232z-7917", evalns.SuiteATMWire, evalns.ModePulsePlain, "20261109t000232z-7917", true},
		{"u10-mem-eval-atmbench-local-plain-20261107t153012z-7f3c", evalns.SuiteATMWire, evalns.ModeLocalPlain, "20261107t153012z-7f3c", true},
		{"u10-mem-eval-atmbench-pulse-plain-20261109t000232z-7917", evalns.SuiteATMWire, evalns.ModePulsePlain, "20261109t000232z-7917", true},
		{"u10-mem-eval-atmbench-local-plain-20261107t153012z-7f3c", evalns.SuiteATMWire, evalns.ModeLocalPlain, "20261107t153012z-7f3c", true},
		{"user-mem-eval-locomo-gen1", evalns.SuiteLoCoMo, evalns.ModePulseCipher, "gen1", true},
		{"u10-mem-eval-locomo-", "", "", "", false},
		{"u10-mem-eval-atmbenchx", "", "", "", false},
		{"mem-eval-u10-locomo", "", "", "", false},
		{"-mem-eval-locomo", "", "", "", false},
		{"mem:self", "", "", "", false},
		{"default", "", "", "", false},
		{"u10-mem-eval-locomo-2026 1107", "", "", "", false},
		{"u10-mem-eval-locomo-x:", "", "", "", false},
		{"ast:default:inc:u10-mem-eval-atmbench", "", "", "", false},
	}
	for _, tc := range cases {
		suite, mode, gen, ok := evalns.Split(tc.scope)
		if ok != tc.wantAccept || suite != tc.wantSuite || mode != tc.wantMode || gen != tc.wantGen {
			t.Errorf("Split(%q) = (%q, %q, %q, %v)，期望 (%q, %q, %q, %v)",
				tc.scope, suite, mode, gen, ok, tc.wantSuite, tc.wantMode, tc.wantGen, tc.wantAccept)
		}
	}
}

// TestIsBenchmarkScopeGuardsProductSideEffects pins the deliberately wider predicate used
// by the product lifecycle: it must catch every benchmark form (including the engine's
// incarnation-prefixed storage form and the retired bare prefix) while every real user
// scope — including names that merely contain "eval" — must stay a normal scope.
func TestIsBenchmarkScopeGuardsProductSideEffects(t *testing.T) {
	cases := []struct {
		scope string
		want  bool
	}{
		// 评测空间：裸形态 / 世代形态 / 三模式 / 引擎落库形态 / 停用前缀。
		{"u10-mem-eval-locomo", true},
		{"u10-mem-eval-atmbench", true},
		{"u10-mem-eval-atmbench-20261107t131016z-1756", true},
		{"u10-mem-eval-atmbench-pulse-plain-20261109t000232z-7917", true},
		{"u10-mem-eval-atmbench-local-plain-20261107t153012z-7f3c", true},
		{"ast:default:inc:u10-mem-eval-atmbench-pulse-plain-20261109t000232z-7917", true},
		{"10-full1013-mem-eval-atmbench", true},
		{"mem-eval-u10-locomo", true},
		{"-mem-eval-locomo", true},
		// 用户空间（必须是 false，否则会误伤正常记忆的自动结晶）。
		{"mem:self", false},
		{"default", false},
		{"ast:default:inc:10", false},
		{"ast:default", false},
		{"10", false},
		{"mem:person:front-1", false},
		{"mem:person:front-10", false},
		{"s-f5-f6", false},
		{"mem-noetic-live-1793908529581823000", false},
		{"mem-live-e2e", false},
		{"probe-slots-shape-1", false},
		// 含 "eval" 字样的普通名字 / 非合法套件：不得误判为评测空间。
		{"u10-mem-eval-team", false},
		{"evaluation-notes", false},
		{"design:eval:notes", false},
	}
	for _, tc := range cases {
		if got := evalns.IsBenchmarkScope(tc.scope); got != tc.want {
			t.Errorf("IsBenchmarkScope(%q) = %v，期望 %v", tc.scope, got, tc.want)
		}
	}
}
