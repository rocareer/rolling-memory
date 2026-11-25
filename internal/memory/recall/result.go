// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package recall carries the local read-side delivery result and its rendering.
package recall

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rocareer/rolling-memory/internal/clock"
	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/text"
)

var gapLabels = map[string]string{
	"no_time":              "缺时间",
	"bad_anchor":           "锚不可回源",
	"source_unavailable":   "来源不可回源",
	"snapshot_unavailable": "检索快照尚未就绪",
	"index_incomplete":     "情景索引不完整",
	"bare_excluded":        "仅提名（bare）",
	"state_excluded":       "状态排除",
	"audience_excluded":    "受众排除",
	"conflicts":            "冲突待裁定",
	"unresolved_mentions":  "未决提及",
	"budget_exceeded":      "预算截止",
	"empty_graph":          "图面为空",
}

var gapOrder = []string{
	"no_time", "bad_anchor", "source_unavailable", "snapshot_unavailable", "index_incomplete", "bare_excluded", "state_excluded",
	"audience_excluded", "conflicts", "unresolved_mentions",
	"budget_exceeded", "empty_graph",
}

type Result struct {
	Scope   string
	Bands   []map[string]any
	RawHits []map[string]any
	Gaps    map[string]int
	Absent  bool
	Meta    map[string]any
}

// ToDict renders the full result shape.
func (r *Result) ToDict() map[string]any {
	gaps := map[string]any{}
	for key, value := range r.Gaps {
		if value != 0 {
			gaps[key] = value
		}
	}
	return map[string]any{
		"scope":    r.Scope,
		"bands":    r.Bands,
		"raw_hits": r.RawHits,
		"absent":   r.Absent,
		"gaps":     gaps,
		"meta":     r.Meta,
	}
}

// ToProbe renders the compact probe shape (hits capped at 12).
func (r *Result) ToProbe() map[string]any {
	hits := []any{}
	seen := map[string]struct{}{}
	for _, band := range r.Bands {
		nodes, _ := band["nodes"].([]map[string]any)
		for _, node := range nodes {
			nodeID := asString(node["id"])
			if _, ok := seen[nodeID]; ok {
				continue
			}
			seen[nodeID] = struct{}{}
			hit := map[string]any{
				"id":      nodeID,
				"summary": ids.Truncate(strings.TrimSpace(plainLine(node["content"])), 160, "…"),
				"date":    defaultAny(node["time"], "unknown"),
				"status":  defaultAny(node["status"], "active"),
				"anchor":  defaultAny(node["anchor"], ""),
			}
			// confidence and relative_strength carry different meanings and must
			// stay separate: relative_strength is only the in-group ranking, never
			// a fact/mention verdict.
			if writerConfidence, ok := node["confidence"]; ok {
				hit["confidence"] = writerConfidence
				hit["evidence"] = probeEvidence(writerConfidence)
			}
			if relStrength, ok := node["relative_strength"]; ok {
				hit["relative_strength"] = relStrength
			}
			if anchorOK, ok := node["anchor_ok"].(bool); ok && !anchorOK {
				hit["anchor_ok"] = false
			}
			hits = append(hits, hit)
			if len(hits) >= 12 {
				break
			}
		}
		if len(hits) >= 12 {
			break
		}
	}
	gaps := map[string]any{}
	for key, value := range r.Gaps {
		if value != 0 {
			gaps[key] = value
		}
	}
	return map[string]any{
		"scope":  r.Scope,
		"absent": r.Absent,
		"hits":   hits,
		"gaps":   gaps,
		"meta":   r.Meta,
	}
}

// mentionOnlyConfidence is the rule extractor's write floor (rule-v1 writes
// 0.35): probe hits at or below it are mention-backed fragments, not settled
// facts. The distinction is spelled out instead of inferred from a number (L6).
const mentionOnlyConfidence = 0.4

// probeEvidence labels one probe hit as "fact" or "mention_only".
func probeEvidence(confidence any) string {
	if asFloat(confidence, 1.0) <= mentionOnlyConfidence {
		return "mention_only"
	}
	return "fact"
}

// Render renders the recall block, truncating at maxChars.
func (r *Result) Render(maxChars int) string {
	return renderRecall(r, maxChars)
}

