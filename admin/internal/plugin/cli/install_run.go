package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"
	"github.com/ai-go-hub/ai-go-admin/pkg/airx"

	"gorm.io/gorm"
)

// sourceSpec 安装源（目录源本身，或包源解包出的临时目录）
type sourceSpec struct {
	// Dir 本机目录（zip 源 = 解包临时目录）
	Dir string
	// Desc 展示用描述
	Desc string
	// Ref 落台账 source 列的值（包文件名 / 仓库相对目录）
	Ref string
	// FromZip 是否包源（包源走包格式与签名策略）
	FromZip bool
	// checksum 整包 sha256（包源）
	checksum string
	// staged 包源解包结果（cleanup 用）
	staged *Staged
}

// cleanup 清理包源解包出的临时目录（目录源无操作）
func (s *sourceSpec) cleanup() {
	if s != nil && s.staged != nil {
		s.staged.Cleanup()
	}
}

// resolveInstallSource 解析安装源：`.zip` 走 staging 解包（zip 防护），否则按目录源解析。
func resolveInstallSource(env *Env, src string) (*sourceSpec, error) {
	raw := strings.TrimSpace(src)
	if strings.HasSuffix(strings.ToLower(raw), ".zip") {
		full := raw
		if !filepath.IsAbs(full) {
			full = env.Abs(raw)
		}
		staged, err := StageZip(full)
		if err != nil {
			return nil, err
		}
		report := staged.Report
		env.Printf("包源：%s（整包 sha256 %s，%d 字节）→ 解包 %d 个条目 / %d 字节（临时目录 %s）\n",
			report.Zip, shortSHA(report.Checksum), report.Bytes, report.Entries, report.ExtractedBytes, staged.Dir)
		env.Printf("  zip 防护：路径穿越/符号链接/设备文件逐条拒绝 · 大小按实际解出字节计量 · "+
			"判重含大小写折叠 + NFC · 上限 整包≤%dMB / 解包≤%dMB / 单文件≤%dMB / 条目≤%d\n",
			MaxPackageBytes>>20, MaxExtractBytes>>20, MaxEntryBytes>>20, MaxEntryCount)
		return &sourceSpec{
			Dir:      staged.Dir,
			Desc:     fmt.Sprintf("%s（已解包至临时目录 %s）", report.Zip, staged.Dir),
			Ref:      filepath.Base(report.Zip),
			FromZip:  true,
			checksum: report.Checksum,
			staged:   staged,
		}, nil
	}

	dir, err := env.PluginSource(raw)
	if err != nil {
		return nil, err
	}
	ref, err := filepath.Rel(env.Root, dir)
	if err != nil {
		ref = dir
	}
	return &sourceSpec{Dir: dir, Desc: dir, Ref: filepath.ToSlash(ref)}, nil
}

// integrityOptions 源侧完整性校验的口径
type integrityOptions struct {
	// RequirePackManifest 包源必备 files.sha256（包格式；缺失即拒装）
	RequirePackManifest bool
	// EnforceSignPolicy 包源签名策略：无签名 ⇒ 需 --allow-unsigned（「未签名包须显式 --allow-unsigned」）
	EnforceSignPolicy bool
	// AllowUnsigned 显式接受未签名/未验证签名的包（有签名且本机登记了受信公钥时照验：无效签名不豁免）
	AllowUnsigned bool
}

// sourceIntegrity 完整性校验读数（落台账）
type sourceIntegrity struct {
	// Checksum 整包 sha256（目录源为空；来源认证由签名承载，未签名时只作事后回溯）
	Checksum string
	// SignedBy 签名公钥指纹（`sha256:<hex>`）；签名未验证时为空——不校验就不能声称指纹
	SignedBy string
	// PackVerified 是否做了包内清单逐项比对
	PackVerified bool
	// PackFiles 清单列出的文件数（PackVerified 时有效）
	PackFiles int
	// SigPresent 源内是否存在签名文件（含未验证态）
	SigPresent bool
}

