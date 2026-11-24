# Collaboration diagnostics

This suite checks what an assistant does when local memory is useful, unnecessary, incomplete, or superseded. It ships synthetic cases only; it does not read a user's memory database or session history.

## Run locally

Start a local OpenAI-compatible model server that supports function calls and seeds. For example, with a model file you already have:

```sh
llama-server --model /path/to/model.gguf --host 127.0.0.1 --port 32190 --parallel 2
```

From the repository root, build Rolling Memory and run the diagnostics. A run needs a new output directory. The path to the weights is used to record a SHA-256 digest; it must be the actual weights served by the endpoint. Supply the correct model name and server build label.

```sh
go build -trimpath -o /tmp/rm-diagnostic-bin ./cmd/rolling-memory
go run ./diagnostics/collaboration \
  -binary /tmp/rm-diagnostic-bin -skills skills/rolling-memory \
  -endpoint http://127.0.0.1:32190 -model local \
  -weights /path/to/model.gguf -server 'llama.cpp BUILD / MODEL' \
  -out /tmp/rm-collaboration-run
```

Add `-baseline /path/to/older/rolling-memory -baseline-skills /path/to/older/skills/rolling-memory` for a paired run. Use the skill copies that actually accompanied each executable. The runner reads each executable's MCP handshake and tool descriptions; for a paired run, both arms' generated queries are replayed through the **current** executable against the same synthetic vault. This isolates the assistant-facing prompts rather than comparing search engines.

Recompute the readout without a model or a running memory service:

```sh
go run ./diagnostics/collaboration -review /tmp/rm-collaboration-run/receipt.json
```

## Read the result

[Public cases v4](cases-v4.json) contains 52 scenarios in English and Chinese. It extends v3 with an `excerpt` group: fact questions whose decisive marker sits deep in the source, past the point a compact receipt (probe) would truncate. Each arm runs three fixed seeds. Seeds repeat the same cases; they are not independent new cases.

- **Correct action**: the assistant selects the expected next action: search history, inspect a current location, request a cited unit, or respond without a tool.
- **Necessary and unnecessary recall**: how often the assistant searches when it should or should not.
- **Evidence hit**: a fact question counts a hit only when the full source excerpt was delivered (mode omitted or `full`) and the returned text contains the scenario's unique marker. A search with an unnecessary default parameter can still hit the evidence.
- **Summary standing in for excerpt**: a fact question answered with a `probe` or `recent` compact receipt is counted separately, even when its summary happens to retain the marker. That is the adversarial dimension v4 adds.
- **Correct source**: the model requests the expected unit by ID. The action is scored; `read_source` and `inspect` are test tools, not additional MCP tools or executed file reads.
- **Strict**: the action and relevant arguments pass (a normal recall sends just `cue`); a historical search must also hit the evidence. A direct response is checked for nonempty, completed output, **not graded for semantic correctness**. Review its raw text for unsupported certainty or claims that a check was performed.
- **All seeds passed**: distinct cases that pass the strict check in every run. This shows repeated behavior, not statistical significance.

`receipt.json` records executable, prompt, model, suite, and result digests plus counts by case family and language. `observations.jsonl` holds each tool selection, argument, direct response, and replayed historical result. Keep both together. A model transport error or incomplete run does not produce a complete receipt; failed individual cases stay in the denominators. Compare versions on the same suite, model weights and settings; changes to these inputs start a separate series. This suite measures first actions on synthetic scenarios, not production adoption or final-answer quality. See [the benchmark guide](../../docs/benchmarks.md) for other evaluations.

## Add cases

Use synthetic records only. Include a similar but incorrect record when the task needs a past decision. Keep the evidence marker unique and out of the question and control cue. Include cases where the right next step is **not** a recall. Add a new versioned suite when cases or expectations change, retain the old set, and measure both versions of Rolling Memory on the same set before comparing. Do not edit cases to improve a measured score.
