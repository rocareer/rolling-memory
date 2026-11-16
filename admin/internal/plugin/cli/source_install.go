package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/ai-go-hub/ai-go-admin/internal/model"
	pluginSource "github.com/ai-go-hub/ai-go-admin/internal/plugin/source"

	"gorm.io/gorm"
)

// NewSourceEnv 构造来源/清单通道的命令环境。
//
// 历史上它与 cli.NewEnv 的唯一差别是「优先读可选配置键 plugin.root」；根定位统一到 resolveSourceRoot
// 之后两者已逐字相同（NewEnv 也读 plugin.root），本函数保留为通道语义的别名，避免内联调用点改名。
func NewSourceEnv() (*Env, error) {
	return NewEnv()
}

// configuredSourceRoot 准入判据（plugin.root）：非空、可转绝对路径、目录内有普通文件 go.mod。
//
// 判据只看「这个目录像不像一个 Go 模块」，**不校验模块身份**——理由与已知边界（实证过，别当承诺读）：
//   - 把插件装进另一个仓是该键的正当用法之一（本机就有一批不同名的检出）；
//   - 但**同名模块的另一份检出不会被区分**（本机就有 5 个检出都是本项目模块名）⇒ 那类错配没有提示；
//   - 异模块名会触发归属提示（cli.go 的 rootOwnerNoticeText，走 stderr），可编译与否取决于目标仓
//     自己是否提供本模块依赖：落位写出的注册行固定是 ModulePath 下的导入路径，本工具不写 go.mod、
//     也不补 require/replace。
//   - 判据不认时（路径不存在 / 目录无 go.mod / go.mod 是目录）resolveSourceRoot 会静默回退 FindRoot，
//     没有额外提示——「配了但没生效」这一点目前靠命令输出里的实际路径自查。
func configuredSourceRoot(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", false
	}
	info, err := os.Stat(filepath.Join(abs, "go.mod"))
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return abs, true
}

// moduleLine 读一个源码根的 go.mod 的 module 行（读不到返回空串）。
// 按空白切词而不是匹配 "module " 前缀：go.mod 允许 `module\t名` 这种写法（Go 本身接受）。
// 两侧引号要剥掉：`module "path"` 也是合法写法（`go mod edit -json` 会给出去引号的 Path）。
func moduleLine(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "module" {
			return strings.Trim(fields[1], `"`)
		}
	}
	return ""
}

// CatalogError 清单通道的稳定错误（handler/CLI 按 Code 与 Status 映射，不解析错误字符串）。
type CatalogError struct {
	// Code 稳定的 error_code（HTTP data.error_code / CLI JSON）
	Code string
	// Status HTTP 语义码（传输层仍为 200，见 httpx.Fail 的既有语义）
	Status int
	// Message 可展示的中文消息（已脱敏）
	Message string
	// Err 原始错误（用于 errors.Is/日志）
	Err error
}

func (e *CatalogError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.Code
}

func (e *CatalogError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func catalogError(code string, status int, format string, args ...any) *CatalogError {
	return &CatalogError{Code: code, Status: status, Message: fmt.Sprintf(format, args...)}
}

func catalogErrorFrom(code string, status int, err error) *CatalogError {
	return &CatalogError{Code: code, Status: status, Message: err.Error(), Err: err}
}

// 预检期的 typed error：调用方与 handler 用 errors.As 稳定映射，不解析错误字符串。
// 每个类型 Unwrap 到 *CatalogError，handler 的既有 errors.As(err, *CatalogError) 分支即可复用。
type (
	// RuleNameCollisionError 规则名与既有 admin_rules 相撞
	RuleNameCollisionError struct {
		Names []string
		cause *CatalogError
	}
	// RulePathCollisionError 规则 path 与既有 admin_rules 相撞
	RulePathCollisionError struct {
		Paths []string
		cause *CatalogError
	}
	// EntityTableExistsError L2 引擎自管实体表已存在
	EntityTableExistsError struct {
		Tables []string
		cause  *CatalogError
	}
	// ExistingTargetsError 目标路径已存在（catalog 固定不覆盖，同内容也拒）
	ExistingTargetsError struct {
		Targets []string
		cause   *CatalogError
	}
)

func (e *RuleNameCollisionError) Error() string { return e.cause.Error() }
func (e *RuleNameCollisionError) Unwrap() error { return e.cause }
func (e *RulePathCollisionError) Error() string { return e.cause.Error() }
func (e *RulePathCollisionError) Unwrap() error { return e.cause }
func (e *EntityTableExistsError) Error() string { return e.cause.Error() }
func (e *EntityTableExistsError) Unwrap() error { return e.cause }
func (e *ExistingTargetsError) Error() string   { return e.cause.Error() }
func (e *ExistingTargetsError) Unwrap() error   { return e.cause }

func newRuleNameCollisionError(names []string) *RuleNameCollisionError {
	return &RuleNameCollisionError{
		Names: names,
		cause: catalogError("rule_name_collision", 409, "规则名与既有 admin_rules 相撞: %s", strings.Join(names, ", ")),
	}
}

func newRulePathCollisionError(paths []string) *RulePathCollisionError {
	return &RulePathCollisionError{
		Paths: paths,
		cause: catalogError("rule_path_collision", 409, "规则 path 与既有 admin_rules 相撞: %s", strings.Join(paths, ", ")),
	}
}

func newEntityTableExistsError(tables []string) *EntityTableExistsError {
	return &EntityTableExistsError{
		Tables: tables,
		cause:  catalogError("entity_table_exists", 409, "L2 实体表已存在: %s", strings.Join(tables, ", ")),
	}
}

func newExistingTargetsError(targets []string) *ExistingTargetsError {
	return &ExistingTargetsError{
		Targets: targets,
		cause:   catalogError("existing_targets", 409, "目标路径已存在（同内容也拒绝覆盖）：%s", strings.Join(targets, ", ")),
	}
}

// CatalogPackageView 预演回执里的包摘要（不返回本机路径/临时目录）。
type CatalogPackageView struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Signed bool   `json:"signed"`
}

