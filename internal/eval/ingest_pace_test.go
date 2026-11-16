// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package eval

import (
	"context"
	"testing"
	"time"
)

// TestIngestPaceFromEnvPinsKnob 锁住客户端节拍开关的语义：
// 缺省 = 0（解除，不加节拍）；正毫秒数按值生效；非法/非正一律回 0（fail-open 到解除，
// 绝不因坏配置把评测卡在未知节拍上）。
func TestIngestPaceFromEnvPinsKnob(t *testing.T) {
	t.Setenv("ROLLING_MEMORY_EVAL_INGEST_PACE_MS", "")
	if got := ingestPaceFromEnv(); got != 0 {
		t.Fatalf("缺省应为 0（解除节拍），got %v", got)
	}
	t.Setenv("ROLLING_MEMORY_EVAL_INGEST_PACE_MS", "150")
	if got := ingestPaceFromEnv(); got != 150*time.Millisecond {
		t.Fatalf("150 应解析为 150ms，got %v", got)
	}
	for _, bad := range []string{"abc", "-5", "0", "  "} {
		t.Setenv("ROLLING_MEMORY_EVAL_INGEST_PACE_MS", bad)
		if got := ingestPaceFromEnv(); got != 0 {
			t.Fatalf("非法值 %q 应回 0，got %v", bad, got)
		}
	}
}

// TestIngestPaceGateWaitSemantics 锁住闸门等待语义（修过死锁的形态）：
//   - 解除节拍：通道为 nil，waitIngestPace 必须**立即放行**——裸 select 等 nil 通道
//     会永久阻塞（历史缺陷），调用点一律走 waitIngestPace 而非裸 select；
//   - 有节拍：等到一拍才放行；ctx 取消优先返回 context.Canceled。
func TestIngestPaceGateWaitSemantics(t *testing.T) {
	saved := evalIngestPace
	t.Cleanup(func() { evalIngestPace = saved })

	// 解除节拍：nil 通道 + 短路放行。
	evalIngestPace = 0
	if ch, stop := evalIngestPaceGate(); ch != nil {
		t.Fatalf("解除节拍时通道应为 nil（由 waitIngestPace 短路放行），got %v", ch)
	} else {
		stop()
	}
	if err := waitIngestPace(context.Background(), nil); err != nil {
		t.Fatalf("解除节拍时 waitIngestPace 必须立即放行，got %v", err)
	}

	// 有节拍：等到一拍才放行（不得立刻返回）。
	evalIngestPace = 30 * time.Millisecond
	ch, stop := evalIngestPaceGate()
	begin := time.Now()
	if err := waitIngestPace(context.Background(), ch); err != nil {
		t.Fatalf("有节拍等待应成功放行，got %v", err)
	}
	if elapsed := time.Since(begin); elapsed < 15*time.Millisecond {
		t.Fatalf("有节拍时不得立刻放行（elapsed=%v）", elapsed)
	}
	stop()

	// 有节拍 + ctx 已取消：必须优先返回 context.Canceled（节拍取 500ms 保证 tick 未就绪）。
	evalIngestPace = 500 * time.Millisecond
	ch2, stop2 := evalIngestPaceGate()
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitIngestPace(cctx, ch2); err != context.Canceled {
		t.Fatalf("ctx 已取消时应返回 context.Canceled，got %v", err)
	}
	stop2()
}
