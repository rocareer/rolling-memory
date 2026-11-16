package cli

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// migrationFilePattern 插件迁移文件名：<n>_<名>.{up,down}.sql（插件内独立自增 1,2,3…）
var migrationFilePattern = regexp.MustCompile(`^(\d+)_([A-Za-z0-9._-]+)\.(up|down)\.sql$`)

// bannedMigrationWords 迁移文件禁词（docs/sql-discipline.md+ plugins/README.md+dry-run 项）：
//   - CONCURRENTLY：整文件隐式事务内必报错（语句内任意位置判定）；
//   - BEGIN / COMMIT：文件内显式事务语句（只在**语句首词**位置判定，见 bannedStatementStartWords）；
//   - ADMIN_RULES / ADMIN_GROUPS：插件迁移不得写宿主菜单 / 角色组表（plugins/README.md硬约定：
//     「插件自带 SQL 不得写 admin_rules」——声明的菜单/节点由**安装器**按 manifest 代写并打归属列
//     `plugin`，插件自己写会让归属漂移、卸载清不干净；角色组表同理，卸载清理面要写回各
//     admin_groups.rules CSV 里的悬挂 ID，插件写它则授权关系失控）。剥离 __PREFIX__ 占位符后按词比对
//     （插件迁移的规范写法是 "__PREFIX__admin_rules"）。
var bannedMigrationWords = []string{"CONCURRENTLY", "BEGIN", "COMMIT", "ADMIN_RULES", "ADMIN_GROUPS"}

// bannedStatementStartWords 只在语句首词位置判定的禁词（PL/pgSQL 函数体在美元引用块内，已被扫描跳过；
// 语句首词的 BEGIN/COMMIT 才是文件内显式事务）
var bannedStatementStartWords = map[string]bool{"BEGIN": true, "COMMIT": true}

// bannedMigrationWordSet 「任意位置」判定的禁词集合（BEGIN/COMMIT 不在此列——它们走语句首词口径）
var bannedMigrationWordSet = func() map[string]bool {
	set := make(map[string]bool, len(bannedMigrationWords))
	for _, word := range bannedMigrationWords {
		if bannedStatementStartWords[word] {
			continue
		}
		set[word] = true
	}
	return set
}()

// Migration 一个插件迁移文件（dry-run 与 verify 的逐文件清单项）
type Migration struct {
	Version    int
	Name       string
	Kind       string // up | down
	Path       string // 文件名（迁移目录内；目录由调用方掌握）
	Bytes      int64
	Statements int
	Banned     []string
}

// Describe 单行摘要（dry-run「编号 / 语句数 / 是否含禁词」）
func (m Migration) Describe() string {
	banned := "无禁词"
	if len(m.Banned) > 0 {
		banned = "含禁词: " + strings.Join(m.Banned, ", ")
	}
	return fmt.Sprintf("%2d_%s.%s.sql  %5d 字节  %3d 条语句  %s", m.Version, m.Name, m.Kind, m.Bytes, m.Statements, banned)
}

// ScanMigrations 扫描插件迁移目录（不存在 ⇒ 空清单，即「无迁移可应用」）。
// 编号必须唯一且 up/down 成对（check-plugin-manifest.sh 同口径），否则拒装。
func ScanMigrations(dir string) ([]Migration, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取迁移目录 %s 失败: %w", dir, err)
	}

	var out []Migration
	present := map[string]bool{} // "version/kind" 去重
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		match := migrationFilePattern.FindStringSubmatch(entry.Name())
		if match == nil {
			return nil, fmt.Errorf("迁移文件名 %q 不符合 <n>_<名>.{up,down}.sql 约定（%s）", entry.Name(), dir)
		}
		version, err := strconv.Atoi(match[1])
		if err != nil {
			return nil, fmt.Errorf("迁移文件名 %q 的编号不是整数: %w", entry.Name(), err)
		}
		key := fmt.Sprintf("%d/%s", version, match[3])
		if present[key] {
			return nil, fmt.Errorf("迁移编号 %d 的 %s 文件重复声明", version, match[3])
		}
		present[key] = true

		content, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("读取迁移文件 %q 失败: %w", entry.Name(), err)
		}
		scan := scanSQL(string(content))
		out = append(out, Migration{
			Version:    version,
			Name:       match[2],
			Kind:       match[3],
			Path:       entry.Name(),
			Bytes:      int64(len(content)),
			Statements: scan.Statements,
			Banned:     scan.Banned,
		})
	}

	// 成对校验（编号唯一且 up/down 成对）
	for _, migration := range out {
		other := "down"
		if migration.Kind == "down" {
			other = "up"
		}
		if !present[fmt.Sprintf("%d/%s", migration.Version, other)] {
			return nil, fmt.Errorf("迁移编号 %d 缺少 .%s.sql（up/down 必须成对）", migration.Version, other)
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Version != out[j].Version {
			return out[i].Version < out[j].Version
		}
		return out[i].Kind == "up" // up 排在 down 前
	})
	return out, nil
}

