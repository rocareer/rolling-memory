// plugin: kb v1.0.0
package kb

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
	repoKB "github.com/ai-go-hub/ai-go-admin/plugins/kb/repository/kb"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/chunk"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/ingest"

	"gorm.io/gorm"
)

// TestMain 包级门禁：kb 包 DB 用例一律不触模型（嵌入阶段由 embed 包单测与冒烟/运行时门禁覆盖）。
// KB_EMBED_ENABLED 经 viper AutomaticEnv 覆盖 kb.embed.enabled（须在 config.Init 前设置）。
func TestMain(m *testing.M) {
	os.Setenv("KB_EMBED_ENABLED", "false")
	os.Exit(m.Run())
}

//  管道端到端（连库；PLUGIN_KB_DB=1 门控——与 acl_test.go 同门）：
// 提取（重切不重解）→ 计划（build 身份）→ 切分（锚可复算）→ 落块（同 build 幂等）→
// 换尺寸档重切（旧 build 保留到换出）→ 水位（单调）→ 删文档清理（守卫 + 空集）。
// 不触模型：嵌入阶段（embed 包）由 embed 包单测与冒烟/运行时门禁覆盖。

const pipelineFixtureMd = "# 合同标题\n\n第一条 定义。本合同自签署之日起生效。\n\n## 第二条 价款\n\n总价为人民币壹万元整。\n"

// pipelineFixture 管道夹具（自建自清；行 id 全登记）
type pipelineFixture struct {
	*kbFixture
	blobIDs []string
	docID   uint
	libID   uint
}

// newPipelineFixture 建库 + 原始件（blob/ref）+ 文档行（registered）——md 缺省件
func newPipelineFixture(t *testing.T, content string) *pipelineFixture {
	t.Helper()
	return newPipelineFixtureFile(t, "fixture.md", "text/markdown", content)
}

// newPipelineFixtureFile 同上，来源文件名/mime 显式指定（格式路由经 DetectFormat 走 SourceRef/Mime，
// 供 table 策略（csv）等非 md 夹具复用）
func newPipelineFixtureFile(t *testing.T, sourceRef, mime, content string) *pipelineFixture {
	t.Helper()
	f := &pipelineFixture{kbFixture: newKBFixture(t)}
	ctx := f.ctx

	f.libID = f.library(model.KBLibraryOwnerKindSystem, 0, fmt.Sprintf("kb-pipeline-test-lib-%s", f.suffix), 1).ID

	data := []byte(content)
	sum256 := sha256.Sum256(data)
	sum1 := sha1.Sum(data)
	blobSHA := hex.EncodeToString(sum256[:])
	contentHash := hex.EncodeToString(sum1[:])

	blob := ingest.BlobRow{SHA256: blobSHA, MIME: mime, Size: int64(len(data)), Bytes: data}
	if err := gorm.G[ingest.BlobRow](f.db).Create(ctx, &blob); err != nil {
		t.Fatalf("建原始件失败: %v", err)
	}
	f.blobIDs = append(f.blobIDs, blobSHA)

	doc := &model.KBDocument{
		LibraryID: f.libID, Title: "pipeline-fixture", SourceKind: model.KBDocumentSourceFile,
		SourceRef: sourceRef, ContentHash: contentHash, Mime: mime,
		Size: int64(len(data)), BuildState: model.KBDocumentBuildRegistered,
		CreatedByKind: model.KBActorKindAdmin, CreatedByID: f.adminID,
	}
	if err := gorm.G[model.KBDocument](f.db).Create(ctx, doc); err != nil {
		t.Fatalf("建文档失败: %v", err)
	}
	f.docIDs = append(f.docIDs, doc.ID)
	f.docID = doc.ID

	ref := ingest.BlobRefRow{LibraryID: f.libID, DocumentID: doc.ID, SHA256: blobSHA}
	if err := gorm.G[ingest.BlobRefRow](f.db).Create(ctx, &ref); err != nil {
		t.Fatalf("建原始件登记失败: %v", err)
	}

	t.Cleanup(func() {
		if err := f.db.Exec("DELETE FROM kb_chunk_vectors WHERE library_id = ?", f.libID).Error; err != nil {
			t.Logf("清理向量失败（请手工核）: %v", err)
		}
		if err := f.db.Exec("DELETE FROM kb_chunk WHERE document_id = ?", f.docID).Error; err != nil {
			t.Logf("清理切片失败（请手工核）: %v", err)
		}
		if err := f.db.Exec("DELETE FROM kb_extract WHERE document_id = ?", f.docID).Error; err != nil {
			t.Logf("清理提取物失败（请手工核）: %v", err)
		}
		if err := f.db.Exec("DELETE FROM kb_blob_ref WHERE document_id = ?", f.docID).Error; err != nil {
			t.Logf("清理原始件登记失败（请手工核）: %v", err)
		}
		for _, sha := range f.blobIDs {
			if err := f.db.Exec("DELETE FROM kb_blob WHERE sha256 = ?", sha).Error; err != nil {
				t.Logf("清理原始件失败（请手工核）: %v", err)
			}
		}
		if err := f.db.Exec("DELETE FROM kb_produce_state WHERE library_id = ?", f.libID).Error; err != nil {
			t.Logf("清理水位失败（请手工核）: %v", err)
		}
	})
	return f
}

