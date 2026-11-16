// plugin: rolling-memory v0.0.58
// cloud_account.go 移植 BuildAdmin 插件市场 `baAccount` 机制，为 Rolling-Memory 插件控制台提供：
//  1. 弹窗一键登录远端云服务（默认标记来源 `Rolling-Memory-Plugin`）；云端模式采用申请制，
//     在线注册已关闭（`/cloud/account/register` fail-closed，返回联系邮箱）；
//  2. 自动接收远端签发的登录 Token 与数据面直连签名钥（`mem2.*`），并以 0600 权限落盘至
//     `~/.config/rolling-memory/cloud-account.json` 与 `~/.local/share/rolling-memory/cloud.key`；
//     账号钥（登录/注册时一并签发）只作换取签名钥的凭据，不作记忆面密钥、不注入子进程；
//  3. 弹窗内一键管理 API Key（查看掩码 / 一键轮换刷新明文 / 多设备 Key 列表与新增/停用）；
//  4. 弹窗内直接查看和管理云端记忆（概览统计 / 近期记忆单元 / 云端在线检索 / 云端在线写入 / 冲突诊断）。
//
// 记忆面唯一合法凭据 = 数据面签名钥（`mem2.*`）：控制面签发、数据面离线验签，只在数据面公开基址生效；
// 控制面代理路径（旧 `mem_*` 用户密钥）已停用，本模块不再内置任何演示密钥，也不回退代理路径。
package rollingmemory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/kit/httpx"
	"github.com/ai-go-hub/ai-go-admin/internal/middleware"
	"github.com/gin-gonic/gin"
)

const (
	defaultCloudPort      = "8081"
	defaultCloudServerURL = "http://127.0.0.1:" + defaultCloudPort
	// defaultDataPlaneURL 本地演示数据面基址（须自带 /open/memory/v1）；账户态未显式配置时用它。
	defaultDataPlaneURL      = "http://127.0.0.1:8082/open/memory/v1"
	defaultUpstreamDevUserID = "10"
	// signedKeyRenewWindow 签名钥剩余有效期低于该阈值即提前续签（签名钥缺省 TTL 24h）。
	signedKeyRenewWindow = 30 * time.Minute
)

// CloudAccountState 本地持久化的云端账户绑定状态（0600 权限存储在 ~/.config/rolling-memory/cloud-account.json）。
type CloudAccountState struct {
	BaseURL string `json:"base_url"`
	Token   string `json:"token,omitempty"`
	// APIKey 账号钥（登录 / 注册 / 轮换时由控制面签发）：仅用于向控制面换取数据面签名钥，
	// 不作记忆面密钥、不注入子进程；记忆面唯一凭据是下方的 SignedKey。
	APIKey    string         `json:"api_key,omitempty"`
	KeyMask   string         `json:"key_mask,omitempty"`
	Namespace string         `json:"namespace,omitempty"`
	User      map[string]any `json:"user,omitempty"`
	MemoryKey map[string]any `json:"memory_key,omitempty"`
	Endpoints map[string]any `json:"endpoints,omitempty"`
	UpdatedAt string         `json:"updated_at,omitempty"`
	// SignedKey 数据面直连签名钥（`mem2.*`，由控制面 `/open/memory/v1/auth/sign-key` 签发）：
	// 端侧唯一记忆面凭据，只在数据面公开基址生效（数据面离线验签，不回连控制面）。
	// 控制面代理路径（旧 `mem_*` 钥）已停用，故签名钥缺失 / 过期即等同「未配置云端记忆」。
	SignedKey          string `json:"signed_key,omitempty"`
	SignedKeyKid       string `json:"signed_key_kid,omitempty"`
	SignedKeyExpiresAt int64  `json:"signed_key_expires_at,omitempty"`
	// DataPlaneURL 公开数据面基址（如 `https://brain.example.com/open/memory/v1`）；
	// 空则落到本机演示数据面（defaultDataPlaneURL）。
	DataPlaneURL string `json:"data_plane_url,omitempty"`
}

