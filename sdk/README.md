# RAME Official Client SDKs (`RAME-BlindPulse-v1` Standard)

**[English](README.md) · [简体中文](README.zh-CN.md)**

This directory contains the official **zero-dependency Client SDKs** in **Go, Python, Node.js, and Java** for the **Rolling Autonoetic Memory Engine (`RAME` · `/open/memory/v1/*`)**, along with the cross-language golden test vector suite (`spec/golden_vectors.json`). **Go** and **Python** provide a complete client (bit-for-bit encoder + local plaintext store + client entry points); **Node.js** and **Java** provide the bit-for-bit encoder.

**SDK Version: `1.11.3`** (semver · independently versioned from the wire spec `RAME-BlindPulse-v1`). Every SDK exposes it as a named constant (`ramesdk.SDKVersion` / `SDK_VERSION`). **Any SDK change must bump this version**; the four SDKs must stay byte-for-byte aligned with the wire spec, and no service or host may consume the encoder by importing the engine's internal packages (the engine is a service: access it only through this SDK / the `/open/memory/v1/*` protocol).

---

## 1. Directory Layout

| Language | Path | Runtime & Dependencies | Verification Command |
|---|---|---|---|
| **Go** | [`sdk/go/ramesdk.go`](go/ramesdk.go) | Go 1.26+ standard library only (`crypto/hmac`, `crypto/sha256`, `unicode`) | `go test -v ./sdk/go/...` |
| **Python** | [`sdk/python/rame_sdk.py`](python/rame_sdk.py) | Python 3.8+ standard library only (`hmac`, `hashlib`, `unicodedata`) | `python3 sdk/python/rame_sdk.py <secret> <id> <text>` |
| **Node.js** | [`sdk/nodejs/index.mjs`](nodejs/index.mjs) | Node.js 18+ built-in `node:crypto` (`BigInt` uint64 wire serialization) | `node sdk/nodejs/index.mjs <secret> <id> <text>` |
| **Java** | [`sdk/java/RameSdk.java`](java/RameSdk.java) | JDK 11+ standard library (`javax.crypto.Mac`, `Long.toUnsignedString`) | `java sdk/java/RameSdk.java <secret> <id> <text>` |
| **Spec & Golden Vectors** | [`sdk/spec/golden_vectors.json`](spec/golden_vectors.json) | Canonical cross-language test vectors (English/CamelCase, CJK sliding bigrams, multilingual Japanese/Korean/Arabic/code, punctuation) | Verified automatically by `TestCrossLanguageSDKGoldenParity` |

---

## 2. `RAME-BlindPulse-v1` Deterministic Client Algorithm Specification

To guarantee **zero drift** between the RAME service, the shipped client, and multi-language SDKs while strictly adhering to **100% language-agnostic zero-tuning rules (zero lexicons, zero character-count prefix stem truncation)**, every SDK implements the exact same 4-stage encoder pipeline:

### Stage 1: 256-Bit Local Key Derivation & `KeyedHash64`
- Given a user secret byte string $S$: $K_{\text{user}} = \text{SHA-256}(S) \in \{0,1\}^{256}$.
- Given a surface token string $w$, let $\text{low} = \text{ToLower}(\text{TrimSpace}(w))$ encoded in UTF-8:
  $$h = \text{LittleEndian.Uint64}\bigl(\text{HMAC-SHA256}(K_{\text{user}},\ \text{UTF8}(\text{low}))[0:8]\bigr)$$

### Stage 2: Deterministic Sentence Segmentation (`SplitSentences`)
- Split text on `\n`, `\r`, `。`, `！`, `？`, `；`, `;`, `!`, `?`.
- A period `.` splits a sentence **unless** both its immediately preceding and immediately following Unicode code points are `Letter || Number` (preserving `v1.0.1`, `655.59`, `a.b`).
- Trim leading/trailing whitespace and discard empty segments.

### Stage 3: Language-Agnostic Surface Word & Sliding Bigram Segmentation (`SplitWords`)
- Scan Unicode code points left-to-right, skipping any code point that is neither `Letter` (`L*`), `Number` (`N*`), nor `Mark` (`M*`):
  1. **Contiguous CJK Han Ideographs (`isHanCp`)**:
     - Blocks: `U+4E00..U+9FFF`, `U+3400..U+4DBF`, `U+F900..U+FAFF`, `U+20000..U+2FA1F`.
     - For a contiguous Han run $c_0 c_1 \dots c_{m-1}$: if $m = 1$, emit `[c_0]`; if $m \ge 2$, emit overlapping sliding 2-character bigrams `[c_0c_1, ..., c_{m-2}c_{m-1}]` followed by single characters `[c_0, ..., c_{m-1}]`.
  2. **Contiguous Kana / Thai / Lao / Khmer (`isKanaOrSEAsianCp`)**:
     - Blocks: `U+30FC`, `U+3040..U+30FF`, `U+FF66..U+FF9F`, `U+0E00..U+0EFF`, `U+1780..U+17FF`. Emit maximal contiguous non-Han unspaced run.
  3. **Spaced Scripts & Identifiers (Latin, Cyrillic, Arabic, Devanagari, Hangul, Digits)**:
     - Consume maximal contiguous run of `Letter || Number || Mark`, allowing internal connectors `-`, `_`, `.` when immediately followed by another spaced `Letter || Number || Mark`.
     - Emit the full token followed by compound subwords split on connectors, `aB` CamelCase, and letter-number boundaries.

