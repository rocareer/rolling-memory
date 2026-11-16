package cli

// 插件迁移扫描（migrate.go）的用例：禁词表（含 admin_rules / admin_groups 两条宿主表禁词）与
// scanSQL 的 DDL 形态识别（adopt 段防漏门禁的判据，见 baseline.go 的 verifyAdopt）。
//
// 禁词的两条理由不同，判定位置也不同：
//   - CONCURRENTLY / BEGIN / COMMIT：整文件隐式事务内必报错 ⇒ 语句内任意位置命中；
//   - ADMIN_RULES / ADMIN_GROUPS：宿主表禁写（plugins/README.md「插件自带 SQL 不得写 admin_rules」）
//     ⇒ 任意位置命中即拒（注释与字符串里的提及不误报——插件迁移的文件头常写着「不写 admin_rules」）。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestScanSQLBannedCheckCoreTables 插件迁移禁写宿主菜单 / 角色组表：
// "__PREFIX__admin_rules"（规范写法，占位符在执行期换成表前缀）、裸写、以及带引号的形态都命中；
// 注释与字符串里的提及不误报。
func TestScanSQLBannedCheckCoreTables(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		sql   string
		want  []string // 期望命中的禁词（子集断言，按 bannedMigrationWords 顺序输出）
		clean bool
	}{
		{
			name: "占位符形态写 admin_rules（插件迁移的规范写法）",
			sql:  "INSERT INTO \"__PREFIX__admin_rules\" (\"name\") VALUES ('plugin/x');\n",
			want: []string{"ADMIN_RULES"},
		},
		{
			name: "裸写 admin_rules",
			sql:  "UPDATE admin_rules SET \"status\" = 0 WHERE \"name\" = 'plugin/x';\n",
			want: []string{"ADMIN_RULES"},
		},
		{
			name: "占位符形态写 admin_groups（授权关系 CSV）",
			sql:  "UPDATE \"__PREFIX__admin_groups\" SET \"rules\" = '' WHERE \"id\" = 1;\n",
			want: []string{"ADMIN_GROUPS"},
		},
		{
			name: "两个宿主表同时命中（顺序 = 禁词表顺序）",
			sql:  "DELETE FROM \"__PREFIX__admin_groups\";\nDELETE FROM \"__PREFIX__admin_rules\";\n",
			want: []string{"ADMIN_RULES", "ADMIN_GROUPS"},
		},
		{
			name:  "行注释里的提及不误报（插件迁移文件头就是这句）",
			sql:   "-- 本文件不写 admin_rules / admin_groups（菜单/节点由安装器按 manifest 代写）\nSELECT 1;\n",
			clean: true,
		},
		{
			name: "块注释与字符串里的提及不误报",
			sql: "/* admin_rules 是宿主表 */\n" +
				"INSERT INTO \"__PREFIX__plugin_x_notes\" (\"body\") SELECT 'admin_groups.rules 不计';\n",
			clean: true,
		},
		{
			name:  "自己插件的表名含近似词不误报（词边界不撞名）",
			sql:   "CREATE TABLE IF NOT EXISTS \"__PREFIX__plugin_x_admin_rules_notes\" (\"id\" bigserial PRIMARY KEY);\n",
			clean: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			scan := scanSQL(c.sql)
			banned := strings.Join(scan.Banned, ",")
			if c.clean {
				if len(scan.Banned) != 0 {
					t.Fatalf("不应命中禁词，实际 %q", banned)
				}
				return
			}
			for _, want := range c.want {
				if !strings.Contains(banned, want) {
					t.Errorf("应命中禁词 %s，实际 %q", want, banned)
				}
			}
		})
	}
}

// TestScanMigrationsBannedCoreTables 端到端（经 ScanMigrations 的迁移文件扫描）：含 admin_rules 的
// 迁移文件被判禁词——安装 / verify / dry-run 三处调用点据此拒装（install.go:99 / verify.go:77 / plan.go:340）。
func TestScanMigrationsBannedCoreTables(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "1_menu.up.sql"),
		"INSERT INTO \"__PREFIX__admin_rules\" (\"pid\", \"type\", \"title\", \"name\") "+
			"VALUES (0, 'menu', '示例', 'plugin/x/index');\n"+
			"INSERT INTO \"__PREFIX__admin_groups\" (\"name\") VALUES ('组');\n")
	mustWrite(t, filepath.Join(dir, "1_menu.down.sql"), "SELECT 1;\n")

	migrations, err := ScanMigrations(dir)
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	if len(migrations) != 2 {
		t.Fatalf("应扫描到 2 个文件: %+v", migrations)
	}
	got := strings.Join(migrations[0].Banned, ",")
	for _, want := range []string{"ADMIN_RULES", "ADMIN_GROUPS"} {
		if !strings.Contains(got, want) {
			t.Errorf("1_menu.up.sql 应命中 %s，实际 %q", want, got)
		}
	}
	if !strings.Contains(migrations[0].Describe(), "含禁词") {
		t.Errorf("逐文件摘要应报含禁词: %q", migrations[0].Describe())
	}
}

