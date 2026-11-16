package cli

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"
)

// ScaffoldOptions scaffold 的旗标
type ScaffoldOptions struct {
	// Kind 插件形态：code | declarative | mixed（默认 mixed）
	Kind string
	// Title 展示名（默认取插件名）
	Title string
	// Force 目标目录非空时强行写入（默认拒绝）
	Force bool
}

// Scaffold 生成插件骨架（开发期）：plugins/<名>/ 下的 manifest + 迁移 + 前端源 + Go 注册文件。
//
// 生成物刻意与「出厂未安装」一致：只写文件，不装、不改 register_gen.go——装与不装由 install 决定。
func Scaffold(env *Env, name string, opts ScaffoldOptions) error {
	kind := opts.Kind
	if kind == "" {
		kind = "mixed"
	}
	title := strings.TrimSpace(opts.Title)
	if title == "" {
		title = name
	}

	// 先用 manifest 校验器的命名规则拦一道（正则 / 保留字 / 与目录名一致）
	probe := &manifest.Manifest{Name: name, Title: title, Version: "1.0.0", Kind: kind, HostAPI: ">=1 <2"}
	if _, err := manifest.Validate(probe, manifest.Options{Prefix: env.Prefix, HostVersion: env.HostVersion}); err != nil {
		return err
	}

	dir := PluginDir(name)
	full := env.Abs(dir)
	if info, err := os.Stat(full); err == nil && info.IsDir() {
		entries, err := os.ReadDir(full)
		if err != nil {
			return fmt.Errorf("读取目标目录失败: %w", err)
		}
		if len(entries) > 0 && !opts.Force {
			return fmt.Errorf("目标目录 %s 非空：换名字或加 --force（避免覆盖既有插件）", dir)
		}
	}

	files := map[string]string{
		"README.md": pluginReadme(name, kind, title),
	}
	// plugin.yaml 的前端段按形态给出：declarative 不需要前端文件（L2 共用宿主通用页）
	switch kind {
	case "declarative":
		files[manifest.FileName] = declarativeManifest(name, title)
	case "code":
		files[manifest.FileName] = codeManifest(name, title, false)
	default:
		files[manifest.FileName] = codeManifest(name, title, true)
	}
	if kind != "declarative" {
		files[path.Join("migrations", "1_init.up.sql")] = "-- 插件迁移编号在插件内独立自增（1,2,3…），走独立版本表 <前缀>plugin_" + name + "_migrations。\n" +
			"-- 整文件一次执行、单个隐式事务 ⇒ 禁 CREATE INDEX CONCURRENTLY（docs/sql-discipline.md）。\n" +
			"CREATE TABLE IF NOT EXISTS \"__PREFIX__plugin_" + name + "_sample\" (\n" +
			"    \"id\"         bigserial   PRIMARY KEY,\n" +
			"    \"title\"      varchar(128) NOT NULL DEFAULT '',\n" +
			"    \"created_at\" timestamptz NOT NULL DEFAULT now(),\n" +
			"    \"updated_at\" timestamptz NOT NULL DEFAULT now()\n" +
			");\n"
		files[path.Join("migrations", "1_init.down.sql")] = "DROP TABLE IF EXISTS \"__PREFIX__plugin_" + name + "_sample\";\n"
		files[path.Join("web", "views", "index.vue")] = pluginVue(name)
		files[path.Join("web", "lang", "zh-cn.yaml")] = "title: " + title + "\nempty: 骨架页：把内容换成你的插件页面\n"
		files[path.Join("web", "lang", "en.yaml")] = "title: " + name + "\nempty: Skeleton page: replace with your plugin UI\n"
		files["plugin.go"] = pluginGo(name, kind, title)
	}

	created := make([]string, 0, len(files))
	names := make([]string, 0, len(files))
	for rel := range files {
		names = append(names, rel)
	}
	slices.Sort(names)
	for _, rel := range names {
		target := filepath.Join(full, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("创建目录失败: %w", err)
		}
		if err := os.WriteFile(target, []byte(files[rel]), 0o644); err != nil {
			return fmt.Errorf("写入 %s 失败: %w", rel, err)
		}
		created = append(created, path.Join(dir, rel))
	}

	// 生成后立刻用校验器自检（骨架必须能通过 manifest 校验，否则是脚手架自身的缺陷）
	if _, _, err := manifest.Load(full, manifest.Options{Prefix: env.Prefix, HostVersion: env.HostVersion}); err != nil {
		return fmt.Errorf("骨架自检失败（脚手架缺陷）: %w", err)
	}

	env.Printf("已生成插件骨架：%s（kind: %s）\n", dir, kind)
	for _, file := range created {
		env.Printf("  %s\n", file)
	}
	env.Printf("\n下一步：\n")
	env.Printf("  1) 编辑 %s 的 menus / entities / configs（键集闭集，未知键拒装）\n", path.Join(dir, manifest.FileName))
	env.Printf("  2) 看全量预演：aigo plugin install %s --dry-run\n", name)
	env.Printf("  3) 装并接线：aigo plugin install %s → 重编译 + 重启后端（接线真相在编译产物）\n", name)
	env.Printf("  4) 启用：aigo plugin enable %s（运行时闸按请求读库，≤1s 生效，无需重启）\n", name)
	env.Printf("  5) 前端改动：dev 下 HMR 直接生效；入库前 pnpm build\n")
	env.Printf("  6) L2 实体页菜单/权限节点由安装器按实体页公式代写（menus 里不必手写；手写同名行时\n" +
		"     path/component/pid/节点动作必须与公式逐字一致，否则拒装）\n")
	env.Printf("\n提示：.air.toml 的 include_dir 需含 \"plugins\"，否则 plugins/ 下的改动不触发热重载\n")
	env.Printf("门禁：check-plugin-manifest.sh / check-plugin-registry.sh 会校 manifest、目录名==name 与注册行\n")
	return nil
}

