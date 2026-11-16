package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"
	"github.com/ai-go-hub/ai-go-admin/pkg/airx"

	"gorm.io/gorm"
)

// UninstallSummary 卸载读数（审计 detail 与收尾提示）
type UninstallSummary struct {
	// Purged 是否执行了 --purge
	Purged bool
	// RolledVersions --purge 回滚的迁移版本数
	RolledVersions int
	// DroppedTables drop 的引擎侧 L2 表
	DroppedTables []string
	// KeptTables 保留的 L2 表（未 --purge，或 ddl_owner=migrations 由插件迁移自管）
	KeptTables []string
	// RemovedFiles 删除的落位路径
	RemovedFiles []string
	// KeptFiles 是否跳过文件删除（--keep-files）
	KeptFiles bool
	// PurgeAdopted 是否给了第三重显式确认（--purge-adopted；只对 adopt 装入的插件有语义）
	PurgeAdopted bool
	// Rules / Entities / Configs / Groups / DanglingIDs DB 侧读数
	Rules, Entities, Configs int64
	Groups, DanglingIDs      int
	// Ledger 台账行摘要（删除前落审计）
	Ledger host.Plugin
}

// runUninstall 卸载执行主线（固定顺序 a–e；破坏性步骤在前，因为 down 文件随后会被删）：
//
//	a)【仅 --purge】按 down 逆序回滚插件迁移（读 plugins/<名>/migrations）→ drop 迁移版本表 → drop L2 表
//	b) 删落位物：plugins/<名>/** + 落位副本（view/语言包）
//	c) 删 DB 侧：归属行与菜单/节点（含各角色组 rules CSV 的悬挂 ID 写回清除）+ 实体元数据 + configs 分组
//	d) 撤 register_gen.go 注册行
//	e) 审计：写 admin_plugin_actions（含台账摘要）→ 删台账行（同一事务，摘要先落）
func runUninstall(ctx context.Context, env *Env, name string, row host.Plugin, opts UninstallOptions) error {
	db, err := env.EnsureDB(ctx)
	if err != nil {
		return err
	}
	summary := &UninstallSummary{Purged: opts.Purge, KeptFiles: opts.KeepFiles, PurgeAdopted: opts.PurgeAdopted, Ledger: row}
	m := loadPlacedManifest(env, name)

	// a)【仅 --purge】迁移回滚 → drop 版本表 → drop L2 表
	if opts.Purge {
		if err := purgePlugin(ctx, env, db, name, m, summary); err != nil {
			return err
		}
	}

	// b) 删落位物（airx.Pause 包住：删到一半被 air 重启打断会留半删现场）
	if !opts.KeepFiles {
		var removeErr error
		func() {
			airx.Pause()
			defer airx.Resume()
			removeErr = removePlacedFiles(env, name, summary)
		}()
		if removeErr != nil {
			return reportUninstallFailure(ctx, env, db, name, opts, summary, "b) 删落位物", removeErr)
		}
	}

	// c) 删 DB 侧（归属行整树 + 角色组悬挂 ID + 实体元数据 + configs 分组）
	report := &cleanReport{}
	if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var txErr error
		report, txErr = cleanOwnedRows(ctx, tx, name)
		return txErr
	}); err != nil {
		return reportUninstallFailure(ctx, env, db, name, opts, summary, "c) 删 DB 侧", err)
	}
	summary.Rules, summary.Entities, summary.Configs = report.Rules, report.Entities, report.Configs
	summary.Groups, summary.DanglingIDs = report.Groups, report.DanglingIDs
	env.Printf("c) 删 DB 侧：归属行 %d（含子树）+ 角色组悬挂 ID 清理 %d 组 / %d 项；实体元数据 %d；configs 分组 %d 行\n",
		report.Rules, report.Groups, report.DanglingIDs, report.Entities, report.Configs)

	// d) 撤注册行
	diff, removed, err := unwirePlugin(env, name)
	if err != nil {
		return reportUninstallFailure(ctx, env, db, name, opts, summary, "d) 撤接线注册行", err)
	}
	if removed {
		env.Printf("d) 撤接线注册行（%s）：\n%s", RegistryFile, diff)
	} else {
		env.Printf("d) 撤接线注册行：注册文件里没有 %s 的行（无需改动；declarative 插件本无注册行）\n", name)
	}

	// e) 审计（含台账摘要）+ 删台账行（同一事务：先写审计、后删行——「行删除前把摘要写进审计表」）
	detail := uninstallAuditDetail(summary)
	if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := WriteAudit(ctx, tx, name, ActionUninstall, opts.Operator, AuditSuccess, detail); err != nil {
			return err
		}
		if _, err := gorm.G[host.Plugin](tx).Where("name = ?", name).Delete(ctx); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return fmt.Errorf("e) 审计/台账删除失败（行仍在：重跑 uninstall 可续；插件归属行已清）: %w", err)
	}
	env.Printf("e) 审计：写 admin_plugin_actions（action=uninstall，含台账摘要）并删除台账行\n")

	env.Printf("\n✓ 已卸载：%s（原状态 %s）\n", name, row.Status)
	keptLabel := ""
	if opts.Purge {
		keptLabel = "（--purge 已删除）"
	}
	env.Printf("  保留项：表与数据（未 --purge 时）、插件迁移版本表%s、plugins/.store/%s-*.zip（本地复装凭据）\n",
		keptLabel, name)
	if len(summary.KeptTables) > 0 {
		env.Printf("  保留的实体表（ddl_owner=migrations 由插件迁移自管，或未 --purge）：%s\n", strings.Join(summary.KeptTables, ", "))
	}
	if summary.Ledger.AdoptedThrough > 0 {
		env.Printf("  接管段：adopted_through=%d（存量库迁移基线装入；1..%d 与核心链对应迁移同物）——"+
			"表与数据、版本表默认保留；要 --purge 需三重显式 --purge --yes --purge-adopted\n",
			summary.Ledger.AdoptedThrough, summary.Ledger.AdoptedThrough)
	}
	env.Printf("  复装不是还原：节点 ID 已变，权限需重新勾选（脚本只清悬挂项，不恢复授权关系）\n")
	if !opts.KeepFiles {
		env.Printf("  git 视角：入库仓里表现为本插件路径的删除项（plugins/%s/**、web/src/views/admin/plugin/%s/**、web/src/lang/*/plugin/%s.yaml）——一次可提交的下架\n",
			name, name, name)
	}
	return nil
}

