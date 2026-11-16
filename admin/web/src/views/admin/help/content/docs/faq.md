# Rolling-Memory Frequently Asked Questions (FAQ)

**[English](faq.md) · [简体中文](zh-CN/faq.md) · [Back to Documentation Hub](README.md) · [Back to Repository Home](../README.md)**

This page answers the most common practical questions about how Rolling Memory works, how it compares to other tools, and where its boundaries lie.

---

## Q1: How is Rolling Memory different from `grep` / `ripgrep` or standard file search? `grep` can already do a lot!

**Short answer**: `grep` is a **scanner for literal strings in your current files when you already know what identifier to look for**, whereas Rolling Memory is a **lookup tool over your past conversations and troubleshooting history**. They work together: **Rolling Memory helps your agent find what was discussed and which files were involved in past sessions, and your agent then uses `Read` or `grep` on the live files.**

In day-to-day coding, relying on `grep` alone hits four common walls where local session memory helps:

| Dimension | Standard Code Search (`grep` / `ripgrep`) | Rolling Memory (Local Verbatim Vault + Lexical-Statistical Search) |
|---|---|---|
| **1. Remembering the symptom, not the symbol name** | **Requires guessing the exact identifier in code**: If you ask *"Where did we fix that status-bar flicker bug?"* and the variable is named `sseStreamDebounce`, `grep "flicker"` returns `0` hits, so the agent spends several turns guessing keywords with `find` and `grep`. | **Finds coordinates from past discussions**: As long as that debugging session happened in a past conversation, the vault holds the verbatim discussion and file path (e.g., `admin/.../dashboard.vue`), letting a symptom query jump straight to the file. |
| **2. "Why we didn't do X" (negative knowledge)** | **Code only shows what was kept, not why an alternative was rejected**: Current source files rarely explain *"Why did we roll back Approach A last week?"*, so a new session can easily repeat the same mistake. | **Preserves past troubleshooting & rollback notes**: Keeps the verbatim record of why an approach failed or which cross-file invariant must be maintained, so future sessions can look it up. |
| **3. Separating stale notes from recent decisions** | **Flat and timeless**: Old notes, comments, and recent changes sit side by side without a session timeline. | **Timestamped with temporal proximity weighting**: Every record carries its timestamp, and ranking weights temporal proximity and demotes cross-day duplicates. |
| **4. Connecting context across sessions** | **Isolated line matches**: Scans only current files on disk without linking multi-turn troubleshooting conversations across days. | **Session & adjacent-sentence co-occurrence**: Uses term-family grouping, two-sentence window co-occurrence, and past citation links (`cited_unit_ids[]`) to surface related passages together. |

---

## Q2: How does it differ from Vector RAG or LLM-extraction memory services (like Mem0 / Zep)?

Most agent memory services rely on **online vector embeddings** or **background LLM calls that summarize conversations and build entity graphs at write time**. Those methods have real strengths for broad paraphrase matching, but for everyday coding agents we chose **verbatim storage + local lexical-statistical retrieval** for four practical reasons:

1. **Genuinely free and real-time (zero model tax, zero background queue)**:
   - No embedding APIs (OpenAI / DashScope / Ollama), no external vector databases, and no background LLM extraction queues;
   - Ingestion and retrieval are deterministic local operations: completed turns are indexed in local SQLite in milliseconds and are searchable immediately (warm-cache retrieval averages **`29–42 ms`** per query) with a $0 background token bill.
2. **No fact-rewriting — avoiding "error compounding" from LLM summarization**:
   - Asking an LLM to summarize conversations at write time sounds appealing, but if your active agent can misread context, the summarizing LLM can misread it too — **turning a possible misunderstanding into a permanently baked-in false memory** once the raw text is discarded (and "iterative memory correction" faces the paradox of how to catch a wrong memory before it misleads later tasks);
   - Economically, saving a few dozen or a few hundred tokens at read time is less than `0.15%` of a `100k–400k` turn, yet generating that summary forces you to pay a hefty upfront LLM comprehension cost at write time (see the full math in [`token-economics.md`](token-economics.md)).
   - Our rule is simple: **what happened, happened.** We only run deterministic format cleanup (stripping protocol wrappers, `<thinking>` blocks, and duplicate logs) and store the verbatim text so a passage read today is identical to the one read next month.
