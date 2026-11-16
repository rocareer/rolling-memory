package rollingmemory

// 后台实时面统一走同源 WebSocket（pkg/ws + MonitorSocket）房间广播 + streamHub 单一 watcher：
// 单一采集 + 变更签名去重 + 多订阅广播。
//
// 设计动机（第一性原理）：控制台各面板此前各自 setInterval 轮询，采集成本随「打开的标签页数量」线性放大，
// 且静默期照旧空转——每 3s 一次 `sync status` 子进程（本机实测单次 22.9s）、每秒一次浮标聚合与金库探针、
// 每 2s 一次终端日志尾读。WebSocket 多路复用解决「单连接零同源阻塞推送」，真正省钱的是把采集从「每连接一份」收敛为
// 「每 kind 一份」：静默期 0 次子进程 / 0 次 SQL / 0 次聚合；状态变化后 ≤巡检周期推帧；N 个标签页共享
// 同一 watcher，成本不叠加。
//
// 帧生产者契约：返回 (签名, 载荷, 是否推帧)。签名相同即状态未变、不重复推帧；昂贵采集（子进程、重聚合）
// 在闭包内先用廉价文件指纹判是否值得采集，故「无变化 ⇒ 无采集」，而不是「无变化 ⇒ 照旧采集后丢弃」。

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/version"
)

const (
	// streamFileWatchInterval 文件指纹巡检周期：与既有 status/dashboard 流保持同一档位，
	// 保证「记忆写入 → 界面刷新」的端到端延迟 ≤250ms。
	streamFileWatchInterval = 250 * time.Millisecond
	// streamSlowWatchInterval 昂贵流（子进程 / 重聚合）的指纹巡检周期。
	streamSlowWatchInterval = time.Second
)

type streamKind string

const (
	streamKindStatus    streamKind = "status"
	streamKindDashboard streamKind = "dashboard"
	streamKindEnvHealth streamKind = "env_health"
	streamKindTerminal  streamKind = "terminal"
	streamKindROI       streamKind = "roi"
	streamKindEval      streamKind = "eval"
)

// streamKey 是一条共享流的身份：同一 key 的全部连接共享一个 watcher，
// scope / harness / limit 参与 key，保证不同查询口径互不串帧。
type streamKey struct {
	kind    streamKind
	scope   string
	harness string
	limit   int
}

// streamFrameProducer 一条流里的单个帧生产者。
type streamFrameProducer struct {
	event string
	// alwaysPush 为 true 时每轮巡检都推帧（用于固定节拍的采样帧）；否则签名不变即不推。
	alwaysPush bool
	// minInterval 是该帧的最小推送间隔（0 = 不限制）：固定节拍帧据此定频（如 1Hz 采样帧），
	// 昂贵帧据此在持续写入时限流（帧率有界 = 控制台 CPU 有界）。
	minInterval time.Duration
	produce     func(ctx context.Context) (sig string, payload any, ok bool)
}

// streamSub 一个实时流订阅：ch 只保留最新帧（容量 = 生产者数 + 1），events 是该订阅关心的帧事件。
type streamSub struct {
	ch     chan string
	events map[string]bool
}

// streamHub 一条共享流的单一采集与广播中枢。
type streamHub struct {
	key       streamKey
	interval  time.Duration
	producers []streamFrameProducer

	mu         sync.Mutex
	subs       map[*streamSub]struct{}
	watchStop  context.CancelFunc
	lastSigs   []string
	lastRuns   []time.Time
	lastFrames map[string]string
	closed     bool
	// poke 让外部请求（如控制台「重新检测」按钮）立刻唤醒一次采集；forceNext 标记
	// 下次采集跳过廉价指纹门与节流，forcePush 标记下次采集产出的帧跳过「与上一帧签名相同」
	// 去重门（用户显式请求的刷新必须有数据帧送达订阅者以结束前端等待态，不能因状态未变被吞掉）。
	poke      chan struct{}
	forceNext bool
	forcePush bool
}

var (
	streamHubsMu sync.Mutex
	streamHubs   = map[streamKey]*streamHub{}
)

