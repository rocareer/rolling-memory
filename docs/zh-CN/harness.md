# 多宿主一键接入指南（Multi-Harness Integration）

**[简体中文](harness.md) · [English](../harness.md)**

> 本文件描述 `rolling-memory install` 的多宿主（Harness）适配架构、注册表落点矩阵、启动器机制与验收判据。

## 1. 设计目标

Rolling Memory 的机械记忆核本身与具体 Coding Agent 宿主完全解耦；不同宿主之间的差异仅在于四项配置落点：**技能根目录（Skill Root）**、**MCP 服务注册文件**、**生命周期 Hooks 事件**与**全局/项目级指令文件**。

`rolling-memory install` 将「四项配置面 × 多家主流宿主」收敛为统一注册表，实现一条命令完成备份、幂等写入与实时 MCP 握手自检（`install --check`）。

---

## 2. 行业接入形态对比

| 接入形态 | 典型做法 | 维护与使用成本 |
|---|---|---|
| **仅提供 MCP + 手工贴配置** | 提供 stdio MCP server，由用户自行查找各宿主的 JSON / TOML 路径并手写配置片段 | 用户需自行处理路径差异、转义格式与 PATH 裁剪问题；无自动备份、无冲突保护 |
| **单一宿主专用插件** | 绑定单一 IDE 或 CLI 的专有扩展接口与专有命令格式 | 跨工具切换时记忆与习惯割裂，无法在多款 Agent 之间共用同一本地记忆库 |
| **`install` 统一注册表 + 原生插件双轨（本项目）** | 通用 stdio MCP（单工具 `recall` · 回忆）+ `install` 自动适配多宿主配置 + 原生插件自动去重 + `install --check` 真握手核验 | 一条命令接入全机宿主，共享同一个本地 SQLite 明文金库，装完即刻校验 |

---

## 3. 分层契约

```text
Harness Agent + Skill        ← 语义理解层：依据技能（SKILL.md）与指令文件按需调用 recall（回忆即记忆）
        │  MCP tools/call
rolling-memory install       ← 宿主适配层：注册表 + 保守写手（JSON / TOML / YAML patch）+ 握手核验
        │  写入启动器与注册项
MCP stdio · CLI · Hooks      ← 机械记忆核：零宿主耦合，标准进程通信契约
        │
SQLite 明文金库 (WAL + FTS5)  ← 本地存储层：本地明文金库
```

- **机械核（`api` / `mcp` / `cli` / `hooks`）零宿主耦合**：不依赖任何特定宿主 SDK，仅通过标准进程协议通信（MCP 为逐行 JSON-RPC 2.0，CLI 为 argv/stdin，Hooks 为 JSON stdin → JSON stdout）。
- **适配层（`internal/harness`）只做保守接线**：负责将启动器路径写入宿主配置文件、同步 `SKILL.md`、按需挂载 Hooks 与指令块，不介入任何记忆语义。
- **提醒型钩子 · 检索 100% 由 Agent 驱动**：可选的逐轮钩子（`--with-hooks`）**绝不自动检索记忆**——`recall` 始终由 Agent 自己调用；钩子只负责保持本地捕获侧新鲜（对话轮次与已挂载的背景目录），并每隔数轮提醒 Agent `recall` 可用。
- **收口 `recall` 自评与自定义 `SKILL.md`**：`SKILL.md` 最末尾设有独立可选区块（`§5 收口如实评价`），约定 Agent 在本轮调用过 `recall` 时，于最终回复收尾附一行简短复盘（报告本轮调用次数、预计节省的盲搜/排障往返及具体依据、以及召回内容是否真正有帮助）。用户可随时打开已安装的 `<技能根>/rolling-memory/SKILL.md` 直接修改或删去末尾这一区块（或在 `rolling-memory install` 时传入 `--skill-file <路径>`）。

---

## 4. 支持的宿主矩阵

### 4.1 默认自动探测宿主（5 家）

| 宿主 | 技能根（用户级） | MCP 注册位置 | 生命周期 Hooks | 指令文件（用户级 / 项目级） |
|---|---|---|---|---|
| **ZCode** | `~/.zcode/skills/` | `~/.zcode/cli/config.json` → `mcp.servers` | ✅ `hooks.events`（需 `hooks.enabled=true`） | `~/.zcode/AGENTS.md` / `AGENTS.md` |
| **Claude Code** | `~/.claude/skills/` | `~/.claude.json` → `mcpServers`（项目级 `.mcp.json`） | ✅ `~/.claude/settings.json` → `hooks` | `~/.claude/CLAUDE.md` / `CLAUDE.md` |
| **Codex** | `~/.codex/skills/` | `~/.codex/config.toml` → `[mcp_servers.<name>]` | —（由 Skill 驱动） | `~/.codex/AGENTS.md` / `AGENTS.md` |
| **DeepSeek Harness (`dsh`)** | `~/.dsh/skills/`（`$DSH_HOME`） | `~/.dsh/profiles/<profile>/cordis.patch.yml` → `dsh-mcp-client` | —（由 Skill 驱动） | `~/.dsh/AGENTS.md` / `AGENTS.md` |
| **CodeBuddy** | `~/.codebuddy/skills/` | `~/.codebuddy/mcp.json` → `mcpServers` | —（由 Skill 驱动） | — / `CODEBUDDY.md` |

