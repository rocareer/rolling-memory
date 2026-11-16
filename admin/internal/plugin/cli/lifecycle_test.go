package cli

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/internal/model"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"

	"gorm.io/gorm"
)

// 生命周期与门禁的用例（四态状态机 /硬门禁与 --keep-files /dry-run 预演）。
//
// 两类口径：
//   - **不连库的 dry-run 用例**：install --dry-run 的全量预演与「零副作用」（不落副本、不改接线文件、不留档）；
//   - **库级用例（PLUGIN_DB=1 门控，复用 install_db_test.go 的一次性工作根与自建自清基建）**：
//     Enable/Disable 的四态与菜单/节点 status 同步、审计计数（含被拒/幂等分支）、启停的打印式 dry-run、
//     uninstall 的「先禁用后卸载」门禁与 --keep-files 的「回到出厂态」语义。
//
// **PLUGIN_DB 纱门口径（F18）**：无 PLUGIN_DB 时库级用例一律 t.Skip，命令仍返回 0 ⇒「绿」可能什么都没跑。
// 验收口径 = `PLUGIN_DB=1 go test -count=1 -timeout=180s ./internal/plugin/...` 的输出里**不得有 SKIP**
// （`go test -v … | grep -c 'SKIP'` 与 `-json` 的 Action=skip 计数都应为 0）；出现任何跳过项
// （如「仓库无 config/*.yaml」「插件元数据表不存在」）都代表该面未被验证，不得当作通过。
//
// 运行：PLUGIN_DB=1 go test -count=1 -timeout=180s ./internal/plugin/cli/

