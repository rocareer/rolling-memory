// plugin: kb v1.0.0
package kb

import (
	"github.com/ai-go-hub/ai-go-admin/internal/service"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
	repoKb "github.com/ai-go-hub/ai-go-admin/plugins/kb/repository/admin/kb"
)

// KBTagGroupService 标签组服务
type KBTagGroupService struct {
	service.IService[model.KBTagGroup]
	repo *repoKb.KBTagGroupRepository
}

// NewKBTagGroupService 创建标签组服务实例
func NewKBTagGroupService(repo *repoKb.KBTagGroupRepository) *KBTagGroupService {
	return &KBTagGroupService{
		IService: service.NewService(repo),
		repo:     repo,
	}
}
