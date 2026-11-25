# 协作诊断

[English](README.md)

这套诊断检查助手在缺历史、已有现场坐标、来源不全或旧记录已过时的情况下，下一步做什么。只用合成题和临时金库，不读个人会话、金库或宿主设置。

## 在本机运行

先启动支持工具调用和随机种子的 OpenAI 兼容本机模型服务。例如，用已有的模型文件：

```sh
llama-server --model /path/to/model.gguf --host 127.0.0.1 --port 32190 --parallel 2
```

在项目根目录构建并运行。输出目录必须是新的；`-weights` 应指向服务实际加载的权重，诊断会记录其 SHA-256。`-model` 填服务接收的模型名，`-server` 填服务构建和模型标识。

```sh
go build -trimpath -o /tmp/rm-diagnostic-bin ./cmd/rolling-memory
go run ./diagnostics/collaboration \
  -binary /tmp/rm-diagnostic-bin -skills skills/rolling-memory \
  -endpoint http://127.0.0.1:32190 -model local \
  -weights /path/to/model.gguf -server 'llama.cpp BUILD / MODEL' \
  -out /tmp/rm-collaboration-run
```

需要版本对照时，另传 `-baseline /path/to/older/rolling-memory -baseline-skills /path/to/older/skills/rolling-memory`；两边分别读取对应版本的 MCP 握手、工具说明和 Skill。事实题的检索统一经当前二进制在同一个合成金库中回放；按 ID 回源则走各自的二进制，检验各版本能否完成读取。

不启动模型也能复算已有读数：

```sh
go run ./diagnostics/collaboration -review /tmp/rm-collaboration-run/receipt.json
```

## 怎么看读数

[公开题集 v5](cases-v5.json) 共 50 题，中英文各 25。它用 4 道真实回源题替换 [v4](cases-v4.json) 中 6 道模拟选 ID 的题。回源题先用较小的本地交付上限取得真实回包，确认有引用 ID、正文已缩短且不含关键条件，再把回包交给模型。每边用三个固定种子，同一题的重复运行不等于三个独立样本。复算 v4 回执时加 `-suite diagnostics/collaboration/cases-v4.json`。

- **动作正确**：选择了预期的下一步：查询历史、读现行文件、核对来源单元，或直接回答。
- **该查与不该查**：分别看必要召回和无益召回，不用少查掩盖漏查。
- **目标证据命中**：事实题只有在完整原文交付（未指定或 `full`）时才计入；标记出现在返回正文中才算命中。多传默认参数可能仍命中，但不算严格通过。
- **摘要顶替原文**：事实题被 `probe` 或 `recent` 之类紧凑回执回答时单独计数——即使摘要碰巧保留了标记，也不当成交付了原文。这正是 v4 新增的对抗面。
- **来源选择正确**（`correct_source`）：请求了回包中引用的对话单元 ID。选对 ID 不等于读回原文。
- **核验回源**（`verified_source_reads`）：实际 MCP 调用返回了同一个单元的完整原文，逐字节比较。`inspect` 仍是模拟动作，不会执行模型生成的文件读取。
- **严格通过**：动作、相关参数均合格；历史题还须命中目标证据。直接回答只检查有完整的非空输出，**不自动判语义**。要确认没有武断宣称“从未发生”或“已核验”，请复核原始答复。
- **三轮都过**：三个种子都严格通过的不同题目数，不是统计显著性。

`receipt.json` 保存二进制、提示、模型、题集与结果指纹，以及分语言、分场景的读数。`observations.jsonl` 保存逐题动作、参数、直接答复、回放结果，以及模型看到的缩短原文。两份一起保存。传输失败或不完整的运行不会产生完整回执；单题失败不从分母剔除。跨版本只在题集、权重与设置相同的条件下对照；这不是生产采纳率或最终答题质量。其他指标见[基准复现](../../docs/zh-CN/benchmarks.md)。

## 加题

只用合成内容。历史场景要有相似但不正确的干扰记录，证据标记应唯一且不能出现在题面或对照查询里。也要有明确不该调用的题。改变题目或期望就另立版本、保留旧集；要比较就让两版产品都跑同一个题集，不为已经看到的分数改题。
