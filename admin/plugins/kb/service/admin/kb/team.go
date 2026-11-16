// plugin: kb v1.0.0
package kb

import (
	"github.com/ai-go-hub/ai-go-admin/internal/service"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
	repoKb "github.com/ai-go-hub/ai-go-admin/plugins/kb/repository/admin/kb"
)

// KBTeamService 团队映射服务
type KBTeamService struct {
	service.IService[model.KBTeam]
	repo *repoKb.KBTeamRepository
}

// NewKBTeamService 创建团队映射服务实例
func NewKBTeamService(repo *repoKb.KBTeamRepository) *KBTeamService {
	return &KBTeamService{
		IService: service.NewService(repo),
		repo:     repo,
	}
}
