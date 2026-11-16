// plugin: ai v0.2.0
// voice_test.go 覆盖语音（TTS）在**无数据库**环境下的可判定行为：
//
//   - 音色入参收敛（必填/长度/格式闭集/状态闭集/补丁语义）；
//   - wheres 白名单（音色列表的高级检索契约）；
//   - dashscope_tts 驱动协议：伪上游（httptest）的音频字节直出与 JSON 信封两态、
//     非 2xx 分类、非法 JSON、缺音频地址、**断流**（半截音频绝不当成功）；
//   - 凭证缺失 fail-closed（本地错误且**不发起任何出站调用**）；
//   - 出站护栏复用（allow_private_hosts=0 拒回环、=1 放行）与脱敏出口（上游回显密钥不留明文）；
//   - 计量口径（call_type='tts'、prompt_tokens=字符数、cost 与 字符数/1e6×price_input 逐位一致）；
//   - 能力门：模型 type=chat 不允许语音调用、驱动缺 Speak 响亮失败；
//   - 公开面 POST /ai/v1/audio/speech 的音频字节回执与 OpenAI 形状错误。
//
// 需要真库的部分在 voice_db_test.go（DSN 门控）。
package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
)

// ===== 伪上游（src：源 DashScopeTtsDriver 的端点契约）=====

// ttsMockAudio 是伪上游返回的「音频字节」（内容不重要，逐字节比对用）。
var ttsMockAudio = []byte("ID3\x03\x00\x00\x00\x00\x00\x00FAKE-MP3-BYTES")

// ttsMockMode 是伪上游的响应形态。
type ttsMockMode string

const (
	// ttsMockAudioBytes 直出音频字节（Content-Type: audio/*）。
	ttsMockAudioBytes ttsMockMode = "audio"
	// ttsMockJSONURL 走源的 JSON 信封：output.audio.url 指向本服务的下载端点。
	ttsMockJSONURL ttsMockMode = "json"
	// ttsMockStatus500 上游 5xx（正文带 error.message）。
	ttsMockStatus500 ttsMockMode = "status"
	// ttsMockNotJSON 200 但正文不是 JSON（源文案：'TTS 响应不是合法 JSON'）。
	ttsMockNotJSON ttsMockMode = "notjson"
	// ttsMockNoURL 200 是 JSON，但没有 output.audio.url（源文案：'TTS 未返回音频下载地址'）。
	ttsMockNoURL ttsMockMode = "nourl"
	// ttsMockTruncate 声明长度后**提前断开连接**（半截音频 ⇒ 必须报错，不得当成功）。
	ttsMockTruncate ttsMockMode = "truncate"
	// ttsMockEchoKey 401 并把渠道 api_key 回显在正文里（验脱敏出口）。
	ttsMockEchoKey ttsMockMode = "echo_key"
)

// ttsMockUpstream 是伪造的百炼 SpeechSynthesizer 上游：记账每次命中，便于断言
// 「恰好命中上游一次」「凭证缺失时零调用」。
type ttsMockUpstream struct {
	server          *httptest.Server
	mode            ttsMockMode
	hitCounter      int32
	downloadCounter int32
	lastBody        atomic.Value // []byte
	lastAuth        atomic.Value // string
}

// newTTSMockUpstream 起一台伪上游（按 TearDown 自动关闭）。
func newTTSMockUpstream(t *testing.T, mode ttsMockMode) *ttsMockUpstream {
	t.Helper()
	mock := &ttsMockUpstream{mode: mode}
	mock.server = httptest.NewServer(http.HandlerFunc(mock.handle))
	t.Cleanup(mock.server.Close)
	return mock
}

// tts 合成端点的完整 URL（渠道 base_url 在本驱动里就是这一个完整端点）。
func (m *ttsMockUpstream) speechURL() string {
	return m.server.URL + "/api/v1/services/audio/tts/SpeechSynthesizer"
}

func (m *ttsMockUpstream) audioURL() string { return m.server.URL + "/audio.bin" }

func (m *ttsMockUpstream) hits() int { return int(atomic.LoadInt32(&m.hitCounter)) }

func (m *ttsMockUpstream) downloads() int { return int(atomic.LoadInt32(&m.downloadCounter)) }

// requestBody 返回最近一次合成请求的原始体（断言请求体形状用）。
func (m *ttsMockUpstream) requestBody(t *testing.T) map[string]any {
	t.Helper()
	raw, _ := m.lastBody.Load().([]byte)
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("伪上游没记到合法请求体: %v（原文 %s）", err, string(raw))
	}
	return body
}

func (m *ttsMockUpstream) authHeader() string {
	value, _ := m.lastAuth.Load().(string)
	return value
}

func (m *ttsMockUpstream) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/audio.bin" {
		atomic.AddInt32(&m.downloadCounter, 1)
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write(ttsMockAudio)
		return
	}
	atomic.AddInt32(&m.hitCounter, 1)
	raw, _ := io.ReadAll(r.Body)
	m.lastBody.Store(raw)
	m.lastAuth.Store(r.Header.Get("Authorization"))

	switch m.mode {
	case ttsMockAudioBytes:
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write(ttsMockAudio)
	case ttsMockJSONURL:
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"output":{"audio":{"url":%q}},"usage":{"characters":3}}`, m.audioURL())
	case ttsMockStatus500:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream exploded"}}`))
	case ttsMockNotJSON:
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("not-json-at-all"))
	case ttsMockNoURL:
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output":{"audio":{}},"usage":{}}`))
	case ttsMockTruncate:
		// 声明长度后提前断开：客户端读正文时报 unexpected EOF ⇒ 必须上抛（半截音频严禁当成功）。
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Header().Set("Content-Length", "1048576")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(ttsMockAudio[:8])
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		if hijacker, ok := w.(http.Hijacker); ok {
			if conn, _, err := hijacker.Hijack(); err == nil {
				_ = conn.Close()
			}
		}
	case ttsMockEchoKey:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprintf(w, `{"error":{"message":"bad key %s"}}`, m.lastKeyFromRequest(r))
	}
}

