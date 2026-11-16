# RAME 官方客户端 SDK（`RAME-BlindPulse-v1` 规范）

**[简体中文](README.zh-CN.md) · [English](README.md)**

本目录提供 **Rolling Autonoetic Memory Engine（洛灵自知记忆引擎）**（`RAME` · `/open/memory/v1/*`）的官方**零依赖客户端 SDK**（**Go、Python、Node.js、Java**），以及跨语言黄金测试向量集（`spec/golden_vectors.json`）。**Go** 与 **Python** 提供完整客户端（逐位一致的编码器 + 本地明文存储 + 客户端入口）；**Node.js** 与 **Java** 提供逐位一致的编码器。

**SDK 版本：`1.11.3`**（语义化版本 · 与线格式规范 `RAME-BlindPulse-v1` 独立版本化）。每个 SDK 都以命名常量暴露该版本（`ramesdk.SDKVersion` / `SDK_VERSION`）。**任何 SDK 变更都必须提升该版本号**；四个 SDK 必须与线格式规范保持逐字节一致，任何服务或宿主都不得通过导入引擎内部包来使用编码器（引擎是服务：只能经本 SDK / `/open/memory/v1/*` 协议访问）。

---

## 1. 目录结构

| 语言 | 路径 | 运行时与依赖 | 验证命令 |
|---|---|---|---|
| **Go** | [`sdk/go/ramesdk.go`](go/ramesdk.go) | 仅 Go 1.26+ 标准库（`crypto/hmac`、`crypto/sha256`、`unicode`） | `go test -v ./sdk/go/...` |
| **Python** | [`sdk/python/rame_sdk.py`](python/rame_sdk.py) | 仅 Python 3.8+ 标准库（`hmac`、`hashlib`、`unicodedata`） | `python3 sdk/python/rame_sdk.py <secret> <id> <text>` |
| **Node.js** | [`sdk/nodejs/index.mjs`](nodejs/index.mjs) | Node.js 18+ 内置 `node:crypto`（`BigInt` uint64 线格式序列化） | `node sdk/nodejs/index.mjs <secret> <id> <text>` |
| **Java** | [`sdk/java/RameSdk.java`](java/RameSdk.java) | JDK 11+ 标准库（`javax.crypto.Mac`、`Long.toUnsignedString`） | `java sdk/java/RameSdk.java <secret> <id> <text>` |
| **规范与黄金向量** | [`sdk/spec/golden_vectors.json`](spec/golden_vectors.json) | 跨语言标准测试向量（英文 / 驼峰、中日韩滑动二元组、日韩阿拉伯多语种、代码、标点） | 由 `TestCrossLanguageSDKGoldenParity` 自动校验 |

---

## 2. `RAME-BlindPulse-v1` 确定性客户端算法规范

为保证 RAME 服务、随包客户端与多语言 SDK 之间**零漂移**，同时严格遵循 **100% 语种无关、零词表、零字符截断** 的规范，每个 SDK 都实现完全相同的四阶段编码流水线：

### 阶段 1：256-bit 本地密钥派生与 `KeyedHash64`
- 给定用户密钥字节串 $S$：$K_{\text{user}} = \text{SHA-256}(S) \in \{0,1\}^{256}$。
- 给定表面词元字符串 $w$，令 $\text{low} = \text{ToLower}(\text{TrimSpace}(w))$ 并以 UTF-8 编码：
  $$h = \text{LittleEndian.Uint64}\bigl(\text{HMAC-SHA256}(K_{\text{user}},\ \text{UTF8}(\text{low}))[0:8]\bigr)$$

### 阶段 2：确定性句子切分（`SplitSentences`）
- 在 `\n`、`\r`、`。`、`！`、`？`、`；`、`;`、`!`、`?` 处切分文本。
- 句点 `.` 切分句子，**除非**其紧邻的前后两个 Unicode 码点均为 `Letter || Number`（以保留 `v1.0.1`、`655.59`、`a.b`）。
- 去除首尾空白并丢弃空片段。

