package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"

	"github.com/golang-migrate/migrate/v4"
	"gorm.io/gorm"
)

// identifierSafe 物理标识符白名单（版本表名由「前缀 + 插件名 + 固定段」拼成；插件名已过 manifest
// 的 ^[a-z][a-z0-9-]{1,31}$ 校验，这里再复核一遍，任何拼进 SQL 的标识符都过同一处）
var identifierSafe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// MigrationOutcome 一次插件迁移应用的读数（补偿回滚与审计的依据）
type MigrationOutcome struct {
	// Table 版本表物理名
	Table string
	// Before 应用前版本（无版本 ⇒ 0）
	Before int
	// TableExisted 应用前版本表是否已存在（补偿时据此决定是否 drop）
	TableExisted bool
	// Applied 本次新应用的版本（升序；**基线路径恒为空**——未执行任何旧 DDL）
	Applied []int
	// After 应用后版本
	After int
	// Dirty 版本表当前是否处于 dirty
	Dirty bool
	// Baselined 本次走「存量库迁移基线（adopt）」：版本表推进到 AdoptedThrough，未执行任何旧 DDL。
	// 补偿据此**不做 down 回滚**——down 文件与核心链迁移是同物，回滚删的是核心链建的表与数据。
	Baselined bool
	// AdoptedThrough 接管边界（= manifest.adopt.through；**仅 Baselined 时写值**——台账回写与 --purge 门禁读它）
	AdoptedThrough int
	// DeclaredThrough manifest 声明的接管边界（声明了 adopt 就有值；full 分支也如实记录，供审计回溯）
	DeclaredThrough int
	// BaselineMode 判定模式 none | adopt | full（rejected 在执行侧是硬错误，不成功返回）
	BaselineMode string
	// ProbesTotal / ProbesHit / Missing 探针读数（审计与日志用；未声明 adopt 时全零）
	ProbesTotal int
	ProbesHit   int
	Missing     []string
}

// ApplyPluginMigrations 按编号应用插件迁移；adopt 未声明时与既往完全同路径
// （干净库 / 下游产品：全量或增量应用）。
//
// 传 dir 为插件迁移目录（安装编排传**已落位**的 plugins/<名>/migrations）。
func ApplyPluginMigrations(ctx context.Context, env *Env, name, dir string) (*MigrationOutcome, error) {
	return ApplyPluginMigrationsWithAdopt(ctx, env, name, dir, nil)
}

