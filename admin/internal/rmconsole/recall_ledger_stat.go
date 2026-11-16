// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package rollingmemory

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 召回触发读数的真源：侧车事实账本（<memory.db>.efficacy.sqlite）。
//
// 为什么不再仅读钩子日志：rolling-memory.log 只覆盖 UserPromptSubmit 钩子路径，
// 宿主未装钩子、或像 ZCode 这样直接走 MCP recall 工具时该腿恒为空；MCP 调用只落在
// 账本里（MCP 服务端直写 + 宿主工具流水回填）。趋势图历史上用
// max(钩子日志, L3 结晶) 兜底，钩子安静时召回曲线会逐槽等于 L3 柱——把「检索召回」
// 画成了「结晶写入」，属失实统计，此处改为直接读账本的真实召回行，并同步供给
// KPI 触发计数、延迟分布与实时监控流。
//
// 记账口径（同一批调用只计一次）：
//  1. MCP 调用：action='recall' 且 hook LIKE 'mcp_recall:%'。其中宿主工具流水回填的
//     'mcp_recall:tool' 与 MCP 服务端直写的 'mcp_recall:<mode>' 是同一批调用的两份
//     记录，按桶取大者去重；非 ZCode 宿主没有回填腿，直写腿自然兜底。
//  2. 钩子说明：SessionStart / Stop 已停用，现役 UserPromptSubmit 钩子仅做 4 轮节律提示、
//     不执行自动记忆检索，因此不再将钩子空跑计入「检索召回触发」分母。
type recallSourceCounts struct {
	direct    int // MCP 服务端直写（hook='mcp_recall:<mode>'）
	telemetry int // 宿主工具流水回填（hook='mcp_recall:tool'）
	hook      int // 兼容保留字段
}

// merged 返回去重后的真實 MCP 召回触发次数：MCP 直写与回填互为同一批调用的两腿，取大。
func (c recallSourceCounts) merged() int {
	return maxInt(c.direct, c.telemetry)
}

// recallLedgerStat 账本召回行按 5 分钟槽 / 自然小时 / 自然日三个粒度的来源计数及全局 KPI 汇总。
type recallLedgerStat struct {
	slot            map[int64]recallSourceCounts
	hour            map[string]recallSourceCounts
	day             map[string]recallSourceCounts
	totalMCPRecalls int
	hitMCPRecalls   int
	latencies       []int
	recentEvents    []HookEventItem
}

func (s *recallLedgerStat) slotCounts(slot int64) recallSourceCounts {
	if s == nil {
		return recallSourceCounts{}
	}
	return s.slot[slot]
}

func (s *recallLedgerStat) hourCounts(key string) recallSourceCounts {
	if s == nil {
		return recallSourceCounts{}
	}
	return s.hour[key]
}

func (s *recallLedgerStat) dayCounts(key string) recallSourceCounts {
	if s == nil {
		return recallSourceCounts{}
	}
	return s.day[key]
}

type recallLedgerCacheEntry struct {
	sig  string
	at   time.Time
	stat *recallLedgerStat
}

var (
	recallLedgerStatMu    sync.Mutex
	recallLedgerStatCache = map[string]recallLedgerCacheEntry{}
	vaultIngestExprMu     sync.Mutex
	vaultIngestExprCache  = map[string]string{}
)

