// plugin: kb v1.0.0
package search

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	repoKB "github.com/ai-go-hub/ai-go-admin/plugins/kb/repository/kb"
)

// ==================== RRF 融合（手算对照钉确定性；先例 rrf_test.go） ====================

// TestRRFFuseHandComputed 手算对照：k=60、两条腿、三块跨腿去重。
//
//	exact = [A(id=11), B(id=12)]；fts = [B(id=12), C(id=13)]
//	B = 1/62 + 1/61 = 0.0325（四位小数）＞ A = 1/61 = 0.0164 ＞ C = 1/62 = 0.0161
func TestRRFFuseHandComputed(t *testing.T) {
	outs := []legOutcome{
		{name: LegExact, hits: []repoKB.ChunkHit{{ChunkID: 11, TextHash: "aa"}, {ChunkID: 12, TextHash: "bb"}}},
		{name: LegFTS, hits: []repoKB.ChunkHit{{ChunkID: 12, TextHash: "bb"}, {ChunkID: 13, TextHash: "cc"}}},
	}
	rows := rrfFuse(outs, 60, 10)
	if len(rows) != 3 {
		t.Fatalf("融合行数 = %d，期望 3", len(rows))
	}
	gotIDs := []uint{rows[0].hit.ChunkID, rows[1].hit.ChunkID, rows[2].hit.ChunkID}
	wantIDs := []uint{12, 11, 13}
	for i := range wantIDs {
		if gotIDs[i] != wantIDs[i] {
			t.Fatalf("次序 = %v，期望 %v（B,A,C 手算序）", gotIDs, wantIDs)
		}
	}
	gotScores := []float64{rows[0].score, rows[1].score, rows[2].score}
	wantScores := []float64{0.0325, 0.0164, 0.0161}
	for i := range wantScores {
		if gotScores[i] != wantScores[i] {
			t.Fatalf("分 = %v，期望 %v（四位小数手算值）", gotScores, wantScores)
		}
	}
	// 跨腿去重：B 的贡献腿集 = [exact, fts]（出现次序）
	if len(rows[0].legs) != 2 || rows[0].legs[0] != LegExact || rows[0].legs[1] != LegFTS {
		t.Fatalf("B 贡献腿集 = %v，期望 [exact fts]", rows[0].legs)
	}
	// 失败腿不进融合（degraded 腿的行不参与）
	outs[1].err = context.DeadlineExceeded
	rows = rrfFuse(outs, 60, 10)
	if len(rows) != 2 || rows[0].hit.ChunkID != 11 {
		t.Fatalf("失败腿未剔除：%+v", rows)
	}
}

// TestRRFFuseTieBreak 同分托底确定性：text_hash 升序 → chunk_id 升序
func TestRRFFuseTieBreak(t *testing.T) {
	outs := []legOutcome{
		{name: LegExact, hits: []repoKB.ChunkHit{{ChunkID: 30, TextHash: "bb"}, {ChunkID: 20, TextHash: "aa"}}},
	}
	rows := rrfFuse(outs, 60, 10)
	if len(rows) != 2 || rows[0].hit.ChunkID != 30 || rows[1].hit.ChunkID != 20 {
		t.Fatalf("同腿内按 rank 保序失败：%+v", rows) // 同腿 rank 不同 ⇒ 分不同（30 者 rank1 分高）
	}
	// 跨腿同 rank ⇒ 同分：hash 升序（aa < bb）
	outs = []legOutcome{
		{name: LegExact, hits: []repoKB.ChunkHit{{ChunkID: 30, TextHash: "bb"}}},
		{name: LegFTS, hits: []repoKB.ChunkHit{{ChunkID: 20, TextHash: "aa"}}},
	}
	rows = rrfFuse(outs, 60, 10)
	if rows[0].hit.ChunkID != 20 || rows[1].hit.ChunkID != 30 {
		t.Fatalf("同分未按 text_hash 升序：%+v", rows)
	}
}

// TestRRFFuseTopKTruncation topK 截断（超帽只留前 topK）
func TestRRFFuseTopKTruncation(t *testing.T) {
	outs := []legOutcome{{name: LegExact, hits: []repoKB.ChunkHit{
		{ChunkID: 1, TextHash: "a"}, {ChunkID: 2, TextHash: "b"}, {ChunkID: 3, TextHash: "c"},
	}}}
	rows := rrfFuse(outs, 60, 2)
	if len(rows) != 2 {
		t.Fatalf("topK 截断失败：%d 行", len(rows))
	}
}