// checkSourceIntegrity 源侧完整性校验：
//   - 包/目录源自带 files.sha256 时逐项比对（清单缺失项 / 不一致 / 清单之外夹带 ⇒ 拒装）；
//   - 包源按 integrityOptions 施加「必备清单 + 签名策略」；签名判定见 signature.go（ed25519，
//     签名对象 = files.sha256 的字节；本机受信公钥来自配置键 plugin.trusted_keys）；
//   - requires 逐项查台账「已装且版本相容」——需要台账，取不到就报错（fail-closed，不静默放行）。
func checkSourceIntegrity(ctx context.Context, env *Env, plan *InstallPlan, opts integrityOptions) (*sourceIntegrity, error) {
	result := &sourceIntegrity{}
	sigFile := DefaultSignatureFile
	if plan.Manifest.Signature != "" {
		sigFile = plan.Manifest.Signature
	}

	var packManifest []byte
	content, err := os.ReadFile(filepath.Join(plan.Dir, PackManifestFile))
	switch {
	case err == nil:
		packManifest = content
		listed, parseErr := ParsePackManifest(content)
		if parseErr != nil {
			return nil, parseErr
		}
		if err := VerifyPackFiles(plan.Dir, listed, sigFile); err != nil {
			return nil, err
		}
		result.PackVerified = true
		result.PackFiles = len(listed)
		env.Printf("✓ %s：逐项比对通过（%d 个文件；清单缺失项 / 不一致 / 清单之外夹带一律拒装）\n", PackManifestFile, len(listed))
	case errors.Is(err, os.ErrNotExist) && opts.RequirePackManifest:
		return nil, fmt.Errorf("包内缺 %s（包格式；用 `aigo plugin export` 生成）：拒装", PackManifestFile)
	case errors.Is(err, os.ErrNotExist):
		env.Printf("（目录源不带 %s：逐项完整性由落位映射与 verify 复查；包源必须以 export 产出的包分发）\n", PackManifestFile)
	default:
		return nil, fmt.Errorf("读取 %s 失败: %w", PackManifestFile, err)
	}

	// 签名判定：ed25519 分离签名，签名对象 = packManifest（files.sha256 的字节）。
	// 判定口径与 fail-closed 边界见 signature.go 头注释；受信公钥取不到（配置项写错）同样是硬错误：
	// 「有一把读不懂的公钥」不能静默退化成「没有公钥」。
	keys, keyErr := LoadTrustedKeys()
	if keyErr != nil {
		return nil, keyErr
	}
	if _, sigErr := os.Stat(filepath.Join(plan.Dir, filepath.FromSlash(sigFile))); sigErr == nil {
		result.SigPresent = true
	} else if !errors.Is(sigErr, os.ErrNotExist) {
		return nil, fmt.Errorf("检查签名 %s 失败: %w", sigFile, sigErr)
	}
	outcome, err := checkSignaturePolicy(env, plan.Dir, sigFile, packManifest, keys, opts)
	if err != nil {
		return nil, err
	}
	result.SignedBy = outcome.Fingerprint

	if plan.FromZip {
		result.Checksum = plan.ZipChecksum
	}

	if strings.TrimSpace(plan.Manifest.Requires) == "" {
		return result, nil
	}
	db, err := env.EnsureDB(ctx)
	if err != nil {
		return nil, fmt.Errorf("校验依赖（requires=%s）需要台账：%w", plan.Manifest.Requires, err)
	}
	for _, item := range splitCSV(plan.Manifest.Requires) {
		name, domain, _ := strings.Cut(item, "@")
		name, domain = strings.TrimSpace(name), strings.TrimSpace(domain)
		row, err := gorm.G[host.Plugin](db).Where("name = ?", name).First(ctx)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("依赖插件 %q 未安装（requires: %s）", name, item)
		}
		if err != nil {
			return nil, fmt.Errorf("读取依赖插件 %q 台账失败: %w", name, err)
		}
		ok, err := host.InRange(domain, row.Version)
		if err != nil {
			return nil, fmt.Errorf("依赖插件 %q 的版本域 %q 无法解析: %w", name, domain, err)
		}
		if !ok {
			return nil, fmt.Errorf("依赖插件 %q 的版本 %s 不满足 %q", name, row.Version, domain)
		}
		env.Printf("✓ 依赖满足：%s %s（%s）\n", name, row.Version, item)
	}
	return result, nil
}

