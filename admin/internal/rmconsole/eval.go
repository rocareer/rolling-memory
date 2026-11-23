// plugin: rolling-memory v0.0.58
// Package rollingmemory 独立「评测」频道后端实现：
//   - LoCoMo 语料库（ACL 2024 · 10 会话 5882 turns）× Backboard 官方 local-qa-judge 冻结协议（locomo-frozen-2026-10-11）；
//   - 测评产物使用专属云端 scope 与独立 JSON store，绝不读取或写入真实用户记忆空间；
//   - 内聚 AI 模型通道与 Answer / Judge 双官方冻结协议 Agent 配置；
//   - 提供权威基线水位（Oracle 77% / rl20 77% / rl5 63% / 0-LLM 76%）与持久化历史运行台账（含 Cat 1~4 分项读数）。
package rollingmemory

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/database"
	"github.com/ai-go-hub/ai-go-admin/internal/kit/httpx"
	"github.com/gin-gonic/gin"
)

const (
	// UserScope 用户日常真实记忆作用域
	UserScope = "default"
	// LegacyUserScope 历史默认记忆作用域（兼容存量数据联读）
	LegacyUserScope = "mem:self"
	// EvalScope LoCoMo 评测专属作用域基名，与真实用户数据物理隔离；
	// 现役评测 scope = 基名 + `-local-plain`（本地明文通道，见 evalScopeForSuite）。
	EvalScope = "u10-mem-eval-locomo"
	// 评测传输标识：唯一合法值 = 本地明文离线通道（与 rmem internal/eval 的
	// LocalPlainTransport 逐字同源）。
	evalLocalPlainTransport = "ramesdk.local_plain.offline-v1"
	// 现役唯一评测臂 = 本地明文（sdk-local-plain），另加 Oracle 金标上限诊断臂。
	EvalArmSDKLocalPlain    = "sdk-local-plain"
	EvalArmOracle           = "oracle"
	EvalPulseModeLocalPlain = "local_plain"

	// evalVerifyCodeManifestMismatch 与引擎 rmem eval 的稳定错误码契约同名：
	// verify-prepare 判定 manifest 与当前语料确定性失配（语料正文口径已更新等），
	// prepare 据此进入 repair + 断点续灌自愈，而不是 fail-closed 把用户的准备动作卡死。
	evalVerifyCodeManifestMismatch = "manifest_corpus_mismatch"
	// evalVerifyCodeManifestIdentityMismatch 与引擎同源：manifest 由旧版本写入
	// （endpoint / 身份 / 切词臂与现役客户端不同），复用不可能但可原地重灌自愈。
	evalVerifyCodeManifestIdentityMismatch = "manifest_identity_mismatch"

	// 官方 local-qa-judge (locomo-frozen-2026-10-11) 冻结协议原文与三枚 SHA1 钉
	FrozenProtocolID   = "locomo-frozen-2026-10-11"
	FrozenAnswerSystem = "You are a conversation memory assistant."
	FrozenJudgeSystem  = "You are evaluating conversational AI memory recall. Return JSON only with the format requested."
	FrozenJudgeUserTPL = "\nYour task is to label an answer to a question as 'CORRECT' or 'WRONG'. You will be given the following data:\n" +
		"    (1) a question (posed by one user to another user), \n" +
		"    (2) a 'gold' (ground truth) answer, \n" +
		"    (3) a generated answer\nwhich you will score as CORRECT/WRONG.\n\n" +
		"The point of the question is to ask about something one user should know about the other user based on their prior conversations.\n" +
		"The gold answer will usually be a concise and short answer that includes the referenced topic, for example:\n" +
		"Question: Do you remember what I got the last time I went to Hawaii?\n" +
		"Gold answer: A shell necklace\n" +
		"The generated answer might be much longer, but you should be generous with your grading - as long as it touches on the same topic as the gold answer, it should be counted as CORRECT. \n\n" +
		"For time related questions, the gold answer will be a specific date, month, year, etc. The generated answer might be much longer or use relative time references (like \"last Tuesday\" or \"next month\"), but you should be generous with your grading - as long as it refers to the same date or time period as the gold answer, it should be counted as CORRECT. Even if the format differs (e.g., \"May 7th\" vs \"7 May\"), consider it CORRECT if it's the same date.\n\n" +
		"Now it's time for the real question:\n" +
		"Question: <question>\n" +
		"Gold answer: <gold_answer>\n" +
		"Generated answer: <generated_answer>\n\n" +
		"First, provide a short (one sentence) explanation of your reasoning, then finish with CORRECT or WRONG. \n" +
		"Do NOT include both CORRECT and WRONG in your response, or it will break the evaluation script.\n\n" +
		"Return your response in JSON format with two keys: \"reasoning\" for your explanation and \"label\" for CORRECT or WRONG.\n"

	PinAnswerSystemSHA1 = "0d59162805571b5622b50446ee8da12490737ade"
	PinJudgeSystemSHA1  = "497f78929df5d4479c84ae7dd9993b4161f3a475"
	PinJudgeUserTPLSHA1 = "38f9b6a17ffd58bf58bb123de44ce40442f1a85b"
)

// AIModelConfig 用户可配置的 AI 模型通道（供测评 answer 与 judge 两个官方协议 Agent 调用）。
type AIModelConfig struct {
	Provider    string  `json:"provider"`
	BaseURL     string  `json:"base_url"`
	APIKey      string  `json:"api_key"`
	HasAPIKey   bool    `json:"has_api_key"`
	AnswerModel string  `json:"answer_model"`
	JudgeModel  string  `json:"judge_model"`
	Temperature float64 `json:"temperature"`
	MaxTokens   int     `json:"max_tokens"`
	TimeoutSec  int     `json:"timeout_sec"`
}

// OfficialProtocolAgent 官方冻结协议智能体描述（answer / judge）。
type OfficialProtocolAgent struct {
	Code            string `json:"code"`
	Name            string `json:"name"`
	NameEn          string `json:"name_en"`
	Role            string `json:"role"`
	Protocol        string `json:"protocol"`
	Model           string `json:"model"`
	SystemPrompt    string `json:"system_prompt"`
	SystemSHA1      string `json:"system_sha1"`
	UserTemplate    string `json:"user_template"`
	UserTemplateSHA string `json:"user_template_sha1"`
	PinVerified     bool   `json:"pin_verified"`
}

// ScopeTableCounts 仅为旧前端保留的兼容字段。
// Deprecated: 云端评测不再查询 trace/node/assertion/mention，所有计数恒为零。
type ScopeTableCounts struct {
	Scope      string `json:"scope"`
	Traces     int    `json:"traces"`
	Nodes      int    `json:"nodes"`
	Assertions int    `json:"assertions"`
	Mentions   int    `json:"mentions"`
}

// CategoryScores 四类题型分项得分（Cat 1 多跳 / Cat 2 时间 / Cat 3 开放 / Cat 4 单跳）。
type CategoryScores struct {
	Cat1 float64 `json:"cat1"` // Multi-hop
	Cat2 float64 `json:"cat2"` // Temporal
	Cat3 float64 `json:"cat3"` // Open-domain
	Cat4 float64 `json:"cat4"` // Single-hop
}

// EvalRunRecord 评测基线或历史运行记录（供图表与历史台账展示）。
type EvalRunRecord struct {
	ID              string  `json:"id"`
	Suite           string  `json:"suite,omitempty"` // locomo | atm
	Label           string  `json:"label"`
	LabelEn         string  `json:"label_en"`
	CreatedAt       string  `json:"created_at"`
	IsBaseline      bool    `json:"is_baseline"`
	Tier            string  `json:"tier"`
	ScoredQuestions int     `json:"scored_questions"`
	Arm             string  `json:"arm"`
	Model           string  `json:"model"`
	Protocol        string  `json:"protocol"`
	EvidenceHit     float64 `json:"evidence_hit"`
	// FullEvidenceHit 全交付上下文证据命中率（与 evidence_hit 的头部窗口口径双轨并行）。
	// evidence_hit 沿用既有语义（交付上下文前 48 词元窗口内命中，internal/eval/scoring.go 的 TokenRecall），
	// 本键对应引擎的 full_evidence_hit（不截断分词的完整交付上下文命中，双轨量尺），只增不改旧键语义。
	FullEvidenceHit float64 `json:"full_evidence_hit,omitempty"`
	// 官方检索指标（与 CLI internal/eval/atmbench.go 的 gold-set-mean 口径对齐，2001 只增键）：
	//   - R10/R20/R50：逐题金标覆盖率（命中金标证据数 / 金标总数）在交付序前 10/20/50 条窗口的均值；
	//   - AnyHit10 / AllHit10：前 10 条窗口内 ≥1 条 / 全部金标命中的题目比例（辅助二值指标）；
	//   - IngestMS：最近一次语料 Prepare/Rebuild 实测导入耗时（毫秒，0 = 未测）。
	R10             float64 `json:"r10"`
	R20             float64 `json:"r20"`
	R50             float64 `json:"r50"`
	AnyHit10        float64 `json:"any_hit_10,omitempty"`
	AllHit10        float64 `json:"all_hit_10,omitempty"`
	IngestMS        int64   `json:"ingest_ms,omitempty"`
	AnswerInContext float64 `json:"answer_in_context"`
	JudgeRate       float64 `json:"judge_rate"`
	JudgeCorrect    int     `json:"judge_correct"`
	JudgeScored     int     `json:"judge_scored"`
	ElapsedSeconds  float64 `json:"elapsed_seconds"`
	// 分相耗时（只增键）：LoadMS 是本次运行开始时的语料就绪/载入阶段（prepare 探针；
	// 复用已备语料时通常 <1s，首次构建时含全量导入）；EvalSeconds 是其后逐题评测的
	// 端到端墙钟。ElapsedSeconds 保持总耗时 = 载入 + 评测。旧记录缺省 0 时前端回退。
	LoadMS      int64   `json:"load_ms,omitempty"`
	EvalSeconds float64 `json:"eval_seconds,omitempty"`
	// 端到端检索耗时与交付量（tokenz-r 口径）运行级聚合：
	//   - AvgRecallMS / P95RecallMS：单题端到端云端检索耗时（毫秒）均值与 P95；
	//   - AvgDeliveredTokens / TotalDeliveredTokens：单题实际送入答题载荷的 tokenz-r Token 均值与总计；
	//   - AvgDeliveredItems：单题实际送入答题载荷的证据条数均值。
	AvgRecallMS          float64        `json:"avg_recall_ms,omitempty"`
	P95RecallMS          float64        `json:"p95_recall_ms,omitempty"`
	AvgDeliveredTokens   int            `json:"avg_delivered_tokens,omitempty"`
	TotalDeliveredTokens int            `json:"total_delivered_tokens,omitempty"`
	AvgDeliveredItems    float64        `json:"avg_delivered_items,omitempty"`
	CategoryEvidence     CategoryScores `json:"category_evidence"`
	CategoryJudge        CategoryScores `json:"category_judge"`
	Note                 string         `json:"note"`
	NoteEn               string         `json:"note_en"`
	// L0 工程师工作台增强（只增键，旧消费方忽略未知键即无损）：
	//   - ReproCLI 等价 CLI 复现命令（run 级，逐题明细另见 EvalQuestionResultItem.ReproCLI）；
	//   - TopK 本次运行的召回池深度（结构化键；旧记录缺省 0 时前端回退解析 armLabel 的 k=/rl 尾注）；
	//   - ProfileTier 本次运行声明的能力档位（空串=继承当前环境配置）；noetic_mode 已停用，不再出现在记录里；
	//   - BucketCounts 错题根因五桶计数（HIT / RECALL_MISS / RANK_CUTOFF / READER_GAP / JUDGE_AMBIGUITY）。
	ReproCLI     string         `json:"repro_cli,omitempty"`
	TopK         int            `json:"top_k,omitempty"`
	ProfileTier  string         `json:"profile_tier,omitempty"`
	BucketCounts map[string]int `json:"bucket_counts,omitempty"`
	// Status / Error 是测评任务表的状态与错误投影（completed | cancelled | failed）。
	Status                 string                     `json:"status,omitempty"`
	Error                  string                     `json:"error,omitempty"`
	MetricsAvailable       map[string]bool            `json:"metrics_available,omitempty"`
	MetricEvidenceExplicit bool                       `json:"metric_evidence_explicit,omitempty"`
	Comparison             *EvalComparisonFingerprint `json:"comparison_fingerprint,omitempty"`
}

type EvalComparisonFingerprint struct {
	QuestionsSHA256 string `json:"questions_sha256"`
	CorpusSHA256    string `json:"corpus_sha256"`
	ScoringSHA256   string `json:"scoring_sha256"`
}

// EvalQuestionItem 题集单题结构（兼容带证据题集与全量题集）。
type EvalQuestionItem struct {
	ID            string   `json:"id"`
	SampleID      string   `json:"sample_id"`
	Category      string   `json:"category"`
	Question      string   `json:"question"`
	Answer        string   `json:"answer"`
	EvidenceTexts []string `json:"evidence_texts"`
}

// EvalOverviewRequest 测评概览与题集分页请求。
type EvalOverviewRequest struct {
	Suite    string `json:"suite"`
	Tier     string `json:"tier"`
	ArmMode  string `json:"arm_mode,omitempty"`
	Category string `json:"category"`
	Keyword  string `json:"keyword"`
	Page     int    `json:"page"`
	PageSize int    `json:"page_size"`
}

// LeaderboardEntry locomo-backboard 榜单条目（含公开商业榜系统与我方同协议实测对标行）。
type LeaderboardEntry struct {
	// Rank 主榜名次（1..N）。排名契约：只对「同题集 / 同分母」的行（1,540 题全量）按 Overall 降序落号；
	// 不同题集 / 不同分母的行不参与排名，一律 Rank=0 + NotRanked=true 并统一排在榜尾。
	Rank int `json:"rank"`
	// NotRanked 显式「不参与主榜排名」标记（不同题集/不同分母行），与 Rank=0 成对出现；
	// 前端据此渲染「不参与排名」而非位置序号，避免出现名次与总分倒挂。
	NotRanked      bool           `json:"not_ranked"`
	ID             string         `json:"id"`
	System         string         `json:"system"`
	SystemEn       string         `json:"system_en"`
	Vendor         string         `json:"vendor"`
	VendorEn       string         `json:"vendor_en"`
	Architecture   string         `json:"architecture"`
	ArchitectureEn string         `json:"architecture_en"`
	Protocol       string         `json:"protocol"`
	ProtocolEn     string         `json:"protocol_en"`
	QuestionSet    string         `json:"question_set"`
	QuestionSetEn  string         `json:"question_set_en"`
	JudgeModel     string         `json:"judge_model"`
	JudgeModelEn   string         `json:"judge_model_en"`
	SingleHop      float64        `json:"single_hop"`  // Cat 4 (Single-hop)
	MultiHop       float64        `json:"multi_hop"`   // Cat 1 (Multi-hop)
	OpenDomain     float64        `json:"open_domain"` // Cat 3 (Open-domain)
	Temporal       float64        `json:"temporal"`    // Cat 2 (Temporal)
	Overall        float64        `json:"overall"`     // 总分 (0~100 百分比数值)
	EvidenceHit    float64        `json:"evidence_hit"`
	IsOurs         bool           `json:"is_ours"`
	IsPublicBoard  bool           `json:"is_public_board"`
	HighlightTag   string         `json:"highlight_tag"`
	HighlightTagEn string         `json:"highlight_tag_en"`
	Note           string         `json:"note"`
	NoteEn         string         `json:"note_en"`
	Categories     CategoryScores `json:"categories"`
}

// ProtocolParitySpec 内置评测协议与 locomo-backboard 榜单同构说明。
type ProtocolParitySpec struct {
	BuiltinProtocol        string            `json:"builtin_protocol"`
	ProtocolID             string            `json:"protocol_id"`
	LeaderboardName        string            `json:"leaderboard_name"`
	LeaderboardNameEn      string            `json:"leaderboard_name_en"`
	CorpusName             string            `json:"corpus_name"`
	CorpusNameEn           string            `json:"corpus_name_en"`
	AnswerSystemPrompt     string            `json:"answer_system_prompt"`
	AnswerSystemSHA1       string            `json:"answer_system_sha1"`
	AnswerPayloadSpec      string            `json:"answer_payload_spec"`
	AnswerPayloadSpecEn    string            `json:"answer_payload_spec_en"`
	AnswerPayloadSHA1      string            `json:"answer_payload_sha1,omitempty"`
	JudgeSystemPrompt      string            `json:"judge_system_prompt"`
	JudgeSystemSHA1        string            `json:"judge_system_sha1"`
	JudgeTemplateSHA1      string            `json:"judge_template_sha1"`
	JudgeTemplateBytes     int               `json:"judge_template_bytes"`
	JudgeVerdictSpec       string            `json:"judge_verdict_spec"`
	JudgeVerdictSpecEn     string            `json:"judge_verdict_spec_en"`
	NumberScorer           string            `json:"number_scorer,omitempty"`
	NumberScorerVersion    string            `json:"number_scorer_version,omitempty"`
	ListScorer             string            `json:"list_scorer,omitempty"`
	ListScorerVersion      string            `json:"list_scorer_version,omitempty"`
	JudgeParser            string            `json:"judge_parser,omitempty"`
	ScoringFixtureSHA256   string            `json:"scoring_fixture_sha256,omitempty"`
	EvaluatorSourcePins    map[string]string `json:"evaluator_source_pins,omitempty"`
	CorpusPins             map[string]string `json:"corpus_pins,omitempty"`
	OfficialScoringAligned bool              `json:"official_scoring_aligned"`
	OfficialScoringParity  string            `json:"official_scoring_parity,omitempty"`
	PromptPinsStatus       string            `json:"prompt_pins_status,omitempty"`
	ScoringFixtureStatus   string            `json:"scoring_fixture_status,omitempty"`
	AnswerFixtureStatus    string            `json:"answer_fixture_status,omitempty"`
	CorpusPinsStatus       string            `json:"corpus_pins_status,omitempty"`
	RecallMetricSpec       string            `json:"recall_metric_spec,omitempty"`
	RecallMetricSpecEn     string            `json:"recall_metric_spec_en,omitempty"`
	AllPinsVerified        bool              `json:"all_pins_verified"`
	CategoryMappingNote    string            `json:"category_mapping_note"`
	CategoryMappingNoteEn  string            `json:"category_mapping_note_en"`
	FairComparisonNotice   string            `json:"fair_comparison_notice"`
	FairComparisonNoticeEn string            `json:"fair_comparison_notice_en"`
}

// EvalLeaderboardResponse 榜单专页响应结构。
type EvalLeaderboardResponse struct {
	ProtocolSpec ProtocolParitySpec      `json:"protocol_spec"`
	Entries      []LeaderboardEntry      `json:"entries"`
	Agents       []OfficialProtocolAgent `json:"agents"`
	Baselines    []EvalRunRecord         `json:"baselines"`
	RunHistory   []EvalRunRecord         `json:"run_history"`
	ATMBench     ATMBenchBoard           `json:"atm_bench"`
}

// EvalATMLeaderboardResponse 剑桥 ATM-Bench 独立榜单页（web/views/atmLeaderboard.vue）响应结构。
// 与 LoCoMo 榜单响应分离：本页只吃 ATM 语料看板 + 官方 atmbench-official-v1 协议栈 +
// 两枚官方智能体（MMRag 答题 / 混合 QS 判官）+ 读档三阶梯度（zero < pro < max）实测台账。
type EvalATMLeaderboardResponse struct {
	ATMBench        ATMBenchBoard           `json:"atm_bench"`
	ATMProtocolSpec ProtocolParitySpec      `json:"atm_protocol_spec"`
	ATMAgents       []OfficialProtocolAgent `json:"atm_agents"`
	ATMBaselines    []EvalRunRecord         `json:"atm_baselines"`
	ATMOfficialPins ATMOfficialPinStatus    `json:"atm_official_pins"`
}

// EvalATMProtocolResponse reports protocol and evidence verification separately.
// OK is true only when the reported pins and the official scorer parity pass.
type EvalATMProtocolResponse struct {
	OK     bool                    `json:"ok"`
	Spec   ProtocolParitySpec      `json:"spec"`
	Pins   ATMOfficialPinStatus    `json:"pins"`
	Agents []OfficialProtocolAgent `json:"agents"`
}

// EvalOverviewResponse 测评概览响应：含同表双域隔离、语料/协议溯源、基线、历史运行、AI 配置与题集。
type EvalOverviewResponse struct {
	Suite string `json:"suite"`
	// Mode 是评测运行模式：本版本恒为 local（本地明文通道，零脉冲 · 零云端请求）。
	Mode string `json:"mode"`
	// DBPath 为旧前端保留，值与 StorePath 相同。
	// Deprecated: 新消费方使用 StorePath；这里是 JSON 明文金库而非 SQLite。
	DBPath          string                  `json:"db_path"`
	StorePath       string                  `json:"store_path"`
	Readiness       EvalReadiness           `json:"readiness"`
	Manifest        *EvalManifest           `json:"manifest,omitempty"`
	EvalDataDir     string                  `json:"eval_data_dir"`
	CorpusTotalRows int                     `json:"corpus_total_rows"`
	UserCounts      ScopeTableCounts        `json:"user_counts"`
	EvalCounts      ScopeTableCounts        `json:"eval_counts"`
	AIConfig        AIModelConfig           `json:"ai_config"`
	AICheck         AIModelCheckResult      `json:"ai_check"`
	Agents          []OfficialProtocolAgent `json:"agents"`
	ProtocolSpec    ProtocolParitySpec      `json:"protocol_spec"`
	ATMOfficialPins ATMOfficialPinStatus    `json:"atm_official_pins,omitempty"`
	Leaderboard     []LeaderboardEntry      `json:"leaderboard"`
	Baselines       []EvalRunRecord         `json:"baselines"`
	RunHistory      []EvalRunRecord         `json:"run_history"`
	ActiveJob       EvalJobStatus           `json:"active_job"`
	Tier            string                  `json:"tier"`
	TierHardCount   int                     `json:"tier_hard_count,omitempty"`
	Tier100Count    int                     `json:"tier_100_count"`
	Tier200Count    int                     `json:"tier_200_count"`
	Tier1540Count   int                     `json:"tier_1540_count"`
	Tier1013Count   int                     `json:"tier_1013_count,omitempty"`
	TotalQuestions  int                     `json:"total_questions"`
	Page            int                     `json:"page"`
	PageSize        int                     `json:"page_size"`
	Questions       []EvalQuestionItem      `json:"questions"`
	QuestionError   string                  `json:"question_error,omitempty"`

	// FrozenCorpus / CorpusSyncCLI 仅为旧前端保留，云端评测不再读取冻结锁。
	// Deprecated: 语料代次与就绪状态分别以 Manifest.CorpusSHA256 和 Readiness 为准。
	FrozenCorpus  string `json:"frozen_corpus,omitempty"`
	CorpusSyncCLI string `json:"corpus_sync_cli,omitempty"`
	// ATMOracleQSCeiling ATM-Bench-Hard 金标证据直投的官方混合 QS 上限（同源常量 ATMOracleHardQSScore，
	// 0–100 数值；评测页 ATM 档图表上限线唯一数据源）。0 = 旧后端无该字段 → 前端只渲染占位提示、不下数字。
	ATMOracleQSCeiling float64 `json:"atm_oracle_qs_ceiling,omitempty"`
}

// AICheckItem 单项模型配置预检条目（供前端 Modal 框逐项展示）。
type AICheckItem struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	LabelEn  string `json:"label_en"`
	Passed   bool   `json:"passed"`
	Detail   string `json:"detail"`
	DetailEn string `json:"detail_en"`
	Blocking bool   `json:"blocking"`
}

// AIModelCheckResult 模型配置完整性与合法性预检结果。
type AIModelCheckResult struct {
	Ready       bool          `json:"ready"`
	IsLocalhost bool          `json:"is_localhost"`
	Summary     string        `json:"summary"`
	SummaryEn   string        `json:"summary_en"`
	Checks      []AICheckItem `json:"checks"`
	// PluginChannels AI 路由插件（plugins/ai）中可直接复用的渠道候选（含模型绑定与生效凭证状态）。
	PluginChannels []AIPluginChannelOption `json:"plugin_channels"`
	// PluginChannelsOK AI 路由插件是否已安装且至少存在一条可路由渠道。
	PluginChannelsOK bool `json:"plugin_channels_ok"`
}

// AIPluginChannelOption AI 路由插件的一条渠道候选（供评测实验室一键接入 Base URL / 密钥 / 模型）。
//
// 只回**生效凭证的存在性**（HasKey）与生效端点，绝不回明文密钥——密钥在服务端直接写入评测配置。
type AIPluginChannelOption struct {
	ChannelID    int64    `json:"channel_id"`
	ChannelName  string   `json:"channel_name"`
	Vendor       string   `json:"vendor"`
	BaseURL      string   `json:"base_url"`
	HasKey       bool     `json:"has_key"`
	Status       int16    `json:"status"`
	Circuit      string   `json:"circuit"`
	UpstreamName string   `json:"upstream_name"`
	Routable     bool     `json:"routable"`
	Models       []string `json:"models"`
}

// AIConnectionTestResponse 在线连通性实测响应（真实调用 Answer + Judge 冻结协议探针）。
type AIConnectionTestResponse struct {
	Success      bool               `json:"success"`
	PinVerified  bool               `json:"pin_verified"`
	Endpoint     string             `json:"endpoint"`
	AnswerModel  string             `json:"answer_model"`
	JudgeModel   string             `json:"judge_model"`
	AnswerOK     bool               `json:"answer_ok"`
	AnswerMS     int64              `json:"answer_ms"`
	AnswerOutput string             `json:"answer_output"`
	AnswerError  string             `json:"answer_error"`
	JudgeOK      bool               `json:"judge_ok"`
	JudgeMS      int64              `json:"judge_ms"`
	JudgeLabel   string             `json:"judge_label"`
	JudgeReason  string             `json:"judge_reason"`
	JudgeError   string             `json:"judge_error"`
	TotalMS      int64              `json:"total_ms"`
	Message      string             `json:"message"`
	MessageEn    string             `json:"message_en"`
	ConfigCheck  AIModelCheckResult `json:"config_check"`
}

// EvidenceMatchItem 单条金标证据在当前召回上下文中的匹配诊断。
type EvidenceMatchItem struct {
	Text        string  `json:"text"`
	TokenRecall float64 `json:"token_recall"`
	Hit         bool    `json:"hit"`
}

// EvalQuestionResultItem 单题评测执行结果明细（用于实时流式推送与批次详情错题剖析）。
type EvalQuestionResultItem struct {
	Index              int                 `json:"index"`
	ID                 string              `json:"id"`
	SampleID           string              `json:"sample_id"`
	Category           string              `json:"category"`
	Question           string              `json:"question"`
	Gold               string              `json:"gold"`
	EvidenceTexts      []string            `json:"evidence_texts"`
	EvidenceMatches    []EvidenceMatchItem `json:"evidence_matches,omitempty"`
	BestEvidenceRecall float64             `json:"best_evidence_recall"`
	EvidenceHit        bool                `json:"evidence_hit"`
	// 官方金标覆盖指标（rank-aware，交付序）：R10/R20/R50 为逐题金标交均（0..1），
	// AnyHit10/AllHit10 为前 10 条窗口内 ≥1 条 / 全部金标命中的二值判定（与 CLI gold-set-mean 同口径）。
	R10             float64  `json:"r10"`
	R20             float64  `json:"r20"`
	R50             float64  `json:"r50"`
	AnyHit10        bool     `json:"any_hit_10,omitempty"`
	AllHit10        bool     `json:"all_hit_10,omitempty"`
	AnswerInContext bool     `json:"answer_in_context"`
	GoldTokenRecall float64  `json:"gold_token_recall"`
	Prediction      string   `json:"prediction"`
	TokenF1         float64  `json:"f1"`
	ExactMatch      bool     `json:"exact"`
	JudgeVerdict    string   `json:"judge"`
	JudgeReason     string   `json:"judge_reason"`
	ContextPreview  string   `json:"context_preview"`
	FormattedItems  []string `json:"formatted_items,omitempty"`
	ContextChars    int      `json:"context_chars"`
	Bands           int      `json:"bands"`
	Nodes           int      `json:"nodes"`
	RawHits         int      `json:"raw"`
	RecallMS        int64    `json:"recall_ms"`
	AnswerMS        int64    `json:"answer_ms"`
	JudgeMS         int64    `json:"judge_ms"`
	TotalMS         int64    `json:"total_ms"`
	// L0 工程师工作台增强（只增键，旧消费方忽略未知键即无损）：
	//   - BandItems / RawItems 交付面按来源拆分的编号行（band×链渲染行 / 原文切片行），
	//     两者按交付序拼起来即 FormattedItems（供单题 X-Ray 显微镜分辨「图谱命中」与「原文命中」占比）；
	//   - FailureBucket 错题根因桶（HIT | RECALL_MISS | RANK_CUTOFF | READER_GAP | JUDGE_AMBIGUITY）；
	//   - ReproCLI 单题等价 CLI 复现命令。
	FailureBucket string   `json:"failure_bucket,omitempty"`
	BandItems     []string `json:"band_items,omitempty"`
	RawItems      []string `json:"raw_items,omitempty"`
	ReproCLI      string   `json:"repro_cli,omitempty"`
	// ContextOrdering 交付行序标注：bands_first = 融合臂（图谱优先，本批设定）；raw_first = 纯原文臂 /
	// oracle 金标臂的历史口径。引擎自身是 raw-first（internal/eval/scoring.go:118），此键用于显式标注
	// 交付序差异，避免读侧口径漂移。
	ContextOrdering string `json:"context_ordering,omitempty"`
	// 逐题引擎 provenance（评测模块自持的证据标签；引擎只按 scope × layer 返回通用能力）：
	//   - EngineTransport / EngineScope / EngineUpstream / LocalFallback 取自严格 recall 的 meta；
	//   - LocalHits 本次本地检索命中数，RetrievalUnitIDs 本次交付的 Unit ID；
	//   - CorpusSHA256 本套件语料 manifest 摘要（只读；缺失时留空，不阻断评测）。
	// 历史明细缺这些键时前端按未知渲染，不臆造。
	EngineTransport  string   `json:"engine_transport,omitempty"`
	EngineScope      string   `json:"engine_scope,omitempty"`
	EngineUpstream   string   `json:"engine_upstream,omitempty"`
	LocalFallback    *bool    `json:"local_fallback,omitempty"`
	LocalHits        int      `json:"local_hits,omitempty"`
	RetrievalUnitIDs []string `json:"retrieval_unit_ids,omitempty"`
	CorpusSHA256     string   `json:"corpus_sha256,omitempty"`
	// L0-3c 实际送入冻结答题载荷的交付计数（与未截断原始召回计数 Bands/Nodes/RawHits 对照）：
	BandsDelivered  int `json:"bands_delivered,omitempty"`
	NodesDelivered  int `json:"nodes_delivered,omitempty"`
	RawDelivered    int `json:"raw_delivered,omitempty"`
	DeliveredItems  int `json:"delivered_items,omitempty"`
	DeliveredTokens int `json:"delivered_tokens,omitempty"`
	// 双轨检索量尺（0009②）：BestEvidenceRecall / EvidenceHit 为完整交付上下文命中率（无 48 词元截断，
	// 对应 CLI 的 full_evidence_recall / full_evidence_hit）；BestHeadEvidenceRecall / HeadEvidenceHit 为前 48
	// 个唯一词元头尺命中率（对应 CLI 的 TokenRecall / evidence_hit），供用户在后台同屏对照两条量尺。
	BestHeadEvidenceRecall float64 `json:"best_head_evidence_recall,omitempty"`
	HeadEvidenceHit        bool    `json:"head_evidence_hit,omitempty"`
	// ATM 官方混合 QS 分项（与 CLI internal/eval/atmbench.go 的 qs_score / qs_scored 同口径）：
	// list_recall 记连续 Jaccard，number 记确定性 EM（0/1），open_end 记 LLM Judge（0/1）；
	// 只有真正产出可判分答案的题才置 qs_scored=true，判官解析失败按 unparsed 剔除。
	// ATM 套件的 judge_rate 取本字段均值（与 CLI 的 summary.judge_rate 一致），
	// JudgeCorrect/JudgeScored 仍保留 >=0.5 的二值计数供五桶归因使用。
	QSScore          float64         `json:"qs_score,omitempty"`
	QSScored         bool            `json:"qs_scored,omitempty"`
	MetricsAvailable map[string]bool `json:"metrics_available,omitempty"`
}

