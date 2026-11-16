// plugin: kb v1.0.0
// 本文件断言本插件路由层的**两条硬契约**（宿主侧复跑 router.Setup 的旧写法不可用：
// router.Setup：那会构成 import cycle——宿主 router 空白导入 internal/plugins，而 internal/plugins 经
// register_gen 反向依赖本插件包；且「安装态断言」在出厂（未安装）态会假红）：
//
//	① 每条后台路由都自挂 AdminAuth **与** AdminPermission（宿主只给「子组 + 运行时闸」，节点鉴权须插件自挂）——
//	   分两层断言：运行时每条路由未认证不得 200 业务（AdminAuth 在位），源码面逐行扫
//	   `middleware.AdminAuth(), middleware.AdminPermission()`（AdminPermission 在位，含 list/get 的
//	   OR 回落语义由宿主中间件承担）；
//	② 路由集推导出的**权限节点名集合**等于 plugin.yaml 里 menus[] 的节点集合（双向）——
//	   节点名**用宿主 permission.BuildCheckPath 实算**（与中间件同一函数），再套用中间件的
//	   list/get→同级 read 回落（internal/middleware/admin_permission.go 的口径），不是照抄手写表；
//	③ 插件源码里的路由注册行**逐行**都在（防「表里登记了、源码里没有」的漂移）。
//
// 两条都是「静默坏」的免疫面：漏挂中间件 = 任何有效后台会话都能直打端点；节点名对不上 = 非超管
// 全 403（装上了但按钮全灰），而静态门禁抓不到。用同包测试（package kb）直接调 Register。
package kb

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/permission"
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"

	"github.com/gin-gonic/gin"
)

// mountSegment 本插件挂载点的相对段（宿主给的组 = 后台入口前缀 + "/plugin/kb"）
const mountSegment = "/plugin/kb"

// pluginRoutes 本插件的后台路由全集（相对段 → 节点名）。
// 表驱动：新增路由必须同时改本表与 plugin.yaml 的 menus 段，否则本文件的两个用例会红。
var pluginRoutes = []struct {
	method string
	rel    string
	node   string
}{
	// 库 / 授权 / 文档基础面
	{http.MethodPost, "/library/list", "plugin/kb/library/read"},
	{http.MethodGet, "/library/get/:pk", "plugin/kb/library/read"},
	{http.MethodPost, "/library/create", "plugin/kb/library/create"},
	{http.MethodPost, "/library/update/:pk", "plugin/kb/library/update"},
	{http.MethodPost, "/library/delete", "plugin/kb/library/delete"},
	{http.MethodPost, "/grant/list", "plugin/kb/grant/read"},
	{http.MethodPost, "/grant/add", "plugin/kb/grant/add"},
	{http.MethodPost, "/grant/remove", "plugin/kb/grant/remove"},
	{http.MethodPost, "/document/list", "plugin/kb/document/read"},
	{http.MethodGet, "/document/get/:pk", "plugin/kb/document/read"},
	{http.MethodPost, "/document/delete", "plugin/kb/document/delete"},
	// 检索 / 逐字核对
	{http.MethodPost, "/search", "plugin/kb/search"},
	{http.MethodGet, "/chunk/get/:pk", "plugin/kb/chunk/read"},
	// 文档动作面
	{http.MethodPost, "/document/rebuild", "plugin/kb/document/rebuild"},
	{http.MethodPost, "/document/cancel", "plugin/kb/document/cancel"},
	{http.MethodPost, "/document/failed", "plugin/kb/document/failed"},
	// 审计
	{http.MethodPost, "/audit/list", "plugin/kb/audit/read"},
	// 标签绑定面
	{http.MethodPost, "/tag_link/list", "plugin/kb/tag_link/read"},
	{http.MethodPost, "/tag_link/bind", "plugin/kb/tag_link/bind"},
	{http.MethodPost, "/tag_link/unbind", "plugin/kb/tag_link/unbind"},
	// 四套 CRUD（tag_rule 无 /sort：表无排序列 ⇒ 路由集与节点集 1:1）
	{http.MethodPost, "/tag/list", "plugin/kb/tag/read"},
	{http.MethodPost, "/tag/create", "plugin/kb/tag/create"},
	{http.MethodPost, "/tag/delete", "plugin/kb/tag/delete"},
	{http.MethodPost, "/tag/sort", "plugin/kb/tag/sort"},
	{http.MethodGet, "/tag/get/:pk", "plugin/kb/tag/read"},
	{http.MethodPost, "/tag/update/:pk", "plugin/kb/tag/update"},
	{http.MethodPost, "/tag_group/list", "plugin/kb/tag_group/read"},
	{http.MethodPost, "/tag_group/create", "plugin/kb/tag_group/create"},
	{http.MethodPost, "/tag_group/delete", "plugin/kb/tag_group/delete"},
	{http.MethodPost, "/tag_group/sort", "plugin/kb/tag_group/sort"},
	{http.MethodGet, "/tag_group/get/:pk", "plugin/kb/tag_group/read"},
	{http.MethodPost, "/tag_group/update/:pk", "plugin/kb/tag_group/update"},
	{http.MethodPost, "/tag_rule/list", "plugin/kb/tag_rule/read"},
	{http.MethodPost, "/tag_rule/create", "plugin/kb/tag_rule/create"},
	{http.MethodPost, "/tag_rule/delete", "plugin/kb/tag_rule/delete"},
	{http.MethodGet, "/tag_rule/get/:pk", "plugin/kb/tag_rule/read"},
	{http.MethodPost, "/tag_rule/update/:pk", "plugin/kb/tag_rule/update"},
	{http.MethodPost, "/team/list", "plugin/kb/team/read"},
	{http.MethodPost, "/team/create", "plugin/kb/team/create"},
	{http.MethodPost, "/team/delete", "plugin/kb/team/delete"},
	{http.MethodPost, "/team/sort", "plugin/kb/team/sort"},
	{http.MethodGet, "/team/get/:pk", "plugin/kb/team/read"},
	{http.MethodPost, "/team/update/:pk", "plugin/kb/team/update"},
	// 运维端点
	{http.MethodPost, "/maintenance/check", "plugin/kb/maintenance/check"},
	{http.MethodPost, "/maintenance/reindex", "plugin/kb/maintenance/reindex"},
	{http.MethodPost, "/maintenance/reembed", "plugin/kb/maintenance/reembed"},
	// 文档导入
	{http.MethodPost, "/document/import", "plugin/kb/document/import"},
}