// TestScanSQLDDL scanSQL 的 DDL 形态识别（去重、按出现顺序）：三种「创建对象」形态命中，
// 回填 / 文案 / 其它 ALTER 形态不命中，注释 / 字符串 / 美元引用块内不误报。
func TestScanSQLDDL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		sql  string
		want []string
	}{
		{
			name: "CREATE TABLE（含 IF NOT EXISTS）",
			sql:  "CREATE TABLE IF NOT EXISTS \"__PREFIX__eval_sets\" (\"id\" bigserial PRIMARY KEY);\n",
			want: []string{"CREATE TABLE"},
		},
		{
			name: "CREATE INDEX",
			sql:  "CREATE INDEX IF NOT EXISTS \"__PREFIX__idx_x\" ON \"__PREFIX__eval_sets\" (\"id\");\n",
			want: []string{"CREATE INDEX"},
		},
		{
			name: "CREATE UNIQUE INDEX",
			sql:  "CREATE UNIQUE INDEX IF NOT EXISTS \"__PREFIX__uk_x\" ON \"__PREFIX__eval_sets\" (\"id\");\n",
			want: []string{"CREATE UNIQUE INDEX"},
		},
		{
			name: "ALTER TABLE ... ADD COLUMN / ADD CONSTRAINT（ONLY 与 IF EXISTS 会占位）",
			sql: "ALTER TABLE ONLY \"__PREFIX__eval_sets\" ADD CONSTRAINT \"uk_x\" UNIQUE (\"id\");\n" +
				"ALTER TABLE IF EXISTS \"__PREFIX__eval_sets\" ADD COLUMN IF NOT EXISTS \"title\" varchar(64);\n",
			want: []string{"ALTER TABLE ADD"},
		},
		{
			name: "去重且按出现顺序（多条同类语句只记一次）",
			sql: "ALTER TABLE \"__PREFIX__a\" ADD COLUMN \"b\" int;\n" +
				"CREATE TABLE IF NOT EXISTS \"__PREFIX__c\" (\"id\" bigserial);\n" +
				"ALTER TABLE \"__PREFIX__a\" ADD COLUMN \"d\" int;\n",
			want: []string{"ALTER TABLE ADD", "CREATE TABLE"},
		},
		{
			name: "纯回填 / 文案 / 统计版本：一条都不记",
			sql: "UPDATE \"__PREFIX__t\" SET \"b\" = 1 WHERE \"b\" IS NULL;\n" +
				"COMMENT ON TABLE \"__PREFIX__t\" IS '说明（含 create table 字样也不误报）';\n" +
				"ALTER TABLE \"__PREFIX__t\" ALTER COLUMN \"b\" SET DEFAULT 2;\n" +
				"DROP INDEX IF EXISTS \"__PREFIX__idx_old\";\n",
		},
		{
			// 闭集边界（非创建形态）：DROP / RENAME / SET 不记 —— 这些版本允许 kind: none
			name: "ALTER TABLE 的 DROP / RENAME / SET 不在闭集内",
			sql: "ALTER TABLE \"__PREFIX__t\" DROP COLUMN IF EXISTS \"b\";\n" +
				"ALTER TABLE \"__PREFIX__t\" RENAME COLUMN \"c\" TO \"d\";\n" +
				"ALTER TABLE \"__PREFIX__t\" SET (fillfactor = 90);\n",
		},
		{
			// 词法层假阳性（方向安全，故意钉住）：把 add 当标识名的 RENAME 会被判成 ALTER TABLE ADD ——
			// 不按位次取 ADD，是为了不漏 schema 限定名 / ONLY / IF EXISTS 等形态（宁可多拦不可漏，见函数注释）
			name: "假阳性（方向安全）：RENAME ... TO add 判为 ALTER TABLE ADD",
			sql:  "ALTER TABLE \"__PREFIX__t\" RENAME COLUMN \"b\" TO add;\n",
			want: []string{"ALTER TABLE ADD"},
		},
		{
			name: "注释 / 字符串 / 美元引用块里的 DDL 不误报",
			sql: "-- CREATE TABLE \"__PREFIX__x\" (\"id\" int);\n" +
				"/* ALTER TABLE \"__PREFIX__y\" ADD COLUMN \"z\" int; */\n" +
				"INSERT INTO \"__PREFIX__t\" (\"body\") SELECT 'CREATE UNIQUE INDEX \"__PREFIX__uk\" ON t (a)';\n" +
				"DO $$\nBEGIN\n  EXECUTE 'CREATE TABLE \"__PREFIX__x\" (\"id\" int)';\nEND;\n$$;\n",
		},
		{
			name: "无结尾分号的最后一条语句同样参与识别",
			sql:  "SELECT 1;\nCREATE TABLE \"__PREFIX__tail\" (\"id\" bigserial PRIMARY KEY)",
			want: []string{"CREATE TABLE"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			scan := scanSQL(c.sql)
			if strings.Join(scan.DDL, ",") != strings.Join(c.want, ",") {
				t.Errorf("DDL 形态应为 %v，实际 %v", c.want, scan.DDL)
			}
		})
	}
}

// TestShippedPluginMigrationsClean 随仓插件的迁移必须零禁词——扫描 plugins/<名>/migrations 全量目录
// （不依赖插件清单，日后新增插件迁移自动纳入），命中即红。
func TestShippedPluginMigrationsClean(t *testing.T) {
	root, err := FindRoot()
	if err != nil {
		t.Fatalf("定位仓库根: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "plugins"))
	if err != nil {
		t.Fatalf("读取 plugins/ 失败: %v", err)
	}
	scanned := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, "plugins", entry.Name(), "migrations")
		if _, err := os.Stat(dir); err != nil {
			continue // 无迁移目录的插件跳过（本机制只约束有迁移的插件）
		}
		migrations, err := ScanMigrations(dir)
		if err != nil {
			t.Fatalf("扫描 plugins/%s/migrations 失败: %v", entry.Name(), err)
		}
		for _, migration := range migrations {
			if len(migration.Banned) > 0 {
				t.Errorf("plugins/%s/migrations/%s 含禁词（%s）：插件迁移不得写宿主表 / 禁事务语句",
					entry.Name(), migration.Path, strings.Join(migration.Banned, ", "))
			}
		}
		scanned++
	}
	if scanned == 0 {
		t.Fatal("一个插件迁移目录都没扫到：本用例失去防护力（plugins/<名>/migrations 约定变了？）")
	}
}