// PluginMigrator 单个插件的迁移执行器：
//   - 独立版本表 <前缀>plugin_<名>_migrations（驱动 x-migrations-table 查询参数）；
//   - 独立 advisory lock（驱动锁 ID 由库名/模式/迁移表名生成 ⇒ 多插件并发安装互不阻塞）；
//   - 事务语义与宿主一致（整文件一个隐式事务 ⇒ 同样禁 CONCURRENTLY）。
type PluginMigrator struct {
	Name    string
	Dir     string
	Table   string
	Migrate *migrate.Migrate
}

// NewPluginMigrator 构造插件迁移执行器（dir 为插件迁移目录）
func NewPluginMigrator(name, dir, prefix string) (*PluginMigrator, error) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("插件 %s 没有迁移目录（%s）：无迁移可应用、无版本表可修", name, dir)
	}
	table := prefix + "plugin_" + name + "_migrations"

	src, err := NewMigrationSource(dir, prefix)
	if err != nil {
		return nil, err
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, MigrateDSN(table, ""))
	if err != nil {
		return nil, fmt.Errorf("连接数据库（迁移表 %s）: %w", table, err)
	}
	return &PluginMigrator{Name: name, Dir: dir, Table: table, Migrate: m}, nil
}

// Close 关闭迁移实例（幂等；驱动 Close 会同时关掉连接）
func (p *PluginMigrator) Close() {
	if p.Migrate != nil {
		_, _ = p.Migrate.Close()
	}
}

// Version 当前版本与脏标记
func (p *PluginMigrator) Version() (version uint, dirty bool, err error) {
	version, dirty, err = p.Migrate.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return 0, false, nil
	}
	return version, dirty, err
}

