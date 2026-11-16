// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package text

import "testing"

// TestIsHighGainTokenColloquialDemotion pins that colloquial chat tokens (laughter, expressive
// elongation) never enter the Primary Gain Tier, while numeric/currency tokens remain high-gain.
// Apostrophe-less contractions are deliberately not demoted (see tokenzr.IsBPEContractedAuxStem).
func TestIsHighGainTokenColloquialDemotion(t *testing.T) {
	for _, in := range []string{"hahaha", "hehehe", "sooooo", "ummmmm"} {
		if IsHighGainToken(in) {
			t.Errorf("IsHighGainToken(%q)=true want false (colloquial token must stay out of Primary Gain Tier)", in)
		}
	}
	for _, in := range []string{"$5", "#42", "５"} {
		if !IsHighGainToken(in) {
			t.Errorf("IsHighGainToken(%q)=false want true (numeric/currency token)", in)
		}
	}
}

// TestTokenizeSkipsCliticContractionJoin pins that grammatical clitic contractions are never
// fabricated into joined pseudo-forms, while apostrophe-embedded proper names still join.
func TestTokenizeSkipsCliticContractionJoin(t *testing.T) {
	got := Tokenize("we've they'll you're Xi'an")
	joined := map[string]bool{}
	for _, tk := range got {
		joined[tk] = true
	}
	for _, bad := range []string{"weve", "theyll", "youre"} {
		if joined[bad] {
			t.Errorf("Tokenize fabricated clitic join %q from %v", bad, got)
		}
	}
	if !joined["xian"] {
		t.Errorf("Tokenize must keep joined proper name 'xian', got %v", got)
	}
}
