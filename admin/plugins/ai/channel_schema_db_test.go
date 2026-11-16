// plugin: ai v0.2.0
// channel_schema_db_test.go 是迁移 3（渠道四列 + 厂商表 + 配额约束）与「厂商 → 渠道」继承合并
// 的用例。
//
// 两类用例同文件：
//   - **纯函数**（无库，任何环境都跑）：mergeSupplierInheritance 的逐字段合并、渠道入参四列的
//     校验与「掩码不回写」、渠道 wheres 白名单、Router 的 Gate 接缝（nil 时行为逐字不变）；
//   - **库级**（DSN 门控，复用 store_db_test.go 的 pluginAITestDB：按 <前缀>plugin_ai_% 清场 +
//     按编号升序应用 migrations/*.up.sql 全部）：四列/索引/约束与厂商表的 DDL 事实、迁移 3 的
//     重复应用（幂等）与 down 回滚（不破坏既有 5 表）、继承合并的真库断言（Candidates 与
//     GetChannelEffective 两条读路径）、渠道 create→update→get→list 的四列往返（走真 handler +
//     真 SQL），以及 usage 写口契约（(request_id,attempt) 冲突覆盖刷新 billed_partial，B4）。
//
// 门控与安全边界同 store_db_test.go：未设 PLUGIN_AI_TEST_DSN ⇒ 整组跳过；DSN 指向的库里存在
// admin_plugins ⇒ 直接拒绝执行（防误指向开发/生产库）。
package ai

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ===== 纯函数：继承合并（三级配置的中间两级）=====

// TestMergeSupplierInheritancePerField 逐字段口径：渠道非空优先、空则取厂商、再无则留零值。
//
// 口径出处：源 AiRouterService::channelConfig 的 pick()（/src/app/ai/service/AiRouterService.php:1274-1316）。
// 关键判据是「非空」只判 == ""（不做 TrimSpace）：渠道里显式填的空白串不被静默换掉。
func TestMergeSupplierInheritancePerField(t *testing.T) {
	supplierSide := Candidate{
		SupplierBaseURL: "https://supplier.example.invalid/v1",
		SupplierAPIKey:  "sk-supplier-secret",
		SupplierDriver:  "responses",
		SupplierTimeout: 42,
	}
	withSupplier := func(c Candidate) Candidate { c.SupplierID = 9; return c }

	// 全空渠道的期望值：四列换成厂商值，其余列（含 Supplier* 继承列本身）与输入一致。
	fullyInherited := withSupplier(supplierSide)
	fullyInherited.BaseURL = supplierSide.SupplierBaseURL
	fullyInherited.APIKey = supplierSide.SupplierAPIKey
	fullyInherited.Driver = supplierSide.SupplierDriver
	fullyInherited.Timeout = supplierSide.SupplierTimeout

	cases := []struct {
		name string
		in   Candidate
		want Candidate
	}{
		{
			name: "渠道四列全空 + 挂厂商 ⇒ 四列全取厂商",
			in:   withSupplier(supplierSide),
			want: fullyInherited,
		},
		{
			name: "渠道显式填值 ⇒ 渠道优先（厂商值不覆盖）",
			in: withSupplier(Candidate{
				BaseURL: "https://own.example.invalid/v1", APIKey: "sk-own-secret",
				Driver: "openai", Timeout: 7,
				SupplierBaseURL: "https://supplier.example.invalid/v1", SupplierAPIKey: "sk-supplier-secret",
				SupplierDriver: "responses", SupplierTimeout: 42,
			}),
			want: withSupplier(Candidate{
				BaseURL: "https://own.example.invalid/v1", APIKey: "sk-own-secret",
				Driver: "openai", Timeout: 7,
				SupplierBaseURL: "https://supplier.example.invalid/v1", SupplierAPIKey: "sk-supplier-secret",
				SupplierDriver: "responses", SupplierTimeout: 42,
			}),
		},
		{
			name: "逐字段混合：渠道只给了 base_url，其余取厂商",
			in: withSupplier(Candidate{
				BaseURL:         "https://own.example.invalid/v1",
				SupplierBaseURL: "https://supplier.example.invalid/v1", SupplierAPIKey: "sk-supplier-secret",
				SupplierDriver: "responses", SupplierTimeout: 42,
			}),
			want: withSupplier(Candidate{
				BaseURL: "https://own.example.invalid/v1", APIKey: "sk-supplier-secret",
				Driver: "responses", Timeout: 42,
				SupplierBaseURL: "https://supplier.example.invalid/v1", SupplierAPIKey: "sk-supplier-secret",
				SupplierDriver: "responses", SupplierTimeout: 42,
			}),
		},
		{
			name: "无厂商（supplier_id=0）⇒ 原样（回落现行为）",
			in: Candidate{
				BaseURL: "https://own.example.invalid/v1", APIKey: "sk-own-secret", Driver: "", Timeout: 0,
				SupplierBaseURL: "https://supplier.example.invalid/v1", SupplierAPIKey: "sk-supplier-secret",
				SupplierDriver: "responses", SupplierTimeout: 42,
			},
			want: Candidate{
				BaseURL: "https://own.example.invalid/v1", APIKey: "sk-own-secret", Driver: "", Timeout: 0,
				SupplierBaseURL: "https://supplier.example.invalid/v1", SupplierAPIKey: "sk-supplier-secret",
				SupplierDriver: "responses", SupplierTimeout: 42,
			},
		},
		{
			name: "悬空厂商（LEFT JOIN 四列全空）⇒ 无从继承、回落现行为",
			in:   withSupplier(Candidate{BaseURL: "https://own.example.invalid/v1", APIKey: "sk-own-secret"}),
			want: withSupplier(Candidate{BaseURL: "https://own.example.invalid/v1", APIKey: "sk-own-secret"}),
		},
		{
			name: "渠道留空白串 ⇒ 按字面生效、不替换成厂商值（不 silent fallback）",
			// timeout 仍按「0 才继承」走：非空判定只对文本三列，超时列按 0 判（源 pick() 同款）。
			in: withSupplier(Candidate{
				BaseURL: " ", SupplierBaseURL: "https://supplier.example.invalid/v1", SupplierTimeout: 42,
			}),
			want: withSupplier(Candidate{
				BaseURL: " ", Timeout: 42,
				SupplierBaseURL: "https://supplier.example.invalid/v1", SupplierTimeout: 42,
			}),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeSupplierInheritance(tc.in)
			if got != tc.want {
				t.Fatalf("合并结果不符\n got=%+v\nwant=%+v", got, tc.want)
			}
			// 非继承列不得被合并动到（快照结构是调度/熔断的共同输入）。
			if got.Name != tc.in.Name || got.Model != tc.in.Model || got.BindingPriority != tc.in.BindingPriority {
				t.Fatalf("合并改动了非继承列: %+v", got)
			}
		})
	}
}

// TestMergeSupplierInheritanceAllKeepsOrderAndCount 批量版只做逐行合并（顺序与条数不变）。
func TestMergeSupplierInheritanceAllKeepsOrderAndCount(t *testing.T) {
	rows := []Candidate{
		{ChannelID: 1, SupplierID: 9, SupplierTimeout: 30},
		{ChannelID: 2, Timeout: 5},
		{ChannelID: 3, SupplierID: 9, SupplierBaseURL: "https://supplier.example.invalid/v1"},
	}
	got := mergeSupplierInheritanceAll(rows)
	if len(got) != 3 {
		t.Fatalf("条数 = %d，期望 3", len(got))
	}
	for i, want := range []uint64{1, 2, 3} {
		if got[i].ChannelID != want {
			t.Fatalf("顺序[%d] = %d，期望 %d", i, got[i].ChannelID, want)
		}
	}
	if got[0].Timeout != 30 || got[2].BaseURL != "https://supplier.example.invalid/v1" {
		t.Fatalf("逐行合并结果不对: %+v", got)
	}
}

