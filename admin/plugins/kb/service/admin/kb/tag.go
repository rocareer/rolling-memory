// plugin: kb v1.0.0
package kb

import (
	"github.com/ai-go-hub/ai-go-admin/internal/service"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
	repoKb "github.com/ai-go-hub/ai-go-admin/plugins/kb/repository/admin/kb"
)

// KBTagService 标签服务
type KBTagService struct {
	service.IService[model.KBTag]
	repo *repoKb.KBTagRepository
}

// NewKBTagService 创建标签服务实例
func NewKBTagService(repo *repoKb.KBTagRepository) *KBTagService {
	return &KBTagService{
		IService: service.NewService(repo),
		repo:     repo,
	}
}
