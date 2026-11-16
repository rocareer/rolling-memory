// plugin: kb v1.0.0
// 知识库作业接线 · kb:ingest 单文档链（处理器 + 全链 + 阶段旁路）。
//
// 从 job.go 拆出（文件规模纪律：job.go 接线链逼近 1024 行截点——按职责拆分而非按行数硬切）；
// 共享装配面（注册/入队/键档）见 job.go。
package kb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/service/jobs"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/chunk"
	kbembed "github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/embed"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/ingest"

	"gorm.io/gorm"
)

// ==================== kb:ingest 处理器 ====================

// ingestHandler kb:ingest 处理器（单文档全链；可重入）
type ingestHandler struct{}

// Run 执行一次单文档导入
func (ingestHandler) Run(ctx context.Context, j *jobs.Job) error {
	var p ingest.IngestParams
	if err := json.Unmarshal(j.Params, &p); err != nil {
		return fmt.Errorf("kb:ingest 参数解析失败: %w", err)
	}
	if p.DocumentID == 0 {
		return errors.New("kb:ingest 缺 document_id（拒绝无参作业）")
	}
	svc := NewService()
	res, err := svc.RunIngest(ctx, p, j)
	if err != nil {
		return err
	}
	if res != nil {
		if b, err := json.Marshal(res); err == nil {
			j.Result(b)
		}
	}
	return nil
}

// IngestResult 单文档导入回执（作业结果 JSON；状态闭集 ready|suspended|canceled|skipped）
type IngestResult struct {
	DocumentID uint                    `json:"document_id"`
	State      string                  `json:"state"`
	BuildHash  string                  `json:"build_hash,omitempty"`
	Chunks     int                     `json:"chunks"`      // 本次切分块数
	ChunkRows  int                     `json:"chunk_rows"`  // 新写入行数（0 = 复用既有——同 build 重跑）
	Embedded   int                     `json:"embedded"`    // 新落向量行数
	RemovedOld int64                   `json:"removed_old"` // 换出时删除的旧 build 切片行数
	RuleTags   int64                   `json:"rule_tags"`   // 规则打标新落 link 行数（0 = 无命中/幂等命中；T3）
	Estimate   *ingest.BudgetSuspended `json:"estimate,omitempty"`
	Embed      *kbembed.Stats          `json:"embed,omitempty"`
	Media      *MediaReadings          `json:"media,omitempty"` // 媒体面提取读数（纯文本类省略）
	Warnings   []string                `json:"warnings,omitempty"`
}

// MediaReadings 媒体面读数（pdf/图片/音频/视频文档的提取读数回执；读数源 = kb_extract.stats）
type MediaReadings struct {
	Format  string  `json:"format"` // 类级格式（pdf/image/audio/video）
	Kind    string  `json:"kind"`   // 提取物 kind（text/ocr/asr）
	Pages   int     `json:"pages,omitempty"`
	Minutes float64 `json:"minutes,omitempty"`
	Frames  int     `json:"frames,omitempty"`
}

// mediaReadingsOf 媒体面读数组装（非媒体格式返回 nil；读数缺省为零值）
func mediaReadingsOf(format string, ext *ingest.Extract) *MediaReadings {
	if ext == nil {
		return nil
	}
	switch format {
	case ingest.FormatPDF, ingest.FormatImage, ingest.FormatAudio, ingest.FormatVideo:
	default:
		return nil
	}
	return &MediaReadings{
		Format:  format,
		Kind:    ext.Kind,
		Pages:   int(ingest.StatFloat(ext.Stats, "pages")),
		Minutes: ingest.StatFloat(ext.Stats, "minutes"),
		Frames:  int(ingest.StatFloat(ext.Stats, "frames")),
	}
}

