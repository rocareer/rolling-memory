// plugin: kb v1.0.0
// 知识库文档导入服务（**本插件新增**：宿主核心没有入库入口，没有它装完就是空库、核心闭环不成立）。
//
// 一条入库路径，两种入参形态（文本身 / 文件字节 + 名称）在下游合一——都落到同一段逻辑：
//
//  1. RequireWrite(view, library_id)：ACL 单点（库**写**谓词：owner ∪ write 授权 ∪ 后台轴）；
//  2. 内容身份：sha256 = 原始件身份（kb_blob 主键）/ sha1 = 文档内容身份
//     （kb_document.content_hash 是 char(40) 的 sha1，与同库同内容的幂等键 uk(library_id, content_hash) 成对）；
//  3. 单事务：写 kb_blob（ON CONFLICT DO NOTHING，重复内容不重写）→ 写 kb_document
//     （**同库同内容已有存活文档 ⇒ 直接回既有文档，不新建**，幂等）→ 写 kb_blob_ref（登记引用）→
//     同事务写审计（action=import，含 title/size/mime/content_hash）；
//  4. 事务提交后投递 kb:ingest 作业（键 kb:ingest:<document_id>:<build_hash>，复用既有入队单点）。
//
// 纪律：本层不做解析/切片（那是作业侧的事）；一切写面经 ACL 单点与审计单点，禁旁路。
package kb

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/ingest"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ImportInput 导入入参（文本与文件两条入口共用）
type ImportInput struct {
	LibraryID uint   // 目标库（必填）
	Name      string // 文档标题（空则回落来源名/「未命名文本」）
	Text      string // 文本内容（source_kind=text）
	Data      []byte // 文件字节（source_kind=file；与 Text 二选一，Data 优先）
	FileName  string // 文件名（source_kind=file 的来源引用；也是判型输入）
	MIME      string // MIME（空则由判型推断）
}

// ImportResult 导入回执
type ImportResult struct {
	DocumentID uint   `json:"document_id"`
	Created    bool   `json:"created"`     // false = 同库同内容已存在（幂等命中既有文档）
	JobID      uint64 `json:"job_id"`      // 投递的导入作业 id（0 = 未投递，见 job_error）
	JobKey     string `json:"job_key"`     // 作业幂等键
	CreatedJob bool   `json:"created_job"` // false = 同键活跃作业已在飞
	BuildState string `json:"build_state"` // 文档当前构建状态（新建时为 registered）
}