// installState 一次安装编排的中间状态（补偿回滚据此逐项回退）
type installState struct {
	plan       *InstallPlan
	artifacts  *installArtifacts
	ddl        *entityDDLApply
	deployer   *Deployer
	migrations *MigrationOutcome
	register   string
}

// applyMigrationsIfPresent ⑥：迁移目录不存在 ⇒ 无迁移可应用（不进运行器、也不建版本表——
// 声明式插件本就没有迁移；建空版本表只会给自己留「半装」残留）。
// 声明了 adopt（接管段）时把整段传下去：三态判定在 ApplyPluginMigrationsWithAdopt 内**重算一次**
// （预演读数不作执行依据——执行侧读到的现场才是依据，同 ④b 的「执行面重新生成」口径）。
func applyMigrationsIfPresent(ctx context.Context, env *Env, plan *InstallPlan) (*MigrationOutcome, error) {
	dir := plan.MigrationsAbs(env)
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("检查迁移目录 %s 失败: %w", plan.MigrationsPath, err)
		}
		env.Printf("⑥ 迁移：无迁移目录（%s）⇒ 无迁移可应用，不建版本表\n", plan.MigrationsPath)
		return nil, nil
	}
	var adopt *manifest.Adopt
	if plan.Manifest != nil {
		adopt = plan.Manifest.Adopt
	}
	return ApplyPluginMigrationsWithAdopt(ctx, env, plan.Name, dir, adopt)
}

