package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/permission"

	"github.com/gin-gonic/gin"
)

// checkPermission 权限校验入口（包级变量：测试注入伪造实现用；生产恒为 permission.New().Check）
var checkPermission = func(ctx context.Context, adminID uint, ruleNames []string, op string) (bool, error) {
	return permission.New().Check(ctx, adminID, ruleNames, op)
}

// AdminPermission 管理员权限校验中间件
// 必须在 AdminAuth（管理员认证中间件）之后执行，仅挂载到需要验权的路由上，无需验权的路由不要注册此中间件
func AdminPermission() gin.HandlerFunc {
	return func(c *gin.Context) {
		admin := GetAdmin(c)
		if admin == nil {
			// 防御性分支（正常链路中 AdminAuth 已先阻断）：401 口径与其他认证失败一致，附 need_login
			writeAuthFailure(c, newAuthFailure(http.StatusUnauthorized, msgNeedLogin))
			return
		}

		// 构建检查路径: 去路径中的 query 和 params，去前缀
		fullPath := c.FullPath()
		if fullPath == "" {
			fullPath = c.Request.URL.Path
		}
		checkPath := permission.BuildCheckPath(fullPath)

		// 构建验权参数
		op := "AND"
		ruleNames := []string{checkPath}

		// list 和 get 同时验 read，op 为 OR
		if lastSlash := strings.LastIndex(checkPath, "/"); lastSlash != -1 {
			lastSeg := checkPath[lastSlash+1:]
			if lastSeg == "list" || lastSeg == "get" {
				ruleNames = []string{checkPath, checkPath[:lastSlash] + "/read"}
				op = "OR"
			}
		}

		hasPermission, err := checkPermission(c.Request.Context(), admin.ID, ruleNames, op)
		if err != nil {
			// 验权依赖数据库：故障是基础设施错误（503，保留会话），不是「无权限」（403）——
			// 归成 403 会让运维把数据库故障误读成用户没权限，故障排查被文案掩盖
			writeAuthFailure(c, newAuthFailure(http.StatusServiceUnavailable, msgServiceUnavailable))
			return
		}
		if !hasPermission {
			writeAuthFailure(c, newAuthFailure(http.StatusForbidden, msgNoPermission))
			return
		}

		c.Next()
	}
}