// TestRegisterExposesExpectedRoutes 路由集逐条对齐（方法 + 相对段 + 无重复 + 无多余）
func TestRegisterExposesExpectedRoutes(t *testing.T) {
	engine := mountedEngine()
	registered := map[string]int{}
	for _, route := range engine.Routes() {
		if !strings.HasPrefix(route.Path, mountSegment) {
			continue
		}
		registered[route.Method+" "+route.Path]++
	}
	for _, spec := range pluginRoutes {
		key := spec.method + " " + mountSegment + spec.rel
		if registered[key] != 1 {
			t.Fatalf("路由 %s 注册数 = %d，期望 1", key, registered[key])
		}
		delete(registered, key)
	}
	if len(registered) > 0 {
		extras := make([]string, 0, len(registered))
		for key := range registered {
			extras = append(extras, key)
		}
		sort.Strings(extras)
		t.Fatalf("存在未在用例表登记的路由（新增路由须同步用例表与 plugin.yaml）：%v", extras)
	}
}

// TestRoutesGuardWithAdminAuth 逐条路由都挂了 AdminAuth：未带凭据的请求不得以 200 成功业务
// （统一响应体带 need_login 是宿主的未登录形态；401 亦接受）。
func TestRoutesGuardWithAdminAuth(t *testing.T) {
	engine := mountedEngine()
	for _, spec := range pluginRoutes {
		t.Run(spec.method+" "+spec.rel, func(t *testing.T) {
			req := httptest.NewRequest(spec.method, mountSegment+spec.rel, strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			engine.ServeHTTP(rec, req)
			if rec.Code == http.StatusOK && !strings.Contains(rec.Body.String(), `"need_login"`) {
				t.Fatalf("未认证请求返 200 且不带 need_login ⇒ 该路由疑似未挂 AdminAuth: %s", rec.Body.String())
			}
		})
	}
}

// TestRouteNodesMatchManifest 路由集 ↔ 清单节点集**双向**相等（读本插件自己的 plugin.yaml）。
func TestRouteNodesMatchManifest(t *testing.T) {
	pluginDir := pluginDirOf(t)
	mf, _, err := manifest.Load(pluginDir, manifest.Options{})
	if err != nil {
		t.Fatalf("读取插件清单失败: %v", err)
	}

	declared := map[string]struct{}{}
	for _, menu := range mf.Menus {
		if menu.Type == "node" {
			declared[menu.Name] = struct{}{}
		}
	}

	// 节点名**实算**：取真实注册表里的每条路由 → 宿主 permission.BuildCheckPath → 中间件的回落
	derived := map[string]struct{}{}
	for _, route := range mountedEngineRoutes() {
		derived[nodeNameOf(route.Path)] = struct{}{}
	}

	missing := make([]string, 0)
	for node := range derived {
		if _, ok := declared[node]; !ok {
			missing = append(missing, node)
		}
	}
	extra := make([]string, 0)
	for node := range declared {
		if _, ok := derived[node]; !ok {
			extra = append(extra, node)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Fatalf("路由推导出但清单未声明的节点（非超管会 403）：%v", missing)
	}
	if len(extra) > 0 {
		t.Fatalf("清单声明但没有任何路由支撑的节点（装饰性权限）：%v", extra)
	}
}

// pluginDirOf 定位本插件目录（= 测试工作目录向上找到仓库根后的 plugins/kb）
func pluginDirOf(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "plugins", "kb")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("未找到 go.mod（仓库根）")
		}
		dir = parent
	}
}

