// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package episodic enumerates the speech segments of a closed harness turn.
//
// A segment is one text part of one speaker message — the unit of L2 episodic
// memory (a harness "turn" mixes dozens of tool steps into one blob, so the turn
// is the wrong unit: its wave address averages heterogeneous topics and delivery
// has to slice it arbitrarily). Enumeration is deterministic and is the single
// source for both the ingest scanner and the zero-copy pointer resolver, so
// `@ref:zcode:<session>:<msgid>#<k>` names the same segment on both sides.
//
// Everything here is form-level: message roles, part types, sequence order. Text
// cleaning (injected context removal) is delegated to `internal/speech`, which is
// the same rule every other ingest path applies.
package episodic

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rocareer/rolling-memory/internal/speech"
)

// Roles of a speech segment.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// ZCodeTextPartFilter is the canonical SQL predicate selecting exactly the speech
// parts of a ZCode session (`part` alias `p`): `type` is the protocol field, so
// reasoning / tool / step / compaction parts are never speech. Every enumeration
// site — live ingest (`ZCodeSessionTurns`, `VisitZCodeTurnsFrom`), the reconcile
// sweep and the `#<k>` resolver (`ZCodeTurnSegmentsUpTo`) — must append it
// verbatim: `#<k>` names the same segment on both sides only while all sites
// filter identically. Head substring sniffing (`instr(substr(p.data, …),
// '"reasoning"')` …) is deliberately absent — an arbitrary byte window applied by
// one site and not another is exactly what shifts `#<k>` onto the wrong segment.
//
// Message-level protocol fields exclude two more classes the harness itself marks
// as not-conversation, so the agent's working narration never becomes speech:
//   - `semantics.uiVisibility = "hidden"`: harness-hidden envelopes (synthetic
//     notices, side-chat/subagent turns);
//   - a message that invoked any tool: its text is the step's process narration
//     (the UI keeps it inside the collapsed work section), never the turn's answer.
//     The structural check covers every finish shape — `tool-calls` (54,445 live),
//     but also interrupted steps (`finish:"other"`) and in-flight steps whose
//     finish is not written yet, where providers stream "Thinking Process:" text
//     into the text channel.
//
// Flattened non-speech envelopes inside a text part (`[thinking]` / `[tool_use …]`
// / `<system-reminder>`) are stripped by `internal/speech` after extraction, never
// by SQL.
const ZCodeTextPartFilter = `json_extract(p.data, '$.type') = 'text'
		  AND COALESCE(json_extract(p.data, '$.synthetic'), 0) = 0
		  AND COALESCE((
		        SELECT json_extract(am.data, '$.semantics.uiVisibility')
		        FROM message am WHERE am.id = p.message_id
		      ), 'visible') <> 'hidden'
		  AND NOT EXISTS (
		        SELECT 1 FROM part tp
		        WHERE tp.message_id = p.message_id
		          AND json_extract(tp.data, '$.type') = 'tool'
		      )`

// Segment is one speaker text part of a closed turn.
type Segment struct {
	// Role is "user" or "assistant".
	Role string
	// Text is the cleaned speech of this part (injected envelopes removed).
	Text string
	// OccurredAt is the part/message timestamp in Unix seconds (0 = unknown).
	OccurredAt int64
	// Index is the segment ordinal inside its turn (0-based), which is what the
	// `#<k>` pointer suffix carries.
	Index int
}

// Turn is one closed exchange: a user message plus the assistant parts that
// followed it before the next user message.
type Turn struct {
	// UserMsgID identifies the message that opened the turn.
	UserMsgID string
	// OccurredAt is the newest known timestamp of the turn.
	OccurredAt int64
	// Segments are the turn's speech parts in conversation order.
	Segments []Segment
}

