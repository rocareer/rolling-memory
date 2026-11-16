// plugin: ai v0.2.0
// billing.go 是计费与 token 计数的唯一实现。
//
// 金额纪律：
//   - cost = round(prompt/1e6*price_input + completion/1e6*price_output, 6)；
//   - 单价是「每百万 token」，numeric(12,6) 用**字符串**承载，全程不经过 float64；
//   - 内部用 big.Int 微单位（USD×1e6），half-away-from-zero 舍入到 6 位；
//   - 解析层显式拒 NaN/Inf/空串/负数/非十进制形态——PostgreSQL 的 CHECK(x>=0)
//     拦不住 NaN（NaN 被 PG 视为大于一切非 NaN 数值）。
//
// token 纪律：
//   - 上游 usage 优先；上游 prompt+completion 均为 0 且 encoding 非空、消息不含多模态媒体
//     才做本地近似估算；其余 tokens_source=”（本次无计数，如失败行）；
//   - 近似公式（整数量化）：tokens = ceil(cjk*num/den) + ceil(other/4)，
//     (num,den) = tokenz-r 4/5（唯一合法编码；tiktoken 已停用）；encoding=” 根本不调用；
//   - 非空输入绝不返回 0；不声明与真实 BPE 一致，错误文案必须写明「近似估算」。
package ai

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync/atomic"
	"time"
)

// 金额精度与单价分母。
const (
	// MicroPerUnit 是 6 位小数的放大倍数（USD → 微美元）。
	MicroPerUnit int64 = 1_000_000
	// TokensPerPriceUnit 是「每百万 token」的单价分母。
	TokensPerPriceUnit int64 = 1_000_000
	// MaxPriceMicro 对应 numeric(12,6) 的上界 999999.999999。
	MaxPriceMicro int64 = 999_999_999_999
	// DefaultCurrency 币种默认值（插件配置 currency 为空时）。
	DefaultCurrency = "USD"
)

// token 估算编码（plugin_ai_model.encoding 的有效值；” 表示关闭估算与预检；全域唯一合法编码为 tokenz-r，tiktoken 已停用）。
const (
	EncodingNone    = ""
	EncodingTokenzR = "tokenz-r"
)

// usage 状态闭集（plugin_ai_usage.status 的 CHECK）。
const (
	UsageStatusSuccess = "success"
	UsageStatusFail    = "fail"
	UsageStatusTimeout = "timeout"
	UsageStatusBreaker = "breaker" // 保留枚举；v1 无写入点
)

// token 来源闭集（plugin_ai_usage.tokens_source 的 CHECK）。
const (
	TokensSourceUsage    = "usage"
	TokensSourceEstimate = "estimate"
	TokensSourceNone     = ""
)

// ErrEstimateDisabled 表示 encoding=”：既不估算也不做上下文预检。
var ErrEstimateDisabled = errors.New("encoding 为空，未启用本地 token 估算")