// CatalogPreflightResult 预演结果（只读；不写库/不落盘）。
type CatalogPreflightResult struct {
	OK                    bool                     `json:"ok"`
	Installable           bool                     `json:"installable"`
	Reason                string                   `json:"reason"`
	Report                string                   `json:"report"`
	Entry                 pluginSource.PluginEntry `json:"entry"`
	Package               CatalogPackageView       `json:"package"`
	CatalogSHA256         string                   `json:"catalog_sha256"`
	Conflicts             []string                 `json:"conflicts"`
	ExistingTargets       []string                 `json:"existing_targets"`
	SignaturePresent      bool                     `json:"signature_present"`
	TrustedKeysConfigured bool                     `json:"trusted_keys_configured"`
	TrustedKeysError      string                   `json:"trusted_keys_error"`
	SignatureVerified     bool                     `json:"signature_verified"`
	AllowUnsignedRequired bool                     `json:"allow_unsigned_required"`
	ErrorCode             string                   `json:"error_code,omitempty"`
}

// CatalogInstallRequest 从清单安装的请求（source 始终必填；没有 force/覆盖选项）。
type CatalogInstallRequest struct {
	SourceID      uint
	UID           string
	Version       string
	AllowUnsigned bool
	ConfirmSHA256 string
	Operator      string
}

// CatalogInstallResult 安装成功回执（effect 只认后端事实：需要重编译 + 重启）。
type CatalogInstallResult struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	Status        string `json:"status"`
	Effect        string `json:"effect"`
	Checksum      string `json:"checksum"`
	SignedBy      string `json:"signed_by"`
	Source        string `json:"source"`
	CatalogSHA256 string `json:"catalog_sha256"`
}

// catalogInstallMu 单进程串行化安装（v1 明确只保证单进程；多进程部署需 DB advisory lock）。
var catalogInstallMu sync.Mutex

// errCatalogDBUnavailable 只读冲突检查取不到库（与“查到冲突”区分：前者 503，后者 409/422）。
var errCatalogDBUnavailable = errors.New("catalog db unavailable")

// trustedKeysLoader 可注入（测试覆盖“公钥配置有误”分支，不依赖真实配置文件）。
var trustedKeysLoader = LoadTrustedKeys

// catalogResolved 一次取源/取清单/取条目的中间结果。
type catalogResolved struct {
	src   *pluginSource.Source
	doc   *pluginSource.CatalogDocument
	entry *pluginSource.PluginEntry
	pkg   *pluginSource.PackageSource
	meta  *pluginSource.PackageMeta
}