// reloadDoc 读回文档行
func (f *pipelineFixture) reloadDoc(t *testing.T) *model.KBDocument {
	t.Helper()
	doc, err := gorm.G[model.KBDocument](f.db).Where("id = ?", f.docID).First(f.ctx)
	if err != nil {
		t.Fatalf("读文档失败: %v", err)
	}
	return &doc
}

// chunkRows 该文档的全部切片行（id 升序）
func (f *pipelineFixture) chunkRows(t *testing.T, build string) []model.KBChunk {
	t.Helper()
	q := gorm.G[model.KBChunk](f.db).Where("document_id = ?", f.docID)
	if build != "" {
		q = q.Where("build_hash = ?", build)
	}
	rows, err := q.Order("id ASC").Find(f.ctx)
	if err != nil {
		t.Fatalf("读切片失败: %v", err)
	}
	return rows
}

// runToChunks 执行 提取 → 计划 → 切分 → 落块（返回计划与块数、新写入行数）
func (f *pipelineFixture) runToChunks(t *testing.T, doc *model.KBDocument) (ingest.Plan, []chunk.Chunk, int) {
	t.Helper()
	ctx := f.ctx
	blob, err := ingest.LoadBlobForDocument(ctx, f.db, doc.ID)
	if err != nil {
		t.Fatalf("取原始件失败: %v", err)
	}
	format, err := ingest.DetectFormat(doc.SourceRef, doc.Mime, blob.Bytes)
	if err != nil {
		t.Fatalf("判型失败: %v", err)
	}
	extractor, version, err := splitExtractorID(format)
	if err != nil {
		t.Fatalf("提取器身份失败: %v", err)
	}
	ext, err := f.svc.ensureExtract(ctx, doc, format, extractor, version, blob.Bytes)
	if err != nil {
		t.Fatalf("提取失败: %v", err)
	}
	plan, err := ingest.ResolvePlan(ctx, f.db, doc, format, ext.Text, blob.SHA256, ext.ExtractorID())
	if err != nil {
		t.Fatalf("计划解析失败: %v", err)
	}
	chunks, err := ingest.SplitDocument(ctx, doc, plan, ext.Text)
	if err != nil {
		t.Fatalf("切分失败: %v", err)
	}
	written, err := ingest.EnsureChunks(ctx, f.db, doc, plan, chunks, ingest.BudgetLimits{})
	if err != nil {
		t.Fatalf("落块失败: %v", err)
	}
	return plan, chunks, written
}

