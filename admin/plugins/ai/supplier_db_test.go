// plugin: ai v0.2.0
// supplier_db_test.go 是厂商资源面的**真库**用例（DSN 门控，复用 store_db_test.go 的 pluginAITestDB：
// 按 <前缀>plugin_ai_% 清场 + 按编号升序应用全部 up 迁移）。
//
// 用法（**必须指向临时库**：用例会 drop/重建本插件的全部物理表）：
//
//	PLUGIN_AI_TEST_DSN='postgres://用户:口令@主机:端口/临时库?sslmode=disable' \
//	  go test -count=1 -run TestSupplier -v ./plugins/ai/
//
// 覆盖：接口往返（create → list 出现 → get 回显掩码 → update 改名/base_url → delete 后消失）、
// 删除保护（有渠道引用 ⇒ code=1 + 可读文案 + 库中行不变 + 整批拒绝不半删）、
// channel_count 与「渠道表 count(*) where supplier_id=该厂商」一致（含按渠道数排序）、
// wheres 高级检索在真库上的实际过滤效果与非法输入的可读拒绝。
//
// 关于迁移依赖（迁移 3）：厂商表 plugin_ai_supplier 与 channel.supplier_id 由
// 3_channel_driver_supplier_gate.up.sql 建立（该迁移落进目录后本文件无需任何改动即跑真表）。
// requireSupplierSchemaForTest 对「表与列都在」做硬校验——schema 缺失/半落地一律红，
// 绝不用临时 DDL 补一份可能与本项目迁移漂移的同构 schema 来把用例跑绿。
package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// supplierAITestDB 连临时库 → pluginAITestDB 的迁移与清场 → 校验厂商面 schema 由迁移建出。
func supplierAITestDB(t *testing.T) (*gorm.DB, *Store) {
	t.Helper()
	db := pluginAITestDB(t)
	requireSupplierSchemaForTest(t, db)
	return db, NewStore(db)
}

// requireSupplierSchemaForTest 要求迁移 3 的产物齐备（表 + channel.supplier_id）；
// 缺任一件或「只落一半」即红——用残缺 schema 跑出的绿没有意义。
func requireSupplierSchemaForTest(t *testing.T, db *gorm.DB) {
	t.Helper()
	hasTable := supplierTestTableExists(t, db)
	hasChannelColumn := supplierTestChannelColumnExists(t, db)
	if !hasTable || !hasChannelColumn {
		t.Fatalf("厂商面 schema 未由迁移建出（plugin_ai_supplier=%v / channel.supplier_id=%v）："+
			"检查迁移 3_channel_driver_supplier_gate.up.sql 是否已被应用", hasTable, hasChannelColumn)
	}
}

// supplierTestTableExists 探测厂商表是否存在（物理名经 table() 拼前缀）。
func supplierTestTableExists(t *testing.T, db *gorm.DB) bool {
	t.Helper()
	var name string
	if err := db.Raw(`SELECT COALESCE(to_regclass(?)::text, '')`, table("plugin_ai_supplier")).Scan(&name).Error; err != nil {
		t.Fatalf("探测厂商表失败: %v", err)
	}
	return name != ""
}

// supplierTestChannelColumnExists 探测渠道表的 supplier_id 列是否存在。
func supplierTestChannelColumnExists(t *testing.T, db *gorm.DB) bool {
	t.Helper()
	var count int64
	if err := db.Raw(`SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = ? AND column_name = 'supplier_id'`,
		table("plugin_ai_channel")).Scan(&count).Error; err != nil {
		t.Fatalf("探测 channel.supplier_id 失败: %v", err)
	}
	return count > 0
}

// supplierTestCountChannels 直接数「渠道表里 supplier_id=该厂商」的行数——channel_count 的对照读数。
func supplierTestCountChannels(t *testing.T, db *gorm.DB, supplierID int64) int64 {
	t.Helper()
	var count int64
	if err := db.Table(table("plugin_ai_channel")).Where(`"supplier_id" = ?`, supplierID).Count(&count).Error; err != nil {
		t.Fatalf("数渠道失败: %v", err)
	}
	return count
}

