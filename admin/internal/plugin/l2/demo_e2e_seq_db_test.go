package l2

// 本文件是 P6 验收的**三条用例**（验收 A / 反向路径 / 验收 B）与它们的探针工具；拆成三个文件是为守住
// 文件规模纪律（≤1024 行）：
//   - demo_e2e_db_test.go    公共基建（前置 / 快照还原 / 收尾清理 / 权限探针夹具等）与包注释；
//   - demo_e2e_engine_test.go L2 引擎级驱动与断言（spec 装载、④b 建表断言、六动作 CRUD）；
//   - 本文件                  三条用例与路由探针工具（demoProbe 系列、git status 口径断言）。
// 三条用例与设计口径的对应见 demo_e2e_db_test.go 的包注释。

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	gotoken "go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/token"
	"github.com/ai-go-hub/ai-go-admin/internal/kit/httpx"
	"github.com/ai-go-hub/ai-go-admin/internal/middleware"
	"github.com/ai-go-hub/ai-go-admin/internal/model"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/cli"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"
	"github.com/ai-go-hub/ai-go-admin/internal/version"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ---------- 验收 A ----------

// TestDemoAcceptanceA_DB 验收 A（「端到端验收标准 A」；L1 主链 + L2 引擎级面）：
//
//	--dry-run 预演（公式代写 + 逐条 DDL）→ install（④b 建表）→ 重编译语义等价 → enable（闸放行）
//	→ 六动作 CRUD + L1 页存在 → disable（闸拒）→ uninstall（零残留；表与 .store 保留）
//	→ 复装（迁移幂等、数据仍在）→ uninstall --purge --yes（表与版本表消失）。
func TestDemoAcceptanceA_DB(t *testing.T) {
	env, db := demoAssistantSetupA(t)
	ctx := context.Background()
	engine := New(db)
	out := env.Out.(*bytes.Buffer)

	// ===== ① --dry-run：全量预演 + 零副作用 =====
	if err := cli.Install(ctx, env, cli.PluginDir(demoName), cli.InstallOptions{DryRun: true, Operator: demoOperator}); err != nil {
		t.Fatalf("--dry-run 应成功: %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"插件安装预演：" + demoName,
		"① manifest 摘要", "② 文件清单", "③ 插件迁移", "④ 将写入的菜单/节点",
		"⑤ 将执行的 DDL", "⑥ 将改写的宿主文件", "⑦ 其余动作与生效方式",
		path.Join(cli.ViewTarget(demoName), "index.vue"),
		cli.LangTarget("zh-cn", demoName),
		cli.ImportPath(demoName),
		"plugin/" + demoName + "/" + demoEntity,
		// F1：⑤ 段逐条列出将执行的 DDL（新实体 ⇒ 建表计划），不是占位说明
		`CREATE TABLE IF NOT EXISTS "` + cli.PhysicalEntityTable(env.Prefix, demoName, demoEntity) + `"`,
		`CREATE UNIQUE INDEX IF NOT EXISTS "` + demoIndexName(t, demoName, "uk", "code") + `"`,
		"COMMENT ON",
		// F2：实体页菜单/节点由公式代写（④ 段标注 + 通用页 component）
		"（公式代写）",
		"/src/views/admin/plugin/render/index.vue",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("--dry-run 输出应含 %q\n--- 实际输出 ---\n%s", want, text)
		}
	}
	out.Reset()
	if err := cli.CheckSkeleton(env.Root); err != nil {
		t.Errorf("--dry-run 不应动 %s: %v", cli.RegistryFile, err)
	}
	if row, ok := demoLedger(t, db, demoName); ok {
		t.Errorf("--dry-run 不应写台账: %+v", row)
	}
	if tableExists(t, db, cli.PhysicalEntityTable(env.Prefix, demoName, demoEntity)) {
		t.Errorf("--dry-run 不应执行 DDL（表 %s 不得存在）", cli.PhysicalEntityTable(env.Prefix, demoName, demoEntity))
	}
	if matches, _ := filepath.Glob(filepath.Join(env.Root, filepath.FromSlash(cli.StoreDir), demoName+"-*.zip")); len(matches) != 0 {
		t.Errorf("--dry-run 不应产生包归档: %v", matches)
	}

	// ===== ② install（目录源）=====
	if err := cli.Install(ctx, env, cli.PluginDir(demoName), cli.InstallOptions{Operator: demoOperator}); err != nil {
		t.Fatalf("安装应成功: %v", err)
	}
	out.Reset()
	ledger := demoAssertLedger(t, db, demoName, host.StatusInstalled)
	if ledger.Kind != "mixed" || ledger.Version != "1.0.0" || ledger.Source != cli.PluginDir(demoName) {
		t.Errorf("台账摘要不符（kind/version/source）: %+v", ledger)
	}
	rows := demoRules(t, db, demoName)
	if len(rows) != 10 {
		t.Fatalf("应代写 10 条菜单/节点（L1 声明 4：分组 1 + L1 页 1 + L1 节点 2；公式代写 6：实体页 1 + 实体节点 5），实际 %d: %v",
			len(rows), demoRuleNames(rows))
	}
	// F4：同名规则零容忍——示例插件的 manifest **只声明 L1 部分**，实体页那一段由安装器代写；
	// 若 manifest 再度自补同一公式，这里会以「同名行写两次」报红（本断言就是那个探测点）。
	if dup := demoDuplicateRuleNames(rows); len(dup) != 0 {
		t.Fatalf("归属行出现同名规则（安装器代写与 manifest 声明重复？）：%v", dup)
	}
	declared, _, err := manifest.Load(env.Abs(cli.PluginDir(demoName)),
		manifest.Options{Prefix: env.Prefix, HostVersion: env.HostVersion})
	if err != nil {
		t.Fatalf("读取已落位清单失败: %v", err)
	}
	for _, menu := range declared.Menus {
		if strings.HasPrefix(menu.Name, "plugin/"+demoName+"/"+demoEntity) {
			t.Errorf("示例插件的 manifest 不得再声明实体页/节点（%s 由公式代写，声明同一公式会写两次）", menu.Name)
		}
	}
	demoAssertMenuStatus(t, db, demoName, 0) // 安装期统一隐藏，enable 才置 1
	demoAssertL1Page(t, env.Root, rows, demoName)
	demoAssertEntityPage(t, rows, demoName)
	demoAssertRouteNodes(t, db, demoName)

	entities, err := gorm.G[host.Entity](db).Where("plugin = ?", demoName).Find(ctx)
	if err != nil {
		t.Fatalf("读取实体元数据失败: %v", err)
	}
	if len(entities) != 1 || entities[0].Name != demoEntity {
		t.Fatalf("实体元数据应为 1 行（%s），实际 %+v", demoEntity, entities)
	}
	if want := cli.PhysicalEntityTable(env.Prefix, demoName, demoEntity); entities[0].PhysicalTable != want {
		t.Errorf("实体物理表名应为 %q，实际 %q", want, entities[0].PhysicalTable)
	}

	configKey := cli.ConfigKey(demoName, "page_hint")
	configs, err := gorm.G[model.Config](db).Where(model.Config{Group: "plugin." + demoName}).Find(ctx)
	if err != nil {
		t.Fatalf("读取 configs 失败: %v", err)
	}
	if len(configs) != 1 || configs[0].Name != configKey {
		t.Fatalf("configs 的 plugin.%s 分组应有 1 行（%s），实际 %+v", demoName, configKey, configs)
	}
	if configs[0].AllowDel != 0 {
		t.Errorf("安装器自管的配置键 allow_del 应为 0（真源是 manifest），实际 %d", configs[0].AllowDel)
	}

	// 落位面：源码与副本都带归属标记（标记幂等 ⇒ 源与副本 sha256 仍一致）
	manifestRel := path.Join(cli.PluginDir(demoName), manifest.FileName)
	if content := demoReadFile(t, env.Abs(manifestRel)); !strings.HasPrefix(string(content), "# plugin: "+demoName+" v1.0.0\n") {
		t.Errorf("%s 的文件头应带归属标记，实际 %q", manifestRel, demoFirstLine(string(content)))
	}
	viewRel := path.Join(cli.ViewTarget(demoName), "index.vue")
	if content := demoReadFile(t, env.Abs(viewRel)); !strings.HasPrefix(string(content), "<!-- plugin: "+demoName+" v1.0.0 -->\n") {
		t.Errorf("%s 的文件头应带归属标记，实际 %q", viewRel, demoFirstLine(string(content)))
	}

	// ⑥ 迁移：版本表 1 且干净；插件自管表与种子行都在
	if version, dirty := demoMigrationState(t, db, demoName); version != 1 || dirty {
		t.Errorf("插件迁移应应用到版本 1 且干净：version=%d dirty=%v", version, dirty)
	}
	notesTable := env.Prefix + "plugin_" + demoName + "_notes"
	if !tableExists(t, db, notesTable) {
		t.Fatalf("插件迁移建的表 %s 应存在", notesTable)
	}
	if count := demoRowCount(t, db, notesTable); count != 1 {
		t.Errorf("种子行应写入 %s：实际 %d 行", notesTable, count)
	}
	if count := demoAuditCount(t, db, demoName, cli.ActionInstall, cli.AuditSuccess); count != 1 {
		t.Errorf("应有 1 条 install/success 审计，实际 %d", count)
	}

	// ===== ③ 重编译语义等价（本用例不重编译：注册文件 + 插件包登记常量已等价）=====
	demoAssertRecompileSemantics(t, env, demoName, "1.0.0", "mixed")

	// ===== ④ enable：菜单放开 + 闸放行 =====
	if err := cli.Enable(ctx, env, demoName, demoOperator); err != nil {
		t.Fatalf("启用应成功: %v", err)
	}
	out.Reset()
	demoAssertLedger(t, db, demoName, host.StatusEnabled)
	demoAssertMenuStatus(t, db, demoName, 1)
	if code := demoGateProbe(t, demoName); code != http.StatusOK {
		t.Errorf("enabled 态经运行时闸应放行（200），实际 %d", code)
	}
	if count := demoAuditCount(t, db, demoName, cli.ActionEnable, cli.AuditSuccess); count != 1 {
		t.Errorf("应有 1 条 enable/success 审计，实际 %d", count)
	}

	// ===== ④' 认证与权限面：manifest 声明的节点必须**真的**是鉴权点（发现项 1/5 的处置）=====
	// ① 源码形态：插件包的 mount 为 /overview 挂了 AdminAuth + AdminPermission
	//    （宿主只给「子组 + 运行时闸」，闸只管 enabled；节点鉴权必须插件自挂——与宿主基路由/L2 引擎同款）
	demoAssertMountMiddleware(t, env.Abs(path.Join(cli.PluginsDir, demoName)))
	// ② 行为面：全部走**真实中间件 + 真实会话**（Bearer → 令牌 → 账号状态 → 会话 → 节点），
	//    夹具（admins 行 + 令牌 + 角色组 + access 行）自建自清；不注入会话
	overviewPath := demoRoutePaths(demoName)[0]
	if got := demoProbe(t, overviewPath, middleware.AdminAuth(), middleware.AdminPermission()); got.Code != http.StatusUnauthorized {
		t.Errorf("不带凭据访问插件路由应被 AdminAuth 拦下（业务码 401），实际 HTTP=%d code=%d", got.HTTP, got.Code)
	}
	fixture := demoNewPermissionFixture(t, db, env.Prefix, demoName)
	denied := demoProbe(t, overviewPath, demoBearerInjector(fixture.raw), middleware.AdminAuth(), middleware.AdminPermission())
	if denied.Code != http.StatusForbidden {
		t.Errorf("非授权管理员（组内无该节点）访问插件路由应 403，实际 HTTP=%d code=%d", denied.HTTP, denied.Code)
	}
	overviewNode := demoRuleByName(rows, "plugin/"+demoName+"/overview")
	if overviewNode == nil {
		t.Fatalf("缺节点行 plugin/%s/overview（实际 %v）", demoName, demoRuleNames(rows))
	}
	if _, err := gorm.G[model.AdminGroup](db).Where("id = ?", fixture.groupID).
		Update(ctx, "rules", strconv.FormatUint(uint64(overviewNode.ID), 10)); err != nil {
		t.Fatalf("授予节点失败: %v", err)
	}
	granted := demoProbe(t, overviewPath, demoBearerInjector(fixture.raw), middleware.AdminAuth(), middleware.AdminPermission())
	if granted.Code != 0 {
		t.Errorf("授予节点后应放行（成功码 0）——节点生效，实际 HTTP=%d code=%d", granted.HTTP, granted.Code)
	}

	// ===== ⑤ L2 实体结构（F1：由安装编排的 ④b 建出，用例只做断言）+ 引擎级六动作 CRUD =====
	spec := demoSpecFromMetadata(t, db, demoName)
	demoAssertInstallBuiltStructure(t, db, engine, spec)
	entityTable := spec.PhysicalTableName()
	columns := tableColumns(t, db, entityTable)
	for _, want := range []string{"id", "title", "status", "code", "weigh", "deleted_at", "created_at", "updated_at"} {
		if _, ok := columns[want]; !ok {
			t.Errorf("列 %s 应存在，实际 %v", want, columns)
		}
	}
	indexes := indexNames(t, db, entityTable)
	for _, want := range []string{
		demoIndexName(t, demoName, "uk", "code"),        // 软删部分唯一
		demoIndexName(t, demoName, "idx", "weigh"),      // drag_sort 的排序列
		demoIndexName(t, demoName, "idx", "deleted_at"), // 软删列
	} {
		if _, ok := indexes[want]; !ok {
			t.Errorf("索引 %s 应一次到位（SQL 纪律：新表索引一次到位），实际 %v", want, indexes)
		}
	}
	demoExerciseEngineCRUD(t, engine, spec)

	// ===== ⑥ disable：菜单隐藏 + 闸拒（≤1s 生效：测试里缓存取 1ns，等价于「立即」）=====
	if err := cli.Disable(ctx, env, demoName, demoOperator); err != nil {
		t.Fatalf("停用应成功: %v", err)
	}
	out.Reset()
	demoAssertLedger(t, db, demoName, host.StatusDisabled)
	demoAssertMenuStatus(t, db, demoName, 0)
	if code := demoGateProbe(t, demoName); code != http.StatusNotFound {
		t.Errorf("disabled 态经运行时闸应 404（不承认存在性），实际 %d", code)
	}
	if count := demoAuditCount(t, db, demoName, cli.ActionDisable, cli.AuditSuccess); count != 1 {
		t.Errorf("应有 1 条 disable/success 审计，实际 %d", count)
	}

	// ===== ⑥' 启（恢复，无需重启）：disabled → enabled，菜单与闸即时恢复（A：停 → **启** → 卸载）=====
	if err := cli.Enable(ctx, env, demoName, demoOperator); err != nil {
		t.Fatalf("再次启用应成功（恢复无需重启）: %v", err)
	}
	out.Reset()
	demoAssertLedger(t, db, demoName, host.StatusEnabled)
	demoAssertMenuStatus(t, db, demoName, 1)
	if code := demoGateProbe(t, demoName); code != http.StatusOK {
		t.Errorf("恢复启用后闸应重新放行（200），实际 %d", code)
	}
	if count := demoAuditCount(t, db, demoName, cli.ActionEnable, cli.AuditSuccess); count != 2 {
		t.Errorf("应有 2 条 enable/success 审计（首次启用 + 恢复），实际 %d", count)
	}
	// 卸载前必须回到非启用态（「先禁用后卸载」是硬门禁）
	if err := cli.Disable(ctx, env, demoName, demoOperator); err != nil {
		t.Fatalf("卸载前停用应成功: %v", err)
	}
	out.Reset()
	demoAssertLedger(t, db, demoName, host.StatusDisabled)
	if code := demoGateProbe(t, demoName); code != http.StatusNotFound {
		t.Errorf("卸载前停用后闸应 404，实际 %d", code)
	}

	// 复装凭据：卸载会删源码，故先从**已装源码（唯一事实源）**打包留档
	pkg, err := cli.Export(env, demoName, "")
	if err != nil {
		t.Fatalf("打包应成功: %v", err)
	}
	if !strings.Contains(pkg, filepath.FromSlash(cli.StoreDir)) {
		t.Errorf("包应落 %s（卸载不带走，复装凭它）: %s", cli.StoreDir, pkg)
	}
	out.Reset()

	// 角色组悬挂 ID（卸载清理面）：自建自清的一条组（哨兵必须留下）
	ruleIDs := make([]string, 0, len(rows)+1)
	for i := range rows {
		ruleIDs = append(ruleIDs, strconv.FormatUint(uint64(rows[i].ID), 10))
	}
	dangling := strings.Join(append(ruleIDs, demoSentinelRuleID), ",")
	group := model.AdminGroup{Name: demoName + "-e2e-" + strconv.Itoa(os.Getpid()), Rules: &dangling, Status: demoUint8Ptr(1)}
	if err := gorm.G[model.AdminGroup](db).Create(ctx, &group); err != nil {
		t.Fatalf("建测试角色组失败: %v", err)
	}
	t.Cleanup(func() {
		if err := db.WithContext(context.Background()).Exec(`DELETE FROM "`+env.Prefix+`admin_groups" WHERE "id" = ?`, group.ID).Error; err != nil {
			t.Logf("收尾删除测试角色组失败: %v", err)
		}
	})

	// ===== ⑦ uninstall（默认：保留表与版本表与 .store）=====
	if err := cli.Uninstall(ctx, env, demoName, cli.UninstallOptions{Operator: demoOperator}); err != nil {
		t.Fatalf("卸载应成功: %v", err)
	}
	out.Reset()
	demoAssertZeroResidue(t, env, db, demoName)
	for _, table := range []string{entityTable, notesTable} {
		if !tableExists(t, db, table) {
			t.Errorf("未 --purge 时表 %s 应保留（数据复装即用）", table)
		}
	}
	if count := demoRowCount(t, db, entityTable); count != 3 {
		t.Errorf("未 --purge 时 L2 实体表数据应保留（3 行，含 1 行软删），实际 %d", count)
	}
	if count := demoRowCount(t, db, notesTable); count != 1 {
		t.Errorf("未 --purge 时迁移自管表数据应保留（1 行种子），实际 %d", count)
	}
	if version, dirty := demoMigrationState(t, db, demoName); version != 1 || dirty {
		t.Errorf("未 --purge 时版本表应保留且干净：version=%d dirty=%v", version, dirty)
	}
	if _, err := os.Stat(pkg); err != nil {
		t.Errorf("未 --purge 时应保留 .store 归档（本地复装凭据）: %v", err)
	}
	updated, err := gorm.G[model.AdminGroup](db).Select("rules").Where("id = ?", group.ID).First(ctx)
	if err != nil {
		t.Fatalf("读取角色组失败: %v", err)
	}
	if updated.Rules == nil || *updated.Rules != demoSentinelRuleID {
		t.Errorf("角色组 rules 应写回清除悬挂 ID（只留哨兵 %s），实际 %v", demoSentinelRuleID, updated.Rules)
	}
	if count := demoAuditCount(t, db, demoName, cli.ActionUninstall, cli.AuditSuccess); count != 1 {
		t.Errorf("应有 1 条 uninstall/success 审计，实际 %d", count)
	}
	// 验收子步①：git status 口径——删文件的卸载在入库仓里表现为
	// **只出现本插件路径的删除项**（无其它改动、无孤儿文件）
	demoAssertGitStatusDeletionsOnly(t, env.Root, demoName)

	// ===== ⑧ 复装（凭 .store 包；数据仍在 ⇒ 迁移幂等）=====
	if err := cli.Install(ctx, env, pkg, cli.InstallOptions{AllowUnsigned: true, Operator: demoOperator}); err != nil {
		t.Fatalf("复装应成功（包源 + 迁移幂等）: %v", err)
	}
	out.Reset()
	reinstalled := demoAssertLedger(t, db, demoName, host.StatusInstalled)
	if reinstalled.Source != filepath.Base(pkg) {
		t.Errorf("包源安装的台账 source 应为包文件名 %q，实际 %q", filepath.Base(pkg), reinstalled.Source)
	}
	if len(reinstalled.Checksum) != 64 {
		t.Errorf("包源安装应落整包 sha256，实际 %q", reinstalled.Checksum)
	}
	// 验收子步③：复装后菜单/节点 status 必须重归 0（隐藏至 enable），且 10 条不重不漏
	reinstalledRows := demoRules(t, db, demoName)
	if len(reinstalledRows) != 10 {
		t.Errorf("复装应重写 10 条菜单/节点（含公式代写），实际 %d: %v", len(reinstalledRows), demoRuleNames(reinstalledRows))
	}
	if dup := demoDuplicateRuleNames(reinstalledRows); len(dup) != 0 {
		t.Errorf("复装后归属行出现同名规则：%v", dup)
	}
	demoAssertMenuStatus(t, db, demoName, 0)
	demoAssertEntityPage(t, reinstalledRows, demoName)
	// 验收子步④：重编译等价（复装后接线文件 + 登记常量与清单一致；本进程未重编译 ⇒ 未接线）
	demoAssertRecompileSemantics(t, env, demoName, "1.0.0", "mixed")
	if version, dirty := demoMigrationState(t, db, demoName); version != 1 || dirty {
		t.Errorf("复装后版本表应仍为 1 且干净（迁移幂等）：version=%d dirty=%v", version, dirty)
	}
	reSpec := demoSpecFromMetadata(t, db, demoName)
	if !tableExists(t, db, entityTable) {
		t.Fatalf("复装后 L2 实体表应仍在（复用保留的表）")
	}
	if count := demoRowCount(t, db, entityTable); count != 3 {
		t.Errorf("复装后 L2 实体数据应仍在（3 行），实际 %d", count)
	}
	if count := demoRowCount(t, db, notesTable); count != 1 {
		t.Errorf("复装后迁移自管表数据应仍在（1 行），实际 %d", count)
	}
	again, err := engine.BuildPlan(ctx, reSpec)
	if err != nil {
		t.Fatalf("复装后的结构计划失败: %v", err)
	}
	if !again.Existing || len(again.Statements) != 0 {
		t.Errorf("复装应以**表结构自省**为基线判定结构齐备（0 条语句），实际 existing=%v:\n%s", again.Existing, joinStatements(again))
	}
	if err := assertPlacedDemo(t, env, demoName); err != nil {
		t.Errorf("复装后落位面不符: %v", err)
	}

	// ===== ⑨ uninstall --purge --yes（表与版本表消失；.store 仍保留）=====
	if err := cli.Uninstall(ctx, env, demoName, cli.UninstallOptions{Purge: true, Yes: true, Operator: demoOperator}); err != nil {
		t.Fatalf("--purge 卸载应成功: %v", err)
	}
	out.Reset()
	demoAssertZeroResidue(t, env, db, demoName)
	for _, table := range append([]string{entityTable, notesTable}, cli.MigrationTableName(demoName, env.Prefix)) {
		if tableExists(t, db, table) {
			t.Errorf("--purge 后表 %s 应消失（含迁移 down 回滚与版本表 drop）", table)
		}
	}
	if _, err := os.Stat(pkg); err != nil {
		t.Errorf("--purge 仍应保留 .store 归档（仅收尾清理会删它）: %v", err)
	}
	if count := demoAuditCount(t, db, demoName, cli.ActionUninstall, cli.AuditSuccess); count != 2 {
		t.Errorf("应有 2 条 uninstall/success 审计（默认卸载 + --purge 卸载），实际 %d", count)
	}
}

