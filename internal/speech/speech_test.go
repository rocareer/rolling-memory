// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package speech

import "testing"

// TestStripProtocolEnvelopes pins the conversation denoise used by every episodic
// ingest path and by the local vault client: only line-anchored protocol envelopes leave
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
		{
			name: "claude task notification envelope",
			body: "<task-notification>\n<task-id>b4ot1q964</task-id>\n<output-file>/tmp/x</output-file>\n</task-notification>\n继续审计。",
			want: "继续审计。",
		},
		{
			name: "claude ide selection envelope",
			body: "<ide_selection>The user selected the lines 106 to 111 from /home/user/app.go</ide_selection>看看这段",
			want: "看看这段",
		},
		{
			name: "claude ide opened file envelope",
			body: "<ide_opened_file>The user opened the file /home/user/app.go in the IDE.</ide_opened_file>",
			want: "",
		},
		{
			name: "claude command envelopes",
			body: "<command-name>/compact</command-name>\n            <command-message>compact</command-message>\n            <command-args></command-args>\n<local-command-stdout>Compacted</local-command-stdout>",
			want: "",
		},
		{
			name: "claude slash command with trailing user text",
			body: "<command-name>/compact</command-name>\n<command-message>compact</command-message>\n<command-args></command-args>真正的问题",
			want: "真正的问题",
		},
		{
			name: "codex turn aborted envelope",
			body: "<turn_aborted>\nThe user interrupted the previous turn.\n</turn_aborted>",
			want: "",
		},
		{
			name: "codex environment context envelope",
			body: "<environment_context>\n  <cwd>/home/user</cwd>\n</environment_context>问题在这里",
			want: "问题在这里",
		},
		{
			name: "unpaired host envelope mention survives",
			body: "查一下 <task-notification> 这个标签是谁写的。",
			want: "查一下 <task-notification> 这个标签是谁写的。",
		},
	}
	for _, tc := range cases {
		if got := StripConversationNoise(tc.body); got != tc.want {
			t.Errorf("%s: StripConversationNoise(%q) = %q, want %q", tc.name, tc.body, got, tc.want)
		}
	}
}
