package serve

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"
	"github.com/ai-go-hub/ai-go-admin/internal/infra/database"
	"github.com/ai-go-hub/ai-go-admin/internal/middleware"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"
	"github.com/ai-go-hub/ai-go-admin/internal/router"

	"github.com/gin-gonic/gin"
	"github.com/spf13/cobra"
)

// NewCommand 返回启动 API 服务的 serve 命令
func NewCommand() *cobra.Command {
	return &cobra.Command{
		Use:           "serve",
		Short:         "启动 API 服务",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          Run,
	}
}

// Run 启动 API 服务
func Run(cmd *cobra.Command, args []string) error {
	// 初始化文件配置系统
	if err := config.Init(); err != nil {
		return fmt.Errorf("初始化配置: %w", err)
	}

	// 设置全局时区
	loc, err := time.LoadLocation(config.Get().App.Timezone)
	if err != nil {
		return fmt.Errorf("加载时区: %w", err)
	}
	time.Local = loc

	// 初始化数据库连接
	if err := database.Init(); err != nil {
		return fmt.Errorf("初始化数据库: %w", err)
	}

	engine := gin.Default()

	cfg := config.Get()

	// 配置可信代理
	if err := engine.SetTrustedProxies(cfg.Server.TrustedProxies); err != nil {
		return fmt.Errorf("设置可信代理: %w", err)
	}

	// 只听回环时拒绝非本机 Host 头（防 DNS 重绑定把恶意网页变成「本机来源」）
	engine.Use(middleware.LoopbackHost(cfg.Server.Host))

	// 注册跨域中间件
	engine.Use(middleware.CORS())

	// 注册全局接口节流中间件
	engine.Use(middleware.Throttle())

	// 注册数据库中间件
	engine.Use(database.Middleware())

	// 插件启动预检：读插件台账与实体元数据（表缺失或 spec 非法 ⇒ 启动失败，fail-fast 不静默零路由），
	// 须在 router.Setup 之前完成
	if err := host.Preflight(context.Background()); err != nil {
		return err
	}

	// 注册路由
	router.Setup(engine)

	addr := net.JoinHostPort(cfg.Server.Host, strconv.Itoa(cfg.Server.Port))
	// Explicit server timeouts instead of gin's Run(): a slow-header peer must not
	// be able to park a goroutine and a socket forever. ReadTimeout stays unset on
	// purpose — hijacked WebSocket connections inherit the server read deadline
	// and would be cut mid-stream.
	server := &http.Server{
		Addr:              addr,
		Handler:           engine,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	fmt.Fprintf(gin.DefaultWriter, "Listening and serving HTTP on %s\n", addr)
	if err := server.ListenAndServe(); err != nil {
		return fmt.Errorf("启动服务: %w", err)
	}
	return nil
}
