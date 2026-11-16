// plugin: ai v0.2.0
// voice.go 是语音合成（TTS）的领域层：plugin_ai_voice 的行模型、格式/采样的闭集口径，
// 以及全插件共用的两件小事实——「字符口径的计数」与「音频响应头」。
//
// 单一事实源纪律（同 model.go 的头注）：
//   - 表名走 table()（前缀单点拼接）；
//   - 闭集常量只在这里声明一次（PL 迁移 6 的 CHECK 与之逐字对齐）；
//   - 与源的对照物是 src/app/ai/driver/DashScopeTtsDriver.php（源仓相对路径；PHP 侧只读对照）
//     （端点与请求体口径）、ChannelProbe.php:244-277（语音渠道探活）、
//     database/migrations/20260909155604_radmin_ai_voice.php（闭集扩展）。
package ai

import (
	"strings"
)

// 音频格式闭集（plugin_ai_voice.format 的 CHECK；空值 ⇒ 默认 mp3，照源 'format' => 'mp3'）。
const (
	VoiceFormatMP3  = "mp3"
	VoiceFormatWAV  = "wav"
	VoiceFormatPCM  = "pcm"
	VoiceFormatOpus = "opus"

	// VoiceFormatDefault 是格式缺省值（源 DashScopeTtsDriver::speak 的 'format' => 'mp3'）。
	VoiceFormatDefault = VoiceFormatMP3
)

// 采样率口径（源：input.sample_rate 默认 24000；百炼 SpeechSynthesizer 支持 8k~48k）。
const (
	VoiceSampleRateDefault = 24000
	voiceSampleRateMin     = 8000
	voiceSampleRateMax     = 48000
)

// 探活默认音色（源 ChannelProbe::speakCoroutine 硬编码 'longxiaochun_v2'）。
const voiceProbeVoice = "longxiaochun_v2"

// voiceCallType 是语音合成的调用类型：取 domain_closure.go 的 CallTypeTTS（闭集单一事实源，
// 与本插件迁移 6 的 CHECK 逐字对齐），本文件只留一个语义别名，避免调用点写裸字面量。
const voiceCallType = CallTypeTTS

// voiceFormats 格式闭集（顺序即错误文案里的枚举顺序）。
var voiceFormats = []string{VoiceFormatMP3, VoiceFormatWAV, VoiceFormatPCM, VoiceFormatOpus}

// Voice 是 plugin_ai_voice 的领域行，同时是 GORM 行模型。
//
// Voice 是**上游音色 ID**（如 longxiaochun_v2），与行名 Name（管理标识）刻意分开：
// 同一个上游音色可以在多个逻辑模型下各配一条，重命名不影响上游契约。
type Voice struct {
	ID         uint64 `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	Name       string `gorm:"column:name;type:varchar(50);not null;default:''" json:"name"`
	Model      string `gorm:"column:model;type:varchar(100);not null;default:''" json:"model"`
	Voice      string `gorm:"column:voice;type:varchar(60);not null;default:''" json:"voice"`
	Format     string `gorm:"column:format;type:varchar(10);not null;default:'mp3'" json:"format"`
	Status     int16  `gorm:"column:status;not null;default:1" json:"status"`
	Remark     string `gorm:"column:remark;type:varchar(255);not null;default:''" json:"remark"`
	CreateTime int64  `gorm:"column:create_time;not null;default:0" json:"create_time"`
	UpdateTime int64  `gorm:"column:update_time;not null;default:0" json:"update_time"`
}

// TableName 物理表名（前缀由 model.go 的 table() 单点拼接）。
func (Voice) TableName() string { return table("plugin_ai_voice") }

// Enabled 音色是否启用（v1 只作展示与筛选，调用期不校验）。
func (v Voice) Enabled() bool { return v.Status == StatusEnabled }

// VoiceFormatValid 判定格式是否命中闭集（大小写敏感：闭集是逐字比较，不做归一化）。
func VoiceFormatValid(value string) bool {
	for _, allowed := range voiceFormats {
		if value == allowed {
			return true
		}
	}
	return false
}

// NormalizeVoiceFormat 收敛格式：空值/纯空白 ⇒ 默认 mp3；闭集外**响亮拒绝**（不静默回落，
// 否则调用方以为选了 wav、上游却按 mp3 合成）。
func NormalizeVoiceFormat(value string) (string, error) {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	if trimmed == "" {
		return VoiceFormatDefault, nil
	}
	if VoiceFormatValid(trimmed) {
		return trimmed, nil
	}
	return "", &APIError{
		HTTPStatus: 400,
		Type:       "invalid_request_error",
		Code:       "invalid_voice_format",
		Message:    "音频格式 " + value + " 不在闭集内（允许：" + strings.Join(voiceFormats, "|") + "）",
		Class:      ErrorClassLocal,
	}
}

// NormalizeVoiceSampleRate 收敛采样率：0 ⇒ 默认 24000；越界夹到 [8000, 48000]。
func NormalizeVoiceSampleRate(value int) int {
	if value <= 0 {
		return VoiceSampleRateDefault
	}
	return clampInt(value, voiceSampleRateMin, voiceSampleRateMax)
}

// speechContentType 音频格式 → 响应 Content-Type（公开面与试听页共用同一出口）。
//
// 未知格式回落 application/octet-stream（不是音频格式就不冒充音频类型）。
func speechContentType(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case VoiceFormatMP3:
		return "audio/mpeg"
	case VoiceFormatWAV:
		return "audio/wav"
	case VoiceFormatPCM:
		return "audio/pcm"
	case VoiceFormatOpus:
		return "audio/ogg"
	default:
		return "application/octet-stream"
	}
}

// speechCharCount 是 TTS 的计数口径：输入文本的**字符数**（[]rune 长度，与源 mb_strlen 同口径——
// CJK 一字符计 1，字节数不参与计算）。
//
// 这个数直接进 usage.prompt_tokens 并作为计费基数（口径与差异见 service_tts.go 头注）。
func speechCharCount(text string) int {
	return len([]rune(text))
}