### 阶段 3：语种无关的表面词与滑动二元组切词（`SplitWords`）
- 从左到右扫描 Unicode 码点，跳过既非 `Letter`（`L*`）、`Number`（`N*`）也非 `Mark`（`M*`）的码点：
  1. **连续汉字方块字（`isHanCp`）**：
     - 区段：`U+4E00..U+9FFF`、`U+3400..U+4DBF`、`U+F900..U+FAFF`、`U+20000..U+2FA1F`。
     - 对连续汉字段 $c_0 c_1 \dots c_{m-1}$：若 $m = 1$，发射 `[c_0]`；若 $m \ge 2$，先发射重叠滑动二字组 `[c_0c_1, ..., c_{m-2}c_{m-1}]`，再发射单字 `[c_0, ..., c_{m-1}]`。
  2. **连续假名 / 泰文 / 老挝文 / 高棉文（`isKanaOrSEAsianCp`）**：
     - 区段：`U+30FC`、`U+3040..U+30FF`、`U+FF66..U+FF9F`、`U+0E00..U+0EFF`、`U+1780..U+17FF`。发射最大的连续非汉字无空格段。
  3. **空格分词文字与标识符（拉丁、西里尔、阿拉伯、天城文、韩文、数字）**：
     - 消费 `Letter || Number || Mark` 的最大连续段，当连接符 `-`、`_`、`.` 紧邻后续 `Letter || Number || Mark` 时允许其出现在内部。
     - 发射完整词元，随后发射按连接符、`aB` 驼峰与字母-数字边界切出的复合子词。

### 阶段 4：载体宽度 `b`（`uint16`）与形态位掩码 `f`（`uint8`）
- 确定性载体宽度 `CarrierUnits(r)`：ASCII 字母 $\to 20$，ASCII 数字 $\to 33$，其他 ASCII $\to 10$；汉字 $\to 100$，韩文 $\to 70$，假名 / 东南亚文字 $\to 42$，Unicode `Nd` $\to 33$，Unicode `L*` $\to 20$，其他 $\to 10$。
- 重复阻尼：若 $\text{ToLower}(r)$ 连续重复且 $\text{runLen} \ge 3$，其载体单位除以 $4$。钳位 $b = \min(65535, \max(10, \sum rw))$。
- 位掩码 $f$：`1<<0` 大写（`wordIdx > 0 || idx > 0`），`1<<1` 驼峰（`idx > 0 && hasLower`），`1<<2` 数字（`N*`），`1<<3` 连接符（`-`、`_`、`.`）。

### 阶段 5：纯协议交换与本地明文还原（`PlaintextStore` × `Client`）
- **3 个本地记忆层**：`background`（`LayerBackground`）、`episodic`（`LayerEpisodic`）、`noetic`（`LayerNoetic`）。
- **四维空间物理坐标与自由块切片**：`UnitMeta`（含 `file_parse`）、`SubUnit`、`PackCompositePackets`（父单元 + 子单元组合，携带 `cited_unit_ids`）、`BlindPulsePacket` 与 `CloudQueryHit` 可携带无明文的可选空间承诺（`doc_h`、`block_h`、`line_start`、`line_end`），由 `file_url` 与 `block_id` 经 `KeyedHash64` 派生，从而在任意文档切片上实现同文档与相邻行共振，且不向云端暴露文件路径或结构。
- **标准 `RMBP`（`atoms_b64`）二进制线格式载荷与内置 `/health` 编解码校验**：所有 SDK 将 `{h, b, f}` 槽位与句子帧编码为标准 `RMBP` Golomb-Rice 混合进制 `atoms_b64` 载荷（`EncodeWirePayload` / `DecodeWirePayload`），并提供内置编解码自校验（`VerifyCodecLocal`，以及针对 `/open/memory/v1/health` 的 `Client.VerifyCodec` / `Client.Health`）。
- **严格的云端交换**：`Client.IngestPacket`、`Client.Remember*`、`Client.QueryHits`、`Client.Recall`、`Client.VerifyAuth`、`Client.Health`、`Client.VerifyCodec`、`Client.ListCloudUnitIDs`、`Client.UpdateUnit` 与 `Client.DeleteUnits` 经 `/open/memory/v1/*` 交换密文脉冲（`pulse_cipher`）或明文脉冲（`pulse_plain`），校验回显的 `tenant_id / scope`，并用本地 `PlaintextStore`（`MemoryStore`、`JSONFileStore` 或自定义存储）还原返回的 Unit ID。

---

## 3. 自定义切词（BYO-Segmenter）与检索质量建议

