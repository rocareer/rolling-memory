// plugin: kb v1.0.0
// 检索腿编排与融合：模式→腿集规划、多腿并行执行、单腿降级、RRF(k) 融合、
// 返回定位组装（snippet 逐字 + 结构化 locator）。
//
// 决策留痕（171「词面腿 + 向量腿并行」）：
//   - 并行 = 新建实现（仓内无多腿并行先例；既有实现是顺序 for 循环逐腿 try）——本文件用
//     goroutine + WaitGroup 并行执行各腿（向量腿含查询嵌入网络往返，串行会把延迟叠加）；
//   - 降级骨架照现行口径：逐腿 try，失败只落 degraded[]（"腿: 原因"）+ 继续；
//     全腿失败才整查报错（先例：逐腿 try、全败才报）；
//   - RRF 内核（score = Σ 1/(k+rank)，rank 从 1 起）；确定性 tie-break
//     （分↓ → text_hash↑ → chunk_id↑，同构替换既有口径的稳定序）；
//     截断 topK；score 四位小数。
package search

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/ai-go-hub/ai-go-admin/plugins/kb/kbcfg"
	repoKB "github.com/ai-go-hub/ai-go-admin/plugins/kb/repository/kb"
	kbembed "github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/embed"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/ingest"
)

// 腿名闭集（词面腿 exact/fts/trgm + 向量腿 vector；fts_or 未启用——闭集加腿=改代码+版本）
const (
	LegExact  = "exact"  // 逐字子串（ILIKE %q%，通配符转义；trgm GIN 支撑）
	LegFTS    = "fts"    // 词面 tsv（websearch_to_tsquery；写读同 FTS 配置单点）
	LegTrgm   = "trgm"   // 词面近似（word_similarity；仅 CJK 查询启用——）
	LegVector = "vector" // 向量腿（halfvec 余弦；可读集 + 当前模型身份先下推再排序）
)

// defaultSnippetCap snippet 字符帽（缺省 200；禁摘要改写——恒为原文逐字子串）
const defaultSnippetCap = 200

// legOutcome 单腿结果（编排内部：腿名 + 命中或失败读数 + 耗时）
type legOutcome struct {
	name string
	hits []repoKB.ChunkHit
	err  error
	ms   int64
}

// planLegs 模式 → 腿集（T4）：保留原签名（单测消费）；生产路径 Search 走 planLegsWithNotes
// （auto 降级需把归因注明带进响应 degraded[] 与检索日志）。
func (s *Service) planLegs(ctx context.Context, readable []uint, mode, query string) ([]string, error) {
	legs, _, err := s.planLegsWithNotes(ctx, readable, mode, query)
	return legs, err
}

// planLegsWithNotes 规划腿集并返回规划期降级注明（进响应 degraded[] 与 kb_search_log；空 = 无）：
//
//	lexical = exact + fts（+ trgm，仅 CJK 查询）；vector = vector；hybrid = lexical + vector；
//	auto    = hybrid —— 但嵌入档关或可读域内无向量内容时自动降级词面（不发嵌入调用、不起向量腿）。
func (s *Service) planLegsWithNotes(ctx context.Context, readable []uint, mode, query string) ([]string, []string, error) {
	lexical := []string{LegExact, LegFTS}
	if containsCJK(query) {
		lexical = append(lexical, LegTrgm) //：trgm 表达式面仅 CJK 查询启用（无空格 CJK 分词缺失召回）
	}
	withVector := func() []string {
		out := make([]string, 0, len(lexical)+1)
		out = append(out, lexical...)
		return append(out, LegVector)
	}
	switch mode {
	case ModeLexical:
		return lexical, nil, nil
	case ModeVector:
		return []string{LegVector}, nil, nil
	case ModeHybrid:
		return withVector(), nil, nil
	case ModeAuto:
		if !embedEnabled() {
			return lexical, nil, nil // 嵌入档关 ⇒ 确定无新向量内容：词面档（不探测、不嵌入）
		}
		// 身份不可用（Provider 未注册/模型未配置）⇒ 确定无**当前身份**可用向量：按 auto 契约
		// 降级词面（探针与腿同一身份口径——不起一条恒空腿、不白付查询嵌入）。日志留痕，不响亮失败。
		identity := kbembed.ModelIdentity()
		if identity == "" {
			log.Printf("[kb] 检索自动档：嵌入模型身份不可用（嵌入档 %s），降级词面腿", kbembed.DefaultAgent)
			return lexical, nil, nil
		}
		has, err := s.store.HasVectorContent(ctx, readable, identity)
		if err != nil {
			return nil, nil, err
		}
		if has {
			return withVector(), nil, nil
		}
		// 无当前身份向量 ⇒ 降级词面；但若域内存在**异身份**向量行，落一条点名日志（身份切换后未重嵌的形态——静默降级会失召回且无任何提示）
		// + 回显一条 degraded 注明
		// （可观测口径：响应面与日志面同一归因；**结果语义不变**，仅注明）。探针失败不阻断检索
		// （best-effort：少一条诊断与注明，不影响降级决定）。
		if foreign, ferr := s.store.HasForeignVectorContent(ctx, readable, identity); ferr == nil && foreign {
			log.Printf("[kb] 检索自动档：可读域内无当前身份（%s）向量行、存在历史/异身份向量行——已降级"+
				"词面腿；若为身份切换后首检，请运行 aigo kb reembed 重产向量（一次性运维步骤）", identity)
			return lexical, []string{fmt.Sprintf("%s: auto 档降级词面——可读域内无当前身份（%s）向量行、存在历史/"+
				"异身份向量行（疑身份切换后未重产：aigo kb reembed 重产，aigo kb check 点名）", LegVector, identity)}, nil
		}
		return lexical, nil, nil // 无（当前身份）向量内容自动降级词面
	}
	return nil, nil, fmt.Errorf("检索模式未登记: %s", mode)
}

