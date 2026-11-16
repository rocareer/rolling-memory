// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package cloudcred 解析端侧云端记忆凭据 —— 数据面直连签名钥（`mem2.*`）。
//
// 端侧唯一合法的记忆面凭据 = 数据面签名钥：由云端控制面签发（Ed25519），数据面离线验签，
// 只在数据面公开基址生效；控制面代理路径（旧 `mem_*` 用户密钥）已停用，客户端不再持有任何
// 内置（baked）密钥。签名钥为短 TTL（缺省 24h），故本包在读取账户态时按剩余有效期自动续签；
// 续签凭据 = 登录时一并落盘的账号钥（仅用于换取签名钥，不作记忆面密钥）。
//
// 账户态与后台插件共用同一份 0600 文件（`~/.config/rolling-memory/cloud-account.json`）：
// 本包只增删自己管理的字段，其余字段（用户资料 / 端点清单 / 掩码视图等）原样保留，避免
// 与插件写入互相覆盖。
package cloudcred

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rocareer/rolling-memory/internal/thinclient"
	"github.com/rocareer/rolling-memory/internal/version"
	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

const (
	// DefaultServerURL 本地演示控制面基址（账号注册 / 登录 / 换取签名钥）。
	DefaultServerURL = "http://127.0.0.1:" + thinclient.DefaultUpstreamPort
	// DefaultDataPlaneURL 本地演示数据面基址（须自带 /open/memory/v1）。
	DefaultDataPlaneURL = thinclient.DefaultBrainDataPlaneBaseURL
	// renewWindow 签名钥剩余有效期低于该阈值即提前续签，避免踩在过期边界上。
	renewWindow = 30 * time.Minute
	// signKeyPath 换取签名钥的控制面端点（与 /open/memory/v1/* 同族）。
	signKeyPath = "/open/memory/v1/auth/sign-key"
	// keyFileName 端侧记忆凭据文件（内容 = 当前生效的签名钥明文）。
	keyFileName = "cloud.key"
)

// State 端侧云端账户态（与后台插件共用同一文件；此处只声明端侧解析需要的字段）。
type State struct {
	BaseURL            string
	Token              string
	AccountKey         string
	Namespace          string
	SignedKey          string
	SignedKeyKid       string
	SignedKeyExpiresAt int64
	DataPlaneURL       string
	BlindPepper        string
}

// EffectiveBlindPepper returns the rotation-invariant tenant blind pepper for local vault hashing:
// explicit server-issued `blind_pepper` if present, otherwise derived from `mem2.*` claims (`rame-tenant-vault:<tid>:<uid>`).
func (s State) EffectiveBlindPepper() string {
	if p := strings.TrimSpace(s.BlindPepper); p != "" {
		return p
	}
	if key := strings.TrimSpace(s.SignedKey); key != "" {
		return string(ramesdk.DeriveBlindPepper([]byte(key)))
	}
	if ns := strings.TrimSpace(s.Namespace); ns != "" {
		return "rame-tenant-vault:" + ns + ":"
	}
	return ""
}

// Load 读取账户态；文件缺失或损坏时返回零值状态（不视为错误：端侧可退回本地金库模式）。
func Load() State {
	raw, err := os.ReadFile(filePath())
	if err != nil {
		return State{}
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return State{}
	}
	st := State{
		BaseURL:            asString(doc["base_url"]),
		Token:              asString(doc["token"]),
		AccountKey:         asString(doc["api_key"]),
		Namespace:          asString(doc["namespace"]),
		SignedKey:          asString(doc["signed_key"]),
		SignedKeyKid:       asString(doc["signed_key_kid"]),
		SignedKeyExpiresAt: int64(asFloat(doc["signed_key_expires_at"])),
		DataPlaneURL:       strings.TrimRight(asString(doc["data_plane_url"]), "/"),
		BlindPepper:        asString(doc["blind_pepper"]),
	}
	if st.BlindPepper == "" && st.SignedKey != "" {
		derived := string(ramesdk.DeriveBlindPepper([]byte(st.SignedKey)))
		if derived != st.SignedKey {
			st.BlindPepper = derived
		}
	}
	if st.BaseURL == "" {
		st.BaseURL = DefaultServerURL
	}
	return st
}

// Ensure 返回可直接使用的数据面凭据；签名钥缺失 / 临期时用账号凭据静默续签。
// 无可续签凭据且无有效签名钥时返回错误（端侧不再回退控制面代理路径）。
func Ensure(ctx context.Context) (baseURL, apiKey string, err error) {
	st := Load()
	if !st.Fresh(time.Now()) {
		if strings.TrimSpace(st.AccountKey) == "" && strings.TrimSpace(st.Token) == "" {
			if st.SignedKey == "" {
				return "", "", fmt.Errorf("云端记忆凭据未配置：请先在插件控制台登录云端账户以获取数据面签名钥")
			}
			return "", "", fmt.Errorf("云端记忆签名钥已过期，且本机无可用账号凭据续签：请重新登录云端账户")
		}
		st, err = st.Refresh(ctx)
		if err != nil {
			return "", "", err
		}
	}
	base, key := st.Credentials()
	return base, key, nil
}

