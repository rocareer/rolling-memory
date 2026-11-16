package cli

// 存量库迁移基线（adopt）的库级用例（PLUGIN_DB 门控；无该变量默认 `go test` 不连库）：
//
//	PLUGIN_DB=1 go test -count=1 -timeout=180s ./internal/plugin/cli/
//
// 覆盖（设计说明）：
//   - 存量臂 baseline：探针全命中 ⇒ 只写版本表到 through、**不执行任何旧 DDL**（插件链独有的对象不出现）、
//     台账 adopted_through 回写、审计 baseline.mode=adopt；
//   - 部分命中 ⇒ 拒装且零副作用（无台账行 / 无版本表 / 无落位文件 / 无安装审计）；
//   - --purge 对 adopt 装入的插件默认拒绝（rejected 留痕），三重显式（--purge --yes --purge-adopted）才放行。
//
// 现场构造：用「前缀 + plugin_<名>_legacy」造一张**模拟核心链**的表（探针对象），插件链自己的
// 2_chain 迁移另建一张只有插件链才会建的表——后者是「旧 DDL 是否被执行」的判据。
import (
	"context"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"

	"gorm.io/gorm"
)

// TestPluginInstallBaselineAdoptDB 存量臂：adopt 全命中 ⇒ baseline（0 条迁移 SQL、版本表写 1 行）
func TestPluginInstallBaselineAdoptDB(t *testing.T) {
	env, db := installDBSetup(t, "18")
	ctx := context.Background()
	name := pluginDBName("18")
	prefix := config.Get().Database.Prefix
	legacy := prefix + "plugin_" + name + "_legacy"
	chainOnly := prefix + "plugin_" + name + "_chain"
	prepareAdoptPluginDB(t, env, db, name, legacy, chainOnly)
	dropTestTables(t, db, legacy, chainOnly)

	// 存量现场：模拟核心链已建好的表（探针对象）——插件尚未安装
	if err := db.WithContext(ctx).Exec(`CREATE TABLE "` + legacy + `" (
		"id" bigserial PRIMARY KEY,
		"note" varchar(32) NOT NULL DEFAULT '',
		"quota" integer NOT NULL DEFAULT 0
	)`).Error; err != nil {
		t.Fatalf("构造存量表失败: %v", err)
	}

	if err := Install(ctx, env, name, InstallOptions{Operator: "pdbtest"}); err != nil {
		t.Fatalf("存量臂安装应成功（baseline）：%v", err)
	}

	// ① 台账：adopted_through 回写（--purge 门禁读它）
	ledger, ok, err := pluginLedger(ctx, db, name)
	if err != nil || !ok {
		t.Fatalf("台账应有一行: ok=%v err=%v", ok, err)
	}
	if ledger.AdoptedThrough != 2 {
		t.Errorf("台账 adopted_through 应为 2，实际 %d", ledger.AdoptedThrough)
	}
	if ledger.Status != host.StatusInstalled {
		t.Errorf("安装后台账应为 installed，实际 %s", ledger.Status)
	}

	// ② 版本表：写到 through（1..2 记为「与核心链同物」）
	state, err := readMigrationState(ctx, db, MigrationTableName(name, prefix))
	if err != nil {
		t.Fatalf("读版本表失败: %v", err)
	}
	if !state.Existed || state.Version != 2 || state.Dirty {
		t.Errorf("版本表应存在且停在 2/干净，实际 %+v", state)
	}

	// ③ 旧 DDL 一条都没执行：插件链独有的表（2_chain 建）不许出现
	var exists bool
	if err := db.WithContext(ctx).Raw(`SELECT to_regclass(?)::text IS NOT NULL`, chainOnly).Row().Scan(&exists); err != nil {
		t.Fatalf("检查插件链独有表失败: %v", err)
	}
	if exists {
		t.Errorf("baseline 不得执行任何旧 DDL：插件链独有的表 %s 不应存在", chainOnly)
	}

	// ④ 审计：baseline.mode=adopt / 版本表系本次新建 / migrations 为空
	detail := lastAuditDetail(t, db, name, ActionInstall, AuditSuccess)
	baseline, ok := detail["baseline"].(map[string]any)
	if !ok {
		t.Fatalf("安装审计应有 baseline 段，实际 %+v", detail)
	}
	if baseline["mode"] != "adopt" || baseline["through"] != float64(2) || baseline["version_table_created"] != true {
		t.Errorf("baseline 段不符: %+v", baseline)
	}
	if applied, ok := detail["migrations"].([]any); !ok || len(applied) != 0 {
		t.Errorf("baseline 的 migrations 应为空数组，实际 %v", detail["migrations"])
	}

	// 收尾：停用后 --keep-files 卸载（表与版本表保留；用例自清另由 installDBSetup/本文件的 clean 兜底）。
	// 状态机要求 enabled → disabled：安装后是 installed，先 enable 再 disable。
	if err := Enable(ctx, env, name, "pdbtest"); err != nil {
		t.Fatalf("启用应成功: %v", err)
	}
	if err := Disable(ctx, env, name, "pdbtest"); err != nil {
		t.Fatalf("停用应成功: %v", err)
	}
	if err := Uninstall(ctx, env, name, UninstallOptions{KeepFiles: true, Operator: "pdbtest"}); err != nil {
		t.Fatalf("--keep-files 卸载应成功: %v", err)
	}
}

