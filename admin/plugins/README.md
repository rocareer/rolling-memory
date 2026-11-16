# 插件（plugins/）

本目录是**插件源码的入库位置**：一个插件一个子目录（`plugins/<名>/`）。机制说明以本文件与
`internal/plugin/manifest` 校验规则为准。

## 状态（机制已在仓；本项目作为参考宿主已装 demo / env-badge）

| 面 | 状态 |
|---|---|
| 宿主基座 `internal/plugin/host`（机制接口版本 / 注册表 / 运行时闸 / 启动预检 / 元数据装载） | 已就位 |
| 迁移 `cmd/migrate/migrations/000005_plugin.{up,down}.sql`（台账 + 实体元数据 + 审计三表、`admin_rules.plugin` 归属列、3 个管理面节点） | 已就位 |
| 迁移 `cmd/migrate/migrations/000006_plugin_admin_menu.{up,down}.sql`（插件管理页菜单行 + 3 个节点挂靠） | 已就位 |
| 启动接线 `internal/plugins`（`register_gen.go` 生成物 + `wiring.go` + L2 引擎 `l2.Mount`） | 已就位（`router.go` 两行空白导入——插件接线 + 管理面路由模块；`cmd/serve` 预检单点块） |
| 命令面 `cmd/plugin` + `internal/plugin/cli` | 已就位：**12 子命令** = `install` / `enable` / `disable` / `uninstall` / `defaults` / `list` / `sync` / `export` / `verify` / `scaffold` / `migrate-force` / `promote`；生命周期执行主线**全部已实现**（install 含 L2 建表与 §5.4 公式代写、卸载清理、promote 单事务退出；每步支持 `--dry-run` 预演，含启停的打印式预演；`defaults` 确保默认依赖插件就位） |
| L2 数据面 `internal/plugin/l2` + `internal/plugin/cli/l2ddl.go` | 已就位：动态引擎 + 安装编排 ④b 的计划/应用（dry-run ⑤ 段逐条列 DDL）；宿主通用页 `web/src/views/admin/plugin/render/index.vue` |
| manifest 校验 `internal/plugin/manifest` | 已就位（闭集解析：未知键拒装 / 命名与保留字 / `host_api` 域 / 长度预算） |
| 示例插件 `plugins/demo` | 已就位（mixed：L2 实体 + L1 页 + 插件迁移），**本项目已安装并启用**（装机示范：注册行、菜单/节点、迁移记录、落位副本俱在）|
| 环境浮标 `plugins/env-badge` | 已就位（code：布局级 widget 的最小样例——后台右下角 DEV/PROD 浮标 + 英文 tooltip），**本项目已安装并启用**；环境判定复用 `cli/environment.go`；**默认依赖插件**（`aigo plugin defaults` 确保就位） |

**结论**：示例插件 `plugins/demo` 的文件在仓（含落位副本 `web/src/views/admin/plugin/demo/**` 与语言包），且本项目**已安装并启用**——`register_gen.go` 有注册行、`admin_rules` 有 `plugin=demo` 的菜单与节点、插件迁移版本表就位。回到「出厂态」以验证安装/卸载语义：`aigo plugin uninstall --keep-files demo`；`aigo plugin scaffold <名>` 可生成新插件骨架。

## 一、部署纪律：迁移先于启动

宿主启动会在 `database.Init()` 之后、`router.Setup()` 之前调一次 `host.Preflight(ctx)`，它读
`admin_plugins` / `admin_plugin_entities`。因此：

- **表缺失（插件机制迁移未应用，即 `cmd/migrate/migrations/*_plugin.up.sql` 未跑）⇒ 服务拒绝启动**，
  错误提示 `请先执行 aigo migrate up`（fail-fast，不静默零路由）。迁移号只前进：插件迁移的文件编号必须 ≥
  当天的 max+1（低号文件在已推进到更高号的库上永不执行，`aigo migrate up` 也修不回来）。
- 插件机制台账此后另有一次**增列**：`000007_plugin_adopt` 给 `admin_plugins` 加 `adopted_through`（存量库迁移
  基线的状态事实，见 §五）。库停在该迁移之前时，安装器读写台账会报「列不存在」⇒ 同样先 `aigo migrate up`。