// RunIngest 单文档导入主链（提取 → 切分 → 预算预检 → 落块 → 嵌入 → 换出）。
//
// 错误语义：返回 error ⇒ 作业失败（worker 退避重试；重跑靠幂等链续跑，不会重复烧钱）；
// 文档侧失败另落 failed_stage/failed_reason（响亮失败：零文本/乱码/嵌入失败）。
func (s *Service) RunIngest(ctx context.Context, p ingest.IngestParams, j *jobs.Job) (*IngestResult, error) {
	doc, err := s.loadDocument(ctx, p.DocumentID)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		// 已删/不存在：清理交 kb:purge；本作业静默收工（不落任何状态）
		return &IngestResult{DocumentID: p.DocumentID, State: "skipped"}, nil
	}
	if done, err := s.stopIfCanceled(ctx, doc); err != nil || done {
		if err != nil {
			return nil, err
		}
		return &IngestResult{DocumentID: doc.ID, State: "canceled"}, nil
	}
	progress := func(stage int64) {
		if j != nil {
			_ = j.Progress(ctx, stage, ingestStageTotal)
		}
	}

	// ① 原始件（内容寻址；取用即复算 sha256 校验完整性）
	if _, err := ingest.TransitionState(ctx, s.db, doc.ID, nil, model.KBDocumentBuildExtracting); err != nil {
		return nil, err
	}
	blob, err := ingest.LoadBlobForDocument(ctx, s.db, doc.ID)
	if err != nil {
		return nil, s.failDoc(ctx, doc, "blob", err)
	}
	if got := chunk.ContentSHA256(blob.Bytes); got != blob.SHA256 {
		return nil, s.failDoc(ctx, doc, "blob", fmt.Errorf("原始件完整性校验失败（复算 %s ≠ 键 %s）", got, blob.SHA256))
	}
	progress(1)

	// ② 判型 + 提取器身份
	name := doc.SourceRef
	if strings.TrimSpace(name) == "" {
		name = doc.Title
	}
	format, err := ingest.DetectFormat(name, doc.Mime, blob.Bytes)
	if err != nil {
		return nil, s.failDoc(ctx, doc, "extract", err)
	}
	extractor, version, err := splitExtractorID(format)
	if err != nil {
		return nil, s.failDoc(ctx, doc, "extract", err)
	}

	// ③ 提取（重切不重解：既有提取物直接复用，锚坐标系冻结；媒体链能力缺失 ⇒ 响亮失败带原因码；
	// 无文本层 pdf 的来源超限在链内光栅化之前抛出 ⇒ 同「挂起待批」语义）
	ext, err := s.ensureExtract(ctx, doc, format, extractor, version, blob.Bytes)
	if err != nil {
		var se *ingest.SourceSuspendedError
		if errors.As(err, &se) && se.Suspended != nil {
			if serr := s.suspendForBudget(ctx, doc, ingest.Plan{}, se.Suspended); serr != nil {
				return nil, serr
			}
			return &IngestResult{DocumentID: doc.ID, State: "suspended", Estimate: se.Suspended}, nil
		}
		return nil, s.failDoc(ctx, doc, "extract", err)
	}
	if _, err := ingest.TransitionState(ctx, s.db, doc.ID, nil, model.KBDocumentBuildExtracted); err != nil {
		return nil, err
	}
	progress(2)

	// ④ 计划（三层配置 + 策略路由 + build 身份）
	plan, err := ingest.ResolvePlan(ctx, s.db, doc, format, ext.Text, blob.SHA256, ext.ExtractorID())
	if err != nil {
		return nil, s.failDoc(ctx, doc, "plan", err)
	}

	// ⑤ 来源上限（max_pages/max_minutes）——**先于切分**：semantic 档在切分内即调句向量
	// （已烧钱），超限挂起必须挡在此前（早期实现排在切分之后，属已废弃形态）。
	pages, minutes := sourceReadings(ext)
	if limited := sourceLimits().Check(pages, minutes); limited != nil {
		if err := s.suspendForBudget(ctx, doc, plan, limited); err != nil {
			return nil, err
		}
		return &IngestResult{DocumentID: doc.ID, State: "suspended", BuildHash: plan.BuildHash, Estimate: limited}, nil
	}
	if _, err := ingest.TransitionState(ctx, s.db, doc.ID, nil, model.KBDocumentBuildChunking); err != nil {
		return nil, err
	}

	// ⑥ 切分（Finalize + VerifyVerbatim 已内置：锚不可复算不落库）
	chunks, err := ingest.SplitDocument(ctx, doc, plan, ext.Text)
	if err != nil {
		return nil, s.failDoc(ctx, doc, "chunk", err)
	}
	progress(3)

	// ⑦ 预算预检（C8：按嵌入文本估算；超限挂起待批——在落块/嵌入烧钱步骤之前）
	estIn := estimateInput(plan, chunks)
	estIn.Pages, estIn.Minutes = sourceReadings(ext) // 媒体计价并入预检（EstCost/MediaCost 读数）
	suspended, err := ingest.CheckBudget(estIn, budgetLimits())
	if err != nil {
		return nil, err
	}
	if suspended != nil {
		if err := s.suspendForBudget(ctx, doc, plan, suspended); err != nil {
			return nil, err
		}
		return &IngestResult{DocumentID: doc.ID, State: "suspended", BuildHash: plan.BuildHash, Chunks: len(chunks), Estimate: suspended}, nil
	}
	progress(4)

	// ⑧ 落切片（同 build 幂等：完整在位不重写 ⇒ chunk.id 稳定 ⇒ 向量行不失效）；
	// 匹配面文本 match_text 由写侧单点填充（ChunkTextOf），tsv 为 DB 生成列（migrations/1_core.up.sql 的 match_text 派生列）
	written, err := ingest.EnsureChunks(ctx, s.db, doc, plan, chunks, budgetLimits())
	if err != nil {
		return nil, s.failDoc(ctx, doc, "chunk", err)
	}
	if _, err := ingest.TransitionState(ctx, s.db, doc.ID, nil, model.KBDocumentBuildChunked); err != nil {
		return nil, err
	}

	// ⑧ 嵌入（kb.embed.enabled=false ⇒ 只切不嵌，检索面自动降级词面腿）
	res := &IngestResult{DocumentID: doc.ID, BuildHash: plan.BuildHash, Chunks: len(chunks), ChunkRows: written}
	res.Media = mediaReadingsOf(format, ext)
	if embedEnabled() {
		if _, err := ingest.TransitionState(ctx, s.db, doc.ID, nil, model.KBDocumentBuildEmbedding); err != nil {
			return nil, err
		}
		stats, err := kbembed.EmbedDocumentBuild(ctx, s.db, doc.ID, plan.BuildHash, embedOptions(func(done, total int64) {
			progress(5) // 嵌入期续租（done/total 由 embed 侧经 GREATEST 只增不减）
		}))
		if err != nil {
			return nil, s.failDoc(ctx, doc, "embedding", err)
		}
		res.Embed = &stats
		res.Embedded = stats.Embedded
		if !stats.Ok() {
			return nil, s.failDoc(ctx, doc, "embedding", fmt.Errorf("嵌入失败 %d 条: %v", stats.Failed, stats.Errors))
		}
	} else {
		res.Warnings = append(res.Warnings, "kb.embed.enabled=false：只切不嵌（检索面自动降级词面腿）")
	}
	if done, err := s.stopIfCanceled(ctx, doc); err != nil || done {
		if err != nil {
			return nil, err
		}
		return &IngestResult{DocumentID: doc.ID, State: "canceled", BuildHash: plan.BuildHash}, nil
	}

	// 同 build 零变更（重复投递/巡检复跑）：不换出、不落审计（免噪音）；**状态复位 ready** 后收工。
	// 复位是必需项：本链已把 build_state 推到 chunked/embedding（每阶段开工前落状态），零变更路径绕过
	// SwitchBuild ⇒ 不复位则文档卡在中间态、检索面看不到（实测：整库重切后 ready=2/3）。
	// CAS from 只认本链刚写的两态：并发换出（行已 ready）时不回写，避免压掉他人的 active_build。
	if doc.BuildState == model.KBDocumentBuildReady && doc.ActiveBuild == plan.BuildHash && written == 0 &&
		(!embedEnabled() || res.Embedded == 0) {
		// 复位**先于**规则打标（已整改）：打标失败即响亮返回时，文档必须已 ready——本链写入的
		// chunked/embedding 中间态不许滞留（滞留态既不在失败列表（只查 failed）、也无 failed_stage 点名面）。
		if _, err := ingest.TransitionState(ctx, s.db, doc.ID,
			[]string{model.KBDocumentBuildChunked, model.KBDocumentBuildEmbedding}, model.KBDocumentBuildReady); err != nil {
			return nil, err
		}
		// 规则打标（T3）：零变更重跑同样求值（重导入批次的可达路径；ON CONFLICT 幂等收敛）
		if err := s.applyRuleTagsStandalone(ctx, doc, res); err != nil {
			// 规则面失败不回退文档状态：复位已落定，文档保持 ready 且在服务，投影失败交作业退避重试
			// （禁把健康的 ready 文档标 failed——零变更回归教训；响亮失败由作业 last_error 承载）
			return nil, fmt.Errorf("kb: 文档 %d 零变更重跑规则打标失败: %w", doc.ID, err)
		}
		res.State = "ready"
		res.Warnings = append(res.Warnings, "同 build 零变更：未换出、导入不留痕（规则打标单列：真变才落行/留痕）")
		return res, nil
	}

	// ⑨ 换出（原子：active_build 换出 + 删旧 build 切片/向量 + 切片级标签重放 + 规则打标 + 同事务审计 import）
	//
	// 标签重放两半：计划须在换出**之前**捕获——换出事务内旧 build 切片行已删，
	// link → (document_id, text_hash) 解不出（捕获只读、失败响亮失败，见 tag_replay.go）；
	// 应用在换出事务内（与删旧行同事务：重挂/丢弃/审计要么全成要么全回滚）。
	// 规则打标（T3）同事务同语义：求值只读 + 落行/留痕同一事务（applyRuleTags）。
	replay, err := s.captureChunkTagReplay(ctx, doc.ID)
	if err != nil {
		return nil, s.failDoc(ctx, doc, "tag_replay", err)
	}
	var removed int64
	removed, err = ingest.SwitchBuild(ctx, s.db, doc.ID, plan.BuildHash, func(ctx context.Context, tx *gorm.DB) error {
		relinked, dropped, err := s.applyChunkTagReplay(ctx, tx, doc.ID, doc.LibraryID, plan.BuildHash, replay)
		if err != nil {
			return err
		}
		ruleTags, ruleNotes, err := s.applyRuleTags(ctx, tx, doc)
		if err != nil {
			return fmt.Errorf("kb: 规则打标失败（换出回滚）: %w", err)
		}
		res.RuleTags = ruleTags
		res.Warnings = append(res.Warnings, ruleNotes...)
		return writeAudit(ctx, tx, AuditEntry{
			LibraryID: doc.LibraryID, DocumentID: doc.ID, Actor: systemActor, Action: model.KBAuditActionImport,
			Detail: map[string]any{
				"build_hash": plan.BuildHash, "chunker": plan.Strategy, "chunker_version": plan.ChunkerVersion,
				"chunks": len(chunks), "chunk_rows": written, "embedded": res.Embedded,
				"removed_old_chunks": removed, "format": format, "extractor": ext.ExtractorID(),
				"tag_relinked": relinked, "tag_dropped": dropped,
			},
		})
	})
	if err != nil {
		if errors.Is(err, ingest.ErrBuildSuperseded) {
			// 并发换出已删本 build 切片行：本次换出被取代 ⇒ 终态收工（不落审计、不标 failed、不重跑——
			// 重跑会重切重嵌把并发换出的结果翻回去并重复计费）。文档保持并发换出方的 ready/build 态，
			// 检索面不受损（实测：原实现此处会写出「ready + active_build 指向零行」的静默坏态）。
			log.Printf("[kb] 换出被取代 doc=%d build=%s: %v", doc.ID, plan.BuildHash, err)
			res.State = "skipped"
			res.Warnings = append(res.Warnings, "换出被取代：并发换出已删本 build 切片行，未换出、未留痕")
			return res, nil
		}
		return nil, s.failDoc(ctx, doc, "switch", err)
	}
	res.RemovedOld = removed
	res.State = "ready"
	progress(6)
	return res, nil
}