### Stage 4: Carrier Width `b` (`uint16`) & Morphological Bitmask `f` (`uint8`)
- Deterministic carrier width `CarrierUnits(r)`: ASCII letter $\to 20$, ASCII digit $\to 33$, other ASCII $\to 10$; Han $\to 100$, Hangul $\to 70$, Kana/SEAsian $\to 42$, Unicode `Nd` $\to 33$, Unicode `L*` $\to 20$, other $\to 10$.
- Repeat damping: if $\text{ToLower}(r)$ repeats consecutively for $\text{runLen} \ge 3$, divide its carrier unit by $4$. Clamp $b = \min(65535, \max(10, \sum rw))$.
- Bitmask $f$: `1<<0` uppercase (`wordIdx > 0 || idx > 0`), `1<<1` CamelCase (`idx > 0 && hasLower`), `1<<2` number (`N*`), `1<<3` connector (`-`, `_`, `.`).

### Stage 5: Pure Protocol Exchange & Local Plaintext Resolution (`PlaintextStore` × `Client`)
- **3 Local Memory Layers**: `background` (`LayerBackground`), `episodic` (`LayerEpisodic`), `noetic` (`LayerNoetic`).
- **4D Spatial Physical Coordinates & Free Block Slicing**: `UnitMeta` (including `file_parse`), `SubUnit`, `PackCompositePackets` (parent + sub-unit composition with `cited_unit_ids`), `BlindPulsePacket`, and `CloudQueryHit` carry optional plaintext-free spatial commitments (`doc_h`, `block_h`, `line_start`, `line_end`) derived via `KeyedHash64` from `file_url` and `block_id`, enabling same-document and adjacent-line resonance across arbitrary document slices without exposing file paths or structure to the cloud.
- **Canonical `RMBP` (`atoms_b64`) Binary Wire Payload & Built-in `/health` Codec Verification**: All SDKs encode `{h, b, f}` slots and sentence frames into the canonical `RMBP` Golomb-Rice mixed-radix `atoms_b64` payload (`EncodeWirePayload` / `DecodeWirePayload`) and provide built-in codec self-verification (`VerifyCodecLocal` and `Client.VerifyCodec` / `Client.Health` against `/open/memory/v1/health`).
- **Strict Cloud Exchange**: `Client.IngestPacket`, `Client.Remember*`, `Client.QueryHits`, `Client.Recall`, `Client.VerifyAuth`, `Client.Health`, `Client.VerifyCodec`, `Client.ListCloudUnitIDs`, `Client.UpdateUnit`, and `Client.DeleteUnits` exchange cipher pulses (`pulse_cipher`) or plaintext pulses (`pulse_plain`) over `/open/memory/v1/*`, validate echoed `tenant_id / scope`, and resolve returned Unit IDs against the local `PlaintextStore` (`MemoryStore`, `JSONFileStore`, or custom store).

---

## 3. Custom Word Segmentation (BYO-Segmenter) & Retrieval Quality Advisory

