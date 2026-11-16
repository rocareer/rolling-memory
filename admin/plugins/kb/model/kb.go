// plugin: kb v1.0.0
package model

import (
	"time"

	"gorm.io/gorm"
)

// KB 模块闭集常量：
// 语义闭集走代码闸、不加 DB CHECK —— 语义闭集走代码闸；加值零迁移）。

// 库属主类型闭集（kb_library.owner_kind；创建后不可改）
const (
	KBLibraryOwnerKindSystem = "system" // 系统级：owner_id=0；仅后台创建/管理
	KBLibraryOwnerKindUser   = "user"   // 用户级：owner_id=users.id（username 可被后台改，禁作键）
)

// 文档来源闭集（kb_document.source_kind）
const (
	KBDocumentSourceFile = "file"
	KBDocumentSourceText = "text"
	KBDocumentSourceURL  = "url"
)

// 文档构建状态闭集（kb_document.build_state；状态机，旁路 failed/canceled 带 stage/reason）
const (
	KBDocumentBuildRegistered = "registered" // 初始态：已登记待提取
	KBDocumentBuildExtracting = "extracting"
	KBDocumentBuildExtracted  = "extracted"
	KBDocumentBuildChunking   = "chunking"
	KBDocumentBuildChunked    = "chunked"
	KBDocumentBuildEmbedding  = "embedding"
	KBDocumentBuildReady      = "ready"    // 就绪：检索面可见的唯一终态
	KBDocumentBuildFailed     = "failed"   // 旁路：见 failed_stage/failed_reason
	KBDocumentBuildCanceled   = "canceled" // 旁路：用户取消终态
)

// 授权主体闭集（kb_grant.subject_kind；subject_id 口径：all=0 哨兵，team=kb_team.id，user=users.id）
const (
	KBGrantSubjectAll  = "all"
	KBGrantSubjectTeam = "team"
	KBGrantSubjectUser = "user"
)

// 授权权限轴闭集（kb_grant.perm；两轴正交：write 不隐含 read，读写双授落两行）
const (
	KBGrantPermRead  = "read"
	KBGrantPermWrite = "write"
)

// 操作者类型闭集（kb_document.created_by_kind / kb_grant.granted_by_kind / kb_audit_log.actor_kind）
// 注意：admin 与 user 是两个独立自增空间，禁混用（读视图硬条款）
const (
	KBActorKindAdmin  = "admin"
	KBActorKindUser   = "user"
	KBActorKindSystem = "system"
)

// 审计动作闭集（kb_audit_log.action；只增不改不删）
const (
	KBAuditActionCreate        = "create"
	KBAuditActionUpdate        = "update"
	KBAuditActionDelete        = "delete"
	KBAuditActionGrantAdd      = "grant_add"
	KBAuditActionGrantRemove   = "grant_remove"
	KBAuditActionOwnerTransfer = "owner_transfer"
	KBAuditActionPurge         = "purge"
	KBAuditActionImport        = "import"
)

// KBLibrary 知识库模型（聚合根：文档 + 切片 + 授权；唯一属主 owner_kind/owner_id）
//
// 可见性单一来源 = kb_grant（本节仅承载归属与管理权）；owner_kind 创建后不可改；
// 用户级→系统级收编仅允许后台显式操作 + 审计（owner_transfer）。
type KBLibrary struct {
	ID          uint            `gorm:"comment:ID;primarykey;autoIncrement" json:"id"`
	OwnerKind   string          `gorm:"comment:属主类型:system=系统级（owner_id=0）,user=用户级（owner_id=users.id）;type:varchar(16);not null;default:''" json:"owner_kind"`
	OwnerID     uint            `gorm:"comment:属主ID（system=0）;not null;default:0" json:"owner_id"`
	Name        string          `gorm:"comment:库名（同属主内唯一，软删豁免；uk 部分唯一）;type:varchar(128);not null;default:''" json:"name"`
	Description string          `gorm:"comment:描述;type:varchar(512);not null;default:''" json:"description"`
	ChunkConfig *string         `gorm:"comment:库级切片配置（JSON；三层覆盖 global▷library▷document 的中间层；NULL=未配置）；未知键写侧拒绝;type:jsonb" json:"chunk_config"`
	Status      *uint8          `gorm:"comment:状态:0=禁用,1=启用（禁用库对一切读路径不可见）;not null;default:1" json:"status"`
	UpdatedAt   *time.Time      `gorm:"comment:更新时间（属主清单排序轴）" json:"updated_at"`
	CreatedAt   *time.Time      `gorm:"comment:创建时间" json:"created_at"`
	DeletedAt   *gorm.DeletedAt `gorm:"comment:删除时间（软删即刻不可见；异步 kb:purge 清尾巴）;index" json:"-"`

	// OwnerUsername 属主用户名（**展示字段，非列**：owner_kind=user 时由列表/详情查询回填 users.username；
	// 属主已删/查无 ⇒ 空串，前端回落裸 owner_id）。标识键仍是 owner_id（username 可改，禁作键——）。
	OwnerUsername string `gorm:"-" json:"owner_username"`
}