// TestPluginInstallBaselineRejectDB 部分命中 ⇒ 拒装且零副作用（先于任何落盘/写库）
func TestPluginInstallBaselineRejectDB(t *testing.T) {
	env, db := installDBSetup(t, "19")
	ctx := context.Background()
	name := pluginDBName("19")
	prefix := config.Get().Database.Prefix
	legacy := prefix + "plugin_" + name + "_legacy"
	chainOnly := prefix + "plugin_" + name + "_chain"
	prepareAdoptPluginDB(t, env, db, name, legacy, chainOnly)
	dropTestTables(t, db, legacy, chainOnly)

	// 只建表、**不建全部探针列**（quota 缺）⇒ 表探针命中、列探针缺失 = 部分命中
	if err := db.WithContext(ctx).Exec(`CREATE TABLE "` + legacy + `" (
		"id" bigserial PRIMARY KEY,
		"note" varchar(32) NOT NULL DEFAULT ''
	)`).Error; err != nil {
		t.Fatalf("构造存量表失败: %v", err)
	}

	err := Install(ctx, env, name, InstallOptions{Operator: "pdbtest"})
	if err == nil {
		t.Fatal("部分命中必须拒装（fail-closed）")
	}
	if !strings.Contains(err.Error(), "迁移基线") || !strings.Contains(err.Error(), "拒装") {
		t.Errorf("拒装理由应点名迁移基线与拒装，实际: %v", err)
	}

	// 零副作用：无台账行、无版本表、无落位文件、无安装审计
	if _, ok, ledgerErr := pluginLedger(ctx, db, name); ok || ledgerErr != nil {
		t.Errorf("拒装不应写台账行: ok=%v err=%v", ok, ledgerErr)
	}
	if state, stateErr := readMigrationState(ctx, db, MigrationTableName(name, prefix)); stateErr != nil || state.Existed {
		t.Errorf("拒装不应建版本表: state=%+v err=%v", state, stateErr)
	}
	if _, statErr := os.Stat(env.Abs(path.Join(ViewTarget(name), "index.vue"))); statErr == nil {
		t.Error("拒装不应落位前端副本（web/src/views/admin/plugin/<名>/）")
	}
	if count, countErr := gorm.G[host.Plugin](db).Where("name = ?", name).Count(ctx, "*"); countErr != nil || count != 0 {
		t.Errorf("拒装不应留台账行，实际 %d err=%v", count, countErr)
	}
}

