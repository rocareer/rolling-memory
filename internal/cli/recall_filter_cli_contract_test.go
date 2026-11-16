// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli_test

import (
	"strings"
	"testing"
)

func TestRecallFiltersFailClosedFromCLI(t *testing.T) {
	newEnv(t)
	seedTranscript(t)
	for _, mode := range []string{"full", "probe", "recent"} {
		for _, tc := range []struct {
			name string
			args []string
		}{
			{"session", []string{"--session", "other"}},
			{"empty-session", []string{"--session", ""}},
			{"asof", []string{"--asof", "2026-01-01"}},
			{"empty-asof", []string{"--asof", ""}},
			{"window-start", []string{"--window-start", "2026-01-01"}},
			{"window-end", []string{"--window-end", "2026-01-01"}},
			{"combined", []string{"--session", "other", "--asof", "2026-01-01", "--window-start", "2025-01-01", "--window-end", "2026-01-01"}},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				args := append([]string{"recall", "--q", "小白", "--result-mode", mode, "--json"}, tc.args...)
				result := run(t, args...)
				payload := jsonObject(t, result.stdout)
				message, _ := payload["error"].(string)
				if result.code != 1 || !strings.Contains(message, "cannot enforce") && !strings.Contains(message, "does not support") {
					t.Fatalf("%v: exit %d, stdout %q, stderr %q; want unsupported error", args, result.code, result.stdout, result.stderr)
				}
				if _, ok := payload["bands"]; ok {
					t.Fatalf("filtered recall returned bands: %s", result.stdout)
				}
				if _, ok := payload["raw_hits"]; ok {
					t.Fatalf("filtered recall returned raw hits: %s", result.stdout)
				}
			})
		}
		result := run(t, "recall", "--q", "小白", "--result-mode", mode, "--json")
		if result.code != 0 {
			t.Fatalf("unfiltered %s: exit %d, stdout %q, stderr %q", mode, result.code, result.stdout, result.stderr)
		}
	}
}
