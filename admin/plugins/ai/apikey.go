// plugin: ai v0.2.0
// apikey.go 是应用接入（API Key）的领域行与凭证原语：
//
//   - APIKey：plugin_ai_api_key 的行模型（也是领域行）。KeyHash 带 json:"-" —— **永不出口**；
//     明文只在 create / reset 的回执里出现一次（对齐源 ApiKey.php 的安全约定「key 明文仅签发/重置时返回一次」）。
//   - 凭证原语：GenerateAPIKey（crypto/rand）、HashAPIKey（sha256 十六进制）、APIKeyDisplayPrefix（展示前缀）。
//   - 入参收敛：ValidateAPIKeySource / NormalizeModelScope（纯函数，可单测）。
//
// 与源的差异（如实登记，均已写进本插件的 README 差异节与交付 risks）：
//   - source 最长 24 字符（源 64）：本插件的计量归因列 plugin_ai_usage.source 是 varchar(30)，
//     网关写入 'key:'+source ⇒ source ≤24 才能保证 usage.source 不被写口按列宽截断
//     （store.go:1080-1089 的 normalizeUsageColumns 按 30 截断，截断会让归因错位）。
//   - status 用 smallint 0/1（源 'enabled'|'disabled'）：与本插件渠道/模型表同口径（router.go 的
//     StatusEnabled/StatusDisabled + handler 的 validStatus），也让宿主前端 switch 控件直接可用。
//   - model_scope 的非数组值显式拒绝（源 parseModelScope 对非数组静默返回 []）：
//     静默把 'model_scope: 5' 当成「空白名单＝允许全部」是一次静默放权，本插件选择 fail-closed。
//   - key_prefix 的形态与源一致（前 12 字符 + '…'），但**不截断明文随机体**：明文 = 'ak_' + 32 字节
//     crypto/rand 的 base64url（46 字符），源是 'ak_' + 24 字符随机体。
package ai

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	// APIKeySecretPrefix 明文 key 的前缀（照源 generate_secret('ak_', 24) 的 'ak_'）。
	APIKeySecretPrefix = "ak_"
	// apiKeyRandomBytes 明文随机体字节数（crypto/rand 直接读出的熵；32 字节 = 256 位）。
	apiKeyRandomBytes = 32
	// apiKeyPrefixRunes 展示前缀的字符数（照源 substr($key, 0, 12)）。
	apiKeyPrefixRunes = 12
	// APIKeyMaxSource source 的最大字符数（'key:'+source ≤ 30 = plugin_ai_usage.source 列宽）。
	APIKeyMaxSource = 24
	// apiKeyMaxName 应用名最大字符数（= plugin_ai_api_key.name 列宽）。
	apiKeyMaxName = 50
	// apiKeyMaxRemark 备注最大字符数（= plugin_ai_api_key.remark 列宽）。
	apiKeyMaxRemark = 255
	// apiKeyMaxScopeItems 白名单项数上限（请求路径上的数组不得无界增长）。
	apiKeyMaxScopeItems = 100
	// apiKeyMaxScopeItem 白名单单项最大字符数（= plugin_ai_model.name 列宽）。
	apiKeyMaxScopeItem = 100
)

