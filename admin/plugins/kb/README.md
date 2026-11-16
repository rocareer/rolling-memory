<!-- plugin: kb v1.0.0 -->
# 知识库插件 `kb`

知识库（KB）的**插件形态**实现：库与授权、团队成员、标签体系、文档导入与切片、检索、审计与成本读数，
外加 1 个侧栏目录 + 5 个后台页面。本插件把既有内置知识库模块按插件契约移植而来（框架线参考实现），
此后**代码、迁移与前端源都在插件包内自持**——除安装器写入的注册行与前端落位副本外，不覆盖宿主任何既有文件。
插件目录约定、生命周期、归属与清理的通用纪律见 [`../README.md`](../README.md)，本文件只写本插件的口径。
注释里出现的章节号/条目代号（形如「节号+编号」与 T / C / R / F / D 系列编号）出自移植前的源设计件，未随插件包收录；本项目口径以本 README 与代码自述为准。

## 一、是什么 / 不是什么

**是**

- 插件名 `kb`（= 目录名 = 台账 `admin_plugins.name` = 路由段 `plugin/kb`），`kind: code`。
- **无 L2 实体**：18 张表全部由插件自管迁移建（见「三」），L2 引擎不建任何表、不注册任何实体路由。
- 后台面：1 目录 + 5 页面（库管理 / 团队 / 标签组 / 标签 / 标签规则）+ **41 个权限节点**，
  由 `plugin.yaml` 的 `menus:` 声明、安装器代写进 `admin_rules` 并打归属列 `plugin=kb`。
- 后端面：库与授权（owner ∪ grant 可见性）、团队、标签组/标签/标签规则/标签绑定、文档导入、
  切片与重建、检索（词面三腿：exact + tsv + trgm）、审计日志、成本日志、作业账本。
- 核心闭环**无 AI 密钥可全程走通**：建库 → 导入一段文本 → 切片落库 → 词面检索命中 → 逐字核对一致。

**不是**（v1 边界，逐条理由与解锁条件见「五」）

- **不带用户体系**：宿主没有用户表与用户模型。属主=用户、授权主体=用户这两条路径在插件内做了**端口化
  fail-closed**（端口未注入时响亮拒绝，不静默放行）；前端也不再出现用户类选项——库属主固定 `system`，
  团队按**组名**登记（未接入用户体系的部署仅登记，不校验）。
- **不支持 PDF 文本层**：`txt` / `md` / `html` / `csv` / `docx` 与纯文本导入可用；PDF 能被判型，
  但**提取阶段响亮失败**（不含 PDF 解析依赖）。扫描件/图片/音视频的云渠道同样一律 fail-closed。
- **没有文档 / 检索 / 切片 / 审计 / 标签绑定的页面**：这些能力有端点、无页面（源实现也没有这些页面），
  v1 走 API 验收。
- **本地兼容索引（盲化词槽腿）、语义候选、媒体云渠道未验证**：当前部署未配 AI 密钥与媒体渠道，相关代码路径按缺省关闭。

## 二、安装与启用

前置三件事：

1. 宿主机制迁移先在位（插件台账与元数据表）：`aigo migrate up`。表缺失时服务会拒绝启动。
2. 两条 PostgreSQL 扩展：`pg_trgm`（trusted，普通角色可建）与 `vector`（pgvector，**非 trusted**，
   需 superuser 或 DBA 预装）。缺 `vector` 时插件迁移会在隐式事务内失败 ⇒ **整个插件装不上**（台账落 `failed`）。
3. 插件源码在仓（`plugins/kb/`）：安装是源码树操作——落位文件、改接线生成物、需重编译。

在宿主仓根执行：

```bash
go run ./cmd migrate up        # 宿主迁移（表已在位可跳过）
go run ./cmd plugin install kb --dry-run                 # 只读预演：文件清单 / 菜单节点 / DDL / 宿主改写逐段打印
go run ./cmd plugin install kb --allow-unsigned          # 真装（本包未签名 ⇒ 必须显式接受未签名）
go run ./cmd plugin enable kb                            # 启用：运行时闸放行、菜单可见
```

扩展没装时先由 DBA 执行（安装账号非 superuser 时 `CREATE EXTENSION vector` 会失败）：

```bash
psql "$DSN" -c 'CREATE EXTENSION IF NOT EXISTS pg_trgm; CREATE EXTENSION IF NOT EXISTS vector;'
```

- 装完即 `installed`（**菜单隐藏**）；**只有 `enabled` 才生效**：路由挂在 `plugin/kb` 子组上，
  运行时闸按请求读台账，非 `enabled` 一律 **404**（不承认存在性；≤1s 生效，启停无需重启）。
