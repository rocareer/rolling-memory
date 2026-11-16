// plugin: kb v1.0.0
package kb

import (
	"context"
	"errors"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/internal/kit/bindx"
	"github.com/ai-go-hub/ai-go-admin/internal/service"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
	svcKB "github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb"
)

// List 列表（浏览面上限：limit 截断到 MaxBrowseLimit=100；Count 不受分页影响）。
// CRUD 基座 BuildPaginateScope 只兜底 limit<1，不设上限——上限纪律在模块入口收口。
func (s *KBTeamService) List(ctx context.Context, opts service.Options) ([]model.KBTeam, error) {
	opts.Limit = svcKB.NormalizeBrowseLimit(opts.Limit)
	return s.IService.List(ctx, opts)
}

// Create 新增团队映射：按 group_name 解析 user_groups 最早命中行，固化 bound_group_id。
// bound_group_id 为派生字段（aigo kb check 巡检比对用），客户端传入值一律被覆盖。
func (s *KBTeamService) Create(ctx context.Context, tri *bindx.Tri[model.KBTeam], opts service.Options) error {
	groupID, err := s.resolveBoundGroupID(ctx, &tri.Model.GroupName)
	if err != nil {
		return err
	}
	tri.Model.BoundGroupID = groupID
	return s.IService.Create(ctx, tri, opts)
}

// Update 更新团队映射：仅当载荷带 group_name 时重解析 bound_group_id；
// bound_group_id 不接受客户端直改（载荷中的同名字段先删除）。
func (s *KBTeamService) Update(ctx context.Context, tri *bindx.Tri[model.KBTeam], opts service.Options) error {
	delete(tri.Map, "bound_group_id")
	if raw, ok := tri.Map["group_name"]; ok {
		name, ok := raw.(string)
		if !ok {
			return errors.New("组名非法")
		}
		groupID, err := s.resolveBoundGroupID(ctx, &name)
		if err != nil {
			return err
		}
		tri.Map["group_name"] = name
		tri.Map["bound_group_id"] = groupID
	}
	return s.IService.Update(ctx, tri, opts)
}

// resolveBoundGroupID 组名 → 组 id（按名绑定是仓内既定语义；解析经 user 轴主体面端口）。
// 组名去空白后就地回写；空名拒；**未接入用户体系 ⇒ bound_group_id=0**（0 = 未记录基线，
// 与「组不存在」区分：前者是部署形态、后者是数据问题——巡检据此点名）。
func (s *KBTeamService) resolveBoundGroupID(ctx context.Context, name *string) (uint, error) {
	groupName := strings.TrimSpace(*name)
	if groupName == "" {
		return 0, errors.New("组名必填")
	}
	groupID, err := svcKB.CurrentPrincipal().ResolveGroupID(ctx, groupName)
	if err != nil {
		if svcKB.IsPrincipalUnavailable(err) {
			*name = groupName
			return 0, nil // 未接入用户体系：仅登记绑定名，不落基线 id
		}
		return 0, errors.New("组名在用户分组中不存在: " + groupName)
	}
	*name = groupName
	return groupID, nil
}
