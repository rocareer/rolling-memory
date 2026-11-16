// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package transcript

import (
	"strings"
	"testing"
)

func TestPyDecodeUTF8MatchesPythonReplace(t *testing.T) {
	replacement := string('\uFFFD')
	cases := []struct {
		raw  []byte
		want string
	}{
		{[]byte("ok"), "ok"},
		{[]byte{0x80}, replacement},
		{[]byte{0x80, 0x80}, replacement + replacement},
		{[]byte{0xE0, 0x80}, replacement + replacement},
		{[]byte{0xE0, 0x80, 0x80}, replacement + replacement + replacement},
		{[]byte{0xF0, 0x9F}, replacement},
		{[]byte{0xF0}, replacement},
		{[]byte{0xED, 0xA0, 0x80}, replacement + replacement + replacement},
		{[]byte{0xC0, 0xAF}, replacement + replacement},
		{[]byte{0xE1, 0x80}, replacement},
		{[]byte{0xF4, 0x90, 0x80, 0x80}, replacement + replacement + replacement + replacement},
		{[]byte{0xE0, 0xA0, 0x80}, "\u0800"},
		{[]byte("a\xE1\x80b"), "a" + replacement + "b"},
		{[]byte("ok\x80ok"), "ok" + replacement + "ok"},
	}
	for _, test := range cases {
		if got := pyDecodeUTF8(test.raw); got != test.want {
			t.Fatalf("pyDecodeUTF8(%v) = %q, want %q", test.raw, got, test.want)
		}
	}
	long := strings.Repeat("老张", 10)
	if got := pyDecodeUTF8([]byte(long)); got != long {
		t.Fatalf("valid UTF-8 changed: %q", got)
	}
}
