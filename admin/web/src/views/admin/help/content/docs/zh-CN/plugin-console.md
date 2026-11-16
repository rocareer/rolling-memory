# Web 管理控制台与插件频道指南

**[简体中文](plugin-console.md) · [English](../plugin-console.md)**

Rolling Memory 随仓提供基于纯 Go SQLite 的完整 Web 管理控制台（`rolling-memory panel`），作为系统唯一的可视化管理与观测面板，将记忆引擎的实时运行监控、独立记忆控制台、ROI 与认知效能监控、力导向记忆图谱、记忆治理、冻结基准评测、插件管理与开源文档中心整合在同一工作台中。

> **从源码编译与启动（`admin/`）**：本项目后台管理控制台（`admin/` 目录）已随仓完整开源（目录结构与二次开发指南见仓内 `admin/README.md`），不分发预编译后台构建包，请直接从源码本地编译运行：
>
> 1. **编译前端静态资源**（产出至 `admin/web/dist/`）：
>    ```bash
>    cd admin/web
>    npm install
>    npm run build
>    cd ../..
>    ```
> 2. **通过 `rolling-memory panel` 一键编译后端并启动**：
>    ```bash
>    CGO_ENABLED=0 go build -trimpath -o rolling-memory ./cmd/rolling-memory
>    ./rolling-memory panel
>    ```
>    `rolling-memory panel` 会自动检测仓库中的 `admin/cmd` 源码并编译后端二进制，启动后端 API（`127.0.0.1:9006`）与前端静态服务（`127.0.0.1:8008`），随后打开 `http://localhost:8008/#/admin`（登录页上写明了本机默认演示账号 `rolling / rolling`）。可通过 `rolling-memory panel status` 查看状态、`rolling-memory panel stop` 停止进程，或使用 `--api-port` / `--web-port`（环境变量 `RM_PANEL_API_PORT` / `RM_PANEL_WEB_PORT`）自定义端口。
> 3. **手动分别编译并启动前后端**（适合二次开发与调试）：
>    ```bash
>    # 终端 1 —— 编译并启动后端 API (127.0.0.1:9006)
>    cd admin && CGO_ENABLED=0 go build -trimpath -o bin/rolling-admin ./cmd && ./bin/rolling-admin serve
>
>    # 终端 2 —— 启动前端 Vite 开发服 (127.0.0.1:8008)
>    cd admin/web && npm run dev
>    ```
>
> **默认仅本机可访问**：控制台默认只监听 `127.0.0.1`，并且只接受发往本机地址（`localhost` / `127.0.0.1`）的请求——它能读写你全部的本地记忆，而开箱账号是公开写明的。需要从其他设备访问时，将环境变量 `SERVER_HOST`（或 `~/.local/share/rolling-memory/admin/config/config.yaml` 的 `server.host`）设为 `0.0.0.0` 或指定网卡地址，并先修改默认口令。

---

## 1. 频道总览

Web 管理控制台左侧导航栏按职能划分为七个一级频道，日常用到的频道在上，权限 / 常规 / 插件等管理频道在下：

