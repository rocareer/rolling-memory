// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package memcore

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// VirtualPart represents an embedded code file, patch, or command output extracted from a raw turn
// and persisted in the database part store as a virtual evidence file.
type VirtualPart struct {
	Kind    string
	SubKind string
	URI     string
	Locator string
	Text    string
	Tool    string
}

const minVirtualPartTokens = TokenTier128

// IsThinkingScratchpadTurn reports whether a raw message is an internal [thinking] scratchpad monologue turn.
func IsThinkingScratchpadTurn(content string) bool {
	return strings.HasPrefix(strings.TrimSpace(content), "[thinking]")
}

// ExtractToolUseTargetFilePath extracts the target file path from a structured [tool_use <Name>] {...} message,
// even when the message itself is a short Read/Edit command under minVirtualPartTokens.
func ExtractToolUseTargetFilePath(content string) string {
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "[tool_use") {
		return ""
	}
	closeBr := strings.IndexByte(trimmed, ']')
	if closeBr <= 0 || closeBr+1 >= len(trimmed) {
		return ""
	}
	jsonBody := strings.TrimSpace(trimmed[closeBr+1:])
	var m map[string]any
	if err := json.Unmarshal([]byte(jsonBody), &m); err == nil && m != nil {
		if fp := firstMapString(m, "file_path", "path", "target_file", "filename"); fp != "" {
			return fp
		}
	}
	if fp := extractJSONStringFieldRaw(jsonBody, "file_path"); fp != "" {
		return fp
	}
	return extractJSONStringFieldRaw(jsonBody, "path")
}

// ExtractVirtualCodeAndToolParts extracts structured code files, patches, and tool outputs from a message body
// without mutating the original message text. An optional prevToolFilePath supplies the target file path
// from an immediately preceding [tool_use Read/Edit] turn when the current [tool_result] turn omits the path.
func ExtractVirtualCodeAndToolParts(content string, prevToolFilePath ...string) []VirtualPart {
	trimmed := strings.TrimSpace(content)
	if CountTokens(trimmed) < minVirtualPartTokens {
		return nil
	}
	var out []VirtualPart
	inheritedURI := ""
	if len(prevToolFilePath) > 0 {
		inheritedURI = strings.TrimSpace(prevToolFilePath[0])
	}

	// 1. Structured [tool_use <Name>] {...} JSON payloads
	if strings.HasPrefix(trimmed, "[tool_use") {
		closeBr := strings.IndexByte(trimmed, ']')
		if closeBr > 0 && closeBr+1 < len(trimmed) {
			header := strings.TrimSpace(trimmed[1:closeBr])
			toolName := strings.TrimSpace(strings.TrimPrefix(header, "tool_use"))
			jsonBody := strings.TrimSpace(trimmed[closeBr+1:])
			if p, ok := extractFromToolUseJSON(toolName, jsonBody); ok {
				out = append(out, p)
				return out
			}
		}
	}

	// 2. Structured [tool_result] outputs (numbered source code lines, diffs, or test outputs)
	if strings.HasPrefix(trimmed, "[tool_result]") {
		body := strings.TrimSpace(strings.TrimPrefix(trimmed, "[tool_result]"))
		if CountTokens(body) >= minVirtualPartTokens {
			uri := detectEmbeddedFilePath(body)
			loc := detectNumberedLineRange(body)
			subKind := "code"
			if loc == "" && (strings.Contains(body, "test session starts") || strings.Contains(body, "Exit code ")) {
				subKind = "command_output"
			}
			if loc == "" {
				lines := strings.Count(body, "\n") + 1
				loc = fmt.Sprintf("L1-L%d", lines)
			}
			if uri == "" && inheritedURI != "" && subKind == "code" {
				uri = inheritedURI
			}
			if uri == "" {
				uri = "virtual://tool_result/" + subKind
			}
			out = append(out, VirtualPart{
				Kind:    "file",
				SubKind: subKind,
				URI:     uri,
				Locator: loc,
				Text:    body,
				Tool:    "tool_result",
			})
			return out
		}
	}

	// 2b. Internal [thinking] scratchpad monologues -> stored as virtual part evidence without polluting structured memory assertion fact graph
	if IsThinkingScratchpadTurn(trimmed) {
		body := strings.TrimSpace(strings.TrimPrefix(trimmed, "[thinking]"))
		if CountTokens(body) >= minVirtualPartTokens {
			lines := strings.Count(body, "\n") + 1
			out = append(out, VirtualPart{
				Kind:    "file",
				SubKind: "thinking",
				URI:     "virtual://thinking",
				Locator: fmt.Sprintf("L1-L%d", lines),
				Text:    body,
				Tool:    "thinking",
			})
			return out
		}
	}

	// 3. Fenced code blocks ```lang ... ```
	rem := trimmed
	seq := 0
	for {
		startFence := strings.Index(rem, "```")
		if startFence < 0 {
			break
		}
		afterStart := rem[startFence+3:]
		nl := strings.IndexByte(afterStart, '\n')
		if nl < 0 {
			break
		}
		lang := strings.TrimSpace(afterStart[:nl])
		codeBody := afterStart[nl+1:]
		endFence := strings.Index(codeBody, "```")
		if endFence < 0 {
			break
		}
		block := strings.TrimSpace(codeBody[:endFence])
		if CountTokens(block) >= minVirtualPartTokens {
			seq++
			uri := detectEmbeddedFilePath(block)
			if uri == "" {
				if lang != "" {
					uri = fmt.Sprintf("virtual://code_block/%s#%d", lang, seq)
				} else {
					uri = fmt.Sprintf("virtual://code_block#%d", seq)
				}
			}
			lines := strings.Count(block, "\n") + 1
			out = append(out, VirtualPart{
				Kind:    "file",
				SubKind: "code",
				URI:     uri,
				Locator: fmt.Sprintf("L1-L%d", lines),
				Text:    block,
				Tool:    "code_block",
			})
		}
		rem = codeBody[endFence+3:]
	}
	out = append(out, extractDenseTabularParts(trimmed)...)
	return out
}

