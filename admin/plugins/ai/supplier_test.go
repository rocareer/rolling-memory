// plugin: ai v0.2.0
// supplier_test.go 是厂商资源面的**无库**用例：字段收敛（name 必填 / timeout clamp / status 闭集 /
// base_url 与 driver 形状）、掩码不回写、删除保护的文案与错误类型、wheres 白名单拒绝违例、
// 路由链与匿名拒绝、出口脱敏（明文永不出接口）。
//
// 需要真库的部分（CRUD 往返、channel_count、删除保护的事务性）在 supplier_db_test.go（DSN 门控）。
//
// 复用 handler_test.go 的通用脚手架（doRequest / decodeAdmin / adminEnvelope / handlerNames /
// hasHandlerNamed：那是本包全部 handler 用例共用的稳定脚手架，不是本文件私有的），
// 厂商专属的 helper（supplierDeps / newSupplierAdminEngine）在本文件内声明，避免与同包其它测试文件撞名。
package ai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// supplierDeps 构造接口层依赖：store 由入参给出；nil 表示「库未初始化」——与生产 defaultStore
// 完全同形（返回 nil + ErrStoreUnavailable），于是 storeOrFail 先写 fail-closed 回执再返回 nil。
func supplierDeps(store *Store) deps {
	loadStore := func(context.Context) (*Store, error) { return store, nil }
	if store == nil {
		loadStore = func(context.Context) (*Store, error) { return nil, ErrStoreUnavailable }
	}
	return deps{
		loadSettings: func(context.Context) (pluginSettings, error) { return parsePluginSettings(nil), nil },
		loadStore:    loadStore,
		loadService: func(context.Context, pluginSettings) (*Service, error) {
			return nil, errors.New("厂商面无编排层依赖")
		},
	}
}

// newSupplierAdminEngine 挂厂商 5 条路由的**完整链**（AdminAuth + AdminPermission + 业务），
// 组前缀与宿主接线同形（admin 组 → plugin/<名> 子组）——授权链用例专用。
func newSupplierAdminEngine(d deps) *gin.Engine {
	return newSupplierEngine(d, true)
}

// newSupplierHandlerEngine 只挂业务处理器（不带中间件）：验的是协议与 fail-closed 回执，
// 不验授权链（那条由 TestSupplierAdminRoutesChain / TestSupplierRoutesRejectAnonymous 覆盖）。
func newSupplierHandlerEngine(d deps) *gin.Engine {
	return newSupplierEngine(d, false)
}

func newSupplierEngine(d deps, withMiddleware bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	group := engine.Group("/admin/plugin/" + Name)
	for _, route := range d.supplierAdminRoutes() {
		if withMiddleware {
			group.Handle(route.Method, route.Path, route.chain()...)
			continue
		}
		group.Handle(route.Method, route.Path, route.Handler)
	}
	return engine
}

// ===== 字段收敛 =====

