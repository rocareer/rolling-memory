// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package core

// Temporary diagnostic (2026-11-23): measures the relative drift of
// sentenceEnergyMean when an append skips the whole-corpus recompute, and how
// often a term's df crosses the high-df classification boundary (the only case
// where effectiveEntryGroup can change, which is the exactness-breaking event).
// Drives the K2 epsilon decision; delete with the other diag instruments.

import (
	"fmt"
	"math"
	"os"
	"testing"
)

func TestZZSentenceEnergySkipDrift(t *testing.T) {
	if os.Getenv("RB_K2_DRIFT") != "1" {
		t.Skip("set RB_K2_DRIFT=1")
	}
	n := 300
	if v := os.Getenv("RB_K2_DRIFT_N"); v != "" {
		fmt.Sscanf(v, "%d", &n)
	}
	base := appendProbeDocs(n)
	eng := NewEngine(BuildIndex(base))
	eng.MaterializeClusterState()

	var crosses, appends int
	var maxRelDrift float64
	for i := 0; i < 10; i++ {
		// Snapshot the pre-append state.
		df0 := make(map[uint64]int, 64)
		thresh0 := eng.Index.HighDFThreshold()
		prevMean := eng.clusterState.sentenceEnergyMean

		d := base[i%len(base)]
		d.UnitID = fmt.Sprintf("k2-probe-%02d", i)
		d.OccurredAt += int64(i + 1)
		for h := range d.Terms {
			df0[h] = eng.Index.TermDF(h)
		}

		eng.AppendDocs([]Doc{d})
		eng.MaterializeClusterState()
		appends++

		thresh1 := eng.Index.HighDFThreshold()
		for h, d0 := range df0 {
			d1 := eng.Index.TermDF(h)
			if (d0 > thresh0) != (d1 > thresh1) {
				crosses++
			}
		}
		full := eng.clusterState.sentenceEnergyMean
		if full != 0 {
			if r := math.Abs(prevMean-full) / math.Abs(full); r > maxRelDrift {
				maxRelDrift = r
			}
		}
	}
	fmt.Printf("[k2-drift] appends=%d thresholdCrossings=%d maxRelDriftOfSkippedMean=%.3g\n",
		appends, crosses, maxRelDrift)
}
