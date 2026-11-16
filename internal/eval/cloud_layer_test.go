// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package eval

import (
	"testing"

	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

// TestATMCloudDocumentsAreEpisodic 守卫评测原始语料的层标注：
// ATM SGM 原始记录（照片/邮件/视频/对话）是评测的 L2 情景语料，必须以 episodic 写入。
// 引擎只认识 background / episodic / noetic 三层，不理解题目/证据/分数等评测语义；
// 评测侧需要的 ID 与判据全部由评测模块自己维护。
func TestATMCloudDocumentsAreEpisodic(t *testing.T) {
	items := []*ATMSGMItem{
		{ItemID: "sgm-1", FullText: "photo caption", OccurredAt: 1730000000},
		{ItemID: "sgm-2", FullText: "email body", OccurredAt: 1730000100},
		nil,
	}
	docs := atmCloudDocuments(items)
	if len(docs) != 2 {
		t.Fatalf("atmCloudDocuments len=%d, want 2 (nil items must be skipped)", len(docs))
	}
	for _, doc := range docs {
		if doc.Layer != ramesdk.LayerEpisodic {
			t.Fatalf("ATM corpus document %q layer=%q, want %q", doc.ID, doc.Layer, ramesdk.LayerEpisodic)
		}
		if doc.ID == "" || doc.Text == "" {
			t.Fatalf("ATM corpus document must keep source id/text: %+v", doc)
		}
	}
}