// TestPipelineDB 单文档管道主链（幂等 / 锚可复算 / 换尺寸档换出 / 水位 / 清理）
func TestPipelineDB(t *testing.T) {
	f := newPipelineFixture(t, pipelineFixtureMd)
	ctx := f.ctx

	doc := f.reloadDoc(t)
	plan, chunks, written := f.runToChunks(t, doc)

	// ① 锚与落库：首轮全量写入
	if written != len(chunks) || len(chunks) == 0 {
		t.Fatalf("首轮应全量写入: written=%d chunks=%d", written, len(chunks))
	}
	rows := f.chunkRows(t, plan.BuildHash)
	if len(rows) != len(chunks) {
		t.Fatalf("切片行数不符: %d != %d", len(rows), len(chunks))
	}
	prefix := ingest.DocLocator(doc.ID)
	for i, r := range rows {
		if r.Seq != i+1 || r.Locator != fmt.Sprintf("%s#p%d", prefix, i+1) {
			t.Fatalf("锚/块序不符: seq=%d locator=%s", r.Seq, r.Locator)
		}
		if r.BuildHash != plan.BuildHash || r.ConfigHash != plan.Config.Hash() || r.ChunkerVersion != plan.ChunkerVersion {
			t.Fatalf("构建身份列不符: %+v", r)
		}
		if r.TextHash == "" || r.ConfigSnapshot == nil {
			t.Fatalf("缺少 text_hash/config_snapshot: %+v", r)
		}
	}
	// 锚可复算（char 区间 + text_hash 复算一致；VerifyWithin 同口径）
	extractText := f.extractText(t)
	chActivity := make([]chunk.Chunk, 0, len(rows))
	for i := range rows {
		chActivity = append(chActivity, chunk.Chunk{
			Seq: rows[i].Seq, Locator: rows[i].Locator, CharStart: rows[i].CharStart,
			CharEnd: rows[i].CharEnd, Text: rows[i].Text, TextHash: rows[i].TextHash,
		})
	}
	if err := chunk.VerifyVerbatim(extractText, chActivity); err != nil {
		t.Fatalf("锚复算校验失败: %v", err)
	}

	// ② 同 build 重跑：行数与行 id 不变（不重写 ⇒ 向量行不失效）
	idsBefore := chunkIDs(rows)
	_, _, written2 := f.runToChunks(t, f.reloadDoc(t))
	rows2 := f.chunkRows(t, plan.BuildHash)
	if written2 != 0 || len(rows2) != len(rows) {
		t.Fatalf("同 build 重跑应零写入且行数不变: written=%d rows=%d", written2, len(rows2))
	}
	for i := range rows2 {
		if rows2[i].ID != idsBefore[i] {
			t.Fatalf("同 build 重跑不应重建行: %d != %d", rows2[i].ID, idsBefore[i])
		}
	}

	// ③ 提取物复用（重切不重解）
	if _, reused := f.countExtracts(t); reused != 1 {
		t.Fatalf("提取物应只 1 行（复用）: %d", reused)
	}

	// ④ 换尺寸档：新 build 与旧 build 并存（旧 build 保留到换出）
	if err := f.db.Model(&model.KBDocument{}).Where("id = ?", doc.ID).
		Update("chunk_config", `{"target":128,"overlap":16}`).Error; err != nil {
		t.Fatalf("写文档级配置失败: %v", err)
	}
	plan2, chunks2, written3 := f.runToChunks(t, f.reloadDoc(t))
	if plan2.BuildHash == plan.BuildHash {
		t.Fatal("换尺寸档后 build_hash 应变化")
	}
	if written3 != len(chunks2) {
		t.Fatalf("新 build 应全量写入: %d != %d", written3, len(chunks2))
	}
	if old := f.chunkRows(t, plan.BuildHash); len(old) != len(rows) {
		t.Fatalf("换出前旧 build 行应保留: %d", len(old))
	}
	// active_build 为 char(40)：空值读出为 40 空格（PG bpchar 补空格语义），断言先 TrimSpace
	if got := f.reloadDoc(t); strings.TrimSpace(got.ActiveBuild) != "" || got.BuildState != model.KBDocumentBuildRegistered {
		t.Fatalf("未换出前 active_build 不应变: %q %s", got.ActiveBuild, got.BuildState)
	}

	// ⑤ 换出：active_build 原子换出 + 删旧 build 行
	removed, err := ingest.SwitchBuild(ctx, f.db, doc.ID, plan2.BuildHash, nil)
	if err != nil {
		t.Fatalf("换出失败: %v", err)
	}
	if removed != int64(len(rows)) {
		t.Fatalf("换出应删旧 build 行 %d: %d", len(rows), removed)
	}
	got := f.reloadDoc(t)
	if got.ActiveBuild != plan2.BuildHash || got.BuildState != model.KBDocumentBuildReady {
		t.Fatalf("换出后状态不符: %q %s", got.ActiveBuild, got.BuildState)
	}
	if old := f.chunkRows(t, plan.BuildHash); len(old) != 0 {
		t.Fatalf("换出后旧 build 行应清空: %d", len(old))
	}
	if now := f.chunkRows(t, plan2.BuildHash); len(now) != len(chunks2) {
		t.Fatalf("换出后新 build 行应保留: %d != %d", len(now), len(chunks2))
	}

	// ⑥ 水位：单调推进，GREATEST 不回退
	if wm, err := ingest.LoadWatermark(ctx, f.db, f.libID); err != nil || wm != 0 {
		t.Fatalf("初始水位应 0: %d %v", wm, err)
	}
	if err := ingest.AdvanceWatermark(ctx, f.db, f.libID, doc.ID); err != nil {
		t.Fatalf("推进水位失败: %v", err)
	}
	if err := ingest.AdvanceWatermark(ctx, f.db, f.libID, 0); err != nil {
		t.Fatalf("水位回退应被 GREATEST 吸收: %v", err)
	}
	if wm, _ := ingest.LoadWatermark(ctx, f.db, f.libID); wm != doc.ID {
		t.Fatalf("水位应保持单调: %d != %d", wm, doc.ID)
	}

	// ⑦ 清理守卫：活文档拒绝清理
	if err := f.svc.RunPurge(ctx, 0, doc.ID); err == nil {
		t.Fatal("未软删文档应拒绝清理（防误清活数据）")
	}
	// 软删（删除面）→ 清理 → 派生面空集 + 原始件登记与孤儿 blob 回收
	if err := f.svc.DeleteDocument(ctx, Actor{Kind: model.KBActorKindAdmin, ID: f.adminID}, doc.ID); err != nil {
		t.Fatalf("软删文档失败: %v", err)
	}
	if err := f.svc.RunPurge(ctx, 0, doc.ID); err != nil {
		t.Fatalf("清理失败: %v", err)
	}
	if rows := f.chunkRows(t, ""); len(rows) != 0 {
		t.Fatalf("清理后切片应空集: %d", len(rows))
	}
	var vectors int64
	if err := f.db.Model(&ingest.ChunkVectorRow{}).Where("library_id = ?", f.libID).Count(&vectors).Error; err != nil {
		t.Fatalf("统计向量失败: %v", err)
	}
	if vectors != 0 {
		t.Fatalf("清理后向量应空集: %d", vectors)
	}
	if _, n := f.countExtracts(t); n != 0 {
		t.Fatalf("清理后提取物应空集: %d", n)
	}
	var blobs int64
	if err := f.db.Model(&ingest.BlobRow{}).Where("sha256 = ?", f.blobIDs[0]).Count(&blobs).Error; err != nil {
		t.Fatalf("统计原始件失败: %v", err)
	}
	if blobs != 0 {
		t.Fatalf("无引用原始件应被回收: %d", blobs)
	}
	if err := f.svc.RunPurge(ctx, 0, doc.ID); err != nil {
		t.Fatalf("清理幂等重入失败: %v", err)
	}
}