- 部署顺序固定为：`aigo migrate up` → `aigo plugin defaults` → 启动服务。新项目/新克隆同样照此顺序，
  不要先起服务再补迁移。`defaults` 是幂等的：默认插件已在台账且 enabled 时只打印「无变化」；
  未安装时走真实安装链（落位/接线/审计）并启用，已装未启用时只做启用。
  管理页菜单行由 `000006_plugin_admin_menu` 落地（3 个权限节点是 `000005` 的种子，由它挂到该菜单名下；
  P2 补的正是 `000005` 附注预告的那一步），故 **000005 已应用过的库只需 `aigo migrate up` 再前进一格**
  即可看到菜单入口——修正走新迁移、不改既有迁移的字节。
- 实体 spec 非法（`admin_plugin_entities.spec`）同样**拒绝启动并点名插件与实体**；`status=failed`
  的插件只留痕（日志）不注册路由，清理入口是 `aigo plugin uninstall --force <名>`。

## 二、目录约定（插件作者视角）

```
plugins/<名>/
  plugin.yaml                  # manifest：唯一元数据事实源（键集闭集，未知键拒装）
  migrations/1_init.up.sql …   # 插件自管编号（1,2,3…），走 per-plugin 版本表
  model/ service/ handler/ router/…   # 可选：建议按四层组织，直接复用 internal/ 基类
  web/views/**                 # → 复制到 web/src/views/admin/plugin/<名>/
  web/views/widget.vue         # 可选：布局级组件 → 副本同路径；宿主后台布局自动挂载
  web/lang/zh-cn.yaml、en.yaml  # → 复制到 web/src/lang/{zh-cn,en}/plugin/<名>.yaml
```

- **源是唯一事实源**：`plugins/<名>/web/**` 是源，`web/src/views/admin/plugin/<名>/**` 与语言包是**落位副本（产物）**；
  二者不一致时以源为准，用 `aigo plugin install --force <名>` 刷新副本（门禁 `check-plugin-frontend-sync.sh` 逐文件比对）。
- **布局级组件（widget）**：插件视图根目录下的 `widget.vue`（仅此文件名）由宿主
  `web/src/layouts/admin/components/pluginWidgets.vue` 在后台布局中自动挂载（eager 扫描
  `/src/views/admin/plugin/*/widget.vue`），用于浮标/浮层这类**跨页面**展示件（示例：`plugins/env-badge`）。
  挂载点不按台账筛插件：组件必须调用**自己的**插件端点决定是否显示——停用/未装时宿主运行时闸返回 404，
  组件据此静默隐藏（fail-closed）。展示件的鉴权按需自挂：面向全体已登录管理员时只挂 `AdminAuth`，
  不占权限节点（否则每个角色组都要补授）。
- **前端必须落 `web/src/views/admin/plugin/<名>/`**：菜单 `component` 由该路径查表（`import.meta.glob('/src/views/admin/**/*.vue')`），
  落 `web/src/views/plugin/` 会**静默查不到**且逃逸操作列门禁。菜单 `component` 写精确路径
  `/src/views/admin/plugin/<名>/index.vue`。
- **宿主管理面菜单行**（框架数据库迁移种子）：`admin_rules` 里 `name`/`path` = `plugin`、
  `component` = `/src/views/admin/plugin/index.vue`、`open_type='tab'`、`keepalive=1`（组件名取菜单名 `plugin`）；
  权限节点（`plugin/read`、`plugin/enable`、`plugin/disable`，000005 种子）由 000006 挂到它名下；
  详情抽屉的卸载入口另带 `plugin/uninstall` 节点（000010 补种并挂靠，同样不授予任何角色组）。
  插件按 §5.4 公式声明的分组规则名是 `plugin/<名>`（名非空 ⇒ 更长），与这行结构上不等。
- 语言包第一层命名空间天然是 `plugin.<名>.*`（由文件物理路径生成）；**宿主管理面语言包**落在
  `web/src/lang/{zh-cn,en}/plugin.yaml`，其第一层命名空间取宿主保留字（`plugin.list.*` 等）——
  插件名被保留字校验排除，两边结构上不可能相撞。
- 插件静态资源走 `static/` 磁盘兜底（根 `embed.go` 只嵌 `config/ asset/ static/images VERSION`，插件资源不随嵌入）。

## 二之二、插件包如何登记自身（L1）

`plugins/<名>/` 是模块内的顶层 Go 包（可 import `internal/**`）。它的 `init()` 只做一件事——把自身登记进
宿主注册表，并把路由挂载点交给宿主（**不要** import `internal/plugins`，那会构成 import cycle）：

