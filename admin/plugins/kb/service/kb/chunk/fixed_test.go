// plugin: kb v1.0.0
package chunk

import (
	"context"
	"strings"
	"testing"
)

// runChunk 跑一个策略并做锚逐字自检（门禁口径：char 区间 + text_hash 复算 + 逐字）
func runChunk(t *testing.T, name string, text string, cfg Config) []Chunk {
	t.Helper()
	ch, err := Get(name)
	if err != nil {
		t.Fatalf("Get(%s): %v", name, err)
	}
	chunks, err := ch.Chunk(context.Background(), Input{Text: text, Locator: "kb://doc/1", Config: cfg})
	if err != nil {
		t.Fatalf("%s.Chunk: %v", name, err)
	}
	kept, err := Finalize("kb://doc/1", chunks)
	if err != nil {
		t.Fatalf("%s.Finalize: %v", name, err)
	}
	if err := VerifyVerbatim(text, kept); err != nil {
		t.Fatalf("%s 锚逐字自检: %v", name, err)
	}
	return kept
}

func TestFixedPacksParagraphsToTarget(t *testing.T) {
	p := func(ch string, n int) string { return strings.Repeat(ch, n) }
	text := p("甲", 100) + "\n\n" + p("乙", 100) + "\n\n" + p("丙", 100)
	cfg := testConfig(t, `{"target":128,"overlap":16,"hard_cap":256,"tail_merge":0}`)
	chunks := runChunk(t, "fixed", text, cfg)
	if len(chunks) != 3 {
		t.Fatalf("三段各 100 rune（> target/2、合并超 target）应切 3 块，实际 %d", len(chunks))
	}
	for i, c := range chunks {
		if c.CharEnd-c.CharStart != 100 {
			t.Fatalf("第 %d 块应 100 rune，实际 %d", i+1, c.CharEnd-c.CharStart)
		}
	}
}

func TestFixedMergesSmallTail(t *testing.T) {
	text := strings.Repeat("甲", 480) + "\n\n" + strings.Repeat("乙", 60)
	cfg := testConfig(t, `{"target":512,"overlap":64,"hard_cap":1024,"tail_merge":128}`)
	chunks := runChunk(t, "fixed", text, cfg)
	if len(chunks) != 1 {
		t.Fatalf("尾块 60 < tail_merge 128 应并入前块，实际 %d 块", len(chunks))
	}
	if got := chunks[0].CharEnd - chunks[0].CharStart; got != 542 {
		t.Fatalf("并入后应覆盖 480+2+60=542 rune，实际 %d", got)
	}
	if chunks[0].CharStart != 0 || chunks[0].CharEnd != 542 {
		t.Fatalf("并入后区间应为 [0,542)，实际 [%d,%d)", chunks[0].CharStart, chunks[0].CharEnd)
	}
}

func TestFixedOverCapSplitUsesOverlap(t *testing.T) {
	text := strings.Repeat("字", 1000)
	cfg := testConfig(t, `{"target":128,"overlap":16,"hard_cap":256,"tail_merge":0}`)
	chunks := runChunk(t, "fixed", text, cfg)
	// hard_cap=256、overlap=16 ⇒ 步长 240：起点 0/240/480/720/960 ⇒ 5 块
	if len(chunks) != 5 {
		t.Fatalf("超帽单段应按 240 步长切 5 块，实际 %d", len(chunks))
	}
	if chunks[4].CharStart != 960 || chunks[4].CharEnd != 1000 {
		t.Fatalf("末块区间应为 [960,1000)，实际 [%d,%d)", chunks[4].CharStart, chunks[4].CharEnd)
	}
	// overlap 只在超帽切分生效（相邻块重叠 16 rune）
	if chunks[0].CharEnd-chunks[1].CharStart != 16 {
		t.Fatalf("超帽切分相邻块应重叠 16 rune，实际 %d", chunks[0].CharEnd-chunks[1].CharStart)
	}
}

func TestFixedZeroTextLoud(t *testing.T) {
	ch, err := Get("fixed")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := ch.Chunk(context.Background(), Input{Text: "   \n\t\n", Config: Default()}); err == nil {
		t.Fatal("零文本必须响亮报错（禁静默入库）")
	}
}
