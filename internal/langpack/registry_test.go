// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package langpack

import (
	"database/sql"
	"reflect"
	"testing"

	"github.com/rocareer/rolling-memory/internal/thinclient"
	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
	_ "modernc.org/sqlite"
)

func TestCatalogIsDefaultOffAndConfigureRestoresCanonical(t *testing.T) {
	cat := Catalog()
	if len(cat) < 7 {
		t.Fatalf("expected at least 7 language packs (en, zh, ja, ko, sea, de, multi), got %d", len(cat))
	}
	for _, item := range cat {
		if item.EnabledByDefault {
			t.Fatalf("language pack %q must not be enabled by default", item.ID)
		}
	}
	plain := ramesdk.NewBlindEncoder([]byte("langpack-test-secret"))
	configured := ramesdk.NewBlindEncoder([]byte("langpack-test-secret"))
	for _, mode := range []string{"", "off", "standard", "default", "universal", "auto"} {
		if err := Configure(configured, mode); err != nil {
			t.Fatalf("Configure(%q): %v", mode, err)
		}
		want, _ := plain.PackBlindPacket("DOC", "RollingMemory v1.0.1 WaveEngine_256 洛灵记忆. Zero plaintext!", 1700000000)
		got, _ := configured.PackBlindPacket("DOC", "RollingMemory v1.0.1 WaveEngine_256 洛灵记忆. Zero plaintext!", 1700000000)
		if !reflect.DeepEqual(want.Slots, got.Slots) || !reflect.DeepEqual(want.SentenceFrames, got.SentenceFrames) {
			t.Fatalf("%q configuration changed canonical packets", mode)
		}
	}
}

func TestEnglishOptInInflectionAndCalendarAlignment(t *testing.T) {
	enc := ramesdk.NewBlindEncoder([]byte("langpack-test-secret"))
	if err := Configure(enc, "en"); err != nil {
		t.Fatalf("Configure(en): %v", err)
	}
	one, _ := enc.PackBlindPacket("Q1", "optimizing", 0)
	two, _ := enc.PackBlindPacket("Q2", "optimization", 0)
	if len(one.Slots) < 2 || len(two.Slots) < 2 {
		t.Fatalf("expected opt-in harmonic atoms, got %d and %d", len(one.Slots), len(two.Slots))
	}
	if one.Slots[0].H == two.Slots[0].H {
		t.Fatalf("surface forms should retain distinct exact hashes")
	}
	if !hasSharedHash(one.Slots[1:], two.Slots[1:]) {
		t.Fatalf("expected shared lexeme harmonic for optimizing/optimization")
	}

	iso, _ := enc.PackBlindPacket("D1", "2025-02-25", 0)
	natural, _ := enc.PackBlindPacket("D2", "February 2025", 0)
	if !hasSharedHash(iso.Slots, natural.Slots) {
		t.Fatalf("expected ISO date and natural month/year to share a blind hash")
	}
}

func TestChineseOptInWireAndUnknownLanguageFailsClosed(t *testing.T) {
	enc := ramesdk.NewBlindEncoder([]byte("langpack-test-secret"))
	if err := Configure(enc, "zh"); err != nil {
		t.Fatalf("Configure(zh): %v", err)
	}
	pkt, _ := enc.PackBlindPacket("ZH1", "2026年11月8日优化数据库性能与洛灵记忆检索", 0)
	if len(pkt.Slots) == 0 {
		t.Fatal("expected non-empty Chinese blind pulse")
	}
	for i, atom := range pkt.Slots {
		if atom.ClassFlags > 0xFF {
			t.Fatalf("slot[%d] exceeds RMBP 8-bit flag domain: %#x", i, atom.ClassFlags)
		}
	}
	wire, err := ramesdk.EncodeWirePayload(pkt.Slots, pkt.SentenceFrames)
	if err != nil {
		t.Fatalf("EncodeWirePayload: %v", err)
	}
	decodedSlots, decodedFrames, err := ramesdk.DecodeWirePayload(wire)
	if err != nil {
		t.Fatalf("DecodeWirePayload: %v", err)
	}
	if !reflect.DeepEqual(decodedSlots, pkt.Slots) || !reflect.DeepEqual(decodedFrames, pkt.SentenceFrames) {
		t.Fatal("RMBP round trip changed the Chinese pulse")
	}
	// Verify structured Chinese date matches ISO date
	isoPkt, _ := enc.PackBlindPacket("ZH_ISO", "2026-11-08", 0)
	if !hasSharedHash(pkt.Slots, isoPkt.Slots) {
		t.Fatal("expected Chinese date 2026年11月8日 and ISO 2026-11-08 to share a blind calendar beacon")
	}
	if err := Configure(enc, "unknown-language"); err == nil {
		t.Fatal("unknown language must fail closed")
	}
}

