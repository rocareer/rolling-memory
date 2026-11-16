// plugin: ai v0.2.0
// store_test.go 覆盖 store 层**不连库**也可判定的部分：GORM 结构解析（扫描目标）与写库前的
// 列宽兜底（normalizeUsageColumns）。真库行为见 store_db_test.go（DSN 门控）。
package ai

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"gorm.io/gorm/schema"
)

// TestScanTargetsAreGormParseable 无库回归（GORM 结构解析层）：store 的每一个**结构体扫描目标**
// 都必须能被 GORM 解析成合法模型。
//
// 缺陷现场：ChannelHealth.Problems 少了 `gorm:"-"` 时，[]Channel 会被 GORM 当成 has-many 关联去猜
// 外键（Channel.ID 是 autoIncrement 主键），解析直接报
// 「invalid field found for struct ...'s field Problems: define a valid foreign key for relations ...」
// —— 真库上表现为 POST /admin/plugin/ai/overview/index 整页 code=1（空表也报）。
// DryRun 模式下 gorm 拒绝执行 Scan（"dry run mode unsupported"），故用 schema.Parse
// 这一条与实际报错**同源**的解析调用把该缺陷钉在默认测试里；真库端到端回归见 store_db_test.go。
//
// 口径与 gorm 的 ScanRows 一致：只放行 schema.ErrUnsupportedDataType（gorm@v1.31.1
// finisher_api.go:577-581 `if err := tx.Statement.Parse(dest); !errors.Is(err, schema.ErrUnsupportedDataType)`）。
func TestScanTargetsAreGormParseable(t *testing.T) {
	targets := []struct {
		name string
		dest any
	}{
		{"ChannelStats", &ChannelStats{}},
		{"ChannelHealth", &ChannelHealth{}}, // 缺陷现场（Problems 必须带 gorm:"-"）
		{"ModelHealth", &ModelHealth{}},
		{"UsageTotals", &UsageTotals{}},
		{"UsageAggregateRow", &UsageAggregateRow{}},
		{"UsageWindow", &UsageWindow{}},
		{"DailyUsage", &DailyUsage{}},
		{"TopModelUsage", &TopModelUsage{}},
	}
	for _, tc := range targets {
		_, err := schema.Parse(tc.dest, &sync.Map{}, schema.NamingStrategy{})
		if err != nil && !errors.Is(err, schema.ErrUnsupportedDataType) {
			t.Fatalf("%s 必须是合法的 GORM 扫描目标（现场：ChannelHealth.Problems 缺 gorm:\"-\" 时这里报 invalid field found，总览端点整页 code=1）: %v",
				tc.name, err)
		}
	}
}

// normalizeUsageColumns 是 UpsertUsage 的写口兜底：任一文本列超长会让**整行**报 22001，
// 而 usage 是计费/审计行、写入失败只记日志（saveUsage 不打断上游响应）⇒ 调用方拿 200、这行静默丢失。
// 公开面自带的 biz_type 是原缺陷的现场（31 字符 > varchar(30)），这里钉成回归。
func TestNormalizeUsageColumnsTruncatesToColumnWidth(t *testing.T) {
	const overlongBizType = "internal-batch-job-daily-report" // 31 字符
	if got := utf8.RuneCountInString(overlongBizType); got != 31 {
		t.Fatalf("前置：样例应为 31 字符，实际 %d", got)
	}
	usage := Usage{
		RequestID:    strings.Repeat("r", 70),
		ModelName:    strings.Repeat("m", 120),
		ChannelName:  strings.Repeat("c", 60),
		CallType:     CallTypeChat,
		BizType:      overlongBizType,
		Source:       strings.Repeat("s", 40),
		TokensSource: TokensSourceUsage,
		Currency:     strings.Repeat("u", 12),
		Status:       UsageStatusSuccess,
		ErrorCode:    strings.Repeat("e", 130),
		ErrorMessage: strings.Repeat("x", 600),
		ErrorClass:   strings.Repeat("k", 25),
	}
	normalizeUsageColumns(&usage)

	if usage.BizType != overlongBizType[:30] {
		t.Fatalf("biz_type 应截到 30 字符并保留前缀，实际 %q", usage.BizType)
	}
	rows := []struct {
		name  string
		value string
		max   int
	}{
		{"request_id", usage.RequestID, 64},
		{"model_name", usage.ModelName, 100},
		{"channel_name", usage.ChannelName, 50},
		{"biz_type", usage.BizType, 30},
		{"source", usage.Source, 30},
		{"currency", usage.Currency, 10},
		{"error_code", usage.ErrorCode, 100},
		{"error_message", usage.ErrorMessage, 500},
		{"error_class", usage.ErrorClass, 20},
	}
	for _, row := range rows {
		if got := utf8.RuneCountInString(row.value); got != row.max {
			t.Fatalf("%s 截断后字符数 = %d，期望 %d", row.name, got, row.max)
		}
	}
	// 未超长的列一个字节都不许动。
	if usage.CallType != CallTypeChat || usage.Status != UsageStatusSuccess || usage.TokensSource != TokensSourceUsage {
		t.Fatalf("未超长的列不应被改动: %+v", usage)
	}
}

// TestNormalizeUsageColumnsCountsRunesNotBytes varchar(n) 是**字符**数：40 个汉字（120 字节）
// 应截到 30 个汉字，而不是按字节截成 10 个。
func TestNormalizeUsageColumnsCountsRunesNotBytes(t *testing.T) {
	usage := Usage{BizType: strings.Repeat("中", 40)}
	normalizeUsageColumns(&usage)
	if got := utf8.RuneCountInString(usage.BizType); got != 30 {
		t.Fatalf("按字符截断后应 30 字符，实际 %d（按字节截会得 10）", got)
	}
}

// TestNormalizeUsageColumnsNoopForShortValues 正常值原样通过，nil 也安全（写入路径上的兜底不能反过来改数据）。
func TestNormalizeUsageColumnsNoopForShortValues(t *testing.T) {
	usage := Usage{
		RequestID: "ai_0123456789abcdef", BizType: "api", Source: "key:gateway",
		Currency: DefaultCurrency, Status: UsageStatusSuccess, CallType: CallTypeChat,
	}
	before := usage
	normalizeUsageColumns(&usage)
	if usage != before {
		t.Fatalf("短值不应被改动: %+v", usage)
	}
	normalizeUsageColumns(nil)
}