// ensureExtract 保证提取物在位（uk(document_id, extractor, version)；重切不重解）。
//
// 媒体链接线：
//   - 提取走 ReadFormatCtx（媒体链的云调用/外部工具需 ctx 取消）；
//   - **新提取**时经 SaveExtractWithCost 把提取物与 kb_cost_log 记账行（非 ai 直连的 OCR/ASR/caption
//     成本）同事务落库（禁无账开销；复用既有提取物**不重复记账**）；
//   - 复用路径回填 Stats（来源读数 pages/minutes 由此进来源上限闸；Stats 损坏 ⇒ 响亮失败）。
func (s *Service) ensureExtract(ctx context.Context, doc *model.KBDocument, format, extractor, version string, data []byte) (*ingest.Extract, error) {
	if existing, err := ingest.LoadExtract(ctx, s.db, doc.ID, extractor, version); err != nil {
		return nil, err
	} else if existing != nil {
		return extractFromRow(existing, format, doc.Mime)
	}
	ext, err := ingest.ReadFormatCtx(ctx, format, data) // 质量门内置（零文本/乱码率响亮失败）
	if err != nil {
		return nil, err
	}
	costs := ingest.MediaCostEntries(ext, doc.LibraryID, doc.ID)
	row, err := ingest.SaveExtractWithCost(ctx, s.db, ext, doc.ID, costs)
	if err != nil {
		return nil, err
	}
	if row.Kind != ext.Kind || row.Extractor != ext.Extractor {
		// 撞 uk（并发/重跑）：复用既有行（其统计与记账随既有行口径——账与产物同事务已保一致）
		return extractFromRow(row, format, doc.Mime)
	}
	return &ingest.Extract{
		Kind: row.Kind, Extractor: row.Extractor, Version: row.Version,
		Format: format, Text: row.Text, MIME: ext.MIME, Stats: ext.Stats,
	}, nil
}

