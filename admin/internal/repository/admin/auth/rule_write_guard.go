package auth

import (
	"context"

	"github.com/ai-go-hub/ai-go-admin/internal/model"
	"github.com/ai-go-hub/ai-go-admin/internal/repository"
)

// adminRuleWriteGuardOmit 规则表写侧固定排除列：插件归属列。
//
// 防护从 handler 构造选项下沉到仓储层（本文件），语义与下沉前逐字段等价：
// handler 侧回归上游原版名单（Create 无、Update: id/children），与本清单的并集与下沉前
// handler 的生效集合完全一致（Create: plugin；Update: id/children/plugin）——
// plugin 是系统维护列（归属只由插件安装器代写，/C3），若放任规则页 API 写入，
// 「按归属清理」会误删/误标宿主规则；列表与详情仍经 ToMap 读出该列。
//
// 放在仓储层的理由：本入口是规则表唯一的请求驱动写入面（AuthAdminRuleService 的
// Create / Update 经此，CRUD 生成器的 BatchCreate 亦复用本 Create），任何新增调用方都自动
// 受同一口径约束，不再依赖各 handler 是否记得在构造选项里列出该字段。
//
// 安装器写路径不受影响的依据（已查证）：插件安装/启用/卸载全部绕开本仓储，直接用
// `gorm.G[model.AdminRule](tx)` 等 GORM Generics API 写库——写入见
// internal/plugin/cli/dbwrite.go 的 writeInstallRules（Create 并显式打 plugin=插件名）、
// 清理见 dbwrite.go 的清理段与 promote_run.go、状态位见 lifecycle.go。
// 故本守卫只约束请求驱动面，不会拦住「安装器写归属列」这条唯一合法的归属写入路径。
// 同样地，服务层 Sort 走 gorm.Generics 的直接 UPDATE（internal/service/base.go），
// BatchCreate（CRUD 生成器建宿主菜单）经本 Create 时 plugin 被剔除，落库取列默认值（空串），
// 与剔除前写入的模型零值同值（宿主所有），语义不变。
var adminRuleWriteGuardOmit = []string{"plugin"}

// Create 覆写通用创建方法：叠加写侧固定排除列后委托基类实现（调用方传入的排除项照旧生效）
func (r *AdminRuleRepository) Create(ctx context.Context, entity *model.AdminRule, opts repository.Options) error {
	return r.Repository.Create(ctx, entity, ruleGuardWriteOptions(opts))
}

// Update 覆写通用更新方法：叠加写侧固定排除列后委托基类实现
// （map 更新同样受 Omit 约束，请求体中携带的 plugin 会被静默丢弃）
func (r *AdminRuleRepository) Update(ctx context.Context, entity map[string]any, opts repository.Options) error {
	return r.Repository.Update(ctx, entity, ruleGuardWriteOptions(opts))
}

// ruleGuardWriteOptions 在调用方选项上叠加写侧固定排除列（复制切片，避免改动调用方底层数组）
func ruleGuardWriteOptions(opts repository.Options) repository.Options {
	merged := make([]string, 0, len(opts.OmitFields)+len(adminRuleWriteGuardOmit))
	merged = append(merged, opts.OmitFields...)
	merged = append(merged, adminRuleWriteGuardOmit...)
	opts.OmitFields = merged
	return opts
}