// TestSupplierCreateValidation 新增入参的必填/闭集/clamp 逐条断言（纯函数，无库）。
func TestSupplierCreateValidation(t *testing.T) {
	text := func(s string) *string { return &s }

	row, err := (supplierSaveRequest{Name: text("  厂商A  "), Vendor: text("deepseek")}).supplier()
	if err != nil {
		t.Fatalf("最小合法入参（只有 name）必须通过: %v", err)
	}
	if row.Name != "厂商A" {
		t.Fatalf("name 必须去首尾空白，实际 %q", row.Name)
	}
	if row.Vendor != "deepseek" || row.Timeout != DefaultRequestTimeout || row.Status != StatusEnabled {
		t.Fatalf("缺省收敛不符：vendor=%q timeout=%d status=%d（期望 deepseek/%d/1）",
			row.Vendor, row.Timeout, row.Status, DefaultRequestTimeout)
	}
	if row.BaseURL != "" || row.APIKey != "" || row.Driver != "" {
		t.Fatalf("未给的字段必须落空串（空=未配，走继承/缺省链）: %+v", row)
	}

	// vendor 闭集（与渠道共用词表）；缺省 openai。
	row, err = (supplierSaveRequest{Name: text("厂商B")}).supplier()
	if err != nil || row.Vendor != "openai" {
		t.Fatalf("vendor 缺省应为 openai: %q err=%v", row.Vendor, err)
	}
	if _, err := (supplierSaveRequest{Name: text("厂商B"), Vendor: text("acme")}).supplier(); err == nil {
		t.Fatal("vendor 闭集外的值必须被拒")
	}

	// name：缺省、空串、纯空白、超长（>50）都要拒。
	for _, bad := range []supplierSaveRequest{
		{},
		{Name: text("")},
		{Name: text("   ")},
		{Name: text(strings.Repeat("厂", 51))},
	} {
		if _, err := bad.supplier(); err == nil {
			t.Fatalf("非法 name 必须被拒: %+v", bad)
		}
	}

	// base_url：空串允许（未配）；非空必须过 ValidateBaseURL。
	if _, err := (supplierSaveRequest{Name: text("厂商C"), BaseURL: text("ftp://example.invalid")}).supplier(); err == nil {
		t.Fatal("base_url 只支持 http/https：ftp 必须被拒")
	}
	row, err = (supplierSaveRequest{Name: text("厂商C"), BaseURL: text("https://api.example.invalid/v1")}).supplier()
	if err != nil || row.BaseURL != "https://api.example.invalid/v1" {
		t.Fatalf("合法 base_url 必须原样落库: %q err=%v", row.BaseURL, err)
	}

	// timeout：缺省 60、负数收敛 0、超上界收敛 600（clamp 0..600）。
	timeoutCases := []struct {
		in   *int
		want int
	}{
		{nil, DefaultRequestTimeout},
		{intPtr(-5), 0},
		{intPtr(0), 0},
		{intPtr(90), 90},
		{intPtr(9999), 600},
	}
	for _, c := range timeoutCases {
		row, err := (supplierSaveRequest{Name: text("厂商D"), Timeout: c.in}).supplier()
		if err != nil {
			t.Fatalf("timeout 收敛不该报错（in=%v）: %v", c.in, err)
		}
		if row.Timeout != c.want {
			t.Fatalf("timeout 收敛应为 %d，实际 %d（in=%v）", c.want, row.Timeout, c.in)
		}
	}

	// status 闭集：nil/0/1 通过，其余拒。
	row, err = (supplierSaveRequest{Name: text("厂商E"), Status: int16Ptr(0)}).supplier()
	if err != nil || row.Status != StatusDisabled {
		t.Fatalf("显式 status=0（停用）必须通过且落 0: %d err=%v", row.Status, err)
	}
	for _, bad := range []int16{2, -1, 9} {
		if _, err := (supplierSaveRequest{Name: text("厂商E"), Status: int16Ptr(bad)}).supplier(); err == nil {
			t.Fatalf("status=%d 在闭集外必须被拒", bad)
		}
	}

	// api_key：空串允许（未配）；含 '*' 的掩码回填在**新建**时拒（不能把掩码当密钥落库）。
	if _, err := (supplierSaveRequest{Name: text("厂商F"), APIKey: text("  ")}).supplier(); err != nil {
		t.Fatalf("空 api_key 必须允许（未配=调用期 fail-closed）: %v", err)
	}
	if _, err := (supplierSaveRequest{Name: text("厂商F"), APIKey: text("sk-****abcd")}).supplier(); err == nil {
		t.Fatal("新建时把掩码当密钥必须被拒（否则该厂商永久带星号调用）")
	}
	row, err = (supplierSaveRequest{Name: text("厂商F"), APIKey: text("  sk-live-abcdefghijkl  ")}).supplier()
	if err != nil || row.APIKey != "sk-live-abcdefghijkl" {
		t.Fatalf("明文 api_key 必须去空白后落库: %q err=%v", row.APIKey, err)
	}

	// driver：空串允许；允许字母/数字/下划线/点/连字符；空白与斜杠等一律拒；
	// 长度按迁移 3 的列宽 varchar(64) 卡（超列宽的写入会在库里报 22001 让整行失败）。
	row, err = (supplierSaveRequest{Name: text("厂商G"), Driver: text("  dashscope_tts  ")}).supplier()
	if err != nil || row.Driver != "dashscope_tts" {
		t.Fatalf("driver 名必须去空白后落库: %q err=%v", row.Driver, err)
	}
	for _, bad := range []string{"open ai", "open/ai", `open"ai`, strings.Repeat("d", supplierDriverMax+1)} {
		if _, err := (supplierSaveRequest{Name: text("厂商G"), Driver: text(bad)}).supplier(); err == nil {
			t.Fatalf("非法 driver %q 必须被拒", bad)
		}
	}
	// 恰好到列宽上限的 driver 必须放行（否则上限形同虚设）。
	row, err = (supplierSaveRequest{Name: text("厂商G"), Driver: text(strings.Repeat("d", supplierDriverMax))}).supplier()
	if err != nil || len([]rune(row.Driver)) != supplierDriverMax {
		t.Fatalf("恰为 %d 字符的 driver 必须放行: len=%d err=%v", supplierDriverMax, len([]rune(row.Driver)), err)
	}

	// remark 按 255 字符截断（varchar(255) 计字符）。
	row, err = (supplierSaveRequest{Name: text("厂商H"), Remark: text(strings.Repeat("备", 300))}).supplier()
	if err != nil || len([]rune(row.Remark)) != supplierRemarkMax {
		t.Fatalf("remark 必须按列宽截断到 %d 字符，实际 %d err=%v", supplierRemarkMax, len([]rune(row.Remark)), err)
	}
}