func renderRecall(result *Result, maxChars int) string {
	gapCount := 0
	for _, value := range result.Gaps {
		if value != 0 {
			gapCount++
		}
	}
	// The block always carries the current local time (second precision, explicit
	// zone): a reader must be able to place a recalled moment against "now" without
	// guessing today's date or the zone the labels live in.
	attrs := fmt.Sprintf("scope=%q now=%q results=%q raw=%q absent=%q gaps=%q ms=%q",
		result.Scope, ids.FormatTimeSeconds(clock.Unix(), ""),
		strconv.Itoa(len(result.Bands)), strconv.Itoa(len(result.RawHits)),
		strconv.FormatBool(result.Absent), strconv.Itoa(gapCount),
		asString(defaultAny(result.Meta["elapsed_ms"], 0)))
	opening := "<rolling-memory-recall " + attrs + ">"
	closing := "</rolling-memory-recall>"
	lines := []string{}
	for _, band := range result.Bands {
		kind := asString(band["kind"])
		if kind == "episodic_turn_band" {
			lines = append(lines, episodicTurnBandLines(band)...)
			continue
		}
		if kind == "cell_band" || kind == "noetic_band" {
			lines = append(lines, bandItemLines(band)...)
			continue
		}
		head, parenParts := bandHead(band)
		lines = append(lines, head+labelParen(parenParts...))
		chains, _ := band["chains"].([]any)
		for _, chainValue := range chains {
			chain := chainValue.(map[string]any)
			lines = append(lines, "  ["+plainLine(chain["type"])+"] "+plainLine(chain["label"])+labelParen(chain["time_label"]))
			nodes, _ := chain["nodes"].([]map[string]any)
			for _, node := range nodes {
				lines = append(lines, nodeLine(node))
			}
		}
		if chains == nil || len(chains) == 0 {
			nodes, _ := band["nodes"].([]map[string]any)
			for _, node := range nodes {
				lines = append(lines, nodeLine(node))
			}
		}
	}
	if len(result.RawHits) > 0 {
		undelivered := make([]map[string]any, 0, len(result.RawHits))
		for _, hit := range result.RawHits {
			// A hit already delivered as a band must not be printed twice: the thin
			// path marks those, so the block stays one readable stream.
			if value, ok := hit["in_band"].(bool); ok && value {
				continue
			}
			undelivered = append(undelivered, hit)
		}
		if len(undelivered) > 0 {
			lines = append(lines, "原文命中（未入图）:")
		}
		for _, hit := range undelivered {
			suffix := ""
			if tags, ok := hit["relative"].([]any); ok && len(tags) > 0 {
				dates := []string{}
				for index, tagValue := range tags {
					if index >= 2 {
						break
					}
					tag := tagValue.(map[string]any)
					dates = append(dates, asString(tag["date"]))
				}
				suffix = "（相对时点 " + strings.Join(dates, "、") + "）"
			}
			timePrefix := ""
			if occ := asInt64(hit["occurred_at"]); occ > 0 {
				if formatted := ids.FormatTimeSeconds(occ, ""); formatted != "" {
					timePrefix = formatted + " "
				}
			}
			lines = append(lines, "  - ["+plainLine(hit["session"])+":"+plainLine(hit["turn"])+"] "+
				timePrefix+plainLine(hit["text"])+suffix)
		}
	}
	activeGaps := []string{}
	for _, key := range gapOrder {
		value := result.Gaps[key]
		if value == 0 {
			continue
		}
		if label, ok := gapLabels[key]; ok {
			activeGaps = append(activeGaps, label+" "+strconv.Itoa(value))
		}
	}
	if len(activeGaps) > 0 {
		lines = append([]string{"缺口: " + strings.Join(activeGaps, " · ")}, lines...)
	}
	if result.Absent {
		lines = append(lines, "不持有：本地图中没有与该线索相关的记忆；可换更具体的名称，或继续对话，系统会自动记录。")
	}
	if truncated, ok := result.Meta["truncated"].(map[string]any); ok {
		if value, ok := truncated["chars"].(bool); ok && value {
			lines = append(lines, "（已截断：受交付预算限制）")
		}
		bandsTrimmed, _ := truncated["bands"].(bool)
		nodesTrimmed, _ := truncated["nodes"].(bool)
		rawTrimmed, _ := truncated["raw"].(bool)
		if bandsTrimmed || nodesTrimmed || rawTrimmed {
			lines = append([]string{"（部分结果未显示；offset 用于翻阅其他结果，不是原文的字符位置）"}, lines...)
		}
	}

	body := strings.Join(lines, "\n")
	// The requested cap governs the whole delivery (envelope included): the
	// earlier 200-rune floor made `caps.chars=300` deliver 357 runes. The
	// truncation marker is reserved inside the budget as well, so a truncated
	// block never overshoots the cap it was asked for.
	budget := maxChars - utf8.RuneCountInString(opening) - utf8.RuneCountInString(closing) - 2
	if budget < 0 {
		budget = 0
	}
	if utf8.RuneCountInString(body) > budget {
		const marker = "\n…（已截断）"
		cut := budget - utf8.RuneCountInString(marker)
		if cut < 0 {
			cut = 0
		}
		trimmed := text.FirstRunes(body, cut)
		// Cut on a line boundary so the block never ends inside a speaker line.
		if boundary := strings.LastIndex(trimmed, "\n"); boundary > 0 {
			trimmed = trimmed[:boundary]
		}
		if trimmed = strings.TrimRightFunc(trimmed, unicode.IsSpace); trimmed == "" {
			body = strings.TrimPrefix(marker, "\n")
		} else {
			body = trimmed + marker
		}
		if truncated, ok := result.Meta["truncated"].(map[string]any); ok {
			truncated["chars"] = true
		} else if result.Meta != nil {
			// The render budget is a second, independent cut: caps may have left the
			// payload whole, so the receipt must carry the flag rather than stay absent
			// while the block says it was truncated.
			result.Meta["truncated"] = map[string]any{"chars": true}
		}
	}
	if body != "" {
		return opening + "\n" + body + "\n" + closing
	}
	return opening + "\n" + closing
}

