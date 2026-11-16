// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package recall

import (
	"regexp"
	"strings"

	"github.com/rocareer/rolling-memory/internal/store"
)

// 检索范围锚（cue 首部 `[<工作区>]`）——「问当前工作区、召回其他工作区」的端侧消歧。
//
// 锚只在**本机侧**被消费：盲态引擎看不到任何字面标记（脉冲已数字化），
// 因此锚不是给云端看的指令，而是交付装配时的候选排序偏好：
// 命中锚（= 本地明文金库 thin_vault_docs.locus，入库时按仓库目录名打标）的候选
// 稳定置前、其它候选原序压后，**不删除任何候选**（fail-open）。
// 未写锚、锚无人命中、或全部命中时，候选序与不带锚完全一致（既有金标零影响）。
var locusAnchorPattern = regexp.MustCompile(`^\s*\[([^\[\]\r\n]{1,64})\]\s*`)

// locusAnchor 提取 cue 首部的工作区锚；未写锚返回空串。
func locusAnchor(cue string) string {
	match := locusAnchorPattern.FindStringSubmatch(cue)
	if match == nil {
		return ""
	}
	return strings.TrimSpace(match[1])
}

// orderCandidatesByLocusAnchor 把命中锚的工作区候选稳定置前，其余候选原序压后。
func orderCandidatesByLocusAnchor(candidates []store.Candidate, cue string) []store.Candidate {
	want := locusAnchor(cue)
	if want == "" || len(candidates) < 2 {
		return candidates
	}
	same := make([]store.Candidate, 0, len(candidates))
	other := make([]store.Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		locus := ""
		if candidate.Data.Locus != nil {
			locus = strings.TrimSpace(*candidate.Data.Locus)
		}
		if strings.EqualFold(locus, want) {
			same = append(same, candidate)
		} else {
			other = append(other, candidate)
		}
	}
	if len(same) == 0 || len(other) == 0 {
		return candidates
	}
	return append(same, other...)
}

// promoteBandsByLocusAnchor 再次在交付面保证「带锚者排最前」。
//
// 候选级分区决定选材与分组，但 band 的收集器（主体带 / 关系对带 / 杂项带）各有
// 固定优先级，跨收集器仍可能把命中锚的 band 压在后面；本函数按「band 内是否含
// 命中锚的条目」做一次稳定分区，使交付顺序对带锚者确定。band 模式有效；条带
// 形态的条目（item 模式）节点不带工作区字段，自动退化为不改动（fail-open）。
func promoteBandsByLocusAnchor(result *Result, cue string) {
	if result == nil {
		return
	}
	want := locusAnchor(cue)
	if want == "" || len(result.Bands) < 2 {
		return
	}
	same := make([]map[string]any, 0, len(result.Bands))
	other := make([]map[string]any, 0, len(result.Bands))
	for _, band := range result.Bands {
		nodes, _ := band["nodes"].([]map[string]any)
		hit := false
		for _, node := range nodes {
			if strings.EqualFold(strings.TrimSpace(asString(node["locus"])), want) {
				hit = true
				break
			}
		}
		if hit {
			same = append(same, band)
		} else {
			other = append(other, band)
		}
	}
	if len(same) == 0 || len(other) == 0 {
		return
	}
	result.Bands = append(same, other...)
	if result.Meta != nil {
		result.Meta["locus_anchor"] = want
	}
}

// LocusAnchor 提取 cue 首部的工作区锚（`[<workspace>]`，未写锚返回空串）。导出给薄客户端
// 交付管线（internal/api thinClientRecall）共用同一提取口径，两种管线的锚语义只有一个正本。
func LocusAnchor(cue string) string { return locusAnchor(cue) }

// PromoteBandsByLocusAnchor 交付面按锚稳定分区（band 内任一节点 locus 命中锚者置前），
// 导出给薄客户端交付管线共用同一排序口径；fail-open（任一侧为空则不改动）。
func PromoteBandsByLocusAnchor(result *Result, cue string) { promoteBandsByLocusAnchor(result, cue) }
