# 示例

**[简体中文](README.zh-CN.md) · [English](README.md)**

这些文件是可复制的接线样例，不是运行时依赖。

**首选一键接入**（自动写各家 skill 与 MCP 注册，带备份、幂等与冲突保护；矩阵与判据见
[docs/zh-CN/harness.md](../docs/zh-CN/harness.md)）：

```bash
rolling-memory install --list        # 查看各家宿主落点与探测结果
rolling-memory install --dry-run     # 预演变更（不修改文件）
rolling-memory install --with-hooks  # 实装（skill + MCP + UserPromptSubmit 钩子）
rolling-memory install --check       # 核验：静态注册 + 实时拉起 MCP 握手
```

`install` 生成的启动器会写入二进制绝对路径（POSIX `rolling-memory.sh` / Windows
`rolling-memory.cmd`），请先通过 `bash install.sh` 安装二进制（或从源码编译 `bin/rolling-memory`）。

下面的样例是**手工等价物**——需要手动配置或接入不在内置注册表里的客户端时可直接参考。

## MCP 与 Hook 配置样例

- [mcp-config.json](mcp-config.json) — 通用 stdio MCP 配置（将 `command` 替换为二进制绝对路径或 `install` 生成的启动器）。
- [zcode-plugin/](zcode-plugin/) — ZCode 插件壳：`mcpServers.rolling-memory` + `UserPromptSubmit` 钩子。
- [claude-code/settings.json](claude-code/settings.json) — Claude Code 的 MCP + `UserPromptSubmit` 钩子配置。
- [dsh-plugin/package.json](dsh-plugin/package.json) — DeepSeek Harness (`dsh`) 插件包配置样例。

手动接线前，先初始化本地配置与 SQLite 明文金库：

```bash
rolling-memory init --write-config
rolling-memory status
```

ZCode 插件：将 `zcode-plugin/` 复制到插件目录（或注册为本地 marketplace 条目），启用后确认
`rolling-memory` MCP server 出现，且 `UserPromptSubmit` 钩子可正常触发。插件内的 `db` / `scope`
为用户配置项，安装后可在插件设置中修改。

## 本地同步、体检与 Web 控制台

```bash
rolling-memory sync status           # 查看 L1/L2/L3 本地金库与宿主对话同步状态
rolling-memory sync reconcile        # 立即导入宿主对话并增量同步已挂载的 L1 本地目录
rolling-memory doctor --json         # 校验本地模式、文件权限与数据库健康度
rolling-memory panel                 # 启动并打开本地 Web 控制台 (http://localhost:8008/#/admin)
```