// bandItemLines renders a flat source item without the retired grouping
// header. One memory is one line; the source label is carried inline so repeated
// workspace names do not become repeated section titles.
func bandItemLines(band map[string]any) []string {
	nodes, _ := band["nodes"].([]map[string]any)
	if len(nodes) == 0 {
		return nil
	}
	source := strings.TrimSpace(asString(band["space_label"]))
	if source == "" && asString(band["kind"]) == "noetic_band" {
		source = "noetic"
	}
	if source == "" {
		source = bandLocus(band)
	}
	if source == "" {
		title := displayMemoryTitle(asString(band["title"]))
		if title != "" && !isAddressTitle(title) {
			source = title
		}
	}
	if source == "" {
		source = "记忆"
	}
	source = plainLine(source)
	lines := make([]string, 0, len(nodes))
	for _, node := range nodes {
		rawContent := strings.TrimSpace(asString(node["content"]))
		contentLines := nonEmptyTrimmedLines(rawContent)
		if source != "" && len(contentLines) > 0 {
			tag := "[" + source + "]"
			if contentLines[0] == tag && len(contentLines) > 1 {
				contentLines = contentLines[1:]
			} else if strings.HasPrefix(contentLines[0], tag+" ") {
				if rest := strings.TrimSpace(strings.TrimPrefix(contentLines[0], tag)); rest != "" {
					contentLines[0] = rest
				}
			}
		}
		prefix := "  - "
		if source != "" {
			prefix += "[" + source + "] "
		}
		if shortened, _ := node["content_truncated"].(bool); shortened {
			prefix += "（正文已缩短 · 单元 ID: " + plainLine(node["id"]) + "） "
		}
		if len(contentLines) <= 1 {
			singleNode := node
			if len(contentLines) == 1 && contentLines[0] != rawContent {
				singleNode = make(map[string]any, len(node))
				for k, v := range node {
					singleNode[k] = v
				}
				singleNode["content"] = contentLines[0]
			}
			line := strings.TrimPrefix(nodeLine(singleNode), "    - ")
			lines = append(lines, prefix+line)
			continue
		}
		firstNode := make(map[string]any, len(node))
		for k, v := range node {
			firstNode[k] = v
		}
		firstNode["content"] = contentLines[0]
		firstLine := strings.TrimPrefix(nodeLine(firstNode), "    - ")
		lines = append(lines, prefix+firstLine)
		for _, extra := range contentLines[1:] {
			lines = append(lines, "    "+plainLine(extra))
		}
	}
	return lines
}

