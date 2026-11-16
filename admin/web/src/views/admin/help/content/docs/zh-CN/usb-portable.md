# U 盘便携模式指南（USB Portable Mode）

**[简体中文](usb-portable.md) · [English](../usb-portable.md)**

> 本指南说明 Rolling Memory 的 U 盘可移动介质架构、一键制盘命令、跨机器自愈锚点（Host Anchor）、`usb` 强同步存储档位与 `rmem eject` 安全拔盘规程。

## 1. 核心结论与适用前提（先说句实在话）

> **关于 U 盘便携模式的坦诚说明**：
> 因为本工具的程序和 SQLite 记忆库都是独立的本地文件，我们顺手预置了这个 U 盘随身模式。但在使用前有两个实际情况请先了解：
> 1. **尚未经过全面实测**：我们目前还没抽出足够时间在各种不同机器和 U 盘介质上对这个模式做充分测试；
> 2. **有个很现实的前提——你得在不同电脑上跑同一个项目**：工程记忆里存的主要是特定代码库的文件路径、函数名和排障记录。如果你换了一台电脑，但那台电脑上并没有开发同一个项目，那带过去的旧项目坐标其实也派不上什么用场；只有当你确实在两台电脑（比如公司电脑和家里电脑）上接着干同一个项目时，随身带着走才有实际意义。
> 
> 它到底算不算个伪命题、在哪些跨机场景下真能帮上忙，欢迎感兴趣且有动手能力的朋友一起尝试和探讨！

**请勿直接将源码仓库复制到 U 盘使用**——源码仓库不包含编译后的可执行二进制，且在未激活便携标记时默认将数据写回宿主 `$HOME`。正确的做法是使用 `rmem init --portable <U盘目录>` 一键生成自包含便携载荷（如需从源码仓构建一盘通吃的全平台 U 盘，见第 4 节方式 B），实现「记忆随盘携带、换机一次接线、拔盘前 `rmem eject` 零坏库」。

---

## 2. 为什么不能直接复制源码目录？

直接将开发源码目录复制到 U 盘在跨机器使用时面临三项硬约束：

1. **缺少自包含可执行二进制**：源码仓库的 `bin/` 目录下仅存放轻量级启动器脚本，未内置编译好的二进制文件；若目标机器未安装 Go 工具链，将无法现场编译运行。
2. **默认路径指向宿主 `$HOME` 而非 U 盘**：在常规宿主模式下，配置文件、数据库与日志分别位于 `~/.config/rolling-memory/`、`~/.local/share/rolling-memory/memory.db` 与 `~/.local/state/rolling-memory/`。未触发便携标记时，即使在 U 盘目录内执行命令，记忆依然会写入当前电脑的本地磁盘，拔盘无法带走。
3. **盘符与挂载点漂移**：U 盘在不同机器或不同插口下可能从 `/Volumes/USB` 变成 `/Volumes/USB 1`（macOS）、`/media/$USER/USB`（Linux），或从 `E:\` 变成 `F:\`（Windows）。若宿主 MCP/Hooks 配置写死单次挂载的绝对路径，更换插口或机器后即会断连。

---

## 3. 架构设计：便携载荷（USB Payload）× 宿主自愈锚点（Host Anchor）

Rolling Memory 采用「**盘上全自包含载荷 + 宿主侧薄锚点自愈**」两层架构：

```text
Coding Agent 宿主（ZCode / Claude Code / Codex / Cursor / Windsurf / Cline / dsh / CodeBuddy）
        │  MCP stdio · Hooks · CLI
        ▼
Host Anchor（每台电脑 ~/.local/share/rolling-memory/bin/rolling-memory.sh 或 .cmd）
        │  1) 原挂载点在位 → 直接 exec 盘上二进制
        │  2) 挂载点/盘符漂移 → 自动扫描 /Volumes · /media · /run/media · /mnt · D:..Z: 寻回 .portable
        ▼
USB Payload（U 盘便携目录，整体随盘移动）
  ├── .portable                       ← 便携模式硬件级触发标记
  ├── bin/rolling-memory[-os-arch]    ← 零依赖静态单二进制（CGO_ENABLED=0）
  ├── skills/rolling-memory/SKILL.md  ← 随盘携带的宿主技能定义
  ├── config/config.toml              ← 相对路径配置（db = "data/memory.db"，profile = "usb"）
  ├── data/memory.db                  ← 本机明文金库（WAL + synchronous=FULL）
  ├── state/rolling-memory.log        ← 运行态与审计日志
  ├── setup.sh / setup.command / setup.cmd   ← 换机一键挂载脚本
  └── eject.sh / eject.command / eject.cmd   ← 拔盘前安全刷盘脚本