- 兼容域：`host_api: ">=1 <2"`，对照宿主机制接口版本 `host.APIVersion = 1.1.0`；
  `framework_req: ">=0.1 <1"` 对宿主版本线**只记录 + 告警、不拒装**。
- 启停与卸载：

```bash
go run ./cmd plugin list                        # 只读：台账 / 进程内注册表 / 注册文件三方对照
go run ./cmd plugin disable kb                  # 停用：路由立即 404；作业 worker 下一轮不再领新活
go run ./cmd plugin uninstall kb                # 默认卸载：清菜单/节点/配置键/落位副本/注册行，保留表与数据
go run ./cmd plugin uninstall --keep-files kb   # 只清 DB 侧与注册行、留源码（源码树在仓、未安装的「出厂态」）
go run ./cmd plugin uninstall --purge --yes kb  # 连同插件迁移回滚 + drop 版本表（会删表与数据）
```

- **先禁用后卸载**是硬门禁（`--force` 只给 `failed` 半装现场用）。
- 环境门禁：生产环境（`plugin.env=prod`，或未显式配置且定位不到源码根）禁止安装与卸载；
  `--dry-run` 是只读预演，不受此限。
- 改动生效方式：后端 Go 代码需**重编译 + 重启**（接线真相在编译产物）；前端页随宿主前端一起构建发布，
  没有独立的插件前端产物。
- **Go 入口契约**：`plugin.go` 只做 `init()` → `host.Register(host.Meta{Name,Version,Kind,Mount})` → `mount()`
  （本插件即此形态）。宿主 `host.Meta` **没有**生命周期方法字段（只有 `Name`/`Version`/`Kind`/`Mount`/`MountPublic`）
  ⇒ 安装/启用/停用/卸载在实仓是 **CLI + 台账动作**，运行时由台账四态 + 运行时闸 + 作业面「每轮自查 status」承担；
  不要在插件里凭空实现这四个方法（编译不过）。

## 三、数据与迁移边界

- 迁移只在本插件目录内：`migrations/1..6`，编号插件内独立自增；**不得**放进宿主全局迁移目录。
- 版本表 = `<表前缀>plugin_kb_migrations`，与宿主 `schema_migrations` **不共享**；另有独立 advisory lock，
  多插件并发安装互不阻塞。
- **18 张表**（由 `1..6` 建，逐表点名核对，别只数个数）：

| 迁移 | 表 |
|---|---|
| `1_core` | `kb_library`、`kb_document`、`kb_chunk`、`kb_grant`、`kb_team`、`kb_audit_log` |
| `2_pipeline` | `kb_blob`、`kb_blob_ref`、`kb_extract`、`kb_chunk_vectors`、`kb_produce_state` |
| `3_search_log` | `kb_search_log` |
| `4_tags` | `kb_tag_group`、`kb_tag`、`kb_tag_link`、`kb_tag_rule` |
| `5_cost_log` | `kb_cost_log` |
| `6_jobs` | `plugin_kb_jobs`（作业账本，含单飞部分唯一索引 `uk_plugin_kb_jobs_active_key`） |

- 表名/索引名在迁移里写成带引号的 `"__PREFIX__…"` 占位符，执行期替换为配置的表前缀；
  **代码与文档都不硬编码前缀**。
- 迁移禁词纪律：整文件单个隐式事务 ⇒ **禁 `CREATE INDEX CONCURRENTLY`**；无 `BEGIN`/`COMMIT`；
  **不写 `admin_rules`**（菜单/节点由清单声明、安装器代写，插件自带 SQL 写它会让归属漂移、卸载清不干净）。
- `1_core` / `2_pipeline` 会执行 `CREATE EXTENSION IF NOT EXISTS`（`pg_trgm` / `vector`）；扩展**不回收**。
- `kind: code` ⇒ 引擎不建任何表；**卸载默认保留表与数据、版本表**（复装幂等），唯一删表路径是 `--purge`。

## 四、配置键与覆盖口径

- **缺省源**：包内 `config/kb.yaml`（随安装落地到插件源树，运维可读）。
- **覆盖源**：`configs` 表的 `plugin.kb` 分组（落库名 `plugin_kb_<键>`），由安装器按 `plugin.yaml` 的
  `configs:` 段写行。
- 口径：**行存在即以其值为准（含空串），行缺失才回落内嵌缺省**；进程内缓存（秒级 TTL），改值后无需重启。

**可在配置页改的 14 个键**（白名单；其余键改不了）：

