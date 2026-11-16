// plugin: kb v1.0.0
// 知识库匹配面回填：kb_chunk.match_text 存量回填核心（aigo kb reindex）。
//
// 背景（migrations/1_core.up.sql 的 match_text 派生列）：match_text 为匹配面唯一真源（chunk.ChunkTextOf 派生文本）；
// 迁移后存量行保持空串 = 「未回填」标记（verbatim 正文非空 ⇒ 空串 ⇔ 未回填），本回填按行复算后
// 幂等写入。tsv 为 DB 生成列（恒自 match_text 派生）⇒ 回填即自动对齐，无需另写 tsv。
//
// 口径（禁第二处）：
//   - 复算单点 = chunk.RowTextOf（行级：标题路径 / 表头 / config_snapshot 自行内真源重建；
//     与写侧 insertChunks、嵌入侧 embedTextOf 同一函数链）；
//   - 幂等：仅当 match_text IS DISTINCT FROM 复算值才写（复跑零写入；并发已回填行不重写）；
//   - 响亮失败：行级复算失败（快照/JSON 非法）= 计入 Failed + Errors 点名（截断），批次收口返回错误；
//   - 分页：keyset 窗口（id > cursor ORDER BY id LIMIT window），禁无界扫描（SQL 纪律）。
package ingest

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/chunk"

	"gorm.io/gorm"
)

// MatchTextOptions 回填选项
type MatchTextOptions struct {
	LibraryID uint // 库收窄（0 = 全库；大库/并发面可逐库复跑）
	DryRun    bool // 只读统计（不写库）：报告「待更新」行数（运维先看，后复跑落地）
	Window    int  // 扫描窗（keyset 分页；≤0 回落 500）
}

// MatchTextStats 回填读数（逐窗累计；可复跑，读数相加无重复计入——幂等）
type MatchTextStats struct {
	Scanned         int64    // 扫描行数
	Pending         int64    // 需更新行数（DryRun 时 = 实际待写数）
	Updated         int64    // 实际写入行数（DryRun 恒 0）
	Unchanged       int64    // 已对齐行数（幂等复跑时的主读数）
	SnapshotMissing int64    // config_snapshot 缺失行数（按代码缺省档复算——如实计数，提示核对）
	Failed          int64    // 行级复算失败数（响亮失败；Errors 为样例）
	Errors          []string // 失败样例（截断）
}

// defaultMatchTextWindow 扫描窗缺省
const defaultMatchTextWindow = 500

// maxMatchTextErrors 失败样例留存上限（防大表全量失败时读数膨胀）
const maxMatchTextErrors = 20

// ReindexMatchText 回填 kb_chunk.match_text（全库或单库；幂等可复跑）。
// 返回读数为本次作用域（库收窄时 = 该库）的累计；行级复算失败（Failed > 0）时仍返回读数并带响亮错误收口。
func ReindexMatchText(ctx context.Context, db *gorm.DB, opts MatchTextOptions) (MatchTextStats, error) {
	var stat MatchTextStats
	if db == nil {
		return stat, errors.New("kb: 回填参数非法（db 为空）")
	}
	win := opts.Window
	if win <= 0 {
		win = defaultMatchTextWindow
	}
	table := tableName(db, model.KBChunk{})

	cursor := uint(0)
	for {
		q := gorm.G[model.KBChunk](db).
			Select("id, text, heading_path, table_meta, config_snapshot, match_text").
			Where("id > ?", cursor)
		if opts.LibraryID > 0 {
			q = q.Where("library_id = ?", opts.LibraryID)
		}
		rows, err := q.Order("id ASC").Limit(win).Find(ctx)
		if err != nil {
			return stat, fmt.Errorf("kb: 扫描切片失败: %w", err)
		}
		if len(rows) == 0 {
			break
		}
		for i := range rows {
			r := &rows[i]
			stat.Scanned++
			if r.ConfigSnapshot == nil || strings.TrimSpace(*r.ConfigSnapshot) == "" {
				stat.SnapshotMissing++
			}
			derived, err := chunk.RowTextOf(r.Text, r.HeadingPath, r.TableMeta, r.ConfigSnapshot)
			if err != nil {
				stat.Failed++
				if len(stat.Errors) < maxMatchTextErrors {
					stat.Errors = append(stat.Errors, fmt.Sprintf("切片 %d: %v", r.ID, err))
				}
				continue
			}
			if derived == r.MatchText {
				stat.Unchanged++
				continue
			}
			stat.Pending++
			if opts.DryRun {
				continue
			}
			// 幂等写（并发已回填行不重写；tsv 生成列随 match_text 自动重算——migrations/1_core.up.sql 的 match_text 派生列）
			tx := db.WithContext(ctx).Exec(
				"UPDATE "+table+" SET match_text = ? WHERE id = ? AND match_text IS DISTINCT FROM ?",
				derived, r.ID, derived)
			if tx.Error != nil {
				return stat, fmt.Errorf("kb: 回填切片 %d 失败: %w", r.ID, tx.Error)
			}
			stat.Updated += tx.RowsAffected
		}
		cursor = rows[len(rows)-1].ID
	}
	if stat.Failed > 0 {
		return stat, fmt.Errorf("kb: 匹配面回填存在 %d 行复算失败（快照/JSON 非法；样例 %d 条已列）——修复后复跑", stat.Failed, len(stat.Errors))
	}
	return stat, nil
}

// MatchTextPendingProbe 未回填探测（只读、可复跑；aigo kb check 匹配面回填段用）：返回 0/1 ——
// kb_chunk 中存在「match_text 为空串且 verbatim 正文非空」的行（空串 ⇔ 未回填，标记口径见 migrations/1_core.up.sql）。
// 命中 ⇒ 该行词面三腿（exact/fts/trgm）不可达（静默失召回的可观测面），处置 = aigo kb reindex。
// 成本：无专门索引——命中即短停（LIMIT 1 存在性探测）；未命中 = 一遍表扫（巡检 CLI、手动运行，kb_chunk
// 现役体量见读数；上量后如需可另议部分索引，属新迁移）。
func MatchTextPendingProbe(ctx context.Context, db *gorm.DB) (int64, error) {
	if db == nil {
		return 0, errors.New("kb: 匹配面未回填探测失败（db 为空）")
	}
	var n int64
	sqlText := "SELECT COUNT(*) FROM (SELECT 1 FROM " + tableName(db, model.KBChunk{}) +
		" WHERE match_text = '' AND text <> '' LIMIT 1) t"
	if err := db.WithContext(ctx).Raw(sqlText).Scan(&n).Error; err != nil {
		return 0, fmt.Errorf("kb: 匹配面未回填探测失败: %w", err)
	}
	return n, nil
}
