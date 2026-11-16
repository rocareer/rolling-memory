package host

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"
	"github.com/ai-go-hub/ai-go-admin/internal/infra/database"

	"gorm.io/gorm"
)

// Snapshot 预检装载的元数据快照（供路由注册与诊断复用；装载后只读）。
type Snapshot struct {
	// Plugins 全部台账行（含 status=failed：留痕不注册路由），按 name 升序
	Plugins []Plugin
	// Entities 可注册路由的实体行（其插件在台账内且状态非 failed），按 plugin、name 升序
	Entities []Entity
}

// Status 取指定插件的台账状态；台账无此行返回 ("", false)
func (s *Snapshot) Status(plugin string) (string, bool) {
	for i := range s.Plugins {
		if s.Plugins[i].Name == plugin {
			return s.Plugins[i].Status, true
		}
	}
	return "", false
}

// EntitiesOf 取指定插件的可注册实体行（按 name 升序）。
// 线性扫描：实体行与插件数量同级（个位到十位量级），无需建索引。
func (s *Snapshot) EntitiesOf(plugin string) []Entity {
	var out []Entity
	for i := range s.Entities {
		if s.Entities[i].Plugin == plugin {
			out = append(out, s.Entities[i])
		}
	}
	return out
}

var (
	snapshotMu sync.RWMutex
	snapshot   *Snapshot
	// loadMu 装载互斥：并发首载只跑一次（Preflight 自身只在末尾拿 snapshotMu 替换快照，
	// 两把锁分工不同，不会自锁）
	loadMu sync.Mutex
)

// Loaded 返回当前已装载的元数据快照（只读：请勿修改返回结构中的切片元素）
func Loaded() (*Snapshot, bool) {
	snapshotMu.RLock()
	defer snapshotMu.RUnlock()
	return snapshot, snapshot != nil
}

// EnsureLoaded 幂等装载：已装载直接返回；未装载则执行一次 Preflight（并发首载由 loadMu 串行化）。
//
// 供路由装配期自举：宿主若没在启动链路显式调用 Preflight（例如未接入 `cmd/serve` 里那一行），
// L2 实体路由也不会因「快照未装载」静默缺席——装载责任在机制自身，不在宿主接线的完整性。
func EnsureLoaded(ctx context.Context) error {
	if _, ok := Loaded(); ok {
		return nil
	}
	loadMu.Lock()
	defer loadMu.Unlock()
	if _, ok := Loaded(); ok {
		return nil
	}
	return Preflight(ctx)
}

// MustLoad 路由装配入口的装载门（幂等，装配期调用）。
//
// 两个分支的判据：
//   - 数据库未初始化（只装配路由的测试 / 嵌入场景）⇒ 记日志后返回，保持「L2 实体路由不注册」的
//     既有行为：未接插件机制的场景不该被拖成启动失败；
//   - 数据库已初始化而装载失败 ⇒ **panic**。panic 发生在 router.Setup 这条启动链路上，等价于启动
//     中止；依据同 Preflight 的否决项——router.Setup 没有错误返回通道，「静默零路由」会被运维
//     误判成「插件在跑」。
func MustLoad(ctx context.Context) {
	if database.DB() == nil {
		log.Printf("[plugin] 数据库未初始化：跳过插件元数据装载（L2 实体路由不注册；先 database.Init() 再装配路由）")
		return
	}
	if err := EnsureLoaded(ctx); err != nil {
		panic(fmt.Sprintf("[plugin] 插件元数据装载失败，启动中止: %v", err))
	}
}