// ===== 纯函数：渠道入参四列与掩码不回写 =====

// TestChannelSaveRequestFourColumnsCreate 新增路径的四列收敛：driver 形状校验、supplier_id 非负、
// rpm/tpm 负值收敛为 0、空 driver 合法（= 未指定，调用期回落默认协议）。
func TestChannelSaveRequestFourColumnsCreate(t *testing.T) {
	base := func() channelSaveRequest {
		return channelSaveRequest{
			Name:    stringPtr("主渠道"),
			Model:   stringPtr("gpt-test"),
			BaseURL: stringPtr("https://api.example.invalid/v1"),
		}
	}

	// ① 缺省：四列取零值（driver 空 = 未指定；0 = 不挂厂商 / 不限配额）。
	row, err := base().channel()
	if err != nil {
		t.Fatalf("最小合法入参不应报错: %v", err)
	}
	if row.Driver != "" || row.SupplierID != 0 || row.RPMLimit != 0 || row.TPMLimit != 0 {
		t.Fatalf("四列缺省应为零值: %+v", row)
	}

	// ② 显式填值：trim 后带出。
	req := base()
	req.Driver = stringPtr("  dashscope_tts  ")
	req.SupplierID = ptrInt64(7)
	req.RPMLimit = intPtr(600)
	req.TPMLimit = intPtr(120000)
	row, err = req.channel()
	if err != nil {
		t.Fatalf("合法四列不应报错: %v", err)
	}
	if row.Driver != "dashscope_tts" || row.SupplierID != 7 || row.RPMLimit != 600 || row.TPMLimit != 120000 {
		t.Fatalf("四列未按入参落行: driver=%q supplier=%d rpm=%d tpm=%d",
			row.Driver, row.SupplierID, row.RPMLimit, row.TPMLimit)
	}

	// ③ 负值：supplier_id 拒（列上有 CHECK，接口层先给可读错误）；rpm/tpm 收敛为 0（不限）。
	bad := base()
	bad.SupplierID = ptrInt64(-1)
	if _, err := bad.channel(); err == nil {
		t.Fatal("supplier_id 负数必须被拒（0=独立渠道，负值无意义）")
	}
	neg := base()
	neg.RPMLimit = intPtr(-5)
	neg.TPMLimit = intPtr(-1)
	row, err = neg.channel()
	if err != nil {
		t.Fatalf("rpm/tpm 负数应收敛而不是报错: %v", err)
	}
	if row.RPMLimit != 0 || row.TPMLimit != 0 {
		t.Fatalf("rpm/tpm 负数必须收敛为 0（= 不限）: %+v", row)
	}

	// ④ driver 形状：闭集交给驱动注册表（未知值在调用期报错），写入期只拦非法字符与超长。
	//   注意首尾空白会被 trim（与其它文本列同口径），所以「含空白」的判据要用内嵌空白。
	for name, badDriver := range map[string]string{
		"内嵌空格":     "open ai",
		"内嵌制表符":    "open\tai",
		"含斜杠":      "app/ai/driver",
		"含反斜杠":     `app\ai\driver\OpenAIDriver`,
		"含非 ASCII": "open驱动",
	} {
		req := base()
		req.Driver = stringPtr(badDriver)
		if _, err := req.channel(); err == nil {
			t.Fatalf("driver %q（%s）必须被拒", badDriver, name)
		}
	}
	long := base()
	long.Driver = stringPtr(strings.Repeat("d", driverNameMaxLen+1))
	if _, err := long.channel(); err == nil {
		t.Fatalf("driver 超过 %d 字符必须被拒", driverNameMaxLen)
	}
	edge := base()
	edge.Driver = stringPtr(strings.Repeat("d", driverNameMaxLen))
	if _, err := edge.channel(); err != nil {
		t.Fatalf("driver 恰好 %d 字符应放行: %v", driverNameMaxLen, err)
	}
}

// TestChannelPatchFourColumnsAndMaskNotWrittenBack 编辑路径的四列三态 + api_key 掩码不回写。
// TestChannelBaseURLNullableOnlyWithSupplier 是「留空 base_url ⇒ 运行时继承厂商」的写入口径回归：
// 源侧只在 supplier_id<=0 时要求 base_url（Channel.php:89），本插件同口径——挂厂商（supplier_id>0）
// 时新增/清空 base_url 必须放行（早期实现两条路都拒空，导致「继承厂商」在管理面不可达，只有直连 DB 能
// 造出该状态）；不挂厂商时留空仍按原文案拒。
func TestChannelBaseURLNullableOnlyWithSupplier(t *testing.T) {
	empty := ""
	blank := "   "
	supplier := int64(9)

	// ① create：挂厂商 + 留空/纯空白 ⇒ 放行，落行的 base_url 为空串（= 继承）。
	for name, value := range map[string]string{"空串": empty, "纯空白": blank} {
		req := channelSaveRequest{
			Name:       stringPtr("继承渠道"),
			Model:      stringPtr("gpt-test"),
			BaseURL:    &value,
			SupplierID: &supplier,
		}
		row, err := req.channel()
		if err != nil {
			t.Fatalf("create 挂厂商 + base_url %s 必须放行（运行时继承厂商地址）: %v", name, err)
		}
		if row.BaseURL != "" || row.SupplierID != 9 {
			t.Fatalf("留空应落空串 + 保留 supplier_id: %+v", row)
		}
	}

	// ② create：不挂厂商 + 留空 ⇒ 仍拒（文案逐字不变：base_url 不合法: base_url 不能为空）。
	for name, req := range map[string]channelSaveRequest{
		"base_url 缺省字段": {Name: stringPtr("独立渠道"), Model: stringPtr("gpt-test")},
		"base_url 空串":   {Name: stringPtr("独立渠道"), Model: stringPtr("gpt-test"), BaseURL: &empty},
		"supplier_id=0": {Name: stringPtr("独立渠道"), Model: stringPtr("gpt-test"), BaseURL: &empty,
			SupplierID: ptrInt64(0)},
	} {
		_, err := req.channel()
		if err == nil || !strings.Contains(err.Error(), "base_url 不能为空") {
			t.Fatalf("%s（未挂厂商）必须按原文案拒: %v", name, err)
		}
	}

	// ③ create：非空 base_url 照旧过格式白名单（挂不挂厂商都一样）。
	bad := channelSaveRequest{Name: stringPtr("坏地址"), Model: stringPtr("gpt-test"),
		BaseURL: stringPtr("ftp://api.example.invalid"), SupplierID: &supplier}
	if _, err := bad.channel(); err == nil {
		t.Fatal("非法 scheme 必须被拒（挂厂商也不豁免格式校验）")
	}

	// ④ update：生效供应商是挂厂商 ⇒ 清空放行；不是 ⇒ 拒（patch() 等价 patchForSupplier(0)，旧行为不变）。
	patch, err := (channelSaveRequest{BaseURL: &empty}).patchForSupplier(9)
	if err != nil {
		t.Fatalf("挂厂商时清空 base_url 必须放行: %v", err)
	}
	if patch.BaseURL == nil || *patch.BaseURL != "" {
		t.Fatalf("清空必须进补丁（= 改为继承）: %+v", patch.BaseURL)
	}
	if _, err := (channelSaveRequest{BaseURL: &empty}).patchForSupplier(0); err == nil {
		t.Fatal("不挂厂商时清空 base_url 必须被拒")
	}
	if _, err := (channelSaveRequest{BaseURL: &empty}).patch(); err == nil {
		t.Fatal("patch()（= patchForSupplier(0)）必须保留旧行为：不挂厂商拒空")
	}
	// 非空：格式错照样拒（挂厂商不豁免），合法值 trim 后进补丁。
	if _, err := (channelSaveRequest{BaseURL: stringPtr("not-a-url")}).patchForSupplier(9); err == nil {
		t.Fatal("挂厂商时非法地址仍必须被拒")
	}
	patch, err = (channelSaveRequest{BaseURL: stringPtr("  https://api.example.invalid/v1  ")}).patchForSupplier(9)
	if err != nil || patch.BaseURL == nil || *patch.BaseURL != "https://api.example.invalid/v1" {
		t.Fatalf("合法地址应 trim 后进补丁: %v %+v", err, patch.BaseURL)
	}
}

