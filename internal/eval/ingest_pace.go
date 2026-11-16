// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package eval

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"
)

// ingest_pace.go —— 评测 ingest / purge 的客户端礼貌节拍（可配置，默认解除）。
//
// 第一性事实：速率限制是**服务端**的职责——引擎以 per-key 配额表达（`rps < 0` 的密钥
// 不受限），配额闸只存在于数据面。客户端节拍既拦不住任何自制客户端（源码开放、可自改），
// 也无法替服务端表达限流策略；在引擎无限制时它只会白白拖长灌库
// （旧的 150ms 节拍 ⇒ ~6.7 条/s ⇒ 11,034 条语料的地板就是 27.6 分钟）。
//
// 因此默认 **0 = 不加节拍**：吞吐由 `--workers` 并发度与 429/503 指数退避
// （rememberWithRetry / deleteUnitsWithRetry）自然成界——两者本就是为「引擎限流从失败
// 变成排队」而设计的。共享主机上需要主动错峰（多维并发评测同机）时，设
// ROLLING_MEMORY_EVAL_INGEST_PACE_MS=150 即可恢复旧的全局节拍行为。
var evalIngestPace = ingestPaceFromEnv()

// ingestPaceFromEnv 读取 ROLLING_MEMORY_EVAL_INGEST_PACE_MS（毫秒）；缺省/非法/非正一律 = 0（解除）。
func ingestPaceFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_EVAL_INGEST_PACE_MS"))
	if raw == "" {
		return 0
	}
	ms, err := strconv.Atoi(raw)
	if err != nil || ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

// evalIngestPaceGate 返回节拍通道与停止函数；节拍解除（<= 0）时返回 nil 通道——
// **调用点必须经 waitIngestPace 等节拍**（nil 通道永远不就绪，直接 select 裸通道
// 又遇上不可取消的 ctx 会把整个 ingest 永久阻塞）。
func evalIngestPaceGate() (<-chan time.Time, func()) {
	if evalIngestPace <= 0 {
		return nil, func() {}
	}
	ticker := time.NewTicker(evalIngestPace)
	return ticker.C, ticker.Stop
}

// waitIngestPace 等节拍或 ctx；nil 通道（兼容旧调用形态）立即放行，只保留 ctx 取消语义。
func waitIngestPace(ctx context.Context, paceC <-chan time.Time) error {
	if paceC == nil {
		return ctx.Err()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-paceC:
		return nil
	}
}