func mountCloudAccountRoutes(g *gin.RouterGroup) {
	ca := g.Group("/cloud/account", middleware.AdminAuth(), middleware.AdminPermission())
	ca.POST("/state", cloudAccountStateHandler)
	ca.POST("/register", cloudAccountRegisterHandler)
	ca.POST("/login", cloudAccountLoginHandler)
	ca.POST("/logout", cloudAccountLogoutHandler)
	ca.POST("/rotate-key", cloudAccountRotateKeyHandler)
	ca.POST("/sign-key", cloudAccountSignKeyHandler)
	ca.POST("/devices", cloudAccountDevicesHandler)
	ca.POST("/memory", cloudAccountMemoryHandler)
}

func cloudAccountFilePath() string {
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "."
	}
	return filepath.Join(home, ".config", "rolling-memory", "cloud-account.json")
}

func cloudKeyFilePath() string {
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "."
	}
	return filepath.Join(home, ".local", "share", "rolling-memory", "cloud.key")
}

func loadCloudAccountState() CloudAccountState {
	st := CloudAccountState{
		BaseURL:   defaultCloudServerURL,
		Namespace: defaultUpstreamDevUserID,
	}
	raw, err := os.ReadFile(cloudAccountFilePath())
	if err != nil {
		return st
	}
	// 文件是本地唯一凭据来源：登出后的空 api_key 必须保持为空，防止 Background 在用户登出后
	// 仍向旧账户上传文档盲化脉冲；本插件不内置任何演示密钥。
	st = CloudAccountState{}
	if err := json.Unmarshal(raw, &st); err != nil {
		return st
	}
	if strings.TrimSpace(st.BaseURL) == "" {
		st.BaseURL = defaultCloudServerURL
	}
	if strings.TrimSpace(st.Namespace) == "" {
		st.Namespace = defaultUpstreamDevUserID
	}
	if strings.TrimSpace(st.KeyMask) == "" && strings.TrimSpace(st.APIKey) != "" {
		st.KeyMask = maskCloudKey(st.APIKey)
	}
	return st
}

func saveCloudAccountState(st CloudAccountState) error {
	if strings.TrimSpace(st.BaseURL) == "" {
		st.BaseURL = defaultCloudServerURL
	}
	st.UpdatedAt = time.Now().Format(time.RFC3339)
	p := cloudAccountFilePath()
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := writeConsoleFileAtomic(p, raw, 0o600); err != nil {
		return err
	}
	if key := strings.TrimSpace(st.SignedKey); key != "" {
		kp := cloudKeyFilePath()
		_ = writeConsoleFileAtomic(kp, []byte(key+"\n"), 0o600)
	}
	return nil
}

func maskCloudKey(k string) string {
	k = strings.TrimSpace(k)
	if k == "" {
		return ""
	}
	if len(k) <= 12 {
		// 短钥只回显首字符，绝不越界切片。
		if len(k) <= 1 {
			return k + "…"
		}
		return k[:1] + "…"
	}
	return k[:8] + "…" + k[len(k)-4:]
}

func callUpstreamJSON(ctx context.Context, baseURL, path, bearerToken, apiKey string, payload any) (map[string]any, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = defaultCloudServerURL
	}
	var bodyReader io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(raw)
	} else {
		bodyReader = strings.NewReader("{}")
	}
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, baseURL+path, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Rolling-Memory-Plugin/"+liveSoftwareVersion())
	if strings.TrimSpace(bearerToken) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(bearerToken))
	}
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("X-Api-Key", strings.TrimSpace(apiKey))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连接云端服务 (%s) 失败: %w", baseURL, err)
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var parsed map[string]any
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		return nil, fmt.Errorf("云端响应解析失败 (HTTP %d)", resp.StatusCode)
	}
	if code, ok := parsed["code"].(float64); ok && code != 0 {
		msg, _ := parsed["message"].(string)
		if msg == "" {
			msg = "云端请求失败"
		}
		return nil, fmt.Errorf("%s", msg)
	}
	data, _ := parsed["data"].(map[string]any)
	if data == nil {
		data = parsed
	}
	return data, nil
}

// requireCloudMode 是云端账户接口的本地模式硬闸：本地模式零云端请求，账户面一律 fail-closed
// （唯一例外是只读本地账户文件的 state 快照，见 cloudAccountStateHandler）。
func requireCloudMode(c *gin.Context) bool {
	if isLocalModeForHome(userHome()) {
		httpx.Fail(c,
			httpx.WithCode(http.StatusBadRequest),
			httpx.WithMessage("本地模式已停用云端账户接口（零云端请求）。如需云端能力，请先用 `rolling-memory mode cloud` 切换运行模式。"),
		)
		return false
	}
	return true
}

