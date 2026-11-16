// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package api

import (
	"strings"
	"unicode/utf8"

	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/recall"
	"github.com/rocareer/rolling-memory/internal/text"
	"github.com/rocareer/rolling-memory/internal/thinclient"
)

// dedupeResolvedHitsByContent preserves every source. Even byte-identical text
// can be independent evidence, and similarity cannot establish fact equivalence.
func dedupeResolvedHitsByContent(hits []thinclient.LocalResolvedHit) ([]thinclient.LocalResolvedHit, int) {
	return hits, 0
}

// applyThinDeliveryCaps applies explicit breadth limits after ranking and grouping.
// Pagination does not assert absence: it only changes the delivered window.
func applyThinDeliveryCaps(result *recall.Result, req RecallRequest) {
	bands := result.Bands
	offset := req.Offset
	if offset < 0 {
		offset = 0
	}
	if offset >= len(bands) {
		bands = bands[:0]
	} else {
		bands = bands[offset:]
	}
	bandsTrimmed := offset > 0 && len(result.Bands) > 0
	if req.CapsBands != nil && *req.CapsBands >= 0 && len(bands) > *req.CapsBands {
		bands = bands[:*req.CapsBands]
		bandsTrimmed = true
	}
	nodesTrimmed := false
	remaining := -1
	if req.CapsNodes != nil && *req.CapsNodes >= 0 {
		remaining = *req.CapsNodes
	}
	selected := make(map[string]bool)
	for _, band := range bands {
		nodes, _ := band["nodes"].([]map[string]any)
		if remaining >= 0 && len(nodes) > remaining {
			nodes = nodes[:remaining]
			band["nodes"] = nodes
			nodesTrimmed = true
		}
		if remaining >= 0 {
			remaining -= len(nodes)
		}
		for _, node := range nodes {
			if id, ok := node["id"].(string); ok {
				selected[id] = true
			}
		}
	}
	result.Bands = bands
	raw := result.RawHits[:0]
	for _, hit := range result.RawHits {
		if id, ok := hit["trace"].(string); ok && selected[id] {
			raw = append(raw, hit)
		}
	}
	rawTrimmed := false
	if req.IncludeRaw != nil && !*req.IncludeRaw {
		rawTrimmed = len(raw) > 0
		raw = raw[:0]
	} else if req.RawLimit > 0 && len(raw) > req.RawLimit {
		raw = raw[:req.RawLimit]
		rawTrimmed = true
	}
	result.RawHits = raw
	if bandsTrimmed || nodesTrimmed || rawTrimmed {
		result.Meta["truncated"] = map[string]any{"bands": bandsTrimmed, "nodes": nodesTrimmed, "raw": rawTrimmed, "chars": false}
		result.Gaps["budget_exceeded"]++
	}
}

