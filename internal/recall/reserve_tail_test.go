// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package recall

import (
	"fmt"
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/store"
)

// White-box boundary tests for the misc-slot reservation in assembleBands: the
// reservation must compare the literal-S head against the last subject band
// that actually shipped (the queue grows mid-loop with second-order subjects),
// and it must not fire when the misc head does not outscore that band.

func tailPtr(value string) *string { return &value }

func tailCand(id string, score float64, subjectID, objectID, sLabel, sLiteral, oLabel, oLiteral string) store.Candidate {
	var sp, op, slp, olp *string
	if subjectID != "" {
		sp = tailPtr(subjectID)
	}
	if objectID != "" {
		op = tailPtr(objectID)
	}
	if sLiteral != "" {
		slp = tailPtr(sLiteral)
	}
	if oLiteral != "" {
		olp = tailPtr(oLiteral)
	}
	return store.Candidate{
		Data: store.CandidateRow{
			Assertion: store.Assertion{
				NodeID: id, Scope: "mem:test", SubjectID: sp, SubjectLiteral: slp,
				RelID: "n_rel", ObjectID: op, ObjectLiteral: olp,
				Status: "active", Confidence: 0.9, Warrant: "referential",
				AnchorTraceHash: "anchorhash", AnchorStart: 0, AnchorEnd: 1,
				TRecord: 900, TSource: 1000, CreatedAt: 900,
			},
			SLabel: sLabel, XLabel: "rel", OLabel: oLabel, TraceText: "x",
		},
		Score: score,
	}
}

func tailKinds(bands []map[string]any) []string {
	out := []string{}
	for _, band := range bands {
		out = append(out, fmt.Sprintf("%s:%s:%d", band["kind"], band["title"], len(band["nodes"].([]map[string]any))))
	}
	return out
}

func tailHas(bands []map[string]any, id string) bool {
	for _, band := range bands {
		for _, node := range band["nodes"].([]map[string]any) {
			if node["id"] == id {
				return true
			}
		}
	}
	return false
}

func tailLastSubjectMax(bands []map[string]any) float64 {
	last := 0.0
	for _, band := range bands {
		if band["kind"] != "subject" {
			continue
		}
		bandMax := 0.0
		for _, node := range band["nodes"].([]map[string]any) {
			if score, ok := node["score"].(float64); ok && score > bandMax {
				bandMax = score
			}
		}
		last = bandMax
	}
	return last
}

func tailSmallCandidates(subjects int, subjectScore, literalScore float64) []store.Candidate {
	cands := []store.Candidate{}
	for i := 1; i <= subjects; i++ {
		sid := fmt.Sprintf("n_s%02d", i)
		label := fmt.Sprintf("s%02d", i)
		cands = append(cands, tailCand(fmt.Sprintf("a_s%02d", i), subjectScore, sid, "", label, "", "o_"+label, ""))
	}
	if literalScore > 0 {
		cands = append(cands, tailCand("a_lit", literalScore, "", "", "", "lit", "", "obj"))
	}
	return cands
}

// targetBands=17 > the 16-slot initial queue: the 17th subject band is appended
// from s16's object (s18, 0.70) while subjectOrder[16] (s17, 0.80) never ships.
// The literal must be reserved when it outscores the shipped tail (0.75 > 0.70).
func TestReserveUsesActualShippedTail(t *testing.T) {
	makeCands := func(literalScore float64) []store.Candidate {
		cands := []store.Candidate{}
		for i := 1; i <= 18; i++ {
			score := 0.95
			if i == 17 {
				score = 0.80
			}
			if i == 18 {
				score = 0.70
			}
			sid := fmt.Sprintf("n_s%02d", i)
			label := fmt.Sprintf("s%02d", i)
			object := ""
			if i == 16 {
				object = "n_s18" // append s18 to the queue mid-loop
			}
			cands = append(cands, tailCand(fmt.Sprintf("a_s%02d", i), score, sid, object, label, "", "", "o_"+label))
		}
		cands = append(cands, tailCand("a_lit", literalScore, "", "", "", "lit", "", "obj"))
		return cands
	}

	bands, _ := assembleBands(makeCands(0.75), 17, 50, 8, 12, 0, nil, nil)
	if !tailHas(bands, "a_lit") {
		t.Fatalf("literal 0.75 > shipped tail %.2f but not reserved: %v",
			tailLastSubjectMax(bands), tailKinds(bands))
	}

	bandsHigh, _ := assembleBands(makeCands(0.85), 17, 50, 8, 12, 0, nil, nil)
	if !tailHas(bandsHigh, "a_lit") {
		t.Fatalf("literal 0.85 not delivered: %v", tailKinds(bandsHigh))
	}

	bandsPage, _ := assembleBands(makeCands(0.75), 8, 50, 8, 12, 9, nil, nil)
	if !tailHas(bandsPage, "a_lit") {
		t.Fatalf("literal 0.75 not delivered on offset page: %v", tailKinds(bandsPage))
	}
}

