// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package eval

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/thinclient"
	"github.com/rocareer/rolling-memory/internal/tokenzr"
	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
	_ "modernc.org/sqlite"
)

// queryVaultPureFTS5BM25 仅使用 SQLite FTS5 trigram + bm25(thin_vault_docs_fts) 检索并按 BM25 毫分降序返回 docID 列表。
func queryVaultPureFTS5BM25(v *thinclient.Vault, query string, topK int) ([]string, error) {
	bm25Map, err := v.QueryFTS5BM25(query, topK)
	if err != nil || len(bm25Map) == 0 {
		return nil, err
	}
	type pair struct {
		id    string
		score int64
	}
	items := make([]pair, 0, len(bm25Map))
	for id, sc := range bm25Map {
		items = append(items, pair{id: id, score: sc})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].score != items[j].score {
			return items[i].score > items[j].score
		}
		return items[i].id < items[j].id
	})
	if len(items) > topK {
		items = items[:topK]
	}
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.id
	}
	return out, nil
}

// fuseRRF 将两组排序 ID 列表按 Reciprocal Rank Fusion (k=60) 融合。
func fuseRRF(primaryIDs, secondaryIDs []string, topK int) []string {
	scores := make(map[string]float64, len(primaryIDs)+len(secondaryIDs))
	for r, id := range primaryIDs {
		scores[id] += 1.0 / float64(60+r+1)
	}
	for r, id := range secondaryIDs {
		scores[id] += 1.0 / float64(60+r+1)
	}
	type ranked struct {
		id    string
		score float64
	}
	all := make([]ranked, 0, len(scores))
	for id, sc := range scores {
		all = append(all, ranked{id: id, score: sc})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].score != all[j].score {
			return all[i].score > all[j].score
		}
		return all[i].id < all[j].id
	})
	if topK > 0 && len(all) > topK {
		all = all[:topK]
	}
	out := make([]string, len(all))
	for i, it := range all {
		out[i] = it.id
	}
	return out
}

var atmDumpFailures atomic.Int64

// dumpRetrievalRow appends one per-question retrieval diagnostic row (JSONL) under dir.
// It exists so retrieval changes can be attributed question-by-question without re-running
// the LLM reader; gated by DUMP_RETRIEVAL_ROWS_DIR and only active for the SDK arm.
func dumpRetrievalRow(dir, tier string, q ATMQAItem, retIDs []string, recMap map[string]float64, evHit bool) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		atmDumpFailures.Add(1)
		return
	}
	row := map[string]any{
		"tier":          tier,
		"id":            q.ID,
		"question":      q.Question,
		"qtype":         q.QType,
		"gold":          q.Answer,
		"evidence_ids":  q.EvidenceIDs,
		"retrieval_ids": retIDs,
		"recall_metric": "gold-set-mean",
		"r_at_1":        recMap["R@1"],
		"r_at_5":        recMap["R@5"],
		"r_at_10":       recMap["R@10"],
		"r_at_25":       recMap["R@25"],
		"r_at_50":       recMap["R@50"],
		"evidence_hit":  evHit,
		"all_hit_10":    len(q.EvidenceIDs) > 0 && recMap["R@10"] >= 1.0-1e-9,
	}
	payload, err := json.Marshal(row)
	if err != nil {
		atmDumpFailures.Add(1)
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "retrieval-"+tier+".jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		atmDumpFailures.Add(1)
		return
	}
	if _, err := f.Write(append(payload, '\n')); err != nil {
		atmDumpFailures.Add(1)
	}
	if err := f.Close(); err != nil {
		atmDumpFailures.Add(1)
	}
}

