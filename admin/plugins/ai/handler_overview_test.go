// plugin: ai v0.2.0
// handler_overview_test.go 承载总览/设置面**可执行**的判据（独立评审 B8：settings/save 的验收拆两层）。
//
// 第一层（本文件，可执行）：settingsSaveRequest.updates() 的纯函数断言——api_key 三态
// （缺省=不修改 / ""=显式清空 / 非空=设置）与最小长度（≥16 字符；短于 16 一律拒收并给出
// 具体文案，防公开面 Bearer 被撞库），以及其余键的边界。
//
// 第二层（需 install 放行，不在本文件）：真实例上 POST /admin/plugin/ai/settings/save 的
// code=1/code=0、库值不变/掩码回显。该 handler 走**全局库句柄** database.DB()
// （handler_overview.go 的 updatePluginSettings），现有 fake-deps 用例（deps 注入）与
// DSN 门控真库用例（只碰 store 层表）都覆盖不到 ⇒ 如实登记为 V2/V3 前置。
//
// 另两组本包出口（同为本文件的判据）：驱动侧 ChatResult 截断字段传播（累积上限：
// 伪上游 + 真驱动）；newGuardedHTTPClient 的 allow_private 双向用例与「不跟随重定向」
// （B3 护栏工厂前提）。
//
// helper 命名不与 handler_test.go/sse_test.go 重名（本文件自带 settingsStrPtr / settingsIntPtr）。
package ai

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func settingsStrPtr(value string) *string { return &value }

func settingsIntPtr(value int) *int { return &value }

// ===== 第一层：settingsSaveRequest.updates()（纯函数，无库）=====

func TestSettingsSaveAPIKeyMinimumLength(t *testing.T) {
	// 非空但短于 16：拒收，且文案给出具体口径（当前 N 个）。
	short := "sk-shortkey"
	_, err := (settingsSaveRequest{APIKey: settingsStrPtr(short)}).updates()
	if err == nil {
		t.Fatal("非空 api_key 短于 16 字符必须被拒")
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("%d", minAPIKeyRunes)) ||
		!strings.Contains(err.Error(), fmt.Sprintf("当前 %d 个", len([]rune(short)))) {
		t.Fatalf("拒收文案要给出最小长度与实际长度，实际 %q", err.Error())
	}

	// 恰好 16：接收（边界闭区间）。
	exact := strings.Repeat("k", minAPIKeyRunes)
	updates, err := (settingsSaveRequest{APIKey: settingsStrPtr(exact)}).updates()
	if err != nil || updates[configKeyAPIKey] != exact {
		t.Fatalf("16 字符必须接收: %+v err=%v", updates, err)
	}

	// 15：拒收（边界外）。
	_, err = (settingsSaveRequest{APIKey: settingsStrPtr(strings.Repeat("k", minAPIKeyRunes-1))}).updates()
	if err == nil {
		t.Fatal("15 字符必须被拒")
	}

	// 长度按**去空白后**的值判：两侧空白不算密钥长度。
	padded := "  " + strings.Repeat("k", minAPIKeyRunes-1) + "  "
	if _, err := (settingsSaveRequest{APIKey: settingsStrPtr(padded)}).updates(); err == nil {
		t.Fatal("去空白后不足 16 字符必须被拒")
	}

	// 空串 = 显式清空（不受最小长度约束）。
	empty := ""
	updates, err = (settingsSaveRequest{APIKey: settingsStrPtr(empty)}).updates()
	if err != nil || updates[configKeyAPIKey] != "" {
		t.Fatalf("空串必须落成清空: %+v err=%v", updates, err)
	}

	// 纯空白 = 去空白后为空 ⇒ 同样是清空（与 v1 行为一致）。
	blank := "   "
	updates, err = (settingsSaveRequest{APIKey: settingsStrPtr(blank)}).updates()
	if err != nil || updates[configKeyAPIKey] != "" {
		t.Fatalf("纯空白按清空处理: %+v err=%v", updates, err)
	}

	// 字段缺省（nil）= 不修改：不得出现 api_key 键。
	updates, err = (settingsSaveRequest{RequestTimeout: settingsIntPtr(30)}).updates()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := updates[configKeyAPIKey]; ok {
		t.Fatalf("字段缺省时不得改写 api_key：%+v", updates)
	}

	// 非空密钥与其它键同批保存：互不影响。
	updates, err = (settingsSaveRequest{
		APIKey:               settingsStrPtr(exact),
		BreakerFailThreshold: settingsIntPtr(7),
	}).updates()
	if err != nil || updates[configKeyAPIKey] != exact || updates[configKeyBreakerFailThreshold] != "7" {
		t.Fatalf("多键保存结果错误: %+v err=%v", updates, err)
	}
}

