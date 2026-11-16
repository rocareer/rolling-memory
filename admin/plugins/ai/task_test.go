// plugin: ai v0.2.0
// task_test.go 是异步任务的**纯函数**用例（无库、无网络、无 goroutine 依赖）：
//
//	提交入参的闭集/边界/互斥字段/幂等键、两键配置的缺省与下界 clamp（task_poll_seconds 不得为 0）、
//	usage request_id 的派生与上界、结果摘要与导出行数、载荷编解码、列表的 wheres 与排序白名单。
//
// 真库侧（迁移 DDL、SKIP LOCKED 领取、并发幂等、端到端）在 task_db_test.go（PLUGIN_AI_TEST_DSN 门控）；
// worker 的领取/重试/分片行为（内存假件）在 task_worker_test.go。
package ai

import (
	"encoding/json"
	"strings"
	"testing"

	"gorm.io/datatypes"
)

// ===== 两键配置：缺省与下界 clamp =====

func TestTaskParseSettingsDefaultsAndClamp(t *testing.T) {
	// 缺省：两个键都缺失时用代码缺省（轮询 5s / 尝试 3 次）
	if got := parseTaskSettings(map[string]string{}); got.PollSeconds != DefaultTaskPollSeconds || got.MaxAttempts != DefaultTaskMaxAttempts {
		t.Fatalf("缺省值不符: %+v", got)
	}

	// task_poll_seconds：0 / 空 / 非法一律回落缺省 5；正数 clamp 到 [1, 3600]
	for _, raw := range []string{"", "0", "0.5", "-1", "-3", "abc", "  "} {
		got := parseTaskSettings(map[string]string{configKeyTaskPollSeconds: raw})
		if got.PollSeconds != DefaultTaskPollSeconds {
			t.Fatalf("poll=%q 应回落缺省 %d，实际 %d", raw, DefaultTaskPollSeconds, got.PollSeconds)
		}
	}
	for raw, want := range map[string]int{"1": 1, "2": 2, "60": 60, "3600": 3600, "99999": MaxTaskPollSeconds} {
		got := parseTaskSettings(map[string]string{configKeyTaskPollSeconds: raw})
		if got.PollSeconds != want {
			t.Fatalf("poll=%q 应为 %d，实际 %d", raw, want, got.PollSeconds)
		}
	}

	// task_max_attempts：0 / 空 / 非法回落缺省 3；正数 clamp 到 [1, 10]
	for _, raw := range []string{"", "0", "-1", "x"} {
		got := parseTaskSettings(map[string]string{configKeyTaskMaxAttempts: raw})
		if got.MaxAttempts != DefaultTaskMaxAttempts {
			t.Fatalf("max_attempts=%q 应回落缺省 %d，实际 %d", raw, DefaultTaskMaxAttempts, got.MaxAttempts)
		}
	}
	for raw, want := range map[string]int{"1": 1, "3": 3, "10": 10, "99": MaxTaskMaxAttempts} {
		got := parseTaskSettings(map[string]string{configKeyTaskMaxAttempts: raw})
		if got.MaxAttempts != want {
			t.Fatalf("max_attempts=%q 应为 %d，实际 %d", raw, want, got.MaxAttempts)
		}
	}
}

// TestTaskPollIntervalNeverBelowOneSecond 钉死「轮询间隔不得为 0」（避免忙等）：
// 任何输入下 normalizeTaskPollSeconds 都 ≥ 1，且 clamp 下界与实现常量一致。
func TestTaskPollIntervalNeverBelowOneSecond(t *testing.T) {
	if MinTaskPollSeconds < 1 {
		t.Fatalf("轮询下界必须是 ≥1 秒，实际 %d", MinTaskPollSeconds)
	}
	inputs := []string{"", "0", "-1", "-100", "abc", "0.0001", " ", "nan", "1e3", "1", "2", "5", "3601"}
	for _, raw := range inputs {
		seconds := normalizeTaskPollSeconds(raw)
		if seconds < MinTaskPollSeconds {
			t.Fatalf("poll=%q 归一成 %d 秒，低于下界 %d（会让 worker 忙等）", raw, seconds, MinTaskPollSeconds)
		}
		if seconds > MaxTaskPollSeconds {
			t.Fatalf("poll=%q 归一成 %d 秒，高于上界 %d", raw, seconds, MaxTaskPollSeconds)
		}
	}
}

