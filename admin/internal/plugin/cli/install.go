package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/internal/model"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"

	"gorm.io/gorm"
)

// InstallOptions install 的旗标（接口定死）
type InstallOptions struct {
	// DryRun 只预演（全量输出），不落盘、不写库、不改接线文件
	DryRun bool
	// Enable 安装完成后接着置 enabled（--enable）
	Enable bool
	// Force 与工作树同名且内容不同时强行覆盖（默认逐项确认 ⇒ 需显式 --force）
	Force bool
	// AllowUnsigned 允许未签名包（v1 签名只提供不强制）；有签名且本机登记了受信公钥时照验
	// （配置键 plugin.trusted_keys）：无效签名不豁免——--allow-unsigned 只覆盖「没有签名 / 无法验证」
	AllowUnsigned bool
	// Operator 审计署名
	Operator string
	// Sanitize 审计 detail.error 的脱敏回调（nil = 原样落库）：
	// 失败现场会带 staging 临时目录与落位目标的**绝对路径**（staging.go 的「写入 %s 失败」、
	// deploy 侧目标路径），而失败审计会在插件管理页展示 ⇒ 两处调用方（catalog 安装通道
	// source_install.go、dev 通道 cmd/plugin 的 aigo plugin install）都传入与返回前端那个
	// message 同一个脱敏函数，口径必须一致（第 7 条）。
	Sanitize func(string) string
}

// sanitizeInstallText 按 opts.Sanitize 脱敏（未配置则原样返回）。
func (o InstallOptions) sanitizeInstallText(text string) string {
	if o.Sanitize == nil {
		return text
	}
	return o.Sanitize(text)
}

// Install 安装插件（编排 ①–⑧；L2 实体的结构 DDL 落在 ④ 与 ⑤ 之间的 ④b）。
//
// 执行主线（③–⑧：staging → DB 单事务 → L2 DDL → 落位 → 迁移 → 接线 → 收尾，含补偿回滚）见 install_run.go；
// 本函数负责 ①②（源解析与 fail-closed 校验）、L2 实体的结构预检（④b 的只读预演，拒绝项先于任何副作用）
// 与 --dry-run（全量预演）。
func Install(ctx context.Context, env *Env, src string, opts InstallOptions) error {
	// ① verify（fail-closed）：zip 源先经 staging 解包防护，再跑清单/依赖校验
	source, err := resolveInstallSource(env, src)
	if err != nil {
		return err
	}
	defer source.cleanup()

	plan, err := BuildInstallPlanFromSource(env, PlanSource{
		Dir:      source.Dir,
		Desc:     source.Desc,
		Ref:      source.Ref,
		FromZip:  source.FromZip,
		Checksum: source.checksum,
	}, opts.Enable)
	if err != nil {
		return err
	}
	integrity, err := checkSourceIntegrity(ctx, env, plan, integrityOptions{
		RequirePackManifest: source.FromZip,
		EnforceSignPolicy:   source.FromZip,
		AllowUnsigned:       opts.AllowUnsigned,
	})
	if err != nil {
		return err
	}

	// ④ 前置：L2 实体结构（DDL）的只读预演与拒装判定。
	// 拒绝项（删列/改类型/改列名等）在执行 ④–⑦ 之前就报全 ⇒ 拒装先于任何落盘/写库；
	// 预演读数进 ⑤ 段逐条列出（dry-run 与执行主线共用同一管线），执行面在 runInstall 的 ④b 重新生成。
	ddlReads, ddlErr := previewEntityDDL(ctx, env, plan)
	plan.DDL = ddlReads

	// ③b 前置：迁移基线（adopt）的**只读**预演（设计说明）：
	// 未声明 adopt ⇒ 零库访问（与既往口径一致）；声明了 ⇒ 读版本表状态 + 跑存在性探针（只读，不构造 migrator）。
	baseline, baselineErr := previewBaseline(ctx, env, plan)
	plan.Baseline = baseline

	plan.Render(env.Writer())

	if ddlErr != nil {
		return fmt.Errorf("L2 实体结构预检未通过（预演已列出逐条 DDL 与拒绝理由；未落盘、未写库）: %w", ddlErr)
	}
	// 迁移基线拒装：部分命中 / 现场与版本行不一致 / dirty ⇒ 在任何落盘/写库之前返回（零副作用）
	if baselineErr != nil {
		return fmt.Errorf("迁移基线（adopt）预检未通过（预演 ③b 已列出逐条探针与判定；未落盘、未写库）: %w", baselineErr)
	}

	// ③ 冲突（与工作树同名且内容不同 ⇒ 默认拒绝；逐项确认的机械化形态：要么 --force，要么不动）
	if !opts.Force {
		var conflicts []string
		for _, file := range plan.Files {
			if strings.Contains(file.Conflict, "内容不同") {
				conflicts = append(conflicts, file.Target)
			}
		}
		if len(conflicts) > 0 {
			return fmt.Errorf("与工作树同名但内容不同的文件 %d 个（逐项确认后加 --force 重跑；装前先核对是否是要覆盖的手工改动）:\n  %s",
				len(conflicts), strings.Join(conflicts, "\n  "))
		}
	}
	for _, migration := range plan.Migrations {
		if len(migration.Banned) > 0 {
			return fmt.Errorf("插件迁移 %s 含禁词（%s）：整文件隐式事务内必报错（docs/sql-discipline.md）",
				migration.Path, strings.Join(migration.Banned, ", "))
		}
	}

	// ② --dry-run 到此结束
	if opts.DryRun {
		env.Printf("\n（--dry-run：以上为全量预演，未落盘、未写库、未改接线文件；「A 的每一步都要能在 dry-run 中预演」）\n")
		return nil
	}

	env.Printf("\n执行安装编排（④–⑧；任一失败即补偿回滚，补偿失败则保留现场、状态留 failed）\n")
	return runInstall(ctx, env, plan, integrity, opts)
}

