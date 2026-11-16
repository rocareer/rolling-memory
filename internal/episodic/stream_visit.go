// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package episodic

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/rocareer/rolling-memory/internal/speech"
)

// VisitJSONLTurns streams complete turns without materializing the transcript.
// The callback's closed flag means another user turn followed; a false flag
// identifies the current tail. A callback error stops the scan immediately.
func VisitJSONLTurns(ctx context.Context, path string, visit func(JSONLTurn, bool) error) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	var current *JSONLTurn
	var pendingAssist []Segment
	nextIndex := 0
	flushPending := func() {
		if current == nil || len(pendingAssist) == 0 {
			pendingAssist = pendingAssist[:0]
			return
		}
		for _, seg := range pendingAssist {
			seg.Index = len(current.Segments)
			current.Segments = append(current.Segments, seg)
			if seg.OccurredAt > current.OccurredAt {
				current.OccurredAt = seg.OccurredAt
			}
		}
		pendingAssist = pendingAssist[:0]
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, readErr := reader.ReadString('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		if isJSONLToolCallLine(line) {
			pendingAssist = pendingAssist[:0]
		} else {
			role, texts, occurredAt := parseJSONLLine(line)
			if role != "" && len(texts) != 0 {
				if role == RoleUser {
					flushPending()
					if current != nil && len(current.Segments) > 0 {
						if err := visit(*current, true); err != nil {
							return err
						}
					} else if current != nil {
						nextIndex--
					}
					current = &JSONLTurn{Index: nextIndex}
					nextIndex++
				}
				if current != nil {
					if role == RoleAssistant {
						for _, text := range texts {
							pendingAssist = append(pendingAssist, Segment{Role: role, Text: text, OccurredAt: occurredAt})
						}
					} else {
						for _, text := range texts {
							current.Segments = append(current.Segments, Segment{Role: role, Text: text, OccurredAt: occurredAt, Index: len(current.Segments)})
						}
						if occurredAt > current.OccurredAt {
							current.OccurredAt = occurredAt
						}
					}
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	flushPending()
	if current != nil && len(current.Segments) > 0 {
		return visit(*current, false)
	}
	return nil
}

// VisitZCodeTurns streams one session from a SQLite query snapshot. The callback
// receives closed=false only for the tail, and may stop iteration with an error.
func VisitZCodeTurns(ctx context.Context, db *sql.DB, sessionID string, visit func(Turn, bool) error) error {
	return VisitZCodeTurnsFrom(ctx, db, sessionID, "", visit)
}

// VisitZCodeTurnsFrom streams one session starting at startUserMsgID when present
// in the session's sequence order, falling back to the start of the session otherwise.
func VisitZCodeTurnsFrom(ctx context.Context, db *sql.DB, sessionID, startUserMsgID string, visit func(Turn, bool) error) error {
	if db == nil || sessionID == "" {
		return errors.New("episodic: zcode session requires a database and session id")
	}
	var mRows *sql.Rows
	var err error
	var startSeq sql.NullInt64
	if strings.TrimSpace(startUserMsgID) != "" {
		_ = db.QueryRowContext(ctx, `SELECT sequence FROM message WHERE session_id = ? AND id = ?`, sessionID, strings.TrimSpace(startUserMsgID)).Scan(&startSeq)
	}
	if startSeq.Valid {
		mRows, err = db.QueryContext(ctx, `
			SELECT m.id, COALESCE(m.time_created, 0), json_extract(m.data, '$.role')
			FROM message m
			WHERE m.session_id = ? AND m.sequence >= ?
			ORDER BY m.sequence ASC`, sessionID, startSeq.Int64)
	} else {
		mRows, err = db.QueryContext(ctx, `
			SELECT m.id, COALESCE(m.time_created, 0), json_extract(m.data, '$.role')
			FROM message m
			WHERE m.session_id = ?
			ORDER BY m.sequence ASC`, sessionID)
	}
	if err != nil {
		return err
	}
	type msgHeader struct {
		id      string
		ts      int64
		speaker string
	}
	var headers []msgHeader
	for mRows.Next() {
		var msgID, role sql.NullString
		var timestamp int64
		if err := mRows.Scan(&msgID, &timestamp, &role); err != nil {
			mRows.Close()
			return err
		}
		speaker := normalizeRole(role.String)
		if speaker == "" {
			continue
		}
		headers = append(headers, msgHeader{id: msgID.String, ts: timestamp, speaker: speaker})
	}
	if err := mRows.Err(); err != nil {
		mRows.Close()
		return err
	}
	if err := mRows.Close(); err != nil {
		return err
	}

	var current *Turn
	const batchSize = 256
	for start := 0; start < len(headers); start += batchSize {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := start + batchSize
		if end > len(headers) {
			end = len(headers)
		}
		chunk := headers[start:end]
		marks := make([]string, len(chunk))
		args := make([]any, len(chunk))
		for i, h := range chunk {
			marks[i] = "?"
			args[i] = h.id
		}
		pRows, err := db.QueryContext(ctx, `
			SELECT p.message_id, json_extract(p.data, '$.text')
			FROM part p
			WHERE p.message_id IN (`+strings.Join(marks, ",")+`)
			  AND `+ZCodeTextPartFilter+`
			ORDER BY p.message_id, p.sequence ASC`, args...)
		if err != nil {
			return err
		}
		partsByMsg := make(map[string][]string, len(chunk))
		for pRows.Next() {
			var msgID, text sql.NullString
			if err := pRows.Scan(&msgID, &text); err != nil {
				pRows.Close()
				return err
			}
			if cleaned := speech.StripConversationNoise(text.String); cleaned != "" {
				partsByMsg[msgID.String] = append(partsByMsg[msgID.String], cleaned)
			}
		}
		pErr := pRows.Err()
		_ = pRows.Close()
		if pErr != nil {
			return pErr
		}
		for _, h := range chunk {
			if err := ctx.Err(); err != nil {
				return err
			}
			cleanedParts := partsByMsg[h.id]
			if len(cleanedParts) == 0 {
				continue
			}
			if h.speaker == RoleUser && (current == nil || h.id != current.UserMsgID) {
				if current != nil {
					if err := visit(*current, true); err != nil {
						return err
					}
				}
				current = &Turn{UserMsgID: h.id}
			}
			if current != nil {
				for _, cleaned := range cleanedParts {
					current.Segments = append(current.Segments, Segment{Role: h.speaker, Text: cleaned, OccurredAt: h.ts, Index: len(current.Segments)})
				}
				if h.ts > current.OccurredAt {
					current.OccurredAt = h.ts
				}
			}
		}
	}
	if current != nil {
		return visit(*current, false)
	}
	return nil
}
