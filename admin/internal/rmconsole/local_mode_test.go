// plugin: rolling-memory v0.0.58
package rollingmemory

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
)

// writeLocalModeConfig 写入仅含运行模式的引擎配置（其余键取缺省）。
func writeLocalModeConfig(t *testing.T, home, mode string) {
	t.Helper()
	dir := filepath.Join(home, ".config", "rolling-memory")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	cfg := "[mode]\nname = \"" + mode + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// TestRollingModeForHomeDefaultsToLocal 锁定本地模式判定：缺省 local、显式 cloud 才走云端、
// 未知取值一律按本地处置（本地是零外呼默认面），环境变量优先级最高。
func TestRollingModeForHomeDefaultsToLocal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ROLLING_MEMORY_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	t.Setenv("ROLLING_MEMORY_MODE", "")
	if got := rollingModeForHome(home); got != modeLocal {
		t.Fatalf("missing config mode = %q, want local", got)
	}
	if !isLocalModeForHome(home) {
		t.Fatal("missing config must be treated as local mode")
	}

	writeLocalModeConfig(t, home, "sideways")
	if !isLocalModeForHome(home) {
		t.Fatal("unknown mode must stay on the local no-egress face")
	}

	writeLocalModeConfig(t, home, "cloud")
	if isLocalModeForHome(home) {
		t.Fatal("explicit cloud mode must not be treated as local")
	}

	t.Setenv("ROLLING_MEMORY_MODE", "local")
	if !isLocalModeForHome(home) {
		t.Fatal("ROLLING_MEMORY_MODE=local must override the config file")
	}
}

// TestStripCloudBrainEnvRemovesOverrides 锁定本地模式下派生 CLI 子进程前的环境清洗：
// 云端端点/密钥覆盖变量必须被剔除，其余环境原样保留。
func TestStripCloudBrainEnvRemovesOverrides(t *testing.T) {
	env := []string{
		"PATH=/usr/bin",
		"ROLLING_MEMORY_BRAIN_URL=http://127.0.0.1:18081/open/memory/v1",
		"ROLLING_MEMORY_BRAIN_KEY=mem2.secret",
		"ROLLING_MEMORY_MODE=local",
	}
	got := stripCloudBrainEnv(env)
	want := []string{"PATH=/usr/bin", "ROLLING_MEMORY_MODE=local"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("stripCloudBrainEnv = %v, want %v", got, want)
	}
}

// TestCollectArtifactStatsReportsLocalVaultInLocalMode 锁定本地模式读数：
// 不把云端地址/用户 ID 当成本地模式的接口绑定读数，如实上报 local://vault。
func TestCollectArtifactStatsReportsLocalVaultInLocalMode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ROLLING_MEMORY_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("ROLLING_MEMORY_MODE", "")
	writeLocalModeConfig(t, home, "local")

	dbPath := filepath.Join(t.TempDir(), "artifacts.db")
	db, err := openWritableVaultDB(dbPath)
	if err != nil {
		t.Fatalf("open vault: %v", err)
	}
	_ = db.Close()

	st := collectArtifactStats(dbPath, UserScope)
	if st.UpstreamEndpoint != "local://vault" {
		t.Fatalf("local mode endpoint = %q, want local://vault", st.UpstreamEndpoint)
	}
	if st.UpstreamUserID != "" {
		t.Fatalf("local mode must not report a cloud user id, got %q", st.UpstreamUserID)
	}
}

