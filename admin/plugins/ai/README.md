<!-- plugin: ai v0.2.0 -->
# AI 网关插件（ai）

把 `rocareer/ai`（PHP/Webman 的 AI 路由插件）提取为 ai-go-admin 的标准插件。本文件是插件的运维与
验收手册：能力三态、表与迁移、安装 / 升级 / 回滚、公开协议面、后台路由与权限节点、配置键全表、
与源实现的差异（含限流与 **429 的隔离口径**）、安全口径。

> **归属标记与行号**：以目录源安装时源树即落位目标，安装器会对插件源树逐文件**前插 / 刷新**一行归属
> 标记（`plugin: ai v<版本>`；白名单格式含 `.go` / `.vue` / `.yaml` / `.sql` / `.md`，幂等判据 =
> 文件头已是同一 version 的标记，否则前插，见 `internal/plugin/cli/marker.go`）。⇒ 安装后本文件的
> **行号会整体后移**，故全文只引具名锚点（键名 / 路由 / 文件路径），**不引行号**；版本号提升会再次
> 刷新标记，源树与前端落位副本要一起提交（逐文件 sha256 保持一致）。

## 一、能力三态

| 能力 | v1（0.1.0） | 本批增量（迁移 2..6） | 落点 / 差异要点 |
|---|---|---|---|
| 渠道管理 | 已落地 | 渠道增 `driver` / `supplier_id` / `rpm_limit` / `tpm_limit` 四列 | `/admin/plugin/ai/channel/*` + 渠道页 |
| 模型管理 | 已落地 | `type` 闭集增 `tts` | `/admin/plugin/ai/model/*` + 模型页 |
| 调度与熔断 | 已落地 | 逐候选准入（渠道级 RPM / TPM），被拒候选不自增 attempt | `gate.go` 挂 `router.gate` |
| 计费与用量 | 已落地 | 部分计费标记 `billed_partial`（有块无收尾的补偿计费）+ `call_type='tts'` | `billing.go` / `service.go` |
| 日志 | 已落地 | 用量 / 日志出口回显部分计费标记 | `/admin/plugin/ai/usage-log/*` + 用量页 logs tab |
| 监控 | 已落地 | 接入设置对话框增本批全部新键并回读生效值 | `/admin/plugin/ai/overview/index` + 总览页 |
| 供应商（厂商）继承 | 未覆盖 | 已落地：厂商表 + 渠道四字段「渠道非空优先、空则继承厂商」逐字段合并 | `/admin/plugin/ai/supplier/*` + 厂商页 |
| API Key 管理 | 未覆盖 | 已落地：按 Key 鉴权 + 计量归因（`usage.source='key:<source>'`）+ 按 Key 限流覆盖 | `/admin/plugin/ai/apikey/*` + API Key 页 |
| 异步任务 / 批量 embedding | 未覆盖 | 已落地：DB 表 + 进程内 worker；批量 embedding 走异步分片并可导出 | `/admin/plugin/ai/task/*` + 任务页 |
| TTS / 语音 | 未覆盖 | 已落地：音色表 + `dashscope_tts` 驱动 + 试听 + 公开音频面 | `/admin/plugin/ai/voice/*` + 语音页 |
| 多驱动矩阵 | 未覆盖 | 已落地：驱动注册表 + Responses 协议 + DeepSeek / DashScope（OpenAI 兼容端点） | `driver_registry.go` / `driver_responses.go` |
| 渠道级 RPM / TPM | 未覆盖 | 已落地（**进程内单实例**，见差异节） | `gate.go` + `ratelimit.go` |
| 跨进程强一致的 RPM / TPM | — | **仍未覆盖（明确不做）** | 单实例 + 事后扣减；跨进程要 Redis 客户端，宿主 `go.mod` 无该依赖 |
| 外置队列（Redis / AMQP） | — | **仍未覆盖（明确不做）** | 宿主无队列基建 ⇒ 用「DB 表 + 进程内 worker」：单实例语义、进程被杀靠租约复位续跑 |
| STT / 图像多模态 | — | **仍未覆盖（明确不做）** | 源侧驱动契约只有语音合成能力，无转写 / 图像入口 |
| 渠道 / 厂商 `settings` jsonb、`extra.model_map` 覆盖链 | — | **仍未覆盖（明确不做）** | 渠道 / 厂商表无 `settings` 列；驱动侧以**静态** modelMap 替代 |
| 平台级 / 应用级 Key 区分、按凭证防爆破 | — | **仍未覆盖** | 本插件只有两类 Key（宿主配置的全局 Key、DB 签发 Key）；未过鉴权的请求不占配额（与源一致） |

## 二、表与迁移

插件自管迁移；业务表名 `<prefix>plugin_ai_<entity>`，版本表 `<prefix>plugin_ai_migrations`（不列业务表）：

| 编号 | 文件 | 内容 |
|---|---|---|
| 1 | `migrations/1_init.{up,down}.sql` | 5 张业务表：`channel` / `model` / `model_channel` / `usage` / `breaker_event` |
| 2 | `migrations/2_usage_partial_billing.{up,down}.sql` | `usage` 加 `billed_partial smallint not null default 0` |
| 3 | `migrations/3_channel_driver_supplier_gate.{up,down}.sql` | 新建 `supplier`；`channel` 加 `driver` / `supplier_id` / `rpm_limit` / `tpm_limit` + 索引 |
| 4 | `migrations/4_api_key.{up,down}.sql` | 新建 `api_key`（`uk(key_hash)`、`uk(source) where source <> ''`） |
| 5 | `migrations/5_task.{up,down}.sql` | 新建 `task`（状态索引；`request_id` 非空部分唯一 + 租约列） |
| 6 | `migrations/6_voice.{up,down}.sql` | 新建 `voice`；放宽 `model.type` 与 `usage.call_type` 的 CHECK 至含 `tts` |