> **Developer Advisory**
>
> Developers can use their own custom word segmentation on the client side (domain lexicons, AST tokenizers, or third-party segmenters)—the cloud engine only consumes `{h, b, f}` integer pulses and `sents` sentence frames. **However, because `h` is an irreversible cryptographic commitment, client-side segmentation quality directly governs retrieval recall, co-occurrence topology, and dry-sentence distillation across the entire memory system:**
> 1. **Avoid Un-Harmonized Glued Monoliths (`b > 320`)**: Emitting whole sentences or long clauses as a single token without subword harmonics causes partial queries to miss whenever a single word differs.
> 2. **Avoid Single-Character Dust (`b <= 20`)**: Splitting spaced text exclusively into single characters saturates document frequency (DF) and flattens waveform contrast.
> 3. **Keep Endogenous Adaptive Harmonics Enabled**: When plugging a custom segmenter into the Go thin client (`Vault.SetWordSegmenter`), keep adaptive harmonic emission enabled (the default) so custom multi-word tokens automatically emit width-conserving sub-carrier harmonics ($\sum B_{\text{sub}} = B_{\text{parent}}$), preserving both custom-phrase precision and 100% fallback subword recall.
> 4. **Prefer Non-Destructive Canonical Harmonic Overlay (`SetCanonicalHarmonics`) Over Replacement Dictionaries**: For structured dates (`2026/11/8`, `2026.11.08`, `2026年11月8日` $\to$ `2026-11-08` ISO beacon) or cross-lingual unit/toponym equivalence (`kg` $\leftrightarrow$ `千克`, or the built-in opt-in ISO 3166 L0–L3 multilingual geographic hierarchy expander `LookupISO3166GeoHarmonics` / `lookup_iso3166_geo_harmonics` / `lookupISO3166GeoHarmonics` mapping cities/provinces/countries across 13+ languages to `@geo:cc:<iso2>` and `@geo:cont:<cont>`), enable `SetCanonicalHarmonics` (`EnableStructuredDates` + `HarmonicExpander`) to inject companion harmonic slots (`b = B_even - 1` / `B_even - 3`, `f = FlagHasConn | FlagIsHarmonicCompanion`) without removing or mutating native segmentation slots.
> 5. **Monitor Cloud `pulse_signal_score` (`0..100`)**: Inspect `pulse_signal_score`, `pulse_signal_grade` (`S/A/B/C/F`), and `pulse_signal_warnings` returned by `/open/memory/v1/ingest` and `/open/memory/v1/doctor` (a score `< 70` indicates client segmentation is degrading memory retrieval capability).

---

## 4. Canonical Harmonic Tables & Opt-In Gain Add-Ons (`SetCanonicalHarmonics` · Non-Destructive Blind Tag Normalization)

### 4.1 What Are Canonical Harmonic Tables (`SetCanonicalHarmonics`)?
When a client hashes surface tokens directly with `HMAC-SHA256(K_user, lower(w))`, two equivalent expressions across languages, symbols, or date formats—such as `"50 kg"` vs. `"50 千克"`, `"$100"` vs. `"100 USD"`, `"Tokyo"` vs. `"東京"`, or `"2026/11/8"` vs. `"2026年11月8日"`—produce completely unrelated 64-bit hashes `h`.

**Canonical Harmonic Tables** are **optional client-side gain add-ons** (`SetCanonicalHarmonics` / `set_canonical_harmonics` / `setCanonicalHarmonics`) that attach an additional **normalized blind companion tag** alongside the original surface token pulse before the packet leaves the client:
1. **Built-In Four-Language SDK Capabilities**:
   - **Structured Date ISO Beacons (`EnableStructuredDates`)**: Zero-lexicon state machine recognizing `YYYY/M/D`, `YYYY.MM.DD`, and `YYYY年M月D日` and emitting a companion cipher pulse for the canonical `YYYY-MM-DD` string (`b = 284`, `f = FlagHasDigit | FlagHasConn`). Since SDK `1.11.0` the same state machine also emits the dedicated calendar-beacon companion pulse `H(K_user, "cal:YYYY-MM-DD")` (`b = 588`, same `f`) immediately before the ISO pulse — an opaque calendar anchor the cloud engine consumes for calendar-axis resonance. The addition is purely additive: the ISO pulse above is unchanged, and the companion appears only when `EnableStructuredDates` is on.
   - **ISO 3166 L0–L3 Geographic Hierarchy Table (`LookupISO3166GeoHarmonics` / `lookup_iso3166_geo_harmonics` / `lookupISO3166GeoHarmonics`)**: Maps multilingual country, region/province, and major city names to canonical 1st-order (`@geo:cc:<iso2>`) and 2nd-order (`@geo:cont:<cont>` / `@dim:geo`) tags.
   - **Merged Closed-Set Harmonics Table (`LookupClosedSetHarmonics` / `lookup_closed_set_harmonics` / `lookupClosedSetHarmonics`)**: One built-in lookup over the merged closed-set axes—geography, kinship, roles, calendar, biology, objects, venues, and SI/IEC/ISO 4217 units with statistical dimension tags (`@unit:*` / `@dim:*`)—composable with custom expanders via `CombineHarmonicExpanders` / `combine_harmonic_expanders` / `combineHarmonicExpanders` and enabled through `SetCanonicalHarmonics`.
   - **Custom `HarmonicExpander` Callback**: Any application can map domain symbols or ontology terms to up to 2 canonical tags per token.
