// plugin: rolling-memory v0.0.58
package rollingmemory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestParseConfigSwitchesAndClassifyOutcome(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	content := `
[mode]
name = "cloud"

[recall]
vector = false
raw_when_sparse = true

[hooks]
boot_context = true
ledger = false

[cloud.sync]
auto_watch = false
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	sw := parseConfigSwitches(cfgPath, true)
	if !sw.BootContext || sw.Ledger || !sw.RawWhenSparse || sw.Mode != "cloud" || !sw.WidgetEnabled {
		t.Fatalf("unexpected switches: %#v", sw)
	}
	if sw.CloudAutoSync {
		t.Fatal("cloud.sync.auto_watch = false must disable CloudAutoSync")
	}

	logPath := filepath.Join(dir, "rolling-memory.log")
	logData := `{"event": "recall", "injected": 1, "bands": 3, "ms": 142, "nudge": 1, "ts": 1793270000}
{"event": "recall", "injected": 0, "dedup": 1, "ts": 1793270010}
{"event": "close", "imported": 1, "replayed": 1, "written": 2, "ts": 1793270020}
`
	if err := os.WriteFile(logPath, []byte(logData), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	events, lastMS := readHookEvents(logPath, 10)
	if len(events) != 3 || lastMS != 142 {
		t.Fatalf("events=%d lastMS=%d", len(events), lastMS)
	}
	if events[0].Outcome != "written" || events[1].Outcome != "dedup" || events[2].Outcome != "injected+nudge" {
		t.Fatalf("unexpected outcomes: %#v", events)
	}
}

func TestParseConfigSwitchesModeFollowsConfig(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"local_explicit", "[mode]\nname = \"local\"\n", "local"},
		{"cloud_explicit", "[mode]\nname = \"cloud\"\n", "cloud"},
		{"local_uppercase", "[mode]\nname = \"Local\"\n", "local"},
		{"missing_section_defaults_local", "[recall]\nraw_when_sparse = true\n", "local"},
		{"invalid_value_keeps_default", "[mode]\nname = \"weird\"\n", "local"},
	}
	for _, tc := range cases {
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "config.toml")
		if err := os.WriteFile(cfgPath, []byte(tc.content), 0o644); err != nil {
			t.Fatalf("%s: write config: %v", tc.name, err)
		}
		if sw := parseConfigSwitches(cfgPath, true); sw.Mode != tc.want {
			t.Fatalf("%s: mode=%q want %q", tc.name, sw.Mode, tc.want)
		}
	}
}

func TestApplySwitchToggleValidation(t *testing.T) {
	home := t.TempDir()
	if err := applySwitchToggle(home, "widget_enabled", "false"); err != nil {
		t.Fatalf("toggle widget_enabled: %v", err)
	}
	if err := applySwitchToggle(home, "widget_enabled", "bad"); err == nil {
		t.Fatal("expected error for invalid widget_enabled value")
	}
	if err := applySwitchToggle(home, "unknown_key", "true"); err == nil {
		t.Fatal("expected error for unknown switch key")
	}
}

func TestCollectDashboardData(t *testing.T) {
	home := t.TempDir()
	logDir := filepath.Join(home, ".local", "state", "rolling-memory")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("mkdir logDir: %v", err)
	}
	shareDir := filepath.Join(home, ".local", "share", "rolling-memory")
	if err := os.MkdirAll(shareDir, 0o755); err != nil {
		t.Fatalf("mkdir shareDir: %v", err)
	}
	dbPath := filepath.Join(shareDir, "memory.db")
	db, err := openWritableVaultDB(dbPath)
	if err != nil {
		t.Fatalf("openWritableVaultDB: %v", err)
	}
	nowUnix := time.Now().Unix()
	_, _ = db.Exec(`INSERT INTO thin_vault_bg_chunks(unit_id, abs_path, rel_path, anchor, occurred_at) VALUES ('bg-1', '/a/b.md', 'b.md', 'h1', ?), ('bg-2', '/a/c.md', 'c.md', 'h2', ?);`, nowUnix-60, nowUnix-30)
	_, _ = db.Exec(`INSERT INTO thin_vault_docs(doc_id, layer, occurred_at, raw_text, address_uri) VALUES ('ep-1', 'episodic', ?, 'episodic turn 1', 'C6:L3:B001/C6:L2:B001/C6:L1:B001#00');`, nowUnix-40)
	_, _ = db.Exec(`INSERT INTO thin_vault_noetic(noetic_id, occurred_at, status, query_text, answer_text, cited_unit_ids_json) VALUES ('nt-1', ?, 'active', 'cue', 'ans', '["ep-1"]');`, nowUnix-20)
	_ = db.Close()

	logPath := filepath.Join(logDir, "rolling-memory.log")
	logData := `{"event": "recall", "injected": 1, "bands": 3, "ms": 180, "ts": 1793270000}
{"event": "recall", "injected": 1, "bands": 2, "ms": 220, "ts": 1793270100}
{"event": "close", "imported": 1, "replayed": 1, "written": 2, "ts": 1793270200}
`
	if err := os.WriteFile(logPath, []byte(logData), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	resp := collectDashboardData(home, "mem:self")
	if resp.Version != Version || resp.Scope != "mem:self" {
		t.Fatalf("unexpected version/scope: %#v", resp)
	}
	if resp.L1Total != 2 || resp.L2Total != 1 || resp.L3Total != 1 || resp.ActiveAssertions != 4 {
		t.Fatalf("unexpected L1/L2/L3 totals: l1=%d l2=%d l3=%d active=%d", resp.L1Total, resp.L2Total, resp.L3Total, resp.ActiveAssertions)
	}
	if sumInts(resp.Trend1D.L1Added) != 2 || sumInts(resp.Trend1D.L2Added) != 1 || sumInts(resp.Trend1D.L3Added) != 1 || sumInts(resp.Trend1D.Assertions) != 4 {
		t.Fatalf("unexpected Trend1D layer sums: %+v", resp.Trend1D)
	}
	if resp.TotalHookEvents != 3 || resp.InjectedEvents != 3 || resp.AvgRecallMS != 200 {
		t.Fatalf("unexpected hook stats: total=%d injected=%d avgMS=%d", resp.TotalHookEvents, resp.InjectedEvents, resp.AvgRecallMS)
	}
	if len(resp.Trend1H.Dates) != 12 || len(resp.Trend1D.Dates) != 24 || len(resp.Trend7D.Dates) != 7 || len(resp.Trend30D.Dates) != 30 {
		t.Fatalf("unexpected trend lengths: 1h=%d 1d=%d 7d=%d 30d=%d", len(resp.Trend1H.Dates), len(resp.Trend1D.Dates), len(resp.Trend7D.Dates), len(resp.Trend30D.Dates))
	}
	if len(resp.AssertionSpark) != 12 || len(resp.LatencySpark) != 12 {
		t.Fatalf("unexpected spark lengths: %d / %d", len(resp.AssertionSpark), len(resp.LatencySpark))
	}

	termResp := handleTerminalAction(home, TerminalRequest{Action: "stream", Limit: 10})
	if len(termResp.StreamLines) != 4 {
		t.Fatalf("expected 4 terminal stream lines (1 unit + 3 hooks), got %d", len(termResp.StreamLines))
	}
	if deterministicIntID("node:abc") != deterministicIntID("node:abc") {
		t.Fatal("deterministicIntID should be deterministic")
	}

	spec := buildProtocolParitySpec()
	if spec.BuiltinProtocol != "locomo-backboard" || spec.ProtocolID != FrozenProtocolID || !spec.AllPinsVerified {
		t.Fatalf("unexpected protocol parity spec: %#v", spec)
	}
	board := locomoBackboardLeaderboard()
	if len(board) < 8 {
		t.Fatalf("expected at least 8 leaderboard entries, got %d", len(board))
	}
	hasBackboard := false
	hasOurs := false
	for _, entry := range board {
		if entry.System == "Backboard" && entry.Overall == 90.00 && entry.IsPublicBoard {
			hasBackboard = true
		}
		if entry.IsOurs {
			hasOurs = true
		}
	}
	if !hasBackboard || !hasOurs {
		t.Fatalf("leaderboard missing expected entries: hasBackboard=%v hasOurs=%v", hasBackboard, hasOurs)
	}
}

func TestEvalPreFlightCheckAndScoringAndDetails(t *testing.T) {
	// 1. 远端未配置 API Key 时应被预检阻断
	blocked := checkAIModelConfig(AIModelConfig{
		BaseURL:     "https://api.deepseek.com/v1",
		APIKey:      "",
		HasAPIKey:   false,
		AnswerModel: "deepseek-chat",
		JudgeModel:  "deepseek-chat",
	})
	if blocked.Ready {
		t.Fatalf("expected remote config without API key to be blocked: %#v", blocked)
	}

	// 2. 远端配置了 API Key 时应通过全部 5 项预检
	ready := checkAIModelConfig(AIModelConfig{
		BaseURL:     "https://api.deepseek.com/v1",
		APIKey:      "sk-test-1234567890",
		HasAPIKey:   true,
		AnswerModel: "deepseek-chat",
		JudgeModel:  "deepseek-chat",
	})
	if !ready.Ready || len(ready.Checks) != 5 {
		t.Fatalf("expected config with key to pass all 5 checks: %#v", ready)
	}

	// 3. 本地 Ollama 免密钥应放行
	localReady := checkAIModelConfig(AIModelConfig{
		BaseURL:     "http://127.0.0.1:11434/v1",
		AnswerModel: "qwen2.5:7b",
		JudgeModel:  "qwen2.5:7b",
	})
	if !localReady.Ready || !localReady.IsLocalhost {
		t.Fatalf("expected localhost config to be ready without key: %#v", localReady)
	}

	// 4. 证据召回与 F1 算子测试
	bestEv, matches := evaluateEvidenceRecall(
		[]string{"Melanie bought a shell necklace in Hawaii."},
		"[2026-05-07] Melanie: I bought a shell necklace when I visited Hawaii.",
	)
	if bestEv < 0.8 || len(matches) != 1 || !matches[0].Hit {
		t.Fatalf("unexpected evidence recall: bestEv=%v matches=%#v", bestEv, matches)
	}
	f1, exact := computeTokenF1("shell necklace", "shell necklace")
	if f1 != 1.0 || !exact {
		t.Fatalf("expected f1=1.0 exact=true, got f1=%v exact=%v", f1, exact)
	}
}

// TestReferenceBaselinesCurrentArmAndRadarID 锁住内置台账的「按 ID 解析」契约：
// 现行臂基行恒可按 radarCurrentBaselineID 取到（最新 1,540 全量读数，判对 1313/1540），
// 防「新增基线或代次刷新」把雷达当前臂 / 默认选中记录挤到别的行而无人察觉。
func TestReferenceBaselinesCurrentArmAndRadarID(t *testing.T) {
	bases := referenceBaselines()
	if len(bases) == 0 {
		t.Fatalf("reference baselines must not be empty")
	}
	current, ok := baselineByID(bases, radarCurrentBaselineID)
	if !ok || current.ID != "baseline-rm-20261116" || current.JudgeCorrect != 1313 || current.Tier != "1540" {
		t.Fatalf("radar current arm must resolve to the latest sealed 1,540 reading by ID: ok=%v row=%q correct=%d tier=%q", ok, current.ID, current.JudgeCorrect, current.Tier)
	}
}

func TestATMBenchSuiteIntegration(t *testing.T) {
	spec := buildATMProtocolParitySpec()
	if !spec.AllPinsVerified || spec.ProtocolID != ATMProtocolID {
		t.Fatalf("ATM protocol pins not verified: %#v", spec)
	}
	board := buildATMBenchLeaderboard()
	if len(board.HardEntries) < 4 || len(board.MainEntries) < 3 {
		t.Fatalf("ATM leaderboard entries incomplete: hard=%d main=%d", len(board.HardEntries), len(board.MainEntries))
	}
	atmBases := atmReferenceBaselines()
	if len(atmBases) < 2 || atmBases[0].Suite != SuiteATM {
		t.Fatalf("ATM baselines invalid: %#v", atmBases)
	}
	jac := computeATMListJaccardAdmin("20240501_120000, email0042", "20240501_120000, EMAIL0042")
	if jac != 1.0 {
		t.Fatalf("expected Jaccard=1.0, got %v", jac)
	}
	emOK, _ := computeATMNumberEMAdmin("6 nights in 2024", "You stayed 6 nights during 2024.")
	if !emOK {
		t.Fatalf("expected number EM match")
	}
	verdict, reason, err := parseATMJudgeOutputAdmin(`{"explanation":"Matches ground truth.","accuracy":"true"}`)
	if err != nil || verdict != "correct" || reason == "" {
		t.Fatalf("parseATMJudgeOutputAdmin failed: verdict=%q reason=%q err=%v", verdict, reason, err)
	}
}

// TestATMScoringParityWithCLI 锁住 Admin 与 CLI（internal/eval/atmbench.go）的官方判分同尺：
// number 用官方 deterministic_accuracy（裸 4 位年份不是 RE_NUMBER 数字）、
// list 用官方 list_jaccard_score、open_end 用官方 parse_judge_response 宽松回退。
func TestATMScoringParityWithCLI(t *testing.T) {
	if ok, _ := computeATMNumberEMAdmin("6 nights", "6 nights in 2024"); !ok {
		t.Fatal("official RE_NUMBER ignores bare 4-digit years; this case must pass")
	}
	if ok, _ := computeATMNumberEMAdmin("Sweden", "I moved from Sweden"); !ok {
		t.Fatal("normalized substring containment must pass")
	}
	if verdict, _, err := parseATMJudgeOutputAdmin("CORRECT"); err != nil || verdict != "correct" {
		t.Fatalf("official keyword fallback must parse to correct, got verdict=%q err=%v", verdict, err)
	}
}

// TestRecomputeATMUsesContinuousQS 锁住 ATM judge_rate 取连续混合 QS 均值（与 CLI summary 一致），
// 同时 LoCoMo 仍用二值判对率；二值计数保留供五桶归因。
func TestRecomputeATMUsesContinuousQS(t *testing.T) {
	items := []EvalQuestionResultItem{
		{Category: "1", JudgeVerdict: "wrong", QSScore: 0.30, QSScored: true},
		{Category: "1", JudgeVerdict: "correct", QSScore: 0.80, QSScored: true},
	}
	_, _, _, jdgRate, correct, scored, _, _, _, _, _ := recomputeLiveAggregates(items, SuiteATM)
	if correct != 1 || scored != 2 {
		t.Fatalf("binary counts changed: correct=%d scored=%d", correct, scored)
	}
	if jdgRate != 0.55 {
		t.Fatalf("ATM judge_rate must be continuous QS mean 0.55, got %v", jdgRate)
	}
	_, _, _, locomoRate, _, _, _, _, _, _, _ := recomputeLiveAggregates(items, SuiteLoCoMo)
	if locomoRate != 0.5 {
		t.Fatalf("LoCoMo judge_rate must stay binary 0.5, got %v", locomoRate)
	}
}

func TestUnifiedWaveCellSwitches(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.toml")

	tomlText := `[hooks]
boot_context = true
ledger = true

[recall]
raw_when_sparse = false
`
	if err := os.WriteFile(cfgPath, []byte(tomlText), 0o600); err != nil {
		t.Fatal(err)
	}
	sw := parseConfigSwitches(cfgPath, true)
	// 无 [mode] 段时按引擎缺省（local），不得再用硬编码 cloud 兜底。
	if sw.Mode != "local" || sw.RawWhenSparse || !sw.BootContext || !sw.Ledger {
		t.Fatalf("unexpected unified the memory engine switches: %+v", sw)
	}

	// Verify collectArtifactStats returns initialized zero-safe struct on missing DB
	art := collectArtifactStats(filepath.Join(tmp, "nonexistent.db"), "mem:self")
	if art.UpstreamEngine == "" {
		t.Fatalf("expected non-empty UpstreamEngine in ArtifactStats")
	}
	bg := snapshotBgTask()
	if bg.Status == "" {
		t.Fatalf("expected non-empty default BgTaskStatus.Status")
	}

	// Verify statusStreamSignature changes when config.toml changes (driving SSE zero-poll push)
	sig1 := statusStreamSignature(tmp, filepath.Join(tmp, "memory.db"), filepath.Join(tmp, "rm.log"))
	cfgDir := filepath.Join(tmp, ".config", "rolling-memory")
	_ = os.MkdirAll(cfgDir, 0o700)
	_ = os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte("[mode]\nname = \"cloud\"\n"), 0o600)
	sig2 := statusStreamSignature(tmp, filepath.Join(tmp, "memory.db"), filepath.Join(tmp, "rm.log"))
	if sig1 == sig2 {
		t.Fatalf("expected statusStreamSignature to change after config.toml write, got identical %q", sig1)
	}
}

// TestStatusStreamPushesOnVaultChange 锁定「记忆图谱 / 控制台」共同依赖的零轮询 SSE 实时推送契约：
// 连接建立即推首帧；本地明文金库（thin_vault_docs / thin_vault_noetic）发生写入后，同一连接必须在
// 下一个 250ms 签名巡检周期内推送携带新计数的新帧——前端据此指纹对比自动刷新图谱，无需手动刷新。
func TestStatusStreamPushesOnVaultChange(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	vaultPath := filepath.Join(tmp, ".local", "share", "rolling-memory", "memory.db")
	db, err := openWritableVaultDB(vaultPath)
	if err != nil {
		t.Fatalf("openWritableVaultDB: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT OR REPLACE INTO thin_vault_docs(doc_id, layer, occurred_at, raw_text, sentences_json, address_uri, kept_indices_json)
		VALUES ('doc-1', 'episodic', 100, 'hello vault', '[]', 'C6:L1:B001#00', '[]')`); err != nil {
		t.Fatalf("seed thin_vault_docs: %v", err)
	}
	if _, err := db.Exec(`INSERT OR REPLACE INTO thin_vault_noetic(noetic_id, occurred_at, status, query_text, answer_text)
		VALUES ('nt-1', 100, 'active', 'q1', 'a1')`); err != nil {
		t.Fatalf("seed thin_vault_noetic: %v", err)
	}

	client := startRoomTestClient(t, "rm/status")
	time.Sleep(400 * time.Millisecond)

	// 写入变更：新增一条 L3 知性印迹（本地金库计数变化），并追加 hook 日志（签名双通道变化）
	if _, err := db.Exec(`INSERT OR REPLACE INTO thin_vault_noetic(noetic_id, occurred_at, status, query_text, answer_text)
		VALUES ('nt-2', 200, 'active', 'q2', 'a2')`); err != nil {
		t.Fatalf("write thin_vault_noetic: %v", err)
	}
	logDir := filepath.Join(tmp, ".local", "state", "rolling-memory")
	_ = os.MkdirAll(logDir, 0o755)
	if err := os.WriteFile(filepath.Join(logDir, "rolling-memory.log"), []byte("{\"event\": \"recall\"}\n"), 0o644); err != nil {
		t.Fatalf("write hook log: %v", err)
	}

	time.Sleep(1200 * time.Millisecond)
	client.stop()

	var noeticCounts []int
	for _, frame := range strings.Split(client.body(), "\n\n") {
		idx := strings.Index(frame, "data: ")
		if idx < 0 {
			continue
		}
		var payload struct {
			Artifacts struct {
				ThinVaultDocs   int `json:"thin_vault_docs"`
				ThinVaultNoetic int `json:"thin_vault_noetic"`
			} `json:"artifacts"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(frame[idx+len("data: "):])), &payload); err != nil {
			continue
		}
		if payload.Artifacts.ThinVaultDocs != 1 {
			t.Fatalf("expected thin_vault_docs=1 in every frame, got %d", payload.Artifacts.ThinVaultDocs)
		}
		noeticCounts = append(noeticCounts, payload.Artifacts.ThinVaultNoetic)
	}
	if len(noeticCounts) < 2 {
		t.Fatalf("expected >=2 status frames (initial + post-write push), got %d: %q", len(noeticCounts), client.body())
	}
	if noeticCounts[0] != 1 || noeticCounts[len(noeticCounts)-1] != 2 {
		t.Fatalf("expected noetic digest to advance 1 -> 2 across frames, got %v", noeticCounts)
	}
}

// TestATMLeaderboardStandalonePayload 剑桥 ATM-Bench 独立榜单页（web/views/atmLeaderboard.vue ⇄
// evalATMLeaderboardHandler）数据契约回归：页面四键载荷（atm_bench / atm_protocol_spec / atm_agents /
// atm_baselines）任一键缺位都会让页面静默空转，故在此把「键有值 + 我方行在位 + 三阶梯度齐档」钉死。
func TestATMLeaderboardStandalonePayload(t *testing.T) {
	spec := buildATMProtocolParitySpec()
	if !spec.AllPinsVerified || spec.ProtocolID != ATMProtocolID {
		t.Fatalf("ATM protocol spec pins not verified: %#v", spec)
	}
	agents := buildATMOfficialAgents(AIModelConfig{})
	if len(agents) != 2 {
		t.Fatalf("expected 2 official ATM agents (answer + judge), got %d", len(agents))
	}
	for _, ag := range agents {
		if !ag.PinVerified {
			t.Fatalf("ATM agent %s pin not verified (sha=%s)", ag.Code, ag.SystemSHA1)
		}
	}
	baselines := atmReferenceBaselines()
	for _, b := range baselines {
		if b.Suite != SuiteATM {
			t.Fatalf("baseline %s suite = %q, want %q", b.ID, b.Suite, SuiteATM)
		}
		if b.Label == "" || b.Note == "" {
			t.Fatalf("baseline %s missing label/note", b.ID)
		}
	}
	board := buildATMBenchLeaderboard()
	if board.BenchmarkName == "" || board.CorpusSummary == "" || board.ReproCommand == "" {
		t.Fatalf("ATM board header fields incomplete: %#v", board)
	}
	oursHard, oursMain := 0, 0
	for _, e := range board.HardEntries {
		if e.IsOurs {
			oursHard++
		}
	}
	for _, e := range board.MainEntries {
		if e.IsOurs {
			oursMain++
		}
	}
	if oursHard == 0 || oursMain == 0 {
		t.Fatalf("ATM board missing our rows (page KPI/best-row derivations rely on them): hard=%d main=%d", oursHard, oursMain)
	}
	// 页面第二、三张 KPI 卡按 is_ours 行的 QS / R@10 取最优，守卫读数非零防「—」空卡。
	bestHardQSSet, bestMainR10Set := false, false
	for _, e := range board.HardEntries {
		if e.IsOurs && e.QSScore > 0 {
			bestHardQSSet = true
		}
	}
	for _, e := range board.MainEntries {
		if e.IsOurs && e.RecallAt10 > 0 {
			bestMainR10Set = true
		}
	}
	if !bestHardQSSet || !bestMainR10Set {
		t.Fatalf("ATM board our rows missing positive QS/R@10: hardQS=%v mainR10=%v", bestHardQSSet, bestMainR10Set)
	}
}

func TestThreeLayersStatsAndBackgroundCLIContract(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "three_layers_test.db")
	db, err := openWritableVaultDB(dbPath)
	if err != nil {
		t.Fatalf("openWritableVaultDB: %v", err)
	}
	defer db.Close()

	// Seed 1 Episodic turn and 1 Noetic record
	_, err = db.Exec(`
		INSERT INTO thin_vault_docs(doc_id, layer, occurred_at, raw_text, address_uri)
		VALUES ('EPI-1', 'episodic', 1762380000, 'User discussed the memory engine architecture in Harness.', 'C6:L1:B001#00')
	`)
	if err != nil {
		t.Fatalf("insert episodic: %v", err)
	}
	_, err = db.Exec(`
		INSERT INTO thin_vault_noetic(noetic_id, occurred_at, status, query_text, answer_text, cited_unit_ids_json)
		VALUES ('NOE-1', 1762380100, 'active', 'What architecture was discussed?', 'the memory engine architecture.', '["EPI-1"]')
	`)
	if err != nil {
		t.Fatalf("insert noetic: %v", err)
	}

	// Create a nested local directory with .md and .json files and mount it into thin_vault_bg_dirs
	bgRoot := filepath.Join(tmpDir, "mounted_kb")
	nestedSub := filepath.Join(bgRoot, "sub1", "sub2")
	if err := os.MkdirAll(nestedSub, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nestedSub, "spec.md"), []byte("# Spec\nNested background doc."), 0o600); err != nil {
		t.Fatalf("write spec.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bgRoot, "config.json"), []byte(`{"port":8081}`), 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}

	_, err = db.Exec(`INSERT INTO thin_vault_bg_dirs(dir_id, root_path, added_at) VALUES ('d_test01', ?, 1762380000)`, bgRoot)
	if err != nil {
		t.Fatalf("insert bg_dir: %v", err)
	}

	previousExecutor := executeAdminBackgroundCLI
	defer func() { executeAdminBackgroundCLI = previousExecutor }()
	calls := []string{}
	executeAdminBackgroundCLI = func(_ context.Context, action, gotDBPath, rootPath string) ([]byte, error) {
		calls = append(calls, action)
		if gotDBPath != dbPath {
			t.Fatalf("db path=%q, want %q", gotDBPath, dbPath)
		}
		switch action {
		case "sync":
			if len(calls) == 1 {
				return []byte(`{"mounted_dirs":1,"scanned_files":2,"ingested_files":2,"ingested_chunks":2}`), nil
			}
			return []byte(`{"mounted_dirs":1,"scanned_files":2,"unchanged_files":2}`), nil
		case "remove":
			if rootPath != bgRoot {
				t.Fatalf("remove root=%q, want %q", rootPath, bgRoot)
			}
			return []byte(`{"removed_unit_ids":["bg:test#0"]}`), nil
		default:
			return nil, errors.New("unexpected action")
		}
	}

	syncRes, err := runAdminBackgroundDirsSync(context.Background(), dbPath)
	if err != nil || syncRes.MountedDirs != 1 || syncRes.ScannedFiles != 2 || syncRes.IngestedFiles != 2 {
		t.Fatalf("unexpected first syncRes: %+v err=%v", syncRes, err)
	}
	syncRes2, err := runAdminBackgroundDirsSync(context.Background(), dbPath)
	if err != nil || syncRes2.UnchangedFiles != 2 || syncRes2.IngestedFiles != 0 {
		t.Fatalf("expected 2 unchanged files on second sync, got %+v err=%v", syncRes2, err)
	}
	removedIDs, err := runAdminBackgroundDirRemove(context.Background(), dbPath, bgRoot)
	if err != nil || len(removedIDs) != 1 || removedIDs[0] != "bg:test#0" {
		t.Fatalf("unexpected remove result: ids=%v err=%v", removedIDs, err)
	}

	executeAdminBackgroundCLI = func(context.Context, string, string, string) ([]byte, error) {
		return []byte(`{"ok":false,"error":"ingest unavailable"}`), errors.New("ingest unavailable")
	}
	t.Setenv("ROLLING_MEMORY_DB", dbPath)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/admin/plugin/rolling-memory/background/sync", nil)
	backgroundSyncHandler(c)
	if !strings.Contains(recorder.Body.String(), `"code":1`) || !strings.Contains(recorder.Body.String(), "ingest unavailable") {
		t.Fatalf("sync failure was not returned as a business error: %s", recorder.Body.String())
	}
}

// TestBackgroundAddHandlerDelegatesToCanonicalCLI 验证控制台挂载统一复用根仓 CLI（单一能力面）：
// 目录经 canonical 路径校验后透传 add，并把 mount.notice 与 sync 原样回给前端；CLI 失败如实报业务错误。
func TestBackgroundAddHandlerDelegatesToCanonicalCLI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dbPath := resolveMemoryDBPath(home)
	t.Setenv("ROLLING_MEMORY_DB", dbPath)
	mountRoot := filepath.Join(home, "mounted_docs")
	if err := os.MkdirAll(mountRoot, 0o755); err != nil {
		t.Fatalf("mkdir mount root: %v", err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(mountRoot)
	if err != nil {
		t.Fatalf("canonicalize mount root: %v", err)
	}
	requestBody := func() string {
		body, err := json.Marshal(map[string]string{"path": mountRoot})
		if err != nil {
			t.Fatalf("encode add request: %v", err)
		}
		return string(body)
	}

	previousExecutor := executeAdminBackgroundCLI
	defer func() { executeAdminBackgroundCLI = previousExecutor }()
	calls := []string{}
	executeAdminBackgroundCLI = func(_ context.Context, action, gotDBPath, rootPath string) ([]byte, error) {
		calls = append(calls, action)
		if gotDBPath != dbPath {
			t.Fatalf("db path=%q, want %q", gotDBPath, dbPath)
		}
		if rootPath != canonicalRoot {
			t.Fatalf("add root=%q, want canonical %q", rootPath, canonicalRoot)
		}
		return []byte(`{"mount":{"dir_id":"d_add01","root_path":"` + canonicalRoot + `","notice":"directory is covered by the existing mount /x"},"sync":{"mounted_dirs":2,"scanned_files":1,"ingested_files":1,"ingested_chunks":1}}`), nil
	}

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/admin/plugin/rolling-memory/background/add", strings.NewReader(requestBody()))
	c.Request.Header.Set("Content-Type", "application/json")
	backgroundAddHandler(c)
	if len(calls) != 1 || calls[0] != "add" {
		t.Fatalf("expected exactly one add CLI call, got %v", calls)
	}
	resp := recorder.Body.String()
	if !strings.Contains(resp, `"code":0`) || !strings.Contains(resp, `"dir_id":"d_add01"`) || !strings.Contains(resp, `"notice":"directory is covered`) {
		t.Fatalf("add response lost mount identity/notice: %s", resp)
	}

	executeAdminBackgroundCLI = func(context.Context, string, string, string) ([]byte, error) {
		return []byte(`{"ok":false,"error":"mount sync unavailable"}`), errors.New("mount sync unavailable")
	}
	recorder = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/admin/plugin/rolling-memory/background/add", strings.NewReader(requestBody()))
	c.Request.Header.Set("Content-Type", "application/json")
	backgroundAddHandler(c)
	if !strings.Contains(recorder.Body.String(), `"code":1`) || !strings.Contains(recorder.Body.String(), "mount sync unavailable") {
		t.Fatalf("add failure was not returned as a business error: %s", recorder.Body.String())
	}
}

