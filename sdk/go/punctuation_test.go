// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package ramesdk_test

import (
	"context"
	"reflect"
	"testing"

	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

func TestPunctuationOnlyAtomsAndPacket(t *testing.T) {
	enc := ramesdk.NewBlindEncoder([]byte("punctuation-test-secret"))
	want := []ramesdk.BlindTokenAtom{
		{H: enc.KeyedHash64(";"), RawBitsX10: 10, ClassFlags: 0},
		{H: enc.KeyedHash64(")"), RawBitsX10: 10, ClassFlags: 0},
	}
	if got := enc.PackSentenceAtoms(";)"); !reflect.DeepEqual(got, want) {
		t.Fatalf("PackSentenceAtoms punctuation: got=%+v want=%+v", got, want)
	}
	pkt, sents := enc.PackBlindPacket("DOC_PUNCT", ";)", 1700000000)
	if !reflect.DeepEqual(sents, []string{";)"}) {
		t.Fatalf("punctuation sentences = %#v", sents)
	}
	if !reflect.DeepEqual(pkt.Slots, want) || len(pkt.SentenceFrames) != 1 || !reflect.DeepEqual(pkt.SentenceFrames[0], want) {
		t.Fatalf("punctuation packet did not preserve both atoms: %+v", pkt)
	}
}

func TestPunctuationOnlyRememberStoresPlaintext(t *testing.T) {
	store := ramesdk.NewMemoryStore([]byte("punctuation-test-secret"))
	client := ramesdk.NewClient("", "", "10", store)
	if _, err := client.Remember(context.Background(), "DOC_PUNCT", ";)", 1700000000); err != nil {
		t.Fatalf("Remember punctuation-only memory: %v", err)
	}
	raw, distilled, kept, err := store.LoadLocalDocument("DOC_PUNCT")
	if err != nil || raw != ";)" || distilled != ";)" || !reflect.DeepEqual(kept, []string{";)"}) {
		t.Fatalf("punctuation plaintext did not round-trip: raw=%q distilled=%q kept=%v err=%v", raw, distilled, kept, err)
	}
}

func TestPunctuationFallbackUnicodeSpaceAndFlags(t *testing.T) {
	enc := ramesdk.NewBlindEncoder([]byte("punctuation-test-secret"))
	for _, tc := range []struct {
		name  string
		input string
		want  []ramesdk.BlindTokenAtom
	}{
		{
			name:  "non BMP code points",
			input: "💡✨",
			want: []ramesdk.BlindTokenAtom{
				{H: enc.KeyedHash64("💡"), RawBitsX10: 10},
				{H: enc.KeyedHash64("✨"), RawBitsX10: 10},
			},
		},
		{
			name:  "unicode spaces skipped",
			input: "; \t\u00a0)",
			want: []ramesdk.BlindTokenAtom{
				{H: enc.KeyedHash64(";"), RawBitsX10: 10},
				{H: enc.KeyedHash64(")"), RawBitsX10: 10},
			},
		},
		{
			name:  "connector flags",
			input: "_-",
			want: []ramesdk.BlindTokenAtom{
				{H: enc.KeyedHash64("_"), RawBitsX10: 10, ClassFlags: ramesdk.FlagHasConn},
				{H: enc.KeyedHash64("-"), RawBitsX10: 10, ClassFlags: ramesdk.FlagHasConn},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := enc.PackSentenceAtoms(tc.input); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("PackSentenceAtoms(%q): got=%+v want=%+v", tc.input, got, tc.want)
			}
		})
	}
}

func TestPunctuationFallbackWhitespaceRemainsEmpty(t *testing.T) {
	enc := ramesdk.NewBlindEncoder([]byte("punctuation-test-secret"))
	for _, input := range []string{"", " \t\n\r\u00a0"} {
		if got := enc.PackSentenceAtoms(input); len(got) != 0 {
			t.Fatalf("PackSentenceAtoms(%q) = %+v, want empty", input, got)
		}
		pkt, sents := enc.PackBlindPacket("DOC_EMPTY", input, 0)
		if len(pkt.Slots) != 0 || len(pkt.SentenceFrames) != 0 || len(sents) != 0 {
			t.Fatalf("PackBlindPacket(%q) produced non-empty output: pkt=%+v sents=%v", input, pkt, sents)
		}
	}
}

func TestPunctuationFallbackLeavesOrdinaryWordPathUnchanged(t *testing.T) {
	enc := ramesdk.NewBlindEncoder([]byte("rame-golden-key-2026"))
	want := []ramesdk.BlindTokenAtom{
		{H: 9689035911546599164, RawBitsX10: 260, ClassFlags: 3},
		{H: 810292289799255170, RawBitsX10: 140, ClassFlags: 1},
		{H: 5993037274799471444, RawBitsX10: 120, ClassFlags: 1},
	}
	if got := enc.PackSentenceAtoms("RollingMemory"); !reflect.DeepEqual(got, want) {
		t.Fatalf("ordinary word atoms changed: got=%+v want=%+v", got, want)
	}
}
