# Changelog

All notable user-facing changes to **Rolling Memory** are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- Memory in scripts written without word spaces (Thai, Kana) is now found by substring queries that span a long run: such runs are split into adjacent bigrams and single characters when the harmonic glue path does not take them, while Han and space-delimited text keep their previous behaviour.
- Creating a config file while `XDG_DATA_HOME` or `XDG_STATE_HOME` is set no longer writes a file that cannot be loaded back on Windows: the resolved data and log paths are emitted as escaped TOML strings, so the backslashes in a Windows path are no longer read as unicode escapes. (Reported and suggested by @dengboyu412-lang in #3; adopted and rewritten in the source tree.)
- The reproduction command exported on an eval report page now quotes the eval-store path and folds the Windows home directory, so a store under the user profile keeps its backslashes and a path with spaces or quotes stays a single argument when pasted into Git Bash. (Reported and suggested by @wzm-Epoch in #4; adopted and rewritten in the source tree.)

## [0.0.15] - 2026-11-23

### Fixed

- The first recall after an ingest no longer pays for building the projection of a newly published cluster: those clusters are warmed in the background, so the first query returns at steady-state latency.
- Retiring a replaced vault snapshot no longer invalidates readers that are still using it: a snapshot generation is retired only after its readers release, so a concurrent recall cannot observe a half-retired store.
- A cached memory-graph projection now holds the corpus lease for as long as it is cached, so the cached view cannot point at a generation that has already been retired.
- The update check no longer reports an error when the Gitee API rate-limits the request; it falls back to the next configured source.
- The local MCP daemon no longer trips a data race when the watchdog cadence is shortened while a server started earlier is still winding down.

### Changed

- Engine, SDK and runtime comments now state the mechanism behind a threshold instead of citing a benchmark name or one of its scores; `make check` enforces this with a new comment gate.

## [0.0.14] - 2026-11-22

### Added

- OpenCode conversations are now imported into episodic memory: the background sync reads OpenCode's local SQLite session store (`session_v2` / `session_message`) and turns completed user/assistant exchanges into memory units through zero-copy `@ref:opencode:` pointers. The live resolver, prune/heal tools, backup preflight and console attribution recognize the new pointer family; older OpenCode stores without `session_message` keep inventory-only behavior, and turns without a completed final answer are reported as incomplete instead of imported.

### Fixed

- The MCP server no longer drops a live agent connection: input silence now releases the vault handle and keeps serving, and the default local-daemon sidecar survives both silence and a replaced build, so hosts that hold a quiet connection open no longer report `Connection closed: MCP server process exited with code 0`. `ROLLING_MEMORY_MCP_IDLE_EXIT` is replaced by `ROLLING_MEMORY_MCP_IDLE_RELEASE`.
- The evidence-adoption audit ledger no longer resets when background ingestion changes which stored row sorts first. The ledger is now bound to the vault's immutable creation identity instead of the oldest stored row; a ledger written by an older build is rebound in place on upgrade so its settled history survives, and only a genuinely different vault rotates the ledger aside.
- The console login page shows the current release version instead of a stale string.

## [0.0.13] - 2026-11-22

### Fixed

- A fresh console installation from the Gitee source resolves the newest release tag before downloading the backend and web assets, instead of requesting Gitee's nonexistent `latest/download` path.
- Both installers resolve Gitee's newest release tag, so an explicit Gitee mirror base installs the SHA-256 verified prebuilt binary instead of failing the prebuilt path.

## [0.0.12] - 2026-11-22

### Added

- Doubao conversation exports saved under `~/Doubao/exports` (or `~/.doubao/exports`) can now be imported alongside the other supported harness transcripts.
- Background updates are selectable in the console: `update.auto` supports `off`, `notify` (check and notify) and `apply` (download, verify and install). Published installs are seeded with `apply`; source installs are seeded with `notify`. The same policy can be run manually with `rolling-memory update --auto`.
- `rolling-memory panel restart` stops and starts the local console from already installed assets, and the console uses it through a detached helper so a backend replacement no longer leaves the console stopped.

### Changed

- Published installs now default to the `stable` update channel; the installer writes `dev` explicitly for source builds, so developers keep tracking `main`. Existing installations that saved `dev` can switch with `rolling-memory config set update.channel stable` (or the console's one-click switch) to receive binary updates.
- The optional six-hour scheduled job now runs `update --auto` and follows the saved background policy instead of only refreshing the cache.
- The admin console's sync status distinguishes history outside `sync.since`, incomplete turns, and excluded subagent sources from pending work, and lists stale and superseded knowledge entries separately.
- Episodic scans reuse the longest already-scanned ZCode turn prefix, so repeated lookups of one long session no longer rescan it from the start.
- The memory graph resolves word families from the published snapshot and caches each visible unit's hydration, so switching capacity, layer, or keyword filters no longer repeats the full projection.

### Fixed

- A console-initiated update restarts the backend and frontend in a detached session; stopping the backend no longer kills the restart helper and leaves the console offline.
- The update dialog shows an em dash instead of a hard-coded `0.0.1` when the local version has not loaded yet, and reloads only after the restarted backend answers again.
- The admin console's recall latency no longer depends on one host's private tool log: the memory server records each recall's elapsed time in the fact ledger for every harness, and the card reports the median across all of them.
- Realtime console freshness, auto-watch, the harness efficacy report, and the ROI adoption audit now cover every supported host through one adapter list instead of special-casing a single host; adoption reads the vault's episodic rows plus JSONL transcripts, not a host-private SQLite schema.
- Recall keeps the clustered scoring path when an expansion budget is zero, instead of silently switching to the legacy scorer.
- A recall call retries once when the local daemon retires between the health probe and the request, instead of returning a transient EOF.
- Harness counts and their per-host cache now honor the `sync.since` ingest bound instead of reporting out-of-scope history as pending work.
- Vault fingerprints and the secret-key fast path read the canonical document rows, and a vault upgraded from an older schema keeps its knowledge-trace columns.
- The admin console reads canonical rows and rule names, seeds the newer interactive route permissions, and only reports a memory-limit restart when the value actually changed.

## [0.0.11] - 2026-11-22

### Added

- A public **Architecture & Performance** page describes the three memory layers, immutable published generations, asynchronous visibility, and measured local recall latency under an ingest backlog. The English page and its Simplified Chinese mirror are linked from the documentation hub.

### Changed

- Recall now serves only the active published generation: a write becomes searchable when the background builder publishes the next generation, not while the write is in flight. On the 5,000-unit ATM corpus, warmed queries stay at p50 11–15 ms and p99 23–25 ms with 0, 200, 2,000 or 5,000 unpublished writes, and only the first query of a new generation pays a one-time open.
- The background builder reuses the previous published generation and applies the change log instead of rebuilding the whole corpus from storage. It now publishes on a fixed 60-second cadence by default (`daemon.snapshot_interval`, `10s–1h`, also `ROLLING_MEMORY_SNAPSHOT_INTERVAL`), so a batch is whatever arrived in that window; reference batches of 64–128 writes publish in about 0.5–3.7 s at 2,000–10,522 units, versus 1.6–14.1 s for a full rebuild. The first generation is still built immediately.
- A write is durable immediately and becomes searchable at the next publish. Daily harness work rarely needs immediate visibility — the current turn is already in the model's context — so the default cadence trades a short delay for stable recall during long sessions and large imports.
- The README now leads with the independent **AML Cycle 2** code-track result: the Noisy new-feature resolution rate is **80.39%** (No. 1 on the closed-source methods board, published 2026-11-19), shown together with the same track's overall Noisy reading (62.00%, tied 3rd), combined ranking (5th), and bug-fix reading (52.53%). The Live Console screenshot moves below the product summary so the official leaderboard card is the first image.

### Fixed

- `install.ps1` now enables TLS 1.2 and disables the `Invoke-WebRequest` progress bar before any download, and is stored as UTF-8 with a BOM so Windows PowerShell 5.1 reads its non-ASCII diagnostics correctly. Previously, unpatched Windows hosts could fail the GitHub handshake, large downloads crawled behind the progress bar, and every non-ASCII message was garbled. (Reported and suggested by @Buyou27 in #2; adopted and rewritten in the source tree.)

## [0.0.10] - 2026-11-21

### Changed

- `rmem eval --fetch <target>` now stops after staging and verifying the upstream data; the evaluation continues in the same invocation only when an explicit `--suite` is given, and `--fetch-only` keeps forcing the staging stop. This removes the previous surprise where a plain fetch command ran a full (possibly paid) evaluation.
- ATM-Bench retrieval reports record the build toolchain (`go_version`, `go_os`, `go_arch`), and the official runner prints them beside the metrics, so a cross-machine reading can be attributed before it is compared with the published numbers.
- Query planning now avoids repeated group lookups, folds sentence groups with a counting scatter, keeps small endorsement batches serial, specializes single-position slot scans, and tabulates term-repeat gain. Kernel differential tests pin the folded sequence and the slot scan. ATM-Bench remains Main-1013 R@10 82.77%, Hard-31 R@10 52.43%, composite 73.67 (7366.8 bps), and the per-question ranking sets are identical.

### Fixed

- The df=0 inferred ordinal-day dispatch now uses the same exact `κ_occ` estimator (`calendarConcentration(1, dfDay, M)`) as the term path instead of the retired `(n−1)/(n+2)` shrink. The change is score-neutral: ATM-Bench remains Main-1013 R@10 82.77%, Hard-31 R@10 52.43%, composite 73.67 (7366.8 bps), and the per-question ranking sets are unchanged.

## [0.0.9] - 2026-11-21

### Added

- `rmem recover` rebuilds a vault from the local transcript window in additive scan passes: it never retires, heals or prunes, `--passes` / `--limit` / `--budget-sec` bound the work, and the command stops after two idle passes. `rmem sync --scan-limit` (1–50000, default 2000) sets how many harness turns one foreground pass may scan per harness, so a manual recovery after a vault reset can walk the full local backlog without changing what the scan writes.

### Changed

- Benchmark disclosure now reports the current product arm: ATM-Bench Main-1013 official R@10 **82.77%** (Any-Hit@10 85.29%, All-Hit@10 80.06%), Hard-31 R@10 **52.43%** (Any-Hit@10 80.65%, All-Hit@10 29.03%), composite **73.67** (7366.8 bps). These are the values a fresh build reproduces after the kernel's exact calendar-occupancy operator replaced the previous heuristic; the value-ordered group-mass summation shipped in the same sync is score-neutral. The earlier 82.85% / 73.72 figures came from the prior heuristic and remain in the 0.0.7 entry as history only.
- The evaluation workbench reports corpus load time (`load_ms`) separately from the question loop (`eval_seconds`). Total elapsed remains their sum, and run records written by earlier releases fall back to the single stored duration.

### Fixed

- `rmem eval --fetch atm` no longer stops when a correct rebuild differs only in its gzip container. The corpus pin now accepts three equivalent proofs — the exact container bytes, the decompressed JSONL stream, or the per-item content digest — so a Go toolchain with a different flate implementation still rebuilds the same 11,034 items in the same order; `UPSTREAM-PROVENANCE.json` records which proof matched. The question tiers remain byte-pinned. `--no-verify` is now documented as skipping the whole pin set rather than one file.
- The ROI monitor accepts a canonical vault whose `rm_units` is a view and skips the revision and twin-row reconciliation when the ledger has neither, so a plain ledger is scanned directly instead of through correlated subqueries.
- The console's stale-binary check now covers the embedded baseline and leaderboard JSON, so refreshing those files marks the console for a rebuild instead of serving the previous numbers.

## [0.0.8] - 2026-11-20

### Changed

- Memory-graph phrase nodes now require two terms whose text spans do not overlap. Adjacent sliding windows of one Han run are the dual track that keeps retrieval recall, but they are one text run projected twice, so they are no longer projected as phrases; disjoint tilings of the same run are recovered instead, which keeps genuine Chinese pairs such as 推进·继续 visible. Every phrase fragment now carries the pair df, both member dfs, `lift` against the independence expectation, and how much of the rarer member the pair covers, and the detail drawer shows them next to the binding energy. Retrieval postings, word families and the tokenizer are untouched.
- The `recall` surface now matches what the local vault can deliver: `--bands` / `--nodes` / `--chars` and `--offset` are documented in the CLI, and the MCP schema advertises `delivery_window` plus `caps{delivery_window,bands,nodes,chars,raw}`. The retired `asof` / `window` / `session` filters are gone instead of being accepted and rejected, and the console no longer sends a graph-walk depth it cannot use.

### Added

- An R entry point for researchers: `research/r/` ships a thin shell-out layer (`rmemory.R`: `rm_init`, `rm_import`, `rm_recall`, `rm_bands` and friends), an end-to-end demo on a throwaway vault, a base-R reproduction of the occupancy-null distribution and its tail bound, and a half-life script that separates the closed form from the estimation problem (right-censored corpora bias a naive bridge share; the censored MLE and a bootstrap interval are worked through on simulated gaps). R needs only `jsonlite`; the CLI plus JSON stays the whole interface. The notes cover the two things that bite first — `system2()` does not quote arguments, and a vault only answers for the scope it was created with — and the JSONL sample carries the `stop_reason` completion marker that `import` requires.


### Removed

- The legacy JSON export import path and the assertion-graph FTS rebuild helper are gone. `import` accepts JSONL transcripts only; other formats are refused instead of being translated by a retired reader, and recall receipts no longer carry a `vector` provider block.
- Databases created by an older release (any database that still carries the retired assertion graph) are refused at open with a message to download the latest release and re-import the transcripts into a new vault; they are never served through the legacy reader.
- The retired assertion-graph engine is deleted: the legacy graph store operators, the old recall read path, the delivery primitives, and the transcript reader are gone, together with the golden corpus that replayed them. The binary now links only the canonical `rm_*` vault, the local delivery result renderer, and the hook fact ledger. The efficacy report therefore covers the hook ledger and vault counters; its old per-turn adoption replay no longer exists.
- The retired recall config keys (`chains`, `rows`, `vector`, `search_engine`, `token_correct`, `alias_expand`, `relative_tag`, `mention_backfill`, `raw_when_sparse`, `hook_bands` / `hook_nodes` / `hook_chars`, `activation_record` / `activation_rank`, `mask_deliverable`, `advanced_ops`) are ignored instead of being parsed and exposed, and `asof` / `window` / `session` are no longer accepted by the CLI or MCP.
- The `wave` command, the `recall` / `status` `--wave` flags, and the Braille oscilloscope renderer are gone. The command was never part of the documented surface.
- The legacy-only item and breadth switches (`--depth`, `--caps-chains`, `--caps-rows`, `--result-unit`, `--rank` / `--no-rank`, `--token-correct`, `--no-alias-expand`, `--relative-tag`, `--no-mention-backfill`) no longer parse. The MCP schema also dropped `deep`, `result_unit`, `rank`, `caps.chains` and `caps.rows` instead of accepting switches the local vault ignores.
- `hook_bands` / `hook_nodes` / `hook_chars` are gone from `[recall]` — the prompt hook captures and reminds but does not recall — and the generated config template no longer writes the retired `vector` switch.

## [0.0.7] - 2026-11-19

### Changed

- **Live host attachment**: the console now names a coding host only while a session is actually attached. The local MCP sidecar renews a lease every thirty seconds, a prompt hook records a two-minute heartbeat, and an expired lease means no live host — a newest transcript or a leftover presence file is no longer presented as the current environment. When nothing is attached the badge reads "No live host detected", and a separate "Recent activity" badge names the host with the newest transcript. `rolling-memory sync status --json` carries the same distinction as `attachment` / `attachments` alongside `recent_harness_id` and `recent_harness_label`, and a process whose host cannot be identified is reported as unknown instead of being labelled as a generic CLI session.
- **One physical corpus**: `rm_docs`/`rm_unit_meta` are the only stored corpus. A read-only `rm_units` read model exposes the effective text and metadata without a second copy. There is no legacy table migration: earlier corpus schemas are not carried forward.
- Benchmark disclosure now uses the current product arm: ATM-Bench Main-1013 official R@10 82.85%, R@25 86.41%, R@50 90.11%; Hard-31 R@10 52.43%, R@25 60.14%, R@50 70.62%; composite score 73.72. The corpus and question pins were re-verified on 2026-11-19. The frozen v0.0.1 readings (77.21% / 46.12%) remain in the v0.0.1 changelog entry as historical values. LoCoMo full-set end-to-end judge accuracy remains 85.26% (1,313/1,540; Top-96 delivery; LLM answerer + judge).
- `rolling-memory status`, `check`, and `doctor` report one corpus only: the canonical counters (memories, episodic, background, crystallized) plus the vault's term statistics. The retired deep-metadata fields are gone, and the configuration block shows the `update.channel` / `update.source` preferences instead of the removed `[llm]` section.

### Fixed

- `rolling-memory install --check` now probes the server without running a background version check, so the check leaves no update cache behind and never reaches the network.
- OpenCode environments are now included in the turn-scanning capability set, so the console reports episodic alignment for them instead of leaving the layer marked as unknown whenever an OpenCode configuration directory is present.
- Prompt hooks and the console now agree on where runtime presence lives when `XDG_STATE_HOME` is set or the engine log is redirected: the attachment ledger is written beside the rest of the runtime state and read from the same directory, so the console no longer reports an idle environment while a session is live.
- Re-running `rolling-memory install` after the binary moved no longer stops on its own generated launcher: an owned launcher is updated in place with a backup, while a foreign script still reports a conflict.
- `rolling-memory install --check` now compares the installed skill with the current release and inspects the launcher. An older owned copy reports `outdated`, a user-edited copy reports `conflict`, and a missing launcher fails the check instead of passing with a live probe against a different executable.
- An enabled native plugin no longer ends up with a second skill, MCP entry, or prompt hook beside it: `--force` cannot duplicate a plugin-provided surface, and `--dedupe` removes only this tool's owned copy. A plugin MCP command that is not available is reported as a conflict rather than trusted.
- After an in-place upgrade, `rolling-memory daemon restart` can retire a daemon that still serves the previous binary. The command authenticates the stale socket by ownership, vault path, and protocol identity; `install` prints it when the mismatch is present.
- Plugin cache directories are ordered by semantic version, so `0.10.0` is selected over `0.9.0`.
- Preparing the evaluation workbench no longer stops with an identity-mismatch error when the evaluation store was written by an earlier engine build: the stored corpus is re-ingested under the current endpoint and tokenizer instead. A resumed ingest also re-encodes every unit whenever the tokenizer arm changes, so a warm store is never searched with the wrong vocabulary. Evaluation receipts now report the number of units actually written, so a warm-store run shows zero instead of the corpus size.

### Added

- **Complete disk-maintenance coverage**: `rmem maintain clean` now also manages vault database backups, launcher backups in `~/.local/bin`, and the console's previous `web-dist` directories, keeping the newest copies per family and writing a receipt. `rmem maintain status` reports evaluation history (`eval/`, `atmbench/`) with its size but never auto-deletes it.
- **Global update entry**: the console top bar now shows an update badge whenever a newer release is available, from any page. One click opens the same SHA-256 verified updater that refreshes the CLI, the console backend, and the web assets in place, then restarts the console.
- **Automatic version awareness**: the web console now surfaces a newer release as soon as its real-time channel connects, with a badge on the version button and a one-time notification. The cached state is refreshed in the background after six hours (thirty minutes after a failure), and the CLI `status` / `doctor` receipts carry the same cached hint without a network request. A tag without installable release artifacts is reported as a source-only update instead of offering a download that cannot succeed.
- **Update channels**: `stable` accepts published non-prerelease tags, `beta` also accepts prerelease tags, and `dev` tracks the latest `main` commit and is the default for new installs during this early stage. The preference is saved in the CLI config (`update.channel`), exposed as a selector in the console's Version & Update dialog, and honored by the optional scheduled check. `dev` reports `main@<commit>` and requires an explicit source rebuild; it never replaces a binary automatically.
- **Optional OS-level update check**: `rolling-memory update --schedule install` registers a user-level launchd agent, systemd user timer, or Windows scheduled task that refreshes the cache and sends one desktop notification per new version. It never installs an update automatically, and `--schedule remove` cleans it up. Source installs can rebuild from a tag with `ROLLING_MEMORY_SOURCE_REF=v0.0.7 bash install.sh` or `bash install.sh --ref v0.0.7`.
- **Update source selector**: the Version & Update dialog now offers GitHub (official) and Gitee (mainland mirror) alongside a custom mirror URL. The choice is saved as `update.source`, reused by the CLI and background checks, and can be set with `rmem config set update.source gitee` or overridden per run with `rmem update --source gitee`. A Gitee tag without attached binaries is reported as a source-only update with a Gitee-specific rebuild hint.

### Removed

- Retired command surfaces are removed instead of kept as stubs: `export`, `reindex`, and `recall --deep` no longer parse, `--mode` and `mode` accept only `local`, and the unused `[llm]` configuration section is gone.

## [0.0.6] - 2026-11-19

### Changed

- **Single retrieval index per memory**: each memory's retrieval terms are derived once when it is saved and stored next to its text in the vault's document store, which is what the engine serves at query time. The in-memory snapshot and the on-disk index cache are keyed by that store's revision counters, so a warm index keeps being reused as new memories arrive, and the packed-atom codec that previously had to be decoded again at read time was removed.
- The memory console, in-app help centre, and benchmark/privacy documentation now describe the shipped deterministic cluster kernel instead of the retired FTS5/BM25 lexical index, and the evaluation page no longer mentions a retired cloud endpoint.
- **Help centre is now a documentation portal**: the console no longer stores its own copy of the docs. Opening Help & Docs shows a GitHub/Gitee site chooser and links to the official repository documentation, so the text always matches the released source; the redundant "Open-Source Repos" page was removed.

### Fixed

- Opening a vault created by an earlier version now derives its existing memories from their own sources — stored text, mounted files, or the original session — before the first recall runs, so an upgrade never leaves recall pointing at an empty corpus. The one-time pass is recorded in the vault, skips units the user deleted, and later opens start immediately.
- Background episodic maintenance now runs one pass at a time per process and stops long transcript scans as soon as its lease is revoked, so several connected hosts no longer stack repeated reads of the same session file or pin a CPU core after a session goes idle.

## [0.0.5] - 2026-11-19

### Added

- **OpenCode integration**: `rolling-memory install opencode` now wires the skill and the local MCP server into OpenCode V2 with its native configuration shape (`mcp.servers` → `type: local`, array `command`, `environment`), including project-scoped installs (`--project`) and `--check` / `--uninstall` support. The adapter writes the user skill root (`~/.config/opencode/skills`, honoring `XDG_CONFIG_HOME`) or `.opencode/skills` for projects, and leaves unrelated providers, models, and servers untouched.

### Changed

- Disk maintenance keeps the engine's on-disk index cache by default. Deleting that cache forced the next recall to rebuild the whole index — minutes of work and a multi-GB memory peak — so freeing the space is now an explicit opt-in (`rmem maintain clean --purge-engine-cache --apply --yes`), and the read-only scan reports the cache separately as kept instead of counting it as reclaimable space.
- Remembering a long text now stores it as several sentence-aligned memory units of at most 8,192 tokens each (identical chunks collapse), so oversized pastes stay recallable and never hit the single-payload ceiling; a text that fits within one unit keeps its existing memory id.

### Fixed

- Long-text units and text/code background files now keep the source text verbatim, including indentation and the whitespace at a cut point, and each one's token bound is checked against the exact text it stores rather than per-sentence estimates. A unit can no longer quietly exceed the 8,192-token rung through cross-boundary re-tokenisation; a single sentence that alone exceeds it is still kept whole instead of being cut.
- Spreadsheet background files use the same exact-fit budget rule when a sheet is split into row blocks, and a row that alone exceeds the budget is kept whole instead of being cut.
- A message long enough to be stored as several memory units now attributes every delivered part to the speaker named by the message's opening line, instead of leaving the parts after the first without a speaker. The attribution is read from the stored source, never guessed from the content.

## [0.0.4] - 2026-11-18

### Changed

- **Engine-native incremental overlay**: the retrieval kernel now serves added, updated, and deleted memory units through a zero-copy on-disk baseline plus a small in-memory delta layer. Continuous ingestion no longer triggers full index rebuilds, so recall stays responsive and resident memory stays bounded as the vault grows.
- **Leaner client surface**: remaining duplicated client/kernel paths were retired — incremental serving, per-unit term lookup, and snapshot verification are now delegated to the engine kernel, and redundant storage and graph-client adapters were removed with no change in memory quality.

### Fixed

- Automatic background sync no longer re-reads and re-verifies every stored episodic payload after a single new turn is saved; the diagnostic check reuses the zero-copy snapshot's verified unit set, removing multi-core CPU spikes during continuous ingestion.
- L3 crystallized insights no longer receive a doubled citation boost when the same evidence is reachable through both the citation closure and a direct match.
- Console maintenance and evaluation wording reads consistently in plain language.

## [0.0.3] - 2026-11-18

### Added

- **Codex hooks support**: `install codex --with-hooks` writes the Claude-shaped `~/.codex/hooks.json` (and project `.codex/hooks.json`), bringing the zero-intrusion memory readiness prompt to Codex sessions; the first run requires the hook to be reviewed and trusted inside Codex.
- **Memory curation commands**: `rmem maintain` (disk-space maintenance — prune old backups and caches, compact and slim the vault; dry-run by default) and `rmem prune` re-anchoring same-source hits by default (cross-source substitution requires an explicit `--reanchor-cross-source`).
- **Cross-turn pointer re-anchoring** with a recurring same-source pointer patrol and a "Cleanup & Self-Healing" console entry, plus a dry-run-first unmount preview for `background remove` that shows the re-bind/retire/citation impact before confirming.
- **L3 verification-reuse gain & ROI gold leaderboard**: crystallized insights whose cited evidence is recalled again rank through a continuous log gain over their citation closure, surfaced as a first-class tier in the console.
- **Evaluation**: the official LoCoMo Cat-5 adversarial tier (446 questions) is scored through the tri-state evaluator, together with three adversarial probe suites (ATM absence 100 / ATM conflict 100 / LoCoMo adversarial 446) published with reproducible evidence.

### Changed

- **Memory governance for large vaults**: steady-state resident memory dropped from runaway multi-gigabyte growth to a bounded footprint — on a 34k-unit live vault, steady ≈3 GB (≈2 GB file-backed pages) where the session previously peaked above 20 GB. The engine now serves rebuilds from disk-backed zero-copy snapshots, releases per-build term maps after use, and no longer rebuilds the full index on every background write, so continuous ingestion keeps foreground recall responsive.
- **Single local-first runtime**: the client was streamlined onto its one supported local mode; the retired local lexical index (FTS5/BM25) and its comparison arm were removed so retrieval runs solely on the engine's cluster kernel — smaller vault files and faster cold scans, with the frozen ATM baseline preserved bit-for-bit (1,044 questions).
- **Smart adaptive delivery window**: recall now delivers through a natural energy window by default (`auto`, up to 16 bands), with optional wider windows (`0-32` / `0-64`) for audits; the MCP `chars` parameter was removed in favor of the window controls.
- **Vault hygiene**: opening a vault now reclaims legacy leftover tables (session cursors, retired indexes), and fresh databases bootstrap thin — no legacy assertion graph — keeping user vaults small and simple.
- **Kernel synchronization**: the retrieval kernel picked up the latest engine revisions — package-internal deduplication, ordinal-day canonicalization, an exported high-DF fraction, and curated closed-set tables — validated bit-for-bit against the frozen ATM baseline.
- Panel lifecycle management is unified: the `rolling-memory-panel` wrapper and the `panel` command share the same pid/log locations.
- Console: the ROI 14-day panel was redesigned (distillation × crystallization × adoption tracks with independent scales and per-day adoption), the real-time recall latency card reports a robust median, and the monitoring float now speaks the Local Vault vocabulary only.

### Removed

- Pre-local-first legacy layers were removed from the client — obsolete remote-client code paths, the optional model-extraction layer, and standalone duplicate helper primitives — a smaller, faster-to-audit codebase with no memory-quality change.

### Fixed

- Recall delivery budget is now enforced end to end: the legacy 200-character per-item truncation floor was removed and truncation markers count against the budget, so `caps.chars = 300` never over-delivers.
- Installer integrity checks are fail-closed: a SHA-256 mismatch for a downloaded release asset is a fatal error instead of a silent fallback to a source build; when no checksum tool or entry is available the installer reports `unverified` with an explicit reason.
- Bare installs and one-command installs now honor the persisted language preference (`--lang` is no longer reset to English).
- Harness conversation capture keeps envelope-only turns out of the user-speech stream.
- L3 crystallization citations can no longer collapse to a bare label line: citations keep the full role block (role line plus continuation lines) and reuse the delivery-side cleaning predicate.
- The real-time stream no longer replays stale memory content: frames that carry memory text are computed fresh for new subscribers.
- Episodic import, scan, and reconcile share one frozen per-source parse and a content-addressed identity per turn, fixing cross-turn drift and enabling safe same-source re-anchoring before deletion checks.
- The console's real-time recall latency card reports a robust median (latest and peak values in the tooltip) instead of a single spike-prone sample.
- LoCoMo corpus rebuilds assign globally unique line ids and dedupe on the locator, eliminating rare id collisions in rebuilt evaluation corpora.

## [0.0.2] - 2026-11-18

### Changed

- Consolidated cloud credential handling onto the single canonical pulse path: `mem2` credentials carrying a `bpep` blinded pepper use it verbatim, static secrets and legacy `mem2` tokens keep the documented derivation, and the speculative V2/legacy dual-read query arm, pepper policies, and the `RekeyUnits` migration layer were removed from the SDK and thin client (local plaintext mode was always single-arm).
- Simplified the local product assembly onto the engine-native canonical companion path: structured-date ISO beacons and the merged closed-set companion tables (SI/IEC units, ISO 4217 currencies, ISO 3166 geography, statistical notation) now load directly from the SDK, replacing the redundant standalone language-pack layer with the same default max-capability indexing behavior.
- Aligned the client SDK with the project license: the SDK under `sdk/` now carries the same **PolyForm Noncommercial 1.0.0** terms as the rest of the repository (superseding the earlier Apache-2.0 SDK terms); commercial use still requires a separate written commercial license.

### Fixed

- Eliminated duplicate inline-snapshot documents during concurrent insert/scan windows and deduplicated repeated cloud candidates, preserving stable product/direct recall ordering; deterministic regressions were added and the LoCoMo product-parity probe passes 24/24 runs.
- Preserved high-resolution email sub-unit line spans by keeping subject metadata out of the document body while retaining the subject-plus-summary header block.

### Added

- Added repository community-health files — a Contributor Covenant 2.1 Code of Conduct (English and Simplified Chinese), an accessibility statement, and a pull-request template — so the public repository covers the full GitHub community-standards checklist.

## [0.0.1] - 2026-11-16

Initial public release.

### Added

- **Local-first persistent memory (`local_plain`)**: on-device long-term memory over a single-file SQLite plaintext vault (FTS5 + BM25 retrieval with dimensionless Shannon self-information gain and event-chain coherence). Zero network egress, zero vector database, zero model calls required for daily memory storage and retrieval.
- **Three-layer memory architecture**: structured separation across L1 Background (mounted local workspace folders), L2 Episodic (streaming Harness conversation capture with zero-copy verified pointer indexing — the multi-generation union read keeps the entire history recallable across encoder upgrades; on a live 4,773-document vault, cited-evidence recall went 26.3% → **90.9%**), and L3 Noetic (consolidated evidence-backed insights, automatically crystallized from recalled evidence).
- **Single-tool MCP & multi-harness integration**: built-in stdio/HTTP MCP server exposing the unified `recall` tool (automatic consolidation on hit; default `top-k = 10`, up to `64`; default `16,000`-character delivery budget) together with one-command installation and lifecycle hooks for ZCode, Claude Code, Codex, DeepSeek Harness (`dsh`), Cursor, CodeBuddy/WorkBuddy, Windsurf, and Cline.
- **Source-available web administration console** (`rolling-memory panel`): Go + SQLite backend (`:9006`) and Vue 3 + TypeScript + Vite frontend (`:8008`) providing a read-only Recall Debugger, three-layer memory browser, Harness sync diagnostics, ROI & adoption telemetry, an interactive memory graph, and an evaluation workbench over an event-driven WebSocket bus.
- **Reproducible evaluation runner (`rmem eval`) & benchmark readings**: corpus-pinned evaluation over ACL 2024 LoCoMo and the University of Cambridge ATM-Bench with isolated evaluation scopes and per-question provenance. On the full official LoCoMo Cat 1–4 set (1,540 questions), the local plaintext arm reaches **85.26%** end-to-end judge accuracy (1313/1540; single-hop 90.96 / temporal 87.23 / multi-hop 70.57 / open-domain 71.88; retrieval 100% offline). ATM-Bench readings: Main-1013 R@10 **77.21%**, Hard-31 R@10 **46.12%** (v0.0.1 frozen build; official gold-set-mean, pinned corpus and protocol). Current product-arm readings are 82.85% / 52.43% — see the current `docs/benchmarks.md`. One-command corpus fetch (`rmem eval --fetch atm|locomo`) downloads the pinned upstream data, rebuilds the corpus and question sets locally, and verifies the digests. The benchmark corpora are never bundled with the product or its release assets (upstream CC BY-NC 4.0 terms).
- **Cross-platform packaging & self-update**: six-platform CLI binaries, macOS `.dmg` and Windows desktop packages (`.zip`), Windows drive-letter/UNC path hardening, and a built-in `rolling-memory update` path with SHA-256-verified release assets and dual GitHub/Gitee sources.
- **Client SDK (`sdk/`)**: a zero-dependency Go SDK (`v1.11.3`) for local memory encoding and integration, with complete third-party license notices.
- **Licensing**: source-available under **PolyForm Noncommercial 1.0.0** — free to use, modify, and share for noncommercial purposes; commercial use or distribution requires a separate license.

[Unreleased]: https://github.com/rocareer/rolling-memory/compare/v0.0.13...HEAD
[0.0.14]: https://github.com/rocareer/rolling-memory/compare/v0.0.13...v0.0.14
[0.0.13]: https://github.com/rocareer/rolling-memory/compare/v0.0.12...v0.0.13
[0.0.12]: https://github.com/rocareer/rolling-memory/compare/v0.0.11...v0.0.12
[0.0.11]: https://github.com/rocareer/rolling-memory/compare/v0.0.10...v0.0.11
[0.0.10]: https://github.com/rocareer/rolling-memory/compare/v0.0.9...v0.0.10
[0.0.9]: https://github.com/rocareer/rolling-memory/compare/v0.0.8...v0.0.9
[0.0.8]: https://github.com/rocareer/rolling-memory/compare/v0.0.7...v0.0.8
[0.0.7]: https://github.com/rocareer/rolling-memory/compare/v0.0.6...v0.0.7
[0.0.6]: https://github.com/rocareer/rolling-memory/compare/v0.0.5...v0.0.6
[0.0.5]: https://github.com/rocareer/rolling-memory/compare/v0.0.4...v0.0.5
[0.0.4]: https://github.com/rocareer/rolling-memory/compare/v0.0.3...v0.0.4
[0.0.3]: https://github.com/rocareer/rolling-memory/compare/v0.0.2...v0.0.3
[0.0.2]: https://github.com/rocareer/rolling-memory/compare/v0.0.1...v0.0.2
[0.0.1]: https://github.com/rocareer/rolling-memory/releases/tag/v0.0.1
