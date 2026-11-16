package host

import (
	"sort"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

// Meta 插件进程内登记信息（插件包 init() 调用 Register 时提供）。
// 注册表是「接线真相」：编译期决定进程认识哪些插件。
type Meta struct {
	// Name 插件名：manifest 的 name、plugins/ 下的目录名、路由段 plugin/<名>
	Name string
	// Version 插件版本（manifest 的 version）
	Version string
	// Kind 插件形态：code | declarative | mixed
	Kind string
	// Mount 路由挂载点（可选）：宿主接线（internal/plugins/wiring.go）为每个插件建好
	// `plugin/<名>` 子组、挂上运行时闸后，把该子组交给插件（「转交」）。
	// 插件只写相对段（不写绝对前缀，写了会双前缀且权限节点名对不上）。
	// 纯声明式插件（kind=declarative）没有 Go 代码，此字段为空；其实体路由由 L2 引擎按元数据注册。
	Mount func(group *gin.RouterGroup)
	// MountPublic 公开路由挂载点（可选；host_api >= 1.1）：宿主在根引擎上建一个**只挂运行时闸**的组
	// （不带后台入口前缀、不带 admin 鉴权）后转交。路径由插件按对外契约逐字书写——插件化前后外部
	// 客户端看到的路径不得变化（例：评测协议网关 /eval/api/**）。
	//
	// 与 Mount 的区别：Mount 的组在 `admin/plugin/<名>` 下且由宿主提供后台前缀；MountPublic 的组在
	// 根路径，宿主只加 host.Gate（启停 ≤1s 生效，非 enabled 一律 404），命名空间与鉴权全由插件自持。
	// 同一插件可同时声明两者；纯声明式插件此字段为空。
	MountPublic func(group *gin.RouterGroup)
}

var (
	registryMu sync.RWMutex
	registry   = map[string]Meta{}
)

// Register 插件包在 init() 中登记自身，例：
//
//	func init() {
//		host.Register(host.Meta{Name: "demo", Version: "1.0.0", Kind: "mixed", Mount: mount})
//	}
//
// 空名与重复名属编程错误（注册发生在 init 期、进程尚未对外服务），直接 panic 暴露。
// 名字合法性与宿主保留字校验属 manifest 校验与门禁（/），此处不重复实现。
func Register(meta Meta) {
	name := strings.TrimSpace(meta.Name)
	if name == "" {
		panic("host: Register 需要非空插件名")
	}
	meta.Name = name

	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[name]; exists {
		panic("host: 插件重复登记: " + name)
	}
	registry[name] = meta
}

// Lookup 按名取登记信息；未登记返回 (Meta{}, false)
func Lookup(name string) (Meta, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	meta, ok := registry[name]
	return meta, ok
}

// Registered 返回全部登记信息（按 name 升序：顺序稳定 ⇒ 路由注册顺序与启动日志确定）。
// 接线侧（internal/plugins/wiring.go）按返回值逐个建组、挂闸、转交挂载点。
func Registered() []Meta {
	registryMu.RLock()
	out := make([]Meta, 0, len(registry))
	for _, meta := range registry {
		out = append(out, meta)
	}
	registryMu.RUnlock()

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