// resolveCatalogEntry 读取来源与清单并校验快照摘要（catalog_stale/catalog_not_synced 在此拦截）。
// 只做取数与结构校验，不取包——安装路径可先用台账拒绝已安装插件，避免无谓下载。
func resolveCatalogEntry(ctx context.Context, env *Env, store pluginSource.Store, sourceID uint, uid, version string) (*catalogResolved, error) {
	if env == nil {
		return nil, catalogError("repo_root_unavailable", 503, "仓库根不可用：只支持源码检出内运行或配置 plugin.root")
	}
	if store == nil {
		return nil, catalogError("repo_root_unavailable", 503, "来源存储不可用")
	}
	src, err := store.GetSource(ctx, sourceID)
	if err != nil {
		if errors.Is(err, pluginSource.ErrNotFound) {
			return nil, catalogError("source_not_found", 404, "来源不存在")
		}
		return nil, catalogError("repo_root_unavailable", 503, "读取来源失败: %v", err)
	}
	if src.Enabled != 1 {
		return nil, catalogError("source_disabled", 409, "来源 %s 已停用", src.Name)
	}
	doc, err := pluginSource.LoadCatalog(ctx, src, env.Root)
	if err != nil {
		return nil, sanitizeCatalogError(mapCatalogSourceError(err), env, src, nil)
	}
	if src.LastCatalogSHA256 == "" {
		return nil, catalogError("catalog_not_synced", 409, "来源 %s 尚未同步清单，请先执行 catalog sync", src.Name)
	}
	if src.LastCatalogSHA256 != doc.SHA256 {
		return nil, catalogError("catalog_stale", 409, "来源 %s 的清单已变化（快照 %s，当前 %s），请先重新同步",
			src.Name, src.LastCatalogSHA256, doc.SHA256)
	}
	entry, ok := doc.Catalog.Entry(uid, version)
	if !ok {
		return nil, catalogError("entry_not_found", 404, "清单中没有 %s@%s", uid, version)
	}
	return &catalogResolved{src: src, doc: doc, entry: entry}, nil
}

// loadCatalogPackage 取包并做包内预检（大小/摘要/元数据/签名声明/files.sha256）。
func loadCatalogPackage(ctx context.Context, env *Env, resolved *catalogResolved) error {
	pkg, err := pluginSource.ResolvePackage(ctx, resolved.src, resolved.doc, resolved.entry)
	if err != nil {
		return sanitizeCatalogError(mapCatalogSourceError(err), env, resolved.src, nil)
	}
	meta, err := pluginSource.InspectPackage(pkg.Path, resolved.entry)
	if err != nil {
		sanitized := sanitizeCatalogError(mapCatalogSourceError(err), env, resolved.src, pkg)
		pkg.Cleanup()
		return sanitized
	}
	resolved.pkg = pkg
	resolved.meta = meta
	return nil
}

// resolveCatalog 预演路径：取清单 + 取包 + 包内预检。
func resolveCatalog(ctx context.Context, env *Env, store pluginSource.Store, sourceID uint, uid, version string) (*catalogResolved, error) {
	resolved, err := resolveCatalogEntry(ctx, env, store, sourceID, uid, version)
	if err != nil {
		return nil, err
	}
	if err := loadCatalogPackage(ctx, env, resolved); err != nil {
		return nil, err
	}
	return resolved, nil
}

// installCatalogZip 与 Install 同一条管线，唯一差别：来源标签用 catalog:<source>:<uid>@<version>，
// 而不是 zip 临时文件名。落位/台账/接线仍由 runInstall 完成（未重写安装链）。
//
// 之所以需要这个入口：Install 内部用 resolveInstallSource 从 zip 路径推导 plan.SourceRef，
// install_run.go 的四处台账/审计都写 plan.SourceRef。本函数在包内（package cli）直接
// StageZip + BuildInstallPlanFromSource，把 Ref 固定为方案要求的标签，再交给 runInstall。
func installCatalogZip(ctx context.Context, env *Env, zipPath, sourceLabel string, opts InstallOptions) error {
	staged, err := StageZip(zipPath)
	if err != nil {
		return err
	}
	defer staged.Cleanup()
	env.Printf("包源：%s（整包 sha256 %s，%d 字节）→ 解包 %d 个条目 / %d 字节\n",
		sourceLabel, shortSHA(staged.Report.Checksum), staged.Report.Bytes, staged.Report.Entries, staged.Report.ExtractedBytes)

	plan, err := BuildInstallPlanFromSource(env, PlanSource{
		Dir:      staged.Dir,
		Desc:     sourceLabel,
		Ref:      sourceLabel,
		FromZip:  true,
		Checksum: staged.Report.Checksum,
	}, opts.Enable)
	if err != nil {
		return err
	}
	integrity, err := checkSourceIntegrity(ctx, env, plan, integrityOptions{
		RequirePackManifest: true,
		EnforceSignPolicy:   true,
		AllowUnsigned:       opts.AllowUnsigned,
	})
	if err != nil {
		return err
	}

	ddlReads, ddlErr := previewEntityDDL(ctx, env, plan)
	plan.DDL = ddlReads
	baseline, baselineErr := previewBaseline(ctx, env, plan)
	plan.Baseline = baseline
	plan.Render(env.Writer())

	if ddlErr != nil {
		return fmt.Errorf("L2 实体结构预检未通过（预演已列出逐条 DDL 与拒绝理由；未落盘、未写库）: %w", ddlErr)
	}
	if baselineErr != nil {
		return fmt.Errorf("迁移基线（adopt）预检未通过（预演 ③b 已列出逐条探针与判定；未落盘、未写库）: %w", baselineErr)
	}
	if !opts.Force {
		conflicts := make([]string, 0)
		for _, file := range plan.Files {
			if strings.Contains(file.Conflict, "内容不同") {
				conflicts = append(conflicts, file.Target)
			}
		}
		if len(conflicts) > 0 {
			return fmt.Errorf("与工作树同名但内容不同的文件 %d 个（catalog 通道固定不覆盖；先人工处理）:\n  %s",
				len(conflicts), strings.Join(conflicts, "\n  "))
		}
	}
	for _, migration := range plan.Migrations {
		if len(migration.Banned) > 0 {
			return fmt.Errorf("插件迁移 %s 含禁词（%s）：整文件隐式事务内必报错（docs/sql-discipline.md）",
				migration.Path, strings.Join(migration.Banned, ", "))
		}
	}
	if opts.DryRun {
		env.Printf("\n（--dry-run：以上为全量预演，未落盘、未写库、未改接线文件；「A 的每一步都要能在 dry-run 中预演」）\n")
		return nil
	}
	env.Printf("\n执行安装编排（④–⑧；任一失败即补偿回滚，补偿失败则保留现场、状态留 failed）\n")
	return runInstall(ctx, env, plan, integrity, opts)
}