// pluginGo 生成插件包的注册文件（init 登记 + 相对段挂载点）
func pluginGo(name, kind, title string) string {
	return "// Package " + name + " 是插件 " + title + "（" + kind + "）的 Go 包：init() 只做一件事——\n" +
		"// 把自身登记进宿主注册表（只 import internal/plugin/host；**不要** import internal/plugins，否则成环）。\n" +
		"package " + name + "\n\n" +
		"import (\n" +
		"\t\"github.com/ai-go-hub/ai-go-admin/internal/plugin/host\"\n\n" +
		"\t\"github.com/gin-gonic/gin\"\n" +
		")\n\n" +
		"func init() {\n" +
		"\thost.Register(host.Meta{Name: \"" + name + "\", Version: \"1.0.0\", Kind: \"" + kind + "\", Mount: mount})\n" +
		"}\n\n" +
		"// mount 拿到的是宿主建好的 plugin/" + name + " 子组（已挂运行时闸）——只写相对段，不写绝对前缀\n" +
		"// （写了会双前缀，且权限节点名与 manifest 的 menus[].name 不再逐字相等）。\n" +
		"func mount(g *gin.RouterGroup) {\n" +
		"\t// 例：g.POST(\"/index/list\", func(c *gin.Context) { httpx.Success(c) })\n" +
		"}\n"
}