// previewBaseline ③b 前置：迁移基线（adopt）的只读预演。
//   - 未声明 adopt ⇒ 零库访问（本机制不介入，dry-run 的「不碰库」口径不变）；
//   - 声明了 ⇒ 取句柄读一次（版本表存在性 + 版本行 + pg_catalog 探针，**只读**）；
//     取不到库 = 硬错误（fail-closed，不假装预演过——同 L2 引擎自管实体的 DDL 预演口径）。
func previewBaseline(ctx context.Context, env *Env, plan *InstallPlan) (*BaselineRead, error) {
	if plan.Manifest == nil || plan.Manifest.Adopt == nil {
		return &BaselineRead{Table: MigrationTableName(plan.Name, env.Prefix), Mode: baselineModeNone}, nil
	}
	// 读库失败一律 fail-closed，但**照实渲染**「读库未完成」（不能渲染成「未声明 adopt」——
	// 那会把一次连接失败误报成「按编号顺序应用」的常规路径，见 baselineModeUnread）
	unread := func(cause error) *BaselineRead {
		return &BaselineRead{
			Declared: true, Through: plan.Manifest.Adopt.Through,
			Table: MigrationTableName(plan.Name, env.Prefix),
			Mode:  baselineModeUnread, Reason: cause.Error(),
		}
	}
	db, err := env.EnsureDB(ctx)
	if err != nil {
		cause := fmt.Errorf("迁移基线（adopt）需要读库（只读探针）: %w", err)
		return unread(cause), cause
	}
	read, err := readBaseline(ctx, db, env.Prefix, plan.Name, plan.Manifest.Adopt)
	if err != nil {
		cause := fmt.Errorf("读迁移基线（adopt）失败: %w", err)
		view := unread(cause)
		view.Probes = read.Probes
		view.ProbesTotal, view.ProbesHit = read.ProbesTotal, read.ProbesHit
		return view, cause
	}
	if read.Mode == baselineModeRejected {
		return read, baselineRejectError(plan.Name, read)
	}
	env.Printf("③b 迁移基线（adopt）：%s\n", baselineSummaryLine(read))
	return read, nil
}

// baselineSummaryLine ③b 摘要行（CLI 输出）的唯一派生点：读数 → 文案，和 ③b 预演段同源。
//
// 「版本表新建」= 现场**没有**版本表 ⇒ 本次安装一定会建它，依据两条事实：
//   - 申报 adopt（接管段）的插件必有迁移目录（verifyAdopt 是装前硬门禁：声明 adopt 而无 up 文件即拒装）
//     ⇒ 执行主线 ⑥ 会构造插件运行器（applyMigrationsIfPresent）；
//   - 驱动构造即建版本表（GOMODCACHE github.com/golang-migrate/migrate/v4@v4.19.1/database/postgres/
//     postgres.go:125 的 WithInstance → ensureVersionTable）。
//
// 所以这里必须**由读数派生**、不得由调用点传常量：同一读数在 ③b 预演段已照实打印
// 「版本表 <名>：不存在 / 已存在（版本…）」（plan.go 的 renderBaseline），两行同源才不会自相矛盾
// （早先调用点硬编码 false：现场无版本表时，摘要说「新建=false」而预演段说「不存在」）。
func baselineSummaryLine(read *BaselineRead) string {
	created := read != nil && read.Declared && !read.State.Existed
	return formatBaselineSummary(read, created)
}

