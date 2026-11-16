package middleware

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/dto"
	"github.com/ai-go-hub/ai-go-admin/internal/infra/token"
	"github.com/ai-go-hub/ai-go-admin/internal/kit/httpx"
	"github.com/ai-go-hub/ai-go-admin/internal/model"
	"github.com/ai-go-hub/ai-go-admin/internal/repository"
	repoAdmin "github.com/ai-go-hub/ai-go-admin/internal/repository/admin"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 本文件是「认证失败分类」的唯一实现点：
//
//	401 → 未认证 / 令牌失效 / 账号不可用：客户端必须清理会话并重新登录，响应附 need_login 标记；
//	403 → 已认证但无权（权限不足）：与登录态无关，不附 need_login；
//	503 → 基础设施故障（数据库 / 驱动不可用）：客户端必须保留会话等待恢复，不附 need_login。
//
// 契约：只有 401 携带 need_login。把 503 标成 need_login，会让一次瞬时数据库故障
// 把全部在线账号从客户端强制登出；把认证失败标成 503，客户端则永远不会回到登录页。
//
// 下面的分类器刻意保持纯函数（不触库、不依赖 gin）：它们是失败分类的可测接口，
// 单元测试直接喂入错误 / 状态值即可覆盖全部分支（见 auth_failure_test.go）。

// 认证 / 授权失败统一文案
const (
	msgNeedLogin          = "请先登录"
	msgTokenInvalid       = "身份认证令牌失效，请重新登录"
	msgReLogin            = "请重新登录"
	msgAccountDisabled    = "账号已被禁用"
	msgServiceUnavailable = "服务暂时不可用，请稍后再试"
	msgNoPermission       = "无权限"
)

// authFailure 认证失败分类结果。
// Status 为业务码：401=未认证/令牌失效/账号不可用（客户端应清理会话），
// 403=与登录态无关的拒绝，503=基础设施故障（客户端必须保留会话，等待服务恢复）。
// 只有 401 会携带 Type=need_login，前端据此决定是否清 token。
// 注意：httpx.Fail 统一走 HTTP 200 + 响应体业务码；这里的 503 是业务码，不是 HTTP 状态码。
type authFailure struct {
	Status  int
	Type    string
	Message string
}

// newAuthFailure 构造认证失败；401 自动附带 need_login 类型，其余状态码一律不带。
func newAuthFailure(status int, message string) *authFailure {
	failure := &authFailure{Status: status, Message: message}
	if status == http.StatusUnauthorized {
		failure.Type = FlagNeedLogin
	}
	return failure
}

// writeAuthFailure 按失败分类下发响应并中断后续处理链：401 附登录引导标记，403 / 503 不附。
func writeAuthFailure(c *gin.Context, failure *authFailure) {
	if failure == nil {
		failure = newAuthFailure(http.StatusUnauthorized, msgNeedLogin)
	}
	opts := []httpx.Option{
		httpx.WithMessage(failure.Message),
		httpx.WithCode(failure.Status),
	}
	if failure.Status == http.StatusUnauthorized {
		opts = append(opts, httpx.WithData(gin.H{"type": FlagNeedLogin}))
	}
	httpx.Fail(c, opts...)
	c.Abort()
}

// ==================== 分类器（纯函数，可独立单测）====================

// tokenLookup 令牌查询入口（包级变量：测试注入伪造实现用；生产恒为 token.Manager().Get）。
var tokenLookup = func(ctx context.Context, raw string) (*model.Token, error) {
	return token.Manager().Get(ctx, raw)
}

// classifyTokenLookup 令牌查询失败分类：存储层故障（*token.StorageError）→ 503，保留客户端会话。
// 非存储层的未知错误同样按 503 处理：宁可让用户稍后重试，也不能让一次未知故障
// 把全部在线账号的会话在客户端清掉（请求照旧被拒 = fail-closed）。
func classifyTokenLookup(err error) *authFailure {
	if !token.IsStorageError(err) {
		log.Printf("[auth] 令牌查询失败（非存储层错误，按服务不可用处理）: %v", err)
	}
	return newAuthFailure(http.StatusServiceUnavailable, msgServiceUnavailable)
}

// classifyAccountLookup 账号查询失败分类：查无此人 → 401（账号可能已被删除，客户端重新登录）；
// 其余（含数据库故障）→ 503，不附 need_login。
func classifyAccountLookup(err error) *authFailure {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return newAuthFailure(http.StatusUnauthorized, msgReLogin)
	}
	return newAuthFailure(http.StatusServiceUnavailable, msgServiceUnavailable)
}

// accountStatusFailure 账号状态判定：disable 拒绝（401），其余放行。
func accountStatusFailure(status string) *authFailure {
	if status == "disable" {
		return newAuthFailure(http.StatusUnauthorized, msgAccountDisabled)
	}
	return nil
}

// ==================== 认证链路 ====================

// authenticateToken 校验原始管理员令牌并加载会话。
// AdminAuth / AdminAuthOptional 共用本入口。
func authenticateToken(ctx context.Context, raw string) (*dto.AdminSession, *authFailure) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, newAuthFailure(http.StatusUnauthorized, msgNeedLogin)
	}

	// 令牌查询：只有「查无此行」与过期属于认证失败（401）；数据库 / 驱动故障是基础设施错误，
	// 走 classifyTokenLookup 得到 503 且不带 need_login，否则瞬时故障会让全部在线管理员被前端强制登出。
	tk, err := tokenLookup(ctx, raw)
	if err != nil {
		return nil, classifyTokenLookup(err)
	}
	if tk == nil || time.Now().After(tk.ExpiredAt) {
		return nil, newAuthFailure(http.StatusUnauthorized, msgTokenInvalid)
	}

	// 令牌类型隔离：仅允许管理员登录类型的令牌
	if tk.Type != token.TypeAdminLogin {
		return nil, newAuthFailure(http.StatusUnauthorized, msgReLogin)
	}

	// 查询管理员信息：查无此人 → 401 重登；数据库故障 → 503
	adminRepo := repoAdmin.NewAdminRepository()
	admin, err := adminRepo.Get(ctx, repository.Options{
		PrimaryKeyValue: strconv.FormatUint(uint64(tk.UserID), 10),
	})
	if err != nil {
		return nil, classifyAccountLookup(err)
	}

	if failure := accountStatusFailure(admin.Status); failure != nil {
		return nil, failure
	}

	return &dto.AdminSession{Admin: admin, Token: raw}, nil
}

// AuthenticateToken 校验原始管理员令牌并加载会话（供 WebSocket 握手等入口直接复用）。
func AuthenticateToken(ctx context.Context, raw string) (*dto.AdminSession, string) {
	sess, failure := authenticateToken(ctx, raw)
	if sess != nil {
		return sess, ""
	}
	if failure != nil && failure.Message != "" {
		return nil, failure.Message
	}
	return nil, msgNeedLogin
}