// newStreamHub 装配一条共享流并补齐内部簿记不变量（订阅表、签名/时间/首帧缓存切片）：
// 注册表路径与契约测试共用同一构造，避免各 kind 各自初始化时漏项。
func newStreamHub(key streamKey, interval time.Duration, producers ...streamFrameProducer) *streamHub {
	if interval <= 0 {
		interval = streamSlowWatchInterval
	}
	return &streamHub{
		key:        key,
		interval:   interval,
		producers:  producers,
		subs:       map[*streamSub]struct{}{},
		lastSigs:   make([]string, len(producers)),
		lastRuns:   make([]time.Time, len(producers)),
		lastFrames: map[string]string{},
		poke:       make(chan struct{}, 1),
	}
}

// streamHubFor 返回 key 对应的共享流；首个订阅者触发 watcher 启动，最后一个订阅者离开即停表并退出注册表
// （key 含 scope 等请求参数，退表可避免长跑控制台被任意 scope 撑大内存）。
func streamHubFor(key streamKey) *streamHub {
	streamHubsMu.Lock()
	defer streamHubsMu.Unlock()
	if hub, ok := streamHubs[key]; ok {
		return hub
	}
	hub := buildStreamHub(key)
	if hub == nil {
		return nil
	}
	hub.key = key
	streamHubs[key] = hub
	return hub
}

func (h *streamHub) subscribe(events map[string]bool) *streamSub {
	sub := &streamSub{ch: make(chan string, len(h.producers)+1), events: events}
	h.mu.Lock()
	initial := h.snapshotLocked(events)
	for _, frame := range initial {
		select {
		case sub.ch <- frame:
		default:
		}
	}
	h.subs[sub] = struct{}{}
	first := len(h.subs) == 1
	if first && !h.closed {
		ctx, cancel := context.WithCancel(context.Background())
		h.watchStop = cancel
		go h.watch(ctx)
	}
	h.mu.Unlock()
	return sub
}

func (h *streamHub) unsubscribe(sub *streamSub) {
	h.mu.Lock()
	_, tracked := h.subs[sub]
	delete(h.subs, sub)
	if tracked {
		close(sub.ch)
	}
	if len(h.subs) == 0 && h.watchStop != nil {
		h.watchStop()
		h.watchStop = nil
		// 无人订阅：清掉首帧缓存与节流记时，下一次订阅重新推首帧（避免新连接读到上一轮的陈旧状态）。
		h.lastSigs = make([]string, len(h.producers))
		h.lastRuns = make([]time.Time, len(h.producers))
		h.lastFrames = map[string]string{}
		streamHubsMu.Lock()
		delete(streamHubs, h.key)
		streamHubsMu.Unlock()
	}
	h.mu.Unlock()
}

func (h *streamHub) snapshotLocked(events map[string]bool) []string {
	frames := make([]string, 0, len(h.producers))
	for _, p := range h.producers {
		if !events[p.event] {
			continue
		}
		if frame := h.lastFrames[p.event]; frame != "" {
			frames = append(frames, frame)
		}
	}
	return frames
}

// progressEventPrefix 判别进度帧（瞬态、单调推进）：队列满时优先替换更早的进度帧——
// 最新进度信息量最大；状态帧（env_health 等全量快照）则绝不因进度帧被挤出。
const progressEventPrefix = "event: env_health_progress"

// broadcast 立即向订阅者推送一帧瞬态事件（供采集过程的进度帧使用）：不做签名去重、
// 不写首帧快照缓存——进度是采集瞬态而非状态，新订阅者不需要重放上一轮的旧进度；
// 队列满时丢弃最旧的进度帧以让最新进度与状态帧都能送达。
func (h *streamHub) broadcast(event string, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	frame := "event: " + event + "\ndata: " + string(raw) + "\n\n"
	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.subs {
		if !sub.events[event] {
			continue
		}
		select {
		case sub.ch <- frame:
		default:
			if !strings.HasPrefix(frame, progressEventPrefix) {
				continue
			}
			select {
			case oldest := <-sub.ch:
				if strings.HasPrefix(oldest, progressEventPrefix) {
					select {
					case sub.ch <- frame:
					default:
					}
				} else {
					select {
					case sub.ch <- oldest:
					default:
					}
				}
			default:
			}
		}
	}
}

