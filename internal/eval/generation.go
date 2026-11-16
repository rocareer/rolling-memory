// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package eval

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/rocareer/rolling-memory/internal/config"
	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

// 评测世代（generation）租约与回收。
//
// 第一性事实：同一套件的两次评测如果共用同一个云端 scope，第二次的「重算记忆」就会把第一次
// 正在用的语料整库清掉（或把两份语料叠在同一个 scope 里互相污染检索）——这就是「打架」。
// 因此每次**新评测**都自动分配一个独立世代 scope：
//
//	<authorized-user>-mem-eval-<suite>-<世代号>    例如 u10-mem-eval-locomo-20261107t153012z-7f3c
//
// 世代号 = UTC 时间戳 + 4 位随机十六进制；scope 仍以 <user>-mem-eval-<suite> 为前缀，
// 与引擎侧「评测命名空间」的既定形态保持一致（数据面只 trim + 191 字节列宽）。
//
// 「清旧记忆」：引擎没有枚举 scope 的公开接口，所以本地租约文件是唯一可枚举的世代真源。
// 分配新世代时，把同一 (endpoint, user, suite) 下**心跳已过期**的旧世代按 /units 权威全集
// 分批清空（云端 + 本地明文金库同步删），并顺带清掉旧固定 scope 的孤儿残留；
// **心跳新鲜的世代一律不清**——这是并发评测互不删库的硬保证。
//
// 租约文件只存身份与读数（scope / 端点 / 语料摘要 / 时间戳），零原文、零密钥。

const (
	// evalGenerationLiveTTL 是世代心跳的新鲜窗口：窗口内视为「在用」（可能是长跑评测的写手，
	// 也可能是 Admin 批次逐题读取的读者），任何回收都必须跳过；窗口外才允许清空回收。
	// 60 分钟 = 长跑评测（Main-1013）的安全上界。
	evalGenerationLiveTTL = 60 * time.Minute
	// evalGenerationAcquireAttempts 是世代号命名重试次数（时间戳 + 4 位随机，撞名概率极低）。
	evalGenerationAcquireAttempts = 3
	// evalGenerationHeartbeatEveryDocs 是 ingest 过程中续期心跳的文档间隔（并发 worker 各自续期）。
	evalGenerationHeartbeatEveryDocs = 32
	// evalGenerationRoleWriter / Reader / Idle 是租约角色：写手（ingest/清库）、读者（recall）、
	// 已结束（idle）。同一 store 上的活跃写手是写-写冲突，必须显式拒绝而不是静默互踩。
	evalGenerationRoleWriter = "writer"
	evalGenerationRoleReader = "reader"
	evalGenerationRoleIdle   = "idle"
)

// EvalGeneration 是一个评测世代的租约记录（只存身份与读数，不存原文）。
type EvalGeneration struct {
	Scope        string `json:"scope"`
	Generation   string `json:"generation"`
	Suite        string `json:"suite"`
	UserID       string `json:"user_id"`
	Endpoint     string `json:"endpoint"`
	StorePath    string `json:"store_path"`
	CorpusSHA256 string `json:"corpus_sha256,omitempty"`
	Documents    int    `json:"documents,omitempty"`
	Host         string `json:"host,omitempty"`
	PID          int    `json:"pid,omitempty"`
	OwnerToken   string `json:"owner_token,omitempty"`
	Role         string `json:"role"`
	StartedAt    string `json:"started_at"`
	HeartbeatAt  string `json:"heartbeat_at"`
	FinishedAt   string `json:"finished_at,omitempty"`
}

// EvalGenerationPurge 是一次回收回执（写进 manifest 与台账，如实记录清了什么、有没有残留）。
type EvalGenerationPurge struct {
	Scope string `json:"scope"`
	Units int    `json:"units"`
	Note  string `json:"note,omitempty"`
	Error string `json:"error,omitempty"`
}

func evalGenerationsRootDir() string {
	stateDir := strings.TrimSpace(config.DefaultStateDir())
	if stateDir == "" {
		return ""
	}
	return filepath.Join(stateDir, "eval", "generations")
}

func evalGenerationDir(suite string) string {
	root := evalGenerationsRootDir()
	if root == "" {
		return ""
	}
	return filepath.Join(root, strings.ToLower(strings.TrimSpace(suite)))
}

func evalGenerationPath(suite, scope string) string {
	dir := evalGenerationDir(suite)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, strings.ToLower(strings.TrimSpace(scope))+".json")
}

