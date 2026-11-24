---
name: rolling-memory-dsh
description: Use Rolling Memory's local recall tool for relevant past context, decisions, or troubleshooting.
---

# Rolling Memory in DSH

Use `mcp__rolling-memory__recall` when a missing decision, failed attempt, or entrypoint would change your next action. Inspect known locations directly. Send `cue` with literal anchors such as paths, errors, or dates; do not pad. Verify the source and current state; a miss is not proof of absence.

Recall uses the local `local_plain` vault. Current Rolling Memory builds sync your DSH session history into that vault in the background; the earliest builds only counted sessions without importing them. An agent may send recalled text to its configured model provider when answering; review the provider's privacy settings before sharing sensitive material.