func TestDualProtocolFTS5BM25Baseline(t *testing.T) {
	if os.Getenv("RUN_FTS5_BM25_BASELINE") != "1" {
		t.Skip("set RUN_FTS5_BM25_BASELINE=1 to run full corpus SQLite FTS5+BM25 dual-protocol benchmark")
	}

	report := map[string]any{
		"generated_at": time.Now().Format(time.RFC3339),
	}

	// =========================================================================
	// Part 1: 剑桥 ATM-Bench (11,034 条 SGM 记忆 · Hard-31 & Main-1013)
	// =========================================================================
	// 用例 cwd = internal/eval ⇒ 仓库根 = ../..；不写本机绝对路径（公开树可提交）。
	dataDir := os.Getenv("ATM_DATA_DIR")
	if dataDir == "" || !filepath.IsAbs(dataDir) {
		t.Fatal("ATM_DATA_DIR must be an absolute verified data directory")
	}
	atmDir, err := ResolveATMDataDir(dataDir)
	if err != nil {
		t.Fatalf("ResolveATMDataDir: %v", err)
	}
	if filepath.Clean(atmDir) != filepath.Clean(dataDir) {
		t.Fatalf("ResolveATMDataDir returned %q, not verified ATM_DATA_DIR %q", atmDir, dataDir)
	}
	if err := VerifyATMCorpusPins(atmDir); err != nil {
		t.Fatalf("VerifyATMCorpusPins: %v", err)
	}
	repoEvalData, _ := filepath.Abs(filepath.Join("..", "..", DataDirName))
	sgmItems, byID, _, err := LoadATMSGMItems(atmDir, ATMScope, 0)
	if err != nil {
		t.Fatalf("LoadATMSGMItems: %v", err)
	}
	hardQAs, err := LoadATMQAItems(atmDir, TierATMHard, 0, 0)
	if err != nil {
		t.Fatalf("LoadATMQAItems(hard): %v", err)
	}
	main1013QAs, err := LoadATMQAItems(atmDir, TierATM1013, 0, 0)
	if err != nil {
		t.Fatalf("LoadATMQAItems(1013): %v", err)
	}

	// 建立 SQLite FTS5+BM25 金库并导入全部 SGM 条目
	atmDBPath := filepath.Join(t.TempDir(), "atm_vault.sqlite")
	atmDB, err := sql.Open("sqlite", atmDBPath)
	if err != nil {
		t.Fatalf("open atm sqlite: %v", err)
	}
	defer atmDB.Close()
	if _, err := atmDB.Exec(`PRAGMA synchronous = OFF; PRAGMA journal_mode = MEMORY;`); err != nil {
		t.Fatalf("sqlite pragma: %v", err)
	}

	atmVault, err := thinclient.NewVault(atmDB, []byte("atm-eval-fts5-bm25-key"))
	if err != nil {
		t.Fatalf("NewVault(atm): %v", err)
	}
	if !atmVault.HasFTS5BM25() {
		t.Fatalf("expected SQLite FTS5+BM25 enabled on atmVault")
	}

	t0Ingest := time.Now()
	tx, err := atmDB.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	stmtDoc, err := tx.Prepare(`INSERT OR REPLACE INTO thin_vault_docs(doc_id, layer, occurred_at, raw_text, sentences_json, address_uri, coord64, waveform_bar, doc_macro_wave, kept_indices_json, assistant_id, incarnation_id, locus) VALUES (?, 'episodic', ?, ?, '[]', '', 0, '', '', '[]', 'default', '', '')`)
	if err != nil {
		t.Fatalf("prepare documents: %v", err)
	}
	stmtMap, err := tx.Prepare(`INSERT OR REPLACE INTO thin_vault_fts_map(rowid, doc_id) VALUES (?, ?)`)
	if err != nil {
		t.Fatalf("prepare map: %v", err)
	}
	stmtFTS, err := tx.Prepare(`INSERT INTO thin_vault_docs_fts(rowid, raw_text) VALUES (?, ?)`)
	if err != nil {
		t.Fatalf("prepare FTS: %v", err)
	}
	for idx, it := range sgmItems {
		rowID := int64(idx + 1)
		if _, err := stmtDoc.Exec(it.ItemID, it.OccurredAt, it.FullText); err != nil {
			t.Fatalf("stmtDoc %s: %v", it.ItemID, err)
		}
		if _, err := stmtMap.Exec(rowID, it.ItemID); err != nil {
			t.Fatalf("stmtMap %s: %v", it.ItemID, err)
		}
		if _, err := stmtFTS.Exec(rowID, it.FullText); err != nil {
			t.Fatalf("stmtFTS %s: %v", it.ItemID, err)
		}
	}
	if err := stmtDoc.Close(); err != nil {
		t.Fatalf("close stmtDoc: %v", err)
	}
	if err := stmtMap.Close(); err != nil {
		t.Fatalf("close stmtMap: %v", err)
	}
	if err := stmtFTS.Close(); err != nil {
		t.Fatalf("close stmtFTS: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit atm sqlite: %v", err)
	}
	var docs, mappings, fts int
	if err := atmDB.QueryRow("SELECT COUNT(*) FROM thin_vault_docs").Scan(&docs); err != nil {
		t.Fatal(err)
	}
	if err := atmDB.QueryRow("SELECT COUNT(*) FROM thin_vault_fts_map").Scan(&mappings); err != nil {
		t.Fatal(err)
	}
	if err := atmDB.QueryRow("SELECT COUNT(*) FROM thin_vault_docs_fts").Scan(&fts); err != nil {
		t.Fatal(err)
	}
	if docs != len(sgmItems) || mappings != len(sgmItems) || fts != len(sgmItems) || len(byID) != len(sgmItems) {
		t.Fatalf("corpus/vault mismatch: items=%d ids=%d docs=%d map=%d fts=%d", len(sgmItems), len(byID), docs, mappings, fts)
	}
	atmIngestSec := time.Since(t0Ingest).Seconds()
	t.Logf("[ATM SQLite FTS5+BM25] Ingested %d SGM items in %.2fs", len(sgmItems), atmIngestSec)

	// 读取已有的纯云端 SDK 结果（若存在）用于做 Cloud + SQLite FTS5/BM25 RRF 混合对比
	cloudHardIDs := map[string][]string{}
	if rawCloud, err := os.ReadFile(filepath.Join(os.Getenv("ATM_RUN_DIR"), "nonexistent-cloud-input")); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(rawCloud)), "\n") {
			var row map[string]any
			if json.Unmarshal([]byte(line), &row) == nil {
				qid := fmt.Sprint(row["id"])
				if rids, ok := row["retrieval_ids"].([]any); ok {
					for _, rid := range rids {
						cloudHardIDs[qid] = append(cloudHardIDs[qid], fmt.Sprint(rid))
					}
				}
			}
		}
	}

	evalATMTier := func(tierName string, qas []ATMQAItem, proto string) map[string]any {
		type qRes struct {
			r1, r5, r10, r25, r50 float64
			evHit, allHit, aic    bool
			latMS, chars, tokens  float64
			qtype                 string
		}
		results := make([]qRes, len(qas))
		evalOne := func(idx int, q ATMQAItem) {
			qStart := time.Now()
			var retIDs []string
			switch proto {
			case "pure-fts5-bm25":
				retIDs, _ = queryVaultPureFTS5BM25(atmVault, q.Question, 50)
			case "sdk-default-fts5-bm25-pulse":
				hits, queryErr := atmVault.SearchClientSDKDefault(q.Question, 50)
				if queryErr != nil {
					t.Errorf("query %s: %v", q.ID, queryErr)
					return
				}
				for _, h := range hits {
					retIDs = append(retIDs, h.UnitID)
				}
			case "cloud-sdk-only":
				retIDs = cloudHardIDs[q.ID]
			case "hybrid-cloud-plus-fts5-bm25":
				hits, queryErr := atmVault.SearchClientSDKDefault(q.Question, 50)
				if queryErr != nil {
					t.Errorf("query %s: %v", q.ID, queryErr)
					return
				}
				var localIDs []string
				for _, h := range hits {
					localIDs = append(localIDs, h.UnitID)
				}
				retIDs = fuseRRF(cloudHardIDs[q.ID], localIDs, 50)
			}
			latMS := float64(time.Since(qStart).Microseconds()) / 1000.0

			recMap := ComputeATMRecallMap(q.EvidenceIDs, retIDs)
			r1, r5, r10, r25, r50 := recMap["R@1"], recMap["R@5"], recMap["R@10"], recMap["R@25"], recMap["R@50"]
			evHit := len(q.EvidenceIDs) > 0 && r10 > 0
			allHit := len(q.EvidenceIDs) > 0 && r10 >= 1.0-1e-9
			if dumpPath := strings.TrimSpace(os.Getenv("DUMP_RETRIEVAL_ROWS_DIR")); dumpPath != "" && proto == "sdk-default-fts5-bm25-pulse" && os.Getenv("ONLY_SDK_CANONICAL_HARMONICS") == "1" {
				dumpRetrievalRow(dumpPath, tierName, q, retIDs, recMap, evHit)
			}
			_, formattedLines := FormatATMEvidencePayload(q.Question, retIDs, byID, 10)
			ctxText := strings.Join(formattedLines, "\n")
			aic := TokenRecall(q.Answer, ctxText) >= 0.8

			results[idx] = qRes{
				r1: r1, r5: r5, r10: r10, r25: r25, r50: r50,
				evHit: evHit, allHit: allHit, aic: aic,
				latMS:  latMS,
				chars:  float64(len([]rune(ctxText))),
				tokens: float64(tokenzr.Count(ctxText)),
				qtype:  q.QType,
			}
		}

		if len(qas) > 200 {
			workers := 8
			var wg sync.WaitGroup
			ch := make(chan int, len(qas))
			for i := range qas {
				ch <- i
			}
			close(ch)
			for w := 0; w < workers; w++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for idx := range ch {
						evalOne(idx, qas[idx])
					}
				}()
			}
			wg.Wait()
		} else {
			for idx, q := range qas {
				evalOne(idx, q)
			}
		}

		var sumR1, sumR5, sumR10, sumR25, sumR50, sumHit10, sumAllHit10, sumAIC float64
		var sumLatencyMS, sumTokens, sumChars float64
		catHit10 := map[string]float64{}
		catAllHit10 := map[string]float64{}
		catR10 := map[string]float64{}
		catN := map[string]int{}
		for _, r := range results {
			sumR1 += r.r1
			sumR5 += r.r5
			sumR10 += r.r10
			sumR25 += r.r25
			sumR50 += r.r50
			if r.evHit {
				sumHit10 += 1.0
				catHit10[r.qtype] += 1.0
			}
			if r.allHit {
				sumAllHit10 += 1.0
				catAllHit10[r.qtype] += 1.0
			}
			if r.aic {
				sumAIC += 1.0
			}
			sumLatencyMS += r.latMS
			sumChars += r.chars
			sumTokens += r.tokens
			catN[r.qtype]++
			catR10[r.qtype] += r.r10
		}
		n := float64(len(qas))
		byCat := map[string]any{}
		for k, cnt := range catN {
			byCat[k] = map[string]any{
				"n":          cnt,
				"r_at_10":    PyRound(catR10[k]/float64(cnt), 4),
				"hit_10":     PyRound(catHit10[k]/float64(cnt), 4),
				"all_hit_10": PyRound(catAllHit10[k]/float64(cnt), 4),
			}
		}
		return map[string]any{
			"tier":              tierName,
			"protocol":          proto,
			"recall_metric":     "gold-set-mean",
			"questions":         len(qas),
			"r_at_1":            PyRound(sumR1/n, 4),
			"r_at_5":            PyRound(sumR5/n, 4),
			"r_at_10":           PyRound(sumR10/n, 4),
			"r_at_25":           PyRound(sumR25/n, 4),
			"r_at_50":           PyRound(sumR50/n, 4),
			"evidence_hit_10":   PyRound(sumHit10/n, 4),
			"all_hit_10":        PyRound(sumAllHit10/n, 4),
			"answer_in_context": PyRound(sumAIC/n, 4),
			"avg_latency_ms":    PyRound(sumLatencyMS/n, 2),
			"avg_tokenzr":       PyRound(sumTokens/n, 1),
			"avg_chars":         PyRound(sumChars/n, 1),
			"by_qtype":          byCat,
		}
	}

	atmResults := map[string]any{
		"corpus_items":                  len(sgmItems),
		"sqlite_ingest_sec":             PyRound(atmIngestSec, 2),
		"official_corpus_pins_verified": true,
		"corpus_sha256":                 PinATMCorpusSHA256,
		"questions_1013_sha256":         PinATM1013SHA256,
		"questions_hard_sha256":         PinATMHardSHA256,
	}
	if os.Getenv("ONLY_SDK_CANONICAL_HARMONICS") != "1" {
		atmResults["hard_31_pure_fts5_bm25"] = evalATMTier("hard-31", hardQAs, "pure-fts5-bm25")
		atmResults["hard_31_sdk_default_fts5_bm25"] = evalATMTier("hard-31", hardQAs, "sdk-default-fts5-bm25-pulse")
		atmResults["hard_31_cloud_sdk_only"] = evalATMTier("hard-31", hardQAs, "cloud-sdk-only")
		atmResults["hard_31_hybrid_cloud_fts5_bm25"] = evalATMTier("hard-31", hardQAs, "hybrid-cloud-plus-fts5-bm25")
		atmResults["main_1013_pure_fts5_bm25"] = evalATMTier("main-1013", main1013QAs, "pure-fts5-bm25")
		atmResults["main_1013_sdk_default_fts5_bm25"] = evalATMTier("main-1013", main1013QAs, "sdk-default-fts5-bm25-pulse")
	}
	// Enable Shannon BPE Prior + Proportional Stem/Morph Harmonics + SDK Structured Date Beacons on local offline Vault
	atmVault.SetCanonicalHarmonics(ramesdk.CanonicalHarmonicOptions{EnableStructuredDates: true})
	atmVault.SetSegmenterOptions(ramesdk.SegmenterPackOptions{
		SentenceAtomPacker: packLocalOfflineShannonSentence,
		HarmonicOverlay:    true,
	})
	t0Warmup := time.Now()
	if _, err := atmVault.SearchClientSDKDefault("warmup", 10); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	atmResults["warmup_ms"] = PyRound(float64(time.Since(t0Warmup).Microseconds())/1000.0, 2)
	atmResults["hard_31_shannon_bpe_offline"] = evalATMTier("hard-31", hardQAs, "sdk-default-fts5-bm25-pulse")
	if os.Getenv("SKIP_MAIN_1013") != "1" {
		atmResults["main_1013_shannon_bpe_offline"] = evalATMTier("main-1013", main1013QAs, "sdk-default-fts5-bm25-pulse")
	}
	if atmDumpFailures.Load() != 0 {
		t.Fatalf("retrieval row write failures: %d", atmDumpFailures.Load())
	}
	atmVault.SetSegmenterOptions(ramesdk.SegmenterPackOptions{})
	atmVault.SetCanonicalHarmonics(ramesdk.CanonicalHarmonicOptions{})
	report["atm_bench"] = atmResults

	if os.Getenv("SKIP_LOCOMO") == "1" {
		outBytes, _ := json.MarshalIndent(report, "", "  ")
		if err := os.WriteFile(filepath.Join(os.Getenv("ATM_RUN_DIR"), "test-report.json"), outBytes, 0o644); err != nil {
			t.Fatalf("write report: %v", err)
		}
		t.Logf("\n%s", string(outBytes))
		return
	}

	// =========================================================================
	// Part 2: LoCoMo (5,882 条对话轮次 · Tier-100)
	// =========================================================================
	locomoDir, err := ResolveDataDir(repoEvalData)
	if err != nil {
		t.Fatalf("ResolveDataDir(locomo): %v", err)
	}
	locManifest, err := LoadManifest(locomoDir)
	if err != nil {
		t.Fatalf("LoadManifest(locomo): %v", err)
	}
	tier100, err := locManifest.Tier("100")
	if err != nil {
		t.Fatalf("locManifest.Tier(100): %v", err)
	}
	locQuestions, err := LoadQuestions(filepath.Join(locomoDir, tier100.File), 0, 100)
	if err != nil {
		t.Fatalf("LoadQuestions(locomo-100): %v", err)
	}
	locCorpus, err := LoadCorpus(filepath.Join(locomoDir, locManifest.Corpus.Path), 0)
	if err != nil {
		t.Fatalf("LoadCorpus(locomo): %v", err)
	}

	locDBPath := filepath.Join(t.TempDir(), "locomo_vault.sqlite")
	locDB, err := sql.Open("sqlite", locDBPath)
	if err != nil {
		t.Fatalf("open locomo sqlite: %v", err)
	}
	defer locDB.Close()
	_, _ = locDB.Exec(`PRAGMA synchronous = OFF; PRAGMA journal_mode = MEMORY;`)

	locVault, err := thinclient.NewVault(locDB, []byte("locomo-eval-fts5-bm25-key"))
	if err != nil {
		t.Fatalf("NewVault(locomo): %v", err)
	}

	t0LocIngest := time.Now()
	locTx, _ := locDB.Begin()
	locStmtDoc, _ := locTx.Prepare(`INSERT OR REPLACE INTO thin_vault_docs(doc_id, layer, occurred_at, raw_text, sentences_json, address_uri, coord64, waveform_bar, doc_macro_wave, kept_indices_json, assistant_id, incarnation_id, locus) VALUES (?, 'episodic', ?, ?, '[]', '', 0, '', '', '[]', 'default', '', '')`)
	locStmtMap, _ := locTx.Prepare(`INSERT OR REPLACE INTO thin_vault_fts_map(rowid, doc_id) VALUES (?, ?)`)
	locStmtFTS, _ := locTx.Prepare(`INSERT INTO thin_vault_docs_fts(rowid, raw_text) VALUES (?, ?)`)
	locTextByID := make(map[string]string, len(locCorpus))
	locScope := "u10-mem-eval-locomo"
	var locRowID int64
	for _, row := range locCorpus {
		text := pyStrip(pyStr(firstTruthy(row["text"], "")))
		if text == "" {
			continue
		}
		locator := pyStr(firstTruthy(row["locator"], ""))
		session := pyStr(firstTruthy(row["session"], firstTruthy(strings.Split(locator, "#")[0], "default")))
		turn := firstTruthy(row["id"], 0)
		occ, _ := pyInt(firstTruthy(row["occurred_at"], 0))
		docID := "locomo-" + ids.SHA256Hex(locScope + "\x00" + session + "\x00" + pyStr(turn))[:24]
		locTextByID[docID] = text
		locRowID++
		_, _ = locStmtDoc.Exec(docID, occ, text)
		_, _ = locStmtMap.Exec(locRowID, docID)
		_, _ = locStmtFTS.Exec(locRowID, text)
	}
	_ = locStmtDoc.Close()
	_ = locStmtMap.Close()
	_ = locStmtFTS.Close()
	if err := locTx.Commit(); err != nil {
		t.Fatalf("commit locomo sqlite: %v", err)
	}
	locIngestSec := time.Since(t0LocIngest).Seconds()
	t.Logf("[LoCoMo SQLite FTS5+BM25] Ingested %d turns in %.2fs", len(locTextByID), locIngestSec)

	evalLoCoMoProto := func(proto string, topK int) map[string]any {
		var sumEvHit, sumFullEvHit, sumAIC, sumEvRecall, sumGoldRecall float64
		var sumLatMS, sumTokens, sumChars float64
		catHit := map[string]float64{}
		catN := map[string]int{}

		for _, q := range locQuestions {
			qText := questionText(q)
			gold := goldAnswer(q)
			evTexts := evidenceTexts(q)
			cat := fmt.Sprint(q["category"])

			qStart := time.Now()
			var retIDs []string
			switch proto {
			case "pure-fts5-bm25":
				retIDs, _ = queryVaultPureFTS5BM25(locVault, qText, topK)
			case "sdk-default-fts5-bm25-pulse":
				hits, _ := locVault.SearchClientSDKDefault(qText, topK)
				for _, h := range hits {
					retIDs = append(retIDs, h.UnitID)
				}
			}
			latMS := float64(time.Since(qStart).Microseconds()) / 1000.0
			sumLatMS += latMS

			lines := make([]string, 0, len(retIDs))
			for _, id := range retIDs {
				if txt := locTextByID[id]; txt != "" {
					lines = append(lines, txt)
				}
			}
			ctxText := TruncateRunes(joinLines(lines), 24576)
			evRec := BestEvidenceRecall(evTexts, ctxText)
			fullEvRec := BestFullEvidenceRecall(evTexts, ctxText)
			goldRec := FullTokenRecall(gold, ctxText)
			evHit := evRec >= 0.6
			fullEvHit := fullEvRec >= 0.6
			aic := goldRec >= 0.8

			sumEvRecall += evRec
			sumGoldRecall += goldRec
			if evHit {
				sumEvHit += 1.0
				catHit[cat] += 1.0
			}
			if fullEvHit {
				sumFullEvHit += 1.0
			}
			if aic {
				sumAIC += 1.0
			}
			catN[cat]++
			sumChars += float64(len([]rune(ctxText)))
			sumTokens += float64(tokenzr.Count(ctxText))
		}
		n := float64(len(locQuestions))
		byCat := map[string]any{}
		for k, cnt := range catN {
			byCat[k] = map[string]any{
				"n":            cnt,
				"evidence_hit": PyRound(catHit[k]/float64(cnt), 4),
			}
		}
		return map[string]any{
			"protocol":             proto,
			"top_k":                topK,
			"questions":            len(locQuestions),
			"evidence_hit":         PyRound(sumEvHit/n, 4),
			"full_evidence_hit":    PyRound(sumFullEvHit/n, 4),
			"best_evidence_recall": PyRound(sumEvRecall/n, 4),
			"answer_in_context":    PyRound(sumAIC/n, 4),
			"answer_token_recall":  PyRound(sumGoldRecall/n, 4),
			"avg_latency_ms":       PyRound(sumLatMS/n, 2),
			"avg_tokenzr":          PyRound(sumTokens/n, 1),
			"avg_chars":            PyRound(sumChars/n, 1),
			"by_category":          byCat,
		}
	}

	locResults := map[string]any{
		"corpus_turns":                      len(locTextByID),
		"sqlite_ingest_sec":                 PyRound(locIngestSec, 2),
		"tier100_pure_fts5_bm25_k96":        evalLoCoMoProto("pure-fts5-bm25", 96),
		"tier100_sdk_default_fts5_bm25_k96": evalLoCoMoProto("sdk-default-fts5-bm25-pulse", 96),
	}
	report["locomo"] = locResults

	outBytes, _ := json.MarshalIndent(report, "", "  ")
	if err := os.WriteFile(filepath.Join(os.Getenv("ATM_RUN_DIR"), "test-report.json"), outBytes, 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	t.Logf("\n%s", string(outBytes))
}

