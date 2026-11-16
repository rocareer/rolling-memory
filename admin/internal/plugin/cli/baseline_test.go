package cli

import (
	"bytes"
	"context"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"
)

// 存量库迁移基线（adopt）的用例（设计说明）。
//
// 两类口径：
//   - **纯单测**（本文件绝大多数用例）：三态判定（decideBaselineMode）、与迁移文件对账（verifyAdopt）、
//     版本归位（restoreVersion）、dry-run 的 ③b 段与零写入——都不连库；
//   - **库级用例**（baseline_db_test.go，PLUGIN_DB=1 门控）：存量臂 baseline 不执行 DDL、探针缺失拒装、
//     --purge 门禁负例；无 PLUGIN_DB 时跳过（验收口径见 README 的纱门条）。

// adoptSection 造一段 adopt（through 与逐版本探针）
func adoptSection(through int, probes ...manifest.AdoptProbe) *manifest.Adopt {
	return &manifest.Adopt{Through: through, Probes: probes}
}

// adoptManifest 造一份带 adopt 段的清单
func adoptManifest(through int, probes ...manifest.AdoptProbe) *manifest.Manifest {
	return &manifest.Manifest{
		Name: "demo", Title: "示例", Version: "1.0.0", Kind: "code", HostAPI: ">=1 <2",
		Adopt: adoptSection(through, probes...),
	}
}

// TestDecideBaselineMode 三态判定表（纯函数；判定口径只有这一处：预演与执行侧共用）
func TestDecideBaselineMode(t *testing.T) {
	t.Parallel()
	table := manifest.AdoptProbe{Version: 1, Kind: "table", Name: "eval_sets"}
	column := manifest.AdoptProbe{Version: 2, Kind: "column", Table: "eval_runs", Name: "title"}
	adopt := adoptSection(2, table, column)
	hitProbes := func(hit ...bool) []BaselineProbe {
		out := make([]BaselineProbe, 0, len(hit))
		for i, value := range hit {
			kind := "table"
			if i == 1 {
				kind = "column"
			}
			out = append(out, BaselineProbe{Version: i + 1, Kind: kind, Hit: value})
		}
		return out
	}

	cases := []struct {
		name   string
		adopt  *manifest.Adopt
		state  migrationState
		probes []BaselineProbe
		want   string
	}{
		{"未声明 adopt", nil, migrationState{}, nil, baselineModeNone},
		{"dirty 拒装", adopt, migrationState{Existed: true, Version: 2, Dirty: true}, hitProbes(true, true), baselineModeRejected},
		{"空版本表全命中 ⇒ baseline", adopt, migrationState{}, hitProbes(true, true), baselineModeAdopt},
		{"空版本表全不命中 ⇒ 全量", adopt, migrationState{}, hitProbes(false, false), baselineModeFull},
		{"空版本表部分命中 ⇒ 拒装", adopt, migrationState{}, hitProbes(true, false), baselineModeRejected},
		{"版本行在接管域内全命中 ⇒ baseline（复装/归位）", adopt, migrationState{Existed: true, Version: 1}, hitProbes(true, true), baselineModeAdopt},
		{"版本行在接管域内部分命中 ⇒ 拒装", adopt, migrationState{Existed: true, Version: 2}, hitProbes(true, false), baselineModeRejected},
		{"版本行在接管域内全不命中 ⇒ 拒装（版本行与结构不一致）", adopt, migrationState{Existed: true, Version: 2}, hitProbes(false, false), baselineModeRejected},
		{"版本行越过接管边界 ⇒ 全量（增量，探针不参与）", adopt, migrationState{Existed: true, Version: 5}, hitProbes(false, true), baselineModeFull},
		{"kind:none 不计入判定（全 none ⇒ 无判定力 ⇒ 拒装兜底）", adopt, migrationState{}, []BaselineProbe{{Version: 1, Kind: "none", Hit: true}}, baselineModeRejected},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			mode, reason := decideBaselineMode(c.adopt, "t_plugin_demo_migrations", c.state, c.probes)
			if mode != c.want {
				t.Fatalf("判定应为 %s，实际 %s（reason=%s）", c.want, mode, reason)
			}
			if mode == baselineModeRejected && strings.TrimSpace(reason) == "" {
				t.Error("拒装必须给出理由（照实上报，不静默）")
			}
		})
	}
}

// TestJudgedProbes kind: none 只作覆盖声明，不参与命中计数
func TestJudgedProbes(t *testing.T) {
	t.Parallel()
	probes := []BaselineProbe{
		{Version: 1, Kind: "table", Hit: true},
		{Version: 2, Kind: "none", Hit: true},
		{Version: 3, Kind: "column", Hit: false},
	}
	total, hit := judgedProbes(probes)
	if total != 2 || hit != 1 {
		t.Fatalf("对象探针计数应为 2/1，实际 %d/%d", total, hit)
	}
}

