// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package api

import (
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/recall"
	"github.com/rocareer/rolling-memory/internal/thinclient"
)

func TestThinDeliveryRawCapAfterBandSelection(t *testing.T) {
	one := 1
	includeRaw := false
	for _, tc := range []struct {
		name string
		req  RecallRequest
		want int
	}{
		{"default", RecallRequest{}, 2},
		{"raw-limit", RecallRequest{RawLimit: 1}, 1},
		{"raw-disabled", RecallRequest{IncludeRaw: &includeRaw}, 0},
		{"band-limit", RecallRequest{CapsBands: &one}, 1},
		{"offset-and-limit", RecallRequest{Offset: 1, CapsBands: &one, RawLimit: 1}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := &recall.Result{
				Bands: []map[string]any{
					{"nodes": []map[string]any{{"id": "a"}}},
					{"nodes": []map[string]any{{"id": "b"}}},
				},
				RawHits: []map[string]any{{"trace": "a"}, {"trace": "b"}},
				Meta:    map[string]any{}, Gaps: map[string]int{},
			}
			applyThinDeliveryCaps(result, tc.req)
			if len(result.RawHits) != tc.want {
				t.Fatalf("raw hits = %#v, want %d", result.RawHits, tc.want)
			}
			if tc.req.Offset == 1 && result.RawHits[0]["trace"] != "b" {
				t.Fatalf("offset leaked previous raw hit: %#v", result.RawHits)
			}
			if (tc.name == "raw-limit" || tc.name == "raw-disabled") && result.Gaps["budget_exceeded"] == 0 {
				t.Fatal("raw delivery reduction must report a budget gap")
			}
		})
	}
}

func TestDedupeResolvedHitsByContentPreservesDistinctFactsAndSources(t *testing.T) {
	prefix := strings.Repeat("The recorded transaction and its supporting evidence remain available. ", 30)
	for _, tc := range []struct {
		name, first, second string
	}{
		{"amount", prefix + "$100", prefix + "$900"},
		{"negation", prefix + "approved", prefix + "not approved"},
		{"time", prefix + "2026-11-10", prefix + "2026-11-11"},
		{"tail", prefix + "first outcome", prefix + "second outcome"},
		{"chinese", prefix + "金额一百元", prefix + "金额九百元"},
		{"arabic", prefix + "تم الدفع", prefix + "لم يتم الدفع"},
		{"identical independent sources", prefix, prefix},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hits := []thinclient.LocalResolvedHit{
				{CloudQueryHit: thinclient.CloudQueryHit{UnitID: "a"}, RawPlaintext: tc.first},
				{CloudQueryHit: thinclient.CloudQueryHit{UnitID: "b"}, RawPlaintext: tc.second},
			}
			got, dropped := dedupeResolvedHitsByContent(hits)
			if dropped != 0 || len(got) != 2 || got[0].UnitID != "a" || got[1].UnitID != "b" {
				t.Fatalf("independent sources must survive: kept=%+v dropped=%d", got, dropped)
			}
		})
	}
}

func TestDedupeResolvedHitsByContentKeepsShortRepeatedPrompts(t *testing.T) {
	hits := []thinclient.LocalResolvedHit{
		{CloudQueryHit: thinclient.CloudQueryHit{UnitID: "a"}, RawPlaintext: "[user] 继续"},
		{CloudQueryHit: thinclient.CloudQueryHit{UnitID: "b"}, RawPlaintext: "[user] 继续"},
	}
	got, dropped := dedupeResolvedHitsByContent(hits)
	if dropped != 0 || len(got) != 2 {
		t.Fatalf("short prompts must not be collapsed by near-duplicate detection: kept=%d dropped=%d", len(got), dropped)
	}
}

func TestStripAssistantRoutingPreambleRemovesHarnessNoise(t *testing.T) {
	text := "[user] 请核对输出。\n" +
		"[assistant] 技能判定：`strict-falsification`｜分支判定：local｜文档判定：docs/a.md\n\n" +
		"[assistant] 结论：交付只保留证据。"
	got := stripAssistantRoutingPreamble(text)
	if strings.Contains(got, "技能判定") || strings.Contains(got, "分支判定") || strings.Contains(got, "文档判定") {
		t.Fatalf("routing preamble survived: %q", got)
	}
	if !strings.Contains(got, "结论：交付只保留证据。") || !strings.Contains(got, "请核对输出。") {
		t.Fatalf("real evidence was removed: %q", got)
	}
}