// runInstall 执行安装编排的 ④–⑧：写库 → L2 DDL（④b）→ 落位 → 迁移 → 接线 → 收尾；
// 任一 ④–⑦ 失败 ⇒ 补偿回滚（删已落位文件 → 回滚已应用迁移 → drop 本次新建的 L2 表 → 删台账/归属行）；
// 补偿失败或迁移留下 dirty ⇒ 保留现场、状态留 failed，由 `uninstall --force` 清理。
//
// 本函数是**安装的唯一执行入口**（CLI 的 Install 与清单通道的 installCatalogZip 都收敛到这里），
// 因此环境门禁（生产禁止安装）放在这里而非各入口的预演段：dry-run 在各自入口就返回、根本到不了此处
// （预演是只读的，不受此限）；各入口另有一道快速拒绝，见 environment.go 的「门禁落点」。
func runInstall(ctx context.Context, env *Env, plan *InstallPlan, integrity *sourceIntegrity, opts InstallOptions) error {
	if err := RequireLifecycle("安装"); err != nil {
		return err
	}
	db, err := env.EnsureDB(ctx)
	if err != nil {
		return err
	}

	// ④ DB（单事务）：台账行(status=failed) + 实体元数据 + 菜单/节点 + configs 分组
	artifacts, err := writeInstallDB(ctx, env, db, plan, plan.SourceRef, integrity.Checksum, integrity.SignedBy)
	if err != nil {
		return fmt.Errorf("④ 写库失败（单事务已整体回滚，无半写现场）: %w", err)
	}
	state := &installState{plan: plan, artifacts: artifacts}
	env.Printf("④ DB（单事务）：台账 status=failed + 菜单/节点 %d 行（status=0）+ 实体 %d 行 + configs %d 行\n",
		len(artifacts.RuleIDs), len(artifacts.EntityIDs), len(artifacts.ConfigIDs))

	// ④b L2 实体结构（引擎 DDL，「装（新实体）」；与 --purge 的删表对称）：
	// 位置在 ④ 之后、⑤ 落位之前——明列「数据回填只能写迁移文件」，⑥ 的插件迁移可能回填 L2 表，
	// 故建表必须先于 ⑥；ddl_owner=migrations 的实体不在引擎的写入面内（结构由插件迁移承担）。
	apply, ddlErr := applyEntityDDL(ctx, env, db, plan)
	state.ddl = apply
	if ddlErr != nil {
		return failInstall(ctx, env, db, state, fmt.Errorf("④b L2 DDL 失败: %w", ddlErr), opts)
	}
	if stat := apply.appliedStatements(); stat > 0 {
		env.Printf("④b DDL：共应用 %d 条，逐条回报见上（每条独立事务；失败即停，不吞错）\n", stat)
	}

	// ⑤ 落位（airx.Pause 包住：文件未落完就被 air 重启打断会留半装现场）
	deployer, err := NewDeployer(env, plan)
	if err != nil {
		return failInstall(ctx, env, db, state, fmt.Errorf("⑤ 落位准备失败: %w", err), opts)
	}
	state.deployer = deployer
	placeErr := func() error {
		airx.Pause()
		defer airx.Resume()
		return deployer.Place(plan)
	}()
	if placeErr != nil {
		return failInstall(ctx, env, db, state, fmt.Errorf("⑤ 落位失败: %w", placeErr), opts)
	}
	env.Printf("⑤ 落位：%d 个文件（mkdir -p + 原子 rename；白名单扩展名已写归属标记 `plugin: %s v%s`）\n",
		len(plan.Files), plan.Name, plan.Manifest.Version)

	// ⑥ 迁移（per-plugin 运行器：独立版本表 + 独立 advisory lock；读**已落位**的 plugins/<名>/migrations）
	outcome, migrateErr := applyMigrationsIfPresent(ctx, env, plan)
	state.migrations = outcome
	if migrateErr != nil {
		return failInstall(ctx, env, db, state, fmt.Errorf("⑥ 迁移失败: %w", migrateErr), opts)
	}

	// ⑦ 接线（只写 register_gen.go；declarative 没有 Go 包，不写注册行）
	diff, err := wirePlugin(env, plan.Name, plan.Manifest.Kind)
	if err != nil {
		return failInstall(ctx, env, db, state, fmt.Errorf("⑦ 接线失败: %w", err), opts)
	}
	state.register = diff
	env.Printf("⑦ 接线（%s）：\n%s", RegistryFile, diff)

	// ⑧ 收尾：台账置 installed（菜单仍隐藏）+ 接管段回写（adopt 装入时记 adopted_through）+ 审计 + 提示
	adoptedThrough := 0
	if outcome != nil {
		adoptedThrough = outcome.AdoptedThrough
	}
	if err := finishInstallStatus(ctx, db, plan.Name, adoptedThrough); err != nil {
		// 台账没能置 installed ⇒ 现场仍是「半装」（文件/迁移/归属行都在、状态留 failed）：
		// 同口径留一条 install/failed 审计（否则这次安装只在错误输出里留痕，审计面看不到；
		//「每动作留审计」/审计落点）。现场不回滚 ⇒ 备份目录可以丢。
		deployer.Discard()
		return auditFailure(ctx, env, db, plan.Name, opts, map[string]any{
			"source":       plan.SourceRef,
			"kind":         plan.Manifest.Kind,
			"ddl":          ddlAuditDetail(apply),
			"failed_step":  "⑧ 收尾写库（台账 status=installed）",
			"compensation": "skipped:现场已就位（唯一清理入口 uninstall --force）",
			"failed_state": true,
		}, fmt.Errorf("⑧ 收尾写库失败（插件现场已就位：核对台账后置 installed，或 `aigo plugin uninstall --force %s` 清理）: %w", plan.Name, err))
	}
	deployer.Discard()
	env.Printf("⑧ 台账 status=installed（菜单仍隐藏：enable 时置 1）%s\n", adoptedLedgerNote(adoptedThrough))

	applied := []int{}
	if outcome != nil && outcome.Applied != nil {
		// baseline（adopt）路径 Applied 为 nil（未执行任何旧 DDL）：审计面按空数组落账，
		// 不落 JSON null——读审计的工具按数组解析（DB 用例 TestPluginInstallBaselineAdoptDB 钉住）。
		applied = outcome.Applied
	}
	auditDetail := map[string]any{
		"source":     plan.SourceRef,
		"checksum":   integrity.Checksum,
		"signed_by":  integrity.SignedBy,
		"kind":       plan.Manifest.Kind,
		"files":      len(plan.Files),
		"rules":      len(artifacts.RuleIDs),
		"entities":   len(artifacts.EntityIDs),
		"configs":    len(artifacts.ConfigIDs),
		"ddl":        ddlAuditDetail(apply),
		"migrations": applied,
		"baseline":   baselineAuditDetail(outcome),
		"register":   strings.TrimSpace(diff),
		"enable":     opts.Enable,
	}
	if err := WriteAudit(ctx, db, plan.Name, ActionInstall, opts.Operator, AuditSuccess, auditDetail); err != nil {
		return fmt.Errorf("安装已完成（台账 status=installed），但审计未写入: %w", err)
	}

	env.Printf("\n✓ 安装完成：%s v%s（kind: %s）\n", plan.Name, plan.Manifest.Version, plan.Manifest.Kind)
	env.Printf("  生效方式：重编译 + 重启后端（接线真相在编译产物）；前端改动需 pnpm build（dev 下 HMR）\n")
	env.Printf("  启用：aigo plugin enable %s（运行时闸按请求读库，≤1s 生效，无需重启）\n", plan.Name)
	env.Printf("  信任定位：kind=code 的插件等价于把该段代码 git merge 进仓库——与宿主同权限、无隔离层\n")

	if opts.Enable {
		if err := setPluginStatus(ctx, env, plan.Name, opts.Operator, host.StatusEnabled); err != nil {
			return fmt.Errorf("--enable 失败（插件已装、台账为 installed）: %w", err)
		}
	}
	return nil
}