| 序号 | 一级频道 | 子栏目 | 核心职责 |
| :--- | :--- | :--- | :--- |
| 1 | **控制台** (`dashboard`) | `实时控制台` · `记忆控制台` (`/#/admin/dashboard/artifacts`) · `ROI 监控屏` · `记忆图谱` | 基于零轮询 WebSocket 实时流的引擎遥测与 5 组 ECharts 图表、独立记忆控制台（100% 向量无关与 LLM 无关的本地记忆引擎连接状态、本地明文金库遥测与运行开关）、记忆 ROI 与认知效能监控台（1 小时 / 24 小时 / 7 天 / 30 天 / 全量五档监控窗，实测 L3 认知结晶与跨层引证转化、跨会话记忆复用倍数、`tokenz-r` 分形干句提纯预算、零拷贝指针占比、多工作区冷热覆盖矩阵与金牌高复用记忆榜），以及基于 Web Worker 力导向与碰撞检测的交互式记忆图谱（零轮询 WebSocket 实时流自动刷新） |
| 2 | **记忆管理** (`plugin/rolling-memory`) | `检索调试` · `记忆总览` · `L1 文档与代码` · `L2 会话记录` · `L3 检索沉淀` | **检索调试**与 Agent 走同一条 `recall` 链路：可选工作区锚（同项目证据排前、其他结果不删）、「最近 Agent 检索」一键重放，默认只读（不写入 L3）；三层页面分别浏览挂载目录分块、Harness 会话记录与 Agent 检索沉淀，支持多维筛选、原文切片溯源与删除，L3 行可「在调试台重放」 |
| 3 | **评测** (`plugin/rolling-memory/eval-dir`) | `评测实验室` · `剑桥 ATM-Bench 榜单` · `LoCoMo-Backboard 榜单` | 剑桥 ATM-Bench（`--suite atm`，`hard / 1013` 第一主基准）与 LoCoMo（`200 / 1540` 对照参照）本地复现与在线评测；两张独立榜单页分别展示剑桥大学 ATM-Bench 多模态个人记忆主榜单（官方 `Recall@10` 口径、Main-1013 / Hard-31 双 Tab）与密码学锁定的 `locomo-backboard` 对照榜单 |
| 4 | **帮助中心** (`help`) | `帮助与文档` · `开源仓库与声明` | 独立通用开源文档模块，直接对接 `docs/zh-CN/` 与 `docs/` 全套双语技术文档、根级 `DECLARATION.md` 诚实免责声明与 `LICENSE` 许可协议 |
| 5 | **权限管理** (`auth`) | `角色组管理` · `管理员管理` · `菜单规则管理` · `管理员日志管理` | RBAC 权限树、菜单规则与操作日志审计 |
| 6 | **常规管理** (`routine`) | `系统配置` · `个人资料` | 站点基础参数与管理员个人资料配置 |
| 7 | **插件** (`plugin-center`) | `插件管理` · `仓库来源` · `插件清单` | 宿主插件生命周期管理（安装/启用/停用/卸载）、`local`/`http`/`git` 仓库来源配置与清单同步预演 |

### 1.1 后台框架来源

本控制台**不是从零自研的后台框架**，其来源与边界如下：

| 层次 | 来源项目 | 许可证 | 在本项目中的关系与修改 |
|---|---|---|---|
| **后台与服务端框架** | **AI GO ADMIN**（Go + Gin + PostgreSQL 后端，Vue 3 + TypeScript + Vite + Pinia + Element Plus 前端） | Apache-2.0 | **衍生开发**：保留原项目 `LICENSE` 与目录骨架，存储改造为本地 SQLite，新增 Rolling Memory 的记忆控制台、ROI 监控、记忆图谱、评测实验室、权限节点、路由与品牌资源；原项目中的 demo / 支付 / 商城等无关代码已移除 |
| **后台 UI 血统** | **BuildAdmin**（PHP 版同源后台，AI GO ADMIN 即其 Go 版本重写） | Apache-2.0（Copyright 妙码生花） | **链路来源**：控制台整体交互与视觉体系承自 BuildAdmin；其中**插件清单页**（`web/src/views/admin/plugin/catalog/`）按 BuildAdmin `v2.3.9`（commit `4686522`）移植并改写 |
| **本项目自有代码** | — | Apache-2.0 | `internal/rmconsole`、`web/src/views/admin/plugin/rolling-memory/`、`web/src/views/admin/dashboard/` 及相关路由 / API / 权限 / 品牌资源均为本项目原创 |

完整的第三方归属（来源地址、开源版本与 commit、逐文件修改说明）登记于 [`../../NOTICE`](../../NOTICE) 第 2 节。第三方项目的商标、Logo、演示图片、支付图标以及账号与商城 API 均**未**纳入本项目。

