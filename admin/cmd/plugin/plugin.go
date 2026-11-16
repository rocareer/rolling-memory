// Package plugin 是 `aigo plugin` 命令组的 cobra 壳（子命令定义与旗标收集）；
// 业务逻辑在 internal/plugin/cli（便于管理面与测试复用）。
package plugin

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/cli"

	"github.com/spf13/cobra"
)

// NewCommand 返回插件命令组（install / enable / disable / uninstall / defaults / list / sync /
// export / verify / scaffold / migrate-force / promote / source / catalog）
func NewCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "plugin",
		Short: "插件管理（manifest 校验 / 安装 / 启停 / 卸载 / 打包 / 审计）",
		Long: "后台插件机制的命令面：\n" +
			"  install/enable/disable/uninstall  生命周期（先禁用后卸载是硬门禁；每步都支持 --dry-run 预演）\n" +
			"  defaults                          确保默认依赖插件已安装并启用（幂等；迁移后、启动前执行）\n" +
			"  list                              台账 / 进程内注册表 / 注册文件三方对照\n" +
			"  sync                              以台账重放 register_gen.go（接线生成物对账）\n" +
			"  verify/scaffold/export            校验源 / 生成骨架 / 打包（含 files.sha256）\n" +
			"  migrate-force                     插件迁移版本表 force（dirty 修复入口）\n" +
			"  promote                           L2 → L1：把实体移出声明式面（删元数据/通用页 + 审计）\n" +
			"  source/catalog                    仓库来源配置与统一清单（catalog v1）：同步/列举/预演/安装\n" +
			"环境门禁：生产环境（plugin.env=prod，或未显式配置且无源码检出）禁止 install/uninstall，\n" +
			"  只允许在开发环境执行；环境标识见插件管理页（/admin/plugin/list 的 env 段）。\n" +
			"信任定位：kind=code 的插件等价于把该段代码 git merge 进仓库（与宿主同权限、无隔离层）。",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if err := config.Init(); err != nil {
				return fmt.Errorf("初始化配置: %w", err)
			}
			loc, err := time.LoadLocation(config.Get().App.Timezone)
			if err != nil {
				return fmt.Errorf("加载时区: %w", err)
			}
			time.Local = loc
			return nil
		},
	}
	root.AddCommand(installCmd(), enableCmd(), disableCmd(), uninstallCmd(), listCmd(), syncCmd(),
		defaultsCmd(), exportCmd(), verifyCmd(), scaffoldCmd(), migrateForceCmd(), promoteCmd(),
		cli.SourceCommand(), cli.CatalogCommand())
	return root
}

// 可测构造点：dev 通道的脱敏接线（install 子命令里的 opts.Sanitize）只在 RunE 真正跑起来时才生效，
// 而 RunE 直接调 env() + cli.Install（会初始化配置、连库、动工作树）⇒ 单测无法在不连库的前提下断言
// 「接线接上了」。把这两个叶子调用变量化，测试即可注入替身、完整跑一遍 installCmd → RunE 路径；
// 生产值就是原来的调用，命令行为不变。
var (
	newPluginEnv     = cli.NewEnv
	runPluginInstall = cli.Install
)

// env 构造命令环境（仓库根 / 表前缀 / 宿主版本）
func env() (*cli.Env, error) {
	return newPluginEnv()
}

// installCmd 安装（校验 +全量预演；执行主线 P2）
func installCmd() *cobra.Command {
	opts := cli.InstallOptions{}
	cmd := &cobra.Command{
		Use:   "install <包|目录>",
		Short: "安装插件（目录源或 zip 包：zip 走 staging 防护 + files.sha256 比对；--dry-run 输出全量预演）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := env()
			if err != nil {
				return err
			}
			// 失败审计的 detail.error 走与 catalog 通道同一个脱敏回调（cli.SanitizePluginText，
			// 即 source.SanitizePaths）：本通道的失败原因同样会带 staging 临时目录、落位目标与命令行给的
			// 包路径等绝对路径，而该字符串要在插件管理页的动作审计区块展示（第 7 条）。
			opts.Sanitize = func(text string) string { return cli.SanitizePluginText(e, text, args[0]) }
			return runPluginInstall(cmd.Context(), e, args[0], opts)
		},
	}
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "只预演（不落盘、不写库、不改接线文件）")
	cmd.Flags().BoolVar(&opts.Enable, "enable", false, "安装完成后接着置 enabled")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "与工作树同名且内容不同时强行覆盖")
	cmd.Flags().BoolVar(&opts.AllowUnsigned, "allow-unsigned", false, "允许未签名包（本机未登记受信公钥 plugin.trusted_keys 时必需；无效签名不豁免）")
	cmd.Flags().StringVar(&opts.Operator, "operator", "", "审计署名（只是署名，不构成身份认证）")
	return cmd
}

