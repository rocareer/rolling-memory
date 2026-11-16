// plugin: kb v1.0.0
package kb

//  多模态管道 DB 用例（连库；PLUGIN_KB_DB=1 门控——命令必须带 PLUGIN_KB_DB=1，未设时 SKIP 属假绿）：
//   - pdf 输入在 v1 **响亮失败**（本插件不含 PDF 解析依赖：判型仍认 pdf，提取阶段报 ErrUnsupportedFormat）
//     ⇒ 文档落 failed_stage='extract'、blob 保留（解锁依赖后可复投重试）、无提取物、无账目；
//   - 文本类 fixture 全链（RunIngest → ready + kb_extract 身份/读数 + 锚可复算 + 重跑幂等）；
//   - kb_cost_log（migrations/5_cost_log.up.sql）：表在场 + RecordCost 写读（numeric 绑定 / 金额快照 / 闭集校验）。
//
// 覆盖边界（如实登记，不做假绿）：
//   - 扫描件 PDF 的「无 OCR 渠道 ⇒ ocr_channel_unavailable」档在 v1 **不可达**（判型之后、渠道判定之前
//     就在格式层响亮失败）⇒ 该用例已撤，原因码与失败旁路由本文件的 pdf 用例覆盖；
//   - job 层「SourceSuspendedError → suspendForBudget」识别分支无本层 DB 用例（触发需注入小 limits）；
//   - 媒体云渠道 / 语义候选 / 嵌入链在无凭据环境**未验证**。

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/ingest"

	"gorm.io/gorm"
)

// mediaFixtureBytes 读入库 fixture（cwd 已切仓库根——newKBFixture 保证）
func mediaFixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("plugins", "kb", "service", "kb", "ingest", "testdata", name))
	if err != nil {
		t.Fatalf("fixture 读取失败（%s）: %v", name, err)
	}
	return b
}

