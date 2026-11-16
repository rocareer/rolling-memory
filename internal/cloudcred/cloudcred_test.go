// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cloudcred

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocareer/rolling-memory/internal/thinclient"
)

// isolateHome 把账户态与凭据文件锁进临时 HOME：端侧凭据一律从本机账户态解析，测试不得触碰真实账户。
func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func TestCredentialsRequireSignedKey(t *testing.T) {
	isolateHome(t)
	base, key := State{SignedKey: "mem2.payload.sig"}.Credentials()
	if key != "mem2.payload.sig" {
		t.Fatalf("signed key = %q", key)
	}
	if base != thinclient.DefaultBrainDataPlaneBaseURL {
		t.Fatalf("data plane base = %q, want %q", base, thinclient.DefaultBrainDataPlaneBaseURL)
	}
	if base, key := (State{}).Credentials(); base != "" || key != "" {
		t.Fatalf("state without a signed key must yield no credentials, got %q %q", base, key)
	}
	// 显式数据面基址优先于本机演示缺省（生产直连）。
	if base, _ := (State{SignedKey: "mem2.x", DataPlaneURL: "https://brain.example.com/open/memory/v1/"}).Credentials(); base != "https://brain.example.com/open/memory/v1" {
		t.Fatalf("explicit data plane base = %q", base)
	}
}

