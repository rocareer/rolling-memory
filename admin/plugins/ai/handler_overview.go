// plugin: ai v0.2.0
// handler_overview.go 是总览与插件设置：
//
//	POST /overview/index   只读总览（一次聚合、零写入）
//	POST /settings/save    插件自管设置面（宿主配置页不会为 plugin.<名> 分组生成 tab，故由插件自管）
//
// 总览字段与窗口对齐源 Overview.php:44/100-137：h24 = now-86400（**近 24 小时**，不是自然日）、
// d7 = 近 7 日单一聚合；d7_series（7 个自然日逐日趋势）是本实现相对源的加项（ask 的「近 7 日趋势」）。
// 设置面纪律：只 UPDATE 白名单内**已存在**的 plugin_ai_* 行——不动 config_group、不增删行。
// api_key 三态（缺省=不修改 / ""=显式清空 / 非空=设置）之外另加**最小长度 ≥16 字符**（源侧无此校验，
// 属本品加项）：公开面 Bearer 的爆破成本与长度直接相关，与 redact.go 的短值脱敏配套（源头挡住短密钥）。
//
// 配置面共 **17 键**（plugin.go 的 pluginConfigKeys 是唯一键表）：v1 的 7 键 + 本批 10 键
// （SSE 三上限、公开面限流三参数、渠道 Gate 两缺省、任务 worker 两键）。
// 新增键的三条纪律：① 键名常量归属各自读出口文件（本文件只引用，不重复声明）；
// ② 回显取**生效值**（与运行面同一份归一函数，见 plugin.go 的 parsePluginSettings）；
// ③ 保存成功后就地失效三处进程内缓存（invalidatePluginSettings 串了限流与 Gate 两处）。
package ai

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/database"
	"github.com/ai-go-hub/ai-go-admin/internal/kit/httpx"
	"github.com/ai-go-hub/ai-go-admin/internal/model"

	"github.com/gin-gonic/gin"
)

// ===== 总览 =====

// overviewIndex 只读总览：h24 / d7 / d7_series / 渠道健康 / 模型数 / Top 模型 / 最近失败 / 最近熔断 /
// 服务状态 / 设置现值。全流程零写入。
func (d deps) overviewIndex(c *gin.Context) {
	store := d.storeOrFail(c)
	if store == nil {
		return
	}
	ctx := c.Request.Context()
	now := nowUnix()

	aggregates, err := store.OverviewAggregates(ctx, now)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("读取用量聚合失败: "+err.Error()))
		return
	}
	health, err := store.ChannelHealth(ctx)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("读取渠道健康失败: "+err.Error()))
		return
	}
	models, err := store.ModelHealth(ctx)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("读取模型统计失败: "+err.Error()))
		return
	}
	breakers, err := store.RecentBreakerEvents(ctx, 5)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("读取最近熔断失败: "+err.Error()))
		return
	}
	set, err := d.loadSettings(ctx)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("读取插件配置失败: "+err.Error()))
		return
	}

	series := make([]gin.H, 0, len(aggregates.D7Series))
	for _, point := range aggregates.D7Series {
		series = append(series, gin.H{
			"day":          point.Day,
			"calls":        point.Calls,
			"total_tokens": point.TotalTokens,
			"cost":         formatCost(point.Cost),
		})
	}
	topModels := make([]gin.H, 0, len(aggregates.TopModels))
	for _, item := range aggregates.TopModels {
		topModels = append(topModels, gin.H{
			"model_id":     item.ModelID,
			"model_name":   item.ModelName,
			"calls":        item.Calls,
			"total_tokens": item.TotalTokens,
			"cost":         formatCost(item.Cost),
		})
	}
	recentFails := make([]gin.H, 0, len(aggregates.RecentFails))
	for _, row := range aggregates.RecentFails {
		recentFails = append(recentFails, gin.H{
			"id":            row.ID,
			"request_id":    row.RequestID,
			"model_name":    row.ModelName,
			"channel_name":  row.ChannelName,
			"status":        row.Status,
			"error_class":   row.ErrorClass,
			"error_code":    row.ErrorCode,
			"error_message": row.ErrorMessage,
			"create_time":   row.CreateTime,
		})
	}

	httpx.Success(c, httpx.WithData(gin.H{
		"h24": gin.H{
			"calls":          aggregates.H24.Calls,
			"success_calls":  aggregates.H24.Success,
			"fail_calls":     aggregates.H24.Fail + aggregates.H24.Timeout,
			"total_tokens":   aggregates.H24.TotalTokens,
			"cost":           formatCost(aggregates.H24.Cost),
			"avg_latency_ms": aggregates.H24.AvgLatencyMS,
		},
		"d7": gin.H{
			"calls":        aggregates.D7.Calls,
			"total_tokens": aggregates.D7.TotalTokens,
			"cost":         formatCost(aggregates.D7.Cost),
		},
		"d7_series": series,
		"channels": gin.H{
			"total":             health.Total,
			"enabled":           health.Enabled,
			"circuit_open":      health.CircuitOpen,
			"circuit_half_open": health.CircuitHalfOpen,
			"problems":          newChannelRows(health.Problems, set),
		},
		"models": gin.H{
			"total":   models.Total,
			"enabled": models.Enabled,
		},
		"top_models":      topModels,
		"recent_fails":    recentFails,
		"recent_breakers": breakers,
		"service": gin.H{
			// 能读到这一段就说明库可用（上面几次读取已实证）；key 未配置时公开面恒 503。
			"db":              true,
			"gateway_key_set": set.APIKeySet,
			"currency":        set.Currency,
		},
		"settings": settingsView(set),
		"time":     now,
	}))
}

