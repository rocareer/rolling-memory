// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient

import (
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/episodic"
	"github.com/rocareer/rolling-memory/internal/episodicunit"
)

// TestEpisodicBackfillLiveAudit is opt-in and read-only; it prints only aggregates.
func TestEpisodicBackfillLiveAudit(t *testing.T) {
	path := os.Getenv("ROLLING_MEMORY_EPISODIC_AUDIT_DB")
	if path == "" {
		t.Skip("live audit not requested")
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT doc_id, raw_text FROM thin_vault_docs WHERE (layer = 'episodic' OR layer = '') AND substr(raw_text,1,11) = '@ref:jsonl:' ORDER BY doc_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type source struct {
		turns []episodic.JSONLTurn
		err   error
	}
	cache := map[string]source{}
	scopes := []string{"mem:self", "default"}
	if scope := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_EPISODIC_AUDIT_SCOPE")); scope != "" {
		scopes = append(scopes, scope)
	}
	total, available, matches := 0, 0, make([]int, len(scopes))
	for rows.Next() {
		var id, ref string
		if err := rows.Scan(&id, &ref); err != nil {
			t.Fatal(err)
		}
		total++
		tail := strings.TrimPrefix(ref, "@ref:jsonl:")
		colon := strings.LastIndex(tail, ":")
		if colon < 0 {
			continue
		}
		path, tail := tail[:colon], tail[colon+1:]
		turn, segment, chunk, hasSegment, hasChunk := splitPointerSegment(tail)
		turnIndex, err := strconv.Atoi(turn)
		if err != nil || !hasSegment {
			continue
		}
		s, ok := cache[path]
		if !ok {
			s.turns, s.err = episodic.JSONLTurns(path)
			cache[path] = s
		}
		if s.err != nil || turnIndex < 0 || turnIndex >= len(s.turns) || segment >= len(s.turns[turnIndex].Segments) {
			continue
		}
		part := s.turns[turnIndex].Segments[segment]
		text, err := episodicSegmentChunk("["+part.Role+"] "+part.Text, segment, chunk, hasChunk, "")
		if err != nil {
			continue
		}
		available++
		for i, scope := range scopes {
			if episodicunit.DocID(scope, text) == id {
				matches[i]++
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	fmt.Printf("jsonl audit total=%d available=%d", total, available)
	for i, scope := range scopes {
		fmt.Printf(" scope[%d]=%s matches=%d", i, scope, matches[i])
	}
	fmt.Println()
}