// lastKeyFromRequest 从 Authorization 头里取出调用方出示的凭证（伪上游「回显密钥」用）。
func (m *ttsMockUpstream) lastKeyFromRequest(r *http.Request) string {
	if key, ok := bearerToken(r.Header.Get("Authorization")); ok {
		return key
	}
	return ""
}

// ttsChannel 造一条指向伪上游的渠道快照。
func ttsChannel(baseURL, apiKey string) Channel {
	return Channel{
		ID: 41, Name: "tts-主渠道", Vendor: "qwen", Driver: DashScopeTTSDriverName,
		BaseURL: baseURL, APIKey: apiKey, Model: "cosyvoice-v2", Timeout: 10,
		Status: StatusEnabled, Circuit: CircuitClosed,
	}
}

// ttsCandidate 造一条候选行（Service 的候选链用它；ChannelSnapshot 映射出渠道快照）。
func ttsCandidate(baseURL, apiKey string) Candidate {
	return Candidate{
		ChannelID: 41, Name: "tts-主渠道", Vendor: "qwen", Driver: DashScopeTTSDriverName,
		BaseURL: baseURL, APIKey: apiKey, Model: "cosyvoice-v2", Timeout: 10,
		ChannelStatus: StatusEnabled, Circuit: CircuitClosed,
		ModelID: 7, BindingPriority: 1, BindingWeight: 1, BindingStatus: StatusEnabled,
	}
}

// ttsRepo 造 TTS 场景的假仓储：逻辑模型 type=tts + 一条候选。
//
// 复用 handler_test.go 的 fakeRepository（同包，避免第二套假仓储漂移）。
func ttsRepo(baseURL, apiKey string) *fakeRepository {
	return &fakeRepository{
		model: Model{
			ID: 7, Name: "tts-test", Type: ModelTypeTTS, Status: StatusEnabled,
			Strategy: StrategyPriority, PriceInput: "3.000000", PriceOutput: "0.000000",
		},
		candidates: []Candidate{ttsCandidate(baseURL, apiKey)},
	}
}

// ttsSpeechDriver 是 Driver + SpeechDriver 的假实现（无网）：只做语音，其它能力沿用 fakeDriver 的失败/空值。
type ttsSpeechDriver struct {
	fakeDriver
	speech    *SpeechResult
	speechErr error
	calls     int
	lastReq   SpeechRequest
	lastCh    Channel
}

// Speak 记录调用并回注入结果。
func (d *ttsSpeechDriver) Speak(_ context.Context, ch Channel, req SpeechRequest) (*SpeechResult, error) {
	d.calls++
	d.lastCh = ch
	d.lastReq = req
	if d.speechErr != nil {
		return nil, d.speechErr
	}
	if d.speech == nil {
		return nil, errors.New("假驱动没有配置语音结果")
	}
	return d.speech, nil
}

// ===== 音色入参收敛（纯函数）=====

func TestVoiceSaveRequestValidation(t *testing.T) {
	name := "龙小淳"
	model := "tts-test"
	voiceID := "longxiaochun_v2"
	remark := strings.Repeat("字", 300)

	row, err := voiceSaveRequest{Name: &name, Model: &model, Voice: &voiceID, Remark: &remark}.voice()
	if err != nil {
		t.Fatalf("合法音色被拒: %v", err)
	}
	if row.Format != VoiceFormatDefault {
		t.Fatalf("format 缺省应为 %s，实际 %q", VoiceFormatDefault, row.Format)
	}
	if row.Status != StatusEnabled {
		t.Fatalf("status 缺省应为启用，实际 %d", row.Status)
	}
	if len([]rune(row.Remark)) != 255 {
		t.Fatalf("remark 应截断到 255 字符，实际 %d", len([]rune(row.Remark)))
	}

	// 必填三项逐个缺失。
	for _, tc := range []struct {
		label string
		req   voiceSaveRequest
	}{
		{"缺音色名", voiceSaveRequest{Model: &model, Voice: &voiceID}},
		{"缺逻辑模型名", voiceSaveRequest{Name: &name, Voice: &voiceID}},
		{"缺上游音色 ID", voiceSaveRequest{Name: &name, Model: &model}},
	} {
		if _, err := tc.req.voice(); err == nil {
			t.Fatalf("%s 必须被拒", tc.label)
		}
	}

	// 长度上限（varchar 列宽口径：按字符数）。
	tooLongName := strings.Repeat("我", 51)
	if _, err := (voiceSaveRequest{Name: &tooLongName, Model: &model, Voice: &voiceID}).voice(); err == nil {
		t.Fatal("51 字符音色名必须被拒（name 是 varchar(50)）")
	}
	tooLongVoice := strings.Repeat("v", 61)
	if _, err := (voiceSaveRequest{Name: &name, Model: &model, Voice: &tooLongVoice}).voice(); err == nil {
		t.Fatal("61 字符上游音色 ID 必须被拒（voice 是 varchar(60)）")
	}

	// 格式闭集：命中通过、闭集外响亮拒绝（不静默回落）。
	for _, format := range voiceFormats {
		value := format
		got, err := voiceSaveRequest{Name: &name, Model: &model, Voice: &voiceID, Format: &value}.voice()
		if err != nil || got.Format != format {
			t.Fatalf("format %q 应通过，实际 %v / %v", format, got, err)
		}
	}
	badFormat := "aac"
	if _, err := (voiceSaveRequest{Name: &name, Model: &model, Voice: &voiceID, Format: &badFormat}).voice(); err == nil {
		t.Fatal("format=aac 必须被拒（闭集 mp3|wav|pcm|opus）")
	}

	// 状态闭集。
	badStatus := int16(2)
	if _, err := (voiceSaveRequest{Name: &name, Model: &model, Voice: &voiceID, Status: &badStatus}).voice(); err == nil {
		t.Fatal("status=2 必须被拒")
	}
	disabled := StatusDisabled
	if row, err := (voiceSaveRequest{Name: &name, Model: &model, Voice: &voiceID, Status: &disabled}).voice(); err != nil || row.Status != StatusDisabled {
		t.Fatalf("status=0 应通过，实际 %v / %v", row, err)
	}
}

