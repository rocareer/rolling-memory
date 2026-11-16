// plugin: kb v1.0.0
package chunk

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/config"
)

// testConfig 经三层单点解析取有效配置（层 JSON 直接给）
func testConfig(t *testing.T, layer string) Config {
	t.Helper()
	cfg, err := Effective([]byte(layer))
	if err != nil {
		t.Fatalf("Effective(%s) 失败: %v", layer, err)
	}
	return cfg
}

// repoRoot 仓库根（向上找 go.mod）
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("未找到 go.mod（仓库根）")
		}
		dir = parent
	}
}

func TestConfigDefaultAndDerivation(t *testing.T) {
	base := Default()
	if base.Target != 512 || base.Overlap != 64 || base.HardCap != 1024 {
		t.Fatalf("缺省档应为 512/64/1024，实际 %d/%d/%d", base.Target, base.Overlap, base.HardCap)
	}

	// 未给 tail_merge ⇒ 按 target/4 派生
	cfg := testConfig(t, `{"target":1024}`)
	if cfg.TailMerge != 256 {
		t.Fatalf("tail_merge 应派生为 target/4=256，实际 %d", cfg.TailMerge)
	}
	// 显式 0 ⇒ 不合并（0 是合法值，不得被零值语义吞掉）
	cfg = testConfig(t, `{"tail_merge":0}`)
	if cfg.TailMerge != 0 {
		t.Fatalf("显式 tail_merge=0 应保留，实际 %d", cfg.TailMerge)
	}
	// 显式 overlap=0 同样保留
	cfg = testConfig(t, `{"overlap":0}`)
	if cfg.Overlap != 0 {
		t.Fatalf("显式 overlap=0 应保留，实际 %d", cfg.Overlap)
	}
}

func TestConfigThreeLayerShallowOverride(t *testing.T) {
	global, err := Effective([]byte(`{"target":512,"overlap":64,"heading_prefix":true,"levels":[1,2,3]}`))
	if err != nil {
		t.Fatalf("global 层: %v", err)
	}
	libLayer := []byte(`{"target":1024,"rows_per_chunk":10}`)
	docLayer := []byte(`{"header_repeat":false}`)
	cfg, err := EffectiveOn(global, libLayer, docLayer)
	if err != nil {
		t.Fatalf("三层合并失败: %v", err)
	}
	if cfg.Target != 1024 || cfg.Overlap != 64 {
		t.Fatalf("library 层覆盖 target 应生效（1024/64），实际 %d/%d", cfg.Target, cfg.Overlap)
	}
	if cfg.TailMerge != 256 { // global 未给 tail_merge ⇒ 按最终 target/4
		t.Fatalf("tail_merge 应按最终 target 派生 256，实际 %d", cfg.TailMerge)
	}
	if cfg.RowsPerChunk != 10 || cfg.HeaderRepeat {
		t.Fatalf("library/document 层键应生效，实际 rows=%d header_repeat=%v", cfg.RowsPerChunk, cfg.HeaderRepeat)
	}
	// 后层优先（document 覆盖 library）
	cfg2, err := EffectiveOn(global, libLayer, []byte(`{"target":768}`))
	if err != nil {
		t.Fatalf("三层合并（后层优先）失败: %v", err)
	}
	if cfg2.Target != 768 {
		t.Fatalf("document 层应覆盖 library 层 target，实际 %d", cfg2.Target)
	}
}

func TestConfigUnknownKeyRejected(t *testing.T) {
	_, err := Effective([]byte(`{"target":512,"chunk_size":256}`))
	if err == nil || !strings.Contains(err.Error(), "chunk_size") {
		t.Fatalf("未知键必须响亮拒绝并点名，实际 err=%v", err)
	}
	if e := ValidateLayer([]byte(`{"targt":512}`)); e == nil {
		t.Fatal("写侧单层校验须拒绝未知键")
	}
	if e := ValidateLayer([]byte(`{"target":256}`)); e != nil {
		t.Fatalf("合法单层不应报错: %v", e)
	}
}

