# Multi-Harness Integration Guide

**[English](harness.md) · [简体中文](zh-CN/harness.md)**

> This document describes the multi-harness architecture, registration matrix, launcher shims, and verification gates of `rolling-memory install`.

## 1. Design Goal

Rolling Memory's mechanical core is completely harness-agnostic. Coding agent harnesses differ in only four integration surfaces: **Skill Root**, **MCP Server Registration File**, **Lifecycle Hooks**, and **Instruction Files (`AGENTS.md` / `CLAUDE.md`)**.

`rolling-memory install` consolidates these four surfaces across all major harnesses into a single registry — backing up existing files, applying idempotent updates, and verifying live MCP handshakes via `install --check`.

---

## 2. Integration Approaches Compared

| Approach | Typical Implementation | Operational Trade-Offs |
|---|---|---|
| **MCP-Only + Manual Config Copy-Paste** | Ships a stdio MCP server; users manually edit each client's JSON or TOML file | Users shoulder path differences, escaping rules, and cropped GUI `PATH` issues; no backups or conflict protection |
| **Single-Harness Exclusive Plugin** | Hard-codes one IDE's extension API and proprietary lifecycle hooks | Memories and workflows are siloed within one tool and cannot be shared across different coding agents |
| **Unified `install` Registry + Native Plugin Deduplication (Rolling Memory)** | Universal stdio MCP (single `recall` tool) + `install` multi-harness writers + native plugin deduplication + `install --check` live handshake verification | One command wires all harnesses on the machine over the same local SQLite graph, with built-in self-verification |

---

## 3. Layered Contract

```text
Harness Agent + Skill        ← Semantic layer: follows SKILL.md & instructions to call recall (writes are captured by hooks)
        │  MCP tools/call
rolling-memory install       ← Harness adapter: registry + conservative writers (JSON / TOML / YAML) + check
        │  writes launcher shim & registrations
MCP stdio · CLI · Hooks      ← Mechanical core: zero harness coupling, standard process protocols
        │
SQLite (WAL + FTS5)          ← Storage layer: local plaintext vault
```

- **Zero Harness Coupling in the Core (`api` / `mcp` / `cli` / `hooks`)**: Communicates strictly over standard process pipes (newline-delimited JSON-RPC 2.0 for MCP, argv/stdin for CLI, JSON stdin → JSON stdout for Hooks).
- **Conservative Wiring in the Adapter (`internal/harness`)**: Writes launcher commands into harness configs, copies `SKILL.md`, and optionally attaches hooks and instruction blocks without touching memory semantics.
- **Reminder-Only Hooks, Agent-Driven Retrieval**: The optional per-prompt hook (`--with-hooks`) never retrieves memory automatically — `recall` is always the agent's own call. The hook only keeps the local capture side fresh (conversation turns and mounted background folders) and, every few turns, reminds the agent that `recall` exists.
- **Turn-End Recall Review & Customizing `SKILL.md`**: At the very bottom of `SKILL.md` (`§5 Wrap-up Review`), a standalone optional section instructs the agent to append a brief wrap-up note whenever `recall` was called in that turn — reporting how many times `recall` was called, estimated round-trips saved with concrete basis, and whether the recalled memory actually helped. You can edit or delete that bottom section in `<skill_root>/rolling-memory/SKILL.md` directly (or pass `--skill-file <path>` to `rolling-memory install`) at any time.

---

## 4. Supported Harness Matrix

### 4.1 Default Auto-Detected Harnesses (5 Targets)

| Harness | User Skill Root | MCP Registration Location | Lifecycle Hooks | Instruction Files (User / Project) |
|---|---|---|---|---|
| **ZCode** | `~/.zcode/skills/` | `~/.zcode/cli/config.json` → `mcp.servers` | ✅ `hooks.events` (`hooks.enabled=true`) | `~/.zcode/AGENTS.md` / `AGENTS.md` |
| **Claude Code** | `~/.claude/skills/` | `~/.claude.json` → `mcpServers` (project `.mcp.json`) | ✅ `~/.claude/settings.json` → `hooks` | `~/.claude/CLAUDE.md` / `CLAUDE.md` |
| **Codex** | `~/.codex/skills/` | `~/.codex/config.toml` → `[mcp_servers.<name>]` | — (Skill-driven) | `~/.codex/AGENTS.md` / `AGENTS.md` |
| **DeepSeek Harness (`dsh`)** | `~/.dsh/skills/` (`$DSH_HOME`) | `~/.dsh/profiles/<profile>/cordis.patch.yml` → `dsh-mcp-client` | — (Skill-driven) | `~/.dsh/AGENTS.md` / `AGENTS.md` |
| **CodeBuddy** | `~/.codebuddy/skills/` | `~/.codebuddy/mcp.json` → `mcpServers` | — (Skill-driven) | — / `CODEBUDDY.md` |

