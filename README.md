# Rolling-Memory

**[English](README.md) · [简体中文](docs/zh-CN/README.zh-CN.md) · [Documentation Hub](docs/README.md) · [Token Math & Falsification](docs/token-economics.md) · [FAQ](docs/faq.md) · [Benchmarks](docs/benchmarks.md) · [Declaration & Disclaimer](DECLARATION.md)**

**Rolling Memory** is a source-available, local-first memory tool for AI coding assistants and agents (Claude Code, Codex, Cursor, Windsurf, Cline, ZCode, etc.) — free for any noncommercial use, built on the **Rolling Autonoetic Memory Engine**.

It automatically records your conversations and mounted code, so in any later session your agent can call one MCP tool — `recall` — to look up past decisions, debugging post-mortems, and code coordinates instead of re-running blind searches. Everything runs in a local SQLite database on your machine with no background model calls, and one shared vault serves every wired harness.

Our goal is simple: **making AI memory accessible and affordable for everyone.** Long-term memory for coding agents should work like local files — a basic utility that any individual developer, student, or small team can run out of the box, without needing a credit card, cloud subscriptions, or a background pipeline that quietly burns LLM tokens on every turn.

In everyday use, that comes down to four practical things: **genuinely free, real-time with no waiting, private with no third party in the middle, and portable enough to carry on a USB drive** —
- **Genuinely free (zero hidden bills)**: No background LLM extraction fees, no online vector embedding API bills, and no cloud hosting or account subscription costs — no sign-up and no credit card;
- **Real-time (no waiting for indexing or LLM extraction)**: There is no background queue waiting on an LLM to extract facts or a batch job to compute embeddings. Conversations and mounted files are indexed in local SQLite in milliseconds and are searchable right away;
- **Safe and private (no third party in the middle)**: Everything compiles to a single zero-dependency Go binary (`CGO_ENABLED=0`) and lives in one SQLite file on your machine, with zero network requests in the memory layer — **other than you and the agent actively working for you, no third-party memory service, embedding provider, or external server ever sees your code, file paths, or conversations.** One precise boundary: if your coding agent uses a remote LLM API, the passages `recall` returns travel to that model provider as part of your prompt (see [docs/privacy.md](docs/privacy.md) §1);
- **Evaluation hooks, evaluation cards, and a rough ROI module enabled by default (easy to disable anytime)**: In local mode, experience-evaluation hooks and evaluation cards are enabled by default so you get an immediate feel for whether a memory hit actually helped, alongside a built-in (admittedly rough) ROI estimation panel in the web console. If you find hooks distracting, you can disable or remove them at any time:
  ```bash
  # Option 1: Wire only the pure MCP recall tool without any hooks or instruction blocks
  rolling-memory install --mcp-only

  # Option 2: Keep the install, but turn off boot-context injection or hook ledger recording
  rolling-memory config set hooks.boot_context false
  rolling-memory config set hooks.ledger false
  ```
- **Built-in USB portable mode**: Because both the binary and the SQLite vault are self-contained files, we also included a USB portable mode (see [`docs/usb-portable.md`](docs/usb-portable.md)) so you can put the tool and your memory vault onto a thumb drive and carry them around. It is still experimental and not thoroughly tested yet, and it comes with a real prerequisite: **for project-specific memories to be useful on another machine, you have to be working on the same project there**. If you are curious and like tinkering, feel free to try it out and tell us what works and what does not.

> **Get started**: Run `curl -fsSL https://github.com/rocareer/rolling-memory/raw/main/install.sh | bash` (or `bash install.sh` from a local checkout) to wire your coding assistants, then just chat normally. Whenever you need to trace back history, ask something like *"Where did we fix that bug last time?"* See the [Quick Start Guide](docs/harness.md) for details.

> **About this release**: This repository ships **pure local mode (`local_plain`)**, running fully offline with zero external network requests. The cloud memory engine is offered separately on an application basis and is not part of this repository; see [DECLARATION.md](DECLARATION.md) for the release and license boundary and [SUPPORT.md](SUPPORT.md) for how to get help.

---

## 1. What does it actually do for you in daily work? (Every saved turn counts)