// codeManifest 生成 code / mixed 形态的 plugin.yaml
func codeManifest(name, title string, withEntity bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "name: %s\n", name)
	fmt.Fprintf(&b, "title: %s\n", title)
	fmt.Fprintf(&b, "version: 1.0.0\n")
	kind := "code"
	if withEntity {
		kind = "mixed"
	}
	fmt.Fprintf(&b, "kind: %s\n", kind)
	fmt.Fprintf(&b, "host_api: \">=1 <2\"\n")
	fmt.Fprintf(&b, "# framework_req: \">=0.1 <1\"   # 只提示不拒装（VERSION 属「各仓保留」）\n")
	fmt.Fprintf(&b, "menus:\n")
	fmt.Fprintf(&b, "  - title: %s\n", title)
	fmt.Fprintf(&b, "    type: dir\n")
	fmt.Fprintf(&b, "    name: plugin/%s\n", name)
	fmt.Fprintf(&b, "    path: plugin/%s\n", name)
	fmt.Fprintf(&b, "    icon: lucide-puzzle\n")
	fmt.Fprintf(&b, "  - title: %s\n", title)
	fmt.Fprintf(&b, "    type: menu\n")
	fmt.Fprintf(&b, "    name: plugin/%s/index\n", name)
	fmt.Fprintf(&b, "    path: plugin/%s/index\n", name)
	fmt.Fprintf(&b, "    component: /src/views/admin/plugin/%s/index.vue\n", name)
	fmt.Fprintf(&b, "    pid: plugin/%s\n", name)
	fmt.Fprintf(&b, "  - title: 查看\n")
	fmt.Fprintf(&b, "    type: node\n")
	fmt.Fprintf(&b, "    name: plugin/%s/index/read\n", name)
	fmt.Fprintf(&b, "    pid: plugin/%s/index\n", name)
	if withEntity {
		fmt.Fprintf(&b, "# L2 实体（kind: declarative/mixed）：实体页菜单与权限节点由安装器公式代写\n")
		fmt.Fprintf(&b, "#（menu path=plugin/%s/<实体>、component=%s、keepalive=0；\n", name, EntityRenderComponent)
		fmt.Fprintf(&b, "#  节点 = <实体页>/{read,create,update,delete[,sort 仅 drag_sort]}）——不要在 menus 里重复声明；\n")
		fmt.Fprintf(&b, "# 若要自定义标题/图标/权重，可以声明同名行，但 path/component/pid 必须与公式逐字一致（不符拒装）。\n")
		fmt.Fprintf(&b, "entities:\n")
		fmt.Fprintf(&b, "  - entity: items\n")
		fmt.Fprintf(&b, "    title_zh: 条目\n")
		fmt.Fprintf(&b, "    title_en: Items\n")
		fmt.Fprintf(&b, "    drag_sort: true\n")
		fmt.Fprintf(&b, "    fields:\n")
		fmt.Fprintf(&b, "      - name: title\n")
		fmt.Fprintf(&b, "        type: string\n")
		fmt.Fprintf(&b, "        title_zh: 标题\n")
		fmt.Fprintf(&b, "        title_en: Title\n")
		fmt.Fprintf(&b, "        required: true\n")
		fmt.Fprintf(&b, "        length: 128\n")
		fmt.Fprintf(&b, "        list: true\n")
		fmt.Fprintf(&b, "        search: {op: LIKE}\n")
		fmt.Fprintf(&b, "        form: input\n")
	}
	fmt.Fprintf(&b, "web:\n")
	fmt.Fprintf(&b, "  views: [web/views]\n")
	fmt.Fprintf(&b, "  lang: [web/lang/zh-cn.yaml, web/lang/en.yaml]\n")
	fmt.Fprintf(&b, "# configs:                      # 自带配置键（安装器写 configs 的 plugin.%s 分组）\n", name)
	fmt.Fprintf(&b, "#   - {name: retry_limit, title: 重试上限, type: number, value: \"3\"}\n")
	return b.String()
}

// declarativeManifest 生成 declarative 形态的 plugin.yaml（无 Go 包、无前端文件，L2 共用宿主通用页）
func declarativeManifest(name, title string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "name: %s\n", name)
	fmt.Fprintf(&b, "title: %s\n", title)
	fmt.Fprintf(&b, "version: 1.0.0\n")
	fmt.Fprintf(&b, "kind: declarative\n")
	fmt.Fprintf(&b, "host_api: \">=1 <2\"\n")
	fmt.Fprintf(&b, "# 声明式插件不需要 menus / web：L2 实体的菜单与页面由安装器按宿主公式代写——\n")
	fmt.Fprintf(&b, "# 分组目录 plugin/%s 也会自动补；menus 里声明实体页时必须与公式逐字一致（不符拒装）。\n", name)
	fmt.Fprintf(&b, "entities:\n")
	fmt.Fprintf(&b, "  - entity: items\n")
	fmt.Fprintf(&b, "    title_zh: 条目\n")
	fmt.Fprintf(&b, "    title_en: Items\n")
	fmt.Fprintf(&b, "    drag_sort: true\n")
	fmt.Fprintf(&b, "    fields:\n")
	fmt.Fprintf(&b, "      - name: title\n")
	fmt.Fprintf(&b, "        type: string\n")
	fmt.Fprintf(&b, "        title_zh: 标题\n")
	fmt.Fprintf(&b, "        title_en: Title\n")
	fmt.Fprintf(&b, "        required: true\n")
	fmt.Fprintf(&b, "        length: 128\n")
	fmt.Fprintf(&b, "        list: true\n")
	fmt.Fprintf(&b, "        search: {op: LIKE}\n")
	fmt.Fprintf(&b, "        form: input\n")
	return b.String()
}

