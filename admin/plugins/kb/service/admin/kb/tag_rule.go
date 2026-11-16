// plugin: kb v1.0.0
package kb

import (
	"github.com/ai-go-hub/ai-go-admin/internal/service"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
	repoKb "github.com/ai-go-hub/ai-go-admin/plugins/kb/repository/admin/kb"
)

// KBTagRuleService 标签规则服务
type KBTagRuleService struct {
	service.IService[model.KBTagRule]
	repo *repoKb.KBTagRuleRepository
}

// NewKBTagRuleService 创建标签规则服务实例
func NewKBTagRuleService(repo *repoKb.KBTagRuleRepository) *KBTagRuleService {
	return &KBTagRuleService{
		IService: service.NewService(repo),
		repo:     repo,
	}
}
