// plugin: mdeditorv3 v1.0.6
// Package mdeditorv3 是「md-editor-v3 富文本编辑器与 Markdown 解析器」基础插件（kind: code）的 Go 包。
//
// 职责：
//   - init()：将自身登记进宿主插件注册表（internal/plugin/host）；
//   - mount()：在宿主建好的 `plugin/mdeditorv3` 路由组下挂载只读元信息端点 `POST /info`（仅要求 AdminAuth），
//     供管理端与前端编辑器组件查询插件启用态与能力集（markdown_parse / markdown_edit / catalog_toc / image_upload）。
package mdeditorv3

import (
	"github.com/ai-go-hub/ai-go-admin/internal/kit/httpx"
	"github.com/ai-go-hub/ai-go-admin/internal/middleware"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"
	"github.com/ai-go-hub/ai-go-admin/internal/version"

	"github.com/gin-gonic/gin"
)

const (
	// Name 插件名称（= plugins/ 目录名 = admin_plugins.name = 路由段 plugin/<名>）
	Name = "mdeditorv3"
	// Version 插件版本（与 plugin.yaml 一致）
	Version = "1.0.6"
	// Kind 插件形态
	Kind = "code"
	// EngineVersion 前端 md-editor-v3 引擎版本
	EngineVersion = "6.4.2"
)

func init() {
	host.Register(host.Meta{Name: Name, Version: Version, Kind: Kind, Mount: mount})
}

func mount(g *gin.RouterGroup) {
	g.POST("/info", middleware.AdminAuth(), info)
}

type infoResponse struct {
	Name           string   `json:"name"`
	Title          string   `json:"title"`
	Version        string   `json:"version"`
	Engine         string   `json:"engine"`
	EngineVersion  string   `json:"engine_version"`
	Capabilities   []string `json:"capabilities"`
	HostVersion    string   `json:"host_version"`
	HostAPIVersion string   `json:"host_api_version"`
}

func info(c *gin.Context) {
	httpx.Success(c, httpx.WithData(buildInfo(version.Get().Version, host.APIVersion)))
}

func buildInfo(hostVersion, hostAPIVersion string) infoResponse {
	return infoResponse{
		Name:          Name,
		Title:         "md-editor-v3富文本编辑器",
		Version:       Version,
		Engine:        "md-editor-v3",
		EngineVersion: EngineVersion,
		Capabilities: []string{
			"markdown_parse",
			"markdown_edit",
			"catalog_toc",
			"image_upload",
		},
		HostVersion:    hostVersion,
		HostAPIVersion: hostAPIVersion,
	}
}
