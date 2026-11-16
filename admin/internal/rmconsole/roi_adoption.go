// plugin: rolling-memory v0.0.58
package rollingmemory

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// roiAdoptionAuditSummary aggregates deterministic post-recall intersection audit metrics.
type roiAdoptionAuditSummary struct {
	AuditedRecalls      int64
	AdoptedRecalls      int64
	AdoptedToolCount    int64
	AdoptedContextCount int64
	UnadoptedCount      int64
	PendingCount        int64
	EchoExcludedTotal   int64
	AdoptionRatePct     float64
}

type zcodeRecallCallWindow struct {
	sessionID string
	callID    string
	startMS   int64
	endMS     int64
	nextMS    int64
	cue       string
	output    string
}

type followupCorpusPair struct {
	harnessID   string
	toolCorpus  string
	replyCorpus string
	delivered   string
	cue         string
}

// byteShannonInfoMass computes the continuous byte-level Shannon information mass
// H_byte(s) * log2(1 + |s|_bytes) as a pure ranking gain for prioritizing matched anchors.
func byteShannonInfoMass(s string) float64 {
	n := len(s)
	if n == 0 {
		return 0
	}
	var freq [256]int
	for i := 0; i < n; i++ {
		freq[s[i]]++
	}
	invN := 1.0 / float64(n)
	var entropy float64
	for _, c := range freq {
		if c > 0 {
			p := float64(c) * invN
			entropy -= p * math.Log2(p)
		}
	}
	return entropy * math.Log2(1.0+float64(n))
}

func isEnvelopeOrRoleMetaToken(lower string) bool {
	switch lower {
	case "[assistant]", "[user]", "[system]", "[tool]", "assistant", "user",
		"rolling-memory-recall", "rolling-memory", "mem:self", "default", "absent", "false", "true", "results":
		return true
	}
	for _, pfx := range []string{
		"<rolling-memory-recall", "scope=", "now=", "results=", "raw=", "absent=", "gaps=", "ms=",
		"mcp__rolling-memory", "rolling-memory:", "@ref:",
	} {
		if strings.HasPrefix(lower, pfx) {
			return true
		}
	}
	return false
}

func isStructuralCoordToken(tok string) (isPath bool, isSymbol bool) {
	if tok == "" {
		return false, false
	}
	lower := strings.ToLower(tok)
	if isEnvelopeOrRoleMetaToken(lower) {
		return false, false
	}
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "//") {
		return false, false
	}
	for _, sysPfx := range []string{"/dev/", "/proc/", "/sys/", "/usr/bin/", "/bin/"} {
		if strings.HasPrefix(lower, sysPfx) {
			return false, false
		}
	}

	allASCII := true
	hasLetter := false
	hasLower := false
	hasInnerUpper := false
	hasDigit := false
	hasStructSep := false
	slashIdx := strings.IndexByte(tok, '/')
	dotIdx := strings.LastIndexByte(tok, '.')

	for i := 0; i < len(tok); i++ {
		b := tok[i]
		if b > 127 {
			allASCII = false
			break
		}
		if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') {
			hasLetter = true
		}
		if b >= 'a' && b <= 'z' {
			hasLower = true
		}
		if i > 0 && b >= 'A' && b <= 'Z' {
			hasInnerUpper = true
		}
		if b >= '0' && b <= '9' {
			hasDigit = true
		}
		if b == '_' || b == '-' || b == ':' || b == '=' {
			hasStructSep = true
		}
	}
	if !allASCII {
		return false, false
	}

	// 1. Formal file path or filename with extension (e.g. "admin/internal/rmconsole/roi_facts.go", "roi.vue")
	if dotIdx > 1 && dotIdx < len(tok)-1 && dotIdx > slashIdx && hasLetter {
		ext := tok[dotIdx+1:]
		extAlpha := len(ext) >= 2 && len(ext) <= 6
		for i := 0; i < len(ext); i++ {
			c := ext[i]
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
				extAlpha = false
				break
			}
		}
		if extAlpha && !(slashIdx < 0 && (lower == "agents.md" || lower == "claude.md" || lower == "readme.md")) {
			return true, false
		}
	}
	if slashIdx > 0 && slashIdx < len(tok)-1 && hasLetter && !strings.ContainsRune(tok, ' ') {
		// Extensionless path is only treated as a concrete target coordinate when it is a binary/socket/temp artifact
		// with '-' or '_' in its basename (e.g. "/tmp/rolling-admin-srv-9006-live"), never a bare workspace directory.
		base := filepath.Base(lower)
		if (strings.HasPrefix(lower, "/tmp/") || strings.Contains(base, "-") || strings.Contains(base, "_")) &&
			!strings.HasSuffix(lower, "/rolling-memory") && !strings.HasSuffix(lower, "/rolling-memory/admin") {
			return true, false
		}
	}

	// 2. Technical symbol / identifier / port / CLI flag (e.g. "triggerProductionRecallLedgerSync", "rolling-admin-srv-9006-live", ":9006", "0.362")
	if strings.HasPrefix(tok, "ep_") || strings.HasPrefix(tok, "nt_") || strings.HasPrefix(tok, "bg_") || strings.HasPrefix(tok, "call_") || strings.HasPrefix(tok, "sess_") || strings.HasPrefix(tok, "msg_") {
		return false, false
	}
	if hasLetter && (hasStructSep || (hasLower && hasInnerUpper) || hasDigit) {
		// Reject single-dash short flags like "-f", "-n", "-p" that lack inner structure (':', '=', or multi-segment '-').
		if strings.HasPrefix(tok, "-") && !strings.HasPrefix(tok, "--") && strings.IndexByte(tok, ':') < 0 && strings.IndexByte(tok, '=') < 0 && strings.Count(tok, "-") == 1 {
			return false, false
		}
		return false, true
	}
	if !hasLetter && hasDigit && (strings.IndexByte(tok, '.') >= 0 || strings.IndexByte(tok, '%') >= 0 || strings.HasPrefix(tok, ":")) {
		// Numeric benchmark metric, version, or port coordinate (e.g. "0.362", "46.12%", ":9006")
		if !(strings.Count(tok, "-") == 2 && len(tok) == 10) && !(strings.Count(tok, ":") == 2 && len(tok) == 8) {
			return false, true
		}
	}
	return false, false
}

func trimTokenDelimiters(raw string) string {
	return strings.TrimFunc(raw, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("()（）[]【】{}<>\"'`，,。；;！!？?、|·", r)
	})
}

func isTokenDelim(r rune) bool {
	return unicode.IsSpace(r) || strings.ContainsRune("()（）[]【】{}<>\"'`，,。；;！!？?、|·\n\r\t", r)
}

type scoredAnchorCandidate struct {
	text   string
	lower  string
	isPath bool
	mass   float64
}

func tailTwoPathSegments(lowerPath string) string {
	clean := strings.Trim(lowerPath, "/")
	idx := strings.LastIndexByte(clean, '/')
	if idx <= 0 {
		return clean
	}
	prev := strings.LastIndexByte(clean[:idx], '/')
	if prev < 0 {
		return clean
	}
	return clean[prev+1:]
}

// extractNovelRecallAnchors extracts structural path coordinates, compound technical symbols,
// and high-information multi-byte phrases from delivered recall text, stripping any echo
// already present in the query cue or cited unit IDs.
func extractNovelRecallAnchors(deliveredText, queryText string, citedUnitIDs []string) (coords []scoredAnchorCandidate, infoPhrases []scoredAnchorCandidate, echoExcluded int) {
	lowerQuery := strings.ToLower(strings.TrimSpace(queryText))
	citedSet := make(map[string]bool, len(citedUnitIDs))
	for _, id := range citedUnitIDs {
		citedSet[strings.ToLower(strings.TrimSpace(id))] = true
	}

	isEcho := func(candLower string, isPath bool) bool {
		if candLower == "" || citedSet[candLower] {
			return true
		}
		if lowerQuery != "" && strings.Contains(lowerQuery, candLower) {
			return true
		}
		if strings.Contains(candLower, "/") {
			base := filepath.Base(candLower)
			if base != "" && base != "." && base != "/" && lowerQuery != "" && strings.Contains(lowerQuery, base) {
				return true
			}
			tail2 := tailTwoPathSegments(candLower)
			if tail2 != "" && lowerQuery != "" && strings.Contains(lowerQuery, tail2) {
				return true
			}
		}
		if colonIdx := strings.LastIndexByte(candLower, ':'); colonIdx >= 0 && colonIdx < len(candLower)-1 {
			suffix := candLower[colonIdx+1:]
			if len(suffix) >= 3 && lowerQuery != "" && strings.Contains(lowerQuery, suffix) {
				return true
			}
		}
		return false
	}

	seen := make(map[string]bool, 64)
	for _, rawTok := range strings.FieldsFunc(deliveredText, isTokenDelim) {
		tok := trimTokenDelimiters(rawTok)
		if tok == "" {
			continue
		}
		// Strip trailing ":line" from file paths while keeping port tokens like ":9006"
		if colonIdx := strings.LastIndexByte(tok, ':'); colonIdx > 2 && strings.Contains(tok[:colonIdx], ".") {
			tok = tok[:colonIdx]
		}
		lower := strings.ToLower(tok)
		if seen[lower] || isEnvelopeOrRoleMetaToken(lower) {
			continue
		}
		isPath, isSym := isStructuralCoordToken(tok)
		if isPath || isSym {
			seen[lower] = true
			if isEcho(lower, isPath) {
				echoExcluded++
				continue
			}
			mass := byteShannonInfoMass(tok)
			coords = append(coords, scoredAnchorCandidate{
				text:   tok,
				lower:  lower,
				isPath: isPath,
				mass:   mass,
			})
			continue
		}

		// Multi-byte / natural-language contiguous span candidate (for non-ASCII or mixed factual phrases)
		if utf8.RuneCountInString(tok) >= 2 {
			seen[lower] = true
			if isEcho(lower, false) {
				echoExcluded++
				continue
			}
			infoPhrases = append(infoPhrases, scoredAnchorCandidate{
				text:  tok,
				lower: lower,
				mass:  byteShannonInfoMass(tok),
			})
		}
	}

	sort.Slice(coords, func(i, j int) bool {
		if coords[i].isPath != coords[j].isPath {
			return coords[i].isPath
		}
		if coords[i].mass != coords[j].mass {
			return coords[i].mass > coords[j].mass
		}
		return coords[i].lower < coords[j].lower
	})
	sort.Slice(infoPhrases, func(i, j int) bool {
		if infoPhrases[i].mass != infoPhrases[j].mass {
			return infoPhrases[i].mass > infoPhrases[j].mass
		}
		return infoPhrases[i].lower < infoPhrases[j].lower
	})
	if len(coords) > 48 {
		coords = coords[:48]
	}
	if len(infoPhrases) > 32 {
		infoPhrases = infoPhrases[:32]
	}
	return coords, infoPhrases, echoExcluded
}

