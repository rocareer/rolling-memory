// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package eval

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

// TestCloudDocumentSpatialMetadataPacking 守卫 CloudDocument 的空间/结构化元数据面：
//   - UnitMeta() 解析 FileURL/BlockID 与 DocKey/BlockKey 的回退；
//   - PackedText() 把高分辨率自由块并入父单元文本，且已出现的片段不重复追加；
//   - 一个 CloudDocument 恒发一个父单元（不产生任何子 UnitID）。
func TestCloudDocumentSpatialMetadataPacking(t *testing.T) {
	doc := CloudDocument{
		ID:         "sgm-1",
		Text:       "ID: sgm-1\nTimestamp: 2022-01-01\nSummary: groceries",
		OccurredAt: 1641058500,
		Author:     "Alice",
		DocKey:     "mail:2022-01-01",
		BlockKey:   "sgm-1",
		LineStart:  1,
		LineEnd:    3,
		FileParse:  "Subject: delivery",
		SubUnits: []ramesdk.SubUnit{
			{Kind: "header", BlockID: "sgm-1#header", Text: "Subject: delivery\nSummary: groceries", LineStart: 1, LineEnd: 2},
			{Kind: "detail", BlockID: "sgm-1#detail", Text: "Detail: milk and bread", LineStart: 3, LineEnd: 3},
		},
	}
	meta := doc.UnitMeta()
	if meta.FileURL != "mail:2022-01-01" || meta.BlockID != "sgm-1" {
		t.Fatalf("UnitMeta fallback mismatch: fileURL=%q blockID=%q", meta.FileURL, meta.BlockID)
	}
	if meta.Author != "Alice" || meta.OccurredAt != 1641058500 || meta.LineStart != 1 || meta.LineEnd != 3 {
		t.Fatalf("UnitMeta spatial/tri-temporal mismatch: %+v", meta)
	}
	if !doc.HasStructuredMeta() {
		t.Fatal("HasStructuredMeta must report true when optional metadata is present")
	}
	if (CloudDocument{ID: "plain", Text: "plain body"}).HasStructuredMeta() {
		t.Fatal("legacy documents without optional metadata must stay on the plain path")
	}

	packed := doc.PackedText()
	for _, want := range []string{"Subject: delivery", "Detail: milk and bread", "Summary: groceries"} {
		if !strings.Contains(packed, want) {
			t.Fatalf("PackedText missing high-resolution block %q: %q", want, packed)
		}
	}
	// 已包含在正文/前序块中的片段不得重复追加。
	if strings.Count(packed, "Subject: delivery") != 1 {
		t.Fatalf("PackedText duplicated an already-present block: %q", packed)
	}
	// 被高分辨率子块覆盖的语义只保留在父单元内。
	if strings.Contains(packed, "#header") || strings.Contains(packed, "#detail") {
		t.Fatalf("PackedText leaked sub-unit identifiers into plaintext: %q", packed)
	}
}