// TestSupplierPatchOnlyCarriesGivenFields 编辑补丁：缺省字段不进补丁、显式零值进补丁。
func TestSupplierPatchOnlyCarriesGivenFields(t *testing.T) {
	patch, err := (supplierSaveRequest{}).patch()
	if err != nil {
		t.Fatalf("空补丁不该报错: %v", err)
	}
	if patch.Name != nil || patch.Vendor != nil || patch.BaseURL != nil || patch.APIKey != nil ||
		patch.Driver != nil || patch.Timeout != nil || patch.Status != nil || patch.Remark != nil {
		t.Fatalf("缺省字段一律不得进补丁（否则会把列清空）: %+v", patch)
	}

	zero := 0
	var disabled int16 = StatusDisabled
	patch, err = (supplierSaveRequest{Timeout: &zero, Status: &disabled}).patch()
	if err != nil {
		t.Fatalf("显式零值必须通过: %v", err)
	}
	if patch.Timeout == nil || *patch.Timeout != 0 || patch.Status == nil || *patch.Status != StatusDisabled {
		t.Fatalf("显式零值必须进补丁（区分「缺省」与「设为 0」）: %+v", patch)
	}
}

// TestSupplierPatchNeverWritesBackMaskedKey 「api_key 空串或含 '*' ⇒ 不修改」的三态断言。
func TestSupplierPatchNeverWritesBackMaskedKey(t *testing.T) {
	blank := ""
	patch, err := (supplierSaveRequest{APIKey: &blank}).patch()
	if err != nil {
		t.Fatalf("空串 api_key 必须容忍（前端清空输入框）: %v", err)
	}
	if patch.APIKey != nil {
		t.Fatalf("空串 api_key 不得进补丁（空 ≠ 清空；清空走显式语义才安全）: %q", *patch.APIKey)
	}

	masked := maskSecret("sk-live-abcdefghijkl")
	patch, err = (supplierSaveRequest{APIKey: &masked}).patch()
	if err != nil {
		t.Fatalf("掩码回填不得报错: %v", err)
	}
	if patch.APIKey != nil {
		t.Fatalf("掩码回填必须被识别为「不修改」，实际写入 %q", *patch.APIKey)
	}

	plain := "  sk-new-secret-000000  "
	patch, err = (supplierSaveRequest{APIKey: &plain}).patch()
	if err != nil {
		t.Fatalf("明文密钥必须接受: %v", err)
	}
	if patch.APIKey == nil || *patch.APIKey != "sk-new-secret-000000" {
		t.Fatalf("明文密钥必须去空白后进补丁，实际 %+v", patch.APIKey)
	}
}

// ===== 删除保护（判定与文案）=====

// TestSupplierDeleteRefusal 删除保护是纯函数：渠道数 >0 即拒（含可读文案与可判定错误类型）。
func TestSupplierDeleteRefusal(t *testing.T) {
	if err := supplierDeleteRefusal(nil); err != nil {
		t.Fatalf("无引用必须放行: %v", err)
	}
	if err := supplierDeleteRefusal([]supplierReference{{ID: 1, Name: "厂商A", Channels: 0}}); err != nil {
		t.Fatalf("渠道数为 0 不算引用，必须放行: %v", err)
	}

	err := supplierDeleteRefusal([]supplierReference{
		{ID: 1, Name: "厂商A", Channels: 2},
		{ID: 2, Name: "厂商B", Channels: 0},
		{ID: 3, Name: "厂商C", Channels: 5},
	})
	if err == nil {
		t.Fatal("被引用的厂商必须拒删")
	}
	if !isSupplierInUse(err) || !errors.Is(err, ErrSupplierInUse) {
		t.Fatalf("错误类型必须可判定（handler 靠它选回执）: %v", err)
	}
	message := err.Error()
	for _, want := range []string{"厂商A", "厂商C", "2 个渠道", "5 个渠道", "共 7 个", "解除挂靠"} {
		if !strings.Contains(message, want) {
			t.Fatalf("拒绝文案缺 %q（要给出可执行的下一步）: %s", want, message)
		}
	}
	if strings.Contains(message, "厂商B") {
		t.Fatalf("未被引用的厂商不该出现在拒绝文案里: %s", message)
	}
}

