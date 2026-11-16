// Package plugin 是插件管理面的业务层（设计说明）。
//
// 三项纪律：
//   - **写路径复用 CLI 单点**：enable/disable 调 internal/plugin/cli 的 Enable/Disable——
//     状态机（四态与允许的下一步）、菜单/节点 status 同步、审计写入是同一份实现，
//     UI 与 CLI 同一份留痕（「管理页的启用/停用复用同一写入函数」；cli 包文档亦声明
//     「便于 P2 的管理页与测试复用」）；
//   - **三处真相显式报告**：台账（本包）、进程内注册表（host）、注册文件（register_gen.go）
//     的差异只报不对齐（/R2）；
//   - **不假装已生效**：台账置 enabled 而进程内没有该插件时回执 effect=rebuild_required
//     （「待重编译」），由前端按 i18n 文案提示。
package plugin

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"
	"github.com/ai-go-hub/ai-go-admin/internal/model"
	pluginCli "github.com/ai-go-hub/ai-go-admin/internal/plugin/cli"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"
	repoPlugin "github.com/ai-go-hub/ai-go-admin/internal/repository/admin/plugin"
	"github.com/ai-go-hub/ai-go-admin/internal/service"
	"github.com/ai-go-hub/ai-go-admin/internal/version"

	"gorm.io/gorm"
)

// ErrNotFound 台账无该插件（未安装）
var ErrNotFound = errors.New("插件不存在（台账无此插件：未安装或已卸载）")

// 差异码（「三处真相」；逐条对齐 internal/plugin/cli 的 driftNotes 同名分支）。
// 后端只给码，文案由前端语言包渲染（plugin.list.wired.*）。
//
// 覆盖面（如实）：要求的标记面 =「运行时是否接线」+「台账/进程内/注册文件」三方差异，
// 本组码即此（接线悬空另归页面级 Runtime.Orphans）。cli.driftNotes 额外还有两条**排查面**诊断——
// 「code 插件不应有 L2 实体行（归属漂移）」与「注册行在而源码目录缺失（go build 会失败）」——
// 管理页未覆盖：加码须同时补前端字典与双语语言包，属增量扩展，留给需要时再议。
const (
	// DriftUnwired 待重编译：台账在、进程内无该插件 ⇒ 路由 404
	DriftUnwired = "unwired"
	// DriftMissingRegister 缺注册行：重编译后也不生效
	DriftMissingRegister = "missing_register"
	// DriftDeclarativeRegister 声明式插件不应有注册行（无 Go 包，go build 会失败）
	DriftDeclarativeRegister = "declarative_register"
	// DriftFailed 半装：清理入口 `aigo plugin uninstall --force <名>`
	DriftFailed = "failed"
)

// 生效方式码（enable/disable 回执；对应 cli.setPluginStatus 的同一段分支，顺序也一致）
const (
	// EffectGateTTL 运行时闸按请求读库（≤1s 缓存）：无需重启
	EffectGateTTL = "gate_ttl"
	// EffectRebuildRequired 待重编译 + 重启（接线真相在编译产物）
	EffectRebuildRequired = "rebuild_required"
	// EffectDeclarative 声明式插件：无 Go 注册行，实体路由随重启后注册
	EffectDeclarative = "declarative"
)

// Row 列表行：台账行 + 运行时标记（管理页「接线」与「差异」两列的取值面）
type Row struct {
	model.Plugin
	// Entities 该插件的 L2 实体行数
	Entities int `json:"entities"`
	// Wired 进程内注册表是否认识该插件（编译期接线的真相）
	Wired bool `json:"wired"`
	// Registered 注册文件是否有该插件的注册行；**nil = 注册文件不可读**
	// （不在源码检出内运行）——此时不谎报为 false「缺注册行」
	Registered *bool `json:"registered"`
	// Drift 差异码列表（DriftUnwired 等；空 = 三处一致）
	Drift []string `json:"drift"`
}