// supplierTestCountSuppliers 直接数厂商表的行数（「库中行不变」的对照读数）。
func supplierTestCountSuppliers(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var count int64
	if err := db.Table(table("plugin_ai_supplier")).Count(&count).Error; err != nil {
		t.Fatalf("数厂商失败: %v", err)
	}
	return count
}

// supplierTestSeedChannel 造一条挂在厂商下的渠道行（直接 SQL：本包只依赖渠道表的 supplier_id 列，
// 不依赖渠道写路径——那四列的往返由渠道自身的库级用例负责）。
func supplierTestSeedChannel(t *testing.T, db *gorm.DB, name string, supplierID int64) {
	t.Helper()
	if err := db.Exec(`INSERT INTO `+quoteIdent(table("plugin_ai_channel"))+
		` ("name", "supplier_id") VALUES (?, ?)`, name, supplierID).Error; err != nil {
		t.Fatalf("造渠道行失败: %v", err)
	}
}

// supplierTestClearChannels 清掉本用例造的渠道行。
func supplierTestClearChannels(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec(`DELETE FROM ` + quoteIdent(table("plugin_ai_channel"))).Error; err != nil {
		t.Fatalf("清渠道失败: %v", err)
	}
}

// supplierTestEngine 挂厂商 5 条路由的**业务处理器**（不带 AdminAuth：授权链由 supplier_test.go
// 的链路断言与匿名拒绝用例覆盖，这里验的是协议与数据面）。
func supplierTestEngine(store *Store) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	d := supplierDeps(store)
	for _, route := range d.supplierAdminRoutes() {
		engine.Handle(route.Method, route.Path, route.Handler)
	}
	return engine
}

// supplierTestCreate 走 POST /supplier/create 并返回新行 ID（失败即 Fatal）。
func supplierTestCreate(t *testing.T, engine *gin.Engine, body string) int64 {
	t.Helper()
	recorder := doRequest(engine, http.MethodPost, "/supplier/create", body, nil)
	envelope := decodeAdmin(t, recorder)
	if envelope.Code != 0 {
		t.Fatalf("create 应成功，实际 %+v（body=%s）", envelope, body)
	}
	var data struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(envelope.Data, &data); err != nil || data.ID <= 0 {
		t.Fatalf("create 回执缺 id: %s err=%v", envelope.Data, err)
	}
	return data.ID
}

// supplierTestRow 取 list 回执里的行（按 id 找）。
func supplierTestRow(t *testing.T, engine *gin.Engine, listBody string, id int64) (supplierRow, bool) {
	t.Helper()
	recorder := doRequest(engine, http.MethodPost, "/supplier/list", listBody, nil)
	envelope := decodeAdmin(t, recorder)
	if envelope.Code != 0 {
		t.Fatalf("list 应成功，实际 %+v", envelope)
	}
	var data struct {
		List  []supplierRow `json:"list"`
		Total int64         `json:"total"`
	}
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		t.Fatalf("list 回执解析失败: %v（原文 %s）", err, envelope.Data)
	}
	for _, row := range data.List {
		if row.ID == id {
			return row, true
		}
	}
	return supplierRow{}, false
}

