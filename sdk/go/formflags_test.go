// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package ramesdk

import (
	"testing"
)

// markOf returns the declared form-flag mask of the first occurrence of token in the sentence
// (and pins the marks/words length alignment invariant).
func markOf(t *testing.T, line, token string) uint8 {
	t.Helper()
	words := SplitWords(line)
	marks := blindFormFlagMarks(line, words)
	if len(marks) != len(words) {
		t.Fatalf("marks/words length mismatch: %d vs %d (line=%q)", len(marks), len(words), line)
	}
	for i, w := range words {
		if w == token {
			return marks[i]
		}
	}
	t.Fatalf("line %q produced no item %q (words=%v)", line, token, words)
	return 0
}

func TestBlindFormFlagRules(t *testing.T) {
	cases := []struct {
		name  string
		line  string
		token string
		want  uint8
	}{
		// bit5 focal header / heading / summary field.
		{"atx heading lead", "# Meeting notes and decisions", "Meeting", FlagIsFocalHeader},
		{"atx heading without space", "#会议记录", "会议", FlagIsFocalHeader},
		{"field label line fullwidth colon", "时间：2026-05-01 morning", "时间", FlagIsFocalHeader},
		{"field label line halfwidth colon", "Date: 2026-05-01 morning", "Date", FlagIsFocalHeader},
		{"spaced prefix is not a label", "Meeting notes: discussed items", "Meeting", 0},
		{"terminator in prefix is not a label", "He said. Then added: more text", "He", 0},
		{"empty prefix is not a label", "：empty label line", "empty", 0},
		{"more than six hashes is not atx", "####### seven hashes", "seven", 0},

		// bit6 strong delimiter envelope.
		{"bracketed head", "[2026-05-01] Meeting minutes", "2026-05-01", FlagHasBoundaryQuote},
		{"cjk quote envelope", "「Meeting minutes」archived", "Meeting", FlagHasBoundaryQuote},
		{"paren envelope", "(2026-05-01) archived", "2026-05-01", FlagHasBoundaryQuote},
		{"weak punctuation is not a delimiter", ",Meeting minutes archived", "Meeting", 0},

		// bit7 quantity-dimension dyad (dimension side only).
		{"latin spaced dyad", "latency 50 ms observed", "ms", FlagIsQuantityOrUnit},
		{"cjk unspaced dyad", "净重50千克的样品", "千克", FlagIsQuantityOrUnit},
		{"cjk spaced dyad", "净重 50 千克", "千克", FlagIsQuantityOrUnit},
		{"decimal anchor still counts", "temperature 36.5 C range 50-60 Hz", "C", FlagIsQuantityOrUnit},
		{"range anchor still counts", "temperature 36.5 C range 50-60 Hz", "Hz", FlagIsQuantityOrUnit},
		{"glued digit-led parent", "uplink 50ms done", "ms", FlagIsQuantityOrUnit},

		// Compound-child guards (structural reason the cross-repo canary stays byte-identical).
		{"version child must not leak", "RollingMemory v1.0.1 uses blind pulses", "uses", 0},
		{"identifier child must not leak", "WaveEngine_256 and irreversible pulses", "and", 0},
		{"hyphenated name child must not leak", "GPT-4 model finished", "model", 0},
		{"numeric side is never marked", "index 50 60 label", "60", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := markOf(t, tc.line, tc.token)
			if got&tc.want != tc.want || (tc.want == 0 && got != 0) {
				t.Fatalf("line=%q token=%q marks=%#x want=%#x (words=%v)", tc.line, tc.token, got, tc.want, SplitWords(tc.line))
			}
		})
	}
}

// TestBlindFormFlagsCanaryParity pins the cross-repo `RAME-BlindPulse-v1` contract:
// the pinned canary vector must stay byte-identical after enabling the declared form flags,
// because the predicates never fire on letter-led mixed compound children or punctuation-only lines.
func TestBlindFormFlagsCanaryParity(t *testing.T) {
	if err := VerifyCodecLocal(); err != nil {
		t.Fatalf("canary drift after enabling declared form flags: %v", err)
	}
	enc := NewBlindEncoder([]byte(CanarySecret))
	pkt, _ := enc.PackBlindPacket(CanaryDocID, CanaryText, CanaryTS)
	if pkt.AtomsB64 != CanaryAtomsB64 {
		t.Fatalf("canary payload drift: got %q want %q", pkt.AtomsB64, CanaryAtomsB64)
	}
	for i, a := range pkt.Slots {
		if a.ClassFlags > 0x0F {
			t.Fatalf("canary slot %d carries declared high-nibble flags: %#x", i, a.ClassFlags)
		}
	}
}

// TestBlindFormFlagsWireRoundTrip verifies the wide-F (8-bit) wire path carries the declared
// flags losslessly while legacy content keeps the 4-bit nibble column byte-for-byte.
func TestBlindFormFlagsWireRoundTrip(t *testing.T) {
	enc := NewBlindEncoder([]byte("rame-form-flag-key-2026"))
	marked, _ := enc.PackBlindPacket("DOC_MARKED", "净重 50 千克，交付日 2026-05-01。\n[2026-05-01] 会议纪要：预算 50 万。\n# 结论", 1732400000)
	raw, err := EncodeWireBytes(marked.Slots, marked.SentenceFrames)
	if err != nil {
		t.Fatalf("EncodeWireBytes(marked) failed: %v", err)
	}
	if raw[5]&wireFlagWideF == 0 {
		t.Fatalf("payload with declared flags must set wireFlagWideF (header flags=%#x)", raw[5])
	}
	decSlots, _, err := DecodeWireBytes(raw)
	if err != nil {
		t.Fatalf("DecodeWireBytes(marked) failed: %v", err)
	}
	if len(decSlots) != len(marked.Slots) {
		t.Fatalf("round-trip slot count mismatch: %d vs %d", len(decSlots), len(marked.Slots))
	}
	high := 0
	for i, a := range decSlots {
		if a != marked.Slots[i] {
			t.Fatalf("slot %d round-trip drift: got %+v want %+v", i, a, marked.Slots[i])
		}
		if a.ClassFlags > 0x0F {
			high++
		}
	}
	if high == 0 {
		t.Fatalf("expected at least one slot with declared high-nibble flags")
	}

	legacy, _ := enc.PackBlindPacket("DOC_LEGACY", "Zero plaintext leaves the client.", 1732400000)
	legacyRaw, err := EncodeWireBytes(legacy.Slots, legacy.SentenceFrames)
	if err != nil {
		t.Fatalf("EncodeWireBytes(legacy) failed: %v", err)
	}
	if legacyRaw[5]&wireFlagWideF != 0 {
		t.Fatalf("legacy payload must not set wireFlagWideF (header flags=%#x)", legacyRaw[5])
	}
}
