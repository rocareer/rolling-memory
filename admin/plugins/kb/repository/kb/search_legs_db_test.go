// plugin: kb v1.0.0
package kb

// 检索腿连库用例：重点 = **HasVectorContent 真连库执行**（auto 档判据）+ **文档在场条件
// **与**向量身份过滤**。
// 门控：PLUGIN_KB_DB=1（同前）；跑法：PLUGIN_KB_DB=1 go test -count=1 -timeout=120s ./plugins/kb/repository/kb/
//
// 来历（实测确认的缺陷与整改）：HasVectorContent 原用 `v.library_id = ANY(?)`——GORM Raw
// 路径对 `(` 后的切片变量逐元素展开为 `ANY($1,$2,…)`：≥2 可读库 ⇒ PG 语法错 42601；单库 ⇒ 标量绑给
// _int8[] 编码失败。即 auto（缺省）档在嵌入档开的真实部署下缺省检索必失败；本用例真连库钉住整改
// （改 `IN ?`）。另增：身份过滤（腿/探针同口径）与文档在场条件（去 ready）真连库用例。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"
	"github.com/ai-go-hub/ai-go-admin/internal/infra/database"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"

	"gorm.io/gorm"
)

const kbLegTestDBEnv = "PLUGIN_KB_DB"

func chdirRepoRootLeg(t *testing.T) {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if err := os.Chdir(dir); err != nil {
				t.Fatal(err)
			}
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("未找到 go.mod（仓库根）")
		}
		dir = parent
	}
}

// legFixture 腿用例夹具（自建自清；行 id 全登记）
type legFixture struct {
	t      *testing.T
	ctx    context.Context
	db     *gorm.DB
	store  *Store
	suffix string

	libIDs   []uint
	docIDs   []uint
	chunkIDs []uint
	vecIDs   []uint
}

func newLegFixture(t *testing.T) *legFixture {
	t.Helper()
	if os.Getenv(kbLegTestDBEnv) != "1" {
		t.Skipf("需要数据库：设 %s=1 启用（库需已应用本插件的自管迁移）", kbLegTestDBEnv)
	}
	chdirRepoRootLeg(t)
	if err := config.Init(); err != nil {
		t.Fatalf("初始化配置: %v", err)
	}
	if err := database.Init(); err != nil {
		t.Fatalf("初始化数据库: %v", err)
	}
	requireKBTestDatabase(t, database.DB())
	applyPluginMigrations(t)
	f := &legFixture{
		t: t, ctx: context.Background(), db: database.DB(), store: NewStore(),
		suffix: fmt.Sprintf("%d", time.Now().UnixNano()),
	}
	t.Cleanup(f.cleanup)
	return f
}

// cleanup 硬删本用例登记行（顺序：向量 → 切片 → 文档 → 库）
func (f *legFixture) cleanup() {
	del := func(table string, ids []uint) {
		if len(ids) == 0 {
			return
		}
		if err := f.db.Exec("DELETE FROM "+table+" WHERE id IN ?", ids).Error; err != nil {
			f.t.Logf("清理 %s 失败（请手工核）: %v", table, err)
		}
	}
	del("kb_chunk_vectors", f.vecIDs)
	del("kb_chunk", f.chunkIDs)
	del("kb_document", f.docIDs)
	del("kb_library", f.libIDs)
}

// legTestModel 用例用「当前模型身份」字面值（占位 = 解析后的嵌入模型标识；整改后
// 向量行 model 列 = 模型身份而非 agent 名，检索腿/探针按当前身份过滤）。
const legTestModel = "kb-leg-identity-v1"

// legHistoryModel 历史身份（收口前口径 = agent 名；现役行不应再以此落列，仅作排除面证据）。
const legHistoryModel = "kb-embed"

