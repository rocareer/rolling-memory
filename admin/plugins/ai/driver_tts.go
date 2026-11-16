// plugin: ai v0.2.0
// driver_tts.go 是 dashscope_tts 驱动：阿里云百炼语音合成（TTS）。
//
// 与源实现 src/app/ai/driver/DashScopeTtsDriver.php 逐条对齐（源仓为 PHP 侧，只读对照）：
//   - base_url 是**完整合成端点**（不是 API 根），空值回落百炼 SpeechSynthesizer 默认端点；
//   - 请求体 {model, input:{text, voice, format, sample_rate}}，Bearer 鉴权（密钥只进请求头）；
//   - 非 2xx / 非 JSON / 取不到音频 三条失败语义与源同文案形态（'[dashscope_tts] HTTP %d: …' 等）；
//   - 只支持语音合成：chat / chatStream / embedding 一律**响亮失败**（与源 DriverException 同文案），
//     capabilities 只声明 speech。
//
// 与源的差异（如实登记，汇总于 README 差异节）：
//   - 源把 output.audio.url（OSS 下载地址，24h 有效）原样交回调用方；本驱动统一**取回字节**出口
//     （公开面与试听页只有一种响应形态：音频字节 + Content-Type），并额外支持「上游直出音频字节」
//     （Content-Type: audio/*，OpenAI 兼容 TTS 端点的常见形态，源未覆盖）。
//     地址来自上游响应，仍走同一套出站护栏（scheme 只许 http/https、目标 IP 由护栏判定、禁跟随重定向、
//     字节封顶 32MiB）。
//   - 源的 speakAsync（回调档）在本插件不需要：Go 侧驱动方法同步阻塞，等待语义由编排层的 context 收口。
//   - 源 speak() 不做文本长度分片（百炼 cosyvoice 单请求约 2000 字符上限）；本驱动保持同口径，
//     超长文本由上游按自己的契约拒绝（**未做本地分片**，如实登记）。
//
// 出站护栏与脱敏（既有出站契约，本包继承）：
//   - 出站客户端一律由 **newGuardedHTTPClient(allowPrivate, timeoutSeconds)** 构造（R1 收敛的唯一护栏出口：
//     不读 HTTP_PROXY、按实际 IP 拒绝回环/内网/链路本地/未指定、禁跟随重定向），按超时值分桶复用；
//   - 一切错误文案在返回前经 redactSecret(…, ch.APIKey) 脱敏（上游回显密钥也不外泄）。
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DashScopeTTSDriverName 是驱动标识（源 DriverManager::registeredClasses 的 'dashscope_tts'）。
const DashScopeTTSDriverName = "dashscope_tts"

// DefaultDashScopeTTSEndpoint 是默认端点（源 DashScopeTtsDriver::DEFAULT_ENDPOINT）。
//
// 注意：宿主渠道表单的 base_url 目前必填（handler_channel 的 ValidateBaseURL 拒空串），
// 因此这个默认值只在「DB 里已有 base_url 为空的行」时生效（例如厂商继承落地后由厂商侧给出）。
const DefaultDashScopeTTSEndpoint = "https://dashscope.aliyuncs.com/api/v1/services/audio/tts/SpeechSynthesizer"

// DashScopeTTSProbeText 是探活文本（源 ChannelProbe::speakCoroutine 的 '连通性测试'）。
const DashScopeTTSProbeText = "连通性测试"

// 音频体与 JSON 信封的上限：音频要在内存里持有到写出，必须封顶（32MiB 够常规时长）。
const (
	maxSpeechAudioBytes = 32 << 20
	maxSpeechJSONBytes  = 1 << 20
)

// SpeechRequest 是驱动层的语音合成请求。
//
// Model 是**逻辑模型名**（兜底用）；实际发给上游的是渠道行的 Model（ch.Model 非空时优先，与 chat 同口径）。
type SpeechRequest struct {
	Model      string
	Text       string
	Voice      string
	Format     string
	SampleRate int
}

// SpeechResult 是驱动层的合成结果：**已取回的音频字节** + 响应头用的 Content-Type。
//
// AudioURL 是上游给出的下载地址（诊断/审计用；本驱动已当面取回字节，调用方不必再取）。
// 音频只在内存与响应流里，服务端不落盘（试听页与公开面共用这一出口）。
type SpeechResult struct {
	Model       string
	Voice       string
	Format      string
	SampleRate  int
	ContentType string
	Audio       []byte
	AudioURL    string
	LatencyMS   int
}

