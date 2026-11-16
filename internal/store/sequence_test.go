// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package store

import (
	"encoding/json"
	"os"
	"testing"
)

type sequenceRatioVectors struct {
	AutojunkDefault bool `json:"autojunk_default"`
	Cases           []struct {
		Name            string  `json:"name"`
		Left            string  `json:"left"`
		Right           string  `json:"right"`
		Ratio           float64 `json:"ratio"`
		RatioNoAutojunk float64 `json:"ratio_no_autojunk"`
	} `json:"cases"`
}

func TestGoldenSequenceRatio(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/golden/vectors/sequence-ratio.json")
	if err != nil {
		t.Fatalf("read sequence-ratio.json: %v", err)
	}
	var vectors sequenceRatioVectors
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatalf("decode sequence-ratio.json: %v", err)
	}
	if !vectors.AutojunkDefault {
		t.Fatal("autojunk_default = false")
	}
	for _, testCase := range vectors.Cases {
		testCase := testCase
		t.Run(testCase.Name, func(t *testing.T) {
			got := sequenceRatio([]rune(testCase.Left), []rune(testCase.Right), true)
			if got != testCase.Ratio {
				t.Fatalf("ratio(%q,%q) = %v, want %v", testCase.Left, testCase.Right, got, testCase.Ratio)
			}
			gotNoJunk := sequenceRatio([]rune(testCase.Left), []rune(testCase.Right), false)
			if gotNoJunk != testCase.RatioNoAutojunk {
				t.Fatalf("ratio_no_autojunk(%q,%q) = %v, want %v",
					testCase.Left, testCase.Right, gotNoJunk, testCase.RatioNoAutojunk)
			}
		})
	}
}

func TestSequenceRatioEmptyAndIdentical(t *testing.T) {
	if ratio := sequenceRatio(nil, nil, true); ratio != 1.0 {
		t.Fatalf("empty ratio = %v, want 1.0", ratio)
	}
	if ratio := sequenceRatio([]rune("老张养猫"), []rune("老张养猫"), true); ratio != 1.0 {
		t.Fatalf("identical ratio = %v, want 1.0", ratio)
	}
	if ratio := sequenceRatio([]rune("老张"), []rune("老张养猫"), true); ratio != 0.6666666666666666 {
		t.Fatalf("prefix ratio = %v", ratio)
	}
}

func TestPyRoundMatchesPython(t *testing.T) {
	for _, testCase := range []struct {
		value  float64
		digits int
		want   float64
	}{
		{2.675, 2, 2.67},
		{0.125, 2, 0.12},
		{-0.125, 2, -0.12},
		{0.5, 0, 0},
		{1.5, 0, 2},
		{2.5, 0, 2},
		{0.16666666666666666, 4, 0.1667},
	} {
		if got := pyRound(testCase.value, testCase.digits); got != testCase.want {
			t.Fatalf("pyRound(%v,%d) = %v, want %v",
				testCase.value, testCase.digits, got, testCase.want)
		}
	}
}
