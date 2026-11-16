// plugin: kb v1.0.0
package kb

import (
	"github.com/ai-go-hub/ai-go-admin/internal/handler"
	"github.com/ai-go-hub/ai-go-admin/internal/middleware"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
	svcKb "github.com/ai-go-hub/ai-go-admin/plugins/kb/service/admin/kb"

	"github.com/gin-gonic/gin"
)

// KBTagRuleHandler 标签规则控制器
type KBTagRuleHandler struct {
	*handler.Handler[model.KBTagRule]
	svc *svcKb.KBTagRuleService
}

// NewKBTagRuleHandler 创建标签规则控制器实例
func NewKBTagRuleHandler(svc *svcKb.KBTagRuleService) *KBTagRuleHandler {
	h := &KBTagRuleHandler{svc: svc}
	h.Handler = handler.NewHandler(svc,
		handler.WithOmitFields(handler.ActionFields{
			Update: []string{"id"},
		}))
	return h
}

// RegisterRoutes 注册路由：**五条**（不用 handler.RegisterBaseRoutes）。
//
// 为什么不用宿主基类：基类按六动作注册（含 `/sort`），而标签规则表没有排序列——本插件清单里
// `tag_rule` 只声明 read/create/update/delete 四枚节点；照基类注册会多出一条没有节点对应的
// `/sort` 路由（非超管调用行为未定义）。这里把这条不一致**收口**：路由集与节点集 1:1。
// 其余三套 CRUD（tag / tag_group / team，表都有排序列）继续走宿主基类，宿主基类不改。
func (h *KBTagRuleHandler) RegisterRoutes(group *gin.RouterGroup) {
	group.POST("/list", middleware.AdminAuth(), middleware.AdminPermission(), h.List)
	group.POST("/create", middleware.AdminAuth(), middleware.AdminPermission(), h.Create)
	group.POST("/delete", middleware.AdminAuth(), middleware.AdminPermission(), h.Delete)

	group.GET("/get/:pk", middleware.AdminAuth(), middleware.AdminPermission(), h.Get)
	group.POST("/update/:pk", middleware.AdminAuth(), middleware.AdminPermission(), h.Update)
}