func TestVoicePatchOnlyTouchesProvidedFields(t *testing.T) {
	patch, err := voiceSaveRequest{}.patch()
	if err != nil {
		t.Fatalf("空补丁不应报错: %v", err)
	}
	if patch.Name != nil || patch.Model != nil || patch.Voice != nil || patch.Format != nil || patch.Status != nil || patch.Remark != nil {
		t.Fatalf("空请求不应产生任何列改动: %+v", patch)
	}

	voiceID := "longxiaochun_v2"
	patch, err = voiceSaveRequest{Voice: &voiceID}.patch()
	if err != nil {
		t.Fatalf("单列补丁不应报错: %v", err)
	}
	if patch.Voice == nil || *patch.Voice != voiceID {
		t.Fatalf("voice 列未被带上: %+v", patch)
	}
	if patch.Name != nil || patch.Model != nil || patch.Format != nil {
		t.Fatalf("未提供的列被误带: %+v", patch)
	}

	// 补丁上的非法格式与非法状态同样要拒（与 create 同口径）。
	badFormat := "aac"
	if _, err := (voiceSaveRequest{Format: &badFormat}).patch(); err == nil {
		t.Fatal("补丁里的非法 format 必须被拒")
	}
	badStatus := int16(9)
	if _, err := (voiceSaveRequest{Status: &badStatus}).patch(); err == nil {
		t.Fatal("补丁里的非法 status 必须被拒")
	}
}

func TestVoicePreviewTextFallsBackToDefault(t *testing.T) {
	if got := voicePreviewText(nil); got != voicePreviewDefaultText {
		t.Fatalf("缺省文本应为 %q，实际 %q", voicePreviewDefaultText, got)
	}
	blank := "   "
	if got := voicePreviewText(&blank); got != voicePreviewDefaultText {
		t.Fatalf("纯空白文本应回落默认，实际 %q", got)
	}
	custom := " 你好 "
	if got := voicePreviewText(&custom); got != "你好" {
		t.Fatalf("显式文本应被 trim，实际 %q", got)
	}
}

func TestSpeechContentTypeMapping(t *testing.T) {
	cases := map[string]string{
		VoiceFormatMP3:  "audio/mpeg",
		VoiceFormatWAV:  "audio/wav",
		VoiceFormatPCM:  "audio/pcm",
		VoiceFormatOpus: "audio/ogg",
		"":              "application/octet-stream",
		"aac":           "application/octet-stream",
	}
	for format, want := range cases {
		if got := speechContentType(format); got != want {
			t.Fatalf("format %q 的 Content-Type = %q，期望 %q", format, got, want)
		}
	}
}

// ===== wheres 白名单（音色列表的高级检索契约）=====

func TestVoiceWhereFieldsWhitelist(t *testing.T) {
	groups, err := validateWhereGroups(voiceWhereFields, []whereGroup{
		{Wheres: []whereCondition{{Field: "name", Operator: "ILIKE", Value: "龙"}}},
		{Wheres: []whereCondition{{Field: "status", Operator: "eq", Value: 1}}, Or: true},
		{Wheres: []whereCondition{{Field: "create_time", Operator: "BETWEEN", Value: []any{1, 2}}}},
		{Wheres: []whereCondition{{Field: "remark", Operator: "NULL"}}},
	})
	if err != nil {
		t.Fatalf("白名单内的条件被拒: %v", err)
	}
	if len(groups) != 4 {
		t.Fatalf("应保留 4 组条件，实际 %d", len(groups))
	}

	// 白名单外字段。
	if _, err := validateWhereGroups(voiceWhereFields, []whereGroup{
		{Wheres: []whereCondition{{Field: "api_key", Operator: "eq", Value: "x"}}},
	}); err == nil || !strings.Contains(err.Error(), "白名单") {
		t.Fatalf("白名单外字段必须被拒且文案含「白名单」，实际 %v", err)
	}
	// 词表外操作符。
	if _, err := validateWhereGroups(voiceWhereFields, []whereGroup{
		{Wheres: []whereCondition{{Field: "name", Operator: "REGEXP", Value: "x"}}},
	}); err == nil {
		t.Fatal("词表外操作符必须被拒")
	}
	// 二值列上的模糊与有序比较。
	for _, operator := range []string{"LIKE", "gt"} {
		if _, err := validateWhereGroups(voiceWhereFields, []whereGroup{
			{Wheres: []whereCondition{{Field: "status", Operator: operator, Value: 1}}},
		}); err == nil {
			t.Fatalf("status（二值列）上的 %s 必须被拒", operator)
		}
	}
	// IN 元素上限。
	values := make([]any, maxWhereInValues+1)
	for i := range values {
		values[i] = i
	}
	if _, err := validateWhereGroups(voiceWhereFields, []whereGroup{
		{Wheres: []whereCondition{{Field: "id", Operator: "IN", Value: values}}},
	}); err == nil {
		t.Fatal("IN 元素超限必须被拒")
	}
	// 空组按宿主管家口径忽略。
	groups, err = validateWhereGroups(voiceWhereFields, []whereGroup{{}, {Wheres: []whereCondition{{Field: "id", Operator: "eq", Value: 1}}}})
	if err != nil || len(groups) != 1 {
		t.Fatalf("空组应被忽略、非空组保留，实际 %d 组 / err=%v", len(groups), err)
	}
}

