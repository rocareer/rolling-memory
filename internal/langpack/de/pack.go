// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package de provides the opt-in Germanic/Nordic compound-splitting companion word segmenter
// (`de`, `nl`, `sv`) and pulse packer, decomposing unhyphenated closed compounds (e.g.,
// `Speicherverwaltungssystem`) via the 128k BPE merge tree while retaining the full compound.
package de

import (
	"strings"
	"unicode"

	"github.com/rocareer/rolling-memory/internal/langpack/multi"
	"github.com/rocareer/rolling-memory/internal/tokenzr"
	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

// SegmentSentence emits canonical surface words (`ramesdk.SplitWords`) and decomposes
// long closed compounds (`carrier >= 180`) into their 128k BPE constituent stems.
func SegmentSentence(sentence string) []string {
	if strings.TrimSpace(sentence) == "" {
		return nil
	}
	base := ramesdk.SplitWords(sentence)
	out := make([]string, 0, len(base)*2)
	for _, w := range base {
		out = append(out, w)
		var cw uint32
		for _, r := range w {
			cw += uint32(ramesdk.CarrierUnits(r))
		}
		if cw >= 180 {
			subs := tokenzr.SplitSubwords(w)
			if len(subs) >= 2 {
				for _, sub := range subs {
					st := strings.TrimSpace(sub)
					var subW uint32
					for _, r := range st {
						if unicode.IsLetter(r) {
							subW += uint32(ramesdk.CarrierUnits(r))
						}
					}
					if subW >= 60 && !strings.EqualFold(st, w) {
						out = append(out, st)
					}
				}
			}
		}
	}
	return out
}

// PackSentence applies the Germanic compound-splitting segmenter and BPE harmonic packer.
func PackSentence(enc *ramesdk.BlindEncoder, sentence string, sentIdx int) []ramesdk.BlindTokenAtom {
	return multi.PackSentenceWithSegmenter(enc, sentence, sentIdx, SegmentSentence, false)
}
