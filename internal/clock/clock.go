// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package clock is the single time source for the engine.
//
// Business packages must not call time.Now or time.Since directly; tests
// replace the wall and monotonic clocks through SetForTest.
package clock

import (
	"sync"
	"time"
)

var (
	mu          sync.RWMutex
	wall        time.Time
	hasWall     bool
	monoMS      float64
	hasMono     bool
	processBase = time.Now()
)

// Now returns the current wall time, or the frozen test value.
func Now() time.Time {
	mu.RLock()
	override, ok := wall, hasWall
	mu.RUnlock()
	if ok {
		return override
	}
	return time.Now()
}

// Unix returns whole Unix seconds, mirroring standard utc_now().
func Unix() int64 {
	return Now().Unix()
}

// MonotonicMS returns milliseconds from a process-relative monotonic clock.
func MonotonicMS() float64 {
	mu.RLock()
	override, ok := monoMS, hasMono
	mu.RUnlock()
	if ok {
		return override
	}
	return float64(time.Since(processBase).Nanoseconds()) / 1e6
}

// SetForTest freezes both clocks and returns a restore function.
func SetForTest(wallTime time.Time, monoMillis float64) (restore func()) {
	mu.Lock()
	prevWall, prevHasWall := wall, hasWall
	prevMono, prevHasMono := monoMS, hasMono
	wall, hasWall = wallTime, true
	monoMS, hasMono = monoMillis, true
	mu.Unlock()
	return func() {
		mu.Lock()
		wall, hasWall = prevWall, prevHasWall
		monoMS, hasMono = prevMono, prevHasMono
		mu.Unlock()
	}
}
