# How Local Memory Saves (or Costs) Tokens and Time: The Turn-Elimination Math

**[English](token-economics.md) · [简体中文](zh-CN/token-economics.md) · [Back to Documentation Hub](README.md) · [Back to Repository Home](../README.md)**

Many memory tools advertise a single headline number (such as "saves 70% of tokens"). Anyone who uses AI coding agents (Claude Code, Cursor, Codex, ZCode, etc.) every day knows reality is more nuanced: sometimes a memory lookup points you straight to the right file and saves several rounds of trial-and-error; other times it returns nothing useful and costs an extra tool call.

Instead of a marketing percentage, this page lays out the arithmetic in plain terms: **how a local memory hit actually saves tokens and wall-clock time in long-context workflows, and under what conditions that claim is falsified (where calling memory costs more than it saves).**

---

## 1. The basic fact: what does one extra agent turn actually cost?

Large language models are stateless. When a coding agent works through a multi-step task, it does not send only your latest sentence; it runs in a **tool-use loop**:

- **Turn 1**: Sends `[system prompt + AGENTS.md + your question]` (say, `20,000` initial tokens) $\rightarrow$ the model thinks and calls a tool;
- **Turn $t$** (mid-way through a long-horizon task, where real-world active contexts typically sit between **$C_t = 100\text{k}$ and $400\text{k}$ tokens**; let's use **$200,000$ tokens** as a concrete midpoint):
  Every time the agent takes one more turn to run another `grep` or `Read`, the client must **re-send the entire current context $C_t$ (`200,000` tokens) as billed input tokens**, plus the model's output tokens ($O_t$), and append the new tool output ($\Delta C_t$) to every subsequent turn.

> **What about Prompt Caching?**
> Even when prompt caching hits, cached input tokens are still billed proportional to the full context length $C_t$ on every turn (typically 10%–25% of the base rate, and 100% on cache misses or TTL expiry), and every extra turn still pays the full wall-clock latency of a network round-trip, model generation, and tool execution (often 3–15 seconds per turn).

This leads to a simple observation:

> **In a long-context session, the main cost of an extra exploratory tool call is not the few hundred tokens returned by the tool — it is re-reading the entire accumulated context $C_t$ one more time.**

---

## 2. When a memory hit saves tokens and time (with a 100k–400k long-horizon context)

In real long-horizon coding tasks, active context sizes typically sit between **`100k` and `400k` tokens**. Let's use **$C_t = 200,000$ tokens** as our working example. Suppose you ask the agent: *"Where did we fix that status-bar flicker last time?"*

### Scenario A: Without memory (blind exploration turns)

Because the variable in code is named `sseStreamDebounce` rather than `flicker`, the agent typically searches by trial and error:
1. **Turn 1**: Runs `grep "flicker"` $\rightarrow$ 0 hits (re-reads `200,000` tokens, takes ~6s);
2. **Turn 2**: Lists frontend directories with `find / glob` $\rightarrow$ gets file lists (re-reads `200,500` tokens, takes ~6s);
3. **Turn 3**: Reads two candidate files $\rightarrow$ wrong files (re-reads `203,000` tokens, takes ~9s);
4. **Turn 4**: Runs `grep "status"` $\rightarrow$ finally locates `dashboard.vue` (re-reads `206,000` tokens, takes ~7s).

Just to locate the right file, those 4 exploratory turns billed roughly **`809,500` input tokens** across the loop and took **28 seconds**.

### Scenario B: One `recall` hit

1. **Turn 1**: The agent calls `recall(cue="status-bar flicker")` $\rightarrow$ local SQLite returns the past debugging note (~`400` tokens) in ~30 ms, pointing directly to `sseStreamDebounce` in `dashboard.vue` (re-reads `200,000` tokens, takes ~6s);
2. **Next**: The agent jumps straight to `Read` on `dashboard.vue`, skipping the 2–3 blind search turns in between.

**The math**:
- **Even if a memory hit eliminates just 1 blind turn**, in a `100k–400k` long-horizon session it saves **one full re-read of the current context (`100,000–400,000` input tokens — `200,000` tokens at the midpoint)** and **5–10 seconds** of waiting;
- **If it eliminates 2–3 blind turns**, it saves **`200,000–1,200,000` input tokens** and 10–25 seconds;
- And because Rolling Memory runs locally on SQLite with zero LLM calls and zero embedding API calls, **the memory lookup itself adds $0 in background model or vector fees**.

That is what we mean by "every saved turn counts": in a `100k–400k` long-context workflow, avoiding even one unnecessary round-trip saves a full context's worth of input tokens.

---

## 3. When this claim is falsified (when calling memory loses tokens)

An honest accounting must also state when calling memory makes things **worse**. Our claim is falsified whenever any of the following three conditions holds:

### Counterexample 1: `recall` misses (or returns irrelevant noise)

If the answer is not in your memory vault, or lexical retrieval fails to match the wording:
- The agent spends **1 turn** calling `recall` (re-reading $C_t = 200,000$ tokens and injecting, say, $R = 600$ tokens of unhelpful passages into the context);
- Having found nothing useful, the agent **still has to run the same 3–4 `grep` turns anyway**.
- **Net result (falsified)**: You saved zero turns, and **lost `100k–400k` input tokens (`200,000` at the midpoint) for the wasted `recall` turn plus `600` tokens carried into every subsequent turn**, plus ~6 seconds of latency.

### Counterexample 2: Short context when the exact symbol is already known

If you are at the start of a fresh session ($C_t = 3,000$ tokens) and ask *"Update line 20 of `parseConfig`"*:
- A single `grep "parseConfig"` will find it immediately;
- Calling `recall` first adds an unnecessary turn and cannot save anything.

### Counterexample 3: Bloated memory injection over dozens of remaining turns

If a memory tool dumps $R = 5,000$ tokens of verbose, unfiltered transcript into your context, and the long-horizon session continues for another $m = 40$ turns:
- Those `5,000` extra tokens are re-sent 40 times, adding $40 \times 5,000 = 200,000$ input tokens — wiping out the savings of eliminating a single `200k` turn!

---

## 4. The break-even equation (how to test it yourself)

Putting both sides together:
- Let $C$ be the active context size (in tokens, typically `100,000–400,000` in long-horizon tasks) when `recall` is called;
- Let $R$ be the number of tokens returned by `recall` and kept in context across $m$ remaining turns in the session;
- Let $p \in [0, 1]$ be the effective hit rate (how often `recall` actually provides a useful coordinate or answer);
- Let $k \ge 1$ be the average number of blind exploratory turns avoided when a hit occurs.

Then the **expected net token savings per `recall` call** is:

$$\mathbb{E}[\Delta \text{Tokens}] \approx \underbrace{p \cdot k \cdot C}_{\text{turns saved on a hit}} - \underbrace{\bigl(C + m \cdot R\bigr)}_{\text{cost of the recall turn + injected payload}}$$

For `recall` to be a net win ($\mathbb{E}[\Delta \text{Tokens}] > 0$), we need:

$$p \cdot k > 1 + \frac{m \cdot R}{C}$$

- **When it wins**: In a `100k–400k` long context (say $C = 200,000$) with compact denoised delivery ($R = 500$, $m = 10$, so $\frac{mR}{C} = \frac{5,000}{200,000} = 0.025$ — only a **2.5%** payload overhead), if the agent calls `recall` selectively when facing genuine historical gaps (say, $p = 50\%$ effective hit rate) and a hit saves an average of $k = 2.5$ blind turns, then $p \cdot k = 1.25 > 1.025$ — an average net saving of ~**`45,000` tokens per call**.
- **When it loses**: If an agent calls `recall` mechanically on every prompt regardless of need (driving the effective hit rate down to $p = 10\%$), then even if a hit saves 3 turns ($p \cdot k = 0.3 < 1$), it will lose tokens overall.

---

## 5. Side note: Why not pay an LLM to summarize memories at write time?

A natural question is: if an LLM condenses each memory into a short one-line summary at write time, doesn't that make the retrieved payload $R$ smaller by a few dozen or a few hundred tokens when consumed later?

**Yes — saving a few dozen or a few hundred tokens at consumption time is the one obvious benefit of LLM extraction.** But once you compare that against `100k–400k` per-turn contexts, the trade-off rarely pays for itself:

1. **The consumption-time saving is tiny relative to the turn**: In a `100k–400k` turn, the difference between delivering $R = 500$ tokens of denoised verbatim text and $R = 200$ tokens of LLM-extracted summary is `300` tokens — less than **0.15%** of a single turn's context.
2. **Yet you have to pay an upfront LLM comprehension cost at write time**: To get that small future reduction (on memories that may never even be queried again), you must pay an LLM to read and summarize the context first:
   - **If the active agent writes notes mid-task**: Having your coding agent call a `remember` / `save_memory` tool during its work loop **immediately costs one extra `100k–400k` tool-use round-trip** — spending 200,000 input tokens right now to maybe save 300 tokens later;
   - **If a sidecar LLM extracts memories in the background**: Feeding conversation context into a background LLM on every turn (plus running iterative memory-update prompts) bills you continuously for a second model to read and digest your history.

That is why Rolling Memory neither exposes a manual "write memory" tool to the agent nor runs a background LLM extractor — it captures and denoises records deterministically on-host at zero token cost.

---

## 6. Our own 7-day (and 24-hour) real usage report

Formulas are abstract, so here is the actual telemetry from our primary development machine over the past week (`2026-11-05` to `2026-11-12`) and the past 24 hours (`2026-11-11 ~18:25` to `2026-11-12 ~18:25`, UTC+8) — pulled straight from our local SQLite databases, the shared hook log, and the live local adoption audit, **including every miss, irrelevant hit, and error**.

### 6.1 Real context size on our machine (why `200k` per turn is conservative)

From our host's local `model_usage` table over the last 7 days:
- Total LLM API requests across our coding sessions: **`110,340` calls**;
- Measured **average input context per call: `272,001` tokens (~`272.0k` tokens/turn**, with `227,781` cached input tokens on average); the last 24 hours ran even higher at **`320.1k` tokens/call** (`14,783` requests).
- Using **`200,000` tokens (`200k`) per turn** below is therefore ~26% below our actual 7-day average (and ~38% below the last-24h average) — conservative in the right direction.

### 6.2 7-day and 24-hour call & hook telemetry

Our workflow is now fully agent-driven: the agent calls MCP `recall` on demand when it lacks coordinates or hits an error, while the prompt hook is kept as an optional, silent safety net (the boot-time auto-injection era ended on Nov 8):

| Metric (Data source) | Last 7 Days | Last 24 Hours | Notes |
|---|---:|---:|---|
| **Agent-initiated MCP `recall` calls** (host `tool_usage` table) | **1,108 calls** (across **448 sessions**) | **80 calls** (across **45 sessions**) | `1,094` completed returns in 7d, `14` errors; last 24h: `74` completed, `6` errors |
| **`UserPromptSubmit` prompt-hook runs** (`rolling-memory.log`, retained) | **536 runs** | **37 runs** | The hook is optional (the installer wires it unless you pass `--mcp-only`); after a re-wire on `11-12 16:24` it resumed as a pure silent safety net (all `37` runs in the last 24h exited silently; last run `11-12 18:12`) |
| ↳ **Injected a 1-line context hint (`injected > 0`)** | `152 runs` | `0 runs` | Fires only when a high-confidence band is found |
| ↳ **Silent no-hit (`absent = 1`)** | `383 runs` | `37 runs` | Exits silently when no high-specificity match is found (0 context tokens added) |
| ↳ **Session deduplication (`dedup = 1`) / 4-turn cadence nudge (`cadence = 1`)** | `1 / 0 runs` | `0 / 0 runs` | Dedup skips bands already injected earlier in the same session; cadence emits a 1-line hint after consecutive misses |

### 6.3 The strictest audit: 33 turn-review cards + a model-free adoption audit (last 24 hours)

Self-reports need a standard, and ours is strict: whenever an agent uses `recall` during a reviewed turn, its wrap-up must include a three-part honesty card — ① how many calls it made, ② how many blind turns it estimates were saved, *with the exact file path or past pitfall as proof*, ③ whether the turn had positive value. The iron rule: **if the agent cannot name the concrete coordinates, it must report `0` saved turns.** (With the local-mode rewrite the card discipline is guided by the installed tool/skill instructions; the earlier Stop-hook reminder has been retired.)

Our host session store keeps a rolling window of recent conversation data; across the last 24 hours (`2026-11-11 ~18:25` to `2026-11-12 ~18:25`, UTC+8) we found **33 deduplicated cards**:

| Evaluation Outcome | Last 24 Hours (33 cards) | What actually happened |
|---|---:|---|
| **Honestly reported `0` saved turns** | **6 (18.2%)** | Two recall calls timed out under heavy background load; four turns needed no call at all — the session-loaded memory index already contained the exact coordinates (rated helpful, `0` measured turns) |
| **Verified positive hits (with concrete file/pitfall proof)** | **27 (81.8%)** | Saved **`116` blind search turns in total** (≈`4.3` per hit, range `2–6` turns) |

A few verbatim examples from our database (both wins and misses):
- **Real win #1** (`11-12 15:23`): *"2 calls, saved 5 turns — following the ROI fact-ledger architecture and host-DB index characteristics from earlier recalls, we pinpointed the cross-day scan bottleneck on a `21 GB`-WAL host database and closed the incremental-watermark loop, avoiding repeated blind index experiments."*
- **Real win #2** (`11-12 14:41`): *"3 calls, saved 6 turns — recall-established benchmark/menu contract coordinates led in one step to the menu-weight code, the default state, and the bilingual YAML dictionaries."*
- **Real win #3** (`11-12 13:58`): *"3 calls, saved 6 turns — earlier recalls supplied the pointer that located the mis-mapped archive-path root cause."*
- **Real miss #1** (`11-12 12:06`): *"2 calls, saved turns: 0 — both early calls timed out under heavy background load; locating was done by direct source reads and live logs. No direct gain this time."*
- **Served without a call** (`11-12 14:48`): *"0 calls, 0 saved — the session-loaded memory index already listed the two exact ledger filenames, so no lookup was needed (rated helpful)."*

**Model-free cross-check — the deterministic post-hoc adoption audit.** Independently of self-reports, every recall's delivered novel anchors (file paths, symbols) are later intersected, at zero model cost, with what the agent actually did next — an anchor appearing in a subsequent tool call counts as `adopted_tool`, in the reply text as `adopted_context` — after stripping text that merely echoes the query. In the **last 24 hours, 46 recall outcomes were settled by the audit and 41 (89.1%) were adopted** (`38` direct file/symbol coordinates + `3` context facts); across the longer retained ledger the same audit reads **`269/440 = 61.1%`** — the most recent window is materially more helpful than the longer-run average. (Snapshot `2026-11-12 ~18:25`, UTC+8; the audit runs live in the local console and costs no model calls.)

### 6.4 Extrapolating a full 7-day week from the last 24 hours & dollar savings under latest official pricing (GPT-6 / Claude Fable / Gemini 4)

Because our review-card discipline covers the retained recent window most completely, **multiplying the audited 24-hour pace by 7 (`24h × 7`)** gives the cleanest single-protocol projection of a full week of heavy development:

1. **7-day turn and token projection from the audited 24-hour window (at `200k` tokens / turn)**:

| Accounting Basis | Audited Last 24 Hours | Projected 7-Day Turns (`24h × 7`) | Projected 7-Day Tokens (at `200k` / turn) | Definition |
|---|---:|---:|---:|---|
| **① Gross Saved** | `116 turns` | **`812 turns`** | **`162.4M tokens`** | Blind search turns avoided across the 27 verified positive cards |
| **② Practical Net Saved** *(50% of zero-turn cards assumed to run in parallel)* | `113 turns` | **`791 turns`** | **`158.2M tokens`** | Deducts the ~half of the 6 zero-turn cards that plausibly ran alongside other read tools |
| **③ Strictest Net Saved** *(100% of zero-turn cards deducted as full turns)* | `110 turns` | **`770 turns`** | **`154.0M tokens`** | Worst-case floor assuming every zero-turn card cost one full standalone `200k` turn |

*(Note: per-turn saved counts are the agents' own honesty-standardized estimates; the deterministic adoption audit in §6.3 above provides the model-free cross-check.)*

2. **Projected 7-day dollar savings under official flagship pricing (`GPT-6` / `Claude Fable` / `Gemini 4`)**:

To keep the dollar math completely transparent, we report three pricing rates for each model:
- **Standard Input**: Official uncached input rate (applies on cold turns, cache misses, or after cache TTL expiry);
- **Blended Real-World Rate (85% Cache Read + 15% Write/Uncached)**: Weighted by our machine's actual measured ratio across `110,340` calls (~85% cached input reads and ~15% cache writes / uncached input);
- **Pure Cache Read Floor**: Assuming 100% of avoided turns would have hit the lowest cached-read rate.

| Flagship Model (Official API Pricing · USD per 1M Tokens) | Official Input Rates (Standard / Cache Read / 85% Blended) | **7-Day Gross Saved**<br>(`162.4M` tokens) | **7-Day Practical Net**<br>(`158.2M` tokens) | **7-Day Strictest Net**<br>(`154.0M` tokens) |
|---|---|---|---|---|
| **OpenAI `GPT-6 Astra`** ($\le 272\text{K}$ Short)<br>*(`developers.openai.com/api/docs/pricing`)* | Std `$10.00`<br>Cache `$1.00` (Write `$12.50`)<br>85% Blend **`$2.73 / M`** | • Standard: **`$1,624`**<br>• 85% Blended: **`$443`**<br>• Pure Cache: `$162` | • Standard: **`$1,582`**<br>• 85% Blended: **`$431`**<br>• Pure Cache: `$158` | • Standard: **`$1,540`**<br>• 85% Blended: **`$420`**<br>• Pure Cache: `$154` |
| **OpenAI `GPT-6 Astra`** ($> 272\text{K}$ Long Context)<br>*(Note: our last-24h average `320.1k` falls in this tier; the 7-day average `272.0k` sits right at the 272K tier boundary)* | Std `$20.00`<br>Cache `$2.00` (Write `$25.00`)<br>85% Blend **`$5.45 / M`** | • Standard: **`$3,248`**<br>• 85% Blended: **`$885`**<br>• Pure Cache: `$325` | • Standard: **`$3,164`**<br>• 85% Blended: **`$862`**<br>• Pure Cache: `$316` | • Standard: **`$3,080`**<br>• 85% Blended: **`$839`**<br>• Pure Cache: `$308` |
| **OpenAI `GPT-6.1 Sol` / `GPT-6 Sol`** ($\le 272\text{K}$) | Std `$2.00`<br>Cache `$0.10` (Write `$2.50`)<br>85% Blend **`$0.46 / M`** | • Standard: **`$325`**<br>• 85% Blended: **`$74.7`**<br>• Pure Cache: `$16.2` | • Standard: **`$316`**<br>• 85% Blended: **`$72.8`**<br>• Pure Cache: `$15.8` | • Standard: **`$308`**<br>• 85% Blended: **`$70.8`**<br>• Pure Cache: `$15.4` |
| **Anthropic `Claude Fable 5.2`** (Latest Flagship)<br>*(`claude.com/pricing`)* | Std `$8.00`<br>Cache `$0.20` (Write `$10.00`)<br>85% Blend **`$1.67 / M`** | • Standard: **`$1,299`**<br>• 85% Blended: **`$271`**<br>• Pure Cache: `$32.5` | • Standard: **`$1,266`**<br>• 85% Blended: **`$264`**<br>• Pure Cache: `$31.6` | • Standard: **`$1,232`**<br>• 85% Blended: **`$257`**<br>• Pure Cache: `$30.8` |
| **Anthropic `Claude Fable 5.1` / `Fable 5`** | Std `$10.00`<br>Cache `$0.25` (Write `$12.50`)<br>85% Blend **`$2.09 / M`** | • Standard: **`$1,624`**<br>• 85% Blended: **`$339`**<br>• Pure Cache: `$40.6` | • Standard: **`$1,582`**<br>• 85% Blended: **`$330`**<br>• Pure Cache: `$39.5` | • Standard: **`$1,540`**<br>• 85% Blended: **`$321`**<br>• Pure Cache: `$38.5` |
| **Google `Gemini 4 Argon`** (Standard tier)<br>*(`aistudio.google.com/docs/pricing`; 50% promo thru Nov 30)* | Std `$4.00` (Promo `$2.00`)<br>Cache `$0.20` (Promo `$0.10`)<br>85% Blend **`$0.77 / M`** (Promo **`$0.39`**) | • Standard: **`$650`** (Promo **`$325`**)<br>• 85% Blended: **`$125`** (Promo **`$63.3`**)<br>• Pure Cache: `$32.5` (Promo `$16.2`) | • Standard: **`$633`** (Promo **`$316`**)<br>• 85% Blended: **`$122`** (Promo **`$61.7`**)<br>• Pure Cache: `$31.6` (Promo `$15.8`) | • Standard: **`$616`** (Promo **`$308`**)<br>• 85% Blended: **`$119`** (Promo **`$60.1`**)<br>• Pure Cache: `$30.8` (Promo `$15.4`) |

That is the honest picture from our own daily use: **it is not magic — 6 of the 33 reviewed turns (18.2%) honestly reported `0` saved turns; but because it runs locally at `0` model cost, the other 27 turns point straight at the right file or past fix, projecting to `812` avoided blind turns per week (`791` practical-net turns) and `162.4M` gross tokens saved (`158.2M` practical net / `154.0M` strictest net). On flagship models like `GPT-6 Astra` or `Claude Fable 5.2`, even after accounting for an 85% prompt-cache hit rate, that translates to roughly `$74.7–$885` per week in real blended savings on a single active developer machine (`$61.7–$862` on the practical-net basis; `$325–$3,248/week` at standard uncached input rates), plus dozens of minutes of avoided waiting.**

### 6.5 How to disable evaluation hooks anytime

In local mode, evaluation hooks and evaluation cards are enabled by default so you can experience and audit these hits firsthand. If you prefer a completely quiet setup, you can disable them at any time:

```bash
# Option 1: Wire only the pure MCP recall tool (removes all hooks and instruction blocks)
rolling-memory install --mcp-only

# Option 2: Keep the install, but disable boot-context injection or hook ledger recording
rolling-memory config set hooks.boot_context false
rolling-memory config set hooks.ledger false
```

---

## 7. What we are working on (and how you can help)

This break-even equation $\left(p \cdot k > 1 + \frac{m \cdot R}{C}\right)$ is the exact reason Rolling Memory is built the way it is, and it defines our roadmap:

1. **$0 model and service overhead**: No background LLM extraction, no paid embedding APIs, and no cloud hosting fees, so there is no hidden token bill outside the equation.
2. **Selective, on-demand `recall`**: Instead of stuffing memory into every prompt, the agent is instructed to call `recall` only when it lacks coordinates or hits an error.
3. **Better denoising (smaller $R$) and higher recall accuracy (higher $p$)**:
   - We are actively improving **structural denoising** (stripping tool-output floods, `<thinking>` blocks, and duplicate logs) to keep $R$ as compact as possible;
   - **We are continuing to refine local lexical-statistical ranking** to raise the hit rate $p$. Note also why coding harnesses are a favorable workload for $p$: in casual human chat benchmarks, lexical search misses items with zero surface-word overlap (nicknames vs. full names, dropped subjects, or vague phrases like *"that project… that person… got it done"*). In daily **coding-agent work**, by contrast, using the same model on a concrete project produces consistent terminology, file paths, and well-formed prose without ungrammatical phrasing or ambiguous references — fitting a lexical-statistical architecture naturally.

**We do not promise that Rolling Memory will save tokens on every task.** You can use the formula above to audit its real impact on your own workflows — and if you find cases where it misses or injects too much noise, please open an issue, share your suggestions, or contribute code so we can improve it together.