// ===== wheres 白名单 =====

// TestSupplierWhereWhitelist 高级检索契约：字段白名单 + 操作符词表 + IN 元素上限，逐类违例必须被拒。
func TestSupplierWhereWhitelist(t *testing.T) {
	if _, ok := supplierWhereFields["api_key"]; ok {
		t.Fatal("api_key 不得进筛选白名单（密钥不是可检索维度）")
	}

	// 合法组合：文本模糊、整数有序比较、二值等值、IN。
	legal := []whereGroup{
		{Wheres: []whereCondition{{Field: "name", Operator: "ILIKE", Value: "厂商"}}},
		{Wheres: []whereCondition{{Field: "timeout", Operator: "egt", Value: 60}}},
		{Wheres: []whereCondition{{Field: "status", Operator: "eq", Value: 1}}},
		{Wheres: []whereCondition{{Field: "id", Operator: "IN", Value: []any{1, 2, 3}}}},
		{Wheres: []whereCondition{{Field: "remark", Operator: "NULL"}}},
	}
	if kept, err := validateWhereGroups(supplierWhereFields, legal); err != nil || len(kept) != len(legal) {
		t.Fatalf("合法 wheres 必须全部保留: kept=%d err=%v", len(kept), err)
	}
	if kept, err := validateWhereGroups(supplierWhereFields, []whereGroup{{}}); err != nil || len(kept) != 0 {
		t.Fatalf("空组按宿主管家口径忽略: kept=%d err=%v", len(kept), err)
	}

	// 白名单外字段（含 api_key——这是本表最该拦的字段）。
	for _, field := range []string{"api_key", "channel_count", "no_such_column"} {
		_, err := validateWhereGroups(supplierWhereFields, []whereGroup{
			{Wheres: []whereCondition{{Field: field, Operator: "eq", Value: "x"}}},
		})
		if err == nil || !strings.Contains(err.Error(), "白名单") {
			t.Fatalf("字段 %q 必须被白名单拒且文案可读，实际 %v", field, err)
		}
	}

	// 词表外操作符。
	_, err := validateWhereGroups(supplierWhereFields, []whereGroup{
		{Wheres: []whereCondition{{Field: "name", Operator: "REGEXP", Value: "x"}}},
	})
	if err == nil || !strings.Contains(err.Error(), "词表") {
		t.Fatalf("词表外操作符必须被拒，实际 %v", err)
	}

	// 类型不兼容：模糊类只许文本列、有序比较不许二值列。
	if _, err := validateWhereGroups(supplierWhereFields, []whereGroup{
		{Wheres: []whereCondition{{Field: "status", Operator: "LIKE", Value: "1"}}},
	}); err == nil {
		t.Fatal("二值列不支持 LIKE，必须被拒")
	}
	if _, err := validateWhereGroups(supplierWhereFields, []whereGroup{
		{Wheres: []whereCondition{{Field: "status", Operator: "gt", Value: 0}}},
	}); err == nil {
		t.Fatal("二值列不支持有序比较，必须被拒")
	}

	// IN 元素超限（>maxWhereInValues）。
	values := make([]any, 0, maxWhereInValues+1)
	for i := 0; i <= maxWhereInValues; i++ {
		values = append(values, i)
	}
	_, err = validateWhereGroups(supplierWhereFields, []whereGroup{
		{Wheres: []whereCondition{{Field: "id", Operator: "IN", Value: values}}},
	})
	if err == nil || !strings.Contains(err.Error(), "过长") {
		t.Fatalf("超限 IN 必须被拒且文案可读，实际 %v", err)
	}
}