func nonEmptyTrimmedLines(text string) []string {
	if !strings.Contains(text, "\n") {
		if trimmed := strings.TrimSpace(text); trimmed != "" {
			return []string{trimmed}
		}
		return nil
	}
	raw := strings.Split(text, "\n")
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// episodicTurnBandLines renders one assembled conversation turn. The segments keep
// their own lines and speaker order; only the header carries provenance, because a
// per-line metadata suffix would flatten the conversation back into the unreadable
// wall the assembly just removed. The header deliberately carries no strength
// number: the caller reads the ordered content, not a scalar that impersonates
// authority (see relativeStrength in the local vault client).
func episodicTurnBandLines(band map[string]any) []string {
	title := plainLine(band["title"])
	nodes, _ := band["nodes"].([]map[string]any)
	if len(nodes) == 0 {
		return []string{title}
	}
	node := nodes[0]
	rendered := asString(node["content"])
	parts := make([]string, 0, 5)
	timeText := strings.TrimSpace(asString(band["time_label"]))
	if timeText == "" || timeText == "unknown" || timeText == "时间未知" {
		timeText = strings.TrimSpace(asString(node["time"]))
	}
	if (timeText == "" || timeText == "unknown" || timeText == "时间未知") && asInt64(node["occurred_at"]) > 0 {
		timeText = ids.FormatTimeSeconds(asInt64(node["occurred_at"]), "")
	}
	if timeText != "" && timeText != "unknown" && timeText != "时间未知" {
		parts = append(parts, plainLine(timeText))
	}
	if locus := strings.TrimSpace(asString(node["locus"])); locus != "" {
		parts = append(parts, plainLine(locus))
	}
	segments := int(asFloat(node["turn_segments"], 0))
	if window, ok := node["segment_window"].([]int); ok && len(window) == 2 {
		if window[0] == window[1] && segments <= 1 {
			parts = append(parts, fmt.Sprintf("段 %d", window[0]))
		} else {
			parts = append(parts, fmt.Sprintf("段 %d–%d", window[0], window[1]))
			if segments > 0 {
				parts = append(parts, strconv.Itoa(segments)+" 段")
			}
		}
	} else if segments > 0 {
		parts = append(parts, strconv.Itoa(segments)+" 段")
	}
	shortened, _ := node["content_truncated"].(bool)
	if shortened {
		parts = append(parts, "正文已缩短")
	}
	header := title
	if len(parts) > 0 {
		header += "（" + strings.Join(parts, " · ") + "）"
	}
	lines := []string{header}
	if sourceIDs, ok := node["source_unit_ids"].([]string); ok && len(sourceIDs) > 0 && (len(sourceIDs) > 1 || shortened) {
		lines = append(lines, "  已核验命中片段: "+plainLine(strings.Join(sourceIDs, ", ")))
		if shortened {
			// The omitted tail lives in the turn's last verified segment; name that
			// one unit so the assistant reads the right source instead of choosing
			// among every cited ID in the band.
			lines = append(lines, fmt.Sprintf(`  Read omitted text if needed: recall({"unit_id":"%s"})`, sourceIDs[len(sourceIDs)-1]))
		}
	}
	rawLines := strings.Split(rendered, "\n")
	multiLineTurn := len(rawLines) > 1
	for idx, line := range rawLines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		isSpeakerLine := strings.HasPrefix(trimmed, "[user]") ||
			strings.HasPrefix(trimmed, "[assistant]") ||
			strings.HasPrefix(trimmed, "[agent]") ||
			strings.HasPrefix(trimmed, "[tool]")
		// Keep speaker lead lines at 2-space indent ("  [assistant] ...") and indent
		// subordinate body lines (lists, tables, headings) inside a multi-line turn at
		// 4 spaces ("    - ...") so nested bullets never collide visually with top-level
		// "  - [Repo] ..." cell_band items.
		if isSpeakerLine || (!multiLineTurn && idx == 0) {
			lines = append(lines, "  "+plainLine(trimmed))
		} else {
			lines = append(lines, "    "+plainLine(trimmed))
		}
	}
	return lines
}

// bandHead builds the agent-facing header for one band: the visible label plus
// the parenthetical labels that are not already part of it. Local-vault ladder
// bands carry an internal memory address as their title ("带·C6:…#00"), which
// means nothing to a reader; for those the header falls back to the space label
// (file coordinates), the node's workspace locus, or a neutral label — and the
// space label is then not repeated in the parens.
func bandHead(band map[string]any) (string, []any) {
	title := strings.TrimSpace(asString(band["title"]))
	space := strings.TrimSpace(asString(band["space_label"]))
	if !isAddressTitle(title) {
		// A source-built title (thin bands prefer the file label) must not repeat
		// the space label in the parens.
		display := displayMemoryTitle(title)
		if space != "" && strings.Contains(display, space) {
			return plainLine(display), []any{band["time_label"]}
		}
		return plainLine(display), []any{band["time_label"], band["space_label"]}
	}
	if space != "" {
		return plainLine(space), []any{band["time_label"]}
	}
	if locus := bandLocus(band); locus != "" {
		return plainLine(locus), []any{band["time_label"]}
	}
	return "记忆", []any{band["time_label"]}
}

