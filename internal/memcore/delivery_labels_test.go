// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeliveryLabelsCatalogCompletenessAndDefaultEnglish(t *testing.T) {
	if DefaultDeliveryLang != DeliveryLangEN {
		t.Fatalf("DefaultDeliveryLang must be DeliveryLangEN (\"en\"), got %q", DefaultDeliveryLang)
	}
	if got := NormalizeDeliveryLang(""); got != DeliveryLangEN {
		t.Fatalf("NormalizeDeliveryLang(\"\") must default to en, got %q", got)
	}
	if got := NormalizeDeliveryLang("unknown-locale"); got != DeliveryLangEN {
		t.Fatalf("NormalizeDeliveryLang(\"unknown-locale\") must fallback to en, got %q", got)
	}
	if got := NormalizeDeliveryLang("zh-CN"); got != DeliveryLangZH {
		t.Fatalf("NormalizeDeliveryLang(\"zh-CN\") must resolve to zh, got %q", got)
	}
	if got := NormalizeDeliveryLang("bilingual"); got != DeliveryLangBilingual {
		t.Fatalf("NormalizeDeliveryLang(\"bilingual\") must resolve to bilingual, got %q", got)
	}

	entries := AllLabelEntries()
	if len(entries) != int(labelKeyCount) {
		t.Fatalf("expected %d catalog entries, got %d", int(labelKeyCount), len(entries))
	}
	seenNames := make(map[string]bool, len(entries))
	for i, e := range entries {
		if int(e.Key) != i {
			t.Fatalf("entry index %d has mismatched Key=%d", i, e.Key)
		}
		if e.Name == "" || seenNames[e.Name] {
			t.Fatalf("entry %d has empty or duplicate Name=%q", i, e.Name)
		}
		seenNames[e.Name] = true
		if e.EN == "" || e.ZH == "" || e.Bilingual == "" {
			t.Fatalf("entry %s (%d) has empty translation: EN=%q ZH=%q Bilingual=%q", e.Name, i, e.EN, e.ZH, e.Bilingual)
		}
		// Default Label(key) must equal EN
		if got := Label(e.Key); got != e.EN {
			t.Fatalf("Label(%s) default must be EN %q, got %q", e.Name, e.EN, got)
		}
		if got := LabelFor("", e.Key); got != e.EN {
			t.Fatalf("LabelFor(\"\", %s) must default to EN %q, got %q", e.Name, e.EN, got)
		}
		if got := LabelFor("zh", e.Key); got != e.ZH {
			t.Fatalf("LabelFor(\"zh\", %s) must return ZH %q, got %q", e.Name, e.ZH, got)
		}
		if !MatchesLabelAnyLang(e.EN, e.Key) || !MatchesLabelAnyLang(e.ZH, e.Key) {
			t.Fatalf("MatchesLabelAnyLang failed for %s", e.Name)
		}
	}
}

