// Package host 是插件机制的宿主基座：机制接口版本、进程内注册表、运行时闸、启动预检与元数据装载。
//
// 包分工（设计说明三层分工）：
//   - 本包（host）：注册表与闸——插件包 init() 调 Register 登记，运行时由 Gate 按请求读库放行；
//     启动预检（Preflight）与元数据装载也在本包，供路由注册与诊断复用；
//   - internal/plugins：接线——register_gen.go（生成物，空白导入各插件包）+ wiring.go（手写 init：
//     遍历本包注册表，为每个插件建 plugin/<名> 子组并挂闸，再交 registry.RegisterAdmin）；
//   - plugins/<名>：插件包，只 import 本包，不 import internal/plugins（否则 Go 非法 import cycle）。
//
// 三处真相：① 进程内注册表（编译期决定接线）；② admin_plugins.status（运行态期望）；
// ③ 「台账 vs 进程内 vs 注册文件」的差异由 CLI 与管理页显式报告，不做静默对齐。
package host

// APIVersion 机制接口版本：manifest 的 host_api 域对本常量校验（见 Compatible）。
// 与产品版本 version.Get().Version 无关——后者属「各仓保留」、两仓版本线独立，只能作提示；
// 本常量随 internal/plugin/host 在两仓同源，才是可校验的域。
//
// 维护约定：机制接口出现破坏性变更（路由挂载形态、注册表契约、闸语义、
// manifest 字段语义、元数据表结构）时必须递增本常量，否则旧插件会被静默放行。
// 1.1.0：新增可选公开路由挂载点 Meta.MountPublic（根级 + 运行时闸）。
// 属加性扩展：不写 MountPublic 的插件行为不变；声明使用公开面的插件须把 host_api 下界提到 >=1.1。
const APIVersion = "1.1.0"
