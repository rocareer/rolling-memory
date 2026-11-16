package cli

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/l2/ddlplan"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"
)

// TestBuildRegistryFileMatchesSkeleton 仓库注册文件必须与生成器按「文件内已登记的名字」重放的结果
// 逐字节一致（P5 的 check-plugin-registry.sh 与安装器的重放都依赖这一点）。
//
// 出厂空骨架是 names 为空的特例；装过插件（register_gen 有注册行）同样必须逐字节等于按自身注册名
// 重放的输出——只比空骨架的写法会在装过插件的仓上误红（本项目现为「已装 demo」形态）。
func TestBuildRegistryFileMatchesSkeleton(t *testing.T) {
	root, err := FindRoot()
	if err != nil {
		t.Fatalf("定位仓库根: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(RegistryFile)))
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", RegistryFile, err)
	}
	names, err := RegisteredPluginsFrom(content)
	if err != nil {
		t.Fatalf("回解析 %s 失败: %v", RegistryFile, err)
	}
	if got := string(BuildRegistryFile(names)); got != string(content) {
		t.Errorf("注册文件与生成器按注册名（%v）重放不一致\n--- 生成器 ---\n%s\n--- 仓库文件 ---\n%s", names, got, content)
	}
	if !strings.HasPrefix(string(content), GeneratedMarker) {
		t.Errorf("出厂骨架必须以生成标记开头: %q", GeneratedMarker)
	}
	if err := CheckSkeleton(root); err != nil {
		t.Errorf("出厂骨架应通过 CheckSkeleton: %v", err)
	}
}

// TestBuildRegistryFileWithNames 生成物：排序去重 + 每行一个空白导入
func TestBuildRegistryFileWithNames(t *testing.T) {
	content := string(BuildRegistryFile([]string{"zeta", "alpha", "alpha", " beta "}))
	alpha := strings.Index(content, `_ "`+ImportPath("alpha")+`"`)
	beta := strings.Index(content, `_ "`+ImportPath("beta")+`"`)
	zeta := strings.Index(content, `_ "`+ImportPath("zeta")+`"`)
	if alpha < 0 || beta < 0 || zeta < 0 {
		t.Fatalf("生成的注册行缺失:\n%s", content)
	}
	if !(alpha < beta && beta < zeta) {
		t.Errorf("注册行应按名字升序:\n%s", content)
	}
	if strings.Count(content, ImportPath("alpha")) != 1 {
		t.Errorf("重复名应去重:\n%s", content)
	}
	names, err := RegisteredPluginsFrom([]byte(content))
	if err != nil {
		t.Fatalf("回解析失败: %v", err)
	}
	if strings.Join(names, ",") != "alpha,beta,zeta" {
		t.Errorf("回解析结果不符: %v", names)
	}
}

// TestRegistryDiff 差异渲染：安装一个插件 ⇒ 只多一行 +
func TestRegistryDiff(t *testing.T) {
	current := BuildRegistryFile(nil)
	next := BuildRegistryFile([]string{"demo"})
	diff := RegistryDiff(current, next)
	if !strings.Contains(diff, ImportPath("demo")) {
		t.Errorf("差异应含新增注册行:\n%s", diff)
	}
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "  - ") {
			t.Errorf("空骨架 → 一个插件不应有删除行: %q", line)
		}
	}
	if got := RegistryDiff(current, current); !strings.Contains(got, "无变化") {
		t.Errorf("相同内容应报无变化: %q", got)
	}
}