// settingsView 设置面的回显（api_key 只回掩码 + 是否已配置；其余 16 项为**生效值**——
// 本批 10 键取自与运行面同一份归一函数，见 plugin.go 的 parsePluginSettings）。
//
// SSE 三上限回显**字节**（页面按 MiB 换算展示与提交）；0 在库里表示「未配」，
// 这里回的是回落缺省后的生效值（1MiB/8MiB/32MiB），避免页面显示 0 而运行面按 1MiB 拦。
func settingsView(set pluginSettings) gin.H {
	limits := set.sseLimits()
	return gin.H{
		"api_key":                  maskSecret(set.APIKey),
		"api_key_set":              set.APIKeySet,
		"breaker_fail_threshold":   set.BreakerFailThreshold,
		"breaker_cooldown_seconds": set.BreakerCooldownSeconds,
		"request_timeout":          set.RequestTimeout,
		"stream_timeout":           set.StreamTimeout,
		"currency":                 set.Currency,
		"allow_private_hosts":      boolTo01(set.AllowPrivateHosts),
		// ---- 本批 10 键（SSE 三上限 / 限流三参数 / 渠道 Gate 两缺省 / 任务 worker 两键）----
		"sse_max_line_bytes":        limits.MaxLineBytes,
		"sse_max_frame_bytes":       limits.MaxFrameBytes,
		"sse_max_accumulated_bytes": limits.MaxAccumulatedBytes,
		"rate_limit_rpm":            set.RateLimitRPM,
		"rate_limit_burst":          set.RateLimitBurst,
		"rate_limit_max_inflight":   set.RateLimitMaxInflight,
		"channel_gate_rpm_default":  set.ChannelGateRPMDefault,
		"channel_gate_tpm_default":  set.ChannelGateTPMDefault,
		"task_poll_seconds":         set.TaskPollSeconds,
		"task_max_attempts":         set.TaskMaxAttempts,
	}
}

// boolTo01 布尔 → 0/1（宿主二值列口径）。
func boolTo01(value bool) int {
	if value {
		return 1
	}
	return 0
}

// ===== 设置保存 =====

// settingsSaveRequest 设置入参。api_key 是**三态**：字段缺省 = 不修改 / ""= 显式清空 / 非空 = 设置。
//
// 本批 10 个新键同为指针三态（缺省不修改）；数值键的边界与运行面**同一份常量**（见 updates 的逐键校验），
// 免得「设置面允许 1e9、运行面 clamp 到 100 万」这类口径分叉。
type settingsSaveRequest struct {
	APIKey                 *string `json:"api_key"`
	BreakerFailThreshold   *int    `json:"breaker_fail_threshold"`
	BreakerCooldownSeconds *int    `json:"breaker_cooldown_seconds"`
	RequestTimeout         *int    `json:"request_timeout"`
	StreamTimeout          *int    `json:"stream_timeout"`
	Currency               *string `json:"currency"`
	AllowPrivateHosts      *int    `json:"allow_private_hosts"`

	SSEMaxLineBytes        *int `json:"sse_max_line_bytes"`
	SSEMaxFrameBytes       *int `json:"sse_max_frame_bytes"`
	SSEMaxAccumulatedBytes *int `json:"sse_max_accumulated_bytes"`

	RateLimitRPM         *int `json:"rate_limit_rpm"`
	RateLimitBurst       *int `json:"rate_limit_burst"`
	RateLimitMaxInflight *int `json:"rate_limit_max_inflight"`

	ChannelGateRPMDefault *int `json:"channel_gate_rpm_default"`
	ChannelGateTPMDefault *int `json:"channel_gate_tpm_default"`

	TaskPollSeconds *int `json:"task_poll_seconds"`
	TaskMaxAttempts *int `json:"task_max_attempts"`
}

