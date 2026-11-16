// plugin: ai v0.2.0
// service_tts.go 是语音合成（TTS）在 *Service 上的编排方法——**新增文件，不改 service.go 的任何签名**。
//
// 编排链与 Chat 逐段同构（begin 能力门 → candidates → Admit 准入 → 驱动调用 → 结算 → 降级），
// 差别只在三处：① 调用类型 call_type='tts'；② 驱动必须实现本文件声明的 SpeechDriver（否则响亮失败、
// 不静默降级）；③ 计量口径按**字符数**（见下）。
//
// 计量口径（**冻结**）：输入文本的字符数（[]rune 长度，与源 mb_strlen 同口径）
//
//	usage.prompt_tokens     = 字符数
//	usage.completion_tokens = 0
//	usage.total_tokens      = 字符数
//	usage.cost              = ComputeCost(字符数, 0, price_input, price_output)
//	                          = round(字符数/1e6×price_input, 6)——与 chat 同一公式，
//	                          「每百万」的分母在 TTS 上解释为**每百万字符**
//	usage.tokens_source     = 'estimate'（字符数是本地计数，不是上游 usage 回传）
//	usage.call_type         = 'tts'
//
// 与源的差异（如实登记，汇总于 README 差异节）：源 parseSpeechUsage（AiRouterService.php:1587-1598）
// 优先取上游 usage（prompt_tokens ?? total_tokens），仅当它为 0 才回落到 mb_strlen(text) 并标 estimate；
// 本插件按包验收口径**冻结为恒字符数**，使「cost 与 字符数/1e6×price_input 逐位一致」不受上游是否回传
// usage 影响（上游字段缺失/口径漂移都不改变本地账）。代价：上游若回传了更精确的用量，本插件不复用。
package ai

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

// SpeechDriver 是「能合成语音的驱动」窄接口，声明在**消费侧**（Go 惯例，先例 TokenCharger）。
//
// 为什么不是 Driver 的第 5 个方法：Driver 是编排层对 chat/embedding 的既有契约，加方法会让所有
// 现役实现（OpenAIDriver、假驱动）与测试一起改；语音是可选能力，用可选接口表达更贴切——
// 也与源 DriverInterface::supportsTts 的「能力声明 + 响亮的门」一致。
//
// 装配面：驱动注册表需实现本接口并按 ch.Driver 分派到 dashscope_tts；
// 若注册表只实现 Driver 的 4 个方法，语音路由会以「渠道驱动不支持语音合成」失败（响亮失败，不静默）。
type SpeechDriver interface {
	Speak(ctx context.Context, ch Channel, req SpeechRequest) (*SpeechResult, error)
}

// SpeechInput 是一次语音合成调用的入参。
//
// BizType / Source 只判空（与 ChatInput 同口径，绝不做闭集校验）。Voice 是上游音色 ID，必填：
// 百炼 cosyvoice 契约里 voice 必填（源 ChannelProbe.php:260 同注）。
type SpeechInput struct {
	Model      string
	Text       string
	Voice      string
	Format     string
	SampleRate int

	BizType string
	Source  string
	// RequestID 仅供测试/重放注入；为空时生成 ai_+24 hex。
	RequestID string
}

// Speech 执行语音合成：按候选链顺序降级，首个成功即返回（熔断/准入/计费与 chat 同一套）。
func (s *Service) Speech(ctx context.Context, in SpeechInput) (*SpeechResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return nil, &APIError{
			HTTPStatus: 400,
			Type:       "invalid_request_error",
			Code:       "empty_speech_text",
			Message:    "语音合成文本不能为空",
			Class:      ErrorClassLocal,
		}
	}
	voice := strings.TrimSpace(in.Voice)
	if voice == "" {
		return nil, &APIError{
			HTTPStatus: 400,
			Type:       "invalid_request_error",
			Code:       "missing_voice",
			Message:    "语音合成必须指定 voice（音色）",
			Class:      ErrorClassLocal,
		}
	}
	format, err := NormalizeVoiceFormat(in.Format)
	if err != nil {
		return nil, err
	}
	// 能力门：模型类型必须恰为 'tts'（type=chat 的模型不许走语音，反之亦然）。
	m, requestID, err := s.begin(ctx, in.Model, voiceCallType, in.BizType, in.Source, in.RequestID)
	if err != nil {
		return nil, err
	}
	candidates, err := s.candidates(ctx, m)
	if err != nil {
		return nil, err
	}

	req := SpeechRequest{
		Model:      m.Name,
		Text:       text,
		Voice:      voice,
		Format:     format,
		SampleRate: NormalizeVoiceSampleRate(in.SampleRate),
	}

	attempt := 0
	var lastErr error
	for _, cand := range candidates {
		if err := ctx.Err(); err != nil {
			lastErr = err
			break
		}
		ch := s.resolveChannel(cand.ChannelSnapshot())
		ad, err := s.router.Admit(ctx, ch, s.nowUnix())
		if err != nil {
			// 熔断存储故障：拒绝该候选，但不算一次真实调用（不自增 attempt）。
			lastErr = err
			continue
		}
		if !ad.Allowed {
			// 熔断/冷却拒绝：跳过且不自增 attempt。
			continue
		}
		ch.Circuit = ad.Circuit

		speaker, ok := s.driver.(SpeechDriver)
		if !ok || !speakerSupportsSpeech(speaker) {
			// 驱动不支持语音合成：文案照源 AiRouterService::speakAsync 的能力门，
			// 且**不漂移**到别的候选之前先记一行失败 usage（与 chat 的失败结算同口径）。
			callErr := speechUnsupportedError(ch)
			latency := 0
			s.settleSpeechFailure(ctx, m, ch, callErr, latency, requestID, attempt, in.BizType, in.Source)
			lastErr = callErr
			attempt++
			continue
		}

		start := time.Now()
		res, callErr := retryCall(ctx, "tts", func() (*SpeechResult, error) {
			return speaker.Speak(ctx, ch, req)
		})
		latency := int(time.Since(start).Milliseconds())

		if callErr == nil {
			s.settleSpeechSuccess(ctx, m, ch, res, text, latency, requestID, attempt, in.BizType, in.Source)
			return res, nil
		}
		s.settleSpeechFailure(ctx, m, ch, callErr, latency, requestID, attempt, in.BizType, in.Source)
		lastErr = callErr
		attempt++
	}
	if lastErr == nil {
		lastErr = errors.New("没有可用渠道")
	}
	return nil, allUnavailableError(lastErr)
}

