---
description: 一键体检 Rolling Memory 记忆系统（单机核心、全机主流 harness 适配、重复挂载去重与记忆库健康度）
---

对本机 Rolling Memory 记忆系统执行全链路体检与状态核验：

1. 运行本机原生安装核验与去重体检：`rolling-memory install --check`（若提示存在插件与全局配置重复挂载，或有已安装的 harness 未适配，执行 `rolling-memory install --with-hooks` 一键自愈收敛）。
2. 检查记忆库健康度与三层同步状态：运行 `rolling-memory status`、`rolling-memory doctor --json`、`rolling-memory check --json` 与 `rolling-memory sync status`。
3. 评估全 Harness 召回效能与 Token 成本 ROI：运行 `rolling-memory efficacy --limit 10`，查看最近会话的召回命中与估算节省往返情况。
4. 汇总输出：
   - **单机核心与多客户端接线**：主流 harness（zcode / claude-code / codex / dsh / codebuddy / cursor / windsurf / cline / workbuddy / doubao）的连通状态，确认无重复 MCP 进程或双倍 Hook；
   - **数据面健康度与召回效能**：本地 SQLite 金库完整性（`check --json`）、三层记忆（L1 Background / L2 Episodic / L3 Noetic）同步进度，以及跨 Harness 召回表现；
   - 如需可视化排查或在线升级，提示运行 `rolling-memory panel` 打开本地 Web 控制台或运行 `rolling-memory update --check` 检查更新。
