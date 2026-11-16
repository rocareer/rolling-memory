// plugin: kb v1.0.0
package chunk

import (
	"context"
	"strings"
	"testing"
)

func TestTableBlocksAndMeta(t *testing.T) {
	text := "name,qty\n启甲,1\n启乙,2\n启丙,3"
	cfg := testConfig(t, `{"rows_per_chunk":2,"header_repeat":true,"min_rows":1,"hard_cap":1024}`)
	chunks := runChunk(t, "table", text, cfg)
	if len(chunks) != 2 {
		t.Fatalf("3 数据行 / 每块 2 行应切 2 块，实际 %d", len(chunks))
	}
	first, second := chunks[0], chunks[1]
	if first.TableMeta == nil || first.TableMeta.RowFrom != 1 || first.TableMeta.RowTo != 2 {
		t.Fatalf("首块 table_meta 行区间应为 1-2，实际 %+v", first.TableMeta)
	}
	if len(first.TableMeta.Header) != 2 || first.TableMeta.Header[0] != "name" {
		t.Fatalf("表头应落 table_meta，实际 %v", first.TableMeta.Header)
	}
	if !strings.HasPrefix(first.Text, "name,qty\n") {
		t.Fatalf("首块正文应含表头行（逐字），实际 %q", first.Text)
	}
	if strings.Contains(second.Text, "name,qty") {
		t.Fatalf("非首块正文不得掺重复表头（锚逐字纪律），实际 %q", second.Text)
	}
	if second.TableMeta.RowFrom != 3 || second.TableMeta.RowTo != 3 {
		t.Fatalf("次块行区间应为 3-3，实际 %+v", second.TableMeta)
	}
	// 表头重复进派生文本（ChunkTextOf 单点）
	if got := ChunkTextOf(second, cfg); !strings.HasPrefix(got, "name,qty\n") {
		t.Fatalf("header_repeat=true 派生文本应前置表头，实际 %q", got)
	}
	off := testConfig(t, `{"header_repeat":false}`)
	if got := ChunkTextOf(second, off); strings.HasPrefix(got, "name,qty") {
		t.Fatalf("header_repeat=false 不应前置表头，实际 %q", got)
	}
}

func TestTableTailRowMerge(t *testing.T) {
	text := "h\n1\n2\n3\n4\n5"
	cfg := testConfig(t, `{"rows_per_chunk":2,"min_rows":2,"header_repeat":false}`)
	chunks := runChunk(t, "table", text, cfg)
	if len(chunks) != 2 {
		t.Fatalf("尾块 1 行 < min_rows 2 应并入前块 ⇒ 2 块，实际 %d", len(chunks))
	}
	last := chunks[len(chunks)-1]
	if last.TableMeta.RowFrom != 3 || last.TableMeta.RowTo != 5 {
		t.Fatalf("并入后末块行区间应为 3-5，实际 %+v", last.TableMeta)
	}
}

func TestTableQuotedNewlineAndHeaderOnly(t *testing.T) {
	text := "a,b\n\"x\ny\",2\nc,3"
	cfg := testConfig(t, `{"rows_per_chunk":10,"min_rows":1}`)
	chunks := runChunk(t, "table", text, cfg)
	if len(chunks) != 1 {
		t.Fatalf("引号内换行不拆记录 ⇒ 1 块，实际 %d", len(chunks))
	}
	if !strings.Contains(chunks[0].Text, "\"x\ny\",2") {
		t.Fatalf("引号内换行应逐字保留，实际 %q", chunks[0].Text)
	}
	if chunks[0].TableMeta.RowFrom != 1 || chunks[0].TableMeta.RowTo != 2 {
		t.Fatalf("数据行应为 1-2，实际 %+v", chunks[0].TableMeta)
	}

	onlyHeader := runChunk(t, "table", "a,b\n", testConfig(t, `{}`))
	if len(onlyHeader) != 1 || onlyHeader[0].TableMeta.RowTo != 0 {
		t.Fatalf("仅表头应产出 1 块且无数据行，实际 %+v", onlyHeader)
	}
}

func TestTableZeroTextLoud(t *testing.T) {
	ch, err := Get("table")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := ch.Chunk(context.Background(), Input{Text: "  \n \n", Config: Default()}); err == nil {
		t.Fatal("零文本必须响亮报错")
	}
}
