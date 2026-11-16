// plugin: kb v1.0.0
package search

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"
	"github.com/ai-go-hub/ai-go-admin/internal/infra/database"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
	repoKB "github.com/ai-go-hub/ai-go-admin/plugins/kb/repository/kb"
	svcKB "github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/chunk"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 连库测试门控（照  先例：PLUGIN_KB_DB=1 启用；未设时 DB 用例一律 SKIP——go test 仍返回 ok 属假绿，
// 不得当作隔离门禁通过）。跑法：PLUGIN_KB_DB=1 go test -count=1 -timeout=120s ./plugins/kb/service/kb/search/...
const kbTestDBEnv = "PLUGIN_KB_DB"

// chdirRepoRootSearch 切到仓库根（config/database 初始化依赖相对路径）
func chdirRepoRootSearch(t *testing.T) {
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

// searchFixture 连库夹具（自建自清；行 id 全登记，只动本用例数据）
type searchFixture struct {
	t       *testing.T
	ctx     context.Context
	db      *gorm.DB
	suffix  string
	adminID uint

	libIDs   []uint
	docIDs   []uint
	chunkIDs []uint
	grantIDs []uint
	teamIDs  []uint
	// principal user 轴主体面假体（本插件不假设宿主有用户体系：user 轴布景经端口注入，
	// 不写任何宿主用户/组表——假体实现在 service/kb 包内，见 principal_fake_test.go）
	principal *kkPrincipal
}

func newSearchFixture(t *testing.T) *searchFixture {
	t.Helper()
	if os.Getenv(kbTestDBEnv) != "1" {
		t.Skipf("需要数据库：设 %s=1 启用（库需已应用本插件的自管迁移）", kbTestDBEnv)
	}
	chdirRepoRootSearch(t)
	if err := config.Init(); err != nil {
		t.Fatalf("初始化配置: %v", err)
	}
	if err := database.Init(); err != nil {
		t.Fatalf("初始化数据库: %v", err)
	}
	requireKBTestDatabase(t, database.DB())
	applyPluginMigrations(t)
	f := &searchFixture{
		t:         t,
		ctx:       context.Background(),
		db:        database.DB(),
		suffix:    fmt.Sprintf("%d", time.Now().UnixNano()),
		adminID:   990001, // 合成后台 id（admin 轴只认 (kind,id)）
		principal: newKKPrincipal(),
	}
	svcKB.RegisterPrincipal(f.principal)
	t.Cleanup(func() { svcKB.RegisterPrincipal(nil) }) // 复原：未注册 ⇒ user 轴 fail-closed
	t.Cleanup(f.cleanup)
	return f
}

// cleanup 自清：raw DELETE 硬删登记行（含检索日志按本用例唯一 token 清）
func (f *searchFixture) cleanup() {
	ids := func(table string, list []uint) {
		if len(list) == 0 {
			return
		}
		if err := f.db.Exec("DELETE FROM "+table+" WHERE id IN ?", list).Error; err != nil {
			f.t.Logf("清理 %s 失败（请手工核）: %v", table, err)
		}
	}
	if err := f.db.Exec("DELETE FROM kb_search_log WHERE query_text LIKE ?", "%"+f.suffix+"%").Error; err != nil {
		f.t.Logf("清理 kb_search_log 失败（请手工核）: %v", err)
	}
	ids("kb_chunk", f.chunkIDs)
	ids("kb_grant", f.grantIDs)
	ids("kb_document", f.docIDs)
	ids("kb_library", f.libIDs)
	ids("kb_team", f.teamIDs)
}

func (f *searchFixture) group(name string, status uint8) *kkGroup {
	f.t.Helper()
	return f.principal.AddGroup(name, status == 1)
}

func (f *searchFixture) user(username, status string, mainGroupID uint) *kkUser {
	f.t.Helper()
	return f.principal.AddUser(username, status == "enable", mainGroupID)
}

func (f *searchFixture) team(groupName string, boundID uint, status uint8) *model.KBTeam {
	f.t.Helper()
	row := &model.KBTeam{GroupName: groupName, BoundGroupID: boundID, DisplayName: groupName, Status: &status}
	if err := gorm.G[model.KBTeam](f.db).Create(f.ctx, row); err != nil {
		f.t.Fatalf("建团队映射失败: %v", err)
	}
	f.teamIDs = append(f.teamIDs, row.ID)
	return row
}

func (f *searchFixture) library(ownerKind string, ownerID uint, name string, status uint8) *model.KBLibrary {
	f.t.Helper()
	row := &model.KBLibrary{OwnerKind: ownerKind, OwnerID: ownerID, Name: name, Status: &status}
	if err := gorm.G[model.KBLibrary](f.db).Create(f.ctx, row); err != nil {
		f.t.Fatalf("建库失败: %v", err)
	}
	f.libIDs = append(f.libIDs, row.ID)
	return row
}

func (f *searchFixture) grant(libraryID uint, subjectKind string, subjectID uint, perm string) {
	f.t.Helper()
	row := &model.KBGrant{
		LibraryID: libraryID, SubjectKind: subjectKind, SubjectID: subjectID, Perm: perm,
		GrantedByKind: model.KBActorKindAdmin, GrantedByID: f.adminID,
	}
	if err := gorm.G[model.KBGrant](f.db).Create(f.ctx, row); err != nil {
		f.t.Fatalf("建授权行失败: %v", err)
	}
	f.grantIDs = append(f.grantIDs, row.ID)
}

// docWithChunk 建文档 + 一片切片（tsv 按写侧单点 to_tsvector('simple', text) 同款生成）；
// buildState/activeBuild/staleHash 控制「就绪三连」的通过与否。
func (f *searchFixture) docWithChunk(libraryID uint, title, token, buildState, activeBuild, chunkBuild string) (*model.KBDocument, *model.KBChunk) {
	f.t.Helper()
	doc := &model.KBDocument{
		LibraryID: libraryID, Title: title, SourceKind: model.KBDocumentSourceText,
		ContentHash: chunk.TextHash(title), BuildState: buildState, ActiveBuild: activeBuild,
		CreatedByKind: model.KBActorKindAdmin, CreatedByID: f.adminID,
	}
	if err := gorm.G[model.KBDocument](f.db).Create(f.ctx, doc); err != nil {
		f.t.Fatalf("建文档失败: %v", err)
	}
	f.docIDs = append(f.docIDs, doc.ID)

	text := "冒烟检索样本 " + token + " 尾注"
	row := &model.KBChunk{
		LibraryID: libraryID, DocumentID: doc.ID, BuildHash: chunkBuild,
		Chunker: "fixed", ChunkerVersion: "v1", Seq: 0,
		Locator: fmt.Sprintf("kb:doc:%d#p1", doc.ID), CharStart: 0, CharEnd: len([]rune(text)),
		Text: text, TextHash: chunk.TextHash(text),
	}
	if err := gorm.G[model.KBChunk](f.db).Create(f.ctx, row); err != nil {
		f.t.Fatalf("建切片失败: %v", err)
	}
	f.chunkIDs = append(f.chunkIDs, row.ID)
	// 匹配面列 match_text（exact/fts/trgm 匹配 match_text，tsv 为 DB 生成列自其派生；
	// 本夹具无标题/表头派生前缀 ⇒ match_text = verbatim 文本）
	if err := f.db.Exec("UPDATE kb_chunk SET match_text = text WHERE id = ?", row.ID).Error; err != nil {
		f.t.Fatalf("回填 match_text 失败: %v", err)
	}
	return doc, row
}

// readyDoc 就绪文档 + 当前 build 切片（active_build = chunk.build_hash）
func (f *searchFixture) readyDoc(libraryID uint, title, token string) (*model.KBDocument, *model.KBChunk) {
	f.t.Helper()
	build := chunk.TextHash("build-" + f.suffix + title)[:40]
	return f.docWithChunk(libraryID, title, token, model.KBDocumentBuildReady, build, build)
}

// 小工具：命中集判定
func itemLibraryIDs(res *Result) map[uint]bool {
	out := map[uint]bool{}
	for _, it := range res.Items {
		out[it.LibraryID] = true
	}
	return out
}

func itemChunkIDs(res *Result) map[uint]bool {
	out := map[uint]bool{}
	for _, it := range res.Items {
		out[it.ChunkID] = true
	}
	return out
}

// ==================== 隔离对抗（门禁：跨库/跨团队/私有库检索 = 空集 fail-closed） ====================

// TestSearchIsolationDB 检索面越权对抗 + 门禁（就绪/build 三连）+ 降级回显 + 日志落行：
//
//	① 跨库：owner A 的检索结果只含自有库块（不含他人库块）；他人发起则反之；
//	② 跨团队未授权（无组籍/未受授）：空集 fail-closed；
//	③ 私有库（他人无 grant）：对非属主恒不可命；
//	④ 未就绪文档 / 旧 build 残留行：不可召回（文档就绪三连下推）；
//	⑤ admin 轴：全可读（含三类库）——管理面口径；
//	⑥ 单腿失败 degraded[] 回显（注入嵌入失败）；无向量内容 auto 自动降级词面；
//	⑦ 检索日志 best-effort 落行（user_id/result_count）；腿 SQL 捕获含 library_id 第一条件。
func TestSearchIsolationDB(t *testing.T) {
	f := newSearchFixture(t)
	token := "kbsearch" + f.suffix

	gTeam := f.group("kb-search-team-"+f.suffix, 1)
	uOwner := f.user("kb-search-owner-"+f.suffix, "enable", 0)
	uOther := f.user("kb-search-other-"+f.suffix, "enable", 0)
	uMember := f.user("kb-search-member-"+f.suffix, "enable", gTeam.ID)
	uNobody := f.user("kb-search-nobody-"+f.suffix, "enable", 0)

	libOwn := f.library(model.KBLibraryOwnerKindUser, uOwner.ID, "kb-search-lib-own-"+f.suffix, 1)
	libPriv := f.library(model.KBLibraryOwnerKindUser, uOther.ID, "kb-search-lib-private-"+f.suffix, 1)
	libTeam := f.library(model.KBLibraryOwnerKindUser, uOther.ID, "kb-search-lib-team-"+f.suffix, 1)
	team := f.team(gTeam.Name, gTeam.ID, 1)
	f.grant(libTeam.ID, model.KBGrantSubjectTeam, team.ID, model.KBGrantPermRead)

	_, ownChunk := f.readyDoc(libOwn.ID, "kb-search-doc-own-"+f.suffix, token)
	_, privChunk := f.readyDoc(libPriv.ID, "kb-search-doc-private-"+f.suffix, token)
	_, teamChunk := f.readyDoc(libTeam.ID, "kb-search-doc-team-"+f.suffix, token)
	// 未就绪文档的切片（内容含 token，但 build_state≠ready ⇒ 不可召回）
	_, pendingChunk := f.docWithChunk(libOwn.ID, "kb-search-doc-pending-"+f.suffix, token,
		model.KBDocumentBuildRegistered, "", chunk.TextHash("pending-"+f.suffix))
	// 旧 build 残留行（文档 ready，active_build 指向 new；残留行 build_hash = 旧 ⇒ 不可召回）
	_, staleChunk := f.docWithChunk(libOwn.ID, "kb-search-doc-stale-"+f.suffix, token,
		model.KBDocumentBuildReady, chunk.TextHash("new-"+f.suffix), chunk.TextHash("stale-"+f.suffix))

	core := svcKB.NewService()
	cache := newQueryVecCache()
	cache.loaded = true // 纯内存（不落 /tmp 文件）
	capture := &sqlCaptureLogger{}
	session := f.db.Session(&gorm.Session{Logger: capture})
	svc := NewServiceWith(session, core, repoKB.NewStore().WithDB(session), cache)

	viewOwner, err := core.UserView(f.ctx, uOwner.ID)
	if err != nil {
		t.Fatalf("owner 视图解析失败: %v", err)
	}
	viewOther, err := core.UserView(f.ctx, uOther.ID)
	if err != nil {
		t.Fatalf("other 视图解析失败: %v", err)
	}
	viewMember, err := core.UserView(f.ctx, uMember.ID)
	if err != nil {
		t.Fatalf("member 视图解析失败: %v", err)
	}
	viewNobody, err := core.UserView(f.ctx, uNobody.ID)
	if err != nil {
		t.Fatalf("nobody 视图解析失败: %v", err)
	}
	viewAdmin, err := core.AdminView(f.ctx, f.adminID)
	if err != nil {
		t.Fatalf("admin 视图解析失败: %v", err)
	}

	// ① owner：只见自有库块；未就绪/旧 build 残留不可召回
	res, err := svc.Search(f.ctx, viewOwner, Options{Query: token, Mode: ModeLexical, TopK: 20})
	if err != nil {
		t.Fatalf("owner 检索失败: %v", err)
	}
	libs, chunks := itemLibraryIDs(res), itemChunkIDs(res)
	if !chunks[ownChunk.ID] {
		t.Fatalf("owner 未召回自有库块（chunk=%d）：%+v", ownChunk.ID, res.Items)
	}
	if libs[libPriv.ID] || libs[libTeam.ID] {
		t.Fatalf("owner 越权召回了他人库：%v", libs)
	}
	if chunks[pendingChunk.ID] || chunks[staleChunk.ID] {
		t.Fatalf("未就绪/旧 build 残留行进了召回：%v", chunks)
	}

	// ② owner 的库对他人（无 grant）不可命：uOther 检索只见自有两库
	resOther, err := svc.Search(f.ctx, viewOther, Options{Query: token, Mode: ModeLexical, TopK: 20})
	if err != nil {
		t.Fatalf("other 检索失败: %v", err)
	}
	libsOther := itemLibraryIDs(resOther)
	if libsOther[libOwn.ID] {
		t.Fatalf("跨库越权：other 命中 owner 的库：%v", libsOther)
	}
	if len(libsOther) != 2 || !libsOther[libPriv.ID] || !libsOther[libTeam.ID] {
		t.Fatalf("other 自有两库未全命中：%v", libsOther)
	}

	// ③ 跨团队受授（组籍成员）命中 libTeam；不含他人私有库
	resMember, err := svc.Search(f.ctx, viewMember, Options{Query: token, Mode: ModeLexical, TopK: 20})
	if err != nil {
		t.Fatalf("member 检索失败: %v", err)
	}
	libsMember, chunksMember := itemLibraryIDs(resMember), itemChunkIDs(resMember)
	if !chunksMember[teamChunk.ID] || libsMember[libOwn.ID] || libsMember[libPriv.ID] {
		t.Fatalf("团队授权命中面不合规：libs=%v chunks=%v", libsMember, chunksMember)
	}

	// ④ 跨库/跨团队/私有库 = 空集 fail-closed（无任何组籍与授权）
	resNobody, err := svc.Search(f.ctx, viewNobody, Options{Query: token, Mode: ModeAuto, TopK: 20})
	if err != nil {
		t.Fatalf("nobody 检索失败: %v", err)
	}
	if len(resNobody.Items) != 0 {
		t.Fatalf("无授权主体应得空集，得到 %d 条：%+v", len(resNobody.Items), resNobody.Items)
	}

	// ⑤ admin 轴：三库就绪块全见；未就绪/旧 build 残留仍不可召回
	resAdmin, err := svc.Search(f.ctx, viewAdmin, Options{Query: token, Mode: ModeLexical, TopK: 20})
	if err != nil {
		t.Fatalf("admin 检索失败: %v", err)
	}
	libsAdmin, chunksAdmin := itemLibraryIDs(resAdmin), itemChunkIDs(resAdmin)
	if !chunksAdmin[ownChunk.ID] || !chunksAdmin[privChunk.ID] || !chunksAdmin[teamChunk.ID] {
		t.Fatalf("admin 未全见三类库：%v", chunksAdmin)
	}
	if len(libsAdmin) != 3 {
		t.Fatalf("admin 可见库数 = %d，期望 3：%v", len(libsAdmin), libsAdmin)
	}
	if chunksAdmin[pendingChunk.ID] || chunksAdmin[staleChunk.ID] {
		t.Fatalf("admin 轴未按就绪三连收敛：%v", chunksAdmin)
	}

	// ⑥ auto：可读域无向量内容 ⇒ 自动降级词面（腿集不含 vector；词面结果仍在）
	resAuto, err := svc.Search(f.ctx, viewOwner, Options{Query: token, Mode: ModeAuto, TopK: 20})
	if err != nil {
		t.Fatalf("auto 检索失败: %v", err)
	}
	if len(resAuto.Legs) == 0 || len(resAuto.Items) == 0 {
		t.Fatalf("auto 回执不合规：legs=%v items=%d", resAuto.Legs, len(resAuto.Items))
	}
	for _, st := range resAuto.Legs {
		if st.Name == LegVector {
			t.Fatalf("无向量内容时 auto 未降级词面：%+v", resAuto.Legs)
		}
	}

	// ⑦ 单腿失败 degraded[] 回显：hybrid + 注入嵌入失败 ⇒ 词面结果仍在、degraded 点名 vector
	svcFail := NewServiceWith(session, core, repoKB.NewStore().WithDB(session), cache)
	svcFail.embedQuery = func(context.Context, string) ([]float32, error) {
		return nil, errors.New("冒烟注入：嵌入不可用")
	}
	resFail, err := svcFail.Search(f.ctx, viewOwner, Options{Query: token, Mode: ModeHybrid, TopK: 20})
	if err != nil {
		t.Fatalf("单腿失败不应整查失败: %v", err)
	}
	if len(resFail.Items) == 0 {
		t.Fatal("单腿失败后词面结果应保留")
	}
	found := false
	for _, d := range resFail.Degraded {
		if strings.HasPrefix(d, LegVector+":") {
			found = true
		}
	}
	if !found {
		t.Fatalf("degraded 未点名 vector：%v", resFail.Degraded)
	}

	// ⑧ top_k 截断回显 + 腿窗上限（NormalizeTopK / NormalizeBrowseLimit 已单测；此处验端到端生效值）
	//    （要求 3 条 > 实际命中数时按 50 帽以下原值回显）
	resCap, err := svc.Search(f.ctx, viewOwner, Options{Query: token, Mode: ModeLexical, TopK: 1000, Limit: 5000})
	if err != nil {
		t.Fatalf("上限检索失败: %v", err)
	}
	if resCap.TopK != MaxTopK || resCap.Limit != svcKB.MaxBrowseLimit {
		t.Fatalf("上限回显 = top_k %d / limit %d（期望 50 / 100）", resCap.TopK, resCap.Limit)
	}

	// ⑨ 逐字核对入口：owner 可取（text 逐字 + text_hash 可复算）；他人/不存在/未就绪同型拒
	detail, err := svc.GetChunk(f.ctx, viewOwner, ownChunk.ID)
	if err != nil {
		t.Fatalf("owner 取切片失败: %v", err)
	}
	if detail.Text != ownChunk.Text || detail.TextHash != chunk.TextHash(detail.Text) {
		t.Fatalf("逐字核对不平：text=%q hash=%s", detail.Text, detail.TextHash)
	}
	if loc := detail.Locator; loc.Anchor != ownChunk.Locator || loc.Kind != "file" {
		t.Fatalf("定位结构不合规：%+v", loc)
	}
	if _, err := svc.GetChunk(f.ctx, viewOther, ownChunk.ID); !errors.Is(err, svcKB.ErrDenied) {
		t.Fatalf("跨用户取切片应同型拒，得到: %v", err)
	}
	if _, err := svc.GetChunk(f.ctx, viewOwner, 987654321); !errors.Is(err, svcKB.ErrDenied) {
		t.Fatalf("不存在切片应同型拒，得到: %v", err)
	}
	if _, err := svc.GetChunk(f.ctx, viewOwner, pendingChunk.ID); !errors.Is(err, svcKB.ErrDenied) {
		t.Fatalf("未就绪文档切片应同型拒，得到: %v", err)
	}

	// ⑩ 检索日志 best-effort 落行（user 轴填 users.id；result_count 与回执一致）
	var logCount int64
	if err := f.db.Table("kb_search_log").
		Where("user_id = ? AND result_count = ?", uOwner.ID, len(res.Items)).
		Count(&logCount).Error; err != nil {
		t.Fatalf("检索日志计数失败: %v", err)
	}
	if logCount < 1 {
		t.Fatalf("检索日志未落行（user_id=%d result_count=%d）", uOwner.ID, len(res.Items))
	}

	// ⑪ scoped 直取不可读库 ⇒ 同型拒（可读集 membership 单点口径：admin scoped 到软删库、
	//    user scoped 到他人私有库都不放行——实测抓到 admin 轴 RequireRead 恒真缺口）
	delLib := f.library(model.KBLibraryOwnerKindUser, uOwner.ID, "kb-search-lib-deleted-"+f.suffix, 1)
	delToken := "kbsearch" + f.suffix + "delonly"
	_, delChunk := f.readyDoc(delLib.ID, "kb-search-doc-deleted-"+f.suffix, delToken)
	if err := f.db.Exec("UPDATE kb_library SET deleted_at = NOW() WHERE id = ?", delLib.ID).Error; err != nil {
		t.Fatalf("软删库失败: %v", err)
	}
	if _, err := svc.Search(f.ctx, viewAdmin, Options{Query: token, LibraryID: delLib.ID}); !errors.Is(err, svcKB.ErrDenied) {
		t.Fatalf("admin scoped 到软删库应同型拒，得到: %v", err)
	}
	if _, err := svc.Search(f.ctx, viewOwner, Options{Query: token, LibraryID: libPriv.ID}); !errors.Is(err, svcKB.ErrDenied) {
		t.Fatalf("user scoped 到他人私有库应同型拒，得到: %v", err)
	}
	resDel, err := svc.Search(f.ctx, viewAdmin, Options{Query: delToken, Mode: ModeLexical, TopK: 20})
	if err != nil {
		t.Fatalf("软删库 token 检索失败: %v", err)
	}
	if _, hit := itemChunkIDs(resDel)[delChunk.ID]; hit {
		t.Fatalf("软删库内容进了召回：%v", itemChunkIDs(resDel))
	}

	// ⑫ 腿 SQL 捕获：每条腿必带 library_id 第一条件（可读集下推）；禁 SELECT *
	sqls := capture.all()
	joined := strings.Join(sqls, "\n")
	// 可读集下推展开后的 SQL 形态：c.library_id IN ($N, ...)（GORM 对 IN ? 的展开）
	if !strings.Contains(joined, "c.library_id IN (") {
		t.Fatalf("腿 SQL 未见可读库集第一条件：%s", joined)
	}
	if strings.Contains(joined, "SELECT *") {
		t.Fatalf("腿 SQL 出现 SELECT *（禁）")
	}
	t.Logf("捕获 SQL（%d 条；含可读集下推）", len(sqls))
}

// ==================== 保留期清理（kb:prune 作业主体；保留期 180 天） ====================

// TestPruneSearchLogDB 保留期清理：超期行删、窗内行留；分批删可重入（再跑删 0）。
func TestPruneSearchLogDB(t *testing.T) {
	f := newSearchFixture(t)
	svc := &Service{db: f.db, now: time.Now}

	oldToken := "smokekbsearch" + f.suffix + "pruneold"
	newToken := "smokekbsearch" + f.suffix + "prunenew"
	if err := f.db.Exec("INSERT INTO kb_search_log (user_id, mode, legs, query_hash, query_text, result_count, latency_ms, degraded, request_id, created_at) VALUES (0, 'auto', '[]', '', ?, 0, 0, '[]', 'prune-test', NOW() - interval '200 days')", oldToken).Error; err != nil {
		t.Fatalf("造超期日志行失败: %v", err)
	}
	if err := f.db.Exec("INSERT INTO kb_search_log (user_id, mode, legs, query_hash, query_text, result_count, latency_ms, degraded, request_id, created_at) VALUES (0, 'auto', '[]', '', ?, 0, 0, '[]', 'prune-test', NOW())", newToken).Error; err != nil {
		t.Fatalf("造窗内日志行失败: %v", err)
	}

	deleted, err := svc.PruneSearchLog(f.ctx, 180)
	if err != nil {
		t.Fatalf("保留期清理失败: %v", err)
	}
	if deleted < 1 {
		t.Fatalf("超期行未被删（deleted=%d）", deleted)
	}
	var oldLeft, newLeft int64
	if err := f.db.Table("kb_search_log").Where("query_text = ?", oldToken).Count(&oldLeft).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Table("kb_search_log").Where("query_text = ?", newToken).Count(&newLeft).Error; err != nil {
		t.Fatal(err)
	}
	if oldLeft != 0 || newLeft != 1 {
		t.Fatalf("清理口径不符：超期残留=%d 窗内保留=%d", oldLeft, newLeft)
	}
	// 可重入：再跑一次删 0
	again, err := svc.PruneSearchLog(f.ctx, 180)
	if err != nil || again != 0 {
		t.Fatalf("清理不幂等：deleted=%d err=%v", again, err)
	}
}

// ==================== SQL 捕获 logger（无第三方依赖最小实现；照  acl_sql_test.go） ====================

type sqlCaptureLogger struct {
	mu   sync.Mutex
	sqls []string
}

func (l *sqlCaptureLogger) LogMode(logger.LogLevel) logger.Interface { return l }
func (l *sqlCaptureLogger) Info(context.Context, string, ...any)     {}
func (l *sqlCaptureLogger) Warn(context.Context, string, ...any)     {}
func (l *sqlCaptureLogger) Error(context.Context, string, ...any)    {}
func (l *sqlCaptureLogger) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	l.mu.Lock()
	l.sqls = append(l.sqls, sql)
	l.mu.Unlock()
}

func (l *sqlCaptureLogger) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.sqls...)
}
