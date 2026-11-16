// plugin: kb v1.0.0
package embed

import (
	"strings"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/chunk"
)

func strPtr(s string) *string { return &s }

// snapshotOf 按给定层生成有效配置快照（chunk 包单点）
func snapshotOf(t *testing.T, layer string) string {
	t.Helper()
	cfg, err := chunk.Effective([]byte(layer))
	if err != nil {
		t.Fatalf("生成配置快照失败: %v", err)
	}
	return cfg.Canonical()
}

func TestEmbedTextOfHeadingPrefix(t *testing.T) {
	// heading_prefix=true（缺省档）且标题路径在场 ⇒ 前缀进派生文本
	row := chunkRow{
		ID: 1, Text: "正文第一段",
		HeadingPath:    strPtr(`["合同","第一条"]`),
		ConfigSnapshot: strPtr(snapshotOf(t, `{"heading_prefix":true}`)),
	}
	got, err := embedTextOf(row)
	if err != nil {
		t.Fatalf("派生失败: %v", err)
	}
	if want := "合同 > 第一条\n正文第一段"; got != want {
		t.Fatalf("派生文本不符:\n got=%q\nwant=%q", got, want)
	}
}

func TestEmbedTextOfHeadingPrefixOff(t *testing.T) {
	row := chunkRow{
		ID: 2, Text: "正文",
		HeadingPath:    strPtr(`["标题"]`),
		ConfigSnapshot: strPtr(snapshotOf(t, `{"heading_prefix":false}`)),
	}
	got, err := embedTextOf(row)
	if err != nil {
		t.Fatalf("派生失败: %v", err)
	}
	if got != "正文" {
		t.Fatalf("heading_prefix=false 应不含前缀: %q", got)
	}
}

func TestEmbedTextOfTableHeader(t *testing.T) {
	row := chunkRow{
		ID:          3,
		Text:        "张三,30",
		TableMeta:   strPtr(`{"header":["姓名","年龄"],"row_from":1,"row_to":1}`),
		HeadingPath: nil,
	}
	got, err := embedTextOf(row)
	if err != nil {
		t.Fatalf("派生失败: %v", err)
	}
	if want := "姓名,年龄\n张三,30"; got != want {
		t.Fatalf("表头重复不符:\n got=%q\nwant=%q", got, want)
	}
}

func TestEmbedTextOfBrokenSnapshotLoudFail(t *testing.T) {
	row := chunkRow{ID: 4, Text: "正文", ConfigSnapshot: strPtr(`{"unknown_key":1}`)}
	if _, err := embedTextOf(row); err == nil {
		t.Fatal("未知配置键应响亮失败（口径未知不得派生）")
	}
	row = chunkRow{ID: 5, Text: "正文", ConfigSnapshot: strPtr(`{bad json`)}
	if _, err := embedTextOf(row); err == nil {
		t.Fatal("非法快照应响亮失败")
	}
}

func TestFormatEmbeddingText(t *testing.T) {
	got := formatEmbedding([]float32{0.5, -1, 0.25})
	if got != "[0.5,-1,0.25]" {
		t.Fatalf("pgvector 文本形态不符: %s", got)
	}
	if !strings.HasPrefix(formatEmbedding(nil), "[") {
		t.Fatal("空向量应输出 []（防御）")
	}
}

func TestNormalizeOptions(t *testing.T) {
	o := normalizeOptions(Options{})
	if o.Agent != DefaultAgent || o.Batch != DefaultBatch || o.MaxChars != DefaultMaxChars {
		t.Fatalf("缺省规整不符: %+v", o)
	}
	o = normalizeOptions(Options{Batch: 999, MaxChars: -1})
	if o.Batch != MaxBatch {
		t.Fatalf("批大小应收敛到上限 %d: %d", MaxBatch, o.Batch)
	}
	if o.MaxChars != 0 {
		t.Fatalf("负 max_chars 应解释为不截断（0）: %d", o.MaxChars)
	}
}

func TestVectorKey(t *testing.T) {
	if got := vectorKey(7, "abc"); got != "7:abc" {
		t.Fatalf("已嵌集合键不符: %s", got)
	}
}