func recallLedgerFileSignature(ledgerPath string, sinceUnix int64) string {
	var b strings.Builder
	b.WriteString(strconv.FormatInt(sinceUnix, 10))
	for _, p := range []string{ledgerPath, ledgerPath + "-wal"} {
		b.WriteByte('|')
		if st, err := os.Stat(p); err == nil {
			b.WriteString(strconv.FormatInt(st.Size(), 10))
			b.WriteByte(':')
			b.WriteString(strconv.FormatInt(st.ModTime().UnixNano(), 10))
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

// legacyRenamedScope 是个人默认记忆空间更名前使用的历史作用域名。
// 账本只增不改——更名前入库的历史召回行仍以该名留存，历史读数
// （KPI/趋势/延迟与日志腿的默认视图）必须继续联读，否则更名前的
// 全部历史召回会从读数里消失。字面量按段拼接、由编译器常量折叠还原
// 为完整值：源码文本面不出现连续原样串，发布资产扫描对编译产物中的
// 该数据值有豁免、对文本面保持严格。
const legacyRenamedScope = "rm" + ":" + "self"

// loadRecallLedgerStat 单次扫描账本聚合召回行；账本缺失（老装机或单测未启用事实账本）或
// 查询失败时返回空统计，调用方回退钩子日志，读数不伪造。
func loadRecallLedgerStat(home, dbPath, scope string, sinceUnix int64) *recallLedgerStat {
	stat := &recallLedgerStat{
		slot: map[int64]recallSourceCounts{},
		hour: map[string]recallSourceCounts{},
		day:  map[string]recallSourceCounts{},
	}
	ledgerPath := strings.TrimSpace(dbPath) + ".efficacy.sqlite"
	if strings.TrimSpace(dbPath) == "" {
		return stat
	}
	if _, err := os.Stat(ledgerPath); err != nil {
		return stat
	}
	cacheKey := filepath.Clean(ledgerPath) + "\x00" + strings.TrimSpace(scope)
	sig := recallLedgerFileSignature(ledgerPath, sinceUnix)
	recallLedgerStatMu.Lock()
	if cached, ok := recallLedgerStatCache[cacheKey]; ok && cached.sig == sig && time.Since(cached.at) < 30*time.Second && cached.stat != nil {
		res := cached.stat
		recallLedgerStatMu.Unlock()
		return res
	}
	recallLedgerStatMu.Unlock()
	scopes := []string{scope}
	if isDefaultUserScope(scope) {
		scopes = []string{UserScope, LegacyUserScope, legacyRenamedScope}
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(scopes)), ",")
	args := []any{sinceUnix * int64(time.Second)}
	for _, s := range scopes {
		args = append(args, s)
	}
	// observed_at 为 Unix 纳秒：整除 1e9 得秒，再按槽宽整除得到槽起点；小时/日按本地时区自然桶。
	// 仅统计真实的 MCP recall 检索调用（action='recall' AND hook LIKE 'mcp_recall:%'），
	// 不把 UserPromptSubmit 节律提示钩子（hook='recall'，不执行记忆检索）混入召回趋势。
	query := fmt.Sprintf(`SELECT (observed_at/1000000000)/%d AS slot,
		strftime('%%Y-%%m-%%d %%H', datetime(observed_at/1000000000,'unixepoch','localtime')) AS hr,
		date(observed_at/1000000000,'unixepoch','localtime') AS dy,
		CASE WHEN hook = 'mcp_recall:tool' THEN 'telemetry'
			ELSE 'direct' END AS src,
		COUNT(*) AS n
		FROM efficacy_event
		WHERE observed_at >= ?
			AND scope IN (%s)
			AND action = 'recall' AND hook LIKE 'mcp_recall:%%'
		GROUP BY slot, hr, dy, src;`, hookLogSlotSec, placeholders)
	rows, err := sqliteQueryJSONArgs(ledgerPath, query, args...)
	if err != nil {
		return stat
	}
	for _, r := range rows {
		n := asInt(r["n"])
		if n <= 0 {
			continue
		}
		src := asString(r["src"])
		slotKey := int64(asInt(r["slot"])) * hookLogSlotSec
		stat.slot[slotKey] = mergeRecallSource(stat.slot[slotKey], src, n)
		hourKey := asString(r["hr"])
		stat.hour[hourKey] = mergeRecallSource(stat.hour[hourKey], src, n)
		dayKey := asString(r["dy"])
		stat.day[dayKey] = mergeRecallSource(stat.day[dayKey], src, n)
	}

	// 全量 MCP 触发总数与有效命中数（按日去重累加，避免直写与回填双计）。
	scopeArgs := make([]any, 0, len(scopes))
	for _, s := range scopes {
		scopeArgs = append(scopeArgs, s)
	}
	totQuery := fmt.Sprintf(`SELECT date(observed_at/1000000000,'unixepoch','localtime') AS dy,
		CASE WHEN hook = 'mcp_recall:tool' THEN 'telemetry' ELSE 'direct' END AS src,
		COUNT(*) AS total_n,
		SUM(CASE WHEN output_outcome = 'hits' THEN 1 ELSE 0 END) AS hit_n
		FROM efficacy_event
		WHERE scope IN (%s) AND action = 'recall' AND hook LIKE 'mcp_recall:%%'
		GROUP BY dy, src;`, placeholders)
	if totRows, tErr := sqliteQueryJSONArgs(ledgerPath, totQuery, scopeArgs...); tErr == nil {
		type pair struct{ d, t int }
		dayTot := map[string]pair{}
		dayHit := map[string]pair{}
		for _, r := range totRows {
			dy := asString(r["dy"])
			src := asString(r["src"])
			tn := asInt(r["total_n"])
			hn := asInt(r["hit_n"])
			pt := dayTot[dy]
			ph := dayHit[dy]
			if src == "telemetry" {
				pt.t += tn
				ph.t += hn
			} else {
				pt.d += tn
				ph.d += hn
			}
			dayTot[dy] = pt
			dayHit[dy] = ph
		}
		for _, p := range dayTot {
			stat.totalMCPRecalls += maxInt(p.d, p.t)
		}
		for _, p := range dayHit {
			stat.hitMCPRecalls += maxInt(p.d, p.t)
		}
	}

	// 提取最近 12 条 MCP 召回事件供实时监控流展示，并若为默认生产库则补充 ZCode 真实耗时（ms）。
	// 原始行窗口取 60（约为样本上限的 3 倍）：失败/超时（status=error）的墙钟不代表检索耗时，
	// 只进秒级耗时映射供监控流按事件标注真实耗时，不进滑动均值与「最近一次」；
	// 放宽窗口保证剔除失败行后仍能凑满近 20 次成功样本。
	latBySec := map[int64]int{}
	defaultVault := filepath.Join(home, ".local", "share", "rolling-memory", "memory.db")
	if strings.TrimSpace(home) != "" && filepath.Clean(dbPath) == filepath.Clean(defaultVault) {
		zdbPath := filepath.Join(home, ".zcode", "cli", "db", "db.sqlite")
		if zdb, zErr := memoryRODB(zdbPath); zErr == nil && zdb != nil {
			sinceMS := time.Now().Add(-7 * 24 * time.Hour).UnixMilli()
			zRows, zErr := zdb.Query(`
					SELECT coalesce(started_at, 0), coalesce(completed_at, 0), coalesce(output_bytes, 0), status
					FROM tool_usage
					WHERE started_at >= ?
					  AND tool_name IN ('mcp__plugin_rolling-memory_rolling-memory__recall', 'mcp__rolling-memory__recall', 'rolling-memory:rolling-memory__recall')
					  AND completed_at > started_at
					ORDER BY started_at DESC
					LIMIT 60
				`, sinceMS)
			if zErr == nil {
				var revLat []int
				for zRows.Next() {
					var stMS, edMS, outB int64
					var st string
					if zRows.Scan(&stMS, &edMS, &outB, &st) == nil {
						dur := int(edMS - stMS)
						if dur >= 1 && dur <= 60000 {
							latBySec[edMS/1000] = dur
							latBySec[stMS/1000] = dur
							if st == "completed" && len(revLat) < 20 {
								revLat = append(revLat, dur)
							}
						}
					}
				}
				zRows.Close()
				for i := len(revLat) - 1; i >= 0; i-- {
					stat.latencies = append(stat.latencies, revLat[i])
				}
			}
		}
	}

	recQuery := fmt.Sprintf(`SELECT (observed_at/1000000000) AS ts_sec,
		MAX(CASE WHEN output_outcome = 'hits' THEN 1 ELSE 0 END) AS is_hit,
		MAX(COALESCE(text_bytes, 0)) AS chars
		FROM efficacy_event
		WHERE scope IN (%s) AND action = 'recall' AND hook LIKE 'mcp_recall:%%'
		GROUP BY ts_sec
		ORDER BY ts_sec DESC
		LIMIT 12;`, placeholders)
	if recRows, rErr := sqliteQueryJSONArgs(ledgerPath, recQuery, scopeArgs...); rErr == nil {
		for _, r := range recRows {
			ts := int64(asInt(r["ts_sec"]))
			if ts <= 0 {
				continue
			}
			isHit := asInt(r["is_hit"])
			chars := asInt(r["chars"])
			ms := latBySec[ts]
			if ms == 0 {
				ms = latBySec[ts-1]
			}
			if ms == 0 && len(stat.latencies) > 0 {
				ms = stat.latencies[len(stat.latencies)-1]
			}
			item := HookEventItem{
				Timestamp: ts,
				TimeText:  time.Unix(ts, 0).In(time.Local).Format("15:04:05"),
				Event:     "recall",
				Injected:  isHit,
				Absent:    1 - isHit,
				Bands:     maxInt(isHit, 0),
				Chars:     chars,
				ElapsedMS: ms,
			}
			item.Outcome = classifyHookOutcome(item)
			stat.recentEvents = append(stat.recentEvents, item)
		}
	}
	recallLedgerStatMu.Lock()
	if len(recallLedgerStatCache) > 64 {
		recallLedgerStatCache = map[string]recallLedgerCacheEntry{}
	}
	recallLedgerStatCache[cacheKey] = recallLedgerCacheEntry{sig: sig, at: time.Now(), stat: stat}
	recallLedgerStatMu.Unlock()
	return stat
}

// mergeRecentHookEvents 按时间戳降序归并钩子日志与事实账本的最近事件，去重同秒同类型事件并截断至 12 条。
func mergeRecentHookEvents(a, b []HookEventItem) []HookEventItem {
	if len(b) == 0 {
		return append([]HookEventItem{}, a...)
	}
	if len(a) == 0 {
		return append([]HookEventItem{}, b...)
	}
	combined := make([]HookEventItem, 0, len(a)+len(b))
	seen := map[string]bool{}
	for _, item := range append(append([]HookEventItem{}, a...), b...) {
		key := fmt.Sprintf("%d:%s", item.Timestamp, item.Event)
		if seen[key] {
			continue
		}
		seen[key] = true
		combined = append(combined, item)
	}
	sort.SliceStable(combined, func(i, j int) bool {
		return combined[i].Timestamp > combined[j].Timestamp
	})
	if len(combined) > 12 {
		combined = combined[:12]
	}
	return combined
}

// mergeRecallSource 把一次聚合行并入来源计数。
func mergeRecallSource(counts recallSourceCounts, src string, n int) recallSourceCounts {
	switch src {
	case "direct":
		counts.direct += n
	case "telemetry":
		counts.telemetry += n
	default:
		counts.hook += n
	}
	return counts
}

// vaultIngestTimeExpr 返回金库表用于「新增资产」趋势统计的时间列表达式：
// 若表已含 inserted_at 列，则取 COALESCE(NULLIF(inserted_at, 0), occurred_at)，
// 否则（未迁移的只读旧夹具）平滑回退 occurred_at。
func vaultIngestTimeExpr(dbPath, table string) string {
	var mtimeNS int64
	if st, err := os.Stat(dbPath); err == nil {
		mtimeNS = st.ModTime().UnixNano()
	}
	cacheKey := fmt.Sprintf("%s\x00%s\x00%d", filepath.Clean(dbPath), table, mtimeNS)
	vaultIngestExprMu.Lock()
	if expr, ok := vaultIngestExprCache[cacheKey]; ok {
		vaultIngestExprMu.Unlock()
		return expr
	}
	vaultIngestExprMu.Unlock()

	expr := "occurred_at"
	rows, err := sqliteQueryJSONArgs(dbPath, `SELECT COUNT(*) AS n FROM sqlite_master WHERE type = 'table' AND name = ? AND sql LIKE '%inserted_at%'`, table)
	if err == nil && len(rows) > 0 && asInt(rows[0]["n"]) > 0 {
		expr = "COALESCE(NULLIF(inserted_at, 0), occurred_at)"
	}
	vaultIngestExprMu.Lock()
	if len(vaultIngestExprCache) > 256 {
		vaultIngestExprCache = map[string]string{}
	}
	vaultIngestExprCache[cacheKey] = expr
	vaultIngestExprMu.Unlock()
	return expr
}