// ===== 驱动协议 =====

func TestDashScopeTTSDriverAudioBytesPassthrough(t *testing.T) {
	mock := newTTSMockUpstream(t, ttsMockAudioBytes)
	driver := NewDashScopeTTSDriver(true)

	res, err := driver.Speak(context.Background(), ttsChannel(mock.speechURL(), "sk-upstream-key"),
		SpeechRequest{Text: "你好世界", Voice: "longxiaochun_v2", Format: VoiceFormatMP3, SampleRate: VoiceSampleRateDefault})
	if err != nil {
		t.Fatalf("合成失败: %v", err)
	}
	if string(res.Audio) != string(ttsMockAudio) {
		t.Fatalf("音频字节未逐位透传: %q", string(res.Audio))
	}
	if res.ContentType != "audio/mpeg" {
		t.Fatalf("Content-Type = %q，期望 audio/mpeg", res.ContentType)
	}
	if mock.hits() != 1 {
		t.Fatalf("上游命中次数 = %d，期望 1", mock.hits())
	}
	if mock.authHeader() != "Bearer sk-upstream-key" {
		t.Fatalf("Authorization = %q（Bearer 同源口径）", mock.authHeader())
	}
	body := mock.requestBody(t)
	if body["model"] != "cosyvoice-v2" {
		t.Fatalf("请求体 model 应取渠道行的 model，实际 %v", body["model"])
	}
	input, _ := body["input"].(map[string]any)
	if input == nil {
		t.Fatalf("请求体缺少 input: %v", body)
	}
	if input["text"] != "你好世界" || input["voice"] != "longxiaochun_v2" || input["format"] != "mp3" {
		t.Fatalf("input 形状不符源契约: %v", input)
	}
	if rate, ok := input["sample_rate"].(float64); !ok || int(rate) != VoiceSampleRateDefault {
		t.Fatalf("input.sample_rate = %v，期望 %d", input["sample_rate"], VoiceSampleRateDefault)
	}
}

func TestDashScopeTTSDriverJSONEnvelopeFetchesAudio(t *testing.T) {
	mock := newTTSMockUpstream(t, ttsMockJSONURL)
	driver := NewDashScopeTTSDriver(true)

	res, err := driver.Speak(context.Background(), ttsChannel(mock.speechURL(), "sk-upstream-key"),
		SpeechRequest{Text: "abc", Voice: "longxiaochun_v2"})
	if err != nil {
		t.Fatalf("合成失败: %v", err)
	}
	if string(res.Audio) != string(ttsMockAudio) {
		t.Fatalf("音频字节未取回: %q", string(res.Audio))
	}
	if res.AudioURL != mock.audioURL() {
		t.Fatalf("AudioURL = %q，期望 %q", res.AudioURL, mock.audioURL())
	}
	if mock.hits() != 1 || mock.downloads() != 1 {
		t.Fatalf("命中/下载次数 = %d/%d，期望 1/1", mock.hits(), mock.downloads())
	}
}

func TestDashScopeTTSDriverFailureModes(t *testing.T) {
	cases := []struct {
		name      string
		mode      ttsMockMode
		wantClass string
		wantCode  string
	}{
		{"上游 5xx", ttsMockStatus500, ErrorClassUpstream, "http_500"},
		{"非 JSON 响应", ttsMockNotJSON, ErrorClassUpstream, "invalid_response"},
		{"缺音频地址", ttsMockNoURL, ErrorClassUpstream, "invalid_response"},
		{"断流（半截音频）", ttsMockTruncate, ErrorClassTransport, "transport"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := newTTSMockUpstream(t, tc.mode)
			driver := NewDashScopeTTSDriver(true)

			res, err := driver.Speak(context.Background(), ttsChannel(mock.speechURL(), "sk-upstream-key"),
				SpeechRequest{Text: "abc", Voice: "longxiaochun_v2"})
			if err == nil {
				t.Fatalf("必须报错（res=%+v）", res)
			}
			if res != nil {
				t.Fatalf("失败时不得返回半截结果: %+v", res)
			}
			var upstream *UpstreamError
			if !errors.As(err, &upstream) {
				t.Fatalf("错误类型 = %T，期望 *UpstreamError: %v", err, err)
			}
			if upstream.Class != tc.wantClass || upstream.Code != tc.wantCode {
				t.Fatalf("分类/码 = %s/%s，期望 %s/%s（文案 %q）",
					upstream.Class, upstream.Code, tc.wantClass, tc.wantCode, upstream.Message)
			}
			if !strings.Contains(upstream.Message, DashScopeTTSDriverName) {
				t.Fatalf("文案应带驱动名（源形态 '[dashscope_tts] …'），实际 %q", upstream.Message)
			}
			// 断流/坏响应都不得触发重试（可重试只留给「连接建立阶段失败」）。
			if upstream.Retryable {
				t.Fatalf("该失败不得标记可重试: %+v", upstream)
			}
		})
	}
}

func TestDashScopeTTSDriverFailClosedWithoutAPIKey(t *testing.T) {
	mock := newTTSMockUpstream(t, ttsMockAudioBytes)
	driver := NewDashScopeTTSDriver(true)

	_, err := driver.Speak(context.Background(), ttsChannel(mock.speechURL(), "   "),
		SpeechRequest{Text: "abc", Voice: "longxiaochun_v2"})
	if err == nil {
		t.Fatal("渠道 api_key 为空必须 fail-closed")
	}
	var upstream *UpstreamError
	if !errors.As(err, &upstream) || upstream.Class != ErrorClassLocal || upstream.Code != "missing_api_key" {
		t.Fatalf("期望 local/missing_api_key，实际 %v", err)
	}
	if mock.hits() != 0 {
		t.Fatalf("凭证缺失时不得发起上游调用，实际命中 %d 次", mock.hits())
	}
}