// ImportDocument 导入一份内容到库（幂等：同库同内容返回既有文档）
func (s *Service) ImportDocument(ctx context.Context, actor Actor, in ImportInput) (*ImportResult, error) {
	if in.LibraryID == 0 {
		return nil, errors.New("kb: 导入缺 library_id")
	}
	view, err := s.BuildReadView(ctx, actor.Kind, actor.ID)
	if err != nil {
		return nil, err
	}
	// 写谓词门（ACL 单点）：无写权 = ErrDenied（HTTP 层同型 404，不泄露库存在性）
	if err := s.RequireWrite(ctx, view, in.LibraryID); err != nil {
		return nil, err
	}

	data, sourceKind, sourceRef, title, mime, err := normalizeImport(in)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, errors.New("kb: 导入内容为空（文本与文件至少给一项）")
	}

	sum256 := sha256.Sum256(data)
	blobSHA := hex.EncodeToString(sum256[:])
	sum1 := sha1.Sum(data)
	contentHash := hex.EncodeToString(sum1[:])

	result := &ImportResult{}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// ① 同库同内容幂等命中 ⇒ 直接回既有文档（不新建、不重复投递）
		existing, err := gorm.G[model.KBDocument](tx).
			Select(documentDetailColumns).
			Where("library_id = ? AND content_hash = ?", in.LibraryID, contentHash).
			First(ctx)
		if err == nil {
			result.DocumentID = existing.ID
			result.BuildState = existing.BuildState
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("kb: 导入幂等查重失败: %w", err)
		}

		// ② 原始件（内容寻址；重复内容不重写字节）
		blob := ingest.BlobRow{SHA256: blobSHA, MIME: mime, Size: int64(len(data)), Bytes: data}
		if err := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).
			Table(tableOf(s.db, &ingest.BlobRow{}, "kb_blob")).Create(&blob).Error; err != nil {
			return fmt.Errorf("kb: 写原始件失败: %w", err)
		}

		// ③ 文档行（初始态 registered；内容身份 = sha1）
		doc := model.KBDocument{
			LibraryID:     in.LibraryID,
			Title:         title,
			SourceKind:    sourceKind,
			SourceRef:     sourceRef,
			ContentHash:   contentHash,
			Mime:          mime,
			Size:          int64(len(data)),
			BuildState:    model.KBDocumentBuildRegistered,
			CreatedByKind: actor.Kind,
			CreatedByID:   actor.ID,
		}
		if err := gorm.G[model.KBDocument](tx).Create(ctx, &doc); err != nil {
			if isUniqueViolation(err) {
				// 并发导入同内容：另一事务已建 ⇒ 回既有文档（幂等，不报错）
				row, findErr := gorm.G[model.KBDocument](tx).
					Select(documentDetailColumns).
					Where("library_id = ? AND content_hash = ?", in.LibraryID, contentHash).
					First(ctx)
				if findErr != nil {
					return fmt.Errorf("kb: 并发导入后回读文档失败: %w", findErr)
				}
				result.DocumentID = row.ID
				result.BuildState = row.BuildState
				return nil
			}
			return fmt.Errorf("kb: 写文档失败: %w", err)
		}
		result.DocumentID = doc.ID
		result.Created = true
		result.BuildState = doc.BuildState

		// ④ 引用登记（共享件引用计数面；同件重复登记忽略）
		ref := ingest.BlobRefRow{LibraryID: in.LibraryID, DocumentID: doc.ID, SHA256: blobSHA}
		if err := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).
			Table(tableOf(s.db, &ingest.BlobRefRow{}, "kb_blob_ref")).Create(&ref).Error; err != nil {
			return fmt.Errorf("kb: 登记原始件引用失败: %w", err)
		}

		// ⑤ 审计（与业务同事务；导入动作在闭集内）
		return writeAudit(ctx, tx, AuditEntry{
			LibraryID:  in.LibraryID,
			DocumentID: doc.ID,
			Actor:      actor,
			Action:     model.KBAuditActionImport,
			Detail: map[string]any{
				"title":        title,
				"size":         int64(len(data)),
				"mime":         mime,
				"content_hash": contentHash,
			},
		})
	})
	if err != nil {
		return nil, err
	}

	// ⑥ 投递导入作业（幂等命中时不重复投递；失败不回滚已落库的文档——重建入口可复投）
	if result.Created {
		job, createdJob, err := s.EnqueueIngestDocument(ctx, result.DocumentID)
		if err != nil {
			return nil, fmt.Errorf("kb: 文档已入库但投递导入作业失败（可用重建入口复投）: %w", err)
		}
		if job != nil {
			result.JobID, result.JobKey, result.CreatedJob = job.ID, job.Key, createdJob
		}
	}
	return result, nil
}

// normalizeImport 归一入参：确定内容字节、来源类型、来源引用、标题与 MIME。
//   - 给了文件字节 ⇒ source_kind=file（来源引用 = 文件名；MIME 空则按判型推断）；
//   - 只给文本 ⇒ source_kind=text（来源引用 = 标题；MIME = text/plain; charset=utf-8）。
//
// 文件路径**在入口先判型**（ingest.DetectFormat 单点）：格式不在提取闭集 ⇒ 入库前就响亮失败，
// 不落一份注定提取失败的行；判型通过但 v1 不支持提取的格式（如 pdf）照常入库，
// 由作业侧在提取阶段响亮失败（failed_stage='extract'），错误文案能说清是哪个格式。
func normalizeImport(in ImportInput) (data []byte, sourceKind, sourceRef, title, mime string, err error) {
	fileName := strings.TrimSpace(in.FileName)
	title = strings.TrimSpace(in.Name)
	if len(in.Data) > 0 {
		format, err := ingest.DetectFormat(fileName, strings.TrimSpace(in.MIME), in.Data)
		if err != nil {
			return nil, "", "", "", "", err
		}
		data = in.Data
		sourceKind = model.KBDocumentSourceFile
		sourceRef = fileName
		if title == "" {
			title = fileName
		}
		mime = strings.TrimSpace(in.MIME)
		if mime == "" {
			mime = mimeForFormat(format)
		}
		return data, sourceKind, sourceRef, title, mime, nil
	}
	data = []byte(in.Text)
	sourceKind = model.KBDocumentSourceText
	if title == "" {
		title = "未命名文本"
	}
	sourceRef = title
	return data, sourceKind, sourceRef, title, "text/plain; charset=utf-8", nil
}

// mimeForFormat 格式 → MIME（仅用于文档行的展示字段；类级三格式无固定 MIME ⇒ 留空，
// 提取与判型都不依赖本字段）
func mimeForFormat(format string) string {
	switch format {
	case ingest.FormatTxt:
		return "text/plain; charset=utf-8"
	case ingest.FormatMd:
		return "text/markdown; charset=utf-8"
	case ingest.FormatHTML:
		return "text/html; charset=utf-8"
	case ingest.FormatCSV:
		return "text/csv; charset=utf-8"
	case ingest.FormatDocx:
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ingest.FormatPDF:
		return "application/pdf"
	default:
		return ""
	}
}
