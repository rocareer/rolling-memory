# Rolling-Memory 技术文档中心（简体中文）

**[简体中文](README.md) · [English](../README.md) · [官方网站](https://rolling-memory.rocareer.com/) · [项目主页（中文）](README.zh-CN.md) · [返回仓库主页（English）](../../README.md)**

本目录（`docs/zh-CN/`）为 Rolling Memory 公开技术文档与根级文档的**简体中文版**，与英文目录 [`docs/`](../README.md) 及仓库根目录英文文档保持对齐。

---

## 文档目录

| 文档 | 核心内容 | 英文版 |
|---|---|---|
| **[README.zh-CN.md](README.zh-CN.md)** | 项目总览、真免费与 AI 记忆普惠初衷、日常效果与局限边界、快速开始 | [../../README.md](../../README.md) |
| **[token-economics.md](token-economics.md)** | 本地记忆是怎么帮你省（或亏）Token 与时间的：`100k–400k` 长程上下文省往返算账、亏损反例，以及本机最近 24 小时与推算整周真实账单（含 GPT-6 / Claude Fable / Gemini 4 官网价格折算） | [docs/token-economics.md](../token-economics.md) |
| **[background-vs-grep.md](background-vs-grep.md)** | 既然 Harness 自带 `grep`，为什么还要用 `recall` 与 `Background` 目录挂载？：适用边界、`grep` 办不到的 4 个核心价值（症状→代码符号桥接、否定性排障教训、跨仓库与 `.docx/.xlsx/.pptx/.pdf` 原生解析、多线索共鸣排序）与 3 套本机自证方法 | [docs/background-vs-grep.md](../background-vs-grep.md) |
| **[DECLARATION.md](DECLARATION.md)** | 开源边界（本仓库发布什么、不发布什么）、贡献入口与按原样（AS IS）免责声明 | [../../DECLARATION.md](../../DECLARATION.md) |
| **[faq.md](faq.md)** | 常见问答（FAQ）：与 `grep` / `ripgrep`、向量 RAG、LLM 抽取服务以及各家 Harness 自带记忆功能（`CLAUDE.md` / `MEMORY.md` / Rules）的区别与互补配合 | [docs/faq.md](../faq.md) |
| **[mcp.md](mcp.md)** | MCP 单工具契约：`recall`（回忆）的完整参数、回执与后台自动收录规范 | [docs/mcp.md](../mcp.md) |
| **[harness.md](harness.md)** | 多宿主一键接入指南：ZCode / Claude Code / Codex / OpenCode / dsh / CodeBuddy 及 Cursor / Windsurf / Cline / WorkBuddy / 豆包 的注册矩阵与 `install --check` 自检 | [docs/harness.md](../harness.md) |
| **[usb-portable.md](usb-portable.md)** | U 盘便携模式指南：现实适用前提（跨电脑开发同一项目）、`rmem init --portable` 一键制盘、挂载点漂移自愈锚点与 `rmem eject` 安全拔盘 | [docs/usb-portable.md](../usb-portable.md) |
| **[plugin-console.md](plugin-console.md)** | Web 管理控制台指南：本地管理面板、WebSocket 实时流、粗略 ROI 趋势估算模块、体验评价 Hook 开关、记忆图谱与评测实验室 | [docs/plugin-console.md](../plugin-console.md) |
| **[console-terminal.md](console-terminal.md)** | Web 控制台内置命令行（终端）使用指南：入口与界面构成、recall/Hook 实时流与水合正文回显、快捷命令与常用命令对照表（status / doctor / recent / check / `sync prune` 只读预演）、白名单执行与显式确认边界 | [docs/console-terminal.md](../console-terminal.md) |
| **[install-upgrade.md](install-upgrade.md)** | 安装与升级指南：发布版默认 `stable`、源码安装默认 `dev`、stable/beta/dev 三通道、`update.auto` 后台策略、CLI / 控制台 / 定时任务 / 安装脚本升级路径，以及破坏性变更、兼容性、备份与回滚 | [docs/install-upgrade.md](../install-upgrade.md) |
| **[configuration.md](configuration.md)** | 配置参考：配置文件位置与优先级、常用 `[core]` / `[recall]` / `[daemon]` 配置（含引擎内存上限 `auto` / `off` / 明确尺寸）、常用环境变量，以及控制台「系统管理 → 记忆服务」的等价操作 | [docs/configuration.md](../configuration.md) |
| **[benchmarks.md](benchmarks.md)** | 基准复现与独立核验指南：自备语料与 `SHA-256` pin、LoCoMo 与剑桥大学 ATM-Bench 本地复现命令、评测协议校验与原始 JSONL 复算 | [docs/benchmarks.md](../benchmarks.md) |
| **[协作诊断](../../diagnostics/collaboration/README.zh-CN.md)** | 用合成题在本机检查助手何时查记忆、何时读现场、何时停止 | [English](../../diagnostics/collaboration/README.md) |
| **[architecture-and-performance.md](architecture-and-performance.md)** | 架构与性能：三层记忆、不可变的已发布代、异步可见性，以及不同入库积压下实测的本地召回时延 | [docs/architecture-and-performance.md](../architecture-and-performance.md) |
| **[privacy.md](privacy.md)** | **隐私与本地数据说明**：哪些数据永不离开本机、本地金库纵深防御、一键自证命令，以及云端能力的申请制预览 | [docs/privacy.md](../privacy.md) |
| **[essays/memory-retrieval-is-not-similarity-search.md](essays/memory-retrieval-is-not-similarity-search.md)** | 理念随笔：为什么相似度不该做记忆检索的主键——对称的相似度与不对称的问答、复读对相似度的膨胀、记忆问题的事件结构，以及把模型版本嵌进记忆格式的长期代价 | [docs/essays/memory-retrieval-is-not-similarity-search.md](../essays/memory-retrieval-is-not-similarity-search.md) |
| **[essays/semantic-bridging-creates-no-new-information.md](essays/semantic-bridging-creates-no-new-information.md)** | 理念随笔：图谱、摘要与指代消解都在做同一件事——语义搭桥；桥不产生新信息，理解该发生在读取那一刻（数据处理不等式 · 预消化悖论） | [docs/essays/semantic-bridging-creates-no-new-information.md](../essays/semantic-bridging-creates-no-new-information.md) |
| **[CONTRIBUTING.md](CONTRIBUTING.md)** | 贡献指南、开发与测试命令、PR 检查清单 | [../../CONTRIBUTING.md](../../CONTRIBUTING.md) |
| **[SECURITY.md](SECURITY.md)** | 安全策略、漏洞私下报告渠道与安全不变量 | [../../SECURITY.md](../../SECURITY.md) |
| **[SUPPORT.md](SUPPORT.md)** | 获取帮助、响应预期与支持范围 | [../../SUPPORT.md](../../SUPPORT.md) |
| **[TRADEMARK.md](TRADEMARK.md)** | 商标与品牌名称使用说明 | [../../TRADEMARK.md](../../TRADEMARK.md) |