// legDoc 造「指定 build_state/active_build 的文档 + 一片指定 build 的切片」（返回库 id、切片 id）
func (f *legFixture) legDoc(tag, buildState, activeBuild, chunkBuild, text string) (uint, uint) {
	f.t.Helper()
	lib := model.KBLibrary{OwnerKind: model.KBLibraryOwnerKindSystem, Name: fmt.Sprintf("kb-leg-%s-%s", tag, f.suffix), Status: u8p(1)}
	if err := gorm.G[model.KBLibrary](f.db).Create(f.ctx, &lib); err != nil {
		f.t.Fatalf("建库失败: %v", err)
	}
	f.libIDs = append(f.libIDs, lib.ID)

	doc := model.KBDocument{
		LibraryID: lib.ID, Title: fmt.Sprintf("kb-leg-doc-%s", tag), SourceKind: model.KBDocumentSourceFile,
		SourceRef: "fixture.md", ContentHash: fmt.Sprintf("%040s", tag+"-h"), Mime: "text/markdown", Size: 1,
		BuildState: buildState, ActiveBuild: activeBuild,
		CreatedByKind: model.KBActorKindAdmin, CreatedByID: 990002,
	}
	if err := gorm.G[model.KBDocument](f.db).Create(f.ctx, &doc); err != nil {
		f.t.Fatalf("建文档失败: %v", err)
	}
	f.docIDs = append(f.docIDs, doc.ID)

	c := model.KBChunk{
		LibraryID: lib.ID, DocumentID: doc.ID, BuildHash: chunkBuild,
		ConfigHash: fmt.Sprintf("%040s", tag+"-c"), Chunker: "fixed", ChunkerVersion: "v1", Seq: 1,
		Locator:   fmt.Sprintf("kb:doc:%d#p1", doc.ID),
		CharStart: 0, CharEnd: len([]rune(text)), Text: text, TextHash: fmt.Sprintf("%040s", tag+"-t"),
		// 匹配面列：legDoc 缺省写 verbatim 文本（= 无派生前缀的行口径）；
		// 派生文本用例经 legDocDerived 显式给（见下）。
		MatchText: text,
	}
	if err := gorm.G[model.KBChunk](f.db).Create(f.ctx, &c); err != nil {
		f.t.Fatalf("建切片失败: %v", err)
	}
	f.chunkIDs = append(f.chunkIDs, c.ID)
	return lib.ID, c.ID
}

// legDocDerived 造「match_text 与 verbatim text 分离」的切片行（匹配面用例：派生前缀只在 match_text；
// matchText 传空串 = 模拟「迁移后未回填」存量行——verbatim 正文在 c.text、匹配面列为空）。
func (f *legFixture) legDocDerived(tag, buildState, activeBuild, chunkBuild, text, matchText string) (uint, uint) {
	f.t.Helper()
	lib := model.KBLibrary{OwnerKind: model.KBLibraryOwnerKindSystem, Name: fmt.Sprintf("kb-leg-%s-%s", tag, f.suffix), Status: u8p(1)}
	if err := gorm.G[model.KBLibrary](f.db).Create(f.ctx, &lib); err != nil {
		f.t.Fatalf("建库失败: %v", err)
	}
	f.libIDs = append(f.libIDs, lib.ID)

	doc := model.KBDocument{
		LibraryID: lib.ID, Title: fmt.Sprintf("kb-leg-doc-%s", tag), SourceKind: model.KBDocumentSourceFile,
		SourceRef: "fixture.md", ContentHash: fmt.Sprintf("%040s", tag+"-h"), Mime: "text/markdown", Size: 1,
		BuildState: buildState, ActiveBuild: activeBuild,
		CreatedByKind: model.KBActorKindAdmin, CreatedByID: 990002,
	}
	if err := gorm.G[model.KBDocument](f.db).Create(f.ctx, &doc); err != nil {
		f.t.Fatalf("建文档失败: %v", err)
	}
	f.docIDs = append(f.docIDs, doc.ID)

	c := model.KBChunk{
		LibraryID: lib.ID, DocumentID: doc.ID, BuildHash: chunkBuild,
		ConfigHash: fmt.Sprintf("%040s", tag+"-c"), Chunker: "heading", ChunkerVersion: "v1", Seq: 1,
		Locator:   fmt.Sprintf("kb:doc:%d#p1", doc.ID),
		CharStart: 0, CharEnd: len([]rune(text)), Text: text, TextHash: fmt.Sprintf("%040s", tag+"-t"),
		MatchText: matchText,
	}
	if err := gorm.G[model.KBChunk](f.db).Create(f.ctx, &c); err != nil {
		f.t.Fatalf("建切片失败: %v", err)
	}
	f.chunkIDs = append(f.chunkIDs, c.ID)
	return lib.ID, c.ID
}