// enableCmd 启用（状态 + 菜单 status + 审计；≤1s 生效，无需重启）
func enableCmd() *cobra.Command {
	opts := cli.StatusOptions{}
	cmd := &cobra.Command{
		Use:   "enable <名>",
		Short: "启用插件（置 enabled + 菜单 status=1 + 审计；运行时闸按请求读库）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := env()
			if err != nil {
				return err
			}
			return cli.EnableWith(cmd.Context(), e, args[0], opts)
		},
	}
	cmd.Flags().StringVar(&opts.Operator, "operator", "", "审计署名")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "只预演（打印状态机判定与将改写的行，一行都不写）")
	return cmd
}

// disableCmd 停用（还原只有这一条路径）
func disableCmd() *cobra.Command {
	opts := cli.StatusOptions{}
	cmd := &cobra.Command{
		Use:   "disable <名>",
		Short: "停用插件（置 disabled + 菜单 status=0 + 审计）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := env()
			if err != nil {
				return err
			}
			return cli.DisableWith(cmd.Context(), e, args[0], opts)
		},
	}
	cmd.Flags().StringVar(&opts.Operator, "operator", "", "审计署名")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "只预演（打印状态机判定与将改写的行，一行都不写）")
	return cmd
}

// uninstallCmd 卸载（先禁用后卸载；--purge 只能与同一次卸载一起执行）
func uninstallCmd() *cobra.Command {
	opts := cli.UninstallOptions{}
	cmd := &cobra.Command{
		Use:   "uninstall <名>",
		Short: "卸载插件（固定顺序；--dry-run 打印将删/将保留清单）",
		Long: "卸载插件（固定顺序 a–e；--dry-run 打印将删/将保留清单）：\n" +
			"  --purge：回滚插件迁移 + drop 版本表 + drop 引擎侧 L2 表（须同次给 --yes）；\n" +
			"  对「存量库迁移基线（adopt）」装入的插件（台账 adopted_through > 0）--purge **默认拒绝**：\n" +
			"    其 down 文件是核心链迁移的副本 ⇒ 回滚删的是核心链建的表与数据，而核心游标仍记已应用\n" +
			"    （`aigo migrate up` 不会重建）⇒ 放行需三重显式 --purge --yes --purge-adopted。",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := env()
			if err != nil {
				return err
			}
			return cli.Uninstall(cmd.Context(), e, args[0], opts)
		},
	}
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "只预演（打印将删/将保留清单）")
	cmd.Flags().BoolVar(&opts.Purge, "purge", false, "回滚插件迁移并 drop 版本表（须同时 --yes；只能与同一次卸载一起执行）")
	cmd.Flags().BoolVar(&opts.Yes, "yes", false, "--purge 的二次确认")
	cmd.Flags().BoolVar(&opts.PurgeAdopted, "purge-adopted", false,
		"--purge 对「存量库迁移基线（adopt）」装入的插件放行（第三重显式确认：adopted_through>0 时默认拒绝——down 是核心链迁移的副本，回滚会删核心建的表与数据）")
	cmd.Flags().BoolVar(&opts.KeepFiles, "keep-files", false, "只清 DB 侧与注册行、保留文件（入库仓的「出厂态」）")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "跳过「先禁用后卸载」门禁（只给 failed 半装现场用）")
	cmd.Flags().StringVar(&opts.Operator, "operator", "", "审计署名")
	return cmd
}

// listCmd 三方对照
func listCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "插件三方对照（台账 / 进程内注册表 / 注册文件），不一致显式报告",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := env()
			if err != nil {
				return err
			}
			return cli.List(cmd.Context(), e)
		},
	}
}

