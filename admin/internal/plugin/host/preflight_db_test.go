package host

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"
	"github.com/ai-go-hub/ai-go-admin/internal/infra/database"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// DB 门控集成测试（默认 skip，需 env；无 PLUGIN_DB 时不连库）：
//
//	PLUGIN_DB=1 go test ./internal/plugin/host/ -count=1
//
// 需要插件机制迁移的三张表；本用例组**自带 up**（pluginDBEnsureSchema：等价 `aigo migrate up` 推进到
// 插件迁移版本，只前进不回滚）——P1 门禁只跑 `PLUGIN_DB=1 go test ./internal/plugin/...`，没有单独的
// migrate 步骤，而验收口径本就是「PLUGIN_DB=1 迁移 up/down/up」（down/up 往返在 cli 包）。
// 只读写本测试前缀（plugindbtest）的行，不碰存量数据。
const pluginDBTestPrefix = "plugindbtest"

var (
	pluginDBOnce sync.Once
	pluginDB     *gorm.DB
	pluginDBErr  error
	pluginDBSeq  int64
)

var errPluginDBNoConfig = errors.New("仓库无 config/*.yaml")

// pluginDBSetup 连真实库并清理本测试前缀的残留行
func pluginDBSetup(t *testing.T) *gorm.DB {
	t.Helper()
	if os.Getenv("PLUGIN_DB") != "1" {
		t.Skip("需 PLUGIN_DB=1（连真实库；插件表缺失时本用例组自带 up 把框架迁移推进到插件迁移版本）")
	}

	pluginDBOnce.Do(func() {
		if err := pluginDBChdirRoot(); err != nil {
			pluginDBErr = err
			return
		}
		// 配置从仓库根磁盘读取（./config）
		if err := config.Init(); err != nil {
			pluginDBErr = fmt.Errorf("初始化配置: %w", err)
			return
		}
		if config.Get().Database.Write.Host == "" {
			pluginDBErr = errPluginDBNoConfig
			return
		}
		if err := database.Init(); err != nil {
			pluginDBErr = fmt.Errorf("初始化数据库: %w", err)
			return
		}
		pluginDB = database.DB()
		if err := pluginDBEnsureSchema(pluginDB); err != nil {
			pluginDBErr = err
			return
		}
	})

	if errors.Is(pluginDBErr, errPluginDBNoConfig) {
		t.Skip("仓库无 config/*.yaml，跳过 DB 集成测试")
	}
	if pluginDBErr != nil {
		t.Fatalf("初始化测试数据库: %v", pluginDBErr)
	}

	pluginDBRequireTables(t, pluginDB)
	pluginDBCleanup(pluginDB)
	t.Cleanup(func() { pluginDBCleanup(pluginDB) })
	return pluginDB
}

// pluginDBTableWait 表就位等待上限（同级 cli 包的 down/up 往返窗口内插件表会短暂缺失）
const pluginDBTableWait = time.Second

// pluginDBTablesReady 插件表是否都已就位（用真实 tableExists，绕开 tableExistsFn——
// 本判定属测试基建，不应受「表缺失」用例的注入影响）
func pluginDBTablesReady(db *gorm.DB) bool {
	ctx := context.Background()
	for _, table := range []string{"admin_plugins", "admin_plugin_entities"} {
		exists, err := tableExists(ctx, db, table)
		if err != nil || !exists {
			return false
		}
	}
	return true
}

