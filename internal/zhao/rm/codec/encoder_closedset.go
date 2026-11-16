// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package codec

import "github.com/rocareer/rolling-memory/internal/zhao/rm/closedset"

// StandardClosedSetTable 與 UniversalSchemaClosedSetTable 真实正本位于领域子目录
// src/zhao/rm/closedset，此处导出兼容引用。
var (
	StandardClosedSetTable        = closedset.StandardClosedSetTable
	UniversalSchemaClosedSetTable = closedset.UniversalSchemaClosedSetTable
	ClosedSetHarmonicEntries      = closedset.ClosedSetHarmonicEntries
)

func LookupISO3166GeoHarmonicPair(token string) (string, string) {
	return closedset.LookupGeoPair(token)
}

func LookupISO3166GeoHarmonics(token string) []string {
	return closedset.LookupGeoHarmonics(token)
}

func LookupUnitDimensionHarmonicPair(token string) (string, string) {
	return closedset.LookupUnitPair(token)
}

func lookupFirstClosedSetDictTag(low string) string {
	return closedset.LookupFirstTag(low)
}

func LookupClosedSetHarmonics(token string) []string {
	return closedset.LookupHarmonics(token)
}

func CombineHarmonicExpanders(es ...HarmonicExpander) HarmonicExpander {
	active := make([]HarmonicExpander, 0, len(es))
	for _, e := range es {
		if e != nil {
			active = append(active, e)
		}
	}
	if len(active) == 0 {
		return nil
	}
	return func(token string) []string {
		var out []string
		for _, e := range active {
			for _, tag := range e(token) {
				if tag == "" {
					continue
				}
				dup := false
				for _, prev := range out {
					if prev == tag {
						dup = true
						break
					}
				}
				if !dup {
					out = append(out, tag)
				}
			}
		}
		return out
	}
}