// syncCmd 以台账重放接线生成物（手改文件 / 直接改库造成的漂移显式对账并修复）
func syncCmd() *cobra.Command {
	opts := cli.SyncOptions{}
	cmd := &cobra.Command{
		Use:   "sync [<名>...]",
		Short: "以台账重放 register_gen.go（接线生成物对账；缺省全域，给名只对账这些名；--dry-run 只预演）",
		Long: "以 admin_plugins 台账重放接线生成物 internal/plugins/register_gen.go：\n" +
			"  重放范围：不给名 ⇒ 全域（整份生成物 = 台账重放）；给出名 ⇒ 只对账这些名（该有的行补上、\n" +
			"    不该有的行撤下；范围外的行原样保留）。名既不在台账也不在注册文件里 ⇒ 报错（防拼错后静默无操作）\n" +
			"  意愿集 = 台账里 kind != declarative 且 status != failed 的插件（declarative 无 Go 包；failed 半装不应接线）\n" +
			"  三方对照（台账 / 进程内注册表 / 注册文件）与 `aigo plugin list` 同一口径；增/删注册行逐条给出理由\n" +
			"  新补的注册行要求源码目录存在（否则拒写：指向缺失包的注册行会让 go build 必失败）\n" +
			"  重放不改 DB 台账与进程内注册表；生效需重编译 + 重启后端",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := env()
			if err != nil {
				return err
			}
			return cli.Sync(cmd.Context(), e, args, opts)
		},
	}
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "只预演（打印将增/删的注册行与差异，不改写生成物）")
	return cmd
}

// defaultsCmd 确保插件模块的默认依赖插件已安装并启用（正本 = host.DefaultPlugins）
func defaultsCmd() *cobra.Command {
	opts := cli.DefaultsOptions{}
	cmd := &cobra.Command{
		Use:   "defaults",
		Short: "确保默认依赖插件已安装并启用（幂等；真实安装链 + 审计）",
		Long: "确保插件模块的默认依赖插件已安装并启用（清单正本 internal/plugin/host/defaults.go）：\n" +
			"  台账无行 ⇒ 走真实安装链（落位/接线/审计）并启用；installed/disabled ⇒ 启用；enabled ⇒ 无变化\n" +
			"  生产环境仍受环境门禁：未安装的默认插件不允许安装（命令报错），已安装的只做启用\n" +
			"  与 requires 的区别：requires 是插件间能力依赖（v1 只校验、不自动装）；本命令是插件模块\n" +
			"  基线插件的显式入口，建议在 `aigo migrate up` 之后、启动服务之前执行",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := env()
			if err != nil {
				return err
			}
			return cli.EnsureDefaults(cmd.Context(), e, opts)
		},
	}
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "只预演（打印每个默认插件的状态与将执行的动作，不写库/不落盘）")
	cmd.Flags().StringVar(&opts.Operator, "operator", "", "审计署名（默认 defaults）")
	return cmd
}

// exportCmd 打包（已装源码 → <名>-<版本>.zip，含 files.sha256；出包即自校验）
func exportCmd() *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:   "export <名>",
		Short: "从已装源码打包（含 files.sha256 并出包自校验；默认落 plugins/.store/）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := env()
			if err != nil {
				return err
			}
			_, err = cli.Export(e, args[0], out)
			return err
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "输出路径（文件或目录；默认 plugins/.store/<名>-<版本>.zip）")
	return cmd
}