### 4.2 显式扩展宿主（5 家）与原生插件去重

- **扩展宿主**：可通过 `rolling-memory install <id>` 显式指定接入 **Cursor**（`cursor` → `~/.cursor/mcp.json`）、**Windsurf**（`windsurf` → `~/.codeium/windsurf/mcp_config.json`）与 **Cline / Roo Code**（`cline` → `~/.cline/mcp_settings.json`），**WorkBuddy**（`workbuddy` → `~/.workbuddy/mcp.json`，即 WorkBuddy 自身连接器文档给出的路径；带点 `~/.workbuddy/.mcp.json` 作为旧宿主版本的兼容载体一并写入；技能根 `~/.workbuddy/skills`，无会话 Hooks；在 WorkBuddy 插件市场安装本插件即可同时接好技能与 MCP 服务，无需命令行步骤），以及 **豆包**（`doubao` → 双技能根 `~/Doubao/skills` 与 `~/.agents/skills`，即客户端自报的 `local_skill_paths`；本地技能收集器把两处扫入其 `# Skill Catalog`，并把其中的 USER skill 上传到账号做跨设备同步。MCP 连接器由豆包 manifest 下发投影、本地命令受 `mcp_command_path_policy` 约束（不接受任意绝对路径或包装脚本），故 `install` 只写技能并如实报 `skipped`；工具面走**地址式连接器**（`rolling-memory mcp --http` 起本地 URL，把地址填进 App，见 §4.4）。
- **原生插件双路去重**：当检测到 ZCode 或 Claude Code 已启用自包含原生插件（`.zcode-plugin` / `.claude-plugin`）时，`install` 会自动清理全局配置文件中的重复挂载，收敛为单一路径。

### 4.3 三类安全配置文件写手

| 格式 | 适用宿主 | 写入与校验策略 |
|---|---|---|
| **JSON 深合并** | ZCode / Claude Code / CodeBuddy / Cursor / Windsurf / Cline / WorkBuddy | 解析既有 JSON → 沿键路径更新目标条目 → 其余配置项与键顺序原样保留 |
| **TOML 段落替换** | Codex | 定位 `[mcp_servers.<name>]`（含 `.env` 子表）精准替换或追加，保留注释与其余段落；写入前经 `go-toml` 完整语法回验 |
| **YAML 标记块追加** | `dsh` | 以 `# >>> rolling-memory (<name>) >>>` 闭合标记块追加或替换；卸载时仅精准移除标记块内部内容 |

### 4.4 豆包（Doubao）新用户接入：从零到可用

面向第一次把本工具接进豆包桌面端的用户：**技能面一条命令**，MCP 工具面需在 App 内补一步。

**第 0 步 · 前置**：豆包桌面端已安装并启动过至少一次（首启会建用户工作区 `~/Doubao`）；本工具已安装（`rolling-memory --version` 有输出）。

**第 1 步 · 写入技能**（双根写入，即豆包客户端自报的 `local_skill_paths`）：

```bash
rolling-memory install doubao            # 同时写入下面两个技能根
rolling-memory install doubao --check    # 复核：应见两行 skill ok
```

| 技能根 | 说明 |
|---|---|
| `~/Doubao/skills/<name>/SKILL.md` | 豆包用户工作区（与 `chats/` 同级），App 首启自建 |
| `~/.agents/skills/<name>/SKILL.md` | 跨 agent 约定根；同名技能优先于 additional 根 |

**第 2 步 · 让豆包扫到**：重启豆包或新开一个会话，即触发本地技能扫描（后台 `background_sync`）。会话技能列表应出现 `rolling-memory`；被扫到的 USER skill 会被豆包打包上传到你的账号做跨设备同步——**无需开发者账号，也无上架审核**。

**第 3 步 · 接上 MCP 工具面（走地址）**：豆包的连接器接受**地址**式 MCP（`mcp_transport`: Streamable HTTP / SSE），但不接受任意绝对路径的本地命令（`mcp_command_path_policy` 会以 `STDIO_COMMAND_ABSOLUTE_NOT_ALLOWED` 拒掉包装脚本）。所以由本工具起一个本地 URL 服务，把地址填进豆包：