// ApplyPluginMigrationsWithAdopt 同上，并处理「存量库迁移基线（adopt）」
// （设计说明）：
//   - 全命中 ⇒ 只把版本表推进到 through（写 1 行；不执行任何旧 DDL，Applied 留空）；
//   - 全不命中 ⇒ 全量（干净库 / 下游库形态）；
//   - 部分命中 / 现场与版本行不一致 / dirty ⇒ **拒装**（硬错误；预演侧已在任何副作用之前先拒一次，
//     这里按「预演读数不作执行依据」再算一次，执行侧读数才是依据）。
//
// 判定在**构造 migrator 之前**做，且只读（版本表存在性 + 版本行 + pg_catalog 探针）：驱动构造 migrator
// 就会建版本表，判定若在其后，一张「本次新建的空版本表」会被误当作既有的链（见 baseline.go 头注）。
func ApplyPluginMigrationsWithAdopt(ctx context.Context, env *Env, name, dir string, adopt *manifest.Adopt) (*MigrationOutcome, error) {
	db, err := env.EnsureDB(ctx)
	if err != nil {
		return nil, err
	}
	outcome := &MigrationOutcome{Table: MigrationTableName(name, env.Prefix), BaselineMode: baselineModeNone}
	outcome.TableExisted, err = migrationTableExists(ctx, db, outcome.Table)
	if err != nil {
		return nil, err
	}

	// 迁移基线（adopt）判定：只读；未声明 adopt 时零额外库访问
	baseline, err := readBaseline(ctx, db, env.Prefix, name, adopt)
	if err != nil {
		return nil, err
	}
	outcome.BaselineMode = baseline.Mode
	outcome.ProbesTotal, outcome.ProbesHit, outcome.Missing = baseline.ProbesTotal, baseline.ProbesHit, baseline.Missing
	if adopt != nil {
		outcome.DeclaredThrough = adopt.Through // 声明值如实入账（full 分支也留痕，供审计回溯）
	}
	if baseline.Mode == baselineModeRejected {
		return outcome, baselineRejectError(name, baseline)
	}

	migrator, err := NewPluginMigrator(name, dir, env.Prefix)
	if err != nil {
		return nil, err
	}
	defer migrator.Close()

	before, dirtyBefore, err := migrator.Version()
	if err != nil {
		return nil, fmt.Errorf("读取插件 %s 的迁移状态失败: %w", name, err)
	}
	outcome.Before = int(before)
	if dirtyBefore {
		outcome.Dirty = true
		return nil, fmt.Errorf("插件 %s 的版本表 %s 处于 dirty（版本 %d）：先 `aigo plugin migrate-force %s %d` 处置，"+
			"再重试（dirty 不做静默重试）", name, outcome.Table, before, name, before)
	}

	// 基线路径：只写版本表（0 条迁移 SQL）；Applied 保持空 ⇒ 补偿不会做 down 回滚
	if baseline.Mode == baselineModeAdopt {
		outcome.Baselined = true
		outcome.AdoptedThrough = adopt.Through
		if int(before) != adopt.Through {
			if err := migrator.Force(adopt.Through); err != nil {
				return outcome, fmt.Errorf("写插件 %s 的迁移基线（版本表 %s → %d）失败: %w", name, outcome.Table, adopt.Through, err)
			}
		}
		outcome.After = adopt.Through
		env.Printf("插件迁移：存量库迁移基线（adopt）——探针 %d/%d 全命中，版本表 %s 记为 1..%d「与核心链同物」，"+
			"未执行任何旧 DDL（迁移 SQL 0 条 / 版本表写 %d 行）\n",
			outcome.ProbesHit, outcome.ProbesTotal, outcome.Table, adopt.Through, boolToInt(int(before) != adopt.Through))
		return outcome, nil
	}

	// 已应用的版本清单（源侧扫描：版本 > before 的 up 文件；Up() 失败时据此报告停在何处）
	migrations, err := ScanMigrations(dir)
	if err != nil {
		return nil, err
	}
	pending := make([]int, 0, len(migrations))
	for _, migration := range migrations {
		if migration.Kind == "up" && migration.Version > outcome.Before {
			pending = append(pending, migration.Version)
		}
	}

	if err := migrator.Up(); err != nil {
		outcome.Dirty = false
		if version, dirty, verr := migrator.Version(); verr == nil {
			outcome.After, outcome.Dirty = int(version), dirty
		}
		if outcome.Dirty {
			return outcome, fmt.Errorf("插件 %s 的迁移失败且版本表被标记 dirty（版本 %d，表 %s）——失败文件是整文件隐式事务，"+
				"PG 已回滚其语句；修复入口 `aigo plugin migrate-force %s %d`；不静默 force、不吞错: %w",
				name, outcome.After, outcome.Table, name, outcome.After, err)
		}
		return outcome, fmt.Errorf("插件 %s 的迁移失败（版本 %d）: %w", name, outcome.After, err)
	}

	outcome.Applied = pending
	if version, _, verr := migrator.Version(); verr == nil {
		outcome.After = int(version)
	}
	if len(pending) == 0 {
		env.Printf("插件迁移：无待应用版本（版本表 %s 已在 %d；复装幂等）\n", outcome.Table, outcome.After)
	} else {
		env.Printf("插件迁移：已应用 %s（版本表 %s：%d → %d）\n", formatVersions(pending), outcome.Table, outcome.Before, outcome.After)
	}
	return outcome, nil
}

