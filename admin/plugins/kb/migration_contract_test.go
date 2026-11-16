// plugin: kb v1.0.0
// 迁移冻结契约（漂移闸，**不连库**）：解析 migrations/*.sql，断言「建的表名集合 + 索引名集合」
// 与冻结清单逐项相等。
//
// 为什么要这个用例：插件的表全部由插件自管迁移治理（宿主 L2 引擎不参与），而迁移与模型/代码之间
// 没有编译期约束——少一条索引（尤其单飞判据的部分唯一索引）或漏一张表，只有在运行期才炸。
// 本用例把「迁移 = 冻结清单」写成可复跑的断言：改 DDL 必须同步改本清单（改清单即改口径，评审可见）。
//
// 口径：只认非注释行里行首的 `CREATE TABLE [IF NOT EXISTS]`/`CREATE [UNIQUE] INDEX [IF NOT EXISTS]`；
// 对象名归一化（去 `__PREFIX__` 占位符）后比对，不比列级差异（列级由运行期 schema 与用例覆盖）。
//
// **占位符闸**（TestMigrationObjectNamesArePrefixed）：每个 CREATE TABLE/CREATE [UNIQUE] INDEX 的
// 对象名都必须以 `__PREFIX__` 起头。原实现只断言「文件正文出现过 `__PREFIX__`」——表名已满足该条件，
// 于是索引名漏占位符（非空前缀部署下索引名不随前缀隔离，同 schema 内多前缀/多实例直接撞名）不会红。
package kb

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// migrationTables 冻结的表清单（18 张：框架线 17 + 作业账本 1；逻辑名，表前缀由执行期叠加）
var migrationTables = []string{
	// 1_core（6）
	"kb_audit_log", "kb_chunk", "kb_document", "kb_grant", "kb_library", "kb_team",
	// 2_pipeline（5）
	"kb_blob", "kb_blob_ref", "kb_chunk_vectors", "kb_extract", "kb_produce_state",
	// 3_search_log（1）
	"kb_search_log",
	// 4_tags（4）
	"kb_tag", "kb_tag_group", "kb_tag_link", "kb_tag_rule",
	// 5_cost_log（1）
	"kb_cost_log",
	// 6_jobs（1）
	"plugin_kb_jobs",
}

// migrationIndexes 逐名冻结的索引（作业账本 3 条）；框架线那批 41 条只按命名约定 + 总数 44（=41+3）
// 断言——改名/互换不会报红（与 README「九」的披露同口径）
var migrationIndexes = []string{
	// 6_jobs（3）：作业账本的不变量实现处（单飞部分唯一索引 + 领取面 + 读数面）
	"uk_plugin_kb_jobs_active_key", "idx_plugin_kb_jobs_due", "idx_plugin_kb_jobs_kind_status",
}

// migrationFiles 迁移文件（编号 → 文件名；与 plugin.yaml 的迁移编号口径一致）
var migrationFiles = []string{
	"1_core.up.sql", "2_pipeline.up.sql", "3_search_log.up.sql",
	"4_tags.up.sql", "5_cost_log.up.sql", "6_jobs.up.sql",
}

var (
	// 对象名捕获到全名（含 __PREFIX__ 占位符）：归一化与占位符闸共用同一份正则
	reCreateTable = regexp.MustCompile(`(?i)^CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?"?([A-Za-z0-9_]+)`)
	reCreateIndex = regexp.MustCompile(`(?i)^CREATE\s+(?:UNIQUE\s+)?INDEX\s+(?:IF\s+NOT\s+EXISTS\s+)?"?([A-Za-z0-9_]+)`)
)

