// Package rollingbrand 是 Rolling 品牌包的入口（kind: code）。
//
// 前端品牌面走构建期 overlay（plugins/rolling-brand/brand/**，见 web/vite.config.ts 的
// brandOverlay）；本包只提供公开品牌信息与资源，不新增后台菜单/路由，不写核心表。
//
// 公开面（MountPublic，host_api >= 1.1；宿主只挂运行时闸，不做后台鉴权）：
//
//	GET /plugin/rolling-brand/brand.json          品牌公开信息（站点名 / 备案号 / 欢迎语 / 资源 URL）
//	GET /plugin/rolling-brand/login-preset.json   开箱默认账号（仅本机/内网来源下发，公网来源只回 enabled=false）
//	GET /plugin/rolling-brand/logo.png            品牌 logo
//	GET /plugin/rolling-brand/favicon.ico         品牌 favicon
package rollingbrand

import (
	_ "embed"
	"net"
	"net/http"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"

	"github.com/gin-gonic/gin"
)

const (
	// Name 插件名（= plugins/ 目录名 = manifest.name）
	Name = "rolling-brand"
	// Version 插件版本（= plugin.yaml 的 version）
	Version = "1.0.0"
	// Kind 插件形态
	Kind = "code"

	// defaultAdminUsername / defaultAdminPassword 是控制台开箱默认账号，取值与宿主 bootstrap
	// 播种的 id=1 管理员一致（internal/infra/database/sqlite_bootstrap.go：用户名 rolling、
	// 口令 rolling）。宿主若改默认账号，这里必须同批改，否则「一键填入」会填入失效口令。
	defaultAdminUsername = "rolling"
	defaultAdminPassword = "rolling"
)

//go:embed brand/assets/logo.png
var logoPNG []byte

//go:embed brand/public/favicon.ico
var faviconICO []byte

func init() {
	host.Register(host.Meta{Name: Name, Version: Version, Kind: Kind, MountPublic: mountPublic})
}

// mountPublic 只提供公开品牌信息；品牌内容全部在本插件源树，不读写核心文件。
func mountPublic(r *gin.RouterGroup) {
	r.GET("/plugin/rolling-brand/brand.json", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"site_name":     "Rolling-Memory",
			"record_number": "京ICP备8888888号-1",
			"welcome_text":  "Rolling-Memory 本地优先图记忆系统 · 高性能、全追加可回源、受众四面隔离、零外发单核引擎。",
			"logo_url":      "/plugin/rolling-brand/logo.png",
			"favicon_url":   "/plugin/rolling-brand/favicon.ico",
		})
	})
	r.GET("/plugin/rolling-brand/login-preset.json", loginPreset)
	r.GET("/plugin/rolling-brand/logo.png", func(c *gin.Context) {
		c.Data(http.StatusOK, "image/png", logoPNG)
	})
	r.GET("/plugin/rolling-brand/favicon.ico", func(c *gin.Context) {
		c.Data(http.StatusOK, "image/x-icon", faviconICO)
	})
}

// loginPreset 下发开箱默认账号（登录页「一键填入」的唯一数据源）。
//
// 默认账号是宿主 bootstrap 播种的固定凭据，登录页明示可省掉首启注册摩擦；但它不应在公网实例上
// 俯拾即得，故只在来源地址落本机/内网面时下发，其余来源一律只回 enabled=false（不泄漏任何凭据）。
func loginPreset(c *gin.Context) {
	if !localClient(c.ClientIP()) {
		c.JSON(http.StatusOK, gin.H{"enabled": false})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"enabled":  true,
		"username": defaultAdminUsername,
		"password": defaultAdminPassword,
	})
}

// localClient 判定来源地址是否可信本机/内网面：loopback、私有网段、链路本地、未指定地址，
// 以及 CGNAT 专用段 100.64.0.0/10（Tailscale 等点对点组网默认落该段）。
func localClient(addr string) bool {
	ip := net.ParseIP(addr)
	if ip == nil {
		return false
	}

	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || inCGNAT(ip)
}

// inCGNAT 命中 CGNAT 专用段 100.64.0.0/10（net.IP 的私有段判定不含该段）。
func inCGNAT(ip net.IP) bool {
	_, cidr, err := net.ParseCIDR("100.64.0.0/10")

	return err == nil && cidr.Contains(ip)
}