// EvalJobStatus 实时流式评测任务状态快照（WebSocket 实时帧与一次性概览快照共用）。
type EvalJobStatus struct {
	JobID               string  `json:"job_id"`
	Status              string  `json:"status"` // idle | running | completed | cancelled | failed
	Phase               string  `json:"phase"`  // idle | ingest | evaluating | done
	PhaseText           string  `json:"phase_text"`
	PhaseTextEn         string  `json:"phase_text_en"`
	Tier                string  `json:"tier"`
	ArmMode             string  `json:"arm_mode"`
	UseAgents           bool    `json:"use_agents"`
	RawLimit            int     `json:"raw_limit"`
	Workers             int     `json:"workers"`
	CategoryFilter      string  `json:"category_filter"`
	Total               int     `json:"total"`
	Done                int     `json:"done"`
	Percent             float64 `json:"percent"`
	StartedAt           string  `json:"started_at"`
	ElapsedSeconds      float64 `json:"elapsed_seconds"`
	ETASeconds          float64 `json:"eta_seconds"`
	CurrentQuestionID   string  `json:"current_question_id"`
	CurrentQuestionText string  `json:"current_question_text"`
	LiveEvidenceHit     float64 `json:"live_evidence_hit"`
	LiveAnswerInContext float64 `json:"live_answer_in_context"`
	LiveF1              float64 `json:"live_f1"`
	// 官方 gold-set-mean 口径实时聚合（交付序 rank-aware，与落档记录 r10/r20/r50 同源同算子）：
	// 检索臂与 LLM 臂逐题累计，运行中即可对照官方榜单读数。
	LiveR10                float64                  `json:"live_r10,omitempty"`
	LiveR20                float64                  `json:"live_r20,omitempty"`
	LiveR50                float64                  `json:"live_r50,omitempty"`
	LiveAnyHit10           float64                  `json:"live_any_hit_10,omitempty"`
	LiveAllHit10           float64                  `json:"live_all_hit_10,omitempty"`
	LiveJudgeRate          float64                  `json:"live_judge_rate"`
	LiveJudgeCorrect       int                      `json:"live_judge_correct"`
	LiveJudgeScored        int                      `json:"live_judge_scored"`
	LiveJudgeWrong         int                      `json:"live_judge_wrong"`
	LiveJudgeUnparsed      int                      `json:"live_judge_unparsed"`
	LiveJudgeError         int                      `json:"live_judge_error"`
	LiveAvgRecallMS        float64                  `json:"live_avg_recall_ms,omitempty"`
	LiveAvgDeliveredTokens int                      `json:"live_avg_delivered_tokens,omitempty"`
	LiveAvgDeliveredItems  float64                  `json:"live_avg_delivered_items,omitempty"`
	LiveCategoryEvidence   CategoryScores           `json:"live_category_evidence"`
	LiveCategoryJudge      CategoryScores           `json:"live_category_judge"`
	LatestItem             *EvalQuestionResultItem  `json:"latest_item,omitempty"`
	RecentItems            []EvalQuestionResultItem `json:"recent_items"`
	Logs                   []string                 `json:"logs"`
	Record                 *EvalRunRecord           `json:"record,omitempty"`
	Error                  string                   `json:"error,omitempty"`
	UpdatedAtUnixMS        int64                    `json:"updated_at_ms"`
	// L0 工程师工作台增强（只增键）：任务级复现命令、模式标记与实时错题分桶计数。
	Suite        string         `json:"suite,omitempty"`
	ReproCLI     string         `json:"repro_cli,omitempty"`
	ProfileTier  string         `json:"profile_tier,omitempty"`
	BucketCounts map[string]int `json:"bucket_counts,omitempty"`
}

type evalIngestRequest struct {
	Suite   string `json:"suite"`
	ArmMode string `json:"arm_mode,omitempty"`
	Action  string `json:"action"` // prepare | verify | rebuild
}

type evalRunRequest struct {
	Suite         string         `json:"suite"`    // locomo | atm
	Mode          string         `json:"mode"`     // single | batch | status | stop | detail | delete | test_ai | check_ai | ai_channels | ai_apply_channel
	Action        string         `json:"action"`   // start | attach
	Tier          string         `json:"tier"`     // 100 | 200 | 1540
	ArmMode       string         `json:"arm_mode"` // sdk-local-plain | oracle
	UseAgents     bool           `json:"use_agents"`
	Category      string         `json:"category"`
	RawLimit      int            `json:"raw_limit"`
	TopK          int            `json:"top_k"`
	EvalStore     string         `json:"eval_store"`
	Workers       int            `json:"workers"`
	Offset        int            `json:"offset"`
	Limit         int            `json:"limit"`
	CustomLabel   string         `json:"custom_label"`
	SavePartial   bool           `json:"save_partial"`
	RunID         string         `json:"run_id"`
	QuestionID    string         `json:"question_id"`
	Question      string         `json:"question"`
	Gold          string         `json:"gold"`
	EvidenceTexts []string       `json:"evidence_texts"`
	ChannelID     int64          `json:"channel_id"`
	ApplyModel    string         `json:"apply_model"`
	AIConfig      *AIModelConfig `json:"ai_config,omitempty"`
	// Profile / NoeticMode 仅为历史请求字段保留；新任务必须为空，否则拒绝。
	Profile    string `json:"profile,omitempty"`
	NoeticMode string `json:"noetic_mode,omitempty"`
}

var (
	evalJobMu     sync.RWMutex
	evalJobCancel context.CancelFunc
	evalJobState  = EvalJobStatus{
		Status:      "idle",
		Phase:       "idle",
		RecentItems: []EvalQuestionResultItem{},
		Logs:        []string{},
	}
	evalRunItemsMu    sync.RWMutex
	evalRunItems      = []EvalQuestionResultItem{}
	evalRunItemsJobID string
	evalJobAIConfig   AIModelConfig
)

func sha1Hex(text string) string {
	sum := sha1.Sum([]byte(text))
	return hex.EncodeToString(sum[:])
}

func aiConfigPath(home string) string {
	return filepath.Join(home, ".config", "rolling-memory", "eval-ai.json")
}

// radarCurrentBaselineID 五维能力雷达「当前」臂所在的基线行（现行最新 1,540 全量读数）。
//
// 内置台账会随代次刷新读数，下标随之漂移 ⇒ 雷达**按 ID 解析、不按下标硬取**（读取点见 memory.go 的
// 「若尚未有自定义运行记录」分支）。
const radarCurrentBaselineID = "baseline-rm-20261116"

// baselineByID 按 ID 在内置台账中取基线行；未命中返回零值与 false（消费方自行回退，勿按固定下标硬取）。
func baselineByID(baselines []EvalRunRecord, id string) (EvalRunRecord, bool) {
	for _, b := range baselines {
		if b.ID == id {
			return b, true
		}
	}
	return EvalRunRecord{}, false
}

// referenceBaselines 内置权威基线台账（按 CreatedAt 倒序：最新在前）。
//
// 顺序契约（勿随意调序）：
//   - 同代次 / 同日的并列基线相邻摆放（g11 zero 与 g11 pro 成对）；本台账只保证
//     「最新代次在最前、Segment 0（baseline-v070-init）恒在末位」；
//   - memory.go 的五维能力雷达在「尚无自定义运行记录」时按 radarCurrentBaselineID 取当前臂
//     （rl20，未命中回退下标 1），按末位取基线臂（Segment 0）⇒ 新增基线插首位不再改变雷达语义；
//   - eval.vue 的默认选中记录仍取 baselines[1]（当前即 `g11` `pro` 行；随首位插入漂移），
//     即「默认展示最新代次里的第二条并列基线」；若需与雷达同臂，须在前端改按 radarCurrentBaselineID 解析。

// loadRunHistoryBySuite 读测评任务表并只返回当前套件的运行记录（台账、停止回包与批次回包共用同一口径）。
// 读失败不伪装成“空台账”：回空并记日志，让调用点同时把错误如实呈现。
func loadRunHistoryBySuite(suite string) []EvalRunRecord {
	history, err := loadEvalTaskHistory(suite)
	if err != nil {
		log.Printf("[eval] 读取测评任务表失败: %v", err)
		return []EvalRunRecord{}
	}
	return history
}

// loadRunHistoryAll 读测评任务表全量（不分套件），供榜单页与五维雷达共用；
// 读失败如实记日志并回空（调用点按“无自定义运行记录”口径回退内置基线，不臆造成绩）。
func loadRunHistoryAll() []EvalRunRecord {
	history, err := loadEvalTaskHistory("")
	if err != nil {
		log.Printf("[eval] 读取测评任务表失败: %v", err)
		return []EvalRunRecord{}
	}
	return history
}

func loadAIConfig(home string) AIModelConfig {
	cfg := AIModelConfig{
		Provider:    "openai",
		BaseURL:     "https://api.deepseek.com/v1",
		AnswerModel: "deepseek-chat",
		JudgeModel:  "deepseek-chat",
		Temperature: 0.0,
		MaxTokens:   32768,
		TimeoutSec:  180,
	}
	raw, err := os.ReadFile(aiConfigPath(home))
	if err == nil {
		_ = json.Unmarshal(raw, &cfg)
	}
	// 推理模型（如 deepseek-v4.1-flash）在多模态长上下文下需要充足生成窗口，
	// 旧配置文件的 1200 tokens 会导致思考未完即被截断；小于 32k 时自动抬升到 32768。
	if cfg.MaxTokens < 32768 {
		cfg.MaxTokens = 32768
	}
	if cfg.TimeoutSec < 180 {
		cfg.TimeoutSec = 180
	}
	if cfg.APIKey == "" {
		cfg.APIKey = os.Getenv("OPENAI_API_KEY")
	}
	cfg.HasAPIKey = strings.TrimSpace(cfg.APIKey) != ""
	return cfg
}

func saveAIConfig(home string, incoming AIModelConfig) (AIModelConfig, error) {
	current := loadAIConfig(home)
	if strings.TrimSpace(incoming.Provider) != "" {
		current.Provider = strings.TrimSpace(incoming.Provider)
	}
	if strings.TrimSpace(incoming.BaseURL) != "" {
		current.BaseURL = strings.TrimSpace(incoming.BaseURL)
	}
	if strings.TrimSpace(incoming.AnswerModel) != "" {
		current.AnswerModel = strings.TrimSpace(incoming.AnswerModel)
	}
	if strings.TrimSpace(incoming.JudgeModel) != "" {
		current.JudgeModel = strings.TrimSpace(incoming.JudgeModel)
	}
	if incoming.MaxTokens > 0 {
		current.MaxTokens = incoming.MaxTokens
	}
	if incoming.TimeoutSec > 0 {
		current.TimeoutSec = incoming.TimeoutSec
	}
	current.Temperature = incoming.Temperature
	trimmedKey := strings.TrimSpace(incoming.APIKey)
	if trimmedKey != "" && !strings.Contains(trimmedKey, "***") {
		current.APIKey = trimmedKey
	}
	current.HasAPIKey = strings.TrimSpace(current.APIKey) != ""

	path := aiConfigPath(home)
	data, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return current, err
	}
	if err := writeConsoleFileAtomic(path, data, 0o600); err != nil {
		return current, err
	}
	return current, nil
}

func maskAIConfig(cfg AIModelConfig) AIModelConfig {
	out := cfg
	out.HasAPIKey = strings.TrimSpace(cfg.APIKey) != ""
	if len(cfg.APIKey) > 8 {
		out.APIKey = cfg.APIKey[:4] + "***" + cfg.APIKey[len(cfg.APIKey)-4:]
	} else if out.HasAPIKey {
		out.APIKey = "******"
	}
	return out
}

func buildProtocolParitySpec() ProtocolParitySpec {
	ansSHA := sha1Hex(FrozenAnswerSystem)
	jdgSysSHA := sha1Hex(FrozenJudgeSystem)
	jdgTplSHA := sha1Hex(FrozenJudgeUserTPL)
	return ProtocolParitySpec{
		BuiltinProtocol:        "locomo-backboard",
		ProtocolID:             FrozenProtocolID,
		LeaderboardName:        "locomo-backboard 商业记忆系统榜单 (Backboard Long-Term Memory Leaderboard)",
		LeaderboardNameEn:      "locomo-backboard Commercial Memory Leaderboard (Backboard.io)",
		CorpusName:             "LoCoMo Benchmark（ACL 2024 · 10 组跨月长对话 · 5,882 轮 · 1,540 道评测问答）",
		CorpusNameEn:           "LoCoMo Benchmark (ACL 2024 · 10 multi-session dialogues · 5,882 turns · 1,540 QAs)",
		AnswerSystemPrompt:     FrozenAnswerSystem,
		AnswerSystemSHA1:       ansSHA,
		AnswerPayloadSpec:      "编号记忆行（N. {id} · {content}）+ 空行（\\n\\n）+ 原题直问（零改写模板、零格式束缚；无记忆时占位 1. （no memories））",
		AnswerPayloadSpecEn:    "Numbered memory lines (N. {id} · {content}) + blank line (\\n\\n) + direct original question (zero template wrapping; fallback: 1. （no memories）)",
		JudgeSystemPrompt:      FrozenJudgeSystem,
		JudgeSystemSHA1:        jdgSysSHA,
		JudgeTemplateSHA1:      jdgTplSHA,
		JudgeTemplateBytes:     len(FrozenJudgeUserTPL),
		JudgeVerdictSpec:       `JSON 闭集输出 {"reasoning": "一句话判分理由", "label": "CORRECT" | "WRONG"}（大小写严格校验，解析失败记 JUDGE_UNPARSED）`,
		JudgeVerdictSpecEn:     `Strict JSON output {"reasoning": "one-sentence explanation", "label": "CORRECT" | "WRONG"} (case-sensitive; malformed responses marked JUDGE_UNPARSED)`,
		AllPinsVerified:        ansSHA == PinAnswerSystemSHA1 && jdgSysSHA == PinJudgeSystemSHA1 && jdgTplSHA == PinJudgeUserTPLSHA1,
		CategoryMappingNote:    "Cat 4 = 单跳事实 (Single-hop) · Cat 1 = 多跳推理 (Multi-hop) · Cat 3 = 开放域推理 (Open-domain) · Cat 2 = 时间推理 (Temporal)",
		CategoryMappingNoteEn:  "Cat 4 = Single-hop Factual · Cat 1 = Multi-hop Reasoning · Cat 3 = Open-domain Reasoning · Cat 2 = Temporal Reasoning",
		FairComparisonNotice:   "同源引擎与隔离评估声明：内置评测协议与 locomo-backboard（https://github.com/Backboard-io/Backboard-Locomo-Benchmark）保持逐字节 Answer/Judge 提示词与三枚 SHA-1 摘要一致。评测直接调用生产环境的同一套本地引擎（local_plain），通过独立评测作用域（*-eval-*）与个人记忆物理隔离。我方各行按行内判官列标注判官口径（现行本机行为 deepseek-v4.1-flash；历史架构行为 GPT-4.1 temp 0.1），横向比较请逐行按判官口径对照。",
		FairComparisonNoticeEn: "Production-Engine & Scope-Isolation Statement: the built-in harness retains byte-identical locomo-backboard Answer/Judge prompts and three SHA-1 pins, evaluating the same local memory engine (local_plain) used in production within an isolated evaluation scope (*-eval-*). Each of our rows carries its judge ruler in the judge column (current on-device row: deepseek-v4.1-flash; historical architecture row: GPT-4.1 temp 0.1) — compare row by row against the judge column.",
	}
}

func locomoBackboardLeaderboard() []LeaderboardEntry {
	entries := []LeaderboardEntry{
		{
			// 我方现行引擎行：1,540 题全量官方 Cat 1–4，同分母参与主榜排名。
			// 读数同源：referenceBaselines() 的 baseline-rm-20261116（判对 1313/1540）。
			ID:             "lb-rm-current-local-plain",
			System:         "Rolling Autonoetic Memory Engine (the memory engine · 0-Vector / 0-LLM)",
			SystemEn:       "Rolling Autonoetic Memory Engine (the memory engine · 0-Vector / 0-LLM)",
			Vendor:         "Rolling-Memory (洛灵自知记忆引擎 · the memory engine)",
			VendorEn:       "Rolling-Memory (Ours · Rolling Autonoetic Memory Engine)",
			Architecture:   "Rolling Autonoetic Memory Engine（Wave 频域干涉波形 × 记忆地址检索 + 2-Hop 波峰桥接 + 三层拓扑 · 0-Vector/0-LLM）",
			ArchitectureEn: "Rolling Autonoetic Memory Engine (Wave Interference × Memory-Address Retrieval + 2-Hop Peak Bridging + 3-Layer Manifold · 0-Vector/0-LLM)",
			Protocol:       "locomo-backboard (" + FrozenProtocolID + ")",
			ProtocolEn:     "locomo-backboard (" + FrozenProtocolID + ")",
			QuestionSet:    "LoCoMo 1,540 题全量（官方 Cat 1–4 · 本机实测）",
			QuestionSetEn:  "LoCoMo Full (1,540 Qs · official Cat 1–4 · on-device run)",
			JudgeModel:     "deepseek-v4.1-flash (temp 0)",
			JudgeModelEn:   "deepseek-v4.1-flash (temp 0)",
			SingleHop:      90.96,
			MultiHop:       70.57,
			OpenDomain:     71.88,
			Temporal:       87.23,
			Overall:        85.26,
			EvidenceHit:    0,
			IsOurs:         true,
			IsPublicBoard:  false,
			HighlightTag:   "本机实测 · 0-Vector/0-LLM · 1,540 全量 85.26%",
			HighlightTagEn: "On-device run · 0-Vector/0-LLM · full 1,540 set 85.26%",
			Note:           "本机实测：0-Vector / 0-LLM 本地明文检索核（检索 100% 离线、零外联）在官方 Cat 1–4 全集 1,540 题上判对 1313/1540 = 85.26%（Cat 4 单跳 90.96% / Cat 2 时序 87.23% / Cat 1 多跳 70.57% / Cat 3 开放域 71.88%）。判官 deepseek-v4.1-flash（冻结协议口径，与外部榜行 GPT-4.1 判官不同，请按行内判官列对照）。封存基线 baseline-rm-20261116，台账 run-20261116-170142（1,540 条逐题明细）。",
			NoteEn:         "On-device run: the 0-Vector / 0-LLM local plaintext retrieval core (100% offline, zero network egress) judged 1313/1540 = 85.26% on the official Cat 1–4 full set (Cat 4 Single-hop 90.96% / Cat 2 Temporal 87.23% / Cat 1 Multi-hop 70.57% / Cat 3 Open-domain 71.88%). Judge: deepseek-v4.1-flash (frozen-protocol ruler, distinct from the external rows' GPT-4.1 — compare against the judge column). Sealed baseline baseline-rm-20261116, ledger run-20261116-170142 (1,540 per-question rows).",
			Categories: CategoryScores{
				Cat4: 0.9096, Cat1: 0.7057, Cat3: 0.7188, Cat2: 0.8723,
			},
		},
		{
			// 我方历史成绩行：早期 LLM + 向量重型架构，1,540 题全量同尺，仅作架构演进对照（该路线不再提供）。
			ID:             "lb-rm-historical-llm-vector",
			System:         "Rolling Memory (LLM + 向量 · 历史架构)",
			SystemEn:       "Rolling Memory (LLM + Vector · Historical Architecture)",
			Vendor:         "Rolling-Memory (我方历史成绩)",
			VendorEn:       "Rolling-Memory (Ours · historical)",
			Architecture:   "早期重型混合架构：LLM 抽取 + 向量检索 + 图/关系增强",
			ArchitectureEn: "Early heavyweight hybrid: LLM extraction + vector retrieval + graph/relational augmentation",
			Protocol:       "locomo-backboard (1,540 题全量同尺)",
			ProtocolEn:     "locomo-backboard (full 1,540 set, same ruler)",
			QuestionSet:    "LoCoMo 1,540 题全量",
			QuestionSetEn:  "LoCoMo Full (1,540 Qs)",
			JudgeModel:     "GPT-4.1 (temp 0.1)",
			JudgeModelEn:   "GPT-4.1 (temp 0.1)",
			SingleHop:      89.10,
			MultiHop:       88.40,
			OpenDomain:     81.30,
			Temporal:       94.40,
			Overall:        92.79,
			EvidenceHit:    98.90,
			IsOurs:         true,
			IsPublicBoard:  false,
			HighlightTag:   "历史成绩 · 早期重型架构（不再提供）",
			HighlightTagEn: "Historical result · early heavyweight architecture (no longer provided)",
			Note:           "历史成绩：我方早期重型架构（LLM 抽取 + 向量检索 + 图/关系增强）在 LoCoMo 1,540 题全量同尺上的存档读数：总分 92.79%（单跳 89.10% / 多跳 88.40% / 开放域 81.30% / 时序 94.40% / 证据命中 98.90%）；GPT-4.1 temp 0.1 判官，与外部榜行同判官口径。该路线已不再提供，仅作架构演进对照；现行引擎为 0-Vector / 0-LLM 本机离线检索（见上行）。",
			NoteEn:         "Historical result: our early heavyweight architecture (LLM extraction + vector retrieval + graph/relational augmentation) on the full LoCoMo 1,540-question set: 92.79% overall (Single-hop 89.10% / Multi-hop 88.40% / Open-domain 81.30% / Temporal 94.40% / Evidence Hit 98.90%); GPT-4.1 temp 0.1 judge — same ruler as the external rows. This route is no longer provided and is kept as an architecture-evolution reference; the current engine is the 0-Vector / 0-LLM on-device retriever (see the row above).",
			Categories: CategoryScores{
				Cat4: 0.8910, Cat1: 0.8840, Cat3: 0.8130, Cat2: 0.9440,
			},
		},
		{
			ID:             "lb-backboard",
			System:         "Backboard",
			SystemEn:       "Backboard",
			Vendor:         "Backboard.io (榜单维护方)",
			VendorEn:       "Backboard.io (Leaderboard Host)",
			Architecture:   "商业记忆助手 API (Commercial Memory Assistant)",
			ArchitectureEn: "Commercial Memory Assistant API",
			Protocol:       "locomo-backboard (Official Harness)",
			ProtocolEn:     "locomo-backboard (Official Harness)",
			QuestionSet:    "LoCoMo 1,540 题全量",
			QuestionSetEn:  "LoCoMo Full (1,540 Qs)",
			JudgeModel:     "GPT-4.1 (temp 0.1)",
			JudgeModelEn:   "GPT-4.1 (temp 0.1)",
			SingleHop:      89.36,
			MultiHop:       75.00,
			OpenDomain:     91.20,
			Temporal:       91.90,
			Overall:        90.00,
			EvidenceHit:    0,
			IsOurs:         false,
			IsPublicBoard:  true,
			HighlightTag:   "外部商业系统 #1 · 90.0%",
			HighlightTagEn: "External Commercial #1 · 90.0%",
			Note:           "locomo-backboard 商业榜单发起方与外部商业系统最高分（开放域 91.20%、单跳 89.36%）",
			NoteEn:         "Creator of the locomo-backboard benchmark and #1 among external commercial vendors (Open-domain 91.20%, Single-hop 89.36%)",
			Categories: CategoryScores{
				Cat4: 0.8936, Cat1: 0.7500, Cat3: 0.9120, Cat2: 0.9190,
			},
		},
		{
			ID:             "lb-memobase-037",
			System:         "Memobase v0.0.37",
			SystemEn:       "Memobase v0.0.37",
			Vendor:         "Memobase",
			VendorEn:       "Memobase",
			Architecture:   "用户画像长期记忆 (Profile-based Long-Term Memory)",
			ArchitectureEn: "Profile-based Long-Term Memory",
			Protocol:       "locomo-backboard (Official Harness)",
			ProtocolEn:     "locomo-backboard (Official Harness)",
			QuestionSet:    "LoCoMo 1,540 题全量",
			QuestionSetEn:  "LoCoMo Full (1,540 Qs)",
			JudgeModel:     "GPT-4.1 (temp 0.1)",
			JudgeModelEn:   "GPT-4.1 (temp 0.1)",
			SingleHop:      70.92,
			MultiHop:       46.88,
			OpenDomain:     77.17,
			Temporal:       85.05,
			Overall:        75.78,
			EvidenceHit:    0,
			IsOurs:         false,
			IsPublicBoard:  true,
			HighlightTag:   "外部商业榜 #2",
			HighlightTagEn: "External Commercial #2",
			Note:           "locomo-backboard 公开商业榜外部系统第 2 名，时序推理 (85.05%) 表现较好",
			NoteEn:         "#2 among external systems on the public locomo-backboard leaderboard, strong in Temporal reasoning (85.05%)",
			Categories: CategoryScores{
				Cat4: 0.7092, Cat1: 0.4688, Cat3: 0.7717, Cat2: 0.8505,
			},
		},
		{
			ID:             "lb-zep",
			System:         "Zep (Graphiti)",
			SystemEn:       "Zep (Graphiti)",
			Vendor:         "Zep AI",
			VendorEn:       "Zep AI",
			Architecture:   "时序知识图谱 + LLM 重排 (Temporal KG + LLM Rerank)",
			ArchitectureEn: "Temporal Knowledge Graph + LLM Rerank",
			Protocol:       "locomo-backboard (Official Harness)",
			ProtocolEn:     "locomo-backboard (Official Harness)",
			QuestionSet:    "LoCoMo 1,540 题全量",
			QuestionSetEn:  "LoCoMo Full (1,540 Qs)",
			JudgeModel:     "GPT-4.1 (temp 0.1)",
			JudgeModelEn:   "GPT-4.1 (temp 0.1)",
			SingleHop:      74.11,
			MultiHop:       66.04,
			OpenDomain:     67.71,
			Temporal:       79.79,
			Overall:        75.14,
			EvidenceHit:    0,
			IsOurs:         false,
			IsPublicBoard:  true,
			HighlightTag:   "外部商业榜 #3",
			HighlightTagEn: "External Commercial #3",
			Note:           "时序知识图谱架构，locomo-backboard 公开商业榜总分 75.14%",
			NoteEn:         "Temporal knowledge graph architecture, scoring 75.14% overall on the public locomo-backboard leaderboard",
			Categories: CategoryScores{
				Cat4: 0.7411, Cat1: 0.6604, Cat3: 0.6771, Cat2: 0.7979,
			},
		},
		{
			ID:             "lb-memobase-032",
			System:         "Memobase v0.0.32",
			SystemEn:       "Memobase v0.0.32",
			Vendor:         "Memobase",
			VendorEn:       "Memobase",
			Architecture:   "用户画像长期记忆 (Profile-based Long-Term Memory)",
			ArchitectureEn: "Profile-based Long-Term Memory",
			Protocol:       "locomo-backboard (Official Harness)",
			ProtocolEn:     "locomo-backboard (Official Harness)",
			QuestionSet:    "LoCoMo 1,540 题全量",
			QuestionSetEn:  "LoCoMo Full (1,540 Qs)",
			JudgeModel:     "GPT-4.1 (temp 0.1)",
			JudgeModelEn:   "GPT-4.1 (temp 0.1)",
			SingleHop:      63.83,
			MultiHop:       52.08,
			OpenDomain:     71.82,
			Temporal:       80.37,
			Overall:        70.91,
			EvidenceHit:    0,
			IsOurs:         false,
			IsPublicBoard:  true,
			HighlightTag:   "外部商业榜 #4",
			HighlightTagEn: "External Commercial #4",
			Note:           "Memobase v0.0.32 在 locomo-backboard 公开商业榜基准得分 70.91%",
			NoteEn:         "Earlier Memobase v0.0.32 release on the public locomo-backboard leaderboard (70.91%)",
			Categories: CategoryScores{
				Cat4: 0.6383, Cat1: 0.5208, Cat3: 0.7182, Cat2: 0.8037,
			},
		},
		{
			ID:             "lb-mem0-graph",
			System:         "Mem0-Graph",
			SystemEn:       "Mem0-Graph",
			Vendor:         "Mem0 AI",
			VendorEn:       "Mem0 AI",
			Architecture:   "向量 + 图记忆混合存储 (Vector + Graph Hybrid)",
			ArchitectureEn: "Vector + Graph Memory Hybrid",
			Protocol:       "locomo-backboard (Official Harness)",
			ProtocolEn:     "locomo-backboard (Official Harness)",
			QuestionSet:    "LoCoMo 1,540 题全量",
			QuestionSetEn:  "LoCoMo Full (1,540 Qs)",
			JudgeModel:     "GPT-4.1 (temp 0.1)",
			JudgeModelEn:   "GPT-4.1 (temp 0.1)",
			SingleHop:      65.71,
			MultiHop:       47.19,
			OpenDomain:     75.71,
			Temporal:       58.13,
			Overall:        68.44,
			EvidenceHit:    0,
			IsOurs:         false,
			IsPublicBoard:  true,
			HighlightTag:   "外部商业榜 #5",
			HighlightTagEn: "External Commercial #5",
			Note:           "Mem0 图增强版在 locomo-backboard 公开商业榜实测得分 68.44%",
			NoteEn:         "Mem0 Graph-enhanced variant on the public locomo-backboard commercial leaderboard (68.44%)",
			Categories: CategoryScores{
				Cat4: 0.6571, Cat1: 0.4719, Cat3: 0.7571, Cat2: 0.5813,
			},
		},
		{
			ID:             "lb-mem0",
			System:         "Mem0",
			SystemEn:       "Mem0",
			Vendor:         "Mem0 AI",
			VendorEn:       "Mem0 AI",
			Architecture:   "向量记忆存储 (Vector Memory Store)",
			ArchitectureEn: "Vector Memory Store",
			Protocol:       "locomo-backboard (Official Harness)",
			ProtocolEn:     "locomo-backboard (Official Harness)",
			QuestionSet:    "LoCoMo 1,540 题全量",
			QuestionSetEn:  "LoCoMo Full (1,540 Qs)",
			JudgeModel:     "GPT-4.1 (temp 0.1)",
			JudgeModelEn:   "GPT-4.1 (temp 0.1)",
			SingleHop:      67.13,
			MultiHop:       51.15,
			OpenDomain:     72.93,
			Temporal:       55.51,
			Overall:        66.88,
			EvidenceHit:    0,
			IsOurs:         false,
			IsPublicBoard:  true,
			HighlightTag:   "外部商业榜 #6",
			HighlightTagEn: "External Commercial #6",
			Note:           "Mem0 标准版在 locomo-backboard 公开商业榜实测得分 66.88%",
			NoteEn:         "Standard Mem0 on the public locomo-backboard commercial leaderboard (66.88%)",
			Categories: CategoryScores{
				Cat4: 0.6713, Cat1: 0.5115, Cat3: 0.7293, Cat2: 0.5551,
			},
		},
		{
			ID:             "lb-langmem",
			System:         "LangMem",
			SystemEn:       "LangMem",
			Vendor:         "LangChain",
			VendorEn:       "LangChain",
			Architecture:   "LangChain 记忆组件 (Agentic Memory SDK)",
			ArchitectureEn: "Agentic Memory SDK",
			Protocol:       "locomo-backboard (Official Harness)",
			ProtocolEn:     "locomo-backboard (Official Harness)",
			QuestionSet:    "LoCoMo 1,540 题全量",
			QuestionSetEn:  "LoCoMo Full (1,540 Qs)",
			JudgeModel:     "GPT-4.1 (temp 0.1)",
			JudgeModelEn:   "GPT-4.1 (temp 0.1)",
			SingleHop:      62.23,
			MultiHop:       47.92,
			OpenDomain:     71.12,
			Temporal:       23.43,
			Overall:        58.10,
			EvidenceHit:    0,
			IsOurs:         false,
			IsPublicBoard:  true,
			HighlightTag:   "外部商业榜 #7",
			HighlightTagEn: "External Commercial #7",
			Note:           "LangChain 记忆模块在 locomo-backboard 公开榜得分 58.10%（时序推理 23.43%）",
			NoteEn:         "LangChain memory SDK on the public locomo-backboard leaderboard at 58.10% (Temporal 23.43%)",
			Categories: CategoryScores{
				Cat4: 0.6223, Cat1: 0.4792, Cat3: 0.7112, Cat2: 0.2343,
			},
		},
		{
			ID:             "lb-openai",
			System:         "OpenAI Memory",
			SystemEn:       "OpenAI Memory",
			Vendor:         "OpenAI",
			VendorEn:       "OpenAI",
			Architecture:   "ChatGPT 内置记忆 (Built-in ChatGPT Memory)",
			ArchitectureEn: "Built-in ChatGPT Memory",
			Protocol:       "locomo-backboard (Official Harness)",
			ProtocolEn:     "locomo-backboard (Official Harness)",
			QuestionSet:    "LoCoMo 1,540 题全量",
			QuestionSetEn:  "LoCoMo Full (1,540 Qs)",
			JudgeModel:     "GPT-4.1 (temp 0.1)",
			JudgeModelEn:   "GPT-4.1 (temp 0.1)",
			SingleHop:      63.79,
			MultiHop:       42.92,
			OpenDomain:     62.29,
			Temporal:       21.71,
			Overall:        52.90,
			EvidenceHit:    0,
			IsOurs:         false,
			IsPublicBoard:  true,
			HighlightTag:   "外部商业榜 #8",
			HighlightTagEn: "External Commercial #8",
			Note:           "OpenAI 内置记忆在 locomo-backboard 公开商业榜基线得分 52.90%",
			NoteEn:         "OpenAI built-in memory baseline on the public locomo-backboard commercial leaderboard (52.90%)",
			Categories: CategoryScores{
				Cat4: 0.6379, Cat1: 0.4292, Cat3: 0.6229, Cat2: 0.2171,
			},
		},
	}
	// 排名契约（与前端展示一致）：主榜只对「同题集 / 同分母」行（1,540 题全量）按 Overall 降序落 Rank=1..N；
	// 不同题集 / 不同分母行（NotRanked=true）不参与排名，Rank 恒为 0 并统一排在榜尾，
	// 既不虚构跨题集名次，也保证名次与总分永不倒挂。
	ranked := make([]LeaderboardEntry, 0, len(entries))
	unranked := make([]LeaderboardEntry, 0, len(entries))
	for _, e := range entries {
		if e.NotRanked {
			e.Rank = 0
			unranked = append(unranked, e)
			continue
		}
		ranked = append(ranked, e)
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].Overall > ranked[j].Overall })
	for i := range ranked {
		ranked[i].Rank = i + 1
	}
	return append(ranked, unranked...)
}