// ===== 提交入参 =====

func TestTaskSubmitRequestBuildsChatAndEmbedding(t *testing.T) {
	// chat：缺省 task_type ⇒ chat；进度总数 = 1
	chatInput, err := taskSubmitRequest{
		Model:    "gpt-test",
		Messages: []Message{{Role: "user", Content: "你好"}},
		BizType:  "admin",
		Source:   "admin:task",
	}.build()
	if err != nil {
		t.Fatalf("合法 chat 提交被拒: %v", err)
	}
	if chatInput.TaskType != TaskTypeChat || chatInput.ProgressTotal != 1 {
		t.Fatalf("chat 提交收敛不符: %+v", chatInput)
	}
	payload, err := decodeTaskPayload(chatInput.Payload)
	if err != nil {
		t.Fatalf("载荷解码失败: %v", err)
	}
	if payload.Model != "gpt-test" || len(payload.Messages) != 1 || payload.BizType != "admin" || payload.Source != "admin:task" {
		t.Fatalf("载荷内容不符: %+v", payload)
	}
	if chatInput.RequestID == "" || !taskRequestIDPattern.MatchString(chatInput.RequestID) {
		t.Fatalf("空 request_id 必须由服务端生成合法值，实际 %q", chatInput.RequestID)
	}

	// 显式 task_type=chat + 三个直通参数
	temperature := 0.7
	topP := 0.9
	maxTokens := 256
	chatInput, err = taskSubmitRequest{
		TaskType: "chat", Model: "gpt-test",
		Messages:    []Message{{Role: "user", Content: "hi"}},
		BizType:     "admin",
		Source:      "admin:task",
		Temperature: &temperature, TopP: &topP, MaxTokens: &maxTokens,
	}.build()
	if err != nil {
		t.Fatalf("带直通参数的 chat 提交被拒: %v", err)
	}
	payload, err = decodeTaskPayload(chatInput.Payload)
	if err != nil {
		t.Fatalf("载荷解码失败: %v", err)
	}
	if payload.Temperature == nil || *payload.Temperature != temperature || payload.TopP == nil || payload.MaxTokens == nil {
		t.Fatalf("直通参数未进载荷: %+v", payload)
	}

	// embedding：进度总数 = 输入条数
	inputs := make([]string, 0, 45)
	for i := 0; i < 45; i++ {
		inputs = append(inputs, "文本")
	}
	embedInput, err := taskSubmitRequest{
		TaskType: "embedding", Model: "bge-test", Inputs: inputs,
		BizType: "admin", Source: "admin:task",
	}.build()
	if err != nil {
		t.Fatalf("合法 embedding 提交被拒: %v", err)
	}
	if embedInput.TaskType != TaskTypeEmbedding || embedInput.ProgressTotal != 45 {
		t.Fatalf("embedding 提交收敛不符: %+v", embedInput)
	}
	payload, err = decodeTaskPayload(embedInput.Payload)
	if err != nil {
		t.Fatalf("载荷解码失败: %v", err)
	}
	if len(payload.Inputs) != 45 || len(payload.Messages) != 0 {
		t.Fatalf("embedding 载荷不符: 输入 %d 条 / messages %d 条", len(payload.Inputs), len(payload.Messages))
	}
}

