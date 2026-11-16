package middleware

import (
	"net/http"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/internal/dto"

	"github.com/gin-gonic/gin"
)

const (
	// 上下文中存储管理员会话信息的键
	CtxAdminKey = "admin"
)

// 标识常量
const (
	FlagNeedLogin = "need_login" // 需要登录
	FlagLoggedIn  = "logged_in"  // 已经登录
)

// AdminAuth 管理员认证中间件，未登录时阻断请求
func AdminAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		session, failure := extractAdmin(c)
		if session != nil {
			c.Set(CtxAdminKey, session)
			c.Next()
			return
		}
		// 按失败分类下发：401 附 need_login（客户端清会话重登），503 不附（客户端保留会话等待恢复）
		writeAuthFailure(c, failure)
	}
}

// AdminAuthOptional 可选管理员认证中间件，有 token 则注入管理员信息，没有也放行
func AdminAuthOptional() gin.HandlerFunc {
	return func(c *gin.Context) {
		if session, _ := extractAdmin(c); session != nil {
			c.Set(CtxAdminKey, session)
		}
		c.Next()
	}
}

// GetAdmin 从上下文中取出管理员信息，未登录时返回 nil
func GetAdmin(c *gin.Context) *dto.AdminSession {
	session, ok := c.Get(CtxAdminKey)
	if !ok {
		return nil
	}
	return session.(*dto.AdminSession)
}

// GetToken 从上下文中取出当前令牌字符串，未登录时返回空
func GetToken(c *gin.Context) string {
	session := GetAdmin(c)
	if session == nil {
		return ""
	}
	return session.Token
}

// extractAdmin 提取并验证 token，返回 (会话信息, 失败分类)
func extractAdmin(c *gin.Context) (*dto.AdminSession, *authFailure) {
	// 提取请求 token
	authHeader := c.GetHeader("authorization")

	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return nil, newAuthFailure(http.StatusUnauthorized, msgNeedLogin)
	}

	// 令牌校验与会话加载见 auth_failure.go（失败分类的唯一实现点）
	return authenticateToken(c.Request.Context(), parts[1])
}
