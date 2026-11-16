// Package plugin 是插件管理面（P2）的数据访问层：表 admin_plugins / admin_plugin_entities /
// admin_plugin_actions 的读单点。
//
// 写路径不在本包：启用/停用的状态机（台账 status + 菜单/节点 status + 审计）由
// internal/plugin/cli 的 Enable/Disable 单点实现，管理页与 CLI 复用同一份（
// 「UI 与 CLI 同一份留痕」）——本包只读，避免出现第二套状态机。
package plugin

import (
	"context"

	"github.com/ai-go-hub/ai-go-admin/internal/model"
	pluginCli "github.com/ai-go-hub/ai-go-admin/internal/plugin/cli"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"
	"github.com/ai-go-hub/ai-go-admin/internal/repository"

	"gorm.io/gorm"
)

// PluginEntity / PluginAction 是表 admin_plugin_entities / admin_plugin_actions 的行类型别名：
// 两者的唯一定义在宿主基座（P1）的 host.Entity 与 cli.PluginAction，此处**别名复用**而非另立一份
// （host/model.go 与 cli/lifecycle.go 的注释同款：P2 的四层管理面直接别名复用 P1 类型）。
// 别名落本包而非 internal/model：cli 已 import model，model → cli 会成环。
type (
	// PluginEntity L2 实体元数据行（表 admin_plugin_entities）
	PluginEntity = host.Entity
	// PluginAction 插件动作审计行（表 admin_plugin_actions，append-only）
	PluginAction = pluginCli.PluginAction
)

// 审计分页上限（SQL 纪律：列表必须分页且有上限；复用宿主 100 的口径）
const (
	auditDefaultLimit = 20
	auditMaxLimit     = 100
)

// Repository 插件台账仓储，嵌入通用仓储（List/Count/Get 等走泛型基类）
type Repository struct {
	*repository.Repository[model.Plugin]
}

// NewRepository 创建插件台账仓储实例
func NewRepository() *Repository {
	return &Repository{
		Repository: repository.NewRepository[model.Plugin](),
	}
}

// FindByName 按插件名取台账行（admin_plugins.name 唯一索引的单行读，「按请求读库」同款）
func (r *Repository) FindByName(ctx context.Context, name string) (*model.Plugin, error) {
	row, err := gorm.G[model.Plugin](r.DB()).Where("name = ?", name).First(ctx)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ListNames 全部插件名（升序）。「接线悬空」（进程内有、台账无）判定用：
// 只取 name 列，小表等值面，不读其余列。
func (r *Repository) ListNames(ctx context.Context) ([]string, error) {
	rows, err := gorm.G[model.Plugin](r.DB()).Select("name").Order("name").Find(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(rows))
	for i := range rows {
		names = append(names, rows[i].Name)
	}
	return names, nil
}

// EntityCountsByPlugin 各插件的实体行数（管理页「实体数」列）。
// 元数据表与插件数量同数量级（个位到十位行），只取 plugin 列在内存里计数——
// 比 GROUP BY（Generics API 的 Select 对表达式引用有额外约定）更直白，也不引入第二处 SQL 形态。
func (r *Repository) EntityCountsByPlugin(ctx context.Context) (map[string]int, error) {
	rows, err := gorm.G[PluginEntity](r.DB()).Select("plugin").Find(ctx)
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int, len(rows))
	for i := range rows {
		counts[rows[i].Plugin]++
	}
	return counts, nil
}

// ListEntities 单插件的实体元数据行（按 name 升序）。
// **不含 spec（JSONB）**：详情面板只需实体名与物理表名，大字段不进列表（SQL 纪律）。
func (r *Repository) ListEntities(ctx context.Context, plugin string) ([]PluginEntity, error) {
	return gorm.G[PluginEntity](r.DB()).
		Select("id", "plugin", "name", "title_zh", "title_en", "table_name", "created_at", "updated_at").
		Where("plugin = ?", plugin).
		Order("name").
		Find(ctx)
}

// ListActions 单插件的动作审计（created_at DESC, id DESC；走 idx_..._plugin_created 索引）。
//
// detail（JSONB）**随行返回**：本面板就是为「为什么装/停失败」而设，detail 是答案本身；
// 收敛面在别处保证——显式 Select（不 SELECT *）、每页 ≤100（auditMaxLimit）、
// 索引 (plugin, created_at DESC) 定扫描面（审计落点）。
func (r *Repository) ListActions(ctx context.Context, plugin string, page, limit int) ([]PluginAction, error) {
	page, limit = NormalizeAuditPage(page, limit)
	return gorm.G[PluginAction](r.DB()).
		Select("id", "plugin", "action", "operator", "status", "detail", "created_at").
		Where("plugin = ?", plugin).
		Order("created_at DESC, id DESC").
		Limit(limit).
		Offset((page - 1) * limit).
		Find(ctx)
}

// CountActions 单插件审计行数（按 plugin 过滤的索引内计数，非全表 count）
func (r *Repository) CountActions(ctx context.Context, plugin string) (int64, error) {
	return gorm.G[PluginAction](r.DB()).Where("plugin = ?", plugin).Count(ctx, "id")
}

// NormalizeAuditPage 审计分页归一：页码 ≥1、条数落在 [1, auditMaxLimit]（唯一收敛点，贴近 SQL；
// 服务层同用它做「生效值回显」）。
func NormalizeAuditPage(page, limit int) (int, int) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = auditDefaultLimit
	}
	if limit > auditMaxLimit {
		limit = auditMaxLimit
	}
	return page, limit
}