| 键（落库名 `plugin_kb_` 前缀） | 缺省值 | 说明 |
|---|---|---|
| `embed_enabled` | `0` | 嵌入档开关。**缺省关**（无密钥环境下开它会让每次导入在嵌入阶段响亮失败） |
| `embed_provider` | `openai-compatible` | 嵌入端口实现名（未注册即 fail-closed） |
| `embed_model` | 空 | 空 = 向量身份不可用 ⇒ 嵌入面 fail-closed |
| `embed_dim` | `1024` | 须与向量列维度一致 |
| `chunk_target` | `512` | 全局切片目标块长（rune） |
| `chunk_overlap` | `64` | 超帽切分重叠 |
| `chunk_hard_cap` | `1024` | 硬帽 |
| `fts_config` | `simple` | 只接受空 / `simple`，其余响亮拒绝 |
| `search_scan_cap` | `5000` | 检索腿窗帽（同时约束榜上限） |
| `prune_retention_days` | `180` | 检索日志保留期 |
| `log_query_text_max` | `200` | 查询文本落库上限（0/负 = 关，封顶 512） |
| `media_ffmpeg_bin` | `ffmpeg` | 抽帧/音轨工具（PATH 查找） |
| `limits_max_pages` | `500` | 单文档页数上限（**始终武装**） |
| `limits_max_minutes` | `120` | 单文档时长上限（**始终武装**） |

**只读内嵌的键**（改不了，理由随段给出）：

| 内嵌键段 | 键 | 只读理由 |
|---|---|---|
| `kb.chunk` | `heading_prefix`、`levels`、`window`、`threshold`、`min`、`max`、`rows_per_chunk`、`header_repeat`、`min_rows` | 切片档：改动会改 `build_hash`（触发全量重切）或属调优面，须随代码与读数同批改 |
| `kb.embed` | `batch`、`max_batch`、`max_chars` | 批与截断属调优面，读写性能口径未做读数前不动 |
| `kb.budget` | `max_docs_per_job`、`max_chunks_per_job`、`max_chars_per_job`、`max_est_tokens_per_job`、四个单价/系数键 | 成本闸：出厂四上限均 `0` = **闸不生效**（仅留估算读数），武装需按库规模显式决策 |
| `kb.media` | `ocr.*`、`asr.*`、`caption.*`、`frame_seconds`、`timeout_*` | 渠道开关只是一半：驱动登记表为空，开了也是 fail-closed；预算系数属调优面 |
| `kb.tag` | `model_enabled`、`agent`、`provider`、`model`、`material_chars`、`max_candidates`、`vocabulary_cap` | 模型候选档：需模型与密钥，缺省 `model_enabled=false` |
| `kb.search` | `rrf_k`、`trgm_threshold`、`query_cache_file`、`query_cache_persist` | 融合与查询缓存口径；缺省 `query_cache_persist=false`（无本地兼容索引腿时缓存无用，且避免落盘路径不确定） |

- 这张白名单由 `kbcfg` 的用例断言：可覆盖键闭集 = 本表 14 项，且与清单 `configs:` 段逐项一致（键集合一旦漂移用例即红）；不要凭本表之外的键去改行为。
- **配置页入口默认不可达（已知限制）**：配置页的分组页签来自宿主 `configs` 表里 `name='config_group'`
  那一行的 JSON 数组，而安装器只写 `plugin.kb` 分组行、**不追加分组数组** ⇒ 这 14 行「有数据、无入口」
  （插件照常读它们，覆盖照样生效）。要拿到页签，由运维手工执行一次（该行是**宿主自管行**，不属插件安装面；
  卸载时按需自行回退）：

```sql
UPDATE configs
SET value = (value::jsonb || '[{"key":"plugin.kb","value":"知识库"}]'::jsonb)::text
WHERE name = 'config_group'
  AND value IS NOT NULL
  AND NOT (value::jsonb @> '[{"key":"plugin.kb"}]'::jsonb);
```

  （表名按实际表前缀替换。）

- **自述偏离**：插件内默认嵌入实现（openai-compatible 直连）为兼容既有端口实现，读宿主 `configs` 的
  `ai` 组键 `ai_api_url` / `ai_api_key`；按本插件缺省档（`embed_enabled=0`、标签候选关、媒体渠道关），
  该路径**完全不执行**。
  - **另读的宿主键（未登记于上表）**：默认实现另读 `configs` 的 `ai` 组键 `ai_assistant_allow_private_url`
    ——控制回环/私网上游是否放行；**缺省 true（未配置 = 允许私网）**，与本插件缺省档的 fail-closed 口径不同；
 要收紧由运维在 `ai` 组显式置 `0`。该键在本项目**无种子行**（宿主种子迁移
    `cmd/migrate/migrations/000003_seed.up.sql` 的 `ai` 组只有 `ai_api_url` / `ai_api_key` /
    `ai_model_list` / `ai_default_model`），开发库 `configs` 亦无该行（那里只有 `plugin.ai` /
    `plugin_ai_allow_private_hosts`，属 `ai` 插件自己的键）。

