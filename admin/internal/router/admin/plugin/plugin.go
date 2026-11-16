// Package plugin 是插件管理面后台路由装配点（设计说明）。
//
// 路径固定为宿主所有（/admin/plugin/{list,get,enable,disable,uninstall}），不占插件命名空间
// （插件路由是 /admin/plugin/<名>/…；插件名被宿主保留字排除 ⇒ 结构上撞不上）。
// 节点名 = 去 /admin 的 checkPath（internal/infra/permission/admin.go BuildCheckPath）：
// list/get 末段回落 plugin/read，enable/disable/uninstall 精确匹配——与框架迁移的节点种子
// 逐名对应（000005_plugin 的 3 个 + 000010_plugin_uninstall_node 补种 plugin/uninstall；
// 种子不授予任何角色组：默认只有超管可见可用）。
package plugin

import (
	handlerPlugin "github.com/ai-go-hub/ai-go-admin/internal/handler/admin/plugin"
	repoPlugin "github.com/ai-go-hub/ai-go-admin/internal/repository/admin/plugin"
	"github.com/ai-go-hub/ai-go-admin/internal/router/registry"
	svcPlugin "github.com/ai-go-hub/ai-go-admin/internal/service/admin/plugin"

	"github.com/gin-gonic/gin"
)

func init() {
	registry.RegisterAdmin(func(group *gin.RouterGroup) {
		repo := repoPlugin.NewRepository()
		svc := svcPlugin.NewService(repo)
		h := handlerPlugin.NewHandler(svc)

		subGroup := group.Group("/plugin")
		h.RegisterRoutes(subGroup)
	})
}
