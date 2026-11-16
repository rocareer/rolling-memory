// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestSubmodularCloneFoldedDF_ImmunityAndDistinctPreservation(t *testing.T) {
	totalDocs := 1000
	cloneTerms := map[uint64]int{1: 1, 2: 1, 3: 1, 4: 1, 5: 1, 6: 1, 7: 1, 8: 1}
	distinctA := map[uint64]int{1: 1, 10: 1, 11: 1, 12: 1, 13: 1, 14: 1, 15: 1, 16: 1}
	distinctB := map[uint64]int{1: 1, 20: 1, 21: 1, 22: 1, 23: 1, 24: 1, 25: 1, 26: 1}

	// Case 1: 10 near-clone copies within the same day should fold far below rawDF=10.
	postings := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}
	effClone := SubmodularCloneFoldedDF(
		postings,
		len(postings),
		totalDocs,
		func(idx int) int64 { return 1700000000 + int64(idx*60) },
		func(_ int) map[uint64]int { return cloneTerms },
	)
	if effClone >= 4 || effClone < 1 {
		t.Fatalf("expected 10 near-clone copies to fold into [1,3], got %d", effClone)
	}

	// Case 2: 25 near-clone copies in a 60-doc corpus (rawDF = 41.7% > 1/3) must still fold to <= 3.
	postings25 := make([]int, 25)
	for i := range postings25 {
		postings25[i] = i
	}
	effClone25 := SubmodularCloneFoldedDF(
		postings25,
		len(postings25),
		60,
		func(idx int) int64 { return 1700000000 + int64(idx*30) },
		func(_ int) map[uint64]int { return cloneTerms },
	)
	if effClone25 > 3 {
		t.Fatalf("expected 25 near-clone copies in N=60 to fold into <=3, got %d", effClone25)
	}

	// Case 3: 3 distinct events on the same day with low overlap (< 5/8) must preserve rawDF=3.
	docsTerms := []map[uint64]int{cloneTerms, distinctA, distinctB}
	effDistinct := SubmodularCloneFoldedDF(
		[]int{0, 1, 2},
		3,
		totalDocs,
		func(idx int) int64 { return 1700000000 + int64(idx*300) },
		func(idx int) map[uint64]int { return docsTerms[idx] },
	)
	if effDistinct != 3 {
		t.Fatalf("expected 3 distinct same-day events to preserve DF=3, got %d", effDistinct)
	}
}

func TestComputeDimensionlessCoherence_ScaleAndLengthInvariance(t *testing.T) {
	totalDocs := 10000
	log2Total := FixedLog2Millibits(uint64(totalDocs + 4))
	df := map[uint64]int{
		101: 3,  // ultra-rare shared entity
		102: 5,  // ultra-rare shared entity
		103: 8,  // rare shared entity
		201: 20, // doc A unique
		301: 20, // doc B unique
	}
	da := map[uint64]int{101: 1, 102: 1, 103: 1, 201: 1}
	di := map[uint64]int{101: 1, 102: 1, 103: 1, 301: 1}

	dice, nuEff := ComputeDimensionlessCoherence(da, di, df, totalDocs, log2Total)
	if dice <= 0.70 || dice > 1.0 {
		t.Fatalf("expected high infoDice in (0.70, 1.0], got %.4f", dice)
	}
	if nuEff <= 2.0 {
		t.Fatalf("expected positive scale-normalized nuEff > 2.0, got %.4f", nuEff)
	}
	if tau := TemporalProximityKernel(0, 1800.0); tau != 1.0 {
		t.Fatalf("expected tau(0)=1.0, got %.4f", tau)
	}
}

func TestVaultSearchClientSDKDefault_CloneInflationImmunity(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "clone_immunity.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	v, err := NewVault(db, []byte("clone-immunity-test-secret"))
	if err != nil {
		t.Fatal(err)
	}

	// Target gold memory shares common token "alpha" with 15 unrelated documents,
	// while its true entity anchor "zephyrion" is repeated across 25 same-day burst clones
	// in a 60-document corpus (rawDF("zephyrion") = 26 > 60/3 = 20, rawDF("alpha") = 16 <= 20).
	// Without SubmodularCloneFoldedDF, "zephyrion" is misclassified as a high-df background
	// word (rawDF > N/3) and loses its surprisal, letting "alpha" distractors win #1!
	_ = v.SaveLocalPlaintext("gold-target", 1700000000, "zephyrion alpha quantum telescope calibration", nil, CloudIngestResponse{})
	for i := 0; i < 25; i++ {
		_ = v.SaveLocalPlaintext(
			fmt.Sprintf("clone-%02d", i),
			1700000010+int64(i*5),
			fmt.Sprintf("zephyrion observatory burst frame telemetry log %d", i%2),
			nil,
			CloudIngestResponse{},
		)
	}
	for i := 0; i < 15; i++ {
		_ = v.SaveLocalPlaintext(
			fmt.Sprintf("distractor-alpha-%02d", i),
			1690000000+int64(i*86400),
			fmt.Sprintf("alpha routine maintenance report %d", i),
			nil,
			CloudIngestResponse{},
		)
	}
	for i := 0; i < 19; i++ {
		_ = v.SaveLocalPlaintext(
			fmt.Sprintf("filler-%02d", i),
			1680000000+int64(i*86400),
			fmt.Sprintf("background archive entry number %d", i),
			nil,
			CloudIngestResponse{},
		)
	}

	hits, err := v.SearchClientSDKDefault("zephyrion alpha", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].UnitID != "gold-target" {
		t.Fatalf("expected gold-target at rank #1 under 25x burst clone inflation, got %+v", hits)
	}
}

func TestSubmodularCloneAttenuation_SameDayAndCrossDay(t *testing.T) {
	cloneTerms := map[uint64]int{1: 1, 2: 1, 3: 1, 4: 1, 5: 1, 6: 1, 7: 1, 8: 1, 9: 1, 10: 1}
	priorSameDay := func(j int) (map[uint64]int, int64, bool) {
		return cloneTerms, 1700000000 + int64(j*10), true
	}
	// First 2 same-day copies (priorCount=0, 1) must not be attenuated (preserve multi-evidence pairs).
	if a1 := SubmodularCloneAttenuation(cloneTerms, 1700000020, 1, priorSameDay); a1 != 1.0 {
		t.Fatalf("expected 2nd same-day copy attenuation=1.0, got %.4f", a1)
	}
	// 3rd+ same-day exact clone copy (priorCount>=2) must be attenuated below 0.75.
	if a3 := SubmodularCloneAttenuation(cloneTerms, 1700000030, 2, priorSameDay); a3 >= 0.75 || a3 <= 0 {
		t.Fatalf("expected 3rd same-day clone attenuation in (0, 0.75), got %.4f", a3)
	}
}
