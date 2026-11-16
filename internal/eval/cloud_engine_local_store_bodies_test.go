// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package eval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

// localPlainStoreFixture 按写入侧同一拼装函数生成 store 记录；mutate 用于构造篡改样本。
func localPlainStoreFixture(t *testing.T, docs []CloudDocument, mutate func(map[string]map[string]any)) string {
	t.Helper()
	store := make(map[string]map[string]any, len(docs))
	for _, doc := range docs {
		meta := doc.UnitMeta()
		occurredAt := meta.OccurredAt
		if occurredAt <= 0 && meta.NarratedAt > 0 {
			occurredAt = meta.NarratedAt
		}
		text := doc.PackedText()
		if doc.HasStructuredMeta() {
			text = ramesdk.FormatPlaintextWithMeta(text, meta)
		}
		store[doc.ID] = map[string]any{
			"doc_id":      doc.ID,
			"occurred_at": occurredAt,
			"raw_text":    text,
		}
	}
	if mutate != nil {
		mutate(store)
	}
	raw, err := json.Marshal(store)
	if err != nil {
		t.Fatalf("marshal fixture store: %v", err)
	}
	path := filepath.Join(t.TempDir(), "eval-atm-local-plain.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write fixture store: %v", err)
	}
	return path
}

func TestVerifyLocalPlainStoreBodiesRejectsTamperedIndexContent(t *testing.T) {
	docs := []CloudDocument{
		// doc-a 走结构化元数据路径（正文带日期前缀）；doc-b 走裸文本路径（store 正文即 PackedText）。
		{ID: "doc-a", Text: "alpha body", OccurredAt: 100, NarratedAt: 100, Layer: "episodic"},
		{ID: "doc-b", Text: "beta body", OccurredAt: 200, Layer: "episodic"},
	}
	engine := &CloudEngine{PulseMode: PulseModeLocalPlain}

	engine.StorePath = localPlainStoreFixture(t, docs, nil)
	if err := engine.verifyLocalPlainStoreBodies(docs); err != nil {
		t.Fatalf("clean store must pass (positive control): %v", err)
	}

	engine.StorePath = localPlainStoreFixture(t, docs, func(store map[string]map[string]any) {
		store["doc-a"]["raw_text"] = "planted gold answer"
	})
	if err := engine.verifyLocalPlainStoreBodies(docs); err == nil {
		t.Fatal("replaced body under unchanged ID must fail closed")
	}

	engine.StorePath = localPlainStoreFixture(t, docs, func(store map[string]map[string]any) {
		store["doc-b"]["occurred_at"] = int64(999)
	})
	if err := engine.verifyLocalPlainStoreBodies(docs); err == nil {
		t.Fatal("replaced timestamp must fail closed")
	}

	engine.StorePath = localPlainStoreFixture(t, docs, func(store map[string]map[string]any) {
		delete(store, "doc-b")
	})
	if err := engine.verifyLocalPlainStoreBodies(docs); err == nil {
		t.Fatal("missing unit must fail closed")
	}
}