// TestRestoreVersion 版本归位：0（无版本行）必须用 -1（golang-migrate 的 NilVersion ⇒ 只 TRUNCATE 不插行）
func TestRestoreVersion(t *testing.T) {
	t.Parallel()
	if got := restoreVersion(0); got != -1 {
		t.Errorf("before=0 应归位为 -1（无行），实际 %d", got)
	}
	if got := restoreVersion(7); got != 7 {
		t.Errorf("before=7 应归位为 7，实际 %d", got)
	}
}

// TestProbeQuery 探针查询构造（**纯函数**）：表前缀按 kind 分——table / index / constraint 的被查名字
// 拼前缀，**column 的列名不拼**（只拼被查的表）。
//
// 事实对照（逐条读自本项目）：
//   - docs/sql-discipline.md:145：「表名 / 索引名 / **约束名**一律 `__PREFIX__` 占位符」——列表里有
//     约束名、没有列名；实例见 000005_plugin.up.sql（CONSTRAINT "__PREFIX__ck_admin_plugins_kind"）；
//   - 列名不带前缀：plugins/demo/migrations/1_init.up.sql（body 列）、
//     cmd/migrate/migrations/000001_admin.up.sql:22-23（COMMENT ON COLUMN "__PREFIX__admins"."id"）；
//   - 表前缀是受支持的配置：internal/infra/database/database.go:39-43 设为 GORM 的 TablePrefix。
//
// 早期实现把前缀**统一**拼到被查名字上 ⇒ 前缀非空时 column 探针恒不命中（要防的事故方向）；
// 约束名那一路的旧行为与本项目口径一致（见 probeQuery 注释）。
func TestProbeQuery(t *testing.T) {
	t.Parallel()
	const prefix = "t_"
	cases := []struct {
		name      string
		probe     manifest.AdoptProbe
		wantQuery string // 查询正文的子串（区分四种 kind）
		wantArgs  []any
	}{
		{
			"table：待查对象 = 前缀 + 逻辑名",
			manifest.AdoptProbe{Version: 1, Kind: "table", Name: "eval_sets"},
			"to_regclass", []any{"t_eval_sets"},
		},
		{
			"index：待查对象 = 前缀 + 逻辑名",
			manifest.AdoptProbe{Version: 2, Kind: "index", Name: "idx_eval_runs_title"},
			"relkind", []any{"t_idx_eval_runs_title"},
		},
		{
			"column：表拼前缀、列名**不拼**前缀",
			manifest.AdoptProbe{Version: 3, Kind: "column", Table: "eval_runs", Name: "title"},
			"attname", []any{"t_eval_runs", "title"},
		},
		{
			// 与 column 不同：约束名同表名/索引名口径（docs/sql-discipline.md:145）
			"constraint：表与约束名都拼前缀",
			manifest.AdoptProbe{Version: 4, Kind: "constraint", Table: "eval_runs", Name: "uk_eval_runs_title"},
			"conname", []any{"t_eval_runs", "t_uk_eval_runs_title"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			query, args, err := probeQuery(prefix, c.probe)
			if err != nil {
				t.Fatalf("探针查询构造不应报错: %v", err)
			}
			if !strings.Contains(query, c.wantQuery) {
				t.Errorf("查询应含 %q，实际 %q", c.wantQuery, query)
			}
			if !reflect.DeepEqual(args, c.wantArgs) {
				t.Errorf("args 应为 %v，实际 %v", c.wantArgs, args)
			}
			// 回归钉（F1 的错判就在这里）：列名**不得**带前缀（旧缺陷 = prefix + name 直接当 attname），
			// 而约束名**必须**带前缀（与 conname 的物理形态一致）
			if c.probe.Kind == "column" && args[1] != c.probe.Name {
				t.Errorf("列名必须是不带前缀的逻辑名 %q，实际 %v（带前缀会恒不命中）", c.probe.Name, args[1])
			}
			if c.probe.Kind == "constraint" && args[1] != prefix+c.probe.Name {
				t.Errorf("约束名应为物理名 %q，实际 %v", prefix+c.probe.Name, args[1])
			}
		})
	}

	// 展示口径同源：column 的物理名 = 前缀 + 表 + "." + 列名（列名不带前缀）；
	// constraint 的点后逐字是 conname（带前缀）——审计里照这个字符串去库里找对象
	column := manifest.AdoptProbe{Kind: "column", Table: "eval_runs", Name: "title"}
	if got := probePhysical(prefix, column); got != "t_eval_runs.title" {
		t.Errorf("列探针物理名应为 t_eval_runs.title，实际 %q", got)
	}
	constraint := manifest.AdoptProbe{Kind: "constraint", Table: "eval_runs", Name: "uk_x"}
	if got := probePhysical(prefix, constraint); got != "t_eval_runs.t_uk_x" {
		t.Errorf("约束探针物理名应为 t_eval_runs.t_uk_x（点后 = conname），实际 %q", got)
	}
	if got := probePhysical(prefix, manifest.AdoptProbe{Kind: "table", Name: "eval_sets"}); got != "t_eval_sets" {
		t.Errorf("表探针物理名应为 t_eval_sets，实际 %q", got)
	}

	// 未声明前缀（两仓现状）时四 kind 都用逻辑名（与旧行为一致，不能因本次修复回归）
	noPrefix := []struct {
		probe    manifest.AdoptProbe
		wantArgs []any
	}{
		{manifest.AdoptProbe{Kind: "table", Name: "eval_sets"}, []any{"eval_sets"}},
		{manifest.AdoptProbe{Kind: "index", Name: "idx_x"}, []any{"idx_x"}},
		{manifest.AdoptProbe{Kind: "column", Table: "eval_runs", Name: "title"}, []any{"eval_runs", "title"}},
		{manifest.AdoptProbe{Kind: "constraint", Table: "eval_runs", Name: "uk_x"}, []any{"eval_runs", "uk_x"}},
	}
	for _, c := range noPrefix {
		_, args, err := probeQuery("", c.probe)
		if err != nil {
			t.Fatalf("空前缀不应报错: %v", err)
		}
		if !reflect.DeepEqual(args, c.wantArgs) {
			t.Errorf("空前缀 args 应为 %v，实际 %v", c.wantArgs, args)
		}
	}

	// 负例：未知 kind 拒（闭集外）；不安全的表前缀 / 逻辑名拒（白名单）
	if _, _, err := probeQuery(prefix, manifest.AdoptProbe{Kind: "view", Name: "v"}); err == nil {
		t.Error("未知探针 kind 应报错")
	}
	if _, _, err := probeQuery("t-", manifest.AdoptProbe{Kind: "table", Name: "eval_sets"}); err == nil {
		t.Error("含非法字符的表前缀应报错（不进 SQL）")
	}
	if _, _, err := probeQuery(prefix, manifest.AdoptProbe{Kind: "column", Table: "eval_runs", Name: "bad name"}); err == nil {
		t.Error("非法列名应报错")
	}
}

