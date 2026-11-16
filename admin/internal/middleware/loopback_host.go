package middleware

import (
	"net"
	"net/http"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/internal/kit/httpx"

	"github.com/gin-gonic/gin"
)

const msgHostNotAllowed = "仅允许通过本机地址访问"

// LoopbackHost 只听回环地址时，拒绝 Host 头不是本机名的请求。
//
// 只绑 127.0.0.1 挡住了局域网直连，但挡不住 DNS 重绑定：恶意网页把自己的域名改解析到
// 127.0.0.1 后，浏览器会以「同源」身份访问本机控制台。这类请求的 Host 头仍是攻击者域名，
// 据此拒绝即可。显式放开到非回环地址（远程访问场景）时不做此校验，由部署方自行把关。
func LoopbackHost(listenHost string) gin.HandlerFunc {
	if !isLoopbackHost(listenHost) {
		return func(c *gin.Context) { c.Next() }
	}
	return func(c *gin.Context) {
		if !isLoopbackHost(requestHostname(c.Request.Host)) {
			httpx.Fail(c, httpx.WithMessage(msgHostNotAllowed), httpx.WithCode(http.StatusForbidden))
			c.Abort()
			return
		}
		c.Next()
	}
}

// requestHostname 取 Host 头中的主机名部分（去端口与 IPv6 方括号）。
func requestHostname(hostHeader string) string {
	hostHeader = strings.TrimSpace(hostHeader)
	if h, _, err := net.SplitHostPort(hostHeader); err == nil {
		return h
	}
	return strings.Trim(hostHeader, "[]")
}

// isLoopbackHost 报告主机名是否指向本机：localhost（含子域）或回环 IP。
func isLoopbackHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
