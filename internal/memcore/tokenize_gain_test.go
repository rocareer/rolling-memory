// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"strings"
	"testing"
)

func TestTokenizeZeroStopwordTableAndGainPriority(t *testing.T) {
	tokens := Tokenize("When did Caroline go to the pottery workshop in 2023?")
	want := map[string]bool{"caroline": true, "go": true, "pottery": true, "workshop": true, "2023": true}
	for _, tok := range tokens {
		delete(want, tok)
	}
	if len(want) > 0 {
		t.Fatalf("missing expected tokens %v from %v", want, tokens)
	}
	// Primary Gain Tier tokens must lead the slice
	if tokens[0] != "caroline" {
		t.Fatalf("expected Primary Gain Tier token 'caroline' at index 0, got %v", tokens)
	}

	cjkTokens := Tokenize("王小明在2023年去了哪里旅游？")
	found2023 := false
	for _, tok := range cjkTokens {
		if tok == "2023" {
			found2023 = true
		}
		if tok == "明年" {
			t.Fatalf("unexpected cross-boundary pseudo-bigram 明年 in %v", cjkTokens)
		}
	}
	if !found2023 {
		t.Fatalf("expected 2023 in %v", cjkTokens)
	}

	// Zero false kills ("代码可以放宽但不能误杀"): all 2-char/3-char Chinese content words,
	// acronyms, homographs, and idioms are 100% preserved without any stopword table.
	for _, cw := range []string{
		"成都", "首都", "地铁", "地址", "阳光", "激光", "将军", "就业", "方便", "便宜",
		"过敏", "过程", "数据", "证据", "自由", "方向", "被子", "人才", "未来", "性别",
		"偏见", "和平", "参与", "现在", "太阳", "目的", "得到", "获得", "觉得", "记得",
		"了解", "扶他林", "维他命", "吉他",
	} {
		toks := Tokenize(cw)
		if len(toks) == 0 || toks[0] != cw {
			t.Fatalf("Chinese content word %q wiped or corrupted by Tokenize: got %v", cw, toks)
		}
	}
	if bad, reason := IsInvalidAliasSurface("双氯芬酸二乙胺乳胶剂", "扶他林"); bad {
		t.Fatalf("valid Chinese drug alias '扶他林' rejected as %q", reason)
	}

	// Endogenous BPE constituent unwrapping: zero cross-constituent ghost bigrams (用什, 么比, 给自, 己设, 么切)
	for _, tc := range []struct {
		query       string
		forbidGhost []string
		mustHave    []string
	}{
		{
			query:       "医生，最近膝盖又有点刺痛，外用什么比较好？",
			forbidGhost: []string{"用什", "么比"},
			mustHave:    []string{"医生", "膝盖", "刺痛", "外用", "比较"},
		},
		{
			query:       "帮我想想，春节后我有没有给自己设过更具体的跑步目标？",
			forbidGhost: []string{"有给", "给自", "己设"},
			mustHave:    []string{"春节", "具体", "跑步", "目标"},
		},
		{
			query:       "不知道怎么切这个系统字段",
			forbidGhost: []string{"么切"},
			mustHave:    []string{"知道", "系统", "字段"},
		},
	} {
		got := Tokenize(tc.query)
		set := map[string]bool{}
		for _, tk := range got {
			set[tk] = true
		}
		for _, g := range tc.forbidGhost {
			if set[g] {
				t.Fatalf("ghost pseudo-bigram %q leaked in Tokenize(%q) = %v", g, tc.query, got)
			}
		}
		for _, m := range tc.mustHave {
			if !set[m] {
				t.Fatalf("expected content token %q missing in Tokenize(%q) = %v", m, tc.query, got)
			}
		}
	}

	// Acronyms, homographs, hyphenated compounds, apostrophe names, Japanese Kanji+Kana, 3-char BPE CJK
	for _, acr := range [][2]string{
		{"United States", "US"},
		{"Information Technology", "IT"},
		{"World Health Organization", "WHO"},
	} {
		if bad, reason := IsInvalidAliasSurface(acr[0], acr[1]); bad {
			t.Fatalf("IsInvalidAliasSurface(%q, %q) = true (%s), want false", acr[0], acr[1], reason)
		}
		if toks := Tokenize(acr[1] + " department"); len(toks) < 2 || !hasToken(toks, strings.ToLower(acr[1])) {
			t.Fatalf("Tokenize(%q) = %v, want %q preserved", acr[1]+" department", toks, strings.ToLower(acr[1]))
		}
	}
	if wbToks := Tokenize("mental well-being"); !hasToken(wbToks, "wellbeing") {
		t.Fatalf("'well-being' missing 'wellbeing' in Tokenize: got %v", wbToks)
	}
	xianToks := Tokenize("visited Xi'an and met Shaquille O'Neal")
	if !hasToken(xianToks, "xian") || !hasToken(xianToks, "oneal") {
		t.Fatalf("expected 'xian' and 'oneal' in %v", xianToks)
	}
	jpToks := Tokenize("昨日東京に行く、ラーメンを食べる")
	for _, wantJP := range []string{"昨日", "東京", "ラーメン", "食べる"} {
		if !hasToken(jpToks, wantJP) {
			t.Fatalf("missing Japanese token %q in %v", wantJP, jpToks)
		}
	}
	cjkBpeToks := Tokenize("他在哈尔滨研究数据库和莫斯科历史")
	for _, want3 := range []string{"哈尔滨", "数据库", "莫斯科"} {
		if !hasToken(cjkBpeToks, want3) {
			t.Fatalf("missing 3-char BPE token %q in %v", want3, cjkBpeToks)
		}
	}

	// Long query (> MaxQueryTokens=24) gain-based compaction ("香农与 BPE 只负责增益"):
	longQ := "note item list info page text neurosurgery cardiothoracic immunotherapy glioblastoma " +
		"t10 t11 t12 t13 t14 t15 t16 t17 t18 t19 t20 t21 t22 t23 t24 t25 t26 t27"
	compacted := Tokenize(longQ)
	if len(compacted) != MaxQueryTokens {
		t.Fatalf("expected len(compacted)==%d, got %d (%v)", MaxQueryTokens, len(compacted), compacted)
	}
	for _, mustMid := range []string{"neurosurgery", "cardiothoracic", "immunotherapy", "glioblastoma"} {
		if !hasToken(compacted, mustMid) {
			t.Fatalf("ultra-high-entropy mid term %q was dropped in %v", mustMid, compacted)
		}
	}
}

func hasToken(toks []string, want string) bool {
	for _, tk := range toks {
		if tk == want {
			return true
		}
	}
	return false
}
