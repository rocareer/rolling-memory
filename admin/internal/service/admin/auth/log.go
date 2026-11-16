package auth

import (
	"context"
	"strings"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/permission"
	"github.com/ai-go-hub/ai-go-admin/internal/model"
	repoAuth "github.com/ai-go-hub/ai-go-admin/internal/repository/admin/auth"
	"github.com/ai-go-hub/ai-go-admin/internal/service"
)

// adminLogListWindow 日志列表默认时间窗
//
// admin_logs 是增长表（审计中间件按请求写入，无清理机制）：列表/统计在无 created_at 条件时会退化为
// 全表排序 + 全表 count（该表只有 admin_id 索引），行数上来后日志页必然拖慢。请求未带 created_at
// 条件时补默认 90 天窗口；查历史数据请显式传 created_at 过滤条件。
const adminLogListWindow = 90 * 24 * time.Hour

// AdminLogService 管理员日志服务
type AdminLogService struct {
	service.IService[model.AdminLog]
	repo *repoAuth.AdminLogRepository
}

// NewAdminLogService 创建管理员日志服务实例
func NewAdminLogService(repo *repoAuth.AdminLogRepository) *AdminLogService {
	return &AdminLogService{
		IService: service.NewService(repo),
		repo:     repo,
	}
}

// Get 覆写通用单条查询方法: 非超管仅可查看自己的日志，严防越权读取他人日志
func (s *AdminLogService) Get(ctx context.Context, opts service.Options) (*model.AdminLog, error) {
	session, err := sessionFrom(opts)
	if err != nil {
		return nil, err
	}

	perm := permission.New()
	super, err := perm.IsSuperAdmin(ctx, session.ID)
	if err != nil {
		return nil, err
	}

	if !super {
		opts.Wheres = append(opts.Wheres, service.WhereGroup{
			Wheres: []service.Where{{
				Field:    "admin_id",
				Operator: "eq",
				Value:    session.ID,
			}},
		})
	}

	return s.IService.Get(ctx, opts)
}

// List 覆写通用查询方法: 非超管仅可查看自己的日志
func (s *AdminLogService) List(ctx context.Context, opts service.Options) ([]model.AdminLog, error) {
	session, err := sessionFrom(opts)
	if err != nil {
		return nil, err
	}

	perm := permission.New()
	super, err := perm.IsSuperAdmin(ctx, session.ID)
	if err != nil {
		return nil, err
	}

	// 非超管则只查询自己的日志
	if !super {
		opts.Wheres = append(opts.Wheres, service.WhereGroup{
			Wheres: []service.Where{{
				Field:    "admin_id",
				Operator: "eq",
				Value:    session.ID,
			}},
		})
	}

	// 未显式指定 created_at 条件时补默认时间窗（增长表：禁止请求路径上的无过滤全表扫描）
	opts.Wheres = appendLogQueryWindow(opts.Wheres)

	return s.IService.List(ctx, opts)
}

// Count 覆写通用统计方法: 与 List 使用相同的权限过滤条件
func (s *AdminLogService) Count(ctx context.Context, opts service.Options) (int64, error) {
	session, err := sessionFrom(opts)
	if err != nil {
		return 0, err
	}

	perm := permission.New()
	super, err := perm.IsSuperAdmin(ctx, session.ID)
	if err != nil {
		return 0, err
	}

	if !super {
		opts.Wheres = append(opts.Wheres, service.WhereGroup{
			Wheres: []service.Where{{
				Field:    "admin_id",
				Operator: "eq",
				Value:    session.ID,
			}},
		})
	}

	// 与 List 使用相同的时间窗口径，保证总数与列表一致
	opts.Wheres = appendLogQueryWindow(opts.Wheres)

	return s.IService.Count(ctx, opts)
}

// appendLogQueryWindow 请求未带 created_at 过滤时补默认时间窗
// 下界截断到整分钟：List 与 Count 两次查询取到相同边界，总数与列表口径一致
func appendLogQueryWindow(wheres []service.WhereGroup) []service.WhereGroup {
	if hasCreatedAtFilter(wheres) {
		return wheres
	}
	return append(wheres, service.WhereGroup{
		Wheres: []service.Where{{
			Field:    "created_at",
			Operator: "egt",
			Value:    time.Now().Add(-adminLogListWindow).Truncate(time.Minute),
		}},
	})
}

// hasCreatedAtFilter 判断条件组中是否已有 created_at 过滤
func hasCreatedAtFilter(wheres []service.WhereGroup) bool {
	for _, group := range wheres {
		for _, w := range group.Wheres {
			field := strings.TrimSpace(w.Field)
			if field == "created_at" || strings.HasSuffix(field, ".created_at") {
				return true
			}
		}
	}
	return false
}
