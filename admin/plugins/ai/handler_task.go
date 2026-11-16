// plugin: ai v0.2.0
// handler_task.go 是异步任务的 4 条后台端点（F5+F6）：
//
//	POST /task/list          任务列表（分页 + status/task_type 闭集筛选 + 快搜 + wheres 高级检索）
//	GET  /task/get/:pk       任务详情（载荷/结果全文 + 分片进度 + 累计 usage 口径）
//	POST /task/submit        提交任务（chat 单条 / embedding 批量；request_id 幂等，空值由服务端强制生成）
//	POST /task/export/:pk    导出结果（GET，application/x-ndjson 流式逐行；服务端不落盘任何文件）
//
// 权限与路由链由 plugin.go 的 adminRoutes 声明（装配面登记）：list/get 走末段 list|get 的 /read 回落，
// 故节点只有 read/submit/export 三个。
//
// 出口纪律：
//   - 列表不含 payload/result 全文（只给摘要与计数）；详情把两者以 JSON 原样给出；
//   - 导出是**流式**：逐行写、逐行 flush，不写临时文件、不整包进响应体（X-Accel-Buffering: no）；
//   - 累计口径复用调用日志的同一份汇总（handler_usage.go 的 summarizeUsage），两处读数不会各说各话。
package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/internal/kit/httpx"

	"github.com/gin-gonic/gin"
)

// taskStatuses / taskTypes 任务列表的筛选闭集（与迁移 5 的两条 CHECK 逐字一致）。
var (
	taskStatuses = []string{TaskStatusPending, TaskStatusProcessing, TaskStatusSuccess, TaskStatusFailed}
	taskTypes    = []string{TaskTypeChat, TaskTypeEmbedding}
)

// taskWhereFields 是任务表的 wheres 字段白名单（列名是已引用常量；payload/result 全文列刻意不在其中
// ——它们是 jsonb 大对象，不开放给「高级检索」做分组条件）。
var taskWhereFields = map[string]whereField{
	"id":             {Column: `"id"`, Kind: whereInt},
	"task_type":      {Column: `"task_type"`, Kind: whereText},
	"status":         {Column: `"status"`, Kind: whereText},
	"source":         {Column: `"source"`, Kind: whereText},
	"biz_type":       {Column: `"biz_type"`, Kind: whereText},
	"request_id":     {Column: `"request_id"`, Kind: whereText},
	"attempt":        {Column: `"attempt"`, Kind: whereInt},
	"progress_done":  {Column: `"progress_done"`, Kind: whereInt},
	"progress_total": {Column: `"progress_total"`, Kind: whereInt},
	"error":          {Column: `"error"`, Kind: whereText},
	"create_time":    {Column: `"create_time"`, Kind: whereInt},
	"update_time":    {Column: `"update_time"`, Kind: whereInt},
}

// 提交侧的取值边界（chat 直通参数；公开面不校验这三项，本处按「管理端提交」收敛）。
const (
	maxTaskTemperature = 2
	maxTaskTopP        = 1
	maxTaskMaxTokens   = 1_000_000
	// maxTaskPayloadBytes 载荷上限：与公开面请求体同量级（8MiB），防「一条任务把整库塞爆」。
	maxTaskPayloadBytes = 8 << 20
)

// ===== 列表 =====

// taskListRequest 任务列表入参：宿主分页契约 + status/task_type 闭集筛选。
type taskListRequest struct {
	listRequest
	Status   string `json:"status"`
	TaskType string `json:"task_type"`
}

// filter 收敛筛选、排序与已校验的 wheres 组。
func (r taskListRequest) filter() (TaskFilter, []whereGroup, error) {
	status := strings.TrimSpace(r.Status)
	if status != "" && !containsString(taskStatuses, status) {
		return TaskFilter{}, nil, fmt.Errorf("status %q 不在闭集内（允许：%s）", r.Status, strings.Join(taskStatuses, "|"))
	}
	taskType := strings.TrimSpace(r.TaskType)
	if taskType != "" && !containsString(taskTypes, taskType) {
		return TaskFilter{}, nil, fmt.Errorf("task_type %q 不在闭集内（允许：%s）", r.TaskType, strings.Join(taskTypes, "|"))
	}
	if err := validateSort(r.Sort, taskSortColumns, "任务"); err != nil {
		return TaskFilter{}, nil, err
	}
	groups, err := validateWhereGroups(taskWhereFields, r.Wheres)
	if err != nil {
		return TaskFilter{}, nil, err
	}
	return TaskFilter{
		Page:     r.Page,
		Limit:    r.Limit,
		Status:   status,
		TaskType: taskType,
		Keyword:  r.keyword(),
		Sort:     r.Sort,
		Order:    r.Order,
	}, groups, nil
}

