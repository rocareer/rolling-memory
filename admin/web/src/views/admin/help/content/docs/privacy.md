# Privacy & Local Data Statement

**[English](privacy.md) · [简体中文](zh-CN/privacy.md) · [Back to Documentation Hub](README.md) · [Back to Repository Home](../README.md)**

Rolling Memory's privacy guarantee fits in one sentence: **100% of your raw plaintext, your consolidated memory, and your local master key stay inside the local SQLite vault on your own machine — the memory engine makes zero external network requests.**

This is not a policy promise: it is enforced by the architecture (the memory component itself has no egress path) and guarded by automated zero-leakage checks in the public test suite. Note that if your coding agent uses a remote LLM API, the passages returned by `recall` will be sent to that model provider as part of your prompt context. Below is what stays local, why your memory assets are safe on your own machine, and how to verify both yourself.

---

## 1. What Never Leaves the Memory Vault

| Asset | Where it lives | Memory Component Egress |
|---|---|---|
| Raw conversations, mounted documents, and recall results | Local SQLite vault (default `~/.local/share/rolling-memory/memory.db`) | **0 — never sent by Rolling Memory** |
| Consolidated L3 `Noetic` traces ($Q$ + `cited_unit_ids[]` + $A$) | Same local vault | **0** |
| Local master key / vault metadata | Same local vault (directory `0700`, file `0600`) | **0** |
| Recall indexes (SQLite FTS5 `trigram` + BM25) | Same local vault | **0** |

- **Local mode is the default and the only mode in this release**: no cloud account, no registration, no background upload, no analytics, no crash reporting, no telemetry.
- **Historical audience restrictions**: Do not rely on `private` labels or `reader=deliverable` to protect sensitive content across all sources. Review recall results before sharing them or sending them to a model.

---

## 2. Why Your Memory Assets Are Safe on Your Local Machine (Defense-in-Depth)

Your engineering memories (architectural decisions, codebase coordinates, debugging post-mortems, and personal knowledge bases) are high-value assets. Rolling Memory stores them under layered local defenses:

### 2.1 On-Host Physical Sovereignty: Single-File SQLite Vault & POSIX `0700/0600` Isolation

- **100% Local Disk Residency**: All raw dialogue turns, document slices, consolidated answers, and your local master key live inside a single local SQLite file (default `~/.local/share/rolling-memory/memory.db`);
- **Strict OS File Permissions**: The client creates the data directory with mode `0700` and the SQLite database and key files with mode `0600` (readable and writable only by you);
- **Disk Encryption**: Rolling Memory does not implement database-level encryption. Your data is protected statically by OS full-disk encryption (macOS FileVault, Linux LUKS, Windows BitLocker). Ensure full-disk encryption is enabled if physical device security is a concern.

### 2.2 Source-in-Place Live Read for L1 `Background`: Zero Redundant Full-Text Duplication

When you mount local knowledge bases or codebases into L1 `Background`:

- **Your Codebase Is Not Duplicated into External Databases**: Rolling Memory uses a **Source-in-Place Live Read** architecture — local SQLite stores only lightweight chunk metadata (file path, `mtime_ns`, byte offsets) rather than copying gigabytes of raw documents;
- **Minimized Local Attack Surface**: Your original `.md`, `.pdf`, `.docx`, and source files remain in their native workspace directories. When a file is edited or deleted on disk, delta reconciliation automatically invalidates dependent local `Noetic` traces.

### 2.3 Historical Audience Labels

Historical records may carry audience labels, but restrictions may not be enforced across all sources. Do not rely on these labels or the `reader` setting as an access-control boundary. Keep credentials and other sensitive material out of shared contexts, and review recall results before sharing them or sending them to a model.

### 2.4 Built-in Local FTS5 Recall & 100% Zero Vendor Lock-In

1. **Offline-First Recall (`Client SDK Default Recall`)**:
   - Your local vault maintains a native SQLite FTS5 lexical-temporal index alongside citation chains. Even offline, on a plane, or with every network interface down, your local memory assets remain 100% searchable and readable;
2. **100% Open Format & BYO-Storage Freedom**:
   - The local vault uses SQLite. Some memories reference conversation and mounted files outside the database; preserving the vault alone does not preserve those sources.

### 2.5 Secret & Log Hygiene

- **API Keys Never Touch Disk Databases**: Secret keys are read exclusively from environment variables or `0600` permission files (files readable by group/others on POSIX are rejected), and are never written into SQLite, `config.toml`, or logs;
- **Zero Message Bodies in Logs & Automatic Redaction**: Lifecycle logs (`~/.local/state/rolling-memory/rolling-memory.log`, rotating at 2 MB) record only event names, counts, latencies, and redacted error summaries — never conversation bodies — and automatically redact API-key, bearer, and access-token patterns.

---

## 3. Verify It Yourself

```bash
rolling-memory doctor --json                    # inspect mode, egress policy, secret permissions, DB health
rolling-memory check --json                     # inspect available database integrity checks
go test ./...                                   # public suite: zero-plaintext / zero-vector egress checks
```

- The Go suite includes checks for local-mode network behavior; it does not establish audience restrictions across all sources;
- Network-layer auditing works too: run any packet capture or firewall (`tcpdump`, Little Snitch, etc.) while the local-mode process works — no conversation-related outbound request is made.

---

## 4. Known Boundaries & Threat Model

- **Physical Device & Local Root Threat Model**: The local SQLite vault is stored in plaintext on local disk (mode `0600`) for sub-millisecond reads and transparent user auditing. If physical device loss or shared-host compromise is part of your threat model, enable OS full-disk encryption (FileVault / LUKS / BitLocker) or place `memory.db` on an encrypted volume;
- **Local access**: Other processes running under your OS user account may read `memory.db` and any copies you make.

---

## 5. Data Sovereignty: Export, Delete, Backup, Migrate

- **Zero collection**: local mode makes 0 external network requests and includes no analytics, crash upload, or usage reporting;
- **Export**: JSON export is unavailable because it does not include the vault and referenced sources. Existing JSON exports can still be imported into a compatible database, but are not complete backups;
- **Backup**: SQLite's `.backup` creates a consistent database copy, but not a self-contained backup. Preserve referenced conversation databases/files and mounted source files separately, including their paths. Protect all copies, which may contain private data and keys; `rolling-memory doctor --json` shows the vault path;
- **Delete**: stop the process and delete `memory.db` plus its `-wal` / `-shm` sidecars to remove the local vault. Mounted background source files are not deleted;
- **Migrate**: a vault copy alone does not migrate referenced sources. Preserve their original paths and verify access separately; `rolling-memory doctor` does not establish that every reference resolves.

---

## 6. Cloud Capability Preview (Application-Based)

Rolling Memory's cloud memory engine (the Rolling Autonoetic Memory Engine) is a separate capability that will open progressively on an **application basis**; online registration is closed. Email [pick@psychologylake.org.cn](mailto:pick@psychologylake.org.cn) to apply (support: [support@psychologylake.org.cn](mailto:support@psychologylake.org.cn)). It is not part of this release: this release is pure local mode, and onboarding details are shared with approved applicants.

Because 100% of your raw data stays in your local vault, there is **zero vendor lock-in** at any point. We remain fully open to open-sourcing the cloud core engine once the team achieves sustainable backing — the full stance is in [DECLARATION.md](../DECLARATION.md).

---

## 7. Reporting a Security Issue

Please report suspected vulnerabilities privately as described in [SECURITY.md](https://github.com/rocareer/rolling-memory/blob/main/SECURITY.md). We credit responsible disclosures and will never require you to hand over your plaintext or your master key to demonstrate an issue.