## 五、v1 范围与未实现项

| 项 | 状态 | 理由与解锁条件 |
|---|---|---|
| PDF 文本层 | **不支持**（判型后会提取失败） | 解析依赖不在宿主、且插包不动宿主依赖清单；解锁 = 为依赖清单加一行依赖并补回提取器与用例 |
| PDF 光栅化（扫描件链） | 代码保留、**v1 不可达、未验证** | PDF 判型阶段即响亮失败 ⇒ 该函数走不到；**不得**读成「已支持扫描件」 |
| 文档 / 检索 / 切片 / 审计 / 标签绑定**页面** | 未提供（端点有） | 源实现本身没有这些页面，新建页不属移植范围；第二批可做页面或只读观测页 |
| 英文语言包的 标签 / 标签组 / 标签规则 文案 | 占位级（结构完整、键集成对） | 源实现的英文侧原文即字段名占位符，照搬等于把占位符入库；第二批翻译 |
| 本地兼容索引（盲化词槽腿） / 语义候选 / 媒体云渠道的**运行验证** | 未验证 | 当前部署未配 AI 密钥与媒体渠道；配齐密钥 + 渠道 + 模型名后再验（不得以「代码存在」当作通过） |
| `kb:tag` 模型候选、`semantic` 切片策略 | 未验证 | 同上（都要模型） |
| 配置页里看不到 `plugin.kb` 分组 | 已知限制 | 机制侧「安装器代写并清理分组数组」属宿主机制变更，须另立需求；当前给运维一次性 SQL（见「四」） |
| 页面 keep-alive | 行为变化 | 清单无 `keepalive` 键，安装器统一写 0 ⇒ 页面不再被缓存，靠重进重拉 |

## 六、运维口径

- **作业面自管**：插件自带最小作业面（账本表 `plugin_kb_jobs`），作业 kind 闭集：
  `kb:ingest`、`kb:rebuild`、`kb:purge`、`kb:tag`、`kb:prune`。worker 随进程启动（只起一次），每轮先自查台账
  `status == 'enabled'`——**停用即停跑**（不再领新活，在飞的作业按租约继续或被重领）。作业到期靠租约重领、
  按 `attempt`/`max_attempts` 退避重试；重跑幂等（内容 sha256、提取键、切片键的唯一约束保证）。
- **运维三端点**（均 POST，各自挂号内声明的权限节点）：`maintenance/check`（团队巡检 + 匹配文本待处理探针 +
  向量身份点名）、`maintenance/reindex`（回填匹配文本，支持 `dry_run`）、`maintenance/reembed`
  （按当前嵌入身份重建向量；**无嵌入身份时响亮失败**）。原先的命令行巡检能力已迁为这三个端点。
- **前置依赖**：`pg_trgm` + `vector`（见「二」）；媒体链另需 `ffmpeg`（键 `media_ffmpeg_bin`）。
- **无密钥降级口径（验收基线）**：缺省 `embed_enabled=0` ⇒ 检索 auto 档**直接走词面腿**
  （exact + tsv；含 CJK 时含 trgm），且**不产出 `degraded`**。这不是缺陷，校验时也别为了让它出现而打开嵌入档
  （那需要密钥，且改掉了缺省档）；真正的缺陷判据是词面腿缺腿或逐字核对不一致。
- **大作业闸**：`limits.max_pages` / `max_minutes` **始终武装**（缺省 500 页 / 120 分钟）；`kb.budget` 四上限
  出厂为 `0` = 闸不生效（只留估算读数）。超限文档落 `failed` 并挂起待批，不静默截断。
- **数据保留**：检索日志按 `prune_retention_days` 清理（`kb:prune` 每日首轮入队）；作业账本终态行随同一作业
  顺带清理，避免无界增长。
- **诊断入口**：`plugin list` 看三方一致性；端点 `maintenance/check` 看扩展与向量身份读数；库级探针与验收口径见「九」「十」。

## 七、生命周期与残留面

- 四态：`failed`（半装，只允许 `uninstall --force`）→ `installed`（菜单隐藏）→ `enabled`（唯一生效态）
  → `disabled`。管理页详情抽屉提供 停用/启用 与 卸载 入口（与 CLI 同一写路径、同一审计）。
- 三处真相：进程内注册表（编译期）、台账 `admin_plugins.status`、注册文件。三者不一致时由 `plugin list`
  与管理页显式报告（例如台账 `enabled` 而进程没有该插件 ⇒ 标「待重编译」、路由 404），**不做静默对齐**。
