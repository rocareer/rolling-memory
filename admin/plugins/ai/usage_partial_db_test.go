// plugin: ai v0.2.0
// usage_partial_db_test.go 是迁移 2（plugin_ai_usage.billed_partial）的库级用例（DSN 门控）。
//
// 为什么必须有：billed_partial 是「流式有块无收尾 ⇒ 部分计费」这一行为的**审计痕迹**，
// 它的载体是**真列**（迁移 2），而列名/列宽/默认值/NOT NULL/CHECK 这些事实只有真库能证；
// 接口层用例（handler_test.go 的分支③/④）走假仓储，断言的是编排层写进 Usage 结构的值，
// 证不到「这一列存在且能落库」。
//
// 覆盖边界（B4 分工，刻意不越界）：本文件只断 **INSERT 路径**（新行写 1 ⇒ 读回 1；
// 零值行 ⇒ 落库默认 0）。**冲突覆盖路径**（同 (request_id,attempt) 先写 0 再写 1 ⇒ 终态 1）
// 归 store.go 的 UpsertUsage DoUpdates 列清单，属 store.go 的文件与用例。
//
// 用法与门控同 store_db_test.go 的 pluginAITestDB（PLUGIN_AI_TEST_DSN 指向临时库；
// 库里有 admin_plugins 即拒绝执行；跑完按 <前缀>plugin_ai_% 清空）。
package ai

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"

	"github.com/gin-gonic/gin"
)

// TestUsageBilledPartialColumnShapeDB 迁移 2 的 DDL 事实：列存在、NOT NULL、默认 0、CHECK 闭集 (0,1)。
func TestUsageBilledPartialColumnShapeDB(t *testing.T) {
	db := pluginAITestDB(t)

	var columnDefault, isNullable, dataType, maxChars string
	row := db.Raw(`SELECT column_default, is_nullable, data_type, COALESCE(character_maximum_length::text, '')
		FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = ? AND column_name = 'billed_partial'`,
		table("plugin_ai_usage")).Row()
	if err := row.Scan(&columnDefault, &isNullable, &dataType, &maxChars); err != nil {
		t.Fatalf("billed_partial 列不存在（迁移 2 未应用？）: %v", err)
	}
	if strings.TrimSpace(columnDefault) != "0" {
		t.Fatalf("billed_partial 默认值 = %q，期望 0（存量行必须自动是「非部分计费」）", columnDefault)
	}
	if isNullable != "NO" {
		t.Fatalf("billed_partial 必须 NOT NULL，实际 is_nullable=%q", isNullable)
	}
	if dataType != "smallint" {
		t.Fatalf("billed_partial 列类型 = %q，期望 smallint（与 stream 同口径）", dataType)
	}

	// CHECK 闭集：约束名带表前缀（归属可识别、卸载/回滚按名可删）。
	constraintName := config.Get().Database.Prefix + "ck_plugin_ai_usage_billed_partial"
	var constraintDef string
	err := db.Raw(`SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conname = ? AND conrelid = to_regclass(?)`,
		constraintName, quoteIdent(table("plugin_ai_usage"))).Row().Scan(&constraintDef)
	if err != nil {
		t.Fatalf("迁移 2 的 CHECK 约束 %s 不存在（回滚/卸载会找不到它）: %v", constraintName, err)
	}
	if !strings.Contains(constraintDef, "billed_partial") {
		t.Fatalf("约束 %s 的定义不含 billed_partial: %s", constraintName, constraintDef)
	}

	// 闭集之外的值必须被 DB 拒（应用层只写 0/1，这里钉住「写歪了不会静默落库」）。
	if err := db.Exec(`INSERT INTO ` + quoteIdent(table("plugin_ai_usage")) + ` ("request_id", "billed_partial")
		VALUES ('req-shape-bad', 2)`).Error; err == nil {
		t.Fatal("billed_partial=2 必须被 CHECK 拒绝（闭集 0|1）")
	}
}

