// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

import (
	"fmt"
	"reflect"
	"runtime"
	"sort"
	"strings"
)

// ---------------------------------------------------------------- receipts

func (a *application) printInit(result map[string]any) {
	if root := pyStr(result["portable_root"]); root != "" {
		a.printf("portable: %s   profile: %s\n", root, pyStr(getOr(result, "profile", "usb")))
	}
	a.printf("database: %s\n", pyStr(result["db"]))
	a.printf("config:   %s\n", pyStr(result["config"]))
	a.printf("scope:    %s   mode: %s   schema: v%s\n",
		pyStr(result["scope"]), pyStr(result["mode"]), pyStr(result["schema_version"]))
}

func (a *application) printRecent(result []map[string]any) {
	if len(result) == 0 {
		a.printLine("recent: (empty)")
		return
	}
	for _, item := range result {
		// 不带任何强度数字：recent 没有检索线索，谈不上「匹配了多少」，
		// 之前那个常量 1.0 只会被读成「每条都最可信」。
		a.printf("%s  %s  [%s · %s]\n",
			pyStr(item["time"]), pyStr(item["content"]), pyStr(item["anchor"]),
			pyStr(item["status"]))
	}
}

func (a *application) printSummary(label string, result map[string]any) {
	keys := make([]string, 0, len(result))
	for key := range result {
		switch key {
		case "left", "right", "winner", "loser":
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+pyStr(result[key]))
	}
	a.printf("%s: %s\n", label, strings.Join(parts, ", "))
}

// ------------------------------------------------------------------- streams

func (a *application) printReplay(result map[string]any) {
	a.printf("replay: %s traces · %s written · %s skipped · anchor_failed=%s · llm_calls=%s\n",
		pyStr(getOr(result, "processed", 0)), pyStr(getOr(result, "written", 0)),
		pyStr(getOr(result, "skipped", 0)), pyStr(getOr(result, "anchor_failed", 0)),
		pyStr(getOr(result, "llm_calls", 0)))
	for _, warning := range limitAny(anyList(result["warnings"]), 5) {
		a.printf("  warning: %s\n", pyStr(warning))
	}
}

func (a *application) printImport(result map[string]any) {
	// A JSON export carries one count per table (Store.ImportScope's
	// map[string]int), whereas a transcript carries the inserted trace count.
	if inserted := asMap(result["inserted"]); inserted != nil {
		a.printf("import (json export): scope %s  inserted %s\n", pyStr(result["scope"]), pyRepr(inserted))
	} else {
		a.printf("import: %s messages · %s new traces · session %s · offset %s/%s\n",
			pyStr(getOr(result, "messages", 0)), pyStr(getOr(result, "inserted", 0)),
			pyStr(result["session"]), pyStr(getOr(result, "offset", 0)),
			pyStr(getOr(result, "total_lines", 0)))
	}
	if replay, ok := result["replay"].(map[string]any); ok {
		a.printReplay(replay)
	}
}

func (a *application) printExport(result map[string]any) {
	if path, ok := result["path"]; ok {
		a.printf("exported: %s  %s\n", pyStr(path), pyRepr(getOr(result, "counts", map[string]any{})))
		return
	}
	_ = a.printJSON(result)
}

// ---------------------------------------------------------------- diagnostics

func (a *application) printStatus(result map[string]any) {
	counts := childMap(result, "counts")
	embedding := childMap(result, "embedding")
	a.printf("version:  %s\n", pyStr(result["version"]))
	a.printf("db:       %s  (%s bytes)\n", pyStr(result["db"]), pyStr(getOr(result, "db_size_bytes", 0)))
	a.printf("scope:    %s\n", pyStr(result["scope"]))
	a.printf("mode:     %s (%s)\n", pyStr(result["mode"]), pyStr(result["egress"]))
	a.printf("graph:    %s active memory units · %s nodes · %s traces · %s private\n",
		pyStr(getOr(result, "active_assertions", 0)), pyStr(getOr(counts, "node", 0)),
		pyStr(getOr(counts, "trace", 0)), pyStr(getOr(result, "private_assertions", 0)))
	a.printf("queue:    %s pending · %s conflicts · %s sessions\n",
		pyStr(getOr(result, "pending_traces", 0)), pyStr(getOr(result, "conflicts_proposed", 0)),
		pyStr(getOr(result, "sessions", 0)))
	a.printf("history:  %s retracted · %s superseded\n",
		pyStr(getOr(result, "retracted_assertions", 0)), pyStr(getOr(result, "superseded_assertions", 0)))
	// Human label for the local compatibility index (embedding provider/model);
	// the JSON keys keep their existing names.
	a.printf("compat:   %s / %s\n", pyStr(embedding["provider"]), pyStr(embedding["model"]))
	if a.args.Flag("wave") {
		seed := fmt.Sprintf("scope %s assertions %s nodes %s traces %s sessions %s",
			pyStr(result["scope"]),
			pyStr(getOr(result, "active_assertions", 0)),
			pyStr(getOr(counts, "node", 0)),
			pyStr(getOr(counts, "trace", 0)),
			pyStr(getOr(result, "sessions", 0)))
		amps, hexCode, slots := ComputeTerminalWaveSpec(seed)
		a.printf("%s", RenderBrailleOscilloscope("Status Telemetry Wave", amps, hexCode, slots, 44))
	}
}