func TestDashScopeTTSDriverEmptyTextRejected(t *testing.T) {
	mock := newTTSMockUpstream(t, ttsMockAudioBytes)
	driver := NewDashScopeTTSDriver(true)
	if _, err := driver.Speak(context.Background(), ttsChannel(mock.speechURL(), "sk-key"),
		SpeechRequest{Text: "   "}); err == nil {
		t.Fatal("空文本必须被拒")
	}
	if mock.hits() != 0 {
		t.Fatalf("空文本不得发起上游调用，实际 %d 次", mock.hits())
	}
}

// ===== 出站护栏与脱敏（既有两项契约的继承）=====

func TestDashScopeTTSDriverOutboundGuard(t *testing.T) {
	mock := newTTSMockUpstream(t, ttsMockAudioBytes)

	// allow_private_hosts=0 ⇒ 回环目标被护栏拦下（且一个字节都没发出去）。
	blocked := NewDashScopeTTSDriver(false)
	_, err := blocked.Speak(context.Background(), ttsChannel(mock.speechURL(), "sk-upstream-key"),
		SpeechRequest{Text: "abc", Voice: "longxiaochun_v2"})
	if err == nil {
		t.Fatal("allow_private_hosts=0 时回环上游必须被拒")
	}
	if !strings.Contains(err.Error(), "出站护栏") {
		t.Fatalf("拒绝文案应来自出站护栏，实际 %q", err.Error())
	}
	if mock.hits() != 0 {
		t.Fatalf("被护栏拦下时上游命中次数应为 0，实际 %d", mock.hits())
	}

	// allow_private_hosts=1 ⇒ 放行（本机自建上游口径）。
	allowed := NewDashScopeTTSDriver(true)
	if _, err := allowed.Speak(context.Background(), ttsChannel(mock.speechURL(), "sk-upstream-key"),
		SpeechRequest{Text: "abc", Voice: "longxiaochun_v2"}); err != nil {
		t.Fatalf("allow_private_hosts=1 时应放行: %v", err)
	}
	if mock.hits() != 1 {
		t.Fatalf("放行后上游命中次数 = %d，期望 1", mock.hits())
	}
}

func TestDashScopeTTSDriverRedactsEchoedKey(t *testing.T) {
	const secret = "sk-tts-key-0123456789"
	mock := newTTSMockUpstream(t, ttsMockEchoKey)
	driver := NewDashScopeTTSDriver(true)

	_, err := driver.Speak(context.Background(), ttsChannel(mock.speechURL(), secret),
		SpeechRequest{Text: "abc", Voice: "longxiaochun_v2"})
	if err == nil {
		t.Fatal("401 必须报错")
	}
	message := err.Error()
	if strings.Contains(message, secret) {
		t.Fatalf("错误文案出现明文密钥: %q", message)
	}
	if !strings.Contains(message, maskSecret(secret)) {
		t.Fatalf("错误文案应含掩码 %q，实际 %q", maskSecret(secret), message)
	}
}

// ===== 计量口径（Service 级，无库）=====

func TestServiceSpeechMeteringFrozen(t *testing.T) {
	mock := newTTSMockUpstream(t, ttsMockAudioBytes)
	repo := ttsRepo(mock.speechURL(), "sk-upstream-key")
	service := NewService(repo, NewDashScopeTTSDriver(true), Config{}, nil)

	// 文本 7 字符（含 CJK）：cost = 7/1e6×3.000000 = 0.000021（逐位锚点）。
	text := "你好世界！ok"
	if speechCharCount(text) != 7 {
		t.Fatalf("用例文本字符数 = %d，期望 7（口径锚点会失配）", speechCharCount(text))
	}
	res, err := service.Speech(context.Background(), SpeechInput{
		Model: "tts-test", Text: text, Voice: "longxiaochun_v2",
		BizType: "api", Source: publicSource,
	})
	if err != nil {
		t.Fatalf("合成失败: %v", err)
	}
	if string(res.Audio) != string(ttsMockAudio) {
		t.Fatalf("音频字节不符: %q", string(res.Audio))
	}
	if mock.hits() != 1 {
		t.Fatalf("上游命中次数 = %d，期望 1", mock.hits())
	}
	if len(repo.usages) != 1 {
		t.Fatalf("usage 行数 = %d，期望 1", len(repo.usages))
	}
	usage := repo.usages[0]
	if usage.CallType != CallTypeTTS {
		t.Fatalf("call_type = %q，期望 %q", usage.CallType, CallTypeTTS)
	}
	if usage.PromptTokens != 7 || usage.TotalTokens != 7 || usage.CompletionTokens != 0 {
		t.Fatalf("token 计数 = prompt %d / completion %d / total %d，期望 7/0/7",
			usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens)
	}
	if usage.TokensSource != TokensSourceEstimate {
		t.Fatalf("tokens_source = %q，期望 %q（字符数是本地计数）", usage.TokensSource, TokensSourceEstimate)
	}
	wantCost, _ := ComputeCost(7, 0, "3.000000", "0.000000")
	if usage.Cost != wantCost || usage.Cost != "0.000021" {
		t.Fatalf("cost = %q，期望 %q（=0.000021）", usage.Cost, wantCost)
	}
	if usage.Status != UsageStatusSuccess || usage.Stream != 0 {
		t.Fatalf("status/stream = %s/%d，期望 success/0", usage.Status, usage.Stream)
	}
	if usage.ChannelID != 41 || usage.ModelName != "tts-test" {
		t.Fatalf("渠道/模型归属不符: %+v", usage)
	}
}

