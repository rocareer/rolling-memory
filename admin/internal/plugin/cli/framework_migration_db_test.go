package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"
	"github.com/ai-go-hub/ai-go-admin/internal/infra/database"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/source"
	"gorm.io/gorm"
)

// 插件机制框架迁移（cmd/migrate/migrations/*_plugin.*.sql）的 up / down / up 往返（P1 验收项）。
//
// **隔离口径（关键）**：
//   - 全程在**临时 schema** 内执行（DSN 的 search_path 指向它，跑完 DROP）：应用 schema 不动，
//     与并行包（host 的库级用例）零干扰——插件迁移的 down 会 DROP 三张表，在应用 schema 上做往返必然踩踏；
//   - schema 内先建 `admin_rules` 的**骨架**（迁移要 ALTER 它）：应用 schema 已迁移 ⇒ 用 `LIKE` 复制真实
//     结构（列/默认值/NOT NULL 逐字一致）；未迁移 ⇒ 用与迁移语句逐列匹配的最小骨架（见 adminRulesStubSQL）；
//   - 迁移源只暴露**该插件迁移这一版**（singleVersionSource）：其后的链上还有会建扩展的迁移
//     （000034 pg_trgm / 000035 vector），整链冷启会把用例绑死在扩展可用性上；
//   - 版本号**从文件名取**（glob *_plugin.up.sql）：本项目/下游项目取号会重排，写死会失真。
//
// 门控：PLUGIN_DB=1 时连真实库（无该变量默认 `go test` 不连库）。
//
//	PLUGIN_DB=1 go test -count=1 -timeout=120s ./internal/plugin/cli/
func TestPluginMigrationRoundTripDB(t *testing.T) {
	root := frameworkMigrationSetup(t)
	db := database.DB()
	ctx := context.Background()

	target, err := pluginMigrationVersion(root)
	if err != nil {
		t.Fatalf("取插件迁移编号: %v", err)
	}

	// ① 隔离 schema（名字带 pid：与遗留 schema / 并发 gate 运行互不干扰）
	schema := fmt.Sprintf("plugin_migration_rt_%d", os.Getpid())
	quoted := quoteIdentIn(schema)
	if err := db.WithContext(ctx).Exec("DROP SCHEMA IF EXISTS " + quoted + " CASCADE").Error; err != nil {
		t.Fatalf("清理隔离 schema 失败: %v", err)
	}
	if err := db.WithContext(ctx).Exec("CREATE SCHEMA " + quoted).Error; err != nil {
		t.Skipf("无 CREATE SCHEMA 权限（隔离往返需要临时 schema）: %v", err)
	}
	t.Cleanup(func() {
		if err := db.WithContext(ctx).Exec("DROP SCHEMA IF EXISTS " + quoted + " CASCADE").Error; err != nil {
			t.Errorf("清理隔离 schema 失败: %v", err)
		}
	})
	if err := createAdminRulesSkeleton(ctx, db, schema); err != nil {
		t.Fatalf("隔离 schema 内建 admin_rules 骨架失败: %v", err)
	}

	// ② 迁移源：整个 migrations 目录 + 只暴露目标版本
	rawSrc, err := NewMigrationSource(filepath.Join(root, "cmd", "migrate", "migrations"), config.Get().Database.Prefix)
	if err != nil {
		t.Fatalf("构造迁移源: %v", err)
	}
	src := singleVersionSource{Driver: rawSrc, version: target}
	m, err := migrate.NewWithSourceInstance("iofs", src, MigrateDSN("", schema))
	if err != nil {
		t.Fatalf("连接迁移驱动: %v", err)
	}
	defer func() { _, _ = m.Close() }()

	// ③ up：隔离 schema 内应用该插件迁移。
	// 用 Up() 而不是 Migrate(target)：单版本源里没有「当前版本 → 目标版本」之间的中间版本，
	// Migrate 会按步数要 41 个版本、只找到 1 个而报 ErrShortLimit；Up()（limit=-1）到源尽头即停。
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("隔离 schema 内应用插件迁移 %d 失败: %v", target, err)
	}
	if version, _ := pluginVersion(t, m); version != target {
		t.Fatalf("up 后版本应为 %d，实际 %d", target, version)
	}
	requirePluginObjects(t, ctx, db, schema, true)

	// ④ down：必须完整逆向（3 张新表 + admin_rules.plugin 列与部分索引 + 3 个管理节点行）
	if err := m.Steps(-1); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("回滚插件迁移失败: %v", err)
	}
	if version, _ := pluginVersion(t, m); version != 0 {
		t.Fatalf("down 后应回到 nil 版本（0），实际 %d", version)
	}
	requirePluginObjects(t, ctx, db, schema, false)
	// 部分索引随列一并消失（列没了索引必没；此处显式断言索引名也不再存在）
	if exists, err := testIndexExistsIn(ctx, db, schema, config.Get().Database.Prefix+"idx_admin_rules_plugin"); err != nil {
		t.Fatalf("检查部分索引失败: %v", err)
	} else if exists {
		t.Error("down 后 admin_rules.plugin 的部分索引应已删除")
	}

	// ⑤ up：幂等重建（DDL 全 IF NOT EXISTS + 节点走 NOT EXISTS 判重；复装/改名重放都会再跑一次）
	if err := m.Steps(1); err != nil {
		t.Fatalf("重新应用插件迁移失败: %v", err)
	}
	if version, _ := pluginVersion(t, m); version != target {
		t.Fatalf("再次 up 后版本应为 %d，实际 %d", target, version)
	}
	requirePluginObjects(t, ctx, db, schema, true)
}

