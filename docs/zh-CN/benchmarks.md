# 基准评测与可复现指南（`rmem eval`）

**[简体中文](benchmarks.md) · [English](../benchmarks.md) · [常见问答](faq.md) · [返回文档中心](README.md)**

Rolling Memory 提供一条命令、协议冻结的评测运行器（`rmem eval`），覆盖剑桥大学 ATM-Bench（第一主基准）与 ACL 2024 LoCoMo（长程对话对照参照）。**基准语料本身不随本仓库分发**——请自行从上游项目获取，并用下文公布的 `SHA-256` 摘要钉住。默认评测臂为 **`local_plain`**：在本地明文金库上完成端侧检索，全程 0 网络请求。云端脉冲臂属于申请制的云端能力预览（见 [privacy.md](privacy.md)），不在本发布版文档范围内。

> **读数证据要求**：所有公开展示的成绩必须携带实际溯源（`transport / scope / corpus_sha256 / commit / 逐题检索 ID`）。Oracle 只是人工参考标注上限对照，不是引擎召回成绩。

---

## 1. 语料与评测数据面

### 1.1 自备语料

评测使用两套公开基准，均不随包，需从上游获取后按 §1.2 的摘要在本机钉住。

**数据许可**：两套语料均由上游作者以 **CC BY-NC 4.0**（非商业）发布 —— ATM-Bench 数据集卡片原文为 “This dataset is released under CC BY-NC 4.0. The accompanying code is released under the MIT License.”（数据：<https://huggingface.co/datasets/Jingbiao/ATM-Bench>）；LoCoMo 仓库自带 CC BY-NC 4.0 的 `LICENSE.txt`（<https://github.com/snap-research/locomo>）。基于上述非商业条款，两套语料及派生的题集文件均**不随本仓库或其发布资产分发**；请按上游条款从上游项目获取，并以 §1.2 的摘要钉住。

**一条命令取数**：`rmem eval --fetch atm --accept-upstream-license` 会从钉住的 Hugging Face revision 下载必要 ATM-Bench 文件（约 35 MB：邮件、图像/视频预处理字幕与两套题集），在本机重建 pinned 语料与题集，对照下方 `SHA-256` 摘要校验后直接开始评测。`rmem eval --fetch locomo --accept-upstream-license` 对 LoCoMo 同样一步到位：下载上游 `locomo10.json`（约 2.8 MB，钉住 revision），在本机确定性重建上游官方 cat1-4 题集（1,540 题，随附官方证据）与 5,882 轮语料，并在 `UPSTREAM-PROVENANCE.json` 记录上游 revision 与重建摘要。下载行为遵循上游数据许可（CC BY-NC 4.0），需由该旗标显式接受；`--fetch-only` 只取数不评测，重复运行会跳过摘要已一致的文件。数据落在你的本地数据目录（用 `--eval-data <dir>` 时 ATM 为 `<dir>/atmbench`，LoCoMo 用数据目录根）。

| 基准 | 上游来源 | 语料 |
|---|---|---|
| **ATM-Bench**（第一主基准） | 剑桥大学 ATM-Bench —— 榜单站点 <https://atmbench.github.io/leaderboard.html> | SGM 多模态个人记忆语料：6,742 封邮件 + 3,759 张照片 + 533 段视频 = **11,034** 条 |
| **LoCoMo**（对照参照） | ACL 2024 LoCoMo | LoCoMo10 标准快照：**5,882** 轮对话；题集以上游官方 cat1-4 全集（**1,540**）为准。此前的便捷档 100/200 任何形态均不再提供。 |

运行器接受以下两种落盘形态：

**（a）带 pin 的资产包**（我们自己的读数就是以此形态产出的）：

```text
<eval-data>/
  MANIFEST.json                 # 语料 + 题集 + 上游来源声明
  corpus.jsonl                  # LoCoMo，5,882 行
  tiers/questions-1540.json     # 1,540 = 上游官方 cat1-4 全集（随附官方证据）
  atmbench/
    MANIFEST.json
    corpus.jsonl.gz             # ATM-Bench SGM，11,034 行
    tiers/questions-1013.json   # Main-1013
    tiers/questions-hard.json   # Hard-31
```

