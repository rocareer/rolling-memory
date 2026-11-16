// plugin: kb v1.0.0
// 知识库作业接线 · 支撑面（配置读取单点 / 文档读取小工具）。
//
// 从 job.go 拆出（文件规模纪律：job.go 接线链已超 1024 行截点——按职责拆分而非按行数硬切）。
// 本文件只承载「无业务决策」的读取面：一切数值来源 = config/kb.yaml（缺省档在 ingest 包常量），
// 一切行读取经泛型 API + 显式列常量（禁 SELECT *）。
package kb

import (
	"context"
	"errors"
	"fmt"

	"github.com/ai-go-hub/ai-go-admin/plugins/kb/kbcfg"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/embed"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/ingest"

	"gorm.io/gorm"
)

// ==================== 配置读取（config/kb.yaml 单点） ====================

// budgetLimits 预算系数与上限（kb.budget.*；缺省档见 ingest.BudgetLimits）
func budgetLimits() ingest.BudgetLimits {
	c := kbcfg.Get().Budget
	return ingest.BudgetLimits{
		MaxDocsPerJob:      c.MaxDocsPerJob,
		MaxChunksPerJob:    c.MaxChunksPerJob,
		MaxCharsPerJob:     c.MaxCharsPerJob,
		MaxEstTokensPerJob: float64(c.MaxEstTokensPerJob),
		CJKTokenPerChar:    c.CJKTokenPerChar,
		NonCJKTokenPerChar: c.NonCJKTokenPerChar,
		//  媒体单价（媒体计价口径；0=未配价——估算按 0 计、记账行标 price_unconfigured）
		OCRPricePerPageCNY:   c.OCRPricePerPage,
		ASRPricePerMinuteCNY: c.ASRPricePerMinute,
	}
}

// embedOptions 嵌入阶段参数（kb.embed.*）
func embedOptions(progress func(done, total int64)) embed.Options {
	c := kbcfg.Get().Embed
	return embed.Options{
		Batch:    c.Batch,
		MaxChars: c.MaxChars,
		Budget:   budgetLimits(),
		Progress: progress,
	}
}

// embedEnabled 嵌入档开关（关 = 只切不嵌；检索面自动降级词面腿）
func embedEnabled() bool { return kbcfg.Get().Embed.Enabled }

// （ftsConfig() 已删——tsv 切换为 DB 生成列（migrations/1_core.up.sql 的 match_text 派生列），
// 写侧不再消费 kb.fts.config；查询侧单点 = ingest.ResolveFTSConfig（收窄为 simple，其余响亮拒绝）。）

// ==================== 文档读取小工具 ====================

// loadDocument 读存活文档（软删 = 未找到；不存在回 (nil, nil)）
func (s *Service) loadDocument(ctx context.Context, documentID uint) (*model.KBDocument, error) {
	row, err := gorm.G[model.KBDocument](s.db).
		Select(documentDetailColumns).
		Where("id = ?", documentID).
		First(ctx)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("kb: 读文档失败: %w", err)
	}
	return &row, nil
}

// tableNameOf 表名单点（与模型 TableName() 同源；raw SQL 禁裸写表名）
func tableNameOf(db *gorm.DB, m any) string {
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(m); err != nil || stmt.Schema == nil || stmt.Schema.Table == "" {
		if t, ok := m.(interface{ TableName() string }); ok {
			return t.TableName()
		}
		return ""
	}
	return stmt.Schema.Table
}

// countRunes rune 计数（成本估算输入）
func countRunes(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}
