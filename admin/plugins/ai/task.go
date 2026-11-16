// plugin: ai v0.2.0
// task.go 是异步任务（F5+F6）的领域契约与纯函数：行模型、闭集、载荷、配置键、进度口径与导出行生成。
//
// 分层（与本插件既有分工一致）：
//   - 本文件：领域类型 + 纯函数（无 SQL、无 gin、无 goroutine）；
//   - store_task.go：账本的全部 SQL 原语（提交/查询/领取/续租/进度/终态/复位/usage 汇总读取）；
//   - task_worker.go：进程内 worker（启用自查 → 领取 → 执行 → 落终态/退回 pending）；
//   - handler_task.go：4 条后台路由（list/get/submit/export）+ 列表的 wheres 白名单。
//
// 与同步路径的一致性是本包的核心纪律：worker **不自己拼上游请求**，而是调用与公开面
// 同一个 *Service（service.go）——熔断、候选降级、计费与 usage 落库全沿用同一份实现，
// 差异只在「谁触发」。因此异步任务不需要第二套降级/计费逻辑，也不会与同步口径分叉。
//
// 重试与 usage 累积的关系（如实写清，导出/详情都按此口径读数）：
//   - 每次任务尝试（plugin_ai_task.attempt = 1,2,…）派生**不同的** usage request_id
//     `task-<本任务 request_id>-a<尝试>-s<分片>`（见 taskUsageRequestID）——重试不会覆盖上一次的
//     计费行，分片之间也各成一行；
//   - 任务详情的累计口径 = 这些 usage 行的汇总（含重试中已经消耗掉的 token 与成本，
//     以及失败片已产生的费用），所以「任务成功」不等于「只花了一次钱」；
//   - 为什么必须派生：usage 的幂等键是 (request_id, attempt)，若各次尝试共用同一 request_id，
//     同一候选的同一 attempt 序号会被**覆盖**，重试痕迹与花费都读不出来。
package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/database"
	"github.com/ai-go-hub/ai-go-admin/internal/model"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// 任务类型闭集（plugin_ai_task.task_type 的 CHECK；取值与 usage.call_type 同源，避免两处漂移）。
const (
	TaskTypeChat      = CallTypeChat
	TaskTypeEmbedding = CallTypeEmbedding
)

// 任务状态机闭集（plugin_ai_task.status 的 CHECK）：pending → processing → success|failed。
// 终态不可逆（重试只发生在 processing → pending 的退回路径上，attempt 已累加）。
const (
	TaskStatusPending    = "pending"
	TaskStatusProcessing = "processing"
	TaskStatusSuccess    = "success"
	TaskStatusFailed     = "failed"
)

// 异步提交侧的硬上限（与公开面同步 64 条护栏**并存**，不是替代）：
//   - maxTaskEmbeddingInputs 远大于同步面（service.go:38-42 的 maxPublicEmbeddingInput=64 保持不变，
//     handler_public.go:421-430 的 400 也保持不变）——异步路径靠 worker 侧分片越过同步护栏；
//   - maxTaskChatMessages 防「一条任务把整个会话塞成几十万条消息」的无界增长。
const (
	maxTaskEmbeddingInputs = 10000
	maxTaskChatMessages    = 200
	// maxTaskRequestIDLen 提交方自定义 request_id 的长度上限：派生的 usage request_id 为
	// "task-" + 本值 + "-a<尝试>" + "-s<分片>"（最长 5 + 40 + 4 + 5 = 54 字符 < varchar(64)）。
	maxTaskRequestIDLen = 40
	// maxTaskUsageIDs 任务详情一次汇总的 usage 行上限（= 最大条数/分片 × 最大尝试数，见下方两常量）。
	maxTaskUsageIDs = maxTaskEmbeddingInputs / maxUpstreamEmbeddingBatch * MaxTaskMaxAttempts
)

// 任务 worker 的两个配置键（落库名 = plugin_ai_<键>；与 plugin.yaml 的 configs 登记同名）。
//
// 注：本文件**自持**读这两个键（worker 每轮直读一次 configs），不依赖 plugin.go 的
// pluginConfigKeys/pluginSettings——那两处由装配面登记，用于设置页的回显与保存白名单；
// 两处共用同一落库键名，不产生第二个「配置真源」。
const (
	configKeyTaskPollSeconds = "plugin_ai_task_poll_seconds"
	configKeyTaskMaxAttempts = "plugin_ai_task_max_attempts"
)