```bash
rolling-memory mcp --http                                    # 默认 http://127.0.0.1:8765/mcp（另有旧式 SSE: /sse）
rolling-memory mcp --http --addr 127.0.0.1:9000 --path /mcp  # 需要换端口 / 路径时
```

在豆包的连接器表单里选 **Streamable HTTP**，填 `http://127.0.0.1:8765/mcp`（若表单只给 SSE 选项，填 `http://127.0.0.1:8765/sse`）。保存后新开会话，工具列表应出现 `recall`（回忆）——记忆面只有这一个工具：对话轮次由 Hooks 自动捕获，`recall` 负责检索并把证据结晶为记忆。

- 服务只绑回环地址（默认 `127.0.0.1`）、请求体上限 8 MiB、跨源请求拒绝；进程需常驻，建议交给系统服务托管。
- `install doubao` 的 MCP 一行仍显示 `skipped` 属**如实反映**：注册面在豆包侧（凭证加密存储 + manifest 下发），命令行写不进去，只能由你在 App 内填地址。

**排查**：技能未出现时，先确认两个技能根下都有 `SKILL.md`；再查豆包运行期日志 `~/Library/Application Support/Doubao/sdk_storage/log/saman_<日期>.log`，搜 `local_skill_paths`（客户端自报技能根）、`scan_local_skills`（扫描结果与 `item_count`）、`[MCP_SERVICE] get all tools … connector_count=`（MCP 装载实况）。地址式连接器未生效时先本地验证服务：`curl -s http://127.0.0.1:8765/mcp -H 'content-type: application/json' -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'`。

**卸载**：`rolling-memory install doubao --uninstall`（只删本工具写入的技能文件，豆包侧账号副本需在 App 内移除）。

---

## 5. 常用命令

```bash
# 1. 查看宿主探测状态与目标注册路径
rolling-memory install --list

# 2. 预演安装（不修改任何文件，仅列出计划变更）
rolling-memory install --dry-run

# 3. 一键接入所有已探测到的宿主（含技能、MCP 与生命周期 Hooks）
rolling-memory install --with-hooks --with-instructions

# 4. 项目级隔离安装（数据库默认落在 ./.rolling-memory/memory.db，scope 默认 project:<目录名>）
cd /path/to/project && rolling-memory install --project zcode claude-code

# 5. 多实例共存安装（自定义 MCP 服务名与技能名）
rolling-memory install --name rmem --skill-name rmem

# 6. 指定特定宿主或 dsh 配置文件
rolling-memory install zcode dsh --profile web

# 7. 静态配置核验 + 实时拉起 MCP 进程验证 initialize / tools/list 握手
rolling-memory install --check

# 8. 安全卸载（仅移除本工具写入的条目，外来配置原样保留）
rolling-memory install --uninstall
```

---

## 6. 四层验收判据

`rolling-memory install --check` 自动执行第 1–3 层机械核验；第 4 层可在新开启的 Agent 会话中验证：

| 层级 | 验收项 | 核验方式 |
|---|---|---|
| **第 1 层：技能就位** | 宿主技能目录存在 `SKILL.md` 且包含完整 frontmatter | 运行 `rolling-memory install --check`；新开会话可见对应技能名称 |
| **第 2 层：MCP 已注册** | 宿主配置文件中存在合法的 `rolling-memory` 服务条目 | 运行 `rolling-memory install --check` 自动解析校验 |
| **第 3 层：进程握手通过** | 真实拉起 MCP 子进程，校验 `initialize` 与 `tools/list` 恰返回 `recall` 单工具 | `install --check` 顶部 `server ok · rolling-memory ... · tools: recall` |
| **第 4 层：会话行为闭环** | 新会话追问历史背景时自动触发 `recall`（回忆即巩固 `Noetic`），生命周期钩子与后台同步进程将已完成的对话轮次自动收进本地金库并作为 `Episodic` 落库 | 在新会话中提问或查看 `~/.local/state/rolling-memory/rolling-memory.log` |

---

## 7. 启动器 Shim 与 U 盘便携自愈锚点

GUI 类 IDE 在启动 MCP 子进程或 Hooks 时常会裁剪 `PATH` 环境变量，导致裸命令名无法找到可执行文件。为此，`install` 默认在宿主侧生成包含绝对路径的启动器 Shim：

```sh
# ~/.local/share/rolling-memory/bin/rolling-memory.sh（0755）
#!/bin/sh
# Rolling-Memory launcher (generated by `rolling-memory install`).
# Absolute path on purpose: harnesses often spawn MCP servers and hooks with a cropped PATH.
exec "<absolute>/bin/rolling-memory" "$@"
```