// stripAssistantRoutingPreamble removes harness routing labels, self-review footers,
// and leaked inline tool-call traces from assistant memories. Those artifacts describe
// the previous turn's harness mechanics, not the memory, and repeating them in every
// hit is pure delivery noise.
func stripAssistantRoutingPreamble(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		body := stripDeliverySpeakerPrefix(trimmed)
		if strings.HasPrefix(body, "技能判定：") || strings.HasPrefix(body, "技能判定:") ||
			strings.HasPrefix(body, "分支判定：") || strings.HasPrefix(body, "分支判定:") ||
			strings.HasPrefix(body, "文档判定：") || strings.HasPrefix(body, "文档判定:") ||
			strings.HasPrefix(body, "模块判定：") || strings.HasPrefix(body, "模块判定:") ||
			strings.HasPrefix(body, "复杂度判定：") || strings.HasPrefix(body, "复杂度判定:") ||
			strings.HasPrefix(body, "[Rolling-Memory 复盘]") ||
			body == "思考过程：" || body == "思考过程:" || body == "Thinking Process:" {
			continue
		}
		if idx := strings.Index(line, "call:default_api:"); idx >= 0 {
			line = strings.TrimSpace(line[:idx])
			if stripDeliverySpeakerPrefix(line) == "" {
				continue
			}
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func stripDeliverySpeakerPrefix(line string) string {
	trimmed := strings.TrimSpace(line)
	for _, prefix := range []string{"[user]", "[assistant]", "[agent]", "[tool]"} {
		if strings.HasPrefix(trimmed, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
		}
	}
	return trimmed
}

type deliverySentence struct {
	text      string
	lineIndex int
	score     int
}

// focusEpisodicDeliveryText keeps the evidence-bearing sentences from an overly
// long turn instead of spending the whole budget on the first paragraph. It is
// extractive only: every delivered sentence remains verbatim source text.
func focusEpisodicDeliveryText(text, query string, maxChars int) string {
	text = stripAssistantRoutingPreamble(text)
	if maxChars <= 0 || utf8.RuneCountInString(text) <= maxChars {
		return text
	}
	sentences := deliverySentences(text)
	if len(sentences) <= 1 {
		return ids.Truncate(text, maxChars, "…")
	}
	queryWords := uniqueLowerWords(query)
	wordWeights := deliveryQueryWordWeights(sentences, queryWords)
	best := 0
	for i := range sentences {
		sentences[i].score = deliverySentenceWeightedScore(sentences[i].text, queryWords, wordWeights)
		if sentences[i].score > sentences[best].score {
			best = i
		}
	}
	// Reserve a compact lead-sentence anchor when the evidence peak lies deep inside
	// a multi-sentence turn and the opening sentence itself carries user intent or
	// query signal, avoiding zero-signal assistant boilerplate preambles ("核查清楚了！").
	headAnchor := ""
	winBudget := maxChars
	leadText := strings.TrimSpace(sentences[0].text)
	if best > 1 && maxChars >= 360 && (sentences[0].score > 0 || strings.HasPrefix(leadText, "[user]")) {
		headCap := maxChars / 6
		if headCap > 200 {
			headCap = 200
		}
		candidateHead := ids.Truncate(sentences[0].text, headCap, "…")
		cost := utf8.RuneCountInString(candidateHead) + 3
		if cost < maxChars/3 {
			headAnchor = candidateHead
			winBudget = maxChars - cost
		}
	}
	low, high := best, best
	used := utf8.RuneCountInString(sentences[best].text)
	for {
		grew := false
		if high+1 < len(sentences) {
			n := utf8.RuneCountInString(sentences[high+1].text) + 1
			if used+n <= winBudget {
				high++
				used += n
				grew = true
			}
		}
		if low-1 >= 0 {
			n := utf8.RuneCountInString(sentences[low-1].text) + 1
			if used+n <= winBudget {
				low--
				used += n
				grew = true
			}
		}
		if !grew {
			break
		}
	}
	// Keep Markdown table headers paired with their separator/data rows when the window
	// starts on a table separator ("| :--- |") or the first table data row.
	if low > 0 && isMarkdownTableSeparatorLine(sentences[low].text) {
		headerCost := utf8.RuneCountInString(sentences[low-1].text) + 1
		if isMarkdownTableRowLine(sentences[low-1].text) && used+headerCost <= maxChars {
			low--
			used += headerCost
		} else if low < high {
			used -= utf8.RuneCountInString(sentences[low].text)
			low++
		}
	} else if low >= 2 && isMarkdownTableRowLine(sentences[low].text) &&
		isMarkdownTableSeparatorLine(sentences[low-1].text) &&
		isMarkdownTableRowLine(sentences[low-2].text) {
		pairCost := utf8.RuneCountInString(sentences[low-1].text) + utf8.RuneCountInString(sentences[low-2].text) + 2
		if used+pairCost <= maxChars {
			low -= 2
			used += pairCost
		}
	}
	selectedLines := joinDeliverySentenceWindow(sentences[low : high+1])
	selected := make([]string, 0, len(selectedLines)+2)
	if headAnchor != "" && low > 0 {
		selected = append(selected, headAnchor, "…")
	}
	selected = append(selected, selectedLines...)
	if low > 0 || high < len(sentences)-1 {
		return ids.Truncate(strings.Join(selected, "\n"), maxChars, "…")
	}
	return strings.Join(selected, "\n")
}

// joinDeliverySentenceWindow reassembles adjacent sentences that originated from the
// same source line back onto a single line so inline lists, bold spans, and multi-sentence
// paragraphs are not shattered into one-clause-per-line fragments.
func joinDeliverySentenceWindow(window []deliverySentence) []string {
	if len(window) == 0 {
		return nil
	}
	lines := make([]string, 0, len(window))
	curLineIdx := -1
	var cur strings.Builder
	for _, s := range window {
		part := strings.TrimSpace(s.text)
		if part == "" {
			continue
		}
		if cur.Len() == 0 {
			cur.WriteString(part)
			curLineIdx = s.lineIndex
			continue
		}
		if s.lineIndex == curLineIdx && curLineIdx >= 0 {
			// Strip a repeated speaker prefix when joining onto the same line.
			for _, prefix := range []string{"[user]", "[assistant]", "[agent]", "[tool]"} {
				if strings.HasPrefix(part, prefix) {
					part = strings.TrimSpace(strings.TrimPrefix(part, prefix))
					break
				}
			}
			if part != "" {
				prevStr := cur.String()
				lastRune, _ := utf8.DecodeLastRuneInString(prevStr)
				firstRune, _ := utf8.DecodeRuneInString(part)
				if lastRune < 0x80 && firstRune < 0x80 && lastRune != ' ' {
					cur.WriteByte(' ')
				}
				cur.WriteString(part)
			}
			continue
		}
		lines = append(lines, cur.String())
		cur.Reset()
		cur.WriteString(part)
		curLineIdx = s.lineIndex
	}
	if cur.Len() > 0 {
		lines = append(lines, cur.String())
	}
	return lines
}

func deliverySentences(text string) []deliverySentence {
	lines := strings.Split(text, "\n")
	out := make([]deliverySentence, 0, len(lines)+8)
	seen := make(map[string]bool, len(lines)+8)
	for lineIdx, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		isStructural := strings.HasPrefix(trimmed, "|") || strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "    ")
		speaker := ""
		body := trimmed
		for _, prefix := range []string{"[user]", "[assistant]", "[agent]", "[tool]"} {
			if strings.HasPrefix(body, prefix) {
				speaker = prefix
				body = strings.TrimSpace(strings.TrimPrefix(body, prefix))
				break
			}
		}
		parts := splitDeliveryLinePreservingPunct(body)
		if len(parts) == 0 {
			parts = []string{body}
		}
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			rendered := part
			if speaker != "" {
				rendered = speaker + " " + part
			}
			if !isStructural && seen[rendered] {
				continue
			}
			if !isStructural {
				seen[rendered] = true
			}
			out = append(out, deliverySentence{text: rendered, lineIndex: lineIdx})
		}
	}
	return out
}

