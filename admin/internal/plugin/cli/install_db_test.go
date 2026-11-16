package cli

// 本文件的库级用例要经 ddlplan 的接口面驱动 L2 引擎（安装编排的 ④b 结构 DDL）⇒ 引擎实现在场：
// 空白导入 internal/plugin/l2 触发其 init 的 ddlplan.Set（与 aigo 二进制经 wiring.go 的接线同源）。
// cli import l2 只出现在**测试二进制**里：l2 不 import cli，故不成环（反向则会触发 cmd/go 的
// 「import cycle not allowed in test」——见 internal/plugin/l2/ddlplan 的包注释）。
import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"
	"github.com/ai-go-hub/ai-go-admin/internal/infra/database"
	"github.com/ai-go-hub/ai-go-admin/internal/model"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"
	_ "github.com/ai-go-hub/ai-go-admin/internal/plugin/l2"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"
	"github.com/ai-go-hub/ai-go-admin/internal/version"

	"github.com/golang-migrate/migrate/v4"
	"gorm.io/gorm"
)

// 安装编排的库级用例（PLUGIN_DB 门控；无该变量默认 `go test` 不连库）：
//
//	PLUGIN_DB=1 go test -count=1 -timeout=180s ./internal/plugin/cli/
//
// 口径：
//   - **文件面落在一次性临时根**（env.Root = t.TempDir()），不动工作树；
//   - **DB 面只用带进程号的测试插件名**（pdbtest{序号}{pid}），用例自建自清（含审计行与插件表）；
//   - 应用 schema 未迁移（admin_rules / admin_groups / configs 缺失）⇒ skip，不猜；
//   - 插件机制三表缺失 ⇒ 用例组自带 up 推进到插件迁移版本（只前进不回滚，与 host 包同口径）。
const pluginDBTestNamePrefix = "pdbtest"

