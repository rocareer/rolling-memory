// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeScanFixture(t *testing.T, path, label string, count int) {
	t.Helper()
	var source strings.Builder
	for i := 0; i < count; i++ {
		fmt.Fprintf(&source, `{"type":"user","timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","content":"%s %d"}}`+"\n", label, i)
	}
	if err := os.WriteFile(path, []byte(source.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestEpisodicSourceScanReplacementBeforeResume(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	writeScanFixture(t, path, "original", 10)
	sources := []episodicScanSource{{HarnessID: "claude-code", Kind: "jsonl", Path: path}}
	cursor, more, err := visitEpisodicSources(context.Background(), sources, "mem:test", episodicSourceCursor{}, 4, func(candidateEpisodicTurn) error { return nil })
	if err != nil || !more {
		t.Fatalf("first page more=%v err=%v", more, err)
	}
	checkpoint, err := json.Marshal(cursor)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(checkpoint, &cursor); err != nil {
		t.Fatal(err)
	}
	replacement := path + ".replacement"
	writeScanFixture(t, replacement, "replacement", 12)
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for page := 0; page < 10; page++ {
		cursor, more, err = visitEpisodicSources(context.Background(), sources, "mem:test", cursor, 4, func(turn candidateEpisodicTurn) error {
			seen[turn.Text] = true
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	for i := 0; i < 12; i++ {
		if !seen[fmt.Sprintf("[user] replacement %d", i)] {
			t.Errorf("replacement item %d was skipped after restart", i)
		}
	}
}

func TestEpisodicSourceScanReadFailureDoesNotBlockOtherSource(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "a.jsonl")
	available := filepath.Join(dir, "b.jsonl")
	writeScanFixture(t, available, "available", 1)
	sources := []episodicScanSource{
		{HarnessID: "claude-code", Kind: "jsonl", Path: missing},
		{HarnessID: "claude-code", Kind: "jsonl", Path: available},
	}
	seen := false
	_, _, err := visitEpisodicSources(context.Background(), sources, "mem:test", episodicSourceCursor{}, 10, func(turn candidateEpisodicTurn) error {
		seen = strings.Contains(turn.Text, "available")
		return nil
	})
	if err == nil {
		t.Error("source failure was not reported")
	}
	if !seen {
		t.Error("available source was blocked by failed source")
	}
}

func TestEpisodicSourceScanSaveFailureDoesNotAdvanceCursor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	writeScanFixture(t, path, "entry", 10)
	sources := []episodicScanSource{{HarnessID: "claude-code", Kind: "jsonl", Path: path}}
	failure := errors.New("vault write failed")
	calls := 0
	cursor, more, err := visitEpisodicSources(context.Background(), sources, "mem:test", episodicSourceCursor{}, 10, func(candidateEpisodicTurn) error {
		calls++
		if calls == 4 {
			return failure
		}
		return nil
	})
	if !errors.Is(err, failure) || !more {
		t.Fatalf("save failure more=%v err=%v", more, err)
	}
	var first string
	_, _, err = visitEpisodicSources(context.Background(), sources, "mem:test", cursor, 1, func(turn candidateEpisodicTurn) error {
		first = turn.Text
		return nil
	})
	if err != nil || first != "[user] entry 3" {
		t.Fatalf("failed item not retried first=%q err=%v", first, err)
	}
}

func TestEpisodicSourceScanAppendAcrossScales(t *testing.T) {
	for _, count := range []int{10, 1000, 10000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.jsonl")
			writeScanFixture(t, path, "entry", count)
			sources := []episodicScanSource{{HarnessID: "claude-code", Kind: "jsonl", Path: path}}
			seen := map[string]int{}
			cursor := episodicSourceCursor{}
			for page := 0; page <= count; page++ {
				var more bool
				var err error
				cursor, more, err = visitEpisodicSources(context.Background(), sources, "mem:test", cursor, 250, func(turn candidateEpisodicTurn) error {
					seen[turn.Text]++
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				if page == 0 {
					f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
					if err != nil {
						t.Fatal(err)
					}
					_, err = fmt.Fprintln(f, `{"type":"user","timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","content":"appended"}}`)
					closeErr := f.Close()
					if err != nil || closeErr != nil {
						t.Fatalf("append err=%v close=%v", err, closeErr)
					}
					if !more {
						cursor = episodicSourceCursor{}
						more = true
					}
				}
				if !more {
					break
				}
			}
			for i := 0; i < count; i++ {
				if seen[fmt.Sprintf("[user] entry %d", i)] == 0 {
					t.Fatalf("missing original entry %d", i)
				}
			}
			if seen["[user] appended"] == 0 {
				t.Fatal("missing appended entry")
			}
		})
	}
}
