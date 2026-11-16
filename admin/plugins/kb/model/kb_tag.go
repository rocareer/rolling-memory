// plugin: kb v1.0.0
package model

import (
	"time"

	"gorm.io/gorm"
)

// 标签系模型与闭集常量（表结构见 migrations/4_tags.up.sql）。
//
// 四表分工：
//   - kb_tag_group：分类/命名空间（scope 闭集 platform|library；**library_id 0=平台**）；
//   - kb_tag：组内平铺（组内唯一、组间可同名；不建 pid 树——T1）；
//   - kb_tag_link：锚定对象关联（**硬删**：解除即删行；uk ON CONFLICT 幂等；留痕走 kb_audit_log）；
//   - kb_tag_rule：规则打标（match_type 闭集；导入/重导批次内求值，重跑同结果）。
//
// 语义闭集一律走代码闸、加值零迁移（不加 DB CHECK；语义闭集走代码闸）——scope/object_type/match_type/
// status/enabled 即本节常量。标签**不参与可读性判定**（可读性只看 owner/grant，「不扩权」）。

// 标签作用域闭集（kb_tag_group.scope；T2 统一表 + scope 闭集）
const (
	// KBTagScopePlatform 平台作用域：library_id 恒 0；超管维护；对一切库可见（库可见标签 = 平台组 ∪ 本库组）
	KBTagScopePlatform = "platform"
	// KBTagScopeLibrary 库作用域：library_id = kb_library.id；属主/后台维护；不可被其它库引用
	KBTagScopeLibrary = "library"
)

// 标签锚定对象类型闭集（kb_tag_link.object_type；锚定对象 = (document|chunk) + 对象 id）
const (
	// KBTagObjectDocument 文档级：重切**不受影响**（object_id 恒指 kb_document.id）
	KBTagObjectDocument = "document"
	// KBTagObjectChunk 切片级：重切按 (document_id, text_hash) 重放（命中重挂 / 失配丢弃 + 审计）
	KBTagObjectChunk = "chunk"
)

// 标签规则匹配类型闭集（kb_tag_rule.match_type；代码闸，加值零迁移）
const (
	KBTagRuleMatchPathGlob      = "path_glob"      // 来源路径 glob
	KBTagRuleMatchFilenameRegex = "filename_regex" // 文件名正则
	KBTagRuleMatchSuffix        = "suffix"         // 后缀（扩展名）
	KBTagRuleMatchMetaEq        = "meta_eq"        // 元信息等值
)

// 审计动作扩展（标签面；动作闭集「加值零迁移」的落点——闭集本体在
// service/kb/audit.go 的 auditActions 单点，本处只给值）。
//
// 口径：绑定/重挂=tag_add；解绑/重切重放失配丢弃=tag_remove（丢弃以 detail.event='replay_drop' 区分）。
const (
	// KBAuditActionTagAdd 标签绑定（含重切重放的「命中重挂」）
	KBAuditActionTagAdd = "tag_add"
	// KBAuditActionTagRemove 标签解绑（含重切重放的「失配丢弃」）
	KBAuditActionTagRemove = "tag_remove"
)

// KBTagGroup 标签组模型（分类/命名空间；作用域经 scope + library_id 表达——0=平台）
type KBTagGroup struct {
	ID        uint            `gorm:"comment:ID;primarykey;autoIncrement" json:"id"`
	Scope     string          `gorm:"comment:作用域:platform=平台（library_id 恒 0）,library=库;type:varchar(16);not null;default:''" json:"scope"`
	LibraryID uint            `gorm:"comment:所属库ID（0=平台；禁 NULL——部分唯一索引下 NULL 互不相等）;column:library_id;not null;default:0" json:"library_id"`
	Name      string          `gorm:"comment:组名（同作用域内唯一，软删豁免）;type:varchar(64);not null;default:''" json:"name"`
	Weigh     int64           `gorm:"comment:排序权重（大者在前）;not null;default:0" json:"weigh"`
	UpdatedAt *time.Time      `gorm:"comment:更新时间" json:"updated_at"`
	CreatedAt *time.Time      `gorm:"comment:创建时间" json:"created_at"`
	DeletedAt *gorm.DeletedAt `gorm:"comment:删除时间（软删；既有标签关联保留待人工处置）;index" json:"-"`
}