// TestSupplierCRUDRoundTripAndDeleteProtectionDB 接口往返 + 删除保护（acceptance 的两条主判据）。
func TestSupplierCRUDRoundTripAndDeleteProtectionDB(t *testing.T) {
	db, store := supplierAITestDB(t)
	engine := supplierTestEngine(store)
	ctx := context.Background()

	const plainKey = "sk-sup-secret-0001"

	// ① create → list 出现（含掩码 api_key 与 channel_count=0）。
	id := supplierTestCreate(t, engine, fmt.Sprintf(
		`{"name":"厂商甲","vendor":"qwen","base_url":"https://api.example.invalid/v1","api_key":%q,"driver":"openai","timeout":90,"remark":"首建"}`,
		plainKey))
	row, ok := supplierTestRow(t, engine, `{"page":1,"limit":10}`, id)
	if !ok {
		t.Fatalf("create 后 list 里找不到厂商 %d（接口往返第一腿失败）", id)
	}
	if row.APIKeyMasked != maskSecret(plainKey) || !row.HasAPIKey {
		t.Fatalf("列表出口的 api_key 必须是掩码: %q（has_api_key=%v）", row.APIKeyMasked, row.HasAPIKey)
	}
	if row.ChannelCount != 0 || row.Name != "厂商甲" || row.Vendor != "qwen" || row.Timeout != 90 {
		t.Fatalf("列表行字段不符: %+v", row)
	}

	// ② get 回显：掩码 + 明文确实落在库里（掩码是出口行为，不是写入行为）。
	recorder := doRequest(engine, http.MethodGet, fmt.Sprintf("/supplier/get/%d", id), "", nil)
	envelope := decodeAdmin(t, recorder)
	if envelope.Code != 0 {
		t.Fatalf("get 应成功，实际 %+v", envelope)
	}
	var got struct {
		Row supplierRow `json:"row"`
	}
	if err := json.Unmarshal(envelope.Data, &got); err != nil {
		t.Fatalf("get 回执解析失败: %v", err)
	}
	if got.Row.APIKeyMasked != maskSecret(plainKey) {
		t.Fatalf("get 回显的 api_key 必须是掩码 %q，实际 %q", maskSecret(plainKey), got.Row.APIKeyMasked)
	}
	raw, err := store.GetSupplier(ctx, id)
	if err != nil {
		t.Fatalf("直读厂商失败: %v", err)
	}
	if raw.APIKey != plainKey {
		t.Fatalf("库里必须存明文（继承要真值），实际 %q", raw.APIKey)
	}

	// ③ update：改名 + 换 base_url；api_key 传掩码 ⇒ 必须原值不动（掩码不回写）。
	recorder = doRequest(engine, http.MethodPost, fmt.Sprintf("/supplier/update/%d", id),
		fmt.Sprintf(`{"name":"厂商甲改","base_url":"https://api2.example.invalid/v1","api_key":%q}`, maskSecret(plainKey)), nil)
	if envelope = decodeAdmin(t, recorder); envelope.Code != 0 {
		t.Fatalf("update 应成功，实际 %+v", envelope)
	}
	raw, err = store.GetSupplier(ctx, id)
	if err != nil {
		t.Fatalf("update 后直读失败: %v", err)
	}
	if raw.Name != "厂商甲改" || raw.BaseURL != "https://api2.example.invalid/v1" {
		t.Fatalf("改名/换 base_url 未生效: name=%q base_url=%q", raw.Name, raw.BaseURL)
	}
	if raw.APIKey != plainKey {
		t.Fatalf("掩码回填不得写入（api_key 应保持原明文），实际 %q", raw.APIKey)
	}
	if raw.Status != StatusEnabled || raw.Vendor != "qwen" || raw.Timeout != 90 {
		t.Fatalf("未提交的字段不得被清空: %+v", raw)
	}

	// ④ 删除保护：挂一条渠道 ⇒ delete 必须 code=1 + 可读文案，且厂商行仍在、渠道挂靠不动。
	supplierTestSeedChannel(t, db, "渠道甲", id)
	before := supplierTestCountSuppliers(t, db)
	recorder = doRequest(engine, http.MethodPost, "/supplier/delete", fmt.Sprintf(`{"pks":["%d"]}`, id), nil)
	envelope = decodeAdmin(t, recorder)
	if envelope.Code != 1 {
		t.Fatalf("被渠道引用的厂商必须拒删（code=1），实际 %+v", envelope)
	}
	for _, want := range []string{"厂商甲改", "仍被渠道引用", "1 个渠道", "解除挂靠"} {
		if !strings.Contains(envelope.Message, want) {
			t.Fatalf("拒绝文案缺 %q（要可执行）: %s", want, envelope.Message)
		}
	}
	if after := supplierTestCountSuppliers(t, db); after != before {
		t.Fatalf("拒删后厂商表行数必须不变（%d → %d）", before, after)
	}
	if _, err := store.GetSupplier(ctx, id); err != nil {
		t.Fatalf("拒删后该厂商行必须仍在: %v", err)
	}
	if count := supplierTestCountChannels(t, db, id); count != 1 {
		t.Fatalf("拒删不得顺手解除渠道挂靠（源行为是解挂后删，本插件按「先解挂再删」），实际 %d 条渠道", count)
	}

	// ⑤ 解除挂靠后删除成功，行真的消失。
	supplierTestClearChannels(t, db)
	recorder = doRequest(engine, http.MethodPost, "/supplier/delete", fmt.Sprintf(`{"pks":["%d"]}`, id), nil)
	envelope = decodeAdmin(t, recorder)
	if envelope.Code != 0 {
		t.Fatalf("解除挂靠后删除应成功，实际 %+v", envelope)
	}
	if _, ok := supplierTestRow(t, engine, `{"page":1,"limit":10}`, id); ok {
		t.Fatalf("删除后厂商 %d 不应再出现在 list 里", id)
	}
	if _, err := store.GetSupplier(ctx, id); err == nil {
		t.Fatalf("删除后厂商 %d 行必须消失", id)
	}
}