func TestSettingsSaveOtherKeyBounds(t *testing.T) {
	cases := []struct {
		name string
		req  settingsSaveRequest
		ok   bool
	}{
		{"无任何键", settingsSaveRequest{}, false},
		{"request_timeout 上界内", settingsSaveRequest{RequestTimeout: settingsIntPtr(600)}, true},
		{"request_timeout 超上界", settingsSaveRequest{RequestTimeout: settingsIntPtr(601)}, false},
		{"request_timeout 负数", settingsSaveRequest{RequestTimeout: settingsIntPtr(-1)}, false},
		{"breaker_fail_threshold 0 合法", settingsSaveRequest{BreakerFailThreshold: settingsIntPtr(0)}, true},
		{"breaker_fail_threshold 超上界", settingsSaveRequest{BreakerFailThreshold: settingsIntPtr(10001)}, false},
		{"breaker_cooldown_seconds 上界内", settingsSaveRequest{BreakerCooldownSeconds: settingsIntPtr(86400)}, true},
		{"breaker_cooldown_seconds 超上界", settingsSaveRequest{BreakerCooldownSeconds: settingsIntPtr(86401)}, false},
		{"stream_timeout 上界内", settingsSaveRequest{StreamTimeout: settingsIntPtr(86400)}, true},
		{"allow_private_hosts=1", settingsSaveRequest{AllowPrivateHosts: settingsIntPtr(1)}, true},
		{"allow_private_hosts=0", settingsSaveRequest{AllowPrivateHosts: settingsIntPtr(0)}, true},
		{"allow_private_hosts=2 不在闭集", settingsSaveRequest{AllowPrivateHosts: settingsIntPtr(2)}, false},
		{"currency 空", settingsSaveRequest{Currency: settingsStrPtr("")}, false},
		{"currency 纯空白", settingsSaveRequest{Currency: settingsStrPtr("   ")}, false},
		{"currency 10 字符", settingsSaveRequest{Currency: settingsStrPtr("ABCDEFGHIJ")}, true},
		{"currency 11 字符", settingsSaveRequest{Currency: settingsStrPtr("ABCDEFGHIJK")}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			updates, err := tc.req.updates()
			if tc.ok && err != nil {
				t.Fatalf("应接收：%v", err)
			}
			if !tc.ok {
				if err == nil {
					t.Fatalf("应拒收，实际落库 %+v", updates)
				}
				if updates != nil {
					t.Fatalf("拒收时必须不产出任何落库项：%+v", updates)
				}
				return
			}
			if len(updates) == 0 {
				t.Fatal("接收时必须至少一项落库键")
			}
		})
	}
}

// ===== 驱动侧：ChatResult 截断字段传播（累积上限；伪上游 + 真驱动）=====

func TestDriverTruncatedFlagPropagatesToChatResult(t *testing.T) {
	const chunks = 30
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for i := 0; i < chunks; i++ {
			fmt.Fprintf(w, "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"%s\"}}]}\n\n",
				strings.Repeat("q", 100))
			if flusher != nil {
				flusher.Flush()
			}
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer server.Close()

	limits := DefaultSSELimits()
	limits.MaxAccumulatedBytes = 500
	driver := NewOpenAIDriverWithSSELimits(true, limits)

	res, err := driver.ChatStream(context.Background(), Channel{
		ID: 3, BaseURL: server.URL, APIKey: "sk-upstream", Model: "m", Timeout: 5,
	}, ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}}, nil)
	if err != nil {
		t.Fatalf("累积超限不得报错（流应正常收尾）：%v", err)
	}
	if res == nil || !res.Truncated {
		t.Fatalf("截断标记未传播到 ChatResult：%+v", res)
	}
	if len(res.Content) != 500 {
		t.Fatalf("content 长度 = %d，应停在阈值 500 内", len(res.Content))
	}
	if res.Chunks != chunks {
		t.Fatalf("帧计数 = %d，期望 %d", res.Chunks, chunks)
	}
	// 同一条流换一个足够大的上限：不得置位（判据可鉴别）。
	roomy := NewOpenAIDriverWithSSELimits(true, SSELimits{MaxAccumulatedBytes: 1 << 20})
	ok, err := roomy.ChatStream(context.Background(), Channel{
		ID: 3, BaseURL: server.URL, APIKey: "sk-upstream", Model: "m", Timeout: 5,
	}, ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ok.Truncated || len(ok.Content) != chunks*100 {
		t.Fatalf("上限内的流不得截断：truncated=%v len=%d", ok.Truncated, len(ok.Content))
	}
}

// ===== B3 前提：出站护栏工厂（newGuardedHTTPClient）=====

func TestNewGuardedHTTPClientBlocksLoopbackUnlessAllowed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer server.Close()

	// allow_private_hosts=0：到 127.0.0.1 的连接被拒（护栏在 TCP 建立前按实际 IP 判定）。
	blocked := newGuardedHTTPClient(false, 5)
	resp, err := blocked.Get(server.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatalf("allow_private=false 时 loopback 必须被拒，实际 %d", resp.StatusCode)
	}
	if !strings.Contains(err.Error(), "出站护栏") {
		t.Fatalf("拒绝文案应指向出站护栏：%v", err)
	}

	// allow_private_hosts=1：放行（本机自建上游 ollama 等）。
	allowed := newGuardedHTTPClient(true, 5)
	resp, err = allowed.Get(server.URL)
	if err != nil {
		t.Fatalf("allow_private=true 必须放行 loopback：%v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != "ok" {
		t.Fatalf("放行后响应体 = %q err=%v", body, err)
	}
}

func TestNewGuardedHTTPClientRefusesRedirect(t *testing.T) {
	var targetHits int
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetHits++
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/next", http.StatusFound)
	}))
	defer redirector.Close()

	// 用 allow_private=true 绕过内网拦截，单看「不跟随重定向」这一条。
	client := newGuardedHTTPClient(true, 5)
	resp, err := client.Get(redirector.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatal("重定向必须被拒（防 SSRF-by-redirect）")
	}
	if !strings.Contains(err.Error(), "重定向") {
		t.Fatalf("拒绝文案应说明不跟随重定向：%v", err)
	}
	if targetHits != 0 {
		t.Fatalf("重定向目标被访问了 %d 次，必须零访问", targetHits)
	}
}