func evalLeaderboardHandler(c *gin.Context) {
	home, _ := os.UserHomeDir()
	aiCfg := loadAIConfig(home)
	httpx.Success(c, httpx.WithData(EvalLeaderboardResponse{
		ProtocolSpec: buildProtocolParitySpec(),
		Entries:      locomoBackboardLeaderboard(),
		Agents:       buildOfficialAgents(aiCfg),
		Baselines:    referenceBaselines(),
		RunHistory:   loadRunHistoryAll(),
		ATMBench:     buildATMBenchLeaderboard(),
	}))
}

// evalATMLeaderboardHandler 剑桥 ATM-Bench 独立榜单页数据端点（权限节点 plugin/rolling-memory/eval/atm-leaderboard）。
func evalATMLeaderboardHandler(c *gin.Context) {
	home, _ := os.UserHomeDir()
	aiCfg := loadAIConfig(home)
	atmDir := resolveATMDataDir(home, "")
	spec := buildATMProtocolParitySpec()
	pins := atmOfficialPinSnapshotAdmin(atmDir)
	spec = applyATMVerificationStatus(spec, pins, atmDir)
	httpx.Success(c, httpx.WithData(EvalATMLeaderboardResponse{
		ATMBench:        buildATMBenchLeaderboard(),
		ATMProtocolSpec: spec,
		ATMAgents:       buildATMOfficialAgents(aiCfg),
		ATMBaselines:    atmReferenceBaselines(),
		ATMOfficialPins: pins,
	}))
}

// evalATMProtocolHandler 官方协议对齐门禁端点（后台协议页与 CI 共用）。
func evalATMProtocolHandler(c *gin.Context) {
	home, _ := os.UserHomeDir()
	atmDir := resolveATMDataDir(home, "")
	spec := buildATMProtocolParitySpec()
	pins := atmOfficialPinSnapshotAdmin(atmDir)
	spec = applyATMVerificationStatus(spec, pins, atmDir)
	ok := spec.AllPinsVerified && spec.OfficialScoringAligned
	httpx.Success(c, httpx.WithData(EvalATMProtocolResponse{
		OK:     ok,
		Spec:   spec,
		Pins:   pins,
		Agents: buildATMOfficialAgents(loadAIConfig(home)),
	}))
}

func buildOfficialAgents(cfg AIModelConfig) []OfficialProtocolAgent {
	ansSHA := sha1Hex(FrozenAnswerSystem)
	jdgSysSHA := sha1Hex(FrozenJudgeSystem)
	jdgTplSHA := sha1Hex(FrozenJudgeUserTPL)
	return []OfficialProtocolAgent{
		{
			Code:            "locomo-answer",
			Name:            "Backboard 官方答题智能体 (Answer Agent)",
			NameEn:          "Backboard Official Answer Agent",
			Role:            "answer",
			Protocol:        FrozenProtocolID,
			Model:           cfg.AnswerModel,
			SystemPrompt:    FrozenAnswerSystem,
			SystemSHA1:      ansSHA,
			UserTemplate:    "1. {id} · {memory_content}\n2. {id} · {memory_content}\n\n{question}",
			UserTemplateSHA: "-",
			PinVerified:     ansSHA == PinAnswerSystemSHA1,
		},
		{
			Code:            "locomo-judge",
			Name:            "Backboard 官方判官智能体 (Judge Agent)",
			NameEn:          "Backboard Official Judge Agent",
			Role:            "judge",
			Protocol:        FrozenProtocolID,
			Model:           cfg.JudgeModel,
			SystemPrompt:    FrozenJudgeSystem,
			SystemSHA1:      jdgSysSHA,
			UserTemplate:    FrozenJudgeUserTPL,
			UserTemplateSHA: jdgTplSHA,
			PinVerified:     jdgSysSHA == PinJudgeSystemSHA1 && jdgTplSHA == PinJudgeUserTPLSHA1,
		},
	}
}

func aiConfigGetHandler(c *gin.Context) {
	home, _ := os.UserHomeDir()
	cfg := loadAIConfig(home)
	httpx.Success(c, httpx.WithData(gin.H{
		"ai_config": maskAIConfig(cfg),
		"ai_check":  checkAIModelConfigWithPlugin(cfg),
		"agents":    buildOfficialAgents(cfg),
	}))
}

func aiConfigSaveHandler(c *gin.Context) {
	var req AIModelConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Fail(c, httpx.WithMessage("参数格式错误: "+err.Error()))
		return
	}
	home, _ := os.UserHomeDir()
	saved, err := saveAIConfig(home, req)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("保存 AI 配置失败: "+err.Error()))
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{
		"ai_config": maskAIConfig(saved),
		"ai_check":  checkAIModelConfigWithPlugin(saved),
		"agents":    buildOfficialAgents(saved),
	}))
}

func isLocalEndpoint(baseURL string) bool {
	lower := strings.ToLower(strings.TrimSpace(baseURL))
	return strings.Contains(lower, "127.0.0.1") ||
		strings.Contains(lower, "localhost") ||
		strings.Contains(lower, "[::1]") ||
		strings.Contains(lower, "0.0.0.0")
}

func mergeAIConfig(base AIModelConfig, override *AIModelConfig) AIModelConfig {
	if override == nil {
		return base
	}
	out := base
	if strings.TrimSpace(override.Provider) != "" {
		out.Provider = strings.TrimSpace(override.Provider)
	}
	if strings.TrimSpace(override.BaseURL) != "" {
		out.BaseURL = strings.TrimSpace(override.BaseURL)
	}
	if strings.TrimSpace(override.AnswerModel) != "" {
		out.AnswerModel = strings.TrimSpace(override.AnswerModel)
	}
	if strings.TrimSpace(override.JudgeModel) != "" {
		out.JudgeModel = strings.TrimSpace(override.JudgeModel)
	}
	if override.MaxTokens > 0 {
		out.MaxTokens = override.MaxTokens
	}
	if override.TimeoutSec > 0 {
		out.TimeoutSec = override.TimeoutSec
	}
	out.Temperature = override.Temperature
	trimmedKey := strings.TrimSpace(override.APIKey)
	if trimmedKey != "" && !strings.Contains(trimmedKey, "***") {
		out.APIKey = trimmedKey
	}
	out.HasAPIKey = strings.TrimSpace(out.APIKey) != ""
	return out
}

func checkAIModelConfig(cfg AIModelConfig) AIModelCheckResult {
	base := strings.TrimSpace(cfg.BaseURL)
	local := isLocalEndpoint(base)
	ansModel := strings.TrimSpace(cfg.AnswerModel)
	jdgModel := strings.TrimSpace(cfg.JudgeModel)
	hasKey := (strings.TrimSpace(cfg.APIKey) != "" && !strings.Contains(cfg.APIKey, "***")) || cfg.HasAPIKey

	urlPassed := false
	urlDetail := "未配置接口地址 (Base URL)"
	urlDetailEn := "Endpoint Base URL is empty"
	if base != "" {
		parsed, err := url.Parse(base)
		if err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" {
			urlPassed = true
			if local {
				urlDetail = fmt.Sprintf("本地回环端点 (%s)", base)
				urlDetailEn = fmt.Sprintf("Local loopback endpoint (%s)", base)
			} else {
				urlDetail = fmt.Sprintf("远端 OpenAI 兼容端点 (%s)", base)
				urlDetailEn = fmt.Sprintf("Remote OpenAI-compatible endpoint (%s)", base)
			}
		} else {
			urlDetail = fmt.Sprintf("接口地址格式无效 (%s)，须以 http:// 或 https:// 开头", base)
			urlDetailEn = fmt.Sprintf("Invalid Base URL (%s); must start with http:// or https://", base)
		}
	}

	keyPassed := hasKey || local
	keyDetail := "已配置有效密钥"
	keyDetailEn := "API Key is configured"
	if !hasKey {
		if local {
			keyDetail = "本地端点免密钥模式（可用）"
			keyDetailEn = "Local endpoint keyless mode (allowed)"
		} else {
			keyDetail = fmt.Sprintf("当前接口 (%s) 为远端服务，尚未配置 API Key，无法调用模型", base)
			keyDetailEn = fmt.Sprintf("Remote endpoint (%s) requires a valid API Key before running agent evaluation", base)
		}
	}

	ansPassed := ansModel != ""
	ansDetail := fmt.Sprintf("locomo-answer 绑定模型: %s", ansModel)
	ansDetailEn := fmt.Sprintf("locomo-answer bound to: %s", ansModel)
	if !ansPassed {
		ansDetail = "未指定 Answer 作答模型名称（如 deepseek-chat / gpt-4.1）"
		ansDetailEn = "Answer Agent model name is empty (e.g. deepseek-chat / gpt-4.1)"
	}

	jdgPassed := jdgModel != ""
	jdgDetail := fmt.Sprintf("locomo-judge 绑定模型: %s", jdgModel)
	jdgDetailEn := fmt.Sprintf("locomo-judge bound to: %s", jdgModel)
	if !jdgPassed {
		jdgDetail = "未指定 Judge 判官模型名称（如 deepseek-chat / gpt-4.1）"
		jdgDetailEn = "Judge Agent model name is empty (e.g. deepseek-chat / gpt-4.1)"
	}

	ansSHA := sha1Hex(FrozenAnswerSystem)
	jdgSysSHA := sha1Hex(FrozenJudgeSystem)
	jdgTplSHA := sha1Hex(FrozenJudgeUserTPL)
	pinsPassed := ansSHA == PinAnswerSystemSHA1 && jdgSysSHA == PinJudgeSystemSHA1 && jdgTplSHA == PinJudgeUserTPLSHA1
	pinsDetail := "三枚 SHA1 协议钉 100% 校验通过 (locomo-frozen-2026-10-11)"
	pinsDetailEn := "All 3 SHA1 protocol pins verified (locomo-frozen-2026-10-11)"
	if !pinsPassed {
		pinsDetail = "冻结协议 SHA1 钉校验失败"
		pinsDetailEn = "Frozen protocol SHA1 pin verification failed"
	}

	checks := []AICheckItem{
		{Key: "base_url", Label: "1. 接口地址 (Base URL)", LabelEn: "1. Endpoint Base URL", Passed: urlPassed, Detail: urlDetail, DetailEn: urlDetailEn, Blocking: true},
		{Key: "api_key", Label: "2. 鉴权密钥 (API Key)", LabelEn: "2. Authentication API Key", Passed: keyPassed, Detail: keyDetail, DetailEn: keyDetailEn, Blocking: true},
		{Key: "answer_model", Label: "3. 官方答题智能体模型 (locomo-answer)", LabelEn: "3. Official Answer Agent Model (locomo-answer)", Passed: ansPassed, Detail: ansDetail, DetailEn: ansDetailEn, Blocking: true},
		{Key: "judge_model", Label: "4. 官方判官智能体模型 (locomo-judge)", LabelEn: "4. Official Judge Agent Model (locomo-judge)", Passed: jdgPassed, Detail: jdgDetail, DetailEn: jdgDetailEn, Blocking: true},
		{Key: "protocol_pins", Label: "5. 冻结协议三枚 SHA1 钉 (Fail-Closed)", LabelEn: "5. Frozen Protocol 3 SHA1 Pins (Fail-Closed)", Passed: pinsPassed, Detail: pinsDetail, DetailEn: pinsDetailEn, Blocking: true},
	}

	ready := urlPassed && keyPassed && ansPassed && jdgPassed && pinsPassed
	summary := "AI 模型通道与冻结协议配置已就绪，可执行 Answer + Judge 双智能体评测"
	summaryEn := "AI model channel & frozen protocol pins are ready for Answer + Judge evaluation"
	if !ready {
		var mZh, mEn []string
		for _, c := range checks {
			if !c.Passed {
				mZh = append(mZh, c.Label)
				mEn = append(mEn, c.LabelEn)
			}
		}
		summary = "模型配置未就绪，缺少或未通过：" + strings.Join(mZh, "、")
		summaryEn = "Model configuration incomplete: " + strings.Join(mEn, ", ")
	}
	return AIModelCheckResult{Ready: ready, IsLocalhost: local, Summary: summary, SummaryEn: summaryEn, Checks: checks}
}

// loadAIPluginChannels 读取 AI 路由插件（plugins/ai）的渠道候选：渠道自身列 + 厂商继承列，
// 附「该渠道可服务的逻辑模型」清单（启用绑定）。AI 插件未安装（表不存在）时返回空清单，绝不报错——
// 评测实验室在未启用 AI 路由插件时仍必须可用。
func loadAIPluginChannels() []AIPluginChannelOption {
	options := []AIPluginChannelOption{}
	db := database.DB()
	if db == nil {
		return options
	}
	hasTable := func(name string) bool {
		var count int64
		row := db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Row()
		if row == nil {
			return false
		}
		if err := row.Scan(&count); err != nil {
			return false
		}
		return count > 0
	}
	if !hasTable("plugin_ai_channel") {
		return options
	}

	type channelRow struct {
		ID           int64  `gorm:"column:id"`
		Name         string `gorm:"column:name"`
		Vendor       string `gorm:"column:vendor"`
		SupplierID   int64  `gorm:"column:supplier_id"`
		BaseURL      string `gorm:"column:base_url"`
		APIKey       string `gorm:"column:api_key"`
		Model        string `gorm:"column:model"`
		Status       int16  `gorm:"column:status"`
		Circuit      string `gorm:"column:circuit"`
		SupBaseURL   string `gorm:"column:sup_base_url"`
		SupAPIKey    string `gorm:"column:sup_api_key"`
		SupStatus    int16  `gorm:"column:sup_status"`
		SupplierSeen int64  `gorm:"column:supplier_seen"`
	}
	rows := []channelRow{}
	query := `SELECT c."id" AS id, c."name" AS name, c."vendor" AS vendor, c."supplier_id" AS supplier_id,
			c."base_url" AS base_url, c."api_key" AS api_key, c."model" AS model,
			c."status" AS status, c."circuit" AS circuit,
			COALESCE(s."base_url", '') AS sup_base_url,
			COALESCE(s."api_key", '') AS sup_api_key,
			COALESCE(s."status", 1) AS sup_status,
			CASE WHEN s."id" IS NULL THEN 0 ELSE 1 END AS supplier_seen
		FROM "plugin_ai_channel" AS c
		LEFT JOIN "plugin_ai_supplier" AS s ON s."id" = c."supplier_id"
		ORDER BY c."priority" ASC, c."id" ASC
		LIMIT 200`
	if err := db.Raw(query).Scan(&rows).Error; err != nil {
		return options
	}

	modelsByChannel := map[int64][]string{}
	{
		type bindingRow struct {
			ChannelID int64  `gorm:"column:channel_id"`
			Name      string `gorm:"column:name"`
		}
		binds := []bindingRow{}
		if hasTable("plugin_ai_model_channel") && hasTable("plugin_ai_model") {
			_ = db.Raw(`SELECT mc."channel_id" AS channel_id, m."name" AS name
				FROM "plugin_ai_model_channel" AS mc
				JOIN "plugin_ai_model" AS m ON m."id" = mc."model_id"
				WHERE mc."status" = 1 AND m."status" = 1
				ORDER BY mc."channel_id" ASC, mc."priority" ASC
				LIMIT 2000`).Scan(&binds).Error
		}
		for _, item := range binds {
			modelsByChannel[item.ChannelID] = append(modelsByChannel[item.ChannelID], item.Name)
		}
	}

	for _, row := range rows {
		baseURL := strings.TrimSpace(row.BaseURL)
		if baseURL == "" {
			baseURL = strings.TrimSpace(row.SupBaseURL)
		}
		key := strings.TrimSpace(row.APIKey)
		if key == "" {
			key = strings.TrimSpace(row.SupAPIKey)
		}
		local := isLocalEndpoint(baseURL)
		vendorDisabled := row.SupplierSeen > 0 && row.SupplierID > 0 && row.SupStatus != 1
		routable := row.Status == 1 && baseURL != "" && (local || key != "") && row.Circuit != "open" && !vendorDisabled
		models := modelsByChannel[row.ID]
		if len(models) == 0 && strings.TrimSpace(row.Model) != "" {
			models = []string{strings.TrimSpace(row.Model)}
		}
		options = append(options, AIPluginChannelOption{
			ChannelID:    row.ID,
			ChannelName:  row.Name,
			Vendor:       row.Vendor,
			BaseURL:      baseURL,
			HasKey:       key != "",
			Status:       row.Status,
			Circuit:      row.Circuit,
			UpstreamName: strings.TrimSpace(row.Model),
			Routable:     routable,
			Models:       models,
		})
	}
	return options
}

// resolveAIPluginChannel 取一条渠道的**生效凭证**（渠道非空优先、空则继承厂商）与可服务模型清单。
func resolveAIPluginChannel(channelID int64) (baseURL, apiKey, upstreamName string, models []string, ok bool) {
	db := database.DB()
	if db == nil || channelID <= 0 {
		return "", "", "", nil, false
	}
	type row struct {
		Name       string `gorm:"column:name"`
		BaseURL    string `gorm:"column:base_url"`
		APIKey     string `gorm:"column:api_key"`
		Model      string `gorm:"column:model"`
		SupBaseURL string `gorm:"column:sup_base_url"`
		SupAPIKey  string `gorm:"column:sup_api_key"`
	}
	var found row
	query := `SELECT c."name" AS name, c."base_url" AS base_url, c."api_key" AS api_key, c."model" AS model,
			COALESCE(s."base_url", '') AS sup_base_url, COALESCE(s."api_key", '') AS sup_api_key
		FROM "plugin_ai_channel" AS c
		LEFT JOIN "plugin_ai_supplier" AS s ON s."id" = c."supplier_id"
		WHERE c."id" = ?
		LIMIT 1`
	if err := db.Raw(query, channelID).Scan(&found).Error; err != nil {
		return "", "", "", nil, false
	}
	if strings.TrimSpace(found.Name) == "" && strings.TrimSpace(found.BaseURL) == "" && strings.TrimSpace(found.SupBaseURL) == "" {
		return "", "", "", nil, false
	}
	baseURL = strings.TrimSpace(found.BaseURL)
	if baseURL == "" {
		baseURL = strings.TrimSpace(found.SupBaseURL)
	}
	apiKey = strings.TrimSpace(found.APIKey)
	if apiKey == "" {
		apiKey = strings.TrimSpace(found.SupAPIKey)
	}
	upstreamName = strings.TrimSpace(found.Model)

	bindingModels := []string{}
	_ = db.Raw(`SELECT m."name" AS name FROM "plugin_ai_model_channel" AS mc
		JOIN "plugin_ai_model" AS m ON m."id" = mc."model_id"
		WHERE mc."channel_id" = ? AND mc."status" = 1 AND m."status" = 1
		ORDER BY mc."priority" ASC, m."id" ASC LIMIT 50`, channelID).Scan(&bindingModels).Error
	seen := map[string]bool{}
	for _, name := range bindingModels {
		name = strings.TrimSpace(name)
		if name != "" && !seen[name] {
			seen[name] = true
			models = append(models, name)
		}
	}
	if len(models) == 0 && upstreamName != "" {
		models = []string{upstreamName}
	}
	return baseURL, apiKey, upstreamName, models, true
}

// checkAIModelConfigWithPlugin 预检 + 附 AI 路由插件渠道候选（评测实验室前置拦截弹窗的数据面）。
func checkAIModelConfigWithPlugin(cfg AIModelConfig) AIModelCheckResult {
	check := checkAIModelConfig(cfg)
	check.PluginChannels = loadAIPluginChannels()
	for _, option := range check.PluginChannels {
		if option.Routable {
			check.PluginChannelsOK = true
			break
		}
	}
	return check
}

func resolveEvalDataDir(home string) string {
	candidates := []string{
		os.Getenv("ROLLING_MEMORY_EVAL_DATA"),
	}
	if xdgData := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); xdgData != "" {
		candidates = append(candidates, filepath.Join(xdgData, "rolling-memory", "evaldata"))
	}
	if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
		candidates = append(candidates, filepath.Join(localAppData, "rolling-memory", "evaldata"))
	}
	if strings.TrimSpace(home) != "" {
		candidates = append(candidates,
			filepath.Join(home, ".local", "share", "rolling-memory", "evaldata"),
			filepath.Join(home, "Rolling-Memory", "evaldata"),
			filepath.Join(home, "rolling-memory", "evaldata"),
		)
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(wd, "evaldata"),
			filepath.Join(wd, "..", "evaldata"),
		)
	}
	for _, cand := range candidates {
		if cand == "" {
			continue
		}
		if info, err := os.Stat(filepath.Join(cand, "MANIFEST.json")); err == nil && !info.IsDir() {
			return cand
		}
	}
	return filepath.Join(home, ".local", "share", "rolling-memory", "evaldata")
}

// resolveMemoryDBPath 供插件其他记忆管理页面使用；评测链路不调用它。
func resolveMemoryDBPath(home string) string {
	if envDB := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_DB")); envDB != "" {
		return envDB
	}
	return filepath.Join(home, ".local", "share", "rolling-memory", "memory.db")
}

// EvalManifest 是 rmem eval 写入 <store>.manifest.json 的语料回执。结构体名与 JSON 键名
// （transport / endpoint / tenant_id / user_id / pulse_mode / ...）为跨版本数据兼容保持不变；
// 本版本只接受本地明文通道的 manifest（transport/endpoint 均指向本地金库）。
// LocalFallback 使用指针，确保字段缺失与显式 false 不会被混为一谈。
type EvalManifest struct {
	Transport     string `json:"transport"`
	Endpoint      string `json:"endpoint"`
	TenantID      string `json:"tenant_id"`
	UserID        string `json:"user_id"`
	Scope         string `json:"scope"`
	Suite         string `json:"suite"`
	PulseMode     string `json:"pulse_mode"`
	CorpusSHA256  string `json:"corpus_sha256"`
	Documents     int    `json:"documents"`
	LocalFallback *bool  `json:"local_fallback"`
	// PurgedUnits 是本次重算在重灌前清空的旧记忆条数（rebuild 口径按本 store 权威全集清点删除）；
	// 0 = 该 scope 原本为空或走的是复用/续灌路径。
	PurgedUnits int `json:"purged_units,omitempty"`
}

// EvalReadiness 描述 manifest 与本地严格探针的当前状态。
type EvalReadiness struct {
	Ready         bool   `json:"ready"`
	Suite         string `json:"suite"`
	Scope         string `json:"scope"`
	StorePath     string `json:"store_path"`
	ManifestValid bool   `json:"manifest_valid"`
	ProbeChecked  bool   `json:"probe_checked"`
	// LocalHits 本 store 已有语料条数。
	LocalHits int           `json:"local_hits"`
	Error     string        `json:"error,omitempty"`
	CheckedAt string        `json:"checked_at,omitempty"`
	Manifest  *EvalManifest `json:"-"`
}

type evalReadinessCacheEntry struct {
	readiness  EvalReadiness
	checkedAt  time.Time
	refreshing bool
}

type evalPrepareKey struct {
	suite     string
	storePath string
}

const (
	evalReadinessCacheTTL      = 10 * time.Minute
	evalReadinessCacheStaleTTL = 2 * time.Hour
)

// evalIngestTimeout 是 Prepare/Rebuild/Verify 的工作上限，而不是 HTTP 请求超时：
// 这些动作会启动 rmem CLI 向隔离评测 scope 写入整份语料（LoCoMo 5,882 / ATM 11,034 条），
// 客户端连接断开后仍继续执行，避免一轮 ingest 被浏览器 10s 超时中途截断。
const evalIngestTimeout = 60 * time.Minute

var (
	evalCLICommand = func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, bin, args...)
		// 子进程零云端覆盖变量：剥离可能从宿主继承的 BRAIN 端点/密钥（评测全程本地明文通道，
		// 只读本地隔离 store；评测 LLM 通道由 answer/judge 的显式 HTTP 客户端承担，不在此子进程）。
		cmd.Env = stripCloudBrainEnv(os.Environ())
		out, err := cmd.Output()
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				out = append(out, exitErr.Stderr...)
			}
		}
		return out, err
	}
	evalReadinessCacheMu sync.RWMutex
	evalReadinessCache   = map[string]evalReadinessCacheEntry{}
	evalPrepareLocksMu   sync.Mutex
	evalPrepareLocks     = map[evalPrepareKey]*sync.Mutex{}
)

func evalScopeForSuite(suite string) string {
	base := EvalScope
	if normalizeSuite(suite) == SuiteATM {
		base = ATMEvalScope
	}
	// 现役唯一评测通道 = 本地明文：scope 恒带 -local-plain 后缀（与 rmem internal/eval 同源）。
	return base + "-local-plain"
}

// evalScopeSuiteModeGeneration 解析评测 scope 的 (suite, mode, generation, ok)：
// 唯一形态 <prefix>-mem-eval-<locomo|atmbench>-local-plain[-<世代号>]。
// 与 rmem（internal/eval 的 SplitEvalScopeWithMode）逐条同源：改一处必须同改另一处（parity 由两侧
// 同名测试表守护）。
func evalScopeSuiteModeGeneration(scope string) (string, string, string, bool) {
	trimmed := strings.ToLower(strings.TrimSpace(scope))
	if trimmed == "" || len(trimmed) > 191 || strings.ContainsAny(trimmed, " \t\r\n:") {
		return "", "", "", false
	}
	index := strings.LastIndex(trimmed, "-mem-eval-")
	if index <= 0 {
		return "", "", "", false
	}
	segments := strings.Split(trimmed[index+len("-mem-eval-"):], "-")
	switch segments[0] {
	case SuiteLoCoMo, "atmbench":
	default:
		return "", "", "", false
	}
	for _, segment := range segments[1:] {
		if segment == "" {
			return "", "", "", false
		}
	}
	suite := SuiteLoCoMo
	if segments[0] == "atmbench" {
		suite = SuiteATM
	}
	genSegments := segments[1:]
	if len(genSegments) < 2 || genSegments[0] != "local" || genSegments[1] != "plain" {
		return "", "", "", false
	}
	return suite, EvalPulseModeLocalPlain, strings.Join(genSegments[2:], "-"), true
}

func evalScopeSuiteName(scope string) (string, bool) {
	suite, _, _, ok := evalScopeSuiteModeGeneration(scope)
	return suite, ok
}

// evalScopeGeneration 取评测 scope 的世代号（历史固定 scope 返回空串；非法 scope 同样返回空串）。
func evalScopeGeneration(scope string) string {
	_, _, gen, ok := evalScopeSuiteModeGeneration(scope)
	if !ok {
		return ""
	}
	return gen
}

// currentEvalScope 返回本 store 当前世代的 scope（manifest 是唯一真源）；
// 尚未 prepare 时退回本地明文硬名的评测 scope（仅作展示/兼容字段，不代表已有语料）。
func currentEvalScope(storePath, suite string) string {
	if manifest, err := readEvalManifest(storePath, suite); err == nil {
		return manifest.Scope
	}
	return evalScopeForSuite(suite)
}

// normalizeEvalArm 归一化评测臂：只支持本地明文（sdk-local-plain，默认）与 oracle 金标上限诊断臂。
// 其余取值（含历史脉冲臂）一律显式拒收。
func normalizeEvalArm(armMode string) (string, error) {
	arm := strings.ToLower(strings.TrimSpace(armMode))
	switch arm {
	case "", EvalArmSDKLocalPlain:
		return EvalArmSDKLocalPlain, nil
	case EvalArmOracle:
		return EvalArmOracle, nil
	default:
		return "", fmt.Errorf("评测臂 %q 非法；仅支持 sdk-local-plain（本地明文，默认）或 oracle", armMode)
	}
}

func validateEvalRunRequest(req evalRunRequest) (string, error) {
	rawArm := req.ArmMode
	if strings.TrimSpace(req.Profile) != "" || strings.TrimSpace(req.NoeticMode) != "" {
		return "", errors.New("profile/noetic_mode 为旧版字段，当前版本不再接受；请移除后重试")
	}
	// 套件/档位 fail-closed：未知取值显式拒收，绝不静默回退默认套件档位
	// （静默回退会真的跑出一场默认档评测并落进台账）。
	switch strings.ToLower(strings.TrimSpace(req.Suite)) {
	case "", "locomo", "atm", "atmbench", "atm-bench":
	default:
		return "", fmt.Errorf("未知评测套件 %q：仅支持 locomo 与 atm", req.Suite)
	}
	// 官方题集口径守卫：ATM-Bench 只有 Main-1013 与 Hard-31 两档，
	// 自造的 100 题前缀档已于 2026-11-09 废止，显式拒收而非静默降级为 hard。
	if normalizeSuite(req.Suite) == SuiteATM {
		switch strings.ToLower(strings.TrimSpace(req.Tier)) {
		case "", "hard", "1013", "full", "main":
		case "100", "main-100", "main100", "atm-100", "atm100":
			return "", ErrATMTierRetiredAdmin
		default:
			return "", fmt.Errorf("未知 ATM 档位 %q：仅支持 hard 与 1013", req.Tier)
		}
	} else {
		// LoCoMo 便捷档 100/200 已停用（题集以上游官方为准）：显式拒收，
		// 与 internal/eval 的 CheckLoCoMoTier 同口径。
		switch strings.ToLower(strings.TrimSpace(req.Tier)) {
		case "", "1540", "adversarial-446":
		case "100", "200":
			return "", fmt.Errorf("LoCoMo 便捷档 %q 不再提供：题集以上游官方为准（cat1-4 全集），请使用 1540", req.Tier)
		default:
			return "", fmt.Errorf("未知 LoCoMo 档位 %q：仅支持 1540 与 adversarial-446", req.Tier)
		}
	}
	// 题集/档位契约校验优先；随后才是评测臂闸（现役只开放本地明文臂）。
	return normalizeEvalArm(rawArm)
}

// resolveEvalDBPathForSuite 返回套件专属的本地明文评测 JSON store（跨版本路径保持不变）。
func resolveEvalDBPathForSuite(home, suite string) string {
	base := "eval-locomo"
	if normalizeSuite(suite) == SuiteATM {
		base = "eval-atm"
	}
	return filepath.Join(home, ".local", "state", "rolling-memory", "eval", base+"-local-plain.json")
}

// evalEndpointMatchesMode 判定评测端点是否为本地金库标识（现役唯一形态 local://...）。
func evalEndpointMatchesMode(endpoint, _ string) bool {
	return strings.HasPrefix(strings.TrimSpace(endpoint), "local://")
}

// readEvalManifest 读取并校验本 store 的语料回执：只接受本地明文通道的 manifest
// （transport / endpoint / pulse_mode 三处都必须指向本地金库，历史脉冲通道 fail-closed 拒绝）。
func readEvalManifest(storePath, suite string) (EvalManifest, error) {
	var manifest EvalManifest
	raw, err := os.ReadFile(storePath + ".manifest.json")
	if err != nil {
		return manifest, err
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return manifest, fmt.Errorf("解析评测 manifest 失败: %w", err)
	}
	expectedSuite := normalizeSuite(suite)
	if manifest.Transport != evalLocalPlainTransport {
		return manifest, fmt.Errorf("manifest.transport 必须为 %s（现役唯一评测通道 = 本地明文）", evalLocalPlainTransport)
	}
	if manifest.PulseMode != EvalPulseModeLocalPlain {
		return manifest, fmt.Errorf("manifest.pulse_mode=%q 必须为 %s", manifest.PulseMode, EvalPulseModeLocalPlain)
	}
	if manifest.Suite != expectedSuite {
		return manifest, fmt.Errorf("manifest.suite=%q 与请求 suite=%q 不符", manifest.Suite, expectedSuite)
	}
	// scope 必须同时匹配套件与本地明文通道；世代号不限具体取值。
	scopeSuite, scopeMode, _, ok := evalScopeSuiteModeGeneration(manifest.Scope)
	if !ok || scopeSuite != expectedSuite || scopeMode != EvalPulseModeLocalPlain {
		return manifest, fmt.Errorf("manifest.scope=%q 与套件 %s 或本地明文通道不符", manifest.Scope, expectedSuite)
	}
	if manifest.LocalFallback == nil || *manifest.LocalFallback {
		return manifest, errors.New("manifest.local_fallback 必须显式为 false")
	}
	if !evalEndpointMatchesMode(manifest.Endpoint, EvalPulseModeLocalPlain) {
		return manifest, errors.New("manifest.endpoint 必须是本地金库标识（local://...）")
	}
	// tenant_id / user_id 是本地评测隔离身份（与 scope 前缀 u10 同源），不代表任何云端账号。
	if manifest.TenantID != "user" || manifest.UserID != "10" || manifest.Documents <= 0 {
		return manifest, errors.New("manifest 必须匹配 tenant_id=user、user_id=10 且 documents 必须为正数")
	}
	if len(manifest.CorpusSHA256) != 64 {
		return manifest, errors.New("manifest.corpus_sha256 必须为 64 位十六进制")
	}
	if _, err := hex.DecodeString(manifest.CorpusSHA256); err != nil {
		return manifest, fmt.Errorf("manifest.corpus_sha256 非法: %w", err)
	}
	if manifest.CorpusSHA256 == "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		return manifest, errors.New("manifest.corpus_sha256 不得为空内容哈希")
	}
	return manifest, nil
}