// NewEvalGenerationID 生成世代号：UTC 紧凑时间戳 + 4 位随机十六进制（可读、可排序）。后缀只有
// 16 bit，同一秒并发下存在生日界内的撞名概率；「绝不撞名」由分配器的已存在即重试守卫兜底，
// 见 AllocateEvalGenerationForMode。
func NewEvalGenerationID(now time.Time) (string, error) {
	suffix := make([]byte, 2)
	if _, err := rand.Read(suffix); err != nil {
		return "", fmt.Errorf("评测世代号随机后缀失败: %w", err)
	}
	return now.UTC().Format("20060102t150405z") + "-" + hex.EncodeToString(suffix), nil
}

// writeJSONAtomic 原子落盘（临时文件 + rename）：租约文件会被多个进程读写（评测进程 + 逐题
// recall 读者 + 回收扫描），必须避免读到半截 JSON。
func writeJSONAtomic(path string, value any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = tmp.Close(); _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(encoded); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// LoadEvalGeneration 读取某个 scope 的世代租约；不存在或不可解析时返回 ok=false。
func LoadEvalGeneration(scope string) (EvalGeneration, bool) {
	var gen EvalGeneration
	suite, ok := EvalScopeSuite(scope)
	if !ok {
		return gen, false
	}
	path := evalGenerationPath(suite, scope)
	if path == "" {
		return gen, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return gen, false
	}
	if err := json.Unmarshal(raw, &gen); err != nil || strings.TrimSpace(gen.Scope) == "" {
		return gen, false
	}
	return gen, true
}

// listEvalGenerations 枚举本地登记的全部世代（引擎无 scope 枚举接口，这里就是唯一真源）。
func listEvalGenerations() []EvalGeneration {
	root := evalGenerationsRootDir()
	if root == "" {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []EvalGeneration
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, entry.Name()))
		if err != nil {
			continue
		}
		for _, file := range files {
			if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(root, entry.Name(), file.Name()))
			if err != nil {
				continue
			}
			var gen EvalGeneration
			if err := json.Unmarshal(raw, &gen); err != nil || strings.TrimSpace(gen.Scope) == "" {
				continue
			}
			out = append(out, gen)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Scope < out[j].Scope })
	return out
}

