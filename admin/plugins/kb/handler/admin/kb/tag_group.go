// plugin: kb v1.0.0
package kb

import (
	"github.com/ai-go-hub/ai-go-admin/internal/handler"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
	svcKb "github.com/ai-go-hub/ai-go-admin/plugins/kb/service/admin/kb"

	"github.com/gin-gonic/gin"
)

// KBTagGroupHandler 标签组控制器
type KBTagGroupHandler struct {
	*handler.Handler[model.KBTagGroup]
	svc *svcKb.KBTagGroupService
}

// NewKBTagGroupHandler 创建标签组控制器实例
func NewKBTagGroupHandler(svc *svcKb.KBTagGroupService) *KBTagGroupHandler {
	h := &KBTagGroupHandler{svc: svc}
	h.Handler = handler.NewHandler(svc,
		handler.WithOmitFields(handler.ActionFields{
			Update: []string{"id"},
		}))
	return h
}

// RegisterRoutes 注册路由
func (h *KBTagGroupHandler) RegisterRoutes(group *gin.RouterGroup) {
	handler.RegisterBaseRoutes(h, group)
}
