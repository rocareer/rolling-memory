// plugin: kb v1.0.0
package kb

import (
	"github.com/ai-go-hub/ai-go-admin/internal/handler"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
	svcKb "github.com/ai-go-hub/ai-go-admin/plugins/kb/service/admin/kb"

	"github.com/gin-gonic/gin"
)

// KBTagHandler 标签控制器
type KBTagHandler struct {
	*handler.Handler[model.KBTag]
	svc *svcKb.KBTagService
}

// NewKBTagHandler 创建标签控制器实例
func NewKBTagHandler(svc *svcKb.KBTagService) *KBTagHandler {
	h := &KBTagHandler{svc: svc}
	h.Handler = handler.NewHandler(svc,
		handler.WithOmitFields(handler.ActionFields{
			Update: []string{"id"},
		}))
	return h
}

// RegisterRoutes 注册路由
func (h *KBTagHandler) RegisterRoutes(group *gin.RouterGroup) {
	handler.RegisterBaseRoutes(h, group)
}
