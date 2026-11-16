// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package tokenzr

import "testing"

// TestUnwrapApostrophelessContractionContract pins the colloquial contraction unwind contract:
// fast IM/chat typing drops apostrophes ("didnt", "youre", "weve"), and the unwinder must map them
// back to their canonical auxiliary/pronoun stems purely from 128k BPE clitic topology, with zero
// false positives on ordinary words sharing the same tail.
func TestUnwrapApostrophelessContractionContract(t *testing.T) {
	cases := map[string]string{
		"didnt":    "didn",
		"couldnt":  "couldn",
		"shouldnt": "shouldn",
		"wasnt":    "wasn",
		"doesnt":   "doesn",
		"havent":   "haven",
		"dont":     "don",
		// "wont" is intentionally absent: its stem "wo" is not a closed-class auxiliary base
		// in the 128k table, and a missed unwrap is the safe direction (no demotion either way).
		"youre":  "you",
		"theyre": "they",
		"weve":   "we",
		"theyll": "they",
	}
	for in, want := range cases {
		if got := UnwrapApostrophelessContraction(in); got != want {
			t.Errorf("UnwrapApostrophelessContraction(%q)=%q want %q", in, got, want)
		}
	}
	for _, in := range []string{"", "want", "plant", "point", "well", "shell", "wheels"} {
		if got := UnwrapApostrophelessContraction(in); got != "" {
			t.Errorf("UnwrapApostrophelessContraction(%q)=%q want no contraction", in, got)
		}
	}
}

// TestIsBPEContractedAuxStemContract pins the negation truncation-stem detection used to keep
// colloquial auxiliary fragments out of the Primary Gain Tier. Bare stems only: apostrophe-less
// forms ("didnt", "youre") and innocuous "-nt" words ("went", "bent") are deliberately not
// classified (see IsBPEContractedAuxStem doc), and missed demotions are the safe direction.
func TestIsBPEContractedAuxStemContract(t *testing.T) {
	for _, in := range []string{"didn", "wasn", "couldn", "wouldn", "shouldn", "doesn", "haven", "hadn", "aren", "weren", "isn", "don"} {
		if !IsBPEContractedAuxStem(in) {
			t.Errorf("IsBPEContractedAuxStem(%q)=false want true", in)
		}
	}
	for _, in := range []string{"", "want", "plant", "point", "went", "bent", "sent", "tent", "didnt", "couldnt", "youre", "theyre", "weve"} {
		if IsBPEContractedAuxStem(in) {
			t.Errorf("IsBPEContractedAuxStem(%q)=true want false", in)
		}
	}
}

// TestCollapsePeriodicSyllableReduplicationContract pins multi-cycle chat laughter collapsing
// to its 2-cycle tandem base while leaving 2-cycle and ordinary words untouched.
func TestCollapsePeriodicSyllableReduplicationContract(t *testing.T) {
	cases := map[string]string{
		"hahaha":   "haha",
		"hahahaha": "haha",
		"hehehe":   "hehe",
		"hihihi":   "hihi",
	}
	for in, want := range cases {
		if got := CollapsePeriodicSyllableReduplication(in); got != want {
			t.Errorf("CollapsePeriodicSyllableReduplication(%q)=%q want %q", in, got, want)
		}
	}
	for _, in := range []string{"", "haha", "hehe", "data", "mama"} {
		if got := CollapsePeriodicSyllableReduplication(in); got != "" {
			t.Errorf("CollapsePeriodicSyllableReduplication(%q)=%q want no collapse", in, got)
		}
	}
}

// TestIsBPEUninflectedTandemReduplicationContract pins the laughter taxonomy: uninflected
// tandem syllables are chat noise, while vocabulary-resident reduplicated words keep their tier.
func TestIsBPEUninflectedTandemReduplicationContract(t *testing.T) {
	for _, in := range []string{"haha", "hehe", "hihi", "xoxo", "hahaha", "hehehe"} {
		if !IsBPEUninflectedTandemReduplication(in) {
			t.Errorf("IsBPEUninflectedTandemReduplication(%q)=false want true", in)
		}
	}
	for _, in := range []string{"", "mama", "papa", "dada", "data"} {
		if IsBPEUninflectedTandemReduplication(in) {
			t.Errorf("IsBPEUninflectedTandemReduplication(%q)=true want false", in)
		}
	}
}
