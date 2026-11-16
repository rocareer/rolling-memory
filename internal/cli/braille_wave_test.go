// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli_test

import (
	"strings"
	"testing"
)

func TestBrailleWaveCommandAndFlags(t *testing.T) {
	newEnv(t)

	// 1. Standalone `rmem wave <text>` renders a 4-row Unicode Braille oscilloscope
	res := run(t, "wave", "The Hilton Glasgow hotel stay for BMVC2024 costs 655.59 GBP.")
	if res.code != 0 {
		t.Fatalf("wave exit=%d stderr=%s", res.code, res.stderr)
	}
	if !strings.Contains(res.stdout, "Memory Engine Oscilloscope") || !strings.Contains(res.stdout, "Peaks:") {
		t.Fatalf("unexpected wave stdout:\n%s", res.stdout)
	}
	hasBraille := false
	for _, r := range res.stdout {
		if r >= 0x2801 && r <= 0x28FF {
			hasBraille = true
			break
		}
	}
	if !hasBraille {
		t.Fatalf("expected non-empty Unicode Braille characters (U+2801..U+28FF) in:\n%s", res.stdout)
	}
	t.Logf("Rendered Terminal Braille Oscilloscope:\n%s", res.stdout)

	// 2. `rmem wave --json` returns structured JSON
	jRes := run(t, "wave", "--json", "BMVC2024 Glasgow 655.59")
	if jRes.code != 0 {
		t.Fatalf("wave --json exit=%d stderr=%s", jRes.code, jRes.stderr)
	}
	obj := jsonObject(t, jRes.stdout)
	if obj["hex"] == "" {
		t.Fatalf("expected non-empty hex in wave --json: %+v", obj)
	}

	// 3. `rmem status --wave` appends the Braille oscilloscope while `rmem status` stays clean
	stWave := run(t, "status", "--wave")
	if stWave.code != 0 || !strings.Contains(stWave.stdout, "Status Telemetry Wave") {
		t.Fatalf("status --wave failed: code=%d stdout=%s stderr=%s", stWave.code, stWave.stdout, stWave.stderr)
	}

	// 4. `rmem recall --q ... --wave` renders the query Braille wave above recall results
	recWave := run(t, "recall", "--q", "BMVC2024 Glasgow", "--wave")
	if recWave.code != 0 || !strings.Contains(recWave.stdout, "Recall Query Wave") {
		t.Fatalf("recall --wave failed: code=%d stdout=%s stderr=%s", recWave.code, recWave.stdout, recWave.stderr)
	}
}
