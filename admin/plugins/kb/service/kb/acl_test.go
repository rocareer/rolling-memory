// plugin: kb v1.0.0
package kb

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"
	"github.com/ai-go-hub/ai-go-admin/internal/infra/database"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"

	"gorm.io/gorm"
)

// 连库测试门控（连库用例口径）：PLUGIN_KB_DB=1 启用；前置 = PostgreSQL 可连 + 已应用本插件的自管迁移。
// 跑法（** 门禁命令必须带 PLUGIN_KB_DB=1**）：PLUGIN_KB_DB=1 go test -count=1 -timeout=120s ./plugins/kb/service/kb/...
// 未设 PLUGIN_KB_DB 时本文件的 DB 用例一律 SKIP（go test 仍返回 ok）——不得把这种 ok 当作隔离/ACL 门禁通过；
// 无库环境只跑纯单元目标：go test -count=1 -run 'TestACLLimitsUnit' ./service/kb/
const kbTestDBEnv = "PLUGIN_KB_DB"

// chdirRepoRootKB 切到仓库根（config/database 初始化依赖相对路径；照连库用例惯例）
func chdirRepoRootKB(t *testing.T) {
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

// ==================== 夹具（自建自清；行 id 全登记，绝不动非本用例数据） ====================

// kbFixture 连库夹具
type kbFixture struct {
	t       *testing.T
	ctx     context.Context
	db      *gorm.DB
	svc     *Service
	suffix  string
	adminID uint

	libIDs   []uint
	docIDs   []uint
	grantIDs []uint
	teamIDs  []uint

	// principal user 轴主体面假体（本插件不假设宿主有用户体系：user 轴布景经端口注入，
	// 不写任何宿主用户/组表——见 principal_fake_test.go）
	principal *fakePrincipal
}

// testGroup 假体组视图（用例只用到 ID / Name）
type testGroup struct {
	ID   uint
	Name string
}

// testUser 假体用户视图（用例只用到 ID）
type testUser struct {
	ID uint
}

func newKBFixture(t *testing.T) *kbFixture {
	t.Helper()
	if os.Getenv(kbTestDBEnv) != "1" {
		t.Skipf("需要数据库：设 %s=1 启用（库需已应用本插件的自管迁移）", kbTestDBEnv)
	}
	chdirRepoRootKB(t)
	if err := config.Init(); err != nil {
		t.Fatalf("初始化配置: %v", err)
	}
	if err := database.Init(); err != nil {
		t.Fatalf("初始化数据库: %v", err)
	}
	requireKBTestDatabase(t, database.DB())
	applyPluginMigrations(t)

	f := &kbFixture{
		t:       t,
		ctx:     context.Background(),
		db:      database.DB(),
		suffix:  fmt.Sprintf("%d", time.Now().UnixNano()),
		adminID: 990001, // 合成后台 id（admin 轴只认 (kind,id)；节点授权是 HTTP 侧前置）
	}
	f.svc = NewService()
	f.principal = newFakePrincipal()
	RegisterPrincipal(f.principal)
	t.Cleanup(func() { RegisterPrincipal(nil) }) // 复原：未注册 ⇒ user 轴 fail-closed
	t.Cleanup(f.cleanup)
	return f
}

// cleanup 自清：硬删本用例登记的一切行（raw DELETE 绕过软删语义，仅测试清理用；
// kb_audit_log 无软删、主键 id 与 library_id/document_id 是不同域——按**登记的业务键**清
// （library_id/document_id IN 本用例集合），绝不拿库 id 当审计主键删（会清不掉本用例行并误删无关行））。
func (f *kbFixture) cleanup() {
	hardDelete := func(table string, ids []uint) {
		if len(ids) == 0 {
			return
		}
		if err := f.db.Exec("DELETE FROM "+table+" WHERE id IN ?", ids).Error; err != nil {
			f.t.Logf("清理 %s 失败（请手工核）: %v", table, err)
		}
	}
	hardDelete("kb_grant", f.grantIDs)
	if len(f.libIDs) > 0 || len(f.docIDs) > 0 {
		if err := f.db.Exec("DELETE FROM kb_audit_log WHERE library_id IN ? OR document_id IN ?", f.libIDs, f.docIDs).Error; err != nil {
			f.t.Logf("清理 kb_audit_log 失败（请手工核）: %v", err)
		}
	}
	hardDelete("kb_document", f.docIDs)
	hardDelete("kb_library", f.libIDs)
	hardDelete("kb_team", f.teamIDs)
}

// group 建组（status：0=禁用，1=启用）——经 user 轴主体面假体登记（不写宿主组表）
func (f *kbFixture) group(name string, status uint8) *testGroup {
	f.t.Helper()
	g := f.principal.addGroup(name, status == 1)
	return &testGroup{ID: g.ID, Name: g.Name}
}

// user 建用户（status："enable"/"disable"；mainGroupID=0 表示无主分组）——经假体登记
func (f *kbFixture) user(username, status string, mainGroupID uint) *testUser {
	f.t.Helper()
	u := f.principal.addUser(username, status == "enable", mainGroupID)
	return &testUser{ID: u.ID}
}

// access 附加组关系（假体内登记）
func (f *kbFixture) access(userID, groupID uint) {
	f.t.Helper()
	f.principal.addAccess(userID, groupID)
}

// hardDeleteGroup 硬删组（模拟组改名/删除导致的绑定悬空）
func (f *kbFixture) hardDeleteGroup(groupID uint) {
	f.t.Helper()
	f.principal.deleteGroup(groupID)
}

// team 建团队映射（groupName ↔ 组名绑定；boundID=绑定时最早命中 id）
func (f *kbFixture) team(groupName string, boundID uint, status uint8) *model.KBTeam {
	f.t.Helper()
	row := &model.KBTeam{GroupName: groupName, BoundGroupID: boundID, DisplayName: groupName, Status: &status}
	if err := gorm.G[model.KBTeam](f.db).Create(f.ctx, row); err != nil {
		f.t.Fatalf("建团队映射失败: %v", err)
	}
	f.teamIDs = append(f.teamIDs, row.ID)
	return row
}

// library 后台代建库（直接落库：本用例聚焦隔离面，建库动作本身另有断言）
func (f *kbFixture) library(ownerKind string, ownerID uint, name string, status uint8) *model.KBLibrary {
	f.t.Helper()
	row := &model.KBLibrary{OwnerKind: ownerKind, OwnerID: ownerID, Name: name, Status: &status}
	if err := gorm.G[model.KBLibrary](f.db).Create(f.ctx, row); err != nil {
		f.t.Fatalf("建库失败: %v", err)
	}
	f.libIDs = append(f.libIDs, row.ID)
	return row
}

// document 建文档（active_build 留空=未产出）
func (f *kbFixture) document(libraryID uint, title string) *model.KBDocument {
	f.t.Helper()
	row := &model.KBDocument{
		LibraryID:  libraryID,
		Title:      title,
		SourceKind: model.KBDocumentSourceText,
		BuildState: model.KBDocumentBuildRegistered,
	}
	if err := gorm.G[model.KBDocument](f.db).Create(f.ctx, row); err != nil {
		f.t.Fatalf("建文档失败: %v", err)
	}
	f.docIDs = append(f.docIDs, row.ID)
	return row
}

// grant 直接落授权行（面板动作另有断言）
func (f *kbFixture) grant(libraryID uint, subjectKind string, subjectID uint, perm string) *model.KBGrant {
	f.t.Helper()
	row := &model.KBGrant{
		LibraryID:     libraryID,
		SubjectKind:   subjectKind,
		SubjectID:     subjectID,
		Perm:          perm,
		GrantedByKind: model.KBActorKindAdmin,
		GrantedByID:   f.adminID,
	}
	if err := gorm.G[model.KBGrant](f.db).Create(f.ctx, row); err != nil {
		f.t.Fatalf("建授权行失败: %v", err)
	}
	f.grantIDs = append(f.grantIDs, row.ID)
	return row
}

// auditCount 本库某动作的审计行数（actor 过滤可选）
func (f *kbFixture) auditCount(libraryID uint, action, actorKind string, actorID uint) int64 {
	f.t.Helper()
	q := gorm.G[model.KBAuditLog](f.db).Where("library_id = ? AND action = ?", libraryID, action)
	if actorKind != "" {
		q = q.Where("actor_kind = ? AND actor_id = ?", actorKind, actorID)
	}
	n, err := q.Count(f.ctx, "id")
	if err != nil {
		f.t.Fatalf("审计计数失败: %v", err)
	}
	return n
}

// libRowCount 属主下的存活库行数（软删不计）
func (f *kbFixture) libRowCount(ownerKind string, ownerID uint, name string) int64 {
	f.t.Helper()
	n, err := gorm.G[model.KBLibrary](f.db).
		Where("owner_kind = ? AND owner_id = ? AND name = ?", ownerKind, ownerID, name).
		Count(f.ctx, "id")
	if err != nil {
		f.t.Fatalf("库计数失败: %v", err)
	}
	return n
}

// docRowCount 文档行数（alive=未软删；all=含软删）
func (f *kbFixture) docRowCount(documentID uint) (alive, all int64) {
	f.t.Helper()
	alive, err := gorm.G[model.KBDocument](f.db).Where("id = ?", documentID).Count(f.ctx, "id")
	if err != nil {
		f.t.Fatalf("文档计数失败: %v", err)
	}
	if err := f.db.Unscoped().Model(&model.KBDocument{}).Where("id = ?", documentID).Count(&all).Error; err != nil {
		f.t.Fatalf("文档计数失败: %v", err)
	}
	return alive, all
}

func strPtrKB(s string) *string { return &s }

// ==================== ① 隔离与权限核心对抗用例 ====================

// TestACLIsolationDB 读视图硬条款端到端：
// 跨用户读他人库拒 / 跨团队未授权读拒 / 撤销即时生效（同视图不缓存授权）/ admin 轴与 user 轴同号不放大 /
// actor_id=0 短路拒 / write-only 不读但能写 / 不存在与无权同型 / fail-closed（停用、悬空团队、禁用库）。
func TestACLIsolationDB(t *testing.T) {
	f := newKBFixture(t)
	ctx := f.ctx
	svc := f.svc
	admin := Actor{Kind: model.KBActorKindAdmin, ID: f.adminID}

	// ---- 造数：组 / 用户 / 团队 / 库 / 文档 / 授权 ----
	gTeam := f.group("kb-acl-team-"+f.suffix, 1)
	gOther := f.group("kb-acl-other-"+f.suffix, 1)
	gWrite := f.group("kb-acl-write-"+f.suffix, 1)

	uOwner := f.user("kb-acl-owner-"+f.suffix, "enable", gTeam.ID) // 属主 + 团队成员（主分组）
	uOther := f.user("kb-acl-other-"+f.suffix, "enable", gOther.ID)
	uWrite := f.user("kb-acl-write-"+f.suffix, "enable", gWrite.ID)

	team := f.team(gTeam.Name, gTeam.ID, 1)

	libOwner := f.library(model.KBLibraryOwnerKindUser, uOwner.ID, "kb-acl-ownerlib-"+f.suffix, 1)
	libOther := f.library(model.KBLibraryOwnerKindUser, uOther.ID, "kb-acl-otherlib-"+f.suffix, 1)
	libPub := f.library(model.KBLibraryOwnerKindSystem, 0, "kb-acl-publib-"+f.suffix, 1)
	f.grant(libPub.ID, model.KBGrantSubjectAll, 0, model.KBGrantPermRead)
	libTeam := f.library(model.KBLibraryOwnerKindSystem, 0, "kb-acl-teamlilb-"+f.suffix, 1)
	f.grant(libTeam.ID, model.KBGrantSubjectTeam, team.ID, model.KBGrantPermRead)

	viewOwner, err := svc.UserView(ctx, uOwner.ID)
	if err != nil {
		t.Fatalf("属主读视图解析失败: %v", err)
	}
	viewOther, err := svc.UserView(ctx, uOther.ID)
	if err != nil {
		t.Fatalf("他用户读视图解析失败: %v", err)
	}

	// ---- 读视图：owner ∪ all ∪ team ----
	if _, err := svc.GetLibrary(ctx, viewOwner, libOwner.ID); err != nil {
		t.Fatalf("属主应可读自建库: %v", err)
	}
	if _, err := svc.GetLibrary(ctx, viewOwner, libPub.ID); err != nil {
		t.Fatalf("公开库（all 行）应可读: %v", err)
	}
	if _, err := svc.GetLibrary(ctx, viewOwner, libTeam.ID); err != nil {
		t.Fatalf("团队成员应可读团队库: %v", err)
	}

	// 跨用户读他人库拒
	if _, err := svc.GetLibrary(ctx, viewOwner, libOther.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("跨用户读他人库应拒，得到: %v", err)
	}

	// 跨团队未授权读拒（uOther 不在团队组籍）
	if _, err := svc.GetLibrary(ctx, viewOther, libTeam.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("跨团队未授权读应拒，得到: %v", err)
	}

	// 「不存在」与「无权」同型（同一哨兵；两层校验路径亦同）
	_, errMissing := svc.GetLibrary(ctx, viewOwner, 987654321)
	_, errDenied := svc.GetLibrary(ctx, viewOwner, libOther.ID)
	if !errors.Is(errMissing, ErrDenied) || !errors.Is(errDenied, ErrDenied) {
		t.Fatalf("同型 404 失效：missing=%v denied=%v", errMissing, errDenied)
	}
	if errMissing.Error() != errDenied.Error() {
		t.Fatalf("同型 404 文案不一致：missing=%q denied=%q", errMissing, errDenied)
	}

	// ---- 列表（两步反查）：Count 与 List 同一构造器 ----
	rows, total, err := svc.ListLibraries(ctx, viewOwner, LibraryListOptions{
		OwnerKind: model.KBLibraryOwnerKindUser, OwnerID: uOwner.ID, Limit: 50,
	})
	if err != nil {
		t.Fatalf("属主列表失败: %v", err)
	}
	if total != 1 || len(rows) != 1 || rows[0].ID != libOwner.ID {
		t.Fatalf("属主列表应恰含自建库：total=%d rows=%d", total, len(rows))
	}
	// 同一构造器：换成他人属主过滤，交集为空（若过滤在应用层后置，total 会泄露存在性）
	_, totalOther, err := svc.ListLibraries(ctx, viewOwner, LibraryListOptions{
		OwnerKind: model.KBLibraryOwnerKindUser, OwnerID: uOther.ID, Limit: 50,
	})
	if err != nil {
		t.Fatalf("他人属主列表失败: %v", err)
	}
	if totalOther != 0 {
		t.Fatalf("不可读库不得进 Count：total=%d", totalOther)
	}

	// ---- 撤销即时生效（软删 grant；同视图不缓存授权） ----
	libRevoke := f.library(model.KBLibraryOwnerKindUser, uOwner.ID, "kb-acl-revoke-"+f.suffix, 1)
	grantRevoke := f.grant(libRevoke.ID, model.KBGrantSubjectUser, uOther.ID, model.KBGrantPermRead)

	if ok, err := svc.ReadableLibrary(ctx, viewOther, libRevoke.ID); err != nil || !ok {
		t.Fatalf("授权后应可读：ok=%v err=%v", ok, err)
	}
	if _, total, err := svc.ListLibraries(ctx, viewOther, LibraryListOptions{
		OwnerKind: model.KBLibraryOwnerKindUser, OwnerID: uOwner.ID, Limit: 50,
	}); err != nil || total != 1 {
		t.Fatalf("授权后反查集应命中：total=%d err=%v", total, err)
	}

	if err := svc.RemoveGrant(ctx, admin, libRevoke.ID, grantRevoke.ID); err != nil {
		t.Fatalf("撤销授权失败: %v", err)
	}

	// 同一视图（无任何本地缓存）即时失效
	if ok, err := svc.ReadableLibrary(ctx, viewOther, libRevoke.ID); err != nil || ok {
		t.Fatalf("撤销后应即时不可读：ok=%v err=%v", ok, err)
	}
	if _, err := svc.GetLibrary(ctx, viewOther, libRevoke.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("撤销后取库应拒，得到: %v", err)
	}
	if _, total, err := svc.ListLibraries(ctx, viewOther, LibraryListOptions{
		OwnerKind: model.KBLibraryOwnerKindUser, OwnerID: uOwner.ID, Limit: 50,
	}); err != nil || total != 0 {
		t.Fatalf("撤销后反查集应即时失效：total=%d err=%v", total, err)
	}
	// 撤销留痕（同事务审计）
	if n := f.auditCount(libRevoke.ID, model.KBAuditActionGrantRemove, model.KBActorKindAdmin, f.adminID); n != 1 {
		t.Fatalf("撤销应恰留 1 条 grant_remove 审计，得到 %d", n)
	}

	// ---- write-only：不读但能写（读写两轴正交） ----
	libW := f.library(model.KBLibraryOwnerKindUser, uOwner.ID, "kb-acl-wlib-"+f.suffix, 1)
	f.grant(libW.ID, model.KBGrantSubjectUser, uWrite.ID, model.KBGrantPermWrite)
	doc := f.document(libW.ID, "kb-acl-doc-"+f.suffix)

	viewWrite, err := svc.UserView(ctx, uWrite.ID)
	if err != nil {
		t.Fatalf("写授权用户视图解析失败: %v", err)
	}
	if ok, err := svc.ReadableLibrary(ctx, viewWrite, libW.ID); err != nil || ok {
		t.Fatalf("write-only 不应可读：ok=%v err=%v", ok, err)
	}
	if _, err := svc.GetLibrary(ctx, viewWrite, libW.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("write-only 取库应拒，得到: %v", err)
	}
	if _, _, err := svc.ListDocuments(ctx, viewWrite, libW.ID, DocumentListOptions{}); !errors.Is(err, ErrDenied) {
		t.Fatalf("write-only 文档列表应拒，得到: %v", err)
	}
	if _, err := svc.GetDocument(ctx, viewWrite, doc.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("write-only 取文档应拒，得到: %v", err)
	}
	if ok, err := svc.WritableLibrary(ctx, viewWrite, libW.ID); err != nil || !ok {
		t.Fatalf("write-only 应可写：ok=%v err=%v", ok, err)
	}

	// 清理键（kb:purge）经钩子透出（接线前用钩子核键口径）
	var purgeKeys []string
	PurgeEnqueueHook = func(_ context.Context, key string) error {
		purgeKeys = append(purgeKeys, key)
		return nil
	}
	defer func() { PurgeEnqueueHook = nil }()

	// 真写：write-only 用户删文档成功（内容写面）
	if err := svc.DeleteDocument(ctx, Actor{Kind: model.KBActorKindUser, ID: uWrite.ID}, doc.ID); err != nil {
		t.Fatalf("write-only 删文档应成功: %v", err)
	}
	wantKey := fmt.Sprintf("kb:doc:%d", doc.ID)
	if len(purgeKeys) != 1 || purgeKeys[0] != wantKey {
		t.Fatalf("清理键口径应 %q，得到 %v", wantKey, purgeKeys)
	}
	if alive, all := f.docRowCount(doc.ID); alive != 0 || all != 1 {
		t.Fatalf("删文档应软删即刻（alive=%d all=%d）", alive, all)
	}
	if n := f.auditCount(libW.ID, model.KBAuditActionDelete, model.KBActorKindUser, uWrite.ID); n != 1 {
		t.Fatalf("删文档应恰留 1 条 delete 审计（user 轴），得到 %d", n)
	}

	// ---- 读写双授落两行（含 read 后可读） ----
	f.grant(libW.ID, model.KBGrantSubjectUser, uWrite.ID, model.KBGrantPermRead)
	viewWrite2, err := svc.UserView(ctx, uWrite.ID)
	if err != nil {
		t.Fatalf("双授后视图解析失败: %v", err)
	}
	if ok, err := svc.ReadableLibrary(ctx, viewWrite2, libW.ID); err != nil || !ok {
		t.Fatalf("双授后应可读：ok=%v err=%v", ok, err)
	}

	// ---- admin 轴：不因 admins.id 与 users.id 同号而放大（两轴谓词分离） ----
	adminSameID := Actor{Kind: model.KBActorKindAdmin, ID: uOwner.ID}
	// user 轴不因同号获得后台全库可见
	if _, err := svc.GetLibrary(ctx, viewOwner, libOther.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("user 轴不得借同号放大（应仍拒），得到: %v", err)
	}
	viewAdminSame, err := svc.AdminView(ctx, adminSameID.ID)
	if err != nil {
		t.Fatalf("admin 视图解析失败: %v", err)
	}
	// admin 轴不借用 user 谓词：零授权也全库可见（含他人库）
	if _, err := svc.GetLibrary(ctx, viewAdminSame, libOther.ID); err != nil {
		t.Fatalf("admin 轴应全库可见（不借助权行）: %v", err)
	}
	// admin 轴写用户库（管理面）；审计落 admin 轴（不混 user 空间）
	if _, err := svc.UpdateLibrary(ctx, adminSameID, libOther.ID, LibraryUpdate{Description: strPtrKB("kb-acl-admin-edit-" + f.suffix)}); err != nil {
		t.Fatalf("admin 轴更新库失败: %v", err)
	}
	if n := f.auditCount(libOther.ID, model.KBAuditActionUpdate, model.KBActorKindAdmin, adminSameID.ID); n != 1 {
		t.Fatalf("admin 轴变更应留 admin 轴审计，得到 %d", n)
	}
	if n := f.auditCount(libOther.ID, model.KBAuditActionUpdate, model.KBActorKindUser, adminSameID.ID); n != 0 {
		t.Fatalf("审计主体混轴（user 空间出现 admin 操作），得到 %d", n)
	}

	// ---- actor_id=0 入口短路拒（不查表；同型 404 族） ----
	for _, kind := range []string{model.KBActorKindUser, model.KBActorKindAdmin, model.KBActorKindSystem, ""} {
		_, err := svc.BuildReadView(ctx, kind, 0)
		if !errors.Is(err, ErrInvalidActor) || !errors.Is(err, ErrDenied) {
			t.Fatalf("actor_id=0 应入口短路拒（kind=%q），得到: %v", kind, err)
		}
	}
	// system 不是请求主体轴（它是 owner_kind/审计闭集）
	if _, err := svc.BuildReadView(ctx, model.KBActorKindSystem, 1); !errors.Is(err, ErrInvalidActor) {
		t.Fatalf("未登记轴应拒，得到: %v", err)
	}

	// ---- fail-closed：账号停用 / 不存在 / 团队悬空 / 禁用库 ----
	uDisabled := f.user("kb-acl-disabled-"+f.suffix, "disable", gOther.ID)
	if _, err := svc.UserView(ctx, uDisabled.ID); !errors.Is(err, ErrActorUnavailable) || !errors.Is(err, ErrDenied) {
		t.Fatalf("停用账号应拒，得到: %v", err)
	}
	if _, err := svc.UserView(ctx, 987654322); !errors.Is(err, ErrActorUnavailable) {
		t.Fatalf("不存在账号应拒，得到: %v", err)
	}

	// 团队悬空（组硬删）：授权行保留但对一切用户不可命中（fail-closed）
	gGone := f.group("kb-acl-gone-"+f.suffix, 1)
	uTeam := f.user("kb-acl-teamuser-"+f.suffix, "enable", gGone.ID)
	teamGone := f.team(gGone.Name, gGone.ID, 1)
	libTeamGone := f.library(model.KBLibraryOwnerKindSystem, 0, "kb-acl-teamlilb-gone-"+f.suffix, 1)
	f.grant(libTeamGone.ID, model.KBGrantSubjectTeam, teamGone.ID, model.KBGrantPermRead)

	viewTeam, err := svc.UserView(ctx, uTeam.ID)
	if err != nil {
		t.Fatalf("团队成员视图解析失败: %v", err)
	}
	if _, err := svc.GetLibrary(ctx, viewTeam, libTeamGone.ID); err != nil {
		t.Fatalf("组名绑定在时成员应可读: %v", err)
	}
	f.hardDeleteGroup(gGone.ID) // 组改名/删除 → 绑定悬空
	viewTeamAfter, err := svc.UserView(ctx, uTeam.ID)
	if err != nil {
		t.Fatalf("悬空后视图解析失败: %v", err)
	}
	if _, err := svc.GetLibrary(ctx, viewTeamAfter, libTeamGone.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("团队悬空应 fail-closed 拒，得到: %v", err)
	}

	// 团队禁用：同样不参与判定（授权保留）
	gT2 := f.group("kb-acl-team2-"+f.suffix, 1)
	uT2 := f.user("kb-acl-teamuser2-"+f.suffix, "enable", gT2.ID)
	teamDisabled := f.team(gT2.Name, gT2.ID, 0) // status=0
	libT2 := f.library(model.KBLibraryOwnerKindSystem, 0, "kb-acl-teamlilb2-"+f.suffix, 1)
	f.grant(libT2.ID, model.KBGrantSubjectTeam, teamDisabled.ID, model.KBGrantPermRead)
	viewT2, err := svc.UserView(ctx, uT2.ID)
	if err != nil {
		t.Fatalf("团队成员2 视图解析失败: %v", err)
	}
	if _, err := svc.GetLibrary(ctx, viewT2, libT2.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("禁用团队应 fail-closed 拒，得到: %v", err)
	}

	// 禁用库：用户读/写路径不可见（含属主）；后台管理面可见
	libDisabled := f.library(model.KBLibraryOwnerKindUser, uOwner.ID, "kb-acl-disabledlib-"+f.suffix, 0)
	if _, err := svc.GetLibrary(ctx, viewOwner, libDisabled.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("禁用库对用户读路径应不可见，得到: %v", err)
	}
	if ok, err := svc.WritableLibrary(ctx, viewOwner, libDisabled.ID); err != nil || ok {
		t.Fatalf("禁用库对用户写路径应不可写：ok=%v err=%v", ok, err)
	}
	if ok, err := svc.ManageableLibrary(ctx, viewOwner, libDisabled.ID); err != nil || !ok {
		t.Fatalf("禁用库属主应仍可管理（改回启用）：ok=%v err=%v", ok, err)
	}
	if _, err := svc.GetLibrary(ctx, viewAdminSame, libDisabled.ID); err != nil {
		t.Fatalf("禁用库后台管理面应可见: %v", err)
	}

	// ---- 管理权 = 属主 ∪ 后台轴（写授权不扩管理权） ----
	if ok, err := svc.ManageableLibrary(ctx, viewWrite2, libW.ID); err != nil || ok {
		t.Fatalf("写授权不得扩管理权：ok=%v err=%v", ok, err)
	}
	if _, err := svc.AddGrant(ctx, Actor{Kind: model.KBActorKindUser, ID: uWrite.ID},
		libW.ID, GrantInput{SubjectKind: model.KBGrantSubjectUser, SubjectID: uOther.ID, Perm: model.KBGrantPermRead}); !errors.Is(err, ErrDenied) {
		t.Fatalf("非属主授他人权应拒，得到: %v", err)
	}

	// ---- 授权面板：幂等 / 主体闭集 / 悬空主体拒 / 同事务审计 ----
	// 幂等：已存在的公开行（造数期落）重复授权 → 回读同一行、不重复落审计
	var g1 model.KBGrant
	if err := f.db.Where("library_id = ? AND subject_kind = ? AND subject_id = ? AND perm = ?",
		libPub.ID, model.KBGrantSubjectAll, 0, model.KBGrantPermRead).First(&g1).Error; err != nil {
		t.Fatalf("读取既有公开授权失败: %v", err)
	}
	again, err := svc.AddGrant(ctx, admin, libPub.ID, GrantInput{SubjectKind: model.KBGrantSubjectAll, Perm: model.KBGrantPermRead})
	if err != nil || again.ID != g1.ID {
		t.Fatalf("重复授权应幂等回读现有行：id=%v err=%v", again, err)
	}
	if n := f.auditCount(libPub.ID, model.KBAuditActionGrantAdd, "", 0); n != 0 {
		t.Fatalf("幂等重复授权不应重复落审计，得到 %d", n)
	}
	if _, err := svc.AddGrant(ctx, admin, libPub.ID, GrantInput{SubjectKind: "org", SubjectID: 1, Perm: model.KBGrantPermRead}); err == nil {
		t.Fatal("未登记主体类型应拒")
	}
	if _, err := svc.AddGrant(ctx, admin, libPub.ID, GrantInput{SubjectKind: model.KBGrantSubjectTeam, SubjectID: 987654323, Perm: model.KBGrantPermRead}); err == nil {
		t.Fatal("悬空团队主体应拒")
	}
	if n := f.auditCount(libPub.ID, model.KBAuditActionGrantAdd, "", 0); n != 0 {
		t.Fatalf("失败授权不得留审计（同事务），得到 %d", n)
	}
	// 正常授权：grant_add 同事务留痕
	gTeam2, err := svc.AddGrant(ctx, admin, libPub.ID, GrantInput{SubjectKind: model.KBGrantSubjectTeam, SubjectID: team.ID, Perm: model.KBGrantPermRead})
	if err != nil {
		t.Fatalf("团队授权失败: %v", err)
	}
	f.grantIDs = append(f.grantIDs, gTeam2.ID)
	if n := f.auditCount(libPub.ID, model.KBAuditActionGrantAdd, model.KBActorKindAdmin, f.adminID); n != 1 {
		t.Fatalf("授权应恰留 1 条 grant_add 审计，得到 %d", n)
	}

	// ---- 建库/删库：审计同事务 + 重名拦截不留痕 + 清理键 ----
	created, err := svc.CreateLibrary(ctx, admin, LibraryInput{
		OwnerKind: model.KBLibraryOwnerKindUser, OwnerID: uOther.ID, Name: "kb-acl-new-" + f.suffix,
	})
	if err != nil {
		t.Fatalf("后台代建库失败: %v", err)
	}
	f.libIDs = append(f.libIDs, created.ID)
	if n := f.auditCount(created.ID, model.KBAuditActionCreate, model.KBActorKindAdmin, f.adminID); n != 1 {
		t.Fatalf("建库应恰留 1 条 create 审计，得到 %d", n)
	}
	// 重名：拒 + 不留库行 + 不留审计（同事务原子性）
	if _, err := svc.CreateLibrary(ctx, admin, LibraryInput{
		OwnerKind: model.KBLibraryOwnerKindUser, OwnerID: uOther.ID, Name: created.Name,
	}); err == nil {
		t.Fatal("同属主重名建库应拒")
	}
	if n := f.libRowCount(model.KBLibraryOwnerKindUser, uOther.ID, created.Name); n != 1 {
		t.Fatalf("重名失败不应留行：%d", n)
	}
	if n := f.auditCount(created.ID, model.KBAuditActionCreate, "", 0); n != 1 {
		t.Fatalf("失败建库不应留审计：%d", n)
	}

	// 归属转移（仅后台轴）：授权行不动
	grantsBefore, _, err := svc.ListGrants(ctx, viewAdminSame, created.ID, 1, 50)
	if err != nil {
		t.Fatalf("授权列表失败: %v", err)
	}
	if err := svc.TransferLibraryOwner(ctx, admin, created.ID, model.KBLibraryOwnerKindSystem, 0); err != nil {
		t.Fatalf("归属转移失败: %v", err)
	}
	grantsAfter, _, err := svc.ListGrants(ctx, viewAdminSame, created.ID, 1, 50)
	if err != nil {
		t.Fatalf("授权列表失败: %v", err)
	}
	if len(grantsBefore) != len(grantsAfter) {
		t.Fatalf("转移归属不得改授权：before=%d after=%d", len(grantsBefore), len(grantsAfter))
	}
	if n := f.auditCount(created.ID, model.KBAuditActionOwnerTransfer, model.KBActorKindAdmin, f.adminID); n != 1 {
		t.Fatalf("归属转移应恰留 1 条审计，得到 %d", n)
	}
	// 授权面板门与 grant_add/remove 同门：读/写授权者（非属主）不得枚举授权主体
	if _, _, err := svc.ListGrants(ctx, viewWrite2, libW.ID, 1, 10); !errors.Is(err, ErrDenied) {
		t.Fatalf("授权者不得读授权面板（管理权门），得到: %v", err)
	}
	// 用户级属主存在性：悬空 users.id / admins.id 混用一律拒（不落行）
	if _, err := svc.CreateLibrary(ctx, admin, LibraryInput{
		OwnerKind: model.KBLibraryOwnerKindUser, OwnerID: 424242424, Name: "kb-acl-ghost-" + f.suffix,
	}); err == nil {
		t.Fatal("悬空用户属主建库应拒")
	}
	// 库列表过滤面：name ILIKE 与 id 区间均在隔离谓词之上收敛（Count 同构造器）
	nameRows, nameTotal, err := svc.ListLibraries(ctx, viewOwner, LibraryListOptions{
		NameLike: "kb-acl-ownerlib-" + f.suffix, Limit: 10,
	})
	if err != nil || nameTotal != 1 || len(nameRows) != 1 || nameRows[0].ID != libOwner.ID {
		t.Fatalf("name 快搜应恰命中属主库：total=%d rows=%d err=%v", nameTotal, len(nameRows), err)
	}
	idFrom, idTo := libOwner.ID, libOwner.ID
	rangeRows, rangeTotal, err := svc.ListLibraries(ctx, viewOwner, LibraryListOptions{
		IDFrom: &idFrom, IDTo: &idTo, Limit: 10,
	})
	if err != nil || rangeTotal != 1 || len(rangeRows) != 1 || rangeRows[0].ID != libOwner.ID {
		t.Fatalf("id 区间过滤应恰命中属主库：total=%d rows=%d err=%v", rangeTotal, len(rangeRows), err)
	}
	// 时间窗过滤（前端 created_at/updated_at BETWEEN 的服务层支撑）
	timeFrom, timeTo := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	if _, timeTotal, err := svc.ListLibraries(ctx, viewOwner, LibraryListOptions{
		CreatedFrom: &timeFrom, CreatedTo: &timeTo, Limit: 10,
	}); err != nil || timeTotal < 1 {
		t.Fatalf("created_at 时间窗应命中本用例库：total=%d err=%v", timeTotal, err)
	}
	// 用户轴不得执行归属转移
	if err := svc.TransferLibraryOwner(ctx, Actor{Kind: model.KBActorKindUser, ID: uOwner.ID}, libOwner.ID, model.KBLibraryOwnerKindSystem, 0); !errors.Is(err, ErrDenied) {
		t.Fatalf("用户轴归属转移应拒，得到: %v", err)
	}

	// 删库：软删即刻 + 清理键 kb:<library_id> + 审计
	if err := svc.DeleteLibrary(ctx, admin, created.ID); err != nil {
		t.Fatalf("删库失败: %v", err)
	}
	wantLibKey := fmt.Sprintf("kb:%d", created.ID)
	if len(purgeKeys) != 2 || purgeKeys[1] != wantLibKey {
		t.Fatalf("删库清理键口径应 %q，得到 %v", wantLibKey, purgeKeys)
	}
	if n := f.auditCount(created.ID, model.KBAuditActionDelete, model.KBActorKindAdmin, f.adminID); n != 1 {
		t.Fatalf("删库应恰留 1 条 delete 审计，得到 %d", n)
	}
	// 软删即刻：管理面/任何面读不到
	if _, err := svc.GetLibrary(ctx, viewAdminSame, created.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("删库后应即时不可见，得到: %v", err)
	}

	// ---- 审计列表（admin 轴；用户轴拒） ----
	if _, err := svc.ListAuditLogs(ctx, viewOwner, AuditListOptions{LibraryID: libOwner.ID}); !errors.Is(err, ErrDenied) {
		t.Fatalf("用户轴读审计应拒，得到: %v", err)
	}
	logs, err := svc.ListAuditLogs(ctx, viewAdminSame, AuditListOptions{LibraryID: libOther.ID, Limit: 10})
	if err != nil || len(logs) == 0 {
		t.Fatalf("审计列表失败：n=%d err=%v", len(logs), err)
	}
	for _, row := range logs {
		if row.LibraryID != libOther.ID {
			t.Fatalf("审计列表串库：%+v", row)
		}
	}

	// ---- 删库后其文档对**后台文档面**同样不可见（软删即刻不因节点授权放宽） ----
	libGoneDoc := f.library(model.KBLibraryOwnerKindSystem, 0, "kb-acl-docgone-lib-"+f.suffix, 1)
	docGone := f.document(libGoneDoc.ID, "kb-acl-docgone-"+f.suffix)
	// 列表面基线：删库前后台文档列表应恰见该文档（正对照，防「恒拒假绿」）
	if rows, total, err := svc.ListDocuments(ctx, viewAdminSame, libGoneDoc.ID, DocumentListOptions{}); err != nil || total != 1 || len(rows) != 1 || rows[0].ID != docGone.ID {
		t.Fatalf("删库前后台文档列表应恰含该文档：total=%d rows=%d err=%v", total, len(rows), err)
	}
	if err := svc.DeleteLibrary(ctx, admin, libGoneDoc.ID); err != nil {
		t.Fatalf("删库（文档级联可见性用例）失败: %v", err)
	}
	if _, err := svc.GetDocument(ctx, viewAdminSame, docGone.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("父库已删后后台文档面应拒（同型），得到: %v", err)
	}
	// 列表面：单资源门（libraryAccessible）统一「库未删」谓词后，按库收窄的
	// 文档列表对软删库同样同型拒——修前 admin 轴 O(1) 恒真 ⇒ 这里会列出软删库内文档（回归即复现）
	if _, _, err := svc.ListDocuments(ctx, viewAdminSame, libGoneDoc.ID, DocumentListOptions{}); !errors.Is(err, ErrDenied) {
		t.Fatalf("父库已删后后台文档列表应拒（同型），得到: %v", err)
	}
	// 同口径复核：单资源读门/写门对软删库的判定（与文档面/可读集同一「未删」谓词，防口径漂移）
	if ok, err := svc.ReadableLibrary(ctx, viewAdminSame, libGoneDoc.ID); err != nil || ok {
		t.Fatalf("软删库 admin 单资源读门应拒：ok=%v err=%v", ok, err)
	}
	if ok, err := svc.WritableLibrary(ctx, viewAdminSame, libGoneDoc.ID); err != nil || ok {
		t.Fatalf("软删库 admin 单资源写门应拒：ok=%v err=%v", ok, err)
	}
	// 管理门同口径（回归）：admin 轴原 O(1) 恒真短路过库谓词 ⇒ 软删库仍可经管理门
	// 列授权（ListGrants 残留可见面）；修后与读/写门一致——软删库对一切面不可见（回归即复现）
	if ok, err := svc.ManageableLibrary(ctx, viewAdminSame, libGoneDoc.ID); err != nil || ok {
		t.Fatalf("软删库 admin 管理门应拒：ok=%v err=%v", ok, err)
	}
	if _, _, err := svc.ListGrants(ctx, viewAdminSame, libGoneDoc.ID, 0, 10); !errors.Is(err, ErrDenied) {
		t.Fatalf("软删库授权面板应拒（同型），得到: %v", err)
	}
}

// ==================== 过滤下推证据（SQL 捕获，见 acl_sql_test.go） ====================

// ==================== ③ 团队授权的可见性（端口化后的等价覆盖） ====================

// TestTeamGrantVisibilityDB 团队授权可见性（原「MemberOfAny × EnabledGroupNames 等价性表」的插件侧等价面）。
//
// 迁移前那个用例对照的是宿主 user 仓储的两个方法（组名集 ↔ 成员命中）；随「插件不假设宿主有用户体系」
// 的端口化改造，那两个方法移出了本插件 ⇒ 同一语义改断言**业务结论**：组名集 → kb_team id 集 →
// kb_grant 命中的整条链路（acl.go 的 enabledTeamIDs / grantMatchCond）覆盖不降：
//   - 团队成员可读被授权库；非成员不可读（不存在/无权同型）；
//   - 组名失效（组硬删）与团队停用 ⇒ 授权行保留但一律不命中（fail-closed）；
//   - 同名双组时命中由**实际组 id** 决定（首行 = 最小 id：主分组取禁用侧则不命中）；
//   - 撤权（软删 grant 行）即时生效。
func TestTeamGrantVisibilityDB(t *testing.T) {
	f := newKBFixture(t)
	ctx := f.ctx
	svc := f.svc

	nameSame := "kb-acl-tv-same-" + f.suffix
	gSameDis := f.group(nameSame, 0) // 同名双组：先建的禁用
	gSameEn := f.group(nameSame, 1)  // 同名双组：后建的启用（最小 id 仍是禁用侧）

	uMainDis := f.user("kb-acl-tv-u1-"+f.suffix, "enable", gSameDis.ID) // 主分组 = 最小 id（禁用侧）
	uMainEn := f.user("kb-acl-tv-u2-"+f.suffix, "enable", gSameEn.ID)   // 主分组 = 启用侧
	uNone := f.user("kb-acl-tv-u3-"+f.suffix, "enable", 0)              // 无组用户

	// 团队绑定按名规范化到「最小 id」（绑定基线由写入口解析，见 team_custom.go）
	tSame := f.team(nameSame, gSameDis.ID, 1)
	libTeam := f.library(model.KBLibraryOwnerKindSystem, 0, "kb-acl-tv-lib-"+f.suffix, 1)
	f.grant(libTeam.ID, model.KBGrantSubjectTeam, tSame.ID, model.KBGrantPermRead)

	readable := func(label string, userID uint, libID uint, want bool) {
		t.Helper()
		view, err := svc.UserView(ctx, userID)
		if err != nil {
			t.Fatalf("%s: 读视图解析失败: %v", label, err)
		}
		_, err = svc.GetLibrary(ctx, view, libID)
		if want && err != nil {
			t.Fatalf("%s: 应可读，得到: %v", label, err)
		}
		if !want && !errors.Is(err, ErrDenied) {
			t.Fatalf("%s: 应 fail-closed 拒，得到: %v", label, err)
		}
	}

	// 主分组 = 启用侧 ⇒ 命中（同名组的启用成员）
	readable("同名双组/主分组取启用侧", uMainEn.ID, libTeam.ID, true)
	// 主分组 = 唯一命中 id 的禁用侧 ⇒ 不命中（团队绑定在禁用组上，停用组不参与判定）
	readable("同名双组/绑定在禁用组", uMainDis.ID, libTeam.ID, false)
	readable("无组用户", uNone.ID, libTeam.ID, false)

	// 组硬删 ⇒ 绑定悬空（授权行保留，但不命中）
	f.hardDeleteGroup(gSameEn.ID)
	f.hardDeleteGroup(gSameDis.ID)
	readable("组硬删后成员不可命中", uMainEn.ID, libTeam.ID, false)

	// 撤权（软删授权行）即时生效：重建一个正常命中的团队授权后撤权
	gOK := f.group("kb-acl-tv-ok-"+f.suffix, 1)
	uOK := f.user("kb-acl-tv-u4-"+f.suffix, "enable", gOK.ID)
	tOK := f.team(gOK.Name, gOK.ID, 1)
	libOK := f.library(model.KBLibraryOwnerKindSystem, 0, "kb-acl-tv-lib2-"+f.suffix, 1)
	grantOK := f.grant(libOK.ID, model.KBGrantSubjectTeam, tOK.ID, model.KBGrantPermRead)
	readable("团队成员可读", uOK.ID, libOK.ID, true)
	actor := Actor{Kind: model.KBActorKindAdmin, ID: f.adminID}
	if err := svc.RemoveGrant(ctx, actor, libOK.ID, grantOK.ID); err != nil {
		t.Fatalf("撤权失败: %v", err)
	}
	readable("撤权后即时不可读", uOK.ID, libOK.ID, false)
}

// TestPrincipalFailClosedDB 未接入用户体系（未注册 Principal 端口）时的正式验收面：
//   - user 轴每条路径一律 ErrDenied 包装的 ErrFailClosed（errors.Is 双向断言）；
//   - **admin 轴行为不变**（可读全集、写面照常）——部署形态变化不改后台语义。
//
// 「把 user 轴用例删掉」不是选项：这就是「无用户体系部署」的验收断言，故与正向用例同文件并存。
func TestPrincipalFailClosedDB(t *testing.T) {
	f := newKBFixture(t)
	ctx := f.ctx
	svc := f.svc

	lib := f.library(model.KBLibraryOwnerKindSystem, 0, "kb-acl-fc-lib-"+f.suffix, 1)
	uAny := f.user("kb-acl-fc-u-"+f.suffix, "enable", 0)

	// 未注册端口（清零）⇒ user 轴全部 fail-closed
	RegisterPrincipal(nil)

	if _, err := svc.UserView(ctx, uAny.ID); !errors.Is(err, ErrFailClosed) || !errors.Is(err, ErrDenied) {
		t.Fatalf("未接入用户体系时 UserView 应 ErrFailClosed，得到: %v", err)
	}
	if _, err := svc.ImportDocument(ctx, Actor{Kind: model.KBActorKindUser, ID: uAny.ID}, ImportInput{
		LibraryID: lib.ID, Text: "fail-closed 探针",
	}); !errors.Is(err, ErrFailClosed) || !errors.Is(err, ErrDenied) {
		t.Fatalf("未接入用户体系时 user 轴写面应 ErrFailClosed，得到: %v", err)
	}
	// 用户级授权主体：端口不可用 ⇒ fail-closed（不静默放行，也不谎称「主体不存在」）
	if err := checkOwnerExists(ctx, 990001); !errors.Is(err, ErrFailClosed) {
		t.Fatalf("端口不可用时主体存在性应 ErrFailClosed，得到: %v", err)
	}

	// admin 轴不变：后台读视图可读全集、写面照常
	adminView, err := svc.AdminView(ctx, f.adminID)
	if err != nil {
		t.Fatalf("admin 轴读视图应照常可用: %v", err)
	}
	if _, err := svc.GetLibrary(ctx, adminView, lib.ID); err != nil {
		t.Fatalf("admin 轴应可读系统级库: %v", err)
	}
	if _, err := svc.ImportDocument(ctx, Actor{Kind: model.KBActorKindAdmin, ID: f.adminID}, ImportInput{
		LibraryID: lib.ID, Text: "admin 轴导入",
	}); err != nil {
		t.Fatalf("admin 轴写面应照常可用: %v", err)
	}
}

// ==================== ③ 巡检面（悬空 / 回收 / 悬空授权） ====================

// TestCheckTeamsDB 巡检面（aigo kb check 的服务侧）：悬空点名 / 组名回收点名 / 悬空授权点名；
// 只读（不改任何行）、可复跑（两次读数一致）。
func TestCheckTeamsDB(t *testing.T) {
	f := newKBFixture(t)
	ctx := f.ctx

	gOK := f.group("kb-acl-ck-ok-"+f.suffix, 1)
	tOK := f.team(gOK.Name, gOK.ID, 1)

	gGone := f.group("kb-acl-ck-gone-"+f.suffix, 1)
	tGone := f.team(gGone.Name, gGone.ID, 1)
	f.hardDeleteGroup(gGone.ID) // 组删除 → 绑定悬空

	gOld := f.group("kb-acl-ck-rc-"+f.suffix, 1)
	oldID := gOld.ID
	f.hardDeleteGroup(gOld.ID)              // 组改名/删除
	gNew := f.group(gOld.Name, 1)           // 同名回收（新 id）
	tRecycle := f.team(gNew.Name, oldID, 1) // 绑定时 id ≠ 现行最早命中 id

	tDisabled := f.team("kb-acl-ck-dis-"+f.suffix, 0, 0)

	lib := f.library(model.KBLibraryOwnerKindSystem, 0, "kb-acl-ck-lib-"+f.suffix, 1)
	f.grant(lib.ID, model.KBGrantSubjectTeam, tOK.ID, model.KBGrantPermRead)
	orphan := f.grant(lib.ID, model.KBGrantSubjectTeam, 987654321, model.KBGrantPermRead) // 悬空授权

	report, err := f.svc.CheckTeams(ctx)
	if err != nil {
		t.Fatalf("巡检失败: %v", err)
	}
	finding := func(teamID uint) (TeamFinding, bool) {
		for i := range report.Teams {
			if report.Teams[i].TeamID == teamID {
				return report.Teams[i], true
			}
		}
		return TeamFinding{}, false
	}

	if row, ok := finding(tOK.ID); !ok || row.Kind != CheckKindOK {
		t.Fatalf("正常绑定应回 ok：%+v ok=%v", row, ok)
	}
	if row, ok := finding(tGone.ID); !ok || row.Kind != CheckKindDangling {
		t.Fatalf("组名不存在应回 dangling：%+v ok=%v", row, ok)
	}
	if row, ok := finding(tRecycle.ID); !ok || row.Kind != CheckKindRecycled || row.CurrentGroupID != gNew.ID {
		t.Fatalf("组名回收应回 recycled（现行 id 应透出）：%+v ok=%v", row, ok)
	}
	if row, ok := finding(tDisabled.ID); !ok || row.Kind != CheckKindDisabled {
		t.Fatalf("禁用团队应回 disabled：%+v ok=%v", row, ok)
	}

	foundOrphan := false
	for _, o := range report.OrphanTeamGrants {
		if o.GrantID == orphan.ID && o.TeamID == 987654321 {
			foundOrphan = true
		}
	}
	if !foundOrphan {
		t.Fatalf("悬空团队授权应点名：%+v", report.OrphanTeamGrants)
	}

	// 复跑一致（只读、可复跑）
	report2, err := f.svc.CheckTeams(ctx)
	if err != nil {
		t.Fatalf("巡检复跑失败: %v", err)
	}
	if len(report2.Teams) != len(report.Teams) || len(report2.OrphanTeamGrants) != len(report.OrphanTeamGrants) {
		t.Fatalf("复跑读数应一致：teams %d→%d orphan %d→%d",
			len(report.Teams), len(report2.Teams), len(report.OrphanTeamGrants), len(report2.OrphanTeamGrants))
	}
}

// ==================== ④ 无库依赖的单元断言（上限/排序/闭集） ====================

// TestACLLimitsUnit 分页上限与排序归一（不连库）
func TestACLLimitsUnit(t *testing.T) {
	if got := NormalizeBrowseLimit(5000); got != MaxBrowseLimit {
		t.Fatalf("浏览面上限应截断到 %d，得到 %d", MaxBrowseLimit, got)
	}
	if got := NormalizeBrowseLimit(0); got != DefaultBrowseLimit {
		t.Fatalf("缺省 limit 应为 %d，得到 %d", DefaultBrowseLimit, got)
	}
	if got := NormalizeBrowseLimit(-3); got != DefaultBrowseLimit {
		t.Fatalf("非法 limit 应回落缺省，得到 %d", got)
	}
	if got := NormalizeHardLimit(999999); got != MaxHardLimit {
		t.Fatalf("硬上限应截断到 %d，得到 %d", MaxHardLimit, got)
	}
	if clampLimit(100000, MaxHardLimit) != MaxHardLimit || MaxHardLimit <= MaxBrowseLimit {
		t.Fatalf("硬上限漏斗不成立")
	}

	// 排序归一：白名单外/空/id 一律归并到「主键托底」（空字段 ⇒ BuildOrderScope 只追加 PK DESC）
	field, order := normalizeSortList("name; DROP TABLE users", "asc", librarySortFields)
	if field != "" || order != "" {
		t.Fatalf("排序白名单外应归并到主键托底，得到 %q %q", field, order)
	}
	field, order = normalizeSortList("id", "desc", librarySortFields)
	if field != "" || order != "" {
		t.Fatalf("id 与主键托底同轴应归并，得到 %q %q", field, order)
	}
	field, order = normalizeSortList("updated_at", "ascending", librarySortFields)
	if field != "updated_at" || order != "asc" {
		t.Fatalf("白名单内字段应生效，得到 %s %s", field, order)
	}
	field, order = normalizeSortList("chunk_config", "desc", librarySortFields)
	if field != "" {
		t.Fatalf("jsonb 大字段不得进排序白名单，得到 %s", field)
	}

	if _, ok := auditActions[model.KBAuditActionGrantAdd]; !ok {
		t.Fatal("审计动作闭集缺 grant_add")
	}
	if _, ok := auditActions["read"]; ok {
		t.Fatal("闭集不得含 read（只增集合是变更动作面）")
	}
}