// TestPluginInstallDryRunNoSideEffects install --dry-run 必须输出全量预演（①–⑦ 段），
// 且不落任何文件、不改接线文件、不留档（「A 的每一步都要能在 dry-run 中预演」）。
//
// 用 kind=code 的骨架：**无 L2 实体 ⇒ 结构预演不需要库**（引擎自管实体的 DDL 预演要读实际表结构做
// 差异基线，库不可用时 fail-closed；见 TestPluginInstallL2DDLDB 的库级覆盖）——本用例因此仍不设
// PLUGIN_DB 门。桥接两面的口径：dry-run 的库依赖只由「有没有引擎自管实体」决定，与插件形态无关。
func TestPluginInstallDryRunNoSideEffects(t *testing.T) {
	root := t.TempDir()
	// 落位映射会校验 web/src/lang/<语言> 存在（同 TestScaffoldPlanAndExport 的前置）
	for _, lang := range []string{"zh-cn", "en"} {
		mustWrite(t, filepath.Join(root, "web", "src", "lang", lang, ".keep"), "")
	}
	buf := &bytes.Buffer{}
	env := &Env{Root: root, Out: buf}
	name := "dryrun-demo"

	if err := Scaffold(env, name, ScaffoldOptions{Kind: "code", Title: "预演用例插件"}); err != nil {
		t.Fatalf("脚手架应成功: %v", err)
	}
	buf.Reset()

	// 目录源 + 无 requires + 无 L2 实体 ⇒ dry-run 全程不碰库（这也是本用例不设 PLUGIN_DB 门的原因）
	if err := Install(context.Background(), env, PluginDir(name), InstallOptions{DryRun: true, Operator: "dryrun"}); err != nil {
		t.Fatalf("--dry-run 应成功（不落盘、不写库、不改接线文件）: %v", err)
	}

	text := buf.String()
	for _, want := range []string{
		"插件安装预演：" + name,
		"① manifest 摘要",
		"② 文件清单",
		"③ 插件迁移",
		"④ 将写入的菜单/节点",
		"⑤ 将执行的 DDL",
		"⑥ 将改写的宿主文件",
		"⑦ 其余动作与生效方式",
		"--dry-run：以上为全量预演",
		path.Join(ViewTarget(name), "index.vue"), // 落位副本目标必须在文件清单里
		LangTarget("zh-cn", name),
		ImportPath(name), // 将写入的注册行（⑥ 段）
	} {
		if !strings.Contains(text, want) {
			t.Errorf("--dry-run 输出应含 %q\n--- 实际输出 ---\n%s", want, text)
		}
	}

	// 零副作用：落位副本、语言包副本、接线文件一个都不许出现；包留档目录也不许有产物
	for _, target := range []string{
		path.Join(ViewTarget(name), "index.vue"),
		LangTarget("zh-cn", name),
		LangTarget("en", name),
		RegistryFile,
	} {
		if _, err := os.Stat(env.Abs(target)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("--dry-run 不应写出 %s（err=%v）", target, err)
		}
	}
	matches, err := filepath.Glob(env.Abs(path.Join(StoreDir, name+"-*")))
	if err != nil {
		t.Fatalf("扫描包留档目录失败: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("--dry-run 不应产生包留档: %v", matches)
	}
}

// TestBaselineSummaryLineVersionTableNew ③b 摘要行的「版本表新建」必须由读数派生（F3 回归）：
//   - 现场无版本表 ⇒ 摘要说 true——本次安装的 ⑥ 一定会建它（申报 adopt 的插件必有迁移目录，运行器
//     构造即 ensureVersionTable），且与同一读数渲染的 ③b 段「版本表 <名>：不存在」同源不矛盾
//     （早先调用点硬编码 false：两行在同一份输出里自相矛盾）；
//   - 既有版本表（复装/归位）⇒ false；
//   - 未声明 adopt / 空读数 ⇒ 常规路径文案（本机制不介入，不出现「版本表新建」）。
//
// 不连库：摘要行与 ③b 段渲染都只吃 BaselineRead（读数由 ③b 前置换来，见 install.go 的 previewBaseline）。
func TestBaselineSummaryLineVersionTableNew(t *testing.T) {
	t.Parallel()
	plan := &InstallPlan{Name: "summary-demo"}
	read := &BaselineRead{
		Declared: true, Through: 2, Table: "plugin_summary-demo_migrations",
		State: migrationState{}, Mode: baselineModeAdopt,
		Probes: []BaselineProbe{
			{Version: 1, Kind: "table", Name: "eval_sets", Physical: "eval_sets", Hit: true},
			{Version: 2, Kind: "none", Hit: true, Physical: "（无对象）"},
		},
		ProbesTotal: 1, ProbesHit: 1,
	}
	plan.Baseline = read

	// ① 现场无版本表：摘要说「新建=true」
	line := baselineSummaryLine(read)
	for _, want := range []string{"mode=adopt", "through=2", "探针=1/1", "版本表新建=true"} {
		if !strings.Contains(line, want) {
			t.Errorf("现场无版本表时摘要应含 %q，实际 %q", want, line)
		}
	}
	buf := &bytes.Buffer{}
	plan.renderBaseline(buf)
	if text := buf.String(); !strings.Contains(text, "版本表 plugin_summary-demo_migrations：不存在") {
		t.Errorf("③b 段应按读数打印「不存在」（与摘要同源）\n--- 实际输出 ---\n%s", text)
	}

	// ② 既有版本表（复装/归位）：摘要说「新建=false」，③b 段说「已存在」——两行仍同源
	read.State = migrationState{Existed: true, Version: 1}
	if line = baselineSummaryLine(read); !strings.Contains(line, "版本表新建=false") {
		t.Errorf("既有版本表时摘要应说「版本表新建=false」，实际 %q", line)
	}
	buf.Reset()
	plan.renderBaseline(buf)
	if text := buf.String(); !strings.Contains(text, "版本表 plugin_summary-demo_migrations：已存在") {
		t.Errorf("③b 段应按读数打印「已存在」\n--- 实际输出 ---\n%s", text)
	}

	// ③ 未声明 adopt / 空读数：常规文案
	for _, empty := range []*BaselineRead{nil, {Declared: false}} {
		if line = baselineSummaryLine(empty); !strings.Contains(line, "未声明 adopt") {
			t.Errorf("未声明 adopt 应走常规文案，实际 %q", line)
		}
	}
}

// TestPluginStatusMachineDB 四态状态机：每步的台账状态、菜单/节点 status 同步与审计计数，
// 以及非法迁移（installed 停用 / failed 启用 / 未装启用）的拒绝面。
func TestPluginStatusMachineDB(t *testing.T) {
	env, db := installDBSetup(t, "5")
	ctx := context.Background()
	name := pluginDBName("5")

	if err := Scaffold(env, name, ScaffoldOptions{Kind: "code", Title: "状态机用例插件"}); err != nil {
		t.Fatalf("脚手架应成功: %v", err)
	}
	if err := Install(ctx, env, name, InstallOptions{Operator: "pdbtest"}); err != nil {
		t.Fatalf("安装应成功: %v", err)
	}

	// installed：菜单/节点必须隐藏（status=0，隐藏至 enable）
	assertLedgerStatus(t, ctx, db, name, host.StatusInstalled)
	assertRuleStatus(t, ctx, db, name, 0)

	// installed 态停用被拒（只有 enabled 才能停用）
	if err := Disable(ctx, env, name, "pdbtest"); err == nil || !strings.Contains(err.Error(), "只有 enabled 才能停用") {
		t.Errorf("installed 态停用应被拒，实际 %v", err)
	}

	// installed → enabled：台账 + 菜单/节点 status=1 + 一条 enable/success 审计
	if err := Enable(ctx, env, name, "pdbtest"); err != nil {
		t.Fatalf("启用应成功: %v", err)
	}
	assertLedgerStatus(t, ctx, db, name, host.StatusEnabled)
	assertRuleStatus(t, ctx, db, name, 1)
	if count := auditCount(t, db, name, ActionEnable, AuditSuccess); count != 1 {
		t.Errorf("应有 1 条 enable/success 审计，实际 %d", count)
	}

	// 重复 enable：已是目标态 ⇒ 无变化、不新写审计
	if err := Enable(ctx, env, name, "pdbtest"); err != nil {
		t.Fatalf("重复启用应无错（已是目标态）: %v", err)
	}
	if count := auditCount(t, db, name, ActionEnable, AuditSuccess); count != 1 {
		t.Errorf("重复启用不应新写审计，实际 %d", count)
	}

	// enabled → disabled：菜单/节点随状态回到 0（还原宿主只有这一条路径）+ disable/success 审计
	if err := Disable(ctx, env, name, "pdbtest"); err != nil {
		t.Fatalf("停用应成功: %v", err)
	}
	assertLedgerStatus(t, ctx, db, name, host.StatusDisabled)
	assertRuleStatus(t, ctx, db, name, 0)
	if count := auditCount(t, db, name, ActionDisable, AuditSuccess); count != 1 {
		t.Errorf("应有 1 条 disable/success 审计，实际 %d", count)
	}
	if err := Disable(ctx, env, name, "pdbtest"); err != nil {
		t.Errorf("重复停用应无错（已是目标态）: %v", err)
	}
	if count := auditCount(t, db, name, ActionDisable, AuditSuccess); count != 1 {
		t.Errorf("重复停用不应新写审计，实际 %d", count)
	}

	// failed（半装）：唯一允许的下一步是 `uninstall --force` ⇒ 启用必须被拒
	if _, err := gorm.G[host.Plugin](db).Where("name = ?", name).Update(ctx, "status", host.StatusFailed); err != nil {
		t.Fatalf("置 failed 态失败: %v", err)
	}
	if err := Enable(ctx, env, name, "pdbtest"); err == nil || !strings.Contains(err.Error(), "failed") {
		t.Errorf("failed 态启用应被拒（只允许 uninstall --force），实际 %v", err)
	}

	// 未装的名字：报「未安装」而不是静默成功
	if err := Enable(ctx, env, name+"-missing", "pdbtest"); err == nil || !strings.Contains(err.Error(), "未安装") {
		t.Errorf("台账无该插件时启用应报未安装，实际 %v", err)
	}

	// 收尾：failed 态走 --force 清理（用例自清）
	if err := Uninstall(ctx, env, name, UninstallOptions{Force: true, Operator: "pdbtest"}); err != nil {
		t.Fatalf("failed 态 --force 清理应成功: %v", err)
	}
	assertZeroResidue(t, env, db, name)
}

// TestPluginUninstallGateKeepFilesDB 覆盖验收 A 的「停 → 卸」两步与 --keep-files 的出厂态语义：
//   - enabled 态卸载被硬门禁拦下（含 rejected 留痕）；--dry-run 的预演只读、不写审计；
//   - --purge 缺 --yes 被二次确认拦下；
//   - disable 后卸载预演打印将删/将保留清单且不动任何行与文件；
//   - --keep-files 卸载：DB 侧与注册行清空、源码与落位副本留在仓里（= 出厂态）。
func TestPluginUninstallGateKeepFilesDB(t *testing.T) {
	env, db := installDBSetup(t, "6")
	ctx := context.Background()
	name := pluginDBName("6")
	buf, ok := env.Out.(*bytes.Buffer)
	if !ok {
		t.Fatal("测试环境应以 *bytes.Buffer 作为输出目标")
	}

	if err := Scaffold(env, name, ScaffoldOptions{Kind: "mixed", Title: "卸载门禁用例插件"}); err != nil {
		t.Fatalf("脚手架应成功: %v", err)
	}
	if err := Install(ctx, env, name, InstallOptions{Operator: "pdbtest"}); err != nil {
		t.Fatalf("安装应成功: %v", err)
	}
	if err := Enable(ctx, env, name, "pdbtest"); err != nil {
		t.Fatalf("启用应成功: %v", err)
	}

	// enabled：卸载（含 --dry-run 预演）一律被门禁拦下——先禁用后卸载是硬门禁
	dryErr := Uninstall(ctx, env, name, UninstallOptions{DryRun: true, Operator: "pdbtest"})
	if dryErr == nil || !strings.Contains(dryErr.Error(), "处于 enabled") {
		t.Errorf("enabled 态的卸载预演也应被拦下，实际 %v", dryErr)
	}
	if count := auditCount(t, db, name, ActionUninstall, AuditRejected); count != 0 {
		t.Errorf("--dry-run 是只读预演，不应写 rejected 审计，实际 %d", count)
	}
	refuseErr := Uninstall(ctx, env, name, UninstallOptions{Operator: "pdbtest"})
	if refuseErr == nil || !strings.Contains(refuseErr.Error(), "处于 enabled") {
		t.Errorf("enabled 态卸载应被拒，实际 %v", refuseErr)
	}
	if count := auditCount(t, db, name, ActionUninstall, AuditRejected); count != 1 {
		t.Errorf("被拒的卸载应留 rejected 审计（含被拒原因），实际 %d", count)
	}
	assertLedgerStatus(t, ctx, db, name, host.StatusEnabled)
	if err := assertPlaced(env, name); err != nil {
		t.Errorf("被拒的卸载不应动文件: %v", err)
	}

	// 停用（--purge 的二次确认在门禁之后的路径上校验）
	if err := Disable(ctx, env, name, "pdbtest"); err != nil {
		t.Fatalf("停用应成功: %v", err)
	}
	if err := Uninstall(ctx, env, name, UninstallOptions{Purge: true, Operator: "pdbtest"}); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("--purge 缺 --yes 应被拒（二次确认），实际 %v", err)
	}

	// --dry-run 卸载预演：打印将删/将保留清单，且不动任何行与文件
	buf.Reset()
	if err := Uninstall(ctx, env, name, UninstallOptions{DryRun: true, Operator: "pdbtest"}); err != nil {
		t.Fatalf("disabled 态的卸载预演应成功: %v", err)
	}
	text := buf.String()
	for _, want := range []string{
		"插件卸载预演：" + name,
		"a) 插件迁移回滚（仅 --purge）",
		"b) 删落位物",
		"c) 删 DB 侧",
		"d) 撤接线注册行",
		"e) 审计",
		"--dry-run：以上为将删/将保留清单，未动任何文件与行",
		"复装不是还原",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("卸载预演输出应含 %q\n--- 实际输出 ---\n%s", want, text)
		}
	}
	assertLedgerStatus(t, ctx, db, name, host.StatusDisabled)
	if err := assertPlaced(env, name); err != nil {
		t.Errorf("卸载预演不应动文件: %v", err)
	}
	if count, err := gorm.G[model.AdminRule](db).Where("plugin = ?", name).Count(ctx, "*"); err != nil || count != 9 {
		t.Errorf("卸载预演不应动归属行（应仍为 9：声明 3 条 +公式代写实体页 1 条 + 节点 5 条），实际 %d err=%v", count, err)
	}

	// --keep-files 卸载：DB 侧与注册行清空、文件留在仓里（出厂态）
	if err := Uninstall(ctx, env, name, UninstallOptions{KeepFiles: true, Operator: "pdbtest"}); err != nil {
		t.Fatalf("--keep-files 卸载应成功: %v", err)
	}
	assertNoDBSideResidue(t, env, db, name)
	if err := assertPlaced(env, name); err != nil {
		t.Errorf("--keep-files 后源码与落位副本应保留（出厂态）: %v", err)
	}
	if count := auditCount(t, db, name, ActionUninstall, AuditSuccess); count != 1 {
		t.Errorf("应有 1 条 uninstall/success 审计，实际 %d", count)
	}
}

