// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package mcp

// instructions is the initialize handshake text: the recall-first doctrine the
// harness agent follows (the same doctrine the shipped harness skill carries).
const instructions = "You have your own persistent memory across sessions (Rolling-Memory). " +
	"Your past conversations and workspace context are remembered automatically in the background — " +
	"you never need to manually write or save memory (every recall that hits evidence automatically consolidates L3 memory).\n" +
	"\n" +
	"- Task start (mandatory): understand the user's intent and call recall with a specific cue before blind searching.\n" +
	"- Task stuck (mandatory): whenever a command/test fails, an error occurs, or a search misses, call recall with the error token or concept before guessing.\n" +
	"- How to write `cue`: prefix with `[<workspace>]` (e.g. `[Rolling-Memory]`) to prioritize current-repo memories, followed by exact literal tokens, file/symbol names, or error fingerprints (avoid paraphrasing or unrelated buzzwords).\n" +
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
	"description": "Recall from your persistent memory with zero model calls (hitting evidence automatically consolidates L3 memory — never manually write memory). Call before blind file/code searches when context, coordinates, or prior decisions are missing, and immediately when a command/test fails. Prefix cue with [<workspace>] to prioritize same-repository memories and pass 2–6 exact literal tokens, file/symbol names, or error fingerprints (avoid natural-language paraphrasing; if noisy, drop broad words and retry with fewer exact tokens). Returns ranked memory results with anchors, status, and local second-precision timestamps (header `now` carries current time). absent=true means the memory does not hold it; it is not an error. Use mode=full (default) for evidence, mode=probe for a compact JSON existence receipt, or mode=recent for newest entries by time.",
	"inputSchema": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"cue": map[string]any{
				"type":        "string",
				"description": "Literal search clue (required for mode=full/probe): prefix with [<workspace>] to prioritize same-repo memories, followed by 2–6 exact tokens, file/symbol names, or error fingerprints. Avoid natural-language sentences or broad buzzwords.",
			},
			"scope":  map[string]any{"type": "string", "description": "Optional memory scope override (defaults to active scope)."},
			"reader": map[string]any{"enum": []any{"owner", "deliverable"}, "description": "Audience visibility filter (defaults to deliverable)."},
			"mode": map[string]any{
				"enum":        []any{"full", "probe", "recent"},
				"description": "full returns the complete recall result; probe returns a compact existence receipt; recent lists the newest memory entries in pure time order (cue ignored).",
			},
			"asof": map[string]any{
				"type":        []any{"string", "integer"},
				"description": "Reserved; do not use (fail closed).",
			},
			"window": map[string]any{
				"type":        "object",
				"description": "Reserved; do not use (fail closed).",
				"properties": map[string]any{
					"start": map[string]any{"type": []any{"string", "integer"}},
					"end":   map[string]any{"type": []any{"string", "integer"}},
				},
			},
			"caps": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"bands":  map[string]any{"type": "integer", "minimum": 0, "maximum": 64, "description": "Maximum memory bands/turns to deliver (default 10, max 64)."},
					"nodes":  map[string]any{"type": "integer", "description": "Maximum total nodes across delivered bands.", "minimum": 0},
					"chars":  map[string]any{"type": "integer", "description": "Maximum character budget for rendered text delivery (default 16000).", "minimum": 0},
					"chains": map[string]any{"type": "integer", "description": "Chains per band (delivery breadth)."},
					"rows":   map[string]any{"type": "integer", "description": "Rows per chain (delivery breadth)."},
					"raw": map[string]any{
						"type":        "integer",
						"minimum":     0,
						"maximum":     64,
						"description": "Raw-hit delivery cap (default 10; 0 uses the default).",
					},
				},
			},
			"result_unit": map[string]any{
				"enum":        []any{"band", "item"},
				"description": "Delivery unit: subject bands (default) or flat source items.",
			},
			"rank": map[string]any{
				"type":        "boolean",
				"description": "Item-mode grouping: true (default) groups by memory unit -> source block; false delivers pure relevance order.",
			},
			"offset":  map[string]any{"type": "integer", "description": "Result pagination offset."},
			"session": map[string]any{"type": "string", "description": "Reserved; do not use (fail closed)."},
			"deep": map[string]any{
				"type":        "boolean",
				"description": "Optional query-hint expansion; skipped automatically in zero-LLM local mode.",
			},
		},
	},
}