func stripReviewFooterFromReply(text string) string {
	if idx := strings.Index(text, "[Rolling-Memory 复盘]"); idx >= 0 {
		return text[:idx]
	}
	return text
}

func matchCandidateInCorpus(c scoredAnchorCandidate, lowerCorpus string) bool {
	if lowerCorpus == "" || c.lower == "" {
		return false
	}
	if strings.Contains(lowerCorpus, c.lower) {
		return true
	}
	if c.isPath && strings.Contains(c.lower, "/") {
		tail2 := tailTwoPathSegments(c.lower)
		if strings.Contains(tail2, "/") && strings.Contains(filepath.Base(tail2), ".") && strings.Contains(lowerCorpus, tail2) {
			return true
		}
	}
	return false
}

func anchorDedupKey(c scoredAnchorCandidate) string {
	if c.isPath {
		base := strings.TrimPrefix(filepath.Base(c.lower), ".")
		if base != "index.vue" && base != "index.ts" && base != "main.go" && base != "routes.go" {
			return base
		}
		if strings.Contains(c.lower, "/") {
			return tailTwoPathSegments(c.lower)
		}
	}
	return strings.TrimPrefix(c.lower, ".")
}

// evaluateSingleRecallAdoption performs deterministic intersection auditing between
// the novel anchors of a recall delivery and the agent's subsequent tool inputs and reply text.
func evaluateSingleRecallAdoption(deliveredText, queryText string, citedUnitIDs []string, toolCorpus, replyCorpus string) (outcome string, matched []string, echoExcluded int) {
	cleanReply := strings.TrimSpace(stripReviewFooterFromReply(replyCorpus))
	cleanTool := strings.TrimSpace(toolCorpus)
	coords, infoPhrases, echoCnt := extractNovelRecallAnchors(deliveredText, queryText, citedUnitIDs)
	if cleanTool == "" && cleanReply == "" {
		return "pending", []string{}, echoCnt
	}

	lowerTool := strings.ToLower(cleanTool)
	lowerReply := strings.ToLower(cleanReply)
	lowerCombined := lowerTool + "\n" + lowerReply

	var toolHits []string
	var contextHits []string
	seenHit := make(map[string]bool, 8)

	for _, c := range coords {
		dKey := anchorDedupKey(c)
		if lowerTool != "" && matchCandidateInCorpus(c, lowerTool) {
			if !seenHit[dKey] {
				seenHit[dKey] = true
				toolHits = append(toolHits, c.text)
			}
			continue
		}
		if lowerReply != "" && matchCandidateInCorpus(c, lowerReply) {
			if !seenHit[dKey] {
				seenHit[dKey] = true
				contextHits = append(contextHits, c.text)
			}
		}
	}

	for _, phrase := range infoPhrases {
		if len(contextHits) >= 6 {
			break
		}
		if matchCandidateInCorpus(phrase, lowerCombined) && !seenHit[phrase.lower] {
			seenHit[phrase.lower] = true
			contextHits = append(contextHits, phrase.text)
		}
	}

	if len(toolHits) > 0 {
		out := append([]string{}, toolHits...)
		for _, ch := range contextHits {
			if len(out) >= 6 {
				break
			}
			out = append(out, ch)
		}
		if len(out) > 6 {
			out = out[:6]
		}
		return "adopted_tool", out, echoCnt
	}
	if len(contextHits) > 0 {
		if len(contextHits) > 6 {
			contextHits = contextHits[:6]
		}
		return "adopted_context", contextHits, echoCnt
	}
	return "unadopted", []string{}, echoCnt
}

type settledNoeticAdoptionEntry struct {
	outcome     string
	matched     []string
	echoCnt     int
	assistantID string
}

var (
	settledNoeticAdoptionMu    sync.Mutex
	settledNoeticAdoptionCache = map[string]settledNoeticAdoptionEntry{}
	zcodeCallPairCacheMu       sync.Mutex
	zcodeCallPairCache         = map[string]followupCorpusPair{}
	zcodeRecallCallsMu         sync.Mutex
	zcodeRecallCallsCache      []zcodeRecallCallWindow
	zcodeRecallCallsWatermark  int64
	zcodeRecallCallsMinFloorMS int64
	noeticWarmRunningMu        sync.Mutex
	noeticWarmRunning          bool
)

func loadCachedZCodeRecallCalls(ctx context.Context, zdb *sql.DB, minSec int64) []zcodeRecallCallWindow {
	zcodeRecallCallsMu.Lock()
	defer zcodeRecallCallsMu.Unlock()

	floorMS := (minSec - 30) * 1000
	if floorMS <= 0 {
		floorMS = time.Now().Add(-14 * 24 * time.Hour).UnixMilli()
	}
	queryFromMS := zcodeRecallCallsWatermark
	expandedBackwards := false
	if queryFromMS <= 0 || zcodeRecallCallsMinFloorMS <= 0 || floorMS < zcodeRecallCallsMinFloorMS {
		queryFromMS = floorMS
		expandedBackwards = zcodeRecallCallsMinFloorMS > 0 && floorMS < zcodeRecallCallsMinFloorMS
		zcodeRecallCallsMinFloorMS = floorMS
	}

	qCtx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
	defer cancel()
	tRows, err := zdb.QueryContext(qCtx, `
		SELECT session_id, tool_call_id, coalesce(started_at, 0), coalesce(completed_at, 0)
		FROM tool_usage
		WHERE started_at >= ?
		  AND tool_name IN ('mcp__plugin_rolling-memory_rolling-memory__recall', 'mcp__rolling-memory__recall', 'rolling-memory:rolling-memory__recall')
		ORDER BY started_at ASC
	`, queryFromMS)
	if err == nil {
		seenCall := make(map[string]bool, len(zcodeRecallCallsCache)+32)
		for _, existing := range zcodeRecallCallsCache {
			seenCall[existing.callID] = true
		}
		maxStart := zcodeRecallCallsWatermark
		added := 0
		for tRows.Next() {
			var c zcodeRecallCallWindow
			if tRows.Scan(&c.sessionID, &c.callID, &c.startMS, &c.endMS) == nil && c.sessionID != "" && c.callID != "" {
				if c.endMS <= 0 {
					c.endMS = c.startMS
				}
				if c.startMS > maxStart {
					maxStart = c.startMS
				}
				if !seenCall[c.callID] {
					seenCall[c.callID] = true
					zcodeRecallCallsCache = append(zcodeRecallCallsCache, c)
					added++
				}
			}
		}
		tRows.Close()
		if maxStart > zcodeRecallCallsWatermark {
			zcodeRecallCallsWatermark = maxStart
		}
		if added > 0 {
			if expandedBackwards {
				sort.Slice(zcodeRecallCallsCache, func(i, j int) bool {
					return zcodeRecallCallsCache[i].startMS < zcodeRecallCallsCache[j].startMS
				})
			}
			const maxCachedRecallCalls = 16384
			if len(zcodeRecallCallsCache) > maxCachedRecallCalls {
				zcodeRecallCallsCache = zcodeRecallCallsCache[len(zcodeRecallCallsCache)-maxCachedRecallCalls:]
				if len(zcodeRecallCallsCache) > 0 {
					zcodeRecallCallsMinFloorMS = zcodeRecallCallsCache[0].startMS
				}
			}
			nextBySession := make(map[string]int64, 64)
			for i := len(zcodeRecallCallsCache) - 1; i >= 0; i-- {
				sid := zcodeRecallCallsCache[i].sessionID
				zcodeRecallCallsCache[i].nextMS = zcodeRecallCallsCache[i].endMS + 240*1000
				if nxt, ok := nextBySession[sid]; ok && nxt > zcodeRecallCallsCache[i].startMS && nxt < zcodeRecallCallsCache[i].nextMS {
					zcodeRecallCallsCache[i].nextMS = nxt
				}
				nextBySession[sid] = zcodeRecallCallsCache[i].startMS
			}
		}
	}
	out := make([]zcodeRecallCallWindow, len(zcodeRecallCallsCache))
	copy(out, zcodeRecallCallsCache)
	return out
}