```go
package demo

import (
	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"

	"github.com/gin-gonic/gin"
)

func init() {
	host.Register(host.Meta{Name: "demo", Version: "1.0.0", Kind: "mixed", Mount: mount})
}

// mount 拿到的是宿主建好的 plugin/demo 子组（已挂运行时闸）——只写相对段
func mount(g *gin.RouterGroup) {
	g.POST("/items/list", listItems)
}
```

接线（`internal/plugins/wiring.go`）在 `init()` 里：先 `l2.Mount`（按元数据注册声明式实体路由——纯声明式插件
没有 Go 包，这条路径是它唯一的接线入口），再遍历 `host.Registered()`，为每个插件建 `plugin/<名>` 子组、
挂 `host.Gate(<名>)`、再调 `Mount`，最后交 `registry.RegisterAdmin`；`register_gen.go` 是生成物，
只含空白导入（**出厂为空骨架**）。纯声明式插件（`kind: declarative`）没有 Go 代码，`Mount` 留空。

### 公开路由（MountPublic · host_api >= 1.1）

能力不只在后台、还要对外提供协议面（例：评测网关 `/eval/api/**`）时，用 `MountPublic` 登记：

```go
host.Register(host.Meta{Name: "demo", Version: "1.1.0", Kind: "code",
    MountPublic: func(g *gin.RouterGroup) { g.POST("/demo/api/ping", ping) }})
```

- **路径逐字对外**：宿主在根引擎上建组，不加 `plugin/<名>` 前缀、不加 AdminAuth/AdminLog——插件化前后
  外部客户端看到的 URL 不得变化；
- **只挂运行时闸**：`host.Gate` 与后台面同源，停用/半装一律 404（≤1s 生效）；协议级鉴权由插件自持；
- **冲突即 panic**：与核心既有公开路由同 method+path 会在启动期被 gin 拒绝——外移同一批提交必须删掉
  核心旧路由，不做静默让位；
- 声明使用本能力须把 manifest 的 `host_api` 下界提到 `>=1.1`（机制接口版本 `host.APIVersion` 已为 1.1.0）。

## 二之三、默认依赖插件（defaults）

插件模块自带一组**默认依赖插件**，正本是 `internal/plugin/host/defaults.go` 的 `DefaultPlugins`
（当前为 `env-badge`：后台环境浮标）。它们不是 `requires` 依赖边——`requires` 是插件之间的能力依赖，
v1 只校验「已装且版本相容」、**不自动安装**；默认插件是宿主对「插件模块基线」的承诺，由显式命令执行：

```bash
aigo migrate up
aigo plugin defaults        # 幂等；可加 --dry-run 预演
# 然后启动服务
```

- **分流**：台账无行 ⇒ 走真实安装链（落位 / 接线 / 审计）并启用；`installed`/`disabled` ⇒ 只启用；
  `enabled` ⇒ 无变化（不写审计）；`failed` ⇒ 报错并提示 `aigo plugin uninstall --force <名>`。
- **环境门禁同源**：无行的默认插件在非 dev 环境由 `RequireLifecycleWith` 拒绝（安装是源码树操作）；
  已安装的只做启用（启停不受生产门禁限制）。启动预检发现台账缺少默认插件时只打印提示、**不自动安装**
  （安装必须走显式命令，避免绕开生产门禁与审计）。
- **可逆**：默认插件仍是普通插件，可停用/卸载；下一次 `aigo plugin defaults` 会恢复
  （停用→启用，卸载→重装）。新增默认插件时，把 `plugins/<名>/` 源码与落位副本一起带到下游，
  否则 `defaults` 会因源码缺失明确报错。

## 三、命名、保留字与长度预算

- `name`：`^[a-z][a-z0-9-]{1,31}$`；= 目录名 = `admin_plugins.name` = 路由段 `plugin/<名>`。
- **宿主保留字**（插件名不得命中）：`render`、`list`、`get`、`read`、`enable`、`disable`、`source`、`catalog`、`uninstall`。命中即拒装。
 `read` 在册的理由：宿主管理面节点名就是 `plugin/read`（框架数据库迁移种子），插件分组规则名
  `plugin/<名>` 与它逐字相等 ⇒ 结构性防撞会失效、只剩 DB 侧查重兜底（`internal/plugin/manifest/manifest.go`
  的 `ReservedNames` 注释有完整说明）。`source`/`catalog` 在册的理由：宿主「仓库来源」与「插件清单」
  两页占用了 `plugin/source`、`plugin/catalog` 路由段与 `web/src/views/admin/plugin/{source,catalog}/` 目录
  （`/admin/plugin/source/...`、`/admin/plugin/catalog/...`），插件名撞上即页面/接口互相顶替。
  `uninstall` 在册的理由：管理面卸载端点占用 `/admin/plugin/uninstall` 路由段（详情抽屉的卸载入口；
  与 `enable`/`disable` 同为管理面动作名）。
  保留字正本是 `internal/plugin/manifest/manifest.go` 的 `ReservedNames`，与本文件说明逐字同步。