func TestFreshHonoursRenewWindow(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	cases := []struct {
		name string
		exp  int64
		want bool
	}{
		{"missing key", 0, false},
		{"expired", now.Unix() - 60, false},
		{"inside renew window", now.Add(renewWindow - time.Minute).Unix(), false},
		{"beyond renew window", now.Add(renewWindow + time.Minute).Unix(), true},
	}
	for _, tc := range cases {
		st := State{SignedKey: "mem2.payload.sig", SignedKeyExpiresAt: tc.exp}
		if got := st.Fresh(now); got != tc.want {
			t.Fatalf("%s: Fresh = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestEnsureExchangesPersistsAndReuses(t *testing.T) {
	home := isolateHome(t)
	var exchanges int64
	mux := http.NewServeMux()
	mux.HandleFunc(signKeyPath, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&exchanges, 1)
		if got := r.Header.Get("X-Api-Key"); got != "mem_account_exchange" {
			t.Errorf("exchange credential = %q, want the persisted account key", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"signed_key": "mem2.fresh.sig",
			"kid":        "kid-1",
			"expires_at": time.Now().Add(24 * time.Hour).Unix(),
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// 账户态由插件写入：除本包管理的字段外还夹带插件自有字段（必须原样保留）。
	stateFile := filePath()
	if err := os.MkdirAll(filepath.Dir(stateFile), 0o700); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	seed := `{"base_url":` + quote(srv.URL) + `,"api_key":"mem_account_exchange","namespace":"user","user":{"id":10}}`
	if err := os.WriteFile(stateFile, []byte(seed), 0o600); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	baseURL, key, err := Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if key != "mem2.fresh.sig" || baseURL != DefaultDataPlaneURL {
		t.Fatalf("credentials = %q %q", baseURL, key)
	}
	if got := atomic.LoadInt64(&exchanges); got != 1 {
		t.Fatalf("exchanges = %d, want 1", got)
	}

	// 二次解析命中有效签名钥：不再回控制面。
	if _, _, err := Ensure(context.Background()); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if got := atomic.LoadInt64(&exchanges); got != 1 {
		t.Fatalf("exchanges after reuse = %d, want 1", got)
	}

	reloaded := Load()
	if reloaded.SignedKey != "mem2.fresh.sig" || reloaded.SignedKeyKid != "kid-1" || reloaded.SignedKeyExpiresAt <= time.Now().Unix() {
		t.Fatalf("persisted state = %+v", reloaded)
	}
	raw, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("state must stay valid JSON: %v", err)
	}
	for _, kept := range []string{"api_key", "user", "namespace"} {
		if _, ok := doc[kept]; !ok {
			t.Fatalf("plugin-owned field %q was dropped from the shared account state", kept)
		}
	}

	// 端侧凭据文件 = 当前生效的签名钥（0600）。
	keyPath := filepath.Join(home, ".local", "share", "rolling-memory", keyFileName)
	keyRaw, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read credential file: %v", err)
	}
	if got := string(keyRaw); got != "mem2.fresh.sig\n" {
		t.Fatalf("credential file = %q", got)
	}
	if info, err := os.Stat(keyPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("credential file permissions = %v (err=%v)", info.Mode().Perm(), err)
	}
}

func TestEnsureWithoutCredential(t *testing.T) {
	isolateHome(t)
	_, _, err := Ensure(context.Background())
	if err == nil {
		t.Fatal("expected an error when neither a signed key nor an exchange credential exists")
	}
	if !strings.Contains(err.Error(), "凭据") {
		t.Fatalf("unexpected error: %v", err)
	}
	// 仅有过期签名钥、无续签凭据：也必须显式报错（不回退任何代理路径）。
	if saveErr := (State{SignedKey: "mem2.stale.sig", SignedKeyExpiresAt: time.Now().Add(-time.Hour).Unix()}).Save(); saveErr != nil {
		t.Fatalf("save stale state: %v", saveErr)
	}
	_, _, err = Ensure(context.Background())
	if err == nil {
		t.Fatal("expected an error for an expired signed key without an exchange credential")
	}
	if !strings.Contains(err.Error(), "重新登录") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestLiveEnsureRenewsNearExpiry 真实续签链路自检（需本机控制面 + 已登录账户态；默认 SKIP）：
// 把账户态里的签名钥到期时刻改到续签窗内，Ensure 必须向控制面换回一把新钥并落盘。
func TestLiveEnsureRenewsNearExpiry(t *testing.T) {
	if os.Getenv("ROLLING_MEMORY_LIVE_TEST") != "1" {
		t.Skip("set ROLLING_MEMORY_LIVE_TEST=1 to exercise the live control-plane exchange")
	}
	before := Load()
	if strings.TrimSpace(before.AccountKey) == "" && strings.TrimSpace(before.Token) == "" {
		t.Skip("no exchange credential on this machine; sign in from the plugin console first")
	}
	staged := before
	staged.SignedKeyExpiresAt = time.Now().Add(5 * time.Minute).Unix()
	if err := staged.Save(); err != nil {
		t.Fatalf("stage near-expiry state: %v", err)
	}
	baseURL, key, err := Ensure(context.Background())
	if err != nil {
		t.Fatalf("live renewal: %v", err)
	}
	if baseURL == "" || key == "" {
		t.Fatal("renewal must yield data-plane credentials")
	}
	after := Load()
	if after.SignedKeyExpiresAt <= staged.SignedKeyExpiresAt {
		t.Fatalf("signed key was not renewed: expires_at %d -> %d", staged.SignedKeyExpiresAt, after.SignedKeyExpiresAt)
	}
	if after.SignedKey == before.SignedKey {
		t.Fatal("renewal must mint a fresh signed key, not reuse the previous one")
	}
	t.Logf("[live renewal] kid=%s expires_at %d -> %d", after.SignedKeyKid, before.SignedKeyExpiresAt, after.SignedKeyExpiresAt)
}

func quote(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}

func TestEffectiveBlindPepperSurvivesMem2Rotation(t *testing.T) {
	isolateHome(t)
	keyA := "mem2.eyJ0aWQiOiJ1c2VyIiwidWlkIjoiMTAiLCJpYXQiOjE3MDAwMDAwMDAsImV4cCI6MTcwMDA4NjQwMH0.sig_rotation_alpha_1111"
	keyB := "mem2.eyJ0aWQiOiJ1c2VyIiwidWlkIjoiMTAiLCJpYXQiOjE3MDAwODAwMDAsImV4cCI6MTcwMDE2NjQwMH0.sig_rotation_beta_2222"

	var call int64
	mux := http.NewServeMux()
	mux.HandleFunc(signKeyPath, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&call, 1)
		key := keyA
		if n > 1 {
			key = keyB
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"signed_key":   key,
			"kid":          "kid-rot",
			"expires_at":   time.Now().Add(24 * time.Hour).Unix(),
			"blind_pepper": "rame-tenant-vault:user:10",
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	stateFile := filePath()
	if err := os.MkdirAll(filepath.Dir(stateFile), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(stateFile, []byte(`{"base_url":`+quote(srv.URL)+`,"api_key":"mem_user_test"}`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, gotKeyA, err := Ensure(context.Background()); err != nil || gotKeyA != keyA {
		t.Fatalf("first Ensure: key=%q err=%v", gotKeyA, err)
	}
	stA := Load()
	if stA.EffectiveBlindPepper() != "rame-tenant-vault:user:10" {
		t.Fatalf("stA EffectiveBlindPepper = %q", stA.EffectiveBlindPepper())
	}

	// Expire keyA and renew -> keyB; blind_pepper must stay identical.
	stA.SignedKeyExpiresAt = time.Now().Add(1 * time.Minute).Unix()
	if err := stA.Save(); err != nil {
		t.Fatalf("save expiring state: %v", err)
	}
	if _, gotKeyB, err := Ensure(context.Background()); err != nil || gotKeyB != keyB {
		t.Fatalf("second Ensure: key=%q err=%v", gotKeyB, err)
	}
	stB := Load()
	if stB.SignedKey == stA.SignedKey {
		t.Fatal("signed_key did not rotate")
	}
	if stB.EffectiveBlindPepper() != stA.EffectiveBlindPepper() || stB.EffectiveBlindPepper() != "rame-tenant-vault:user:10" {
		t.Fatalf("blind_pepper drifted across mem2 rotation: %q -> %q", stA.EffectiveBlindPepper(), stB.EffectiveBlindPepper())
	}
}
