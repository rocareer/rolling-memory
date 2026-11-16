// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package langpack

import (
	"reflect"
	"testing"

	"github.com/rocareer/rolling-memory/internal/langpack/common"
	"github.com/rocareer/rolling-memory/internal/langpack/multi"
	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

// TestCommonCoreCoversExtendedDimensions 钉住客户端可选外挂公共核心（跨语种符号层）的量纲覆盖：
// 货币 ISO 4217 扩种、时间跨度、能量、功率、压强、速度、面积、电学、角度、统计比率。
func TestCommonCoreCoversExtendedDimensions(t *testing.T) {
	cases := []struct{ token, unit, dim string }{
		{"aud", "@unit:curr:aud", "@dim:money"},
		{"cad", "@unit:curr:cad", "@dim:money"},
		{"sgd", "@unit:curr:sgd", "@dim:money"},
		{"krw", "@unit:curr:krw", "@dim:money"},
		{"₩", "@unit:curr:krw", "@dim:money"},
		{"inr", "@unit:curr:inr", "@dim:money"},
		{"₹", "@unit:curr:inr", "@dim:money"},
		{"brl", "@unit:curr:brl", "@dim:money"},
		{"₽", "@unit:curr:rub", "@dim:money"},
		{"zar", "@unit:curr:zar", "@dim:money"},
		{"twd", "@unit:curr:twd", "@dim:money"},
		{"thb", "@unit:curr:thb", "@dim:money"},
		{"vnd", "@unit:curr:vnd", "@dim:money"},
		{"lak", "@unit:curr:lak", "@dim:money"},
		{"khr", "@unit:curr:khr", "@dim:money"},

		{"day", "@unit:time:day", "@dim:duration"},
		{"days", "@unit:time:day", "@dim:duration"},
		{"month", "@unit:time:month", "@dim:duration"},
		{"year", "@unit:time:year", "@dim:duration"},
		{"yr", "@unit:time:year", "@dim:duration"},
		{"quarter", "@unit:time:quarter", "@dim:duration"},
		{"decade", "@unit:time:decade", "@dim:duration"},

		{"kcal", "@unit:energy:kcal", "@dim:energy"},
		{"kwh", "@unit:energy:kwh", "@dim:energy"},
		{"joule", "@unit:energy:j", "@dim:energy"},
		{"kw", "@unit:power:kw", "@dim:power"},
		{"watt", "@unit:power:w", "@dim:power"},
		{"horsepower", "@unit:power:hp", "@dim:power"},
		{"psi", "@unit:press:psi", "@dim:pressure"},
		{"kpa", "@unit:press:pa", "@dim:pressure"},
		{"mph", "@unit:speed:mph", "@dim:speed"},
		{"knots", "@unit:speed:kn", "@dim:speed"},
		{"sqft", "@unit:area:sqft", "@dim:area"},
		{"hectare", "@unit:area:ha", "@dim:area"},
		{"acre", "@unit:area:acre", "@dim:area"},
		{"m²", "@unit:area:m2", "@dim:area"},
		{"kva", "@unit:power:va", "@dim:electric"},
		{"ohm", "@unit:res:ohm", "@dim:electric"},
		{"ampere", "@unit:amp:a", "@dim:electric"},
		{"radians", "@unit:angle:deg", "@dim:angle"},

		{"pint", "@unit:vol:pint", "@dim:volume"},
		{"tablespoon", "@unit:vol:tbsp", "@dim:volume"},
		{"byte", "@unit:data:byte", "@dim:data"},
		{"petabyte", "@unit:data:pb", "@dim:data"},
		{"rpm", "@unit:freq:rpm", "@dim:freq"},
		{"ppm", "@unit:stat:ratio", "@dim:stat"},
		{"maximum", "@unit:stat:extreme", "@dim:stat"},
		{"total", "@unit:stat:total", "@dim:stat"},
	}
	for _, c := range cases {
		u, d := common.LookupCommonUnitAndStatHarmonics(c.token)
		if u != c.unit || d != c.dim {
			t.Fatalf("common(%q) = (%q,%q), want (%q,%q)", c.token, u, d, c.unit, c.dim)
		}
	}
}

// TestCompositeLookupProducesCrossLanguageCanonicalEquivalence 钉住客户端外挂启用时「同一物理量纲跨语种/
// 跨符号归一到同一 @unit/@dim 谐波标签」。
func TestCompositeLookupProducesCrossLanguageCanonicalEquivalence(t *testing.T) {
	groups := []struct {
		want   string
		tokens []string
	}{
		{"@unit:mass:kg", []string{"kg", "kilograms", "千克", "公斤", "킬로그램", "キログラム", "กิโลกรัม"}},
		{"@unit:curr:gbp", []string{"gbp", "£", "pounds", "英镑"}},
		{"@unit:time:day", []string{"day", "days", "天", "日", "일", "tag", "วัน"}},
		{"@unit:time:month", []string{"month", "月", "월", "monat", "เดือน"}},
		{"@unit:time:year", []string{"year", "years", "年", "년", "jahr", "ปี"}},
		{"@unit:time:min", []string{"min", "分钟", "분", "minute", "นาที"}},
		{"@unit:time:s", []string{"sec", "秒", "초", "sekunde", "วินาที"}},
		{"@unit:power:kw", []string{"kw", "kilowatt", "千瓦", "kilowattstunde", "กิโลวัตต์"}},
		{"@unit:data:byte", []string{"byte", "bytes", "字节"}},
	}
	for _, g := range groups {
		for _, tok := range g.tokens {
			u, _ := multi.LookupUnitAndStatHarmonics(tok)
			if u != g.want {
				t.Fatalf("LookupUnitAndStatHarmonics(%q) = %q, want %q", tok, u, g.want)
			}
		}
	}
}

// TestCommonCoreDoesNotTagAmbiguousPlainWords 反噪声守卫：日常高频词/代词/动词/单字母
// 绝不能被误标为量纲。
func TestCommonCoreDoesNotTagAmbiguousPlainWords(t *testing.T) {
	ambiguous := []string{
		"in", "a", "i", "us", "is", "it", "bit", "cup", "try", "rub", "bar", "times",
		"degree", "pa", "ha", "ah", "va", "d", "mo", "ma", "card", "europ",
		"卡", "安", "欧", "元",
	}
	for _, tok := range ambiguous {
		if u, d := common.LookupCommonUnitAndStatHarmonics(tok); u != "" || d != "" {
			t.Fatalf("ambiguous plain word %q must stay untagged by common core, got (%q,%q)", tok, u, d)
		}
		if u, d := multi.LookupEnglishUnitHarmonics(tok); u != "" || d != "" {
			t.Fatalf("ambiguous plain word %q must stay untagged by English table, got (%q,%q)", tok, u, d)
		}
	}
}

// TestEnglishUnitHarmonicAtomsStayEightBitAndRoundTrip 端到端：英文谐波外挂产出的量纲伴随槽
// 必须落在 RMBP 8-bit F 域（f <= 0xFF），且 EncodeWirePayload/DecodeWirePayload 无损往返。
func TestEnglishUnitHarmonicAtomsStayEightBitAndRoundTrip(t *testing.T) {
	enc := ramesdk.NewBlindEncoder([]byte("harmonic-table-enrichment-key"))
	if err := Configure(enc, "en"); err != nil {
		t.Fatalf("Configure(en): %v", err)
	}
	text := "Paid 655.59 GBP for 3 nights and 120 km drive, battery 95% at -2 C, 12 kWh used."
	pkt, _ := enc.PackBlindPacket("ENRICH_1", text, 0)
	if len(pkt.Slots) == 0 {
		t.Fatal("expected non-empty blind pulse")
	}
	wantHashes := map[uint64]bool{
		enc.KeyedHash64("@unit:curr:gbp"):   true,
		enc.KeyedHash64("@dim:money"):       true,
		enc.KeyedHash64("@unit:len:km"):     true,
		enc.KeyedHash64("@unit:energy:kwh"): true,
	}
	seen := make(map[uint64]bool, 4)
	for i, a := range pkt.Slots {
		if a.ClassFlags > 0xFF {
			t.Fatalf("slot[%d] exceeds RMBP 8-bit flag domain: %#x", i, a.ClassFlags)
		}
		if wantHashes[a.H] {
			seen[a.H] = true
		}
	}
	for h := range wantHashes {
		if !seen[h] {
			t.Fatalf("expected canonical unit/dim harmonic %#x to be emitted", h)
		}
	}
	wire, err := ramesdk.EncodeWirePayload(pkt.Slots, pkt.SentenceFrames)
	if err != nil {
		t.Fatalf("EncodeWirePayload: %v", err)
	}
	decSlots, decFrames, err := ramesdk.DecodeWirePayload(wire)
	if err != nil {
		t.Fatalf("DecodeWirePayload: %v", err)
	}
	if !reflect.DeepEqual(decSlots, pkt.Slots) || !reflect.DeepEqual(decFrames, pkt.SentenceFrames) {
		t.Fatal("RMBP round trip changed the enriched English pulse")
	}
}

// TestSEAsianUnitWordsMatchAndScan 钉住 SEA（泰/老/高棉）可选外挂单位词两层通路。
func TestSEAsianUnitWordsMatchAndScan(t *testing.T) {
	direct := []struct{ token, unit string }{
		{"กิโลกรัม", "@unit:mass:kg"},
		{"นาที", "@unit:time:min"},
		{"เปอร์เซ็นต์", "@unit:stat:pct"},
		{"ກິໂລກຣາມ", "@unit:mass:kg"},
		{"ປີ", "@unit:time:year"},
		{"ກີບ", "@unit:curr:lak"},
		{"ມູນຄ່າ", ""},
		{"គីឡូក្រាម", "@unit:mass:kg"},
		{"ឆ្នាំ", "@unit:time:year"},
	}
	for _, c := range direct {
		u, _ := multi.LookupSEAsianUnitHarmonics(c.token)
		if u != c.unit {
			t.Fatalf("LookupSEAsianUnitHarmonics(%q) = %q, want %q", c.token, u, c.unit)
		}
	}

	got := multi.ExtractSEAsianUnitWords("ซื้อของหนัก 5 กิโลกรัม ใช้เวลา 30 นาที ลดราคา 20 เปอร์เซ็นต์")
	want := map[string]bool{"กิโลกรัม": true, "นาที": true, "เปอร์เซ็นต์": true}
	if len(got) != len(want) {
		t.Fatalf("ExtractSEAsianUnitWords = %v, want exactly %v", got, want)
	}
	for _, w := range got {
		if !want[w] {
			t.Fatalf("unexpected SEA unit word %q in %v", w, got)
		}
	}

	enc := ramesdk.NewBlindEncoder([]byte("harmonic-sea-key"))
	if err := Configure(enc, "th"); err != nil {
		t.Fatalf("Configure(th): %v", err)
	}
	pkt, _ := enc.PackBlindPacket("SEA_1", "ซื้อของหนัก 5 กิโลกรัม ใช้เวลา 30 นาที ลดราคา 20 เปอร์เซ็นต์", 0)
	if len(pkt.Slots) == 0 {
		t.Fatal("expected non-empty Thai blind pulse")
	}
	seen := make(map[uint64]bool, len(pkt.Slots))
	for _, a := range pkt.Slots {
		seen[a.H] = true
	}
	for _, tag := range []string{"@unit:mass:kg", "@unit:time:min", "@unit:stat:pct", "@dim:mass", "@dim:duration"} {
		if !seen[enc.KeyedHash64(tag)] {
			t.Fatalf("expected Thai pulse to carry %q harmonic", tag)
		}
	}
	wire, err := ramesdk.EncodeWirePayload(pkt.Slots, pkt.SentenceFrames)
	if err != nil {
		t.Fatalf("EncodeWirePayload(th): %v", err)
	}
	decSlots, _, err := ramesdk.DecodeWirePayload(wire)
	if err != nil || !reflect.DeepEqual(decSlots, pkt.Slots) {
		t.Fatalf("RMBP round trip changed the Thai enriched pulse: err=%v", err)
	}
}

// TestInlineSymbolScannerCoversExtendedCurrencyAndPermille 钉住紧贴数字的符号扩种。
func TestInlineSymbolScannerCoversExtendedCurrencyAndPermille(t *testing.T) {
	cases := []struct{ text, want string }{
		{"paid ₹500 today", "inr"},
		{"paid ₩9000 today", "krw"},
		{"paid ₽1500 today", "rub"},
		{"paid ₺300 today", "try"},
		{"paid ฿1200 today", "thb"},
		{"paid ₱450 today", "php"},
		{"paid ₫90000 today", "vnd"},
		{"paid ₪80 today", "ils"},
		{"margin 3‰ today", "ppm"},
	}
	for _, c := range cases {
		found := false
		for _, sym := range common.ExtractInlineSymbolUnits(c.text) {
			if sym == c.want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("ExtractInlineSymbolUnits(%q) missing %q, got %v", c.text, c.want, common.ExtractInlineSymbolUnits(c.text))
		}
	}
}

// TestISO3166MultilingualGeoHarmonicTable_L0ToL3 钉住 SDK 正本与 langpack 共用的
// ISO 3166 L0（大洲）-> L1（国家/地区与国籍形容词）-> L2（省/州）-> L3（核心城市）多语种地名两阶谐波映射。
func TestISO3166MultilingualGeoHarmonicTable_L0ToL3(t *testing.T) {
	cases := []struct {
		token string
		t1    string
		t2    string
	}{
		// L0 Continents
		{"european", "@geo:cont:eu", "@dim:geo"},
		{"欧洲", "@geo:cont:eu", "@dim:geo"},
		{"ヨーロッパ", "@geo:cont:eu", "@dim:geo"},
		{"유럽", "@geo:cont:eu", "@dim:geo"},
		// L1-L3 Europe (PT, FR, IT, ES, GB, DE, HU)
		{"portugal", "@geo:cc:pt", "@geo:cont:eu"},
		{"porto", "@geo:cc:pt", "@geo:cont:eu"},
		{"波尔图", "@geo:cc:pt", "@geo:cont:eu"},
		{"france", "@geo:cc:fr", "@geo:cont:eu"},
		{"marseille", "@geo:cc:fr", "@geo:cont:eu"},
		{"cassis", "@geo:cc:fr", "@geo:cont:eu"},
		{"巴黎", "@geo:cc:fr", "@geo:cont:eu"},
		{"milan", "@geo:cc:it", "@geo:cont:eu"},
		{"lombardy", "@geo:cc:it", "@geo:cont:eu"},
		{"santiagodecompostela", "@geo:cc:es", "@geo:cont:eu"},
		{"galicia", "@geo:cc:es", "@geo:cont:eu"},
		{"cambridge", "@geo:cc:gb", "@geo:cont:eu"},
		{"glasgow", "@geo:cc:gb", "@geo:cont:eu"},
		{"munich", "@geo:cc:de", "@geo:cont:eu"},
		{"budapest", "@geo:cc:hu", "@geo:cont:eu"},
		// L1-L3 Asia, Americas, Africa, Oceania
		{"yunnan", "@geo:cc:cn", "@geo:cont:as"},
		{"kunming", "@geo:cc:cn", "@geo:cont:as"},
		{"广州", "@geo:cc:cn", "@geo:cont:as"},
		{"tokyo", "@geo:cc:jp", "@geo:cont:as"},
		{"서울", "@geo:cc:kr", "@geo:cont:as"},
		{"กรุงเทพ", "@geo:cc:th", "@geo:cont:as"},
		{"amman", "@geo:cc:jo", "@geo:cont:as"},
		{"neworleans", "@geo:cc:us", "@geo:cont:am"},
		{"louisiana", "@geo:cc:us", "@geo:cont:am"},
		{"houston", "@geo:cc:us", "@geo:cont:am"},
		{"boston", "@geo:cc:us", "@geo:cont:am"},
		{"cairo", "@geo:cc:eg", "@geo:cont:af"},
		{"giza", "@geo:cc:eg", "@geo:cont:af"},
		{"sydney", "@geo:cc:au", "@geo:cont:oc"},
	}
	for _, c := range cases {
		g1, g2 := ramesdk.LookupISO3166GeoHarmonicPair(c.token)
		if g1 != c.t1 || g2 != c.t2 {
			t.Fatalf("ramesdk.LookupISO3166GeoHarmonicPair(%q) = (%q, %q), want (%q, %q)", c.token, g1, g2, c.t1, c.t2)
		}
		u1, u2 := multi.LookupUnitAndStatHarmonics(c.token)
		if u1 != c.t1 || u2 != c.t2 {
			t.Fatalf("multi.LookupUnitAndStatHarmonics(%q) = (%q, %q), want (%q, %q)", c.token, u1, u2, c.t1, c.t2)
		}
	}
}

// TestCoreOntologyHarmonicTables_KinshipCalendarBioAndObjects 钉住四大新增多语种闭集谐波表：
// 1. 亲属称谓与社交角色 (@kin/*, @role/*)
// 2. 日历周期、月份、星期、季节与节假日 (@cal/*)
// 3. 伴侣动物、家畜与植物 (@bio/*)
// 4. 交通、住宿、餐饮与财务票据生活载体 (@obj/*, @venue/*)
func TestCoreOntologyHarmonicTables_KinshipCalendarBioAndObjects(t *testing.T) {
	cases := []struct {
		token string
		t1    string
		t2    string
	}{
		// 1. Kinship & Social Roles
		{"mother", "@kin:parent:mother", "@kin:family"},
		{"妈妈", "@kin:parent:mother", "@kin:family"},
		{"お母さん", "@kin:parent:mother", "@kin:family"},
		{"엄마", "@kin:parent:mother", "@kin:family"},
		{"husband", "@kin:spouse", "@kin:family"},
		{"老婆", "@kin:spouse", "@kin:family"},
		{"colleague", "@role:colleague", "@dim:social"},
		{"同事", "@role:colleague", "@dim:social"},
		{"dentist", "@role:medical", "@dim:social"},
		{"牙医", "@role:medical", "@dim:social"},

		// 2. Calendar, Months, Seasons & Days of Week
		{"february", "@cal:month:02", "@cal:season:winter"},
		{"二月", "@cal:month:02", "@cal:season:winter"},
		{"spring", "@cal:season:spring", "@dim:time"},
		{"春天", "@cal:season:spring", "@dim:time"},
		{"weekend", "@cal:dow:weekend", "@dim:time"},
		{"周末", "@cal:dow:weekend", "@dim:time"},

		// 3. Animals, Pets & Plants
		{"kitten", "@bio:animal:cat", "@bio:pet"},
		{"猫咪", "@bio:animal:cat", "@bio:pet"},
		{"고양이", "@bio:animal:cat", "@bio:pet"},
		{"corgi", "@bio:animal:dog", "@bio:pet"},
		{"柴犬", "@bio:animal:dog", "@bio:pet"},
		{"cows", "@bio:animal:bovine", "@dim:bio"},
		{"mushroom", "@bio:plant", "@dim:bio"},
		{"蘑菇", "@bio:plant", "@dim:bio"},

		// 4. Transit, Lodging, Dining & Receipts
		{"aircraft", "@obj:transit:air", "@dim:travel"},
		{"航班", "@obj:transit:air", "@dim:travel"},
		{"bicycle", "@obj:transit:bike", "@dim:travel"},
		{"bike", "@obj:transit:bike", "@dim:travel"},
		{"自行车", "@obj:transit:bike", "@dim:travel"},
		{"hotel", "@venue:lodging", "@dim:travel"},
		{"accommodation", "@venue:lodging", "@dim:travel"},
		{"hilton", "@venue:lodging", "@dim:travel"},
		{"酒店", "@venue:lodging", "@dim:travel"},
		{"restaurant", "@venue:dining", "@dim:food"},
		{"餐厅", "@venue:dining", "@dim:food"},
		{"mochi", "@obj:food:dish", "@dim:food"},
		{"sashimi", "@obj:food:dish", "@dim:food"},
		{"invoice", "@obj:doc:receipt", "@dim:money"},
		{"receipt", "@obj:doc:receipt", "@dim:money"},
		{"报销", "@obj:doc:receipt", "@dim:money"},
	}
	for _, c := range cases {
		g1, g2 := ramesdk.LookupISO3166GeoHarmonicPair(c.token)
		if g1 != c.t1 || g2 != c.t2 {
			t.Fatalf("ramesdk.LookupISO3166GeoHarmonicPair(%q) = (%q, %q), want (%q, %q)", c.token, g1, g2, c.t1, c.t2)
		}
		u1, u2 := multi.LookupUnitAndStatHarmonics(c.token)
		if u1 != c.t1 || u2 != c.t2 {
			t.Fatalf("multi.LookupUnitAndStatHarmonics(%q) = (%q, %q), want (%q, %q)", c.token, u1, u2, c.t1, c.t2)
		}
	}
}

// TestMultiPackPreservesHighNibbleFormFlagsAndSentInitialGuard 钉住 langpack/multi 在启用时：
// 1. 保留 bit5 (FlagIsFocalHeader)、bit6 (FlagHasBoundaryQuote)、bit7 (FlagIsQuantityOrUnit) 高 4 位形态声明；
// 2. 句内含 ISO 日期（触发前置日期词扩展）时，句首首个表面词绝不因偏移而被误标 FlagHasUpper。
func TestMultiPackPreservesHighNibbleFormFlagsAndSentInitialGuard(t *testing.T) {
	enc := ramesdk.NewBlindEncoder([]byte("multi-form-flags-key"))
	if err := Configure(enc, "multi"); err != nil {
		t.Fatalf("Configure(multi): %v", err)
	}
	pkt, _ := enc.PackBlindPacket("FORM_MULTI_1",
		"[2026-05-01] 交付纪要：净重 50 千克，延迟 50 ms。\n# 结论\nThe quick brown fox paid $480 on 2026-05-01.",
		1732400000)
	var sawQuote, sawFocal, sawQty bool
	hThe := enc.KeyedHash64("the")
	hKg := enc.KeyedHash64("千克")
	hMs := enc.KeyedHash64("ms")
	hUsd := enc.KeyedHash64("usd")
	var kgFlags, msFlags, usdFlags uint8
	for _, a := range pkt.Slots {
		if a.ClassFlags&ramesdk.FlagHasBoundaryQuote != 0 {
			sawQuote = true
		}
		if a.ClassFlags&ramesdk.FlagIsFocalHeader != 0 {
			sawFocal = true
		}
		if a.ClassFlags&ramesdk.FlagIsQuantityOrUnit != 0 {
			sawQty = true
		}
		if a.H == hThe && (a.ClassFlags&ramesdk.FlagIsHarmonicCompanion) == 0 {
			if a.ClassFlags&ramesdk.FlagHasUpper != 0 {
				t.Fatalf("sentence-initial word 'The' in date-bearing sentence must not be marked FlagHasUpper, got flags=%#x", a.ClassFlags)
			}
		}
		if a.H == hKg && (a.ClassFlags&ramesdk.FlagIsHarmonicCompanion) == 0 {
			kgFlags |= a.ClassFlags
		}
		if a.H == hMs && (a.ClassFlags&ramesdk.FlagIsHarmonicCompanion) == 0 {
			msFlags |= a.ClassFlags
		}
		if a.H == hUsd && (a.ClassFlags&ramesdk.FlagIsHarmonicCompanion) == 0 {
			usdFlags |= a.ClassFlags
		}
	}
	if !sawQuote || !sawFocal || !sawQty {
		t.Fatalf("expected multi pack to preserve bit5/bit6/bit7 form flags: quote=%v focal=%v qty=%v", sawQuote, sawFocal, sawQty)
	}
	if kgFlags&ramesdk.FlagIsQuantityOrUnit == 0 || msFlags&ramesdk.FlagIsQuantityOrUnit == 0 || usdFlags&ramesdk.FlagIsQuantityOrUnit == 0 {
		t.Fatalf("expected quantity unit carriers (千克/ms/usd) to carry FlagIsQuantityOrUnit: kg=%#x ms=%#x usd=%#x", kgFlags, msFlags, usdFlags)
	}

	// Apostrophe fragments ("s" in "it's", "t" in "don't") must not emit false @unit:time:s / @unit:mass:ton harmonics,
	// whereas numeric-adjacent "5 m" must emit @unit:len:m.
	pkt2, _ := enc.PackBlindPacket("FORM_MULTI_2", "It's a 5 m cable, don't touch.", 1732400000)
	hUnitS := enc.KeyedHash64("@unit:time:s")
	hUnitTon := enc.KeyedHash64("@unit:mass:ton")
	hUnitM := enc.KeyedHash64("@unit:len:m")
	sawUnitM := false
	for _, a := range pkt2.Slots {
		if a.H == hUnitS || a.H == hUnitTon {
			t.Fatalf("apostrophe fragment must not emit unit harmonic (@unit:time:s or @unit:mass:ton), got atom=%+v", a)
		}
		if a.H == hUnitM {
			sawUnitM = true
		}
	}
	if !sawUnitM {
		t.Fatal("expected numeric-adjacent '5 m' to emit @unit:len:m harmonic")
	}
}