func userHome() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "."
	}
	return home
}

// cloudAccountStateHandler 返回本地保存的云端账户状态（不含登录 Token / 账号钥原文；一律只回掩码）。
func cloudAccountStateHandler(c *gin.Context) {
	st := loadCloudAccountState()
	localMode := isLocalModeForHome(userHome())
	var renewErr error
	if !localMode {
		// 签名钥是短 TTL 凭据：读取状态时顺带续签；失败不阻断回执，由 has_signed_key / signed_key_error 如实反映。
		// 本地模式不续签（零外呼），由 mode/remote_enabled 字段如实告知账户面未启用。
		renewErr = ensureSignedKey(c.Request.Context(), &st)
	}
	mask := st.KeyMask
	if mask == "" && st.APIKey != "" {
		mask = maskCloudKey(st.APIKey)
	}
	payload := gin.H{
		"mode":           rollingModeForHome(userHome()),
		"remote_enabled": !localMode,
		"logged_in":      st.Token != "" || st.APIKey != "",
		"base_url":       st.BaseURL,
		"namespace":      st.Namespace,
		"key_mask":       mask,
		"has_api_key":    st.APIKey != "",
		"key_file":       cloudKeyFilePath(),
		"user":           st.User,
		"memory_key":     st.MemoryKey,
		"endpoints":      st.Endpoints,
		"updated_at":     st.UpdatedAt,
		// 记忆面凭据（数据面直连签名钥）回执：不返回明文，明文只用于子进程注入与本地金库直连。
		"has_signed_key":        st.SignedKey != "",
		"signed_key_kid":        st.SignedKeyKid,
		"signed_key_mask":       maskCloudKey(st.SignedKey),
		"signed_key_expires_at": st.SignedKeyExpiresAt,
		"data_plane_url":        st.DataPlaneURL,
	}
	if renewErr != nil {
		payload["signed_key_error"] = renewErr.Error()
	}
	httpx.Success(c, httpx.WithData(payload))
}

type cloudAuthReq struct {
	BaseURL   string `json:"base_url"`
	Username  string `json:"username"`
	Password  string `json:"password"`
	RotateKey bool   `json:"rotate_key"`
}

// validateCloudEndpointURL 校验云端控制面/数据面 URL：远端主机必须使用 https://，
// http:// 仅允许本机回环地址，防止凭据通过明文传输或向非 HTTP 协议/内网主机发起 SSRF。
func validateCloudEndpointURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || strings.TrimSpace(parsed.Hostname()) == "" {
		return fmt.Errorf("URL 格式无效")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		return nil
	case "http":
		if isStrictLoopbackHost(parsed.Hostname()) {
			return nil
		}
		return fmt.Errorf("非本机回环地址必须使用 https://")
	default:
		return fmt.Errorf("仅支持 https://（或本机回环 http://）")
	}
}

// cloudAccountRegisterHandler 关闭在线注册：云端模式采用申请制、逐步开放试用；
// 本端点 fail-closed 返回联系邮箱，不再调用远端 plugin-register。
func cloudAccountRegisterHandler(c *gin.Context) {
	httpx.Fail(c,
		httpx.WithCode(http.StatusForbidden),
		httpx.WithMessage("云端模式采用申请制，当前逐步开放试用；请发送申请邮件至 pick@psychologylake.org.cn（支持：support@psychologylake.org.cn），开通后使用已发放账号登录。"),
	)
}