// TestScanMigrations 插件迁移扫描：编号唯一且成对、禁词、语句数
func TestScanMigrations(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("1_init.up.sql", "CREATE TABLE t (id bigint);\nINSERT INTO t VALUES (1);\n")
	write("1_init.down.sql", "DROP TABLE IF EXISTS t;\n")
	// 美元引用块里的 BEGIN（PL/pgSQL 函数体）不应误报；语句级 BEGIN 才是禁词
	write("2_more.up.sql", "CREATE FUNCTION f() RETURNS void AS $$\nBEGIN\n  RETURN;\nEND;\n$$ LANGUAGE plpgsql;\n")
	write("2_more.down.sql", "DROP FUNCTION IF EXISTS f();\n")

	migrations, err := ScanMigrations(dir)
	if err != nil {
		t.Fatalf("合法迁移目录应通过: %v", err)
	}
	if len(migrations) != 4 {
		t.Fatalf("应扫描到 4 个文件: %+v", migrations)
	}
	if migrations[0].Kind != "up" || migrations[0].Version != 1 {
		t.Errorf("排序应为 版本升序、up 在 down 前: %+v", migrations[0])
	}
	if migrations[0].Statements != 2 {
		t.Errorf("1_init.up.sql 应为 2 条语句，实际 %d", migrations[0].Statements)
	}
	for _, migration := range migrations {
		if len(migration.Banned) != 0 {
			t.Errorf("美元引用块内的 BEGIN 与普通 DDL 不应报禁词: %+v", migration)
		}
	}

	// 单条语句 BEGIN;（文件内显式事务，禁写）与 CONCURRENTLY
	banDir := t.TempDir()
	mustWrite(t, filepath.Join(banDir, "1_x.up.sql"), "BEGIN;\nCREATE INDEX CONCURRENTLY idx_x ON t (id);\nCOMMIT;\n")
	mustWrite(t, filepath.Join(banDir, "1_x.down.sql"), "SELECT 1;")
	scanned, err := ScanMigrations(banDir)
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	banned := strings.Join(scanned[0].Banned, ",")
	if !strings.Contains(banned, "CONCURRENTLY") || !strings.Contains(banned, "BEGIN") || !strings.Contains(banned, "COMMIT") {
		t.Errorf("应报出 CONCURRENTLY / BEGIN / COMMIT 三个禁词，实际 %q", banned)
	}

	// 编号不成对 / 重复 / 文件名不合规 一律拒绝
	pairDir := t.TempDir()
	mustWrite(t, filepath.Join(pairDir, "1_x.up.sql"), "SELECT 1;")
	if _, err := ScanMigrations(pairDir); err == nil {
		t.Error("缺 down 文件应报错（up/down 必须成对）")
	}
	dupDir := t.TempDir()
	mustWrite(t, filepath.Join(dupDir, "1_x.up.sql"), "SELECT 1;")
	mustWrite(t, filepath.Join(dupDir, "1_x.down.sql"), "SELECT 1;")
	mustWrite(t, filepath.Join(dupDir, "1_y.up.sql"), "SELECT 1;")
	if _, err := ScanMigrations(dupDir); err == nil {
		t.Error("同编号重复声明应报错")
	}
	badDir := t.TempDir()
	mustWrite(t, filepath.Join(badDir, "init.sql"), "SELECT 1;")
	if _, err := ScanMigrations(badDir); err == nil {
		t.Error("文件名不合规应报错")
	}

	// 目录不存在 ⇒ 空清单（「无迁移可应用」）
	if migrations, err := ScanMigrations(filepath.Join(dir, "absent")); err != nil || len(migrations) != 0 {
		t.Errorf("不存在的迁移目录应返回空清单: %v, %v", migrations, err)
	}
}

