package auth

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strconv"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/permission"
	"github.com/ai-go-hub/ai-go-admin/internal/infra/token"
	"github.com/ai-go-hub/ai-go-admin/internal/kit/bindx"
	"github.com/ai-go-hub/ai-go-admin/internal/model"
	repoAdmin "github.com/ai-go-hub/ai-go-admin/internal/repository/admin"
	"github.com/ai-go-hub/ai-go-admin/internal/service"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// AuthAdminService 管理员账号管理服务
type AuthAdminService struct {
	service.IService[model.Admin]
	repo *repoAdmin.AdminRepository
}

// NewAuthAdminService 创建管理员账号管理服务实例
func NewAuthAdminService(repo *repoAdmin.AdminRepository) *AuthAdminService {
	return &AuthAdminService{
		IService: service.NewService(repo),
		repo:     repo,
	}
}

// Create 覆写通用创建方法: 校验用户名唯一 + 加密密码 + 校验分组权限
func (s *AuthAdminService) Create(ctx context.Context, tri *bindx.Tri[model.Admin], opts service.Options) error {
	if tri.Model.Username == "" {
		return errors.New("用户名不能为空")
	}
	if _, err := s.repo.FindByUsername(ctx, tri.Model.Username); err == nil {
		return errors.New("用户名已存在")
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}

	if tri.Model.Password == "" {
		return errors.New("密码不能为空")
	}
	hashed, err := bcrypt.GenerateFromPassword([]byte(tri.Model.Password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("密码加密失败: %w", err)
	}
	tri.Model.Password = string(hashed)

	// 校验分组权限
	if err := s.validateGroupAccesses(ctx, tri.Model.AdminGroupAccesses, opts); err != nil {
		return err
	}

	return s.IService.Create(ctx, tri, opts)
}

// Update 覆写通用更新方法: 用户名唯一校验 + 密码为空时移除字段不更新，非空则加密 + 分组关联替换
func (s *AuthAdminService) Update(ctx context.Context, tri *bindx.Tri[model.Admin], opts service.Options) error {
	pk, err := strconv.ParseUint(opts.PrimaryKeyValue, 10, 32)
	if err != nil {
		return errors.New("主键值错误")
	}

	// 校验目标管理员是否在操作者可见范围内，防止低权账号操作更高权限账号
	if err := s.ensureTargetsVisible(ctx, []uint{uint(pk)}, opts); err != nil {
		return err
	}

	// 禁止通过管理员更新接口禁用自己的账号
	session, err := sessionFrom(opts)
	if err != nil {
		return err
	}
	if uint(pk) == session.ID && tri.Map["status"] == "disable" {
		return errors.New("不能禁用自己的账号")
	}

	// 用户名唯一校验
	if tri.Model.Username == "" {
		return errors.New("用户名不能为空")
	}
	userNameExist, err := s.repo.FindByUsername(ctx, tri.Model.Username)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if userNameExist != nil && strconv.FormatUint(uint64(userNameExist.ID), 10) != opts.PrimaryKeyValue {
		return errors.New("用户名已存在")
	}

	// 密码为空时移除字段，确保不覆盖旧密码；非空则加密后写入
	// model.Admin.Password 为 json:"-"，但控制器已用 DTO 绑定并经 copier 填入 Model.Password
	passwordChanged := tri.Model.Password != ""
	if passwordChanged {
		// 改「自己」的口令必须回验当前密码：令牌只证明「当前会话」，不证明持钥人对该账号有处置权。
		// 自助资料路径与管理端编辑自己都会走到这里；代改他人口令不校验（操作者本就不掌握目标口令）。
		if uint(pk) == session.ID {
			raw, _ := tri.Map["old_password"].(string)
			if err := s.verifyCurrentPassword(ctx, uint(pk), raw); err != nil {
				return err
			}
		}

		hashed, err := bcrypt.GenerateFromPassword([]byte(tri.Model.Password), bcrypt.DefaultCost)
		if err != nil {
			return fmt.Errorf("密码加密失败: %w", err)
		}
		tri.Map["password"] = string(hashed)
	} else {
		delete(tri.Map, "password")
	}

	// 分组信息提取
	accesses := tri.Model.AdminGroupAccesses
	delete(tri.Map, "admin_group_accesses")

	// 当前密码只用于凭证校验，不落库（该列不存在，直接入库会 SQL 报错）
	delete(tri.Map, "old_password")

	// 校验分组权限
	if err := s.validateGroupAccesses(ctx, accesses, opts); err != nil {
		return err
	}

	// 改密后撤销该账号的全部管理员登录令牌：鉴权只校验令牌存在 / 未过期 / 类型 / 账号状态，
	// 不校验凭据新鲜度 ⇒ 改密不撤销时旧令牌仍可用最长 30 天（记住登录的有效期），
	// 「密码泄露后改密」不构成有效止损。先撤销再落库，落库失败也不会留下旧令牌可用的窗口。
	if passwordChanged {
		if err := token.Manager().Clear(ctx, uint(pk), token.TypeAdminLogin); err != nil {
			return fmt.Errorf("撤销登录令牌失败: %w", err)
		}
	}

	if err := s.IService.Update(ctx, tri, opts); err != nil {
		return err
	}

	// 改密同时清零登录失败计数与锁定：改密通常用于账号处置，处置后不应继续被旧锁拦住
	// （登录侧要等 24h 失败窗口过期才自动清零，锁定期内无从登录）。清零失败不影响本次
	// 改密结果（密码已落库、旧令牌已撤销），只记日志。
	if passwordChanged {
		if err := s.repo.ResetLoginFailure(ctx, uint(pk)); err != nil {
			log.Printf("[warn] 改密后清零登录失败计数失败: admin_id=%d: %v", pk, err)
		}
	}

	// 替换分组关联
	if accesses != nil {
		return s.repo.ReplaceGroupAccesses(ctx, uint(pk), accesses)
	}

	return nil
}

// verifyCurrentPassword 校验 raw 是否为该管理员的当前密码。
//
// 令牌只能证明「当前会话」，不能证明持钥人对该账号有处置权：令牌泄露 / XSS 场景下，
// 若不回验当前密码，持钥人可直接改密接管账号。Update 在「操作者 == 目标」且改密时调用本方法；
// 代改他人密码不调用（操作者本就不掌握目标账号的口令，改由写路径权限层级校验兜底）。
func (s *AuthAdminService) verifyCurrentPassword(ctx context.Context, adminID uint, raw string) error {
	if raw == "" {
		return errors.New("请填写当前密码")
	}

	admin, err := s.IService.Get(ctx, service.Options{PrimaryKeyValue: strconv.FormatUint(uint64(adminID), 10)})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errors.New("管理员不存在")
		}
		return errors.New("校验当前密码失败")
	}
	if bcrypt.CompareHashAndPassword([]byte(admin.Password), []byte(raw)) != nil {
		return errors.New("当前密码错误")
	}
	return nil
}

