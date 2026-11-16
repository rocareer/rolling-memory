// plugin: ai v0.2.0
// Store 是 AI 路由插件的全部 SQL 原子操作住所：渠道/模型/绑定/用量/熔断事件的查询与写入原语。
//
// 职责边界（与编排层/handler 的分工）：
//   - store（本文件）：SQL 与事务——CAS、原子自增、幂等覆盖、聚合；
//   - 编排层（router.go / circuit.go / billing.go / service.go）：决策与编排，**不写原生 SQL**；
//   - handler：协议（解析/鉴权/回执/脱敏）。
//
// 契约实现：
//   - *Store 实现 circuit.go 的 CircuitStore（熔断状态机的全部 SQL 原语）；
//   - *Store 实现 service.go 的 Repository（CircuitStore + FindModel + Candidates + UpsertUsage）；
//   - 编译期断言见文件末尾。
//
// 安全纪律：
//   - 所有值一律走占位符；只有「代码常量」进 SQL 文本（表名、列名、排序方向、聚合表达式）；
//   - 排序字段与聚合维度先过本文件的字段白名单，未知值拒绝，绝不拼接用户输入；
//   - 表名经 model.go 的 table() 拼前缀并做标识符复核；本文件的 quoteIdent 只包裹已复核的名字；
//   - 渠道 api_key 由 store 原样返回（写入/调用需要真值），**出口脱敏是 handler 的职责**；
//     Channel.APIKey 无 JSON 标签，Candidate 无 JSON 标签，杜绝误序列化。
//
// 时间口径：所有 *_time / *_at 列都是 UTC 秒（int64），由本文件统一取 time.Now().Unix() 或由调用方传入。
package ai

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/database"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 分页与列表上限（照源：渠道/模型默认 10、日志默认 20；clamp 一律 1~100）
const (
	DefaultChannelListLimit = 10
	DefaultModelListLimit   = 10
	DefaultUsageLogLimit    = 20
	MaxListLimit            = 100
	AggregateRowLimit       = 100
	BreakerEventListLimit   = 20
)

// lastErrorMaxLen last_error / error_message / last_probe_error 的列宽（按字符截断）
const lastErrorMaxLen = 500

// ErrStoreUnavailable 数据库未初始化（handler 的 nil-DB fail-closed 回执也读它）
var ErrStoreUnavailable = errors.New("插件 ai：数据库未初始化")

// ErrUnknownGroup 用量聚合维度不在闭集内（model|biz|source|biz_source|day）
var ErrUnknownGroup = errors.New("插件 ai：未知的用量聚合维度")

// Store 数据访问层（持有 *gorm.DB；构造函数不缓存请求级状态）
type Store struct {
	db *gorm.DB
}

// NewStore 构造 Store（db 为 nil 时所有方法返回 ErrStoreUnavailable，fail-closed）
func NewStore(db *gorm.DB) *Store {
	return &Store{db: db}
}

// ready 可用性检查（nil 接收者也走这里：调用方无需先判空）
func (s *Store) ready() error {
	if s == nil || s.db == nil {
		return ErrStoreUnavailable
	}
	return nil
}

// DB 返回底层连接（只给同插件的其他文件/诊断使用，不鼓励绕过原语）
func (s *Store) DB() *gorm.DB {
	if s == nil {
		return nil
	}
	return s.db
}

// isSQLite 判断当前底层数据库连接是否为 SQLite 方言（复用宿主 database.IsSQLite 口径；
// 传入连接非空时以该连接的方言为准，便于用例注入连接）。
func isSQLite(db *gorm.DB) bool {
	if db != nil && db.Dialector != nil {
		return strings.EqualFold(db.Dialector.Name(), "sqlite")
	}
	return database.IsSQLite()
}

// likeOp 返回当前方言下的大小写不敏感模糊匹配操作符（PostgreSQL 用 ILIKE，SQLite 的 LIKE 默认 ASCII 大小写不敏感）
func (s *Store) likeOp() string {
	if s != nil && isSQLite(s.db) {
		return "LIKE"
	}
	return "ILIKE"
}

// nowUnix 当前 UTC 秒
func nowUnix() int64 { return time.Now().Unix() }

// quoteIdent 包裹已复核的标识符（table() 保证只含 [A-Za-z0-9_]）
func quoteIdent(name string) string { return `"` + name + `"` }

// truncateRunes 按字符截断（PostgreSQL varchar(n) 计字符，不计字节）
func truncateRunes(value string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}

// normalizePage 分页归一：page≥1；limit≤0 用 default；上限 100
func normalizePage(page, limit, def int) (offset, size int) {
	if page < 1 {
		page = 1
	}
	if limit <= 0 {
		limit = def
	}
	if limit > MaxListLimit {
		limit = MaxListLimit
	}
	return (page - 1) * limit, limit
}

// normalizeLimit 单值 limit 归一（事件列表等）
func normalizeLimit(limit, def int) int {
	if limit <= 0 {
		limit = def
	}
	if limit > MaxListLimit {
		limit = MaxListLimit
	}
	return limit
}

// orderClause 排序白名单：未知字段回落默认排序；方向只认 asc（其余一律 DESC）
func orderClause(field, order string, allowed map[string]string, fallback string) string {
	column, ok := allowed[strings.TrimSpace(field)]
	if !ok {
		return fallback
	}
	if strings.EqualFold(strings.TrimSpace(order), "asc") {
		return column + " ASC"
	}
	return column + " DESC"
}

// ===== 渠道 =====

var channelSortColumns = map[string]string{
	"id":          `"id"`,
	"name":        `"name"`,
	"vendor":      `"vendor"`,
	"priority":    `"priority"`,
	"weight":      `"weight"`,
	"status":      `"status"`,
	"circuit":     `"circuit"`,
	"create_time": `"create_time"`,
	"update_time": `"update_time"`,
}

// ChannelFilter 渠道列表筛选（顶层等值 status/vendor + keyword 快搜 name ILIKE；高级 wheres 由 handler 合并）
type ChannelFilter struct {
	Page    int
	Limit   int
	Keyword string
	Status  *int16
	Vendor  string
	Sort    string
	Order   string
}

// ChannelStats 渠道列表顶部统计条（固定四字段，照源 Channel.php）
type ChannelStats struct {
	CircuitOpen     int64 `gorm:"column:circuit_open" json:"circuit_open"`
	CircuitHalfOpen int64 `gorm:"column:circuit_half_open" json:"circuit_half_open"`
	Problem         int64 `gorm:"column:problem" json:"problem"` // circuit≠closed 或 last_error≠''
	Fail1h          int64 `gorm:"column:fail_1h" json:"fail_1h"` // usage 近 1h status ∈ fail|timeout|breaker
}

// ChannelListResult 渠道分页结果
type ChannelListResult struct {
	Rows  []Channel    `json:"list"`
	Total int64        `json:"total"`
	Stats ChannelStats `json:"stats"`
}

// ChannelPatch 渠道可编辑列（nil = 不修改；熔断字段与累计统计不在其中，只走专用原语）
type ChannelPatch struct {
	Name            *string
	Vendor          *string
	Driver          *string
	SupplierID      *int64
	RPMLimit        *int
	TPMLimit        *int
	BaseURL         *string
	APIKey          *string
	Model           *string
	Timeout         *int
	Priority        *int
	Weight          *int
	Status          *int16
	FailThreshold   *int
	CooldownSeconds *int
	Remark          *string
}

// channelQuery 渠道列表/计数的共同筛选（复用同一实现，避免 list 与 total 条件漂移）
func (s *Store) channelQuery(ctx context.Context, f ChannelFilter) *gorm.DB {
	q := s.db.WithContext(ctx).Model(&Channel{})
	if f.Status != nil {
		q = q.Where(`"status" = ?`, *f.Status)
	}
	if vendor := strings.TrimSpace(f.Vendor); vendor != "" {
		q = q.Where(`"vendor" = ?`, vendor)
	}
	if keyword := strings.TrimSpace(f.Keyword); keyword != "" {
		q = q.Where(`"name" `+s.likeOp()+` ?`, "%"+keyword+"%")
	}
	return q
}