// taskList 任务分页列表（默认 id DESC；keyword 命中 request_id/source/biz_type/模型名）。
func (d deps) taskList(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	var req taskListRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	filter, groups, err := req.filter()
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	ctx := c.Request.Context()
	var page TaskPage
	if len(groups) == 0 {
		page, err = store.ListTasks(ctx, filter)
	} else {
		page, err = store.listTasksWithWheres(ctx, filter, groups)
	}
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("读取任务列表失败: "+err.Error()))
		return
	}
	rows := make([]taskRow, 0, len(page.Rows))
	for _, row := range page.Rows {
		rows = append(rows, newTaskRow(row))
	}
	httpx.Success(c, httpx.WithData(gin.H{"list": rows, "total": page.Total}))
}

// listTasksWithWheres 与 Store.ListTasks 同口径（同一基础筛选/排序/分页），只多一层 wheres。
// 与 handler_usage.go 的 listUsageLogsWithWheres 同款（本插件对 wheres 的唯一越层点，见 plugin.go 头注）。
func (s *Store) listTasksWithWheres(ctx context.Context, f TaskFilter, groups []whereGroup) (TaskPage, error) {
	result := TaskPage{Rows: []Task{}}
	if err := s.ready(); err != nil {
		return result, err
	}
	countQuery, err := applyWhereGroups(s.taskQuery(ctx, f), taskWhereFields, groups)
	if err != nil {
		return result, err
	}
	if err := countQuery.Count(&result.Total).Error; err != nil {
		return result, err
	}
	listQuery, err := applyWhereGroups(s.taskQuery(ctx, f), taskWhereFields, groups)
	if err != nil {
		return result, err
	}
	offset, limit := normalizePage(f.Page, f.Limit, DefaultTaskListLimit)
	order := orderClause(f.Sort, f.Order, taskSortColumns, `"id" DESC`)
	if err := listQuery.Order(order).Offset(offset).Limit(limit).Find(&result.Rows).Error; err != nil {
		return result, err
	}
	return result, nil
}

// ===== 详情 =====

// taskGet 任务详情：行（不含 jsonb 全文列）+ 解析后的载荷/结果 + 分片进度 + 累计 usage 口径。
func (d deps) taskGet(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	id, err := parsePK(c, "任务")
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	ctx := c.Request.Context()
	row, err := store.GetTask(ctx, uint64(id))
	if err != nil {
		if errors.Is(err, ErrTaskNotFound) {
			httpx.Fail(c, httpx.WithMessage(fmt.Sprintf("任务 %d 不存在", id)))
			return
		}
		httpx.Fail(c, httpx.WithMessage("读取任务失败: "+err.Error()))
		return
	}
	usageIDs := taskUsageRequestIDs(*row)
	usages, err := store.TaskUsageRows(ctx, usageIDs)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("读取任务用量失败: "+err.Error()))
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{
		"row":               *row,
		"payload":           rawJSONOrNil(row.Payload),
		"result":            rawJSONOrNil(row.Result),
		"entries":           taskResultEntryCount(row.TaskType, row.Result),
		"usage":             summarizeUsage(usages),
		"usage_request_ids": usageIDs,
		"usage_rows":        len(usages),
	}))
}

// rawJSONOrNil 把 jsonb 文本转成「原样 JSON」出口（nil/空 ⇒ JSON null，不输出空字符串）。
func rawJSONOrNil(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	return json.RawMessage(raw)
}

// ===== 提交 =====

// taskSubmitRequest 提交入参：chat 用 messages、embedding 用 inputs；两套互斥，混用一律拒。
type taskSubmitRequest struct {
	TaskType string    `json:"task_type"`
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Inputs   []string  `json:"inputs"`

	BizType string `json:"biz_type"`
	Source  string `json:"source"`
	// RequestID 幂等键：缺省/空串 ⇒ 服务端生成（**强制非空**，见 build）。
	RequestID string `json:"request_id"`

	Temperature *float64 `json:"temperature"`
	TopP        *float64 `json:"top_p"`
	MaxTokens   *int     `json:"max_tokens"`
}

