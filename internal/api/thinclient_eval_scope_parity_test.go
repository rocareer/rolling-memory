// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package api

import (
	"slices"
	"testing"

	"github.com/rocareer/rolling-memory/internal/evalns"
	"github.com/rocareer/rolling-memory/internal/thinclient"
)

// TestThinClientSiblingExcludeTokensMatchEvalNamespace pins the drift guard between the
// daily-recall exclude list and the evaluation-namespace authority (internal/evalns):
// the thin client must ship 「标记 + 合法套件名」shapes for every live suite, so an
// ordinary recall never joins evaluation partitions, while user scopes that merely
// contain "eval" (e.g. u10-mem-eval-team) stay untouched.
func TestThinClientSiblingExcludeTokensMatchEvalNamespace(t *testing.T) {
	want := []string{evalns.Marker + evalns.SuiteLoCoMo, evalns.Marker + evalns.SuiteATMWire}
	got := thinclient.DefaultSiblingExcludeScopeTokens()
	if !slices.Equal(got, want) {
		t.Fatalf("DefaultSiblingExcludeScopeTokens() = %#v, want evalns-aligned %#v", got, want)
	}
}
