// plugin: kb v1.0.0
package ingest

import (
	"math"
	"strings"
	"testing"
)

// kb_cost_log 记账：条目校验/金额/媒体量构造（纯函数面；DB 写入面在 kb 包门禁用例）。

func TestCostEntryValidAndAmount(t *testing.T) {
	ok := CostEntry{LibraryID: 1, DocumentID: 2, Kind: CostKindOCR, Unit: CostUnitPage, Quantity: 3, UnitPriceCNY: 0.01}
	if err := ok.Valid(); err != nil {
		t.Fatalf("合法条目被拒: %v", err)
	}
	if got := ok.Amount(); math.Abs(got-0.03) > 1e-9 {
		t.Fatalf("金额应 0.03，实际 %v", got)
	}
	// 舍入到 6 位（numeric(14,6) 列宽）
	r := CostEntry{LibraryID: 1, DocumentID: 2, Kind: CostKindASR, Unit: CostUnitMinute, Quantity: 1.23456789, UnitPriceCNY: 1}
	if got := r.Amount(); math.Abs(got-1.234568) > 1e-9 {
		t.Fatalf("金额应舍入 1.234568，实际 %v", got)
	}

	bad := []CostEntry{
		{Kind: CostKindOCR, Unit: CostUnitPage}, // 缺归属
		{LibraryID: 1, Kind: "unknown", Unit: CostUnitPage},
		{LibraryID: 1, Kind: CostKindOCR, Unit: "page2"},
		{LibraryID: 1, Kind: CostKindOCR, Unit: CostUnitPage, Quantity: -1},
		{LibraryID: 1, Kind: CostKindOCR, Unit: CostUnitPage, UnitPriceCNY: -0.1},
	}
	for i, c := range bad {
		if err := c.Valid(); err == nil {
			t.Fatalf("非法条目 %d 应被拒: %+v", i, c)
		}
	}
}

func TestMediaCostEntriesConstruction(t *testing.T) {
	// OCR 产物：ocr_pages → page 计价（单价未配 ⇒ price_unconfigured）
	ocrExt := &Extract{
		Kind: ExtractKindOCR, Extractor: extractorImage, Version: ExtractorVersion,
		Stats: map[string]any{"ocr_pages": 3, "provider": "fake-ocr"},
	}
	entries := MediaCostEntries(ocrExt, 7, 42)
	if len(entries) != 1 {
		t.Fatalf("ocr 应 1 条记账，实际 %d", len(entries))
	}
	e := entries[0]
	if e.Kind != CostKindOCR || e.Unit != CostUnitPage || e.Quantity != 3 || e.LibraryID != 7 || e.DocumentID != 42 {
		t.Fatalf("ocr 条目不符: %+v", e)
	}
	if v, _ := e.Detail["price_unconfigured"].(bool); !v {
		t.Fatalf("未配价应标注: %+v", e.Detail)
	}
	if e.Detail["extractor"] != "kb-image@v1" {
		t.Fatalf("extractor 应入 detail: %+v", e.Detail)
	}

	// ASR + 视频帧：两条（minute + frame）
	asrExt := &Extract{
		Kind: ExtractKindASR, Extractor: extractorVideo, Version: ExtractorVersion,
		Stats: map[string]any{"minutes": 1.5, "frames": 4, "provider": "fake"},
	}
	entries = MediaCostEntries(asrExt, 7, 42)
	if len(entries) != 2 {
		t.Fatalf("asr+帧应 2 条记账，实际 %d", len(entries))
	}
	if entries[0].Kind != CostKindASR || entries[0].Unit != CostUnitMinute || entries[0].Quantity != 1.5 {
		t.Fatalf("asr 条目不符: %+v", entries[0])
	}
	if entries[1].Kind != CostKindCaption || entries[1].Unit != CostUnitFrame || entries[1].Quantity != 4 {
		t.Fatalf("帧条目不符: %+v", entries[1])
	}

	// 纯文本产物：零条目（本地提取零成本不记账）
	textExt := &Extract{Kind: ExtractKindText, Extractor: extractorPDF, Version: ExtractorVersion, Stats: map[string]any{"pages": 3}}
	if got := MediaCostEntries(textExt, 7, 42); len(got) != 0 {
		t.Fatalf("文本产物不应记账: %+v", got)
	}
	// 无读数：零条目
	noStats := &Extract{Kind: ExtractKindOCR, Extractor: extractorImage, Version: ExtractorVersion}
	if got := MediaCostEntries(noStats, 7, 42); len(got) != 0 {
		t.Fatalf("无媒体读数不应记账: %+v", got)
	}

	// 分账标记：stats["billed_via"]="ai"（驱动经模型端口计费，成本归其调用日志）
	// ⇒ 本表只记量：单价强制 0 + detail 标明口径（即便单价已配也不双计）
	aiBilled := &Extract{
		Kind: ExtractKindOCR, Extractor: extractorImage, Version: ExtractorVersion,
		Stats: map[string]any{"ocr_pages": 3, "provider": "fake-ocr", "billed_via": "ai"},
	}
	got := MediaCostEntries(aiBilled, 7, 42)
	if len(got) != 1 || got[0].UnitPriceCNY != 0 {
		t.Fatalf("ai 面计费应只记量（单价 0）: %+v", got)
	}
	if v, _ := got[0].Detail["billed_via"].(string); v != "ai" {
		t.Fatalf("billed_via 应入 detail: %+v", got[0].Detail)
	}
}

func TestMediaCostEntriesPriceFromConfigPolicy(t *testing.T) {
	// config 未初始化 ⇒ 单价 0（未配价不虚构）；有价时 amount 生效（此处以注入限制不可行——
	// 验证 StatFloat 读数形态容忍即可：int/float64 两形态同值）
	ext := &Extract{
		Kind: ExtractKindASR, Extractor: extractorAudio, Version: ExtractorVersion,
		Stats: map[string]any{"minutes": float64(2)},
	}
	entries := MediaCostEntries(ext, 1, 2)
	if len(entries) != 1 || entries[0].Quantity != 2 {
		t.Fatalf("float64 读数不符: %+v", entries)
	}
	ext.Stats["minutes"] = 3 // int 形态
	entries = MediaCostEntries(ext, 1, 2)
	if len(entries) != 1 || entries[0].Quantity != 3 {
		t.Fatalf("int 读数不符: %+v", entries)
	}
}

func TestStatFloatForms(t *testing.T) {
	stats := map[string]any{"a": 2, "b": int64(3), "c": 4.5}
	if StatFloat(stats, "a") != 2 || StatFloat(stats, "b") != 3 || StatFloat(stats, "c") != 4.5 {
		t.Fatalf("读数形态容忍不符: %+v", stats)
	}
	if StatFloat(stats, "missing", "a") != 2 {
		t.Fatal("多键回退应命中 a")
	}
	if StatFloat(nil, "a") != 0 {
		t.Fatal("nil 应 0")
	}
	// 字符串读数不猜（响亮口径：不识别 = 0）
	if StatFloat(map[string]any{"x": "12"}, "x") != 0 {
		t.Fatal("字符串读数不应隐式转换")
	}
}

func TestCostKindListNonEmpty(t *testing.T) {
	if s := costKindList(); !strings.Contains(s, CostKindOCR) || !strings.Contains(s, CostKindCaption) {
		t.Fatalf("闭集清单缺项: %s", s)
	}
}