func TestAllCompanionSegmenters_JaKoSeaDeAndVaultIntegration(t *testing.T) {
	// 1. Japanese (`ja`): separates Kanji stem and Katakana loanword from Hiragana particles
	jaSeg, err := Segmenter("ja")
	if err != nil {
		t.Fatalf("Segmenter(ja): %v", err)
	}
	jaTokens := jaSeg("データベースのパフォーマンスを最適化する")
	if len(jaTokens) < 4 {
		t.Fatalf("expected Japanese segmenter to split Kanji/Katakana/Hiragana runs, got %v", jaTokens)
	}

	// 2. Korean (`ko`): extracts Hangul content stem alongside Eojeol
	koSeg, err := Segmenter("ko")
	if err != nil {
		t.Fatalf("Segmenter(ko): %v", err)
	}
	koTokens := koSeg("데이터베이스에서 성능을 최적화했습니다")
	if len(koTokens) < 3 {
		t.Fatalf("expected Korean segmenter to emit Eojeol and BPE stems, got %v", koTokens)
	}

	// 3. Southeast Asian (`th` / `lo` / `km` -> `sea`): decomposes unspaced Thai sentence and improves pulse signal
	plainEnc := ramesdk.NewBlindEncoder([]byte("sea-key"))
	thaiText := "ระบบฐานข้อมูลความจำอัตโนมัติทำงานได้อย่างรวดเร็วและปลอดภัย"
	plainPkt, _ := plainEnc.PackBlindPacket("TH_PLAIN", thaiText, 1700000000)
	plainSig := ramesdk.EvaluatePulseSignal(plainPkt)

	seaEnc := ramesdk.NewBlindEncoder([]byte("sea-key"))
	if err := Configure(seaEnc, "th"); err != nil {
		t.Fatalf("Configure(th): %v", err)
	}
	seaPkt, _ := seaEnc.PackBlindPacket("TH_SEA", thaiText, 1700000000)
	seaSig := ramesdk.EvaluatePulseSignal(seaPkt)
	if len(seaPkt.Slots) <= len(plainPkt.Slots) || seaSig.GluedSlotCount > plainSig.GluedSlotCount {
		t.Fatalf("expected Thai companion pack to decompose unspaced glued carrier: plainSlots=%d (glued=%d) vs seaSlots=%d (glued=%d)",
			len(plainPkt.Slots), plainSig.GluedSlotCount, len(seaPkt.Slots), seaSig.GluedSlotCount)
	}

	// 4. German (`de`): splits closed compound words while retaining surface compound
	deEnc := ramesdk.NewBlindEncoder([]byte("de-key"))
	if err := Configure(deEnc, "de"); err != nil {
		t.Fatalf("Configure(de): %v", err)
	}
	deCompound, _ := deEnc.PackBlindPacket("DE_1", "Speicherverwaltungssystem", 0)
	dePart, _ := deEnc.PackBlindPacket("DE_2", "Speicher", 0)
	if !hasSharedHash(deCompound.Slots, dePart.Slots) {
		t.Fatalf("expected German compound 'Speicherverwaltungssystem' to share constituent harmonic with 'Speicher'")
	}

	// 5. Vault integration (`ConfigureVault`)
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	vault, err := thinclient.NewVault(db, []byte("vault-lang-secret"))
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}
	if err := ConfigureVault(vault, "en"); err != nil {
		t.Fatalf("ConfigureVault(en): %v", err)
	}
	vPkt1, _ := vault.PackBlindPacket("V1", "optimizing", 1700000000)
	vPkt2, _ := vault.PackBlindPacket("V2", "optimization", 1700000000)
	if len(vPkt1.Slots) < 2 || !hasSharedHash(vPkt1.Slots[1:], vPkt2.Slots[1:]) {
		t.Fatalf("expected ConfigureVault(en) to activate BPE inflection harmonics on Vault.PackBlindPacket")
	}
	if err := ConfigureVault(vault, "universal"); err != nil {
		t.Fatalf("ConfigureVault(universal): %v", err)
	}
	vPlain, _ := vault.PackBlindPacket("V_PLAIN", "optimizing", 1700000000)
	if len(vPlain.Slots) != 1 {
		t.Fatalf("expected ConfigureVault(universal) to restore canonical 1-slot encoding, got %d", len(vPlain.Slots))
	}
}