// TestZeroChangeKeepsReadyDB 零变更路径不得降级 build_state（实测：整库重切后 ready=2/3）。
// 首轮全链到 ready；同 build 重跑（零变更：不重写行、不换出、不落审计）后文档必须仍为 ready。
func TestZeroChangeKeepsReadyDB(t *testing.T) {
	f := newPipelineFixture(t, pipelineFixtureMd)
	ctx := f.ctx

	if _, err := f.svc.RunIngest(ctx, ingest.IngestParams{DocumentID: f.docID}, nil); err != nil {
		t.Fatalf("首轮导入失败: %v", err)
	}
	first := f.reloadDoc(t)
	if first.BuildState != model.KBDocumentBuildReady || strings.TrimSpace(first.ActiveBuild) == "" {
		t.Fatalf("首轮应 ready 且换出 active_build: state=%s build=%q", first.BuildState, first.ActiveBuild)
	}
	firstRows := f.chunkRows(t, "")

	res, err := f.svc.RunIngest(ctx, ingest.IngestParams{DocumentID: f.docID}, nil)
	if err != nil {
		t.Fatalf("同 build 重跑失败: %v", err)
	}
	if res.State != "ready" || res.ChunkRows != 0 {
		t.Fatalf("重跑应零变更 ready: state=%s written=%d", res.State, res.ChunkRows)
	}
	again := f.reloadDoc(t)
	if again.BuildState != model.KBDocumentBuildReady {
		t.Fatalf("零变更重跑不得降级 build_state: %s", again.BuildState)
	}
	if strings.TrimSpace(again.ActiveBuild) != strings.TrimSpace(first.ActiveBuild) {
		t.Fatalf("active_build 不应变: %q vs %q", again.ActiveBuild, first.ActiveBuild)
	}
	if rows := f.chunkRows(t, ""); len(rows) != len(firstRows) {
		t.Fatalf("重跑不应增删切片行: %d vs %d", len(rows), len(firstRows))
	}
}

