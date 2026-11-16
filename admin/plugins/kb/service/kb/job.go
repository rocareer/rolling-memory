// plugin: kb v1.0.0
// 知识库作业接线（队列与状态机；预留钩子的落点）。
//
// 三个 kind（jobs.Register；worker 按注册集领取，无池/并发档配置）：
//   - kb:ingest  单文档全链：提取 → 切分 → 嵌入 → 换出（ready）；键 kb:ingest:<document_id>:<build_hash>
//   - kb:rebuild 整库重切/重嵌（水位驱动逐窗追平）；键 kb:rebuild:<library_id>:<build_hash>
//   - kb:purge   清理（幂等可重入；PurgeEnqueueHook 在此接线）；键 kb:<library_id> / kb:doc:<document_id>
//
// 纪律与语义：
//   - 审计与业务**同事务**（writeAudit 未导出 ⇒ 一切作业侧写面必须落在本包）；
//     作业 actor = {Kind: system, ID: 0}（system 哨兵；不得借作业面冒充 admin/user）。
//   - 幂等链：blob=sha256 / 提取物=uk(doc, extractor@version) / 切片=uk(doc, build_hash, seq) /
//     向量=uk(chunk_id, model, text_hash)——重试与租约回收会重跑 handler，靠产物键续跑，不靠「跑没跑过」。
//   - 预算预检（C8，作业入口）：估算超限 ⇒ **挂起待批**（不消耗模型预算）：文档落
//     failed_stage='budget'（build_state 保持未开工态）+ 审计 import 明细记估算；批准 = 复投作业。
//     kb:ingest 的预检在切分后、嵌入前（烧钱步骤之前）；kb:rebuild 的文档数上限 = 窗口钳制
//     （window ≤ kb.budget.max_docs_per_job），字符/切片/token 上限按文档预检。
//   - 取消：运行中取消走业务标志 cancel_requested（jobs Cancel 仅限 pending），阶段边界检查。
//   - 规则打标（T3）：导入/重导入批次内求值——应用面见 tag_rule.go（写面单点 + 同事务审计）。
//   - 文件分工：本文件 = 公共/装配（注册/入队/键档）；kb:ingest 单文档链见 job_ingest.go，
//     kb:rebuild 见 job_rebuild.go，kb:purge 见 job_purge.go（job_support.go = 无决策读取面）。
package kb

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/service/jobs"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/chunk"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb/ingest"
)

// systemActor 作业面操作者（system 哨兵：actor_id 恒 0，不得借作业面放大为 admin/user）
var systemActor = Actor{Kind: model.KBActorKindSystem, ID: 0}

// 阶段进度口径（Progress 上报 + 借力 worker watchdog 续租）
const ingestStageTotal = 6

// init 注册三类作业处理器 + 接线预留的清理入队钩子。
//
// 注册是「随包 init 进入 serve 二进制导入图」的副作用：kb 服务包经 handler→router→serve 链引用即自动注册
// （同仓先例：按 kind 注册即接入）。出队 SQL 按「本进程已注册 kind 集」过滤 ⇒ 未注册 kind 留 pending 等新进程认领。
func init() {
	jobs.Register(ingest.JobKindIngest, ingestHandler{})
	jobs.Register(ingest.JobKindRebuild, rebuildHandler{})
	jobs.Register(ingest.JobKindPurge, purgeHandler{})

	//  钩子接线：删库/删文档 = 软删即刻 + 入队清理（键口径：kb:<library_id> / kb:doc:<document_id>）
	PurgeEnqueueHook = func(ctx context.Context, key string) error {
		key = strings.TrimSpace(key)
		if key == "" {
			return errors.New("kb: 清理作业键为空")
		}
		_, created, err := jobs.NewStore().Enqueue(ctx, jobs.EnqueueInput{
			Kind: ingest.JobKindPurge,
			Key:  key,
		})
		if err != nil {
			return err
		}
		if created {
			log.Printf("[kb] 清理作业入队（键 %s）", key)
		}
		return nil
	}
}

// ==================== 入队助手（导入面/重切面的调用点；键口径） ====================

