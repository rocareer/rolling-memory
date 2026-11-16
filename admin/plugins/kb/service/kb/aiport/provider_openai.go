// plugin: kb v1.0.0
// 默认 Provider：openai-compatible（OpenAI 兼容 /embeddings 与 /chat/completions 直连上游，
// 读 configs.ai 组的地址与密钥 + config/kb.yaml 的模型键）。
//
// D1（同款）：本文件是框架默认 Provider 的**唯一**载体，注册自包含于本文件的 init()——
//   - 下游部署可把本文件从构建/合并结果物理排除（文件独立 = 可物理排除的边界）
//     （sync-framework.sh 会 `git rm -f`）：产品必须走其受治理模型运行时（agent/egress/成本口径），
//     自写 HTTP 直连属违规，故默认 Provider 的合规只由物理排除保证；
//   - 因此：包内其余文件一律不得 import/引用本文件的符号（注册经 init 自注册进注册表），
//     否则排除本文件后编译断裂（包内其它文件一律不得引用其符号）；
//   - 配置指向未注册 Provider 时调用方 fail-closed（aiport.Embedder/Chatter），绝不回退到本实现。
package aiport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/repository/common"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/kbcfg"
)

// 上游请求策略常量（重试仅限取得响应头前；错误体截断）
const (
	// upstreamMaxAttempts 总尝试次数（含首次请求；最多 2 次重试）
	upstreamMaxAttempts = 3
	// upstreamRetryBase 每次重试等待的基础时长（第 n 次重试等待 base×(n-1)）
	upstreamRetryBase = 500 * time.Millisecond
	// upstreamErrorMaxChars 错误消息截断长度（字符，非字节）
	upstreamErrorMaxChars = 512
	// upstreamErrorReadBytes 读取上游错误体的上限（截断前先限量读，防大响应体）
	upstreamErrorReadBytes = 4096
	// upstreamMaxResponseBytes 成功响应体读取上限（嵌入批响应可达数百 KB：64 条 × 1024 维浮点 JSON；
	// 16MB 为宽裕上限——超限按读取失败响亮处理，不静默截断）
	upstreamMaxResponseBytes = 16 << 20
	// upstreamMaxRedirects 同主机重定向跟随上限（跨主机重定向一律不跟随）
	upstreamMaxRedirects = 5
)

// configs.ai 组键（本实现的上游凭证 / 默认模型 / 私网开关来源）
const (
	settingsGroupAI   = "ai"
	keyAPIURL         = "ai_api_url"
	keyAPIKey         = "ai_api_key"
	keyDefaultModel   = "ai_default_model"
	keyAllowPrivateIP = "ai_assistant_allow_private_url"
)

// init 自注册默认 Provider（D1：注册必须自包含于本文件，见文件头注释）
func init() {
	RegisterEmbedder(ProviderOpenAICompatible, func() Embedder { return &openAICompatibleEmbedder{} })
	RegisterChatter(ProviderOpenAICompatible, func() Chatter { return &openAICompatibleChatter{} })
}

// upstreamHTTPClient 上游客户端：不设总超时（总超时由调用方 ctx 控制），仅控制建连与响应头阶段
var upstreamHTTPClient = &http.Client{
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ResponseHeaderTimeout: 30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		IdleConnTimeout:       90 * time.Second,
	},
	// 不跟随跨域重定向：同主机重定向有限跟随，跨主机直接停止并回原响应
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) == 0 {
			return nil
		}
		if len(via) >= upstreamMaxRedirects {
			return errors.New("上游重定向次数过多")
		}
		if !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
			return http.ErrUseLastResponse
		}
		return nil
	},
}

// upstreamSettings 上游接线快照（每请求读一次；configs 表 + config/kb.yaml 合并）
type upstreamSettings struct {
	APIURL          string
	APIKey          string
	DefaultModel    string
	AllowPrivateURL bool
	EmbedModel      string
	EmbedDim        int
	TagModel        string
}

// loadUpstreamSettings 读取上游接线（configs.ai 组 + kb 配置段）；
// 配置不可读 ⇒ 错误（fail-closed：不得以空键发起调用）
func loadUpstreamSettings(ctx context.Context) (*upstreamSettings, error) {
	raw, err := common.NewConfigRepository().GetConfigsByGroup(ctx, settingsGroupAI)
	if err != nil {
		return nil, fmt.Errorf("kb: 读取 ai 组配置失败: %w", err)
	}
	kb := kbcfg.Get()
	st := &upstreamSettings{
		APIURL:          strings.TrimSpace(raw[keyAPIURL]),
		APIKey:          strings.TrimSpace(raw[keyAPIKey]),
		DefaultModel:    strings.TrimSpace(raw[keyDefaultModel]),
		AllowPrivateURL: parseConfigBool(raw[keyAllowPrivateIP], true),
		EmbedModel:      strings.TrimSpace(kb.Embed.Model),
		EmbedDim:        kb.Embed.Dim,
		TagModel:        strings.TrimSpace(kb.Tag.Model),
	}
	return st, nil
}

