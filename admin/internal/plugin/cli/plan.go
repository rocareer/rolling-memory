package cli

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"
)

// PlanFile 计划中的一次落位（源文件 → 目标路径；同名冲突逐项提示）
type PlanFile struct {
	// Rel 源文件在插件目录内的相对路径（落位时按它读源；/ 分隔）
	Rel string
	// Target 仓库相对目标路径
	Target   string
	Bytes    int64
	SHA256   string
	Conflict string // "" = 新增；否则为工作树既有文件的同名状态描述
}

// InstallPlan 安装预演（--dry-run 输出表逐项；P2 的执行主线按同一计划落盘）
type InstallPlan struct {
	Name        string
	Source      string // 展示用源描述（目录 / 包）
	SourceRef   string // 落台账 source 列的值（包文件名 / 仓库相对目录）
	FromZip     bool   // 包源（zip 防护与签名策略施加）
	ZipChecksum string // 整包 sha256（包源）
	Dir         string // 插件目录（本机路径；包源为解包临时目录）
	Manifest    *manifest.Manifest
	Warnings    []string
	// Files 落位文件清单（源 → 目标映射；含 Rel 供落位读源）
	Files      []PlanFile
	Migrations []Migration
	// MigrationsPath 插件迁移目录（仓库相对路径）：安装 ⑥ 读**已落位**处，卸载 --purge 读同一处
	MigrationsPath string
	// Menus 安装器要代写的菜单/节点全集：manifest 声明 +公式补齐（BuildInstallMenus）
	Menus []manifest.Menu
	// DDL L2 实体的结构计划读数（dry-run ⑤ 段逐条列出；执行主线 ④b 重新生成后应用）
	DDL []EntityDDL
	// Baseline 迁移基线（adopt）的只读读数（dry-run ③b 段逐条列出；执行主线 ⑥ 重算，见 baseline.go）
	Baseline *BaselineRead
	Registry string // register_gen.go 的前后差异
	Enable   bool   // --enable：安装完成后接着置 enabled
}

// MigrationsAbs 插件迁移目录的绝对路径（安装 ⑥ 与卸载 --purge 共用）
func (p *InstallPlan) MigrationsAbs(env *Env) string {
	return env.Abs(p.MigrationsPath)
}

// SourceMigrationsAbs **安装源**侧迁移目录的绝对路径（补偿回滚读 down 文件用它，见 failInstall）。
//
// 为什么不能读落位副本：补偿顺序是「删已落位文件 → 回滚已应用迁移 → 删台账/归属行」，
// 第一步已把 plugins/<名>/migrations 删掉（包源/新落位）或还原（目录源与落位同址）；
// down 文件在源目录里始终可取（包源的 staging 临时目录活到 Install 返回之后），这是该顺序下
// 唯一「按 down 逆序回滚」能读到的位置。
func (p *InstallPlan) SourceMigrationsAbs(env *Env) string {
	dir := "migrations"
	if p.Manifest != nil && strings.TrimSpace(p.Manifest.MigrationsDir) != "" {
		dir = p.Manifest.MigrationsDir
	}
	return filepath.Join(p.Dir, filepath.FromSlash(dir))
}

// MigrationsPath 插件迁移目录的仓库相对路径（默认 migrations/；插件落位后与卸载期同址）
func MigrationsPath(name string, m *manifest.Manifest) string {
	dir := "migrations"
	if m != nil && strings.TrimSpace(m.MigrationsDir) != "" {
		dir = m.MigrationsDir
	}
	return path.Join(PluginDir(name), dir)
}

// BuildInstallPlan 构建安装预演（目录源入口；包源走 BuildInstallPlanFromSource——先经 StageZip 解包）
func BuildInstallPlan(env *Env, src string, enable bool) (*InstallPlan, error) {
	if strings.HasSuffix(strings.ToLower(strings.TrimSpace(src)), ".zip") {
		return nil, fmt.Errorf("zip 包源须先经 staging 解包（`aigo plugin install %s` 内部完成 zip 防护与清单校验）；"+
			"本函数只处理目录源", strings.TrimSpace(src))
	}
	dir, err := env.PluginSource(src)
	if err != nil {
		return nil, err
	}
	ref, err := filepath.Rel(env.Root, dir)
	if err != nil {
		ref = dir
	}
	return BuildInstallPlanFromSource(env, PlanSource{Dir: dir, Desc: dir, Ref: filepath.ToSlash(ref)}, enable)
}