// UninstallOptions uninstall 的旗标
type UninstallOptions struct {
	// DryRun 只预演将删清单（同口径）
	DryRun bool
	// Purge 回滚插件迁移 + drop 版本表（与 L2 表）——只能与同一次 uninstall 一起执行
	Purge bool
	// Yes --purge 的二次确认
	Yes bool
	// PurgeAdopted --purge 对「存量库迁移基线（adopt）」装入的插件放行（第三重显式确认）。
	// adopt 装入的插件其 down 文件是**核心链迁移的副本** ⇒ --purge 的回滚删的是核心链建的表与数据，
	// 而核心游标仍记这些迁移已应用（`aigo migrate up` 不会重建）⇒ 默认拒绝（fail-closed）。
	PurgeAdopted bool
	// KeepFiles 跳过文件删除（只清 DB 侧与注册行 ⇒ 入库仓里回到「出厂态」）
	KeepFiles bool
	// Force 跳过「先禁用后卸载」门禁（唯一入口，只给 failed 半装现场用）
	Force bool
	// Operator 审计署名
	Operator string
}

// Uninstall 卸载插件：先禁用后卸载是硬门禁（唯一例外是 failed 态的 --force 半装现场清理）；
// 固定顺序 a–e（a 迁移回滚 → b 删落位物 → c 删 DB 侧 → d 撤注册行 → e 审计）见 uninstall_run.go；
// --dry-run 打印同口径的将删/将保留清单。
//
// 本函数是卸载的**唯一入口**（HTTP 面 /admin/plugin/uninstall 复用本函数），环境门禁（生产禁止卸载，
// 连 --force 也不放行）与其余门禁同点落在这里；执行段 runUninstall 因而无需重复判定。
func Uninstall(ctx context.Context, env *Env, name string, opts UninstallOptions) error {
	db, err := env.EnsureDB(ctx)
	if err != nil {
		return err
	}
	row, err := gorm.G[host.Plugin](db).Where("name = ?", name).First(ctx)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("台账无插件 %q（未安装）：无卸载对象", name)
	}
	if err != nil {
		return fmt.Errorf("读取插件台账失败: %w", err)
	}

	// 环境门禁（生产禁止卸载）：先于其余门禁——生产环境里「先 disable 再 uninstall」这类指引都无意义。
	// --dry-run 不拦（dry-run 承诺：未动任何文件与行），但这一道与其余卸载门禁同款走
	// refuseUninstall ⇒ 被拒的尝试照样留痕（action=uninstall / status=rejected + 被拒原因）。
	if !opts.DryRun {
		if err := RequireLifecycle("卸载"); err != nil {
			return refuseUninstall(ctx, env, db, name, opts, err)
		}
	}

	// 先禁用后卸载（硬门禁）；唯一例外是 failed 态的 --force（半装现场清理）。
	// 被拒的尝试同样留痕（result=rejected + 被拒原因）：「每动作留审计」与「detail …
	// （含被拒原因）」——硬门禁挡住的动作是对宿主有实际意图的动作，不能静默消失。
	if row.Status == host.StatusFailed {
		if !opts.Force {
			return refuseUninstall(ctx, env, db, name, opts,
				fmt.Errorf("插件 %s 处于 failed（半装）：清理入口是 `aigo plugin uninstall --force %s`（跳过禁停门禁）", name, name))
		}
	} else if row.Status == host.StatusEnabled {
		return refuseUninstall(ctx, env, db, name, opts,
			fmt.Errorf("插件 %s 处于 enabled：先 `aigo plugin disable %s`（先禁用后卸载是硬门禁）", name, name))
	}
	if opts.Purge && !opts.Yes {
		return refuseUninstall(ctx, env, db, name, opts,
			fmt.Errorf("--purge 会回滚插件迁移并删表，必须同时给 --yes（二次确认）"))
	}
	// 存量库迁移基线（adopt）装入的插件：--purge 默认拒绝（三重显式：--purge --yes --purge-adopted）。
	// 判据是台账里的**状态事实** adopted_through（不是审计留痕），见 设计说明。
	if opts.Purge && row.AdoptedThrough > 0 && !opts.PurgeAdopted {
		return refuseUninstall(ctx, env, db, name, opts,
			fmt.Errorf("插件 %s 以「存量库迁移基线（adopt）」装入（adopted_through=%d）：其 down 文件是核心链迁移的副本 ⇒ "+
				"--purge 的逆序回滚删的是**核心链建的表与数据**，而核心游标仍记这些迁移已应用（`aigo migrate up` 不会重建）——"+
				"--purge 对 adopt 装入的插件默认拒绝（fail-closed）。放行需三重显式：--purge --yes --purge-adopted"+
				"（执行前打印将回滚/将 drop 的对象面与恢复路径）", name, row.AdoptedThrough))
	}
	// 第三重放行的**承诺**是「执行前打印将 drop 的对象面」（见上一条文案），而对象面只存在于**已落位
	// manifest 的 adopt 段**（renderAdoptedPurgeNotice）：清单读不到 ⇒ 对象面无从声明 ⇒ 拒绝放行。
	// 否则放行的是一次「删核心链建的表与数据」的回滚，而人看不到将删什么——承诺落空且不可自愈。
	if opts.Purge && row.AdoptedThrough > 0 && opts.PurgeAdopted {
		if err := checkPurgeAdoptedManifest(name, row.AdoptedThrough, loadPlacedManifest(env, name), opts); err != nil {
			return refuseUninstall(ctx, env, db, name, opts, err)
		}
	}
	if opts.PurgeAdopted && !opts.Purge {
		env.Printf("⚠ --purge-adopted 只在 --purge 下有意义：本次未给 --purge，按普通卸载执行（表与数据、版本表保留）\n")
	}
	if opts.Purge && opts.KeepFiles {
		env.Printf("⚠ --purge 与 --keep-files 同时给出：按 a–e 顺序执行（--purge 回滚迁移读 plugins/%s/migrations，随后 --keep-files 保留该目录）\n", name)
	}

	if err := renderUninstallPlan(ctx, env, db, name, opts); err != nil {
		return err
	}
	if opts.DryRun {
		env.Printf("\n（--dry-run：以上为将删/将保留清单，未动任何文件与行）\n")
		return nil
	}
	return runUninstall(ctx, env, name, row, opts)
}

