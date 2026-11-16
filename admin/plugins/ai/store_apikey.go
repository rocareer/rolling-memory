// plugin: ai v0.2.0
// store_apikey.go 是应用接入（API Key）的 SQL 原子操作住所（表 plugin_ai_api_key）。
//
// 职责边界与 store.go 同口径：本文件只有 SQL 与事务；入参收敛在 handler_apikey.go 的纯函数，
// 鉴权决策在 apikey_auth.go。安全纪律：值一律走占位符，只有「代码常量」进 SQL 文本；
// key_hash 只进 WHERE / SET（**绝不进出口结构**——APIKey.KeyHash 带 json:"-"）。
//
// 缓存口径：本表**没有任何进程内缓存**——停用/删除/重置在下一个请求即生效（与源同口径，
// 也是「禁用错 Key 立刻止血」的运维前提）。只有插件配置（全局兜底 key）走 plugin.go 的 5s TTL 缓存。
package ai

import (
	"context"
	"errors"
	"strings"

	"gorm.io/gorm"
)

// DefaultAPIKeyListLimit 应用接入列表默认每页条数（照源 ApiKey.php 的 clamp_limit 缺省 10）。
const DefaultAPIKeyListLimit = 10

// apiKeySortColumns 排序白名单：key_hash 刻意不在其中（密钥摘要不提供任何可探测/可排序的入口）。
var apiKeySortColumns = map[string]string{
	"id":           `"id"`,
	"name":         `"name"`,
	"source":       `"source"`,
	"rate_limit":   `"rate_limit"`,
	"status":       `"status"`,
	"expire_at":    `"expire_at"`,
	"last_used_at": `"last_used_at"`,
	"create_time":  `"create_time"`,
	"update_time":  `"update_time"`,
}

// APIKeyFilter 应用接入列表筛选（顶层等值 status/source + keyword 快搜 name ILIKE；
// 高级 wheres 由 handler 合并——与渠道/模型列表同一契约）。
type APIKeyFilter struct {
	Page    int
	Limit   int
	Keyword string
	Source  string
	Status  *int16
	Sort    string
	Order   string
}

// APIKeyListResult 应用接入分页结果。
type APIKeyListResult struct {
	Rows  []APIKey `json:"list"`
	Total int64    `json:"total"`
}

// APIKeyPatch 可编辑列（nil = 不修改）。
//
// ModelScope 的「显式清空」用「指向空切片」（&[]string{}）表达，与 nil（不修改）区分；
// ExpireAt 传 0 表示清空（列写 NULL）；Source 允许改（改了立刻影响后续计费归因，见 handler 头注）。
type APIKeyPatch struct {
	Name       *string
	Source     *string
	ModelScope *[]string
	RateLimit  *int
	ExpireAt   *int64
	Status     *int16
	Remark     *string
}

// apiKeyQuery 列表/计数的共同筛选（同一实现，避免 list 与 total 条件漂移）。
func (s *Store) apiKeyQuery(ctx context.Context, f APIKeyFilter) *gorm.DB {
	q := s.db.WithContext(ctx).Model(&APIKey{})
	if f.Status != nil {
		q = q.Where(`"status" = ?`, *f.Status)
	}
	if source := strings.TrimSpace(f.Source); source != "" {
		q = q.Where(`"source" = ?`, source)
	}
	if keyword := strings.TrimSpace(f.Keyword); keyword != "" {
		q = q.Where(`"name" `+s.likeOp()+` ?`, "%"+keyword+"%")
	}
	return q
}

// ListAPIKeys 应用接入分页列表（默认 id DESC；照源 index 的默认排序）。
func (s *Store) ListAPIKeys(ctx context.Context, f APIKeyFilter) (APIKeyListResult, error) {
	result := APIKeyListResult{Rows: []APIKey{}}
	if err := s.ready(); err != nil {
		return result, err
	}
	if err := s.apiKeyQuery(ctx, f).Count(&result.Total).Error; err != nil {
		return result, err
	}
	offset, limit := normalizePage(f.Page, f.Limit, DefaultAPIKeyListLimit)
	order := orderClause(f.Sort, f.Order, apiKeySortColumns, `"id" DESC`)
	if err := s.apiKeyQuery(ctx, f).Order(order).Offset(offset).Limit(limit).Find(&result.Rows).Error; err != nil {
		return result, err
	}
	return result, nil
}

// listAPIKeysWithWheres 与 ListAPIKeys 同口径（同一基础筛选/排序/分页），只多一层 wheres。
// 条件由接口层拼装（store 的列表原语不接受 wheres），列名与操作符都已过 apiKeyWhereFields 白名单、
// 值全参数化 —— 与 handler_channel.go 的 listChannelsWithWheres 同款分工。
func (s *Store) listAPIKeysWithWheres(ctx context.Context, f APIKeyFilter, groups []whereGroup) (APIKeyListResult, error) {
	result := APIKeyListResult{Rows: []APIKey{}}
	if err := s.ready(); err != nil {
		return result, err
	}
	countQuery, err := applyWhereGroups(s.apiKeyQuery(ctx, f), apiKeyWhereFields, groups)
	if err != nil {
		return result, err
	}
	if err := countQuery.Count(&result.Total).Error; err != nil {
		return result, err
	}
	listQuery, err := applyWhereGroups(s.apiKeyQuery(ctx, f), apiKeyWhereFields, groups)
	if err != nil {
		return result, err
	}
	offset, limit := normalizePage(f.Page, f.Limit, DefaultAPIKeyListLimit)
	order := orderClause(f.Sort, f.Order, apiKeySortColumns, `"id" DESC`)
	if err := listQuery.Order(order).Offset(offset).Limit(limit).Find(&result.Rows).Error; err != nil {
		return result, err
	}
	return result, nil
}

