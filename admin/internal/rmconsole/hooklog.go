// plugin: rolling-memory v0.0.58
// hooklog.go —— hook 日志聚合（实时控制台趋势图与 KPI 卡的日志腿）。
//
// 口径（与「记忆吞吐与检索召回趋势」图例一一对应）：
//   - 只有 event == "recall" 的行计入「召回」腿；start/close 等收尾事件不计
//     （它们既不是召回，也不产生原文）；
//   - 每行按 "scope" 归集：带 scope 的行只进对应 scope 桶；未带 scope 的历史行
//     归入未归属桶并计入所有 scope 视图（滚动窗口内新行补齐 scope 后自然收敛）；
//   - 全文件按 (size, mtime) 缓存：日志未变时复用上次扫描结果，1s 轮询不重复解析；
//   - 日志由 hooks 侧在超过 2 MiB 时轮转为 <log>.1（单备份），聚合只覆盖当前文件，
//     所以「Hook 触发总数」与「近 30 天」的日志腿都只到当前日志窗口为止。
package rollingmemory

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	// hookLogSlotSec 5 分钟槽（与前端「近 1 小时」的 12 桶一致）
	hookLogSlotSec = int64(300)
	// hookLogFutureSkew 允许的未来时间戳（秒）：超出视为坏行，不入桶
	hookLogFutureSkew = int64(60)
	// 保留窗口：日桶 31 天、小时桶 25 小时、5 分钟槽 2 小时
	hookLogDayRetain  = 31 * 24 * time.Hour
	hookLogHourRetain = 25 * time.Hour
	hookLogSlotRetain = 2 * time.Hour
	// 最近事件条数 / 延迟样本上限（均值取最近 20，迷你图取最后 12）
	hookLogRecentMax  = 12
	hookLogLatencyMax = 40
)

// scopeLogStat 单个 scope 的 recall 行计数。
type scopeLogStat struct {
	Recalls int            // 该 scope 的 recall 行总数
	Day     map[string]int // "2006-01-02" -> 计数
	Hour    map[string]int // "2006-01-02 15" -> 计数
	Five    map[int64]int  // 5 分钟槽起点（epoch，时钟对齐）-> 计数
}

func newScopeLogStat() *scopeLogStat {
	return &scopeLogStat{
		Day:  map[string]int{},
		Hour: map[string]int{},
		Five: map[int64]int{},
	}
}

func (s *scopeLogStat) add(ts int64, now time.Time, loc *time.Location) {
	if s == nil {
		return
	}
	s.Recalls++
	t := time.Unix(ts, 0).In(loc)
	if now.Sub(t) <= hookLogDayRetain {
		s.Day[t.Format("2006-01-02")]++
	}
	if now.Sub(t) <= hookLogHourRetain {
		s.Hour[t.Format("2006-01-02 15")]++
	}
	if now.Sub(t) <= hookLogSlotRetain {
		s.Five[t.Unix()-t.Unix()%hookLogSlotSec]++
	}
}

func (s *scopeLogStat) day(key string) int {
	if s == nil {
		return 0
	}
	return s.Day[key]
}

func (s *scopeLogStat) hour(key string) int {
	if s == nil {
		return 0
	}
	return s.Hour[key]
}

func (s *scopeLogStat) five(slot int64) int {
	if s == nil {
		return 0
	}
	return s.Five[slot]
}

// hookLogStat 一次日志扫描的聚合结果。
type hookLogStat struct {
	Path string
	Size int64
	Mod  time.Time

	Total    int // 文件内全部事件行
	Injected int // injected>0 || written>0 的行
	LastMS   int // 最后一条带 ms 的事件行（任意类型）
	Latency  []int
	Recent   []HookEventItem // 最近 12 条，最新在前
	FiveAll  map[int64]int   // 全部事件行的 5 分钟槽（全机，Hook 迷你图用）

	Scopes   map[string]*scopeLogStat // 带 scope 字段的行
	Unscoped *scopeLogStat            // 无 scope 字段的历史行
	All      *scopeLogStat            // 全部 recall 行（各 scope + 未归属）
}

// scopeGroup 取（必要时新建）某 scope 的计数桶。
func (s *hookLogStat) scopeGroup(label string) *scopeLogStat {
	group := s.Scopes[label]
	if group == nil {
		group = newScopeLogStat()
		s.Scopes[label] = group
	}
	return group
}

// isDefaultUserScope 判断是否为默认个人记忆空间（default、历史 mem:self 与更名前历史名互认联读）。
func isDefaultUserScope(scope string) bool {
	return scope == UserScope || scope == LegacyUserScope
}

// recallDay 指定 scope 的日桶计数（scope 为空或 ALL 取全机）。
func (s *hookLogStat) recallDay(scope, key string) int {
	if s == nil {
		return 0
	}
	if scope == "" || scope == "ALL" {
		return s.All.day(key)
	}
	if isDefaultUserScope(scope) {
		return s.Scopes[UserScope].day(key) + s.Scopes[LegacyUserScope].day(key) + s.Scopes[legacyRenamedScope].day(key) + s.Unscoped.day(key)
	}
	return s.Scopes[scope].day(key) + s.Unscoped.day(key)
}

