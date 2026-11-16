// plugin: kb v1.0.0
package chunk

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// topicEmbedder 假句向量：含「甲」→ 轴 1；否则 → 轴 2（用于制造确定性边界）
func topicEmbedder(calls *int) Embedder {
	return func(_ context.Context, texts []string) ([][]float32, error) {
		*calls++
		out := make([][]float32, len(texts))
		for i, s := range texts {
			if strings.Contains(s, "甲") {
				out[i] = []float32{1, 0}
			} else {
				out[i] = []float32{0, 1}
			}
		}
		return out, nil
	}
}

func TestSemanticSplitsAtTopicBoundary(t *testing.T) {
	sents := []string{
		"甲组第一句，关于切片锚契约与尺寸档的实现细节说明。",
		"甲组第二句，关于文本口径单点与派生文本的说明。",
		"甲组第三句，关于构建身份与配置哈希的口径说明。",
		"甲组第四句，关于重切触发条件的判定说明。",
		"乙组第一句，关于周末出行与晚餐安排的闲谈。",
		"乙组第二句，关于天气变化与带伞提醒的闲谈。",
		"乙组第三句，关于电影排期与购票渠道的闲谈。",
		"乙组第四句，关于假期计划与朋友聚会的闲谈。",
	}
	text := strings.Join(sents, "")
	cfg := testConfig(t, `{"window":3,"threshold":0.25,"min":16,"max":128}`)
	if len([]rune(text)) <= cfg.Max {
		t.Fatalf("测试文本应长于 max 以触发嵌入，实际 %d rune", len([]rune(text)))
	}
	calls := 0
	ch, err := Get("semantic")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	chunks, err := ch.Chunk(context.Background(), Input{
		Text: text, Locator: "kb://doc/1", Config: cfg, Embedder: topicEmbedder(&calls), AgentIdentity: "kb-embed@1",
	})
	if err != nil {
		t.Fatalf("semantic.Chunk: %v", err)
	}
	if calls != 1 {
		t.Fatalf("句向量应一次批量送出，实际调用 %d 次", calls)
	}
	kept, err := Finalize("kb://doc/1", chunks)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if err := VerifyVerbatim(text, kept); err != nil {
		t.Fatalf("锚逐字自检: %v", err)
	}
	if len(kept) != 2 {
		t.Fatalf("话题边界（分位阈值下）应切 2 块，实际 %d 块", len(kept))
	}
	if !strings.Contains(kept[0].Text, "甲组第一句") || strings.Contains(kept[0].Text, "乙组") {
		t.Fatalf("首块应只含甲组，实际 %q", kept[0].Text)
	}
	if !strings.Contains(kept[1].Text, "乙组第一句") {
		t.Fatalf("次块应从乙组起，实际 %q", kept[1].Text)
	}
}

func TestSemanticShortTextSkipsEmbedding(t *testing.T) {
	text := "短文一句。短句二句。"
	cfg := testConfig(t, `{"max":128,"min":16}`)
	ch, err := Get("semantic")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	calls := 0
	chunks, err := ch.Chunk(context.Background(), Input{
		Text: text, Locator: "kb://doc/1", Config: cfg, Embedder: topicEmbedder(&calls),
	})
	if err != nil {
		t.Fatalf("短文不应触发嵌入: %v", err)
	}
	if calls != 0 {
		t.Fatalf("短文（≤ max）不应调用嵌入，实际 %d 次", calls)
	}
	if len(chunks) != 1 || chunks[0].CharEnd != len([]rune(text)) {
		t.Fatalf("短文应单块覆盖全文，实际 %+v", chunks)
	}
}

func TestSemanticRequiresEmbedderAndValidVectors(t *testing.T) {
	long := strings.Repeat("长句内容甲乙丙丁戊己庚辛。", 10)
	cfg := testConfig(t, `{"max":64,"min":16,"window":3,"threshold":0.25}`)
	ch, err := Get("semantic")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := ch.Chunk(context.Background(), Input{Text: long, Config: cfg}); err == nil {
		t.Fatal("缺 Embedder 必须响亮报错（semantic 缺省关，启用须注入 kb-embed 调用）")
	}
	// 向量条数不符
	bad := func(_ context.Context, texts []string) ([][]float32, error) {
		return [][]float32{{1, 0}}, nil
	}
	if _, err := ch.Chunk(context.Background(), Input{Text: long, Config: cfg, Embedder: bad}); err == nil {
		t.Fatal("向量条数不符必须响亮报错")
	}
	// 嵌入失败透传
	fail := func(_ context.Context, _ []string) ([][]float32, error) { return nil, errors.New("渠道不可用") }
	if _, err := ch.Chunk(context.Background(), Input{Text: long, Config: cfg, Embedder: fail}); err == nil {
		t.Fatal("嵌入失败必须透传报错（禁静默降级）")
	}
}

func TestSemanticMaxCapsSegment(t *testing.T) {
	// 无句边界的长文本 ⇒ 按 max 硬切
	text := strings.Repeat("字", 300)
	cfg := testConfig(t, `{"max":128,"min":16}`)
	ch, err := Get("semantic")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	calls := 0
	chunks, err := ch.Chunk(context.Background(), Input{Text: text, Config: cfg, Embedder: topicEmbedder(&calls)})
	if err != nil {
		t.Fatalf("semantic.Chunk: %v", err)
	}
	if len(chunks) != 3 {
		t.Fatalf("300 rune 无句边界应按 max=128 切 3 块，实际 %d", len(chunks))
	}
	for i, c := range chunks {
		if c.CharEnd-c.CharStart > 128 {
			t.Fatalf("第 %d 块超 max：%d", i+1, c.CharEnd-c.CharStart)
		}
	}
}