// speakerSupportsSpeech 是可选的第二道能力门：驱动实现了 SupportsSpeech() 且为 false 时也拒
// （dashscope_tts 恒 true；注册表按渠道能力分派时可以声明 false）。
func speakerSupportsSpeech(driver SpeechDriver) bool {
	declarer, ok := driver.(interface{ SupportsSpeech() bool })
	if !ok {
		return true
	}
	return declarer.SupportsSpeech()
}

// speechUnsupportedError 是「该渠道的驱动不支持语音合成」的响亮失败（文案照源 speakAsync 的能力门）。
func speechUnsupportedError(ch Channel) *UpstreamError {
	return localError("driver_not_support_speech",
		fmt.Sprintf("[%s] 渠道驱动不支持语音合成（TTS），请为 TTS 模型绑定语音驱动渠道", ch.Name))
}

// settleSpeechSuccess 写一行成功的 TTS usage + 渠道累计 + 熔断成功（与 chat 的成功结算同口径）。
func (s *Service) settleSpeechSuccess(ctx context.Context, m Model, ch Channel, res *SpeechResult, text string, latency int, requestID string, attempt int, bizType, source string) {
	if res == nil {
		return
	}
	chars := speechCharCount(text)
	now := s.nowUnix()
	usage := Usage{
		RequestID:    requestID,
		Attempt:      attempt,
		ModelID:      m.ID,
		ModelName:    m.Name,
		ChannelID:    ch.ID,
		ChannelName:  ch.Name,
		CallType:     voiceCallType,
		BizType:      bizType,
		Source:       source,
		Stream:       0,
		PromptTokens: chars,
		// completion_tokens 恒 0：TTS 没有「输出 token」，成本只按输入字符计。
		TotalTokens:  chars,
		TokensSource: TokensSourceEstimate,
		Cost:         s.costOrZero(m, chars, 0),
		Currency:     s.cfg.Currency,
		LatencyMS:    latency,
		Status:       UsageStatusSuccess,
		CreateTime:   now,
		UpdateTime:   now,
	}
	acct := accountingContext(ctx)
	defer acct.cancel()
	s.saveUsage(acct.ctx, usage)
	if err := s.breaker.RecordTotals(acct.ctx, uint64(ch.ID), true, chars, now); err != nil {
		log.Printf("ai: 累加语音渠道成功计数失败 channel=%d: %v", ch.ID, err)
	}
	if err := s.breaker.OnSuccess(acct.ctx, ch, now); err != nil {
		log.Printf("ai: 语音成功迁移熔断状态失败 channel=%d: %v", ch.ID, err)
	}
}

// settleSpeechFailure 写一行失败的 TTS usage（cost=0）+ 渠道失败累计 + 熔断失败（与 chat 同口径）。
func (s *Service) settleSpeechFailure(ctx context.Context, m Model, ch Channel, callErr error, latency int, requestID string, attempt int, bizType, source string) {
	info := ClassifyError(callErr)
	now := s.nowUnix()
	usage := Usage{
		RequestID:    requestID,
		Attempt:      attempt,
		ModelID:      m.ID,
		ModelName:    m.Name,
		ChannelID:    ch.ID,
		ChannelName:  ch.Name,
		CallType:     voiceCallType,
		BizType:      bizType,
		Source:       source,
		Stream:       0,
		TokensSource: TokensSourceNone,
		Cost:         "0.000000",
		Currency:     s.cfg.Currency,
		LatencyMS:    latency,
		Status:       info.Status,
		ErrorCode:    info.Code,
		ErrorMessage: truncateMessage(info.Message, 500),
		ErrorClass:   info.Class,
		CreateTime:   now,
		UpdateTime:   now,
	}
	acct := accountingContext(ctx)
	defer acct.cancel()
	s.saveUsage(acct.ctx, usage)
	if err := s.breaker.RecordTotals(acct.ctx, uint64(ch.ID), false, 0, now); err != nil {
		log.Printf("ai: 累加语音渠道失败计数失败 channel=%d: %v", ch.ID, err)
	}
	if err := s.breaker.OnFailure(acct.ctx, ch, info.Message, now); err != nil {
		log.Printf("ai: 语音失败迁移熔断状态失败 channel=%d: %v", ch.ID, err)
	}
}
