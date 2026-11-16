package cli

import (
	"context"
	"fmt"

	"github.com/ai-go-hub/ai-go-admin/internal/model"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"

	"gorm.io/gorm"
)

// promote 的执行主线（「L2 → L1 升级（promote）必须显式退出 L2」）：
// 预演与门禁在 install.go 的 Promote（同一读数管线）；本文件只做 --yes 之后的写面。

// runPromote 执行 promote（**单事务**：删实体元数据行 → 删公式面菜单/节点 → 角色组悬挂 ID 写回 → 审计）：
// 任一步失败整事务回滚（元数据行与菜单行不会半删）；审计与行删除同事务（行删除前落摘要，
// 同 uninstall e 段的口径）。
func runPromote(ctx context.Context, env *Env, db *gorm.DB, name string, entity host.Entity, removeRows []model.AdminRule, opts PromoteOptions) error {
	ids := make(map[uint]struct{}, len(removeRows))
	ruleIDs := make([]uint, 0, len(removeRows))
	for i := range removeRows {
		ids[removeRows[i].ID] = struct{}{}
		ruleIDs = append(ruleIDs, removeRows[i].ID)
	}

	var removedRules int64
	var groups, dangling int
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		affected, err := gorm.G[host.Entity](tx).Where("plugin = ? AND name = ?", name, entity.Name).Delete(ctx)
		if err != nil {
			return fmt.Errorf("删除实体元数据失败: %w", err)
		}
		if affected == 0 {
			return fmt.Errorf("实体元数据行已不存在（并发变动？）：不猜、不回写（重跑 promote 看现场）")
		}

		if len(ruleIDs) > 0 {
			removed, err := gorm.G[model.AdminRule](tx).Where("plugin = ? AND id IN ?", name, ruleIDs).Delete(ctx)
			if err != nil {
				return fmt.Errorf("删除通用页菜单/节点失败: %w", err)
			}
			removedRules = int64(removed)
			if groups, dangling, err = cleanGroupRuleIDs(ctx, tx, ids); err != nil {
				return err
			}
		}

		return WriteAudit(ctx, tx, name, ActionUpgrade, opts.Operator, AuditSuccess, map[string]any{
			// promote=true 是**安装期所有权检查读取的机器可读结论**（checkEntityOwnership：复装再声明
			// ddl_owner=engine 即拒装）——改这个键名/形态会同步失效那道闸，勿动（双重治理）。
			"promote":       true,
			"entity":        entity.Name,
			"table":         entity.PhysicalTable,
			"entity_rows":   1,
			"rules_deleted": removedRules,
			"groups":        groups,
			"dangling_ids":  dangling,
			"note":          "L2 → L1（promote）：元数据与通用页已退出；表归插件迁移自管（ddl_owner=migrations 口径）；重启后 L2 路由消失",
		})
	})
	if err != nil {
		env.Printf("\n✗ promote 未完成（事务已整体回滚，未写任何行）: %v\n", err)
		return err
	}

	env.Printf("\n✓ promote 完成：%s 的实体 %s 已退出 L2——元数据行 1 删除、公式面菜单/节点 %d 删除、角色组悬挂 ID 清理 %d 组 / %d 项；审计 action=upgrade\n",
		name, entity.Name, removedRules, groups, dangling)
	env.Printf("  生效：重启进程后 L2 路由消失（L1 四层与页面按前置 ① 生成；接线真相在编译产物）\n")
	env.Printf("  表 %s：引擎不再 ALTER/DROP（--purge 也不删它）⇒ DDL 与删除只能由插件迁移承担\n", entity.PhysicalTable)
	return nil
}

// refusePromote 拒绝留痕：--yes 路径上的拒绝写 rejected 审计（含被拒原因与实体名）；预演（无 --yes）
// 不写——预演是「未动任何行」的只读动作（同 uninstall 的 refuseUninstall 口径）。审计写不进去只提示，
// 拒绝本身不受审计可用性影响。
func refusePromote(ctx context.Context, env *Env, db *gorm.DB, name, entity string, opts PromoteOptions, cause error) error {
	if !opts.Yes {
		return cause
	}
	detail := map[string]any{"reason": cause.Error(), "entity": entity}
	if err := WriteAudit(ctx, db, name, ActionUpgrade, opts.Operator, AuditRejected, detail); err != nil {
		env.Printf("（审计未写入：%v）\n", err)
	}
	return cause
}

// placedManifestDeclaresEntity 已落位 manifest 是否仍声明该实体（promote 的提示项：不拒装，
// 只提醒作者同步删 entities 段，否则复装会把元数据写回——）。读不到 manifest ⇒ 不提示。
func placedManifestDeclaresEntity(env *Env, name, entity string) bool {
	m := loadPlacedManifest(env, name)
	if m == nil {
		return false
	}
	for _, item := range m.Entities {
		if item.Entity == entity {
			return true
		}
	}
	return false
}