// TestSupplierBatchDeleteRefusesWholeBatchDB 批量删除的原子性：本批里任一被引用 ⇒ 整批拒、不半删。
func TestSupplierBatchDeleteRefusesWholeBatchDB(t *testing.T) {
	db, store := supplierAITestDB(t)
	engine := supplierTestEngine(store)

	free := supplierTestCreate(t, engine, `{"name":"厂商乙","vendor":"openai"}`)
	busy := supplierTestCreate(t, engine, `{"name":"厂商丙","vendor":"openai"}`)
	supplierTestSeedChannel(t, db, "渠道丙", busy)

	before := supplierTestCountSuppliers(t, db)
	recorder := doRequest(engine, http.MethodPost, "/supplier/delete",
		fmt.Sprintf(`{"pks":["%d","%d"]}`, free, busy), nil)
	envelope := decodeAdmin(t, recorder)
	if envelope.Code != 1 {
		t.Fatalf("含被引用厂商的整批删除必须拒，实际 %+v", envelope)
	}
	if after := supplierTestCountSuppliers(t, db); after != before {
		t.Fatalf("整批拒删后两行都必须仍在（%d → %d）——半删是不可接受的中间态", before, after)
	}
	if _, err := store.GetSupplier(context.Background(), free); err != nil {
		t.Fatalf("同批里未被引用的厂商也必须保留: %v", err)
	}
}

