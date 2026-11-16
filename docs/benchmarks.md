# Benchmarks & Reproducibility Guide (`rmem eval`)

**[English](benchmarks.md) · [简体中文](zh-CN/benchmarks.md) · [FAQ](faq.md) · [Documentation Hub](README.md)**

Rolling Memory ships a one-command, frozen-protocol evaluation runner (`rmem eval`) for the University of Cambridge ATM-Bench (primary benchmark) and ACL 2024 LoCoMo (comparative reference). **The benchmark corpora themselves are not redistributed with this repository** — bring your own copy obtained from the upstream projects and pin it with the `SHA-256` digests published below. The default evaluation arm is **`local_plain`**: on-device retrieval over the local plaintext vault with zero network requests. Cloud pulse arms belong to the application-based cloud preview (see [privacy.md](privacy.md)) and are out of scope for this release's documentation.

> **Evidence requirement**: every published score must carry actual provenance (`transport / scope / corpus_sha256 / commit / per-question retrieval IDs`). Oracle is a human-labelled ceiling, not an engine retrieval score.

---

## 1. Corpus & Evaluation Data Plane

### 1.1 Bring your own corpus

Two public benchmarks are used. Neither is bundled; both are obtained from the upstream project and then pinned locally with the digests in §1.2.

**Data licensing**: both corpora are published by their authors under **CC BY-NC 4.0** (non-commercial). ATM-Bench's dataset card states "This dataset is released under CC BY-NC 4.0. The accompanying code is released under the MIT License." (data: <https://huggingface.co/datasets/Jingbiao/ATM-Bench>); LoCoMo ships a CC BY-NC 4.0 `LICENSE.txt` (<https://github.com/snap-research/locomo>). Because of those non-commercial terms, both corpora — and the derived tier files — are **not redistributed** with this repository or its release assets; obtain them from the upstream projects under the upstream terms and pin them with the digests below.

**One-command fetch**: `rmem eval --fetch atm --accept-upstream-license` downloads the required ATM-Bench files (~35 MB: emails, pre-processed image/video captions, and both question sets) from the pinned Hugging Face revision, rebuilds the pinned corpus and tier files **on your machine**, checks them against the `SHA-256` pins below, and then runs the evaluation. `rmem eval --fetch locomo --accept-upstream-license` does the same for LoCoMo: it downloads the upstream `locomo10.json` (~2.8 MB, pinned revision) and rebuilds the upstream-official Cat 1–4 question set (1,540 questions, with official evidence) plus the 5,882-turn corpus, recording the upstream revision and the derived digests in `UPSTREAM-PROVENANCE.json`. Both downloads happen under the upstream data license (CC BY-NC 4.0), which the flag explicitly accepts; `--fetch-only` stages the data without running, and re-runs skip any file whose digest already matches. Data lands in your local data directory (`<dir>/atmbench` for ATM when `--eval-data <dir>` is given; LoCoMo uses the data directory root).

| Benchmark | Upstream source | Corpus |
|---|---|---|
| **ATM-Bench** (primary) | University of Cambridge ATM-Bench — leaderboard site <https://atmbench.github.io/leaderboard.html> | SGM Multimodal Personal Memory Corpus: 6,742 emails + 3,759 photos + 533 videos = **11,034** items |
| **LoCoMo** (comparative reference) | ACL 2024 LoCoMo | LoCoMo10 standard snapshot: **5,882** dialogue turns; question set = the upstream-official Cat 1–4 collection (**1,540**). The former convenience tiers 100/200 are retired and are not provided in any form. |

The runner accepts either of two on-disk forms:

**(a) The pinned asset pack** (what our own readings were produced against):

```text
<eval-data>/
  MANIFEST.json                 # corpus + tiers + upstream source declaration
  corpus.jsonl                  # LoCoMo, 5,882 rows
  tiers/questions-1540.json     # 1,540 = upstream-official cat1-4 set (with official evidence)
  atmbench/
    MANIFEST.json
    corpus.jsonl.gz             # ATM-Bench SGM, 11,034 rows
    tiers/questions-1013.json   # Main-1013
    tiers/questions-hard.json   # Hard-31
```