// loadZCodeFollowupCorpora queries the ZCode host SQLite DB using covering indexes
// (tool_usage_started_tool_idx + message_session_time_created_id_idx + part_message_sequence_idx)
// for the most recent L3 Noetic events when inspecting the default user production vault.
func loadZCodeFollowupCorpora(ctx context.Context, home, memoryPath string, events []roiNoeticEvent) (map[string]followupCorpusPair, map[string]bool) {
	out := make(map[string]followupCorpusPair)
	completed := make(map[string]bool, len(events))
	if strings.TrimSpace(home) == "" || len(events) == 0 {
		for _, ev := range events {
			completed[ev.NoeticID] = true
		}
		return out, completed
	}
	defaultPath := filepath.Join(home, ".local", "share", "rolling-memory", "memory.db")
	if filepath.Clean(memoryPath) != filepath.Clean(defaultPath) {
		for _, ev := range events {
			completed[ev.NoeticID] = true
		}
		return out, completed
	}
	zdb, err := memoryRODB(filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"))
	if err != nil || zdb == nil {
		for _, ev := range events {
			completed[ev.NoeticID] = true
		}
		return out, completed
	}

	limit := len(events)
	if limit > 100 {
		limit = 100
	}
	subset := events[:limit]
	minSec := subset[len(subset)-1].OccurredAt
	for _, ev := range subset {
		if ev.OccurredAt > 0 && (minSec == 0 || ev.OccurredAt < minSec) {
			minSec = ev.OccurredAt
		}
	}
	if minSec <= 0 {
		return out, completed
	}

	calls := loadCachedZCodeRecallCalls(context.Background(), zdb, minSec)
	if len(calls) == 0 {
		for _, ev := range subset {
			completed[ev.NoeticID] = true
		}
		return out, completed
	}

	qCtx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()

	// Match each Noetic event (in deterministic newest-first order) to its corresponding ZCode recall tool call within ±15s.
	type matchedPair struct {
		noeticID string
		callIdx  int
	}
	usedCall := make(map[int]bool, len(subset))
	var orderedMatches []matchedPair
	for _, ev := range subset {
		evMS := ev.OccurredAt * 1000
		bestIdx := -1
		bestDiff := int64(15000)
		for idx, c := range calls {
			if usedCall[idx] {
				continue
			}
			diff := c.endMS - evMS
			if diff < 0 {
				diff = -diff
			}
			if diff <= bestDiff {
				bestDiff = diff
				bestIdx = idx
			}
		}
		if bestIdx >= 0 {
			usedCall[bestIdx] = true
			orderedMatches = append(orderedMatches, matchedPair{noeticID: ev.NoeticID, callIdx: bestIdx})
		} else {
			completed[ev.NoeticID] = true
		}
	}

	// For each matched call in deterministic newest-first order, query the covering index on
	// message(session_id, time_created, id) and collect subsequent tool inputs and assistant replies.
	nowMS := time.Now().UnixMilli()
	for _, mp := range orderedMatches {
		noeticID := mp.noeticID
		c := calls[mp.callIdx]
		if c.callID != "" {
			zcodeCallPairCacheMu.Lock()
			cachedPair, hasCachedPair := zcodeCallPairCache[c.callID]
			zcodeCallPairCacheMu.Unlock()
			if hasCachedPair {
				completed[noeticID] = true
				out[noeticID] = cachedPair
				continue
			}
		}
		if qCtx.Err() != nil {
			break
		}
		seenMID := make(map[string]bool, 16)
		var mids []any
		var marks []string
		addMID := func(mid string) {
			if mid != "" && !seenMID[mid] {
				seenMID[mid] = true
				mids = append(mids, mid)
				marks = append(marks, "?")
			}
		}

		// Step A: Always include the 4 most recent messages at or before c.startMS in this session
		// (using covering index message_session_time_created_id_idx), because an assistant turn
		// message is always created at or before its tool part's c.startMS!
		if preRows, preErr := zdb.QueryContext(qCtx, `
			SELECT m.id
			FROM message m
			WHERE m.session_id = ? AND m.time_created <= ?
			ORDER BY m.time_created DESC
			LIMIT 4
		`, c.sessionID, c.startMS); preErr == nil {
			var preMIDs []string
			for preRows.Next() {
				var mid string
				if preRows.Scan(&mid) == nil && mid != "" {
					preMIDs = append(preMIDs, mid)
				}
			}
			preRows.Close()
			for i := len(preMIDs) - 1; i >= 0; i-- {
				addMID(preMIDs[i])
			}
		}

		// Step B: Include up to 14 subsequent messages in (c.startMS, c.nextMS].
		if postRows, postErr := zdb.QueryContext(qCtx, `
			SELECT m.id
			FROM message m
			WHERE m.session_id = ? AND m.time_created > ? AND m.time_created <= ?
			ORDER BY m.time_created ASC
			LIMIT 14
		`, c.sessionID, c.startMS, c.nextMS); postErr == nil {
			for postRows.Next() {
				var mid string
				if postRows.Scan(&mid) == nil {
					addMID(mid)
				}
			}
			postRows.Close()
		}
		if len(mids) == 0 {
			if qCtx.Err() == nil {
				completed[noeticID] = true
			}
			continue
		}

		midsWithCall := make([]any, 0, len(mids)+1)
		midsWithCall = append(midsWithCall, c.callID)
		midsWithCall = append(midsWithCall, mids...)

		// Query without cross-message ORDER BY so SQLite scans part_message_sequence_idx directly
		// with zero TEMP B-TREE sorting; we sort the small result slice in Go memory (<0.01ms).
		pRows, pErr := zdb.QueryContext(qCtx, `
			SELECT coalesce(p.time_created, 0),
			       coalesce(p.sequence, 0),
			       coalesce(json_extract(p.data, '$.type'), ''),
			       coalesce(json_extract(p.data, '$.callID'), ''),
			       coalesce(json_extract(p.data, '$.tool'), ''),
			       coalesce(substr(json_extract(p.data, '$.state.input'), 1, 4096), ''),
			       CASE WHEN json_extract(p.data, '$.callID') = ? THEN coalesce(substr(json_extract(p.data, '$.state.output'), 1, 16384), '') ELSE '' END,
			       coalesce(substr(json_extract(p.data, '$.text'), 1, 8192), '')
			FROM part p
			WHERE p.message_id IN (`+strings.Join(marks, ",")+`)
			  AND (
			      instr(substr(p.data, 1, 140), '"tool"') > 0
			      OR (
			          (instr(substr(p.data, 1, 64), '"text"') > 0 OR instr(substr(p.data, 1, 64), '"reasoning"') > 0)
			          AND COALESCE(json_extract(p.data, '$.synthetic'), 0) = 0
			      )
			  )
		`, midsWithCall...)
		if pErr != nil {
			continue
		}

		type rawPartItem struct {
			pTime       int64
			pSeq        int64
			pType       string
			callID      string
			toolName    string
			stateInput  string
			stateOutput string
			textBody    string
		}
		var partItems []rawPartItem
		for pRows.Next() {
			var it rawPartItem
			if pRows.Scan(&it.pTime, &it.pSeq, &it.pType, &it.callID, &it.toolName, &it.stateInput, &it.stateOutput, &it.textBody) == nil {
				partItems = append(partItems, it)
			}
		}
		pRows.Close()
		sort.Slice(partItems, func(i, j int) bool {
			if partItems[i].pTime != partItems[j].pTime {
				return partItems[i].pTime < partItems[j].pTime
			}
			return partItems[i].pSeq < partItems[j].pSeq
		})

		var toolBuf, replyBuf strings.Builder
		var deliveredOut, cueText string
		seenRecallPart := false
		for _, it := range partItems {
			pTime, pType, callID, toolName, stateInput, stateOutput, textBody := it.pTime, it.pType, it.callID, it.toolName, it.stateInput, it.stateOutput, it.textBody
			if pType == "tool" {
				if callID == c.callID || strings.Contains(toolName, "recall") {
					if callID == c.callID {
						seenRecallPart = true
						if stateOutput != "" {
							deliveredOut = stateOutput
						}
						if stateInput != "" {
							var inMap map[string]any
							if json.Unmarshal([]byte(stateInput), &inMap) == nil {
								if cueVal, ok := inMap["cue"].(string); ok && cueVal != "" {
									cueText = cueVal
								}
							}
						}
					} else if seenRecallPart && pTime > c.endMS {
						// Stop at the next recall invocation in the same session.
						break
					}
					continue
				}
				if !seenRecallPart && pTime < c.startMS {
					continue
				}
				if pTime > c.nextMS {
					continue
				}
				if strings.TrimSpace(stateInput) != "" {
					toolBuf.WriteString(stateInput)
					toolBuf.WriteByte('\n')
				}
			} else if pType == "text" || pType == "reasoning" {
				if !seenRecallPart && pTime < c.endMS {
					continue
				}
				if pTime > c.nextMS {
					continue
				}
				if strings.TrimSpace(textBody) != "" {
					replyBuf.WriteString(textBody)
					replyBuf.WriteByte('\n')
				}
			}
		}
		pair := followupCorpusPair{
			harnessID:   "zcode",
			toolCorpus:  toolBuf.String(),
			replyCorpus: replyBuf.String(),
			delivered:   deliveredOut,
			cue:         cueText,
		}
		if qCtx.Err() == nil {
			completed[noeticID] = true
			if c.callID != "" && c.endMS > 0 && nowMS-c.endMS > 60000 {
				zcodeCallPairCacheMu.Lock()
				if len(zcodeCallPairCache) > 8192 {
					zcodeCallPairCache = map[string]followupCorpusPair{}
				}
				zcodeCallPairCache[c.callID] = pair
				zcodeCallPairCacheMu.Unlock()
			}
		}
		if toolBuf.Len() > 0 || replyBuf.Len() > 0 || deliveredOut != "" {
			out[noeticID] = pair
		}
	}
	return out, completed
}

type vaultDocFollowupRow struct {
	docID      string
	occurredAt int64
	locus      string
	text       string
	isTool     bool
	jsonlPath  string
}

type jsonlRecallWindow struct {
	harnessID   string
	sessionKey  string
	callID      string
	timestampMS int64
	cue         string
	delivered   string
	toolCorpus  string
	replyCorpus string
	outputBytes int64
	isError     bool
}

type jsonlParseCacheEntry struct {
	size    int64
	mtimeNS int64
	windows []jsonlRecallWindow
}

var (
	jsonlRecallCacheMu sync.Mutex
	jsonlRecallCache   = map[string]jsonlParseCacheEntry{}
)

func inferHarnessIDFromJSONLPath(path string) string {
	lower := strings.ToLower(path)
	switch {
	case strings.Contains(lower, ".workbuddy") || strings.Contains(lower, "workbuddy"):
		return "workbuddy"
	case strings.Contains(lower, ".codebuddy") || strings.Contains(lower, "codebuddy"):
		return "codebuddy"
	case strings.Contains(lower, ".claude") || strings.Contains(lower, "claude"):
		return "claude-code"
	case strings.Contains(lower, ".codex") || strings.Contains(lower, "codex"):
		return "codex"
	case strings.Contains(lower, ".dsh") || strings.Contains(lower, "/dsh/"):
		return "dsh"
	case strings.Contains(lower, ".cursor") || strings.Contains(lower, "cursor"):
		return "cursor"
	case strings.Contains(lower, "windsurf"):
		return "windsurf"
	case strings.Contains(lower, "cline"):
		return "cline"
	case strings.Contains(lower, ".zcode"):
		return "zcode"
	default:
		return "claude-code"
	}
}

func parseJSONLTimestampMS(obj map[string]any, fallbackMS int64) int64 {
	for _, key := range []string{"timestamp", "created_at", "time_created", "ts"} {
		v, ok := obj[key]
		if !ok || v == nil {
			continue
		}
		switch tv := v.(type) {
		case float64:
			n := int64(tv)
			if n > 1_000_000_000_000 {
				return n
			}
			if n > 1_000_000_000 {
				return n * 1000
			}
		case string:
			s := strings.TrimSpace(tv)
			if s == "" {
				continue
			}
			if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
				return t.UnixMilli()
			}
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				return t.UnixMilli()
			}
			if parsed, err := strconv.ParseInt(s, 10, 64); err == nil {
				if parsed > 1_000_000_000_000 {
					return parsed
				}
				if parsed > 1_000_000_000 {
					return parsed * 1000
				}
			}
		}
	}
	return fallbackMS
}