### 4.2 Extended Harnesses (5 Targets) & Native Plugin Deduplication

- **Extended Targets**: Explicitly pass the harness ID (`rolling-memory install <id>`) to wire **Cursor** (`cursor` → `~/.cursor/mcp.json`), **Windsurf** (`windsurf` → `~/.codeium/windsurf/mcp_config.json`), **Cline / Roo Code** (`cline` → `~/.cline/mcp_settings.json`), **WorkBuddy** (`workbuddy` → `~/.workbuddy/mcp.json`, the path WorkBuddy's own connector docs name; the dotted `~/.workbuddy/.mcp.json` is kept as a legacy carrier for host builds that read it; skill root `~/.workbuddy/skills`, no session hooks; installing the bundled plugin from the WorkBuddy plugin marketplace wires the skill and the MCP server together — no CLI step needed), and **Doubao** (`doubao` → dual skill roots `~/Doubao/skills` and `~/.agents/skills`, i.e. the client-reported `local_skill_paths`). The local skill collector scans both roots into the app's `# Skill Catalog` and syncs USER skills to the account for cross-device use. The MCP connector is projected by the Doubao manifest and local commands are constrained by `mcp_command_path_policy` (no arbitrary absolute paths or wrapper scripts), so `install` writes only the skills and truthfully reports `skipped`; the tool plane uses the address-style connector (`rolling-memory mcp --http`, see §4.4).
- **Native Plugin Single-Path Deduplication**: When ZCode or Claude Code already has the self-contained native plugin enabled (`.zcode-plugin` / `.claude-plugin`), `install` automatically cleans up duplicate global config entries so only one instance runs.

### 4.3 Three Safe Config Writers

| Format | Target Harnesses | Write & Verification Strategy |
|---|---|---|
| **JSON Deep Merge** | ZCode / Claude Code / CodeBuddy / Cursor / Windsurf / Cline / WorkBuddy | Parses existing JSON, updates only the target server/hook entry, and preserves all other keys and ordering |
| **TOML Section Surgery** | Codex | Replaces or appends `[mcp_servers.<name>]` (and `.env` subtable) while keeping comments intact; re-parses with `go-toml` before writing |
| **YAML Marker Block** | `dsh` | Appends or updates a `# >>> rolling-memory (<name>) >>>` block; uninstall removes only the marked block |

### 4.4 Doubao Onboarding: From Zero to Working

For first-time users wiring this tool into the Doubao desktop app: **the skill plane is one command**; the MCP tool plane needs one step inside the app.

**Step 0 · Prerequisites**: Doubao desktop installed and launched at least once (first launch creates the user workspace `~/Doubao`); this tool installed (`rolling-memory --version` prints a version).

**Step 1 · Write skills** (dual roots, i.e. the client-reported `local_skill_paths`):

```bash
rolling-memory install doubao            # writes both skill roots below
rolling-memory install doubao --check    # verify: two "skill ok" lines
```

| Skill root | Purpose |
|---|---|
| `~/Doubao/skills/<name>/SKILL.md` | Doubao user workspace (sibling of `chats/`), created on first launch |
| `~/.agents/skills/<name>/SKILL.md` | Cross-agent convention root; same-name skills here take precedence over additional roots |

**Step 2 · Let Doubao discover it**: Restart Doubao or open a new session to trigger the local skill scan (background `background_sync`). The session skill list should show `rolling-memory`; scanned USER skills are packaged and uploaded to your Doubao account for cross-device sync — **no developer account and no marketplace review required**.

**Step 3 · Connect the MCP tool plane (address-style)**: Doubao's connector accepts **address-style** MCP (`mcp_transport`: Streamable HTTP / SSE) but rejects arbitrary local command paths (`mcp_command_path_policy` rejects wrapper scripts with `STDIO_COMMAND_ABSOLUTE_NOT_ALLOWED`). Start a local URL service and paste the address into Doubao:

```bash
rolling-memory mcp --http                                    # default http://127.0.0.1:8765/mcp (SSE endpoint: /sse)
rolling-memory mcp --http --addr 127.0.0.1:9000 --path /mcp  # custom port / path
```

In the Doubao connector form choose **Streamable HTTP** and enter `http://127.0.0.1:8765/mcp` (if only SSE is offered, enter `http://127.0.0.1:8765/sse`). Save, open a new session, and the tool list should show `recall` — the memory plane exposes exactly one tool: conversation turns are captured by hooks automatically, while `recall` retrieves and crystallizes evidence into memory.

- The service binds loopback only (default `127.0.0.1`), caps request bodies at 8 MiB, and rejects cross-origin requests; keep it running, preferably under a system service manager.
- The MCP line from `install doubao` still reports `skipped` — that is truthful: registration lives on the Doubao side (encrypted credential storage + manifest projection) and cannot be written from the CLI, so you paste the address in the app.

**Troubleshooting**: If the skill does not appear, first confirm `SKILL.md` exists under both skill roots; then inspect the Doubao runtime log `~/Library/Application Support/Doubao/sdk_storage/log/saman_<date>.log` for `local_skill_paths` (client-reported roots), `scan_local_skills` (scan results and `item_count`), and `[MCP_SERVICE] get all tools … connector_count=` (MCP loading). If the address connector does not work, self-test the local service first: `curl -s http://127.0.0.1:8765/mcp -H 'content-type: application/json' -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'`.

**Uninstall**: `rolling-memory install doubao --uninstall` (removes only files written by this tool; the Doubao account copy must be removed in the app).

---

## 5. Common Commands

```bash
# 1. Inspect detected harnesses and target registration paths
rolling-memory install --list

# 2. Preview planned changes without modifying any files
rolling-memory install --dry-run

# 3. Wire all detected harnesses (skills + MCP + lifecycle hooks + instructions)
rolling-memory install --with-hooks --with-instructions

# 4. Project-scoped installation (defaults to ./.rolling-memory/memory.db and scope project:<dir>)
cd /path/to/project && rolling-memory install --project zcode claude-code

# 5. Coexist with another instance using a custom server/skill name
rolling-memory install --name rmem --skill-name rmem

# 6. Wire specific harnesses or a specific dsh profile
rolling-memory install zcode dsh --profile web

# 7. Verify static registrations + run a live MCP initialize / tools/list handshake
rolling-memory install --check

# 8. Safely uninstall all entries written by Rolling Memory (preserves foreign entries)
rolling-memory install --uninstall
```

---

## 6. Four-Tier Verification Gate

`rolling-memory install --check` automatically verifies Tiers 1–3; Tier 4 is observed in a new agent session:

| Tier | Check | Verification Method |
|---|---|---|
| **Tier 1: Skill Discovered** | `SKILL.md` exists under the harness skill root with valid frontmatter | `rolling-memory install --check` |
| **Tier 2: MCP Registered** | Valid `rolling-memory` server entry exists in the harness configuration file | `rolling-memory install --check` |
| **Tier 3: Live MCP Handshake** | Spawns the launcher, sends `initialize` and `tools/list`, and confirms the single `recall` tool | `install --check` (`server ok · rolling-memory ... · tools: recall`) |
| **Tier 4: Session Behavior** | Agent calls `recall` on history-dependent prompts; hooks capture the turn for local memory | Ask a history question in a new session or inspect `~/.local/state/rolling-memory/rolling-memory.log` |

---

## 7. Launcher Shim & Portable USB Host Anchor

Because GUI IDEs frequently spawn MCP servers and hooks with a cropped `PATH`, `install` generates an absolute-path launcher shim:

```sh
# ~/.local/share/rolling-memory/bin/rolling-memory.sh (0755)
#!/bin/sh
# Rolling-Memory launcher (generated by `rolling-memory install`).
# Absolute path on purpose: harnesses often spawn MCP servers and hooks with a cropped PATH.
exec "<absolute>/bin/rolling-memory" "$@"
```

- **Self-Contained Execution**: The Go binary embeds SQLite and all runtime logic; the shim performs a single `exec`.
- **Windows Support**: Generates `%LOCALAPPDATA%\rolling-memory\bin\rolling-memory.cmd` (`@echo off` + absolute path + `%*`).
- **Portable USB Host Anchor**: When `install` is executed from a `.portable` USB payload, the generated launcher automatically upgrades to a **mount-drift-healing Host Anchor** — executing the original path when mounted at the same location, or scanning removable-media mount roots (`/Volumes/*`, `/media/*`, `/run/media/*`, `/mnt/*`, and Windows `D:..Z:`) when mount names or drive letters shift (see [usb-portable.md](usb-portable.md)).
- **Ownership Guard**: Shims carry the `Rolling-Memory launcher` header comment so subsequent `install` runs update them idempotently while foreign scripts report `conflict`.

---

## 8. Backups, Idempotency & Rollback

- **Automatic Pre-Write Backups**: Every modified configuration file is backed up to `<file>.rolling-memory.<YYYYmmddHHMMSS>.bak` before writing.
- **Strict Idempotency**: Re-running `install` when nothing changed reports `unchanged` without creating redundant backups.
- **Conflict Protection**: Existing entries not owned by this instance report `conflict` unless `--force` is explicitly supplied.
- **One-Command Rollback**: Restore any `.bak` file or run `rolling-memory install --uninstall` to remove all registered entries cleanly.

---

## 9. Online Self-Update & Vault Safety (`rolling-memory update`)

End users can keep the CLI binary, the local web console, and all wired harness integrations up to date without `git` or a Go toolchain via `rolling-memory update` (aliased as `rolling-memory upgrade`):

```bash
# 1. Compare local SHA-256 digests against the release channel's SHA256SUMS (read-only)
rolling-memory update --check

# 2. Update the CLI binary, refresh wired harnesses, and update the console if installed
rolling-memory update

# 3. Also download/install or update the local web console (rolling-admin + web-dist.tar.gz)
rolling-memory update --with-console

# 4. Use a custom release mirror URL (or set ROLLING_MEMORY_RELEASE_BASE)
rolling-memory update --release-base https://mirror.example.com/rolling-memory/releases/latest/download

# 5. Machine-readable JSON output for scripts or admin tooling
rolling-memory update --check --json
```

- **Fail-Closed SHA-256 Verification**: Every downloaded artifact (`rolling-memory-<os>-<arch>`, `rolling-admin-<os>-<arch>`, `web-dist.tar.gz`) is verified against the signed `SHA256SUMS` manifest before replacing any local file; any digest mismatch aborts the update without touching existing binaries.
- **Zero-Downtime Daemon & Console Handover**: After atomically swapping the CLI binary, `update` signals the local background daemon (`engine.sock`) to step down cleanly so the next MCP or CLI call spawns the new binary immediately, and restarts the local web console if it was running.
- **Vault Safety Guarantee**: Updating never modifies, truncates, or overwrites your local SQLite memory vault (`~/.local/share/rolling-memory/memory.db`); schema migrations are applied idempotently in place when the database is opened.
- **Zero-Environment Design (No Git / Go / Node.js / Python Required)**:
  - Both `install.sh` and `install.ps1` fetch prebuilt static binaries verified against `SHA256SUMS` (supporting `curl` or `wget` on POSIX, native x64 and ARM64 on Windows, and automatic release-mirror fallback when the primary channel is unreachable).
  - The installer automatically and idempotently adds `~/.local/bin` (or `%LOCALAPPDATA%\rolling-memory\bin` on Windows) to your user shell profile / `PATH` (set `ROLLING_MEMORY_NO_MODIFY_PATH=1` to opt out).
  - The local Web Console (`rolling-memory panel`) serves its static frontend directly from the Go binary (`--serve-web`), requiring neither Node.js nor Python on the user's machine. Pass `--desktop-shortcut` to `rolling-memory install` to create a double-clickable desktop launcher.