// TestDemoAcceptanceFailurePath_DB 反向路径（「故意让 ⑥ 迁移失败 ⇒ 状态 failed ⇒ 清理干净」）：
// 唯一命名 + 临时源（仓里的 demo 一个字节都不动），把 1_init.up.sql 换成必失败的 SQL：
//
//	install 失败 ⇒ 现场保留（failed + 归属行 + 落位文件 + dirty 版本表）
//	→ migrate-force 归位（dirty 修复入口）
//	→ uninstall --force --purge --yes（跳过「先禁用后卸载」门禁）⇒ 零残留 + 版本表消失。
func TestDemoAcceptanceFailurePath_DB(t *testing.T) {
	env, db, src := demoCopySetup(t, "fail")
	ctx := context.Background()
	name := path.Base(filepath.ToSlash(src))
	out := env.Out.(*bytes.Buffer)

	broken := "CREATE TABLE \"__PREFIX__plugin_" + name + "_broken\" (\n    \"id\" bigint\n"
	if err := os.WriteFile(filepath.Join(src, "migrations", "1_init.up.sql"), []byte(broken), 0o644); err != nil {
		t.Fatalf("注入迁移失败用例失败: %v", err)
	}

	if err := cli.Install(ctx, env, src, cli.InstallOptions{Operator: demoOperator}); err == nil {
		t.Fatal("迁移失败应报错（不吞错、不静默 force）")
	}
	out.Reset()

	// 现场保留：failed + 归属行 + 落位文件 + dirty 版本表 + failed 审计
	demoAssertLedger(t, db, name, host.StatusFailed)
	if count, err := gorm.G[model.AdminRule](db).Where("plugin = ?", name).Count(ctx, "*"); err != nil || count != 10 {
		t.Errorf("现场保留时归属行应在（10 条），实际 %d err=%v", count, err)
	}
	if _, err := os.Stat(env.Abs(path.Join(cli.PluginDir(name), manifest.FileName))); err != nil {
		t.Errorf("现场保留时落位文件应在: %v", err)
	}
	if version, dirty := demoMigrationState(t, db, name); version != 1 || !dirty {
		t.Errorf("版本表应停在 dirty（版本 1）：version=%d dirty=%v", version, dirty)
	}
	if count := demoAuditCount(t, db, name, cli.ActionInstall, cli.AuditFailed); count == 0 {
		t.Error("应留下 install/failed 审计（含失败步骤与现场状态）")
	}
	// dirty 态拒绝对回滚：--purge 必须先由人归位（不静默 force）
	purgeErr := cli.Uninstall(ctx, env, name, cli.UninstallOptions{Force: true, Purge: true, Yes: true, Operator: demoOperator})
	if purgeErr == nil || !strings.Contains(purgeErr.Error(), "dirty") {
		t.Errorf("dirty 态 --purge 应被拒并点名 dirty，实际 %v", purgeErr)
	}
	out.Reset()

	// 归位（-1 = 无版本），再按固定顺序清理
	if err := cli.MigrateForce(env, name, -1); err != nil {
		t.Fatalf("migrate-force 应成功: %v", err)
	}
	out.Reset()
	if err := cli.Uninstall(ctx, env, name, cli.UninstallOptions{Force: true, Purge: true, Yes: true, Operator: demoOperator}); err != nil {
		t.Fatalf("failed 态的 --force --purge 清理应成功: %v", err)
	}
	out.Reset()
	demoAssertZeroResidue(t, env, db, name)
	for _, table := range demoOwnedTables(env.Prefix, name) {
		if tableExists(t, db, table) {
			t.Errorf("清理后表 %s 应消失（--purge 覆盖版本表与实体表）", table)
		}
	}
	if count := demoAuditCount(t, db, name, cli.ActionUninstall, cli.AuditSuccess); count != 1 {
		t.Errorf("应有 1 条 uninstall/success 审计（被拒的 --purge 只留 rejected），实际 %d", count)
	}
}