func TestTaskSubmitRequestRejectsInvalidInputs(t *testing.T) {
	tooManyMessages := make([]Message, maxTaskChatMessages+1)
	for i := range tooManyMessages {
		tooManyMessages[i] = Message{Role: "user", Content: "x"}
	}
	tooManyInputs := make([]string, maxTaskEmbeddingInputs+1)
	for i := range tooManyInputs {
		tooManyInputs[i] = "x"
	}
	overlongSource := strings.Repeat("s", 31)
	badTemperature := 3.0
	badTopP := 1.5
	badMaxTokens := 0
	good := taskSubmitRequest{
		Model: "gpt-test", Messages: []Message{{Role: "user", Content: "hi"}},
		BizType: "admin", Source: "admin:task",
	}

	cases := []struct {
		name    string
		mutate  func(*taskSubmitRequest)
		wantSub string
	}{
		{"任务类型闭集外", func(r *taskSubmitRequest) { r.TaskType = "image" }, "不在闭集内"},
		{"model 必填", func(r *taskSubmitRequest) { r.Model = " " }, "model 必填"},
		{"chat 缺 messages", func(r *taskSubmitRequest) { r.Messages = nil }, "messages（非空数组）"},
		{"chat 消息条数超限", func(r *taskSubmitRequest) { r.Messages = tooManyMessages }, "最多"},
		{"chat 消息缺 role", func(r *taskSubmitRequest) { r.Messages = []Message{{Content: "hi"}} }, "role 必填"},
		{"chat 混入 inputs", func(r *taskSubmitRequest) { r.Inputs = []string{"x"} }, "不接受 inputs"},
		{"biz_type 缺失", func(r *taskSubmitRequest) { r.BizType = "" }, "biz_type"},
		{"source 缺失", func(r *taskSubmitRequest) { r.Source = "" }, "source"},
		{"source 超列宽", func(r *taskSubmitRequest) { r.Source = overlongSource }, "最长 30 字符"},
		{"temperature 越界", func(r *taskSubmitRequest) { r.Temperature = &badTemperature }, "temperature 取值范围"},
		{"top_p 越界", func(r *taskSubmitRequest) { r.TopP = &badTopP }, "top_p 取值范围"},
		{"max_tokens 越界", func(r *taskSubmitRequest) { r.MaxTokens = &badMaxTokens }, "max_tokens 取值范围"},
	}
	for _, tc := range cases {
		req := good
		tc.mutate(&req)
		if _, err := req.build(); err == nil || !strings.Contains(err.Error(), tc.wantSub) {
			t.Fatalf("%s：期望含 %q 的错误，实际 %v", tc.name, tc.wantSub, err)
		}
	}

	embeddingGood := taskSubmitRequest{
		TaskType: "embedding", Model: "bge-test", Inputs: []string{"a"},
		BizType: "admin", Source: "admin:task",
	}
	embeddingCases := []struct {
		name    string
		mutate  func(*taskSubmitRequest)
		wantSub string
	}{
		{"embedding 缺 inputs", func(r *taskSubmitRequest) { r.Inputs = nil }, "需要 inputs"},
		{"embedding 条数超限", func(r *taskSubmitRequest) { r.Inputs = tooManyInputs }, "最多 10000 条"},
		{"embedding 空文本", func(r *taskSubmitRequest) { r.Inputs = []string{"a", " "} }, "不能为空"},
		{"embedding 混入 messages", func(r *taskSubmitRequest) { r.Messages = good.Messages }, "不接受 messages"},
		{"embedding 混入 chat 参数", func(r *taskSubmitRequest) { r.MaxTokens = &badMaxTokens }, "chat 任务参数"},
	}
	for _, tc := range embeddingCases {
		req := embeddingGood
		tc.mutate(&req)
		if _, err := req.build(); err == nil || !strings.Contains(err.Error(), tc.wantSub) {
			t.Fatalf("%s：期望含 %q 的错误，实际 %v", tc.name, tc.wantSub, err)
		}
	}
}

// TestTaskSubmitRequestRequestIDRules 覆盖幂等键的三态：缺省=生成、合法=原样、非法=拒绝。
func TestTaskSubmitRequestRequestIDRules(t *testing.T) {
	base := taskSubmitRequest{
		Model: "gpt-test", Messages: []Message{{Role: "user", Content: "hi"}},
		BizType: "admin", Source: "admin:task",
	}

	// 缺省 ⇒ 服务端生成（非空 + 合法字符集 + 派生 usage id 不超列宽）
	generated := base
	input, err := generated.build()
	if err != nil {
		t.Fatalf("缺省 request_id 提交被拒: %v", err)
	}
	if input.RequestID == "" {
		t.Fatal("缺省 request_id 未生成（部分唯一索引不覆盖空值 ⇒ 幂等会失效）")
	}
	if derived := taskUsageRequestID(input.RequestID, MaxTaskMaxAttempts, maxTaskEmbeddingInputs/maxUpstreamEmbeddingBatch); len(derived) > 64 {
		t.Fatalf("派生的 usage request_id 超 varchar(64)：%q（%d 字符）", derived, len(derived))
	}

	// 合法自定义 ⇒ 原样保留
	custom := base
	custom.RequestID = "  order:2026-10-24_batch.1  "
	input, err = custom.build()
	if err != nil {
		t.Fatalf("合法自定义 request_id 被拒: %v", err)
	}
	if input.RequestID != "order:2026-10-24_batch.1" {
		t.Fatalf("自定义 request_id 未去空白保留：%q", input.RequestID)
	}

	// 非法 ⇒ 拒绝（长度 / 字符集）
	for _, bad := range []string{strings.Repeat("x", maxTaskRequestIDLen+1), "has space", "换行\n", "emoji😀"} {
		req := base
		req.RequestID = bad
		if _, err := req.build(); err == nil {
			t.Fatalf("非法 request_id %q 未被拒绝", bad)
		}
	}
}