// TestPluginInstallUninstallDB 端到端（验收 A 的 L1 主链）：
//
//	安装（④–⑧）→ 各面就位断言 → 卸载（保留表与版本表）→ 零残留断言 →
//	复装（迁移幂等）→ --purge 卸载 → 表与版本表消失。
func TestPluginInstallUninstallDB(t *testing.T) {
	env, db := installDBSetup(t, "1")
	ctx := context.Background()
	name := pluginDBName("1")
	prefix := config.Get().Database.Prefix

	if err := Scaffold(env, name, ScaffoldOptions{Kind: "mixed", Title: "编排用例插件"}); err != nil {
		t.Fatalf("脚手架应成功: %v", err)
	}
	// 追加自带配置键（覆盖 ④ 的 configs 分组写入与卸载清理面）
	manifestPath := env.Abs(path.Join(PluginDir(name), "plugin.yaml"))
	content, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	appended := string(content) + "\nconfigs:\n  - {name: retry_limit, title: 重试上限, type: number, value: \"3\"}\n"
	if err := os.WriteFile(manifestPath, []byte(appended), 0o644); err != nil {
		t.Fatal(err)
	}

	// ===== 安装（④–⑧ 执行主线）=====
	if err := Install(ctx, env, name, InstallOptions{Operator: "pdbtest"}); err != nil {
		t.Fatalf("安装应成功: %v", err)
	}

	ledger, ok, err := pluginLedger(ctx, db, name)
	if err != nil || !ok {
		t.Fatalf("台账应有一行: ok=%v err=%v", ok, err)
	}
	if ledger.Status != host.StatusInstalled {
		t.Errorf("安装后台账应为 installed（菜单隐藏），实际 %s", ledger.Status)
	}
	if ledger.Kind != "mixed" || ledger.InstalledAt == nil {
		t.Errorf("台账摘要不符: kind=%s installed_at=%v", ledger.Kind, ledger.InstalledAt)
	}
	if want := "plugins/" + name; ledger.Source != want {
		t.Errorf("台账 source 应为目录源的仓库相对路径 %q，实际 %q", want, ledger.Source)
	}

	rules, err := gorm.G[model.AdminRule](db).Where("plugin = ?", name).Order("id").Find(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 3 条声明（dir/menu/node）+公式代写：实体页 1 条 + 权限节点 5 条（drag_sort ⇒ 含 sort）
	if len(rules) != 9 {
		t.Fatalf("应代写 9 条菜单/节点（声明 3 +公式 6），实际 %d: %v", len(rules), keysOfRules(rules))
	}
	byName := map[string]model.AdminRule{}
	for _, rule := range rules {
		if _, dup := byName[rule.Name]; dup {
			t.Fatalf("归属行出现同名规则 %s（安装器代写与 manifest 声明重复？）", rule.Name)
		}
		byName[rule.Name] = rule
		if rule.Status == nil || *rule.Status != 0 {
			t.Errorf("安装期菜单/节点 status 应为 0（隐藏至 enable）: %s = %v", rule.Name, rule.Status)
		}
		if rule.Keepalive == nil || *rule.Keepalive != 0 {
			t.Errorf("菜单 keepalive 应为 0（组件名与规则名不一致时写 1 会静默不缓存）: %s", rule.Name)
		}
	}
	dirRule, ok := byName["plugin/"+name]
	if !ok {
		t.Fatalf("缺分组目录行 plugin/%s（实际 %v）", name, keysOfRules(rules))
	}
	menuRule, ok := byName["plugin/"+name+"/index"]
	if !ok || menuRule.Pid == nil || *menuRule.Pid != dirRule.ID {
		t.Errorf("菜单 plugin/%s/index 的 pid 应指向分组 %d（实际 %v）", name, dirRule.ID, menuRule.Pid)
	}
	if menuRule.Component == nil || *menuRule.Component != "/src/views/admin/plugin/"+name+"/index.vue" {
		t.Errorf("菜单 component 应精确指向插件页: %v", menuRule.Component)
	}
	//公式代写：实体页（menu）+ 权限节点（node）
	entityPage, ok := byName["plugin/"+name+"/items"]
	if !ok {
		t.Fatalf("缺公式代写的实体页 plugin/%s/items（实际 %v）", name, keysOfRules(rules))
	}
	if entityPage.Type != "menu" || entityPage.Path == nil || *entityPage.Path != "plugin/"+name+"/items" {
		t.Errorf("实体页行应为 menu path=plugin/%s/items，实际 type=%s path=%v", name, entityPage.Type, entityPage.Path)
	}
	if entityPage.Component == nil || *entityPage.Component != EntityRenderComponent {
		t.Errorf("实体页 component 应为宿主通用页 %s，实际 %v", EntityRenderComponent, entityPage.Component)
	}
	if entityPage.Pid == nil || *entityPage.Pid != dirRule.ID {
		t.Errorf("实体页 pid 应指向插件分组 %d，实际 %v", dirRule.ID, entityPage.Pid)
	}
	if entityPage.Keepalive == nil || *entityPage.Keepalive != 0 {
		t.Errorf("实体页 keepalive 应为 0（全部实体共用一个组件，写 1 会静默不缓存），实际 %v", entityPage.Keepalive)
	}
	for _, action := range EntityPageActions(true) { // 骨架的实体 drag_sort: true ⇒ 含 sort
		node, ok := byName["plugin/"+name+"/items/"+action]
		if !ok {
			t.Errorf("缺实体页权限节点 plugin/%s/items/%s（实际 %v）", name, action, keysOfRules(rules))
			continue
		}
		if node.Type != "node" || node.Pid == nil || *node.Pid != entityPage.ID {
			t.Errorf("节点 %s 应为 node 且 pid 指向实体页 %d，实际 type=%s pid=%v", node.Name, entityPage.ID, node.Type, node.Pid)
		}
	}

	entities, err := gorm.G[host.Entity](db).Where("plugin = ?", name).Find(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(entities) != 1 || entities[0].Name != "items" {
		t.Fatalf("实体元数据应为 1 行（items），实际 %+v", entities)
	}
	if want := prefix + "plugin_" + name + "_items"; entities[0].PhysicalTable != want {
		t.Errorf("实体物理表名应为 %q，实际 %q", want, entities[0].PhysicalTable)
	}
	// spec 按**结构**断言（jsonb 列会归一化文本形态：键序、空白，逐字比对必假红）：
	// 与清单声明的实体**逐键等价**——不是抽查几个键，而是两侧 JSON 文档解析后深比对
	// （omitempty 之下键集两边一致：落库值就是清单实体的 json.Marshal 产物）。
	sourceManifest, _, err := manifest.Load(env.Abs(PluginDir(name)), manifest.Options{Prefix: prefix, HostVersion: env.HostVersion})
	if err != nil {
		t.Fatalf("读取已落位插件的清单失败: %v", err)
	}
	if len(sourceManifest.Entities) != 1 {
		t.Fatalf("清单应声明 1 个实体，实际 %d", len(sourceManifest.Entities))
	}
	wantSpec, err := json.Marshal(sourceManifest.Entities[0])
	if err != nil {
		t.Fatal(err)
	}
	var wantSpecValue, gotSpecValue any
	if err := json.Unmarshal(wantSpec, &wantSpecValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(entities[0].Spec, &gotSpecValue); err != nil {
		t.Fatalf("实体 spec 不是合法 JSON: %v（%s）", err, entities[0].Spec)
	}
	if !reflect.DeepEqual(gotSpecValue, wantSpecValue) {
		t.Errorf("实体 spec 应与清单声明的实体逐键一致\n清单: %s\n落库: %s", wantSpec, entities[0].Spec)
	}

	configs, err := gorm.G[model.Config](db).Where(model.Config{Group: "plugin." + name}).Find(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 1 || configs[0].Name != ConfigKey(name, "retry_limit") {
		t.Fatalf("configs 分组应写入 1 行（%s），实际 %+v", ConfigKey(name, "retry_limit"), configs)
	}
	if configs[0].Title != "重试上限" || configs[0].Value == nil || *configs[0].Value != "3" {
		t.Errorf("配置键应带 manifest 声明的标题与初值，实际 title=%q value=%v", configs[0].Title, configs[0].Value)
	}
	if configs[0].AllowDel != 0 {
		t.Errorf("插件配置键 allow_del 应为 0（该列 default=1，零值会被 GORM 跳过 ⇒ 安装器必须显式回写），实际 %d", configs[0].AllowDel)
	}

	// 落位面：源码 + 落位副本 + 归属标记；接线面：register_gen.go 加了本插件一行
	if err := assertPlaced(env, name); err != nil {
		t.Errorf("落位面不符: %v", err)
	}
	registryRaw, err := os.ReadFile(env.Abs(RegistryFile))
	if err != nil {
		t.Fatalf("接线文件应存在: %v", err)
	}
	registered, err := RegisteredPluginsFrom(registryRaw)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(registered, name) {
		t.Errorf("%s 应含 %s 的注册行（实际 %v）", RegistryFile, name, registered)
	}
	// 迁移面：版本表与插件自管的表
	if ver, dirty, err := pluginMigrationState(ctx, db, name); err != nil || dirty || ver != 1 {
		t.Errorf("版本表应为 1 且干净: version=%d dirty=%v err=%v", ver, dirty, err)
	}
	if exists, err := testTableExistsIn(ctx, db, currentSchemaName(ctx, db), prefix+"plugin_"+name+"_sample"); err != nil || !exists {
		t.Errorf("插件迁移建的表应存在: exists=%v err=%v", exists, err)
	}
	// ④b L2 DDL（F1：安装链调用引擎）：实体表由**安装本身**建出（不再由用例手动驱动）
	entityTable := prefix + "plugin_" + name + "_items"
	if exists, err := testTableExistsIn(ctx, db, currentSchemaName(ctx, db), entityTable); err != nil || !exists {
		t.Errorf("安装的 ④b 应建出 L2 实体表 %s（ddl_owner=engine）: exists=%v err=%v", entityTable, exists, err)
	}
	if count := auditCount(t, db, name, ActionInstall, AuditSuccess); count != 1 {
		t.Errorf("应有 1 条 install/success 审计，实际 %d", count)
	}

	// 复装凭据：卸载会把源码目录删掉，故先从**已装源码（唯一事实源）**打出包留档
	pkg, err := Export(env, name, "")
	if err != nil {
		t.Fatalf("打包应成功: %v", err)
	}
	if !strings.Contains(pkg, filepath.FromSlash(StoreDir)) {
		t.Errorf("包应落 %s（卸载不带走，复装凭它）: %s", StoreDir, pkg)
	}

	// ===== 角色组悬挂 ID（卸载清理面）：自建自清的一条组 =====
	dangling := fmt.Sprintf("%d,%d,%d,777001", rules[0].ID, rules[1].ID, rules[2].ID)
	group := model.AdminGroup{Name: name + "-group", Rules: &dangling, Status: uint8Ptr(1)}
	if err := gorm.G[model.AdminGroup](db).Create(ctx, &group); err != nil {
		t.Fatalf("建测试角色组失败: %v", err)
	}
	t.Cleanup(func() {
		db.WithContext(ctx).Exec(`DELETE FROM "`+prefix+`admin_groups" WHERE "id" = ?`, group.ID)
	})

	// ===== 卸载（默认：保留表与版本表）=====
	if err := Uninstall(ctx, env, name, UninstallOptions{Operator: "pdbtest"}); err != nil {
		t.Fatalf("卸载应成功: %v", err)
	}
	assertZeroResidue(t, env, db, name)
	if _, dirty, err := pluginMigrationState(ctx, db, name); err != nil || dirty {
		t.Errorf("未 --purge 时版本表应保留且干净: dirty=%v err=%v", dirty, err)
	}
	if exists, err := testTableExistsIn(ctx, db, currentSchemaName(ctx, db), prefix+"plugin_"+name+"_sample"); err != nil || !exists {
		t.Errorf("未 --purge 时表与数据应保留: exists=%v err=%v", exists, err)
	}
	updated, err := gorm.G[model.AdminGroup](db).Select("rules").Where("id = ?", group.ID).First(ctx)
	if err != nil {
		t.Fatalf("读取角色组失败: %v", err)
	}
	if updated.Rules == nil || *updated.Rules != "777001" {
		t.Errorf("角色组 rules 应写回清除悬挂 ID（保留 777001），实际 %v", updated.Rules)
	}
	if count := auditCount(t, db, name, ActionUninstall, AuditSuccess); count != 1 {
		t.Errorf("应有 1 条 uninstall/success 审计，实际 %d", count)
	}

	// ===== 复装（凭 .store 包恢复源码；数据仍在 ⇒ 迁移幂等；包源需显式 --allow-unsigned）=====
	if err := Install(ctx, env, pkg, InstallOptions{AllowUnsigned: true, Operator: "pdbtest"}); err != nil {
		t.Fatalf("复装应成功（包源 + 迁移幂等）: %v", err)
	}
	reinstalled, ok, err := pluginLedger(ctx, db, name)
	if err != nil || !ok {
		t.Fatalf("复装后台账应有一行: ok=%v err=%v", ok, err)
	}
	if reinstalled.Source != filepath.Base(pkg) {
		t.Errorf("包源安装的台账 source 应为包文件名 %q，实际 %q", filepath.Base(pkg), reinstalled.Source)
	}
	if len(reinstalled.Checksum) != 64 {
		t.Errorf("包源安装应落整包 sha256 到 checksum，实际 %q", reinstalled.Checksum)
	}
	if ver, dirty, err := pluginMigrationState(ctx, db, name); err != nil || dirty || ver != 1 {
		t.Errorf("复装后版本表应仍为 1 且干净: version=%d dirty=%v err=%v", ver, dirty, err)
	}
	if err := assertPlaced(env, name); err != nil {
		t.Errorf("复装后落位面不符: %v", err)
	}

	// ===== --purge 卸载（只能与同一次卸载一起执行）=====
	if err := Uninstall(ctx, env, name, UninstallOptions{Purge: true, Yes: true, Operator: "pdbtest"}); err != nil {
		t.Fatalf("--purge 卸载应成功: %v", err)
	}
	assertZeroResidue(t, env, db, name)
	if exists, err := testTableExistsIn(ctx, db, currentSchemaName(ctx, db), MigrationTableName(name, prefix)); err != nil || exists {
		t.Errorf("--purge 后版本表应消失: exists=%v err=%v", exists, err)
	}
	if exists, err := testTableExistsIn(ctx, db, currentSchemaName(ctx, db), prefix+"plugin_"+name+"_sample"); err != nil || exists {
		t.Errorf("--purge 后插件表应消失: exists=%v err=%v", exists, err)
	}
	// 与安装的 ④b 建表对称：ddl_owner=engine 的表由 --purge drop
	if exists, err := testTableExistsIn(ctx, db, currentSchemaName(ctx, db), entityTable); err != nil || exists {
		t.Errorf("--purge 后 L2 实体表应消失（与 ④b 建表对称）: exists=%v err=%v", exists, err)
	}
}

// TestPluginInstallL2DDLDB 安装链的结构 DDL 面（「装（新实体）」/「升级/复装」/dry-run ⑤ 段）：
//
//	dry-run 逐条列出将执行的 DDL（建表 + 索引）且零副作用 → 实装由 ④b 建表 → 复装以表结构自省为基线
//	（0 条语句）→ 含**拒绝类**变更（varchar 收窄）的源在 dry-run 阶段就被拒、且未写任何库行。
func TestPluginInstallL2DDLDB(t *testing.T) {
	env, db := installDBSetup(t, "7")
	ctx := context.Background()
	name := pluginDBName("7")
	prefix := config.Get().Database.Prefix
	buf, ok := env.Out.(*bytes.Buffer)
	if !ok {
		t.Fatal("测试环境应以 *bytes.Buffer 作为输出目标")
	}

	if err := Scaffold(env, name, ScaffoldOptions{Kind: "declarative", Title: "L2 DDL 用例插件"}); err != nil {
		t.Fatalf("脚手架应成功: %v", err)
	}
	entityTable := prefix + "plugin_" + name + "_items"

	// ===== --dry-run：逐条列出 DDL（⑤ 段），零副作用 =====
	buf.Reset()
	if err := Install(ctx, env, name, InstallOptions{DryRun: true, Operator: "pdbtest"}); err != nil {
		t.Fatalf("--dry-run 应成功: %v", err)
	}
	text := buf.String()
	for _, want := range []string{
		"⑤ 将执行的 DDL",
		`CREATE TABLE IF NOT EXISTS "` + entityTable + `"`,
		`CREATE INDEX IF NOT EXISTS "` + prefix + "idx_plugin_" + name + "_items_weigh" + `"`,
		"COMMENT ON",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("--dry-run 应逐条列出 DDL，缺 %q\n--- 实际输出 ---\n%s", want, text)
		}
	}
	if exists, err := testTableExistsIn(ctx, db, currentSchemaName(ctx, db), entityTable); err != nil || exists {
		t.Errorf("--dry-run 不得建表: exists=%v err=%v", exists, err)
	}
	if _, ok, err := pluginLedger(ctx, db, name); err != nil || ok {
		t.Errorf("--dry-run 不得写台账: ok=%v err=%v", ok, err)
	}

	// ===== 实装：④b 按 spec 建表 + 索引一次到位 =====
	buf.Reset()
	if err := Install(ctx, env, name, InstallOptions{Operator: "pdbtest"}); err != nil {
		t.Fatalf("安装应成功: %v", err)
	}
	text = buf.String()
	if !strings.Contains(text, "④b DDL") || !strings.Contains(text, "✓ [create_table]") {
		t.Errorf("安装输出应逐条回报 ④b 的 DDL 结果\n--- 实际输出 ---\n%s", text)
	}
	if exists, err := testTableExistsIn(ctx, db, currentSchemaName(ctx, db), entityTable); err != nil || !exists {
		t.Fatalf("安装后 L2 实体表应存在: exists=%v err=%v", exists, err)
	}
	// F2：纯声明式插件（无 menus 段）装后**必须有菜单入口**——分组 + 实体页 + 权限节点全部由公式代写
	rules, err := gorm.G[model.AdminRule](db).Where("plugin = ?", name).Order("id").Find(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 7 { // 分组 1 + 实体页 1 + 节点 5（drag_sort ⇒ 含 sort）
		t.Fatalf("声明式插件应代写 7 条菜单/节点（公式），实际 %d: %v", len(rules), keysOfRules(rules))
	}
	for i := range rules {
		if rules[i].Status == nil || *rules[i].Status != 0 {
			t.Errorf("安装期菜单/节点 status 应为 0: %s = %v", rules[i].Name, rules[i].Status)
		}
	}
	page := byRuleName(rules, "plugin/"+name+"/items")
	if page == nil || page.Type != "menu" || page.Component == nil || *page.Component != EntityRenderComponent {
		t.Fatalf("实体页行必须是公式（menu + 宿主通用页），实际 %+v", page)
	}
	if node := byRuleName(rules, "plugin/"+name+"/items/sort"); node == nil || node.Pid == nil || *node.Pid != page.ID {
		t.Errorf("drag_sort 的实体应代写 sort 节点并挂到实体页下（pid=%d），实际 %+v", page.ID, node)
	}

	// ===== 复装：以表结构自省为基线 ⇒ 0 条语句（不重复建表、不报错）=====
	// 卸载前无需 disable：状态是 installed（从未 enable），而「先禁用后卸载」只拦 enabled 态；
	// 复装凭据取**卸载前**打的 .store 包（卸载会删源码目录 ⇒ 目录源在卸载后已不存在，与编排用例同口径）
	pkg, err := Export(env, name, "")
	if err != nil {
		t.Fatalf("打包应成功: %v", err)
	}
	if err := Uninstall(ctx, env, name, UninstallOptions{Operator: "pdbtest"}); err != nil {
		t.Fatalf("卸载应成功: %v", err)
	}
	buf.Reset()
	if err := Install(ctx, env, pkg, InstallOptions{AllowUnsigned: true, Operator: "pdbtest"}); err != nil {
		t.Fatalf("复装应成功（表保留 ⇒ 自省基线判定结构齐备）: %v", err)
	}
	if text = buf.String(); !strings.Contains(text, "④b DDL") {
		t.Errorf("复装应经 ④b（自省基线）: \n--- 实际输出 ---\n%s", text)
	}

	// ===== 拒绝类变更（varchar 收窄）⇒ dry-run 阶段就拒装，零副作用 =====
	// 用**安装源的一份拷贝**承载坏变更（不改编在仓/工作根的已装源码），dry-run 只读 ⇒ 已装状态不受影响
	srcCopy := path.Join("src", name)
	if err := os.CopyFS(env.Abs(srcCopy), os.DirFS(env.Abs(PluginDir(name)))); err != nil {
		t.Fatalf("拷贝安装源失败: %v", err)
	}
	mutateManifestFieldLength(t, env.Abs(srcCopy), 32)
	buf.Reset()
	dryErr := Install(ctx, env, srcCopy, InstallOptions{DryRun: true, Operator: "pdbtest"})
	if dryErr == nil || !strings.Contains(dryErr.Error(), "收窄") {
		t.Errorf("varchar 收窄（拒绝类变更）应在 dry-run 阶段被拒并点名理由，实际 %v", dryErr)
	}
	if text = buf.String(); !strings.Contains(text, "收窄") { // ⑤ 段要列出计划与拒绝理由（不吞错）
		t.Errorf("dry-run 应列出被拒的结构变更理由\n--- 实际输出 ---\n%s", text)
	}
	if count := auditCount(t, db, name, ActionInstall, AuditSuccess); count != 2 {
		t.Errorf("拒装不得新增 install/success 审计（应仍为 2），实际 %d", count)
	}
	assertLedgerStatus(t, ctx, db, name, host.StatusInstalled) // 已装现场未被拒装动作改动
}

// mutateManifestFieldLength 就地改实体字段的 varchar 长度（拒绝类变更的夹具：收窄 ⇒ 引擎拒绝、提示升 mixed/L1）
func mutateManifestFieldLength(t *testing.T, dir string, length int) {
	t.Helper()
	file := filepath.Join(dir, manifest.FileName)
	content, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", file, err)
	}
	// 骨架里 title 的长度声明是固定一行（length: 128）
	replaced := bytes.Replace(content, []byte("        length: 128\n"), []byte(fmt.Sprintf("        length: %d\n", length)), 1)
	if bytes.Equal(replaced, content) {
		t.Fatalf("%s 里没有可替换的 length 声明（骨架形态变了？）", file)
	}
	if err := os.WriteFile(file, replaced, 0o644); err != nil {
		t.Fatalf("写回 %s 失败: %v", file, err)
	}
	if _, _, err := manifest.Load(dir, manifest.Options{Prefix: config.Get().Database.Prefix, HostVersion: version.Get().Version}); err != nil {
		t.Fatalf("改写后的清单未过校验: %v", err)
	}
}

// TestPluginInstallPlaceFailureDB ⑤ 落位失败 ⇒ **补偿回滚完成**（一键撤销到安装前，无 failed 残留）。
// 触发方式：包源（zip）内的 plugin.go 是普通文件，而工作树的同名目标是符号链接 ⇒ 落位侧拒软链。
func TestPluginInstallPlaceFailureDB(t *testing.T) {
	env, db := installDBSetup(t, "2")
	ctx := context.Background()
	name := pluginDBName("2")

	if err := Scaffold(env, name, ScaffoldOptions{Kind: "code"}); err != nil {
		t.Fatalf("脚手架应成功: %v", err)
	}
	pkg, err := Export(env, name, "")
	if err != nil {
		t.Fatalf("打包应成功: %v", err)
	}
	// 工作树里造一个软链落位目标（源码目录整体删掉后重建）
	if err := os.RemoveAll(env.Abs(PluginDir(name))); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(env.Abs(PluginDir(name)), 0o755); err != nil {
		t.Fatal(err)
	}
	linkTarget := env.Abs(path.Join(PluginDir(name), "plugin.go"))
	if err := os.Symlink(filepath.Join(env.Root, "nowhere.go"), linkTarget); err != nil {
		t.Fatalf("建软链失败: %v", err)
	}

	installErr := Install(ctx, env, pkg, InstallOptions{AllowUnsigned: true, Operator: "pdbtest"})
	if installErr == nil {
		t.Fatal("落位目标为符号链接时应失败")
	}
	if !strings.Contains(installErr.Error(), "符号链接") {
		t.Errorf("错误应点名符号链接，实际 %v", installErr)
	}
	// 补偿完成 ⇒ 各面零残留（含台账：不是 failed 残留）；源目录是用例自建的宿主机件，
	// 只应剩那条软链——落位文件必须已回滚
	assertNoInstallResidue(t, env, db, name)
	if _, err := os.Lstat(linkTarget); err != nil {
		t.Errorf("宿主既有的符号链接不是插件物，不应被删: %v", err)
	}
	if count := auditCount(t, db, name, ActionInstall, AuditFailed); count == 0 {
		t.Error("应留下 install/failed 审计（含补偿过程）")
	}
}

// TestPluginInstallMigrateFailureDB ⑥ 迁移失败 ⇒ 版本表 dirty ⇒ **保留现场、状态留 failed**（不静默 force）
// ⇒ `migrate-force` 归位 ⇒ `uninstall --force --purge` 清理干净（反向路径）。
func TestPluginInstallMigrateFailureDB(t *testing.T) {
	env, db := installDBSetup(t, "3")
	ctx := context.Background()
	name := pluginDBName("3")
	prefix := config.Get().Database.Prefix

	if err := Scaffold(env, name, ScaffoldOptions{Kind: "code"}); err != nil {
		t.Fatalf("脚手架应成功: %v", err)
	}
	// 把迁移换成必失败的 SQL（语法错误；down 文件保持合法）
	broken := "CREATE TABLE \"__PREFIX__plugin_" + name + "_broken\" (\n    \"id\" bigint\n"
	mustWrite(t, env.Abs(path.Join(PluginDir(name), "migrations", "1_init.up.sql")), broken)

	if err := Install(ctx, env, name, InstallOptions{Operator: "pdbtest"}); err == nil {
		t.Fatal("迁移失败应报错")
	}

	ledger, ok, err := pluginLedger(ctx, db, name)
	if err != nil || !ok {
		t.Fatalf("失败后应保留台账行（现场保留）: ok=%v err=%v", ok, err)
	}
	if ledger.Status != host.StatusFailed {
		t.Errorf("现场保留时台账状态应为 failed，实际 %s", ledger.Status)
	}
	rules, err := gorm.G[model.AdminRule](db).Where("plugin = ?", name).Count(ctx, "*")
	if err != nil || rules != 3 {
		t.Errorf("现场保留时归属行应在（3 条菜单/节点），实际 %d err=%v", rules, err)
	}
	if _, err := os.Stat(env.Abs(path.Join(PluginDir(name), "plugin.yaml"))); err != nil {
		t.Errorf("现场保留时落位文件应在: %v", err)
	}
	if ver, dirty, err := pluginMigrationState(ctx, db, name); err != nil || !dirty || ver != 1 {
		t.Errorf("版本表应为 dirty（版本 1）: version=%d dirty=%v err=%v", ver, dirty, err)
	}

	// dirty 态拒绝对回滚：uninstall --purge 应报错并给 migrate-force 提示
	purgeErr := Uninstall(ctx, env, name, UninstallOptions{Force: true, Purge: true, Yes: true, Operator: "pdbtest"})
	if purgeErr == nil {
		t.Fatal("dirty 态 --purge 应拒绝执行")
	}
	if !strings.Contains(purgeErr.Error(), "dirty") {
		t.Errorf("错误应点名 dirty，实际 %v", purgeErr)
	}

	// 归位（-1 = 无版本），再按固定顺序清理
	if err := MigrateForce(env, name, -1); err != nil {
		t.Fatalf("migrate-force 应成功: %v", err)
	}
	if err := Uninstall(ctx, env, name, UninstallOptions{Force: true, Purge: true, Yes: true, Operator: "pdbtest"}); err != nil {
		t.Fatalf("--force --purge 清理应成功: %v", err)
	}
	assertZeroResidue(t, env, db, name)
	if exists, err := testTableExistsIn(ctx, db, currentSchemaName(ctx, db), MigrationTableName(name, prefix)); err != nil || exists {
		t.Errorf("清理后版本表应消失: exists=%v err=%v", exists, err)
	}
}

// TestPluginInstallWireFailureDB ⑦ 接线失败 ⇒ **补偿回滚完成**（含「回滚已应用迁移」）。
//
// 锁定的是补偿回滚读 down 文件的**位置**：补偿顺序是「删已落位文件 → 回滚已应用迁移 →
// 删台账/归属行」，第一步之后落位副本 plugins/<名>/migrations 已不复存在 ⇒ down 文件只能从**安装源**
// 读（包源 = staging 解包临时目录；目录源 = 源码目录）。用例用「源码目录在 plugins/ 之外」的形态
// 落位结果全为新建，落位副本被删后源目录是 down 文件的唯一出处（读到已删副本则补偿退化为「保留现场」）。
//
// 触发方式：接线文件所在目录只读（只读检出 / CI 只读挂载）⇒ wirePlugin 的原子写入失败，
// 而读侧仍是「文件不存在」（撤注册行一步不受影响），注入面精确落在 ⑦。
func TestPluginInstallWireFailureDB(t *testing.T) {
	env, db := installDBSetup(t, "4")
	ctx := context.Background()
	name := pluginDBName("4")
	prefix := config.Get().Database.Prefix

	if err := Scaffold(env, name, ScaffoldOptions{Kind: "mixed", Title: "接线失败用例插件"}); err != nil {
		t.Fatalf("脚手架应成功: %v", err)
	}
	// 源码目录移到 plugins/ 之外：落位副本全为新建（与「包源安装」同形）
	sourceDir := path.Join("src", name)
	if err := os.MkdirAll(env.Abs(path.Dir(sourceDir)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(env.Abs(PluginDir(name)), env.Abs(sourceDir)); err != nil {
		t.Fatalf("移动源码目录失败: %v", err)
	}
	// 接线文件所在目录只读：读路径无该文件（= 尚未接线），写路径必失败
	registryDir := env.Abs(path.Dir(RegistryFile))
	if err := os.MkdirAll(registryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(registryDir, 0o555); err != nil {
		t.Fatal(err)
	}
	// 注入自检：目录确为只读。以 root 运行或平台不施加目录权限时 chmod 不构成阻挡 ⇒ 触发不了 ⑦，
	// 如实跳过而不是让用例去猜（探针只在目录仍可写时才会建出文件，清理与否则一并处理）。
	if probe, probeErr := os.CreateTemp(registryDir, ".probe-"); probeErr == nil {
		probeName := probe.Name()
		_ = probe.Close()
		_ = os.Remove(probeName)
		t.Skip("接线目录仍可写（以 root 运行或平台不施加目录权限）：无法机械触发 ⑦ 的写入失败")
	}

	installErr := Install(ctx, env, sourceDir, InstallOptions{Operator: "pdbtest"})
	if installErr == nil {
		t.Fatal("接线文件不可写时应失败")
	}
	if !strings.Contains(installErr.Error(), "接线") {
		t.Errorf("错误应点名接线步骤，实际 %v", installErr)
	}

	// 补偿完成（不是「保留现场」）⇒ 各面零残留；迁移与版本表一并回滚（down 文件取自安装源）
	assertZeroResidue(t, env, db, name)
	schema := currentSchemaName(ctx, db)
	if exists, err := testTableExistsIn(ctx, db, schema, MigrationTableName(name, prefix)); err != nil || exists {
		t.Errorf("补偿后版本表应消失（系本次安装新建）: exists=%v err=%v", exists, err)
	}
	if exists, err := testTableExistsIn(ctx, db, schema, prefix+"plugin_"+name+"_sample"); err != nil || exists {
		t.Errorf("补偿后插件迁移建的表应随 down 回滚: exists=%v err=%v", exists, err)
	}
	if _, err := os.Stat(env.Abs(path.Join(sourceDir, "plugin.yaml"))); err != nil {
		t.Errorf("安装源的源码不是落位物，不应被补偿带走: %v", err)
	}
	// 审计：install/failed，且 detail 记录的补偿是「完成」（failed_state=false）而非「保留现场」
	audit, err := gorm.G[PluginAction](db).
		Where("plugin = ? AND action = ? AND status = ?", name, ActionInstall, AuditFailed).First(ctx)
	if err != nil {
		t.Fatalf("应留下 install/failed 审计: %v", err)
	}
	var detail map[string]any
	if err := json.Unmarshal(audit.Detail, &detail); err != nil {
		t.Fatalf("审计 detail 不是合法 JSON: %v", err)
	}
	if detail["failed_state"] != false {
		t.Errorf("补偿应完成（failed_state=false），实际 detail=%v", detail)
	}
}

// installDBSetup 库级用例的公共前置（PLUGIN_DB 门控 + 一次性工作根 + 自建自清）
func installDBSetup(t *testing.T, suffix string) (*Env, *gorm.DB) {
	t.Helper()
	if os.Getenv("PLUGIN_DB") != "1" {
		t.Skip("需 PLUGIN_DB=1（连真实库；用例会写入并清理 <前缀>plugin_pdbtest* 的行、表与一次性工作根）")
	}
	frameworkMigrationSetup(t) // 连库 + chdir 仓库根（与既有库级用例同款基建）
	db := database.DB()
	if db == nil {
		t.Fatal("数据库句柄不可用")
	}
	requireAppSchema(t, db)
	ensurePluginSchema(t, db)
	requirePluginColumn(t, db)

	name := pluginDBName(suffix)
	prefix := config.Get().Database.Prefix
	// 上轮残留（上次失败留下的）先清掉，保证用例从干净起点开始
	installDBPurgeRows(t, db, name)
	t.Cleanup(func() { installDBPurgeRows(t, db, name) })

	root := t.TempDir()
	for _, lang := range []string{"zh-cn", "en"} {
		mustWrite(t, filepath.Join(root, "web", "src", "lang", lang, ".keep"), "")
	}
	env := &Env{Root: root, Prefix: prefix, HostVersion: version.Get().Version, Out: &bytes.Buffer{}}
	return env, db
}

// installDBPurgeRows 清掉测试插件的全部库侧痕迹（行与插件表；审计行一并清掉——只在测试里这么做）
func installDBPurgeRows(t *testing.T, db *gorm.DB, name string) {
	t.Helper()
	ctx := context.Background()
	prefix := config.Get().Database.Prefix
	deletes := []struct {
		sql string
		arg string
	}{
		{`DELETE FROM "` + prefix + `admin_rules" WHERE "plugin" = ?`, name},
		{`DELETE FROM "` + prefix + `admin_plugin_entities" WHERE "plugin" = ?`, name},
		{`DELETE FROM "` + prefix + `admin_plugin_actions" WHERE "plugin" = ?`, name},
		{`DELETE FROM "` + prefix + `configs" WHERE "group" = ?`, "plugin." + name},
	}
	for _, item := range deletes {
		if err := db.WithContext(ctx).Exec(item.sql, item.arg).Error; err != nil {
			t.Fatalf("清理测试残留失败（%s）: %v", item.sql, err)
		}
	}
	for _, table := range []string{
		MigrationTableName(name, prefix),
		prefix + "plugin_" + name + "_sample",
		prefix + "plugin_" + name + "_broken",
		prefix + "plugin_" + name + "_items", // L2 实体表（安装链 ④b 建表；--purge 才 drop）
	} {
		if !identifierSafe.MatchString(table) {
			t.Fatalf("测试表名 %q 不是安全标识符", table)
		}
		if err := db.WithContext(ctx).Exec(`DROP TABLE IF EXISTS "` + table + `"`).Error; err != nil {
			t.Fatalf("清理测试表 %s 失败: %v", table, err)
		}
	}
}

// requireAppSchema 应用 schema 必须已迁移（否则 skip：不猜、不代做宿主迁移）
func requireAppSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	ctx := context.Background()
	schema := currentSchemaName(ctx, db)
	prefix := config.Get().Database.Prefix
	for _, table := range []string{"admin_rules", "admin_groups", "configs"} {
		exists, err := testTableExistsIn(ctx, db, schema, prefix+table)
		if err != nil {
			t.Fatalf("检查数据表 %s 失败: %v", table, err)
		}
		if !exists {
			t.Skipf("应用 schema 未迁移（缺 %s）：先 `aigo migrate up`", prefix+table)
		}
	}
}

// requirePluginColumn 插件机制的两处增列必须在位（④ 段的 admin_rules.plugin；迁移基线的
// admin_plugins.adopted_through，设计说明）。
// 缺任一 ⇒ skip 并点名迁移（库未推进时不静默失败；验收口径仍要求 PLUGIN_DB 输出无 SKIP）。
func requirePluginColumn(t *testing.T, db *gorm.DB) {
	t.Helper()
	ctx := context.Background()
	prefix := config.Get().Database.Prefix
	exists, err := testColumnExistsIn(ctx, db, currentSchemaName(ctx, db), prefix+"admin_rules", "plugin")
	if err != nil {
		t.Fatalf("检查 admin_rules.plugin 失败: %v", err)
	}
	if !exists {
		t.Skipf("admin_rules.plugin 缺失（插件机制迁移未应用）：先 `aigo migrate up`")
	}
	// 迁移基线列（框架数据库迁移增列）：缺它时 GORM 读写 admin_plugins 会直接报列不存在
	adopted, err := testColumnExistsIn(ctx, db, currentSchemaName(ctx, db), prefix+"admin_plugins", "adopted_through")
	if err != nil {
		t.Fatalf("检查 admin_plugins.adopted_through 失败: %v", err)
	}
	if !adopted {
		t.Skipf("admin_plugins.adopted_through 缺失（迁移基线的框架迁移未应用）：先 `aigo migrate up`")
	}
}

// ensurePluginSchema 插件机制三表：缺失 ⇒ 自带 up 推进到插件迁移版本（只前进不回滚；dirty ⇒ 报错不猜）
func ensurePluginSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	ctx := context.Background()
	schema := currentSchemaName(ctx, db)
	prefix := config.Get().Database.Prefix

	missing := false
	for _, table := range []string{"admin_plugins", "admin_plugin_entities", "admin_plugin_actions"} {
		exists, err := testTableExistsIn(ctx, db, schema, prefix+table)
		if err != nil {
			t.Fatalf("检查数据表 %s 失败: %v", table, err)
		}
		if !exists {
			missing = true
		}
	}
	if !missing {
		return
	}

	root, err := FindRoot()
	if err != nil {
		t.Fatalf("定位仓库根: %v", err)
	}
	target, err := pluginMigrationVersion(root)
	if err != nil {
		t.Fatalf("取插件迁移编号: %v", err)
	}
	rawSrc, err := NewMigrationSource(filepath.Join(root, "cmd", "migrate", "migrations"), prefix)
	if err != nil {
		t.Fatalf("构造迁移源: %v", err)
	}
	src := singleVersionSource{Driver: rawSrc, version: target}
	m, err := migrate.NewWithSourceInstance("iofs", src, MigrateDSN("", ""))
	if err != nil {
		t.Fatalf("连接迁移驱动: %v", err)
	}
	defer func() { _, _ = m.Close() }()

	current, dirty := pluginVersion(t, m)
	if dirty {
		t.Fatalf("框架迁移处于 dirty（版本 %d）：先 `aigo migrate force %d` 修复", current, current)
	}
	if current >= target {
		t.Fatalf("迁移版本已在 %d，但插件机制三表缺失：请人工核对 `aigo migrate up` 的输出", current)
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("推进到插件迁移版本失败: %v", err)
	}
	if exists, err := testTableExistsIn(ctx, db, schema, prefix+"admin_plugins"); err != nil || !exists {
		t.Fatalf("推进后插件表仍缺失: exists=%v err=%v", exists, err)
	}
}

// pluginDBName 测试插件名（带序号与进程号：与遗留数据、并行用例互不干扰）
func pluginDBName(suffix string) string {
	return fmt.Sprintf("%s%s%d", pluginDBTestNamePrefix, suffix, os.Getpid())
}

// pluginMigrationState 插件版本表的版本与 dirty（直读版本表；表不存在 ⇒ error）
func pluginMigrationState(ctx context.Context, db *gorm.DB, name string) (uint, bool, error) {
	table := MigrationTableName(name, config.Get().Database.Prefix)
	if !identifierSafe.MatchString(table) {
		return 0, false, fmt.Errorf("版本表名 %q 不是安全标识符", table)
	}
	var version int64
	var dirty bool
	row := db.WithContext(ctx).Raw(`SELECT version, dirty FROM "` + table + `" LIMIT 1`).Row()
	if err := row.Scan(&version, &dirty); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil // 版本表空 = 无版本（干净）
		}
		return 0, false, err
	}
	return uint(version), dirty, nil
}

// auditCount 统计审计行数
func auditCount(t *testing.T, db *gorm.DB, name, action, status string) int64 {
	t.Helper()
	count, err := gorm.G[PluginAction](db).
		Where("plugin = ? AND action = ? AND status = ?", name, action, status).
		Count(context.Background(), "*")
	if err != nil {
		t.Fatalf("统计审计失败: %v", err)
	}
	return count
}

// assertPlaced 落位面断言：源树（含 web/** 源）、落位副本、语言包都在，且文件头带归属标记
func assertPlaced(env *Env, name string) error {
	content, err := os.ReadFile(env.Abs(path.Join(PluginDir(name), "plugin.go")))
	if err != nil {
		return fmt.Errorf("源码 %s 缺失: %w", PluginDir(name), err)
	}
	if !strings.HasPrefix(string(content), "// plugin: "+name+" v1.0.0\n") {
		return fmt.Errorf("源码应带归属标记: %q", firstLine(string(content)))
	}
	// 源树里的 web/** 必须落地（源是唯一事实源：export 只以源打包，门禁按源-副本比对）
	if _, err := os.Stat(env.Abs(path.Join(PluginDir(name), "web", "views", "index.vue"))); err != nil {
		return fmt.Errorf("源树 %s/web/views/index.vue 缺失: %w", PluginDir(name), err)
	}
	viewContent, err := os.ReadFile(env.Abs(path.Join(ViewTarget(name), "index.vue")))
	if err != nil {
		return fmt.Errorf("落位副本 %s 缺失: %w", ViewTarget(name), err)
	}
	if !strings.HasPrefix(string(viewContent), "<!-- plugin: "+name+" v1.0.0 -->\n") {
		return fmt.Errorf("落位副本应带归属标记: %q", firstLine(string(viewContent)))
	}
	if langTargets := LangTargetsOf(env, name); len(langTargets) != 2 {
		return fmt.Errorf("语言包落位副本应有 2 份（zh-cn/en），实际 %v", langTargets)
	}
	return nil
}

// assertZeroResidue 卸载/补偿后的**零残留**断言（与DB 门控口径逐项对齐）：
// 无台账行 / 无归属行 / 无实体元数据 / 无 configs 分组 / 无孤儿目录 / 无孤儿注册行。
func assertZeroResidue(t *testing.T, env *Env, db *gorm.DB, name string) {
	t.Helper()
	assertZeroResidueExceptSource(t, env, db, name)
	if _, err := os.Stat(env.Abs(PluginDir(name))); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("源码目录应删除: %v", err)
	}
}

// assertNoInstallResidue ⑤ 落位失败补偿后的零残留：行/副本/注册行同 assertZeroResidue 口径，
// 源目录改判「只剩用例自建的软链」（那个目录是宿主机件、不是插件物，补偿只回滚自己落的东西）。
func assertNoInstallResidue(t *testing.T, env *Env, db *gorm.DB, name string) {
	t.Helper()
	assertZeroResidueExceptSource(t, env, db, name)
	entries, err := os.ReadDir(env.Abs(PluginDir(name)))
	if err != nil {
		t.Errorf("源目录应仍在（用例自建，内为软链）: %v", err)
		return
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if len(names) != 1 || names[0] != "plugin.go" {
		t.Errorf("落位文件应已回滚，源目录内应只剩用例自建的软链，实际 %v", names)
	}
}

// assertZeroResidueExceptSource 零残留的公共段（源目录另行判定）
func assertZeroResidueExceptSource(t *testing.T, env *Env, db *gorm.DB, name string) {
	t.Helper()
	ctx := context.Background()

	if row, ok, err := pluginLedger(ctx, db, name); ok || err != nil {
		t.Errorf("台账应无 %s 的行: row=%+v err=%v", name, row, err)
	}
	if count, err := gorm.G[model.AdminRule](db).Where("plugin = ?", name).Count(ctx, "*"); err != nil || count != 0 {
		t.Errorf("归属规则行应清空，实际 %d err=%v", count, err)
	}
	if count, err := gorm.G[host.Entity](db).Where("plugin = ?", name).Count(ctx, "*"); err != nil || count != 0 {
		t.Errorf("实体元数据应清空，实际 %d err=%v", count, err)
	}
	if count, err := gorm.G[model.Config](db).Where(model.Config{Group: "plugin." + name}).Count(ctx, "*"); err != nil || count != 0 {
		t.Errorf("configs 分组应清空，实际 %d err=%v", count, err)
	}
	if _, err := os.Stat(env.Abs(ViewTarget(name))); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("落位副本目录应删除: %v", err)
	}
	if targets := LangTargetsOf(env, name); len(targets) != 0 {
		t.Errorf("语言包落位副本应删除: %v", targets)
	}
	registryRaw, err := os.ReadFile(env.Abs(RegistryFile))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("读取接线文件失败: %v", err)
	}
	registered, err := RegisteredPluginsFrom(registryRaw)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(registered, name) {
		t.Errorf("接线文件不应再有 %s 的注册行（实际 %v）", name, registered)
	}
}

// keysOfRules 规则名清单（错误信息用）
func keysOfRules(rules []model.AdminRule) []string {
	names := make([]string, 0, len(rules))
	for i := range rules {
		names = append(names, rules[i].Name)
	}
	return names
}

// byRuleName 按规则名取行（缺行 ⇒ nil；返回切片内地址以便断言字段）
func byRuleName(rules []model.AdminRule, name string) *model.AdminRule {
	for i := range rules {
		if rules[i].Name == name {
			return &rules[i]
		}
	}
	return nil
}

// firstLine 首行（错误信息用）
func firstLine(content string) string {
	if idx := strings.IndexByte(content, '\n'); idx >= 0 {
		return content[:idx]
	}
	return content
}
