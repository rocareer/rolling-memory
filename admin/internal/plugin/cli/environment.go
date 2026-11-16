package cli

import (
	"net/http"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"
)

// 插件生命周期（安装 / 卸载）的环境判定与环境门禁（宿主级口径，本文件是**唯一权威**）。
//
// 口径：**生产环境禁止插件安装与卸载，开发环境允许**；插件页面显示环境标识。
//
// 判定（三级，输入只有「显式配置值」与「源码根是否可用」两个事实）：
//  1. 显式配置 `plugin.env = dev | prod`（viper 的 AutomaticEnv ⇒ 环境变量 `PLUGIN_ENV` 同样生效）
//     ⇒ 以配置为准：生产环境边界由运维钉死，不依赖「本机碰巧有源码检出」这一物理条件。
//  2. 未显式配置（auto）⇒ 按能力判定：能定位源码根（`plugin.root`，或 FindRoot 命中含 go.mod 的目录）
//     ⇒ dev，否则 prod。依据是物理事实而非猜测：v1 的安装/卸载是**源码树操作**（落位文件 + 改
//     register_gen.go + 需重编译），源码根不可用 ⇒ 本来就执行不了；因此「页面标识」与「实际能力」
//     不会互相矛盾（这正是把判定与 resolveSourceRoot 放在同一处的原因）。
//  3. 显式值认不出（非 dev/prod）⇒ 按 prod 处置（fail-closed：宁拒绝不放行）；Source 仍记 config、
//     原始值随 Configured 回报，运维据此自查笔误。
//
// 门禁落点（同一实现，纵深防御；错误码统一 lifecycle_forbidden ⇒ HTTP 403）：
//   - HTTP 清单通道：handler 在最外层先拒（省掉整包下载与暂存）与 InstallCatalog 各一道；
//   - 写路径：runInstall（安装的最终执行段）与 Uninstall（卸载唯一入口）各一道。
//
// 环境标识（管理面）：`Environment` 直接作为 /admin/plugin/list 与 /admin/plugin/catalog/list 响应的
// `env` 段（字段即契约，snake_case）——两个插件页面据此渲染标识；不含任何本机路径。
const (
	// EnvDev 开发环境：允许插件安装 / 卸载
	EnvDev = "dev"
	// EnvProd 生产环境：禁止插件安装 / 卸载
	EnvProd = "prod"
	// EnvSourceConfig 判定依据：显式配置 plugin.env（含认不出的值——按 prod 处置）
	EnvSourceConfig = "config"
	// EnvSourceAuto 判定依据：未显式配置，按「源码根是否可用」自动判定
	EnvSourceAuto = "auto"
)

// Environment 环境判定结果（管理面响应里的 `env` 段）。
type Environment struct {
	// Name 判定结果：dev | prod
	Name string `json:"name"`
	// Source 判定依据：config（显式配置）| auto（未配置，按能力判定）
	Source string `json:"source"`
	// Configured 显式配置的原始值（Source=config 时非空；认不出的值也原样回报，便于自查笔误）
	Configured string `json:"configured,omitempty"`
	// LifecycleAllowed 安装 / 卸载是否放行（= Name 为 dev）。与判定在同一构造函数里算出，
	// 前端直接用它决定按钮可用性（不在前端复述规则，避免两处口径漂移；后端门禁才是权威）。
	LifecycleAllowed bool `json:"lifecycle_allowed"`
}

// DetectEnvironment 按当前进程的配置与文件系统判定运行环境（每请求调用，成本 = 1–2 次 stat）。
//
// 前提：调用方所在进程**已初始化配置**（serve 启动即 Init；CLI 由 NewEnv/config.Init 兜底）。
// 本函数刻意不自己 Init：它是只读判定，会被 HTTP 每请求调用，不在热路径上做全局初始化；
// 未初始化时 vip 为 nil ⇒ 读不到 plugin.env/plugin.root，判定退化为「按 CWD 的源码检出自动判」，
// 与命令面的结论可能不一致——这正是要求调用方先 Init 的原因。
func DetectEnvironment() Environment {
	configured := ""
	if vip := config.Viper(); vip != nil {
		configured = vip.GetString("plugin.env")
	}
	_, rootErr := resolveSourceRoot()
	return classifyEnvironment(configured, rootErr == nil)
}

