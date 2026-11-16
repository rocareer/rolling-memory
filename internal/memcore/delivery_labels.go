// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"strings"
)

// DeliveryLang specifies the target language for presentation-layer meta-labels
// in memory delivery and LLM context scaffolding.
//
// Scope & Non-Breakage Contract:
//   - Included (62 keys): Only presentation-layer meta-labels visible to reader LLMs in delivery
//     items and context scaffolding (band headers, chain headers, inline provenance keys, temporal
//     prefixes, epistemic/loop/gap tags, artifact kind labels, lexical distribution, 8-facet roles,
//     and whisper/float headers).
//   - Excluded (strictly untouched to prevent LLM output / parser chain breakage):
//     1) Structural line-start multimodal protocol markers ("【图片】", "【音频】", "【视频】", "【文件】")
//     used for byte-offset anchor slicing in ComposeTurnText;
//     2) Encoder write-side prompt contract tokens ("【#1】", "已落图：") coupled to remember tool rules;
//     3) Engine channel sentinels and structural IDs ("[eng:attribution]", "[E#k]", "[A#m]", "[#id]", "@k", "∅");
//     4) Offline human CLI Markdown audit tables.
type DeliveryLang string

const (
	// DeliveryLangEN is the default compact English label set (optimal token efficiency
	// and cross-lingual LLM attention alignment).
	DeliveryLangEN DeliveryLang = "en"
	// DeliveryLangZH is the Chinese label set for Chinese UI, audit, and debugging views.
	DeliveryLangZH DeliveryLang = "zh"
	// DeliveryLangBilingual emits dual-language anchors where applicable.
	DeliveryLangBilingual DeliveryLang = "bilingual"

	// DefaultDeliveryLang is the system-wide default label language ("en").
	DefaultDeliveryLang = DeliveryLangEN
)

// NormalizeDeliveryLang canonicalizes a raw language code into a supported DeliveryLang,
// defaulting to DeliveryLangEN ("en") when empty or unrecognized.
func NormalizeDeliveryLang(lang string) DeliveryLang {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "", "en", "eng", "english", "default":
		return DeliveryLangEN
	case "zh", "cn", "zh-cn", "zh-tw", "zh_cn", "zh_tw", "chinese", "han":
		return DeliveryLangZH
	case "bi", "bilingual", "dual", "en+zh", "zh+en":
		return DeliveryLangBilingual
	default:
		return DefaultDeliveryLang
	}
}

// LabelKey identifies a canonical presentation-layer meta-label in the translation table.
type LabelKey uint8

