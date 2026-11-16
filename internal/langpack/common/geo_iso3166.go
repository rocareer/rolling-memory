// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package common — ISO 3166 L0–L3 Multilingual Geographic Hierarchy Harmonic Table.
//
// Delegates to the canonical client SDK (`ramesdk.LookupISO3166GeoHarmonicPair`) so that
// all 4 language SDKs and the Go `langpack` companion suite share a single source of truth.
package common

import (
	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

// LookupISO3166GeoHarmonics resolves a lowercase token into its canonical ISO 3166
// 1st-order (@geo:cc:<iso2> or @geo:cont:<cont>) and 2nd-order (@geo:cont:<cont> or @dim:geo)
// geographic harmonic tags.
func LookupISO3166GeoHarmonics(low string) (geoTag1 string, geoTag2 string) {
	return ramesdk.LookupISO3166GeoHarmonicPair(low)
}