// TestATMCloudDocumentsCarrySpatialCoordinates 守卫 ATM 适配器的四维空间坐标注入：
// FileURL = 同源场域（atmSessionID）、BlockID = 条目级标识、全文行跨度 1..totalLines，
// 且每条 SGM 恒定对应一个 CloudDocument（/units 与 corpus 一一相等）。
func TestATMCloudDocumentsCarrySpatialCoordinates(t *testing.T) {
	email := &ATMSGMItem{
		ItemID: "email202201010001", Modality: "email", Timestamp: "2022-01-01 17:35:00",
		Subject: "Delivery", Summary: "Groceries arrive tomorrow", Detail: "Para one.\n\nPara two.",
	}
	photo := &ATMSGMItem{
		ItemID: "20220703_210745", Modality: "image", Timestamp: "2022-07-03 21:07:45",
		Location: "West Quay Road", City: "Southampton, United Kingdom",
		ShortCap: "a small airplane", Caption: "plane over the bay", OCRText: "EXIT 12",
		Tags: []string{"plane", "sky"},
	}
	photoPeer := &ATMSGMItem{
		ItemID: "20220703_211500", Modality: "video", Timestamp: "2022-07-03 21:15:00",
		Location: "West Quay Road", City: "Southampton, United Kingdom",
		ShortCap: "boats in the harbour", OCRText: "HARBOUR", Tags: []string{"boat"},
	}
	HydrateATMSGMItem(ATMScope, email)
	HydrateATMSGMItem(ATMScope, photo)
	HydrateATMSGMItem(ATMScope, photoPeer)

	docs := atmCloudDocuments([]*ATMSGMItem{email, photo, photoPeer})
	if len(docs) != 3 {
		t.Fatalf("each SGM item must map to exactly one CloudDocument, got %d", len(docs))
	}
	for _, doc := range docs {
		if doc.FileURL == "" || doc.BlockID == "" {
			t.Fatalf("ATM doc %q missing FileURL/BlockID: %+v", doc.ID, doc)
		}
		if doc.LineStart != 1 || doc.LineEnd != strings.Count(doc.Text, "\n")+1 {
			t.Fatalf("ATM doc %q line span = %d..%d, want 1..%d", doc.ID, doc.LineStart, doc.LineEnd, strings.Count(doc.Text, "\n")+1)
		}
		if len(doc.SubUnits) == 0 {
			t.Fatalf("ATM doc %q carries no high-resolution sub-units: %+v", doc.ID, doc)
		}
		if doc.Layer != ramesdk.LayerEpisodic {
			t.Fatalf("ATM doc %q layer = %q", doc.ID, doc.Layer)
		}
	}
	// 同日同城的媒体共享同一 FileURL（进而在 SDK 侧共享同一 DocHash）⇒ 场域共振。
	if docs[1].FileURL != docs[2].FileURL {
		t.Fatalf("same-day same-city media must share a field domain: %q vs %q", docs[1].FileURL, docs[2].FileURL)
	}
	if docs[1].FileURL == docs[0].FileURL {
		t.Fatalf("mail and media must not share a field domain: %q", docs[1].FileURL)
	}
	// 邮件条目含 Subject+Summary 标题块与多段 Detail 块。
	kinds := make([]string, 0, len(docs[0].SubUnits))
	for _, sub := range docs[0].SubUnits {
		kinds = append(kinds, sub.Kind)
		if sub.LineStart <= 0 || sub.LineEnd < sub.LineStart {
			t.Fatalf("ATM sub-unit %q line span invalid: %d..%d", sub.Kind, sub.LineStart, sub.LineEnd)
		}
	}
	if kinds[0] != "header" {
		t.Fatalf("email sub-units must start with the Subject+Summary header block, got %v", kinds)
	}
	hasDetail := false
	for _, kind := range kinds {
		if strings.HasPrefix(kind, "detail") {
			hasDetail = true
		}
	}
	if !hasDetail {
		t.Fatalf("multi-paragraph Detail must produce detail blocks, got %v", kinds)
	}
}

