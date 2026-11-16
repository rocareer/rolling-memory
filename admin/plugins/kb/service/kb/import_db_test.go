// plugin: kb v1.0.0
// 文档导入用例（DB 门控）：入库入口是**本插件新增**的一环——宿主核心没有它，而没有它插件装完就是空库、
// 核心闭环不成立。本文件钉住导入的语义：
//
//	① 幂等：同库同内容重复导入 ⇒ created=false 且回既有文档（不新建、不重复投递作业）；
//	② 写谓词门：无权（读视图解析失败 / 无写权）时 ErrDenied 同型（不泄露库存在性）；
//	③ 身份口径：kb_blob 键 = sha256、kb_document.content_hash = sha1（40 字符列）、
//	   同事务写审计（action=import，含 title/size/mime/content_hash）；
//	④ 投递：事务提交后投递 kb:ingest 作业（同键单飞；created_job 如实回显）；
//	⑤ 入参形态：文件身（含判型）与文本身两条入口合一。
//
// 跑法（隔离库，库名须含 kb_test）：
//
//	createdb -h <主机> -p <端口> -U <用户> aigo_kb_test
//	DATABASE_WRITE_DBNAME=aigo_kb_test PLUGIN_KB_DB=1 go -C ai-go-admin test -count=1 -p 1 ./plugins/kb/service/kb/
package kb

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
)

// TestImportDocumentDB 导入的幂等 / 身份 / 审计 / 投递语义
func TestImportDocumentDB(t *testing.T) {
	f := newKBFixture(t)
	ctx := f.ctx
	svc := f.svc
	actor := Actor{Kind: model.KBActorKindAdmin, ID: f.adminID}

	lib := f.library(model.KBLibraryOwnerKindSystem, 0, "kb-import-lib-"+f.suffix, 1)
	text := "# 导入用例标题\n\n第一段正文，用于词面腿命中。\n"

	in := ImportInput{LibraryID: lib.ID, Name: "kb-import-doc-" + f.suffix, Text: text}
	first, err := svc.ImportDocument(ctx, actor, in)
	if err != nil {
		t.Fatalf("首次导入失败: %v", err)
	}
	if !first.Created || first.DocumentID == 0 {
		t.Fatalf("首次导入应新建文档：%+v", first)
	}
	if first.BuildState != model.KBDocumentBuildRegistered {
		t.Fatalf("新建文档初始态应 registered，实际 %q", first.BuildState)
	}
	if first.JobID == 0 || first.JobKey == "" {
		t.Fatalf("导入应投递 kb:ingest 作业：%+v", first)
	}
	f.docIDs = append(f.docIDs, first.DocumentID)

	// 身份口径：blob 键 = sha256；文档内容身份 = sha1（char(40)）
	sum256 := sha256.Sum256([]byte(text))
	sum1 := sha1.Sum([]byte(text))
	doc, err := svc.GetDocument(ctx, mustAdminViewImport(t, svc, f.adminID), first.DocumentID)
	if err != nil {
		t.Fatalf("读文档失败: %v", err)
	}
	if doc.ContentHash != hex.EncodeToString(sum1[:]) {
		t.Fatalf("content_hash 应为原文 sha1，实际 %q", doc.ContentHash)
	}
	var blob string
	if err := f.db.Raw("SELECT sha256 FROM "+tableOf(f.db, &blobProbe{}, "kb_blob")+" WHERE sha256 = ?",
		hex.EncodeToString(sum256[:])).Scan(&blob).Error; err != nil {
		t.Fatalf("读原始件失败: %v", err)
	}
	if blob == "" {
		t.Fatal("原始件未按 sha256 落 kb_blob")
	}

	// 审计：action=import 与业务同事务
	var audits int64
	if err := f.db.Raw("SELECT COUNT(*) FROM "+f.svc.tAudit+" WHERE document_id = ? AND action = ?",
		first.DocumentID, model.KBAuditActionImport).Scan(&audits).Error; err != nil {
		t.Fatalf("读审计失败: %v", err)
	}
	if audits != 1 {
		t.Fatalf("导入应落一条 import 审计，实际 %d 条", audits)
	}

	// 幂等：同库同内容重复导入 ⇒ created=false 且同一个文档
	second, err := svc.ImportDocument(ctx, actor, in)
	if err != nil {
		t.Fatalf("重复导入失败: %v", err)
	}
	if second.Created || second.DocumentID != first.DocumentID {
		t.Fatalf("同库同内容应幂等回既有文档：%+v（首次 %d）", second, first.DocumentID)
	}

	// 不同内容 ⇒ 新文档
	third, err := svc.ImportDocument(ctx, actor, ImportInput{
		LibraryID: lib.ID, Name: "kb-import-doc2-" + f.suffix, Text: text + "补充一段。",
	})
	if err != nil {
		t.Fatalf("二次导入失败: %v", err)
	}
	if !third.Created || third.DocumentID == first.DocumentID {
		t.Fatalf("不同内容应新建文档：%+v", third)
	}
	f.docIDs = append(f.docIDs, third.DocumentID)

	// 用户轴：① 端口已接入但主体不存在 ⇒ 主体不可用（ErrActorUnavailable，仍属 ErrDenied 族）；
	// ② 端口未接入（未接用户体系的部署）⇒ fail-closed（ErrFailClosed）
	if _, err := svc.ImportDocument(ctx, Actor{Kind: model.KBActorKindUser, ID: 990001}, in); !errors.Is(err, ErrActorUnavailable) {
		t.Fatalf("端口已接入但主体不存在时应拒（ErrActorUnavailable），得到: %v", err)
	}
	RegisterPrincipal(nil)
	if _, err := svc.ImportDocument(ctx, Actor{Kind: model.KBActorKindUser, ID: 990001}, in); !errors.Is(err, ErrFailClosed) {
		t.Fatalf("无用户体系时 user 轴导入应 fail-closed，得到: %v", err)
	}
}