// 任务 worker 的配置缺省与边界（plugin.yaml 的两键同口径）。
const (
	// DefaultTaskPollSeconds 轮询间隔缺省（秒）
	DefaultTaskPollSeconds = 5
	// MinTaskPollSeconds 轮询间隔下界：**clamp ≥ 1**，避免 0 变成忙等（0/非法一律回落缺省 5）
	MinTaskPollSeconds = 1
	// MaxTaskPollSeconds 轮询间隔上界（1 小时；再长就该用别的机制了）
	MaxTaskPollSeconds = 3600
	// DefaultTaskMaxAttempts 尝试次数缺省
	DefaultTaskMaxAttempts = 3
	// MaxTaskMaxAttempts 尝试次数上界（与 maxTaskUsageIDs 的推导有关：改动会同时放大汇总的 IN 列表）
	MaxTaskMaxAttempts = 10
	// DefaultTaskLease 任务租约时长：执行期间由续租协程按 1/3 周期续，租约过期即可被其它进程重领
	DefaultTaskLease = 5 * time.Minute
)

// taskRequestIDPattern 提交方自定义 request_id 的字符集：只允许 ASCII 字母数字与 _ . : -
// （它会被拼进 usage.request_id 与 LIKE/日志文案；限制字符集可避免空白/控制字符与转义歧义）。
var taskRequestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]+$`)

// ErrTaskNotFound 任务不存在（handler 转可读回执）
var ErrTaskNotFound = errors.New("插件 ai：任务不存在")

// Task 是 plugin_ai_task 的领域行，同时是 GORM 行模型。
//
// Payload / Result 是 jsonb 全文：**不进 JSON 出口**（json:"-"）——列表与详情给的是解析后的
// 结构化字段（详情）或摘要（列表），避免把上万条向量的全文塞进每次轮询。
type Task struct {
	ID            uint64         `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	TaskType      string         `gorm:"column:task_type;type:varchar(16);not null;default:'chat'" json:"task_type"`
	Status        string         `gorm:"column:status;type:varchar(16);not null;default:'pending'" json:"status"`
	Payload       datatypes.JSON `gorm:"column:payload;type:jsonb" json:"-"`
	Result        datatypes.JSON `gorm:"column:result;type:jsonb" json:"-"`
	Error         string         `gorm:"column:error;type:varchar(500);not null;default:''" json:"error"`
	Source        string         `gorm:"column:source;type:varchar(30);not null;default:''" json:"source"`
	BizType       string         `gorm:"column:biz_type;type:varchar(30);not null;default:''" json:"biz_type"`
	RequestID     string         `gorm:"column:request_id;type:varchar(64);not null;default:''" json:"request_id"`
	Attempt       int            `gorm:"column:attempt;not null;default:0" json:"attempt"`
	ProgressDone  int            `gorm:"column:progress_done;not null;default:0" json:"progress_done"`
	ProgressTotal int            `gorm:"column:progress_total;not null;default:0" json:"progress_total"`
	LockedUntil   *time.Time     `gorm:"column:locked_until" json:"-"`
	CreateTime    int64          `gorm:"column:create_time;not null;default:0" json:"create_time"`
	UpdateTime    int64          `gorm:"column:update_time;not null;default:0" json:"update_time"`
}

// TableName 物理表名（前缀由 model.go 的 table() 单点拼接）。
func (Task) TableName() string { return table("plugin_ai_task") }

// ===== 任务载荷（jsonb 的规范形状）=====

// taskPayload 是 plugin_ai_task.payload 的形状：提交期由 handler 收敛并编码，worker 只读。
// chat 用 Messages，embedding 用 Inputs；两套字段互斥（提交侧拒混用）。
type taskPayload struct {
	Model   string `json:"model"`
	BizType string `json:"biz_type"`
	Source  string `json:"source"`

	Messages []Message `json:"messages,omitempty"`
	Inputs   []string  `json:"inputs,omitempty"`

	// chat 直通参数（与公开面同名的三件；公开面不校验取值范围，本处提交侧做边界收敛）
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"top_p,omitempty"`
	MaxTokens   *int     `json:"max_tokens,omitempty"`
}

// encodeTaskPayload 编码载荷（空模型/空载荷在提交侧已被拒；此处只做序列化）。
func encodeTaskPayload(payload taskPayload) (datatypes.JSON, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("任务载荷无法编码: %w", err)
	}
	return datatypes.JSON(raw), nil
}

// decodeTaskPayload 解析载荷；空/损坏载荷是**永久失败**（重试也不会好，worker 直接置 failed）。
func decodeTaskPayload(raw datatypes.JSON) (taskPayload, error) {
	var payload taskPayload
	if len(raw) == 0 {
		return payload, errors.New("任务载荷为空")
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return payload, fmt.Errorf("任务载荷不是合法 JSON: %w", err)
	}
	return payload, nil
}

// ===== 任务结果（jsonb 的规范形状）=====

// taskChatResultJSON 是 chat 任务落库的 result 形状（内容 + 本次 usage 快照）。
type taskChatResultJSON struct {
	ID               string `json:"id,omitempty"`
	Model            string `json:"model,omitempty"`
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
	FinishReason     string `json:"finish_reason,omitempty"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	TotalTokens      int    `json:"total_tokens"`
}

