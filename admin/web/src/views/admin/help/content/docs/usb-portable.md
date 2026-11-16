# USB Portable Mode Guide

**[English](usb-portable.md) · [简体中文](zh-CN/usb-portable.md)**

> This guide explains Rolling Memory's removable-media architecture, one-command USB payload assembly, mount-drift-healing Host Anchor, the `usb` synchronous storage profile, and the `rmem eject` safe unmounting workflow.

## 1. Core Summary & Practical Premise (Upfront Note)

> **An honest note on USB portable mode**:
> Because both the binary and the SQLite vault are self-contained local files, we included this USB portable mode. Before using it, please keep two practical realities in mind:
> 1. **Not yet battle-tested across all setups**: We have not had the time to test this mode thoroughly across diverse hardware and removable drives;
> 2. **A practical prerequisite — you need to be working on the same project across machines**: Coding memories mostly store repo-specific file paths, symbols, and debugging notes. If you move to a second computer where you work on a completely different project, coordinates from the first project won't help much; carrying a vault on a thumb drive only makes sense when you work on the same codebase across multiple machines (e.g., office and home desktops).
> 
> Whether portable agent memory is genuinely useful or mostly a false premise is an open question — if you are curious and enjoy tinkering, feel free to try it out and discuss it with us!

**Do not simply copy the source repository onto a USB drive.** A source checkout does not contain a compiled binary and defaults to writing data into the host machine's `$HOME` directory. Instead, use `rmem init --portable <USB_DIR>` to assemble a self-contained portable payload (for a multi-platform drive assembled from a source checkout, see Option B in section 4) — carrying your graph memory on the drive, wiring each new computer once, and running `rmem eject` before unplugging with zero database corruption.

---

## 2. Why Copying the Source Directory Does Not Work

Copying the development source repository directly onto a USB drive fails across machines for three structural reasons:

1. **No Self-Contained Binary**: The repository's `bin/` directory only holds lightweight POSIX/Windows wrapper scripts rather than a compiled binary. If a target machine does not have a Go toolchain installed, it cannot build the engine on the fly.
2. **Default Paths Point to Host `$HOME`**: In standard host mode, configuration, database, and log files resolve to `~/.config/rolling-memory/`, `~/.local/share/rolling-memory/memory.db`, and `~/.local/state/rolling-memory/`. Without an active portable marker, running commands from a USB directory still writes memories to the local computer's disk.
3. **Drive-Letter & Mount-Point Drift**: Removable drives frequently change mount paths across machines or USB ports — e.g., `/Volumes/USB` to `/Volumes/USB 1` on macOS, `/media/$USER/USB` on Linux, or `E:\` to `F:\` on Windows. Hardcoding a single mount path into host MCP/Hooks configurations breaks as soon as the mount point shifts.

---

## 3. Architecture: Self-Contained USB Payload × Mount-Drift-Healing Host Anchor

Rolling Memory solves removable-media portability with a two-layer architecture:

```text
Coding Agent Harness (ZCode / Claude Code / Codex / Cursor / Windsurf / Cline / dsh / CodeBuddy)
        │  MCP stdio · Hooks · CLI
        ▼
Host Anchor (~/.local/share/rolling-memory/bin/rolling-memory.sh or .cmd on each machine)
        │  1) Original mount path present → direct exec
        │  2) Mount-point / drive-letter drift → scans /Volumes · /media · /run/media · /mnt · D:..Z: for .portable
        ▼
USB Payload (Portable directory on the USB drive, moves with the drive)
  ├── .portable                       ← Hardware-level portable mode trigger marker
  ├── bin/rolling-memory[-os-arch]    ← Zero-dependency static binary (CGO_ENABLED=0)
  ├── skills/rolling-memory/SKILL.md  ← Bundled agent skill definition
  ├── config/config.toml              ← Relative-path config (db = "data/memory.db", profile = "usb")
  ├── data/memory.db                  ← Local plaintext vault (WAL + synchronous=FULL)
  ├── state/rolling-memory.log        ← Runtime and audit log
  ├── setup.sh / setup.command / setup.cmd   ← One-step host wiring scripts
  └── eject.sh / eject.command / eject.cmd   ← Pre-unmount WAL flush & integrity check scripts
