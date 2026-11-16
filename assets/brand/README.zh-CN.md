# Rolling-Memory 品牌标识系统

**[简体中文](README.zh-CN.md) · [English](README.md)**

字标以三个大写 **R** 水平叠加——**Remember · Rolling · Recall**——左重右轻。

几何规范：首字（L0）字怀与腿下开口 100% 净空 + `4.5u` 硬边光学切槽 + `5.2u` 左柱锚定刻槽 +
后两层疏密百叶光栅。

四态标准形态：**① 阳（白底深墨） · ② 阴（黑底纯白） · ③ 彩虹阳（白底高浓 R7 极光） ·
④ 彩虹阴（黑底高亮 R7 极光）**。

## 一、三套字标方案

| 方案编号 | 字标家族 | 核心几何特征 | 适用定位 |
| --- | --- | --- | --- |
| **`F03`** | **Open WebUI · Archivo Black（曲膝刻槽百叶）** | `23u` 左柱 + `5.2u` 垂直刻槽 + 右腿带弧形「曲膝」垂直落地，三层叠开形成三道平行 S 弧韵律 | 现代 AI 平台 / 对话与插件主界面 |
| **`P03`** | **Prism 45° · 棱镜切面体（八角刻槽百叶）** | `24u` 左柱 + `5.2u` 垂直刻槽 + `45°` 光学倒角八角字碗 + `28°` 直切斜腿 | 硬核记忆引擎 / 底层基础设施与技术文档 |
| **`R07`** | **Grotesk Stencil · 01 工业几何（圆碗刻槽百叶）** | `24u` 左柱 + `5.2u` 垂直刻槽 + `R30/R13` 光学偏心内环 + `28°` 直切斜腿 | 通用工程标识 / 全尺寸基准标 |

## 二、四态配色

### ① 阳（纸白底深墨）

- **L0 Remember**：`#0F1014`（100% 实心曜石墨）
- **L1 Rolling**：`#2C2E38`（68% 占空比密百叶深钛灰）
- **L2 Recall**：`#686C7E`（40% 占空比疏百叶中钛灰）
- **标准底色**：`#F5F5F7`

### ② 阴（曜石底纯白）

- **L0 Remember**：`#F5F5F7`（100% 实心纸白）
- **L1 Rolling**：`#BFC2D0`（68% 占空比密百叶亮银灰）
- **L2 Recall**：`#6E7285`（40% 占空比疏百叶暗银灰）
- **标准底色**：`#0C0D11`

### ③ 彩虹阳（纸白底 R7 高浓极光）

- **L0 Remember（实心热态锚）**：`#E67E00 (0%) → #D9381E (48%) → #C81E5A (100%)`（熔岩琥珀 $\to$ 朱砂深红）
- **L1 Rolling（密百叶）**：`#D81B60 (0%) → #8E24AA (52%) → #4527A0 (100%)`（胭脂红 $\to$ 皇家紫 $\to$ 深靛紫）
- **L2 Recall（疏百叶）**：`#4527A0 (0%) → #0066DF (52%) → #003BB5 (100%)`（深靛紫 $\to$ 钴蓝 $\to$ 深海蓝）
- **标准底色**：`#F7F7FA`

### ④ 彩虹阴（曜石底 R7 高亮极光）

- **L0 Remember（实心热态锚）**：`#FFD60A (0%) → #FF9F0A (45%) → #FF6482 (100%)`（炽热金橙 $\to$ 珊瑚粉红）
- **L1 Rolling（密百叶）**：`#FF6482 (0%) → #BF5AF2 (55%) → #5E5CE6 (100%)`（珊瑚粉 $\to$ 极光紫 $\to$ 靛紫）
- **L2 Recall（疏百叶）**：`#5E5CE6 (0%) → #0A84FF (55%) → #0040DD (100%)`（靛紫 $\to$ 电光蓝 $\to$ 深海蓝）
- **标准底色**：`#060609`

## 三、SVG 母版清单（`svg/`）

### `F03 · Archivo Black`（4 枚）

- [`svg/f03-archivo-yang.svg`](svg/f03-archivo-yang.svg) — ① 阳
- [`svg/f03-archivo-yin.svg`](svg/f03-archivo-yin.svg) — ② 阴
- [`svg/f03-archivo-rainbow-yang.svg`](svg/f03-archivo-rainbow-yang.svg) — ③ 彩虹阳
- [`svg/f03-archivo-rainbow-yin.svg`](svg/f03-archivo-rainbow-yin.svg) — ④ 彩虹阴

### `P03 · Prism 45°`（4 枚）

- [`svg/p03-prism-yang.svg`](svg/p03-prism-yang.svg) — ① 阳
- [`svg/p03-prism-yin.svg`](svg/p03-prism-yin.svg) — ② 阴
- [`svg/p03-prism-rainbow-yang.svg`](svg/p03-prism-rainbow-yang.svg) — ③ 彩虹阳
- [`svg/p03-prism-rainbow-yin.svg`](svg/p03-prism-rainbow-yin.svg) — ④ 彩虹阴

### `R07 · Grotesk Stencil`（4 枚）

- [`svg/r07-grotesk-yang.svg`](svg/r07-grotesk-yang.svg) — ① 阳
- [`svg/r07-grotesk-yin.svg`](svg/r07-grotesk-yin.svg) — ② 阴
- [`svg/r07-grotesk-rainbow-yang.svg`](svg/r07-grotesk-rainbow-yang.svg) — ③ 彩虹阳
- [`svg/r07-grotesk-rainbow-yin.svg`](svg/r07-grotesk-rainbow-yin.svg) — ④ 彩虹阴

十二枚母版的独立预览页见 [`rolling-memory-brand-r7-final.html`](rolling-memory-brand-r7-final.html)。