// runLegs 并行执行腿集（结果按腿集原序落位；单腿失败不阻断其他腿）。
// tags 非空 = 标签过滤下推（T5：两路子查询拼进同一条件，随可读集进 WHERE，禁应用层后过滤）。
func (s *Service) runLegs(ctx context.Context, readable []uint, query string, window int, legs []string, tags *repoKB.TagFilter) []legOutcome {
	out := make([]legOutcome, len(legs))
	var wg sync.WaitGroup
	for i, name := range legs {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			start := s.now()
			hits, err := s.runLeg(ctx, readable, query, window, name, tags)
			out[i] = legOutcome{name: name, hits: hits, err: err, ms: s.now().Sub(start).Milliseconds()}
		}(i, name)
	}
	wg.Wait()
	return out
}

// runLeg 单腿执行（机械分发；查询嵌入走 ai 唯一入口 + 查询向量缓存）。
func (s *Service) runLeg(ctx context.Context, readable []uint, query string, window int, leg string, tags *repoKB.TagFilter) ([]repoKB.ChunkHit, error) {
	switch leg {
	case LegExact:
		like := "%" + escapeLike(query) + "%"
		return s.store.SearchExact(ctx, readable, like, window, tags)
	case LegFTS:
		// 查询配置单点（tsv 生成列冻结 simple，其余值响亮拒绝 ⇒
		// 该腿降级回显 degraded[]，不静默错位失召回）
		fts, err := ingest.ResolveFTSConfig(kbcfg.Get().FTS.Config)
		if err != nil {
			return nil, err
		}
		return s.store.SearchFTS(ctx, readable, query, fts, window, tags)
	case LegTrgm:
		return s.store.SearchTrgm(ctx, readable, query, trgmThreshold(), window, tags)
	case LegVector:
		if !embedEnabled() {
			return nil, errors.New("嵌入档关闭（kb.embed.enabled=false）")
		}
		// 身份**先解析**（早失败：身份不可用不白付一次查询嵌入；过滤面与缓存键面同源单点——见 vector.go）
		identity, err := resolvedModelIdentity()
		if err != nil {
			return nil, err
		}
		vec, err := s.embedQuery(ctx, query)
		if err != nil {
			return nil, err
		}
		return s.store.SearchVector(ctx, readable, formatEmbedding(vec), identity, window, tags)
	}
	return nil, fmt.Errorf("检索腿未登记: %s", leg)
}

// fusedRow 融合结果行（命中 + 融合分 + 贡献腿集）
type fusedRow struct {
	hit   repoKB.ChunkHit
	score float64
	legs  []string
}

// rrfFuse 秩融合（Reciprocal Rank Fusion；照既有 RRF 形态，去重键 = chunk_id（KB 无 hash 主键；
// 同一 chunk 跨腿去重，保留首个命中行——各腿列面同构）：
// score = Σ 1/(k+rank)；排序：score 降序 → text_hash 升序 → chunk_id 升序（确定性）；截断 topK；
// score 四位小数。k ≤ 0 回落 60（RRF(k=60)）。
func rrfFuse(outs []legOutcome, k, topK int) []fusedRow {
	if k <= 0 {
		k = defaultRRFK
	}
	type agg struct {
		hit   repoKB.ChunkHit
		score float64
		legs  []string
	}
	byID := map[uint]*agg{}
	order := make([]uint, 0, 32)
	for _, o := range outs {
		if o.err != nil {
			continue
		}
		for idx, h := range o.hits {
			rank := idx + 1
			a, ok := byID[h.ChunkID]
			if !ok {
				a = &agg{hit: h}
				byID[h.ChunkID] = a
				order = append(order, h.ChunkID)
			}
			a.score += 1 / float64(k+rank)
			a.legs = appendUnique(a.legs, o.name)
		}
	}
	rows := make([]fusedRow, 0, len(order))
	for _, id := range order {
		a := byID[id]
		rows = append(rows, fusedRow{hit: a.hit, score: a.score, legs: a.legs})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].score != rows[j].score {
			return rows[i].score > rows[j].score
		}
		if rows[i].hit.TextHash != rows[j].hit.TextHash {
			return rows[i].hit.TextHash < rows[j].hit.TextHash
		}
		return rows[i].hit.ChunkID < rows[j].hit.ChunkID
	})
	if topK > 0 && len(rows) > topK {
		rows = rows[:topK]
	}
	for i := range rows {
		rows[i].score = round4(rows[i].score)
	}
	return rows
}

