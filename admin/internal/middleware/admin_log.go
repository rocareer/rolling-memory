package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"
	"github.com/ai-go-hub/ai-go-admin/internal/infra/permission"
	"github.com/ai-go-hub/ai-go-admin/internal/kit/httpx"
	"github.com/ai-go-hub/ai-go-admin/internal/model"
	"github.com/ai-go-hub/ai-go-admin/internal/repository"
	repoAuth "github.com/ai-go-hub/ai-go-admin/internal/repository/admin/auth"
	"github.com/ai-go-hub/ai-go-admin/pkg/util"

	"github.com/gin-gonic/gin"
)

const (
	maxBodyLogSize = 64 * 1024 // 64KB

	// CtxAdminLogTitleKey 控制器可通过 c.Set(CtxAdminLogTitleKey, "标题") 自定义日志标题
	// 若未设置，中间件将自动从数据库查询规则标题
	CtxAdminLogTitleKey = "admin_log_title"
)

// secretBodyKeys 是请求体里绝不允许进操作日志的敏感字段名（AI 模型密钥、云端口令等）。
var secretBodyKeys = map[string]bool{
	"password": true, "passwd": true, "old_password": true, "new_password": true,
	"api_key": true, "apikey": true, "secret": true, "secret_key": true,
	"access_key": true, "private_key": true, "token": true,
}

// redactBodySecrets 把 JSON 请求体里敏感字段的值替换为 ***，其余内容保持原样；
// 非 JSON 或解析失败的请求体原样保留（宁可不记也不误伤）。请求本身不受影响。
func redactBodySecrets(raw string) string {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return raw
	}
	redactSecretsInValue(parsed, 0)
	encoded, err := json.Marshal(parsed)
	if err != nil {
		return raw
	}
	return string(encoded)
}

func redactSecretsInValue(value any, depth int) {
	if depth > 8 {
		return
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			if secretBodyKeys[strings.ToLower(key)] {
				typed[key] = "***"
				continue
			}
			redactSecretsInValue(item, depth+1)
		}
	case []any:
		for _, item := range typed {
			redactSecretsInValue(item, depth+1)
		}
	}
}

// AdminLog 管理员操作日志中间件
// 全局中间件，通过路径前缀判断是否记录
func AdminLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		fullPath := c.FullPath()
		if fullPath == "" {
			fullPath = c.Request.URL.Path
		}

		// 构建权限检查路径，用于匹配 AdminRule 的 name 字段
		adminPath := config.Get().Server.AdminBaseRoutePath
		checkPath := permission.BuildCheckPath(fullPath)

		// 仅记录管理后台请求
		// 仅记录 POST 请求
		// 额外排除 list 请求
		if !strings.HasPrefix(fullPath, adminPath+"/") || c.Request.Method != http.MethodPost || strings.HasSuffix(checkPath, "/list") {
			c.Next()
			return
		}

		// 捕获请求体，排除了 chunked 请求
		var bodyData string
		if c.Request.ContentLength > 0 {
			bodyBytes, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBodyLogSize))

			if err != nil {
				httpx.Fail(c, httpx.WithMessage("操作日志记录失败: 读取 c.Request.Body 失败"))
				c.Abort()
				return
			}

			c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

			if len(bodyBytes) > 0 {
				bodyData = redactBodySecrets(string(bodyBytes))
			}
		}

		c.Next()

		// 提前捕获所有需要的数据，避免 goroutine 中读取已复用的 context
		adminID := uint(0)
		adminName := ""
		admin := GetAdmin(c)
		if admin != nil {
			adminID = admin.ID
			adminName = admin.Username
		}

		clientIP := httpx.ClientIP(c)
		userAgent := util.TruncateStr(httpx.UserAgent(c), 255)

		// 检查控制器层是否已设置自定义日志标题
		customTitle, hasCustomTitle := c.Get(CtxAdminLogTitleKey)

		// 异步写入日志
		go func() {
			var title string
			repo := repoAuth.NewAdminLogRepository()
			if hasCustomTitle {
				title = customTitle.(string)
			} else {
				title = repo.GetRuleTitle(context.Background(), checkPath)
			}

			log := model.AdminLog{
				AdminID:   adminID,
				Username:  adminName,
				URL:       fullPath,
				Title:     title,
				IP:        clientIP,
				UserAgent: userAgent,
			}
			if bodyData != "" {
				log.Data = &bodyData
			}

			repo.Create(context.Background(), &log, repository.Options{})
		}()
	}
}