// purgePlugin a) 步骤：回滚插件迁移 → drop 版本表 → drop L2 表（引擎侧的；ddl_owner=migrations 由迁移自管）。
// 迁移目录缺失时：**无版本表**则跳过（本就无迁移可回滚），**有版本表**则拒绝（无法按 down 逆序回滚，不猜）。
func purgePlugin(ctx context.Context, env *Env, db *gorm.DB, name string, m *manifest.Manifest, summary *UninstallSummary) error {
	migrationsPath := MigrationsPath(name, m)
	full := env.Abs(migrationsPath)
	table := MigrationTableName(name, env.Prefix)
	tableExists, err := migrationTableExists(ctx, db, table)
	if err != nil {
		return fmt.Errorf("a) --purge 读取版本表状态失败: %w", err)
	}

	if info, statErr := os.Stat(full); statErr != nil || !info.IsDir() {
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("a) --purge 检查迁移目录 %s 失败: %w", migrationsPath, statErr)
		}
		if tableExists {
			return fmt.Errorf("a) --purge 无法回滚迁移：迁移目录 %s 缺失而版本表 %s 存在"+
				"（无 down 文件可回滚，不猜）——先恢复插件源码再 --purge，或改用不带 --purge 的卸载", migrationsPath, table)
		}
		env.Printf("a) 迁移：无迁移目录（%s）且无版本表 %s ⇒ 跳过回滚与 drop\n", migrationsPath, table)
	} else {
		rolled, err := PurgePluginMigrations(ctx, env, name, full)
		if err != nil {
			return fmt.Errorf("a) --purge 回滚插件迁移失败: %w", err)
		}
		summary.RolledVersions = rolled
	}

	entities, err := gorm.G[host.Entity](db).Where("plugin = ?", name).Find(ctx)
	if err != nil {
		return fmt.Errorf("读取实体元数据失败: %w", err)
	}
	for i := range entities {
		tableName := entities[i].PhysicalTable
		if entityDDLOwner(entities[i].Spec) == "migrations" {
			summary.KeptTables = append(summary.KeptTables, tableName+"（ddl_owner=migrations：结构由插件迁移自管，引擎不重复 DROP）")
			continue
		}
		if err := DropPluginTable(ctx, db, tableName); err != nil {
			return fmt.Errorf("a) drop L2 实体表失败: %w", err)
		}
		summary.DroppedTables = append(summary.DroppedTables, tableName)
		env.Printf("a) drop L2 实体表 %s\n", tableName)
	}
	if len(entities) == 0 {
		env.Printf("a) 无 L2 实体元数据行：无引擎侧的表可 drop\n")
	}
	return nil
}

// entityDDLOwner 从 spec JSON 读 ddl_owner（缺省 engine）
func entityDDLOwner(spec []byte) string {
	var raw struct {
		DdlOwner string `json:"ddl_owner"`
	}
	if err := json.Unmarshal(spec, &raw); err != nil || strings.TrimSpace(raw.DdlOwner) == "" {
		return "engine"
	}
	return raw.DdlOwner
}