// A second-order object can bring a low-scored subject onto the tail: here s17
// (0.80) rides in as a_xo's object, so the shipped tail is s17 even though
// subjectOrder[16] is never reached. The literal reserves the slot only when it
// outscores that shipped tail.
func TestReserveTailWithSecondOrderObject(t *testing.T) {
	makeCands := func(literalScore float64) []store.Candidate {
		cands := []store.Candidate{}
		for i := 1; i <= 17; i++ {
			score := 0.95
			if i == 17 {
				score = 0.80
			}
			sid := fmt.Sprintf("n_s%02d", i)
			label := fmt.Sprintf("s%02d", i)
			cands = append(cands, tailCand(fmt.Sprintf("a_s%02d", i), score, sid, "", label, "", "o_"+label, ""))
		}
		cands = append(cands, tailCand("a_xo", 0.99, "n_s16", "n_s17", "s16", "", "s17", ""))
		cands = append(cands, tailCand("a_lit", literalScore, "", "", "", "lit", "", "obj"))
		return cands
	}

	bands, _ := assembleBands(makeCands(0.85), 17, 50, 8, 12, 0, nil, nil)
	if !tailHas(bands, "a_lit") {
		t.Fatalf("literal 0.85 > shipped tail 0.80 but not reserved: %v", tailKinds(bands))
	}
	if len(bands) != 17 || bands[len(bands)-1]["kind"] != "misc" {
		t.Fatalf("want 16 subject + 1 misc band, got %v", tailKinds(bands))
	}

	bandsLow, _ := assembleBands(makeCands(0.75), 17, 50, 8, 12, 0, nil, nil)
	if tailHas(bandsLow, "a_lit") {
		t.Fatalf("literal 0.75 <= shipped tail 0.80 but reserved: %v", tailKinds(bandsLow))
	}
	if len(bandsLow) != 17 || bandsLow[len(bandsLow)-1]["kind"] != "subject" {
		t.Fatalf("want 17 subject bands, got %v", tailKinds(bandsLow))
	}
}

// Pagination positive: caps=1, offset=2 => targetBands=3; the reserved page
// delivers the literal instead of a third subject band.
func TestReserveOffsetPageDeliversLiteral(t *testing.T) {
	bands, _ := assembleBands(tailSmallCandidates(3, 0.90, 0.95), 1, 50, 8, 12, 2, nil, nil)
	if !tailHas(bands, "a_lit") {
		t.Fatalf("literal not delivered at offset page: %v", tailKinds(bands))
	}
}

// caps=1: the single slot is reserved and the page becomes the misc band.
func TestReserveCapsOne(t *testing.T) {
	bands, _ := assembleBands(tailSmallCandidates(1, 0.90, 0.95), 1, 50, 8, 12, 0, nil, nil)
	if !tailHas(bands, "a_lit") {
		t.Fatalf("literal not delivered at caps=1: %v", tailKinds(bands))
	}
	bandsTight, _ := assembleBands(tailSmallCandidates(1, 0.90, 0.95), 1, 1, 8, 12, 0, nil, nil)
	if !tailHas(bandsTight, "a_lit") {
		t.Fatalf("literal not delivered at caps=1, nodes=1: %v", tailKinds(bandsTight))
	}
}

// Tie: strict > means no reservation; output stays deterministic.
func TestReserveTieNotTriggered(t *testing.T) {
	cands := tailSmallCandidates(8, 0.90, 0.90)
	first, _ := assembleBands(cands, 8, 50, 8, 12, 0, nil, nil)
	if tailHas(first, "a_lit") {
		t.Fatalf("tie unexpectedly reserved: %v", tailKinds(first))
	}
	for i := 0; i < 20; i++ {
		next, _ := assembleBands(cands, 8, 50, 8, 12, 0, nil, nil)
		if strings.Join(tailKinds(next), "|") != strings.Join(tailKinds(first), "|") {
			t.Fatalf("nondeterministic tie output at %d: %v vs %v", i, tailKinds(first), tailKinds(next))
		}
	}
}

// Misc-only candidates still ship through the misc band with no subject groups.
func TestReserveMiscOnly(t *testing.T) {
	bands, _ := assembleBands(tailSmallCandidates(0, 0, 0.95), 8, 50, 8, 12, 0, nil, nil)
	if len(bands) != 1 || bands[0]["kind"] != "misc" || !tailHas(bands, "a_lit") {
		t.Fatalf("single literal not delivered via misc: %v", tailKinds(bands))
	}
	cands := []store.Candidate{
		tailCand("a_l1", 0.95, "", "", "", "l1", "", "o1"),
		tailCand("a_l2", 0.85, "", "", "", "l2", "", "o2"),
		tailCand("a_l3", 0.75, "", "", "", "l3", "", "o3"),
	}
	bands, _ = assembleBands(cands, 8, 50, 8, 12, 0, nil, nil)
	if !tailHas(bands, "a_l1") || len(bands) != 1 || bands[0]["kind"] != "misc" {
		t.Fatalf("misc-only failed: %v", tailKinds(bands))
	}
}

// Lower-scored misc: no reservation by design; the literal remains absent.
func TestReserveLowerMiscNotTriggered(t *testing.T) {
	bands, _ := assembleBands(tailSmallCandidates(8, 0.90, 0.80), 8, 50, 8, 12, 0, nil, nil)
	if tailHas(bands, "a_lit") {
		t.Fatalf("lower-scored misc unexpectedly delivered: %v", tailKinds(bands))
	}
}