// TestSwitchBuildSupersededDB 并发换出交错：迟到换出不得写出「ready + active_build 指向零行」的
// 静默坏态（实测确认：原语序列探针）。
//
// 序列：两个 build 的行并存在场 → SwitchBuild(build2)（删 build1 行）→ SwitchBuild(build1)（迟到者：
// 其行已被前一次换出删除）。期望：迟到换出被拒（ErrBuildSuperseded，整事务回滚），末态 = 首次换出方
// ready/build2 且 build2 行完整在场、检索可见性谓词命中 > 0。
func TestSwitchBuildSupersededDB(t *testing.T) {
	f := newPipelineFixture(t, pipelineFixtureMd)
	ctx := f.ctx

	plan1, _, _ := f.runToChunks(t, f.reloadDoc(t))
	if err := f.db.Model(&model.KBDocument{}).Where("id = ?", f.docID).
		Update("chunk_config", `{"target":128,"overlap":16}`).Error; err != nil {
		t.Fatalf("写文档级配置失败: %v", err)
	}
	plan2, chunks2, _ := f.runToChunks(t, f.reloadDoc(t))
	if plan2.BuildHash == plan1.BuildHash {
		t.Fatal("换尺寸档后 build_hash 应变化")
	}

	if _, err := ingest.SwitchBuild(ctx, f.db, f.docID, plan2.BuildHash, nil); err != nil {
		t.Fatalf("首次换出（build2）失败: %v", err)
	}
	if _, err := ingest.SwitchBuild(ctx, f.db, f.docID, plan1.BuildHash, nil); !errors.Is(err, ingest.ErrBuildSuperseded) {
		t.Fatalf("迟到换出应拒（ErrBuildSuperseded）: %v", err)
	}

	got := f.reloadDoc(t)
	if got.BuildState != model.KBDocumentBuildReady || strings.TrimSpace(got.ActiveBuild) != plan2.BuildHash {
		t.Fatalf("末态应为首次换出方 ready/build2: state=%s build=%q", got.BuildState, got.ActiveBuild)
	}
	if rows := f.chunkRows(t, plan1.BuildHash); len(rows) != 0 {
		t.Fatalf("build1 行应已清: %d", len(rows))
	}
	if rows := f.chunkRows(t, plan2.BuildHash); len(rows) != len(chunks2) {
		t.Fatalf("build2 行应完整在场: %d/%d", len(rows), len(chunks2))
	}
	// 检索可见性谓词（逐字照 search_legs chunkBaseCond：ready ∧ c.build_hash = d.active_build）
	var visible int64
	if err := f.db.Raw("SELECT count(*) FROM kb_chunk c JOIN kb_document d ON d.id = c.document_id"+
		" WHERE c.document_id = ? AND d.deleted_at IS NULL AND d.build_state = 'ready' AND c.build_hash = d.active_build",
		f.docID).Scan(&visible).Error; err != nil {
		t.Fatalf("检索可见性查询失败: %v", err)
	}
	if visible == 0 {
		t.Fatal("末态检索面命中应 > 0（active_build 指向零行 = 静默不可召回）")
	}
}

// createBareDoc 无原始件登记的文档（RunIngest 必失败：kb_blob_ref 无行）
func (f *pipelineFixture) createBareDoc(t *testing.T, title string) uint {
	t.Helper()
	doc := &model.KBDocument{
		LibraryID: f.libID, Title: title, SourceKind: model.KBDocumentSourceFile,
		SourceRef: "bare.md", ContentHash: fmt.Sprintf("%040s", title), Mime: "text/markdown", Size: 1,
		BuildState: model.KBDocumentBuildRegistered, CreatedByKind: model.KBActorKindAdmin, CreatedByID: 990001,
	}
	if err := gorm.G[model.KBDocument](f.db).Create(f.ctx, doc); err != nil {
		t.Fatalf("建无原始件文档失败: %v", err)
	}
	f.docIDs = append(f.docIDs, doc.ID)
	return doc.ID
}

// createDocWithBlob 造「原始件 + 登记 + 文档行」（registered；可被 RunIngest 跑通）
func (f *pipelineFixture) createDocWithBlob(t *testing.T, title, content string) uint {
	t.Helper()
	data := []byte(content)
	sum256 := sha256.Sum256(data)
	sum1 := sha1.Sum(data)
	blobSHA := hex.EncodeToString(sum256[:])
	blob := ingest.BlobRow{SHA256: blobSHA, MIME: "text/markdown", Size: int64(len(data)), Bytes: data}
	if err := gorm.G[ingest.BlobRow](f.db).Create(f.ctx, &blob); err != nil {
		t.Fatalf("建原始件失败: %v", err)
	}
	f.blobIDs = append(f.blobIDs, blobSHA)
	doc := &model.KBDocument{
		LibraryID: f.libID, Title: title, SourceKind: model.KBDocumentSourceFile,
		SourceRef: "extra.md", ContentHash: hex.EncodeToString(sum1[:]), Mime: "text/markdown",
		Size: int64(len(data)), BuildState: model.KBDocumentBuildRegistered,
		CreatedByKind: model.KBActorKindAdmin, CreatedByID: 990001,
	}
	if err := gorm.G[model.KBDocument](f.db).Create(f.ctx, doc); err != nil {
		t.Fatalf("建文档失败: %v", err)
	}
	f.docIDs = append(f.docIDs, doc.ID)
	ref := ingest.BlobRefRow{LibraryID: f.libID, DocumentID: doc.ID, SHA256: blobSHA}
	if err := gorm.G[ingest.BlobRefRow](f.db).Create(f.ctx, &ref); err != nil {
		t.Fatalf("建原始件登记失败: %v", err)
	}
	return doc.ID
}

