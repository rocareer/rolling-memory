// plugin: kb v1.0.0
// 可读库集解析（步骤②③ 的**集合形态**单点；检索腿与后续消费面共用）。
//
// 为什么单列一件：单资源面用 EXISTS（acl.go·libraryAccessCond），但「检索腿第一条件
// library_id = ANY(:readable)」需要**集合**——本文件把集合形态收敛为单点，谓词仍全部出自
// acl.go 的构造器（grantLibraryIDs 反查 + libraryListFilter 主查询），禁在别处另造第二口径。
//
// 两轴语义（与 ListLibraries 同一构造器；不新造口径）：
//   - user 轴：未删 ∧ status=1 ∧（owner=本人 ∪ perm='read' 授权反查集）；
//   - admin 轴：未删（全库——含禁用库；管理/探针面口径与 ListLibraries 一致；不借用 user 谓词）。
package kb

import (
	"context"
	"fmt"

	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
)

// ReadableLibraryIDs 可读库集（集合形态；每请求一次、请求内复用）。
//
// 空集合法（无任何可读库）：调用方（检索腿）按「空集直接返回空」处理——不发兜底查询。
// 未解析视图（nil）⇒ ErrDenied（fail-closed；防旁路入口直调）。
func (s *Service) ReadableLibraryIDs(ctx context.Context, v *ReadView) ([]uint, error) {
	if v == nil {
		return nil, ErrDenied
	}
	// 步骤②：kb_grant 反查命中 library_id 集（admin 轴不消费，返回 nil）
	grantIDs, err := s.grantLibraryIDs(ctx, v, model.KBGrantPermRead)
	if err != nil {
		return nil, fmt.Errorf("可读集反查失败: %w", err)
	}
	// 步骤③：主查询 → 集合（与列表/计数同一构造器；Count 与 List 防两处口径，此处取 id 集）
	cond, args := s.libraryListFilter(v, grantIDs)
	ids := make([]uint, 0, 16)
	if err := s.db.WithContext(ctx).
		Raw("SELECT "+s.tLibrary+".id FROM "+s.tLibrary+" WHERE "+cond+" ORDER BY "+s.tLibrary+".id", args...).
		Scan(&ids).Error; err != nil {
		return nil, fmt.Errorf("可读集读取失败: %w", err)
	}
	return ids, nil
}
