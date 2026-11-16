// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package shaping

import (
	"math"
	"strings"
)

// 连跑折叠的触发线与保留数均由 tf 增益饱和点导出：词频增益 1+min(1, ln tf)
// 在 tf >= e 处顶格，保留 ceil(e) 份使被折叠词项的增益保持饱和值，折叠对词
// 项打分通道严格中性；触发线 ceil(e^2) 对应退化复读签名下界，低于它的短跑
// （口语强调、合法重述）一律原样保留。
var (
	runFoldTrigger = int(math.Ceil(math.E * math.E))
	runFoldKeep    = int(math.Ceil(math.E))
)

// CollapseDegenerateTokenRuns 折叠病理性的同一 token 行内连跑与同一文本行
// 跨行连跑（多模态识别器的退化复读死循环产物），每段连跑仅当长度超过触发
// 线时折到保留数。判据是整 token / 整行的精确字符串相等：零词表、零字符类、
// 零长度阈值，任意语种与符号系统同构生效；散布重复（非连续）不受影响。
// 仅用于入库索引投影，不改写任何原文存储。
func CollapseDegenerateTokenRuns(text string) string {
	if text == "" {
		return text
	}
	lines := strings.Split(text, "\n")
	changed := false
	for li, line := range lines {
		if folded, ok := foldLineTokenRuns(line); ok {
			lines[li] = folded
			changed = true
		}
	}
	kept := lines
	if folded, ok := foldIdenticalLineRuns(lines); ok {
		kept = folded
		changed = true
	}
	if !changed {
		return text
	}
	return strings.Join(kept, "\n")
}

// foldLineTokenRuns 折叠单行内按空白切分后的同 token 连跑；无触发时报告
// 未变更以便调用方保留原始行（含原始空白排版）。
func foldLineTokenRuns(line string) (string, bool) {
	toks := strings.Fields(line)
	if len(toks) <= runFoldTrigger {
		return line, false
	}
	out := toks[:0]
	changed := false
	i := 0
	for i < len(toks) {
		j := i + 1
		for j < len(toks) && toks[j] == toks[i] {
			j++
		}
		run := j - i
		if run > runFoldTrigger {
			out = append(out, toks[i:i+runFoldKeep]...)
			changed = true
		} else {
			out = append(out, toks[i:j]...)
		}
		i = j
	}
	if !changed {
		return line, false
	}
	return strings.Join(out, " "), true
}

// foldIdenticalLineRuns 折叠去首尾空白后逐字相等的连续行；空白行不参与
// 折叠（保持原有段落结构）。保留段取连跑的前几条原始行。
func foldIdenticalLineRuns(lines []string) ([]string, bool) {
	if len(lines) <= runFoldTrigger {
		return lines, false
	}
	out := make([]string, 0, len(lines))
	changed := false
	i := 0
	for i < len(lines) {
		key := strings.TrimSpace(lines[i])
		j := i + 1
		for key != "" && j < len(lines) && strings.TrimSpace(lines[j]) == key {
			j++
		}
		run := j - i
		if run > runFoldTrigger {
			out = append(out, lines[i:i+runFoldKeep]...)
			changed = true
		} else {
			out = append(out, lines[i:j]...)
		}
		i = j
	}
	if !changed {
		return lines, false
	}
	return out, true
}
