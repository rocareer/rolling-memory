// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package episodic

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestJSONLStreamResolverAgreesAcrossScales(t *testing.T) {
	for _, count := range []int{1, 100, 10000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			var source strings.Builder
			for i := 0; i < count; i++ {
				fmt.Fprintf(&source, `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"你好 %d"},{"type":"input_text","text":"مرحبا 日本語"}]}}`+"\n", i)
				source.WriteString(`{"payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ответ Καλημέρα"},{"type":"tool","text":"ignored"}]}}` + "\n")
				source.WriteString(`{"isMeta":true,"type":"user","message":{"content":"ignored"}}` + "\n")
			}
			path := writeJSONL(t, []string{source.String()})
			turns, err := JSONLTurns(path)
			if err != nil || len(turns) != count {
				t.Fatalf("turns=%d err=%v", len(turns), err)
			}
			for _, index := range []int{0, count / 2, count - 1} {
				got, err := JSONLTurnSegments(path, index)
				if err != nil || !reflect.DeepEqual(got, turns[index].Segments) {
					t.Fatalf("turn %d: got=%+v want=%+v err=%v", index, got, turns[index].Segments, err)
				}
			}
			for _, index := range []int{-1, count} {
				if _, err := JSONLTurnSegments(path, index); err == nil {
					t.Fatalf("turn %d unexpectedly resolved", index)
				}
			}
		})
	}
}

func TestJSONLStreamResolverLiveChangesAndLongLine(t *testing.T) {
	long := strings.Repeat("世界مرحبا", 20000)
	path := writeJSONL(t, []string{`{"type":"user","message":{"role":"user","content":"` + long + `"}}`})
	first, err := JSONLTurnSegments(path, 0)
	if err != nil || len(first) != 1 || first[0].Text != long {
		t.Fatalf("long line failed: segments=%d err=%v", len(first), err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(`{"type":"assistant","message":{"role":"assistant","content":"追加"}}` + "\n" + `{"type":"user","message":{"role":"user","content":"next"}}`)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	all, err := JSONLTurns(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := range all {
		got, err := JSONLTurnSegments(path, i)
		if err != nil || !reflect.DeepEqual(got, all[i].Segments) {
			t.Fatalf("turn %d after append: got=%+v err=%v", i, got, err)
		}
	}
	if err := os.WriteFile(path, []byte(`{"type":"user","message":{"role":"user","content":"replacement"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := JSONLTurnSegments(path, 0)
	if err != nil || len(got) != 1 || got[0].Text != "replacement" {
		t.Fatalf("replacement: %+v %v", got, err)
	}
	if _, err := JSONLTurnSegments(path, 1); err == nil {
		t.Fatal("truncated turn unexpectedly resolved")
	}
}

func TestJSONLStreamResolverHarnessShapesAndInvalidRows(t *testing.T) {
	path := writeJSONL(t, []string{
		"",
		`not json`,
		`{"type":"user","message":`,
		`{"role":"human","content":"top level"}`,
		`{"role":"agent","content":"top level reply"}`,
		`{"type":"user","message":{"role":"user","content":"message envelope"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","text":"ignored"},{"type":"text","text":"reply"}]}}`,
		`{"payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"payload envelope"}]}}`,
		`{"payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"response"}]}}`,
		`{"isCompactSummary":true,"type":"user","message":{"content":"ignored"}}`,
		`{"type":"user","content":"type fallback"}`,
		`{"type":"assistant","content":"type response"}`,
	})
	turns, err := JSONLTurns(path)
	if err != nil || len(turns) != 4 {
		t.Fatalf("turns=%+v err=%v", turns, err)
	}
	for index, turn := range turns {
		got, err := JSONLTurnSegments(path, index)
		if err != nil || !reflect.DeepEqual(got, turn.Segments) {
			t.Fatalf("turn %d: got=%+v want=%+v err=%v", index, got, turn.Segments, err)
		}
	}
}

func TestZCodeResolverMultipleUserParts(t *testing.T) {
	db := openZCodeFixture(t)
	if _, err := db.Exec(`INSERT INTO part(id, message_id, session_id, sequence, data) VALUES ('extra', 'm1', 's1', 2, '{"type":"text","text":"مرحبا 日本語"}')`); err != nil {
		t.Fatal(err)
	}
	turns, err := ZCodeSessionTurns(db, "s1")
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range turns {
		got, err := ZCodeTurnSegments(db, "s1", turn.UserMsgID)
		if err != nil || !reflect.DeepEqual(got, turn.Segments) {
			t.Fatalf("turn %s: got=%+v want=%+v err=%v", turn.UserMsgID, got, turn.Segments, err)
		}
	}
}
