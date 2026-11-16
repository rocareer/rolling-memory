package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestLoopbackHostRejectsForeignHostHeader 钉住 DNS 重绑定防线：只听回环时，Host 头必须是本机名；
// 显式放开到非回环地址时不拦（远程访问由部署方把关）。
func TestLoopbackHostRejectsForeignHostHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	serve := func(listenHost, hostHeader string) (int, authResp) {
		t.Helper()
		r := gin.New()
		r.Use(LoopbackHost(listenHost))
		r.GET("/ping", func(c *gin.Context) { c.String(http.StatusOK, "pong") })
		req := httptest.NewRequest(http.MethodGet, "/ping", nil)
		req.Host = hostHeader
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		var body authResp
		if rec.Body.String() != "pong" {
			_ = json.Unmarshal(rec.Body.Bytes(), &body)
		}
		return rec.Code, body
	}

	for _, host := range []string{"127.0.0.1:9006", "localhost:9006", "LOCALHOST", "[::1]:9006", "app.localhost:8008", "127.0.0.2"} {
		if code, body := serve("127.0.0.1", host); code != http.StatusOK || body.Code != 0 {
			t.Fatalf("loopback listener must allow Host %q, got http=%d body=%+v", host, code, body)
		}
	}
	for _, host := range []string{"evil.example:9006", "192.168.1.10:9006", "127.0.0.1.evil.example", "localhost.evil.example", ""} {
		_, body := serve("127.0.0.1", host)
		if body.Code != http.StatusForbidden || body.Message != msgHostNotAllowed {
			t.Fatalf("loopback listener must reject Host %q, got body=%+v", host, body)
		}
	}
	for _, listen := range []string{"0.0.0.0", "", "192.168.1.10"} {
		if code, body := serve(listen, "evil.example:9006"); code != http.StatusOK || body.Code != 0 {
			t.Fatalf("non-loopback listener %q must not filter Host, got http=%d body=%+v", listen, code, body)
		}
	}
}