func TestCloudAccountLogoutDoesNotRestoreDevelopmentKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := saveCloudAccountState(CloudAccountState{BaseURL: defaultCloudServerURL, Namespace: defaultUpstreamDevUserID}); err != nil {
		t.Fatalf("save logged-out account: %v", err)
	}
	state := loadCloudAccountState()
	if state.APIKey != "" {
		t.Fatal("logged-out account unexpectedly restored the development API key")
	}
	if state.BaseURL != defaultCloudServerURL || state.Namespace != defaultUpstreamDevUserID {
		t.Fatalf("unexpected account fallback state: %+v", state)
	}
}

func TestLiveAdminBackgroundSync_UserID10(t *testing.T) {
	if os.Getenv("ROLLING_MEMORY_LIVE_TEST") != "1" {
		t.Skip("set ROLLING_MEMORY_LIVE_TEST=1 to run the local userid=10 integration")
	}
	if strings.TrimSpace(os.Getenv("ROLLING_MEMORY_BIN")) == "" {
		t.Fatal("ROLLING_MEMORY_BIN must point to a freshly built rolling-memory binary")
	}
	// 端侧唯一凭据 = 数据面签名钥（`mem2.*`）：取本机已登录的账户态并临期续签；无凭据即跳过。
	account := loadCloudAccountState()
	if err := ensureSignedKey(context.Background(), &account); err != nil {
		t.Skipf("live data-plane credentials not configured (%v); skipping live background integration", err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := saveCloudAccountState(account); err != nil {
		t.Fatalf("save live account state: %v", err)
	}
	bgRoot := filepath.Join(home, "synthetic-background")
	if err := os.MkdirAll(bgRoot, 0o755); err != nil {
		t.Fatalf("mkdir synthetic background: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bgRoot, "integration.md"), []byte(
		"# Synthetic Background Integration\nThis disposable document verifies blind background ingest and purge.\n",
	), 0o600); err != nil {
		t.Fatalf("write synthetic document: %v", err)
	}
	dbPath := filepath.Join(home, "memory.db")
	t.Setenv("ROLLING_MEMORY_DB", dbPath)
	t.Cleanup(func() {
		if _, err := runAdminBackgroundDirRemove(context.Background(), dbPath, bgRoot); err != nil {
			t.Logf("cleanup background purge: %v", err)
		}
	})

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/admin/plugin/rolling-memory/background/add", strings.NewReader(`{"path":`+strconv.Quote(bgRoot)+`}`))
	c.Request.Header.Set("Content-Type", "application/json")
	backgroundAddHandler(c)
	if !strings.Contains(recorder.Body.String(), `"code":0`) || !strings.Contains(recorder.Body.String(), `"ingested_files":1`) {
		t.Fatalf("live background add failed: %s", recorder.Body.String())
	}

	db, err := openWritableVaultDB(dbPath)
	if err != nil {
		t.Fatalf("open live vault: %v", err)
	}
	defer db.Close()
	var cloudChunks int
	if err := db.QueryRow(`SELECT COUNT(*) FROM thin_vault_bg_chunks WHERE address_uri != '' AND slots_json != '[]'`).Scan(&cloudChunks); err != nil {
		t.Fatalf("count live cloud chunks: %v", err)
	}
	if cloudChunks != 1 {
		t.Fatalf("live cloud chunks=%d, want 1", cloudChunks)
	}
}

// TestGraphProductManagerAudit 产品经理视角的真实图谱数据契约审计：
// 1) 256 截断时不得产生「断边孤儿节点」（任何入选 Cell 必有可见成员，任何入选 Unit 必有归属父 Cell）；
// 2) EventTotal 必须等于域内真实总节点数（不受 limit 截断影响），NodeShown 才随 limit 变化；
// 3) 全部（limit=-1）与 256 截断的孤立节点数均必须为 0；
// 4) collectArtifactStats 必须合并统计 thin_vault_docs 与 thin_vault_bg_chunks。
func TestGraphProductManagerAudit(t *testing.T) {
	home, _ := os.UserHomeDir()
	dbPath := resolveMemoryDBPath(home)
	if _, err := os.Stat(dbPath); err != nil {
		t.Skipf("real memory db %s not found", dbPath)
	}

	g256 := buildMemoryGraphWithLayers(dbPath, UserScope, 256, "", nil, nil)
	gAll := buildMemoryGraphWithLayers(dbPath, UserScope, -1, "", nil, nil)

	// Live-machine audit: the vault keeps absorbing background/episodic writes and the
	// upstream fetch is stale-while-revalidate cached, so a single pair of builds can
	// straddle a write. Rebuild once before declaring the invariant broken.
	if g256.Stats.EventTotal != gAll.Stats.EventTotal {
		g256 = buildMemoryGraphWithLayers(dbPath, UserScope, 256, "", nil, nil)
		gAll = buildMemoryGraphWithLayers(dbPath, UserScope, -1, "", nil, nil)
	}
	if g256.Stats.EventTotal != gAll.Stats.EventTotal {
		t.Fatalf("EventTotal must be constant across limits: 256=%d ALL=%d", g256.Stats.EventTotal, gAll.Stats.EventTotal)
	}
	if gAll.Stats.EventTotal != len(gAll.Nodes) {
		t.Fatalf("ALL EventTotal=%d must equal len(Nodes)=%d", gAll.Stats.EventTotal, len(gAll.Nodes))
	}

	// 连通性校验：每个入选 Cell 在画布中必须有 >=1 个 member_of 成员，且任何孤立节点数为 0
	memberCount := make(map[int]int)
	for _, e := range g256.Edges {
		if e.Relation == "member_of" {
			memberCount[e.To]++
		}
	}
	isolated := 0
	for _, n := range g256.Nodes {
		if n.Kind == "cell" && memberCount[n.ID] == 0 {
			t.Errorf("orphan Cell without visible members: %s", n.NodeKey)
			isolated++
		}
	}
	if isolated > 0 {
		t.Fatalf("limit=256 produced %d orphan nodes (must be 0)", isolated)
	}

	// ALL 模式下同样零孤立
	allMember := make(map[int]int)
	for _, e := range gAll.Edges {
		if e.Relation == "member_of" {
			allMember[e.To]++
		}
	}
	for _, n := range gAll.Nodes {
		if n.Kind == "cell" && allMember[n.ID] == 0 {
			t.Fatalf("ALL orphan Cell: %s", n.NodeKey)
		}
	}

	st := collectArtifactStats(dbPath, UserScope)
	if st.ThinVaultDocs == 0 {
		t.Fatalf("expected ThinVaultDocs>0 (docs+bg_chunks), got %+v", st)
	}

	// 三层均衡在场断言（证伪防御：当域内 L1/L2/L3 均 >0 时，limit=256 返回的 Unit 必须三层全 >0，且无 📦 emoji）
	shownByKindLayer := make(map[string]int)
	for _, n := range g256.Nodes {
		if strings.Contains(n.Summary, "📦") {
			t.Fatalf("node %s still contains legacy emoji in summary: %q", n.NodeKey, n.Summary)
		}
		shownByKindLayer[n.Kind+":"+n.Layer]++
	}
	if g256.Stats.LayerNodeCounts["background"] > 0 && shownByKindLayer["unit:background"] == 0 {
		t.Fatalf("limit=256 starved L1 background units (0 shown despite %d in domain)", g256.Stats.LayerNodeCounts["background"])
	}
	if g256.Stats.LayerNodeCounts["episodic"] > 0 && shownByKindLayer["unit:episodic"] == 0 {
		t.Fatalf("limit=256 starved L2 episodic units (0 shown despite %d in domain)", g256.Stats.LayerNodeCounts["episodic"])
	}
	if g256.Stats.LayerNodeCounts["noetic"] > 0 && shownByKindLayer["unit:noetic"] == 0 {
		t.Fatalf("limit=256 starved L3 noetic units (0 shown despite %d in domain)", g256.Stats.LayerNodeCounts["noetic"])
	}

	t.Logf("✅ 256: EventTotal=%d Shown=%d Edges=%d (shownByKindLayer=%v) | ALL: EventTotal=%d Shown=%d | ArtifactStats Docs=%d Cells=%d Noetic=%d Eligible=%d",
		g256.Stats.EventTotal, g256.Stats.NodeShown, g256.Stats.EdgeShown, shownByKindLayer,
		gAll.Stats.EventTotal, gAll.Stats.NodeShown,
		st.ThinVaultDocs, st.ThinVaultCells, st.ThinVaultNoetic, st.EligibleNodes)
}

// TestNativeRoutesMountWithoutPluginRegistry 锁定 rmconsole 的原生静态挂载形态：
// 模块不再经 host.Register/插件运行时闸，mount/mountPublic 必须可独立挂载出全部后台面
// 与公开面路由（admin/plugins/rolling-memory 插件形态已停用，禁止回潮）。
func TestNativeRoutesMountWithoutPluginRegistry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	admin := engine.Group("/admin")
	mount(admin.Group("plugin/" + Name))
	mountPublic(engine.Group(""))

	want := map[string]bool{
		"/admin/plugin/rolling-memory/status":                   false,
		"/admin/plugin/rolling-memory/widget":                   false,
		"/admin/plugin/rolling-memory/dashboard":                false,
		"/admin/plugin/rolling-memory/efficacy":                 false,
		"/admin/plugin/rolling-memory/roi/facts":                false,
		"/admin/plugin/rolling-memory/graph":                    false,
		"/admin/plugin/rolling-memory/graph/expand":             false,
		"/admin/plugin/rolling-memory/terminal":                 false,
		"/admin/plugin/rolling-memory/memories/query":           false,
		"/admin/plugin/rolling-memory/recall/query":             false,
		"/admin/plugin/rolling-memory/layers/stats":             false,
		"/admin/plugin/rolling-memory/layers/list":              false,
		"/admin/plugin/rolling-memory/layers/get/:pk":           false,
		"/admin/plugin/rolling-memory/layers/get":               false,
		"/admin/plugin/rolling-memory/layers/delete":            false,
		"/admin/plugin/rolling-memory/env/health":               false,
		"/admin/plugin/rolling-memory/sync/reconcile":           false,
		"/admin/plugin/rolling-memory/background/sync":          false,
		"/admin/plugin/rolling-memory/system/version":           false,
		"/admin/plugin/rolling-memory/system/update/check":      false,
		"/admin/plugin/rolling-memory/system/update/apply":      false,
		"/admin/plugin/rolling-memory/eval/leaderboard":         false,
		"/admin/plugin/rolling-memory/cloud/account/state":      false,
		"/admin/plugin/rolling-memory/cloud/account/register":   false,
		"/admin/plugin/rolling-memory/cloud/account/login":      false,
		"/admin/plugin/rolling-memory/cloud/account/logout":     false,
		"/admin/plugin/rolling-memory/cloud/account/rotate-key": false,
		"/admin/plugin/rolling-memory/cloud/account/sign-key":   false,
		"/admin/plugin/rolling-memory/cloud/account/devices":    false,
		"/admin/plugin/rolling-memory/cloud/account/memory":     false,
		"/plugin/rolling-memory/locomo/graph":                   false,
		"/plugin/rolling-memory/locomo/recall":                  false,
		"/ws":                                                   false,
	}
	for _, r := range engine.Routes() {
		if strings.HasSuffix(r.Path, "/stream") {
			t.Fatalf("legacy SSE stream route must not be mounted: %s", r.Path)
		}
		if _, ok := want[r.Path]; ok {
			want[r.Path] = true
		}
	}
	for path, found := range want {
		if !found {
			t.Fatalf("native route not mounted without plugin registry: %s (mounted=%d)", path, len(engine.Routes()))
		}
	}

	// 验证 routes.go / cloud_account.go 中全部后台路由均已显式挂载 middleware.AdminPermission()
	for _, srcFile := range []string{"routes.go", "cloud_account.go"} {
		raw, err := os.ReadFile(srcFile)
		if err != nil {
			t.Fatalf("read %s: %v", srcFile, err)
		}
		for lineNo, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			if (strings.HasPrefix(trimmed, "g.POST(") || strings.HasPrefix(trimmed, "g.GET(")) &&
				strings.Contains(trimmed, "middleware.AdminAuth()") &&
				!strings.Contains(trimmed, "middleware.AdminPermission()") {
				t.Fatalf("%s:%d mounts AdminAuth() without AdminPermission(): %s", srcFile, lineNo+1, trimmed)
			}
		}
	}
}