// publishStateFrame 写入最新状态帧快照（lastFrames / lastSigs）并立即推送给所有订阅者：
// 供写操作（如 syncReconcileHandler）产出新状态后直接更新共享流，保证当前订阅者立刻收到、
// 且随后新打开/切入的页面也能从 snapshotLocked 0ms 读到最新状态而非旧缓存。
func (h *streamHub) publishStateFrame(event, sig string, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	frame := "event: " + event + "\ndata: " + string(raw) + "\n\n"
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range h.producers {
		if h.producers[i].event == event {
			h.lastSigs[i] = sig
			h.lastRuns[i] = time.Now()
		}
	}
	h.lastFrames[event] = frame
	for sub := range h.subs {
		if !sub.events[event] {
			continue
		}
		select {
		case sub.ch <- frame:
		default:
			select {
			case <-sub.ch:
			default:
			}
			select {
			case sub.ch <- frame:
			default:
			}
		}
	}
}

// notifyStreamKindRefresh 唤醒所有属于指定 kind 的活跃共享流立即重采并推帧：
// 供大库后台异步缓存（dashboardSnapCache / roiVaultBundleCache / syncProductionRecallLedger）
// 在异步重算完成后主动把最新快照推送到前端 WebSocket。
func notifyStreamKindRefresh(kind streamKind) {
	streamHubsMu.Lock()
	hubs := make([]*streamHub, 0, len(streamHubs))
	for k, h := range streamHubs {
		if k.kind == kind && h != nil {
			hubs = append(hubs, h)
		}
	}
	streamHubsMu.Unlock()
	for _, h := range hubs {
		if h.hasSubscribers() {
			h.RequestRefresh()
		}
	}
}

// RequestRefresh 请求立即采集一次并跳过「无变化」指纹门与签名去重门：供控制台显式刷新
// （重新检测 / 同步后刷新）使用——用户主动发起的检测必须产生一次真实采集并把数据帧送达前端，
// 不能因节流、文件指纹未变或载荷签名与上一帧相同而被静默吞掉（否则前端等待态永不收尾）。
func (h *streamHub) RequestRefresh() {
	h.mu.Lock()
	h.forceNext = true
	h.forcePush = true
	h.mu.Unlock()
	select {
	case h.poke <- struct{}{}:
	default:
	}
}

// consumeForce 读取并清除一次强制位；由帧生产者在采集开始时调用。
func (h *streamHub) consumeForce() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	forced := h.forceNext
	h.forceNext = false
	return forced
}

func (h *streamHub) hasSubscribers() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs) > 0
}

func (h *streamHub) watch(ctx context.Context) {
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()
	h.collect(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.poke:
			// 外部显式请求（控制台「重新检测」）：立刻采集一次，不等巡检节拍。
			h.collect(ctx)
		case <-ticker.C:
			h.collect(ctx)
		}
	}
}

func (h *streamHub) collect(ctx context.Context) {
	h.mu.Lock()
	forcePush := h.forcePush
	h.forcePush = false
	h.mu.Unlock()
	for i := range h.producers {
		p := &h.producers[i]
		if !h.wanted(p.event) {
			// 本连接都不关心的帧绝不采集（例如没人开浮标时不跑 1Hz 金库探针）。
			continue
		}
		if err := ctx.Err(); err != nil {
			return
		}
		now := time.Now()
		h.mu.Lock()
		throttled := !forcePush && p.minInterval > 0 && !h.lastRuns[i].IsZero() && now.Sub(h.lastRuns[i]) < p.minInterval
		h.mu.Unlock()
		if throttled {
			continue
		}
		sig, payload, ok := p.produce(ctx)
		if !ok {
			continue
		}
		h.mu.Lock()
		h.lastRuns[i] = now
		if !forcePush && !p.alwaysPush && sig != "" && sig == h.lastSigs[i] {
			h.mu.Unlock()
			continue
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			h.mu.Unlock()
			continue
		}
		h.lastSigs[i] = sig
		frame := "event: " + p.event + "\ndata: " + string(raw) + "\n\n"
		h.lastFrames[p.event] = frame
		for sub := range h.subs {
			if !sub.events[p.event] {
				continue
			}
			select {
			case sub.ch <- frame:
			default:
				// 慢客户端只保留最新一帧：实时状态帧是自洽全量状态，丢旧帧不丢信息，也不阻塞采集侧。
				select {
				case <-sub.ch:
				default:
				}
				select {
				case sub.ch <- frame:
				default:
				}
			}
		}
		h.mu.Unlock()
	}
}

