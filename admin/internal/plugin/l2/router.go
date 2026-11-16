package l2

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/ai-go-hub/ai-go-admin/internal/middleware"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"
	"github.com/ai-go-hub/ai-go-admin/internal/router/registry"

	"github.com/gin-gonic/gin"
)

// 实体路由注册（router.go 行 /）：
//
//	POST plugin/<名>/<实体>/list、/create、/delete [/sort 仅 drag_sort]
//	GET  plugin/<名>/<实体>/get/:pk
//	POST plugin/<名>/<实体>/update/:pk      ← 必须带 :pk（前端 TableRouterAPI 拼 ${url}/${pk}，F9）
//
// 相对段只有 plugin/<名>/<实体>，绝对前缀由宿主 admin 组提供（：写了绝对前缀会双前缀，且
// BuildCheckPath 剥前缀后的节点名与节点命名对不上 ⇒ 非超管全 403）。
// 每个实体子组都挂 host.Gate(<名>)：闸按请求读 admin_plugins.status（≤1s 缓存），非 enabled 一律 404。
//
// 注册时机：router.Setup 遍历 registry.AdminRoutes 时。元数据装载由**本包自举**（Mount 首行的
// host.MustLoad）——宿主在启动链路显式调用 host.Preflight 只是把「装载失败」提前成更干净的启动
// 错误，不再是本路由能不能挂上的前提（没有它也不会静默零路由）。
// 新增实体需要重启进程（路由在启动时注册，显式边界）；字段/文案/校验变更即时生效（按请求读元数据）。

// snapshotFn 元数据快照来源（生产恒为 host.Loaded；测试注入假快照 ⇒ 路由形状可零库断言）
var snapshotFn = func() (*host.Snapshot, bool) { return host.Loaded() }

// SetSnapshotSource 替换快照来源（传 nil 恢复默认）。仅测试用：生产链路的快照只能由 host.Preflight 装载。
func SetSnapshotSource(fn func() (*host.Snapshot, bool)) {
	if fn == nil {
		snapshotFn = func() (*host.Snapshot, bool) { return host.Loaded() }
		return
	}
	snapshotFn = fn
}

var mountOnce sync.Once

func init() {
	// 自注册：宿主加一行空白导入 `_ ".../internal/plugin/l2"` 即可接入（无需改接线文件）。
	// 与接线侧显式调用 l2.Mount(admin) 等价——包级 Mount 幂等，两条都做也只注册一次。
	registry.RegisterAdmin(Mount)
}

// Mount 包级入口（幂等）：默认引擎 + 启动快照，注册全部 L2 实体路由。
// 接线侧（internal/plugins/wiring.go）请调用本函数；直接调 (*Engine).Mount 不幂等。
//
// 首行自举元数据装载（幂等）：本包的两个入口（init 注册的自注册行、接线侧显式调用）都可能先于
// 其它接线点执行，装载放在这里 ⇒ 无论哪个先跑，快照都已就绪；装载失败即启动中止（host.MustLoad）。
func Mount(admin *gin.RouterGroup) {
	host.MustLoad(context.Background())
	mountOnce.Do(func() { Default().Mount(admin) })
}

// Mount 注册快照内全部实体的六动作路由（非幂等：一个进程只调一次）。
// spec 非法的实体**直接 panic**（启动即失败）：router.Setup 没有错误返回通道，而「静默零路由」正是
// 明确否决的降级（运维会把零路由误判成插件在跑）。宿主闭集校验已在 host.Preflight 把关，
// 此处拦截的是引擎增补规则（保留列名、操作符与类型兼容、trgm 类型、长度预算、物理表名字符集）。
func (e *Engine) Mount(admin *gin.RouterGroup) {
	if admin == nil {
		return
	}
	snapshot, ok := snapshotFn()
	if !ok {
		// 快照缺失 = 数据库未初始化（只装配路由的测试 / 嵌入场景）：包级 Mount 的 host.MustLoad
		// 已在 DB 就绪时保证快照非 nil，故本分支只在这些场景命中，不 panic——未接插件机制的环境
		// 不该被拖成启动失败。零插件时快照也是非 nil（Preflight 恒装载空快照）⇒ 本分支不会被
		// 「仓里没有插件」触发，不会把正常部署误判成故障。
		log.Printf("[plugin] L2 实体路由未注册：元数据快照未装载（数据库未初始化？）")
		return
	}

	registered := make([]string, 0, len(snapshot.Entities))
	for _, entity := range snapshot.Entities {
		if err := e.registerEntity(admin, entity); err != nil {
			panic(fmt.Sprintf("插件 %s 的实体 %s 无法注册 L2 路由: %v（修正 admin_plugin_entities 的 spec 后重启；或 aigo plugin uninstall 该插件）",
				entity.Plugin, entity.Name, err))
		}
		registered = append(registered, entity.Plugin+"/"+entity.Name)
	}
	if len(registered) > 0 {
		log.Printf("[plugin] 已注册 %d 个 L2 实体路由（%s/plugin/<名>/<实体>）: %s",
			len(registered), strings.TrimSuffix(admin.BasePath(), "/"), strings.Join(registered, ", "))
	}
}

// registerEntity 注册单个实体的六动作路由（sort 仅 drag_sort 的实体注册）
func (e *Engine) registerEntity(admin *gin.RouterGroup, row host.Entity) error {
	spec, err := Parse(row)
	if err != nil {
		return err
	}

	subGroup := admin.Group("plugin/" + spec.Plugin + "/" + spec.Name)
	subGroup.Use(host.Gate(spec.Plugin))

	entity := &entityHandler{engine: e, plugin: spec.Plugin, entity: spec.Name}

	// 与宿主基路由同序同形（internal/handler/base.go 的 RegisterBaseRoutes），只走 GET/POST
	subGroup.POST("/list", middleware.AdminAuth(), middleware.AdminPermission(), entity.List)
	subGroup.POST("/create", middleware.AdminAuth(), middleware.AdminPermission(), entity.Create)
	subGroup.POST("/delete", middleware.AdminAuth(), middleware.AdminPermission(), entity.Delete)
	subGroup.GET("/get/:pk", middleware.AdminAuth(), middleware.AdminPermission(), entity.Get)
	subGroup.POST("/update/:pk", middleware.AdminAuth(), middleware.AdminPermission(), entity.Update)
	if spec.DragSort() {
		subGroup.POST("/sort", middleware.AdminAuth(), middleware.AdminPermission(), entity.Sort)
	}
	return nil
}

// 编译期断言：六动作的方法签名与宿主 handler.IHandler 的精神一致（同名同参；L2 不复用泛型四层，
// 故不实现该接口——此处只钉住「动作名与签名形状不漂移」）。
var _ = [...]func(*entityHandler, *gin.Context){
	(*entityHandler).List,
	(*entityHandler).Get,
	(*entityHandler).Create,
	(*entityHandler).Update,
	(*entityHandler).Delete,
	(*entityHandler).Sort,
}