// refuseUninstall 卸载门禁拒绝：先如实留痕（action=uninstall / status=rejected，detail 带被拒原因与旗标），
// 再返回原错误。审计写不进去只提示——拒绝本身不受审计可用性影响（同 install 侧 auditFailure 的口径）。
//   - 只给「台账里确有其行」的拒绝路径（插件名可被审计表直接反查；未装的名字不写）；
//   - `--dry-run` 的预演不写审计：预演是「未动任何行」的只读动作（dry-run 承诺），
//     写 rejected 行会让预演变成一次留痕。
func refuseUninstall(ctx context.Context, env *Env, db *gorm.DB, name string, opts UninstallOptions, cause error) error {
	if opts.DryRun {
		return cause
	}
	detail := map[string]any{
		"reason":        cause.Error(),
		"purge":         opts.Purge,
		"purge_adopted": opts.PurgeAdopted,
		"force":         opts.Force,
	}
	if err := WriteAudit(ctx, db, name, ActionUninstall, opts.Operator, AuditRejected, detail); err != nil {
		env.Printf("（审计未写入：%v）\n", err)
	}
	return cause
}

// renderUninstallPlan 输出卸载预演（清理面与保留面逐项对齐；与 runUninstall 的执行顺序逐条同口径）
func renderUninstallPlan(ctx context.Context, env *Env, db *gorm.DB, name string, opts UninstallOptions) error {
	env.Printf("插件卸载预演：%s（状态 %s%s）\n\n", name, currentStatus(db, ctx, name), purgeLabel(opts))

	m := loadPlacedManifest(env, name)
	migrationsPath := MigrationsPath(name, m)
	env.Printf("== a) 插件迁移回滚（仅 --purge）==\n")
	if !opts.Purge {
		env.Printf("默认不执行：迁移与版本表 %s 保留（数据与结构复装即用）\n", MigrationTableName(name, env.Prefix))
	} else {
		ledger, ok, ledgerErr := pluginLedger(ctx, db, name)
		if ledgerErr != nil {
			return ledgerErr
		}
		if ok && ledger.AdoptedThrough > 0 {
			// 清单门禁的第二道（判据同 Uninstall 处，那里已先拒一次并留痕）：渲染面不再静默跳过
			// 「将回滚/将 drop 的对象面」整段而仍然照着 a–e 执行——放行 ⟺ 清单已打印。
			if err := checkPurgeAdoptedManifest(name, ledger.AdoptedThrough, m, opts); err != nil {
				return err
			}
			renderAdoptedPurgeNotice(env, name, ledger.AdoptedThrough, m, opts)
		}
		migrations, err := ScanMigrations(env.Abs(migrationsPath))
		if err != nil {
			return err
		}
		if len(migrations) == 0 {
			env.Printf("回滚：无迁移文件（%s 不存在或为空）——现场缺源码时 --purge 会拒绝执行（不猜、不回滚）\n", migrationsPath)
		}
		for i := len(migrations) - 1; i >= 0; i-- { // 逆序回滚
			if migrations[i].Kind == "down" {
				env.Printf("回滚 %s（%d 条语句）\n", migrations[i].Path, migrations[i].Statements)
			}
		}
		env.Printf("drop 版本表 %s\n", MigrationTableName(name, env.Prefix))
		entities, err := gorm.G[host.Entity](db).Where("plugin = ?", name).Find(ctx)
		if err != nil {
			return fmt.Errorf("读取实体元数据失败: %w", err)
		}
		for i := range entities {
			if entityDDLOwner(entities[i].Spec) == "migrations" {
				env.Printf("保留 L2 实体表 %s（ddl_owner=migrations：结构由插件迁移自管，引擎不重复 DROP）\n", entities[i].PhysicalTable)
				continue
			}
			env.Printf("drop L2 实体表 %s\n", entities[i].PhysicalTable)
		}
		if len(entities) == 0 {
			env.Printf("（无 L2 实体元数据行：无引擎侧的表可 drop）\n")
		}
	}

	env.Printf("\n== b) 删落位物%s ==\n", keepLabel(opts))
	if !opts.KeepFiles {
		env.Printf("整目录 %s/**（源码入库位置；入库仓里表现为本插件路径的删除项）\n", PluginDir(name))
		env.Printf("整目录 %s/**（落位副本）\n", ViewTarget(name))
		langFiles := LangTargetsOf(env, name)
		if len(langFiles) == 0 {
			env.Printf("语言包：无（%s/*/plugin/%s.yaml 未落位）\n", WebLangRoot, name)
		}
		for _, langFile := range langFiles {
			env.Printf("%s（语言包落位副本）\n", langFile)
		}
		env.Printf("归属依据是**目录**与固定映射，不扫描文件头标记（标记是留痕，不是删除依据）\n")
	} else {
		env.Printf("--keep-files：源码与落位副本保留（入库仓的「出厂态」由它达到）\n")
	}

	rules := gorm.G[model.AdminRule](db).Where("plugin = ?", name)
	ruleRows, err := rules.Count(ctx, "*")
	if err != nil {
		return fmt.Errorf("统计归属规则行失败: %w", err)
	}
	entityRows, err := gorm.G[host.Entity](db).Where("plugin = ?", name).Count(ctx, "*")
	if err != nil {
		return fmt.Errorf("统计实体元数据行失败: %w", err)
	}
	env.Printf("\n== c) 删 DB 侧 ==\n")
	env.Printf("admin_rules 归属行（含子节点子树）：%d 行\n", ruleRows)
	env.Printf("admin_groups.rules CSV 里这些 ID 的悬挂项一并写回清除（写回为逗号分隔 ID；无法解析的项原样保留）\n")
	env.Printf("admin_plugin_entities 元数据行：%d 行\n", entityRows)
	env.Printf("configs 的 plugin.%s 分组（安装器写入的配置键）\n", name)

	content, err := os.ReadFile(env.Abs(RegistryFile))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	registered, err := RegisteredPluginsFrom(content)
	if err != nil {
		return err
	}
	rest := make([]string, 0, len(registered))
	for _, item := range registered {
		if item != name {
			rest = append(rest, item)
		}
	}
	env.Printf("\n== d) 撤接线注册行（%s）==\n%s", RegistryFile, RegistryDiff(content, BuildRegistryFile(rest)))

	env.Printf("\n== e) 审计 ==\n写入 admin_plugin_actions（action=uninstall，含台账摘要）+ 删台账行（同一事务，摘要先落；export 的案卷不受影响）\n")

	env.Printf("\n== 保留项 ==\n")
	env.Printf("- 表与数据（未 --purge 时）、插件迁移版本表（同上）、plugins/.store/%s-*.zip（本地复装凭据）\n", name)
	env.Printf("- 复装不是还原：节点 ID 已变，权限需重新勾选（脚本只能清悬挂项）\n")
	return nil
}