// build 收敛提交入参（纯函数，可单测）：闭集/边界/互斥字段/幂等键全在这一处校验。
func (r taskSubmitRequest) build() (TaskSubmitInput, error) {
	taskType := strings.TrimSpace(r.TaskType)
	if taskType == "" {
		taskType = TaskTypeChat
	}
	if !containsString(taskTypes, taskType) {
		return TaskSubmitInput{}, fmt.Errorf("task_type %q 不在闭集内（允许：%s）", r.TaskType, strings.Join(taskTypes, "|"))
	}

	model := strings.TrimSpace(r.Model)
	if model == "" {
		return TaskSubmitInput{}, errors.New("model 必填")
	}
	if len([]rune(model)) > 100 {
		return TaskSubmitInput{}, errors.New("model 最长 100 字符")
	}

	bizType := strings.TrimSpace(r.BizType)
	source := strings.TrimSpace(r.Source)
	if err := ValidateCallContext(bizType, source); err != nil {
		return TaskSubmitInput{}, err // 复用同步路径的同一份校验（文案逐字一致）
	}
	if len([]rune(bizType)) > 30 || len([]rune(source)) > 30 {
		return TaskSubmitInput{}, errors.New("biz_type 与 source 最长 30 字符（usage 对应列宽）")
	}

	requestID := strings.TrimSpace(r.RequestID)
	if requestID == "" {
		// **强制非空**：空 request_id 逃逸部分唯一索引 ⇒ 幂等失效；服务端生成是唯一入口口径。
		requestID = NewRequestID()
	} else if err := validateTaskRequestID(requestID); err != nil {
		return TaskSubmitInput{}, err
	}

	payload := taskPayload{Model: model, BizType: bizType, Source: source}
	progressTotal := 0
	switch taskType {
	case TaskTypeChat:
		if len(r.Inputs) > 0 {
			return TaskSubmitInput{}, errors.New("chat 任务不接受 inputs（批量 embedding 请用 task_type=embedding）")
		}
		if len(r.Messages) == 0 {
			return TaskSubmitInput{}, errors.New("chat 任务需要 messages（非空数组）")
		}
		if len(r.Messages) > maxTaskChatMessages {
			return TaskSubmitInput{}, fmt.Errorf("messages 最多 %d 条（当前 %d 条）", maxTaskChatMessages, len(r.Messages))
		}
		for index, message := range r.Messages {
			if strings.TrimSpace(message.Role) == "" {
				return TaskSubmitInput{}, fmt.Errorf("messages[%d].role 必填", index)
			}
		}
		temperature, err := boundedTaskFloat(r.Temperature, 0, maxTaskTemperature, "temperature")
		if err != nil {
			return TaskSubmitInput{}, err
		}
		topP, err := boundedTaskFloat(r.TopP, 0, maxTaskTopP, "top_p")
		if err != nil {
			return TaskSubmitInput{}, err
		}
		if r.MaxTokens != nil && (*r.MaxTokens < 1 || *r.MaxTokens > maxTaskMaxTokens) {
			return TaskSubmitInput{}, fmt.Errorf("max_tokens 取值范围 1..%d", maxTaskMaxTokens)
		}
		payload.Messages = r.Messages
		payload.Temperature = temperature
		payload.TopP = topP
		payload.MaxTokens = r.MaxTokens
		progressTotal = 1
	case TaskTypeEmbedding:
		if len(r.Messages) > 0 {
			return TaskSubmitInput{}, errors.New("embedding 任务不接受 messages（chat 请用 task_type=chat）")
		}
		if r.Temperature != nil || r.TopP != nil || r.MaxTokens != nil {
			return TaskSubmitInput{}, errors.New("temperature/top_p/max_tokens 是 chat 任务参数")
		}
		if len(r.Inputs) == 0 {
			return TaskSubmitInput{}, errors.New("embedding 任务需要 inputs（非空字符串数组）")
		}
		if len(r.Inputs) > maxTaskEmbeddingInputs {
			return TaskSubmitInput{}, fmt.Errorf("inputs 最多 %d 条（当前 %d 条；公开面同步路径上限 %d 条不变）",
				maxTaskEmbeddingInputs, len(r.Inputs), maxPublicEmbeddingInput)
		}
		for index, item := range r.Inputs {
			if strings.TrimSpace(item) == "" {
				return TaskSubmitInput{}, fmt.Errorf("inputs[%d] 不能为空（空文本没有向量可算）", index)
			}
		}
		payload.Inputs = r.Inputs
		progressTotal = len(r.Inputs)
	}

	raw, err := encodeTaskPayload(payload)
	if err != nil {
		return TaskSubmitInput{}, err
	}
	if err := checkTaskPayloadSize(raw); err != nil {
		return TaskSubmitInput{}, err
	}
	return TaskSubmitInput{
		TaskType:      taskType,
		Payload:       raw,
		RequestID:     requestID,
		BizType:       bizType,
		Source:        source,
		ProgressTotal: progressTotal,
	}, nil
}

