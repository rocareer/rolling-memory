// plugin: kb v1.0.0
// 非 LLM 成本记账（云 OCR/ASR/caption 独立驱动的按量计价——「非 ai 直连必须自建记账行，
// 禁无账开销」）。表 kb_cost_log 见 migrations/5_cost_log.up.sql（只增不改不删）。
//
// 口径：
//   - 经模型端口者（embedding/chat/VLM）的记账归各 Provider 实现，**不落本表**；两者并集即 KB 总成本；
//   - 记账与提取物**同事务**落库（SaveExtractWithCost）：提取成功但记账失败 ⇒ 整体回滚重试，
//     绝不出现「有开销而无账」；重切不重解（提取物复用路径）**不重复记账**；
//   - 单价快照进 unit_price（config kb.budget.ocr_price_per_page / asr_price_per_minute；
//     0 = 未配价 ⇒ unit_price=0 + detail.price_unconfigured=true，**禁虚构价格**）；
//   - 三源构造单点 = MediaCostEntries（提取读数 → 条目）；本文件即全部写面（RecordCost 为唯一 INSERT 出口）。
package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/ai-go-hub/ai-go-admin/plugins/kb/kbcfg"
	"gorm.io/gorm"
)

// 成本事件闭集（kb_cost_log.kind；代码闸，不加 DB CHECK——migrations/5_cost_log.up.sql 注释同口径）
const (
	CostKindOCR     = "ocr"     // 光学识别（页）
	CostKindASR     = "asr"     // 语音转写（分钟）
	CostKindCaption = "caption" // 图像描述（帧）
	CostKindFrame   = "frame"   // 关键帧处理（帧）
)

// 计价单位闭集（kb_cost_log.unit）
const (
	CostUnitPage   = "page"
	CostUnitMinute = "minute"
	CostUnitFrame  = "frame"
	CostUnitCall   = "call"
)

// CostLogRow kb_cost_log 行类型（migrations/5_cost_log.up.sql 列面对齐）
type CostLogRow struct {
	ID         uint       `gorm:"column:id;primarykey"`
	LibraryID  uint       `gorm:"column:library_id"`
	DocumentID uint       `gorm:"column:document_id"`
	Kind       string     `gorm:"column:kind"`
	Provider   string     `gorm:"column:provider"`
	Unit       string     `gorm:"column:unit"`
	Quantity   float64    `gorm:"column:quantity"`
	UnitPrice  float64    `gorm:"column:unit_price"`
	CostCNY    float64    `gorm:"column:cost_cny"`
	Detail     *string    `gorm:"column:detail"`
	CreatedAt  *time.Time `gorm:"column:created_at"`
}

// TableName 表名单点
func (CostLogRow) TableName() string { return "kb_cost_log" }

// CostEntry 记账条目（构造侧单点 = MediaCostEntries；本文件是唯一写面）
type CostEntry struct {
	LibraryID    uint
	DocumentID   uint
	Kind         string         // 闭集 CostKind*
	Provider     string         // 提供方读数（渠道/驱动名；可空）
	Unit         string         // 闭集 CostUnit*
	Quantity     float64        // 量（页/分钟/帧；实测值非估算）
	UnitPriceCNY float64        // 单价快照（元/单位；0=未配价）
	Detail       map[string]any // 明细（price_unconfigured/estimate 等；可 nil）
}

// Valid 闭集与值域校验（写侧单点）
func (e CostEntry) Valid() error {
	if e.DocumentID == 0 && e.LibraryID == 0 {
		return errors.New("kb: 记账条目缺归属（library_id/document_id 全 0）")
	}
	switch e.Kind {
	case CostKindOCR, CostKindASR, CostKindCaption, CostKindFrame:
	default:
		return fmt.Errorf("kb: 记账 kind 未登记: %q（闭集：%s）", e.Kind, costKindList())
	}
	switch e.Unit {
	case CostUnitPage, CostUnitMinute, CostUnitFrame, CostUnitCall:
	default:
		return fmt.Errorf("kb: 记账 unit 未登记: %q", e.Unit)
	}
	if e.Quantity < 0 || e.UnitPriceCNY < 0 {
		return fmt.Errorf("kb: 记账量/单价为负（q=%.3f p=%.6f）", e.Quantity, e.UnitPriceCNY)
	}
	return nil
}

