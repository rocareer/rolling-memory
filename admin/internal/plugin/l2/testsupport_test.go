package l2

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"

	"github.com/gin-gonic/gin"
	"gorm.io/datatypes"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

// 本文件是 l2 包的测试基建（不连库）：DryRun 实例、元数据行与 spec 构造、gin 测试上下文。
// 库级用例（PLUGIN_DB=1）在 db_test.go，自带建表与清理，不依赖插件元数据表。

// captureLogger 捕获 GORM 渲染出的 SQL（DryRun 会话用；其余日志方法丢弃）
type captureLogger struct {
	mu   sync.Mutex
	sqls []string
}

func (l *captureLogger) LogMode(logger.LogLevel) logger.Interface { return l }

func (l *captureLogger) Info(context.Context, string, ...interface{}) {}

func (l *captureLogger) Warn(context.Context, string, ...interface{}) {}

func (l *captureLogger) Error(context.Context, string, ...interface{}) {}

func (l *captureLogger) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sqls = append(l.sqls, sql)
}

// ParamsFilter 让 gorm 不要把参数内联进 SQL 文本（默认走 Dialector.Explain，会把 $1 替换成字面量）：
// 本包的形状断言要看的是「值走占位符」的原始形态，故实现该接口原样返回 SQL（callbacks.go:139 ✓）。
func (l *captureLogger) ParamsFilter(_ context.Context, sql string, _ ...interface{}) (string, []interface{}) {
	return sql, nil
}

func (l *captureLogger) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.sqls...)
}

// dryRunDB 构造不连库的 DryRun 实例（仓库既有口径：sql.Open 惰性 + DisableAutomaticPing 免握手 +
// SkipDefaultTransaction 免 Begin，全程零网络，见 internal/repository/base_test.go 的同名助手）。
// tablePrefix 注入 GORM 的 NamingStrategy（探针用：证明 db.Table() 不吃表前缀）。
func dryRunDB(t *testing.T, tablePrefix string) (*gorm.DB, *captureLogger) {
	t.Helper()
	capture := &captureLogger{}
	db, err := gorm.Open(
		postgres.New(postgres.Config{DSN: "host=127.0.0.1 port=1 user=none password=none dbname=none sslmode=disable"}),
		&gorm.Config{
			DryRun:                 true,
			DisableAutomaticPing:   true,
			SkipDefaultTransaction: true,
			NamingStrategy:         schema.NamingStrategy{TablePrefix: tablePrefix},
			Logger:                 capture,
		},
	)
	if err != nil {
		t.Fatalf("构造 DryRun 实例失败（不应连库）: %v", err)
	}
	return db, capture
}

// expectTable 期望的物理表名（含本地配置前缀的正确形态；用例不硬编码前缀——配置可能来自 .env.yaml）
func expectTable(plugin, entity string) string {
	return PhysicalEntityTable(prefixOf(), plugin, entity)
}

// quoted 期望 SQL 文本里的引用形态
func quoted(name string) string { return `"` + name + `"` }

// dryRunEngine 不连库的引擎（SQL 文本断言用：DryRun 只渲染不执行）
func dryRunEngine(t *testing.T, tablePrefix string) (*Engine, *captureLogger) {
	t.Helper()
	db, capture := dryRunDB(t, tablePrefix)
	return New(db), capture
}

// entityRow 造一条实体元数据行（spec 由 manifest.Entity 序列化——与安装器写入的形状逐字一致）
func entityRow(t *testing.T, plugin, entity string, spec manifest.Entity) host.Entity {
	t.Helper()
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("序列化 spec 失败: %v", err)
	}
	return host.Entity{
		Plugin:        plugin,
		Name:          entity,
		TitleZh:       spec.TitleZh,
		TitleEn:       spec.TitleEn,
		PhysicalTable: PhysicalEntityTable(prefixOf(), plugin, entity),
		Spec:          datatypes.JSON(raw),
	}
}

// mustParse 走真实路径（Parse）造运行期 Spec：负例之外的一切用例都用它，保证与生产同一条校验链
func mustParse(t *testing.T, plugin, entity string, spec manifest.Entity) *Spec {
	t.Helper()
	parsed, err := Parse(entityRow(t, plugin, entity, spec))
	if err != nil {
		t.Fatalf("Parse(%s/%s) 意外失败: %v", plugin, entity, err)
	}
	return parsed
}

// textField 文本字段的便捷构造（测试用样板；mutate 可选，用于改类型/长度/唯一等）
func textField(name, title string, mutate ...func(*manifest.Field)) manifest.Field {
	field := manifest.Field{
		Name:        name,
		Type:        "string",
		TitleZh:     title,
		TitleEn:     name,
		Length:      128,
		List:        true,
		Search:      &manifest.Search{Op: "ILIKE"},
		QuickSearch: true,
		Form:        "input",
	}
	if len(mutate) > 0 && mutate[0] != nil {
		mutate[0](&field)
	}
	return field
}

// testContext 构造 gin 测试上下文（可带请求体；params 为路径参数）
func testContext(method, target, body string, params gin.Params) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, target, reader)
	request.Header.Set("Content-Type", "application/json")
	c.Request = request
	c.Params = params
	return c, recorder
}

// responseBody 解析统一响应体 {code,message,time,data}
type responseBody struct {
	Code    int            `json:"code"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data"`
}

func decodeResponse(t *testing.T, recorder *httptest.ResponseRecorder) responseBody {
	t.Helper()
	var payload responseBody
	if body := strings.TrimSpace(recorder.Body.String()); body != "" {
		if err := json.Unmarshal([]byte(body), &payload); err != nil {
			t.Fatalf("响应不是统一响应体: %v（原文 %s）", err, body)
		}
	}
	return payload
}

// entityFor 构造单实体执行器（注入 resolver：不连库也能打到动作逻辑；生产路径见 router.go）
func entityFor(engine *Engine, spec *Spec) *entityHandler {
	return &entityHandler{
		engine:   engine,
		plugin:   spec.Plugin,
		entity:   spec.Name,
		resolver: func(context.Context) (*Spec, error) { return spec, nil },
	}
}

// dryRunQuery 渲染一次 DryRun 查询并返回 SQL 文本与参数（只渲染不执行）。
// 注意：Statement 属于链上最后那个 *gorm.DB（Session(clone=1) 之后的首个链式调用会换新 Statement），
// 故必须从 Find 的返回值读，不能从 session 变量读。DryRun 下 gorm 不重置 SQL/Vars
// （callbacks.go:149 的 `if !stmt.DB.DryRun { stmt.SQL.Reset() ... }`），因此读得到。
func dryRunQuery(t *testing.T, db *gorm.DB, build func(*gorm.DB) *gorm.DB) (string, []any) {
	t.Helper()
	session := db.Session(&gorm.Session{DryRun: true})
	rows := make([]map[string]any, 0, 1)
	tx := build(session).Find(&rows)
	if tx.Error != nil {
		t.Fatalf("渲染 SQL 失败: %v", tx.Error)
	}
	return tx.Statement.SQL.String(), tx.Statement.Vars
}