// TestBuildInstallMenusFormula菜单/节点公式（F2 代写 + F3 拒装；纯函数面，不连库）：
//   - 声明为空（纯声明式形态）⇒ 分组 + 实体页 + 权限节点全部由公式代写；
//   - 声明了同一公式的行 ⇒ 不重复写，以声明为准（标题/图标/权重由作者定）；
//   - 声明与公式不符（path / component / pid / 节点动作 / sort 当 drag_sort=false）⇒ 拒装。
func TestBuildInstallMenusFormula(t *testing.T) {
	t.Parallel()
	dragEntity := manifest.Entity{
		Entity: "items", TitleZh: "条目", TitleEn: "Items", DragSort: true,
		Fields: []manifest.Field{{Name: "title", Type: "string", Length: 32, Form: "input"}},
	}
	plainEntity := manifest.Entity{
		Entity: "items", TitleZh: "条目", TitleEn: "Items",
		Fields: []manifest.Field{{Name: "title", Type: "string", Length: 32, Form: "input"}},
	}
	makeManifest := func(menus []manifest.Menu, entities []manifest.Entity) *manifest.Manifest {
		return &manifest.Manifest{
			Name: "demo", Title: "示例插件", Version: "1.0.0", Kind: "mixed",
			HostAPI: ">=1 <2", Menus: menus, Entities: entities,
		}
	}
	byName := func(menus []manifest.Menu) {
		t.Helper()
		seen := make(map[string]struct{}, len(menus))
		for _, menu := range menus {
			if _, dup := seen[menu.Name]; dup {
				t.Fatalf("代写清单出现同名规则 %s（重复写入面）", menu.Name)
			}
			seen[menu.Name] = struct{}{}
		}
	}

	// ① 无声明：公式补齐分组 + 实体页 + 5 个权限节点（drag_sort ⇒ 含 sort）
	menus, err := BuildInstallMenus("demo", makeManifest(nil, []manifest.Entity{dragEntity}))
	if err != nil {
		t.Fatalf("空声明应可代写（纯声明式插件装后必须有菜单入口）: %v", err)
	}
	want := []string{
		"plugin/demo", "plugin/demo/items",
		"plugin/demo/items/read", "plugin/demo/items/create", "plugin/demo/items/update",
		"plugin/demo/items/delete", "plugin/demo/items/sort",
	}
	if len(menus) != len(want) {
		t.Fatalf("应代写 %d 条（%v），实际 %d: %v", len(want), want, len(menus), menus)
	}
	byName(menus)
	dir := lookupPage(menus, "plugin/demo")
	if dir.Type != "dir" || dir.Path != "plugin/demo" || dir.Pid != "" {
		t.Errorf("分组行应为 dir/path=plugin/demo/pid 空，实际 %+v", dir)
	}
	page := lookupPage(menus, "plugin/demo/items")
	if page.Type != "menu" || page.Path != "plugin/demo/items" || page.Pid != "plugin/demo" ||
		page.Component != EntityRenderComponent || page.Title != "条目" {
		t.Errorf("实体页行必须是公式（path/component/pid/标题），实际 %+v", page)
	}
	for _, action := range []string{"read", "create", "update", "delete", "sort"} {
		node := lookupPage(menus, "plugin/demo/items/"+action)
		if node.Type != "node" || node.Pid != "plugin/demo/items" {
			t.Errorf("节点 %s 应为 node 且 pid 指向实体页，实际 %+v", action, node)
		}
	}

	// ② 声明了同一公式的行：以声明为准（权重保留），且不重复写
	declared := []manifest.Menu{{
		Title: "条目", Type: "menu", Name: "plugin/demo/items", Path: "plugin/demo/items",
		Component: EntityRenderComponent, Pid: "plugin/demo", Weigh: 110, Icon: "lucide-list",
	}}
	menus, err = BuildInstallMenus("demo", makeManifest(declared, []manifest.Entity{dragEntity}))
	if err != nil {
		t.Fatalf("合规声明应通过: %v", err)
	}
	if len(menus) != len(want) {
		t.Errorf("声明同一公式的行不应重复写（应仍为 %d 条），实际 %d: %v", len(want), len(menus), menus)
	}
	byName(menus)
	if kept := lookupPage(menus, "plugin/demo/items"); kept.Weigh != 110 || kept.Icon != "lucide-list" {
		t.Errorf("同名声明行的标题/权重/图标应以作者声明为准，实际 %+v", kept)
	}

	// ③ 不符公式 ⇒ 拒装（逐条：path / component / pid / 节点动作 / sort 当 drag_sort=false）
	badCases := []struct {
		name    string
		menus   []manifest.Menu
		entity  manifest.Entity
		wantErr string
	}{
		{
			name: "实体页 path 不符",
			menus: []manifest.Menu{{Title: "条目", Type: "menu", Name: "plugin/demo/items", Path: "plugin/demo/other",
				Component: EntityRenderComponent, Pid: "plugin/demo"}},
			entity:  dragEntity,
			wantErr: "path 必须逐字等于",
		},
		{
			name: "实体页 component 不符",
			menus: []manifest.Menu{{Title: "条目", Type: "menu", Name: "plugin/demo/items", Path: "plugin/demo/items",
				Component: "/src/views/admin/plugin/demo/items.vue", Pid: "plugin/demo"}},
			entity:  dragEntity,
			wantErr: "component 必须逐字等于",
		},
		{
			name: "实体页 pid 不符",
			menus: []manifest.Menu{{Title: "条目", Type: "menu", Name: "plugin/demo/items", Path: "plugin/demo/items",
				Component: EntityRenderComponent}},
			entity:  dragEntity,
			wantErr: "pid 必须是插件分组",
		},
		{
			name:    "节点动作不在闭集",
			menus:   []manifest.Menu{{Title: "列表", Type: "node", Name: "plugin/demo/items/list", Pid: "plugin/demo/items"}},
			entity:  dragEntity,
			wantErr: "不在公式闭集内",
		},
		{
			name:    "sort 节点当 drag_sort=false",
			menus:   []manifest.Menu{{Title: "排序", Type: "node", Name: "plugin/demo/items/sort", Pid: "plugin/demo/items"}},
			entity:  plainEntity,
			wantErr: "不在公式闭集内",
		},
	}
	for _, item := range badCases {
		if _, err := BuildInstallMenus("demo", makeManifest(item.menus, []manifest.Entity{item.entity})); err == nil {
			t.Errorf("用例 %q：不符公式应拒装", item.name)
		} else if !strings.Contains(err.Error(), item.wantErr) {
			t.Errorf("用例 %q：错误应点名 %q，实际 %v", item.name, item.wantErr, err)
		}
	}

	// ④ 无实体的 code 插件：不代写任何行（公式只服务 L2 实体页）
	menus, err = BuildInstallMenus("demo", makeManifest(nil, nil))
	if err != nil || len(menus) != 0 {
		t.Errorf("无实体不应代写菜单/节点，实际 %d 条 err=%v", len(menus), err)
	}
}