// RollbackPluginMigrations 回滚本次安装新应用的版本（补偿回滚用）。dirty 态**拒绝**：
// 不静默 force，由人显式处置后再清理。
//
// 基线（adopt）路径单独处理：本次**没有执行任何旧 DDL**，故绝不按 down 回滚（down 与核心链同物，
// 回滚会删核心链建的表与数据）——只处置版本表：本次新建 ⇒ drop；既有 ⇒ 归位到安装前版本（无行 = -1）。
func RollbackPluginMigrations(ctx context.Context, env *Env, name, dir string, outcome *MigrationOutcome) error {
	if outcome == nil {
		return nil
	}
	if outcome.Baselined {
		return rollbackBaseline(ctx, env, name, dir, outcome)
	}
	if len(outcome.Applied) == 0 {
		return nil
	}
	migrator, err := NewPluginMigrator(name, dir, env.Prefix)
	if err != nil {
		return err
	}
	defer migrator.Close()

	version, dirty, err := migrator.Version()
	if err != nil {
		return fmt.Errorf("读取插件 %s 的迁移状态失败: %w", name, err)
	}
	if dirty {
		return fmt.Errorf("插件 %s 的版本表 %s 处于 dirty（版本 %d）：不静默 force——"+
			"先 `aigo plugin migrate-force %s <版本>` 归位，再由 `aigo plugin uninstall --force %s` 清理",
			name, outcome.Table, version, name, name)
	}
	if int(version) != outcome.After {
		return fmt.Errorf("插件 %s 的版本表停在 %d（预期 %d）：现场已被人为改动，不猜、不回滚", name, version, outcome.After)
	}
	if err := migrator.Migrate.Steps(-len(outcome.Applied)); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("回滚插件 %s 的迁移（%s）失败: %w", name, formatVersions(outcome.Applied), err)
	}
	env.Printf("补偿：已回滚插件迁移 %s（版本表 %s 归位至 %d）\n", formatVersions(outcome.Applied), outcome.Table, outcome.Before)

	// 版本表若是本次安装新建的，回滚后一并 drop（不给半装现场留张空表）
	if !outcome.TableExisted {
		db, dbErr := env.EnsureDB(ctx)
		if dbErr != nil {
			return dbErr
		}
		if err := DropMigrationTable(ctx, db, outcome.Table); err != nil {
			return err
		}
		env.Printf("补偿：版本表 %s 系本次安装新建，已 drop\n", outcome.Table)
	}
	return nil
}

// rollbackBaseline 基线（adopt）安装的补偿：零 down 回滚，只把版本表恢复到安装前形态。
//   - 版本表系本次新建 ⇒ drop（半装现场不留空表；复装时探针会再判一次，判定可复现）；
//   - 版本表先前就在 ⇒ Force 归位到安装前版本（无行 = Force(-1)，与「表在、无行」逐字同态）；
//     归位前照 dirty / 被改动两条口径拦一道（不静默 force、不猜）。
func rollbackBaseline(ctx context.Context, env *Env, name, dir string, outcome *MigrationOutcome) error {
	if !outcome.TableExisted {
		db, err := env.EnsureDB(ctx)
		if err != nil {
			return err
		}
		if err := DropMigrationTable(ctx, db, outcome.Table); err != nil {
			return err
		}
		env.Printf("补偿：版本表 %s 系本次安装新建（迁移基线：只写了版本行 %d），已 drop；未执行任何 down 回滚\n",
			outcome.Table, outcome.AdoptedThrough)
		return nil
	}
	if outcome.Before == outcome.After {
		env.Printf("补偿：迁移基线（adopt）未改动版本表 %s（安装前后同为 %d）；未执行任何 down 回滚\n", outcome.Table, outcome.After)
		return nil
	}

	migrator, err := NewPluginMigrator(name, dir, env.Prefix)
	if err != nil {
		return err
	}
	defer migrator.Close()

	version, dirty, err := migrator.Version()
	if err != nil {
		return fmt.Errorf("读取插件 %s 的迁移状态失败: %w", name, err)
	}
	if dirty {
		return fmt.Errorf("插件 %s 的版本表 %s 处于 dirty（版本 %d）：不静默 force——先 `aigo plugin migrate-force %s <版本>` 归位，"+
			"再由 `aigo plugin uninstall --force %s` 清理", name, outcome.Table, version, name, name)
	}
	if int(version) != outcome.After {
		return fmt.Errorf("插件 %s 的版本表停在 %d（预期 %d）：现场已被人为改动，不猜、不回滚", name, version, outcome.After)
	}
	if err := migrator.Force(restoreVersion(outcome.Before)); err != nil {
		return fmt.Errorf("把插件 %s 的版本表 %s 归位到安装前版本 %d 失败: %w", name, outcome.Table, outcome.Before, err)
	}
	env.Printf("补偿：迁移基线（adopt）把版本表 %s 归位至安装前（%s）；未执行任何 down 回滚\n",
		outcome.Table, describeVersion(outcome.Before))
	return nil
}

