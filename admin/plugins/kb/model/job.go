// plugin: kb v1.0.0
// 本插件作业账本模型（kb:ingest / kb:rebuild / kb:purge / kb:tag / kb:prune 五类作业的读模型）。
//
// 为什么在本插件内自建账本：宿主没有可供插件复用的、与「插件迁移隔离」相容的作业面
// （插件的表由插件自管迁移建、插件不得写宿主自管表），故本插件自带一张最小账本表，
// 语义与作业面（service/jobs）成对：
//   - 单飞判据 = 部分唯一索引 uk_plugin_kb_jobs_active_key（同 key 至多一条 pending/running）；
//   - 领取 = `FOR UPDATE SKIP LOCKED` + 租约（locked_until），租约到期可被重领；
//   - 进度 done 单调不回退；终态 = done/failed/canceled；
//   - 表名 plugin_kb_jobs（物理名 = GORM NamingStrategy 的表前缀 + 本名），与迁移里的
//     `"__PREFIX__plugin_kb_jobs"` 同物。
package model

import "time"

// 作业状态闭集（与作业面 worker 的状态机成对；写侧闸在 service/jobs）
const (
	JobStatusPending  = "pending"  // 待执行
	JobStatusRunning  = "running"  // 执行中（持租约）
	JobStatusDone     = "done"     // 完成
	JobStatusFailed   = "failed"   // 失败（重试耗尽）
	JobStatusCanceled = "canceled" // 已取消
)

// JobActiveStatuses 活跃状态集（单飞判据与领取面共用：同 key 至多一条活跃行）
var JobActiveStatuses = []string{JobStatusPending, JobStatusRunning}

// Job 作业账本行
//
// 与 kind 的关系：kind 是业务闭集（kb:ingest 等），由作业面注册表约束；本表不设 DB CHECK
// （加值零迁移，与 KB 其它闭集列同口径）。
type Job struct {
	ID            uint64     `gorm:"comment:ID;primarykey;autoIncrement" json:"id"`
	Kind          string     `gorm:"comment:作业类型（注册名）;type:varchar(64);not null;default:''" json:"kind"`
	Key           string     `gorm:"comment:幂等键（同键至多一个活跃行）;type:varchar(255);not null;default:''" json:"key"`
	Status        string     `gorm:"comment:状态:pending/running/done/failed/canceled;type:varchar(16);not null;default:'pending'" json:"status"`
	Attempt       int        `gorm:"comment:已尝试次数（领取即 +1）;not null;default:0" json:"attempt"`
	MaxAttempts   int        `gorm:"comment:最大尝试次数;not null;default:3" json:"max_attempts"`
	RunAt         time.Time  `gorm:"comment:可执行时间（退避重试的锚）;not null" json:"run_at"`
	LockedUntil   *time.Time `gorm:"comment:租约到期时间（过期即可被重领）" json:"locked_until"`
	Params        *string    `gorm:"comment:入参（JSON）;type:jsonb" json:"params,omitempty"`
	Result        *string    `gorm:"comment:结果（JSON）;type:jsonb" json:"result,omitempty"`
	ProgressDone  int64      `gorm:"comment:进度已完成（单调不回退）;not null;default:0" json:"progress_done"`
	ProgressTotal int64      `gorm:"comment:进度总数;column:progress_total;not null;default:0" json:"progress_total"`
	Reason        string     `gorm:"comment:最近一次失败原因;type:varchar(512);not null;default:''" json:"reason"`
	CreatedAt     *time.Time `gorm:"comment:创建时间" json:"created_at"`
	UpdatedAt     *time.Time `gorm:"comment:更新时间" json:"updated_at"`
}

// TableName 指定 Job 模型表名（物理名 = 表前缀 + 本名；与插件迁移 __PREFIX__ 占位符同物）
func (Job) TableName() string {
	return "plugin_kb_jobs"
}
