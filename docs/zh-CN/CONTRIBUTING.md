# 贡献指南

**[English](../../CONTRIBUTING.md) · [简体中文](CONTRIBUTING.md)**

感谢你考虑为本项目做出贡献。

## 基本原则

- 运行时保持零依赖：随包二进制自包含，其模块集合为下列固定白名单（不引入 CLI 框架、ORM、日志库或 diff 库）。可选扩展可以存在，但必须保持可选。
- 本地模式绝不发起任何非环回（non-loopback）请求。
- 保护本地存储、备份以及与外部服务共享的数据中的敏感信息。
- 每条记忆单元都必须有逐字锚点，不得新增「稍后补锚点」的路径。
- 写入只增不改：不得新增修改既有单元内容的代码路径。
- 对同一 DB / scope / cue / asof / caps，读取路径必须保持确定性。
- `absent` 必须保持为一等状态，与错误严格区分。

## 开发

```bash
go build ./...
go vet ./...
gofmt -l .                                  # 必须无输出
go test ./... -count=1
go test ./internal/golden -count=1          # 冻结黄金夹具（见 testdata/golden/MANIFEST.json）
CGO_ENABLED=0 go build -trimpath ./cmd/rolling-memory   # 可移植性冒烟（纯 Go，无 CGO）
```

依赖是固定在 `go.mod` / `go.sum` 中的白名单：`modernc.org/sqlite`（纯 Go SQLite）、
`golang.org/x/text`（NFKC / casefold）、`golang.org/x/crypto`（blake2b）与
`github.com/pelletier/go-toml/v2`。CLI、JSON-RPC 服务与词元相似度匹配器均为刻意手写。

测试只使用合成数据并自我隔离：运行于临时 HOME / XDG / 数据库根，绝不打开真实的
`~/.local/share/rolling-memory/memory.db`。

黄金对拍：`testdata/golden/` 由 `MANIFEST.json` 封存（来源信息 + 逐文件 sha256 + `seal_sha256`；
场景清单见 `testdata/golden/README.md`），测试套件会回放并校验密码学封条。绝不从临时测试运行
重新生成期望值。语义契约变更必须显式重封基线并记录来源信息。

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
gofmt -l .                                    # 3. 必须无输出
go test ./... -count=1                        # 4. 单元 + 进程级 e2e + 黄金对拍
go test ./internal/golden -count=1            # 5. 仅黄金回放（4 的子集，迭代时常用）
go test -race ./internal/... -count=1          # 6. 引擎包的竞态检测
go test ./internal/... -shuffle=on -count=2    # 7. 顺序无关（无测试间耦合）
# 8. 五目标交叉编译冒烟：全部发布目标均为纯 Go 构建
for t in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64; do
  CGO_ENABLED=0 GOOS="${t%/*}" GOARCH="${t#*/}" go build -trimpath -o /dev/null ./cmd/rolling-memory || exit 1
done
```

- 测试只使用合成数据，默认隔离 HOME / XDG / DB 根——绝不指向真实记忆库，也绝不让新测试写出临时根之外。
- 文档中引用的数字（场景数、文件数）来自夹具本身（`testdata/golden/MANIFEST.json` 与
  `testdata/golden/README.md`）；请先在那里更新。

## Pull Request

- 一个 PR 只做一件事；新增行为与隐私边界须附测试。
- 改动公共契约（工具 schema、回执形状、同步契约）时，须同步更新 `docs/` 下对应文档与 `CHANGELOG.md`。
- 改动 schema 时须给出迁移策略（当前 schema 为 v1；谨慎对待存量 DB）。
- 测试与示例中不得包含真实个人数据、API Key、对话记录或私有 URL。

## 许可与签署

本项目第一方代码对使用者按 [PolyForm Noncommercial License 1.0.0](../../LICENSE) 授权（SDK 按 [Apache License 2.0](../../sdk/LICENSE)），同时项目另行销售商业许可。为了在不设 CLA 表单的前提下支撑这种双授权模式，**入站贡献按 Apache License 2.0 接收**：提交贡献即表示你同意以 Apache-2.0 提供该贡献，维护者因此可将其纳入项目的非商业与商业许可条款再分发。每次提交请使用 `git commit -s`
签署（Developer Certificate of Origin 1.1）；无需单独的 CLA。请保留 [NOTICE](../../NOTICE) 中的第三方署名。

## 安全

请私下向维护者报告安全问题，不要在公开 issue 中披露漏洞细节。见 [SECURITY.md](SECURITY.md)。
