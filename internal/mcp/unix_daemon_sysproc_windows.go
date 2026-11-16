//go:build windows

// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package mcp

import "syscall"

// daemonSysProcAttr: Windows has no session-leader flag (syscall.SysProcAttr
// carries no Setsid field), and a child process already outlives its parent
// there, so no attribute is set.
func daemonSysProcAttr() *syscall.SysProcAttr {
	return nil
}