// PreflightCatalog 非破坏预演：复用 cli.Install --dry-run 的全量管线，另做只读冲突检查。
func PreflightCatalog(ctx context.Context, env *Env, store pluginSource.Store, sourceID uint, uid, version, operator string) (*CatalogPreflightResult, error) {
	resolved, err := resolveCatalog(ctx, env, store, sourceID, uid, version)
	if err != nil {
		return nil, err
	}
	defer resolved.pkg.Cleanup()

	result := &CatalogPreflightResult{
		Entry:         *resolved.entry,
		Package:       CatalogPackageView{Size: resolved.pkg.Size, SHA256: resolved.pkg.SHA256, Signed: resolved.entry.Package.Signed != nil && *resolved.entry.Package.Signed},
		CatalogSHA256: resolved.doc.SHA256,
		Conflicts:     []string{},
	}
	keys, keyErr := trustedKeysLoader()
	result.TrustedKeysConfigured = keyErr == nil && len(keys) > 0
	if keyErr != nil {
		result.TrustedKeysError = keyErr.Error()
	}
	result.SignaturePresent = resolved.meta.SignaturePresent

	installed, err := store.InstalledVersions(ctx, []string{uid})
	if err != nil {
		return nil, catalogError("repo_root_unavailable", 503, "读取插件台账失败: %v", err)
	}
	row, wasInstalled := installed[uid]

	var targets []string
	var ruleNames, rulePaths, entityTables []string
	if !wasInstalled {
		stage, stageErr := StageZip(resolved.pkg.Path)
		if stageErr != nil {
			return nil, sanitizeCatalogError(catalogErrorFrom("install_failed", 422, stageErr), env, resolved.src, resolved.pkg)
		}
		defer stage.Cleanup()
		plan, planErr := BuildInstallPlanFromSource(env, PlanSource{
			Dir:      stage.Dir,
			Desc:     catalogSourceLabel(resolved.src.Name, uid, version),
			Ref:      catalogSourceLabel(resolved.src.Name, uid, version),
			FromZip:  true,
			Checksum: resolved.pkg.SHA256,
		}, false)
		if planErr != nil {
			return nil, sanitizeCatalogError(catalogErrorFrom("install_failed", 422, planErr), env, resolved.src, resolved.pkg)
		}
		targets, err = catalogCheckTargets(env, plan)
		if err != nil {
			return nil, sanitizeCatalogError(catalogErrorFrom("existing_targets", 409, err), env, resolved.src, resolved.pkg)
		}
		ruleNames, rulePaths, err = catalogCheckRuleCollisions(ctx, env, plan)
		if err != nil {
			return nil, mapCatalogDBError(err)
		}
		entityTables, err = catalogCheckEntityTables(ctx, env, plan)
		if err != nil {
			return nil, mapCatalogDBError(err)
		}
	}

	var report bytes.Buffer
	previewEnv := *env
	previewEnv.Out = &report
	installErr := installCatalogZip(ctx, &previewEnv, resolved.pkg.Path,
		catalogSourceLabel(resolved.src.Name, uid, version), InstallOptions{
			DryRun:        true,
			AllowUnsigned: true, // 预演只为拿到报告；无效签名仍会被拒（signature.go 的既有口径）
			Force:         false,
			Operator:      operator,
		})
	result.Report = sanitizeCatalogText(report.String(), env, resolved.src, resolved.pkg)
	result.SignatureVerified = resolved.meta.SignaturePresent && result.TrustedKeysConfigured && installErr == nil

	result.Conflicts = append(result.Conflicts, ruleNamesAndPaths(ruleNames, rulePaths)...)
	result.Conflicts = append(result.Conflicts, entityTables...)
	result.ExistingTargets = targets
	result.Conflicts = append(result.Conflicts, targets...)

	switch {
	case wasInstalled && row.Status == "failed":
		result.Reason = "plugin_failed"
	case wasInstalled:
		result.Reason = "already_installed"
	case len(ruleNames) > 0:
		result.Reason = "rule_name_collision"
		result.ErrorCode = "rule_name_collision"
	case len(rulePaths) > 0:
		result.Reason = "rule_path_collision"
		result.ErrorCode = "rule_path_collision"
	case len(entityTables) > 0:
		result.Reason = "entity_table_exists"
		result.ErrorCode = "entity_table_exists"
	case len(targets) > 0:
		result.Reason = "existing_targets"
		result.ErrorCode = "existing_targets"
	case installErr != nil:
		result.Reason = "install_failed"
		result.ErrorCode = "install_failed"
	}
	// 固定优先级：already_installed/plugin_failed 高于 trusted_keys_invalid。
	if keyErr != nil && result.Reason == "" {
		result.Reason = "trusted_keys_invalid"
		result.ErrorCode = "trusted_keys_invalid"
	}
	if installErr != nil && result.ErrorCode == "" && result.Reason == "" {
		result.Reason = "install_failed"
		result.ErrorCode = "install_failed"
		if resolved.meta.SignaturePresent && result.TrustedKeysConfigured {
			result.Reason = "signature_invalid"
			result.ErrorCode = "signature_invalid"
		}
	}
	result.OK = installErr == nil && keyErr == nil
	result.AllowUnsignedRequired = !result.SignatureVerified && keyErr == nil
	result.Installable = !wasInstalled && result.Reason == "" && installErr == nil && keyErr == nil
	return result, nil
}