// Usage 是 plugin_ai_usage 的领域行，同时是 GORM 行模型。
//
// 每次真实上游尝试一行（(request_id,attempt) 幂等）；流式成功行 stream=1，
// **流式「有块、无收尾」的补偿行也 stream=1 并置 billed_partial=1**
// （见 service.settleStreamFailure 的分支③说明），
// 非流式失败行与「无块」的流式失败行 stream=0、无 token 计数。
// Cost 是 6 位小数字符串，出口不做浮点运算。
type Usage struct {
	ID               uint64 `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	RequestID        string `gorm:"column:request_id;type:varchar(64);not null;default:''" json:"request_id"`
	Attempt          int    `gorm:"column:attempt;not null;default:0" json:"attempt"`
	ModelID          uint64 `gorm:"column:model_id;not null;default:0" json:"model_id"`
	ModelName        string `gorm:"column:model_name;type:varchar(100);not null;default:''" json:"model_name"`
	ChannelID        int64  `gorm:"column:channel_id;not null;default:0" json:"channel_id"`
	ChannelName      string `gorm:"column:channel_name;type:varchar(50);not null;default:''" json:"channel_name"`
	CallType         string `gorm:"column:call_type;type:varchar(16);not null;default:'chat'" json:"call_type"`
	BizType          string `gorm:"column:biz_type;type:varchar(30);not null;default:''" json:"biz_type"`
	Source           string `gorm:"column:source;type:varchar(30);not null;default:''" json:"source"`
	Stream           int16  `gorm:"column:stream;not null;default:0" json:"stream"`
	PromptTokens     int    `gorm:"column:prompt_tokens;not null;default:0" json:"prompt_tokens"`
	CompletionTokens int    `gorm:"column:completion_tokens;not null;default:0" json:"completion_tokens"`
	TotalTokens      int    `gorm:"column:total_tokens;not null;default:0" json:"total_tokens"`
	CachedTokens     int64  `gorm:"column:cached_tokens;not null;default:0" json:"cached_tokens"`
	TokensSource     string `gorm:"column:tokens_source;type:varchar(16);not null;default:''" json:"tokens_source"`
	Cost             string `gorm:"column:cost;type:numeric(12,6);not null;default:0" json:"cost"`
	Currency         string `gorm:"column:currency;type:varchar(10);not null;default:'USD'" json:"currency"`
	LatencyMS        int    `gorm:"column:latency_ms;not null;default:0" json:"latency_ms"`
	Status           string `gorm:"column:status;type:varchar(16);not null;default:'success'" json:"status"`
	// BilledPartial 是「部分计费」审计标记（迁移 2 落的真列，CHECK IN (0,1)）：
	// 1=流式「有块、无收尾」的补偿结算行（上游已产出内容却没收尾，按已聚合内容部分计费）；
	// 0=正常行（含成功行与无 token 计数的失败行）。本行随 Usage 直接序列化 ⇒ usage 接口自动回显。
	BilledPartial int16 `gorm:"column:billed_partial;not null;default:0" json:"billed_partial"`
	// ErrorCode / ErrorMessage / ErrorClass 沿用失败分类闭集；部分计费行的 status 仍是 fail|timeout。
	ErrorCode    string `gorm:"column:error_code;type:varchar(100);not null;default:''" json:"error_code"`
	ErrorMessage string `gorm:"column:error_message;type:varchar(500);not null;default:''" json:"error_message"`
	ErrorClass   string `gorm:"column:error_class;type:varchar(20);not null;default:''" json:"error_class"`
	CreateTime   int64  `gorm:"column:create_time;not null;default:0" json:"create_time"`
	UpdateTime   int64  `gorm:"column:update_time;not null;default:0" json:"update_time"`
}

// TableName 物理表名（前缀由 model.go 的 table() 单点拼接）。
func (Usage) TableName() string { return table("plugin_ai_usage") }

// UsageResolution 是一次调用的 token 计数结果。
type UsageResolution struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CachedTokens     int64
	Source           string
}

// ParsePriceMicro 把 numeric 字符串解析成「微单位」（USD×1e6）。
//
// 接受：空整数部分（.5）、尾随小数点（1.）、前导 '+'。
// 拒绝：空串/纯空白、NaN/Inf（含大小写与符号变体）、负数、非十进制、超过 6 位小数
// 的部分按 half-away-from-zero 舍入（第 7 位 ≥5 进位）、超出 numeric(12,6) 上界。
func ParsePriceMicro(raw string) (int64, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, errors.New("单价不能为空")
	}
	lower := strings.ToLower(s)
	switch lower {
	case "nan", "+nan", "-nan",
		"inf", "+inf", "-inf",
		"infinity", "+infinity", "-infinity":
		return 0, fmt.Errorf("单价必须是有限十进制数: %q", raw)
	}
	if strings.HasPrefix(s, "-") {
		return 0, fmt.Errorf("单价不能为负: %q", raw)
	}
	s = strings.TrimPrefix(s, "+")
	if s == "" {
		return 0, fmt.Errorf("单价不是合法十进制数: %q", raw)
	}
	if strings.Count(s, ".") > 1 {
		return 0, fmt.Errorf("单价不是合法十进制数: %q", raw)
	}
	intPart, fracPart, _ := strings.Cut(s, ".")
	if intPart == "" && fracPart == "" {
		return 0, fmt.Errorf("单价不是合法十进制数: %q", raw)
	}
	for _, r := range intPart {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("单价不是合法十进制数: %q", raw)
		}
	}
	for _, r := range fracPart {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("单价不是合法十进制数: %q", raw)
		}
	}

	micro := new(big.Int)
	if intPart != "" {
		v, ok := new(big.Int).SetString(intPart, 10)
		if !ok {
			return 0, fmt.Errorf("单价不是合法十进制数: %q", raw)
		}
		micro.Mul(v, big.NewInt(MicroPerUnit))
	}
	if len(fracPart) > 6 {
		head, _ := new(big.Int).SetString(fracPart[:6], 10)
		if head != nil {
			micro.Add(micro, head)
		}
		// half-away-from-zero：正数下第 7 位 ≥5 即进位。
		if fracPart[6] >= '5' {
			micro.Add(micro, big.NewInt(1))
		}
	} else {
		padded := fracPart + strings.Repeat("0", 6-len(fracPart))
		if padded != "" {
			frac, _ := new(big.Int).SetString(padded, 10)
			if frac != nil {
				micro.Add(micro, frac)
			}
		}
	}
	if !micro.IsInt64() {
		return 0, fmt.Errorf("单价超出 numeric(12,6) 范围: %q", raw)
	}
	v := micro.Int64()
	if v < 0 {
		return 0, fmt.Errorf("单价不能为负: %q", raw)
	}
	if v > MaxPriceMicro {
		return 0, fmt.Errorf("单价超出 numeric(12,6) 范围: %q", raw)
	}
	return v, nil
}

// FormatMicro 把微单位格式化成固定 6 位小数的字符串（不做浮点运算）。
func FormatMicro(micro int64) string {
	sign := ""
	if micro < 0 {
		sign = "-"
		micro = -micro
	}
	return fmt.Sprintf("%s%d.%06d", sign, micro/MicroPerUnit, micro%MicroPerUnit)
}

// ComputeCostMicro 返回 6 位小数的微单位成本。
//
// 先把两段乘积求和再统一舍入一次（不要分段舍入，否则边界会差 1 微单位）：
//
//	raw = prompt*price_input_micro + completion*price_output_micro
//	cost_micro = round(raw / 1e6, half-away-from-zero)
//
// 结果必须落在 numeric(12,6) 之内（usage.cost 与单价的列宽同口径），否则返回错误。
func ComputeCostMicro(promptTokens, completionTokens int, priceInput, priceOutput string) (int64, error) {
	if promptTokens < 0 || completionTokens < 0 {
		return 0, errors.New("token 数不能为负")
	}
	pin, err := ParsePriceMicro(priceInput)
	if err != nil {
		return 0, fmt.Errorf("输入单价: %w", err)
	}
	pout, err := ParsePriceMicro(priceOutput)
	if err != nil {
		return 0, fmt.Errorf("输出单价: %w", err)
	}

	raw := new(big.Int).Mul(big.NewInt(int64(promptTokens)), big.NewInt(pin))
	raw.Add(raw, new(big.Int).Mul(big.NewInt(int64(completionTokens)), big.NewInt(pout)))

	den := big.NewInt(MicroPerUnit)
	q, r := new(big.Int).QuoRem(raw, den, new(big.Int))
	// raw >= 0（单价与 token 均非负），half-away-from-zero 即 2*r >= den 时进位。
	if r.Cmp(new(big.Int).Rsh(den, 1)) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	if !q.IsInt64() {
		return 0, errors.New("成本超出可表示范围")
	}
	// numeric(12,6) 上界（与 ParsePriceMicro 同口径）：超出即拒，绝不把写不进 usage.cost 的值
	// 交给写库层——那会让整行 INSERT 报 22003 而 usage 是计费/审计行（写入失败只记日志 ⇒ 静默丢行）。
	if q.Cmp(big.NewInt(MaxPriceMicro)) > 0 {
		return 0, fmt.Errorf("成本超出 numeric(12,6) 上界（%s）", FormatMicro(MaxPriceMicro))
	}
	return q.Int64(), nil
}

// ComputeCost 返回 6 位小数的成本字符串。
func ComputeCost(promptTokens, completionTokens int, priceInput, priceOutput string) (string, error) {
	micro, err := ComputeCostMicro(promptTokens, completionTokens, priceInput, priceOutput)
	if err != nil {
		return "", err
	}
	return FormatMicro(micro), nil
}

// cjkRange 是 token 估算的 CJK 类码点范围（含端点）。
//
// 与既定口径一致：U+3000–303F / U+3040–30FF / U+3400–4DBF / U+4E00–9FFF /
// U+AC00–D7AF / U+F900–FAFF / U+FF00–FFEF。
var cjkRanges = [][2]rune{
	{0x3000, 0x303F},
	{0x3040, 0x30FF},
	{0x3400, 0x4DBF},
	{0x4E00, 0x9FFF},
	{0xAC00, 0xD7AF},
	{0xF900, 0xFAFF},
	{0xFF00, 0xFFEF},
}

func isCJK(r rune) bool {
	for _, rg := range cjkRanges {
		if r >= rg[0] && r <= rg[1] {
			return true
		}
	}
	return false
}

func ceilDiv(a, b int) int {
	if a <= 0 {
		return 0
	}
	return (a + b - 1) / b
}

// EstimateTokens 是按 encoding 分档的近似 token 计数。
//
// 单次线性 pass、零分配（不构造 rune 切片）、整数算术：
//
//	tokens = ceil(cjk*num/den) + ceil(other/4)
//
// encoding=” 返回 ErrEstimateDisabled；未知编码返回错误（不静默按 0 记）。
func EstimateTokens(encoding, text string) (int, error) {
	var num, den int
	switch encoding {
	case EncodingNone:
		return 0, ErrEstimateDisabled
	case EncodingTokenzR:
		num, den = 4, 5
	default:
		return 0, fmt.Errorf("未知或已退役的 encoding %q（只支持 %q/%q，tiktoken 已退役）", encoding, EncodingNone, EncodingTokenzR)
	}

	cjk, other := 0, 0
	for _, r := range text {
		if isCJK(r) {
			cjk++
		} else {
			other++
		}
	}
	return ceilDiv(cjk*num, den) + ceilDiv(other, 4), nil
}

// ResolveUsageCounts 按三档优先级产出 token 计数。
//
//	① 上游 usage 优先：prompt 或 completion 任一 >0 ⇒ tokens_source='usage'；
//	② 两者都为 0（上游返回 0 不等于没用量）且 encoding 非空、非多模态 ⇒ 本地估算；
//	③ 其余 ⇒ tokens_source=''（本次无计数）。
//
// total_tokens 恒 = prompt+completion；cached_tokens 只透传、不进成本公式。
func ResolveUsageCounts(promptTokens, completionTokens int, cachedTokens int64,
	encoding, promptText, completionText string, hasMedia bool) UsageResolution {

	if promptTokens < 0 {
		promptTokens = 0
	}
	if completionTokens < 0 {
		completionTokens = 0
	}
	if promptTokens > 0 || completionTokens > 0 {
		return UsageResolution{
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      promptTokens + completionTokens,
			CachedTokens:     cachedTokens,
			Source:           TokensSourceUsage,
		}
	}

	res := UsageResolution{CachedTokens: cachedTokens}
	if encoding == EncodingNone || hasMedia {
		return res
	}
	p, err := EstimateTokens(encoding, promptText)
	if err != nil {
		return res
	}
	c, err := EstimateTokens(encoding, completionText)
	if err != nil {
		return res
	}
	if p == 0 && c == 0 {
		// 空输入：本次没有可计数的内容，保持 tokens_source=''。
		return res
	}
	res.PromptTokens = p
	res.CompletionTokens = c
	res.TotalTokens = p + c
	res.Source = TokensSourceEstimate
	return res
}

// CheckContextWindow 做上下文预检。
//
// 只在 encoding 非空、context_window>0、消息不含多模态媒体时生效；
// 超过窗口返回的错误文案必须含「近似估算」。
func CheckContextWindow(encoding string, contextWindow int, text string, hasMedia bool) (int, error) {
	if encoding == EncodingNone || contextWindow <= 0 || hasMedia {
		return 0, nil
	}
	n, err := EstimateTokens(encoding, text)
	if err != nil {
		// 未知 encoding 按「未启用」处理：不阻断请求。
		return 0, nil
	}
	if n > contextWindow {
		return n, fmt.Errorf("prompt 超出模型上下文窗口: %d > %d（近似估算）", n, contextWindow)
	}
	return n, nil
}

// ContentText 把 OpenAI 消息的 content（string / 多模态数组 / null）归一成纯文本。
func ContentText(content any) string {
	switch v := content.(type) {
	case nil:
		return ""
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, part := range v {
			b.WriteString(ContentText(part))
		}
		return b.String()
	case []string:
		return strings.Join(v, "")
	case map[string]any:
		if text, ok := v["text"].(string); ok {
			return text
		}
		if content, ok := v["content"]; ok {
			return ContentText(content)
		}
		return ""
	default:
		return fmt.Sprint(v)
	}
}

// MessagesText 把整组 messages 拼成估算用文本（含 role，近似口径）。
func MessagesText(messages []Message) string {
	var b strings.Builder
	for _, m := range messages {
		if m.Role != "" {
			b.WriteString(m.Role)
			b.WriteByte('\n')
		}
		b.WriteString(ContentText(m.Content))
		b.WriteByte('\n')
	}
	return b.String()
}

// EmbeddingInputText 把 embedding input（字符串或字符串数组）拼成估算用文本。
func EmbeddingInputText(input any) string {
	switch v := input.(type) {
	case nil:
		return ""
	case string:
		return v
	case []string:
		return strings.Join(v, "\n")
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				parts = append(parts, s)
				continue
			}
			parts = append(parts, ContentText(item))
		}
		return strings.Join(parts, "\n")
	default:
		return fmt.Sprint(v)
	}
}

// HasMultimodalMessages 判断消息是否含多模态媒体。
//
// 触发 token 估算豁免的键：
// input_audio / image_url / audio_url / video_url / file，或字符串内容以 data: 开头。
func HasMultimodalMessages(messages []Message) bool {
	for _, m := range messages {
		if hasMultimodalValue(m.Content, 0) {
			return true
		}
	}
	return false
}

func hasMultimodalValue(v any, depth int) bool {
	if depth > 8 {
		return false
	}
	switch x := v.(type) {
	case nil:
		return false
	case string:
		return strings.HasPrefix(strings.TrimSpace(x), "data:")
	case []any:
		for _, item := range x {
			if hasMultimodalValue(item, depth+1) {
				return true
			}
		}
	case []map[string]any:
		for _, item := range x {
			if hasMultimodalValue(item, depth+1) {
				return true
			}
		}
	case map[string]any:
		for k, val := range x {
			switch k {
			case "image_url", "input_audio", "audio_url", "video_url", "file":
				return true
			case "type":
				if s, ok := val.(string); ok && isMediaType(s) {
					return true
				}
			}
			if hasMultimodalValue(val, depth+1) {
				return true
			}
		}
	}
	return false
}

func isMediaType(s string) bool {
	switch s {
	case "image_url", "image", "input_audio", "audio_url", "audio", "video_url", "video", "file":
		return true
	default:
		return false
	}
}

// NewRequestID 生成 ai_+24 个十六进制字符（12 字节随机数）。
//
// 同一次调用的所有降级尝试共享同一个 request_id；usage 的唯一键是
// (request_id, attempt)。crypto/rand 不可用时用时间戳+计数器兜底，仍保证 24 hex。
func NewRequestID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err == nil {
		return "ai_" + hex.EncodeToString(b[:])
	}
	binary.BigEndian.PutUint64(b[0:8], uint64(time.Now().UnixNano()))
	binary.BigEndian.PutUint32(b[8:12], uint32(atomic.AddUint64(&requestIDSeq, 1)))
	return "ai_" + hex.EncodeToString(b[:])
}

var requestIDSeq uint64

// CurrencyOrDefault 空值回落 USD（插件配置默认值）。
func CurrencyOrDefault(currency string) string {
	currency = strings.TrimSpace(currency)
	if currency == "" {
		return DefaultCurrency
	}
	return currency
}
