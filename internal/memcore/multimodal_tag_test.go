// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"testing"
	"unicode"
)

func TestLocateFirstMultimodalPartLineAntiInjection(t *testing.T) {
	// 1. Mid-sentence user text mentioning "【图片】" or "【文件】" must NOT be mistaken for a multimodal part line.
	inBandText := "请把那张【图片】和对应的【文件】发到群里，不要漏掉【音频】记录。"
	if off := LocateFirstMultimodalPartLine(inBandText); off != -1 {
		t.Fatalf("expected -1 for mid-sentence in-band mention, got %d", off)
	}
	if HasStructuralMultimodalPart(inBandText) {
		t.Fatalf("expected HasStructuralMultimodalPart=false for mid-sentence mention")
	}

	// 2. Structural line-start "\n【图片】" composed by ComposeTurnText must be accurately located.
	composed := "请看这张架构图：\n【图片】新网关v2架构图：采用分片集群，P99 延迟 6.8ms。"
	off := LocateFirstMultimodalPartLine(composed)
	want := len("请看这张架构图：\n")
	if off != want {
		t.Fatalf("expected offset %d, got %d", want, off)
	}

	// 3. StripMultimodalProtocolTags must remove CJK protocol tags so pure-ASCII turns are not polluted with Han runes.
	enComposed := "I went to the support group yesterday.\n【图片】a photo of a dog walking past a wall"
	stripped := StripMultimodalProtocolTags(enComposed)
	for _, r := range stripped {
		if unicode.Is(unicode.Han, r) {
			t.Fatalf("expected zero Han runes after StripMultimodalProtocolTags on English turn, found %q in %q", r, stripped)
		}
	}

	// 4. StripModalBracketSlotPrefix must strip text-tag wrappers from entity slot labels while preserving pure entity names.
	cases := [][2]string{
		{"【图片】一幅装在木质画框中的风景油画", "一幅装在木质画框中的风景油画"},
		{"【文件：Q3_roadmap.pdf】", "Q3_roadmap.pdf"},
		{"【音频: shift_09.mp3】", "shift_09.mp3"},
		{"【图片：cam_04_ocr.png】", "cam_04_ocr.png"},
		{"[image: sunset_over_lake.png]", "sunset_over_lake.png"},
		{"[file] Q3_roadmap.pdf", "Q3_roadmap.pdf"},
		{"普通实体名", "普通实体名"},
	}
	for _, tc := range cases {
		if got := StripModalBracketSlotPrefix(tc[0]); got != tc[1] {
			t.Fatalf("StripModalBracketSlotPrefix(%q) = %q, want %q", tc[0], got, tc[1])
		}
	}

	// 5. ComputeScriptBitMass must recognize accented Latin, curly quotes, em-dashes, and Greek units as pure Latin/neutral script
	//    (replacing brittle byte-level isASCIIString checks), while accurately measuring Han/Kana/Hangul/Cyrillic bit mass.
	latinWithTypoAndGreek := "We met at the café — it’s Gabriel García Márquez’s favorite spot (Cr = 92 μmol/L, α = 0.05)!"
	m1 := ComputeScriptBitMass(latinWithTypoAndGreek)
	if !m1.IsPureLatinOrNeutralScript() || m1.LatinBits < 100.0 {
		t.Fatalf("expected IsPureLatinOrNeutralScript=true for accented Latin + curly quotes + Greek, got %+v", m1)
	}
	m2 := ComputeScriptBitMass("京都のカフェでコーヒーを飲んだ")
	if m2.IsPureLatinOrNeutralScript() || m2.HanBits <= 0 || m2.KanaBits <= 0 {
		t.Fatalf("expected positive HanBits and KanaBits for Japanese text, got %+v", m2)
	}

	// 6. TrimRedundantRelObjectEcho must strip trailing object echo from predicate slot.
	if got := TrimRedundantRelObjectEcho("says she'd like to get the location metadata part", "the location metadata part"); got != "says she'd like to get" {
		t.Fatalf("expected 'says she'd like to get', got %q", got)
	}
	if got := TrimRedundantRelObjectEcho("has two identical definitions", "Two Identical Definitions"); got != "has" {
		t.Fatalf("expected 'has', got %q", got)
	}
	// 架构契约「语义词闸门全拆 · 宁宽勿误拒」：泛称 NP 判据已拆，不再拒收（语义归 LLM）。
	if bad, reason := IsInvalidAliasSurface("Elena", "a small dog"); bad {
		t.Fatalf("泛称 NP 语义词闸门应已拆除，实测仍拒收：%s", reason)
	}

	// 7. AreScriptsOrthogonal 的 math.Min 逐族交集判据：同族共享信息量不判正交，跨族零共享才判正交。
	if AreScriptsOrthogonal(ComputeScriptBitMass("Redis cluster"), ComputeScriptBitMass("Redis topology")) {
		t.Fatalf("shared Latin bit mass must not be reported orthogonal")
	}
	if !AreScriptsOrthogonal(ComputeScriptBitMass("Redis cluster"), ComputeScriptBitMass("存储引擎")) {
		t.Fatalf("disjoint Latin/Han script families should be orthogonal")
	}
}