// ensureUpstream 上游可用性断言（地址/密钥齐备 + 地址过 SSRF 合规校验）
func (st *upstreamSettings) ensureUpstream(ctx context.Context) error {
	if st.APIURL == "" || st.APIKey == "" {
		return errors.New("kb: 上游未配置（configs.ai 组的 ai_api_url / ai_api_key 齐备后才可用）")
	}
	if err := validateUpstreamURL(ctx, st.APIURL, st.AllowPrivateURL); err != nil {
		return fmt.Errorf("kb: AI 服务地址不合规: %w", err)
	}
	return nil
}

// ==================== 嵌入 Provider ====================

// openAICompatibleEmbedder OpenAI 兼容嵌入 Provider（无状态；每请求读配置快照）
// settings 字段为测试注入缝（nil = 生产路径 loadUpstreamSettings）
type openAICompatibleEmbedder struct {
	settings func(ctx context.Context) (*upstreamSettings, error)
}

// loadSettings 读上游接线（注入优先；生产走 configs.ai 组 + kb 配置段）
func (p *openAICompatibleEmbedder) loadSettings(ctx context.Context) (*upstreamSettings, error) {
	if p.settings != nil {
		return p.settings(ctx)
	}
	return loadUpstreamSettings(ctx)
}

// Name 实现 Embedder
func (p *openAICompatibleEmbedder) Name() string { return ProviderOpenAICompatible }

// Identity 当前向量空间身份 = 配置的嵌入模型（kb.embed.model；空 ⇒ 噪声错误，调用方 fail-closed）
func (p *openAICompatibleEmbedder) Identity(ctx context.Context) (string, error) {
	st, err := p.loadSettings(ctx)
	if err != nil {
		return "", err
	}
	if st.EmbedModel == "" {
		return "", errors.New("kb: 嵌入模型未配置（kb.embed.model 为空，向量身份不可用）")
	}
	return st.EmbedModel, nil
}

// AnchorIdentity 语义切分锚身份 = <Provider 名>@<模型身份>（换模型/换实现 ⇒ 新 build_hash ⇒ 重产）
func (p *openAICompatibleEmbedder) AnchorIdentity(ctx context.Context) (string, error) {
	identity, err := p.Identity(ctx)
	if err != nil {
		return "", err
	}
	return ProviderOpenAICompatible + "@" + identity, nil
}