// TestVerifyAdoptDDLGate 防漏门禁：含 DDL 对象创建的版本必须至少一条对象探针（kind: none 拒）；
// 纯注释/文案/回填版本允许 kind: none（判据 = 该版本 up 文件的语句事实，不靠作者自述）。
func TestVerifyAdoptDDLGate(t *testing.T) {
	t.Parallel()

	writeMigrations := func(t *testing.T, files map[string]string) string {
		t.Helper()
		dir := t.TempDir()
		for name, content := range files {
			mustWrite(t, filepath.Join(dir, name), content)
		}
		return dir
	}
	// 三个版本：建表 / 加列（ALTER TABLE ... ADD）/ 建唯一索引 + 一条纯回填
	withDDL := map[string]string{
		"1_init.up.sql":     "CREATE TABLE IF NOT EXISTS \"__PREFIX__eval_sets\" (id bigserial PRIMARY KEY);\n",
		"1_init.down.sql":   "DROP TABLE IF EXISTS \"__PREFIX__eval_sets\";\n",
		"2_column.up.sql":   "ALTER TABLE \"__PREFIX__eval_sets\" ADD COLUMN IF NOT EXISTS \"title\" varchar(64);\n",
		"2_column.down.sql": "ALTER TABLE \"__PREFIX__eval_sets\" DROP COLUMN IF EXISTS \"title\";\n",
		"3_index.up.sql": "CREATE UNIQUE INDEX IF NOT EXISTS \"__PREFIX__uk_eval_sets_title\"\n" +
			"    ON \"__PREFIX__eval_sets\" (\"title\");\n",
		"3_index.down.sql": "DROP INDEX IF EXISTS \"__PREFIX__uk_eval_sets_title\";\n",
	}

	t.Run("含 CREATE TABLE 的版本声明 kind: none ⇒ 拒", func(t *testing.T) {
		t.Parallel()
		dir := writeMigrations(t, withDDL)
		migrations, err := ScanMigrations(dir)
		if err != nil {
			t.Fatal(err)
		}
		adopt := adoptSection(3,
			manifest.AdoptProbe{Version: 1, Kind: "none"},
			manifest.AdoptProbe{Version: 2, Kind: "column", Table: "eval_sets", Name: "title"},
			manifest.AdoptProbe{Version: 3, Kind: "index", Name: "uk_eval_sets_title"},
		)
		err = verifyAdopt(dir, adopt, migrations)
		if err == nil {
			t.Fatal("建表版本声明 kind: none 应拒（防漏门禁）")
		}
		for _, want := range []string{"1_init.up.sql", "version 1", "CREATE TABLE", "kind: none", "拒装"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("拒装理由应含 %q，实际: %v", want, err)
			}
		}
	})

	t.Run("含 ALTER TABLE ... ADD 的版本声明 kind: none ⇒ 拒", func(t *testing.T) {
		t.Parallel()
		dir := writeMigrations(t, withDDL)
		migrations, _ := ScanMigrations(dir)
		adopt := adoptSection(3,
			manifest.AdoptProbe{Version: 1, Kind: "table", Name: "eval_sets"},
			manifest.AdoptProbe{Version: 2, Kind: "none"},
			manifest.AdoptProbe{Version: 3, Kind: "index", Name: "uk_eval_sets_title"},
		)
		err := verifyAdopt(dir, adopt, migrations)
		if err == nil || !strings.Contains(err.Error(), "ALTER TABLE ADD") {
			t.Fatalf("加列版本声明 kind: none 应拒并点名形态，实际: %v", err)
		}
	})

	t.Run("含 CREATE UNIQUE INDEX 的版本声明 kind: none ⇒ 拒", func(t *testing.T) {
		t.Parallel()
		dir := writeMigrations(t, withDDL)
		migrations, _ := ScanMigrations(dir)
		adopt := adoptSection(3,
			manifest.AdoptProbe{Version: 1, Kind: "table", Name: "eval_sets"},
			manifest.AdoptProbe{Version: 2, Kind: "column", Table: "eval_sets", Name: "title"},
			manifest.AdoptProbe{Version: 3, Kind: "none"},
		)
		err := verifyAdopt(dir, adopt, migrations)
		if err == nil || !strings.Contains(err.Error(), "CREATE UNIQUE INDEX") {
			t.Fatalf("建索引版本声明 kind: none 应拒并点名形态，实际: %v", err)
		}
	})

	t.Run("含 CREATE TABLE + table 探针 ⇒ 过；纯注释版本 + kind: none ⇒ 过", func(t *testing.T) {
		t.Parallel()
		dir := writeMigrations(t, map[string]string{
			"1_init.up.sql":   "CREATE TABLE IF NOT EXISTS \"__PREFIX__eval_sets\" (id bigserial PRIMARY KEY);\n",
			"1_init.down.sql": "DROP TABLE IF EXISTS \"__PREFIX__eval_sets\";\n",
			// 纯文案 / 回填版本：只有注释与一条 INSERT，不建任何对象
			"2_seed.up.sql": "-- 纯菜单 / 文案版本：本文件不建对象（种子由安装器代写）\n" +
				"INSERT INTO \"__PREFIX__eval_sets\" (\"id\") SELECT 1 WHERE NOT EXISTS (SELECT 1 FROM \"__PREFIX__eval_sets\");\n",
			"2_seed.down.sql": "SELECT 1;\n",
		})
		migrations, err := ScanMigrations(dir)
		if err != nil {
			t.Fatal(err)
		}
		adopt := adoptSection(2,
			manifest.AdoptProbe{Version: 1, Kind: "table", Name: "eval_sets"},
			manifest.AdoptProbe{Version: 2, Kind: "none"},
		)
		if err := verifyAdopt(dir, adopt, migrations); err != nil {
			t.Fatalf("建表版本有 table 探针、纯回填版本 kind: none，应通过: %v", err)
		}
	})

	t.Run("每个含 DDL 的版本各有对象探针 ⇒ 过（= 库级用例 prepareAdoptPluginDB 的 adopt 段形状）", func(t *testing.T) {
		t.Parallel()
		dir := writeMigrations(t, withDDL)
		migrations, err := ScanMigrations(dir)
		if err != nil {
			t.Fatal(err)
		}
		adopt := adoptSection(3,
			manifest.AdoptProbe{Version: 1, Kind: "table", Name: "eval_sets"},
			// 该版本建了两类对象（加列 + 建唯一索引），声明**一条**对象探针即满足门禁
			// （题面口径「必须至少一条对象探针」；探针完备度仍由作者判断）
			manifest.AdoptProbe{Version: 2, Kind: "column", Table: "eval_sets", Name: "title"},
			manifest.AdoptProbe{Version: 3, Kind: "index", Name: "uk_eval_sets_title"},
		)
		if err := verifyAdopt(dir, adopt, migrations); err != nil {
			t.Fatalf("三个版本各有对象探针，应通过: %v", err)
		}
	})

	t.Run("DDL 只在注释 / 字符串 / 美元引用块里 ⇒ 不误判", func(t *testing.T) {
		t.Parallel()
		dir := writeMigrations(t, map[string]string{
			"1_notes.up.sql": "-- 参照 CREATE TABLE __PREFIX__eval_sets 的旧形态（本版本不建对象）\n" +
				"SELECT 'CREATE TABLE \"__PREFIX__eval_sets\" (id bigint);';\n" +
				"DO $$\nBEGIN\n  EXECUTE 'ALTER TABLE \"__PREFIX__eval_sets\" ADD COLUMN \"x\" int';\nEND;\n$$;\n",
			"1_notes.down.sql": "SELECT 1;\n",
		})
		migrations, err := ScanMigrations(dir)
		if err != nil {
			t.Fatal(err)
		}
		adopt := adoptSection(1, manifest.AdoptProbe{Version: 1, Kind: "none"})
		if err := verifyAdopt(dir, adopt, migrations); err != nil {
			t.Fatalf("注释/字符串/美元引用块内的 DDL 形态不应触发门禁: %v", err)
		}
	})
}