// DashScopeTTSDriver 是语音合成驱动。
//
// clients 按超时值分桶复用护栏客户端（ResponseHeaderTimeout 是客户端级配置，不能逐请求改），
// 构造一律走 newGuardedHTTPClient（新驱动的唯一护栏出口，与 OpenAIDriver 同一条实现）。
type DashScopeTTSDriver struct {
	allowPrivate bool

	mu      sync.Mutex
	clients map[int]*http.Client
}

// NewDashScopeTTSDriver 构造语音驱动。allowPrivate=true 时放行本机/内网目标（本机 mock 与自建 TTS）。
func NewDashScopeTTSDriver(allowPrivate bool) *DashScopeTTSDriver {
	return &DashScopeTTSDriver{allowPrivate: allowPrivate, clients: make(map[int]*http.Client)}
}

// client 取护栏客户端（按生效超时分桶；构造走 newGuardedHTTPClient）。
func (d *DashScopeTTSDriver) client(timeoutSeconds int) *http.Client {
	seconds := EffectiveTimeout(timeoutSeconds)
	d.mu.Lock()
	defer d.mu.Unlock()
	if c, ok := d.clients[seconds]; ok {
		return c
	}
	c := newGuardedHTTPClient(d.allowPrivate, seconds)
	d.clients[seconds] = c
	return c
}

// speechDo 是本驱动的请求执行出口，语义与 driver.go 的 do 逐条相同（同一护栏客户端 + 同一失败判定）：
//
//	连接期失败（请求字节尚未写出）⇒ transport 类、可重试；
//	非 2xx ⇒ 按状态码分类的 UpstreamError（401/403=auth、429=rate_limit、其余=upstream）；
//	2xx ⇒ 原样交回响应（由调用方按形态解析）。
func (d *DashScopeTTSDriver) speechDo(httpReq *http.Request, timeoutSeconds int) (*http.Response, error) {
	var wrote atomic.Bool
	trace := &httptrace.ClientTrace{
		WroteRequest: func(httptrace.WroteRequestInfo) { wrote.Store(true) },
	}
	httpReq = httpReq.WithContext(httptrace.WithClientTrace(httpReq.Context(), trace))

	resp, err := d.client(timeoutSeconds).Do(httpReq)
	if err != nil {
		return nil, classifyTransportError(err, !wrote.Load())
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, upstreamErrorFromResponse(resp)
	}
	return resp, nil
}

// SupportsSpeech 声明语音合成能力（源 DriverInterface::supportsTts）。
func (d *DashScopeTTSDriver) SupportsSpeech() bool { return true }

// Capabilities 是给驱动注册表声明能力的只读值：本驱动**只**支持 speech
// （chat / embedding 一律响亮失败，见本文件末尾）。
func (d *DashScopeTTSDriver) Capabilities() []string { return []string{"speech"} }

// Speak 执行一次语音合成：POST {base_url}，取音频字节（直出或按返回地址取回）。
func (d *DashScopeTTSDriver) Speak(ctx context.Context, ch Channel, req SpeechRequest) (*SpeechResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		// 源文案：'语音合成文本为空'
		return nil, localError("empty_speech_text", "语音合成文本为空")
	}
	// 凭证缺失 fail-closed：**不发起任何出站调用**（与 chat/embedding 的 missing_api_key 同口径）。
	if strings.TrimSpace(ch.APIKey) == "" {
		return nil, localError("missing_api_key", "渠道未配置 api_key")
	}
	endpoint := strings.TrimSpace(ch.BaseURL)
	if endpoint == "" {
		endpoint = DefaultDashScopeTTSEndpoint
	}
	if err := ValidateBaseURL(endpoint); err != nil {
		return nil, localError("invalid_base_url", err.Error())
	}
	format, err := NormalizeVoiceFormat(req.Format)
	if err != nil {
		return nil, err
	}
	sampleRate := NormalizeVoiceSampleRate(req.SampleRate)

	model := strings.TrimSpace(ch.Model)
	if model == "" {
		model = strings.TrimSpace(req.Model)
	}

	// 请求体与源逐字同形：voice/format/sample_rate 收敛进 input；voice 为空时不发该键。
	input := map[string]any{
		"text":        text,
		"format":      format,
		"sample_rate": sampleRate,
	}
	if voice := strings.TrimSpace(req.Voice); voice != "" {
		input["voice"] = voice
	}
	body := map[string]any{"model": model, "input": input}

	callCtx, cancel := context.WithTimeout(ctx, time.Duration(EffectiveTimeout(ch.Timeout))*time.Second)
	defer cancel()

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, localError("encode_request", "编码合成请求失败: "+err.Error())
	}
	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, localError("invalid_request", err.Error())
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	// 密钥只进请求头：任何错误文案都不拼接 Header，出站错误另经 speechDriverError 脱敏。
	httpReq.Header.Set("Authorization", "Bearer "+ch.APIKey)

	start := time.Now()
	// speechDo() 同时给出护栏客户端与「非 2xx ⇒ UpstreamError（按状态码分类）」的映射。
	resp, err := d.speechDo(httpReq, ch.Timeout)
	if err != nil {
		return nil, speechDriverError(ch, err)
	}
	defer resp.Body.Close()

	result := &SpeechResult{
		Model:      model,
		Voice:      strings.TrimSpace(req.Voice),
		Format:     format,
		SampleRate: sampleRate,
		LatencyMS:  int(time.Since(start).Milliseconds()),
	}
	if isAudioContentType(resp.Header.Get("Content-Type")) {
		audio, truncated, readErr := readCappedBody(resp.Body, maxSpeechAudioBytes)
		if readErr != nil {
			// 断流：绝不把半截音频当成功
			return nil, speechDriverError(ch, classifyTransportError(readErr, false))
		}
		if truncated {
			return nil, speechInvalidError(ch, fmt.Sprintf("上游音频体超过上限（%d 字节）", maxSpeechAudioBytes))
		}
		if len(audio) == 0 {
			return nil, speechInvalidError(ch, "上游返回空音频体")
		}
		result.Audio = audio
		result.ContentType = strings.TrimSpace(resp.Header.Get("Content-Type"))
		return result, nil
	}

	audio, audioURL, contentType, envelopeErr := d.readSpeechEnvelope(callCtx, ch, resp.Body, format)
	if envelopeErr != nil {
		return nil, envelopeErr
	}
	result.Audio = audio
	result.AudioURL = audioURL
	result.ContentType = contentType
	result.LatencyMS = int(time.Since(start).Milliseconds())
	return result, nil
}