// TestATMHighResolutionSubUnitsAlignToFullTextLines 是「子块行号锚定父文档真实行位置」的判据：
// email 标题块锚定 FullText 的 Summary 行、Detail 段从 Detail 行起按空行边界逐段推进；
// 媒体场域块跨 Location..Short Caption、OCR/Tags 块跨 OCR..Tags；全部落在父单元
// 1..totalLines 区间内（不再从 1 起合成），且块文本确实取自对应行区间。
func TestATMHighResolutionSubUnitsAlignToFullTextLines(t *testing.T) {
	email := &ATMSGMItem{
		ItemID: "email202201010001", Modality: "email", Timestamp: "2022-01-01 17:35:00",
		Subject: "Delivery", Summary: "Groceries arrive tomorrow",
		Detail: "Para one line one.\nPara one line two.\n\nPara two.",
	}
	media := &ATMSGMItem{
		ItemID: "20220703_210745", Modality: "image", Timestamp: "2022-07-03 21:07:45",
		Location: "West Quay Road", City: "Southampton, United Kingdom",
		ShortCap: "a small airplane", OCRText: "EXIT 12", Tags: []string{"plane", "sky"},
	}
	HydrateATMSGMItem(ATMScope, email)
	HydrateATMSGMItem(ATMScope, media)

	docs := atmCloudDocuments([]*ATMSGMItem{email, media})
	byID := map[string]CloudDocument{}
	for _, doc := range docs {
		byID[doc.ID] = doc
	}
	emailDoc, mediaDoc := byID[email.ItemID], byID[media.ItemID]
	if got := strings.Count(emailDoc.Text, "\n") + 1; got != 7 {
		t.Fatalf("email FullText must keep the fixed 4-line header: lines=%d\ntext=%q", got, emailDoc.Text)
	}
	if got := strings.Count(mediaDoc.Text, "\n") + 1; got != 8 {
		t.Fatalf("media FullText must keep the fixed 8-line layout: lines=%d\ntext=%q", got, mediaDoc.Text)
	}

	assertSub := func(doc CloudDocument, kind string, wantStart, wantEnd int, wantText string) {
		t.Helper()
		for _, sub := range doc.SubUnits {
			if sub.Kind != kind {
				continue
			}
			if sub.LineStart != wantStart || sub.LineEnd != wantEnd {
				t.Fatalf("%s sub-unit %q line span = %d..%d, want %d..%d (FullText lines=%v)",
					doc.ID, kind, sub.LineStart, sub.LineEnd, wantStart, wantEnd, strings.Split(doc.Text, "\n"))
			}
			if sub.LineStart < 1 || sub.LineEnd > strings.Count(doc.Text, "\n")+1 {
				t.Fatalf("%s sub-unit %q escapes the parent span: %d..%d", doc.ID, kind, sub.LineStart, sub.LineEnd)
			}
			lines := strings.Split(doc.Text, "\n")[sub.LineStart-1 : sub.LineEnd]
			if !strings.Contains(strings.Join(lines, "\n"), wantText) {
				t.Fatalf("%s sub-unit %q does not map onto its FullText line span %d..%d: %q not in %q",
					doc.ID, kind, sub.LineStart, sub.LineEnd, wantText, strings.Join(lines, "\n"))
			}
			return
		}
		t.Fatalf("%s missing sub-unit %q: %+v", doc.ID, kind, doc.SubUnits)
	}

	// email：标题块锚定 Summary 行（Subject 不在 FullText 中），Detail 两段各自锚定真实行。
	assertSub(emailDoc, "header", 3, 3, "Summary: Groceries arrive tomorrow")
	assertSub(emailDoc, "detail:0", 4, 5, "Para one line one.\nPara one line two.")
	assertSub(emailDoc, "detail:1", 7, 7, "Para two.")
	// 媒体：场域块跨 Location..Short Caption，OCR/Tags 块跨 OCR..Tags。
	assertSub(mediaDoc, "scene", 4, 5, "Location: West Quay Road\nShort Caption: a small airplane")
	assertSub(mediaDoc, "ocr_tags", 7, 8, "OCR: EXIT 12\nTags: plane, sky")
	// 每个块都不得越出父单元行区间。
	for _, doc := range docs {
		for _, sub := range doc.SubUnits {
			if sub.LineStart < doc.LineStart || sub.LineEnd > doc.LineEnd {
				t.Fatalf("%s sub-unit %q escapes parent span %d..%d: %d..%d",
					doc.ID, sub.Kind, doc.LineStart, doc.LineEnd, sub.LineStart, sub.LineEnd)
			}
		}
	}
}

// TestLocomoTurnSubUnitsExtractObservations 守卫 LoCoMo 轮次的多模态观察块组装：
// 正文内方括号观察片段各成一块，显式结构化字段也各成一块，行跨度沿用轮次物理行号。
func TestLocomoTurnSubUnitsExtractObservations(t *testing.T) {
	text := "Congrats Joanna! How was it to see it?  [shares a photo holding a controller]"
	subs := locomoTurnSubUnits("locomo-abc", text, map[string]any{"image_caption": "A controller on a couch"}, 7, 7)
	if len(subs) != 2 {
		t.Fatalf("expected 2 high-resolution sub-units (caption + bracketed observation), got %d: %+v", len(subs), subs)
	}
	found := map[string]bool{}
	for _, sub := range subs {
		found[strings.TrimSpace(sub.Text)] = true
		if sub.LineStart != 7 || sub.LineEnd != 7 {
			t.Fatalf("sub-unit %q must inherit the turn line span, got %d..%d", sub.Kind, sub.LineStart, sub.LineEnd)
		}
		if sub.ID == "" || sub.BlockID == "" {
			t.Fatalf("sub-unit %q missing stable identifiers: %+v", sub.Kind, sub)
		}
	}
	if !found["shares a photo holding a controller"] {
		t.Fatalf("bracketed observation was not extracted: %+v", subs)
	}
	if !found["A controller on a couch"] {
		t.Fatalf("structured caption field was not extracted: %+v", subs)
	}
	if subs := locomoTurnSubUnits("locomo-abc", "plain turn without attachments.", nil, 1, 1); len(subs) != 0 {
		t.Fatalf("plain turns must not invent sub-units: %+v", subs)
	}
}