// embeddingsPayload 上游请求体
type embeddingsPayload struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// embeddingsResponse 上游响应体（按 index 重排序；标准 OpenAI 形状）
type embeddingsResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Model string `json:"model"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// Embed 批量嵌入（批内顺序保持；条数/维度/身份回显在返回前校验）
func (p *openAICompatibleEmbedder) Embed(ctx context.Context, meta CallMeta, inputs []string) (*EmbedResult, error) {
	meta = meta.Normalize()
	st, err := p.loadSettings(ctx)
	if err != nil {
		return nil, err
	}
	if st.EmbedModel == "" {
		return nil, errors.New("kb: 嵌入模型未配置（kb.embed.model 为空，向量身份不可用）")
	}
	if len(inputs) == 0 {
		return &EmbedResult{ModelIdentity: st.EmbedModel, Model: st.EmbedModel}, nil
	}
	if err := st.ensureUpstream(ctx); err != nil {
		return nil, err
	}

	payload, err := json.Marshal(embeddingsPayload{Model: st.EmbedModel, Input: inputs})
	if err != nil {
		return nil, fmt.Errorf("kb: 组装嵌入请求失败: %w", err)
	}
	body, err := upstreamPostJSON(ctx, st, "/embeddings", payload)
	if err != nil {
		return nil, err
	}
	var parsed embeddingsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("kb: 解析嵌入响应失败: %w", err)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return nil, fmt.Errorf("kb: 上游嵌入报错: %s", sanitizeUpstreamText(parsed.Error.Message, st.APIKey))
	}
	if len(parsed.Data) != len(inputs) {
		return nil, fmt.Errorf("kb: 嵌入响应条数不符（%d != %d）", len(parsed.Data), len(inputs))
	}
	ordered := make([][]float32, len(inputs))
	for i := range parsed.Data {
		idx := parsed.Data[i].Index
		if idx < 0 || idx >= len(inputs) {
			return nil, fmt.Errorf("kb: 嵌入响应序号越界（index=%d，条数=%d）", idx, len(inputs))
		}
		if len(parsed.Data[i].Embedding) == 0 {
			return nil, fmt.Errorf("kb: 嵌入响应含空向量（index=%d）", idx)
		}
		ordered[idx] = parsed.Data[i].Embedding
	}
	dim := len(ordered[0])
	for i := range ordered {
		if len(ordered[i]) == 0 {
			return nil, fmt.Errorf("kb: 嵌入响应缺向量行（index=%d）", i)
		}
		if len(ordered[i]) != dim {
			return nil, fmt.Errorf("kb: 嵌入响应维度不一致（%d != %d，index=%d）", len(ordered[i]), dim, i)
		}
	}
	if st.EmbedDim > 0 && dim != st.EmbedDim {
		return nil, fmt.Errorf("kb: 嵌入维度不符（上游 %d != kb.embed.dim %d；列存 halfvec 维度需一致）", dim, st.EmbedDim)
	}
	// 观测读数（不参与身份判定）：上游回显模型为空则回落请求模型
	model := strings.TrimSpace(parsed.Model)
	if model == "" {
		model = st.EmbedModel
	}
	return &EmbedResult{Vectors: ordered, ModelIdentity: st.EmbedModel, Model: model, Channel: ProviderOpenAICompatible}, nil
}

// ==================== 对话 Provider（kb:tag 候选生成） ====================

// openAICompatibleChatter OpenAI 兼容非流式对话 Provider（无状态；每请求读配置快照）
// settings 字段为测试注入缝（nil = 生产路径 loadUpstreamSettings）
type openAICompatibleChatter struct {
	settings func(ctx context.Context) (*upstreamSettings, error)
}

// loadSettings 读上游接线（注入优先；生产走 configs.ai 组 + kb 配置段）
func (p *openAICompatibleChatter) loadSettings(ctx context.Context) (*upstreamSettings, error) {
	if p.settings != nil {
		return p.settings(ctx)
	}
	return loadUpstreamSettings(ctx)
}

// Name 实现 Chatter
func (p *openAICompatibleChatter) Name() string { return ProviderOpenAICompatible }

// chatPayload 上游请求体（非流式）
type chatPayload struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatResponse 上游响应体（取首个 choice 的正文）
type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Model string `json:"model"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// Chat 非流式生成（模型 = kb.tag.model，空回落 ai_default_model）
func (p *openAICompatibleChatter) Chat(ctx context.Context, meta CallMeta, input string, vars map[string]any) (*ChatResult, error) {
	meta = meta.Normalize()
	st, err := p.loadSettings(ctx)
	if err != nil {
		return nil, err
	}
	model := st.TagModel
	if model == "" {
		model = st.DefaultModel
	}
	if model == "" {
		return nil, errors.New("kb: 对话模型未配置（kb.tag.model 与 ai_default_model 皆为空）")
	}
	if err := st.ensureUpstream(ctx); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(chatPayload{
		Model:    model,
		Messages: []chatMessage{{Role: "user", Content: input}},
		Stream:   false,
	})
	if err != nil {
		return nil, fmt.Errorf("kb: 组装对话请求失败: %w", err)
	}
	body, err := upstreamPostJSON(ctx, st, "/chat/completions", payload)
	if err != nil {
		return nil, err
	}
	var parsed chatResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("kb: 解析对话响应失败: %w", err)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return nil, fmt.Errorf("kb: 上游对话报错: %s", sanitizeUpstreamText(parsed.Error.Message, st.APIKey))
	}
	if len(parsed.Choices) == 0 {
		return nil, errors.New("kb: 上游对话响应无 choices")
	}
	outModel := strings.TrimSpace(parsed.Model)
	if outModel == "" {
		outModel = model
	}
	// vars 供实现映射模板变量：框架默认实现直接发单条 user 消息（v1 无 system 装配）
	return &ChatResult{Content: parsed.Choices[0].Message.Content, Model: outModel}, nil
}

// ==================== HTTP 公共面 ====================