// readSpeechEnvelope 解析「JSON 信封」形态（源契约）：output.audio.url ⇒ 取回字节。
func (d *DashScopeTTSDriver) readSpeechEnvelope(ctx context.Context, ch Channel, body io.Reader, format string) ([]byte, string, string, error) {
	raw, truncated, err := readCappedBody(body, maxSpeechJSONBytes)
	if err != nil {
		return nil, "", "", speechDriverError(ch, classifyTransportError(err, false))
	}
	if truncated {
		return nil, "", "", speechInvalidError(ch, fmt.Sprintf("上游响应体超过上限（%d 字节，非音频响应）", maxSpeechJSONBytes))
	}
	var out struct {
		Output struct {
			Audio struct {
				URL string `json:"url"`
			} `json:"audio"`
		} `json:"output"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		// 源文案：'TTS 响应不是合法 JSON'
		return nil, "", "", speechInvalidError(ch, "TTS 响应不是合法 JSON: "+err.Error())
	}
	audioURL := strings.TrimSpace(out.Output.Audio.URL)
	if audioURL == "" {
		// 源文案：'TTS 未返回音频下载地址: <原始响应前 200 字符>'
		return nil, "", "", speechInvalidError(ch, "TTS 未返回音频下载地址: "+truncateRunes(strings.TrimSpace(string(raw)), 200))
	}
	audio, contentType, fetchErr := d.fetchSpeechAudio(ctx, ch, audioURL, format)
	if fetchErr != nil {
		return nil, "", "", fetchErr
	}
	return audio, audioURL, contentType, nil
}

// fetchSpeechAudio 把上游给出的音频地址当面取回字节。
//
// 安全口径：地址来自**上游响应**（不是调用方），仍走同一套出站护栏——scheme 只许 http/https、
// 目标 IP 由护栏判定（回环/内网默认拒，allow_private_hosts=1 才放行）、禁跟随重定向、字节封顶。
func (d *DashScopeTTSDriver) fetchSpeechAudio(ctx context.Context, ch Channel, audioURL, format string) ([]byte, string, error) {
	if err := ValidateBaseURL(audioURL); err != nil {
		return nil, "", speechInvalidError(ch, "上游音频地址不可用: "+err.Error())
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, audioURL, nil)
	if err != nil {
		return nil, "", speechInvalidError(ch, err.Error())
	}
	httpReq.Header.Set("Accept", "audio/*, */*")
	resp, err := d.speechDo(httpReq, ch.Timeout)
	if err != nil {
		return nil, "", speechDriverError(ch, err)
	}
	defer resp.Body.Close()

	audio, truncated, readErr := readCappedBody(resp.Body, maxSpeechAudioBytes)
	if readErr != nil {
		return nil, "", speechDriverError(ch, classifyTransportError(readErr, false))
	}
	if truncated {
		return nil, "", speechInvalidError(ch, fmt.Sprintf("音频体超过上限（%d 字节）", maxSpeechAudioBytes))
	}
	if len(audio) == 0 {
		return nil, "", speechInvalidError(ch, "上游音频地址返回空体")
	}
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if !isAudioContentType(contentType) {
		contentType = speechContentType(format)
	}
	return audio, contentType, nil
}

// Models 是探活入口（宿主渠道页的「探活」与编排层复用同一个方法）。
//
// 语音端点**没有 /models 路由**，源 ChannelProbe::speakCoroutine 因此改发一次 1 条真实合成
// （文本 '连通性测试'、音色 longxiaochun_v2、mp3/24000），2xx 即成功——本方法同口径
// （代价：探活会产生一次极小的真实合成费用，源侧亦然）。
//
// 返回值恒为空列表：源的探活 models_count 取自响应 data[]（语音响应没有该字段 ⇒ 0），保持一致。
func (d *DashScopeTTSDriver) Models(ctx context.Context, ch Channel) ([]string, error) {
	probe := SpeechRequest{
		Model:      ch.Model,
		Text:       DashScopeTTSProbeText,
		Voice:      voiceProbeVoice,
		Format:     VoiceFormatDefault,
		SampleRate: VoiceSampleRateDefault,
	}
	if _, err := d.Speak(ctx, ch, probe); err != nil {
		return nil, err
	}
	return []string{}, nil
}

// ===== 能力门（只支持 speech）=====

// Chat 语音驱动没有 chat 端点（与源 DriverException 同文案）。
func (d *DashScopeTTSDriver) Chat(context.Context, Channel, ChatRequest) (*ChatResult, error) {
	return nil, unsupportedSpeechCapability("chat")
}

// ChatStream 同 Chat（源 chatAsync 亦为响亮失败，不做静默降级）。
func (d *DashScopeTTSDriver) ChatStream(context.Context, Channel, ChatRequest, func([]byte) error) (*ChatResult, error) {
	return nil, unsupportedSpeechCapability("chat")
}

// Embeddings 语音驱动没有 embedding 端点（与源 DriverException 同文案）。
func (d *DashScopeTTSDriver) Embeddings(context.Context, Channel, EmbeddingRequest) (*EmbeddingResult, error) {
	return nil, unsupportedSpeechCapability("embedding")
}

// unsupportedSpeechCapability 是能力门的统一出口（local 类：调用方配错了渠道，不是上游的错）。
func unsupportedSpeechCapability(capability string) *UpstreamError {
	return localError("unsupported_capability",
		fmt.Sprintf("[%s] 语音驱动不支持 %s", DashScopeTTSDriverName, capability))
}

// ===== 小工具（纯函数）=====

// isAudioContentType 判定响应是否是「直出音频」形态（audio/*）。
func isAudioContentType(contentType string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "audio/")
}

// readCappedBody 读满上限之内的字节，返回 (数据, 是否超限, 读取错误)。
//
// 读取错误（断流）必须上抛：绝不把半截内容当成功（音频尤其如此）。
func readCappedBody(reader io.Reader, max int64) ([]byte, bool, error) {
	data, err := io.ReadAll(io.LimitReader(reader, max+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > max {
		return nil, true, nil
	}
	return data, false, nil
}

// speechDriverError 给上游/传输错误补驱动名（源 '[%s] HTTP %d: %s' 的形态）并统一脱敏。
//
// 同时适用于 *UpstreamError（改就地改它自己的 Message，它就是本次调用新建的对象）与普通 error。
func speechDriverError(ch Channel, err error) error {
	if err == nil {
		return nil
	}
	var upstreamErr *UpstreamError
	if errors.As(err, &upstreamErr) && upstreamErr != nil {
		message := truncateMessage(upstreamErr.Message, 500)
		switch {
		case upstreamErr.HTTPStatus > 0:
			upstreamErr.Message = fmt.Sprintf("[%s] HTTP %d: %s", DashScopeTTSDriverName, upstreamErr.HTTPStatus, message)
		case message != "":
			upstreamErr.Message = fmt.Sprintf("[%s] %s", DashScopeTTSDriverName, message)
		}
		upstreamErr.Message = redactSecret(upstreamErr.Message, ch.APIKey)
		return upstreamErr
	}
	return errors.New(redactSecret(truncateMessage(err.Error(), 500), ch.APIKey))
}

// speechInvalidError 是「拿到响应但内容不可用」的统一出口（不可重试，与 driver.go 的 upstreamInvalid 同口径）。
func speechInvalidError(ch Channel, message string) *UpstreamError {
	inner := redactSecret(truncateMessage(message, 500), ch.APIKey)
	return upstreamInvalid(fmt.Sprintf("[%s] %s", DashScopeTTSDriverName, inner))
}