// TestImportDocumentFileDB 文件身入口：判型通过即入库（p0 = txt），未登记格式在入口响亮拒绝
func TestImportDocumentFileDB(t *testing.T) {
	f := newKBFixture(t)
	ctx := f.ctx
	svc := f.svc
	actor := Actor{Kind: model.KBActorKindAdmin, ID: f.adminID}
	lib := f.library(model.KBLibraryOwnerKindSystem, 0, "kb-import-file-lib-"+f.suffix, 1)

	res, err := svc.ImportDocument(ctx, actor, ImportInput{
		LibraryID: lib.ID,
		FileName:  "kb-import-" + f.suffix + ".txt",
		Data:      []byte("纯文本内容，走文件身入口。\n"),
	})
	if err != nil {
		t.Fatalf("文件导入失败: %v", err)
	}
	f.docIDs = append(f.docIDs, res.DocumentID)

	doc, err := svc.GetDocument(ctx, mustAdminViewImport(t, svc, f.adminID), res.DocumentID)
	if err != nil {
		t.Fatalf("读文档失败: %v", err)
	}
	if doc.SourceKind != model.KBDocumentSourceFile {
		t.Fatalf("文件导入的 source_kind 应为 file，实际 %q", doc.SourceKind)
	}
	if doc.Mime == "" {
		t.Fatal("文件导入应带 MIME（按判型推断）")
	}

	// 未登记格式（既非文本类也非多模态四类）：入口即拒，不落注定失败的行
	if _, err := svc.ImportDocument(ctx, actor, ImportInput{
		LibraryID: lib.ID,
		FileName:  "unknown-" + f.suffix + ".bin",
		Data:      []byte("\x00\x01\x02binary-ish"),
	}); err == nil {
		t.Fatal("未登记格式应响亮拒绝")
	}
}

// blobProbe kb_blob 表名解析探针（与模型 TableName() 同源：raw SQL 禁裸写表名）
type blobProbe struct{}

// TableName 探针表名
func (blobProbe) TableName() string { return "kb_blob" }

// mustAdminViewImport 取后台读视图（用例助手；名字与同包其它用例助手区分）
func mustAdminViewImport(t *testing.T, svc *Service, adminID uint) *ReadView {
	t.Helper()
	view, err := svc.AdminView(t.Context(), adminID)
	if err != nil {
		t.Fatalf("构造 admin 读视图失败: %v", err)
	}
	return view
}