func (a *application) printDoctor(result map[string]any) {
	// The JSON keeps the python key (empty string); the human line
	// prints the Go runtime instead.
	a.printf("rolling-memory %s · go %s · sqlite %s\n",
		pyStr(result["version"]), strings.TrimPrefix(runtime.Version(), "go"), pyStr(result["sqlite"]))
	for _, check := range childList(result, "checks") {
		mark := "--"
		if truthy(check["ok"]) {
			mark = "ok"
		}
		a.printf("  [%s] %s: %s\n", mark, pyStr(check["name"]), pyStr(check["detail"]))
	}
	for _, warning := range anyList(result["warnings"]) {
		a.printf("  warning: %s\n", pyStr(warning))
	}
	integrity := childMap(result, "integrity")
	if truthy(integrity["ok"]) {
		a.printLine("integrity: ok")
	} else {
		a.printLine("integrity: FAILED")
	}
	for _, issue := range limitList(childList(integrity, "issues"), 10) {
		a.printf("  %s: %s: %s\n", pyStr(issue["severity"]), pyStr(issue["code"]), pyStr(issue["detail"]))
	}
}

func (a *application) printCheck(result map[string]any) {
	if truthy(result["ok"]) {
		a.printLine("check: ok")
	} else {
		a.printLine("check: FAILED")
	}
	for _, issue := range childList(result, "issues") {
		a.printf("  %s: %s: %s\n", pyStr(issue["severity"]), pyStr(issue["code"]), pyStr(issue["detail"]))
	}
	counts := childMap(result, "counts")
	a.printf("counts: nodes=%s assertions=%s traces=%s bare=%s conflicts=%s\n",
		pyStr(getOr(counts, "node", 0)), pyStr(getOr(counts, "assertion", 0)),
		pyStr(getOr(counts, "trace", 0)), pyStr(getOr(result, "bare_assertions", 0)),
		pyStr(getOr(result, "conflicts_proposed", 0)))
}

func (a *application) printReindex(result map[string]any) {
	// "vectors" and "fts" are the JSON receipt keys; the human line reports the
	// lexical index plus the local compatibility index, which never feeds engine
	// retrieval.
	a.printf("reindex: fts=%s local_compat_index=%s\n", pyStr(result["fts"]), pyStr(result["vectors"]))
	if truthy(result["warning"]) {
		a.printf("  warning: %s\n", pyStr(result["warning"]))
	}
}