const (
	// 1. Band & Section Headers (带头与分区标题 · 20 keys)
	LabelBandSubjectPrefix LabelKey = iota
	LabelBandTailRule
	LabelBandPredObjTitle
	LabelBandMiscTitle
	LabelBandEpisodeSnapshotTitle
	LabelBandEvidenceLinesTitle
	LabelBandEvidenceArtifactsTitle
	LabelBandEvidenceCombinedTitle
	LabelSectionEvidenceLines
	LabelSectionEvidenceArtifacts
	LabelBandFallbackRawTitle
	LabelBandSupplementRawTitle
	LabelBandPreciseFallbackPrefix
	LabelBandHeadHitsTitle
	LabelBandSelfFeedbackTitle
	LabelBandTopicClusterTitle
	LabelBandFloatTitle
	LabelBandPortraitPrefix
	LabelBandMemoirPrefix
	LabelBandUniverseSnapshotPrefix

	// 2. Chain & Grammatical Section Tags (链名与语法段标题 · 12 keys)
	LabelChainSubjObj
	LabelChainSubjPred
	LabelChainPredObj
	LabelChainUnbound
	LabelChainIntransitive
	LabelChainValueTag
	LabelChainObjectCoexist
	LabelChainOmittedPrefix
	LabelSectionSubjPred
	LabelSectionSubjPredCont
	LabelSectionUnbound
	LabelSectionIntransitive

	// 3. Inline Field & Provenance Keys (行内字段与溯源指针 · 16 keys)
	LabelFieldSourceQuote
	LabelFieldFollowup
	LabelFieldAdjacentReply
	LabelFieldSpeaker
	LabelFieldEvidencePtr
	LabelFieldArtifactsPtr
	LabelFieldRelatedPtr
	LabelFieldSameDayPrior
	LabelFieldWarrant
	LabelFieldLocation
	LabelFieldRelativeTime
	LabelFieldCodePartPrefix
	LabelFieldParticipants
	LabelFieldOtherOccurrences
	LabelFieldOtherMentions
	LabelFieldAnchorAnomaly

	// 4. Temporal & Span Anchors (时空与跨度锚点 · 5 keys)
	LabelTimeSpokenOn
	LabelTimeEventTime
	LabelTimeSpokenPointSuffix
	LabelTimeUnknown
	LabelTimeCoverPrefix

	// 5. Epistemic Modality, Prospective Loop & Slot Gap Tags (模态、前瞻闭环与未定槽标签 · 14 keys)
	LabelTagQuestionInSource
	LabelTagLoopOpen
	LabelTagLoopFulfilled
	LabelTagLoopWaived
	LabelTagLoopBroken
	LabelGapSubject
	LabelGapObject
	LabelGapHolder
	LabelSubjectAbsent
	LabelHotSessionActive
	LabelHotCrossActive
	LabelStatusReplaced
	LabelSelfFeedbackVerdictPrefix
	LabelWhisperRealtimePrefix

	// 6. Multimodal Artifact Kind Labels (多模态证据件类型签 · 6 keys)
	LabelArtifactImage
	LabelArtifactAudio
	LabelArtifactVideo
	LabelArtifactCode
	LabelArtifactDocument
	LabelArtifactFile

	// 7. Objective Lexical Distribution & Episode Facet Labels (客观词元分布与八面场景角色签 · 9 keys)
	LabelTokenDistribution
	LabelFacetWho
	LabelFacetWhen
	LabelFacetWhere
	LabelFacetWhat
	LabelFacetWhy
	LabelFacetHow
	LabelFacetResult
	LabelFacetObject

	labelKeyCount
)

// LabelEntry holds the multi-language translations for a single LabelKey.
type LabelEntry struct {
	Key       LabelKey `json:"key"`
	Name      string   `json:"name"`
	EN        string   `json:"en"`
	ZH        string   `json:"zh"`
	Bilingual string   `json:"bilingual"`
}