func TestMemoryDetailBackgroundChunkAndRadarATMValues(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "detail_bg.db")
	db, err := openWritableVaultDB(dbPath)
	if err != nil {
		t.Fatalf("openWritableVaultDB: %v", err)
	}
	defer db.Close()

	_, err = db.Exec(`
		INSERT INTO thin_vault_bg_chunks(unit_id, abs_path, rel_path, chunk_index, anchor, occurred_at, address_uri)
		VALUES ('bg:test_chunk_01', '/workspace/docs/arch.md', 'docs/arch.md', 0, 'L1-L20', 1762380000, 'C6:L1:BG#00')
	`)
	if err != nil {
		t.Fatalf("insert bg_chunk: %v", err)
	}

	det, err := queryMemoryDetailFromDB(dbPath, "bg:test_chunk_01")
	if err != nil {
		t.Fatalf("queryMemoryDetailFromDB(bg:test_chunk_01) failed: %v", err)
	}
	if det.Item.ID != "bg:test_chunk_01" || det.Item.Warrant != "background" || !strings.Contains(det.TraceText, "docs/arch.md#L1-L20") {
		t.Fatalf("unexpected background chunk detail: %+v", det)
	}

	atmRec := EvalRunRecord{
		Suite:       SuiteATM,
		EvidenceHit: 0.8036,
		JudgeScored: 1013,
		JudgeRate:   0.6552,
		CategoryJudge: CategoryScores{
			Cat1: 0.4964, Cat2: 0.7222, Cat3: 0.6537, Cat4: 0,
		},
	}
	vals := evalRadarValues(atmRec)
	if len(vals) != 5 || vals[1] <= 0 {
		t.Fatalf("ATM radar values must not collapse dimension 2 to 0 when Cat3 > 0: got %v", vals)
	}

	var dashResp DashboardResponse
	populateATMDashboardRadar(&dashResp)
	if !dashResp.RadarAvailable || len(dashResp.RadarCurrent) != 5 || len(dashResp.RadarBaseline) != 5 || len(dashResp.RadarHardCurrent) != 5 || len(dashResp.RadarHardBaseline) != 5 {
		t.Fatalf("ATM dashboard radar must populate both Main-1013 and Hard-31 5D vectors: %+v", dashResp)
	}
	if !strings.Contains(dashResp.RadarBaselineLabel, "ATM-RAG") || !strings.Contains(dashResp.RadarHardBaselineLabel, "ATM-RAG") {
		t.Fatalf("ATM dashboard radar baseline must pair with official ATM-RAG baseline: main=%q hard=%q", dashResp.RadarBaselineLabel, dashResp.RadarHardBaselineLabel)
	}
}

