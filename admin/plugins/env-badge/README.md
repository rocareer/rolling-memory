<!-- plugin: env-badge v1.0.0 -->
# env-badge（环境检测浮标）

后台布局右下角显示 **DEV / PROD**（英文），鼠标悬停显示判定依据与插件生命周期状态。

## 环境判定

复用宿主唯一权威 `internal/plugin/cli/environment.go`，不另立口径：

1. 显式配置 `plugin.env=dev|prod`（环境变量 `PLUGIN_ENV` 同样生效）⇒ 以配置为准；
2. 未配置 ⇒ 按「源码检出是否可用」自动判定（检出 ⇒ `dev`，否则 `prod`）；
3. 显式值认不出（非 `dev|prod`）⇒ 按 `prod` 处置，tooltip 如实标注。

## 组成

| 面 | 位置 |
|---|---|
| 数据端点 | `POST /admin/plugin/env-badge/info`（只挂 `AdminAuth`，不占权限节点） |
| 布局组件 | `web/views/widget.vue` → 落位副本 `web/src/views/admin/plugin/env-badge/widget.vue` |
| 宿主挂载点 | `web/src/layouts/admin/components/pluginWidgets.vue`（扫描 `.../plugin/*/widget.vue`） |

浮标对全体已登录管理员可见；插件停用/未装时宿主运行时闸返回 404，组件静默隐藏（fail-closed）。

## 默认依赖

本插件是插件模块的**默认依赖插件**（正本 `internal/plugin/host/defaults.go` 的 `DefaultPlugins`）：
部署时在 `aigo migrate up` 之后执行 `aigo plugin defaults`，命令会确保本插件已安装并启用。
仍可正常停用/卸载，下一次 `aigo plugin defaults` 会恢复。