func isRecallToolName(name, namespace, server string) bool {
	ln := strings.ToLower(strings.TrimSpace(name))
	lns := strings.ToLower(strings.TrimSpace(namespace))
	lsrv := strings.ToLower(strings.TrimSpace(server))
	if strings.Contains(ln, "rolling-memory") || strings.Contains(ln, "rolling_memory") {
		return strings.Contains(ln, "recall")
	}
	if ln == "recall" && (strings.Contains(lns, "rolling") || strings.Contains(lsrv, "rolling") || (lns == "" && lsrv == "")) {
		return true
	}
	return ln == "mcp__rolling-memory__recall" || ln == "mcp__plugin_rolling-memory_rolling-memory__recall"
}

func extractCueFromArguments(raw any) string {
	switch v := raw.(type) {
	case map[string]any:
		if c, ok := v["cue"].(string); ok && strings.TrimSpace(c) != "" {
			return strings.TrimSpace(c)
		}
		if inp, ok := v["input"].(map[string]any); ok {
			if c, ok := inp["cue"].(string); ok && strings.TrimSpace(c) != "" {
				return strings.TrimSpace(c)
			}
		}
	case string:
		s := strings.TrimSpace(v)
		if strings.HasPrefix(s, "{") {
			var m map[string]any
			if json.Unmarshal([]byte(s), &m) == nil {
				return extractCueFromArguments(m)
			}
		}
	}
	return ""
}

func stringifyToolArguments(raw any) string {
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case map[string]any:
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
	}
	return ""
}

