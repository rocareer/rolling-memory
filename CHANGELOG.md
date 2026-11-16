# Changelog

All notable user-facing changes to **Rolling Memory** are documented in this file.

---

## v0.0.1 — 2026-11-16

Initial public release.

- **Local-first persistent memory (`local_plain`)**: on-device long-term memory over a single-file SQLite plaintext vault (FTS5 + BM25 retrieval with dimensionless Shannon self-information gain and event-chain coherence). Zero network egress, zero vector database, zero model calls required for daily memory storage and retrieval.
- **Three-layer memory architecture**: structured separation across L1 Background (mounted local workspace folders), L2 Episodic (streaming Harness conversation capture with zero-copy verified pointer indexing — the multi-generation union read keeps the entire history recallable across encoder upgrades; on a live 4,773-document vault, cited-evidence recall went 26.3% → **90.9%**), and L3 Noetic (consolidated evidence-backed insights, automatically crystallized from recalled evidence).
- **Single-tool MCP & multi-harness integration**: built-in stdio/HTTP MCP server exposing the unified `recall` tool (automatic consolidation on hit; default `top-k = 10`, up to `64`; default `16,000`-character delivery budget) together with one-command installation and lifecycle hooks for ZCode, Claude Code, Codex, DeepSeek Harness (`dsh`), Cursor, CodeBuddy/WorkBuddy, Windsurf, and Cline.
- **Source-available web administration console** (`rolling-memory panel`): Go + SQLite backend (`:9006`) and Vue 3 + TypeScript + Vite frontend (`:8008`) providing a read-only Recall Debugger, three-layer memory browser, Harness sync diagnostics, ROI & adoption telemetry, an interactive memory graph, and an evaluation workbench over an event-driven WebSocket bus.
- **Reproducible evaluation runner (`rmem eval`) & benchmark readings**: corpus-pinned evaluation over ACL 2024 LoCoMo and the University of Cambridge ATM-Bench with isolated evaluation scopes and per-question provenance. On the full official LoCoMo Cat 1–4 set (1,540 questions), the local plaintext arm reaches **85.26%** end-to-end judge accuracy (1313/1540; single-hop 90.96 / temporal 87.23 / multi-hop 70.57 / open-domain 71.88; retrieval 100% offline). ATM-Bench readings: Main-1013 R@10 **82.56%**, Hard-31 R@10 **50.35%** (official gold-set-mean, pinned corpus and protocol). One-command corpus fetch (`rmem eval --fetch atm|locomo`) downloads the pinned upstream data, rebuilds the corpus and question sets locally, and verifies the digests. The benchmark corpora are never bundled with the product or its release assets (upstream CC BY-NC 4.0 terms).
- **Cross-platform packaging & self-update**: six-platform CLI binaries, macOS `.dmg` and Windows desktop packages (`.zip`), Windows drive-letter/UNC path hardening, and a built-in `rolling-memory update` path with SHA-256-verified release assets and dual GitHub/Gitee sources.
- **Cross-language client SDKs (`sdk/`)**: unified Go, Python, Node.js, and Java SDKs (`v1.11.3`, Apache-2.0) supporting the symmetric `local_plain` / `pulse_plain` / `pulse_cipher` modes, with complete third-party license notices.
- **Licensing**: source-available under **PolyForm Noncommercial 1.0.0** — free to use, modify, and share for noncommercial purposes; commercial use or distribution requires a separate license (the SDKs additionally carry Apache-2.0 terms for integration).