⇒ **9 张业务表**（channel / model / model_channel / usage / breaker_event / supplier / api_key / task / voice），
版本表另计。写迁移时的三条编排纪律（踩了就是静默事故）：

1. **编号必须是 1..N 连续前缀，且 up/down 成对**。运行器只推「版本 > 当前」的待执行版本
   （`internal/plugin/cli/migrate_run.go`）⇒ **跳号会让该版本永久静默跳过**；而静态校验只检查「编号唯一 + up/down 成对」，
   **不校验连续性** ⇒ 连续性靠人工自律，发布前机械自检（下面这条命令**必须逐字照抄**）：
   `ls plugins/ai/migrations | sed -n 's/^\([[:digit:]][[:digit:]]*\)_.*/\1/p' | sort -n | uniq -c`
   （应得 1..N 每号恰一对）。**不要写成 GNU 记法 `[0-9]\+`**：BSD sed（macOS 自带）的 BRE 把 `\+`
   当字面加号，`[0-9]\+` 实为「数字后跟一个字面 +」——迁移文件名永不含 `+` ⇒ 该命令恒 0 行、
   rc=0；而本段相邻的自检（`gofmt -l` / `go vet`）通过信号同样是空输出，空输出会被读成「通过」，
   连续性守卫就此静默空转。POSIX 安全写法是三处通用口径：`\([[:digit:]][[:digit:]]*\)`（本行所用）
   或 `sed -nE 's/^([0-9]+)_.*/\1/p'`。
2. **多批改动同时带迁移时，必须全部落地后只做一次安装**。迁移的执行时机**只有安装编排的第 ⑥ 步**
   （`internal/plugin/cli/install_run.go`）：**没有任何命令能对「已装插件」单独应用新迁移**，
   `plugin migrate-force <名> <版本>` 只改版本表标记、既不执行也不回滚 SQL。
3. **版本升级会触发源树归属标记前插 / 叠加**（见文首注）：改版本号就要接受「源树 + 落位副本的全部文件
   头部刷新标记」，并把该批刷新一起提交，否则前端 sha256 同步门禁会红。

迁移文件自身的硬约束（运行器校验，见 `internal/plugin/cli/migrate.go`）：整文件隐式事务（文件内**不得**
再写 `BEGIN` / `COMMIT`）、不得 `CONCURRENTLY`、不得写宿主 `admin_rules` / `admin_groups`、表名一律用
`__PREFIX__` 占位符；down 逐条逆操作（先删约束 / 索引再删列、先删子表再删主表），**只删本插件自建对象**。

## 三、安装、升级与回滚

```bash
# 首次安装（台账无行）
go -C ai-go-admin run ./cmd migrate up                            # 宿主核心链（插件机制表）
go -C ai-go-admin run ./cmd plugin install plugins/ai --enable
go -C ai-go-admin run ./cmd plugin verify plugins/ai
go -C ai-go-admin run ./cmd plugin list
```

安装做的事：落位前端源与语言包副本 → 执行插件迁移（第 ⑥ 步，唯一执行点）→ 重写接线注册文件 →
写台账（`--enable` 置启用）→ 写菜单 / 权限节点 / `configs` 分组。**改装后必须重新编译并重启后端**
才生效（接线的真相在编译产物）；运行期闸按库内台账判定（≤1s 缓存，`internal/plugin/host/gate.go`），
库内无台账行或非 enabled 时插件路由一律 404。

### 升级（已装 → 新版本，含新增迁移）

已装插件**不能**直接重跑安装：台账已有行时安装器硬拒「重复安装不接受」（`internal/plugin/cli/dbwrite.go`），
而 `--force` 只放行「与工作树同名且内容不同」的文件冲突、**不作用于台账**。真实路径是三步：

```bash
go -C ai-go-admin run ./cmd plugin disable ai                 # enabled 必须先禁用（硬门禁）
go -C ai-go-admin run ./cmd plugin uninstall --keep-files ai
#   ↑ 不带 --purge：保留插件表与数据、保留迁移版本表；--keep-files：保留源与落位副本
# 更新插件（新增 migrations/2..6、版本号提升：0.1.0 → 0.2.0，见 plugin.yaml 的 version，
#   plugin.go 的 Version 常量与之逐字对齐；版本一变，安装器会在源树 + 落位副本逐文件前插 / 叠加
#   归属标记行——该批文件头改写要一起提交，否则 check-plugin-frontend-sync.sh 的 sha256 比对会红）
go -C ai-go-admin run ./cmd plugin install plugins/ai --enable
```

**两笔必须预先接受的代价**（卸载的 DB 侧清理面是确定的，`internal/plugin/cli/dbwrite.go` /
`uninstall_run.go`）：

1. **`configs` 的 `plugin.ai` 分组被整组删除**：网关 API Key、熔断阈值 / 冷却、超时、币种、限流三参数、
   渠道 Gate 两缺省、worker 两键**全部回到 manifest 默认值**，重装后必须逐项重配。
