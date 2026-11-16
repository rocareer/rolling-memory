// plugin: kb v1.0.0
// 核心闭环冒烟（DB 门控；**无 AI 密钥可全程走通**的那条链）：
//
//	建库 → 导入一段文本 → 作业面切片（kb:ingest）→ 词面三腿检索命中 → 逐字核对一致
//	→ 文档级重建 → 取消 → 失败清单 → 删文档（软删 + 清理入队）→ 运维巡检读数。
//
// 为什么用**外置测试包**（package kb_test）：本用例要同时用服务层（建库/导入/重建/取消）与检索层
// （search 包），而 search 包本身依赖服务层 ⇒ 同包测试（package kb）里 import search 会构成 import cycle。
// 外置测试包不参与包依赖图，两处都能用；代价是夹具自持（见下）。
//
// 口径（缺省档，勿改）：
//   - 嵌入档缺省关（kb.embed.enabled=false）⇒ 检索 auto 档**直接走词面腿**、degraded 恒为空
//     （不得为「让 degraded 非空」打开嵌入档：那需要密钥且会改掉出厂缺省）；
//   - 本用例只断言词面链；嵌入 / 语义候选 / 媒体云渠道在无密钥环境**未验证**（如实登记，不以代码在位当通过）。
//
// 跑法（隔离库，库名须含 kb_test）：
//
//	createdb -h <主机> -p <端口> -U <用户> aigo_kb_test
//	DATABASE_WRITE_DBNAME=aigo_kb_test PLUGIN_KB_DB=1 go -C ai-go-admin test -count=1 -p 1 -run TestE2ESmokeDB ./plugins/kb/service/kb/
package kb_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"
	"github.com/ai-go-hub/ai-go-admin/internal/infra/database"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/cli"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"
	model "github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/service/jobs"
	svcKB "github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb"
	kbsearch "github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/search"

	"gorm.io/gorm"
)

// e2eFixture 自持夹具（库级用例的统一门控 + 库名守卫 + 插件迁移 + 登记行清理）
type e2eFixture struct {
	t      *testing.T
	ctx    context.Context
	db     *gorm.DB
	svc    *svcKB.Service
	suffix string

	adminID uint
	libIDs  []uint
	docIDs  []uint
}