// APIKey 是 plugin_ai_api_key 的领域行，同时是 GORM 行模型（列与 constraints 以
// migrations/4_api_key.up.sql 为锚）。
//
// ModelScope 用 serializer:json 与 jsonb 列互转（与 router.go:100 的 Model.Capabilities 同款）。
// 读路径（鉴权）只认 Status/ExpireAt/ModelScope/RateLimit/Source/KeyPrefix；KeyHash 只用于查库。
type APIKey struct {
	ID         uint64   `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	Name       string   `gorm:"column:name;type:varchar(50);not null;default:''" json:"name"`
	KeyHash    string   `gorm:"column:key_hash;type:varchar(64);not null" json:"-"`
	KeyPrefix  string   `gorm:"column:key_prefix;type:varchar(24);not null;default:''" json:"key_prefix"`
	Source     string   `gorm:"column:source;type:varchar(24);not null;default:''" json:"source"`
	ModelScope []string `gorm:"column:model_scope;type:jsonb;not null;default:'[]';serializer:json" json:"model_scope"`
	RateLimit  int      `gorm:"column:rate_limit;not null;default:0" json:"rate_limit"`
	Status     int16    `gorm:"column:status;not null;default:1" json:"status"`
	ExpireAt   *int64   `gorm:"column:expire_at" json:"expire_at"`
	LastUsedAt *int64   `gorm:"column:last_used_at" json:"last_used_at"`
	Remark     string   `gorm:"column:remark;type:varchar(255);not null;default:''" json:"remark"`
	CreateTime int64    `gorm:"column:create_time;not null;default:0" json:"create_time"`
	UpdateTime int64    `gorm:"column:update_time;not null;default:0" json:"update_time"`
}

// TableName 物理表名（前缀由 model.go 的 table() 单点拼接）。
func (APIKey) TableName() string { return table("plugin_ai_api_key") }

// IsExpired 是否已过期（NULL/0 = 永不过期；now ≤ 0 时取当前时刻）。
func (r APIKey) IsExpired(now int64) bool {
	if r.ExpireAt == nil {
		return false
	}
	expire := *r.ExpireAt
	if expire <= 0 {
		return false
	}
	if now <= 0 {
		now = nowUnix()
	}
	return expire < now
}

// AllowsModel 模型白名单校验（空白名单 = 允许全部模型；照源 AiApiKey::allowsModel 的 in_array 口径）。
func (r APIKey) AllowsModel(model string) bool {
	if len(r.ModelScope) == 0 {
		return true
	}
	target := strings.TrimSpace(model)
	for _, item := range r.ModelScope {
		if item == target {
			return true
		}
	}
	return false
}

// Authenticatable 鉴权准入判定：status=1 且未过期（停用与过期都给 401，不区分文案——不泄露账户状态）。
func (r APIKey) Authenticatable(now int64) bool {
	return r.Status == StatusEnabled && !r.IsExpired(now)
}

// ===== 凭证原语 =====

// GenerateAPIKey 生成一枚新明文 key：'ak_' + crypto/rand 32 字节的 base64url（无填充）。
//
// 熵来源必须是 crypto/rand（math/rand 的种子可预测，等于没有凭证）；随机源不可用时返回错误，
// 由调用方按 fail-closed 回执（绝不回落到弱随机）。
func GenerateAPIKey() (string, error) {
	buf := make([]byte, apiKeyRandomBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成 API Key 失败（crypto/rand 不可用）: %w", err)
	}
	return APIKeySecretPrefix + base64.RawURLEncoding.EncodeToString(buf), nil
}

// HashAPIKey 明文 → sha256 十六进制（64 字符 = key_hash 列宽；照源 hash_secret 的口径）。
//
// 鉴权走「按哈希查库」（与源同等口径：AiGatewayAuthMiddleware 的 where key_hash = hash_secret($apiKey)），
// 因而命中路径不需要再比一次明文；全局配置 key 的兜底路径仍用 sha256 + subtle.ConstantTimeCompare。
func HashAPIKey(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// APIKeyDisplayPrefix 展示前缀：前 12 字符 + '…'（照源 substr($key, 0, 12) . '…'）。
// 这是**唯一**可以离开服务端的 key 形态（列表/详情只回它；明文只在签发/重置回执出现一次）。
func APIKeyDisplayPrefix(secret string) string {
	runes := []rune(strings.TrimSpace(secret))
	if len(runes) == 0 {
		return ""
	}
	if len(runes) <= apiKeyPrefixRunes {
		return string(runes) + "…"
	}
	return string(runes[:apiKeyPrefixRunes]) + "…"
}

// ===== 入参收敛（纯函数）=====

// ValidateAPIKeySource 校验调用方标识（计费/用量统计的归因维度）。
//
// 字符集照源 ApiKey.php 的 '/^[a-zA-Z0-9_:.\-]{1,64}$/'（本插件把长度收到 24，理由见文件头注）。
func ValidateAPIKeySource(raw string) (string, error) {
	source := strings.TrimSpace(raw)
	if source == "" {
		return "", errors.New("source 调用方标识不能为空（计费/用量统计维度）")
	}
	if length := len([]rune(source)); length > APIKeyMaxSource {
		return "", fmt.Errorf("source 最长 %d 个字符（'key:'+source 必须落在用量 source 列宽 30 内，当前 %d）",
			APIKeyMaxSource, length)
	}
	for _, item := range source {
		if !isAPIKeySourceRune(item) {
			return "", fmt.Errorf("source 仅允许字母、数字、下划线、冒号、点、连字符（最长 %d）", APIKeyMaxSource)
		}
	}
	return source, nil
}

// isAPIKeySourceRune 判定字符是否在 source 的字符集内（数字/字母/_:.-）。
func isAPIKeySourceRune(item rune) bool {
	switch {
	case item >= '0' && item <= '9':
		return true
	case item >= 'a' && item <= 'z':
		return true
	case item >= 'A' && item <= 'Z':
		return true
	case item == '_', item == ':', item == '.', item == '-':
		return true
	default:
		return false
	}
}

// NormalizeModelScope 收敛 model_scope（照源 parseModelScope，非数组值改为显式拒绝，见文件头注）：
//
//	缺省 / null            ⇒ nil（空 = 允许全部模型）
//	字符串                 ⇒ 先按 JSON 数组解析，失败则按逗号分隔
//	数组（[]any / []string）⇒ 只取字符串元素（非字符串剔除，照源）；逐项 trim、去重、丢空项
//	其它（数字/布尔/对象）  ⇒ 错误（fail-closed，不静默放权）
//
// 超长项（> 100 字符 = plugin_ai_model.name 列宽）与超出项数的部分一律丢弃——白名单是收窄语义，
// 丢项只会更严，绝不更宽。
func NormalizeModelScope(raw any) ([]string, error) {
	switch value := raw.(type) {
	case nil:
		return nil, nil
	case string:
		text := strings.TrimSpace(value)
		if text == "" {
			return nil, nil
		}
		var items []string
		if err := json.Unmarshal([]byte(text), &items); err == nil {
			return cleanModelScope(items), nil
		}
		return cleanModelScope(strings.Split(text, ",")), nil
	case []string:
		return cleanModelScope(value), nil
	case []any:
		items := make([]string, 0, len(value))
		for _, item := range value {
			if text, ok := item.(string); ok {
				items = append(items, text)
			}
		}
		return cleanModelScope(items), nil
	default:
		return nil, fmt.Errorf("model_scope 必须是字符串数组或逗号分隔字符串（实际是 %T）", raw)
	}
}

// cleanModelScope 去空白、丢空项、丢超长项、去重并限项数（顺序保持首次出现的次序）。
func cleanModelScope(items []string) []string {
	out := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		text := strings.TrimSpace(item)
		if text == "" || len([]rune(text)) > apiKeyMaxScopeItem {
			continue
		}
		if _, ok := seen[text]; ok {
			continue
		}
		seen[text] = struct{}{}
		out = append(out, text)
		if len(out) >= apiKeyMaxScopeItems {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// apiKeyScopeJSON 把白名单序列化成 jsonb 文本：空列表恒写 '[]'（绝不写 JSON null，
// 让「空白名单＝允许全部」在库里有唯一形态）。map 更新里 serializer 不生效 ⇒ 必须显式走它
// （同 store.go:735-773 对 capabilities 的处理）。
func apiKeyScopeJSON(scope []string) string {
	if len(scope) == 0 {
		return "[]"
	}
	raw, err := json.Marshal(scope)
	if err != nil {
		// []string 不可能 Marshal 失败；真失败了也只可能是编码器异常 ⇒ 写成空白名单（更严的一侧）。
		return "[]"
	}
	return string(raw)
}