// validateLocalRecallMeta 校验严格 recall 的 meta 块：传输 / 脉冲模式 / scope / 端点 / 隔离身份
// 必须与本地明文通道一致（历史脉冲通道的 meta 一律 fail-closed 拒绝）。
func validateLocalRecallMeta(recallRaw map[string]any, expectedScope string) error {
	metaValue, ok := recallRaw["meta"]
	if !ok {
		return errors.New("recall.meta 缺失")
	}
	meta, ok := metaValue.(map[string]any)
	if !ok {
		return errors.New("recall.meta 必须是对象")
	}
	if asString(meta["engine_transport"]) != evalLocalPlainTransport {
		return fmt.Errorf("recall.meta.engine_transport 必须为 %s", evalLocalPlainTransport)
	}
	if mode, ok := meta["pulse_mode"].(string); !ok || mode != EvalPulseModeLocalPlain {
		return fmt.Errorf("recall.meta.pulse_mode 必须为 %s", EvalPulseModeLocalPlain)
	}
	if asString(meta["engine_scope"]) != expectedScope {
		return fmt.Errorf("recall.meta.engine_scope=%q 与专属 scope=%q 不符", asString(meta["engine_scope"]), expectedScope)
	}
	if !evalEndpointMatchesMode(asString(meta["engine_upstream"]), EvalPulseModeLocalPlain) {
		return errors.New("recall.meta.engine_upstream 必须是本地金库标识（local://...）")
	}
	if asString(meta["tenant_id"]) != "user" {
		return errors.New("recall.meta.tenant_id 必须为 user")
	}
	if asString(meta["user_id"]) != "10" {
		return errors.New("recall.meta.user_id 必须为 10")
	}
	fallback, exists := meta["local_fallback"]
	if !exists {
		return errors.New("recall.meta.local_fallback 缺失")
	}
	fallbackBool, ok := fallback.(bool)
	if !ok || fallbackBool {
		return errors.New("recall.meta.local_fallback 必须为 bool false")
	}
	return nil
}

// validateEvalRawHits 校验 raw_hits 与命中计数（键名 local_hits，
// 现值即本次本地检索命中数），并返回去重后的 UnitID 列表。
func validateEvalRawHits(recallRaw map[string]any, suite string, atmUnits map[string]atmEvalUnit) ([]string, error) {
	rawValue, ok := recallRaw["raw_hits"]
	if !ok {
		return nil, errors.New("recall.raw_hits 缺失")
	}
	rawHits, ok := rawValue.([]any)
	if !ok {
		return nil, errors.New("recall.raw_hits 必须是数组")
	}
	meta, _ := recallRaw["meta"].(map[string]any)
	hitCountValue, ok := meta["local_hits"]
	if !ok {
		return nil, errors.New("recall.meta.local_hits 缺失")
	}
	switch hitCountValue.(type) {
	case float64, int, int64:
	default:
		return nil, errors.New("recall.meta.local_hits 必须是数字")
	}
	if hitCount := asInt(hitCountValue); hitCount != len(rawHits) {
		return nil, fmt.Errorf("recall.meta.local_hits=%d 与 raw_hits=%d 不一致", hitCount, len(rawHits))
	}
	unitIDs := make([]string, 0, len(rawHits))
	seen := make(map[string]struct{}, len(rawHits))
	for index, rawHit := range rawHits {
		hit, ok := rawHit.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("recall.raw_hits[%d] 必须是对象", index)
		}
		unitID := strings.TrimSpace(asString(hit["unit_id"]))
		sessionID := strings.TrimSpace(asString(hit["session"]))
		traceID := strings.TrimSpace(asString(hit["trace"]))
		if unitID == "" || sessionID != unitID || traceID != unitID {
			return nil, fmt.Errorf("recall.raw_hits[%d] 的 unit_id/session/trace 必须同为非空 UnitID", index)
		}
		if _, exists := seen[unitID]; exists {
			return nil, fmt.Errorf("评测引擎返回重复 UnitID %q", unitID)
		}
		seen[unitID] = struct{}{}
		if normalizeSuite(suite) == SuiteATM {
			if atmUnits == nil {
				return nil, errors.New("ATM UnitID 映射未加载")
			}
			if _, exists := atmUnits[unitID]; !exists {
				return nil, fmt.Errorf("评测引擎返回未知 ATM UnitID %q", unitID)
			}
		}
		unitIDs = append(unitIDs, unitID)
	}
	return unitIDs, nil
}

// runLocalEvalRecall 走 rmem 的本地明文严格 recall（本地隔离 store + 离线双簇检索，零云端请求）。
// scope 一律取自本 store 的 manifest 世代（每次新评测自动分配独立 scope），不再用固定 scope 常量。
func runLocalEvalRecall(ctx context.Context, bin, storePath, suite, query string, topK int, atmUnits map[string]atmEvalUnit) (map[string]any, []string, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil, errors.New("本地评测 recall 需要非空查询")
	}
	if topK <= 0 {
		topK = 96
		if normalizeSuite(suite) == SuiteATM {
			topK = 50
		}
	}
	manifest, err := readEvalManifest(storePath, suite)
	if err != nil {
		return nil, nil, fmt.Errorf("评测语料未 prepare（无法确定本 store 的评测世代 scope）: %w", err)
	}
	scope := manifest.Scope
	// 热腿优先：批次 worker 注入的常驻 `recall --stdin-batch` 会话免去逐题进程冷启；
	// 会话损坏（管道断/序号错位）回退一次性 fork 路径重试同一查询，两腿响应结构同构。
	if warm := warmEvalRecallSessionFrom(ctx); warm != nil {
		if recallRaw, warmErr := warm.recall(query, topK); warmErr == nil {
			if err := validateLocalRecallMeta(recallRaw, scope); err != nil {
				return nil, nil, err
			}
			unitIDs, err := validateEvalRawHits(recallRaw, suite, atmUnits)
			if err != nil {
				return nil, nil, err
			}
			return recallRaw, unitIDs, nil
		}
	}
	args := []string{
		"recall", "--db", storePath, "--scope", scope,
		"--raw-limit", strconv.Itoa(topK), "--json", "--q", query,
	}
	var out []byte
	var err2 error
	for attempt := 1; attempt <= 3; attempt++ {
		out, err2 = evalCLICommand(ctx, bin, args...)
		if err2 == nil || ctx.Err() != nil {
			break
		}
		errMsg := err2.Error() + " " + string(out)
		if !strings.Contains(errMsg, "signal: terminated") &&
			!strings.Contains(errMsg, "signal: killed") &&
			!strings.Contains(errMsg, "connection reset") {
			break
		}
		time.Sleep(time.Duration(attempt*250) * time.Millisecond)
	}
	if err2 != nil {
		detail := strings.TrimSpace(string(out))
		if detail != "" {
			return nil, nil, fmt.Errorf("本地 recall 失败: %w: %s", err2, detail)
		}
		return nil, nil, fmt.Errorf("本地 recall 失败: %w", err2)
	}
	var recallRaw map[string]any
	if err := json.Unmarshal(out, &recallRaw); err != nil {
		return nil, nil, fmt.Errorf("解析 recall JSON 失败: %w", err)
	}
	if err := validateLocalRecallMeta(recallRaw, scope); err != nil {
		return nil, nil, err
	}
	unitIDs, err := validateEvalRawHits(recallRaw, suite, atmUnits)
	if err != nil {
		return nil, nil, err
	}
	return recallRaw, unitIDs, nil
}

func readinessForManifest(manifest EvalManifest, storePath string) EvalReadiness {
	return EvalReadiness{
		Suite: manifest.Suite, Scope: manifest.Scope, StorePath: storePath,
		ManifestValid: true, Manifest: &manifest,
	}
}

func cacheEvalReadiness(readiness EvalReadiness) {
	if readiness.Manifest == nil {
		return
	}
	evalReadinessCacheMu.Lock()
	evalReadinessCache[readiness.StorePath] = evalReadinessCacheEntry{readiness: readiness, checkedAt: time.Now()}
	evalReadinessCacheMu.Unlock()
}

// evalVerifyFailure 保留 rmem CLI 侧的结构化验证失效：message 是信封里的语义明细
// （而不是裸 "exit status 1"），code 是稳定错误码，供 prepare 分类是否进入自愈重灌。
type evalVerifyFailure struct {
	code    string
	message string
	err     error
}

func (e *evalVerifyFailure) Error() string { return e.message }

func (e *evalVerifyFailure) Unwrap() error { return e.err }

// decodeEvalCLIJSON 解析评测子进程的 JSON 载荷。评测命令会在 stdout 先写
// `[eval] arm=...` 之类的诊断行，再输出最终 JSON；直接对整段输出调用
// json.Unmarshal 会把失败信封（尤其是稳定 error_code）一起丢掉，导致
// manifest_corpus_mismatch 无法进入自愈重灌路径。这里允许前缀行与多行 JSON。
func decodeEvalCLIJSON(out []byte, v any) error {
	trimmed := bytes.TrimSpace(out)
	if len(trimmed) == 0 {
		return errors.New("CLI output is empty")
	}
	if err := json.Unmarshal(trimmed, v); err == nil {
		return nil
	}
	// 常见形态：诊断行在前，单行 JSON 在最后一行。
	lines := bytes.Split(trimmed, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		if err := json.Unmarshal(line, v); err == nil {
			return nil
		}
	}
	// 兼容带前缀的 pretty-print JSON：从每个可能的对象起点尝试到输出末尾。
	for idx := bytes.IndexByte(trimmed, '{'); idx >= 0; {
		if err := json.Unmarshal(trimmed[idx:], v); err == nil {
			return nil
		}
		next := bytes.IndexByte(trimmed[idx+1:], '{')
		if next < 0 {
			break
		}
		idx += next + 1
	}
	return fmt.Errorf("CLI output is not a JSON object: %s", clipRunes(string(trimmed), 240))
}

// parseEvalCLIVerifyFailure 解开 rmem 的 JSON 失败信封 {ok:false, error, error_code}；
// 非 JSON 输出（进程被杀、stderr 独有）退化为 exec 错误 + 原始输出明细，绝不丢诊断信息。
func parseEvalCLIVerifyFailure(out []byte, execErr error) *evalVerifyFailure {
	failure := &evalVerifyFailure{err: execErr}
	var envelope map[string]any
	if decodeEvalCLIJSON(out, &envelope) == nil {
		failure.code = strings.TrimSpace(asString(envelope["error_code"]))
		failure.message = strings.TrimSpace(asString(envelope["error"]))
	}
	if failure.message == "" {
		if detail := strings.TrimSpace(string(out)); detail != "" {
			failure.message = execErr.Error() + ": " + detail
		} else {
			failure.message = execErr.Error()
		}
	}
	return failure
}

func probeLocalEvalStore(ctx context.Context, bin, storePath, suite, query string, atmUnits map[string]atmEvalUnit) (EvalReadiness, error) {
	_ = query
	_ = atmUnits
	manifest, err := readEvalManifest(storePath, suite)
	if err != nil {
		return EvalReadiness{
			Suite: normalizeSuite(suite), Scope: evalScopeForSuite(suite), StorePath: storePath,
			Error: err.Error(), CheckedAt: time.Now().Format(time.RFC3339),
		}, err
	}
	readiness := readinessForManifest(manifest, storePath)
	home, _ := os.UserHomeDir()
	dataDir := resolveEvalDataDir(home)
	tier := "200"
	if normalizeSuite(suite) == SuiteATM {
		dataDir = resolveATMDataDir(home, "")
		tier = "hard"
	}
	out, err := evalCLICommand(ctx, bin, buildEvalVerifyArgs(suite, tier, storePath, dataDir)...)
	readiness.ProbeChecked = true
	readiness.CheckedAt = time.Now().Format(time.RFC3339)
	if err != nil {
		// 裸 "exit status 1" 无诊断价值：解开 CLI 的 JSON 失败信封，保留语义明细与稳定错误码。
		failure := parseEvalCLIVerifyFailure(out, err)
		readiness.Error = failure.Error()
		cacheEvalReadiness(readiness)
		return readiness, failure
	}
	var verification map[string]any
	if err := decodeEvalCLIJSON(out, &verification); err != nil {
		readiness.Error = err.Error()
		cacheEvalReadiness(readiness)
		return readiness, fmt.Errorf("解析 verify-prepare 回执失败: %w", err)
	}
	if err := validateEvalPrepareVerification(verification, readiness.Scope); err != nil {
		readiness.Error = err.Error()
		cacheEvalReadiness(readiness)
		return readiness, err
	}
	engine, _ := verification["engine"].(map[string]any)
	if asString(engine["endpoint"]) != manifest.Endpoint {
		err := errors.New("verify-prepare endpoint 与 manifest 不匹配")
		readiness.Error = err.Error()
		cacheEvalReadiness(readiness)
		return readiness, err
	}
	readiness.Ready = true
	readiness.LocalHits = manifest.Documents
	cacheEvalReadiness(readiness)
	return readiness, nil
}

func inspectLocalEvalReadiness(ctx context.Context, bin, storePath, suite, query string, atmUnits map[string]atmEvalUnit, force bool) (EvalReadiness, error) {
	manifest, err := readEvalManifest(storePath, suite)
	if err != nil {
		return EvalReadiness{
			Suite: normalizeSuite(suite), Scope: evalScopeForSuite(suite), StorePath: storePath,
			Error: err.Error(), CheckedAt: time.Now().Format(time.RFC3339),
		}, err
	}
	if !force {
		evalReadinessCacheMu.RLock()
		cached, ok := evalReadinessCache[storePath]
		evalReadinessCacheMu.RUnlock()
		if ok && cached.readiness.Manifest != nil &&
			cached.readiness.Suite == manifest.Suite && cached.readiness.Scope == manifest.Scope &&
			cached.readiness.Manifest.Transport == manifest.Transport &&
			cached.readiness.Manifest.PulseMode == manifest.PulseMode &&
			cached.readiness.Manifest.CorpusSHA256 == manifest.CorpusSHA256 &&
			cached.readiness.Manifest.Endpoint == manifest.Endpoint &&
			cached.readiness.Manifest.TenantID == manifest.TenantID &&
			cached.readiness.Manifest.UserID == manifest.UserID &&
			cached.readiness.Manifest.Documents == manifest.Documents {
			age := time.Since(cached.checkedAt)
			if age < evalReadinessCacheTTL {
				readiness := cached.readiness
				readiness.Manifest = &manifest
				if readiness.Error != "" {
					return readiness, errors.New(readiness.Error)
				}
				return readiness, nil
			}
			if age < evalReadinessCacheStaleTTL {
				if !cached.refreshing {
					evalReadinessCacheMu.Lock()
					if cur, exists := evalReadinessCache[storePath]; exists && !cur.refreshing {
						cur.refreshing = true
						evalReadinessCache[storePath] = cur
						go func(b, sp, su, q string, units map[string]atmEvalUnit) {
							defer func() {
								evalReadinessCacheMu.Lock()
								if entry, ok := evalReadinessCache[sp]; ok && entry.refreshing {
									entry.refreshing = false
									evalReadinessCache[sp] = entry
								}
								evalReadinessCacheMu.Unlock()
							}()
							bgCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
							defer cancel()
							_, _ = probeLocalEvalStore(bgCtx, b, sp, su, q, units)
						}(bin, storePath, suite, query, atmUnits)
					}
					evalReadinessCacheMu.Unlock()
				}
				readiness := cached.readiness
				readiness.Manifest = &manifest
				if readiness.Error != "" {
					return readiness, errors.New(readiness.Error)
				}
				return readiness, nil
			}
		}
	}
	return probeLocalEvalStore(ctx, bin, storePath, suite, query, atmUnits)
}

func evalPreparationMutex(suite, storePath string) *sync.Mutex {
	key := evalPrepareKey{suite: normalizeSuite(suite), storePath: storePath}
	evalPrepareLocksMu.Lock()
	defer evalPrepareLocksMu.Unlock()
	mu := evalPrepareLocks[key]
	if mu == nil {
		mu = &sync.Mutex{}
		evalPrepareLocks[key] = mu
	}
	return mu
}

func buildEvalPrepareArgs(suite, tier, storePath, dataDir string, rebuild bool) []string {
	suite = normalizeSuite(suite)
	if suite == SuiteATM {
		tier = normalizeATMTier(tier)
	} else {
		tier = normalizeTier(tier)
	}
	args := []string{"eval", "--eval-store", storePath, "--eval-data", dataDir}
	if suite == SuiteATM {
		args = append(args, "--suite", "atm")
	}
	args = append(args,
		"--tier", tier, "--limit", "1", "--answerer", "none", "--judge", "off", "--json",
	)
	if rebuild {
		args = append(args, "--rebuild")
	}
	return args
}

func buildEvalVerifyArgs(suite, tier, storePath, dataDir string) []string {
	args := buildEvalPrepareArgs(suite, tier, storePath, dataDir, false)
	return append(args, "--verify-prepare")
}

func validateEvalPrepareVerification(payload map[string]any, expectedScope string) error {
	if verified, ok := payload["verified"].(bool); !ok || !verified {
		return errors.New("verify-prepare 未返回 verified=true")
	}
	if asString(payload["transport"]) != evalLocalPlainTransport {
		return fmt.Errorf("verify-prepare transport 必须为 %s（现役唯一评测通道 = 本地明文）", evalLocalPlainTransport)
	}
	engine, _ := payload["engine"].(map[string]any)
	actualScope := asString(payload["scope"])
	if engScope := asString(engine["scope"]); engScope != "" {
		actualScope = engScope
	}
	if actualScope != expectedScope {
		return errors.New("verify-prepare scope 不匹配")
	}
	if fallback, ok := payload["local_fallback"].(bool); !ok || fallback {
		return errors.New("verify-prepare.local_fallback 必须显式为 false")
	}
	if mode, ok := engine["pulse_mode"].(string); !ok || mode != EvalPulseModeLocalPlain {
		return fmt.Errorf("verify-prepare.engine.pulse_mode 必须为 %s", EvalPulseModeLocalPlain)
	}
	if asString(engine["tenant_id"]) != "user" || asString(engine["user_id"]) != "10" {
		return errors.New("verify-prepare tenant/user 不匹配")
	}
	if !evalEndpointMatchesMode(asString(engine["endpoint"]), EvalPulseModeLocalPlain) {
		return errors.New("verify-prepare endpoint 必须是本地金库标识（local://...）")
	}
	return nil
}

// evalVerifyFailureHealsByReingest 判定 verify 失效是否属于「本 store 的既有 manifest
// 已不能支持复用，但按现役装配原地重灌即可自愈」的确定性失效：
//   - manifest_corpus_mismatch：manifest 摘要/scope/条数与当前语料不符；
//   - manifest_identity_mismatch：manifest 的端点/身份/切词臂与现役客户端不符
//     （旧版本写下的库）。
//
// 其余失效（脉冲模式契约变更、健康检查失败、金库不可读、进程被杀）保持 fail-closed，
// 绝不盲目重灌。
func evalVerifyFailureHealsByReingest(code string) bool {
	switch code {
	case evalVerifyCodeManifestMismatch, evalVerifyCodeManifestIdentityMismatch:
		return true
	}
	return false
}

func prepareLocalEvalStore(ctx context.Context, bin, home, suite, tier, storePath, dataDir, probeQuery string, atmUnits map[string]atmEvalUnit, rebuild bool) (EvalReadiness, error) {
	mu := evalPreparationMutex(suite, storePath)
	mu.Lock()
	defer mu.Unlock()

	if !rebuild {
		readiness, err := probeLocalEvalStore(ctx, bin, storePath, suite, probeQuery, atmUnits)
		if err == nil && readiness.Ready {
			return readiness, nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			// 确定性失配（语料口径或 manifest 身份已过期）：复用已不可能，
			// CLI 的 Prepare 走 repair + 断点续灌自愈重灌，继续本动作而非卡死。
			var verifyErr *evalVerifyFailure
			if !errors.As(err, &verifyErr) || !evalVerifyFailureHealsByReingest(verifyErr.code) {
				return readiness, fmt.Errorf("本地评测 prepare 失败，未触发重灌: %w", err)
			}
		}
	}

	if suite == SuiteATM {
		dataDir = resolveATMDataDir(home, dataDir)
	}
	var out []byte
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		attemptRebuild := rebuild && attempt == 1
		args := buildEvalPrepareArgs(suite, tier, storePath, dataDir, attemptRebuild)
		out, err = evalCLICommand(ctx, bin, args...)
		if err == nil || ctx.Err() != nil {
			break
		}
		errMsg := err.Error() + " " + string(out)
		if !strings.Contains(errMsg, "signal: terminated") && !strings.Contains(errMsg, "signal: killed") {
			break
		}
		time.Sleep(time.Duration(attempt*500) * time.Millisecond)
	}
	if err != nil {
		detail := strings.TrimSpace(string(out))
		return EvalReadiness{Suite: normalizeSuite(suite), Scope: evalScopeForSuite(suite), StorePath: storePath, Error: err.Error()},
			fmt.Errorf("rmem eval prepare 失败: %w: %s", err, detail)
	}
	readiness, err := probeLocalEvalStore(ctx, bin, storePath, suite, probeQuery, atmUnits)
	if err != nil {
		return readiness, fmt.Errorf("prepare 后严格探针失败: %w", err)
	}
	if !readiness.Ready {
		return readiness, errors.New("prepare 后严格探针仍未返回本地评测语料")
	}
	return readiness, nil
}

func requireEvalManifestForSingle(storePath, suite string) (EvalManifest, error) {
	manifest, err := readEvalManifest(storePath, suite)
	if err != nil {
		return manifest, fmt.Errorf("评测语料未 prepare，single 模式不会自动 ingest: %w", err)
	}
	return manifest, nil
}

// normalizeTier 归一到当前提供的题集档位：LoCoMo 以上游官方为准（cat1-4 全集 = 1540）；
// 不再提供的便捷档 100/200 归一到 1540（归档历史行只读展示，不按档位码回放）。
func normalizeTier(tier string) string {
	switch strings.TrimSpace(tier) {
	case "1540":
		return "1540"
	default:
		return "1540"
	}
}

