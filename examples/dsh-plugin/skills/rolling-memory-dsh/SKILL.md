---
name: rolling-memory-dsh
description: Use Rolling Memory's local recall tool for relevant past context, decisions, or troubleshooting.
---

# Rolling Memory in DSH

Use `mcp__rolling-memory__recall` when prior context, a decision, or an unfamiliar entry point would help. Search with a dense cue built from the target memory's own literal words — intent, object/file/symbol names, 1–2 hard identifiers (verbatim error, status code, version, number, date) and the occasion, typically 12–25 content words; never pad with generic fillers (they pull unrelated long sessions forward). Verify any result against current files and commands. An empty result means no matching memory was found.

Recall uses the local `local_plain` vault. Current Rolling Memory builds sync your DSH session history into that vault in the background; the earliest builds only counted sessions without importing them. An agent may send recalled text to its configured model provider when answering; review the provider's privacy settings before sharing sensitive material.