func (h *streamHub) wanted(event string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.subs {
		if sub.events[event] {
			return true
		}
	}
	return false
}

// buildStreamHub 按 kind 装配帧生产者；返回 nil 表示未知 kind。
func buildStreamHub(key streamKey) *streamHub {
	home, _ := os.UserHomeDir()
	switch key.kind {
	case streamKindStatus:
		return newStatusStreamHub(key, home)
	case streamKindDashboard:
		return newDashboardStreamHub(key, home)
	case streamKindEnvHealth:
		return newEnvHealthStreamHub(key, home)
	case streamKindTerminal:
		return newTerminalStreamHub(key, home)
	case streamKindROI:
		return newROIStreamHub(key, home)
	case streamKindEval:
		return newEvalStreamHub(key)
	}
	return nil
}

func newEvalStreamHub(key streamKey) *streamHub {
	return newStreamHub(key, 200*time.Millisecond,
		streamFrameProducer{event: "eval_progress", produce: func(context.Context) (string, any, bool) {
			snap := snapshotJobStatus()
			sig := fmt.Sprintf("%s|%d|%s|%d|%d", snap.JobID, snap.UpdatedAtUnixMS, snap.Status, snap.Done, snap.Total)
			return sig, snap, true
		}},
	)
}

// newStatusStreamHub 装配控制台状态流：`status` 帧按文件指纹变化推全量状态，
// `probe` 帧按 1Hz 固定节拍推浮标采样（供心电式实时曲线，替代原每秒一次的 /widget 轮询端点）。
func newStatusStreamHub(key streamKey, home string) *streamHub {
	dbPath := resolveMemoryDBPath(home)
	logPath := filepath.Join(home, ".local", "state", "rolling-memory", "rolling-memory.log")
	hostVer := version.Get().Version
	hostAPIVer := HostAPIVersion

	var sigMu sync.Mutex
	var lastFullSig string
	var snapMu sync.Mutex
	var snapshot StatusResponse
	var snapshotAt time.Time

	return newStreamHub(key, streamFileWatchInterval,
		// 全量状态帧：先比 250ms 级的廉价文件指纹，指纹未变时**完全不跑全量聚合**（静默期 0 次 SQL）；
		// 指纹变化且距离上次聚合 ≥500ms 才重算产物统计与最近单元，故持续写入时聚合频率有界（2Hz），
		// 控制台 CPU 不会被自己的实时面吃满，而去重推帧与首帧补发由 hub 的帧缓存承担。
		streamFrameProducer{event: "status", minInterval: 500 * time.Millisecond, produce: func(context.Context) (string, any, bool) {
			sig := statusStreamSignature(home, dbPath, logPath)
			sigMu.Lock()
			defer sigMu.Unlock()
			if sig == lastFullSig {
				return "", nil, false
			}
			payload := collectStatus(home, hostVer, hostAPIVer)
			lastFullSig = sig
			snapMu.Lock()
			snapshot, snapshotAt = payload, time.Now()
			snapMu.Unlock()
			return sig, payload, true
		}},
		// 浮标采样帧：固定 1Hz 节拍（实时曲线的每格 = 1 秒），只做一次本地金库/上游探针 + 复用最近一次
		// status 快照的计数（快照由指纹巡检驱动，任何真实变化都会在 ≤250ms 内刷新它），
		// 因此每秒的采集成本从「一次 collectStatus 全量聚合」降为「一次探针 + 一次状态文件读」。
		streamFrameProducer{event: "probe", alwaysPush: true, minInterval: time.Second, produce: func(ctx context.Context) (string, any, bool) {
			snapMu.Lock()
			latest, at := snapshot, snapshotAt
			snapMu.Unlock()
			if at.IsZero() {
				return "", nil, false
			}
			return "", buildWidgetSnapshot(ctx, home, latest), true
		}},
	)
}