// adminRulesStubSQL 应用 schema 未迁移时用的最小 admin_rules 骨架：列集与三条语句逐列对齐
// （ALTER 加列、部分索引、节点 INSERT 的 16 列）。真实 admin_rules 由宿主数据库迁移建立，
// 且本插件迁移的语句已冻结 ⇒ 骨架只需满足它们的列引用。
const adminRulesStubSQL = `
CREATE TABLE %s.%s (
    "id"         bigserial   PRIMARY KEY,
    "pid"        bigint,
    "type"       varchar(64) NOT NULL DEFAULT '',
    "title"      varchar(64) NOT NULL DEFAULT '',
    "name"       varchar(64) NOT NULL DEFAULT '',
    "path"       varchar(255) DEFAULT '',
    "icon"       varchar(64) DEFAULT '',
    "open_type"  varchar(64) DEFAULT '',
    "url"        varchar(255) DEFAULT '',
    "component"  varchar(255) DEFAULT '',
    "keepalive"  smallint    NOT NULL DEFAULT 1,
    "extend"     varchar(64) DEFAULT '',
    "remark"     varchar(255) DEFAULT '',
    "weigh"      integer     NOT NULL DEFAULT 0,
    "status"     smallint    NOT NULL DEFAULT 1,
    "created_at" timestamptz,
    "updated_at" timestamptz
)`

// createAdminRulesSkeleton 在隔离 schema 内准备 admin_rules：优先 `LIKE` 复制真实表结构
// （应用 schema 已迁移时的最强对齐），否则用最小骨架
func createAdminRulesSkeleton(ctx context.Context, db *gorm.DB, schema string) error {
	table := quoteIdentIn(schema) + "." + quoteIdentIn(config.Get().Database.Prefix+"admin_rules")
	source := quoteIdentIn(config.Get().Database.Prefix + "admin_rules")

	exists, err := testTableExistsIn(ctx, db, currentSchemaName(ctx, db), config.Get().Database.Prefix+"admin_rules")
	if err != nil {
		return err
	}
	if exists {
		return db.WithContext(ctx).Exec("CREATE TABLE " + table + " (LIKE " + source + " INCLUDING DEFAULTS)").Error
	}
	return db.WithContext(ctx).Exec(fmt.Sprintf(adminRulesStubSQL, quoteIdentIn(schema), quoteIdentIn(config.Get().Database.Prefix+"admin_rules"))).Error
}

// pluginVersion 读迁移版本（nil 版本按 0）
func pluginVersion(t *testing.T, m *migrate.Migrate) (uint, bool) {
	t.Helper()
	version, dirty, err := m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return 0, dirty
	}
	if err != nil {
		t.Fatalf("读取迁移版本: %v", err)
	}
	return version, dirty
}