func TestConfigValueDomainRejected(t *testing.T) {
	cases := []string{
		`{"target":99}`,
		`{"target":5000}`,
		`{"overlap":64,"target":64}`,
		`{"hard_cap":256,"target":512}`,
		`{"threshold":0}`,
		`{"threshold":1.5}`,
		`{"levels":[1,7]}`,
		`{"levels":[]}`,
		`{"min":4096,"max":512}`,
		`{"rows_per_chunk":0}`,
		`{"min_rows":10,"rows_per_chunk":5}`,
		`{"strategy":"nope"}`,
		`{"window":0}`,
		`{"heading_prefix":"yes"}`,
	}
	for _, c := range cases {
		if _, err := Effective([]byte(c)); err == nil {
			t.Fatalf("值域外/类型错配置必须拒绝: %s", c)
		}
	}
	// levels 去重升序归一
	cfg := testConfig(t, `{"levels":[6,2,2,1]}`)
	if len(cfg.Levels) != 3 || cfg.Levels[0] != 1 || cfg.Levels[1] != 2 || cfg.Levels[2] != 6 {
		t.Fatalf("levels 应升序去重 [1 2 6]，实际 %v", cfg.Levels)
	}
}

func TestConfigCanonicalHashAndAffectsChunk(t *testing.T) {
	a := testConfig(t, `{"target":512,"overlap":64}`)
	b := testConfig(t, `{"overlap":64,"target":512}`) // 键序无关
	if a.Canonical() != b.Canonical() || a.Hash() != b.Hash() {
		t.Fatalf("同值配置的规范形态与哈希应一致：%s / %s", a.Canonical(), b.Canonical())
	}
	if len(a.Hash()) != 40 {
		t.Fatalf("config_hash 应为 sha1 十六进制 40 位，实际 %q", a.Hash())
	}
	if a.AffectsChunk(b) {
		t.Fatal("同值配置不应触发重切")
	}
	c := testConfig(t, `{"target":256}`)
	if !a.AffectsChunk(c) {
		t.Fatal("chunk-affecting 键（target）变更应触发重切")
	}
}

func TestGlobalFromYAML(t *testing.T) {
	if err := config.Init(); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	cfg, err := Global()
	if err != nil {
		t.Fatalf("Global: %v", err)
	}
	def := Default()
	if cfg.Target != def.Target || cfg.Overlap != def.Overlap || cfg.HardCap != def.HardCap {
		t.Fatalf("config/kb.yaml 全局档应与代码缺省一致：%d/%d/%d", cfg.Target, cfg.Overlap, cfg.HardCap)
	}
	if !cfg.HeadingPrefix || !cfg.HeaderRepeat {
		t.Fatalf("config/kb.yaml 的 heading_prefix/header_repeat 应为 true，实际 %v/%v", cfg.HeadingPrefix, cfg.HeaderRepeat)
	}
	if len(cfg.Levels) != 6 {
		t.Fatalf("levels 应为 1..6，实际 %v", cfg.Levels)
	}
	if cfg.TailMerge != 128 {
		t.Fatalf("tail_merge 应为 128，实际 %d", cfg.TailMerge)
	}
}

func TestFinalizeAnchorAndVerify(t *testing.T) {
	docLoc := "kb://doc/42"
	if got := Anchor(docLoc, 3); got != "kb://doc/42#p3" {
		t.Fatalf("锚形态错误: %s", got)
	}
	base, seq, ok := ParseAnchor("kb://doc/42#p3")
	if !ok || base != docLoc || seq != 3 {
		t.Fatalf("锚反解析失败: %s %d %v", base, seq, ok)
	}
	for _, bad := range []string{"", "kb://doc/42", "kb://doc/42#p0", "kb://doc/42#px", "#p1"} {
		if _, _, ok := ParseAnchor(bad); ok {
			t.Fatalf("非法锚应解析失败: %q", bad)
		}
	}

	text := "第一段\n\n第二段"
	rs := []rune(text)
	chunks := []Chunk{
		{CharStart: 0, CharEnd: 3, Text: string(rs[0:3])},
		{CharStart: 4, CharEnd: 4, Text: ""}, // 空白块：Finalize 剔除
		{CharStart: 5, CharEnd: 8, Text: string(rs[5:8])},
	}
	kept, err := Finalize(docLoc, chunks)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if len(kept) != 2 {
		t.Fatalf("Finalize 应剔除空白块留 2 块，实际 %d", len(kept))
	}
	if kept[0].Seq != 1 || kept[0].Locator != docLoc+"#p1" {
		t.Fatalf("Finalize 未规整 seq/locator: %+v", kept[0])
	}
	if kept[1].Seq != 2 || kept[1].Locator != docLoc+"#p2" {
		t.Fatalf("Finalize 未规整 seq/locator: %+v", kept[1])
	}
	if kept[0].TextHash != TextHash("第一段") || len(kept[0].TextHash) != 40 {
		t.Fatalf("text_hash 口径错误: %q", kept[0].TextHash)
	}
	if err := VerifyVerbatim(text, kept); err != nil {
		t.Fatalf("锚逐字校验: %v", err)
	}

	// 空 locator / 全空白 ⇒ 响亮报错
	if _, err := Finalize("", kept); err == nil {
		t.Fatal("空 doc_locator 必须报错（锚须指向父单元）")
	}
	if _, err := Finalize(docLoc, []Chunk{{CharStart: 0, CharEnd: 0, Text: "  "}}); err == nil {
		t.Fatal("零文本必须报错（禁静默入库）")
	}
	// 篡改 text_hash ⇒ 复算不一致
	broken := []Chunk{{CharStart: 0, CharEnd: 3, Text: "第一段", TextHash: TextHash("别的")}}
	if err := VerifyWithin(text, broken); err == nil {
		t.Fatal("text_hash 复算不一致必须报错")
	}
	// 非逐字（改写字面）⇒ VerifyVerbatim 报错、VerifyWithin 通过
	rewritten := []Chunk{{CharStart: 0, CharEnd: 3, Text: "第一斷", TextHash: TextHash("第一斷")}}
	if err := VerifyWithin(text, rewritten); err != nil {
		t.Fatalf("VerifyWithin 应只看区间与哈希: %v", err)
	}
	if err := VerifyVerbatim(text, rewritten); err == nil {
		t.Fatal("非逐字必须被 VerifyVerbatim 拦下")
	}
	// 区间越界
	if err := VerifyWithin(text, []Chunk{{CharStart: 0, CharEnd: 99, Text: "x", TextHash: TextHash("x")}}); err == nil {
		t.Fatal("区间越界必须报错")
	}
}

