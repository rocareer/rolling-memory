// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

// 0–96 主带单调凸边际信号预算律（基础带装配不变量，零高级算子依赖）。
// MonotonicConvexBandPass evaluates the 0–96 band monotonic convex marginal signal budget law
// without dividing by floating global peak scores (preserving strict monotonicity under query expansion).
func MonotonicConvexBandPass(bi, totalBands, rowCount, connAccepted, unconnAccepted int, isDirectConn, isTraceConn, hasNamed bool, maxNS float64) (pass bool, incConn, incUnconn bool) {
	if bi == 0 {
		return true, false, false
	}
	isConn := isDirectConn || isTraceConn
	if isConn {
		if bi == 1 || totalBands <= 4 {
			pass = true
		} else if connAccepted < 4 && maxNS >= 4.5 {
			pass = true
		} else if connAccepted < 5 && rowCount <= 4 && maxNS >= 4.0 {
			pass = true
		} else {
			floor := 24.0 + 7.0*float64(connAccepted-4)
			if rowCount <= 4 && isDirectConn {
				floor = 8.0 + 3.5*float64(connAccepted-4)
			}
			if maxNS >= floor {
				pass = true
			}
		}
		if pass {
			return true, bi >= 2, false
		}
		return false, false, false
	}
	unconnFloor := 11.0
	if unconnAccepted >= 2 {
		unconnFloor = 24.0 + 8.0*float64(unconnAccepted-1)
	}
	if rowCount <= 4 && maxNS >= unconnFloor {
		return true, false, true
	}
	if !hasNamed && maxNS >= 9.0+6.0*float64(unconnAccepted) {
		return true, false, true
	}
	return false, false, false
}
