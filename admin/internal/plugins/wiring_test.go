package plugins

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"

	"github.com/gin-gonic/gin"
)

// TestMountWiresPluginSubgroup 接线单测（不连库）：登记一个假插件后调 mount，断言
//   - 路由落在 `plugin/<名>` 相对段下（绝对前缀由宿主 admin 组提供）；
//   - 插件拿到的是子组，只写相对段即可（`/ping` → `/admin/plugin/wtest/ping`）。
//
// 闸的行为不在本用例内（它按请求读库，见 host 包的 DB 门禁用例）。
func TestMountWiresPluginSubgroup(t *testing.T) {
	gin.SetMode(gin.TestMode)

	host.Register(host.Meta{
		Name:    "wtest",
		Version: "1.0.0",
		Kind:    "code",
		Mount: func(g *gin.RouterGroup) {
			g.POST("/ping", func(c *gin.Context) { c.String(http.StatusOK, "pong") })
		},
	})

	engine := gin.New()
	mount(engine.Group("/admin"))

	var found bool
	for _, route := range engine.Routes() {
		if route.Method == http.MethodPost && route.Path == "/admin/plugin/wtest/ping" {
			found = true
		}
	}
	if !found {
		t.Fatalf("插件路由未挂在 plugin/<名> 相对段下，实际路由: %+v", engine.Routes())
	}

	// 相对段之外的路径不应存在（宿主不会替插件加前缀）
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/plugin/wtest/ping", nil)
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("未带宿主前缀的路径应 404（避免双前缀/漏前缀歧义），实际 %d", rec.Code)
	}
}

// TestMountDeclarativeHasNoGoRoutes 纯声明式插件（无 Mount）不产生 Go 路由，
// 但仍建好 plugin/<名> 子组（L2 实体路由在 P3 由引擎在同一子组内注册）。
func TestMountDeclarativeHasNoGoRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	host.Register(host.Meta{Name: "wdecl", Version: "1.0.0", Kind: "declarative"})

	engine := gin.New()
	mount(engine.Group("/admin"))

	for _, route := range engine.Routes() {
		if strings.HasPrefix(route.Path, "/admin/plugin/wdecl") {
			t.Fatalf("声明式插件不应产生 Go 路由: %+v", route)
		}
	}
}

// TestMountPublicWiresGatedRootRoutes 公开面接线（不连库）：MountPublic 的路由挂在根路径
// （无后台前缀），且宿主确实挂了运行时闸——测试进程无数据库时闸 fail-closed（404），
// 插件 handler 不被调用。
func TestMountPublicWiresGatedRootRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	called := false
	host.Register(host.Meta{
		Name:    "wpub",
		Version: "1.0.0",
		Kind:    "code",
		MountPublic: func(g *gin.RouterGroup) {
			g.POST("/wpub/api/ping", func(c *gin.Context) {
				called = true
				c.String(http.StatusOK, "pong")
			})
		},
	})

	engine := gin.New()
	mountPublic(engine)

	var found bool
	for _, route := range engine.Routes() {
		if route.Method == http.MethodPost && route.Path == "/wpub/api/ping" {
			found = true
		}
		if strings.HasPrefix(route.Path, "/admin/plugin/wpub") {
			t.Fatalf("公开路由不应落在后台前缀下: %+v", route)
		}
	}
	if !found {
		t.Fatalf("公开路由未挂在根路径: %+v", engine.Routes())
	}

	// 无数据库 ⇒ host.Gate fail-closed；路由存在但 handler 不可达。
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/wpub/api/ping", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("未启用/无库时公开路由应 404（运行时闸），实际 %d", rec.Code)
	}
	if called {
		t.Error("闸未生效：插件 handler 在未启用状态下被调用")
	}
}

// TestMountPublicSkippedWhenAbsent 未声明 MountPublic 的插件不产生根级路由。
func TestMountPublicSkippedWhenAbsent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	host.Register(host.Meta{
		Name:    "wpubnone",
		Version: "1.0.0",
		Kind:    "code",
		Mount: func(g *gin.RouterGroup) {
			g.POST("/ping", func(c *gin.Context) { c.String(http.StatusOK, "pong") })
		},
	})

	engine := gin.New()
	mountPublic(engine)

	for _, route := range engine.Routes() {
		if strings.Contains(route.Path, "wpubnone") {
			t.Fatalf("未声明 MountPublic 的插件不应产生路由: %+v", route)
		}
	}
}
