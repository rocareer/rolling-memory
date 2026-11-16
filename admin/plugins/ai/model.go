// plugin: ai v0.2.0
// Package ai 是 AI 路由插件（kind: mixed）的 Go 包。
//
// 本文件是数据契约的两项基础设施：
//   - table()：物理表名 = <config.database.prefix>plugin_ai_<entity>，前缀先过标识符白名单；
//   - Candidate：候选集查询的扁平行（渠道调用字段 + 厂商继承列 + 绑定 priority/weight/status）。
//
// 各表的 GORM 行模型由领域文件就地声明（Channel/Model/ModelChannel 在 router.go，
// BreakerEvent 在 circuit.go，Usage 在 billing.go），它们的 TableName() 都调用本文件的
// table()——单一出口，避免每处各拼一遍前缀。改列时以 plugins/ai/migrations/1_init.up.sql 为锚同步。
//
// 表名纪律：不能依赖 GORM 的 NamingStrategy.TablePrefix——实现 TableName() 的模型不走命名策略
// （gorm@v1.31.1/schema/schema.go:186-191 的 Tabler 分支直接取 TableName() 返回值），
// 且宿主只在前缀非空时才设 NamingStrategy（internal/infra/database/database.go:38-42）。
package ai

import (
	"regexp"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"
)

// 逻辑模型调用类型闭集（plugin_ai_model.type）：chat|embedding。
// 其余列闭集定义在领域文件里（单一事实源）：
//   - 状态/策略/调用类型：router.go（Status*/Strategy*/CallType*）
//   - 熔断三态与事件类型：circuit.go（Circuit*/Event*）
//   - usage 状态与 token 来源：billing.go（UsageStatus*/TokensSource*）
//   - 失败分类：driver.go（ErrorClass*）
const (
	ModelTypeChat      = "chat"
	ModelTypeEmbedding = "embedding"
)

// identifierSafe 标识符白名单：配置表前缀来自外部，拼进 SQL 前必须复核
// （口径同 plugins/demo/plugin.go 的 notesTableSafe 与 internal/plugin/cli 的 identifierSafe）。
var identifierSafe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// table 物理表名的唯一出口：<config.database.prefix> + 固定逻辑名。
//
// 前缀非法（含引号/分号/空白等）时 panic：这是配置损坏的部署错误，宁可响亮失败，
// 也绝不静默拼出一张同名的错误表（fail-closed）。
func table(logical string) string {
	if !identifierSafe.MatchString(logical) {
		panic("plugin/ai: 逻辑表名 " + logical + " 不是安全标识符（代码常量异常）")
	}
	prefix := config.Get().Database.Prefix
	if prefix != "" && !identifierSafe.MatchString(prefix) {
		panic("plugin/ai: 数据库表前缀 " + prefix + " 不是安全标识符（配置异常）：拒绝拼表名")
	}
	return prefix + logical
}

// Candidate 是候选集查询的扁平行：渠道调用字段 + 绑定行的 priority/weight/status。
//
// 不嵌套 Channel——Router 的 ChannelSnapshot() 做唯一一次映射（router.go），
// 避免两套结构在字段更新时漂移。APIKey 是上游明文凭证：本类型**没有 JSON 标签**，
// 绝不参与序列化；日志与错误文案都不得包含该字段。
//
// 继承（三级配置的中间两级）：Supplier* 四列是所挂厂商（supplier_id>0）的候选继承列，
// 由 store.Candidates 的 LEFT JOIN 一次取回；BaseURL / APIKey / Driver / Timeout 在返回前
// 已被 mergeSupplierInheritance 合并成**生效值**（渠道自身非空优先、空则取厂商、再无则留零值
// 由插件缺省兜底）。SupplierAPIKey 同样是明文凭证 ⇒ 本类型整体不进 JSON。
type Candidate struct {
	ChannelID uint64 `gorm:"column:channel_id"`
	Name      string `gorm:"column:name"`
	Vendor    string `gorm:"column:vendor"`
	// Driver 是渠道生效驱动名（空串=默认 OpenAI 兼容驱动；驱动注册表按它分派）。
	Driver     string `gorm:"column:driver"`
	SupplierID uint64 `gorm:"column:supplier_id"`
	// RPMLimit / TPMLimit 是渠道级配额（0=未配；准入门实现见 gate.go）。
	RPMLimit            int    `gorm:"column:rpm_limit"`
	TPMLimit            int    `gorm:"column:tpm_limit"`
	BaseURL             string `gorm:"column:base_url"`
	APIKey              string `gorm:"column:api_key"`
	Model               string `gorm:"column:model"`
	Timeout             int    `gorm:"column:timeout"`
	ChannelStatus       int16  `gorm:"column:channel_status"`
	FailThreshold       int    `gorm:"column:fail_threshold"`
	CooldownSeconds     int    `gorm:"column:cooldown_seconds"`
	Circuit             string `gorm:"column:circuit"`
	ConsecutiveFailures int    `gorm:"column:consecutive_failures"`
	OpenedAt            *int64 `gorm:"column:opened_at"`
	LastSuccessAt       *int64 `gorm:"column:last_success_at"`
	LastError           string `gorm:"column:last_error"`
	// 厂商继承列（LEFT JOIN 未命中时由 SQL 的 COALESCE 收敛成零值，见 store.go 的 channelSupplierJoin）。
	SupplierBaseURL string `gorm:"column:supplier_base_url"`
	SupplierAPIKey  string `gorm:"column:supplier_api_key"`
	SupplierDriver  string `gorm:"column:supplier_driver"`
	SupplierTimeout int    `gorm:"column:supplier_timeout"`
	ModelID         uint64 `gorm:"column:model_id"`
	BindingPriority int    `gorm:"column:binding_priority"`
	BindingWeight   int    `gorm:"column:binding_weight"`
	BindingStatus   int16  `gorm:"column:binding_status"`
}

// ModelChannelRow 是 ModelChannel 的兼容别名（router.go 的 Binding 注释按此名引用；
// ModelChannel 已实现 Snapshot() Binding，本别名不重复定义方法）。
type ModelChannelRow = ModelChannel
