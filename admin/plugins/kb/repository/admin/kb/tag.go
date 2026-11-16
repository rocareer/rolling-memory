// plugin: kb v1.0.0
package kb

import (
	"github.com/ai-go-hub/ai-go-admin/internal/repository"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
)

// KBTagRepository 标签仓储
type KBTagRepository struct {
	*repository.Repository[model.KBTag]
}

// NewKBTagRepository 创建标签仓储实例
func NewKBTagRepository() *KBTagRepository {
	return &KBTagRepository{
		Repository: repository.NewRepository[model.KBTag](),
	}
}