// TestDemoAcceptanceB_DB 验收 B（「端到端验收标准 B」；L2 声明式链路）：
//
//	install → enable（闸放行）→ 安装的 ④b 建表 + 六动作建数据 → 升级 v2 加列（**自省基线**：dry-run
//	列出 ALTER，落地由安装链的 ④b 执行）→ 立即生效无需重启（字段来自元数据）→ disable + uninstall
//	（默认保留表与数据）→ 复装 v2（表结构自省**补齐新列**）→ uninstall --purge --yes（表消失）。
//
// 唯一命名 + 临时源：仓里的 demo 不动；v2 = 拷贝源的清单升版本号 + 实体加 remark 列（允许的唯一列级变更）。
// 验收子步②与④在这里：升级面的 dry-run 逐条列出 ALTER；v2 落地后**本进程未重启**也看到新列（元数据按请求读库）；
// 「重编译语义等价」用注册行 + 登记常量断言（demoAssertRecompileSemantics）。
func TestDemoAcceptanceB_DB(t *testing.T) {
	env, db, src := demoCopySetup(t, "b")
	ctx := context.Background()
	name := path.Base(filepath.ToSlash(src))
	engine := New(db)
	out := env.Out.(*bytes.Buffer)

	// ===== 装（v1）=====
	if err := cli.Install(ctx, env, src, cli.InstallOptions{Operator: demoOperator}); err != nil {
		t.Fatalf("安装应成功: %v", err)
	}
	out.Reset()
	demoAssertLedger(t, db, name, host.StatusInstalled)
	if count, err := gorm.G[model.AdminRule](db).Where("plugin = ?", name).Count(ctx, "*"); err != nil || count != 10 {
		t.Errorf("应代写 10 条菜单/节点（含公式代写），实际 %d err=%v", count, err)
	}
	demoAssertMenuStatus(t, db, name, 0)
	demoAssertEntityPage(t, demoRules(t, db, name), name)

	// ===== enable（缺 enable 则菜单不出现、接口全 404）=====
	if err := cli.Enable(ctx, env, name, demoOperator); err != nil {
		t.Fatalf("启用应成功: %v", err)
	}
	out.Reset()
	demoAssertMenuStatus(t, db, name, 1)
	if code := demoGateProbe(t, name); code != http.StatusOK {
		t.Errorf("enabled 态经运行时闸应放行（200），实际 %d", code)
	}

	// ===== 安装的 ④b 建表（F1：DDL 在安装编排放行）+ 引擎级六动作建数据 =====
	specV1 := demoSpecFromMetadata(t, db, name)
	demoAssertInstallBuiltStructure(t, db, engine, specV1)
	entityTable := specV1.PhysicalTableName()
	demoExerciseEngineCRUD(t, engine, specV1)
	if _, ok := tableColumns(t, db, entityTable)[demoRemarkField().Name]; ok {
		t.Fatalf("v1 表不应有 %s 列（升级前基线）", demoRemarkField().Name)
	}

	// ===== 升级 v2 加列（自省基线）：清单升版本 + 加一列 =====
	demoMutateManifest(t, src, func(m *manifest.Manifest) {
		m.Version = "2.0.0"
		m.Entities[0].Fields = append(m.Entities[0].Fields, demoRemarkField())
	})
	demoBumpRegisterVersion(t, src, "1.0.0", "2.0.0")
	loaded, _, err := manifest.Load(src, manifest.Options{Prefix: prefixOf(), HostVersion: version.Get().Version})
	if err != nil {
		t.Fatalf("装载 v2 源清单失败: %v", err)
	}
	if loaded.Version != "2.0.0" || len(loaded.Entities) != 1 {
		t.Fatalf("v2 源清单不符: version=%s entities=%d", loaded.Version, len(loaded.Entities))
	}
	// 验收子步②：升级面的 **dry-run 逐条列出 ALTER TABLE**（-B），且零副作用。
	// 预演用 --force：v1 的落位副本与 v2 源内容不同 ⇒「同名逐项确认」口径默认拒绝；
	// 预演本身只读（不改台账、不动表结构）。
	out.Reset()
	if err := cli.Install(ctx, env, src, cli.InstallOptions{DryRun: true, Force: true, Operator: demoOperator}); err != nil {
		t.Fatalf("v2 的 --dry-run 应成功（升级预演）: %v", err)
	}
	dryText := out.String()
	if !strings.Contains(dryText, `ALTER TABLE "`+entityTable+`" ADD COLUMN IF NOT EXISTS "`+demoRemarkField().Name+`"`) {
		t.Errorf("升级 dry-run 的 ⑤ 段应逐条列出 ALTER TABLE:\n%s", dryText)
	}
	ddlSection := dryText
	if start := strings.Index(ddlSection, "== ⑤"); start >= 0 {
		ddlSection = ddlSection[start:]
	}
	if end := strings.Index(ddlSection, "== ⑥"); end >= 0 {
		ddlSection = ddlSection[:end]
	}
	// 只扫**语句行**（render 的 `  [kind] SQL` 形态）并只禁明列的两类破坏性自动动作——删表与删列
	// （只进报告）。两处口径要点：① ⑤ 段里的 ⚠ 告警散文不是引擎生成的 DDL（既有表接管提示里就有
	// 「--purge 也会 DROP 它」），按整段文本 grep "DROP" 会被正当告警打红；② DROP INDEX 与 DROP DEFAULT
	// 都在允许面内（「允许 加/删索引」；「放宽类变更」= DROP DEFAULT），不能一并打红。
	for _, line := range strings.Split(ddlSection, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "[") {
			continue
		}
		if strings.Contains(trimmed, "DROP TABLE") || strings.Contains(trimmed, "DROP COLUMN") {
			t.Errorf("升级预演不得自动删表/删列（只进报告）: %s", trimmed)
		}
	}
	if _, ok := tableColumns(t, db, entityTable)[demoRemarkField().Name]; ok {
		t.Errorf("dry-run 不得执行 DDL（%s 列不该出现）", demoRemarkField().Name)
	}
	// dry-run 只读：台账**状态与版本都不动**——此刻插件正处于 cli.Enable 之后的 enabled 态，
	// 预演不得把它改回 installed/其它值，版本也应仍停在 1.0.0
	if ledger := demoAssertLedger(t, db, name, host.StatusEnabled); ledger.Version != "1.0.0" {
		t.Errorf("dry-run 不应改台账版本（应仍为 1.0.0），实际 %s", ledger.Version)
	}
	out.Reset()

	// ===== 卸载留表（默认卸载：表与数据保留，元数据/菜单/行清零）=====
	if err := cli.Disable(ctx, env, name, demoOperator); err != nil {
		t.Fatalf("停用应成功: %v", err)
	}
	out.Reset()
	if err := cli.Uninstall(ctx, env, name, cli.UninstallOptions{Operator: demoOperator}); err != nil {
		t.Fatalf("卸载应成功: %v", err)
	}
	out.Reset()
	demoAssertZeroResidue(t, env, db, name)
	if !tableExists(t, db, entityTable) || demoRowCount(t, db, entityTable) != 3 {
		t.Errorf("未 --purge 时实体表与数据应保留：exists=%v", tableExists(t, db, entityTable))
	}
	if version, dirty := demoMigrationState(t, db, name); version != 1 || dirty {
		t.Errorf("未 --purge 时版本表应保留：version=%d dirty=%v", version, dirty)
	}

	// ===== 复装 v2：表结构自省**补齐新列**（由安装链的 ④b 执行，不再由用例驱动引擎）=====
	// 保留下来的表正是 v1 结构（无 remark 列）——复装新版必须按自省基线把列补回来
	out.Reset()
	if err := cli.Install(ctx, env, src, cli.InstallOptions{Operator: demoOperator}); err != nil {
		t.Fatalf("复装 v2 应成功: %v", err)
	}
	installText := out.String()
	if !strings.Contains(installText, `ADD COLUMN IF NOT EXISTS "`+demoRemarkField().Name+`"`) {
		t.Errorf("复装 v2 的 ④b 应按自省基线补齐新列（输出里应逐条回报 ADD COLUMN）:\n%s", installText)
	}
	out.Reset()
	reinstalled := demoAssertLedger(t, db, name, host.StatusInstalled)
	if reinstalled.Version != "2.0.0" {
		t.Errorf("复装后台账版本应为 2.0.0，实际 %s", reinstalled.Version)
	}
	if version, dirty := demoMigrationState(t, db, name); version != 1 || dirty {
		t.Errorf("复装后版本表应仍为 1 且干净（插件迁移与 L2 结构无关）：version=%d dirty=%v", version, dirty)
	}
	if _, ok := tableColumns(t, db, entityTable)[demoRemarkField().Name]; !ok {
		t.Fatalf("复装后 %s 列应存在（补齐新列）", demoRemarkField().Name)
	}
	if count := demoRowCount(t, db, entityTable); count != 3 {
		t.Errorf("补齐新列不得丢数据：应仍为 3 行，实际 %d", count)
	}
	demoAssertInstallBuiltStructure(t, db, engine, demoSpecFromMetadata(t, db, name))

	// ===== 验收子步②（后段）：升级**立即生效、无需重启**——字段来自元数据（按请求读库）=====
	// 引擎的 Resolve 走与生产同一条按请求读库的路径（测试里缓存压到 1ns），本进程未重启：
	live, err := engine.Resolve(ctx, name, demoEntity)
	if err != nil {
		t.Fatalf("按请求装载实体元数据失败: %v", err)
	}
	if len(live.Fields()) != len(specV1.Fields())+1 {
		t.Fatalf("元数据应即时带上新列（v2 spec），实际字段数 %d（v1 为 %d）", len(live.Fields()), len(specV1.Fields()))
	}
	handler := entityFor(engine, live)
	c, recorder := testContext(http.MethodPost, "/x", `{"page":1,"limit":10}`, nil)
	handler.List(c)
	payload := decodeResponse(t, recorder)
	if payload.Code != 0 {
		t.Fatalf("升级后列表失败: %s", payload.Message)
	}
	meta, _ := payload.Data["meta"].(map[string]any)
	columns, _ := meta["columns"].([]any)
	hasRemark := false
	for _, item := range columns {
		if column, ok := item.(map[string]any); ok && column["prop"] == demoRemarkField().Name {
			hasRemark = true
		}
	}
	if !hasRemark {
		t.Errorf("升级后列表元数据应即时带上新列 %s（无重启；字段来自元数据）:\n%v", demoRemarkField().Name, columns)
	}
	// 验收子步④：重编译语义等价（v2 版：注册行 + 登记常量 = 2.0.0；本进程未重编译 ⇒ 未接线）
	demoAssertRecompileSemantics(t, env, name, "2.0.0", "mixed")

	// ===== --purge 删表 =====
	if err := cli.Uninstall(ctx, env, name, cli.UninstallOptions{Purge: true, Yes: true, Operator: demoOperator}); err != nil {
		t.Fatalf("--purge 卸载应成功: %v", err)
	}
	out.Reset()
	demoAssertZeroResidue(t, env, db, name)
	for _, table := range demoOwnedTables(env.Prefix, name) {
		if tableExists(t, db, table) {
			t.Errorf("--purge 后表 %s 应消失", table)
		}
	}
}

