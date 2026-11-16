package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"
	"github.com/ai-go-hub/ai-go-admin/internal/model"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"

	"gorm.io/gorm"
)

// 撞车与归属漂移的库级用例（PLUGIN_DB 门控；DB 门控两条）：
//
//	PLUGIN_DB=1 go test -count=1 -timeout=180s ./internal/plugin/cli/
//
//   - 名称撞车硬拦截：待写 admin_rules.name 与既有行同名 ⇒ 拒装即零副作用（前缀白名单是第一道、
//     DB 查重是第二道；同名 vue-router 路由是「删旧加新」，会静默替换宿主页面）；
//   - 归属漂移：「admin_rules.plugin <> '' 的行其 plugin 必须存在于 admin_plugins」——检测器以探针行
//     做正/负例，并在每个生命周期步骤后持有（含卸载后的零残留复验）。

// TestPluginRuleNameCollisionDB 名称撞车 ⇒ 安装被拒且零副作用（无台账/无归属行/无落位/无注册行/
// 无 success 审计；既有撞车行原地不动）——/第二道。
func TestPluginRuleNameCollisionDB(t *testing.T) {
	env, db := installDBSetup(t, "14")
	ctx := context.Background()
	name := pluginDBName("14")
	prefix := config.Get().Database.Prefix

	if err := Scaffold(env, name, ScaffoldOptions{Kind: "mixed", Title: "撞车用例插件"}); err != nil {
		t.Fatalf("脚手架应成功: %v", err)
	}

	// 既有宿主行（plugin=''，非插件物）与将要代写的公式节点同名
	collideName := "plugin/" + name + "/items/read"
	collide := model.AdminRule{
		Type: "node", Title: "撞车行", Name: collideName,
		Path: stringPtr(""), Icon: stringPtr(""), OpenType: stringPtr(""), URL: stringPtr(""),
		Component: stringPtr(""), Keepalive: uint8Ptr(0), Extend: stringPtr(""), Remark: stringPtr(""),
		Status: uint8Ptr(1),
	}
	if err := gorm.G[model.AdminRule](db).Create(ctx, &collide); err != nil {
		t.Fatalf("造撞车行失败: %v", err)
	}
	t.Cleanup(func() {
		db.WithContext(context.Background()).Exec(`DELETE FROM "`+prefix+`admin_rules" WHERE "id" = ?`, collide.ID)
	})

	installErr := Install(ctx, env, name, InstallOptions{Operator: "pdbtest"})
	if installErr == nil {
		t.Fatal("与既有 admin_rules 行同名应拒装（名称撞车硬拦截）")
	}
	if !strings.Contains(installErr.Error(), "同名") || !strings.Contains(installErr.Error(), collideName) {
		t.Errorf("拒装错误应点名撞车的规则名，实际 %v", installErr)
	}

	// 零副作用：无台账行 / 无本插件归属行 / 撞车行未被改动 / 无落位副本 / 无注册行 / 无 success 审计
	if row, ok, err := pluginLedger(ctx, db, name); ok || err != nil {
		t.Errorf("拒装不得写台账: row=%+v err=%v", row, err)
	}
	if count, err := gorm.G[model.AdminRule](db).Where("plugin = ?", name).Count(ctx, "*"); err != nil || count != 0 {
		t.Errorf("拒装不得写归属行，实际 %d err=%v", count, err)
	}
	if count, err := gorm.G[model.AdminRule](db).Where("id = ?", collide.ID).Count(ctx, "*"); err != nil || count != 1 {
		t.Errorf("既有撞车行不得被动，实际 %d err=%v", count, err)
	}
	if _, err := os.Stat(env.Abs(ViewTarget(name))); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("拒装不得落位副本: %v", err)
	}
	registered, err := RegisteredPlugins(env.Root)
	if err != nil {
		t.Fatalf("读取接线文件失败: %v", err)
	}
	if slices.Contains(registered, name) {
		t.Errorf("拒装不得写注册行，实际 %v", registered)
	}
	if count := auditCount(t, db, name, ActionInstall, AuditSuccess); count != 0 {
		t.Errorf("拒装不得留 install/success 审计，实际 %d", count)
	}
	assertNoOwnershipDrift(t, db)
}