func (g EvalGeneration) heartbeatTime() time.Time {
	for _, value := range []string{g.HeartbeatAt, g.StartedAt} {
		if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(value)); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

// live 判定：心跳在新鲜窗口内即视为「在用」，回收必须跳过。
func (g EvalGeneration) live(now time.Time) bool {
	beat := g.heartbeatTime()
	if beat.IsZero() {
		return false
	}
	return now.Sub(beat) < evalGenerationLiveTTL
}

func newEvalGeneration(cfg *config.Config, suite, storePath string, now time.Time) (EvalGeneration, error) {
	return newEvalGenerationForMode(cfg, suite, PulseModePulseCipher, storePath, now)
}

func newEvalGenerationForMode(cfg *config.Config, suite, pulseMode, storePath string, now time.Time) (EvalGeneration, error) {
	suite = strings.ToLower(strings.TrimSpace(suite))
	pulseMode = NormalizePulseMode(pulseMode)
	userID := cloudUserID()
	generationID, err := NewEvalGenerationID(now)
	if err != nil {
		return EvalGeneration{}, err
	}
	host, _ := os.Hostname()
	stamp := now.UTC().Format(time.RFC3339)
	endpoint := "local://sqlite-fts5-bm25"
	if pulseMode != PulseModeLocalPlain {
		endpoint = cloudEndpoint(cfg)
	}
	ownerTok, _ := NewEvalGenerationID(now)
	return EvalGeneration{
		Scope:       ScopeForModeGeneration(suite, userID, pulseMode, generationID),
		Generation:  generationID,
		Suite:       suite,
		UserID:      userID,
		Endpoint:    endpoint,
		StorePath:   storePath,
		Host:        host,
		PID:         os.Getpid(),
		OwnerToken:  ownerTok,
		Role:        evalGenerationRoleWriter,
		StartedAt:   stamp,
		HeartbeatAt: stamp,
	}, nil
}

// SaveEvalGeneration 落盘租约（原子写）。
func SaveEvalGeneration(gen EvalGeneration) error {
	path := evalGenerationPath(gen.Suite, gen.Scope)
	if path == "" {
		return errors.New("评测世代登记目录不可用（state dir 未解析）")
	}
	return writeJSONAtomic(path, gen)
}

// recordGenerationCorpus 把语料身份写进世代租约（心跳 + 摘要 + 条数 + 金库路径）。无租约时按其身份补建一条，
// 让历史固定 scope 的语料也进入可枚举、可回收的世代体系。
func recordGenerationCorpus(scope, storePath string, manifest CloudManifest) {
	suite, ok := EvalScopeSuite(scope)
	if !ok {
		return
	}
	gen, exists := LoadEvalGeneration(scope)
	if !exists {
		gen = EvalGeneration{Scope: scope, Suite: suite, Role: evalGenerationRoleWriter}
		gen.StartedAt = time.Now().UTC().Format(time.RFC3339)
	}
	gen.Generation = EvalScopeGeneration(scope)
	gen.UserID = firstNonEmptyText(gen.UserID, manifest.UserID)
	gen.Endpoint = firstNonEmptyText(gen.Endpoint, manifest.Endpoint)
	gen.StorePath = firstNonEmptyText(gen.StorePath, storePath)
	gen.CorpusSHA256 = manifest.CorpusSHA256
	gen.Documents = manifest.Documents
	if gen.Role == "" {
		gen.Role = evalGenerationRoleWriter
	}
	gen.HeartbeatAt = time.Now().UTC().Format(time.RFC3339)
	_ = SaveEvalGeneration(gen)
}

func firstNonEmptyText(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// touchEvalGeneration 续期心跳；role 为空时保留既有角色（读者不得把写手降级成读者）。
func touchEvalGeneration(scope, role string) {
	gen, ok := LoadEvalGeneration(scope)
	if !ok {
		if role == "" {
			return
		}
		suite, suiteOK := EvalScopeSuite(scope)
		if !suiteOK {
			return
		}
		gen = EvalGeneration{Scope: scope, Suite: suite, Role: role}
		gen.StartedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if role != "" && !(gen.Role == evalGenerationRoleWriter && role == evalGenerationRoleReader) {
		gen.Role = role
	}
	gen.HeartbeatAt = time.Now().UTC().Format(time.RFC3339)
	_ = SaveEvalGeneration(gen)
}

// processAlive 判定同机进程是否仍存在（signal 0 探活）：ESRCH / 进程已回收（os.ErrProcessDone）
// 才算死亡；其余错误——权限不足（EPERM）、平台不支持 signal——一律按「存在」保守处理：
// 宁可不清理，也不要在另一个 live 写手身上动手。
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if err := process.Signal(syscall.Signal(0)); err != nil {
		if errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrProcessDone) {
			return false
		}
		return true
	}
	return true
}

// liveWriterOnStore 判定某个 store 上是否已有别的**活着的**写手世代（写-写冲突的唯一判据）：
//   - 角色必须是 writer 且心跳新鲜（读者/已结束的世代不构成冲突）；
//   - 同机租约按进程存活判定：进程已退出（信号 0 返回 ESRCH）即不冲突——
//     否则一次 rebuild 结束后 15 分钟内心跳窗口内无法再次 rebuild；
//   - 本进程自己的租约不算冲突（同进程重开世代不与自己打架）；
//   - 跨机只能靠心跳（无法探活），窗口内保守拒绝。
func liveWriterOnStore(storePath, endpoint, userID string, now time.Time) (EvalGeneration, bool) {
	storePath = strings.TrimSpace(storePath)
	if storePath == "" {
		return EvalGeneration{}, false
	}
	host, _ := os.Hostname()
	selfPID := os.Getpid()
	for _, gen := range listEvalGenerations() {
		if strings.TrimSpace(gen.StorePath) != storePath {
			continue
		}
		if gen.Endpoint != endpoint || gen.UserID != userID {
			continue
		}
		if gen.Role != evalGenerationRoleWriter || !gen.live(now) {
			continue
		}
		if gen.Host == host {
			if gen.PID == selfPID {
				continue
			}
			if !processAlive(gen.PID) {
				continue
			}
		}
		return gen, true
	}
	return EvalGeneration{}, false
}

// AllocateEvalGeneration 为一次新评测分配独立世代 scope。
//
//  1. 同 store 上若已有活跃写手（另一个评测正在 ingest / 清库），显式拒绝——写-写同 store
//     会互踩本地明文金库（最后一次 flush 覆盖另一份），必须让调用方换 store 或等待，
//     而不是静默地把两边都做坏。
//  2. 世代号重试若干次（撞名概率极低），登记租约（role=writer）后返回。
//
// 新世代的云端 scope 构造上为空，随后 Ingest(rebuild=true) 会按 /units 复核并给出清库回执。
func AllocateEvalGeneration(cfg *config.Config, suite, storePath string) (EvalGeneration, error) {
	return AllocateEvalGenerationForMode(cfg, suite, PulseModePulseCipher, storePath)
}

// AllocateEvalGenerationForMode 为指定脉冲模式（local_plain / pulse_plain / pulse_cipher）分配独立世代 scope。
func AllocateEvalGenerationForMode(cfg *config.Config, suite, pulseMode, storePath string) (EvalGeneration, error) {
	now := time.Now()
	pulseMode = NormalizePulseMode(pulseMode)
	endpoint := "local://sqlite-fts5-bm25"
	if pulseMode != PulseModeLocalPlain {
		endpoint = cloudEndpoint(cfg)
	}
	userID := cloudUserID()
	if busy, ok := liveWriterOnStore(storePath, endpoint, userID, now); ok {
		return EvalGeneration{}, fmt.Errorf(
			"评测 store %s 正被活跃世代 %s 写入（心跳 %s，角色 %s）：请等待其结束，或用 --eval-store 指定独立 store",
			storePath, busy.Scope, busy.HeartbeatAt, busy.Role)
	}
	var lastErr error
	for attempt := 0; attempt < evalGenerationAcquireAttempts; attempt++ {
		gen, err := newEvalGenerationForMode(cfg, suite, pulseMode, storePath, time.Now())
		if err != nil {
			return EvalGeneration{}, err
		}
		if _, exists := LoadEvalGeneration(gen.Scope); exists {
			lastErr = fmt.Errorf("世代号 %s 已被占用", gen.Scope)
			continue
		}
		if err := SaveEvalGeneration(gen); err != nil {
			// 登记目录不可写（例如只读 HOME）不得让评测失败：退化为「无租约的世代」，
			// 后续回收不会把它当成可清对象（无登记 = 不枚举），可读性优先。
			return gen, nil
		}
		return gen, nil
	}
	return EvalGeneration{}, lastErr
}

// startNewGeneration 打开一个新世代：分配 → 回收同模式过期世代 → 把引擎绑定到新 scope。
// 回收回执挂在引擎上，随本次 manifest 落盘（审计「这次回收了谁」）。
func (e *CloudEngine) startNewGeneration(ctx context.Context, logf func(string, ...any)) error {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if e.cfg == nil {
		return errors.New("cloud evaluation engine is not initialized with a config")
	}
	gen, err := AllocateEvalGenerationForMode(e.cfg, e.Suite, e.PulseMode, e.StorePath)
	if err != nil {
		return err
	}
	if err := e.BindScope(gen.Scope); err != nil {
		return err
	}
	e.ownsGeneration = true
	e.ownerToken = gen.OwnerToken
	logf("[eval] 新评测世代 scope=%s mode=%s（每种脉冲模式独立 scope，互不清库、互不干扰）", gen.Scope, NormalizePulseMode(e.PulseMode))
	e.retired = e.ReconcileRetiredGenerations(ctx, logf)
	return nil
}

func (e *CloudEngine) finishGeneration() {
	if e == nil || strings.TrimSpace(e.Scope) == "" || !e.ownsGeneration {
		return
	}
	if gen, ok := LoadEvalGeneration(e.Scope); ok {
		if gen.OwnerToken != "" && e.ownerToken != "" && gen.OwnerToken != e.ownerToken {
			return
		}
		stamp := time.Now().UTC().Format(time.RFC3339)
		gen.Role = evalGenerationRoleIdle
		gen.HeartbeatAt = stamp
		gen.FinishedAt = stamp
		_ = SaveEvalGeneration(gen)
	}
}

// CloseGeneration 在评测收尾时落「已结束」标记，并顺手回收心跳过期的旧世代（best-effort）。
// 标记而非删除：心跳窗口内的世代仍可能被 Admin 批次逐题读取，回收只在心跳过期后进行。
func (e *CloudEngine) CloseGeneration(ctx context.Context, logf func(string, ...any)) []EvalGenerationPurge {
	if e == nil || strings.TrimSpace(e.Scope) == "" {
		return nil
	}
	e.finishGeneration()
	return e.ReconcileRetiredGenerations(ctx, logf)
}

// adoptStoredGeneration 采纳本 store 上既有 manifest 的世代：manifest 是「这个 store 当前是谁」
// 的唯一真源；仅当套件与脉冲模式（local_plain / pulse_plain / pulse_cipher）均一致时才采纳。
func (e *CloudEngine) adoptStoredGeneration() (EvalGeneration, bool) {
	manifest, err := ReadCloudManifest(e.StorePath)
	if err != nil || manifest.Suite != e.Suite || e.validateManifestPulseMode(manifest) != nil {
		return EvalGeneration{}, false
	}
	suite, scopeMode, _, ok := SplitEvalScopeWithMode(manifest.Scope)
	if !ok || suite != e.Suite {
		return EvalGeneration{}, false
	}
	if NormalizePulseMode(scopeMode) != NormalizePulseMode(e.PulseMode) {
		return EvalGeneration{}, false
	}
	gen, _ := LoadEvalGeneration(manifest.Scope)
	gen.Scope = manifest.Scope
	gen.Generation = EvalScopeGeneration(manifest.Scope)
	gen.Suite = manifest.Suite
	gen.UserID = manifest.UserID
	gen.Endpoint = manifest.Endpoint
	gen.StorePath = e.StorePath
	gen.CorpusSHA256 = manifest.CorpusSHA256
	gen.Documents = manifest.Documents
	if err := e.BindScope(manifest.Scope); err != nil {
		return EvalGeneration{}, false
	}
	e.ownsGeneration = false
	e.ownerToken = ""
	return gen, true
}

// remoteScopeEmpty 以 /units 权威全集（或 local 模式本地金库）判定该 scope 是否为空。
// 空 = 旧记忆已被清空（他处清库、世代回收、引擎重置）⇒ 复用无意义，必须新开世代重算。
func (e *CloudEngine) remoteScopeEmpty(ctx context.Context) (bool, error) {
	if NormalizePulseMode(e.PulseMode) == PulseModeLocalPlain {
		return len(e.localStoredUnitIDs()) == 0, nil
	}
	if err := e.CheckHealth(ctx); err != nil {
		return false, err
	}
	ids, err := e.Client.ListCloudUnitIDs(ctx)
	if err != nil {
		return false, err
	}
	return len(ids) == 0, nil
}

// ReconcileRetiredGenerations 回收过期世代（「新测评回收过期记忆」）：
//
//   - 只清理**本地租约登记过**、且心跳已过期（> evalGenerationLiveTTL）的世代——活跃的评测/批次
//     一律跳过，这是「不打架」的硬保证；
//   - 逐 scope 走 /units 权威全集分批删除（云端成功后才删本地明文），逐条留回执；
//   - 任何失败都只记回执、绝不阻塞本次评测（清理过期记忆是卫生动作，不是本次运行的前置条件）。
//
// **不做「无租约即孤儿」的推断清理**：没有租约的 scope 可能正被其他评测进程使用，
// 按缺少登记去删它等于删其他在跑数据。历史固定 scope 的残留只有在被
// 登记（adopt / ingest 会补建租约）之后才进入回收体系。
func (e *CloudEngine) ReconcileRetiredGenerations(ctx context.Context, logf func(string, ...any)) []EvalGenerationPurge {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	// 清库要按权威租户回显校验（tenant 与 user 分离）；身份未确认时暂缓清理过期记忆，
	// 绝不拿未经核验的租户去删另一个 scope。
	if err := e.CheckHealth(ctx); err != nil {
		logf("[eval] 清理过期记忆暂缓（云端身份未确认）：%v", err)
		return nil
	}
	// 回收要把旧世代的本地明文一起删干净：先按盘上真值刷新内存金库快照。
	// 旧世代与本世代共用同一 store 文件时，本进程内的快照可能早于旧世代的写入，
	// 不刷新就删不掉（下次 flush 还会把旧明文写回盘）。
	if err := e.reloadPlaintextStore(); err != nil {
		logf("[eval] 本地金库刷新失败，跳过清理过期记忆：%v", err)
		return nil
	}
	var receipts []EvalGenerationPurge
	now := time.Now()
	activeMode := NormalizePulseMode(e.PulseMode)
	for _, gen := range listEvalGenerations() {
		if gen.Scope == e.Scope {
			continue
		}
		if gen.Endpoint != e.Endpoint || gen.UserID != e.UserID {
			continue
		}
		if e.Suite != "" && gen.Suite != "" && gen.Suite != e.Suite {
			continue
		}
		// 模式隔离：脉冲明文（pulse_plain）、脉冲密文（pulse_cipher）与本地明文（local_plain）各自独立维护世代，
		// 严禁脉冲明文评测去回收脉冲密文的数据（反之亦然）！
		if EvalScopePulseMode(gen.Scope) != activeMode {
			continue
		}
		if gen.live(now) {
			continue
		}
		var units int
		var err error
		if activeMode == PulseModeLocalPlain {
			if sameEvalStorePath(gen.StorePath, e.StorePath) || storeSharedWithLiveGeneration(gen.StorePath, gen.Scope, now) {
				// 共享物理 store 正在被当前或其他活跃世代使用，严禁删除其 SQLite 文件。
			} else if rmErr := os.Remove(gen.StorePath + ".local-vault.sqlite"); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
				err = rmErr
			}
		} else {
			units, err = e.purgeForeignScope(ctx, gen.Scope, gen.StorePath)
		}
		receipt := EvalGenerationPurge{Scope: gen.Scope, Units: units}
		if err != nil {
			receipt.Error = err.Error()
			logf("[eval] 旧世代 %s 回收失败（不阻塞本次评测）：%v", gen.Scope, err)
		} else {
			_ = os.Remove(evalGenerationPath(gen.Suite, gen.Scope))
			if activeMode == PulseModeLocalPlain {
				logf("[eval] 旧世代 %s 已回收：%d 条旧记忆（本地隔离金库已清理 · 零云端请求）", gen.Scope, units)
			} else {
				logf("[eval] 旧世代 %s 已回收：%d 条旧记忆（云端 + 本地金库同步删）", gen.Scope, units)
			}
		}
		receipts = append(receipts, receipt)
	}
	return receipts
}

func canonicalEvalStorePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	clean := filepath.Clean(p)
	if resolved, err := filepath.EvalSymlinks(clean); err == nil && resolved != "" {
		return filepath.Clean(resolved)
	}
	return clean
}

func sameEvalStorePath(a, b string) bool {
	ca, cb := canonicalEvalStorePath(a), canonicalEvalStorePath(b)
	return ca != "" && ca == cb
}

func storeSharedWithLiveGeneration(storePath, excludeScope string, now time.Time) bool {
	if strings.TrimSpace(storePath) == "" {
		return false
	}
	for _, other := range listEvalGenerations() {
		if other.Scope == excludeScope {
			continue
		}
		if other.live(now) && sameEvalStorePath(other.StorePath, storePath) {
			return true
		}
	}
	return false
}

// reloadPlaintextStore 按盘上真值重建内存明文金库（同一 store 文件被多个世代 / 多个进程
// 依次使用时，进程内快照必须与盘上对齐，否则删除不生效、flush 还会写回旧明文）。
func (e *CloudEngine) reloadPlaintextStore() error {
	if e == nil || e.Client == nil || strings.TrimSpace(e.StorePath) == "" {
		return errors.New("cloud evaluation engine is not initialized")
	}
	store, err := ramesdk.NewJSONFileStore(e.StorePath, []byte(evalVaultSecretFromKey(e.Client.APIToken)))
	if err != nil {
		return err
	}
	applyPulseTokenizer(store, e.PulseTokenizer)
	e.Store = store
	e.Client.Store = store
	return nil
}