// TestSupplierListRequestFilter 列表入参收敛：status 闭集、排序白名单（含 channel_count）、快搜键兼容。
func TestSupplierListRequestFilter(t *testing.T) {
	filter, groups, err := (supplierListRequest{
		listRequest: listRequest{Page: 2, Limit: 20, Sort: "channel_count", Order: "desc", Keyword: " 甲 "},
		Vendor:      "openai",
	}).filter()
	if err != nil || len(groups) != 0 {
		t.Fatalf("合法列表入参不该报错: %v", err)
	}
	if filter.Page != 2 || filter.Limit != 20 || filter.Sort != "channel_count" || filter.Vendor != "openai" || filter.Keyword != "甲" {
		t.Fatalf("列表筛选收敛不符: %+v", filter)
	}

	if _, _, err := (supplierListRequest{listRequest: listRequest{Sort: "api_key"}}).filter(); err == nil {
		t.Fatal("排序字段白名单外的值必须被拒（静默回落会让调用方以为排序已生效）")
	}
	bad := int16(2)
	if _, _, err := (supplierListRequest{Status: optionalStatus{Value: &bad}}).filter(); err == nil {
		t.Fatal("顶层 status 闭集外的值必须被拒")
	}

	// 快搜兼容宿主前端的 quickSearchKeywords 形状。
	filter, _, err = (supplierListRequest{listRequest: listRequest{
		QuickSearchKeywords: quickSearchKeywords{"乙"},
	}}).filter()
	if err != nil || filter.Keyword != "乙" {
		t.Fatalf("quickSearchKeywords 形状必须兼容: %+v err=%v", filter, err)
	}
}

// ===== 出口脱敏 =====

