// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"fmt"
	"strings"
)

// ArtifactKindLabelFor returns the localized label for an artifact kind ("image", "audio", "video", "code", "document", "file").
// Defaults to English when lang is empty.
func ArtifactKindLabelFor(lang string, kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "image", "img", "photo", "picture":
		return LabelFor(lang, LabelArtifactImage)
	case "audio", "voice", "sound", "speech":
		return LabelFor(lang, LabelArtifactAudio)
	case "video", "movie", "clip":
		return LabelFor(lang, LabelArtifactVideo)
	case "code", "diff", "source":
		return LabelFor(lang, LabelArtifactCode)
	case "document", "doc", "pdf", "markdown":
		return LabelFor(lang, LabelArtifactDocument)
	default:
		return LabelFor(lang, LabelArtifactFile)
	}
}

// FacetRoleLabelFor returns the localized 8-facet episode role label (defaults to English when lang is empty).
func FacetRoleLabelFor(lang string, roleFacet string) string {
	switch strings.ToLower(strings.TrimSpace(roleFacet)) {
	case "who":
		return LabelFor(lang, LabelFacetWho)
	case "when":
		return LabelFor(lang, LabelFacetWhen)
	case "where":
		return LabelFor(lang, LabelFacetWhere)
	case "what":
		return LabelFor(lang, LabelFacetWhat)
	case "why":
		return LabelFor(lang, LabelFacetWhy)
	case "how":
		return LabelFor(lang, LabelFacetHow)
	case "result", "outcome":
		return LabelFor(lang, LabelFacetResult)
	case "object":
		return LabelFor(lang, LabelFacetObject)
	default:
		return LabelFor(lang, LabelFacetWhat)
	}
}

// FormatLoopStateTagFor returns the localized inline tag for a prospective loop state.
// Defaults to English ("[Pending]", "[Fulfilled]", "[Waived]", "[Aborted]") when lang is empty or "en".
func FormatLoopStateTagFor(lang string, loopState string, includeOpen bool) string {
	switch strings.ToLower(strings.TrimSpace(loopState)) {
	case LoopStateOpen:
		if includeOpen {
			return LabelFor(lang, LabelTagLoopOpen)
		}
		return ""
	case LoopStateFulfilled:
		return LabelFor(lang, LabelTagLoopFulfilled)
	case LoopStateWaived:
		return LabelFor(lang, LabelTagLoopWaived)
	case LoopStateBroken:
		return LabelFor(lang, LabelTagLoopBroken)
	default:
		return ""
	}
}

// FormatSpokenEventDateTag formats the dual-temporal anchor suffix (spoken_on -> event_time)
// in the requested language (defaults to compact English `spoken_on:X → event_time:Y`).
func FormatSpokenEventDateTag(lang string, srcTag, evTag string) string {
	switch NormalizeDeliveryLang(lang) {
	case DeliveryLangZH:
		return " ｜ 讲述日:" + srcTag + " → 事件时点:" + evTag
	case DeliveryLangBilingual:
		return " ｜ 讲述日:" + srcTag + "（spoken_on:" + srcTag + " → event_time:" + evTag + "）"
	default:
		return " ｜ spoken_on:" + srcTag + " → event_time:" + evTag
	}
}

// FormatEvidenceLinesBandHeader formats the header of the [E#k] evidence lines band.
func FormatEvidenceLinesBandHeader(lang string, count int) string {
	if NormalizeDeliveryLang(lang) == DeliveryLangZH {
		return fmt.Sprintf("【证据带·证据行】证据行 %d 条\n【证据行】", count)
	}
	return fmt.Sprintf("【Evidence Band · Lines】%d lines\n【Evidence Lines】", count)
}

// FormatEvidenceArtifactsBandHeader formats the header of the [A#m] evidence artifacts band.
func FormatEvidenceArtifactsBandHeader(lang string, count int) string {
	if NormalizeDeliveryLang(lang) == DeliveryLangZH {
		return fmt.Sprintf("【证据带·证据件】证据件 %d 件\n【证据件】", count)
	}
	return fmt.Sprintf("【Evidence Band · Artifacts】%d artifacts\n【Evidence Artifacts】", count)
}