// taskEmbeddingResultJSON 是 embedding 任务落库的 result 形状。
//
// 体量如实登记：Embeddings 是全量向量（条数 × 维度 × 8 字节量级），上万条高维模型会有几十 MB 的
// jsonb 行（PG 走 TOAST 外存，读写都吃内存）。v1 不做向量外置存储/分块（属后续项），
// 导出与详情都按「一次读整行」处理。
type taskEmbeddingResultJSON struct {
	Model      string      `json:"model,omitempty"`
	Count      int         `json:"count"`
	Dimensions int         `json:"dimensions"`
	Embeddings [][]float64 `json:"embeddings"`
}

// encodeChatTaskResult 把驱动层结果收敛成落库形状。
func encodeChatTaskResult(res *ChatResult) (datatypes.JSON, error) {
	if res == nil {
		return nil, errors.New("chat 任务结果是空值")
	}
	raw, err := json.Marshal(taskChatResultJSON{
		ID:               res.ID,
		Model:            res.Model,
		Content:          res.Content,
		ReasoningContent: res.ReasoningContent,
		FinishReason:     res.FinishReason,
		PromptTokens:     res.PromptTokens,
		CompletionTokens: res.CompletionTokens,
		TotalTokens:      res.TotalTokens,
	})
	if err != nil {
		return nil, fmt.Errorf("chat 任务结果无法编码: %w", err)
	}
	return datatypes.JSON(raw), nil
}

// encodeEmbeddingTaskResult 把分片合并后的向量收敛成落库形状（保序：按分片顺序拼接）。
func encodeEmbeddingTaskResult(model string, vectors [][]float64) (datatypes.JSON, error) {
	raw, err := json.Marshal(taskEmbeddingResultJSON{
		Model:      model,
		Count:      len(vectors),
		Dimensions: embeddingDimensions(vectors),
		Embeddings: vectors,
	})
	if err != nil {
		return nil, fmt.Errorf("embedding 任务结果无法编码: %w", err)
	}
	return datatypes.JSON(raw), nil
}

// embeddingDimensions 取首条向量的维度（空结果 = 0；各条维度不一致时按失败处理见 worker）。
func embeddingDimensions(vectors [][]float64) int {
	if len(vectors) == 0 {
		return 0
	}
	return len(vectors[0])
}

// ===== 结果摘要与导出（同一份解析，保证「导出行数 = 结果条目数」）=====

// taskResultEntryCount 结果条目数：embedding = 向量条数；chat = 1（成功）或 0（无结果）。
// 导出路由的行数判据就是它（两者共用同一份解析）。
func taskResultEntryCount(taskType string, raw datatypes.JSON) int {
	if len(raw) == 0 {
		return 0
	}
	switch taskType {
	case TaskTypeEmbedding:
		var result taskEmbeddingResultJSON
		if err := json.Unmarshal(raw, &result); err != nil {
			return 0
		}
		return len(result.Embeddings)
	case TaskTypeChat:
		var result taskChatResultJSON
		if err := json.Unmarshal(raw, &result); err != nil {
			return 0
		}
		return 1
	default:
		return 0
	}
}