// Delete 覆写通用删除方法: 同时删除关联的分组数据
func (s *AuthAdminService) Delete(ctx context.Context, opts service.Options) error {
	if len(opts.PrimaryKeyValues) == 0 {
		return errors.New("请选择要删除的记录")
	}

	uids := make([]uint, 0, len(opts.PrimaryKeyValues))
	for _, pk := range opts.PrimaryKeyValues {
		uid, err := strconv.ParseUint(pk, 10, 32)
		if err != nil {
			return errors.New("主键值错误")
		}
		uids = append(uids, uint(uid))
	}

	session, err := sessionFrom(opts)
	if err != nil {
		return err
	}

	// 禁止删除自己的账号
	if slices.Contains(uids, session.ID) {
		return errors.New("不能删除自己的账号")
	}

	// 校验目标管理员是否在操作者可见范围内，防止低权账号删除更高权限账号
	if err := s.ensureTargetsVisible(ctx, uids, opts); err != nil {
		return err
	}

	// 删除分组关联
	for _, uid := range uids {
		if err := s.repo.DeleteGroupAccesses(ctx, uid); err != nil {
			return err
		}
	}

	return s.IService.Delete(ctx, opts)
}

// ensureTargetsVisible 校验目标管理员的分组规则是否全部在操作者可见范围内
func (s *AuthAdminService) ensureTargetsVisible(ctx context.Context, uids []uint, opts service.Options) error {
	session, err := sessionFrom(opts)
	if err != nil {
		return err
	}

	perm := permission.New()
	super, err := perm.IsSuperAdmin(ctx, session.ID)
	if err != nil {
		return err
	}
	if super {
		return nil
	}

	visible, err := perm.GetVisibleGroupIDs(ctx, session.ID, nil)
	if err != nil {
		return err
	}
	visibleSet := make(map[uint]struct{}, len(visible))
	for _, id := range visible {
		visibleSet[id] = struct{}{}
	}

	targetGroupIDs, err := s.repo.GroupIDsByUIDs(ctx, uids)
	if err != nil {
		return err
	}
	for _, gid := range targetGroupIDs {
		if _, ok := visibleSet[gid]; !ok {
			return errors.New("无权操作该管理员")
		}
	}
	return nil
}