// Up 应用全部待执行迁移（P2 的安装链会调它；失败即停，dirty 照实上报）
func (p *PluginMigrator) Up() error {
	if err := p.Migrate.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

// DownAll 回滚全部已应用迁移（--purge 用；按 down 逆序由驱动保证）
func (p *PluginMigrator) DownAll() error {
	if err := p.Migrate.Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

// Force 把版本表强制置为指定版本（修复 dirty 的唯一入口；：不做静默重试、不吞错）
func (p *PluginMigrator) Force(version int) error {
	return p.Migrate.Force(version)
}

// MigrationTableName 插件版本表物理名（<前缀>plugin_<名>_migrations）
func MigrationTableName(name, prefix string) string {
	return prefix + "plugin_" + name + "_migrations"
}

// MigrateForce 把插件版本表强制置为指定版本（修复 dirty 的唯一入口）。
// 不做静默重试：dirty 状态先照实打印，force 由人显式发起。
func MigrateForce(env *Env, name string, version int) error {
	dir, err := env.PluginSource(name)
	if err != nil {
		return err
	}
	m, _, err := manifest.Load(dir, manifest.Options{Prefix: env.Prefix, HostVersion: env.HostVersion})
	if err != nil {
		return err
	}
	migrationsDir := m.MigrationsDir
	if migrationsDir == "" {
		migrationsDir = "migrations"
	}

	migrator, err := NewPluginMigrator(m.Name, filepath.Join(dir, filepath.FromSlash(migrationsDir)), env.Prefix)
	if err != nil {
		return err
	}
	defer migrator.Close()

	current, dirty, err := migrator.Version()
	if err != nil {
		return fmt.Errorf("读取插件 %s 的迁移状态失败: %w", m.Name, err)
	}
	state := "干净"
	if dirty {
		state = "脏（有迁移失败）"
	}
	env.Printf("插件 %s：版本表 %s，当前版本 %d（%s）\n", m.Name, migrator.Table, current, state)

	if err := migrator.Force(version); err != nil {
		return fmt.Errorf("force 失败: %w", err)
	}
	env.Printf("已 force 到版本 %d（仅改版本表标记，不执行也不回滚任何 SQL——修完现场后再 `aigo plugin migrate-force %s <目标版本>` 归位）\n", version, m.Name)
	return nil
}

// NewMigrationSource 构造迁移源（磁盘目录 + __PREFIX__ 前缀替换）。
// 前缀替换与 cmd/migrate/prefix.go 同机制（该实现在 cmd 包内不可复用，此处同构实现；
// 两处口径：只替换 ReadUp/ReadDown 返回的 SQL 文本）。导出以便测试复用同一口径。
func NewMigrationSource(dir, prefix string) (source.Driver, error) {
	src, err := iofs.New(os.DirFS(dir), ".")
	if err != nil {
		return nil, fmt.Errorf("加载迁移文件（%s）: %w", dir, err)
	}
	return &prefixDriver{Driver: src, prefix: prefix}, nil
}

// MigrateDSN 迁移用的数据库 DSN（写库配置）：
//   - table 非空 ⇒ `x-migrations-table` 指定独立版本表（插件迁移；空则用驱动默认 schema_migrations）；
//   - schema 非空 ⇒ `search_path` 限定 schema（lib/pq 把非驱动参数作为会话运行时参数下发；
//     测试的隔离往返用它把整条迁移链关进临时 schema）。
func MigrateDSN(table, schema string) string {
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
	if table != "" {
		q.Set("x-migrations-table", table)
	}
	if schema != "" {
		q.Set("search_path", schema)
	}
	if tz := cfg.App.Timezone; tz != "" {
		q.Set("TimeZone", tz)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// prefixDriver 包装 source.Driver，把迁移 SQL 里的 __PREFIX__ 替换为表前缀
// （与 cmd/migrate/prefix.go 同形：整文件读出替换后再交给驱动执行）
type prefixDriver struct {
	source.Driver
	prefix string
}

// ReadUp 读取 up 迁移并替换前缀
func (d *prefixDriver) ReadUp(version uint) (io.ReadCloser, string, error) {
	r, id, err := d.Driver.ReadUp(version)
	if err != nil {
		return nil, "", err
	}
	return d.replace(r), id, nil
}

// ReadDown 读取 down 迁移并替换前缀
func (d *prefixDriver) ReadDown(version uint) (io.ReadCloser, string, error) {
	r, id, err := d.Driver.ReadDown(version)
	if err != nil {
		return nil, "", err
	}
	return d.replace(r), id, nil
}

// replace 读整份 SQL、替换占位符后回灌
func (d *prefixDriver) replace(r io.ReadCloser) io.ReadCloser {
	data, _ := io.ReadAll(r)
	_ = r.Close()
	replaced := strings.ReplaceAll(string(data), "__PREFIX__", d.prefix)
	return io.NopCloser(strings.NewReader(replaced))
}

// sqlScan 极简 SQL 词法扫描结果（只服务 dry-run/verify 的预览口径：
// 语句数按「非空语句的分号」计数；禁词在字符串/注释/美元引用块内不误报）
type sqlScan struct {
	Statements int
	Banned     []string
	// DDL 本文件里「创建对象」语句的形态清单（去重、按出现顺序）：CREATE TABLE / CREATE INDEX /
	// CREATE UNIQUE INDEX / ALTER TABLE ADD。adopt 段的防漏门禁按它判定「该版本是否必须至少一条
	// 对象探针」（baseline.go 的 verifyAdopt），判据是**文件事实**而非作者自述。
	DDL []string
}

// scanSQL 扫描迁移 SQL（跳过行注释、块注释、单引号字符串与 $tag$ 美元引用块）
func scanSQL(data string) sqlScan {
	seen := map[string]bool{}
	ddlSeen := map[string]bool{}
	var ddl []string
	words := make([]string, 0, 8) // 当前语句的词序列（DDL 形态识别用）
	statements := 0
	statementOpen := false
	ban := func(word string) { seen[word] = true }
	// finishStatement 语句边界：按词序列记 DDL 形态（去重），并清空词缓冲
	finishStatement := func() {
		if form := ddlObjectCreation(words); form != "" && !ddlSeen[form] {
			ddlSeen[form] = true
			ddl = append(ddl, form)
		}
		words = words[:0]
	}

	i, n := 0, len(data)
	for i < n {
		c := data[i]
		switch {
		case c == '-' && i+1 < n && data[i+1] == '-':
			for i < n && data[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && data[i+1] == '*':
			i += 2
			for i+1 < n && !(data[i] == '*' && data[i+1] == '/') {
				i++
			}
			i += 2
		case c == '\'':
			i++
			for i < n {
				if data[i] == '\'' {
					if i+1 < n && data[i+1] == '\'' {
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
		case c == '$':
			if delim := dollarQuoteDelim(data[i:]); delim != "" {
				rest := data[i+len(delim):]
				end := strings.Index(rest, delim)
				if end < 0 {
					i = n
					break
				}
				i += len(delim) + end + len(delim)
				continue
			}
			i++
		case c == ';':
			if statementOpen {
				statements++
				statementOpen = false
				finishStatement()
			}
			i++
		default:
			if isWordChar(c) {
				start := i
				for i < n && isWordChar(data[i]) {
					i++
				}
				token := strings.ToUpper(data[start:i])
				switch {
				case bannedStatementStartWords[token]:
					// BEGIN/COMMIT 只在**语句首词**位置算禁词（PL/pgSQL 函数体已被美元引用块跳过）
					if !statementOpen {
						ban(token)
					}
				default:
					// __PREFIX__ 占位符先剥掉再比对（该占位符在执行期被替换为表前缀，扫描期不可知，
					// 故按占位符形态拦：「__PREFIX__admin_rules」与「admin_rules」都是同一处违规）
					word := strings.TrimPrefix(token, "__PREFIX__")
					if bannedMigrationWordSet[word] {
						ban(word)
					}
				}
				words = append(words, token)
				statementOpen = true
				continue
			}
			if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
				statementOpen = true
			}
			i++
		}
	}
	if statementOpen {
		statements++ // 无结尾分号的最后一条语句
		finishStatement()
	}

	banned := make([]string, 0, len(seen))
	for _, word := range bannedMigrationWords {
		if seen[word] {
			banned = append(banned, word)
		}
	}
	return sqlScan{Statements: statements, Banned: banned, DDL: ddl}
}

// ddlObjectCreation 语句词序列（大写，不含注释/字符串/美元引用块内的词）→「创建对象」形态标签；
// 非创建 ⇒ 空串。闭集（= adopt 段防漏门禁的判据，baseline.go 的 verifyAdopt）：
//
//	CREATE TABLE            → 建表
//	CREATE [UNIQUE] INDEX   → 建索引
//	ALTER TABLE ... ADD ... → 加列 / 加约束
//
// 这三类形态建出的对象**没有任何别的链会补建**（核心链 `aigo migrate up` 只推宿主表），故含它们的版本
// 必须声明对象探针；纯 INSERT / UPDATE / COMMENT ON / ALTER COLUMN SET DEFAULT 等回填与文案形态不在闭集内
// （这些版本允许 kind: none）。
//
// 判法取舍（fail-closed）：ALTER TABLE 语句里**只要出现 ADD 这个词**即判为「加对象」——不按位次取
// （ONLY / IF EXISTS 会占位，schema 限定名又会多一个词，按位次取反而漏判）。代价是词法层的假阳性极小
// 且方向安全：`ALTER TABLE t RENAME COLUMN b TO add`（把 add 当标识名）会被多拦一次，作者补一条探针即可；
// 反之漏判会让「加列的版本声明成 kind: none」混过去（正是本门禁要堵的洞）。ALTER ... DROP / RENAME / SET
// 不在此列（非创建）。
func ddlObjectCreation(words []string) string {
	if len(words) == 0 {
		return ""
	}
	switch words[0] {
	case "CREATE":
		switch {
		case len(words) > 1 && words[1] == "TABLE":
			return "CREATE TABLE"
		case len(words) > 1 && words[1] == "INDEX":
			return "CREATE INDEX"
		case len(words) > 2 && words[1] == "UNIQUE" && words[2] == "INDEX":
			return "CREATE UNIQUE INDEX"
		}
	case "ALTER":
		if len(words) > 1 && words[1] == "TABLE" && slices.Contains(words[2:], "ADD") {
			return "ALTER TABLE ADD"
		}
	}
	return ""
}

// dollarQuoteDelim 匹配 $tag$ 形式的美元引用定界符（$1 之类的占位符不匹配）
func dollarQuoteDelim(s string) string {
	if len(s) < 2 || s[0] != '$' {
		return ""
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if c == '$' {
			return s[:i+1]
		}
		if !isWordChar(c) {
			return ""
		}
	}
	return ""
}

// isWordChar 标识符字符（字母 / 数字 / 下划线）
func isWordChar(c byte) bool {
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