- **卸载清理面**（与安装写入面逐项对齐）：`admin_rules` 里 `plugin=kb` 的整棵菜单/节点子树 + 各角色组 `rules`
  内的悬挂 ID + `configs` 的 `plugin.kb` 分组 + 前端落位副本 + 注册行。**保留**：表与数据、插件迁移版本表、
  本地包留档。
- **前端源与副本**：`plugins/kb/web/**` 是唯一事实源，`web/src/views/admin/plugin/kb/**` 与
  `web/src/lang/{zh-cn,en}/plugin/kb.yaml` 是落位副本（产物）。**改页面改源**，用
  `go run ./cmd plugin install --force kb` 刷新副本；两侧逐文件 sha256 由门禁比对。
- 菜单 `component` 必须精确落在 `/src/views/admin/plugin/kb/<页>/index.vue`（宿主前端按路径精确查表，
  落错位置**静默查不到**）。
- 语言包第一层命名空间由落位路径生成为 `plugin.kb.*`；**语言包/清单 YAML 语法非法会让整站白屏**，
  合并或改键后必须跑 YAML 门禁（见「八」）。

## 八、接管路径（若某部署已建过 `kb_*` 表）

- 本清单**不声明 `adopt`**：宿主从未建过任何 `kb_*` 表，不存在「插件迁移与宿主核心链同物」的对象。
  默认路径是**全量重放**，而各迁移本身是 `IF NOT EXISTS` 形态，重放无害。
- 若某部署确已用别的途径建过同名表，且确认与 `migrations/1..6` 是**同物**：补 `adopt` 段
  （`through: 6` + 逐版本探针）后再装。安装期按三态判定：探针**全命中** ⇒ 只把版本表写到边界（迁移 SQL 0 条）；
  **全不命中** ⇒ 全量重放；**部分命中 ⇒ 拒装**（fail-closed，先于任何落盘/写库）——此时先补齐宿主核心链再重装，
  或人工确认后 `go run ./cmd plugin migrate-force kb <版本>` 归位版本表。
- `--purge` 与 adopt：已接管装入的插件 `--purge` 默认拒绝（down 文件可能删的是别人建的表与数据），
  放行需 `--purge --yes --purge-adopted`，且落位清单必须可读。

## 九、自检与门禁

在宿主仓根执行：

```bash
go build ./... && go vet ./... && go test ./...              # 编译 + 静态 + 全部用例（无库用例不得跳绿）
go run ./cmd plugin verify plugins/kb                        # 清单 / 注册行 / 前端源-副本校验
pnpm -C web run typecheck && pnpm -C web run build           # 前端类型检查与构建
```

- 清单结构由 `check-plugin-manifest.sh` 校验（闭集键集 / 命名 / 长度预算）；**语言包与插件配置的 YAML 语法**由验收脚本 `static` 阶段的逐文件 YAML 解析兜底（见「十」）——非法 YAML 会让整站白屏，改过 `plugin.yaml` 或语言包后必须重跑。
- 注册行 ↔ git 索引互查以索引为准：跑门禁前 `git add plugins/kb`（只暂存；本文件不涉及提交动作）。
- `check-plugins.sh` 是**整仓**插件面检查（遍历 `plugins/` 下所有插件）：其它插件的违规同样会让它变红——
  红了先按违规项里的插件名判断是否属于本插件，别把别人的落位副本缺失读成本插件缺陷。
- **库级用例**另有一套门控（不带它一律跳过，命令照样退出 0 —— 别把「跳绿」当通过）：

```bash
createdb -h <主机> -p <端口> -U <用户> aigo_kb_test
DATABASE_WRITE_DBNAME=aigo_kb_test PLUGIN_KB_DB=1 \
  go test -count=1 -timeout=300s -p 1 ./plugins/kb/...
```

  用例启动时会断言当前库名匹配 `*kb_test*`，否则**响亮拒绝**（不是 skip）——防止误连开发库。
  **不要**把这类用例指向开发库/生产库（会动到已装插件与实体数据），也不要改用它处的插件套件门控变量。
- 库级用例（`PLUGIN_KB_DB=1` 门控的那批，按文件与用例名）覆盖面：**端到端冒烟（建库 → 导入一段文本 → 切片 → 词面三腿检索 → 逐字核对）**、
  文档导入、切片/管道状态机（含重建与水位冻结）、检索可见域与检索日志清理、迁移契约（18 张表**表名逐条冻结**；
  索引按**总数 44** 断言 + 作业账本 3 条索引**逐名**断言——框架线那批索引名未逐条冻结，改名或互换不会报红）、
  作业面六条不变量（同键单飞、租约重领、attempt 上限、停用不领活、进度单调、未注册 kind 不领）；
  重建/取消/失败清单/删除等段落另按下面的「核心闭环探针」走查。