// extractFromRow 既有提取物行 → 领域视图（Stats 反序列化回填；损坏 ⇒ 响亮失败不静默降级读数）
func extractFromRow(row *ingest.ExtractRow, format, mime string) (*ingest.Extract, error) {
	ext := &ingest.Extract{
		Kind: row.Kind, Extractor: row.Extractor, Version: row.Version,
		Format: format, Text: row.Text, MIME: mime,
	}
	if row.Stats != nil && strings.TrimSpace(*row.Stats) != "" {
		var stats map[string]any
		if err := json.Unmarshal([]byte(*row.Stats), &stats); err != nil {
			return nil, fmt.Errorf("kb: 提取物读数损坏（document=%d extractor=%s@%s）: %w", row.DocumentID, row.Extractor, row.Version, err)
		}
		ext.Stats = stats
	}
	return ext, nil
}

// splitExtractorID 提取器身份拆解（extractor@version；uk 两列）
func splitExtractorID(format string) (extractor, version string, err error) {
	id, err := ingest.ExtractorIDFor(format)
	if err != nil {
		return "", "", err
	}
	i := strings.Index(id, "@")
	if i <= 0 || i == len(id)-1 {
		return "", "", fmt.Errorf("kb: 提取器身份非法: %s", id)
	}
	return id[:i], id[i+1:], nil
}