What it does under the hood is straightforward: it saves your past conversations and mounted local documents **verbatim in a local SQLite database**, indexes them with local lexical and statistical signals, and gives your agent a single MCP tool — `recall` — to look up past records and their source coordinates.

What is the practical benefit? **Helping your harness agent cut down on blind search round-trips.**

In daily long-horizon coding sessions (where active context sizes typically sit between **`100k` and `400k` tokens**), every extra tool-use turn forces the client to **re-send that entire 100k–400k context to the LLM as billed input tokens**, while you wait for another round of model inference and tool execution. When you ask something like *"Where did we fix that status-bar flicker last time?"* or *"Why did we drop Approach A?"*:
- Without memory, the agent often has to guess keywords and spend 3–5 turns running `grep`, `find`, and `Read` by trial and error;
- If `recall` hits a past record that points straight to the target file or decision, **eliminating even a single unnecessary round-trip saves one full re-read of your current context's total tokens (`100k–400k` tokens) — and shaves a bit of wall-clock time off the task. Every bit saved helps.**

Of course, **if `recall` misses and returns nothing useful, that extra turn costs you tokens instead of saving them.** Rather than making vague claims, we wrote a dedicated note laying out the exact arithmetic — when it saves tokens, when it loses tokens, how you can falsify our claim, and **our own real-world dogfooding log from the past week (and past 24 hours)**:
👉 **[How Local Memory Saves (or Costs) Tokens and Time: The Turn-Elimination Math (with Our 7-Day Real Usage Report)](docs/token-economics.md)**

> **A quick look at our own 24-hour audited numbers & 7-day cost projection**:
> On our primary development machine (where `110,340` LLM requests over the past 7 days averaged **`272.0k` input tokens per call** — and the last 24 hours averaged **`320.1k`**), the last 24 hours produced **`33` deduplicated turn-review cards**: **`6` (18.2%) honestly reported `0` saved turns** (two recall calls timed out under heavy load; four turns needed no lookup at all), while **`27` (81.8%) pinpointed exact file coordinates or past fixes and saved `116` blind search turns in total (≈`4.3` per hit)**. A model-free cross-check agrees: of the **`46` recall outcomes settled by our deterministic post-hoc adoption audit in the last 24 hours, `41` (89.1%) were actually adopted** in the agents' subsequent tool calls or replies (`38` direct file/symbol coordinates + `3` context facts) — versus **`61.1%`** across the longer retained ledger.
> Extrapolating that 24-hour audited pace across a full week (**`× 7 days`**) projects to **`812` avoided blind turns per week** — or **`162.4M` gross tokens saved per week (`158.2M` practical net; `154.0M` strictest net, assuming every zero-turn card cost a full standalone `200k` turn)** at a conservative **`200k` tokens per turn**. At the latest official API pricing for **GPT-6 Astra, Claude Fable 5.2, and Gemini 4 Argon**, even after factoring in an 85% prompt-cache read ratio, that translates to roughly **`$74.7–$885/week`** in real blended savings on a single heavy developer machine (**`$325–$3,248/week`** at standard uncached input rates). Full breakdown and pricing table in [`docs/token-economics.md` §6](docs/token-economics.md).

**To be completely upfront: we do not promise it will always help.** Lexical retrieval has real limits, and we are actively working to make it better —especially stripping out more log noise and improving recall accuracy. Suggestions, bug reports, and code or algorithmic contributions are warmly welcome so we can improve it together.

---

## 2. Why keep verbatim records + local lexical-statistical search?

Many memory systems ask an LLM to summarize conversations at ingestion time, or chunk and embed everything into dense vectors. Those approaches have clear strengths (summaries are compact; vectors bridge synonyms), but we chose to build a solid local verbatim baseline first for three practical reasons:

1. **Original records hold all the facts; ingestion-time rewriting doesn't just lose detail — it compounds error**: There is a common myth that LLM-based summarization is a form of "distillation." But if your agent can misinterpret context, so can the LLM tasked with summarizing it. **Using a potentially hallucinating model to rewrite an already potentially misinterpreted conversation is "error compounding."** Worse, once the original transcript is "summarized" and discarded, the truth is lost forever. Some systems propose "costly explicit iterative memory correction," but that leads to a paradox: **How do you detect the error? And how do you ensure the incorrect memory doesn't poison subsequent reasoning before you catch it?**
   Our ironclad rule is simple: **history happened as it happened.** We do not rewrite, compress, or "polish" records, because every rewrite is a new opportunity to bake hallucination and factual error into your long-term memory. A passage retrieved today is the same as the one retrieved next year, ensuring an unshakeable chain of evidence.
