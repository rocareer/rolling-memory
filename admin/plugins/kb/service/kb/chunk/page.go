// plugin: kb v1.0.0
// page 策略：扫描件/图片（表：page_as_chunk、cross_page_merge=false、块文本=OCR/VLM 输出）。
//
// 输入契约：提取文本以 **\f（form feed，U+000C）分页**——第 n 段 = 第 n 页（OCR 提取器按此约定
// 逐页拼接；单图无 \f = 单页）。空页跳过、页号不减（页号 = \f 段落序，1 起）。
//
// 口径：
//   - page_as_chunk：页 = 块的基本粒度；cross_page_merge=false：**页间不合并**（块不跨 \f）；
//   - 页内沿用共享段落打包语义（splitParagraphsRange + packUnits）；页文本超硬帽时按硬帽在页内
//     切分——hard_cap 是全局 token 成本上限，优先级高于「页=一块」的粒度偏好；
//   - 块 Text = 提取文本的页内区间**逐字**切片（verbatim；锚与 VerifyVerbatim 与其余策略同构）；
//   - 产出行：PageFrom=PageTo=页号（kb_chunk.page_from/page_to 落列）。
package chunk

import (
	"context"
	"errors"
)

// pageChunker page 策略
type pageChunker struct{}

// Name 策略名（注册表键）
func (pageChunker) Name() string { return "page" }

// Version 契约版本（口径升级 ⇒ 升版本 + 重跑派生投影）
func (pageChunker) Version() string { return "v1" }

// Chunk 按页切块（零文本/零页 ⇒ 响亮报错）
func (pageChunker) Chunk(_ context.Context, in Input) ([]Chunk, error) {
	rs := []rune(in.Text)
	if isBlankRunes(rs) {
		return nil, errors.New("kb page 切片零文本（空/全空白；零文本禁静默入库）")
	}
	pages := splitFormFeedPages(rs)
	var out []Chunk
	for i, pg := range pages {
		if _, ok := trimSpan(rs, pg); !ok {
			continue // 空页跳过（页号不减）
		}
		pageNo := i + 1
		units := splitParagraphsRange(rs, pg.start, pg.end)
		chunks := packUnits(rs, units, in.Config)
		for j := range chunks {
			chunks[j].PageFrom = pageNo
			chunks[j].PageTo = pageNo
		}
		out = append(out, chunks...)
	}
	if len(out) == 0 {
		return nil, errors.New("kb page 切片零产出（无含文本的页；零文本禁静默入库）")
	}
	return out, nil
}

// splitFormFeedPages 按 \f 切页（返回原文本 rune 区间的页段；含页首尾空白，由消费方 trimSpan）
func splitFormFeedPages(rs []rune) []unit {
	out := make([]unit, 0, 8)
	start := 0
	for i, r := range rs {
		if r == '\f' {
			out = append(out, unit{start, i})
			start = i + 1
		}
	}
	out = append(out, unit{start, len(rs)})
	return out
}