// removePlacedFiles b) 步骤：删插件归属的文件（整目录 + 语言包），并清空由此产生的空目录。
// 归属依据是**目录**（plugins/<名>/、views/admin/plugin/<名>/）与固定映射（lang/*/plugin/<名>.yaml），
// 不扫描文件头标记——标记是留痕，不是删除依据。
func removePlacedFiles(env *Env, name string, summary *UninstallSummary) error {
	targets := []string{PluginDir(name), ViewTarget(name)}
	langFiles := LangTargetsOf(env, name)

	failures := make([]string, 0, len(targets)+len(langFiles))
	for _, target := range targets {
		removed, err := env.RemoveTree(target)
		if err != nil {
			failures = append(failures, err.Error())
			continue
		}
		if removed {
			summary.RemovedFiles = append(summary.RemovedFiles, target+"/**")
		}
	}
	for _, langFile := range langFiles {
		removed, err := env.RemoveFile(langFile)
		if err != nil {
			failures = append(failures, err.Error())
			continue
		}
		if removed {
			summary.RemovedFiles = append(summary.RemovedFiles, langFile)
		}
	}
	// 空目录清理：停在落位根的上一级，不动宿主目录结构
	env.PruneEmptyDir(ViewTarget(name), WebViewsRoot, path.Dir(WebViewsRoot))
	for _, langFile := range langFiles {
		langDir := path.Dir(langFile) // web/src/lang/<语言>/plugin
		env.PruneEmptyDir(langDir, WebLangRoot, path.Join(WebLangRoot, path.Base(path.Dir(langDir))))
	}
	if len(summary.RemovedFiles) == 0 {
		env.Printf("b) 删落位物：无（源码与落位副本本就不在）\n")
	} else {
		env.Printf("b) 删落位物：%s\n", strings.Join(summary.RemovedFiles, "、"))
	}
	if len(failures) > 0 {
		return fmt.Errorf("删除未完成：\n  %s", strings.Join(failures, "\n  "))
	}
	return nil
}

// LangTargetsOf 该插件在各语言下的语言包落位目标（glob 全部语言目录，覆盖 zh-cn/en 之外的语种）
func LangTargetsOf(env *Env, name string) []string {
	pattern := env.Abs(path.Join(WebLangRoot, "*", "plugin", name+".yaml"))
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		out = append(out, relPathOf(env.Root, match))
	}
	return out
}

// reportUninstallFailure 卸载中途失败：如实报告已做/未做的步骤，并尽力写失败审计
func reportUninstallFailure(ctx context.Context, env *Env, db *gorm.DB, name string, opts UninstallOptions,
	summary *UninstallSummary, step string, cause error) error {
	env.Printf("✗ %s 失败：%v\n", step, cause)
	env.Printf("  提示：卸载不设补偿回滚（破坏性动作），步骤可从断点续跑（重跑 `aigo plugin uninstall %s` 会按同顺序继续）\n", name)
	if err := WriteAudit(ctx, db, name, ActionUninstall, opts.Operator, AuditFailed, uninstallFailureDetail(env, summary, step, cause)); err != nil {
		env.Printf("（审计未写入：%v）\n", err)
	}
	return cause
}

// uninstallFailureDetail 卸载失败审计的 detail（台账摘要 + 失败步骤 + 失败原因）。
//
// error 走 SanitizePluginText（与 catalog 通道的 sanitizeCatalogText 同一实现）：删落位物/删 DB 侧失败
// 的原因会带落位目标与仓库根的绝对路径，而该字符串会在插件管理页的动作审计区块展示（第 7 条）。
// 抽成函数是为了让「脱敏确实作用在落库的 detail 上」有一条不依赖数据库的断言（与 install 侧的
// failureAuditDetail 同口径，见 install_options_test.go）。
func uninstallFailureDetail(env *Env, summary *UninstallSummary, step string, cause error) map[string]any {
	detail := uninstallAuditDetail(summary)
	detail["error"] = SanitizePluginText(env, cause.Error())
	detail["failed_step"] = step
	return detail
}

// uninstallAuditDetail 卸载审计详情（台账摘要 + 各面读数；台账行删除前落库）
func uninstallAuditDetail(summary *UninstallSummary) map[string]any {
	detail := map[string]any{
		"purge":           summary.Purged,
		"purge_adopted":   summary.PurgeAdopted,
		"adopted_through": summary.Ledger.AdoptedThrough, // 台账行随卸载删除 ⇒ 这里是 adopt 装入事实的最后留痕
		"keep_files":      summary.KeptFiles,
		"version":         summary.Ledger.Version,
		"kind":            summary.Ledger.Kind,
		"status":          summary.Ledger.Status,
		"installed_at":    formatTime(summary.Ledger.InstalledAt),
		"removed_files":   summary.RemovedFiles,
		"rolled_versions": summary.RolledVersions,
		"dropped_tables":  summary.DroppedTables,
		"kept_tables":     summary.KeptTables,
		"rules":           summary.Rules,
		"entities":        summary.Entities,
		"configs":         summary.Configs,
		"groups":          summary.Groups,
		"dangling_ids":    summary.DanglingIDs,
	}
	return detail
}

// formatTime 时间展示（nil ⇒ 空串）
func formatTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.RFC3339)
}

// loadPlacedManifest 读已落位插件的 manifest（卸载期用它定位迁移目录与语言包；缺失 ⇒ nil，
// 由调用方退化为按固定映射清理）
func loadPlacedManifest(env *Env, name string) *manifest.Manifest {
	dir := env.Abs(PluginDir(name))
	m, _, err := manifest.Load(dir, manifest.Options{Prefix: env.Prefix, HostVersion: env.HostVersion})
	if err != nil {
		return nil
	}
	return m
}
