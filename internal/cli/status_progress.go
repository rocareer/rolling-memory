// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

// `sync status` 的进度事件通道（面向管理后台的实时进度显示）。
//
// 契约：默认完全静默；仅当环境变量 ROLLING_MEMORY_STATUS_PROGRESS=1（由管理后台在
// 采集子进程上设置）时，把阶段事件以 JSONL 单行写入 stderr——stdout 的 --json 载荷
// 逐字节不变，人类可读的终端输出不受影响。事件是采集瞬态而非状态：单行形如
// {"progress":{"stage":"scan","status":"step","elapsed_ms":123,...}}，后台按行中继为
// SSE 进度帧；未知字段/未知 stage 由消费方按前向兼容忽略，不作为协议契约的一部分。

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

const statusProgressEnv = "ROLLING_MEMORY_STATUS_PROGRESS"

// 阶段词汇：与后台 SSE 进度帧及控制台时间线一一对应。
const (
	progressStageVault  = "vault"  // 本地金库统计
	progressStageWiring = "wiring" // 宿主接线检测
	progressStageScan   = "scan"   // 对话历史扫描（最慢的一段）
	progressStageIndex  = "index"  // Episodic 索引诊断
)

type statusProgressEmitter struct {
	started time.Time
}

// statusProgress 是进程级单例：`sync status` 单命令单进程，其余子命令从不开启，
// 因此 reconcile / rebuild 等复用扫描函数的路径天然静默（nil 即零开销）。
var statusProgress *statusProgressEmitter

// beginStatusProgress 在 `sync status` 入口按环境变量开启进度事件。
func beginStatusProgress() bool {
	if os.Getenv(statusProgressEnv) != "1" {
		return false
	}
	statusProgress = &statusProgressEmitter{started: time.Now()}
	return true
}

// endStatusProgress 关闭通道并推一个收尾帧（阶段=all）。
func endStatusProgress() {
	if statusProgress == nil {
		return
	}
	emitStatusProgress("all", "done", nil)
	statusProgress = nil
}

func emitStatusProgress(stage, status string, fields map[string]any) {
	if statusProgress == nil {
		return
	}
	event := map[string]any{
		"stage":      stage,
		"status":     status,
		"elapsed_ms": time.Since(statusProgress.started).Milliseconds(),
	}
	for key, value := range fields {
		event[key] = value
	}
	raw, err := json.Marshal(map[string]any{"progress": event})
	if err != nil {
		return
	}
	fmt.Fprintln(os.Stderr, string(raw))
}

// stageStart 发 start 帧并返回对应的 done 发射器；调用方在阶段收尾时传入附加字段。
func stageStart(stage string, fields map[string]any) func(map[string]any) {
	emitStatusProgress(stage, "start", fields)
	return func(extra map[string]any) {
		emitStatusProgress(stage, "done", extra)
	}
}
