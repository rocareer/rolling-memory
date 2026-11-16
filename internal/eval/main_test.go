// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package eval

import (
	"os"
	"testing"
)

// TestMain 把整个测试二进制的 state dir 锁进临时目录：
//   - 世代租约目录（<state dir>/eval/generations）绝不写进真实用户状态目录；
//   - 用例之间也不互相看到对方的世代租约（判定不受历史残留影响）。
//
// 这是包级不变量：任何用例都不需要（也不允许）自己再准备 state dir 隔离。
func TestMain(m *testing.M) {
	stateHome, err := os.MkdirTemp("", "mem-eval-test-state-*")
	if err != nil {
		panic("create isolated state dir: " + err.Error())
	}
	_ = os.Setenv("XDG_STATE_HOME", stateHome)
	_ = os.Setenv("HOME", stateHome)
	code := m.Run()
	_ = os.RemoveAll(stateHome)
	os.Exit(code)
}
