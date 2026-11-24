// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package mcp

const recallWhen = "If a past decision, failed attempt or missing entrypoint would change your next action, call recall before guessing. " +
	"If the current evidence already identifies the next check (for example, a file and line in an error), inspect it directly. " +
	"If a hit adds nothing new, continue from current evidence."

// instructions is the initialize handshake text: the recall guidance the
// harness agent follows (the same doctrine the shipped harness skill carries).
const instructions = "You have your own persistent memory across sessions (Rolling-Memory). " +
	"Your past conversations and workspace context are remembered automatically in the background — " +
	"you never need to manually write or save memory (every recall that hits evidence automatically consolidates L3 memory).\n" +
	"\n" +
	"- Call rule: " + recallWhen + "\n" +
	"- How to write `cue`: prefix with `[<workspace>]` (e.g. `[Rolling-Memory]`) to prioritize current-repo memories, then give a dense literal cue — intent, exact literal tokens for the objects themselves (file/symbol/command names), at least 1–2 hard identifiers (error text, status code, route, version, PR/issue number, date) and the occasion, typically 12–25 content words. Every word must really occur in the target memory; never pad with generic fillers (`help / look / that issue / why / how / related`) — they drag unrelated long sessions to the front. A 2–6 token cue is only for confirming one known symbol or error code.\n" +
	"- Treat what you recall as a traceable lead, not proof: verify against the live disk before acting.\n" +
	"- `absent: true` means the memory does not hold it; it is not an error, and not a refutation.\n"

// tools is the tools/list payload: a single tool, recall, with its frozen
// name, description, and inputSchema contract.
var tools = []any{
	recallTool,
}

var recallTool = map[string]any{
	"name":  "recall",
	"title": "回忆",
	"annotations": map[string]any{
		"readOnlyHint":  true,
		"openWorldHint": false,
	},
	"description": "Recall from your persistent memory with zero model calls (hitting evidence automatically consolidates L3 memory — never manually write memory). " + recallWhen + " Prefix cue with [<workspace>] to prioritize same-repository memories, then pass a dense cue made of the target memory's own exact literal tokens: intent + object/file/symbol names + 1–2 hard identifiers (verbatim error, status code, route, version, commit/PR/issue number, date) + the occasion (typically 12–25 content words; avoid natural-language sentences and generic fillers, which pull unrelated long sessions forward). Use only 2–6 tokens to confirm a single known symbol or error code. If hits are noisy, drop the broad words and keep the exact ones. Returns ranked memory results with anchors, status, and local second-precision timestamps (header `now` carries current time). absent=true means the memory does not hold it; it is not an error. Use mode=full (default) for evidence, mode=probe for a compact JSON existence receipt, or mode=recent for newest entries by time.",
	"inputSchema": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"cue": map[string]any{
				"type":        "string",
				"description": "Literal search clue (required for mode=full/probe): prefix with [<workspace>] to prioritize same-repo memories, then give a dense literal cue — intent, object/file/symbol names verbatim, 1–2 hard identifiers (error text, status code, route, version, number, date), and the occasion; typically 12–25 content words, space-separated, no full sentences and no generic fillers. Use 2–6 tokens only to confirm a single known symbol or error code.",
			},
			"scope":  map[string]any{"type": "string", "description": "Optional memory scope override (defaults to active scope)."},
			"reader": map[string]any{"enum": []any{"owner", "deliverable"}, "description": "Audience visibility filter (defaults to deliverable)."},
			"mode": map[string]any{
				"enum":        []any{"full", "probe", "recent"},
				"description": "full returns the complete recall result; probe returns a compact existence receipt; recent lists the newest memory entries in pure time order (cue ignored).",
			},
			"delivery_window": map[string]any{
				"enum":        []any{"auto", "0-16", "0-32", "0-64"},
				"description": "Optional result breadth, not excerpt length. Omit for normal queries; use a wider window only when comparing several past events.",
			},
			"offset": map[string]any{"type": "integer", "description": "Result pagination offset."},
		},
	},
}