2. **`admin_rules` 的归属行与权限节点被删并重建，节点 ID 全变**：脚本只清各角色组 `rules` CSV 里的
   悬挂 ID、**不恢复授权关系**（卸载末段明确写「复装不是还原」）⇒ 非超管角色组需重新勾授权。

### 失败现场与恢复顺序

`install` 的补偿回滚只撤销**本次安装自己的动作**（删已落位文件 → 回滚本次应用的迁移 → drop 本次新建的
表 → 撤接线 → 删除本次写下的台账 / 归属行），**不会**恢复卸载阶段已删掉的 `configs` 分组与 `admin_rules`；
迁移留下 dirty 或补偿失败时还会保留现场、台账停留 failed（半装清场入口是 `plugin uninstall` 加 `--force`，
仅用于 failed 现场）。

⇒ **升级失败的现场 = 插件未装 + 接入设置全丢 + 授权全丢**，比「升级前」差得多。故升级前先 dump 回滚凭据：

```sql
-- ① configs 的 plugin.ai 分组
SELECT "name", "value" FROM "configs" WHERE "group" = 'plugin.ai';
-- ② admin_rules 中归属本插件的整棵树（含 id / pid / name / title / type / status）
SELECT * FROM "admin_rules" WHERE "plugin" = 'ai';
-- ③ 各角色组的授权 CSV（旧 ID 的对照表）
SELECT "id", "rules" FROM "admin_groups";
```

恢复顺序（写死，别换）：**① 先把插件重装起来（`install ... --enable`）→ ② 按 dump 逐行恢复 `configs` 的
`plugin.ai` 分组值 → ③ 按 dump 恢复授权**。第 ③ 步要按**节点 name** 对齐：重装后节点 `name` 不变、`ID`
全变，把 dump 里「角色组 → 旧 ID」的对照按 name 映射成新 ID，再写回该组的 `rules` CSV（或直接在页面上重勾）。

## 四、公开协议面（MountPublic，无后台前缀）

| 路由 | 说明 |
|---|---|
| `POST /ai/v1/chat/completions` | 非流式 JSON；`stream=true` 走 SSE（逐帧转发 + `X-Accel-Buffering: no`） |
| `POST /ai/v1/embeddings` | `input` ≤ 64 条（同步路径；发给上游时 20 条 / 片串行） |
| `GET /ai/v1/models` | 只列 `status=1` 且 `type=chat` 的逻辑模型（≤100 条，不分页）；本批起按该 Key 的 `model_scope` 白名单过滤 |
| `POST /ai/v1/audio/speech` | 本批新增：TTS 合成，音频字节流透传（`Content-Type: audio/*`） |

鉴权与错发形状（公开面唯一挂链点：**按 Key 鉴权 → 按 Key 限流 → 业务处理器**）：

- 先按 `key_hash` 查 `plugin_ai_api_key`（`status=enabled` 且未过期）⇒ 命中即把调用方身份写进
  `usage.source='key:<source>'`，并**异步**刷新 `last_used_at`（最终一致、不阻塞响应、允许丢更新）；
  未命中回落宿主 `configs` 的 `plugin_ai_api_key`（该路径 `source='key:gateway'`）；
  密钥未配置 ⇒ 503 fail-closed；格式错 / 不匹配 ⇒ 401。**查库出错必须 fail-closed**，不得静默回落全局 Key 比对。
- 错误形状：真实 HTTP 状态码 + `{"error":{"message","type","code"}}`（OpenAI 形状），
  **不沿用**宿主的「HTTP 200 + code=1」。
- 请求体先套 8MiB 上限：超限 413 `request_too_large`，且不把请求原文写进任何回执或日志。
- 鉴权中间件的查库面走 `deps` 的注入点（缺省从 `loadStore` 取 `*Store`，见 `apikey_auth.go`）：
  无库单测据此注入假 store，于是**协议用例仍经生产挂载链**（`mountPublic`）跑，
  而不是在测试里复制一份链路。

### 429 的两种来源与隔离口径（**断言 429 前必读**）

公开面出现 429 有**两个**来源，形状不同、次序固定：

1. **宿主全局节流**（`cmd/serve/serve.go` 的 `middleware.Throttle()`）：默认 `max=10`/s、`burst=10`、
   `ignore_url=false` ⇒ **按（客户端 IP + URL）**计数（`config/throttle.yaml`），超限按**宿主形状**输出
   429（响应文案来自该配置的 `message`，**不是** OpenAI 形状）。它挂在插件 handler **之前**，一定会抢先。
2. **插件按 Key 限流**（`ratelimit.go`，见差异节 (b)）：超限才输出本插件的 OpenAI 形状 429
   （`type=rate_limit_error`、`code=rate_limit_exceeded`、`Retry-After: 60`）。

⇒ **任何要断言「本插件限流生效」的场景，必须先隔离宿主节流**：验证实例的 `.env.yaml` 覆盖
`throttle.enabled=false`，或把 `throttle.max` 调到远高于被测并发 / 速率。否则第 N+1 次会先被宿主形状的 429
抢走，断言 `code=rate_limit_exceeded` 必然假红。**反向断言同样要做**：把 `throttle.enabled` 恢复 `true`
跑同一场景 ⇒ 先看到宿主形状 429，即证明「隔离步骤是必要的」，而不是画蛇添足。

