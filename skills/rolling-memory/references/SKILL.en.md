---
name: rolling-memory
description: Persistent memory across sessions (`recall`). Retrieve when history, architectural boundaries, or unknown entrypoints matter; recall when stuck or facing an error, then verify current facts.
---

# Your Memory Brain (`recall`)

This is **your persistent memory** across sessions. Conversations and workspace context are remembered in the background; you do not need to save memory manually. Understand the user's intent first and retrieve when information gaps make recall useful; recall before guessing when a task gets stuck.

> **Fidelity ≠ Correctness — do not trust memory blindly.**
> **Memory is faithful to what happened, but past sessions can be wrong**: memory records what was said or tried at the time, which may include mistaken assumptions, superseded decisions, or bugs that have since been fixed.
> Treat every recalled item as a **traceable lead** rather than established fact: always verify against live disk truth (`Read` / `git status` / real commands) before acting, and never treat memory as proof.

## 1. When to Call `recall`

1. **Task start — understand intent, then retrieve for information gaps**:
   - Use a concrete `cue` when cross-session context or intent is unclear, an architectural boundary or historical decision matters, or an entrypoint is unknown before a blind search. Skip recall when the task is self-contained, coordinates are known, and history is irrelevant.
2. **Task Stuck — Mandatory Obstacle Recall**:
   - Whenever you get stuck mid-task — a command/build/test fails, a counter-intuitive error appears, a symbol or file path is missing, or you are unsure how something was done before — **immediately call `recall`** with the error token, symbol, or concept as `cue` before guessing or retrying.

## 2. How to Call `recall`

Call `recall` with a concrete `cue` — five rules:

- **Start the `cue` with a retrieval-scope anchor**: `[<current workspace>]` = the current repo name (e.g. `[Rolling-Memory]`); memories from that workspace lead the hits (cross-repo items may still appear — judge by the cited source). Omit it only for a deliberate cross-repo search.
- **Query with the original words, not paraphrases**: retrieval matches literally (no semantic association) — synonyms and rewordings often match nothing. Put only words that **the thing you are actually asking about** would contain — file/symbol names, verbatim errors, status codes, proper nouns. **Do not carry loosely related buzzwords**: an unrelated topic's word boosts unrelated memories and pushes out the recent ones you actually want.
- **For troubleshooting, use error fingerprints**: verbatim error text, exception types, status codes, route/module literals (e.g. `Failed to fetch dynamically imported module`, `504`, `/admin/loading`) instead of "stuck / won't open / slow".
- **Vet the hits yourself**: read entries containing fix recipes and pitfall records first and treat generic descriptions as background only; actively drop hits that contradict your stated exclusions (e.g. "exclude evaluation / leaderboards"); when hits are flooded by an unrelated topic, **drop that word and re-query with a smaller word set** instead of adding more words to the `cue`.
- **`absent: true` is not an error**: it only means that exact phrasing matched nothing. Rephrase once; if still absent, say plainly that no prior memory was found and switch to physical verification (`Read` / `Grep` / `git log`). Never fabricate past memories.

## 3. How to Use What You Recalled

1. **Jump straight to `Read` / `Edit`**: When a recalled memory provides a file coordinate, open it directly with `Read` instead of searching blindly.
2. **Past memory $\neq$ current task done**: Recalled items reflect what happened in *previous* sessions, never that the current turn's request has already been executed. Always check live disk truth (`Read` / `git status`) and complete the user's current request yourself.

## 4. Maintaining Installed Copies (CLI)

```bash
rolling-memory install --lang en <connected-harness…>      # install English skill & set admin console default language to English
rolling-memory install --lang zh-CN <connected-harness…>   # install Chinese skill & set admin console default language to Chinese
rolling-memory install --check                             # verify static registration + live MCP handshake
```

---

## 5. Wrap-up Review (Required When `recall` Was Called This Turn · Optional Section)

> **Customization**: If you do not want this wrap-up review in agent replies, simply delete or edit this section at the bottom of your local `SKILL.md`.

Whenever you called `recall` in this turn, append the following structured review line at the end of your final reply (omit if `recall` was not called; fill in candidly without boilerplate praise):

```text
[Rolling-Memory Review] Calls: <N> | Saved round-trips: <M> (basis: <file coordinate/pitfall hit, or none>) | Verdict: <helpful|partial|unhelpful> — <what it unlocked or why it missed/staled/noised>
```
