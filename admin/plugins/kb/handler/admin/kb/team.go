// plugin: kb v1.0.0
package kb

import (
	"github.com/ai-go-hub/ai-go-admin/internal/handler"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
	svcKb "github.com/ai-go-hub/ai-go-admin/plugins/kb/service/admin/kb"

	"github.com/gin-gonic/gin"
)

// KBTeamHandler 团队映射控制器
type KBTeamHandler struct {
	*handler.Handler[model.KBTeam]
	svc *svcKb.KBTeamService
}

// NewKBTeamHandler 创建团队映射控制器实例
func NewKBTeamHandler(svc *svcKb.KBTeamService) *KBTeamHandler {
	h := &KBTeamHandler{svc: svc}
	h.Handler = handler.NewHandler(svc,
		handler.WithOmitFields(handler.ActionFields{
			Update: []string{"id"},
		}))
	return h
}

// RegisterRoutes 注册路由
func (h *KBTeamHandler) RegisterRoutes(group *gin.RouterGroup) {
	handler.RegisterBaseRoutes(h, group)
}