// pluginVue 生成骨架页（落在 web/src/views/admin/plugin/<名>/index.vue）。
// 语言包键取 plugin.<名>.*（由落位路径 web/src/lang/<语言>/plugin/<名>.yaml 生成），
// 不写 defineOptions：与既有页面同款（keep-alive 的 include 按菜单 name 匹配，见 main.vue）。
func pluginVue(name string) string {
	return "<template>\n" +
		"    <div class=\"plugin-page\">\n" +
		"        <el-card shadow=\"never\">\n" +
		"            <template #header>{{ t('plugin." + name + ".title') }}</template>\n" +
		"            <el-empty :description=\"t('plugin." + name + ".empty')\" />\n" +
		"        </el-card>\n" +
		"    </div>\n" +
		"</template>\n\n" +
		"<script setup lang=\"ts\">\n" +
		"import { useI18n } from 'vue-i18n'\n\n" +
		"const { t } = useI18n()\n" +
		"</script>\n"
}

// pluginReadme 生成插件目录内的说明（作者视角的最小须知）
func pluginReadme(name, kind, title string) string {
	return "# " + title + "（" + name + "）\n\n" +
		"插件形态：`" + kind + "`。机制与纪律见仓库 `plugins/README.md`（本目录是插件源码的入库位置）。\n\n" +
		"## 目录\n\n" +
		"```\n" +
		"plugins/" + name + "/\n" +
		"  plugin.yaml             # manifest：键集闭集，未知键拒装\n" +
		"  migrations/1_init.{up,down}.sql   # 插件自管编号（1,2,3…），独立版本表\n" +
		"  plugin.go               # init() 登记进宿主注册表（只 import internal/plugin/host）\n" +
		"  web/views/**            # → web/src/views/admin/plugin/" + name + "/**\n" +
		"  web/lang/*.yaml         # → web/src/lang/<语言>/plugin/" + name + ".yaml\n" +
		"```\n\n" +
		"## 纪律\n\n" +
		"- 插件迁移**不得**放进 `cmd/migrate/migrations/`（全局游标只负责宿主表）；\n" +
		"- 插件自带 SQL **不得**写 `admin_rules`（菜单/节点由安装器代写并打归属列）；\n" +
		"- L2 实体页菜单与权限节点**由安装器公式代写**（`plugin/<名>/<实体>` + `{read,create,update,delete[,sort]}`）：\n" +
		"  `menus:` 里不必手写；若手写同名行，path/component/pid 与节点动作必须与公式逐字一致，否则拒装。\n" +
		"- 禁 `CREATE INDEX CONCURRENTLY` / 文件内 `BEGIN`/`COMMIT`（整文件隐式事务，`docs/sql-discipline.md`）；\n" +
		"- `web/views/**` 与语言包是**源**，`web/src/views/admin/plugin/" + name + "/**` 是**落位副本**（产物）；\n" +
		"- 前端菜单 `component` 必须落在 `views/admin/plugin/` 下，否则菜单静默查不到。\n\n" +
		"## 常用命令\n\n" +
		"```bash\n" +
		"aigo plugin install " + name + " --dry-run   # 全量预演（不落盘、不写库）\n" +
		"aigo plugin install " + name + "             # 安装（④–⑧ 编排：写库 → ④b L2 DDL → 落位 → 迁移 → 接线 → 收尾）\n" +
		"aigo plugin enable " + name + "              # 启用（≤1s 生效，无需重启）\n" +
		"aigo plugin verify " + name + "              # 校验源（manifest/迁移/落位映射/完整性）\n" +
		"aigo plugin export " + name + "              # 打包为 <名>-<版本>.zip（含 files.sha256）\n" +
		"```\n"
}
