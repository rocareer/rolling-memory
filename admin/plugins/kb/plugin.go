// plugin: kb v1.0.0
// Package kb 是本插件（uid = kb，kind: code）的入口包——宿主三层分工里「插件」那一层。
//
// 只做两件事：
//   - init()：把自身登记进宿主注册表（只 import internal/plugin/host；**不要** import
//     internal/plugins——那会构成 Go 非法 import cycle）；
//   - mount()：在宿主建好、已挂运行时闸的 `plugin/kb` 子组上挂本插件的后台路由（只写相对段；
//     每条路由自挂 AdminAuth + AdminPermission——宿主只给「子组 + 运行时闸」，节点鉴权须插件自挂），
//     并启动插件自管的作业 worker。
//
// 目录形状（除 `internal/` 这一级外保持与宿主逐级同形，便于逐文件核对）：
//
//	plugins/kb/
//	  plugin.yaml                 元数据（菜单/节点/配置键/前端落位声明的唯一事实源）
//	  plugin.go                   本文件：入口（Register + mount 转交 router）
//	  config/kb.yaml              插件自带缺省配置（go:embed；**不进宿主 config/**）
//	  kbcfg/                      配置访问器单点（内嵌缺省 ▷ configs 表 plugin.kb 分组覆盖）
//	  model/                      KB 六表 + 标签四表 + 作业账本的模型
//	  repository/                 检索腿 SQL 与四套 CRUD 薄壳
//	  service/kb/                 KB 核心（权限/库/文档/作业/切片/提取/检索/标签）
//	  service/admin/kb/           四套 CRUD 服务 + 自定义校验
//	  service/jobs/               插件自管最小作业面（kind 注册表 + 账本 + worker）
//	  handler/admin/kb/           后台控制器（含运维端点与文档导入端点）
//	  router/admin/kb/            路由装配单点（mount 转交本包）
//	  migrations/                 插件自管迁移 1..6（版本表 <表前缀>plugin_kb_migrations）
//	  web/                        前端源（落位副本由安装器生成，逐字节一致）
//
// 生命周期：宿主的注册表只收「名/版本/形态/挂载点」，没有 Install/Start/Disable/Uninstall 方法
// ——启停语义由「台账四态 + 运行时闸 + worker 每轮自查 status」实现（停用即闸 404、即不领活）。
package kb

import (
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"
	kbrouter "github.com/ai-go-hub/ai-go-admin/plugins/kb/router/admin/kb"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/service/jobs"

	"github.com/gin-gonic/gin"
)

// 插件自身的事实（与 plugin.yaml 逐字对齐；重编译后进程内注册表以本处常量为准）
const (
	// Name 插件名（= plugins/ 目录名 = 台账行名 = 路由段 plugin/kb）
	Name = "kb"
	// Version 插件版本（= plugin.yaml 的 version）
	Version = "1.0.0"
	// Kind 插件形态（= plugin.yaml 的 kind；本插件无 L2 实体，结构表全在 migrations/）
	Kind = "code"
)

func init() {
	host.Register(host.Meta{Name: Name, Version: Version, Kind: Kind, Mount: mount})
}

// mount 路由挂载点：宿主给的是**已挂运行时闸**的 `plugin/kb` 子组。
// 只写相对段——写绝对前缀会双前缀，且权限节点名（剥掉后台入口前缀后的路径）与清单的
// menus[].name 不再逐字相等（会造成「装上了但非超管全 403」的静默坏）。
//
// 作业 worker 在这里启动（sync.Once 保护，只起一次）：mount 由宿主 router.Setup 期间调用，
// 此时 database.Init 与插件预检均已完成，DB/配置可用。
func mount(g *gin.RouterGroup) {
	kbrouter.Register(g)
	jobs.StartWorkerOnce()
}