## 五、后台路由与权限节点

后台路由共 **42 条**，全部挂 `AdminAuth` → `AdminPermission` → 业务处理器，前缀 `/admin/plugin/ai`
（`plugin.go` 的 `adminRoutes` 与 `plugin.yaml` 的 `menus[].name` 逐字对齐）：

| 路由 | 权限节点 |
|---|---|
| `POST /admin/plugin/ai/channel/list` | `plugin/ai/channel/read` |
| `GET /admin/plugin/ai/channel/get/:pk` | `plugin/ai/channel/read` |
| `POST /admin/plugin/ai/channel/create` | `plugin/ai/channel/create` |
| `POST /admin/plugin/ai/channel/update/:pk` | `plugin/ai/channel/update` |
| `POST /admin/plugin/ai/channel/delete` | `plugin/ai/channel/delete` |
| `POST /admin/plugin/ai/channel/switch` | `plugin/ai/channel/switch` |
| `POST /admin/plugin/ai/channel/probe` | `plugin/ai/channel/probe` |
| `POST /admin/plugin/ai/channel/breaker` | `plugin/ai/channel/breaker` |
| `POST /admin/plugin/ai/channel/events` | `plugin/ai/channel/events` |
| `POST /admin/plugin/ai/model/list` | `plugin/ai/model/read` |
| `GET /admin/plugin/ai/model/get/:pk` | `plugin/ai/model/read` |
| `POST /admin/plugin/ai/model/create` | `plugin/ai/model/create` |
| `POST /admin/plugin/ai/model/update/:pk` | `plugin/ai/model/update` |
| `POST /admin/plugin/ai/model/delete` | `plugin/ai/model/delete` |
| `POST /admin/plugin/ai/model/bind` | `plugin/ai/model/bind` |
| `POST /admin/plugin/ai/usage/list` | `plugin/ai/usage/read` |
| `POST /admin/plugin/ai/usage-log/list` | `plugin/ai/usage-log/read` |
| `POST /admin/plugin/ai/usage-log/detail` | `plugin/ai/usage-log/detail` |
| `POST /admin/plugin/ai/overview/index` | `plugin/ai/overview/index` |
| `POST /admin/plugin/ai/settings/save` | `plugin/ai/settings/save` |
| `POST /admin/plugin/ai/supplier/list` | `plugin/ai/supplier/read` |
| `GET /admin/plugin/ai/supplier/get/:pk` | `plugin/ai/supplier/read` |
| `POST /admin/plugin/ai/supplier/create` | `plugin/ai/supplier/create` |
| `POST /admin/plugin/ai/supplier/update/:pk` | `plugin/ai/supplier/update` |
| `POST /admin/plugin/ai/supplier/delete` | `plugin/ai/supplier/delete` |
| `POST /admin/plugin/ai/apikey/list` | `plugin/ai/apikey/read` |
| `GET /admin/plugin/ai/apikey/get/:pk` | `plugin/ai/apikey/read` |
| `POST /admin/plugin/ai/apikey/create` | `plugin/ai/apikey/create` |
| `POST /admin/plugin/ai/apikey/update/:pk` | `plugin/ai/apikey/update` |
| `POST /admin/plugin/ai/apikey/delete` | `plugin/ai/apikey/delete` |
| `POST /admin/plugin/ai/apikey/reset/:pk` | `plugin/ai/apikey/reset` |
| `POST /admin/plugin/ai/apikey/switch` | `plugin/ai/apikey/switch` |
| `POST /admin/plugin/ai/task/list` | `plugin/ai/task/read` |
| `GET /admin/plugin/ai/task/get/:pk` | `plugin/ai/task/read` |
| `POST /admin/plugin/ai/task/submit` | `plugin/ai/task/submit` |
| `GET /admin/plugin/ai/task/export/:pk` | `plugin/ai/task/export` |
| `POST /admin/plugin/ai/voice/list` | `plugin/ai/voice/read` |
| `GET /admin/plugin/ai/voice/get/:pk` | `plugin/ai/voice/read` |
| `POST /admin/plugin/ai/voice/create` | `plugin/ai/voice/create` |
| `POST /admin/plugin/ai/voice/update/:pk` | `plugin/ai/voice/update` |
| `POST /admin/plugin/ai/voice/delete` | `plugin/ai/voice/delete` |
| `POST /admin/plugin/ai/voice/preview` | `plugin/ai/voice/preview` |

节点名 = `BuildCheckPath(FullPath)`（`internal/infra/permission`）；末段为 `list|get` 的路由由中间件按
「同名节点 OR 同前缀 `/read`」判定，因此列表与详情共用 `read` 一个节点。本批新增的 5 个只读面
（supplier / apikey / task / voice 的 list、get）与「高级检索」白名单契约一致：非法字段 / 操作符
被拒（可读文案），不 500。

**页面菜单（`plugin.yaml` 的 `menus`，10 条 = 1 个目录 + 9 个功能页）**：

