package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/internal/dto"
	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"
	"github.com/ai-go-hub/ai-go-admin/internal/infra/database"
	"github.com/ai-go-hub/ai-go-admin/internal/model"

	"github.com/gin-gonic/gin"
)

// 存储侧故障不得被当作「用户认证失败」处理。
//
// 取证方式：连一个独立测试库，把令牌表删掉（`relation "tokens" does not exist`）——
// 与连接故障同属基础设施错误（非「令牌失效」），正是本用例要判的分类面；
// 再带一个 Bearer 令牌请求 AdminAuth 保护的路由，观察失败分类：
//   - 正确行为：503 + 不带 need_login（客户端保留会话、稍后重试）；
//   - 错误行为：401 + need_login（前端据此清 token 并跳登录页 ⇒ 一次瞬时存储故障把全部在线管理员强制登出）。
//
// 运行（必须指向独立测试库，用例会删表；库名须含 test/probe）：
//
//	AUTHFAIL_DB=1 DATABASE_WRITE_DBNAME=<独立测试库> go test ./internal/middleware/ -run AuthFailureOnDatabaseDown -v
func requireFailureTestDB(t *testing.T) {
	t.Helper()
	name := strings.ToLower(config.Get().Database.Write.DBName)
	if name == "" || (!strings.Contains(name, "test") && !strings.Contains(name, "probe")) {
		t.Fatalf("拒绝在非测试库上运行（dbname=%q）：请用 DATABASE_WRITE_DBNAME 指向名字含 test/probe 的独立库", name)
	}
}

func TestAuthFailureOnDatabaseDown(t *testing.T) {
	if os.Getenv("AUTHFAIL_DB") != "1" {
		t.Skip("需 AUTHFAIL_DB=1 且 DATABASE_WRITE_DBNAME 指向独立测试库（用例会删 tokens 表以注入存储侧故障）")
	}

	if err := config.Init(); err != nil {
		t.Fatalf("初始化配置失败: %v", err)
	}
	requireFailureTestDB(t)
	if err := database.Init(); err != nil {
		t.Fatalf("初始化数据库失败: %v", err)
	}

	if err := database.DB().Exec("DROP TABLE IF EXISTS tokens").Error; err != nil {
		t.Fatalf("注入存储侧故障失败: %v", err)
	}
	t.Cleanup(func() { _ = database.DB().AutoMigrate(&model.Token{}) })

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/admin/probe", AdminAuth(), func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/admin/probe", nil)
	req.Header.Set("Authorization", "Bearer zz-probe-token")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	var resp struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应体非 JSON: %v（%s）", err, rec.Body.String())
	}
	if resp.Code == http.StatusUnauthorized || strings.Contains(string(resp.Data), FlagNeedLogin) {
		t.Fatalf("存储故障被判成认证失败（body=%s）：前端会清 token 并把全部在线管理员登出；应判 503 且不带 need_login", rec.Body.String())
	}
	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("业务码 = %d，期望 503（服务暂时不可用，body=%s）", resp.Code, rec.Body.String())
	}
}

// 同一类缺陷的第二面：验权层故障被当成「无权限」（403）上报——运维会把数据库故障误读成用户没权限。
//
// 取证方式：连同库后删掉验权依赖的分组关联表（`relation "admin_group_access" does not exist`），
// 请求一条挂 AdminPermission 的路由（会话直接注入，只验验权层分类）：
//   - 正确行为：503 + 不带 need_login（保留会话，故障可排查）；
//   - 错误行为：403 无权限（把基础设施故障说成权限问题）。
func TestPermissionFailureOnDatabaseDown(t *testing.T) {
	if os.Getenv("AUTHFAIL_DB") != "1" {
		t.Skip("需 AUTHFAIL_DB=1 且 DATABASE_WRITE_DBNAME 指向独立测试库（用例会删表以注入存储侧故障）")
	}

	if err := config.Init(); err != nil {
		t.Fatalf("初始化配置失败: %v", err)
	}
	requireFailureTestDB(t)
	if err := database.Init(); err != nil {
		t.Fatalf("初始化数据库失败: %v", err)
	}

	if err := database.DB().Exec("DROP TABLE IF EXISTS admin_group_access").Error; err != nil {
		t.Fatalf("注入存储侧故障失败: %v", err)
	}
	t.Cleanup(func() { _ = database.DB().AutoMigrate(&model.AdminGroupAccess{}) })

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/admin/probe",
		func(c *gin.Context) {
			c.Set(CtxAdminKey, &dto.AdminSession{Admin: &model.Admin{ID: 1}, Token: "zz-probe-token"})
		},
		AdminPermission(),
		func(c *gin.Context) { c.Status(http.StatusOK) },
	)

	req := httptest.NewRequest(http.MethodGet, "/admin/probe", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	var resp struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应体非 JSON: %v（%s）", err, rec.Body.String())
	}
	if resp.Code == http.StatusForbidden {
		t.Fatalf("验权层故障被判成「无权限 403」（body=%s）：数据库故障会被误读成用户没权限，应判 503", rec.Body.String())
	}
	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("业务码 = %d，期望 503（body=%s）", resp.Code, rec.Body.String())
	}
	if strings.Contains(string(resp.Data), FlagNeedLogin) {
		t.Fatalf("服务端故障不得带 need_login（data=%s）", string(resp.Data))
	}
}