// TestWidgetAndGraphHandlersMakeZeroCloudCallsInLocalMode 锁定本地模式下两个高频/重型读口的零外呼：
// /widget（1Hz 采样）改为本地 SQLite 金库往返；/graph 不再拉取云端拓扑。两者都必须命中哨兵即失败。
func TestWidgetAndGraphHandlersMakeZeroCloudCallsInLocalMode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ROLLING_MEMORY_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("ROLLING_MEMORY_MODE", "")
	writeLocalModeConfig(t, home, "local")

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	// 本地金库：真实建表 + 一条情景单元，确保 /graph 走本地合并路径。
	dbPath := filepath.Join(home, ".local", "share", "rolling-memory", "memory.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatalf("mkdir data dir: %v", err)
	}
	t.Setenv("ROLLING_MEMORY_DB", dbPath)
	db, err := openWritableVaultDB(dbPath)
	if err != nil {
		t.Fatalf("open vault: %v", err)
	}
	if _, err := db.Exec(`INSERT OR REPLACE INTO thin_vault_docs
		(doc_id, layer, occurred_at, raw_text, sentences_json, address_uri, coord64, kept_indices_json)
		VALUES ('local_graph_1', 'episodic', 1762380000, '本地图谱单元', '[]', 'C6:L1:B001#00', 1, '[]')`); err != nil {
		_ = db.Close()
		t.Fatalf("seed local doc: %v", err)
	}
	_ = db.Close()

	// 账户态指向哨兵：本地模式连读都不该读它，更不该拨号。
	acct := `{"base_url":"` + srv.URL + `","token":"tok","api_key":"key","namespace":"default"}`
	if err := os.WriteFile(cloudAccountFilePath(), []byte(acct), 0o600); err != nil {
		t.Fatalf("write account state: %v", err)
	}

	widgetRaw := callPluginHandler(t, widgetHandler, "/widget", `{}`)
	var widgetEnvelope struct {
		Code int `json:"code"`
		Data struct {
			Mode             string  `json:"mode"`
			UpstreamOnline   bool    `json:"upstream_online"`
			UpstreamEndpoint string  `json:"upstream_endpoint"`
			UpstreamRTTMS    float64 `json:"upstream_rtt_ms"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(widgetRaw), &widgetEnvelope); err != nil {
		t.Fatalf("decode widget: %v (%s)", err, widgetRaw)
	}
	if widgetEnvelope.Code != 0 || widgetEnvelope.Data.Mode != "local" || widgetEnvelope.Data.UpstreamEndpoint != "local://vault" {
		t.Fatalf("local widget = %s, want mode=local endpoint=local://vault", widgetRaw)
	}
	if !widgetEnvelope.Data.UpstreamOnline {
		t.Fatalf("local vault probe must be online: %s", widgetRaw)
	}

	InvalidateLocomoPublicCorpusCache()
	graphRaw := callPluginHandler(t, graphHandler, "/graph", `{"scope":"default","limit":50}`)
	if !strings.Contains(graphRaw, `"suite":"locomo"`) {
		t.Fatalf("local graph must return preset LoCoMo preview graph: %s", clipTest(graphRaw, 400))
	}
	if strings.Contains(graphRaw, "local_graph_1") {
		t.Fatalf("local mode preview graph must not expose raw local vault docs: %s", clipTest(graphRaw, 400))
	}

	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("local mode widget/graph made %d cloud requests, want 0", got)
	}
}

func clipTest(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// TestLocalPlainEvalArmGuard 锁定评测臂的本地模式闸：纯本地只开放 local_plain，
// 脉冲臂（pulse_plain / pulse_cipher）在本地模式 fail-closed，切到 cloud 模式后放行
// （脉冲协议与 SDK 实现完整保留，供下一代接入）。
func TestLocalPlainEvalArmGuard(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ROLLING_MEMORY_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("ROLLING_MEMORY_MODE", "")

	writeLocalModeConfig(t, home, "local")
	if err := localPlainEvalArmError(EvalPulseModeLocalPlain); err != nil {
		t.Fatalf("local mode must allow local_plain: %v", err)
	}
	for _, pulse := range []string{EvalPulseModePulsePlain, EvalPulseModePulseCipher} {
		err := localPlainEvalArmError(pulse)
		if err == nil || !strings.Contains(err.Error(), "本地模式仅支持 local_plain") {
			t.Fatalf("local mode + %s error = %v, want the explicit local-mode message", pulse, err)
		}
	}

	writeLocalModeConfig(t, home, "cloud")
	for _, pulse := range []string{EvalPulseModeLocalPlain, EvalPulseModePulsePlain, EvalPulseModePulseCipher} {
		if err := localPlainEvalArmError(pulse); err != nil {
			t.Fatalf("cloud mode must allow %s: %v", pulse, err)
		}
	}
}

// TestEvalIngestRejectsPulseArmInLocalMode 端到端锁闸：本地模式下 ingest 请求带脉冲臂
// 必须在任何外部动作之前 fail-closed（消息可见，且不产生评测准备）。
func TestEvalIngestRejectsPulseArmInLocalMode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ROLLING_MEMORY_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("ROLLING_MEMORY_MODE", "")
	writeLocalModeConfig(t, home, "local")

	raw := callPluginHandler(t, evalIngestHandler, "/eval/ingest", `{"suite":"locomo","action":"prepare","arm_mode":"sdk-pulse-cipher","pulse_mode":"pulse_cipher"}`)
	var envelope struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		t.Fatalf("decode ingest response: %v (%s)", err, raw)
	}
	if envelope.Code == 0 || !strings.Contains(envelope.Message, "本地模式仅支持 local_plain") {
		t.Fatalf("local-mode pulse ingest = %s, want fail-closed local-mode error", raw)
	}
}

// TestWidgetHandlerProbesCloudInCloudMode 是上面零外呼测试的正对照（防假绿）：
// 同一哨兵在显式 cloud 模式下必须被真实拨号，证明「0 请求」不是探针本身失效。
func TestWidgetHandlerProbesCloudInCloudMode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ROLLING_MEMORY_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("ROLLING_MEMORY_MODE", "")
	writeLocalModeConfig(t, home, "cloud")

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	acct := `{"base_url":"` + srv.URL + `","token":"tok","api_key":"key","namespace":"default"}`
	if err := os.WriteFile(cloudAccountFilePath(), []byte(acct), 0o600); err != nil {
		t.Fatalf("write account state: %v", err)
	}

	raw := callPluginHandler(t, widgetHandler, "/widget", `{}`)
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			Mode             string `json:"mode"`
			UpstreamEndpoint string `json:"upstream_endpoint"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		t.Fatalf("decode widget: %v (%s)", err, raw)
	}
	if envelope.Code != 0 || envelope.Data.Mode != "cloud" {
		t.Fatalf("cloud widget = %s, want mode=cloud", raw)
	}
	if envelope.Data.UpstreamEndpoint != srv.URL+"/open/memory/v1" {
		t.Fatalf("cloud widget endpoint = %q, want the configured sentinel", envelope.Data.UpstreamEndpoint)
	}
	if got := atomic.LoadInt32(&hits); got == 0 {
		t.Fatal("cloud mode must probe the configured endpoint (positive control for the zero-egress test)")
	}
}