// lookupPage 取清单里的某一行（测试断言用；缺行即 Fatal）
func lookupPage(menus []manifest.Menu, name string) manifest.Menu {
	for _, menu := range menus {
		if menu.Name == name {
			return menu
		}
	}
	return manifest.Menu{}
}

// TestInstallPlanRenderDDLReads dry-run ⑤ 段的 DDL 读数渲染（回归：**nil 计划 + error 不得空指针 panic**）：
//   - 计划未生成（Err 非空、Plan nil：spec 非法 / trgm 不可用 / 自省失败等都在 Build 里直接返回 nil 计划）
//     ⇒ 打 ✗ 后跳过，且不得声称「0 条：结构已与 spec 一致」；
//   - 拒绝项与已允许的变更并存（Err 非空、Plan 非空）⇒ 逐条列出计划读数并标明整批不执行；
//   - ddl_owner=migrations（Note 非空、Plan nil）⇒ 只打说明。
func TestInstallPlanRenderDDLReads(t *testing.T) {
	t.Parallel()
	newPlan := func(reads []EntityDDL) *InstallPlan {
		return &InstallPlan{
			Name:   "demo",
			Source: "plugins/demo",
			Manifest: &manifest.Manifest{
				Name: "demo", Title: "示例插件", Version: "1.0.0", Kind: "mixed", HostAPI: ">=1 <2",
				Entities: []manifest.Entity{{Entity: "items", TitleZh: "条目", TitleEn: "Items"}},
			},
			DDL: reads,
		}
	}
	render := func(plan *InstallPlan) string {
		t.Helper()
		buf := &bytes.Buffer{}
		plan.Render(buf) // 回归点：此前「nil 计划 + 非空 Err」在这里空指针 panic
		return buf.String()
	}

	// ① 计划未生成：✗ 必到、不得误报「0 条一致」
	out := render(newPlan([]EntityDDL{{
		Entity: "items", Table: "plugin_demo_items", DDLOwner: "engine",
		Err: errors.New(`实体 items 的 spec 非法: 字段 title（type=int）的 search.op="ILIKE" 是模糊匹配类操作符`),
	}}))
	if !strings.Contains(out, "✗ 实体 items 的 spec 非法") {
		t.Errorf("预演应逐条列出拒绝理由（✗），实际:\n%s", out)
	}
	if strings.Contains(out, "（0 条") {
		t.Errorf("计划未生成时不得声称「结构已与 spec 一致」，实际:\n%s", out)
	}

	// ② 拒绝项 + 已允许变更并存：计划逐条列出 + 标明整批不执行
	out = render(newPlan([]EntityDDL{{
		Entity: "items", Table: "plugin_demo_items", DDLOwner: "engine", Existing: true,
		Err: errors.New("字段 title 的 varchar 长度由 128 收窄到 32：拒绝（请提供迁移文件并升为 mixed/L1）"),
		Plan: &ddlplan.Plan{
			Plugin: "demo", Entity: "items", Table: "plugin_demo_items", DDLOwner: "engine", Existing: true,
			Statements: []ddlplan.Statement{{Kind: "add_column",
				Text: `ALTER TABLE "plugin_demo_items" ADD COLUMN IF NOT EXISTS "remark" VARCHAR(64) DEFAULT ''`}},
		},
	}}))
	if !strings.Contains(out, "整批不执行") || !strings.Contains(out, `ADD COLUMN IF NOT EXISTS "remark"`) {
		t.Errorf("有拒绝理由时应列出计划读数并标明整批不执行，实际:\n%s", out)
	}

	// ③ ddl_owner=migrations：只打说明，不发 DDL、不声称一致
	out = render(newPlan([]EntityDDL{{
		Entity: "items", Table: "plugin_demo_items", DDLOwner: "migrations", Note: migrationsOwnedNote,
	}}))
	if !strings.Contains(out, "ddl_owner=migrations") || strings.Contains(out, "（0 条") {
		t.Errorf("migrations 自管实体应只打说明，实际:\n%s", out)
	}
}

