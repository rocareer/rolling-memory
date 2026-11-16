package rollingmemory

// env/health 流式采集的进度中继（管理控制台侧）。
//
// 契约：`sync status` 子进程在 ROLLING_MEMORY_STATUS_PROGRESS=1 时向 stderr 输出
// JSONL 进度事件（{"progress":{...}}，stdout 的 --json 载荷不变）；这里把 stderr 逐行
// 分流——进度行中继给收集器（由 WebSocket 共享流广播为 env_health_progress 帧），其余行按原
// 语义累积进 stderr 缓冲区供失败诊断。一次性端点（无接收器挂载）不设该环境变量，
// 子进程与既有的零开销行为完全一致。

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/ai-go-hub/ai-go-admin/internal/kit/httpx"
	"github.com/gin-gonic/gin"
)

// statusProgressEnv 是管理后台向 CLI 子进程声明「请输出进度事件」的环境变量名。
const statusProgressEnv = "ROLLING_MEMORY_STATUS_PROGRESS"

// statusProgressLinePrefix 是进度事件行的判别前缀：只有本前缀的行会被中继，
// 其余 stderr 内容（引擎诊断、报错文本）零解析地进入错误缓冲区。
const statusProgressLinePrefix = `{"progress":`

// adminSyncStderrCap 错误缓冲区上限：失败诊断只需尾部少量文本，防引擎长日志吃内存。
const adminSyncStderrCap = 16 << 10

type envHealthProgressKey struct{}

// withEnvHealthProgressSink 把采集进度接收器挂到 context 上；只有 env/health 流式
// 采集路径会挂载，其余调用（一次性端点、暂停/继续等）读到的永远为 nil。
func withEnvHealthProgressSink(ctx context.Context, sink func(map[string]any)) context.Context {
	if sink == nil {
		return ctx
	}
	return context.WithValue(ctx, envHealthProgressKey{}, sink)
}

func envHealthProgressSink(ctx context.Context) func(map[string]any) {
	sink, _ := ctx.Value(envHealthProgressKey{}).(func(map[string]any))
	return sink
}

// relayAdminSyncProgress 逐行读取子进程 stderr：进度事件行解码后交给 sink，其余行
// 累积进 errBuf。引擎的进度事件都是单行 JSON，读取端按行处理即可保持流式。
func relayAdminSyncProgress(r io.Reader, sink func(map[string]any), errBuf *bytes.Buffer) {
	reader := bufio.NewReaderSize(r, 64*1024)
	for {
		line, err := reader.ReadString('\n')
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, statusProgressLinePrefix):
			var event struct {
				Progress map[string]any `json:"progress"`
			}
			if json.Unmarshal([]byte(trimmed), &event) == nil && event.Progress != nil {
				sink(event.Progress)
			}
		case trimmed != "" && errBuf.Len() < adminSyncStderrCap:
			errBuf.WriteString(trimmed)
			errBuf.WriteByte('\n')
		}
		if err != nil {
			return
		}
	}
}

// runAdminSyncCLIStreaming 以「stdout 完整收集 + stderr 边读边中继」的方式运行 status
// 子进程：进度帧即时外发，同时最终产出与批量路径（cmd.Output）保持同一语义。
func runAdminSyncCLIStreaming(cmd *exec.Cmd, sink func(map[string]any)) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return finishAdminSyncCLIResult("status", nil, "", err)
	}
	cmd.Stdout = &stdout
	if err := cmd.Start(); err != nil {
		return finishAdminSyncCLIResult("status", nil, "", err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		relayAdminSyncProgress(stderrPipe, sink, &stderr)
	}()
	waitErr := cmd.Wait()
	wg.Wait()
	return finishAdminSyncCLIResult("status", stdout.Bytes(), stderr.String(), waitErr)
}

// streamHubPeek 只读查询已注册的共享流；不存在时返回 nil —— 与 streamHubFor 的
// 创建语义相区别：显式刷新绝不为一次请求凭空建出没有订阅者的空流。
func streamHubPeek(key streamKey) *streamHub {
	streamHubsMu.Lock()
	defer streamHubsMu.Unlock()
	return streamHubs[key]
}

// envHealthRefreshHandler 控制台「重新检测」入口：有实时面订阅时把强制采集排入共享流，
// 进度帧与最终数据帧都经既有 WebSocket 共享房间送达（N 个页面共享同一次采集、同一条进度）；
// 无订阅时退化为一次性同步采集，直接把载荷带回（此时没有进度通道可推）。
func envHealthRefreshHandler(c *gin.Context) {
	var req struct {
		Harness string `json:"harness"`
	}
	if !bindJSON(c, &req) {
		return
	}
	home, _ := os.UserHomeDir()
	harness := strings.TrimSpace(req.Harness)
	if hub := streamHubPeek(streamKey{kind: streamKindEnvHealth, harness: harness}); hub != nil && hub.hasSubscribers() {
		hub.RequestRefresh()
		httpx.Success(c, httpx.WithData(map[string]any{"queued": true}))
		return
	}
	if harness == "" {
		harness = activeHarnessID(home)
	}
	payload, err := collectEnvHealthPayload(c.Request.Context(), home, harness)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	storeCachedEnvHealthPayload(home, harness, payload)
	httpx.Success(c, httpx.WithData(payload))
}