**(b) The upstream raw ATM-Bench layout** (no repacked `corpus.jsonl.gz`, no repacked tier files):

```text
<eval-data>/
  raw_memory/email/emails.json
  processed_memory/image_batch_results.json
  processed_memory/video_batch_results.json
```

Layout (b) is loaded directly by the SGM loader, but the `SHA-256` pre-verification in §1.2 applies to form (a) only — against a raw directory, pass `--no-verify` to skip digest pre-verification explicitly.

**Pointing the runner at your directory**: `rmem eval --eval-data <dir>`. Without that flag the resolver walks, in order: `ROLLING_MEMORY_EVAL_DATA` → next to the executable (and up to two parent levels) → the working directory and up to four ancestors → `$XDG_DATA_HOME/rolling-memory/evaldata` → `$LOCALAPPDATA/rolling-memory/evaldata` → `~/.local/share/rolling-memory/evaldata`, `~/Rolling Memory/evaldata`, `~/rolling-memory/evaldata`. ATM-Bench additionally honours `ROLLING_MEMORY_ATM_DATA` and a `raw_memory/email/emails.json` marker.

### 1.2 Published corpus pins

```text
ATM-Bench atmbench/corpus.jsonl.gz      c9c4cc55dd3be147638121765b5dc7b8321a5e292b90909b68ae7fe8a57ee3a7
ATM-Bench atmbench/tiers/questions-1013.json  3c4cbf84deeb158c78d515626a540908878d304ce0cda5c8e2ae7e52c6b878a2
ATM-Bench atmbench/tiers/questions-hard.json  f6f63ff196a9f1de5715e35b328d4e80ca80311f2e2eae8028e7cb60e87ad37e

# Upstream file pin (verified by `rmem eval --fetch locomo`):
#   data/locomo10.json  79fa87e90f04081343b8c8debecb80a9a6842b76a7aa537dc9fdf651ea698ff4  (revision 3eb6f2c5)

# Archived LoCoMo pack (kept for historical-reading provenance only; no longer distributed
# and not byte-reproducible — the files embedded a build timestamp):
#   LoCoMo corpus.jsonl              6c7e5ca22618d9632d71b24b698e966f9bd296ebf1211992bb559e1665185440
#   LoCoMo tiers/questions-100.json   48688b20c7dcd5cdca4cd355e04b7d94bb005b8e3523c66fedb33906a0acfeb5
#   LoCoMo tiers/questions-200.json   0734f89871035f80ef5a46e65756123f56b1f6df2c77f6500f48ab7c953489ef
#   LoCoMo tiers/questions-1540.json  6dd815a5fcca55eabee6345fca326192b167950aa845b7e8d330c1d43c2eee5f
```

The ATM-Bench corpus digest is additionally compiled into the runner as an enforced pin, so a repacked corpus that does not match is rejected before any question is scored.

**Honest limitation (derived artifacts)**: the ATM-Bench tier files are project-derived and rebuild byte-identically: `rmem eval --fetch atm` reconstructs the corpus and both tier files from the pinned upstream revision on your machine and verifies them against the `SHA-256` pins above — a passing rebuild is byte-for-byte the same input our readings were produced on. The LoCoMo question set follows the upstream: `rmem eval --fetch locomo` rebuilds the official Cat 1–4 set (1,540) deterministically from the pinned upstream revision, recording the upstream revision and derived digests in `UPSTREAM-PROVENANCE.json`; it is not byte-identical to the archived historical pack (those files embedded a build timestamp and a private intermediate pool), which is not distributed. The retired convenience tiers 100/200 are not provided in any form.

### 1.3 Execution plane