func extractTextFromContentBlocks(raw any) string {
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case []any:
		var parts []string
		for _, elem := range v {
			switch item := elem.(type) {
			case string:
				if strings.TrimSpace(item) != "" {
					parts = append(parts, strings.TrimSpace(item))
				}
			case map[string]any:
				for _, k := range []string{"text", "content", "output"} {
					if s, ok := item[k].(string); ok && strings.TrimSpace(s) != "" {
						parts = append(parts, strings.TrimSpace(s))
					}
				}
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		if c, ok := v["content"]; ok {
			if s := extractTextFromContentBlocks(c); s != "" {
				return s
			}
		}
		if s, ok := v["stdout"].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
		if s, ok := v["text"].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// parseJSONLRecallWindows parses a single harness JSONL session transcript and extracts all
// recall tool invocations along with their delivered output and subsequent tool/reply corpora:
//   - Family A (Claude Code / WorkBuddy / CodeBuddy / Cursor / Windsurf / Cline):
//     assistant `tool_use` + user `tool_result` + assistant `text`/`thinking`
//   - Family B (Codex / DSH):
//     `event_msg` (`McpToolCall`) + `response_item` (`function_call` / `function_call_output` / assistant `message`)
func parseJSONLRecallWindows(path string) []jsonlRecallWindow {
	cleanPath := filepath.Clean(strings.TrimSpace(path))
	if cleanPath == "" {
		return nil
	}
	info, err := os.Stat(cleanPath)
	if err != nil || info.IsDir() {
		return nil
	}
	mtimeNS := info.ModTime().UnixNano()
	fSize := info.Size()

	jsonlRecallCacheMu.Lock()
	if cached, ok := jsonlRecallCache[cleanPath]; ok && cached.size == fSize && cached.mtimeNS == mtimeNS {
		out := make([]jsonlRecallWindow, len(cached.windows))
		copy(out, cached.windows)
		jsonlRecallCacheMu.Unlock()
		return out
	}
	jsonlRecallCacheMu.Unlock()

	f, err := os.Open(cleanPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	harnessID := inferHarnessIDFromJSONLPath(cleanPath)
	sessionKey := strings.TrimSuffix(filepath.Base(cleanPath), ".jsonl")
	defaultTS := info.ModTime().UnixMilli()

	type pendingWindow struct {
		win      jsonlRecallWindow
		toolBuf  strings.Builder
		replyBuf strings.Builder
	}
	var windows []*pendingWindow
	callIdxByID := make(map[string]int, 8)
	activeIdx := -1

	ensureRecallCall := func(callID, cue, delivered string, tsMS int64, isErr bool) int {
		cid := strings.TrimSpace(callID)
		if cid != "" {
			if idx, exists := callIdxByID[cid]; exists {
				pw := windows[idx]
				if pw.win.cue == "" && cue != "" {
					pw.win.cue = cue
				}
				if pw.win.delivered == "" && delivered != "" {
					pw.win.delivered = delivered
					pw.win.outputBytes = int64(len(delivered))
				}
				if tsMS > 0 && pw.win.timestampMS == defaultTS {
					pw.win.timestampMS = tsMS
				}
				if isErr {
					pw.win.isError = true
				}
				activeIdx = idx
				return idx
			}
		} else {
			cid = "line_" + strconv.Itoa(len(windows)+1)
		}
		pw := &pendingWindow{
			win: jsonlRecallWindow{
				harnessID:   harnessID,
				sessionKey:  sessionKey,
				callID:      cid,
				timestampMS: tsMS,
				cue:         cue,
				delivered:   delivered,
				outputBytes: int64(len(delivered)),
				isError:     isErr,
			},
		}
		idx := len(windows)
		windows = append(windows, pw)
		callIdxByID[cid] = idx
		activeIdx = idx
		return idx
	}

	scanLinesResilient(f, 4*1024*1024, func(rawLine string) {
		line := strings.TrimSpace(rawLine)
		if line == "" || line[0] != '{' {
			return
		}
		var obj map[string]any
		if json.Unmarshal([]byte(line), &obj) != nil || obj == nil {
			return
		}
		tsMS := parseJSONLTimestampMS(obj, defaultTS)
		topType, _ := obj["type"].(string)

		// 1. Codex / DSH `event_msg` -> `item_completed` -> `McpToolCall`
		if topType == "event_msg" {
			payload, _ := obj["payload"].(map[string]any)
			if payload != nil {
				item, _ := payload["item"].(map[string]any)
				if item != nil {
					itemType, _ := item["type"].(string)
					toolName, _ := item["tool"].(string)
					serverName, _ := item["server"].(string)
					if itemType == "McpToolCall" && isRecallToolName(toolName, "", serverName) {
						callID, _ := item["id"].(string)
						cue := extractCueFromArguments(item["arguments"])
						var deliv string
						if res, ok := item["result"].(map[string]any); ok {
							deliv = extractTextFromContentBlocks(res["content"])
						}
						status, _ := item["status"].(string)
						ensureRecallCall(callID, cue, deliv, tsMS, status == "failed" || status == "error")
					} else if itemType == "McpToolCall" && activeIdx >= 0 {
						argStr := stringifyToolArguments(item["arguments"])
						if argStr != "" {
							windows[activeIdx].toolBuf.WriteString(argStr)
							windows[activeIdx].toolBuf.WriteByte('\n')
						}
					}
				}
			}
			return
		}

		// 2. Codex / DSH `response_item` (`function_call` / `function_call_output` / `message`)
		if topType == "response_item" {
			payload, _ := obj["payload"].(map[string]any)
			if payload != nil {
				pType, _ := payload["type"].(string)
				switch pType {
				case "function_call":
					name, _ := payload["name"].(string)
					ns, _ := payload["namespace"].(string)
					callID, _ := payload["call_id"].(string)
					if callID == "" {
						callID, _ = payload["id"].(string)
					}
					if isRecallToolName(name, ns, "") {
						cue := extractCueFromArguments(payload["arguments"])
						ensureRecallCall(callID, cue, "", tsMS, false)
					} else if activeIdx >= 0 {
						argStr := stringifyToolArguments(payload["arguments"])
						if argStr != "" {
							windows[activeIdx].toolBuf.WriteString(argStr)
							windows[activeIdx].toolBuf.WriteByte('\n')
						}
					}
					return
				case "function_call_output":
					callID, _ := payload["call_id"].(string)
					outTxt := extractTextFromContentBlocks(payload["output"])
					if idx, ok := callIdxByID[strings.TrimSpace(callID)]; ok {
						if outTxt != "" {
							windows[idx].win.delivered = outTxt
							windows[idx].win.outputBytes = int64(len(outTxt))
						}
					} else if strings.Contains(outTxt, "<rolling-memory-recall") && activeIdx >= 0 && windows[activeIdx].win.delivered == "" {
						windows[activeIdx].win.delivered = outTxt
						windows[activeIdx].win.outputBytes = int64(len(outTxt))
					}
					return
				case "message":
					role, _ := payload["role"].(string)
					if strings.EqualFold(role, "assistant") && activeIdx >= 0 {
						txt := extractTextFromContentBlocks(payload["content"])
						if txt != "" {
							windows[activeIdx].replyBuf.WriteString(txt)
							windows[activeIdx].replyBuf.WriteByte('\n')
						}
					}
					return
				}
			}
		}

		// 3. Claude Code / WorkBuddy / CodeBuddy / Cursor / Windsurf / Cline message format
		msg, _ := obj["message"].(map[string]any)
		if msg == nil {
			msg = obj
		}
		role, _ := msg["role"].(string)
		if role == "" {
			role = topType
		}
		role = strings.ToLower(strings.TrimSpace(role))

		switch content := msg["content"].(type) {
		case string:
			if (role == "assistant" || role == "ai" || role == "agent") && activeIdx >= 0 && strings.TrimSpace(content) != "" {
				windows[activeIdx].replyBuf.WriteString(strings.TrimSpace(content))
				windows[activeIdx].replyBuf.WriteByte('\n')
			}
		case []any:
			for _, rawBlock := range content {
				block, ok := rawBlock.(map[string]any)
				if !ok || block == nil {
					continue
				}
				bType, _ := block["type"].(string)
				switch bType {
				case "tool_use":
					tName, _ := block["name"].(string)
					callID, _ := block["id"].(string)
					// Support DeferredToolCall wrapping mcp__rolling-memory__recall in JSONL harnesses too
					inpMap, _ := block["input"].(map[string]any)
					wrappedName := ""
					var wrappedInput any = inpMap
					if inpMap != nil {
						if wn, ok := inpMap["name"].(string); ok && wn != "" {
							wrappedName = wn
							if wi, ok := inpMap["input"]; ok {
								wrappedInput = wi
							}
						}
					}
					if isRecallToolName(tName, "", "") || isRecallToolName(wrappedName, "", "") {
						cue := extractCueFromArguments(wrappedInput)
						ensureRecallCall(callID, cue, "", tsMS, false)
					} else if activeIdx >= 0 {
						argStr := stringifyToolArguments(wrappedInput)
						if argStr != "" {
							windows[activeIdx].toolBuf.WriteString(argStr)
							windows[activeIdx].toolBuf.WriteByte('\n')
						}
					}
				case "tool_result":
					tuID, _ := block["tool_use_id"].(string)
					if tuID == "" {
						tuID, _ = block["id"].(string)
					}
					outTxt := extractTextFromContentBlocks(block["content"])
					if outTxt == "" {
						outTxt = extractTextFromContentBlocks(obj["toolUseResult"])
					}
					isErr, _ := block["is_error"].(bool)
					if idx, ok := callIdxByID[strings.TrimSpace(tuID)]; ok {
						if outTxt != "" {
							windows[idx].win.delivered = outTxt
							windows[idx].win.outputBytes = int64(len(outTxt))
						}
						if isErr {
							windows[idx].win.isError = true
						}
					} else if strings.Contains(outTxt, "<rolling-memory-recall") {
						if activeIdx >= 0 && windows[activeIdx].win.delivered == "" {
							windows[activeIdx].win.delivered = outTxt
							windows[activeIdx].win.outputBytes = int64(len(outTxt))
						} else {
							ensureRecallCall(tuID, "", outTxt, tsMS, isErr)
						}
					}
				case "text", "output_text", "thinking", "reasoning":
					if (role == "assistant" || role == "ai" || role == "agent") && activeIdx >= 0 {
						txt, _ := block["text"].(string)
						if txt == "" {
							txt, _ = block["thinking"].(string)
						}
						if strings.TrimSpace(txt) != "" {
							windows[activeIdx].replyBuf.WriteString(strings.TrimSpace(txt))
							windows[activeIdx].replyBuf.WriteByte('\n')
						}
					}
				}
			}
		}
	})

	result := make([]jsonlRecallWindow, 0, len(windows))
	for _, pw := range windows {
		pw.win.toolCorpus = pw.toolBuf.String()
		pw.win.replyCorpus = pw.replyBuf.String()
		result = append(result, pw.win)
	}

	jsonlRecallCacheMu.Lock()
	if len(jsonlRecallCache) > 512 {
		jsonlRecallCache = map[string]jsonlParseCacheEntry{}
	}
	jsonlRecallCache[cleanPath] = jsonlParseCacheEntry{
		size:    fSize,
		mtimeNS: mtimeNS,
		windows: result,
	}
	jsonlRecallCacheMu.Unlock()
	return result
}

// scanLinesResilient reads lines up to maxLineBytes without aborting the entire file
// when a single oversized line (e.g. a base64 image in a harness JSONL transcript)
// exceeds the cap: oversized lines are drained and skipped so all subsequent turns
// in the transcript are still audited.
func scanLinesResilient(r io.Reader, maxLineBytes int, visit func(line string)) {
	br := bufio.NewReaderSize(r, 64*1024)
	var accum []byte
	oversized := false
	for {
		frag, err := br.ReadSlice('\n')
		if err == bufio.ErrBufferFull {
			if !oversized {
				if len(accum)+len(frag) <= maxLineBytes {
					accum = append(accum, frag...)
				} else {
					accum = accum[:0]
					oversized = true
				}
			}
			continue
		}
		if oversized {
			oversized = false
			accum = accum[:0]
			if err != nil {
				return
			}
			continue
		}
		var line string
		if len(accum) > 0 {
			if len(accum)+len(frag) <= maxLineBytes {
				accum = append(accum, frag...)
				line = string(accum)
			}
			accum = accum[:0]
		} else if len(frag) <= maxLineBytes {
			line = string(frag)
		}
		if line != "" {
			visit(line)
		}
		if err != nil {
			return
		}
	}
}

func discoverMultiHarnessJSONLFiles(home string, minUnixSec int64, extraPaths []string) []string {
	seen := make(map[string]bool, 64)
	var out []string
	addPath := func(p string, checkMtime bool) {
		clean := filepath.Clean(strings.TrimSpace(p))
		if clean == "" || seen[clean] || !strings.HasSuffix(strings.ToLower(clean), ".jsonl") {
			return
		}
		info, err := os.Stat(clean)
		if err != nil || info.IsDir() {
			return
		}
		if checkMtime && minUnixSec > 0 && info.ModTime().Unix() < minUnixSec-7200 {
			return
		}
		seen[clean] = true
		out = append(out, clean)
	}

	for _, ep := range extraPaths {
		addPath(ep, false)
	}
	if strings.TrimSpace(home) == "" {
		return out
	}

	patterns := []string{
		filepath.Join(home, ".claude", "projects", "*", "*.jsonl"),
		filepath.Join(home, ".claude", "projects", "*", "sessions", "*.jsonl"),
		filepath.Join(home, ".workbuddy", "projects", "*", "*.jsonl"),
		filepath.Join(home, ".workbuddy", "projects", "*", "sessions", "*.jsonl"),
		filepath.Join(home, ".codebuddy", "projects", "*", "*.jsonl"),
		filepath.Join(home, ".codebuddy", "projects", "*", "sessions", "*.jsonl"),
		filepath.Join(home, ".codex", "sessions", "*", "*", "*", "*.jsonl"),
		filepath.Join(home, ".codex", "sessions", "*.jsonl"),
		filepath.Join(home, ".codex", "archived_sessions", "*.jsonl"),
		filepath.Join(home, ".dsh", "sessions", "*.jsonl"),
		filepath.Join(home, ".cursor", "projects", "*", "*.jsonl"),
		filepath.Join(home, ".cursor", "sessions", "*.jsonl"),
		filepath.Join(home, ".windsurf", "sessions", "*.jsonl"),
		filepath.Join(home, ".cline", "sessions", "*.jsonl"),
		filepath.Join(home, ".zcode", "projects", "*", "sessions", "*.jsonl"),
	}
	for _, pat := range patterns {
		matches, err := filepath.Glob(pat)
		if err != nil {
			continue
		}
		for _, m := range matches {
			addPath(m, true)
		}
	}

	// Also discover Codex rollout paths registered in ~/.codex/state_*.sqlite (`threads.rollout_path`).
	if codexDBs, err := filepath.Glob(filepath.Join(home, ".codex", "state_*.sqlite")); err == nil {
		for _, cdbPath := range codexDBs {
			cdb := openVaultReadOnly(cdbPath)
			if cdb == nil {
				continue
			}
			rows, qErr := cdb.Query(`SELECT coalesce(rollout_path, '') FROM threads ORDER BY updated_at DESC LIMIT 40`)
			if qErr == nil {
				for rows.Next() {
					var rPath string
					if rows.Scan(&rPath) == nil && rPath != "" {
						addPath(rPath, true)
					}
				}
				rows.Close()
			}
			cdb.Close()
		}
	}
	return out
}

func extractJSONLPathFromPointer(raw string) string {
	const prefix = "@ref:jsonl:"
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(trimmed, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(trimmed, prefix)
	if lastColon := strings.LastIndex(rest, ":"); lastColon > 0 {
		return strings.TrimSpace(rest[:lastColon])
	}
	return ""
}

// loadMultiHarnessJSONLFollowupCorpora scans JSONL session transcripts across Claude Code,
// Codex, WorkBuddy, CodeBuddy, DSH, Cursor/Windsurf/Cline, and any @ref:jsonl: pointers referenced
// by the vault, matching each L3 Noetic event by exact cue or timestamp proximity.
func loadMultiHarnessJSONLFollowupCorpora(home, memoryPath string, events []roiNoeticEvent, extraJSONLPaths []string) map[string]followupCorpusPair {
	out := make(map[string]followupCorpusPair)
	if len(events) == 0 {
		return out
	}
	for _, ev := range events {
		for _, cu := range ev.CitedUnits {
			if p := extractJSONLPathFromPointer(cu.StoredText); p != "" {
				extraJSONLPaths = append(extraJSONLPaths, p)
			}
		}
	}

	minSec := events[len(events)-1].OccurredAt
	for _, ev := range events {
		if ev.OccurredAt > 0 && (minSec == 0 || ev.OccurredAt < minSec) {
			minSec = ev.OccurredAt
		}
	}

	scanHome := ""
	if strings.TrimSpace(home) != "" {
		defaultPath := filepath.Join(home, ".local", "share", "rolling-memory", "memory.db")
		if filepath.Clean(memoryPath) == filepath.Clean(defaultPath) {
			scanHome = home
		}
	}
	files := discoverMultiHarnessJSONLFiles(scanHome, minSec, extraJSONLPaths)
	if len(files) == 0 {
		return out
	}

	var allWins []jsonlRecallWindow
	for _, f := range files {
		wins := parseJSONLRecallWindows(f)
		if len(wins) > 0 {
			allWins = append(allWins, wins...)
		}
	}
	if len(allWins) == 0 {
		return out
	}

	usedWin := make(map[int]bool, len(allWins))
	for _, ev := range events {
		evCue := strings.ToLower(strings.TrimSpace(ev.QueryText))
		evMS := ev.OccurredAt * 1000
		bestIdx := -1
		bestScore := -1

		for idx, w := range allWins {
			if usedWin[idx] {
				continue
			}
			wCue := strings.ToLower(strings.TrimSpace(w.cue))
			cueMatch := evCue != "" && wCue != "" && (wCue == evCue || strings.Contains(wCue, evCue) || strings.Contains(evCue, wCue))
			diffMS := w.timestampMS - evMS
			if diffMS < 0 {
				diffMS = -diffMS
			}
			timeClose := evMS > 0 && w.timestampMS > 0 && diffMS <= 30000

			score := -1
			if cueMatch && timeClose {
				score = 100000 - int(diffMS)
			} else if cueMatch && (diffMS <= 600000 || w.timestampMS == 0) {
				score = 50000
			} else if timeClose && wCue == "" {
				score = 20000 - int(diffMS)
			}
			if score > bestScore {
				bestScore = score
				bestIdx = idx
			}
		}
		if bestIdx >= 0 {
			usedWin[bestIdx] = true
			w := allWins[bestIdx]
			out[ev.NoeticID] = followupCorpusPair{
				harnessID:   w.harnessID,
				toolCorpus:  w.toolCorpus,
				replyCorpus: w.replyCorpus,
				delivered:   w.delivered,
				cue:         w.cue,
			}
		}
	}
	return out
}

// syncMultiHarnessJSONLRecallLedger imports recall tool invocations from non-ZCode JSONL
// harness transcripts (Claude Code, Codex, WorkBuddy, CodeBuddy, DSH) into the sidecar
// efficacy ledger idempotently by event_id.
func syncMultiHarnessJSONLRecallLedger(home, fp string, ldb *sql.DB) {
	if strings.TrimSpace(home) == "" || fp == "" || ldb == nil {
		return
	}
	minSec := time.Now().Add(-zcodeRecallLedgerLookback).Unix()
	files := discoverMultiHarnessJSONLFiles(home, minSec, nil)
	if len(files) == 0 {
		return
	}
	tx, err := ldb.Begin()
	if err != nil {
		return
	}
	for _, f := range files {
		wins := parseJSONLRecallWindows(f)
		for _, w := range wins {
			if w.callID == "" || w.timestampMS <= 0 {
				continue
			}
			obsAt := w.timestampMS * int64(time.Millisecond)
			outcome := "hits"
			if w.isError {
				outcome = "error"
			} else if w.outputBytes <= 0 || strings.Contains(w.delivered, `absent="true"`) {
				outcome = "empty"
			}
			rawID := w.harnessID + "\x00" + w.sessionKey + "\x00" + w.callID
			sum := sha256.Sum256([]byte(rawID))
			shaHex := hex.EncodeToString(sum[:])
			sessSum := sha256.Sum256([]byte(fp + "\x00" + w.harnessID + "\x00" + w.sessionKey))
			sessDigest := "s_" + hex.EncodeToString(sessSum[:])[:24]
			evID := w.harnessID + "_" + shaHex[:24]
			_, _ = tx.Exec(`
				INSERT OR IGNORE INTO efficacy_event(
					event_id, execution_id, session, scope, harness, hook,
					output_outcome, action, target_event_id, observed_at,
					observation, text_sha256, text_bytes
				) VALUES (?, ?, ?, 'mem:self', ?, 'mcp_recall:tool', ?, 'recall', '', ?, 'observed', ?, ?)
			`, evID, shaHex[:32], sessDigest, w.harnessID, outcome, obsAt, shaHex, w.outputBytes)
		}
	}
	_ = tx.Commit()
}

// loadVaultFollowupDocs loads inline and hydrated episodic documents from thin_vault_docs
// so non-ZCode harnesses (Claude Code, Codex, Cursor, WorkBuddy) and unit tests also
// undergo deterministic post-recall intersection auditing, while collecting any @ref:jsonl:
// transcript paths for direct JSONL tool/reply extraction.
func loadVaultFollowupDocs(ctx context.Context, db *sql.DB, home string, minOccurredAt int64) ([]vaultDocFollowupRow, []string) {
	if db == nil {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx, `
		SELECT doc_id, coalesce(occurred_at, 0), coalesce(locus, ''), coalesce(raw_text, ''), coalesce(sentences_json, '')
		FROM thin_vault_docs
		WHERE occurred_at >= ?
		ORDER BY occurred_at ASC, rowid ASC
		LIMIT 400
	`, minOccurredAt)
	if err != nil {
		return nil, nil
	}
	defer rows.Close()

	type scannedFollowupRow struct {
		docID      string
		occurredAt int64
		locus      string
		rawText    string
		sentsJSON  string
	}
	var scanned []scannedFollowupRow
	var extraJSONLPaths []string
	seenJSONL := make(map[string]bool, 16)
	seenRefWarm := make(map[string]bool, 16)
	var refWarmRows []map[string]any
	for rows.Next() {
		var occAt int64
		var docID, locus, rawText, sentsJSON string
		if rows.Scan(&docID, &occAt, &locus, &rawText, &sentsJSON) != nil {
			continue
		}
		trimmed := strings.TrimSpace(rawText)
		if jp := extractJSONLPathFromPointer(trimmed); jp != "" && !seenJSONL[jp] {
			seenJSONL[jp] = true
			extraJSONLPaths = append(extraJSONLPaths, jp)
		}
		// 零拷贝指针尚未进缓存时先收集，循环后一次性走规范批量水合（守护进程优先）；
		// 早期实现只读缓存，冷缓存行会被静默丢弃、审计语料随缓存冷暖而漂移。
		if strings.HasPrefix(trimmed, "@ref:") && !seenRefWarm[trimmed] {
			_, rawHit := readHydrateCache(hydrateCacheKey(hydrateFlavorRaw, home, trimmed))
			_, liveHit := readHydrateCache(hydrateCacheKey(hydrateFlavorLive, home, trimmed))
			if !rawHit && !liveHit {
				seenRefWarm[trimmed] = true
				refWarmRows = append(refWarmRows, map[string]any{"content": trimmed})
			}
		}
		scanned = append(scanned, scannedFollowupRow{
			docID:      docID,
			occurredAt: occAt,
			locus:      locus,
			rawText:    rawText,
			sentsJSON:  sentsJSON,
		})
	}
	if len(refWarmRows) > 0 {
		warmHydrateZCodePointersBatch(home, refWarmRows)
	}

	var out []vaultDocFollowupRow
	for _, r := range scanned {
		trimmed := strings.TrimSpace(r.rawText)
		body := ""
		if strings.HasPrefix(trimmed, "@ref:") {
			if cached, ok := readHydrateCache(hydrateCacheKey(hydrateFlavorRaw, home, trimmed)); ok && cached != "" {
				body = cached
			} else if cachedLive, ok := readHydrateCache(hydrateCacheKey(hydrateFlavorLive, home, trimmed)); ok && cachedLive != "" {
				body = cachedLive
			}
		} else {
			body = trimmed
		}
		if body == "" && r.sentsJSON != "" && r.sentsJSON != "[]" {
			var sents []string
			if json.Unmarshal([]byte(r.sentsJSON), &sents) == nil && len(sents) > 0 {
				body = strings.Join(sents, "\n")
			}
		}
		if body == "" {
			continue
		}
		lowerBody := strings.ToLower(body)
		isTool := strings.HasPrefix(lowerBody, "[tool]") ||
			strings.Contains(lowerBody, `"file_path"`) ||
			strings.Contains(lowerBody, `"command"`) ||
			strings.Contains(lowerBody, "call:default_api:")
		out = append(out, vaultDocFollowupRow{
			docID:      r.docID,
			occurredAt: r.occurredAt,
			locus:      r.locus,
			text:       body,
			isTool:     isTool,
		})
	}
	// 定稿顺序与 SQL 扫描顺序一致（occurred_at ASC, rowid ASC）：调用方依赖升序并在
	// occurredAt > nextOcc 处 break，顺序不得随缓存命中位置漂移。
	return out, extraJSONLPaths
}

var (
	persistedAdoptionLoadedMu sync.Mutex
	persistedAdoptionLoaded   = map[string]bool{}
)

// loadPersistedNoeticAdoption loads settled historical adoption records from the existing
// sidecar ledger (.efficacy.sqlite) into settledNoeticAdoptionCache once per process.
// It never creates .efficacy.sqlite if the file does not already exist.
func loadPersistedNoeticAdoption(memoryPath string) {
	cleanMemPath := filepath.Clean(strings.TrimSpace(memoryPath))
	if cleanMemPath == "" {
		return
	}
	persistedAdoptionLoadedMu.Lock()
	if persistedAdoptionLoaded[cleanMemPath] {
		persistedAdoptionLoadedMu.Unlock()
		return
	}
	persistedAdoptionLoadedMu.Unlock()

	ledgerPath := cleanMemPath + ".efficacy.sqlite"
	info, err := os.Stat(ledgerPath)
	if err != nil || !info.Mode().IsRegular() {
		return
	}
	ldb := openVaultReadOnly(ledgerPath)
	if ldb == nil {
		return
	}
	defer ldb.Close()
	rows, err := ldb.Query(`
		SELECT noetic_id, outcome, matched_json, echo_excluded, assistant_id
		FROM efficacy_noetic_adoption
	`)
	if err != nil {
		return
	}
	defer rows.Close()

	loaded := make(map[string]settledNoeticAdoptionEntry, 256)
	for rows.Next() {
		var nid, outcome, matchedRaw, assistID string
		var echoCnt int
		if rows.Scan(&nid, &outcome, &matchedRaw, &echoCnt, &assistID) != nil || nid == "" {
			continue
		}
		var matched []string
		_ = json.Unmarshal([]byte(matchedRaw), &matched)
		if matched == nil {
			matched = []string{}
		}
		loaded[cleanMemPath+"\x00"+nid] = settledNoeticAdoptionEntry{
			outcome:     outcome,
			matched:     matched,
			echoCnt:     echoCnt,
			assistantID: assistID,
		}
	}
	if len(loaded) > 0 {
		settledNoeticAdoptionMu.Lock()
		for k, v := range loaded {
			settledNoeticAdoptionCache[k] = v
		}
		settledNoeticAdoptionMu.Unlock()
	}
	persistedAdoptionLoadedMu.Lock()
	persistedAdoptionLoaded[cleanMemPath] = true
	persistedAdoptionLoadedMu.Unlock()
}

// savePersistedNoeticAdoption persists newly settled historical adoption records into
// .efficacy.sqlite when the sidecar ledger file already exists on disk.
func savePersistedNoeticAdoption(memoryPath string, entries map[string]settledNoeticAdoptionEntry) {
	cleanMemPath := filepath.Clean(strings.TrimSpace(memoryPath))
	if cleanMemPath == "" || len(entries) == 0 {
		return
	}
	ledgerPath := cleanMemPath + ".efficacy.sqlite"
	info, err := os.Stat(ledgerPath)
	if err != nil || !info.Mode().IsRegular() {
		return
	}
	ldb, err := openPlainSQLite(ledgerPath)
	if err != nil || ldb == nil {
		return
	}
	defer ldb.Close()
	var ver int
	if ldb.QueryRow(`PRAGMA user_version`).Scan(&ver) != nil || ver != 1 {
		return
	}
	_, _ = ldb.Exec(`
		CREATE TABLE IF NOT EXISTS efficacy_noetic_adoption (
			noetic_id TEXT PRIMARY KEY,
			outcome TEXT NOT NULL,
			matched_json TEXT NOT NULL,
			echo_excluded INTEGER NOT NULL DEFAULT 0,
			assistant_id TEXT NOT NULL DEFAULT '',
			audited_at INTEGER NOT NULL DEFAULT 0
		);
	`)
	tx, txErr := ldb.Begin()
	if txErr != nil {
		return
	}
	nowUnix := time.Now().Unix()
	prefix := cleanMemPath + "\x00"
	for k, v := range entries {
		nid := strings.TrimPrefix(k, prefix)
		if nid == "" {
			continue
		}
		mRaw, _ := json.Marshal(v.matched)
		_, _ = tx.Exec(`
			INSERT OR REPLACE INTO efficacy_noetic_adoption(noetic_id, outcome, matched_json, echo_excluded, assistant_id, audited_at)
			VALUES (?, ?, ?, ?, ?, ?)
		`, nid, v.outcome, string(mRaw), v.echoCnt, v.assistantID, nowUnix)
	}
	_ = tx.Commit()
}

// auditNoeticStreamAdoption populates AdoptionOutcome, MatchedAnchors, and EchoExcludedCount
// on each roiNoeticEvent in stream and returns the aggregated summary.
func auditNoeticStreamAdoption(ctx context.Context, db *sql.DB, home, memoryPath string, stream []roiNoeticEvent) roiAdoptionAuditSummary {
	var summary roiAdoptionAuditSummary
	if len(stream) == 0 {
		return summary
	}

	loadPersistedNoeticAdoption(memoryPath)
	nowUnix := time.Now().Unix()
	cleanMemPath := filepath.Clean(strings.TrimSpace(memoryPath))
	var uncachedEvents []roiNoeticEvent
	inBatch := make(map[string]bool, 100)
	cachedByID := make(map[string]settledNoeticAdoptionEntry, len(stream))

	settledNoeticAdoptionMu.Lock()
	for i := range stream {
		key := cleanMemPath + "\x00" + stream[i].NoeticID
		if entry, ok := settledNoeticAdoptionCache[key]; ok {
			cachedByID[stream[i].NoeticID] = entry
		} else if len(uncachedEvents) < 100 {
			uncachedEvents = append(uncachedEvents, stream[i])
			inBatch[stream[i].NoeticID] = true
		}
	}
	settledNoeticAdoptionMu.Unlock()

	var zcodeMap map[string]followupCorpusPair
	var zcodeCompleted map[string]bool
	var vaultDocs []vaultDocFollowupRow
	var jsonlMap map[string]followupCorpusPair
	if len(uncachedEvents) > 0 {
		minOcc := uncachedEvents[len(uncachedEvents)-1].OccurredAt
		for i := range uncachedEvents {
			if uncachedEvents[i].OccurredAt > 0 && (minOcc == 0 || uncachedEvents[i].OccurredAt < minOcc) {
				minOcc = uncachedEvents[i].OccurredAt
			}
		}
		zcodeMap, zcodeCompleted = loadZCodeFollowupCorpora(ctx, home, memoryPath, uncachedEvents)
		var extraJSONLPaths []string
		vaultDocs, extraJSONLPaths = loadVaultFollowupDocs(ctx, db, home, minOcc)
		jsonlMap = loadMultiHarnessJSONLFollowupCorpora(home, memoryPath, uncachedEvents, extraJSONLPaths)
	}

	newSettled := make(map[string]settledNoeticAdoptionEntry, len(uncachedEvents))
	for i := range stream {
		ev := &stream[i]
		if entry, ok := cachedByID[ev.NoeticID]; ok {
			ev.AdoptionOutcome = entry.outcome
			ev.MatchedAnchors = entry.matched
			ev.EchoExcludedCount = entry.echoCnt
			if (ev.AssistantID == "" || ev.AssistantID == "default") && entry.assistantID != "" {
				ev.AssistantID = entry.assistantID
			}
		} else if !inBatch[ev.NoeticID] {
			ev.AdoptionOutcome = "pending"
			ev.MatchedAnchors = []string{}
		} else {
			citedSet := make(map[string]bool, len(ev.CitedUnitIDs))
			for _, uid := range ev.CitedUnitIDs {
				citedSet[strings.TrimSpace(uid)] = true
			}

			// Build full delivered text from AnswerText + CitedSentences + CitedUnits.
			var delivBuf strings.Builder
			if ev.AnswerText != "" {
				delivBuf.WriteString(ev.AnswerText)
				delivBuf.WriteByte('\n')
			}
			for _, s := range ev.CitedSentences {
				delivBuf.WriteString(s)
				delivBuf.WriteByte('\n')
			}
			for _, cu := range ev.CitedUnits {
				if cu.FullText != "" && !strings.HasPrefix(cu.FullText, "@ref:") {
					delivBuf.WriteString(cu.FullText)
					delivBuf.WriteByte('\n')
				} else if cu.StoredText != "" && !strings.HasPrefix(cu.StoredText, "@ref:") {
					delivBuf.WriteString(cu.StoredText)
					delivBuf.WriteByte('\n')
				}
			}

			var toolBuf, replyBuf strings.Builder
			queryCue := ev.QueryText
			inferredHarness := ""
			if zPair, hasZCodeSession := zcodeMap[ev.NoeticID]; hasZCodeSession {
				inferredHarness = "zcode"
				if zPair.delivered != "" {
					delivBuf.WriteString(zPair.delivered)
					delivBuf.WriteByte('\n')
				}
				if zPair.cue != "" && queryCue == "" {
					queryCue = zPair.cue
				}
				if zPair.toolCorpus != "" {
					toolBuf.WriteString(zPair.toolCorpus)
					toolBuf.WriteByte('\n')
				}
				if zPair.replyCorpus != "" {
					replyBuf.WriteString(zPair.replyCorpus)
					replyBuf.WriteByte('\n')
				}
			} else if jPair, hasJSONLSession := jsonlMap[ev.NoeticID]; hasJSONLSession {
				inferredHarness = jPair.harnessID
				if jPair.delivered != "" {
					delivBuf.WriteString(jPair.delivered)
					delivBuf.WriteByte('\n')
				}
				if jPair.cue != "" && queryCue == "" {
					queryCue = jPair.cue
				}
				if jPair.toolCorpus != "" {
					toolBuf.WriteString(jPair.toolCorpus)
					toolBuf.WriteByte('\n')
				}
				if jPair.replyCorpus != "" {
					replyBuf.WriteString(jPair.replyCorpus)
					replyBuf.WriteByte('\n')
				}
			} else {
				for _, cu := range ev.CitedUnits {
					if strings.HasPrefix(cu.StoredText, "@ref:zcode:") {
						inferredHarness = "zcode"
						break
					}
					if jp := extractJSONLPathFromPointer(cu.StoredText); jp != "" {
						inferredHarness = inferHarnessIDFromJSONLPath(jp)
						break
					}
				}
				// Bound the vault doc window to [ev.OccurredAt, nextRecallOccurredAt] (up to +600s)
				// for non-ZCode harnesses and isolated unit-test databases.
				nextOcc := ev.OccurredAt + 600
				if i > 0 && stream[i-1].OccurredAt > ev.OccurredAt && stream[i-1].OccurredAt < nextOcc {
					nextOcc = stream[i-1].OccurredAt
				}
				evLocus := ""
				for _, cu := range ev.CitedUnits {
					if strings.TrimSpace(cu.Locus) != "" {
						evLocus = strings.TrimSpace(cu.Locus)
						break
					}
				}
				for _, vd := range vaultDocs {
					if citedSet[vd.docID] {
						continue
					}
					if vd.occurredAt < ev.OccurredAt {
						continue
					}
					if ev.OccurredAt > 0 && vd.occurredAt > nextOcc {
						break
					}
					if evLocus != "" && vd.locus != "" && !strings.EqualFold(evLocus, vd.locus) {
						continue
					}
					if vd.isTool {
						toolBuf.WriteString(vd.text)
						toolBuf.WriteByte('\n')
					} else {
						replyBuf.WriteString(vd.text)
						replyBuf.WriteByte('\n')
					}
				}
			}

			outcome, matched, echoCnt := evaluateSingleRecallAdoption(
				delivBuf.String(),
				queryCue,
				ev.CitedUnitIDs,
				toolBuf.String(),
				replyBuf.String(),
			)
			ev.AdoptionOutcome = outcome
			ev.MatchedAnchors = matched
			ev.EchoExcludedCount = echoCnt
			if (ev.AssistantID == "" || ev.AssistantID == "default") && inferredHarness != "" {
				ev.AssistantID = inferredHarness
			}
			if ev.OccurredAt > 0 && nowUnix-ev.OccurredAt > 600 && (outcome != "pending" || zcodeCompleted[ev.NoeticID]) {
				newSettled[cleanMemPath+"\x00"+ev.NoeticID] = settledNoeticAdoptionEntry{
					outcome:     ev.AdoptionOutcome,
					matched:     ev.MatchedAnchors,
					echoCnt:     ev.EchoExcludedCount,
					assistantID: ev.AssistantID,
				}
			}
		}

		if ev.Status != "active" {
			continue
		}
		summary.EchoExcludedTotal += int64(ev.EchoExcludedCount)
		switch ev.AdoptionOutcome {
		case "adopted_tool":
			summary.AuditedRecalls++
			summary.AdoptedRecalls++
			summary.AdoptedToolCount++
		case "adopted_context":
			summary.AuditedRecalls++
			summary.AdoptedRecalls++
			summary.AdoptedContextCount++
		case "unadopted":
			summary.AuditedRecalls++
			summary.UnadoptedCount++
		default:
			summary.PendingCount++
		}
	}
	if len(newSettled) > 0 {
		settledNoeticAdoptionMu.Lock()
		if len(settledNoeticAdoptionCache) > 8192 {
			settledNoeticAdoptionCache = map[string]settledNoeticAdoptionEntry{}
			persistedAdoptionLoadedMu.Lock()
			delete(persistedAdoptionLoaded, cleanMemPath)
			persistedAdoptionLoadedMu.Unlock()
		}
		for k, v := range newSettled {
			settledNoeticAdoptionCache[k] = v
		}
		remaining := 0
		for i := range stream {
			if _, ok := settledNoeticAdoptionCache[cleanMemPath+"\x00"+stream[i].NoeticID]; !ok && stream[i].OccurredAt > 0 && nowUnix-stream[i].OccurredAt > 600 {
				remaining++
			}
		}
		settledNoeticAdoptionMu.Unlock()
		savePersistedNoeticAdoption(memoryPath, newSettled)
		if remaining > 0 {
			triggerBackgroundNoeticAdoptionWarm(home, memoryPath, stream)
		}
	}
	if summary.AuditedRecalls > 0 {
		summary.AdoptionRatePct = roundFloat(float64(summary.AdoptedRecalls)*100.0/float64(summary.AuditedRecalls), 1)
	}
	return summary
}

func triggerBackgroundNoeticAdoptionWarm(home, memoryPath string, snapshot []roiNoeticEvent) {
	noeticWarmRunningMu.Lock()
	if noeticWarmRunning {
		noeticWarmRunningMu.Unlock()
		return
	}
	noeticWarmRunning = true
	cp := make([]roiNoeticEvent, len(snapshot))
	copy(cp, snapshot)
	noeticWarmRunningMu.Unlock()

	go func() {
		defer func() {
			noeticWarmRunningMu.Lock()
			noeticWarmRunning = false
			noeticWarmRunningMu.Unlock()
		}()
		db := openVaultReadOnly(memoryPath)
		if db == nil {
			return
		}
		defer db.Close()
		for pass := 0; pass < 35; pass++ {
			nowUnix := time.Now().Unix()
			cleanMemPath := filepath.Clean(strings.TrimSpace(memoryPath))
			settledNoeticAdoptionMu.Lock()
			rem := 0
			for i := range cp {
				if _, ok := settledNoeticAdoptionCache[cleanMemPath+"\x00"+cp[i].NoeticID]; !ok && cp[i].OccurredAt > 0 && nowUnix-cp[i].OccurredAt > 600 {
					rem++
				}
			}
			settledNoeticAdoptionMu.Unlock()
			if rem == 0 {
				break
			}
			_ = auditNoeticStreamAdoption(context.Background(), db, home, memoryPath, cp)
			roiVaultBundleMu.Lock()
			delete(roiVaultBundleCache, memoryPath)
			roiVaultBundleMu.Unlock()
		}
	}()
}

// enrichThreeLayerRowsWithAdoption attaches cached L3 adoption outcomes, matched novel anchors,
// and resolved harness IDs to any `noetic` rows in a ThreeLayerRow slice in O(1) memory lookup.
func enrichThreeLayerRowsWithAdoption(dbPath string, rows []ThreeLayerRow) {
	hasNoetic := false
	for i := range rows {
		if rows[i].Layer == "noetic" {
			hasNoetic = true
			break
		}
	}
	if !hasNoetic || strings.TrimSpace(dbPath) == "" {
		return
	}
	bundle := readROIFactsVaultBundle(dbPath)
	byID := make(map[string]roiNoeticEvent, len(bundle.noeticStream))
	for _, ev := range bundle.noeticStream {
		byID[ev.NoeticID] = ev
	}
	cleanMemPath := filepath.Clean(strings.TrimSpace(dbPath))
	settledNoeticAdoptionMu.Lock()
	defer settledNoeticAdoptionMu.Unlock()
	for i := range rows {
		if rows[i].Layer != "noetic" {
			continue
		}
		if ev, ok := byID[rows[i].ID]; ok {
			rows[i].AdoptionOutcome = ev.AdoptionOutcome
			rows[i].MatchedAnchors = ev.MatchedAnchors
			if rows[i].AssistantID == "" || rows[i].AssistantID == "default" {
				rows[i].AssistantID = ev.AssistantID
			}
			continue
		}
		if entry, ok := settledNoeticAdoptionCache[cleanMemPath+"\x00"+rows[i].ID]; ok {
			rows[i].AdoptionOutcome = entry.outcome
			rows[i].MatchedAnchors = entry.matched
			if rows[i].AssistantID == "" || rows[i].AssistantID == "default" {
				rows[i].AssistantID = entry.assistantID
			}
		}
	}
}