// GetAPIKey 按 ID 取行（不存在返回 gorm.ErrRecordNotFound）。
func (s *Store) GetAPIKey(ctx context.Context, id int64) (*APIKey, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	var row APIKey
	if err := s.db.WithContext(ctx).Where(`"id" = ?`, id).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// LookupAPIKeyByHash 按 sha256 key_hash 取行（公开面鉴权的唯一查询；未命中返回 gorm.ErrRecordNotFound）。
//
// 只走唯一索引 uk(key_hash)；**不**在这里判 status/expire_at —— 判定归 apikey_auth.go 的
// Authenticatable（本方法只负责取行，便于「命中但停用/过期 ⇒ 401」与「未命中 ⇒ 回落全局」分开断言）。
func (s *Store) LookupAPIKeyByHash(ctx context.Context, keyHash string) (*APIKey, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	hash := strings.TrimSpace(keyHash)
	if hash == "" {
		return nil, gorm.ErrRecordNotFound
	}
	var row APIKey
	if err := s.db.WithContext(ctx).Where(`"key_hash" = ?`, hash).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// CreateAPIKey 新增应用（调用方负责必填/白名单校验；本方法只补时间戳与空列表形态）。
//
// GORM 陷阱（与渠道表同款，store_db_test.go:131-134 有登记）：status 带 default:1 标签、零值会被
// Create 省略 ⇒ 走本方法建不出 status=0 的行（落库恒为 DB 默认 1）。停用走 SwitchAPIKey；
// 签发侧（handler_apikey.go 的 issue）照源 add 的口径恒建启用行。
func (s *Store) CreateAPIKey(ctx context.Context, row *APIKey) error {
	if err := s.ready(); err != nil {
		return err
	}
	if row == nil {
		return errors.New("插件 ai：应用接入行为空")
	}
	if row.ModelScope == nil {
		// jsonb 列的形态纪律：空白名单恒为 '[]'（serializer:json 对 nil 会写成 JSON null）。
		row.ModelScope = []string{}
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

// UpdateAPIKey 更新可编辑列（返回受影响行数；0 = 不存在或值未变化）。
func (s *Store) UpdateAPIKey(ctx context.Context, id int64, patch APIKeyPatch) (int64, error) {
	if err := s.ready(); err != nil {
		return 0, err
	}
	updates := map[string]any{"update_time": nowUnix()}
	if patch.Name != nil {
		updates["name"] = *patch.Name
	}
	if patch.Source != nil {
		updates["source"] = *patch.Source
	}
	if patch.ModelScope != nil {
		// map 更新不走字段 serializer ⇒ 显式 JSON 文本（PostgreSQL 用 ::jsonb，SQLite 直接写文本）。
		if isSQLite(s.db) {
			updates["model_scope"] = apiKeyScopeJSON(*patch.ModelScope)
		} else {
			updates["model_scope"] = gorm.Expr("?::jsonb", apiKeyScopeJSON(*patch.ModelScope))
		}
	}
	if patch.RateLimit != nil {
		updates["rate_limit"] = *patch.RateLimit
	}
	if patch.ExpireAt != nil {
		if *patch.ExpireAt <= 0 {
			updates["expire_at"] = nil // 0 = 清空过期时间（永不过期）
		} else {
			updates["expire_at"] = *patch.ExpireAt
		}
	}
	if patch.Status != nil {
		updates["status"] = *patch.Status
	}
	if patch.Remark != nil {
		updates["remark"] = *patch.Remark
	}
	res := s.db.WithContext(ctx).Model(&APIKey{}).Where(`"id" = ?`, id).Updates(updates)
	return res.RowsAffected, res.Error
}

// SwitchAPIKey 启停：只改 status（返回受影响行数）。
func (s *Store) SwitchAPIKey(ctx context.Context, id int64, status int16) (int64, error) {
	if err := s.ready(); err != nil {
		return 0, err
	}
	res := s.db.WithContext(ctx).Model(&APIKey{}).Where(`"id" = ?`, id).
		Updates(map[string]any{"status": status, "update_time": nowUnix()})
	return res.RowsAffected, res.Error
}

// RotateAPIKey 重置：换 key_hash + key_prefix（旧明文立刻失效），不动其它列。
func (s *Store) RotateAPIKey(ctx context.Context, id int64, keyHash, keyPrefix string, at int64) (int64, error) {
	if err := s.ready(); err != nil {
		return 0, err
	}
	if at <= 0 {
		at = nowUnix()
	}
	res := s.db.WithContext(ctx).Model(&APIKey{}).Where(`"id" = ?`, id).
		Updates(map[string]any{"key_hash": keyHash, "key_prefix": keyPrefix, "update_time": at})
	return res.RowsAffected, res.Error
}

// TouchAPIKeyLastUsed 刷新最近使用时刻（公开面每请求一次的后台异步写；
// at ≤ 0 时取当前时刻）。失败由调用方只记日志——last_used_at 是展示/运维字段，不是计费口径。
func (s *Store) TouchAPIKeyLastUsed(ctx context.Context, id int64, at int64) (int64, error) {
	if err := s.ready(); err != nil {
		return 0, err
	}
	if at <= 0 {
		at = nowUnix()
	}
	res := s.db.WithContext(ctx).Model(&APIKey{}).Where(`"id" = ?`, id).
		Updates(map[string]any{"last_used_at": at})
	return res.RowsAffected, res.Error
}

// DeleteAPIKeys 批量删除（删除后其 Key 立即失效：本表无缓存）。
func (s *Store) DeleteAPIKeys(ctx context.Context, ids []int64) error {
	if err := s.ready(); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Where(`"id" IN ?`, ids).Delete(&APIKey{}).Error
}