// TestUninstallPurgeAdoptedManifestGate 放行清单门禁（F4 回归，不连库）：
// 三重显式（--purge --yes --purge-adopted）买的是「执行前打印将回滚/将 drop 的对象面」，而对象面只存在于
// **已落位 manifest 的 adopt 段**（renderAdoptedPurgeNotice 逐条打印探针声明的对象）⇒ 清单读不到就不放行
// （fail-closed）。早先落位 manifest 读不到（loadPlacedManifest ⇒ nil）时整段对象面被静默跳过，purge 照常
// 执行——放行的是一次「删核心链建的表与数据」的回滚，而人看不到将删什么。
//
// 覆盖：① 落位目录不在 ⇒ 拒（文案点名恢复路径）；② manifest 在但无 adopt 段 ⇒ 拒（对象面无从声明）；
// ③ 补上 adopt 段 ⇒ 放行；④ 不在三重显式的路径上（未 adopt 装入 / 缺第三重 / 缺 --purge）⇒ 本门禁不介入。
func TestUninstallPurgeAdoptedManifestGate(t *testing.T) {
	t.Parallel()
	triple := UninstallOptions{Purge: true, Yes: true, PurgeAdopted: true}
	root := t.TempDir()
	env := &Env{Root: root, Out: &bytes.Buffer{}}
	name := "purge-gate-demo"
	manifestFile := path.Join(PluginDir(name), "plugin.yaml")

	// ① 落位目录不在（loadPlacedManifest ⇒ nil）：拒 + 文案给恢复路径
	err := checkPurgeAdoptedManifest(name, 2, loadPlacedManifest(env, name), triple)
	if err == nil {
		t.Fatal("放行清单读不到时必须拒绝放行（无清单不放行，fail-closed）")
	}
	for _, want := range []string{"放行清单读不到", "adopted_through=2", manifestFile, "恢复路径", "aigo plugin uninstall " + name} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("拒绝文案应含 %q，实际: %v", want, err)
		}
	}

	// ② manifest 在、但没有 adopt 段（对象面无从声明）：同样拒
	if err := Scaffold(env, name, ScaffoldOptions{Kind: "code", Title: "放行清单门禁用例"}); err != nil {
		t.Fatalf("脚手架应成功: %v", err)
	}
	placed := loadPlacedManifest(env, name)
	if placed == nil || placed.Adopt != nil {
		t.Fatalf("骨架应落出可读的 manifest 且不带 adopt 段: %+v", placed)
	}
	if err := checkPurgeAdoptedManifest(name, 2, placed, triple); err == nil || !strings.Contains(err.Error(), "放行清单读不到") {
		t.Errorf("无 adopt 段时应拒（对象面无从声明），实际 %v", err)
	}

	// ③ 补上 adopt 段（对象面可声明）：放行
	content, err := os.ReadFile(env.Abs(manifestFile))
	if err != nil {
		t.Fatal(err)
	}
	adopt := "\nadopt:\n  through: 1\n  probes:\n    - version: 1\n      kind: table\n      name: purge_gate_legacy\n"
	if err := os.WriteFile(env.Abs(manifestFile), append(content, []byte(adopt)...), 0o644); err != nil {
		t.Fatal(err)
	}
	placed = loadPlacedManifest(env, name)
	if placed == nil || placed.Adopt == nil {
		t.Fatalf("补上 adopt 段后 manifest 应可读且带 adopt: %+v", placed)
	}
	if err := checkPurgeAdoptedManifest(name, 2, placed, triple); err != nil {
		t.Errorf("清单可读（带 adopt 段）时应放行，实际 %v", err)
	}

	// ④ 不在三重显式的路径上：本门禁不介入（未 adopt 装入的无效旗标与缺项由上游门禁与提示管）
	for _, c := range []struct {
		label          string
		adoptedThrough int
		m              *manifest.Manifest
		opts           UninstallOptions
	}{
		{"未 adopt 装入（台账 adopted_through=0）", 0, nil, triple},
		{"缺 --purge-adopted（前两重由上游门禁管）", 2, nil, UninstallOptions{Purge: true, Yes: true}},
		{"--purge-adopted 未配 --purge（无效旗标，只提示）", 2, nil, UninstallOptions{PurgeAdopted: true}},
	} {
		if err := checkPurgeAdoptedManifest(name, c.adoptedThrough, c.m, c.opts); err != nil {
			t.Errorf("%s：本门禁不应介入，实际 %v", c.label, err)
		}
	}
}

