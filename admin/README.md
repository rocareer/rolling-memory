# Rolling-Memory Web 管理控制台 (`admin/`)

> **鸣谢 / Acknowledgment**：本管理控制台基于 **AI GO ADMIN**（[GitHub](https://github.com/ai-go-hub/ai-go-admin) · [Gitee](https://gitee.com/ai-go-hub/ai-go-admin) · [AtomGit](https://atomgit.com/ai-go-hub/ai-go-admin) · Apache-2.0）二次开发；UI 血统承自 **BuildAdmin**（[Gitee](https://gitee.com/wonderful-code/buildadmin) · Apache-2.0 · © 妙码生花）。保留其 `LICENSE`，修改与第三方组件见 [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md) 与 [`NOTICE`](NOTICE)。

本目录（`admin/`）提供 **Rolling Memory** 的原生内置 Web 管理控制台（前端 Vue 3 + TypeScript + Vite + Element Plus，后端纯 Go + Gin + SQLite），包含完整的源码。我们不提供预编译的后台构建包，用户可直接从本目录源码自行编译与运行。

完整的功能模块与频道说明请参阅：
- 简体中文：[`../docs/zh-CN/plugin-console.md`](../docs/zh-CN/plugin-console.md)
- English：[`../docs/plugin-console.md`](../docs/plugin-console.md)

---

## 1. 目录结构与核心模块

```text
admin/
├── cmd/                        # 控制台后端入口（serve / migrate / plugin 等子命令）
├── config/                     # 默认配置（config.yaml：默认监听 127.0.0.1:9006，使用本地 SQLite）
├── internal/
│   ├── rmconsole/              # Rolling-Memory 控制台后端核心实现（记忆总览、三层浏览、检索调试、ROI 监控、3D 图谱、评测实验室、WebSocket 实时流）
│   ├── infra/database/         # 本地 SQLite 自动建表与初始菜单/权限节点引导（sqlite_bootstrap.go）
│   └── plugin/                 # 宿主插件生命周期与声明式渲染引擎
├── pkg/ws/                     # 单连接多房间同源 WebSocket 实时推送总线
└── web/                        # 前端工程（Vue 3 + TypeScript + Vite + Pinia + Element Plus）
    ├── .env                    # 默认前端端口配置（VITE_PORT=8008）
    └── src/views/admin/
        ├── dashboard/          # 实时控制台、记忆控制台（artifacts）、ROI 监控屏、力导向记忆图谱
        ├── plugin/rolling-memory/ # 检索调试器、记忆总览、L1/L2/L3 三层记忆浏览器、评测实验室与双榜单
        └── help/               # 随仓双语开源文档中心
```

---

## 2. 从源码编译与运行（Build from Source）

### 2.1 环境要求

- **Go**：`>= 1.26`（纯 Go 编译，`CGO_ENABLED=0`，无需本地安装 C 编译器或外部数据库）
- **Node.js**：`>= 20`（推荐使用 `npm` 或 `pnpm` 构建前端资源）

### 2.2 方式一：通过 `rolling-memory panel` 自动从源码编译启动（推荐）

如果你已经在仓库根目录编译了 `rolling-memory` CLI，并在 `admin/web` 下完成了前端构建，`panel` 子命令会自动识别仓库内的 `admin/` 源码并编译启动：

```bash
# 1. 构建前端静态资源（产出至 admin/web/dist/）
cd admin/web
npm install
npm run build
cd ../..

# 2. 编译主 CLI 并启动控制台（自动从 admin/ 源码编译后端并拉起 :9006 与 :8008）
CGO_ENABLED=0 go build -trimpath -o rolling-memory ./cmd/rolling-memory
./rolling-memory panel
```

常用管理命令：
- `./rolling-memory panel status`：查看控制台前后端进程与端口状态
- `./rolling-memory panel stop`：停止控制台前后端进程

### 2.3 方式二：手动分别编译并启动后端与前端

你也可以直接在 `admin/` 目录下独立编译和运行后端与前端（适合二次开发与调试）：

**1. 编译并启动后端 API 服务（默认 `http://127.0.0.1:9006`）**

```bash
cd admin
CGO_ENABLED=0 go build -trimpath -o bin/rolling-admin ./cmd
./bin/rolling-admin serve
```

**2. 启动前端服务（默认 `http://localhost:8008/#/admin`）**

- **开发热更新模式（Vite Dev Server）**：
  ```bash
  cd admin/web
  npm install
  npm run dev
  ```
- **生产构建与静态托管模式**：
  ```bash
  cd admin/web
  npm install
  npm run build
  # 使用主 CLI 静态托管构建好的 dist 目录（免 Node 运行时）
  ../../rolling-memory panel --serve-web ./dist --web-port 8008
  ```

启动后在浏览器访问 `http://localhost:8008/#/admin`，使用登录页提示的本机默认演示账号即可登录。

> **安全提示（默认仅本机回环访问）**：
> 控制台可读写你本机的全部记忆金库，且开箱默认账号公开写明，因此后端默认仅绑定 `127.0.0.1:9006` 并拒绝非本机 Host 请求。如需在局域网或远程设备访问，请先在后台修改默认管理员密码，再通过环境变量 `SERVER_HOST=0.0.0.0`（或修改 `admin/config/config.yaml` 中的 `server.host`）放开监听地址。

---

## 3. 后台框架来源与合规声明（Framework Provenance）

本目录（`admin/`）并非从零自研的后台框架，其来源与边界如下：

| 层次 | 来源项目 | 许可证 | 在本项目中的关系与修改 |
|---|---|---|---|
| **后台与服务端框架** | **AI GO ADMIN**（`ai-go-admin`，Go + Gin + PostgreSQL 后端，Vue 3 + TypeScript + Vite + Pinia + Element Plus 前端） | Apache-2.0 | **衍生开发**：保留其 `LICENSE` 与目录骨架，改造为本地 SQLite 零配置存储，新增 Rolling Memory 的记忆控制台、ROI 监控、记忆图谱、评测实验室、权限节点、路由与品牌资源；原项目中的支付 / 商城等无关代码已移除 |
| **后台 UI 血统** | **BuildAdmin**（PHP 版同源后台，AI GO ADMIN 即其 Go 版本重写） | Apache-2.0（Copyright 妙码生花） | **链路来源**：控制台整体交互与视觉体系承自 BuildAdmin；其中**插件清单页**（`web/src/views/admin/plugin/catalog/`）按 BuildAdmin `v2.3.9`（commit `4686522`）移植并改写 |
| **本项目自有代码** | — | Apache-2.0 | `internal/rmconsole`、`web/src/views/admin/plugin/rolling-memory/`、`web/src/views/admin/dashboard/` 及相关路由 / API / 权限 / 品牌资源均为本项目原创 |

- **完整第三方归属与逐文件修改说明**：[`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md)（含来源地址、上游版本与 commit、派生文件清单）。
- **根级合规声明**：[`../NOTICE`](../NOTICE) 第 2 节「Optional Web Administration Framework」同时登记 AI GO ADMIN 与 BuildAdmin。
- **未复制内容**：BuildAdmin 与 AI GO ADMIN 的商标、Logo、演示图片、支付图标、账号与商城 API 均未纳入本仓库。

### 鸣谢与上游开源项目地址

| 上游项目 | 仓库地址 | 许可证 / 版权 |
|---|---|---|
| **AI GO ADMIN**（后台框架基座） | [GitHub](https://github.com/ai-go-hub/ai-go-admin) · [Gitee](https://gitee.com/ai-go-hub/ai-go-admin) · [AtomGit](https://atomgit.com/ai-go-hub/ai-go-admin) · [文档站](https://www.ai-go-hub.com/) | Apache-2.0 © AI GO ADMIN contributors |
| **BuildAdmin**（UI 血统 / 插件清单页来源） | [Gitee](https://gitee.com/wonderful-code/buildadmin) · 官网 [buildadmin.com](https://buildadmin.com/) | Apache-2.0 © 妙码生花 |
