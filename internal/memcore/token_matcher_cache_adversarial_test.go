// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"math"
	"testing"
	"unicode/utf8"
)

// refMatchTokenOrStemUncached 独立无缓存参考实现（逐字复刻旧 MatchTokenOrStemWithStem 每次重算路径）。
func refMatchTokenOrStemUncached(hay, tok, st string, legacyStem bool) float64 {
	if hay == "" || tok == "" {
		return 0
	}
	if legacyStem {
		if IsShortOrDigitsToken(tok) {
			if ContainsWordBounded(hay, tok) {
				return 1.0
			}
			return 0
		}
		if ContainsWordBounded(hay, tok) {
			return 1.0
		}
		return MatchTokenOrStemWithStem(hay, tok, st, true)
	}
	if IsShortOrDigitsToken(tok) {
		if ContainsWordBounded(hay, tok) {
			return 1.0
		}
		return 0
	}
	if HasIdeographRune(tok) {
		if IsPureHan(tok) {
			if len(hay) >= len(tok) && ContainsSubstring(hay, tok) {
				return 1.0
			}
			return 0
		}
		if ContainsSubstring(hay, tok) {
			return 1.0
		}
		qRunes := []rune(tok)
		bestIdeo := 0.0
		rem := hay
		for rem != "" {
			idx := indexRuneStr(rem, qRunes[0])
			if idx < 0 {
				break
			}
			tailRunes := []rune(rem[idx:])
			if len(tailRunes) > len(qRunes) {
				tailRunes = tailRunes[:len(qRunes)]
			}
			if sim := MorphSimWordPair(qRunes, nil, tailRunes); sim > bestIdeo {
				bestIdeo = sim
			}
			_, w := utf8.DecodeRuneInString(rem[idx:])
			if w <= 0 {
				break
			}
			rem = rem[idx+w:]
		}
		return bestIdeo
	}
	// 清空槽位强制走未命中重算后对比
	slot := fnv32Pair(tok, st) & 1023
	compiledTokSlots[slot].Store(nil)
	return MatchTokenOrStemWithStem(hay, tok, st, false)
}

func ContainsSubstring(s, sub string) bool {
	return indexStr(s, sub) >= 0
}

func indexStr(s, sub string) int {
	if sub == "" {
		return 0
	}
	if len(sub) > len(s) {
		return -1
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func indexRuneStr(s string, target rune) int {
	for i, r := range s {
		if r == target {
			return i
		}
	}
	return -1
}

// TestCompiledTokenSpecAndTimeWindowsCacheAdversarial 验证：
//  1. 预编译词元规格缓存在多语种（EN 屈折/复合词、CJK 纯汉字、日文汉字+假名混合、短词/数字、哈希槽冲突）下
//     与每次重算 buildCompiledTokenSpec 的结果 0 ULP 逐位相等；
//  2. ParseTimeWindows 缓存返回的切片被调用方原地修改后，绝不污染后续调用的缓存值（防御性拷贝守卫）。
func TestCompiledTokenSpecAndTimeWindowsCacheAdversarial(t *testing.T) {
	hays := []string{
		"caroline painted a sunset in her studio last week",
		"melanie went camping and swimming at the lake",
		"the fireworks band started in college",
		"食堂で美味しい寿司を食べた",
		"我们昨天在西湖边散步聊天",
		"version 12 release 2026-11-02",
		"",
	}
	toks := [][2]string{
		{"caroline", "carolin"},
		{"painted", "paint"},
		{"painting", "paint"},
		{"swimming", "swim"},
		{"fireworks", "firework"},
		{"firework", "firework"},
		{"食べる", "食べる"},
		{"西湖", "西湖"},
		{"12", "12"},
		{"in", "in"},
		{"nonexistenttokenxyz", "nonexistenttokenxyz"},
	}

	for _, pair := range toks {
		tok, st := pair[0], pair[1]
		for _, hay := range hays {
			// 第 1 次（冷槽位构建）
			slot := fnv32Pair(tok, st) & 1023
			compiledTokSlots[slot].Store(nil)
			cold := MatchTokenOrStemWithStem(hay, tok, st, false)
			// 第 2 次（热槽位命中）
			hot := MatchTokenOrStemWithStem(hay, tok, st, false)
			// 强制注入同槽位不同词元模拟哈希碰撞
			compiledTokSlots[slot].Store(buildCompiledTokenSpec("collision_other_tok", "collision_other_st"))
			afterCollision := MatchTokenOrStemWithStem(hay, tok, st, false)

			if math.Float64bits(cold) != math.Float64bits(hot) || math.Float64bits(cold) != math.Float64bits(afterCollision) {
				t.Fatalf("MatchTokenOrStemWithStem 缓存不等价 hay=%q tok=%q: cold=%v hot=%v coll=%v", hay, tok, cold, hot, afterCollision)
			}
		}
	}

	// 验证 ParseTimeWindows 缓存防御性拷贝：调用方篡改返回切片不得污染后续调用
	q := "We met on 2023-07-15 and again in August 2023."
	w1 := ParseTimeWindows(q)
	if len(w1) == 0 {
		t.Fatalf("expected non-empty time windows for %q", q)
	}
	origSince := w1[0].Since
	w1[0].Since = -999999 // 恶意原地篡改调用方拿到的切片
	w2 := ParseTimeWindows(q)
	if len(w2) != len(w1) || w2[0].Since != origSince {
		t.Fatalf("ParseTimeWindows 缓存被调用方切片篡改污染：got %+v, want Since=%d", w2, origSince)
	}
}

func BenchmarkMatchTokenOrStemMissHot(b *testing.B) {
	hay := "melanie went camping and swimming at the lake with her family"
	tok, st := "photography", "photographi"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = MatchTokenOrStemWithStem(hay, tok, st, false)
	}
}