**（b）ATM-Bench 上游原始目录形态**（不重打包 `corpus.jsonl.gz`、不重打包题集）：

```text
<eval-data>/
  raw_memory/email/emails.json
  processed_memory/image_batch_results.json
  processed_memory/video_batch_results.json
```

形态（b）由 SGM 装载器直接读取，但 §1.2 的 `SHA-256` 预校验只适用于形态（a）——对原始目录请显式加 `--no-verify` 跳过摘要预校验。

**指定语料目录**：`rmem eval --eval-data <dir>`。不给该参数时，解析顺序为：`ROLLING_MEMORY_EVAL_DATA` → 可执行文件同级（及最多上两级）→ 当前工作目录及最多四级祖先 → `$XDG_DATA_HOME/rolling-memory/evaldata` → `$LOCALAPPDATA/rolling-memory/evaldata` → `~/.local/share/rolling-memory/evaldata`、`~/Rolling Memory/evaldata`、`~/rolling-memory/evaldata`。ATM-Bench 另认 `ROLLING_MEMORY_ATM_DATA` 与 `raw_memory/email/emails.json` 标记。

### 1.2 公布的语料 pin

```text
ATM-Bench atmbench/corpus.jsonl.gz      c9c4cc55dd3be147638121765b5dc7b8321a5e292b90909b68ae7fe8a57ee3a7
ATM-Bench atmbench/tiers/questions-1013.json  3c4cbf84deeb158c78d515626a540908878d304ce0cda5c8e2ae7e52c6b878a2
ATM-Bench atmbench/tiers/questions-hard.json  f6f63ff196a9f1de5715e35b328d4e80ca80311f2e2eae8028e7cb60e87ad37e

# 上游文件 pin（由 `rmem eval --fetch locomo` 校验）：
#   data/locomo10.json  79fa87e90f04081343b8c8debecb80a9a6842b76a7aa537dc9fdf651ea698ff4（revision 3eb6f2c5）

# 归档 LoCoMo 包（仅存档历史读数溯源用；不再分发，亦不可逐位复刻——旧文件内嵌构建时间戳）：
#   LoCoMo corpus.jsonl              6c7e5ca22618d9632d71b24b698e966f9bd296ebf1211992bb559e1665185440
#   LoCoMo tiers/questions-100.json   48688b20c7dcd5cdca4cd355e04b7d94bb005b8e3523c66fedb33906a0acfeb5
#   LoCoMo tiers/questions-200.json   0734f89871035f80ef5a46e65756123f56b1f6df2c77f6500f48ab7c953489ef
#   LoCoMo tiers/questions-1540.json  6dd815a5fcca55eabee6345fca326192b167950aa845b7e8d330c1d43c2eee5f
```

ATM-Bench 语料摘要另以强制 pin 形式编入运行器：重打包后与之不符的语料会在任何题目被打分之前就被拒绝。

**诚实说明（派生件）**：ATM-Bench 题集为项目派生且可逐位重建：`rmem eval --fetch atm` 在你的机器上从钉住的上游 revision 重建语料与两档题集，并对照上表 `SHA-256` 摘要校验——校验通过即为与我们公布读数**逐位同源**的输入。LoCoMo 题集以上游官方为准：`rmem eval --fetch locomo` 从钉住的上游 revision 确定性重建官方 cat1-4 全集（1,540），并在 `UPSTREAM-PROVENANCE.json` 记录上游 revision 与重建摘要；它与归档历史包**不是逐位一致**（旧文件内嵌构建时间戳与未公开的中间池），历史包不再分发。便捷档 100/200 任何形态均不再提供。

### 1.3 执行面

