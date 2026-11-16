// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/tokenzr"
)

// Line-structured chunks open with `[rel_path#anchor] (abs_path)` and keep one rendered
// line per source line, so `L<start>-<end>` anchors map to real file:line coordinates and
// every consumer can open the file without another lookup.
func TestReadAndChunkLocalFileHeaderCarriesAbsPathAndExactLineMapping(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "demo.go")
	// The 1024-token chunk budget needs a body of several hundred such lines before it splits,
	// so the fixture stays large enough to exercise both the split and the line mapping.
	const totalLines = 600
	var builder strings.Builder
	for line := 1; line <= totalLines; line++ {
		if line%10 == 0 {
			builder.WriteString("\n") // blank lines still count against the file's own numbering
			continue
		}
		builder.WriteString(fmt.Sprintf("line %d token%d payload\n", line, line))
	}
	if err := os.WriteFile(abs, []byte(builder.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	chunks, err := ReadAndChunkLocalFile(abs, "demo.go")
	if err != nil {
		t.Fatalf("chunk: %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("expected the file to split into several chunks, got %d", len(chunks))
	}
	for index, chunk := range chunks {
		header := fmt.Sprintf("[demo.go#%s] (%s)", chunk.Anchor, abs)
		if !strings.HasPrefix(chunk.Text, header+"\n") {
			t.Fatalf("chunk %d header = %q, want prefix %q", index, firstLine(chunk.Text), header)
		}
		body := strings.TrimPrefix(chunk.Text, header+"\n")
		if got, want := strings.Count(body, "\n")+1, chunk.LineEnd-chunk.LineStart+1; got != want {
			t.Fatalf("chunk %d body lines = %d, want %d (range L%d-%d)", index, got, want, chunk.LineStart, chunk.LineEnd)
		}
		if index == 0 {
			if chunk.LineStart != 1 || chunk.Anchor != fmt.Sprintf("L1-%d", chunk.LineEnd) {
				t.Fatalf("first chunk anchor = %q (start %d, end %d)", chunk.Anchor, chunk.LineStart, chunk.LineEnd)
			}
			continue
		}
		if chunk.LineStart != chunks[index-1].LineEnd+1 {
			t.Fatalf("chunk %d starts at %d but previous ended at %d", index, chunk.LineStart, chunks[index-1].LineEnd)
		}
	}
	last := chunks[len(chunks)-1]
	if last.Anchor != fmt.Sprintf("L%d-%d", last.LineStart, totalLines) || last.LineEnd != totalLines {
		t.Fatalf("last chunk must end at the file's final line: anchor=%q end=%d", last.Anchor, last.LineEnd)
	}
}

func firstLine(text string) string {
	if cut := strings.Index(text, "\n"); cut >= 0 {
		return text[:cut]
	}
	return text
}

// The chunk budget is measured in tokenz-r tokens: every chunk stays inside `TargetChunkTokens`
// unless a single source line alone exceeds the budget (lines are atomic and are never split),
// and such an oversized line forms its own chunk.
func TestChunkBudgetIsCountedInTokenzRTokens(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "budget.md")
	var builder strings.Builder
	for i := 0; i < 120; i++ {
		builder.WriteString(fmt.Sprintf(
			"第%d段 本地记忆单元预算校验：分块按 tokenz-r 计数，逐行原子不可切分，覆盖必须连续无缝隙。\n", i))
	}
	oversized := strings.Repeat("超长单行本身超过分块预算时仍保持整行原子不可切分", 200)
	builder.WriteString(oversized + "\n")
	if err := os.WriteFile(abs, []byte(builder.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	chunks, err := ReadAndChunkLocalFile(abs, "budget.md")
	if err != nil {
		t.Fatalf("chunk: %v", err)
	}
	if len(chunks) < 5 {
		t.Fatalf("expected the document to cross the budget repeatedly, got %d chunks", len(chunks))
	}
	oversizedChunks := 0
	for index, chunk := range chunks {
		header := fmt.Sprintf("[budget.md#%s] (%s)", chunk.Anchor, abs)
		body := strings.TrimPrefix(chunk.Text, header+"\n")
		tokens := 0
		for _, line := range strings.Split(body, "\n") {
			tokens += tokenzr.Count(strings.TrimSpace(line))
		}
		if tokens <= TargetChunkTokens {
			continue
		}
		if chunk.LineStart != chunk.LineEnd {
			t.Fatalf("chunk %d holds %d tokens across %d lines (budget %d)",
				index, tokens, chunk.LineEnd-chunk.LineStart+1, TargetChunkTokens)
		}
		oversizedChunks++
	}
	if oversizedChunks != 1 {
		t.Fatalf("oversized single lines must stand alone: got %d oversized chunks, want 1", oversizedChunks)
	}
}