// ---------- 小工具 ----------

// demoAssertGitStatusDeletionsOnly 验收子步①：验收 A 的「git status 口径」（/）——
// 删文件的卸载在入库仓里表现为**只出现本插件路径的删除项**（无其它改动、无孤儿文件）。
//
// 口径与边界（如实）：
//   - 只在「是 git 工作树且 git 可执行」时作数：否则 skip（该断言只在能证明的形态下有结论）；
//   - pathspec 限定**本插件自己的路径**（源码树 + 视图副本 + 各语言目录下的语言包副本）：逐行必须是
//     从索引或工作区删除（"D "/" D"）；出现修改（M）或未跟踪（??）即报红——后者就是「孤儿文件」的信号；
//   - **不判仓库全局是否干净**：其它并行的未提交改动不属本用例的举证面（断言只对插件路径说话），
//     接线文件 internal/plugins/register_gen.go 的还原由 demoAssertZeroResidue 的注册行断言负责。
func demoAssertGitStatusDeletionsOnly(t *testing.T, root, name string) {
	t.Helper()
	paths := []string{cli.PluginDir(name), cli.ViewTarget(name)}
	langDirs, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(cli.WebLangRoot), "*"))
	if err != nil {
		t.Fatalf("扫描语言目录失败: %v", err)
	}
	for _, dir := range langDirs {
		if info, statErr := os.Stat(dir); statErr != nil || !info.IsDir() {
			continue
		}
		paths = append(paths, path.Join(cli.WebLangRoot, filepath.Base(dir), "plugin", name+".yaml"))
	}

	args := append([]string{"-C", root, "status", "--porcelain", "--untracked-files=all", "--"}, paths...)
	output, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) || strings.Contains(string(output), "not a git repository") {
			t.Skipf("非 git 工作树（或 git 不可用）：跳过 git status 口径断言（%v / %s）", err, strings.TrimSpace(string(output)))
		}
		t.Fatalf("git status 失败: %v（%s）", err, strings.TrimSpace(string(output)))
	}

	deleted := 0
	offenders := make([]string, 0)
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(line) < 3 { // 形态异常：原样列进违规行
			offenders = append(offenders, line)
			continue
		}
		switch line[:2] {
		case "D ", " D", "DD":
			deleted++
		default:
			offenders = append(offenders, line)
		}
	}
	if len(offenders) > 0 {
		t.Errorf("卸载后插件路径下应**只有删除项**（无其它改动、无孤儿文件），实际还有：\n  %s", strings.Join(offenders, "\n  "))
	}
	if deleted == 0 {
		t.Errorf("插件 %s 的入库文件应表现为删除项（本用例卸载删了 %s、%s 与语言包副本）——git status 无删除行说明播种面不符",
			name, cli.PluginDir(name), cli.ViewTarget(name))
	}
}

