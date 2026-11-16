// plugin: kb v1.0.0
package search

import (
	"context"
	"strings"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/cli"

	"gorm.io/gorm"
)

// 库名守卫（DB 用例的**硬闸**，不是 skip）：目标库名必须含 "kb_test"。
//
// 为什么不是警告：本插件的库级用例会自建自清（raw DELETE）知识库表行——误连开发库（那里有其它
// 插件的安装行与实体数据）会真删数据。故库名不符一律 t.Fatalf 响亮中止整组用例。
// 跑法（隔离库）：
//
//	createdb -h <主机> -p <端口> -U <用户> aigo_kb_test
//	DATABASE_WRITE_DBNAME=aigo_kb_test PLUGIN_KB_DB=1 go -C ai-go-admin test -count=1 -p 1 ./plugins/kb/...
func requireKBTestDatabase(t *testing.T, db *gorm.DB) {
	t.Helper()
	if db == nil {
		t.Fatalf("数据库未初始化（database.Init 后 DB() 为空）")
	}
	var name string
	if err := db.Raw("SELECT current_database()").Scan(&name).Error; err != nil {
		t.Fatalf("读取当前库名失败: %v", err)
	}
	if !strings.Contains(name, "kb_test") {
		t.Fatalf("库级用例只允许在隔离库上跑（库名须含 kb_test），当前库 = %q；"+
			"误连开发库会真删知识库表行，已中止", name)
	}
}

// applyPluginMigrations 应用本插件自管迁移（隔离库夹具的前置；幂等——DDL 全为 IF NOT EXISTS）。
//
// 与安装器同一条链（cli.ApplyPluginMigrations）：版本表 <前缀>plugin_kb_migrations、
// 独立 advisory lock、与宿主 schema_migrations 互不干扰。库上需允许 `CREATE EXTENSION
// pg_trgm`（trusted）与 `vector`（pgvector，非 trusted ⇒ 隔离库须由有权限的账号预建）。
func applyPluginMigrations(t *testing.T) {
	t.Helper()
	env, err := cli.NewEnv()
	if err != nil {
		t.Fatalf("构造迁移环境失败: %v", err)
	}
	if _, err := cli.ApplyPluginMigrations(context.Background(), env, "kb", "plugins/kb/migrations"); err != nil {
		t.Fatalf("应用插件迁移失败（隔离库需允许建扩展 pg_trgm/vector）: %v", err)
	}
}
