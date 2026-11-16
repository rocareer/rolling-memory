// plugin: ai v0.2.0
// sqlite_store_test.go 是 SQLite 方言路径的库级回归用例（本机 rolling-admin 宿主以 SQLite 单文件库运行）。
//
// 为什么必须真库：GORM 的**自动加引号**行为只在真库上暴露——
//   - Group(name)：单标识符走 WriteQuoted 自动加引号 ⇒ 传「已带引号的列名」会二次包裹
//     （SQLite 生成 `"model_id"` ⇒ no such column: "model_id"）；
//   - Pluck(column)：同上；
//   - SQLite 无 `now()` / `GREATEST()` / `->>` / `::jsonb` / `FILTER` 之外的 PG 语法差异。
//
// 内存假库与 DryRun 都抓不到（本文件的存在就是因为这两个缺陷只在真库上现形）。
//
// 建表走**生产同物**：临时库文件 + 宿主的 `database.BootstrapSQLite`（与线上 bootstrap 同一份
// 幂等 DDL：本插件 9 张表 + 菜单/台账播种），用例不自己抄一份 DDL，避免「测试通过但线上建表不同」。
package ai

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/database"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// setupSQLiteStore 在临时 SQLite 文件上建库并返回 Store。
//
// 不走全局 `database.Init()`：`config.Get()` 返回**副本**，用例改不动全局配置（改了不生效，
// 且会把库落到进程 CWD 的 data/ 里），直连临时文件才是可预期的隔离形态。
func setupSQLiteStore(t *testing.T) *Store {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "plugin-ai-test.db") +
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
	return NewStore(db)
}

// seedSQLiteFixtures 播一条渠道 / 逻辑模型 / 绑定（与线上「AI 路由」页最小可用配置同形）。
func seedSQLiteFixtures(t *testing.T, store *Store) (channelID int64, modelID uint64) {
	t.Helper()
	ctx := context.Background()
	channel := &Channel{
		Name: "sqlite-ch", Vendor: "deepseek", BaseURL: "https://api.deepseek.com/v1",
		APIKey: "sk-sqlite-fixture", Model: "deepseek-chat", Timeout: 60,
		Priority: 1, Weight: 1, Status: StatusEnabled, Circuit: CircuitClosed,
	}
	if err := store.CreateChannel(ctx, channel); err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	model := &Model{
		Name: "deepseek-chat", Type: ModelTypeChat, Strategy: StrategyPriority,
		Status: StatusEnabled, PriceInput: "2.000000", PriceOutput: "8.000000",
		Capabilities: []string{"chat", "function_calling"},
	}
	if err := store.CreateModel(ctx, model); err != nil {
		t.Fatalf("CreateModel: %v", err)
	}
	return channel.ID, model.ID
}