// Runtime 页面级运行时对照（三处真相）
type Runtime struct {
	// RegistryAvailable 注册文件是否读到（false ⇒ 各行 Registered 一律 nil）
	RegistryAvailable bool `json:"registry_file"`
	// Wired 进程内注册表的插件名（升序）
	Wired []string `json:"wired"`
	// Registered 注册文件里的插件名（升序；RegistryAvailable=false 时为空）
	Registered []string `json:"registered"`
	// Orphans 接线悬空：进程内有、台账无（装一次或撤注册行）
	Orphans []string `json:"orphans"`
}

// FileRow 落位文件清单行（源 = cli.BuildPlanFiles，与 `install --dry-run` 的 ② 段同源）
type FileRow struct {
	// Rel 源文件在插件目录内的相对路径
	Rel string `json:"rel"`
	// Target 仓库相对目标路径（源树 + views/lang 副本各一条，与落位计划逐条对齐）
	Target string `json:"target"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
	// Conflict 工作树同名文件的诊断文本（CLI dry-run 同源，中文；空 = 新增）
	Conflict string `json:"conflict"`
}

// Files 文件清单视图
type Files struct {
	// Available 是否读到源目录（false：不在源码检出内运行，或源目录不存在——如声明式插件）
	Available bool `json:"available"`
	// Source 插件源码目录（仓库相对路径）
	Source string    `json:"source,omitempty"`
	List   []FileRow `json:"list"`
}

// AuditView 动作审计面板（分页；page/limit 为归一后的生效值）
type AuditView struct {
	List  []repoPlugin.PluginAction `json:"list"`
	Total int64                     `json:"total"`
	Page  int                       `json:"page"`
	Limit int                       `json:"limit"`
}

// Detail 单插件详情（接口面：manifest 摘要 + 文件清单 + 校验和 + 实体列表 + 动作审计。
// 校验和即台账行自带的 checksum 列，不另立字段）
type Detail struct {
	Row      Row                       `json:"row"`
	Entities []repoPlugin.PluginEntity `json:"entities"`
	Audit    AuditView                 `json:"audit"`
	Files    Files                     `json:"files"`
}

// ActionResult enable/disable 的回执
type ActionResult struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Kind   string `json:"kind"`
	Wired  bool   `json:"wired"`
	// Effect 生效方式码：gate_ttl | rebuild_required | declarative
	Effect string `json:"effect"`
}

// Service 插件管理面服务，嵌入通用服务（List/Get/Count 等走泛型基类）
type Service struct {
	service.IService[model.Plugin]
	repo *repoPlugin.Repository

	// envOnce/cliEnv 懒建 CLI 环境（enable/disable 复用 cli 单点）
	envOnce sync.Once
	env     *pluginCli.Env

	// rootOnce 仓库根定位一次（注册文件与插件源目录都在检出内）
	rootOnce sync.Once
	root     string
	rootErr  error
}

// NewService 创建插件管理面服务实例
func NewService(repo *repoPlugin.Repository) *Service {
	return &Service{
		IService: service.NewService(repo),
		repo:     repo,
	}
}

// BuildRows 给台账行补运行时标记，并返回页面级的三处对照（list 契约）。
// 开销：1 次实体计数读 + 1 次全量插件名读 + 1 次注册文件读（小表与本地文件，后台低频面）。
func (s *Service) BuildRows(ctx context.Context, list []model.Plugin) ([]Row, *Runtime, error) {
	view, err := s.runtime(ctx)
	if err != nil {
		return nil, nil, err
	}
	counts, err := s.repo.EntityCountsByPlugin(ctx)
	if err != nil {
		return nil, nil, err
	}

	rows := make([]Row, 0, len(list))
	for i := range list {
		rows = append(rows, buildRow(&list[i], counts[list[i].Name], view))
	}
	return rows, view, nil
}

// Detail 单插件详情
func (s *Service) Detail(ctx context.Context, name string, page, limit int) (*Detail, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("插件名不能为空")
	}

	ledger, err := s.repo.FindByName(ctx, name)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	entities, err := s.repo.ListEntities(ctx, name)
	if err != nil {
		return nil, err
	}

	page, limit = repoPlugin.NormalizeAuditPage(page, limit)
	audit, err := s.repo.ListActions(ctx, name, page, limit)
	if err != nil {
		return nil, err
	}
	total, err := s.repo.CountActions(ctx, name)
	if err != nil {
		return nil, err
	}

	view, err := s.runtime(ctx)
	if err != nil {
		return nil, err
	}
	counts, err := s.repo.EntityCountsByPlugin(ctx)
	if err != nil {
		return nil, err
	}

	return &Detail{
		Row:      buildRow(ledger, counts[name], view),
		Entities: entities,
		Audit:    AuditView{List: audit, Total: total, Page: page, Limit: limit},
		Files:    s.fileList(name),
	}, nil
}

// Enable 置 enabled（台账 + 菜单/节点 status + 审计；写路径复用 cli 单点）
func (s *Service) Enable(ctx context.Context, name, operator string) (*ActionResult, error) {
	return s.setStatus(ctx, name, operator, host.StatusEnabled)
}

// Disable 置 disabled（同一写路径；「还原宿主」只有这一条代码路径）
func (s *Service) Disable(ctx context.Context, name, operator string) (*ActionResult, error) {
	return s.setStatus(ctx, name, operator, host.StatusDisabled)
}

// setStatus 启停共用：调 cli.Enable/Disable（状态机与审计的唯一实现），再按结果拼回执
func (s *Service) setStatus(ctx context.Context, name, operator, target string) (*ActionResult, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("插件名不能为空")
	}

	switch target {
	case host.StatusEnabled:
		if err := pluginCli.Enable(ctx, s.cliEnv(), name, operator); err != nil {
			return nil, err
		}
	case host.StatusDisabled:
		if err := pluginCli.Disable(ctx, s.cliEnv(), name, operator); err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("未知目标状态: " + target)
	}

	row, err := s.repo.FindByName(ctx, name)
	if err != nil {
		return nil, err
	}
	result := &ActionResult{Name: row.Name, Status: row.Status, Kind: row.Kind, Wired: s.isWired(row.Name)}
	switch {
	case row.Kind == "declarative":
		// 声明式插件没有 Go 注册行（L2 实体路由由引擎按元数据注册）——「待重编译」对它是误报
		result.Effect = EffectDeclarative
	case !result.Wired:
		result.Effect = EffectRebuildRequired
	default:
		result.Effect = EffectGateTTL
	}
	return result, nil
}

// UninstallParams 卸载参数（HTTP 面；与 CLI 旗标一一对应，映射单点见 uninstallOptions）
type UninstallParams struct {
	// Purge 彻底卸载：回滚插件迁移 + drop 版本表与引擎侧 L2 表（不可恢复）
	Purge bool
	// KeepFiles 只清运行态：保留源码与落位文件（入库仓「出厂态」）
	KeepFiles bool
	// Force 仅给 failed 半装现场（CLI 对 enabled 仍一律拒绝，不因 force 放行）
	Force bool
}

// UninstallResult 卸载回执（report = CLI 同一实现的执行回执，脱敏后原样回报）
type UninstallResult struct {
	Name      string `json:"name"`
	Purge     bool   `json:"purge"`
	KeepFiles bool   `json:"keep_files"`
	// Effect 生效方式码：rebuild_required（code/mixed：注册行已撤，需重编译 + 重启）|
	// gate_ttl（declarative：无 Go 注册行，运行时闸按台账即时拒绝）
	Effect string `json:"effect"`
	// Report 执行回执（CLI 输出已脱敏；含逐段删/保留清单与计数）
	Report string `json:"report"`
}

// Uninstall 卸载插件：写路径复用 cli.Uninstall 单点（环境门禁 / 先禁用后卸载硬门禁 / --purge 二次确认 /
// adopt 装入的 --purge 默认拒绝 全在 CLI 侧同一实现，本层不复制规则）。需要**源码检出**（落位物与注册行
// 都在检出内）：仓库根不可用时如实拒绝，不退化成「只清库」（那会留下进程内注册与仓库文件的双重真相漂移）。
func (s *Service) Uninstall(ctx context.Context, name string, params UninstallParams, operator string) (*UninstallResult, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("插件名不能为空")
	}

	// kind 先读（cli.Uninstall 成功后台账行即删）——回执的 effect 按它派生
	ledger, err := s.repo.FindByName(ctx, name)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	env, err := pluginCli.NewSourceEnv()
	if err != nil || env == nil || env.Root == "" {
		return nil, errors.New("仓库根不可用：只支持源码检出内运行或配置 plugin.root")
	}

	var report bytes.Buffer
	runEnv := *env
	runEnv.Out = &report
	if err := pluginCli.Uninstall(ctx, &runEnv, name, uninstallOptions(params, operator)); err != nil {
		// 稳定错误码（lifecycle_forbidden 等）原样透传，由 handler 映射 status/code；
		// 其余错误的文案可能带绝对路径（落位目标/仓库根）——页面提示与审计同一脱敏口径（同 install 侧）。
		var catalogErr *pluginCli.CatalogError
		if errors.As(err, &catalogErr) {
			return nil, err
		}
		return nil, errors.New(pluginCli.SanitizePluginText(env, err.Error()))
	}

	return &UninstallResult{
		Name:      name,
		Purge:     params.Purge,
		KeepFiles: params.KeepFiles,
		Effect:    uninstallEffect(ledger.Kind),
		Report:    pluginCli.SanitizePluginText(env, report.String()),
	}, nil
}

// uninstallOptions 参数 → CLI 旗标映射（**唯一映射点**，单测钉住）。Purge 与 Yes 强制成对：
// HTTP 请求本身即显式动作，但「--purge 必须二次确认」的 CLI 门禁不被绕过——界面侧以卸载对话框的
// 「彻底删除」勾选承担确认；adopt 装入（adopted_through>0）的插件 purge 由 cli 默认拒绝
// （三重显式 --purge-adopted 只走 CLI，界面不提供）。
func uninstallOptions(params UninstallParams, operator string) pluginCli.UninstallOptions {
	return pluginCli.UninstallOptions{
		Purge:     params.Purge,
		Yes:       params.Purge,
		KeepFiles: params.KeepFiles,
		Force:     params.Force,
		Operator:  operator,
	}
}

// uninstallEffect 卸载回执的生效方式码：声明式无 Go 注册行 ⇒ 台账闸即时生效；code/mixed 需重编译
func uninstallEffect(kind string) string {
	if kind == "declarative" {
		return EffectGateTTL
	}
	return EffectRebuildRequired
}

// runtime 汇总三处真相（进程内注册表 / 注册文件 / 台账），并算出接线悬空项
func (s *Service) runtime(ctx context.Context) (*Runtime, error) {
	rt := &Runtime{Wired: pluginCli.WiredNames(), Registered: []string{}, Orphans: []string{}}
	if rt.Wired == nil {
		rt.Wired = []string{}
	}

	root, err := s.repoRoot()
	if err == nil {
		names, registeredErr := pluginCli.RegisteredPlugins(root)
		if registeredErr != nil {
			// 文件在但读不动（权限/IO）：按「不可读」处理——各行 Registered 保持 nil，
			// 不把读失败谎报成「缺注册行」（差异必须显式，不能靠猜）
			log.Printf("[plugin] 读取注册文件 %s 失败，注册行面降级为不可读: %v", pluginCli.RegistryFile, registeredErr)
		} else {
			rt.RegistryAvailable = true
			rt.Registered = names
		}
	}

	names, err := s.repo.ListNames(ctx)
	if err != nil {
		return nil, err
	}
	ledger := make(map[string]struct{}, len(names))
	for _, name := range names {
		ledger[name] = struct{}{}
	}
	for _, name := range rt.Wired {
		if _, ok := ledger[name]; !ok {
			rt.Orphans = append(rt.Orphans, name)
		}
	}
	return rt, nil
}

// buildRow 单行的运行时标记（drift 逐条对齐 cli.driftNotes 的台账侧分支）
func buildRow(row *model.Plugin, entities int, runtime *Runtime) Row {
	out := Row{Plugin: *row, Entities: entities, Wired: inList(runtime.Wired, row.Name), Drift: []string{}}
	if runtime.RegistryAvailable {
		registered := inList(runtime.Registered, row.Name)
		out.Registered = &registered
	}

	if !out.Wired && row.Kind != "declarative" {
		out.Drift = append(out.Drift, DriftUnwired)
	}
	if out.Registered != nil && !*out.Registered && row.Kind != "declarative" {
		out.Drift = append(out.Drift, DriftMissingRegister)
	}
	if row.Kind == "declarative" && out.Registered != nil && *out.Registered {
		out.Drift = append(out.Drift, DriftDeclarativeRegister)
	}
	if row.Status == host.StatusFailed {
		out.Drift = append(out.Drift, DriftFailed)
	}
	return out
}

// fileList 落位文件清单（best-effort：读不到源目录就让 Available=false 由前端提示，不报错）
func (s *Service) fileList(name string) Files {
	env := s.cliEnv()
	if env.Root == "" {
		return Files{List: []FileRow{}}
	}
	rel := pluginCli.PluginDir(name)
	if _, err := os.Stat(env.Abs(rel)); err != nil {
		return Files{List: []FileRow{}}
	}

	plans, err := pluginCli.BuildPlanFiles(env, env.Abs(rel), name)
	if err != nil {
		log.Printf("[plugin] 读取插件 %s 的源文件清单失败（文件面降级）: %v", name, err)
		return Files{List: []FileRow{}}
	}
	list := make([]FileRow, 0, len(plans))
	for i := range plans {
		list = append(list, FileRow{
			Rel:      plans[i].Rel,
			Target:   plans[i].Target,
			Bytes:    plans[i].Bytes,
			SHA256:   plans[i].SHA256,
			Conflict: plans[i].Conflict,
		})
	}
	return Files{Available: true, Source: rel, List: list}
}

// isWired 插件是否已接线到本进程（编译产物里的注册行）
func (s *Service) isWired(name string) bool {
	_, ok := host.Lookup(name)
	return ok
}

// repoRoot 仓库根（注册文件与插件源目录所在；定位一次并缓存）。
// 与 cliEnv()/环境判定同口径走 NewEnv→resolveSourceRoot（可选键 plugin.root 优先，否则向上找 go.mod）：
// 曾在「非源码检出 + plugin.root 指向检出」这种部署下与 fileList 分叉——注册行面报「不可读」而
// 文件面可用，页面给出的原因（不在源码检出内运行）与事实不符。
func (s *Service) repoRoot() (string, error) {
	s.rootOnce.Do(func() {
		env, err := pluginCli.NewEnv()
		if err != nil {
			s.rootErr = err
			return
		}
		s.root = env.Root
	})
	return s.root, s.rootErr
}

// cliEnv CLI 运行环境（懒建一次）。
// 优先用 cli.NewEnv()（含仓库根，文件面可用）；**不在源码检出内运行**的项目根定位失败时，
// 退化为最小 Env——启停路径（cli.setPluginStatus）只用到 EnsureDB 与 Printf，不读文件系统，
// 因此退化不影响 enable/disable；此时 fileList 与注册文件面按「不可用」如实降级。
func (s *Service) cliEnv() *pluginCli.Env {
	s.envOnce.Do(func() {
		if env, err := pluginCli.NewEnv(); err == nil {
			s.env = env
			return
		}
		s.env = &pluginCli.Env{
			Prefix:      config.Get().Database.Prefix,
			HostVersion: version.Get().Version,
		}
	})
	return s.env
}

// inList 名字是否在清单内（清单为个位到十位量级，线性查找即可）
func inList(names []string, name string) bool {
	for _, item := range names {
		if item == name {
			return true
		}
	}
	return false
}
