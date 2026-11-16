// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"sync/atomic"
	"time"
)

// clockNow is the single, overridable wall-clock source for read-side time-dependent operators
// (e.g. the Zeigarnik prospective-loop urgency ramp and post-horizon decay envelope).
// Production uses the real wall clock; tests inject a fixed clock through SetNowUnixForTest so that
// read-side scoring and rendering stay deterministic across runs.
var clockNow atomic.Pointer[func() int64]

// InjectedNowUnix returns the test-injected reference timestamp if one has been set via
// SetNowUnixForTest; otherwise it returns (0, false) so pure functions can derive their
// reference clock deterministically from the input corpus without reading the host wall clock.
func InjectedNowUnix() (int64, bool) {
	if fn := clockNow.Load(); fn != nil {
		return (*fn)(), true
	}
	return 0, false
}

// NowUnix returns the kernel's current reference timestamp (seconds since epoch).
func NowUnix() int64 {
	if fn := clockNow.Load(); fn != nil {
		return (*fn)()
	}
	return time.Now().Unix()
}

// SetNowUnixForTest replaces the kernel wall clock with fn and returns a restore function.
// Callers must defer the restore function (typically through t.Cleanup).
func SetNowUnixForTest(fn func() int64) (restore func()) {
	previous := clockNow.Load()
	clockNow.Store(&fn)
	return func() { clockNow.Store(previous) }
}