// TestSupplierChannelCountAndWheresDB channel_count 与渠道表读数一致（含按渠道数排序）+ wheres 真库过滤。
func TestSupplierChannelCountAndWheresDB(t *testing.T) {
	db, store := supplierAITestDB(t)
	engine := supplierTestEngine(store)
	ctx := context.Background()

	alpha := supplierTestCreate(t, engine, `{"name":"alpha 厂商","vendor":"openai","status":1}`)
	beta := supplierTestCreate(t, engine, `{"name":"beta 厂商","vendor":"deepseek","status":1}`)
	idle := supplierTestCreate(t, engine, `{"name":"idle 厂商","vendor":"openai","status":0}`)
	supplierTestSeedChannel(t, db, "渠道 alpha-1", alpha)
	supplierTestSeedChannel(t, db, "渠道 alpha-2", alpha)
	supplierTestSeedChannel(t, db, "渠道 beta-1", beta)

	// ① channel_count 与 count(*) where supplier_id=该厂商 一致（逐个厂商对读）。
	counts, err := store.SupplierChannelCounts(ctx, []int64{alpha, beta, idle})
	if err != nil {
		t.Fatalf("批量取渠道数失败: %v", err)
	}
	for _, id := range []int64{alpha, beta, idle} {
		if want := supplierTestCountChannels(t, db, id); counts[id] != want {
			t.Fatalf("厂商 %d 的 channel_count=%d，渠道表读数=%d（必须一致）", id, counts[id], want)
		}
	}
	if counts[alpha] != 2 || counts[beta] != 1 || counts[idle] != 0 {
		t.Fatalf("造数后的渠道数不符: %+v", counts)
	}

	// ② 列表页每行都带 channel_count（接口出口），并对得上表读数。
	for _, tc := range []struct {
		id   int64
		want int64
	}{{alpha, 2}, {beta, 1}, {idle, 0}} {
		row, ok := supplierTestRow(t, engine, `{"page":1,"limit":10}`, tc.id)
		if !ok {
			t.Fatalf("厂商 %d 不在列表里", tc.id)
		}
		if row.ChannelCount != tc.want {
			t.Fatalf("列表行 channel_count=%d，期望 %d", row.ChannelCount, tc.want)
		}
	}

	// ③ 按 channel_count 排序（相关子查询）在真库上可用：降序 α(2) → β(1) → idle(0)。
	recorder := doRequest(engine, http.MethodPost, "/supplier/list", `{"page":1,"limit":10,"sort":"channel_count","order":"desc"}`, nil)
	envelope := decodeAdmin(t, recorder)
	if envelope.Code != 0 {
		t.Fatalf("按 channel_count 排序必须被白名单接受，实际 %+v", envelope)
	}
	var sorted struct {
		List []supplierRow `json:"list"`
	}
	if err := json.Unmarshal(envelope.Data, &sorted); err != nil {
		t.Fatalf("排序回执解析失败: %v", err)
	}
	if len(sorted.List) != 3 || sorted.List[0].ID != alpha || sorted.List[1].ID != beta || sorted.List[2].ID != idle {
		t.Fatalf("按 channel_count 降序排列不符: %+v", sorted.List)
	}

	// ④ wheres 真库过滤：文本模糊 / 二值等值 / IN 三态。
	if _, ok := supplierTestRow(t, engine, `{"page":1,"limit":10,"wheres":[{"wheres":[{"field":"name","operator":"ILIKE","value":"alpha"}]}]}`, alpha); !ok {
		t.Fatalf("wheres(name ILIKE alpha) 必须命中 alpha 厂商")
	}
	betaRows := supplierTestRows(t, engine, `{"page":1,"limit":10,"wheres":[{"wheres":[{"field":"name","operator":"ILIKE","value":"alpha"}]}]}`)
	if len(betaRows) != 1 {
		t.Fatalf("wheres(name ILIKE alpha) 应恰好命中 1 行，实际 %d 行", len(betaRows))
	}
	enabledRows := supplierTestRows(t, engine, `{"page":1,"limit":10,"wheres":[{"wheres":[{"field":"status","operator":"eq","value":1}]}]}`)
	if len(enabledRows) != 2 {
		t.Fatalf("wheres(status eq 1) 应命中 2 行（alpha/beta），实际 %d 行", len(enabledRows))
	}
	inRows := supplierTestRows(t, engine, fmt.Sprintf(
		`{"page":1,"limit":10,"wheres":[{"wheres":[{"field":"id","operator":"IN","value":["%d","%d"]}]}]}`, alpha, idle))
	if len(inRows) != 2 {
		t.Fatalf("wheres(id IN [α,idle]) 应命中 2 行，实际 %d 行", len(inRows))
	}

	// ⑤ 非法 wheres：可读拒绝（code=1）而不是 500 / 静默全表。
	for _, body := range []string{
		`{"wheres":[{"wheres":[{"field":"api_key","operator":"eq","value":"x"}]}]}`,
		`{"wheres":[{"wheres":[{"field":"name","operator":"REGEXP","value":"x"}]}]}`,
	} {
		recorder := doRequest(engine, http.MethodPost, "/supplier/list", body, nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("非法 wheres 必须是 HTTP 200 + code=1（宿主口径），实际 %d", recorder.Code)
		}
		envelope := decodeAdmin(t, recorder)
		if envelope.Code != 1 || envelope.Message == "" {
			t.Fatalf("非法 wheres 必须可读拒绝，实际 %+v（body=%s）", envelope, body)
		}
	}
}

// supplierTestRows 取 list 回执的全部行（过滤断言用）。
func supplierTestRows(t *testing.T, engine *gin.Engine, body string) []supplierRow {
	t.Helper()
	recorder := doRequest(engine, http.MethodPost, "/supplier/list", body, nil)
	envelope := decodeAdmin(t, recorder)
	if envelope.Code != 0 {
		t.Fatalf("list 应成功（body=%s），实际 %+v", body, envelope)
	}
	var data struct {
		List []supplierRow `json:"list"`
	}
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		t.Fatalf("list 回执解析失败: %v", err)
	}
	return data.List
}