// ZCodeSessionTurns enumerates every closed turn of one ZCode session DB.
// Parts are read with the canonical filter (`type='text'`, `synthetic=0`) in
// `(message.sequence, part.sequence)` order — the same shape the resolver uses.
func ZCodeSessionTurns(db *sql.DB, sessionID string) ([]Turn, error) {
	if db == nil || strings.TrimSpace(sessionID) == "" {
		return nil, nil
	}
	rows, err := db.Query(`
		SELECT m.id,
		       COALESCE(m.time_created, 0) AS ts,
		       json_extract(m.data, '$.role') AS role,
		       json_extract(p.data, '$.text') AS txt
		FROM message m
		JOIN part p ON p.message_id = m.id
		WHERE m.session_id = ?
		  AND `+ZCodeTextPartFilter+`
		ORDER BY m.sequence ASC, p.sequence ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	turns := make([]Turn, 0, 16)
	currentTurn := -1
	currentUserMsg := ""
	for rows.Next() {
		var msgID, role, txt sql.NullString
		var ts int64
		if rows.Scan(&msgID, &ts, &role, &txt) != nil {
			continue
		}
		speaker := normalizeRole(role.String)
		cleaned := speech.StripConversationNoise(txt.String)
		if speaker == "" || cleaned == "" {
			continue
		}
		if speaker == RoleUser && msgID.String != currentUserMsg {
			turns = append(turns, Turn{UserMsgID: msgID.String})
			currentTurn = len(turns) - 1
			currentUserMsg = msgID.String
		}
		if currentTurn < 0 {
			// Assistant text before the session's first user message has no turn.
			continue
		}
		turn := &turns[currentTurn]
		turn.Segments = append(turn.Segments, Segment{
			Role: speaker, Text: cleaned, OccurredAt: ts, Index: len(turn.Segments),
		})
		if ts > turn.OccurredAt {
			turn.OccurredAt = ts
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return dropEmptyTurns(turns), nil
}

type zcodeTurnCacheKey struct {
	db           *sql.DB
	sessionID    string
	userMsgID    string
	msgCount     int64
	maxSeq       int64
	totalChanges int64
}

var (
	zcodeTurnSegmentsCache sync.Map
	zcodeTurnCacheCount    int
	zcodeTurnCacheMu       sync.Mutex
)

const maxZCodeTurnCacheEntries = 4096

func storeZCodeTurnCache(key zcodeTurnCacheKey, val []Segment) {
	zcodeTurnCacheMu.Lock()
	if zcodeTurnCacheCount >= maxZCodeTurnCacheEntries {
		zcodeTurnSegmentsCache.Range(func(k, _ any) bool {
			zcodeTurnSegmentsCache.Delete(k)
			return true
		})
		zcodeTurnCacheCount = 0
	}
	zcodeTurnCacheCount++
	zcodeTurnCacheMu.Unlock()
	zcodeTurnSegmentsCache.Store(key, val)
}

// ZCodeTurnSegments enumerates one turn, starting at userMsgID and stopping at the
// next user message. The resolver uses this so a `#<k>` pointer never needs the
// whole session.
func ZCodeTurnSegments(db *sql.DB, sessionID, userMsgID string) ([]Segment, error) {
	segs, _, err := ZCodeTurnSegmentsUpTo(db, sessionID, userMsgID, -1)
	return segs, err
}

// ZCodeTurnSegmentsUpTo enumerates the speech segments of one turn starting at userMsgID.
// When minSegments > 0, it stops as soon as at least minSegments segments have been
// collected (returning complete=false) or the turn ends at the next user message
// (returning complete=true), avoiding scanning hundreds of trailing tool-call messages
// when resolving an early segment `#k`.
func ZCodeTurnSegmentsUpTo(db *sql.DB, sessionID, userMsgID string, minSegments int) ([]Segment, bool, error) {
	if db == nil || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(userMsgID) == "" {
		return nil, false, errors.New("episodic: zcode turn requires session and user message id")
	}
	var cacheKey zcodeTurnCacheKey
	if minSegments <= 0 {
		var msgCount, maxSeq, totalChg int64
		_ = db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(sequence), 0), total_changes() FROM message WHERE session_id = ?`, sessionID).Scan(&msgCount, &maxSeq, &totalChg)
		cacheKey = zcodeTurnCacheKey{
			db:           db,
			sessionID:    sessionID,
			userMsgID:    userMsgID,
			msgCount:     msgCount,
			maxSeq:       maxSeq,
			totalChanges: totalChg,
		}
		if cached, ok := zcodeTurnSegmentsCache.Load(cacheKey); ok {
			src := cached.([]Segment)
			out := make([]Segment, len(src))
			copy(out, src)
			return out, true, nil
		}
	}
	var startSeq sql.NullInt64
	if err := db.QueryRow(
		`SELECT sequence FROM message WHERE id = ? AND session_id = ?`,
		userMsgID, sessionID,
	).Scan(&startSeq); err != nil {
		return nil, false, err
	}
	type msgHeader struct {
		id      string
		seq     sql.NullInt64
		ts      int64
		speaker string
	}
	segments := make([]Segment, 0, 8)
	started := false
	complete := true

	if startSeq.Valid {
		jRows, jErr := db.Query(`
			SELECT m.id, COALESCE(m.time_created, 0) AS ts,
			       json_extract(m.data, '$.role') AS role,
			       COALESCE(p.sequence, 0) AS pseq, p.rowid AS prowid,
			       json_extract(p.data, '$.text') AS txt
			FROM message m
			JOIN part p ON p.message_id = m.id
			WHERE m.session_id = ?
			  AND m.sequence >= ?
			  AND `+ZCodeTextPartFilter+`
			ORDER BY m.sequence ASC`, sessionID, startSeq.Int64)
		if jErr == nil {
			type partItem struct {
				pseq   int64
				prowid int64
				txt    string
			}
			var curMsgID, curSpeaker string
			var curTS int64
			var curParts []partItem
			closedTurn := false
			stoppedEarly := false

			flushMsg := func() {
				if curMsgID == "" || len(curParts) == 0 || closedTurn || stoppedEarly {
					curParts = curParts[:0]
					return
				}
				if !started && (curSpeaker != RoleUser || curMsgID != userMsgID) {
					curParts = curParts[:0]
					return
				}
				if len(curParts) > 1 {
					sort.SliceStable(curParts, func(i, j int) bool {
						if curParts[i].pseq != curParts[j].pseq {
							return curParts[i].pseq < curParts[j].pseq
						}
						return curParts[i].prowid < curParts[j].prowid
					})
				}
				var cleanedParts []string
				for _, pt := range curParts {
					if cleaned := speech.StripConversationNoise(pt.txt); cleaned != "" {
						cleanedParts = append(cleanedParts, cleaned)
					}
				}
				curParts = curParts[:0]
				if len(cleanedParts) == 0 {
					return
				}
				if curSpeaker == RoleUser {
					if !started {
						started = true
					} else if curMsgID != userMsgID {
						closedTurn = true
						return
					}
				}
				for _, cleaned := range cleanedParts {
					segments = append(segments, Segment{
						Role: curSpeaker, Text: cleaned, OccurredAt: curTS, Index: len(segments),
					})
				}
				if minSegments > 0 && started && len(segments) >= minSegments {
					stoppedEarly = true
					complete = false
				}
			}

			for jRows.Next() {
				var mid, role, txt sql.NullString
				var ts, pseq, prowid int64
				if jRows.Scan(&mid, &ts, &role, &pseq, &prowid, &txt) != nil {
					continue
				}
				sp := normalizeRole(role.String)
				if sp == "" || mid.String == "" {
					continue
				}
				if mid.String != curMsgID {
					flushMsg()
					if closedTurn || stoppedEarly {
						break
					}
					curMsgID = mid.String
					curSpeaker = sp
					curTS = ts
				}
				curParts = append(curParts, partItem{pseq: pseq, prowid: prowid, txt: txt.String})
			}
			rowErr := jRows.Err()
			jRows.Close()
			if rowErr != nil {
				return nil, false, rowErr
			}
			if !closedTurn && !stoppedEarly {
				flushMsg()
			}
			if len(segments) == 0 {
				return nil, false, fmt.Errorf("episodic: empty zcode turn %s", userMsgID)
			}
			if minSegments <= 0 && complete {
				cachedCopy := make([]Segment, len(segments))
				copy(cachedCopy, segments)
				storeZCodeTurnCache(cacheKey, cachedCopy)
			}
			return segments, complete, nil
		}
	}

	offset := 0
	const pageSize = 16

	for {
		var mRows *sql.Rows
		var err error
		if startSeq.Valid {
			mRows, err = db.Query(`
				SELECT m.id, m.sequence,
				       COALESCE(m.time_created, 0) AS ts,
				       json_extract(m.data, '$.role') AS role
				FROM message m
				WHERE m.session_id = ?
				  AND m.sequence >= ?
				ORDER BY m.sequence ASC
				LIMIT ? OFFSET ?`, sessionID, startSeq.Int64, pageSize, offset)
		} else {
			mRows, err = db.Query(`
				SELECT m.id, m.sequence,
				       COALESCE(m.time_created, 0) AS ts,
				       json_extract(m.data, '$.role') AS role
				FROM message m
				WHERE m.session_id = ?
				ORDER BY m.sequence ASC
				LIMIT ? OFFSET ?`, sessionID, pageSize, offset)
		}
		if err != nil {
			return nil, false, err
		}
		var headers []msgHeader
		rowsRead := 0
		for mRows.Next() {
			rowsRead++
			var msgID, role sql.NullString
			var seq sql.NullInt64
			var ts int64
			if mRows.Scan(&msgID, &seq, &ts, &role) != nil {
				continue
			}
			speaker := normalizeRole(role.String)
			if speaker == "" {
				continue
			}
			headers = append(headers, msgHeader{id: msgID.String, seq: seq, ts: ts, speaker: speaker})
		}
		if err := mRows.Err(); err != nil {
			mRows.Close()
			return nil, false, err
		}
		mRows.Close()
		if rowsRead == 0 {
			break
		}
		offset += rowsRead

		closedTurn := false
		stoppedEarly := false
		for _, h := range headers {
			if !started {
				if h.speaker != RoleUser || h.id != userMsgID {
					continue
				}
			}
			pRows, err := db.Query(`
				SELECT json_extract(p.data, '$.text') AS txt
				FROM part p
				WHERE p.message_id = ?
				  AND `+ZCodeTextPartFilter+`
				ORDER BY p.sequence ASC`, h.id)
			if err != nil {
				return nil, false, err
			}
			var cleanedParts []string
			for pRows.Next() {
				var txt sql.NullString
				if pRows.Scan(&txt) != nil {
					continue
				}
				if cleaned := speech.StripConversationNoise(txt.String); cleaned != "" {
					cleanedParts = append(cleanedParts, cleaned)
				}
			}
			pErr := pRows.Err()
			pRows.Close()
			if pErr != nil {
				return nil, false, pErr
			}
			if len(cleanedParts) == 0 {
				continue
			}
			if h.speaker == RoleUser {
				if !started {
					started = true
				} else if h.id != userMsgID {
					closedTurn = true
					break
				}
			}
			for _, cleaned := range cleanedParts {
				segments = append(segments, Segment{
					Role: h.speaker, Text: cleaned, OccurredAt: h.ts, Index: len(segments),
				})
			}
			if minSegments > 0 && started && len(segments) >= minSegments {
				stoppedEarly = true
				complete = false
				break
			}
		}
		if closedTurn || stoppedEarly || rowsRead < pageSize {
			break
		}
	}
	if len(segments) == 0 {
		return nil, false, fmt.Errorf("episodic: empty zcode turn %s", userMsgID)
	}
	if minSegments <= 0 && complete {
		cachedCopy := make([]Segment, len(segments))
		copy(cachedCopy, segments)
		storeZCodeTurnCache(cacheKey, cachedCopy)
	}
	return segments, complete, nil
}

// JSONLTurn is one closed turn of a JSONL harness transcript.
type JSONLTurn struct {
	// Index is the turn ordinal in file order (0-based), carried by `@ref:jsonl:`.
	Index int
	// OccurredAt is the newest known timestamp of the turn.
	OccurredAt int64
	// Segments are the turn's speech parts in file order.
	Segments []Segment
}

// JSONLTurns enumerates every closed turn of a JSONL transcript.
func JSONLTurns(path string) ([]JSONLTurn, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("episodic: empty jsonl path")
	}
	raw, err := os.ReadFile(strings.TrimSpace(path))
	if err != nil {
		return nil, err
	}
	turns := make([]JSONLTurn, 0, 16)
	current := -1
	var pendingAssist []Segment
	flushPending := func() {
		if current < 0 || len(pendingAssist) == 0 {
			pendingAssist = pendingAssist[:0]
			return
		}
		turn := &turns[current]
		for _, seg := range pendingAssist {
			seg.Index = len(turn.Segments)
			turn.Segments = append(turn.Segments, seg)
			if seg.OccurredAt > turn.OccurredAt {
				turn.OccurredAt = seg.OccurredAt
			}
		}
		pendingAssist = pendingAssist[:0]
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if isJSONLToolCallLine(line) {
			// A tool call in the same turn means the preceding assistant text was
			// step narration, not the turn's answer.
			pendingAssist = pendingAssist[:0]
			continue
		}
		role, texts, occurredAt := parseJSONLLine(line)
		if role == "" || len(texts) == 0 {
			continue
		}
		if role == RoleUser {
			flushPending()
			turns = append(turns, JSONLTurn{Index: len(turns)})
			current = len(turns) - 1
		}
		if current < 0 {
			continue
		}
		if role == RoleAssistant {
			for _, txt := range texts {
				pendingAssist = append(pendingAssist, Segment{
					Role: role, Text: txt, OccurredAt: occurredAt,
				})
			}
			continue
		}
		turn := &turns[current]
		for _, txt := range texts {
			turn.Segments = append(turn.Segments, Segment{
				Role: role, Text: txt, OccurredAt: occurredAt, Index: len(turn.Segments),
			})
		}
		if occurredAt > turn.OccurredAt {
			turn.OccurredAt = occurredAt
		}
	}
	flushPending()
	out := make([]JSONLTurn, 0, len(turns))
	for _, turn := range turns {
		if len(turn.Segments) == 0 {
			continue
		}
		turn.Index = len(out)
		out = append(out, turn)
	}
	return out, nil
}

// isJSONLToolCallLine reports whether a JSONL transcript line records a standalone
// tool invocation item (Codex / DSH `response_item` with `function_call`,
// `custom_tool_call`, `web_search_call`, or `local_shell_call`). When such a line
// follows assistant text inside the same turn, that preceding text was process
// narration for the tool step rather than the turn's final answer.
func isJSONLToolCallLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || !strings.HasPrefix(trimmed, "{") {
		return false
	}
	if !strings.Contains(trimmed, "function_call") &&
		!strings.Contains(trimmed, "custom_tool_call") &&
		!strings.Contains(trimmed, "web_search_call") &&
		!strings.Contains(trimmed, "local_shell_call") {
		return false
	}
	var obj map[string]any
	if json.Unmarshal([]byte(trimmed), &obj) != nil || obj == nil {
		return false
	}
	checkType := func(t string) bool {
		switch strings.TrimSpace(t) {
		case "function_call", "custom_tool_call", "web_search_call", "local_shell_call":
			return true
		}
		return false
	}
	if topType, _ := obj["type"].(string); checkType(topType) {
		return true
	}
	if payload, ok := obj["payload"].(map[string]any); ok && payload != nil {
		if pType, _ := payload["type"].(string); checkType(pType) {
			return true
		}
	}
	return false
}

// JSONLTurnSegments enumerates one turn of a JSONL transcript by ordinal.
func JSONLTurnSegments(path string, turnIndex int) ([]Segment, error) {
	return jsonlTurnSegments(path, turnIndex)
}

// parseJSONLLine extracts one speaker row of a harness transcript. It accepts the
// shapes the supported harnesses write (`message`/`payload` envelopes with string
// or block-list content) and returns one text per content block: a segment is a
// part, never a merged message.
func parseJSONLLine(line string) (role string, texts []string, occurredAt int64) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || !strings.HasPrefix(trimmed, "{") {
		return "", nil, 0
	}
	var obj map[string]any
	if json.Unmarshal([]byte(trimmed), &obj) != nil || obj == nil {
		return "", nil, 0
	}
	if truthy(obj["isMeta"]) || truthy(obj["isCompactSummary"]) {
		return "", nil, 0
	}
	message, _ := obj["message"].(map[string]any)
	if message == nil {
		if payload, ok := obj["payload"].(map[string]any); ok && payload != nil {
			if pType, _ := payload["type"].(string); pType == "message" || payload["role"] != nil {
				message = payload
			}
		}
	}
	if message == nil {
		message = obj
	}
	roleText, _ := message["role"].(string)
	if roleText == "" {
		roleText, _ = obj["type"].(string)
	}
	speaker := normalizeRole(roleText)
	if speaker == "" {
		return "", nil, 0
	}
	if speaker == RoleAssistant {
		// A tool step's text is the agent's process narration, not conversation: the
		// harness marks the step it ended with a tool call (`stop_reason: "tool_use"`,
		// which ZCode writes as `finish: "tool-calls"`). Only the turn's answer step is
		// speech; 3,059/3,262 live Claude-Code text rows carry the marker.
		if stopReason, _ := message["stop_reason"].(string); strings.TrimSpace(stopReason) == "tool_use" {
			return "", nil, 0
		}
	}
	parts := make([]string, 0, 4)
	switch content := message["content"].(type) {
	case string:
		parts = append(parts, content)
	case []any:
		for _, block := range content {
			item, ok := block.(map[string]any)
			if !ok {
				continue
			}
			blockType, _ := item["type"].(string)
			if blockType != "text" && blockType != "input_text" && blockType != "output_text" {
				continue
			}
			if value, ok := item["text"].(string); ok && strings.TrimSpace(value) != "" {
				parts = append(parts, value)
			}
		}
	}
	texts = make([]string, 0, len(parts))
	for _, part := range parts {
		cleaned := speech.StripConversationNoise(part)
		if cleaned == "" {
			continue
		}
		if strings.HasPrefix(cleaned, "<app-context>") || strings.HasPrefix(cleaned, "# AGENTS.md instructions for ") ||
			strings.HasPrefix(cleaned, "<environment_context>") {
			continue
		}
		texts = append(texts, cleaned)
	}
	if len(texts) == 0 {
		return "", nil, 0
	}
	return speaker, texts, parseUnixSeconds(obj["timestamp"])
}

func parseUnixSeconds(value any) int64 {
	normalize := func(v int64) int64 {
		if v > 1e11 {
			return v / 1000
		}
		return v
	}
	switch typed := value.(type) {
	case float64:
		return normalize(int64(typed))
	case int64:
		return normalize(typed)
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return normalize(parsed)
		}
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			return 0
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
			if parsed, err := time.Parse(layout, text); err == nil {
				return parsed.Unix()
			}
		}
	}
	return 0
}

func normalizeRole(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "user", "human":
		return RoleUser
	case "assistant", "ai", "bot", "agent":
		return RoleAssistant
	}
	return ""
}

func dropEmptyTurns(turns []Turn) []Turn {
	out := make([]Turn, 0, len(turns))
	for _, turn := range turns {
		if len(turn.Segments) == 0 {
			continue
		}
		out = append(out, turn)
	}
	return out
}

func truthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case float64:
		return typed != 0
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	}
	return true
}