// upstreamPostJSON 向上游 POST JSON（路径拼接 + 响应头前重试；返回响应体原文）。
// 重试仅限：网络错误、响应码 429/5xx；总尝试 upstreamMaxAttempts 次（含首次）。
func upstreamPostJSON(ctx context.Context, st *upstreamSettings, pathHint string, payload []byte) ([]byte, error) {
	endpoint := strings.TrimRight(st.APIURL, "/") + pathHint
	var lastErr error
	for attempt := 0; attempt < upstreamMaxAttempts; attempt++ {
		if attempt > 0 {
			wait := upstreamRetryBase * time.Duration(attempt)
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("kb: 上游调用被取消: %w", ctx.Err())
			case <-time.After(wait):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
		if err != nil {
			return nil, fmt.Errorf("kb: 构造上游请求失败: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+st.APIKey)

		resp, err := upstreamHTTPClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("kb: 上游请求失败: %w", sanitizeErr(err, st.APIKey))
			if ctx.Err() != nil {
				return nil, lastErr
			}
			continue
		}
		if resp.StatusCode == http.StatusOK {
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, upstreamMaxResponseBytes))
			_ = resp.Body.Close()
			if readErr != nil {
				lastErr = fmt.Errorf("kb: 读上游响应失败: %w", readErr)
				continue
			}
			return body, nil
		}
		// 非 200：只读错误体上限（防大响应体），429/5xx 退避重试，其余立即响亮失败
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, upstreamErrorReadBytes))
		_ = resp.Body.Close()
		statusErr := fmt.Errorf("kb: 上游状态码 %d: %s", resp.StatusCode, sanitizeUpstreamText(truncateChars(string(errBody), upstreamErrorMaxChars), st.APIKey))
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = statusErr
			continue
		}
		return nil, statusErr
	}
	return nil, lastErr
}

// validateUpstreamURL SSRF 合规校验：
// 仅 http/https；恒拒链路本地/未指定/组播；回环与私网由 ai_assistant_allow_private_url 控制。
func validateUpstreamURL(ctx context.Context, rawURL string, allowPrivate bool) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("地址无法解析: %w", err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("仅允许 http/https，当前: %q", parsed.Scheme)
	}
	host := strings.TrimSpace(parsed.Hostname())
	if host == "" {
		return errors.New("地址缺少主机名")
	}
	ips, err := resolveHostIPs(ctx, host)
	if err != nil {
		return err
	}
	for _, ip := range ips {
		if isAlwaysDeniedIP(ip) {
			return fmt.Errorf("地址指向链路本地/保留网段（%s），已拒绝", ip)
		}
		if !allowPrivate && isPrivateIP(ip) {
			return fmt.Errorf("地址指向回环或私网（%s），已按 ai_assistant_allow_private_url=0 拒绝", ip)
		}
	}
	return nil
}

// resolveHostIPs 解析主机地址：IP 字面量直接返回，域名走解析器（返回全部结果）
func resolveHostIPs(ctx context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("地址解析失败: %w", err)
	}
	if len(ips) == 0 {
		return nil, errors.New("地址无解析结果")
	}
	return ips, nil
}

// isAlwaysDeniedIP 恒拒网段：链路本地（含 169.254.0.0/16 云元数据）、未指定、组播
func isAlwaysDeniedIP(ip net.IP) bool {
	if ip4 := ip.To4(); ip4 != nil {
		return ip4.IsLinkLocalUnicast() || ip4.IsUnspecified() || ip4.IsMulticast()
	}
	return ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast()
}

// isPrivateIP 回环与私网（RFC1918 / IPv6 ULA）：由 ai_assistant_allow_private_url 控制
func isPrivateIP(ip net.IP) bool {
	if ip4 := ip.To4(); ip4 != nil {
		return ip4.IsLoopback() || ip4.IsPrivate()
	}
	return ip.IsLoopback() || ip.IsPrivate()
}

// truncateChars 按 rune 截断（超长截断并补省略号）
func truncateChars(s string, n int) string {
	if n <= 0 {
		return s
	}
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n]) + "…"
}

// sanitizeUpstreamText 屏蔽密钥（错误体/消息可能回显 Authorization）
func sanitizeUpstreamText(text, secret string) string {
	text = strings.TrimSpace(text)
	if secret == "" {
		return text
	}
	return strings.ReplaceAll(text, secret, "***")
}

// sanitizeErr 错误消息脱敏（同 sanitizeUpstreamText，保留错误链）
func sanitizeErr(err error, secret string) error {
	if err == nil || secret == "" {
		return err
	}
	msg := strings.ReplaceAll(err.Error(), secret, "***")
	return errors.New(msg)
}

// parseConfigBool 解析 configs 表布尔串（1/true/on/yes 为真）
func parseConfigBool(raw string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return fallback
	case "1", "true", "on", "yes":
		return true
	case "0", "false", "off", "no":
		return false
	default:
		return fallback
	}
}
