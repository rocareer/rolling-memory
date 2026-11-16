package routine

import (
	"context"
	"fmt"

	"github.com/ai-go-hub/ai-go-admin/internal/model"
	repoCommon "github.com/ai-go-hub/ai-go-admin/internal/repository/common"
	"github.com/ai-go-hub/ai-go-admin/internal/service"
	"github.com/ai-go-hub/ai-go-admin/pkg/xss"

	"gorm.io/gorm"
)

// ConfigService 系统配置服务
type ConfigService struct {
	service.IService[model.Config]
	repo *repoCommon.ConfigRepository
}

// NewConfigService 创建系统配置服务实例
func NewConfigService(repo *repoCommon.ConfigRepository) *ConfigService {
	return &ConfigService{
		IService: service.NewService(repo),
		repo:     repo,
	}
}

// configGroupRegistered 判断 group 是否属于已登记的配置分组。
//
// 写口必须以登记表为准：group 来自请求路径参数，若不做归属校验，持有 routine/config/update
// 的角色可以用任意 group（如插件落库分组 plugin.ai）盲写不属于本页面的配置行。
func configGroupRegistered(groups []repoCommon.ConfigGroupItem, group string) bool {
	for _, item := range groups {
		if item.Key == group {
			return true
		}
	}
	return false
}

// BatchSave 批量保存配置值: 根据 group 和 name 定位行，更新 value
func (s *ConfigService) BatchSave(ctx context.Context, group string, data map[string]string) error {
	// 只允许已登记分组：config_group 是配置页的唯一分组来源，未登记分组（含插件自管分组）
	// 必须走各自的管理入口，不能被通用写口盲写。
	groups, err := s.repo.GetConfigGroups(ctx)
	if err != nil {
		return fmt.Errorf("读取配置分组失败: %w", err)
	}
	if !configGroupRegistered(groups, group) {
		return fmt.Errorf("配置分组 %q 未登记，拒绝写入", group)
	}

	// 预加载该分组配置的 name -> type 映射，对 editor 类型值做反 XSS 清洗
	list, err := gorm.G[model.Config](s.repo.DB()).
		Where(`"group" = ?`, group).
		Find(ctx)
	if err != nil {
		return err
	}
	types := make(map[string]string, len(list))
	for _, cfg := range list {
		types[cfg.Name] = cfg.Type
	}

	for name, value := range data {
		if types[name] == "editor" {
			value = xss.HTMLPolicySanitize(value)
		}
		_, err := gorm.G[map[string]any](s.repo.DB()).
			Table(model.Config{}.TableName()).
			Where(`name = ? AND "group" = ?`, name, group).
			Updates(ctx, map[string]any{"value": value})
		if err != nil {
			return err
		}
	}
	return nil
}