// TableName 指定 KBLibrary 模型表名（前缀由 GORM 命名策略添加）
func (KBLibrary) TableName() string {
	return "kb_library"
}

// KBDocument 文档模型（库内可追溯知识单元）
//
// content_hash = 原文 sha1（导入幂等键：同库同内容重复登记被 uk 拦下）；
// build_state 为状态机载体；active_build 指向当前 build_hash（重切原子换出，换窗期检索腿钉它）；
// 源文件字节不落本表（C7 私有内容寻址 kb_blob）。
type KBDocument struct {
	ID              uint            `gorm:"comment:ID;primarykey;autoIncrement" json:"id"`
	LibraryID       uint            `gorm:"comment:所属库ID;not null;default:0" json:"library_id"`
	Title           string          `gorm:"comment:标题（列表快搜轴，trgm GIN 支撑）;type:varchar(255);not null;default:''" json:"title"`
	SourceKind      string          `gorm:"comment:来源:file=文件,text=文本,url=链接;type:varchar(32);not null;default:''" json:"source_kind"`
	SourceRef       string          `gorm:"comment:来源引用（外部 ref 文本；不复制文件）;type:text;not null;default:''" json:"source_ref"`
	ContentHash     string          `gorm:"comment:内容身份=原文 sha1（导入幂等键：uk(library_id,content_hash) 部分唯一）;column:content_hash;type:char(40);not null;default:''" json:"content_hash"`
	Mime            string          `gorm:"comment:MIME 类型;type:varchar(100);not null;default:''" json:"mime"`
	Size            int64           `gorm:"comment:字节数;not null;default:0" json:"size"`
	BuildState      string          `gorm:"comment:构建状态:registered/extracting/extracted/chunking/chunked/embedding/ready（旁路 failed/canceled）;type:varchar(16);not null;default:'registered'" json:"build_state"`
	ActiveBuild     string          `gorm:"comment:当前生效 build_hash（重切原子换出点；空=未产出过）;column:active_build;type:char(40);not null;default:''" json:"active_build"`
	FailedStage     string          `gorm:"comment:失败阶段（build_state=failed 时有意义）;column:failed_stage;type:varchar(32);not null;default:''" json:"failed_stage"`
	FailedReason    string          `gorm:"comment:失败原因（响亮失败：零文本/覆盖率/乱码率超阈）;column:failed_reason;type:varchar(512);not null;default:''" json:"failed_reason"`
	CancelRequested *uint8          `gorm:"comment:取消请求:0=无,1=已请求（jobs Cancel 仅限 pending，运行中取消走本业务标志）;column:cancel_requested;not null;default:0" json:"cancel_requested"`
	ChunkConfig     *string         `gorm:"comment:文档级切片配置（JSON；三层覆盖的末层；NULL=继承库/全局）;type:jsonb" json:"chunk_config"`
	CreatedByKind   string          `gorm:"comment:创建者类型:admin=后台代建,user=前台（写面）;column:created_by_kind;type:varchar(16);not null;default:''" json:"created_by_kind"`
	CreatedByID     uint            `gorm:"comment:创建者ID（admin→admins.id，user→users.id；两 id 空间分列禁混用）;column:created_by_id;not null;default:0" json:"created_by_id"`
	UpdatedAt       *time.Time      `gorm:"comment:更新时间" json:"updated_at"`
	CreatedAt       *time.Time      `gorm:"comment:创建时间" json:"created_at"`
	DeletedAt       *gorm.DeletedAt `gorm:"comment:删除时间（软删即刻 + kb:doc:<id> 入队清块/向量）;index" json:"-"`
}

// TableName 指定 KBDocument 模型表名
func (KBDocument) TableName() string {
	return "kb_document"
}

