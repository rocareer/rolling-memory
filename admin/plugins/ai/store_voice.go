// plugin: ai v0.2.0
// store_voice.go 是 plugin_ai_voice 的数据访问层（本文件**不占** store.go，避免与其它包撞车主）。
//
// 与渠道/模型列表同构：同一实现服务「列表 / 计数」，基础筛选与排序都在 voiceQuery 上，
// 高级检索（wheres）只由接口层拼装（handler_voice.go 的 listVoicesWithWheres）。
package ai

import (
	"context"
	"errors"
	"strings"

	"gorm.io/gorm"
)

// DefaultVoiceListLimit 音色列表默认每页条数（与渠道/模型同口径）。
const DefaultVoiceListLimit = 10

// voiceSortColumns 排序白名单（白名单外的 sort 一律拒，见 handler 的 validateSort）。
var voiceSortColumns = map[string]string{
	"id":          `"id"`,
	"name":        `"name"`,
	"model":       `"model"`,
	"voice":       `"voice"`,
	"format":      `"format"`,
	"status":      `"status"`,
	"create_time": `"create_time"`,
	"update_time": `"update_time"`,
}

// VoiceFilter 音色列表筛选（顶层等值 status/model + keyword 快搜 name|voice）。
type VoiceFilter struct {
	Page    int
	Limit   int
	Keyword string
	Status  *int16
	Model   string
	Sort    string
	Order   string
}

// VoiceListResult 音色分页结果（无统计条：音色是配置面，不需要渠道那套健康读数）。
type VoiceListResult struct {
	Rows  []Voice `json:"list"`
	Total int64   `json:"total"`
}

// VoicePatch 音色可编辑列（nil = 不修改）。
type VoicePatch struct {
	Name   *string
	Model  *string
	Voice  *string
	Format *string
	Status *int16
	Remark *string
}

// voiceQuery 音色列表/计数的共同筛选（list 与 total 用同一份，避免条件漂移）。
//
// 快搜同时命中 name 与 voice：管理面既可能按「音色名」找，也可能按上游音色 ID 找。
func (s *Store) voiceQuery(ctx context.Context, f VoiceFilter) *gorm.DB {
	q := s.db.WithContext(ctx).Model(&Voice{})
	if f.Status != nil {
		q = q.Where(`"status" = ?`, *f.Status)
	}
	if model := strings.TrimSpace(f.Model); model != "" {
		q = q.Where(`"model" = ?`, model)
	}
	if keyword := strings.TrimSpace(f.Keyword); keyword != "" {
		like := "%" + keyword + "%"
		op := s.likeOp()
		q = q.Where(`"name" `+op+` ? OR "voice" `+op+` ?`, like, like)
	}
	return q
}

// ListVoices 音色分页列表（默认排序 update_time DESC, id DESC：配置面按最近改动看）。
func (s *Store) ListVoices(ctx context.Context, f VoiceFilter) (VoiceListResult, error) {
	result := VoiceListResult{Rows: []Voice{}}
	if err := s.ready(); err != nil {
		return result, err
	}
	if err := s.voiceQuery(ctx, f).Count(&result.Total).Error; err != nil {
		return result, err
	}
	offset, limit := normalizePage(f.Page, f.Limit, DefaultVoiceListLimit)
	order := orderClause(f.Sort, f.Order, voiceSortColumns, `"update_time" DESC, "id" DESC`)
	if err := s.voiceQuery(ctx, f).Order(order).Offset(offset).Limit(limit).Find(&result.Rows).Error; err != nil {
		return result, err
	}
	return result, nil
}

// GetVoice 按 ID 取音色（不存在返回 gorm.ErrRecordNotFound）。
func (s *Store) GetVoice(ctx context.Context, id int64) (*Voice, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	var row Voice
	if err := s.db.WithContext(ctx).Where(`"id" = ?`, id).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// FindVoiceByName 按音色名取行（唯一键口径；仅诊断与用例使用）。
func (s *Store) FindVoiceByName(ctx context.Context, name string) (*Voice, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	var row Voice
	if err := s.db.WithContext(ctx).Where(`"name" = ?`, strings.TrimSpace(name)).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// CreateVoice 新增音色（调用方负责必填与白名单校验；本方法只补时间戳）。
func (s *Store) CreateVoice(ctx context.Context, row *Voice) error {
	if err := s.ready(); err != nil {
		return err
	}
	if row == nil {
		return errors.New("插件 ai：音色为空")
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

// UpdateVoice 更新音色可编辑列（返回受影响行数；0 = 不存在或值未变化）。
func (s *Store) UpdateVoice(ctx context.Context, id int64, patch VoicePatch) (int64, error) {
	if err := s.ready(); err != nil {
		return 0, err
	}
	updates := map[string]any{"update_time": nowUnix()}
	if patch.Name != nil {
		updates["name"] = *patch.Name
	}
	if patch.Model != nil {
		updates["model"] = *patch.Model
	}
	if patch.Voice != nil {
		updates["voice"] = *patch.Voice
	}
	if patch.Format != nil {
		updates["format"] = *patch.Format
	}
	if patch.Status != nil {
		updates["status"] = *patch.Status
	}
	if patch.Remark != nil {
		updates["remark"] = *patch.Remark
	}
	res := s.db.WithContext(ctx).Model(&Voice{}).Where(`"id" = ?`, id).Updates(updates)
	return res.RowsAffected, res.Error
}

// DeleteVoices 批量删除音色（音色行没有下游引用，直接删）。
func (s *Store) DeleteVoices(ctx context.Context, ids []int64) error {
	if err := s.ready(); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Where(`"id" IN ?`, ids).Delete(&Voice{}).Error
}

// voiceExists 存在性复核（PG 在「值未变化」时也可能报 RowsAffected=0，不能只凭它判「不存在」）。
func voiceExists(ctx context.Context, store *Store, id int64) bool {
	_, err := store.GetVoice(ctx, id)
	return err == nil
}