// taskExportLines 把落库结果展开成 NDJSON 行（导出路由的唯一实现；服务端不落任何临时文件）。
//
// 行形状（每行一个 JSON 对象，行尾由路由补 '\n'）：
//   - embedding：每条向量一行 {"index":i,"object":"embedding","embedding":[...]}
//     （index 是提交时的输入位次，与输入一一对应）；
//   - chat：单行 {"index":0,"object":"chat.completion","content":…,…}。
func taskExportLines(taskType string, raw datatypes.JSON) ([][]byte, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	switch taskType {
	case TaskTypeEmbedding:
		var result taskEmbeddingResultJSON
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, fmt.Errorf("embedding 结果不是合法 JSON: %w", err)
		}
		lines := make([][]byte, 0, len(result.Embeddings))
		for index, vector := range result.Embeddings {
			line, err := json.Marshal(struct {
				Index     int       `json:"index"`
				Object    string    `json:"object"`
				Embedding []float64 `json:"embedding"`
			}{Index: index, Object: "embedding", Embedding: vector})
			if err != nil {
				return nil, fmt.Errorf("第 %d 条向量无法编码: %w", index, err)
			}
			lines = append(lines, line)
		}
		return lines, nil
	case TaskTypeChat:
		var result taskChatResultJSON
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, fmt.Errorf("chat 结果不是合法 JSON: %w", err)
		}
		line, err := json.Marshal(struct {
			Index        int    `json:"index"`
			Object       string `json:"object"`
			Model        string `json:"model,omitempty"`
			Content      string `json:"content"`
			FinishReason string `json:"finish_reason,omitempty"`
			TotalTokens  int    `json:"total_tokens"`
		}{Index: 0, Object: "chat.completion", Model: result.Model,
			Content: result.Content, FinishReason: result.FinishReason, TotalTokens: result.TotalTokens})
		if err != nil {
			return nil, fmt.Errorf("chat 结果无法编码: %w", err)
		}
		return [][]byte{line}, nil
	default:
		return nil, fmt.Errorf("未知任务类型 %q（闭集 chat|embedding）", taskType)
	}
}

// ===== usage request_id 的派生（重试/分片各成一行，见文件头注）=====

// taskUsageRequestID 派生一次「任务尝试内、第 shard 片」的 usage request_id。
// shard ≤ 0 表示该类型不分片（chat），只带尝试序号。
func taskUsageRequestID(taskRequestID string, attempt, shard int) string {
	id := "task-" + taskRequestID
	if attempt > 0 {
		id += "-a" + strconv.Itoa(attempt)
	} else {
		// 理论上不可达（attempt 由领取 SQL 累加，恒 ≥1）；留个确定形状便于排查，不生成 "-a0"。
		id += "-a?"
	}
	if shard > 0 {
		id += "-s" + strconv.Itoa(shard)
	}
	return id
}

// taskShardCount 任务类型对应的分片数（chat=1；embedding=⌈条数/上游单片上限⌉）。
func taskShardCount(taskType string, progressTotal int) int {
	if taskType != TaskTypeEmbedding {
		return 1
	}
	if progressTotal <= 0 {
		return 0
	}
	return (progressTotal + maxUpstreamEmbeddingBatch - 1) / maxUpstreamEmbeddingBatch
}

// taskUsageRequestIDs 任务截至当前的 usage request_id 全集（确定性推导，不查库）：
// 尝试 1..row.Attempt × 分片 1..⌈progress_total/20⌉。失败尝试可能只跑到中间某片，
// 多推几个 id 只是 IN 列表里多几个不命中的值（无害），换来「汇总不漏行」。
//
// 上限：maxTaskEmbeddingInputs/maxUpstreamEmbeddingBatch × MaxTaskMaxAttempts = maxTaskUsageIDs，
// 与提交/配置的两道上界同源 ⇒ 不会无界增长。
func taskUsageRequestIDs(row Task) []string {
	shards := taskShardCount(row.TaskType, row.ProgressTotal)
	if shards <= 0 {
		return nil
	}
	ids := make([]string, 0, row.Attempt*shards)
	for attempt := 1; attempt <= row.Attempt; attempt++ {
		for shard := 1; shard <= shards; shard++ {
			ids = append(ids, taskUsageRequestID(row.RequestID, attempt, taskShardSuffixIndex(row.TaskType, shard)))
			if len(ids) >= maxTaskUsageIDs {
				return ids
			}
		}
	}
	return ids
}

// taskShardSuffixIndex 派生 id 里的分片序号：只有 embedding 带分片后缀（chat 恒 0 ⇒ 不带后缀）。
func taskShardSuffixIndex(taskType string, shard int) int {
	if taskType == TaskTypeEmbedding {
		return shard
	}
	return 0
}

// ===== 列表出口行（不含 payload/result 全文）=====

// taskRow 是列表行：内嵌 Task（Payload/Result 被 json:"-" 隐去）+ 载荷/结果摘要。
type taskRow struct {
	Task
	Model         string `json:"model"`
	MessagesCount int    `json:"messages_count"`
	InputsCount   int    `json:"inputs_count"`
	Summary       string `json:"summary"`
	Entries       int    `json:"entries"`
}