// ListChannels 渠道分页列表 + 全局统计条。api_key 原样返回，出口脱敏由 handler 负责。
func (s *Store) ListChannels(ctx context.Context, f ChannelFilter) (ChannelListResult, error) {
	result := ChannelListResult{Rows: []Channel{}}
	if err := s.ready(); err != nil {
		return result, err
	}
	if err := s.channelQuery(ctx, f).Count(&result.Total).Error; err != nil {
		return result, err
	}
	offset, limit := normalizePage(f.Page, f.Limit, DefaultChannelListLimit)
	order := orderClause(f.Sort, f.Order, channelSortColumns, `"priority" ASC, "id" DESC`)
	if err := s.channelQuery(ctx, f).Order(order).Offset(offset).Limit(limit).Find(&result.Rows).Error; err != nil {
		return result, err
	}
	stats, err := s.ChannelStats(ctx, nowUnix())
	if err != nil {
		return result, err
	}
	result.Stats = stats
	return result, nil
}

// ChannelStats 渠道健康统计（全表口径；fail_1h 读 usage 表）
func (s *Store) ChannelStats(ctx context.Context, now int64) (ChannelStats, error) {
	var stats ChannelStats
	if err := s.ready(); err != nil {
		return stats, err
	}
	if err := s.db.WithContext(ctx).Table(table("plugin_ai_channel")).
		Select(`COUNT(*) FILTER (WHERE "circuit" = 'open') AS circuit_open,
		        COUNT(*) FILTER (WHERE "circuit" = 'half_open') AS circuit_half_open,
		        COUNT(*) FILTER (WHERE "circuit" <> 'closed' OR "last_error" <> '') AS problem`).
		Scan(&stats).Error; err != nil {
		return stats, err
	}
	if err := s.db.WithContext(ctx).Model(&Usage{}).
		Where(`"status" IN ? AND "create_time" >= ?`,
			[]string{UsageStatusFail, UsageStatusTimeout, UsageStatusBreaker}, now-3600).
		Count(&stats.Fail1h).Error; err != nil {
		return stats, err
	}
	return stats, nil
}

// GetChannel 按 ID 取渠道（不存在返回 gorm.ErrRecordNotFound）
func (s *Store) GetChannel(ctx context.Context, id int64) (*Channel, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	var row Channel
	if err := s.db.WithContext(ctx).Where(`"id" = ?`, id).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// CreateChannel 新增渠道（调用方负责必填与白名单校验；本方法只补时间戳）
func (s *Store) CreateChannel(ctx context.Context, row *Channel) error {
	if err := s.ready(); err != nil {
		return err
	}
	if row == nil {
		return errors.New("插件 ai：渠道为空")
	}
	at := nowUnix()
	if row.CreateTime == 0 {
		row.CreateTime = at
	}
	if row.UpdateTime == 0 {
		row.UpdateTime = at
	}
	return s.db.WithContext(ctx).Create(row).Error
}

// UpdateChannel 更新渠道可编辑列（返回受影响行数；0 = 不存在）
func (s *Store) UpdateChannel(ctx context.Context, id int64, patch ChannelPatch) (int64, error) {
	if err := s.ready(); err != nil {
		return 0, err
	}
	updates := map[string]any{"update_time": nowUnix()}
	if patch.Name != nil {
		updates["name"] = *patch.Name
	}
	if patch.Vendor != nil {
		updates["vendor"] = *patch.Vendor
	}
	if patch.Driver != nil {
		updates["driver"] = *patch.Driver
	}
	if patch.SupplierID != nil {
		updates["supplier_id"] = *patch.SupplierID
	}
	if patch.RPMLimit != nil {
		updates["rpm_limit"] = *patch.RPMLimit
	}
	if patch.TPMLimit != nil {
		updates["tpm_limit"] = *patch.TPMLimit
	}
	if patch.BaseURL != nil {
		updates["base_url"] = *patch.BaseURL
	}
	if patch.APIKey != nil {
		updates["api_key"] = *patch.APIKey
	}
	if patch.Model != nil {
		updates["model"] = *patch.Model
	}
	if patch.Timeout != nil {
		updates["timeout"] = *patch.Timeout
	}
	if patch.Priority != nil {
		updates["priority"] = *patch.Priority
	}
	if patch.Weight != nil {
		updates["weight"] = *patch.Weight
	}
	if patch.Status != nil {
		updates["status"] = *patch.Status
	}
	if patch.FailThreshold != nil {
		updates["fail_threshold"] = *patch.FailThreshold
	}
	if patch.CooldownSeconds != nil {
		updates["cooldown_seconds"] = *patch.CooldownSeconds
	}
	if patch.Remark != nil {
		updates["remark"] = *patch.Remark
	}
	res := s.db.WithContext(ctx).Model(&Channel{}).Where(`"id" = ?`, id).Updates(updates)
	return res.RowsAffected, res.Error
}

// DeleteChannels 批量删除渠道：同一事务内先删绑定再删渠道（杜绝半删状态）
func (s *Store) DeleteChannels(ctx context.Context, ids []int64) error {
	if err := s.ready(); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	channelIDs := make([]uint64, len(ids))
	for i, id := range ids {
		channelIDs[i] = uint64(id)
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where(`"channel_id" IN ?`, channelIDs).Delete(&ModelChannel{}).Error; err != nil {
			return err
		}
		return tx.Where(`"id" IN ?`, ids).Delete(&Channel{}).Error
	})
}

// SwitchChannel 渠道启停：只改 status，不动熔断字段与累计统计（返回受影响行数）
func (s *Store) SwitchChannel(ctx context.Context, id int64, status int16) (int64, error) {
	if err := s.ready(); err != nil {
		return 0, err
	}
	res := s.db.WithContext(ctx).Model(&Channel{}).Where(`"id" = ?`, id).
		Updates(map[string]any{"status": status, "update_time": nowUnix()})
	return res.RowsAffected, res.Error
}

// SetProbeResult 写回探活快照（**探活隔离**：绝不触碰熔断字段、失败计数与 usage 记账）。
// ok ⇒ last_probe_ok=1、latency、清空 error、probe_fail_streak 清零；
// fail ⇒ last_probe_ok=0、latency=NULL、写可读原因、streak+1（原子自增）。
func (s *Store) SetProbeResult(ctx context.Context, id int64, ok bool, latencyMS int, probeErr string, at int64) (int64, error) {
	if err := s.ready(); err != nil {
		return 0, err
	}
	if at <= 0 {
		at = nowUnix()
	}
	updates := map[string]any{
		"last_probe_at":    at,
		"last_probe_error": "",
		"update_time":      at,
	}
	if ok {
		updates["last_probe_ok"] = int16(1)
		updates["last_probe_latency"] = latencyMS
		updates["probe_fail_streak"] = 0
	} else {
		updates["last_probe_ok"] = int16(0)
		updates["last_probe_latency"] = nil
		updates["last_probe_error"] = truncateRunes(probeErr, lastErrorMaxLen)
		updates["probe_fail_streak"] = gorm.Expr(`"probe_fail_streak" + 1`)
	}
	res := s.db.WithContext(ctx).Model(&Channel{}).Where(`"id" = ?`, id).Updates(updates)
	return res.RowsAffected, res.Error
}

// ChannelHealth 总览页的渠道健康段。
//
// Problems 必须带 `gorm:"-"`：它是聚合结果之外的**第二段查询**的产物（见 ChannelHealth 读取原语），
// 不是关联字段。少了这个标签，GORM 会把 []Channel 当成 has-many 关联去猜外键
// （Channel.ID 是 autoIncrement 主键），Scan(&health) 直接报
// 「invalid field found for struct ...'s field Problems: define a valid foreign key for relations」——
// 空表也一样报，于是总览端点整页 code=1（store_db_test.go 有针对真库的回归用例）。
type ChannelHealth struct {
	Total           int64     `json:"total"`
	Enabled         int64     `json:"enabled"`
	CircuitOpen     int64     `json:"circuit_open"`
	CircuitHalfOpen int64     `json:"circuit_half_open"`
	Problems        []Channel `gorm:"-" json:"problems"` // 最多 5 条；api_key 由 handler 脱敏
}

// ChannelHealth 读取渠道健康（总数/启用/熔断中/半开/异常样本≤5）
func (s *Store) ChannelHealth(ctx context.Context) (ChannelHealth, error) {
	health := ChannelHealth{Problems: []Channel{}}
	if err := s.ready(); err != nil {
		return health, err
	}
	if err := s.db.WithContext(ctx).Table(table("plugin_ai_channel")).
		Select(`COUNT(*) AS total,
		        COUNT(*) FILTER (WHERE "status" = 1) AS enabled,
		        COUNT(*) FILTER (WHERE "circuit" = 'open') AS circuit_open,
		        COUNT(*) FILTER (WHERE "circuit" = 'half_open') AS circuit_half_open`).
		Scan(&health).Error; err != nil {
		return health, err
	}
	if err := s.db.WithContext(ctx).Model(&Channel{}).
		Where(`"circuit" <> ? OR "last_error" <> ''`, CircuitClosed).
		Order(`"update_time" DESC, "id" DESC`).Limit(5).
		Find(&health.Problems).Error; err != nil {
		return health, err
	}
	return health, nil
}