// TestSQLiteStorePaths 覆盖本机 SQLite 方言下**曾经报错或易错**的读路径：
// 绑定替换（Pluck）、渠道/模型/厂商/日志列表（模糊匹配）、渠道健康统计（FILTER）、
// 用量五维聚合（GROUP BY 引号与 strftime 切日）、总览聚合、用量筛选下拉（Pluck ×2）、
// 熔断失败自增（RETURNING）、任务领取（租约方言路径）。
func TestSQLiteStorePaths(t *testing.T) {
	store := setupSQLiteStore(t)
	ctx := context.Background()
	channelID, modelID := seedSQLiteFixtures(t, store)

	// ① 绑定替换：Pluck("id") 的二次引号缺陷在此路径上现形（原先 100% 失败）。
	bound, err := store.ReplaceBindings(ctx, modelID, []ModelChannel{{
		ModelID: modelID, ChannelID: uint64(channelID), Priority: 1, Weight: 1, Status: StatusEnabled,
	}})
	if err != nil {
		t.Fatalf("ReplaceBindings: %v", err)
	}
	if bound != 1 {
		t.Fatalf("ReplaceBindings 绑定数 = %d，期望 1", bound)
	}
	bindings, err := store.ListBindings(ctx, []uint64{modelID})
	if err != nil || len(bindings) != 1 || bindings[0].ChannelName != "sqlite-ch" {
		t.Fatalf("ListBindings: err=%v rows=%+v", err, bindings)
	}

	// ② 列表与模糊匹配（ILIKE→LIKE 分支）
	channels, err := store.ListChannels(ctx, ChannelFilter{Keyword: "SQLITE", Limit: 10})
	if err != nil {
		t.Fatalf("ListChannels(keyword): %v", err)
	}
	if channels.Total != 1 {
		t.Fatalf("ListChannels 快搜命中 = %d，期望 1（SQLite 的 LIKE 对 ASCII 大小写不敏感）", channels.Total)
	}
	if _, err := store.ListModels(ctx, ModelFilter{Keyword: "deepseek"}); err != nil {
		t.Fatalf("ListModels(keyword): %v", err)
	}
	if _, err := store.ListUsageLogs(ctx, UsageFilter{ModelName: "deep", ChannelName: "SQLITE"}); err != nil {
		t.Fatalf("ListUsageLogs(模糊): %v", err)
	}

	// ③ 渠道健康统计（COUNT(*) FILTER + 时间窗）
	stats, err := store.ChannelStats(ctx, nowUnix())
	if err != nil {
		t.Fatalf("ChannelStats: %v", err)
	}
	if stats.Problem != 0 {
		t.Fatalf("新渠道不应计入 problem：%+v", stats)
	}

	// ④ 用量写入 + 五维聚合（GROUP BY 单标识符自动加引号的方言差异）
	for i, row := range []Usage{
		{RequestID: "sqlite-req-1", Attempt: 0, ModelID: modelID, ModelName: "deepseek-chat",
			ChannelID: int64(channelID), ChannelName: "sqlite-ch", CallType: CallTypeChat,
			BizType: "locomo-answer", Source: "eval", PromptTokens: 1000, CompletionTokens: 200,
			TotalTokens: 1200, CachedTokens: 400, Cost: "0.003600", Currency: "CNY",
			LatencyMS: 800, Status: UsageStatusSuccess},
		{RequestID: "sqlite-req-2", Attempt: 0, ModelID: modelID, ModelName: "deepseek-chat",
			ChannelID: int64(channelID), ChannelName: "sqlite-ch", CallType: CallTypeChat,
			BizType: "locomo-judge", Source: "eval", PromptTokens: 500, CompletionTokens: 50,
			TotalTokens: 550, Cost: "0.001400", Currency: "CNY",
			LatencyMS: 400, Status: UsageStatusFail},
	} {
		if err := store.UpsertUsage(ctx, &row); err != nil {
			t.Fatalf("UpsertUsage[%d]: %v", i, err)
		}
	}
	for _, group := range []string{"model", "biz", "source", "biz_source", "day"} {
		res, err := store.AggregateUsage(ctx, UsageAggregateQuery{Group: group})
		if err != nil {
			t.Fatalf("AggregateUsage(%s): %v", group, err)
		}
		if res.Totals.Calls != 2 {
			t.Fatalf("AggregateUsage(%s) 总计调用 = %d，期望 2", group, res.Totals.Calls)
		}
		if len(res.Rows) == 0 {
			t.Fatalf("AggregateUsage(%s) 无分组行", group)
		}
		if group == "day" && res.Rows[0].Day == "" {
			t.Fatalf("AggregateUsage(day) 未切出日期：%+v", res.Rows[0])
		}
	}

	// ⑤ 总览聚合（h24 / d7 / 逐日 / Top 模型 / 最近失败）
	overview, err := store.OverviewAggregates(ctx, nowUnix())
	if err != nil {
		t.Fatalf("OverviewAggregates: %v", err)
	}
	if overview.H24.Calls != 2 || overview.D7.Calls != 2 {
		t.Fatalf("总览窗口调用数不对：h24=%d d7=%d", overview.H24.Calls, overview.D7.Calls)
	}
	if len(overview.D7Series) == 0 || overview.D7Series[0].Day == "" {
		t.Fatalf("逐日趋势未切出日期：%+v", overview.D7Series)
	}
	if len(overview.TopModels) == 0 || overview.TopModels[0].ModelName != "deepseek-chat" {
		t.Fatalf("Top 模型读数不对：%+v", overview.TopModels)
	}
	if len(overview.RecentFails) != 1 {
		t.Fatalf("最近失败行数 = %d，期望 1", len(overview.RecentFails))
	}

	// ⑥ 用量筛选下拉（Pluck("source") / Pluck("biz_type") 的二次引号缺陷面）
	filters, err := store.ListUsageFilters(ctx)
	if err != nil {
		t.Fatalf("ListUsageFilters: %v", err)
	}
	if len(filters.Sources) != 1 || filters.Sources[0] != "eval" {
		t.Fatalf("来源下拉读数不对：%+v", filters.Sources)
	}
	if len(filters.BizTypes) != 2 {
		t.Fatalf("业务类型下拉读数不对：%+v", filters.BizTypes)
	}

	// ⑦ 熔断失败原子自增（RETURNING 分支）
	consecutive, err := store.MarkFailure(ctx, uint64(channelID), "sqlite 探针失败", nowUnix())
	if err != nil {
		t.Fatalf("MarkFailure: %v", err)
	}
	if consecutive != 1 {
		t.Fatalf("连续失败计数 = %d，期望 1", consecutive)
	}
	if ok, err := store.MarkSuccess(ctx, uint64(channelID), nowUnix()); err != nil || !ok {
		t.Fatalf("MarkSuccess: ok=%v err=%v", ok, err)
	}

	// ⑧ 模型能力列（jsonb → SQLite 文本直写分支）
	capabilities := []string{"chat"}
	if affected, err := store.UpdateModel(ctx, modelID, ModelPatch{Capabilities: &capabilities}); err != nil || affected == 0 {
		t.Fatalf("UpdateModel(capabilities): affected=%d err=%v", affected, err)
	}
	got, err := store.GetModel(ctx, modelID)
	if err != nil {
		t.Fatalf("GetModel: %v", err)
	}
	if len(got.Capabilities) != 1 || got.Capabilities[0] != "chat" {
		t.Fatalf("capabilities 回读不对：%+v", got.Capabilities)
	}

	// ⑨ 任务台账：提交（非空 request_id 幂等）→ 领取（租约方言）→ 续租 → 完成终态（结果列 jsonb → 文本直写）
	task, created, err := store.SubmitTask(ctx, TaskSubmitInput{
		TaskType:      TaskTypeChat,
		Payload:       []byte(`{"model":"deepseek-chat","messages":[]}`),
		RequestID:     "sqlite-task-1",
		ProgressTotal: 1,
	})
	if err != nil || !created {
		t.Fatalf("SubmitTask: task=%+v created=%v err=%v", task, created, err)
	}
	if again, createdAgain, err := store.SubmitTask(ctx, TaskSubmitInput{
		TaskType:      TaskTypeChat,
		Payload:       []byte(`{"model":"deepseek-chat","messages":[]}`),
		RequestID:     "sqlite-task-1",
		ProgressTotal: 1,
	}); err != nil || createdAgain || again.ID != task.ID {
		t.Fatalf("SubmitTask 幂等未生效：again=%+v created=%v err=%v", again, createdAgain, err)
	}
	claimed, err := store.claimTask(ctx, 0)
	if err != nil {
		t.Fatalf("claimTask: %v", err)
	}
	if claimed == nil || claimed.ID != task.ID || claimed.Attempt != 1 {
		t.Fatalf("claimTask 读数不对：%+v", claimed)
	}
	if affected, err := store.renewTaskLease(ctx, claimed.ID, 0); err != nil || affected != 1 {
		t.Fatalf("renewTaskLease: affected=%d err=%v", affected, err)
	}
	if err := store.updateTaskProgress(ctx, claimed.ID, 1, 1); err != nil {
		t.Fatalf("updateTaskProgress: %v", err)
	}
	if err := store.markTaskSuccess(ctx, claimed.ID, claimed.Attempt, []byte(`{"content":"ok"}`)); err != nil {
		t.Fatalf("markTaskSuccess: %v", err)
	}
	if affected, err := store.resetStaleTaskLeases(ctx); err != nil {
		t.Fatalf("resetStaleTaskLeases: %v", err)
	} else if affected != 0 {
		t.Fatalf("已完成任务不应被复位：affected=%d", affected)
	}

	// ⑩ 体检报告（同一库、同一 Store）：24 项判据在 SQLite 上应给出与线上一致的结构
	report, err := buildAssetReport(ctx, store, parsePluginSettings(nil), 24)
	if err != nil {
		t.Fatalf("buildAssetReport: %v", err)
	}
	if report.Summary.Channels != 1 || report.Summary.Models != 1 || report.Summary.RoutableBindings != 1 {
		t.Fatalf("体检覆盖读数不对：%+v", report.Summary)
	}
	if report.Summary.ChannelsWithKey != 1 {
		t.Fatalf("密钥覆盖读数不对：%+v", report.Summary)
	}
	if report.Cost.Summary.TotalCalls != 2 || report.Cost.Summary.TotalTokens != 1750 {
		t.Fatalf("成本窗口读数不对：%+v", report.Cost.Summary)
	}
	if report.Cost.Summary.CacheSaved <= 0 {
		t.Fatalf("缓存节省应大于 0（命中 400 token）：%+v", report.Cost.Summary)
	}
	if len(report.Findings) != 0 {
		// 唯一允许的发现是内置消费面智能体（fixture 未配 locomo 模型时会给 unroutable）
		for _, finding := range report.Findings {
			if finding.Code != CodeAgentModelUnroutable {
				t.Fatalf("不应产生的体检发现：%+v", finding)
			}
		}
	}
}