// EnqueueIngestDocument 投递单文档导入（键 kb:ingest:<document_id>:<build_hash>）。
//
// 键内 build 为**元数据档估算**（不含提取文本）：txt 升档探测需要正文（chunk.RouteTXT），故键档与作业内
// 实际 build 可能有差异——键只作单飞判据（同 doc 同配置至多一活跃作业），不参与产物身份；
// 作业内以 ResolvePlan 复算的 build_hash 为准（幂等链由 uk(doc, build_hash, seq) 承担）。
func (s *Service) EnqueueIngestDocument(ctx context.Context, documentID uint) (*model.Job, bool, error) {
	if documentID == 0 {
		return nil, false, errors.New("kb: 投递导入作业缺 document_id")
	}
	doc, err := s.loadDocument(ctx, documentID)
	if err != nil {
		return nil, false, err
	}
	if doc == nil {
		return nil, false, fmt.Errorf("kb: 文档不存在或已删: %d", documentID)
	}
	keyBuild, err := s.provisionalBuild(ctx, doc)
	if err != nil {
		return nil, false, err
	}
	key := fmt.Sprintf("%s:%d:%s", ingest.JobKindIngest, documentID, keyBuild)
	return jobs.NewStore().Enqueue(ctx, jobs.EnqueueInput{
		Kind:   ingest.JobKindIngest,
		Key:    key,
		Params: ingest.IngestParams{DocumentID: documentID, Reason: "enqueue"},
	})
}

// EnqueueRebuildLibrary 投递整库重切/重嵌（键 kb:rebuild:<library_id>:<build_hash>）。
// build 传空 = 键档取 "auto"（重嵌/巡检触发；产物身份仍以作业内实际 build 为准）。
func (s *Service) EnqueueRebuildLibrary(ctx context.Context, libraryID uint, mode, build string) (*model.Job, bool, error) {
	if libraryID == 0 {
		return nil, false, errors.New("kb: 投递重切作业缺 library_id")
	}
	if strings.TrimSpace(mode) == "" {
		mode = ingest.RebuildModeRechunk
	}
	if mode != ingest.RebuildModeRechunk && mode != ingest.RebuildModeReembed {
		return nil, false, fmt.Errorf("kb: 重切模式未登记: %s", mode)
	}
	b := strings.TrimSpace(build)
	if b == "" {
		b = "auto"
	}
	key := fmt.Sprintf("%s:%d:%s", ingest.JobKindRebuild, libraryID, b)
	return jobs.NewStore().Enqueue(ctx, jobs.EnqueueInput{
		Kind:   ingest.JobKindRebuild,
		Key:    key,
		Params: ingest.RebuildParams{LibraryID: libraryID, Mode: mode, Reason: "enqueue"},
	})
}

// provisionalBuild 键档 build 估算（元数据口径；见 EnqueueIngestDocument 注释）。
// 判型/路由不成立（媒体面/未登记）⇒ 退内容身份（键仍确定，单飞语义不变）。
func (s *Service) provisionalBuild(ctx context.Context, doc *model.KBDocument) (string, error) {
	if doc == nil || doc.ID == 0 {
		return "", errors.New("kb: 键档 build 缺文档")
	}
	blob, err := ingest.LoadBlobForDocument(ctx, s.db, doc.ID)
	if err != nil {
		return doc.ContentHash, nil
	}
	cfg, err := chunk.Global()
	if err != nil {
		return "", err
	}
	name := doc.SourceRef
	if strings.TrimSpace(name) == "" {
		name = doc.Title
	}
	format, ferr := ingest.DetectFormat(name, doc.Mime, blob.Bytes)
	if ferr != nil {
		return doc.ContentHash, nil
	}
	extractorID, ferr := ingest.ExtractorIDFor(format)
	if ferr != nil {
		return doc.ContentHash, nil
	}
	strategy := strings.TrimSpace(cfg.Strategy)
	if strategy == "" {
		if strategy, ferr = chunk.RouteFormat(format); ferr != nil {
			return doc.ContentHash, nil
		}
		cfg.Strategy = strategy
	}
	cv, err := chunk.StrategyVersion(strategy, "")
	if err != nil {
		return "", err
	}
	build, err := chunk.BuildHash(blob.SHA256, cfg.Hash(), cv, extractorID)
	if err != nil {
		return doc.ContentHash, nil
	}
	return build, nil
}