- 装后只读探针（在隔离库上做）：台账 `status = enabled`；`admin_rules` 里 `plugin='kb'` 的行数 =
  6（1 目录 + 5 菜单）+ 41 节点；版本表 `plugin_kb_migrations` 为**单行** `version=6` / `dirty=false`（golang-migrate 形态，不是「6 行」）；「三」的 18 张表逐表点名存在；
  前端副本与源逐文件 sha256 一致；停用后同一 URL 返回 404、启用后恢复。
- 核心闭环探针（无 AI 密钥即可）：建库 → `document/import`（文本）→ 轮询 `document/list` 至 `build_state=ready`
  → `search`（auto 档，期望词面腿命中、`degraded` 为空）→ `chunk/get` 逐字核对 → `document/rebuild` → 取消
  → 失败清单 → 删除 → `maintenance/check` 读数。

## 十、验收：一条命令跑完「装得上、起得来、启停闸认账、能复原」

上面「九」是把每一步摊开的手工口径；可按如下阶段逐项执行非破坏性验收（使用一次性隔离库，正常路径不污染开发库）：

```bash
go test -count=1 ./plugins/kb/...                      # 静态与单测验证
go run ./cmd plugin install plugins/kb --dry-run       # 安装预演
```

阶段与判据（每步都打印 `[PASS]`/`[FAIL]`/`[SKIP]`/`[WARN]` 与现场读数）：

| 阶段 | 判据 |
|---|---|
| `preflight` | 工具齐备；临时库名含 `kb_test`；不选 8091 与配置里的 `server.port`；注入 `PLUGIN_ENV=dev` |
| `db` | 先取**开发库只读指纹**（收尾再取一次比对，证「没落到开发库上」）；`createdb` 一次性库 + `create extension if not exists pg_trgm, vector`（幂等；缺扩展时直接判「装不上不是插件缺陷」并给出 DBA 步骤） |
| `host-migrate` | **迁移前门禁（真探针，非纯只读）**：对**目标库**跑 `migrate version`，读数必须是「当前版本: 0」——证明目标确是刚建的新库，才允许执行第一条写库命令（该探针在构造期会往目标库建「若缺」的空版本表并短暂持 advisory lock，落点 = 一次性临时库）；随后宿主迁移推进到最新，并硬断言①临时库 `schema_migrations` 已就位（≥1 行）、②开发库只读指纹的 schema 版本段未变；`admin_plugins`/`admin_rules`/`admin_groups`/`configs`/`admins` 就位 |
| `static` | `check-plugin-{manifest,frontend-sync,registry}.sh`（违规行**按插件归属**：别的插件的未提交违规只记 WARN 并照实打印）、插件与语言包 YAML 语法、`go build ./plugins/kb/...`、`go build ./...`、`go vet ./...`、`gofmt -l plugins/kb`（`./cmd` 的编译在 `gate` 段——装完注册行才是完整接线） |
| `dryrun` | `plugin install kb --dry-run` 零副作用通过；「② 文件清单」段在；「③ 插件迁移」段**无禁词命中** |
| `install` | 安装完成且源码树仍在；落位副本与注册行已生成 |
| `enable` | `admin_plugins.status = enabled`，归属菜单/节点 `status = 1` |
| `ledger` | 归属行 **47**（1 目录 + 5 菜单 + 41 节点）；版本表**单行** `version=6` / `dirty=false`；**18 张表逐表点名**；`configs` 的 `plugin.kb` 分组 **14** 行；`register_gen.go` 有注册行；`admin_plugin_actions` 有 install/enable 留痕；前端源↔落位副本逐文件 sha256 |
| `gate` | 另起端口：enabled → **鉴权拦截**（本项目口径：HTTP 200 + 响应体 `code:401`；若哪天改成真 401 也接受）→ `disable` → 同一 URL **404**（不承认存在性，真 HTTP 404）+ 菜单 `status` 全 0 → `enable` → 恢复拦截。**端口全被占 ⇒ FAIL**（覆盖丢失不降级；确要跳过得显式 `KB_ACCEPT_SKIP_GATE=1`） |
| `embed-skip` | 嵌入/语义/媒体面按现场读数（`ai_api_key`、`embed_enabled`、`embed_model`）**显式跳过**并打印原因，并登记进总结的「未验证面」清单 |
| `uninstall` | 先 `disable`（硬门禁）再 `uninstall kb --purge --yes --keep-files`：18 张表与版本表回滚、台账/归属行/configs 分组清零、卸载审计留痕、注册行撤除、`register_gen.go` 与运行前一致。三项复核**口径逐项不同**：① 源码树 sha256——内容漂移 ⇒ WARN、目录被删 ⇒ FAIL 并复原；② 前端落位副本 sha256——漂移**或目录被删**都只记 WARN；③ 本插件注册行——运行前没有而卸载后残留 ⇒ FAIL（收尾时只摘这一行），其余差异按「滤掉本插件行后 cmp」（不是 sha256）判，属非本插件写入 ⇒ WARN |
| `core` | `PLUGIN_KB_DB=1 go test -v ./plugins/kb/...`（端到端冒烟 + 导入、切片管道、检索、迁移契约、作业面不变量）。判据带**收集下界**：退出 0 但收到 0 条 ⇒ FAIL（零证据不算过）；未分类 SKIP ⇒ FAIL（子进程专用入口 `*HelperProcess*` 不计覆盖丢失）；收尾总结登记未验证面 |
| `cleanup` | 停进程、删临时库、删临时目录；开发库指纹再比一次；**计数行与「结论」都在这里、全部计数发生之后同一快照打印**（日志默认保留，路径在结尾打印） |