// ===== 熔断状态机原语（实现 circuit.go 的 CircuitStore）=====

// EnterCAS 熔断准入 CAS：open 且冷却结束的渠道 ⇒ half_open（opened_at 记为探针开始时间）。
// 返回 true 表示本调用当选唯一探针（受影响行数 > 0）；false 表示被别人抢先，调用方必须跳过。
func (s *Store) EnterCAS(ctx context.Context, id uint64, now int64) (bool, error) {
	if err := s.ready(); err != nil {
		return false, err
	}
	if now <= 0 {
		now = nowUnix()
	}
	res := s.db.WithContext(ctx).Model(&Channel{}).
		Where(`"id" = ? AND "circuit" = ? AND "status" = ?`, int64(id), CircuitOpen, StatusEnabled).
		Updates(map[string]any{
			"circuit":     CircuitHalfOpen,
			"opened_at":   now,
			"update_time": now,
		})
	return res.RowsAffected > 0, res.Error
}

// ReselectStaleProbe 探针卡死护栏：half_open 且 opened_at 早于 now-cooldownSeconds
// （进程被杀导致探针未收口）⇒ 重选探针并刷新 opened_at。返回是否当选。
func (s *Store) ReselectStaleProbe(ctx context.Context, id uint64, now, cooldownSeconds int64) (bool, error) {
	if err := s.ready(); err != nil {
		return false, err
	}
	if now <= 0 {
		now = nowUnix()
	}
	res := s.db.WithContext(ctx).Model(&Channel{}).
		Where(`"id" = ? AND "circuit" = ? AND "opened_at" IS NOT NULL AND "opened_at" < ?`,
			int64(id), CircuitHalfOpen, now-cooldownSeconds).
		Updates(map[string]any{"opened_at": now, "update_time": now})
	return res.RowsAffected > 0, res.Error
}

// MarkFailure 失败计数原子自增 + RETURNING（禁读-改-写）：返回自增后的 consecutive_failures。
// last_error 截断 500；是否达到阈值由 circuit.go 判定后调用 OpenCircuit。
func (s *Store) MarkFailure(ctx context.Context, id uint64, lastError string, at int64) (int, error) {
	if err := s.ready(); err != nil {
		return 0, err
	}
	if at <= 0 {
		at = nowUnix()
	}
	statement := `UPDATE ` + quoteIdent(table("plugin_ai_channel")) +
		` SET "consecutive_failures" = "consecutive_failures" + 1, "last_error" = ?, "update_time" = ?` +
		` WHERE "id" = ? RETURNING "consecutive_failures"`
	var consecutive int
	if err := s.db.WithContext(ctx).Raw(statement, truncateRunes(lastError, lastErrorMaxLen), at, int64(id)).
		Row().Scan(&consecutive); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, gorm.ErrRecordNotFound
		}
		return 0, err
	}
	return consecutive, nil
}

// MarkSuccess 成功迁移：单条 UPDATE 写 closed/清零/心跳。返回 true 表示真的写了
// （稳态被 SQL 条件拦下时返回 false）。
func (s *Store) MarkSuccess(ctx context.Context, id uint64, at int64) (bool, error) {
	if err := s.ready(); err != nil {
		return false, err
	}
	if at <= 0 {
		at = nowUnix()
	}
	res := s.db.WithContext(ctx).Model(&Channel{}).
		Where(`"id" = ? AND ("circuit" <> ? OR "consecutive_failures" <> 0 OR "opened_at" IS NOT NULL
		        OR "last_success_at" IS NULL OR "last_success_at" <= ?)`,
			int64(id), CircuitClosed, at-HeartbeatMinSeconds).
		Updates(map[string]any{
			"circuit":              CircuitClosed,
			"consecutive_failures": 0,
			"opened_at":            nil,
			"last_success_at":      at,
			"update_time":          at,
		})
	return res.RowsAffected > 0, res.Error
}

// OpenCircuit 跳闸：closed→open（阈值触发）或 half_open→open（探针失败，重新计时冷却）。
// 已 open 时不重置 opened_at（返回 false）。
func (s *Store) OpenCircuit(ctx context.Context, id uint64, at int64) (bool, error) {
	if err := s.ready(); err != nil {
		return false, err
	}
	if at <= 0 {
		at = nowUnix()
	}
	res := s.db.WithContext(ctx).Model(&Channel{}).
		Where(`"id" = ? AND "circuit" <> ?`, int64(id), CircuitOpen).
		Updates(map[string]any{"circuit": CircuitOpen, "opened_at": at, "update_time": at})
	return res.RowsAffected > 0, res.Error
}

// ManualOpenCircuit 人工熔断（语义同 OpenCircuit；已 open 返回 false 由 handler 报「已在熔断中」）。
// 不改 total_calls/total_failures/total_tokens，也不伪造 consecutive_failures。
func (s *Store) ManualOpenCircuit(ctx context.Context, id uint64, at int64) (bool, error) {
	return s.OpenCircuit(ctx, id, at)
}

// ManualCloseCircuit 人工恢复：closed + 连续失败清零 + opened_at=NULL。
// 不改累计统计（返回受影响行数；0 = 渠道不存在）。
func (s *Store) ManualCloseCircuit(ctx context.Context, id uint64, at int64) (int64, error) {
	if err := s.ready(); err != nil {
		return 0, err
	}
	if at <= 0 {
		at = nowUnix()
	}
	res := s.db.WithContext(ctx).Model(&Channel{}).Where(`"id" = ?`, int64(id)).
		Updates(map[string]any{
			"circuit":              CircuitClosed,
			"consecutive_failures": 0,
			"opened_at":            nil,
			"update_time":          at,
		})
	return res.RowsAffected, res.Error
}

// ===== 模型与绑定 =====

var modelSortColumns = map[string]string{
	"id":          `"id"`,
	"name":        `"name"`,
	"type":        `"type"`,
	"status":      `"status"`,
	"create_time": `"create_time"`,
	"update_time": `"update_time"`,
}

// ModelFilter 模型列表筛选（顶层等值 type/status + keyword 快搜 name）
type ModelFilter struct {
	Page    int
	Limit   int
	Keyword string
	Type    *string
	Status  *int16
	Sort    string
	Order   string
}

// ModelBindingRow 绑定行的读模型（附渠道名，供列表/详情展示，避免 N+1）
type ModelBindingRow struct {
	ModelID     uint64 `gorm:"column:model_id" json:"model_id"`
	ChannelID   uint64 `gorm:"column:channel_id" json:"channel_id"`
	ChannelName string `gorm:"column:channel_name" json:"channel_name"`
	Priority    int    `gorm:"column:priority" json:"priority"`
	Weight      int    `gorm:"column:weight" json:"weight"`
	Status      int16  `gorm:"column:status" json:"status"`
}

// ModelWithBindings 模型行 + 绑定明细 + 渠道名 + 绑定数
type ModelWithBindings struct {
	Model
	Bindings     []ModelBindingRow `gorm:"-" json:"channels"`
	ChannelNames []string          `gorm:"-" json:"channel_names"`
	BoundCount   int               `gorm:"-" json:"bound_count"`
}

// ModelListResult 模型分页结果
type ModelListResult struct {
	Rows  []ModelWithBindings `json:"list"`
	Total int64               `json:"total"`
}

// ModelPatch 模型可编辑列（nil = 不修改；改名查重由 handler 做）
type ModelPatch struct {
	Name          *string
	Type          *string
	Strategy      *string
	Description   *string
	Status        *int16
	PriceInput    *string
	PriceOutput   *string
	Encoding      *string
	ContextWindow *int
	Capabilities  *[]string
}

// modelQuery 模型列表/计数的共同筛选
func (s *Store) modelQuery(ctx context.Context, f ModelFilter) *gorm.DB {
	q := s.db.WithContext(ctx).Model(&Model{})
	if f.Status != nil {
		q = q.Where(`"status" = ?`, *f.Status)
	}
	if f.Type != nil && strings.TrimSpace(*f.Type) != "" {
		q = q.Where(`"type" = ?`, strings.TrimSpace(*f.Type))
	}
	if keyword := strings.TrimSpace(f.Keyword); keyword != "" {
		q = q.Where(`"name" `+s.likeOp()+` ?`, "%"+keyword+"%")
	}
	return q
}