func TestTaskCheckPayloadSizeGuard(t *testing.T) {
	if err := checkTaskPayloadSize(make([]byte, maxTaskPayloadBytes)); err != nil {
		t.Fatalf("恰好达上限应放行: %v", err)
	}
	if err := checkTaskPayloadSize(make([]byte, maxTaskPayloadBytes+1)); err == nil {
		t.Fatal("超上限 1 字节必须被拒（防单条任务把库塞爆）")
	}
}

// ===== usage request_id 派生 =====

func TestTaskUsageRequestIDDerivationAndBounds(t *testing.T) {
	// chat：不分片，只带尝试序号
	chat := Task{TaskType: TaskTypeChat, Status: TaskStatusFailed, RequestID: "rid-1", Attempt: 2, ProgressTotal: 1}
	chatIDs := taskUsageRequestIDs(chat)
	wantChat := []string{"task-rid-1-a1", "task-rid-1-a2"}
	if strings.Join(chatIDs, ",") != strings.Join(wantChat, ",") {
		t.Fatalf("chat 派生集不符: %v（期望 %v）", chatIDs, wantChat)
	}

	// embedding：尝试 × 分片（45 条 ⇒ 3 片）
	embedding := Task{TaskType: TaskTypeEmbedding, Status: TaskStatusSuccess, RequestID: "rid-2", Attempt: 1, ProgressTotal: 45}
	embeddingIDs := taskUsageRequestIDs(embedding)
	wantEmbedding := []string{"task-rid-2-a1-s1", "task-rid-2-a1-s2", "task-rid-2-a1-s3"}
	if strings.Join(embeddingIDs, ",") != strings.Join(wantEmbedding, ",") {
		t.Fatalf("embedding 派生集不符: %v（期望 %v）", embeddingIDs, wantEmbedding)
	}

	// 未领取过（attempt=0）⇒ 没有任何 usage 行可查
	if ids := taskUsageRequestIDs(Task{TaskType: TaskTypeChat, RequestID: "rid-3", ProgressTotal: 1}); len(ids) != 0 {
		t.Fatalf("attempt=0 不应派生 usage id: %v", ids)
	}

	// 上界：最大条数 × 最大尝试数 ⇒ 派生集不超过 maxTaskUsageIDs，且每个 id 都在 varchar(64) 内
	worst := Task{
		TaskType: TaskTypeEmbedding, RequestID: strings.Repeat("r", maxTaskRequestIDLen),
		Attempt: MaxTaskMaxAttempts, ProgressTotal: maxTaskEmbeddingInputs,
	}
	ids := taskUsageRequestIDs(worst)
	if len(ids) == 0 || len(ids) > maxTaskUsageIDs {
		t.Fatalf("派生集规模失控: %d（上限 %d）", len(ids), maxTaskUsageIDs)
	}
	for _, id := range ids {
		if len(id) > 64 {
			t.Fatalf("派生 id 超列宽: %q（%d 字符）", id, len(id))
		}
	}
}

// ===== 结果编码 / 摘要 / 导出 =====