// newTaskRow 生成列表行（摘要口径照源 TaskStore::format/summary：embedding 给「N 条 × D 维」，
// chat 给内容前 60 字或消息条数）。载荷/结果损坏时摘要留空，不影响列表可用。
func newTaskRow(row Task) taskRow {
	out := taskRow{Task: row, Entries: taskResultEntryCount(row.TaskType, row.Result)}
	if payload, err := decodeTaskPayload(row.Payload); err == nil {
		out.Model = payload.Model
		out.MessagesCount = len(payload.Messages)
		out.InputsCount = len(payload.Inputs)
	}
	switch row.TaskType {
	case TaskTypeEmbedding:
		var result taskEmbeddingResultJSON
		if len(row.Result) > 0 && json.Unmarshal(row.Result, &result) == nil && result.Count > 0 {
			out.Summary = fmt.Sprintf("%d 条 × %d 维", result.Count, result.Dimensions)
			break
		}
		if out.InputsCount > 0 {
			out.Summary = fmt.Sprintf("%d 条待处理", out.InputsCount)
		}
	case TaskTypeChat:
		var result taskChatResultJSON
		if len(row.Result) > 0 && json.Unmarshal(row.Result, &result) == nil && result.Content != "" {
			out.Summary = truncateRunes(result.Content, 60)
			if len([]rune(result.Content)) > 60 {
				out.Summary += "…"
			}
			break
		}
		if out.MessagesCount > 0 {
			out.Summary = fmt.Sprintf("%d 条消息", out.MessagesCount)
		}
	}
	if row.Error != "" {
		out.Summary = "失败: " + truncateRunes(row.Error, 60)
	}
	return out
}

// ===== 任务 worker 的配置（两键：轮询间隔与尝试次数上限）=====

// taskSettings 是任务 worker 的生效配置（非法值已回落）。
type taskSettings struct {
	PollSeconds int
	MaxAttempts int
}

// parseTaskSettings 解析两键（纯函数，可单测）：
//   - task_poll_seconds：**0 / 空 / 非法一律回落缺省 5**；正数 clamp 到 [1, 3600]
//     （下界 1 秒是硬要求：0 会让 worker 变成忙等）；
//   - task_max_attempts：0 / 空 / 非法回落缺省 3；正数 clamp 到 [1, 10]。
func parseTaskSettings(values map[string]string) taskSettings {
	return taskSettings{
		PollSeconds: normalizeTaskPollSeconds(values[configKeyTaskPollSeconds]),
		MaxAttempts: normalizeTaskMaxAttempts(values[configKeyTaskMaxAttempts]),
	}
}

// normalizeTaskPollSeconds 轮询间隔归一（下界 clamp；0/非法回落缺省）。
func normalizeTaskPollSeconds(raw string) int {
	value, ok := positiveInt(raw)
	if !ok {
		return DefaultTaskPollSeconds
	}
	return clampInt(value, MinTaskPollSeconds, MaxTaskPollSeconds)
}

// normalizeTaskMaxAttempts 尝试次数归一（下界 clamp；0/非法回落缺省）。
func normalizeTaskMaxAttempts(raw string) int {
	value, ok := positiveInt(raw)
	if !ok {
		return DefaultTaskMaxAttempts
	}
	return clampInt(value, 1, MaxTaskMaxAttempts)
}

// loadTaskSettings 读两键（worker 每轮直读一次；不缓存 ⇒ 改设置下一轮即生效，间隔至少 1s 也不会变成忙等）。
// 读库失败按错误返回（worker 用缺省值继续，见 taskPollInterval）。
func loadTaskSettings(ctx context.Context) (taskSettings, error) {
	db := database.DB()
	if db == nil {
		return taskSettings{}, fmt.Errorf("%w（任务配置读取须在 database.Init() 之后）", ErrStoreUnavailable)
	}
	rows, err := gorm.G[model.Config](db).
		Where("name IN ?", []string{configKeyTaskPollSeconds, configKeyTaskMaxAttempts}).
		Find(ctx)
	if err != nil {
		return taskSettings{}, err
	}
	values := make(map[string]string, len(rows))
	for _, row := range rows {
		if row.Value != nil {
			values[row.Name] = *row.Value
		}
	}
	return parseTaskSettings(values), nil
}

// validateTaskRequestID 提交方自定义 request_id 的字符集与长度（空串表示「由服务端生成」）。
func validateTaskRequestID(requestID string) error {
	trimmed := strings.TrimSpace(requestID)
	if trimmed == "" {
		return nil
	}
	if len([]rune(trimmed)) > maxTaskRequestIDLen {
		return fmt.Errorf("request_id 最长 %d 字符（当前 %d 字符）", maxTaskRequestIDLen, len([]rune(trimmed)))
	}
	if !taskRequestIDPattern.MatchString(trimmed) {
		return errors.New("request_id 只允许字母、数字与 _ . : - （它会拼进 usage.request_id）")
	}
	return nil
}