// pluginDBWaitTables 有界等待表就位（每 50ms 复查一次）
func pluginDBWaitTables(db *gorm.DB, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		if pluginDBTablesReady(db) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// pluginDBEnsureSchema 确保插件表就位：已在 ⇒ 直接用；短暂缺失（同级往返窗口）⇒ 等；
// 迁移未应用 ⇒ 推进到插件迁移版本后再等。装载失败即报错，不静默降级。
func pluginDBEnsureSchema(db *gorm.DB) error {
	unavailable := fmt.Errorf("插件表不可用（%s）：插件机制框架迁移未生效——请检查 `aigo migrate up` 的输出", physicalName("admin_plugins"))
	if pluginDBWaitTables(db, pluginDBTableWait) {
		return nil
	}
	if err := pluginDBSchemaUp(); err != nil {
		return err
	}
	if !pluginDBWaitTables(db, pluginDBTableWait) {
		return unavailable
	}
	return nil
}

// pluginDBRequireTables 每个用例前确认表就位（同级用例的 down/up 往返窗口内会短暂缺表，故有界等待后
// 再判定，避免把「窗口」误读成「迁移未应用」）
func pluginDBRequireTables(t *testing.T, db *gorm.DB) {
	t.Helper()
	if pluginDBWaitTables(db, pluginDBTableWait) {
		return
	}
	for _, table := range []string{"admin_plugins", "admin_plugin_entities"} {
		exists, err := tableExists(context.Background(), db, table)
		if err != nil {
			t.Fatalf("检查数据表 %s 失败: %v", physicalName(table), err)
		}
		if !exists {
			t.Fatalf("数据表 %s 不存在：请先执行 `aigo migrate up`（*_plugin.up.sql）", physicalName(table))
		}
	}
	t.Fatalf("插件表在 %v 内未就位：同级用例的 down/up 往返可能仍在进行，请重跑", pluginDBTableWait)
}

// pluginDBSchemaUp 把框架迁移推进到插件迁移（*_plugin.up.sql）的版本；只前进不回滚：
//   - 游标 >= 目标版本 ⇒ 不动（下游项目把插件迁移编号改到更高号时，绝不回滚其迁移）；
//   - 迁移处于 dirty ⇒ 报错（不猜、不 force，交人工处置）；
//   - 复用 aigo migrate up 的同一份迁移文件、同一前缀替换、同一 golang-migrate 驱动与版本表。
func pluginDBSchemaUp() error {
	const dir = "cmd/migrate/migrations"

	matches, err := filepath.Glob(filepath.Join(dir, "*_plugin.up.sql"))
	if err != nil {
		return fmt.Errorf("定位插件迁移文件: %w", err)
	}
	if len(matches) != 1 {
		return fmt.Errorf("应恰有一个 *_plugin.up.sql（实际 %d 个）: %v", len(matches), matches)
	}
	version, err := strconv.ParseUint(strings.SplitN(filepath.Base(matches[0]), "_", 2)[0], 10, 64)
	if err != nil {
		return fmt.Errorf("迁移文件 %s 的编号不可解析: %w", matches[0], err)
	}

	src, err := iofs.New(os.DirFS(dir), ".")
	if err != nil {
		return fmt.Errorf("加载迁移文件: %w", err)
	}
	src = &pluginPrefixDriver{Driver: src, prefix: config.Get().Database.Prefix}

	m, err := migrate.NewWithSourceInstance("iofs", src, pluginDSN("schema_migrations"))
	if err != nil {
		return fmt.Errorf("连接迁移驱动: %w", err)
	}
	defer func() { _, _ = m.Close() }()

	current, dirty, err := m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		current, err = 0, nil
	}
	if err != nil {
		return fmt.Errorf("读取迁移版本: %w", err)
	}
	if dirty {
		return fmt.Errorf("框架迁移处于 dirty（版本 %d）：请先 `aigo migrate force %d` 修复后再跑库级用例", current, current)
	}
	if current >= uint(version) {
		return nil
	}
	if err := m.Migrate(uint(version)); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("推进框架迁移到 %d 失败: %w", version, err)
	}
	return nil
}