| 菜单 | type | name | component |
|---|---|---|---|
| AI 路由 | dir | `plugin/ai` | —（侧栏分组，不产生前端路由） |
| 总览 | menu | `plugin/ai/overview` | `/src/views/admin/plugin/ai/overview/index.vue` |
| 渠道管理 | menu | `plugin/ai/channel` | `/src/views/admin/plugin/ai/channel/index.vue` |
| 模型管理 | menu | `plugin/ai/model` | `/src/views/admin/plugin/ai/model/index.vue` |
| 用量统计 | menu | `plugin/ai/usage` | `/src/views/admin/plugin/ai/usage/index.vue` |
| 调用日志 | menu | `plugin/ai/usage-log` | `/src/views/admin/plugin/ai/usage/index.vue`（复用用量页 logs tab，无独立页面） |
| 厂商管理 | menu | `plugin/ai/supplier` | `/src/views/admin/plugin/ai/supplier/index.vue` |
| 应用接入（API Key） | menu | `plugin/ai/apikey` | `/src/views/admin/plugin/ai/apikey/index.vue` |
| 异步任务 | menu | `plugin/ai/task` | `/src/views/admin/plugin/ai/task/index.vue` |
| 语音合成 | menu | `plugin/ai/voice` | `/src/views/admin/plugin/ai/voice/index.vue` |

权限节点共 **36 个**（v1 的 18 个 + 本批新增 18 个：supplier 4 / apikey 6 / task 3 / voice 5）。

## 六、配置键全表（分组 `plugin.ai`）

落库名 = `plugin_ai_<键>`；宿主配置页不为 `plugin.<名>` 分组生成 tab ⇒ 读写只走插件自管的
`POST /admin/plugin/ai/settings/save`（宿主通用写口 `routine/config/update/:pk` 已加「分组必须已登记」
校验，插件分组不在其登记表内）。

| 键 | 落库名 | 类型 | 缺省 | 说明 |
|---|---|---|---|---|
| `api_key` | `plugin_ai_api_key` | password | 空 | 公开面 Bearer 凭证；留空 = 未配置（503 fail-closed）；非空时要求 ≥16 字符，出口只回显掩码 |
| `breaker_fail_threshold` | `plugin_ai_breaker_fail_threshold` | number | 5 | 渠道 `fail_threshold=0` 时使用 |
| `breaker_cooldown_seconds` | `plugin_ai_breaker_cooldown_seconds` | number | 60 | 渠道 `cooldown_seconds=0` 时使用 |
| `request_timeout` | `plugin_ai_request_timeout` | number | 60 | 渠道 `timeout=0` 时使用 |
| `stream_timeout` | `plugin_ai_stream_timeout` | number | 300 | 流式总时长上限（不受渠道 timeout 覆盖） |
| `currency` | `plugin_ai_currency` | string | USD | 金额出口的币种标签 |
| `allow_private_hosts` | `plugin_ai_allow_private_hosts` | switch | 0 | 1 = 放行 loopback / 内网出站（本机自建上游）；0 = 出站护栏拦截 |
| `sse_max_line_bytes` | `plugin_ai_sse_max_line_bytes` | number | 1048576 | SSE 单行上限（超限按上游协议异常终止，返回不可重试错误） |
| `sse_max_frame_bytes` | `plugin_ai_sse_max_frame_bytes` | number | 8388608 | SSE 单帧上限（同上） |
| `sse_max_accumulated_bytes` | `plugin_ai_sse_max_accumulated_bytes` | number | 33554432 | SSE 累积内容上限（超限置截断标记、停止累积） |
| `rate_limit_rpm` | `plugin_ai_rate_limit_rpm` | number | 0 | 公开面按 Key 的全局缺省 RPM；**0 = 不限**；DB Key 的 `rate_limit>0` 时用 Key 自身值覆盖 |
| `rate_limit_burst` | `plugin_ai_rate_limit_burst` | number | 0 | 桶容量，**只作用于全局层**；0 = 自动 `max(10, ceil(rpm/60))`（按 Key 覆盖层恒用该 Key 的 `rate_limit`） |
| `rate_limit_max_inflight` | `plugin_ai_rate_limit_max_inflight` | number | 0 | 网关**进程内**在飞请求数上限（**所有凭证共用**、无 Key 级覆盖）；0 = 不限；超限立即 429（不排队） |
| `channel_gate_rpm_default` | `plugin_ai_channel_gate_rpm_default` | number | 0 | 渠道 `rpm_limit=0` 时的缺省；**0 = 不限**（见差异节 (a)） |
| `channel_gate_tpm_default` | `plugin_ai_channel_gate_tpm_default` | number | 0 | 渠道 `tpm_limit=0` 时的缺省；**0 = 不限** |
| `task_poll_seconds` | `plugin_ai_task_poll_seconds` | number | 5 | 任务 worker 轮询间隔；**下界 clamp ≥1**（0 回落缺省，避免忙等） |
| `task_max_attempts` | `plugin_ai_task_max_attempts` | number | 3 | 异步任务最大尝试次数，超限置 failed |

## 七、与源实现的差异（逐条登记）

### (a) 渠道级准入（RPM / TPM，`gate.go` + `ratelimit.go`）

- **取值链**：渠道 `rpm_limit` / `tpm_limit` → 插件缺省 `channel_gate_rpm_default` /
  `channel_gate_tpm_default` → **0 = 不限**。源侧速率缺省是 **1000 rpm**（`channel_gate_rate_default`，
  `ChannelGate.php`），本插件**不发明限额**故缺省 0；要「与源对齐」就在设置里显式填 1000。