func extractFromToolUseJSON(toolName, jsonBody string) (VirtualPart, bool) {
	var m map[string]any
	if err := json.Unmarshal([]byte(jsonBody), &m); err == nil && m != nil {
		filePath := firstMapString(m, "file_path", "path", "target_file", "filename")
		codeText := firstMapString(m, "content", "new_string", "patch", "diff", "command")
		if CountTokens(codeText) < minVirtualPartTokens {
			return VirtualPart{}, false
		}
		if filePath == "" {
			filePath = detectEmbeddedFilePath(codeText)
		}
		if filePath == "" {
			filePath = "virtual://tool_use/" + strings.ToLower(toolName)
		}
		lines := strings.Count(codeText, "\n") + 1
		return VirtualPart{
			Kind:    "file",
			SubKind: "code",
			URI:     filePath,
			Locator: fmt.Sprintf("L1-L%d", lines),
			Text:    codeText,
			Tool:    toolName,
		}, true
	}
	// Fallback when the 10KB JSON string was truncated at the tail by upstream transport
	filePath := extractJSONStringFieldRaw(jsonBody, "file_path")
	if filePath == "" {
		filePath = extractJSONStringFieldRaw(jsonBody, "path")
	}
	codeText := extractJSONStringFieldRaw(jsonBody, "content")
	if codeText == "" {
		codeText = extractJSONStringFieldRaw(jsonBody, "new_string")
	}
	if CountTokens(codeText) < minVirtualPartTokens {
		return VirtualPart{}, false
	}
	if filePath == "" {
		filePath = "virtual://tool_use/" + strings.ToLower(toolName)
	}
	lines := strings.Count(codeText, "\n") + 1
	return VirtualPart{
		Kind:    "file",
		SubKind: "code",
		URI:     filePath,
		Locator: fmt.Sprintf("L1-L%d", lines),
		Text:    codeText,
		Tool:    toolName,
	}, true
}

