// plugin: ai v0.2.0
// redact.go 是密钥脱敏的**唯一实现**（相对 v1 都在 plugin.go 里）与驱动错误的脱敏出口。
//
// 算法（v1 → 本品的变化）：
//   - v1：候选长度 < 6 直接跳过（短密钥不进替换 ⇒ 一旦上游回显短密钥就明文落库）；
//   - 本品：**非空即替换**——长值（≥6 字符）整串替换；短值（<6）只在「前后皆为非字母数字
//     边界」时替换（独立词判定），从而既不漏短值，也不会把正常文案里的片段误伤
//     （如 secret="abc" 时 "xabcx" 不动、"abc" 单独出现才替换）。
//
// 与「≥16 位」的设置面校验（handler_overview.go 的 settingsSaveRequest.updates）配套：
// 源头把网关密钥挡在 16 位以下，出口再兜一层（渠道里遗留的短密钥也照样脱敏）。
//
// 单一出口纪律（对外契约）：
//   - redactSecret 用于「把可能回显密钥的文案透出之前」（渠道 CRUD 的 DB 报错、探活回执…）；
//   - redactUpstreamErr 用于驱动返回错误的出口——Chat/ChatStream/Embeddings/Models 四个
//     方法在返回前统一调用，于是 usage.error_message（service.go 的 ClassifyError 取 Message）、
//     渠道 last_error（circuit.go → store.MarkFailure）、探活落库与回执（handler_channel.go）
//     拿到的都是同一份已脱敏文案。
package ai

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// shortSecretRunes 是「短值」的门槛：长度 < 6 的密钥按独立词边界判定替换（见文件头注）。
const shortSecretRunes = 6

// redactSecret 把错误文案里可能被回显的密钥换成掩码。
//
// 用途：数据库/驱动的报错偶尔会把写入值带进文案（例如类型解析失败回显 "值"），
// 而「密钥永不进日志与回执」是本插件的不变式——所以在把底层错误透出给接口之前先脱敏。
//
// 规则：非空即参与替换；长值（≥6 字符）整串替换，短值按前后非字母数字边界判定替换；
// 空串与纯空白候选跳过（未配置）。掩码口径与 maskSecret 逐字一致（等长掩码）。
func redactSecret(message string, secrets ...string) string {
	for _, secret := range secrets {
		secret = strings.TrimSpace(secret)
		if secret == "" {
			continue
		}
		mask := maskSecret(secret)
		if len([]rune(secret)) < shortSecretRunes {
			message = replaceBoundedSecret(message, secret, mask)
			continue
		}
		message = strings.ReplaceAll(message, secret, mask)
	}
	return message
}

// replaceBoundedSecret 替换「前后皆为非字母数字边界」的短密钥（独立词）。
//
// 逐字节扫描而非正则：掩码里可能含正则替换串的特殊字符（$ 等），手工拼接可避免
// ReplaceAllString 的展开语义踩坑；短值出现在正常文案里的概率更高，故宁可不替换。
//
// 命中的片段若嵌在更长的词里（"xabcx"），**只前进一个字符**继续找——不能吞掉整段命中，
// 否则 "abcabc" 里第二个 abc 会因为前一个被吃掉而误判成行首独立词。
func replaceBoundedSecret(message, secret, mask string) string {
	var out strings.Builder
	out.Grow(len(message))
	cursor := 0
	for cursor <= len(message)-len(secret) {
		index := strings.Index(message[cursor:], secret)
		if index < 0 {
			break
		}
		start := cursor + index
		end := start + len(secret)
		if !isWordRuneAt(message, start-1) && !isWordRuneAt(message, end) {
			out.WriteString(message[cursor:start])
			out.WriteString(mask)
			cursor = end
			continue
		}
		size := 1
		if _, runeSize := utf8.DecodeRuneInString(message[start:]); runeSize > 0 {
			size = runeSize
		}
		out.WriteString(message[cursor : start+size])
		cursor = start + size
	}
	out.WriteString(message[cursor:])
	return out.String()
}

// isWordRuneAt 判定候选位置（可能越界）的字符是否是字母或数字。
//
// 越界（-1 或超过长度）视为边界；Unicode 字母/数字都算「词内字符」，故中文夹持的
// 短密钥不会被当作独立词（"密钥abc密钥" 不替换，语义上它属于同一串文案）。
func isWordRuneAt(text string, index int) bool {
	if index < 0 || index >= len(text) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(text[index:])
	return isWordRune(r)
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// redactUpstreamErr 是驱动出口的脱敏（保持错误分类字段与可重试标记原样）。
//
// 只克隆 *UpstreamError：它的 Class/Code/HTTPStatus/Retryable 是失败分类闭集的输入，
// 必须逐字保留；其余错误（*StreamAbortedError 等）原样返回——变类型会破坏
// errors.Is(err, ErrClientClosed) 一类判定，且它们的文案来自本进程、不含上游回显。
func redactUpstreamErr(err error, apiKey string) error {
	if err == nil || strings.TrimSpace(apiKey) == "" {
		return err
	}
	var upstream *UpstreamError
	if !errors.As(err, &upstream) || upstream == nil {
		return err
	}
	redacted := redactSecret(upstream.Message, apiKey)
	if redacted == upstream.Message {
		return err
	}
	clone := *upstream
	clone.Message = redacted
	return &clone
}