// deliveryLabelTable is the single source of truth for all presentation-layer meta-labels.
// Indexed in O(1) by LabelKey.
var deliveryLabelTable = [labelKeyCount]LabelEntry{
	// 1. Band & Section Headers
	LabelBandSubjectPrefix: {
		Key:       LabelBandSubjectPrefix,
		Name:      "band.subject.prefix",
		EN:        "【Memory Band·",
		ZH:        "【记忆带·",
		Bilingual: "【Memory Band·",
	},
	LabelBandTailRule: {
		Key:       LabelBandTailRule,
		Name:      "band.tail.rule",
		EN:        "head = latest; reverse chronological",
		ZH:        "链头 = 现状；链内时间倒序",
		Bilingual: "head = latest; reverse chronological",
	},
	LabelBandPredObjTitle: {
		Key:       LabelBandPredObjTitle,
		Name:      "band.pred_obj.title",
		EN:        "【Pred-Obj Supplement】Target attribution & provenance (by predicate-object; reverse chronological)",
		ZH:        "【谓宾补充带】客体侧归属与来源（同谓宾视图；链内时间倒序）",
		Bilingual: "【Pred-Obj Supplement / 谓宾补充带】Target attribution & provenance",
	},
	LabelBandMiscTitle: {
		Key:       LabelBandMiscTitle,
		Name:      "band.misc.title",
		EN:        "【Misc Band】Unanchored relevant items (ordered; promoted upon consolidation)",
		ZH:        "【零散带】未锚定入带的相关件（仍有序；可随巩固晋升）",
		Bilingual: "【Misc Band / 零散带】Unanchored relevant items",
	},
	LabelBandEpisodeSnapshotTitle: {
		Key:       LabelBandEpisodeSnapshotTitle,
		Name:      "band.episode_snapshot.title",
		EN:        "【Episode Snapshot · 8-Facet Companion Memory】Co-occurring facts activated from the same episode:",
		ZH:        "【情景快照带·八面伴随记忆】由命中核心记忆激活的同场景伴随事实（尾插补池 · 不占主带头席）：",
		Bilingual: "【Episode Snapshot / 情景快照带·八面伴随记忆】Co-occurring facts activated from the same episode:",
	},
	LabelBandEvidenceLinesTitle: {
		Key:       LabelBandEvidenceLinesTitle,
		Name:      "band.evidence_lines.title",
		EN:        "【Evidence Band · Lines】",
		ZH:        "【证据带·证据行】",
		Bilingual: "【Evidence Band · Lines / 证据带·证据行】",
	},
	LabelBandEvidenceArtifactsTitle: {
		Key:       LabelBandEvidenceArtifactsTitle,
		Name:      "band.evidence_artifacts.title",
		EN:        "【Evidence Band · Artifacts】",
		ZH:        "【证据带·证据件】",
		Bilingual: "【Evidence Band · Artifacts / 证据带·证据件】",
	},
	LabelBandEvidenceCombinedTitle: {
		Key:       LabelBandEvidenceCombinedTitle,
		Name:      "band.evidence_combined.title",
		EN:        "【Evidence Band · Consolidated】",
		ZH:        "【证据带·集中存证】",
		Bilingual: "【Evidence Band · Consolidated / 证据带·集中存证】",
	},
	LabelSectionEvidenceLines: {
		Key:       LabelSectionEvidenceLines,
		Name:      "section.evidence_lines",
		EN:        "【Evidence Lines】",
		ZH:        "【证据行】",
		Bilingual: "【Evidence Lines / 证据行】",
	},
	LabelSectionEvidenceArtifacts: {
		Key:       LabelSectionEvidenceArtifacts,
		Name:      "section.evidence_artifacts",
		EN:        "【Evidence Artifacts】",
		ZH:        "【证据件】",
		Bilingual: "【Evidence Artifacts / 证据件】",
	},
	LabelBandFallbackRawTitle: {
		Key:       LabelBandFallbackRawTitle,
		Name:      "band.fallback_raw.title",
		EN:        "【Raw Hits (Unindexed)】Bounded fallback from raw traces (unindexed; promoted upon replay)",
		ZH:        "【原文命中（未入图）】兜底束：图面稀疏时自原文有界召回（未入图；可随回放晋升）",
		Bilingual: "【Raw Hits (Unindexed) / 原文命中（未入图）】Bounded fallback from raw traces",
	},
	LabelBandSupplementRawTitle: {
		Key:       LabelBandSupplementRawTitle,
		Name:      "band.supplement_raw.title",
		EN:        "【Raw Supplement】Bounded lexical supplement (fallback when unindexed or unselected)",
		ZH:        "【原文·补】金标词常开补充束（有界；原文未入图或未被选中时兜底）",
		Bilingual: "【Raw Supplement / 原文·补】Bounded lexical supplement",
	},
	LabelBandPreciseFallbackPrefix: {
		Key:       LabelBandPreciseFallbackPrefix,
		Name:      "band.precise_fallback.prefix",
		EN:        "【Raw Fallback】",
		ZH:        "【原文·兜底】",
		Bilingual: "【Raw Fallback / 原文·兜底】",
	},
	LabelBandHeadHitsTitle: {
		Key:       LabelBandHeadHitsTitle,
		Name:      "band.head_hits.title",
		EN:        "【Query Hits】",
		ZH:        "【题面命中】",
		Bilingual: "【Query Hits / 题面命中】",
	},
	LabelBandSelfFeedbackTitle: {
		Key:       LabelBandSelfFeedbackTitle,
		Name:      "band.self_feedback.title",
		EN:        "【Self-Answer History · Feedback】Same-question history in scope (verdict / query / turn · verbatim):",
		ZH:        "【自答历史·回灌】本域同题历史（判决行 / 问 / 轮次 · 逐字）：",
		Bilingual: "【Self-Answer History / 自答历史·回灌】Same-question history in scope:",
	},
	LabelBandTopicClusterTitle: {
		Key:       LabelBandTopicClusterTitle,
		Name:      "band.topic_cluster.title",
		EN:        "【Topic Overview · Co-occurrence Projection】Subjects co-occurring with hit seeds (ordered by strength; #id retrieves verbatim text):",
		ZH:        "【话题概览·共现投影】以本次命中种子为中心，同断言共现的主体（按强度降序；锚 #id 可精确取回原文）：",
		Bilingual: "【Topic Overview / 话题概览·共现投影】Subjects co-occurring with hit seeds:",
	},
	LabelBandFloatTitle: {
		Key:       LabelBandFloatTitle,
		Name:      "band.float.title",
		EN:        "【Subconscious Float】Background hints (may not be directly related to current query)",
		ZH:        "【浮现】潜意识提示（未必与当前问题相关）",
		Bilingual: "【Subconscious Float / 浮现】Background hints",
	},
	LabelBandPortraitPrefix: {
		Key:       LabelBandPortraitPrefix,
		Name:      "band.portrait.prefix",
		EN:        "【Portrait·",
		ZH:        "【画像·",
		Bilingual: "【Portrait/画像·",
	},
	LabelBandMemoirPrefix: {
		Key:       LabelBandMemoirPrefix,
		Name:      "band.memoir.prefix",
		EN:        "【Memoir·",
		ZH:        "【正传·",
		Bilingual: "【Memoir/正传·",
	},
	LabelBandUniverseSnapshotPrefix: {
		Key:       LabelBandUniverseSnapshotPrefix,
		Name:      "band.universe_snapshot.prefix",
		EN:        "【Memory Universe · 8-Facet Episode Snapshot】",
		ZH:        "【记忆宇宙·八面场景快照】",
		Bilingual: "【Memory Universe / 记忆宇宙·八面场景快照】",
	},

	// 2. Chain & Grammatical Section Tags
	LabelChainSubjObj: {
		Key:       LabelChainSubjObj,
		Name:      "chain.subj_obj",
		EN:        "Subj-Obj",
		ZH:        "主宾链",
		Bilingual: "Subj-Obj/主宾链",
	},
	LabelChainSubjPred: {
		Key:       LabelChainSubjPred,
		Name:      "chain.subj_pred",
		EN:        "Subj-Pred",
		ZH:        "主谓链",
		Bilingual: "Subj-Pred/主谓链",
	},
	LabelChainPredObj: {
		Key:       LabelChainPredObj,
		Name:      "chain.pred_obj",
		EN:        "Pred-Obj",
		ZH:        "谓宾链",
		Bilingual: "Pred-Obj/谓宾链",
	},
	LabelChainUnbound: {
		Key:       LabelChainUnbound,
		Name:      "chain.unbound",
		EN:        "Unbound",
		ZH:        "未定",
		Bilingual: "Unbound/未定",
	},
	LabelChainIntransitive: {
		Key:       LabelChainIntransitive,
		Name:      "chain.intransitive",
		EN:        "Intransitive",
		ZH:        "二元",
		Bilingual: "Intransitive/二元",
	},
	LabelChainValueTag: {
		Key:       LabelChainValueTag,
		Name:      "chain.value_tag",
		EN:        "val",
		ZH:        "值",
		Bilingual: "val/值",
	},
	LabelChainObjectCoexist: {
		Key:       LabelChainObjectCoexist,
		Name:      "chain.object_coexist",
		EN:        "coexisting_objects×",
		ZH:        "客体并存×",
		Bilingual: "coexisting_objects/客体并存×",
	},
	LabelChainOmittedPrefix: {
		Key:       LabelChainOmittedPrefix,
		Name:      "chain.omitted.prefix",
		EN:        "\n〔Omitted chains: ",
		ZH:        "\n〔未显示链名：",
		Bilingual: "\n〔Omitted chains / 未显示链名：",
	},
	LabelSectionSubjPred: {
		Key:       LabelSectionSubjPred,
		Name:      "section.subj_pred",
		EN:        "\n【Subj-Pred · State Evolution】",
		ZH:        "\n【主谓链·状态演进】",
		Bilingual: "\n【Subj-Pred · State Evolution / 主谓链·状态演进】",
	},
	LabelSectionSubjPredCont: {
		Key:       LabelSectionSubjPredCont,
		Name:      "section.subj_pred_cont",
		EN:        "\n【Subj-Pred · State Evolution (cont.)】",
		ZH:        "\n【主谓链·状态演进（续）】",
		Bilingual: "\n【Subj-Pred · State Evolution (cont.) / 主谓链·状态演进（续）】",
	},
	LabelSectionUnbound: {
		Key:       LabelSectionUnbound,
		Name:      "section.unbound",
		EN:        "\n【Unbound Object Rows】",
		ZH:        "\n【未定行】（客体未定：原文未给 / 指代解不开；后续观察可补全）",
		Bilingual: "\n【Unbound Object Rows / 未定行】",
	},
	LabelSectionIntransitive: {
		Key:       LabelSectionIntransitive,
		Name:      "section.intransitive",
		EN:        "\n【Intransitive Rows】",
		ZH:        "\n【二元行】（客体不适用：谓词无客体）",
		Bilingual: "\n【二元行】（Intransitive predicate rows）",
	},

	// 3. Inline Field & Provenance Keys
	LabelFieldSourceQuote: {
		Key:       LabelFieldSourceQuote,
		Name:      "field.source_quote",
		EN:        "Quote",
		ZH:        "原文",
		Bilingual: "Quote/原文",
	},
	LabelFieldFollowup: {
		Key:       LabelFieldFollowup,
		Name:      "field.followup",
		EN:        "Follow-up",
		ZH:        "追问续答",
		Bilingual: "Follow-up/追问续答",
	},
	LabelFieldAdjacentReply: {
		Key:       LabelFieldAdjacentReply,
		Name:      "field.adjacent_reply",
		EN:        "↳ Next-turn reply: ",
		ZH:        "↳ 紧邻回复: ",
		Bilingual: "↳ Next-turn reply / 紧邻回复: ",
	},
	LabelFieldSpeaker: {
		Key:       LabelFieldSpeaker,
		Name:      "field.speaker",
		EN:        "Speaker",
		ZH:        "讲述人",
		Bilingual: "Speaker/讲述人",
	},
	LabelFieldEvidencePtr: {
		Key:       LabelFieldEvidencePtr,
		Name:      "field.evidence_ptr",
		EN:        "Evidence",
		ZH:        "证据",
		Bilingual: "Evidence/证据",
	},
	LabelFieldArtifactsPtr: {
		Key:       LabelFieldArtifactsPtr,
		Name:      "field.artifacts_ptr",
		EN:        "Artifacts",
		ZH:        "证据件",
		Bilingual: "Artifacts/证据件",
	},
	LabelFieldRelatedPtr: {
		Key:       LabelFieldRelatedPtr,
		Name:      "field.related_ptr",
		EN:        "Related",
		ZH:        "关联",
		Bilingual: "Related/关联",
	},
	LabelFieldSameDayPrior: {
		Key:       LabelFieldSameDayPrior,
		Name:      "field.same_day_prior",
		EN:        "Same-day prior",
		ZH:        "同日上文",
		Bilingual: "Same-day prior/同日上文",
	},
	LabelFieldWarrant: {
		Key:       LabelFieldWarrant,
		Name:      "field.warrant",
		EN:        "Warrant",
		ZH:        "凭据",
		Bilingual: "Warrant/凭据",
	},
	LabelFieldLocation: {
		Key:       LabelFieldLocation,
		Name:      "field.location",
		EN:        "Location",
		ZH:        "位置",
		Bilingual: "Location/位置",
	},
	LabelFieldRelativeTime: {
		Key:       LabelFieldRelativeTime,
		Name:      "field.relative_time",
		EN:        "rel_time",
		ZH:        "相对时点",
		Bilingual: "rel_time/相对时点",
	},
	LabelFieldCodePartPrefix: {
		Key:       LabelFieldCodePartPrefix,
		Name:      "field.code_part.prefix",
		EN:        "[Code:",
		ZH:        "[代码件:",
		Bilingual: "[Code/代码件:",
	},
	LabelFieldParticipants: {
		Key:       LabelFieldParticipants,
		Name:      "field.participants",
		EN:        "Participants",
		ZH:        "参与",
		Bilingual: "Participants/参与",
	},
	LabelFieldOtherOccurrences: {
		Key:       LabelFieldOtherOccurrences,
		Name:      "field.other_occurrences",
		EN:        "Also occurred",
		ZH:        "另发生",
		Bilingual: "Also occurred/另发生",
	},
	LabelFieldOtherMentions: {
		Key:       LabelFieldOtherMentions,
		Name:      "field.other_mentions",
		EN:        "Also mentioned",
		ZH:        "另提及",
		Bilingual: "Also mentioned/另提及",
	},
	LabelFieldAnchorAnomaly: {
		Key:       LabelFieldAnchorAnomaly,
		Name:      "field.anchor_anomaly",
		EN:        "Anchor anomaly (verification failed)",
		ZH:        "锚异常（复核未过）",
		Bilingual: "Anchor anomaly / 锚异常（复核未过）",
	},

	// 4. Temporal & Span Anchors
	LabelTimeSpokenOn: {
		Key:       LabelTimeSpokenOn,
		Name:      "time.spoken_on",
		EN:        "spoken_on",
		ZH:        "讲述日",
		Bilingual: "讲述日/spoken_on",
	},
	LabelTimeEventTime: {
		Key:       LabelTimeEventTime,
		Name:      "time.event_time",
		EN:        "event_time",
		ZH:        "事件时点",
		Bilingual: "事件时点/event_time",
	},
	LabelTimeSpokenPointSuffix: {
		Key:       LabelTimeSpokenPointSuffix,
		Name:      "time.spoken_point_suffix",
		EN:        "(spoken_on)",
		ZH:        "（讲述时点）",
		Bilingual: "（讲述时点/spoken_on）",
	},
	LabelTimeUnknown: {
		Key:       LabelTimeUnknown,
		Name:      "time.unknown",
		EN:        "time_unknown",
		ZH:        "时间未知",
		Bilingual: "time_unknown/时间未知",
	},
	LabelTimeCoverPrefix: {
		Key:       LabelTimeCoverPrefix,
		Name:      "time.cover_prefix",
		EN:        "span",
		ZH:        "覆盖",
		Bilingual: "span/覆盖",
	},

	// 5. Epistemic Modality, Prospective Loop & Slot Gap Tags
	LabelTagQuestionInSource: {
		Key:       LabelTagQuestionInSource,
		Name:      "tag.question_in_source",
		EN:        "[Question in source]",
		ZH:        "[原句为疑问句]",
		Bilingual: "[原句为疑问句 / Question in source]",
	},
	LabelTagLoopOpen: {
		Key:       LabelTagLoopOpen,
		Name:      "tag.loop_open",
		EN:        "[Pending]",
		ZH:        "[待兑现]",
		Bilingual: "[Pending / 待兑现]",
	},
	LabelTagLoopFulfilled: {
		Key:       LabelTagLoopFulfilled,
		Name:      "tag.loop_fulfilled",
		EN:        "[Fulfilled]",
		ZH:        "[已兑现]",
		Bilingual: "[Fulfilled / 已兑现]",
	},
	LabelTagLoopWaived: {
		Key:       LabelTagLoopWaived,
		Name:      "tag.loop_waived",
		EN:        "[Waived]",
		ZH:        "[已取消]",
		Bilingual: "[Waived / 已取消]",
	},
	LabelTagLoopBroken: {
		Key:       LabelTagLoopBroken,
		Name:      "tag.loop_broken",
		EN:        "[Aborted]",
		ZH:        "[已中止]",
		Bilingual: "[Aborted / 已中止]",
	},
	LabelGapSubject: {
		Key:       LabelGapSubject,
		Name:      "gap.subject",
		EN:        "subject_unbound",
		ZH:        "主体未定",
		Bilingual: "subject_unbound/主体未定",
	},
	LabelGapObject: {
		Key:       LabelGapObject,
		Name:      "gap.object",
		EN:        "object_unbound",
		ZH:        "客体未定",
		Bilingual: "object_unbound/客体未定",
	},
	LabelGapHolder: {
		Key:       LabelGapHolder,
		Name:      "gap.holder",
		EN:        "source_unattributed",
		ZH:        "来源不明",
		Bilingual: "source_unattributed/来源不明",
	},
	LabelSubjectAbsent: {
		Key:       LabelSubjectAbsent,
		Name:      "gap.subject_absent",
		EN:        "(subjectless)",
		ZH:        "（无主体）",
		Bilingual: "(subjectless/无主体)",
	},
	LabelHotSessionActive: {
		Key:       LabelHotSessionActive,
		Name:      "hot.session_active",
		EN:        "[Session Hot]",
		ZH:        "[本会话热活]",
		Bilingual: "[Session Hot / 本会话热活]",
	},
	LabelHotCrossActive: {
		Key:       LabelHotCrossActive,
		Name:      "hot.cross_active",
		EN:        "[Hot]",
		ZH:        "[热活]",
		Bilingual: "[Hot / 热活]",
	},
	LabelStatusReplaced: {
		Key:       LabelStatusReplaced,
		Name:      "status.replaced",
		EN:        "replaced",
		ZH:        "已更替",
		Bilingual: "replaced/已更替",
	},
	LabelSelfFeedbackVerdictPrefix: {
		Key:       LabelSelfFeedbackVerdictPrefix,
		Name:      "feedback.verdict.prefix",
		EN:        "【Prior Verdict】",
		ZH:        "【上轮判定】",
		Bilingual: "【Prior Verdict / 上轮判定】",
	},
	LabelWhisperRealtimePrefix: {
		Key:       LabelWhisperRealtimePrefix,
		Name:      "whisper.realtime.prefix",
		EN:        "[Memory Whisper]",
		ZH:        "[记忆脑实时耳语]",
		Bilingual: "[Memory Whisper / 记忆脑实时耳语]",
	},

	// 6. Multimodal Artifact Kind Labels
	LabelArtifactImage: {
		Key:       LabelArtifactImage,
		Name:      "artifact.image",
		EN:        "image",
		ZH:        "图片",
		Bilingual: "image/图片",
	},
	LabelArtifactAudio: {
		Key:       LabelArtifactAudio,
		Name:      "artifact.audio",
		EN:        "audio",
		ZH:        "音频",
		Bilingual: "audio/音频",
	},
	LabelArtifactVideo: {
		Key:       LabelArtifactVideo,
		Name:      "artifact.video",
		EN:        "video",
		ZH:        "视频",
		Bilingual: "video/视频",
	},
	LabelArtifactCode: {
		Key:       LabelArtifactCode,
		Name:      "artifact.code",
		EN:        "code",
		ZH:        "代码",
		Bilingual: "code/代码",
	},
	LabelArtifactDocument: {
		Key:       LabelArtifactDocument,
		Name:      "artifact.document",
		EN:        "document",
		ZH:        "文档",
		Bilingual: "document/文档",
	},
	LabelArtifactFile: {
		Key:       LabelArtifactFile,
		Name:      "artifact.file",
		EN:        "file",
		ZH:        "文件",
		Bilingual: "file/文件",
	},

	// 7. Objective Lexical Distribution & Episode Facet Labels
	LabelTokenDistribution: {
		Key:       LabelTokenDistribution,
		Name:      "dist.token_distribution",
		EN:        "Token distribution",
		ZH:        "词元分布",
		Bilingual: "Token distribution / 词元分布",
	},
	LabelFacetWho: {
		Key:       LabelFacetWho,
		Name:      "facet.who",
		EN:        "who",
		ZH:        "何人",
		Bilingual: "who/何人",
	},
	LabelFacetWhen: {
		Key:       LabelFacetWhen,
		Name:      "facet.when",
		EN:        "when",
		ZH:        "何时",
		Bilingual: "when/何时",
	},
	LabelFacetWhere: {
		Key:       LabelFacetWhere,
		Name:      "facet.where",
		EN:        "where",
		ZH:        "何处",
		Bilingual: "where/何处",
	},
	LabelFacetWhat: {
		Key:       LabelFacetWhat,
		Name:      "facet.what",
		EN:        "what",
		ZH:        "何事",
		Bilingual: "what/何事",
	},
	LabelFacetWhy: {
		Key:       LabelFacetWhy,
		Name:      "facet.why",
		EN:        "why",
		ZH:        "何因",
		Bilingual: "why/何因",
	},
	LabelFacetHow: {
		Key:       LabelFacetHow,
		Name:      "facet.how",
		EN:        "how",
		ZH:        "何法",
		Bilingual: "how/何法",
	},
	LabelFacetResult: {
		Key:       LabelFacetResult,
		Name:      "facet.result",
		EN:        "outcome",
		ZH:        "何果",
		Bilingual: "outcome/何果",
	},
	LabelFacetObject: {
		Key:       LabelFacetObject,
		Name:      "facet.object",
		EN:        "object",
		ZH:        "何物",
		Bilingual: "object/何物",
	},
}

// Label returns the default English (DeliveryLangEN) string for key.
func Label(key LabelKey) string {
	return TranslateLabel(DefaultDeliveryLang, key)
}

// LabelFor returns the localized label for raw lang ("en" default, "zh", or "bilingual").
func LabelFor(lang string, key LabelKey) string {
	return TranslateLabel(NormalizeDeliveryLang(lang), key)
}

// TranslateLabel looks up key in O(1) for the normalized DeliveryLang.
func TranslateLabel(lang DeliveryLang, key LabelKey) string {
	if key >= labelKeyCount {
		return ""
	}
	entry := deliveryLabelTable[key]
	switch lang {
	case DeliveryLangZH:
		return entry.ZH
	case DeliveryLangBilingual:
		if entry.Bilingual != "" {
			return entry.Bilingual
		}
		return entry.EN
	default:
		return entry.EN
	}
}

// AllLabelEntries returns a copy of the complete translation catalog.
func AllLabelEntries() []LabelEntry {
	out := make([]LabelEntry, labelKeyCount)
	copy(out, deliveryLabelTable[:])
	return out
}