3. **Dense vectors blur exact symbols, whereas AI coding sessions use consistent terminology that fits deterministic information-theoretic search**:
   - Dense embeddings map text into a smooth semantic space: great for loose synonyms, but near-identical symbols like `err_code_4012` vs. `err_code_4013`, port `8008` vs. `1828`, `v0.0.1` vs. `v0.0.2`, or `2026-05-12` vs. `2026-05-19` end up with `>0.98` cosine similarity and easily collide. Instead of dense vectors or static external vocabularies, Rolling Memory combines adaptive corpus self-information (with logarithmic folding for same-day repeated logs), deterministic cross-form alignment (dates, word inflections, compound identifiers, inline number-unit pairs), and dimensionless multi-cue coherence resonance;
   - On the public University of Cambridge **ATM-Bench** (11,034 multimodal text records), our pure local retrieval reaches an official `Recall@10` (per-question gold coverage mean) of **`77.21%`** on Main-1013 (auxiliary `Any-Hit@10` `80.45%`) and **`46.12%`** on Hard-31 (auxiliary `Any-Hit@10` `80.65%`);
   - Inspecting the ~20% of benchmark questions missed in the top 10 shows they are mainly **zero-lexical-overlap cases from casual human chat** — such as nicknames vs. full names ("Old Zhang" vs. "Zhang San"), dropped subjects, or vague spoken references (*"that project… that person… got it done"*);
   - In **coding harness workflows**, by contrast, you typically use the same model on the same repository: the AI writes well-formed prose without ungrammatical phrasing or ambiguous references, anchored on consistent module names, identifiers, file paths, and error strings. Since a frontier LLM is already sitting in front of you as your host agent, the memory layer only needs to surface verbatim evidence in tens of milliseconds and leave 100% of semantic reasoning to the host model (our team previously reached **92.79** on LoCoMo using heavyweight vector/graph/LLM architectures — leaving them out of the default local path here is a deliberate cost-benefit choice).

---

## Q3: Every harness vendor already has its own memory feature (`CLAUDE.md` / `AGENTS.md`, `MEMORY.md` auto-notes, Cursor Rules) — why use this too?

Keeping core rules in `AGENTS.md` / `CLAUDE.md` and using host Auto-Memory is great practice (we use them every day), but host built-in memory and Rolling Memory **are not "either/or" substitutes — they complement each other like a sticky note on your desk versus a full-history black-box recorder**:

1. **Host auto-notes cost turns to write and only capture the tip of the iceberg**:
   - Built-in auto-memory typically relies on the agent deciding mid-task to call a file-write tool and save a short note into `MEMORY.md`. Every time the agent stops to write a note, that tool call itself **costs an extra `100k–400k` context round-trip**; worse, everything the agent didn't think was important enough to write down at the time (an error stack from three days ago, a file path or flag mentioned in passing) vanishes when the session closes.
   - Rolling Memory quietly indexes completed conversation turns in local SQLite in the background at zero token cost and zero prompt footprint at rest. When your curated notes don't cover a detail, `recall` can pull the original passage from your full conversation history.
2. **Always-on files bloat the context window if you dump everything into them**:
   - `AGENTS.md` and `MEMORY.md` indexes are injected **in full on every single turn**, which is ideal for a few dozen core project invariants. Dumping hundreds of debugging post-mortems into them burns billed tokens on every prompt and dilutes attention (*Lost-in-the-Middle*).
   - Rolling Memory keeps thousands of historical records at **0 prompt token cost** at rest, retrieving only a few hundred tokens when a relevant question actually comes up.
3. **One shared local memory across all your harnesses (breaking vendor silos)**:
   - Developers frequently switch between ZCode, Claude Code, Codex, Cursor, Windsurf, and Cline. Each vendor's built-in memory is an **isolated silo** — a bug you solved in Claude Code is invisible when you switch to Cursor or Codex.
   - `rolling-memory install` wires 10 harnesses to a single unified local SQLite vault in one command, so a troubleshooting session in one tool is immediately searchable from all the others.
