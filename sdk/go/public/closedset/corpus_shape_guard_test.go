// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package closedset

import "testing"

// TestCorpusShapedMicroToponymsStayOut is the regression guard for the
// evaluation-corpus-shaped micro-toponyms removed from StandardClosedSetTable
// on 2026-11-18 (paired-run record in docs/CHANGELOG.md): their inclusion
// traced to a specific evaluation corpus rather than to a public
// world-knowledge criterion. Closed-set rows feed the harmonic channel on both
// the document and the query side, so re-adding any of them changes the
// inverted-index population and requires the full paired-run protocol
// (same pool, single variable, multi-corpus) before landing.
func TestCorpusShapedMicroToponymsStayOut(t *testing.T) {
	removed := []string{
		// Part I GB row (Cornwall micro-cluster).
		"petersfield", "marazion", "penzance", "ives", "scilly",
		"newquay", "truro", "falmouth",
		// Part I FR row (Provence micro-communes).
		"aubagne", "cassis", "marignane", "istres", "pertuis",
		"la-seyne-sur-mer", "seyne-sur-mer", "sablettes", "saint-elme",
		// Dead misspelling of the collapsed "le havre" form.
		"leharve",
	}
	for _, tok := range removed {
		if HasToken(tok) {
			t.Fatalf("corpus-shaped toponym %q re-entered the closed set (tag=%q)", tok, LookupFirstTag(tok))
		}
	}
	// Sanity: universally documented places in the same rows still resolve.
	if got1, got2 := LookupGeoPair("cambridge"); got1 != "@geo:cc:gb" || got2 != "@geo:cont:eu" {
		t.Fatalf("cambridge = (%q,%q), want (@geo:cc:gb,@geo:cont:eu)", got1, got2)
	}
	if got1, got2 := LookupGeoPair("marseille"); got1 != "@geo:cc:fr" || got2 != "@geo:cont:eu" {
		t.Fatalf("marseille = (%q,%q), want (@geo:cc:fr,@geo:cont:eu)", got1, got2)
	}
}
