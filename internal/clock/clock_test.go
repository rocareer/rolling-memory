// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package clock

import (
	"testing"
	"time"
)

func TestSetForTestRestores(t *testing.T) {
	before := Unix()
	restore := SetForTest(time.Unix(1793102400, 0), 1234.5)
	if got := Unix(); got != 1793102400 {
		t.Fatalf("Unix = %d, want 1793102400", got)
	}
	if got := MonotonicMS(); got != 1234.5 {
		t.Fatalf("MonotonicMS = %v, want 1234.5", got)
	}
	restore()
	if got := MonotonicMS(); got == 1234.5 {
		t.Fatalf("MonotonicMS still frozen after restore")
	}
	if after := Unix(); after < before {
		t.Fatalf("Unix went backwards: %d -> %d", before, after)
	}
}
