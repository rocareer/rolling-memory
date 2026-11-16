// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/eval"
)

// runCloudEvalRecall serves the Admin per-question workbench through the same
// strict SDK data plane as rmem eval, without opening the legacy graph store.
func (a *application) runCloudEvalRecall() int {
	cfg, err := config.Load(config.LoadOptions{
		Path:  a.args.StringPtr("config"),
		DB:    a.args.StringPtr("db"),
		Scope: a.args.StringPtr("scope"),
	})
	if err != nil {
		return a.fail(err)
	}
	engine, err := eval.NewCloudEngineForScope(cfg, a.args.String("scope"), cfg.DB)
	if err != nil {
		return a.fail(err)
	}
	if a.args.Flag("stdin_batch") {
		return a.runCloudEvalRecallBatch(engine)
	}
	cue := strings.TrimSpace(a.args.String("cue"))
	if cue == "" {
		return a.fail(errors.New("cloud evaluation recall requires --q"))
	}
	topK := a.args.Int("raw_limit")
	if topK <= 0 {
		topK = 20
	}
	payload, err := cloudEvalRecallPayload(engine, cue, topK)
	if err != nil {
		return a.fail(err)
	}
	if a.args.Flag("json") {
		if err := a.printJSON(payload); err != nil {
			return a.fail(err)
		}
		return 0
	}
	for _, line := range payload["formatted"].([]string) {
		a.printf("%s\n", line)
	}
	return 0
}

// runCloudEvalRecallBatch 在同一个 CloudEngine 上按 stdin JSONL 服务批量评测 recall：
// 引擎（含 .zcsr 零拷贝索引）只打开一次，免去批量跑分逐题 fork 子进程的冷启动重付。
// 行协议与 `recall --stdin-batch`（非评测 scope）一致：每行一请求/一响应、行级容错。
func (a *application) runCloudEvalRecallBatch(engine *eval.CloudEngine) int {
	reader := bufio.NewReaderSize(a.stdin, 1<<20)
	writer := bufio.NewWriter(a.stdout)
	defer writer.Flush()
	enc := json.NewEncoder(writer)
	enc.SetEscapeHTML(false)
	defaultTopK := a.args.Int("raw_limit")
	if defaultTopK <= 0 {
		defaultTopK = 20
	}
	for {
		line, readErr := reader.ReadString('\n')
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			out := map[string]any{}
			var req recallBatchRequest
			if jsonErr := json.Unmarshal([]byte(trimmed), &req); jsonErr != nil {
				out["error"] = "invalid batch request line: " + jsonErr.Error()
			} else {
				if req.ReqID != "" {
					out["req_id"] = req.ReqID
				}
				topK := req.RawLimit
				if topK <= 0 {
					topK = defaultTopK
				}
				if strings.TrimSpace(req.Q) == "" {
					out["error"] = "cloud evaluation recall requires q"
				} else if payload, err := cloudEvalRecallPayload(engine, req.Q, topK); err != nil {
					out["error"] = err.Error()
				} else {
					for key, value := range payload {
						out[key] = value
					}
				}
			}
			if err := enc.Encode(out); err != nil {
				return a.fail(err)
			}
			if err := writer.Flush(); err != nil {
				return a.fail(err)
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return 0
			}
			return a.fail(readErr)
		}
	}
}

// cloudEvalRecallPayload 执行一次严格评测 recall 并组装与单发 --json 完全同构的响应体。
func cloudEvalRecallPayload(engine *eval.CloudEngine, cue string, topK int) (map[string]any, error) {
	hits, err := engine.Recall(context.Background(), cue, topK)
	if err != nil {
		return nil, err
	}
	rawHits := make([]map[string]any, 0, len(hits))
	bands := make([]map[string]any, 0, len(hits))
	formatted := make([]string, 0, len(hits))
	unitIDs := make([]string, 0, len(hits))
	for index, hit := range hits {
		text := hit.DistilledPlaintext
		if strings.TrimSpace(text) == "" {
			text = hit.RawPlaintext
		}
		rawHits = append(rawHits, map[string]any{
			"trace": hit.UnitID, "session": hit.UnitID, "unit_id": hit.UnitID,
			"turn": int64(index + 1), "author": "ramesdk", "occurred_at": hit.OccurredAt,
			"text": text, "score": hit.FinalWeight, "layer": hit.Layer,
		})
		bands = append(bands, map[string]any{
			"id": hit.AddressURI, "kind": "cell_band", "subject": hit.AddressURI,
			"score": hit.FinalWeight, "nodes": []map[string]any{{
				"id": hit.UnitID, "rendered": text, "anchor": hit.UnitID,
				"score": hit.FinalWeight, "layer": hit.Layer,
			}},
		})
		formatted = append(formatted, text)
		unitIDs = append(unitIDs, hit.UnitID)
	}
	meta := engine.Metadata()
	meta["engine_transport"] = eval.TransportForPulseMode(engine.PulseMode)
	meta["engine_scope"] = engine.Scope
	meta["engine_upstream"] = engine.Endpoint
	meta["local_fallback"] = false
	meta["cloud_hits"] = len(hits)
	// 语料摘要属于评测模块自己的证据标签（从本套件 manifest 只读获取）；
	// 记忆引擎不参与、也不需要理解任何评测字段。
	if manifest, manifestErr := eval.ReadCloudManifest(engine.StorePath); manifestErr == nil {
		meta["corpus_sha256"] = manifest.CorpusSHA256
	}
	return map[string]any{
		"scope": engine.Scope, "bands": bands, "raw_hits": rawHits,
		"formatted": formatted, "retrieval_unit_ids": unitIDs, "meta": meta,
	}, nil
}

func rejectRetiredEvalScope(scope string) error {
	trimmed := strings.TrimSpace(scope)
	if strings.Contains(trimmed, ":eval:") || strings.HasPrefix(trimmed, "rm-"+"eval-") {
		return fmt.Errorf("retired or invalid evaluation scope %q; use the SDK-generated <authorized-user>-mem-eval-<suite> scope", scope)
	}
	return nil
}