func loadTierQuestions(dataDir, tier string) ([]EvalQuestionItem, error) {
	tier = normalizeTier(tier)
	path := filepath.Join(dataDir, "tiers", "questions-"+tier+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Questions []map[string]any `json:"questions"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	items := make([]EvalQuestionItem, 0, len(doc.Questions))
	for _, q := range doc.Questions {
		evList := []string{}
		if rawEv, ok := q["evidence_texts"].([]any); ok {
			for _, e := range rawEv {
				if s, isStr := e.(string); isStr && s != "" {
					evList = append(evList, s)
				}
			}
		}
		items = append(items, EvalQuestionItem{
			ID:            asString(q["id"]),
			SampleID:      asString(q["sample_id"]),
			Category:      asString(q["category"]),
			Question:      asString(q["question"]),
			Answer:        asString(q["answer"]),
			EvidenceTexts: evList,
		})
	}
	return items, nil
}

func snapshotJobStatus() EvalJobStatus {
	evalJobMu.RLock()
	defer evalJobMu.RUnlock()
	cp := evalJobState
	if len(evalJobState.RecentItems) > 0 {
		cp.RecentItems = make([]EvalQuestionResultItem, len(evalJobState.RecentItems))
		copy(cp.RecentItems, evalJobState.RecentItems)
	} else {
		cp.RecentItems = []EvalQuestionResultItem{}
	}
	if len(evalJobState.Logs) > 0 {
		cp.Logs = make([]string, len(evalJobState.Logs))
		copy(cp.Logs, evalJobState.Logs)
	} else {
		cp.Logs = []string{}
	}
	return cp
}

func evalOverviewHandler(c *gin.Context) {
	var req EvalOverviewRequest
	_ = c.ShouldBindJSON(&req)
	suite := normalizeSuite(req.Suite)
	if suite == SuiteATM {
		req.Tier = normalizeATMTier(req.Tier)
	} else {
		req.Tier = normalizeTier(req.Tier)
	}
	if req.Page <= 0 {
		req.Page = 1
	}
	if req.PageSize <= 0 || req.PageSize > 100 {
		req.PageSize = 20
	}

	home, _ := os.UserHomeDir()
	storePath := resolveEvalDBPathForSuite(home, suite)
	dataDir := resolveEvalDataDir(home)
	scope := currentEvalScope(storePath, suite)
	userCounts := ScopeTableCounts{}
	evalCounts := ScopeTableCounts{Scope: scope}
	aiCfg := loadAIConfig(home)

	var allQuestions []EvalQuestionItem
	var err error
	questionError := ""
	if suite == SuiteATM && req.Tier == "" {
		questionError = ErrATMTierRetiredAdmin.Error()
	} else {
		if suite == SuiteATM {
			allQuestions, err = loadATMTierQuestions(dataDir, home, req.Tier)
		} else {
			allQuestions, err = loadTierQuestions(dataDir, req.Tier)
		}
		if err != nil {
			// 题集异常只阻断题目与运行，不让整个工作台失去历史、配置和恢复入口。
			questionError = err.Error()
			allQuestions = []EvalQuestionItem{}
		}
	}

	catFilter := strings.TrimSpace(req.Category)
	kwFilter := strings.ToLower(strings.TrimSpace(req.Keyword))
	filtered := make([]EvalQuestionItem, 0, len(allQuestions))
	for _, q := range allQuestions {
		if catFilter != "" && q.Category != catFilter {
			continue
		}
		if kwFilter != "" {
			if !strings.Contains(strings.ToLower(q.Question), kwFilter) &&
				!strings.Contains(strings.ToLower(q.Answer), kwFilter) &&
				!strings.Contains(strings.ToLower(q.ID), kwFilter) {
				continue
			}
		}
		filtered = append(filtered, q)
	}

	total := len(filtered)
	start := (req.Page - 1) * req.PageSize
	if start > total {
		start = total
	}
	end := start + req.PageSize
	if end > total {
		end = total
	}

	corpusRows := 5882
	agents := buildOfficialAgents(aiCfg)
	protoSpec := buildProtocolParitySpec()
	atmPins := ATMOfficialPinStatus{}
	baselines := referenceBaselines()
	filteredHistory := loadRunHistoryBySuite(suite)
	if suite == SuiteATM {
		corpusRows = 11034
		agents = buildATMOfficialAgents(aiCfg)
		protoSpec = buildATMProtocolParitySpec()
		atmDir := resolveATMDataDir(home, dataDir)
		atmPins = atmOfficialPinSnapshotAdmin(atmDir)
		protoSpec = applyATMVerificationStatus(protoSpec, atmPins, atmDir)
		baselines = atmReferenceBaselines()
	}

	readiness := EvalReadiness{Suite: suite, Scope: scope, StorePath: storePath}
	probeQuery := "memory"
	if len(allQuestions) > 0 {
		probeQuery = allQuestions[0].Question
	}
	manifest, manifestErr := readEvalManifest(storePath, suite)
	if manifestErr != nil {
		readiness.Error = manifestErr.Error()
	} else {
		readiness = readinessForManifest(manifest, storePath)
	}
	var atmUnits map[string]atmEvalUnit
	if manifestErr == nil && suite == SuiteATM {
		atmUnits, err = loadATMEvalUnits(home, dataDir)
		if err != nil {
			readiness.Error = "加载 ATM UnitID 映射失败: " + err.Error()
		}
	}
	if manifestErr == nil && readiness.Error == "" {
		bin := resolveBinary(home)
		if bin == "" {
			readiness.Error = "未找到 rolling-memory 引擎二进制"
		} else {
			readiness, _ = inspectLocalEvalReadiness(c.Request.Context(), bin, storePath, suite, probeQuery, atmUnits, false)
		}
	}
	if readiness.Manifest != nil {
		corpusRows = readiness.Manifest.Documents
	}

	httpx.Success(c, httpx.WithData(EvalOverviewResponse{
		Suite:           suite,
		Mode:            modeLocal,
		DBPath:          storePath,
		StorePath:       storePath,
		Readiness:       readiness,
		Manifest:        readiness.Manifest,
		EvalDataDir:     dataDir,
		CorpusTotalRows: corpusRows,
		UserCounts:      userCounts,
		EvalCounts:      evalCounts,
		AIConfig:        maskAIConfig(aiCfg),
		AICheck:         checkAIModelConfigWithPlugin(aiCfg),
		Agents:          agents,
		ProtocolSpec:    protoSpec,
		ATMOfficialPins: atmPins,
		Leaderboard:     locomoBackboardLeaderboard(),
		Baselines:       baselines,
		RunHistory:      filteredHistory,
		ActiveJob:       snapshotJobStatus(),
		Tier:            req.Tier,
		TierHardCount:   31,
		Tier100Count:    100,
		Tier200Count:    200,
		Tier1540Count:   1540,
		Tier1013Count:   1013,
		TotalQuestions:  total,
		Page:            req.Page,
		PageSize:        req.PageSize,
		Questions:       filtered[start:end],
		QuestionError:   questionError,
		// ATM-Hard 金标 QS 上限与 ATM 榜单 Oracle 行同源（ATMOracleHardQSScore），评测页图表不再自持硬编码读数。
		ATMOracleQSCeiling: ATMOracleHardQSScore,
	}))
}

func evalIngestHandler(c *gin.Context) {
	var req evalIngestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Fail(c, httpx.WithMessage("请求参数错误: "+err.Error()))
		return
	}
	action := strings.ToLower(strings.TrimSpace(req.Action))
	// 显式动作契约：空白动作不隐式触发 prepare（写路径绝不隐式执行），一律 fail-closed 拒收。
	if action != "prepare" && action != "verify" && action != "rebuild" {
		httpx.Fail(c, httpx.WithMessage("动作 "+req.Action+" 不受支持；必须显式指定 prepare、verify 或 rebuild（rebuild 会先清空该 scope 的全部旧记忆再全量重灌）"))
		return
	}
	suite := normalizeSuite(req.Suite)
	home, _ := os.UserHomeDir()
	// 现役唯一评测通道 = 本地明文：脉冲臂名一律在任何外部动作之前 fail-closed 拒收。
	if _, armErr := normalizeEvalArm(req.ArmMode); armErr != nil {
		httpx.Fail(c, httpx.WithCode(http.StatusBadRequest), httpx.WithMessage(armErr.Error()))
		return
	}
	storePath := resolveEvalDBPathForSuite(home, suite)
	dataDir := resolveEvalDataDir(home)
	bin := resolveBinary(home)
	if bin == "" {
		httpx.Fail(c, httpx.WithMessage("未找到 rolling-memory 引擎二进制"))
		return
	}

	var atmUnits map[string]atmEvalUnit
	probeQuery := "memory"
	var err error
	if suite == SuiteATM {
		atmUnits, err = loadATMEvalUnits(home, dataDir)
		if err == nil {
			var questions []EvalQuestionItem
			questions, err = loadATMTierQuestions(dataDir, home, "hard")
			if err == nil && len(questions) > 0 {
				probeQuery = questions[0].Question
			}
		}
	} else {
		var questions []EvalQuestionItem
		questions, err = loadTierQuestions(dataDir, "200")
		if err == nil && len(questions) > 0 {
			probeQuery = questions[0].Question
		}
	}
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("加载 prepare 探针失败: "+err.Error()))
		return
	}

	tier := "200"
	if suite == SuiteATM {
		tier = "hard"
	}
	startedAt := time.Now()
	// 语料写入与 HTTP 请求生命周期解耦：Prepare/Rebuild 要向隔离评测 scope 写入 5,882
	// （LoCoMo）或 11,034（ATM）条原文，耗时可达数分钟，远超浏览器/代理的请求超时。
	// 客户端断开（超时、刷新、切页）不得杀死已经开始的 CLI；单飞锁保证并发点击复用同一轮。
	workCtx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), evalIngestTimeout)
	defer cancel()
	var readiness EvalReadiness
	if action == "verify" {
		readiness, err = probeLocalEvalStore(workCtx, bin, storePath, suite, probeQuery, atmUnits)
	} else {
		readiness, err = prepareLocalEvalStore(workCtx, bin, home, suite, tier, storePath, dataDir, probeQuery, atmUnits, action == "rebuild")
	}
	if err != nil {
		// 客户端可能已断开（长时 ingest 常见），错误必须同时进服务端日志，否则只剩一个丢失的响应体。
		log.Printf("[eval] ingest %s suite=%s elapsed=%s failed: %v", action, suite, time.Since(startedAt).Round(time.Millisecond), err)
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	log.Printf("[eval] ingest %s suite=%s elapsed=%s ready=%v documents=%d", action, suite, time.Since(startedAt).Round(time.Millisecond), readiness.Ready, readiness.LocalHits)
	if action == "prepare" || action == "rebuild" {
		recordEvalIngestTelemetry(suite, time.Since(startedAt).Milliseconds())
	}
	httpx.Success(c, httpx.WithData(gin.H{
		"suite":       suite,
		"action":      action,
		"elapsed_ms":  time.Since(startedAt).Milliseconds(),
		"store_path":  storePath,
		"readiness":   readiness,
		"manifest":    readiness.Manifest,
		"user_counts": ScopeTableCounts{},
		"eval_counts": ScopeTableCounts{Scope: readiness.Scope},
	}))
}

// isReferenceBaselineID 判断台账 ID 是否属于内置封印基线（两套件）。
// 基线是静态聚合读数，没有逐题明细；前端据此显示明确空态，而不是假装有合成明细。
func isReferenceBaselineID(runID string) bool {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return false
	}
	for _, b := range referenceBaselines() {
		if b.ID == runID {
			return true
		}
	}
	for _, b := range atmReferenceBaselines() {
		if b.ID == runID {
			return true
		}
	}
	return false
}

// loadRunDetailItems 读取一场运行的逐题明细：真源是测评任务表的逐题明细表；
// 内置封印 ATM 基线行没有表内明细，退回其项目内冻结的可复算产物 JSONL。
// 绝不从金标答案伪造逐题预测（Strict Falsification Guard）。
func loadRunDetailItems(home, dataDir, runID string) []EvalQuestionResultItem {
	_ = dataDir
	if items, err := loadEvalTaskItems(runID); err != nil {
		log.Printf("[eval] 读取逐题明细失败 run=%s: %v", runID, err)
	} else if len(items) > 0 {
		return items
	}
	// Sealed ATM local_plain baseline rows fall back to matching persisted task items when present.
	switch strings.TrimSpace(runID) {
	case "atm-baseline-rm-hard-local-plain":
		if items, err := loadEvalTaskItems("atm-run-20261111-161745"); err == nil && len(items) > 0 {
			return items
		}
	case "atm-baseline-rm-full1013-local-plain":
		if items, err := loadEvalTaskItems("atm-run-20261111-161746"); err == nil && len(items) > 0 {
			return items
		}
	}
	if atmItems := loadATMResultsJSONL(home, runID); len(atmItems) > 0 {
		return atmItems
	}
	// Strict Falsification Guard: never synthesize fake per-question predictions
	// from ground-truth answers when a historical baseline has no persisted JSONL/JSON details.
	return []EvalQuestionResultItem{}
}

// itemForLiveStream 返回用于 WebSocket 实时帧的 item 副本：只剥离大数组 retrieval_unit_ids
// （每帧最多 25 条，全量下发会把实时帧撑大一个量级），provenance 标量字段保留。
// 落盘明细仍保存完整 Unit ID，供 /units 全集复算与原始 JSON 追溯。
func itemForLiveStream(item EvalQuestionResultItem) EvalQuestionResultItem {
	item.RetrievalUnitIDs = nil
	return item
}

var (
	evalManifestDigestMu    sync.RWMutex
	evalManifestDigestCache = map[string]evalManifestDigestEntry{}
)

type evalManifestDigestEntry struct {
	modTime time.Time
	size    int64
	digest  string
}

// manifestCorpusDigest 读取套件 manifest 的语料摘要（按文件 mtime/size 缓存）。
// 只读、缺失即返回空串：provenance 缺该键时前端显示未知，绝不阻断评测。
func manifestCorpusDigest(storePath string) string {
	manifestPath := storePath + ".manifest.json"
	info, err := os.Stat(manifestPath)
	if err != nil {
		return ""
	}
	evalManifestDigestMu.RLock()
	cached, ok := evalManifestDigestCache[manifestPath]
	evalManifestDigestMu.RUnlock()
	if ok && cached.size == info.Size() && cached.modTime.Equal(info.ModTime()) {
		return cached.digest
	}
	var manifest EvalManifest
	raw, err := os.ReadFile(manifestPath)
	if err != nil || json.Unmarshal(raw, &manifest) != nil {
		return ""
	}
	evalManifestDigestMu.Lock()
	evalManifestDigestCache[manifestPath] = evalManifestDigestEntry{
		modTime: info.ModTime(), size: info.Size(), digest: manifest.CorpusSHA256,
	}
	evalManifestDigestMu.Unlock()
	return manifest.CorpusSHA256
}

// applyRecallProvenance 把严格 recall 的 meta / Unit ID / 语料摘要写到逐题明细。
// 这是评测模块自己的证据标签，引擎不参与：引擎只按 scope 返回通用 Unit 集合，
// 不使用也不理解任何评测字段。
func applyRecallProvenance(item *EvalQuestionResultItem, recallRaw map[string]any, unitIDs []string, storePath string) {
	if item == nil {
		return
	}
	if meta, ok := recallRaw["meta"].(map[string]any); ok {
		if v := asString(meta["engine_transport"]); v != "" {
			item.EngineTransport = v
		}
		if v := asString(meta["engine_scope"]); v != "" {
			item.EngineScope = v
		}
		if v := asString(meta["engine_upstream"]); v != "" {
			item.EngineUpstream = v
		}
		if fallback, ok := meta["local_fallback"].(bool); ok {
			item.LocalFallback = &fallback
		}
		// local_hits 即本次本地检索命中数。
		if _, ok := meta["local_hits"]; ok {
			item.LocalHits = asInt(meta["local_hits"])
		}
		if recallMS := asInt(meta["recall_ms"]); recallMS > 0 {
			item.RecallMS = int64(recallMS)
		}
	}
	if len(unitIDs) > 0 {
		item.RetrievalUnitIDs = append([]string(nil), unitIDs...)
	}
	item.CorpusSHA256 = manifestCorpusDigest(storePath)
}

// countDeliveredTokens 计算单题实际交付上下文的 tokenz-r Token 数：
//   - 优先累加 CLI 评测 recall 路径用 tokenz-r 精确算出的 raw_hits[i]["tokens"]（前 deliveredCount 条）；
//   - 若为 Oracle 模式或旧产物无逐条 tokens 字段，则按 tokenz-r 128k 词表同源密度计算。
func countDeliveredTokens(recallRaw map[string]any, deliveredCount int, fallbackText string) int {
	if recallRaw != nil && deliveredCount > 0 {
		if rHits, ok := recallRaw["raw_hits"].([]any); ok && len(rHits) > 0 {
			total := 0
			hasTokens := false
			limit := deliveredCount
			if limit > len(rHits) {
				limit = len(rHits)
			}
			for i := 0; i < limit; i++ {
				if m, ok := rHits[i].(map[string]any); ok {
					if tok := asInt(m["tokens"]); tok > 0 {
						total += tok
						hasTokens = true
					}
				}
			}
			if hasTokens {
				return total
			}
		}
	}
	return estimateTokenzRCount(fallbackText)
}

// estimateTokenzRCount 按 tokenz-r 128k Byte-Level BPE 词表同源预分词规则计算 Token 数（Oracle / 缺省兜底）。
func estimateTokenzRCount(text string) int {
	s := strings.TrimSpace(text)
	if s == "" {
		return 0
	}
	cjk := 0
	ascii := 0
	for _, r := range s {
		if isContinuousSyllabicOrIdeographicAdmin(r) {
			cjk++
		} else {
			ascii++
		}
	}
	tokens := int(float64(cjk)*0.65 + float64(ascii)*0.28 + 0.5)
	if tokens < 1 {
		tokens = 1
	}
	return tokens
}

// computeDeliveryTelemetry 从逐题明细聚合运行级的端到端检索时间（均值 / P95）、交付 Token（均值 / 总计）与交付条数（均值）。
func computeDeliveryTelemetry(items []EvalQuestionResultItem) (avgRecallMS, p95RecallMS float64, avgTokens, totalTokens int, avgItems float64) {
	n := len(items)
	if n == 0 {
		return 0, 0, 0, 0, 0
	}
	recalls := make([]int64, 0, n)
	var sumRecall int64
	sumItems := 0
	for _, it := range items {
		recalls = append(recalls, it.RecallMS)
		sumRecall += it.RecallMS
		delivItems := it.DeliveredItems
		if delivItems <= 0 {
			delivItems = len(it.FormattedItems)
		}
		if delivItems <= 0 {
			delivItems = it.RawDelivered
		}
		sumItems += delivItems
		tok := it.DeliveredTokens
		if tok <= 0 && len(it.FormattedItems) > 0 {
			tok = estimateTokenzRCount(strings.Join(it.FormattedItems, "\n"))
		}
		totalTokens += tok
	}
	sort.Slice(recalls, func(i, j int) bool { return recalls[i] < recalls[j] })
	p95Idx := (95*n + 99) / 100
	if p95Idx <= 0 {
		p95Idx = 1
	}
	if p95Idx > n {
		p95Idx = n
	}
	avgRecallMS = roundFloat(float64(sumRecall)/float64(n), 1)
	p95RecallMS = float64(recalls[p95Idx-1])
	avgTokens = (totalTokens + n/2) / n
	avgItems = roundFloat(float64(sumItems)/float64(n), 1)
	return
}

// markOracleProvenance Oracle 臂只使用金标证据，不属于任何引擎检索成绩；
// 仍显式标注 transport/scope/local_fallback，避免前端把空字段误读成证据缺失。
func markOracleProvenance(item *EvalQuestionResultItem, scope string, hits int, storePath string) {
	if item == nil {
		return
	}
	item.EngineTransport = "oracle-gold"
	item.EngineScope = scope
	item.EngineUpstream = "oracle-gold"
	fallback := false
	item.LocalFallback = &fallback
	item.LocalHits = hits
	item.CorpusSHA256 = manifestCorpusDigest(storePath)
}

func extractAlphaNumTokens(text string) []string {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return nil
	}
	var cur []rune
	var out []string
	flush := func() {
		if len(cur) > 0 {
			out = append(out, string(cur))
			cur = cur[:0]
		}
	}
	for _, r := range lower {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if r > 0x4E00 && r < 0x9FFF {
				flush()
				out = append(out, string(r))
			} else {
				cur = append(cur, r)
			}
		} else {
			flush()
		}
	}
	flush()
	return out
}

// roundFloat 复刻 CLI internal/eval/aggregate.go::PyRound（十进制字符串格式化 = Python
// round 的二进制浮点行为）。此前用 math.Round(v*p)/p，平局值上与 CLI 可差 1e-4，
// 破「双端复算四位全等」判据。
func roundFloat(v float64, digits int) float64 {
	text := strconv.FormatFloat(v, 'f', digits, 64)
	parsed, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return v
	}
	return parsed
}

func computeTokenRecall(needle, haystack string) float64 {
	wanted := map[string]struct{}{}
	for _, t := range extractAlphaNumTokens(needle) {
		wanted[t] = struct{}{}
	}
	if len(wanted) == 0 {
		return 0
	}
	have := map[string]struct{}{}
	for _, t := range extractAlphaNumTokens(haystack) {
		have[t] = struct{}{}
	}
	hit := 0
	for t := range wanted {
		if _, ok := have[t]; ok {
			hit++
		}
	}
	return float64(hit) / float64(len(wanted))
}

// computeTokenF1 复刻 CLI internal/eval/scoring.go::TokenF1 与 ATM/LoCoMo 行内 exact 口径：
// 词元 = strings.Fields(NormalizeAnswer(text))（丢弃 SQuAD 冠词、连续表意/音节文字按单字切分），
// 多重集 F1；exact = 归一化非空且全等（ATM 口径）。
// 此前用 extractAlphaNumTokens（保留冠词、CJK 仅按 0x4E00-0x9FFF 单字切分），与 CLI 同名异义。
func computeTokenF1(prediction, gold string) (float64, bool) {
	predNorm := normalizeAnswerAdmin(prediction)
	truthNorm := normalizeAnswerAdmin(gold)
	exact := predNorm != "" && predNorm == truthNorm
	pred := strings.Fields(predNorm)
	truth := strings.Fields(truthNorm)
	if len(pred) == 0 || len(truth) == 0 {
		return 0, exact
	}
	counts := map[string]int{}
	for _, t := range pred {
		counts[t]++
	}
	overlap := 0
	for _, t := range truth {
		if counts[t] > 0 {
			counts[t]--
			overlap++
		}
	}
	if overlap == 0 {
		return 0, exact
	}
	p := float64(overlap) / float64(len(pred))
	r := float64(overlap) / float64(len(truth))
	return roundFloat(2*p*r/(p+r), 4), exact
}

func evaluateEvidenceRecall(evidenceTexts []string, contextText string) (float64, []EvidenceMatchItem) {
	if len(evidenceTexts) == 0 {
		return 0, nil
	}
	best := 0.0
	matches := make([]EvidenceMatchItem, 0, len(evidenceTexts))
	for _, ev := range evidenceTexts {
		score := roundFloat(computeTokenRecall(ev, contextText), 4)
		if score > best {
			best = score
		}
		matches = append(matches, EvidenceMatchItem{
			Text:        ev,
			TokenRecall: score,
			Hit:         score >= 0.6,
		})
	}
	return best, matches
}

// evaluateRankedEvidenceRecall 官方口径金标覆盖（rank-aware，交付序 1..N）：
// 对每条金标证据取其在交付行序中的首次命中位次（token recall >= 0.6，与窗口命中同阈），
// 返回前 10/20/50 条窗口内的逐题覆盖率（金标交均）与 Any-Hit / All-Hit 二值判定，
// 与 CLI internal/eval/atmbench.go 的 gold-set-mean 口径对齐（对交付序取窗口，不截断交付文本）。
func evaluateRankedEvidenceRecall(evidenceTexts []string, orderedLineTexts []string) (r10, r20, r50 float64, anyHit10, allHit10 bool) {
	n := len(evidenceTexts)
	if n == 0 {
		return 0, 0, 0, false, false
	}
	ranks := make([]int, n)
	remaining := n
	for i, line := range orderedLineTexts {
		rankPos := i + 1
		for ei, ev := range evidenceTexts {
			if ranks[ei] != 0 {
				continue
			}
			if computeTokenRecall(ev, line) >= 0.6 {
				ranks[ei] = rankPos
				remaining--
			}
		}
		if remaining == 0 {
			break
		}
	}
	var h10v, h20v, h50v int
	anyHit10 = false
	allHit10 = true
	for _, rank := range ranks {
		if rank > 0 && rank <= 10 {
			h10v++
			anyHit10 = true
		} else {
			allHit10 = false
		}
		if rank > 0 && rank <= 20 {
			h20v++
		}
		if rank > 0 && rank <= 50 {
			h50v++
		}
	}
	return roundFloat(float64(h10v)/float64(n), 4),
		roundFloat(float64(h20v)/float64(n), 4),
		roundFloat(float64(h50v)/float64(n), 4),
		anyHit10, allHit10
}

// computeOfficialRecallAggregates 聚合逐题官方金标覆盖指标（均值口径，与 CLI gold-set-mean 对齐）：
// r10/r20/r50 为逐题金标交均的运行级均值；anyHit10/allHit10 为前 10 条窗口二值指标的题目占比。
func computeOfficialRecallAggregates(items []EvalQuestionResultItem) (r10, r20, r50, anyHit10, allHit10 float64) {
	n := len(items)
	if n == 0 {
		return
	}
	var s10, s20, s50 float64
	anyCnt, allCnt := 0, 0
	for _, it := range items {
		s10 += it.R10
		s20 += it.R20
		s50 += it.R50
		if it.AnyHit10 {
			anyCnt++
		}
		if it.AllHit10 {
			allCnt++
		}
	}
	return roundFloat(s10/float64(n), 4),
		roundFloat(s20/float64(n), 4),
		roundFloat(s50/float64(n), 4),
		roundFloat(float64(anyCnt)/float64(n), 4),
		roundFloat(float64(allCnt)/float64(n), 4)
}

// evalIngestTelemetry 记录最近一次语料 Prepare/Rebuild 的实测导入耗时（毫秒），
// 供运行记录展示「语料导入耗时」；verify 探针不覆盖该读数（读路径不污染写遥测）。
// 内存 map 只是热缓存，权威落盘在 store 旁的 `<store>.ingest-telemetry.json`：
// admin 进程重启后读数不丢，台账 IngestMS 不再缺位。
var (
	evalIngestTelemetryMu sync.Mutex
	evalIngestTelemetryMS = map[string]int64{}
)

func evalIngestTelemetryPath(suite string) string {
	home, _ := os.UserHomeDir()
	return resolveEvalDBPathForSuite(home, suite) + ".ingest-telemetry.json"
}

// recordEvalIngestTelemetry 记录一次语料导入耗时（键 = suite，写穿至磁盘）。
func recordEvalIngestTelemetry(suite string, ms int64) {
	if ms <= 0 {
		return
	}
	evalIngestTelemetryMu.Lock()
	defer evalIngestTelemetryMu.Unlock()
	evalIngestTelemetryMS[suite] = ms
	payload, err := json.Marshal(map[string]int64{"ingest_ms": ms})
	if err == nil {
		_ = os.WriteFile(evalIngestTelemetryPath(suite), payload, 0o644)
	}
}

// evaluateHead48EvidenceRecall 计算前 48 个唯一词元头尺下的最佳证据召回率（与 CLI internal/eval/scoring.go::TokenRecall
// 经 internal/ids/ids.go::Tokenize 的 48 词元头尺严格对齐，用于后台双轨量尺对照）。
func evaluateHead48EvidenceRecall(evidenceTexts []string, contextText string) float64 {
	if len(evidenceTexts) == 0 {
		return 0
	}
	const headUniqueTokenCap = 48
	have := map[string]struct{}{}
	for _, t := range extractAlphaNumTokens(contextText) {
		if _, exists := have[t]; !exists {
			if len(have) >= headUniqueTokenCap {
				break
			}
			have[t] = struct{}{}
		}
	}
	if len(have) == 0 {
		return 0
	}
	best := 0.0
	for _, ev := range evidenceTexts {
		wanted := map[string]struct{}{}
		for _, t := range extractAlphaNumTokens(ev) {
			if _, exists := wanted[t]; !exists && len(wanted) < headUniqueTokenCap {
				wanted[t] = struct{}{}
			}
		}
		if len(wanted) == 0 {
			continue
		}
		hit := 0
		for t := range wanted {
			if _, ok := have[t]; ok {
				hit++
			}
		}
		score := roundFloat(float64(hit)/float64(len(wanted)), 4)
		if score > best {
			best = score
		}
	}
	return best
}

func completeOpenAIChat(ctx context.Context, baseURL, apiKey, model, systemPrompt, userPrompt string, temp float64, maxTokens, timeoutSec int) (string, int64, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return "", 0, errors.New("接口地址 (Base URL) 未配置")
	}
	endpoint := base
	if !strings.HasSuffix(endpoint, "/chat/completions") {
		endpoint += "/chat/completions"
	}
	if strings.TrimSpace(model) == "" {
		return "", 0, errors.New("模型名称未配置")
	}
	if timeoutSec <= 0 {
		timeoutSec = 60
	}
	payload := map[string]any{
		"model": model,
		"messages": []map[string]any{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
		"temperature": temp,
		"stream":      false,
	}
	if maxTokens > 0 {
		payload["max_tokens"] = maxTokens
	}
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return "", 0, err
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	}
	started := time.Now()
	resp, err := http.DefaultClient.Do(req)
	elapsedMS := time.Since(started).Milliseconds()
	if err != nil {
		return "", elapsedMS, err
	}
	defer resp.Body.Close()
	respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(respBytes))
		if len(msg) > 240 {
			msg = msg[:240] + "..."
		}
		return "", elapsedMS, fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	var parsed map[string]any
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		return "", elapsedMS, fmt.Errorf("解析模型 JSON 响应失败: %v", err)
	}
	choices, _ := parsed["choices"].([]any)
	if len(choices) == 0 {
		return "", elapsedMS, errors.New("模型响应缺少 choices 字段")
	}
	first, _ := choices[0].(map[string]any)
	msgObj, _ := first["message"].(map[string]any)
	// 评测时彻底剥离 reasoning_content（及任何内嵌 <think>...</think> 思考块），绝不把思考草稿回落冒充为答案。
	content := stripEvalReasoningContent(asString(msgObj["content"]))
	if content == "" {
		finish := asString(first["finish_reason"])
		reasoningLen := len([]rune(asString(msgObj["reasoning_content"])))
		return "", elapsedMS, fmt.Errorf("模型返回正文 content 为空（finish_reason=%q, reasoning_chars=%d）", finish, reasoningLen)
	}
	return content, elapsedMS, nil
}

// stripEvalReasoningContent 剥离模型输出中可能内嵌的 <think>...</think> 思考块，只保留纯净最终答案。
func stripEvalReasoningContent(text string) string {
	s := strings.TrimSpace(text)
	for strings.HasPrefix(s, "<think>") {
		end := strings.Index(s, "</think>")
		if end < 0 {
			return ""
		}
		s = strings.TrimSpace(s[end+len("</think>"):])
	}
	return s
}

func formatFrozenAnswerPayload(question string, formattedLines []string) string {
	var b strings.Builder
	if len(formattedLines) == 0 {
		b.WriteString("1. （no memories）")
	} else {
		for idx, line := range formattedLines {
			if idx > 0 {
				b.WriteString("\n")
			}
			b.WriteString(line)
		}
	}
	b.WriteString("\n\n")
	b.WriteString(strings.TrimSpace(question))
	return b.String()
}

func formatFrozenJudgeUser(question, gold, prediction string) string {
	out := strings.ReplaceAll(FrozenJudgeUserTPL, "<question>", strings.TrimSpace(question))
	out = strings.ReplaceAll(out, "<gold_answer>", strings.TrimSpace(gold))
	out = strings.ReplaceAll(out, "<generated_answer>", strings.TrimSpace(prediction))
	return out
}

// parseFrozenJudgeOutput 与 CLI internal/eval/frozen.go::ParseFrozenJudgeVerdict 严格对齐：
// 取首个 `{` 到末个 `}` 的 JSON；label 严格闭集 {CORRECT, WRONG}（大小写敏感）；
// 不做子串回退、不做 label 大小写归一——任何宽松放行都会令面板判分率偏离冻结协议（locomo-frozen-2026-10-11）。
func parseFrozenJudgeOutput(raw string) (string, string, error) {
	text := strings.TrimSpace(raw)
	start := strings.IndexByte(text, '{')
	end := strings.LastIndexByte(text, '}')
	if start < 0 || end <= start {
		return "unparsed", text, fmt.Errorf("JUDGE_UNPARSED")
	}
	var obj struct {
		Reasoning string `json:"reasoning"`
		Label     string `json:"label"`
	}
	if err := json.Unmarshal([]byte(text[start:end+1]), &obj); err != nil {
		return "unparsed", "JSON parse failed: " + err.Error(), fmt.Errorf("JUDGE_UNPARSED: %w", err)
	}
	if obj.Label != "CORRECT" && obj.Label != "WRONG" {
		return "unparsed", strings.TrimSpace(obj.Reasoning), fmt.Errorf("JUDGE_UNPARSED")
	}
	if obj.Label == "CORRECT" {
		return "correct", strings.TrimSpace(obj.Reasoning), nil
	}
	return "wrong", strings.TrimSpace(obj.Reasoning), nil
}

func testAIConnection(ctx context.Context, cfg AIModelConfig) AIConnectionTestResponse {
	chk := checkAIModelConfig(cfg)
	spec := buildProtocolParitySpec()
	resp := AIConnectionTestResponse{
		PinVerified: spec.AllPinsVerified,
		Endpoint:    cfg.BaseURL,
		AnswerModel: cfg.AnswerModel,
		JudgeModel:  cfg.JudgeModel,
		ConfigCheck: chk,
	}
	if !chk.Ready {
		resp.Message = chk.Summary
		resp.MessageEn = chk.SummaryEn
		return resp
	}
	probeQuestion := "What did the traveler buy the last time they went to Hawaii?"
	probeGold := "A shell necklace"
	probeLines := []string{"1. m1 · [2026-05-07] Traveler: I bought a shell necklace when I visited Hawaii last summer."}
	started := time.Now()
	ansOut, ansMS, ansErr := completeOpenAIChat(ctx, cfg.BaseURL, cfg.APIKey, cfg.AnswerModel, FrozenAnswerSystem, formatFrozenAnswerPayload(probeQuestion, probeLines), 0.2, cfg.MaxTokens, cfg.TimeoutSec)
	resp.AnswerMS = ansMS
	if ansErr != nil {
		resp.AnswerError = ansErr.Error()
		resp.TotalMS = time.Since(started).Milliseconds()
		resp.Message = "Answer 智能体调用失败: " + ansErr.Error()
		resp.MessageEn = "Answer Agent call failed: " + ansErr.Error()
		return resp
	}
	resp.AnswerOK = true
	resp.AnswerOutput = ansOut

	jdgOut, jdgMS, jdgErr := completeOpenAIChat(ctx, cfg.BaseURL, cfg.APIKey, cfg.JudgeModel, FrozenJudgeSystem, formatFrozenJudgeUser(probeQuestion, probeGold, ansOut), 0.0, cfg.MaxTokens, cfg.TimeoutSec)
	resp.JudgeMS = jdgMS
	resp.TotalMS = time.Since(started).Milliseconds()
	if jdgErr != nil {
		resp.JudgeError = jdgErr.Error()
		resp.Message = "Judge 判官智能体调用失败: " + jdgErr.Error()
		resp.MessageEn = "Judge Agent call failed: " + jdgErr.Error()
		return resp
	}
	verdict, reason, parseErr := parseFrozenJudgeOutput(jdgOut)
	if parseErr != nil {
		resp.JudgeError = parseErr.Error()
		resp.Message = "Judge 返回格式不符合 JSON 闭集要求: " + parseErr.Error()
		resp.MessageEn = "Judge output did not match strict JSON format: " + parseErr.Error()
		return resp
	}
	resp.JudgeOK = true
	resp.JudgeLabel = strings.ToUpper(verdict)
	resp.JudgeReason = reason
	resp.Success = true
	resp.Message = fmt.Sprintf("双智能体通道握手成功！Answer (%dms) + Judge (%dms, 裁决=%s)", resp.AnswerMS, resp.JudgeMS, resp.JudgeLabel)
	resp.MessageEn = fmt.Sprintf("Dual-Agent channel verified! Answer (%dms) + Judge (%dms, verdict=%s)", resp.AnswerMS, resp.JudgeMS, resp.JudgeLabel)
	return resp
}

// 错题根因桶闭集（对齐评测工作台 4-Bucket Failure Taxonomy + 命中桶）。
// 全部由既有布尔 / 计数 / 判词字段机械派生，不做任何词句语义判断（语言语种无关）。
const (
	BucketHit            = "HIT"             // 检索与答题双命中（0-LLM 臂即检索面命中）
	BucketRecallMiss     = "RECALL_MISS"     // 召回腿完全空手：指向种子 / 词面 / 向量召回层
	BucketRankCutoff     = "RANK_CUTOFF"     // 候选池有召回物却未进交付窗口：指向排序与预算分配层
	BucketReaderGap      = "READER_GAP"      // 证据已在交付窗口却答错：指向交付渲染 / 阅读面
	BucketJudgeAmbiguity = "JUDGE_AMBIGUITY" // 判词不可用或答案实质等价却判错：属评测方差面，禁据此外改检索算法
)

// bucketOrder 固定五桶输出顺序（汇总计数与 UI 渲染顺序稳定）。
var bucketOrder = []string{BucketHit, BucketRecallMiss, BucketRankCutoff, BucketReaderGap, BucketJudgeAmbiguity}

// classifyFailureBucket 把单题结果机械归入错题根因桶（闭集见 Bucket* 常量）。
//
// 判定阶梯（只读既有字段，零词句语义、零 IO）：
//  1. 判词 correct ⇒ HIT；
//  2. 判词 unparsed / error ⇒ JUDGE_AMBIGUITY（判官面不可用，先归评测方差面，不误指检索算法）；
//  3. 判词 wrong 且答题与金标实质等价（F1 ≥ 0.5）⇒ JUDGE_AMBIGUITY；
//  4. 证据在位（evidence_hit 或 answer_in_context）⇒ wrong = READER_GAP，其余 = HIT；
//  5. 证据不在位：召回腿有产出（raw_hits / bands / nodes / best_evidence_recall 任一非零）⇒ RANK_CUTOFF；
//     召回腿完全空手 ⇒ RECALL_MISS。
//
// 注：判词不可用（unparsed/error）与「实质等价却判错」两档都归 JUDGE_AMBIGUITY——根因指向判官口径与
// 评测方差面，据此改检索算法属误伤。金标已在窗口（answer_in_context）而答题仍差时归第 4 级 READER_GAP，
// 保证五桶互斥且各自可行动。
func classifyFailureBucket(item EvalQuestionResultItem) string {
	verdict := strings.ToLower(strings.TrimSpace(item.JudgeVerdict))
	switch verdict {
	case "correct":
		return BucketHit
	case "unparsed", "error":
		return BucketJudgeAmbiguity
	}
	if verdict == "wrong" && item.TokenF1 >= 0.5 {
		return BucketJudgeAmbiguity
	}
	if item.EvidenceHit || item.AnswerInContext {
		if verdict == "wrong" {
			return BucketReaderGap
		}
		return BucketHit
	}
	if item.BestEvidenceRecall > 0 || item.RawHits > 0 || item.Bands > 0 || item.Nodes > 0 {
		return BucketRankCutoff
	}
	return BucketRecallMiss
}

// bucketCountsFromItems 汇总错题根因五桶计数（HIT / RECALL_MISS / RANK_CUTOFF / READER_GAP / JUDGE_AMBIGUITY）。
// 有题即返回五键定形（缺桶计 0），无题返回 nil（omitempty 下不出键）；台账与实时帧共用。
func bucketCountsFromItems(items []EvalQuestionResultItem) map[string]int {
	if len(items) == 0 {
		return nil
	}
	counts := make(map[string]int, len(bucketOrder))
	for _, bucket := range bucketOrder {
		counts[bucket] = 0
	}
	for _, it := range items {
		bucket := strings.TrimSpace(it.FailureBucket)
		if bucket == "" {
			bucket = classifyFailureBucket(it)
		}
		counts[bucket]++
	}
	return counts
}

// plainReproPath 只放行无需 Bash 引号即可原样构成单个实参的字符。
var plainReproPath = regexp.MustCompile(`^[a-zA-Z0-9_./:+-]+$`)

// portableCLIStorePath 生成导出 Bash 命令里的 --eval-store 实参：
//   - 先把当前用户主目录（按原生路径语义判定是否包含）折叠为 ~/...，避免在可复制命令中硬编码特定机器的
//     绝对用户目录；位于主目录同级或跨卷的路径保持绝对形式；
//   - 再给需要引号的路径补单引号：Windows 原生路径里的反斜杠在 Git Bash 会被当转义符吞掉，
//     含空格的路径会被切成多个实参；
//   - 前导 ~ 不加引号，由读者机器上的 Bash 自行展开。
//
// Suggested by @wzm-Epoch in #4 and adopted here; see CHANGELOG.md.
func portableCLIStorePath(storePath string) string {
	p := filepath.ToSlash(storePath)
	if p == "~" {
		return p
	}
	prefix := ""
	if strings.HasPrefix(p, "~/") {
		prefix, p = "~/", p[2:]
	} else if home, err := os.UserHomeDir(); err == nil && filepath.IsAbs(storePath) {
		if rel, err := filepath.Rel(home, storePath); err == nil && filepath.IsLocal(rel) {
			if rel == "." {
				return "~"
			}
			prefix, p = "~/", filepath.ToSlash(rel)
		}
	}
	if plainReproPath.MatchString(p) {
		return prefix + p
	}
	return prefix + "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
}

// buildReproCLICommand 生成任意目录可直接运行的 SDK 契约复现命令。
func buildReproCLICommand(tier, armMode, storePath string, rawLimit int, useAgents bool) string {
	if rawLimit <= 0 {
		rawLimit = 96
	}
	args := []string{"rmem", "eval", "--eval-store", portableCLIStorePath(storePath), "--tier", normalizeTier(tier)}
	if strings.EqualFold(strings.TrimSpace(armMode), EvalArmOracle) {
		args = append(args, "--oracle")
	}
	if useAgents {
		args = append(args, "--answerer", "llm", "--judge", "llm")
	} else {
		args = append(args, "--answerer", "none", "--judge", "off")
	}
	args = append(args, "--top-k", strconv.Itoa(rawLimit))
	return strings.Join(args, " ")
}

func activeEvalTierForSuite(suite string) string {
	evalJobMu.RLock()
	tier := evalJobState.Tier
	evalJobMu.RUnlock()
	if normalizeSuite(suite) == SuiteATM {
		return normalizeATMTier(tier)
	}
	return normalizeTier(tier)
}

func activeEvalTier() string {
	return activeEvalTierForSuite(SuiteLoCoMo)
}

// —— 单题评测交付容量常量（字符载体口径 · 对齐记忆核心 tokenz-r TokenTier 阶梯）——
// 本插件随宿主独立模块（github.com/ai-go-hub/ai-go-admin）构建，不可直接引用记忆核 internal/delivery，
// 故在此以同一 2 的幂次阶梯口径（128 / 256 / 512 / 1024 / 2048 / 4096 / 8192 tokens）锚定容量常量，
// 禁止再出现匿名字符数魔数；数值本身保持冻结，以保证评测读数与 JSON / CLI 契约不变：
//   - tokenz-r 128k 词表实测：ASCII ≈ 0.3 token/字符、CJK ≈ 0.6–0.7 token/字；
//   - evalDeliverCharCap = 6000 字符 ≈ ASCII ≈1.8k tokens / CJK ≈4.0k tokens，即 TokenTier2048–TokenTier4096
//     的单题本地交付渲染窗口（不再向严格云端 recall 发出 --chars）；
//   - evalPreviewCharCap = 900 字符 ≈ ASCII ≈0.27k tokens / CJK ≈0.6k tokens，即 TokenTier256–TokenTier512
//     的单题明细预览窗口（context_preview 显示上界）。
const (
	evalDeliverCharCap = 24576
	evalPreviewCharCap = 900
)

func executeSingleQuestionEval(ctx context.Context, bin, storePath string, q EvalQuestionItem, index int, armMode string, rawLimit int, useAgents bool, aiCfg AIModelConfig) (EvalQuestionResultItem, map[string]any, error) {
	normalizedArm, err := normalizeEvalArm(armMode)
	if err != nil {
		return EvalQuestionResultItem{}, nil, err
	}
	armMode = normalizedArm
	if rawLimit <= 0 {
		rawLimit = 96
	}
	totalStart := time.Now()
	resItem := EvalQuestionResultItem{
		Index: index, ID: q.ID, SampleID: q.SampleID, Category: q.Category,
		Question: q.Question, Gold: q.Answer, EvidenceTexts: q.EvidenceTexts,
	}

	var recallRaw map[string]any
	var retrievalUnitIDs []string
	var contextLines []string
	var formattedItems []string
	// L0 无损增强：交付面按来源分行收集。行体先不带编号，装配成交付窗口后再统一编号，
	// 从而「先排序 / 再选材 / 最后编号」，编号顺序即实际送入冻结答题载荷的顺序。
	type bandEntry struct {
		text string
	}
	type deliveredLine struct {
		kind string // band | raw
		text string
	}
	var rawLines []string
	var bandEntries []bandEntry

	recallStart := time.Now()
	if armMode == EvalArmOracle {
		for idx, ev := range q.EvidenceTexts {
			trimmed := strings.TrimSpace(ev)
			if trimmed == "" {
				continue
			}
			contextLines = append(contextLines, trimmed)
			rawLines = append(rawLines, fmt.Sprintf("gold-%d · %s", idx+1, trimmed))
		}
		resItem.RawHits = len(contextLines)
		recallRaw = map[string]any{"mode": "oracle", "scope": evalScopeForSuite(SuiteLoCoMo), "evidence_texts": q.EvidenceTexts, "formatted": formattedItems}
	} else {
		strictRaw, unitIDs, err := runLocalEvalRecall(ctx, bin, storePath, SuiteLoCoMo, q.Question, rawLimit, nil)
		if err != nil {
			return resItem, nil, err
		}
		recallRaw = strictRaw
		retrievalUnitIDs = unitIDs
		seen := map[string]bool{}
		addCtx := func(s string) {
			s = strings.TrimSpace(s)
			if s == "" || seen[s] {
				return
			}
			seen[s] = true
			contextLines = append(contextLines, s)
		}
		if rHits, ok := recallRaw["raw_hits"].([]any); ok {
			resItem.RawHits = len(rHits)
			for idx, h := range rHits {
				m, ok := h.(map[string]any)
				if !ok {
					continue
				}
				txt := strings.TrimSpace(asString(m["text"]))
				if txt == "" {
					continue
				}
				addCtx(txt)
				author := strings.TrimSpace(asString(m["author"]))
				occ := int64(asInt(m["occurred_at"]))
				dateStr := ""
				if occ > 0 {
					dateStr = time.Unix(occ, 0).UTC().Format("2006-01-02")
				}
				prefix := ""
				if dateStr != "" && author != "" && author != "user" {
					prefix = "[" + dateStr + "] " + author + ": "
				} else if dateStr != "" {
					prefix = "[" + dateStr + "] "
				} else if author != "" && author != "user" {
					prefix = author + ": "
				}
				traceID := asString(m["trace"])
				if len(traceID) > 8 {
					traceID = traceID[:8]
				}
				if traceID == "" {
					traceID = fmt.Sprintf("m%d", idx+1)
				}
				rawLines = append(rawLines, fmt.Sprintf("%s · %s%s", traceID, prefix, txt))
			}
		}
		seenBandLine := map[string]bool{}
		if bands, ok := recallRaw["bands"].([]any); ok {
			resItem.Bands = len(bands)
			for _, b := range bands {
				bm, ok := b.(map[string]any)
				if !ok {
					continue
				}
				if nodes, ok := bm["nodes"].([]any); ok {
					resItem.Nodes += len(nodes)
					for _, n := range nodes {
						nm, ok := n.(map[string]any)
						if !ok {
							continue
						}
						rendered := strings.TrimSpace(asString(nm["content"]))
						if rendered == "" {
							continue
						}
						addCtx(rendered)
						// 按 content 行去重（对齐引擎 seenBandLine）：只避免同一行重复占用交付窗口，不丢信息。
						if seenBandLine[rendered] {
							continue
						}
						seenBandLine[rendered] = true
						nid := asString(nm["id"])
						if len(nid) > 8 {
							nid = nid[:8]
						}
						bandEntries = append(bandEntries, bandEntry{text: fmt.Sprintf("%s · %s", nid, rendered)})
					}
				}
			}
		}
	}
	resItem.RecallMS = time.Since(recallStart).Milliseconds()
	if armMode == EvalArmOracle {
		// Oracle 臂不写云端，只保留评测命名空间基名作为 provenance 标签（无云端世代 scope）。
		markOracleProvenance(&resItem, evalScopeForSuite(SuiteLoCoMo), len(contextLines), storePath)
	} else {
		applyRecallProvenance(&resItem, recallRaw, retrievalUnitIDs, storePath)
	}

	// SDK 云端与 Oracle 都采用原文优先；不得为停用融合臂重排或补充本地结果。
	ordered := make([]deliveredLine, 0, rawLimit)
	resItem.ContextOrdering = "raw_first"
	for _, line := range rawLines {
		if len(ordered) >= rawLimit {
			break
		}
		ordered = append(ordered, deliveredLine{kind: "raw", text: line})
	}
	for _, entry := range bandEntries {
		if len(ordered) >= rawLimit {
			break
		}
		ordered = append(ordered, deliveredLine{kind: "band", text: entry.text})
	}
	formattedItems = make([]string, 0, len(ordered))
	resItem.BandItems = make([]string, 0, len(ordered))
	resItem.RawItems = make([]string, 0, len(ordered))
	for idx, line := range ordered {
		numbered := fmt.Sprintf("%d. %s", idx+1, line.text)
		formattedItems = append(formattedItems, numbered)
		if line.kind == "band" {
			resItem.BandItems = append(resItem.BandItems, numbered)
		} else {
			resItem.RawItems = append(resItem.RawItems, numbered)
		}
	}
	resItem.NodesDelivered = len(resItem.BandItems)
	resItem.RawDelivered = len(resItem.RawItems)
	if resItem.NodesDelivered > 0 {
		resItem.BandsDelivered = resItem.Bands
		if resItem.BandsDelivered > resItem.NodesDelivered {
			resItem.BandsDelivered = resItem.NodesDelivered
		}
	}
	if armMode == EvalArmOracle && recallRaw != nil {
		// 保持 oracle 单题接口回传的 recall.formatted 与历史一致（金标交付行）。
		recallRaw["formatted"] = formattedItems
	}

	fullContext := strings.Join(contextLines, "\n")
	if len([]rune(fullContext)) > evalDeliverCharCap {
		fullContext = string([]rune(fullContext)[:evalDeliverCharCap])
	}
	resItem.ContextChars = len([]rune(fullContext))
	resItem.DeliveredItems = len(formattedItems)
	resItem.DeliveredTokens = countDeliveredTokens(recallRaw, len(formattedItems), strings.Join(formattedItems, "\n"))
	previewRunes := []rune(fullContext)
	if len(previewRunes) > evalPreviewCharCap {
		resItem.ContextPreview = string(previewRunes[:evalPreviewCharCap]) + " …"
	} else {
		resItem.ContextPreview = fullContext
	}
	resItem.FormattedItems = formattedItems

	bestEv, evMatches := evaluateEvidenceRecall(q.EvidenceTexts, fullContext)
	resItem.BestEvidenceRecall = bestEv
	resItem.EvidenceMatches = evMatches
	resItem.EvidenceHit = armMode == EvalArmOracle || bestEv >= 0.6
	// 官方金标覆盖（rank-aware）：对交付行序取前 10/20/50 条窗口的逐题覆盖率与 Any/All-Hit 判定。
	orderedTexts := make([]string, len(ordered))
	for oi, line := range ordered {
		orderedTexts[oi] = line.text
	}
	resItem.R10, resItem.R20, resItem.R50, resItem.AnyHit10, resItem.AllHit10 = evaluateRankedEvidenceRecall(q.EvidenceTexts, orderedTexts)
	resItem.MetricsAvailable = copyEvalMetricAvailability(nil)
	if len(q.EvidenceTexts) > 0 {
		for _, key := range []string{"evidence_hit", "r10", "r20", "r50", "any_hit_10", "all_hit_10"} {
			resItem.MetricsAvailable[key] = true
		}
	}
	for _, key := range []string{"avg_recall_ms", "p95_recall_ms", "avg_delivered_items", "avg_delivered_tokens", "total_delivered_tokens"} {
		resItem.MetricsAvailable[key] = true
	}
	resItem.MetricsAvailable["answer_in_context"] = strings.TrimSpace(q.Answer) != ""
	headEv := evaluateHead48EvidenceRecall(q.EvidenceTexts, fullContext)
	resItem.BestHeadEvidenceRecall = headEv
	resItem.HeadEvidenceHit = armMode == EvalArmOracle || headEv >= 0.6
	goldRecall := roundFloat(computeTokenRecall(q.Answer, fullContext), 4)
	resItem.GoldTokenRecall = goldRecall
	resItem.AnswerInContext = goldRecall >= 0.8

	if useAgents {
		ansOut, ansMS, ansErr := completeOpenAIChat(ctx, aiCfg.BaseURL, aiCfg.APIKey, aiCfg.AnswerModel, FrozenAnswerSystem, formatFrozenAnswerPayload(q.Question, formattedItems), 0.2, aiCfg.MaxTokens, aiCfg.TimeoutSec)
		resItem.AnswerMS = ansMS
		if ansErr != nil {
			resItem.JudgeVerdict = "error"
			resItem.JudgeReason = "Answer Agent Error: " + ansErr.Error()
		} else {
			resItem.Prediction = ansOut
			f1, _ := computeTokenF1(ansOut, q.Answer)
			resItem.TokenF1 = f1
			// LoCoMo 的 exact 走 CLI internal/eval/stages.go::mergeLoCoMoGrade 口径：
			// NormalizeAnswer(pred) == NormalizeAnswer(gold)，无 ATM 那条非空前置。
			resItem.ExactMatch = normalizeAnswerAdmin(ansOut) == normalizeAnswerAdmin(q.Answer)

			jdgOut, jdgMS, jdgErr := completeOpenAIChat(ctx, aiCfg.BaseURL, aiCfg.APIKey, aiCfg.JudgeModel, FrozenJudgeSystem, formatFrozenJudgeUser(q.Question, q.Answer, ansOut), 0.0, aiCfg.MaxTokens, aiCfg.TimeoutSec)
			resItem.JudgeMS = jdgMS
			if jdgErr != nil {
				resItem.JudgeVerdict = "error"
				resItem.JudgeReason = "Judge Agent Error: " + jdgErr.Error()
			} else {
				verdict, reason, _ := parseFrozenJudgeOutput(jdgOut)
				resItem.JudgeVerdict = verdict
				resItem.JudgeReason = reason
			}
		}
	}
	// L0 无损增强：错题根因桶与单题等价复现命令（只由既有字段派生 / 只读生效参数，纯函数，无新增 IO）。
	resItem.FailureBucket = classifyFailureBucket(resItem)
	resItem.ReproCLI = buildReproCLICommand(activeEvalTier(), armMode, storePath, rawLimit, useAgents)
	resItem.TotalMS = time.Since(totalStart).Milliseconds()
	return resItem, recallRaw, nil
}

func appendJobLog(line string) {
	stamp := time.Now().Format("15:04:05")
	evalJobState.Logs = append(evalJobState.Logs, "["+stamp+"] "+line)
	if len(evalJobState.Logs) > 80 {
		evalJobState.Logs = evalJobState.Logs[len(evalJobState.Logs)-80:]
	}
}

// recomputeLiveAggregates 复算面板聚合指标（与 CLI summary 同尺）：
//   - f1 / qs_score 均按「逐行四位取整后再求均值」——CLI 从 results-*.jsonl 复算时读的
//     是行内已 PyRound 的值（internal/eval/aggregate.go::Aggregate / atmSummaryExtras）；
//   - ATM-Bench 的分类分取每题官方分均值（list_recall 连续 Jaccard / number EM /
//     open_end 判官），与 CLI by_category 同尺；二值 verdict 只供五桶归因；
//   - judge_unparsed 与 judge_error 分列（CLI summary 的 judge_unparsed / judge_error 同名同义），
//     二者均剔除出 judge_scored 分母。
func recomputeLiveAggregates(items []EvalQuestionResultItem, suite string) (evRate, aicRate, f1Avg, jdgRate float64, jdgCorrect, jdgScored, jdgWrong, jdgUnparsed, jdgError int, catEv, catJdg CategoryScores) {
	n := len(items)
	if n == 0 {
		return
	}
	evHits, aicHits := 0, 0
	f1Sum := 0.0
	qsSum := 0.0
	qsCnt := 0
	catTotal := map[string]int{}
	catEvHits := map[string]int{}
	catJdgScored := map[string]int{}
	catJdgHits := map[string]int{}
	catQSSum := map[string]float64{}
	catQSCnt := map[string]int{}

	for _, it := range items {
		cat := it.Category
		catTotal[cat]++
		if it.EvidenceHit {
			evHits++
			catEvHits[cat]++
		}
		if it.AnswerInContext {
			aicHits++
		}
		f1Sum += roundFloat(it.TokenF1, 4)
		if it.QSScored {
			qsCnt++
			qsSum += roundFloat(it.QSScore, 4)
			catQSSum[cat] += roundFloat(it.QSScore, 4)
			catQSCnt[cat]++
		}
		switch it.JudgeVerdict {
		case "correct":
			jdgCorrect++
			jdgScored++
			catJdgScored[cat]++
			catJdgHits[cat]++
		case "wrong":
			jdgWrong++
			jdgScored++
			catJdgScored[cat]++
		case "unparsed":
			jdgUnparsed++
		case "error":
			jdgError++
		}
	}
	evRate = roundFloat(float64(evHits)/float64(n), 4)
	aicRate = roundFloat(float64(aicHits)/float64(n), 4)
	f1Avg = roundFloat(f1Sum/float64(n), 4)
	if jdgScored > 0 {
		jdgRate = roundFloat(float64(jdgCorrect)/float64(jdgScored), 4)
	}
	calcCat := func(hitMap, totMap map[string]int, k string) float64 {
		tot := totMap[k]
		if tot <= 0 {
			return 0
		}
		return roundFloat(float64(hitMap[k])/float64(tot), 4)
	}
	catEv = CategoryScores{
		Cat1: calcCat(catEvHits, catTotal, "1"),
		Cat2: calcCat(catEvHits, catTotal, "2"),
		Cat3: calcCat(catEvHits, catTotal, "3"),
		Cat4: calcCat(catEvHits, catTotal, "4"),
	}
	catJdg = CategoryScores{
		Cat1: calcCat(catJdgHits, catJdgScored, "1"),
		Cat2: calcCat(catJdgHits, catJdgScored, "2"),
		Cat3: calcCat(catJdgHits, catJdgScored, "3"),
		Cat4: calcCat(catJdgHits, catJdgScored, "4"),
	}
	// ATM-Bench 官方分型口径：judge_rate 与分类分都取每题官方分（list_recall 连续
	// Jaccard / number EM / open_end 判官）逐行四位取整后的均值，与 CLI
	// internal/eval/atmbench.go 的 summary.judge_rate / by_category 对齐；
	// 二值 verdict（含 list_recall 的 0.5 阈值）只服务五桶归因，不得当分数展示。
	if normalizeSuite(suite) == SuiteATM && qsCnt > 0 {
		jdgRate = roundFloat(qsSum/float64(qsCnt), 4)
		calcOfficial := func(k string) float64 {
			if catQSCnt[k] <= 0 {
				return 0
			}
			return roundFloat(catQSSum[k]/float64(catQSCnt[k]), 4)
		}
		catJdg = CategoryScores{
			Cat1: calcOfficial("1"),
			Cat2: calcOfficial("2"),
			Cat3: calcOfficial("3"),
			Cat4: calcOfficial("4"),
		}
	}
	return
}

func startStreamingEvalJob(home string, req evalRunRequest) (EvalJobStatus, error) {
	validatedArm, armErr := validateEvalRunRequest(req)
	if armErr != nil {
		return EvalJobStatus{}, armErr
	}
	req.ArmMode = validatedArm
	bin := resolveBinary(home)
	if bin == "" {
		return EvalJobStatus{}, errors.New("未找到 rolling-memory 引擎二进制")
	}
	suite := normalizeSuite(req.Suite)
	storePath := resolveEvalDBPathForSuite(home, suite)
	dataDir := resolveEvalDataDir(home)
	tier := normalizeTier(req.Tier)
	if suite == SuiteATM {
		tier = normalizeATMTier(req.Tier)
	}
	armMode := req.ArmMode
	rawLimit := req.RawLimit
	if rawLimit <= 0 {
		if suite == SuiteATM {
			rawLimit = 50
		} else {
			rawLimit = 96
		}
	}
	workers := req.Workers
	if workers <= 0 {
		if req.UseAgents {
			workers = 3
		} else {
			workers = 2
		}
	}
	if workers > 8 {
		workers = 8
	}

	aiCfg := mergeAIConfig(loadAIConfig(home), req.AIConfig)
	if req.UseAgents {
		chk := checkAIModelConfig(aiCfg)
		if !chk.Ready {
			return EvalJobStatus{}, errors.New(chk.Summary)
		}
	}

	var allQuestions []EvalQuestionItem
	var err error
	if suite == SuiteATM {
		allQuestions, err = loadATMTierQuestions(dataDir, home, tier)
	} else {
		allQuestions, err = loadTierQuestions(dataDir, tier)
	}
	if err != nil {
		return EvalJobStatus{}, fmt.Errorf("加载 Tier-%s 题集失败: %v", tier, err)
	}
	catFilter := strings.TrimSpace(req.Category)
	filtered := make([]EvalQuestionItem, 0, len(allQuestions))
	for _, q := range allQuestions {
		if catFilter != "" && q.Category != catFilter {
			continue
		}
		filtered = append(filtered, q)
	}
	if req.Offset > 0 && req.Offset < len(filtered) {
		filtered = filtered[req.Offset:]
	}
	limit := req.Limit
	if limit <= 0 {
		limit = len(filtered)
	}
	if limit < len(filtered) {
		filtered = filtered[:limit]
	}
	if len(filtered) == 0 {
		return EvalJobStatus{}, errors.New("当前筛选条件下没有可评测的题目")
	}

	evalJobMu.Lock()
	if evalJobState.Status == "running" {
		evalJobMu.Unlock()
		return EvalJobStatus{}, errors.New("已有评测任务正在运行或保存，请稍后再启动")
	}
	if evalJobCancel != nil {
		evalJobCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	evalJobCancel = cancel

	jobID := "job-" + time.Now().Format("20060102-150405")
	if evalJobState.JobID == jobID || strings.HasPrefix(evalJobState.JobID, jobID+"-") {
		jobID = fmt.Sprintf("%s-%d", jobID, time.Now().UnixNano()%100000)
	}
	reproCmd := buildReproCLICommand(tier, armMode, storePath, rawLimit, req.UseAgents)
	if suite == SuiteATM {
		reproCmd = buildATMReproCLICommand(tier, armMode, storePath, rawLimit, req.UseAgents)
	}
	evalJobAIConfig = aiCfg
	evalJobState = EvalJobStatus{
		JobID: jobID, Suite: suite, Status: "running", Phase: "evaluating",
		PhaseText: "正在初始化评测环境…", PhaseTextEn: "Initializing evaluation environment...",
		Tier: tier, ArmMode: armMode, UseAgents: req.UseAgents,
		RawLimit: rawLimit, Workers: workers, CategoryFilter: catFilter,
		Total: len(filtered), Done: 0, Percent: 0,
		StartedAt:   time.Now().Format("15:04:05"),
		RecentItems: []EvalQuestionResultItem{}, Logs: []string{},
		UpdatedAtUnixMS: time.Now().UnixMilli(),
		ReproCLI:        reproCmd,
	}
	appendJobLog(fmt.Sprintf("启动评测任务 %s：Suite=%s · Tier-%s · 共 %d 题 · 检索臂=%s (rl%d) · 双智能体=%v",
		jobID, suite, tier, len(filtered), armMode, rawLimit, req.UseAgents))
	evalRunItemsMu.Lock()
	evalRunItems = make([]EvalQuestionResultItem, 0, len(filtered))
	evalRunItemsJobID = jobID
	evalRunItemsMu.Unlock()
	evalJobMu.Unlock()
	initialSnap := snapshotJobStatus()

	go runStreamingEvalWorker(ctx, home, bin, storePath, dataDir, jobID, req, aiCfg, filtered)
	return initialSnap, nil
}

func claimEvalFailure(ctx context.Context, jobID string) bool {
	evalJobMu.Lock()
	defer evalJobMu.Unlock()
	if ctx.Err() != nil || evalJobState.JobID != jobID || evalJobState.Status != "running" || evalJobState.Phase == "finalizing" {
		return false
	}
	evalJobState.Phase = "finalizing"
	evalJobState.UpdatedAtUnixMS = time.Now().UnixMilli()
	return true
}

func beginEvalQuestion(ctx context.Context, jobID string, q EvalQuestionItem, total int) bool {
	evalJobMu.Lock()
	defer evalJobMu.Unlock()
	if ctx.Err() != nil || evalJobState.JobID != jobID || evalJobState.Status != "running" || evalJobState.Phase == "finalizing" {
		return false
	}
	evalJobState.Phase = "evaluating"
	evalJobState.CurrentQuestionID = q.ID
	evalJobState.CurrentQuestionText = q.Question
	evalJobState.PhaseText = fmt.Sprintf("正在评测第 %d/%d 题 (%s · Cat %s)…", evalJobState.Done+1, total, q.ID, q.Category)
	evalJobState.PhaseTextEn = fmt.Sprintf("Evaluating Q %d/%d (%s · Cat %s)...", evalJobState.Done+1, total, q.ID, q.Category)
	return true
}

// evalRunPhases 是一次评测任务的墙钟分相：语料就绪/载入阶段与随后的逐题评测。
// ElapsedSeconds 仍是两者之和；分相只增字段、不改旧字段语义。
type evalRunPhases struct {
	LoadSeconds float64
	EvalSeconds float64
}

func runStreamingEvalWorker(ctx context.Context, home, bin, storePath, dataDir, jobID string, req evalRunRequest, aiCfg AIModelConfig, questions []EvalQuestionItem) {
	started := time.Now()
	workerError := ""
	suite := normalizeSuite(req.Suite)
	armMode := req.ArmMode
	rawLimit := req.RawLimit
	if rawLimit <= 0 {
		if suite == SuiteATM {
			rawLimit = 50
		} else {
			rawLimit = 96
		}
	}
	workers := req.Workers
	if workers <= 0 {
		workers = 2
	}
	if workers > 8 {
		workers = 8
	}

	var atmUnits map[string]atmEvalUnit
	if suite == SuiteATM {
		var err error
		atmUnits, err = loadATMEvalUnits(home, dataDir)
		if err != nil {
			if claimEvalFailure(ctx, jobID) {
				finalizeEvalRunRecordWithStatus(home, jobID, req, aiCfg, nil, time.Since(started).Seconds(), evalRunPhases{}, evalTaskStatusFailed, "加载 ATM UnitID 映射失败: "+err.Error())
			}
			return
		}
	}
	phases := evalRunPhases{}
	if armMode != EvalArmOracle {
		evalJobMu.Lock()
		if evalJobState.JobID != jobID || evalJobState.Status != "running" || evalJobState.Phase == "finalizing" || ctx.Err() != nil {
			evalJobMu.Unlock()
			return
		}
		evalJobState.Phase = "ingest"
		evalJobState.PhaseText = "正在准备隔离评测语料（分配独立世代 scope → 写入原文语料 → 完整性核验）…"
		evalJobState.PhaseTextEn = "Preparing isolated evaluation corpus (allocating generation scope -> ingesting corpus -> integrity probe)..."
		evalJobState.UpdatedAtUnixMS = time.Now().UnixMilli()
		evalJobMu.Unlock()

		probeQuery := "memory"
		if len(questions) > 0 {
			probeQuery = questions[0].Question
		}
		// 优先复用已校验通过的隔离评测语料（耗时 <1s），仅在未准备或校验未过时自动构建，
		// 避免每次点运行都强制全量重灌 11,034 / 5,882 条文档导致长时间卡在 0%。
		loadStart := time.Now()
		readiness, err := prepareLocalEvalStore(ctx, bin, home, suite, req.Tier, storePath, dataDir, probeQuery, atmUnits, false)
		if err != nil {
			phases.LoadSeconds = time.Since(loadStart).Seconds()
			if ctx.Err() == nil && claimEvalFailure(ctx, jobID) {
				finalizeEvalRunRecordWithStatus(home, jobID, req, aiCfg, nil, time.Since(started).Seconds(), phases, evalTaskStatusFailed, err.Error())
			}
			return
		}
		phases.LoadSeconds = time.Since(loadStart).Seconds()
		evalJobMu.Lock()
		if evalJobState.JobID == jobID && evalJobState.Status == "running" {
			appendJobLog(fmt.Sprintf("隔离评测语料已就绪：scope=%s · 回收旧单元=%d 条 · documents=%d · probe_hits=%d",
				readiness.Scope, readiness.Manifest.PurgedUnits, readiness.Manifest.Documents, readiness.LocalHits))
		}
		evalJobMu.Unlock()
	}
	evalStart := time.Now()

	type indexedTask struct {
		idx int
		q   EvalQuestionItem
	}
	taskCh := make(chan indexedTask, len(questions))
	resultCh := make(chan EvalQuestionResultItem, len(questions))
	for idx, q := range questions {
		taskCh <- indexedTask{idx: idx + 1, q: q}
	}
	close(taskCh)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// 非 Oracle 臂：每个 worker 持一个常驻 recall 热子进程服务全部分到的题，
			// 免去逐题进程冷启；启动失败不致命（workerCtx 不带热会话即整 worker 回退
			// 一次性 fork 路径，读数口径同构）。
			workerCtx := ctx
			if armMode != EvalArmOracle {
				if manifest, mErr := readEvalManifest(storePath, suite); mErr == nil {
					if warm, wErr := startWarmEvalRecallSession(ctx, bin, storePath, manifest.Scope); wErr == nil {
						defer warm.Close()
						workerCtx = withWarmEvalRecallSession(ctx, warm)
					}
				}
			}
			for task := range taskCh {
				if !beginEvalQuestion(ctx, jobID, task.q, len(questions)) {
					return
				}

				var item EvalQuestionResultItem
				var err error
				if suite == SuiteATM {
					item, _, err = executeSingleATMQuestionEval(workerCtx, bin, storePath, task.q, task.idx, armMode, rawLimit, req.UseAgents, aiCfg, atmUnits)
				} else {
					item, _, err = executeSingleQuestionEval(workerCtx, bin, storePath, task.q, task.idx, armMode, rawLimit, req.UseAgents, aiCfg)
				}
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					evalJobMu.Lock()
					if workerError == "" && ctx.Err() == nil && evalJobState.JobID == jobID && evalJobState.Status == "running" && evalJobState.Phase != "finalizing" {
						workerError = err.Error()
						evalJobState.Phase = "finalizing"
						evalJobState.Error = workerError
						appendJobLog("严格召回失败，任务已 fail closed: " + err.Error())
						if evalJobCancel != nil {
							evalJobCancel()
						}
						evalJobState.UpdatedAtUnixMS = time.Now().UnixMilli()
					}
					evalJobMu.Unlock()
					return
				}
				select {
				case resultCh <- item:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	completedItems := make([]EvalQuestionResultItem, 0, len(questions))
	for item := range resultCh {
		evalJobMu.RLock()
		superseded := evalJobState.JobID != jobID
		evalJobMu.RUnlock()
		completedItems = append(completedItems, item)
		if superseded || ctx.Err() != nil {
			continue
		}
		evalJobMu.RLock()
		evalRunItemsMu.Lock()
		if evalRunItemsJobID == jobID && evalJobState.JobID == jobID && evalJobState.Status == "running" && evalJobState.Phase != "finalizing" && ctx.Err() == nil {
			evalRunItems = append(evalRunItems, item)
		}
		evalRunItemsMu.Unlock()
		evalJobMu.RUnlock()

		done := len(completedItems)
		total := len(questions)
		elapsed := roundFloat(time.Since(started).Seconds(), 1)
		eta := 0.0
		if done > 0 && done < total {
			eta = roundFloat((time.Since(started).Seconds()/float64(done))*float64(total-done), 1)
		}
		evRate, aicRate, f1Avg, jdgRate, jdgCor, jdgSco, jdgWrg, jdgUnp, jdgErrCnt, catEv, catJdg := recomputeLiveAggregates(completedItems, suite)
		avgRecallMS, _, avgDelivTokens, _, avgDelivItems := computeDeliveryTelemetry(completedItems)
		liveR10, liveR20, liveR50, liveAny10, liveAll10 := computeOfficialRecallAggregates(completedItems)

		evalJobMu.Lock()
		if evalJobState.JobID != jobID || evalJobState.Status != "running" || evalJobState.Phase == "finalizing" || ctx.Err() != nil {
			evalJobMu.Unlock()
			continue
		}
		evalJobState.Done = done
		evalJobState.Percent = roundFloat(float64(done)*100.0/float64(total), 1)
		evalJobState.ElapsedSeconds = elapsed
		evalJobState.ETASeconds = eta
		evalJobState.LiveEvidenceHit = evRate
		evalJobState.LiveAnswerInContext = aicRate
		evalJobState.LiveF1 = f1Avg
		evalJobState.LiveR10 = liveR10
		evalJobState.LiveR20 = liveR20
		evalJobState.LiveR50 = liveR50
		evalJobState.LiveAnyHit10 = liveAny10
		evalJobState.LiveAllHit10 = liveAll10
		evalJobState.LiveJudgeRate = jdgRate
		evalJobState.LiveJudgeCorrect = jdgCor
		evalJobState.LiveJudgeScored = jdgSco
		evalJobState.LiveJudgeWrong = jdgWrg
		evalJobState.LiveJudgeUnparsed = jdgUnp
		evalJobState.LiveJudgeError = jdgErrCnt
		evalJobState.LiveAvgRecallMS = avgRecallMS
		evalJobState.LiveAvgDeliveredTokens = avgDelivTokens
		evalJobState.LiveAvgDeliveredItems = avgDelivItems
		evalJobState.LiveCategoryEvidence = catEv
		evalJobState.LiveCategoryJudge = catJdg
		// L0 无损增强：实时错题根因五桶计数（只增键，既有 live_* 键名与含义不变）。
		evalJobState.BucketCounts = bucketCountsFromItems(completedItems)

		itemCopy := itemForLiveStream(item)
		evalJobState.LatestItem = &itemCopy
		evalJobState.RecentItems = append([]EvalQuestionResultItem{itemCopy}, evalJobState.RecentItems...)
		if len(evalJobState.RecentItems) > 25 {
			evalJobState.RecentItems = evalJobState.RecentItems[:25]
		}
		evTag := "MISS"
		if item.EvidenceHit {
			evTag = "HIT"
		}
		if req.UseAgents {
			appendJobLog(fmt.Sprintf("[%d/%d] %s (Cat %s) → Ev:%s(%.0f%%) · Judge:%s · %dms | 累计 R@10=%.1f%% Ev=%.1f%% Judge=%.1f%% (%d/%d)",
				done, total, item.ID, item.Category, evTag, item.BestEvidenceRecall*100, strings.ToUpper(item.JudgeVerdict), item.TotalMS,
				liveR10*100, evRate*100, jdgRate*100, jdgCor, jdgSco))
		} else {
			appendJobLog(fmt.Sprintf("[%d/%d] %s (Cat %s) → Ev:%s(%.0f%%) · AIC:%v · %dms | 累计 R@10=%.1f%% Ev=%.1f%% AIC=%.1f%%",
				done, total, item.ID, item.Category, evTag, item.BestEvidenceRecall*100, item.AnswerInContext, item.TotalMS,
				liveR10*100, evRate*100, aicRate*100))
		}
		evalJobState.UpdatedAtUnixMS = time.Now().UnixMilli()
		evalJobMu.Unlock()
	}

	if ctx.Err() != nil {
		evalJobMu.RLock()
		errMsg := workerError
		evalJobMu.RUnlock()
		if errMsg != "" {
			phases.EvalSeconds = time.Since(evalStart).Seconds()
			finalizeEvalRunRecordWithStatus(home, jobID, req, aiCfg, completedItems, time.Since(started).Seconds(), phases, evalTaskStatusFailed, errMsg)
		}
		return
	}
	evalJobMu.Lock()
	if evalJobState.JobID != jobID || evalJobState.Status != "running" || ctx.Err() != nil {
		evalJobMu.Unlock()
		return
	}
	evalJobState.Phase = "finalizing"
	evalJobState.UpdatedAtUnixMS = time.Now().UnixMilli()
	evalJobMu.Unlock()
	phases.EvalSeconds = time.Since(evalStart).Seconds()
	finalizeEvalRunRecord(home, jobID, req, aiCfg, completedItems, time.Since(started).Seconds(), phases, false)
}

func evalTaskProvenanceFromItems(items []EvalQuestionResultItem) evalTaskProvenance {
	if len(items) == 0 {
		return evalTaskProvenance{}
	}
	item := items[0]
	return evalTaskProvenance{Scope: item.EngineScope, Endpoint: item.EngineUpstream, CorpusSHA256: item.CorpusSHA256}
}

func finalizeEvalRunRecord(home, jobID string, req evalRunRequest, aiCfg AIModelConfig, items []EvalQuestionResultItem, elapsedSec float64, phases evalRunPhases, isPartial bool) (EvalRunRecord, error) {
	status := evalTaskStatusCompleted
	if isPartial {
		status = evalTaskStatusCancelled
	}
	return finalizeEvalRunRecordWithStatus(home, jobID, req, aiCfg, items, elapsedSec, phases, status, "")
}

func buildEvalRunRecord(home, jobID string, req evalRunRequest, aiCfg AIModelConfig, items []EvalQuestionResultItem, elapsedSec float64, phases evalRunPhases, status, errMsg string) EvalRunRecord {
	isPartial := status != evalTaskStatusCompleted
	suite := normalizeSuite(req.Suite)
	tier := normalizeTier(req.Tier)
	if suite == SuiteATM {
		tier = normalizeATMTier(req.Tier)
	}
	armMode, armErr := normalizeEvalArm(req.ArmMode)
	if armErr != nil {
		armMode = "invalid-retired-arm"
	}
	rawLimit := req.RawLimit
	if rawLimit <= 0 {
		if suite == SuiteATM {
			rawLimit = 50
		} else {
			rawLimit = 96
		}
	}
	evRate, aicRate, f1Avg, jdgRate, jdgCor, jdgSco, _, _, _, catEv, catJdg := recomputeLiveAggregates(items, suite)
	avgRecallMS, p95RecallMS, avgDelivTokens, totalDelivTokens, avgDelivItems := computeDeliveryTelemetry(items)
	r10Avg, r20Avg, r50Avg, anyHit10Rate, allHit10Rate := computeOfficialRecallAggregates(items)

	armLabel := fmt.Sprintf("%s-none (0-LLM · rl%d)", armMode, rawLimit)
	if suite == SuiteATM {
		armLabel = fmt.Sprintf("atm-%s-%s-none (0-LLM · k=%d)", tier, armMode, rawLimit)
	}
	modelLabel := "none (0-LLM)"
	if req.UseAgents {
		armLabel = fmt.Sprintf("%s-llm (rl%d)", armMode, rawLimit)
		if suite == SuiteATM {
			armLabel = fmt.Sprintf("atm-%s-%s-llm (k=%d)", tier, armMode, rawLimit)
		}
		modelLabel = aiCfg.AnswerModel
		if aiCfg.JudgeModel != "" && aiCfg.JudgeModel != aiCfg.AnswerModel {
			modelLabel = aiCfg.AnswerModel + " / " + aiCfg.JudgeModel
		}
	}
	now := time.Now()
	runID := "run-" + now.Format("20060102-150405")
	if suite == SuiteATM {
		runID = "atm-run-" + now.Format("20060102-150405")
	}
	if jobID != "" {
		runID = jobID
	}
	catSuffix := ""
	if strings.TrimSpace(req.Category) != "" {
		catSuffix = " · Cat " + strings.TrimSpace(req.Category)
	}
	partialTag, partialTagEn := "", ""
	if isPartial {
		partialTag = " [中途保存]"
		partialTagEn = " [Partial Saved]"
	}
	lbl := strings.TrimSpace(req.CustomLabel)
	lblEn := lbl
	if lbl == "" {
		if suite == SuiteATM {
			lbl = fmt.Sprintf("ATM Run · Tier-%s (%d 题%s)%s", tier, len(items), catSuffix, partialTag)
			lblEn = fmt.Sprintf("ATM Run · Tier-%s (%d Qs%s)%s", tier, len(items), catSuffix, partialTagEn)
		} else {
			lbl = fmt.Sprintf("Run · Tier-%s (%d 题%s)%s", tier, len(items), catSuffix, partialTag)
			lblEn = fmt.Sprintf("Run · Tier-%s (%d Qs%s)%s", tier, len(items), catSuffix, partialTagEn)
		}
	}
	protoID := FrozenProtocolID
	storePath := resolveEvalDBPathForSuite(home, suite)
	reproCmd := buildReproCLICommand(tier, armMode, storePath, rawLimit, req.UseAgents)
	if suite == SuiteATM {
		protoID = ATMProtocolID
		reproCmd = buildATMReproCLICommand(tier, armMode, storePath, rawLimit, req.UseAgents)
	}
	rec := EvalRunRecord{
		ID: runID, Suite: suite, Label: lbl, LabelEn: lblEn,
		CreatedAt: now.Format("2006-01-02 15:04:05"), IsBaseline: false,
		Tier: tier, ScoredQuestions: len(items), Arm: armLabel,
		Model: modelLabel, Protocol: protoID,
		EvidenceHit: evRate, AnswerInContext: aicRate,
		JudgeRate: jdgRate, JudgeCorrect: jdgCor, JudgeScored: jdgSco,
		ElapsedSeconds:       roundFloat(elapsedSec, 1),
		LoadMS:               int64(roundFloat(phases.LoadSeconds*1000, 0)),
		EvalSeconds:          roundFloat(phases.EvalSeconds, 1),
		AvgRecallMS:          avgRecallMS,
		P95RecallMS:          p95RecallMS,
		AvgDeliveredTokens:   avgDelivTokens,
		TotalDeliveredTokens: totalDelivTokens,
		AvgDeliveredItems:    avgDelivItems,
		R10:                  r10Avg,
		R20:                  r20Avg,
		R50:                  r50Avg,
		AnyHit10:             anyHit10Rate,
		AllHit10:             allHit10Rate,
		CategoryEvidence:     catEv, CategoryJudge: catJdg,
		Note: fmt.Sprintf("评测实验室实测 (%s · F1=%.3f · 检索 %.1fms · 交付 %.1f条/%dtok · 耗时 %.1fs)",
			armLabel, f1Avg, avgRecallMS, avgDelivItems, avgDelivTokens, elapsedSec),
		NoteEn: fmt.Sprintf("Evaluation Lab run (%s · F1=%.3f · recall %.1fms · deliv %.1f items/%dtok · %.1fs)",
			armLabel, f1Avg, avgRecallMS, avgDelivItems, avgDelivTokens, elapsedSec),
		ReproCLI:     reproCmd,
		TopK:         rawLimit,
		BucketCounts: bucketCountsFromItems(items),
	}
	rec.MetricsAvailable = evalRunMetricAvailability(items, suite, rec.IngestMS)
	rec.MetricEvidenceExplicit = true
	rec.Status, rec.Error = status, errMsg
	return rec
}

func finalizeEvalRunRecordWithStatus(home, jobID string, req evalRunRequest, aiCfg AIModelConfig, items []EvalQuestionResultItem, elapsedSec float64, phases evalRunPhases, status, errMsg string) (EvalRunRecord, error) {
	rec := buildEvalRunRecord(home, jobID, req, aiCfg, items, elapsedSec, phases, status, errMsg)
	isPartial := status != evalTaskStatusCompleted
	prov := evalTaskProvenanceFromItems(items)
	ledgerErr := saveEvalTaskResult(rec, prov, status, errMsg, items)
	ledgerWarning := ""
	if ledgerErr != nil {
		ledgerWarning = "评测结果未保存：" + ledgerErr.Error()
	}

	evalJobMu.Lock()
	if evalJobState.JobID == jobID || jobID == "" {
		if status == evalTaskStatusFailed {
			evalJobState.Status = "failed"
			evalJobState.Error = errMsg
			evalJobState.Phase = "done"
			evalJobState.PhaseText = fmt.Sprintf("评测失败，已将完成的 %d 题保存至台账", len(items))
			evalJobState.PhaseTextEn = fmt.Sprintf("Evaluation failed; saved %d completed questions to ledger", len(items))
		} else if isPartial {
			evalJobState.Status = "cancelled"
			evalJobState.Phase = "done"
			evalJobState.PhaseText = fmt.Sprintf("评测已手动停止，已将完成的 %d 题保存至台账", len(items))
			evalJobState.PhaseTextEn = fmt.Sprintf("Evaluation stopped; saved %d completed questions to ledger", len(items))
		} else {
			evalJobState.Status = "completed"
			evalJobState.Phase = "done"
			evalJobState.PhaseText = fmt.Sprintf("评测全部完成！共 %d 题", len(items))
			evalJobState.PhaseTextEn = fmt.Sprintf("Evaluation completed! %d Qs", len(items))
		}
		if ledgerWarning != "" {
			evalJobState.Status = "failed"
			evalJobState.Record = nil
			evalJobState.PhaseText = ledgerWarning
			evalJobState.PhaseTextEn = "Evaluation results were not saved: " + ledgerWarning
			if evalJobState.Error == "" {
				evalJobState.Error = ledgerWarning
			} else {
				evalJobState.Error += "; " + ledgerWarning
			}
			appendJobLog(ledgerWarning)
		} else {
			evalJobState.Record = &rec
			appendJobLog(fmt.Sprintf("评测收口已记入台账 (%s)：题数=%d · 耗时=%.1fs", rec.ID, len(items), elapsedSec))
		}
		evalJobState.UpdatedAtUnixMS = time.Now().UnixMilli()
	}
	evalJobMu.Unlock()
	if rec.Suite == SuiteLoCoMo {
		InvalidateLocomoPublicCorpusCache()
	}
	if ledgerErr != nil {
		return rec, fmt.Errorf("评测结果未保存: %w", ledgerErr)
	}
	return rec, nil
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func normalizeEvalRunRequest(home string, req *evalRunRequest) error {
	if req.TopK > 0 {
		req.RawLimit = req.TopK
	}
	if strings.EqualFold(strings.TrimSpace(req.ArmMode), EvalArmOracle) || strings.TrimSpace(req.EvalStore) == "—" {
		req.EvalStore = ""
	}
	if requested := strings.TrimSpace(req.EvalStore); requested != "" {
		expected := resolveEvalDBPathForSuite(home, req.Suite)
		if filepath.Clean(requested) != filepath.Clean(expected) {
			return fmt.Errorf("eval_store 必须使用本套件固定隔离路径 %s", expected)
		}
	}
	return nil
}

func evalRunHandler(c *gin.Context) {
	var req evalRunRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Fail(c, httpx.WithMessage("参数错误: "+err.Error()))
		return
	}
	home, _ := os.UserHomeDir()
	if err := normalizeEvalRunRequest(home, &req); err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	dataDir := resolveEvalDataDir(home)
	bin := resolveBinary(home)

	switch req.Mode {
	case "status":
		httpx.Success(c, httpx.WithData(gin.H{
			"job":         snapshotJobStatus(),
			"run_history": loadRunHistoryBySuite(req.Suite),
		}))
		return
	case "check_ai":
		aiCfg := mergeAIConfig(loadAIConfig(home), req.AIConfig)
		httpx.Success(c, httpx.WithData(gin.H{
			"ai_check":  checkAIModelConfigWithPlugin(aiCfg),
			"ai_config": maskAIConfig(aiCfg),
		}))
		return
	case "ai_channels":
		// AI 路由插件渠道候选清单（供前置拦截弹窗展示与一键接入）
		options := loadAIPluginChannels()
		routable := 0
		for _, option := range options {
			if option.Routable {
				routable++
			}
		}
		httpx.Success(c, httpx.WithData(gin.H{
			"channels": options,
			"total":    len(options),
			"routable": routable,
		}))
		return
	case "ai_apply_channel":
		// 一键接入 AI 插件渠道：服务端读取生效凭证（渠道非空优先、空则继承厂商）写进评测配置。
		// 明文密钥不出服务端：只在写盘后回掩码配置与新的预检结果。
		baseURL, apiKey, upstreamName, models, ok := resolveAIPluginChannel(req.ChannelID)
		if !ok {
			httpx.Fail(c, httpx.WithMessage("未找到该 AI 渠道（plugins/ai 未安装或渠道不存在）"))
			return
		}
		if strings.TrimSpace(baseURL) == "" {
			httpx.Fail(c, httpx.WithMessage("该渠道与其所属厂商都未配置 Base URL，无法接入"))
			return
		}
		model := strings.TrimSpace(req.ApplyModel)
		if model == "" {
			if len(models) > 0 {
				model = models[0]
			} else {
				model = upstreamName
			}
		}
		if model == "" {
			httpx.Fail(c, httpx.WithMessage("该渠道未绑定任何逻辑模型，请先在 AI 插件中绑定模型"))
			return
		}
		saved, err := saveAIConfig(home, AIModelConfig{
			Provider:    "openai",
			BaseURL:     baseURL,
			APIKey:      apiKey,
			AnswerModel: model,
			JudgeModel:  model,
		})
		if err != nil {
			httpx.Fail(c, httpx.WithMessage("接入 AI 渠道失败: "+err.Error()))
			return
		}
		httpx.Success(c, httpx.WithData(gin.H{
			"ai_config": maskAIConfig(saved),
			"ai_check":  checkAIModelConfigWithPlugin(saved),
			"agents":    buildOfficialAgents(saved),
			"applied":   gin.H{"channel_id": req.ChannelID, "base_url": baseURL, "model": model, "models": models},
			"has_key":   strings.TrimSpace(apiKey) != "",
		}))
		return
	case "test_ai":
		aiCfg := mergeAIConfig(loadAIConfig(home), req.AIConfig)
		httpx.Success(c, httpx.WithData(testAIConnection(c.Request.Context(), aiCfg)))
		return
	case "stop":
		evalJobMu.Lock()
		wasRunning := evalJobState.Status == "running" && evalJobState.Phase != "finalizing" && evalJobState.Error == ""
		if wasRunning && evalJobCancel != nil {
			evalJobCancel()
			evalJobCancel = nil
		}
		jobID := evalJobState.JobID
		elapsed := evalJobState.ElapsedSeconds
		activeAIConfig := evalJobAIConfig
		activeReq := evalRunRequest{
			Suite: evalJobState.Suite, Tier: evalJobState.Tier, ArmMode: evalJobState.ArmMode,
			UseAgents: evalJobState.UseAgents, Category: evalJobState.CategoryFilter, RawLimit: evalJobState.RawLimit,
		}
		var itemsCopy []EvalQuestionResultItem
		if wasRunning && req.SavePartial {
			evalRunItemsMu.RLock()
			if evalRunItemsJobID == jobID {
				itemsCopy = append(itemsCopy, evalRunItems...)
			}
			evalRunItemsMu.RUnlock()
		}
		if wasRunning {
			evalJobState.Phase = "finalizing"
			evalJobState.PhaseText = "评测已中止，正在保存结果"
			evalJobState.PhaseTextEn = "Evaluation stopped; saving results"
			appendJobLog("用户手动中止了评测任务。")
			evalJobState.UpdatedAtUnixMS = time.Now().UnixMilli()
		}
		evalJobMu.Unlock()

		var savedRec *EvalRunRecord
		if wasRunning {
			rec, err := finalizeEvalRunRecord(home, jobID, activeReq, activeAIConfig, itemsCopy, elapsed, evalRunPhases{}, true)
			if err != nil {
				httpx.Fail(c, httpx.WithMessage(err.Error()))
				return
			}
			savedRec = &rec
		}
		httpx.Success(c, httpx.WithData(gin.H{
			"job":         snapshotJobStatus(),
			"record":      savedRec,
			"run_history": loadRunHistoryBySuite(activeReq.Suite),
		}))
		return
	case "detail":
		items := loadRunDetailItems(home, dataDir, req.RunID)
		isBaseline := isReferenceBaselineID(req.RunID)
		reason := ""
		if len(items) == 0 {
			if isBaseline {
				reason = "该记录是封印基线，只有聚合读数，没有逐题明细；不能用于逐题 Δ。"
			} else {
				reason = "该运行没有落盘的逐题明细（历史记录或未保存明细的运行）；逐题 Δ 不可用。"
			}
		}
		httpx.Success(c, httpx.WithData(gin.H{
			"run_id":    req.RunID,
			"total":     len(items),
			"available": len(items) > 0,
			"baseline":  isBaseline,
			"reason":    reason,
			"items":     items,
		}))
		return
	case "delete":
		if err := deleteEvalTaskRecord(req.RunID); err != nil {
			httpx.Fail(c, httpx.WithMessage(err.Error()))
			return
		}
		httpx.Success(c, httpx.WithData(gin.H{"run_history": loadRunHistoryBySuite(req.Suite)}))
		return
	case "single":
		if bin == "" {
			httpx.Fail(c, httpx.WithMessage("未找到 rolling-memory 引擎二进制"))
			return
		}
		qText := strings.TrimSpace(req.Question)
		if qText == "" {
			httpx.Fail(c, httpx.WithMessage("question 不能为空"))
			return
		}
		aiCfg := mergeAIConfig(loadAIConfig(home), req.AIConfig)
		if req.UseAgents {
			chk := checkAIModelConfig(aiCfg)
			if !chk.Ready {
				httpx.Fail(c, httpx.WithMessage(chk.Summary))
				return
			}
		}
		armMode, armErr := validateEvalRunRequest(req)
		if armErr != nil {
			httpx.Fail(c, httpx.WithMessage(armErr.Error()))
			return
		}
		qItem := EvalQuestionItem{
			ID:            firstNonEmptyStr(req.QuestionID, "custom-probe"),
			Category:      firstNonEmptyStr(req.Category, "4"),
			Question:      qText,
			Answer:        req.Gold,
			EvidenceTexts: req.EvidenceTexts,
		}
		var resItem EvalQuestionResultItem
		var recallRaw map[string]any
		var err error
		suite := normalizeSuite(req.Suite)
		// scope 取自本 store 的 manifest 世代（每次新评测自动分配独立 scope）；Oracle 臂无评测世代。
		targetScope := evalScopeForSuite(suite)
		storePath := resolveEvalDBPathForSuite(home, suite)
		if armMode != EvalArmOracle {
			manifest, manifestErr := requireEvalManifestForSingle(storePath, suite)
			if manifestErr != nil {
				httpx.Fail(c, httpx.WithMessage(manifestErr.Error()))
				return
			}
			targetScope = manifest.Scope
		}
		var atmUnits map[string]atmEvalUnit
		if suite == SuiteATM {
			atmUnits, err = loadATMEvalUnits(home, dataDir)
			if err != nil {
				httpx.Fail(c, httpx.WithMessage("加载 ATM UnitID 映射失败: "+err.Error()))
				return
			}
		}
		if suite == SuiteATM {
			resItem, recallRaw, err = executeSingleATMQuestionEval(c.Request.Context(), bin, storePath, qItem, 1, armMode, req.RawLimit, req.UseAgents, aiCfg, atmUnits)
		} else {
			resItem, recallRaw, err = executeSingleQuestionEval(c.Request.Context(), bin, storePath, qItem, 1, armMode, req.RawLimit, req.UseAgents, aiCfg)
		}
		if err != nil {
			httpx.Fail(c, httpx.WithMessage(err.Error()))
			return
		}
		httpx.Success(c, httpx.WithData(gin.H{
			"mode": "single", "scope": targetScope,
			"question": qText, "gold": req.Gold,
			"item": resItem, "recall": recallRaw,
		}))
		return
	}

	if bin == "" {
		httpx.Fail(c, httpx.WithMessage("未找到 rolling-memory 引擎二进制"))
		return
	}
	aiCfg := mergeAIConfig(loadAIConfig(home), req.AIConfig)
	if req.UseAgents {
		chk := checkAIModelConfig(aiCfg)
		if !chk.Ready {
			httpx.Fail(c, httpx.WithMessage(chk.Summary))
			return
		}
	}
	snap, err := startStreamingEvalJob(home, req)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	// Action == "start" 供前端 WebSocket 模式异步启动批次并立即返回初始快照，后续进度由 rm/eval 房间推送。
	if strings.EqualFold(strings.TrimSpace(req.Action), "start") {
		httpx.Success(c, httpx.WithData(gin.H{
			"mode":   "batch",
			"scope":  currentEvalScope(resolveEvalDBPathForSuite(home, req.Suite), req.Suite),
			"job":    snap,
			"record": snap.Record,
		}))
		return
	}
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case <-time.After(150 * time.Millisecond):
		}
		cur := snapshotJobStatus()
		if cur.JobID != snap.JobID || cur.Status == "completed" || cur.Status == "cancelled" || cur.Status == "failed" {
			if cur.Status == "failed" {
				httpx.Fail(c, httpx.WithMessage(cur.Error))
				return
			}
			httpx.Success(c, httpx.WithData(gin.H{
				"mode": "batch", "scope": currentEvalScope(resolveEvalDBPathForSuite(home, req.Suite), req.Suite),
				"job": cur, "record": cur.Record,
				"run_history": loadRunHistoryBySuite(req.Suite),
			}))
			return
		}
	}
}

const (
	// SuiteLoCoMo 第一套默认测评体系（LoCoMo ACL 2024）
	SuiteLoCoMo = "locomo"
	// SuiteATM 第二套官方测评体系（剑桥大学 ATM-Bench 多模态个人记忆基准）
	SuiteATM = "atm"

	// ATMEvalScope ATM-Bench 云端评测专属作用域，与真实用户数据物理隔离。
	ATMEvalScope = "u10-mem-eval-atmbench"

	// ATMProtocolID 剑桥大学 ATM-Bench 官方协议标识
	ATMProtocolID = "atmbench-official-v1"

	// ATMSystemPrompt 官方 MMRag 答题系统提示词
	ATMSystemPrompt = "You are a memory QA assistant. Use ONLY the provided evidence to answer. " +
		"If the evidence is insufficient, answer 'Unknown'. Respond with only the answer. " +
		"If the question asks to recall or list items (photos/emails/videos), respond with the corresponding evidence IDs only, comma-separated, with no extra text."

	// ATMEvidencePromptTPL 官方 MMRag 答题 user prompt 模板
	ATMEvidencePromptTPL = "Question: {question}\n\nEvidence:\n{evidence}\n\nProvide the answer based solely on the evidence."

	// ATMJudgePromptTPL 官方 LLM_JUDGE_PROMPT 评判提示词模板
	ATMJudgePromptTPL = "You are an evaluator, and you are given a task to evaluate a model predictions " +
		"with a given question. Let's follow the instructions step by step to make a " +
		"judgement.  " +
		"1. As the first step, you need to check whether the prediction was really " +
		"answering the question.  " +
		"2. If the model prediction does provide a meaningful answer, judge whether " +
		"the model Prediction matches the ground truth answer by reasoning according " +
		"to the following steps:  " +
		"2.1: Always assume the ground truth is correct.  " +
		"2.2: Pay attention to theses special cases:  " +
		"a. If the ground truth answer contains numbers, the value of \"accuracy\" " +
		"is true only if numbers in ground truth and numbers in model predictions " +
		"match very well; in case of math questions, \"accuracy\" is true only if " +
		"the numbers in model predictions EXACTLY matches the numbers in ground truth;  " +
		"b. If the ground truth answer contains time, and/or time range, \"accuracy\" " +
		"is \"true\" only if if times and time ranges in ground truth and model " +
		"predictions match very well.  " +
		"c. If the ground truth answer contains a set of objects, \"accuracy\" is " +
		"\"true\" if the model prediction covers most of the objects in the ground truth; " +
		"however, \"accuracy\" if \"false\" if the  model prediction has a lot of objects " +
		"that are not in the ground truth.  " +
		"d. If the ground truth is something similar to \"I don't know\", \"accuracy\" " +
		"is \"true\" only if the model prediction also implies the similar thing.  " +
		"2.3: Even if the prediction statement is reasonable, if it conflicts with or does " +
		"not match the ground truth, \"accuracy\" should be \"false\".  " +
		"2.4. \"Accuracy\" is true if the ground truth information is covered by the prediction. " +
		"The prediction is allowed to provide more information but should not be against " +
		"the ground truth. If it is hard to  decide whether the prediction matches ground " +
		"truth, \"accuracy\" should be \"false\".  " +
		"Think step by step following the instructions above, and then make a judgment. " +
		"Respond with only a single JSON blob with an \"explanation\" field that has your " +
		"short(less than 100 word) reasoning  steps and an \"accuracy\" field which is " +
		"\"true\" or \"false\".  " +
		"Question: {{question}}  " +
		"Ground truth: {{answer}}  " +
		"Prediction: {{prediction}}"

	PinATMSystemSHA1         = "a6b5c7dc183ecd842460536037e83bfdd7ea9c33"
	PinATMEvidencePromptSHA1 = "d10f252e6a131025696100551493932d8377b24c"
	PinATMJudgeTPLSHA1       = "324ac8a5a9f3b8b3a1a87be370bf9dfbf8da91f1"

	// ATMOracleHardQSScore ATM-Bench-Hard 31 题「金标证据直投」的官方混合 QS 上限（Oracle 行 QSScore 唯一真源）。
	// 引用面：① buildATMBenchLeaderboard() 的 Hard Oracle 行；② EvalOverviewResponse.ATMOracleQSCeiling
	// （评测页图表 ATM-Hard 金标上限线，替代旧硬编码 54.8%）；③ atmLeaderboard.vue 的 kpi4Sub {qs} 占位。
	// 禁在任何消费面另写裸数字（读数出处见 Oracle 行 Note：Σmin(10,gold)/gold/31 = 96.47% 结构性 R@10 上限）。
	ATMOracleHardQSScore = 46.69
)

// ATMBenchEntry 剑桥大学 ATM-Bench 排行榜单行（含 Tab 1 主榜与 Tab 2 ATM-Bench-Hard 榜）。
type ATMBenchEntry struct {
	Rank           int     `json:"rank"`
	Board          string  `json:"board"`    // "hard" | "main"
	Category       string  `json:"category"` // "Memory" | "RAG" | "Oracle"
	System         string  `json:"system"`
	SystemEn       string  `json:"system_en"`
	Vendor         string  `json:"vendor"`
	VendorEn       string  `json:"vendor_en"`
	Architecture   string  `json:"architecture"`
	ArchitectureEn string  `json:"architecture_en"`
	IsOurs         bool    `json:"is_ours"`
	RecallAt10     float64 `json:"recall_at_10"`
	RecallAt25     float64 `json:"recall_at_25"`
	RecallAt50     float64 `json:"recall_at_50"`
	QSScore        float64 `json:"qs_score"`
	ListRecall     float64 `json:"list_recall"`
	NumberEM       float64 `json:"number_em"`
	OpenEnd        float64 `json:"open_end"`
	IndexTimeHr    string  `json:"index_time_hr"`
	HighlightTag   string  `json:"highlight_tag"`
	HighlightTagEn string  `json:"highlight_tag_en"`
	Note           string  `json:"note"`
	NoteEn         string  `json:"note_en"`
}

// ATMBenchBoard 剑桥大学 ATM-Bench 多模态个人记忆基准看板。
type ATMBenchBoard struct {
	BenchmarkName   string          `json:"benchmark_name"`
	BenchmarkNameEn string          `json:"benchmark_name_en"`
	CorpusSummary   string          `json:"corpus_summary"`
	CorpusSummaryEn string          `json:"corpus_summary_en"`
	ReproCommand    string          `json:"repro_command"`
	HardEntries     []ATMBenchEntry `json:"hard_entries"`
	MainEntries     []ATMBenchEntry `json:"main_entries"`
}

var (
	reATMSubjectLine = regexp.MustCompile(`(?m)^Subject:\s*(.+)$`)
	reATMMediaID     = regexp.MustCompile(`\b(\d{8}_\d{6})\b`)
	reATMEmailID     = regexp.MustCompile(`(?i)\b(email\d+)\b`)
	reATMListSep     = regexp.MustCompile(`[,;\n/]+`)
	reATMNumberToken = regexp.MustCompile(`[-+]?\d+(?:\.\d+)?`)
)

func normalizeSuite(suite string) string {
	switch strings.ToLower(strings.TrimSpace(suite)) {
	case "atm", "atmbench", "atm-bench":
		return SuiteATM
	default:
		return SuiteLoCoMo
	}
}

// normalizeATMTier 归一化 admin 侧 ATM 官方档位（hard | 1013）。
// 已废止的自造 100 题前缀档返回空串，调用方 fail-closed（禁止静默降级为 hard）。
func normalizeATMTier(tier string) string {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "1013", "full", "main":
		return "1013"
	case "100", "main-100", "main100", "atm-100", "atm100":
		return ""
	default:
		return "hard"
	}
}

func atmPinStatus(verified bool, err error) string {
	if verified {
		return "verified"
	}
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return "unverified"
	}
	return "failed"
}

func atmFixturePinStatus(dir, name string, verified bool) string {
	if verified {
		return "verified"
	}
	if dir == "" {
		return "unverified"
	}
	_, err := os.Stat(filepath.Join(dir, name))
	if err == nil {
		return "failed"
	}
	return atmPinStatus(false, err)
}

func applyATMVerificationStatus(spec ProtocolParitySpec, pins ATMOfficialPinStatus, atmDir string) ProtocolParitySpec {
	spec.PromptPinsStatus = "failed"
	if pins.PromptPinsVerified {
		spec.PromptPinsStatus = "verified"
	}
	fixtureDir := atmOfficialFixtureDirAdmin(atmDir)
	spec.ScoringFixtureStatus = atmFixturePinStatus(fixtureDir, "atm_official_scoring_vectors.json", pins.FixtureVerified)
	spec.AnswerFixtureStatus = atmFixturePinStatus(fixtureDir, "atm_official_answer_payload_vectors.json", pins.AnswerPayloadVerified)
	spec.CorpusPinsStatus = "unverified"
	if pins.CorpusPinsVerified {
		spec.CorpusPinsStatus = "verified"
	} else if pins.CorpusPinsError != "" {
		spec.CorpusPinsStatus = "failed"
		if strings.HasSuffix(pins.CorpusPinsError, ": "+syscall.ENOENT.Error()) || pins.CorpusPinsError == "ATM-Bench 数据目录未解析" {
			spec.CorpusPinsStatus = "unverified"
		}
	}
	spec.AllPinsVerified = spec.PromptPinsStatus == "verified" && spec.ScoringFixtureStatus == "verified" &&
		spec.AnswerFixtureStatus == "verified" && spec.CorpusPinsStatus == "verified"
	return spec
}

func buildATMProtocolParitySpec() ProtocolParitySpec {
	sysSHA := sha1Hex(ATMSystemPrompt)
	jdgSHA := sha1Hex(ATMJudgePromptTPL)
	promptStatus := "verified"
	if verifyATMOfficialProtocolAdmin() != nil {
		promptStatus = "failed"
	}
	return ProtocolParitySpec{
		BuiltinProtocol:        "atmbench-official",
		ProtocolID:             ATMProtocolID,
		LeaderboardName:        "剑桥大学 ATM-Bench 多模态个人记忆排行榜 (University of Cambridge ATM-Bench Leaderboard)",
		LeaderboardNameEn:      "University of Cambridge ATM-Bench Multimodal Personal Memory Leaderboard",
		CorpusName:             "SGM 多模态个人指称记忆语料（4 年跨度 · 6,742 封邮件 + 3,759 张照片 + 533 段视频 = 11,034 条记忆 · 1,013 道主榜题 + 31 道 Hard 高难题）",
		CorpusNameEn:           "SGM Multimodal Personal Memory Corpus (4-year span · 6,742 emails + 3,759 photos + 533 videos = 11,034 items · 1,013 Main QAs + 31 Hard QAs)",
		AnswerSystemPrompt:     ATMSystemPrompt,
		AnswerSystemSHA1:       sysSHA,
		AnswerPayloadSpec:      "Evidence 1..K (Top-10 召回条目完整 ID/Timestamp/Location/Caption/OCR/Tags/Detail) + 原题直问",
		AnswerPayloadSpecEn:    "Evidence 1..K (Top-10 retrieved items with ID/Timestamp/Location/Caption/OCR/Tags/Detail) + original question",
		AnswerPayloadSHA1:      PinATMEvidencePromptSHA1,
		JudgeSystemPrompt:      "(Single-turn user prompt with step-by-step rubric)",
		JudgeSystemSHA1:        "-",
		JudgeTemplateSHA1:      jdgSHA,
		JudgeTemplateBytes:     len(ATMJudgePromptTPL),
		JudgeVerdictSpec:       `官方混合 QS (Quality Score) 评分：Cat 1 (list_recall) = 确定性证据 ID 集合 Jaccard · Cat 2 (number) = 官方 deterministic_accuracy（日期/时间/货币/相对日期归一） · Cat 3 (open_end) = LLM Judge JSON {"explanation", "accuracy": "true"|"false"}`,
		JudgeVerdictSpecEn:     `Official Hybrid QS (Quality Score): Cat 1 (list_recall) = Deterministic Evidence ID Jaccard · Cat 2 (number) = official deterministic_accuracy (date/time/currency/relative-date normalized) · Cat 3 (open_end) = LLM Judge JSON {"explanation", "accuracy": "true"|"false"}`,
		NumberScorer:           "ATM-Bench official deterministic_accuracy (upstream evaluate_qa.py::_deterministic_accuracy_core)",
		NumberScorerVersion:    ATMOfficialScoringVersion,
		ListScorer:             "ATM-Bench official list_jaccard_score (upstream evaluate_qa.py::_list_jaccard_core)",
		ListScorerVersion:      ATMOfficialScoringVersion,
		JudgeParser:            "ATM-Bench official parse_judge_response (JSON first; upstream lenient fallback; no JUDGE_UNPARSED)",
		ScoringFixtureSHA256:   PinATMOfficialScoringFixtureSHA256,
		EvaluatorSourcePins:    atmOfficialSourceSHA1s(),
		CorpusPins:             atmOfficialCorpusSHA256s(),
		OfficialScoringParity:  "unverified",
		PromptPinsStatus:       promptStatus,
		RecallMetricSpec:       "官方 Recall@10 = 逐题金标证据覆盖率的平均值；Any-Hit@10（至少命中一条）与 Recall@kGT（全部命中）为辅助指标。",
		RecallMetricSpecEn:     "Official Recall@10 is the mean per-question gold evidence coverage; Any-Hit@10 (at least one hit) and Recall@kGT (all evidence hit) are auxiliary metrics.",
		CategoryMappingNote:    "Cat 1 = list_recall (证据ID枚举 · Jaccard) · Cat 2 = number (数值/日期 · 确定性 EM) · Cat 3 = open_end (开放问答 · LLM Judge)",
		CategoryMappingNoteEn:  "Cat 1 = list_recall (Evidence ID List · Jaccard) · Cat 2 = number (Numeric/Date · Deterministic EM) · Cat 3 = open_end (Open-Ended · LLM Judge)",
		FairComparisonNotice:   "用户评测使用隔离作用域；历史参考读数不是当前用户的运行结果。提示词摘要、外部语料摘要与官方判分对拍分别核验；没有对拍证据时不得据此宣称评分等价。",
		FairComparisonNoticeEn: "User evaluations use an isolated scope; historical reference readings are not the current user's results. Prompt hashes, external corpus hashes, and official scorer parity are verified separately; scoring equivalence is not claimed without parity evidence.",
	}
}

func buildATMOfficialAgents(cfg AIModelConfig) []OfficialProtocolAgent {
	sysSHA := sha1Hex(ATMSystemPrompt)
	jdgSHA := sha1Hex(ATMJudgePromptTPL)
	return []OfficialProtocolAgent{
		{
			Code:            "atm-answer",
			Name:            "ATM-Bench 官方多模态记忆答题智能体 (MMRag Answerer)",
			NameEn:          "ATM-Bench Official MMRag Answer Agent",
			Role:            "answer",
			Protocol:        ATMProtocolID,
			Model:           cfg.AnswerModel,
			SystemPrompt:    ATMSystemPrompt,
			SystemSHA1:      sysSHA,
			UserTemplate:    ATMEvidencePromptTPL,
			UserTemplateSHA: PinATMEvidencePromptSHA1,
			PinVerified:     sysSHA == PinATMSystemSHA1 && sha1Hex(ATMEvidencePromptTPL) == PinATMEvidencePromptSHA1,
		},
		{
			Code:            "atm-judge",
			Name:            "ATM-Bench 官方混合 QS 判官智能体 (Hybrid QS Judge)",
			NameEn:          "ATM-Bench Official Hybrid QS Judge Agent",
			Role:            "judge",
			Protocol:        ATMProtocolID,
			Model:           cfg.JudgeModel,
			SystemPrompt:    "(Embedded in user prompt per official evaluate_qa spec)",
			SystemSHA1:      "-",
			UserTemplate:    ATMJudgePromptTPL,
			UserTemplateSHA: jdgSHA,
			PinVerified:     jdgSHA == PinATMJudgeTPLSHA1,
		},
	}
}

func resolveATMDataDir(home, baseEvalDataDir string) string {
	candidates := []string{
		filepath.Join(baseEvalDataDir, "atmbench"),
		filepath.Join(home, ".local", "share", "rolling-memory", "evaldata", "atmbench"),
		filepath.Join(home, "Rolling-Memory", "evaldata", "atmbench"),
		filepath.Join(home, "rolling-memory", "evaldata", "atmbench"),
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(wd, "evaldata", "atmbench"),
			filepath.Join(wd, "..", "evaldata", "atmbench"),
			filepath.Join(wd, "..", "..", "evaldata", "atmbench"),
			filepath.Join(wd, "..", "..", "..", "evaldata", "atmbench"),
		)
	}
	candidates = append(candidates, "/tmp/atmbench_workspace/data")
	for _, cand := range candidates {
		if info, err := os.Stat(filepath.Join(cand, "MANIFEST.json")); err == nil && !info.IsDir() {
			return cand
		}
		if info, err := os.Stat(filepath.Join(cand, "atm-bench", "atm-bench-hard.json")); err == nil && !info.IsDir() {
			return cand
		}
	}
	return filepath.Join(baseEvalDataDir, "atmbench")
}

func mapATMQTypeToCategory(qtype string) string {
	switch strings.ToLower(strings.TrimSpace(qtype)) {
	case "list_recall", "1":
		return "1"
	case "number", "2":
		return "2"
	default:
		return "3"
	}
}

func loadATMTierQuestions(baseEvalDataDir, home, tier string) ([]EvalQuestionItem, error) {
	atmDir := resolveATMDataDir(home, baseEvalDataDir)
	normTier := normalizeATMTier(tier)
	if normTier == "" {
		return nil, fmt.Errorf("%w", ErrATMTierRetiredAdmin)
	}
	candidates := []string{
		filepath.Join(atmDir, "tiers", "questions-"+normTier+".json"),
	}
	if normTier == "hard" {
		candidates = append(candidates, filepath.Join(atmDir, "atm-bench", "atm-bench-hard.json"))
	} else {
		candidates = append(candidates, filepath.Join(atmDir, "atm-bench", "atm-bench.json"))
	}
	var raw []byte
	var err error
	for _, p := range candidates {
		raw, err = os.ReadFile(p)
		if err == nil {
			break
		}
	}
	if err != nil {
		return nil, err
	}
	var rawList []map[string]any
	if err := json.Unmarshal(raw, &rawList); err != nil {
		return nil, err
	}
	items := make([]EvalQuestionItem, 0, len(rawList))
	for _, q := range rawList {
		evList := []string{}
		if rawEv, ok := q["evidence_ids"].([]any); ok {
			for _, e := range rawEv {
				if s, isStr := e.(string); isStr && s != "" {
					evList = append(evList, s)
				}
			}
		}
		qtype := asString(q["qtype"])
		items = append(items, EvalQuestionItem{
			ID:            asString(q["id"]),
			SampleID:      qtype,
			Category:      mapATMQTypeToCategory(qtype),
			Question:      asString(q["question"]),
			Answer:        asString(q["answer"]),
			EvidenceTexts: evList,
		})
	}
	return items, nil
}

// atmEvalUnit 是 ATM 官方语料的本地只读映射，用于校验云端 UnitID 与 Oracle 金标交付。
type atmEvalUnit struct {
	ID         string   `json:"id"`
	Modality   string   `json:"m"`
	Timestamp  string   `json:"ts,omitempty"`
	OccurredAt int64    `json:"occ,omitempty"`
	Location   string   `json:"loc,omitempty"`
	City       string   `json:"city,omitempty"`
	ShortCap   string   `json:"sc,omitempty"`
	Caption    string   `json:"cap,omitempty"`
	OCRText    string   `json:"ocr,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	Subject    string   `json:"subj,omitempty"`
	Summary    string   `json:"sum,omitempty"`
	Detail     string   `json:"det,omitempty"`
	Text       string   `json:"-"`
}

var atmEvalUnitsCache sync.Map

func hydrateATMEvalUnit(unit *atmEvalUnit) {
	if unit.Modality == "email" {
		if unit.Subject == "" {
			if match := reATMSubjectLine.FindStringSubmatch(unit.Detail); len(match) == 2 {
				unit.Subject = strings.TrimSpace(match[1])
			}
		}
		unit.Text = strings.Join([]string{
			"ID: " + unit.ID,
			"Timestamp: " + unit.Timestamp,
			"Summary: " + unit.Summary,
			"Detail: " + unit.Detail,
		}, "\n")
		return
	}
	unit.Text = strings.Join([]string{
		"ID: " + unit.ID,
		"Type: " + unit.Modality,
		"Timestamp: " + unit.Timestamp,
		"Location: " + unit.Location,
		"Short Caption: " + unit.ShortCap,
		"Caption: " + unit.Caption,
		"OCR: " + unit.OCRText,
		"Tags: " + strings.Join(unit.Tags, ", "),
	}, "\n")
}

func loadATMEvalUnits(home, baseEvalDataDir string) (map[string]atmEvalUnit, error) {
	atmDir := resolveATMDataDir(home, baseEvalDataDir)
	if err := verifyATMCorpusPinsAdmin(atmDir); err != nil {
		return nil, err
	}
	if cached, ok := atmEvalUnitsCache.Load(atmDir); ok {
		return cached.(map[string]atmEvalUnit), nil
	}
	units := make(map[string]atmEvalUnit, 11034)
	gzPath := filepath.Join(atmDir, "corpus.jsonl.gz")
	if info, err := os.Stat(gzPath); err == nil && !info.IsDir() {
		file, err := os.Open(gzPath)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		reader, err := gzip.NewReader(file)
		if err != nil {
			return nil, err
		}
		defer reader.Close()
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var unit atmEvalUnit
			if err := json.Unmarshal([]byte(line), &unit); err != nil {
				return nil, err
			}
			if strings.TrimSpace(unit.ID) == "" {
				continue
			}
			hydrateATMEvalUnit(&unit)
			units[unit.ID] = unit
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
	} else {
		readList := func(relative string) ([]map[string]any, error) {
			raw, err := os.ReadFile(filepath.Join(atmDir, relative))
			if err != nil {
				return nil, err
			}
			var entries []map[string]any
			if err := json.Unmarshal(raw, &entries); err != nil {
				return nil, err
			}
			return entries, nil
		}
		emails, err := readList("raw_memory/email/emails.json")
		if err != nil {
			return nil, err
		}
		images, err := readList("processed_memory/image_batch_results.json")
		if err != nil {
			return nil, err
		}
		videos, err := readList("processed_memory/video_batch_results.json")
		if err != nil {
			return nil, err
		}
		for _, entry := range emails {
			unit := atmEvalUnit{
				ID: asString(entry["id"]), Modality: "email", Timestamp: asString(entry["timestamp"]),
				Subject: asString(entry["subject"]), Summary: asString(entry["short_summary"]), Detail: asString(entry["detail"]),
			}
			if unit.ID != "" {
				hydrateATMEvalUnit(&unit)
				units[unit.ID] = unit
			}
		}
		addMedia := func(entries []map[string]any, pathKey, modality string) {
			for _, entry := range entries {
				rawPath := asString(entry[pathKey])
				if rawPath == "" {
					continue
				}
				base := filepath.Base(rawPath)
				id := strings.TrimSuffix(base, filepath.Ext(base))
				if id == "" {
					continue
				}
				unit := atmEvalUnit{
					ID: id, Modality: modality, Timestamp: asString(entry["timestamp"]),
					Location: asString(entry["location_name"]), City: asString(entry["city"]),
					ShortCap: asString(entry["short_caption"]), Caption: asString(entry["caption"]), OCRText: asString(entry["ocr_text"]),
				}
				if tags, ok := entry["tags"].([]any); ok {
					for _, tag := range tags {
						if value := asString(tag); value != "" {
							unit.Tags = append(unit.Tags, value)
						}
					}
				}
				hydrateATMEvalUnit(&unit)
				units[id] = unit
			}
		}
		addMedia(images, "image_path", "image")
		addMedia(videos, "video_path", "video")
	}
	if len(units) == 0 {
		return nil, errors.New("ATM 官方语料没有可用 UnitID")
	}
	atmEvalUnitsCache.Store(atmDir, units)
	return units, nil
}

// computeATMListJaccardAdmin 逐字委托官方 list_jaccard_score（由 sync 脚本与
// internal/eval/atmofficial_scoring.go 保持同源）。
func computeATMListJaccardAdmin(groundTruth, prediction string) float64 {
	return ATMOfficialListJaccard(groundTruth, prediction)
}

// normalizeAnswerAdmin 复刻 CLI internal/eval/scoring.go::NormalizeAnswer：
// 小写、连续表意/音节文字按单字切分、仅保留字母/数字/空白、丢弃 SQuAD 冠词。
// ATM number 题与 CLI 必须同尺，否则同一答案在两端会得到不同判分。
func normalizeAnswerAdmin(text string) string {
	value := strings.ToLower(strings.TrimSpace(text))
	var cleaned strings.Builder
	for _, char := range value {
		if isContinuousSyllabicOrIdeographicAdmin(char) {
			cleaned.WriteByte(' ')
			cleaned.WriteRune(char)
			cleaned.WriteByte(' ')
			continue
		}
		if unicode.IsLetter(char) || unicode.IsNumber(char) || isPySpaceAdmin(char) {
			cleaned.WriteRune(char)
			continue
		}
		cleaned.WriteByte(' ')
	}
	words := make([]string, 0, 8)
	for _, word := range strings.Fields(cleaned.String()) {
		switch word {
		case "a", "an", "the":
			continue
		}
		words = append(words, word)
	}
	return strings.Join(words, " ")
}

func isContinuousSyllabicOrIdeographicAdmin(char rune) bool {
	switch {
	case char >= 0x4E00 && char <= 0x9FFF,
		char >= 0x3400 && char <= 0x4DBF,
		char >= 0xF900 && char <= 0xFAFF,
		char >= 0x20000 && char <= 0x2FA1F,
		char >= 0x3040 && char <= 0x30FF, // kana
		char >= 0xAC00 && char <= 0xD7AF, // hangul syllables
		char >= 0x0E00 && char <= 0x0EFF, // thai
		char >= 0x1780 && char <= 0x17FF: // khmer
		return true
	}
	return false
}

func isPySpaceAdmin(char rune) bool {
	return unicode.IsSpace(char) || (char >= '\u001c' && char <= '\u001f')
}

// computeATMNumberEMAdmin 逐字委托官方 _deterministic_accuracy_core（无 question）。
// Deprecated: 评测路径必须调用 computeATMNumberEMAdminWithQuestion 以解析相对日期。
func computeATMNumberEMAdmin(groundTruth, prediction string) (bool, string) {
	return computeATMNumberEMAdminWithQuestion(groundTruth, prediction, "")
}

// computeATMNumberEMAdminWithQuestion 逐字委托官方 number 确定性判分。
func computeATMNumberEMAdminWithQuestion(groundTruth, prediction, question string) (bool, string) {
	return ATMOfficialNumberAccuracy(groundTruth, prediction, question)
}

// parseATMJudgeOutputAdmin 逐字委托官方 parse_judge_response：JSON 优先，
// JSON 失败/缺 accuracy 时按官方宽松回退；官方没有 JUDGE_UNPARSED 语义。
func parseATMJudgeOutputAdmin(raw string) (string, string, error) {
	acc, exp, _ := ATMOfficialParseJudgeResponse(raw)
	if acc {
		return "correct", exp, nil
	}
	return "wrong", exp, nil
}

// atmAnswerDeliveryK 是 ATM-Bench 官方答题交付窗口（retrieve_k = 10，与
// internal/eval/atmbench.go 的 retrieveK 同口径）。rawLimit 只控制检索深度（候选池），
// 不是交给答题器的证据条数；把整个候选池塞进答题材料会让 QS 因多给证据而虚高。
const atmAnswerDeliveryK = 10

// atmDeliveryWindow 返回答题交付窗口：产品臂恒为 10；Oracle 臂是金标上限诊断，
// 按 rawLimit 直投全部金标证据（官方披露口径），不参与产品臂对比。
func atmDeliveryWindow(armMode string, rawLimit int) int {
	if armMode == EvalArmOracle {
		return rawLimit
	}
	return atmAnswerDeliveryK
}

// countATMGoldHits counts the official gold-set-mean R@k hits for one question:
// a gold Unit that appears several times in the delivery sequence is counted
// once, at its first rank, so the numerator can never exceed the gold-set
// cardinality (a duplicated gold list used to push R@10 to 2.0 on the Oracle
// arm). retRank keeps each Unit's first delivery rank for the match detail rows.
func countATMGoldHits(gtSet map[string]bool, retIDs []string) (hits10, hits20, hits50 int, retRank map[string]int) {
	retRank = map[string]int{}
	seen := make(map[string]bool, len(retIDs))
	for i, id := range retIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		retRank[id] = i + 1
		if !gtSet[id] {
			continue
		}
		// R@10 是排序名次诊断，与交付窗口无关（Oracle 臂交付全量也照算 Top-10）。
		if i < 10 {
			hits10++
		}
		if i < 20 {
			hits20++
		}
		if i < 50 {
			hits50++
		}
	}
	return hits10, hits20, hits50, retRank
}

