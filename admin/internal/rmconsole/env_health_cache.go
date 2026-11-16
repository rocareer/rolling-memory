// plugin: rolling-memory v0.0.58
package rollingmemory

import (
	"context"
	"strings"
	"sync"
	"time"
)

// 环境健康读数（/env/health 一次性端点）的采集成本是一次 `rolling-memory sync status --json`
// 子进程：本机实测散热态 7.5s（user 6.3s、89% CPU），而该端点会被「进入记忆总览 / 三层独立页
// 挂载 / 面板重试」等路径反复请求 —— 每次都整跑一遍子进程，页面首屏与后台都被拖住。
//
// TTL 取值推演：读数内容 = Harness 检测 + 健康度评分 + 三层对齐/待同步计数，变化尺度是
// 「会话轮次」的分钟级增量与挂载目录增删；30s 错峰对诊断面板无感知，而写操作（对齐同步）
// 路径显式失效缓存，保证「刚同步完」看到的不是旧计数。用户显式点击「重新检测」以 force 绕过。
// 零/退化态：子进程失败时回退到上一次成功读数（最多 envHealthPayloadStaleTTL），从未成功过
// 才如实报错，不把页面打回空面板。缓存键为「本地金库路径 + Harness」，基数极小无需淘汰；
// 持锁期间完成采集，并发调用在过期点串行至多一次重算（单飞语义，与 artifact_stats_cache 一致）。
const (
	envHealthPayloadCacheTTL = 30 * time.Second
	// envHealthPayloadStaleTTL 采集失败时的降级回退窗口：窗口内沿用上一次成功读数。
	envHealthPayloadStaleTTL = 5 * time.Minute
)

var (
	envHealthPayloadMu    sync.Mutex
	envHealthPayloadCache = map[string]*envHealthPayloadEntry{}
)

type envHealthPayloadEntry struct {
	payload    map[string]any
	at         time.Time
	refreshing bool
}

// cachedEnvHealthPayload 返回环境健康读数：TTL 内直接复用；过期但在 StaleTTL 窗口内且非 force 时
// 立即返回上次成功读数并触发后台单飞刷新（Stale-While-Revalidate）；force 或无缓存时同步采集。
func cachedEnvHealthPayload(ctx context.Context, home, harness string, force bool) (map[string]any, error) {
	key := envHealthCacheKey(home, harness)
	envHealthPayloadMu.Lock()

	now := time.Now()
	entry := envHealthPayloadCache[key]
	if !force && entry != nil {
		if now.Sub(entry.at) < envHealthPayloadCacheTTL {
			p := entry.payload
			envHealthPayloadMu.Unlock()
			return p, nil
		}
		if now.Sub(entry.at) < envHealthPayloadStaleTTL {
			p := entry.payload
			if !entry.refreshing {
				entry.refreshing = true
				go func(h, har, k string) {
					bgCtx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
					defer cancel()
					fresh, err := collectEnvHealthPayload(bgCtx, h, har)
					envHealthPayloadMu.Lock()
					if err == nil && fresh != nil {
						envHealthPayloadCache[k] = &envHealthPayloadEntry{payload: fresh, at: time.Now()}
					} else if cur := envHealthPayloadCache[k]; cur != nil {
						cur.refreshing = false
					}
					envHealthPayloadMu.Unlock()
				}(home, harness, key)
			}
			envHealthPayloadMu.Unlock()
			return p, nil
		}
	}
	defer envHealthPayloadMu.Unlock()

	payload, err := collectEnvHealthPayload(ctx, home, harness)
	if err != nil {
		if entry != nil && now.Sub(entry.at) < envHealthPayloadStaleTTL {
			return entry.payload, nil
		}
		return nil, err
	}
	envHealthPayloadCache[key] = &envHealthPayloadEntry{payload: payload, at: now}
	return payload, nil
}

// invalidateEnvHealthCache 写操作（对齐同步等）完成后丢弃该金库的全部环境健康读数，
// 使页面随后读取到新的待同步计数，而不是等 TTL 过期。
func invalidateEnvHealthCache(home string) {
	invalidateVaultReadCaches()
	prefix := envHealthCacheKey(home, "")
	envHealthPayloadMu.Lock()
	defer envHealthPayloadMu.Unlock()
	for k := range envHealthPayloadCache {
		if strings.HasPrefix(k, prefix) {
			delete(envHealthPayloadCache, k)
		}
	}
}

// envHealthCacheKey 缓存键 = 金库路径 + Harness 过滤（不同 Harness 的读数内容不同）。
func envHealthCacheKey(home, harness string) string {
	return resolveMemoryDBPath(home) + "\x00" + strings.TrimSpace(harness)
}

// storeCachedEnvHealthPayload 将实时流或对齐同步产出的最新环境健康载荷写入对应缓存键，
// 使首屏订阅与一次性端点可直接秒级复用。
func storeCachedEnvHealthPayload(home, harness string, payload map[string]any) {
	if payload == nil {
		return
	}
	key := envHealthCacheKey(home, harness)
	envHealthPayloadMu.Lock()
	envHealthPayloadCache[key] = &envHealthPayloadEntry{payload: payload, at: time.Now()}
	envHealthPayloadMu.Unlock()
}

// peekCachedEnvHealthPayload 只读取出 StaleTTL 窗口内已有的环境健康快照及其采集时刻（不触发子进程）。
func peekCachedEnvHealthPayload(home, harness string) (map[string]any, time.Time, bool) {
	key := envHealthCacheKey(home, harness)
	envHealthPayloadMu.Lock()
	defer envHealthPayloadMu.Unlock()
	entry := envHealthPayloadCache[key]
	if entry == nil || entry.payload == nil || time.Since(entry.at) >= envHealthPayloadStaleTTL {
		return nil, time.Time{}, false
	}
	return entry.payload, entry.at, true
}