// Amount 金额（= quantity × unit_price；四舍五入 6 位——numeric(14,6) 列宽）
func (e CostEntry) Amount() float64 { return math.Round(e.Quantity*e.UnitPriceCNY*1e6) / 1e6 }

// RecordCost 写一行记账（只增；单语句自身原子）。链内写入请走 SaveExtractWithCost（与提取物同事务）。
func RecordCost(ctx context.Context, db *gorm.DB, e CostEntry) error {
	if db == nil {
		return errors.New("kb: 记账缺 db")
	}
	return insertCostRow(ctx, db, e)
}

// insertCostRow 记账行插入单点（唯一 INSERT 语句面）
func insertCostRow(ctx context.Context, db *gorm.DB, e CostEntry) error {
	if err := e.Valid(); err != nil {
		return err
	}
	detail, err := marshalStats(e.Detail) // map → *string（同 stats 序列化路径；空 map = NULL）
	if err != nil {
		return fmt.Errorf("kb: 记账明细序列化失败: %w", err)
	}
	row := CostLogRow{
		LibraryID: e.LibraryID, DocumentID: e.DocumentID,
		Kind: e.Kind, Provider: truncateRunes(e.Provider, 64), Unit: e.Unit,
		Quantity: e.Quantity, UnitPrice: e.UnitPriceCNY, CostCNY: e.Amount(),
		Detail: detail,
	}
	if err := gorm.G[CostLogRow](db).Create(ctx, &row); err != nil {
		return fmt.Errorf("kb: 落记账行失败: %w", err)
	}
	return nil
}

// SaveExtractWithCost 落提取物（重切不重解）+ **新提取时**同事务落记账行（禁无账开销）。
//
// 语义分派：
//   - 已有提取物（uk 命中）⇒ 直接复用，**不记账**（重切不重解；成本只在真实发生的那次计入）；
//   - 新提取 ⇒ 单事务「提取物 + costs 全部记账行」原子落；并发窗口撞 uk ⇒ 事务回滚，
//     按「对方已落」处理（对方同事务已记账）——回读复用，不重复记账。
func SaveExtractWithCost(ctx context.Context, db *gorm.DB, ext *Extract, documentID uint, costs []CostEntry) (*ExtractRow, error) {
	if db == nil || ext == nil || documentID == 0 {
		return nil, errors.New("kb: 落提取物参数非法")
	}
	existing, err := LoadExtract(ctx, db, documentID, ext.Extractor, ext.Version)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}
	stats, err := marshalStats(ext.Stats)
	if err != nil {
		return nil, err
	}
	var row ExtractRow
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row = ExtractRow{
			DocumentID: documentID, Kind: ext.Kind, Extractor: ext.Extractor,
			Version: ext.Version, Text: ext.Text, Stats: stats,
		}
		if err := gorm.G[ExtractRow](tx).Create(ctx, &row); err != nil {
			return err
		}
		for _, c := range costs {
			if err := insertCostRow(ctx, tx, c); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if isUniqueViolation(err) {
			// 并发窗口：另一事务先落（其同事务已记账）⇒ 回读复用
			again, rerr := LoadExtract(ctx, db, documentID, ext.Extractor, ext.Version)
			if rerr != nil {
				return nil, rerr
			}
			if again == nil {
				return nil, fmt.Errorf("kb: 提取物并发冲突后回读为空（document=%d）", documentID)
			}
			return again, nil
		}
		return nil, fmt.Errorf("kb: 落提取物失败: %w", err)
	}
	return &row, nil
}