// maxSSESettingBytes 是 SSE 三级上限的取值上界（1GiB）：0 = 回落缺省，
// 而更大的「上限」等于不限——那正是这三级护栏要防的（上游用无界流把内存吃光）。
const maxSSESettingBytes = 1 << 30

// minAPIKeyRunes 是网关 API Key 的最小长度（非空时校验；空串表示「显式清空」不受此限）。
//
// 依据：公开面 Bearer 的字典序爆破成本与长度直接相关（源侧无此校验，属本品加项）；
// 与 redact.go 的短值脱敏配套——设置面从源头挡住短密钥，出口再兜一层。
const minAPIKeyRunes = 16

// updates 收敛成「落库键 → 新值」（纯函数，可单测）：只含出现过的键，数值走边界校验。
func (r settingsSaveRequest) updates() (map[string]string, error) {
	updates := make(map[string]string, len(pluginConfigKeys))
	if r.APIKey != nil {
		// 显式清空（""）与设置（非空）都落在这里；缺省不改由指针 nil 区分。
		value := strings.TrimSpace(*r.APIKey)
		if value != "" && len([]rune(value)) < minAPIKeyRunes {
			return nil, fmt.Errorf("api_key 长度至少 %d 个字符（当前 %d 个）：过短的网关密钥易被撞库",
				minAPIKeyRunes, len([]rune(value)))
		}
		updates[configKeyAPIKey] = value
	}
	if r.BreakerFailThreshold != nil {
		if err := checkRange("breaker_fail_threshold", *r.BreakerFailThreshold, 0, 10000); err != nil {
			return nil, err
		}
		updates[configKeyBreakerFailThreshold] = strconv.Itoa(*r.BreakerFailThreshold)
	}
	if r.BreakerCooldownSeconds != nil {
		if err := checkRange("breaker_cooldown_seconds", *r.BreakerCooldownSeconds, 0, 86400); err != nil {
			return nil, err
		}
		updates[configKeyBreakerCooldown] = strconv.Itoa(*r.BreakerCooldownSeconds)
	}
	if r.RequestTimeout != nil {
		if err := checkRange("request_timeout", *r.RequestTimeout, 0, 600); err != nil {
			return nil, err
		}
		updates[configKeyRequestTimeout] = strconv.Itoa(*r.RequestTimeout)
	}
	if r.StreamTimeout != nil {
		if err := checkRange("stream_timeout", *r.StreamTimeout, 0, 86400); err != nil {
			return nil, err
		}
		updates[configKeyStreamTimeout] = strconv.Itoa(*r.StreamTimeout)
	}
	if r.Currency != nil {
		value := strings.TrimSpace(*r.Currency)
		if value == "" {
			return nil, errors.New("币种不能为空")
		}
		if len([]rune(value)) > 10 {
			return nil, errors.New("币种最长 10 个字符")
		}
		updates[configKeyCurrency] = value
	}
	if r.AllowPrivateHosts != nil {
		if *r.AllowPrivateHosts != 0 && *r.AllowPrivateHosts != 1 {
			return nil, errors.New("allow_private_hosts 只能是 0 或 1")
		}
		updates[configKeyAllowPrivateHosts] = strconv.Itoa(*r.AllowPrivateHosts)
	}
	// ---- 本批 10 键：0 一律表示「未配/不限」（运行面的归一函数会把 0 变成缺省或「不限」）----
	if r.SSEMaxLineBytes != nil {
		if err := checkRange("sse_max_line_bytes", *r.SSEMaxLineBytes, 0, maxSSESettingBytes); err != nil {
			return nil, err
		}
		updates[configKeySSEMaxLineBytes] = strconv.Itoa(*r.SSEMaxLineBytes)
	}
	if r.SSEMaxFrameBytes != nil {
		if err := checkRange("sse_max_frame_bytes", *r.SSEMaxFrameBytes, 0, maxSSESettingBytes); err != nil {
			return nil, err
		}
		updates[configKeySSEMaxFrameBytes] = strconv.Itoa(*r.SSEMaxFrameBytes)
	}
	if r.SSEMaxAccumulatedBytes != nil {
		if err := checkRange("sse_max_accumulated_bytes", *r.SSEMaxAccumulatedBytes, 0, maxSSESettingBytes); err != nil {
			return nil, err
		}
		updates[configKeySSEMaxAccumulatedBytes] = strconv.Itoa(*r.SSEMaxAccumulatedBytes)
	}
	if r.RateLimitRPM != nil {
		if err := checkRange("rate_limit_rpm", *r.RateLimitRPM, 0, rateLimitRPMMax); err != nil {
			return nil, err
		}
		updates[configKeyRateLimitRPM] = strconv.Itoa(*r.RateLimitRPM)
	}
	if r.RateLimitBurst != nil {
		if err := checkRange("rate_limit_burst", *r.RateLimitBurst, 0, rateLimitBurstMax); err != nil {
			return nil, err
		}
		updates[configKeyRateLimitBurst] = strconv.Itoa(*r.RateLimitBurst)
	}
	if r.RateLimitMaxInflight != nil {
		if err := checkRange("rate_limit_max_inflight", *r.RateLimitMaxInflight, 0, rateLimitMaxInflightMax); err != nil {
			return nil, err
		}
		updates[configKeyRateLimitMaxInflight] = strconv.Itoa(*r.RateLimitMaxInflight)
	}
	if r.ChannelGateRPMDefault != nil {
		if err := checkRange("channel_gate_rpm_default", *r.ChannelGateRPMDefault, 0, channelGateRPMMax); err != nil {
			return nil, err
		}
		updates[configKeyChannelGateRPMDefault] = strconv.Itoa(*r.ChannelGateRPMDefault)
	}
	if r.ChannelGateTPMDefault != nil {
		if err := checkRange("channel_gate_tpm_default", *r.ChannelGateTPMDefault, 0, channelGateTPMMax); err != nil {
			return nil, err
		}
		updates[configKeyChannelGateTPMDefault] = strconv.Itoa(*r.ChannelGateTPMDefault)
	}
	if r.TaskPollSeconds != nil {
		if err := checkRange("task_poll_seconds", *r.TaskPollSeconds, 0, MaxTaskPollSeconds); err != nil {
			return nil, err
		}
		updates[configKeyTaskPollSeconds] = strconv.Itoa(*r.TaskPollSeconds)
	}
	if r.TaskMaxAttempts != nil {
		if err := checkRange("task_max_attempts", *r.TaskMaxAttempts, 0, MaxTaskMaxAttempts); err != nil {
			return nil, err
		}
		updates[configKeyTaskMaxAttempts] = strconv.Itoa(*r.TaskMaxAttempts)
	}
	if len(updates) == 0 {
		return nil, errors.New("没有需要保存的设置项")
	}
	return updates, nil
}

