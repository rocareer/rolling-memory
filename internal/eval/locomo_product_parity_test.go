// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package eval

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/thinclient"
	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

// TestLoCoMoDirectRankerMatchesProductRecall 钉住「唯一引擎」契约：
// 同一金库、同一查询下，直接排序臂（Vault.SearchClientSDKDefault）与产品召回臂
// （thinclient.RecallWithOptions，即日常 recall 与评测共用的入口）的 Top-10 必须逐位一致。
// 任一臂单独漂移都会让「日常使用 = 评测」失效，故作为常驻回归守卫。
// 全量 1540 题对拍已在 2026-11-11 实测为 0 差异；默认跑前 200 题控制时长。
func TestLoCoMoDirectRankerMatchesProductRecall(t *testing.T) {
	dataDir := filepath.Join("..", "..", "evaldata")
	corpusPath := filepath.Join(dataDir, "corpus.jsonl")
	questionsPath := filepath.Join(dataDir, "tiers", "questions-200.json")
	if _, err := os.Stat(corpusPath); err != nil {
		t.Skipf("bundled LoCoMo corpus unavailable: %v", err)
	}
	if _, err := os.Stat(questionsPath); err != nil {
		t.Skipf("bundled LoCoMo questions unavailable: %v", err)
	}
	corpus, err := LoadCorpus(corpusPath, 0)
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}
	questions, err := LoadQuestions(questionsPath, 0, 0)
	if err != nil {
		t.Fatalf("load questions: %v", err)
	}
	storePath := filepath.Join(t.TempDir(), "eval-locomo-local-plain.json")
	engine, err := NewCloudEngine(&config.Config{Mode: "local"}, SuiteLoCoMo, storePath, WithPulseMode(PulseModeLocalPlain))
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	documents := make([]CloudDocument, 0, len(corpus))
	cursor := map[string]int{}
	for _, row := range corpus {
		text := pyStrip(pyStr(firstTruthy(row["text"], "")))
		if text == "" {
			continue
		}
		author := pyStrip(pyStr(firstTruthy(row["author"], firstTruthy(row["speaker"], ""))))
		locator := pyStr(firstTruthy(row["locator"], ""))
		session := pyStr(firstTruthy(row["session"], firstTruthy(strings.Split(locator, "#")[0], "default")))
		turn := firstTruthy(row["id"], 0)
		occurred, convErr := pyInt(firstTruthy(row["occurred_at"], 0))
		if convErr != nil {
			t.Fatalf("occurred_at: %v", convErr)
		}
		docID := "locomo-" + ids.SHA256Hex(engine.Scope + "\x00" + session + "\x00" + pyStr(turn))[:24]
		lineCount := strings.Count(text, "\n") + 1
		if lineCount < 1 {
			lineCount = 1
		}
		lineStart := cursor[session] + 1
		lineEnd := lineStart + lineCount - 1
		cursor[session] = lineEnd
		blockID := locator
		if blockID == "" {
			blockID = session + "#" + pyStr(turn)
		}
		documents = append(documents, CloudDocument{
			ID: docID, Text: text, Author: author, OccurredAt: occurred, NarratedAt: occurred,
			Layer: ramesdk.LayerEpisodic, DocKey: session, FileURL: session,
			BlockKey: blockID, BlockID: blockID, LineStart: lineStart, LineEnd: lineEnd,
			SubUnits: locomoTurnSubUnits(docID, text, row, lineStart, lineEnd),
		})
	}
	if _, err := engine.Ingest(context.Background(), documents, true, 8, nil); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	vault, err := engine.ensureLocalPlainVault()
	if err != nil || vault == nil {
		t.Fatalf("vault: %v", err)
	}
	tc := thinclient.NewLocalClient(vault)

	// 交付文本同源：JSON store 水合（历史臂交付面）与派生金库水合（统一产品臂交付面）
	// 的 raw / distilled 文本必须逐文档一致——检索 ID 相同但交付上下文不同同样会
	// 让「日常 = 评测」失效（LoCoMo 的答案入上下文指标即基于 distilled 文本）。
	var textDiffs int
	for _, doc := range documents {
		storeRaw, storeDist, _, sErr := engine.Store.LoadLocalDocument(doc.ID)
		if sErr != nil {
			t.Fatalf("store load %s: %v", doc.ID, sErr)
		}
		vaultRaw, vaultDist, _, vErr := vault.LoadLocalDocument(doc.ID)
		if vErr != nil {
			t.Fatalf("vault load %s: %v", doc.ID, vErr)
		}
		if storeRaw != vaultRaw || storeDist != vaultDist {
			textDiffs++
		}
	}
	if textDiffs != 0 {
		t.Fatalf("hydration text diverges on %d/%d documents (store vs vault)", textDiffs, len(documents))
	}

	const rawDepth = 96
	var compared, mismatches int64
	var mu sync.Mutex
	examples := make([]string, 0, 4)
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, q := range questions {
		text := questionText(q)
		if strings.TrimSpace(text) == "" {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(text string) {
			defer wg.Done()
			defer func() { <-sem }()
			direct, derr := vault.SearchClientSDKDefault(text, rawDepth)
			if derr != nil {
				return
			}
			product, perr := tc.RecallWithOptions(context.Background(), text, thinclient.BlindQueryOptions{TopK: rawDepth})
			if perr != nil {
				return
			}
			atomic.AddInt64(&compared, 1)
			limit := 10
			if len(direct) < limit {
				limit = len(direct)
			}
			for i := 0; i < limit; i++ {
				if i >= len(product) || direct[i].UnitID != product[i].UnitID {
					atomic.AddInt64(&mismatches, 1)
					mu.Lock()
					if len(examples) < 4 {
						examples = append(examples, text+" | direct="+direct[i].UnitID)
					}
					mu.Unlock()
					return
				}
			}
		}(text)
	}
	wg.Wait()
	if compared == 0 {
		t.Fatal("no questions compared")
	}
	if mismatches != 0 {
		for _, ex := range examples {
			t.Logf("mismatch: %s", ex)
		}
		t.Fatalf("direct ranker and product recall diverge on %d/%d questions", mismatches, compared)
	}
}
