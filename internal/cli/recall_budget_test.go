// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli_test

import (
	"strings"
	"testing"
)

func TestRecallBudgetBounds(t *testing.T) {
	newEnv(t)
	for _, args := range [][]string{
		{"recall", "--result-mode", "recent", "--raw-limit", "65"},
		{"recall", "--result-mode", "recent", "--raw-limit", "-1"},
		{"recall", "--result-mode", "recent", "--bands", "65"},
	} {
		result := run(t, args...)
		if result.code == 0 || !strings.Contains(result.stderr, "64") {
			t.Fatalf("%v: exit %d, stderr %q", args, result.code, result.stderr)
		}
	}
}