// splitDeliveryLinePreservingPunct splits a single source line into sentence units
// while keeping terminal punctuation (。！？；;!?.) and any immediately following
// closing quotes/brackets/markdown markers attached to the sentence. Markdown table
// rows, code-fence lines, and inline backtick code spans are kept whole so paths,
// filenames (e.g. ~/.zcode, .efficacy.sqlite, *.md), and URLs are never shredded.
func splitDeliveryLinePreservingPunct(body string) []string {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return nil
	}
	if strings.HasPrefix(trimmed, "|") || strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "    ") {
		return []string{trimmed}
	}
	hasPairedBackticks := strings.Count(trimmed, "`") >= 2
	var out []string
	var cur strings.Builder
	inBacktick := false
	for i, n := 0, len(trimmed); i < n; {
		r, w := utf8.DecodeRuneInString(trimmed[i:])
		if hasPairedBackticks && r == '`' {
			inBacktick = !inBacktick
		}
		cur.WriteRune(r)
		if inBacktick {
			i += w
			continue
		}
		isBound := r == '。' || r == '！' || r == '？' || r == '；'
		if !isBound && (r == ';' || r == '!' || r == '?') {
			nextTokenChar := false
			if i+w < n {
				nr, _ := utf8.DecodeRuneInString(trimmed[i+w:])
				nextTokenChar = isDeliveryAlnum(nr) || nr == '=' || nr == '/' || nr == '_' || nr == '-' || nr == '.'
			}
			isBound = !nextTokenChar
		}
		if !isBound && r == '.' {
			prevDigit, nextAlnum, nextPathOrDot := false, false, false
			if i > 0 {
				pr, _ := utf8.DecodeLastRuneInString(trimmed[:i])
				prevDigit = pr >= '0' && pr <= '9'
			}
			if i+w < n {
				nr, _ := utf8.DecodeRuneInString(trimmed[i+w:])
				nextAlnum = isDeliveryAlnum(nr)
				nextPathOrDot = nr == '.' || nr == '/' || nr == '\\' || nr == '*' || nr == '`' || nr == '_' || nr == '-'
			}
			// Never split when "." is followed immediately by an identifier/path character
			// (e.g. "3.14", "~/.zcode", ".efficacy.sqlite", "*.md", "[recall].chars") or
			// when it is a numbered list prefix ("1. ", "2. ").
			isListNumberPrefix := prevDigit && strings.TrimSpace(cur.String()) == cur.String() && len(cur.String()) <= 6
			isBound = !nextAlnum && !nextPathOrDot && !isListNumberPrefix
		}
		i += w
		if isBound {
			// Keep trailing closing quotes, brackets, or inline markdown emphasis with the sentence,
			// but never steal an opening symmetric delimiter (` " ' *) that starts the next clause.
			for i < n {
				nr, nw := utf8.DecodeRuneInString(trimmed[i:])
				if isDeliveryClosingPunctForClause(cur.String(), nr) {
					cur.WriteRune(nr)
					i += nw
					continue
				}
				break
			}
			if s := strings.TrimSpace(cur.String()); s != "" {
				out = append(out, s)
			}
			cur.Reset()
		}
	}
	if tail := strings.TrimSpace(cur.String()); tail != "" {
		out = append(out, tail)
	}
	return out
}