4. **Verbatim ground-truth transcripts when model-written summaries get things wrong**:
   - Host auto-memories are model-generated summaries; if the model misunderstood something when writing the note, that mistaken summary stays in your notes. Rolling Memory never rewrites facts — it preserves the verbatim passage and provenance so you can always verify what was actually said.

---

## Q4: Are my code and memories safe on my own machine? Does any third party see them?

**This release is pure local mode (`local_plain`): other than you and the agent actively working for you, no third-party memory service, embedding provider, or external server ever sees your code, file paths, or conversations.**

1. **Single-file local SQLite with `0700/0600` permission isolation**:
   - The local vault stores memory records, mounted document offsets, and search indexes in an SQLite file on your computer (default `~/.local/share/rolling-memory/memory.db`, directory `0700`, file `0600` — readable only by your OS user account), inheriting OS full-disk encryption (FileVault / LUKS / BitLocker).
2. **Zero egress in the memory plane (and a clear boundary for remote answerers)**:
   - Ingesting, indexing, and retrieving memories happen locally with zero external network requests. Historical audience restrictions may not be enforced across all sources; review recall results before sharing them.
   - To be precise about the boundary: if your coding agent is configured to use a remote LLM API (such as Claude, GPT, or DeepSeek), the passages returned by `recall` will be sent to that model provider when the agent answers your prompt.
3. **Source-in-place reads for mounted codebases (zero full-text duplication)**:
   - Mounting local code or doc directories stores only file paths and byte offsets in SQLite and reads files in place on demand, without copying your repository into an external database.
4. **Local data**:
   - The vault is an SQLite database. Referenced conversation and mounted files remain separate; see [privacy.md](privacy.md) for backup limitations.

---

## Q5: Do I need a cloud account? What about future cloud capabilities?

**No — this release is pure local mode, requires no account or registration, and makes zero external network requests.**

Our cloud memory engine is a separate capability opening progressively on an **application basis** (online registration is closed; email [pick@psychologylake.org.cn](mailto:pick@psychologylake.org.cn) to apply, or [support@psychologylake.org.cn](mailto:support@psychologylake.org.cn) for support). It is not part of this release: the local tool and local vault remain complete, usable, and source-available — see [Declaration & Disclaimer (DECLARATION.md)](../DECLARATION.md) for the release and license boundary.

---

## Q6: How can I verify your benchmark scores and token-saving math myself?

**Don't take static claims on faith — verify and falsify both on your own machine:**

1. **Bundled checksummed datasets & one-command offline reproduction**:
   - The benchmark corpora are **not** redistributed with this repository: obtain LoCoMo and Cambridge ATM-Bench from their upstream projects, point the runner at your copy with `--eval-data <dir>`, and pin it with the `SHA-256` digests published in [benchmarks.md](benchmarks.md). Both benchmarks are one command away: `rmem eval --fetch atm --accept-upstream-license` downloads the required upstream files (~35 MB) from the pinned Hugging Face revision, rebuilds the pinned corpus and tier sets on your machine, verifies them against those digests, and then runs the evaluation; `rmem eval --fetch locomo --accept-upstream-license` downloads the upstream `locomo10.json` (~2.8 MB) and rebuilds the upstream-official Cat 1–4 question set (1,540; the convenience tiers 100/200 are retired). Then run `rmem eval --suite atm --tier 1013 --answerer none --judge off --top-k 50` offline to execute retrieval and recompute official `Recall@10` and `Any-Hit@10` directly from the generated JSONL files. Corpus-dependent tests skip when no corpus is present; `rmem eval` itself exits with an actionable error rather than producing a reading out of nothing.
2. **Upfront about benchmark limitations**:
   - As noted in the README, our ranking weights and heuristics were iterated through error analysis on these benchmarks and should not be read as blind zero-shot generalization or an official leaderboard submission.
3. **Built-in evaluation hooks & our real 7-day dogfooding ledger**:
   - Local mode enables experience-evaluation hooks and evaluation cards by default (disable anytime via `rolling-memory install --mcp-only` or `rolling-memory config set hooks.ledger false`). For the exact turn-elimination math, counterexamples, and our own development machine's audited 24-hour and 7-day token/dollar ledger, see [Token Math & Falsification (`token-economics.md`)](token-economics.md).
