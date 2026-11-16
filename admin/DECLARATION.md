# Declaration, Release Boundary & Disclaimer

**[English](DECLARATION.md) · [简体中文](docs/zh-CN/DECLARATION.md)**

---

## 1. Release Boundary: what ships here, and what does not

**Rolling Memory** is a local-first memory tool for AI coding agents, built on the **Rolling Autonoetic Memory Engine**. This repository ships **pure local mode (`local_plain`)** and contains the complete, auditable client side of the system: the CLI and multi-harness plugin, the local SQLite plaintext vault, the cross-language SDKs, the transport-layer egress gate, and the `rmem eval` evaluation runner.

The high-order **cloud memory engine**, **encrypted pulse mode (`pulse_cipher`)**, and **proprietary research cores (such as high-order relational cluster expansion and multimodal re-grounding)** are closed-source and are **not** open or included in this repository. Nothing shipped here requires them: this release runs 100% in local plaintext mode (`local_plain`) fully offline with zero external network requests.

As cryptography and trustworthy-AI researchers, we reject disingenuous excuses — **we never claim "security through obscurity"** (hiding code to protect user data). Under **Kerckhoffs's Principle**, true data privacy relies on a verifiable trust boundary and user-visible data paths, never on hiding client-side code. The boundary above rests on three objective engineering principles:

1. **Minimal auditable TCB & verifiable local data path**:
   - The two critical questions for any user are: **"Where does my data actually live?"** and **"Can I verify that nothing leaves my machine?"**
   - The local client, the SQLite plaintext vault, the standard SDKs, and the transport-layer egress gate are deliberately small, so that a security engineer can audit them directly and confirm that **raw text and the local master key never leave the local database** in the shipped local mode.
   - All plaintext and keys reside in a single local SQLite file with `0700/0600` OS permission isolation (inheriting FileVault / LUKS / BitLocker full-disk encryption); L1 `Background` uses Source-in-Place Live Read (zero full-text duplication); the built-in local FTS5 fallback plus open SQLite / pluggable local-store formats keep the vault 100% offline-usable and free of vendor lock-in (see [`docs/privacy.md`](docs/privacy.md)).
2. **Zero local compute overhead for the on-device plane**:
   - Heavy global co-occurrence manifolds, multi-granularity episodic splitting & addressing, fractal dry-sentence distillation (stripping repetitive OCR loops and boilerplate), and cross-device synchronization are cloud-plane concerns. The on-device plane therefore stays at **millisecond latency with near-zero memory overhead**, with no background model calls and no embedding API bills.
3. **Adversarial anti-poisoning defense & no vendor lock-in**:
   - Agents operating in open environments face **Adversarial Memory Poisoning** — malicious web pages or untrusted tool outputs attempting to hijack Top-1 recall via keyword stuffing or repetition loops;
   - Your plaintext lives in your own local SQLite file and can be exported at any time; nothing in the local plane depends on a service account.

> **Evaluation corpora are not redistributed with this repository.** Both corpora are published upstream under **CC BY-NC 4.0** (non-commercial); the benchmark datasets behind our published readings are obtained by the reader from the upstream benchmark projects and pinned with the published `SHA-256` digests — see [`docs/benchmarks.md`](docs/benchmarks.md). Reproducibility rests on those pins and on the frozen scoring protocol, **not** on a bundled corpus.

## 2. Honest Disclosure & Disclaimer

1. **Collaboration Model**: This project uses a **"human-defined invariants & acceptance criteria + AI-assisted implementation"** workflow.
2. **Benchmark Integrity & Open Verification (BYO-Corpus + BYO-LLM Cost Notice)**: published readings carry cryptographic corpus pins (`SHA-256`) and judge-prompt pins (`SHA-1`). The corpora themselves are **supplied by the reader** from the upstream benchmark projects; tampering with evaluation corpora or manually patching memory units for benchmark questions is strictly forbidden. **Anyone can reproduce the retrieval path at any time with the `rmem eval` runner** once a corpus is in place (deterministic retrieval runs offline at $0 cost; end-to-end QA + LLM judge via `--answerer llm --judge llm` requires your own LLM endpoint and your own token budget). Two behaviours are intentional and different: a corpus-dependent **test skips** when no corpus is present, so the repository's own test suite stays green without one; **`rmem eval` itself exits with a clear, actionable error** rather than producing a reading out of nothing.
3. **Limitations**: We are **not necessarily right, and not necessarily smart**. Both human engineering and AI involvement can introduce defects, edge-case omissions, and inconsistencies.
4. **No Warranty ("AS IS")**: **We make no commitment or guarantee regarding the quality of this software** — no guarantee of correctness, security, completeness, fitness for a particular purpose, or long-term maintainability.
5. **Assumption of Risk**: Users must **judge for themselves and bear all consequences**. Review, test, and assess risks independently before running this software. In no event shall the authors or licensor be liable for any direct, indirect, incidental, or consequential damages.
6. **License Boundary (Noncommercial Use Only · Commercial Use Licensed Separately)**: Released under the [PolyForm Noncommercial License 1.0.0](LICENSE) (SPDX: `PolyForm-Noncommercial-1.0.0`). The full source code is free to use, modify, and share **for any noncommercial purpose**. **Commercial use, enterprise production deployment, managed SaaS hosting, and commercial distribution are prohibited** without a separate written commercial license from the authors. The client SDKs under `sdk/` are separately licensed under Apache-2.0 (`sdk/LICENSE`). Third-party components retain their respective upstream licenses as detailed in [NOTICE](NOTICE).

## 3. Contributing

- **Contributing guide**: [`CONTRIBUTING.md`](CONTRIBUTING.md) — development setup, test commands, and the pull-request checklist.
- **Issues & feature requests**: <https://github.com/rocareer/rolling-memory/issues> — reproducible bug reports and capability proposals are welcome.
- **Support scope**: [`SUPPORT.md`](SUPPORT.md) — what is and is not supported, and the response expectations.
- **Security reports**: [`SECURITY.md`](SECURITY.md) — report vulnerabilities privately; do not open a public exploit issue.

---

This declaration is an honest statement of position; it constitutes no warranty or legal undertaking of any kind. Independent recomputation, academic discussion, and corrections are welcome.