// TestPluginUninstallPurgeAdoptedGateDB --purge 对 adopt 装入的插件默认拒绝；三重显式才放行
func TestPluginUninstallPurgeAdoptedGateDB(t *testing.T) {
	env, db := installDBSetup(t, "20")
	ctx := context.Background()
	name := pluginDBName("20")
	prefix := config.Get().Database.Prefix
	legacy := prefix + "plugin_" + name + "_legacy"
	chainOnly := prefix + "plugin_" + name + "_chain"
	prepareAdoptPluginDB(t, env, db, name, legacy, chainOnly)
	dropTestTables(t, db, legacy, chainOnly)
	if err := db.WithContext(ctx).Exec(`CREATE TABLE "` + legacy + `" (
		"id" bigserial PRIMARY KEY,
		"note" varchar(32) NOT NULL DEFAULT '',
		"quota" integer NOT NULL DEFAULT 0
	)`).Error; err != nil {
		t.Fatalf("构造存量表失败: %v", err)
	}
	if err := Install(ctx, env, name, InstallOptions{Operator: "pdbtest"}); err != nil {
		t.Fatalf("存量臂安装应成功（baseline）：%v", err)
	}
	// 状态机要求 enabled → disabled：安装后是 installed，先 enable 再 disable。
	if err := Enable(ctx, env, name, "pdbtest"); err != nil {
		t.Fatalf("启用应成功: %v", err)
	}
	if err := Disable(ctx, env, name, "pdbtest"); err != nil {
		t.Fatalf("停用应成功: %v", err)
	}

	// ① 只给 --purge --yes ⇒ 必须拒绝（且留 rejected 审计）
	err := Uninstall(ctx, env, name, UninstallOptions{Purge: true, Yes: true, Operator: "pdbtest"})
	if err == nil || !strings.Contains(err.Error(), "--purge-adopted") {
		t.Fatalf("adopt 装入的插件 --purge 应被默认拒绝并点名 --purge-adopted，实际 %v", err)
	}
	if count := auditCount(t, db, name, ActionUninstall, AuditRejected); count != 1 {
		t.Errorf("被拒的 --purge 应留一条 rejected 审计，实际 %d", count)
	}
	// 试点现场未被破坏：表与版本表都在
	if state, stateErr := readMigrationState(ctx, db, MigrationTableName(name, prefix)); stateErr != nil || !state.Existed || state.Version != 2 {
		t.Errorf("被拒后版本表应原样保留在 2，实际 %+v err=%v", state, stateErr)
	}
	var exists bool
	if err := db.WithContext(ctx).Raw(`SELECT to_regclass(?)::text IS NOT NULL`, legacy).Row().Scan(&exists); err != nil || !exists {
		t.Errorf("被拒后存量表应原样保留: exists=%v err=%v", exists, err)
	}

	// ② --dry-run 预演必须打印 a0 段（将回滚对象面 + 恢复路径），且不写审计、不动现场
	if err := Uninstall(ctx, env, name, UninstallOptions{DryRun: true, Purge: true, Yes: true, PurgeAdopted: true, Operator: "pdbtest"}); err != nil {
		t.Fatalf("--purge 预演应成功（只打印）: %v", err)
	}
	if state, _ := readMigrationState(ctx, db, MigrationTableName(name, prefix)); !state.Existed {
		t.Error("预演不应动版本表")
	}

	// ③ 三重显式 ⇒ 放行（purge 会按 down 逆序回滚：这里 down 是删插件链自己的对象；版本表随 --purge drop）
	if err := Uninstall(ctx, env, name, UninstallOptions{Purge: true, Yes: true, PurgeAdopted: true, Operator: "pdbtest"}); err != nil {
		t.Fatalf("三重显式的 --purge 应放行: %v", err)
	}
	if state, stateErr := readMigrationState(ctx, db, MigrationTableName(name, prefix)); stateErr != nil || state.Existed {
		t.Errorf("--purge 后版本表应被 drop: %+v err=%v", state, stateErr)
	}
}

