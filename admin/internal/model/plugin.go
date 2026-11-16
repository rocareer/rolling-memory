package model

import (
	"time"
)

// Plugin 插件台账模型（表 admin_plugins；设计说明台账字段表）。
//
// 列的唯一事实源是框架迁移（`cmd/migrate/migrations/000005_plugin.up.sql`，跨仓合并时仅改名重编号）——
// 本结构与之逐列对齐。同表的只读使用方（P1 预检与运行时闸）经 `type Plugin = model.Plugin` **别名复用**
// 本类型（host/model.go），全仓只有这一份字段定义；「改列时两处同改」的人工约定已删除。
//
// `status` 是**期望状态**（failed/installed/enabled/disabled，闭集见 host.Status* 常量）；
// 「是否生效」另由进程内注册表与运行时闸决定——两处不一致时由管理页与 `aigo plugin list` 显式报告差异，
// 不做静默对齐。
type Plugin struct {
	ID      uint   `gorm:"comment:ID;primarykey;autoIncrement" json:"id"`
	Name    string `gorm:"comment:插件名;type:varchar(32);not null;default:''" json:"name"`
	Title   string `gorm:"comment:展示名;type:varchar(64);not null;default:''" json:"title"`
	Version string `gorm:"comment:插件版本;type:varchar(32);not null;default:''" json:"version"`
	// Kind 形态：code=源码插件，declarative=声明式，mixed=混合（闭集与迁移的 CHECK 约束一致）
	Kind string `gorm:"comment:形态:code=源码插件,declarative=声明式,mixed=混合;type:varchar(16);not null;default:'code'" json:"kind"`
	// Status 四态状态机；默认 failed 是 fail-closed：手工插入的行不应被当作已安装
	Status string `gorm:"comment:状态:failed=半装,installed=已安装,enabled=启用,disabled=停用;type:varchar(16);not null;default:'failed'" json:"status"`
	// HostAPI 安装时校验过的机制接口版本域（对 host.APIVersion）
	HostAPI string `gorm:"comment:安装时校验过的机制接口版本域;type:varchar(64);not null;default:''" json:"host_api"`
	// HostVersion 安装时的宿主版本（version.Get().Version；仅诊断，不参与校验）
	HostVersion string `gorm:"comment:安装时的宿主版本(仅诊断);type:varchar(32);not null;default:''" json:"host_version"`
	// FrameworkReq 框架线版本域（只记录 + 告警，不拒装）
	FrameworkReq string `gorm:"comment:框架线版本域(只记录不拒装);type:varchar(64);not null;default:''" json:"framework_req"`
	// Requires 依赖插件列表（name@range，逗号分隔）
	Requires string `gorm:"comment:依赖插件(name@range,逗号分隔);type:text;not null;default:''" json:"requires"`
	// AdoptedThrough 存量库迁移基线（adopt）的接管边界：> 0 = 本插件以「1..该版本与核心链对应迁移同物」
	// 的基线口径装入（安装期只写插件版本表、未执行任何旧 DDL）；--purge 对它的默认拒绝门禁读这一列。
	// 设计说明（框架数据库迁移增列）。
	AdoptedThrough int `gorm:"comment:接管段(>0=adopt装入,1..该版本与核心链同物);not null;default:0" json:"adopted_through"`
	// Checksum 整包 sha256（来源认证由 SignedBy 承载）
	Checksum    string     `gorm:"comment:整包sha256;type:varchar(64);not null;default:''" json:"checksum"`
	Source      string     `gorm:"comment:安装来源;type:varchar(255);not null;default:''" json:"source"`
	SignedBy    string     `gorm:"comment:签名公钥指纹(空=未签名);type:varchar(128);not null;default:''" json:"signed_by"`
	InstalledAt *time.Time `gorm:"comment:安装时间" json:"installed_at"`
	UpdatedAt   *time.Time `gorm:"comment:更新时间" json:"updated_at"`
	Remark      string     `gorm:"comment:备注;type:varchar(255);not null;default:''" json:"remark"`
}

// TableName 固定逻辑表名；物理前缀由迁移的 __PREFIX__ 占位符与 GORM 命名策略承担
// （与 model.AdminRule 等既有模型同款：此处不写前缀）。
func (Plugin) TableName() string {
	return "admin_plugins"
}