func TestReplaceDeliveryLabelsAndTokenReduction(t *testing.T) {
	zhSample := strings.Join([]string{
		"【记忆带·Elena Vance】主宾链 2 条 · 主谓链 1 条 · 未定 1 条（覆盖 2024-01-01→2024-05-10；链头 = 现状；链内时间倒序）",
		"  · [eng:attribution] 词元分布：主语「Elena Vance」；已逐字命中词元 [Solaris, quantum]，未逐字命中词元 [Mars]。",
		"〔主宾链·Solaris · 客体并存×2〕",
		"  · 2024-05-01 ｜ Elena Vance works_at Solaris ｜ 证据:[E#1][A#1·文档][A#2·代码] ｜ 讲述日:2024-05-02（spoken_on:2024-05-02 → event_time:2024-05-01） ｜ 相对时点:2024-05-01 ｜ 凭据:performative ｜ [已兑现] ｜ 位置:Zurich/Lab ｜ 另发生 2 次（2024-04-01） · 另提及 3 次 · 参与：Chen Mo",
		"  · 2024-01-01（讲述时点） ｜ Elena Vance asks ∅ ｜ [客体未定] ｜ [原句为疑问句 / Question in source]",
		"【主谓链·状态演进】",
		"〔主谓链·role→值〕",
		"〔主谓链·值: Junior→Lead〕",
		"  · 2024-03-01 ｜ Elena Vance role Lead Architect ｜ 原文:「[代码件:/src/main.go L1-L10]\nI am the Lead Architect at Solaris.」 ｜ 追问续答:「 Congrats!」",
		"…（截断：共 5 链 / 12 条，已显示 2 链 / 4 条）",
		"〔未显示链名：hobby→hiking / pet→cat〕",
		"【零散带】未锚定入带的相关件（仍有序；可随巩固晋升）",
		"【情景快照带·八面伴随记忆】由命中核心记忆激活的同场景伴随事实（尾插补池 · 不占主带头席）：",
		"  - [何人] [#101] [讲述人:Bob] Elena Vance —collaborates_with→ Bob",
		"【题面命中】行分 Top-3（含逐字锚 · 头部位束）",
		"【原文·补】金标词常开补充束（有界；原文未入图或未被选中时兜底）",
		"  · 2024-05-01 ｜ user: When did you join?\n    ↳ 紧邻回复: May 1.",
		"【原文·兜底】2024-05-01 ｜ user: fallback text",
		"【自答历史·回灌】本域同题历史（判决行 / 问 / 轮次 · 逐字）：",
		"  · 2024-05-01 ｜ 【上轮判定】上一轮选择 A，得分 0（判错） ｜ 问：When? ｜ 轮次：step01",
		"【话题概览·共现投影】以本次命中种子为中心，同断言共现的主体（按强度降序；锚 #id 可精确取回原文）：",
		"- Bob：关系 works_at · 共现 4 行 · 锚 #202",
		"【浮现】潜意识提示（未必与当前问题相关）",
		"· 承诺 · 3 天前 ｜ Submit report",
		"【画像·Elena Vance】(node 1 · 5 条活跃记忆单元)",
		"【正传·mem:person:1】分身 2 个 · 断言行 8 · 时间窗 2024-01-01 → 2024-05-01",
		"【记忆宇宙·八面场景快照】容器 ep:1 · 宏观地址 actual/chat/2024-05/lab · 成员 6 条",
		"[记忆脑实时耳语] 刚记住事实",
		"【证据带·证据行】证据行 1 条\n【证据行】",
		"  · [E#1] 2024-05-01 (Elena Vance→Solaris) ｜ 原文:「Joined Solaris on May 1.」 ｜ 证据件:[A#1·文档] ｜ 同日上文:[E#0]",
		"【证据带·证据件】证据件 1 件\n【证据件】",
		"  · [A#1] 【文档 · document · part#0 · 关联:[E#1]】 ｜ Solaris onboarding handbook",
	}, "\n")

	// Default targetLang="" must project to compact English ("en")
	enProjected := ReplaceDeliveryLabels(zhSample, "")
	for _, wantEN := range []string{
		"【Memory Band·Elena Vance】Subj-Obj 2 · Subj-Pred 1 · Unbound 1 (span 2024-01-01→2024-05-10; head = latest; reverse chronological)",
		"Token distribution: subject 「Elena Vance」; matched tokens [Solaris, quantum], unmatched tokens [Mars]",
		"〔Subj-Obj·Solaris · coexisting_objects×2〕",
		" ｜ Evidence:[E#1][A#1·document][A#2·code]",
		" ｜ spoken_on:2024-05-02 → event_time:2024-05-01",
		" ｜ rel_time:2024-05-01",
		" ｜ Warrant:performative",
		" ｜ [Fulfilled]",
		" ｜ Location:Zurich/Lab",
		"Also occurred 2 times（2024-04-01）",
		"Also mentioned 3 times",
		"Participants: Chen Mo",
		"(spoken_on)",
		"[object_unbound]",
		"[Question in source]",
		"【Subj-Pred · State Evolution】",
		"〔Subj-Pred·role→val〕",
		"〔Subj-Pred·val: Junior→Lead〕",
		"[Code:/src/main.go L1-L10]",
		" ｜ Quote:「[Code:/src/main.go L1-L10]\nI am the Lead Architect at Solaris.」",
		" ｜ Follow-up:「 Congrats!」",
		"…(truncated: total 5 chains / 12 rows, shown 2 chains / 4 rows)",
		"〔Omitted chains: hobby→hiking / pet→cat〕",
		"【Misc Band】",
		"【Episode Snapshot · 8-Facet Companion Memory】",
		"- [who] [#101] [Speaker:Bob]",
		"【Query Hits】 Top-3 (with verbatim anchors · head bundle)",
		"【Raw Supplement】",
		"↳ Next-turn reply: May 1.",
		"【Raw Fallback】2024-05-01",
		"【Self-Answer History · Feedback】",
		"【Prior Verdict】 prior choice A, score 0 (incorrect) ｜ Q: When? ｜ Turn: step01",
		"【Topic Overview · Co-occurrence Projection】",
		" · co-occur 4 rows · anchor #202",
		"【Subconscious Float】",
		"· Commitment · 3",
		"【Portrait·Elena Vance】(node 1 · 5 active memory units)",
		"【Memoir·mem:person:1】incarnations 2 · rows 8 · window 2024-01-01 → 2024-05-01",
		"【Memory Universe · 8-Facet Episode Snapshot】container ep:1 · macro actual/chat/2024-05/lab · members 6",
		"[Memory Whisper]",
		"【Evidence Band · Lines】Evidence lines: 1",
		"【Evidence Lines】",
		" ｜ Artifacts:[A#1·document]",
		" ｜ Same-day prior:[E#0]",
		"【Evidence Band · Artifacts】Evidence artifacts: 1",
		"【Evidence Artifacts】",
		"【document · document · part#0 · Related:[E#1]】",
	} {
		if !strings.Contains(enProjected, wantEN) {
			t.Fatalf("expected English projected text to contain %q, got:\n%s", wantEN, enProjected)
		}
	}

	// Verify verbatim evidence quotes and anchor IDs are 100% preserved
	for _, verbatim := range []string{
		"I am the Lead Architect at Solaris.",
		"Joined Solaris on May 1.",
		"Solaris onboarding handbook",
		"[E#1]",
		"[A#1",
		"[eng:attribution]",
	} {
		if !strings.Contains(enProjected, verbatim) {
			t.Fatalf("verbatim evidence %q was corrupted during label replacement:\n%s", verbatim, enProjected)
		}
	}

	// Verify token reduction in English mode via tokenz-r
	zhTokens := CountTokens(zhSample)
	enTokens := CountTokens(enProjected)
	if enTokens >= zhTokens {
		t.Fatalf("expected compact English meta-labels to use fewer tokens than Chinese/bilingual labels: en=%d >= zh=%d", enTokens, zhTokens)
	}

	// Round-trip back to Chinese ("zh")
	zhRestored := ReplaceDeliveryLabels(enProjected, "zh")
	for _, wantZH := range []string{
		"【记忆带·Elena Vance】主宾链 2 条 · 主谓链 1 条 · 未定 1 条（覆盖 2024-01-01→2024-05-10；链头 = 现状；链内时间倒序）",
		"〔主宾链·Solaris · 客体并存×2〕",
		"〔主谓链·role→值〕",
		"〔主谓链·值: Junior→Lead〕",
		"〔未显示链名：hobby→hiking / pet→cat〕",
		" ｜ 相对时点:2024-05-01",
		"另发生 2 次（2024-04-01）",
		"另提及 3 次",
		"[代码件:/src/main.go L1-L10]",
		"↳ 紧邻回复: May 1.",
		"【原文·兜底】",
		"【题面命中】行分 Top-3（含逐字锚 · 头部位束）",
		"【上轮判定】上一轮选择 A，得分 0（判错） ｜ 问：When? ｜ 轮次：step01",
		" · 共现 4 行 · 锚 #202",
		"· 承诺 · 3",
		"【画像·Elena Vance】(node 1 · 5 条活跃记忆单元)",
		"【正传·mem:person:1】分身 2 个 · 断言行 8 · 时间窗 2024-01-01 → 2024-05-01",
		"【记忆宇宙·八面场景快照】容器 ep:1 · 宏观地址 actual/chat/2024-05/lab · 成员 6",
		"[记忆脑实时耳语]",
		"【证据带·证据行】证据行 1",
		"【证据带·证据件】证据件 1",
		" ｜ [已兑现]",
		" ｜ 位置:Zurich/Lab",
		"- [何人] [#101] [讲述人:Bob]",
	} {
		if !strings.Contains(zhRestored, wantZH) {
			t.Fatalf("expected round-trip ZH text to contain %q, got:\n%s", wantZH, zhRestored)
		}
	}
}