> `core` **故意排在 `uninstall` 之后**：插件的库级用例夹具会直接动 `admin_plugins` 台账（作业面用例
> 会为自己的测试插/删同名台账行），夹在「安装」与「卸载」之间会把安装行的状态改掉，使卸载复原那一段
> 取不到读数。先用 CLI 把「装 → 启 → 停 → 卸」验完，再让用例在自己的夹具世界里跑，两边互不踩。

非破坏保证（脚本自身口径，逐条可核对）：

- **只连本脚本新建的库**：库名含 `kb_test`，落库前断言「临时库名 ≠ 配置里的开发库名」，收尾
  `drop database ... with (force)`；临时库已存在时**拒绝复用**（不误删他人数据）。
- **不写 `.env.yaml` 与 `config/*.yaml`**（但会**只读**解析其 `server.port` 与开发库名——宿主 `go run ./cmd` 自身
  也读 `.env.yaml`，这里只是沿用宿主同一配置层序）：库名与端口只用环境变量覆盖（`DATABASE_WRITE_DBNAME`、
  `SERVER_PORT`），口令只在进程环境里传递（`PGPASSWORD`），不进 argv、不进日志（收尾统一脱敏一遍）。
- **不动 8091**：探针端口从 8099 起自选，且避开配置里的 `server.port`。
- **不改插件源码**：卸载固定 `--keep-files`（只清 DB 侧与注册行，保留 `plugins/kb/**` 与落位副本）。
  卸载后三项复核**口径逐项不同**：① 源码树逐文件 sha256——内容漂移 ⇒ WARN、目录**被删** ⇒ FAIL 并从安装前备份复原；
  ② 前端落位副本逐文件 sha256——内容漂移**或目录被删**都只记 WARN（落位副本可由重装再生，非数据源）；③ 本插件注册行——
  运行前没有而卸载后残留 ⇒ FAIL（当场只摘这一行），其余差异按「滤掉本插件行后 cmp」判（不是 sha256）。
  内容漂移的归因：`--keep-files` 的卸载根本不写源码树（它跳过整个「删落位物」步），所以差异只可能
  来自外部写入者——脚本照实打印 diff 并提示
  「在没有别的写入者的时段重跑本阶段取干净读数」，不冒充本插件的结论。
- **不整文件回写共享文件**：`internal/plugins/register_gen.go` 由多个插件的批次共同改写，脚本只做
  **摘掉本插件那一行**这种定点操作，收尾比对时也先把本插件的行滤掉再比，绝不用备份整文件覆盖
  （否则会把别人正在写的注册行一起抹掉）。
- **成功与失败都自清理**：后台探针进程按 PID 收口（先 TERM 后 KILL），临时库与临时目录照删；
  **日志两种路径都保留**（成功也留，路径在开头与结尾各打印一次；`KB_ACCEPT_KEEP_LOG=0` 可回到
  「成功即删」旧行为）——头条读数因此可复核。

开关与边界：

