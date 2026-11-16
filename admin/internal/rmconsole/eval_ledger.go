// plugin: rolling-memory v0.0.58
// eval_ledger.go —— 测评任务台账（admin SQLite）的全部 SQL 原语（机械 SQL，零业务语义）。
//
// 形态：宿主 admin 库两张表（DDL 见 internal/infra/database/sqlite_bootstrap.go 的
// bootstrapRollingMemoryEvalTables，与本文件逐列同源）：
//   - plugin_rolling_memory_eval_task       一次测评运行一行（**全量保留，无条数上限**）；
//   - plugin_rolling_memory_eval_task_item  逐题明细（评分 + 引擎 provenance）。
//
// payload 列是 EvalRunRecord / EvalQuestionResultItem 的无损 JSON（回放真源），名下列是它的投影：
// 列表过滤与 SQL 直查用列，读路径永远以 payload 为准（列只写不读，杜绝两份数据互相漂移）。
// 每次测评运行都会先清空该 scope 的云端记忆再全量重灌（“每次都要重算记忆”），因此**台账是
// 运行历史的唯一留存面**：不做条数上限、不做覆盖式滚动，失败运行也留行（status=failed）。
//
// 分层：`*On` 后缀是接 `*gorm.DB` 的原语（用例在临时 SQLite + 宿主 BootstrapSQLite 上直测），
// 不带后缀的入口取全局 admin 连接（未初始化即 fail-closed，不静默降级成“空台账”）。
//
// 表名不拼前缀：SQLite 形态下 sqlite_bootstrap.go 是建表的唯一事实源，与本插件既有的
// plugin_ai_* 只读查询同口径（本机 admin 的 configs.database.prefix 为空）。
package rollingmemory

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"
	"github.com/ai-go-hub/ai-go-admin/internal/infra/database"

	"gorm.io/gorm"
)

// 台账物理表名（与 BootstrapSQLite 的 DDL 字面量逐字相等）。
const (
	evalTaskTableName     = "plugin_rolling_memory_eval_task"
	evalTaskItemTableName = "plugin_rolling_memory_eval_task_item"
)

// 任务状态闭集（与 DDL 的 CHECK 约束同源）。
const (
	evalTaskStatusCompleted = "completed"
	evalTaskStatusCancelled = "cancelled"
	evalTaskStatusFailed    = "failed"
)

// errEvalLedgerUnavailable 数据库未初始化（只装配路由的测试/嵌入场景）：写读一律 fail-closed，
// 不静默降级成“空台账”——空台账会把历史读成“从未跑过”。
var errEvalLedgerUnavailable = errors.New("测评任务台账不可用：admin 数据库未初始化")

// evalTaskProvenance 是一次运行的运行级证据（评测模块自持的标签，上游不参与）。
type evalTaskProvenance struct {
	Scope        string // 云端隔离 scope（如 u10-mem-eval-locomo）
	Endpoint     string // 数据面端点
	CorpusSHA256 string // 语料摘要（manifest 真值；缺失留空）
	PurgedUnits  int    // 本次重算在重灌前清空的旧记忆条数
}

// evalTaskRow 台账读数行：payload 是无损记录，status 是任务状态（失败运行没有成绩）。
type evalTaskRow struct {
	ID      string `gorm:"column:id"`
	Payload string `gorm:"column:payload"`
	Status  string `gorm:"column:status"`
}

// evalLedger 取宿主 admin 连接；未初始化时 fail-closed（不返回“空台账”）。
func evalLedger() (*gorm.DB, error) {
	db := database.DB()
	if db == nil {
		return nil, errEvalLedgerUnavailable
	}
	return db, nil
}