- **burst**：缺省 `max(10, ceil(rpm/60))`，与源同口径。
- **等待超预算 fail-open**：取不到令牌且等待超预算时**放行并记日志**（与源 `ChannelGate::awaitRate`
  的 fail-open 同口径：闸是配额保护，不是正确性依赖）。
- **TPM 是事后扣减**：本插件在**每次真实尝试结束后**按该次 `usage.total_tokens` 扣减进程内预算窗口；
  源侧是**派发前** `reserveTokensAsync` 预约、回执后 `settleTokens` 多退少补，且走 Redis Lua 原子
  （`BaseDriver.php` / `ChannelGate::tryTakeRateToken`）。⇒ 本插件在并发窗口内**可能超额**。
- **被拒候选不自增 attempt**：预算不足 ⇒ `Admit` 返回 false ⇒ 直接换下一候选（不污染渠道失败率、不记熔断）。

### (b) 公开面按 Key 限流（`ratelimit.go`）

- 本插件**自持令牌桶**（互斥 + 时间补桶 + TTL 清理），桶键 = `sha256(出示凭证)` 前 16 位 hex
  （与源 `'ai:rl:' + key_hash 前 16` 同构）。
- **全局缺省 0 = 不限**（`rate_limit_rpm`）；Key 级覆盖 `rate_limit > 0` 时优先，即使全局为 0 也生效
  （对齐源 `ApiKeyRateLimitMiddleware` 的「rate_limit > 0 优先生效」）。源侧全局缺省是 **120 次/分钟每 Key**。
- **按 Key 层的桶容量 = 该 Key 的 `rate_limit`**（首窗最多放行 `rate_limit` 次，此后按 `rate_limit/60`
  每秒补桶）：源对 DB Key 用固定窗口 `Limiter::check($bucket, $limit, 60)`，**没有**渠道级那条
  `max(10, ceil(rpm/60))` 下限 ⇒ 该下限在本插件也只作用于全局层（`rate_limit_burst` 键）。
  判据：全局 rpm=10 + Key `rate_limit=2` ⇒ **第 3 次** 429（`ratelimit_test.go` 的
 `TestRateLimitKeyOverrideThirdRequestIs429`）。早期实现把这条下限也套到按 Key 层，`rate_limit=2`
  的 Key 首窗可放行 10 次（5 倍限额），且要到第 11 次才 429。
- **在飞上限是本插件自有的闸，且是进程内单计数器**：所有凭证共用一份计数、**没有 Key 级覆盖**
  （`plugin_ai_api_key` 也没有该列）⇒ 一个 Key 的慢请求会挤掉其它 Key 的名额；源
  `ApiKeyRateLimitMiddleware` 只有固定窗口计数，没有在飞闸。多副本时各限各的。
- **进程内单实例**：源用官方 limiter 的 Redis 驱动做固定窗口、多进程共享精确限流；本插件是进程内桶
  ⇒ 水平扩容 = 限额 × 副本数，重启清零。跨进程强一致见能力三态表（需 Redis 客户端依赖）。
- **未过鉴权的请求不占配额**：**与源一致**——源中间件挂在 `AiGatewayAuthMiddleware` 之后
  （`ApiKeyRateLimitMiddleware` 头注明确「先鉴权出 aiApiKey 再计数」），本插件的限流层同样在鉴权之后。
- **源只对应用级 Key 限流**（平台级 `X-Internal-Key` 不限）；本插件只有两类 Key、不做该区分。
- **429 形状两边一致**：`type=rate_limit_error`、`code=rate_limit_exceeded`、`Retry-After: 60`
  （源 `tooManyRequests()` 同形状）。源在限流后端故障时 fail-open；本插件自持桶无外部后端，天然无此故障面。
- 限流路径**不写 `plugin_ai_usage`、不计渠道统计**。

### (c) 驱动与协议（`driver_registry.go` / `driver_responses.go`）

- 按渠道 `driver` 列分派驱动；**空值回落 openai**（与 v1 逐字一致），**未知 driver 返回 local 错误、
  不静默回落**。驱动注册表同时覆盖 `Models`，因此「渠道探活」也走分派。
- `responses`：POST `{base}/responses`，把语义化事件规范化为 chat 语义；**明确不支持 embedding**
  （能力门拒绝，报 local 错误）。
- `deepseek` / `dashscope` 别名：走 OpenAI 兼容端点 + 内置默认 `base_url` + **静态**模型名映射
  （如 `deepseek-chat → deepseek-v3`、`deepseek-reasoner → deepseek-r1`）。**差异**：源侧有
  `extra.model_map` 覆盖链（渠道 / 厂商 settings 层），本插件渠道 / 厂商表无 `settings` 列 ⇒ 不提供该覆盖面。
- `dashscope_tts`：本插件自注册驱动，只声明语音合成能力。**验收边界**：TTS 目前按协议层 + mock 上游
  验收，**真机可用性未验**（需要上游凭据），不要把它当作已实测能力对外表述。

### (d) 429 的隔离口径

见第四节末「429 的两种来源与隔离口径」——**断言本插件 429 前必须先关 / 调高宿主节流**，否则宿主形状的
429 会抢先。

### (e) 其他已登记差异

