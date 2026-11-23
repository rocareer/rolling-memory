# MCP 单工具契约（`recall` · 回忆）

**[简体中文](mcp.md) · [English](../mcp.md)**

Rolling Memory 的 MCP server 是一个 stdio 进程（`rolling-memory mcp`），server 名 `rolling-memory`，向宿主 Coding Agent 暴露**唯一工具：`recall`（中文名：回忆）**。

在 **Rolling Autonoetic Memory Engine（洛灵自知记忆引擎）** 记忆模型下，Agent **完全不需要手动调用写记忆工具**：
- **L1 `Background`（背景层）**：用户挂载的本地目录文件由客户端原位分块；挂载时全量建索引，之后按文件 mtime/size 与块级 SHA-256 增量核对（原文始终留在本地磁盘，本地索引）；同一物理文件只索引一次——重复导入同一目录、或父目录与其子目录同时挂载都不会重复入库（文件归属最深的挂载点）；移除挂载只会移除没有被其他挂载覆盖的文件索引，仍被其他挂载目录覆盖的文件保留索引与溯源。
- **L2 `Episodic`（情景层）**：后台巡检可从受支持的本地来源导入对话轮次；`rolling-memory sync reconcile` 可请求扫描。采集并不覆盖所有宿主和转录格式，详见[对话采集范围与手工导入](harness.md#45-对话采集范围与手工导入)。
- **L3 `Noetic`（自知/巩固层）**：每一次执行 `recall`（回忆）命中真实单元时，自动将当前提问与被引证单元句柄实时固化为本地 `Noetic` 结晶（**回忆即记忆、回忆即巩固**）。

---

## 1. 启动与环境变量

```bash
rolling-memory mcp        # 构建产物 bin/rolling-memory，或 install 生成的启动器
```

| 变量 | 默认 | 作用 |
|---|---|---|
| `ROLLING_MEMORY_CONFIG` | `~/.config/rolling-memory/config.toml` | 配置文件 |
| `ROLLING_MEMORY_DB` | `~/.local/share/rolling-memory/memory.db` | 本地 SQLite 明文金库路径 |
| `ROLLING_MEMORY_SCOPE` | `default` | 记忆域 |
| `ROLLING_MEMORY_MODE` | `local` | 运行模式 —— `local` 为默认值，也是本发布版的唯一模式 |
| `ROLLING_MEMORY_PORTABLE` | 未设 | U 盘便携载荷根目录（设 `1` 时自动解析） |
| `ROLLING_MEMORY_MCP_DEBUG` | 未设 | `1` 时把错误栈写 stderr |
| `ROLLING_MEMORY_MCP_IDLE_RELEASE` | `30m` | 空闲释放金库句柄的预算（Go duration）；`0` 或负值表示不释放、保持句柄打开 |

协议：换行分隔 JSON-RPC 2.0；服务端独占 stdout。客户端只需实现 `initialize` / `tools/list` / `tools/call`。

`initialize` 返回：

```json
{
  "protocolVersion": "2024-11-05",
  "capabilities": {"tools": {"listChanged": false}},
  "serverInfo": {"name": "rolling-memory", "version": "<随仓库 VERSION>"},
  "instructions": "..."
}
```

服务端惰性连库：`initialize` 与 `tools/list` 不打开数据库；首个 `tools/call` 才打开。

**进程生命周期**：stdio 会话在整个宿主会话期内保持连接。仅两种情况下会话会主动结束：①父进程消失（孤儿）；②进程内服务发现磁盘上的可执行文件已不是本进程的构建，于是退出，让旧语义停止写入金库。输入静默超过 `ROLLING_MEMORY_MCP_IDLE_RELEASE`（默认 `30m`，每次收到输入即重置）时，进程内会话只关闭自己打开的金库句柄，下次调用时惰性重开；进程与宿主连接始终存活。默认的守护进程转发侧车本身不持有金库，因此输入静默与换版都不会中断它；共享守护进程在换版后自行退出，并在下次调用时重启。退出原因仅在 `ROLLING_MEMORY_MCP_DEBUG=1` 时写 stderr。

---

## 2. 唯一工具：`recall`（回忆）

### 2.1 输入参数

| 参数 | 类型 | 说明 |
|---|---|---|
| `cue` | string | 字面检索线索（`mode=full`/`probe` 必填）：首部可加 `[<工作区>]`（如 `[Rolling-Memory]`）优先召回当前仓库记忆，后接**稠密**字面线索——意图、对象/文件/符号原文、1–2 个硬标识符（报错原文、状态码、路由、版本号、编号、日期）与时间场合；典型 12–25 个实词，空格分隔。每个词都应真的出现在目标记忆里，避免泛化虚词（它们会把无关长会话顶进前排）。2–6 个词只用于确认单个已知符号或错误码；若命中杂乱，删掉泛词、保留精确词 |
| `scope` | string | 覆盖默认 scope |
| `reader` | `owner` \| `deliverable` | 默认 `deliverable`；历史受众限制可能无法覆盖所有来源。分享结果前请先检查内容。 |
| `mode` | `full` \| `probe` \| `recent` | `full` 返回完整召回带与证据链；`probe` 返回紧凑存在性回执（摘要截断至 160 字）；`recent` 按时间倒序返回最新记忆条目（纯时间序、无语义匹配，忽略 `cue`） |
| `delivery_window` | `auto` \| `0-16` \| `0-32` \| `0-64` | **智能自适应交付窗口**（默认 `auto` 跟随后台设置，初始默认 `0-16`）：基于物理信息能量自然衰减包络 $E(u_k) \ge E(u_1)/e$ 零保底选材（无匹配时返回 `0` 条，单点精准命中自动返回 `1–5` 条，多事件/跨期问题自动沿能量平台展开至档位上限）。日常对话请省略或传 `auto`；仅在跨会话排查时传 `0-32`（标准扩展），或在全局架构溯源/多事件盘点时传 `0-64`（深度全景）。（基准评测中，ATM-Bench 默认采用 `0-64`，LoCoMo 默认采用 `0-96`。） |
| `caps` | object | `{delivery_window, bands, nodes, chars, raw}` 交付预算覆写：交付带数、总节点数、字符预算与原文命中上限。 |
| `offset` | int | 召回带分页偏移 |

### 2.2 Full 回执与实时 `Noetic` 结晶

文本内容返回预算受控的 `<rolling-memory-recall ...>...</rolling-memory-recall>` 块，直接供 Agent 消费。

关键字段：
- `absent`：未命中相关记忆。**这不是错误**（`isError = false`），Agent 据实说明「记忆里没有」并继续执行当前任务。
- `gaps`：缺口归因标记（如 `empty_graph`、`budget_exceeded` 等）。
- **时间标签**：记忆时间一律为本地钟面时刻，精确到秒并显式带 UTC 偏移；块头部 `now` 携带当前时间——对照「现在」判断远近，不必再猜日期与时区。
- `meta.legs`：各检索腿命中计数；`meta.activation`：激活度账本（0 LLM 调用）。
- **实时 `Noetic` 结晶**：只要 `recall` 命中真实证据单元（`cited_unit_ids` 非空）且 `mode != "probe"`，客户端即刻在本地引用索引落盘问答句柄，并在本地图谱实时生成 L3 `Noetic` 节点与 `cites` 引证边（0 LLM 调用）。

### 2.3 Probe 回执（`mode="probe"`）

```json
{
  "scope": "mem:self",
  "absent": false,
  "hits": [
    {"id": "ep_...", "summary": "...", "date": "2026-11-10", "status": "active",
     "anchor": "ep_...", "relative_strength": 1.0}
  ],
  "gaps": {},
  "meta": {"count": 20, "cue_coverage": 0.48, "elapsed_ms": 412, "engine": "Rolling Autonoetic Memory Engine", "noetic_hits": 1}
}
```

`relative_strength` 是本条检索权重相对于**同来源族候选峰值**的比例。不同来源族分别归一化，峰值候选不一定出现在最终结果中。它不是概率、相关度或真伪判断，也不能跨查询或跨族比较。

`meta.cue_coverage` 在有提供时，表示首个候选的源文本覆盖了多少不重复的线索原子；它不是对全部结果的汇总，也不是语义相关度。字段缺失不一定表示零重合；源文本越长，也可能覆盖更多线索原子。

---

## 3. 错误语义

| 情况 | JSON-RPC | 说明 |
|---|---|---|
| 未知方法 | `error.code = -32601` | 客户端编程错误 |
| 未知工具 / 参数不是对象 | `error.code = -32602` | 客户端编程错误（MCP 仅暴露 `recall`） |
| JSON 解析失败 | `error.code = -32700` | 行级错误，服务端继续 |
| 工具内部失败（DB、配置、网络） | `result.isError = true` | 文本含脱敏后的原因 |
| 查询成功但无命中记忆 | `result.isError = false`, `absent = true` | 一等认知状态，非错误 |

---

## 4. 轻量生命周期 Hook

遵循「**不需要 LLM 决策的动作不设 Hook**」原则，系统仅挂载**唯一一个 `UserPromptSubmit` Hook**：

| 事件 | 命令 | 行为 |
|---|---|---|
| `UserPromptSubmit` | `rolling-memory hook recall --json` | 在宿主支持且已安装钩子时，可提醒 Agent 按需调用 `recall` 并协助本地同步；不保证每个已完成轮次都被导入。 |

- **全链路 Fail-Open**：任何异常均退出 `0`，仅记日志，绝不阻塞宿主会话。
- **回声抑制账本**：同一 session + prompt（或相同渲染内容）在 1 小时窗口内自动去重，不重复占用上下文。
- **零出网**：本发布版为纯本地模式——提问、对话与记忆全部留在本机 SQLite 金库，不发往任何远端。

---

## 5. 客户端配置示例

- [examples/mcp-config.json](https://github.com/rocareer/rolling-memory/blob/main/examples/mcp-config.json)
- [examples/zcode-plugin/](https://github.com/rocareer/rolling-memory/tree/main/examples/zcode-plugin)
- [examples/claude-code/settings.json](https://github.com/rocareer/rolling-memory/blob/main/examples/claude-code/settings.json)