func TestFocusEpisodicDeliveryTextKeepsQueryBearingSentence(t *testing.T) {
	text := "[assistant] 开场是一段很长的背景说明。" +
		strings.Repeat("这里继续重复背景信息但和问题无关。", 20) +
		"真正的结论是检索被重复信息淹没。" +
		strings.Repeat("后续还有更多无关尾巴。", 20)
	got := focusEpisodicDeliveryText(text, "重复信息淹没", 200)
	if !strings.Contains(got, "检索被重复信息淹没。") {
		t.Fatalf("query-bearing sentence or terminal punctuation was lost: %q", got)
	}
	if len([]rune(got)) > 210 {
		t.Fatalf("focused delivery exceeded its budget: %d runes", len([]rune(got)))
	}
}

func TestFocusEpisodicDeliveryTextPreservesListNumbersQuotesAndPaths(t *testing.T) {
	text := "[assistant] 先汇报无关背景：" + strings.Repeat("无关背景填充句子。", 25) + "\n" +
		"2. **「做好了吗？」** —— 现在注入时机 100% 归 Agent（`go test ./internal/mcp/...` 全绿）。\n" +
		"| 层级 | 职责 |\n" +
		"| --- | --- |\n" +
		"| L1 | 工具契约 |\n" +
		strings.Repeat("结尾继续填充无关长句。", 25)
	got := focusEpisodicDeliveryText(text, "做好了吗 注入时机 Agent", 260)
	if !strings.Contains(got, "2. **「做好了吗？」**") {
		t.Fatalf("list number or quoted question mark was shredded: %q", got)
	}
	if !strings.Contains(got, "./internal/mcp/...") {
		t.Fatalf("path literal was split at dots: %q", got)
	}
	if strings.Contains(got, "\n2\n") || strings.Contains(got, "\n」**") {
		t.Fatalf("inline clause was broken across lines: %q", got)
	}
}

func TestFocusEpisodicDeliveryTextPreservesDotfilePathsURLsAndSymmetricBackticks(t *testing.T) {
	text := "[assistant] 思考过程：\n" +
		strings.Repeat("无关背景描述句子。", 25) + "\n" +
		"- 现役 `.efficacy.sqlite` 与 `~/.zcode/AGENTS.md`、`examples/zcode-plugin/.zcode-plugin/plugin.json`、`docs/*.md`、`[recall].chars`；`pluginHasHooks` 同时识别 `http://127.0.0.1:18767/messages?sessionId=s-fd0ebb47`。\n" +
		"| # | 层级 | 结论 |\n" +
		"| :--- | :--- | :--- |\n" +
		"| C1 | MCP 工具面 | 唯一工具 recall |\n" +
		strings.Repeat("结尾填充句子。", 25) + "\n" +
		"[Rolling-Memory 复盘] 调用: 1 次 | 节省往返: 2 次 | 评价: 有帮助"
	got := focusEpisodicDeliveryText(text, "efficacy.sqlite pluginHasHooks C1 唯一工具", 420)
	for _, want := range []string{
		"`.efficacy.sqlite`",
		"`~/.zcode/AGENTS.md`",
		"`examples/zcode-plugin/.zcode-plugin/plugin.json`",
		"`docs/*.md`",
		"`[recall].chars`",
		"；`pluginHasHooks`",
		"?sessionId=s-fd0ebb47",
		"| # | 层级 | 结论 |",
		"| C1 | MCP 工具面 | 唯一工具 recall |",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("focused delivery corrupted or dropped %q:\n%s", want, got)
		}
	}
	for _, forbidden := range []string{
		". efficacy",
		"~/. zcode",
		"*. md",
		"[recall]. chars",
		"` pluginHasHooks`",
		"? sessionId=",
		"思考过程：",
		"[Rolling-Memory 复盘]",
	} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("focused delivery leaked artifact %q:\n%s", forbidden, got)
		}
	}
}