```

### 3.1 便携根目录（`PortableRoot`）判定规则

引擎在启动时按以下优先级自动判定是否进入便携模式（见 [`internal/config/config.go`](https://github.com/rocareer/rolling-memory/blob/main/internal/config/config.go)）：

1. **环境变量显式指定**：若设置了 `ROLLING_MEMORY_PORTABLE=<目录>`，直接以该目录作为便携根目录；若设为 `1` 或 `true`，则自动解析当前可执行文件对应的载荷根目录。
2. **`.portable` 标记文件自动探测（含符号链接穿透）**：引擎解析当前可执行文件的真实路径（自动穿透 `symlink` 符号链接）：
   - **标准分层布局（推荐）**：当二进制位于 `<root>/bin/rolling-memory` 且父目录存在 `<root>/.portable` 时，自动将 `<root>` 识别为便携根目录，`config/`、`data/`、`state/` 均位于 `<root>/` 顶层；
   - **极简同级布局**：当二进制与 `.portable` 位于同一目录 `<dir>/` 时，以 `<dir>` 作为便携根目录。
3. **相对路径锚定与防回落保护**：进入便携模式后，配置文件中的相对路径（如 `db = "data/memory.db"`、`log = "state/rolling-memory.log"`）恒定相对于 `PortableRoot` 解析，不受当前终端工作目录（`CWD`）影响；即使配置文件保留了默认模板字面量，也会自动重定向至 U 盘目录。

---

## 4. 三步上手：制作、换机接入与安全拔盘

### 第一步：一键制作 U 盘便携载荷

将 U 盘插入电脑（建议格式化为 **exFAT** 或 **APFS / ext4**，以下假设挂载点为 `/Volumes/USB`），可根据使用场景选择以下任一方式：

#### 方式 A：单平台快速制盘（毫秒级，无需编译环境）

若当前电脑已安装 `rolling-memory`（或 `rmem`）二进制，直接执行：

```bash
rmem init --portable /Volumes/USB/rolling-memory
```

该命令会在 `/Volumes/USB/rolling-memory` 下自动完成全部装配：写入 `.portable` 标记、复制当前二进制至 `bin/`、复制 `SKILL.md` 至 `skills/`、生成相对路径 `config/config.toml`（自动开启 `storage.profile = "usb"`）、初始化 `data/memory.db` 并生成 `setup.*` 与 `eject.*` 辅助脚本。

#### 方式 B：全平台通用制盘（macOS + Linux + Windows 一盘通吃）

若需要在 macOS（Apple Silicon / Intel）、Linux（x86_64 / ARM64）和 Windows（x64）多台不同操作系统的电脑间共用同一块 U 盘，在源码仓库根目录下交叉编译五个纯 Go 二进制（此方式需要本机安装 Go 工具链）：

```bash
USB=/Volumes/USB/rolling-memory
mkdir -p "$USB/bin"
for t in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64; do
  os=${t%/*}; arch=${t#*/}
  ext=""; [ "$os" = "windows" ] && ext=".exe"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -o "$USB/bin/rolling-memory-$os-$arch$ext" ./cmd/rolling-memory