// estimateInput 预算预检输入（估算口径：Σ块字符 × 系数；字符取嵌入文本 = ChunkTextOf 派生。
// 媒体量（pages/minutes）由调用方回填——EstCost 并入媒体计价，见 CheckBudget）
func estimateInput(plan ingest.Plan, chunks []chunk.Chunk) ingest.BudgetInput {
	in := ingest.BudgetInput{Docs: 1, Chunks: len(chunks)}
	for i := range chunks {
		text := chunk.ChunkTextOf(chunks[i], plan.Config)
		cjk, other := ingest.CountWordsLike(text)
		in.Chars += int64(countRunes(text))
		in.CJK += cjk
		in.Other += other
	}
	return in
}

// sourceReadings 来源读数（上限判据）：文本类格式 = 单页、零时长；多模态由提取阶段在
// kb_extract.stats 回填 pages/minutes 后同一闸生效（数字形态容忍 int/float64/json.Number——
// 内存直传与 DB 反序列化两种来路）。
func sourceReadings(ext *ingest.Extract) (pages int, minutes float64) {
	pages = 1
	if ext == nil {
		return pages, 0
	}
	if v := ingest.StatFloat(ext.Stats, "pages"); v > 0 {
		pages = int(v)
	}
	if v := ingest.StatFloat(ext.Stats, "minutes"); v > 0 {
		minutes = v
	}
	return pages, minutes
}