```

### 3.1 `PortableRoot` Resolution Rules

On startup, the engine determines whether portable mode is active in the following order (see [`internal/config/config.go`](https://github.com/rocareer/rolling-memory/blob/main/internal/config/config.go)):

1. **Explicit Environment Variable**: If `ROLLING_MEMORY_PORTABLE=<DIR>` is set, `<DIR>` is used directly as the portable root; if set to `1` or `true`, the payload root of the running executable is used.
2. **`.portable` Marker Auto-Discovery (with Symlink Resolution)**: The engine resolves the real executable path (automatically following symlinks via `EvalSymlinks`):
   - **Standard Bundle Layout (Recommended)**: When the binary resides at `<root>/bin/rolling-memory` and `<root>/.portable` exists in the parent directory, `<root>` is selected as the portable root so `config/`, `data/`, and `state/` live cleanly under `<root>/`.
   - **Flat Sibling Layout**: When the binary and `.portable` reside in the same directory `<dir>/`, `<dir>` is used as the portable root.
3. **Relative Path Anchoring & Host-Fallback Protection**: When portable mode is active, relative paths in `config.toml` (such as `db = "data/memory.db"` and `log = "state/rolling-memory.log"`) always resolve against `PortableRoot` regardless of the current working directory (`CWD`). Even if a default template path is present, it is automatically redirected to the USB payload.

---

## 4. Three-Step Workflow: Assemble, Wire, and Safely Eject

### Step 1: Assemble the USB Portable Payload

Insert your USB drive (formatted as **exFAT** or **APFS / ext4**; mounted at `/Volumes/USB` in the examples below) and choose either assembly method:

#### Option A: Single-Platform Fast Assembly (Milliseconds, no compiler required)

If you already have the `rolling-memory` (or `rmem`) binary on your current machine, run:

```bash
rmem init --portable /Volumes/USB/rolling-memory
```

This command assembles the complete payload under `/Volumes/USB/rolling-memory`: writes `.portable`, copies the running binary into `bin/`, copies `SKILL.md` into `skills/`, writes a relative-path `config/config.toml` (`storage.profile = "usb"`), initializes `data/memory.db`, and generates `setup.*` and `eject.*` helper scripts.

#### Option B: Multi-Platform Universal USB Drive (macOS + Linux + Windows on One Drive)

To share the same USB drive across macOS (Apple Silicon / Intel), Linux (x86_64 / ARM64), and Windows (x64) machines, cross-compile the five pure-Go binaries from the repository root (this path requires a Go toolchain):

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

The loop cross-compiles all five zero-dependency binaries (`CGO_ENABLED=0`) into `/Volumes/USB/rolling-memory/bin/`; the last two commands place the host build and let it assemble the payload, generating OS/architecture-aware `setup` and `eject` scripts.

---

### Step 2: One-Step Wiring on Any Computer (Once per Machine)

When you plug the USB drive into a computer for the first time, run the setup script once to wire your Coding Agent harnesses:

- **macOS**: Double-click `setup.command` in Finder, or run in Terminal:
  ```bash
  sh /Volumes/USB/rolling-memory/setup.sh
  ```
- **Linux**:
  ```bash
  sh /media/$USER/USB/rolling-memory/setup.sh
  ```
- **Windows**: Double-click `setup.cmd`, or run in Command Prompt / PowerShell:
  ```cmd
  E:\rolling-memory\setup.cmd
  ```
- **Target Specific Harnesses (Optional)**: Any extra flags are forwarded to `install`:
  ```bash
  sh /Volumes/USB/rolling-memory/setup.sh zcode claude-code --force
  ```

> **Automatic Mount-Point Drift Healing**: Running `install` from a portable payload writes a **Portable Host Anchor** to `~/.local/share/rolling-memory/bin/rolling-memory.sh` (`%LOCALAPPDATA%\rolling-memory\bin\rolling-memory.cmd` on Windows). If the USB drive later mounts as `/Volumes/USB 1` or shifts from `E:` to `F:`, the Host Anchor automatically scans `/Volumes`, `/media`, `/run/media`, `/mnt`, and `D:..Z:` to locate your `.portable` payload without re-running `install`.

Verify the registration and live MCP handshake at any time:

```bash
/Volumes/USB/rolling-memory/bin/rolling-memory install --check
```

---

### Step 3: Daily Use & Safe Eject (`rmem eject`)

During normal coding sessions, the MCP `recall` tool retrieves and lifecycle hooks write `/Volumes/USB/rolling-memory/data/memory.db` automatically. You can also inspect the portable memory store status and health directly from the drive at any time:

```bash
/Volumes/USB/rolling-memory/bin/rolling-memory status
```

**Before unplugging the USB drive**, close active IDE sessions and run the safe eject command (or double-click `eject.command` / `eject.cmd`):

```bash
/Volumes/USB/rolling-memory/bin/rolling-memory eject
```

Once it reports `"ok": true`, `checkpoint: TRUNCATE`, and `integrity.ok: true`, unmount the volume through your operating system and remove the drive.

---

## 5. The `usb` Storage Profile & Corruption Protection

Removable flash media (especially exFAT volumes) lack metadata journaling and are exposed to sudden removal. When portable mode is active (or `storage.profile = "usb"` is set), the database engine automatically switches from the `local` profile to the hardened `usb` profile (see [`internal/db/db.go`](https://github.com/rocareer/rolling-memory/blob/main/internal/db/db.go)):

| SQLite Pragma / Behavior | `local` Profile (Host SSD) | `usb` Profile (Portable USB) | Engineering Purpose |
|---|---|---|---|
| `PRAGMA journal_mode` | `WAL` | `WAL` (falls back to `DELETE` if unsupported) | Atomic commits and crash recovery |
| `PRAGMA synchronous` | `NORMAL` | **`FULL`** | Forces `fsync` on every transaction commit so committed frames survive power loss |
| `PRAGMA busy_timeout` | `5000` ms | **`15000` ms** | Absorbs USB flash controller write stalls and latency spikes |
| `PRAGMA wal_autocheckpoint` | `1000` pages | **`256` pages (~1 MB)** | Frequently folds WAL frames back into the main database file |
| `PRAGMA mmap_size` | Default | **`0` (Disabled)** | Prevents `SIGBUS` process crashes if removable media is unplugged mid-read |
| `PRAGMA cache_size` | Default | `-8192` (8 MiB) | Reduces repeated random reads over slower USB buses |
| `Store.Close()` / CLI command exit | Standard close | **Runs `PRAGMA wal_checkpoint(TRUNCATE)`** | Truncates the WAL file to zero bytes at the end of every command or session |
| `rmem eject` | On-demand | **`TRUNCATE` checkpoint + full `Check()` gate** | Confirms `busy=0`, zero pending WAL frames, and 100% verbatim anchor integrity before removal |

---

## 6. Security & Operational Best Practices

1. **Physical Loss & Volume Encryption**: The SQLite database (`data/memory.db`) is unencrypted on disk. Because USB drives can be lost, strongly consider using a **hardware-encrypted USB drive**, **macOS APFS Encrypted volume**, **Linux LUKS**, or a **VeraCrypt container**.
2. **API Keys Never Touch the USB Drive**: API keys are read exclusively from host environment variables or host `0600` secret files — never written into the USB drive's `config.toml`, `memory.db`, or logs.
3. **Single-Host Exclusive Mounting**: SQLite file locking does not support concurrent writes from two physical machines mounting the same storage volume simultaneously (e.g., over a network share). Mount and use the drive on one machine at a time.
4. **Do Not Combine with `--project`**: Running `install --project` pins `ROLLING_MEMORY_DB` to `<cwd>/.rolling-memory/memory.db` on the host disk, bypassing the USB database. Always use the default global portable wiring (`setup.sh` or `rmem install --with-hooks`).

---

## 7. Troubleshooting

- **Q: After plugging the USB drive into a different computer, why does the agent report `portable USB drive not mounted`?**
  - **A**: Ensure you have run `setup.sh` / `setup.command` / `setup.cmd` at least once on that computer to install the Host Anchor. If already installed, verify that the USB volume is mounted and that `.portable` exists in the payload root.
- **Q: macOS blocks `setup.command` with an "unidentified developer" prompt.**
  - **A**: Clear the quarantine attribute via `xattr -dr com.apple.quarantine /Volumes/USB/rolling-memory` or run `sh /Volumes/USB/rolling-memory/setup.sh` directly in Terminal.
- **Q: `rmem eject` returns `"busy": 1`.**
  - **A**: Another IDE window or MCP server process still holds an open connection to `memory.db`. Close the remaining IDE session and re-run `rmem eject` until it reports `"busy": 0, "ok": true`.
