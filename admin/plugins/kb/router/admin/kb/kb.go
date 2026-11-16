// plugin: kb v1.0.0
// Package kb 知识库后台路由装配单点（由插件入口 plugin.go 的 mount 调用）。
//
// **层级口径（关键）**：宿主交给插件的是 `admin/plugin/kb` 这一级子组（已挂运行时闸），
// 所以本文件只写相对段——`/library`、`/tag`、`/tag_group`、`/tag_rule`、`/tag_link`、`/team`。
// 若照搬「组级再拼一层 /kb」，路径会变成 `admin/plugin/kb/kb/library/...`，权限节点名随之变成
// `plugin/kb/kb/...`，与清单里声明的 `plugin/kb/...` 不再逐字相等 ⇒ 非超管全部 403
// （装上但按钮全灰的静默坏；门禁只查静态形态，抓不到）。
//
// 节点名推导：去后台入口前缀后的路径，遇 `:param` 截断，末段 `list`/`get` 由 AdminPermission 回落
// 同级 `read`。逐条对照见各 handler 的注册注释与插件清单的 menus 段。
package kb

import (
	handlerKB "github.com/ai-go-hub/ai-go-admin/plugins/kb/handler/admin/kb"
	repoKB "github.com/ai-go-hub/ai-go-admin/plugins/kb/repository/admin/kb"
	svcAdminKB "github.com/ai-go-hub/ai-go-admin/plugins/kb/service/admin/kb"
	svcKB "github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb"

	"github.com/gin-gonic/gin"
)

// Register 注册本插件的全部后台路由（在宿主给的 plugin/kb 子组上写相对段）
func Register(group *gin.RouterGroup) {
	registerCore(group)
	registerTag(group)
	registerTagGroup(group)
	registerTagRule(group)
	registerTagLink(group)
	registerTeam(group)
	registerMaintenance(group)
	registerImport(group)
}

// registerCore 库 / 授权 / 文档 / 检索 / 切片 / 审计（同一 handler 实例，注册点分散在各自的文件里）
func registerCore(group *gin.RouterGroup) {
	h := handlerKB.NewHandler(newService())
	// 基础面（库 / 授权 / 文档最小面 / 检索 / 逐字核对）
	h.RegisterRoutes(group)
	// 文档动作面（重建 / 取消 / 失败清单）：注册单点在 handler/admin/kb/document.go 自持
	h.RegisterActionRoutes(group)
	// 审计只读列表：注册单点在 handler/admin/kb/audit.go 自持
	h.RegisterAuditRoutes(group)
}

// registerTag 标签管理（四套 CRUD 之一；表有排序列 ⇒ 走宿主基类六动作，节点含 sort）
func registerTag(group *gin.RouterGroup) {
	sub := group.Group("/tag")
	repo := repoKB.NewKBTagRepository()
	svc := svcAdminKB.NewKBTagService(repo)
	handlerKB.NewKBTagHandler(svc).RegisterRoutes(sub)
}

// registerTagGroup 标签组管理
func registerTagGroup(group *gin.RouterGroup) {
	sub := group.Group("/tag_group")
	repo := repoKB.NewKBTagGroupRepository()
	svc := svcAdminKB.NewKBTagGroupService(repo)
	handlerKB.NewKBTagGroupHandler(svc).RegisterRoutes(sub)
}

// registerTagRule 标签规则管理（**五条**路由：该表无排序列，路由集与节点集 1:1，见 handler 注释）
func registerTagRule(group *gin.RouterGroup) {
	sub := group.Group("/tag_rule")
	repo := repoKB.NewKBTagRuleRepository()
	svc := svcAdminKB.NewKBTagRuleService(repo)
	handlerKB.NewKBTagRuleHandler(svc).RegisterRoutes(sub)
}

// registerTagLink 标签绑定面（手写三端点：list/bind/unbind）
func registerTagLink(group *gin.RouterGroup) {
	h := handlerKB.NewHandler(newService())
	h.RegisterTagLinkRoutes(group.Group("/tag_link"))
}

// registerTeam 团队映射（表有排序列 ⇒ 走宿主基类六动作）
func registerTeam(group *gin.RouterGroup) {
	sub := group.Group("/team")
	repo := repoKB.NewKBTeamRepository()
	svc := svcAdminKB.NewKBTeamService(repo)
	handlerKB.NewKBTeamHandler(svc).RegisterRoutes(sub)
}

// registerMaintenance 运维端点（巡检 / 词面索引重建 / 重投嵌入）——原宿主 CLI 三命令的插件化替代
func registerMaintenance(group *gin.RouterGroup) {
	handlerKB.NewMaintenanceHandler(newService()).RegisterRoutes(group.Group("/maintenance"))
}

// registerImport 文档导入端点（库 → 文档的**入库入口**；本插件自带，宿主核心没有这一环）
func registerImport(group *gin.RouterGroup) {
	handlerKB.NewImportHandler(newService()).RegisterRoutes(group.Group("/document"))
}

// newService 服务实例构造单点（无内部可变状态 ⇒ 每个装配点各持一份；集中在此便于统一替换）
func newService() *svcKB.Service { return svcKB.NewService() }
