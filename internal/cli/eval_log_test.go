// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// TestEvalLogWriterKeepsJSONStdoutClean 守卫 rmem eval --json 的机器可读契约：
// stdout 必须是纯 JSON，进度日志只能走 stderr，否则 Admin 的 verify-prepare 回执
// 会因 "[eval] ..." 前缀解析失败而把已完成的 Prepare 判成失败。
func TestEvalLogWriterKeepsJSONStdoutClean(t *testing.T) {
	var stdout, stderr bytes.Buffer
	logf := evalLogWriter(true,
		func(format string, args ...any) { fmt.Fprintf(&stdout, format+"\n", args...) },
		func(format string, args ...any) { fmt.Fprintf(&stderr, format+"\n", args...) },
	)
	logf("[eval] arm=%s tier=%s", "sdk-pulse-cipher-none", "100")
	if stdout.Len() != 0 {
		t.Fatalf("json mode leaked logs to stdout: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "[eval] arm=sdk-pulse-cipher-none tier=100") {
		t.Fatalf("json mode logs missing on stderr: %q", stderr.String())
	}
}

func TestEvalLogWriterHumanModeWritesStdout(t *testing.T) {
	var stdout, stderr bytes.Buffer
	logf := evalLogWriter(false,
		func(format string, args ...any) { fmt.Fprintf(&stdout, format+"\n", args...) },
		func(format string, args ...any) { fmt.Fprintf(&stderr, format+"\n", args...) },
	)
	logf("[eval] arm=%s", "oracle")
	if !strings.Contains(stdout.String(), "[eval] arm=oracle") {
		t.Fatalf("human mode must keep logs on stdout: %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("human mode must not write stderr: %q", stderr.String())
	}
}