2. **Go Client Companion Suite (`internal/langpack`)**:
   - **Universal SI / ISO 4217 / Statistical Core (`langpack/common`)**: Maps international currency symbols/codes (`$`, `€`, `£`, `¥`, `₩`, `₹`, `usd`, `eur`, `cny` $\to$ `@unit:curr:*`, `@dim:money`), physical measurement symbols (`kg`, `km`, `kwh`, `psi`, `gb` $\to$ `@unit:*`, `@dim:*`), and statistical notations (`%`, `p99`, `avg`, `max` $\to$ `@unit:stat:*`, `@dim:stat`).
   - **Multilingual Measurement & Morphological Pack (`langpack/multi` & per-language packs)**: Aligns multilingual unit words (`kilograms` / `千克` / `公斤` / `キログラム` / `킬로그램` / `กิโลกรัม` $\to$ `@unit:mass:kg`, `@dim:mass`) and morphological variants onto the same canonical tags.

### 4.2 Client-Side Mechanism: Attaching a Blind Normalized Label
Harmonic tables operate **100% on the client side** as a pure blind-tag overlay:
1. **Keep the Primary Blind Pulse Intact**: For a surface token $w$ (e.g., `"千克"` or `"kg"`), the encoder first emits its native primary atom `{H(w), b = B_even, f}`—the original surface token is never deleted or replaced.
2. **Append Blind Companion Pulses**: When the harmonic table matches $w$ to canonical tags $t_1, t_2$ (e.g., `t_1 = "@unit:mass:kg"`, `t_2 = "@dim:mass"`), the encoder hashes those tags with the **exact same user master key $K_{\text{user}}$** and appends up to two companion atoms immediately after the primary atom:
   - **1st-Order Companion (Exact Canonical Unit / Country)**: `{H(t_1), b = B_even - 1, f = FlagHasConn | FlagIsHarmonicCompanion}`
   - **2nd-Order Companion (Dimension / Continent Category)**: `{H(t_2), b = B_even - 3, f = FlagHasConn | FlagIsHarmonicCompanion}`
3. **100% Cipher Pulse Consistency (Server-Irreversible, Zero Plaintext)**: Because `H(t_1)` and `H(t_2)` are computed locally via `HMAC-SHA256(K_user, tag)` into 64-bit integer pulses (`{h, b, f}`), **harmonic values are themselves cipher pulses, never plaintext**. Because the server does not hold $K_{\text{user}}$, it can never reverse a harmonic hash `h` or know what the harmonic content represents (it cannot tell whether a companion pulse came from a weight unit, a currency, a date, or a city). Whether a user writes `"50 kg"` or `"50 千克"`, both packets retain their distinct primary hashes (`H("kg")` vs. `H("千克")`) while sharing the identical companion hash `H("@unit:mass:kg")`—effectively **attaching a shared normalization label to both cipher tokens without revealing either the token or the label to the server**.

### 4.3 Why It Is a Pure Gain Add-On (Not Cheating, Optional by Design)
- **Works Without It (Zero Hard Dependency)**: The memory engine and all four SDKs run out of the box with `SetCanonicalHarmonics` disabled (`EnableStructuredDates = false`, `Expander = nil`). Without harmonic tables, exact token and subword recall work normally; enabling harmonic tables simply provides additional cross-notation and cross-lingual recall gain.
- **Blind Tag Normalization, Not Corpus Tailoring or Cheating**:
  - **Harmonic Values Are Blind Pulses (Server Cannot Reverse or Inspect Them)**: Canonical strings like `"@unit:mass:kg"` or `"@geo:cc:jp"` exist only inside client memory during encoding and **never appear on the wire**. On the wire and on the server, a harmonic value is just an opaque 64-bit integer `h` derived with the user's secret key $K_{\text{user}}$—the server has zero knowledge of the harmonic's meaning or category.
  - **Zero Corpus Mutation**: The user's raw text in local storage and the primary token pulses are never altered, fabricated, or filtered.
  - **Universal Standards Only**: Built-in tables encode only objective international standards (ISO 8601 dates, ISO 4217 currencies, SI/IEC physical units, ISO 3166 geography)—never benchmark-specific answers, user entities, or ad-hoc prompt rules.
  - **Strictly Additive Companion Flag (`FlagIsHarmonicCompanion`)**: Every harmonic slot explicitly declares itself as a companion tag (`b = B_even - 1` / `B_even - 3`, `FlagIsHarmonicCompanion`), acting strictly as a secondary normalization index rather than masquerading as raw text.

---

## 5. License

Apache License 2.0 — see [LICENSE](LICENSE) in this directory. The repository root is separately licensed under the PolyForm Noncommercial License 1.0.0 (see [../LICENSE](../LICENSE)); third-party attributions are in [../NOTICE](../NOTICE).