func firstMapString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func extractJSONStringFieldRaw(raw, key string) string {
	pat := `"` + key + `"`
	idx := strings.Index(raw, pat)
	if idx < 0 {
		return ""
	}
	rest := raw[idx+len(pat):]
	colon := strings.IndexByte(rest, ':')
	if colon < 0 {
		return ""
	}
	rest = strings.TrimSpace(rest[colon+1:])
	if !strings.HasPrefix(rest, `"`) {
		return ""
	}
	var b strings.Builder
	escaped := false
	for i := 1; i < len(rest); i++ {
		ch := rest[i]
		if escaped {
			switch ch {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '"', '\\', '/':
				b.WriteByte(ch)
			default:
				b.WriteByte(ch)
			}
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if ch == '"' {
			break
		}
		b.WriteByte(ch)
	}
	return b.String()
}

func detectEmbeddedFilePath(text string) string {
	for _, line := range strings.SplitN(text, "\n", 20) {
		line = strings.TrimSpace(line)
		for _, field := range strings.Fields(line) {
			cleaned := strings.Trim(field, "`\"'*:(),[]{}")
			if strings.HasPrefix(cleaned, "/") && strings.Count(cleaned, "/") >= 2 && hasFormalPathExtension(cleaned) {
				return cleaned
			}
			if strings.Count(cleaned, "/") >= 1 && hasFormalPathExtension(cleaned) && !strings.HasPrefix(cleaned, "http") {
				return cleaned
			}
		}
	}
	return ""
}

// hasFormalPathExtension 零扩展名白名单：只要符合 POSIX/URI 路径形式（含 / 分隔、带非空扩展名且扩展名为 1-8 位 ASCII 字母/数字）即合法。
func hasFormalPathExtension(p string) bool {
	if colon := strings.LastIndexByte(p, ':'); colon > 0 {
		p = p[:colon]
	}
	slash := strings.LastIndexByte(p, '/')
	if slash < 0 || slash == len(p)-1 {
		return false
	}
	base := p[slash+1:]
	dot := strings.LastIndexByte(base, '.')
	if dot <= 0 || dot == len(base)-1 {
		return false
	}
	ext := base[dot+1:]
	if len(ext) > 8 {
		return false
	}
	hasLetter := false
	for _, r := range ext {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return false
		}
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			hasLetter = true
		}
	}
	return hasLetter && !strings.Contains(p, "..") && !strings.Contains(p, "://")
}

func detectNumberedLineRange(body string) string {
	lines := strings.Split(body, "\n")
	firstNum, lastNum, numberedCnt := 0, 0, 0
	for _, ln := range lines {
		ln = strings.TrimLeft(ln, " ")
		if ln == "" {
			continue
		}
		n, ok := parseLeadingLineNumber(ln)
		if ok {
			numberedCnt++
			if firstNum == 0 {
				firstNum = n
			}
			lastNum = n
		}
	}
	if numberedCnt >= 3 && firstNum > 0 && lastNum >= firstNum {
		return fmt.Sprintf("L%d-L%d", firstNum, lastNum)
	}
	return ""
}

// HasNumberedLineRange reports whether text contains >= 3 consecutively numbered source lines.
func HasNumberedLineRange(text string) bool {
	return detectNumberedLineRange(text) != ""
}

func parseLeadingLineNumber(ln string) (int, bool) {
	i := 0
	for i < len(ln) && ln[i] >= '0' && ln[i] <= '9' {
		i++
	}
	if i == 0 || i > 7 || i >= len(ln) {
		return 0, false
	}
	sep := ln[i]
	if sep != '\t' && sep != ' ' && sep != ':' {
		return 0, false
	}
	val := 0
	for j := 0; j < i; j++ {
		val = val*10 + int(ln[j]-'0')
	}
	return val, val > 0
}

// FocusOversizedToolTurnSlices bounds the number of encoder slices for oversized structured tool turns
// ([tool_use Write/Edit] or [tool_result] dumps) where the full code payload is already stored in the virtual part table.
// Both kept slices remain 100% verbatim substrings of the original turn text.
func FocusOversizedToolTurnSlices(text string, slices []string) []string {
	if len(slices) <= 2 {
		return slices
	}
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "[tool_use") ||
		strings.HasPrefix(trimmed, "[tool_result]") ||
		strings.HasPrefix(trimmed, "[thinking]") {
		return []string{slices[0], slices[len(slices)-1]}
	}
	return slices
}