// cloudAccountLoginHandler 弹窗一键登录远端云服务账号（支持 rotate_key=true 同步获取新明文 API Key）。
func cloudAccountLoginHandler(c *gin.Context) {
	var req cloudAuthReq
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Fail(c, httpx.WithMessage("参数错误: "+err.Error()))
		return
	}
	if !requireCloudMode(c) {
		return
	}
	st := loadCloudAccountState()
	if strings.TrimSpace(req.BaseURL) != "" {
		trimmedBase := strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
		if err := validateCloudEndpointURL(trimmedBase); err != nil {
			httpx.Fail(c, httpx.WithMessage("非法云端基址: "+err.Error()))
			return
		}
		st.BaseURL = trimmedBase
	}
	data, err := callUpstreamJSON(c.Request.Context(), st.BaseURL, "/user/auth/plugin-login", "", "", map[string]any{
		"username":   strings.TrimSpace(req.Username),
		"password":   req.Password,
		"rotate_key": req.RotateKey,
		"keep":       true,
	})
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	applyUpstreamAuthData(&st, data)
	signedKeyErr := refreshSignedKey(c.Request.Context(), &st)
	_ = saveCloudAccountState(st)
	payload := gin.H{
		"logged_in":   true,
		"base_url":    st.BaseURL,
		"namespace":   st.Namespace,
		"api_key":     asStr(data["api_key"]),
		"key_mask":    st.KeyMask,
		"has_api_key": st.APIKey != "",
		"key_file":    cloudKeyFilePath(),
		"user":        st.User,
		"memory_key":  st.MemoryKey,
		"devices":     data["devices"],
		"endpoints":   st.Endpoints,
	}
	if signedKeyErr != nil {
		payload["signed_key_error"] = signedKeyErr.Error()
	}
	httpx.Success(c, httpx.WithData(payload))
}

// refreshSignedKey 向控制面换取数据面直连签名钥（`mem2.*`）并写入账户状态。
// 签名钥是端侧唯一记忆面凭据，故换取失败必须显式上报（不再静默回退控制面代理路径）；
// 失败时既有签名钥仅在仍在有效期内才保留，过期即清空，避免端侧继续使用过期钥。
func refreshSignedKey(ctx context.Context, st *CloudAccountState) error {
	if st == nil {
		return fmt.Errorf("账户状态不可用")
	}
	if strings.TrimSpace(st.APIKey) == "" && strings.TrimSpace(st.Token) == "" {
		return fmt.Errorf("缺少账号凭据：请先登录云端账户")
	}
	data, err := callUpstreamJSON(ctx, st.BaseURL, "/open/memory/v1/auth/sign-key", st.Token, st.APIKey, map[string]any{})
	if err != nil {
		if !signedKeyFresh(*st) {
			st.SignedKey, st.SignedKeyKid, st.SignedKeyExpiresAt = "", "", 0
		}
		return err
	}
	key := asStr(data["signed_key"])
	if key == "" {
		if !signedKeyFresh(*st) {
			st.SignedKey, st.SignedKeyKid, st.SignedKeyExpiresAt = "", "", 0
		}
		return fmt.Errorf("控制面未返回签名钥：请确认控制面已启用数据面鉴权签发")
	}
	st.SignedKey = key
	st.SignedKeyKid = asStr(data["kid"])
	st.SignedKeyExpiresAt = int64(asFloat(data["expires_at"]))
	return nil
}

// signedKeyFresh 报告既有签名钥是否仍在有效期内（含续签宽限窗）。
func signedKeyFresh(st CloudAccountState) bool {
	if strings.TrimSpace(st.SignedKey) == "" || st.SignedKeyExpiresAt <= 0 {
		return false
	}
	return time.Now().Add(signedKeyRenewWindow).Unix() < st.SignedKeyExpiresAt
}

// ensureSignedKey 确保账户态持有可用签名钥：缺失 / 临期即用账号凭据续签并落盘；仍有效则不动。
func ensureSignedKey(ctx context.Context, st *CloudAccountState) error {
	if st == nil || signedKeyFresh(*st) {
		return nil
	}
	if err := refreshSignedKey(ctx, st); err != nil {
		return err
	}
	return saveCloudAccountState(*st)
}