// ListModels 模型分页列表（附绑定明细/渠道名/绑定数；固定两次查询，无 N+1）
func (s *Store) ListModels(ctx context.Context, f ModelFilter) (ModelListResult, error) {
	result := ModelListResult{Rows: []ModelWithBindings{}}
	if err := s.ready(); err != nil {
		return result, err
	}
	if err := s.modelQuery(ctx, f).Count(&result.Total).Error; err != nil {
		return result, err
	}
	offset, limit := normalizePage(f.Page, f.Limit, DefaultModelListLimit)
	order := orderClause(f.Sort, f.Order, modelSortColumns, `"id" DESC`)
	if err := s.modelQuery(ctx, f).Order(order).Offset(offset).Limit(limit).Find(&result.Rows).Error; err != nil {
		return result, err
	}
	ids := make([]uint64, 0, len(result.Rows))
	for i := range result.Rows {
		ids = append(ids, result.Rows[i].ID)
	}
	bindings, err := s.ListBindings(ctx, ids)
	if err != nil {
		return result, err
	}
	attachBindings(result.Rows, bindings)
	return result, nil
}

// attachBindings 把绑定明细按 model_id 分发给模型行（保持查询顺序）
func attachBindings(rows []ModelWithBindings, bindings []ModelBindingRow) {
	if len(rows) == 0 {
		return
	}
	byModel := make(map[uint64][]ModelBindingRow, len(rows))
	for _, b := range bindings {
		byModel[b.ModelID] = append(byModel[b.ModelID], b)
	}
	for i := range rows {
		items := byModel[rows[i].ID]
		if items == nil {
			items = []ModelBindingRow{}
		}
		rows[i].Bindings = items
		rows[i].BoundCount = len(items)
		names := make([]string, 0, len(items))
		seen := make(map[string]struct{}, len(items))
		for _, item := range items {
			if item.ChannelName == "" {
				continue
			}
			if _, dup := seen[item.ChannelName]; dup {
				continue
			}
			seen[item.ChannelName] = struct{}{}
			names = append(names, item.ChannelName)
		}
		rows[i].ChannelNames = names
	}
}

// FindModel 按对外统一名查逻辑模型（Repository 契约；不存在返回 ErrModelNotFound）。
// 启停判定由编排层做（Model.Enabled），本方法不按 status 过滤——禁用模型要能被查出来再翻成 404。
func (s *Store) FindModel(ctx context.Context, name string) (Model, error) {
	if err := s.ready(); err != nil {
		return Model{}, err
	}
	var row Model
	err := s.db.WithContext(ctx).Where(`"name" = ?`, name).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Model{}, ErrModelNotFound
	}
	if err != nil {
		return Model{}, err
	}
	return row, nil
}

// GetModel 按 ID 取模型 + 已绑定渠道明细
func (s *Store) GetModel(ctx context.Context, id uint64) (*ModelWithBindings, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	var row ModelWithBindings
	if err := s.db.WithContext(ctx).Where(`"id" = ?`, id).First(&row).Error; err != nil {
		return nil, err
	}
	bindings, err := s.ListBindings(ctx, []uint64{id})
	if err != nil {
		return nil, err
	}
	rows := []ModelWithBindings{row}
	attachBindings(rows, bindings)
	return &rows[0], nil
}

// CreateModel 新增模型（唯一名冲突由 DB 唯一索引拦下，handler 翻成可读错误）
func (s *Store) CreateModel(ctx context.Context, row *Model) error {
	if err := s.ready(); err != nil {
		return err
	}
	if row == nil {
		return errors.New("插件 ai：模型为空")
	}
	at := nowUnix()
	if row.CreateTime == 0 {
		row.CreateTime = at
	}
	if row.UpdateTime == 0 {
		row.UpdateTime = at
	}
	return s.db.WithContext(ctx).Create(row).Error
}

// UpdateModel 更新模型可编辑列（不触碰绑定；返回受影响行数）。
// capabilities 在 map 更新里不走字段 serializer，故显式经 JSON 文本 + ?::jsonb 写入。
func (s *Store) UpdateModel(ctx context.Context, id uint64, patch ModelPatch) (int64, error) {
	if err := s.ready(); err != nil {
		return 0, err
	}
	updates := map[string]any{"update_time": nowUnix()}
	if patch.Name != nil {
		updates["name"] = *patch.Name
	}
	if patch.Type != nil {
		updates["type"] = *patch.Type
	}
	if patch.Strategy != nil {
		updates["strategy"] = *patch.Strategy
	}
	if patch.Description != nil {
		updates["description"] = *patch.Description
	}
	if patch.Status != nil {
		updates["status"] = *patch.Status
	}
	if patch.PriceInput != nil {
		updates["price_input"] = *patch.PriceInput
	}
	if patch.PriceOutput != nil {
		updates["price_output"] = *patch.PriceOutput
	}
	if patch.Encoding != nil {
		updates["encoding"] = *patch.Encoding
	}
	if patch.ContextWindow != nil {
		updates["context_window"] = *patch.ContextWindow
	}
	if patch.Capabilities != nil {
		raw, err := json.Marshal(*patch.Capabilities)
		if err != nil {
			return 0, err
		}
		if isSQLite(s.db) {
			updates["capabilities"] = string(raw)
		} else {
			updates["capabilities"] = gorm.Expr("?::jsonb", string(raw))
		}
	}
	res := s.db.WithContext(ctx).Model(&Model{}).Where(`"id" = ?`, id).Updates(updates)
	return res.RowsAffected, res.Error
}

// DeleteModels 批量删除模型：同一事务内先删绑定再删模型行
func (s *Store) DeleteModels(ctx context.Context, ids []uint64) error {
	if err := s.ready(); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where(`"model_id" IN ?`, ids).Delete(&ModelChannel{}).Error; err != nil {
			return err
		}
		return tx.Where(`"id" IN ?`, ids).Delete(&Model{}).Error
	})
}

// ListBindings 批量取绑定明细（一次 JOIN 查询；ids 为空返回空切片）
func (s *Store) ListBindings(ctx context.Context, modelIDs []uint64) ([]ModelBindingRow, error) {
	rows := []ModelBindingRow{}
	if err := s.ready(); err != nil {
		return rows, err
	}
	if len(modelIDs) == 0 {
		return rows, nil
	}
	err := s.db.WithContext(ctx).
		Table(quoteIdent(table("plugin_ai_model_channel"))+` AS mc`).
		Select(`mc."model_id" AS model_id,
		        mc."channel_id" AS channel_id,
		        COALESCE(c."name", '') AS channel_name,
		        mc."priority" AS priority,
		        mc."weight" AS weight,
		        mc."status" AS status`).
		Joins(`LEFT JOIN `+quoteIdent(table("plugin_ai_channel"))+` AS c ON c."id" = mc."channel_id"`).
		Where(`mc."model_id" IN ?`, modelIDs).
		Order(`mc."model_id" ASC, mc."priority" ASC, mc."weight" DESC, mc."channel_id" ASC`).
		Find(&rows).Error
	return rows, err
}

