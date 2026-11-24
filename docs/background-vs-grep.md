# Why Use `recall` and `Background` Directory Mounts When Your Harness Already Has `grep`? (Boundaries, Value & Self-Verification)

**[English](background-vs-grep.md) · [简体中文](zh-CN/background-vs-grep.md) · [Back to Documentation Hub](README.md) · [Back to Repository Home](../README.md)**

When developers see that Rolling Memory can mount arbitrary local directories (`L1 Background`), a natural question comes up right away:

> *"If `Background` can mount any directory — say I add my code repository so `recall` can find records in it — isn't that redundant? My coding harness can already run `grep` / `ripgrep` directly. Where is the actual value, and how can you prove it?"*

That question goes straight to the core. **If you are simply looking for a known function name or an exact string inside the repository currently open in your workspace, mounting that entire codebase into `Background` to replace `grep` is not only redundant — it can actually cost you extra tokens.**

Rolling Memory is never meant to replace `grep`. This document lays out the exact boundary: **when you should just use `grep`**, **the 4 things `grep` alone cannot do**, and **3 ways you can verify the difference on your own machine in minutes**.

---

## 1. The Boundary First: When Is Calling `recall` Redundant?

As noted in the counterexamples in [`token-economics.md` §3](token-economics.md), there are clear cases where your agent should use `grep` or `Read` directly and skip `recall`:

1. **Current workspace + known exact symbol = use `grep` directly, do not call `recall`**
   - If you ask your agent *"Update line 20 of `parseConfig`"* or *"Find all callers of `NewVault` in this repo"*, a single `grep "parseConfig"` locates the live code in milliseconds with 100% precision. Calling `recall` first just wastes an extra tool-use turn.
2. **Do not blindly mount an entire giant, rapidly changing active codebase into `Background` as a generic full-text search**
   - The source files in your active workspace are already on disk for `grep`, `glob`, and `Read` to inspect directly;
   - Rolling Memory is built for **joint retrieval across `L2 Episodic` (past session history) + `L3 Noetic` (crystallized citation links) + `L1 Background` (cross-directory & rich-format reference docs)**.

So if your harness already has `grep`, where do `recall` and `Background` mounts provide real value?

---

## 2. Four Things `grep` Alone Cannot Do

| Dimension | Built-in `grep` / `ripgrep` | Rolling Memory (`L2 Episodic` + `L3 Noetic` + `L1 Background`) |
|---|---|---|
| **1. Remembering the symptom, not the symbol name** | **Requires guessing the exact literal identifier**: If the variable is named `sseStreamDebounce`, `grep "flicker"` returns `0` hits, forcing the agent to spend 3–5 turns guessing keywords with `find`, `grep`, and `Read`. | **Bridges natural-language symptoms to code coordinates via past discussions**: Because an earlier session discussed how status-bar flicker came from `sseStreamDebounce` in `dashboard.vue`, a symptom lookup returns the file path and line anchor (`#L45-82`) in one hop. |
| **2. "Why we didn't do X" (negative knowledge)** | **Code only shows what survived, not why an alternative was rejected**: Current files rarely explain *"Why did we roll back Approach A last week?"*, so a fresh session can easily repeat the same mistake. | **Preserves failed attempts & decision rationale**: Keeps the verbatim error stack, rollback reason, and cross-file invariants from past debugging sessions so future sessions don't repeat dead ends. |
| **3. Cross-repo reach & binary office documents** | **Typically limited to plain text in the current workspace**: Scans plain-text files inside the active project directory, and cannot read `.docx` / `.xlsx` / `.pptx` / `.pdf` spec files. | **Cross-directory mounts + zero-dependency binary doc parsing**: Mounts external sibling repos and spec folders, parses Word / Excel / PPT / PDF natively in Go, and reads chunks in place on demand without duplicating large files into SQLite. |
| **4. Common-term log floods vs. multi-cue ranking** | **Unranked line floods**: Searching a common domain word like `timeout` or `token` in a large repo dumps thousands of flat lines across test fixtures and comments, bloating the context window. | **Adaptive self-information & multi-cue coherence ranking**: Weights terms by live vault rarity (folding same-day repeated logs logarithmically), aligns dates/inflections/number-unit pairs, and ranks passages where multiple rare cues co-occur in a tight window. |

Here is how each of those four points plays out in practice:

### Value 1: Bridging "Natural-Language Symptoms $\rightarrow$ Exact Code Symbols" (The Primary Turn-Saver)

`grep` matches literal substrings or regular expressions. In daily work, however, you often ask questions in terms of **symptoms, behavior, or past context**:
- *"Where did we fix that status-bar flicker bug last time?"*
- *"Why does cold-start search stall on large vaults?"*

