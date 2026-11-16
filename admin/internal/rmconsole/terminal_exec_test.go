// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package rollingmemory

import (
	"strings"
	"testing"
)

// TestTerminalExecArgsOnlyLiveSubcommands 锁定 Live Console 命令键 → CLI 子命令的映射契约：
// 只允许现行子命令，govern / conflicts / nodes 等停用键必须回落到 status，否则终端点出 usage 报错。
func TestTerminalExecArgsOnlyLiveSubcommands(t *testing.T) {
	const db = "/tmp/vault.db"
	cases := []struct {
		key      string
		wantSub  string
		wantArgs []string
	}{
		{"status", "status", []string{"status", "--db", db, "--json"}},
		{"doctor", "doctor", []string{"doctor", "--db", db, "--json"}},
		{"check", "check", []string{"check", "--db", db, "--json"}},
		{"recent", "recent", []string{"recent", "--db", db, "--limit", "10", "--json"}},
		{"", "status", []string{"status", "--db", db, "--json"}},
		{"unknown", "status", []string{"status", "--db", db, "--json"}},
		// 停用键（历史前端与旧书签可能仍传入）：不得再映射到已停用子命令。
		{"health", "status", []string{"status", "--db", db, "--json"}},
		{"report", "status", []string{"status", "--db", db, "--json"}},
		{"conflicts", "status", []string{"status", "--db", db, "--json"}},
		{"nodes", "status", []string{"status", "--db", db, "--json"}},
	}
	retired := map[string]bool{"govern": true, "conflicts": true, "nodes": true}
	for _, tc := range cases {
		args, key := terminalExecArgs(tc.key, db)
		if key != tc.wantSub {
			t.Fatalf("terminalExecArgs(%q) key = %q, want %q", tc.key, key, tc.wantSub)
		}
		if strings.Join(args, " ") != strings.Join(tc.wantArgs, " ") {
			t.Fatalf("terminalExecArgs(%q) args = %v, want %v", tc.key, args, tc.wantArgs)
		}
		if len(args) == 0 || retired[args[0]] {
			t.Fatalf("terminalExecArgs(%q) 映射到退役子命令: %v", tc.key, args)
		}
	}
}