// splitLongLineByTokens breaks an extreme single line (> TokenTier128 tokens without newlines, e.g. minified JSON/log/hex)
// into TokenTier128-sized sub-lines so sliding-window scoring and budget expansion never choke on a single giant line.
func splitLongLineByTokens(s string, chunkTokens int) []string {
	if chunkTokens <= 0 {
		chunkTokens = TokenTier128
	}
	if CountTokens(s) <= chunkTokens {
		return []string{s}
	}
	rs := []rune(s)
	var out []string
	for len(rs) > 0 {
		// 切点 = 满足 CountTokens(rs[:L]) <= chunkTokens 的**最大** L（与原实现语义逐字等价）。
		// 原实现直接在 [1, len(rs)] 上全局二分：每次探测都要分词一个长度可达 n 的前缀，
		// 单块成本 O(n log n)，长行（100KB+ 压缩 JSON / 工具结果）下整函数秒级——2026-11-04
		// 实测 153KB 单行 1.5s（单次分词 9ms 的 168 倍），并发下多路 worker 堆在同一函数。
		// 改为「指数定界 + 局部二分」：先以倍增找到首个不可行上界（每次探测长度 ≤ 2× 答案），
		// 再在该窄区间内二分。计数长度恒被答案量级（≈chunkTokens 词元）夹住 ⇒ 与 n 解耦。
		lo, hi := 0, 0
		step := 1
		for {
			cand := hi + step
			if cand > len(rs) {
				cand = len(rs)
			}
			if CountTokens(string(rs[:cand])) <= chunkTokens {
				lo = cand
				if cand == len(rs) {
					hi = cand
					break
				}
				hi = cand
				step *= 2
				continue
			}
			hi = cand
			break
		}
		if lo < 1 {
			// 单 rune 前缀恒 ≥1 词元且 chunkTokens ≥ 1 ⇒ 至少推进 1 rune（防死循环，与原 lo≥1 同口径）。
			lo = 1
		}
		for lo < hi {
			mid := (lo + hi + 1) / 2
			if CountTokens(string(rs[:mid])) <= chunkTokens {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		// Prefer breaking on whitespace or punctuation near the token boundary if available
		cut := lo
		if cut < len(rs) {
			for back := cut; back > (cut*3)/4; back-- {
				ch := rs[back-1]
				if ch == ' ' || ch == ',' || ch == ';' || ch == '}' || ch == ')' || ch == '。' || ch == '，' {
					cut = back
					break
				}
			}
		}
		out = append(out, strings.TrimSpace(string(rs[:cut])))
		rs = rs[cut:]
	}
	return out
}

// ExtractFocusedCodeWindow extracts a compact, query- and anchor-centered window from an oversized raw trace or virtual evidence part
// using the standard power-of-two tokenz-r budget ladder (128 / 256 / 512 / 1024 / 2048 / 4096 / 8192 tokens).
func ExtractFocusedCodeWindow(text string, queryTokens []string, maxTokens int) (snippet string, fileURI string, locator string, compacted bool) {
	budget := NormalizeTokenTierBudget(maxTokens)
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || CountTokens(trimmed) <= budget {
		return trimmed, "", "", false
	}

	// If this is a [tool_use <Name>] JSON with an escaped "content" field, unwrap the virtual file for clean line windowing.
	envelopeHeader := ""
	bodyToWindow := trimmed
	if strings.HasPrefix(trimmed, "[tool_use") {
		if closeBr := strings.IndexByte(trimmed, ']'); closeBr > 0 {
			toolTag := trimmed[:closeBr+1]
			jsonPart := strings.TrimSpace(trimmed[closeBr+1:])
			if vp, ok := extractFromToolUseJSON(strings.TrimPrefix(strings.TrimSuffix(toolTag, "]"), "[tool_use"), jsonPart); ok && vp.Text != "" {
				fileURI = vp.URI
				envelopeHeader = fmt.Sprintf("%s file_path=%s", toolTag, vp.URI)
				bodyToWindow = vp.Text
			}
		}
	} else if strings.HasPrefix(trimmed, "[tool_result]") {
		fileURI = detectEmbeddedFilePath(trimmed)
		envelopeHeader = "[tool_result]"
		if fileURI != "" {
			envelopeHeader += " file=" + fileURI
		}
		bodyToWindow = strings.TrimSpace(strings.TrimPrefix(trimmed, "[tool_result]"))
	}

	rawLines := strings.Split(bodyToWindow, "\n")
	var lines []string
	for _, rl := range rawLines {
		if CountTokens(rl) > TokenTier128 {
			sub := strings.ReplaceAll(rl, `\n`, "\n")
			sub = strings.ReplaceAll(sub, "}, {", "},\n{")
			sub = strings.ReplaceAll(sub, ".  ", ".\n")
			for _, sl := range strings.Split(sub, "\n") {
				if CountTokens(sl) > TokenTier128 {
					for _, cl := range strings.Split(sl, ". ") {
						cl = strings.TrimSpace(cl)
						if cl == "" {
							continue
						}
						if !strings.HasSuffix(cl, ".") {
							cl += "."
						}
						if CountTokens(cl) > TokenTier128 {
							lines = append(lines, splitLongLineByTokens(cl, TokenTier128)...)
						} else {
							lines = append(lines, cl)
						}
					}
				} else {
					lines = append(lines, sl)
				}
			}
		} else {
			lines = append(lines, rl)
		}
	}
	if len(lines) > 4 {
		var filtered []string
		skipMenu := false
		for _, ln := range lines {
			tln := strings.TrimSpace(ln)
			if strings.Contains(tln, "AVAILABLE ACTIONS:") {
				skipMenu = true
				filtered = append(filtered, ln)
				continue
			}
			if skipMenu {
				if strings.HasPrefix(tln, `", "role":`) || strings.HasPrefix(tln, `"},`) || strings.HasPrefix(tln, `{`) {
					skipMenu = false
					filtered = append(filtered, ln)
				}
				continue
			}
			filtered = append(filtered, ln)
		}
		lines = filtered
	}
	if len(lines) <= 1 {
		if CountTokens(bodyToWindow) > budget {
			half := budget / 2
			if half < TokenTier128 {
				half = TokenTier128
			}
			headPart := TruncateToTokens(bodyToWindow, half)
			out := headPart
			if envelopeHeader != "" {
				out = envelopeHeader + "\n" + out
			}
			return out, fileURI, "", true
		}
		return trimmed, fileURI, "", false
	}

	// Precompute per-line tokenz-r counts once so window expansion is O(lines) without repeated BPE passes.
	lineToks := make([]int, len(lines))
	for i, ln := range lines {
		t := CountTokens(ln)
		if t <= 0 {
			t = 1
		}
		lineToks[i] = t
	}

	// Score each line by query token matches (longer identifier tokens and verbatim anchor spans carry higher weight).
	lowToks := make([]string, 0, len(queryTokens))
	seenTok := map[string]bool{}
	for _, tk := range queryTokens {
		tl := strings.ToLower(strings.TrimSpace(tk))
		if StringShannonBits(tl) > 2.0 && !seenTok[tl] {
			seenTok[tl] = true
			lowToks = append(lowToks, tl)
		}
	}

	lineScores := make([]float64, len(lines))
	bestLine := 0
	bestWindowScore := -1.0
	for i, ln := range lines {
		lowLn := strings.ToLower(ln)
		codePart, _ := stripOptionalLineNumber(ln)
		trimmedCode := strings.TrimSpace(codePart)
		isHeader := isCodeScopeHeaderLine(trimmedCode)
		sc := 0.0
		for _, tk := range lowToks {
			if strings.Contains(lowLn, tk) {
				w := 1.0
				if strings.Contains(tk, " ") && StringShannonBits(tk) >= 16.0 {
					// Verbatim multi-token anchor span from the assertion itself: pin center directly to this anchor line.
					w = 24.0
				} else if StringShannonBits(tk) >= 8.0 || strings.Contains(tk, "_") {
					w = 3.0
					if isHeader {
						w = 5.5
					}
				} else if StringShannonBits(tk) >= 4.5 {
					w = 1.8
				}
				sc += w
			}
		}
		lineScores[i] = sc
	}

	// Sliding window over lines: for block-header lines, weight subsequent body lines (+1..+16) alongside neighbors
	// so that a matched declaration header naturally captures its indented body.
	for i := range lines {
		winSc := lineScores[i] * 2.5
		lo := i - 6
		if lo < 0 {
			lo = 0
		}
		hi := i + 6
		codePart, _ := stripOptionalLineNumber(lines[i])
		if isCodeScopeHeaderLine(strings.TrimSpace(codePart)) {
			hi = i + 16
		}
		if hi >= len(lines) {
			hi = len(lines) - 1
		}
		for j := lo; j <= hi; j++ {
			if j == i {
				continue
			}
			dist := i - j
			if dist < 0 {
				dist = -dist
			}
			winSc += lineScores[j] / float64(1+dist)
		}
		if winSc > bestWindowScore {
			bestWindowScore = winSc
			bestLine = i
		}
	}

	if envelopeHeader != "" {
		envToks := CountTokens(envelopeHeader) + 8
		if budget-envToks >= TokenTier128 {
			budget -= envToks
		}
	}

	if bestWindowScore <= 0 {
		// Extreme Case 3: Zero lexical overlap (e.g. vector/topology hop recall) -> take 2/3 head + 1/3 tail tokens
		var headLines []string
		used := 0
		headBudget := (budget * 2) / 3
		for i := 0; i < len(lines); i++ {
			tLen := lineToks[i]
			if used+tLen > headBudget && len(headLines) > 0 {
				break
			}
			headLines = append(headLines, lines[i])
			used += tLen
		}
		var tailLines []string
		tailBudget := budget - used
		for i := len(lines) - 1; i >= len(headLines); i-- {
			tLen := lineToks[i]
			if tLen > tailBudget {
				break
			}
			tailLines = append([]string{lines[i]}, tailLines...)
			tailBudget -= tLen
		}
		locator, _ = windowNumberedLineRange(lines, 0, len(lines)-1)
		body := strings.Join(headLines, "\n")
		if len(tailLines) > 0 {
			body += "\n…\n" + strings.Join(tailLines, "\n")
		}
		if envelopeHeader != "" {
			if locator != "" {
				return fmt.Sprintf("%s [%s]\n%s", envelopeHeader, locator, body), fileURI, locator, true
			}
			return envelopeHeader + "\n" + body, fileURI, "", true
		}
		return body, fileURI, locator, true
	}

	// Expand outward from bestLine while respecting tokenz-r budget.
	// First-principles structural block rule: when bestLine is a block/scope header (e.g. a function/class/section
	// declaration ending with ':', '{', '('), its substantive body lives in the subsequent indented block (indent > headerIndent).
	// Prioritize downward expansion through the indented body before spending budget on preceding unrelated lines.
	startLine, endLine := bestLine, bestLine
	usedTokens := lineToks[bestLine]
	if usedTokens > budget {
		lines[bestLine] = TruncateToTokens(lines[bestLine], budget)
		usedTokens = budget
	}
	bestCodePart, _ := stripOptionalLineNumber(lines[bestLine])
	bestTrimmed := strings.TrimSpace(bestCodePart)
	var farSubWindow []string
	if isCodeScopeHeaderLine(bestTrimmed) {
		headerIndent := countLeadingIndentCols(bestCodePart)
		// Find where this indented block ends
		blockEnd := bestLine
		for k := bestLine + 1; k < len(lines); k++ {
			nextCode, _ := stripOptionalLineNumber(lines[k])
			nextTrim := strings.TrimSpace(nextCode)
			if nextTrim != "" && !strings.HasPrefix(nextTrim, "#") && !strings.HasPrefix(nextTrim, "//") {
				if countLeadingIndentCols(nextCode) <= headerIndent {
					break
				}
			}
			blockEnd = k
		}
		//Allow up to 2 preceding context lines first
		for up := 0; up < 2 && startLine > 0; up++ {
			tLen := lineToks[startLine-1]
			if usedTokens+tLen <= budget {
				startLine--
				usedTokens += tLen
			}
		}
		// Expand downward through the indented block while within budget
		for endLine < blockEnd {
			tLen := lineToks[endLine+1]
			if usedTokens+tLen > budget {
				break
			}
			endLine++
			usedTokens += tLen
		}
		// Extreme Case 2: Giant indented block where the header was matched at bestLine, but a high-scoring statement
		// (e.g. a raise ValueError / return line deep at line +120) lies beyond the contiguous budget.
		if endLine < blockEnd {
			farLine := -1
			farScore := 2.9
			for k := endLine + 1; k <= blockEnd; k++ {
				if lineScores[k] > farScore {
					farScore = lineScores[k]
					farLine = k
				}
			}
			if farLine > endLine {
				// Reserve up to 1/3 of budget for the deep hit sub-window inside the same block
				subBudget := budget / 3
				if subBudget < 48 {
					subBudget = 48
				}
				for endLine > bestLine && budget-usedTokens < subBudget {
					usedTokens -= lineToks[endLine]
					endLine--
				}
				fStart, fEnd := farLine, farLine
				fUsed := lineToks[farLine]
				for fStart > endLine+1 && fUsed+lineToks[fStart-1] <= subBudget {
					fStart--
					fUsed += lineToks[fStart]
				}
				for fEnd < blockEnd && fUsed+lineToks[fEnd+1] <= subBudget {
					fEnd++
					fUsed += lineToks[fEnd]
				}
				farSubWindow = lines[fStart : fEnd+1]
				usedTokens += fUsed
			}
		}
	}
	for {
		progressed := false
		if endLine+1 < len(lines) && len(farSubWindow) == 0 {
			tLen := lineToks[endLine+1]
			if usedTokens+tLen <= budget {
				endLine++
				usedTokens += tLen
				progressed = true
			}
		}
		if startLine > 0 {
			tLen := lineToks[startLine-1]
			if usedTokens+tLen <= budget {
				startLine--
				usedTokens += tLen
				progressed = true
			}
		}
		if !progressed {
			break
		}
	}

	// Attach enclosing scope signature headers (e.g., enclosing def/func and class/type declarations)
	// when the focused window starts inside a method/function body, bounded by TokenTier128.
	var scopePrefixLines []string
	prefixTokens := 0
	if startLine > 0 {
		winIndent := windowMinCodeIndent(lines[startLine : endLine+1])
		curIndent := winIndent
		for i := startLine - 1; i >= 0; i-- {
			codePart, okCode := stripOptionalLineNumber(lines[i])
			if !okCode {
				codePart = lines[i]
			}
			trimmedCode := strings.TrimSpace(codePart)
			if trimmedCode == "" || strings.HasPrefix(trimmedCode, "#") || strings.HasPrefix(trimmedCode, "//") {
				continue
			}
			ind := countLeadingIndentCols(codePart)
			if ind < curIndent && isCodeScopeHeaderLine(trimmedCode) {
				if startLine-i <= 8 {
					extraToks := 0
					for k := i; k < startLine; k++ {
						extraToks += lineToks[k]
					}
					if extraToks <= TokenTier128/2 {
						startLine = i
						curIndent = ind
						continue
					}
				}
				if prefixTokens+lineToks[i] <= TokenTier128 {
					scopePrefixLines = append([]string{lines[i]}, scopePrefixLines...)
					prefixTokens += lineToks[i]
				}
				curIndent = ind
				if ind == 0 || len(scopePrefixLines) >= 2 {
					break
				}
			}
		}
	}

	locLines := lines[startLine : endLine+1]
	if len(farSubWindow) > 0 {
		locLines = append(append([]string{}, locLines...), farSubWindow...)
	}
	locator, _ = windowNumberedLineRange(locLines, 0, len(locLines)-1)

	windowBody := strings.Join(lines[startLine:endLine+1], "\n")
	if len(farSubWindow) > 0 {
		windowBody += "\n  …\n" + strings.Join(farSubWindow, "\n")
	}
	if len(scopePrefixLines) > 0 {
		windowBody = strings.Join(scopePrefixLines, "\n  …\n") + "\n  …\n" + windowBody
	}
	if envelopeHeader != "" {
		if locator != "" {
			return fmt.Sprintf("%s [%s]\n%s", envelopeHeader, locator, windowBody), fileURI, locator, true
		}
		return envelopeHeader + "\n" + windowBody, fileURI, "", true
	}
	if locator == "" {
		return windowBody, fileURI, "", true
	}
	return fmt.Sprintf("[%s]\n%s", locator, windowBody), fileURI, locator, true
}

// windowNumberedLineRange 取窗口内**真实行号**区间（无行号 ⇒ 空）：
// 仅当窗口内出现 ≥3 行显式前导行号（与 detectNumberedLineRange 同阈值）才发行区间。
// 反例守卫：pytest traceback / 命令输出等无行号文本，窗口序号绝不能被当成文件行号
// （禁止回退 "L<start+1>-L<end+1>"，否则会把 traceback 标成 L57-L87 冒充源码区间）。
func windowNumberedLineRange(lines []string, start, end int) (string, bool) {
	if start < 0 || end < start || end >= len(lines) {
		return "", false
	}
	first, last, cnt := 0, 0, 0
	for i := start; i <= end; i++ {
		n, ok := parseLeadingLineNumber(strings.TrimLeft(lines[i], " "))
		if !ok {
			continue
		}
		cnt++
		if first == 0 {
			first = n
		}
		last = n
	}
	if cnt < 3 || first <= 0 || last < first {
		return "", false
	}
	return fmt.Sprintf("L%d-L%d", first, last), true
}

func stripOptionalLineNumber(ln string) (string, bool) {
	trimmedLeft := strings.TrimLeft(ln, " ")
	i := 0
	for i < len(trimmedLeft) && trimmedLeft[i] >= '0' && trimmedLeft[i] <= '9' {
		i++
	}
	if i == 0 || i > 7 || i >= len(trimmedLeft) {
		return ln, false
	}
	sep := trimmedLeft[i]
	if sep != '\t' && sep != ' ' && sep != ':' {
		return ln, false
	}
	rest := trimmedLeft[i+1:]
	return rest, true
}

func countLeadingIndentCols(s string) int {
	cols := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ' ':
			cols++
		case '\t':
			cols += 4
		default:
			return cols
		}
	}
	return cols
}

