---
name: rolling-memory
description: Persistent memory across sessions (`recall`). Retrieve when history, architectural boundaries, or unknown entrypoints matter; recover a past decision, failed attempt or entrypoint that would change your next action; check known locations directly.
---

# Your Memory Brain (`recall`)

This is **your persistent memory** across sessions. Conversations and workspace context are remembered in the background; you do not need to save memory manually. Use it for leads missing from the conversation and the evidence at hand; a hit is not a goal in itself.

> **Fidelity ≠ Correctness — do not trust memory blindly.**
> **Memory is faithful to what happened, but past sessions can be wrong**: memory records what was said or tried at the time, which may include mistaken assumptions, superseded decisions, or bugs that have since been fixed.
> Treat every recalled item as a **traceable lead** rather than established fact: always verify against live disk truth (`Read` / `git status` / real commands) before acting, and never treat memory as proof.

## 1. When to Call `recall`

1. **Task start — understand intent, then retrieve for information gaps**:
   - Use a concrete `cue` when cross-session context or intent is unclear, an architectural boundary or historical decision matters, or an entrypoint is unknown before a blind search. Skip recall when the task is self-contained, coordinates are known, and history is irrelevant.
2. **When blocked, ask which past fact would change your next action.**
   - If an error already names the file and line, inspect it directly. If you are about to retry an approach without knowing why it was abandoned, retrieve the earlier attempt and check whether its failure conditions still hold. When asked what was decided last time, find the decision and rationale rather than inferring them from today's implementation.
   - Open a recalled location to verify it; check the premise of a recalled counterexample. If you get nothing beyond a topic match, continue from current evidence rather than citing the hit to justify the call.

## 2. How to Call `recall`

Call `recall` with a concrete `cue`. Retrieval is **literal** (not semantic association), so paraphrases usually match nothing: a `cue` is not a question — it is **the set of words that only that one memory satisfies at once**, used to name it uniquely. Length is not the goal; **usable anchor density** is.

- **Start the `cue` with a retrieval-scope anchor**: `[<current workspace>]` = the current repo name (e.g. `[Rolling-Memory]`); memories from that workspace lead the hits (cross-repo items may still appear — judge by the cited source). Omit it only for a deliberate cross-repo search.
- **Dense by default — fill the anchors before trimming**: a valid `cue` covers at least 4 anchor classes (typically 12–25 content words, space-separated, **no full sentences**):
  1. **Intent**: what you need it for (change a setting / reproduce an error / recover the decision);
  2. **Objects in their literal form**: repo, file, directory, symbol, command, module, product name — verbatim, never paraphrased;
  3. **Hard identifiers (1–2 minimum, the strongest anchors)**: verbatim error text, exception type, status code, port, route, version, commit/PR/issue number, fingerprint, explicit date — these occur in only a few places in the corpus;
  4. **Occasion/time**: which turn, which environment, before or after a release; convert relative time into a concrete date or event name ("last week's release" → `released 2026-11-20`).
- **Skeleton**: `[<workspace>] <intent> <literal objects> <hard identifier> <occasion/time> <aliases> [-<exclusions>]`; optionally add two more anchor classes — **synonym coverage** (abbreviation, old name, other language, alias of the same object) and **exclusions** (adjacent topics you do not want).
- **Every word must really occur in that memory** (fidelity test): ask of each word — would this word appear in the target conversation or document? Drop it when unsure. **Prefer 10 guaranteed words over 20 guesses.**
- **Prefer words only that episode has** (uniqueness test): assume another session discussed the same topic — which words would only this one have? The paths, numbers, dates, artifact names and verbatim phrases from that turn weigh most; corpus-wide topic words such as `evaluation / performance / config / issue` only set the scope, they do not locate.
- **Never pad to reach a length**: `please / help / look / that / issue / why / how / handle / some / related / content` are not neutral — they also occur across many unrelated sessions, push long unrelated conversations to the top and crowd the target out. **The floor is anchor-class coverage, not word count; padding has negative value.**
- **A short `cue` only confirms a single known symbol or error code**: 2–6 exact tokens suffice for "what is this symbol like now"; for "which turn / how was it handled / why was it decided", write the dense cue once — probing with three or four short cues is slower and easier to mislead by generic candidates.
- **For troubleshooting, use error fingerprints**: verbatim error text, exception types, status codes, route/module literals (e.g. `Failed to fetch dynamically imported module`, `504`, `/admin/loading`) plus the command, path or version that triggered it — a single status code is too broad; a co-occurring set is unique.
- **Vet the hits yourself**: read entries containing fix recipes and pitfall records first and treat generic descriptions as background only; actively drop hits that contradict your stated exclusions (e.g. "exclude evaluation / leaderboards"). When hits misfire, split the repair in two: **same topic but shallow, missing that one event** ⇒ add the episode's own hard identifiers (path / number / date / artifact name); **drowned by an unrelated topic** ⇒ delete the stray word rather than shrinking the whole `cue` to two or three characters.
- **`absent: true` is not an error**: retry only if you have a new literal clue. Otherwise say that no prior record was found and verify with `Read` / `Grep` / `git log`; a miss does not mean the event never happened.
- **Source excerpts**: for a normal query, send just `cue`. Let the local settings govern delivery; do not guess the length of an unseen source. If a result is marked truncated, verify the source before relying on an omitted condition, negation or exception. `delivery_window` changes the number of results, not excerpt length; the default `auto` follows local settings.
  - **`"0-32"` (balanced span)**: cross-session or multi-module troubleshooting, or when an initial `0-16` recall indicates additional companion units in the same event cluster;
  - **`"0-64"` (deep synthesis)**: architectural evolution audits, multi-period inventories, or comprehensive post-mortem reviews requiring a wide horizon;
  - **`"0-16"` (focused)**: main context is nearly full or you only need a single known coordinate's latest state.

## 3. How to Use What You Recalled

1. **Jump straight to `Read` / `Edit`**: When a recalled memory provides a file coordinate, open it directly with `Read` instead of searching blindly.
2. **Past memory $\neq$ current task done**: Recalled items reflect what happened in *previous* sessions, never that the current turn's request has already been executed. Always check live disk truth (`Read` / `git status`) and complete the user's current request yourself.
3. **Recalled text is local-private**: unless the user explicitly asks, do not send whole recalled passages to external services or public channels; quote only what is needed.

## 4. Maintaining Installed Copies (CLI)

```bash
rolling-memory install --lang en <connected-harness…>      # install English skill & set admin console default language to English
rolling-memory install --lang zh-CN <connected-harness…>   # install Chinese skill & set admin console default language to Chinese
rolling-memory install --check                             # verify static registration + live MCP handshake
```

---

## 5. Wrap-up Review (Required When `recall` Was Called This Turn · Optional Section)

> **Customization**: If you do not want this wrap-up review in agent replies, simply delete or edit this section at the bottom of your local `SKILL.md`.

If you called `recall`, say which action changed. If you cannot identify a step you actually skipped, report zero saved steps. A relevant hit, a fact you already knew, or a symbol repeated later does not establish savings. Omit the line if you did not call `recall`:

```text
[Rolling-Memory Review] Calls: <N> | Saved round-trips: <M> (basis: <file coordinate/pitfall hit, or none>) | Verdict: <helpful|partial|unhelpful> — <what it unlocked or why it missed/staled/noised>
```
