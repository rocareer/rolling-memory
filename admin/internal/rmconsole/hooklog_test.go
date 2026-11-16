// plugin: rolling-memory v0.0.58
package rollingmemory

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestHookLogRecallOnlyAndScopeAttribution 钉住日志腿的三条口径：
// ① 只有 recall 行进「召回」腿（start/close 不计）；② 带 scope 的行只进对应 scope；
// ③ 未带 scope 的历史行计入所有 scope 视图（滚动窗口内自然收敛）。
func TestHookLogRecallOnlyAndScopeAttribution(t *testing.T) {
	hookLogMu.Lock()
	hookLogCache = map[string]*hookLogStat{}
	hookLogMu.Unlock()

	dir := t.TempDir()
	logPath := filepath.Join(dir, "rolling-memory.log")
	slot := time.Now().Unix() - time.Now().Unix()%hookLogSlotSec
	lines := []string{
		fmt.Sprintf(`{"event": "recall", "injected": 1, "scope": "mem:self", "ms": 120, "ts": %d}`, slot+10),
		fmt.Sprintf(`{"event": "recall", "injected": 1, "scope": "mem:project:other", "ms": 140, "ts": %d}`, slot+20),
		fmt.Sprintf(`{"event": "start", "imported": 1, "scope": "mem:self", "ts": %d}`, slot+30),
		fmt.Sprintf(`{"event": "close", "written": 1, "scope": "mem:self", "ts": %d}`, slot+40),
		fmt.Sprintf(`{"event": "recall", "injected": 0, "ts": %d}`, slot+50),
		fmt.Sprintf(`{"event": "recall", "injected": 1, "scope": "mem:self", "ts": %d}`, time.Now().Unix()+3600),
	}
	if err := os.WriteFile(logPath, []byte(joinLines(lines)), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	stat := loadHookLogStat(logPath)
	if stat.Total != len(lines) {
		t.Fatalf("total = %d, want %d", stat.Total, len(lines))
	}
	if stat.Injected != 4 {
		t.Fatalf("injected = %d, want 4（两条 recall 注入 + close 的 written + 未来行的注入，均按行计）", stat.Injected)
	}
	if got := stat.recallSlot("mem:self", slot); got != 2 {
		t.Fatalf("mem:self slot = %d, want 2（本 scope 1 行 + 未归属 1 行）", got)
	}
	if got := stat.recallSlot("mem:project:other", slot); got != 2 {
		t.Fatalf("mem:project:other slot = %d, want 2（本 scope 1 行 + 未归属 1 行）", got)
	}
	if got := stat.recallSlot("ALL", slot); got != 3 {
		t.Fatalf("ALL slot = %d, want 3（三种 recall 行，横跨两个 scope + 未归属）", got)
	}
	if got := stat.hookSlot(slot); got != 5 {
		t.Fatalf("hookSlot = %d, want 5（全部事件行；未来行不入桶）", got)
	}
	if got := stat.recallSlot("mem:self", slot+hookLogSlotSec); got != 0 {
		t.Fatalf("future slot = %d, want 0（未来时间戳不入桶）", got)
	}

	// (size, mtime) 未变 ⇒ 命中缓存；追加新行 ⇒ 重扫并计入新槽
	cached := loadHookLogStat(logPath)
	if cached != stat {
		t.Fatal("expected cached aggregate for unchanged log")
	}
	extra := fmt.Sprintf(`{"event": "recall", "injected": 1, "scope": "mem:self", "ts": %d}`, slot+slotInc())
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	if _, err := f.WriteString(extra + "\n"); err != nil {
		t.Fatalf("append log: %v", err)
	}
	_ = f.Close()
	reloaded := loadHookLogStat(logPath)
	if reloaded == stat {
		t.Fatal("expected rescan after log append")
	}
	if got := reloaded.recallSlot("mem:self", slot); got != 3 {
		t.Fatalf("mem:self slot after append = %d, want 3", got)
	}
}

// slotInc 返回一个仍落在当前 5 分钟槽内的偏移（避免踩跨槽边界）。
func slotInc() int64 {
	return time.Now().Unix()%hookLogSlotSec - 1
}

func joinLines(lines []string) string {
	out := ""
	for _, line := range lines {
		out += line + "\n"
	}
	return out
}

// TestDashboardFileSignatureDetectsChanges 验证 SSE 流的 250ms 文件指纹探针：
// WAL 写入、日志追加、5 分钟时钟槽跨越均能立即改变签名，无变化时签名恒定。
func TestDashboardFileSignatureDetectsChanges(t *testing.T) {
	home := t.TempDir()
	dbPath := filepath.Join(home, "memory.db")
	logPath := filepath.Join(home, "rolling-memory.log")
	now := time.Unix(1793360000, 0)

	sig0 := dashboardFileSignature(home, dbPath, logPath, now)
	if sig0 != dashboardFileSignature(home, dbPath, logPath, now) {
		t.Fatal("signature must be deterministic when files are unchanged")
	}

	if err := os.WriteFile(dbPath+"-wal", []byte("wal-frame-1"), 0o644); err != nil {
		t.Fatalf("write wal: %v", err)
	}
	sig1 := dashboardFileSignature(home, dbPath, logPath, now)
	if sig1 == sig0 {
		t.Fatal("expected signature to change after WAL write")
	}

	if err := os.WriteFile(logPath, []byte(`{"event":"recall","ts":1793360001}`+"\n"), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	sig2 := dashboardFileSignature(home, dbPath, logPath, now)
	if sig2 == sig1 {
		t.Fatal("expected signature to change after hook log write")
	}

	sigNextSlot := dashboardFileSignature(home, dbPath, logPath, now.Add(5*time.Minute))
	if sigNextSlot == sig2 {
		t.Fatal("expected signature to change when crossing 5-minute bucket boundary")
	}
}

// TestCollectDashboardData_MCPLedgerIgnoresRetiredHookNoise 验证当事实账本（.efficacy.sqlite）
// 存在真实 MCP recall 记录时，历史钩子日志（start/close 停用钩子与 UserPromptSubmit 节律空跑）
// 不会掺入 TotalHookEvents / InjectedEvents / InjectRatio 与召回趋势图。
func TestCollectDashboardData_MCPLedgerIgnoresRetiredHookNoise(t *testing.T) {
	home := t.TempDir()
	logDir := filepath.Join(home, ".local", "state", "rolling-memory")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("mkdir logDir: %v", err)
	}
	shareDir := filepath.Join(home, ".local", "share", "rolling-memory")
	if err := os.MkdirAll(shareDir, 0o755); err != nil {
		t.Fatalf("mkdir shareDir: %v", err)
	}
	dbPath := filepath.Join(shareDir, "memory.db")
	db, err := openWritableVaultDB(dbPath)
	if err != nil {
		t.Fatalf("openWritableVaultDB: %v", err)
	}
	_ = db.Close()

	nowUnix := time.Now().Unix()
	slot := nowUnix - nowUnix%hookLogSlotSec

	// 写入大量停用 start/close 与 absent=1 的节律钩子噪声到 rolling-memory.log
	logPath := filepath.Join(logDir, "rolling-memory.log")
	noiseLines := []string{
		fmt.Sprintf(`{"event": "start", "imported": 0, "scope": "default", "ts": %d}`, slot+1),
		fmt.Sprintf(`{"event": "close", "written": 0, "scope": "default", "ts": %d}`, slot+2),
		fmt.Sprintf(`{"event": "recall", "injected": 0, "absent": 1, "cadence": 1, "scope": "default", "ts": %d}`, slot+3),
		fmt.Sprintf(`{"event": "recall", "injected": 0, "absent": 1, "scope": "default", "ts": %d}`, slot+4),
	}
	if err := os.WriteFile(logPath, []byte(joinLines(noiseLines)), 0o644); err != nil {
		t.Fatalf("write noise log: %v", err)
	}

	// 建立事实账本并写入 4 次真实 MCP recall（3 次 hits，1 次 empty）以及 2 条 hook='recall' 节律行
	ledgerPath := dbPath + ".efficacy.sqlite"
	ldb, err := openVaultWriteOnly(ledgerPath)
	if err != nil {
		t.Fatalf("open ledger db: %v", err)
	}
	_, err = ldb.Exec(`CREATE TABLE efficacy_event (
		seq INTEGER PRIMARY KEY AUTOINCREMENT,
		event_id TEXT NOT NULL UNIQUE,
		execution_id TEXT NOT NULL,
		session TEXT NOT NULL,
		scope TEXT NOT NULL,
		harness TEXT NOT NULL,
		hook TEXT NOT NULL,
		output_outcome TEXT NOT NULL,
		action TEXT NOT NULL,
		target_event_id TEXT NOT NULL,
		observed_at INTEGER NOT NULL,
		observation TEXT NOT NULL,
		text_sha256 TEXT NOT NULL,
		text_bytes INTEGER
	);`)
	if err != nil {
		_ = ldb.Close()
		t.Fatalf("create efficacy_event: %v", err)
	}
	insertEv := func(id, hook, action, outcome string, tsSec int64, bytes int) {
		_, iErr := ldb.Exec(`INSERT INTO efficacy_event(event_id, execution_id, session, scope, harness, hook, output_outcome, action, target_event_id, observed_at, observation, text_sha256, text_bytes)
			VALUES (?, 'ex', 's1', 'default', 'zcode', ?, ?, ?, '', ?, 'observed', 'h', ?);`,
			id, hook, outcome, action, tsSec*1_000_000_000, bytes)
		if iErr != nil {
			t.Fatalf("insert event %s: %v", id, iErr)
		}
	}
	insertEv("e1", "mcp_recall:full", "recall", "hits", slot+10, 1200)
	insertEv("e2", "mcp_recall:full", "recall", "hits", slot+20, 980)
	insertEv("e3", "mcp_recall:probe", "recall", "hits", slot+30, 640)
	insertEv("e4", "mcp_recall:full", "recall", "empty", slot+40, 0)
	// UserPromptSubmit 钩子在账本里的节律行不应计入 MCP 召回触发
	insertEv("h1", "recall", "decision", "empty", slot+50, 0)
	insertEv("h2", "recall", "output_attempt", "success", slot+60, 120)
	_ = ldb.Close()

	resp := collectDashboardData(home, "default")
	if resp.TotalHookEvents != 4 || resp.InjectedEvents != 3 || resp.InjectRatio != 75.0 {
		t.Fatalf("expected MCP ledger stats total=4 hits=3 ratio=75.0, got total=%d hits=%d ratio=%v",
			resp.TotalHookEvents, resp.InjectedEvents, resp.InjectRatio)
	}
	if got1H := sumInts(resp.Trend1H.Recalls); got1H != 4 {
		t.Fatalf("expected Trend1H.Recalls sum=4 (pure MCP recalls), got %d (%v)", got1H, resp.Trend1H.Recalls)
	}
	if len(resp.RecentEvents) != 4 {
		t.Fatalf("expected 4 recent MCP recall events without retired hook noise, got %d: %+v", len(resp.RecentEvents), resp.RecentEvents)
	}
}

// TestRecallLedgerMergesRenamedHistoricalScope 钉住默认视图的历史联读口径：个人默认记忆空间
// 更名前入库的历史召回行（旧作用域名）必须与 default / mem:self 合并计数，否则更名前的
// 全部历史召回会从 KPI 与趋势读数里消失；显式按旧名查询时只回该名下的行。
func TestRecallLedgerMergesRenamedHistoricalScope(t *testing.T) {
	home := t.TempDir()
	shareDir := filepath.Join(home, ".local", "share", "rolling-memory")
	if err := os.MkdirAll(shareDir, 0o755); err != nil {
		t.Fatalf("mkdir shareDir: %v", err)
	}
	dbPath := filepath.Join(shareDir, "memory.db")
	db, err := openWritableVaultDB(dbPath)
	if err != nil {
		t.Fatalf("openWritableVaultDB: %v", err)
	}
	_ = db.Close()

	ledgerPath := dbPath + ".efficacy.sqlite"
	ldb, err := openPlainSQLite(ledgerPath)
	if err != nil {
		t.Fatalf("open ledger db: %v", err)
	}
	if _, err := ldb.Exec(`CREATE TABLE efficacy_event (
		seq INTEGER PRIMARY KEY AUTOINCREMENT,
		event_id TEXT NOT NULL UNIQUE,
		execution_id TEXT NOT NULL,
		session TEXT NOT NULL,
		scope TEXT NOT NULL,
		harness TEXT NOT NULL,
		hook TEXT NOT NULL,
		output_outcome TEXT NOT NULL,
		action TEXT NOT NULL,
		target_event_id TEXT NOT NULL,
		observed_at INTEGER NOT NULL,
		observation TEXT NOT NULL,
		text_sha256 TEXT NOT NULL,
		text_bytes INTEGER
	);`); err != nil {
		_ = ldb.Close()
		t.Fatalf("create efficacy_event: %v", err)
	}
	nowSec := time.Now().Unix()
	insert := func(id, scope string, tsSec int64) {
		_, iErr := ldb.Exec(`INSERT INTO efficacy_event(event_id, execution_id, session, scope, harness, hook, output_outcome, action, target_event_id, observed_at, observation, text_sha256, text_bytes)
			VALUES (?, 'ex', 's1', ?, 'zcode', 'mcp_recall:full', 'hits', 'recall', '', ?, 'observed', 'h', 100);`,
			id, scope, tsSec*1_000_000_000)
		if iErr != nil {
			t.Fatalf("insert event %s: %v", id, iErr)
		}
	}
	insert("renamed", legacyRenamedScope, nowSec-3600)
	insert("legacy", LegacyUserScope, nowSec-1800)
	insert("current", UserScope, nowSec-600)
	_ = ldb.Close()

	stat := loadRecallLedgerStat(home, dbPath, UserScope, nowSec-30*86400)
	if stat.totalMCPRecalls != 3 || stat.hitMCPRecalls != 3 {
		t.Fatalf("default view must merge renamed historical scope: total=%d hits=%d, want 3/3",
			stat.totalMCPRecalls, stat.hitMCPRecalls)
	}
	onlyRenamed := loadRecallLedgerStat(home, dbPath, legacyRenamedScope, nowSec-30*86400)
	if onlyRenamed.totalMCPRecalls != 1 {
		t.Fatalf("explicit renamed-scope view = %d, want 1（不做反向扩张）", onlyRenamed.totalMCPRecalls)
	}
}

// TestRecallLedgerLatencyExcludesFailedCalls 钉住「近 20 次滑动均值」的成功样本口径：
// 失败/超时（status=error）的墙钟不代表检索耗时，被剔除出延迟序列；成功完成的最近调用
// 仍按时间序（旧→新）进入序列。
func TestRecallLedgerLatencyExcludesFailedCalls(t *testing.T) {
	home := t.TempDir()
	shareDir := filepath.Join(home, ".local", "share", "rolling-memory")
	if err := os.MkdirAll(shareDir, 0o755); err != nil {
		t.Fatalf("mkdir shareDir: %v", err)
	}
	dbPath := filepath.Join(shareDir, "memory.db")
	db, err := openWritableVaultDB(dbPath)
	if err != nil {
		t.Fatalf("openWritableVaultDB: %v", err)
	}
	_ = db.Close()

	// 账本只需存在即可：延迟腿读宿主工具流水，不依赖账本行。
	ldb, err := openPlainSQLite(dbPath + ".efficacy.sqlite")
	if err != nil {
		t.Fatalf("open ledger db: %v", err)
	}
	if _, err := ldb.Exec(`CREATE TABLE efficacy_event (
		seq INTEGER PRIMARY KEY AUTOINCREMENT,
		event_id TEXT NOT NULL UNIQUE,
		execution_id TEXT NOT NULL,
		session TEXT NOT NULL,
		scope TEXT NOT NULL,
		harness TEXT NOT NULL,
		hook TEXT NOT NULL,
		output_outcome TEXT NOT NULL,
		action TEXT NOT NULL,
		target_event_id TEXT NOT NULL,
		observed_at INTEGER NOT NULL,
		observation TEXT NOT NULL,
		text_sha256 TEXT NOT NULL,
		text_bytes INTEGER
	);`); err != nil {
		_ = ldb.Close()
		t.Fatalf("create efficacy_event: %v", err)
	}
	_ = ldb.Close()

	zdb, err := openPlainSQLite(filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"))
	if err != nil {
		t.Fatalf("open zcode db: %v", err)
	}
	if _, err := zdb.Exec(`CREATE TABLE tool_usage (
		tool_name TEXT, started_at INTEGER, completed_at INTEGER, output_bytes INTEGER, status TEXT
	)`); err != nil {
		_ = zdb.Close()
		t.Fatalf("create tool_usage: %v", err)
	}
	nowMS := time.Now().UnixMilli()
	insertUsage := func(startedAgoMS, durMS int64, status string) {
		_, iErr := zdb.Exec(`INSERT INTO tool_usage(tool_name, started_at, completed_at, output_bytes, status)
			VALUES ('mcp__rolling-memory__recall', ?, ?, 0, ?)`,
			nowMS-startedAgoMS, nowMS-startedAgoMS+durMS, status)
		if iErr != nil {
			t.Fatalf("insert usage: %v", iErr)
		}
	}
	insertUsage(40000, 100, "completed")
	insertUsage(30000, 30000, "error") // 30s 超时
	insertUsage(20000, 9, "error")     // 瞬时失败
	insertUsage(10000, 200, "completed")
	_ = zdb.Close()

	stat := loadRecallLedgerStat(home, dbPath, UserScope, time.Now().Unix()-3600)
	if len(stat.latencies) != 2 || stat.latencies[0] != 100 || stat.latencies[1] != 200 {
		t.Fatalf("latencies = %v, want [100 200]（失败/超时行不得进入滑动均值）", stat.latencies)
	}
}

// TestHookLogDefaultViewMergesRenamedScope 钉住日志腿默认视图的同口径联读：旧作用域名的
// 历史 recall 行与 default / mem:self / 未归属行一并计入默认视图。
func TestHookLogDefaultViewMergesRenamedScope(t *testing.T) {
	hookLogMu.Lock()
	hookLogCache = map[string]*hookLogStat{}
	hookLogMu.Unlock()

	dir := t.TempDir()
	logPath := filepath.Join(dir, "rolling-memory.log")
	slot := time.Now().Unix() - time.Now().Unix()%hookLogSlotSec
	lines := []string{
		fmt.Sprintf(`{"event": "recall", "injected": 1, "scope": %q, "ms": 110, "ts": %d}`, legacyRenamedScope, slot+10),
		fmt.Sprintf(`{"event": "recall", "injected": 1, "scope": "mem:self", "ms": 120, "ts": %d}`, slot+20),
		fmt.Sprintf(`{"event": "recall", "injected": 1, "scope": "default", "ms": 130, "ts": %d}`, slot+30),
	}
	if err := os.WriteFile(logPath, []byte(joinLines(lines)), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	stat := loadHookLogStat(logPath)
	if got := stat.recallSlot(UserScope, slot); got != 3 {
		t.Fatalf("default view slot = %d, want 3（旧名 + mem:self + default 合并联读）", got)
	}
	if got := stat.recallSlot(legacyRenamedScope, slot); got != 1 {
		t.Fatalf("renamed-scope direct view = %d, want 1", got)
	}
	// 日桶按键由逐行时间戳推导（避免测试恰逢午夜跨日时误判）。
	lastDay := time.Unix(slot+30, 0).In(time.Local).Format("2006-01-02")
	wantLastDay := 0
	for _, ts := range []int64{slot + 10, slot + 20, slot + 30} {
		if time.Unix(ts, 0).In(time.Local).Format("2006-01-02") == lastDay {
			wantLastDay++
		}
	}
	if got := stat.recallDay(UserScope, lastDay); got != wantLastDay {
		t.Fatalf("default view day = %d, want %d", got, wantLastDay)
	}
}