// TestVerifyAdopt 与迁移文件事实对账（through = 最大编号 / 1..through 连续 / 探针对象名必须在 up 文件里）
func TestVerifyAdopt(t *testing.T) {
	t.Parallel()

	writeMigrations := func(t *testing.T, files map[string]string) string {
		t.Helper()
		dir := t.TempDir()
		for name, content := range files {
			mustWrite(t, filepath.Join(dir, name), content)
		}
		return dir
	}
	twoVersions := map[string]string{
		"1_init.up.sql":   "CREATE TABLE IF NOT EXISTS \"__PREFIX__eval_sets\" (id bigserial PRIMARY KEY);\n",
		"1_init.down.sql": "DROP TABLE IF EXISTS \"__PREFIX__eval_sets\";\n",
		"2_runs.up.sql": "ALTER TABLE \"__PREFIX__eval_runs\" ADD COLUMN IF NOT EXISTS \"title\" varchar(64);\n" +
			"CREATE INDEX IF NOT EXISTS \"__PREFIX__idx_eval_runs_title\" ON \"__PREFIX__eval_runs\" (\"title\");\n",
		"2_runs.down.sql": "DROP INDEX IF EXISTS \"__PREFIX__idx_eval_runs_title\";\n",
	}

	t.Run("合法（表探针 + 列探针 + 索引探针）", func(t *testing.T) {
		t.Parallel()
		dir := writeMigrations(t, twoVersions)
		migrations, err := ScanMigrations(dir)
		if err != nil {
			t.Fatal(err)
		}
		adopt := adoptSection(2,
			manifest.AdoptProbe{Version: 1, Kind: "table", Name: "eval_sets"},
			manifest.AdoptProbe{Version: 2, Kind: "column", Table: "eval_runs", Name: "title"},
			manifest.AdoptProbe{Version: 2, Kind: "index", Name: "idx_eval_runs_title"},
		)
		if err := verifyAdopt(dir, adopt, migrations); err != nil {
			t.Fatalf("合法 adopt 段应通过: %v", err)
		}
	})

	t.Run("through ≠ 最大编号", func(t *testing.T) {
		t.Parallel()
		dir := writeMigrations(t, twoVersions)
		migrations, _ := ScanMigrations(dir)
		adopt := adoptSection(1, manifest.AdoptProbe{Version: 1, Kind: "table", Name: "eval_sets"})
		if err := verifyAdopt(dir, adopt, migrations); err == nil || !strings.Contains(err.Error(), "最大编号") {
			t.Fatalf("through 与最大编号不符应拒，实际 %v", err)
		}
	})

	t.Run("编号不连续", func(t *testing.T) {
		t.Parallel()
		dir := writeMigrations(t, map[string]string{
			"1_init.up.sql":    "CREATE TABLE IF NOT EXISTS \"__PREFIX__eval_sets\" (id bigserial);\n",
			"1_init.down.sql":  "DROP TABLE IF EXISTS \"__PREFIX__eval_sets\";\n",
			"3_third.up.sql":   "CREATE TABLE IF NOT EXISTS \"__PREFIX__eval_third\" (id bigserial);\n",
			"3_third.down.sql": "DROP TABLE IF EXISTS \"__PREFIX__eval_third\";\n",
		})
		migrations, err := ScanMigrations(dir)
		if err != nil {
			t.Fatal(err)
		}
		adopt := adoptSection(3,
			manifest.AdoptProbe{Version: 1, Kind: "table", Name: "eval_sets"},
			manifest.AdoptProbe{Version: 2, Kind: "none"},
			manifest.AdoptProbe{Version: 3, Kind: "table", Name: "eval_third"},
		)
		if err := verifyAdopt(dir, adopt, migrations); err == nil || !strings.Contains(err.Error(), "缺编号 2") {
			t.Fatalf("编号不连续应拒，实际 %v", err)
		}
	})

	t.Run("虚探针（对象名不在该版本 up 文件里）", func(t *testing.T) {
		t.Parallel()
		dir := writeMigrations(t, twoVersions)
		migrations, _ := ScanMigrations(dir)
		adopt := adoptSection(2,
			manifest.AdoptProbe{Version: 1, Kind: "table", Name: "eval_sets"},
			manifest.AdoptProbe{Version: 2, Kind: "table", Name: "eval_not_there"},
		)
		err := verifyAdopt(dir, adopt, migrations)
		if err == nil || !strings.Contains(err.Error(), "找不到") {
			t.Fatalf("虚探针应拒（防虚方向），实际 %v", err)
		}
	})

	t.Run("探针名只出现在别的版本里也算虚", func(t *testing.T) {
		t.Parallel()
		dir := writeMigrations(t, twoVersions)
		migrations, _ := ScanMigrations(dir)
		adopt := adoptSection(2,
			manifest.AdoptProbe{Version: 2, Kind: "table", Name: "eval_sets"}, // 只在 1_init.up.sql 里
			manifest.AdoptProbe{Version: 1, Kind: "none"},
		)
		if err := verifyAdopt(dir, adopt, migrations); err == nil {
			t.Fatal("探针版本与对象所在文件不符应拒")
		}
	})

	t.Run("行注释里的名字不算数（去注释后比对）", func(t *testing.T) {
		t.Parallel()
		dir := writeMigrations(t, map[string]string{
			"1_init.up.sql":   "-- 参照 __PREFIX__eval_sets 的旧形态（本文件其实不建表）\nSELECT 1;\n",
			"1_init.down.sql": "SELECT 1;\n",
		})
		migrations, _ := ScanMigrations(dir)
		adopt := adoptSection(1, manifest.AdoptProbe{Version: 1, Kind: "table", Name: "eval_sets"})
		if err := verifyAdopt(dir, adopt, migrations); err == nil {
			t.Fatal("只在注释里出现的对象名应判为虚探针")
		}
	})

	t.Run("无迁移文件 / 未声明 adopt", func(t *testing.T) {
		t.Parallel()
		adopt := adoptSection(1, manifest.AdoptProbe{Version: 1, Kind: "table", Name: "eval_sets"})
		if err := verifyAdopt(t.TempDir(), adopt, nil); err == nil || !strings.Contains(err.Error(), "没有迁移文件") {
			t.Fatalf("声明 adopt 但无迁移应拒，实际 %v", err)
		}
		if err := verifyAdopt(t.TempDir(), nil, nil); err != nil {
			t.Fatalf("未声明 adopt 应直接通过: %v", err)
		}
	})
}