func isDeliveryAlnum(r rune) bool {
	if r < 0x80 {
		return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
	}
	return r >= 0xA0
}

func isDeliveryClosingPunctForClause(clause string, r rune) bool {
	switch r {
	case '\u201d', '\u2019', '」', '』', '）', '》', '】', '〕', ')', ']', '}':
		return true
	case '`', '"', '\'', '*':
		return strings.Count(clause, string(r))%2 == 1
	}
	return false
}

func uniqueLowerWords(text string) []string {
	words := thinclient.SplitWords(strings.ToLower(strings.TrimSpace(text)))
	seen := make(map[string]bool, len(words))
	out := make([]string, 0, len(words))
	for _, word := range words {
		word = strings.TrimSpace(word)
		if word == "" || seen[word] {
			continue
		}
		seen[word] = true
		out = append(out, word)
	}
	return out
}

func deliveryQueryWordWeights(sentences []deliverySentence, queryWords []string) map[string]int {
	if len(queryWords) == 0 {
		return nil
	}
	nSent := len(sentences)
	if nSent < 1 {
		nSent = 1
	}
	lowerSents := make([]string, len(sentences))
	for i, s := range sentences {
		lowerSents[i] = strings.ToLower(s.text)
	}
	weights := make(map[string]int, len(queryWords))
	for _, w := range queryWords {
		sf := 0
		for _, ls := range lowerSents {
			if strings.Contains(ls, w) {
				sf++
			}
		}
		shannonGain := int(text.StringShannonBits(w) * 10.0)
		if shannonGain < 10 {
			shannonGain = 10
		}
		if shannonGain > 160 {
			shannonGain = 160
		}
		// Local sentence-rarity multiplier: words appearing in only 1-2 sentences of a long turn
		// carry up to ~3x higher focus weight than ubiquitous words repeated across every sentence.
		idfPerm := int(1000 + (int64(nSent)*1800)/int64(sf+1))
		if idfPerm > 3200 {
			idfPerm = 3200
		}
		weights[w] = (shannonGain * idfPerm) / 1000
	}
	return weights
}

func deliverySentenceWeightedScore(sentence string, queryWords []string, wordWeights map[string]int) int {
	if len(queryWords) == 0 {
		return 0
	}
	low := strings.ToLower(sentence)
	score := 0
	matched := 0
	for _, word := range queryWords {
		if strings.Contains(low, word) {
			w := wordWeights[word]
			if w <= 0 {
				w = int(text.StringShannonBits(word) * 10.0)
				if w < 10 {
					w = 10
				}
			}
			score += w
			matched++
		}
	}
	// Synergy gain when multiple distinct query tokens co-occur in the same sentence.
	if matched >= 2 {
		score += score * (matched - 1) / 4
	}
	return score
}

func isMarkdownTableRowLine(line string) bool {
	body := stripDeliverySpeakerPrefix(line)
	return strings.HasPrefix(body, "|") && strings.Count(body, "|") >= 2
}

func isMarkdownTableSeparatorLine(line string) bool {
	body := stripDeliverySpeakerPrefix(line)
	if !strings.HasPrefix(body, "|") || !strings.Contains(body, "-") {
		return false
	}
	for _, r := range body {
		switch r {
		case '|', '-', ':', ' ', '\t':
		default:
			return false
		}
	}
	return true
}