// newDashboardStreamHub 装配主控制台状态流：签名（文件指纹 + 后台任务进度 + 5 分钟槽）变化即推全量仪表盘。
// 与 status 流同一套两段闸：指纹未变不跑聚合，变化且距上次聚合 ≥500ms 才重算（持续写入时 CPU 有界）。
func newDashboardStreamHub(key streamKey, home string) *streamHub {
	dbPath := resolveMemoryDBPath(home)
	logPath := filepath.Join(home, ".local", "state", "rolling-memory", "rolling-memory.log")
	var sigMu sync.Mutex
	var lastFullSig string
	var hubRef *streamHub
	hub := newStreamHub(key, streamFileWatchInterval,
		streamFrameProducer{event: "dashboard", minInterval: 500 * time.Millisecond, produce: func(context.Context) (string, any, bool) {
			now := time.Now()
			sig := dashboardFileSignature(home, dbPath, logPath, now)
			forced := hubRef != nil && hubRef.consumeForce()
			sigMu.Lock()
			defer sigMu.Unlock()
			if !forced && sig == lastFullSig {
				return "", nil, false
			}
			payload := collectDashboardData(home, key.scope)
			lastFullSig = sig
			return jsonSignature(payload), payload, true
		}},
	)
	hubRef = hub
	return hub
}

// newEnvHealthStreamHub 装配环境与记忆健康流。
//
// 采集成本是本流的关键约束：`sync status` 子进程在本机实测全量扫描约 22.9s（按当前 Harness 过滤后 0.9s，
// 载荷内容一致）。故：① 先比廉价文件指纹，无变化完全不启子进程；② 子进程按当前 Harness 过滤，把单次采集
// 从「秒级～数十秒级」压到亚秒级；③ 昂贵采集最小间隔 10s，避免持续写入时连轴转；④ 每 120s 兜底重采一次，
// 覆盖指纹盲区（例如无 Hook 写入的宿主会话在既有 jsonl 上原地增长）。
func newEnvHealthStreamHub(key streamKey, home string) *streamHub {
	dbPath := resolveMemoryDBPath(home)
	logPath := filepath.Join(home, ".local", "state", "rolling-memory", "rolling-memory.log")
	cfgPath := filepath.Join(home, ".config", "rolling-memory", "config.toml")

	var gateMu sync.Mutex
	var lastGateSig string
	var lastGateAt time.Time

	// 进度帧经 hub 广播：采集前推 running 帧，采集期把 CLI stderr 的进度事件即时转成
	// 实时广播帧，采集结束（含失败）推收尾帧——首帧等待因此有真实阶段与计数可显。
	var hubRef *streamHub
	progress := func(ev map[string]any) {
		if hubRef != nil {
			hubRef.broadcast("env_health_progress", ev)
		}
	}
	hub := newStreamHub(key, streamSlowWatchInterval,
		streamFrameProducer{event: "env_health", produce: func(ctx context.Context) (string, any, bool) {
			gate := envHealthChangeSignature(home, cfgPath, dbPath, logPath)
			// 显式刷新（重新检测）跳过指纹门与节流：用户主动请求必须产生一次真实采集。
			forced := hubRef != nil && hubRef.consumeForce()
			if !forced {
				gateMu.Lock()
				unchanged := gate == lastGateSig
				stale := time.Since(lastGateAt) >= envHealthMaxSilence
				throttled := time.Since(lastGateAt) < envHealthProbeMinInterval
				gateMu.Unlock()
				if unchanged && !stale {
					return "", nil, false
				}
				if throttled && !stale {
					return "", nil, false
				}
			}
			harness := strings.TrimSpace(key.harness)
			if harness == "" {
				harness = activeHarnessID(home)
			}
			startedAt := time.Now()
			progress(map[string]any{"running": true, "started_at": startedAt.UnixMilli()})
			payload, err := collectEnvHealthPayload(withEnvHealthProgressSink(ctx, progress), home, harness)
			if err != nil {
				progress(map[string]any{"running": false, "error": err.Error(), "elapsed_ms": time.Since(startedAt).Milliseconds()})
				// 采集失败：保留上一帧（前端已有手动刷新与错误提示路径），不用错误对象顶掉界面上的有效状态。
				return "", nil, false
			}
			progress(map[string]any{"running": false, "elapsed_ms": time.Since(startedAt).Milliseconds()})
			storeCachedEnvHealthPayload(home, harness, payload)
			gateMu.Lock()
			lastGateSig, lastGateAt = gate, time.Now()
			gateMu.Unlock()
			return envHealthPayloadSignature(payload), payload, true
		}},
	)
	// 首帧缓存预热：若进程内已有近期成功采集的环境健康快照，直接预装为首帧，
	// 新打开/切回页面的订阅者 0ms 立即可见；若快照已过新鲜度窗口，后续巡检仍会异步刷新。
	initHarness := strings.TrimSpace(key.harness)
	if initHarness == "" {
		initHarness = activeHarnessID(home)
	}
	if cached, cachedAt, ok := peekCachedEnvHealthPayload(home, initHarness); ok {
		if raw, err := json.Marshal(cached); err == nil {
			sig := envHealthPayloadSignature(cached)
			hub.lastSigs[0] = sig
			hub.lastFrames["env_health"] = "event: env_health\ndata: " + string(raw) + "\n\n"
			if time.Since(cachedAt) < envHealthPayloadCacheTTL {
				lastGateSig = envHealthChangeSignature(home, cfgPath, dbPath, logPath)
				lastGateAt = cachedAt
			}
		}
	}
	hubRef = hub
	return hub
}