// recallHour 指定 scope 的小时桶计数（scope 为空或 ALL 取全机）。
func (s *hookLogStat) recallHour(scope, key string) int {
	if s == nil {
		return 0
	}
	if scope == "" || scope == "ALL" {
		return s.All.hour(key)
	}
	if isDefaultUserScope(scope) {
		return s.Scopes[UserScope].hour(key) + s.Scopes[LegacyUserScope].hour(key) + s.Scopes[legacyRenamedScope].hour(key) + s.Unscoped.hour(key)
	}
	return s.Scopes[scope].hour(key) + s.Unscoped.hour(key)
}

// recallSlot 指定 scope 的 5 分钟槽计数（scope 为空或 ALL 取全机）。
func (s *hookLogStat) recallSlot(scope string, slot int64) int {
	if s == nil {
		return 0
	}
	if scope == "" || scope == "ALL" {
		return s.All.five(slot)
	}
	if isDefaultUserScope(scope) {
		return s.Scopes[UserScope].five(slot) + s.Scopes[LegacyUserScope].five(slot) + s.Scopes[legacyRenamedScope].five(slot) + s.Unscoped.five(keyOrSlot(slot))
	}
	return s.Scopes[scope].five(slot) + s.Unscoped.five(slot)
}

func keyOrSlot(slot int64) int64 { return slot }

// hookSlot 全机全部事件行的 5 分钟槽计数（任意事件类型）。
func (s *hookLogStat) hookSlot(slot int64) int {
	if s == nil {
		return 0
	}
	return s.FiveAll[slot]
}

func emptyHookLogStat(path string) *hookLogStat {
	return &hookLogStat{
		Path:     path,
		Latency:  []int{},
		Recent:   []HookEventItem{},
		FiveAll:  map[int64]int{},
		Scopes:   map[string]*scopeLogStat{},
		Unscoped: newScopeLogStat(),
		All:      newScopeLogStat(),
	}
}

// scanHookLog 全量扫描当前日志文件（hooks 侧 2 MiB 轮转 ⇒ 单次成本有上界）。
func scanHookLog(path string, now time.Time, loc *time.Location) *hookLogStat {
	stat := emptyHookLogStat(path)
	info, err := os.Stat(path)
	if err != nil {
		return stat
	}
	stat.Size = info.Size()
	stat.Mod = info.ModTime()

	handle, err := os.Open(path)
	if err != nil {
		return stat
	}
	defer handle.Close()

	nowUnix := now.Unix()
	scanLinesResilient(handle, 1024*1024, func(rawLine string) {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			return
		}
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) != nil {
			return
		}
		stat.Total++
		injected := asInt(m["injected"])
		written := asInt(m["written"])
		if injected > 0 || written > 0 {
			stat.Injected++
		}
		ev := asString(m["event"])
		ms := asInt(m["ms"])
		ts := int64(asInt(m["ts"]))
		if ms > 0 {
			stat.LastMS = ms
			if len(stat.Latency) >= hookLogLatencyMax {
				stat.Latency = append(stat.Latency[:0], stat.Latency[1:]...)
			}
			stat.Latency = append(stat.Latency, ms)
		}
		stat.Recent = append(stat.Recent, hookEventItemFrom(m, ts, ms))
		if len(stat.Recent) > hookLogRecentMax {
			stat.Recent = stat.Recent[len(stat.Recent)-hookLogRecentMax:]
		}
		if ts <= 0 || ts > nowUnix+hookLogFutureSkew {
			return
		}
		t := time.Unix(ts, 0).In(loc)
		if now.Sub(t) <= hookLogSlotRetain {
			stat.FiveAll[t.Unix()-t.Unix()%hookLogSlotSec]++
		}
		if ev != "recall" {
			return
		}
		stat.All.add(ts, now, loc)
		group := stat.Unscoped
		if label := strings.TrimSpace(asString(m["scope"])); label != "" {
			group = stat.scopeGroup(label)
		}
		group.add(ts, now, loc)
	})
	return stat
}

func hookEventItemFrom(m map[string]any, ts int64, ms int) HookEventItem {
	item := HookEventItem{
		Timestamp: ts,
		TimeText:  time.Unix(ts, 0).Format("15:04:05"),
		Event:     asString(m["event"]),
		Injected:  asInt(m["injected"]),
		Bands:     asInt(m["bands"]),
		Raw:       asInt(m["raw"]),
		ElapsedMS: ms,
		Chars:     asInt(m["chars"]),
		Dedup:     asInt(m["dedup"]),
		Absent:    asInt(m["absent"]),
		Nudge:     asInt(m["nudge"]),
		Imported:  asInt(m["imported"]),
		Replayed:  asInt(m["replayed"]),
		Written:   asInt(m["written"]),
	}
	item.Outcome = classifyHookOutcome(item)
	return item
}

var (
	hookLogMu    sync.Mutex
	hookLogCache = map[string]*hookLogStat{}
)

// loadHookLogStat 读取 hook 日志聚合（(size, mtime) 未变时复用缓存）。
func loadHookLogStat(path string) *hookLogStat {
	info, err := os.Stat(path)
	if err != nil {
		return emptyHookLogStat(path)
	}
	hookLogMu.Lock()
	defer hookLogMu.Unlock()
	if cached := hookLogCache[path]; cached != nil && cached.Size == info.Size() && cached.Mod.Equal(info.ModTime()) {
		return cached
	}
	stat := scanHookLog(path, time.Now(), time.Local)
	hookLogCache[path] = stat
	return stat
}