// ReplaceBindings 绑定全量替换（upsert 语义）：事务内「删该模型全部绑定 → 只插入仍存在的渠道」。
// priority/weight 负数收敛为 0；status 非 1 一律收敛为 0（与 CHECK 闭集一致）。返回插入条数。
func (s *Store) ReplaceBindings(ctx context.Context, modelID uint64, binds []ModelChannel) (int, error) {
	if err := s.ready(); err != nil {
		return 0, err
	}
	inserted := 0
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var exists int64
		if err := tx.Model(&Model{}).Where(`"id" = ?`, modelID).Count(&exists).Error; err != nil {
			return err
		}
		if exists == 0 {
			return gorm.ErrRecordNotFound
		}
		if err := tx.Where(`"model_id" = ?`, modelID).Delete(&ModelChannel{}).Error; err != nil {
			return err
		}
		if len(binds) == 0 {
			return nil
		}
		// 只保留真实存在的渠道（channel_id 不存在的绑定跳过，不报错）
		channelIDs := make([]int64, 0, len(binds))
		seenChannel := make(map[int64]struct{}, len(binds))
		for _, bind := range binds {
			if bind.ChannelID == 0 {
				continue
			}
			id := int64(bind.ChannelID)
			if _, dup := seenChannel[id]; dup {
				continue
			}
			seenChannel[id] = struct{}{}
			channelIDs = append(channelIDs, id)
		}
		if len(channelIDs) == 0 {
			return nil
		}
		var existing []int64
		if err := tx.Model(&Channel{}).Where(`"id" IN ?`, channelIDs).Pluck("id", &existing).Error; err != nil {
			return err
		}
		allowed := make(map[int64]struct{}, len(existing))
		for _, id := range existing {
			allowed[id] = struct{}{}
		}
		at := nowUnix()
		rows := make([]ModelChannel, 0, len(channelIDs))
		seenInsert := make(map[uint64]struct{}, len(channelIDs))
		for _, bind := range binds {
			if _, ok := allowed[int64(bind.ChannelID)]; !ok {
				continue
			}
			if _, dup := seenInsert[bind.ChannelID]; dup {
				continue
			}
			seenInsert[bind.ChannelID] = struct{}{}
			priority := bind.Priority
			if priority < 0 {
				priority = 0
			}
			weight := bind.Weight
			if weight < 0 {
				weight = 0
			}
			status := bind.Status
			if status != StatusEnabled {
				status = StatusDisabled
			}
			rows = append(rows, ModelChannel{
				ModelID:    modelID,
				ChannelID:  bind.ChannelID,
				Priority:   priority,
				Weight:     weight,
				Status:     status,
				CreateTime: at,
				UpdateTime: at,
			})
		}
		if len(rows) == 0 {
			return nil
		}
		if err := tx.Create(&rows).Error; err != nil {
			return err
		}
		inserted = len(rows)
		return nil
	})
	return inserted, err
}

// ===== 渠道候选与继承合并（三级配置的中间两级：厂商 → 渠道）=====

// channelCallColumns 候选集与生效读取共用的「渠道调用列」片段（别名口径 = model.go 的 Candidate）。
// 只放代码常量：列名/别名都是固定字符串，值一律走占位符（本文件的安全纪律）。
const channelCallColumns = `c."id" AS channel_id,
        c."name" AS name,
        c."vendor" AS vendor,
        c."driver" AS driver,
        c."supplier_id" AS supplier_id,
        c."rpm_limit" AS rpm_limit,
        c."tpm_limit" AS tpm_limit,
        c."base_url" AS base_url,
        c."api_key" AS api_key,
        c."model" AS model,
        c."timeout" AS timeout,
        c."status" AS channel_status,
        c."fail_threshold" AS fail_threshold,
        c."cooldown_seconds" AS cooldown_seconds,
        c."circuit" AS circuit,
        c."consecutive_failures" AS consecutive_failures,
        c."opened_at" AS opened_at,
        c."last_success_at" AS last_success_at,
        c."last_error" AS last_error`

// channelSupplierColumns 是厂商侧的继承列片段。
//
// LEFT JOIN 未命中（supplier_id=0 或厂商行已删）时四列都是 NULL ⇒ 必须 COALESCE 成零值，
// 否则 GORM 把 NULL 扫进 string/int 会直接报错（不是「值恰好为空」）。
// 厂商 status 刻意不参与判定：源侧继承只认字段非空（Supplier::find 无状态过滤），
// 「停用厂商仍被继承」与源一致；把厂商停用为一道闸属独立项（不在本批）。
const channelSupplierColumns = `COALESCE(s."base_url", '') AS supplier_base_url,
        COALESCE(s."api_key", '') AS supplier_api_key,
        COALESCE(s."driver", '') AS supplier_driver,
        COALESCE(s."timeout", 0) AS supplier_timeout`

// channelSupplierJoin 厂商继承的 LEFT JOIN 片段（表名经 table() 拼前缀 + quoteIdent 包裹）。
//
// **必须是函数、不能是包级 var**：table() 读的是运行时配置（部署可配 database.prefix），
// 而包级 var 的初始化发生在 config 装载之前（当时前缀还是零值）⇒ 带前缀的库上 JOIN 会打到
// 一张不存在的表（42P01），且只在带前缀部署上暴露。本包其余 table() 调用都在函数体内，口径一致。
func channelSupplierJoin() string {
	return `LEFT JOIN ` + quoteIdent(table("plugin_ai_supplier")) + ` AS s ON s."id" = c."supplier_id"`
}

// mergeSupplierInheritance 逐字段合并「渠道非空优先、空则取厂商、再无则留零值」。
//
// 口径照源 AiRouterService::channelConfig（/src/app/ai/service/AiRouterService.php:1274-1316）的
// pick()：渠道字段非空即用自己的，否则取所挂厂商的同名列；两者都为空时：
//   - BaseURL/APIKey/Driver 留空串（调用期由驱动 fail-closed 报「未配置凭证」/按空 driver 回落默认协议）；
//   - Timeout 留 0（由 Service.resolveChannel 回落插件配置 request_timeout，源侧同位置回落
//     ai_config('router.timeout', 60)）。
//
// 「非空」的判定严格照源：只判 == ""（不做 TrimSpace）。这样渠道里显式填的空白串不会被
// 静默替换成厂商值——写入侧的 trim 已经发生在 handler，读到空白串说明是既有脏数据，
// 让它按字面生效比默默换一套凭证更容易排查。
// 厂商 status / 悬空 supplier_id 都不参与判定（同源；未命中即四列全空）。
func mergeSupplierInheritance(row Candidate) Candidate {
	if row.SupplierID == 0 {
		return row
	}
	if row.BaseURL == "" {
		row.BaseURL = row.SupplierBaseURL
	}
	if row.APIKey == "" {
		row.APIKey = row.SupplierAPIKey
	}
	if row.Driver == "" {
		row.Driver = row.SupplierDriver
	}
	if row.Timeout == 0 {
		row.Timeout = row.SupplierTimeout
	}
	return row
}

// mergeSupplierInheritanceAll 批量版（Candidates 返回前逐行合并）。
func mergeSupplierInheritanceAll(rows []Candidate) []Candidate {
	for i := range rows {
		rows[i] = mergeSupplierInheritance(rows[i])
	}
	return rows
}

// Candidates 候选集（Repository 契约）：绑定与渠道双 enabled，按绑定 priority ASC / weight DESC / channel id ASC。
// 一次 JOIN 查询（LEFT JOIN 厂商取继承列），绝不逐候选查库；返回扁平候选行（Router.Build 再做同口径复核与加权），
// 其 BaseURL/APIKey/Driver/Timeout 已按 mergeSupplierInheritance 合并成生效值。
func (s *Store) Candidates(ctx context.Context, modelID uint64) ([]Candidate, error) {
	rows := []Candidate{}
	if err := s.ready(); err != nil {
		return rows, err
	}
	err := s.db.WithContext(ctx).
		Table(quoteIdent(table("plugin_ai_channel"))+` AS c`).
		Select(channelCallColumns+`,
		        `+channelSupplierColumns+`,
		        mc."model_id" AS model_id,
		        mc."priority" AS binding_priority,
		        mc."weight" AS binding_weight,
		        mc."status" AS binding_status`).
		Joins(`JOIN `+quoteIdent(table("plugin_ai_model_channel"))+` AS mc ON mc."channel_id" = c."id"`).
		Joins(channelSupplierJoin()).
		Where(`mc."model_id" = ? AND mc."status" = ? AND c."status" = ?`,
			modelID, StatusEnabled, StatusEnabled).
		Order(`mc."priority" ASC, mc."weight" DESC, c."id" ASC`).
		Find(&rows).Error
	if err != nil {
		return rows, err
	}
	return mergeSupplierInheritanceAll(rows), nil
}

// GetChannelEffective 读取单渠道的**生效**行（渠道非空优先、空则取厂商；与 Candidates 同一份合并实现）。
//
// 用途：需要「与真实路由看到同一份凭证」的路径——探活（handler_channel.channelProbe）必须用它，
// 否则渠道把 base_url/api_key/driver 留空挂厂商时，探活会拿空凭证 fail-closed，探活结果与路由行为脱节
// （源侧为此另写了一份等价实现 ChannelProbe::effectiveCredential，见 /src/app/ai/support/ChannelProbe.php:132-153）。
// **管理面表单回显请用 GetChannel**：那里要显示渠道自身列，保留「留空即继承」的语义。
func (s *Store) GetChannelEffective(ctx context.Context, id int64) (*Channel, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	var row Candidate
	err := s.db.WithContext(ctx).
		Table(quoteIdent(table("plugin_ai_channel"))+` AS c`).
		Select(channelCallColumns+`, `+channelSupplierColumns).
		Joins(channelSupplierJoin()).
		Where(`c."id" = ?`, id).
		Take(&row).Error
	if err != nil {
		return nil, err
	}
	if row.ChannelID == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	ch := mergeSupplierInheritance(row).ChannelSnapshot()
	return &ch, nil
}

