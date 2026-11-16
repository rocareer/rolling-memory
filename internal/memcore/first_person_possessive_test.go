// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import "testing"

// 架构契约「代词 / 指代 / 关系 / 词面闸口 100% 归 LLM，代码不处理语义与词句」：
// 第一人称领属限定语检测已废止（原静态词表 {"my ","our ","我的","我们的"} 属代码语义猜测）。
// 本测试锁定废止契约：三枚 API 恒定不命中/原样返回；回归验证 = 把任何词表判断加回即红。
func TestFirstPersonPossessiveDetectionRetired(t *testing.T) {
	for _, s := range []string{"our deck", "My coffee", "我们的车", "我的车", "our own", "his deck", "the deck"} {
		if HasFirstPersonPossessivePrefix(s) {
			t.Fatalf("HasFirstPersonPossessivePrefix(%q) = true，代码不得再做人称/领属语义判定", s)
		}
		if got, changed := StripFirstPersonPossessivePrefix(s); changed || got != s {
			t.Fatalf("StripFirstPersonPossessivePrefix(%q) = (%q,%v)，应原样返回 (s,false)", s, got, changed)
		}
	}
	for _, s := range []string{"i", "me", "my", "mine", "myself", "我", "本人", "he", "they"} {
		if IsFirstPersonSingularPronoun(s) {
			t.Fatalf("IsFirstPersonSingularPronoun(%q) = true，人称判定归 LLM（代码不得用代词表）", s)
		}
	}
}