// TestBaselineReadNotDeclared 未声明 adopt ⇒ Mode=none 且不碰库（readBaseline 的零库访问分支）
func TestBaselineReadNotDeclared(t *testing.T) {
	t.Parallel()
	read, err := readBaseline(context.Background(), nil, "t_", "demo", nil)
	if err != nil {
		t.Fatalf("未声明 adopt 不应报错（也不应碰库）: %v", err)
	}
	if read.Mode != baselineModeNone || read.Declared || read.Table != "t_plugin_demo_migrations" {
		t.Fatalf("读数不符: %+v", read)
	}
}

// TestPreviewBaselineNotDeclared 预演侧同口径：未声明 adopt ⇒ 不取库句柄（dry-run 的「不碰库」不变量）
func TestPreviewBaselineNotDeclared(t *testing.T) {
	t.Parallel()
	env := &Env{Root: t.TempDir(), Out: &bytes.Buffer{}}
	plan := &InstallPlan{Name: "demo", Manifest: &manifest.Manifest{Name: "demo"}}
	read, err := previewBaseline(context.Background(), env, plan)
	if err != nil {
		t.Fatalf("未声明 adopt 的预演不应报错（库不可用也不该碰）: %v", err)
	}
	if read.Mode != baselineModeNone || read.Table != MigrationTableName("demo", "") {
		t.Fatalf("读数不符: %+v", read)
	}
}