done
CGO_ENABLED=0 go build -trimpath -o "$USB/bin/rolling-memory" ./cmd/rolling-memory
"$USB/bin/rolling-memory" init --portable "$USB"
```

该流程会通过 `CGO_ENABLED=0` 交叉编译全部 5 个平台的零依赖二进制并放入 `/Volumes/USB/rolling-memory/bin/`；最后两条命令放入宿主平台二进制并由其完成载荷装配，生成的 `setup` 与 `eject` 脚本会根据目标机的操作系统和 CPU 架构自动选择对应的二进制文件。

---

### 第二步：在任意电脑上一键接入（每台机器仅需一次）

将 U 盘插入目标电脑后，只需执行一次接入脚本即可将宿主 Agent 挂载至盘上记忆库：

- **macOS**：双击 U 盘目录内的 `setup.command`，或在终端执行：
  ```bash
  sh /Volumes/USB/rolling-memory/setup.sh
  ```
- **Linux**：
  ```bash
  sh /media/$USER/USB/rolling-memory/setup.sh
  ```
- **Windows**：双击 `setup.cmd`，或在命令提示符 / PowerShell 中运行：
  ```cmd
  E:\rolling-memory\setup.cmd
  ```
- **指定特定宿主（可选）**：所有附加参数均会透传给 `install` 子命令，例如仅挂载 ZCode 与 Claude Code：
  ```bash
  sh /Volumes/USB/rolling-memory/setup.sh zcode claude-code --force
  ```

> **挂载点漂移自愈**：从便携载荷运行 `install` 时，写入宿主 `~/.local/share/rolling-memory/bin/rolling-memory.sh`（Windows 为 `%LOCALAPPDATA%\rolling-memory\bin\rolling-memory.cmd`）的是**便携自愈锚点（Portable Host Anchor）**。后续即使 U 盘在该电脑上的挂载路径变为 `/Volumes/USB 1` 或盘符从 `E:` 变为 `F:`，宿主锚点也会自动在 `/Volumes`、`/media`、`/run/media`、`/mnt` 及 `D:..Z:` 中寻回 `.portable` 载荷，无需重新执行 `install`。

接入完成后，可执行以下命令验证静态注册与实时 MCP 握手：

```bash
/Volumes/USB/rolling-memory/bin/rolling-memory install --check
```

---

### 第三步：日常使用与安全拔盘（`rmem eject`）

日常在 IDE 或终端中使用 Coding Agent 时，MCP `recall` 工具负责检索，Hooks 负责自动写入，读写 U 盘上的 `data/memory.db`。你也可以随时从 U 盘直接查看当前记忆库状态与健康度：

```bash
/Volumes/USB/rolling-memory/bin/rolling-memory status
```

**准备拔下 U 盘前**，请先关闭正在使用该记忆库的 IDE 会话，然后运行安全弹出命令（或双击盘上的 `eject.command` / `eject.cmd`）：

```bash
/Volumes/USB/rolling-memory/bin/rolling-memory eject
```

确认输出 `"ok": true`、`checkpoint: TRUNCATE` 且 `integrity.ok: true` 后，再通过操作系统的「安全弹出 / 卸载卷」拔下 U 盘。

---

## 5. `usb` 存储档位与拔盘防损机制

可移动介质（尤其是 exFAT 文件系统）不提供元数据日志，且存在意外断电或拔出风险。当处于便携模式（或显式配置 `storage.profile = "usb"`）时，存储引擎会自动从常规 `local` 档位切换至 `usb` 强同步档位（见 [`internal/db/db.go`](https://github.com/rocareer/rolling-memory/blob/main/internal/db/db.go)）：

| SQLite 参数 / 行为 | `local` 档位（本机 SSD） | `usb` 档位（U 盘便携模式） | 工程目的 |
|---|---|---|---|
| `PRAGMA journal_mode` | `WAL` | `WAL`（不支持时自动退回 `DELETE`） | 保证读写原子性与崩溃恢复能力 |
| `PRAGMA synchronous` | `NORMAL` | **`FULL`** | 每次事务提交强制 `fsync` 落盘，防止拔盘丢失已提交帧 |
| `PRAGMA busy_timeout` | `5000` ms | **`15000` ms** | 容忍 USB 闪存控制器的偶发写停顿与高延迟 |
| `PRAGMA wal_autocheckpoint` | `1000` 页 | **`256` 页（约 1 MB）** | 高频自动将 WAL 合入主库，缩短未合入日志窗口 |
| `PRAGMA mmap_size` | 默认 | **`0`（强制禁用）** | 严禁对可移动介质做内存映射，防止热拔出引发进程 `SIGBUS` 崩溃 |
| `PRAGMA cache_size` | 默认 | `-8192`（8 MiB） | 减少对慢速闪存介质的重复随机读取 |
| `Store.Close()` / CLI 命令退出 | 常规关闭 | **自动执行 `PRAGMA wal_checkpoint(TRUNCATE)`** | 每次命令或会话结束即刻将 WAL 截断归零 |
| `rmem eject` | 按需调用 | **`TRUNCATE` 刷盘 + 全表 `Check()` 完整性校验** | 拔盘前确认 `busy=0`、WAL 归零且逐字锚与图结构 100% 完好 |

---

## 6. 安全与使用注意事项

1. **物理丢失与加密建议**：SQLite 数据库文件（`data/memory.db`）本身为明文存储。鉴于 U 盘随身携带存在遗失风险，强烈建议使用**硬件加密 U 盘**、**macOS APFS 加密卷**、**Linux LUKS** 或 **VeraCrypt 加密容器**存放便携目录。
2. **API 密钥永不落盘**：API Key 只从当前宿主环境变量或宿主 `0600` 权限密钥文件读取，绝不写入 U 盘的 `config.toml`、`memory.db` 或日志文件中。
3. **单机独占挂载**：SQLite 的文件锁不支持两台物理机器同时挂载同一块存储介质并发写入（如通过网络共享或双头 USB 同时挂载）；同一时刻请仅在一台机器上挂载使用。
4. **避免在便携模式下混用 `--project`**：`install --project` 会将 `ROLLING_MEMORY_DB` 固定为当前项目目录内的 `.rolling-memory/memory.db`，从而绕开 U 盘主库；在 U 盘模式下请直接使用默认的全局便携安装（`setup.sh` 或 `rmem install --with-hooks`）。

---

## 7. 常见问题排查（Troubleshooting）

- **问：换了一台电脑插上 U 盘后，为什么 Agent 提示 `portable USB drive not mounted`？**
  - **答**：请先确认是否为首次在该电脑上使用——新电脑需运行一次盘上的 `setup.sh` / `setup.command` / `setup.cmd` 以写入宿主锚点；若已安装过锚点，请检查 U 盘是否已挂载成功以及载荷根目录下是否存在 `.portable` 标记文件。
- **问：在 macOS 上双击 `setup.command` 提示「无法打开，因为无法验证开发者」？**
  - **答**：这是 macOS Gatekeeper 对外置可执行脚本的默认拦截。可在终端执行 `xattr -dr com.apple.quarantine /Volumes/USB/rolling-memory` 清除隔离属性，或直接在终端运行 `sh /Volumes/USB/rolling-memory/setup.sh`。
- **问：执行 `rmem eject` 时返回 `"busy": 1`？**
  - **答**：说明仍有某个 IDE 窗口或后台 MCP 进程持有数据库连接。请关闭对应的 IDE 会话后再执行一次 `rmem eject`，确认返回 `"busy": 0, "ok": true` 后再拔盘。
