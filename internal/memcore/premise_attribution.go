// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"strings"
)

// ==================== 引擎通道键（机读）+ 语料哨兵转义（协议冻结 2026-10-31） ====================
//
// 交付契约：交付文本中**一切由引擎生成、要求读者执行动作的文本行**必须以 `[eng:<key>]` 机读键开头，
// 下游可据此确定性识别 / 剥离引擎指令面；语料派生文本中的同形字面量在渲染叶统一转义为 `\[eng:`
// （形式层转义，与 JSON 转义同族：仅当语料真的含保留哨兵时才改变字节），使「引擎标注」与
// 「用户 / 语料内容」在通道上不可混淆（防伪造）。
const (
	// EngineChannelKeyAttribution 归属与前提核对通道键（当前唯一在册的引擎指令通道）。
	EngineChannelKeyAttribution = "[eng:attribution]"
	// engineChannelSentinel 保留哨兵（转义对象；非指令通道自身）。
	engineChannelSentinel = "[eng:"
)

// EscapeEngineChannel 转义语料侧保留哨兵：`[eng:` → `\[eng:`。
// 幂等：扫描时成对跳过 `\X`（前置反斜杠者视为已转义，原样保留）。
func EscapeEngineChannel(text string) string {
	if !strings.Contains(text, engineChannelSentinel) {
		return text
	}
	var b strings.Builder
	b.Grow(len(text) + 8)
	for i := 0; i < len(text); {
		if text[i] == '\\' && i+1 < len(text) {
			b.WriteByte(text[i])
			b.WriteByte(text[i+1])
			i += 2
			continue
		}
		if strings.HasPrefix(text[i:], engineChannelSentinel) {
			b.WriteString(`\`)
			b.WriteString(engineChannelSentinel)
			i += len(engineChannelSentinel)
			continue
		}
		b.WriteByte(text[i])
		i++
	}
	return b.String()
}

// engineChannelLine 引擎通道行唯一合法形态：`  · [eng:<key>] <陈述式正文>`。
func engineChannelLine(body string) string {
	return "  · " + EngineChannelKeyAttribution + " " + body
}

// SealDeliveryChannels 交付收口通道封印（单漏斗 · 幂等 · 每次交付只调用一次）：
// 逐行判定——行首（允许前导空白与 `· `）恰为在册通道键的行判为**引擎行**（保留）；其余行内一切
// `[eng:` 转义为 `\[eng:`（语料侧伪造面闭合：任何非引擎位置出现保留哨兵都被中和为字面量）。
func SealDeliveryChannels(text string) string {
	if !strings.Contains(text, engineChannelSentinel) {
		return text
	}
	lines := strings.Split(text, "\n")
	for i, ln := range lines {
		if strings.HasPrefix(strings.TrimLeft(ln, " \t·"), EngineChannelKeyAttribution) {
			continue
		}
		if strings.Contains(ln, engineChannelSentinel) {
			lines[i] = EscapeEngineChannel(ln)
		}
	}
	return strings.Join(lines, "\n")
}

// BandConceptProfile summarizes the own-speaker declarative query-concept coverage of a subject band.
type BandConceptProfile struct {
	SubjectLabel string
	IsCueMatched bool
	RowCount     int
	DeclHits     map[string]bool
	MaxRowHits   int
	MultiHitRows int
}

// ComputeBandPremiseAttributionNote computes a pure graph-topology cross-subject and premise verification
// note for the primary queried subject band (bandIdx == 0) using 0 LLM and 0 semantic wordlists.
//
// 输出形态：`  · [eng:attribution] <陈述式结构元数据>`（归属事实 / 词元分布事实），在所有读面（含答题面与人类读面）
// 恒为 100% 纯客观图谱结构事实陈述，严禁包含任何指令口吻（如「核对要求」「须」「不得」「请务必」「切勿」「如何作答」）。
//
// Mathematical criteria:
//  1. Cross-Subject Premise Swap: an unqueried protagonist band B_k (k >= 1, RowCount >= 5) holds declarative
//     query concepts that are absent from the queried subject band B_0's own declarative assertions
//     (|DeclHits(B_k) \ DeclHits(B_0)| >= 1), or has a strictly higher single-row declarative concept co-occurrence
//     (MaxRowHits(B_k) > MaxRowHits(B_0) with |DeclHits(B_k)| >= 2), or more multi-concept declarative assertions
//     (MultiHitRows(B_k) > MultiHitRows(B_0) && MaxRowHits(B_k) >= MaxRowHits(B_0) && |DeclHits(B_k)| >= 2).
//  2. Single-Subject Premise Deficit: at least one independent non-subject query concept is completely absent
//     from B_0's own declarative assertions (|NonSubjCues \ DeclHits(B_0)| >= 1 when |NonSubjCues| >= 2).
func ComputeBandPremiseAttributionNote(nonSubjCues []string, profiles []BandConceptProfile, withRequirement bool) string {
	if len(nonSubjCues) == 0 || len(profiles) == 0 {
		return ""
	}
	prim := profiles[0]
	if !prim.IsCueMatched || prim.RowCount < 5 || prim.DeclHits == nil {
		return ""
	}
	// Collapse overlapping sub-tokens so only independent query concepts are evaluated.
	// First check against all hit tokens in prim.DeclHits so UTF-8 sort order of nonSubjCues
	// never strands a sliding 2-gram seam token (e.g. "虑想" between hit "焦虑" and hit "清楚").
	var indepCues []string
	for _, tok := range nonSubjCues {
		if tok == "" {
			continue
		}
		if !prim.DeclHits[tok] {
			hitOverlap := false
			for hitTok, ok := range prim.DeclHits {
				if !ok || hitTok == "" {
					continue
				}
				if (len(hitTok) > len(tok) && strings.Contains(hitTok, tok)) ||
					(len(tok) > len(hitTok) && strings.Contains(tok, hitTok)) ||
					SharesEdgeIdeograph(hitTok, tok) {
					hitOverlap = true
					break
				}
			}
			if hitOverlap {
				continue
			}
		}
		overlaps := false
		for idx, prev := range indepCues {
			if (len(prev) > len(tok) && strings.Contains(prev, tok)) ||
				(len(tok) > len(prev) && strings.Contains(tok, prev)) ||
				SharesEdgeIdeograph(prev, tok) {
				overlaps = true
				if prim.DeclHits[tok] && !prim.DeclHits[prev] {
					indepCues[idx] = tok
				}
				break
			}
		}
		if !overlaps {
			indepCues = append(indepCues, tok)
		}
	}
	if len(indepCues) == 0 {
		return ""
	}

	var missingFromPrim []string
	for _, tok := range indepCues {
		if !prim.DeclHits[tok] {
			missingFromPrim = append(missingFromPrim, tok)
		}
	}

	bestOtherIdx := -1
	var bestOtherExclusive []string
	bestOtherTotal := 0
	for i := 1; i < len(profiles); i++ {
		p := profiles[i]
		if p.IsCueMatched || p.RowCount < 5 || len(p.DeclHits) == 0 {
			continue
		}
		var excl []string
		total := 0
		for _, tok := range indepCues {
			if p.DeclHits[tok] {
				total++
				if !prim.DeclHits[tok] {
					excl = append(excl, tok)
				}
			}
		}
		qualifies := len(excl) >= 1 ||
			(p.MaxRowHits > prim.MaxRowHits && total >= 2) ||
			(p.MultiHitRows > prim.MultiHitRows && p.MaxRowHits >= prim.MaxRowHits && total >= 2)
		if qualifies && (len(excl) > len(bestOtherExclusive) || (len(excl) == len(bestOtherExclusive) && total > bestOtherTotal)) {
			bestOtherIdx = i
			bestOtherExclusive = excl
			bestOtherTotal = total
		}
	}

	if bestOtherIdx >= 0 {
		otherSubj := profiles[bestOtherIdx].SubjectLabel
		tokList := bestOtherExclusive
		if len(tokList) == 0 {
			for _, tok := range indepCues {
				if profiles[bestOtherIdx].DeclHits[tok] {
					tokList = append(tokList, tok)
				}
			}
		}
		if len(tokList) > 5 {
			tokList = tokList[:5]
		}
		tokJoined := strings.Join(tokList, ", ")
		_ = withRequirement
		return engineChannelLine("词元分布：主语「" + prim.SubjectLabel + "」；查询词元 [" + tokJoined +
			"] 出现于「" + otherSubj + "」本人陈述行（未逐字出现于「" + prim.SubjectLabel + "」本人陈述行）。")
	}

	// Single-subject lexical distribution note (pure Shannon self-information bit-ratio, zero token-count magic numbers):
	//  - Compute total independent query concept bits (totalBits) and matched concept bits (hitBits);
	//  - When the primary subject band already covers >= 72% of total query Shannon bits with strong single-turn
	//    co-occurrence ratio (>= 0.45) across multiple turns, the remaining < 28% bits belong to cross-turn
	//    aggregation phrasing (e.g. "先后, 两步") and emit NO note;
	//  - Otherwise, when matched bits >= 2*AtomicConceptEntropyBits and missing bits >= AtomicConceptEntropyBits,
	//    emit a purely objective lexical distribution fact listing the matched and unmatched query concepts.
	var totalBits, hitBits float64
	for _, tok := range indepCues {
		b := StringShannonBits(tok)
		totalBits += b
		if prim.DeclHits[tok] {
			hitBits += b
		}
	}
	missBits := totalBits - hitBits
	hitRatio := 0.0
	if totalBits > 0 {
		hitRatio = hitBits / totalBits
	}
	rowBitRatio := float64(prim.MaxRowHits) / float64(len(indepCues))
	if hitRatio >= 0.65 && rowBitRatio >= 0.50 && prim.MultiHitRows >= 2 {
		return ""
	}
	if hitRatio >= 0.72 && rowBitRatio >= 0.50 && prim.MaxRowHits >= 2 && len(missingFromPrim) == 1 {
		hasDigit := false
		for _, ch := range missingFromPrim[0] {
			if ch >= '0' && ch <= '9' {
				hasDigit = true
				break
			}
		}
		if !hasDigit {
			return ""
		}
	}
	if prim.MaxRowHits >= 2 && hitRatio >= 0.25 && missBits >= AtomicConceptEntropyBits && rowBitRatio < 1.0 {
		var hitList []string
		for _, tok := range indepCues {
			if prim.DeclHits[tok] {
				hitList = append(hitList, tok)
				if len(hitList) >= 4 {
					break
				}
			}
		}
		missList := missingFromPrim
		if len(missList) > 8 {
			missList = missList[:8]
		}
		return engineChannelLine("词元分布：主语「" + prim.SubjectLabel + "」；已逐字命中词元 [" + strings.Join(hitList, ", ") +
			"]，未逐字命中词元 [" + strings.Join(missList, ", ") + "]。")
	}
	return ""
}