// sourceLimits 来源上限（config kb.limits.*；0/未配取缺省 500/120——闸始终武装）
func sourceLimits() ingest.SourceLimits { return ingest.SourceLimitsOf() }

// suspendForBudget 预算挂起待批：落 failed_stage='budget'（build_state 不动 = 未开工）+ 同事务审计。
func (s *Service) suspendForBudget(ctx context.Context, doc *model.KBDocument, plan ingest.Plan, suspended *ingest.BudgetSuspended) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := ingest.MarkBudgetSuspended(ctx, tx, doc.ID, suspended.Error()); err != nil {
			return err
		}
		return writeAudit(ctx, tx, AuditEntry{
			LibraryID: doc.LibraryID, DocumentID: doc.ID, Actor: systemActor, Action: model.KBAuditActionImport,
			Detail: map[string]any{
				"event": "budget_suspend", "build_hash": plan.BuildHash,
				"what": suspended.What, "value": suspended.Value, "limit": suspended.Limit,
				"est_tokens": suspended.EstTokens, "est_cost_cny": suspended.EstCost,
			},
		})
	})
}

// failDoc 落失败旁路（文档 failed + 原因；返回包装错误给 worker 退避重试）。
func (s *Service) failDoc(ctx context.Context, doc *model.KBDocument, stage string, cause error) error {
	if err := ingest.MarkFailed(ctx, s.db, doc.ID, stage, cause.Error()); err != nil {
		log.Printf("[kb] 落失败状态异常 doc=%d stage=%s: %v", doc.ID, stage, err)
	}
	return fmt.Errorf("kb: 文档 %d 在 %s 阶段失败: %w", doc.ID, stage, cause)
}

// stopIfCanceled 取消检查（阶段边界；已请求 ⇒ 落 canceled 终态并返回 true）。
func (s *Service) stopIfCanceled(ctx context.Context, doc *model.KBDocument) (bool, error) {
	fresh, err := s.loadDocument(ctx, doc.ID)
	if err != nil {
		return false, err
	}
	if fresh == nil || !ingest.Canceled(fresh) {
		return false, nil
	}
	if err := ingest.MarkCanceled(ctx, s.db, doc.ID); err != nil {
		return false, err
	}
	log.Printf("[kb] 文档 %d 已按 cancel_requested 取消", doc.ID)
	return true, nil
}
