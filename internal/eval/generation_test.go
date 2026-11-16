// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package eval

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

// bindCurrentGeneration 让引擎绑定到一个新分配的世代 scope（模拟「本次评测的世代」），
// 便于把固定基名 scope / 其它世代当作候选清理对象。
func bindCurrentGeneration(t *testing.T, engine *CloudEngine) EvalGeneration {
	t.Helper()
	gen := mustAllocateGeneration(t, engine.Suite, engine.StorePath)
	if err := engine.BindScope(gen.Scope); err != nil {
		t.Fatalf("bind current generation: %v", err)
	}
	return gen
}

// isolateEvalStateDir 把世代租约目录锁进临时 state dir：用例绝不写真实用户状态目录，
// 也不受真实目录里的历史世代影响。
func isolateEvalStateDir(t *testing.T) string {
	t.Helper()
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("HOME", stateHome)
	return filepath.Join(stateHome, "rolling-memory", "eval", "generations")
}

func mustAllocateGeneration(t *testing.T, suite, storePath string) EvalGeneration {
	t.Helper()
	gen, err := AllocateEvalGeneration(testConfig(t), suite, storePath)
	if err != nil {
		t.Fatalf("AllocateEvalGeneration: %v", err)
	}
	return gen
}

// TestFixEvalGenerationIsolation 验证：世代隔离、所有权校验与并发冲突修复。
func TestFixEvalGenerationIsolation(t *testing.T) {
	isolateEvalStateDir(t)
	server := newCloudTestServer(t)
	vault := filepath.Join(t.TempDir(), "vault.json")

	// 验证 1: 必须显式区分模式（默认值改为 local_plain，避免误调用远程）
	cfg := testConfig(t)
	if _, err := NewCloudEngine(cfg, SuiteLoCoMo, vault, WithPulseMode("invalid")); err == nil {
		t.Fatal("invalid pulse mode must be rejected")
	}

	// 验证 2: 租约所有权与并发保护（跨进程/跨世代互斥）
	engine := newTestCloudEngineAt(t, server, SuiteLoCoMo, vault)

	// 模拟已存在活跃写手
	busy := EvalGeneration{
		Scope: "busy", Suite: SuiteLoCoMo, UserID: "10", Endpoint: engine.Endpoint,
		StorePath: vault, Role: evalGenerationRoleWriter, PID: 12345, // 模拟一个存活进程
		HeartbeatAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := SaveEvalGeneration(busy); err != nil {
		t.Fatal(err)
	}

	_, err := AllocateEvalGeneration(engine.cfg, SuiteLoCoMo, vault)
	if err == nil || !strings.Contains(err.Error(), "活跃世代") {
		t.Fatalf("写手冲突必须报错，got: %v", err)
	}
}

// ageEvalGeneration 把租约心跳改成「早已过期」，模拟已结束 / 崩溃的旧世代。
func ageEvalGeneration(t *testing.T, gen EvalGeneration, age time.Duration) EvalGeneration {
	t.Helper()
	loaded, ok := LoadEvalGeneration(gen.Scope)
	if !ok {
		t.Fatalf("generation lease %s not found", gen.Scope)
	}
	loaded.HeartbeatAt = time.Now().Add(-age).UTC().Format(time.RFC3339)
	loaded.FinishedAt = loaded.HeartbeatAt
	loaded.Role = evalGenerationRoleIdle
	if err := SaveEvalGeneration(loaded); err != nil {
		t.Fatalf("save aged generation: %v", err)
	}
	return loaded
}

// injectScopeUnit 直接往假引擎的某个 scope 里塞一条 Unit（模拟外部写入 / 历史残留）。
func injectScopeUnit(server *cloudTestServer, scope, unitID string) {
	server.mu.Lock()
	defer server.mu.Unlock()
	server.ingestsByScope[scope] = append(server.ingestsByScope[scope],
		ramesdk.BlindPulsePacket{UnitID: unitID, Scope: scope})
}

// TestSplitEvalScopeGenerationForm 钉住 scope 形态闭集：
//
//	<user>-mem-eval-<locomo|atmbench>[-<世代号>]
//
// 世代号（每次新评测自动分配）必须被接受；历史固定 scope 兼容；停用形态一律拒绝。
func TestSplitEvalScopeGenerationForm(t *testing.T) {
	cases := []struct {
		scope      string
		wantSuite  string
		wantGen    string
		wantAccept bool
	}{
		{"u10-mem-eval-locomo", SuiteLoCoMo, "", true},
		{"u10-mem-eval-atmbench", SuiteATM, "", true},
		{"u10-mem-eval-locomo-pulse-plain", SuiteLoCoMo, "", true},
		{"u10-mem-eval-atmbench-pulse-plain", SuiteATM, "", true},
		{"u10-mem-eval-locomo-local-plain", SuiteLoCoMo, "", true},
		{"u10-mem-eval-atmbench-local-plain", SuiteATM, "", true},
		{"u10-mem-eval-locomo-20261107t153012z-7f3c", SuiteLoCoMo, "20261107t153012z-7f3c", true},
		{"u10-mem-eval-atmbench-20261107t153012z-7f3c", SuiteATM, "20261107t153012z-7f3c", true},
		{"u10-mem-eval-locomo-pulse-plain-20261107t153012z-7f3c", SuiteLoCoMo, "20261107t153012z-7f3c", true},
		{"u10-mem-eval-atmbench-pulse-plain-20261107t153012z-7f3c", SuiteATM, "20261107t153012z-7f3c", true},
		{"u10-mem-eval-locomo-local-plain-20261107t153012z-7f3c", SuiteLoCoMo, "20261107t153012z-7f3c", true},
		{"u10-mem-eval-atmbench-local-plain-20261107t153012z-7f3c", SuiteATM, "20261107t153012z-7f3c", true},
		{"user-mem-eval-locomo-gen1", SuiteLoCoMo, "gen1", true},
		{"u10-mem-eval-locomo-", "", "", false},
		{"u10-mem-eval-atmbenchx", "", "", false},
		{"mem-eval-u10-locomo", "", "", false},
		{"mem-eval-u10-full-locomo-100", "", "", false},
		{"-mem-eval-locomo", "", "", false},
		{"mem:self", "", "", false},
		{"default", "", "", false},
		{"u10-mem-eval-locomo-2026 1107", "", "", false},
		{"u10-mem-eval-locomo-x:", "", "", false},
	}
	for _, testCase := range cases {
		suite, generation, ok := SplitEvalScope(testCase.scope)
		if ok != testCase.wantAccept || suite != testCase.wantSuite || generation != testCase.wantGen {
			t.Errorf("SplitEvalScope(%q) = (%q, %q, %v)，期望 (%q, %q, %v)",
				testCase.scope, suite, generation, ok, testCase.wantSuite, testCase.wantGen, testCase.wantAccept)
		}
		if got := IsCloudEvalScope(testCase.scope); got != testCase.wantAccept {
			t.Errorf("IsCloudEvalScope(%q) = %v，期望 %v", testCase.scope, got, testCase.wantAccept)
		}
	}
	if got := ScopeForGeneration(SuiteLoCoMo, "10", "20261107t153012z-7f3c"); got != "u10-mem-eval-locomo-20261107t153012z-7f3c" {
		t.Fatalf("ScopeForGeneration = %q", got)
	}
	if got := ScopeForGeneration(SuiteATM, "10", ""); got != ATMScope {
		t.Fatalf("空世代号必须退化为固定 scope，得到 %q", got)
	}
}

// TestEvalGenerationIDsAreUnique 世代号 = UTC 时间戳 + 随机后缀。后缀只有 2 字节（16 bit），
// 同一秒内抽样存在生日界内的撞名概率（64 次同秒抽样约 3% 会撞），因此这里只锁两件事：
// ① 时间戳位与格式正确；② 后缀确实来自随机源（同一时刻取样不得恒定）。
// 「绝不撞名」的保证在分配器侧：AllocateEvalGenerationForMode 对已存在的 scope 重试换号，
// 由 TestEvalAllocateRejectsLiveWriterOnSameStore 与 TestEvalPrepareOpensFreshGenerationScope 覆盖。
func TestEvalGenerationIDsAreUnique(t *testing.T) {
	now := time.Date(2026, 11, 7, 15, 30, 12, 0, time.UTC)
	first, err := NewEvalGenerationID(now)
	if err != nil {
		t.Fatalf("NewEvalGenerationID: %v", err)
	}
	body, suffix, ok := strings.Cut(first, "-")
	if !ok || body != "20261107t153012z" || len(suffix) != 4 || strings.Trim(suffix, "0123456789abcdef") != "" {
		t.Fatalf("世代号格式错误：%s", first)
	}
	distinct := map[string]bool{}
	for index := 0; index < 8; index++ {
		id, err := NewEvalGenerationID(now)
		if err != nil {
			t.Fatalf("NewEvalGenerationID: %v", err)
		}
		distinct[id] = true
	}
	if len(distinct) < 2 {
		t.Fatalf("同一时刻的后缀恒定，随机源失效：%v", distinct)
	}
}

// TestEvalAllocateRejectsLiveWriterOnSameStore 同一 store 上的**别的活进程**写手是写-写冲突：
// 必须显式拒绝（换 store 或等待），不得静默让两个 ingest 互踩同一份明文金库；
// 进程已退出（或心跳过期）的租约不构成冲突。
func TestEvalAllocateRejectsLiveWriterOnSameStore(t *testing.T) {
	isolateEvalStateDir(t)
	server := newCloudTestServer(t)
	_ = newTestCloudEngine(t, server, SuiteLoCoMo) // 固定端点与凭据环境
	cfg := testConfig(t)
	storePath := filepath.Join(t.TempDir(), "vault.json")

	// 造一个「另一个活进程」的写手租约（PID 1 恒存在），必须是冲突。
	foreign := mustAllocateGeneration(t, SuiteLoCoMo, storePath)
	foreign.PID = 1
	foreign.Role = evalGenerationRoleWriter
	if err := SaveEvalGeneration(foreign); err != nil {
		t.Fatalf("save foreign lease: %v", err)
	}
	if _, err := AllocateEvalGeneration(cfg, SuiteLoCoMo, storePath); err == nil {
		t.Fatal("活写手在用时，第二个 ingest 必须被拒绝")
	}
	// 独立 store 不受影响（并发评测各有独立 store 与世代 scope）。
	if _, err := AllocateEvalGeneration(cfg, SuiteLoCoMo, filepath.Join(t.TempDir(), "other.json")); err != nil {
		t.Fatalf("独立 store 的分配不应被拒绝: %v", err)
	}
	// 进程已退出（heartbeat 新鲜但 PID 不存在）⇒ 不冲突：一次 rebuild 结束后必须能立刻再 rebuild。
	dead := mustAllocateGeneration(t, SuiteLoCoMo, filepath.Join(t.TempDir(), "dead.json"))
	child := exec.Command("sh", "-c", "exit 0")
	if err := child.Run(); err == nil && child.Process != nil {
		dead.PID = child.Process.Pid // 该进程已退出
		if err := SaveEvalGeneration(dead); err != nil {
			t.Fatalf("save dead lease: %v", err)
		}
		if _, err := AllocateEvalGeneration(cfg, SuiteLoCoMo, dead.StorePath); err != nil {
			t.Fatalf("已退出进程的租约不应阻塞重建: %v", err)
		}
	} else {
		t.Logf("跳过「已退出进程」子用例（无法派生子进程：%v）", err)
	}
	// 心跳过期 ⇒ 同样不阻塞。
	ageEvalGeneration(t, foreign, time.Hour)
	if _, err := AllocateEvalGeneration(cfg, SuiteLoCoMo, storePath); err != nil {
		t.Fatalf("过期写手的 store 应可重新分配: %v", err)
	}
}

// TestEvalReconcilePurgesIdleRetiredGenerations 清旧记忆的唯一判据是「心跳过期」：
// 活跃世代一律跳过；过期世代按 /units 权威全集清空（云端 + 本地金库）并留回执。
func TestEvalReconcilePurgesIdleRetiredGenerations(t *testing.T) {
	isolateEvalStateDir(t)
	server := newCloudTestServer(t)
	vault := filepath.Join(t.TempDir(), "vault.json")
	engine := newTestCloudEngineAt(t, server, SuiteLoCoMo, vault)
	bindCurrentGeneration(t, engine)

	retired := mustAllocateGeneration(t, SuiteLoCoMo, vault)
	retiredEngine, err := NewCloudEngineForScope(engine.cfg, retired.Scope, vault)
	if err != nil {
		t.Fatalf("bind retired generation: %v", err)
	}
	documents := []CloudDocument{
		{ID: "old-a", Text: "alpha old memory", OccurredAt: 1},
		{ID: "old-b", Text: "beta old memory", OccurredAt: 2},
	}
	if _, err := retiredEngine.Ingest(context.Background(), documents, false, 2, nil); err != nil {
		t.Fatalf("seed retired generation: %v", err)
	}

	// 心跳新鲜：必须原样保留（并发评测互不删库）。
	if receipts := engine.ReconcileRetiredGenerations(context.Background(), nil); len(receipts) != 0 {
		t.Fatalf("活跃世代不得被回收: %+v", receipts)
	}
	if ids := server.scopeUnitIDs(retired.Scope); len(ids) != 2 {
		t.Fatalf("活跃世代云端数据不应被删: %v", ids)
	}

	// 心跳过期：回收云端 + 本地明文，摘除租约，回执记录条数。
	ageEvalGeneration(t, retired, time.Hour)
	receipts := engine.ReconcileRetiredGenerations(context.Background(), nil)
	if len(receipts) != 1 || receipts[0].Scope != retired.Scope || receipts[0].Units != 2 || receipts[0].Error != "" {
		t.Fatalf("过期世代回收回执不符: %+v", receipts)
	}
	if ids := server.scopeUnitIDs(retired.Scope); len(ids) != 0 {
		t.Fatalf("过期世代云端残留: %v", ids)
	}
	if _, ok := LoadEvalGeneration(retired.Scope); ok {
		t.Fatal("回收成功后必须摘除租约，避免重复清理")
	}
	if _, _, _, err := engine.Store.LoadLocalDocument("old-a"); err == nil {
		t.Fatal("回收必须同步删除本地明文金库里的旧世代文档")
	}
	raw, err := os.ReadFile(vault)
	if err != nil {
		t.Fatalf("read vault: %v", err)
	}
	if strings.Contains(string(raw), "old-a") || strings.Contains(string(raw), "old-b") {
		t.Fatal("本地金库落盘内容仍含旧世代明文")
	}
}

// TestEvalReconcileClearsAgedLegacyBaseScopeResidue 旧固定 scope（历史形态）一旦被登记
// （adopt / ingest 会补建租约），其残留就进入世代回收体系：心跳过期后按 /units 全集清空。
// 反之「无租约」不再被推断为孤儿——那可能正被其他评测进程使用（见 generation.go）。
func TestEvalReconcileClearsAgedLegacyBaseScopeResidue(t *testing.T) {
	isolateEvalStateDir(t)
	server := newCloudTestServer(t)
	vault := filepath.Join(t.TempDir(), "vault.json")
	engine := newTestCloudEngineAt(t, server, SuiteLoCoMo, vault)
	bindCurrentGeneration(t, engine)

	base := ScopeForSuite(SuiteLoCoMo, "10")
	legacyEngine, err := NewCloudEngineForScope(engine.cfg, base, vault)
	if err != nil {
		t.Fatalf("bind legacy base scope: %v", err)
	}
	legacyDocuments := []CloudDocument{{ID: "legacy-a", Text: "legacy residue", OccurredAt: 1}}
	if _, err := legacyEngine.Ingest(context.Background(), legacyDocuments, false, 1, nil); err != nil {
		t.Fatalf("seed legacy scope: %v", err)
	}
	if ids := server.scopeUnitIDs(base); len(ids) != 1 {
		t.Fatalf("legacy 语料未写入: %v", ids)
	}

	// 心跳新鲜：即使在重建途中也不得被回收（并发评测互不删库的硬保证）。
	if receipts := engine.ReconcileRetiredGenerations(context.Background(), nil); len(receipts) != 0 {
		t.Fatalf("心跳新鲜的旧固定 scope 不得被回收: %+v", receipts)
	}
	if ids := server.scopeUnitIDs(base); len(ids) != 1 {
		t.Fatalf("活跃旧固定 scope 云端数据不应被删: %v", ids)
	}

	// 心跳过期：进入回收体系，清空云端并摘除租约。
	if gen, ok := LoadEvalGeneration(base); !ok {
		t.Fatal("ingest 必须为旧固定 scope 补建租约（回收体系的入口）")
	} else {
		ageEvalGeneration(t, gen, time.Hour)
	}
	receipts := engine.ReconcileRetiredGenerations(context.Background(), nil)
	if len(receipts) != 1 || receipts[0].Scope != base || receipts[0].Units != 1 || receipts[0].Error != "" {
		t.Fatalf("过期旧固定 scope 回收回执不符: %+v", receipts)
	}
	if ids := server.scopeUnitIDs(base); len(ids) != 0 {
		t.Fatalf("旧固定 scope 仍有残留: %v", ids)
	}
	if _, ok := LoadEvalGeneration(base); ok {
		t.Fatal("回收成功后必须摘除租约")
	}
}

// TestEvalPrepareOpensFreshGenerationScope 每次「新评测」都自动分配独立世代 scope：
// 语料只落新世代；固定基名 scope 不再被写入；旧世代（心跳过期）在新评测时被回收，
// 而心跳新鲜的上一代不受影响（并发安全）。
func TestEvalPrepareOpensFreshGenerationScope(t *testing.T) {
	isolateEvalStateDir(t)
	server := newCloudTestServer(t)
	vault := filepath.Join(t.TempDir(), "vault.json")
	engine := newTestCloudEngineAt(t, server, SuiteLoCoMo, vault)

	documents := []CloudDocument{
		{ID: "gen-a", Text: "alpha memory", OccurredAt: 1},
		{ID: "gen-b", Text: "beta memory", OccurredAt: 2},
	}
	first, err := engine.Prepare(context.Background(), documents, true, false, 2, nil)
	if err != nil {
		t.Fatalf("first prepare: %v", err)
	}
	base := ScopeForSuite(SuiteLoCoMo, "10")
	if first.Scope == base || first.Generation == "" {
		t.Fatalf("新评测必须落在独立世代 scope：%+v", first)
	}
	if suite, generation, ok := SplitEvalScope(first.Scope); !ok || suite != SuiteLoCoMo || generation != first.Generation {
		t.Fatalf("世代 scope 形态不符：%q gen=%q", first.Scope, first.Generation)
	}
	if ids := server.scopeUnitIDs(first.Scope); len(ids) != len(documents) {
		t.Fatalf("语料未写入世代 scope: %v", ids)
	}
	if ids := server.scopeUnitIDs(base); len(ids) != 0 {
		t.Fatalf("新架构不得再写固定基名 scope: %v", ids)
	}
	firstLease, ok := LoadEvalGeneration(first.Scope)
	if !ok || firstLease.Role != evalGenerationRoleWriter || firstLease.CorpusSHA256 != first.CorpusSHA256 {
		t.Fatalf("世代租约未登记: %+v ok=%v", firstLease, ok)
	}

	// 造一个上一代：已结束且心跳过期，带 2 条旧记忆。
	retiredVault := filepath.Join(t.TempDir(), "retired.json")
	retired := mustAllocateGeneration(t, SuiteLoCoMo, retiredVault)
	retiredEngine, err := NewCloudEngineForScope(engine.cfg, retired.Scope, retiredVault)
	if err != nil {
		t.Fatalf("bind retired generation: %v", err)
	}
	if _, err := retiredEngine.Ingest(context.Background(), documents, false, 2, nil); err != nil {
		t.Fatalf("seed retired generation: %v", err)
	}
	ageEvalGeneration(t, retired, time.Hour)

	second, err := engine.Prepare(context.Background(), documents, true, false, 2, nil)
	if err != nil {
		t.Fatalf("second prepare: %v", err)
	}
	if second.Scope == first.Scope || second.Scope == retired.Scope || second.Generation == "" {
		t.Fatalf("第二次新评测必须换新世代: %+v", second)
	}
	if len(second.RetiredScopes) != 1 || second.RetiredScopes[0].Scope != retired.Scope || second.RetiredScopes[0].Units != 2 {
		t.Fatalf("清旧记忆回执缺失: %+v", second.RetiredScopes)
	}
	if ids := server.scopeUnitIDs(retired.Scope); len(ids) != 0 {
		t.Fatalf("旧世代残留未清: %v", ids)
	}
	if _, ok := LoadEvalGeneration(retired.Scope); ok {
		t.Fatal("旧世代租约未被摘除")
	}
	if ids := server.scopeUnitIDs(first.Scope); len(ids) != len(documents) {
		t.Fatalf("心跳新鲜的上一代被误清（并发会打架）: %v", ids)
	}
}

// TestEvalPrepareRebuildsWhenGenerationEvictedElsewhere 世代被别处清空（引擎重置 / 外部清库）时，
// 复用路径不得返回「documents=N 而云端为 0」的假回执，也不得因本地金库封条完整而跳过重灌。
func TestEvalPrepareRebuildsWhenGenerationEvictedElsewhere(t *testing.T) {
	isolateEvalStateDir(t)
	server := newCloudTestServer(t)
	vault := filepath.Join(t.TempDir(), "vault.json")
	engine := newTestCloudEngineAt(t, server, SuiteLoCoMo, vault)

	documents := []CloudDocument{
		{ID: "evict-a", Text: "alpha memory", OccurredAt: 1},
		{ID: "evict-b", Text: "beta memory", OccurredAt: 2},
	}
	first, err := engine.Prepare(context.Background(), documents, true, false, 2, nil)
	if err != nil {
		t.Fatalf("first prepare: %v", err)
	}
	// 模拟外部清库（或引擎重置）：该世代云端为空。
	server.mu.Lock()
	server.ingestsByScope[first.Scope] = nil
	server.mu.Unlock()

	manifest, err := engine.Prepare(context.Background(), documents, false, true, 2, nil)
	if err != nil {
		t.Fatalf("世代被清空后必须自动重建，而不是 fail-closed: %v", err)
	}
	if manifest.Scope == first.Scope {
		t.Fatal("被清空的世代不得复用，必须新开世代")
	}
	if ids := server.scopeUnitIDs(manifest.Scope); len(ids) != len(documents) {
		t.Fatalf("重建世代语料不完整: %v", ids)
	}
}

// TestEvalPrepareKeepsBlindReingestClosed 世代在用且远端与语料不一致时（语料变了 / 有外来 Unit /
// 传输异常），复用必须 fail-closed——绝不把「不一致」当成「空库」盲灌。
func TestEvalPrepareKeepsBlindReingestClosed(t *testing.T) {
	isolateEvalStateDir(t)
	server := newCloudTestServer(t)
	vault := filepath.Join(t.TempDir(), "vault.json")
	engine := newTestCloudEngineAt(t, server, SuiteLoCoMo, vault)

	documents := []CloudDocument{{ID: "keep-a", Text: "alpha memory", OccurredAt: 1}}
	prepared, err := engine.Prepare(context.Background(), documents, true, false, 1, nil)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	// 同一世代里出现一条外来 Unit（人工 ingest / 历史残留）⇒ 远端全集与语料不再相等。
	injectScopeUnit(server, prepared.Scope, "keep-foreign")
	if _, err := engine.Prepare(context.Background(), documents, false, true, 1, nil); err == nil {
		t.Fatalf("远端含外来 Unit 时不得复用（scope=%s）", prepared.Scope)
	}
}

// TestEvalReconcileNeverTouchesUnregisteredScopes 回收只认本地租约：
// **没有租约的 scope（可能是其他进程正在写的固定 scope）一律不碰**——哪怕它云端非空。
func TestEvalReconcileNeverTouchesUnregisteredScopes(t *testing.T) {
	isolateEvalStateDir(t)
	server := newCloudTestServer(t)
	vault := filepath.Join(t.TempDir(), "vault.json")
	engine := newTestCloudEngineAt(t, server, SuiteLoCoMo, vault)
	bindCurrentGeneration(t, engine)

	// 一个「别处在用」的固定 scope：云端有数据，但本地没有任何租约登记。
	foreign := ScopeForSuite(SuiteLoCoMo, "10")
	injectScopeUnit(server, foreign, "foreign-a")
	injectScopeUnit(server, foreign, "foreign-b")
	// 另一个套件里同样无登记的 scope。
	foreignATM := ScopeForSuite(SuiteATM, "10")
	injectScopeUnit(server, foreignATM, "foreign-atm")

	receipts := engine.ReconcileRetiredGenerations(context.Background(), nil)
	if len(receipts) != 0 {
		t.Fatalf("无租约的 scope 不得进入回收: %+v", receipts)
	}
	if ids := server.scopeUnitIDs(foreign); len(ids) != 2 {
		t.Fatalf("无租约的固定 scope 被误删: %v", ids)
	}
	if ids := server.scopeUnitIDs(foreignATM); len(ids) != 1 {
		t.Fatalf("无租约的 ATM scope 被误删: %v", ids)
	}
}

// TestEvalGenerationLeaseRoundTrip 租约读写与角色语义：读者续期不得把写手降级。
func TestEvalGenerationLeaseRoundTrip(t *testing.T) {
	root := isolateEvalStateDir(t)
	server := newCloudTestServer(t)
	_ = newTestCloudEngine(t, server, SuiteLoCoMo)
	gen := mustAllocateGeneration(t, SuiteLoCoMo, filepath.Join(t.TempDir(), "vault.json"))
	path := filepath.Join(root, "locomo", gen.Scope+".json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("租约文件未落盘 %s: %v", path, err)
	}
	writer, ok := LoadEvalGeneration(gen.Scope)
	if !ok || writer.Role != evalGenerationRoleWriter {
		t.Fatalf("租约初值不符: %+v ok=%v", writer, ok)
	}
	touchEvalGeneration(gen.Scope, evalGenerationRoleReader)
	loaded, _ := LoadEvalGeneration(gen.Scope)
	if !loaded.live(time.Now()) {
		t.Fatalf("续期后必须判为活跃: %+v", loaded)
	}
	if loaded.Role != evalGenerationRoleWriter {
		t.Fatalf("写手角色被读者降级: %+v", loaded)
	}
	// 读者形态的世代（无写手）可被读者角色标注。
	reader := mustAllocateGeneration(t, SuiteLoCoMo, filepath.Join(t.TempDir(), "reader.json"))
	idle := reader
	idle.Role = evalGenerationRoleIdle
	_ = SaveEvalGeneration(idle)
	touchEvalGeneration(reader.Scope, evalGenerationRoleReader)
	if again, _ := LoadEvalGeneration(reader.Scope); again.Role != evalGenerationRoleReader {
		t.Fatalf("读者续期未更新角色: %+v", again)
	}
}