// ModelHealth 总览页的模型健康段
type ModelHealth struct {
	Total   int64 `json:"total"`
	Enabled int64 `json:"enabled"`
}

// ModelHealth 读取模型总数/启用数
func (s *Store) ModelHealth(ctx context.Context) (ModelHealth, error) {
	health := ModelHealth{}
	if err := s.ready(); err != nil {
		return health, err
	}
	err := s.db.WithContext(ctx).Table(table("plugin_ai_model")).
		Select(`COUNT(*) AS total, COUNT(*) FILTER (WHERE "status" = 1) AS enabled`).
		Scan(&health).Error
	return health, err
}

// ===== 用量流水 =====

var usageLogSortColumns = map[string]string{
	"id":           `"id"`,
	"create_time":  `"create_time"`,
	"latency_ms":   `"latency_ms"`,
	"total_tokens": `"total_tokens"`,
	"cost":         `"cost"`,
	"status":       `"status"`,
	"model_id":     `"model_id"`,
	"channel_id":   `"channel_id"`,
}

// UsageFilter 调用日志筛选（等值 status/call_type/biz_type/source/tokens_source/request_id；
// 模糊 model_name/channel_name；范围 start/end 为 create_time 秒级闭区间）
type UsageFilter struct {
	Page         int
	Limit        int
	Status       string
	CallType     string
	BizType      string
	Source       string
	TokensSource string
	RequestID    string
	ModelName    string
	ChannelName  string
	Start        *int64
	End          *int64
	Sort         string
	Order        string
}

// UsageLogPage 调用日志分页结果
type UsageLogPage struct {
	Rows  []Usage `json:"list"`
	Total int64   `json:"total"`
}

// usageLogQuery 调用日志的共同筛选
func (s *Store) usageLogQuery(ctx context.Context, f UsageFilter) *gorm.DB {
	q := s.db.WithContext(ctx).Model(&Usage{})
	if f.Status != "" {
		q = q.Where(`"status" = ?`, f.Status)
	}
	if f.CallType != "" {
		q = q.Where(`"call_type" = ?`, f.CallType)
	}
	if f.BizType != "" {
		q = q.Where(`"biz_type" = ?`, f.BizType)
	}
	if f.Source != "" {
		q = q.Where(`"source" = ?`, f.Source)
	}
	if f.TokensSource != "" {
		q = q.Where(`"tokens_source" = ?`, f.TokensSource)
	}
	if f.RequestID != "" {
		q = q.Where(`"request_id" = ?`, f.RequestID)
	}
	if name := strings.TrimSpace(f.ModelName); name != "" {
		q = q.Where(`"model_name" `+s.likeOp()+` ?`, "%"+name+"%")
	}
	if name := strings.TrimSpace(f.ChannelName); name != "" {
		q = q.Where(`"channel_name" `+s.likeOp()+` ?`, "%"+name+"%")
	}
	if f.Start != nil {
		q = q.Where(`"create_time" >= ?`, *f.Start)
	}
	if f.End != nil {
		q = q.Where(`"create_time" <= ?`, *f.End)
	}
	return q
}

// ListUsageLogs 调用日志分页（默认 id DESC、limit 20）
func (s *Store) ListUsageLogs(ctx context.Context, f UsageFilter) (UsageLogPage, error) {
	result := UsageLogPage{Rows: []Usage{}}
	if err := s.ready(); err != nil {
		return result, err
	}
	if err := s.usageLogQuery(ctx, f).Count(&result.Total).Error; err != nil {
		return result, err
	}
	offset, limit := normalizePage(f.Page, f.Limit, DefaultUsageLogLimit)
	order := orderClause(f.Sort, f.Order, usageLogSortColumns, `"id" DESC`)
	if err := s.usageLogQuery(ctx, f).Order(order).Offset(offset).Limit(limit).Find(&result.Rows).Error; err != nil {
		return result, err
	}
	return result, nil
}

// usageTextColumn 一个 usage 文本列的（列名, 值指针, 字宽）——字宽与 billing.go 的 GORM type 标签、
// migrations/1_init.up.sql 的 DDL 同源，单位是**字符**（PostgreSQL 的 varchar(n) 计字符不计字节）。
type usageTextColumn struct {
	name  string
	value *string
	max   int
}

// normalizeUsageColumns 在写库前把文本列按列宽截断，并记一行日志（发生了哪些列的截断）。
//
// 为什么必须有：任一列超长会让 PostgreSQL 报 22001 使**整行**写入失败，而 usage 是计费/审计行——
// 编排层（service.go 的 saveUsage）只记日志、不打断上游响应，于是调用方照常拿到 200、这一行静默丢失，
// 同时通道累计（Channel 的 total_calls/total_failures）仍会 +1，口径分叉且事后无法对账。
// 公开面的 biz_type 来自调用方（v1 只判空、不做闭集与长度校验），upstream 派生的 error_code 同理，
// 都不能假设天然落在列宽之内；此处是**唯一的 SQL 写口**，兜在这里写不进去才真的写不进去。
func normalizeUsageColumns(usage *Usage) {
	if usage == nil {
		return
	}
	fields := []usageTextColumn{
		{"request_id", &usage.RequestID, 64},
		{"model_name", &usage.ModelName, 100},
		{"channel_name", &usage.ChannelName, 50},
		{"call_type", &usage.CallType, 16},
		{"biz_type", &usage.BizType, 30},
		{"source", &usage.Source, 30},
		{"tokens_source", &usage.TokensSource, 16},
		{"currency", &usage.Currency, 10},
		{"status", &usage.Status, 16},
		{"error_code", &usage.ErrorCode, 100},
		{"error_message", &usage.ErrorMessage, 500},
		{"error_class", &usage.ErrorClass, 20},
	}
	truncated := make([]string, 0, 1)
	for _, column := range fields {
		next := truncateRunes(*column.value, column.max)
		if next == *column.value {
			continue
		}
		*column.value = next
		truncated = append(truncated, column.name)
	}
	if len(truncated) > 0 {
		log.Printf("ai: usage 文本列超长已按列宽截断 request_id=%s columns=%s", usage.RequestID, strings.Join(truncated, ","))
	}
}

// UpsertUsage 写一行用量（Repository 契约）：冲突键 (request_id,attempt)，
// 后到覆盖业务列、保留首次 create_time。写入前按列宽兜底截断（见 normalizeUsageColumns）。
//
// billed_partial（部分计费标记，迁移 2）必须在覆盖列清单里：流式「有块无收尾」的补偿结算
// （service.go 的 settleStreamFailure 分支③）可能先写一行 billed_partial=0 的失败行，
// 随后同 (request_id,attempt) 再写一行 billed_partial=1 的补偿行——该列不进清单则第二次写
// 只更新业务列、标记永远停在 0，审计面看到的「已部分计费」与真实扣费分叉。
// 注意：列清单里的 billed_partial 要求该列存在（迁移 2 已应用），否则整条 upsert 报 42703。
func (s *Store) UpsertUsage(ctx context.Context, usage *Usage) error {
	if err := s.ready(); err != nil {
		return err
	}
	if usage == nil {
		return errors.New("插件 ai：用量行为空")
	}
	normalizeUsageColumns(usage)
	at := nowUnix()
	if usage.CreateTime == 0 {
		usage.CreateTime = at
	}
	usage.UpdateTime = at
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "request_id"}, {Name: "attempt"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"model_id", "model_name", "channel_id", "channel_name",
			"call_type", "biz_type", "source", "stream",
			"prompt_tokens", "completion_tokens", "total_tokens", "cached_tokens",
			"tokens_source", "cost", "currency", "latency_ms",
			"billed_partial",
			"status", "error_code", "error_message", "error_class", "update_time",
		}),
	}).Create(usage).Error
}

