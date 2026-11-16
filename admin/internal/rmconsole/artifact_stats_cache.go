package rollingmemory

import (
	"database/sql"
	"os"
	"sync"
	"time"
)

// 控制台读数中的全库去重统计（thin_vault_docs ∪ thin_vault_bg_chunks 的 cell_uri 去重）
// 是整个控制台最贵的单条 SQL：全表 substr/instr + UNION + COUNT(DISTINCT) 排序聚合，
// 本机 8.4 万行实测 0.25–0.65s/次。该读数只用于展示（referent/eligible nodes），
// 不需要秒级新鲜度——否则浮标 1Hz 采样与状态帧会随金库每次写入把它整条重跑，
// 把后台服务长期钉在高 CPU。故按路径 + TTL 缓存；docs/noetic 等参与浮标增量的
// 轻量计数不在本缓存内，保持每次现算（3×COUNT(*) 实测仅 10–30ms）。
//
// TTL 取值推演：本读数在金库持续写入下实测增速 ~分钟级（5,020 cells 对 7.5 万行），
// 60s 错峰对展示无感知；零/退化态（库不存在、查询失败）回退上一次成功读数，
// 无历史读数时为 0（查询失败保持零值）；缓存键为本地单租户库路径，
// 基数极小无需淘汰；持锁期间完成计算，并发调用在过期点串行至多一次重算（单飞语义）。
const (
	artifactCellsCacheTTL = 60 * time.Second
	// walTruncateThresholdBytes = 16 MiB（SQLite 默认 wal_autocheckpoint=1000 约 4 MiB 的 4 倍）：
	// PASSIVE 自动 checkpoint 不会缩小磁盘上的 -wal 文件尺寸（本机曾累积至 271.6 MiB，
	// 拖慢每次只读查询的 _sqlite3WalFindFrame）。超过水位时在后台非阻塞触发一次 TRUNCATE。
	walTruncateThresholdBytes = 16 << 20
)

var (
	artifactCellsMu    sync.Mutex
	artifactCellsCache = map[string]*artifactCellsEntry{}
	walCheckpointBusy  sync.Mutex
)

type artifactCellsEntry struct {
	cells int
	at    time.Time
}

// maybeCheckpointVaultWAL 当 <dbPath>-wal 超过 16 MiB 时，在后台单飞执行一次短超时
// PRAGMA wal_checkpoint(TRUNCATE)，回收已落盘但未截断的 WAL 文件尺寸，绝不阻塞调用方。
func maybeCheckpointVaultWAL(dbPath string) {
	st, err := os.Stat(dbPath + "-wal")
	if err != nil || st.Size() < walTruncateThresholdBytes {
		return
	}
	if !walCheckpointBusy.TryLock() {
		return
	}
	go func(p string) {
		defer walCheckpointBusy.Unlock()
		db, err := sql.Open("sqlite", sqliteFileURI(p)+"?_pragma=busy_timeout(200)&_pragma=journal_mode(WAL)")
		if err != nil {
			return
		}
		defer db.Close()
		_, _ = db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	}(dbPath)
}

// cachedArtifactCells 返回全库去重 cell 数：TTL 内直接复用；过期重算并刷新缓存；
// 重算失败时回退到上一次成功读数（不归零），从未成功过则返回 0。
func cachedArtifactCells(dbPath string) int {
	artifactCellsMu.Lock()
	defer artifactCellsMu.Unlock()
	now := time.Now()
	if e := artifactCellsCache[dbPath]; e != nil && now.Sub(e.at) < artifactCellsCacheTTL {
		return e.cells
	}
	maybeCheckpointVaultWAL(dbPath)
	rows, err := sqliteQueryJSON(dbPath, `
		SELECT COUNT(DISTINCT cell_uri) AS cells_cnt
		FROM (
			SELECT substr(address_uri, 1, instr(address_uri || '#', '#') - 1) AS cell_uri FROM thin_vault_docs WHERE address_uri != ''
			UNION
			SELECT substr(address_uri, 1, instr(address_uri || '#', '#') - 1) AS cell_uri FROM thin_vault_bg_chunks WHERE address_uri != ''
		);
	`)
	if err != nil || len(rows) == 0 {
		if e := artifactCellsCache[dbPath]; e != nil {
			return e.cells
		}
		return 0
	}
	cells := asInt(rows[0]["cells_cnt"])
	artifactCellsCache[dbPath] = &artifactCellsEntry{cells: cells, at: now}
	return cells
}