// TestPluginOwnershipDriftDB归属漂移门控：admin_rules.plugin 列非空的行，其 plugin 必须存在于
// admin_plugins」；检测器用探针行做正/负例（先报出、清后归零），生命周期各步后持有，
// 卸载后与零残留一并复验（含角色组 rules CSV 无悬挂 ID）。
func TestPluginOwnershipDriftDB(t *testing.T) {
	env, db := installDBSetup(t, "15")
	ctx := context.Background()
	name := pluginDBName("15")
	prefix := config.Get().Database.Prefix

	if err := Scaffold(env, name, ScaffoldOptions{Kind: "mixed", Title: "漂移用例插件"}); err != nil {
		t.Fatalf("脚手架应成功: %v", err)
	}
	if err := Install(ctx, env, name, InstallOptions{Operator: "pdbtest"}); err != nil {
		t.Fatalf("安装应成功: %v", err)
	}
	assertNoOwnershipDrift(t, db) // 装后：归属行 ↔ 台账一致

	if err := Enable(ctx, env, name, "pdbtest"); err != nil {
		t.Fatalf("启用应成功: %v", err)
	}
	assertNoOwnershipDrift(t, db)
	if err := Disable(ctx, env, name, "pdbtest"); err != nil {
		t.Fatalf("停用应成功: %v", err)
	}
	assertNoOwnershipDrift(t, db)

	// 探针（正例）：手工插一条「台账没有的归属名」的行 ⇒ 检测器必须报出
	probe := name + "-probe"
	probeRow := model.AdminRule{Type: "node", Title: "漂移探针", Name: "plugin/" + probe + "/x"}
	if err := gorm.G[model.AdminRule](db).Create(ctx, &probeRow); err != nil {
		t.Fatalf("造探针行失败: %v", err)
	}
	// 归属列按列名直写（与安装器同口径）：不依赖 model 的归属字段，模块要能在无该字段的宿主上编译
	if _, err := gorm.G[model.AdminRule](db).Where("id = ?", probeRow.ID).Update(ctx, "plugin", probe); err != nil {
		t.Fatalf("写探针归属失败: %v", err)
	}
	t.Cleanup(func() {
		db.WithContext(context.Background()).Exec(`DELETE FROM "`+prefix+`admin_rules" WHERE "id" = ?`, probeRow.ID)
	})
	drift, err := ownershipDriftRows(ctx, db)
	if err != nil {
		t.Fatalf("漂移检测失败: %v", err)
	}
	if !slices.Contains(drift, probe) {
		t.Errorf("检测器应报出孤儿归属名 %s，实际 %v", probe, drift)
	}
	// 负例：清掉探针 ⇒ 归零（不误报）
	if _, err := gorm.G[model.AdminRule](db).Where("id = ?", probeRow.ID).Delete(ctx); err != nil {
		t.Fatalf("清理探针行失败: %v", err)
	}
	if drift, err = ownershipDriftRows(ctx, db); err != nil || slices.Contains(drift, probe) {
		t.Errorf("探针清理后应不再报出 %s，实际 %v err=%v", probe, drift, err)
	}

	// 角色组悬挂 ID：把本插件规则 ID 塞进组 CSV ⇒ 卸载后必须写回清除（零残留）
	rules, err := gorm.G[model.AdminRule](db).Where("plugin = ?", name).Order("id").Find(ctx)
	if err != nil || len(rules) == 0 {
		t.Fatalf("应读回本插件归属行: %d 行 err=%v", len(rules), err)
	}
	dangling := fmt.Sprintf("%d,777001", rules[0].ID)
	group := model.AdminGroup{Name: name + "-drift-group", Rules: &dangling, Status: uint8Ptr(1)}
	if err := gorm.G[model.AdminGroup](db).Create(ctx, &group); err != nil {
		t.Fatalf("建测试角色组失败: %v", err)
	}
	t.Cleanup(func() {
		db.WithContext(context.Background()).Exec(`DELETE FROM "`+prefix+`admin_groups" WHERE "id" = ?`, group.ID)
	})

	if err := Uninstall(ctx, env, name, UninstallOptions{Operator: "pdbtest"}); err != nil {
		t.Fatalf("卸载应成功: %v", err)
	}
	assertZeroResidue(t, env, db, name)
	assertNoOwnershipDrift(t, db)
	updated, err := gorm.G[model.AdminGroup](db).Select("rules").Where("id = ?", group.ID).First(ctx)
	if err != nil || updated.Rules == nil || *updated.Rules != "777001" {
		t.Errorf("卸载应写回清除悬挂规则 ID（保留 777001），实际 %v err=%v", updated.Rules, err)
	}
}

// ownershipDriftRows 归属漂移检查：admin_rules.plugin 列非空的行，其 plugin 必须在
// admin_plugins 有对应行。返回台账里没有对应行的归属名（升序去重）。行数口径：归属行与菜单树
// 同级（个位到几百），整列读可接受。
func ownershipDriftRows(ctx context.Context, db *gorm.DB) ([]string, error) {
	// 归属列按列名读（不依赖 model 的归属字段；与安装器/清理面同口径）
	type ownedRule struct {
		Plugin string
	}
	var owned []ownedRule
	table := config.Get().Database.Prefix + "admin_rules"
	if err := db.WithContext(ctx).Raw(`SELECT "plugin" FROM "` + table + `" WHERE "plugin" <> ''`).Scan(&owned).Error; err != nil {
		return nil, fmt.Errorf("读取归属规则行失败: %w", err)
	}
	ledger, err := gorm.G[host.Plugin](db).Select("name").Find(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取插件台账失败: %w", err)
	}
	known := make(map[string]struct{}, len(ledger))
	for i := range ledger {
		known[ledger[i].Name] = struct{}{}
	}
	var drift []string
	for i := range owned {
		if _, ok := known[owned[i].Plugin]; !ok {
			drift = append(drift, owned[i].Plugin)
		}
	}
	slices.Sort(drift)
	return slices.Compact(drift), nil
}

// assertNoOwnershipDrift 归属漂移断言（全局扫描：裸漂移行哪怕是别的用例/人工留下的，也一并点名——
// 这正是要拦的形态）
func assertNoOwnershipDrift(t *testing.T, db *gorm.DB) {
	t.Helper()
	drift, err := ownershipDriftRows(context.Background(), db)
	if err != nil {
		t.Fatalf("归属漂移检查失败: %v", err)
	}
	if len(drift) > 0 {
		t.Errorf("归属漂移：admin_rules.plugin<>'' 的行必须有对应台账行，孤儿归属名 %v", drift)
	}
}
