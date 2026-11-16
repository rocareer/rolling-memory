// plugin: ai v0.2.0
// apikey_auth.go 是公开面的唯一鉴权中间件（apiKeyAuth）：按 Key 查库 + 计量归因 + 按 Key 限流覆盖。
//
// 鉴权次序（每一步都以 fail-closed 收口；与 v1 的「单密钥比对」相比，只有第 3/4 步是新增面）：
//
//  1. 读插件配置 —— 读不到 ⇒ 503 settings_unavailable（与 v1 同形状）；
//  2. 解析 Authorization: Bearer（前缀大小写不敏感、token 不得为空；缺头/格式错 ⇒ 进第 5 步）；
//  3. 按 sha256(出示凭证) 查 plugin_ai_api_key（**查库出错 ⇒ 503 fail-closed**）：
//     命中且 status=1 且未过期 ⇒ 放行（第 4 步）；
//     命中但 status=0 或已过期 ⇒ 401（不回落全局比对 —— 停用的 Key 不得因「碰巧等于全局 key」复活）；
//     未命中（ErrRecordNotFound）⇒ 进第 5 步；
//  4. 命中分支的接缝与后台动作：
//     setKeySource(c, 'key:'+source) —— 计量归因（handler_public.go 的 ChatInput/EmbeddingInput.Source）；
//     setKeyScope(c, model_scope) —— 仅白名单非空时写（空 = 允许全部，等同未设置）；
//     rate_limit > 0 ⇒ setRateLimitOverride(c, &rateLimitOverride{RPM: rate_limit})（0 = 跟随插件全局，不写）；
//     每请求一个后台 goroutine 刷 last_used_at（见下方取舍）；
//  5. 全局兜底（configs.plugin_ai_api_key）：未配置 ⇒ 503 api_key_not_configured（现行为逐字不变）；
//     配置了 ⇒ sha256 + subtle.ConstantTimeCompare 比对，命中放行（source 维持 'key:gateway'），不匹配 401。
//
// 安全纪律：
//   - **查库出错绝不静默回落全局 key 比对**：一次 DB 抖动若回落，会让「已停用/已删除的 Key」重新可用；
//   - 未命中凭证一律 401，且「不存在 / 已停用 / 已过期 / 不匹配」共用同一份文案（不泄露账户状态）；
//   - 明文凭证只在内存里出现：查库只送 sha256，日志与错误文案都不带凭证（本文件不打印任何 header）。
//
// 口径冻结（model_scope 的**准入判定不在本层**，本层只喂接缝）：
//   - 三个接缝（setKeySource / setKeyScope / setRateLimitOverride）由 ratelimit.go 声明，本层是它们
//     唯一的写入方（命中 DB Key 时）；未命中即缺省 = v1 行为（source='key:gateway'、无 scope、无覆盖）；
//   - 四个公开端点**全部**消费白名单：handler_public.go:226（publicChat）、:454（publicEmbeddings）、
//     :551（publicModels 的过滤）、handler_voice.go 的 publicSpeech（语音面，同一 modelInKeyScope
//     判定 + 同一 `keyScopeModelNotFound` 404 回执形状）；**未设置 scope 时四条输出与 v1 逐字一致**
//     （接缝的 nil 语义：不过滤、不拒绝）。语音面这条此前是缺口（此前只接了 keySourceOf 计量接缝），
//     本批已补齐——四条公开链的判据是同一行 `modelInKeyScope`。
//     本层刻意**不做**第二套判定：中间件手里没有解析后的模型名，硬判就得把请求体再读一遍（由
//     「白名单为空才跳过」退化为「每个命中 DB Key 的请求都多一次读体」），且两处判定必然漂移；
//     权威判定点是拿到模型名之后、**发上游之前**的 handler。
//
// last_used_at 的刷新口径（与源的 60s 节流写**不同**，如实登记）：
//   - 每请求一个后台 goroutine，无上限、无重试、允许丢更新，失败只记日志 —— 照
//     internal/middleware/admin_log.go:86 的异步写先例；
//   - 取舍：换掉「每请求同步写」的响应延迟与同一 Key 的行锁竞争（本文件保证响应不等它）；
//     last_used_at 是展示/运维字段，不是计费口径（计费走 plugin_ai_usage）；
//   - 必须用 context.Background()：请求上下文在响应返回后即取消，带它进去的写必然失败。
package ai