// pluginMigrationVersion 取插件机制框架迁移的编号（glob *_plugin.up.sql，要求恰有一个）
func pluginMigrationVersion(root string) (uint, error) {
	matches, err := filepath.Glob(filepath.Join(root, "cmd", "migrate", "migrations", "*_plugin.up.sql"))
	if err != nil {
		return 0, fmt.Errorf("定位插件迁移文件: %w", err)
	}
	if len(matches) != 1 {
		return 0, fmt.Errorf("应恰有一个 *_plugin.up.sql（实际 %d 个）: %v", len(matches), matches)
	}
	version, err := strconv.ParseUint(strings.SplitN(filepath.Base(matches[0]), "_", 2)[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("迁移文件 %s 的编号不可解析: %w", matches[0], err)
	}
	return uint(version), nil
}

// currentSchemaName 取连接默认 schema（用于定位应用 schema 里的真实表）
func currentSchemaName(ctx context.Context, db *gorm.DB) string {
	var schema string
	if err := db.WithContext(ctx).Raw("SELECT current_schema()").Scan(&schema).Error; err != nil {
		return ""
	}
	return schema
}

// frameworkMigrationSetup 连真实库（PLUGIN_DB gated）并返回仓库根
func frameworkMigrationSetup(t *testing.T) string {
	t.Helper()
	if os.Getenv("PLUGIN_DB") != "1" {
		t.Skip("需 PLUGIN_DB=1（连真实库，在隔离 schema 内做插件迁移的 down/up 往返）")
	}

	root, err := FindRoot()
	if err != nil {
		t.Fatalf("定位仓库根: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("切换到仓库根: %v", err)
	}
	if err := config.Init(); err != nil {
		t.Fatalf("初始化配置: %v", err)
	}
	if config.Get().Database.Write.Host == "" {
		t.Skip("仓库无 config/*.yaml，跳过 DB 集成测试")
	}
	if err := database.Init(); err != nil {
		t.Fatalf("初始化数据库: %v", err)
	}
	return root
}

// requirePluginObjects 断言插件迁移的对象在给定 schema 内齐备（want=true）或全清（want=false）
func requirePluginObjects(t *testing.T, ctx context.Context, db *gorm.DB, schema string, want bool) {
	t.Helper()
	prefix := config.Get().Database.Prefix
	for _, table := range []string{"admin_plugins", "admin_plugin_entities", "admin_plugin_actions"} {
		exists, err := testTableExistsIn(ctx, db, schema, prefix+table)
		if err != nil {
			t.Fatalf("检查数据表 %s 失败: %v", table, err)
		}
		if exists != want {
			t.Errorf("schema %s 内数据表 %s：存在=%v，期望=%v", schema, table, exists, want)
		}
	}
	exists, err := testColumnExistsIn(ctx, db, schema, prefix+"admin_rules", "plugin")
	if err != nil {
		t.Fatalf("检查 admin_rules.plugin 失败: %v", err)
	}
	if exists != want {
		t.Errorf("schema %s 内 admin_rules.plugin：存在=%v，期望=%v", schema, exists, want)
	}
	count := countPluginNodesIn(t, ctx, db, schema)
	if want && count != 3 {
		t.Errorf("schema %s 内管理面节点应为 3 行（plugin/read、plugin/enable、plugin/disable），实际 %d 行", schema, count)
	}
	if !want && count != 0 {
		t.Errorf("schema %s 内管理面节点应全部删除，实际 %d 行", schema, count)
	}
}

// countPluginNodesIn 统计给定 schema 内 3 个管理面节点行数
// （schema 与表名来自测试常量/进程号，标识符经 quoteIdentIn 引用）
func countPluginNodesIn(t *testing.T, ctx context.Context, db *gorm.DB, schema string) int64 {
	t.Helper()
	table := quoteIdentIn(schema) + "." + quoteIdentIn(config.Get().Database.Prefix+"admin_rules")
	var count int64
	query := "SELECT COUNT(*) AS count FROM " + table +
		` WHERE "name" IN ('plugin/read','plugin/enable','plugin/disable') AND "type" = 'node'`
	if err := db.WithContext(ctx).Raw(query).Scan(&count).Error; err != nil {
		t.Fatalf("统计管理面节点失败（%s）: %v", table, err)
	}
	return count
}

// testTableExistsIn 指定 schema 内的表存在性（pg_catalog；relkind='r' 只认普通表）
func testTableExistsIn(ctx context.Context, db *gorm.DB, schema, table string) (bool, error) {
	var count int64
	err := db.WithContext(ctx).Raw(`
SELECT COUNT(*) AS count
FROM pg_catalog.pg_class t
JOIN pg_catalog.pg_namespace n ON n.oid = t.relnamespace
WHERE n.nspname = ? AND t.relkind = 'r' AND t.relname = ?`, schema, table).Scan(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// testColumnExistsIn 指定 schema 内的列存在性（information_schema）
func testColumnExistsIn(ctx context.Context, db *gorm.DB, schema, table, column string) (bool, error) {
	var count int64
	err := db.WithContext(ctx).Raw(`
SELECT COUNT(*) AS count
FROM information_schema.columns
WHERE table_schema = ? AND table_name = ? AND column_name = ?`, schema, table, column).Scan(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// testIndexExistsIn 指定 schema 内的索引存在性
func testIndexExistsIn(ctx context.Context, db *gorm.DB, schema, index string) (bool, error) {
	var count int64
	err := db.WithContext(ctx).Raw(`
SELECT COUNT(*) AS count
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = ? AND c.relkind = 'i' AND c.relname = ?`, schema, index).Scan(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// quoteIdentIn 双引号引用标识符（schema 名由测试生成：固定前缀 + 进程号，不含引号）
func quoteIdentIn(ident string) string {
	return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
}

// singleVersionSource 只暴露一个版本的迁移源：驱动看不到其它版本，
// 于是 Migrate(target) 只应用该版、Steps(-1) 只回滚该版（其余版本由 ReadUp/ReadDown 的 os.ErrNotExist 挡掉）。
type singleVersionSource struct {
	source.Driver
	version uint
}

// First 首个（也是唯一）可用版本
func (s singleVersionSource) First() (uint, error) { return s.version, nil }

// Prev 没有更早的版本 ⇒ ErrNotExist（readDown 走「回滚首版」分支，把版本置回 nil）
func (s singleVersionSource) Prev(uint) (uint, error) { return 0, os.ErrNotExist }

// Next 尚未到达目标版本 ⇒ 直接给目标版本；否则结束
func (s singleVersionSource) Next(version uint) (uint, error) {
	if version < s.version {
		return s.version, nil
	}
	return 0, os.ErrNotExist
}