// newTerminalStreamHub 装配终端流：Hook 日志与金库指纹变化即推一帧新的终端输出。
func newTerminalStreamHub(key streamKey, home string) *streamHub {
	dbPath := resolveMemoryDBPath(home)
	logPath := filepath.Join(home, ".local", "state", "rolling-memory", "rolling-memory.log")
	limit := key.limit
	if limit <= 0 {
		limit = 40
	}
	req := TerminalRequest{Action: "stream", Limit: limit}
	return newStreamHub(key, streamSlowWatchInterval,
		streamFrameProducer{event: "terminal", produce: func(context.Context) (string, any, bool) {
			sig := terminalStreamSignature(home, dbPath, logPath, limit)
			return sig, handleTerminalAction(home, req), true
		}},
	)
}

// newROIStreamHub 装配 ROI 监控流：先比金库与效能台账指纹，变化且距上次重聚合 ≥5s 才重跑 ROI 聚合
// （该聚合读 Vault 全量窗口 + 台账 + @ref 指针水合，是后台最重的一次性查询，绝不按秒空转）。
func newROIStreamHub(key streamKey, home string) *streamHub {
	dbPath := resolveMemoryDBPath(home)
	req := roiFactsRequest{Scope: key.scope, Harness: key.harness}

	var gateMu sync.Mutex
	var lastGateSig string
	var lastGateAt time.Time
	var hubRef *streamHub

	hub := newStreamHub(key, streamSlowWatchInterval,
		streamFrameProducer{event: "roi", produce: func(ctx context.Context) (string, any, bool) {
			gate := roiFactsChangeSignature(home, dbPath)
			forced := hubRef != nil && hubRef.consumeForce()
			if !forced {
				gateMu.Lock()
				changed := gate != lastGateSig
				throttled := time.Since(lastGateAt) < roiFactsMinInterval
				gateMu.Unlock()
				if !changed || throttled {
					return "", nil, false
				}
			}
			triggerProductionRecallLedgerSync(home, dbPath)
			resp := queryROIFacts(ctx, dbPath, req)
			if resp.Status == "available" && resp.IdentityStatus != "verified" {
				resp.Status, resp.Reason, resp.Coverage, resp.IdentityStatus = "unknown", "ledger_identity_unverified", "unknown", "unknown"
				resp.Counts, resp.Windows, resp.RecentEvents = nil, []roiFactWindow{}, []roiFactEvent{}
			}
			gateMu.Lock()
			lastGateSig, lastGateAt = gate, time.Now()
			gateMu.Unlock()
			return jsonSignature(resp), resp, true
		}},
	)
	hubRef = hub
	return hub
}