// writeEvalTaskRowOn 写入 / 覆盖一条测评任务行（同 id 覆盖：id 由运行时刻生成，正常不冲突）。
// rec 的投影列由本函数从同一份 rec 提取，payload 与列永远来自同一次序列化，不产生第二真源。
func writeEvalTaskRowOn(db *gorm.DB, rec EvalRunRecord, prov evalTaskProvenance, status, errMsg string) error {
	if db == nil {
		return errEvalLedgerUnavailable
	}
	runID := strings.TrimSpace(rec.ID)
	if runID == "" {
		return errors.New("测评任务 id 不能为空")
	}
	if status == "" {
		status = evalTaskStatusCompleted
	}
	payload, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("序列化测评任务记录失败: %w", err)
	}
	catEv, err := json.Marshal(rec.CategoryEvidence)
	if err != nil {
		return fmt.Errorf("序列化分项得分失败: %w", err)
	}
	catJdg, err := json.Marshal(rec.CategoryJudge)
	if err != nil {
		return fmt.Errorf("序列化分项得分失败: %w", err)
	}
	buckets, err := json.Marshal(rec.BucketCounts)
	if err != nil {
		return fmt.Errorf("序列化五桶计数失败: %w", err)
	}
	// 空 JSON（nil map）统一落 '{}'，避免 NULL 进入读侧。
	norm := func(raw []byte) string {
		if len(raw) == 0 || string(raw) == "null" {
			return "{}"
		}
		return string(raw)
	}
	now := time.Now().Unix()
	return db.Exec(`INSERT INTO "`+evalTaskTableName+`" (
			"id", "suite", "status", "label", "label_en", "tier", "arm", "model", "protocol",
			"scope", "endpoint", "corpus_sha256", "purged_units",
			"scored_questions", "evidence_hit", "full_evidence_hit", "answer_in_context",
			"judge_rate", "judge_correct", "judge_scored", "elapsed_seconds",
			"avg_recall_ms", "p95_recall_ms", "avg_delivered_tokens", "total_delivered_tokens", "avg_delivered_items",
			"category_evidence", "category_judge", "bucket_counts",
			"note", "note_en", "repro_cli", "error", "payload", "create_time", "update_time"
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT("id") DO UPDATE SET
			"suite"="excluded"."suite", "status"="excluded"."status",
			"label"="excluded"."label", "label_en"="excluded"."label_en",
			"tier"="excluded"."tier", "arm"="excluded"."arm", "model"="excluded"."model",
			"protocol"="excluded"."protocol", "scope"="excluded"."scope",
			"endpoint"="excluded"."endpoint", "corpus_sha256"="excluded"."corpus_sha256",
			"purged_units"="excluded"."purged_units",
			"scored_questions"="excluded"."scored_questions", "evidence_hit"="excluded"."evidence_hit",
			"full_evidence_hit"="excluded"."full_evidence_hit",
			"answer_in_context"="excluded"."answer_in_context",
			"judge_rate"="excluded"."judge_rate", "judge_correct"="excluded"."judge_correct",
			"judge_scored"="excluded"."judge_scored", "elapsed_seconds"="excluded"."elapsed_seconds",
			"avg_recall_ms"="excluded"."avg_recall_ms", "p95_recall_ms"="excluded"."p95_recall_ms",
			"avg_delivered_tokens"="excluded"."avg_delivered_tokens",
			"total_delivered_tokens"="excluded"."total_delivered_tokens",
			"avg_delivered_items"="excluded"."avg_delivered_items",
			"category_evidence"="excluded"."category_evidence", "category_judge"="excluded"."category_judge",
			"bucket_counts"="excluded"."bucket_counts", "note"="excluded"."note",
			"note_en"="excluded"."note_en", "repro_cli"="excluded"."repro_cli",
			"error"="excluded"."error", "payload"="excluded"."payload",
			"update_time"="excluded"."update_time"`,
		runID, normalizeSuite(rec.Suite), status, rec.Label, rec.LabelEn, rec.Tier, rec.Arm,
		rec.Model, rec.Protocol, prov.Scope, prov.Endpoint, prov.CorpusSHA256, prov.PurgedUnits,
		rec.ScoredQuestions, rec.EvidenceHit, rec.FullEvidenceHit, rec.AnswerInContext,
		rec.JudgeRate, rec.JudgeCorrect, rec.JudgeScored, rec.ElapsedSeconds,
		rec.AvgRecallMS, rec.P95RecallMS, rec.AvgDeliveredTokens, rec.TotalDeliveredTokens, rec.AvgDeliveredItems,
		norm(catEv), norm(catJdg), norm(buckets),
		rec.Note, rec.NoteEn, rec.ReproCLI, truncateEvalLedgerText(errMsg, 500),
		string(payload), now, now,
	).Error
}

