package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/l2/ddlplan"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"

	"gorm.io/gorm"
)

// L2 实体结构（DDL）的安装链接入（「演进与 DDL 执行」/）：
//
//   - 装（新实体）：引擎 BuildPlan → ApplyPlan，逐条独立事务、**失败即停并逐条回报**（不吞错）；
//   - 升级/复装：基线是**表结构自省**（引擎内建，l2/ddl.go），spec 是目标态 ⇒ 复装新版自动补齐新列、
//     已列一律保留；删列/改类型/改列名被引擎拒绝（提示升 mixed/L1 用迁移文件）；
//   - ddl_owner=migrations：引擎不做任何 ALTER/DROP（表结构与删除全由插件迁移承担）；
//   - dry-run：同一 BuildPlan 的读数在 ⑤ 段逐条列出（只读，不执行；库不可用且确有引擎自管实体 ⇒ fail-closed）；
//   - 与 --purge 对称：卸载 --purge 只 drop ddl_owner=engine 的表（uninstall_run.go 的 purgePlugin）；
//     安装失败的补偿回滚只 drop「本次安装新建」的表（既有表与数据一律不动——与卸载的保留口径一致）。
//
// 引擎经 **internal/plugin/l2/ddlplan** 的接口面接入（叶子包）：cli 不能直接 import
// internal/plugin/l2——l2 的演示用例 import 本包，反向依赖会构成 import cycle not allowed in test
// （ddlplan 的包注释是这条约束的唯一说明处）。

// EntityDDL 一个 L2 实体的结构计划读数（dry-run ⑤ 段 / ④b 执行段 / 审计共用同一读数）
type EntityDDL struct {
	// Entity 实体名
	Entity string
	// Table 物理表名
	Table string
	// DDLOwner engine | migrations
	DDLOwner string
	// Existing 计划构建时表是否已存在（false ⇒ 计划是建表；true ⇒ 自省基线的差异计划）
	Existing bool
	// Plan 引擎生成的结构计划（ddl_owner=engine 时非空）
	Plan *ddlplan.Plan
	// Results 逐条执行回报（仅执行主线填；dry-run 为空）
	Results []ddlplan.Result
	// Created 本次安装**新建**的表（补偿回滚的 drop 名单；既有表恒 false）
	Created bool
	// Note 非引擎自管实体的说明（ddl_owner=migrations）
	Note string
	// Warnings CLI 侧的所有权告警（既有表接管提示等；与引擎的 Plan.Warnings 分列，渲染与审计逐条上报）
	Warnings []string
	// Err 该实体的计划/执行错误（dry-run 一次报全，不吞错）
	Err error
}

// engineOwnedCount 清单里由引擎管结构的实体数（ddl_owner 缺省即 engine）
func engineOwnedCount(entities []manifest.Entity) int {
	count := 0
	for _, entity := range entities {
		if entityOwnedByEngine(entity) {
			count++
		}
	}
	return count
}

// requirePlanner 取引擎的结构计划实现；未接入 ⇒ fail-closed（不静默跳过：跳过了等于「装成功但表没建」）
func requirePlanner() (ddlplan.Planner, error) {
	planner := ddlplan.Get()
	if planner == nil {
		return nil, fmt.Errorf("L2 引擎未接入本二进制（internal/plugin/l2 未链接）：无法生成/执行实体结构 DDL——" +
			"本命令必须与宿主同源构建（cmd/main.go → internal/plugins → l2 的接线）")
	}
	return planner, nil
}

// ownershipTakeoverNote 既有表的接管提示（design「升级/复装的基线」与「promote 必须显式退出 L2」）：
// 表已存在时引擎按自省基线接管其列/索引（--purge 也会 DROP 它）——若该表已 promote 移交 L1/迁移自管，
// manifest 必须把该实体声明为 ddl_owner: migrations（或从 entities 段移除），否则同一张表双重治理。
func ownershipTakeoverNote(table string) string {
	return "既有表 " + table + "：引擎将按自省基线接管其列/索引（--purge 也会 DROP 它）；" +
		"若该表已 promote 给 L1/迁移自管，必须把该实体声明为 ddl_owner: migrations（或从 entities 段移除），" +
		"否则同一张表被 L2 引擎与 L1 四层双重治理"
}

