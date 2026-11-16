// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli_test

import (
	"strings"
	"testing"
)

// TestRecallStdinBatchServesMultipleRequestsOnOneProcess 钉住 `recall --stdin-batch` 行协议：
//  1. 一个进程按 stdin JSONL 服务多行请求，每行返回一个带 req_id 回显的 JSON 响应；
//  2. 行级坏请求（空 q / 非法 JSON / 越界 raw_limit）只产生错误行，绝不终止整批；
//  3. 命中行结构与单发 `recall --json` 同构（同样携带 hits / meta 键）。
func TestRecallStdinBatchServesMultipleRequestsOnOneProcess(t *testing.T) {
	newEnv(t)
	seedTranscript(t)

	single := run(t, "recall", "--q", "老张", "--json", "--no-crystallize")
	if single.code != 0 {
		t.Fatalf("single recall exit %d: %s%s", single.code, single.stdout, single.stderr)
	}
	singlePayload := jsonObject(t, single.stdout)

	input := strings.Join([]string{
		`{"req_id":"q1","q":"老张"}`,
		`not-json`,
		`{"req_id":"q2","q":""}`,
		`{"req_id":"q3","q":"小白","raw_limit":4}`,
		`{"req_id":"q4","q":"老张","raw_limit":999}`,
	}, "\n") + "\n"
	batch := runInput(t, input, "recall", "--stdin-batch", "--no-crystallize")
	if batch.code != 0 {
		t.Fatalf("batch exit %d: %s%s", batch.code, batch.stdout, batch.stderr)
	}
	lines := strings.Split(strings.TrimSpace(batch.stdout), "\n")
	if len(lines) != 5 {
		t.Fatalf("want 5 response lines, got %d: %s", len(lines), batch.stdout)
	}

	first := jsonObject(t, lines[0])
	if first["req_id"] != "q1" {
		t.Fatalf("line1 req_id = %#v", first["req_id"])
	}
	for _, key := range []string{"bands", "raw_hits", "meta"} {
		if _, ok := first[key]; !ok {
			t.Fatalf("batch hit line misses %q: %s", key, lines[0])
		}
		if _, ok := singlePayload[key]; !ok {
			t.Fatalf("single payload misses %q (isomorphism baseline broken): %s", key, single.stdout)
		}
	}

	if msg, _ := jsonObject(t, lines[1])["error"].(string); !strings.Contains(msg, "invalid batch request line") {
		t.Fatalf("line2 should be a line-level JSON error: %s", lines[1])
	}
	if msg, _ := jsonObject(t, lines[2])["error"].(string); !strings.Contains(msg, "requires q") {
		t.Fatalf("line3 should reject empty q: %s", lines[2])
	}
	third := jsonObject(t, lines[3])
	if third["req_id"] != "q3" {
		t.Fatalf("line4 req_id = %#v", third["req_id"])
	}
	if _, hasErr := third["error"]; hasErr {
		t.Fatalf("line4 should succeed: %s", lines[3])
	}
	if msg, _ := jsonObject(t, lines[4])["error"].(string); !strings.Contains(msg, "raw_limit") {
		t.Fatalf("line5 should reject out-of-range raw_limit: %s", lines[4])
	}
}
