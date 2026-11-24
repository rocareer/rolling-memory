# 贡献指南

**[English](../../CONTRIBUTING.md) · [简体中文](CONTRIBUTING.md)**

感谢你考虑为本项目做出贡献。

## 项目状态与开发模式

Rolling Memory 处于**高速、活跃演进期**（尚未发布 1.0）。本仓库是项目的**公开发布面**：接收源自内部开发树的净化源码快照与打标发布，每次快照发布前都会进行私有材料扫描。因此这里的提交历史呈现的是发布/快照提交，而非逐特性的完整开发历史；所有面向用户的变更都记录在[变更日志](https://github.com/rocareer/rolling-memory/blob/main/CHANGELOG.md)与[发布页](https://github.com/rocareer/rolling-memory/releases)。

由于引擎演进很快，API、文件格式与行为可能在不同版本间发生变化——每处变化都会记录在变更日志中，我们也尽力让每个版本可复现、可基准对照。感谢理解与支持，也诚挚欢迎参与：问题反馈、基准复现、文档修订与 Pull Request 都同样宝贵（见下文）。谢谢你和我们一起把这个项目做下去。

## 基本原则

- 运行时保持零依赖：随包二进制自包含，其模块集合为下列固定白名单（不引入 CLI 框架、ORM、日志库或 diff 库）。可选扩展可以存在，但必须保持可选。
- 本地模式绝不发起任何非环回（non-loopback）请求。
- 保护本地存储、备份以及与外部服务共享的数据中的敏感信息。
- 每条记忆单元都必须有逐字锚点，不得新增「稍后补锚点」的路径。
- 写入只增不改：不得新增修改既有单元内容的代码路径。
- 在金库、作用域、检索线索、时间点和交付设置相同的情况下，读取路径必须保持确定性。
- `absent` 必须保持为一等状态，与错误严格区分。

## 开发

```bash
go build ./...
go vet ./...
gofmt -l cmd internal sdk                     # 必须无输出
go test ./... -count=1
CGO_ENABLED=0 go build -trimpath ./cmd/rolling-memory   # 可移植性冒烟（纯 Go，无 CGO）
```

依赖是固定在 `go.mod` / `go.sum` 中的白名单：`modernc.org/sqlite`（纯 Go SQLite）、
`golang.org/x/text`（NFKC / casefold）、`golang.org/x/crypto`（blake2b）与
`github.com/pelletier/go-toml/v2`。CLI、JSON-RPC 服务与词元相似度匹配器均为刻意手写。

测试只使用合成数据并自我隔离：运行于临时 HOME / XDG / 数据库根，绝不打开真实的
`~/.local/share/rolling-memory/memory.db`。

格式向量：`testdata/golden/vectors/` 保存 id / 时间辅助函数（`internal/ids`）钉住的 JSON 期望值。
它们是经评审的输入而非生成产物：绝不从临时测试运行刷新；语义契约变更须在同一 PR 中显式评审并更新向量。

## 版本与弃用

- 发布遵循语义化版本（`MAJOR.MINOR.PATCH`），打 `vX.Y.Z` tag。
- 每个版本都有非空的 `CHANGELOG.md` 段落；GitHub / Gitee Release 正文取自该段落，不手工另写。
- 弃用须在 `CHANGELOG.md` 与对应 `docs/` 页面公告，至少保留一个次版本，并在下一个主版本移除。
  被移除的别名一律 fail-closed，不得新增静默回退。
- 项目处于 `0.y.z` 期间，次版本可以包含有文档记录的破坏性变更，但 changelog 必须写明迁移步骤。
- 公共契约（工具 schema、回执形状、同步契约、模式名）随版本一起版本化；改动须在同一 PR 中
  更新文档与 changelog。

## PR 检查清单（请求评审前逐项执行）

以下命令均在仓库根执行且必须全绿——这是分支必须通过的校验集合：

```bash
go build ./...                                # 1. 可编译
go vet ./...                                  # 2. 静态检查
gofmt -l cmd internal sdk                     # 3. 必须无输出
go test -short ./... -count=1                 # 4. 单元 + 进程级 e2e
go test -short -race ./internal/... -count=1  # 5. 引擎包的竞态检测
go test -short ./internal/... -shuffle=on -count=2  # 6. 顺序无关（无测试间耦合）
# 7. 五目标交叉编译冒烟：全部发布目标均为纯 Go 构建
for t in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64; do
  CGO_ENABLED=0 GOOS="${t%/*}" GOARCH="${t#*/}" go build -trimpath -o /dev/null ./cmd/rolling-memory || exit 1
done
```

公开工作流以相同的命令集运行，并统一带 `-short`。规模与性能测量夹具（用大型合成语料记录性能读数）是测量仪器而非正确性门禁，因此不进入 CI；评审性能改动时请去掉 `-short` 再跑一遍。

- 测试只使用合成数据，默认隔离 HOME / XDG / DB 根——绝不指向真实记忆库，也绝不让新测试写出临时根之外。
- 文档中引用的数字（场景数、文件数）来自夹具本身（`testdata/golden/vectors/`）；请先在那里更新。

## Pull Request

- 一个 PR 只做一件事；新增行为与隐私边界须附测试。
- 改动公共契约（工具 schema、回执形状、同步契约）时，须同步更新 `docs/` 下对应文档与 `CHANGELOG.md`。
- 改动 schema 时须给出迁移策略（当前 schema 为 v1；谨慎对待存量 DB）。
- 测试与示例中不得包含真实个人数据、API Key、对话记录或私有 URL。

### 补丁如何被采纳，以及如何署名

本仓库是发布快照，所以 PR **不能在这里直接合并**：合并会被下一次快照覆盖，且发布校验要求公仓树与导出树逐字节一致。因此补丁走**采纳**而非合并：

- **只取最小可用子集。** 如果一个 PR 把修复与无关的格式重排混在一起，我们只采纳修复，丢掉重排——重排既让改动无法快速复核，也会与并行开发冲突。
- **采纳内容在 `CHANGELOG.md` 的对应分类下署名**，写明你的账号与 PR 号，例如
  `(Reported and suggested by @you in #123; adopted and rewritten in the source tree.)`。
- **若你的提交内容基本原样落地，采纳提交会带 `Co-authored-by: 姓名 <邮箱>` 尾注**；若我们做了结构性重写（结构、命名或范围变化），则以变更日志致谢为署名，diff 不会与你的补丁一致。
- **已按「已采纳」关闭的 PR 请不要重提精简版**：修复已在源码树里，再提一份只是重复劳动。
- **不采纳的改动给理由，不沉默。** 如果前提不成立——例如为本来就正常的行为做兼容性重写——评审会明确说明。

最容易合并的贡献形式是：针对我们发布的文件、单一关注点的小 diff（一个 bug 加它的测试，或一处文档修订）。

## 许可与签署

本项目第一方代码（含 `sdk/` 下客户端 SDK，见 `sdk/LICENSE`）对使用者按 [PolyForm Noncommercial License 1.0.0](../../LICENSE) 授权，同时项目另行销售商业许可。为了在不设 CLA 表单的前提下支撑这种双授权模式，**入站贡献按 Apache License 2.0 接收**：提交贡献即表示你同意以 Apache-2.0 提供该贡献，维护者因此可将其纳入项目的非商业与商业许可条款再分发。每次提交请使用 `git commit -s`
签署（Developer Certificate of Origin 1.1）；无需单独的 CLA。请保留 [NOTICE](../../NOTICE) 中的第三方署名。

## 安全

请私下向维护者报告安全问题，不要在公开 issue 中披露漏洞细节。见 [SECURITY.md](SECURITY.md)。
