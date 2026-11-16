<!-- plugin: demo v1.0.0 -->
# 示例插件 `demo`

本目录是**示例插件**，同时覆盖三种形态、充当验收靶子与 `aigo plugin scaffold` 的输出样板
机制与纪律见 `../README.md`。

| 面 | 落点 | 说明 |
|---|---|---|
| L2 实体 | `plugin.yaml` 的 `entities: items` | 表结构由 L2 引擎按 spec 建（`ddl_owner: engine`）：安装编排的 ④b 生成并逐条执行 `CREATE TABLE IF NOT EXISTS` + 索引一次到位 + `COMMENT ON`（`internal/plugin/cli/l2ddl.go`）；复装/升级以**表结构自省**为基线补齐新列；`title` / `status`(0/1) / `code`(部分唯一) / `weigh`(拖动排序) / 软删列；六动作路由 `plugin/demo/items/{list,get,create,update,delete,sort}` 由引擎在启动时注册（§5.2/§5.4）；`--purge` 卸载时按同一归属 drop 表（ddl_owner=engine 才 drop） |
| L1 自定义页 | `plugin.go` + `web/views/index.vue` | `init()` 登记进宿主注册表，`mount()` 在已挂闸的 `plugin/demo` 子组上挂 `POST /overview`（**并自挂 `AdminAuth` + `AdminPermission`**，见下节）；页面读插件自身的四项事实（§4.2/§4.5） |
| 插件迁移 | `migrations/1_init.{up,down}.sql` | 插件**自管**表 `<前缀>plugin_demo_notes`（含一行种子）：编号插件内自增、独立版本表、`--purge` 才按 down 回滚（§4.3） |
| 菜单/节点 | `plugin.yaml` 的 `menus:` **只声明 L1 部分** | 分组目录 + L1 页 + L1 节点；**L2 实体页与权限节点由安装器按 §5.4 公式代写**（`plugin/demo/items` + `{read,create,update,delete,sort}`，`component` = 宿主通用页 `render/index.vue`）。全部行由安装器代写进 `admin_rules` 并打归属列 `plugin=demo`（§4.1/§5.4） |

## 插件路由的中间件纪律（作者必读）

宿主交给插件的是**已挂运行时闸**的 `plugin/<名>` 子组：**闸**只回答「插件是否 enabled」，
**不回答「这个管理员能不能用」**。因此每条后台路由都必须自己挂
`middleware.AdminAuth()` + `middleware.AdminPermission()`（与宿主 `internal/handler/base.go:17-25`、
L2 引擎 `internal/plugin/l2/router.go:100-104` 的每个动作同款）：

- 不挂 ⇒ manifest 里声明的节点只是装饰（任何持有任意有效后台会话的人都能打到该端点）；
- 挂上后的语义：未登录 401、无该节点 403、有节点放行（认证失败走「HTTP 200 + 业务码 401/403」，只有运行时闸是 HTTP 404）；
- 节点名必须与路由 FullPath 经 `BuildCheckPath` 的推导结果逐字相等（`list`/`get` 兼查同级的 `read`），
  否则非超管全 403。

样例见本目录 `plugin.go` 的 `mount`；验收用例对这条纪律做了源码形态断言 + 401/403/放行三种真实中间件探针。

## 出厂未安装

文件在仓 ≠ 已安装。出厂态 = 本目录在仓 + `internal/plugins/register_gen.go` 为空骨架 +
`admin_rules` 无本插件行 + 无迁移记录（§9）。回到该态：

```bash
aigo plugin uninstall --keep-files demo
```

## 两端长度预算的自证

`len(demo)=4`、实体 `items=5` ⇒ 联合长度 9，远低于 `admin_rules.name` 的 49 与引擎索引名的
`42 − len(表前缀)`（§5.3/§5.4）；节点名最长 `plugin/demo/items/create` = 23。

## 落地边界（如实登记）

- **L2 实体页菜单/节点**：由**安装器按 §5.4 公式代写**（`internal/plugin/cli/ruleformula.go` 的
  `BuildInstallMenus`）⇒ 本插件的 `menus:` 里**不再声明**那一段（重复声明会被同名行断言抓到；
  手写同名行时 path/component/pid/节点动作必须与公式逐字一致，否则拒装）。
- **L2 实体表的 DDL 执行**：落在安装编排的 **④b**（`internal/plugin/cli/l2ddl.go`：④ 之后、⑤ 落位之前
  ——§5.3 明列「数据回填只能写迁移文件」，⑥ 的插件迁移可能回填 L2 表，故建表必须先于 ⑥）；
  `--dry-run` 的 ⑤ 段逐条列出同一计划；引擎自管实体存在而库不可读时 dry-run **fail-closed**。
- L1 前端文件随宿主前端一起构建发布（无独立插件产物）；L2 新增实体需重启进程（路由在启动时注册），
  字段/文案变更即时生效（元数据驱动）。