// readEvalTaskRowsOn 读取台账（suite 为空 = 全部套件），按写入时间倒序（最新在前）。
// 只返回有成绩的运行（completed | cancelled）：失败运行同样留行在表内（status=failed），
// 但不进 run_history——前端默认选中与 A/B 对比都取 run_history 首行，失败行没有成绩会污染读数。
func readEvalTaskRowsOn(db *gorm.DB, suite string) ([]EvalRunRecord, error) {
	if db == nil {
		return nil, errEvalLedgerUnavailable
	}
	query := `SELECT "id" AS id, "payload" AS payload, "status" AS status FROM "` + evalTaskTableName + `" WHERE "status" <> ?`
	args := []any{evalTaskStatusFailed}
	if want := strings.TrimSpace(suite); want != "" {
		query += ` AND "suite" = ?`
		args = append(args, normalizeSuite(want))
	}
	query += ` ORDER BY "create_time" DESC, "id" DESC`
	rows := []evalTaskRow{}
	if err := db.Raw(query, args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("读取测评任务台账失败: %w", err)
	}
	out := make([]EvalRunRecord, 0, len(rows))
	skippedIDs := make([]string, 0, 4)
	skipped := 0
	for _, row := range rows {
		var rec EvalRunRecord
		if strings.TrimSpace(row.Payload) == "" || json.Unmarshal([]byte(row.Payload), &rec) != nil || strings.TrimSpace(rec.ID) == "" {
			// 单行损坏不吞掉整段历史：如实记录并跳过（写侧 payload 由本插件生成，正常不会走到这里）。
			// 汇总成一行，避免每次读取（列表页轮询）把同一批遗留行刷满日志。
			skipped++
			if len(skippedIDs) < 4 {
				skippedIDs = append(skippedIDs, row.ID)
			}
			continue
		}
		if rec.Status == "" {
			rec.Status = row.Status
		}
		out = append(out, rec)
	}
	if skipped > 0 {
		logSkippedEvalRowsOnce(skipped, skippedIDs)
	}
	return out, nil
}

var (
	evalLedgerSkippedLogMu  sync.Mutex
	evalLedgerLastSkippedID string
)

func logSkippedEvalRowsOnce(skipped int, skippedIDs []string) {
	sig := fmt.Sprintf("%d:%s", skipped, strings.Join(skippedIDs, ","))
	evalLedgerSkippedLogMu.Lock()
	same := evalLedgerLastSkippedID == sig
	if !same {
		evalLedgerLastSkippedID = sig
	}
	evalLedgerSkippedLogMu.Unlock()
	if !same {
		log.Printf("[eval-ledger] 跳过 %d 条非当前 payload 契约的历史测评任务行（样例 id: %s）", skipped, strings.Join(skippedIDs, ", "))
	}
}

