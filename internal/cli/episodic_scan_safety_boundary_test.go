// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEpisodicSourceScanRetriesFailedSourceAfterOtherProgress(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "a.jsonl")
	available := filepath.Join(dir, "b.jsonl")
	writeScanFixture(t, available, "available", 1)
	sources := []episodicScanSource{
		{HarnessID: "claude-code", Kind: "jsonl", Path: missing},
		{HarnessID: "claude-code", Kind: "jsonl", Path: available},
	}
	seen := map[string]bool{}
	visit := func(turn candidateEpisodicTurn) error { seen[turn.Text] = true; return nil }
	cursor, _, err := visitEpisodicSources(context.Background(), sources, "mem:test", episodicSourceCursor{}, 10, visit)
	if err == nil || !seen["[user] available 0"] {
		t.Fatalf("first pass err=%v seen=%v", err, seen)
	}
	writeScanFixture(t, missing, "recovered", 1)
	_, _, err = visitEpisodicSources(context.Background(), sources, "mem:test", cursor, 10, visit)
	if err != nil || !seen["[user] recovered 0"] {
		t.Fatalf("recovered source was not retried: err=%v seen=%v cursor=%+v", err, seen, cursor)
	}
}

func TestEpisodicSourceScanSaveFailureStopsBeforeOtherSource(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "a.jsonl")
	second := filepath.Join(dir, "b.jsonl")
	writeScanFixture(t, first, "first", 1)
	writeScanFixture(t, second, "second", 1)
	sources := []episodicScanSource{
		{HarnessID: "claude-code", Kind: "jsonl", Path: first},
		{HarnessID: "claude-code", Kind: "jsonl", Path: second},
	}
	failure := errors.New("system write failure")
	var seen []string
	cursor, more, err := visitEpisodicSources(context.Background(), sources, "mem:test", episodicSourceCursor{}, 10, func(turn candidateEpisodicTurn) error {
		seen = append(seen, turn.Text)
		return failure
	})
	if !errors.Is(err, failure) || !more || len(seen) != 1 || seen[0] != "[user] first 0" {
		t.Fatalf("save failure err=%v more=%v seen=%v", err, more, seen)
	}
	var retried string
	_, _, err = visitEpisodicSources(context.Background(), sources, "mem:test", cursor, 1, func(turn candidateEpisodicTurn) error {
		retried = turn.Text
		return nil
	})
	if err != nil || retried != "[user] first 0" {
		t.Fatalf("failed save not retried: err=%v retried=%q", err, retried)
	}
}

func TestEpisodicSourceScanGrowingSourceDoesNotStarveLaterSource(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "a.jsonl")
	second := filepath.Join(dir, "b.jsonl")
	writeScanFixture(t, first, "growing", 4)
	writeScanFixture(t, second, "later", 1)
	sources := []episodicScanSource{
		{HarnessID: "claude-code", Kind: "jsonl", Path: first},
		{HarnessID: "claude-code", Kind: "jsonl", Path: second},
	}
	cursor := episodicSourceCursor{}
	seenLater := false
	for page := 0; page < 8 && !seenLater; page++ {
		var err error
		cursor, _, err = visitEpisodicSources(context.Background(), sources, "mem:test", cursor, 2, func(turn candidateEpisodicTurn) error {
			seenLater = seenLater || strings.Contains(turn.Text, "later")
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(first, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		var writeErr error
		for i := 0; i < 2; i++ {
			if _, writeErr = fmt.Fprintf(f, `{"type":"user","timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","content":"appended %d %d"}}`+"\n", page, i); writeErr != nil {
				break
			}
		}
		closeErr := f.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatalf("append err=%v close=%v", writeErr, closeErr)
		}
	}
	if !seenLater {
		t.Fatal("later source starved while earlier source grew")
	}
}
