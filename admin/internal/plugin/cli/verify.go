package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"
)

// VerifyOptions verify 的旗标
type VerifyOptions struct {
	// AllowUnsigned 显式接受未验证的签名（包源未签名、或本机未登记受信公钥时必需）
	AllowUnsigned bool
	// Operator 审计署名（审计写入是尽力而为：verify 是本地通道，库不可用时只提示不阻断判定）
	Operator string
}

// Verify 校验插件源或插件包（install 首步 ①verify：结构 / manifest / host_api / 依赖 / 校验和 / 签名）：
//   - `<包>.zip` 走包通道：staging 解包（zip 防护）→ manifest → files.sha256 逐项比对 →
//     签名判定（签名对象 = files.sha256 的字节）→ requires 查台账；
//   - 目录源：manifest 闭集与命名规则、host_api 相容、长度预算（manifest 包）；迁移编号唯一且成对、
//     逐文件禁词（/）；落位映射可解释（web/ 下只允许 views/** 与 lang/<语言>.yaml）；
//     自带 files.sha256 时逐项比对（缺失 / 不一致 / 清单外夹带 ⇒ 拒）；
//   - requires 逐项查台账「已装且版本相容」（非空时查库，取不到台账即报错，不静默放行）。
//
// 返回 nil 表示全部通过；任一检查失败即返回错误（fail-closed）。
func Verify(ctx context.Context, env *Env, target string, opts VerifyOptions) error {
	target = strings.TrimSpace(target)
	if strings.HasSuffix(strings.ToLower(target), ".zip") {
		return verifyPackage(ctx, env, target, opts)
	}

	dir, dirErr := env.PluginSource(target)
	if dirErr != nil {
		return dirErr
	}

	m, warnings, err := manifest.Load(dir, manifest.Options{Prefix: env.Prefix, HostVersion: env.HostVersion})
	if err != nil {
		return fmt.Errorf("✗ manifest: %w", err)
	}
	env.Printf("校验插件源：%s\n", dir)
	env.Printf("✓ manifest：%s v%s（kind: %s；host_api %q 与宿主机制接口版本 %s 相容）\n",
		m.Name, m.Version, m.Kind, m.HostAPI, host.APIVersion)
	env.Printf("✓ 目录名 == name（%s）\n", PluginName(dir))

	//公式校验（F3）：实体页/权限节点的声明与公式不符 ⇒ 拒（安装期同一处校验，这里提前一次到 verify 面）
	menus, menuErr := BuildInstallMenus(m.Name, m)
	if menuErr != nil {
		return fmt.Errorf("✗ 菜单/节点公式: %w", menuErr)
	}
	env.Printf("✓ 菜单/节点：声明 %d 条 +公式代写 %d 条（实体页与权限节点）\n",
		len(m.Menus), len(menus)-len(m.Menus))
	for _, warning := range warnings {
		env.Printf("⚠ %s\n", warning)
	}

	migrationsDir := m.MigrationsDir
	if migrationsDir == "" {
		migrationsDir = "migrations"
	}
	migrations, err := ScanMigrations(filepath.Join(dir, filepath.FromSlash(migrationsDir)))
	if err != nil {
		return fmt.Errorf("✗ 迁移: %w", err)
	}
	if len(migrations) == 0 {
		env.Printf("✓ 迁移：无（未声明迁移目录或目录为空）\n")
	} else {
		env.Printf("✓ 迁移：%d 个文件（编号唯一且成对）\n", len(migrations))
		for _, migration := range migrations {
			env.Printf("    %s\n", migration.Describe())
		}
		for _, migration := range migrations {
			if len(migration.Banned) > 0 {
				return fmt.Errorf("✗ 迁移 %s 含禁词（%s）：整文件隐式事务内必报错（docs/sql-discipline.md）",
					migration.Path, strings.Join(migration.Banned, ", "))
			}
		}
	}

	// adopt 段（存量库迁移基线）与迁移文件对账：through = 最大编号、1..through 连续、探针对象名必须出现在
	// 该版本 up 文件里（防虚）。装前在这里先拦一次（安装期同一处校验，install 的计划构建会再走一遍）。
	if err := verifyAdopt(filepath.Join(dir, filepath.FromSlash(migrationsDir)), m.Adopt, migrations); err != nil {
		return fmt.Errorf("✗ adopt（迁移基线）: %w", err)
	}
	if m.Adopt != nil {
		env.Printf("✓ adopt（存量库迁移基线）：through=%d，%d 条探针（与迁移文件对账通过；存量库全命中 ⇒ 只写版本表、不执行旧 DDL）\n",
			m.Adopt.Through, len(m.Adopt.Probes))
	}

	files, err := BuildPlanFiles(env, dir, m.Name, m.Signature)
	if err != nil {
		return fmt.Errorf("✗ 落位映射: %w", err)
	}
	env.Printf("✓ 落位映射：%d 项（每个文件先落源树 plugins/%s/**；web/views/** 另落副本 %s/**、web/lang/<语言>.yaml 另落 web/src/lang/<语言>/plugin/%s.yaml）\n",
		len(files), m.Name, ViewTarget(m.Name), m.Name)

	// 依赖 + 完整性（同一实现：目录源自带 files.sha256 时逐项比对，清单缺失项 / 不一致 / 清单外夹带 ⇒ 拒；
	// 签名判定：有签名则按本机受信公钥（plugin.trusted_keys）验——有效即过并记指纹，本机无公钥时
	// 需 --allow-unsigned 显式接受未验证的签名；无效签名一律拒）。
	// 依赖非空才碰库（fail-closed——台账取不到就是错误，不静默放行）。
	plan := &InstallPlan{Dir: dir, Manifest: m}
	if _, err := checkSourceIntegrity(ctx, env, plan, integrityOptions{AllowUnsigned: opts.AllowUnsigned}); err != nil {
		return fmt.Errorf("✗ 依赖/完整性: %w", err)
	}
	if strings.TrimSpace(m.Requires) == "" {
		env.Printf("（requires 为空：无依赖校验）\n")
	}

	// 审计（尽力而为：verify 是本地通道，不因审计不可写而改变判定，但要如实提示）
	writeVerifyAudit(ctx, env, m.Name, opts.Operator, "success")
	env.Printf("\n校验通过：%s v%s\n", m.Name, m.Version)
	return nil
}