// TableName 指定 KBTagGroup 模型表名（前缀由 GORM 命名策略添加）
func (KBTagGroup) TableName() string { return "kb_tag_group" }

// KBTag 标签模型（组内平铺：组内唯一、组间可同名；不建树——T1）
type KBTag struct {
	ID        uint            `gorm:"comment:ID;primarykey;autoIncrement" json:"id"`
	GroupID   uint            `gorm:"comment:所属标签组ID（作用域经组继承）;column:group_id;not null;default:0" json:"group_id"`
	Name      string          `gorm:"comment:标签名（组内唯一、组间可同名）;type:varchar(64);not null;default:''" json:"name"`
	Weigh     int64           `gorm:"comment:排序权重;not null;default:0" json:"weigh"`
	Status    *uint8          `gorm:"comment:状态:0=禁用,1=启用（禁用标签不可新绑定）;not null;default:1" json:"status"`
	UpdatedAt *time.Time      `gorm:"comment:更新时间" json:"updated_at"`
	CreatedAt *time.Time      `gorm:"comment:创建时间" json:"created_at"`
	DeletedAt *gorm.DeletedAt `gorm:"comment:删除时间（软删；既有 link 不自动清——回撤属投影重建）;index" json:"-"`
}

// TableName 指定 KBTag 模型表名
func (KBTag) TableName() string { return "kb_tag" }

// KBTagLink 标签关联模型（**硬删**：解除即删行，无软删列）
//
// uk(tag_id,object_type,object_id) = ON CONFLICT 幂等落点；留痕走 kb_audit_log（tag_add/tag_remove）；
// 随身列只有 operator_id（单列）——轴归属（admin/user）以同事务审计行为准（两 id 空间禁混用）。
type KBTagLink struct {
	ID         uint       `gorm:"comment:ID;primarykey;autoIncrement" json:"id"`
	TagID      uint       `gorm:"comment:标签ID;column:tag_id;not null;default:0" json:"tag_id"`
	ObjectType string     `gorm:"comment:锚定对象类型:document|chunk;column:object_type;type:varchar(16);not null;default:''" json:"object_type"`
	ObjectID   uint       `gorm:"comment:锚定对象ID（document→kb_document.id；chunk→kb_chunk.id）;column:object_id;not null;default:0" json:"object_id"`
	OperatorID uint       `gorm:"comment:操作者ID（轴归属以 kb_audit_log 为准；system 作业面=0）;column:operator_id;not null;default:0" json:"operator_id"`
	CreatedAt  *time.Time `gorm:"comment:创建时间（重放重挂=删旧行+插新行）" json:"created_at"`
}

// TableName 指定 KBTagLink 模型表名
func (KBTagLink) TableName() string { return "kb_tag_link" }

// KBTagRule 标签规则模型（导入/重导入批次内求值，重跑同结果幂等）
type KBTagRule struct {
	ID        uint            `gorm:"comment:ID;primarykey;autoIncrement" json:"id"`
	LibraryID uint            `gorm:"comment:所属库ID（规则随库作用，不跨库）;column:library_id;not null;default:0" json:"library_id"`
	Name      string          `gorm:"comment:规则名（库内唯一，软删豁免）;type:varchar(64);not null;default:''" json:"name"`
	Enabled   *uint8          `gorm:"comment:启用:0=停用,1=启用;not null;default:1" json:"enabled"`
	MatchType string          `gorm:"comment:匹配类型:path_glob|filename_regex|suffix|meta_eq（闭集在代码侧）;column:match_type;type:varchar(16);not null;default:''" json:"match_type"`
	Pattern   string          `gorm:"comment:匹配式（按 match_type 解释）;type:varchar(255);not null;default:''" json:"pattern"`
	TagID     uint            `gorm:"comment:命中后所打标签ID;column:tag_id;not null;default:0" json:"tag_id"`
	Priority  int64           `gorm:"comment:优先级（小者先评；同值按 id 升序）;not null;default:0" json:"priority"`
	UpdatedAt *time.Time      `gorm:"comment:更新时间" json:"updated_at"`
	CreatedAt *time.Time      `gorm:"comment:创建时间" json:"created_at"`
	DeletedAt *gorm.DeletedAt `gorm:"comment:删除时间（软删；回撤属投影重建）;index" json:"-"`
}

// TableName 指定 KBTagRule 模型表名
func (KBTagRule) TableName() string { return "kb_tag_rule" }