// pluginDSN 框架迁移的数据库 DSN（写库配置 + 显式版本表；与 cmd/migrate.dsn 同形）
func pluginDSN(table string) string {
	cfg := config.Get()
	c := cfg.Database.Write
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(c.User, c.Password),
		Host:   fmt.Sprintf("%s:%d", c.Host, c.Port),
		Path:   c.DBName,
	}
	q := u.Query()
	q.Set("sslmode", c.SSLMode)
	q.Set("x-migrations-table", table)
	if tz := cfg.App.Timezone; tz != "" {
		q.Set("TimeZone", tz)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// pluginPrefixDriver 包装 source.Driver，把迁移 SQL 的 __PREFIX__ 换成表前缀
// （与 cmd/migrate/prefix.go 同形：整文件读出替换后再交驱动执行）
type pluginPrefixDriver struct {
	source.Driver
	prefix string
}

func (d *pluginPrefixDriver) ReadUp(version uint) (io.ReadCloser, string, error) {
	r, id, err := d.Driver.ReadUp(version)
	if err != nil {
		return nil, "", err
	}
	return pluginPrefixReplace(r, d.prefix), id, nil
}

func (d *pluginPrefixDriver) ReadDown(version uint) (io.ReadCloser, string, error) {
	r, id, err := d.Driver.ReadDown(version)
	if err != nil {
		return nil, "", err
	}
	return pluginPrefixReplace(r, d.prefix), id, nil
}

func pluginPrefixReplace(r io.ReadCloser, prefix string) io.ReadCloser {
	data, _ := io.ReadAll(r)
	_ = r.Close()
	return io.NopCloser(strings.NewReader(strings.ReplaceAll(string(data), "__PREFIX__", prefix)))
}

// pluginDBCleanup 清理本测试前缀的台账行与实体行
func pluginDBCleanup(db *gorm.DB) {
	_ = db.Where("plugin LIKE ?", pluginDBTestPrefix+"%").Delete(&Entity{}).Error
	_ = db.Where("name LIKE ?", pluginDBTestPrefix+"%").Delete(&Plugin{}).Error
}

func pluginDBChdirRoot() error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return os.Chdir(dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return errors.New("未找到 go.mod（仓库根）")
		}
		dir = parent
	}
}

// newPluginDBPlugin 造一条测试台账行（名字唯一且落在 varchar(32) 内）
func newPluginDBPlugin(t *testing.T, db *gorm.DB, status string) Plugin {
	t.Helper()
	now := time.Now()
	plugin := Plugin{
		Name:         fmt.Sprintf("%s-%d", pluginDBTestPrefix, atomic.AddInt64(&pluginDBSeq, 1)),
		Title:        "插件预检用例",
		Version:      "1.0.0",
		Kind:         "mixed",
		Status:       status,
		HostAPI:      ">=1 <2",
		HostVersion:  "0.0.0-test",
		FrameworkReq: ">=0.1 <1",
		InstalledAt:  &now,
		UpdatedAt:    &now,
	}
	if err := db.Create(&plugin).Error; err != nil {
		t.Fatalf("创建测试插件台账行失败: %v", err)
	}
	return plugin
}

// newPluginDBEntity 造一条测试实体元数据行
func newPluginDBEntity(t *testing.T, db *gorm.DB, plugin, name, spec string) Entity {
	t.Helper()
	entity := Entity{
		Plugin:        plugin,
		Name:          name,
		TitleZh:       "条目",
		TitleEn:       "Items",
		PhysicalTable: "plugin_" + plugin + "_" + name,
		Spec:          datatypes.JSON([]byte(spec)),
	}
	if err := db.Create(&entity).Error; err != nil {
		t.Fatalf("创建测试实体元数据行失败: %v", err)
	}
	return entity
}

// pluginDBValidSpec 一份合法 spec（结构校验通过）
func pluginDBValidSpec(entity string) string {
	return `{"entity":"` + entity + `","title_zh":"条目","title_en":"Items",` +
		`"fields":[{"name":"title","type":"string"},{"name":"status","type":"int"}]}`
}