**鸣谢与开源项目地址**：AI GO ADMIN —— [GitHub](https://github.com/ai-go-hub/ai-go-admin) · [Gitee](https://gitee.com/ai-go-hub/ai-go-admin) · [AtomGit](https://atomgit.com/ai-go-hub/ai-go-admin)（Apache-2.0，© AI GO ADMIN contributors）；BuildAdmin —— [Gitee](https://gitee.com/wonderful-code/buildadmin)（Apache-2.0，© 妙码生花）。感谢两个开源项目的作者与社区。

---

## 2. 独立「记忆控制台」与零轮询 WebSocket 实时流架构

### 2.1 `Dashboard -> 记忆控制台 (Memory Console)` (`/#/admin/dashboard/artifacts`)

在 `实时控制台`、`ROI 监控屏` 与 `记忆图谱` 之外，控制台提供独立的 **`Dashboard -> 记忆控制台`**（路由 `/#/admin/dashboard/artifacts`），集中展示并管控 **Rolling Autonoetic Memory Engine（洛灵自知记忆引擎 · 100% 向量无关 · 100% LLM 无关）** 的运行状态：

- **零向量、零大模型极简架构面板**：无需配置任何向量嵌入模型或交叉重排模型，直接监控记忆引擎连通性、脉冲吞吐与本地明文金库资产统计。
- **运行期开关实时控制**：一键切换开场自动回忆注入（`boot_context`）、召回调用账本记录（`ledger`）、稀疏回退原文（`raw_when_sparse`）与悬浮监控微件（`widget_enabled`）。

### 2.2 零轮询 WebSocket 实时推送流

记忆控制台、全局状态微件、ROI 监控屏、环境健康面板与终端抽屉全面采用 **零轮询 WebSocket 实时流** 替代传统定时器轮询：

- **事件驱动即时推送**：配置变更、记忆写入与后台作业状态通过单条多路复用 WebSocket 长连接即时推送到前端，消除高频 HTTP 轮询开销。
- **变化才推、静默零开销**：每个面板订阅的房间只在其底层状态真正变化时推帧；没有任何变化时，控制台不发查询、不做聚合、不起维护子进程。所有打开的面板共享同一个服务端监听器，开销不随标签页数量叠加。
- **后台任务实时进度条**：在执行基准评测或维护作业时，WebSocket 通道实时推送完成比例、吞吐速率（**`items/s`**）与预计剩余时间（**`ETA`**）。
- **可选定周期刷新**：提供刷新档位的面板（实时控制台、ROI 监控屏）仍保留定周期选项（每 15s/30s/60s 或手动），作为低功耗替代口径。

### 2.3 `Dashboard -> ROI 监控屏`（粗略趋势估算模块）与体验评价 Hook 开关 (`/#/admin/dashboard/roi`)

后台预置了一个 **ROI 与认知效能评价模块**，同时在本地模式下默认启用了**记忆体验评价 Hook 与评价卡**，让你对“记忆到底有没有帮上忙”有直观感受。

> **坦诚说明与随时关闭方法**：
> 后台自动统计的 ROI 模块本质上是一个**不那么准确的粗略趋势估算器**（只能根据词面重叠与引证次数做近似推算，看个大概趋势即可；更严苛的逐轮正反算账与本机真实一周记录见 [`token-economics.md`](token-economics.md)）。如果你觉得评价 Hook 或提示打扰了日常工作，随时可以通过以下命令关掉：
> ```bash
> # 方式 1：只挂载纯 MCP recall 工具，移除所有 Hook 与指令提示
> rolling-memory install --mcp-only
>
> # 方式 2：保留安装，单独关闭开场注入或 Hook 账本记录
> rolling-memory config set hooks.boot_context false
> rolling-memory config set hooks.ledger false
> ```

该面板从本地三层明文金库与调用账本提取五项统计信号供参考：

- **L3 认知结晶与跨层实名引证转化**：统计 Agent 主动调用 `recall` 命中底层 L1/L2 证据并自动结晶为 L3 认知手柄的次数、累计跨层引证链条数与单次召回平均引证单元数。
- **跨会话记忆资产复用倍数（Write Once, Cite N×）**：度量底层记忆单元在后续不同会话中被反复召回引证的平均复用倍数、最高单条复用次数以及高复用金牌单元榜（点击可展开查看引证过该单元的全部历史 Query）。
- **`tokenz-r` 召回交付预算与干句提纯比**：使用全域统一分词标准 `tokenz-r` 实测每次召回交付的精炼干句 Token 预算与提纯压缩率。
- **零拷贝 `@ref:` 金库底座与 14 天沉淀×结晶脉冲**：展示零拷贝情景指针占比、去重后的记忆地址数，以及近 14 天每日 L1/L2 沉淀与 L3 认知结晶的双轨趋势。
- **多工作区（Locus）冷热覆盖矩阵与证据链溯源抽屉**：按工作区诊断记忆沉淀与召回活跃度（高频召回热区 vs 待激活冷区），支持点击工作区联动筛选金牌单元榜与 L3 认知结晶证据流。

### 2.4 在线版本检查与原地安全升级（「版本与在线更新」弹窗）

在 **`记忆管理 -> 记忆总览`**（`/#/admin/plugin/rolling-memory/index`）与 **`控制台 -> 记忆控制台`**（`/#/admin/dashboard/artifacts`）顶部操作栏均内置了 **「版本与在线更新 (Version & Update)」** 入口：

- **打开窗口零外呼**：打开弹窗仅读取本机三项核心组件（`rolling-memory` CLI 引擎核、`rolling-admin` 控制台后端、`web-dist.tar.gz` 前端静态资源）的本地 SHA-256 指纹，不发起任何外部网络请求。
- **按需比对远端 `SHA256SUMS` 清单**：仅当用户主动点击 **「检查更新」** 时，才从官方发布渠道（或弹窗内填写的自定义镜像源 URL）获取 `SHA256SUMS`，以表格逐项展示本地与远端 SHA-256 指纹对比及待更新状态。
- **一键安全升级与平滑热重启**：点击 **「立即安全升级」** 即通过 SHA-256 强校验原位替换有更新的组件、自动刷新本机 AI 宿主接线，并在后台平滑重启控制台服务后自动刷新浏览器界面；全过程绝不修改或覆盖本地 SQLite 明文金库（`memory.db`）。

### 2.5 三层记忆浏览器与正文 Markdown 预览（`/#/admin/plugin/rolling-memory/index`）

记忆总览与 L1 / L2 / L3 三层页面共用同一套记忆单元浏览器：列表与详情弹窗直接展示**原始正文**——L1 挂载分块的正文在你打开详情时从源文件现场重读；若源文件已被移动或删除，控制台如实说明并给出来源位置与一键重试，绝不用坐标冒充正文。

各正文阅读面（记忆浏览器详情、检索调试台的原文命中与完整轨迹、云端回忆结果、ROI 证据抽屉）共用同一个 Markdown 阅读面，**无需手动切换**：散文与 Markdown 文档按标准 Markdown 渲染（标题、列表、表格、引用与围栏代码块，代码块一键复制并带行号）；结构化内容自动格式化为围栏代码块——JSON 自动缩进美化、JSONL 逐行保留、源码按语言高亮；**「复制原文」**始终复制未形变的存储字节。格式化只改变阅读呈现，绝不改写、截断或伪造内容。

---

## 3. 插件频道与环境校验说明

1. **环境自动判定（`dev` vs `prod`）**：
   - **开发环境（`dev`）**：当服务运行在源码检出树内或显式设置 `plugin.env=dev` 时，允许执行插件安装、卸载与来源清单同步。
   - **生产环境（`prod`）**：由于源码插件（`kind: code`）涉及源码树文件落位与重编译，生产环境默认强制封闭安装与卸载通道（返回 `lifecycle_forbidden`），确保线上运行态安全。
2. **插件三种形态**：
   - `code`（L1 源码插件）：包含 Go 路由与前端视图，首次安装后需重编译后端进程生效；
   - `declarative`（L2 声明式插件）：纯元数据驱动，由通用渲染引擎即时挂载；
   - `mixed`（混合插件）：兼具声明式实体与自定义扩展逻辑。

---

## 4. 帮助中心与 `docs/` 多语种文档映射

后台「帮助中心」频道直接加载仓库（[`https://github.com/rocareer/rolling-memory`](https://github.com/rocareer/rolling-memory)）根目录与 `docs/zh-CN/`、`docs/` 下的真实全文，切换后台语言时自动切换对应语种文档：

- **根级总览与合规声明**：[`README.zh-CN.md`](README.zh-CN.md) · [`README.md`](../../README.md) · [`DECLARATION.md`](DECLARATION.md) · [`LICENSE`](../../LICENSE) · [`NOTICE`](../../NOTICE) · [`CHANGELOG.md`](../../CHANGELOG.md)
- **核心架构与接口文档（简体中文 `docs/zh-CN/` · 英文 `docs/`）**：[`mcp.md`](mcp.md) · [`harness.md`](harness.md) · [`usb-portable.md`](usb-portable.md) · [`benchmarks.md`](benchmarks.md) · [`privacy.md`](privacy.md) · [`plugin-console.md`](plugin-console.md)