// checkPurgeAdoptedManifest --purge-adopted 的放行清单门禁（fail-closed，判据只有这一处）。
//
// 三重显式（--purge --yes --purge-adopted）的第三重，买的就是「执行前打印将回滚/将 drop 的对象面」
// 这一步知情权——而对象面只能从**已落位 manifest 的 adopt 段**读出来（renderAdoptedPurgeNotice 逐条
// 打印探针声明的对象）。清单读不到（plugins/<名>/ 不在、manifest 缺失或读不动、没有 adopt 段）时：
//   - 放行 = 在「不知道这次逆序回滚会删掉哪些核心链对象」的情况下删核心链建的表与数据，且核心游标
//     仍记这些迁移已应用（`aigo migrate up` 不会重建）⇒ 库进入不可自愈态；
//   - 故这里**不放行**（无清单不放行），错误文案给出两条恢复路径（恢复清单 / 改走普通卸载）。
//
// 只在「台账说 adopt 装入（adopted_through > 0）+ --purge + --purge-adopted」时介入：前两重由
// Uninstall 的门禁管（未 adopt 装入的 --purge-adopted 只是无效旗标，见那两条提示）。
func checkPurgeAdoptedManifest(name string, adoptedThrough int, m *manifest.Manifest, opts UninstallOptions) error {
	if !(opts.Purge && opts.PurgeAdopted && adoptedThrough > 0) {
		return nil
	}
	if m != nil && m.Adopt != nil {
		return nil
	}
	return fmt.Errorf("插件 %s 以「存量库迁移基线（adopt）」装入（台账 adopted_through=%d），但**放行清单读不到**"+
		"（%s/%s 不在、读不动，或没有 adopt 段）：--purge-adopted 的放行条件正是「执行前打印将 drop 的对象面」，"+
		"没有清单就不放行（fail-closed）——这次回滚删的是**核心链建的表与数据**，而核心游标（schema_migrations）"+
		"仍记它们已应用（`aigo migrate up` 不会重建）。\n"+
		"  恢复路径①：把装入时的 %s 恢复到 %s/（adopt 段须在：through + 逐条 probes），再执行本次卸载；\n"+
		"  恢复路径②：放弃 --purge，按普通卸载（表与数据、版本表保留）：`aigo plugin uninstall %s`"+
		"（源码目录也不在时加 --keep-files）",
		name, adoptedThrough, PluginDir(name), manifest.FileName, manifest.FileName, PluginDir(name), name)
}