// TestRouteSourcesDeclareBothMiddlewares 源码面逐行扫描：插件自己的每条路由注册行都必须同时出现
// `middleware.AdminAuth()` 与 `middleware.AdminPermission()`。
//
// 为什么必须扫源码：运行时探针只能证明 AdminAuth（未认证被拦），**证明不了** AdminPermission
// ——缺它时任何已登录管理员都能直打端点（节点鉴权形同虚设），而所有既有用例都不会红。
// 宿主基座 handler.RegisterBaseRoutes 的四条路由（tag / tag_group / team + 显式注册的 tag_rule 不适用）
// 由宿主实现自持两个中间件（宿主的 internal/handler/base.go 与其单测是那部分的证据面），
// 本用例额外断言「走基座的调用点个数」= 3，防插件侧新增路由时悄悄绕过基座。
func TestRouteSourcesDeclareBothMiddlewares(t *testing.T) {
	root := pluginDirOf(t)
	files, err := pluginGoFiles(filepath.Join(root, "handler"), filepath.Join(root, "router"))
	if err != nil {
		t.Fatalf("遍历插件源码失败: %v", err)
	}
	reRoute := regexp.MustCompile(`\.(POST|GET)\(`)
	baseCalls := 0
	routeLines := 0
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("读取 %s 失败: %v", file, err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.Contains(trimmed, "handler.RegisterBaseRoutes(") {
				baseCalls++
			}
			if !reRoute.MatchString(trimmed) || strings.HasPrefix(trimmed, "//") {
				continue
			}
			routeLines++
			if !strings.Contains(trimmed, "middleware.AdminAuth()") {
				t.Fatalf("%s:%d 路由注册行缺 AdminAuth: %s", file, i+1, trimmed)
			}
			if !strings.Contains(trimmed, "middleware.AdminPermission()") {
				t.Fatalf("%s:%d 路由注册行缺 AdminPermission（任何已登录管理员都能直打该端点）: %s", file, i+1, trimmed)
			}
		}
	}
	if routeLines == 0 {
		t.Fatal("未扫到任何路由注册行（口径失效：检查正则或源码形态）")
	}
	if baseCalls != 3 {
		t.Fatalf("走宿主基座 RegisterBaseRoutes 的调用点 = %d，期望 3（tag / tag_group / team；tag_rule 因无排序列显式注册）", baseCalls)
	}
}

// TestRouteTableCoversRegisteredRoutes 用例表 ↔ 真实注册路由（双向）：防「表里登记了、源码里没有」。
func TestRouteTableCoversRegisteredRoutes(t *testing.T) {
	registered := map[string]struct{}{}
	for _, route := range mountedEngineRoutes() {
		registered[route.Method+" "+route.Path] = struct{}{}
	}
	table := map[string]struct{}{}
	for _, spec := range pluginRoutes {
		table[spec.method+" "+mountSegment+spec.rel] = struct{}{}
	}
	for key := range registered {
		if _, ok := table[key]; !ok {
			t.Fatalf("真实注册路由 %s 未在用例表里登记（新增路由须同步用例表）", key)
		}
	}
	for key := range table {
		if _, ok := registered[key]; !ok {
			t.Fatalf("用例表登记了 %s，但引擎里没有这条路由（源码已删/路径写错）", key)
		}
	}
}

// nodeNameOf 路由路径 → 权限节点名（与中间件同口径：宿主 BuildCheckPath + list/get 回落同级 read）
func nodeNameOf(fullPath string) string {
	checkPath := permission.BuildCheckPath(fullPath)
	if lastSlash := strings.LastIndex(checkPath, "/"); lastSlash != -1 {
		switch checkPath[lastSlash+1:] {
		case "list", "get":
			return checkPath[:lastSlash] + "/read"
		}
	}
	return checkPath
}

// pluginGoFiles 递归收集插件源码文件（排除 _test.go：扫描面是生产代码里的路由注册行）
func pluginGoFiles(dirs ...string) ([]string, error) {
	out := make([]string, 0, 16)
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			out = append(out, path)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// mountedEngineRoutes 真实注册路由表（只挂本插件的引擎）
func mountedEngineRoutes() []gin.RouteInfo {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	Register(engine.Group(mountSegment))
	return engine.Routes()
}

// mountedEngine 建一个只挂本插件路由的引擎（不跑宿主 router.Setup：出厂态未安装 ⇒ 会假红）
func mountedEngine() *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	Register(engine.Group(mountSegment))
	return engine
}