// MatchesLabelAnyLang reports whether s contains either the EN, ZH, or Bilingual variant of key.
func MatchesLabelAnyLang(s string, key LabelKey) bool {
	if key >= labelKeyCount || s == "" {
		return false
	}
	e := deliveryLabelTable[key]
	return (e.EN != "" && strings.Contains(s, e.EN)) ||
		(e.ZH != "" && strings.Contains(s, e.ZH)) ||
		(e.Bilingual != "" && strings.Contains(s, e.Bilingual))
}

// IsLabelExactly reports whether s equals any language variant of key (surrounding whitespace ignored).
// 读链哨兵比较统一走本函数：切换交付语种后 `"时间未知"` 字面量比较会断链（架构契约「不散落硬编码标签」）。
func IsLabelExactly(s string, key LabelKey) bool {
	if key >= labelKeyCount {
		return false
	}
	t := strings.TrimSpace(s)
	if t == "" {
		return false
	}
	e := deliveryLabelTable[key]
	return t == e.EN || t == e.ZH || t == e.Bilingual
}

// StripSpokenPointSuffix removes the spoken-time fallback suffix from an event date tag
// regardless of whether it was rendered in Chinese ("（讲述时点）"), English (" (spoken_on)" / "(spoken_on)"),
// or bilingual ("（讲述时点/spoken_on）"), preventing same-day evidence linkage breakage.
func StripSpokenPointSuffix(evTag string) string {
	s := evTag
	for _, suf := range []string{
		deliveryLabelTable[LabelTimeSpokenPointSuffix].ZH,
		" " + deliveryLabelTable[LabelTimeSpokenPointSuffix].EN,
		deliveryLabelTable[LabelTimeSpokenPointSuffix].EN,
		deliveryLabelTable[LabelTimeSpokenPointSuffix].Bilingual,
	} {
		if suf != "" && strings.HasSuffix(s, suf) {
			return strings.TrimSuffix(s, suf)
		}
	}
	return s
}

// ExtractSubjectFromBandHeader extracts the subject key from a band header regardless of whether
// it was rendered in English ("【Memory Band·X】") or Chinese ("【记忆带·X】").
func ExtractSubjectFromBandHeader(bandHeader string) (string, bool) {
	for _, pfx := range []string{
		deliveryLabelTable[LabelBandSubjectPrefix].EN,
		deliveryLabelTable[LabelBandSubjectPrefix].ZH,
	} {
		if i := strings.Index(bandHeader, pfx); i >= 0 {
			rest := bandHeader[i+len(pfx):]
			if j := strings.Index(rest, "】"); j > 0 {
				return rest[:j], true
			}
		}
	}
	return "", false
}