// Preflight 启动预检与元数据装载：在 database.Init() 之后、router.Setup() 之前调用一次。
//   - 台账/实体表缺失（插件机制框架迁移未应用）⇒ 返回错误并提示 `aigo migrate up`（启动失败）；
//   - admin_rules.plugin 归属列缺失 ⇒ 返回错误并给出同一提示：归属是「按插件整片收口」（启停/卸载）
//     的唯一依据，缺列时写入会失败、清理会空转，必须前置拦下；
//   - 实体 spec 无法装载或非法 ⇒ 返回错误并点名插件与实体（启动失败）；
//   - status=failed 的插件留痕但不注册路由（记日志，其实体行不进快照）；
//   - 成功后把台账与实体元数据装入内存快照，变更由下次启动或 CLI 侧体现（路由在启动时注册）。
//
// 不用静默降级：router.Setup 没有错误返回通道，装载失败必须变成启动失败，否则运维会把
// 「零路由」误判成「插件在跑」（否决项）。
func Preflight(ctx context.Context) error {
	db := database.DB()
	if db == nil {
		return errors.New("插件预检: 数据库未初始化（须在 database.Init() 之后调用）")
	}

	// ① 表缺失（迁移未跑）⇒ 可读错误 + 可执行提示
	for _, table := range []string{"admin_plugins", "admin_plugin_entities"} {
		exists, err := tableExistsFn(ctx, db, table)
		if err != nil {
			return fmt.Errorf("插件预检: 检查数据表 %s 失败: %w", physicalName(table), err)
		}
		if !exists {
			return fmt.Errorf("插件预检: 数据表 %s 不存在（插件机制框架迁移未应用）；请先执行 `aigo migrate up`", physicalName(table))
		}
	}

	// ①b 归属列缺失（迁移未跑完 / 目标库结构被裁剪）⇒ 同一口径的可读错误
	owned, err := columnExistsFn(ctx, db, "admin_rules", "plugin")
	if err != nil {
		return fmt.Errorf("插件预检: 检查归属列 %s 失败: %w", physicalName("admin_rules")+".plugin", err)
	}
	if !owned {
		return fmt.Errorf("插件预检: 归属列 %s 不存在（插件机制框架迁移未应用）；请先执行 `aigo migrate up`", physicalName("admin_rules")+".plugin")
	}

	// ② 台账 + 实体元数据（表小且无大字段，整行读；与既有 permission.GetRules 同口径）
	plugins, err := gorm.G[Plugin](db).Order("name").Find(ctx)
	if err != nil {
		return fmt.Errorf("插件预检: 读取 admin_plugins 失败: %w", err)
	}

	var entities []Entity
	if len(plugins) > 0 {
		names := make([]string, len(plugins))
		for i := range plugins {
			names[i] = plugins[i].Name
		}
		entities, err = gorm.G[Entity](db).Where("plugin IN ?", names).Order("plugin, name").Find(ctx)
		if err != nil {
			return fmt.Errorf("插件预检: 读取 admin_plugin_entities 失败: %w", err)
		}
	}

	// ③ 装载：failed 留痕不注册；spec 非法即失败并点名实体；归属漂移（台账无此插件）不进快照
	statuses := make(map[string]string, len(plugins))
	for i := range plugins {
		statuses[plugins[i].Name] = plugins[i].Status
	}

	// 默认依赖插件缺失只提示、不自动安装：安装是源码树操作（生产门禁/审计/落位一条不少），
	// 显式入口是 `aigo plugin defaults`；启动阶段替运维做决定会绕开这条纪律。
	for _, name := range DefaultPlugins {
		if _, ok := statuses[name]; !ok {
			log.Printf("[plugin] 默认依赖插件 %s 未安装：部署顺序为 `aigo migrate up` → `aigo plugin defaults` → 启动服务（本提示不改变启动结果）", name)
		}
	}

	next := &Snapshot{Plugins: plugins}
	for i := range entities {
		entity := entities[i]
		status, ok := statuses[entity.Plugin]
		if !ok {
			log.Printf("[plugin] 实体 %s 的归属插件 %s 不在台账内（归属漂移）：不注册路由，请核对 admin_rules.plugin 与 admin_plugin_entities", entity.Name, entity.Plugin)
			continue
		}
		if status == StatusFailed {
			continue
		}
		if err := ValidateSpec(entity); err != nil {
			return fmt.Errorf("插件预检: 插件 %s 的实体 %s 的 spec 非法: %w", entity.Plugin, entity.Name, err)
		}
		next.Entities = append(next.Entities, entity)
	}

	if len(plugins) > 0 {
		for _, plugin := range plugins {
			if plugin.Status == StatusFailed {
				log.Printf("[plugin] 插件 %s 处于 failed（半装）状态：不注册路由；清理入口 `aigo plugin uninstall --force %s`", plugin.Name, plugin.Name)
			}
		}
		log.Printf("[plugin] 插件元数据已装载：%d 个插件 / %d 个可注册实体", len(plugins), len(next.Entities))
	}

	snapshotMu.Lock()
	snapshot = next
	snapshotMu.Unlock()
	return nil
}

// tableExistsFn 表存在性判定（生产路径恒为 tableExists；DB 门禁用例注入它以覆盖
// 「表缺失 ⇒ 启动失败」分支，避免为测该分支真去改库结构）
var tableExistsFn = tableExists

// columnExistsFn 列存在性判定（同 tableExistsFn 的注入口径，覆盖「归属列缺失 ⇒ 启动失败」分支）
var columnExistsFn = columnExists

// physicalName 物理表名（配置前缀 + 逻辑名；与 crud.WithPrefix 同口径，默认前缀为空）
func physicalName(table string) string {
	return config.Get().Database.Prefix + table
}

func tableExists(ctx context.Context, db *gorm.DB, table string) (bool, error) {
	if db.Dialector != nil && db.Dialector.Name() == "sqlite" {
		return db.WithContext(ctx).Migrator().HasTable(physicalName(table)), nil
	}
	var count int64
	if err := db.WithContext(ctx).Raw(`
SELECT COUNT(*) AS count
FROM pg_catalog.pg_class t
JOIN pg_catalog.pg_namespace n ON n.oid = t.relnamespace
WHERE n.nspname = current_schema()
    AND t.relkind = 'r'
    AND t.relname = ?`, physicalName(table)).Scan(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func columnExists(ctx context.Context, db *gorm.DB, table, column string) (bool, error) {
	if db.Dialector != nil && db.Dialector.Name() == "sqlite" {
		return db.WithContext(ctx).Migrator().HasColumn(physicalName(table), column), nil
	}
	var count int64
	if err := db.WithContext(ctx).Raw(`
SELECT COUNT(*) AS count
FROM pg_catalog.pg_attribute a
JOIN pg_catalog.pg_class t ON t.oid = a.attrelid
JOIN pg_catalog.pg_namespace n ON n.oid = t.relnamespace
WHERE n.nspname = current_schema()
    AND t.relname = ?
    AND a.attname = ?
    AND a.attnum > 0
    AND NOT a.attisdropped`, physicalName(table), column).Scan(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}