// TestScaffoldPlanAndExport 脚手架 → 预演（落位映射）→ 打包（含 files.sha256 校验）闭环
func TestScaffoldPlanAndExport(t *testing.T) {
	root := t.TempDir()
	// 目标语言目录须存在（落位映射会校验 web/src/lang/<语言>）
	for _, lang := range []string{"zh-cn", "en"} {
		if err := os.MkdirAll(filepath.Join(root, "web", "src", "lang", lang), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env := &Env{Root: root, Out: &bytes.Buffer{}}

	if err := Scaffold(env, "demo", ScaffoldOptions{Kind: "mixed", Title: "示例插件"}); err != nil {
		t.Fatalf("脚手架应成功: %v", err)
	}
	dir := env.Abs(PluginDir("demo"))
	if _, _, err := manifest.Load(dir, manifest.Options{Prefix: env.Prefix, HostVersion: env.HostVersion}); err != nil {
		t.Fatalf("骨架必须通过 manifest 校验: %v", err)
	}

	// 落位映射：web/views/** 与 web/lang/*.yaml 各归其位
	files, err := BuildPlanFiles(env, dir, "demo")
	if err != nil {
		t.Fatalf("落位映射应成功: %v", err)
	}
	targets := map[string]bool{}
	for _, file := range files {
		targets[file.Target] = true
	}
	for _, want := range []string{
		"plugins/demo/plugin.yaml",
		"plugins/demo/plugin.go",
		"web/src/views/admin/plugin/demo/index.vue",
		"web/src/lang/zh-cn/plugin/demo.yaml",
		"web/src/lang/en/plugin/demo.yaml",
	} {
		if !targets[want] {
			t.Errorf("落位目标缺 %q（实际 %v）", want, targets)
		}
	}

	// 打包：<名>-<版本>.zip（含 files.sha256），读回包逐项校验
	target, err := Export(env, "demo", "")
	if err != nil {
		t.Fatalf("打包应成功: %v", err)
	}
	if filepath.Base(target) != "demo-1.0.0.zip" {
		t.Errorf("包名应为 demo-1.0.0.zip: %s", target)
	}
	if !strings.Contains(target, filepath.FromSlash(StoreDir)) {
		t.Errorf("默认输出应落 %s: %s", StoreDir, target)
	}

	packed := readZipEntries(t, target)
	manifestRaw, ok := packed[PackManifestFile]
	if !ok {
		t.Fatalf("包内应有 %s（实际条目：%v）", PackManifestFile, keysOf(packed))
	}
	listed, err := ParsePackManifest(manifestRaw)
	if err != nil {
		t.Fatalf("解析包内 %s 失败: %v", PackManifestFile, err)
	}
	if len(listed) == 0 {
		t.Fatal("清单不应为空")
	}
	// 清单逐项与包内条目比对（sha256 一致），且包内不得有清单之外的条目
	for _, file := range listed {
		content, ok := packed[file.Path]
		if !ok {
			t.Errorf("清单列出的 %q 不在包内（条目：%v）", file.Path, keysOf(packed))
			continue
		}
		if sum := sha256Hex(content); sum != file.SHA256 {
			t.Errorf("包内 %q 的 sha256 与清单不符：%s != %s", file.Path, sum, file.SHA256)
		}
	}
	if want := len(listed) + 1; len(packed) != want { // 清单自身不列入清单（本例无签名文件 ⇒ 条目数 = 清单 + 1）
		t.Errorf("包内条目数应为 %d（清单 %d + %s），实际 %d", want, len(listed), PackManifestFile, len(packed))
	}
	// 源目录与清单一致（供 install --dry-run 的完整性校验复用同一判定）
	if err := VerifyPackFiles(dir, listed, DefaultSignatureFile); err != nil {
		t.Fatalf("清单应逐项可验: %v", err)
	}

	// 清单之外夹带文件 ⇒ 拒
	mustWrite(t, filepath.Join(dir, "model", "evil.go"), "package model\n")
	if err := VerifyPackFiles(dir, listed, DefaultSignatureFile); err == nil {
		t.Error("清单之外的文件应被拒（防夹带随目录复制进编译面）")
	}
}

// readZipEntries 读回 zip 的全部条目（相对路径 → 内容）
func readZipEntries(t *testing.T, path string) map[string][]byte {
	t.Helper()
	reader, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("打开包 %s 失败: %v", path, err)
	}
	defer reader.Close()

	out := make(map[string][]byte, len(reader.File))
	for _, file := range reader.File {
		handle, err := file.Open()
		if err != nil {
			t.Fatalf("读取包内 %s 失败: %v", file.Name, err)
		}
		content, err := io.ReadAll(handle)
		_ = handle.Close()
		if err != nil {
			t.Fatalf("读取包内 %s 失败: %v", file.Name, err)
		}
		out[file.Name] = content
	}
	return out
}

// keysOf 条目名（错误信息用，稳定排序）
func keysOf(entries map[string][]byte) []string {
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// sha256Hex 内容摘要（与 fileSHA256 同口径）
func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// mustWrite 写测试文件（必要时建目录）
func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestBuildPlanFilesRejectsWebOutsideClosedSet 落位闭集：web/ 下只收 views/** 与 lang/<语言>.yaml，
// 其它路径（含试图落到 web/src/** 的形态）一律拒装——这是「插件写不到核心文件」的第一道保证。
func TestBuildPlanFilesRejectsWebOutsideClosedSet(t *testing.T) {
	root := t.TempDir()
	env := &Env{Root: root, Out: &bytes.Buffer{}}
	dir := filepath.Join(root, PluginDir("demo"))
	mustWrite(t, filepath.Join(dir, "web", "src", "x.ts"), "x")

	_, err := BuildPlanFiles(env, dir, "demo")
	if err == nil {
		t.Fatal("web/ 下非闭集路径应拒装")
	}
	if !strings.Contains(err.Error(), "落位规则闭集") {
		t.Fatalf("错误应说明落位规则闭集: %v", err)
	}
}

// TestBuildPlanFilesRejectsMissingLangDir 语言包副本的目标语言目录必须已存在（否则副本会落到宿主
// 语言体系之外，出现「复制了但没人认识」的文件）：缺失即拒装。
func TestBuildPlanFilesRejectsMissingLangDir(t *testing.T) {
	root := t.TempDir()
	env := &Env{Root: root, Out: &bytes.Buffer{}}
	dir := filepath.Join(root, PluginDir("demo"))
	mustWrite(t, filepath.Join(dir, "web", "lang", "plugin.yaml"), "k: v")

	_, err := BuildPlanFiles(env, dir, "demo")
	if err == nil {
		t.Fatal("语言目录缺失应拒装")
	}
	if !strings.Contains(err.Error(), "语言目录缺失") {
		t.Fatalf("错误应说明语言目录缺失: %v", err)
	}
}
