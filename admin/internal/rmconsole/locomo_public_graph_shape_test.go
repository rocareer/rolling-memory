// plugin: rolling-memory v0.0.58
package rollingmemory

import "testing"

// 形状预算常量：写死的是「封顶契约」本身，任一改动必须与 loadLocomoCorpus 同步核验。
//   - questionCiteCap：单题引证封顶 —— (a) 云端真实召回只取 Top-4 条高置信 unit_id，
//     (b) 证据句补足至最多 6 条，二者共用同一引证集合；
//   - cooccurChainCap：同址共现链上限 —— multiMemberCells 链式构造 `i < 48`。
const (
	questionCiteCap = 6
	cooccurChainCap = 48
)

// TestLocomoPublicGraphShapeBudget 首页第一屏公开图谱（LoCoMo）的形状预算门禁。
//
// 历史缺陷：单题云端召回的 Top-96 取回窗口被整包并入该题引证，
// 该题触达的 68 个记忆地址随即被全对全连边 ⇒ C(68,2)=2,278 条 weak_edge：
// 首屏 2,321 条黄边中 98.1% 来自这一题，最高度节点（noetic）度数 97，画布退化成一颗黄球。
// 本门禁用真实语料把「引证封顶 ⇒ 单题团边 ≤ C(6,2)」的契约钉死：任何把外部召回窗口
// 整包塞回引证（或调大封顶）的改动都会在这里变红。
func TestLocomoPublicGraphShapeBudget(t *testing.T) {
	corpus, err := loadLocomoCorpus()
	if err != nil {
		t.Skipf("LoCoMo 公开图谱语料不可用（%v）：跳过形状预算门禁", err)
	}
	if len(corpus.questions) == 0 {
		t.Skip("语料已载入但题目为空（evaldata/tiers/questions-100.json 缺失）：门禁无法成立，跳过")
	}

	questionIDs := make(map[string]bool, len(corpus.questions))
	for _, q := range corpus.questions {
		questionIDs[q.ID] = true
	}

	resp := buildLocomoSubgraph(corpus, -1, "", nil)
	citesByAnchor := make(map[string]int)
	weakByAnchor := make(map[string]int)
	degreeByNode := make(map[int]int)
	citesTotal, weakTotal := 0, 0
	for _, e := range resp.Edges {
		switch e.Relation {
		case "cites":
			citesTotal++
			if e.Anchor == "" {
				t.Errorf("cites 边 %d→%d 缺少 anchor（引证归属丢失）", e.From, e.To)
			}
			citesByAnchor[e.Anchor]++
		case "weak_edge":
			weakTotal++
			if e.Anchor == "" {
				t.Errorf("weak_edge 边 %d→%d 缺少 anchor（共振归属丢失）", e.From, e.To)
			}
			if questionIDs[e.Anchor] {
				weakByAnchor[e.Anchor]++
			}
		}
		degreeByNode[e.From]++
		degreeByNode[e.To]++
	}

	for anchor, n := range citesByAnchor {
		if n > questionCiteCap {
			t.Errorf("单题引证 %s = %d 条，超出封顶 %d 条（外部召回窗口未被裁剪）", anchor, n, questionCiteCap)
		}
	}
	maxWeakPerQuestion := questionCiteCap * (questionCiteCap - 1) / 2
	for anchor, n := range weakByAnchor {
		if n > maxWeakPerQuestion {
			t.Errorf("单题跨地址共振 %s = %d 条，超出 C(%d,2)=%d 条（O(k²) 全对全回潮）",
				anchor, n, questionCiteCap, maxWeakPerQuestion)
		}
	}
	if budget := maxWeakPerQuestion*len(corpus.questions) + cooccurChainCap; weakTotal > budget {
		t.Errorf("weak_edge 总数 %d 条，超出预算 %d 条（%d 题 × C(%d,2) + 同址共现链 %d 条）",
			weakTotal, budget, len(corpus.questions), questionCiteCap, cooccurChainCap)
	}

	maxDegree := 0
	for _, d := range degreeByNode {
		if d > maxDegree {
			maxDegree = d
		}
	}
	t.Logf("形状读数：题目=%d 节点=%d 边=%d（cites %d / weak_edge %d）单题共振峰值≤%d 最高度=%d",
		len(corpus.questions), len(resp.Nodes), len(resp.Edges), citesTotal, weakTotal, maxWeakPerQuestion, maxDegree)
}