// PlanSource 安装源（把解析好的源交给计划构建：目录源与包源（已解包）共用一条计划管线）
type PlanSource struct {
	// Dir 插件目录（本机路径；包源 = 解包临时目录）
	Dir string
	// Desc 展示用描述
	Desc string
	// Ref 落台账 source 列的值
	Ref string
	// FromZip 是否包源
	FromZip bool
	// Checksum 整包 sha256（包源）
	Checksum string
}

// BuildInstallPlanFromSource 构建安装预演（只读：不写库、不落盘、不改接线文件）
func BuildInstallPlanFromSource(env *Env, source PlanSource, enable bool) (*InstallPlan, error) {
	dir := source.Dir
	m, warnings, err := manifest.Load(dir, manifest.Options{
		Prefix:      env.Prefix,
		HostVersion: env.HostVersion,
		// 包源解包出的临时目录名与插件名无关（包根不承载名字）⇒ 跳过「目录名 == name」；
		// 其余校验（含 web / migrations_dir 路径存在性）照旧。落位时按 manifest.name 建 plugins/<名>/。
		IgnoreDirName: source.FromZip,
	})
	if err != nil {
		return nil, err
	}

	// 菜单/节点：manifest 声明 +公式补齐（实体页与权限节点由安装器代写；不符公式 ⇒ 拒装）。
	// 这一步是**安装期校验**，dry-run 与执行主线都经它 ⇒ 「不符拒装」先于任何落盘/写库。
	menus, err := BuildInstallMenus(m.Name, m)
	if err != nil {
		return nil, err
	}

	plan := &InstallPlan{
		Name:           m.Name,
		Source:         source.Desc,
		SourceRef:      source.Ref,
		FromZip:        source.FromZip,
		ZipChecksum:    source.Checksum,
		Dir:            dir,
		Manifest:       m,
		Warnings:       warnings,
		Menus:          menus,
		MigrationsPath: MigrationsPath(m.Name, m),
		Enable:         enable,
	}

	// ① 落位文件清单（源 → 目标映射，含同名冲突标记；包元数据不落位，见 BuildPlanFiles 注释）
	if plan.Files, err = BuildPlanFiles(env, dir, m.Name, m.Signature); err != nil {
		return nil, err
	}

	// ② 插件迁移逐文件（编号 / 语句数 / 禁词；编号唯一且成对）
	migrationsDir := m.MigrationsDir
	if migrationsDir == "" {
		migrationsDir = "migrations"
	}
	plan.Migrations, err = ScanMigrations(filepath.Join(dir, filepath.FromSlash(migrationsDir)))
	if err != nil {
		return nil, err
	}

	// ②b adopt（接管段）与迁移文件事实对账：through = 最大编号、1..through 连续、探针对象名必须出现在
	// 该版本 up 文件里（防虚）。装前就拒——manifest 的结构闭集只覆盖一半（见 verifyAdopt）。
	if err := verifyAdopt(filepath.Join(dir, filepath.FromSlash(migrationsDir)), m.Adopt, plan.Migrations); err != nil {
		return nil, err
	}

	// ③ 接线生成物的前后差异（安装器唯一改写的宿主文件）。
	// kind=declarative 没有 Go 包 ⇒ 执行主线（wirePlugin）不写注册行，预演也必须显示「无变化」：
	// dry-run 与执行同口径，预演里不许出现不会发生的写入（「A 的每一步都要能在 dry-run 中预演」）。
	if m.Kind == "declarative" {
		plan.Registry = declarativeWireNote
		return plan, nil
	}
	registered, err := RegisteredPlugins(env.Root)
	if err != nil {
		return nil, err
	}
	current := BuildRegistryFile(registered)
	if content, readErr := os.ReadFile(env.Abs(RegistryFile)); readErr == nil {
		current = content
	}
	next := BuildRegistryFile(append(append([]string{}, registered...), m.Name))
	plan.Registry = RegistryDiff(current, next)

	return plan, nil
}