// displayMemoryTitle removes the retired subject-band prefix from reader-facing
// output. The internal compatibility shape may still carry it, but the delivered
// text never re-introduces the old taxonomy.
func displayMemoryTitle(title string) string {
	title = strings.TrimSpace(title)
	title = strings.TrimPrefix(title, "带·")
	title = strings.TrimPrefix(title, "记忆带·")
	if title == "记忆带" {
		return "记忆"
	}
	return title
}

// isAddressTitle reports whether a band title is only an internal addressing
// coordinate (ladder "C6:…" cells or "ast:…" incarnations) with no reader value.
func isAddressTitle(title string) bool {
	body := displayMemoryTitle(title)
	if body == "" {
		return false
	}
	return strings.HasPrefix(body, "C6:") || strings.HasPrefix(body, "ast:") ||
		strings.Contains(body, "/C6:") || strings.Contains(body, "/ast:")
}

// bandLocus returns the first workspace locus carried by a band's nodes, walking
// chains when the band has no flat node list.
func bandLocus(band map[string]any) string {
	if nodes, ok := band["nodes"].([]map[string]any); ok {
		for _, node := range nodes {
			if locus := strings.TrimSpace(asString(node["locus"])); locus != "" {
				return locus
			}
		}
	}
	chains, _ := band["chains"].([]any)
	for _, chainValue := range chains {
		chain, ok := chainValue.(map[string]any)
		if !ok {
			continue
		}
		nodes, _ := chain["nodes"].([]map[string]any)
		for _, node := range nodes {
			if locus := strings.TrimSpace(asString(node["locus"])); locus != "" {
				return locus
			}
		}
	}
	return ""
}

func nodeLine(node map[string]any) string {
	parts := []string{}
	if status := strings.TrimSpace(asString(node["status"])); status != "" && status != "active" {
		parts = append(parts, plainLine(status))
	}
	if anchorOK, ok := node["anchor_ok"].(bool); ok && !anchorOK {
		parts = append(parts, "锚异常")
	}
	if value := strings.TrimSpace(asString(node["reason"])); value != "" {
		parts = append(parts, plainLine(value))
	}
	if tags, ok := node["relative"].([]any); ok && len(tags) > 0 {
		dates := []string{}
		for index, tagValue := range tags {
			if index >= 2 {
				break
			}
			tag := tagValue.(map[string]any)
			dates = append(dates, asString(tag["date"]))
		}
		parts = append(parts, "相对时点 "+strings.Join(dates, "、"))
	}
	prefix := ""
	if text := strings.TrimSpace(asString(node["time"])); text != "" && text != "unknown" {
		prefix = plainLine(text) + " "
	}
	line := "    - " + prefix + plainLine(node["content"])
	if len(parts) > 0 {
		line += "（" + strings.Join(parts, " · ") + "）"
	}
	return line
}

// labelParen renders the "（time · space）" suffix with empty parts collapsed;
// it returns "" when every part is empty so headers never carry empty parens.
func labelParen(parts ...any) string {
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		text := strings.TrimSpace(asString(part))
		if text == "" || text == "unknown" {
			continue
		}
		values = append(values, plainLine(text))
	}
	if len(values) == 0 {
		return ""
	}
	return "（" + strings.Join(values, " · ") + "）"
}

// plainLine flattens one content line for the delivery block: newlines become
// spaces so a node never breaks the line structure. Content is delivered as
// plain text — no HTML entity escaping, because the block is read by an agent
// (and often quoted back into code) rather than parsed as markup.
func plainLine(value any) string {
	text := strings.ReplaceAll(asString(value), "\n", " ")
	return strings.ReplaceAll(text, "\r", " ")
}

// ------------------------------------------------------------------ helpers

func asString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64)
	}
	return fmt.Sprint(value)
}

func asInt64(value any) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	case string:
		parsed, err := strconv.ParseInt(typed, 10, 64)
		if err == nil {
			return parsed
		}
	}
	return 0
}

func asFloat(value any, fallback float64) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case string:
		parsed, err := strconv.ParseFloat(typed, 64)
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func defaultAny(value any, fallback any) any {
	if value == nil {
		return fallback
	}
	return value
}