func TestChannelPatchFourColumnsAndMaskNotWrittenBack(t *testing.T) {
	// ① 四列缺省 ⇒ 不进补丁（nil = 不修改）。
	patch, err := (channelSaveRequest{}).patch()
	if err != nil {
		t.Fatalf("空入参不应报错: %v", err)
	}
	if patch.Driver != nil || patch.SupplierID != nil || patch.RPMLimit != nil || patch.TPMLimit != nil {
		t.Fatalf("缺省四列不得进补丁: %+v", patch)
	}

	// ② 显式值（含 driver 清空、supplier 解绑、配额清零）⇒ 进补丁。
	zero := int64(0)
	empty := ""
	req := channelSaveRequest{
		Driver:     &empty,
		SupplierID: &zero,
		RPMLimit:   intPtr(0),
		TPMLimit:   intPtr(30),
	}
	patch, err = req.patch()
	if err != nil {
		t.Fatalf("显式零值不应报错: %v", err)
	}
	if patch.Driver == nil || *patch.Driver != "" {
		t.Fatalf("driver 显式空串必须进补丁（= 改为不指定驱动）: %+v", patch.Driver)
	}
	if patch.SupplierID == nil || *patch.SupplierID != 0 {
		t.Fatalf("supplier_id 显式 0 必须进补丁（= 解绑厂商）: %+v", patch.SupplierID)
	}
	if patch.RPMLimit == nil || *patch.RPMLimit != 0 || patch.TPMLimit == nil || *patch.TPMLimit != 30 {
		t.Fatalf("配额零值必须进补丁: rpm=%+v tpm=%+v", patch.RPMLimit, patch.TPMLimit)
	}

	// ③ 非法值仍拒（未被四列改动放过）。
	negSupplier := int64(-3)
	if _, err := (channelSaveRequest{SupplierID: &negSupplier}).patch(); err == nil {
		t.Fatal("supplier_id 负数必须被拒")
	}
	if _, err := (channelSaveRequest{Driver: stringPtr("bad driver")}).patch(); err == nil {
		t.Fatal("非法 driver 必须被拒")
	}

	// ④ api_key 三态（口径不变）：缺省 / 空串 / 含 '*' ⇒ 不修改；明文 ⇒ 写入。
	masked := "sk-c**********cret"
	for _, value := range []string{"", masked} {
		patch, err := (channelSaveRequest{APIKey: &value}).patch()
		if err != nil {
			t.Fatalf("api_key %q 不应报错: %v", value, err)
		}
		if patch.APIKey != nil {
			t.Fatalf("api_key %q 必须视为不修改（掩码/空串永不回写）: %+v", value, patch.APIKey)
		}
	}
	plain := "sk-new-secret-value"
	patch, err = (channelSaveRequest{APIKey: &plain}).patch()
	if err != nil || patch.APIKey == nil || *patch.APIKey != plain {
		t.Fatalf("明文 api_key 必须写入: patch=%+v err=%v", patch, err)
	}
}

// TestChannelWhereFieldsIncludeFourColumns 高级检索白名单接受四个新列、拒绝敏感列。
func TestChannelWhereFieldsIncludeFourColumns(t *testing.T) {
	for _, field := range []string{"driver", "supplier_id", "rpm_limit", "tpm_limit"} {
		if _, ok := channelWhereFields[field]; !ok {
			t.Fatalf("wheres 白名单缺字段 %s（四列既是回显面也是筛选面）", field)
		}
	}
	groups := []whereGroup{{Wheres: []whereCondition{{Field: "supplier_id", Operator: "eq", Value: 3}}}}
	if _, err := validateWhereGroups(channelWhereFields, groups); err != nil {
		t.Fatalf("supplier_id 的等值条件应被接受: %v", err)
	}
	for _, field := range []string{"api_key", "unknown_column"} {
		bad := []whereGroup{{Wheres: []whereCondition{{Field: field, Operator: "eq", Value: "x"}}}}
		if _, err := validateWhereGroups(channelWhereFields, bad); err == nil {
			t.Fatalf("字段 %q 必须不在白名单内（api_key 一律不可筛）", field)
		}
	}
}

// ===== 纯函数：Router 的 Gate 接缝（挂载点）=====

// channelGateStub 是可控的准入门桩：记录调用次数与被看到的渠道快照。
type channelGateStub struct {
	allowed   bool
	err       error
	calls     int
	seenIDs   []int64
	seenNames []string
}

func (g *channelGateStub) Admit(_ context.Context, ch Channel, _ int64) (bool, error) {
	g.calls++
	g.seenIDs = append(g.seenIDs, ch.ID)
	g.seenNames = append(g.seenNames, ch.Name)
	return g.allowed, g.err
}

// TestRouterAdmitGateNilKeepsLegacyBehavior gate 为 nil 时 Admit 的行为必须与「没有这一层」逐字一致
// （router_test.go 的既有断言不作任何改动的前提）。
func TestRouterAdmitGateNilKeepsLegacyBehavior(t *testing.T) {
	router := NewRouter(nil, nil)
	ch := Channel{ID: 5, Name: "ch", Circuit: CircuitOpen}

	ad, err := router.Admit(context.Background(), ch, 100)
	if err != nil {
		t.Fatalf("gate/breaker 都为 nil 时不应报错: %v", err)
	}
	if !ad.Allowed || ad.Circuit != CircuitOpen {
		t.Fatalf("gate 为 nil 时准入结果应保持一致（Allowed=true + 原样 circuit），实际 %+v", ad)
	}
	if ad, err := (*Router)(nil).Admit(context.Background(), ch, 100); err != nil || !ad.Allowed {
		t.Fatalf("nil 接收者也必须放行: ad=%+v err=%v", ad, err)
	}
}