// TestRebuildWatermarkFreezeDB 水位不得越过失败文档（实测确认：原实现在失败文档之后
// 仍随后续成功推进水位 ⇒ 退避重试空转、失败文档永不再扫）。序列：[doc1 可成功 / doc2 无原始件必失败 /
// doc3 可成功]：水位必须冻结在 doc1；二次重跑仍扫到 doc2 并再报失败（不静默成功）。
func TestRebuildWatermarkFreezeDB(t *testing.T) {
	f := newPipelineFixture(t, pipelineFixtureMd)
	ctx := f.ctx
	doc1 := f.docID
	doc2 := f.createBareDoc(t, "kb-pipeline-test-bare")
	doc3 := f.createDocWithBlob(t, "kb-pipeline-test-third", pipelineFixtureMd+"\n追加段落（第三文档）。\n")
	// 本用例多文档派生行清理（pipelineFixture 清尾只认首文档）
	t.Cleanup(func() {
		f.db.Exec("DELETE FROM kb_chunk WHERE library_id = ?", f.libID)
		f.db.Exec("DELETE FROM kb_extract WHERE document_id IN (SELECT id FROM kb_document WHERE library_id = ?)", f.libID)
		f.db.Exec("DELETE FROM kb_blob_ref WHERE library_id = ?", f.libID)
		f.db.Exec("DELETE FROM kb_document WHERE library_id = ?", f.libID)
	})

	res, err := f.svc.RunRebuild(ctx, ingest.RebuildParams{LibraryID: f.libID, Mode: ingest.RebuildModeRechunk}, nil)
	if err == nil {
		t.Fatal("存在失败文档时作业应报错（交退避重试）")
	}
	if res.Failed != 1 {
		t.Fatalf("应 1 个失败文档: %+v", res)
	}
	if res.Watermark != doc1 {
		t.Fatalf("水位应冻结在失败文档之前（doc1=%d；doc2=%d doc3=%d）: watermark=%d", doc1, doc2, doc3, res.Watermark)
	}
	if wm, werr := ingest.LoadWatermark(ctx, f.db, f.libID); werr != nil || wm != doc1 {
		t.Fatalf("库内水位应冻结在 doc1: wm=%d err=%v", wm, werr)
	}
	// 二次重跑（模拟退避重试）：仍扫到 doc2 并再次失败——不得「Processed=0 静默成功」
	res2, err2 := f.svc.RunRebuild(ctx, ingest.RebuildParams{LibraryID: f.libID, Mode: ingest.RebuildModeRechunk}, nil)
	if err2 == nil {
		t.Fatalf("二次重跑应仍报失败（水位未越过失败文档）: %+v", res2)
	}
}

// TestRebuildResetSweepDB 全量档（Reset）：水位追平库末后「改档 → 整库重切」仍可达（实测确认：
// 库级路径原无全量/复位档，只能 psql 改水位）。
func TestRebuildResetSweepDB(t *testing.T) {
	f := newPipelineFixture(t, pipelineFixtureMd)
	ctx := f.ctx

	if _, err := f.svc.RunIngest(ctx, ingest.IngestParams{DocumentID: f.docID}, nil); err != nil {
		t.Fatalf("首轮导入失败: %v", err)
	}
	if err := ingest.AdvanceWatermark(ctx, f.db, f.libID, f.docID); err != nil {
		t.Fatalf("推进水位失败: %v", err)
	}

	// 水位已追平：无 Reset ⇒ 空转（不白跑）
	res, err := f.svc.RunRebuild(ctx, ingest.RebuildParams{LibraryID: f.libID, Mode: ingest.RebuildModeRechunk}, nil)
	if err != nil {
		t.Fatalf("水位追平后空转不应报错: %v", err)
	}
	if res.Processed != 0 || res.Windows != 0 {
		t.Fatalf("水位追平后应空转: processed=%d windows=%d", res.Processed, res.Windows)
	}

	// Reset ⇒ 自 0 起扫全库（同 build ⇒ 零变更 ready，行数不变）
	res, err = f.svc.RunRebuild(ctx, ingest.RebuildParams{LibraryID: f.libID, Mode: ingest.RebuildModeRechunk, Reset: true}, nil)
	if err != nil {
		t.Fatalf("全量重切失败: %v", err)
	}
	if res.Processed == 0 || res.Ready == 0 {
		t.Fatalf("Reset 应扫到文档: processed=%d ready=%d", res.Processed, res.Ready)
	}
	if got := f.reloadDoc(t); got.BuildState != model.KBDocumentBuildReady {
		t.Fatalf("全量重切后应 ready（零变更）：%s", got.BuildState)
	}
}

// ==================== 匹配面（match_text）落库与召回对齐（三面同切） ====================