// TestPluginStatusDryRunDB 启停的打印式 dry-run（「A 的每一步都要能在 dry-run 中预演」）：
//   - 预演走**同一套状态机**（该拒的照实拒：installed 态停用仍被拦）；
//   - 预演打印将改写的读数（台账 from→to、归属行条数与目标 status、审计将记的一条）；
//   - 预演零副作用：台账 / 菜单节点 status / 审计计数一行都不动。
func TestPluginStatusDryRunDB(t *testing.T) {
	env, db := installDBSetup(t, "17")
	ctx := context.Background()
	name := pluginDBName("17")
	buf, ok := env.Out.(*bytes.Buffer)
	if !ok {
		t.Fatal("测试环境应以 *bytes.Buffer 作为输出目标")
	}

	if err := Scaffold(env, name, ScaffoldOptions{Kind: "code", Title: "启停预演用例插件"}); err != nil {
		t.Fatalf("脚手架应成功: %v", err)
	}
	if err := Install(ctx, env, name, InstallOptions{Operator: "pdbtest"}); err != nil {
		t.Fatalf("安装应成功: %v", err)
	}

	// ① installed 态停用：预演与执行同样被状态机拦下（不因为 --dry-run 而放行）
	if err := DisableWith(ctx, env, name, StatusOptions{DryRun: true, Operator: "pdbtest"}); err == nil ||
		!strings.Contains(err.Error(), "只有 enabled 才能停用") {
		t.Errorf("installed 态停用预演应被同一套状态机拒，实际 %v", err)
	}

	// ② installed → enabled 的预演：打印读数，零副作用
	buf.Reset()
	if err := EnableWith(ctx, env, name, StatusOptions{DryRun: true, Operator: "pdbtest"}); err != nil {
		t.Fatalf("启用预演应成功: %v", err)
	}
	text := buf.String()
	for _, want := range []string{
		"启停预演（--dry-run）",
		"台账 admin_plugins.status：installed → enabled",
		"归属菜单/节点（admin_rules.plugin=" + name + "）3 条：status → 1",
		"审计：admin_plugin_actions 一条 enable/success（署名 pdbtest）",
		"--dry-run：以上为启停预演，未写任何行、未写审计",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("启停预演输出应含 %q\n--- 实际输出 ---\n%s", want, text)
		}
	}
	assertLedgerStatus(t, ctx, db, name, host.StatusInstalled) // 台账未动
	assertRuleStatus(t, ctx, db, name, 0)                      // 菜单/节点未动
	if count := auditCount(t, db, name, ActionEnable, AuditSuccess); count != 0 {
		t.Errorf("预演不得写审计，实际 enable/success = %d", count)
	}

	// ③ 真实 enable 后做 disable 预演：读数换成 enabled → disabled，且仍然零副作用
	if err := Enable(ctx, env, name, "pdbtest"); err != nil {
		t.Fatalf("启用应成功: %v", err)
	}
	buf.Reset()
	if err := DisableWith(ctx, env, name, StatusOptions{DryRun: true, Operator: "pdbtest"}); err != nil {
		t.Fatalf("停用预演应成功: %v", err)
	}
	if text = buf.String(); !strings.Contains(text, "台账 admin_plugins.status：enabled → disabled") {
		t.Errorf("停用预演应打印 enabled → disabled，实际:\n%s", text)
	}
	assertLedgerStatus(t, ctx, db, name, host.StatusEnabled)
	assertRuleStatus(t, ctx, db, name, 1)
	if count := auditCount(t, db, name, ActionDisable, AuditSuccess); count != 0 {
		t.Errorf("预演不得写审计，实际 disable/success = %d", count)
	}

	// 收尾：真实停用 + --keep-files 卸载（用例自清；预演不动现场，故清理照常）
	if err := Disable(ctx, env, name, "pdbtest"); err != nil {
		t.Fatalf("停用应成功: %v", err)
	}
	if err := Uninstall(ctx, env, name, UninstallOptions{KeepFiles: true, Operator: "pdbtest"}); err != nil {
		t.Fatalf("--keep-files 卸载应成功: %v", err)
	}
	assertNoDBSideResidue(t, env, db, name)
}