2. **Your agent already reads well — why pay a large upfront LLM extraction tax just to save a few dozen tokens at read time?**: The one obvious benefit of LLM summarization is that the extracted note may be a few dozen or a few hundred tokens shorter when retrieved later. Compared against a `100k–400k` per-turn context, however, saving a few hundred tokens at consumption time is tiny — yet to get that summary, you have to pay the **LLM comprehension cost** at write time, whether your active agent spends an extra `100k–400k` round-trip calling a "save memory" tool mid-task, or a separate sidecar LLM reads the context to extract facts. Since your coding agent already has strong reading comprehension and knows your current question, it is much more economical to run zero-cost deterministic format cleanup (stripping protocol wrappers, `<thinking>` blocks, and duplicate log lines) and hand the verbatim passage to the agent on demand.
3. **Code identifiers, errors, and file paths live in the literal words — while dense vectors blur exact symbols and AI coding sessions use consistent terminology**: A large share of engineering lookups (project codenames, function names, config keys, error strings, ports, version tags, dates, file paths) are exact lexical anchors. Dense embedding vectors work by smoothing text into a continuous semantic space — which bridges loose synonyms, but makes near-identical symbols like `err_code_4012` vs. `err_code_4013`, port `8008` vs. `1828`, `v0.0.1` vs. `v0.0.2`, or `2026-05-12` vs. `2026-05-19` look almost indistinguishable (`>0.98` cosine similarity), while static embedding vocabularies struggle with newly coined identifiers and require heavy model downloads or minutes of CPU fan spin to backfill tens of thousands of historical turns. Just as importantly, in a coding harness you are typically working with the same model on the same codebase: unlike casual human conversation, an AI assistant writes structured prose without ungrammatical phrasing, dropped subjects, or ambiguous pronouns, and its vocabulary stays consistent across turns.
4. **Clear division of labor — skipping heavy stacks is a deliberate engineering choice, not a lack of familiarity**: When you work in Claude Code, Cursor, or ZCode, a frontier LLM is already sitting in front of you. A local memory tool does not need to run a second small model in the background to guess semantics; its job is to surface verbatim, high-information evidence in tens of milliseconds and leave 100% of semantic reasoning to your host agent. Our team knows the heavy LLM-extraction + vector + knowledge-graph route firsthand — across earlier heavyweight memory prototypes, we achieved a **92.79** score on the LoCoMo benchmark. Building and running those heavy pipelines is precisely what made their hidden token bills and ingestion latency obvious to us, and motivated us to see how far a genuinely free, lightweight local system can go for everyday coding work.

---

## 3. How it works

The system divides the work into three simple steps. The first two run locally via deterministic code; the third is left to your agent:

| Step | Where it runs | What it does |
|---|---|---|
| **1. Store records** | Local SQLite file (default `~/.local/share/rolling-memory/memory.db`, directory `0700`, file `0600`) | Stores conversation turns, chunk offsets for mounted local files, timestamps, source identifiers, and content hashes. When mounting local code or doc folders, it records only file paths and byte offsets and reads files in place on demand, without duplicating your repository into the database. |
| **2. Index & retrieve** | Local indexes inside the same SQLite file | Combines SQLite FTS5 trigram search and BM25 with three non-vector information-theoretic mechanisms: **(a) adaptive self-information & burst-clone folding** (rarity weights adapt directly to your live vault's distribution without relying on frozen external vocabularies, while near-duplicate logs pasted repeatedly on the same day are folded logarithmically so they never dilute rare terms); **(b) deterministic cross-form alignment** (bridging natural-language dates like `"May 12, 2025"` with ISO `"2025-05-12"`, morphological variants, compound identifiers, and inline number-unit pairs like `"500ms"` or `"16GiB"`); and **(c) dimensionless multi-cue coherence resonance** (amplifying candidate passages where multiple rare cues from the query co-occur within a tight temporal window). Indexes can be rebuilt from the raw records at any time. |
| **3. Read & judge** | Your host agent or you | The agent calls the single MCP tool `recall`, receives the matching source passages and provenance pointers, and reads them in the context of your current question. |

In daily use, the agent never needs to call a manual "write memory" tool: a background watcher records completed conversation turns during quiet windows, and whenever `recall` hits useful passages, the system records the link between that query and the cited unit IDs locally so related future lookups can find them faster.

---

## 4. Public benchmark results and where it falls short

We evaluated this local retrieval pipeline on two public long-term memory benchmarks: the University of Cambridge **ATM-Bench** (11,034 personal multimodal text records) and **LoCoMo** (5,882 dialogue turns across 1,540 questions). All figures below can be recomputed directly from the archived per-question retrieval outputs (JSONL).

> **Metric note**: ATM-Bench `Recall@10` below strictly follows the official evaluator (`comprehensive_eval.py`), which computes the **mean per-question gold-set coverage**. Binary reachability (`Any-Hit@10`, whether at least one gold item appears in the top 10) is listed separately as an auxiliary diagnostic and is never mixed with official `Recall@10` when comparing against public baselines. Daily plugin usage (`rmem recall` / MCP `recall`) and the `rmem eval` runner share the exact same local engine, sentence-harmonic encoder, and ranker.

| Metric | ATM-Bench Main (1,013 questions) | ATM-Bench Hard (31 questions) | LoCoMo Full Set (1,540 questions) |
|---|---|---|---|
| **Official `Recall@10` (per-question gold coverage mean)** | **77.21%** | **46.12%** | — |
| **Auxiliary `Any-Hit@10` (at least one gold item in Top-10)** | 80.45% (815/1,013) | 80.65% (25/31) | — |
| **Answer Token Recall / Answer-in-Context (2026-11-08 reference run)** | — | — | 72.93% / 53.96% (831/1,540) |
| **End-to-end judge accuracy (deepseek-v4.1-flash judge, 2026-11-16)** | — | — | **85.26%** (1313/1540 · cat1 70.57 / cat2 87.23 / cat3 71.88 / cat4 90.96) |
| **Per-query retrieval latency (warm cache, mean / P95)** | 29.05 ms / 58.5 ms | 41.89 ms / 86.5 ms | 31.40 ms / 49.7 ms |

These results show both what local lexical-statistical search can do and where its limits lie:

- **Roughly one in five Main questions still misses the Top-10 (and why that matters less in coding harnesses)**: On ATM Main-1013, **199 questions (19.64%)** have no gold evidence in the top 10, and **120 questions** have none in the top 50. On Hard-31, **6 questions** miss the top 10. Inspecting those misses shows they are mainly cases with **zero lexical overlap** between the question and the record. That happens often in **human-to-human casual conversation** — for example, nicknames vs. formal names (calling someone "Old Zhang" in one place and "Zhang San" in another), dropped subjects, or vague references (*"that project… that person… got it done"*). In **coding harness workflows**, however, you typically use the same model to develop a specific project: its output is consistent and well-formed, free of ungrammatical phrasing or ambiguous references, and anchored on the same module names, symbols, and file paths. That environment fits this system's lexical-statistical architecture naturally, and dense vectors or graphs add less value here than people often assume (we are experienced in that area too — our team previously achieved a **92.79** score on LoCoMo with heavyweight vector/graph/LLM architectures; leaving them out of the default local path here is a practical engineering choice).
- **How much a local dense vector adds (controlled test)**: On the same 11,034 ATM records, running a local `all-MiniLM-L6-v2` vector retriever alone gives an official `Recall@10` of 67.35% on Main and 29.58% on Hard. In that controlled run (pre-fusion reference baselines: Main 76.72% / Hard 41.93%), fusing the vector model with our lexical-statistical backbone at a **small weight ($w_{\mathrm{vec}} = 0.15$)** via RRF raised Main `Recall@10` to **78.42% (+1.70 percentage points, +17 net hit questions)** and Hard to **42.96%–43.20%**; however, fusing them at **equal weight ($w_{\mathrm{vec}} = 1.00$)** pulled Hard `Recall@10` down to **38.68% (-3.25 percentage points)**.
- **Same-protocol reference points (not an official leaderboard submission)**: Under the same standard fixed 2B SGM text protocol, publicly reported ATM-Bench official `Recall@10` baselines include MemPalace (76.40%), ATM-RAG (68.70%), HippoRAG2 (66.40%), and Mem0 (61.90%) on Main; and MemoryOS (32.70%), HippoRAG2 (31.90%), ATM-RAG (30.40%), and Mem0 (23.70%) on Hard. Our numbers are local recomputations — **this system is not an official leaderboard submission and claims no official rank**.

---

## 5. Known limitations and scope boundaries

1. **Preserving a record does not make its content true**: The memory system only tells you that a statement was recorded at a given time. That statement may have been mistaken, may now be outdated, or may contain untrusted text. Verifying whether a past record is still accurate remains the job of the human user and downstream agent.
2. **"No LLM" and "offline" apply only to the memory plane**: Ingesting, indexing, and retrieving memories make no model calls and no network requests. However, if your coding agent is connected to a remote LLM API (such as Claude, GPT, or DeepSeek), the passages returned by `recall` will be sent to that model provider when the agent answers your prompt.
3. **Scores reflect iterative development on these benchmarks**: The weighting rules and heuristics were refined through multiple rounds of error analysis on ATM-Bench and LoCoMo; they should not be read as blind zero-shot generalization to unseen datasets.
4. **Language and scale coverage need broader testing**: Current evaluations focus on English personal archives and long dialogues (~$10^4$ records). Other languages, heavy code-switching, and million-record streaming workloads need further testing and community feedback.
5. **Provided "AS IS"**: This software is provided without warranty of any kind. See [DECLARATION.md](DECLARATION.md) for the full disclaimer.

---

## 6. Quick start

### Option A: One-line installer or Desktop Client (macOS / Linux / Windows — no Git, Go, Node.js, or Python required)

**Desktop Client (macOS `.dmg` / Windows `.zip` — zero terminal required)**:
- **macOS (`Rolling-Memory-<version>-darwin-arm64.dmg` / `darwin-amd64.dmg`)**: Drag **`Rolling-Memory.app`** to `/Applications` and launch it. On first launch it deploys the bundled engine and console assets, initializes your local memory vault, wires detected AI hosts, places a menu-bar tray icon (for one-click session sync, host re-wiring, and online updates), and opens the console in a native desktop window.
- **Windows (`Rolling-Memory-<version>-windows-amd64-desktop.zip` / `windows-arm64-desktop.zip`)**: Extract and double-click **`Rolling-Memory-Desktop.exe`**. It silently initializes your local vault, wires detected AI hosts, creates a Desktop shortcut, starts the local console services (`:9006` / `:8008`), and opens a dedicated app window.

**Command-line one-liner (zero pre-installed dev tools required)**:
```bash
# macOS / Linux: downloads SHA-256 verified prebuilt binary (via curl or wget, with automatic mirror fallback),
# adds ~/.local/bin to your shell profile, initializes the local vault, wires detected harnesses, and runs check
curl -fsSL https://github.com/rocareer/rolling-memory/raw/main/install.sh | bash
# Optional: add a desktop shortcut icon to launch the Web Console with a double-click
rolling-memory install --with-hooks --desktop-shortcut

# Windows PowerShell (supports both x64 and ARM64; automatically adds the install directory to your User PATH):
irm https://github.com/rocareer/rolling-memory/raw/main/install.ps1 | iex
```

### Option B: Build from source (requires Go 1.26+)

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

### Option C: Online self-update (CLI & Web Console — SHA-256 verified, Git-free)

```bash
rolling-memory update --check          # compare local SHA-256 digests against the release channel without modifying files
rolling-memory update                  # alias: `rolling-memory upgrade` — update CLI binary, installed console, and host wiring
rolling-memory update --with-console   # also install or update the local web console (backend :9006 + web frontend :8008)
rolling-memory panel --update          # check and update console assets before opening http://localhost:8008/#/admin
rolling-memory update --release-base https://your-mirror.example.com/releases/latest/download
```

You can also click **Version & Update** in the top bar of the Web Console (`Memory Overview` or `Memory Console`) to inspect local SHA-256 digests, check for updates, and upgrade in place from the browser. Updating only swaps binaries, static web assets, and host wiring — it **never modifies or overwrites your local SQLite memory vault (`memory.db`)**.

---

## 7. MCP and multi-harness setup

Running `rolling-memory install --with-hooks` automatically configures detected installations of **ZCode, Claude Code, Codex, Cursor, Windsurf, Cline, dsh, CodeBuddy, WorkBuddy, and Doubao** with your machine's real paths. If you are wiring another MCP client by hand, replace `<YOUR_HOME>` below with your home directory path (e.g., `/Users/alice` on macOS or `/home/alice` on Linux):

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

For tool parameters and response formats, see [MCP Contract](docs/mcp.md) and the [Multi-Harness Guide](docs/harness.md).

---

## 8. Reproduce and verify benchmarks

The `rmem eval` runner verifies your corpus against the pinned `SHA-256` digests published in [docs/benchmarks.md](docs/benchmarks.md). **The benchmark corpora are not redistributed with this repository** — obtain them from the upstream projects, point the runner at your copy with `--eval-data <dir>`, then run retrieval evaluations completely offline and recompute metrics directly from the generated JSONL files. Both benchmarks support one-command acquisition (download from the pinned upstream revision, rebuild locally, verify the digests):

```bash
# Fetch + rebuild the ATM-Bench corpus from upstream (pin-verified, ~35 MB)
rmem eval --fetch-only --fetch atm --accept-upstream-license

# Fetch + rebuild the upstream-official LoCoMo question set (1,540, ~2.8 MB)
rmem eval --fetch-only --fetch locomo --accept-upstream-license

# Run ATM-Bench (Main-1013 or Hard-31) local retrieval evaluation (no answerer/judge LLM calls)
rmem eval --suite atm --tier 1013 --answerer none --judge off --top-k 50
rmem eval --suite atm --tier hard --answerer none --judge off --top-k 50

# Run LoCoMo local retrieval evaluation over the official Cat 1-4 question set
rmem eval --tier 1540 --answerer none --judge off --top-k 20
```

Each run uses an isolated evaluation scope so it never touches your personal memory database. Full instructions and verification steps are in [docs/benchmarks.md](docs/benchmarks.md).

---

## 9. Documentation

| Document | Contents | Simplified Chinese |
|---|---|---|
| **[Token Math & Falsification (`docs/token-economics.md`)](docs/token-economics.md)** | How eliminating a blind search turn saves (or costs) tokens in long-context sessions, with break-even math and counterexamples | [docs/zh-CN/token-economics.md](docs/zh-CN/token-economics.md) |
| **[FAQ (`docs/faq.md`)](docs/faq.md)** | How this relates to `grep` / `ripgrep`, vector RAG, LLM extraction services, and static `AGENTS.md` files | [docs/zh-CN/faq.md](docs/zh-CN/faq.md) |
| **[Benchmarks (`docs/benchmarks.md`)](docs/benchmarks.md)** | LoCoMo and Cambridge ATM-Bench reproduction commands, protocol pins, and raw JSONL recomputation | [docs/zh-CN/benchmarks.md](docs/zh-CN/benchmarks.md) |
| **[MCP Contract (`docs/mcp.md`)](docs/mcp.md)** | `recall` input parameters, output format, and background capture | [docs/zh-CN/mcp.md](docs/zh-CN/mcp.md) |
| **[Multi-Harness Guide (`docs/harness.md`)](docs/harness.md)** | One-command setup across supported coding agents and `install --check` | [docs/zh-CN/harness.md](docs/zh-CN/harness.md) |
| **[USB Portable Mode (`docs/usb-portable.md`)](docs/usb-portable.md)** | Running and safely ejecting a portable memory vault on removable drives | [docs/zh-CN/usb-portable.md](docs/zh-CN/usb-portable.md) |
| **[Web Console (`docs/plugin-console.md`)](docs/plugin-console.md)** | Built-in local web console for browsing memories, status monitoring, and running evaluations | [docs/zh-CN/plugin-console.md](docs/zh-CN/plugin-console.md) |
| **[Privacy & Local Data (`docs/privacy.md`)](docs/privacy.md)** | Local storage, file permissions, historical audience limitations, backup, and deletion | [docs/zh-CN/privacy.md](docs/zh-CN/privacy.md) |

---

## 10. Common question: Every harness vendor already has its own memory feature — why use this too?

Tools like Claude Code, Cursor, Windsurf, and ZCode already ship built-in memory features (`CLAUDE.md` / `AGENTS.md`, Auto-Memory `MEMORY.md` notes, Rules, etc.). **We use those built-in features ourselves every day — they and Rolling Memory are not competing "either/or" choices; they complement each other like a sticky note on your desk versus a full-history black-box recorder**:

1. **Built-in auto-notes cost turns to write and only capture a tiny fraction of what happened**:
   Host auto-memory typically relies on the agent deciding mid-task that something is worth remembering and calling a file-write tool to jot down a short note. That has two catches: first, **every time the agent stops to write a note, that tool call itself costs an extra `100k–400k` round-trip**; second, **everything the agent didn't think to write down at the time** (an error stack from three days ago, a flag or file path mentioned in passing) is gone once the session closes. Rolling Memory quietly indexes completed conversation turns in local SQLite at zero token cost and zero prompt footprint at rest — when your curated notes don't have a detail, `recall` can pull the original passage from your full history.
2. **Always-on instruction files bloat the prompt if you dump everything into them**:
   `AGENTS.md` and `MEMORY.md` indexes are injected into every turn in full, which is great for a few dozen core project rules. Piling months of debugging post-mortems into them burns tokens on every turn and dilutes attention. Keeping long-tail history in an on-demand SQLite index costs `0` prompt tokens until `recall` is actually called.
3. **One shared local memory across all your harnesses (no tool lock-in)**:
   Most developers switch between Claude Code, Cursor, Codex, ZCode, or Cline. Each vendor's built-in memory is an **isolated silo** — a bug you solved in Claude Code is invisible when you open Cursor or Codex. `rolling-memory install` wires **10 harnesses to the same local SQLite database**, so a fix recorded in one tool is immediately searchable from any other.
4. **Unedited verbatim transcripts as a ground-truth fallback when summaries go wrong**:
   Built-in auto-memories are model-written summaries; if the model misunderstood the context when writing the note, that mistaken summary stays in your notes. Rolling Memory never rewrites facts — it keeps the verbatim transcript and timestamps, acting as a local "flight recorder" you and your agent can check whenever a summary looks suspicious.

---

## 11. License, contributing, and acknowledgments

- **Contributing**: Issues, suggestions, and pull requests are very welcome — see [CONTRIBUTING.md](CONTRIBUTING.md) and [SUPPORT.md](SUPPORT.md). This project uses a "human-defined invariants & acceptance criteria + AI-assisted implementation" workflow; the full disclaimer is in [DECLARATION.md](DECLARATION.md).
- **License**: Released under the **[PolyForm Noncommercial License 1.0.0](LICENSE)** (SPDX: `PolyForm-Noncommercial-1.0.0`). Full source code is free to use, modify, and share **for any noncommercial purpose**; **commercial use and commercial distribution (including SaaS hosting and commercial embedding) require a separate written commercial license**. The four-language client SDKs under `sdk/` are separately licensed under **Apache-2.0** (`sdk/LICENSE`). Third-party attributions are in [NOTICE](NOTICE).
- **Acknowledgments**: The web administration console is a derivative of **[AI GO ADMIN](https://github.com/ai-go-hub/ai-go-admin)** (Apache-2.0; Go + Gin + Vue 3 admin framework — mirrors: [Gitee](https://gitee.com/ai-go-hub/ai-go-admin) · [AtomGit](https://atomgit.com/ai-go-hub/ai-go-admin)), whose UI lineage traces back to **[BuildAdmin](https://gitee.com/wonderful-code/buildadmin)** (Apache-2.0, © 妙码生花). The plugin catalog page is ported from BuildAdmin `v2.3.9`. Per-file attribution and modification notes are registered in [NOTICE](NOTICE) §2.

Copyright © 2026 Rocareer & Rolling-Memory Organization (Albert `<albert@rocareer.com>`). All rights reserved.