- **零外部运行时依赖**：Go 编译产物自包含 SQLite 与全部运行逻辑，启动器仅执行一次轻量 `exec`。
- **Windows 原生支持**：在 Windows 下自动生成 `%LOCALAPPDATA%\rolling-memory\bin\rolling-memory.cmd`（`@echo off` + 绝对路径 + `%*`）。
- **U 盘便携模式（Portable Host Anchor）**：当从带有 `.portable` 标记的 U 盘便携载荷执行 `install` 时，生成的启动器自动升级为**跨挂载点自愈锚点**——优先直连原路径；若发生盘符或挂载点名称漂移（如 macOS `/Volumes/USB 1` 或 Windows `F:\`），则自动扫描可移动介质挂载根寻回 `.portable` 载荷（完整制盘与安全拔盘规程见 [usb-portable.md](usb-portable.md)）。
- **所有权识别**：启动器头部包含 `Rolling-Memory launcher` 签名注释，重复运行 `install` 可幂等更新；若检测到非本工具生成的同名脚本，则报告 `conflict` 并拒绝覆盖。

---

## 8. 备份、幂等与回滚保证

- **改前自动备份**：修改任何既有宿主配置文件前，自动生成带时间戳的备份文件 `<file>.rolling-memory.<YYYYmmddHHMMSS>.bak`。
- **严格幂等**：配置未发生变化时，重复执行 `install` 报告 `unchanged`，不产生多余写入或重复备份。
- **冲突保护**：若目标配置中已存在非本实例管理的同名条目，默认报告 `conflict` 而不覆盖；仅当显式传入 `--force` 时才执行替换。
- **一键回滚**：可将 `.bak` 备份文件直接还原，或执行 `rolling-memory install --uninstall` 精准移除全部已注册条目。

---

## 9. 在线自更新与金库安全契约（`rolling-memory update`）

无需安装 `git` 或 Go 编译环境，即可通过 `rolling-memory update`（别名 `rolling-memory upgrade`）一站式升级 CLI 二进制、本地 Web 控制台与全机宿主接线：

```bash
# 1. 只读检查：拉取发布渠道 SHA256SUMS 并比对本地组件 SHA-256 指纹（不修改任何文件）
rolling-memory update --check

# 2. 一键升级 CLI 二进制、刷新已接入宿主配置，并在已安装控制台时同步升级控制台
rolling-memory update

# 3. 同时下载安装或升级本地 Web 控制台（rolling-admin 后端 + web-dist.tar.gz 前端）
rolling-memory update --with-console

# 4. 指定自定义发布镜像源 URL（也可设置环境变量 ROLLING_MEMORY_RELEASE_BASE）
rolling-memory update --release-base https://mirror.example.com/rolling-memory/releases/latest/download

# 5. 输出结构化 JSON 结果供脚本或后台调用
rolling-memory update --check --json
```

- **SHA-256 强校验（Fail-Closed）**：每个下载产物（`rolling-memory-<os>-<arch>`、`rolling-admin-<os>-<arch>`、`web-dist.tar.gz`）在替换本地文件前均强制比对发布渠道的 `SHA256SUMS`；若校验缺失或不匹配则立即中止，绝不污染既有二进制。
- **守护进程与控制台平滑热切换**：CLI 二进制完成原子替换后，自动通知本机常驻守护进程（`engine.sock`）优雅下线，使下一次 MCP 或 CLI 请求即刻由新版二进制接管；若 Web 控制台处于运行中，升级完成后会自动重启控制台服务。
- **本地金库零触碰安全承诺**：升级全过程仅替换可执行文件、前端静态资源与宿主配置项，**永不修改、清空或覆盖用户的本地 SQLite 明文金库（`~/.local/share/rolling-memory/memory.db`）**；数据库表结构演进均在打开时原位幂等迁移。
- **零开发环境依赖（免装 Git / Go / Node.js / Python）**：
  - `install.sh` 与 `install.ps1` 默认直接拉取经 `SHA256SUMS` 强校验的预编译静态二进制（POSIX 下自适应 `curl` 或 `wget`，Windows 下原生支持 x64 与 ARM64，并在主发布源不可达时自动回退备用镜像源）。
  - 安装脚本会自动、幂等地将 `~/.local/bin`（Windows 为 `%LOCALAPPDATA%\rolling-memory\bin`）写入当前用户的终端配置文件或系统用户级 `PATH`（如需跳过可设 `ROLLING_MEMORY_NO_MODIFY_PATH=1`）。
  - 本地 Web 控制台（`rolling-memory panel`）由 Go 单二进制内置静态服务（`--serve-web`）直接托管前端页面，无需在电脑上安装 Node.js 或 Python；在 `rolling-memory install` 时加上 `--desktop-shortcut` 即可在桌面生成双击启动图标。