> **开发者建议**
>
> 开发者可以在客户端使用自己的切词方式（领域词典、AST 切词器或第三方切词组件）——云端引擎只消费 `{h, b, f}` 整数脉冲与 `sents` 句子帧。**但由于 `h` 是不可逆的密码学承诺，端侧切词质量会直接决定整个记忆系统的检索召回、共现拓扑与干句提纯质量：**
> 1. **避免未做谐波归一的长粘连块（`b > 320`）**：把整句或长分句作为单个词元发射且不带子词谐波，会在任一单词不同时导致部分查询失配。
> 2. **避免单字符碎屑（`b <= 20`）**：把空格分词文本全部拆成单字符会使其文档频率（DF）饱和，抹平波形对比度。
> 3. **保持内生自适应谐波开启**：把自定义切词器接入 Go 薄客户端（`Vault.SetWordSegmenter`）时，保持自适应谐波发射开启（默认），自定义多词词元会自动发射等宽子载体谐波（$\sum B_{\text{sub}} = B_{\text{parent}}$），同时保留自定义短语精度与 100% 子词兜底召回。
> 4. **优先使用非破坏性标准谐波叠加（`SetCanonicalHarmonics`），而非替换词典**：对于结构化日期（`2026/11/8`、`2026.11.08`、`2026年11月8日` $\to$ `2026-11-08` ISO 信标）或跨语种计量 / 地名等价（`kg` $\leftrightarrow$ `千克`，或内置可选的 ISO 3166 L0–L3 多语种地理层级展开器 `LookupISO3166GeoHarmonics` / `lookup_iso3166_geo_harmonics` / `lookupISO3166GeoHarmonics`，将 13+ 种语言的城市 / 省州 / 国家映射到 `@geo:cc:<iso2>` 与 `@geo:cont:<cont>`），启用 `SetCanonicalHarmonics`（`EnableStructuredDates` + `HarmonicExpander`）注入伴随谐波槽位（`b = B_even - 1` / `B_even - 3`，`f = FlagHasConn | FlagIsHarmonicCompanion`），而不删除或改写原生切词槽位。
> 5. **监控云端 `pulse_signal_score`（`0..100`）**：检查 `/open/memory/v1/ingest` 与 `/open/memory/v1/doctor` 返回的 `pulse_signal_score`、`pulse_signal_grade`（`S/A/B/C/F`）与 `pulse_signal_warnings`（分数 `< 70` 表示客户端切词正在削弱记忆检索能力）。

---

## 4. 标准谐波表与可选增益附加项（`SetCanonicalHarmonics` · 非破坏性密文标签归一化）

### 4.1 什么是标准谐波表（`SetCanonicalHarmonics`）？
当客户端直接用 `HMAC-SHA256(K_user, lower(w))` 对表面词元做哈希时，跨语言、符号或日期格式的两种等价表达——例如 `"50 kg"` 与 `"50 千克"`、`"$100"` 与 `"100 USD"`、`"Tokyo"` 与 `"東京"`、`"2026/11/8"` 与 `"2026年11月8日"`——会生成完全无关的 64 位哈希 `h`。

**标准谐波表**是**可选的客户端增益附加项**（`SetCanonicalHarmonics` / `set_canonical_harmonics` / `setCanonicalHarmonics`），在数据包离开客户端之前，为原始表面词元脉冲附加一个**归一化的密文伴随标签**：
1. **四语言 SDK 内置能力**：
   - **结构化日期 ISO 信标（`EnableStructuredDates`）**：零词表状态机识别 `YYYY/M/D`、`YYYY.MM.DD` 与 `YYYY年M月D日`，并为标准 `YYYY-MM-DD` 字符串发射伴随密文脉冲（`b = 284`，`f = FlagHasDigit | FlagHasConn`）。自 SDK `1.11.0` 起，同一状态机还会在 ISO 脉冲之前紧邻发射专用日历信标伴随脉冲 `H(K_user, "cal:YYYY-MM-DD")`（`b = 588`，`f` 相同）——一个供云端引擎做日历轴共振的不透明日历锚点。该能力纯属附加：上述 ISO 脉冲不变，且伴随脉冲仅在 `EnableStructuredDates` 开启时出现。
   - **ISO 3166 L0–L3 地理层级表（`LookupISO3166GeoHarmonics` / `lookup_iso3166_geo_harmonics` / `lookupISO3166GeoHarmonics`）**：将多语种国家、地区 / 省州与主要城市名映射为标准一阶（`@geo:cc:<iso2>`）与二阶（`@geo:cont:<cont>` / `@dim:geo`）标签。
   - **合并闭集谐波表（`LookupClosedSetHarmonics` / `lookup_closed_set_harmonics` / `lookupClosedSetHarmonics`）**：对合并后的闭集轴——地理、亲属、角色、日历、生物、物体、场所，以及带统计维度标签（`@unit:*` / `@dim:*`）的 SI/IEC/ISO 4217 单位——提供一次内置查表；可通过 `CombineHarmonicExpanders` / `combine_harmonic_expanders` / `combineHarmonicExpanders` 与自定义展开器组合，并经 `SetCanonicalHarmonics` 启用。
   - **自定义 `HarmonicExpander` 回调**：任何应用都可以把领域符号或本体术语映射为每个词元最多 2 个标准标签。