func TestMeasurementAndStatisticalUnitHarmonics(t *testing.T) {
	encEN := ramesdk.NewBlindEncoder([]byte("unit-stat-secret"))
	encZH := ramesdk.NewBlindEncoder([]byte("unit-stat-secret"))
	if err := Configure(encEN, "en"); err != nil {
		t.Fatalf("Configure(en): %v", err)
	}
	if err := Configure(encZH, "zh"); err != nil {
		t.Fatalf("Configure(zh): %v", err)
	}

	// 1. Cross-lingual measurement unit alignment: "50 kg" <-> "50 千克" (1st-order @unit:mass:kg + 2nd-order @dim:mass)
	pktKG, _ := encEN.PackBlindPacket("U_KG", "50 kg", 0)
	pktZH, _ := encZH.PackBlindPacket("U_ZH", "50 千克", 0)
	if !hasSharedHash(pktKG.Slots, pktZH.Slots) {
		t.Fatalf("expected '50 kg' and '50 千克' to share canonical mass unit harmonic")
	}

	// 2. 2nd-order dimension category alignment: Common Core "$145.00" <-> Common Core "€145.00" <-> ZH "145 欧元" (@dim:money)
	pktUSD, _ := encEN.PackBlindPacket("D_USD", "Hotel invoice $145.00 settled", 0)
	pktReceiptEur, _ := encEN.PackBlindPacket("D_EUR", "Hotel invoice €145.00 settled", 0)
	pktZHEur, _ := encZH.PackBlindPacket("D_ZH_EUR", "酒店账单 145 欧元", 0)
	if !hasSharedHash(pktUSD.Slots, pktReceiptEur.Slots) || !hasSharedHash(pktReceiptEur.Slots, pktZHEur.Slots) {
		t.Fatalf("expected '$145.00', '€145.00', and '145 欧元' to share Common Core @unit:curr:eur / @dim:money harmonics")
	}

	// 3. Statistical unit alignment: "95%" <-> "95 percent" <-> "95 百分比" (@unit:stat:pct + @dim:stat)
	pktPctSym, _ := encEN.PackBlindPacket("S_SYM", "accuracy reached 95%", 0)
	pktPctWord, _ := encEN.PackBlindPacket("S_WORD", "95 percent", 0)
	pktPctZH, _ := encZH.PackBlindPacket("S_ZH", "准确率 95 百分比", 0)
	if !hasSharedHash(pktPctSym.Slots, pktPctWord.Slots) || !hasSharedHash(pktPctWord.Slots, pktPctZH.Slots) {
		t.Fatalf("expected '95%%', '95 percent', and '95 百分比' to share statistical unit harmonic")
	}
}

func hasSharedHash(a, b []ramesdk.BlindTokenAtom) bool {
	seen := make(map[uint64]bool, len(a))
	for _, atom := range a {
		seen[atom.H] = true
	}
	for _, atom := range b {
		if seen[atom.H] {
			return true
		}
	}
	return false
}

func TestMaxCapOnlineCorpusDFConjugateWashout(t *testing.T) {
	defer SetMaxCapOnlineCorpusDF(nil, 0)
	enc := ramesdk.NewBlindEncoder([]byte("test-maxcap-online-df-secret"))

	// Cold-start (N=0): "chronotrigger" receives high static BPE carrier width
	coldAtoms := PackMaxCapabilitySentence(enc, "chronotrigger valve", 0)
	hChrono := enc.KeyedHash64("chronotrigger")
	hValve := enc.KeyedHash64("valve")
	var coldChronoB, coldValveB uint16
	for _, a := range coldAtoms {
		if a.H == hChrono && (a.RawBitsX10%2 == 0) {
			coldChronoB = a.RawBitsX10
		}
		if a.H == hValve && (a.RawBitsX10%2 == 0) {
			coldValveB = a.RawBitsX10
		}
	}
	if coldChronoB <= coldValveB {
		t.Fatalf("expected cold-start static BPE width of 'chronotrigger' (%d) > 'valve' (%d)", coldChronoB, coldValveB)
	}

	// Online DF active (N=80): "chronotrigger" is ubiquitous boilerplate (df=80/80), "valve" is rare (df=1/80)
	SetMaxCapOnlineCorpusDF(map[string]int{
		"chronotrigger": 80,
		"valve":         1,
	}, 80)
	warmAtoms := PackMaxCapabilitySentence(enc, "chronotrigger valve", 0)
	var warmChronoB, warmValveB uint16
	for _, a := range warmAtoms {
		if a.H == hChrono && (a.RawBitsX10%2 == 0) {
			warmChronoB = a.RawBitsX10
		}
		if a.H == hValve && (a.RawBitsX10%2 == 0) {
			warmValveB = a.RawBitsX10
		}
	}
	if warmValveB <= warmChronoB {
		t.Fatalf("expected online DF conjugate washout to rank 'valve' (%d) > boilerplate 'chronotrigger' (%d)", warmValveB, warmChronoB)
	}
}