// restoreVersion 安装前版本 → Force 的目标值（golang-migrate 的 Force(-1) = NilVersion ⇒ 只 TRUNCATE 不插行）
func restoreVersion(before int) int {
	if before <= 0 {
		return -1
	}
	return before
}

// describeVersion 版本展示（0 = 无版本行）
func describeVersion(version int) string {
	if version <= 0 {
		return "无版本行"
	}
	return fmt.Sprintf("版本 %d", version)
}

// boolToInt 计数展示（true ⇒ 1）
func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// PurgePluginMigrations 卸载 --purge 的 a) 步骤：按 down 逆序回滚插件迁移 → drop 版本表。
// 返回回滚的版本数（无版本表 ⇒ 0）。
func PurgePluginMigrations(ctx context.Context, env *Env, name, dir string) (int, error) {
	db, err := env.EnsureDB(ctx)
	if err != nil {
		return 0, err
	}
	table := MigrationTableName(name, env.Prefix)
	exists, err := migrationTableExists(ctx, db, table)
	if err != nil {
		return 0, err
	}
	if !exists {
		env.Printf("插件迁移：版本表 %s 不存在（无迁移记录），跳过回滚\n", table)
		return 0, nil
	}

	migrator, err := NewPluginMigrator(name, dir, env.Prefix)
	if err != nil {
		return 0, err
	}
	defer migrator.Close()

	version, dirty, err := migrator.Version()
	if err != nil {
		return 0, fmt.Errorf("读取插件 %s 的迁移状态失败: %w", name, err)
	}
	if dirty {
		return 0, fmt.Errorf("插件 %s 的版本表 %s 处于 dirty（版本 %d）：--purge 拒绝在脏态回滚"+
			"（不静默 force）——先 `aigo plugin migrate-force %s %d` 处置", name, table, version, name, version)
	}

	migrations, err := ScanMigrations(dir)
	if err != nil {
		return 0, err
	}
	applied := 0
	for _, migration := range migrations {
		if migration.Kind == "up" && uint(migration.Version) <= version {
			applied++
		}
	}
	if applied > 0 {
		if err := migrator.DownAll(); err != nil {
			return 0, fmt.Errorf("按 down 逆序回滚插件 %s 的迁移失败: %w", name, err)
		}
	}
	if err := DropMigrationTable(ctx, db, table); err != nil {
		return applied, err
	}
	env.Printf("插件迁移：已回滚 %d 个版本（当前版本 %d）并 drop 版本表 %s\n", applied, version, table)
	return applied, nil
}

// DropMigrationTable 删除插件版本表（--purge 与「本次安装新建的表」补偿用）
func DropMigrationTable(ctx context.Context, db *gorm.DB, table string) error {
	if !identifierSafe.MatchString(table) {
		return fmt.Errorf("版本表名 %q 不是安全标识符：拒绝拼进 SQL", table)
	}
	if err := db.WithContext(ctx).Exec(`DROP TABLE IF EXISTS "` + table + `"`).Error; err != nil {
		return fmt.Errorf("drop 版本表 %s 失败: %w", table, err)
	}
	return nil
}

// DropPluginTable drop 一张插件物理表（--purge 的 L2 表；表名来自元数据行，仍过标识符白名单）
func DropPluginTable(ctx context.Context, db *gorm.DB, table string) error {
	if !identifierSafe.MatchString(table) {
		return fmt.Errorf("物理表名 %q 不是安全标识符：拒绝拼进 SQL", table)
	}
	if err := db.WithContext(ctx).Exec(`DROP TABLE IF EXISTS "` + table + `"`).Error; err != nil {
		return fmt.Errorf("drop 实体表 %s 失败: %w", table, err)
	}
	return nil
}