// renderAdoptedPurgeNotice adopt 装入插件的 --purge 提示（设计说明）：
// 打印「回滚对象面（探针声明的对象）+ 恢复路径」；未给 --purge-adopted 时 --purge 已在门禁处被拒。
// 探针只声明**存在性**，down 文件覆盖的对象面可能更大——如实标注，不假装清单完整。
func renderAdoptedPurgeNotice(env *Env, name string, adoptedThrough int, m *manifest.Manifest, opts UninstallOptions) {
	env.Printf("\n== a0) 迁移基线（adopt）==\n")
	env.Printf("该插件以「存量库迁移基线（adopt）」装入（台账 adopted_through=%d）：1..%d 与核心链对应迁移同物，\n", adoptedThrough, adoptedThrough)
	env.Printf("安装期**未执行任何旧 DDL**（版本表由基线写入）⇒ 这些结构是**核心链**建的，不是插件链建的。\n")
	env.Printf("--purge 会按插件 down 逆序回滚（down 文件 = 核心链迁移的副本）⇒ 删的是核心链建的表与数据；\n")
	env.Printf("核心游标（schema_migrations）仍记这些迁移已应用 ⇒ `aigo migrate up` **不会**重建：库会进入「结构缺失、游标说在」的不可自愈态。\n")
	env.Printf("默认拒绝（fail-closed）；放行需三重显式 --purge --yes --purge-adopted（本次 purge_adopted=%v）。\n", opts.PurgeAdopted)

	if m != nil && m.Adopt != nil && opts.PurgeAdopted {
		env.Printf("将回滚/将 drop 的对象面（按 manifest adopt 探针声明；物理名口径与探针/审计同一处 probePhysical，探针只声明存在性，down 覆盖面可能更大）：\n")
		for _, probe := range m.Adopt.Probes {
			switch probe.Kind {
			case "table", "index", "column", "constraint":
				// 逐字用 probePhysical：约束名与表名/索引名一样带前缀、列名不带（此前本处手搓格式串
				// 漏了约束名的前缀 ⇒ 打印的名字与库中实际对象名不一致，purge 前清单会误导操作者）。
				env.Printf("  %-10s %s\n", probe.Kind, probePhysical(env.Prefix, probe))
			default:
				env.Printf("  （v%d kind: none：无对象声明）\n", probe.Version)
			}
		}
		env.Printf("恢复路径：核心链不回填这些对象 ⇒ 从备份恢复，或按插件链全量重建（重装后探针全不命中 ⇒ 走全量分支）。\n")
	}
	env.Printf("\n")
}