- **部分计费 `billed_partial`**：流式「有块、无收尾」（客户端断开 / 上游中途断流）在 v1 记 `cost=0`、
  不计 token；本批起改为按已聚合内容补算 tokens 与 cost、`status` 仍为 fail / timeout、**不触发熔断**
  （`billed_partial=1` 供审计）。无内容块时保持零计费。**该路径的 token 照常进渠道 TPM 扣减**
  （`RecordTotals` 拿的就是同一份 counts——上游确已消耗；DB 侧 `channel.total_tokens` 仍只累加成功行，
  与 §(a) 的「事后扣减」口径一致）。
- **任务 worker 的单实例语义**：插件 disabled 时 worker 每轮仍读台账但**不领活（空转）**；进程被杀时
  进行中任务靠租约过期复位重跑（`request_id` 幂等）；批量提交的幂等靠 DB 约束而非应用层锁。
- **租约丢失会停机**（`task_worker.go` 的 execute + `store_task.go` 的 updateOwnedTask）：续租报错或
  影响行数 0 ⇒ 取消执行上下文（上游调用被中断）且**一个字都不写**——终态归重领者；三个终态写口
  另带 `status='processing' AND attempt=?` 守卫，未命中即 `ErrTaskLeaseLost` 且不落库（防「终态取
  最后写入者」）。**残余窗口如实登记**：续租周期 = 租约/3（默认 100s），租约被外力作废（如带外 SQL
  改 `locked_until`）后的一个周期内本副本仍可能在跑 ⇒ 同一任务的双跑窗口收敛到续租周期，不是 0。
- **`task_poll_seconds` 有下界**（clamp ≥1），防止 0 值忙等。
- **TTS 计量**：本插件把输入**字符数**写进 `usage.prompt_tokens`，cost 用 `price_input` 按每百万字符
  计价（与 chat 同公式），`call_type='tts'`——这是本插件对源侧计价口径的映射，不是源口径的复刻。
- **`usage.source` 列宽 30**：`'key:' + source` 必须 ≤30 ⇒ Key 的 `source` 字段限制 ≤24 字符（写入侧
  另有截断兜底）。

## 八、安全口径

- 渠道 / 厂商的 `api_key` 永不序列化，列表与详情只回显掩码；编辑回填掩码（含 `*`）或空串**不回写**。
- 公开面 Bearer 用 SHA-256 + `subtle.ConstantTimeCompare`（与密钥长度无关）；DB Key 明文用 `crypto/rand`
  生成（≥32 字节），库里只存 `sha256`，明文只在创建 / 重置回执出现一次。
- **出站护栏（唯一工厂）**：默认拦截回环 / 内网 / 链路本地地址，且**不跟随重定向**；本机自建上游需显式
  `allow_private_hosts=1`。**新增驱动必须复用同一护栏工厂与同一脱敏出口**：
  - 护栏工厂 = `newGuardedHTTPClient(allowPrivate bool, timeoutSeconds int) *http.Client`；
  - 脱敏出口 = 驱动构造错误时用**该渠道的 api_key** 脱敏，使 `usage.error_message`、渠道 `last_error`、
    探活落库与探活回执全部经**同一出口**（错误文案、回执、日志都不出现明文密钥）。
- 流式解析有三级上限（单行 / 单帧 / 累积，见配置键表）：超限按上游协议异常终止并返回**不可重试**错误，
  **不静默截断为成功**；累积超限只置截断标记、保留已聚合内容。
- 公开面请求体 8MiB 上限（413 且不落原文）；embeddings 同步路径 `input` ≤64 条。
- 宿主通用配置写口 `routine/config/update/:pk` 有「分组必须已登记」校验，插件配置只能走
  `plugin/ai/settings/save`，避免持有宿主配置权限的角色盲写 `plugin_ai_*` 键。

## 九、自检与门禁

```bash
cd ai-go-admin
gofmt -l plugins/ai                                   # 应无输出
go build ./...                                        # rc=0
go vet ./plugins/ai/                                  # 应无输出
go test -count=1 ./plugins/ai/                        # ok（未设 DSN 时库级用例 SKIP，命令仍返回 0）
npm --prefix web run typecheck                        # rc=0（vue-tsc --noEmit；前端页与语言包）
go run ./cmd plugin verify plugins/ai                 # manifest / registry / frontend-sync 校验
```

库级回归用例**必须指向临时库**——用例会按 `<prefix>plugin_ai_%` 前缀清场并重建插件的表，且库内只要存在
`admin_plugins` 台账行就直接拒绝执行（防误指向）；**切勿指向开发 / 生产库**。口令从仓库
`config/config.yaml` 的 `database.write` 读，不写进任何文件（含本文件）：

```bash
PLUGIN_AI_TEST_DSN='postgres://<user>:<password>@<host>:<port>/<临时库>?sslmode=disable' \
  go -C ai-go-admin test -count=1 ./plugins/ai/
```

设了 DSN 的这一跑**不得有 SKIP**（库级用例全部真跑）；未设 DSN 时同一命令里库级用例显式 SKIP 且命令返回 0
（两套口径都对——「绿但什么都没跑」的陷阱因此只存在于未设 DSN 的那一侧）。多处并行测试共用同一临时库会互相踩
（清场 drop / 重建成表冲突），跑批时各会话各用一个库，或串行。
库级脚手架（`pluginAITestDB` / `taskTestDB` / `apiKeyTestDB`）每个用例新开一个连接池，收尾**必关池**
（`t.Cleanup` 里 `sqlDB.Close()`）——不关池时空闲连接随用例数累积，会把共享 PG 的
`max_connections`（本机 100）打满，表现为 `SQLSTATE 53300` 的**假红**（曾把「全 PASS」变成不可复现）。

