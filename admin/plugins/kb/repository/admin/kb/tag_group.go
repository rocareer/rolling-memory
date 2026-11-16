// plugin: kb v1.0.0
package kb

import (
	"github.com/ai-go-hub/ai-go-admin/internal/repository"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
)

// KBTagGroupRepository 标签组仓储
type KBTagGroupRepository struct {
	*repository.Repository[model.KBTagGroup]
}

// NewKBTagGroupRepository 创建标签组仓储实例
func NewKBTagGroupRepository() *KBTagGroupRepository {
	return &KBTagGroupRepository{
		Repository: repository.NewRepository[model.KBTagGroup](),
	}
}
