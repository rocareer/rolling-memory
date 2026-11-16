// plugin: rolling-memory v0.0.58
package rollingmemory

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestEnvHealthPayloadCacheReusesAndForces 锁定 /env/health 的 TTL 复用契约：
// ① TTL 内的重复请求不再整跑 `sync status` 子进程（本机实测单次 7.5s / 89% CPU）；
// ② 显式 force（「重新检测」）必须绕过缓存、产生一次真实采集；
// ③ 采集失败时在降级窗口内回退上一次成功读数，不把页面打回空面板。
func TestEnvHealthPayloadCacheReusesAndForces(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ROLLING_MEMORY_MODE", "local")

	prevExec := executeAdminSyncCLI
	t.Cleanup(func() {
		executeAdminSyncCLI = prevExec
		envHealthPayloadMu.Lock()
		envHealthPayloadCache = map[string]*envHealthPayloadEntry{}
		envHealthPayloadMu.Unlock()
	})

	calls := 0
	executeAdminSyncCLI = func(_ context.Context, action, _, _, _ string, _ int) ([]byte, error) {
		calls++
		return []byte(`{"active_harness_id":"zcode","active_harness_label":"ZCode","health_score":96,"total_pending_sync":3}`), nil
	}

	first, err := cachedEnvHealthPayload(context.Background(), home, "", false)
	if err != nil || first["health_score"] != float64(96) {
		t.Fatalf("first collect mismatch: payload=%v err=%v", first, err)
	}
	second, err := cachedEnvHealthPayload(context.Background(), home, "", false)
	if err != nil || second["total_pending_sync"] != float64(3) {
		t.Fatalf("cached read mismatch: payload=%v err=%v", second, err)
	}
	if calls != 1 {
		t.Fatalf("repeat request must reuse the cached reading, CLI calls=%d", calls)
	}

	forced, err := cachedEnvHealthPayload(context.Background(), home, "", true)
	if err != nil || forced["health_score"] != float64(96) {
		t.Fatalf("forced collect mismatch: payload=%v err=%v", forced, err)
	}
	if calls != 2 {
		t.Fatalf("force must bypass the cache, CLI calls=%d", calls)
	}

	// Harness 过滤是缓存键的一部分：换 Harness 必须重新采集。
	if _, err := cachedEnvHealthPayload(context.Background(), home, "zcode", false); err != nil {
		t.Fatalf("harness-scoped collect: %v", err)
	}
	if calls != 3 {
		t.Fatalf("different harness must not reuse another harness reading, CLI calls=%d", calls)
	}

	// 采集失败：降级窗口内回退上一次成功读数。
	executeAdminSyncCLI = func(_ context.Context, _, _, _, _ string, _ int) ([]byte, error) {
		return nil, errors.New("sync status exploded")
	}
	fallback, err := cachedEnvHealthPayload(context.Background(), home, "", true)
	if err != nil {
		t.Fatalf("failure inside the stale window must fall back to the last success: %v", err)
	}
	if !strings.Contains(asString(fallback["active_harness_label"]), "ZCode") {
		t.Fatalf("fallback payload mismatch: %v", fallback)
	}

	// 写操作（对齐同步）完成后缓存必须失效，下一次读取要重新采集。
	invalidateEnvHealthCache(home)
	if _, err := cachedEnvHealthPayload(context.Background(), home, "", false); err == nil {
		t.Fatalf("after invalidation a failing CLI must surface the error, not a stale reading")
	}
}