// assertLedgerStatus 台账状态断言（行不存在即失败）
func assertLedgerStatus(t *testing.T, ctx context.Context, db *gorm.DB, name, want string) {
	t.Helper()
	row, ok, err := pluginLedger(ctx, db, name)
	if err != nil || !ok {
		t.Fatalf("台账应有 %s 的行: ok=%v err=%v", name, ok, err)
	}
	if row.Status != want {
		t.Errorf("台账状态应为 %s，实际 %s", want, row.Status)
	}
}

// assertRuleStatus 该插件归属规则行（菜单/节点）的 status 逐行断言
func assertRuleStatus(t *testing.T, ctx context.Context, db *gorm.DB, name string, want uint8) {
	t.Helper()
	rules, err := gorm.G[model.AdminRule](db).Where("plugin = ?", name).Find(ctx)
	if err != nil {
		t.Fatalf("读取归属规则行失败: %v", err)
	}
	if len(rules) == 0 {
		t.Fatalf("插件 %s 应有归属规则行（菜单/节点）", name)
	}
	for i := range rules {
		if rules[i].Status == nil || *rules[i].Status != want {
			t.Errorf("规则 %s 的 status 应为 %d，实际 %v", rules[i].Name, want, rules[i].Status)
		}
	}
}

// assertNoDBSideResidue 卸载后 DB 侧与注册行的零残留（文件面另判：--keep-files 时文件是有意保留的）
func assertNoDBSideResidue(t *testing.T, env *Env, db *gorm.DB, name string) {
	t.Helper()
	ctx := context.Background()

	if row, ok, err := pluginLedger(ctx, db, name); ok || err != nil {
		t.Errorf("台账应无 %s 的行: row=%+v err=%v", name, row, err)
	}
	if count, err := gorm.G[model.AdminRule](db).Where("plugin = ?", name).Count(ctx, "*"); err != nil || count != 0 {
		t.Errorf("归属规则行应清空，实际 %d err=%v", count, err)
	}
	if count, err := gorm.G[host.Entity](db).Where("plugin = ?", name).Count(ctx, "*"); err != nil || count != 0 {
		t.Errorf("实体元数据应清空，实际 %d err=%v", count, err)
	}
	if count, err := gorm.G[model.Config](db).Where(model.Config{Group: "plugin." + name}).Count(ctx, "*"); err != nil || count != 0 {
		t.Errorf("configs 分组应清空，实际 %d err=%v", count, err)
	}
	registered, err := RegisteredPlugins(env.Root)
	if err != nil {
		t.Fatalf("读取接线文件失败: %v", err)
	}
	if slices.Contains(registered, name) {
		t.Errorf("接线文件不应再有 %s 的注册行（实际 %v）", name, registered)
	}
}