func windowMinCodeIndent(winLines []string) int {
	minInd := 999
	for _, ln := range winLines {
		codePart, _ := stripOptionalLineNumber(ln)
		trimmed := strings.TrimSpace(codePart)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "//") {
			continue
		}
		ind := countLeadingIndentCols(codePart)
		if ind < minInd {
			minInd = ind
		}
	}
	if minInd == 999 {
		return 0
	}
	return minInd
}

func isCodeScopeHeaderLine(trimmedCode string) bool {
	// Language-agnostic syntactic block-header check (zero language keyword list):
	// An enclosing scope declaration line in structured source code ends with a block/parameter opener (':', '{', '(')
	// and contains an identifier followed by a space or parameter list '(' without starting with a punctuation symbol.
	// 保留：熵门在此是切分等价的结构依赖（实测移除即令 150KB 无空白行分块内容漂移）；转 Top-K 预算属独立改造。
	if StringShannonBits(trimmedCode) < 6.0 {
		return false
	}
	firstRune, _ := utf8.DecodeRuneInString(trimmedCode)
	if !unicode.IsLetter(firstRune) && firstRune != '_' && firstRune != '@' {
		return false
	}
	lastByte := trimmedCode[len(trimmedCode)-1]
	if lastByte != ':' && lastByte != '{' && lastByte != '(' {
		return false
	}
	return strings.Contains(trimmedCode, "(") || strings.Contains(trimmedCode, " ")
}

