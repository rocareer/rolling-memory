// plugin: kb v1.0.0
// 知识库作业接线 · kb:rebuild 库级重切/重嵌（处理器 + 水位窗口追平）。
//
// 从 job.go 拆出（文件规模纪律：按职责拆分而非按行数硬切）；共享装配面见 job.go。
package kb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/service/jobs"
	kbembed "github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/embed"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/ingest"

	"gorm.io/gorm"
)

// ==================== kb:rebuild 处理器 ====================

// rebuildHandler kb:rebuild 处理器（库级逐窗追平）
type rebuildHandler struct{}

// Run 执行一次库级重切/重嵌
func (rebuildHandler) Run(ctx context.Context, j *jobs.Job) error {
	var p ingest.RebuildParams
	if err := json.Unmarshal(j.Params, &p); err != nil {
		return fmt.Errorf("kb:rebuild 参数解析失败: %w", err)
	}
	if p.LibraryID == 0 {
		return errors.New("kb:rebuild 缺 library_id（拒绝无参作业）")
	}
	svc := NewService()
	res, err := svc.RunRebuild(ctx, p, j)
	if err != nil {
		return err
	}
	if res != nil {
		if b, err := json.Marshal(res); err == nil {
			j.Result(b)
		}
	}
	return nil
}

// RebuildResult 库级重切/重嵌回执
type RebuildResult struct {
	LibraryID uint           `json:"library_id"`
	Mode      string         `json:"mode"`
	Processed int            `json:"processed"`
	Ready     int            `json:"ready"`
	Failed    int            `json:"failed"`
	Suspended int            `json:"suspended"`
	Watermark uint           `json:"watermark"` // 出口水位（单调；只覆盖「连续成功前缀」）
	Windows   int            `json:"windows"`
	Embed     *kbembed.Stats `json:"embed,omitempty"`
}