// TestCloudAccountEndpointsFailClosedInLocalMode 锁定云端账户面的本地模式硬闸：
// 状态快照（只读本地文件）可读，但任何会外呼的账户操作一律 fail-closed，且不发出任何请求。
func TestCloudAccountEndpointsFailClosedInLocalMode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ROLLING_MEMORY_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("ROLLING_MEMORY_MODE", "")
	writeLocalModeConfig(t, home, "local")

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	// 即便磁盘上残留了云端账户态，本地模式也不得拨号。
	acct := `{"base_url":"` + srv.URL + `","token":"tok","api_key":"key","namespace":"default"}`
	if err := os.WriteFile(cloudAccountFilePath(), []byte(acct), 0o600); err != nil {
		t.Fatalf("write account state: %v", err)
	}

	stateRaw := callPluginHandler(t, cloudAccountStateHandler, "/cloud/account/state", `{}`)
	var stateEnvelope struct {
		Code int `json:"code"`
		Data struct {
			Mode          string `json:"mode"`
			RemoteEnabled bool   `json:"remote_enabled"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stateRaw), &stateEnvelope); err != nil {
		t.Fatalf("decode account state: %v (%s)", err, stateRaw)
	}
	if stateEnvelope.Code != 0 || stateEnvelope.Data.Mode != "local" || stateEnvelope.Data.RemoteEnabled {
		t.Fatalf("local account state = %s, want mode=local remote_enabled=false", stateRaw)
	}

	for name, handler := range map[string]gin.HandlerFunc{
		"memory":  cloudAccountMemoryHandler,
		"devices": cloudAccountDevicesHandler,
		"rotate":  cloudAccountRotateKeyHandler,
		"signkey": cloudAccountSignKeyHandler,
	} {
		raw := callPluginHandler(t, handler, "/cloud/account/"+name, `{"action":"status"}`)
		var envelope struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
			t.Fatalf("decode %s response: %v (%s)", name, err, raw)
		}
		if envelope.Code == 0 || !strings.Contains(envelope.Message, "本地模式已停用云端账户接口") {
			t.Fatalf("%s in local mode = %s, want fail-closed local-mode error", name, raw)
		}
	}

	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("local mode cloud account surface made %d requests, want 0", got)
	}
}