// verifyPackage 包通道校验（`aigo plugin verify <包.zip>`，「可被 aigo plugin verify 单独校验」）：
// staging 解包 → manifest → files.sha256 逐项比对 → 签名判定 → requires 查台账；全部通过才输出结论。
func verifyPackage(ctx context.Context, env *Env, zipPath string, opts VerifyOptions) error {
	env.Printf("校验插件包：%s\n", zipPath)
	report, err := VerifyPackage(ctx, env, zipPath, opts.AllowUnsigned)
	if err != nil {
		return fmt.Errorf("✗ 包校验: %w", err)
	}
	report.Render(env)

	writeVerifyAudit(ctx, env, report.Name, opts.Operator, "success")
	env.Printf("\n包校验通过：%s v%s（整包 sha256 %s）\n", report.Name, report.Version, report.Checksum)
	return nil
}

// writeVerifyAudit 写 verify 审计（库/表不可用 ⇒ 打印提示，不改变校验结论）
func writeVerifyAudit(ctx context.Context, env *Env, plugin, operator, status string) {
	db, err := env.EnsureDB(ctx)
	if err != nil {
		env.Printf("（审计未写入：%v）\n", err)
		return
	}
	if err := WriteAudit(ctx, db, plugin, ActionVerify, operator, status, map[string]any{"note": "aigo plugin verify（本地校验通道）"}); err != nil {
		env.Printf("（审计未写入：%v）\n", err)
	}
}