// classifyEnvironment 判定纯函数（显式配置值 × 源码根可用性）；分离出来便于逐格测试。
// 三个分支的取值见文件头「判定」段：显式 dev / 显式 prod（含认不出的值）/ 未配置。
func classifyEnvironment(configured string, rootAvailable bool) Environment {
	value := strings.TrimSpace(configured)
	if value != "" {
		name := EnvProd
		// 宽松到「大小写与首尾空白不算错」，但不收别名：只认 dev / prod，其余一律按 prod（fail-closed）。
		switch strings.ToLower(value) {
		case EnvDev:
			name = EnvDev
		case EnvProd:
			name = EnvProd
		}
		return Environment{Name: name, Source: EnvSourceConfig, Configured: value, LifecycleAllowed: name == EnvDev}
	}
	name := EnvProd
	if rootAvailable {
		name = EnvDev
	}
	return Environment{Name: name, Source: EnvSourceAuto, LifecycleAllowed: name == EnvDev}
}

// detectEnvironment 门禁读取环境判定的唯一取值点（默认就是 DetectEnvironment）。
//
// 之所以留这个包级注入点：写路径门禁（runInstall / Uninstall）必须用**执行时**的真实判定，
// 不能由调用方传参（传参就能被绕）；而单测要验证「生产环境拒装」又不宜为此初始化全局配置或
// 改工作目录（本包的库级用例 frameworkMigrationSetup 依赖「谁先初始化配置」，抢先后会改变其行为）。
// 仅测试替换，且只允许**非并行**用例替换（Go 的 t.Parallel 用例在顺序用例跑完后才并发执行，
// 因此与替换不重叠；包内并行用例也不触发本门禁）。
var detectEnvironment = DetectEnvironment

// RequireLifecycle 生命周期动作（安装 / 卸载）的环境门禁：生产环境返回 *CatalogError
// （error_code=lifecycle_forbidden / HTTP 403），调用方原样透传即可。
//
// 文案不含任何本机路径（与来源/清单通道同一脱敏口径）：错误会进插件管理页的动作审计与页面提示。
func RequireLifecycle(action string) error {
	return RequireLifecycleWith(action, detectEnvironment())
}

// RequireLifecycleWith 同 RequireLifecycle，但用调用方给定的判定结果（HTTP handler 注入固定环境时用；
// 判定规则单点仍在 classifyEnvironment，两处不会分叉）。
func RequireLifecycleWith(action string, env Environment) error {
	if env.LifecycleAllowed {
		return nil
	}
	return catalogError("lifecycle_forbidden", http.StatusForbidden,
		"生产环境禁止插件%s（环境判定：%s；依据：%s）。安装与卸载是源码树操作，只允许在开发环境执行；"+
			"dev 环境（含未显式配置时的源码检出）不受此限",
		action, env.Name, env.basis())
}

// basis 判定依据的可读表述（CLI 错误与页面提示同源；不含路径）。
func (e Environment) basis() string {
	if e.Source == EnvSourceConfig {
		if e.Name == EnvProd && !isKnownEnvValue(e.Configured) {
			return "配置 plugin.env=" + e.Configured + " 无法识别（只认 dev | prod），已按生产环境处置"
		}
		return "已显式配置 plugin.env=" + e.Configured
	}
	if e.LifecycleAllowed {
		return "未配置 plugin.env，自动判定：检测到源码检出"
	}
	return "未配置 plugin.env，自动判定：未检测到源码检出"
}

// isKnownEnvValue 显式配置值是否是可识别的环境名（认不出 ⇒ 按 prod 处置）。
func isKnownEnvValue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case EnvDev, EnvProd:
		return true
	default:
		return false
	}
}