- **On-device**: retrieval runs against the local SQLite plaintext vault (FTS5 + BM25 + integer IDF), which also stores the resolved plaintext for every returned Unit ID.
- **Zero egress**: an evaluation never sends conversations, questions, or results to a remote endpoint; it is reproducible offline.
- **Fail-closed**: a failed vault open, missing data directory, corpus digest mismatch, or prepare-manifest mismatch aborts the run; results are never padded from a second source.
- **Missing corpus is never silently scored — and never breaks the test suite**: corpus-dependent tests **skip** rather than fail when no corpus is present, so the repository's own test suite stays green without one; `rmem eval` itself exits with a clear, actionable "data directory not found" error instead of emitting an empty or fabricated reading.
- **Evaluation namespaces**: each run receives its own generation scope inside a dedicated evaluation namespace; your personal memory scope (`default`) and historical evaluation scopes are never reused for new runs. Idle older generations are reclaimed on the next evaluation, while running evaluations are never evicted.

---

## 2. Run Retrieval Evaluation

These commands disable only **Answer/Judge LLM calls**; retrieval itself stays fully local. Add `--eval-data <dir>` if your corpus lives outside the default search path.

```bash
# Primary benchmark — ATM-Bench: Hard-31 and Main-1013
rmem eval --suite atm --tier hard --answerer none --judge off --top-k 50
rmem eval --suite atm --tier 1013 --answerer none --judge off --top-k 50

# Comparative reference — LoCoMo: full-corpus ingest, then the official Cat 1–4 set (tier 1540; the convenience tiers 100/200 are retired)
rmem eval --tier 1540 --answerer none --judge off --top-k 20

# Minimal smoke run: 3 corpus units and 1 question in an isolated vault
rmem eval --tier 1540 --limit 1 --corpus-limit 3 \
  --answerer none --judge off --top-k 3 \
  --eval-store /tmp/rame-eval-smoke.json --out-dir /tmp/rame-eval-smoke-out
```

**Prepare-manifest controls**: `--rebuild` allocates a new generation scope for the run and retires idle older generations (inside the evaluation namespace only); `--reuse-prepare` reuses an existing prepare state only after an identical scope/digest check; `--verify-prepare` re-verifies the manifest and corpus read-only, with no ingestion or scoring.

`--mode`, `--retrieval`, `--eval-db`, and graph-delivery cap flags are not supported in reproduction commands.

---

## 3. Optional Frozen Answer/Judge

End-to-end QA requires a user-provided OpenAI-compatible model and its inference budget. Prompt bytes and SHA-1 pins are unchanged:

```bash
export ROLLING_MEMORY_EVAL_API_KEY="your-own-llm-api-key"

rmem eval --tier 1540 \
  --answerer llm --judge llm \
  --eval-base-url "https://api.deepseek.com/v1" \
  --eval-model "deepseek-chat" --top-k 96 --context-chars 24576
```

| Protocol component | SHA-1 pin |
|---|---|
| Answer System | `0d59162805571b5622b50446ee8da12490737ade` |
| Judge System | `497f78929df5d4479c84ae7dd9993b4161f3a475` |
| Judge User Template | `38f9b6a17ffd58bf58bb123de44ce40442f1a85b` |

---

## 4. Independently Falsify & Recompute

Each run writes `summary-sdk-<arm>-*.json` and `results-sdk-<arm>-*.jsonl` under `<out-dir>` (the arm is part of the file name), plus `<eval-store>.manifest.json`. Verify at least:

1. the recorded `transport` names the active arm, and `local_fallback` is `false`;
2. `scope` matches the dedicated evaluation namespace (never your personal scope);
3. `corpus_sha256` matches the corpus you supplied (compare it against the §1.2 pin for the same file) and `documents` equals the number of ingested units;
4. every question records its retrieval unit IDs, and every Unit ID resolves in the isolated local vault;
5. recompute `evidence_hit` and `R@10/R@25/R@50` directly from raw JSONL rather than trusting terminal output;
6. cross-probe another question or scope to prove that real user memories and other suites are invisible.