// readyDocWithVector 造「就绪文档 + 当前 build 切片 + 向量行」三件套（身份 = legTestModel）
func (f *legFixture) readyDocWithVector(tag string) uint {
	f.t.Helper()
	return f.readyDocWithVectorModel(tag, legTestModel)
}

// readyDocWithVectorModel 同上，向量行身份显式指定（身份过滤用例面）
func (f *legFixture) readyDocWithVectorModel(tag, identity string) uint {
	f.t.Helper()
	build := fmt.Sprintf("%040s", tag) // char(40) 等宽（bpchar 补空格语义下也不影响等值比较）
	libID, chunkID := f.legDoc(tag, model.KBDocumentBuildReady, build, build, "x")

	// halfvec(1024) 文本形态：1024 维零向量
	vec := chunkVectorRow{
		ChunkID: chunkID, LibraryID: libID, Model: identity, TextHash: fmt.Sprintf("%040s", tag+"-t"),
		Embedding: "[" + strings.Repeat("0,", 1023) + "0]", EmbeddingSpace: "",
	}
	if err := gorm.G[chunkVectorRow](f.db).Create(f.ctx, &vec); err != nil {
		f.t.Fatalf("建向量行失败: %v", err)
	}
	f.vecIDs = append(f.vecIDs, vec.ID)
	return libID
}

// chunkVectorRow kb_chunk_vectors 行（本用例写入面；列面对齐 migrations/2_pipeline.up.sql）
type chunkVectorRow struct {
	ID             uint   `gorm:"column:id;primarykey"`
	ChunkID        uint   `gorm:"column:chunk_id"`
	LibraryID      uint   `gorm:"column:library_id"`
	Model          string `gorm:"column:model"`
	TextHash       string `gorm:"column:text_hash"`
	Embedding      string `gorm:"column:embedding"`
	EmbeddingSpace string `gorm:"column:embedding_space"`
}

// TableName 表名单点
func (chunkVectorRow) TableName() string { return "kb_chunk_vectors" }

func u8p(v uint8) *uint8 { return &v }

// TestHasVectorContentDB auto 档判据真连库：单库/多库/零向量/非可读四态（整改前单库/多库两态必报错）；
// 身份口径 = 当前模型身份（历史 agent 名等其它身份不算「有内容」）。
func TestHasVectorContentDB(t *testing.T) {
	f := newLegFixture(t)
	ctx := f.ctx

	libA := f.readyDocWithVector("a")
	libB := f.readyDocWithVector("b")

	// 单库（原缺陷：单元素 → `ANY($1)` 标量绑给 _int8[] 编码失败）
	ok, err := f.store.HasVectorContent(ctx, []uint{libA}, legTestModel)
	if err != nil || !ok {
		t.Fatalf("单库探测应 true/nil: ok=%v err=%v", ok, err)
	}
	// 多库（原缺陷：展开成 `ANY($1,$2)` → SQLSTATE 42601）
	ok, err = f.store.HasVectorContent(ctx, []uint{libA, libB}, legTestModel)
	if err != nil || !ok {
		t.Fatalf("多库探测应 true/nil: ok=%v err=%v", ok, err)
	}
	// 可读集外（存在向量但不可读 ⇒ false，不是错误）
	ok, err = f.store.HasVectorContent(ctx, []uint{libA + 100000}, legTestModel)
	if err != nil || ok {
		t.Fatalf("非可读库应 false/nil: ok=%v err=%v", ok, err)
	}
	// 空集（服务层短路语义：false/nil）
	ok, err = f.store.HasVectorContent(ctx, nil, legTestModel)
	if err != nil || ok {
		t.Fatalf("空可读集应 false/nil: ok=%v err=%v", ok, err)
	}
	// 空身份（服务层不该发；机械短路 false/nil——不许 `v.model = ''` 兜底混入空身份行）
	ok, err = f.store.HasVectorContent(ctx, []uint{libA}, "")
	if err != nil || ok {
		t.Fatalf("空身份应 false/nil: ok=%v err=%v", ok, err)
	}
}