// TestRouterAdmitGateSeam 非 nil gate 的三态：放行 / 拒绝（Allowed=false、err=nil）/ 故障（err 带出）。
// 同时钉住两件事：gate 拿到的是已合并的渠道快照（含 driver 与配额列）；熔断拒绝的候选不消耗配额。
func TestRouterAdmitGateSeam(t *testing.T) {
	ctx := context.Background()
	ch := Channel{ID: 7, Name: "ch-7", Circuit: CircuitClosed, Driver: "responses", RPMLimit: 600, TPMLimit: 900}

	// ① 放行：Admission 与熔断侧一致，gate 恰好被调用一次且看到 driver/配额列。
	// gate 字段是**同包未导出**字段——installChannelGate 也是这样挂的（不新增导出的构造签名）。
	allow := &channelGateStub{allowed: true}
	router := NewRouter(nil, nil)
	router.gate = allow
	ad, err := router.Admit(ctx, ch, 100)
	if err != nil || !ad.Allowed || ad.Circuit != CircuitClosed {
		t.Fatalf("gate 放行时应返回熔断侧结果: ad=%+v err=%v", ad, err)
	}
	if allow.calls != 1 {
		t.Fatalf("gate 应恰好被调用一次，实际 %d", allow.calls)
	}
	if len(allow.seenIDs) != 1 || allow.seenIDs[0] != 7 || allow.seenNames[0] != "ch-7" {
		t.Fatalf("gate 收到的渠道快照不对: ids=%v names=%v", allow.seenIDs, allow.seenNames)
	}

	// ② 拒绝：Allowed=false 且 err=nil（调用方据此「跳过候选、不自增 attempt」）。
	deny := &channelGateStub{allowed: false}
	denied := NewRouter(nil, nil)
	denied.gate = deny
	ad, err = denied.Admit(ctx, ch, 100)
	if err != nil {
		t.Fatalf("配额拒绝不是错误，必须是 (false, nil): %v", err)
	}
	if ad.Allowed {
		t.Fatalf("配额不足时必须 Allowed=false: %+v", ad)
	}

	// ③ 故障：err 带出且 Allowed=false 兜底（调用方即使忽略 err 也不会误放行）。
	boom := errors.New("配额存储故障")
	broken := NewRouter(nil, nil)
	broken.gate = &channelGateStub{allowed: true, err: boom}
	ad, err = broken.Admit(ctx, ch, 100)
	if !errors.Is(err, boom) {
		t.Fatalf("gate 故障必须带出原错误: %v", err)
	}
	if ad.Allowed {
		t.Fatalf("gate 故障时 Allowed 必须兜底为 false: %+v", ad)
	}

	// ④ 熔断拒绝在先：候选不进配额面（不发令牌、不扣 TPM 窗口）。
	store := newFakeCircuitStore()
	store.circuit[7] = CircuitHalfOpen
	store.openedAt[7] = 100
	breakers := NewRouter(NewCircuit(store), nil)
	stub := &channelGateStub{allowed: true}
	breakers.gate = stub
	half := ch
	half.Circuit = CircuitHalfOpen
	half.OpenedAt = ptrInt64(100)
	ad, err = breakers.Admit(ctx, half, 110)
	if err != nil {
		t.Fatalf("半开探针在飞时不应报错: %v", err)
	}
	if ad.Allowed {
		t.Fatalf("熔断拒绝时不得放行: %+v", ad)
	}
	if stub.calls != 0 {
		t.Fatalf("被熔断拒绝的候选不得进准入门（避免白扣配额），实际调用 %d 次", stub.calls)
	}
}

// TestRouterFirstAdmittedSkipsGateDeniedCandidate 候选链上的语义：被配额拒绝的候选被跳过
// （换下一候选、不算一次真实调用），全被拒时返回 ok=false 且不返回错误。
func TestRouterFirstAdmittedSkipsGateDeniedCandidate(t *testing.T) {
	ctx := context.Background()
	router := NewRouter(nil, nil)
	// 只放行渠道 2：桩按渠道 ID 判定。
	router.gate = channelGateByID{allowed: map[int64]bool{2: true}}

	candidates := []Candidate{
		testCandidate(1, 1, 1), // 配额不足 ⇒ 跳过
		testCandidate(2, 2, 1), // 放行
	}
	got, ok, err := router.FirstAdmitted(ctx, candidates, 100)
	if err != nil {
		t.Fatalf("配额拒绝不应产生错误: %v", err)
	}
	if !ok || got.ChannelID != 2 {
		t.Fatalf("配额拒绝的候选必须被跳过、由下一个承接: ok=%v got=%+v", ok, got)
	}

	// 全被拒：ok=false、err=nil（调用方按「所有候选都不可用」收口）。
	router.gate = channelGateByID{allowed: map[int64]bool{}}
	_, ok, err = router.FirstAdmitted(ctx, candidates, 100)
	if err != nil || ok {
		t.Fatalf("全部被配额拒绝时应 ok=false 且 err=nil: ok=%v err=%v", ok, err)
	}
}

// channelGateByID 按渠道 ID 白名单放行的准入门桩（FirstAdmitted 的换道用例用）。
type channelGateByID struct {
	allowed map[int64]bool
}

func (g channelGateByID) Admit(_ context.Context, ch Channel, _ int64) (bool, error) {
	return g.allowed[ch.ID], nil
}

// ===== 库级脚手架（DSN 门控）=====

// channelSchemaDeps 把真实 *Store 注入接口层（其余依赖用不可用桩）：库级用例走「真 handler → 真 SQL」，
// 覆盖入参 → 落库 → 回显整链；编排单例不参与（渠道 CRUD 不需要驱动）。
func channelSchemaDeps(db *gorm.DB) deps {
	unavailable := errors.New("库级用例不装配编排单例")
	return deps{
		loadSettings: func(context.Context) (pluginSettings, error) { return parsePluginSettings(nil), nil },
		loadStore:    func(context.Context) (*Store, error) { return NewStore(db), nil },
		loadService:  func(context.Context, pluginSettings) (*Service, error) { return nil, unavailable },
	}
}

// channelSchemaEngine 只挂业务处理器：本文件要验的是「入参 → SQL → 回显」，
// 鉴权/权限链（AdminAuth + AdminPermission）属 handler_test.go 的结构性用例与宿主中间件覆盖面，
// 在这里挂上只会让库级用例变成权限测试。
func channelSchemaEngine(d deps) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	group := engine.Group("/admin/plugin/" + Name)
	for _, route := range d.adminRoutes() {
		group.Handle(route.Method, route.Path, route.Handler)
	}
	return engine
}

// channelSchemaMigrationSQL 读一个迁移文件并按进程表前缀替换 __PREFIX__（与安装器/测试基建同物）。
func channelSchemaMigrationSQL(t *testing.T, file string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(pluginAITestMigrationsDir, file))
	if err != nil {
		t.Fatalf("读取迁移文件 %s 失败: %v", file, err)
	}
	return strings.ReplaceAll(string(body), "__PREFIX__", config.Get().Database.Prefix)
}

// channelSchemaColumnShape 一列的形状（信息模式读出来的事实）。
type channelSchemaColumnShape struct {
	DataType      string
	MaxLen        int
	IsNullable    string
	ColumnDefault string
}

// channelSchemaColumnInfo 读一列的形状；列不存在返回 ok=false（不 Fatalf，调用方自行判定）。
func channelSchemaColumnInfo(t *testing.T, db *gorm.DB, logicalTable, column string) (channelSchemaColumnShape, bool) {
	t.Helper()
	var shape channelSchemaColumnShape
	var maxLen sql.NullInt64
	var columnDefault sql.NullString
	err := db.Raw(`SELECT data_type, character_maximum_length, is_nullable, column_default
		FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = ? AND column_name = ?`,
		table(logicalTable), column).Row().Scan(&shape.DataType, &maxLen, &shape.IsNullable, &columnDefault)
	if errors.Is(err, sql.ErrNoRows) {
		return channelSchemaColumnShape{}, false
	}
	if err != nil {
		t.Fatalf("读取列 %s.%s 的形状失败: %v", logicalTable, column, err)
	}
	shape.MaxLen = int(maxLen.Int64)
	shape.ColumnDefault = columnDefault.String
	return shape, true
}

// channelSchemaTableExists 表是否存在（to_regclass 口径，与 store_db_test.go 的防误指向判据同物）。
func channelSchemaTableExists(t *testing.T, db *gorm.DB, logicalTable string) bool {
	t.Helper()
	var name sql.NullString
	if err := db.Raw(`SELECT to_regclass(?)::text`, quoteIdent(table(logicalTable))).Row().Scan(&name); err != nil {
		t.Fatalf("探测表 %s 失败: %v", logicalTable, err)
	}
	return name.Valid && name.String != ""
}

// channelSchemaHasString 小工具：切片里是否有该字符串（避免为一行判定引入额外依赖）。
func channelSchemaHasString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// channelSchemaRow 渠道出口形状里本文件关心的字段（get / list 共用）。
type channelSchemaRow struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Driver     string `json:"driver"`
	SupplierID int64  `json:"supplier_id"`
	RPMLimit   int    `json:"rpm_limit"`
	TPMLimit   int    `json:"tpm_limit"`
	APIKey     string `json:"api_key"`
	HasAPIKey  bool   `json:"has_api_key"`
	BaseURL    string `json:"base_url"`
	Model      string `json:"model"`
	Timeout    int    `json:"timeout"`
}

// ===== 库级：DDL 事实 =====