// TestInstallDryRunBaselineSectionNoSideEffects dry-run 的 ③b 段与零写入：
// 用 kind=code 的骨架（无 adopt、无 L2 实体）跑预演——③b 段必须出现且说清「未声明 adopt」，
// 同时照旧不落任何文件、不改接线文件、不留档（旧口径不因本机制松动）。
func TestInstallDryRunBaselineSectionNoSideEffects(t *testing.T) {
	root := t.TempDir()
	for _, lang := range []string{"zh-cn", "en"} {
		mustWrite(t, filepath.Join(root, "web", "src", "lang", lang, ".keep"), "")
	}
	buf := &bytes.Buffer{}
	env := &Env{Root: root, Out: buf}
	name := "baseline-dry"
	if err := Scaffold(env, name, ScaffoldOptions{Kind: "code", Title: "基线预演用例插件"}); err != nil {
		t.Fatalf("脚手架应成功: %v", err)
	}
	buf.Reset()

	if err := Install(context.Background(), env, PluginDir(name), InstallOptions{DryRun: true, Operator: "dryrun"}); err != nil {
		t.Fatalf("--dry-run 应成功: %v", err)
	}
	text := buf.String()
	for _, want := range []string{
		"③b 迁移基线（adopt 存量库接管）",
		"manifest 未声明 adopt 段",
		"--dry-run：以上为全量预演",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("--dry-run 输出应含 %q\n--- 实际输出 ---\n%s", want, text)
		}
	}
	for _, target := range []string{path.Join(ViewTarget(name), "index.vue"), RegistryFile} {
		if _, err := os.Stat(env.Abs(target)); err == nil {
			t.Errorf("--dry-run 不应写出 %s", target)
		}
	}
}