// TestHasForeignVectorContentDB 异身份探针真连库：域内只有历史/异身份
// 向量行（旧 agent 名口径）时——HasVectorContent=false（auto 档降级词面）、HasForeignVectorContent
// =true（服务层据此落点名日志，替代静默失召回）；仅当前身份行时探针为 false；混存时命中；
// 非可读域/空集/空身份一律 false/nil（与 HasVectorContent 同边界）。
func TestHasForeignVectorContentDB(t *testing.T) {
	f := newLegFixture(t)
	ctx := f.ctx

	// ① 仅历史身份（收口前落库口径 agent 名）：无当前身份内容、有异身份行
	libLegacy := f.readyDocWithVectorModel("legacy-only", "kb-embed")
	if ok, err := f.store.HasVectorContent(ctx, []uint{libLegacy}, legTestModel); err != nil || ok {
		t.Fatalf("仅历史身份不应算当前身份内容: ok=%v err=%v", ok, err)
	}
	if ok, err := f.store.HasForeignVectorContent(ctx, []uint{libLegacy}, legTestModel); err != nil || !ok {
		t.Fatalf("仅历史身份应命中异身份探针: ok=%v err=%v", ok, err)
	}
	// ② 仅当前身份：非异身份
	libCur := f.readyDocWithVector("cur-only")
	if ok, err := f.store.HasForeignVectorContent(ctx, []uint{libCur}, legTestModel); err != nil || ok {
		t.Fatalf("仅当前身份不应命中异身份探针: ok=%v err=%v", ok, err)
	}
	// ③ 两身份混存（多库）：命中
	if ok, err := f.store.HasForeignVectorContent(ctx, []uint{libCur, libLegacy}, legTestModel); err != nil || !ok {
		t.Fatalf("混存应命中异身份探针: ok=%v err=%v", ok, err)
	}
	// ④ 边界：非可读域 / 空集 / 空身份 —— false/nil
	if ok, err := f.store.HasForeignVectorContent(ctx, []uint{libLegacy + 100000}, legTestModel); err != nil || ok {
		t.Fatalf("非可读库应 false/nil: ok=%v err=%v", ok, err)
	}
	if ok, err := f.store.HasForeignVectorContent(ctx, nil, legTestModel); err != nil || ok {
		t.Fatalf("空可读集应 false/nil: ok=%v err=%v", ok, err)
	}
	if ok, err := f.store.HasForeignVectorContent(ctx, []uint{libLegacy}, ""); err != nil || ok {
		t.Fatalf("空身份应 false/nil: ok=%v err=%v", ok, err)
	}
}

// TestSearchLegModelIdentityDB 向量身份过滤真连库：腿与探针都只认「当前模型身份」；
// 历史 agent 名身份（收口前落库口径）与其它身份行按不匹配自然排除（不混入）。
func TestSearchLegModelIdentityDB(t *testing.T) {
	f := newLegFixture(t)
	ctx := f.ctx

	libCur := f.readyDocWithVectorModel("idm-cur", legTestModel)
	libOld := f.readyDocWithVectorModel("idm-old", legHistoryModel)
	vec := "[" + strings.Repeat("0,", 1023) + "0]"

	// 两库均可读：只召回当前身份那一行（历史身份不混入）
	hits, err := f.store.SearchVector(ctx, []uint{libCur, libOld}, vec, legTestModel, 10, nil)
	if err != nil {
		t.Fatalf("向量腿失败: %v", err)
	}
	if len(hits) != 1 || hits[0].LibraryID != libCur {
		t.Fatalf("身份过滤不符（应恰留当前身份行）：%+v", hits)
	}
	// 探针同口径：历史身份库对当前身份 = 无可用向量；对历史身份 = 有（证明是身份过滤而非无数据）
	if ok, err := f.store.HasVectorContent(ctx, []uint{libOld}, legTestModel); err != nil || ok {
		t.Fatalf("探针应按当前身份排除历史行: ok=%v err=%v", ok, err)
	}
	if ok, err := f.store.HasVectorContent(ctx, []uint{libOld}, legHistoryModel); err != nil || !ok {
		t.Fatalf("探针按历史身份应 true（排除来自过滤）: ok=%v err=%v", ok, err)
	}
	// 空身份机械短路（不发 `v.model = ''` 兜底查询）
	if got, err := f.store.SearchVector(ctx, []uint{libCur}, vec, "", 10, nil); err != nil || got != nil {
		t.Fatalf("空身份应零行零错: %v %v", got, err)
	}
}