// TestChannelSchemaFourColumnsIndexesAndSupplierTableDB 迁移 3 的 DDL 事实：
// 渠道四列（类型/非空/默认值）与 supplier_id 索引、配额非负约束、厂商表及其列清单。
func TestChannelSchemaFourColumnsIndexesAndSupplierTableDB(t *testing.T) {
	db := pluginAITestDB(t)
	prefix := config.Get().Database.Prefix

	// ① 渠道四列：存在 + 类型 + NOT NULL + 默认值（默认值是「存量行升级后语义正确」的依据：
	//    空 driver = 未指定、0 = 不挂厂商 / 不限配额、timeout 不在此列）。
	cases := []struct {
		column      string
		wantType    string
		wantMaxLen  int
		wantDefault string
	}{
		{"driver", "character varying", 255, "''"},
		{"supplier_id", "bigint", 0, "0"},
		{"rpm_limit", "integer", 0, "0"},
		{"tpm_limit", "integer", 0, "0"},
	}
	for _, tc := range cases {
		shape, ok := channelSchemaColumnInfo(t, db, "plugin_ai_channel", tc.column)
		if !ok {
			t.Fatalf("迁移 3 后 plugin_ai_channel.%s 必须存在（应用用例的 schema 就是它的验收集）", tc.column)
		}
		if shape.DataType != tc.wantType {
			t.Fatalf("列 %s 类型 = %q，期望 %q", tc.column, shape.DataType, tc.wantType)
		}
		if tc.wantMaxLen > 0 && shape.MaxLen != tc.wantMaxLen {
			t.Fatalf("列 %s 长度 = %d，期望 %d（与源的 supplier.driver 同口径）", tc.column, shape.MaxLen, tc.wantMaxLen)
		}
		if shape.IsNullable != "NO" {
			t.Fatalf("列 %s 必须 NOT NULL（继承与配额判定都按零值语义），实际 is_nullable=%q", tc.column, shape.IsNullable)
		}
		if !strings.Contains(shape.ColumnDefault, tc.wantDefault) {
			t.Fatalf("列 %s 默认值 = %q，期望包含 %q（存量行升级后必须落在「未配」语义上）",
				tc.column, shape.ColumnDefault, tc.wantDefault)
		}
	}

	// ② supplier_id 索引：厂商页的 channel_count 与继承 JOIN 都靠它。
	indexNames := []string{}
	if err := db.Raw(`SELECT indexname FROM pg_indexes
		WHERE schemaname = current_schema() AND tablename = ?`, table("plugin_ai_channel")).Scan(&indexNames).Error; err != nil {
		t.Fatalf("读渠道索引清单失败: %v", err)
	}
	wantIndex := prefix + "idx_plugin_ai_channel_supplier"
	if !channelSchemaHasString(indexNames, wantIndex) {
		t.Fatalf("渠道表缺索引 %s（现有：%v）", wantIndex, indexNames)
	}
	var indexDef string
	if err := db.Raw(`SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ?`,
		wantIndex).Row().Scan(&indexDef); err != nil {
		t.Fatalf("读索引定义失败: %v", err)
	}
	// pg_indexes 的 indexdef 里列名不一定带引号（PostgreSQL 只对必要的标识符加引号）⇒ 按不带引号的列名判。
	if !strings.Contains(indexDef, "supplier_id") {
		t.Fatalf("索引 %s 没建在 supplier_id 上: %s", wantIndex, indexDef)
	}

	// ③ 配额/厂商 ID 的非负约束：绕过接口层直接写负值必须被 DB 拒（接口层已 clamp，这里是兜底）。
	store := NewStore(db)
	ctx := context.Background()
	row := Channel{Name: "ch-check", Model: "m", BaseURL: "https://api.example.invalid/v1", Status: StatusEnabled}
	if err := store.CreateChannel(ctx, &row); err != nil {
		t.Fatalf("造渠道失败: %v", err)
	}
	if err := db.Exec(`UPDATE `+quoteIdent(table("plugin_ai_channel"))+` SET "rpm_limit" = -1 WHERE "id" = ?`, row.ID).Error; err == nil {
		t.Fatal("rpm_limit=-1 必须被 CHECK 拒绝（非负约束缺失会让配额算法收到负预算）")
	}
	var constraintDef string
	err := db.Raw(`SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conname = ? AND conrelid = to_regclass(?)`,
		prefix+"ck_plugin_ai_channel_gate_nums", quoteIdent(table("plugin_ai_channel"))).Row().Scan(&constraintDef)
	if err != nil {
		t.Fatalf("迁移 3 的 CHECK %sck_plugin_ai_channel_gate_nums 不存在: %v", prefix, err)
	}
	for _, column := range []string{"supplier_id", "rpm_limit", "tpm_limit"} {
		if !strings.Contains(constraintDef, column) {
			t.Fatalf("约束定义缺 %s: %s", column, constraintDef)
		}
	}

	// ④ 厂商表存在且列清单与源一致（多一列少一列都算漂移）。
	if !channelSchemaTableExists(t, db, "plugin_ai_supplier") {
		t.Fatal("迁移 3 后 plugin_ai_supplier 必须存在")
	}
	wantColumns := []string{
		"id", "name", "vendor", "base_url", "api_key", "driver",
		"timeout", "status", "remark", "create_time", "update_time",
	}
	columns := []string{}
	if err := db.Raw(`SELECT column_name FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = ? ORDER BY column_name`,
		table("plugin_ai_supplier")).Scan(&columns).Error; err != nil {
		t.Fatalf("读厂商表列清单失败: %v", err)
	}
	if len(columns) != len(wantColumns) {
		t.Fatalf("厂商表列数 = %d（%v），期望 %d（%v）", len(columns), columns, len(wantColumns), wantColumns)
	}
	for _, want := range wantColumns {
		if !channelSchemaHasString(columns, want) {
			t.Fatalf("厂商表缺列 %s（现有 %v）", want, columns)
		}
	}
	supplierIndexes := []string{}
	if err := db.Raw(`SELECT indexname FROM pg_indexes
		WHERE schemaname = current_schema() AND tablename = ?`, table("plugin_ai_supplier")).Scan(&supplierIndexes).Error; err != nil {
		t.Fatalf("读厂商索引清单失败: %v", err)
	}
	if !channelSchemaHasString(supplierIndexes, prefix+"idx_plugin_ai_supplier_status") {
		t.Fatalf("厂商表缺 status 索引（现有 %v）", supplierIndexes)
	}
}

// TestChannelSchemaMigration3IdempotentAndRollbackDB 迁移 3 的两个运维前提：
// up 可重复应用（幂等列判断）、down 可回滚且不破坏既有 5 表。
func TestChannelSchemaMigration3IdempotentAndRollbackDB(t *testing.T) {
	db := pluginAITestDB(t)
	up := channelSchemaMigrationSQL(t, "3_channel_driver_supplier_gate.up.sql")
	down := channelSchemaMigrationSQL(t, "3_channel_driver_supplier_gate.down.sql")

	// ① 重复应用 up：列/表/索引都在（幂等 DDL），且不报错。
	if err := db.Exec(up).Error; err != nil {
		t.Fatalf("迁移 3 的 up 必须可重复应用（幂等），第二次应用报错: %v", err)
	}
	for _, column := range []string{"driver", "supplier_id", "rpm_limit", "tpm_limit"} {
		if _, ok := channelSchemaColumnInfo(t, db, "plugin_ai_channel", column); !ok {
			t.Fatalf("重复应用 up 后渠道列 %s 消失", column)
		}
	}
	if !channelSchemaTableExists(t, db, "plugin_ai_supplier") {
		t.Fatal("重复应用 up 后厂商表消失（CREATE TABLE IF NOT EXISTS 失效？）")
	}

	// ② down：本迁移的对象全部撤销（列/表/索引）。
	if err := db.Exec(down).Error; err != nil {
		t.Fatalf("迁移 3 的 down 报错: %v", err)
	}
	for _, column := range []string{"driver", "supplier_id", "rpm_limit", "tpm_limit"} {
		if _, ok := channelSchemaColumnInfo(t, db, "plugin_ai_channel", column); ok {
			t.Fatalf("down 之后渠道列 %s 仍存在", column)
		}
	}
	if channelSchemaTableExists(t, db, "plugin_ai_supplier") {
		t.Fatal("down 之后厂商表仍存在")
	}

	// ③ 既有 5 表必须毫发无损（down 越界删表 = 数据事故）。
	for _, logical := range []string{
		"plugin_ai_channel", "plugin_ai_model", "plugin_ai_model_channel", "plugin_ai_usage", "plugin_ai_breaker_event",
	} {
		if !channelSchemaTableExists(t, db, logical) {
			t.Fatalf("down 破坏了既有表 %s（回滚只许撤本迁移的对象）", logical)
		}
	}

	// ④ 恢复现场：重新应用 up（同一用例之后的断言与人工排查继续可用）。
	if err := db.Exec(up).Error; err != nil {
		t.Fatalf("回滚后重新应用 up 失败: %v", err)
	}
	if !channelSchemaTableExists(t, db, "plugin_ai_supplier") {
		t.Fatal("重新应用 up 后厂商表未恢复")
	}
}