// envHealthProbeMinInterval 昂贵采集（子进程）最小间隔；envHealthMaxSilence 是兜底重采周期（指纹盲区上界）。
// 三个节流值用变量而非常量声明：契约测试需要把它们压到毫秒级来验证「变化才采集、静默不采集」。
var (
	envHealthProbeMinInterval = 30 * time.Second
	envHealthMaxSilence       = 120 * time.Second
	// roiFactsMinInterval ROI 重聚合最小间隔（变更驱动 + 节流，防持续写入时连轴重算）。
	roiFactsMinInterval = 5 * time.Second
)

// envHealthChangeSignature 是环境健康流的廉价变更指纹：控制台可见状态的全部输入面
// （配置、金库主文件、Hook 日志、宿主活动工作区、宿主会话库主文件、各 Harness 历史根目录）。
// 不纳高频流式刷新的 `-wal` 侧车文件（避免流式生成期间每 10s 空转拉起一次 `sync status` 子进程；
// 回合收尾与同步落库均会更新 active_workspace.json / rolling-memory.log）。
func envHealthChangeSignature(home, cfgPath, dbPath, logPath string) string {
	paths := []string{
		cfgPath,
		dbPath,
		logPath,
		filepath.Join(home, ".local", "state", "rolling-memory", "active_workspace.json"),
		// 宿主会话库主文件：配合 active_workspace.json 与 Hook 日志覆盖回合级活跃变化。
		filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"),
		// 各 Harness 的历史根目录：目录 mtime 覆盖「新会话落盘」。
		filepath.Join(home, ".zcode", "projects"),
		filepath.Join(home, ".claude", "projects"),
		filepath.Join(home, ".codex", "sessions"),
		filepath.Join(home, ".dsh", "sessions"),
		filepath.Join(home, ".codebuddy", "projects"),
		filepath.Join(home, ".workbuddy", "projects"),
	}
	var b strings.Builder
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil {
			fmt.Fprintf(&b, "%s:%d@%d;", p, st.Size(), st.ModTime().UnixNano())
		} else {
			b.WriteString(p)
			b.WriteString(":-;")
		}
	}
	return b.String()
}

// terminalStreamSignature 终端流的变更指纹：终端输出只由 Hook 日志与金库近况构成，两者的指纹即完整输入面。
func terminalStreamSignature(home, dbPath, logPath string, limit int) string {
	var b strings.Builder
	for _, p := range []string{
		logPath,
		dbPath,
		dbPath + "-wal",
		filepath.Join(home, ".config", "rolling-memory", "config.toml"),
	} {
		if st, err := os.Stat(p); err == nil {
			fmt.Fprintf(&b, "%s:%d@%d;", p, st.Size(), st.ModTime().UnixNano())
		}
	}
	fmt.Fprintf(&b, "limit=%d", limit)
	return b.String()
}

// roiFactsChangeSignature ROI 流的变更指纹：金库 + 效能台账 + 宿主 recall 流水（ROI 聚合的全部输入面）。
func roiFactsChangeSignature(home, dbPath string) string {
	var b strings.Builder
	for _, p := range []string{
		dbPath,
		dbPath + "-wal",
		dbPath + ".efficacy.sqlite",
		dbPath + ".efficacy.sqlite-wal",
		filepath.Join(home, ".config", "rolling-memory", "config.toml"),
		filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"),
		filepath.Join(home, ".zcode", "cli", "db", "db.sqlite-wal"),
	} {
		if st, err := os.Stat(p); err == nil {
			fmt.Fprintf(&b, "%s:%d@%d;", p, st.Size(), st.ModTime().UnixNano())
		} else {
			b.WriteString(p)
			b.WriteString(":-;")
		}
	}
	return b.String()
}

