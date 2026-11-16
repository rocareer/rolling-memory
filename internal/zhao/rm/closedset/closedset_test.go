// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package closedset

import (
	"strings"
	"testing"
)

func TestClosedSetTablesIntegrityAndBijection(t *testing.T) {
	if len(StandardClosedSetTable) == 0 || len(UniversalSchemaClosedSetTable) == 0 {
		t.Fatalf("closed-set tables must be non-empty: std=%d univ=%d", len(StandardClosedSetTable), len(UniversalSchemaClosedSetTable))
	}
	if len(ClosedSetHarmonicEntries) != len(StandardClosedSetTable)+len(UniversalSchemaClosedSetTable) {
		t.Fatalf("combined table length mismatch: got=%d", len(ClosedSetHarmonicEntries))
	}
	seenTokenRow := make(map[string]string, 7200)
	for i, row := range ClosedSetHarmonicEntries {
		colon := strings.LastIndexByte(row, ':')
		if colon <= 0 {
			t.Fatalf("row %d missing colon: %q", i, row)
		}
		head := row[:colon]
		toks := strings.Split(row[colon+1:], ",")
		if len(toks) < 2 {
			t.Fatalf("row %d (%s) has <2 tokens: %v", i, head, toks)
		}
		for _, tok := range toks {
			tok = strings.TrimSpace(tok)
			if tok == "" {
				continue
			}
			if prevHead, exists := seenTokenRow[tok]; exists {
				t.Fatalf("token %q mapped to multiple closed-set rows (%s vs %s): violates single-valued quotient map", tok, prevHead, head)
			}
			seenTokenRow[tok] = head
		}
	}
	if got := LookupFirstTag("newzealand"); got != "@geo:cc:nz" {
		t.Fatalf("LookupFirstTag(newzealand) = %q, want @geo:cc:nz", got)
	}
	if got := LookupFirstTag("kg"); got != "@unit:mass:kg" {
		t.Fatalf("LookupFirstTag(kg) = %q, want @unit:mass:kg", got)
	}
}