// verifyCmd 校验源或包（结构 / manifest / 迁移 / 落位映射 / 完整性 / 签名 / 依赖）
func verifyCmd() *cobra.Command {
	opts := cli.VerifyOptions{}
	cmd := &cobra.Command{
		Use:   "verify <名|目录|包.zip>",
		Short: "校验插件源或插件包（fail-closed；包走 staging 防护 + files.sha256 逐项比对 + 可选 ed25519 验签）",
		Long: "校验插件源（目录）或插件包（<名>-<版本>.zip），任一检查失败即非零退出（fail-closed）：\n" +
			"  manifest 闭集与命名 · host_api 相容 · 迁移编号成对与禁词 · 落位映射 · requires 查台账\n" +
			"  包源另加：staging zip 防护 · 包内 files.sha256 逐项比对（缺失 / 不一致 / 清单之外夹带 ⇒ 拒）\n" +
			"  签名（可选，ed25519 分离签名，签名对象 = files.sha256 的字节）：\n" +
			"    本机在配置键 plugin.trusted_keys 登记作者公钥（hex / base64）⇒ 验签，通过则记指纹；\n" +
			"    未登记公钥或包未签名 ⇒ 须显式 --allow-unsigned 才接受；无效签名一律拒（--allow-unsigned 不豁免）。",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := env()
			if err != nil {
				return err
			}
			return cli.Verify(cmd.Context(), e, args[0], opts)
		},
	}
	cmd.Flags().BoolVar(&opts.AllowUnsigned, "allow-unsigned", false, "允许未签名包 / 未验证的签名（无效签名不豁免）")
	cmd.Flags().StringVar(&opts.Operator, "operator", "", "审计署名")
	return cmd
}

// scaffoldCmd 生成骨架
func scaffoldCmd() *cobra.Command {
	opts := cli.ScaffoldOptions{}
	cmd := &cobra.Command{
		Use:   "scaffold <名>",
		Short: "生成插件骨架（manifest + 迁移 + 前端源 + Go 注册文件）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := env()
			if err != nil {
				return err
			}
			return cli.Scaffold(e, args[0], opts)
		},
	}
	cmd.Flags().StringVar(&opts.Kind, "kind", "mixed", "插件形态：code | declarative | mixed")
	cmd.Flags().StringVar(&opts.Title, "title", "", "展示名（默认取插件名）")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "目标目录非空时强行写入")
	return cmd
}

// migrateForceCmd 插件迁移版本表 force（dirty 修复入口）
func migrateForceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate-force <名> <版本>",
		Short: "强制插件版本表到指定版本（修复 dirty；只改标记，不执行/回滚 SQL）",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := env()
			if err != nil {
				return err
			}
			version, err := parseVersion(args[1])
			if err != nil {
				return err
			}
			return cli.MigrateForce(e, args[0], version)
		},
	}
	return cmd
}

// promoteCmd L2 → L1 升级（缺 --yes 只预演；执行主线删元数据/通用页菜单节点 + 审计）
func promoteCmd() *cobra.Command {
	opts := cli.PromoteOptions{}
	cmd := &cobra.Command{
		Use:   "promote <名> --entity <实体>",
		Short: "把某个 L2 实体退出声明式（删元数据/通用页菜单节点 + 审计；表与数据保留，缺 --yes 只预演）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := env()
			if err != nil {
				return err
			}
			return cli.Promote(cmd.Context(), e, args[0], opts)
		},
	}
	cmd.Flags().StringVar(&opts.Entity, "entity", "", "要退出 L2 的实体名（必填）")
	cmd.Flags().BoolVar(&opts.Yes, "yes", false, "确认执行")
	cmd.Flags().StringVar(&opts.Operator, "operator", "", "审计署名")
	return cmd
}

// versionArgPattern 迁移版本号字面形态：可选符号 + 十进制数字（严格全匹配）
var versionArgPattern = regexp.MustCompile(`^[+-]?[0-9]+$`)

// parseVersion 解析迁移版本号（严格全匹配：可选的 +/- 号后必须全是十进制数字；
// -1 表示回退到「无版本」，与 golang-migrate 的 Force 语义一致）。
// 为什么不用 fmt.Sscanf("%d")：它对 "1.5"/"2x"/"0x10" 都是**前缀命中**（解出 1/2/0），
// 会把打错的版本号悄悄 force 到一个意料外的版本上——dirty 修复入口必须拒绝这种输入。
func parseVersion(raw string) (int, error) {
	value := strings.TrimSpace(raw)
	if !versionArgPattern.MatchString(value) {
		return 0, fmt.Errorf("版本号 %q 不是整数（只接受可选符号 + 十进制数字，不接受 \"1.5\"/\"2x\"/\"0x10\" 这类写法）", raw)
	}
	version, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("版本号 %q 超出整数范围: %w", raw, err)
	}
	return version, nil
}