// InstallCatalog 从清单安装（复用既有 cli.Install；不支持升级/重装，不提供 force）。
func InstallCatalog(ctx context.Context, env *Env, store pluginSource.Store, req CatalogInstallRequest) (*CatalogInstallResult, error) {
	catalogInstallMu.Lock()
	defer catalogInstallMu.Unlock()

	// 环境门禁先于取包：生产环境（plugin.env=prod，或未显式配置且没有源码检出）连下载/暂存都不做。
	// 错误码 lifecycle_forbidden（HTTP 403）由 handler 的 writeCatalogError 直接映射；
	// 写路径另有 runInstall 的同款门禁兜底（纵深防御，本函数只负责「快速拒绝」）。
	if err := RequireLifecycle("安装"); err != nil {
		return nil, err
	}

	// 下载前检查：来源/清单快照/条目 + confirm_sha256 + 台账；已安装/升级/降级不下载包。
	resolved, err := resolveCatalogEntry(ctx, env, store, req.SourceID, req.UID, req.Version)
	if err != nil {
		return nil, err
	}
	if req.ConfirmSHA256 == "" || !strings.EqualFold(req.ConfirmSHA256, resolved.entry.Package.SHA256) {
		return nil, catalogError("package_sha_mismatch", 409,
			"confirm_sha256 与当前清单包摘要不一致（清单 %s，确认 %s）；请重新预演", resolved.entry.Package.SHA256, req.ConfirmSHA256)
	}
	installed, err := store.InstalledVersions(ctx, []string{req.UID})
	if err != nil {
		return nil, catalogError("repo_root_unavailable", 503, "读取插件台账失败: %v", err)
	}
	if row, ok := installed[req.UID]; ok {
		switch {
		case row.Status == "failed":
			return nil, catalogError("plugin_failed", 409, "插件 %s 处于 failed 半装状态；先 `aigo plugin uninstall --force %s` 清理", req.UID, req.UID)
		case row.Version == req.Version:
			return nil, catalogError("already_installed", 409, "插件 %s v%s 已安装（v1 不支持重装）", req.UID, req.Version)
		default:
			cmp, cmpErr := compareVersion(row.Version, req.Version)
			if cmpErr != nil {
				return nil, catalogError("upgrade_not_supported", 409, "已安装版本 %s，目标 %s：v1 不支持升级/降级", row.Version, req.Version)
			}
			if cmp > 0 {
				return nil, catalogError("downgrade_not_supported", 409, "已安装 v%s，目标 v%s 为降级：拒绝", row.Version, req.Version)
			}
			return nil, catalogError("upgrade_not_supported", 409, "已安装 v%s，目标 v%s：v1 不支持升级（先卸载再安装或另立升级设计）", row.Version, req.Version)
		}
	}

	// 公钥配置错误在读包前就拒绝（不下载、不暂存）
	keys, keyErr := trustedKeysLoader()
	if keyErr != nil {
		return nil, catalogError("trusted_keys_invalid", 422, "受信公钥配置有误: %v", keyErr)
	}

	// 取包 + 包内预检（大小/摘要/元数据/签名声明）
	if err := loadCatalogPackage(ctx, env, resolved); err != nil {
		return nil, err
	}
	defer resolved.pkg.Cleanup()

	trustedConfigured := len(keys) > 0
	signatureVerified := resolved.meta.SignaturePresent && trustedConfigured
	allowUnsignedRequired := !signatureVerified
	if allowUnsignedRequired && !req.AllowUnsigned {
		return nil, catalogError("unsigned_not_allowed", 409,
			"包未签名或本机无法验证（signature_present=%v，trusted_keys_configured=%v）；确认后以 allow_unsigned=true 重试",
			resolved.meta.SignaturePresent, trustedConfigured)
	}

	// 安装前只读冲突检查（与预演同口径；预演后到安装前仍有竞态，安装链的写库会再次兜底）
	stage, err := StageZip(resolved.pkg.Path)
	if err != nil {
		return nil, sanitizeCatalogError(catalogErrorFrom("install_failed", 422, err), env, resolved.src, resolved.pkg)
	}
	defer stage.Cleanup()
	plan, err := BuildInstallPlanFromSource(env, PlanSource{
		Dir:      stage.Dir,
		Desc:     catalogSourceLabel(resolved.src.Name, req.UID, req.Version),
		Ref:      catalogSourceLabel(resolved.src.Name, req.UID, req.Version),
		FromZip:  true,
		Checksum: resolved.pkg.SHA256,
	}, false)
	if err != nil {
		return nil, sanitizeCatalogError(catalogErrorFrom("install_failed", 422, err), env, resolved.src, resolved.pkg)
	}
	targets, err := catalogCheckTargets(env, plan)
	if err != nil {
		return nil, sanitizeCatalogError(catalogErrorFrom("existing_targets", 409, err), env, resolved.src, resolved.pkg)
	}
	if len(targets) > 0 {
		return nil, newExistingTargetsError(targets)
	}
	ruleNames, rulePaths, err := catalogCheckRuleCollisions(ctx, env, plan)
	if err != nil {
		return nil, mapCatalogDBError(err)
	}
	if len(ruleNames) > 0 {
		return nil, newRuleNameCollisionError(ruleNames)
	}
	if len(rulePaths) > 0 {
		return nil, newRulePathCollisionError(rulePaths)
	}
	entityTables, err := catalogCheckEntityTables(ctx, env, plan)
	if err != nil {
		return nil, mapCatalogDBError(err)
	}
	if len(entityTables) > 0 {
		return nil, newEntityTableExistsError(entityTables)
	}

	// 签名预检（typed 映射；不依赖 Install 的中文错误串）：无效签名与“未签名未确认”在此分型。
	packManifest, readErr := os.ReadFile(filepath.Join(stage.Dir, PackManifestFile))
	if readErr != nil {
		return nil, sanitizeCatalogError(catalogErrorFrom("manifest_mismatch", 422, readErr), env, resolved.src, resolved.pkg)
	}
	if _, sigErr := checkSignaturePolicy(env, stage.Dir, resolved.meta.SignatureFile, packManifest, keys, integrityOptions{
		RequirePackManifest: true,
		EnforceSignPolicy:   true,
		AllowUnsigned:       req.AllowUnsigned,
	}); sigErr != nil {
		code, status := "install_failed", 422
		if resolved.meta.SignaturePresent && trustedConfigured {
			code = "signature_invalid"
		}
		return nil, sanitizeCatalogError(catalogErrorFrom(code, status, sigErr), env, resolved.src, resolved.pkg)
	}

	if err := installCatalogZip(ctx, env, resolved.pkg.Path,
		catalogSourceLabel(resolved.src.Name, req.UID, req.Version), InstallOptions{
			DryRun:        false,
			Enable:        false,
			Force:         false,
			AllowUnsigned: req.AllowUnsigned,
			Operator:      req.Operator,
			// 审计 detail.error 与下面返回前端/CLI 的 message 用**同一个**脱敏函数（第 7 条）：
			// 失败原因会带临时 zip、staging 解包目录与落位目标的绝对路径。
			Sanitize: func(text string) string {
				return sanitizeCatalogText(text, env, resolved.src, resolved.pkg)
			},
		}); err != nil {
		return nil, sanitizeCatalogError(catalogErrorFrom("install_failed", 500, err), env, resolved.src, resolved.pkg)
	}

	after, err := store.InstalledVersions(ctx, []string{req.UID})
	if err != nil {
		return nil, catalogError("install_failed", 500, "安装已完成但读取台账失败（先刷新插件列表对账）: %v", err)
	}
	row, ok := after[req.UID]
	if !ok {
		return nil, catalogError("install_failed", 500, "安装命令返回成功但台账没有 %s（用 `aigo plugin list` 对账）", req.UID)
	}
	return &CatalogInstallResult{
		Name:          req.UID,
		Version:       req.Version,
		Status:        row.Status,
		Effect:        "rebuild_required",
		Checksum:      row.Checksum,
		SignedBy:      row.SignedBy,
		Source:        row.Source,
		CatalogSHA256: resolved.doc.SHA256,
	}, nil
}