func TestTaskExportLinesMatchResultEntries(t *testing.T) {
	// chat：单行
	chatRaw, err := encodeChatTaskResult(&ChatResult{
		ID: "cmpl-1", Model: "gpt-test", Content: "你好，世界", FinishReason: "stop",
		PromptTokens: 3, CompletionTokens: 5, TotalTokens: 8,
	})
	if err != nil {
		t.Fatalf("chat 结果编码失败: %v", err)
	}
	chatLines, err := taskExportLines(TaskTypeChat, chatRaw)
	if err != nil {
		t.Fatalf("chat 导出失败: %v", err)
	}
	if len(chatLines) != 1 {
		t.Fatalf("chat 导出行数应为 1，实际 %d", len(chatLines))
	}
	var chatLine struct {
		Index       int    `json:"index"`
		Object      string `json:"object"`
		Content     string `json:"content"`
		TotalTokens int    `json:"total_tokens"`
	}
	if err := json.Unmarshal(chatLines[0], &chatLine); err != nil {
		t.Fatalf("chat 导出行为非法 JSON: %v", err)
	}
	if chatLine.Index != 0 || chatLine.Object != "chat.completion" || chatLine.Content != "你好，世界" || chatLine.TotalTokens != 8 {
		t.Fatalf("chat 导出内容不符: %+v", chatLine)
	}
	if got := taskResultEntryCount(TaskTypeChat, chatRaw); got != len(chatLines) {
		t.Fatalf("chat 条目数 %d 与导出行数 %d 不一致", got, len(chatLines))
	}

	// embedding：每向量一行、行数 = 条目数、位次与内容逐条对齐
	vectors := [][]float64{{1, 2, 3}, {4, 5, 6}, {7, 8, 9}}
	embeddingRaw, err := encodeEmbeddingTaskResult("bge-test", vectors)
	if err != nil {
		t.Fatalf("embedding 结果编码失败: %v", err)
	}
	lines, err := taskExportLines(TaskTypeEmbedding, embeddingRaw)
	if err != nil {
		t.Fatalf("embedding 导出失败: %v", err)
	}
	if len(lines) != len(vectors) {
		t.Fatalf("embedding 导出行数 %d ≠ 条目数 %d", len(lines), len(vectors))
	}
	if got := taskResultEntryCount(TaskTypeEmbedding, embeddingRaw); got != len(lines) {
		t.Fatalf("embedding 条目数 %d 与导出行数 %d 不一致", got, len(lines))
	}
	for index, line := range lines {
		var item struct {
			Index     int       `json:"index"`
			Object    string    `json:"object"`
			Embedding []float64 `json:"embedding"`
		}
		if err := json.Unmarshal(line, &item); err != nil {
			t.Fatalf("第 %d 行为非法 JSON: %v", index, err)
		}
		if item.Index != index || item.Object != "embedding" || len(item.Embedding) != 3 || item.Embedding[0] != vectors[index][0] {
			t.Fatalf("第 %d 行内容不符: %+v", index, item)
		}
	}

	// 无结果 / 未知类型
	if lines, err := taskExportLines(TaskTypeChat, nil); err != nil || len(lines) != 0 {
		t.Fatalf("空结果应导出 0 行且不报错: %v / %d 行", err, len(lines))
	}
	if _, err := taskExportLines("image", chatRaw); err == nil {
		t.Fatal("未知任务类型必须报错（不静默导空）")
	}
}

func TestTaskRowSummaryAndEntries(t *testing.T) {
	embeddingPayload, err := encodeTaskPayload(taskPayload{
		Model: "bge-test", Inputs: []string{"a", "b"}, BizType: "admin", Source: "admin:task",
	})
	if err != nil {
		t.Fatalf("载荷编码失败: %v", err)
	}
	embeddingResult, err := encodeEmbeddingTaskResult("bge-test", [][]float64{{1, 2, 3}, {4, 5, 6}})
	if err != nil {
		t.Fatalf("结果编码失败: %v", err)
	}
	row := newTaskRow(Task{
		TaskType: TaskTypeEmbedding, Status: TaskStatusSuccess,
		Payload: embeddingPayload, Result: embeddingResult, ProgressTotal: 2, ProgressDone: 2,
	})
	if row.Model != "bge-test" || row.InputsCount != 2 || row.Entries != 2 || row.Summary != "2 条 × 3 维" {
		t.Fatalf("embedding 列表行不符: %+v", row)
	}

	chatPayload, err := encodeTaskPayload(taskPayload{
		Model: "gpt-test", Messages: []Message{{Role: "user", Content: "hi"}},
		BizType: "admin", Source: "admin:task",
	})
	if err != nil {
		t.Fatalf("载荷编码失败: %v", err)
	}
	// 未完成：摘要给「消息条数」，不给结果
	row = newTaskRow(Task{TaskType: TaskTypeChat, Status: TaskStatusPending, Payload: chatPayload, ProgressTotal: 1})
	if row.MessagesCount != 1 || row.Summary != "1 条消息" || row.Entries != 0 {
		t.Fatalf("chat 未完成列表行不符: %+v", row)
	}
	// 失败：摘要给错误（截断 60 字）
	row = newTaskRow(Task{
		TaskType: TaskTypeChat, Status: TaskStatusFailed, Payload: chatPayload,
		Error: strings.Repeat("boom", 40),
	})
	if !strings.HasPrefix(row.Summary, "失败: ") || len([]rune(row.Summary)) > 70 {
		t.Fatalf("失败行摘要不符: %q", row.Summary)
	}
	// 载荷损坏不炸：摘要留空
	row = newTaskRow(Task{TaskType: TaskTypeChat, Status: TaskStatusPending, Payload: datatypes.JSON("not json")})
	if row.Summary != "" || row.MessagesCount != 0 {
		t.Fatalf("损坏载荷应摘要留空: %+v", row)
	}
}