- **端侧运行**：检索引擎为本地 SQLite 明文金库（FTS5 + BM25 + 整数 IDF），金库同时保存每个返回 Unit ID 对应的原始明文。
- **零出网**：评测不会把对话、问题或结果发送到任何远端；可完全离线复现。
- **fail-closed**：金库打开失败、数据目录缺失、语料指纹不符或准备清单不匹配任一发生即终止评测；结果绝不从第二来源补足。
- **语料缺失绝不静默打分，也绝不打断测试套件**：依赖语料的测试在无语料时**跳过（skip）而非失败**，因此本仓库自带测试在没有语料时保持全绿；而 `rmem eval` 本体以明确的「找不到评测数据目录」错误退出，绝不产出空的或伪造的读数。
- **评测命名空间（每次评测自动分配）**：评测不需要手工准备作用域——每次新评测都会自动获得专属评测命名空间内的独立世代作用域；用户默认个人记忆空间（`default`）与历史评测 scope 均不可用于新评测。空闲下来的世代会在下一次评测时自动回收，正在运行的评测绝不被回收。

---

## 2. 运行检索评测

以下命令只关闭 **Answer/Judge LLM** 调用；检索本身完全在本地完成。若语料不在默认搜索路径下，请加 `--eval-data <dir>`。

```bash
# 第一主基准 — ATM-Bench：Hard-31 与 Main-1013
rmem eval --suite atm --tier hard --answerer none --judge off --top-k 50
rmem eval --suite atm --tier 1013 --answerer none --judge off --top-k 50

# 对照参照 — LoCoMo：完整语料摄入，跑官方 Cat 1–4 全量集（tier 1540；便利档 100/200 已停用）
rmem eval --tier 1540 --answerer none --judge off --top-k 20

# 最小冒烟：只取 3 条语料、1 道题；使用独立本地 Vault
rmem eval --tier 1540 --limit 1 --corpus-limit 3 \
  --answerer none --judge off --top-k 3 \
  --eval-store /tmp/rame-eval-smoke.json --out-dir /tmp/rame-eval-smoke-out
```

**准备清单控制**：`--rebuild` 为本次评测分配新的世代作用域并回收空闲世代（仅限评测命名空间内）；`--reuse-prepare` 仅在 scope/语料指纹完全一致时复用既有准备态；`--verify-prepare` 只读核验 manifest 与语料，不摄入、不跑题。

`--mode`、`--retrieval`、`--eval-db` 与图交付帽参数不在复现命令支持范围内。

---

## 3. 可选：冻结 Answer/Judge

端到端问答需要用户自行提供 OpenAI 兼容模型并承担推理成本。协议文本及 SHA-1 钉保持不变：

```bash
export ROLLING_MEMORY_EVAL_API_KEY="your-own-llm-api-key"

rmem eval --tier 1540 \
  --answerer llm --judge llm \
  --eval-base-url "https://api.deepseek.com/v1" \
  --eval-model "deepseek-chat" --top-k 96 --context-chars 24576
```

| 协议组件 | 锁定 SHA-1 |
|---|---|
| Answer System | `0d59162805571b5622b50446ee8da12490737ade` |
| Judge System | `497f78929df5d4479c84ae7dd9993b4161f3a475` |
| Judge User Template | `38f9b6a17ffd58bf58bb123de44ce40442f1a85b` |

---

## 4. 独立复算与验证

每次运行在 `<out-dir>` 写入 `summary-sdk-<臂名>-*.json` 与 `results-sdk-<臂名>-*.jsonl`（臂名写在文件名中），并在 `<eval-store>.manifest.json` 写入准备清单。至少核验：

1. 记录的 `transport` 指向当前实际使用的评测臂，且 `local_fallback = false`；
2. `scope` 属于专属评测命名空间（绝不是你的个人记忆空间）；
3. `corpus_sha256` 与你自备的语料一致（请与 §1.2 中同名文件的 pin 比对），`documents` 等于实际摄入单元数；
4. 每题均记录其检索 Unit ID，且每个 Unit ID 都能在本地独立 Vault 中还原；
5. 直接读取原始 JSONL 重算 `evidence_hit`、`R@10/R@25/R@50`，不要只信终端汇总；
6. 用另一道题或另一 scope 做交叉探针，确认真实用户记忆和其他评测集均不可见。

