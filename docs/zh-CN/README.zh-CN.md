# Rolling-Memory

**[简体中文](README.zh-CN.md) · [English](../../README.md) · [官方网站](https://rolling-memory.rocareer.com/) · [AML 官方榜单](https://agentmemories.ai/leaderboard/) · [文档中心](README.md) · [省 Token 算账与边界](token-economics.md) · [常见问答](faq.md) · [评测与复现](benchmarks.md) · [贡献者](../../CONTRIBUTORS.md) · [立场与免责声明](DECLARATION.md)**

**Rolling Memory**（中文品牌「洛灵」）是一个源码公开、本地优先的记忆工具，面向 AI 编程助手与 Agent——Claude Code、Codex、OpenCode、Cursor、Windsurf、Cline、ZCode 等——由 **Rolling Autonoetic Memory Engine（洛灵自知记忆引擎）** 驱动。它在本地索引受支持的对话来源与挂载文件，经唯一 MCP 工具 `recall`（回忆）提供给接入的编程助手；对话采集范围和手工导入方式见[接入指南](harness.md#45-对话采集范围与手工导入)。**无需云端、无向量、无后台大模型调用**；非商业用途免费。

**独立第三方评测成绩**：在 **Agent Memory Leaderboard（AML，记忆之巅）第二期**的代码赛道闭源方法榜中，Rolling Memory 的「Noisy 新功能」代码子能力以 **80.39% 解决率位列第 1**，为该子项全榜最高；提交所用引擎与本仓库发布的本地引擎同源，未针对赛道做任何特调。榜单于 2026-11-19 发布，详见 [AML 官方榜单](https://agentmemories.ai/leaderboard/)。

![Agent Memory Leaderboard 第二期 — Rolling Memory 在闭源方法榜代码赛道 Noisy 新功能子能力以 80.39% 解决率位列第 1](../assets/aml-cycle2-code-noisy-new-feature.webp)

*AML 第二期 · 闭源方法榜 · 代码赛道 · 代码子能力「Noisy 新功能」：**第 1 名 —— 解决率 80.39%**。[官方榜单 →](https://agentmemories.ai/leaderboard/)*

> **开始使用**：运行 `curl -fsSL https://github.com/rocareer/rolling-memory/raw/main/install.sh | bash`（或在本地仓库运行 `bash install.sh`）连接你的编程助手，然后正常干活就行。需要追溯历史时问一句“上次那个 Bug 是在哪修的？”即可。详见[快速开始指南](harness.md)。

它带来的效果很直接：你的 Agent 问一次，就能拿回**原始记录**本身——当时的决定、判断理由、精确到文件的坐标——而不用一轮轮盲搜试错。摘要留下的只是梗概，`recall` 还给你的，是事情当时真实发生的样子。

我们做这个项目的初衷很简单：**希望做到 AI 记忆的普惠与平权**——让长期记忆像本地文件一样，成为每个普通开发者、学生和小团队都能零门槛用得起的基础工具，而不是必须绑卡、买云托管、或者在后台源源不断烧大模型 Token 的昂贵服务。

所以，这套记忆系统在日常使用中有几个最直观的特点：**真免费、实时不用等、安全无第三方经手，还可以放进 U 盘随身带走**——
- **真免费（零隐性账单）**：不用付后台 LLM 抽取费（0 后台 Token 账单）、不用付在线向量化（Embedding）API 费、不用付各种云数据库托管费与账号费，不用注册也不用绑卡；
- **本地检索**：无需后台大模型抽摘要或向量化服务；已索引的对话和文档可通过 `recall` 按需查询；
- **安全的（没有第三方经手你的代码和信息）**：编译出来就一个单文件 Go 程序，所有数据都存在你自己电脑的一个 SQLite 文件里，记忆检索全程不连外网——**除了你自己和正在给你干活的那个 Agent，没有任何第三方记忆平台或外部服务商能接触到你的代码、路径和对话信息。** 一个精确的边界：如果你的 Agent 连接的是云端大模型 API，`recall` 找出的片段会作为上下文发往对应模型服务商（详见 [privacy.md](privacy.md) 第 1 节）；
- **默认启用体验评价与后台 ROI 估算模块（随时可关）**：在本地模式下，我们默认启用了记忆体验评价 Hook 与评价卡，让你在日常使用中对“这次记忆到底有没有帮上忙”有直观感受；Web 后台也预置了一个粗略的 ROI 评价模块供参考（坦率讲，自动统计很难做到完全精准，看个大概趋势就好）。如果你觉得这些 Hook 提示打扰了工作，随时可以关掉或只装纯工具模式：
  ```bash
  # 方式 1：只挂载纯 MCP recall 工具，不安装任何 Hook 与指令提示
  rolling-memory install --mcp-only

  # 方式 2：保留安装，关闭 Hook 账本记录
  rolling-memory config set hooks.ledger false
  ```
- **预置了 U 盘随身模式**：因为程序和记忆库都是独立的本地文件，我们还预置了一个 U 盘便携模式（见 [`usb-portable.md`](usb-portable.md)），可以把记忆工具和记忆库一起塞进 U 盘随身带走。这个模式还比较新、我们没做充分测试，它也有个很现实的前提：**你得在不同的电脑上跑同一个项目，原来的项目记忆才会有用**。欢迎感兴趣的朋友一起尝试，好用不好用都欢迎告诉我们。

上面这些全部只靠一个本地二进制和一个本地 SQLite 金库完成。内置的 **Live Console** 把它们放在同一屏里：

![Rolling Memory Live Console — 本地金库遥测、MCP recall 账本与记忆资产增长](../assets/live-console-zh.webp)

*内置 **Live Console**（`rolling-memory panel`）一览——本地金库遥测、MCP `recall` 账本与三层记忆资产增长，全部在本机运行、零外部网络请求。*

> **关于当前发布版**：本仓库发布的是**纯本地模式（`local_plain`）**：召回、存储与结晶全部在本地金库完成，不发起任何记忆面网络请求；对外请求仅三类——你自行配置的评测大模型端点、可选的上游基准语料下载、可选的更新检查。另有面向高级与企业用户的独立**云端记忆服务**（端到端密文设计）采用申请制提供，不属于本仓库；发布与许可边界见 [DECLARATION.md](DECLARATION.md)，获取帮助见 [SUPPORT.md](SUPPORT.md)。

---

## 1. 日常工作中，它到底能带来什么？（能省一点是一点）

它做的事情非常实在：把你的历史对话和本地文档**按原话保存在本机的 SQLite 里**，用本地词法与统计检索建好索引，给你的 Agent 提供一个 `recall`（回忆）工具。

至于它能带来什么实际效果？最直观的一点是：**帮你的 Harness Agent 减少盲目搜索的往返次数。**

在日常长程工作场景中（大家的当前上下文尺寸基本都在 **`100k ~ 400k` tokens** 之间），大模型每多走一次工具往返，客户端都必须把**当前这 10 万～40 万 Token 的完整上下文重新作为输入发给模型计费一次**，还得再等一轮模型推理和工具执行。当遇到“上次那个闪烁的 Bug 在哪修的？”或者“之前为什么不用方案 A？”这类问题时：
- 如果没有记忆，Agent 往往要猜关键词跑好几轮 `grep`、`find`、`Read` 试错；
- 如果 `recall` 刚好命中了一条过往记录，直接告诉 Agent 目标在哪个文件、当时是怎么处理的，**哪怕仅仅帮你减少了 1 次往返，省下的也是当前长上下文的单次总 Token（`100k ~ 400k` tokens），同时也可能缩短一点总任务执行时长——能省一点是一点。**

当然，**如果 `recall` 没搜到有用信息、白跑了一轮，那反而会多花一次往返的 Token。** 我们专门写了一篇文档，把这笔账是怎么算的、什么情况下省、什么情况下会亏，连同**我们自己开发机最近一周（及最近 24 小时）的真实使用记录与省下的 Token 实测账单**一起摊开讲清楚：
👉 **[《本地记忆是怎么帮你省（或亏）Token 与时间的：一笔谁都能算清的往返账（附本机真实一周使用记录）》](token-economics.md)**

> **我们先交个底（我们自己最近 24 小时实测与整周推算账单）**：
> 在我们自己的主力开发机上（实测最近 7 天 `110,340` 次模型请求，单次往返平均输入上下文达 **`272.0k` tokens**；最近 24 小时均值更高，达 **`320.1k` tokens**），**最近 24 小时**产出了 **`33` 份去重后的逐轮实名自评卡**：**有 `6` 份（18.2%）诚实判定为“0 节省”**（2 次调用因后台高负载超时；4 轮会话加载的记忆索引已直接给出坐标、无需调用）；另有 **`27` 份（81.8%）确实命中了精确文件坐标或过往排障记录，累计帮 Agent 少走 `116` 次盲搜往返（每次命中平均约 `4.3` 次）**。零模型交叉验证一致：**最近 24 小时有 `46` 条召回结论完成确定性事后采纳审计、其中 `41` 条（89.1%）被实际采纳**（`38` 条直达文件/符号坐标 + `3` 条事实采纳）——而更长保留账本上的同一审计读数为 **`61.1%`**。
> 按最近 24 小时的节奏直接 **$\times 7$ 天推算整周**，一周可省下 **`812` 次盲搜往返**；按每次往返平均 `200k tokens` 保守折算，**整周毛节省约 `1.62 亿` tokens（`162.4M`；扣除半数零增益卡片后实际净省约 `1.58 亿` / `158.2M` tokens；极端保守下限 `1.54 亿` / `154.0M` tokens）**。对照 **GPT-6 Astra / Claude Fable 5.2 / Gemini 4 Argon** 官网最新价格，即便算上本机 85% 的高比例缓存折扣，一台主力开发机每周也能省下约 **`$74.7 ~ $885` 美元**（若按未缓存标准输入原价计算则相当于每周 **`$325 ~ $3,248` 美元**）。完整正反明细与三家官网价格换算表见 [`token-economics.md` §6](token-economics.md)。

**我们也实话实说：我们不承诺它一定有用。** 词法检索有它的天然天花板，我们自己也在努力持续提升——比如把工具日志的噪音剥得更干净、把召回准确率做得更高。非常希望大家在使用中提出批评建议，或者直接贡献方法和代码，我们一起把它完善好。

---

## 2. 为什么坚持「保留原话 + 本地词法统计检索」？

目前很多记忆方案会在写入时调用大模型把对话改写成摘要，或者切块转成向量。这些做法各有优势（摘要省空间、向量能跨同义词），但我们之所以选择先做扎实的本地原话检索，是基于四点很朴素的考虑：

1. **原话里包含了全部真实细节，入库时改写不仅容易丢细节，还可能引入不可逆的错误叠加**：大家对“用大模型抽取总结”有个常见的迷思，认为这能“提纯”。但别忘了，如果你的 Agent 现在理解上下文都会出错，那么后台负责抽取的 LLM 一样会理解错。**用一个可能出错的模型去改写一个可能理解偏的对话，这本质上是“错上加错”**。最可怕的是，一旦原始记录被错误地“总结”并丢弃了原文，那个错误就被永远固化了，再也找不回当时的真实情况。虽然有人尝试用“昂贵的记忆迭代”来修正，但这又陷入了新的悖论：**怎么发现它是错的？又如何保证在错的记忆被发现前，它不参与下一轮的错误扭曲？**
   所以，Rolling Memory 坚守一条核心准则：**历史上发生过的事情，就是发生了**。我们不做改写、不压缩、不“润色”，因为每改写一次，就是给幻觉和事实错误多增加一次机会。这次读到的原文是这样，下次读到的原文也必须是这样，确保证据链条的绝对保真。
2. **你的 Agent 本身阅读理解能力就很强，没必要为省几十个消费 Token 先交一大笔“抽取理解税”**：让 LLM 提前把原话抽取成简短摘要，唯一显而易见的好处是将来读取消费时每次可能少占几十、几百个 Token。但在动辄 `100k ~ 400k` 的单次往返上下文面前，省这几十、几百个 Token 其实非常有限；反过来，为了生成那点摘要，你却必须先付出实打实的 **LLM 抽取理解成本**——无论是让正在干活的 Agent 在往返中分心调工具随手记（这本身就会多走一轮几十万 Token 的往返！），还是专门用旁路 LLM 去阅读上下文提取。既然今天消费记忆的本来就是阅读理解能力很强的 Agent，不如只做零成本的确定性格式清理（去掉协议外壳、`<thinking>` 块或重复刷屏日志），把带出处的原话直接交给当前 Agent。
3. **代码符号、报错、文件名和日期本来就在字面上——稠密向量反而会模糊精确符号，而 Coding Agent 的表达天然高度一致**：日常开发里大量跨会话线索（项目内部代号、函数名、配置项、报错码、端口号、版本号、日期、文件路径）本身就是精确的字面锚点。稠密向量（Embedding）的数学本质是把文本平滑映射到连续语义空间——这虽然能拉近宽泛的近义词，却会让 `err_code_4012` 与 `err_code_4013`、端口 `8008` 与 `1828`、`v0.0.1` 与 `v0.0.2`、日期 `2026-05-12` 与 `2026-05-19` 的余弦相似度高达 `0.98` 以上而极易张冠李戴；况且静态向量模型不仅对训练截点后的私有代号失灵，首次回填几万轮历史对话还要下载几百 MB 模型权重、让风扇狂转十几分钟。更重要的是，在你的 Harness 工作中，往往是同一个大模型围绕同一个项目持续输出——AI 的表达结构规范，基本不会出现日常口语里的语病、主语省略或指代不明，用词天然高度一致。
4. **明确的分工边界——不做重型堆叠，不是因为不会，而是因为在这里没必要那么重**：当你使用 Claude Code、Cursor 或 ZCode 时，坐在你面前的本来就是最聪明的主力大模型。本地记忆工具的本职工作不是在后台再跑一个小模型去替它猜语义，而是以几十毫秒的速度把零失真的高信息量原始证据翻出来，把语义理解和推理 100% 交给你的宿主 Agent。在向量、知识图谱和大模型多轮抽取这条重型路线上，我们团队其实非常熟悉——在决定做这个极简本地工具之前，我们曾迭代过多代重型记忆架构，在公开的 LoCoMo 基准上做出过 **92.79 分** 的成绩。也正因为亲手把重型路线推到过那个高度，我们才更清楚它在日常开发里的隐性账单与延迟代价，反过来想把真正轻快、零成本的本地确定性检索做到位。

---

## 3. 它是怎么工作的？

系统分成简单的三步，前两步完全在本机按确定性规则运行，第三步交给你的 Agent：

| 环节 | 位置与实现 | 具体做什么 |
|---|---|---|
| **1. 存原文** | 本地 SQLite 文件（默认 `~/.local/share/rolling-memory/memory.db`，目录 `0700`，文件 `0600`） | 保存对话片段、挂载文档的分块偏移量、时间戳、来源标识和内容哈希。挂载本地代码或文档目录时只记录文件路径与偏移量，按需原位读取，不会把整个代码库重复复制进数据库。 |
| **2. 建索引与检索** | 由金库原文派生的本地索引（可随时重建） | 在本地明文金库上运行完全确定性的无向量信息论检索内核：纯子串分词后，由簇检索排序器融合三项机制：**① 在线自适应信息量与同日刷屏折叠**（词项稀有度权重完全随你本地记忆库的真实分布动态演化，不依赖外部冻结词表，且同一天内重复粘贴的刷屏日志会自动按对数折叠，绝不冲垮稀有词权重）；**② 跨形式确定性对齐**（无需向量模型即可将 `"May 12, 2025"` 与 ISO `"2025-05-12"`、词形时态变体、复合标识符以及 `"500ms"` / `"16GiB"` 等内联数字与计量单位对齐）；**③ 多线索事件链共鸣**（当提问中的多个稀有线索在候选记录中同时出现且时间邻近时，按无量纲信息重合度放大权重）。全程不依赖外部模型、冻结词表或网络请求。 |
| **3. 阅读与判断** | 你的宿主 Agent 或你自己 | Agent 通过唯一的 MCP 工具 `recall` 拿到相关原文片段和出处后，结合当前问题去阅读、核对和作答。 |

日常使用时不需要 Agent 手动调用“写记忆”接口：后台进程可导入受支持来源的对话轮次；其他来源可能需要手工导入。当 `recall` 查到有用片段时，系统也会在本地记录这次查询与命中片段的关联。

---

## 4. 独立第三方评测与公开基准成绩

### 4.1 AML 第二期 —— 官方第三方评测

**Agent Memory Leaderboard（AML，记忆之巅）** 是由 Datawhale 联合近 30 家高校与研究机构共同组织的公开横评：所有参赛系统使用同一套任务、同一生成模型与同一评分模型，差异主要来自记忆系统本身。Rolling Memory 以本仓库发布的同一套本地引擎参加代码赛道闭源方法榜，未做任何赛道特调。

| 代码赛道读数（AML 第二期，2026-11-19 发布） | 结果 | 名次 |
|---|---|---|
| **Noisy 新功能解决率** | **80.39%** | **全榜第 1** |
| Noisy 总体解决率 | 62.00% | 闭源方法榜并列第 3 |
| 代码赛道官方综合排名（Overall） | 61.00 | 第 5 |
| Noisy 缺陷修复解决率 | 52.53% | 中游 |

页面顶部的图片即为「Noisy 新功能」子项的官方榜单卡。请按子项粒度理解：**80.39% 是一个子能力读数，不代表 Rolling Memory 在代码记忆的每个维度都排第一。** 同一赛道的缺陷修复子项（52.53%）正说明了另一半：把同仓库历史经验迁移到新功能，是「原话 + 高信息量检索」最擅长的场景；而精确找回对当前代码版本仍然有效的旧修复，是另一种更难的检索问题，我们仍在补强。代码赛道官方综合排名为第 5；AML 三个赛道使用不同任务与指标，不能互相折算或比较。

官方榜单：<https://agentmemories.ai/leaderboard/>

### 4.2 ATM-Bench 与 LoCoMo —— 可复现的本地读数

我们也在两个公开长期记忆基准（剑桥大学 **ATM-Bench** 的 11,034 条个人多模态文本记录，以及 **LoCoMo** 的 5,882 轮长对话、1,540 道问题）上测试了这套本地检索。所有数字均可从归档的逐题检索结果（JSONL）直接复算。

> **指标说明**：下表中的 ATM-Bench 官方 `Recall@10` 严格采用官方评测脚本（`comprehensive_eval.py`）的**逐题金标集合覆盖率均值**公式；`Any-Hit@10`（前 10 条结果里至少命中 1 条金标证据的题目比例）与 `All-Hit@10`（前 10 条结果里包含全部金标证据的题目比例）作为辅助指标单独列出，不与官方 `Recall@10` 混比。插件日常使用（`rmem recall` / MCP `recall`）与 `rmem eval` 评测运行器完全共用同一个本地引擎、同一套单句谐波编码器与排序器。

| 评测指标 | ATM-Bench Main（1,013 题） | ATM-Bench Hard（31 题） | LoCoMo 全量（1,540 题） |
|---|---|---|---|
| **官方 `Recall@10`（逐题金标覆盖率均值）** | **82.77%** | **52.43%** | — |
| **官方 `Recall@25` / `Recall@50`** | 86.44% / 90.16% | 59.91% / 70.62% | — |
| **辅助 `Any-Hit@10`（至少命中一条金标）** | 85.29%（864/1,013） | 80.65%（25/31） | — |
| **辅助 `All-Hit@10`（金标全部进入前 10）** | 80.06%（811/1,013） | 29.03%（9/31） | — |
| **答案词元覆盖率 / 答案入上下文比例** | — | — | 72.93% / 53.96%（831/1,540） |
| **端到端判对率（`deepseek-v4.1-flash` 答器+判官，2026-11-16）** | — | — | **85.26%**（1313/1540 · cat1 70.57 / cat2 87.23 / cat3 71.88 / cat4 90.96） |
| **单题检索耗时（记录值；均值 / P95）** | 68.6 ms / 108.3 ms | 107.9 ms / 651 ms | 371 ms 均值（未记录 P95） |

- **现行产品臂**：上表 ATM 两行是 `local_plain` 产品臂的现行读数（语料 `c9c4cc55…`、题面 `3c4cbf84…` / `f6f63ff1…`），2026-11-20 复核。综合分（0.70 × Main R@10 + 0.30 × Hard R@10）= **73.67**。v0.0.1 冻结读数（77.21% / 46.12%）仅作为历史值保留在变更日志中，请勿与现行行混比。
- **耗时**按单次运行记录，受缓存状态、CPU 与并发影响；上表数值只作单次参考，不是跨运行基准。

### 4.3 怎么读这些数字

这些数字既展示了纯本地词法统计的潜力，也清楚地暴露了它的短板：

- **这些数字是某一时刻的记录，不是长期断言**：系统仍在迭代，榜单分数是某个构建在某个时刻的真实分数。对当前构建而言，权威读数是本节按钉住语料复算的内置基线；当榜面条目与内置基线不一致时，把榜面条目当作已合并构建的历史记录，把本节基线当作当前值。
- **仍有约七分之一的题目在 Top-10 里没找着（以及为什么这在你的 Harness 开发中影响没那么大）**：在 ATM Main 的 1,013 道题中，有 **148 道题（14.61%）** 在前 10 条里没有命中任何金标证据，其中 **86 道题** 在前 50 条里也没有命中；Hard 的 31 道题中有 **6 道题** 未进前 10（**4 道** 未进前 50）。复盘这些漏召回可以发现，找不到的基本都是**提问与原文在词面上没有交集**的情况——这在**真人生活对话**里很常见：比如聊起“老张”和“张三”，字面不是一个词却是同一个人；或者日常口语里常有指代不明、省略主语的表述（比如“这个项目……那个人……后来做完了”之类）。但在你的 **Harness Agent 日常开发**中，通常是用同一个模型围绕同一个代码库完成开发，AI 的输出用词一致、表述完整，基本不会出现语病和指代不清，讨论的核心又是固定的模块名、函数名与文件路径——恰恰适合本系统的词法分析架构，向量和图技术在这里能发挥的额外作用其实没那么大（而在向量与图记忆领域，我们团队其实也是专业的，此前在 LoCoMo 基准上做出过 **92.79 分**的成绩，之所以在这里不默认上向量和图，纯粹是基于日常开发性价比的务实取舍）。
- **加上本地小向量能补回多少（对照测试）**：在同一批 11,034 条记录上，单独跑本地向量模型 `all-MiniLM-L6-v2` 的官方 `Recall@10` 是 Main 67.35% / Hard 29.58%。在该次对照实验中（融合前参考基线：Main 76.72% / Hard 41.93%），把小权重（$w_{\mathrm{vec}} = 0.15$）的向量与本项目的词法统计主干做 RRF 融合后，Main `Recall@10` 达到 **78.42%（+1.70 个百分点，净增 17 道命中题）**，Hard 达到 **42.96%–43.20%**；但如果按**等权重（$w_{\mathrm{vec}} = 1.00$）**融合，向量噪声反而会把 Hard 拉低到 **38.68%（下降 3.25 个百分点）**。
- **同口径 ATM-Bench 公开基线参考**：在相同的标准固定 2B SGM 文本协议下，ATM-Bench 公开基线的官方 `Recall@10` 参考值为：Main 上 MemPalace 76.40%、ATM-RAG 68.70%、HippoRAG2 66.40%、Mem0 61.90%；Hard 上 MemoryOS 32.70%、HippoRAG2 31.90%、ATM-RAG 30.40%、Mem0 23.70%。**本项目两行已在 ATM-Bench 官方榜单上**：维护者于 2026-11-24 接受并合并该提交（`atmbench/atmbench.github.io`，合并提交 `42b8418`），DeepSeek V4.1 Flash 为 QS **73.55 / 39.46**、Qwen3-VL-8B 为 **58.29 / 16.63**（Main / Hard），`Recall@10` **82.77 / 52.43**。榜单脚注同时记录：这些属样本内读数，并给出同一检索下更早那次完整跑的 72.78 / 35.52 与 57.29 / 16.63。§4.1 的 AML 第二期结果是另一项官方第三方榜单成绩。

---

## 5. 使用前请了解的局限与边界

1. **记录下来 $\neq$ 内容一定正确**：记忆系统只负责告诉你“过去某条记录里这么写过”，无法保证那句话当时没说错、或者现在没过期。真伪核验始终需要人和下游 Agent 自己把关。
2. **“不调大模型”和“不出网”仅指记忆存取本身**：记忆的入库、建索引和检索完全在本地完成；但如果你的 Agent 连接的是云端大模型 API，那么被 `recall` 找出来的片段在跟随对话发给大模型时，依然会发往对应的模型服务商。
3. **评测结果带有同题集迭代的因素**：检索权重和规则在开发过程中针对 ATM-Bench 和 LoCoMo 做过多轮排错与调整，不能直接等同于在未知数据集上的盲测表现。AML 成绩是第二期单轮、单赛道榜单的读数，不保证在后续周期或其它赛道上复现。
4. **语种与超大规模场景仍待更多检验**：目前的评测集中在英文个人档案与长对话（万级记录）。更多语种、重度中英混杂以及百万级记录下的表现，还需要大家一起测试和改进。
5. **按原样（AS IS）提供**：软件不提供任何担保，完整声明见 [DECLARATION.md](DECLARATION.md)。

---

## 6. 快速开始

### 方式一：桌面客户端或一键安装脚本（macOS / Linux / Windows —— 免装 Git、Go、Node.js 或 Python）

**桌面客户端安装包（macOS `.dmg` / Windows `.zip` —— 零命令行开箱即用）**：
- **macOS（`Rolling-Memory-<version>-darwin-arm64.dmg` / `darwin-amd64.dmg`）**：打开镜像将 **`Rolling-Memory.app`** 拖入 `Applications` 并双击运行。首次启动会自动释放内置引擎与控制台组件、初始化本地记忆库、自动接线本机已安装的 AI 编程宿主，在系统右上角状态栏驻留快捷图标（支持一键同步会话、重新接线宿主与在线更新），并在原生桌面窗口中打开控制台。
- **Windows（`Rolling-Memory-<version>-windows-amd64-desktop.zip` / `windows-arm64-desktop.zip`）**：解压后双击 **`Rolling-Memory-Desktop.exe`**，即可无黑框静默完成本地记忆库初始化、AI 宿主自动接线、桌面快捷方式创建，并自动拉起本地控制台服务（`:9006` / `:8008`）与独立应用窗口。

**命令行一键脚本（无需预装任何开发环境）**：
```bash
# macOS / Linux：自动下载经 SHA-256 校验的预编译二进制（自适应 curl/wget 与镜像回退）、
# 自动将 ~/.local/bin 写入终端配置文件、初始化本地记忆库、配置已安装宿主并执行自检
curl -fsSL https://github.com/rocareer/rolling-memory/raw/main/install.sh | bash
# 可选：在桌面生成「Rolling Memory Console」快捷图标，双击即可打开本地 Web 控制台
rolling-memory install --with-hooks --desktop-shortcut

# Windows PowerShell（原生支持 x64 与 ARM64，自动将安装目录写入当前用户 PATH）：
irm https://github.com/rocareer/rolling-memory/raw/main/install.ps1 | iex
```

**装完立刻提示找不到 `rolling-memory` 命令？** 安装脚本会把 `PATH` 写进终端配置文件，但已经打开的终端仍沿用启动时的环境变量。新开一个终端即可；也可以直接复制下面这一行，再用 `rolling-memory --version` 确认：

```bash
# bash / zsh —— 只对当前终端生效；新开终端会自动读取配置里的条目
export PATH="$HOME/.local/bin:$PATH"
rolling-memory --version
```

```fish
# fish
fish_add_path ~/.local/bin
rolling-memory --version
```

```powershell
# Windows —— 已打开的 PowerShell 窗口不会自动刷新，重开窗口或执行：
$env:Path = "$env:LOCALAPPDATA\rolling-memory\bin;$env:Path"
rolling-memory --version
```

### 方式二：从源码编译（需 Go 1.26+）

```bash
git clone https://github.com/rocareer/rolling-memory.git
cd rolling-memory
CGO_ENABLED=0 go build -trimpath -o bin/rolling-memory ./cmd/rolling-memory
ln -sf rolling-memory bin/rmem
export PATH="$PWD/bin:$PATH"

rolling-memory init --write-config
rolling-memory install --with-hooks
rolling-memory install --check
```

### 方式三：在线检查与一键安全升级（命令行 & Web 控制台 —— SHA-256 强校验 · 免 Git）

```bash
rolling-memory update --check          # 仅比对本地文件与发布渠道的 SHA-256 指纹，不修改任何文件
rolling-memory update                  # 原地升级 CLI 二进制、已装控制台与宿主接线
rolling-memory update --with-console   # 一并安装或升级本地 Web 控制台（后端 :9006 + 前端 :8008）
rolling-memory panel --update          # 启动控制台前自动检查并更新前后端资源，随后打开 http://localhost:8008/#/admin
rolling-memory update --release-base https://your-mirror.example.com/releases/latest/download
```

你也可以在 Web 管理控制台（「记忆总览」或「记忆控制台」顶部操作栏）点击 **「版本与在线更新」**，在浏览器内查看本地组件的 SHA-256 指纹、在线检查新版并一键原位升级。升级过程仅替换可执行文件、前端静态资源与宿主配置接线，**绝不触碰或覆盖你的本地 SQLite 明文金库（`memory.db`）**。

> **升级默认值**：预编译安装跟随 `stable` 并在后台安装经过校验的更新；源码安装跟随 `dev` 并只做通知。通道与 `update.auto` 策略可随时在控制台更新弹窗或用 `rolling-memory config set update.channel` 修改。完整通道、破坏性变更、备份与回滚说明见[安装与升级指南](install-upgrade.md)。

---

## 7. MCP 与多宿主接入

执行 `rolling-memory install --with-hooks` 会自动检测并填入本机真实路径，一键接入 **ZCode、Claude Code、Codex、OpenCode、Cursor、Windsurf、Cline、dsh、CodeBuddy、WorkBuddy、豆包** 等宿主。如需在其他支持 stdio MCP 的客户端中手动配置，将下方 `<YOUR_HOME>` 替换为你的家目录路径（例如 macOS 的 `/Users/alice` 或 Linux 的 `/home/alice`）：

```json
{
  "mcpServers": {
    "rolling-memory": {
      "command": "<YOUR_HOME>/.local/bin/rolling-memory",
      "args": ["mcp"],
      "env": {
        "ROLLING_MEMORY_DB": "<YOUR_HOME>/.local/share/rolling-memory/memory.db"
      }
    }
  }
}
```

参数说明与返回格式见 [MCP 契约](mcp.md) 与 [多宿主部署指南](harness.md)。

---

## 8. 复现评测与独立核验

`rmem eval` 命令行评测器会用 [benchmarks.md](benchmarks.md) 公布的 `SHA-256` 摘要校验你自备的语料。**基准语料不随本仓库分发**——请从上游项目获取，用 `--eval-data <dir>` 指向你的副本，然后即可在断网环境下直接跑检索评测，并从生成的原始 JSONL 文件自行复算。两套基准都支持一条命令取数（从钉住的上游 revision 下载、本机重建并校验摘要）：

```bash
# 一条命令取数并重建 ATM-Bench 语料（pin 校验，约 35 MB）
rmem eval --fetch-only --fetch atm --accept-upstream-license

# 一条命令取数并重建 LoCoMo 上游官方题集（1,540 题，约 2.8 MB）
rmem eval --fetch-only --fetch locomo --accept-upstream-license

# 运行 ATM-Bench（Main-1013 或 Hard-31）纯本地检索评测（不调答题与判官 LLM）
rmem eval --suite atm --tier 1013 --answerer none --judge off --top-k 50
rmem eval --suite atm --tier hard --answerer none --judge off --top-k 50

# 运行 LoCoMo 纯本地检索评测（上游官方 cat1-4 全集）
rmem eval --tier 1540 --answerer none --judge off --top-k 20
```

每次运行都会在独立的评测命名空间（scope）下输出逐题命中 Unit ID 与统计摘要，不会影响你的日常个人记忆库。详见 [benchmarks.md](benchmarks.md)。

---

## 9. 文档目录

| 文档 | 内容说明 | English |
|---|---|---|
| **[省 Token 算账与边界 (`token-economics.md`)](token-economics.md)** | 详细拆解长上下文下减少一次往返能省多少 Token，以及什么情况下反而会亏（含盈亏公式与反例） | [docs/token-economics.md](../token-economics.md) |
| **[`recall` 与 `Background` 对比 `grep` (`background-vs-grep.md`)](background-vs-grep.md)** | 既然 Harness 自带 `grep`，为什么还要用 `recall` 与 `Background` 目录挂载？（边界、4 项核心价值与 3 套自证方法） | [docs/background-vs-grep.md](../background-vs-grep.md) |
| **[常见问答 (`faq.md`)](faq.md)** | 与 `grep` / `ripgrep`、向量 RAG、LLM 抽取服务以及静态 `AGENTS.md` 的区别与配合方式 | [docs/faq.md](../faq.md) |
| **[评测与复现 (`benchmarks.md`)](benchmarks.md)** | LoCoMo 与剑桥 ATM-Bench 本地复现命令、评测协议校验与原始 JSONL 复算说明 | [docs/benchmarks.md](../benchmarks.md) |
| **[MCP 契约 (`mcp.md`)](mcp.md)** | `recall` 工具的输入参数、返回结构与后台自动收录机制 | [docs/mcp.md](../mcp.md) |
| **[配置参考 (`configuration.md`)](configuration.md)** | 配置文件位置与优先级、常用配置（交付窗口、引擎内存上限）、环境变量，以及控制台「系统管理 → 记忆服务」的等价操作 | [docs/configuration.md](../configuration.md) |
| **[多宿主接入 (`harness.md`)](harness.md)** | 各大 AI 编程助手的一键安装与 `install --check` 状态检查 | [docs/harness.md](../harness.md) |
| **[U 盘便携模式 (`usb-portable.md`)](usb-portable.md)** | 将记忆库放在移动存储设备上的初始化与安全卸载方式 | [docs/usb-portable.md](../usb-portable.md) |
| **[Web 管理控制台 (`plugin-console.md`)](plugin-console.md)** | 本地管理面板、记忆浏览、状态监控与评测实验室说明，以及 3D 记忆图谱**预览**（内置 LoCoMo 示例） | [docs/plugin-console.md](../plugin-console.md) |
| **[隐私与本地数据 (`privacy.md`)](privacy.md)** | 本地数据存储、权限控制、历史受众限制、备份与删除方法 | [docs/privacy.md](../privacy.md) |

---

## 10. 常见疑问：各家 Harness 厂商都有自己的记忆工具了，为什么还要用你们的？

Claude Code、Cursor、Windsurf、ZCode 等工具提供各自的指令和记忆功能。它们与 Rolling Memory 可以互补：笔记提供简明指引，已导入的来源可供本地检索。

1. **宿主自带的笔记“写的时候要花钱，而且只记了冰山一角”**：
   宿主自带的 Auto-Memory 通常依靠 Agent 主动写笔记，未写入的细节可能不会保留。Rolling Memory 可在本地索引受支持的对话来源，让 `recall` 按需检索已导入的原文；它不保证自动采集全部历史，其他来源可[手工导入](harness.md#45-对话采集范围与手工导入)。
2. **常驻笔记塞多了会撑爆上下文，而按需检索平时不占地方**：
   `AGENTS.md` 或 `MEMORY.md` 适合保存简明的项目规则；不同宿主加载它们的时机和方式不同，文件过大可能占用更多上下文。已导入的历史记录可以通过 `recall` 按需检索。
3. **跨工具、跨 Harness 共享同一个本地记忆底座（不被单一工具锁定）**：
   接入的多个工具可以查询同一个本地 SQLite 金库，包括已从受支持来源导入的记录；接入工具本身不等于自动导入该工具的对话历史。
4. **保留未经模型改写的“第一现场原话”，防止摘要笔记写错后死无对证**：
   宿主自带的自动记忆往往是模型总结后的几句话。一旦模型总结时理解偏了，那条错笔记就会一直误导后续会话。Rolling Memory 不改写事实，存的是当时的原话和出处，相当于给你的所有 Agent 配了一个本地“行车记录仪”，随时能核对当时到底是怎么说的。

---

## 11. 源码许可、贡献入口与致谢

- **贡献入口**：非常欢迎大家提 Issue、分享想法或提交 PR 一起完善——见 [CONTRIBUTING.md](CONTRIBUTING.md) 与 [SUPPORT.md](SUPPORT.md)。本项目采用**「人类定义架构、形式化不变量与验收标准 + AI 辅助代码实现」**的协作模式；完整免责说明见 [DECLARATION.md](DECLARATION.md)。
- **贡献者**：外部贡献统一记录在 [CONTRIBUTORS.md](../../CONTRIBUTORS.md)。感谢 [@Buyou27](https://github.com/Buyou27) 提交 Windows PowerShell 5.1 兼容性修复（[`install.ps1`](../../install.ps1)，[#2](https://github.com/rocareer/rolling-memory/pull/2)）。
- **源码许可**：采用 **[PolyForm Noncommercial License 1.0.0](../../LICENSE)**（SPDX：`PolyForm-Noncommercial-1.0.0`）公开全部源码（含 `sdk/` 下客户端 SDK，见 `sdk/LICENSE`），**任何非商业用途均可自由使用、修改与分享**；**商业使用与商业分发（含 SaaS 托管与商业产品内嵌）须另行签署书面商业许可**。第三方组件署名见 [NOTICE](../../NOTICE)。
- **致谢**：本项目的 Web 管理控制台以 **[AI GO ADMIN](https://github.com/ai-go-hub/ai-go-admin)**（Apache-2.0，Go + Gin + Vue 3 后台框架；镜像：[Gitee](https://gitee.com/ai-go-hub/ai-go-admin) · [AtomGit](https://atomgit.com/ai-go-hub/ai-go-admin)）为基座衍生开发；其 UI 血统承自 **[BuildAdmin](https://gitee.com/wonderful-code/buildadmin)**（Apache-2.0，© 妙码生花），插件清单页按 BuildAdmin `v2.3.9` 移植改写。逐文件归属与修改说明见 [NOTICE](../../NOTICE) §2。感谢两个开源项目的作者与社区。

Copyright © 2026 Rocareer & Rolling-Memory Organization (Albert `<albert@rocareer.com>`). All rights reserved.