// KBChunk 切片模型（派生面：无软删，重切即删重建）
//
// 定位真值 = char_start/char_end + text_hash + chunker_version（seq 是显示缓存，512→256 换档不得据它反推）；
// 锚 = locator（<doc_locator>#pN，锚语法同构）；text 为 verbatim 原文（大字段：禁进列表 Select）；
// match_text 为派生文本（匹配面唯一真源，chunk.ChunkTextOf 单点）；tsv 为 DB 生成列（恒自 match_text 派生），
// 二者不进列表列面；向量行在 kb_chunk_vectors。
type KBChunk struct {
	ID             uint       `gorm:"comment:ID;primarykey;autoIncrement" json:"id"`
	LibraryID      uint       `gorm:"comment:所属库ID（冗余过滤列）;not null;default:0" json:"library_id"`
	DocumentID     uint       `gorm:"comment:所属文档ID;not null;default:0" json:"document_id"`
	BuildHash      string     `gorm:"comment:构建身份 sha1（含 content_hash/config_hash/chunker_version/extractor@version）;column:build_hash;type:char(40);not null;default:''" json:"build_hash"`
	ConfigHash     string     `gorm:"comment:有效切片配置 sha1（build_hash 组成项；可自 config_snapshot 复算）;column:config_hash;type:char(40);not null;default:''" json:"config_hash"`
	Chunker        string     `gorm:"comment:切片策略名（代码闭集注册表）；code=代码,heading=标题层级等;type:varchar(32);not null;default:''" json:"chunker"`
	ChunkerVersion string     `gorm:"comment:切片器版本（锚可复算要素之一）;column:chunker_version;type:varchar(32);not null;default:''" json:"chunker_version"`
	Seq            int        `gorm:"comment:块序（显示缓存，非定位真值）;not null;default:0" json:"seq"`
	Locator        string     `gorm:"comment:锚=<doc_locator>#pN（锚语法同构，可反解析回父单元）;type:text;not null;default:''" json:"locator"`
	CharStart      int        `gorm:"comment:字符区间起（定位真值，rune 口径）;column:char_start;not null;default:0" json:"char_start"`
	CharEnd        int        `gorm:"comment:字符区间止（定位真值）;column:char_end;not null;default:0" json:"char_end"`
	HeadingPath    *string    `gorm:"comment:标题路径（JSON 数组；heading 策略产出）;column:heading_path;type:jsonb" json:"heading_path"`
	PageFrom       int        `gorm:"comment:起始页（1 起；0=不适用）;column:page_from;not null;default:0" json:"page_from"`
	PageTo         int        `gorm:"comment:截止页（0=不适用）;column:page_to;not null;default:0" json:"page_to"`
	TimeFrom       int64      `gorm:"comment:时间区间起（BIGINT 秒；0=不适用——音视频/会话）;column:time_from;not null;default:0" json:"time_from"`
	TimeTo         int64      `gorm:"comment:时间区间止（BIGINT 秒；0=不适用）;column:time_to;not null;default:0" json:"time_to"`
	TableMeta      *string    `gorm:"comment:表格元信息（JSON：表头/行列区间；table 策略产出）;column:table_meta;type:jsonb" json:"table_meta"`
	Text           string     `gorm:"comment:切片正文（verbatim 原文；写入与检索经 ChunkTextOf 单点派生）;type:text;not null;default:''" json:"text"`
	MatchText      string     `gorm:"comment:派生文本（匹配面唯一真源=chunk.ChunkTextOf：标题路径+表头+verbatim 正文；空串=未回填，aigo kb reindex 幂等复跑）;column:match_text;type:text;not null;default:''" json:"match_text"`
	TextHash       string     `gorm:"comment:切片正文 sha1（重切重放/幂等判据）;column:text_hash;type:char(40);not null;default:''" json:"text_hash"`
	TokenCount     int        `gorm:"comment:token 估算数（成本口径）;column:token_count;not null;default:0" json:"token_count"`
	ConfigSnapshot *string    `gorm:"comment:本次切分的有效配置快照（JSON；复算 config_hash 的载体）;column:config_snapshot;type:jsonb" json:"config_snapshot"`
	CreatedAt      *time.Time `gorm:"comment:创建时间（重切即删重建，不设 updated_at）" json:"created_at"`
}

// TableName 指定 KBChunk 模型表名
func (KBChunk) TableName() string {
	return "kb_chunk"
}

