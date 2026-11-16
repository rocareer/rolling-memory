// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package hooks

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/efficacy"
	"github.com/rocareer/rolling-memory/internal/store"
)

type hookOutputWriter struct {
	buffer   bytes.Buffer
	limit    int
	writeErr error
	flushErr error
}

func (w *hookOutputWriter) Write(p []byte) (int, error) {
	if w.limit >= 0 && len(p) > w.limit {
		p = p[:w.limit]
	}
	n, _ := w.buffer.Write(p)
	return n, w.writeErr
}

func (w *hookOutputWriter) Flush() error { return w.flushErr }

func TestHookEfficacyBoundLedgerAndPrivacy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		limit    int
		writeErr error
		flushErr error
		wantOut  string
	}{
		{name: "success", limit: -1, wantOut: "success"},
		{name: "short", limit: 7, wantOut: "short_write"},
		{name: "write failure", limit: 0, writeErr: io.ErrClosedPipe, wantOut: "failure"},
		{name: "flush failure", limit: -1, flushErr: io.ErrClosedPipe, wantOut: "failure"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t)
			seedCat(t)
			wsDir := filepath.Join(env.base, "Rolling-Memory")
			if err := os.MkdirAll(wsDir, 0o755); err != nil {
				t.Fatalf("mkdir ws: %v", err)
			}
			// Retrieval is agent-driven, so the hook's only output surface is the
			// four-turn cadence reminder: three prior user turns put this prompt on
			// the cadence.
			seeded := openMemory(t)
			for turn := 1; turn <= 3; turn++ {
				if _, _, err := seeded.Store.AddTrace("mem:hooks", fmt.Sprintf("先前第 %d 轮用户输入", turn), store.TraceOptions{
					Session: "sensitive-session", Turn: int64(turn), Kind: "message", Author: "user", Audience: "shared",
				}); err != nil {
					t.Fatalf("seed trace: %v", err)
				}
			}
			_ = seeded.Close()
			writer := &hookOutputWriter{limit: tc.limit, writeErr: tc.writeErr, flushErr: tc.flushErr}
			payload := fmt.Sprintf(`{"session_id":"sensitive-session","cwd":%q,"prompt":"继续检查规格同步机制"}`, wsDir)
			code := HookMain([]string{"recall", "--json"}, strings.NewReader(payload), writer, io.Discard)
			if code != 0 || (tc.limit == -1 && writer.buffer.Len() == 0) {
				t.Fatalf("hook exit=%d output bytes=%d", code, writer.buffer.Len())
			}
			path, err := efficacy.DefaultLedgerPath(env.db)
			if err != nil {
				t.Fatal(err)
			}
			ledger, err := efficacy.OpenLedger(path)
			if err != nil {
				t.Fatalf("open bound ledger: %v", err)
			}
			defer ledger.Close()
			entries, err := ledger.Events(0, 10)
			if err != nil || len(entries) != 1 {
				t.Fatalf("entries=%+v err=%v", entries, err)
			}
			if entries[0].OutputOutcome != tc.wantOut {
				t.Fatalf("got outcome %q, want %q", entries[0].OutputOutcome, tc.wantOut)
			}
			if strings.Contains(entries[0].Session, "sensitive-session") {
				t.Fatalf("raw session leaked into ledger: %q", entries[0].Session)
			}
			raw, err := os.ReadFile(env.log)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "sensitive-session") || strings.Contains(string(raw), "小白") {
				t.Fatal("log leaked payload")
			}
		})
	}
}

func TestHookEfficacyEmptyAndOtherEvents(t *testing.T) {
	env := newTestEnv(t)
	code, output, _ := runHookMain(t, []string{"recall", "--json"}, `{}`)
	if code != 0 || output != "" {
		t.Fatalf("empty hook exit=%d output=%q", code, output)
	}
	code, _, _ = runHookMain(t, []string{"close", "--json"}, `{"session_id":"s1"}`)
	if code != 0 {
		t.Fatalf("close exit = %d", code)
	}
	path, _ := efficacy.DefaultLedgerPath(env.db)
	// When memory.db has not been initialized yet (empty hook before any DB write),
	// no unbound sidecar ledger should be created.
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unbound ledger was created for non-existent DB: %v", err)
	}
}