// demoIndexName 引擎索引名（公式与 ddl.go 的 indexName 同一处实现，避免测试自带一套命名规则）
func demoIndexName(t *testing.T, name, kind, suffix string) string {
	t.Helper()
	index, err := indexName(prefixOf(), name, demoEntity, kind, suffix)
	if err != nil {
		t.Fatalf("计算索引名失败: %v", err)
	}
	return index
}

// demoFirstLine 首行（错误信息用）
func demoFirstLine(text string) string {
	if idx := strings.IndexByte(text, '\n'); idx >= 0 {
		return text[:idx]
	}
	return text
}

// assertPlacedDemo 复装后的落位面（源码 + 视图副本 + 语言包副本 + 归属标记）
func assertPlacedDemo(t *testing.T, env *cli.Env, name string) error {
	t.Helper()
	content := demoReadFile(t, env.Abs(path.Join(cli.PluginDir(name), manifest.FileName)))
	if !strings.HasPrefix(string(content), "# plugin: "+name+" v1.0.0\n") {
		return fmt.Errorf("%s 的文件头应带归属标记，实际 %q", cli.PluginDir(name), demoFirstLine(string(content)))
	}
	if _, err := os.Stat(env.Abs(path.Join(cli.PluginDir(name), "web", "views", "index.vue"))); err != nil {
		return fmt.Errorf("源树 %s/web/views/index.vue 应存在: %w", cli.PluginDir(name), err)
	}
	if _, err := os.Stat(env.Abs(path.Join(cli.ViewTarget(name), "index.vue"))); err != nil {
		return fmt.Errorf("落位副本 %s 应存在: %w", cli.ViewTarget(name), err)
	}
	if targets := demoLangTargets(t, env.Root, name); len(targets) != 2 {
		return fmt.Errorf("语言包副本应有 2 份（zh-cn/en），实际 %v", targets)
	}
	return nil
}