// TestIngestPreservesExactUnitSetWithSpatialMetadata 是「/units 对账 100% 精确、零 foreign unit」
// 的行为证明：带完整空间/结构化元数据的语料写入后，VerifyPrepared 必须按 doc.ID 全集精确相等通过，
// 且云端收到的每个脉冲都携带 doc_h / block_h / line_start / line_end 与作者承诺。
func TestIngestPreservesExactUnitSetWithSpatialMetadata(t *testing.T) {
	server := newCloudTestServer(t)
	engine := newTestCloudEngine(t, server, SuiteLoCoMo)
	documents := []CloudDocument{
		{
			ID: "spatial-a", Text: "alpha memory body", OccurredAt: 1700000000, NarratedAt: 1700000000,
			Author: "Caroline", FileURL: "session-1", BlockID: "session-1#0",
			LineStart: 1, LineEnd: 1,
			SubUnits: []ramesdk.SubUnit{{Kind: "observation", Text: "shares a photo", LineStart: 1, LineEnd: 1}},
		},
		{
			ID: "spatial-b", Text: "beta memory body", OccurredAt: 1700000060, NarratedAt: 1700000060,
			Author: "Caroline", FileURL: "session-1", BlockID: "session-1#1",
			LineStart: 2, LineEnd: 2,
			// 子块行区间刻意远离父单元行区间：它只用于端侧高分辨率定位，
			// 绝不允许改写上行脉冲里父单元的 (line_start, line_end)。
			SubUnits: []ramesdk.SubUnit{{Kind: "observation", Text: "beta detail block", LineStart: 5, LineEnd: 9}},
		},
	}
	manifest, err := engine.Ingest(context.Background(), documents, false, 2, nil)
	if err != nil {
		t.Fatalf("Ingest with spatial metadata: %v", err)
	}
	if manifest.Documents != len(documents) {
		t.Fatalf("manifest documents = %d, want %d", manifest.Documents, len(documents))
	}
	verified, err := engine.VerifyPrepared(context.Background(), documents)
	if err != nil {
		t.Fatalf("VerifyPrepared must pass with exact 1:1 unit set: %v", err)
	}
	if !verified.Reused || verified.CorpusSHA256 != manifest.CorpusSHA256 {
		t.Fatalf("verification receipt mismatch: %+v", verified)
	}

	server.mu.Lock()
	packets := append([]ramesdk.BlindPulsePacket(nil), server.ingests...)
	server.mu.Unlock()
	if len(packets) != len(documents) {
		t.Fatalf("cloud received %d pulses for %d documents (sub-units must stay inside the parent)", len(packets), len(documents))
	}
	byID := make(map[string]ramesdk.BlindPulsePacket, len(packets))
	for _, pkt := range packets {
		byID[pkt.UnitID] = pkt
		if pkt.DocHash == 0 || pkt.BlockHash == 0 || pkt.LineStart == 0 || pkt.AuthorHash == 0 {
			t.Fatalf("packet %q lost spatial/author commitments: %+v", pkt.UnitID, pkt)
		}
	}
	first, second := byID["spatial-a"], byID["spatial-b"]
	if first.UnitID == "" || second.UnitID == "" {
		t.Fatalf("expected exactly the corpus unit ids, got %v", byID)
	}
	if first.DocHash != second.DocHash {
		t.Fatalf("same session must share doc_h: %d vs %d", first.DocHash, second.DocHash)
	}
	if first.BlockHash == second.BlockHash {
		t.Fatalf("distinct blocks must keep distinct block_h: %d", first.BlockHash)
	}
	if first.LineStart != 1 || second.LineStart != 2 || second.LineEnd != 2 {
		t.Fatalf("line spans not transmitted: first=%d..%d second=%d..%d", first.LineStart, first.LineEnd, second.LineStart, second.LineEnd)
	}
	// 子块坐标是端侧专用面：即使子块声明 5..9，上行父包仍只带父单元 2..2。
	if second.LineEnd != 2 {
		t.Fatalf("sub-unit line span leaked into the upstream parent pulse: %d..%d", second.LineStart, second.LineEnd)
	}
	if _, _, _, err := engine.Store.LoadLocalDocument("spatial-a"); err != nil {
		t.Fatalf("local plaintext vault missing the document: %v", err)
	}
}