// MediaCostEntries 提取产物 → 记账条目（构造单点；无媒体读数 = 空——纯文本/本地提取零成本不记账）。
//
// 读数约定（media.go 写入侧）：
//   - kind=ocr：stats.ocr_pages（或 pages）——页单价 = kb.budget.ocr_price_per_page；
//   - kind=asr：stats.minutes——分钟单价 = kb.budget.asr_price_per_minute；
//   - stats.frames（视频关键帧）——caption 按帧记账（单价无 config 键 = 0 + price_unconfigured，
//     驱动若走模型端口（VLM）则其成本由该侧记账，此处不重复计价——帧行仅记量）。
func MediaCostEntries(ext *Extract, libraryID, documentID uint) []CostEntry {
	if ext == nil || len(ext.Stats) == 0 {
		return nil
	}
	prices := mediaPrices()
	provider, _ := ext.Stats["provider"].(string)
	extractorID := ext.ExtractorID()
	// 分账标记：驱动若经 ai 面计费（VLM/ai 渠道），须在提取读数写
	// stats["billed_via"]="ai"——本函数见该标记一律按「量行」记（单价强制 0 + detail 标明），
	// 避免同一笔开销同时落模型端口侧账（自动记账）与 kb_cost_log（本表）双计；两账并集
	// 仍是 KB 总成本。
	billedViaAI := false
	if v, ok := ext.Stats["billed_via"].(string); ok && strings.EqualFold(strings.TrimSpace(v), "ai") {
		billedViaAI = true
	}
	out := make([]CostEntry, 0, 3)
	item := func(kind, unit string, qty, price float64) {
		if qty <= 0 {
			return
		}
		detail := map[string]any{"extractor": extractorID}
		if provider != "" {
			detail["provider"] = provider
		}
		if billedViaAI {
			price = 0 // ai 面已自动记账：本表只记量（金额快照 0，明细标明口径）
			detail["billed_via"] = "ai"
		}
		if price <= 0 {
			detail["price_unconfigured"] = true
		}
		out = append(out, CostEntry{
			LibraryID: libraryID, DocumentID: documentID,
			Kind: kind, Provider: provider, Unit: unit,
			Quantity: qty, UnitPriceCNY: price, Detail: detail,
		})
	}
	switch ext.Kind {
	case ExtractKindOCR:
		item(CostKindOCR, CostUnitPage, StatFloat(ext.Stats, "ocr_pages", "pages"), prices.OCRPricePerPageCNY)
	case ExtractKindASR:
		item(CostKindASR, CostUnitMinute, StatFloat(ext.Stats, "minutes"), prices.ASRPricePerMinuteCNY)
		item(CostKindCaption, CostUnitFrame, StatFloat(ext.Stats, "frames"), 0)
	}
	return out
}

// StatFloat 读数取值（导出单点：int/float64/int64/json.Number 多形态容忍——内存直传 vs DB 反序列化
// 的数字形态差异；调用方：本包记账构造与 kb 包 job 侧来源读数）。
func StatFloat(stats map[string]any, keys ...string) float64 {
	for _, k := range keys {
		switch v := stats[k].(type) {
		case float64:
			return v
		case int:
			return float64(v)
		case int64:
			return float64(v)
		case json.Number:
			if f, err := v.Float64(); err == nil {
				return f
			}
		}
	}
	return 0
}

// mediaPrices 媒体单价快照（插件配置；未配价 = 全 0——不虚构价格）
func mediaPrices() BudgetLimits {
	b := kbcfg.Get().Budget
	return BudgetLimits{
		OCRPricePerPageCNY:   b.OCRPricePerPage,
		ASRPricePerMinuteCNY: b.ASRPricePerMinute,
	}
}

// CostCountForDocument 文档已记账行数（巡检/冒烟读数；≤1 查询走 idx(document_id,id)）
func CostCountForDocument(ctx context.Context, db *gorm.DB, documentID uint) (int64, error) {
	if db == nil || documentID == 0 {
		return 0, errors.New("kb: 记账读数参数非法")
	}
	n, err := gorm.G[CostLogRow](db).Where("document_id = ?", documentID).Count(ctx, "id")
	if err != nil {
		return 0, fmt.Errorf("kb: 读文档记账行失败: %w", err)
	}
	return n, nil
}

// sumCostDoc 只为便于测试断言（同文档金额合计；不用于请求路径）
func sumCostDoc(ctx context.Context, db *gorm.DB, documentID uint) (float64, error) {
	rows, err := gorm.G[CostLogRow](db).Select("cost_cny").Where("document_id = ?", documentID).Find(ctx)
	if err != nil {
		return 0, err
	}
	total := 0.0
	for _, r := range rows {
		total += r.CostCNY
	}
	return math.Round(total*1e6) / 1e6, nil
}

// costKindList 闭集名清单（报错/展示用；保持与 Valid 分支同步）
func costKindList() string {
	return strings.Join([]string{CostKindOCR, CostKindASR, CostKindCaption, CostKindFrame}, " / ")
}
