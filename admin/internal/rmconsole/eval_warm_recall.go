// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package rollingmemory

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// warmEvalRecallSession 维护一个常驻 `recall --stdin-batch` 热子进程为批量评测服务：
// 批次内逐题 recall 不再各自 fork 子进程（进程冷启 + SQLite 冷打开 + 语料快照重建
// 每题重付，单题墙钟被放大一个数量级），而是在同一热进程上按 JSONL 行协议请求/应答。
// 该会话只做读路径（评测隔离 scope 服务端禁结晶），损坏即弃用——调用方回退一次性
// fork 路径重试同一查询，读数口径与兼容路径保持同构。
type warmEvalRecallSession struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	seq    int
	broken bool
}

// startWarmEvalRecallSession 启动一个绑定 (storePath, scope) 的批量 recall 热进程。
// 启动参数与一次性 fork 路径逐项同构（--db/--scope），行内只传 q 与 raw_limit。
func startWarmEvalRecallSession(ctx context.Context, bin, storePath, scope string) (*warmEvalRecallSession, error) {
	if strings.TrimSpace(bin) == "" || strings.TrimSpace(storePath) == "" || strings.TrimSpace(scope) == "" {
		return nil, errors.New("warm recall session requires bin/storePath/scope")
	}
	cmd := exec.CommandContext(ctx, bin, "recall", "--stdin-batch", "--db", storePath, "--scope", scope)
	cmd.Env, _ = brainChildEnv(ctx)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, err
	}
	return &warmEvalRecallSession{cmd: cmd, stdin: stdin, stdout: bufio.NewReaderSize(stdout, 1<<20)}, nil
}

// recall 在热进程上执行一次查询。req_id 单调递增并强校验回显序号，杜绝行错位
// 把上一题的命中安到下一题头上；任何管道/解码/序号异常都把会话标记为 broken。
func (s *warmEvalRecallSession) recall(query string, topK int) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.broken {
		return nil, errors.New("warm recall session broken")
	}
	s.seq++
	reqID := strconv.Itoa(s.seq)
	line, err := json.Marshal(map[string]any{"req_id": reqID, "q": query, "raw_limit": topK})
	if err != nil {
		return nil, err
	}
	if _, err := s.stdin.Write(append(line, '\n')); err != nil {
		s.broken = true
		return nil, err
	}
	respLine, err := s.stdout.ReadBytes('\n')
	if err != nil {
		s.broken = true
		return nil, err
	}
	payload := map[string]any{}
	if err := json.Unmarshal(respLine, &payload); err != nil {
		s.broken = true
		return nil, err
	}
	if got := asString(payload["req_id"]); got != reqID {
		s.broken = true
		return nil, fmt.Errorf("warm recall req_id mismatch: want %s got %s", reqID, got)
	}
	if msg, ok := payload["error"].(string); ok && msg != "" {
		// 行级业务错误（查询被引擎拒绝等）：会话本身健康，错误原样上抛。
		return nil, errors.New(msg)
	}
	delete(payload, "req_id")
	return payload, nil
}

// Close 终止热进程：先关 stdin 触发 EOF 正常退出，异常时兜底杀进程。
func (s *warmEvalRecallSession) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stdin != nil {
		_ = s.stdin.Close()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		done := make(chan struct{})
		go func() { _ = s.cmd.Wait(); close(done) }()
		select {
		case <-done:
		default:
			_ = s.cmd.Process.Kill()
			<-done
		}
	}
}

type warmEvalRecallSessionKey struct{}

// withWarmEvalRecallSession 把 worker 专属热会话注入 ctx：执行器签名不变，
// runStrictCloudEvalRecall 自行探测并优先走热腿。
func withWarmEvalRecallSession(ctx context.Context, s *warmEvalRecallSession) context.Context {
	return context.WithValue(ctx, warmEvalRecallSessionKey{}, s)
}

func warmEvalRecallSessionFrom(ctx context.Context) *warmEvalRecallSession {
	s, _ := ctx.Value(warmEvalRecallSessionKey{}).(*warmEvalRecallSession)
	return s
}