// TestE2ESmokeDB 核心闭环（词面腿）
func TestE2ESmokeDB(t *testing.T) {
	f := newE2EFixture(t)
	actor := svcKB.Actor{Kind: model.KBActorKindAdmin, ID: f.adminID}
	view := mustView(t, f)

	lib, err := f.svc.CreateLibrary(f.ctx, actor, svcKB.LibraryInput{
		OwnerKind: model.KBLibraryOwnerKindSystem,
		Name:      "kb-e2e-lib-" + f.suffix,
	})
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	f.libIDs = append(f.libIDs, lib.ID)

	body := "# 核心闭环冒烟\n\n知识库插件闭环探针：词面腿应当命中本句。\n"
	res, err := f.svc.ImportDocument(f.ctx, actor, svcKB.ImportInput{
		LibraryID: lib.ID, Name: "kb-e2e-doc-" + f.suffix, Text: body,
	})
	if err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	f.docIDs = append(f.docIDs, res.DocumentID)

	waitDocumentReady(t, f, view, res.DocumentID)

	// 词面检索（auto 档；缺省嵌入档关 ⇒ 走词面腿、degraded 恒空）
	searchSvc := kbsearch.NewService(f.svc)
	result, err := searchSvc.Search(f.ctx, view, kbsearch.Options{
		Query:     "词面腿应当命中",
		Mode:      kbsearch.ModeAuto,
		LibraryID: lib.ID,
		TopK:      5,
	})
	if err != nil {
		t.Fatalf("检索失败: %v", err)
	}
	if len(result.Items) == 0 {
		t.Fatalf("词面检索应有命中：legs=%+v degraded=%v", result.Legs, result.Degraded)
	}
	if len(result.Degraded) != 0 {
		t.Fatalf("缺省嵌入档关 ⇒ degraded 必须为空，实际 %v", result.Degraded)
	}
	if !hasLexicalLeg(result.Legs) {
		t.Fatalf("词面腿（exact/fts/trgm）至少应有一条命中：%+v", result.Legs)
	}

	// 逐字核对：切片正文 verbatim，含导入文本里的整句
	chunk, err := searchSvc.GetChunk(f.ctx, view, result.Items[0].ChunkID)
	if err != nil {
		t.Fatalf("读切片失败: %v", err)
	}
	if !strings.Contains(chunk.Text, "词面腿应当命中") {
		t.Fatalf("逐字核对失败：切片正文未含导入句（text=%q）", chunk.Text)
	}

	// 重建 → 取消 → 失败清单
	rebuild, err := f.svc.RebuildDocument(f.ctx, actor, res.DocumentID)
	if err != nil {
		t.Fatalf("重建投递失败: %v", err)
	}
	if rebuild.JobID == 0 {
		t.Fatalf("重建应回执作业：%+v", rebuild)
	}
	if _, err := f.svc.CancelDocument(f.ctx, actor, res.DocumentID); err != nil {
		t.Fatalf("取消失败: %v", err)
	}
	failed, total, err := f.svc.ListFailedDocuments(f.ctx, view, lib.ID, svcKB.DocumentFailedOptions{Page: 1, Limit: 10})
	if err != nil {
		t.Fatalf("失败清单失败: %v", err)
	}
	if total != int64(len(failed)) {
		t.Fatalf("失败清单 total=%d 与行数 %d 不一致", total, len(failed))
	}

	// 运维巡检（干净读数：无团队授权 ⇒ 无悬空授权）
	report, err := f.svc.CheckTeams(f.ctx)
	if err != nil {
		t.Fatalf("巡检失败: %v", err)
	}
	if len(report.OrphanTeamGrants) != 0 {
		t.Fatalf("本用例未建团队授权 ⇒ 不应有悬空授权：%+v", report.OrphanTeamGrants)
	}

	// 删文档（软删即刻）
	if err := f.svc.DeleteDocument(f.ctx, actor, res.DocumentID); err != nil {
		t.Fatalf("删文档失败: %v", err)
	}
	if _, err := f.svc.GetDocument(f.ctx, view, res.DocumentID); err == nil {
		t.Fatal("软删后文档应对一切面不可见")
	}
}

// newE2EFixture 连库夹具（门控 + 库名守卫 + 插件迁移 + 台账置 enabled + 自清）
func newE2EFixture(t *testing.T) *e2eFixture {
	t.Helper()
	if os.Getenv("PLUGIN_KB_DB") != "1" {
		t.Skip("需要数据库：设 PLUGIN_KB_DB=1 启用（隔离库，库名须含 kb_test）")
	}
	chdirRepoRootE2E(t)
	if err := config.Init(); err != nil {
		t.Fatalf("初始化配置: %v", err)
	}
	if err := database.Init(); err != nil {
		t.Fatalf("初始化数据库: %v", err)
	}
	db := database.DB()
	requireKBTestDatabaseE2E(t, db)
	env, err := cli.NewEnv()
	if err != nil {
		t.Fatalf("构造迁移环境失败: %v", err)
	}
	if _, err := cli.ApplyPluginMigrations(context.Background(), env, "kb", "plugins/kb/migrations"); err != nil {
		t.Fatalf("应用插件迁移失败（隔离库需允许建扩展 pg_trgm/vector）: %v", err)
	}

	f := &e2eFixture{
		t:       t,
		ctx:     context.Background(),
		db:      db,
		svc:     svcKB.NewService(),
		suffix:  time.Now().Format("150405.000000000"),
		adminID: 990001, // 合成后台 id（admin 轴只认 (kind,id)；节点授权是 HTTP 侧前置）
	}
	// 作业面启用（worker 每轮自查台账）——只建自己这一行，结束时删掉
	if err := db.Exec(`INSERT INTO admin_plugins (name, title, version, kind, status) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (name) DO UPDATE SET status = EXCLUDED.status`,
		jobs.PluginName, "知识库", "1.0.0", "code", host.StatusEnabled).Error; err != nil {
		t.Fatalf("写台账行失败: %v", err)
	}
	t.Cleanup(f.cleanup)
	return f
}