func TestPreflightDBLoads(t *testing.T) {
	db := pluginDBSetup(t)
	ctx := context.Background()

	// failed（半装）插件：留痕但不注册路由；其中实体即使 spec 非法也不阻断启动
	failedPlugin := newPluginDBPlugin(t, db, StatusFailed)
	newPluginDBEntity(t, db, failedPlugin.Name, "items", pluginDBValidSpec("items"))
	newPluginDBEntity(t, db, failedPlugin.Name, "broken", `{"entity":"broken","fields":[{"name":"Bad","type":"string"}]}`)

	// installed 插件：实体进快照（等待 enable 后由闸放行，路由在启动时注册）
	installedPlugin := newPluginDBPlugin(t, db, StatusInstalled)
	installedEntity := newPluginDBEntity(t, db, installedPlugin.Name, "items", pluginDBValidSpec("items"))

	if err := Preflight(ctx); err != nil {
		t.Fatalf("预检应通过: %v", err)
	}

	snap, ok := Loaded()
	if !ok {
		t.Fatal("预检后应装载快照")
	}
	if status, found := snap.Status(failedPlugin.Name); !found || status != StatusFailed {
		t.Fatalf("台账应含 failed 插件 %s: %q, %v", failedPlugin.Name, status, found)
	}
	if entities := snap.EntitiesOf(failedPlugin.Name); len(entities) != 0 {
		t.Errorf("failed 插件的实体不应进快照: %+v", entities)
	}
	entities := snap.EntitiesOf(installedPlugin.Name)
	if len(entities) != 1 || entities[0].Name != installedEntity.Name {
		t.Fatalf("installed 插件的实体应进快照: %+v", entities)
	}
	if entities[0].PhysicalTable != installedEntity.PhysicalTable {
		t.Errorf("物理表名应原样装载: %q, want %q", entities[0].PhysicalTable, installedEntity.PhysicalTable)
	}
}

func TestPreflightDBRejectsBadSpec(t *testing.T) {
	db := pluginDBSetup(t)

	plugin := newPluginDBPlugin(t, db, StatusInstalled)
	entity := newPluginDBEntity(t, db, plugin.Name, "items", `{"entity":"items","fields":[{"name":"Bad-Name","type":"string"}]}`)

	err := Preflight(context.Background())
	if err == nil {
		t.Fatal("非法 spec 应让预检失败")
	}
	if !strings.Contains(err.Error(), plugin.Name) || !strings.Contains(err.Error(), entity.Name) {
		t.Errorf("错误应点名插件与实体: %v", err)
	}
}

func TestGateDB(t *testing.T) {
	db := pluginDBSetup(t)
	ctx := context.Background()

	plugin := newPluginDBPlugin(t, db, StatusEnabled)

	SetGateTTL(0) // 恢复默认上限并清空缓存
	defer SetGateTTL(0)

	if !gate.enabled(ctx, plugin.Name) {
		t.Fatal("enabled 插件应放行")
	}

	if err := db.Model(&Plugin{}).Where("name = ?", plugin.Name).Update("status", StatusDisabled).Error; err != nil {
		t.Fatalf("改库失败: %v", err)
	}
	if !gate.enabled(ctx, plugin.Name) {
		t.Fatal("TTL 内应仍用缓存状态（CLI 启停 ≤TTL 生效的语义）")
	}

	SetGateTTL(0) // 清空缓存后重新读库
	if gate.enabled(ctx, plugin.Name) {
		t.Fatal("清缓存后应读到 disabled 并拒绝")
	}

	if gate.enabled(ctx, pluginDBTestPrefix+"-absent") {
		t.Fatal("台账无此插件（未安装）应拒绝")
	}
}

func TestTableExistsDB(t *testing.T) {
	db := pluginDBSetup(t)
	ctx := context.Background()

	exists, err := tableExists(ctx, db, "admin_plugins")
	if err != nil || !exists {
		t.Fatalf("admin_plugins 应存在: %v, %v", exists, err)
	}

	// 表缺失判定（预检失败态①的判据）：不存在的表必须报告不存在且无错误
	exists, err = tableExists(ctx, db, "definitely_absent_table_for_preflight_test")
	if err != nil {
		t.Fatalf("查询不存在的表不应报错: %v", err)
	}
	if exists {
		t.Error("不存在的表不应报告存在")
	}
}