// ReplaceDeliveryLabels translates presentation-layer meta-labels inside an already-rendered
// delivery string or LLM context block into targetLang ("en" default, "zh", or "bilingual").
// Verbatim evidence quotes, node labels, multimodal byte-offset tags ("【图片】"), and structural
// anchors ([E#k], [A#m], #id) are preserved intact.
func ReplaceDeliveryLabels(text string, targetLang string) string {
	if text == "" {
		return ""
	}
	dst := NormalizeDeliveryLang(targetLang)
	var pairs []string
	addPair := func(from, to string) {
		if from != "" && to != "" && from != to {
			pairs = append(pairs, from, to)
		}
	}

	switch dst {
	case DeliveryLangEN:
		// 1. Multi-token band & section headers (longest match first)
		addPair("【证据带·证据行】证据行 ", deliveryLabelTable[LabelBandEvidenceLinesTitle].EN+"Evidence lines: ")
		addPair("【证据带·证据件】证据件 ", deliveryLabelTable[LabelBandEvidenceArtifactsTitle].EN+"Evidence artifacts: ")
		addPair("【证据带·集中存证】证据行 ", deliveryLabelTable[LabelBandEvidenceCombinedTitle].EN+"Evidence lines: ")
		addPair(" 条 · 证据件 ", " · Evidence artifacts: ")
		addPair(" 条\n【证据行】", "\n"+deliveryLabelTable[LabelSectionEvidenceLines].EN)
		addPair(" 件\n【证据件】", "\n"+deliveryLabelTable[LabelSectionEvidenceArtifacts].EN)
		addPair("【证据带·证据行】", deliveryLabelTable[LabelBandEvidenceLinesTitle].EN)
		addPair("【证据带·证据件】", deliveryLabelTable[LabelBandEvidenceArtifactsTitle].EN)
		addPair("【证据带·集中存证】", deliveryLabelTable[LabelBandEvidenceCombinedTitle].EN)
		addPair("\n【证据行】", "\n"+deliveryLabelTable[LabelSectionEvidenceLines].EN)
		addPair("\n【证据件】", "\n"+deliveryLabelTable[LabelSectionEvidenceArtifacts].EN)
		addPair(deliveryLabelTable[LabelBandEpisodeSnapshotTitle].ZH, deliveryLabelTable[LabelBandEpisodeSnapshotTitle].EN)
		addPair(deliveryLabelTable[LabelBandMiscTitle].ZH, deliveryLabelTable[LabelBandMiscTitle].EN)
		addPair(deliveryLabelTable[LabelBandPredObjTitle].ZH, deliveryLabelTable[LabelBandPredObjTitle].EN)
		addPair(deliveryLabelTable[LabelBandFallbackRawTitle].ZH, deliveryLabelTable[LabelBandFallbackRawTitle].EN)
		addPair(deliveryLabelTable[LabelBandSupplementRawTitle].ZH, deliveryLabelTable[LabelBandSupplementRawTitle].EN)
		addPair(deliveryLabelTable[LabelBandPreciseFallbackPrefix].ZH, deliveryLabelTable[LabelBandPreciseFallbackPrefix].EN)
		addPair("【题面命中】行分 Top-", deliveryLabelTable[LabelBandHeadHitsTitle].EN+" Top-")
		addPair("（含逐字锚 · 头部位束）", " (with verbatim anchors · head bundle)")
		addPair(deliveryLabelTable[LabelBandHeadHitsTitle].ZH, deliveryLabelTable[LabelBandHeadHitsTitle].EN)
		addPair(deliveryLabelTable[LabelBandSelfFeedbackTitle].ZH, deliveryLabelTable[LabelBandSelfFeedbackTitle].EN)
		addPair(deliveryLabelTable[LabelBandTopicClusterTitle].ZH, deliveryLabelTable[LabelBandTopicClusterTitle].EN)
		addPair(deliveryLabelTable[LabelBandFloatTitle].ZH, deliveryLabelTable[LabelBandFloatTitle].EN)
		addPair(deliveryLabelTable[LabelBandPortraitPrefix].ZH, deliveryLabelTable[LabelBandPortraitPrefix].EN)
		addPair(" 条活跃记忆单元)", " active memory units)")
		addPair(deliveryLabelTable[LabelBandMemoirPrefix].ZH, deliveryLabelTable[LabelBandMemoirPrefix].EN)
		addPair("】域 ", "】scopes ")
		addPair(" 个 · 分身 ", " · incarnations ")
		addPair("】分身 ", "】incarnations ")
		addPair(" 个 · 断言行 ", " · rows ")
		addPair(" · 时间窗 ", " · window ")
		addPair(deliveryLabelTable[LabelBandUniverseSnapshotPrefix].ZH+"容器 ", deliveryLabelTable[LabelBandUniverseSnapshotPrefix].EN+"container ")
		addPair(deliveryLabelTable[LabelBandUniverseSnapshotPrefix].ZH, deliveryLabelTable[LabelBandUniverseSnapshotPrefix].EN)
		addPair(" · 宏观地址 ", " · macro ")
		addPair(" · 成员 ", " · members ")
		addPair("  [上级超容器/super]（", "  [Super-containers]（")
		addPair("  [下级子容器成员/children]（", "  [Child-containers]（")
		addPair(" 个容器成员）：", " members）：")
		addPair("    * 子容器 ", "    * child ")
		addPair("· 承诺 · ", "· Commitment · ")
		addPair("· 实体 · ", "· Entity · ")
		addPair("· 常想 · ", "· Recurrent · ")
		addPair("· 未决 · ", "· Unresolved · ")
		addPair("关系面未命名", "unnamed_relations")
		addPair(" · 共现 ", " · co-occur ")
		addPair(" 行 · 锚 #", " rows · anchor #")
		addPair("按选择表装配（表序 = 优先序；链内时间倒序）", "assembled by selection table (table order = priority; reverse chronological)")
		addPair(deliveryLabelTable[LabelSectionSubjPredCont].ZH, deliveryLabelTable[LabelSectionSubjPredCont].EN)
		addPair(deliveryLabelTable[LabelSectionSubjPred].ZH, deliveryLabelTable[LabelSectionSubjPred].EN)
		addPair(deliveryLabelTable[LabelSectionUnbound].ZH, deliveryLabelTable[LabelSectionUnbound].EN)
		addPair(deliveryLabelTable[LabelSectionIntransitive].ZH, deliveryLabelTable[LabelSectionIntransitive].EN)
		addPair(deliveryLabelTable[LabelSectionIntransitive].Bilingual, deliveryLabelTable[LabelSectionIntransitive].EN)
		addPair("；链头 = 现状；链内时间倒序）", "; "+deliveryLabelTable[LabelBandTailRule].EN+")")
		addPair("（链头 = 现状；链内时间倒序）", " ("+deliveryLabelTable[LabelBandTailRule].EN+")")
		addPair(deliveryLabelTable[LabelBandTailRule].ZH, deliveryLabelTable[LabelBandTailRule].EN)
		addPair("】主宾链 ", "】Subj-Obj ")
		addPair(" 条 · 主谓链 ", " · Subj-Pred ")
		addPair(" 条 · 未定 ", " · Unbound ")
		addPair(" 条（覆盖 ", " (span ")
		addPair(" 条 (", " (")
		addPair(deliveryLabelTable[LabelBandSubjectPrefix].ZH, deliveryLabelTable[LabelBandSubjectPrefix].EN)
		addPair(deliveryLabelTable[LabelChainOmittedPrefix].ZH, deliveryLabelTable[LabelChainOmittedPrefix].EN)
		addPair(" · "+deliveryLabelTable[LabelChainObjectCoexist].ZH, " · "+deliveryLabelTable[LabelChainObjectCoexist].EN)
		addPair("→值]", "→"+deliveryLabelTable[LabelChainValueTag].EN+"]")
		addPair("→值〕", "→"+deliveryLabelTable[LabelChainValueTag].EN+"〕")
		addPair("〔主谓链·值: ", "〔Subj-Pred·"+deliveryLabelTable[LabelChainValueTag].EN+": ")
		addPair("·值: ", "·"+deliveryLabelTable[LabelChainValueTag].EN+": ")
		addPair("\n…（截断：共 ", "\n…(truncated: total ")
		addPair(" 链 / ", " chains / ")
		addPair(" 条，已显示 ", " rows, shown ")
		addPair(" 条）", " rows)")

		// 2. Inline delimiters, provenance pointers & temporal/modality tags
		addPair("[原句为疑问句 / Question in source]", deliveryLabelTable[LabelTagQuestionInSource].EN)
		addPair("[原句为疑问句]", deliveryLabelTable[LabelTagQuestionInSource].EN)
		addPair("【上轮判定】上一轮选择 ", deliveryLabelTable[LabelSelfFeedbackVerdictPrefix].EN+" prior choice ")
		addPair("，得分 ", ", score ")
		addPair("（判对）", " (correct)")
		addPair("（判错）", " (incorrect)")
		addPair(" ｜ 问：", " ｜ Q: ")
		addPair(" ｜ 轮次：", " ｜ Turn: ")
		addPair(deliveryLabelTable[LabelSelfFeedbackVerdictPrefix].ZH, deliveryLabelTable[LabelSelfFeedbackVerdictPrefix].EN)
		addPair(deliveryLabelTable[LabelWhisperRealtimePrefix].ZH, deliveryLabelTable[LabelWhisperRealtimePrefix].EN)
		addPair(" ｜ 原文:「", " ｜ Quote:「")
		addPair(" ｜ 追问续答:「", " ｜ Follow-up:「")
		addPair(deliveryLabelTable[LabelFieldAdjacentReply].ZH, deliveryLabelTable[LabelFieldAdjacentReply].EN)
		addPair(" ｜ 讲述人(", " ｜ Speaker(")
		addPair(" [讲述人:", " [Speaker:")
		addPair(" ｜ 证据:[", " ｜ Evidence:[")
		addPair(" ｜ 证据件:[", " ｜ Artifacts:[")
		addPair(" · 关联:[", " · Related:[")
		addPair(" ｜ 同日上文:[", " ｜ Same-day prior:[")
		addPair(" ｜ 凭据:", " ｜ Warrant:")
		addPair(" ｜ 位置:", " ｜ Location:")
		addPair(" ｜ 相对时点:", " ｜ "+deliveryLabelTable[LabelFieldRelativeTime].EN+":")
		addPair(deliveryLabelTable[LabelFieldCodePartPrefix].ZH, deliveryLabelTable[LabelFieldCodePartPrefix].EN)
		addPair("另发生 ", deliveryLabelTable[LabelFieldOtherOccurrences].EN+" ")
		addPair(" 次（", " times（")
		addPair("另提及 ", deliveryLabelTable[LabelFieldOtherMentions].EN+" ")
		addPair(" 次", " times")
		addPair("参与：", "Participants: ")
		addPair(" ｜ 锚异常（复核未过）", " ｜ "+deliveryLabelTable[LabelFieldAnchorAnomaly].EN)
		addPair("（讲述时点）", " "+deliveryLabelTable[LabelTimeSpokenPointSuffix].EN)
		addPair("讲述日/spoken_on:", "spoken_on:")
		addPair("事件时点/event_time:", "event_time:")
		addPair("[待兑现]", deliveryLabelTable[LabelTagLoopOpen].EN)
		addPair("[已兑现]", deliveryLabelTable[LabelTagLoopFulfilled].EN)
		addPair("[已取消]", deliveryLabelTable[LabelTagLoopWaived].EN)
		addPair("[已中止]", deliveryLabelTable[LabelTagLoopBroken].EN)
		addPair("[本会话热活]", deliveryLabelTable[LabelHotSessionActive].EN)
		addPair("[热活]", deliveryLabelTable[LabelHotCrossActive].EN)
		addPair(" · 已更替]", " · replaced]")
		addPair("主体未定", deliveryLabelTable[LabelGapSubject].EN)
		addPair("客体未定", deliveryLabelTable[LabelGapObject].EN)
		addPair("来源不明", deliveryLabelTable[LabelGapHolder].EN)
		addPair("（无主体）", deliveryLabelTable[LabelSubjectAbsent].EN)
		addPair("·图片]", "·image]")
		addPair("·音频]", "·audio]")
		addPair("·视频]", "·video]")
		addPair("·代码]", "·code]")
		addPair("·文档]", "·document]")
		addPair("·文件]", "·file]")
		addPair("【图片 · ", "【image · ")
		addPair("【音频 · ", "【audio · ")
		addPair("【视频 · ", "【video · ")
		addPair("【代码 · ", "【code · ")
		addPair("【文档 · ", "【document · ")
		addPair("【文件 · ", "【file · ")
		addPair("词元分布：主语「", "Token distribution: subject 「")
		addPair("词元分布：", "Token distribution: ")
		addPair("」；查询词元 [", "」; query tokens [")
		addPair("] 出现于「", "] appear in 「")
		addPair("」本人陈述行（未逐字出现于「", "」's own statements (not verbatim in 「")
		addPair("」本人陈述行）。", "」's own statements).")
		addPair("」；已逐字命中词元 [", "」; matched tokens [")
		addPair("]，未逐字命中词元 [", "], unmatched tokens [")
		addPair(" · 覆盖 ", " · span ")
		addPair("〔主宾链·", "〔Subj-Obj·")
		addPair("〔主谓链·", "〔Subj-Pred·")
		addPair("〔谓宾链·", "〔Pred-Obj·")
		addPair("〔未定·", "〔Unbound·")
		addPair("〔二元·", "〔Intransitive·")
		for _, fk := range []LabelKey{
			LabelFacetWho, LabelFacetWhen, LabelFacetWhere, LabelFacetWhat,
			LabelFacetWhy, LabelFacetHow, LabelFacetResult, LabelFacetObject,
		} {
			addPair("- ["+deliveryLabelTable[fk].ZH+"]", "- ["+deliveryLabelTable[fk].EN+"]")
		}

	case DeliveryLangZH:
		addPair(deliveryLabelTable[LabelBandEvidenceLinesTitle].EN+"Evidence lines: ", "【证据带·证据行】证据行 ")
		addPair(deliveryLabelTable[LabelBandEvidenceArtifactsTitle].EN+"Evidence artifacts: ", "【证据带·证据件】证据件 ")
		addPair(deliveryLabelTable[LabelBandEvidenceCombinedTitle].EN+"Evidence lines: ", "【证据带·集中存证】证据行 ")
		addPair(" · Evidence artifacts: ", " 条 · 证据件 ")
		addPair(deliveryLabelTable[LabelBandEvidenceLinesTitle].EN, "【证据带·证据行】")
		addPair(deliveryLabelTable[LabelBandEvidenceArtifactsTitle].EN, "【证据带·证据件】")
		addPair(deliveryLabelTable[LabelBandEvidenceCombinedTitle].EN, "【证据带·集中存证】")
		addPair("\n"+deliveryLabelTable[LabelSectionEvidenceLines].EN, "\n【证据行】")
		addPair("\n"+deliveryLabelTable[LabelSectionEvidenceArtifacts].EN, "\n【证据件】")
		addPair(deliveryLabelTable[LabelBandEpisodeSnapshotTitle].EN, deliveryLabelTable[LabelBandEpisodeSnapshotTitle].ZH)
		addPair(deliveryLabelTable[LabelBandMiscTitle].EN, deliveryLabelTable[LabelBandMiscTitle].ZH)
		addPair(deliveryLabelTable[LabelBandPredObjTitle].EN, deliveryLabelTable[LabelBandPredObjTitle].ZH)
		addPair(deliveryLabelTable[LabelBandFallbackRawTitle].EN, deliveryLabelTable[LabelBandFallbackRawTitle].ZH)
		addPair(deliveryLabelTable[LabelBandSupplementRawTitle].EN, deliveryLabelTable[LabelBandSupplementRawTitle].ZH)
		addPair(deliveryLabelTable[LabelBandPreciseFallbackPrefix].EN, deliveryLabelTable[LabelBandPreciseFallbackPrefix].ZH)
		addPair(deliveryLabelTable[LabelBandHeadHitsTitle].EN+" Top-", "【题面命中】行分 Top-")
		addPair(" (with verbatim anchors · head bundle)", "（含逐字锚 · 头部位束）")
		addPair(deliveryLabelTable[LabelBandHeadHitsTitle].EN, deliveryLabelTable[LabelBandHeadHitsTitle].ZH)
		addPair(deliveryLabelTable[LabelBandSelfFeedbackTitle].EN, deliveryLabelTable[LabelBandSelfFeedbackTitle].ZH)
		addPair(deliveryLabelTable[LabelBandTopicClusterTitle].EN, deliveryLabelTable[LabelBandTopicClusterTitle].ZH)
		addPair(deliveryLabelTable[LabelBandFloatTitle].EN, deliveryLabelTable[LabelBandFloatTitle].ZH)
		addPair(deliveryLabelTable[LabelBandPortraitPrefix].EN, deliveryLabelTable[LabelBandPortraitPrefix].ZH)
		addPair(" active memory units)", " 条活跃记忆单元)")
		addPair(deliveryLabelTable[LabelBandMemoirPrefix].EN, deliveryLabelTable[LabelBandMemoirPrefix].ZH)
		addPair("】scopes ", "】域 ")
		addPair(" · incarnations ", " 个 · 分身 ")
		addPair("】incarnations ", "】分身 ")
		addPair(" · rows ", " 个 · 断言行 ")
		addPair(" · window ", " · 时间窗 ")
		addPair(deliveryLabelTable[LabelBandUniverseSnapshotPrefix].EN+"container ", deliveryLabelTable[LabelBandUniverseSnapshotPrefix].ZH+"容器 ")
		addPair(deliveryLabelTable[LabelBandUniverseSnapshotPrefix].EN, deliveryLabelTable[LabelBandUniverseSnapshotPrefix].ZH)
		addPair(" · macro ", " · 宏观地址 ")
		addPair(" · members ", " · 成员 ")
		addPair("  [Super-containers]（", "  [上级超容器/super]（")
		addPair("  [Child-containers]（", "  [下级子容器成员/children]（")
		addPair(" members）：", " 个容器成员）：")
		addPair("    * child ", "    * 子容器 ")
		addPair("· Commitment · ", "· 承诺 · ")
		addPair("· Entity · ", "· 实体 · ")
		addPair("· Recurrent · ", "· 常想 · ")
		addPair("· Unresolved · ", "· 未决 · ")
		addPair("unnamed_relations", "关系面未命名")
		addPair(" · co-occur ", " · 共现 ")
		addPair(" rows · anchor #", " 行 · 锚 #")
		addPair("assembled by selection table (table order = priority; reverse chronological)", "按选择表装配（表序 = 优先序；链内时间倒序）")
		addPair(deliveryLabelTable[LabelSectionSubjPredCont].EN, deliveryLabelTable[LabelSectionSubjPredCont].ZH)
		addPair(deliveryLabelTable[LabelSectionSubjPred].EN, deliveryLabelTable[LabelSectionSubjPred].ZH)
		addPair(deliveryLabelTable[LabelSectionUnbound].EN, deliveryLabelTable[LabelSectionUnbound].ZH)
		addPair(deliveryLabelTable[LabelSectionIntransitive].EN, deliveryLabelTable[LabelSectionIntransitive].ZH)
		addPair("; "+deliveryLabelTable[LabelBandTailRule].EN+")", "；链头 = 现状；链内时间倒序）")
		addPair(" ("+deliveryLabelTable[LabelBandTailRule].EN+")", "（链头 = 现状；链内时间倒序）")
		addPair(deliveryLabelTable[LabelBandTailRule].EN, deliveryLabelTable[LabelBandTailRule].ZH)
		addPair("】Subj-Obj ", "】主宾链 ")
		addPair(" · Subj-Pred ", " 条 · 主谓链 ")
		addPair(" · Unbound ", " 条 · 未定 ")
		addPair(" (span ", " 条（覆盖 ")
		addPair(deliveryLabelTable[LabelBandSubjectPrefix].EN, "【记忆带·")
		addPair(deliveryLabelTable[LabelChainOmittedPrefix].EN, deliveryLabelTable[LabelChainOmittedPrefix].ZH)
		addPair(" · "+deliveryLabelTable[LabelChainObjectCoexist].EN, " · "+deliveryLabelTable[LabelChainObjectCoexist].ZH)
		addPair("→"+deliveryLabelTable[LabelChainValueTag].EN+"]", "→值]")
		addPair("→"+deliveryLabelTable[LabelChainValueTag].EN+"〕", "→值〕")
		addPair("〔Subj-Pred·"+deliveryLabelTable[LabelChainValueTag].EN+": ", "〔主谓链·值: ")
		addPair("·"+deliveryLabelTable[LabelChainValueTag].EN+": ", "·值: ")
		addPair("\n…(truncated: total ", "\n…（截断：共 ")
		addPair(" chains / ", " 链 / ")
		addPair(" rows, shown ", " 条，已显示 ")
		addPair(" rows)", " 条）")
		addPair(deliveryLabelTable[LabelTagQuestionInSource].EN, deliveryLabelTable[LabelTagQuestionInSource].ZH)
		addPair("[原句为疑问句 / Question in source]", deliveryLabelTable[LabelTagQuestionInSource].ZH)
		addPair(deliveryLabelTable[LabelSelfFeedbackVerdictPrefix].EN+" prior choice ", "【上轮判定】上一轮选择 ")
		addPair(", score ", "，得分 ")
		addPair(" (correct)", "（判对）")
		addPair(" (incorrect)", "（判错）")
		addPair(" ｜ Q: ", " ｜ 问：")
		addPair(" ｜ Turn: ", " ｜ 轮次：")
		addPair(deliveryLabelTable[LabelSelfFeedbackVerdictPrefix].EN, deliveryLabelTable[LabelSelfFeedbackVerdictPrefix].ZH)
		addPair(deliveryLabelTable[LabelWhisperRealtimePrefix].EN, deliveryLabelTable[LabelWhisperRealtimePrefix].ZH)
		addPair(" ｜ Quote:「", " ｜ 原文:「")
		addPair(" ｜ Follow-up:「", " ｜ 追问续答:「")
		addPair(deliveryLabelTable[LabelFieldAdjacentReply].EN, deliveryLabelTable[LabelFieldAdjacentReply].ZH)
		addPair(" ｜ Speaker(", " ｜ 讲述人(")
		addPair(" [Speaker:", " [讲述人:")
		addPair(" ｜ Evidence:[", " ｜ 证据:[")
		addPair(" ｜ Artifacts:[", " ｜ 证据件:[")
		addPair(" · Related:[", " · 关联:[")
		addPair(" ｜ Same-day prior:[", " ｜ 同日上文:[")
		addPair(" ｜ Warrant:", " ｜ 凭据:")
		addPair(" ｜ Location:", " ｜ 位置:")
		addPair(" ｜ "+deliveryLabelTable[LabelFieldRelativeTime].EN+":", " ｜ 相对时点:")
		addPair(deliveryLabelTable[LabelFieldCodePartPrefix].EN, deliveryLabelTable[LabelFieldCodePartPrefix].ZH)
		addPair(deliveryLabelTable[LabelFieldOtherOccurrences].EN+" ", "另发生 ")
		addPair(" times（", " 次（")
		addPair(deliveryLabelTable[LabelFieldOtherMentions].EN+" ", "另提及 ")
		addPair(" times", " 次")
		addPair("Participants: ", "参与：")
		addPair(" ｜ "+deliveryLabelTable[LabelFieldAnchorAnomaly].EN, " ｜ 锚异常（复核未过）")
		addPair(" "+deliveryLabelTable[LabelTimeSpokenPointSuffix].EN, "（讲述时点）")
		addPair(deliveryLabelTable[LabelTagLoopOpen].EN, "[待兑现]")
		addPair(deliveryLabelTable[LabelTagLoopFulfilled].EN, "[已兑现]")
		addPair(deliveryLabelTable[LabelTagLoopWaived].EN, "[已取消]")
		addPair(deliveryLabelTable[LabelTagLoopBroken].EN, "[已中止]")
		addPair(deliveryLabelTable[LabelHotSessionActive].EN, "[本会话热活]")
		addPair(deliveryLabelTable[LabelHotCrossActive].EN, "[热活]")
		addPair(" · replaced]", " · 已更替]")
		addPair(deliveryLabelTable[LabelGapSubject].EN, "主体未定")
		addPair(deliveryLabelTable[LabelGapObject].EN, "客体未定")
		addPair(deliveryLabelTable[LabelGapHolder].EN, "来源不明")
		addPair(deliveryLabelTable[LabelSubjectAbsent].EN, "（无主体）")
		addPair("·image]", "·图片]")
		addPair("·audio]", "·音频]")
		addPair("·video]", "·视频]")
		addPair("·code]", "·代码]")
		addPair("·document]", "·文档]")
		addPair("·file]", "·文件]")
		addPair("【image · ", "【图片 · ")
		addPair("【audio · ", "【音频 · ")
		addPair("【video · ", "【视频 · ")
		addPair("【code · ", "【代码 · ")
		addPair("【document · ", "【文档 · ")
		addPair("【file · ", "【文件 · ")
		addPair("Token distribution: subject 「", "词元分布：主语「")
		addPair("Token distribution: ", "词元分布：")
		addPair("」; query tokens [", "」；查询词元 [")
		addPair("] appear in 「", "] 出现于「")
		addPair("」's own statements (not verbatim in 「", "」本人陈述行（未逐字出现于「")
		addPair("」's own statements).", "」本人陈述行）。")
		addPair("」; matched tokens [", "」；已逐字命中词元 [")
		addPair("], unmatched tokens [", "]，未逐字命中词元 [")
		addPair(" · span ", " · 覆盖 ")
		addPair("〔Subj-Obj·", "〔主宾链·")
		addPair("〔Subj-Pred·", "〔主谓链·")
		addPair("〔Pred-Obj·", "〔谓宾链·")
		addPair("〔Unbound·", "〔未定·")
		addPair("〔Intransitive·", "〔二元·")
		for _, fk := range []LabelKey{
			LabelFacetWho, LabelFacetWhen, LabelFacetWhere, LabelFacetWhat,
			LabelFacetWhy, LabelFacetHow, LabelFacetResult, LabelFacetObject,
		} {
			addPair("- ["+deliveryLabelTable[fk].EN+"]", "- ["+deliveryLabelTable[fk].ZH+"]")
		}
	}

	out := text
	if len(pairs) > 0 {
		out = strings.NewReplacer(pairs...).Replace(out)
	}
	if dst == DeliveryLangEN {
		out = collapseBilingualSpokenOnToEN(out)
	}
	return out
}

// collapseBilingualSpokenOnToEN compacts "讲述日:YYYY-MM-DD（spoken_on:YYYY-MM-DD → event_time:YYYY-MM-DD）"
// into "spoken_on:YYYY-MM-DD → event_time:YYYY-MM-DD", eliminating duplicate date tokens in English mode.
func collapseBilingualSpokenOnToEN(s string) string {
	const pfx = "讲述日:"
	const mid = "（spoken_on:"
	for {
		idx := strings.Index(s, pfx)
		if idx < 0 {
			break
		}
		rest := s[idx+len(pfx):]
		mIdx := strings.Index(rest, mid)
		if mIdx <= 0 || mIdx > 24 {
			break
		}
		afterMid := rest[mIdx+len(mid):]
		closeIdx := strings.Index(afterMid, "）")
		if closeIdx <= 0 || closeIdx > 64 {
			break
		}
		inner := afterMid[:closeIdx]
		s = s[:idx] + "spoken_on:" + inner + afterMid[closeIdx+len("）"):]
	}
	return s
}
