# Golden fixtures

Generated from the pinned reference oracle (`a0fa206`, product v0.7.4) at generator
commit `215328b` — the single authoritative pin is `MANIFEST.json.generator.commit`
(`215328b292ed1e97b61a8c2c7109f093781bee52`, with `a0fa206`/v0.7.4 as recorded in
`generator.scope`).
Do not hand-edit: verify with the Go seal check below, and reseal only with
owner approval.

## Frozen parameters

- `TZ=UTC`, `LC_ALL=C`, `FIXED_NOW=1793102400` (2026-10-27T12:00:00Z), `MONO_BASE=1000000`.
- `time.time`/`time.monotonic` are patched for the whole generation; all DB time columns are explicit.
- The environment is cleared of `ROLLING_MEMORY_*`, `XDG_*`, LLM/legacy vector-family/sync secrets; `HOME`/`TMPDIR` are synthetic.
- The install scenario patches `time.strftime` to a fixed backup stamp; backups are compared as `<TS>` paths.

## Layout

```text
testdata/golden/
├── README.md
├── MANIFEST.json                  provenance + per-file sha256 + seal
├── schema/v1.sha256               schema.sql digest
├── vectors/                       ids/normalize/tokenize/time/window/anchor/ratio/hash-embedding (legacy compat vector family, retained)/pyjson
├── db/<scenario>.db               frozen SQLite fixture (0444, no -wal/-shm)
├── fixtures/<scenario>/           fixture.json (ops + frozen columns) + expected/
├── cli/<scenario>/                cases.json + input/ + expected/<case>.*
├── mcp/<scenario>/                transcript.jsonl + expected.jsonl + meta.json
├── hooks/<scenario>/              cases.json + input/ + <case>/stdin.* + <case>/expected_*
└── install/<scenario>/            home/ template + case.json + expected/<case>.report.json + tree.json
```

## Normalization

Golden JSON is stored after applying the canonical normalization rules: `elapsed_ms`→0,
`db_size_bytes`→0, `gaps.budget_exceeded`→0, `exported_at`→`FIXED_NOW`,
config/status `db`/`log`/`path`→`<DB>`/`<LOG>`/`<CFG>`, `python`→`<RUNTIME>`,
`sqlite`→`<SQLITE>`, legacy vector-family (`hash-embedding`) warnings→`null`, `meta.deep.error`→`<ENV>`,
`alias_collisions[*].nodes` sorted by id, `similar_labels` sorted by
`(-ratio, left.id, right.id)`, rendered `ms="<digits>"`→`ms="0"`, hook log `ts`→`<NOW>`,
the desktop launcher file name (`.command`/`.desktop`/`.lnk`)→`<SHORTCUT>`,
install backup stamps→`<TS>`, and all synthetic paths tokenized (`<TMP>`, `<HOME>`,
`<REPO>`, `<OUT>`, `<CFG>`, `<DB>`, `<LOG>`, `<CASE>`).

Row order: the `activation` ledger is upserted while iterating an unordered set,
so its physical row order is hash-seed dependent.
Its rows are therefore dumped, digested and sealed **ordered by `node_id`**; a
runner must compare that table with the same order instead of `rowid`.

## Surface tokens

Surface scenarios (`cli/`, `mcp/`, `hooks/`, `install/`) drive command
surfaces instead of a fixture DB.  Input templates and expectations share
these tokens; the runner substitutes them with its own paths before use.

| token | meaning |
|---|---|
| `<TMP>` | the runner work root |
| `<CASE>` | `<TMP>/cases/<scenario>` |
| `<HOME>` | `<CASE>/home` (synthetic HOME) |
| `<CFG>` | `<CASE>/config.toml` |
| `<DB>` | `<CASE>/memory.db` |
| `<LOG>` | `<CASE>/state/rolling-memory.log` |
| `<OUT>` | `<CASE>/out` |
| `<NOW>` | the frozen clock value `FIXED_NOW` |
| `<TS>` | install backup stamp (`rolling-memory.<TS>.bak`) |
| `<SHORTCUT>` | desktop launcher file name (platform variant abstracted; the location `Desktop` under `<HOME>` stays) |