// TestMigrationContract 迁移对象集 = 冻结清单（表 18 / 索引 44；up 与 down 成对）
func TestMigrationContract(t *testing.T) {
	dir := filepath.Join(pluginDirOfTest(t), "migrations")

	tables := map[string]struct{}{}
	indexes := map[string]struct{}{}
	for _, name := range migrationFiles {
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读取迁移 %s 失败: %v", name, err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "--") {
				continue
			}
			normalized := strings.ReplaceAll(trimmed, "__PREFIX__", "")
			if match := reCreateTable.FindStringSubmatch(normalized); match != nil {
				tables[match[1]] = struct{}{}
			}
			if match := reCreateIndex.FindStringSubmatch(normalized); match != nil {
				indexes[match[1]] = struct{}{}
			}
		}
		if _, err := os.Stat(filepath.Join(dir, strings.Replace(name, ".up.sql", ".down.sql", 1))); err != nil {
			t.Fatalf("迁移 %s 缺 down 对（up/down 必须成对）: %v", name, err)
		}
	}

	gotTables := make([]string, 0, len(tables))
	for name := range tables {
		gotTables = append(gotTables, name)
	}
	sort.Strings(gotTables)
	wantTables := slices.Clone(migrationTables)
	sort.Strings(wantTables)
	if !slices.Equal(gotTables, wantTables) {
		t.Fatalf("迁移建的表集合与冻结清单不一致：\n迁移 %v\n清单 %v", gotTables, wantTables)
	}

	// 索引：冻结清单里逐条点名核对（框架线的 41 条按命名约定在迁移内，此处断言前缀与总量）
	if len(indexes) != 44 {
		names := make([]string, 0, len(indexes))
		for name := range indexes {
			names = append(names, name)
		}
		sort.Strings(names)
		t.Fatalf("迁移建的索引数 = %d，期望 44（框架线 41 + 作业账本 3）：%v", len(indexes), names)
	}
	for _, name := range migrationIndexes {
		if _, ok := indexes[name]; !ok {
			t.Fatalf("作业账本索引 %s 未在迁移里（作业面单飞/领取/读数依赖它）", name)
		}
	}
}

// TestMigrationObjectNamesArePrefixed 逐条对象名的占位符闸：表名与索引名都必须带 `__PREFIX__`。
//
// 为什么单独一条：见文件头注——「文件里出现过占位符」不足以证明**每个对象名**都带；
// 而前缀缺位只在「非空表前缀 + 同 schema 多实例」的部署上炸（迁移失败/索引撞名），
// 单机默认前缀（空）下永远不炸，属最容易被漏掉的一类纪律。
func TestMigrationObjectNamesArePrefixed(t *testing.T) {
	dir := filepath.Join(pluginDirOfTest(t), "migrations")
	for _, name := range migrationFiles {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("读取迁移 %s 失败: %v", name, err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "--") {
				continue
			}
			for _, re := range []*regexp.Regexp{reCreateTable, reCreateIndex} {
				match := re.FindStringSubmatch(trimmed)
				if match == nil {
					continue
				}
				if !strings.HasPrefix(match[1], "__PREFIX__") {
					t.Fatalf("%s:%d 对象名 %q 缺 __PREFIX__ 占位符（表名/索引名必须随配置表前缀隔离）：%s",
						name, i+1, match[1], trimmed)
				}
			}
		}
	}
}

// TestMigrationStatics 迁移静态纪律（安装期会被再查一遍，此处前置到用例，改错立刻可见）：
// 无语句级 CONCURRENTLY；无 ADMIN_RULES 直写（菜单由清单声明、安装器代写）；表名/索引名带 __PREFIX__ 占位符。
func TestMigrationStatics(t *testing.T) {
	dir := filepath.Join(pluginDirOfTest(t), "migrations")
	for _, name := range migrationFiles {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("读取迁移 %s 失败: %v", name, err)
		}
		body := string(raw)
		for _, line := range strings.Split(body, "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "--") {
				continue
			}
			upper := strings.ToUpper(trimmed)
			if strings.Contains(upper, "CONCURRENTLY") {
				t.Fatalf("%s 含禁用语句 CONCURRENTLY（整文件隐式事务内必报错）：%s", name, trimmed)
			}
			if strings.Contains(upper, "ADMIN_RULES") || strings.Contains(upper, "ADMIN_GROUPS") {
				t.Fatalf("%s 直写宿主菜单/角色组表（菜单与节点由插件清单声明、安装器代写）：%s", name, trimmed)
			}
			if strings.HasPrefix(upper, "BEGIN") || strings.HasPrefix(upper, "COMMIT") {
				t.Fatalf("%s 含显式事务语句（整文件一个隐式事务）：%s", name, trimmed)
			}
		}
		if !strings.Contains(body, `"__PREFIX__`) {
			t.Fatalf("%s 未使用 __PREFIX__ 表名占位符（表前缀由执行期配置叠加）", name)
		}
	}
}

// pluginDirOfTest 定位插件根（测试工作目录 = 本包目录 ⇒ 向上找插件根：含 plugin.yaml 与 migrations）
func pluginDirOfTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "plugin.yaml")); err == nil {
		return dir
	}
	// go test 的工作目录是包目录（plugins/kb）——兜底：向上逐级找
	for {
		if _, err := os.Stat(filepath.Join(dir, "plugin.yaml")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("未找到插件根（plugin.yaml）")
		}
		dir = parent
	}
}
