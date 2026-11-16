// plugin: kb v1.0.0
package kb

import (
	"github.com/ai-go-hub/ai-go-admin/internal/repository"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
)

// KBTagRuleRepository 标签规则仓储
type KBTagRuleRepository struct {
	*repository.Repository[model.KBTagRule]
}

// NewKBTagRuleRepository 创建标签规则仓储实例
func NewKBTagRuleRepository() *KBTagRuleRepository {
	return &KBTagRuleRepository{
		Repository: repository.NewRepository[model.KBTagRule](),
	}
}