// RunRebuild 库级重切/重嵌：
//   - rechunk：从水位起逐窗追平（窗口 = min(缺省, kb.budget.max_docs_per_job)）；每窗内逐文档走
//     RunIngest 单链（失败文档不堵尾：继续后续窗；水位只推进「连续成功前缀」）；
//   - reembed：只重嵌（切片产物不动；kb.embed.enabled=false ⇒ 空转收工）。
func (s *Service) RunRebuild(ctx context.Context, p ingest.RebuildParams, j *jobs.Job) (*RebuildResult, error) {
	lib, err := gorm.G[model.KBLibrary](s.db).Select("id, name").Where("id = ?", p.LibraryID).First(ctx)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("kb:rebuild 目标库不存在或已删: %d", p.LibraryID)
	}
	if err != nil {
		return nil, fmt.Errorf("kb:rebuild 读取目标库失败: %w", err)
	}
	mode := strings.TrimSpace(p.Mode)
	if mode == "" {
		mode = ingest.RebuildModeRechunk
	}
	if mode != ingest.RebuildModeRechunk && mode != ingest.RebuildModeReembed {
		return nil, fmt.Errorf("kb: 重切模式未登记: %s", mode)
	}
	res := &RebuildResult{LibraryID: lib.ID, Mode: mode}

	if mode == ingest.RebuildModeReembed {
		if !embedEnabled() {
			res.Ready = 0
			return res, nil
		}
		stats, err := kbembed.EmbedLibraryPending(ctx, s.db, lib.ID, embedOptions(func(done, total int64) {
			if j != nil {
				_ = j.Progress(ctx, done, total)
			}
		}))
		if err != nil {
			return nil, err
		}
		res.Embed = &stats
		res.Ready = stats.Embedded + stats.Skipped
		if !stats.Ok() {
			return res, fmt.Errorf("kb: 重嵌失败 %d 条: %v", stats.Failed, stats.Errors)
		}
		return res, nil
	}

	window := p.Window
	if window <= 0 {
		window = ingest.DefaultRebuildWindow
	}
	if window > MaxHardLimit {
		window = MaxHardLimit
	}
	if lim := budgetLimits().MaxDocsPerJob; lim > 0 && lim < window {
		window = lim // 单作业文档数上限 = 窗口钳制（不触发挂起；超大库由多次投递分段处理）
	}

	wm, err := ingest.LoadWatermark(ctx, s.db, lib.ID)
	if err != nil {
		return nil, err
	}
	if p.Reset {
		wm = 0 // 全量档：自 0 起扫（= 改档后整库重切的可达路径；水位行只前进、不被本档改写）
	}
	total, err := s.pendingDocCount(ctx, lib.ID, wm)
	if err != nil {
		return nil, err
	}
	res.Watermark = wm
	for win := 0; win < ingest.MaxRebuildWindows; win++ {
		docs, err := s.pendingDocs(ctx, lib.ID, wm, window)
		if err != nil {
			return res, err
		}
		if len(docs) == 0 {
			break
		}
		res.Windows++
		contiguous := wm // 「≤ 该 id 的全部候选文档已终态」的连续前缀（失败/挂起处**停住**）
		stopped := false // 本窗首处非终态（失败/挂起）后冻结 contiguous：后续成功不得越过（已整改：
		//                  原实现会在失败文档之后继续推进水位 ⇒ 水位越过失败文档、退避重试空转、失败文档永不重试）
		for _, doc := range docs {
			// 逐文档走单链（进度由本循环按文档数上报——传 nil 作业避免单文档阶段进度掩盖文档级读数）
			one, err := s.RunIngest(ctx, ingest.IngestParams{DocumentID: doc.ID, Reason: "rebuild"}, nil)
			if err != nil {
				res.Failed++
				stopped = true
				log.Printf("[kb] rebuild 文档 %d 失败: %v", doc.ID, err)
				continue // 失败不堵尾：继续后续文档（产物照落）；水位冻结在该文档之前
			}
			switch one.State {
			case "ready":
				res.Ready++
				if !stopped {
					contiguous = doc.ID
				}
			case "suspended":
				res.Suspended++ // 挂起待批：非终态 ⇒ 水位不得越过（stopped 冻结；批准后复投续跑）
				stopped = true
			case "canceled", "skipped":
				if !stopped {
					contiguous = doc.ID // 终态（取消/已删/换出被取代）：不堵水位
				}
			}
		}
		res.Processed += len(docs)
		if contiguous > wm {
			if err := ingest.AdvanceWatermark(ctx, s.db, lib.ID, contiguous); err != nil {
				return res, err
			}
			wm = contiguous
			res.Watermark = wm
		} else {
			break // 本窗无推进（首处失败/挂起堵住尾巴）：本作业收工——失败由退避重试再跑、挂起等批准复投
		}
		if j != nil {
			_ = j.Progress(ctx, int64(res.Processed), int64(total))
		}
		if len(docs) < window {
			break
		}
	}
	if res.Failed > 0 {
		return res, fmt.Errorf("kb:rebuild 有 %d 个文档失败（水位只推进连续成功前缀；失败文档可复投重试）", res.Failed)
	}
	return res, nil
}

// pendingDocs 待处理文档窗（水位后、按 id 升序；keyset 走 idx_kb_document_library_id）
func (s *Service) pendingDocs(ctx context.Context, libraryID, afterID uint, limit int) ([]model.KBDocument, error) {
	rows, err := gorm.G[model.KBDocument](s.db).
		Select("id, library_id, build_state").
		Where("library_id = ? AND id > ?", libraryID, afterID).
		Order("id ASC").Limit(limit).Find(ctx)
	if err != nil {
		return nil, fmt.Errorf("kb: 扫描待处理文档失败: %w", err)
	}
	return rows, nil
}

// pendingDocCount 待处理文档总数（水位后；索引 count，作业面用）
func (s *Service) pendingDocCount(ctx context.Context, libraryID, afterID uint) (int64, error) {
	n, err := gorm.G[model.KBDocument](s.db).
		Where("library_id = ? AND id > ?", libraryID, afterID).
		Count(ctx, "id")
	if err != nil {
		return 0, fmt.Errorf("kb: 统计待处理文档失败: %w", err)
	}
	return n, nil
}