// prepareAdoptPluginDB 造一个声明 adopt 的插件源（骨架 + 两版迁移 + adopt 段），并注册用例清理。
// legacy = 模拟核心链建的表（探针对象，prefix + 逻辑名）；chainOnly = 只有插件链才会建的表（旧 DDL 判据）。
func prepareAdoptPluginDB(t *testing.T, env *Env, db *gorm.DB, name, legacy, chainOnly string) {
	t.Helper()
	prefix := config.Get().Database.Prefix
	logicalLegacy := strings.TrimPrefix(legacy, prefix) // manifest 里的探针对象名**不带前缀**
	logicalChain := strings.TrimPrefix(chainOnly, prefix)
	if err := Scaffold(env, name, ScaffoldOptions{Kind: "code", Title: "迁移基线用例插件"}); err != nil {
		t.Fatalf("脚手架应成功: %v", err)
	}

	migrationsDir := env.Abs(path.Join(PluginDir(name), "migrations"))
	if err := os.RemoveAll(migrationsDir); err != nil {
		t.Fatalf("清空骨架迁移目录失败: %v", err)
	}
	files := map[string]string{
		"1_legacy.up.sql": "CREATE TABLE IF NOT EXISTS \"__PREFIX__" + logicalLegacy + "\" (\n" +
			"    \"id\" bigserial PRIMARY KEY,\n" +
			"    \"note\" varchar(32) NOT NULL DEFAULT '',\n" +
			"    \"quota\" integer NOT NULL DEFAULT 0\n" +
			");\n",
		"1_legacy.down.sql": "DROP TABLE IF EXISTS \"__PREFIX__" + logicalLegacy + "\";\n",
		"2_chain.up.sql": "ALTER TABLE \"__PREFIX__" + logicalLegacy + "\" ADD COLUMN IF NOT EXISTS \"quota\" integer NOT NULL DEFAULT 0;\n" +
			"CREATE TABLE IF NOT EXISTS \"__PREFIX__" + logicalChain + "\" (\"id\" bigserial PRIMARY KEY);\n",
		"2_chain.down.sql": "DROP TABLE IF EXISTS \"__PREFIX__" + logicalChain + "\";\n",
	}
	for file, content := range files {
		mustWrite(t, filepath.Join(migrationsDir, file), content)
	}

	manifestPath := env.Abs(path.Join(PluginDir(name), "plugin.yaml"))
	content, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	adopt := "\nadopt:\n  through: 2\n  probes:\n" +
		"    - version: 1\n      kind: table\n      name: " + logicalLegacy + "\n" +
		"    - version: 2\n      kind: column\n      table: " + logicalLegacy + "\n      name: quota\n"
	if err := os.WriteFile(manifestPath, append(content, []byte(adopt)...), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { dropTestTables(t, db, legacy, chainOnly) })
}

// dropTestTables 用例自清：drop 本文件的临时表（含失败残留）
func dropTestTables(t *testing.T, db *gorm.DB, tables ...string) {
	t.Helper()
	ctx := context.Background()
	for _, table := range tables {
		if !identifierSafe.MatchString(table) {
			t.Fatalf("测试表名 %q 不是安全标识符", table)
		}
		if err := db.WithContext(ctx).Exec(`DROP TABLE IF EXISTS "` + table + `"`).Error; err != nil {
			t.Fatalf("清理测试表 %s 失败: %v", table, err)
		}
	}
}

// lastAuditDetail 最近一条 (action,status) 审计的 detail（JSON 解出；安装审计的 baseline 段断言用它）
func lastAuditDetail(t *testing.T, db *gorm.DB, name, action, status string) map[string]any {
	t.Helper()
	rows, err := gorm.G[PluginAction](db).
		Where("plugin = ? AND action = ? AND status = ?", name, action, status).
		Order("id").Find(context.Background())
	if err != nil {
		t.Fatalf("读取审计失败: %v", err)
	}
	if len(rows) == 0 {
		t.Fatalf("没有 (action=%s,status=%s) 的审计行", action, status)
	}
	latest := rows[len(rows)-1]
	var detail map[string]any
	if err := json.Unmarshal(latest.Detail, &detail); err != nil {
		t.Fatalf("解析审计 detail 失败: %v（原始 %s）", err, latest.Detail)
	}
	return detail
}
