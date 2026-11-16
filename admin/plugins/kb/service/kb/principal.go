// plugin: kb v1.0.0
// user 轴主体面端口（Principal）：把「前台用户体系」的六个能力从 KB 逻辑里抽出来，由**宿主/下游部署**
// 按自身用户体系实现并注入。
//
// 为什么端口化：目标形态下插件不假设宿主存在用户体系（本插件不建也不读 users/user_groups 类表——
// 那些表属宿主自管面）。而 KB 的隔离口径里 user 轴需要：账号存在性/启用态、用户名（列表展示）、
// 启用组名集（团队读视图输入）、组名→现行 id（团队绑定与巡检基线）。
//
// 纪律（fail-closed，不降级放行）：
//   - 每个方法在「端口未注册」时一律返回 ErrFailClosed（ErrDenied 包装）——**不是**「查不到就放过」；
//   - UserExists 的失败语义 = 「目标主体不存在」与「端口不可用」是两类错误：前者服务层给
//     「目标属主不存在」文案，后者给 ErrFailClosed（日志可区分，UI 同型）；
//   - UserNames 不可用时 owner_username 整体不填（列表照常返回——展示字段缺失不影响可见性判定）；
//   - admin 轴完全不经过本端口（后台轴按节点授权全库可见），故「无用户体系」的部署里
//     admin 面行为不变。
package kb

import (
	"context"
	"errors"
	"fmt"
)

// Principal user 轴主体面端口（实现方 = 宿主/下游部署的用户体系适配层）
type Principal interface {
	// UserExists 用户存在且未软删（写面无权/悬空主体判据）
	UserExists(ctx context.Context, id uint) error
	// UserActive 用户存在且状态启用（读视图入口守卫；停用 ⇒ ErrActorUnavailable 语义）
	UserActive(ctx context.Context, id uint) error
	// UserNames 批量取用户名（列表展示字段 owner_username；缺 id 可不返回）
	UserNames(ctx context.Context, ids []uint) (map[uint]string, error)
	// EnabledGroupNames 用户所属的**启用**组名集（团队读视图的输入）
	EnabledGroupNames(ctx context.Context, id uint) ([]string, error)
	// GroupNameIDs 组名 → 现行组 id（团队绑定基线比对；缺名可不返回）
	GroupNameIDs(ctx context.Context, names []string) (map[string]uint, error)
	// ResolveGroupID 组名 → 组 id（团队绑定入口；不存在 ⇒ 错误）
	ResolveGroupID(ctx context.Context, name string) (uint, error)
}

var principalState struct {
	impl Principal
}

// RegisterPrincipal 注册 user 轴主体面实现（宿主确有用户体系时注入；重复注册覆盖前值）
func RegisterPrincipal(p Principal) {
	principalState.impl = p
}

// CurrentPrincipal 取当前实现；未注册 ⇒ fail-closed 桩（每个方法一律 ErrFailClosed）
func CurrentPrincipal() Principal {
	if principalState.impl != nil {
		return principalState.impl
	}
	return failClosedPrincipal{}
}

// failClosedPrincipal 未接入用户体系时的实现：一律拒（不降级放行、不静默返回空集）
type failClosedPrincipal struct{}

// UserExists 未接入用户体系 ⇒ 拒
func (failClosedPrincipal) UserExists(context.Context, uint) error {
	return errPrincipalUnavailable("UserExists")
}

// UserActive 未接入用户体系 ⇒ 拒
func (failClosedPrincipal) UserActive(context.Context, uint) error {
	return errPrincipalUnavailable("UserActive")
}

// UserNames 未接入用户体系 ⇒ 拒（调用方按展示字段缺失处理）
func (failClosedPrincipal) UserNames(context.Context, []uint) (map[uint]string, error) {
	return nil, errPrincipalUnavailable("UserNames")
}

// EnabledGroupNames 未接入用户体系 ⇒ 拒
func (failClosedPrincipal) EnabledGroupNames(context.Context, uint) ([]string, error) {
	return nil, errPrincipalUnavailable("EnabledGroupNames")
}

// GroupNameIDs 未接入用户体系 ⇒ 拒
func (failClosedPrincipal) GroupNameIDs(context.Context, []string) (map[string]uint, error) {
	return nil, errPrincipalUnavailable("GroupNameIDs")
}

// ResolveGroupID 未接入用户体系 ⇒ 拒
func (failClosedPrincipal) ResolveGroupID(context.Context, string) (uint, error) {
	return 0, errPrincipalUnavailable("ResolveGroupID")
}

// errPrincipalUnavailable 端口不可用（ErrFailClosed 族：HTTP 层同型 404，日志可区分）
func errPrincipalUnavailable(method string) error {
	return fmt.Errorf("%w：user 轴主体面未接入（Principal.%s）", ErrFailClosed, method)
}

// IsPrincipalUnavailable 端口不可用判定（调用方按「无用户体系」分支处理展示/绑定降级）
func IsPrincipalUnavailable(err error) bool {
	return errors.Is(err, ErrFailClosed)
}
