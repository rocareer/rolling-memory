// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package episodic

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestVisitJSONLTurnsParityAcrossScales(t *testing.T) {
	for _, count := range []int{0, 1, 100, 10000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			var source strings.Builder
			for i := 0; i < count; i++ {
				fmt.Fprintf(&source, `{"type":"user","message":{"role":"user","content":"你好 %d"}}`+"\n", i)
				source.WriteString(`{"type":"assistant","message":{"role":"assistant","content":"مرحبا"}}` + "\n")
			}
			path := filepath.Join(t.TempDir(), "transcript.jsonl")
			if err := os.WriteFile(path, []byte(source.String()), 0o600); err != nil {
				t.Fatal(err)
			}
			want, err := JSONLTurns(path)
			if err != nil {
				t.Fatal(err)
			}
			var got []JSONLTurn
			var closed []bool
			err = VisitJSONLTurns(context.Background(), path, func(turn JSONLTurn, complete bool) error {
				got = append(got, turn)
				closed = append(closed, complete)
				return nil
			})
			if err != nil || len(got) != len(want) {
				t.Fatalf("count=%d got=%d err=%v", count, len(got), err)
			}
			for i := range want {
				if !reflect.DeepEqual(got[i], want[i]) || closed[i] != (i+1 < count) {
					t.Fatalf("turn %d: got=%+v want=%+v closed=%v", i, got[i], want[i], closed[i])
				}
			}
		})
	}
}

func TestVisitJSONLTurnsCancellationAndCallbackFailure(t *testing.T) {
	path := writeJSONL(t, []string{`{"role":"user","content":"one"}`, `{"role":"user","content":"two"}`})
	failure := errors.New("stop")
	calls := 0
	err := VisitJSONLTurns(context.Background(), path, func(JSONLTurn, bool) error {
		calls++
		return failure
	})
	if !errors.Is(err, failure) || calls != 1 {
		t.Fatalf("callback calls=%d err=%v", calls, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = VisitJSONLTurns(ctx, path, func(JSONLTurn, bool) error { t.Fatal("visited after cancellation"); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled scan: %v", err)
	}
}

func TestVisitZCodeTurnsParityAndWALSnapshot(t *testing.T) {
	db := openZCodeFixture(t)
	want, err := ZCodeSessionTurns(db, "s1")
	if err != nil {
		t.Fatal(err)
	}
	var got []Turn
	var closed []bool
	err = VisitZCodeTurns(context.Background(), db, "s1", func(turn Turn, complete bool) error {
		got = append(got, turn)
		closed = append(closed, complete)
		return nil
	})
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%+v want=%+v err=%v", got, want, err)
	}
	for i := range closed {
		if closed[i] != (i+1 < len(closed)) {
			t.Fatalf("turn %d closed=%v", i, closed[i])
		}
	}

	path := filepath.Join(t.TempDir(), "wal.sqlite")
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	_, err = writer.Exec(`PRAGMA journal_mode=WAL; CREATE TABLE message(id TEXT, session_id TEXT, sequence INTEGER, time_created INTEGER, data TEXT); CREATE TABLE part(message_id TEXT, sequence INTEGER, data TEXT); INSERT INTO message VALUES ('m1','s',1,1,'{"role":"user"}'), ('m2','s',2,2,'{"role":"user"}'); INSERT INTO part VALUES ('m1',1,'{"type":"text","text":"one"}'), ('m2',1,'{"type":"text","text":"two"}');`)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var ids []string
	err = VisitZCodeTurns(context.Background(), reader, "s", func(turn Turn, _ bool) error {
		ids = append(ids, turn.UserMsgID)
		if len(ids) == 1 {
			_, err := writer.Exec(`INSERT INTO message VALUES ('m3','s',3,3,'{"role":"user"}'); INSERT INTO part VALUES ('m3',1,'{"type":"text","text":"three"}');`)
			return err
		}
		return nil
	})
	if err != nil || !reflect.DeepEqual(ids, []string{"m1", "m2"}) {
		t.Fatalf("snapshot ids=%v err=%v", ids, err)
	}
	ids = nil
	err = VisitZCodeTurns(context.Background(), reader, "s", func(turn Turn, _ bool) error { ids = append(ids, turn.UserMsgID); return nil })
	if err != nil || !reflect.DeepEqual(ids, []string{"m1", "m2", "m3"}) {
		t.Fatalf("next snapshot ids=%v err=%v", ids, err)
	}
}
