package host

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestGateCacheAndRefresh(t *testing.T) {
	var calls int32
	status := StatusEnabled
	g := newGate(time.Second, func(ctx context.Context, name string) (string, error) {
		atomic.AddInt32(&calls, 1)
		return status, nil
	})
	ctx := context.Background()

	if !g.enabled(ctx, "demo") {
		t.Fatal("enabled 状态应放行")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("首次判定应查一次库，实际 %d 次", got)
	}

	// TTL 内改库仍走缓存——这正是「CLI 启停 ≤TTL 生效」的语义
	status = StatusDisabled
	if !g.enabled(ctx, "demo") {
		t.Fatal("TTL 内应仍用缓存状态")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("TTL 内不应重复查库，实际 %d 次", got)
	}

	// setTTL 清空缓存（同时恢复默认上限）后重新读库
	g.setTTL(0)
	if g.enabled(ctx, "demo") {
		t.Fatal("清缓存后应读到 disabled 并拒绝")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("清缓存后应再查一次库，实际 %d 次", got)
	}

	// TTL 过期后重新读库
	g.setTTL(10 * time.Millisecond)
	time.Sleep(25 * time.Millisecond)
	_ = g.enabled(ctx, "demo")
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("TTL 过期后应再查一次库，实际 %d 次", got)
	}
}

func TestGateFailClosed(t *testing.T) {
	var calls int32
	g := newGate(time.Second, func(ctx context.Context, name string) (string, error) {
		atomic.AddInt32(&calls, 1)
		return "", errors.New("库不可用")
	})
	ctx := context.Background()
	if g.enabled(ctx, "demo") {
		t.Fatal("读状态失败必须按未启用处理")
	}
	if g.enabled(ctx, "demo") {
		t.Fatal("读状态失败必须按未启用处理")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("失败结果不写缓存，应查两次库，实际 %d 次", got)
	}

	// 台账无此行（readStatus 返回空状态）⇒ 拒绝
	empty := newGate(time.Second, func(ctx context.Context, name string) (string, error) { return "", nil })
	if empty.enabled(ctx, "demo") {
		t.Fatal("空状态（台账无此行）必须拒绝")
	}

	// 其余状态一律拒绝
	for _, status := range []string{StatusFailed, StatusInstalled, StatusDisabled} {
		state := status
		other := newGate(time.Second, func(ctx context.Context, name string) (string, error) { return state, nil })
		if other.enabled(ctx, "demo") {
			t.Errorf("状态 %s 不应放行", state)
		}
	}
}

func TestSetGateTTLGlobal(t *testing.T) {
	defer SetGateTTL(0) // 恢复默认

	SetGateTTL(2 * time.Second)
	gate.mu.Lock()
	got := gate.ttl
	gate.mu.Unlock()
	if got != 2*time.Second {
		t.Fatalf("SetGateTTL(2s) 后 TTL = %v", got)
	}

	SetGateTTL(0)
	gate.mu.Lock()
	got = gate.ttl
	gate.mu.Unlock()
	if got != defaultGateTTL {
		t.Fatalf("SetGateTTL(0) 应恢复默认 %v，实际 %v", defaultGateTTL, got)
	}
}
