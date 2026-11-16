package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/dto"
	"github.com/ai-go-hub/ai-go-admin/internal/infra/token"
	"github.com/ai-go-hub/ai-go-admin/internal/model"

	"github.com/gin-gonic/gin"
)

// 认证失败分类（401 / 403 / 503）的断言：
// 分类器为纯函数，令牌查询经 tokenLookup 注入伪造实现，因此不连库即可覆盖全部分支；
// 真库下的分类闭环（存储故障不得带 need_login）见 auth_failure_db_test.go。

type authResp struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// withTokenLookup 注入令牌查询伪造实现（生产恒为 token.Manager().Get）
func withTokenLookup(t *testing.T, tk *model.Token, err error) {
	t.Helper()
	orig := tokenLookup
	tokenLookup = func(ctx context.Context, raw string) (*model.Token, error) {
		return tk, err
	}
	t.Cleanup(func() { tokenLookup = orig })
}

// TestNewAuthFailureOnly401CarriesNeedLogin 契约：只有 401 携带 need_login
func TestNewAuthFailureOnly401CarriesNeedLogin(t *testing.T) {
	cases := []struct {
		status    int
		wantLogin bool
	}{
		{http.StatusUnauthorized, true},
		{http.StatusForbidden, false},
		{http.StatusServiceUnavailable, false},
	}
	for _, tc := range cases {
		failure := newAuthFailure(tc.status, "x")
		if got := failure.Type == FlagNeedLogin; got != tc.wantLogin {
			t.Fatalf("状态码 %d：need_login = %v，期望 %v", tc.status, got, tc.wantLogin)
		}
	}
}

// TestAdminAuthTokenClassification 令牌链路分类：失效类 → 401 + need_login；存储故障 → 503 且不带
func TestAdminAuthTokenClassification(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name         string
		tk           *model.Token
		err          error
		wantCode     int
		wantMsg      string
		wantLoginTag bool
	}{
		{"查无此行", nil, nil, http.StatusUnauthorized, msgTokenInvalid, true},
		{
			"令牌已过期",
			&model.Token{Token: "h", Type: token.TypeAdminLogin, UserID: 1, ExpiredAt: time.Now().Add(-time.Minute)},
			nil, http.StatusUnauthorized, msgTokenInvalid, true,
		},
		{
			"前台令牌不得当管理员令牌用",
			&model.Token{Token: "h", Type: "user_login", UserID: 1, ExpiredAt: time.Now().Add(time.Hour)},
			nil, http.StatusUnauthorized, msgReLogin, true,
		},
		{
			"存储层故障",
			nil, &token.StorageError{Op: "get", Err: errors.New("connection refused")},
			http.StatusServiceUnavailable, msgServiceUnavailable, false,
		},
		{
			"未知错误同样按服务不可用兜底",
			nil, errors.New("unexpected"),
			http.StatusServiceUnavailable, msgServiceUnavailable, false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withTokenLookup(t, tc.tk, tc.err)

			engine := gin.New()
			entered := false
			engine.GET("/admin/x", AdminAuth(), func(c *gin.Context) { entered = true })

			req := httptest.NewRequest(http.MethodGet, "/admin/x", nil)
			req.Header.Set("Authorization", "Bearer raw-token")
			rec := httptest.NewRecorder()
			engine.ServeHTTP(rec, req)

			if entered {
				t.Fatal("请求应被阻断，实际进入业务处理")
			}
			var resp authResp
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("响应体非 JSON: %v（%s）", err, rec.Body.String())
			}
			if resp.Code != tc.wantCode {
				t.Fatalf("业务码 = %d，期望 %d（%s）", resp.Code, tc.wantCode, rec.Body.String())
			}
			if resp.Message != tc.wantMsg {
				t.Fatalf("文案 = %q，期望 %q", resp.Message, tc.wantMsg)
			}
			if hasTag := strings.Contains(string(resp.Data), FlagNeedLogin); hasTag != tc.wantLoginTag {
				t.Fatalf("need_login 标记 = %v，期望 %v（data=%s）", hasTag, tc.wantLoginTag, string(resp.Data))
			}
		})
	}
}

// TestAdminAuthAnonymous 无 Authorization 头 → 401 + need_login
func TestAdminAuthAnonymous(t *testing.T) {
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.GET("/admin/x", AdminAuth(), func(c *gin.Context) {})

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/x", nil))

	var resp authResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应体非 JSON: %v（%s）", err, rec.Body.String())
	}
	if resp.Code != http.StatusUnauthorized || resp.Message != msgNeedLogin {
		t.Fatalf("未登录应判 401 %q，实际 code=%d message=%q", msgNeedLogin, resp.Code, resp.Message)
	}
	if !strings.Contains(string(resp.Data), FlagNeedLogin) {
		t.Fatalf("未登录必须带 need_login（data=%s）", string(resp.Data))
	}
}

// TestAdminPermissionClassification 验权层分类：故障 → 503（不附 need_login）、无权 → 403（不附）、有权放行
func TestAdminPermissionClassification(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name         string
		allowed      bool
		err          error
		wantCode     int
		wantEntered  bool
		wantLoginTag bool
	}{
		{"有权放行", true, nil, 0, true, false},
		{"无权 403", false, nil, http.StatusForbidden, false, false},
		{"验权层故障 503", false, errors.New("connection refused"), http.StatusServiceUnavailable, false, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			orig := checkPermission
			checkPermission = func(ctx context.Context, adminID uint, ruleNames []string, op string) (bool, error) {
				return tc.allowed, tc.err
			}
			t.Cleanup(func() { checkPermission = orig })

			engine := gin.New()
			entered := false
			engine.GET("/admin/x",
				func(c *gin.Context) {
					// 直接注入会话（本用例只验验权层分类）
					c.Set(CtxAdminKey, &dto.AdminSession{Admin: &model.Admin{ID: 1}, Token: "h"})
				},
				AdminPermission(),
				func(c *gin.Context) { entered = true },
			)

			rec := httptest.NewRecorder()
			engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/x", nil))

			if entered != tc.wantEntered {
				t.Fatalf("进入业务 = %v，期望 %v", entered, tc.wantEntered)
			}
			if tc.wantEntered {
				return
			}
			var resp authResp
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("响应体非 JSON: %v（%s）", err, rec.Body.String())
			}
			if resp.Code != tc.wantCode {
				t.Fatalf("业务码 = %d，期望 %d（%s）", resp.Code, tc.wantCode, rec.Body.String())
			}
			if hasTag := strings.Contains(string(resp.Data), FlagNeedLogin); hasTag != tc.wantLoginTag {
				t.Fatalf("need_login 标记 = %v，期望 %v（data=%s）", hasTag, tc.wantLoginTag, string(resp.Data))
			}
		})
	}
}