Because the function in code is named `sseStreamDebounce` or `pruneLargeVaultHydrationCandidates` — and the word "flicker" never appears in the source comment:
- **With `grep` alone**: Turn 1 runs `grep "flicker"` (`0` hits) $\rightarrow$ Turn 2 runs `glob` to list frontend directories $\rightarrow$ Turn 3 `Read`s two guessed files and finds nothing $\rightarrow$ Turn 4 tries `grep "status"`. In a typical **`100k–400k` token** long-context session, every extra trial-and-error turn re-sends that entire 100k–400k context as billed input tokens (see [`token-economics.md`](token-economics.md)).
- **With `recall`**: `recall` searches **L2 past conversations + L3 citation links + L1 mounted docs simultaneously**. As long as that bug or design was discussed before, `recall` returns the verbatim discussion and the exact file coordinate in tens of milliseconds, letting your agent `Read` the right file on the next turn and skip the blind search loop.

### Value 2: Preserving "Negative Knowledge" That Never Appears in `HEAD`

Your repository's current `HEAD` only shows **what code is there now** — it does not show **why three simpler-looking alternatives failed and were rolled back**.

For example:
- Why does the macOS updater write a temporary file in the same directory and `mv -f` instead of running `cp` over the live binary? (Because overwriting an in-use executable's inode in place triggers a macOS kernel code-signing kill.)
- Why must changing file A always be paired with regenerating file B?

When you start a new session a few days later and ask an agent to clean up that code, `grep` across the codebase cannot tell the agent why the simpler approach failed. `recall` retrieves the past troubleshooting transcript and error trace verbatim so the agent does not re-introduce the bug.

### Value 3: Where `L1 Background` Actually Shines — Cross-Repo Reference & Binary Office Docs (`.docx/.xlsx/.pptx/.pdf`)

If plain-text source files in your current workspace can already be searched with `grep`, what should you mount into `L1 Background`? It solves two physical blind spots of workspace `grep`:

1. **Mounting external directories outside your active workspace (shared specs, sibling repos, protocol docs)**
   - A coding agent's file search is normally scoped to the single project directory currently open. In real projects, however, working on a frontend repo often requires checking API contracts in a backend repo, a shared SDK directory, or a central folder of architecture notes and RFCs;
   - Mounting those external folders via `rolling-memory background add --path <dir>` lets your agent retrieve cross-repo specifications alongside past session discussions in a single `recall` call without switching workspaces.
2. **Native zero-dependency parsing for binary office documents that `grep` cannot read**
   - Product specs, API inventories, and test matrices often live in Word (`.docx`), Excel (`.xlsx`), PowerPoint (`.pptx`), or PDF (`.pdf`) files where plain `grep` only sees binary bytes;
   - Rolling Memory includes a pure-Go stream reader (`CGO_ENABLED=0`, zero external dependencies) that extracts and segments documents into **`1024-token`** chunks across:
     - **Office & paged documents**: `.docx` (paragraphs and tables), `.xlsx` (worksheet row blocks with `Sheet1:r1-15` anchors), `.pptx` (slide text with `slide3` anchors), and `.pdf` (text streams with `page2` anchors);
     - **Structured data & text/code**: `.md`, `.txt`, `.rst`, `.json`, `.jsonl`, `.csv`, `.tsv`, `.yaml`, `.yml`, `.toml`, and common source files (with exact `#L1-60` line anchors).
3. **Source-in-place live reading (zero full-text duplication bloat) + automatic stale invalidation**
   - Mounting a folder does **not** copy megabytes of raw files into SQLite. The database stores only lightweight file metadata (canonical path, `mtime`, file size, content `SHA-256`, and chunk offset indexes);
   - When `recall` hits a chunk, it reads the passage live on demand from the original file on disk in milliseconds;
   - During incremental sync (`rolling-memory background sync`), unchanged files are skipped in $O(1)$ via `os.Stat`. When a file changes, only modified chunks are re-indexed, and any local `L3 Noetic` records that cited the old chunks are automatically marked `stale` so outdated citations never mislead future sessions.

### Value 4: Multi-Cue Information-Gain Ranking vs. Unranked `grep` Line Floods

When investigating an issue that spans multiple concepts (for example, `timeout`, `sqlite`, `busy`, or a date paired with an error code):
- `grep` scans line by line for a single pattern; searching a common term dumps hundreds or thousands of flat matches across test fixtures, comments, and logs, flooding the context window;
- Rolling Memory's local retrieval kernel combines **adaptive corpus self-information (with logarithmic folding for same-day repeated log bursts)**, **deterministic cross-form alignment (bridging natural-language dates like `"May 12, 2025"` with ISO `"2025-05-12"`, word inflections, compound identifiers, and inline number-unit pairs like `"500ms"` or `"16GiB"`)**, and **multi-cue coherence resonance**, ranking the passages where multiple high-information cues co-occur at the top.

---

## 3. How Can You Verify This Yourself? (Three Reproducible Proofs)

You do not need to take any claim on faith. You can verify or falsify the value directly on your own machine in three ways:

### Proof 1: The Break-Even Formula & Built-in Zero-Model Post-Hoc Evidence-Reuse Audit

In [`token-economics.md`](token-economics.md), we write out the exact break-even inequality for calling `recall`:

$$p \cdot k > 1 + \frac{m \cdot R}{C}$$

(where $C$ is your active context length such as `200k` tokens, $R$ is the returned passage size, $p$ is the effective hit rate, and $k$ is the number of blind search turns saved on a hit).

To cross-check the effective hit rate $p$ independently of self-reports, Rolling Memory runs a **100% deterministic, zero-LLM post-hoc anti-echo evidence-reuse audit**:
- **How it works**: Whenever `recall` returns file paths or symbol anchors, the local auditor inspects the session's **subsequent tool inputs and reply text** (after stripping text that merely echoes the query):
  - If a returned file path or symbol appears in a later `Read` / `Edit` tool input, it is recorded as **`adopted_tool` (tool-input match)**;
  - If unique facts from the recalled passage appear in a later reply, it is recorded as **`adopted_context` (reply match)**;
  - If the settled follow-up window contains no delivered anchor, it is recorded as **`unadopted` (no observed match)**. This is an observational result only: no match does not prove the recall was useless, and a match does not prove a step was saved.
- **Observed numbers from our primary development machine**:
  - Under real coding workloads averaging `272.0k–320.1k` input tokens per call, our longer retained ledger (`440` settled recall outcomes) shows an observed evidence-reuse share of **`61.1%` (`269/440`)** — the share of settled recalls whose delivered anchors appeared in later tool inputs or replies — and the most recent 24-hour window reached **`89.1%` (`41/46`)**;
  - Across `33` deduplicated per-turn review cards in the last 24 hours, **`6` (18.2%) honestly reported `0` saved turns**, while **`27` (81.8%) pinpointed exact file coordinates or past fixes and saved `116` blind search turns (`≈4.3` turns saved per hit)** — projecting to **`791` net saved long-context turns per week (`~158.2M` input tokens)** on a single heavy developer machine (full breakdown and pricing tables in [`token-economics.md` §6](token-economics.md)).
- **Check it on your own machine**: Run `rolling-memory panel` to open the local Web Console ([http://localhost:8008/#/admin](http://localhost:8008/#/admin) $\rightarrow$ ROI Monitor) and inspect every single `recall` call in your own sessions to see whether its delivered anchors reappeared in later tool inputs/replies (a match), or whether the observation window is still pending. Neither result alone proves a step was saved.

### Proof 2: Offline One-Command Reproduction on Public Academic Benchmarks

Simple substring matching or plain BM25 degrades sharply on multi-cue, long-horizon questions. On the University of Cambridge **ATM-Bench** (11,034 personal multimodal records) and **LoCoMo** (1,540 long-dialogue questions), this exact local vector-free, LLM-free retrieval kernel achieves:
- **ATM-Bench Main (1,013 questions)**: Official `Recall@10` (per-question gold coverage mean) **`82.77%`** (auxiliary `Any-Hit@10`: `85.29%`; current product arm, re-verified 2026-11-20);
- **ATM-Bench Hard (31 multi-hop questions)**: Official `Recall@10` **`52.43%`** (auxiliary `Any-Hit@10`: `80.65%`);
- **LoCoMo Full Set (1,540 questions)**: End-to-end judge accuracy **`85.26%`**.

Anyone can fetch the `SHA-256`-pinned upstream corpus with a single command, run the evaluation completely offline, and recompute every metric from the raw per-question JSONL output (see [`benchmarks.md`](benchmarks.md)):

```bash
# 1. Fetch the upstream corpus and verify SHA-256 pins locally (~35 MB)
rmem eval --fetch-only --fetch atm --accept-upstream-license

# 2. Run Main-1013 and Hard-31 retrieval completely offline (0 LLM calls, 0 network requests)
rmem eval --suite atm --tier 1013 --answerer none --judge off --top-k 50
rmem eval --suite atm --tier hard --answerer none --judge off --top-k 50
```

### Proof 3: A 10-Second Side-by-Side Test (`grep` vs. `recall`) on Your Own Machine

After using the tool for a few days, pick a question that **describes a bug symptom or past design reason without naming the code identifier** and run both side by side in your terminal:

```bash
# 1. Search the codebase with grep for a symptom description — typically 0 hits or unrelated comments
grep -rn "status-bar flicker" .

# 2. Run rmem recall with the same phrase — returns the past debugging discussion, reason, and exact file path #Lx-y
rmem recall --cue "status-bar flicker"
```

---

## 4. Practical Best Practices: How to Combine `grep`, `recall`, and `Background`

1. **Looking up a known function name, variable, or exact string in the current repo $\rightarrow$ let your agent use `grep` / `ripgrep` directly**
   That is what `grep` is built for — fast, exact, and zero extra turns.
2. **Looking up past bug symptoms, "why we didn't do X", or cross-session context $\rightarrow$ call `recall`**
   Use a single millisecond local `recall` to turn a vague symptom into an exact `file_path#Lx-y` coordinate and past rationale, then use `Read` or `grep` on the live code.
3. **Keep `Background` mounts focused and high-signal**
   Mount **architecture and product spec folders, reference directories containing `.docx` / `.xlsx` / `.pptx` / `.pdf` files, or external sibling repositories** that your agent needs to reference across workspaces:
   ```bash
   # Mount an external specification folder or sibling protocol repo
   rolling-memory background add --path ~/work/shared-specs

   # Inspect mounted directories and run incremental sync
   rolling-memory background list
   rolling-memory background sync
   ```
