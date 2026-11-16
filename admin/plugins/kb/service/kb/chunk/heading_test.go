// plugin: kb v1.0.0
package chunk

import (
	"strings"
	"testing"
)

func TestHeadingSectionsAndPath(t *testing.T) {
	text := "# 一级标题\n正文一\n\n## 二级标题\n正文二"
	cfg := testConfig(t, `{"target":512,"heading_prefix":true}`)
	chunks := runChunk(t, "heading", text, cfg)
	if len(chunks) != 2 {
		t.Fatalf("两级标题应切 2 块，实际 %d", len(chunks))
	}
	if got := chunks[0].HeadingPath; len(got) != 1 || got[0] != "一级标题" {
		t.Fatalf("首块标题路径应为 [一级标题]，实际 %v", got)
	}
	if got := chunks[1].HeadingPath; len(got) != 2 || got[0] != "一级标题" || got[1] != "二级标题" {
		t.Fatalf("次块标题路径应为 [一级标题 二级标题]，实际 %v", got)
	}
	// 首块区间应含标题行（逐字）
	if !strings.HasPrefix(chunks[0].Text, "# 一级标题") {
		t.Fatalf("首块应含标题行，实际 %q", chunks[0].Text)
	}
	// heading_prefix=true ⇒ 派生文本前置标题路径（ChunkTextOf 单点）
	if got := ChunkTextOf(chunks[1], cfg); !strings.HasPrefix(got, "一级标题 > 二级标题\n") {
		t.Fatalf("派生文本应前置标题路径，实际 %q", got)
	}
	off := testConfig(t, `{"heading_prefix":false}`)
	if got := ChunkTextOf(chunks[1], off); strings.HasPrefix(got, "一级标题") {
		t.Fatalf("heading_prefix=false 不应前置，实际 %q", got)
	}
}

func TestHeadingPreambleAndLevels(t *testing.T) {
	text := "序言段落。\n\n# 标题一\n正文甲\n\n### 深标题\n正文乙"
	cfg := testConfig(t, `{"levels":[1],"target":512}`)
	chunks := runChunk(t, "heading", text, cfg)
	if len(chunks) != 2 {
		t.Fatalf("levels=[1] ⇒ 只按 1 级切（### 归入正文）：应 2 块，实际 %d", len(chunks))
	}
	if len(chunks[0].HeadingPath) != 0 {
		t.Fatalf("序言块路径应为空，实际 %v", chunks[0].HeadingPath)
	}
	if len(chunks[1].HeadingPath) != 1 || chunks[1].HeadingPath[0] != "标题一" {
		t.Fatalf("第 2 块路径应为 [标题一]，实际 %v", chunks[1].HeadingPath)
	}
	if !strings.Contains(chunks[1].Text, "### 深标题") {
		t.Fatalf("非命中层级标题应作正文保留，实际 %q", chunks[1].Text)
	}
}

func TestHeadingSetext(t *testing.T) {
	text := "Setext 标题\n==========\n正文一\n\n子标题\n------\n正文二"
	cfg := testConfig(t, `{"levels":[1,2],"target":512}`)
	chunks := runChunk(t, "heading", text, cfg)
	if len(chunks) != 2 {
		t.Fatalf("setext 一二级应切 2 块，实际 %d", len(chunks))
	}
	if chunks[0].HeadingPath[0] != "Setext 标题" {
		t.Fatalf("首块路径错误: %v", chunks[0].HeadingPath)
	}
	if len(chunks[1].HeadingPath) != 2 || chunks[1].HeadingPath[1] != "子标题" {
		t.Fatalf("次块路径应为 [Setext 标题 子标题]，实际 %v", chunks[1].HeadingPath)
	}
}

func TestHeadingLongSectionSplitsWithPath(t *testing.T) {
	body := strings.Repeat("字", 300)
	text := "# 甲\n" + body[:150] + "\n\n" + body[150:] + "\n\n# 乙\n短正文"
	cfg := testConfig(t, `{"target":128,"hard_cap":256,"overlap":16,"tail_merge":0}`)
	chunks := runChunk(t, "heading", text, cfg)
	if len(chunks) < 3 {
		t.Fatalf("长节应切多块，实际 %d", len(chunks))
	}
	for i := 0; i < len(chunks)-1; i++ {
		if len(chunks[i].HeadingPath) != 1 || chunks[i].HeadingPath[0] != "甲" {
			t.Fatalf("甲节第 %d 块路径错误: %v", i+1, chunks[i].HeadingPath)
		}
	}
	last := chunks[len(chunks)-1]
	if len(last.HeadingPath) != 1 || last.HeadingPath[0] != "乙" {
		t.Fatalf("末块应属乙节，实际 %v", last.HeadingPath)
	}
}