// purgeForeignScope 用本引擎的身份与凭据清空另一个 scope（本 store 共用时复用同一金库实例，
// 保证「云端删除成功 → 本地明文同步删」这条链的一致性）。
func (e *CloudEngine) purgeForeignScope(ctx context.Context, scope, storePath string) (int, error) {
	scope = strings.TrimSpace(scope)
	if scope == "" || scope == e.Scope {
		return 0, nil
	}
	if _, ok := EvalScopeSuite(scope); !ok {
		return 0, fmt.Errorf("scope %q 不在评测命名空间，拒绝清库", scope)
	}
	apiKey := e.Client.APIToken
	store := e.Store
	if strings.TrimSpace(storePath) != "" && storePath != e.StorePath {
		var err error
		store, err = ramesdk.NewJSONFileStore(storePath, []byte(evalVaultSecretFromKey(apiKey)))
		if err != nil {
			return 0, fmt.Errorf("清库需要旧世代的本地金库 %s: %w", storePath, err)
		}
	}
	client := ramesdk.NewClient(e.Endpoint, apiKey, e.UserID, store)
	client.TenantID = e.TenantID
	client.Scope = scope
	client.HTTPClient = &http.Client{Timeout: 5 * time.Minute}
	return purgeScopeUnitsWithClient(ctx, client, scope, func(string, ...any) {})
}