// FocusArtifactSummary computes an anchor- and query-centered summary window for an evidence artifact [A#m]
// (whether a document, code file, tool output, or structured text part) using the standard tokenz-r tier ladder (256 / 512 / 1024 tokens).
// Universal tier rule: short/medium parts (<= TokenTier512) are preserved intact; oversized parts (> TokenTier512) are windowed
// to the requested tier budget ( upgrading TokenTier256 to TokenTier512 when the source artifact itself exceeds TokenTier512).
func FocusArtifactSummary(text string, cues []string, maxTokens int) (summary string, locator string) {
	if maxTokens <= 0 {
		maxTokens = TokenTier256
	}
	budget := NormalizeTokenTierBudget(maxTokens)
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", ""
	}
	totalToks := CountTokens(trimmed)
	if budget == TokenTier256 && totalToks > TokenTier256 {
		budget = TokenTier512
	}
	if totalToks <= budget {
		if strings.Contains(trimmed, "\n") && detectNumberedLineRange(trimmed) != "" {
			return trimmed, detectNumberedLineRange(trimmed)
		}
		if strings.Count(trimmed, "\n") <= 2 {
			return strings.TrimSpace(strings.ReplaceAll(trimmed, "\n", " ")), ""
		}
		return trimmed, ""
	}
	win, _, loc, compacted := ExtractFocusedCodeWindow(trimmed, cues, budget)
	if compacted && win != "" {
		return win, loc
	}
	return TruncateToTokens(strings.TrimSpace(strings.ReplaceAll(trimmed, "\n", " ")), budget), ""
}