// executeSingleATMQuestionEval 对单道 ATM-Bench 题执行严格云端召回或本地金标 Oracle，并进行官方混合 QS 判分。
func executeSingleATMQuestionEval(ctx context.Context, bin, storePath string, q EvalQuestionItem, index int, armMode string, rawLimit int, useAgents bool, aiCfg AIModelConfig, atmUnits map[string]atmEvalUnit) (EvalQuestionResultItem, map[string]any, error) {
	normalizedArm, err := normalizeEvalArm(armMode)
	if err != nil {
		return EvalQuestionResultItem{}, nil, err
	}
	armMode = normalizedArm
	if rawLimit <= 0 {
		rawLimit = 50
	}
	deliveryK := atmDeliveryWindow(armMode, rawLimit)
	totalStart := time.Now()
	resItem := EvalQuestionResultItem{
		Index: index, ID: q.ID, SampleID: q.SampleID, Category: q.Category,
		Question: q.Question, Gold: q.Answer, EvidenceTexts: q.EvidenceTexts,
	}

	recallStart := time.Now()
	var recallRaw map[string]any
	var retIDs []string
	if armMode == EvalArmOracle {
		retIDs = append(retIDs, q.EvidenceTexts...)
		rawHits := make([]any, 0, len(retIDs))
		for _, unitID := range retIDs {
			unit, ok := atmUnits[unitID]
			if !ok {
				return resItem, nil, fmt.Errorf("Oracle 金标包含未知 ATM UnitID %q", unitID)
			}
			rawHits = append(rawHits, map[string]any{
				"unit_id": unitID, "session": unitID, "trace": unitID, "text": unit.Text,
			})
		}
		recallRaw = map[string]any{
			"mode": "oracle", "scope": evalScopeForSuite(SuiteATM), "raw_hits": rawHits,
			"retrieval_unit_ids": retIDs, "formatted": []string{},
		}
	} else {
		strictRaw, unitIDs, err := runLocalEvalRecall(ctx, bin, storePath, SuiteATM, q.Question, rawLimit, atmUnits)
		if err != nil {
			return resItem, nil, err
		}
		recallRaw = strictRaw
		retIDs = unitIDs
	}
	resItem.RecallMS = time.Since(recallStart).Milliseconds()
	if armMode == EvalArmOracle {
		markOracleProvenance(&resItem, evalScopeForSuite(SuiteATM), len(retIDs), storePath)
	} else {
		applyRecallProvenance(&resItem, recallRaw, retIDs, storePath)
	}

	var formattedItems []string
	var evidenceChunks []string
	var bandLines []string

	if bands, ok := recallRaw["bands"].([]any); ok {
		resItem.Bands = len(bands)
		for _, b := range bands {
			bm, _ := b.(map[string]any)
			if nodes, ok := bm["nodes"].([]any); ok {
				resItem.Nodes += len(nodes)
				for _, n := range nodes {
					nm, _ := n.(map[string]any)
					rendered := strings.TrimSpace(asString(nm["content"]))
					if rendered != "" {
						bandLines = append(bandLines, fmt.Sprintf("%d. %s", len(bandLines)+1, rendered))
					}
				}
			}
		}
	}

	if rHits, ok := recallRaw["raw_hits"].([]any); ok {
		resItem.RawHits = len(rHits)
		for idx, h := range rHits {
			m, ok := h.(map[string]any)
			if !ok {
				continue
			}
			sess := strings.TrimSpace(asString(m["unit_id"]))
			txt := strings.TrimSpace(asString(m["text"]))
			if txt == "" {
				txt = strings.TrimSpace(atmUnits[sess].Text)
			}
			if txt != "" {
				oneLine := strings.ReplaceAll(txt, "\n", " | ")
				line := fmt.Sprintf("%d. %s · %s", idx+1, sess, oneLine)
				if len(formattedItems) < deliveryK {
					formattedItems = append(formattedItems, line)
					evidenceChunks = append(evidenceChunks, fmt.Sprintf("Evidence %d:\n%s", len(evidenceChunks)+1, txt))
				}
			}
		}
	}

	resItem.BandItems = bandLines
	// RawItems 是「交付面」原文行（与 FormattedItems 同集合，LoCoMo 路径同语义）；
	// 检索深度由 RawHits / RetrievalUnitIDs 单独表达，不能把未交付候选混进交付面。
	resItem.RawItems = formattedItems
	resItem.FormattedItems = formattedItems
	resItem.BandsDelivered = resItem.Bands
	resItem.NodesDelivered = len(bandLines)
	resItem.RawDelivered = len(formattedItems)
	resItem.DeliveredItems = len(formattedItems)
	resItem.ContextOrdering = "raw_first"

	fullContext := strings.Join(evidenceChunks, "\n")
	resItem.ContextChars = len([]rune(fullContext))
	resItem.DeliveredTokens = countDeliveredTokens(recallRaw, len(formattedItems), fullContext)
	previewRunes := []rune(fullContext)
	if len(previewRunes) > evalPreviewCharCap {
		resItem.ContextPreview = string(previewRunes[:evalPreviewCharCap]) + " …"
	} else {
		resItem.ContextPreview = fullContext
	}

	// 计算 ATM-Bench R@10 与 R@50 金标证据 ID 命中率
	gtSet := map[string]bool{}
	for _, eid := range q.EvidenceTexts {
		gtSet[eid] = true
	}
	hits10, hits20, hits50, retRank := countATMGoldHits(gtSet, retIDs)
	matches := make([]EvidenceMatchItem, 0, len(q.EvidenceTexts))
	// 官方 gold-set-mean 口径（逐题）：分母取去重金标集合，R@k = |set(GT)∩set(Top-k)| / |set(GT)|；
	// 同一 Unit 在序列里重复出现只按其首次名次计一次，重复 ID 不得把分子抬过集合交的基数
	// （Oracle 臂曾因重复金标算得 R@10=2.0）。Any/All-Hit@10 为同窗口二值判定。
	resItem.MetricsAvailable = copyEvalMetricAvailability(nil)
	for _, key := range []string{"avg_recall_ms", "p95_recall_ms", "avg_delivered_items", "avg_delivered_tokens", "total_delivered_tokens"} {
		resItem.MetricsAvailable[key] = true
	}
	if len(gtSet) > 0 {
		for _, key := range []string{"evidence_hit", "r10", "r20", "r50", "any_hit_10", "all_hit_10", "answer_in_context"} {
			resItem.MetricsAvailable[key] = true
		}
		resItem.R10 = roundFloat(float64(hits10)/float64(len(gtSet)), 4)
		resItem.R20 = roundFloat(float64(hits20)/float64(len(gtSet)), 4)
		resItem.R50 = roundFloat(float64(hits50)/float64(len(gtSet)), 4)
		resItem.AnyHit10 = hits10 > 0
		resItem.AllHit10 = hits10 == len(gtSet)
	}
	for _, eid := range q.EvidenceTexts {
		rk := retRank[eid]
		sc := 0.0
		if rk > 0 && rk <= deliveryK {
			sc = 1.0
		} else if rk > deliveryK && rk <= rawLimit {
			// 检索到但未交付：只作诊断信号，不计入交付命中。
			sc = 0.5
		}
		matches = append(matches, EvidenceMatchItem{
			Text:        eid,
			TokenRecall: sc,
			Hit:         rk > 0 && rk <= deliveryK,
		})
	}
	if len(q.EvidenceTexts) > 0 {
		// 交付口径：EvidenceHit / AnswerInContext / BestEvidenceRecall 一律按实际送入
		// 答题器的 deliveryK 条证据计算；GoldTokenRecall 的检索深度诊断对齐官方 R@20 窗口
		// （旧 R@25 为非官方窗口，已回收——官方评测窗口只有 10/20/50）。
		// 分母与分子同口径（去重金标集合）：重复金标不得把召回率算成 50% 或 200%。
		goldTotal := len(gtSet)
		if goldTotal == 0 {
			goldTotal = len(q.EvidenceTexts)
		}
		resItem.BestEvidenceRecall = roundFloat(float64(hits10)/float64(goldTotal), 4)
		resItem.BestHeadEvidenceRecall = roundFloat(float64(hits10)/float64(goldTotal), 4)
		resItem.HeadEvidenceHit = hits10 > 0
		resItem.GoldTokenRecall = roundFloat(float64(hits20)/float64(goldTotal), 4)
		resItem.EvidenceHit = hits10 > 0
		resItem.AnswerInContext = hits10 > 0
	}
	resItem.EvidenceMatches = matches

	if useAgents {
		userPayload := strings.NewReplacer("{question}", q.Question, "{evidence}", fullContext).Replace(ATMEvidencePromptTPL)
		ansOut, ansMS, ansErr := completeOpenAIChat(ctx, aiCfg.BaseURL, aiCfg.APIKey, aiCfg.AnswerModel, ATMSystemPrompt, userPayload, 0.0, aiCfg.MaxTokens, aiCfg.TimeoutSec)
		resItem.AnswerMS = ansMS
		if ansErr != nil {
			resItem.JudgeVerdict = "error"
			resItem.JudgeReason = "ATM Answerer Error: " + ansErr.Error()
		} else {
			resItem.Prediction = ansOut
			f1, exact := computeTokenF1(ansOut, q.Answer)
			resItem.TokenF1 = f1
			resItem.ExactMatch = exact

			switch q.Category {
			case "1": // list_recall -> Jaccard
				jac := computeATMListJaccardAdmin(q.Answer, ansOut)
				if jac >= 0.5 {
					resItem.JudgeVerdict = "correct"
				} else {
					resItem.JudgeVerdict = "wrong"
				}
				resItem.JudgeReason = fmt.Sprintf("ATM-Bench Official list_recall Jaccard = %.3f", jac)
				// 官方 QS 记连续 Jaccard；JudgeVerdict 的 0.5 二值只服务五桶归因。
				// TokenF1 列不覆写（CLI 行内 f1 恒为 TokenF1，Jaccard 只落 qs_score）；
				// QS 行内四位取整与 CLI PyRound(qsScore, 4) 同尺。
				resItem.QSScore = roundFloat(jac, 4)
				resItem.QSScored = true
			case "2": // number -> Deterministic EM
				emOK, reason := computeATMNumberEMAdminWithQuestion(q.Answer, ansOut, q.Question)
				if emOK {
					resItem.JudgeVerdict = "correct"
					resItem.QSScore = 1.0
				} else {
					resItem.JudgeVerdict = "wrong"
				}
				resItem.QSScored = true
				resItem.JudgeReason = reason
			default: // open_end -> LLM Judge
				jdgPrompt := strings.NewReplacer(
					"{{question}}", strings.TrimSpace(q.Question),
					"{{answer}}", strings.TrimSpace(q.Answer),
					"{{prediction}}", strings.TrimSpace(ansOut),
				).Replace(ATMJudgePromptTPL)
				jdgOut, jdgMS, jdgErr := completeOpenAIChat(ctx, aiCfg.BaseURL, aiCfg.APIKey, aiCfg.JudgeModel, "You are an objective QA evaluation judge. Respond with JSON only.", jdgPrompt, 0.0, aiCfg.MaxTokens, aiCfg.TimeoutSec)
				resItem.JudgeMS = jdgMS
				if jdgErr != nil {
					resItem.JudgeVerdict = "error"
					resItem.JudgeReason = "ATM Judge Error: " + jdgErr.Error()
				} else {
					verdict, reason, parseErr := parseATMJudgeOutputAdmin(jdgOut)
					resItem.JudgeVerdict = verdict
					resItem.JudgeReason = reason
					if parseErr == nil {
						resItem.QSScored = true
						if verdict == "correct" {
							resItem.QSScore = 1.0
						}
					}
				}
			}
		}
	}

	resItem.FailureBucket = classifyFailureBucket(resItem)
	resItem.ReproCLI = buildATMReproCLICommand(activeEvalTierForSuite(SuiteATM), armMode, storePath, rawLimit, useAgents)
	resItem.TotalMS = time.Since(totalStart).Milliseconds()
	return resItem, recallRaw, nil
}

