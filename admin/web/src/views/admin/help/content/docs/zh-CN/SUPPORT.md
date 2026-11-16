# 支持

**[English](../../SUPPORT.md) · [简体中文](SUPPORT.md)**

## 获取帮助

- **文档**：[docs/](README.md) 与应用内帮助中心。
- **邮件支持**：`support@psychologylake.org.cn`（组织邮箱）。
- **云端试用申请、合作与投资**：`pick@psychologylake.org.cn`。
- **缺陷报告与功能请求**：在 <https://github.com/rocareer/rolling-memory/issues> 提交 issue。
- **安全报告**：请遵循 [SECURITY.md](SECURITY.md)；不要为漏洞提交公开 issue。
- **使用问答与讨论**：优先使用 GitHub Discussions；不可用时使用带 `question` 标签的 issue。

## 响应预期

本项目由社区维护。支持按尽力而为原则提供，不附带服务级别协议（SLA）。我们会争取在数个工作日内
初步分诊新 issue，但响应时间取决于维护者可用性。

## 语言

issue 与文档支持英文与简体中文；其他语言同样欢迎，但响应可能较慢。

## 受支持版本

最新发布的次版本会获得修复。较旧的次版本可能被要求在提供修复前先升级。本地模式无需账号或订阅；
云端试用模式为邀请制，不在本支持政策覆盖范围内。

## 范围

我们提供以下帮助：

- 安装与宿主集成（MCP、CLI、插件安装器）；
- 本地模式的导入、召回、导出、备份与迁移；
- 已发布源码或二进制中的可复现缺陷；
- 文档错误。

我们不提供：

- 定制开发、私有部署或咨询；
- 对第三方宿主、模型或云服务商的保证；
- 以官方项目名分发的修改版分支的支持。

## 提交 issue 之前

1. 运行 `rolling-memory doctor --json`，并附上已移除密钥的输出。
2. 说明版本（`rolling-memory --version`）、操作系统、宿主集成方式与存储模式（`local` 或 `cloud`）。
3. 提供最小复现与你实际执行的完整命令。
4. 从日志中移除 API Key、令牌、个人数据与金库内容。