// TestSearchExactBuildConditionsDB 文档在场条件真连库：
//   - 重建/换窗在途（build_state ≠ ready）但 active_build 指向旧完成 build ⇒ **仍可召回**（消除召回空洞）；
//   - 未产出 active_build（空）⇒ 不命中（含 build_hash 同为空串的边界——空串字面在 SQL 层兜住）；
//   - 旧 build 残留（build_hash ≠ active_build）⇒ 不命中。
func TestSearchExactBuildConditionsDB(t *testing.T) {
	f := newLegFixture(t)
	ctx := f.ctx

	tokenInProgress := "kblegbuild" + f.suffix + "inprogress"
	tokenUnbuilt := "kblegbuild" + f.suffix + "unbuilt"
	tokenUnbuiltEmpty := "kblegbuild" + f.suffix + "unbuiltempty"
	tokenStale := "kblegbuild" + f.suffix + "stale"

	build := fmt.Sprintf("%040s", "f5-live")
	libInProgress, chunkInProgress := f.legDoc("f5-prog",
		model.KBDocumentBuildEmbedding, build, build, "重建在途样本 "+tokenInProgress+" 尾注")
	libUnbuilt, _ := f.legDoc("f5-unbuilt",
		model.KBDocumentBuildRegistered, "", fmt.Sprintf("%040s", "f5-unbuilt"), "未建成样本 "+tokenUnbuilt+" 尾注")
	libUnbuiltEmpty, _ := f.legDoc("f5-unbuiltempty",
		model.KBDocumentBuildChunking, "", "", "未建成空串样本 "+tokenUnbuiltEmpty+" 尾注")
	libStale, _ := f.legDoc("f5-stale",
		model.KBDocumentBuildReady, fmt.Sprintf("%040s", "f5-new"), fmt.Sprintf("%040s", "f5-old"), "残留样本 "+tokenStale+" 尾注")

	search := func(libID uint, token string) []ChunkHit {
		t.Helper()
		hits, err := f.store.SearchExact(ctx, []uint{libID}, "%"+token+"%", 10, nil)
		if err != nil {
			t.Fatalf("exact 腿失败（token=%s）: %v", token, err)
		}
		return hits
	}

	// 文档在场核心：在途文档的旧完成 build 行仍可召回（早期实现在 ready 依赖下被整体摘除）
	if hits := search(libInProgress, tokenInProgress); len(hits) != 1 || hits[0].ChunkID != chunkInProgress {
		t.Fatalf("重建在途的旧完成 build 应仍可召回：%+v", hits)
	}
	// 未产出 active_build：恒不命中（两条边界：build_hash 有值 / build_hash 亦为空）
	if hits := search(libUnbuilt, tokenUnbuilt); len(hits) != 0 {
		t.Fatalf("未产出 active_build 不应命中：%+v", hits)
	}
	if hits := search(libUnbuiltEmpty, tokenUnbuiltEmpty); len(hits) != 0 {
		t.Fatalf("active_build 空串（build_hash 亦空）不应命中：%+v", hits)
	}
	// 旧 build 残留：不命中（等值条件）
	if hits := search(libStale, tokenStale); len(hits) != 0 {
		t.Fatalf("旧 build 残留行不应命中：%+v", hits)
	}
}

