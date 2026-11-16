package host

import (
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/model"

	"gorm.io/datatypes"
)

// 插件生命周期状态（admin_plugins.status；四态状态机）。
// 台账记「期望状态」，是否生效另由进程内注册表与运行时闸决定。
const (
	StatusFailed    = "failed"    // 半装：编排失败且补偿回滚失败，只允许 uninstall --force 清理
	StatusInstalled = "installed" // 已安装但菜单隐藏（闸拒 404），待 enable
	StatusEnabled   = "enabled"   // 启用：唯一被闸放行的状态
	StatusDisabled  = "disabled"  // 已停用（菜单隐藏，闸拒 404）
)

// Plugin 插件台账行（表 admin_plugins）。
//
// **别名复用而非第二份定义**：权威模型是 internal/model/plugin.go 的 model.Plugin（带 gorm 列标签，
// 四层管理面用），host 侧（预检、闸、CLI 状态机）只读同一类型 —— 列漂移在编译期就暴露，
// 不存在「改列时两处同改」的人工约定（此前两份结构体同改的欠账由此关闭）。
type Plugin = model.Plugin

// Entity L2 实体元数据行（表 admin_plugin_entities）。
// 四层管理面的行别名在 internal/repository/admin/plugin（PluginEntity = host.Entity），
// 唯一定义在本包（model 侧不另立一份，避免再次出现「两处同改」）。
type Entity struct {
	ID uint `json:"id"`
	// Plugin 归属插件名（= admin_plugins.name）
	Plugin string `json:"plugin"`
	// Name 实体名（闭集标识符：^[a-z][a-z0-9_]{0,62}$）
	Name string `json:"name"`
	// TitleZh / TitleEn 实体标题（文案随响应下发，绕开构建期语言包）
	TitleZh string `json:"title_zh"`
	TitleEn string `json:"title_en"`
	// PhysicalTable 物理表名（元数据显式存实际值，不由命名规则现场推导）
	PhysicalTable string         `gorm:"column:table_name" json:"table_name"`
	Spec          datatypes.JSON `json:"spec"`
	CreatedAt     *time.Time     `json:"created_at"`
	UpdatedAt     *time.Time     `json:"updated_at"`
}

// TableName 固定表名；表前缀由迁移的 __PREFIX__ 占位符承担
func (Entity) TableName() string {
	return "admin_plugin_entities"
}