func TestNonBreakageGuardForProtocolAndEncoderTokens(t *testing.T) {
	// 1. Structural line-start multimodal protocol markers ("【图片】", "【音频】", "【视频】", "【文件】")
	// and write-side encoder contract tokens ("【#1】", "已落图：") MUST NOT be mutated by ReplaceDeliveryLabels,
	// preventing byte-offset anchor drift and LLM tool-schema breakage.
	protectedSample := strings.Join([]string{
		"【#1】时间：2026-10-30 ｜ 性质：fact ｜ 说话人：Alice",
		"请看这张架构图：\n【图片】a photo of system architecture v2\n【音频】00:15 meeting recording\n【视频】demo clip\n【文件】spec.pdf",
		"已落图：Alice — works_at → Solaris [#101]",
		"  · [eng:attribution] Token distribution: subject",
	}, "\n")

	afterEN := ReplaceDeliveryLabels(protectedSample, "en")
	for _, mustStayIntact := range []string{
		"【#1】时间：2026-10-30",
		"\n【图片】a photo of system architecture v2",
		"\n【音频】00:15 meeting recording",
		"\n【视频】demo clip",
		"\n【文件】spec.pdf",
		"已落图：Alice — works_at → Solaris [#101]",
		"[eng:attribution]",
	} {
		if !strings.Contains(afterEN, mustStayIntact) {
			t.Fatalf("protocol/encoder contract token %q was broken by ReplaceDeliveryLabels:\n%s", mustStayIntact, afterEN)
		}
	}

	// 2. Verify StripSpokenPointSuffix handles ZH, EN, and Bilingual suffixes without breaking same-day comparison
	for _, rawTag := range []string{
		"2024-05-01（讲述时点）",
		"2024-05-01 (spoken_on)",
		"2024-05-01(spoken_on)",
		"2024-05-01（讲述时点/spoken_on）",
	} {
		if got := StripSpokenPointSuffix(rawTag); got != "2024-05-01" {
			t.Fatalf("StripSpokenPointSuffix(%q) = %q, want \"2024-05-01\"", rawTag, got)
		}
	}

	// 3. Verify ExtractSubjectFromBandHeader works on both EN and ZH headers
	if subj, ok := ExtractSubjectFromBandHeader("【Memory Band·Elena Vance】Subj-Obj 2"); !ok || subj != "Elena Vance" {
		t.Fatalf("ExtractSubjectFromBandHeader failed on EN header: subj=%q ok=%v", subj, ok)
	}
	if subj, ok := ExtractSubjectFromBandHeader("【记忆带·Elena Vance】主宾链 2 条"); !ok || subj != "Elena Vance" {
		t.Fatalf("ExtractSubjectFromBandHeader failed on ZH header: subj=%q ok=%v", subj, ok)
	}
}