func TestLocalOfflineEngineATME2E(t *testing.T) {
	if os.Getenv("RUN_LOCAL_OFFLINE_E2E") != "1" {
		t.Skip("set RUN_LOCAL_OFFLINE_E2E=1 to run local offline engine E2E ATM evaluation")
	}
	apiKey := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_EVAL_API_KEY"))
	baseURL := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_EVAL_BASE_URL"))
	model := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_EVAL_MODEL"))
	if model == "" {
		model = "deepseek-v4.1-flash"
	}
	pick := func(envKey, fallback string) string {
		if v := strings.TrimSpace(os.Getenv(envKey)); v != "" {
			return v
		}
		return fallback
	}
	answerBaseURL := pick("LOCAL_OFFLINE_ANSWER_BASE_URL", baseURL)
	answerAPIKey := pick("LOCAL_OFFLINE_ANSWER_API_KEY", apiKey)
	answerModel := pick("LOCAL_OFFLINE_ANSWER_MODEL", model)
	judgeBaseURL := pick("LOCAL_OFFLINE_JUDGE_BASE_URL", baseURL)
	judgeAPIKey := pick("LOCAL_OFFLINE_JUDGE_API_KEY", apiKey)
	judgeModel := pick("LOCAL_OFFLINE_JUDGE_MODEL", model)
	judgeEffort := strings.TrimSpace(os.Getenv("LOCAL_OFFLINE_JUDGE_REASONING_EFFORT"))
	answerThinking := strings.EqualFold(strings.TrimSpace(os.Getenv("LOCAL_OFFLINE_ANSWER_THINKING")), "true")
	if answerAPIKey == "" || answerBaseURL == "" || judgeAPIKey == "" || judgeBaseURL == "" {
		t.Skip("requires answer/judge endpoint + key (LOCAL_OFFLINE_{ANSWER,JUDGE}_BASE_URL/API_KEY or ROLLING_MEMORY_EVAL_*)")
	}

	repoEvalData, _ := filepath.Abs(filepath.Join("..", "..", DataDirName))
	atmDir, err := ResolveATMDataDir(filepath.Join(repoEvalData, "atmbench"))
	if err != nil {
		t.Fatalf("ResolveATMDataDir: %v", err)
	}
	if err := VerifyATMCorpusPins(atmDir); err != nil {
		t.Fatalf("VerifyATMCorpusPins: %v", err)
	}
	officialPinsVerified := true
	judgeStar := judgeModel != "gpt-5-mini"
	outDir := pick("LOCAL_OFFLINE_OUT_DIR", "/tmp")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir LOCAL_OFFLINE_OUT_DIR: %v", err)
	}
	noCache := strings.EqualFold(strings.TrimSpace(os.Getenv("LOCAL_OFFLINE_NO_CACHE")), "1")
	workers := 24
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("LOCAL_OFFLINE_WORKERS"))); err == nil && n > 0 {
		workers = n
	}
	limit := 0
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("LOCAL_OFFLINE_LIMIT"))); err == nil && n > 0 {
		limit = n
	}
	tierWanted := func(name string) bool {
		raw := strings.TrimSpace(os.Getenv("LOCAL_OFFLINE_TIERS"))
		if raw == "" {
			return true
		}
		for _, part := range strings.Split(raw, ",") {
			part = strings.ToLower(strings.TrimSpace(part))
			if part == name || (name == "1013" && part == "main") {
				return true
			}
		}
		return false
	}
	sgmItems, byID, _, err := LoadATMSGMItems(atmDir, ATMScope, 0)
	if err != nil {
		t.Fatalf("LoadATMSGMItems: %v", err)
	}
	hardQAs, err := LoadATMQAItems(atmDir, TierATMHard, 0, 0)
	if err != nil {
		t.Fatalf("LoadATMQAItems(hard): %v", err)
	}
	main1013QAs, err := LoadATMQAItems(atmDir, TierATM1013, 0, 0)
	if err != nil {
		t.Fatalf("LoadATMQAItems(1013): %v", err)
	}

	atmDBPath := filepath.Join(t.TempDir(), "atm_offline_e2e.sqlite")
	atmDB, err := sql.Open("sqlite", atmDBPath)
	if err != nil {
		t.Fatalf("open atm sqlite: %v", err)
	}
	defer atmDB.Close()
	_, _ = atmDB.Exec(`PRAGMA synchronous = OFF; PRAGMA journal_mode = MEMORY;`)

	atmVault, err := thinclient.NewVault(atmDB, []byte("atm-eval-fts5-bm25-key"))
	if err != nil {
		t.Fatalf("NewVault(atm): %v", err)
	}
	atmVault.SetCanonicalHarmonics(ramesdk.CanonicalHarmonicOptions{EnableStructuredDates: true})
	atmVault.SetSegmenterOptions(ramesdk.SegmenterPackOptions{
		SentenceAtomPacker: packLocalOfflineShannonSentence,
		HarmonicOverlay:    true,
	})
	tx, _ := atmDB.Begin()
	stmtDoc, _ := tx.Prepare(`INSERT OR REPLACE INTO thin_vault_docs(doc_id, layer, occurred_at, raw_text, sentences_json, address_uri, coord64, waveform_bar, doc_macro_wave, kept_indices_json, assistant_id, incarnation_id, locus) VALUES (?, 'episodic', ?, ?, '[]', '', 0, '', '', '[]', 'default', '', '')`)
	stmtMap, _ := tx.Prepare(`INSERT OR REPLACE INTO thin_vault_fts_map(rowid, doc_id) VALUES (?, ?)`)
	stmtFTS, _ := tx.Prepare(`INSERT INTO thin_vault_docs_fts(rowid, raw_text) VALUES (?, ?)`)
	for idx, it := range sgmItems {
		rowID := int64(idx + 1)
		_, _ = stmtDoc.Exec(it.ItemID, it.OccurredAt, it.FullText)
		_, _ = stmtMap.Exec(rowID, it.ItemID)
		_, _ = stmtFTS.Exec(rowID, it.FullText)
	}
	_ = stmtDoc.Close()
	_ = stmtMap.Close()
	_ = stmtFTS.Close()
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit atm sqlite: %v", err)
	}
	// Warm up the vault's in-memory slot hash cache once
	_, _ = atmVault.SearchClientSDKDefault("warmup", 10)

	cfg := testConfig(t)
	cfg.Mode = "cloud"
	ansClient, err := NewChatClient(cfg, ChatOptions{
		// 答器：默认思考开 + max_tokens 不设帽；Ollama/Qwen 本地端点建议
		// LOCAL_OFFLINE_ANSWER_THINKING=0（非推理模型）并把超时抬到 180s。
		BaseURL: answerBaseURL, Model: answerModel, APIKey: answerAPIKey, Temperature: 0.0, MaxTokens: 0, TimeoutSec: 180, Thinking: answerThinking,
	})
	if err != nil {
		t.Fatalf("NewChatClient(ans): %v", err)
	}
	jdgClient, err := NewChatClient(cfg, ChatOptions{
		BaseURL: judgeBaseURL, Model: judgeModel, APIKey: judgeAPIKey, Temperature: 0.0, MaxTokens: 4096, TimeoutSec: 90, Thinking: false, ReasoningEffort: judgeEffort,
	})
	if err != nil {
		t.Fatalf("NewChatClient(jdg): %v", err)
	}

	runTierE2E := func(tierName string, qas []ATMQAItem) map[string]any {
		type cachedEval struct {
			ctxKey         string
			prediction     string
			judgeCorrect   any
			judgeReason    string
			qsScore        float64
			qsScored       bool
			answerErr      string
			unscoredReason string
		}
		if limit > 0 && len(qas) > limit {
			qas = qas[:limit]
		}
		prevByQID := map[string]cachedEval{}
		if !noCache {
			if rawPrev, err := os.ReadFile(filepath.Join(outDir, "results-atm-"+tierName+"-local-offline-llm.jsonl")); err == nil {
				for _, line := range strings.Split(strings.TrimSpace(string(rawPrev)), "\n") {
					if strings.TrimSpace(line) == "" {
						continue
					}
					var row map[string]any
					if json.Unmarshal([]byte(line), &row) == nil {
						qid := fmt.Sprint(row["id"])
						scored, _ := row["qs_scored"].(bool)
						pred := strings.TrimSpace(fmt.Sprint(row["prediction"]))
						if !scored || pred == "" || pred == "<nil>" {
							continue
						}
						var fLines []string
						if arr, ok := row["formatted_items"].([]any); ok {
							for _, it := range arr {
								fLines = append(fLines, fmt.Sprint(it))
							}
						}
						sc, _ := row["qs_score"].(float64)
						prevByQID[qid] = cachedEval{
							ctxKey:       strings.Join(fLines, "\n"),
							prediction:   pred,
							judgeCorrect: row["judge_correct"],
							judgeReason:  fmt.Sprint(row["judge_reason"]),
							qsScore:      sc,
							qsScored:     scored,
						}
					}
				}
			}
		}

		type preRetrieved struct {
			retIDs []string
			recSec float64
		}
		pre := make([]preRetrieved, len(qas))
		for idx, q := range qas {
			t0 := time.Now()
			hits, _ := atmVault.SearchClientSDKDefault(q.Question, 50)
			retIDs := make([]string, 0, len(hits))
			for _, h := range hits {
				retIDs = append(retIDs, h.UnitID)
			}
			pre[idx] = preRetrieved{retIDs: retIDs, recSec: time.Since(t0).Seconds()}
		}
		rows := ParallelMap(len(qas), workers, func(idx int) map[string]any {
			q := qas[idx]
			retIDs := pre[idx].retIDs
			recSec := pre[idx].recSec
			recMap := ComputeATMRecallMap(q.EvidenceIDs, retIDs)
			r10, r50 := recMap["R@10"], recMap["R@50"]
			evHit := r10 >= 0.5 || (len(q.EvidenceIDs) > 0 && r10 > 0)
			goldSet := make(map[string]bool, len(q.EvidenceIDs))
			for _, id := range q.EvidenceIDs {
				goldSet[id] = true
			}
			goldInTop10 := 0
			for i, id := range retIDs {
				if i >= 10 {
					break
				}
				if goldSet[id] {
					goldInTop10++
				}
			}
			anyHit10 := goldInTop10 > 0
			allHit10 := len(goldSet) > 0 && goldInTop10 == len(goldSet)
			userPayload, formattedLines := FormatATMEvidencePayload(q.Question, retIDs, byID, 10)
			ctxText := strings.Join(formattedLines, "\n")
			evTexts := make([]string, 0, len(q.EvidenceIDs))
			for _, id := range q.EvidenceIDs {
				if it := byID[id]; it != nil {
					evTexts = append(evTexts, it.FullText)
				}
			}
			bestEvRecall := BestEvidenceRecall(evTexts, ctxText)
			fullEvRecall := BestFullEvidenceRecall(evTexts, ctxText)
			goldRecall := TokenRecall(q.Answer, ctxText)
			fullGoldRecall := FullTokenRecall(q.Answer, ctxText)
			aic := goldRecall >= 0.8

			var prediction string
			var judgeCorrect any
			var judgeReason string
			var qsScore float64
			var qsScored bool
			var answerErr string
			var unscoredReason string

			if prev, ok := prevByQID[q.ID]; ok && prev.ctxKey == ctxText {
				prediction = prev.prediction
				judgeCorrect = prev.judgeCorrect
				judgeReason = prev.judgeReason
				qsScore = prev.qsScore
				qsScored = prev.qsScored
				answerErr = prev.answerErr
				unscoredReason = prev.unscoredReason
			} else {
				if ans, aErr := ansClient.CompleteWithRetry(ATMSystemPrompt, userPayload); aErr != nil {
					answerErr = aErr.Error()
				} else {
					prediction = strings.TrimSpace(ans)
				}
				switch {
				case answerErr != "":
					// 答不上 ≠ 答错：链路最终失败时记 unscored，禁止静默判 0 污染 QS 分母。
					unscoredReason = "ANSWER_UNAVAILABLE: " + answerErr
					judgeReason = unscoredReason
				default:
					switch q.QType {
					case "list_recall":
						qsScore = ComputeATMListJaccard(q.Answer, prediction)
						qsScored = true
						judgeCorrect = qsScore >= 0.5
						judgeReason = fmt.Sprintf("ATM official list_recall Jaccard = %.3f", qsScore)
					case "number":
						emOK, reason := ComputeATMNumberEMWithQuestion(q.Answer, prediction, q.Question)
						if emOK {
							qsScore = 1.0
						}
						qsScored = true
						judgeCorrect = emOK
						judgeReason = reason
					default:
						if rawJdg, jErr := jdgClient.CompleteWithRetry("", BuildATMJudgePrompt(q.Question, q.Answer, prediction)); jErr == nil {
							if acc, exp, pErr := ParseATMJudgeResponse(rawJdg); pErr == nil {
								judgeCorrect = acc
								if acc {
									qsScore = 1.0
								}
								qsScored = true
								judgeReason = exp
							} else {
								judgeReason = "JUDGE_UNPARSED"
							}
						} else {
							unscoredReason = "JUDGE_UNAVAILABLE: " + jErr.Error()
							judgeReason = unscoredReason
						}
					}
				}
			}
			f1 := TokenF1(prediction, q.Answer)
			exact := NormalizeAnswer(prediction) != "" && NormalizeAnswer(prediction) == NormalizeAnswer(q.Answer)
			qCat := "4"
			switch q.QType {
			case "list_recall":
				qCat = "1"
			case "number":
				qCat = "2"
			case "open_end":
				qCat = "3"
			}
			verdictStr := ""
			if b, ok := judgeCorrect.(bool); ok {
				if b {
					verdictStr = "correct"
				} else {
					verdictStr = "wrong"
				}
			}
			// Evidence signals stay ID-level (this arm retrieves exact corpus unit IDs, so
			// "retrieved" means the annotated unit is in Top-10/Top-50); the gold-token-recall
			// input is the real answer token recall against the delivered context, never R@25.
			failureBucket := ClassifyFailureBucket(evHit, aic || fullGoldRecall >= 0.8, r50, f1, fullGoldRecall, verdictStr)
			return map[string]any{
				"id": q.ID, "sample_id": q.QType, "category": qCat, "qtype": q.QType,
				"question": q.Question, "gold": q.Answer, "evidence_texts": q.EvidenceIDs,
				"retrieval_ids": retIDs, "retrieval_recall": recMap, "prediction": prediction,
				"f1": PyRound(f1, 4), "exact": exact,
				"evidence_recall": PyRound(r10, 4), "best_evidence_recall": PyRound(bestEvRecall, 4),
				"evidence_hit": evHit, "recall_at_10_any_hit": anyHit10, "recall_at_10_gt": allHit10,
				"full_evidence_recall": PyRound(fullEvRecall, 4), "full_evidence_hit": fullEvRecall >= 0.6,
				"answer_token_recall": PyRound(fullGoldRecall, 4), "answer_in_context": aic,
				"failure_bucket": failureBucket, "judge": verdictStr, "judge_correct": judgeCorrect,
				"judge_reason": judgeReason, "qs_score": PyRound(qsScore, 4), "qs_scored": qsScored,
				"answer_error": answerErr, "unscored_reason": unscoredReason,
				"context_chars": len(ctxText), "recall_seconds": PyRound(recSec, 4),
				"formatted_items": formattedLines,
				// Provenance: the local arm is a local SQLite FTS5/BM25 vault with
				// zero cloud memory-engine traffic. These fields make the artifact
				// self-describing for the submission contract.
				"engine_transport":              "local.sqlite-fts5-bm25",
				"engine_scope":                  "local-eval-temp-vault",
				"engine_upstream":               "local://sqlite-fts5-bm25",
				"pulse_mode":                    PulseModeLocalPlain,
				"local_fallback":                false,
				"cloud_hits":                    0,
				"corpus_sha256":                 PinATMCorpusSHA256,
				"corpus_rows":                   len(sgmItems),
				"official_corpus_pins_verified": officialPinsVerified,
				"scoring_protocol":              ATMOfficialScoringProtocol,
				"scoring_version":               ATMOfficialScoringVersion,
				"scoring_fixture_sha256":        PinATMOfficialScoringFixtureSHA256,
				"scoring_number":                "official deterministic_accuracy",
				"scoring_list":                  "official list_jaccard_score",
				"scoring_judge_parser":          "official parse_judge_response",
				"answer_model":                  answerModel,
				"judge_model":                   judgeModel,
				"judge_star":                    judgeStar,
				"recall_metric":                 "gold-set-mean",
			}
		})
		summary := Aggregate(rows, "atm-"+tierName+"-local-offline-llm")
		gtCount, anyCount := 0, 0
		for _, r := range rows {
			if v, ok := r["recall_at_10_gt"].(bool); ok && v {
				gtCount++
			}
			if v, ok := r["recall_at_10_any_hit"].(bool); ok && v {
				anyCount++
			}
		}
		summary["recall_at_10_gt_rate"] = PyRound(float64(gtCount)/float64(len(rows)), 4)
		summary["recall_at_10_any_hit_rate"] = PyRound(float64(anyCount)/float64(len(rows)), 4)
		// unscored 分母显式化：答案/判官链路重试耗尽后记 unscored，既不计入 qs 分母也不隐藏。
		scoredCount, unscoredCount, answerErrCount := 0, 0, 0
		for _, r := range rows {
			if v, ok := r["qs_scored"].(bool); ok && v {
				scoredCount++
			} else {
				unscoredCount++
			}
			if s, ok := r["answer_error"].(string); ok && s != "" {
				answerErrCount++
			}
		}
		summary["scored_count"] = scoredCount
		summary["unscored_count"] = unscoredCount
		summary["answer_error_count"] = answerErrCount
		qsSum := 0.0
		for _, r := range rows {
			if v, ok := r["qs_score"].(float64); ok {
				qsSum += v
			}
		}
		if scoredCount > 0 {
			summary["qs_score_scored_only"] = PyRound(qsSum/float64(scoredCount), 4)
		}
		if len(rows) > 0 {
			summary["qs_score_all_denominator"] = PyRound(qsSum/float64(len(rows)), 4)
		}
		summary["engine_transport"] = "local.sqlite-fts5-bm25"
		summary["engine_scope"] = "local-eval-temp-vault"
		summary["engine_upstream"] = "local://sqlite-fts5-bm25"
		summary["pulse_mode"] = PulseModeLocalPlain
		summary["local_fallback"] = false
		summary["cloud_hits"] = 0
		summary["corpus_sha256"] = PinATMCorpusSHA256
		summary["official_corpus_pins_verified"] = officialPinsVerified
		summary["scoring_protocol"] = ATMOfficialScoringProtocol
		summary["scoring_version"] = ATMOfficialScoringVersion
		summary["scoring_fixture_sha256"] = PinATMOfficialScoringFixtureSHA256
		summary["answer_model"] = answerModel
		summary["judge_model"] = judgeModel
		// 推理参数入证：同一 model 在不同 thinking / 帽下来自不同臂，读数不可横比。
		summary["answer_max_tokens"] = 0
		summary["answer_thinking"] = true
		summary["judge_max_tokens"] = 4096
		summary["judge_thinking"] = false
		summary["judge_star"] = judgeStar
		summary["answer_base_url"] = answerBaseURL
		summary["judge_base_url"] = judgeBaseURL
		summary["answer_thinking"] = answerThinking
		summary["judge_reasoning_effort"] = judgeEffort
		summary["limit"] = limit
		summary["no_cache"] = noCache
		summary["recall_metric"] = "gold-set-mean"
		var sumR1, sumR5, sumR10, sumR25, sumR50, sumQS, sumTok float64
		qsCnt := 0
		catQSSum := map[string]float64{}
		catQSCnt := map[string]int{}
		for _, r := range rows {
			rm := r["retrieval_recall"].(map[string]float64)
			sumR1 += rm["R@1"]
			sumR5 += rm["R@5"]
			sumR10 += rm["R@10"]
			sumR25 += rm["R@25"]
			sumR50 += rm["R@50"]
			if r["qs_scored"].(bool) {
				sc := r["qs_score"].(float64)
				sumQS += sc
				qsCnt++
				qt := r["qtype"].(string)
				catQSSum[qt] += sc
				catQSCnt[qt]++
			}
			lines := r["formatted_items"].([]string)
			sumTok += float64(tokenzr.Count(strings.Join(lines, "\n")))
		}
		n := float64(len(rows))
		byQTypeQS := map[string]any{}
		for qt, cnt := range catQSCnt {
			byQTypeQS[qt] = PyRound(catQSSum[qt]/float64(cnt), 4)
		}
		summary["r_at_1"] = PyRound(sumR1/n, 4)
		summary["r_at_5"] = PyRound(sumR5/n, 4)
		summary["r_at_10"] = PyRound(sumR10/n, 4)
		summary["r_at_25"] = PyRound(sumR25/n, 4)
		summary["r_at_50"] = PyRound(sumR50/n, 4)
		if qsCnt > 0 {
			summary["qs_score"] = PyRound(sumQS/float64(qsCnt), 4)
		}
		summary["by_qtype_qs"] = byQTypeQS
		summary["avg_tokenzr"] = PyRound(sumTok/n, 1)
		_ = writeResults(filepath.Join(outDir, "results-atm-"+tierName+"-local-offline-llm.jsonl"), rows)
		return summary
	}

	e2eOut := map[string]any{}
	if tierWanted("hard") {
		e2eOut["hard_31"] = runTierE2E("hard", hardQAs)
	}
	if tierWanted("1013") {
		e2eOut["main_1013"] = runTierE2E("1013", main1013QAs)
	}
	b, _ := json.MarshalIndent(e2eOut, "", "  ")
	_ = os.WriteFile(filepath.Join(outDir, "local-offline-atm-e2e.json"), b, 0o644)
	t.Logf("\n%s", string(b))
}

