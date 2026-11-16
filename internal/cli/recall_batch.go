// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/rocareer/rolling-memory/internal/api"
	"github.com/rocareer/rolling-memory/internal/text"
)

// recallBatchRequest 是 `recall --stdin-batch` 的单行请求契约。一个常驻热进程按行服务
// 多次 recall，免去调用方（评测台批量跑分）逐题 fork 子进程的冷启动开销（进程启动 +
// SQLite 冷打开 + 语料快照重建）。行协议刻意极小：除 req_id / q / raw_limit 外全部沿用
// 本次进程启动时的 CLI 参数与默认值，保证与单发 `recall --q ... --json` 路径逐字段同构。
type recallBatchRequest struct {
	ReqID    string `json:"req_id"`
	Q        string `json:"q"`
	RawLimit int    `json:"raw_limit"`
}

// runRecallStdinBatch 在同一个已打开的 Memory 上按 stdin JSONL 循环服务 recall：
// 每行一个请求、每行一个 JSON 响应（成功 = result.ToDict() + req_id 回显；失败 =
// {"req_id","error"}），行级错误不终止进程，stdin EOF 正常退出。
func (a *application) runRecallStdinBatch(memory *api.Memory) (int, error) {
	reader := bufio.NewReaderSize(a.stdin, 1<<20)
	writer := bufio.NewWriter(a.stdout)
	defer writer.Flush()
	// 常驻进程退出前有界等待在途 L3 结晶脉冲，与单发路径同约（评测隔离 scope 本身禁结晶）。
	defer api.WaitForPendingNoeticPulses(api.NoeticPulseBudget)

	// 进程级一次性参数：与单发 runRecall 同源取值，整批所有行共享。
	mode := a.args.String("result_mode")
	offset := a.args.Int("offset")
	depth := a.args.Int("depth")
	deep := a.args.Flag("deep")
	noCrystallize := a.args.Flag("no_crystallize")
	defaultRawLimit := a.args.Int("raw_limit")
	var presence *text.PresenceCoordinates
	if coords := (text.PresenceCoordinates{
		World:       "actual",
		Carrier:     strings.TrimSpace(os.Getenv("ROLLING_MEMORY_PRESENCE_CARRIER")),
		Locus:       strings.TrimSpace(os.Getenv("ROLLING_MEMORY_PRESENCE_LOCUS")),
		Incarnation: strings.TrimSpace(os.Getenv("ROLLING_MEMORY_PRESENCE_INCARNATION")),
	}.Normalized()); !coords.IsEmpty() {
		presence = &coords
	}

	enc := json.NewEncoder(writer)
	enc.SetEscapeHTML(false)
	emitLine := func(payload map[string]any) error {
		if err := enc.Encode(payload); err != nil {
			return err
		}
		return writer.Flush()
	}

	for {
		line, readErr := reader.ReadString('\n')
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if readErr != nil {
				if errors.Is(readErr, io.EOF) {
					return 0, nil
				}
				return 1, readErr
			}
			continue
		}

		payload := map[string]any{}
		var req recallBatchRequest
		if jsonErr := json.Unmarshal([]byte(trimmed), &req); jsonErr != nil {
			payload["error"] = "invalid batch request line: " + jsonErr.Error()
		} else {
			if req.ReqID != "" {
				payload["req_id"] = req.ReqID
			}
			rawLimit := req.RawLimit
			if rawLimit == 0 {
				rawLimit = defaultRawLimit
			}
			switch {
			case rawLimit < 1 || rawLimit > 64:
				payload["error"] = "raw_limit must be between 1 and 64"
			case strings.TrimSpace(req.Q) == "":
				payload["error"] = "recall requires q"
			default:
				result, recallErr := memory.Recall(api.RecallRequest{
					Cue:           req.Q,
					Mode:          mode,
					Offset:        offset,
					Depth:         depth,
					RawLimit:      rawLimit,
					Deep:          deep,
					Presence:      presence,
					NoCrystallize: noCrystallize,
				})
				if recallErr != nil {
					payload["error"] = recallErr.Error()
				} else {
					for key, value := range result.ToDict() {
						payload[key] = value
					}
				}
			}
		}
		if emitErr := emitLine(payload); emitErr != nil {
			return 1, emitErr
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return 0, nil
			}
			return 1, readErr
		}
	}
}