// settingsSave 保存插件设置：只 UPDATE 已存在的白名单行，写后立即失效配置缓存
// （清空 api_key 后公开面下一次请求即 503 fail-closed）。
func (d deps) settingsSave(c *gin.Context) {
	var req settingsSaveRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	updates, err := req.updates()
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	ctx := c.Request.Context()
	saved, err := updatePluginSettings(ctx, updates)
	// 无论成功与否都失效缓存：部分键可能已经写入。失效面含本批三组读出口
	// （插件配置 + 限流三键 + 渠道 Gate 两缺省；任务两键每轮直读无需失效）。
	invalidatePluginSettings()
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	set, err := d.loadSettings(ctx)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("设置已保存，但重新读取配置失败: "+err.Error()))
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{"saved": saved, "settings": settingsView(set)}))
}

// updatePluginSettings 只 UPDATE 白名单内已存在的 configs 行（不增行、不删行、不动 config_group）。
func updatePluginSettings(ctx context.Context, updates map[string]string) ([]string, error) {
	db := database.DB()
	if db == nil {
		return nil, fmt.Errorf("%w（设置保存须在 database.Init() 之后）", ErrStoreUnavailable)
	}
	const configGroup = "plugin." + Name
	saved := make([]string, 0, len(updates))
	for _, key := range pluginConfigKeys { // 固定顺序：结果可预期
		value, ok := updates[key]
		if !ok {
			continue
		}
		// name + group 双条件：插件配置的落库归属是 plugin.<名>，只按 name 定位会与宿主
		// 其它分组里同名行（理论上）串写，与宿主配置写口的口径也不一致。
		result := db.WithContext(ctx).Model(&model.Config{}).
			Where("name = ? AND \"group\" = ?", key, configGroup).
			Update("value", value)
		if result.Error != nil {
			return saved, fmt.Errorf("写入配置项 %s 失败: %w", key, result.Error)
		}
		if result.RowsAffected == 0 {
			return saved, fmt.Errorf("配置项 %s 不存在（插件未安装或版本未补齐该行，先执行 aigo plugin install ai）", key)
		}
		saved = append(saved, key)
	}
	return saved, nil
}

// checkRange 数值边界检查（含下界与上界的可读文案）。
func checkRange(name string, value, low, high int) error {
	if value < low || value > high {
		return fmt.Errorf("%s 必须在 %d~%d 之间（当前 %d）", name, low, high, value)
	}
	return nil
}
