# Web Console & Plugin Channel Guide

**[English](plugin-console.md) · [简体中文](zh-CN/plugin-console.md)**

Rolling Memory ships with a full pure-Go SQLite Web Administration Console (`rolling-memory panel`) as the unified visual console for the entire system, integrating real-time engine telemetry, the dedicated Memory Console, the ROI & Cognitive Efficacy Monitor, interactive force-directed memory graph visualization, memory governance, frozen benchmark evaluation, plugin lifecycle management, and the documentation hub into a single workspace.

![Rolling Memory Live Console — local vault telemetry, MCP recall ledger and memory asset growth](assets/live-console.webp)

*The Live Console at a glance (local mode): on-device vault telemetry, the MCP `recall` ledger, and 3-layer memory asset growth.*

> **Building & running from source (`admin/`)**: the Web Administration Console source (`admin/`) is available in this repository; see [LICENSE](../LICENSE) and [NOTICE](../NOTICE) for license and attribution details, and `admin/README.md` for the module guide. We do not ship prebuilt console binaries — build and launch it locally from source:
>
> 1. **Build the web frontend** (outputs to `admin/web/dist/`):
>    ```bash
>    cd admin/web
>    npm install
>    npm run build
>    cd ../..
>    ```
> 2. **Build & launch with `rolling-memory panel` (one command)**:
>    ```bash
>    CGO_ENABLED=0 go build -trimpath -o rolling-memory ./cmd/rolling-memory
>    ./rolling-memory panel
>    ```
>    `rolling-memory panel` automatically compiles `admin/cmd` from your checkout, starts the backend API on `127.0.0.1:9006`, serves `admin/web/dist` on `127.0.0.1:8008`, and opens `http://localhost:8008/#/admin` (sign in with the local demo account shown on the login page, default `rolling / rolling`). Use `rolling-memory panel status` to inspect both processes, `rolling-memory panel stop` to stop them, `rolling-memory panel restart` to reload them from the installed backend and web assets, and `--api-port` / `--web-port` (or `RM_PANEL_API_PORT` / `RM_PANEL_WEB_PORT`) to customize ports.
> 3. **Or run backend and frontend manually** (for console development):
>    ```bash
>    # Terminal 1 — Backend API (127.0.0.1:9006)
>    cd admin && CGO_ENABLED=0 go build -trimpath -o bin/rolling-admin ./cmd && ./bin/rolling-admin serve
>
>    # Terminal 2 — Frontend dev server or static host (127.0.0.1:8008)
>    cd admin/web && npm run dev
>    ```
>
>    **Frontend supply — both modes are supported:** `npm run dev` starts a Vite dev server with hot module replacement (frontend edits apply instantly; no build step needed), while `npm run build` produces static assets under `admin/web/dist/` that `rolling-memory panel --serve-web ./dist --web-port 8008` can host without a Node runtime. Installed copies without a source tree serve the built `web-dist/` assets, refreshed from the release channel by `rolling-memory panel --update`.
>
> **Local only by default**: the console listens on `127.0.0.1` and only accepts requests addressed to this computer (`localhost` / `127.0.0.1`), because it can read and modify all of your local memory. The out-of-the-box account is **`rolling` / `rolling`** — publicly documented, and the login page offers one-click fill when you open it locally. You can change the password anytime after signing in under **`Routine` → `Profile`**; the new password is kept in your local console database and survives upgrades. To reach the console from another device, set the `SERVER_HOST` environment variable (or `server.host` in `~/.local/share/rolling-memory/admin/config/config.yaml`) to `0.0.0.0` or a specific network address — and change the default password first.

---

## 1. Channel Overview

The left navigation bar of the Web Administration Console is organized into seven top-level functional channels — day-to-day channels on top, administration channels (permissions, routine settings, plugins) at the bottom:

| # | Top-Level Channel | Sub-Channels | Core Responsibilities |
| :--- | :--- | :--- | :--- |
| 1 | **Dashboard** (`dashboard`) | `Live Console` · `Memory Console` (`/#/admin/dashboard/artifacts`) · `ROI Monitor` · `Memory Graph` | Zero-poll WebSocket real-time engine KPIs & 5 ECharts telemetry panels, dedicated Memory Console (100% Vector-Free & LLM-Free local memory engine connectivity, local plaintext vault telemetry, and runtime switches), the Memory ROI & Cognitive Efficacy Monitor (1h / 24h / 7d / 30d / All-Time windows tracking L3 Noetic crystallization & citation links, cross-session memory reuse multiplier, `tokenz-r` distillation compression & delivery budget, zero-copy pointer ratio, per-workspace heat matrix, and Top Cited Units), and an interactive memory graph generated from the local vault with a Web Worker force-directed layout and collision detection (zero-poll WebSocket live auto-refresh; word / phrase / cluster / free-unit nodes — see §2.6) |
| 2 | **Memory Management** (`plugin/rolling-memory`) | `Recall Debugger` · `Memory Overview` · `L1 Docs & Code` · `L2 Sessions` · `L3 Recall Trail` | The **Recall Debugger** runs the exact `recall` path your Agent uses: an optional workspace anchor (that project's evidence ranks first, nothing is dropped), one-click replay of recent Agent recalls, and read-only by default (nothing is written to L3). The three layer pages browse mounted-folder chunks, Harness session records, and the Agent recall trail with multi-dimensional filters, source-span provenance, and deletion; any L3 row can be replayed in the debugger |
| 3 | **Evaluation** (`plugin/rolling-memory/eval-dir`) | `Evaluation Lab` · `Cambridge ATM-Bench Leaderboard` · `LoCoMo-Backboard Leaderboard` | Cambridge ATM-Bench (`--suite atm`, `hard / 1013` primary benchmark) and LoCoMo (`200 / 1540` comparative reference) evaluation runs; two dedicated leaderboard pages display the University of Cambridge ATM-Bench multimodal personal-memory board (official `Recall@10` definition, Main-1013 / Hard-31 tabs) and the cryptographically pinned `locomo-backboard` comparative board |
| 4 | **Help Center** (`help`) | `Help & Docs` | Documentation portal that opens the official GitHub or Gitee repository docs in a new tab; the console no longer stores a local copy |
| 5 | **Permissions** (`auth`) | `Role Groups` · `Administrators` · `Menu Rules` · `Admin Logs` | RBAC permission tree, menu routing rules, and administrator operation audit logs |
| 6 | **Routine** (`routine`) | `System Config` · `Profile` | Site parameters and administrator profile settings |
| 7 | **Plugins** (`plugin-center`) | `Plugin Management` · `Plugin Sources` · `Plugin Catalog` | Host plugin lifecycle management (install/enable/disable/uninstall), `local`/`http`/`git` repository sources, and manifest synchronization |

### 1.1 Framework Provenance

The console is **not a bespoke admin framework written from scratch**. Its provenance and boundaries:

| Layer | Source project | License | Relationship in this project |
|---|---|---|---|
| **Backend & server framework** | **AI GO ADMIN** (Go + Gin + PostgreSQL backend; Vue 3 + TypeScript + Vite + Pinia + Element Plus frontend) | Apache-2.0 | **Derivative work**: original `LICENSE` and directory skeleton retained, storage adapted to local SQLite, and Rolling Memory pages, APIs, permissions, routes, branding, Memory Console, ROI monitor, memory graph, and evaluation lab added; demo/payment/market code was removed where it did not apply |
| **UI lineage** | **BuildAdmin** (the PHP admin framework that AI GO ADMIN reimplements in Go) | Apache-2.0 (Copyright 妙码生花) | **Lineage source**: the console's interaction and visual system derive from BuildAdmin; the **plugin catalog page** (`web/src/views/admin/plugin/catalog/`) is ported from BuildAdmin `v2.3.9` (commit `4686522`) and adapted |
| **First-party code** | — | PolyForm-Noncommercial-1.0.0 | `internal/rmconsole`, `web/src/views/admin/plugin/rolling-memory/`, `web/src/views/admin/dashboard/`, and their routes / APIs / permissions / brand assets are original to this project |

**Declaration**: The console is a derivative work of **AI GO ADMIN**; its UI lineage traces back to **BuildAdmin** — both projects are open-sourced under **Apache-2.0**; this project retains their copyright notices, honors their license terms, and discloses all modifications. Full attribution (source URLs, version and commit, per-file modification notes) is registered in [`../NOTICE`](../NOTICE) §2; third-party trademarks, logos, demo images, payment icons, and account/marketplace APIs are **not** included in this project.

**Acknowledgments**: Our sincere thanks to the authors and communities of **AI GO ADMIN** ([GitHub](https://github.com/ai-go-hub/ai-go-admin) · [Gitee](https://gitee.com/ai-go-hub/ai-go-admin) · [AtomGit](https://atomgit.com/ai-go-hub/ai-go-admin), © AI GO ADMIN contributors) and **BuildAdmin** ([Gitee](https://gitee.com/wonderful-code/buildadmin), © 妙码生花) — your open-source work laid a solid foundation for this console.

---

## 2. Dedicated Memory Console & Zero-Poll Real-Time WebSocket Architecture

### 2.1 `Dashboard -> Memory Console` (`/#/admin/dashboard/artifacts`)

Alongside the **Live Console**, **ROI Monitor**, and **Memory Graph**, the dashboard provides a dedicated **`Dashboard -> Memory Console`** (`/#/admin/dashboard/artifacts`) for unified monitoring and control of the **Rolling Autonoetic Memory Engine** (100% Vector-Free · 100% LLM-Free):

- **Vector-Free & LLM-Free Architecture Panel**: Requires zero embedding model or cross-encoder reranking configuration — directly monitors local memory engine status, recall throughput, and local plaintext vault statistics.
- **Live Runtime Switches**: Toggle recall ledger recording (`ledger`) and the floating status widget (`widget_enabled`) in real time.

### 2.2 Zero-Poll Real-Time WebSocket Streams

The Memory Console, status widget, ROI monitor, environment health panel, and terminal drawer all replace timer polling with a **Zero-Poll WebSocket real-time channel**:

- **Event-Driven Instant Push**: Configuration updates, memory writes, and background job transitions stream immediately over a shared WebSocket connection without background timer polling overhead.
- **Change-Driven, Quiet When Idle**: Each panel subscribes to a room that pushes a frame only when its underlying state actually changes; while nothing happens, the console issues no queries, no aggregations, and no maintenance subprocesses. All open panels share one connection and one server-side watcher, so cost does not multiply with the number of open tabs.
- **Live Task Progress Bars**: During benchmark runs or maintenance tasks, the WebSocket channel pushes live completion percentages, throughput (**`items/s`**), and estimated time remaining (**`ETA`**) directly to the console.
- **Optional Fixed Cadence**: Where a panel offers a refresh selector (Live Console, ROI Monitor), the fixed-cadence options (every 15s/30s/60s, or manual) remain available as a low-power alternative to the real-time stream.

### 2.3 `Dashboard -> ROI Monitor` (Rough Trend Estimator) & Evaluation Hook Toggles (`/#/admin/dashboard/roi`)

The console includes a built-in **ROI & Efficacy evaluation module**, and local mode enables **experience-evaluation hooks and evaluation cards** by default so you get an immediate feel for whether memory hits are helping.

> **Honest note & how to disable hooks anytime**:
> The automatic ROI panel in the web console is an **approximate trend estimator** (relying on heuristic overlap and citation counts rather than exact per-turn ground truth; for the exact turn-elimination math and our audited 7-day dogfooding ledger, see [`token-economics.md`](token-economics.md)). If you find hooks distracting, you can disable or remove them at any time:
> ```bash
> # Option 1: Wire only the pure MCP recall tool (removes all hooks and instruction blocks)
> rolling-memory install --mcp-only
>
> # Option 2: Keep the install, but turn off hook ledger recording
> rolling-memory config set hooks.ledger false
> ```

The panel extracts five signals from the local plaintext vault and recall ledger for reference:

- **L3 Crystallization & Citation Yield**: Tracks how often agent-initiated `recall` queries hit underlying L1/L2 evidence and crystallize into L3 Noetic handles with explicit `cited_unit_ids` links.
- **Memory Reuse Multiplier (Write Once, Cite N×)**: Measures average and peak cross-session citation reuse per hit unit, surfacing the **Top Cited Memory Units** leaderboard with full query provenance.
- **`tokenz-r` Delivery Budget & Distillation Compression**: Uses the unified `tokenz-r` tokenizer to measure average delivered tokens per recall and dry-sentence distillation compression.
- **Zero-Copy `@ref:` Vault Footprint & 14-Day Growth × Crystallization × Evidence Reuse**: Reports the zero-copy episodic pointer ratio, the deduplicated memory-address count, and a 14-day three-track trend of daily memory growth (L1 docs & code / L2 sessions), L3 crystallization, and observed evidence reuse (delivered anchors matched in later tool inputs or replies).
- **Per-Workspace (Locus) Heat Matrix & Evidence Drawer**: Diagnoses memory coverage and recall activity across workspaces (Active Recall hot zones vs. uncited cold zones) with one-click filtering and provenance drawers.

### 2.4 Online Version Check & In-Place Self-Update (`Version & Update` Dialog)

Both **`Memory Management -> Memory Overview`** (`/#/admin/plugin/rolling-memory/index`) and **`Dashboard -> Memory Console`** (`/#/admin/dashboard/artifacts`) provide a **Version & Update** button in the top action bar:

- **Zero Egress on Open**: Opening the dialog inspects only local SHA-256 digests of the three components (`rolling-memory` CLI binary, `rolling-admin` console backend, and `web-dist.tar.gz` frontend assets) with zero external network requests.
- **Update Source Choice**: The dialog offers **GitHub (official)**, **Gitee (mainland mirror)**, and **Custom mirror**. Choosing GitHub or Gitee saves `update.source` and is reused by the CLI and background checks; Custom lets you enter a one-off mirror URL. A Gitee tag without attached binaries is reported as a source-only update rather than a download that cannot succeed.
- **Explicit SHA-256 Manifest Comparison**: Clicking **Check for Updates** fetches `SHA256SUMS` from the configured release channel (or a custom mirror URL entered in the dialog) and displays a side-by-side table of local vs. remote SHA-256 digests and update status.
- **One-Click Verified Upgrade & Graceful Reload**: Clicking **Update Now** runs `rolling-memory update` with SHA-256 verification, atomically updates changed binaries and web assets, refreshes host harness integrations, and smoothly restarts the console before reloading the browser page — while leaving your local SQLite memory vault (`memory.db`) untouched.

### 2.5 Three-Layer Memory Browser & Markdown Preview (`/#/admin/plugin/rolling-memory/index`)

The memory overview and the L1 / L2 / L3 pages share one memory-unit browser. Lists and the detail dialog show the **original content** directly; L1 mounted chunk bodies are re-read from the source file when you open the detail, and if a source file has been moved or deleted the console says so plainly, shows the source location, and offers a one-click retry instead of pretending the coordinates are the content.

Every content surface (memory browser detail, recall debugger raw hits and full trace, recall results, and the ROI evidence drawer) shares one Markdown reading face with **no manual switch**: prose and Markdown documents render as standard Markdown (headings, lists, tables, quotes, and fenced code blocks with one-click copy and line numbers), while structured content is formatted automatically into fenced code blocks — JSON is pretty-printed, JSONL keeps one record per line, and source files are highlighted by language. **Copy raw** always copies the unmodified stored bytes. Formatting only changes how the text is displayed — it never rewrites, truncates, or fabricates content.

### 2.6 `Dashboard -> Memory Graph`

The Memory Graph is generated from your local vault by the engine itself. Nodes follow two orthogonal axes: the memory layer (**L1 Background / L2 Episodic / L3 Noetic**) and the structural level — **word** (word family), **phrase** (two adjacent terms whose text spans do not overlap; adjacent sliding windows of one character run are the same text projected twice and are not projected as phrases), **cluster** (container), and **unit** / **free unit**. Containers are grouped as **time episodes**, **entity clusters** (dominant word family), and **source clusters** (one engine document provenance — a multi-chunk file, PDF, email thread or any other chunked source). Units outside every container are shown as **free units** on the outer shell.

The canvas supports layer / kind / cluster-type filters, word and phrase inspection, cluster member drill-down, citation edges, and a detail drawer with word energy, phrase binding energy, pair and member df, `lift` association and coverage, and cluster membership. A dedicated **df=1 fragments** switch hides words and phrases that appear only once by default, so sparse noise does not drown the structural graph; turn it on to inspect the full lexical layer. When the local memory service is not running, the page shows an explicit empty state instead of sample data. The built-in LoCoMo sample remains available on the public landing page for layout preview.

![Rolling Memory Memory Graph — an interactive 3D memory nebula](assets/memory-graph.webp)

*Public landing-page preview: the built-in LoCoMo sample rendered as an interactive 3D memory nebula. The console's `Memory Graph` renders your real local vault with word / phrase / cluster / free-unit nodes, the three memory layers, the three container types, and citation links.*

---

## 3. Plugin Channel & Environment Gate

1. **Automatic Environment Detection (`dev` vs `prod`)**:
   - **Development (`dev`)**: When running inside a source checkout or with `plugin.env=dev`, plugin installation, uninstallation, and catalog synchronization are enabled.
   - **Production (`prod`)**: Because source-level plugins (`kind: code`) modify source trees and require recompilation, install and uninstall endpoints are locked by default in production (`lifecycle_forbidden`) to protect runtime safety.
2. **Three Plugin Forms**:
   - `code` (L1 Source Plugin): Ships Go routes and frontend views; requires backend recompilation after initial installation.
   - `declarative` (L2 Declarative Plugin): Pure metadata-driven entities mounted immediately by the generic rendering engine.
   - `mixed` (Hybrid Plugin): Combines declarative entities with custom extension logic.

---

## 4. Help Center: Official Documentation Portal

The **Help Center** channel is a lightweight portal. Opening `Help & Docs` shows a site chooser for GitHub and Gitee, and either entry opens the official repository's `docs/` directory in a new tab. The console does not keep its own markdown copy, so the text you read always matches the released source: [`https://github.com/rocareer/rolling-memory`](https://github.com/rocareer/rolling-memory) · [`https://gitee.com/rocareer/Rolling-Memory`](https://gitee.com/rocareer/Rolling-Memory).

The repository carries the full documentation set:

- **Root overview and legal declarations**: [`README.md`](../README.md) · [`README.zh-CN.md`](zh-CN/README.zh-CN.md) · [`DECLARATION.md`](../DECLARATION.md) · [`LICENSE`](../LICENSE) · [`NOTICE`](../NOTICE) · [`CHANGELOG.md`](../CHANGELOG.md)
- **Core technical manuals (`docs/` & `docs/zh-CN/`)**: [`token-economics.md`](token-economics.md) · [`background-vs-grep.md`](background-vs-grep.md) · [`faq.md`](faq.md) · [`mcp.md`](mcp.md) · [`harness.md`](harness.md) · [`usb-portable.md`](usb-portable.md) · [`benchmarks.md`](benchmarks.md) · [`privacy.md`](privacy.md) · [`plugin-console.md`](plugin-console.md)
