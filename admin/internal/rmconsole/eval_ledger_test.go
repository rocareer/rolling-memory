// plugin: rolling-memory v0.0.58
// eval_ledger_test.go 是测评任务台账（admin SQLite）的库级回归用例（本机 rolling-admin 宿主以
// SQLite 单文件库运行）。
//
// 为什么必须真库：台账的读路径直接打 `plugin_rolling_memory_eval_task*` 两张表，列语义
// （payload 无损回放、suite 列过滤、status 闭集、逐题明细覆盖式写入）只有真库上才验得到；
// 建表走**生产同物**：临时库文件 + 宿主 `database.BootstrapSQLite`（与线上 bootstrap 同一份
// 幂等 DDL），用例不自己抄一份 DDL，避免「测试通过但线上建表不同」。
package rollingmemory

import (
	"bytes"
	"log"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/database"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// setupEvalLedgerDB 在临时 SQLite 文件上建库（与线上 bootstrap 同物）。
func setupEvalLedgerDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "rolling-memory-ledger-test.db") +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.New(sqlite.Config{DriverName: "sqlite", DSN: dsn}), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开临时 SQLite 库失败: %v", err)
	}
	if err := database.BootstrapSQLite(db); err != nil {
		t.Fatalf("BootstrapSQLite: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// evalLedgerFixtureRecord 造一条与 finalizeEvalRunRecord 同形的任务记录。
func evalLedgerFixtureRecord(runID, suite, tier string) EvalRunRecord {
	return EvalRunRecord{
		ID: runID, Suite: suite, Label: "Run · Tier-" + tier, Tier: tier,
		CreatedAt: "2026-11-07 12:00:00", ScoredQuestions: 2,
		Arm: "sdk-pulse-cipher-llm (rl20)", Model: "deepseek-chat", Protocol: FrozenProtocolID,
		EvidenceHit: 0.5, JudgeRate: 1, JudgeCorrect: 2, JudgeScored: 2,
		ElapsedSeconds:   12.5,
		CategoryEvidence: CategoryScores{Cat1: 1, Cat2: 0.5},
		CategoryJudge:    CategoryScores{Cat1: 1, Cat2: 1},
		BucketCounts:     map[string]int{"HIT": 1, "READER_GAP": 1},
		Note:             "fixture", NoteEn: "fixture",
	}
}

// TestEvalLedgerRoundTrip 任务行无损回放：payload 真源 + 投影列（含 suite 过滤与 provenance 列）。
func TestEvalLedgerRoundTrip(t *testing.T) {
	db := setupEvalLedgerDB(t)

	locomo := evalLedgerFixtureRecord("run-20261107-120000", "locomo", "100")
	prov := evalTaskProvenance{
		Scope:        EvalScope,
		Endpoint:     "http://127.0.0.1:8082/open/memory/v1",
		CorpusSHA256: "abc123",
		PurgedUnits:  5882,
	}
	if err := writeEvalTaskRowOn(db, locomo, prov, evalTaskStatusCompleted, ""); err != nil {
		t.Fatalf("写入任务行失败: %v", err)
	}
	atm := evalLedgerFixtureRecord("atm-run-20261107-120000", "atm", "hard")
	if err := writeEvalTaskRowOn(db, atm, evalTaskProvenance{Scope: ATMEvalScope}, evalTaskStatusCompleted, ""); err != nil {
		t.Fatalf("写入 ATM 任务行失败: %v", err)
	}

	// 同 id 覆盖写入（重放幂等）。
	locomo.ScoredQuestions = 3
	if err := writeEvalTaskRowOn(db, locomo, prov, evalTaskStatusCancelled, ""); err != nil {
		t.Fatalf("覆盖写入失败: %v", err)
	}

	all, err := readEvalTaskRowsOn(db, "")
	if err != nil {
		t.Fatalf("读取全部台账失败: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("台账行数 = %d, want 2（同 id 覆盖后不得增行）", len(all))
	}

	var got *EvalRunRecord
	for i := range all {
		if all[i].ID == locomo.ID {
			got = &all[i]
		}
	}
	if got == nil {
		t.Fatalf("未读回 run-20261107-120000：%+v", all)
	}
	if got.ScoredQuestions != 3 || got.Status != evalTaskStatusCancelled {
		t.Fatalf("覆盖写入未生效：scored=%d status=%q", got.ScoredQuestions, got.Status)
	}
	if got.CategoryEvidence.Cat2 != 0.5 || got.BucketCounts["READER_GAP"] != 1 {
		t.Fatalf("结构化字段未无损回放：%+v", got)
	}

	// suite 列过滤：ATM 历史不得混进 LoCoMo 台账（反之亦然）。
	locomoOnly, err := readEvalTaskRowsOn(db, "locomo")
	if err != nil {
		t.Fatalf("按 suite 读取失败: %v", err)
	}
	if len(locomoOnly) != 1 || locomoOnly[0].ID != locomo.ID {
		t.Fatalf("suite 过滤失效：%+v", locomoOnly)
	}

	// provenance 列落库（scope / endpoint / corpus_sha256 / purged_units）。
	var provRow struct {
		Scope        string `gorm:"column:scope"`
		Endpoint     string `gorm:"column:endpoint"`
		CorpusSHA256 string `gorm:"column:corpus_sha256"`
		PurgedUnits  int    `gorm:"column:purged_units"`
	}
	if err := db.Raw(`SELECT "scope","endpoint","corpus_sha256","purged_units" FROM "`+evalTaskTableName+`" WHERE "id" = ?`, locomo.ID).
		Scan(&provRow).Error; err != nil {
		t.Fatalf("读取 provenance 列失败: %v", err)
	}
	if provRow.Scope != EvalScope || provRow.Endpoint != prov.Endpoint ||
		provRow.CorpusSHA256 != "abc123" || provRow.PurgedUnits != 5882 {
		t.Fatalf("provenance 列不符：%+v", provRow)
	}
}

// TestEvalLedgerItemsRoundTrip 逐题明细：覆盖式写入、按题序读回、删除级联。
func TestEvalLedgerItemsRoundTrip(t *testing.T) {
	db := setupEvalLedgerDB(t)
	runID := "run-20261107-130000"
	if err := writeEvalTaskRowOn(db, evalLedgerFixtureRecord(runID, "locomo", "100"), evalTaskProvenance{}, evalTaskStatusCompleted, ""); err != nil {
		t.Fatalf("写入任务行失败: %v", err)
	}
	items := []EvalQuestionResultItem{
		{Index: 2, ID: "q2", Category: "2", EvidenceHit: true, JudgeVerdict: "wrong", QSScore: 0.25, CloudHits: 5,
			EngineTransport: "ramesdk.pulse_cipher.open-memory-v1", EngineScope: EvalScope, TotalMS: 900,
			RetrievalUnitIDs: []string{"u2", "u9"}},
		{Index: 1, ID: "q1", Category: "1", EvidenceHit: true, JudgeVerdict: "correct", QSScore: 1, CloudHits: 3,
			EngineTransport: "ramesdk.pulse_cipher.open-memory-v1", EngineScope: EvalScope, TotalMS: 800,
			RetrievalUnitIDs: []string{"u1"}},
	}
	if err := writeEvalTaskItemRowsOn(db, runID, items); err != nil {
		t.Fatalf("写入逐题明细失败: %v", err)
	}
	// 覆盖式写入：同 run 再写一条，旧行必须被清掉（不是追加）。
	if err := writeEvalTaskItemRowsOn(db, runID, items[:1]); err != nil {
		t.Fatalf("覆盖写入逐题明细失败: %v", err)
	}
	got, err := readEvalTaskItemRowsOn(db, runID)
	if err != nil {
		t.Fatalf("读取逐题明细失败: %v", err)
	}
	if len(got) != 1 || got[0].ID != "q2" {
		t.Fatalf("覆盖写入未生效：%+v", got)
	}
	if got[0].JudgeVerdict != "wrong" || got[0].CloudHits != 5 || len(got[0].RetrievalUnitIDs) != 2 {
		t.Fatalf("逐题明细未无损回放：%+v", got[0])
	}

	// 投影列：correct 由判词派生（wrong ⇒ 0），evidence_hit 布尔落 0/1。
	var proj struct {
		Correct     int `gorm:"column:correct"`
		EvidenceHit int `gorm:"column:evidence_hit"`
	}
	if err := db.Raw(`SELECT "correct","evidence_hit" FROM "`+evalTaskItemTableName+`" WHERE "run_id" = ?`, runID).
		Scan(&proj).Error; err != nil {
		t.Fatalf("读取明细投影列失败: %v", err)
	}
	if proj.Correct != 0 || proj.EvidenceHit != 1 {
		t.Fatalf("明细投影列不符：%+v", proj)
	}

	// 删除级联：任务行 + 明细一次清掉；再删同 id 必须报“未找到”。
	if err := deleteEvalTaskRowsOn(db, runID); err != nil {
		t.Fatalf("删除任务失败: %v", err)
	}
	var count int64
	if err := db.Raw(`SELECT COUNT(*) FROM "`+evalTaskItemTableName+`" WHERE "run_id" = ?`, runID).Scan(&count).Error; err != nil {
		t.Fatalf("统计明细失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("删除后仍残留 %d 条逐题明细", count)
	}
	if err := deleteEvalTaskRowsOn(db, runID); err == nil || !strings.Contains(err.Error(), "未找到") {
		t.Fatalf("重复删除应报未找到，实际: %v", err)
	}
}

// TestEvalLedgerFailedRunsAreHiddenButStored 失败运行：表内留行、不进 run_history 读数面。
func TestEvalLedgerFailedRunsAreHiddenButStored(t *testing.T) {
	db := setupEvalLedgerDB(t)
	failed := evalLedgerFixtureRecord("job-20261107-140000", "locomo", "100")
	failed.ScoredQuestions = 0
	failed.Status = evalTaskStatusFailed
	failed.Error = "rmem eval prepare 失败: 上游不可达"
	if err := writeEvalTaskRowOn(db, failed, evalTaskProvenance{}, evalTaskStatusFailed, failed.Error); err != nil {
		t.Fatalf("写入失败任务行失败: %v", err)
	}
	if err := writeEvalTaskRowOn(db, evalLedgerFixtureRecord("run-20261107-140100", "locomo", "100"), evalTaskProvenance{}, evalTaskStatusCompleted, ""); err != nil {
		t.Fatalf("写入完成任务行失败: %v", err)
	}

	visible, err := readEvalTaskRowsOn(db, "locomo")
	if err != nil {
		t.Fatalf("读取台账失败: %v", err)
	}
	if len(visible) != 1 || visible[0].ID != "run-20261107-140100" {
		t.Fatalf("失败运行不得进读数面：%+v", visible)
	}

	var stored struct {
		Status string `gorm:"column:status"`
		Error  string `gorm:"column:error"`
	}
	if err := db.Raw(`SELECT "status","error" FROM "`+evalTaskTableName+`" WHERE "id" = ?`, failed.ID).
		Scan(&stored).Error; err != nil {
		t.Fatalf("读取失败任务行失败: %v", err)
	}
	if stored.Status != evalTaskStatusFailed || stored.Error == "" {
		t.Fatalf("失败任务行必须留表（status/error）：%+v", stored)
	}
}

// TestEvalLedgerUnavailableFailsClosed 未初始化库：写读都 fail-closed，不伪装成空台账。
func TestEvalLedgerUnavailableFailsClosed(t *testing.T) {
	if _, err := readEvalTaskRowsOn(nil, "locomo"); err == nil {
		t.Fatal("nil 库读取必须 fail-closed")
	}
	if err := writeEvalTaskRowOn(nil, evalLedgerFixtureRecord("run-x", "locomo", "100"), evalTaskProvenance{}, evalTaskStatusCompleted, ""); err == nil {
		t.Fatal("nil 库写入必须 fail-closed")
	}
	if err := deleteEvalTaskRowsOn(nil, "run-x"); err == nil {
		t.Fatal("nil 库删除必须 fail-closed")
	}
}

// TestEvalLedgerLegacyRowsSkipIsAggregated 遗留行（payload 非当前契约、无 id）：
// 跳过必须如实留痕，但日志聚合成一行——列表页轮询读取时，逐行日志会把服务端日志刷满。
func TestEvalLedgerLegacyRowsSkipIsAggregated(t *testing.T) {
	db := setupEvalLedgerDB(t)
	legacy := []string{"cli-atm-hard-20261107t170154z-0007", "cli-locomo-20261107t172342z-09a5"}
	for _, rowID := range legacy {
		if err := db.Exec(
			`INSERT INTO "`+evalTaskTableName+`" ("id","suite","status","payload","create_time","update_time") VALUES (?,?,?,?,?,?)`,
			rowID, "locomo", evalTaskStatusCompleted, `{"arm":"atm-hard-sdk-cloud-llm","evidence_hit":0.2258}`, 1, 1,
		).Error; err != nil {
			t.Fatalf("插入旧格式行失败: %v", err)
		}
	}
	if err := writeEvalTaskRowOn(db, evalLedgerFixtureRecord("run-20261107-150000", "locomo", "100"), evalTaskProvenance{}, evalTaskStatusCompleted, ""); err != nil {
		t.Fatalf("写入任务行失败: %v", err)
	}

	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	rows, err := readEvalTaskRowsOn(db, "")
	if err != nil {
		t.Fatalf("读取台账失败: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "run-20261107-150000" {
		t.Fatalf("旧格式行不得进读数面：%+v", rows)
	}
	logged := buf.String()
	if strings.Count(logged, "[eval-ledger]") != 1 {
		t.Fatalf("跳行日志必须聚合为一行，实际: %q", logged)
	}
	for _, want := range []string{"跳过 2 条", legacy[0]} {
		if !strings.Contains(logged, want) {
			t.Fatalf("聚合日志缺 %q，实际: %q", want, logged)
		}
	}
}
