// plugin: kb v1.0.0
// semantic 策略：无显式结构长文（表：window=3、分位 threshold、min=128、max=1024；**缺省关**，C4）。
//
// 判据：句向量化后按「相邻句窗」余弦相似度求**分位阈值**，低于阈值的边界切分；再按 min/max 归并/切分。
// 模型参与 = 现役嵌入（句向量批量经注入的 Embedder 一次送出；生产注入体 = ai.RunEmbeddings，
// Source="kb"、agent=kb-embed）。
//
// 纪律（C4）：缺省关（配置未显式选 semantic 就不会走到本策略）；启用须在库级配置写 strategy=semantic；
// **嵌入 agent 身份必须入 build（StrategyVersion(name,"kb-embed@<version>")）**——换 agent/换版本 ⇒ 换 build。
// 读口径说明：句向量来自上游模型，非位级确定（先例：kb-embed 确定性用例阈值 min_cos=0.995）⇒
// 重跑可能微调边界；幂等靠 chunk 层 uk(document_id,build_hash,seq) + text_hash 承担（同 build 重跑
// 边界漂移会以唯一键冲突暴露，由管道按失败上报，不静默覆盖）。
package chunk

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"unicode"
)

// semanticChunker semantic 策略
type semanticChunker struct{}

// Name 策略名（注册表键）
func (semanticChunker) Name() string { return "semantic" }

// Version 契约版本（口径升级 ⇒ 升版本 + 重跑派生投影；嵌入 agent 身份经 StrategyVersion 并入）
func (semanticChunker) Version() string { return "v1" }