// appendUnique 追加去重（腿名集；保持出现次序）
func appendUnique(list []string, v string) []string {
	for _, s := range list {
		if s == v {
			return list
		}
	}
	return append(list, v)
}

// round4 四位小数（读数口径：float64(int64(v*10000+0.5))/10000）
func round4(v float64) float64 { return float64(int64(v*10000+0.5)) / 10000 }

// Locator 结构化定位（切片锚 + 同构承载文件行/页/时间戳；此处 paths 取文档来源引用）。
// 字段缺省省略（0 值不输出），kind 三档：file（纯文本）/ page（页定位）/ ts（时间戳；细分 dialog）。
type Locator struct {
	Anchor    string `json:"anchor"`
	Kind      string `json:"kind"`
	Path      string `json:"path,omitempty"`
	CharStart int    `json:"char_start"`
	CharEnd   int    `json:"char_end"`
	PageFrom  int    `json:"page_from,omitempty"`
	PageTo    int    `json:"page_to,omitempty"`
	TimeFrom  int64  `json:"time_from,omitempty"`
	TimeTo    int64  `json:"time_to,omitempty"`
}

// buildLocator 定位组装（纯函数；字段直取切片行，不改写）
func buildLocator(h repoKB.ChunkHit) Locator {
	kind := "file"
	switch {
	case h.TimeFrom > 0:
		kind = "ts"
	case h.PageFrom > 0:
		kind = "page"
	}
	loc := Locator{
		Anchor:    h.Locator,
		Kind:      kind,
		CharStart: h.CharStart,
		CharEnd:   h.CharEnd,
		PageFrom:  h.PageFrom,
		PageTo:    h.PageTo,
		TimeFrom:  h.TimeFrom,
		TimeTo:    h.TimeTo,
	}
	if kind == "file" {
		loc.Path = h.SourceRef
	}
	return loc
}

// buildSnippet snippet 组装单点（原文逐字子串 + 高亮区间，字符帽缺省 200；禁摘要改写）。
// 返回 snippet 与高亮区间（snippet 内 rune 偏移 [起,止)；无命中 = [-1,-1]）。
//
// 高亮口径：大小写不敏感定位（仅当 ToLower 不改变字节长度时启用——防 Unicode 大小写映射改变长度
// 而错位；CJK 恒等长）；命中点居中开窗（命中前留 1/4 帽），窗内不足以容纳命中区间则不加高亮。
func buildSnippet(text, query string, cap int) (string, [2]int) {
	none := [2]int{-1, -1}
	if cap <= 0 {
		cap = defaultSnippetCap
	}
	runes := []rune(text)
	if len(runes) == 0 {
		return "", none
	}
	start, end := -1, -1
	if q := strings.TrimSpace(query); q != "" {
		low := strings.ToLower(text)
		if len(low) == len(text) { // 字节长度不变才映射偏移（安全近似）
			if idx := strings.Index(low, strings.ToLower(q)); idx >= 0 {
				start = utf8.RuneCountInString(text[:idx])
				end = start + utf8.RuneCountInString(q)
			}
		}
	}
	base := 0
	if len(runes) > cap {
		if start >= 0 && start > cap/4 {
			base = start - cap/4
		}
		if base+cap > len(runes) {
			base = len(runes) - cap
		}
		if base < 0 {
			base = 0
		}
	}
	window := runes[base:]
	if len(window) > cap {
		window = window[:cap]
	}
	hl := none
	if start >= 0 && end > start && start >= base && end <= base+len(window) {
		hl = [2]int{start - base, end - base}
	}
	return string(window), hl
}

// containsCJK 查询是否含 CJK（trgm 腿启用闸；「仅 CJK 查询启用」）：
// 覆盖中日韩汉字/假名/谚文/兼容与全角区。
func containsCJK(s string) bool {
	for _, r := range s {
		switch {
		case r >= 0x2E80 && r <= 0x9FFF, // CJK 部首扩展 + 假名 + 统一表意
			r >= 0xAC00 && r <= 0xD7AF, // 谚文音节
			r >= 0xF900 && r <= 0xFAFF, // CJK 兼容表意
			r >= 0xFF00 && r <= 0xFFEF: // 全角形式
			return true
		}
	}
	return false
}

// escapeLike 转义 LIKE/ILIKE 通配符（\ % _；口径同 kb.escapeLike——跨包各自持有小工具）
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