// ===== 列表筛选与 wheres 白名单 =====

func TestTaskListRequestWheresAndClosedSets(t *testing.T) {
	// 合法：闭集筛选 + 一条 wheres + 白名单内排序
	req := taskListRequest{
		listRequest: listRequest{
			Page: 1, Limit: 20, Sort: "create_time", Order: "asc",
			Wheres: []whereGroup{{Wheres: []whereCondition{
				{Field: "status", Operator: "eq", Value: TaskStatusSuccess},
				{Field: "task_type", Operator: "IN", Value: []any{TaskTypeChat, TaskTypeEmbedding}},
			}, Or: true}},
		},
		Status: TaskStatusFailed, TaskType: TaskTypeEmbedding,
	}
	filter, groups, err := req.filter()
	if err != nil {
		t.Fatalf("合法列表入参被拒: %v", err)
	}
	if filter.Status != TaskStatusFailed || filter.TaskType != TaskTypeEmbedding || len(groups) != 1 {
		t.Fatalf("列表入参收敛不符: %+v / %d 组", filter, len(groups))
	}

	// 非法字段 / 操作符 / 闭集外取值 / 白名单外排序：一律拒（不静默忽略）
	bad := []struct {
		name string
		req  taskListRequest
	}{
		{"wheres 字段不在白名单", taskListRequest{listRequest: listRequest{Wheres: []whereGroup{{Wheres: []whereCondition{
			{Field: "payload", Operator: "eq", Value: "x"}}}}}}},
		{"wheres 操作符不在词表", taskListRequest{listRequest: listRequest{Wheres: []whereGroup{{Wheres: []whereCondition{
			{Field: "status", Operator: "REGEXP", Value: "x"}}}}}}},
		{"wheres 模糊操作符用在非文本列", taskListRequest{listRequest: listRequest{Wheres: []whereGroup{{Wheres: []whereCondition{
			{Field: "attempt", Operator: "LIKE", Value: "x"}}}}}}},
		{"status 闭集外", taskListRequest{Status: "done"}},
		{"task_type 闭集外", taskListRequest{TaskType: "image"}},
		{"排序字段不在白名单", taskListRequest{listRequest: listRequest{Sort: "payload"}}},
	}
	for _, tc := range bad {
		if _, _, err := tc.req.filter(); err == nil {
			t.Fatalf("%s：必须被拒", tc.name)
		}
	}

	// 空组按宿主管家口径忽略（不报错、不进 groups）
	req = taskListRequest{listRequest: listRequest{Wheres: []whereGroup{{Wheres: nil}}}}
	if _, groups, err := req.filter(); err != nil || len(groups) != 0 {
		t.Fatalf("空 wheres 组应被忽略: %v / %d 组", err, len(groups))
	}
}

// TestTaskLoadSettingsFallbackDefaults 钉死「读配置失败不阻断」的取值口径（纯函数侧）。
func TestTaskParseSettingsPartialValues(t *testing.T) {
	got := parseTaskSettings(map[string]string{
		configKeyTaskPollSeconds: "1",
	})
	if got.PollSeconds != 1 || got.MaxAttempts != DefaultTaskMaxAttempts {
		t.Fatalf("只配一个键时应另一个走缺省: %+v", got)
	}
}
