# Rolling 品牌包（rolling-brand）

把 Rolling 基础品牌（登录页品牌区、logo、favicon、欢迎语、站点名/备案号）从核心外置为可装卸插件。

## 形态

- `kind: code`。前端品牌面走**构建期 overlay**：`plugins/rolling-brand/brand/**` 按 `web/src/**`
  相对路径 1:1 镜像；宿主 Vite 的 `brand-overlay` 解析器在 `VITE_BRAND=rolling-brand` 时优先命中，
  未命中回退 `web/src/**`；空值 = 上游默认。**不覆盖任何核心文件。**
- 后端只提供公开品牌信息（`/plugin/rolling-brand/brand.json` 与品牌资源）；`migrations/1_init`
  建插件自管的 `rolling_brand_settings` 表，不写核心表/核心迁移。

## 启用 / 停用

```bash
# 启用（构建期）：本机 web/.env.development.local（gitignored），或产品构建环境显式设置
VITE_BRAND=rolling-brand
# 停用：留空并重新构建
VITE_BRAND=
```

卸载插件源树后，overlay 目录不存在，解析器自动回退上游默认外观；无需备份/恢复核心文件。

## 安装 / 卸载

```bash
go run ./cmd plugin verify plugins/rolling-brand
go run ./cmd plugin install plugins/rolling-brand --dry-run
go run ./cmd plugin install plugins/rolling-brand
go run ./cmd plugin uninstall rolling-brand --dry-run
go run ./cmd plugin uninstall rolling-brand
```

默认卸载保留 `rolling_brand_settings` 表与数据、插件迁移版本表、`.store` 包；`--purge` 才按
`migrations/1_init.down.sql` 回滚。品牌配置不写核心 `configs`，卸载不会影响核心站点配置。

## 插件中心 / 分发

- 标准包：`plugins/packages/rolling-brand-1.0.0.zip`（catalog v1 包，含 `files.sha256`，未签名）。
- 统一清单：`plugins/catalog.yaml`（`ai-go-plugin-catalog/v1`，`package.path` 相对清单目录）。
- 配置来源并同步清单：
  ```bash
  go run ./cmd plugin catalog sync
  ```
- **源码已在仓**（框架同步或本项目开发）：用目录源安装，catalog 安装会因 `existing_targets` 拒绝：
  ```bash
  go run ./cmd plugin install plugins/rolling-brand
  ```
- **目标仓没有源码**（纯包安装）：插件中心「插件清单」页安装；未签名需勾选「允许未签名」
  （CLI 为 `--allow-unsigned`）。
- 下游按需安装；品牌是否启用由构建期 `VITE_BRAND=rolling-brand` 决定，不强制改写产品品牌。

## 文件

- `plugin.yaml` / `plugin.go`：插件清单与公开品牌信息端点
- `brand/`：前端 overlay（登录页、logo、favicon、`main.ts` 入口与 `runtime.ts` 欢迎语覆盖）
- `web/lang/`：品牌语言包（`plugin.rolling-brand.*`）
- `migrations/`：插件自管品牌设置表
- `FILES.md` / `CORE-CONFLICTS.md`：两件套声明