// TestMatchTextDerivedRecallDB 子节标题词面召回（真实导入管道 → 落块 → 词面腿）：
//   - 每行 match_text = ChunkTextOf 口径（标题路径前缀 + verbatim 正文，写读同源单点）；
//   - 祖先标题词只存在于派生面（节块 verbatim 正文不含祖先标题）——旧口径（词面腿匹配 verbatim）不可达；
//   - tsv 为 DB 生成列（恒自 match_text 派生）⇒ 祖先标题词经 FTS 词面可达。
func TestMatchTextDerivedRecallDB(t *testing.T) {
	f := newPipelineFixture(t, pipelineFixtureMd)
	ctx := f.ctx

	if _, err := f.svc.RunIngest(ctx, ingest.IngestParams{DocumentID: f.docID}, nil); err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	rows := f.chunkRows(t, "")
	var sub *model.KBChunk
	for i := range rows {
		if strings.Contains(rows[i].Text, "总价为人民币壹万元整") {
			sub = &rows[i]
			break
		}
	}
	if sub == nil {
		t.Fatalf("未找到「第二条 价款」节块：%+v", rows)
	}
	// ① 派生列 = ChunkTextOf 口径（标题路径（祖先链）前缀 + verbatim 正文；夹具标题行 = 一级「合同标题」/ 二级「第二条 价款」）
	want := "合同标题 > 第二条 价款\n" + sub.Text
	if sub.MatchText != want {
		t.Fatalf("match_text 应为派生文本（ChunkTextOf 口径）：got=%q want=%q", sub.MatchText, want)
	}
	// ② 祖先标题词只在派生面（verbatim 正文不含）
	if strings.Contains(sub.Text, "合同标题") {
		t.Fatalf("夹具前提失据：verbatim 正文不应含祖先标题词：%q", sub.Text)
	}
	// ③ tsv 为 DB 生成列（自 match_text 派生）：祖先标题词经 FTS 词面可达
	var n int64
	if err := f.db.Raw("SELECT count(*) FROM kb_chunk WHERE id = ? AND tsv @@ websearch_to_tsquery('simple', ?)",
		sub.ID, "合同标题").Scan(&n).Error; err != nil {
		t.Fatalf("tsv 探询失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("祖先标题词应经生成 tsv 命中（n=%d）", n)
	}
}

// TestMatchTextTableHeaderRecallDB 表头词面召回（真实导入管道 · table 策略）：
// CSV 首块含表头行（逐字），**非首块** verbatim 正文不含表头行——表头只进派生文本（header_repeat；
// 锚逐字纪律）⇒ 旧口径（词面腿匹配 verbatim）在非首块搜表头词不可达，本批 exact/fts 经
// match_text / 生成 tsv 可达。夹具 60 数据行 > rows_per_chunk(50) ⇒ 恰两块（第二块 = 表头只在派生面）。
func TestMatchTextTableHeaderRecallDB(t *testing.T) {
	var csv strings.Builder
	csv.WriteString("name,qty\n")
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&csv, "苹果%d,%d\n", i, i)
	}
	f := newPipelineFixtureFile(t, "fixture.csv", "text/csv", csv.String())
	ctx := f.ctx

	if _, err := f.svc.RunIngest(ctx, ingest.IngestParams{DocumentID: f.docID}, nil); err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	rows := f.chunkRows(t, "")
	var tail *model.KBChunk
	for i := range rows {
		// 非首块：verbatim 正文不含表头词，派生面含（表头重复进派生文本）
		if !strings.Contains(rows[i].Text, "qty") && strings.Contains(rows[i].MatchText, "qty") {
			tail = &rows[i]
			break
		}
	}
	if tail == nil {
		t.Fatalf("未找到「表头只在派生面」的块（夹具 60 行 ⇒ 第二块起）：%+v", rows)
	}
	if !strings.HasPrefix(tail.MatchText, "name,qty\n") {
		t.Fatalf("派生文本应前置表头：%q", tail.MatchText)
	}
	// ① exact 腿（匹配 match_text）：表头词可达（命中集含 tail 块）
	hits, err := repoKB.NewStore().SearchExact(ctx, []uint{f.libID}, "%qty%", 100, nil)
	if err != nil {
		t.Fatalf("exact 腿失败: %v", err)
	}
	found := false
	for _, h := range hits {
		if h.ChunkID == tail.ID {
			found = true
			if strings.Contains(h.Text, "qty") {
				t.Fatalf("命中行 Text 应仍是 verbatim 正文（不含重复表头）：%q", h.Text)
			}
		}
	}
	if !found {
		t.Fatalf("exact 腿应按 match_text 召回非首块的表头词：%+v", hits)
	}
	// ② fts 腿（生成 tsv 自 match_text）：表头词可达（命中集含 tail 块）
	ftsHits, err := repoKB.NewStore().SearchFTS(ctx, []uint{f.libID}, "qty", "simple", 100, nil)
	if err != nil {
		t.Fatalf("fts 腿失败: %v", err)
	}
	found = false
	for _, h := range ftsHits {
		if h.ChunkID == tail.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("fts 腿应按生成 tsv 召回非首块的表头词：%+v", ftsHits)
	}
}