可显式运行 `--oracle` 获取参考标注上限对照，但其 `transport=oracle-gold`，不得与引擎检索成绩混称。

---

## 5. 报告与榜单规则

- **溯源要求**：每次评测记录存储模式、scope、语料 SHA-256、commit 与逐题检索 ID；缺少这些溯源的读数只归档、不发布。
- **冻结发布**：只在冻结 commit 与语料上发布读数，并在同一文档写明判分协议与限制。
- **禁止跨臂混称**：本地读数绝不写成远端引擎成绩；缺少溯源的旧读数不复用。
- **榜单规则**：本仓库发布的读数默认不是 ATM/LoCoMo 官方榜单条目；除非外部榜单明确接受该臂与完整协议，且提交记录完整。

---

## 6. 本地模式基准披露（`local_plain`）

默认 `local_plain` 评测臂 100% 离线运行于本地 SQLite 明文金库（`FTS5` + `BM25` + 整数自信息连续增益与无量纲事件链共鸣），全程 0 网络请求、0 向量索引、0 建库大模型调用。插件日常使用（`rmem recall` / MCP `recall`）与 `rmem eval` 评测运行器完全共用同一个本地引擎、同一套单句谐波编码器与排序器。

| 基准套件 / 切分 | 语料单元数（`corpus_sha256` 前缀） | 官方 `Recall@10`（逐题金标覆盖均值） | 辅助 `Any-Hit@10`（二值触达率） | `Recall@10GT`（Top-10 全证据率） | 端到端 `QS`（`gpt-5-mini` minimal 官方判官） |
|---|---:|---:|---:|---:|---:|
| **ATM-Bench（`Main-1013`）** | 11,034（`c9c4cc55…`） | **77.21%** | **80.45%**（`815/1,013`） | **73.94%**（`749/1,013`） | **65.54%**（`DeepSeek-V4.1-flash` 答题） |
| **ATM-Bench-Hard（`Hard-31`）** | 11,034（`c9c4cc55…`） | **46.12%** | **80.65%**（`25/31`） | **22.58%**（`7/31`） | **27.38%**（`DeepSeek-V4.1-flash` 答题） |
| **LoCoMo（`tier 1540` 官方 Cat 1–4）** | 5,882（`6c7e5ca2…`） | 不适用（tier 1540 无金标证据） | 不适用 | 不适用 | **85.26%**（`1313/1540` · cat1 70.57 / cat2 87.23 / cat3 71.88 / cat4 90.96） |

- **适用范围与限制**：`local_plain` 提供端侧确定性词法统计检索（`transport = ramesdk.local_plain.offline-v1`，`local_fallback = false`），不等同于远端引擎计算精度。`Hard-31` 为 31 道多证据聚合题小样本（二值命中率每 $\pm 1$ 题对应 $\pm 3.23$ 个百分点）。
- **LoCoMo 运行溯源**：答题与判官均为 `deepseek-v4.1-flash`（冻结协议，判官 system SHA-1 `497f7892…`），`--top-k 96 --context-chars 24576`，48 并发，耗时 2,040.6s，0 解析失败 / 0 错误；tokenz-r 交付量 mean 5,164.7 tok/题，召回 mean 371ms；五桶 HIT 1313 / JUDGE_AMBIGUITY 73 / RECALL_MISS 154；台账 `run-20261116-170142`（逐题 1,540 条）。判官为 deepseek（非榜面 GPT-4.1），与公开榜单横比须注意判官口径；同判官 2026-11-13 同臂为 82.01%（+3.25pp，非配对 A/B）。检索内核本体读数 **r@10 = 57.09% / r@50 = 75.51%**（官方金标交均）取自带金标的 `tiers/questions-200.json` 证据臂；tier 1540 无金标证据，其 `evidence_hit` 恒 0 属构造使然而非测量失败。