func TestLocalOfflineEngineLoCoMoFullAndE2E(t *testing.T) {
	if os.Getenv("RUN_LOCAL_OFFLINE_LOCOMO") != "1" {
		t.Skip("set RUN_LOCAL_OFFLINE_LOCOMO=1 to run local offline engine LoCoMo baseline")
	}
	repoEvalData, _ := filepath.Abs(filepath.Join("..", "..", DataDirName))
	locomoDir, err := ResolveDataDir(repoEvalData)
	if err != nil {
		t.Fatalf("ResolveDataDir(locomo): %v", err)
	}
	locManifest, err := LoadManifest(locomoDir)
	if err != nil {
		t.Fatalf("LoadManifest(locomo): %v", err)
	}
	locCorpus, err := LoadCorpus(filepath.Join(locomoDir, locManifest.Corpus.Path), 0)
	if err != nil {
		t.Fatalf("LoadCorpus(locomo): %v", err)
	}

	locDBPath := filepath.Join(t.TempDir(), "locomo_offline.sqlite")
	locDB, err := sql.Open("sqlite", locDBPath)
	if err != nil {
		t.Fatalf("open locomo sqlite: %v", err)
	}
	defer locDB.Close()
	_, _ = locDB.Exec(`PRAGMA synchronous = OFF; PRAGMA journal_mode = MEMORY;`)

	locVault, err := thinclient.NewVault(locDB, []byte("locomo-eval-fts5-bm25-key"))
	if err != nil {
		t.Fatalf("NewVault(locomo): %v", err)
	}
	t0Ingest := time.Now()
	locTx, _ := locDB.Begin()
	locStmtDoc, _ := locTx.Prepare(`INSERT OR REPLACE INTO thin_vault_docs(doc_id, layer, occurred_at, raw_text, sentences_json, address_uri, coord64, waveform_bar, doc_macro_wave, kept_indices_json, assistant_id, incarnation_id, locus) VALUES (?, 'episodic', ?, ?, '[]', '', 0, '', '', '[]', 'default', '', '')`)
	locStmtMap, _ := locTx.Prepare(`INSERT OR REPLACE INTO thin_vault_fts_map(rowid, doc_id) VALUES (?, ?)`)
	locStmtFTS, _ := locTx.Prepare(`INSERT INTO thin_vault_docs_fts(rowid, raw_text) VALUES (?, ?)`)
	locTextByID := make(map[string]string, len(locCorpus))
	locScope := "u10-mem-eval-locomo"
	sessionLineCursor := make(map[string]int, 64)
	var locRowID int64
	for _, row := range locCorpus {
		text := pyStrip(pyStr(firstTruthy(row["text"], "")))
		if text == "" {
			continue
		}
		author := pyStrip(pyStr(firstTruthy(row["author"], firstTruthy(row["speaker"], ""))))
		locator := pyStr(firstTruthy(row["locator"], ""))
		session := pyStr(firstTruthy(row["session"], firstTruthy(strings.Split(locator, "#")[0], "default")))
		turn := firstTruthy(row["id"], 0)
		occ, _ := pyInt(firstTruthy(row["occurred_at"], 0))
		docID := "locomo-" + ids.SHA256Hex(locScope + "\x00" + session + "\x00" + pyStr(turn))[:24]
		lineCount := strings.Count(text, "\n") + 1
		if lineCount < 1 {
			lineCount = 1
		}
		lineStart := sessionLineCursor[session] + 1
		lineEnd := lineStart + lineCount - 1
		sessionLineCursor[session] = lineEnd
		blockID := locator
		if blockID == "" {
			blockID = session + "#" + pyStr(turn)
		}
		cdoc := CloudDocument{
			ID: docID, Text: text, Author: author, OccurredAt: occ, NarratedAt: occ,
			Layer: ramesdk.LayerEpisodic, DocKey: session, FileURL: session,
			BlockKey: blockID, BlockID: blockID, LineStart: lineStart, LineEnd: lineEnd,
			SubUnits: locomoTurnSubUnits(docID, text, row, lineStart, lineEnd),
		}
		fullText := ramesdk.FormatPlaintextWithMeta(cdoc.PackedText(), cdoc.UnitMeta())
		locTextByID[docID] = fullText
		locRowID++
		_, _ = locStmtDoc.Exec(docID, occ, fullText)
		_, _ = locStmtMap.Exec(locRowID, docID)
		_, _ = locStmtFTS.Exec(locRowID, fullText)
	}
	_ = locStmtDoc.Close()
	_ = locStmtMap.Close()
	_ = locStmtFTS.Close()
	if err := locTx.Commit(); err != nil {
		t.Fatalf("commit locomo sqlite: %v", err)
	}
	ingestSec := time.Since(t0Ingest).Seconds()
	_, _ = locVault.SearchClientSDKDefault("warmup", 10)

	evalLoCoMoRetrieval := func(tierName string, topK int) (map[string]any, []map[string]any, []contextRow) {
		tierSpec, err := locManifest.Tier(tierName)
		if err != nil {
			t.Fatalf("Tier(%s): %v", tierName, err)
		}
		questions, err := LoadQuestions(filepath.Join(locomoDir, tierSpec.File), 0, 0)
		if err != nil {
			t.Fatalf("LoadQuestions(%s): %v", tierName, err)
		}
		ctxRows := make([]contextRow, len(questions))
		var sumEvHit, sumFullEvHit, sumAIC, sumEvRec, sumFullEvRec, sumGoldRec, sumLatMS, sumTok, sumChars float64
		catHit := map[string]float64{}
		catFullHit := map[string]float64{}
		catN := map[string]int{}
		for i, q := range questions {
			qText := questionText(q)
			gold := goldAnswer(q)
			evTexts := evidenceTexts(q)
			cat := fmt.Sprint(q["category"])

			qStart := time.Now()
			hits, _ := locVault.SearchClientSDKDefault(qText, topK)
			latMS := float64(time.Since(qStart).Microseconds()) / 1000.0
			sumLatMS += latMS

			lines := make([]string, 0, len(hits))
			unitIDs := make([]string, 0, len(hits))
			for _, h := range hits {
				if txt := locTextByID[h.UnitID]; txt != "" {
					lines = append(lines, txt)
					unitIDs = append(unitIDs, h.UnitID)
				}
			}
			ctxText := TruncateRunes(joinLines(lines), 24576)
			ctxRows[i] = contextRow{
				question: q,
				context:  ctxText,
				items:    Itemize(ctxText, 0),
				counts:   map[string]int{"raw": len(lines), "cloud_hits": len(lines), "context_chars": len([]rune(ctxText))},
				seconds:  latMS / 1000.0,
				unitIDs:  unitIDs,
			}
			evRec := BestEvidenceRecall(evTexts, ctxText)
			fullEvRec := BestFullEvidenceRecall(evTexts, ctxText)
			goldRec := FullTokenRecall(gold, ctxText)
			evHit := evRec >= 0.6
			fullEvHit := fullEvRec >= 0.6
			aic := goldRec >= 0.8

			sumEvRec += evRec
			sumFullEvRec += fullEvRec
			sumGoldRec += goldRec
			if evHit {
				sumEvHit++
				catHit[cat]++
			}
			if fullEvHit {
				sumFullEvHit++
				catFullHit[cat]++
			}
			if aic {
				sumAIC++
			}
			catN[cat]++
			sumChars += float64(len([]rune(ctxText)))
			sumTok += float64(tokenzr.Count(ctxText))
		}
		n := float64(len(questions))
		byCat := map[string]any{}
		for k, cnt := range catN {
			byCat[k] = map[string]any{
				"n":                 cnt,
				"head_evidence_hit": PyRound(catHit[k]/float64(cnt), 4),
				"full_evidence_hit": PyRound(catFullHit[k]/float64(cnt), 4),
			}
		}
		return map[string]any{
			"tier":                 tierName,
			"top_k":                topK,
			"questions":            len(questions),
			"head_evidence_hit":    PyRound(sumEvHit/n, 4),
			"full_evidence_hit":    PyRound(sumFullEvHit/n, 4),
			"best_evidence_recall": PyRound(sumFullEvRec/n, 4),
			"answer_in_context":    PyRound(sumAIC/n, 4),
			"answer_token_recall":  PyRound(sumGoldRec/n, 4),
			"avg_latency_ms":       PyRound(sumLatMS/n, 2),
			"avg_tokenzr":          PyRound(sumTok/n, 1),
			"avg_chars":            PyRound(sumChars/n, 1),
			"by_category":          byCat,
		}, questions, ctxRows
	}

	r100, _, _ := evalLoCoMoRetrieval("100", 96)
	r200, _, _ := evalLoCoMoRetrieval("200", 96)
	r1540, q1540, ctx1540 := evalLoCoMoRetrieval("1540", 96)

	out := map[string]any{
		"corpus_turns":      len(locTextByID),
		"sqlite_ingest_sec": PyRound(ingestSec, 2),
		"tier_100":          r100,
		"tier_200":          r200,
		"tier_1540":         r1540,
	}
	bInit, _ := json.MarshalIndent(out, "", "  ")
	_ = os.WriteFile("/tmp/local-offline-locomo-baseline.json", bInit, 0o644)

	apiKey := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_EVAL_API_KEY"))
	baseURL := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_EVAL_BASE_URL"))
	model := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_EVAL_MODEL"))
	if model == "" {
		model = "deepseek-v4.1-flash"
	}
	if apiKey != "" && baseURL != "" {
		cfg := testConfig(t)
		cfg.Mode = "cloud"
		anser, jdger, err := buildProtocols(cfg, Options{
			Answerer: "llm", Judge: "llm", BaseURL: baseURL, Model: model, APIKey: apiKey,
			Temperature: 0.0, JudgeTemp: 0.0, MaxTokens: 1024, TimeoutSec: 45, Thinking: false,
		})
		if err != nil {
			t.Fatalf("buildProtocols: %v", err)
		}
		resFile := "/tmp/results-locomo-1540-local-offline-llm.jsonl"
		existingByID := map[string]map[string]any{}
		if rawBytes, err := os.ReadFile(resFile); err == nil {
			for _, line := range strings.Split(strings.TrimSpace(string(rawBytes)), "\n") {
				if line == "" {
					continue
				}
				var r map[string]any
				if json.Unmarshal([]byte(line), &r) == nil {
					if id := fmt.Sprint(r["id"]); id != "" && r["judge"] != "error" && r["judge"] != "unparsed" {
						existingByID[id] = r
					}
				}
			}
		}
		fAppend, _ := os.OpenFile(resFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		var appendMu sync.Mutex

		results := ParallelMap(len(q1540), 48, func(idx int) map[string]any {
			row := ctx1540[idx]
			qid := fmt.Sprint(row.question["id"])
			if ex, ok := existingByID[qid]; ok {
				return ex
			}
			gold := goldAnswer(row.question)
			evTexts := evidenceTexts(row.question)
			ans, aErr := anser.Answer(questionText(row.question), row.items)
			pred := strings.TrimSpace(ans)
			verdict := "wrong"
			reason := "empty or error"
			if aErr == nil && pred != "" {
				if v, jErr := jdger.Judge(questionText(row.question), gold, pred); jErr != nil {
					verdict = "unparsed"
					reason = jErr.Error()
				} else if v.Correct {
					verdict = "correct"
					reason = v.Reason
				} else {
					verdict = "wrong"
					reason = v.Reason
				}
			}
			f1 := TokenF1(pred, gold)
			exact := NormalizeAnswer(pred) == NormalizeAnswer(gold)
			evRec := BestEvidenceRecall(evTexts, row.context)
			fullEvRec := BestFullEvidenceRecall(evTexts, row.context)
			goldRec := FullTokenRecall(gold, row.context)
			evHit := evRec >= 0.6
			fullEvHit := fullEvRec >= 0.6 || (len(evTexts) == 0 && goldRec >= 0.8)
			aic := goldRec >= 0.8
			bucket := ClassifyFailureBucket(fullEvHit, aic, fullEvRec, f1, goldRec, verdict)
			rMap := map[string]any{
				"id": row.question["id"], "sample_id": row.question["sample_id"],
				"category": row.question["category"], "question": row.question["question"],
				"gold": gold, "prediction": pred, "context": row.context,
				"f1": PyRound(f1, 4), "exact": exact,
				"evidence_hit": evHit, "full_evidence_hit": fullEvHit, "answer_in_context": aic,
				"best_evidence_recall": PyRound(evRec, 4), "full_evidence_recall": PyRound(fullEvRec, 4),
				"answer_token_recall": PyRound(goldRec, 4), "failure_bucket": bucket,
				"judge": verdict, "judge_reason": reason,
				"context_chars": row.counts["context_chars"], "raw": row.counts["raw"],
				"recall_seconds": PyRound(row.seconds, 4),
			}
			if fAppend != nil {
				if bLine, err := json.Marshal(rMap); err == nil {
					appendMu.Lock()
					_, _ = fAppend.Write(append(bLine, '\n'))
					appendMu.Unlock()
				}
			}
			return rMap
		})
		if fAppend != nil {
			_ = fAppend.Close()
		}
		_ = writeResults(resFile, results)
		locSummary := Aggregate(results, "locomo-1540-local-offline-llm")
		for k, v := range judgeTotals(results) {
			locSummary[k] = v
		}
		out["tier_1540_e2e_llm"] = locSummary
	}

	b, _ := json.MarshalIndent(out, "", "  ")
	_ = os.WriteFile("/tmp/local-offline-locomo-baseline.json", b, 0o644)
	t.Logf("\n%s", string(b))
}