// failureAuditDetail 安装失败审计的 detail 骨架。
//
// error 走 opts.Sanitize（与返回前端/CLI 的 message 同一回调）：失败原因会带 staging 临时目录
// 与落位目标的绝对路径，而该字符串会在插件管理页的动作审计区块展示（第 7 条）。
// 抽成函数是为了让「脱敏确实作用在落库的 detail 上」有一条不依赖数据库的断言（见 install_options_test.go）。
func failureAuditDetail(opts InstallOptions, cause error, state *installState) map[string]any {
	return map[string]any{
		"error":  opts.sanitizeInstallText(cause.Error()),
		"source": state.plan.SourceRef,
		"kind":   state.plan.Manifest.Kind,
	}
}

// failInstall 失败收尾：补偿回滚并把审计写成 failed。
//   - 迁移留下 dirty ⇒ 补偿无法完成（不静默 force）⇒ **保留现场**（文件与行都不动）、状态留 failed；
//   - 否则按「删已落位文件 → 回滚已应用迁移 → drop 本次新建的 L2 表 → 撤接线 → 删台账/归属行」逐段回退，
//     **任一失败即停在原地**保留现场；
//   - 现场保留时唯一清理入口是 `aigo plugin uninstall --force <名>`。
func failInstall(ctx context.Context, env *Env, db *gorm.DB, state *installState, cause error, opts InstallOptions) error {
	env.Printf("\n✗ %v\n", cause)
	detail := failureAuditDetail(opts, cause, state)
	if state.ddl != nil {
		detail["ddl"] = ddlAuditDetail(state.ddl)
	}

	if state.migrations != nil && state.migrations.Dirty {
		env.Printf("补偿回滚：**未执行**——迁移失败留下 dirty 标志（版本表 %s，版本 %d）。"+
			"不做静默 force ⇒ 现场保留、状态留 failed\n", state.migrations.Table, state.migrations.After)
		env.Printf("  修复入口：`aigo plugin migrate-force %s <版本>` 归位后，`aigo plugin uninstall --force %s` 清理\n",
			state.plan.Name, state.plan.Name)
		detail["compensation"] = "skipped:dirty"
		detail["failed_state"] = true
		return auditFailure(ctx, env, db, state.plan.Name, opts, detail, cause)
	}

	steps := make([]string, 0, 4)
	if state.deployer != nil {
		if err := state.deployer.Rollback(); err != nil {
			return keepScene(ctx, env, db, state, opts, cause, detail, "文件面回滚未完成", err)
		}
		state.deployer.Discard()
		steps = append(steps, fmt.Sprintf("已删除落位文件并还原被覆盖项（%d 个目标）", len(state.plan.Files)))
	}
	// 迁移回滚读**安装源**的 migrations（不是落位副本）：补偿顺序「删已落位文件 →
	// 回滚已应用迁移」，上一步已把 plugins/<名>/migrations 删掉（包源/新落位）或还原（目录源与落位同址）；
	// down 文件仍在源目录（包源的 staging 临时目录活到 Install 返回之后）——读落位副本必然「没有迁移目录」，
	// 会让本可完成的补偿退化成「保留现场」（⑥ 成功、⑦ 失败时现场已被拆掉一半，不是设计承诺的「一键撤销」）。
	if err := RollbackPluginMigrations(ctx, env, state.plan.Name, state.plan.SourceMigrationsAbs(env), state.migrations); err != nil {
		return keepScene(ctx, env, db, state, opts, cause, detail, "迁移回滚未完成", err)
	}
	if state.migrations != nil && len(state.migrations.Applied) > 0 {
		steps = append(steps, fmt.Sprintf("已回滚迁移 %s", formatVersions(state.migrations.Applied)))
	}
	// 基线（adopt）：本次没执行任何旧 DDL ⇒ 绝不按 down 回滚（down 与核心链同物）；版本表另按基线口径处置
	if state.migrations != nil && state.migrations.Baselined {
		steps = append(steps, fmt.Sprintf("迁移基线（adopt）：本次未执行任何旧 DDL；版本表 %s 已按基线口径处置（见上）", state.migrations.Table))
	}
	// DDL 段：只回滚「本次安装新建」的表（补偿口径 = 一键撤销到安装前）；既有表一律不动——
	// 复装/升级路径上的加列无数据损失且重试幂等（与卸载默认保留表的同口径）。
	dropped, err := dropInstallCreatedTables(ctx, db, state.ddl)
	if err != nil {
		return keepScene(ctx, env, db, state, opts, cause, detail, "L2 新建表回滚未完成", err)
	}
	if len(dropped) > 0 {
		steps = append(steps, fmt.Sprintf("已删除本次安装新建的 L2 表 %s（既有表与数据一律不动）", strings.Join(dropped, ", ")))
	} else if state.ddl != nil && state.ddl.appliedStatements() > 0 {
		steps = append(steps, "本次未新建表：既有表上的加列/加索引不回滚（无数据损失、重试幂等；与卸载的保留口径一致）")
	}
	if _, removed, err := unwirePlugin(env, state.plan.Name); err != nil {
		return keepScene(ctx, env, db, state, opts, cause, detail, "接线回滚未完成", err)
	} else if removed {
		steps = append(steps, "已撤接线注册行")
	}

	var report *cleanReport
	txErr := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		if report, err = cleanOwnedRows(ctx, tx, state.plan.Name); err != nil {
			return err
		}
		report.LedgerDelete = true
		_, err = gorm.G[host.Plugin](tx).Where("name = ?", state.plan.Name).Delete(ctx)
		return err
	})
	if txErr != nil {
		return keepScene(ctx, env, db, state, opts, cause, detail, "DB 侧归属行删除未完成", txErr)
	}
	steps = append(steps, fmt.Sprintf("已删归属行（rules=%d entities=%d configs=%d 台账=1；角色组悬挂 ID 清理 %d 组 / %d 项）",
		report.Rules, report.Entities, report.Configs, report.Groups, report.DanglingIDs))

	env.Printf("补偿回滚完成（一键撤销到安装前）：\n")
	for _, step := range steps {
		env.Printf("  - %s\n", step)
	}
	detail["compensation"] = steps
	detail["failed_state"] = false
	return auditFailure(ctx, env, db, state.plan.Name, opts, detail, cause)
}

