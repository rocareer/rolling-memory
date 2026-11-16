package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"
	"github.com/ai-go-hub/ai-go-admin/internal/infra/database"
	"github.com/ai-go-hub/ai-go-admin/internal/model"

	"github.com/golang-migrate/migrate/v4"
	"gorm.io/gorm"
)

// 管理页入口行（数据库迁移）的库级回归保护。
//
// 为什么单独一件：000006 补的是 000005 附注预告的「管理页菜单 + 3 个节点挂靠」——没有这一行，
// 前端动态路由与菜单树都不含管理页（连超管也无入口），且 authNode 无 /admin/plugin 键会让页面的
// 操作按钮恒被隐藏。这里把它钉住：菜单行字段口径 + 3 个节点逐行挂到该菜单名下。
//
// 只前进不回滚：游标已在目标版本 ⇒ 跳过推进；dirty ⇒ 报错不猜（与 host/install 两套库级用例同口径）。
//
//	PLUGIN_DB=1 go test -count=1 -timeout=120s ./internal/plugin/cli/
func TestPluginAdminMenuWiringDB(t *testing.T) {
	root := frameworkMigrationSetup(t) // PLUGIN_DB=1 门控 + chdir 仓库根 + config/database 初始化
	db := database.DB()
	if db == nil {
		t.Fatal("数据库句柄不可用")
	}
	ctx := context.Background()
	prefix := config.Get().Database.Prefix

	target, err := adminMenuMigrationVersion(root)
	if err != nil {
		t.Fatalf("取管理页菜单迁移编号失败: %v", err)
	}

	src, err := NewMigrationSource(filepath.Join(root, "cmd", "migrate", "migrations"), prefix)
	if err != nil {
		t.Fatalf("构造迁移源: %v", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, MigrateDSN("", ""))
	if err != nil {
		t.Fatalf("连接迁移驱动: %v", err)
	}
	defer func() { _, _ = m.Close() }()

	current, dirty := pluginVersion(t, m)
	if dirty {
		t.Fatalf("框架迁移处于 dirty（版本 %d）：先 `aigo migrate force %d` 修复", current, current)
	}
	if current < target {
		// 只补推进「最后一步」：游标更靠前说明宿主整链还没跑（本用例不代做宿主迁移——那是部署纪律）
		if current+1 < target {
			t.Skipf("框架迁移版本在 %d，距管理页菜单迁移 %d 尚差多步：先 `aigo migrate up`", current, target)
		}
		if err := m.Migrate(target); err != nil && !errors.Is(err, migrate.ErrNoChange) {
			t.Fatalf("推进到管理页菜单迁移 %d 失败: %v", target, err)
		}
		t.Logf("框架迁移已推进到 %d（管理页菜单行与节点挂靠）", target)
	}

	// 菜单行：字段口径即前端查表与鉴权的合同（component 精确路径 / open_type=tab / keepalive=1 / status=1）
	menu, err := gorm.G[model.AdminRule](db).Where("name = ? AND type = ?", "plugin", "menu").First(ctx)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("管理页菜单行缺失（name='plugin' AND type='menu'）：迁移 %d 未生效？", target)
	}
	if err != nil {
		t.Fatalf("读取管理页菜单行失败: %v", err)
	}
	if menu.Component == nil || *menu.Component != "/src/views/admin/plugin/index.vue" {
		t.Errorf("菜单 component 应精确指向管理页（import.meta.glob 按字符串查表），实际 %v", menu.Component)
	}
	if menu.Path == nil || *menu.Path != "plugin" {
		t.Errorf("菜单 path 应为 plugin（authNode 键 = /admin/plugin 由它派生），实际 %v", menu.Path)
	}
	if menu.OpenType == nil || *menu.OpenType != "tab" {
		t.Errorf("菜单 open_type 应为 tab（只有 tab + component 命中才注册前端路由），实际 %v", menu.OpenType)
	}
	if menu.Keepalive == nil || *menu.Keepalive != 1 {
		t.Errorf("菜单 keepalive 应为 1（组件名 = 菜单 name = plugin），实际 %v", menu.Keepalive)
	}
	if menu.Status == nil || *menu.Status != 1 {
		t.Errorf("菜单 status 应为 1（=0 则 /admin/init 不下发，超管也看不到入口），实际 %v", menu.Status)
	}

	// 3 个管理面节点逐行挂到该菜单名下（authNode 键 /admin/plugin/{read,enable,disable} 由此成立）
	names := []string{"plugin/read", "plugin/enable", "plugin/disable"}
	nodes, err := gorm.G[model.AdminRule](db).Where("name IN ?", names).Find(ctx)
	if err != nil {
		t.Fatalf("读取管理面节点失败: %v", err)
	}
	if len(nodes) != 3 {
		t.Fatalf("管理面节点应为 3 行（%s），实际 %d 行", strings.Join(names, "、"), len(nodes))
	}
	for i := range nodes {
		if nodes[i].Type != "node" {
			t.Errorf("规则 %s 的 type 应为 node，实际 %s", nodes[i].Name, nodes[i].Type)
		}
		if nodes[i].Pid == nil || *nodes[i].Pid != menu.ID {
			t.Errorf("节点 %s 应挂到管理页菜单 %d 名下（实际 pid=%v）：不挂则页面的 read/enable/disable 按钮恒被隐藏",
				nodes[i].Name, menu.ID, nodes[i].Pid)
		}
	}
}

// adminMenuMigrationVersion 取管理页菜单迁移的编号（glob *_plugin_admin_menu.up.sql，要求恰有一个）。
// 与 pluginMigrationVersion 同口径：编号从文件名取（跨仓 rename 到 max+1 后本用例仍成立）；
// 文件被改名/删除时本用例报红——这正是「入口行缺失」这类回归的信号。
func adminMenuMigrationVersion(root string) (uint, error) {
	matches, err := filepath.Glob(filepath.Join(root, "cmd", "migrate", "migrations", "*_plugin_admin_menu.up.sql"))
	if err != nil {
		return 0, fmt.Errorf("定位管理页菜单迁移文件: %w", err)
	}
	if len(matches) != 1 {
		return 0, fmt.Errorf("应恰有一个 *_plugin_admin_menu.up.sql（实际 %d 个）: %v", len(matches), matches)
	}
	version, err := strconv.ParseUint(strings.SplitN(filepath.Base(matches[0]), "_", 2)[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("迁移文件 %s 的编号不可解析: %w", matches[0], err)
	}
	return uint(version), nil
}
