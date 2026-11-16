// plugin: kb v1.0.0
package kb

import (
	"github.com/ai-go-hub/ai-go-admin/internal/repository"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
)

// KBTeamRepository 团队映射仓储
type KBTeamRepository struct {
	*repository.Repository[model.KBTeam]
}

// NewKBTeamRepository 创建团队映射仓储实例
func NewKBTeamRepository() *KBTeamRepository {
	return &KBTeamRepository{
		Repository: repository.NewRepository[model.KBTeam](),
	}
}