// writeEvalTaskItemRowsOn 覆盖式写入一场运行的逐题明细（先清后插，重放 / 重跑幂等）。
// items 为空时不写（真空运行不该在明细表留下空壳行）。
func writeEvalTaskItemRowsOn(db *gorm.DB, runID string, items []EvalQuestionResultItem) error {
	runID = strings.TrimSpace(runID)
	if runID == "" || len(items) == 0 {
		return nil
	}
	if db == nil {
		return errEvalLedgerUnavailable
	}
	now := time.Now().Unix()
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`DELETE FROM "`+evalTaskItemTableName+`" WHERE "run_id" = ?`, runID).Error; err != nil {
			return fmt.Errorf("清理旧逐题明细失败: %w", err)
		}
		for idx, item := range items {
			payload, err := json.Marshal(item)
			if err != nil {
				return fmt.Errorf("序列化逐题明细失败: %w", err)
			}
			seq := item.Index
			if seq <= 0 {
				seq = idx + 1
			}
			correct := 0
			if strings.EqualFold(strings.TrimSpace(item.JudgeVerdict), "correct") {
				correct = 1
			}
			evidenceHit := 0
			if item.EvidenceHit {
				evidenceHit = 1
			}
			if err := tx.Exec(`INSERT INTO "`+evalTaskItemTableName+`" (
					"run_id", "seq", "question_id", "category", "correct", "evidence_hit", "qs_score",
					"engine_transport", "engine_scope", "cloud_hits",
					"recall_ms", "delivered_tokens", "delivered_items", "total_ms", "payload", "create_time"
				) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				runID, seq, item.ID, item.Category, correct, evidenceHit, item.QSScore,
				item.EngineTransport, item.EngineScope, item.CloudHits,
				item.RecallMS, item.DeliveredTokens, item.DeliveredItems, item.TotalMS, string(payload), now,
			).Error; err != nil {
				return fmt.Errorf("写入逐题明细失败（第 %d 题）: %w", seq, err)
			}
		}
		return nil
	})
}

// readEvalTaskItemRowsOn 读取一场运行的逐题明细（按题序升序）。查无此行返回空切片（不是错误）。
func readEvalTaskItemRowsOn(db *gorm.DB, runID string) ([]EvalQuestionResultItem, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return []EvalQuestionResultItem{}, nil
	}
	if db == nil {
		return nil, errEvalLedgerUnavailable
	}
	type detailRow struct {
		Payload string `gorm:"column:payload"`
	}
	rows := []detailRow{}
	if err := db.Raw(`SELECT "payload" AS payload FROM "`+evalTaskItemTableName+`" WHERE "run_id" = ? ORDER BY "seq" ASC, "id" ASC`, runID).
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("读取逐题明细失败: %w", err)
	}
	out := make([]EvalQuestionResultItem, 0, len(rows))
	for _, row := range rows {
		var item EvalQuestionResultItem
		if strings.TrimSpace(row.Payload) == "" || json.Unmarshal([]byte(row.Payload), &item) != nil {
			log.Printf("[eval-ledger] 跳过损坏的逐题明细行 run=%s", runID)
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

// deleteEvalTaskRowsOn 删除一场运行（任务行 + 逐题明细），事务内一次完成。
// 权威封印基线不在表内（内置代码台账）⇒ 查无此行按“未找到（权威基线不可删除）”返回。
func deleteEvalTaskRowsOn(db *gorm.DB, runID string) error {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return errors.New("run_id 不能为空")
	}
	if db == nil {
		return errEvalLedgerUnavailable
	}
	return db.Transaction(func(tx *gorm.DB) error {
		res := tx.Exec(`DELETE FROM "`+evalTaskTableName+`" WHERE "id" = ?`, runID)
		if res.Error != nil {
			return fmt.Errorf("删除测评任务失败: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return errors.New("未找到指定的历史运行记录（权威基线不可删除）")
		}
		if err := tx.Exec(`DELETE FROM "`+evalTaskItemTableName+`" WHERE "run_id" = ?`, runID).Error; err != nil {
			return fmt.Errorf("删除逐题明细失败: %w", err)
		}
		return nil
	})
}

// ---------------- 全局入口（取宿主 admin 连接） ----------------

// saveEvalTaskRecord 写入 / 覆盖一条测评任务行（completed | cancelled | failed 三态）。
func saveEvalTaskRecord(rec EvalRunRecord, prov evalTaskProvenance, status, errMsg string) error {
	db, err := evalLedger()
	if err != nil {
		return err
	}
	return writeEvalTaskRowOn(db, rec, prov, status, errMsg)
}

// loadEvalTaskHistory 读取台账（suite 为空 = 全部套件），只含有成绩的运行，最新在前。
func loadEvalTaskHistory(suite string) ([]EvalRunRecord, error) {
	db, err := evalLedger()
	if err != nil {
		return nil, err
	}
	return readEvalTaskRowsOn(db, suite)
}

// saveEvalTaskItems 覆盖式写入一场运行的逐题明细。
func saveEvalTaskItems(runID string, items []EvalQuestionResultItem) error {
	db, err := evalLedger()
	if err != nil {
		return err
	}
	return writeEvalTaskItemRowsOn(db, runID, items)
}

// loadEvalTaskItems 读取一场运行的逐题明细（查无返回空切片）。
func loadEvalTaskItems(runID string) ([]EvalQuestionResultItem, error) {
	db, err := evalLedger()
	if err != nil {
		return nil, err
	}
	return readEvalTaskItemRowsOn(db, runID)
}

// deleteEvalTaskRecord 删除一场运行（任务行 + 逐题明细，事务内一次完成）。
func deleteEvalTaskRecord(runID string) error {
	db, err := evalLedger()
	if err != nil {
		return err
	}
	return deleteEvalTaskRowsOn(db, runID)
}

// evalLedgerDBPath 返回 admin SQLite 的文件路径（供实时面指纹采样：台账写入即指纹变化）。
// 与宿主 internal/infra/database 的 SQLite 缺省路径同源；非 SQLite（PG）部署下这里退回缺省路径——
// 指纹只影响实时面刷新时机，不影响任何读数。
func evalLedgerDBPath(home string) string {
	if dbPath := strings.TrimSpace(config.Get().Database.Write.DBName); dbPath != "" &&
		dbPath != "aigo" && dbPath != "data/rolling-admin.db" {
		if strings.HasPrefix(dbPath, "~/") {
			return filepath.Join(home, strings.TrimPrefix(dbPath, "~/"))
		}
		return dbPath
	}
	return filepath.Join(home, ".local", "share", "rolling-memory", "admin", "rolling-admin.db")
}

// truncateEvalLedgerText 按字符截断台账文本列（错误摘要列宽 500；schema 不受影响）。
func truncateEvalLedgerText(text string, limit int) string {
	trimmed := strings.TrimSpace(text)
	if limit <= 0 {
		return trimmed
	}
	runes := []rune(trimmed)
	if len(runes) <= limit {
		return trimmed
	}
	return string(runes[:limit])
}