// KBGrant 授权模型（可见性单一来源：可枚举 grant 行）
//
// 公开 = subject_kind='all' 一行（subject_id=0 哨兵）；撤销 = 软删 grant 行（即时生效）；
// uk(library_id,subject_kind,subject_id,perm) 部分唯一：读写两轴正交，双授落两行。
type KBGrant struct {
	ID            uint            `gorm:"comment:ID;primarykey;autoIncrement" json:"id"`
	LibraryID     uint            `gorm:"comment:被授权库ID;not null;default:0" json:"library_id"`
	SubjectKind   string          `gorm:"comment:主体类型:all=公开（subject_id=0 哨兵）,team=团队（kb_team.id）,user=用户（users.id）;column:subject_kind;type:varchar(16);not null;default:''" json:"subject_kind"`
	SubjectID     uint            `gorm:"comment:主体ID（all 恒 0；喂 0 只对 all 合法——入口守卫禁 actor_id≤0 放大）;column:subject_id;not null;default:0" json:"subject_id"`
	Perm          string          `gorm:"comment:权限轴:read|write（正交：write 不扩读；读谓词显式要求 read）;type:varchar(16);not null;default:'read'" json:"perm"`
	GrantedByKind string          `gorm:"comment:操作者类型:admin|user;column:granted_by_kind;type:varchar(16);not null;default:''" json:"granted_by_kind"`
	GrantedByID   uint            `gorm:"comment:操作者ID（与 kb_audit_log 互为佐证）;column:granted_by_id;not null;default:0" json:"granted_by_id"`
	UpdatedAt     *time.Time      `gorm:"comment:更新时间" json:"updated_at"`
	CreatedAt     *time.Time      `gorm:"comment:创建时间" json:"created_at"`
	DeletedAt     *gorm.DeletedAt `gorm:"comment:删除时间（撤销=软删，读路径即时不可见；重授可行）;index" json:"-"`
}

// TableName 指定 KBGrant 模型表名
func (KBGrant) TableName() string {
	return "kb_grant"
}

// KBTeam 团队映射模型（稳定 id ↔ user_groups.name 绑定；成员判定仍走 MemberOfAny 语义单点）
//
// 按名绑定是仓内既定语义（按名绑定：同名的组即同语义组）；组改名 = 改一行绑定（grant 行不动）；
// bound_group_id 记绑定时 FindByName 最早命中 id（供 aigo kb check 巡检比对回收）；组删除/改名悬空 = fail-closed + 巡检点名。
type KBTeam struct {
	ID           uint            `gorm:"comment:ID;primarykey;autoIncrement" json:"id"`
	GroupName    string          `gorm:"comment:绑定的组名（user_groups.name；按名匹配，软删豁免 uk）;column:group_name;type:varchar(64);not null;default:''" json:"group_name"`
	BoundGroupID uint            `gorm:"comment:绑定时 FindByName 最早命中组ID（巡检比对回收：现行 id 与之不符即点名）;column:bound_group_id;not null;default:0" json:"bound_group_id"`
	DisplayName  string          `gorm:"comment:显示名;column:display_name;type:varchar(128);not null;default:''" json:"display_name"`
	Status       *uint8          `gorm:"comment:状态:0=禁用,1=启用（禁用团队不参与读视图 teams(u) 判定）;not null;default:1" json:"status"`
	UpdatedAt    *time.Time      `gorm:"comment:更新时间" json:"updated_at"`
	CreatedAt    *time.Time      `gorm:"comment:创建时间" json:"created_at"`
	DeletedAt    *gorm.DeletedAt `gorm:"comment:删除时间（删绑定=授权行不动，悬空由巡检点名兜底）;index" json:"-"`
}

// TableName 指定 KBTeam 模型表名
func (KBTeam) TableName() string {
	return "kb_team"
}

// KBAuditLog 审计模型（只增不改不删，无软删）
//
// 写入面：grant_add/grant_remove/owner_transfer 与业务变更同事务（I5）；覆盖前台与作业行为；
// 列表只读、按时间窗 + 上限（禁全表 count）。
type KBAuditLog struct {
	ID         uint       `gorm:"comment:ID;primarykey;autoIncrement" json:"id"`
	LibraryID  uint       `gorm:"comment:关联库ID（0=无库上下文）;column:library_id;not null;default:0" json:"library_id"`
	DocumentID uint       `gorm:"comment:关联文档ID（0=非文档对象）;column:document_id;not null;default:0" json:"document_id"`
	ActorKind  string     `gorm:"comment:操作者类型:admin|user|system;column:actor_kind;type:varchar(16);not null;default:''" json:"actor_kind"`
	ActorID    uint       `gorm:"comment:操作者ID（admin→admins.id，user→users.id，system=0）;column:actor_id;not null;default:0" json:"actor_id"`
	Action     string     `gorm:"comment:动作:create/update/delete/grant_add/grant_remove/owner_transfer/purge/import;type:varchar(32);not null;default:''" json:"action"`
	Detail     *string    `gorm:"comment:变更明细（JSON：前后值/授权主体等）;type:jsonb;not null;default:'{}'" json:"detail"`
	CreatedAt  *time.Time `gorm:"comment:创建时间" json:"created_at"`
}

// TableName 指定 KBAuditLog 模型表名
func (KBAuditLog) TableName() string {
	return "kb_audit_log"
}