// AddChannelTotals 渠道累计：success ⇒ total_calls+1 且 total_tokens += tokens；非 success ⇒ total_failures+1。
// channel_id=0（未落到渠道）直接跳过。**每次真实尝试恰好调用一次**（流式在最终收口调用）。
func (s *Store) AddChannelTotals(ctx context.Context, channelID uint64, success bool, tokens int, at int64) error {
	if err := s.ready(); err != nil {
		return err
	}
	if channelID == 0 {
		return nil
	}
	if at <= 0 {
		at = nowUnix()
	}
	updates := map[string]any{"update_time": at}
	if success {
		updates["total_calls"] = gorm.Expr(`"total_calls" + 1`)
		updates["total_tokens"] = gorm.Expr(`"total_tokens" + ?`, tokens)
	} else {
		updates["total_failures"] = gorm.Expr(`"total_failures" + 1`)
	}
	return s.db.WithContext(ctx).Model(&Channel{}).Where(`"id" = ?`, int64(channelID)).Updates(updates).Error
}

// UsageDetailChain 按 request_id 还原尝试链（attempt 升序）
func (s *Store) UsageDetailChain(ctx context.Context, requestID string) ([]Usage, error) {
	rows := []Usage{}
	if err := s.ready(); err != nil {
		return rows, err
	}
	if strings.TrimSpace(requestID) == "" {
		return rows, nil
	}
	err := s.db.WithContext(ctx).Where(`"request_id" = ?`, requestID).
		Order(`"attempt" ASC, "id" ASC`).Find(&rows).Error
	return rows, err
}

// UsageAggregateQuery 用量聚合入参
type UsageAggregateQuery struct {
	Group     string // model|biz|source|biz_source|day（默认 model）
	ModelName string // 模糊 ILIKE
	BizType   string // 等值
	Source    string // 等值
	Start     *int64 // create_time ≥（秒级闭区间）
	End       *int64 // create_time ≤
}

// UsageTotals 聚合窗口合计
type UsageTotals struct {
	Calls       int64  `gorm:"column:calls" json:"calls"`
	Success     int64  `gorm:"column:success" json:"success"`
	Fail        int64  `gorm:"column:fail" json:"fail"`
	Timeout     int64  `gorm:"column:timeout" json:"timeout"`
	TotalTokens int64  `gorm:"column:total_tokens" json:"total_tokens"`
	Cost        string `gorm:"column:cost" json:"cost"`
}

// UsageAggregateRow 聚合行（按 Group 填充对应维度列；Cost 为 numeric 文本，出口由 handler 格式化为 6 位小数）
type UsageAggregateRow struct {
	Group            string `gorm:"column:group_key" json:"group"`
	ModelID          uint64 `gorm:"column:model_id" json:"model_id,omitempty"`
	ModelName        string `gorm:"column:model_name" json:"model_name,omitempty"`
	BizType          string `gorm:"column:biz_type" json:"biz_type,omitempty"`
	Source           string `gorm:"column:source" json:"source,omitempty"`
	Day              string `gorm:"column:day" json:"day,omitempty"`
	Calls            int64  `gorm:"column:calls" json:"calls"`
	Success          int64  `gorm:"column:success" json:"success"`
	Fail             int64  `gorm:"column:fail" json:"fail"`
	Timeout          int64  `gorm:"column:timeout" json:"timeout"`
	PromptTokens     int64  `gorm:"column:prompt_tokens" json:"prompt_tokens"`
	CompletionTokens int64  `gorm:"column:completion_tokens" json:"completion_tokens"`
	TotalTokens      int64  `gorm:"column:total_tokens" json:"total_tokens"`
	Cost             string `gorm:"column:cost" json:"cost"`
}

// UsageAggregateResult 聚合结果 + 同筛选口径的合计 + 真实下拉选项
type UsageAggregateResult struct {
	Rows    []UsageAggregateRow `json:"list"`
	Totals  UsageTotals         `json:"totals"`
	Filters UsageFilterValues   `json:"filters"`
}

// UsageFilterValues 用量页下拉数据源（各限 100 条）
type UsageFilterValues struct {
	Sources  []string `json:"sources"`
	BizTypes []string `json:"biz_types"`
}

// metricSelect 聚合指标表达式（代码常量；status 分桶互斥：success/fail/timeout，v1 无 breaker 行）
const metricSelect = `COUNT(*) AS calls,
	COUNT(*) FILTER (WHERE "status" = 'success') AS success,
	COUNT(*) FILTER (WHERE "status" = 'fail') AS fail,
	COUNT(*) FILTER (WHERE "status" = 'timeout') AS timeout,
	CAST(COALESCE(SUM("prompt_tokens"), 0) AS BIGINT) AS prompt_tokens,
	CAST(COALESCE(SUM("completion_tokens"), 0) AS BIGINT) AS completion_tokens,
	CAST(COALESCE(SUM("total_tokens"), 0) AS BIGINT) AS total_tokens,
	CAST(COALESCE(SUM("cost"), 0) AS TEXT) AS cost`

// usageAggregateFilter 聚合筛选（与日志列表同口径：model_name 模糊、biz/source 等值、时间闭区间）
func (s *Store) usageAggregateFilter(ctx context.Context, q UsageAggregateQuery) *gorm.DB {
	db := s.db.WithContext(ctx).Model(&Usage{})
	if name := strings.TrimSpace(q.ModelName); name != "" {
		db = db.Where(`"model_name" `+s.likeOp()+` ?`, "%"+name+"%")
	}
	if q.BizType != "" {
		db = db.Where(`"biz_type" = ?`, q.BizType)
	}
	if q.Source != "" {
		db = db.Where(`"source" = ?`, q.Source)
	}
	if q.Start != nil {
		db = db.Where(`"create_time" >= ?`, *q.Start)
	}
	if q.End != nil {
		db = db.Where(`"create_time" <= ?`, *q.End)
	}
	return db
}

// AggregateUsage 用量聚合（五维度闭集；day 用 to_char(to_timestamp(create_time),'YYYY-MM-DD') 且按天倒序，
// 其余按 calls 降序；聚合行上限 100，无分页）。
func (s *Store) AggregateUsage(ctx context.Context, q UsageAggregateQuery) (UsageAggregateResult, error) {
	result := UsageAggregateResult{Rows: []UsageAggregateRow{}, Filters: UsageFilterValues{Sources: []string{}, BizTypes: []string{}}}
	if err := s.ready(); err != nil {
		return result, err
	}
	group := strings.TrimSpace(q.Group)
	if group == "" {
		group = "model"
	}

	var selectExpr, groupExpr, orderExpr string
	switch group {
	case "model":
		selectExpr = `"model_id" AS group_key, "model_id" AS model_id, MAX("model_name") AS model_name, ` + metricSelect
		// groupExpr 必须是**裸列名**：GORM 的 Group(name) 对「单个合法标识符」走 WriteQuoted 自动加引号，
		// 传已带双引号的名字会被二次包裹（SQLite 生成 `"model_id"` ⇒ no such column）。
		// 复合表达式（`a, b` 与函数调用）GORM 判为 Raw 原样透传，不受影响。
		groupExpr = "model_id"
		orderExpr = `calls DESC, group_key ASC`
	case "biz":
		selectExpr = `"biz_type" AS group_key, "biz_type" AS biz_type, ` + metricSelect
		groupExpr = "biz_type"
		orderExpr = `calls DESC, group_key ASC`
	case "source":
		selectExpr = `"source" AS group_key, "source" AS source, ` + metricSelect
		groupExpr = "source"
		orderExpr = `calls DESC, group_key ASC`
	case "biz_source":
		selectExpr = `("biz_type" || '|' || "source") AS group_key, "biz_type" AS biz_type, "source" AS source, ` + metricSelect
		groupExpr = `"biz_type", "source"`
		orderExpr = `calls DESC, group_key ASC`
	case "day":
		dayExpr := `to_char(to_timestamp("create_time"), 'YYYY-MM-DD')`
		if isSQLite(s.db) {
			dayExpr = `strftime('%Y-%m-%d', "create_time", 'unixepoch', 'localtime')`
		}
		selectExpr = dayExpr + ` AS group_key, ` + dayExpr + ` AS day, ` + metricSelect
		groupExpr = dayExpr
		orderExpr = `group_key DESC`
	default:
		return result, ErrUnknownGroup
	}

	if err := s.usageAggregateFilter(ctx, q).
		Select(selectExpr).
		Group(groupExpr).
		Order(orderExpr).
		Limit(AggregateRowLimit).
		Scan(&result.Rows).Error; err != nil {
		return result, err
	}
	if err := s.usageAggregateFilter(ctx, q).Select(metricSelect).Scan(&result.Totals).Error; err != nil {
		return result, err
	}
	filters, err := s.ListUsageFilters(ctx)
	if err != nil {
		return result, err
	}
	result.Filters = filters
	return result, nil
}