// mechanismTableExists 机制表存在性（information_schema 探测；不依赖机制表自身，恒可用）。
// 表名口径：机制模型都实现 Tabler（`TableName()` 逐字返回、**不含前缀**）⇒ GORM 侧读写用的表名不带
// 前缀；而迁移建表带 `__PREFIX__`（默认前缀为空 ⇒ 两者一致）。故两种形态都探一遍，任一存在即继续；
// 避免在前缀非空的部署里把检查静默跳过（fail-open）。真正的 promote 查询仍走机制自身的 GORM 路径
// （gorm.G[PluginAction]），与审计写入（WriteAudit）落在同一张表。
func mechanismTableExists(ctx context.Context, db *gorm.DB, bare, prefixed string) (bool, error) {
	var count int64
	err := db.WithContext(ctx).Raw(
		"SELECT COUNT(*) AS count FROM information_schema.tables WHERE table_schema = current_schema() AND table_name IN (?, ?)",
		bare, prefixed).Scan(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// checkEntityOwnership 引擎自管实体的所有权核对（④ 前置预检与 ④b 执行共用；「promote 必须显式退出 L2」）：
//
//   - 既有表（existing）：返回接管提示（复装/升级路径保持可用 ——明定「复装/升级以表结构自省为基线」，
//     故**不因表已存在而拒装**；但必须让「引擎重新接管 ALTER/--purge DROP 权」这件事被逐条看见）；
//   - promote 记录：审计里存在该实体的 promote 记录（action=upgrade + detail.promote=true + detail.entity）
//     ⇒ **拒装**——该实体已显式退出 L2，复装再声明 ddl_owner=engine 会把引擎的 ALTER 与 DROP 权重新挂回
//     一张（可能已含业务数据的）表，正是明令禁止的双重治理；修复路径 = 实体从 entities 段移除（L1 形态）
//     或声明 ddl_owner: migrations（表结构与删除由插件迁移承担）；
//   - 机制表未迁移（探测不到 admin_plugin_actions）⇒ 无历史可查，只给既有表提示（④ 写库自会报缺表）；
//     机制表在而读取失败 ⇒ fail-closed 拒装（读不到就不猜）。
//
// SQL 量级：information_schema 探测 1 次 + 审计过滤走 (plugin, created_at) 部分索引的前缀列（插件级少量行），
// jsonb 明细过滤只作用于这些行 ⇒ 安装（非请求路径）可忽略。
func checkEntityOwnership(ctx context.Context, env *Env, db *gorm.DB, plugin string, entity manifest.Entity, table string, existing bool) ([]string, error) {
	var warnings []string
	if existing {
		warnings = append(warnings, ownershipTakeoverNote(table))
	}

	hasTable, err := mechanismTableExists(ctx, db, "admin_plugin_actions", env.Prefix+"admin_plugin_actions")
	if err != nil {
		return warnings, fmt.Errorf("所有权检查失败（探测审计表）: %w", err)
	}
	if !hasTable {
		return warnings, nil // 机制表未迁移：无历史可查（安装 ④ 自会报缺表）
	}
	row, err := gorm.G[PluginAction](db).
		Where("plugin = ? AND action = ? AND detail->>'promote' = 'true' AND detail->>'entity' = ?",
			plugin, ActionUpgrade, entity.Entity).
		Order("id DESC").First(ctx)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return warnings, nil
	}
	if err != nil {
		return warnings, fmt.Errorf("所有权检查失败（读取 promote 记录；若机制表缺失或前缀口径不符，先 `aigo migrate up` 核对）: %w", err)
	}
	return warnings, fmt.Errorf("实体 %s 的表 %s 此前已经 promote 移交 L1/迁移自管（promote 审计记录 %s）："+
		"复装声明的 ddl_owner=engine 会把 L2 引擎的 ALTER 与 --purge 的 DROP 重新挂回该表（禁止同一张表双重治理）——"+
		"修复：把该实体从 entities 段移除（L1 形态）或声明 ddl_owner: migrations（表结构与删除由插件迁移承担）；"+
		"审计表是 append-only 的账，不构成可清理的开关",
		entity.Entity, table, row.CreatedAt.Format(time.RFC3339))
}

// previewEntityDDL ④ 之前的只读预检：生成全部实体的结构计划读数（dry-run 的 ⑤ 段与 ④b 执行主线共用同一管线）。
//
// fail-closed 边界（「A 的每一步都要能在 dry-run 中预演」）：存在引擎自管实体而库不可用时，
// 无法对表结构自省 ⇒ 不能预演 DDL ⇒ 返回错误（不假装预演过；真实安装本来也需要库，见 ④）。
// 只有 ddl_owner=migrations 的实体（或无实体）时，引擎本就不做 DDL ⇒ 不碰库。
func previewEntityDDL(ctx context.Context, env *Env, plan *InstallPlan) ([]EntityDDL, error) {
	entities := plan.Manifest.Entities
	reads := make([]EntityDDL, 0, len(entities))
	owned := engineOwnedCount(entities)
	if owned == 0 {
		for _, entity := range entities {
			reads = append(reads, EntityDDL{
				Entity: entity.Entity, DDLOwner: ddlOwnerOf(entity),
				Table: PhysicalEntityTable(env.Prefix, plan.Name, entity.Entity),
				Note:  migrationsOwnedNote,
			})
		}
		return reads, nil
	}

	planner, err := requirePlanner()
	if err != nil {
		return nil, err
	}
	db, err := env.EnsureDB(ctx)
	if err != nil {
		return nil, fmt.Errorf("L2 实体结构（DDL）的逐条预演需要数据库连接（%d 个 ddl_owner=engine 的实体要读实际表结构做差异基线）: %w",
			owned, err)
	}
	return buildEntityDDLPlan(ctx, env, db, plan, planner, reads)
}

// buildEntityDDLPlan 逐实体生成结构计划（本函数不做任何 DDL 执行）
func buildEntityDDLPlan(ctx context.Context, env *Env, db *gorm.DB, plan *InstallPlan,
	planner ddlplan.Planner, reads []EntityDDL) ([]EntityDDL, error) {
	failures := make([]string, 0)
	for _, entity := range plan.Manifest.Entities {
		read := EntityDDL{
			Entity: entity.Entity, DDLOwner: ddlOwnerOf(entity),
			Table: PhysicalEntityTable(env.Prefix, plan.Name, entity.Entity),
		}
		if !entityOwnedByEngine(entity) {
			read.Note = migrationsOwnedNote
			reads = append(reads, read)
			continue
		}
		entityPlan, err := planner.Build(ctx, db, plan.Name, entity, read.Table)
		read.Plan = entityPlan
		if entityPlan != nil {
			read.Existing = entityPlan.Existing
			if entityPlan.Table != "" {
				read.Table = entityPlan.Table
			}
		}
		if err != nil {
			read.Err = err
			failures = append(failures, err.Error())
		}
		// 所有权核对（既有表提示 + promote 记录拒装；与计划错误并列上报，不互相掩盖）
		warnings, refuse := checkEntityOwnership(ctx, env, db, plan.Name, entity, read.Table, read.Existing)
		read.Warnings = warnings
		if refuse != nil {
			if read.Err == nil {
				read.Err = refuse
			}
			failures = append(failures, refuse.Error())
		}
		reads = append(reads, read)
	}
	if len(failures) > 0 {
		return reads, fmt.Errorf("L2 实体结构计划被拒（不执行、不落盘）: %s", strings.Join(failures, "; "))
	}
	return reads, nil
}

// entityDDLApply ④b 的执行读数（补偿回滚与审计共用）
type entityDDLApply struct {
	// Reads 逐实体的计划与逐条执行回报
	Reads []EntityDDL
	// Created 本次新建的表（补偿回滚只 drop 它们；既有表一律不动）
	Created []string
}

// appliedStatements 全部执行过的语句数
func (a *entityDDLApply) appliedStatements() int {
	if a == nil {
		return 0
	}
	count := 0
	for i := range a.Reads {
		count += len(a.Reads[i].Results)
	}
	return count
}

// applyEntityDDL ④b 执行主线：对每个实体重复 BuildPlan → ApplyPlan（装/升级/复装同一管线：
// 装 = CREATE TABLE IF NOT EXISTS + 索引一次到位 + COMMENT ON；既有表 = 自省基线的加列 /
// 放宽默认值（DROP DEFAULT）/ 补索引；改类型、改长度与改列名一律拒绝，删列只报告；
// 拒绝项 ⇒ 计划本身报错，失败即停并逐条回报，不吞错）。
//
// 返回的读数在失败时同样有效：Created 记录「本次新建的表」——ApplyPlan 逐条执行，建表成功而后续
// 索引/注释失败时表已存在，补偿回滚据此 drop（既有表绝不 drop，见 failInstall）。
func applyEntityDDL(ctx context.Context, env *Env, db *gorm.DB, plan *InstallPlan) (*entityDDLApply, error) {
	apply := &entityDDLApply{Reads: make([]EntityDDL, 0, len(plan.Manifest.Entities))}
	planner, err := requirePlanner()
	if err != nil {
		return apply, err
	}
	for _, entity := range plan.Manifest.Entities {
		read := EntityDDL{
			Entity: entity.Entity, DDLOwner: ddlOwnerOf(entity),
			Table: PhysicalEntityTable(env.Prefix, plan.Name, entity.Entity),
		}
		if !entityOwnedByEngine(entity) {
			read.Note = migrationsOwnedNote
			apply.Reads = append(apply.Reads, read)
			env.Printf("④b DDL：实体 %s（表 %s）%s——跳过（结构由插件迁移承担）\n", entity.Entity, read.Table, migrationsOwnedNote)
			continue
		}

		entityPlan, buildErr := planner.Build(ctx, db, plan.Name, entity, read.Table)
		read.Plan = entityPlan
		if entityPlan != nil {
			read.Existing = entityPlan.Existing
			if entityPlan.Table != "" {
				read.Table = entityPlan.Table
			}
		}
		if buildErr != nil {
			read.Err = buildErr
			apply.Reads = append(apply.Reads, read)
			return apply, fmt.Errorf("实体 %s 的结构计划被拒（不执行）: %w", entity.Entity, buildErr)
		}
		if entityPlan == nil {
			// 实现契约：成功路径恒返回计划（引擎侧 BuildPlan 的成功分支都 return plan, nil）⇒ 空计划按失败处理
			// （fail-closed）：既不把 nil 传进 Apply（那会在引擎里空指针），也不让它流进渲染或审计
			empty := fmt.Errorf("引擎返回空的结构计划（nil）：按实现契约不存在此形态，拒绝继续（fail-closed）")
			read.Err = empty
			apply.Reads = append(apply.Reads, read)
			return apply, fmt.Errorf("实体 %s 的结构计划为空: %w", entity.Entity, empty)
		}

		// 所有权核对的执行侧兜底（④ 前置已拒装；此处再核一次，保证任何入口都不会对「已 promote 的表」执行 ALTER）。
		warnings, refuse := checkEntityOwnership(ctx, env, db, plan.Name, entity, read.Table, read.Existing)
		read.Warnings = warnings
		if refuse != nil {
			read.Err = refuse
			apply.Reads = append(apply.Reads, read)
			return apply, fmt.Errorf("实体 %s 的所有权检查未通过（不执行 DDL）: %w", entity.Entity, refuse)
		}

		results, applyErr := planner.Apply(ctx, db, entityPlan)
		read.Results = results
		if !entityPlan.Existing {
			read.Created = true
			apply.Created = append(apply.Created, entityPlan.Table)
		}
		apply.Reads = append(apply.Reads, read)
		reportEntityDDL(env, read)
		if applyErr != nil {
			return apply, fmt.Errorf("实体 %s 的 DDL 执行失败（已应用 %d/%d 条）: %w",
				entity.Entity, len(results), len(entityPlan.Statements), applyErr)
		}
	}
	return apply, nil
}

// reportEntityDDL 逐条回报一个实体的 DDL 结果（失败的那条同时打印错误；「不吞错」）。
// 无计划（nil）时也要把 Err 打出来：那条路径上调用方随后会以「④b DDL 失败」整体报错，
// 这里先给出实体级读数，便于定位是 spec 非法还是表结构变更被拒。
func reportEntityDDL(env *Env, read EntityDDL) {
	if read.Plan == nil {
		if read.Err != nil {
			env.Printf("④b DDL：实体 %s：✗ %s\n", read.Entity, read.Err.Error())
		}
		return
	}
	scope := "既有表（自省基线）"
	if !read.Existing {
		scope = "新建表"
	}
	env.Printf("④b DDL：实体 %s → 表 %s（%s，计划 %d 条，执行 %d 条）\n",
		read.Entity, read.Table, scope, len(read.Plan.Statements), len(read.Results))
	for _, result := range read.Results {
		mark, detail := "✓", ""
		if result.Error != "" {
			mark, detail = "✗", "  ← "+result.Error
		}
		env.Printf("  %s [%s] %s%s\n", mark, result.Kind, firstLineOf(result.Text), detail)
	}
	for _, warning := range read.Warnings {
		env.Printf("  ⚠ %s\n", warning)
	}
	for _, warning := range read.Plan.Warnings {
		env.Printf("  ⚠ %s\n", warning)
	}
}

// firstLineOf 多行 DDL 的单行摘要（建表语句是多行的，逐条回报取首行即可定位）
func firstLineOf(text string) string {
	if idx := strings.IndexByte(text, '\n'); idx >= 0 {
		return strings.TrimSpace(text[:idx]) + " …"
	}
	return strings.TrimSpace(text)
}

// ddlAuditDetail 审计 detail 的结构面读数（逐实体：表/owner/是否既有/语句数/失败）
func ddlAuditDetail(apply *entityDDLApply) []map[string]any {
	if apply == nil {
		return nil
	}
	items := make([]map[string]any, 0, len(apply.Reads))
	for _, read := range apply.Reads {
		item := map[string]any{
			"entity":   read.Entity,
			"owner":    read.DDLOwner,
			"table":    read.Table,
			"existing": read.Existing,
		}
		if read.Plan != nil {
			item["statements"] = len(read.Plan.Statements)
		}
		item["applied"] = len(read.Results)
		if len(read.Warnings) > 0 {
			item["warnings"] = read.Warnings
		}
		if read.Created {
			item["created"] = true
		}
		if read.Err != nil {
			item["error"] = read.Err.Error()
		}
		items = append(items, item)
	}
	return items
}

// dropInstallCreatedTables 补偿回滚的 DDL 段：只 drop「本次安装新建」的表（apply.Created）。
// 既有表（复装/升级路径）一律不动——加列无数据损失且重试幂等（与卸载默认保留表同口径）。
func dropInstallCreatedTables(ctx context.Context, db *gorm.DB, apply *entityDDLApply) ([]string, error) {
	if apply == nil || len(apply.Created) == 0 {
		return nil, nil
	}
	dropped := make([]string, 0, len(apply.Created))
	for _, table := range apply.Created {
		if err := DropPluginTable(ctx, db, table); err != nil {
			return dropped, err
		}
		dropped = append(dropped, table)
	}
	return dropped, nil
}
