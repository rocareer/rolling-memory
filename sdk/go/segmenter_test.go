// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package ramesdk_test

import (
	"strings"
	"testing"

	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

// TestFTS5BM25Segmenter_TermStream pins the local FTS5/BM25 term stream:
//   - surface words from SplitWords (compound subwords included, same source as QueryFTS5BM25);
//   - mechanical carrier width >= FTS5BM25TermMinCarrierX10 (60 = 3 Latin letters / 1 Han)
//     survives, two-letter glue words drop;
//   - lowercased, first-occurrence order, repetitions preserved (tf survives blind packing).
func TestFTS5BM25Segmenter_TermStream(t *testing.T) {
	got := ramesdk.FTS5BM25Segmenter("The Cathedral in Porto, to the cathedral again!")
	want := []string{"the", "cathedral", "porto", "the", "cathedral", "again"}
	if len(got) != len(want) {
		t.Fatalf("term stream = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("term[%d] = %q, want %q (full=%v)", i, got[i], want[i], got)
		}
	}
	for _, term := range got {
		if term != strings.ToLower(term) {
			t.Fatalf("term %q is not lowercased", term)
		}
	}
}

// TestFTS5BM25Segmenter_DropsShortGlueWords pins that sub-threshold words never enter the
// pulse, which is exactly what makes the fts5-bm25 arm sparser than the standard arm.
func TestFTS5BM25Segmenter_DropsShortGlueWords(t *testing.T) {
	// 60 = 3 Latin letters; two-letter and single-letter surface words are below the bar.
	for _, glue := range []string{"in", "to", "of", "at", "a", "I", "is"} {
		if terms := ramesdk.FTS5BM25Segmenter(glue); len(terms) != 0 {
			t.Fatalf("glue word %q must be filtered by carrier width, got %v", glue, terms)
		}
	}
}

// TestPulseTokenizerArmsProduceDifferentPulses guards the three-mode contract:
// the local-FTS tokenizer must not silently degrade into the standard tokenizer.
func TestPulseTokenizerArmsProduceDifferentPulses(t *testing.T) {
	secret := []byte("frame-vault-tokenizer-arm-key")
	text := "The cathedral in Porto was stunning; we walked to the river at night."

	stdEnc := ramesdk.NewBlindEncoder(secret)
	stdPkt, _ := stdEnc.PackBlindPacket("doc", text, 1700000000)
	if len(stdPkt.Slots) == 0 {
		t.Fatalf("standard arm produced an empty pulse")
	}

	ftsEnc := ramesdk.NewBlindEncoder(secret)
	ftsEnc.SetSegmenterOptions(ramesdk.SegmenterPackOptions{Segmenter: ramesdk.FTS5BM25Segmenter, HarmonicOverlay: true})
	ftsPkt, _ := ftsEnc.PackBlindPacket("doc", text, 1700000000)
	if len(ftsPkt.Slots) == 0 {
		t.Fatalf("fts5-bm25 arm produced an empty pulse")
	}

	glueHash := ftsEnc.KeyedHash64("in")
	if armHasAtom(ftsPkt.Slots, glueHash) {
		t.Fatalf("fts5-bm25 arm must not carry sub-threshold glue word %q", "in")
	}
	if !armHasAtom(stdPkt.Slots, glueHash) {
		t.Fatalf("standard arm is expected to keep the surface word %q", "in")
	}
	if len(ftsPkt.Slots) >= len(stdPkt.Slots) {
		t.Fatalf("fts5-bm25 pulse should be sparser than standard: fts=%d std=%d", len(ftsPkt.Slots), len(stdPkt.Slots))
	}
}

func armHasAtom(atoms []ramesdk.BlindTokenAtom, h uint64) bool {
	for _, a := range atoms {
		if a.H == h {
			return true
		}
	}
	return false
}