// envHealthPayloadSignature 环境健康帧的去重签名：忽略每次采集必然变化的 `updated_at`
// （纯展示时间戳，参与签名会让帧永不重复），其余字段的全部变化都要推帧。
func envHealthPayloadSignature(payload map[string]any) string {
	canonical := make(map[string]any, len(payload))
	for k, v := range payload {
		if k == "updated_at" {
			continue
		}
		canonical[k] = v
	}
	return jsonSignature(canonical)
}

// jsonSignature 以稳定序列化计算载荷签名（sha256 十六进制；载荷为几十 KB 量级，单次哈希成本可忽略）。
func jsonSignature(payload any) string {
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("%x", sum[:16])
}

// buildWidgetSnapshot 装配浮标契约载荷：一次本地金库/上游探针 + 最近一次状态快照。
// `/widget` 端点与 status 流的 1Hz probe 帧共用本装配，两处口径不漂移。
func buildWidgetSnapshot(ctx context.Context, home string, st StatusResponse) WidgetResponse {
	var upOnline bool
	var upRTT float64
	var upEngine, upEndpoint string
	if isLocalModeForHome(home) {
		// 本地模式：浮标采样本地 SQLite 明文金库往返，零云端请求。
		upOnline, upRTT, upEngine, upEndpoint = probeLocalVaultHealth(home)
	} else {
		upOnline, upRTT, upEngine, upEndpoint = probeUpstreamV1Health(ctx)
	}
	lastEv := "idle"
	var lastEvTS int64
	if len(st.HookEvents) > 0 {
		lastEv = st.HookEvents[0].Event + " (" + st.HookEvents[0].Outcome + ")"
		lastEvTS = st.HookEvents[0].Timestamp
	}
	bgStr := ""
	if st.BgTask.Running {
		bgStr = fmt.Sprintf("%s %.0f%%", st.BgTask.TaskType, st.BgTask.Percent)
	}
	recallMS := st.LastRecallMS
	if recallMS <= 0 && upRTT > 0 {
		recallMS = int(upRTT + 0.5)
	}
	return WidgetResponse{
		Enabled:           st.Switches.WidgetEnabled,
		Online:            upOnline,
		UpstreamOnline:    upOnline,
		UpstreamRTTMS:     upRTT,
		UpstreamEndpoint:  upEndpoint,
		UpstreamEngine:    upEngine,
		Version:           st.Version,
		CoreVersion:       st.CoreVersion,
		CoreCodename:      st.CoreCodename,
		Mode:              st.Switches.Mode,
		Scope:             st.Scope,
		ActiveHarness:     detectActiveHarnessLabel(home),
		ActiveAssertions:  st.ActiveAssertions,
		ConflictsProposed: st.ConflictsProposed,
		LastEvent:         lastEv,
		LastEventTS:       lastEvTS,
		EventCount:        len(st.HookEvents),
		LastRecallMS:      recallMS,
		BootContext:       st.Switches.BootContext,
		Ledger:            st.Switches.Ledger,
		BgTask:            bgStr,
		SampledAtUnixMS:   time.Now().UnixMilli(),
	}
}

// activeHarnessID 读取 ~/.local/state/rolling-memory/active_workspace.json 的 carrier 机器标识
// （`harness:zcode` → `zcode`），用作 `sync status --harness` 的过滤值。
// 与 detectActiveHarnessLabel 同源同文件但不同用途：前者要机器标识（子进程过滤），后者要宿主显示名
// （界面文案，映射表只服务展示），故不合并。
func activeHarnessID(home string) string {
	raw, err := os.ReadFile(filepath.Join(home, ".local", "state", "rolling-memory", "active_workspace.json"))
	if err != nil {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(asString(m["carrier"]))), "harness:")
}