// List 覆写通用查询方法: 只读取自己管理员有权管理的数据
func (s *AuthAdminService) List(ctx context.Context, opts service.Options) ([]model.Admin, error) {
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
		wheres, err := s.visibilityWheres(ctx, session.ID)
		if err != nil {
			return nil, err
		}
		opts.Wheres = append(opts.Wheres, wheres...)
	}

	return s.IService.List(ctx, opts)
}

// Count 覆写通用统计方法: 只读取自己管理员有权管理的数据
func (s *AuthAdminService) Count(ctx context.Context, opts service.Options) (int64, error) {
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
		wheres, err := s.visibilityWheres(ctx, session.ID)
		if err != nil {
			return 0, err
		}
		opts.Wheres = append(opts.Wheres, wheres...)
	}

	return s.IService.Count(ctx, opts)
}

// Get 覆写通用单条查询方法: 只读取自己管理员有权管理的数据（与 List/Count 同口径）
//
// List/Count 按可见分组过滤、Update/Delete 有 ensureTargetsVisible 兜底，通用 Get 却不做行级
// 归属校验，而权限中间件对 /get 与 /list 同按「read」节点放行 ⇒ 只被授予管理员读权限、并非
// 超管的账号可以按 id 直接读取可见范围外管理员的详情（含邮箱、手机号、最近登录 IP、分组归属）。
// 此处把可见范围补到详情路径；越权 id 按「记录不存在」处理。
func (s *AuthAdminService) Get(ctx context.Context, opts service.Options) (*model.Admin, error) {
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
		wheres, err := s.visibilityWheres(ctx, session.ID)
		if err != nil {
			return nil, err
		}
		opts.Wheres = append(opts.Wheres, wheres...)
	}

	return s.IService.Get(ctx, opts)
}

// visibilityWheres 返回过滤「不可见管理员」的查询条件
func (s *AuthAdminService) visibilityWheres(ctx context.Context, adminID uint) ([]service.WhereGroup, error) {
	visibleIDs, err := permission.New().GetVisibleGroupIDs(ctx, adminID, nil)
	if err != nil {
		return nil, err
	}

	excludedUIDs, err := s.repo.UIDsByInvisibleGroups(ctx, visibleIDs)
	if err != nil {
		return nil, err
	}
	if len(excludedUIDs) == 0 {
		return nil, nil
	}

	return []service.WhereGroup{{
		Wheres: []service.Where{{
			Field:    "id",
			Operator: "NOT IN",
			Value:    excludedUIDs,
		}},
	}}, nil
}

// validateGroupAccesses 校验分组权限，待分配分组必须全部在操作者可见范围内
func (s *AuthAdminService) validateGroupAccesses(ctx context.Context, accesses []model.AdminGroupAccess, opts service.Options) error {
	if len(accesses) == 0 {
		return nil
	}

	session, err := sessionFrom(opts)
	if err != nil {
		return err
	}

	perm := permission.New()
	super, err := perm.IsSuperAdmin(ctx, session.ID)
	if err != nil {
		return err
	}
	if super {
		return nil
	}

	visible, err := perm.GetVisibleGroupIDs(ctx, session.ID, nil)
	if err != nil {
		return err
	}
	visibleSet := make(map[uint]struct{}, len(visible))
	for _, id := range visible {
		visibleSet[id] = struct{}{}
	}

	for _, acc := range accesses {
		if _, ok := visibleSet[acc.GroupID]; !ok {
			return errors.New("无权分配该分组")
		}
	}
	return nil
}
