// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package speech

import "testing"

// TestStripProtocolEnvelopes pins the conversation denoise used by every episodic
// ingest path and by the thin client: only line-anchored protocol envelopes leave
// the text, every remaining byte stays where the speaker put it.
func TestStripProtocolEnvelopes(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "no protocol marker is a zero-change fast path",
			body: "普通回复，没有任何协议信封。\nplain reply without envelopes.",
			want: "普通回复，没有任何协议信封。\nplain reply without envelopes.",
		},
		{
			name: "whole turn is a thinking scratchpad",
			body: "[thinking] Let me re-read the failing assertion first.",
			want: "",
		},
		{
			name: "speech survives around thinking and tool envelopes",
			body: "先说结论：缓存要按 docID 失效。\n[tool_use Bash] {\"cmd\": \"go test ./internal/ids/\"}\n" +
				"[tool_result] ok  github.com/rocareer/rolling-memory/internal/ids\n",
			want: "先说结论：缓存要按 docID 失效。",
		},
		{
			name: "inline mention is not a block anchor",
			body: "协议里 `[thinking]` 只是行首标记，正文提到它不算信封。",
			want: "协议里 `[thinking]` 只是行首标记，正文提到它不算信封。",
		},
		{
			name: "prefix ambiguity keeps [tool_useX] as speech",
			body: "[tool_useX] is not an envelope.\n",
			want: "[tool_useX] is not an envelope.\n",
		},
		{
			name: "multi-script speech is never sliced",
			body: "مرحبا بالعالم\nสวัสดีชาวโลก\nこんにちは世界\n[thinking] scratch\nשלום עולם\n",
			want: "مرحبا بالعالم\nสวัสดีชาวโลก\nこんにちは世界",
		},
		{
			name: "a speaker envelope ends the envelope body",
			body: "[thinking] 先看 store 包的断言。\n[user] 结论：缓存按 docID 失效。\n",
			want: "[user] 结论：缓存按 docID 失效。\n",
		},
		{
			name: "a blank line ends the envelope body",
			body: "[tool_result] ok  github.com/rocareer/rolling-memory/internal/ids\n\n最终答案：保留这段话。\n",
			want: "最终答案：保留这段话。\n",
		},
		{
			name: "consecutive envelopes are removed together",
			body: "[user] 跑一下测试。\n[thinking] 走一遍包依赖。\n[tool_use Bash] {\"cmd\": \"go test ./...\"}\n[tool_result] ok\n",
			want: "[user] 跑一下测试。",
		},
	}
	for _, tc := range cases {
		if got := StripProtocolEnvelopes(tc.body); got != tc.want {
			t.Errorf("%s: StripProtocolEnvelopes(%q) = %q, want %q", tc.name, tc.body, got, tc.want)
		}
	}
}

// TestStripConversationNoise covers the injection envelopes that sit next to the
// protocol blocks: closed tag pairs are removed, unpaired mentions survive.
func TestStripConversationNoise(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "closed thinking tag pair",
			body: "<thinking>internal monologue</thinking> 用户可见的回答。",
			want: "用户可见的回答。",
		},
		{
			name: "unpaired tag mention survives",
			body: "文档里写的是 <thinking> 标签用法。",
			want: "文档里写的是 <thinking> 标签用法。",
		},
		{
			name: "system reminder with attributes",
			body: `<system-reminder data-x="1">hidden</system-reminder>visible`,
			want: "visible",
		},
		{
			name: "recall envelope by name",
			body: `<rolling-memory-recall>secret</rolling-memory-recall> hello`,
			want: "hello",
		},
		{
			name: "any recall-suffixed envelope",
			body: `<other-recall attr="1">memory</other-recall> tail`,
			want: "tail",
		},
		{
			name: "tool status lines",
			body: "line\n[tool] todo\nafter",
			want: "line\n \nafter",
		},
		{
			name: "backtick or quoted tag pair is code mention not injection",
			body: "拍平形态含 `[thinking]`、`<thinking>…</thinking>` 与 \"<system-reminder>x</system-reminder>\"。",
			want: "拍平形态含 `[thinking]`、`<thinking>…</thinking>` 与 \"<system-reminder>x</system-reminder>\"。",
		},
	}
	for _, tc := range cases {
		if got := StripConversationNoise(tc.body); got != tc.want {
			t.Errorf("%s: StripConversationNoise(%q) = %q, want %q", tc.name, tc.body, got, tc.want)
		}
	}
}