// ===== 库级：继承合并（真库两例 + 无厂商回落）=====

// TestChannelSupplierInheritanceCandidatesDB 覆盖候选读路径的继承：
// 渠道留空 + 有厂商 ⇒ 取厂商四列；渠道显式填值 ⇒ 渠道优先；无厂商 / 悬空厂商 ⇒ 回落现行为。
func TestChannelSupplierInheritanceCandidatesDB(t *testing.T) {
	db := pluginAITestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	supplier := Supplier{
		Name: "厂商甲", Vendor: "openai",
		BaseURL: "https://supplier.example.invalid/v1", APIKey: "sk-supplier-secret",
		Driver: "responses", Timeout: 42, Status: StatusEnabled,
	}
	if err := store.CreateSupplier(ctx, &supplier); err != nil {
		t.Fatalf("造厂商失败: %v", err)
	}
	if supplier.ID == 0 {
		t.Fatal("厂商 ID 未回填")
	}

	// 四类渠道：全空挂厂商（继承）、显式填值挂厂商（渠道优先）、不挂厂商、悬空 supplier_id。
	inherit := Channel{
		Name: "ch-inherit", Vendor: "openai", Model: "m-inherit", SupplierID: supplier.ID,
		BaseURL: "", APIKey: "", Driver: "", Timeout: 0, Status: StatusEnabled,
	}
	own := Channel{
		Name: "ch-own", Vendor: "openai", Model: "m-own", SupplierID: supplier.ID,
		BaseURL: "https://own.example.invalid/v1", APIKey: "sk-own-secret",
		Driver: "openai", Timeout: 7, Status: StatusEnabled,
	}
	standalone := Channel{
		Name: "ch-standalone", Vendor: "openai", Model: "m-standalone",
		BaseURL: "https://standalone.example.invalid/v1", APIKey: "sk-standalone-secret",
		Status: StatusEnabled,
	}
	dangling := Channel{
		Name: "ch-dangling", Vendor: "openai", Model: "m-dangling", SupplierID: 987654321,
		BaseURL: "https://dangling.example.invalid/v1", APIKey: "sk-dangling-secret",
		Status: StatusEnabled,
	}
	channels := []*Channel{&inherit, &own, &standalone, &dangling}
	for _, ch := range channels {
		if err := store.CreateChannel(ctx, ch); err != nil {
			t.Fatalf("造渠道 %s 失败: %v", ch.Name, err)
		}
	}
	// 渠道 timeout 的「留空」态就是 0（列默认 60 只兜「裸 SQL 不写该列」）：
	// Channel.Timeout 刻意不带 GORM 的 default 标签，Create 传 0 必须真落 0——
	// 否则「不指定超时 ⇒ 继承厂商」在这条路径上不可达（也让 create 与 update 口径分叉）。
	stored, err := store.GetChannel(ctx, inherit.ID)
	if err != nil {
		t.Fatalf("读取继承渠道失败: %v", err)
	}
	if stored.Timeout != 0 {
		t.Fatalf("Create(timeout=0) 落库 = %d，期望 0（GORM default 标签会让它被写成列默认 60）", stored.Timeout)
	}

	model := Model{
		Name: "gpt-inherit", Type: CallTypeChat, Strategy: StrategyPriority,
		PriceInput: "1.000000", PriceOutput: "2.000000", Status: StatusEnabled,
	}
	if err := store.CreateModel(ctx, &model); err != nil {
		t.Fatalf("造模型失败: %v", err)
	}
	binds := make([]ModelChannel, 0, len(channels))
	for i, ch := range channels {
		binds = append(binds, ModelChannel{
			ChannelID: uint64(ch.ID), Priority: i + 1, Weight: 1, Status: StatusEnabled,
		})
	}
	if _, err := store.ReplaceBindings(ctx, model.ID, binds); err != nil {
		t.Fatalf("建绑定失败: %v", err)
	}

	rows, err := store.Candidates(ctx, model.ID)
	if err != nil {
		t.Fatalf("候选集查询失败: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("候选数 = %d，期望 4（%+v）", len(rows), rows)
	}
	byID := map[uint64]Candidate{}
	for _, row := range rows {
		byID[row.ChannelID] = row
	}

	// ① 渠道留空 + 有厂商 ⇒ 四列全取厂商（这是本包的核心新语义）。
	got := byID[uint64(inherit.ID)]
	if got.BaseURL != supplier.BaseURL || got.APIKey != supplier.APIKey ||
		got.Driver != supplier.Driver || got.Timeout != supplier.Timeout {
		t.Fatalf("继承未生效: base_url=%q api_key=%q driver=%q timeout=%d，期望 %q/%q/%q/%d",
			got.BaseURL, got.APIKey, got.Driver, got.Timeout,
			supplier.BaseURL, supplier.APIKey, supplier.Driver, supplier.Timeout)
	}
	if got.SupplierID != uint64(supplier.ID) {
		t.Fatalf("候选行未带回 supplier_id: %d", got.SupplierID)
	}

	// ② 渠道显式填值 ⇒ 渠道优先（含 driver/timeout 这两个「非空即不再继承」的项）。
	got = byID[uint64(own.ID)]
	if got.BaseURL != own.BaseURL || got.APIKey != own.APIKey || got.Driver != own.Driver || got.Timeout != own.Timeout {
		t.Fatalf("渠道显式值未优先: %+v（期望 base_url=%q driver=%q timeout=%d）", got, own.BaseURL, own.Driver, own.Timeout)
	}

	// ③ 无厂商 ⇒ 回落现行为（渠道自身值原样；timeout=0 仍由 Service.resolveChannel 兜插件配置）。
	got = byID[uint64(standalone.ID)]
	if got.BaseURL != standalone.BaseURL || got.APIKey != standalone.APIKey ||
		got.Driver != "" || got.Timeout != 0 || got.SupplierID != 0 {
		t.Fatalf("无厂商渠道不应用任何继承、也不该被填值: %+v", got)
	}

	// ④ 悬空 supplier_id ⇒ 无从继承（LEFT JOIN 四列全空），渠道自身值原样。
	got = byID[uint64(dangling.ID)]
	if got.BaseURL != dangling.BaseURL || got.APIKey != dangling.APIKey || got.Driver != "" || got.Timeout != 0 {
		t.Fatalf("悬空厂商不得把渠道值清空或替换: %+v", got)
	}
}

// TestGetChannelEffectiveReadsInheritedCredentialDB 单行生效读（探活路径）：探活与真实调用
// 必须看到同一份凭证——渠道留空挂厂商时探活不能拿空凭证 fail-closed。
func TestGetChannelEffectiveReadsInheritedCredentialDB(t *testing.T) {
	db := pluginAITestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	supplier := Supplier{
		Name: "厂商乙", Vendor: "qwen",
		BaseURL: "https://supplier-b.example.invalid/v1", APIKey: "sk-supplier-b",
		Driver: "dashscope", Timeout: 30, Status: StatusEnabled,
	}
	if err := store.CreateSupplier(ctx, &supplier); err != nil {
		t.Fatalf("造厂商失败: %v", err)
	}
	empty := Channel{Name: "ch-probe-inherit", Vendor: "qwen", Model: "m-probe", SupplierID: supplier.ID, Status: StatusEnabled}
	if err := store.CreateChannel(ctx, &empty); err != nil {
		t.Fatalf("造渠道失败: %v", err)
	}
	// 渠道留空（base_url/api_key/driver/timeout 全零值）挂厂商：探活读生效行必须拿到厂商凭证。

	effective, err := store.GetChannelEffective(ctx, empty.ID)
	if err != nil {
		t.Fatalf("读取生效行失败: %v", err)
	}
	if effective.BaseURL != supplier.BaseURL || effective.APIKey != supplier.APIKey {
		t.Fatalf("生效行未继承厂商凭证: base_url=%q api_key=%q", effective.BaseURL, effective.APIKey)
	}
	if effective.Driver != supplier.Driver || effective.Timeout != supplier.Timeout {
		t.Fatalf("生效行未继承厂商驱动/超时: driver=%q timeout=%d", effective.Driver, effective.Timeout)
	}
	// 渠道自身的非继承列必须原样带出（探活用 Name/Model/ID 做上下文与写回）。
	if effective.ID != empty.ID || effective.Name != empty.Name || effective.Model != empty.Model ||
		effective.SupplierID != supplier.ID {
		t.Fatalf("生效行的渠道自身列不对: %+v", effective)
	}
	// 表单回显（GetChannel）读的仍是渠道自身列：留空 = 继承，不被回填固化成渠道值。
	raw, err := store.GetChannel(ctx, empty.ID)
	if err != nil {
		t.Fatalf("读取渠道自身行失败: %v", err)
	}
	if raw.BaseURL != "" || raw.APIKey != "" || raw.Driver != "" || raw.Timeout != 0 {
		t.Fatalf("渠道自身列被生效值污染（一次保存就会把继承固化成渠道凭证）: %+v", raw)
	}

	// 不存在的渠道：探活据此回「渠道 X 不存在」，不能误报成「凭证未配」。
	if _, err := store.GetChannelEffective(ctx, 987654321); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("不存在的渠道应返回 ErrRecordNotFound，实际 %v", err)
	}
}