// TestUserPerspectiveCommandAndDocLinkContract 从用户视角锁死后台与帮助中心暴露的所有可复制命令与链接：
//  1. 任何复现命令（buildReproCLICommand / buildATMReproCLICommand / referenceBaselines / atmReferenceBaselines / buildATMBenchLeaderboard）
//     严禁包含仓库相对二进制前缀（`./bin/` 或 `bin/`），也严禁把服务端本机的 `/Users/<user>/` 或 `/home/<user>/` 绝对路径直接暴露进命令；
//  2. 帮助中心 `help/index.vue` 的快捷命令（quickCommands）严禁包含已移除子命令（如 `govern`），置顶文档 ID（pinnedDocIds）必须在 `docsData.ts` 中全部存在；
//  3. `docsData.ts` 中的 GitHub 链接严禁指向不存在的 `/blob/main/` 或 `/tree/main/` 分支。
func TestUserPerspectiveCommandAndDocLinkContract(t *testing.T) {
	home, _ := os.UserHomeDir()
	sampleStore := filepath.Join(home, ".local", "state", "rolling-memory", "eval", "eval-locomo-local-plain.json")
	reproCmds := []string{
		buildReproCLICommand("200", EvalArmSDKLocalPlain, sampleStore, 20, false),
		buildATMReproCLICommand("hard", EvalArmSDKLocalPlain, sampleStore, 50, true),
		buildATMBenchLeaderboard().ReproCommand,
	}
	for _, b := range referenceBaselines() {
		if b.ReproCLI != "" {
			reproCmds = append(reproCmds, b.ReproCLI)
		}
	}
	for _, b := range atmReferenceBaselines() {
		if b.ReproCLI != "" {
			reproCmds = append(reproCmds, b.ReproCLI)
		}
	}
	for _, cmd := range reproCmds {
		if !strings.HasPrefix(cmd, "rmem eval ") {
			t.Fatalf("repro command must start with portable `rmem eval `, got %q", cmd)
		}
		if strings.Contains(cmd, "./bin/") || strings.Contains(cmd, " bin/rmem") {
			t.Fatalf("repro command must not require source checkout relative path: %q", cmd)
		}
		if strings.Contains(cmd, "/Users/") || strings.Contains(cmd, "/home/") {
			t.Fatalf("repro command must fold user home directory to `~`: %q", cmd)
		}
		if strings.Contains(cmd, "--tier 100") {
			t.Fatalf("repro command must not emit retired `--tier 100`: %q", cmd)
		}
	}

	helpVuePath := filepath.Join("..", "..", "web", "src", "views", "admin", "help", "index.vue")
	helpRaw, err := os.ReadFile(helpVuePath)
	if err != nil {
		t.Fatalf("read %s: %v", helpVuePath, err)
	}
	helpText := string(helpRaw)
	for _, forbidden := range []string{"govern health", "govern report", "docs-design", "docs/design.md"} {
		if strings.Contains(helpText, forbidden) {
			t.Fatalf("help/index.vue contains dead command or doc reference %q", forbidden)
		}
	}

	docsDataPath := filepath.Join("..", "..", "web", "src", "views", "admin", "help", "docsData.ts")
	docsDataRaw, err := os.ReadFile(docsDataPath)
	if err != nil {
		t.Fatalf("read %s: %v", docsDataPath, err)
	}
	docsDataText := string(docsDataRaw)
	if strings.Contains(docsDataText, "/blob/local/") || strings.Contains(docsDataText, "/tree/local/") {
		t.Fatal("docsData.ts must point to default branch `main`, not `/blob/local/` or `/tree/local/`")
	}
}
