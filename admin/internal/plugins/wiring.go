// Package plugins 是插件的接线机床（设计说明三层分工的中间一层）。
//
// 分工：register_gen.go 是生成物（只含各插件包的空白导入，安装器写）；wiring.go（本文件）是
// 框架手写代码，安装器此后不再触碰。与 F3/F6 先例的差别（如实记录）：注册表与接线不能同包
// —— 插件包若 import「被空白导入它的那个包」是 Go 非法 import cycle，故注册表独立成
// internal/plugin/host，插件只 import host，本包负责建组/挂闸/转交。
//
// 时序（Go 保证被导入包先初始化）：插件包 init() 调 host.Register 登记 → 本包 init() 把挂载
// 函数交 registry.RegisterAdmin（此时 admin 组尚未创建、配置也未加载）→ router.Setup 遍历
// AdminRoutes 执行挂载函数（此时 admin 组已带后台入口前缀与 AdminLog 中间件）。
package plugins

import (
	"log"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/l2"
	"github.com/ai-go-hub/ai-go-admin/internal/router/registry"

	"github.com/gin-gonic/gin"
)

func init() {
	registry.RegisterAdmin(mount)
	// 公开面接线与后台面同批登记：两者都读同一份进程内注册表，但挂在根引擎（无后台前缀、
	// 无 AdminAuth/AdminLog），只挂运行时闸。
	registry.Register(mountPublic)
}

// mountPublic 为声明了 MountPublic 的插件在根引擎上建「只挂运行时闸」的组并转交。
//
// 与 mount 的差别（/-㉞）：
//   - **路径逐字对外**：插件按对外契约写绝对路径（`/eval/api/**` 等），宿主不加 `plugin/<名>` 前缀——
//     插件化是打包位置变化，外部客户端看到的 URL 不得变化；
//   - **闸同源**：host.Gate 按请求读 admin_plugins.status（≤1s 缓存），非 enabled 一律 404，
//     与后台面启停语义一致（停用插件不能继续答公开流量）；
//   - **鉴权归插件**：公开面没有 AdminAuth；协议级鉴权（AML 签名、native 令牌等）由插件自持，
//     宿主不替它猜；
//   - 路由冲突（与核心既有公开路由同 method+path）会在 gin 启动期 panic —— 这正是外移同一批提交里
//     必须删掉核心旧路由的原因，不做静默让位。
func mountPublic(r *gin.Engine) {
	metas := host.Registered()
	names := make([]string, 0, len(metas))
	for _, meta := range metas {
		if meta.MountPublic == nil {
			continue
		}
		group := r.Group("", host.Gate(meta.Name))
		meta.MountPublic(group)
		names = append(names, meta.Name)
	}
	if len(names) > 0 {
		log.Printf("[plugin] 已接线 %d 个插件的公开路由（根级、带运行时闸）: %s",
			len(names), strings.Join(names, ", "))
	}
}

// mount 为每个进程内已登记的插件建 `plugin/<名>` 子组、挂运行时闸，再把子组转交插件。
//
//   - **相对段**：只用 `plugin/<名>`，绝对前缀由宿主（admin 组）提供——插件写了绝对前缀会双前缀，
//     且 BuildCheckPath 剥前缀后的节点名与 manifest 声明的 name 不再逐字相等（非超管全 403），
//     闸也会被挂两次；
//   - **闸**：host.Gate 按请求读 admin_plugins.status（≤1s 缓存），非 enabled 一律 404 ⇒
//     enable/disable 无需重启即可生效（CLI 与 serve 是两个进程，无跨进程通知通道）；
//   - **L2 实体路由（P3）**：`internal/plugin/l2` 的引擎按 host.Loaded() 快照在同一子组内注册
//     `/plugin/<名>/<实体>/{list,create,delete,sort}` + `POST /update/:pk` + `GET /get/:pk`
//     （元数据装载由 l2.Mount 首行自举：host.MustLoad 幂等；宿主在 database.Init() 之后显式调用
//     host.Preflight 只是把装载失败提前成更干净的启动错误，不是路由挂载的前提）。
//     接线入口在下面的 mount 里显式调用（l2.Mount）——本文件是接线机床。
func mount(admin *gin.RouterGroup) {
	// L2 声明式实体路由：必须在下面「无 L1 插件即早退」分支**之前**接线——纯声明式插件
	// （kind=declarative）没有 Go 包 ⇒ host.Registered() 为空，早退会把它的六动作路由一并吞掉
	// （P3 交付项「实体路由注册」）。l2.Mount 幂等（包内 mountOnce）：l2 自己的 init()
	// 也注册了同一入口（import 本包即触发），两条路径都跑也不会双注册。
	l2.Mount(admin)

	metas := host.Registered()
	if len(metas) == 0 {
		return
	}

	names := make([]string, 0, len(metas))
	for _, meta := range metas {
		group := admin.Group("plugin/" + meta.Name)
		group.Use(host.Gate(meta.Name))
		if meta.Mount != nil {
			meta.Mount(group)
		}
		names = append(names, meta.Name)
	}
	log.Printf("[plugin] 已接线 %d 个插件（路由挂载于 %s/plugin/<名>）: %s",
		len(metas), admin.BasePath(), strings.Join(names, ", "))
}