// TestRenderBaselineSections ③b 段逐条打印：探针（含 none 免探）、命中/缺失、判定与「0 条旧 DDL」口径
func TestRenderBaselineSections(t *testing.T) {
	t.Parallel()
	plan := &InstallPlan{
		Name: "demo",
		Manifest: adoptManifest(3,
			manifest.AdoptProbe{Version: 1, Kind: "table", Name: "eval_sets"},
			manifest.AdoptProbe{Version: 2, Kind: "none"},
			manifest.AdoptProbe{Version: 3, Kind: "column", Table: "eval_runs", Name: "title"},
		),
	}
	plan.Baseline = &BaselineRead{
		Declared: true, Through: 3, Table: "t_plugin_demo_migrations",
		State: migrationState{}, Mode: baselineModeAdopt,
		Probes: []BaselineProbe{
			{Version: 1, Kind: "table", Name: "eval_sets", Physical: "t_eval_sets", Hit: true},
			{Version: 2, Kind: "none", Hit: true, Physical: "（无对象）"},
			{Version: 3, Kind: "column", Table: "eval_runs", Name: "title", Physical: "t_eval_runs.title", Hit: true},
		},
		ProbesTotal: 2, ProbesHit: 2,
	}
	buf := &bytes.Buffer{}
	plan.renderBaseline(buf)
	text := buf.String()
	for _, want := range []string{
		"adopt.through=3",
		"版本表 t_plugin_demo_migrations：不存在",
		"✓ v1    table      t_eval_sets",
		"· v2    none       （显式免探：该版本无对象创建）",
		"✓ v3    column     t_eval_runs.title",
		"baseline（adopt）",
		"迁移 SQL 0 条",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("③b 段应含 %q\n--- 实际输出 ---\n%s", want, text)
		}
	}

	// 拒装分支：缺失探针逐条列出 + 两条处置
	plan.Baseline = &BaselineRead{
		Declared: true, Through: 3, Table: "t_plugin_demo_migrations",
		Mode: baselineModeRejected, Reason: "探针只命中 1/2（部分命中）⇒ 拒装（fail-closed）",
		Probes: []BaselineProbe{
			{Version: 1, Kind: "table", Name: "eval_sets", Physical: "t_eval_sets", Hit: true},
			{Version: 3, Kind: "column", Table: "eval_runs", Name: "title", Physical: "t_eval_runs.title", Hit: false},
		},
		ProbesTotal: 2, ProbesHit: 1,
		Missing: []string{"✗ v3    column     t_eval_runs.title"},
	}
	buf.Reset()
	plan.renderBaseline(buf)
	text = buf.String()
	for _, want := range []string{"拒装", "✗ v3    column     t_eval_runs.title", "aigo migrate up", "migrate-force"} {
		if !strings.Contains(text, want) {
			t.Errorf("拒装分支应含 %q\n--- 实际输出 ---\n%s", want, text)
		}
	}
}