// PromoteOptions promote 的旗标
type PromoteOptions struct {
	// Entity 要退出 L2 的实体名
	Entity string
	// Yes 确认执行（缺省只预演：打印将删清单；执行主线见 promote_run.go）
	Yes bool
	// Operator 审计署名
	Operator string
}

// Promote L2 → L1 升级的执行主线（/C8）：把一个 L2 实体移出声明式面——
// 删 admin_plugin_entities 的该实体元数据行 + 通用页菜单/节点（公式面，含各角色组 rules
// CSV 的悬挂 ID 写回清除）+ 写审计（action=upgrade）。**表与数据一律不动**：此后该表等价于
// ddl_owner=migrations（DDL 与 DROP 只能由插件迁移承担，否则同一张表被 L2 引擎与 L1 四层双重治理）。
//
// 门禁与口径：
//   - 缺 --entity ⇒ 报错；缺 --yes ⇒ 只打印预演（含将删行清单），不写任何行、不写审计；
//   - 台账必须存在（未安装无 promote 对象）；failed（半装）拒绝——该态唯一清理入口是 uninstall --force；
//   - 被删行公式面的**精确规则名**定位（页 + 动作并集，见 entityPageRuleNames）：不用 LIKE
//     前缀匹配（实体名允许下划线、前缀词会误伤）；页下作者自有的行（不在公式面）保留并如实报告；
//   - 审计里落 promote 结论（detail.promote=true）：安装期的所有权检查据此拒装「复装仍声明
//     ddl_owner=engine」的实体（该表此后归 L1/迁移自管，禁双重治理；见 checkEntityOwnership）；
//   - --yes 路径上「台账存在但被拒」的尝试写 rejected 审计（同 uninstall 的 refuseUninstall 口径）；
//     预演（无 --yes）是只读动作，不写审计。
func Promote(ctx context.Context, env *Env, name string, opts PromoteOptions) error {
	entityName := strings.TrimSpace(opts.Entity)
	if entityName == "" {
		return fmt.Errorf("promote 需要 --entity <实体>（指定要退出 L2 的实体）")
	}
	db, err := env.EnsureDB(ctx)
	if err != nil {
		return err
	}
	ledger, err := gorm.G[host.Plugin](db).Where("name = ?", name).First(ctx)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("台账无插件 %q（未安装）：无 promote 对象", name)
	}
	if err != nil {
		return fmt.Errorf("读取插件台账失败: %w", err)
	}
	if ledger.Status == host.StatusFailed {
		return refusePromote(ctx, env, db, name, entityName, opts,
			fmt.Errorf("插件 %s 处于 failed（半装）：该态的唯一清理入口是 `aigo plugin uninstall --force %s`，不接受 promote", name, name))
	}
	entity, err := gorm.G[host.Entity](db).Where("plugin = ? AND name = ?", name, entityName).First(ctx)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return refusePromote(ctx, env, db, name, entityName, opts,
			fmt.Errorf("插件 %s 没有名为 %q 的 L2 实体元数据（未以声明式形态安装，或已 promote）：无删除对象", name, entityName))
	}
	if err != nil {
		return fmt.Errorf("读取实体元数据失败: %w", err)
	}

	// 公式面行（页 + 动作并集）与同插件的其余归属行（作者自有：保留，只报告）
	formula := make(map[string]struct{})
	for _, ruleName := range entityPageRuleNames(name, entityName) {
		formula[ruleName] = struct{}{}
	}
	owned, err := gorm.G[model.AdminRule](db).Where("plugin = ?", name).Order("id").Find(ctx)
	if err != nil {
		return fmt.Errorf("读取归属规则行失败: %w", err)
	}
	removeRows := make([]model.AdminRule, 0, len(formula))
	keepRows := make([]model.AdminRule, 0, len(owned))
	for i := range owned {
		if _, hit := formula[owned[i].Name]; hit {
			removeRows = append(removeRows, owned[i])
			continue
		}
		keepRows = append(keepRows, owned[i])
	}

	env.Printf("L2 → L1 升级（promote）：%s 的实体 %s（物理表 %s；台账状态 %s）\n\n", name, entity.Name, entity.PhysicalTable, ledger.Status)
	env.Printf("前置（①，必须先做）：用 CRUD 生成器的「选择数据表」模式对 %s 生成四层与页面（Rebuild=No）\n", entity.PhysicalTable)
	env.Printf("② 将删除：admin_plugin_entities 的元数据行（id=%d）\n", entity.ID)
	env.Printf("   将删除的通用页菜单/节点 %d 行（公式面：实体页 %s + 权限节点）：\n", len(removeRows), entityPageName(name, entityName))
	for i := range removeRows {
		env.Printf("     - %s（%s）\n", removeRows[i].Name, removeRows[i].Type)
	}
	if len(keepRows) > 0 {
		names := make([]string, 0, len(keepRows))
		for i := range keepRows {
			names = append(names, keepRows[i].Name)
		}
		env.Printf("   保留 %d 条归属行（不在公式面内：作者自有的菜单/节点）: %s\n", len(keepRows), strings.Join(names, ", "))
	}
	env.Printf("   被删行在各角色组 rules CSV 里的悬挂 ID 一并写回清除（无法解析的项原样保留，同口径）\n")
	env.Printf("③ 重启后 L2 路由消失、L1 路由生效\n")
	env.Printf("④ 该表此后等价于 ddl_owner=migrations：DDL 与 DROP 只能由插件迁移承担（否则双重治理）\n")
	env.Printf("⑤ 审计：写入 admin_plugin_actions（action=upgrade + promote 结论）\n")
	env.Printf("⑥ 复装保护：安装期按审计里的 promote 结论拒装「该实体仍声明 ddl_owner=engine」的复装；" +
		"复装前必须把该实体从 entities 段移除（L1 形态）或声明 ddl_owner: migrations\n")
	if placedManifestDeclaresEntity(env, name, entityName) {
		env.Printf("⚠ 已落位 manifest 仍声明实体 %s：升级为 L1 后请同步删除 entities 段的该实体，否则复装会把元数据写回\n", entityName)
	}

	if !opts.Yes {
		env.Printf("\n（未加 --yes：以上为预演；未写任何行。前置 ① 完成后再加 --yes 执行）\n")
		return nil
	}
	return runPromote(ctx, env, db, name, entity, removeRows, opts)
}

// currentStatus 台账状态（读失败按空串）
func currentStatus(db *gorm.DB, ctx context.Context, name string) string {
	row, err := gorm.G[host.Plugin](db).Where("name = ?", name).First(ctx)
	if err != nil {
		return "?"
	}
	return row.Status
}

// purgeLabel 卸载预演的旗标摘要
func purgeLabel(opts UninstallOptions) string {
	labels := make([]string, 0, 3)
	if opts.Purge {
		labels = append(labels, "--purge")
	}
	if opts.PurgeAdopted {
		labels = append(labels, "--purge-adopted")
	}
	if opts.Force {
		labels = append(labels, "--force")
	}
	if len(labels) == 0 {
		return ""
	}
	return " · " + strings.Join(labels, " ")
}

// keepLabel 文件删除段的旗标摘要
func keepLabel(opts UninstallOptions) string {
	if opts.KeepFiles {
		return "（--keep-files：跳过）"
	}
	return ""
}

// splitCSV 逗号分隔清单（去空项与首尾空白）
func splitCSV(s string) []string {
	var out []string
	for _, item := range strings.Split(s, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}
