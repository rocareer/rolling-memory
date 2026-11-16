// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package eval

import (
	"testing"

	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

// TestRepairLocalPlainStoreBodiesSelfHealsTamperedBodies：暖库续灌的正文完整性闸必须
// 就地删除被替换的单元交回续灌重灌（自愈），放行缺单元（断点续灌常态），
// 并 fail-closed 拒绝语料外孤儿单元。篡改样本复用 localPlainStoreFixture 的
// 写入侧同一拼装函数，保证「期望正文」与真实写入路径同源。
func TestRepairLocalPlainStoreBodiesSelfHealsTamperedBodies(t *testing.T) {
	docs := []CloudDocument{
		// doc-a 走结构化元数据路径（正文带日期前缀）；doc-b 走裸文本路径（store 正文即 PackedText）。
		{ID: "doc-a", Text: "alpha body", OccurredAt: 100, NarratedAt: 100, Layer: "episodic"},
		{ID: "doc-b", Text: "beta body", OccurredAt: 200, Layer: "episodic"},
	}
	engine := &CloudEngine{PulseMode: PulseModeLocalPlain}
	logf := func(string, ...any) {}

	// 正向对照：干净 store 零修复、零报错。
	engine.StorePath = localPlainStoreFixture(t, docs, nil)
	store, err := ramesdk.NewJSONFileStore(engine.StorePath, []byte("repair-probe-key"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	engine.Store = store
	if err := engine.repairLocalPlainStoreBodies(docs, logf); err != nil {
		t.Fatalf("clean store must pass: %v", err)
	}

	// 篡改 1 个正文：必须自愈（从 store 删除、交回续灌重灌），未篡改单元保持原样。
	engine.StorePath = localPlainStoreFixture(t, docs, func(record map[string]map[string]any) {
		record["doc-a"]["raw_text"] = "poisoned body"
	})
	store, err = ramesdk.NewJSONFileStore(engine.StorePath, []byte("repair-probe-key"))
	if err != nil {
		t.Fatalf("open tampered store: %v", err)
	}
	engine.Store = store
	if err := engine.repairLocalPlainStoreBodies(docs, logf); err != nil {
		t.Fatalf("tampered body must be repaired for re-ingest, not rejected: %v", err)
	}
	if _, _, _, err := store.LoadLocalDocument("doc-a"); err == nil {
		t.Fatalf("tampered unit must be removed from store so resume re-ingests it")
	}
	if _, _, _, err := store.LoadLocalDocument("doc-b"); err != nil {
		t.Fatalf("untampered unit must stay in store: %v", err)
	}

	// 孤儿单元（不在当前语料）：fail-closed，绝不静默带入索引。
	engine.StorePath = localPlainStoreFixture(t, docs, func(record map[string]map[string]any) {
		record["doc-orphan"] = map[string]any{"doc_id": "doc-orphan", "occurred_at": 1, "raw_text": "stray body"}
	})
	store, err = ramesdk.NewJSONFileStore(engine.StorePath, []byte("repair-probe-key"))
	if err != nil {
		t.Fatalf("open orphan store: %v", err)
	}
	engine.Store = store
	if err := engine.repairLocalPlainStoreBodies(docs, logf); err == nil {
		t.Fatalf("orphan unit must fail closed")
	}

	// 缺单元（断点续灌场景）：照常放行，由续灌补灌。
	engine.StorePath = localPlainStoreFixture(t, docs, func(record map[string]map[string]any) {
		delete(record, "doc-b")
	})
	store, err = ramesdk.NewJSONFileStore(engine.StorePath, []byte("repair-probe-key"))
	if err != nil {
		t.Fatalf("open partial store: %v", err)
	}
	engine.Store = store
	if err := engine.repairLocalPlainStoreBodies(docs, logf); err != nil {
		t.Fatalf("missing unit must be allowed for resume: %v", err)
	}
}
