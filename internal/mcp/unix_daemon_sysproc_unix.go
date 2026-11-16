//go:build !windows

// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package mcp

import "syscall"

// daemonSysProcAttr detaches the spawned daemon into its own session so it
// survives the caller's shell and process group on Unix.
func daemonSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