// ===== 库级：渠道 create→update→get→list 四列往返（走真 handler + 真 SQL）=====

// TestChannelCRUDFourColumnsRoundTripDB 渠道四列的完整往返：
// create（四列带值）→ get（回显 + api_key 掩码）→ update（掩码不回写、新值可改）→ list（四列带值）。
func TestChannelCRUDFourColumnsRoundTripDB(t *testing.T) {
	db := pluginAITestDB(t)
	store := NewStore(db)
	engine := channelSchemaEngine(channelSchemaDeps(db))
	ctx := context.Background()

	supplier := Supplier{
		Name: "厂商丙", Vendor: "openai", BaseURL: "https://supplier-c.example.invalid/v1",
		APIKey: "sk-supplier-c", Driver: "responses", Timeout: 25, Status: StatusEnabled,
	}
	if err := store.CreateSupplier(ctx, &supplier); err != nil {
		t.Fatalf("造厂商失败: %v", err)
	}

	// ① create：四列随请求落库（含挂厂商）。
	createBody := fmt.Sprintf(`{"name":"四列渠道","model":"gpt-four","base_url":"https://api.example.invalid/v1",
		"api_key":"sk-channel-secret","driver":"responses","supplier_id":%d,"rpm_limit":600,"tpm_limit":120000}`, supplier.ID)
	recorder := doRequest(engine, http.MethodPost, "/admin/plugin/ai/channel/create", createBody, nil)
	envelope := decodeAdmin(t, recorder)
	if envelope.Code != 0 {
		t.Fatalf("create 失败: code=%d message=%s", envelope.Code, envelope.Message)
	}
	var created struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(envelope.Data, &created); err != nil {
		t.Fatalf("create 回执 data 解析失败: %v（%s）", err, envelope.Data)
	}
	if created.ID <= 0 {
		t.Fatalf("create 未回 ID: %+v", created)
	}

	row, err := store.GetChannel(ctx, created.ID)
	if err != nil {
		t.Fatalf("读取新建渠道失败: %v", err)
	}
	if row.Driver != "responses" || row.SupplierID != supplier.ID || row.RPMLimit != 600 || row.TPMLimit != 120000 {
		t.Fatalf("create 未落四列: driver=%q supplier_id=%d rpm=%d tpm=%d",
			row.Driver, row.SupplierID, row.RPMLimit, row.TPMLimit)
	}

	// ② get：四列回显 + api_key 只出掩码（明文永不出接口）。
	getPath := fmt.Sprintf("/admin/plugin/ai/channel/get/%d", created.ID)
	envelope = decodeAdmin(t, doRequest(engine, http.MethodGet, getPath, "", nil))
	if envelope.Code != 0 {
		t.Fatalf("get 失败: code=%d message=%s", envelope.Code, envelope.Message)
	}
	var getData struct {
		Row channelSchemaRow `json:"row"`
	}
	if err := json.Unmarshal(envelope.Data, &getData); err != nil {
		t.Fatalf("get 回执解析失败: %v（%s）", err, envelope.Data)
	}
	if getData.Row.Driver != "responses" || getData.Row.SupplierID != supplier.ID ||
		getData.Row.RPMLimit != 600 || getData.Row.TPMLimit != 120000 {
		t.Fatalf("get 回显缺四列: %+v", getData.Row)
	}
	if getData.Row.APIKey == "sk-channel-secret" || getData.Row.APIKey == "" {
		t.Fatalf("get 必须回掩码而不是明文/空串: %q", getData.Row.APIKey)
	}
	if !strings.Contains(getData.Row.APIKey, "*") || !getData.Row.HasAPIKey {
		t.Fatalf("掩码形状或 has_api_key 不对: %q has=%v", getData.Row.APIKey, getData.Row.HasAPIKey)
	}

	// ③ update：改四列 + 回填掩码 ⇒ 掩码不回写（api_key 保持原明文）、四列按新值落库。
	masked := getData.Row.APIKey
	updateBody := fmt.Sprintf(`{"driver":"openai","supplier_id":0,"rpm_limit":0,"tpm_limit":30,"api_key":%q}`, masked)
	updatePath := fmt.Sprintf("/admin/plugin/ai/channel/update/%d", created.ID)
	envelope = decodeAdmin(t, doRequest(engine, http.MethodPost, updatePath, updateBody, nil))
	if envelope.Code != 0 {
		t.Fatalf("update 失败: code=%d message=%s", envelope.Code, envelope.Message)
	}
	row, err = store.GetChannel(ctx, created.ID)
	if err != nil {
		t.Fatalf("更新后读取渠道失败: %v", err)
	}
	if row.Driver != "openai" || row.SupplierID != 0 || row.RPMLimit != 0 || row.TPMLimit != 30 {
		t.Fatalf("update 未落四列新值: driver=%q supplier_id=%d rpm=%d tpm=%d",
			row.Driver, row.SupplierID, row.RPMLimit, row.TPMLimit)
	}
	if row.APIKey != "sk-channel-secret" {
		t.Fatalf("掩码回填被写回（api_key 变成 %q）——掩码永不回写", row.APIKey)
	}

	// ④ update：显式零值也要落库（解绑/清零是四列的合法状态，不能被零值省略吃掉）。
	clearBody := `{"driver":"","supplier_id":0,"rpm_limit":0,"tpm_limit":0}`
	envelope = decodeAdmin(t, doRequest(engine, http.MethodPost, updatePath, clearBody, nil))
	if envelope.Code != 0 {
		t.Fatalf("清零 update 失败: code=%d message=%s", envelope.Code, envelope.Message)
	}
	row, err = store.GetChannel(ctx, created.ID)
	if err != nil {
		t.Fatalf("清零后读取渠道失败: %v", err)
	}
	if row.Driver != "" || row.SupplierID != 0 || row.RPMLimit != 0 || row.TPMLimit != 0 {
		t.Fatalf("显式零值未落库: driver=%q supplier_id=%d rpm=%d tpm=%d",
			row.Driver, row.SupplierID, row.RPMLimit, row.TPMLimit)
	}

	// ⑤ list：四列带值回显（含 api_key 掩码），且筛选面认 supplier_id/driver。
	restoreBody := fmt.Sprintf(`{"driver":"responses","supplier_id":%d,"rpm_limit":600,"tpm_limit":120000}`, supplier.ID)
	envelope = decodeAdmin(t, doRequest(engine, http.MethodPost, updatePath, restoreBody, nil))
	if envelope.Code != 0 {
		t.Fatalf("恢复四列失败: code=%d message=%s", envelope.Code, envelope.Message)
	}
	listBody := fmt.Sprintf(`{"page":1,"limit":10,"wheres":[{"wheres":[{"field":"supplier_id","operator":"eq","value":%d}]}]}`, supplier.ID)
	envelope = decodeAdmin(t, doRequest(engine, http.MethodPost, "/admin/plugin/ai/channel/list", listBody, nil))
	if envelope.Code != 0 {
		t.Fatalf("list 失败: code=%d message=%s", envelope.Code, envelope.Message)
	}
	var listData struct {
		List  []channelSchemaRow `json:"list"`
		Total int64              `json:"total"`
	}
	if err := json.Unmarshal(envelope.Data, &listData); err != nil {
		t.Fatalf("list 回执解析失败: %v（%s）", err, envelope.Data)
	}
	if listData.Total != 1 || len(listData.List) != 1 {
		t.Fatalf("按 supplier_id 筛选应恰好命中 1 条，实际 total=%d len=%d", listData.Total, len(listData.List))
	}
	listed := listData.List[0]
	if listed.ID != created.ID || listed.Driver != "responses" || listed.SupplierID != supplier.ID ||
		listed.RPMLimit != 600 || listed.TPMLimit != 120000 {
		t.Fatalf("list 回显缺四列: %+v", listed)
	}
	if listed.APIKey == "sk-channel-secret" {
		t.Fatal("list 不得回显明文 api_key")
	}
}