2. **Go 客户端伴随套件（`internal/langpack`）**：
   - **通用 SI / ISO 4217 / 统计核心（`langpack/common`）**：映射国际货币符号 / 代码（`$`、`€`、`£`、`¥`、`₩`、`₹`、`usd`、`eur`、`cny` $\to$ `@unit:curr:*`、`@dim:money`）、物理计量符号（`kg`、`km`、`kwh`、`psi`、`gb` $\to$ `@unit:*`、`@dim:*`）与统计记号（`%`、`p99`、`avg`、`max` $\to$ `@unit:stat:*`、`@dim:stat`）。
   - **多语种计量与形态包（`langpack/multi` 及各语言包）**：把多语种单位词（`kilograms` / `千克` / `公斤` / `キログラム` / `킬로그램` / `กิโลกรัม` $\to$ `@unit:mass:kg`、`@dim:mass`）与形态变体对齐到同一批标准标签。

### 4.2 客户端机制：附加密文归一化标签
谐波表**100% 在客户端**以纯密文标签叠加的方式工作：
1. **保持主密文脉冲不变**：对表面词元 $w$（例如 `"千克"` 或 `"kg"`），编码器先发射其原生主原子 `{H(w), b = B_even, f}`——原始表面词元永远不会被删除或替换。
2. **追加伴随密文脉冲**：当谐波表把 $w$ 匹配到标准标签 $t_1, t_2$（例如 `t_1 = "@unit:mass:kg"`、`t_2 = "@dim:mass"`）时，编码器用**完全相同的用户主密钥 $K_{\text{user}}$** 对这些标签做哈希，并在主原子之后紧邻追加最多两个伴随原子：
   - **一阶伴随（精确标准单位 / 国家）**：`{H(t_1), b = B_even - 1, f = FlagHasConn | FlagIsHarmonicCompanion}`
   - **二阶伴随（维度 / 大洲类别）**：`{H(t_2), b = B_even - 3, f = FlagHasConn | FlagIsHarmonicCompanion}`
3. **100% 密文脉冲一致（服务端不可逆、零明文）**：由于 `H(t_1)` 与 `H(t_2)` 在本地经 `HMAC-SHA256(K_user, tag)` 计算为 64 位整数脉冲（`{h, b, f}`），**谐波值本身就是密文脉冲，绝不是明文**。由于服务端不持有 $K_{\text{user}}$，它永远无法反推谐波哈希 `h`，也无法知道谐波内容代表什么（无法分辨伴随脉冲来自重量单位、货币、日期还是城市）。无论用户写 `"50 kg"` 还是 `"50 千克"`，两个数据包都保留各自不同的主哈希（`H("kg")` 与 `H("千克")`），同时共享完全相同的伴随哈希 `H("@unit:mass:kg")`——本质上**在不向服务端暴露词元或标签的前提下，为两个密文词元贴上了同一张共享的归一化标签**。

### 4.3 为什么它是纯增益附加项（不是作弊，且默认可选）
- **不开也能工作（零硬依赖）**：记忆引擎与四个 SDK 在 `SetCanonicalHarmonics` 关闭时（`EnableStructuredDates = false`、`Expander = nil`）开箱即用。没有谐波表，精确词元与子词召回照常工作；启用谐波表只是额外获得跨写法与跨语种的召回增益。
- **密文标签归一化，而非语料定制或作弊**：
  - **谐波值就是密文脉冲（服务端无法反推或检视）**：`"@unit:mass:kg"` 或 `"@geo:cc:jp"` 这类标准字符串只在编码期间的客户端内存中存在，**绝不出现在线路上**。在线路上与服务端，谐波值只是用用户密钥 $K_{\text{user}}$ 派生的一个不透明 64 位整数 `h`——服务端对谐波的含义或类别零知识。
  - **语料零改动**：用户本地存储中的原始文本与主词元脉冲永远不会被改写、伪造或过滤。
  - **仅用通用标准**：内置表只编码客观国际标准（ISO 8601 日期、ISO 4217 货币、SI/IEC 物理单位、ISO 3166 地理），绝不包含评测专用答案、用户实体或临时提示规则。
  - **严格附加的伴随标记（`FlagIsHarmonicCompanion`）**：每个谐波槽位都显式声明自己是伴随标签（`b = B_even - 1` / `B_even - 3`，`FlagIsHarmonicCompanion`），严格作为二级归一化索引，而不冒充原始文本。

---

## 5. 许可

Apache License 2.0 —— 见本目录 [LICENSE](LICENSE)。仓库根另行采用 PolyForm Noncommercial License 1.0.0（见 [../LICENSE](../LICENSE)）；第三方署名见 [../NOTICE](../NOTICE)。