// Fresh 报告签名钥是否仍在有效期内（含续签宽限窗）。
func (s State) Fresh(now time.Time) bool {
	if strings.TrimSpace(s.SignedKey) == "" || s.SignedKeyExpiresAt <= 0 {
		return false
	}
	return now.Add(renewWindow).Unix() < s.SignedKeyExpiresAt
}

// Credentials 返回端侧应使用的数据面基址与签名钥；缺一即视为不可用。
func (s State) Credentials() (baseURL, apiKey string) {
	key := strings.TrimSpace(s.SignedKey)
	if key == "" {
		return "", ""
	}
	baseURL = strings.TrimRight(strings.TrimSpace(s.DataPlaneURL), "/")
	if baseURL == "" {
		baseURL = DefaultDataPlaneURL
	}
	return baseURL, key
}

// Refresh 以账号凭据（已持有账号钥 / 登录 Token）换取新的数据面签名钥并落盘。
func (s State) Refresh(ctx context.Context) (State, error) {
	base := strings.TrimRight(strings.TrimSpace(s.BaseURL), "/")
	if base == "" {
		base = DefaultServerURL
	}
	data, err := callJSON(ctx, base, signKeyPath, s.Token, s.AccountKey, map[string]any{})
	if err != nil {
		return s, err
	}
	key := asString(data["signed_key"])
	if key == "" {
		return s, fmt.Errorf("控制面未返回签名钥（请确认控制面已启用数据面鉴权签发）")
	}
	s.SignedKey = key
	s.SignedKeyKid = asString(data["kid"])
	s.SignedKeyExpiresAt = int64(asFloat(data["expires_at"]))
	if bp := asString(data["blind_pepper"]); bp != "" {
		s.BlindPepper = bp
	} else if derived := string(ramesdk.DeriveBlindPepper([]byte(key))); derived != key {
		s.BlindPepper = derived
	}
	if dp := strings.TrimRight(asString(data["data_plane_url"]), "/"); dp != "" {
		s.DataPlaneURL = dp
	}
	if err := s.Save(); err != nil {
		return s, err
	}
	return s, nil
}

// Save 合并写回账户态（只覆盖本包管理的字段，保留插件写入的用户资料 / 端点清单等）。
func (s State) Save() error {
	p := filePath()
	doc := map[string]any{}
	if raw, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(raw, &doc)
	}
	setOrClear(doc, "base_url", s.BaseURL)
	setOrClear(doc, "signed_key", s.SignedKey)
	setOrClear(doc, "signed_key_kid", s.SignedKeyKid)
	setOrClear(doc, "data_plane_url", s.DataPlaneURL)
	setOrClear(doc, "blind_pepper", s.BlindPepper)
	if s.SignedKeyExpiresAt > 0 {
		doc["signed_key_expires_at"] = s.SignedKeyExpiresAt
	} else {
		delete(doc, "signed_key_expires_at")
	}
	doc["updated_at"] = time.Now().Format(time.RFC3339)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		return err
	}
	return s.writeKeyFile()
}

// writeKeyFile 把当前生效的签名钥写入端侧凭据文件（cloud.key），供 UI/CLI 直接引用。
func (s State) writeKeyFile() error {
	key := strings.TrimSpace(s.SignedKey)
	if key == "" {
		return nil
	}
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "."
	}
	p := filepath.Join(home, ".local", "share", "rolling-memory", keyFileName)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(key+"\n"), 0o600)
}

func filePath() string {
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "."
	}
	return filepath.Join(home, ".config", "rolling-memory", "cloud-account.json")
}

func setOrClear(doc map[string]any, key, value string) {
	if value = strings.TrimSpace(value); value != "" {
		doc[key] = value
		return
	}
	delete(doc, key)
}

// callJSON 调用控制面 JSON 端点（与插件同口径：Authorization Bearer / X-Api-Key 双携带）。
func callJSON(ctx context.Context, baseURL, path, bearerToken, apiKey string, payload any) (map[string]any, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, strings.TrimRight(baseURL, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Rolling-Memory/"+version.Version)
	if token := strings.TrimSpace(bearerToken); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if key := strings.TrimSpace(apiKey); key != "" {
		req.Header.Set("X-Api-Key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连接云端服务 (%s) 失败: %w", baseURL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("云端响应解析失败 (HTTP %d)", resp.StatusCode)
	}
	if code, ok := parsed["code"].(float64); ok && code != 0 {
		msg := asString(parsed["message"])
		if msg == "" {
			msg = "云端请求失败"
		}
		return nil, fmt.Errorf("%s", msg)
	}
	if !respSuccessful(resp) {
		if msg := asString(parsed["error"]); msg != "" {
			return nil, fmt.Errorf("%s", msg)
		}
		if msg := asString(parsed["message"]); msg != "" {
			return nil, fmt.Errorf("%s", msg)
		}
		return nil, fmt.Errorf("云端请求失败 (HTTP %d)", resp.StatusCode)
	}
	if data, ok := parsed["data"].(map[string]any); ok {
		return data, nil
	}
	return parsed, nil
}

func respSuccessful(resp *http.Response) bool {
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

func asString(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func asFloat(v any) float64 {
	n, _ := v.(float64)
	return n
}