func buildATMReproCLICommand(tier, armMode, storePath string, rawLimit int, useAgents bool) string {
	if rawLimit <= 0 {
		rawLimit = 50
	}
	args := []string{"rmem", "eval", "--eval-store", portableCLIStorePath(storePath), "--suite", "atm", "--tier", normalizeATMTier(tier)}
	if strings.EqualFold(strings.TrimSpace(armMode), EvalArmOracle) {
		args = append(args, "--oracle")
	}
	if useAgents {
		args = append(args, "--answerer", "llm", "--judge", "llm")
	} else {
		args = append(args, "--answerer", "none", "--judge", "off")
	}
	args = append(args, "--top-k", strconv.Itoa(rawLimit))
	return strings.Join(args, " ")
}

// loadATMResultsJSONL loads persisted per-question X-Ray items for sealed ATM local_plain baselines.
func loadATMResultsJSONL(home, runID string) []EvalQuestionResultItem {
	var candidates []string
	outDir := filepath.Join(home, ".local", "state", "rolling-memory", "eval", "out")
	switch strings.TrimSpace(runID) {
	case "atm-baseline-rm-hard-local-plain":
		candidates = []string{
			filepath.Join(outDir, "results-atm-hard-sdk-local-plain-llm.jsonl"),
			filepath.Join(outDir, "results-atm-hard-sdk-local-plain-none.jsonl"),
		}
	case "atm-baseline-rm-full1013-local-plain":
		candidates = []string{
			filepath.Join(outDir, "results-atm-1013-sdk-local-plain-llm.jsonl"),
			filepath.Join(outDir, "results-atm-1013-sdk-local-plain-none.jsonl"),
		}
	default:
		return nil
	}
	var raw []byte
	var err error
	for _, p := range candidates {
		raw, err = os.ReadFile(p)
		if err == nil {
			break
		}
	}
	if err != nil || len(raw) == 0 {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	out := make([]EvalQuestionResultItem, 0, len(lines))
	for idx, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) != nil {
			continue
		}
		var evTexts []string
		if rawEv, ok := m["evidence_texts"].([]any); ok {
			for _, e := range rawEv {
				if s := asString(e); s != "" {
					evTexts = append(evTexts, s)
				}
			}
		}
		var fmtItems []string
		if rawFmt, ok := m["formatted_items"].([]any); ok {
			for _, f := range rawFmt {
				if s := asString(f); s != "" {
					fmtItems = append(fmtItems, s)
				}
			}
		}
		ctxPreview := strings.Join(fmtItems, "\n")
		if len([]rune(ctxPreview)) > evalPreviewCharCap {
			ctxPreview = string([]rune(ctxPreview)[:evalPreviewCharCap]) + " …"
		}
		evRec := asFloat(m["evidence_recall"])
		evHit, _ := m["evidence_hit"].(bool)
		// 与 CLI 行内字段同名同义：
		//   answer_in_context = 金标答案词元在交付上下文中的覆盖率 ≥0.8（CLI atmRecallRow::aic），
		//   面板「上下文含答案率 / Answer in Context」列必须读它；此前误读 full_evidence_hit
		//   （全证据命中）⇒ 同一份 JSONL 面板显示 0.871 而 CLI summary 为 0.0323（实数据对照）。
		aic, _ := m["answer_in_context"].(bool)
		qsScored, _ := m["qs_scored"].(bool)
		item := EvalQuestionResultItem{
			Index:              idx + 1,
			ID:                 asString(m["id"]),
			SampleID:           asString(m["sample_id"]),
			Category:           asString(m["category"]),
			Question:           asString(m["question"]),
			Gold:               asString(m["gold"]),
			EvidenceTexts:      evTexts,
			BestEvidenceRecall: evRec,
			EvidenceHit:        evHit,
			AnswerInContext:    aic,
			QSScore:            asFloat(m["qs_score"]),
			QSScored:           qsScored,
			GoldTokenRecall:    asFloat(m["answer_token_recall"]),
			Prediction:         asString(m["prediction"]),
			TokenF1:            asFloat(m["f1"]),
			JudgeVerdict:       asString(m["judge"]),
			JudgeReason:        asString(m["judge_reason"]),
			ContextPreview:     ctxPreview,
			FormattedItems:     fmtItems,
			RawItems:           fmtItems,
			ContextChars:       asInt(m["context_chars"]),
			Bands:              asInt(m["bands"]),
			Nodes:              asInt(m["nodes"]),
			RawHits:            asInt(m["raw_hits"]),
			RawDelivered:       len(fmtItems),
			RecallMS:           int64(asFloat(m["recall_seconds"]) * 1000),
			TotalMS:            int64(asFloat(m["recall_seconds"]) * 1000),
			FailureBucket:      asString(m["failure_bucket"]),
			ContextOrdering:    "raw_first",
		}
		if item.FailureBucket == "" {
			item.FailureBucket = classifyFailureBucket(item)
		}
		out = append(out, item)
	}
	return out
}