func TestRegistryAndRouting(t *testing.T) {
	for _, name := range []string{"fixed", "heading", "table", "code", "semantic", "dialog", "page"} {
		ch, err := Get(name)
		if err != nil {
			t.Fatalf("策略 %s 应已登记: %v", name, err)
		}
		if ch.Name() != name || ch.Version() == "" {
			t.Fatalf("策略登记不自洽: %s / %s", ch.Name(), ch.Version())
		}
	}
	if _, err := Get("unknownfmt"); err == nil {
		t.Fatal("未登记策略必须响亮报错")
	}
	if !ValidStrategy("fixed") || !ValidStrategy("dialog") || !ValidStrategy("page") || ValidStrategy("unknown") {
		t.Fatal("策略闭集判定错误")
	}

	routes := map[string]string{
		"md": "heading", ".md": "heading", "HTML": "heading", "docx": "heading", "pdf": "heading",
		"txt": "fixed", "text": "fixed", "csv": "table", "tsv": "table",
		"go": "code", "py": "code", "yaml": "code",
		// 图片/扫描件 → page；音频/视频 → dialog（类级键与扩展名同路由）
		"image": "page", "png": "page", "jpg": "page", "jpeg": "page",
		"audio": "dialog", "mp3": "dialog", "wav": "dialog",
		"video": "dialog", "mp4": "dialog", "mov": "dialog",
	}
	for in, want := range routes {
		got, err := RouteFormat(in)
		if err != nil || got != want {
			t.Fatalf("路由 %q 应为 %s，实际 %s err=%v", in, want, got, err)
		}
	}
	for _, in := range []string{"exe", "bin", "unknownfmt", ""} {
		if _, err := RouteFormat(in); err == nil {
			t.Fatalf("未登记格式 %q 必须响亮报错（禁静默兜底）", in)
		}
	}
	if got := RouteTXT("随便一段话\n没有标题结构"); got != "fixed" {
		t.Fatalf("无标题 txt 应走 fixed，实际 %s", got)
	}
	if got := RouteTXT("标题\n====\n正文"); got != "heading" {
		t.Fatalf("检出 setext 标题的 txt 应升 heading，实际 %s", got)
	}
}

func TestStrategyVersionAndBuildHash(t *testing.T) {
	v, err := StrategyVersion("fixed", "")
	if err != nil || v != "v1" {
		t.Fatalf("一般策略版本: %s %v", v, err)
	}
	if _, err := StrategyVersion("semantic", ""); err == nil {
		t.Fatal("semantic 缺 agent 身份必须报错（C4 agent 版本入锚）")
	}
	v, err = StrategyVersion("semantic", "kb-embed@3")
	if err != nil || v != "v1+kb-embed@3" {
		t.Fatalf("semantic 版本应并入 agent 身份: %s %v", v, err)
	}
	if _, err := StrategyVersion("nope", ""); err == nil {
		t.Fatal("未登记策略必须报错")
	}

	bh, err := BuildHash(ContentSHA256([]byte("原文")), "cfg", "v1", "kb-text@1")
	if err != nil || len(bh) != 40 {
		t.Fatalf("build_hash 形态错误: %s %v", bh, err)
	}
	bh2, _ := BuildHash(ContentSHA256([]byte("原文")), "cfg", "v1", "kb-text@1")
	if bh != bh2 {
		t.Fatal("同输入 build_hash 必须相同")
	}
	bh3, _ := BuildHash(ContentSHA256([]byte("原文")), "cfg2", "v1", "kb-text@1")
	if bh == bh3 {
		t.Fatal("config_hash 变更必须换 build")
	}
	for _, miss := range [][4]string{{"", "c", "v", "e"}, {"d", "", "v", "e"}, {"d", "c", "", "e"}, {"d", "c", "v", ""}} {
		if _, err := BuildHash(miss[0], miss[1], miss[2], miss[3]); err == nil {
			t.Fatalf("缺分量必须报错: %v", miss)
		}
	}
}