- `cli/<scenario>/cases.json` lists every invocation in order (`argv`,
  `note`); `expected/<id>.stdout.json` (JSON commands) or `.stdout.txt`
  (plain text), plus `.stderr.txt` and `.exit.txt`.  A `sql` case mutates
  the fixture DB directly and expects `expected/<id>.json` (`rowcount`).
  `$<case>/...` references are resolved before the argv is frozen, so the
  committed argv carries concrete ids.
- `mcp/<scenario>/transcript.jsonl` is the literal stdio input (one
  JSON-RPC message per line); `expected.jsonl` holds one canonical JSON
  line per response (sorted keys, `ensure_ascii=false`).
- `hooks/<scenario>/<case>/stdin.{json,txt}` is the payload template;
  `expected_stdout.txt`, `expected_stderr.txt`, `expected_exit.txt` and,
  when the case logs, `expected_log.jsonl` (main log) /
  `expected_hook_log.txt` (fail-open fallback log).
- `install/<scenario>/home/**` is the synthetic HOME template; `case.json`
  lists the invocations; `expected/<id>.report.json` holds each
  `install --json` report, `expected/tree-after-force.json` the installed state
  and `expected/tree.json` the final state after `--uninstall` (backup files
  land in each `backups` list as `<TS>` paths; the launcher binary at
  `<CASE>/bin/rolling-memory` is provided by the runner).  Golden files are
  `0444`: copy the template into a scratch tree and make it writable before
  running install.

## Seal

`MANIFEST.json.files` maps every file except `MANIFEST.json` to its sha256.
`seal_sha256 = sha256(concat(sorted(f"{path}\t{sha256}\n")))`.

## Check

```sh
go test ./internal/golden/... -count=1
```

The golden package verifies the committed MANIFEST seal — every listed file is
re-hashed (this README included), files the manifest does not list are flagged,
and `seal_sha256` is recomputed — and replays the frozen scenarios against the
Go implementation. The cross-check arm that compared the tree against a fresh
regeneration retired together with the legacy driver; a reseal therefore
requires owner approval rather than a silent pass.

## Fixture scenarios