// ---------- 路由的认证与权限面（发现项 1/5 的处置工具） ----------

// demoProbeResult 探针读数：HTTP 状态 + 统一响应体的业务码。
// 本项目的认证/授权失败走「HTTP 200 + 业务码 401/403/503」（internal/kit/httpx 恒用 http.StatusOK，
// 分类码在 body 的 code 里，见 internal/middleware/auth_failure.go:65-80）；只有运行时闸是 HTTP 404。
// 故两个都要读：探针的 handler 用 httpx.Success（成功码 0），与失败码可判别。
type demoProbeResult struct {
	HTTP int
	Code int
}

// demoProbe 构造「单路由 + 真实中间件链」的探针（handler 固定为 httpx.Success），返回 HTTP 状态与业务码。
func demoProbe(t *testing.T, fullPath string, middlewares ...gin.HandlerFunc) demoProbeResult {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	chain := append(append([]gin.HandlerFunc{}, middlewares...), func(c *gin.Context) { httpx.Success(c) })
	router.POST(fullPath, chain...)

	request := httptest.NewRequest(http.MethodPost, fullPath, nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return demoProbeResult{HTTP: recorder.Code, Code: decodeResponse(t, recorder).Code}
}

// demoBearerInjector 把 Bearer 头放进请求（模拟前端请求层的 Authorization 注入；必须排在 AdminAuth 之前）
func demoBearerInjector(rawToken string) gin.HandlerFunc {
	return func(c *gin.Context) { c.Request.Header.Set("Authorization", "Bearer "+rawToken) }
}

// demoPermissionFixture 权限探针夹具的读数（角色组 ID / 原始令牌）
type demoPermissionFixture struct {
	groupID uint
	raw     string
}

// demoNewPermissionFixture 建权限探针夹具并把清理登记到 t.Cleanup：
//   - **必须建真管理员行**：`admin_group_access.uid` 有指向 `admins(id)` 的外键
//     （框架数据库迁移的 fk_admin_group_access_uid），
//     借用一个不存在的 uid 会被外键拒绝（P6 实测：SQLSTATE 23503）；
//   - 令牌用宿主自己的令牌服务签发（入库即 SHA256，与 AdminAuth 的取令牌口径同源）
//     ⇒ 探针走「Bearer → 令牌 → 账号状态 → 会话 → 节点」的完整真实链路，不注入会话；
//   - 用户名/组名带进程号：与既有行不撞车；收尾按 ID/UID 精确删除自己那几行。
func demoNewPermissionFixture(t *testing.T, db *gorm.DB, prefix, name string) demoPermissionFixture {
	t.Helper()
	ctx := context.Background()
	suffix := strconv.Itoa(os.Getpid())

	probeAdmin := model.Admin{Username: name + "-perm-" + suffix, Nickname: "插件权限探针", Status: "enable"}
	if err := gorm.G[model.Admin](db).Create(ctx, &probeAdmin); err != nil {
		t.Fatalf("建权限探针管理员失败: %v", err)
	}
	rules := ""
	probeGroup := model.AdminGroup{Name: name + "-perm-" + suffix, Rules: &rules, Status: demoUint8Ptr(1)}
	if err := gorm.G[model.AdminGroup](db).Create(ctx, &probeGroup); err != nil {
		t.Fatalf("建权限探针角色组失败: %v", err)
	}
	access := model.AdminGroupAccess{UID: probeAdmin.ID, GroupID: probeGroup.ID}
	if err := gorm.G[model.AdminGroupAccess](db).Create(ctx, &access); err != nil {
		t.Fatalf("建权限探针 access 行失败: %v", err)
	}
	raw := fmt.Sprintf("plugin-e2e-%s-%d", name, os.Getpid())
	tokenRow := model.Token{Token: raw, Type: token.TypeAdminLogin, UserID: probeAdmin.ID, ExpiredAt: time.Now().Add(time.Hour)}
	if err := token.Manager().Create(ctx, &tokenRow); err != nil {
		t.Fatalf("签发权限探针令牌失败: %v", err)
	}

	t.Cleanup(func() {
		cleanupCtx := context.Background()
		steps := []struct {
			what string
			sql  string
			args []any
		}{
			{"access 行", `DELETE FROM "` + prefix + `admin_group_access" WHERE "uid" = ? AND "group_id" = ?`, []any{probeAdmin.ID, probeGroup.ID}},
			{"角色组", `DELETE FROM "` + prefix + `admin_groups" WHERE "id" = ?`, []any{probeGroup.ID}},
			{"令牌", `DELETE FROM "` + prefix + `tokens" WHERE "user_id" = ? AND "type" = ?`, []any{probeAdmin.ID, token.TypeAdminLogin}},
			{"管理员", `DELETE FROM "` + prefix + `admins" WHERE "id" = ?`, []any{probeAdmin.ID}},
		}
		for _, step := range steps {
			if err := db.WithContext(cleanupCtx).Exec(step.sql, step.args...).Error; err != nil {
				t.Logf("收尾删除权限探针%s失败: %v", step.what, err)
			}
		}
	})
	return demoPermissionFixture{groupID: probeGroup.ID, raw: raw}
}

// demoAssertMountMiddleware 源码形态断言：插件包 plugin.go 的 mount() 为 /overview 挂了
// AdminAuth + AdminPermission（宿主只提供「子组 + 运行时闸」；节点鉴权必须插件自己挂，
// 本项目既有口径见 internal/handler/base.go:17-25 与 internal/plugin/l2/router.go:100-104）。
// 行为面由 demoProbe 的真实中间件探针覆盖，本条负责「源码漂移」这一半。
func demoAssertMountMiddleware(t *testing.T, dir string) {
	t.Helper()
	file := filepath.Join(dir, "plugin.go")
	parsed, err := parser.ParseFile(gotoken.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", file, err)
	}

	var chain []string
	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "mount" || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) < 2 {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "POST" {
				return true
			}
			for _, arg := range call.Args[1:] {
				nested, ok := arg.(*ast.CallExpr)
				if !ok {
					continue
				}
				if callee, ok := nested.Fun.(*ast.SelectorExpr); ok {
					chain = append(chain, callee.Sel.Name)
				}
			}
			return true
		})
	}
	for _, want := range []string{"AdminAuth", "AdminPermission"} {
		found := false
		for _, got := range chain {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s 的 mount() 未挂 %s（实际处理链 %v）：宿主只给「子组 + 运行时闸」，"+
				"节点鉴权必须插件自己挂，否则 manifest 声明的节点只是装饰", file, want, chain)
		}
	}
}

// demoDuplicateRuleNames 归属行里重复出现的规则名（安装器代写与 manifest 声明撞车时的探测点）
func demoDuplicateRuleNames(rows []model.AdminRule) []string {
	seen := map[string]int{}
	for i := range rows {
		seen[rows[i].Name]++
	}
	var dup []string
	for name, count := range seen {
		if count > 1 {
			dup = append(dup, name)
		}
	}
	slices.Sort(dup)
	return dup
}
