// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rocareer/rolling-memory/internal/config"
)

// writeActiveWorkspacePresence 以 hook 写侧的同一形状落一份 active_workspace.json。
func writeActiveWorkspacePresence(t *testing.T, carrier, session string, updatedAt int64) {
	t.Helper()
	stateDir := config.DefaultStateDir()
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	body, err := json.Marshal(map[string]any{
		"world":      "actual",
		"carrier":    carrier,
		"locus":      "demo-repo",
		"session_id": session,
		"updated_at": updatedAt,
	})
	if err != nil {
		t.Fatalf("marshal presence: %v", err)
	}
	path := filepath.Join(stateDir, "active_workspace.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write presence: %v", err)
	}
}

func runStatusJSON(t *testing.T, dbPath string, extra ...string) map[string]any {
	t.Helper()
	args := append([]string{"sync", "status", "--db", dbPath, "--json"}, extra...)
	res := run(t, args...)
	if res.code != 0 {
		t.Fatalf("sync status exit=%d stdout=%s stderr=%s", res.code, res.stdout, res.stderr)
	}
	return jsonObject(t, res.stdout)
}

func harnessItem(t *testing.T, payload map[string]any, id string) map[string]any {
	t.Helper()
	items, _ := payload["harnesses"].([]any)
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if item["id"] == id {
			return item
		}
	}
	t.Fatalf("harness %q missing from inventory: %v", id, payload["harnesses"])
	return nil
}

// TestSyncStatusResolvesWorkBuddyPresenceToWorkBuddy 锁定宿主激活探测回归：
// presence 文件里的宿主是 workbuddy（WorkBuddy 插件 hook 写入），早期因注册表
// 只有 codebuddy 条目而匹配失败，落进「最近活跃」兜底并把并列 0 分的第 0 位
// （zcode）点亮 —— 在从未安装 ZCode 的机器上显示「ZCode 激活」。
func TestSyncStatusResolvesWorkBuddyPresenceToWorkBuddy(t *testing.T) {
	env := newEnv(t)
	if err := os.MkdirAll(filepath.Join(os.Getenv("HOME"), ".workbuddy"), 0o755); err != nil {
		t.Fatalf("mkdir workbuddy: %v", err)
	}
	writeActiveWorkspacePresence(t, "harness:workbuddy", "sess-workbuddy-1", time.Now().Unix())

	payload := runStatusJSON(t, filepath.Join(env.base, "workbuddy.db"))
	if payload["active_harness_id"] != "workbuddy" {
		t.Fatalf("active_harness_id = %v, want workbuddy", payload["active_harness_id"])
	}
	if payload["active_harness_label"] != "WorkBuddy" {
		t.Fatalf("active_harness_label = %v, want WorkBuddy", payload["active_harness_label"])
	}
	if payload["detection_source"] != "active_workspace.json" {
		t.Fatalf("detection_source = %v, want active_workspace.json", payload["detection_source"])
	}
	workbuddy := harnessItem(t, payload, "workbuddy")
	if workbuddy["active"] != true || workbuddy["detected"] != true {
		t.Fatalf("workbuddy item = %+v, want active+detected", workbuddy)
	}
	if zcode := harnessItem(t, payload, "zcode"); zcode["active"] != false {
		t.Fatalf("zcode must not be active: %+v", zcode)
	}
}

// TestSyncStatusWithoutHarnessEvidenceReportsNone 锁定零证据诚实口径：
// 没有 presence 文件、没有宿主环境变量、没有转录 —— 活跃宿主为「无」，
// 而不是凭硬编码默认值点亮某个宿主。
func TestSyncStatusWithoutHarnessEvidenceReportsNone(t *testing.T) {
	env := newEnv(t)
	payload := runStatusJSON(t, filepath.Join(env.base, "no-evidence.db"))
	if payload["active_harness_id"] != "" || payload["active_harness_label"] != "" {
		t.Fatalf("expected no active harness, got id=%v label=%v", payload["active_harness_id"], payload["active_harness_label"])
	}
	if payload["detection_source"] != "none" {
		t.Fatalf("detection_source = %v, want none", payload["detection_source"])
	}
	items, _ := payload["harnesses"].([]any)
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if item["active"] == true {
			t.Fatalf("harness %v marked active without evidence", item["id"])
		}
	}
}

