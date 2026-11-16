package host

import "slices"

// DefaultPlugins 插件模块的默认依赖插件（宿主基线；唯一正本）。
//
// 语义：这些插件随插件模块一起分发，是「插件模块基线」的一部分——开发/部署在
// `aigo migrate up` 之后、启动服务之前执行 `aigo plugin defaults`，由它确保已安装并启用。
//
// 与 manifest.requires 的区别（不要混用）：
//   - requires 是**插件之间**的能力依赖，v1 只校验「已装且版本相容」，不自动安装；
//   - 本清单是宿主对**模块基线**的默认承诺（例：env-badge 是后台环境浮标，属于展示件，
//     不是任何插件的能力前置），由 CLI 的显式命令执行，不改变 requires 的语义。
//
// 执行纪律：安装/启用仍走 cli 的真实生命周期（环境门禁、先禁后卸、审计、落位/接线一条不少），
// 本清单只提供名字；版本/来源以 plugins/<名>/plugin.yaml 为唯一事实源。默认插件仍可停用/卸载，
// 下一次 `aigo plugin defaults` 会恢复（停用→启用，卸载→重装）。
//
// 下游同步：本清单随 internal/plugin/host 下发；新增条目时同步把插件源码（plugins/<名>/）与
// 落位副本一起带到下游，否则 `defaults` 会因源码缺失明确报错。
var DefaultPlugins = []string{"env-badge"}

// IsDefaultPlugin 名称是否在默认依赖插件清单里。
func IsDefaultPlugin(name string) bool {
	return slices.Contains(DefaultPlugins, name)
}