// TestSupplierRowMasksAPIKey 出口形状：掩码进 api_key，明文既不出 JSON 也不出现在行模型里。
func TestSupplierRowMasksAPIKey(t *testing.T) {
	const plain = "sk-live-abcdefghijkl"
	row := newSupplierRow(Supplier{ID: 7, Name: "厂商A", APIKey: plain, Status: StatusEnabled}, 3)

	payload, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("序列化出口行失败: %v", err)
	}
	if strings.Contains(string(payload), plain) {
		t.Fatalf("明文 api_key 不得出现在接口回执里: %s", payload)
	}
	var decoded struct {
		APIKey       string `json:"api_key"`
		HasAPIKey    bool   `json:"has_api_key"`
		ChannelCount int64  `json:"channel_count"`
		Status       int16  `json:"status"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("回执不是 JSON: %v", err)
	}
	if decoded.APIKey != maskSecret(plain) {
		t.Fatalf("api_key 必须是掩码，实际 %q（期望 %q）", decoded.APIKey, maskSecret(plain))
	}
	if !decoded.HasAPIKey || decoded.ChannelCount != 3 || decoded.Status != StatusEnabled {
		t.Fatalf("出口行字段不符: %+v", decoded)
	}

	// 行模型自身没有 api_key JSON 键（json:"-"）——任何地方误序列化都不会泄明文。
	raw, err := json.Marshal(Supplier{APIKey: plain})
	if err != nil {
		t.Fatalf("序列化行模型失败: %v", err)
	}
	if strings.Contains(string(raw), plain) || strings.Contains(string(raw), "api_key") {
		t.Fatalf("Supplier 行模型不得携带 api_key 键: %s", raw)
	}
	if mask := maskSecret(""); mask != "" {
		t.Fatalf("未配凭证的掩码必须是空串（前端据此显示「未配置」），实际 %q", mask)
	}
}

// ===== 路由链与匿名拒绝 =====

// TestSupplierAdminRoutesChain 5 条路由逐条验：AdminAuth + AdminPermission 都在链上、节点名与约定逐字相等。
func TestSupplierAdminRoutesChain(t *testing.T) {
	want := map[string]string{
		"POST /supplier/list":       "plugin/ai/supplier/read",
		"GET /supplier/get/:pk":     "plugin/ai/supplier/read",
		"POST /supplier/create":     "plugin/ai/supplier/create",
		"POST /supplier/update/:pk": "plugin/ai/supplier/update",
		"POST /supplier/delete":     "plugin/ai/supplier/delete",
	}
	routes := defaultDeps.supplierAdminRoutes()
	if len(routes) != len(want) {
		t.Fatalf("厂商路由应为 %d 条，实际 %d 条", len(want), len(routes))
	}
	for _, route := range routes {
		key := route.Method + " " + route.Path
		node, ok := want[key]
		if !ok {
			t.Fatalf("出现约定外的路由: %s", key)
		}
		if route.Node != node {
			t.Fatalf("%s 的权限节点应为 %q，实际 %q", key, node, route.Node)
		}
		names := handlerNames(route.chain())
		if len(names) != 3 {
			t.Fatalf("%s 的处理器链应为 3 段（AdminAuth + AdminPermission + 业务），实际 %d 段", key, len(names))
		}
		if !hasHandlerNamed(names, "AdminAuth") {
			t.Errorf("%s 未挂 AdminAuth: %v", key, names)
		}
		if !hasHandlerNamed(names, "AdminPermission") {
			t.Errorf("%s 未挂 AdminPermission（节点会变成装饰）: %v", key, names)
		}
	}
}

// TestSupplierRoutesRejectAnonymous 逐条无凭证请求：HTTP 200 + code=401/请先登录（宿主口径）。
func TestSupplierRoutesRejectAnonymous(t *testing.T) {
	engine := newSupplierAdminEngine(supplierDeps(NewStore(nil)))
	for _, route := range defaultDeps.supplierAdminRoutes() {
		path := "/admin/plugin/ai" + strings.ReplaceAll(route.Path, ":pk", "1")
		recorder := doRequest(engine, route.Method, path, "{}", nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s %s：后台面必须沿用宿主口径 HTTP 200，实际 %d", route.Method, path, recorder.Code)
		}
		envelope := decodeAdmin(t, recorder)
		if envelope.Code != http.StatusUnauthorized || envelope.Message != "请先登录" {
			t.Fatalf("%s %s：未带凭证应被 AdminAuth 拒（code=401/请先登录），实际 %+v", route.Method, path, envelope)
		}
	}
}

// ===== 无库 fail-closed =====

// TestSupplierHandlersFailClosedWithoutStore 无库环境下 5 条路由都必须 code=1 且文案可读（不是 panic）。
func TestSupplierHandlersFailClosedWithoutStore(t *testing.T) {
	engine := newSupplierHandlerEngine(supplierDeps(nil))
	cases := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/admin/plugin/ai/supplier/list", "{}"},
		{http.MethodGet, "/admin/plugin/ai/supplier/get/1", ""},
		{http.MethodPost, "/admin/plugin/ai/supplier/create", `{"name":"厂商A"}`},
		{http.MethodPost, "/admin/plugin/ai/supplier/update/1", `{"name":"厂商A"}`},
		{http.MethodPost, "/admin/plugin/ai/supplier/delete", `{"pks":["1"]}`},
	}
	for _, c := range cases {
		recorder := doRequest(engine, c.method, c.path, c.body, nil)
		envelope := decodeAdmin(t, recorder)
		if envelope.Code != 1 || envelope.Message == "" {
			t.Fatalf("%s %s：无库时必须 fail-closed（code=1 + 可读文案），实际 %+v", c.method, c.path, envelope)
		}
	}
}

// TestSupplierDeleteRequiresPks 删除入参收敛（纯函数）：空 pks 必须被拒（不能把「全删」当默认语义），
// 非正整数一律拒，正常列表放行。handler 只是把这条文案原样透出（无库时先被 storeOrFail 拦下，
// 故回执形状不在本用例断言面内）。
func TestSupplierDeleteRequiresPks(t *testing.T) {
	if _, err := (pksRequest{}).ids("厂商"); err == nil || !strings.Contains(err.Error(), "厂商") {
		t.Fatalf("空 pks 必须被拒且文案指向要删的资源，实际 err=%v", err)
	}
	// 非正整数一律拒（" 1 " 这类带空白的写法按宿主 handler 口径是容许的，故不在拒绝面内）。
	for _, bad := range []string{"0", "-1", "abc", ""} {
		if _, err := (pksRequest{Pks: []string{bad}}).ids("厂商"); err == nil {
			t.Fatalf("非法主键 %q 必须被拒", bad)
		}
	}
	ids, err := (pksRequest{Pks: []string{"3", "7"}}).ids("厂商")
	if err != nil || len(ids) != 2 || ids[0] != 3 || ids[1] != 7 {
		t.Fatalf("正常 pks 必须解析成 int64 列表: %v err=%v", ids, err)
	}
}

// int16Ptr 取 int16 的地址（测试专用；handler_test.go 已有 intPtr）。
func int16Ptr(value int16) *int16 { return &value }