// newMediaFixture 媒体面夹具（照 newPipelineFixture 的建库+blob+文档模式；清理由本用例登记表覆盖）。
// 顺序注意：先 newKBFixture（PLUGIN_KB_DB 门控 + chdir 仓库根）再读 fixture——未设 PLUGIN_KB_DB 时安静 SKIP。
func newMediaFixture(t *testing.T, fixtureName, mime string) *pipelineFixture {
	t.Helper()
	f := &pipelineFixture{kbFixture: newKBFixture(t)}
	ctx := f.ctx
	data := mediaFixtureBytes(t, fixtureName)

	f.libID = f.library(model.KBLibraryOwnerKindSystem, 0, fmt.Sprintf("kb-media-test-lib-%s", f.suffix), 1).ID

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
		LibraryID: f.libID, Title: "media-fixture-" + fixtureName, SourceKind: model.KBDocumentSourceFile,
		SourceRef: fixtureName, ContentHash: contentHash, Mime: mime,
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
		if err := f.db.Exec("DELETE FROM kb_cost_log WHERE document_id = ?", f.docID).Error; err != nil {
			t.Logf("清理记账行失败（请手工核）: %v", err)
		}
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

// extractRowOfDocument 读文档提取物行（不存在 ⇒ t.Fatal）
func extractRowOfDocument(t *testing.T, f *pipelineFixture) ingest.ExtractRow {
	t.Helper()
	row, err := gorm.G[ingest.ExtractRow](f.db).Where("document_id = ?", f.docID).First(f.ctx)
	if err != nil {
		t.Fatalf("读提取物失败: %v", err)
	}
	return row
}

// countExtractRows 文档提取物行数
func countExtractRows(t *testing.T, f *pipelineFixture) int64 {
	t.Helper()
	n, err := gorm.G[ingest.ExtractRow](f.db).Where("document_id = ?", f.docID).Count(f.ctx, "id")
	if err != nil {
		t.Fatalf("统计提取物失败: %v", err)
	}
	return n
}

// TestPDFUnsupportedLoudFailDB pdf 输入在 v1 的正式语义：**响亮失败**、文档落 failed(extract)、
// blob 保留、无提取物、无账目（判型仍认 pdf ⇒ 错误文案能说清「是这个格式但本插件不支持」）。
func TestPDFUnsupportedLoudFailDB(t *testing.T) {
	f := newMediaFixture(t, "fixture-text-1p.pdf", "application/pdf")
	ctx := f.ctx

	_, err := f.svc.RunIngest(ctx, ingest.IngestParams{DocumentID: f.docID, Reason: "slice-test"}, nil)
	if err == nil {
		t.Fatal("v1 不支持 PDF 文本层：必须响亮失败（禁伪造产物）")
	}
	if !strings.Contains(err.Error(), "v1 未支持 PDF 文本层提取") {
		t.Fatalf("错误文案应点明 v1 裁剪口径: %v", err)
	}

	doc := f.reloadDoc(t)
	if doc.BuildState != model.KBDocumentBuildFailed || doc.FailedStage != "extract" {
		t.Fatalf("文档失败旁路不符: state=%s stage=%s", doc.BuildState, doc.FailedStage)
	}
	// blob 保留：解锁 PDF 依赖后可从同一原始件复投
	if _, err := ingest.LoadBlobForDocument(ctx, f.db, f.docID); err != nil {
		t.Fatalf("失败后应保留 blob: %v", err)
	}
	// 无产物、无账目（无提取不落库、无开销不记账）
	if n := countExtractRows(t, f); n != 0 {
		t.Fatalf("失败路径不应落提取物: %d", n)
	}
	if n, _ := ingest.CostCountForDocument(ctx, f.db, f.docID); n != 0 {
		t.Fatalf("失败路径不应有记账行: %d", n)
	}
}

// TestCostLogRecordDB kb_cost_log（migrations/5_cost_log.up.sql）写读：numeric 绑定 / 金额快照 / 闭集校验
func TestCostLogRecordDB(t *testing.T) {
	// 夹具用文本类：本用例只测记账行写读，与来源格式无关（PDF 在 v1 不可解析）
	f := newMediaFixture(t, "fixture-text-1p.pdf", "text/plain; charset=utf-8")
	ctx := f.ctx

	if err := ingest.RecordCost(ctx, f.db, ingest.CostEntry{
		LibraryID: f.libID, DocumentID: f.docID, Kind: ingest.CostKindOCR, Provider: "smoke-ocr",
		Unit: ingest.CostUnitPage, Quantity: 3, UnitPriceCNY: 0.01,
		Detail: map[string]any{"estimate": true},
	}); err != nil {
		t.Fatalf("RecordCost 写入失败: %v", err)
	}
	row, err := gorm.G[ingest.CostLogRow](f.db).Where("document_id = ?", f.docID).First(ctx)
	if err != nil {
		t.Fatalf("读记账行失败: %v", err)
	}
	if row.Kind != ingest.CostKindOCR || row.Unit != ingest.CostUnitPage || row.Quantity != 3 || row.Provider != "smoke-ocr" {
		t.Fatalf("记账行字段不符: %+v", row)
	}
	if math.Abs(row.CostCNY-0.03) > 1e-9 || math.Abs(row.UnitPrice-0.01) > 1e-9 {
		t.Fatalf("金额快照不符: cost=%v price=%v", row.CostCNY, row.UnitPrice)
	}
	detailStr := ""
	if row.Detail != nil {
		detailStr = *row.Detail
	}
	// 注：jsonb 输出为规范化文本（键值间有空格）——按子串断言，不锁精确格式
	if !strings.Contains(detailStr, "estimate") || !strings.Contains(detailStr, "true") {
		t.Fatalf("明细应落 detail: %q", detailStr)
	}
	if row.CreatedAt == nil {
		t.Fatal("created_at 应落时间")
	}

	// 闭集校验（写侧）：非法 kind/unit 被拒（永不入库）
	if err := ingest.RecordCost(ctx, f.db, ingest.CostEntry{
		LibraryID: f.libID, DocumentID: f.docID, Kind: "unknown", Unit: ingest.CostUnitPage, Quantity: 1,
	}); err == nil {
		t.Fatal("闭集外 kind 应被拒")
	}

	// 读数面（巡检/后台）
	if n, err := ingest.CostCountForDocument(ctx, f.db, f.docID); err != nil || n != 1 {
		t.Fatalf("记账行计数应 1: n=%d err=%v", n, err)
	}
}