| 环境变量 | 作用 |
|---|---|
| `KB_ACCEPT_KEEP=1` | 保留临时库与临时目录（排障用） |
| `KB_ACCEPT_KEEP_LOG=0` | 成功跑完即删日志（默认保持：日志保留、路径在结尾打印，头条读数可复核；失败/中止一律保留） |
| `KB_ACCEPT_LOG=<路径>` | 指定日志落点（默认 `$TMPDIR/kb-plugin-accept-<时间戳>.log`） |
| `KB_ACCEPT_FORCE=1` | 安装追加 `--force`（仅在确认要覆盖手工改动时；默认遇到「同名不同内容」就停） |
| `KB_ACCEPT_WEB=1` | 追加前端 `typecheck` + `build`（耗时长，默认跳过） |
| `KB_ACCEPT_SKIP_GATE=1` | 无可用探针端口时**显式接受**跳过运行时闸探针（默认判 FAIL：覆盖丢失不降级） |
| `KB_ACCEPT_ALLOW_DBSUITE_SKIP=1` | 显式接受库级用例里的未分类 SKIP（默认 SKIP 即 FAIL） |
| `KB_ACCEPT_ALLOWED_SKIPS="<名> <通配*>"` | 额外的「可接受 SKIP」用例名（`*HelperProcess*` 恒可接受，属子进程专用入口） |
| `KB_ACCEPT_MIN_CASES=N` | 库级用例收集下界（默认 1；收到 0 条即「零证据」判 FAIL） |
| `KB_ACCEPT_ALLOW_REGISTERED=1` | 插件运行前已有注册行时仍要跑（卸载会撤掉该行，收尾不代改，自行复原） |

**覆盖丢失一律不降级**：库级用例收到 0 条、出现未分类 SKIP、无可用探针端口——这三条都不会再「退出 0 且
报通过」。**收尾总结会打印「未验证面」清单**（如嵌入/语义/媒体云渠道、PDF 文本层、`reembed`、`degraded`
非空分支、HTTP 逐端点；以及任何被显式接受的跳过），并写明「结论：通过（限**有读数的面**）」——别把
「通过」读成「全绿」。**每个 `[SKIP]` 都由脚本自动登记进这份清单**（去重、逐条成行）——包括默认不跑的
`KB_ACCEPT_WEB=1` 前端 typecheck/build 面：默认跑出「结论：通过」时它也在未验证面里，不会被藏掉。

**脚本故意不做的一件事**：HTTP 侧「登录取令牌 → 逐端点闭环」不自动化。它要后台登录口令，而口令进环境
变量就会经过命令行与进程环境（还容易被日志捞到）——一个要入库的脚本不该鼓励这种用法。这个环节脚本只
做**免凭据的两层**（在跑着的探针服务上打同一 URL：鉴权拦截 → disabled 404 → 恢复拦截），链路级证据由
`core` 阶段的库级用例承担；**也就是说链路证据是被测插件自证**，脚本独立证的是「路由在、闸认账、启停生效」。
逐端点那一段按上面 `core` 阶段打印的步骤手工走一遍并留档。

**无 AI 密钥的验收边界**（与「五」同一口径）：`embed-skip` 阶段把 **本地兼容索引（盲化词槽腿）、语义候选、媒体云渠道
（OCR/ASR/字幕抽帧）、`reembed`、检索 `degraded` 非空分支、PDF 文本层**逐条标为「未验证」并打印现场
读数——**未验证不等于通过**；也不得为让它出结果而打开嵌入档（缺省 `embed_enabled=0` 是设计口径）。
本轮真正的通过路径是关键词腿（exact + tsv + trgm）。

环境串扰的排法（实测过）：共享 `GOCACHE` 与另一模块的并发 `-trimpath` 构建会让 Go 命令报
`could not import ... (open <GOCACHE>/<构建哈希>/...: no such file or directory)`——脚本对 `go build ./...`、
`go vet ./...` **以及插件级 `go build`、`go run`（各子命令）、`migrate version`/`migrate up`、库级 `go test`、
`gate` 段的安装后重编译（`go build -o <工作目录>/aigo ./cmd`）** 的该签名都自动重试一次，重试用
**隔离 `GOCACHE`**（`mktemp -d`）：隔离重跑通过即记 `WARN` 并归因「共享缓存串扰，不是插件缺陷」，
两次都失败才按真实失败处置并打印隔离跑法。手工隔离跑法：
`GOCACHE="$(mktemp -d)" go test -count=1 ./plugins/kb/...`。

版本表读数口径（与上面「九」同口径）：`plugin_kb_migrations` 是 golang-migrate 的版本表，
形态是**单行**、两列 `version` 与 `dirty`（同仓已装插件的同名版本表实测只有 1 行 `1 / f`）。正确判据是
「单行且 `version=6`、`dirty=false`」，不是「6 行」；脚本按单行形态断言。

退出码：`0` = **有读数的面**全通过（未验证面已在总结里列明）；`1` = 有 `FAIL`；`2` = 前置不满足（未开始或已中止）。
计数行（`PASS/SKIP/WARN/FAIL`）与「结论」都在收尾块、**全部计数发生之后**同一快照打印（旧版把计数行放在
主流程尾部，比实际少 1）；「结论：通过」只在最终码为 0 时出现——头条读数、结论与退出码三者同源。