// TestCloudDocumentDigestStabilityForPlainDocuments 守卫向后兼容：不带可选字段的文档，
// 其语料摘要与历史口径逐字节一致（新字段全部 omitempty），旧 manifest 不被误判作废。
func TestCloudDocumentDigestStabilityForPlainDocuments(t *testing.T) {
	legacy := []CloudDocument{{ID: "doc-a", Text: "alpha memory", OccurredAt: 1, Layer: ramesdk.LayerEpisodic}}
	digest, err := CloudCorpusDigest(legacy)
	if err != nil {
		t.Fatalf("CloudCorpusDigest: %v", err)
	}
	want := `[{"id":"doc-a","text":"alpha memory","occurred_at":1,"layer":"episodic"}]`
	raw, err := jsonMarshalForTest(legacy)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if raw != want {
		t.Fatalf("plain CloudDocument JSON drifted: %s", raw)
	}
	if digest == "" || len(digest) != 64 {
		t.Fatalf("unexpected digest: %q", digest)
	}
}

func jsonMarshalForTest(value any) (string, error) {
	raw, err := json.Marshal(value)
	return string(raw), err
}

// TestRunLoCoMoWithSpatialMetadataKeepsRecallCompatible 是 LoCoMo 适配器升级后的端到端回归：
// 每轮对话带上说话人/会话 DocHash/物理行号/多模态观察块之后，云端仍是「一条语料 = 一个 UnitID」，
// 证据命中与 Recall 口径不变（evidence_hit 保持 1.0），且脉包携带完整四维坐标。
func TestRunLoCoMoWithSpatialMetadataKeepsRecallCompatible(t *testing.T) {
	dataDir := buildTinyData(t)
	server := newCloudTestServer(t)
	engine := newTestCloudEngine(t, server, SuiteLoCoMo)
	summary, err := Run(engine.cfg, Options{
		DataDir: dataDir, Tier: "1540", StorePath: engine.StorePath, OutDir: t.TempDir(),
		PulseMode: PulseModePulseCipher,
		Reader:    "deliverable", Answerer: "none", Judge: "off",
		Workers: 2, ContextChars: 6000, RawLimit: 5,
		ProgressEach: 0, Log: func(string, ...any) {},
	})
	if err != nil {
		t.Fatalf("Run with spatial metadata: %v", err)
	}
	if summary["n"] != 2 || summary["transport"] != CloudTransport || summary["local_fallback"] != false {
		t.Fatalf("LoCoMo provenance/metrics drifted: %v", summary)
	}
	if summary["evidence_hit"] != 1.0 {
		t.Fatalf("evidence hit must stay 1.0 after speaker/line metadata injection: %v", summary["evidence_hit"])
	}
	server.mu.Lock()
	packets := append([]ramesdk.BlindPulsePacket(nil), server.ingests...)
	server.mu.Unlock()
	if len(packets) != 3 {
		t.Fatalf("each conversation turn must stay exactly one cloud unit: got %d packets", len(packets))
	}
	docHashes := map[uint64]int{}
	for _, pkt := range packets {
		if pkt.AuthorHash == 0 || pkt.DocHash == 0 || pkt.BlockHash == 0 || pkt.LineStart == 0 || pkt.LineEnd < pkt.LineStart {
			t.Fatalf("turn %q lost spatial/speaker coordinates: %+v", pkt.UnitID, pkt)
		}
		docHashes[pkt.DocHash]++
	}
	// 两个会话 ⇒ 两个 DocHash；同会话的两轮共享同一 DocHash。
	if len(docHashes) != 2 {
		t.Fatalf("expected one doc_h per session (2 sessions), got %v", docHashes)
	}
	shared := false
	for _, count := range docHashes {
		if count == 2 {
			shared = true
		}
	}
	if !shared {
		t.Fatalf("same-session turns must share doc_h: %v", docHashes)
	}
	// 本地明文金库仍还原原始轮次文本，并带上说话人（Recall 交付面不变差）。
	vault, err := ramesdk.NewJSONFileStore(engine.StorePath, []byte("mem_test_cloud_eval"))
	if err != nil {
		t.Fatalf("open local plaintext vault: %v", err)
	}
	raw, _, _, err := vault.LoadLocalDocument(packets[0].UnitID)
	if err != nil {
		t.Fatalf("load local plaintext: %v", err)
	}
	if !strings.Contains(raw, ": ") {
		t.Fatalf("speaker attribution missing from local plaintext: %q", raw)
	}
}