| # | scope | fixture | DB | expectations |
|---|---|---|---|---|
| 010-graph-bands | `mem:golden:010` | `fixtures/010-graph-bands/fixture.json` | `db/010-graph-bands.db` | 13 file(s) |
| 020-raw-fallback | `mem:golden:020` | `fixtures/020-raw-fallback/fixture.json` | `db/020-raw-fallback.db` | 10 file(s) |
| 030-date-window | `mem:golden:030` | `fixtures/030-date-window/fixture.json` | `db/030-date-window.db` | 6 file(s) |
| 031-session-filter | `mem:golden:031` | `fixtures/031-session-filter/fixture.json` | `db/031-session-filter.db` | 6 file(s) |
| 040-anchor-recheck | `mem:golden:040` | `fixtures/040-anchor-recheck/fixture.json` | `db/040-anchor-recheck.db` | 6 file(s) |
| 050-private | `mem:golden:050` | `fixtures/050-private/fixture.json` | `db/050-private.db` | 7 file(s) |
| 060-same-as-merge | `mem:golden:060` | `fixtures/060-same-as-merge/fixture.json` | `db/060-same-as-merge.db` | 12 file(s) |
| 070-superseded-visible | `mem:golden:070` | `fixtures/070-superseded-visible/fixture.json` | `db/070-superseded-visible.db` | 9 file(s) |
| 080-absent-gaps | `mem:golden:080` | `fixtures/080-absent-gaps/fixture.json` | `db/080-absent-gaps.db` | 6 file(s) |
| 090-cjk-emoji | `mem:golden:090` | `fixtures/090-cjk-emoji/fixture.json` | `db/090-cjk-emoji.db` | 6 file(s) |
| 100-no-fts | `mem:golden:100` | `fixtures/100-no-fts/fixture.json` | `db/100-no-fts.db` | 15 file(s) |
| 110-conflicts | `mem:golden:110` | `fixtures/110-conflicts/fixture.json` | `db/110-conflicts.db` | 9 file(s) |
| 120-dry-run | `mem:golden:120` | `fixtures/120-dry-run/fixture.json` | `db/120-dry-run.db` | 5 file(s) |
| 130-batch-remember | `mem:golden:130` | `fixtures/130-batch-remember/fixture.json` | `db/130-batch-remember.db` | 5 file(s) |
| 140-idempotent | `mem:golden:140` | `fixtures/140-idempotent/fixture.json` | `db/140-idempotent.db` | 6 file(s) |
| 150-supersedes-forget | `mem:golden:150` | `fixtures/150-supersedes-forget/fixture.json` | `db/150-supersedes-forget.db` | 7 file(s) |
| 160-caps-offset-probe | `mem:golden:160` | `fixtures/160-caps-offset-probe/fixture.json` | `db/160-caps-offset-probe.db` | 23 file(s) |
| 170-time-ref | `mem:golden:170` | `fixtures/170-time-ref/fixture.json` | `db/170-time-ref.db` | 4 file(s) |
| 180-read-legs | `mem:golden:180` | `fixtures/180-read-legs/fixture.json` | `db/180-read-legs.db` | 19 file(s) |
| 190-meta-closure | `mem:golden:190` | `fixtures/190-meta-closure/fixture.json` | `db/190-meta-closure.db` | 15 file(s) |
| 200-readout | `mem:golden:200` | `fixtures/200-readout/fixture.json` | `db/200-readout.db` | 13 file(s) |
| 201-status-doctor-check | `mem:golden:201` | `fixtures/201-status-doctor-check/fixture.json` | `db/201-status-doctor-check.db` | 7 file(s) |
| 202-deep | `mem:golden:202` | `fixtures/202-deep/fixture.json` | `db/202-deep.db` | 6 file(s) |
| 203-write-aliases | `mem:golden:203` | `fixtures/203-write-aliases/fixture.json` | `db/203-write-aliases.db` | 4 file(s) |
| 204-replay-rewind | `mem:golden:204` | `fixtures/204-replay-rewind/fixture.json` | `db/204-replay-rewind.db` | 8 file(s) |
| 205-portrait | `mem:golden:205` | `fixtures/205-portrait/fixture.json` | `db/205-portrait.db` | 5 file(s) |
| 206-govern-report | `mem:golden:206` | `fixtures/206-govern-report/fixture.json` | `db/206-govern-report.db` | 10 file(s) |
| 207-nodes-vector | `mem:golden:207` | `fixtures/207-nodes-vector/fixture.json` | `db/207-nodes-vector.db` | 7 file(s) |
| 210-export-import | `mem:golden:210` | `fixtures/210-export-import/fixture.json` | `db/210-export-import.db` | 5 file(s) |
| 250-config-egress | `mem:golden:250` | `fixtures/250-config-egress/fixture.json` | `db/250-config-egress.db` | 16 file(s) |
| 270-transcript-cursor | `mem:golden:270` | `fixtures/270-transcript-cursor/fixture.json` | `db/270-transcript-cursor.db` | 6 file(s) |

## Surface scenarios

| # | kind | entry | expectations |
|---|---|---|---|
| 208-cli-matrix | cli | `cli/208-cli-matrix/cases.json` | 172 file(s) |
| 209-mcp-caps | mcp | `mcp/209-mcp-caps/transcript.jsonl` | 1 file(s) |
| 220-mcp | mcp | `mcp/220-mcp/transcript.jsonl` | 1 file(s) |
| 230-hooks | hooks | `hooks/230-hooks/cases.json` | 39 file(s) |
| 240-install | install | `install/240-install/case.json` | 23 file(s) |

All corpus content is synthetic (`老张`, `小白`, `Alice`, `Alicia`, `project-a`, fixed 2023–2026 dates).
No user memory, real session, absolute host path, API key or evaluation corpus is included.

## Coverage

37/37 scenarios regenerated against the pinned oracle `a0fa206`
(product v0.7.4, generator commit `215328b`):

- fixture scenarios `010`–`207`, `210`, `270` plus `250` (config/egress)
  and `260` (sync) — DB + op list + normalized expectations.
- surface scenarios `208` (CLI matrix, 57 invocations covering
  all 35 leaf commands), `209` (MCP non-dict `caps`), `220` (MCP protocol),
  `230` (hooks) and `240` (install across the eight harness registries).

PIN-specific receipt changes are frozen as-is: `remember` carries the
`existing[]` echo, `recall.meta` carries `activation` and the conditional
`raw_error`, config exposes `recall.activation_record`/`activation_rank`,
and `status`/`doctor` version output is normalized to `0.0.1` (the
repository's real version is authoritative in `VERSION`).

The seal is verified by the Go check above.