// ==================== 上限规整（top_k 缺省 8 / 硬上限 50；浏览面 ≤100） ====================

func TestNormalizeTopK(t *testing.T) {
	cases := map[int]int{0: 8, -3: 8, 1: 1, 8: 8, 50: 50, 51: 50, 1000: 50}
	for in, want := range cases {
		if got := NormalizeTopK(in); got != want {
			t.Fatalf("NormalizeTopK(%d) = %d，期望 %d", in, got, want)
		}
	}
}

func TestNormalizeMode(t *testing.T) {
	for _, in := range []string{"", "auto", "lexical", "vector", "hybrid", " HYBRID "} {
		if _, err := normalizeMode(in); err != nil {
			t.Fatalf("闭集内模式被拒：%q err=%v", in, err)
		}
	}
	for _, in := range []string{"full", "rrf", "vector2"} {
		if _, err := normalizeMode(in); err == nil {
			t.Fatalf("闭集外模式未拒：%q", in)
		}
	}
	if m, _ := normalizeMode(""); m != ModeAuto {
		t.Fatalf("空模式应归 auto，得到 %s", m)
	}
}

// ==================== 模式→腿集规划 ====================

func TestPlanLegsModes(t *testing.T) {
	svc := &Service{}
	ctx := context.Background()
	// 非 CJK：lexical = [exact, fts]（trgm 不启用——仅 CJK）
	legs, err := svc.planLegs(ctx, []uint{1}, ModeLexical, "alpha beta")
	if err != nil || len(legs) != 2 || legs[0] != LegExact || legs[1] != LegFTS {
		t.Fatalf("lexical（非 CJK）腿集 = %v err=%v", legs, err)
	}
	// CJK：lexical = [exact, fts, trgm]
	legs, err = svc.planLegs(ctx, []uint{1}, ModeLexical, "知识库检索")
	if err != nil || len(legs) != 3 || legs[2] != LegTrgm {
		t.Fatalf("lexical（CJK）腿集 = %v err=%v", legs, err)
	}
	// vector / hybrid
	if legs, _ = svc.planLegs(ctx, []uint{1}, ModeVector, "q"); len(legs) != 1 || legs[0] != LegVector {
		t.Fatalf("vector 腿集 = %v", legs)
	}
	if legs, _ = svc.planLegs(ctx, []uint{1}, ModeHybrid, "q"); len(legs) != 3 || legs[2] != LegVector {
		t.Fatalf("hybrid 腿集 = %v", legs)
	}
	// auto + 未初始化配置（embed 关）⇒ 词面档（不探测、不触 DB）
	if legs, _ = svc.planLegs(ctx, []uint{1}, ModeAuto, "q"); len(legs) != 2 {
		t.Fatalf("auto（嵌入档关）腿集 = %v", legs)
	}
}

// ==================== snippet（原文逐字子串 + 高亮区间） ====================

func TestBuildSnippet(t *testing.T) {
	// 短文本：全文 + 命中高亮
	text := "前段铺垫：检索目标词 ZEBRA 出现在这里。"
	sn, hl := buildSnippet(text, "ZEBRA", defaultSnippetCap)
	if sn != text {
		t.Fatalf("短文本应原样返回：%q", sn)
	}
	if hl[0] < 0 || hl[1] <= hl[0] {
		t.Fatalf("高亮缺失：%v", hl)
	}
	// 高亮区间逐字对齐（rune 口径）
	rs := []rune(sn)
	if got := string(rs[hl[0]:hl[1]]); got != "ZEBRA" {
		t.Fatalf("高亮区间 = %q，期望 ZEBRA", got)
	}
	// 大小写不敏感核对
	sn2, hl2 := buildSnippet(text, "zebra", defaultSnippetCap)
	if sn2 != text || hl2 != hl {
		t.Fatalf("大小写不敏感核对失败：%q %v", sn2, hl2)
	}
	// 无命中：前缀帽（≤200 rune；禁改写）
	long := ""
	for i := 0; i < 300; i++ {
		long += "字"
	}
	sn3, hl3 := buildSnippet(long, "不存在词", defaultSnippetCap)
	if len([]rune(sn3)) != defaultSnippetCap || hl3 != [2]int{-1, -1} {
		t.Fatalf("超长无命中应前缀截断 200 rune 且无高亮：len=%d hl=%v", len([]rune(sn3)), hl3)
	}
	// 命中在尾部：窗内命中应含高亮且逐字
	deep := long + "尾部命中词"
	sn4, hl4 := buildSnippet(deep, "尾部命中词", defaultSnippetCap)
	if hl4[0] < 0 {
		t.Fatalf("长文本尾部命中未给高亮：%q", sn4)
	}
	rs4 := []rune(sn4)
	if got := string(rs4[hl4[0]:hl4[1]]); got != "尾部命中词" {
		t.Fatalf("尾部高亮区间 = %q", got)
	}
}

