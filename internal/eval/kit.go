// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package eval implements the isolated cloud-SDK benchmark harness.
package eval

import "sync"

const (
	// Scope is the LoCoMo cloud-evaluation scope for the default development tenant.
	// Other tenants derive their own isolated scope with ScopeForSuite.
	Scope = "u10-mem-eval-locomo"
)

// Str mirrors standard str() for the value types the harness carries.
func Str(value any) string { return pyStr(value) }

// Truthy mirrors standard truthiness for the value types the harness carries.
func Truthy(value any) bool { return truthy(value) }

// TruncateRunes mirrors text[:limit] (code points, not bytes).
func TruncateRunes(text string, limit int) string { return firstRunes(text, limit) }

// ParallelMap runs fn for indexes [0,count) with at most workers goroutines
// and returns results in index order (ThreadPoolExecutor + futures order).
func ParallelMap[T any](count, workers int, fn func(index int) T) []T {
	results := make([]T, count)
	if count == 0 {
		return results
	}
	if workers < 1 {
		workers = 1
	}
	if workers > count {
		workers = count
	}
	next := make(chan int)
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for index := range next {
				results[index] = fn(index)
			}
		}()
	}
	for index := 0; index < count; index++ {
		next <- index
	}
	close(next)
	wait.Wait()
	return results
}