// TestSyncStatusStalePresenceLosesToFresherTranscript 锁定过期 presence 不再挟持读数：
// 三天前的 zcode presence 对上刚刚写入的 claude-code 转录，活跃宿主应为 claude-code，
// 来源如实记 recent_activity。
func TestSyncStatusStalePresenceLosesToFresherTranscript(t *testing.T) {
	env := newEnv(t)
	writeActiveWorkspacePresence(t, "harness:zcode", "sess-old", time.Now().Add(-72*time.Hour).Unix())

	sessionDir := filepath.Join(os.Getenv("HOME"), ".claude", "projects", "demo")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir claude sessions: %v", err)
	}
	lines := `{"type":"user","sessionId":"s1","timestamp":"2026-11-12T00:00:00","message":{"role":"user","content":[{"type":"text","text":"检查活跃宿主检测。"}]}}` + "\n" +
		`{"type":"assistant","sessionId":"s1","timestamp":"2026-11-12T00:00:10","message":{"role":"assistant","content":[{"type":"text","text":"已修复并复验。"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(sessionDir, "s1.jsonl"), []byte(lines), 0o644); err != nil {
		t.Fatalf("write claude transcript: %v", err)
	}

	payload := runStatusJSON(t, filepath.Join(env.base, "stale.db"))
	if payload["active_harness_id"] != "claude-code" {
		t.Fatalf("active_harness_id = %v, want claude-code", payload["active_harness_id"])
	}
	if payload["detection_source"] != "recent_activity" {
		t.Fatalf("detection_source = %v, want recent_activity", payload["detection_source"])
	}
}

// TestSyncStatusHarnessFilterIsScopeNotEvidence 锁定 `--harness` 只是扫描/展示范围：
// 它可以让过滤目标进入清单，但不得把自身当作「活跃」证据无中生有。
func TestSyncStatusHarnessFilterIsScopeNotEvidence(t *testing.T) {
	env := newEnv(t)
	payload := runStatusJSON(t, filepath.Join(env.base, "filter.db"), "--harness", "workbuddy")
	if payload["active_harness_id"] != "" {
		t.Fatalf("filter fabricated an active harness: %v", payload["active_harness_id"])
	}
	if !func() bool {
		items, _ := payload["harnesses"].([]any)
		for _, raw := range items {
			if item, _ := raw.(map[string]any); item["id"] == "workbuddy" {
				return true
			}
		}
		return false
	}() {
		t.Fatalf("filtered harness missing from inventory: %v", payload["harnesses"])
	}
}

// TestSyncReconcileImportsWorkBuddyJSONLTurns 锁定 16 机器 WorkBuddy 会话同步：
// `~/.workbuddy/projects/<ws>/<conv>.jsonl`（毫秒级 timestamp + input_text/output_text 块）
// 在 `sync status` 与 `sync reconcile` 下可正确计数并导入本地金库。
func TestSyncReconcileImportsWorkBuddyJSONLTurns(t *testing.T) {
	env := newEnv(t)
	dbPath := filepath.Join(env.base, "workbuddy-sync.db")
	projDir := filepath.Join(os.Getenv("HOME"), ".workbuddy", "projects", "Users-demo-WorkBuddy-2026-11-11")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatalf("mkdir workbuddy project: %v", err)
	}
	nowMS := time.Now().Add(-5 * time.Minute).UnixMilli()
	lines := `{"id":"u1","timestamp":` + jsonNumberString(nowMS) + `,"type":"message","role":"user","content":[{"type":"input_text","text":"在 16 机器上同步 WorkBuddy 会话记忆。"}]}` + "\n" +
		`{"id":"a1","timestamp":` + jsonNumberString(nowMS+2000) + `,"type":"message","role":"assistant","content":[{"type":"output_text","text":"WorkBuddy JSONL 会话已成功扫描并入库。"}]}` + "\n"
	if err := os.WriteFile(filepath.Join(projDir, "conv-1.jsonl"), []byte(lines), 0o644); err != nil {
		t.Fatalf("write workbuddy transcript: %v", err)
	}
	writeActiveWorkspacePresence(t, "harness:workbuddy", "conv-1", time.Now().Unix())

	st := runStatusJSON(t, dbPath)
	wb := harnessItem(t, st, "workbuddy")
	if wb["turns_scan_supported"] != true || wb["sessions_count"] != float64(1) || wb["local_turns_count"] != float64(2) || wb["unsynced_turns_count"] != float64(2) {
		t.Fatalf("workbuddy status before reconcile = %+v", wb)
	}

	res := run(t, "sync", "reconcile", "--db", dbPath, "--json")
	if res.code != 0 {
		t.Fatalf("sync reconcile exit=%d stdout=%s stderr=%s", res.code, res.stdout, res.stderr)
	}
	rep := jsonObject(t, res.stdout)
	if rep["target_harness"] != "workbuddy" || rep["episodic_imported_from_harness"] != float64(2) {
		t.Fatalf("workbuddy reconcile report = %+v", rep)
	}
	postSt, _ := rep["status"].(map[string]any)
	wbAfter := harnessItem(t, postSt, "workbuddy")
	if wbAfter["synced_turns_count"] != float64(2) || wbAfter["unsynced_turns_count"] != float64(0) {
		t.Fatalf("workbuddy status after reconcile = %+v", wbAfter)
	}
}

func jsonNumberString(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}