func TestServiceSpeechFreezesCharsEvenWhenUpstreamReturnsUsage(t *testing.T) {
	// JSON 信封里带了 usage.characters=3：冻结口径下 prompt_tokens 仍是输入字符数，不随上游字段漂移。
	mock := newTTSMockUpstream(t, ttsMockJSONURL)
	repo := ttsRepo(mock.speechURL(), "sk-upstream-key")
	service := NewService(repo, NewDashScopeTTSDriver(true), Config{}, nil)

	if _, err := service.Speech(context.Background(), SpeechInput{
		Model: "tts-test", Text: "一二三四五", Voice: "longxiaochun_v2",
		BizType: "api", Source: publicSource,
	}); err != nil {
		t.Fatalf("合成失败: %v", err)
	}
	if len(repo.usages) != 1 {
		t.Fatalf("usage 行数 = %d，期望 1", len(repo.usages))
	}
	if repo.usages[0].PromptTokens != 5 {
		t.Fatalf("prompt_tokens = %d，期望 5（恒=输入字符数）", repo.usages[0].PromptTokens)
	}
}

// ===== 能力门 / 失败结算 =====

func TestServiceSpeechCapabilityGateRejectsChatModel(t *testing.T) {
	mock := newTTSMockUpstream(t, ttsMockAudioBytes)
	repo := ttsRepo(mock.speechURL(), "sk-upstream-key")
	repo.model.Type = ModelTypeChat // 模型类型不匹配
	service := NewService(repo, NewDashScopeTTSDriver(true), Config{}, nil)

	_, err := service.Speech(context.Background(), SpeechInput{
		Model: "tts-test", Text: "abc", Voice: "longxiaochun_v2",
		BizType: "api", Source: publicSource,
	})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.HTTPStatus != 400 || apiErr.Code != "model_type_mismatch" {
		t.Fatalf("模型 type=chat 走语音必须 400 model_type_mismatch，实际 %v", err)
	}
	if mock.hits() != 0 {
		t.Fatalf("能力门必须在上游之前拦下，实际命中 %d 次", mock.hits())
	}
	if len(repo.usages) != 0 {
		t.Fatalf("能力门失败不写 usage，实际 %d 行", len(repo.usages))
	}
}

func TestServiceSpeechDriverWithoutSpeechFailsLoudly(t *testing.T) {
	// 只有 Driver 四个方法、没有 Speak：语音必须响亮失败并落一行失败 usage。
	repo := ttsRepo("https://api.example.com", "sk-upstream-key")
	service := NewService(repo, &fakeDriver{}, Config{}, nil)

	_, err := service.Speech(context.Background(), SpeechInput{
		Model: "tts-test", Text: "abc", Voice: "longxiaochun_v2",
		BizType: "api", Source: publicSource,
	})
	// 候选耗尽的公开文案不泄露内部细节 ⇒ 断言落在 Detail（内部原因）上。
	var apiErr *APIError
	if !errors.As(err, &apiErr) || !strings.Contains(apiErr.Detail(), "驱动不支持语音合成") {
		t.Fatalf("应给「驱动不支持语音合成」的响亮失败，实际 %v", err)
	}
	if len(repo.usages) != 1 {
		t.Fatalf("失败路径应落 1 行 usage，实际 %d", len(repo.usages))
	}
	usage := repo.usages[0]
	if usage.CallType != CallTypeTTS || usage.Status != UsageStatusFail || usage.ErrorClass != ErrorClassLocal {
		t.Fatalf("失败 usage 不符: %+v", usage)
	}
	if usage.Cost != "0.000000" || usage.TokensSource != TokensSourceNone {
		t.Fatalf("失败行不得计费/计数: %+v", usage)
	}
}

func TestServiceSpeechRequiresTextAndVoice(t *testing.T) {
	repo := ttsRepo("https://api.example.com", "sk-upstream-key")
	service := NewService(repo, NewDashScopeTTSDriver(true), Config{}, nil)

	for _, tc := range []struct {
		label string
		in    SpeechInput
		code  string
	}{
		{"空文本", SpeechInput{Model: "tts-test", Voice: "v", BizType: "api", Source: publicSource}, "empty_speech_text"},
		{"缺音色", SpeechInput{Model: "tts-test", Text: "abc", BizType: "api", Source: publicSource}, "missing_voice"},
		{"缺 biz_type", SpeechInput{Model: "tts-test", Text: "abc", Voice: "v", Source: publicSource}, "invalid_request_error"},
	} {
		_, err := service.Speech(context.Background(), tc.in)
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.HTTPStatus != 400 || apiErr.Code != tc.code {
			t.Fatalf("%s 期望 400/%s，实际 %v", tc.label, tc.code, err)
		}
	}
}

func TestServiceSpeechFailureSettlementRecordsUpstreamError(t *testing.T) {
	driver := &ttsSpeechDriver{speechErr: &UpstreamError{
		HTTPStatus: 429, Class: ErrorClassRateLimit, Code: "http_429", Message: "上游限流",
	}}
	repo := ttsRepo("https://api.example.com", "sk-upstream-key")
	service := NewService(repo, driver, Config{}, nil)

	_, err := service.Speech(context.Background(), SpeechInput{
		Model: "tts-test", Text: "abc", Voice: "longxiaochun_v2",
		BizType: "api", Source: publicSource,
	})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.HTTPStatus != 429 {
		t.Fatalf("上游 429 应映射成公开 429，实际 %v", err)
	}
	if len(repo.usages) != 1 {
		t.Fatalf("失败路径 usage 行数 = %d，期望 1", len(repo.usages))
	}
	usage := repo.usages[0]
	if usage.Status != UsageStatusFail || usage.ErrorClass != ErrorClassRateLimit || usage.CallType != CallTypeTTS {
		t.Fatalf("失败 usage 不符: %+v", usage)
	}
}

// ===== 公开面 POST /ai/v1/audio/speech =====

func newSpeechEngine(t *testing.T, repo Repository, driver Driver, apiKey string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	d := engineDeps(repo, driver, apiKey)
	engine := gin.New()
	// 与接线口径同形：bearerAuth（公开面鉴权）→ 业务处理器。
	engine.POST("/ai/v1/audio/speech", d.bearerAuth(), d.publicSpeech)
	return engine
}