// ListUsageFilters 用量页下拉数据源：去重 source / biz_type，各限 100 条
func (s *Store) ListUsageFilters(ctx context.Context) (UsageFilterValues, error) {
	values := UsageFilterValues{Sources: []string{}, BizTypes: []string{}}
	if err := s.ready(); err != nil {
		return values, err
	}
	if err := s.db.WithContext(ctx).Model(&Usage{}).
		Where(`"source" <> ''`).
		Distinct().
		Order(`"source" ASC`).
		Limit(AggregateRowLimit).
		Pluck("source", &values.Sources).Error; err != nil {
		return values, err
	}
	if err := s.db.WithContext(ctx).Model(&Usage{}).
		Where(`"biz_type" <> ''`).
		Distinct().
		Order(`"biz_type" ASC`).
		Limit(AggregateRowLimit).
		Pluck("biz_type", &values.BizTypes).Error; err != nil {
		return values, err
	}
	return values, nil
}

// UsageWindow 一个时间窗的汇总（总览页 h24/d7）
type UsageWindow struct {
	Calls        int64   `gorm:"column:calls" json:"calls"`
	Success      int64   `gorm:"column:success" json:"success"`
	Fail         int64   `gorm:"column:fail" json:"fail"`
	Timeout      int64   `gorm:"column:timeout" json:"timeout"`
	TotalTokens  int64   `gorm:"column:total_tokens" json:"total_tokens"`
	Cost         string  `gorm:"column:cost" json:"cost"`
	AvgLatencyMS float64 `gorm:"column:avg_latency_ms" json:"avg_latency_ms"`
}

// DailyUsage 逐日趋势点（7 个自然日）
type DailyUsage struct {
	Day         string `gorm:"column:day" json:"day"`
	Calls       int64  `gorm:"column:calls" json:"calls"`
	TotalTokens int64  `gorm:"column:total_tokens" json:"total_tokens"`
	Cost        string `gorm:"column:cost" json:"cost"`
}

// TopModelUsage Top 模型行
type TopModelUsage struct {
	ModelID     uint64 `gorm:"column:model_id" json:"model_id"`
	ModelName   string `gorm:"column:model_name" json:"model_name"`
	Calls       int64  `gorm:"column:calls" json:"calls"`
	TotalTokens int64  `gorm:"column:total_tokens" json:"total_tokens"`
	Cost        string `gorm:"column:cost" json:"cost"`
}

// OverviewAggregates 总览页用量聚合（只读；窗口口径照源 Overview.php：h24 = now-86400，不是自然日）
type OverviewAggregates struct {
	H24         UsageWindow     `json:"h24"`
	D7          UsageWindow     `json:"d7"`
	D7Series    []DailyUsage    `json:"d7_series"`
	TopModels   []TopModelUsage `json:"top_models"`
	RecentFails []Usage         `json:"recent_fails"`
}

// windowTotals 单个时间窗的汇总（from 为 create_time 下界，UTC 秒）
func (s *Store) windowTotals(ctx context.Context, from int64) (UsageWindow, error) {
	window := UsageWindow{}
	err := s.db.WithContext(ctx).Model(&Usage{}).
		Select(metricSelect+`,
			CAST(COALESCE(AVG("latency_ms"), 0) AS REAL) AS avg_latency_ms`).
		Where(`"create_time" >= ?`, from).
		Scan(&window).Error
	return window, err
}

// OverviewAggregates 总览聚合：h24（近 24 小时）、d7（近 7 日单一聚合）、d7_series（7 个自然日逐日，
// 相对源的加项）、Top 模型（近 24h，≤5）、最近失败（status ∈ fail|timeout|breaker，≤5）。
func (s *Store) OverviewAggregates(ctx context.Context, now int64) (OverviewAggregates, error) {
	result := OverviewAggregates{D7Series: []DailyUsage{}, TopModels: []TopModelUsage{}, RecentFails: []Usage{}}
	if err := s.ready(); err != nil {
		return result, err
	}
	if now <= 0 {
		now = nowUnix()
	}
	h24, err := s.windowTotals(ctx, now-86400)
	if err != nil {
		return result, err
	}
	d7, err := s.windowTotals(ctx, now-7*86400)
	if err != nil {
		return result, err
	}
	result.H24 = h24
	result.D7 = d7

	// d7_series：7 个自然日（按数据库会话时区切日，与 to_char 的展示口径一致）
	dayExpr := `to_char(to_timestamp("create_time"), 'YYYY-MM-DD')`
	if isSQLite(s.db) {
		dayExpr = `strftime('%Y-%m-%d', "create_time", 'unixepoch', 'localtime')`
	}
	d7Query := s.db.WithContext(ctx).Model(&Usage{}).
		Select(dayExpr + ` AS day,
		        COUNT(*) AS calls,
		        CAST(COALESCE(SUM("total_tokens"), 0) AS BIGINT) AS total_tokens,
		        CAST(COALESCE(SUM("cost"), 0) AS TEXT) AS cost`)
	if isSQLite(s.db) {
		d7Query = d7Query.Where(`"create_time" >= ?`, now-7*86400)
	} else {
		d7Query = d7Query.Where(`"create_time" >= EXTRACT(EPOCH FROM (date_trunc('day', to_timestamp(?)) - interval '6 days'))::bigint`, now)
	}
	if err := d7Query.
		Group(dayExpr).
		Order(`day ASC`).
		Scan(&result.D7Series).Error; err != nil {
		return result, err
	}

	if err := s.db.WithContext(ctx).Model(&Usage{}).
		Select(`"model_id" AS model_id,
		        MAX("model_name") AS model_name,
		        COUNT(*) AS calls,
		        CAST(COALESCE(SUM("total_tokens"), 0) AS BIGINT) AS total_tokens,
		        CAST(COALESCE(SUM("cost"), 0) AS TEXT) AS cost`).
		Where(`"create_time" >= ?`, now-86400).
		Group("model_id").
		Order(`calls DESC, model_id ASC`).
		Limit(5).
		Scan(&result.TopModels).Error; err != nil {
		return result, err
	}

	if err := s.db.WithContext(ctx).Model(&Usage{}).
		Where(`"status" IN ?`, []string{UsageStatusFail, UsageStatusTimeout, UsageStatusBreaker}).
		Order(`"id" DESC`).Limit(5).
		Find(&result.RecentFails).Error; err != nil {
		return result, err
	}
	return result, nil
}

// ===== 熔断事件 =====

// InsertBreakerEvent 写一条熔断事件（CircuitStore 契约；best-effort：
// 调用方 circuit.go 落库失败只记日志，不影响主流程）。
func (s *Store) InsertBreakerEvent(ctx context.Context, event *BreakerEvent) error {
	if err := s.ready(); err != nil {
		return err
	}
	if event == nil {
		return errors.New("插件 ai：熔断事件为空")
	}
	event.LastError = truncateRunes(event.LastError, lastErrorMaxLen)
	at := nowUnix()
	if event.CreateTime == 0 {
		event.CreateTime = at
	}
	if event.UpdateTime == 0 {
		event.UpdateTime = at
	}
	return s.db.WithContext(ctx).Create(event).Error
}

// ListBreakerEvents 单渠道最近事件（按 id DESC，默认 20 条，上限 100）
func (s *Store) ListBreakerEvents(ctx context.Context, channelID int64, limit int) ([]BreakerEvent, error) {
	rows := []BreakerEvent{}
	if err := s.ready(); err != nil {
		return rows, err
	}
	err := s.db.WithContext(ctx).Where(`"channel_id" = ?`, channelID).
		Order(`"id" DESC`).Limit(normalizeLimit(limit, BreakerEventListLimit)).
		Find(&rows).Error
	return rows, err
}

// RecentBreakerEvents 全局最近事件（总览页，≤5 条）
func (s *Store) RecentBreakerEvents(ctx context.Context, limit int) ([]BreakerEvent, error) {
	rows := []BreakerEvent{}
	if err := s.ready(); err != nil {
		return rows, err
	}
	err := s.db.WithContext(ctx).Order(`"id" DESC`).Limit(normalizeLimit(limit, 5)).Find(&rows).Error
	return rows, err
}

// 编译期断言：Store 实现熔断状态机的全部 SQL 原语（circuit.go 的 CircuitStore），
// 同时满足编排层端口（service.go 的 Repository，内嵌 CircuitStore）。
var (
	_ CircuitStore = (*Store)(nil)
	_ Repository   = (*Store)(nil)
)