// cleanup 自清：软/硬删本用例建的库与文档行 + 台账行
func (f *e2eFixture) cleanup() {
	if len(f.docIDs) > 0 {
		if err := f.db.Exec("DELETE FROM kb_audit_log WHERE document_id IN ?", f.docIDs).Error; err != nil {
			f.t.Logf("清理审计失败（请手工核）: %v", err)
		}
		if err := f.db.Exec("DELETE FROM kb_chunk WHERE document_id IN ?", f.docIDs).Error; err != nil {
			f.t.Logf("清理切片失败（请手工核）: %v", err)
		}
		if err := f.db.Exec("DELETE FROM kb_document WHERE id IN ?", f.docIDs).Error; err != nil {
			f.t.Logf("清理文档失败（请手工核）: %v", err)
		}
	}
	if len(f.libIDs) > 0 {
		if err := f.db.Exec("DELETE FROM kb_library WHERE id IN ?", f.libIDs).Error; err != nil {
			f.t.Logf("清理库失败（请手工核）: %v", err)
		}
	}
	if err := f.db.Exec("DELETE FROM admin_plugins WHERE name = ?", jobs.PluginName).Error; err != nil {
		f.t.Logf("清理台账行失败（请手工核）: %v", err)
	}
}

// waitDocumentReady 有界轮询：每轮领一条作业，直到文档就绪或超轮次（不空等：轮次上限即退出条件）
func waitDocumentReady(t *testing.T, f *e2eFixture, view *svcKB.ReadView, documentID uint) {
	t.Helper()
	const maxRounds = 20
	for i := 0; i < maxRounds; i++ {
		doc, err := f.svc.GetDocument(f.ctx, view, documentID)
		if err != nil {
			t.Fatalf("读文档失败: %v", err)
		}
		switch doc.BuildState {
		case model.KBDocumentBuildReady:
			return
		case model.KBDocumentBuildFailed:
			t.Fatalf("文档构建失败（stage=%s reason=%s）", doc.FailedStage, doc.FailedReason)
		}
		if _, err := jobs.RunOnce(f.ctx); err != nil {
			t.Fatalf("作业面执行失败: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	doc, _ := f.svc.GetDocument(f.ctx, view, documentID)
	t.Fatalf("文档在 %d 轮内未就绪（当前态 %q）", maxRounds, doc.BuildState)
}

// mustView 后台读视图
func mustView(t *testing.T, f *e2eFixture) *svcKB.ReadView {
	t.Helper()
	view, err := f.svc.AdminView(f.ctx, f.adminID)
	if err != nil {
		t.Fatalf("构造 admin 读视图失败: %v", err)
	}
	return view
}

// hasLexicalLeg 词面三腿至少一条命中（exact/fts/trgm）
func hasLexicalLeg(legs []kbsearch.LegStat) bool {
	for _, leg := range legs {
		switch leg.Name {
		case kbsearch.LegExact, kbsearch.LegFTS, kbsearch.LegTrgm:
			if leg.Hits > 0 {
				return true
			}
		}
	}
	return false
}

// chdirRepoRootE2E 切到仓库根（config/database 初始化依赖相对路径）
func chdirRepoRootE2E(t *testing.T) {
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

// requireKBTestDatabaseE2E 库名守卫（**硬闸**：误连开发库会真删知识库表行，故不符即中止）
func requireKBTestDatabaseE2E(t *testing.T, db *gorm.DB) {
	t.Helper()
	if db == nil {
		t.Fatalf("数据库未初始化")
	}
	var name string
	if err := db.Raw("SELECT current_database()").Scan(&name).Error; err != nil {
		t.Fatalf("读取当前库名失败: %v", err)
	}
	if !strings.Contains(name, "kb_test") {
		t.Fatalf("库级用例只允许在隔离库上跑（库名须含 kb_test），当前库 = %q", name)
	}
}