// TestUsageBilledPartialInsertPathDB INSERT 路径：补偿行写 1 读回 1（含 tokens/cost/stream 的落库事实），
// 零值行读回 0（GORM 对「带 default 标签的零值字段」省略列 ⇒ 走 DB 默认，正是分支④要的语义）。
func TestUsageBilledPartialInsertPathDB(t *testing.T) {
	db := pluginAITestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	// ① 分支③ 的补偿行：stream=1 + 上游 usage 计数 + cost 6 位小数 + billed_partial=1。
	partial := Usage{
		RequestID:        "req-partial-1",
		Attempt:          0,
		ModelID:          7,
		ModelName:        "gpt-test",
		ChannelID:        11,
		ChannelName:      "主渠道",
		CallType:         CallTypeChat,
		BizType:          "api",
		Source:           publicSource,
		Stream:           1,
		PromptTokens:     10,
		CompletionTokens: 4,
		TotalTokens:      14,
		TokensSource:     TokensSourceUsage,
		Cost:             "0.000018",
		Currency:         DefaultCurrency,
		Status:           UsageStatusFail,
		ErrorCode:        "invalid_response",
		ErrorMessage:     "上游中断",
		ErrorClass:       ErrorClassUpstream,
		BilledPartial:    1,
	}
	if err := store.UpsertUsage(ctx, &partial); err != nil {
		t.Fatalf("写入部分计费行失败（billed_partial 列不存在时这里是 42703）: %v", err)
	}

	chain, err := store.UsageDetailChain(ctx, "req-partial-1")
	if err != nil {
		t.Fatalf("读尝试链失败: %v", err)
	}
	if len(chain) != 1 {
		t.Fatalf("部分计费行应恰好 1 行，实际 %d", len(chain))
	}
	got := chain[0]
	if got.BilledPartial != 1 {
		t.Fatalf("billed_partial 读回 = %d，期望 1（写 1 必须落 1，否则审计标记形同虚设）", got.BilledPartial)
	}
	if got.Stream != 1 || got.TokensSource != TokensSourceUsage || got.TotalTokens != 14 {
		t.Fatalf("补偿行的计数列不对: stream=%d tokens_source=%q total=%d", got.Stream, got.TokensSource, got.TotalTokens)
	}
	if got.Cost != "0.000018" {
		t.Fatalf("cost 读回 = %q，期望 0.000018（numeric(12,6) 文本往返不失真）", got.Cost)
	}
	if got.Status != UsageStatusFail {
		t.Fatalf("部分计费行的 status 仍是 fail（失败语义不变，只是补了账）: %q", got.Status)
	}

	// ② 分支④ 的失败行：不显式赋 billed_partial ⇒ 走 DB 默认 0。
	plain := Usage{
		RequestID:    "req-plain-1",
		Attempt:      0,
		CallType:     CallTypeChat,
		BizType:      "api",
		Source:       publicSource,
		Status:       UsageStatusFail,
		TokensSource: TokensSourceNone,
		Cost:         "0.000000",
		Currency:     DefaultCurrency,
	}
	if err := store.UpsertUsage(ctx, &plain); err != nil {
		t.Fatalf("写入普通失败行失败: %v", err)
	}
	chain, err = store.UsageDetailChain(ctx, "req-plain-1")
	if err != nil {
		t.Fatalf("读尝试链失败: %v", err)
	}
	if len(chain) != 1 || chain[0].BilledPartial != 0 {
		t.Fatalf("无块失败行必须 billed_partial=0（默认值），实际 %+v", chain)
	}
}

// TestUsagePartialFlagEchoedByUsageLogEndpointDB 验收项「usage 接口回显 billed_partial」：
// 走**真 store + 真库**的 /usage-log/list 处理器，回执 JSON 必须逐字带 billed_partial=1。
//
// 这里只挂业务处理器（不经 AdminAuth/AdminPermission 链——那是 handler_test.go 的
// TestAdminRoutesMountAuthAndPermission 覆盖的对象），目的是证明「列值 → GORM 行 → 接口回执」
// 这条链没有在任一层被漏掉（Usage 结构体是回执的唯一形状，json 标签即出口契约）。
func TestUsagePartialFlagEchoedByUsageLogEndpointDB(t *testing.T) {
	db := pluginAITestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	usage := Usage{
		RequestID:        "req-partial-echo",
		Attempt:          0,
		ModelName:        "gpt-test",
		ChannelID:        11,
		ChannelName:      "主渠道",
		CallType:         CallTypeChat,
		BizType:          "api",
		Source:           publicSource,
		Stream:           1,
		PromptTokens:     10,
		CompletionTokens: 4,
		TotalTokens:      14,
		TokensSource:     TokensSourceUsage,
		Cost:             "0.000018",
		Currency:         DefaultCurrency,
		Status:           UsageStatusFail,
		ErrorCode:        "invalid_response",
		ErrorMessage:     "上游中断",
		ErrorClass:       ErrorClassUpstream,
		BilledPartial:    1,
	}
	if err := store.UpsertUsage(ctx, &usage); err != nil {
		t.Fatalf("写入部分计费行失败: %v", err)
	}

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	d := deps{
		loadSettings: func(context.Context) (pluginSettings, error) { return parsePluginSettings(nil), nil },
		loadStore:    func(context.Context) (*Store, error) { return store, nil },
		loadService: func(context.Context, pluginSettings) (*Service, error) {
			return nil, errors.New("本用例只走 usage-log 读面")
		},
	}
	engine.POST("/usage-log/list", d.usageLogList)

	recorder := doRequest(engine, http.MethodPost, "/usage-log/list", `{"request_id":"req-partial-echo"}`, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `"billed_partial":1`) {
		t.Fatalf("usage 接口回执必须回显 billed_partial=1: %s", body)
	}
	if !strings.Contains(body, `"stream":1`) || !strings.Contains(body, `"tokens_source":"usage"`) {
		t.Fatalf("部分计费行的 stream/tokens_source 也要回显: %s", body)
	}
}
