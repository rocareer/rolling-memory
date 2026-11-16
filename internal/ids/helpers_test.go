// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package ids

import (
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	cases := []struct {
		input      string
		want       string
		notContain string
	}{
		{"key=sk-secret-value", "key=***", "sk-secret-value"},
		{"token=ghp_abcdefgh", "token=***", "ghp_abcdefgh"},
		{"Authorization: Bearer abcdefgh1234", "Authorization: ***", "abcdefgh1234"},
		{"https://x/?access_token=abc&y=1", "https://x/?***&y=1", "access_token=abc"},
		{"token=abcdefgh", "***", "abcdefgh"},
		{"老张sk-abcdefgh", "老张sk-abcdefgh", ""},
		{"plain text", "plain text", ""},
	}
	for _, testCase := range cases {
		got := Redact(testCase.input)
		if got != testCase.want {
			t.Errorf("Redact(%q) = %q, want %q", testCase.input, got, testCase.want)
		}
		if testCase.notContain != "" && strings.Contains(got, testCase.notContain) {
			t.Errorf("Redact(%q) leaked %q", testCase.input, testCase.notContain)
		}
	}
}

func TestStripInjected(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{
			input: `<rolling-memory-recall>secret</rolling-memory-recall> hello`,
			want:  "hello",
		},
		{
			input: `<system-reminder data-x="1">hidden</system-reminder>visible`,
			want:  "visible",
		},
		{
			input: `<rolling-memory-boot>old</rolling-memory-boot>tail`,
			want:  "tail",
		},
		{
			input: `<other-recall attr="1">memory</other-recall> tail`,
			want:  "tail",
		},
		{
			input: "line\n[tool] todo\nafter",
			want:  "line\n \nafter",
		},
		{
			input: "<thinking>internal monologue</thinking> 用户可见的回答。",
			want:  "用户可见的回答。",
		},
		{
			input: "[thinking] 先看 store 包的断言。\n[user] 结论：缓存按 docID 失效。\n[assistant] 同意。\n",
			want:  "[user] 结论：缓存按 docID 失效。\n[assistant] 同意。",
		},
		{
			input: "[user] 跑一下测试。\n[tool_use Bash] {\"cmd\": \"go test ./...\"}\n[tool_result] ok  .\n",
			want:  "[user] 跑一下测试。",
		},
		{
			input: "正文里提到 `[tool_result]` 不算信封。",
			want:  "正文里提到 `[tool_result]` 不算信封。",
		},
	}
	for _, testCase := range cases {
		if got := StripInjected(testCase.input); got != testCase.want {
			t.Errorf("StripInjected(%q) = %q, want %q", testCase.input, got, testCase.want)
		}
	}
}

func TestSentenceSplit(t *testing.T) {
	got := SentenceSplit("老张养了一只猫。Alice likes SQLite. Next one", 40, 240)
	want := []string{"老张养了一只猫。", "Alice likes SQLite.", "Next one"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SentenceSplit = %#v, want %#v", got, want)
	}
	got = SentenceSplit("e.g. this stays together", 40, 240)
	if !reflect.DeepEqual(got, []string{"e.g. this stays together"}) {
		t.Fatalf("SentenceSplit lowercase continuation = %#v", got)
	}
	got = SentenceSplit("One. Two. Three. Four.", 2, 240)
	if len(got) != 2 {
		t.Fatalf("SentenceSplit maxParts = %#v", got)
	}
	got = SentenceSplit("abcdefghij。", 40, 5)
	if len(got) != 1 || got[0] != "abcde" {
		t.Fatalf("SentenceSplit maxLen = %#v", got)
	}
	got = SentenceSplit("<other-recall>x</other-recall> kept", 40, 240)
	if !reflect.DeepEqual(got, []string{"kept"}) {
		t.Fatalf("SentenceSplit injection = %#v", got)
	}
}

func TestTruncateAndCollections(t *testing.T) {
	if got := Truncate("  hello world  ", 8, "…"); got != "hello w…" {
		t.Errorf("Truncate = %q", got)
	}
	if got := Truncate("hello", 5, "…"); got != "hello" {
		t.Errorf("Truncate exact = %q", got)
	}
	if got := Truncate("hello", 1, "…"); got != "h" {
		t.Errorf("Truncate suffix limit = %q", got)
	}
	if got := UniqueOrdered([]string{"a", "b", "a", "c", "b"}); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("UniqueOrdered = %v", got)
	}
	if got := Mean(nil); got != 0 {
		t.Errorf("Mean(nil) = %v", got)
	}
	if got := Mean([]float64{1, 2, 3}); got != 2 {
		t.Errorf("Mean = %v", got)
	}
	if got := EscapeLike(`a\b%c_d`); got != `a\\b\%c\_d` {
		t.Errorf("EscapeLike = %q", got)
	}
}

func TestNetworkHelpers(t *testing.T) {
	host, ok := URLHost("https://User:Pass@Example.COM:443/x")
	if !ok || host != "example.com" {
		t.Fatalf("URLHost = (%q, %v)", host, ok)
	}
	if host, ok := URLHost("http://[::1]:80/x"); !ok || host != "::1" {
		t.Fatalf("URLHost IPv6 = (%q, %v)", host, ok)
	}
	for _, value := range []string{"localhost", "LOCALHOST", ".localhost", "127.0.0.1", "::1", "[::1]"} {
		if !IsLoopbackHost(value) {
			t.Errorf("IsLoopbackHost(%q) = false", value)
		}
	}
	for _, value := range []string{"", "8.8.8.8", "example.com", "127.0.0.1.evil.com"} {
		if IsLoopbackHost(value) {
			t.Errorf("IsLoopbackHost(%q) = true", value)
		}
	}
}

func TestTurnAndClampEdgeCases(t *testing.T) {
	reference := int64(1793102400)
	if got := ParseTime("1969-12-31T23:59:59.5Z", ParseTimeOptions{TimeRef: &reference}); got != 0 {
		t.Errorf("pre-epoch fractional ParseTime = %d, want 0", got)
	}
	if got := ParseTime("1970-01-01T00:00:00.5Z", ParseTimeOptions{TimeRef: &reference}); got != 0 {
		t.Errorf("epoch fractional ParseTime = %d, want 0", got)
	}
	if got := AssertionNodeID("abc"); got != "a_abc" {
		t.Errorf("AssertionNodeID short = %q, want a_abc", got)
	}
	if got := NormalizeTurn(7.0); got != 0 {
		t.Errorf("NormalizeTurn(7.0) = %d, want 0", got)
	}
	if got := NormalizeTurn(7); got != 7 {
		t.Errorf("NormalizeTurn(7) = %d, want 7", got)
	}
	if got := NormalizeTurn(nil); got != 0 {
		t.Errorf("NormalizeTurn(nil) = %d, want 0", got)
	}
	if got := Clamp(math.NaN(), 0, 1); got != 1 {
		t.Errorf("Clamp(NaN) = %v, want 1", got)
	}
	if got := Clamp(-2, 0, 1); got != 0 {
		t.Errorf("Clamp(-2) = %v", got)
	}
	if got := Clamp(2, 0, 1); got != 1 {
		t.Errorf("Clamp(2) = %v", got)
	}
}