func TestChunkTextOf(t *testing.T) {
	cfg := testConfig(t, `{"heading_prefix":true,"header_repeat":true}`)
	c := Chunk{
		Text:        "正文",
		HeadingPath: []string{"一级", "二级"},
		TableMeta:   &TableMeta{Header: []string{"a", "b"}},
	}
	want := "一级 > 二级\na,b\n正文"
	if got := ChunkTextOf(c, cfg); got != want {
		t.Fatalf("派生文本单点口径错误:\n got=%q\nwant=%q", got, want)
	}
	off := testConfig(t, `{"heading_prefix":false,"header_repeat":false}`)
	if got := ChunkTextOf(c, off); got != "正文" {
		t.Fatalf("关闭前缀后应只剩正文，实际 %q", got)
	}
}

// TestRowTextOf 行级复算单点（kb_chunk 行四列 → ChunkTextOf 口径；embed 嵌入派生与
// aigo kb reindex 回填同源）：jsonb 缺席 = 无前缀；快照缺失 = 代码缺省档；JSON/快照非法 = 响亮报错。
func TestRowTextOf(t *testing.T) {
	heading := `["一级","二级"]`
	meta := `{"header":["a","b"],"row_from":1,"row_to":2,"columns":2}`

	// ① 派生前缀齐全（标题路径 + 表头 + 正文；快照显式开）
	got, err := RowTextOf("正文", &heading, &meta, strPtr(`{"heading_prefix":true,"header_repeat":true}`))
	if err != nil || got != "一级 > 二级\na,b\n正文" {
		t.Fatalf("行级复算口径错误: got=%q err=%v", got, err)
	}
	// ② 快照缺失 ⇒ 代码缺省档（heading_prefix/header_repeat 缺省 true）
	got, err = RowTextOf("正文", &heading, &meta, nil)
	if err != nil || got != "一级 > 二级\na,b\n正文" {
		t.Fatalf("缺快照应按代码缺省档复算: got=%q err=%v", got, err)
	}
	// ③ jsonb 缺席（NULL/空）⇒ 无前缀（仅正文）
	if got, err = RowTextOf("正文", nil, nil, nil); err != nil || got != "正文" {
		t.Fatalf("无结构列应只剩正文: got=%q err=%v", got, err)
	}
	empty := ""
	if got, err = RowTextOf("正文", &empty, &empty, nil); err != nil || got != "正文" {
		t.Fatalf("空 jsonb 应视为缺席: got=%q err=%v", got, err)
	}
	// ④ 关闭前缀的快照 ⇒ 不前置
	got, err = RowTextOf("正文", &heading, &meta, strPtr(`{"heading_prefix":false,"header_repeat":false}`))
	if err != nil || got != "正文" {
		t.Fatalf("快照关闭前缀后应只剩正文: got=%q err=%v", got, err)
	}
	// ⑤ 响亮失败：标题路径 JSON 非法 / 快照非法
	badJSON := `{"oops"`
	if _, err := RowTextOf("正文", &badJSON, nil, nil); err == nil {
		t.Fatal("标题路径 JSON 非法应响亮报错")
	}
	if _, err := RowTextOf("正文", nil, nil, strPtr(`{"target":"abc"}`)); err == nil {
		t.Fatal("配置快照非法应响亮报错（口径未知不得静默复算）")
	}
}

// strPtr 测试用小工具
func strPtr(s string) *string { return &s }

func TestEffectiveOnNilLayers(t *testing.T) {
	base, err := Effective(nil, []byte("  "), []byte(`{"target":256}`))
	if err != nil {
		t.Fatalf("空层应忽略: %v", err)
	}
	if base.Target != 256 {
		t.Fatalf("非空层应生效，实际 %d", base.Target)
	}
}