// BuildPlanFiles 把插件目录内的文件映射为落位目标：
//   - 每个文件先落**源树** `plugins/<名>/<相对路径>`（含 `web/**`：源是唯一事实源，export 只以源打包）；
//   - `web/views/**` **另**落副本 `web/src/views/admin/plugin/<名>/**`（F7：必须在 views/admin 下）；
//   - `web/lang/<语言>.yaml` **另**落副本 `web/src/lang/<语言>/plugin/<名>.yaml`；
//   - `web/` 下其它文件 → 拒装（落位规则闭集，避免「复制了但没人认识」的文件）。
//
// **包元数据不落位**：`files.sha256` 与签名文件是**包的形态**，不进源码树（源码树布局
// 里没有它们；包留档在 plugins/.store/，清单由 export 每次重生成）——否则包源安装会给自己留下
// 「源目录带清单、而清单不认识落位时写入的归属标记」的自相矛盾现场。skip 追加列表由调用方传入
// （manifest 的 signature 声明）。
//
// 同一源文件出现两条计划项（源树 + 副本）是**有意为之**：两者都被源-副本 sha256 门禁覆盖，
// 且 zip 源安装后仓里同样有 `plugins/<名>/web/**`（否则源树缺前端源，export 与门禁都会失真）。
func BuildPlanFiles(env *Env, dir, name string, skip ...string) ([]PlanFile, error) {
	skipped := map[string]bool{PackManifestFile: true, DefaultSignatureFile: true}
	for _, item := range skip {
		if item = path.Clean(filepath.ToSlash(strings.TrimSpace(item))); item != "" && item != "." {
			skipped[item] = true
		}
	}

	var files []PlanFile
	err := filepath.WalkDir(dir, func(full string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, full)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if skipped[rel] {
			return nil
		}

		targets, err := planTargets(env, rel, name)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("插件目录含非普通文件 %q（%s）：拒装（只收普通文件）", rel, info.Mode().String())
		}
		sum, err := fileSHA256(full)
		if err != nil {
			return err
		}
		for _, target := range targets {
			files = append(files, PlanFile{
				Rel:      rel,
				Target:   target,
				Bytes:    info.Size(),
				SHA256:   sum,
				Conflict: conflictLabel(env, target, sum),
			})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("遍历插件目录 %s 失败: %w", dir, err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("插件目录 %s 内没有文件（至少要有 %s）", dir, manifest.FileName)
	}
	return files, nil
}

// planTargets 单文件的目标路径清单：源树（恒有）+ web 副本（views/lang 各一条）
func planTargets(env *Env, rel, name string) ([]string, error) {
	targets := []string{path.Join(PluginDir(name), rel)}
	if !strings.HasPrefix(rel, "web/") {
		return targets, nil
	}
	copyTarget, err := planCopyTarget(env, rel, name)
	if err != nil {
		return nil, err
	}
	return append(targets, copyTarget), nil
}

// planCopyTarget web 素材的副本目标：views/** 与 lang/<语言>.yaml 两种形态，其它一律拒装
func planCopyTarget(env *Env, rel, name string) (string, error) {
	rest := strings.TrimPrefix(rel, "web/")
	switch {
	case strings.HasPrefix(rest, "views/"):
		return path.Join(ViewTarget(name), strings.TrimPrefix(rest, "views/")), nil
	case strings.HasPrefix(rest, "lang/") && strings.HasSuffix(rest, ".yaml"):
		lang := strings.TrimSuffix(path.Base(rest), ".yaml")
		if _, err := os.Stat(env.Abs(path.Join(WebLangRoot, lang))); err != nil {
			return "", fmt.Errorf("web/lang 声明了语言 %q，但 %s/%s 不存在（语言目录缺失）", lang, WebLangRoot, lang)
		}
		return LangTarget(lang, name), nil
	default:
		return "", fmt.Errorf("web/ 下只允许 views/** 与 lang/<语言>.yaml（遇到 %q）：落位规则闭集，其它文件拒装", rel)
	}
}

// conflictLabel 工作树同名文件的冲突标记（文件清单须标出「是否与工作树既有文件同名」）
func conflictLabel(env *Env, target, sum string) string {
	existing, err := fileSHA256(env.Abs(target))
	if err != nil {
		return ""
	}
	if existing == sum {
		return "工作树已有同名文件（内容相同 ⇒ 幂等覆盖）"
	}
	return "工作树已有同名文件（内容不同 ⇒ 逐项确认或 --force）"
}

// Render 输出dry-run 全量预演
func (p *InstallPlan) Render(w io.Writer) {
	fmt.Fprintf(w, "插件安装预演：%s v%s（kind: %s）\n源：%s\n\n", p.Manifest.Name, p.Manifest.Version, p.Manifest.Kind, p.Source)

	fmt.Fprintf(w, "== ① manifest 摘要 ==\n")
	fmt.Fprintf(w, "name:          %s\n", p.Manifest.Name)
	fmt.Fprintf(w, "title:         %s\n", p.Manifest.Title)
	fmt.Fprintf(w, "version:       %s\n", p.Manifest.Version)
	fmt.Fprintf(w, "kind:          %s\n", p.Manifest.Kind)
	fmt.Fprintf(w, "host_api:      %s（与宿主机制接口版本 %s 相容）\n", p.Manifest.HostAPI, host.APIVersion)
	fmt.Fprintf(w, "framework_req: %s\n", orDash(p.Manifest.FrameworkReq))
	fmt.Fprintf(w, "requires:      %s\n", orDash(p.Manifest.Requires))
	fmt.Fprintf(w, "menus: %d 条（含公式代写 %d 条；公式口径见 ④ 段）/ entities: %d 个 / configs: %d 个 / 插件迁移: %d 个文件\n",
		len(p.Menus), len(formulaMenuNames(p.Manifest, p.Menus)), len(p.Manifest.Entities), len(p.Manifest.Configs), len(p.Migrations))
	for _, warning := range p.Warnings {
		fmt.Fprintf(w, "⚠ %s\n", warning)
	}

	fmt.Fprintf(w, "\n== ② 文件清单（目标路径 / 字节数 / sha256 / 工作树同名）==\n")
	for _, file := range p.Files {
		conflict := "新增"
		if file.Conflict != "" {
			conflict = file.Conflict
		}
		fmt.Fprintf(w, "%-64s %8d B  %s  %s\n", file.Target, file.Bytes, shortSHA(file.SHA256), conflict)
	}
	fmt.Fprintf(w, "落位方式：逐文件 mkdir -p + 原子 rename；逐级拒软链；白名单扩展名（.go/.vue/.ts/.yaml/.sql/.md/.css 等）在文件头写归属标记 `plugin: %s v%s`（幂等，审计留痕）\n",
		p.Name, p.Manifest.Version)

	fmt.Fprintf(w, "\n== ③ 插件迁移（编号 / 语句数 / 禁词）==\n")
	if len(p.Migrations) == 0 {
		fmt.Fprintln(w, "（无迁移文件）")
	}
	for _, migration := range p.Migrations {
		fmt.Fprintf(w, "%s\n", migration.Describe())
	}
	for _, migration := range p.Migrations {
		if len(migration.Banned) > 0 {
			fmt.Fprintf(w, "✗ %s 含禁词（%s）：整文件隐式事务内必报错，装前先改（docs/sql-discipline.md）\n",
				migration.Path, strings.Join(migration.Banned, ", "))
		}
	}

	// ③b 迁移基线（adopt）：三态判定的只读读数（探针逐条 + 将走的分支）。只读——不构造 migrator（构造即建版本表）。
	fmt.Fprintf(w, "\n== ③b 迁移基线（adopt 存量库接管）==\n")
	p.renderBaseline(w)

	fmt.Fprintf(w, "\n== ④ 将写入的菜单/节点（安装器代写 admin_rules 并打归属列 plugin=%s；status=0 隐藏至 enable）==\n", p.Name)
	fmt.Fprintf(w, "口径（公式）：分组 dir path=plugin/%s；实体页 menu path=plugin/%s/<实体>、component=%s、pid=分组、keepalive=0；\n",
		p.Name, p.Name, EntityRenderComponent)
	fmt.Fprintln(w, "           节点 node = 实体页 + {read,create,update,delete[,sort 仅 drag_sort]}（名字须与路由推导逐字相等）")
	if len(p.Menus) == 0 {
		fmt.Fprintln(w, "（无菜单声明，且无 L2 实体）")
	}
	auto := formulaMenuNames(p.Manifest, p.Menus)
	for _, menu := range p.Menus {
		origin := ""
		if _, ok := auto[menu.Name]; ok {
			origin = "（公式代写）"
		}
		fmt.Fprintf(w, "%-5s %-40s path=%-32s component=%-52s status=%d weigh=%d pid=%s%s\n",
			menu.Type, menu.Name, orDash(menu.Path), orDash(menu.Component), menu.Status, menu.Weigh, orDash(menu.Pid), origin)
	}

	fmt.Fprintf(w, "\n== ⑤ 将执行的 DDL（L2 实体结构；引擎按 spec 生成，装/升级/复装同一管线：实际表结构自省为基线）==\n")
	if len(p.Manifest.Entities) == 0 {
		fmt.Fprintln(w, "（无 L2 实体）")
	}
	for _, read := range p.DDL {
		fmt.Fprintf(w, "entity %s（ddl_owner=%s）表=%s\n", read.Entity, read.DDLOwner, orDash(read.Table))
		if read.Note != "" {
			fmt.Fprintf(w, "  · %s\n", read.Note)
		}
		// 无计划：ddl_owner=migrations（本就不发 DDL，Note 已在上面给出）或计划未生成（Err 给出理由）。
		// 必须先于「解引用计划」判定——Read.Plan 为 nil 而 Err 非空是**合法读数**（spec 非法、trgm 不可用、
		// 自省失败等都在 Build 里直接返回 nil 计划），早先的分支顺序会让预演在这里空指针 panic。
		if read.Plan == nil {
			if read.Err != nil {
				fmt.Fprintf(w, "  ✗ %s\n", read.Err.Error())
			}
			continue
		}
		// 有拒绝理由而计划非空：拒绝项与被拒批次的「已允许变更」并存 ⇒ 照常逐条列出（便于定位），
		// 但整批不执行（拒装），故不再声称「结构已与 spec 一致」。
		if read.Err != nil {
			fmt.Fprintf(w, "  ✗ %s\n", read.Err.Error())
			fmt.Fprintln(w, "  （以下为计划读数：**整批不执行**，仅用于定位待处理的变更）")
		} else {
			scope := "表不存在 ⇒ 建表 + 索引一次到位 + COMMENT ON，逐条独立事务、失败即停"
			if read.Existing {
				scope = "表已存在 ⇒ 自省基线的差异计划；表里存在而 spec 未声明的列一律保留，只进报告"
			}
			fmt.Fprintf(w, "  （%s）\n", scope)
		}
		if len(read.Plan.Statements) == 0 && read.Err == nil {
			fmt.Fprintln(w, "  （0 条：结构已与 spec 一致）")
		}
		for _, statement := range read.Plan.Statements {
			fmt.Fprintf(w, "  [%s] %s\n", statement.Kind, statement.Text)
		}
		for _, warning := range read.Warnings {
			fmt.Fprintf(w, "  ⚠ %s\n", warning)
		}
		for _, warning := range read.Plan.Warnings {
			fmt.Fprintf(w, "  ⚠ %s\n", warning)
		}
		if len(read.Plan.KeptColumns) > 0 {
			fmt.Fprintf(w, "  · 表里存在而 spec 未声明的列（保留，不自动 DROP）：%s\n", strings.Join(read.Plan.KeptColumns, ", "))
		}
	}
	if len(p.DDL) == 0 && len(p.Manifest.Entities) > 0 {
		fmt.Fprintln(w, "（结构计划未生成：见上方错误；dry-run 对引擎自管实体须读到库——fail-closed，不假装预演过）")
	}

	fmt.Fprintf(w, "\n== ⑥ 将改写的宿主文件 ==\n%s\n%s", RegistryFile, p.Registry)

	fmt.Fprintf(w, "\n== ⑦ 其余动作与生效方式 ==\n")
	fmt.Fprintf(w, "- 台账：写入 admin_plugins（status=failed → 安装完成后置 installed）\n")
	fmt.Fprintf(w, "- L2 实体结构（DDL）：引擎按 spec 逐条应用（表不存在 ⇒ 建表；已存在 ⇒ 自省基线补列/补索引；"+
		"ddl_owner=migrations 的实体由插件迁移承担，引擎不做 ALTER/DROP）\n")
	fmt.Fprintf(w, "- 迁移：按编号应用插件迁移（独立版本表 <前缀>plugin_%s_migrations，独立 advisory lock）%s\n",
		p.Name, p.baselineWireNote())
	fmt.Fprintf(w, "- 审计：写入 admin_plugin_actions（action=install）\n")
	if p.Enable {
		fmt.Fprintln(w, "- --enable：安装完成后置 enabled 并放开菜单（status=1）")
	} else {
		fmt.Fprintln(w, "- 菜单默认隐藏（status=0）；启用需 `aigo plugin enable "+p.Name+"`")
	}
	fmt.Fprintf(w, "- 生效方式：重编译 + 重启后端（接线真相在编译产物）；前端改动需 pnpm build（dev 下 HMR）\n")
	fmt.Fprintf(w, "- 信任定位：kind=code 的插件等价于把该段代码 git merge 进仓库——与宿主同权限、无隔离层\n")
}

// renderBaseline 打印 ③b 段（迁移基线的只读读数与将走的分支；/：dry-run 逐条列探针与判定）
func (p *InstallPlan) renderBaseline(w io.Writer) {
	read := p.Baseline
	if read == nil || !read.Declared {
		fmt.Fprintln(w, "（manifest 未声明 adopt 段：迁移按 ③ 的编号顺序应用——干净库 / 下游产品路径；版本表已有值 ⇒ 增量）")
		return
	}
	fmt.Fprintf(w, "adopt.through=%d（接管边界 = 插件迁移的最大编号；1..through 与核心链对应迁移同物）\n", read.Through)
	state := "不存在"
	if read.State.Existed {
		state = fmt.Sprintf("已存在（%s，dirty=%v）", describeVersion(read.State.Version), read.State.Dirty)
	}
	fmt.Fprintf(w, "版本表 %s：%s\n", read.Table, state)
	fmt.Fprintf(w, "探针 %d 条（对象探针 %d 条命中 %d）：\n", len(read.Probes), read.ProbesTotal, read.ProbesHit)
	for _, probe := range read.Probes {
		fmt.Fprintf(w, "  %s\n", probe.Describe())
	}
	switch read.Mode {
	case baselineModeAdopt:
		if read.State.Version > 0 {
			fmt.Fprintf(w, "判定：**baseline（adopt）**——版本表已在接管域内（%d），归位到 %d（迁移 SQL 0 条 / 版本表写 %d 行），不执行任何旧 DDL\n",
				read.State.Version, read.Through, boolToInt(read.State.Version != read.Through))
		} else {
			fmt.Fprintf(w, "判定：**baseline（adopt）**——存量结构已在位：只把版本表写为 1..%d（迁移 SQL 0 条 / 版本表写 1 行），不执行任何旧 DDL\n",
				read.Through)
		}
	case baselineModeFull:
		fmt.Fprintf(w, "判定：**全量**（%s）：按 ③ 的编号顺序应用插件链（存量臂不触发）\n", read.Reason)
	case baselineModeRejected:
		fmt.Fprintf(w, "判定：**拒装**（fail-closed，零副作用）：%s\n", read.Reason)
		for _, missing := range read.Missing {
			fmt.Fprintf(w, "  %s\n", missing)
		}
		fmt.Fprintf(w, "  处置①：先把核心链推到位（`aigo migrate up`）再重装；处置②：人工确认后 `aigo plugin migrate-force %s <版本>` 归位版本表\n", p.Name)
	case baselineModeUnread:
		fmt.Fprintf(w, "判定：**未完成**（读库失败，fail-closed；不假装预演过）：%s\n", read.Reason)
	default:
		fmt.Fprintf(w, "判定：%s\n", read.Mode)
	}
}

// baselineWireNote ⑦ 段的迁移一行按 ③b 的判定改口径（未声明 adopt 时不留尾巴）
func (p *InstallPlan) baselineWireNote() string {
	if p.Baseline == nil || !p.Baseline.Declared {
		return ""
	}
	return "；声明了 adopt ⇒ 按 ③b 的三态判定：全命中只写基线（0 条旧 DDL）/ 全不命中全量 / 部分命中拒装"
}

// shortSHA 展示用的 sha256 前缀
func shortSHA(sum string) string {
	if len(sum) > 12 {
		return sum[:12] + "…"
	}
	return sum
}

// orDash 空值展示为 —
func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}
