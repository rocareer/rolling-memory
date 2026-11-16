// plugin: kb v1.0.0
package chunk

import (
	"context"
	"strings"
	"testing"
)

func TestCodeFileGranularityAndHunks(t *testing.T) {
	small := "package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n"
	cfg := testConfig(t, `{"max":512,"overlap":0}`)
	chunks := runChunk(t, "code", small, cfg)
	if len(chunks) != 1 {
		t.Fatalf("小文件应单块（文件粒度），实际 %d", len(chunks))
	}
	if chunks[0].CharStart != 0 || chunks[0].CharEnd != len([]rune(small)) {
		t.Fatalf("文件粒度块应覆盖全文，实际 [%d,%d)", chunks[0].CharStart, chunks[0].CharEnd)
	}

	// 大文件：hunk 边界 = 深度 0 ∧ 缩进 0 ∧ 前行空行
	body := strings.Repeat("\tprintln(\"填充行填充行\")\n", 20)
	big := "package main\n\nimport \"fmt\"\n\nfunc alpha() {\n" + body + "}\n\nfunc beta() {\n" + body + "}\n"
	chunks = runChunk(t, "code", big, cfg)
	if len(chunks) != 4 {
		t.Fatalf("应识别 4 个 hunk（package/import/alpha/beta），实际 %d 块\n%+v", len(chunks), chunks)
	}
	if !strings.HasPrefix(chunks[0].Text, "package main") {
		t.Fatalf("首块应从首行起，实际 %q", chunks[0].Text)
	}
	if !strings.HasPrefix(chunks[2].Text, "func alpha() {") {
		t.Fatalf("第 3 块应为 alpha hunk，实际 %q", chunks[2].Text)
	}
	if strings.Contains(chunks[2].Text, "func beta") {
		t.Fatalf("hunk 不得跨函数合并，实际 %q", chunks[2].Text)
	}
}

func TestCodeHunkOverCapSplits(t *testing.T) {
	big := "package main\n\n" + strings.Repeat("// 注释行填充内容\n", 40) + "\nfunc main() {}\n"
	cfg := testConfig(t, `{"max":128,"overlap":0}`)
	chunks := runChunk(t, "code", big, cfg)
	if len(chunks) < 2 {
		t.Fatalf("超 max 的 hunk 应再切，实际 %d 块", len(chunks))
	}
	for i, c := range chunks {
		if c.CharEnd-c.CharStart > 128 {
			t.Fatalf("第 %d 块超 max=128：%d rune", i+1, c.CharEnd-c.CharStart)
		}
	}
}

func TestCodeFallbackWithoutBoundaries(t *testing.T) {
	// 无空行、无缩进、无花括号 ⇒ 无 hunk 边界 ⇒ 回退空行分段打包（单段超 max 则按 max 硬切）
	text := strings.Repeat("字", 300)
	cfg := testConfig(t, `{"max":128,"overlap":0}`)
	chunks := runChunk(t, "code", text, cfg)
	if len(chunks) != 3 {
		t.Fatalf("300 rune 单段按 max=128 应切 3 块，实际 %d", len(chunks))
	}
	for i, c := range chunks {
		if c.CharEnd-c.CharStart > 128 {
			t.Fatalf("第 %d 块超 max=128：%d rune", i+1, c.CharEnd-c.CharStart)
		}
	}
}

func TestCodeZeroTextLoud(t *testing.T) {
	ch, err := Get("code")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := ch.Chunk(context.Background(), Input{Text: "\n\n", Config: Default()}); err == nil {
		t.Fatal("零文本必须响亮报错")
	}
}