`--oracle` remains available as an explicit gold ceiling. Its `transport=oracle-gold` must never be presented as engine retrieval performance.

---

## 5. Reporting & Leaderboard Rules

- **Provenance**: every evaluation records the storage mode, scope, corpus SHA-256, commit, and per-question retrieval IDs. A reading without that provenance is quarantined, not published.
- **Frozen commit**: publish numbers only from a frozen commit and corpus, with the scoring protocol and limitations stated in the same document.
- **No cross-arm mixing**: a local reading is never presented as a remote-engine result, and historical readings that lack provenance are not reused.
- **Leaderboards**: the readings published here are not official ATM/LoCoMo leaderboard entries unless the external leaderboard accepts the exact arm and protocol recorded in the submission.

---

## 6. Local-Mode Reference Disclosure (`local_plain`)

The default `local_plain` arm runs 100% offline over the local SQLite plaintext vault (`FTS5` + `BM25` + integer self-information continuous gain and dimensionless event-chain coherence) with zero network requests, zero vector indexes, and zero ingestion LLM calls. Daily plugin usage (`rmem recall` / MCP `recall`) and the `rmem eval` runner share the exact same local engine, sentence-harmonic encoder, and ranker.

| Suite / Split | Corpus Units (`corpus_sha256` prefix) | Official `Recall@10` (GT Coverage Mean) | Auxiliary `Any-Hit@10` | `Recall@10GT` (All Gold in Top-10) | End-to-End `QS` (`gpt-5-mini` minimal judge) |
|---|---:|---:|---:|---:|---:|
| **ATM-Bench (`Main-1013`)** | 11,034 (`c9c4cc55…`) | **77.21%** | **80.45%** (`815/1,013`) | **73.94%** (`749/1,013`) | **65.54%** (`DeepSeek-V4.1-flash` answerer) |
| **ATM-Bench-Hard (`Hard-31`)** | 11,034 (`c9c4cc55…`) | **46.12%** | **80.65%** (`25/31`) | **22.58%** (`7/31`) | **27.38%** (`DeepSeek-V4.1-flash` answerer) |
| **LoCoMo (`tier 1540` official Cat 1–4)** | 5,882 (`6c7e5ca2…`) | n/a (tier 1540 carries no gold evidence) | n/a | n/a | **85.26%** (`1313/1540` · cat1 70.57 / cat2 87.23 / cat3 71.88 / cat4 90.96) |

- **Scope & Limitations**: `local_plain` provides deterministic on-device lexical-statistical retrieval (`transport = ramesdk.local_plain.offline-v1`, `local_fallback = false`) and is not equivalent to remote engine manifold precision. `Hard-31` contains 31 multi-evidence questions ($\pm 1$ question $= \pm 3.23$ percentage points on binary hit metrics).
- **LoCoMo run provenance (2026-11-16)**: `answerer + judge = deepseek-v4.1-flash` (frozen protocol, judge system SHA-1 `497f7892…`), `--top-k 96 --context-chars 24576`, 48 workers, 2,040.6s, 0 unparsed / 0 error; tokenz-r delivery mean 5,164.7 tokens per question, recall mean 371ms; buckets HIT 1313 / JUDGE_AMBIGUITY 73 / RECALL_MISS 154; eval ledger `run-20261116-170142` (1,540 per-question rows). The judge is deepseek (not the leaderboard's GPT-4.1) — compare against leaderboards accordingly; the same-judge 2026-11-13 same-arm reading was 82.01% (+3.25pp, not a paired A/B). The retrieval kernel itself reads **r@10 = 57.09% / r@50 = 75.51%** (gold-set-mean) over the pinned `tiers/questions-200.json` evidence arm; tier 1540 has no gold evidence, so its `evidence_hit` is identically 0 by construction, not by measurement failure.
