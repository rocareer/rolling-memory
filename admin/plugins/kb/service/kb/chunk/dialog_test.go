// plugin: kb v1.0.0
package chunk

import (
	"context"
	"strings"
	"testing"
)

// dialog 策略：轮=单元、多轮合并、禁跨会话合并、speaker_prefix 原生保留、秒级时间戳。

func TestDialogMergesTurnsWithinSession(t *testing.T) {
	text := "[00:01] 甲: 你好\n[00:05] 乙: 你好，请问有什么可以帮您？\n"
	cfg := testConfig(t, `{"target":512,"overlap":64,"hard_cap":1024}`)
	chunks := runChunk(t, "dialog", text, cfg) // runChunk 已做锚逐字自检（speaker 前缀为 verbatim 原文一部分）
	if len(chunks) != 1 {
		t.Fatalf("同会话两轮应合并为 1 块，实际 %d", len(chunks))
	}
	if chunks[0].TimeFrom != 1 || chunks[0].TimeTo != 5 {
		t.Fatalf("时间戳应 1→5 秒，实际 %d→%d", chunks[0].TimeFrom, chunks[0].TimeTo)
	}
	if !strings.Contains(chunks[0].Text, "[00:01] 甲:") {
		t.Fatalf("speaker 前缀应保留于块文本: %q", chunks[0].Text)
	}
}

func TestDialogSessionBoundaryStopsMerge(t *testing.T) {
	text := "[00:01] 甲: A\n\n[00:10] 乙: B\n" // 空行 = 会话边界
	cfg := testConfig(t, `{"target":512,"overlap":64,"hard_cap":1024}`)
	chunks := runChunk(t, "dialog", text, cfg)
	if len(chunks) != 2 {
		t.Fatalf("禁跨会话合并：两张会话应 2 块，实际 %d", len(chunks))
	}
}

func TestDialogNoTurnLoudFail(t *testing.T) {
	ch, err := Get("dialog")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := ch.Chunk(context.Background(), Input{Text: "普通文本\n没有轮首行结构。", Config: Default()}); err == nil {
		t.Fatal("无合法轮首行必须响亮报错（禁把非会话文本按会话切）")
	}
	if _, err := ch.Chunk(context.Background(), Input{Text: "   \n\t", Config: Default()}); err == nil {
		t.Fatal("零文本必须响亮报错")
	}
}

func TestDialogLongTurnSplitSharesTimestamps(t *testing.T) {
	// 单轮 600 rune（无换行）：hard_cap=256、overlap=16 ⇒ 步长 240 → 3 块（0/240/480）
	text := "[00:07] 甲: " + strings.Repeat("字", 600) + "\n"
	cfg := testConfig(t, `{"target":128,"overlap":16,"hard_cap":256,"tail_merge":0}`)
	chunks := runChunk(t, "dialog", text, cfg)
	if len(chunks) != 3 {
		t.Fatalf("单轮超帽应轮内切 3 块，实际 %d", len(chunks))
	}
	for i, c := range chunks {
		if c.TimeFrom != 7 || c.TimeTo != 7 {
			t.Fatalf("轮内切分块应共享时间戳 7s，第 %d 块 %d→%d", i+1, c.TimeFrom, c.TimeTo)
		}
	}
}

func TestDialogTimestampForms(t *testing.T) {
	cases := []struct {
		line string
		want int
	}{
		{"[00:12] 甲: x", 12},     // MM:SS
		{"[1:02:03] 甲: x", 3723}, // HH:MM:SS
		{"[45] 甲: x", 45},        // SS
		{"[00:12.500] 甲: x", 12}, // 小数截整
	}
	for _, c := range cases {
		sec, speaker, ok := parseDialogTurn(c.line)
		if !ok || sec != c.want || speaker == "" {
			t.Fatalf("解析 %q 期望 %d 秒，实际 %d（speaker=%q ok=%v）", c.line, c.want, sec, speaker, ok)
		}
	}
	if _, _, ok := parseDialogTurn("[abc] 甲: x"); ok {
		t.Fatal("非数字时间戳不应解析为轮首")
	}
	if _, _, ok := parseDialogTurn("[00:12] 无冒号结构"); ok {
		t.Fatal("缺 speaker 冒号不应解析为轮首")
	}
}

func TestDialogContinuationLinesFoldIntoCurrentTurn(t *testing.T) {
	text := "[00:01] 甲: 第一行\n续行文本（无前缀）\n[00:09] 乙: 回复\n"
	cfg := testConfig(t, `{"target":512,"overlap":64,"hard_cap":1024}`)
	chunks := runChunk(t, "dialog", text, cfg)
	if len(chunks) != 1 {
		t.Fatalf("两轮（含续行）应合并为 1 块，实际 %d", len(chunks))
	}
	if !strings.Contains(chunks[0].Text, "续行文本（无前缀）") {
		t.Fatalf("续行应保留于块文本: %q", chunks[0].Text)
	}
	if chunks[0].TimeTo != 9 {
		t.Fatalf("TimeTo 应为末轮时间戳 9，实际 %d", chunks[0].TimeTo)
	}
}