- `menus[].name` 必须以 `plugin/<名>/` 开头（结构性防撞宿主路由名；vue-router 同名是「删旧加新」，
  会**静默替换宿主页面**，DB 侧另有查重硬拦截）。
- 长度预算（安装期硬校验，按 `ddl_owner` 取更紧者）：
  - 节点名受 `admin_rules.name` 的 `varchar(64)` 约束（**所有实体**，与 DDL 归属无关）：`len(名) + len(实体) ≤ 49`；
  - `ddl_owner: engine`（缺省，引擎管结构的表）的**索引名**受 PG 标识符 63 字节约束：`len(名) + len(实体) ≤ 42 − len(表前缀)`；
    42 的来由是**截断兜底形态**的可用长度：`前缀 + 21 + len(名) + len(实体) ≤ 63`（`"idx_plugin_"` 11 + 分隔 2 + 8 位 sha256）。
    `ddl_owner: migrations` 的表由插件迁移自管结构、引擎不发索引名 ⇒ 只剩 49（推导见 `internal/plugin/manifest/budget.go`）。
  - 推荐：名 ≤16、实体 ≤24；示例插件按 名 ≤12、实体 ≤16 更宽松。

## 四、归属与清理（不靠命名约定）

- `admin_rules` 有归属列 `plugin`（空=宿主所有，部分索引 `WHERE plugin <> ''`）。插件声明的菜单/节点由
  **安装器统一代写**并打该列——**插件自带 SQL 不得写 `admin_rules`**，否则归属会漂移、卸载清不干净。
- 卸载清理面（与安装写入面逐项对齐）：`admin_rules` 中 `plugin=<名>` 的整棵子树 + 各角色组 `rules` CSV 里的
  悬挂 ID + 实体元数据行 + `configs` 的 `plugin.<名>` 分组 + 落位副本 + 注册行；**保留**表与数据、
  插件迁移版本表、`plugins/.store/<名>-*.zip`。
- `--purge` 只能与**同一次** `uninstall` 一起执行（先回滚插件迁移、后删文件）；`--keep-files` 只清 DB 侧与
  注册行、留文件（源码入库的仓里这就是「回到出厂态」）。**`adopted_through > 0`（存量库迁移基线装入）的插件
  `--purge` 默认拒绝**，放行需 `--purge --yes --purge-adopted`，**且已落位 `plugin.yaml` 的 `adopt` 段必须可读**
  （读不到即不放行，fail-closed——放行的承诺就是「执行前打印将 drop 的对象面」，见 §五）。
- **出厂登记态（硬约定）**：仓里存在 `plugins/<名>/` 文件 ≠ 已安装。未安装 = 文件在、`register_gen.go`
  为空骨架、`admin_rules` 无该插件行、无迁移记录——此时插件对运行中的后台**零可见影响**。

## 五、迁移与数据面边界

- 插件的 L1 迁移**只放插件目录内**（`plugins/<名>/migrations/`），编号插件的独立自增 `1,2,3…`，
  走每插件一张版本表 `<前缀>plugin_<名>_migrations`；**不得**放进 `cmd/migrate/migrations/`
  （全局游标只负责宿主自己的表；跨仓「rename 到 max+1、内容零改动」约定只作用于宿主迁移）。
- 事务语义与宿主一致：整文件一次执行、**单个隐式事务**，因此同样**禁 `CREATE INDEX CONCURRENTLY`**；
  大表加索引走「低峰手工 CONCURRENTLY + 迁移 `IF NOT EXISTS` 对齐」。
- L2 实体表的 DDL **不写迁移文件**，由声明 diff 生成；`--purge` 是唯一删表路径（界面不提供硬删）。
- 数据回填（UPDATE/backfill）只能写迁移文件（`kind: mixed`），引擎不做 backfill。

### 存量库迁移基线（manifest 的 `adopt` 段）

