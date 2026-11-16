package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"strings"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"
	"github.com/ai-go-hub/ai-go-admin/internal/model"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"

	"gorm.io/gorm"
)

// promote（L2 → L1 升级）的库级用例（PLUGIN_DB 门控；/C8）：
//
//	PLUGIN_DB=1 go test -count=1 -timeout=180s ./internal/plugin/cli/
//
// 口径：预演（缺 --yes）零副作用；--yes 执行删「实体元数据 +公式面菜单/节点」并写审计；
// 表与数据保留（此后归插件迁移自管）；按**精确规则名**定位（前缀词/自有行必须保留）。
func TestPluginPromoteDB(t *testing.T) {
	env, db := installDBSetup(t, "12")
	ctx := context.Background()
	name := pluginDBName("12")
	prefix := config.Get().Database.Prefix
	buf, ok := env.Out.(*bytes.Buffer)
	if !ok {
		t.Fatal("测试环境应以 *bytes.Buffer 作为输出目标")
	}

	if err := Scaffold(env, name, ScaffoldOptions{Kind: "mixed", Title: "promote 用例插件"}); err != nil {
		t.Fatalf("脚手架应成功: %v", err)
	}
	if err := Install(ctx, env, name, InstallOptions{Operator: "pdbtest"}); err != nil {
		t.Fatalf("安装应成功: %v", err)
	}

	rules, err := gorm.G[model.AdminRule](db).Where("plugin = ?", name).Order("id").Find(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 9 {
		t.Fatalf("基线应有 9 条归属行（声明 3 +公式 6），实际 %d: %v", len(rules), keysOfRules(rules))
	}
	page := byRuleName(rules, "plugin/"+name+"/items")
	if page == nil {
		t.Fatalf("缺实体页行 plugin/%s/items（实际 %v）", name, keysOfRules(rules))
	}

	// 干扰项（回归「精确名定位」）：同前缀的衍生实体行与作者自有页行必须**保留**——
	// 早先用 `name LIKE '页/%'` 的前缀匹配会误删它们（且 `_` 在 LIKE 里是通配符）。
	probes := []model.AdminRule{
		{Type: "node", Title: "前缀词实体行", Name: "plugin/" + name + "/itemsb/read"},
		{Type: "menu", Title: "作者自有页行", Name: "plugin/" + name + "/items-detail", Pid: &page.ID},
	}
	probeNames := make([]string, 0, len(probes))
	for i := range probes {
		if err := gorm.G[model.AdminRule](db).Create(ctx, &probes[i]); err != nil {
			t.Fatalf("造干扰行 %s 失败: %v", probes[i].Name, err)
		}
		// 归属列按列名直写（与安装器同口径）：不依赖 model 的归属字段
		if _, err := gorm.G[model.AdminRule](db).Where("id = ?", probes[i].ID).Update(ctx, "plugin", name); err != nil {
			t.Fatalf("写干扰行 %s 归属失败: %v", probes[i].Name, err)
		}
		probeNames = append(probeNames, probes[i].Name)
	}

	// 角色组悬挂 ID（promote 的清理面：被删行的 ID 要从各角色组 rules CSV 写回清除）
	readNode := byRuleName(rules, "plugin/"+name+"/items/read")
	if readNode == nil {
		t.Fatalf("缺公式节点 plugin/%s/items/read（实际 %v）", name, keysOfRules(rules))
	}
	dangling := fmt.Sprintf("%d,%d,777001", page.ID, readNode.ID)
	group := model.AdminGroup{Name: name + "-promote-group", Rules: &dangling, Status: uint8Ptr(1)}
	if err := gorm.G[model.AdminGroup](db).Create(ctx, &group); err != nil {
		t.Fatalf("建测试角色组失败: %v", err)
	}
	t.Cleanup(func() {
		db.WithContext(context.Background()).Exec(`DELETE FROM "`+prefix+`admin_groups" WHERE "id" = ?`, group.ID)
	})

	// ===== ① 预演（缺 --yes）：打印将删清单 + 零副作用 =====
	buf.Reset()
	if err := Promote(ctx, env, name, PromoteOptions{Entity: "items"}); err != nil {
		t.Fatalf("promote 预演应成功: %v", err)
	}
	preview := buf.String()
	for _, want := range []string{
		"L2 → L1 升级（promote）",
		"前置（①，必须先做）",
		"② 将删除：admin_plugin_entities 的元数据行",
		"将删除的通用页菜单/节点 6 行",
		"保留 5 条归属行（不在公式面内：作者自有的菜单/节点）", // 声明 3 + 干扰 2
		probeNames[0], probeNames[1],
		"已落位 manifest 仍声明实体 items",
		"⑥ 复装保护",
		"未加 --yes：以上为预演",
	} {
		if !strings.Contains(preview, want) {
			t.Errorf("promote 预演应含 %q\n--- 实际输出 ---\n%s", want, preview)
		}
	}
	assertPromoteUntouched(t, ctx, db, name, 11, 1) // 11 = 9 基线 + 2 干扰行；1 个实体元数据行
	if count := auditCount(t, db, name, ActionUpgrade, AuditSuccess); count != 0 {
		t.Errorf("预演不得写 upgrade/success 审计，实际 %d", count)
	}
	if count := auditCount(t, db, name, ActionUpgrade, AuditRejected); count != 0 {
		t.Errorf("预演是只读动作，不得写 rejected 审计，实际 %d", count)
	}
	updated, err := gorm.G[model.AdminGroup](db).Select("rules").Where("id = ?", group.ID).First(ctx)
	if err != nil || updated.Rules == nil || *updated.Rules != dangling {
		t.Errorf("预演不得改角色组 rules（应仍为 %q），实际 %v err=%v", dangling, updated.Rules, err)
	}

	// ===== ② --yes 执行：删公式面 + 元数据 + 悬挂 ID 写回 + 审计 =====
	buf.Reset()
	if err := Promote(ctx, env, name, PromoteOptions{Entity: "items", Yes: true, Operator: "pdbtest"}); err != nil {
		t.Fatalf("promote 执行应成功: %v", err)
	}
	done := buf.String()
	if !strings.Contains(done, "✓ promote 完成") || !strings.Contains(done, "表 "+prefix+"plugin_"+name+"_items") {
		t.Errorf("promote 完成输出应含读数与表归口说明\n--- 实际输出 ---\n%s", done)
	}

	if count, err := gorm.G[host.Entity](db).Where("plugin = ?", name).Count(ctx, "*"); err != nil || count != 0 {
		t.Errorf("实体元数据行应删除，实际 %d err=%v", count, err)
	}
	after, err := gorm.G[model.AdminRule](db).Where("plugin = ?", name).Order("id").Find(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 5 { // 声明 3 + 干扰 2
		t.Fatalf("promote 后应剩 5 条（L1 声明 3 + 干扰 2），实际 %d: %v", len(after), keysOfRules(after))
	}
	for _, formulaName := range entityPageRuleNames(name, "items") {
		if row := byRuleName(after, formulaName); row != nil {
			t.Errorf("公式面行应删除: %s", formulaName)
		}
	}
	for _, probe := range probeNames {
		if byRuleName(after, probe) == nil {
			t.Errorf("公式面外的归属行应保留: %s（实际 %v）", probe, keysOfRules(after))
		}
	}
	updated, err = gorm.G[model.AdminGroup](db).Select("rules").Where("id = ?", group.ID).First(ctx)
	if err != nil || updated.Rules == nil || *updated.Rules != "777001" {
		t.Errorf("被删行的悬挂 ID 应写回清除（保留 777001），实际 %v err=%v", updated.Rules, err)
	}
	audit, err := gorm.G[PluginAction](db).
		Where("plugin = ? AND action = ? AND status = ?", name, ActionUpgrade, AuditSuccess).First(ctx)
	if err != nil {
		t.Fatalf("应有 1 条 upgrade/success 审计: %v", err)
	}
	var detail map[string]any
	if err := json.Unmarshal(audit.Detail, &detail); err != nil {
		t.Fatalf("审计 detail 不是合法 JSON: %v", err)
	}
	if detail["entity"] != "items" || detail["rules_deleted"] != float64(6) || detail["dangling_ids"] != float64(2) {
		t.Errorf("审计读数不符（entity=items / rules_deleted=6 / dangling_ids=2），实际 %v", detail)
	}
	if detail["promote"] != true {
		t.Errorf("审计必须落机器可读的 promote 结论（安装期所有权检查读它），实际 %v", detail["promote"])
	}
	// 表与数据一律不动（此后归插件迁移自管：引擎不再 ALTER/DROP）
	if exists, err := testTableExistsIn(ctx, db, currentSchemaName(ctx, db), prefix+"plugin_"+name+"_items"); err != nil || !exists {
		t.Errorf("promote 不得删 L2 实体表: exists=%v err=%v", exists, err)
	}

	// ===== ③ 重复 promote：实体元数据已不在 ⇒ 拒绝；--yes 路径写 rejected 留痕 =====
	repeatErr := Promote(ctx, env, name, PromoteOptions{Entity: "items", Yes: true, Operator: "pdbtest"})
	if repeatErr == nil || !strings.Contains(repeatErr.Error(), "没有名为") {
		t.Errorf("实体已退出 L2 时应拒绝并点名，实际 %v", repeatErr)
	}
	if count := auditCount(t, db, name, ActionUpgrade, AuditSuccess); count != 1 {
		t.Errorf("重复 promote 不得新增 success 审计，实际 %d", count)
	}
	if count := auditCount(t, db, name, ActionUpgrade, AuditRejected); count != 1 {
		t.Errorf("--yes 路径的拒绝应写 rejected 审计，实际 %d", count)
	}
	// 无 --yes 再预演：同样是「没有名为」的拒绝，但不写审计（只读预演）
	if previewErr := Promote(ctx, env, name, PromoteOptions{Entity: "items"}); previewErr == nil {
		t.Error("实体已退出 L2 时预演也应报「没有名为」")
	}
	if count := auditCount(t, db, name, ActionUpgrade, AuditRejected); count != 1 {
		t.Errorf("预演拒绝不得写 rejected 审计，实际 %d", count)
	}

	// ===== ④ 台账无行 ⇒ 报未安装（无 promote 对象）=====
	if err := Promote(ctx, env, name+"x", PromoteOptions{Entity: "items"}); err == nil || !strings.Contains(err.Error(), "台账无插件") {
		t.Errorf("台账无该插件时应报未安装，实际 %v", err)
	}

	// ===== ⑤ 修复路径可用：源码副本改声明 ddl_owner: migrations ⇒ dry-run 不再拒（表归插件迁移自管）=====
	srcCopy := path.Join("src", name)
	if err := os.CopyFS(env.Abs(srcCopy), os.DirFS(env.Abs(PluginDir(name)))); err != nil {
		t.Fatalf("拷贝安装源失败: %v", err)
	}
	mutateManifestDDLOwner(t, env.Abs(srcCopy))
	buf.Reset()
	if err := Install(ctx, env, srcCopy, InstallOptions{DryRun: true, Force: true, Operator: "pdbtest"}); err != nil {
		t.Fatalf("改声明 ddl_owner: migrations 后 dry-run 应不再拒（--force 只因副本 manifest 与已落位件同名不同内容）: %v", err)
	}
	if out := buf.String(); !strings.Contains(out, "ddl_owner=migrations") {
		t.Errorf("预演应给出 migrations 自管的说明\n--- 实际输出 ---\n%s", out)
	}
	assertPromoteUntouched(t, ctx, db, name, 5, 0) // dry-run 零副作用

	// 复装凭据（卸载会删源码目录；⑦ 的复装拒装用它打出的包）
	pkg, err := Export(env, name, "")
	if err != nil {
		t.Fatalf("打包应成功: %v", err)
	}

	// ===== ⑥ --purge 卸载：元数据已退出 ⇒ 引擎侧不再 drop 该表（等价 ddl_owner=migrations）=====
	// 源码与迁移目录仍在（从未做普通卸载）⇒ purgePlugin 可回滚插件迁移并 drop 版本表。
	if err := Uninstall(ctx, env, name, UninstallOptions{Purge: true, Yes: true, Operator: "pdbtest"}); err != nil {
		t.Fatalf("--purge 卸载应成功: %v", err)
	}
	assertZeroResidue(t, env, db, name)
	assertNoOwnershipDrift(t, db)
	if exists, err := testTableExistsIn(ctx, db, currentSchemaName(ctx, db), prefix+"plugin_"+name+"_items"); err != nil || !exists {
		t.Errorf("promote 后该表归插件迁移自管：--purge（引擎侧）不得 drop 它: exists=%v err=%v", exists, err)
	}
	if exists, err := testTableExistsIn(ctx, db, currentSchemaName(ctx, db), MigrationTableName(name, prefix)); err != nil || exists {
		t.Errorf("--purge 应回滚插件迁移并 drop 版本表: exists=%v err=%v", exists, err)
	}

	// ===== ⑦ 复装保护（F23 边界）：卸载后复装（manifest 仍声明 ddl_owner=engine 缺省）⇒ 安装期所有权检查拒装 =====
	// 表仍在 + promote 记录在案（审计 append-only）⇒ 「把 L2 引擎的 ALTER 与 --purge DROP 权重新挂回该表」
	// 不可能再发生（明令禁止同一张表双重治理）。
	buf.Reset()
	reinstallErr := Install(ctx, env, pkg, InstallOptions{AllowUnsigned: true, Operator: "pdbtest"})
	if reinstallErr == nil {
		t.Fatal("promote 过的实体复装（仍声明 ddl_owner=engine）应被安装期所有权检查拒装")
	}
	if !strings.Contains(reinstallErr.Error(), "promote") || !strings.Contains(reinstallErr.Error(), "ddl_owner: migrations") {
		t.Errorf("拒装错误应点名 promote 结论与修复路径，实际 %v", reinstallErr)
	}
	if out := buf.String(); !strings.Contains(out, "✗") || !strings.Contains(out, "已经 promote 移交") {
		t.Errorf("安装预演应逐条列出拒装理由\n--- 实际输出 ---\n%s", out)
	}
	// 零副作用：元数据不得被写回（0）、无归属行（0）、无台账行、无新 install success 审计、该表不动
	assertPromoteUntouched(t, ctx, db, name, 0, 0)
	if row, ok, err := pluginLedger(ctx, db, name); ok || err != nil {
		t.Errorf("拒装不得写台账: row=%+v err=%v", row, err)
	}
	if count := auditCount(t, db, name, ActionInstall, AuditSuccess); count != 1 {
		t.Errorf("拒装不得新增 install/success 审计（应仍 1，仅最初那次安装），实际 %d", count)
	}
	if exists, err := testTableExistsIn(ctx, db, currentSchemaName(ctx, db), prefix+"plugin_"+name+"_items"); err != nil || !exists {
		t.Errorf("拒装不得动该表（应仍存在）: exists=%v err=%v", exists, err)
	}
}

// assertPromoteUntouched 预演的零副作用断言：归属行数、实体元数据行数与预演前一致
func assertPromoteUntouched(t *testing.T, ctx context.Context, db *gorm.DB, name string, wantRules, wantEntities int64) {
	t.Helper()
	if count, err := gorm.G[model.AdminRule](db).Where("plugin = ?", name).Count(ctx, "*"); err != nil || count != wantRules {
		t.Errorf("预演不得动归属行（应 %d 条），实际 %d err=%v", wantRules, count, err)
	}
	if count, err := gorm.G[host.Entity](db).Where("plugin = ?", name).Count(ctx, "*"); err != nil || count != wantEntities {
		t.Errorf("预演不得动实体元数据（应 %d 行），实际 %d err=%v", wantEntities, count, err)
	}
}

// mutateManifestDDLOwner 把副本 manifest 的实体改声明为 ddl_owner: migrations（promote 修复路径的夹具：
// 骨架的实体块有固定的 `    drag_sort: true` 行，紧随其后插入归属键）
func mutateManifestDDLOwner(t *testing.T, dir string) {
	t.Helper()
	file := path.Join(dir, manifest.FileName)
	content, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", file, err)
	}
	replaced := bytes.Replace(content, []byte("    drag_sort: true\n"),
		[]byte("    drag_sort: true\n    ddl_owner: migrations\n"), 1)
	if bytes.Equal(replaced, content) {
		t.Fatalf("%s 里没有可替换的 drag_sort 声明（骨架形态变了？）", file)
	}
	if err := os.WriteFile(file, replaced, 0o644); err != nil {
		t.Fatalf("写回 %s 失败: %v", file, err)
	}
}

// TestPluginReinstallTakeoverWarningDB 既有表 + 无 promote 记录 ⇒ **不拒装**（明定「复装/升级以
// 表结构自省为基线」）但逐条告警：引擎正在重新接管该表的 ALTER/--purge DROP 权，若它已 promote 给
// L1/迁移自管必须改声明（防「静默重新挂权」的安装期告警面；F23 边界的另一半）。
func TestPluginReinstallTakeoverWarningDB(t *testing.T) {
	env, db := installDBSetup(t, "16")
	ctx := context.Background()
	name := pluginDBName("16")
	prefix := config.Get().Database.Prefix
	buf, ok := env.Out.(*bytes.Buffer)
	if !ok {
		t.Fatal("测试环境应以 *bytes.Buffer 作为输出目标")
	}

	if err := Scaffold(env, name, ScaffoldOptions{Kind: "mixed", Title: "复装告警用例插件"}); err != nil {
		t.Fatalf("脚手架应成功: %v", err)
	}
	if err := Install(ctx, env, name, InstallOptions{Operator: "pdbtest"}); err != nil {
		t.Fatalf("安装应成功: %v", err)
	}
	pkg, err := Export(env, name, "")
	if err != nil {
		t.Fatalf("打包应成功: %v", err)
	}
	if err := Uninstall(ctx, env, name, UninstallOptions{Operator: "pdbtest"}); err != nil {
		t.Fatalf("卸载应成功（保留表与版本表）: %v", err)
	}

	// 复装：表已存在（自省基线 ⇒ 0 条语句）且无 promote 记录 ⇒ 必须成功，但逐条给出接管告警
	buf.Reset()
	if err := Install(ctx, env, pkg, InstallOptions{AllowUnsigned: true, Operator: "pdbtest"}); err != nil {
		t.Fatalf("既有表复装应成功（复装/升级以表结构自省为基线）: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"既有表 " + prefix + "plugin_" + name + "_items",
		"ddl_owner: migrations",
		"promote",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("复装应逐条给出既有表接管告警（缺 %q）\n--- 实际输出 ---\n%s", want, out)
		}
	}
	if count, err := gorm.G[host.Entity](db).Where("plugin = ?", name).Count(ctx, "*"); err != nil || count != 1 {
		t.Errorf("复装应把实体元数据写回 1 行，实际 %d err=%v", count, err)
	}

	// 收尾：--purge（未 promote，引擎自管 ⇒ 实体表随元数据行一并 drop）
	if err := Uninstall(ctx, env, name, UninstallOptions{Purge: true, Yes: true, Operator: "pdbtest"}); err != nil {
		t.Fatalf("--purge 卸载应成功: %v", err)
	}
	assertZeroResidue(t, env, db, name)
	assertNoOwnershipDrift(t, db)
	if exists, err := testTableExistsIn(ctx, db, currentSchemaName(ctx, db), prefix+"plugin_"+name+"_items"); err != nil || exists {
		t.Errorf("--purge 后实体表应消失（引擎自管）: exists=%v err=%v", exists, err)
	}
}
