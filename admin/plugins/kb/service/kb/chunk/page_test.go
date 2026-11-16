// plugin: kb v1.0.0
package chunk

import (
	"context"
	"strings"
	"testing"
)

// page 策略：\f 分页、page_as_chunk、cross_page_merge=false、页号落 PageFrom/PageTo。

func TestPagePerFormFeedPage(t *testing.T) {
	text := "第一页内容\n\n若干行\n" + "\f" + "第二页内容\n"
	cfg := testConfig(t, `{"target":512,"overlap":64,"hard_cap":1024}`)
	chunks := runChunk(t, "page", text, cfg) // runChunk 已做锚逐字自检
	if len(chunks) != 2 {
		t.Fatalf("两页应切 2 块，实际 %d", len(chunks))
	}
	if chunks[0].PageFrom != 1 || chunks[0].PageTo != 1 {
		t.Fatalf("首块页号应 1/1，实际 %d/%d", chunks[0].PageFrom, chunks[0].PageTo)
	}
	if chunks[1].PageFrom != 2 || chunks[1].PageTo != 2 {
		t.Fatalf("次块页号应 2/2，实际 %d/%d", chunks[1].PageFrom, chunks[1].PageTo)
	}
}

func TestPageBlankPageKeepsNumbering(t *testing.T) {
	text := "A\f\fB" // 页1=A、页2=空、页3=B
	cfg := testConfig(t, `{"target":512,"overlap":64,"hard_cap":1024}`)
	chunks := runChunk(t, "page", text, cfg)
	if len(chunks) != 2 {
		t.Fatalf("空页跳过应余 2 块，实际 %d", len(chunks))
	}
	if chunks[0].PageFrom != 1 || chunks[1].PageFrom != 3 {
		t.Fatalf("空页不占块但页号不减：期望 1/3，实际 %d/%d", chunks[0].PageFrom, chunks[1].PageFrom)
	}
}

func TestPageSinglePageNoFormFeed(t *testing.T) {
	chunks := runChunk(t, "page", "单页文本，无分页符。", testConfig(t, `{"target":512,"overlap":64,"hard_cap":1024}`))
	if len(chunks) != 1 || chunks[0].PageFrom != 1 {
		t.Fatalf("无 \\f 应单页单块，实际 %d 块（页号 %d）", len(chunks), chunks[0].PageFrom)
	}
}

func TestPageOverCapSplitsWithinPage(t *testing.T) {
	text := strings.Repeat("字", 1000) // 单页超帽（hard_cap=256、overlap=16 ⇒ 步长 240）
	cfg := testConfig(t, `{"target":128,"overlap":16,"hard_cap":256,"tail_merge":0}`)
	chunks := runChunk(t, "page", text, cfg)
	if len(chunks) != 5 {
		t.Fatalf("页内超帽应按 240 步长切 5 块，实际 %d", len(chunks))
	}
	for i, c := range chunks {
		if c.PageFrom != 1 || c.PageTo != 1 {
			t.Fatalf("页内切分块应共享页号 1，第 %d 块 %d/%d", i+1, c.PageFrom, c.PageTo)
		}
	}
}

func TestPageZeroTextLoud(t *testing.T) {
	ch, err := Get("page")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := ch.Chunk(context.Background(), Input{Text: " \n\f\n ", Config: Default()}); err == nil {
		t.Fatal("零文本必须响亮报错（禁静默入库）")
	}
}