// cloudAccountSignKeyHandler 手动刷新数据面直连签名钥（UI/CLI 可随时调用；失败显式报错）。
func cloudAccountSignKeyHandler(c *gin.Context) {
	var req struct {
		DataPlaneURL string `json:"data_plane_url"`
	}
	_ = c.ShouldBindJSON(&req)
	if !requireCloudMode(c) {
		return
	}
	st := loadCloudAccountState()
	if dp := strings.TrimRight(strings.TrimSpace(req.DataPlaneURL), "/"); dp != "" {
		if err := validateCloudEndpointURL(dp); err != nil {
			httpx.Fail(c, httpx.WithMessage("非法数据面基址: "+err.Error()))
			return
		}
		st.DataPlaneURL = dp
	}
	if err := refreshSignedKey(c.Request.Context(), &st); err != nil {
		_ = saveCloudAccountState(st)
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	_ = saveCloudAccountState(st)
	httpx.Success(c, httpx.WithData(gin.H{
		"has_signed_key":  st.SignedKey != "",
		"signed_key_kid":  st.SignedKeyKid,
		"signed_key_mask": maskCloudKey(st.SignedKey),
		"expires_at":      st.SignedKeyExpiresAt,
		"data_plane_url":  st.DataPlaneURL,
	}))
}

func applyUpstreamAuthData(st *CloudAccountState, data map[string]any) {
	if tok := asStr(data["token"]); tok != "" {
		st.Token = tok
	}
	if ns := asStr(data["namespace"]); ns != "" {
		st.Namespace = ns
	}
	if u, ok := data["user"].(map[string]any); ok {
		st.User = u
		if st.Namespace == "" {
			st.Namespace = strings.ToLower(asStr(u["username"]))
		}
	}
	if mk, ok := data["memory_key"].(map[string]any); ok {
		st.MemoryKey = mk
		if m := asStr(mk["mask"]); m != "" {
			st.KeyMask = m
		}
	}
	if plain := asStr(data["api_key"]); plain != "" {
		st.APIKey = plain
		st.KeyMask = maskCloudKey(plain)
	}
	if eps, ok := data["endpoints"].(map[string]any); ok {
		st.Endpoints = eps
	}
}

// cloudAccountLogoutHandler 退出云端账号登录（清理本地 Token，保留 BaseURL）。
func cloudAccountLogoutHandler(c *gin.Context) {
	if !requireCloudMode(c) {
		return
	}
	st := loadCloudAccountState()
	if st.Token != "" {
		_, _ = callUpstreamJSON(c.Request.Context(), st.BaseURL, "/user/auth/logout", st.Token, "", nil)
	}
	clean := CloudAccountState{BaseURL: st.BaseURL}
	_ = saveCloudAccountState(clean)
	httpx.Success(c, httpx.WithData(gin.H{"logged_in": false, "base_url": clean.BaseURL}))
}

// cloudAccountRotateKeyHandler 轮换默认记忆 API Key 并自动将新明文写入本机 cloud.key。
func cloudAccountRotateKeyHandler(c *gin.Context) {
	if !requireCloudMode(c) {
		return
	}
	st := loadCloudAccountState()
	if st.Token == "" {
		httpx.Fail(c, httpx.WithMessage("请先登录云端账户后再轮换 API Key"))
		return
	}
	data, err := callUpstreamJSON(c.Request.Context(), st.BaseURL, "/user/memory-key/rotate", st.Token, "", nil)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	if plain := asStr(data["key"]); plain != "" {
		st.APIKey = plain
	}
	if mask := asStr(data["mask"]); mask != "" {
		st.KeyMask = mask
	}
	refreshErr := refreshSignedKey(c.Request.Context(), &st)
	_ = saveCloudAccountState(st)
	payload := gin.H{
		"api_key":  st.APIKey,
		"key_mask": st.KeyMask,
		"key_file": cloudKeyFilePath(),
		"raw":      data,
	}
	if refreshErr != nil {
		payload["signed_key_error"] = refreshErr.Error()
	}
	httpx.Success(c, httpx.WithData(payload))
}

type cloudDevicesReq struct {
	Action string `json:"action"` // list | create | rotate | disable
	ID     uint64 `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
}

// cloudAccountDevicesHandler 管理多设备 API Key（list / create / rotate / disable）。
func cloudAccountDevicesHandler(c *gin.Context) {
	if !requireCloudMode(c) {
		return
	}
	st := loadCloudAccountState()
	if st.Token == "" {
		httpx.Fail(c, httpx.WithMessage("请先登录云端账户"))
		return
	}
	var req cloudDevicesReq
	_ = c.ShouldBindJSON(&req)
	action := strings.ToLower(strings.TrimSpace(req.Action))
	if action == "" {
		action = "list"
	}
	pathMap := map[string]string{
		"list":    "/user/devices/list",
		"create":  "/user/devices/create",
		"rotate":  "/user/devices/rotate",
		"disable": "/user/devices/disable",
	}
	targetPath, ok := pathMap[action]
	if !ok {
		httpx.Fail(c, httpx.WithMessage("不支持的设备操作: "+action))
		return
	}
	data, err := callUpstreamJSON(c.Request.Context(), st.BaseURL, targetPath, st.Token, "", map[string]any{
		"id":   req.ID,
		"name": req.Name,
		"kind": req.Kind,
	})
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	httpx.Success(c, httpx.WithData(data))
}

type cloudMemoryManageReq struct {
	Action       string `json:"action"` // status | recent | recall | remember | conflicts | forget | delete
	SubNamespace string `json:"sub_namespace"`
	ID           string `json:"id,omitempty"`
	NodeID       string `json:"node_id,omitempty"`
	Reason       string `json:"reason,omitempty"`
	Limit        int    `json:"limit"`
	Cue          string `json:"cue"`
	TopK         int    `json:"top_k"`
	Tier         string `json:"tier"`
	Subject      string `json:"subject"`
	Rel          string `json:"rel"`
	Object       string `json:"object"`
	ObjectLit    string `json:"object_literal"`
	Content      string `json:"content"`
	ClosesPrior  bool   `json:"closes_prior"`
}

// cloudAccountMemoryHandler 代理调用远端 `/user/memory/{status,recent,recall,remember,conflicts,forget}` 云端记忆管理接口。
func cloudAccountMemoryHandler(c *gin.Context) {
	if !requireCloudMode(c) {
		return
	}
	st := loadCloudAccountState()
	if st.Token == "" && st.APIKey == "" {
		httpx.Fail(c, httpx.WithMessage("请先登录云端账户或配置 API Key"))
		return
	}
	var req cloudMemoryManageReq
	_ = c.ShouldBindJSON(&req)
	action := strings.ToLower(strings.TrimSpace(req.Action))
	if action == "" {
		action = "status"
	}
	pathMap := map[string]string{
		"status":    "/user/memory/status",
		"recent":    "/user/memory/recent",
		"recall":    "/user/memory/recall",
		"remember":  "/user/memory/remember",
		"conflicts": "/user/memory/conflicts",
		"forget":    "/user/memory/forget",
		"delete":    "/user/memory/forget",
	}
	targetPath, ok := pathMap[action]
	if !ok {
		httpx.Fail(c, httpx.WithMessage("不支持的记忆管理操作: "+action))
		return
	}
	data, err := callUpstreamJSON(c.Request.Context(), st.BaseURL, targetPath, st.Token, st.APIKey, req)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	httpx.Success(c, httpx.WithData(data))
}

func asStr(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

// accountNumericUserID extracts the canonical numeric user id (upstream `users.id`)
// from the account state. The account namespace is the TENANT name and must never
// be used as a user id: composing it as one folds the engine scope onto
// `ast:default` (no incarnation) instead of the canonical `ast:default:inc:<uid>`.
func accountNumericUserID(st CloudAccountState) string {
	raw, ok := st.User["id"]
	if !ok || raw == nil {
		return ""
	}
	switch v := raw.(type) {
	case float64:
		if v <= 0 {
			return ""
		}
		return strconv.FormatInt(int64(v), 10)
	case json.Number:
		return strings.TrimSpace(v.String())
	default:
		text := strings.TrimSpace(asStr(raw))
		if text == "" || text == "0" {
			return ""
		}
		return text
	}
}

// resolveBrainEndpoint 解析子进程应使用的记忆端点与凭据。
// 端侧唯一合法凭据 = 数据面直连签名钥（`mem2.*`）：命中即返回「数据面基址 + 签名钥」；
// 签名钥缺失 / 过期即返回空（调用方显式报错）—— 控制面代理路径（旧 `mem_*` 钥）已停用，
// 签名钥只对数据面公开基址有效，故这里不做任何代理回退。
func resolveBrainEndpoint(account CloudAccountState) (baseURL, apiKey string) {
	apiKey = strings.TrimSpace(account.SignedKey)
	if apiKey == "" {
		return "", ""
	}
	baseURL = strings.TrimRight(strings.TrimSpace(account.DataPlaneURL), "/")
	if baseURL == "" {
		baseURL = defaultDataPlaneURL
	}
	return baseURL, apiKey
}