// MergeArtifactSummaryWindow appends a newly focused non-overlapping window to an existing artifact summary
// when multiple assertions on the same turn anchor to distinct regions of the same evidence artifact [A#m],
// governed by the standard tokenz-r tier ladder (default TokenTier1024, cap TokenTier4096).
func MergeArtifactSummaryWindow(existingSummary, existingLoc, newWindow, newLoc string, maxTotalTokens int) (summary string, locator string) {
	if maxTotalTokens <= 0 {
		maxTotalTokens = TokenTier1024
	}
	budget := NormalizeTokenTierBudget(maxTotalTokens)
	newWindow = strings.TrimSpace(newWindow)
	if newWindow == "" {
		return existingSummary, existingLoc
	}
	if existingSummary == "" {
		return newWindow, newLoc
	}
	if strings.Contains(existingSummary, newWindow) || (newLoc != "" && existingLoc != "" && strings.Contains(existingLoc, newLoc)) {
		return existingSummary, existingLoc
	}
	if strings.Contains(newWindow, existingSummary) {
		if newLoc != "" {
			return newWindow, newLoc
		}
		return newWindow, existingLoc
	}
	combined := existingSummary + "\n  …\n" + newWindow
	if CountTokens(combined) > budget {
		return existingSummary, existingLoc
	}
	loc := existingLoc
	if newLoc != "" {
		if loc == "" || strings.HasPrefix(loc, "part#") {
			loc = newLoc
		} else if !strings.Contains(loc, newLoc) {
			loc = loc + "+" + newLoc
		}
	}
	return combined, loc
}