// countExtracts 该文档的提取物行数（含复用计数）
func (f *pipelineFixture) countExtracts(t *testing.T) (*ingest.ExtractRow, int64) {
	t.Helper()
	n, err := gorm.G[ingest.ExtractRow](f.db).Where("document_id = ?", f.docID).Count(f.ctx, "id")
	if err != nil {
		t.Fatalf("统计提取物失败: %v", err)
	}
	return nil, n
}

// extractText 读回提取文本（无行 t.Fatal）
func (f *pipelineFixture) extractText(t *testing.T) string {
	t.Helper()
	row, err := gorm.G[ingest.ExtractRow](f.db).Where("document_id = ?", f.docID).First(f.ctx)
	if err != nil {
		t.Fatalf("读提取物失败: %v", err)
	}
	return row.Text
}

// chunkIDs 行 id 序列
func chunkIDs(rows []model.KBChunk) []uint {
	out := make([]uint, 0, len(rows))
	for i := range rows {
		out = append(out, rows[i].ID)
	}
	return out
}

// TestParsePurgeKeyUnit 清理键解析（纯单元；无库）
func TestParsePurgeKeyUnit(t *testing.T) {
	cases := []struct {
		key     string
		lib, dc uint
		bad     bool
	}{
		{"kb:7", 7, 0, false},
		{"kb:doc:9", 0, 9, false},
		{"kb:", 0, 0, true},
		{"kb:doc:", 0, 0, true},
		{"kb:doc:0", 0, 0, true},
		{"kb:x", 0, 0, true},
		{"", 0, 0, true},
		{"doc:9", 0, 0, true},
	}
	for _, c := range cases {
		lib, dc, err := parsePurgeKey(c.key)
		if c.bad {
			if err == nil {
				t.Fatalf("键 %q 应判非法", c.key)
			}
			continue
		}
		if err != nil || lib != c.lib || dc != c.dc {
			t.Fatalf("键 %q 解析不符: lib=%d doc=%d err=%v", c.key, lib, dc, err)
		}
	}
}

// TestProvisionalBuildUnit 键档 build 的退路语义（无 blob 登记 ⇒ 退内容身份；不触库表变化）
func TestProvisionalBuildUnit(t *testing.T) {
	f := newPipelineFixture(t, pipelineFixtureMd)
	doc := f.reloadDoc(t)
	build, err := f.svc.provisionalBuild(context.Background(), doc)
	if err != nil {
		t.Fatalf("键档 build 失败: %v", err)
	}
	if build == "" {
		t.Fatal("键档 build 不应为空")
	}
	// 与作业内实际 build 可不同（元数据档），但必须确定（重复调用同值）
	again, err := f.svc.provisionalBuild(context.Background(), doc)
	if err != nil || again != build {
		t.Fatalf("键档 build 应确定: %q vs %q (%v)", build, again, err)
	}
	// 未登记原始件的文档：退内容身份（不报错）
	doc2 := &model.KBDocument{ID: doc.ID + 999999, ContentHash: "deadbeef"}
	fallback, err := f.svc.provisionalBuild(context.Background(), doc2)
	if err != nil || fallback != "deadbeef" {
		t.Fatalf("无原始件应退内容身份: %q %v", fallback, err)
	}
	// 判型失败也不报错：mime 空 + 名字无扩展名（pdf/图片/音视频的类级判型已登记，此路径保留防回归）
	doc3 := &model.KBDocument{ID: doc.ID, ContentHash: "cafebabe", Title: "无扩展名", SourceRef: "x"}
	if _, err := f.svc.provisionalBuild(context.Background(), doc3); err != nil {
		t.Fatalf("判型失败应退内容身份（不报错）: %v", err)
	}
	// pdf 提取器身份已登记（键档 build 走 kb-pdf@v1）
	if _, _, err := splitExtractorID(ingest.FormatPDF); err != nil {
		t.Fatalf("pdf 提取器身份应已登记: %v", err)
	}
}

// TestSplitExtractorIDUnit 提取器身份拆解（纯单元）
func TestSplitExtractorIDUnit(t *testing.T) {
	name, version, err := splitExtractorID("md")
	if err != nil || name != "kb-md" || version != "v1" {
		t.Fatalf("拆解不符: %s %s %v", name, version, err)
	}
	// pdf 已登记（文本层提取器 kb-pdf@v1）
	if name, version, err := splitExtractorID(ingest.FormatPDF); err != nil || name != "kb-pdf" || version != "v1" {
		t.Fatalf("pdf 提取器身份不符: %s %s %v", name, version, err)
	}
	if _, _, err := splitExtractorID("exe"); !errors.Is(err, ingest.ErrUnsupportedFormat) {
		t.Fatalf("未登记格式应响亮失败: %v", err)
	}
}