// ===== 库级：usage 写口契约（B4）=====

// TestStoreUpsertUsageConflictOverwriteRefreshesBilledPartialDB B4 的可鉴别断言：
// (request_id,attempt) 冲突覆盖必须能**刷新** billed_partial——先写 0 的行，再写同一
// (request_id,attempt) 且 billed_partial=1 ⇒ 终态必须是 1。
//
// 为什么需要它：分支③（流式「有块无收尾」）的补偿结算与分支④ 的失败行共用
// (request_id,attempt)，若 billed_partial 不在 UpsertUsage 的 DoUpdates 列清单里，
// 第二次写只更新业务列、审计标记永远停在 0 ⇒ 「已部分计费」与真实扣费分叉且事后无法对账。
// INSERT 路径（新行写 1 读回 1）由 usage_partial_db_test.go 覆盖，两边分工不重叠。
func TestStoreUpsertUsageConflictOverwriteRefreshesBilledPartialDB(t *testing.T) {
	db := pluginAITestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	const requestID = "req-partial-overwrite"
	base := Usage{
		RequestID:     requestID,
		Attempt:       0,
		ChannelID:     11,
		ChannelName:   "主渠道",
		CallType:      CallTypeChat,
		BizType:       "api",
		Source:        publicSource,
		Stream:        1,
		TokensSource:  TokensSourceNone,
		Cost:          "0.000000",
		Currency:      DefaultCurrency,
		Status:        UsageStatusFail,
		ErrorCode:     "stream_interrupted",
		ErrorClass:    ErrorClassUpstream,
		BilledPartial: 0,
	}

	// ① 第一次写：失败行（billed_partial=0，显式落 0 以钉住「先写 0」这个前置）。
	if err := store.UpsertUsage(ctx, &base); err != nil {
		t.Fatalf("第一次写失败行失败（billed_partial 列不存在时这里是 42703）: %v", err)
	}
	if got := channelSchemaReadBilledPartial(t, db, requestID); got != 0 {
		t.Fatalf("第一次写后 billed_partial = %d，期望 0", got)
	}

	// ② 第二次写：同一 (request_id,attempt)，补偿后 billed_partial=1 + 计数/成本一起刷新。
	partial := base
	partial.BilledPartial = 1
	partial.TokensSource = TokensSourceUsage
	partial.PromptTokens = 10
	partial.CompletionTokens = 4
	partial.TotalTokens = 14
	partial.Cost = "0.000018"
	partial.LatencyMS = 1234
	partial.ErrorCode = "stream_interrupted"
	if err := store.UpsertUsage(ctx, &partial); err != nil {
		t.Fatalf("冲突覆盖写失败: %v", err)
	}

	// ③ 终态：必须恰好 1 行、billed_partial=1、业务列已被覆盖（不是新增一行）。
	chain, err := store.UsageDetailChain(ctx, requestID)
	if err != nil {
		t.Fatalf("读尝试链失败: %v", err)
	}
	if len(chain) != 1 {
		t.Fatalf("(request_id,attempt) 幂等覆盖应恰好 1 行，实际 %d（%+v）", len(chain), chain)
	}
	if chain[0].BilledPartial != 1 {
		t.Fatalf("终态 billed_partial = %d，期望 1——该列不在 DoUpdates 列清单时这里必然是 0（B4 的鉴别点）",
			chain[0].BilledPartial)
	}
	if chain[0].TotalTokens != 14 || chain[0].TokensSource != TokensSourceUsage || chain[0].Cost != "0.000018" {
		t.Fatalf("覆盖写未刷新计数/成本列: %+v", chain[0])
	}
	if got := channelSchemaReadBilledPartial(t, db, requestID); got != 1 {
		t.Fatalf("库中终态 billed_partial = %d，期望 1", got)
	}

	// ④ 反向：覆盖写也必须能把标记刷回 0（审计标记是状态而不是一次性的「置位」）。
	plain := base
	plain.BilledPartial = 0
	plain.Stream = 0
	if err := store.UpsertUsage(ctx, &plain); err != nil {
		t.Fatalf("覆盖写回 0 失败: %v", err)
	}
	if got := channelSchemaReadBilledPartial(t, db, requestID); got != 0 {
		t.Fatalf("覆盖写回 0 后 billed_partial = %d，期望 0", got)
	}
}

// channelSchemaReadBilledPartial 直读库中的 billed_partial（不经 Usage 结构体，
// 避免「结构体字段值」与「库里真值」混为一谈）。
func channelSchemaReadBilledPartial(t *testing.T, db *gorm.DB, requestID string) int {
	t.Helper()
	var value sql.NullInt64
	err := db.Raw(`SELECT "billed_partial" FROM `+quoteIdent(table("plugin_ai_usage"))+`
		WHERE "request_id" = ? AND "attempt" = 0`, requestID).Row().Scan(&value)
	if err != nil {
		t.Fatalf("读 billed_partial 失败: %v", err)
	}
	if !value.Valid {
		t.Fatalf("request_id=%s 的行不存在或 billed_partial 为 NULL", requestID)
	}
	return int(value.Int64)
}
