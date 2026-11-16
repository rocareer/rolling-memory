package middleware

import (
	"net/url"
	"slices"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

// CORS 跨域中间件
func CORS() gin.HandlerFunc {
	cfg := config.Get().CORS

	corsCfg := cors.Config{
		AllowMethods:     cfg.Methods,
		AllowHeaders:     cfg.Headers,
		MaxAge:           time.Duration(cfg.MaxAge) * time.Second,
		AllowCredentials: true,
	}

	// 使用 AllowOriginFunc 统一处理（避免 AllowAllOrigins=true 与 AllowCredentials=true 组合冲突），
	// 同时兼容配置中的裸主机名（localhost）、host:port（localhost:8008）与完整 Origin（http://localhost:8008）。
	allowAll := slices.Contains(cfg.Origins, "*")
	corsCfg.AllowOriginFunc = func(origin string) bool {
		if allowAll {
			return true
		}
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		return slices.Contains(cfg.Origins, origin) ||
			slices.Contains(cfg.Origins, u.Host) ||
			slices.Contains(cfg.Origins, u.Hostname())
	}

	return cors.New(corsCfg)
}