// TestRenderBaselineUnreadAndIncremental ③b 段的另两个分支：
//   - 读库失败 ⇒ 照实标「未完成（fail-closed）」，**不得**渲染成「未声明 adopt」（那会把连接失败误报成常规路径）；
//   - 版本行越过接管边界 ⇒ 增量（full）分支。
func TestRenderBaselineUnreadAndIncremental(t *testing.T) {
	t.Parallel()
	plan := &InstallPlan{Name: "demo", Manifest: adoptManifest(1, manifest.AdoptProbe{Version: 1, Kind: "table", Name: "eval_sets"})}

	plan.Baseline = &BaselineRead{
		Declared: true, Through: 1, Table: "t_plugin_demo_migrations",
		Mode: baselineModeUnread, Reason: "连接数据库失败: dial tcp 127.0.0.1:5432: connect: connection refused",
	}
	buf := &bytes.Buffer{}
	plan.renderBaseline(buf)
	text := buf.String()
	if !strings.Contains(text, "未完成") || !strings.Contains(text, "fail-closed") {
		t.Errorf("读库失败应照实标「未完成 + fail-closed」\n--- 实际输出 ---\n%s", text)
	}
	if strings.Contains(text, "未声明 adopt") {
		t.Errorf("读库失败不得渲染成「未声明 adopt」\n--- 实际输出 ---\n%s", text)
	}

	plan.Baseline = &BaselineRead{
		Declared: true, Through: 1, Table: "t_plugin_demo_migrations",
		State: migrationState{Existed: true, Version: 5}, Mode: baselineModeFull,
		Reason: "版本表 t_plugin_demo_migrations 已在 5（越过接管边界 1）：按插件链增量应用（接管段不再介入）",
	}
	buf.Reset()
	plan.renderBaseline(buf)
	text = buf.String()
	if !strings.Contains(text, "全量") || !strings.Contains(text, "越过接管边界") {
		t.Errorf("越过边界应渲染为全量/增量分支\n--- 实际输出 ---\n%s", text)
	}
}

// TestBaselineRejectErrorText 拒装错误的文案：带缺失探针清单 + 两条处置 + 插件名
func TestBaselineRejectErrorText(t *testing.T) {
	t.Parallel()
	read := &BaselineRead{Mode: baselineModeRejected, Reason: "探针只命中 1/2", Missing: []string{"✗ v2 table x"}}
	err := baselineRejectError("demo", read)
	for _, want := range []string{"demo", "探针只命中 1/2", "✗ v2 table x", "aigo migrate up", "aigo plugin migrate-force demo"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("拒装文案应含 %q，实际: %v", want, err)
		}
	}
}

// TestBaselineAuditDetail 审计的 baseline 段（mode/through/探针计数/version_table_created）
func TestBaselineAuditDetail(t *testing.T) {
	t.Parallel()
	none := baselineAuditDetail(nil)
	if none["mode"] != baselineModeNone {
		t.Errorf("未走迁移时应记 mode=none，实际 %v", none["mode"])
	}

	outcome := &MigrationOutcome{
		Table: "t_plugin_demo_migrations", TableExisted: false, Baselined: true, AdoptedThrough: 2,
		BaselineMode: baselineModeAdopt, ProbesTotal: 2, ProbesHit: 2, DeclaredThrough: 2,
	}
	detail := baselineAuditDetail(outcome)
	if detail["mode"] != baselineModeAdopt || detail["through"] != 2 ||
		detail["probes_total"] != 2 || detail["probes_hit"] != 2 || detail["version_table_created"] != true {
		t.Errorf("baseline 段不符: %+v", detail)
	}

	// 既有版本表（复装/归位）：version_table_created 必须为 false（补偿不会 drop 它）
	outcome.TableExisted = true
	if detail = baselineAuditDetail(outcome); detail["version_table_created"] != false {
		t.Errorf("既有版本表时 version_table_created 应为 false，实际 %v", detail["version_table_created"])
	}
}

// TestAdoptedLedgerNote ⑧ 的台账回写提示（未走基线时不留尾巴）
func TestAdoptedLedgerNote(t *testing.T) {
	t.Parallel()
	if got := adoptedLedgerNote(0); got != "" {
		t.Errorf("未走基线应为空串，实际 %q", got)
	}
	if got := adoptedLedgerNote(3); !strings.Contains(got, "adopted_through=3") {
		t.Errorf("应含 adopted_through=3，实际 %q", got)
	}
}