// migrationTableExists 版本表存在性（to_regclass 走 search_path；插件名/前缀拼出的名字已是闭集）
func migrationTableExists(ctx context.Context, db *gorm.DB, table string) (bool, error) {
	var name sql.NullString
	if err := db.WithContext(ctx).Raw("SELECT to_regclass(?)::text", table).Row().Scan(&name); err != nil {
		return false, fmt.Errorf("检查版本表 %s 失败: %w", table, err)
	}
	return name.Valid && name.String != "", nil
}

// formatVersions 版本清单展示（1,2,3）
func formatVersions(versions []int) string {
	parts := make([]string, 0, len(versions))
	for _, version := range versions {
		parts = append(parts, fmt.Sprintf("%d", version))
	}
	return strings.Join(parts, ",")
}

// declarativeWireNote kind=declarative 的接线说明（无 Go 包 ⇒ 不写注册行）。
// **预演（plan.Registry）与执行（wirePlugin）共用同一文案**：dry-run 与执行同口径，。
const declarativeWireNote = "（kind=declarative：无 Go 包，不写注册行——L2 实体由宿主通用页与引擎承担）\n"

// wirePlugin 把插件追加入 register_gen.go（安装器唯一改写的宿主文件；第七步接线）。
// kind=declarative 没有 Go 包 ⇒ 不写注册行（写了 go build 必失败）。
func wirePlugin(env *Env, name, kind string) (string, error) {
	if kind == "declarative" {
		return declarativeWireNote, nil
	}
	registered, err := RegisteredPlugins(env.Root)
	if err != nil {
		return "", err
	}
	target := env.Abs(RegistryFile)
	current, err := os.ReadFile(target)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("读取 %s 失败: %w", RegistryFile, err)
	}
	next := BuildRegistryFile(append(append([]string{}, registered...), name))
	diff := RegistryDiff(current, next)
	// 生成物的父目录按需自建（该目录属宿主树，安装器只写这一个文件 ⇒ 建其所在目录是自洽的最小动作）
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return diff, fmt.Errorf("创建 %s 的父目录失败: %w", RegistryFile, err)
	}
	if err := atomicWriteFile(target, next, 0o644); err != nil {
		return diff, fmt.Errorf("改写 %s 失败: %w", RegistryFile, err)
	}
	return diff, nil
}

// unwirePlugin 撤下 register_gen.go 里的注册行（卸载 d；未注册时原样返回，不算失败）
func unwirePlugin(env *Env, name string) (string, bool, error) {
	current, err := os.ReadFile(env.Abs(RegistryFile))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Sprintf("（%s 不存在：无注册行可撤）", RegistryFile), false, nil
		}
		return "", false, fmt.Errorf("读取 %s 失败: %w", RegistryFile, err)
	}
	registered, err := RegisteredPluginsFrom(current)
	if err != nil {
		return "", false, err
	}
	rest := make([]string, 0, len(registered))
	for _, item := range registered {
		if item != name {
			rest = append(rest, item)
		}
	}
	next := BuildRegistryFile(rest)
	diff := RegistryDiff(current, next)
	if string(next) != string(current) {
		if err := atomicWriteFile(env.Abs(RegistryFile), next, 0o644); err != nil {
			return diff, false, fmt.Errorf("改写 %s 失败: %w", RegistryFile, err)
		}
		return diff, true, nil
	}
	return diff, false, nil
}

// pluginLedger 读取台账行（不存在 ⇒ ok=false；错误如实上报）
func pluginLedger(ctx context.Context, db *gorm.DB, name string) (host.Plugin, bool, error) {
	row, err := gorm.G[host.Plugin](db).Where("name = ?", name).First(ctx)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return host.Plugin{}, false, nil
	}
	if err != nil {
		return host.Plugin{}, false, fmt.Errorf("读取插件台账失败: %w", err)
	}
	return row, true, nil
}