func (a *application) printEfficacy(m map[string]any) {
	a.printf("Rolling-Memory Cross-Harness Recall Efficacy · Grade %s (%s/100) · scope %s\n",
		pyStr(m["grade"]), pyStr(m["efficacy_score"]), pyStr(m["scope"]))
	a.printf("  sessions=%s  turns=%s  injected=%s  silenced=%s  adopted=%s (coord=%s · causal=%s · echo_stripped=%s · self_healed=%s)\n",
		pyStr(m["total_sessions"]), pyStr(m["evaluated_turns"]),
		pyStr(m["injected_count"]), pyStr(m["silenced_count"]),
		pyStr(m["adopted_count"]), pyStr(m["code_coord_hits"]),
		pyStr(getOr(m, "causal_direct_hits", 0)),
		pyStr(getOr(m, "echo_excluded_count", 0)),
		pyStr(getOr(m, "self_healed_count", 0)))
	a.printf("  adopt_rate=%s%%  silence_rate=%s%%  avg_round_context=%s tok (fresh=%s + cached=%s)\n",
		pyStr(m["adopt_rate"]), pyStr(m["silence_rate"]),
		pyStr(getOr(m, "avg_round_cost_tokens", 0)),
		pyStr(getOr(m, "avg_round_fresh_tokens", 0)),
		pyStr(getOr(m, "avg_round_cached_tokens", 0)))
	a.printf("  today_net_saved=%s tok (-%sr · fresh=%s + cached=%s)  cumulative_net_saved=%s tok (-%sr · fresh=%s + cached=%s · injected=%s)\n",
		pyStr(getOr(m, "today_net_saved_tokens", 0)), pyStr(getOr(m, "today_saved_rounds", 0)),
		pyStr(getOr(m, "today_saved_fresh_tokens", 0)), pyStr(getOr(m, "today_saved_cached_tokens", 0)),
		pyStr(m["net_saved_tokens"]), pyStr(m["total_saved_rounds"]),
		pyStr(getOr(m, "saved_fresh_tokens", 0)), pyStr(getOr(m, "saved_cached_tokens", 0)),
		pyStr(m["injected_tokens"]))
	a.printLine("harnesses:")
	for _, h := range childList(m, "harnesses") {
		a.printf("  - %-12s [%s] sessions=%s turns=%s injected=%s adopted=%s (%s%%) silenced=%s saved_rounds=%s net_tokens=%s\n",
			pyStr(h["id"]), pyStr(h["status"]),
			pyStr(h["sessions"]), pyStr(h["turns"]),
			pyStr(h["injections"]), pyStr(h["adopted"]), pyStr(h["adopt_rate"]),
			pyStr(h["silenced"]), pyStr(h["saved_rounds"]), pyStr(h["net_saved_tokens"]))
	}
	sessions := childList(m, "sessions")
	if len(sessions) > 0 {
		a.printLine("recent evaluations:")
		for _, s := range limitList(sessions, 12) {
			a.printf("  [%s · %s · %s] outcome=%s round_cost=%s saved_rounds=%s net_tokens=%s :: %s\n",
				pyStr(s["time_text"]), pyStr(s["harness"]), pyStr(s["workspace"]),
				pyStr(s["outcome"]), pyStr(getOr(s, "round_cost_tokens", 0)),
				pyStr(s["saved_rounds"]), pyStr(s["net_tokens"]),
				pyStr(s["prompt"]))
		}
	}
}

func (a *application) printEfficacyCard(m map[string]any) {
	const border = "+--------------------------------------------------------------+"
	row := func(label, val string) {
		line := fmt.Sprintf("| %-18s : %-39s |", label, val)
		a.printLine(line)
	}
	a.printLine(border)
	a.printLine("| ROLLING-MEMORY · LOCAL MEMORY ROI RECEIPT (0-Vector · 0-LLM) |")
	a.printLine(border)
	row("Scope / Grade", fmt.Sprintf("%s · Grade %s (%s/100)", pyStr(m["scope"]), pyStr(m["grade"]), pyStr(m["efficacy_score"])))
	row("Audited Sessions", fmt.Sprintf("%s sessions · %s evaluated turns", pyStr(m["total_sessions"]), pyStr(m["evaluated_turns"])))
	row("Adoption / Silence", fmt.Sprintf("%s%% adopted · %s%% noise silenced", pyStr(m["adopt_rate"]), pyStr(m["silence_rate"])))
	row("Avg Turn Context", fmt.Sprintf("%s tok/turn", pyStr(getOr(m, "avg_round_cost_tokens", 0))))
	row("Today Net Saved", fmt.Sprintf("%s tok (%s blind turns avoided)", pyStr(getOr(m, "today_net_saved_tokens", 0)), pyStr(getOr(m, "today_saved_rounds", 0))))
	row("Total Net Saved", fmt.Sprintf("%s tok (%s blind turns avoided)", pyStr(m["net_saved_tokens"]), pyStr(m["total_saved_rounds"])))
	a.printLine(border)
	a.printLine("| Verified locally · github.com/rocareer/rolling-memory        |")
	a.printLine(border)
}

// ------------------------------------------------------------------- helpers

func anyList(value any) []any {
	if value == nil {
		return nil
	}
	reflected := reflect.ValueOf(value)
	for reflected.Kind() == reflect.Interface || reflected.Kind() == reflect.Pointer {
		if reflected.IsNil() {
			return nil
		}
		reflected = reflected.Elem()
	}
	if reflected.Kind() != reflect.Slice && reflected.Kind() != reflect.Array {
		return nil
	}
	items := make([]any, 0, reflected.Len())
	for index := 0; index < reflected.Len(); index++ {
		items = append(items, reflected.Index(index).Interface())
	}
	return items
}

func limitAny(values []any, limit int) []any {
	if len(values) > limit {
		return values[:limit]
	}
	return values
}

func limitList(values []map[string]any, limit int) []map[string]any {
	if len(values) > limit {
		return values[:limit]
	}
	return values
}

func firstRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return string(runes)
}