func TestFormatHelpersDefaultEnglish(t *testing.T) {
	if got := ArtifactKindLabelFor("", "code"); got != "code" {
		t.Fatalf("expected default ArtifactKindLabelFor(\"\", \"code\") == \"code\", got %q", got)
	}
	if got := ArtifactKindLabelFor("zh", "code"); got != "代码" {
		t.Fatalf("expected ArtifactKindLabelFor(\"zh\", \"code\") == \"代码\", got %q", got)
	}
	if got := FacetRoleLabelFor("", "where"); got != "where" {
		t.Fatalf("expected default FacetRoleLabelFor(\"\", \"where\") == \"where\", got %q", got)
	}
	if got := FacetRoleLabelFor("zh", "where"); got != "何处" {
		t.Fatalf("expected FacetRoleLabelFor(\"zh\", \"where\") == \"何处\", got %q", got)
	}
	if got := FormatLoopStateTagFor("", LoopStateFulfilled, false); got != "[Fulfilled]" {
		t.Fatalf("expected default FormatLoopStateTagFor(\"\", fulfilled) == \"[Fulfilled]\", got %q", got)
	}
	if got := FormatLoopStateTagFor("zh", LoopStateFulfilled, false); got != "[已兑现]" {
		t.Fatalf("expected FormatLoopStateTagFor(\"zh\", fulfilled) == \"[已兑现]\", got %q", got)
	}
	if got := FormatSpokenEventDateTag("", "2024-05-02", "2024-05-01"); got != " ｜ spoken_on:2024-05-02 → event_time:2024-05-01" {
		t.Fatalf("unexpected default FormatSpokenEventDateTag: %q", got)
	}
}

// TestDeliveryLabelsSingleSourceOfTruthGate ensures that within internal/memcore production files,
// band headers and prospective loop tags are exclusively defined in delivery_labels*.go
// rather than duplicated as scattered string literals.
func TestDeliveryLabelsSingleSourceOfTruthGate(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob *.go: %v", err)
	}
	forbiddenLiterals := []string{
		`"【记忆带·"`,
		`"【Memory Band·"`,
		`"[待兑现]"`,
		`"[已兑现]"`,
		`"[已取消]"`,
		`"[已中止]"`,
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "delivery_labels.go" || f == "delivery_labels_replace.go" {
			continue
		}
		raw, rerr := os.ReadFile(f)
		if rerr != nil {
			t.Fatalf("read %s: %v", f, rerr)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			if cIdx := strings.Index(trimmed, "//"); cIdx >= 0 {
				trimmed = trimmed[:cIdx]
			}
			for _, lit := range forbiddenLiterals {
				if strings.Contains(trimmed, lit) {
					t.Fatalf("file %s contains scattered hardcoded label literal %s; use delivery_labels.go single source of truth instead", f, lit)
				}
			}
		}
	}
}