func TestPublicSpeechReturnsAudioBytes(t *testing.T) {
	mock := newTTSMockUpstream(t, ttsMockAudioBytes)
	repo := ttsRepo(mock.speechURL(), "sk-upstream-key")
	engine := newSpeechEngine(t, repo, NewDashScopeTTSDriver(true), "sk-gateway-key")

	recorder := doRequest(engine, http.MethodPost, "/ai/v1/audio/speech",
		`{"model":"tts-test","input":"你好世界","voice":"longxiaochun_v2","response_format":"mp3"}`,
		map[string]string{"Authorization": "Bearer sk-gateway-key"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Content-Type") != "audio/mpeg" {
		t.Fatalf("Content-Type = %q，期望 audio/mpeg", recorder.Header().Get("Content-Type"))
	}
	if recorder.Body.String() != string(ttsMockAudio) {
		t.Fatalf("响应体不是逐位音频字节: %q", recorder.Body.String())
	}
	if mock.hits() != 1 {
		t.Fatalf("上游命中次数 = %d，期望 1", mock.hits())
	}
	if len(repo.usages) != 1 || repo.usages[0].Source != publicSource {
		t.Fatalf("公开面 usage 归属不符: %+v", repo.usages)
	}
	if repo.usages[0].BizType != publicBizType {
		t.Fatalf("biz_type = %q，期望 %q", repo.usages[0].BizType, publicBizType)
	}
}

func TestPublicSpeechValidationAndAuth(t *testing.T) {
	mock := newTTSMockUpstream(t, ttsMockAudioBytes)
	repo := ttsRepo(mock.speechURL(), "sk-upstream-key")
	engine := newSpeechEngine(t, repo, NewDashScopeTTSDriver(true), "sk-gateway-key")

	cases := []struct {
		label   string
		body    string
		auth    string
		status  int
		errCode string
	}{
		{"缺 model", `{"input":"你好","voice":"v"}`, "Bearer sk-gateway-key", http.StatusBadRequest, "invalid_request_error"},
		{"缺 input", `{"model":"tts-test","voice":"v"}`, "Bearer sk-gateway-key", http.StatusBadRequest, "invalid_request_error"},
		{"缺 voice", `{"model":"tts-test","input":"你好"}`, "Bearer sk-gateway-key", http.StatusBadRequest, "invalid_request_error"},
		{"非法格式", `{"model":"tts-test","input":"你好","voice":"v","response_format":"aac"}`, "Bearer sk-gateway-key", http.StatusBadRequest, "invalid_voice_format"},
		{"错凭证", `{"model":"tts-test","input":"你好","voice":"v"}`, "Bearer wrong-key", http.StatusUnauthorized, "invalid_api_key"},
		{"缺凭证", `{"model":"tts-test","input":"你好","voice":"v"}`, "", http.StatusUnauthorized, "invalid_api_key"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			headers := map[string]string{}
			if tc.auth != "" {
				headers["Authorization"] = tc.auth
			}
			recorder := doRequest(engine, http.MethodPost, "/ai/v1/audio/speech", tc.body, headers)
			if recorder.Code != tc.status {
				t.Fatalf("状态码 = %d，期望 %d（body=%s）", recorder.Code, tc.status, recorder.Body.String())
			}
			var payload struct {
				Error struct {
					Message string `json:"message"`
					Type    string `json:"type"`
					Code    string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
				t.Fatalf("错误回执不是 OpenAI 形状: %v（%s）", err, recorder.Body.String())
			}
			if payload.Error.Code != tc.errCode {
				t.Fatalf("error.code = %q，期望 %q", payload.Error.Code, tc.errCode)
			}
			if payload.Error.Message == "" || payload.Error.Type == "" {
				t.Fatalf("错误回执缺 message/type: %s", recorder.Body.String())
			}
		})
	}
	if mock.hits() != 0 {
		t.Fatalf("参数/鉴权失败不得触达上游，实际命中 %d 次", mock.hits())
	}
}

func TestPublicSpeechReturnsOpenAIShapedErrorOnUpstreamFailure(t *testing.T) {
	mock := newTTSMockUpstream(t, ttsMockStatus500)
	repo := ttsRepo(mock.speechURL(), "sk-upstream-key")
	engine := newSpeechEngine(t, repo, NewDashScopeTTSDriver(true), "sk-gateway-key")

	recorder := doRequest(engine, http.MethodPost, "/ai/v1/audio/speech",
		`{"model":"tts-test","input":"你好","voice":"longxiaochun_v2"}`,
		map[string]string{"Authorization": "Bearer sk-gateway-key"})
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("上游 5xx 应映射 503，实际 %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"error"`) {
		t.Fatalf("错误回执不是 OpenAI 形状: %s", recorder.Body.String())
	}
	// 公开面文案不得泄露内部渠道/上游细节。
	if strings.Contains(recorder.Body.String(), "upstream exploded") || strings.Contains(recorder.Body.String(), "tts-主渠道") {
		t.Fatalf("公开回执泄露内部细节: %s", recorder.Body.String())
	}
}

// TestPublicSpeechEnforcesModelScope 是跨面访问控制的回归：语音公开面必须与 chat / embeddings /
// models 三条一样消费 DB Key 的 `model_scope`（此前只接了计量 source 接缝，白名单外的模型
// 在语音面照样 200 并真实合成计费）。
//
// 判据（与 publicChat/publicEmbeddings 逐字同形状）：白名单外 ⇒ 404 + code=model_not_found 且
// **不触上游、不写 usage**；白名单内 ⇒ 正常成音频；未设置 scope ⇒ 旧行为（不过滤、不拒绝）。
func TestPublicSpeechEnforcesModelScope(t *testing.T) {
	mock := newTTSMockUpstream(t, ttsMockAudioBytes)
	repo := ttsRepo(mock.speechURL(), "sk-upstream-key")
	d := engineDeps(repo, NewDashScopeTTSDriver(true), "sk-gateway-key")
	gin.SetMode(gin.TestMode)

	// withScope 复刻 apiKeyAuth 命中带 model_scope 的 DB Key 时写的接缝（setKeyScope 是写口）。
	withScope := func(scope ...string) gin.HandlerFunc {
		return func(c *gin.Context) {
			setKeyScope(c, scope)
			c.Next()
		}
	}
	engine := gin.New()
	engine.POST("/out-of-scope", withScope("other-model"), d.bearerAuth(), d.publicSpeech)
	engine.POST("/in-scope", withScope("tts-test"), d.bearerAuth(), d.publicSpeech)
	engine.POST("/no-scope", d.bearerAuth(), d.publicSpeech)

	body := `{"model":"tts-test","input":"你好","voice":"longxiaochun_v2"}`
	headers := map[string]string{"Authorization": "Bearer sk-gateway-key"}

	recorder := doRequest(engine, http.MethodPost, "/out-of-scope", body, headers)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("白名单外模型必须 404，实际 %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Error struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("错误回执不是 OpenAI 形状: %v（%s）", err, recorder.Body.String())
	}
	if payload.Error.Code != "model_not_found" || payload.Error.Type != "invalid_request_error" {
		t.Fatalf("回执 code/type = %q/%q，期望 model_not_found/invalid_request_error", payload.Error.Code, payload.Error.Type)
	}
	if mock.hits() != 0 || len(repo.usages) != 0 {
		t.Fatalf("白名单拒绝必须发生在发上游之前：hits=%d usages=%d", mock.hits(), len(repo.usages))
	}

	// 白名单内含该模型 ⇒ 正常合成（证明本用例拦的是白名单，不是别的门）。
	recorder = doRequest(engine, http.MethodPost, "/in-scope", body, headers)
	if recorder.Code != http.StatusOK || recorder.Body.String() != string(ttsMockAudio) {
		t.Fatalf("白名单内模型应正常出音频，实际 %d（body=%s）", recorder.Code, recorder.Body.String())
	}
	if mock.hits() != 1 {
		t.Fatalf("白名单内这一发应触上游 1 次，实际 %d", mock.hits())
	}

	// 未设置 scope（全局 key / 无 model_scope 的 Key）⇒ 输出与旧行为逐字一致。
	recorder = doRequest(engine, http.MethodPost, "/no-scope", body, headers)
	if recorder.Code != http.StatusOK || recorder.Body.String() != string(ttsMockAudio) {
		t.Fatalf("未设置 scope 必须恒放行（v1 行为不变），实际 %d（body=%s）", recorder.Code, recorder.Body.String())
	}
}

// ===== Models（探活）=====

func TestDashScopeTTSDriverModelsProbesBySynthesis(t *testing.T) {
	mock := newTTSMockUpstream(t, ttsMockAudioBytes)
	driver := NewDashScopeTTSDriver(true)

	models, err := driver.Models(context.Background(), ttsChannel(mock.speechURL(), "sk-upstream-key"))
	if err != nil {
		t.Fatalf("探活失败: %v", err)
	}
	if len(models) != 0 {
		t.Fatalf("语音渠道的 models_count 应为 0（源口径），实际 %v", models)
	}
	if mock.hits() != 1 {
		t.Fatalf("探活应发一次真实合成（源 ChannelProbe 同口径），实际命中 %d 次", mock.hits())
	}
	body := mock.requestBody(t)
	input, _ := body["input"].(map[string]any)
	if input == nil || input["text"] != DashScopeTTSProbeText || input["voice"] != voiceProbeVoice {
		t.Fatalf("探活请求体不符源契约: %v", body)
	}

	// 凭证缺失：探活同样 fail-closed（不发起调用）。
	if _, err := driver.Models(context.Background(), ttsChannel(mock.speechURL(), "")); err == nil {
		t.Fatal("凭证缺失时探活必须报错")
	}
	if mock.hits() != 1 {
		t.Fatalf("凭证缺失的探活不得新增上游命中，实际 %d 次", mock.hits())
	}
}

// ===== 能力门（驱动自身的响亮失败）=====

func TestDashScopeTTSDriverRefusesChatAndEmbedding(t *testing.T) {
	driver := NewDashScopeTTSDriver(true)
	ch := ttsChannel("https://api.example.com", "sk-key")
	if _, err := driver.Chat(context.Background(), ch, ChatRequest{}); err == nil {
		t.Fatal("语音驱动的 Chat 必须响亮失败")
	} else if !strings.Contains(err.Error(), "语音驱动不支持 chat") {
		t.Fatalf("Chat 失败文案不符源口径: %v", err)
	}
	if _, err := driver.ChatStream(context.Background(), ch, ChatRequest{}, nil); err == nil {
		t.Fatal("语音驱动的 ChatStream 必须响亮失败")
	}
	if _, err := driver.Embeddings(context.Background(), ch, EmbeddingRequest{}); err == nil {
		t.Fatal("语音驱动的 Embeddings 必须响亮失败")
	} else if !strings.Contains(err.Error(), "语音驱动不支持 embedding") {
		t.Fatalf("Embeddings 失败文案不符源口径: %v", err)
	}
	if !driver.SupportsSpeech() {
		t.Fatal("dashscope_tts 必须声明支持语音合成")
	}
	caps := driver.Capabilities()
	if len(caps) != 1 || caps[0] != "speech" {
		t.Fatalf("能力声明应只有 speech，实际 %v", caps)
	}
}

// 编译期断言：dashscope_tts 同时满足编排层的 Driver 与服务层的 SpeechDriver。
var (
	_ Driver       = (*DashScopeTTSDriver)(nil)
	_ SpeechDriver = (*DashScopeTTSDriver)(nil)
)