// boundedTaskFloat 可选浮点参数的边界校验（nil = 不传 = 用上游默认）。
func boundedTaskFloat(value *float64, low, high float64, name string) (*float64, error) {
	if value == nil {
		return nil, nil
	}
	if *value < low || *value > high {
		return nil, fmt.Errorf("%s 取值范围 %g..%g", name, low, high)
	}
	return value, nil
}

// checkTaskPayloadSize 载荷体量护栏（8MiB，与公开面请求体同量级）。
func checkTaskPayloadSize(raw []byte) error {
	if len(raw) > maxTaskPayloadBytes {
		return fmt.Errorf("任务载荷过大（%d 字节，上限 %d 字节），请拆分后再提交", len(raw), maxTaskPayloadBytes)
	}
	return nil
}

// taskSubmit 提交任务：同 request_id 重复提交只留一行（created=false 回既有任务）。
func (d deps) taskSubmit(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	var req taskSubmitRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	input, err := req.build()
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	row, created, err := store.SubmitTask(c.Request.Context(), input)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("提交任务失败: "+err.Error()))
		return
	}
	data := gin.H{
		"id":             row.ID,
		"task_type":      row.TaskType,
		"status":         row.Status,
		"request_id":     row.RequestID,
		"progress_total": row.ProgressTotal,
		"created":        created,
	}
	if !created {
		httpx.Success(c, httpx.WithMessage("同 request_id 的任务已存在，返回既有任务"), httpx.WithData(data))
		return
	}
	httpx.Success(c, httpx.WithData(data))
}

// ===== 导出 =====

// taskExport 导出任务结果：application/x-ndjson，**逐行写 + 逐行 flush**，服务端不落盘。
//
// 行数 = result 的条目数（embedding 每条向量一行、chat 一行；判据与详情页的 entries 同源，
// 见 task.go 的 taskExportLines / taskResultEntryCount）。
func (d deps) taskExport(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	id, err := parsePK(c, "任务")
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	row, err := store.GetTask(c.Request.Context(), uint64(id))
	if err != nil {
		if errors.Is(err, ErrTaskNotFound) {
			httpx.Fail(c, httpx.WithMessage(fmt.Sprintf("任务 %d 不存在", id)))
			return
		}
		httpx.Fail(c, httpx.WithMessage("读取任务失败: "+err.Error()))
		return
	}
	if row.Status != TaskStatusSuccess {
		httpx.Fail(c, httpx.WithMessage(fmt.Sprintf("任务尚未完成（status=%s），无法导出", row.Status)))
		return
	}
	lines, err := taskExportLines(row.TaskType, row.Result)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("解析任务结果失败: "+err.Error()))
		return
	}
	if len(lines) == 0 {
		httpx.Fail(c, httpx.WithMessage("任务没有可导出的结果条目"))
		return
	}

	header := c.Writer.Header()
	header.Set("Content-Type", "application/x-ndjson; charset=utf-8")
	header.Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"task-%d.ndjson\"", row.ID))
	// 反代/SSE 侧的同款提示：本响应是流式，不要缓冲整包
	header.Set("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	for _, line := range lines {
		if _, err := c.Writer.Write(append(line, '\n')); err != nil {
			// 客户端断开：停止读取（服务端没有需要清理的临时文件/句柄）
			return
		}
	}
	c.Writer.Flush()
}