// TestSearchDerivedTextMatchColumnDB 匹配面 = match_text 派生文本（三面同切）：
// 词面三腿（exact/fts/trgm）一律匹配 kb_chunk.match_text（= chunk.ChunkTextOf：标题路径 + 表头 +
// verbatim 正文），不再匹配 verbatim c.text；tsv 为 DB 生成列（恒自 match_text 派生）。
//
// 用例构造（legDocDerived：text 与 matchText 分离——模拟写侧派生后的行态）：
//   - A 子节块：正文「总价为人民币壹万元整。」不含标题词，match_text = "合同标题 > 第二条 价款\n" + 正文
//     ⇒ 标题词「第二条 价款」/「价款」只在派生面；
//   - B 未回填存量行：正文含词但 match_text 为空串（迁移后未跑 aigo kb reindex 的行态）
//     ⇒ 三腿均不可达（回填步骤的必要性由本断言钉住）。
func TestSearchDerivedTextMatchColumnDB(t *testing.T) {
	f := newLegFixture(t)
	ctx := f.ctx

	build := fmt.Sprintf("%040s", "mt-live")
	body := "总价为人民币壹万元整。"
	libA, chunkA := f.legDocDerived("mt-derived", model.KBDocumentBuildReady, build, build,
		body, "合同标题 > 第二条 价款\n"+body)

	tokenLegacy := "mtlegacy" + f.suffix
	libB, _ := f.legDocDerived("mt-legacy", model.KBDocumentBuildReady, build, build,
		"未回填存量行样本 "+tokenLegacy+" 尾注", "")

	// ① exact：标题词只在 match_text ⇒ 命中 A；命中行返回列 Text 仍是 verbatim（不含标题词）
	hits, err := f.store.SearchExact(ctx, []uint{libA}, "%第二条 价款%", 10, nil)
	if err != nil {
		t.Fatalf("exact 腿失败: %v", err)
	}
	if len(hits) != 1 || hits[0].ChunkID != chunkA {
		t.Fatalf("exact 腿应按 match_text 召回派生标题词：%+v", hits)
	}
	if strings.Contains(hits[0].Text, "第二条 价款") {
		t.Fatalf("命中行 Text 应仍是 verbatim 正文（不含派生前缀）：%q", hits[0].Text)
	}
	// ② exact 反面：未回填行（match_text 空）即使正文含词也不可达（回填前边界；aigo kb reindex 回填后可达）
	if hits, err := f.store.SearchExact(ctx, []uint{libB}, "%"+tokenLegacy+"%", 10, nil); err != nil || len(hits) != 0 {
		t.Fatalf("未回填行（match_text 空）不应被 exact 命中：hits=%+v err=%v", hits, err)
	}
	// ③ fts：tsv 为生成列（自 match_text 派生）——标题词经 FTS 腿可达
	if hits, err := f.store.SearchFTS(ctx, []uint{libA}, "第二条", "simple", 10, nil); err != nil || len(hits) != 1 {
		t.Fatalf("fts 腿应按生成 tsv（自 match_text）召回标题词：hits=%+v err=%v", hits, err)
	}
	if hits, err := f.store.SearchFTS(ctx, []uint{libB}, tokenLegacy, "simple", 10, nil); err != nil || len(hits) != 0 {
		t.Fatalf("未回填行的生成 tsv 为空，不应命中：hits=%+v err=%v", hits, err)
	}
	// ④ trgm（CJK）：标题词经 word_similarity(match_text) 可达
	if hits, err := f.store.SearchTrgm(ctx, []uint{libA}, "第二条 价款", 0.3, 10, nil); err != nil || len(hits) != 1 {
		t.Fatalf("trgm 腿应按 match_text 召回标题词：hits=%+v err=%v", hits, err)
	}
	if hits, err := f.store.SearchTrgm(ctx, []uint{libB}, tokenLegacy, 0.3, 10, nil); err != nil || len(hits) != 0 {
		t.Fatalf("未回填行不应被 trgm 命中：hits=%+v err=%v", hits, err)
	}
	// ⑤ 表头重复（table 策略）：表头词「qty」只在 match_text（"name,qty\n" 前缀）不在 verbatim 正文
	libC, chunkC := f.legDocDerived("mt-header", model.KBDocumentBuildReady, build, build,
		"苹果,3\n梨,5", "name,qty\n苹果,3\n梨,5")
	if hits, err := f.store.SearchExact(ctx, []uint{libC}, "%qty%", 10, nil); err != nil || len(hits) != 1 || hits[0].ChunkID != chunkC {
		t.Fatalf("exact 腿应按 match_text 召回表头词：hits=%+v err=%v", hits, err)
	}
	if hits, err := f.store.SearchFTS(ctx, []uint{libC}, "qty", "simple", 10, nil); err != nil || len(hits) != 1 || hits[0].ChunkID != chunkC {
		t.Fatalf("fts 腿应按生成 tsv（自 match_text）召回表头词：hits=%+v err=%v", hits, err)
	}
}
