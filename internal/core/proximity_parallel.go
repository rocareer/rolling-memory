// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.org.cn>

package core

import (
	"runtime"
	"sync"
	"sync/atomic"
)

// This adapter owns the worker pool for the per-candidate proximity/synergy
// pre-pass. The portable core subset stays free of runtime and sync imports;
// pipeline_cluster.go reaches this pool through proximityEntityDispatch and
// keeps a serial fallback when the hook is absent.
//
// Each candidate is independent: the worker writes only candidates[u].entity,
// reads the shared read-only cluster state, and uses no query scratch. The
// parallel and serial paths therefore produce bit-identical values; the peak
// fold runs after the dispatch in dirty-unit order.

// proximityWorkersOverride pins the worker count for A/B measurement; 0 uses
// the runtime default (GOMAXPROCS).
var proximityWorkersOverride int

// proximityUnitEvaluations counts evaluated candidate indices when a test pins
// the worker count, so tests can assert the parallel path actually ran.
var proximityUnitEvaluations atomic.Int64

func init() {
	proximityEntityDispatch = dispatchProximityEntity
}

// SetProximityWorkers pins the proximity worker count; n<=0 restores the
// runtime default. Measurement only; production never calls it.
func SetProximityWorkers(n int) {
	proximityWorkersOverride = n
}

func evalProximityEntity(u int32, coreLex []float64, candidates []rmCandidate, gates []float64, st *rmClusterState, groups map[uint64]float64, total, residual float64) {
	if proximityWorkersOverride > 0 {
		proximityUnitEvaluations.Add(1)
	}
	c := &candidates[u]
	if len(c.matches) < 2 || coreLex[u] <= 0 {
		return
	}
	gate := gateAt(gates, u)
	prox, syn := st.rmProximityAndSynergy(c, groups, total)
	c.entity += gate * (residual*prox + syn)
}

func dispatchProximityEntity(units []int32, coreLex []float64, candidates []rmCandidate, gates []float64, st *rmClusterState, groups map[uint64]float64, total, residual float64) {
	n := len(units)
	if n == 0 {
		return
	}
	workers := proximityWorkersOverride
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	if workers > n {
		workers = n
	}
	// Below four evaluations per worker the fixed goroutine spawn and wakeup
	// cost dominates the parallel gain; keep identical coverage in one loop.
	if workers < 2 || n <= 4*workers {
		for _, u := range units {
			evalProximityEntity(u, coreLex, candidates, gates, st, groups, total, residual)
		}
		return
	}
	var wg sync.WaitGroup
	// Round-robin static partition: each worker strides the index, so no shared
	// cursor is contended. A single atomic counter would serialise all workers
	// on one cache line (the profile showed the resulting park/unpark traffic
	// as pthread_cond_signal plus usleep), while round-robin keeps balance for
	// the early-return candidates that dominate the list.
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := w; i < n; i += workers {
				evalProximityEntity(units[i], coreLex, candidates, gates, st, groups, total, residual)
			}
		}(w)
	}
	wg.Wait()
}