插件**后装**于「核心链已经建过表」的库时，版本表（`<前缀>plugin_<名>_migrations`）与全局游标（`schema_migrations`）
互不对账 ⇒ 默认路径只有一条：从空版本表全量重放插件链（`ALTER`/回填/菜单种子会二次应用）。`adopt` 段用
「声明 + 探针」把这条路径换成三态判定：

```yaml
adopt:
  through: 37          # 接管边界：插件迁移 1..through 与核心链对应迁移「同物」
  probes:              # 逐版本存在性探针；**每个版本都要显式覆盖**（对象探针或 kind: none）
    - {version: 1, kind: table, name: eval_sets}          # 一律写不含表前缀的逻辑名（前缀由安装器按 kind 拼）
    - {version: 2, kind: none}                            # 纯菜单/文案/回填版本：显式免探
    - {version: 30, kind: column, table: eval_runs, name: title}
```

- **前缀口径（按 kind 分，别记成「一律不带前缀」）**：manifest 里**一律写不含表前缀的逻辑名**，前缀由安装器拼
  ——拼不拼**按 kind 分**：`table`/`index` 的对象名拼前缀；`constraint` 的**约束名也拼**（迁移纪律要求表名/
  索引名/**约束名**都带 `__PREFIX__`；未显式命名的内联约束由 PG 按**物理表名**起名 ⇒ 同样含前缀）；
  **`column` 的列名不拼**（列名从不带前缀，只有被查的**表**拼）。两仓当前表前缀为空 ⇒ 看不出差别，但前缀非空时
  拼错即错判（列探针恒不命中 ⇒ 存量库被判「干净库」⇒ 全量重放旧迁移——正是本机制要防的方向）。
- **kind 闭集**：`table` | `column` | `index` | `constraint` | `none`；`column`/`constraint` 必须带 `table`
  （逻辑表名），`table`/`index` 不得带；探针只断言**存在性**（不校验类型/行数）。
- **装前硬校验**（不满足即拒装；纯文件对账，不连库）：`through` = 插件迁移的**最大编号**且 `1..through` 连续成对；
  每个版本都被 `probes` 覆盖（防漏）；每条对象探针的对象名必须出现在**该版本**的 up 文件里（防虚）；
  **含 DDL 对象创建的版本必须至少一条对象探针**——判据 = 该版本 up 文件的语句事实（`scanSQL` 词法扫描，跳注释/
  字符串/美元引用块），形态闭集 = `CREATE TABLE` / `CREATE [UNIQUE] INDEX` / `ALTER TABLE … ADD`；
  **`kind: none` 只允许纯菜单 / 文案 / 回填版本**（up 文件里没有上述形态，如 `INSERT`/`UPDATE`/`COMMENT ON`/
  `ALTER COLUMN SET DEFAULT`）。判定在 `verifyAdopt`：安装计划（②b 段）与 `aigo plugin verify` 各走一遍。
- **安装期三态判定**（只读：版本表状态 + 探针，且**不构造 migrator**——构造即建版本表）：
  - 探针**全命中** ⇒ `baseline`：只把版本表写到 `through`（**迁移 SQL 0 条**，`Applied` 留空 ⇒ 补偿不做 down 回滚）；
  - **全不命中** ⇒ 全量：干净库/下游产品形态，按编号顺序应用插件链（既往路径不变）；
  - **部分命中** ⇒ **拒装**（fail-closed，先于任何落盘/写库）：先 `aigo migrate up` 把核心链推到位再重装，
    或人工确认后 `aigo plugin migrate-force <名> <版本>` 归位版本表。
- **dry-run**：③b 段逐条打印探针（`✓/✗` + 物理名）与将走的分支；baseline 分支写明「迁移 SQL 0 条 / 版本表写 1 行」。
  ③b 摘要的「版本表新建」取值 = 现场**没有**版本表（⇒ 本次安装一定会建它：申报 `adopt` 必有迁移目录 ⇒ 执行侧必
  构造迁移器，而驱动构造即建版本表）——与预演段的「版本表 …：不存在/已存在」同源派生。
- **台账与审计**：装成后 `admin_plugins.adopted_through` 记接管边界（框架迁移 `000007_plugin_adopt`），
  `aigo plugin list` 的「接管」列可见；安装审计 `detail.baseline` 记 mode/through/探针计数/缺失项。
- **卸载**：默认（不 `--purge`）表与数据、版本表一律保留，复装时按同一判定走（幂等）；管理页卸载对话框的
  「彻底删除」勾选即 `--purge`（adopt 装入时界面禁选，放行仅走 CLI 三重显式）；`adopted_through > 0`
  时 `--purge` **默认拒绝**——down 文件是核心链迁移的副本，回滚删的是**核心链建的表与数据**，而核心游标仍记
  这些迁移已应用（`aigo migrate up` 不会重建）。放行需三重显式：`--purge --yes --purge-adopted`
  （执行前打印将回滚/将 drop 的对象面与恢复路径）；**且放行清单必须可读**——对象面只能从**已落位 `plugin.yaml`
  的 `adopt` 段**读出，清单读不到（`plugins/<名>/` 不在、manifest 缺失/读不动、或没有 `adopt` 段）时**不放行**
  （fail-closed；恢复清单后重跑，或放弃 `--purge` 走普通卸载）。`--purge-adopted` 后重装时探针全不命中 ⇒ 走全量分支 ⇒ 结构可重建。
- **边界**：`1..through` 只对 `kind: code`/`mixed`（L1）有意义——L2 实体表名由公式唯一确定，不可能接管核心既有表；
  「幂等复制」形态继续有效，两者可共存（同一插件二选一）。

## 六、开发期与热重载

- 后端：`.air.toml` 的 `include_dir` 须含 `plugins`（本项目已含；缺失时 `plugins/` 下的改动
  **不触发** air 重编译）；改了插件 Go 代码后需**重编译 + 重启**才生效（接线真相在编译产物）。
- 前端：dev 下 HMR 直接生效；改动落位后的副本要入库前走 `pnpm build`。
- 装配时机：插件前端页随宿主前端一起构建发布，**没有独立的插件前端产物**；L2 新增实体需要重启进程
  （路由在启动时注册），字段/文案变更即时生效（元数据驱动）。

## 七、生效、状态与运行时闸

- **三处真相**：① 进程内注册表（编译期决定进程认识哪些插件）；② `admin_plugins.status`（期望状态）；
  ③ 注册文件。三者不一致时由 `aigo plugin list` 与管理页显式报告差异（如台账 `enabled` 而进程没有该插件
  ⇒ 标注「待重编译」，路由 404）。**不做静默对齐**；`aigo plugin sync [名…]` 以台账重放注册生成物对账
  （`--dry-run` 只预演，不写审计——动作域六项闭集，sync 属生成物对账而非生命周期动作）。
- 生命周期四态：`failed`（半装，只允许 `uninstall --force`）→ `installed`（菜单隐藏）→ `enabled`（唯一生效态）
  → `disabled`。**先禁用后卸载**是硬门禁。管理页**详情抽屉**提供 停用/启用 与 卸载 入口（同一 cli 写路径与审计；
  卸载对话框的「彻底删除」勾选即 `--purge`，adopt 装入时界面禁选；生产环境按环境门禁置灰、后端仍会拒）。
- **运行时闸**：插件路由挂在 `plugin/<名>` 子组上，闸按请求读 `admin_plugins.status`（默认 ≤1s 缓存），
  非 `enabled` 一律 **404**（不承认存在性、不进业务 handler）——所以 `enable` / `disable` 无需重启，
  最迟一个缓存周期生效。缓存上限可配：宿主内默认值 + `host.SetGateTTL` 扩展点（接线侧启动时调用）。
- **环境门禁（安装 / 卸载）**：生产环境禁止插件安装与卸载，开发环境允许；插件页面（插件管理 / 插件清单）
  顶部显示环境标识。判定单一实现 = `internal/plugin/cli/environment.go`：
  ① 显式配置 `plugin.env = dev | prod`（可选键，环境变量 `PLUGIN_ENV` 同效）⇒ 以配置为准——生产环境边界
     由运维钉死，不依赖「本机碰巧有源码检出」这一物理条件；
  ② 未显式配置 ⇒ 自动判定：能定位源码根（`plugin.root`，或向上找到含 `go.mod` 的目录）⇒ `dev`，否则 `prod`
     （v1 的安装/卸载就是源码树操作——落位文件 + 改接线生成物 + 需重编译——没有源码根时本也执行不了）；
  ③ 显式值认不出（非 dev/prod）⇒ 按 `prod` 处置（fail-closed），原始值随接口的 `env.configured` 回报，便于自查笔误。
  门禁落点：HTTP 清单通道第一道（不下载、不暂存；error_code=`lifecycle_forbidden` ⇒ 403）+ 管理面卸载端点
  （`/admin/plugin/uninstall`，先于参数校验）+ 写路径（`runInstall` / `Uninstall`，后者连 `--force` 也不放行）。
  **留痕口径**：只有 CLI 的 `Uninstall` 被拒会写 `status=rejected` 审计行；HTTP 侧的预门禁
  （handler 层，先于 service/CLI 执行）与安装门禁（在任何取库/写审计之前返回）都不落行——被拒事实进
  服务日志与返回文案，不进 `admin_plugin_actions`。`--dry-run` 是只读预演，不受此限。管理面读数：`/admin/plugin/list`、
  `/admin/plugin/get`、`/admin/plugin/catalog/list` 与 `/admin/plugin/source/list` 响应的 `env` 段
  （`{name, source, configured?, lifecycle_allowed}`），页面据此渲染标识并置灰安装/卸载入口。
- **`plugin.root` 是什么**（可选键，环境变量 `PLUGIN_ROOT` 同效）：它就是插件的**仓库根**——`install`/`uninstall`/
  `scaffold`/`export`/`sync`/`defaults` 的**写与删根**（`Env.Root`，落位文件、`plugins/.store` 包、
  `internal/plugins/register_gen.go` 都在它下面），**相对路径参数也以它为基**。优先级：`plugin.root` 命中
  （目录内有普通文件 `go.mod`）就用它，否则从当前目录向上找最近的 `go.mod`；环境判定用的是同一个事实
  （这是「页面标识与实际能力不矛盾」的结构保证，见上一条）。
  **归现有意只做「可见」而不做「拦截」**，所以边界要说清（都是实测过的，不要当承诺读）：
  ① 判据只看「目录里有普通文件 `go.mod`」，不校验模块身份——因此**同名模块的另一份检出不会被区分**
  （本机就有 5 个检出都叫 `github.com/ai-go-hub/ai-go-admin`，指向哪一个都不会有提示）；
 ② 根被接受、且模块名与本项目不同时，构造命令环境会在 **stderr** 打一行归属提示（写/删根 + 模块名，
  不污染 `--json` 的 stdout）；
  ③ 提示**不代表目标仓装得上**：落位写出的注册行固定是 `github.com/ai-go-hub/ai-go-admin/plugins/<名>`
  这一条导入路径，本工具不写 `go.mod`、也不补 `require`/`replace`——目标仓要能编译，得自己提供该模块依赖；
  ④ `plugin.root` 配了但判据不认（路径不存在 / 无 `go.mod` / `go.mod` 是目录）时，会**静默**回退
  「从当前目录向上找 `go.mod`」，没有额外提示——「配了没生效」要靠命令输出里的实际路径自查。
  配错方向与后果：多检出机器上误配/误导出 `PLUGIN_ROOT`、或从兄弟仓目录里跑 CLI，都会把插件文件与注册行
  写进、并在卸载时从**那个**仓删除——看到归属提示、或看到输出里的根不是预期的仓，就要停下来核目标仓。

## 八、兼容域与 `host.APIVersion` 递增约定

- manifest 的 `host_api` 是**机制接口版本域**（如 `>=1 <2`），安装时对宿主常量 `host.APIVersion` 校验
  （`host.Compatible`）；域语法支持 `>= > <= < =` 与裸版本，不支持 `||` / `^` / `~` / 预发布标签（拒解析）。
- **维护约定（必须遵守）**：`internal/plugin/host` 出现**破坏性变更**（路由挂载形态、注册表契约、闸语义、
  manifest 字段语义、元数据表结构）时，**必须递增 `host.APIVersion`**，否则旧插件会被静默放行、兼容域形同虚设。
- `framework_req`（框架线版本域，对 `version.Get().Version`）**只记录 + 告警、不拒装**：`VERSION` 属「各仓保留」、
  下游项目是产品版本线，用它做拒装依据会无据拒装或无据放行。

## 九、信任等级（必读）

`kind: code`（L1）的插件**等价于把第三方 Go/Vue 代码 `git merge` 进仓库**：与宿主同权限、无隔离层、无运行时隔离。
v1 的缓解只到「让动作可见、可审计、可撤销」：`--dry-run` 预演、源码独立目录（`git status` 直接可见）、
单文件注册机床、一键 `uninstall`、全动作审计（`admin_plugin_actions`，CLI 与管理页同一份留痕）。
`--operator` 只是署名，不是身份认证——真正的边界在 OS/部署侧访问控制。

## 十、门禁与自检

- 插件面无 DB 的纯静态检查可通过 `go run ./cmd plugin verify` 与 `go test ./internal/plugin/...` 执行；**`plugins/` 为空或不存在必须全绿**（空态零影响）。
  其中前端落位副本**按 manifest 的 `web` 清单展开**逐文件 sha256 比对（不是硬编码 views/lang 两目录）。
- `web/src/views/admin/plugin/**` 自动进操作列门禁（操作列必须 `render:'buttons'`）；
  check-size 自动覆盖新目录（代码 ≤1024 行、文档 ≤512 行）。
- `PLUGIN_DB=1` 的库级用例在 `internal/plugin/...`：迁移 up/down/up、启动预检的两种失败态、闸状态、
  安装/卸载/promote/sync 全链。`host` 包用例**自带 up**（把框架迁移推进到插件迁移版本，只前进不回滚），
  所以库级用例不依赖先手工 `aigo migrate up`。
- **PLUGIN_DB 纱门口径（验收必查）**：不带 `PLUGIN_DB` 时库级用例一律 `t.Skip` 而命令仍返回 0 ⇒「绿」可能
  什么都没跑。验收 = 下方 `PLUGIN_DB=1` 命令的输出里**不得有 SKIP**（`grep -c 'SKIP'` 与 `-json` 的
  `Action=skip` 计数都应为 0）；出现任何跳过项都代表该面未被验证，不得当作通过。

### 库级用例的第二套门控：`CATALOG_TEST_DSN`（清单快照的查询语义）

`internal/plugin/source/store_db_test.go` 的用例验证**查询语义**与快照事务边界——列举的过滤条件
（来源 / uid / 版本 / 关键字）是否真的进 SQL、来源行与快照行是否同生同死。这类断言只能落在**真库的
返回行数**上：内存假库整个忽略查询体，`DryRun`/`ToSQL` 也看不到「预挂在 *gorm.DB 上的条件被泛型入口
的 NewDB 会话丢弃」这一层（同一条件在 DryRun 下反而带 WHERE）。

**它不认 `PLUGIN_DB`**：那套门控连的是配置里的开发库，本项目开发库有已装插件与实体数据，库级用例不得指向它。
本组用例只认显式给定的**临时库** DSN；未设 DSN ⇒ 整组 `t.Skip`（默认 `go test ./...` 不连库、不需 PG）。

```bash
# ① 建一次性临时库（名字任取，跑完即删；建议带 test/repro 字样，避免误连开发库）
#    口令走环境变量：createdb/dropdb 没有 --password 旗标，缺 PGPASSWORD（或 ~/.pgpass）时会卡在交互提示
export PGPASSWORD='<口令>'
createdb -h <主机> -p <端口> -U <用户> aigo_catalog_test

# ② 用 DSN 指向它跑这组用例（用例自带 AutoMigrate 两张表 + 逐用例 TRUNCATE）
#    口令若含 URL 保留字符（@ : / ? # [ ] % & + =）必须先做 percent-encode 再拼进 DSN，
#    否则会被当成 URL 分隔符解析失败
CATALOG_TEST_DSN='postgres://<用户>:<口令>@<主机>:<端口>/aigo_catalog_test?sslmode=disable' \
  go test -count=1 -run TestStore -v ./internal/plugin/source/

# ③ 跑完删库（临时库不留档）+ 清口令变量
dropdb -h <主机> -p <端口> -U <用户> aigo_catalog_test
unset PGPASSWORD
```

- **验收口径**：不设 DSN 时这些用例是 `--- SKIP`（`go test` 照样退出 0）⇒ 想拿它当验收依据，必须看
  输出里是 `--- PASS` 而不是 `--- SKIP`；`-run TestStore` 一次覆盖快照生命周期与两处列举过滤。
- DSN 指向的库需要**建表/写权限**（`AutoMigrate` + `TRUNCATE`）。**绝不指向开发库/生产库**——只认显式
  DSN 就是为了让「顺手连上开发库」不可能发生。

常用自检：

```bash
go build ./...
go test -count=1 -timeout=60s ./internal/plugin/...
PLUGIN_DB=1 go test -count=1 -timeout=60s ./internal/plugin/...   # 连真实库；验收须确认输出无 SKIP
CATALOG_TEST_DSN='postgres://…/临时库' go test -count=1 -run TestStore -v ./internal/plugin/source/   # 见上节
```