文档与实现的对齐自检（README 是本插件的运维面事实源，改了清单就要复核这三条）：

```bash
# 配置键：两侧数量与集合都应一致
sed -n '/^configs:/,$p' plugins/ai/plugin.yaml | grep -c '^  - name:'
grep -cE '^\| `[a-z_]+` \| `plugin_ai_' plugins/ai/README.md

# 路由：adminRoutes + publicRoutes（plugin.go）逐条应能在第五节/第四节找到同名行
grep -c 'http.Method' plugins/ai/plugin.go

# 迁移编号 1..N 连续且 up/down 成对（POSIX 安全写法：BSD sed 的 BRE 不支持 \+
ls plugins/ai/migrations | sed -n 's/^\([[:digit:]][[:digit:]]*\)_.*/\1/p' | sort -n | uniq -c
```

## 十、装配点（本批接线落点，读码入口）

新增的能力不是自动生效的——它们挂在下面五个装配点上（改这些点之前先读对应文件头注）：

| 装配点 | 位置 | 内容 |
|---|---|---|
| 后台路由 | `plugin.go` 的 `adminRoutes` | 42 条（v1 的 20 + 厂商 5 / 应用接入 7 / 任务 4 / 语音 6）；`Node` 与 `plugin.yaml` 的 `menus[].name` 逐字相等 |
| 公开链 | `plugin.go` 的 `mountPublic` | `apiKeyAuth()` → `rateLimit()` → 业务处理器（唯一挂链点，勿在别处再挂一层） |
| 驱动 | `plugin.go` 的 `sharedDriver` | 返回驱动注册表（按渠道 `driver` 列分派）；缓存键 = 私网开关 + SSE 三级上限 + `RegistrySignature()`。**探活与转发共用同一实例**（`handler_channel.go` 的探活调用点显式传上限） |
| 准入门 | `plugin.go` 的 `defaultService` | `installChannelGate(service, sharedChannelGate())`：同时挂 `Router.gate`（候选级准入）与 `Circuit.charger`（TPM 预算回写）。门是**进程内单例**（重建 Service 不得清零已消耗预算） |
| 任务 worker | `plugin.go` 的 `mount` | `StartTaskWorkerOnce(ctx)`（`sync.Once`；宿主没有插件生命周期钩子，停用态靠每轮自查台账收敛） |
| 语音驱动 | `plugin.go` 的 `init` | `dashscope_tts` 自注册（能力 `speech`）；不注册则该驱动名走 `unknown_driver`，语音路由响亮失败 |
| 配置键 | `plugin.go` 的 `pluginConfigKeys` + `handler_overview.go` | 17 键单表；`settingsView` 回显**生效值**、`settingsSaveRequest.updates()` 的边界与运行面同一份常量；保存后就地失效三处缓存（插件配置 + 限流 + 渠道 Gate） |

结构性事实由测试钉住：`handler_test.go` 断言后台路由 42 条（每条链 = `AdminAuth` + `AdminPermission` + 业务）、
公开路由 4 条（真正注册进引擎）、`plugin.yaml` 声明 36 个节点且每个节点 `BuildCheckPath` 逐字相等、
节点与路由双向无孤儿。

## 十一、本批未覆盖 / 未验（如实登记）

以下条目**没有**在本批实测，不得当作已验能力对外表述：

1. **TTS 真机**：无可用上游凭据 ⇒ `dashscope_tts` 只按协议层 + mock 上游验收（见差异节 (c)）。
2. **`install` 未执行**（写仓动作须 owner 放行）：菜单 / 节点 / `configs` 行的**入库**、迁移 2..6 在真实库的
   应用、归属标记 `v0.1.0 → v0.2.0` 的刷新都还没跑过；现有证据是「清单静态门禁 + 迁移文件自身 + 库级用例」。
3. **语音公开面的真机 429 / 413 隔离与 404 白名单回执未走真 HTTP**：代码侧已补齐 `publicSpeech` 的
   `modelInKeyScope` 判定（四条公开链口径一致，见差异节 (b) 与 `handler_voice.go` 头注），但本轮只跑
   进程内用例，未在真实例上复跑 §七(d) 的宿主节流隔离脚本与真 HTTP 白名单 404 断言。
4. **真链路验收未跑**：按 Key 限流的真 HTTP 429（含宿主节流隔离与反向断言）、413 真客户端、
   真驱动 + 真库的 SSE 端到端（含 >1MiB 单行 / >32MiB 累积 / TPM 换道）、AdminPermission 放行支路、
   浏览器走查 —— 全部留待后续验证批次。
5. **跨进程强一致 RPM / TPM** 与 **外置队列**：见能力三态表的「仍未覆盖（明确不做）」两行。
6. **发布前扫描脚本不覆盖 DSN 形状**：工作区级的「私有信息扫描」模式面覆盖本机路径 / 记忆目录 / 凭据样例等
   形态，**不**覆盖 `postgres://<user>:<pass>@<host>` 这类连接串 ⇒ 本文的 DSN 示例一律写占位符，靠人工自律
   （扩模式面属工作区脚本改动，需单独放行）。
7. **常驻实例未加载 AI 插件时**：一切 HTTP / 端到端验证都需要「重编译 + 独立端口 + 独立 CWD」的新实例，
   不得在常驻进程上做验证。