// Chunk 句窗分位切分（缺 Embedder / 零文本 / 向量条数不符 ⇒ 响亮报错）
func (semanticChunker) Chunk(ctx context.Context, in Input) ([]Chunk, error) {
	rs := []rune(in.Text)
	if isBlankRunes(rs) {
		return nil, errors.New("kb semantic 切片零文本（空/全空白；零文本禁静默入库）")
	}
	max := in.Config.Max
	if max <= 0 {
		max = defaultMax
	}
	min := in.Config.Min
	if min <= 0 {
		min = defaultSemanticMin
	}
	window := in.Config.Window
	if window < 1 {
		window = defaultWindow
	}

	// 短文：单块（不触发嵌入——省一次调用）
	if len(rs) <= max {
		return []Chunk{{CharStart: 0, CharEnd: len(rs), Text: in.Text}}, nil
	}

	sents := splitSentences(rs)
	if len(sents) <= 1 {
		// 无句边界：按 max 硬切（步长 = max）
		return hardSplit(rs, 0, len(rs), max), nil
	}
	if in.Embedder == nil {
		return nil, errors.New("kb semantic 切片缺句向量入口（Input.Embedder 为空；semantic 缺省关——启用须注入 kb-embed 调用）")
	}

	texts := make([]string, len(sents))
	for i, s := range sents {
		texts[i] = string(rs[s.start:s.end])
	}
	vectors, err := in.Embedder(ctx, texts)
	if err != nil {
		return nil, fmt.Errorf("kb semantic 句向量化失败: %w", err)
	}
	if len(vectors) != len(texts) {
		return nil, fmt.Errorf("kb semantic 句向量条数不符：句 %d / 向量 %d", len(texts), len(vectors))
	}
	for i := range vectors {
		if len(vectors[i]) == 0 {
			return nil, fmt.Errorf("kb semantic 句向量为空（第 %d 句）", i+1)
		}
		if len(vectors[i]) != len(vectors[0]) {
			return nil, fmt.Errorf("kb semantic 句向量维度不一：第 %d 句 %d 维 / 首句 %d 维", i+1, len(vectors[i]), len(vectors[0]))
		}
	}

	// 相邻句窗相似度（边界 i：左窗 [i-window,i) 与右窗 [i,i+window) 的均值向量余弦）
	sims := make([]float64, 0, len(vectors)-1)
	for i := 1; i < len(vectors); i++ {
		l := i - window
		if l < 0 {
			l = 0
		}
		r := i + window
		if r > len(vectors) {
			r = len(vectors)
		}
		sims = append(sims, cosine(meanVec(vectors[l:i]), meanVec(vectors[i:r])))
	}

	// 分位阈值：threshold ∈ (0,1] 作**分位**（0.25 = 相似度分布下 25 分位值），低于该分位值的边界切分
	threshold := quantile(sims, in.Config.Threshold)

	// 按边界切段（段 = 句下标区间 [a,b]）
	type segment struct{ a, b int }
	var segments []segment
	start := 0
	for i := 1; i < len(vectors); i++ {
		if sims[i-1] < threshold {
			segments = append(segments, segment{start, i - 1})
			start = i
		}
	}
	segments = append(segments, segment{start, len(vectors) - 1})

	// min 归并：段过短 ⇒ 与下一段合并（保持连续；末段过短 ⇒ 并入上一段）
	var merged []segment
	for _, seg := range segments {
		if len(merged) > 0 && spanLen(sents, merged[len(merged)-1]) < min {
			prev := merged[len(merged)-1]
			merged[len(merged)-1] = segment{prev.a, seg.b}
			continue
		}
		merged = append(merged, seg)
	}
	if len(merged) >= 2 && spanLen(sents, merged[len(merged)-1]) < min {
		last := merged[len(merged)-1]
		merged = merged[:len(merged)-1]
		merged[len(merged)-1].b = last.b
	}

	// 产出块：段内按 max 切分（句边界优先，单句超 max 硬切）
	var out []Chunk
	for _, seg := range merged {
		s := sents[seg.a].start
		e := sents[seg.b].end
		if e-s <= max {
			out = append(out, Chunk{CharStart: s, CharEnd: e, Text: string(rs[s:e])})
			continue
		}
		// 贪心按句聚合到 ≤ max
		cur := -1
		flush := func(end int) {
			if cur >= 0 {
				out = append(out, Chunk{CharStart: cur, CharEnd: end, Text: string(rs[cur:end])})
				cur = -1
			}
		}
		for i := seg.a; i <= seg.b; i++ {
			sent := sents[i]
			if sent.end-sent.start > max {
				flush(sents[i-1].end)
				out = append(out, hardSplit(rs, sent.start, sent.end, max)...)
				continue
			}
			if cur < 0 {
				cur = sent.start
				continue
			}
			if sent.end-cur > max {
				flush(sents[i-1].end)
				cur = sent.start
				continue
			}
		}
		if cur >= 0 {
			flush(sents[seg.b].end)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("kb semantic 切片零产出（零文本禁静默入库）")
	}
	return out, nil
}

// hardSplit 区间按 max 硬切（步长 = max；无重叠——semantic 无 overlap 档）
func hardSplit(rs []rune, start, end, max int) []Chunk {
	var out []Chunk
	for s := start; s < end; {
		e := s + max
		if e > end {
			e = end
		}
		out = append(out, Chunk{CharStart: s, CharEnd: e, Text: string(rs[s:e])})
		if e >= end {
			break
		}
		s = e
	}
	return out
}

// spanLen 段（句区间）覆盖的 rune 数
func spanLen(sents []unit, seg struct{ a, b int }) int {
	if seg.a < 0 || seg.b >= len(sents) || seg.a > seg.b {
		return 0
	}
	return sents[seg.b].end - sents[seg.a].start
}

// splitSentences 句扫描（收缩首尾空白）：终止符 = 。！？!?；; 与换行；`.”` 仅在（后接空白/结束 ∧ 前一字符非数字）时计句尾
func splitSentences(rs []rune) []unit {
	var raw []unit
	start := -1
	flush := func(end int) {
		if start < 0 {
			return
		}
		if u, ok := trimSpan(rs, unit{start, end}); ok {
			raw = append(raw, u)
		}
		start = -1
	}
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if start < 0 {
			if unicode.IsSpace(r) {
				continue
			}
			start = i
		}
		switch r {
		case '。', '！', '？', '!', '?', '；', ';', '\n':
			flush(i + 1)
		case '.':
			next := rune(0)
			if i+1 < len(rs) {
				next = rs[i+1]
			}
			prevDigit := i > 0 && unicode.IsDigit(rs[i-1])
			if !prevDigit && (next == 0 || unicode.IsSpace(next)) {
				flush(i + 1)
			}
		}
	}
	flush(len(rs))
	return raw
}

// meanVec 向量均值
func meanVec(vectors [][]float32) []float32 {
	if len(vectors) == 0 {
		return nil
	}
	out := make([]float32, len(vectors[0]))
	for _, v := range vectors {
		for i, x := range v {
			out[i] += x
		}
	}
	n := float32(len(vectors))
	for i := range out {
		out[i] /= n
	}
	return out
}

// cosine 余弦相似度（零向量返回 0）
func cosine(a, b []float32) float64 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	var dot, na, nb float64
	for i := 0; i < n; i++ {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// quantile 分位值（线性插值；q ∈ (0,1]；空样本返回 0）
func quantile(values []float64, q float64) float64 {
	if len(values) == 0 {
		return 0
	}
	if q <= 0 {
		q = 0.01
	}
	if q > 1 {
		q = 1
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	pos := q * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	if lo == hi {
		return sorted[lo]
	}
	frac := pos - float64(lo)
	return sorted[lo]*(1-frac) + sorted[hi]*frac
}