// keepScene 补偿失败：保留现场、状态留 failed（台账行不动），并给出唯一清理入口
func keepScene(ctx context.Context, env *Env, db *gorm.DB, state *installState, opts InstallOptions,
	cause error, detail map[string]any, step string, stepErr error) error {
	env.Printf("✗ 补偿回滚未完成（%s）：%v\n", step, stepErr)
	env.Printf("  ⇒ **保留现场**、状态留 failed；唯一清理入口：`aigo plugin uninstall --force %s`\n", state.plan.Name)
	detail["compensation"] = "incomplete:" + step
	detail["failed_state"] = true
	detail["compensation_error"] = opts.sanitizeInstallText(stepErr.Error())
	return auditFailure(ctx, env, db, state.plan.Name, opts, detail, cause)
}

// auditFailure 写失败审计（写不进去只提示，不掩盖原错误）
func auditFailure(ctx context.Context, env *Env, db *gorm.DB, name string, opts InstallOptions,
	detail map[string]any, cause error) error {
	if err := WriteAudit(ctx, db, name, ActionInstall, opts.Operator, AuditFailed, detail); err != nil {
		env.Printf("（审计未写入：%v）\n", err)
	}
	return cause
}

// baselineAuditDetail 安装审计的 baseline 段（mode/through/探针计数/缺失项/版本表是否本次新建）。
// mode=rejected 不会出现在成功审计里（拒装发生在 ④ 之前、无台账行 ⇒ 同「①–③ 阶段拒装不写审计」口径，
// 已知边界）；这里的 mode 为 none（未声明 adopt）/ adopt / full。
func baselineAuditDetail(outcome *MigrationOutcome) map[string]any {
	mode := baselineModeNone
	detail := map[string]any{
		"mode":                  mode,
		"through":               0,
		"probes_total":          0,
		"probes_hit":            0,
		"missing":               []string{},
		"version_table_created": false,
	}
	if outcome == nil {
		return detail
	}
	if outcome.BaselineMode != "" {
		detail["mode"] = outcome.BaselineMode
	}
	detail["through"] = outcome.DeclaredThrough // 声明值（full 分支如实记声明边界；adopt 分支与 AdoptedThrough 相等）
	detail["probes_total"] = outcome.ProbesTotal
	detail["probes_hit"] = outcome.ProbesHit
	if len(outcome.Missing) > 0 {
		detail["missing"] = outcome.Missing
	}
	detail["version_table_created"] = outcome.Baselined && !outcome.TableExisted
	return detail
}

// adoptedLedgerNote ⑧ 的台账回写提示（未走基线时为空串）
func adoptedLedgerNote(adoptedThrough int) string {
	if adoptedThrough <= 0 {
		return ""
	}
	return fmt.Sprintf("；接管段回写 adopted_through=%d（--purge 据此默认拒绝）", adoptedThrough)
}