// ==================== 定位组装 ====================

func TestBuildLocator(t *testing.T) {
	fileHit := repoKB.ChunkHit{Locator: "kb:doc:7#p3", CharStart: 10, CharEnd: 42, SourceRef: "smoke://doc"}
	loc := buildLocator(fileHit)
	if loc.Kind != "file" || loc.Path != "smoke://doc" || loc.Anchor != "kb:doc:7#p3" {
		t.Fatalf("file 档定位 = %+v", loc)
	}
	pageHit := repoKB.ChunkHit{Locator: "kb:doc:8#p1", PageFrom: 3, PageTo: 4}
	if loc = buildLocator(pageHit); loc.Kind != "page" || loc.PageFrom != 3 || loc.Path != "" {
		t.Fatalf("page 档定位 = %+v", loc)
	}
	tsHit := repoKB.ChunkHit{Locator: "kb:doc:9#p2", TimeFrom: 120, TimeTo: 130, PageFrom: 1}
	if loc = buildLocator(tsHit); loc.Kind != "ts" || loc.TimeFrom != 120 {
		t.Fatalf("ts 档定位（时间戳优先） = %+v", loc)
	}
}

// ==================== 查询向量缓存（键/原子落盘/损坏空启） ====================

func TestQueryCacheKeyAndPersist(t *testing.T) {
	if cacheKey("kb-embed", "q") == cacheKey("kb-embedq", "") {
		t.Fatal("缓存键拼接歧义（NUL 分隔失效）")
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "kb-qvec.json")
	c := newQueryVecCache()
	c.file = file // 直指临时文件（跳过缺省路径解析）
	c.loaded = true
	if _, ok := c.Get("kb-embed", "未缓存"); ok {
		t.Fatal("未缓存查询不应命中")
	}
	vec := []float32{0.5, -1.25, 3}
	if err := c.Put("kb-embed", "冒烟检索", vec); err != nil {
		t.Fatalf("Put 落盘失败: %v", err)
	}
	if _, err := filepath.Glob(file + ".tmp"); err != nil {
		t.Fatal(err)
	}
	if matches, _ := filepath.Glob(file + ".tmp"); len(matches) != 0 {
		t.Fatalf("原子落盘残留 .tmp：%v", matches)
	}
	// 重载（模拟进程重启）：同查询同向量
	c2 := newQueryVecCache()
	c2.file = file
	got, ok := c2.Get("kb-embed", "冒烟检索")
	if !ok || len(got) != 3 || got[2] != 3 {
		t.Fatalf("重载未命中或向量不符：ok=%v got=%v", ok, got)
	}
	// 损坏文件 = 空启（不 panic、不命中）
	if err := os.WriteFile(file, []byte("{不是 json"), 0o644); err != nil {
		t.Fatal(err)
	}
	c3 := newQueryVecCache()
	c3.file = file
	if _, ok := c3.Get("kb-embed", "冒烟检索"); ok {
		t.Fatal("损坏文件应空启")
	}
}

// ==================== 小工具 ====================

func TestContainsCJKAndEscapeLike(t *testing.T) {
	if !containsCJK("知识库 retr") || !containsCJK("カタカナ") || !containsCJK("한글") {
		t.Fatal("CJK 判定漏判")
	}
	if containsCJK("plain ascii only") {
		t.Fatal("纯 ASCII 误判为 CJK")
	}
	if got := escapeLike(`50%_\x`); got != `50\%\_\\x` {
		t.Fatalf("LIKE 转义 = %q", got)
	}
}