// catalogSourceLabel 台账/审计/展示的统一来源标签（方案定死的格式）。
func catalogSourceLabel(srcName, uid, version string) string {
	return fmt.Sprintf("catalog:%s:%s@%s", srcName, uid, version)
}

func catalogCheckTargets(env *Env, plan *InstallPlan) ([]string, error) {
	seen := make(map[string]struct{}, len(plan.Files))
	targets := make([]string, 0, len(plan.Files))
	for _, file := range plan.Files {
		if err := ensureNoSymlink(env.Root, file.Target); err != nil {
			return nil, err
		}
		if _, err := os.Lstat(env.Abs(file.Target)); err == nil {
			if _, ok := seen[file.Target]; !ok {
				seen[file.Target] = struct{}{}
				targets = append(targets, file.Target)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("检查目标 %s 失败: %w", file.Target, err)
		}
	}
	sort.Strings(targets)
	return targets, nil
}

func catalogCheckRuleCollisions(ctx context.Context, env *Env, plan *InstallPlan) ([]string, []string, error) {
	if len(plan.Menus) == 0 {
		return nil, nil, nil
	}
	db, err := env.EnsureDB(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", errCatalogDBUnavailable, err)
	}
	names := make([]string, 0, len(plan.Menus))
	paths := make([]string, 0, len(plan.Menus))
	seenPath := make(map[string]struct{}, len(plan.Menus))
	for _, menu := range plan.Menus {
		names = append(names, menu.Name)
		if strings.TrimSpace(menu.Path) != "" {
			if _, ok := seenPath[menu.Path]; !ok {
				seenPath[menu.Path] = struct{}{}
				paths = append(paths, menu.Path)
			}
		}
	}
	// 规则名查重复用安装链的 checkRuleNameCollision（同一份口径）；命中后再取名单做 typed error。
	var nameHits []string
	if collisionErr := checkRuleNameCollision(ctx, db, plan); collisionErr != nil {
		rows, queryErr := gorm.G[model.AdminRule](db).Select("name").Where("name IN ?", names).Find(ctx)
		if queryErr != nil {
			return nil, nil, fmt.Errorf("%w: %v", errCatalogDBUnavailable, queryErr)
		}
		for i := range rows {
			nameHits = append(nameHits, rows[i].Name)
		}
		if len(nameHits) == 0 {
			return nil, nil, fmt.Errorf("%w: %v", errCatalogDBUnavailable, collisionErr)
		}
	}
	var pathHits []string
	if len(paths) > 0 {
		pathRows, err := gorm.G[model.AdminRule](db).Select("path").Where("path IN ?", paths).Find(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %v", errCatalogDBUnavailable, err)
		}
		for i := range pathRows {
			if pathRows[i].Path != nil && *pathRows[i].Path != "" {
				pathHits = append(pathHits, *pathRows[i].Path)
			}
		}
	}
	sort.Strings(nameHits)
	sort.Strings(pathHits)
	return nameHits, pathHits, nil
}

func catalogCheckEntityTables(ctx context.Context, env *Env, plan *InstallPlan) ([]string, error) {
	// 引擎自管实体需要读库做结构自省；显式区分“库不可用”（503）与“计划被拒”（422）。
	if engineOwnedCount(plan.Manifest.Entities) > 0 {
		if _, err := env.EnsureDB(ctx); err != nil {
			return nil, fmt.Errorf("%w: %v", errCatalogDBUnavailable, err)
		}
	}
	reads, err := previewEntityDDL(ctx, env, plan)
	plan.DDL = reads
	if err != nil {
		return nil, catalogErrorFrom("install_failed", 422, err)
	}
	tables := make([]string, 0, len(reads))
	for _, read := range reads {
		if read.DDLOwner == "engine" && read.Existing && read.Table != "" {
			tables = append(tables, read.Table)
		}
	}
	sort.Strings(tables)
	return tables, nil
}

func ruleNamesAndPaths(names, paths []string) []string {
	out := make([]string, 0, len(names)+len(paths))
	for _, name := range names {
		out = append(out, "rule_name:"+name)
	}
	for _, path := range paths {
		out = append(out, "rule_path:"+path)
	}
	return out
}

func compareVersion(a, b string) (int, error) {
	amaj, amin, apat, err := pluginSource.ParseVersion(a)
	if err != nil {
		return 0, err
	}
	bmaj, bmin, bpat, err := pluginSource.ParseVersion(b)
	if err != nil {
		return 0, err
	}
	switch {
	case amaj != bmaj:
		return amaj - bmaj, nil
	case amin != bmin:
		return amin - bmin, nil
	default:
		return apat - bpat, nil
	}
}

func mapCatalogSourceError(err error) *CatalogError {
	switch {
	case errors.Is(err, pluginSource.ErrInvalidInput):
		return catalogErrorFrom("catalog_invalid", 422, err)
	case errors.Is(err, pluginSource.ErrPathEscape):
		return catalogErrorFrom("catalog_invalid", 422, err)
	case errors.Is(err, pluginSource.ErrCatalogNotFound):
		return catalogErrorFrom("catalog_invalid", 422, err)
	case errors.Is(err, pluginSource.ErrPackageNotFound):
		return catalogErrorFrom("package_not_found", 502, err)
	case errors.Is(err, pluginSource.ErrPackageSizeMismatch):
		return catalogErrorFrom("package_size_mismatch", 422, err)
	case errors.Is(err, pluginSource.ErrPackageSHAMismatch):
		return catalogErrorFrom("package_sha_mismatch", 422, err)
	case errors.Is(err, pluginSource.ErrPackageSignedMismatch):
		return catalogErrorFrom("package_signed_mismatch", 422, err)
	case errors.Is(err, pluginSource.ErrManifestMismatch):
		return catalogErrorFrom("manifest_mismatch", 422, err)
	case errors.Is(err, pluginSource.ErrNetworkBlocked):
		return catalogErrorFrom("network_blocked", 502, err)
	case errors.Is(err, pluginSource.ErrUpstreamError):
		return catalogErrorFrom("upstream_error", 502, err)
	default:
		return catalogErrorFrom("catalog_invalid", 422, err)
	}
}

func mapCatalogDBError(err error) *CatalogError {
	var catalogErr *CatalogError
	if errors.As(err, &catalogErr) {
		return catalogErr
	}
	if errors.Is(err, errCatalogDBUnavailable) {
		return catalogErrorFrom("repo_root_unavailable", 503, err)
	}
	return catalogErrorFrom("install_failed", 422, err)
}

// SanitizePluginText 插件面共用的脱敏入口（**与 catalog 通道同一条实现**）：仓库根 → <repo>、
// 系统临时目录 → <tmp>、extra 里的绝对路径 → <source>（实现即 source.SanitizePaths，见下）。
//
// 供**不经 catalog 通道**的三个审计落库点使用（它们的 detail.error 会带 staging 临时目录与落位目标的
// 绝对路径，而该字符串要在插件管理页的动作审计区块展示，第 7 条）：
//   - `aigo plugin install`：cmd/plugin 把它塞进 InstallOptions.Sanitize（与 failureAuditDetail 同源），
//     并把命令行给的那个源路径当 extra（与 catalog 通道传 src.Location 同口径）；
//   - 启停写库失败：lifecycle.go 的失败审计；
//   - 卸载失败/被拒：uninstall_run.go 的失败审计。
func SanitizePluginText(env *Env, text string, extra ...string) string {
	root := ""
	if env != nil {
		root = env.Root
	}
	// 只认**绝对路径**的 extra：相对路径替换无意义，而 "/" 这类退化输入整串替换会把文本毁掉
	// （命令行给的是人打的参数，不能假定它一定是像样的路径）。
	safe := make([]string, 0, len(extra))
	for _, item := range extra {
		if item != "" && filepath.IsAbs(item) && strings.Trim(item, string(filepath.Separator)) != "" {
			safe = append(safe, item)
		}
	}
	return pluginSource.SanitizePaths(text, root, safe...)
}

// sanitizeCatalogText 统一走 source.SanitizePaths（与 source/test、last_sync_error 同一实现；
// 此处只额外替换 catalog 包临时路径为 <package>，不截断）。
func sanitizeCatalogText(text string, env *Env, src *pluginSource.Source, pkg *pluginSource.PackageSource) string {
	if pkg != nil {
		text = strings.ReplaceAll(text, pkg.Path, "<package>")
		if pkg.TempDir != "" {
			text = strings.ReplaceAll(text, pkg.TempDir, "<tmp>")
		}
	}
	root := ""
	if env != nil {
		root = env.Root
	}
	extra := make([]string, 0, 1)
	if src != nil {
		extra = append(extra, src.Location)
	}
	return pluginSource.SanitizePaths(text, root, extra...)
}

// sanitizeCatalogError 给错误消息做路径脱敏（来源 location 若是绝对路径也替换为 <source>）。
func sanitizeCatalogError(mapped *CatalogError, env *Env, src *pluginSource.Source, pkg *pluginSource.PackageSource) *CatalogError {
	if mapped == nil {
		return nil
	}
	mapped.Message = sanitizeCatalogText(mapped.Message, env, src, pkg)
	return mapped
}
