// plugin: demo v1.0.0
// Package demo 是示例插件（kind: mixed）的 Go 包。
//
// 只做两件事（三层分工里「插件」那一层）：
//   - init()：把自身登记进宿主注册表（注册表侧只依赖 internal/plugin/host；**不要** import
//     internal/plugins——那会构成 Go 非法 import cycle，注册表与被空白导入的包必须分属不同包）；
//   - mount()：在宿主建好、已挂运行时闸的 `plugin/demo` 子组上挂 L1 自定义页的数据端点
//     （只写相对段；并自挂 AdminAuth + AdminPermission，见下方注释）。
//
// L2 实体（items）不需要本包做任何事：六动作路由由 internal/plugin/l2 按 admin_plugin_entities 的
// 元数据在启动时注册，字段与文案改动即时生效。插件迁移自管的表由 migrations/ 建。
package demo

import (
	"context"
	"errors"
	"regexp"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"
	"github.com/ai-go-hub/ai-go-admin/internal/infra/database"
	"github.com/ai-go-hub/ai-go-admin/internal/kit/httpx"
	"github.com/ai-go-hub/ai-go-admin/internal/middleware"
	"github.com/ai-go-hub/ai-go-admin/internal/model"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"
	"github.com/ai-go-hub/ai-go-admin/internal/version"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 插件自身的事实（与 plugin.yaml 逐字对齐；重编译后进程内注册表以本处常量为准，「接线真相」）
const (
	// Name 插件名（= plugins/ 目录名 = admin_plugins.name = 路由段 plugin/<名>）
	Name = "demo"
	// Version 插件版本（= plugin.yaml 的 version）
	Version = "1.0.0"
	// Kind 插件形态（= plugin.yaml 的 kind）
	Kind = "mixed"
	// Entity L2 实体名（= admin_plugin_entities.name；表结构由引擎按 spec 管）
	Entity = "items"
	// configKeyPageHint 自带配置键的落库名（安装器写 configs 的 plugin.demo 分组；合形见 cli.ConfigKey）
	configKeyPageHint = "plugin_demo_page_hint"
	// notesTableSuffix 插件迁移自管的表（migrations/1_init.up.sql 建；表前缀由安装期配置叠加）
	notesTableSuffix = "plugin_demo_notes"
)

// notesTableSafe 自管表名的标识符复核：前缀来自配置、拼进 SQL 前先过白名单（口径同 cli.identifierSafe）
var notesTableSafe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func init() {
	host.Register(host.Meta{Name: Name, Version: Version, Kind: Kind, Mount: mount})
}

// mount L1 路由挂载点：宿主给的是**已挂运行时闸**的 `plugin/demo` 子组。
// 只写相对段——写绝对前缀会双前缀，且权限节点名与 manifest 的 menus[].name 不再逐字相等。
//
// **每条后台路由必须自己挂 AdminAuth + AdminPermission**（本项目既有口径：宿主泛型基路由
// internal/handler/base.go:17-25、L2 实体路由 internal/plugin/l2/router.go:100-104 的每个动作都这么挂）：
// 宿主只提供「子组 + 运行时闸」两件事——**闸**管「插件是否启用（enabled）」，**节点**管「这个管理员能不能用」。
// 不挂中间件的话，manifest 里声明的 plugin/demo/overview 就只是个装饰：任何持有任意有效后台会话的人
// 都能直接打到本端点（插件子组对所有人开放）。挂上之后的语义：未登录 401、无该节点 403、有节点放行。
func mount(g *gin.RouterGroup) {
	g.POST("/overview", middleware.AdminAuth(), middleware.AdminPermission(), overview)
}

// overview 概览（POST /<后台入口前缀>/plugin/demo/overview；权限节点 plugin/demo/overview）：
// 读「插件自身的四项事实」给 L1 页——注册表常量、L2 实体元数据、迁移自管表的最新一行、自带配置键。
// 读不到就如实失败（fail-closed），不给页面一个看着正常的空态。
func overview(c *gin.Context) {
	ctx := c.Request.Context()
	db := database.DB()
	if db == nil {
		httpx.Fail(c, httpx.WithMessage("概览失败：数据库未初始化"))
		return
	}

	entity, err := gorm.G[host.Entity](db).Where("plugin = ? AND name = ?", Name, Entity).First(ctx)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("概览失败：读取实体元数据（"+Name+"/"+Entity+"）——插件未安装？: "+err.Error()))
		return
	}

	note, err := latestNote(ctx, db)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("概览失败：读取插件自管表: "+err.Error()))
		return
	}

	hint, err := pageHint(ctx, db)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("概览失败：读取自带配置键: "+err.Error()))
		return
	}

	httpx.Success(c, httpx.WithData(gin.H{
		"name":             Name,
		"version":          Version,
		"kind":             Kind,
		"host_api_version": host.APIVersion,
		"host_version":     version.Get().Version,
		"entity":           Entity,
		"entity_table":     entity.PhysicalTable, // 物理表名的唯一事实源 = 元数据行本身
		"note":             note,
		"page_hint":        hint,
	}))
}

// latestNote 迁移自管表的最新一行（L1 数据面的样板读法：显式 Select + 主键倒序 + LIMIT 1，SQL 纪律）
func latestNote(ctx context.Context, db *gorm.DB) (string, error) {
	table := config.Get().Database.Prefix + notesTableSuffix
	if !notesTableSafe.MatchString(table) {
		return "", errors.New("自管表名 " + table + " 不是安全标识符（配置的表前缀异常）：拒绝拼进 SQL")
	}
	rows := make([]struct {
		Body string `gorm:"column:body"`
	}, 0, 1)
	if err := db.WithContext(ctx).Table(table).Select("body").Order("id DESC").Limit(1).Find(&rows).Error; err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", nil
	}
	return rows[0].Body, nil
}

// pageHint 自带配置键的当前值（安装器写入 configs 的 plugin.demo 分组；缺行按空串）
func pageHint(ctx context.Context, db *gorm.DB) (string, error) {
	row, err := gorm.G[model.Config](db).Select("value").Where("name = ?", configKeyPageHint).First(ctx)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if row.Value == nil {
		return "", nil
	}
	return *row.Value, nil
}