import (
	"context"
	"errors"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// apiKeySourcePrefix 是计量归因的前缀：usage.source = 'key:' + <source>（照源 AiApi.php 的
// `$source = 'key:' . $key->source` 口径）；'key:'+source ≤ 30 = plugin_ai_usage.source 列宽
// （source 的 24 字符上限见 apikey.go）。
const apiKeySourcePrefix = "key:"

// apiKeyAuthStore 是鉴权中间件的数据面（窄接口，消费侧声明；*Store 实现，见 store_apikey.go）。
//
// 为什么是接口：*Store 绑定在 *gorm.DB 上，无库环境（单测）构造不出真实现；接口让五态与
// fail-closed 分支可以在无库环境下逐条覆盖，真库语义则由 apikey_db_test.go 覆盖
// （与 TokenCharger 同一「接口在消费侧声明」的惯例）。
type apiKeyAuthStore interface {
	// LookupAPIKeyByHash 按 sha256 key_hash 取行；未命中返回 gorm.ErrRecordNotFound。
	LookupAPIKeyByHash(ctx context.Context, keyHash string) (*APIKey, error)
	// TouchAPIKeyLastUsed 刷新 last_used_at（后台 goroutine 调用；失败只记日志）。
	TouchAPIKeyLastUsed(ctx context.Context, id int64, at int64) (int64, error)
}

// apiKeyAuth 生产装配：配置读 deps.loadSettings，查库面取 deps.loadAuthStore
// （缺省从 deps.loadStore 取 *Store）。
//
// 查库面单独留在 deps 里的理由：*Store 绑在 *gorm.DB 上，无库单测构造不出真实现，
// 而 handler_test.go 的公开面用例（协议形状/请求体上限/流式帧）要经**生产的挂载链**
// （mountPublic：apiKeyAuth → rateLimit → handler）跑；注入点缺位就只能让测试复制一遍链路，
// 生产的链组合反而无人覆盖。
func (d deps) apiKeyAuth() gin.HandlerFunc {
	loadAuthStore := d.loadAuthStore
	if loadAuthStore == nil {
		loadAuthStore = func(ctx context.Context) (apiKeyAuthStore, error) {
			store, err := d.loadStore(ctx)
			if err != nil {
				return nil, err
			}
			return store, nil
		}
	}
	return apiKeyAuthWith(d.loadSettings, loadAuthStore)
}

// apiKeyAuthWith 构造鉴权中间件：两个依赖显式入参（无库单测据此注入假实现）。
func apiKeyAuthWith(
	loadSettings func(context.Context) (pluginSettings, error),
	loadAuthStore func(context.Context) (apiKeyAuthStore, error),
) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()

		set, err := loadSettings(ctx)
		if err != nil {
			writePublicAPIError(c, &APIError{
				HTTPStatus: http.StatusServiceUnavailable,
				Type:       "upstream_unavailable",
				Code:       "settings_unavailable",
				Message:    "服务暂不可用",
				Class:      ErrorClassLocal,
				Err:        err,
			})
			return
		}

		token, hasToken := bearerToken(c.GetHeader("Authorization"))

		var authStore apiKeyAuthStore
		var row *APIKey
		if hasToken {
			authStore, err = loadAuthStore(ctx)
			if err != nil {
				// 拿不到查库面：fail-closed（不比对、不回落全局 key）。
				writePublicAPIError(c, storeUnavailableAPIError(err))
				return
			}
			row, err = authStore.LookupAPIKeyByHash(ctx, HashAPIKey(token))
			switch {
			case err == nil:
			case errors.Is(err, gorm.ErrRecordNotFound):
				row = nil
			default:
				// 查库出错：fail-closed。**绝不**静默回落全局 key 比对 ——
				// 一次 DB 抖动若回落，会让「已停用 / 已删除」的 Key 重新变成可用的网关凭证。
				writePublicAPIError(c, storeUnavailableAPIError(err))
				return
			}
		}

		if row != nil {
			if !row.Authenticatable(nowUnix()) {
				writePublicAPIError(c, unauthorizedAPIKeyError())
				return
			}
			// 计量归因：命中 DB Key 的 source 不再是 'key:gateway'（B2 修法的落点）。
			setKeySource(c, apiKeySourcePrefix+row.Source)
			if len(row.ModelScope) > 0 {
				// 空白名单不写接缝：等同「未设置」，让 publicModels 维持现状输出。
				setKeyScope(c, row.ModelScope)
			}
			if row.RateLimit > 0 {
				// 0 = 跟随插件全局（不写 override），>0 时覆盖全局（对齐源 rate_limit>0 优先）。
				setRateLimitOverride(c, &rateLimitOverride{RPM: row.RateLimit})
			}
			refreshAPIKeyLastUsed(authStore, row.ID)
			c.Next()
			return
		}

		// 全局兜底：v1 的 configs.plugin_ai_api_key（向后兼容路径）。
		if !set.APIKeySet {
			writePublicAPIError(c, &APIError{
				HTTPStatus: http.StatusServiceUnavailable,
				Type:       "upstream_unavailable",
				Code:       "api_key_not_configured",
				Message:    "网关密钥未配置（请在 AI 路由总览页的接入设置中配置 API Key）",
				Class:      ErrorClassLocal,
			})
			return
		}
		if !hasToken || !secretEqual(token, set.APIKey) {
			writePublicAPIError(c, unauthorizedAPIKeyError())
			return
		}
		// 全局 key 路径显式写回默认 source：本中间件是唯一知道「这次是谁放行的」的地方。
		setKeySource(c, publicSource)
		c.Next()
	}
}

// unauthorizedAPIKeyError「未命中凭证」的唯一 401 形状（缺失/格式错/不存在/停用/过期/不匹配共用：
// 不区分文案，避免向调用方泄露账户状态；形状与 v1 的公开面 401 逐字一致）。
func unauthorizedAPIKeyError() *APIError {
	return &APIError{
		HTTPStatus: http.StatusUnauthorized,
		Type:       "invalid_request_error",
		Code:       "invalid_api_key",
		Message:    "无效的 API Key",
		Class:      ErrorClassAuth,
	}
}

// storeUnavailableAPIError 查库面不可用（实例取不到或查询出错）⇒ 503 fail-closed。
func storeUnavailableAPIError(err error) *APIError {
	return &APIError{
		HTTPStatus: http.StatusServiceUnavailable,
		Type:       "upstream_unavailable",
		Code:       "store_unavailable",
		Message:    "服务暂不可用",
		Class:      ErrorClassLocal,
		Err:        err,
	}
}

// refreshAPIKeyLastUsed 后台异步刷 last_used_at（不阻塞响应；取舍见文件头注）。
func refreshAPIKeyLastUsed(store apiKeyAuthStore, id uint64) {
	if store == nil || id == 0 {
		return
	}
	rowID := int64(id)
	go func() {
		if _, err := store.TouchAPIKeyLastUsed(context.Background(), rowID, nowUnix()); err != nil {
			log.Printf("ai: 刷新 last_used_at 失败 api_key_id=%d: %v", rowID, err)
		}
	}()
}
