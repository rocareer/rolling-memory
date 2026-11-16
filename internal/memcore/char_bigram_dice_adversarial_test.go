// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"math"
	"strings"
	"testing"
)

func TestCharBigramDiceSymmetricMultisetBeyondFormerCap(t *testing.T) {
	a, b := []rune("ab"), []rune(strings.Repeat("x", 33)+"ab")
	forward := CharBigramDice(a, b)
	reverse := CharBigramDice(b, a)
	want := 2.0 / 35.0
	if math.Abs(forward-want) > 1e-12 || math.Abs(reverse-want) > 1e-12 {
		t.Fatalf("CharBigramDice symmetry/value mismatch: forward=%.12f reverse=%.12f want=%.12f", forward, reverse, want)
	}
	if got := CharBigramDice(a, a); got != 1.0 {
		t.Fatalf("identity control failed: got %.12f want 1", got)
	}
}
