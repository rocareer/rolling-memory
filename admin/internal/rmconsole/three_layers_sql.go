// plugin: rolling-memory v0.0.58
// three_layers_sql.go —— 三层认知记忆浏览器（L1 Background / L2 Episodic / L3 Noetic）的只读 SQL 下推层。
//
// 三层语义（不得破坏）：
//
//	· L1 Background = 用户自己挂载的本地目录文件底物（thin_vault_bg_dirs → thin_vault_bg_files → thin_vault_bg_chunks）；
//	· L2 Episodic   = Harness 对话记录单元（thin_vault_docs，layer='episodic'，空 layer 按旧口径回退为 episodic）；
//	· L3 Noetic     = Agent 提问 + 检索证据固化（thin_vault_noetic，cited_unit_ids_json 指回 L1/L2 单元）。
//
// 三层不是三张同构表，而是「两张物理表 + 一个派生层标签」；本文件把它们投影成一张统一视图，
// 让筛选 / 排序 / 分页全部在 SQLite 侧完成。早期实现把三张表的全量行读进 Go、在内存里过滤排序分页，
// 每次翻页 / 排序都退化成 O(全库)，这里改为 O(页大小)。
package rollingmemory

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// 三层记忆的规范层名（与前端 layerBrowser 的 layer 取值、以及引擎 thin_vault_docs.layer 取值一致）。
const (
	backgroundLayer = "background"
	episodicLayer   = "episodic"
	noeticLayer     = "noetic"
)

// layerListQuery 三层统一列表的只读查询参数（全部来自 BuildAdmin TableManager 契约）。
type layerListQuery struct {
	layer   string // "" | background | episodic | noetic
	status  string // "" | active | stale（与历史口径一致：只作用于 L3）
	dirID   string // L1 挂载目录过滤
	relPath string // L1 相对路径过滤（小写）
	keyword string // 页内快搜关键词（小写）
	wheres  []tableWhereGroup
	sort    string
	order   string
	limit   int
	offset  int
}

// countMetric 只读取一个标量计数（列必须别名为 cnt）；缺表 / 缺库 / 解析失败一律归零，
// 绝不在读路径创建任何表或文件。
func countMetric(dbPath, query string, dst *int) {
	if dst == nil {
		return
	}
	rows, err := sqliteQueryJSON(dbPath, query)
	if err != nil || len(rows) == 0 {
		*dst = 0
		return
	}
	*dst = asInt(rows[0]["cnt"])
}

// noeticCitedLinkCount L3 活跃引证链总数（仅统计 status='active' 的结晶，与 ROI 监控屏口径一致）。
func noeticCitedLinkCount(dbPath string) int {
	rows, err := sqliteQueryJSON(dbPath, `SELECT cited_unit_ids_json FROM thin_vault_noetic WHERE COALESCE(NULLIF(status, ''), 'active') = 'active';`)
	if err != nil {
		return 0
	}
	total := 0
	for _, r := range rows {
		var cited []string
		if json.Unmarshal([]byte(asString(r["cited_unit_ids_json"])), &cited) == nil {
			total += len(cited)
		}
	}
	return total
}

type layerGlobalCounts struct {
	bgDirs       int
	bgFiles      int
	bgChunks     int
	bgCount      int
	epCount      int
	epCells      int
	noeticActive int
	noeticStale  int
	noeticLinks  int
}

var (
	layerGlobalCountsMu    sync.Mutex
	layerGlobalCountsCache = map[string]struct {
		sig    string
		counts layerGlobalCounts
	}{}
)

func fillCachedLayerGlobalCounts(dbPath string, resp *LayerStatsResponse) {
	if resp == nil {
		return
	}
	sig := facetsFileSignature(dbPath)
	layerGlobalCountsMu.Lock()
	if cached, ok := layerGlobalCountsCache[dbPath]; ok && cached.sig == sig {
		c := cached.counts
		layerGlobalCountsMu.Unlock()
		resp.BackgroundDirCount = c.bgDirs
		resp.BackgroundFileCount = c.bgFiles
		resp.BackgroundChunkCount = c.bgChunks
		resp.BackgroundCount = c.bgCount
		resp.EpisodicCount = c.epCount
		resp.EpisodicCellCount = c.epCells
		resp.NoeticActiveCount = c.noeticActive
		resp.NoeticStaleCount = c.noeticStale
		resp.NoeticCitedLinks = c.noeticLinks
		return
	}
	layerGlobalCountsMu.Unlock()

	var c layerGlobalCounts
	countMetric(dbPath, `SELECT COUNT(*) AS cnt FROM thin_vault_bg_dirs`, &c.bgDirs)
	countMetric(dbPath, `SELECT COUNT(*) AS cnt FROM thin_vault_bg_files`, &c.bgFiles)
	countMetric(dbPath, `SELECT COUNT(*) AS cnt FROM thin_vault_bg_chunks`, &c.bgChunks)
	var bgDocsCnt int
	if rows, err := sqliteQueryJSON(dbPath, `
		SELECT
			COALESCE(SUM(CASE WHEN lower(trim(COALESCE(layer,''))) = 'background' THEN 1 ELSE 0 END), 0) AS bg_doc_cnt,
			COALESCE(SUM(CASE WHEN layer = 'episodic' OR COALESCE(layer, '') = '' THEN 1 ELSE 0 END), 0) AS ep_cnt,
			COUNT(DISTINCT CASE WHEN (layer = 'episodic' OR COALESCE(layer, '') = '') AND address_uri != '' THEN substr(address_uri, 1, instr(address_uri || '#', '#') - 1) END) AS ep_cells
		FROM thin_vault_docs;
	`); err == nil && len(rows) > 0 {
		bgDocsCnt = asInt(rows[0]["bg_doc_cnt"])
		c.epCount = asInt(rows[0]["ep_cnt"])
		c.epCells = asInt(rows[0]["ep_cells"])
	}
	c.bgCount = bgDocsCnt + c.bgChunks
	if rows, err := sqliteQueryJSON(dbPath, `SELECT COALESCE(NULLIF(status, ''), 'active') AS st, cited_unit_ids_json FROM thin_vault_noetic;`); err == nil {
		for _, r := range rows {
			if asString(r["st"]) == "active" {
				c.noeticActive++
				var cited []string
				if json.Unmarshal([]byte(asString(r["cited_unit_ids_json"])), &cited) == nil {
					c.noeticLinks += len(cited)
				}
			} else {
				c.noeticStale++
			}
		}
	}
	layerGlobalCountsMu.Lock()
	layerGlobalCountsCache[dbPath] = struct {
		sig    string
		counts layerGlobalCounts
	}{sig: sig, counts: c}
	layerGlobalCountsMu.Unlock()

	resp.BackgroundDirCount = c.bgDirs
	resp.BackgroundFileCount = c.bgFiles
	resp.BackgroundChunkCount = c.bgChunks
	resp.BackgroundCount = c.bgCount
	resp.EpisodicCount = c.epCount
	resp.EpisodicCellCount = c.epCells
	resp.NoeticActiveCount = c.noeticActive
	resp.NoeticStaleCount = c.noeticStale
	resp.NoeticCitedLinks = c.noeticLinks
}

// vaultTablePresent 探测金库是否具备某张表（只读 sqlite_master 查询）。
func vaultTablePresent(dbPath, table string) bool {
	rows, err := sqliteQueryJSON(dbPath, `SELECT name AS cnt FROM sqlite_master WHERE type IN ('table','view') AND name = `+sqlQuote(table)+` LIMIT 1;`)
	return err == nil && len(rows) > 0
}

// vaultHasDocPointers 探测 thin_vault_docs 在指定层下是否存在 @ref: 零拷贝指针行。
func vaultHasDocPointers(dbPath, layer string) bool {
	if !vaultTablePresent(dbPath, "thin_vault_docs") {
		return false
	}
	if layer == backgroundLayer {
		rows, err := sqliteQueryJSON(dbPath, `SELECT 1 AS cnt FROM thin_vault_docs WHERE lower(trim(COALESCE(layer, ''))) = 'background' AND substr(ltrim(raw_text), 1, 5) = '@ref:' LIMIT 1;`)
		return err == nil && len(rows) > 0
	}
	rows, err := sqliteQueryJSON(dbPath, `SELECT 1 AS cnt FROM thin_vault_docs WHERE substr(ltrim(raw_text), 1, 5) = '@ref:' LIMIT 1;`)
	return err == nil && len(rows) > 0
}

type pointerDocsCacheEntry struct {
	sig  string
	rows []map[string]any
}

var (
	pointerDocsCacheMu sync.Mutex
	pointerDocsCache   = map[string]pointerDocsCacheEntry{}
)

// cachedVaultPointerDocRows 按金库文件签名（facetsFileSignature）缓存 thin_vault_docs 中所有 @ref: 指针行的投影，
// 避免每次关键词搜索、正文筛选或按 title/content 排序都让 SQLite 全表扫描并 JSON 序列化数万条指针行（单次省 ~120ms）。
func cachedVaultPointerDocRows(dbPath string, q layerListQuery) ([]map[string]any, error) {
	includeDocs := q.layer == "" || q.layer == backgroundLayer || q.layer == episodicLayer
	if !includeDocs || !vaultTablePresent(dbPath, "thin_vault_docs") {
		return nil, nil
	}
	sig := facetsFileSignature(dbPath)
	pointerDocsCacheMu.Lock()
	cached, ok := pointerDocsCache[dbPath]
	if ok && cached.sig == sig {
		allRows := cached.rows
		pointerDocsCacheMu.Unlock()
		return filterCachedPointerRows(allRows, q), nil
	}
	pointerDocsCacheMu.Unlock()

	const ptrSQL = `
		SELECT
			CASE
				WHEN lower(trim(COALESCE(layer, ''))) = 'background' THEN 'background'
				WHEN lower(trim(COALESCE(layer, ''))) = 'noetic' THEN 'noetic'
				ELSE 'episodic'
			END AS layer,
			'doc' AS src,
			doc_id AS id,
			'active' AS status,
			raw_text AS title,
			raw_text AS content,
			'' AS answer_text,
			COALESCE(address_uri, '') AS address_uri,
			'' AS rel_path, '' AS anchor, '' AS abs_path,
			occurred_at AS occurred_at,
			'' AS cited_json
		FROM thin_vault_docs
		WHERE substr(ltrim(raw_text), 1, 5) = '@ref:'`
	allRows, err := sqliteQueryJSON(dbPath, ptrSQL)
	if err != nil {
		return nil, err
	}
	pointerDocsCacheMu.Lock()
	pointerDocsCache[dbPath] = pointerDocsCacheEntry{sig: sig, rows: allRows}
	pointerDocsCacheMu.Unlock()
	return filterCachedPointerRows(allRows, q), nil
}

func filterCachedPointerRows(allRows []map[string]any, q layerListQuery) []map[string]any {
	if len(allRows) == 0 {
		return nil
	}
	excludeBG := q.dirID != "" || q.relPath != ""
	if q.layer == "" && !excludeBG {
		return allRows
	}
	out := make([]map[string]any, 0, len(allRows))
	for _, r := range allRows {
		lyr := asString(r["layer"])
		if excludeBG && lyr == backgroundLayer {
			continue
		}
		if q.layer == episodicLayer && (lyr == backgroundLayer || lyr == noeticLayer) {
			continue
		}
		if q.layer == backgroundLayer && lyr != backgroundLayer {
			continue
		}
		out = append(out, r)
	}
	return out
}

// queryLayerRowsWithHydratedRefs 在存在 @ref: 零拷贝指针且带关键词/正文 wheres/正文排序时，
// 先在 SQL 侧过滤 L1/L3 与非指针行，再结合内存缓存的 @ref: 指针候选水合会话正文，执行与 SQL 完全同构的过滤、排序与分页。
func queryLayerRowsWithHydratedRefs(dbPath string, q layerListQuery, home string) ([]ThreeLayerRow, int, error) {
	ptrRows, err := cachedVaultPointerDocRows(dbPath, q)
	if err != nil {
		return nil, 0, err
	}
	union, unionArgs := layerUnionSQLMode(dbPath, q, true)
	var plainRows []map[string]any
	if q.offset < 0 {
		q.offset = 0
	}
	overrideTotal := -1
	isPureContentSort := q.keyword == "" && len(q.wheres) == 0 && hydratedSortField(q.sort) &&
		q.dirID == "" && q.relPath == "" && q.status == "" &&
		q.limit > 0 && q.offset >= 0 && q.offset+q.limit <= 1000 &&
		(q.layer == "" || q.layer == backgroundLayer || q.layer == episodicLayer || q.layer == noeticLayer)

	if union != "" {
		rawSQL := "SELECT layer, src, id, status, title, content, answer_text, address_uri, rel_path, anchor, abs_path, occurred_at, cited_json FROM (" + union + ") AS u"
		if isPureContentSort {
			// 纯按 title/content 排序且无过滤条件时：非指针行（L1 分块 / L3 结晶）的 title/content 在 SQL 侧已是终态，
			// 直接在 SQL 侧按排序方向截断前 offset+limit 行，避免把 5.6 万条 L1 分块全表搬进 Go 内存排序。
			rawSQL += layerOrderSQL(q.sort, q.order) + fmt.Sprintf(" LIMIT %d", q.offset+q.limit)
			var tmp LayerStatsResponse
			fillCachedLayerGlobalCounts(dbPath, &tmp)
			switch q.layer {
			case backgroundLayer:
				overrideTotal = tmp.BackgroundCount
			case episodicLayer:
				overrideTotal = tmp.EpisodicCount
			case noeticLayer:
				overrideTotal = tmp.NoeticActiveCount + tmp.NoeticStaleCount
			default:
				overrideTotal = tmp.BackgroundCount + tmp.EpisodicCount + tmp.NoeticActiveCount + tmp.NoeticStaleCount
			}
		}
		plainRows, err = sqliteQueryJSONArgs(dbPath, rawSQL, unionArgs...)
		if err != nil {
			return nil, 0, err
		}
	}
	if len(plainRows) == 0 && len(ptrRows) == 0 {
		return []ThreeLayerRow{}, 0, nil
	}
	kw := strings.ToLower(strings.TrimSpace(q.keyword))
	candPtrs := ptrRows
	if len(candPtrs)+len(plainRows) > 512 {
		candPtrs = pruneLargeVaultHydrationCandidates(dbPath, home, candPtrs, kw, q.wheres)
	}
	rows := make([]map[string]any, 0, len(plainRows)+len(candPtrs))
	rows = append(rows, plainRows...)
	rows = append(rows, candPtrs...)
	warmHydrateZCodePointersBatch(home, rows)
	warmHydrateLocalDocumentsBatch(home, rows)

	// 快速分页路径：当排序字段不依赖水合正文且无 wheres 时，非 @ref: 行在 SQL 侧已精确过滤，
	// 仅需校验 @ref: 行的关键词命中，按原始投影行排序切片后只对当前页（limit 行）构造 ThreeLayerRow，
	// 省去对数千条非当前页命中行的 clipRunes / formatUnixTime / JSON 反序列化开销。
	if len(q.wheres) == 0 && !hydratedSortField(q.sort) {
		matchedRaw := make([]map[string]any, 0, len(rows))
		for _, r := range rows {
			raw := strings.TrimSpace(asString(r["content"]))
			if !strings.HasPrefix(raw, "@ref:") {
				matchedRaw = append(matchedRaw, r)
				continue
			}
			if kw != "" {
				resolved, ok := hydratedEpisodicPointerLive(home, raw)
				if !ok {
					resolved = ""
				}
				id := strings.TrimSpace(asString(r["id"]))
				addr := strings.TrimSpace(asString(r["address_uri"]))
				if !strings.Contains(strings.ToLower(id), kw) &&
					!strings.Contains(strings.ToLower(addr), kw) &&
					(resolved == "" || !strings.Contains(strings.ToLower(resolved), kw)) {
					continue
				}
			}
			matchedRaw = append(matchedRaw, r)
		}
		sortRawLayerRows(matchedRaw, q.sort, q.order)
		total := len(matchedRaw)
		if total == 0 || q.limit <= 0 || q.offset >= total {
			return []ThreeLayerRow{}, total, nil
		}
		end := q.offset + q.limit
		if end > total {
			end = total
		}
		pageSlice := matchedRaw[q.offset:end]
		out := make([]ThreeLayerRow, 0, len(pageSlice))
		for _, r := range pageSlice {
			out = append(out, layerRowFromSQL(r, home))
		}
		return out, total, nil
	}

	filtered := make([]ThreeLayerRow, 0, len(rows))
	for _, r := range rows {
		row := layerRowFromSQL(r, home)
		if kw != "" {
			hay := strings.ToLower(row.ID + " " + row.Title + " " + row.Content + " " + row.AddressURI + " " + row.RelPath + " " + strings.Join(row.CitedUnitIDs, " "))
			if !strings.Contains(hay, kw) {
				continue
			}
		}
		if !matchHydratedLayerWheres(row, q.wheres) {
			continue
		}
		filtered = append(filtered, row)
	}
	sortHydratedLayerRows(filtered, q.sort, q.order)
	total := len(filtered)
	if overrideTotal >= 0 {
		total = overrideTotal
	}
	if total == 0 || q.limit <= 0 || q.offset >= len(filtered) {
		return []ThreeLayerRow{}, total, nil
	}
	end := q.offset + q.limit
	if end > len(filtered) {
		end = len(filtered)
	}
	return filtered[q.offset:end], total, nil
}

type recallCueCacheEntry struct {
	ids map[string]bool
	at  time.Time
}

var (
	recallCueCacheMu sync.Mutex
	recallCueCache   = map[string]recallCueCacheEntry{}
)

// pruneLargeVaultHydrationCandidates 在上万条 @ref: 指针的生产大库上通过本机常驻引擎倒排索引与内存缓存收敛待水合候选集，
// 避免单次列表请求对数万条零拷贝指针触发 GB 级宿主库全表扫描导致 25s+ 超时。
func pruneLargeVaultHydrationCandidates(dbPath, home string, rows []map[string]any, kw string, wheres []tableWhereGroup) []map[string]any {
	searchCue := kw
	if searchCue == "" {
		for _, g := range wheres {
			for _, cond := range g.Wheres {
				if v := strings.TrimSpace(asString(cond.Value)); v != "" {
					searchCue = strings.ToLower(v)
					break
				}
			}
			if searchCue != "" {
				break
			}
		}
	}

	// 先统计尚未进入内存水合缓存的 @ref: 指针数：若未缓存指针本身已 ≤64 条，无需再调守护进程倒排索引。
	uncachedRefs := 0
	for _, r := range rows {
		raw := strings.TrimSpace(asString(r["content"]))
		if strings.HasPrefix(raw, "@ref:") {
			if _, ok := readHydrateCache(hydrateCacheKey(hydrateFlavorLive, home, raw)); !ok {
				uncachedRefs++
			}
		}
	}

	var matchedIDs map[string]bool
	daemonResponded := false
	matchedTurns := make(map[string]bool, 64)
	if searchCue != "" && uncachedRefs > 64 {
		cacheKey := dbPath + "\x00" + searchCue
		now := time.Now()
		recallCueCacheMu.Lock()
		if entry, ok := recallCueCache[cacheKey]; ok && now.Sub(entry.at) < 45*time.Second {
			matchedIDs = entry.ids
			daemonResponded = true
		}
		recallCueCacheMu.Unlock()

		if matchedIDs == nil {
			matchedIDs = make(map[string]bool, 64)
			scope := resolveBoundVaultScope(dbPath, UserScope)
			extractIDs := func(raw map[string]any, dst map[string]bool) {
				if bList, ok := raw["bands"].([]any); ok {
					for _, bAny := range bList {
						if bMap, ok := bAny.(map[string]any); ok {
							if id := strings.TrimSpace(asString(bMap["id"])); id != "" {
								dst[id] = true
							}
							if nodes, ok := bMap["nodes"].([]any); ok {
								for _, nAny := range nodes {
									if nMap, ok := nAny.(map[string]any); ok {
										if nid := strings.TrimSpace(asString(nMap["id"])); nid != "" {
											dst[nid] = true
										}
									}
								}
							}
						}
					}
				}
				if rHits, ok := raw["raw_hits"].([]any); ok {
					for _, rAny := range rHits {
						if rMap, ok := rAny.(map[string]any); ok {
							if tr := strings.TrimSpace(asString(rMap["trace"])); tr != "" {
								dst[tr] = true
							}
						}
					}
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 220*time.Millisecond)
			raw, handled, dErr := tryConsoleDaemonRecall(ctx, home, dbPath, scope, "owner", "full", 8, 8, 1024, 1, 8, searchCue, "", true, false, "", "", "")
			cancel()
			if handled && dErr == nil && raw != nil {
				daemonResponded = true
				extractIDs(raw, matchedIDs)
				recallCueCacheMu.Lock()
				if len(recallCueCache) > 256 {
					recallCueCache = map[string]recallCueCacheEntry{}
				}
				recallCueCache[cacheKey] = recallCueCacheEntry{ids: matchedIDs, at: time.Now()}
				recallCueCacheMu.Unlock()
			} else {
				// 守护进程在 220ms 预算内未返回：先写入短占位避免并发重入，并在后台完成召回写入缓存。
				recallCueCacheMu.Lock()
				recallCueCache[cacheKey] = recallCueCacheEntry{ids: matchedIDs, at: time.Now()}
				recallCueCacheMu.Unlock()
				go func(h, db, sc, cue, ck string) {
					bgCtx, bgCancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer bgCancel()
					if bgRaw, ok, err := tryConsoleDaemonRecall(bgCtx, h, db, sc, "owner", "full", 16, 16, 2048, 1, 16, cue, "", true, false, "", "", ""); ok && err == nil && bgRaw != nil {
						bgIDs := make(map[string]bool, 64)
						extractIDs(bgRaw, bgIDs)
						recallCueCacheMu.Lock()
						recallCueCache[ck] = recallCueCacheEntry{ids: bgIDs, at: time.Now()}
						recallCueCacheMu.Unlock()
					}
				}(home, dbPath, scope, searchCue, cacheKey)
			}
		}
	}
	if len(matchedIDs) > 0 {
		for _, r := range rows {
			id := strings.TrimSpace(asString(r["id"]))
			if matchedIDs[id] {
				raw := strings.TrimSpace(asString(r["content"]))
				if hash := strings.LastIndex(raw, "#"); hash > 5 {
					matchedTurns[raw[:hash]] = true
				}
			}
		}
	}

	out := make([]map[string]any, 0, 256)
	uncachedKeep := 0
	const maxUncachedHydrateCap = 64
	const maxUncachedFallback = 16
	for _, r := range rows {
		raw := strings.TrimSpace(asString(r["content"]))
		if !strings.HasPrefix(raw, "@ref:") {
			out = append(out, r)
			continue
		}
		id := strings.TrimSpace(asString(r["id"]))
		addr := strings.TrimSpace(asString(r["address_uri"]))
		if cachedText, ok := readHydrateCache(hydrateCacheKey(hydrateFlavorLive, home, raw)); ok {
			if kw != "" && len(wheres) == 0 {
				effText := cachedText
				if effText == "" {
					effText = raw
				}
				if strings.Contains(strings.ToLower(id), kw) ||
					strings.Contains(strings.ToLower(addr), kw) ||
					strings.Contains(strings.ToLower(effText), kw) {
					out = append(out, r)
				}
				continue
			}
			out = append(out, r)
			continue
		}
		if uncachedKeep >= maxUncachedHydrateCap {
			continue
		}
		base := raw
		if hash := strings.LastIndex(raw, "#"); hash > 5 {
			base = raw[:hash]
		}
		if matchedIDs[id] || matchedTurns[base] {
			uncachedKeep++
			out = append(out, r)
			continue
		}
		// 仅匹配 id 与 address_uri（绝不匹配未水合的 @ref:zcode:/@ref:jsonl: 指针字面量，
		// 否则搜 "zcode" / "jsonl" / 路径名会把全库数万条指针全部送入同步水合）。
		if kw != "" && strings.Contains(strings.ToLower(id+" "+addr), kw) {
			uncachedKeep++
			out = append(out, r)
			continue
		}
		fallbackCap := maxUncachedFallback
		if kw == "" && len(wheres) == 0 {
			fallbackCap = maxUncachedHydrateCap
		}
		if !daemonResponded && len(matchedIDs) == 0 && uncachedKeep < fallbackCap {
			uncachedKeep++
			out = append(out, r)
		}
	}
	return out
}

func matchHydratedLayerWheres(row ThreeLayerRow, groups []tableWhereGroup) bool {
	for _, g := range groups {
		if len(g.Wheres) == 0 {
			continue
		}
		active := 0
		matched := false
		allMatched := true
		for _, cond := range g.Wheres {
			ok, valid := matchHydratedLayerCond(row, cond)
			if !valid {
				continue
			}
			active++
			if ok {
				matched = true
			} else {
				allMatched = false
			}
		}
		if active == 0 {
			continue
		}
		if g.Or {
			if !matched {
				return false
			}
		} else if !allMatched {
			return false
		}
	}
	return true
}

func matchHydratedLayerCond(row ThreeLayerRow, cond tableWhereCondition) (bool, bool) {
	field := strings.ToLower(strings.TrimSpace(cond.Field))
	op := strings.ToUpper(strings.TrimSpace(cond.Operator))
	if op == "" {
		op = "EQ"
	}
	val := strings.TrimSpace(asString(cond.Value))
	if val == "" && op != "IN" {
		return true, false
	}
	if op == "BETWEEN" && (field == "time_text" || field == "occurred_at") {
		parts := strings.SplitN(val, ",", 2)
		if len(parts) != 2 {
			return true, false
		}
		start, okStart := parseLayerTimeBound(parts[0], false)
		end, okEnd := parseLayerTimeBound(parts[1], true)
		if !okStart && !okEnd {
			return true, false
		}
		if !okStart {
			start = 0
		}
		if !okEnd {
			end = int64(1) << 62
		}
		return row.OccurredAt >= start && row.OccurredAt <= end, true
	}
	var fieldVal string
	switch field {
	case "id":
		fieldVal = row.ID
	case "layer":
		fieldVal = row.Layer
	case "status":
		fieldVal = row.Status
	case "title":
		fieldVal = row.Title
	case "content":
		fieldVal = row.Content + " " + row.Title + " " + row.AnswerText
	case "answer_text":
		fieldVal = row.AnswerText
	case "address_uri":
		fieldVal = row.AddressURI + " " + strings.Join(row.CitedUnitIDs, " ")
	case "cited_unit_ids":
		fieldVal = strings.Join(row.CitedUnitIDs, " ")
	case "rel_path":
		fieldVal = row.RelPath
	case "anchor":
		fieldVal = row.Anchor
	case "time_text", "occurred_at":
		fieldVal = row.TimeText
	default:
		fieldVal = row.ID + " " + row.Title + " " + row.Content + " " + row.AddressURI + " " + row.RelPath
	}
	fv := strings.ToLower(fieldVal)
	lv := strings.ToLower(val)
	switch op {
	case "EQ", "=":
		if field == "cited_unit_ids" || field == "address_uri" {
			return fv == lv || strings.Contains(fv, lv), true
		}
		return fv == lv, true
	case "NE", "!=", "<>":
		return fv != lv, true
	case "LIKE", "ILIKE":
		return strings.Contains(fv, lv), true
	case "NOT LIKE", "NOT ILIKE":
		return !strings.Contains(fv, lv), true
	case "IN":
		var targets []string
		if arr, ok := cond.Value.([]any); ok {
			for _, it := range arr {
				if s := strings.ToLower(strings.TrimSpace(asString(it))); s != "" {
					targets = append(targets, s)
				}
			}
		} else {
			for _, part := range strings.Split(lv, ",") {
				if s := strings.TrimSpace(part); s != "" {
					targets = append(targets, s)
				}
			}
		}
		if len(targets) == 0 {
			return true, false
		}
		for _, t := range targets {
			if fv == t {
				return true, true
			}
		}
		return false, true
	default:
		return strings.Contains(fv, lv), true
	}
}

func sortHydratedLayerRows(rows []ThreeLayerRow, sortField, sortOrder string) {
	field := strings.ToLower(strings.TrimSpace(sortField))
	asc := strings.EqualFold(strings.TrimSpace(sortOrder), "asc")
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		var cmp int
		switch field {
		case "id":
			cmp = strings.Compare(a.ID, b.ID)
		case "layer":
			cmp = strings.Compare(a.Layer, b.Layer)
		case "status":
			cmp = strings.Compare(a.Status, b.Status)
		case "title":
			cmp = strings.Compare(a.Title, b.Title)
		case "content":
			cmp = strings.Compare(a.Content, b.Content)
		case "answer_text":
			cmp = strings.Compare(a.AnswerText, b.AnswerText)
		case "address_uri":
			cmp = strings.Compare(a.AddressURI, b.AddressURI)
		case "rel_path":
			cmp = strings.Compare(a.RelPath, b.RelPath)
		case "anchor":
			cmp = strings.Compare(a.Anchor, b.Anchor)
		default:
			if a.OccurredAt < b.OccurredAt {
				cmp = -1
			} else if a.OccurredAt > b.OccurredAt {
				cmp = 1
			}
		}
		if cmp == 0 {
			return a.ID < b.ID
		}
		if asc {
			return cmp < 0
		}
		return cmp > 0
	})
}

func sortRawLayerRows(rows []map[string]any, sortField, sortOrder string) {
	field := strings.ToLower(strings.TrimSpace(sortField))
	asc := strings.EqualFold(strings.TrimSpace(sortOrder), "asc")
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		var cmp int
		switch field {
		case "id", "layer", "status", "answer_text", "address_uri", "rel_path", "anchor":
			cmp = strings.Compare(asString(a[field]), asString(b[field]))
		default:
			oa := int64(asInt(a["occurred_at"]))
			ob := int64(asInt(b["occurred_at"]))
			if oa < ob {
				cmp = -1
			} else if oa > ob {
				cmp = 1
			}
		}
		if cmp == 0 {
			return asString(a["id"]) < asString(b["id"])
		}
		if asc {
			return cmp < 0
		}
		return cmp > 0
	})
}

// hydratedSortField 报告该排序字段是否只有在水合后才具备正确定序：
// title / content 在指针行上与 raw_text（@ref: 指针文本）不同，SQL 侧排序会按指针文本比较；
// 其余字段（id / layer / status / rel_path / anchor / occurred_at / time_text / answer_text）
// 两路取值一致，无需为此整体水合。
func hydratedSortField(sortField string) bool {
	switch strings.ToLower(strings.TrimSpace(sortField)) {
	case "title", "content":
		return true
	default:
		return false
	}
}

// wheresNeedHydration 报告高级筛选条件是否涉及水合后才会改变的正文列（title / content 或默认复合字段）：
// 若仅按 layer / id / status / time_text / occurred_at / rel_path / anchor / address_uri / cited_unit_ids 筛选，
// SQL 下推与水合后过滤结果 100% 恒等，无需全表水合。
func wheresNeedHydration(groups []tableWhereGroup) bool {
	for _, g := range groups {
		for _, cond := range g.Wheres {
			if strings.TrimSpace(asString(cond.Value)) == "" && !strings.EqualFold(strings.TrimSpace(cond.Operator), "IN") {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(cond.Field)) {
			case "id", "layer", "status", "answer_text", "address_uri", "cited_unit_ids", "rel_path", "anchor", "time_text", "occurred_at":
				continue
			default:
				return true
			}
		}
	}
	return false
}

// queryLayerRows 执行三层统一列表查询：返回当前页行（已格式化为 ThreeLayerRow）与过滤后总数。
func queryLayerRows(dbPath string, q layerListQuery, home string) ([]ThreeLayerRow, int, error) {
	// 1. 指针水合路径只在「查询范围含 docs 层、且必须读到水合正文」时才启用：
	//    L3（noetic）不含 @ref: 指针；关键词、涉及 title/content 的高级筛选、以及按 title/content 排序
	//    才依赖水合后的正文；其余筛选与排序（含表头默认的 occurred_at）直接由 SQL 下推完成。
	if q.layer != noeticLayer && vaultHasDocPointers(dbPath, q.layer) && (q.keyword != "" || wheresNeedHydration(q.wheres) || hydratedSortField(q.sort)) {
		return queryLayerRowsWithHydratedRefs(dbPath, q, home)
	}

	whereSQL, whereArgs := layerWheresSQL(q.wheres)
	if whereSQL == "" {
		whereSQL = "1=1"
	}

	sortField := strings.ToLower(strings.TrimSpace(q.sort))
	isUnfilteredDefaultSort := whereSQL == "1=1" &&
		q.keyword == "" &&
		q.dirID == "" &&
		q.relPath == "" &&
		(q.status == "" || (q.layer == noeticLayer && (q.status == "active" || q.status == "stale"))) &&
		(sortField == "" || sortField == "occurred_at" || sortField == "time_text" || sortField == "id") &&
		(q.layer == "" || q.layer == backgroundLayer || q.layer == episodicLayer || q.layer == noeticLayer)

	if isUnfilteredDefaultSort && q.limit > 0 && q.offset >= 0 && q.offset+q.limit <= 1000 {
		var tmp LayerStatsResponse
		fillCachedLayerGlobalCounts(dbPath, &tmp)
		var total int
		switch q.layer {
		case backgroundLayer:
			total = tmp.BackgroundCount
		case episodicLayer:
			total = tmp.EpisodicCount
		case noeticLayer:
			if q.status == "active" {
				total = tmp.NoeticActiveCount
			} else if q.status == "stale" {
				total = tmp.NoeticStaleCount
			} else {
				total = tmp.NoeticActiveCount + tmp.NoeticStaleCount
			}
		default:
			total = tmp.BackgroundCount + tmp.EpisodicCount + tmp.NoeticActiveCount + tmp.NoeticStaleCount
		}
		if total == 0 {
			return []ThreeLayerRow{}, 0, nil
		}
		branchCap := q.offset + q.limit
		dir := "DESC"
		if strings.EqualFold(strings.TrimSpace(q.order), "asc") {
			dir = "ASC"
		}
		noeticOrder := fmt.Sprintf("occurred_at %s, noetic_id ASC", dir)
		docOrder := fmt.Sprintf("occurred_at %s, doc_id ASC", dir)
		chunkOrder := fmt.Sprintf("occurred_at %s, unit_id ASC", dir)
		if sortField == "id" {
			noeticOrder = fmt.Sprintf("noetic_id %s", dir)
			docOrder = fmt.Sprintf("doc_id %s", dir)
			chunkOrder = fmt.Sprintf("unit_id %s", dir)
		}
		var branches []string
		want := func(name string) bool { return q.layer == "" || q.layer == name }
		if want(noeticLayer) && vaultTablePresent(dbPath, "thin_vault_noetic") {
			noeticWhere := "1=1"
			if q.status == "active" {
				noeticWhere = "COALESCE(NULLIF(status, ''), 'active') = 'active'"
			} else if q.status == "stale" {
				noeticWhere = "COALESCE(NULLIF(status, ''), 'active') = 'stale'"
			}
			branches = append(branches, fmt.Sprintf(`SELECT * FROM (
					SELECT 'noetic' AS layer, 'noetic' AS src, noetic_id AS id,
					       COALESCE(NULLIF(status, ''), 'active') AS status,
					       query_text AS title,
					       ('Q: ' || query_text || char(10) || 'A: ' || answer_text) AS content,
					       answer_text AS answer_text,
					       '' AS address_uri, '' AS rel_path, '' AS anchor, '' AS abs_path,
					       occurred_at AS occurred_at,
					       COALESCE(cited_unit_ids_json, '[]') AS cited_json
					FROM thin_vault_noetic
					WHERE %s
					ORDER BY %s LIMIT %d
				)`, noeticWhere, noeticOrder, branchCap))
		}
		if (q.layer == "" || q.layer == backgroundLayer || q.layer == episodicLayer) && vaultTablePresent(dbPath, "thin_vault_docs") {
			docWhere := "1=1"
			if q.layer == episodicLayer {
				docWhere = "lower(trim(COALESCE(layer, ''))) NOT IN ('background', 'noetic')"
			} else if q.layer == backgroundLayer {
				docWhere = "lower(trim(COALESCE(layer, ''))) = 'background'"
			}
			branches = append(branches, fmt.Sprintf(`SELECT * FROM (
				SELECT CASE
				         WHEN lower(trim(COALESCE(layer, ''))) = 'background' THEN 'background'
				         WHEN lower(trim(COALESCE(layer, ''))) = 'noetic' THEN 'noetic'
				         ELSE 'episodic'
				       END AS layer,
				       'doc' AS src, doc_id AS id, 'active' AS status,
				       raw_text AS title, raw_text AS content, '' AS answer_text,
				       COALESCE(address_uri, '') AS address_uri,
				       '' AS rel_path, '' AS anchor, '' AS abs_path,
				       occurred_at AS occurred_at, '' AS cited_json
				FROM thin_vault_docs
				WHERE %s
				ORDER BY %s LIMIT %d
			)`, docWhere, docOrder, branchCap))
		}
		if want(backgroundLayer) && vaultTablePresent(dbPath, "thin_vault_bg_chunks") {
			branches = append(branches, fmt.Sprintf(`SELECT * FROM (
				SELECT 'background' AS layer, 'chunk' AS src, unit_id AS id, 'active' AS status,
				       (rel_path || ' #' || anchor) AS title,
				       ('[' || rel_path || '#' || anchor || '] (' || abs_path || ')') AS content,
				       '' AS answer_text,
				       COALESCE(address_uri, '') AS address_uri,
				       rel_path AS rel_path, anchor AS anchor, abs_path AS abs_path,
				       occurred_at AS occurred_at, '' AS cited_json
				FROM thin_vault_bg_chunks
				ORDER BY %s LIMIT %d
			)`, chunkOrder, branchCap))
		}
		if len(branches) > 0 {
			fastPageSQL := "SELECT layer, src, id, status, title, content, answer_text, address_uri, rel_path, anchor, abs_path, occurred_at, cited_json FROM (" +
				strings.Join(branches, " UNION ALL ") + ")" + layerOrderSQL(q.sort, q.order) + " LIMIT ? OFFSET ?"
			if rows, fErr := sqliteQueryJSONArgs(dbPath, fastPageSQL, q.limit, q.offset); fErr == nil {
				warmHydrateZCodePointersBatch(home, rows)
				warmHydrateLocalDocumentsBatch(home, rows)
				items := make([]ThreeLayerRow, 0, len(rows))
				for _, r := range rows {
					items = append(items, layerRowFromSQL(r, home))
				}
				return items, total, nil
			}
		}
	}

	union, unionArgs := layerUnionSQL(dbPath, q)
	if union == "" {
		return []ThreeLayerRow{}, 0, nil
	}

	if q.offset == 0 && q.limit > 0 {
		probeSQL := "SELECT layer, src, id, status, title, content, answer_text, address_uri, rel_path, anchor, abs_path, occurred_at, cited_json" +
			" FROM (" + union + ") AS u WHERE " + whereSQL + layerOrderSQL(q.sort, q.order) + " LIMIT ?"
		probeArgs := append(append(append([]any{}, unionArgs...), whereArgs...), q.limit+1)
		if probeRows, pErr := sqliteQueryJSONArgs(dbPath, probeSQL, probeArgs...); pErr == nil && len(probeRows) <= q.limit {
			warmHydrateZCodePointersBatch(home, probeRows)
			warmHydrateLocalDocumentsBatch(home, probeRows)
			items := make([]ThreeLayerRow, 0, len(probeRows))
			for _, r := range probeRows {
				items = append(items, layerRowFromSQL(r, home))
			}
			return items, len(probeRows), nil
		}
	}

	countSQL := "SELECT COUNT(*) AS cnt FROM (" + union + ") AS u WHERE " + whereSQL
	countRows, err := sqliteQueryJSONArgs(dbPath, countSQL, append(append([]any{}, unionArgs...), whereArgs...)...)
	if err != nil {
		return nil, 0, err
	}
	total := 0
	if len(countRows) > 0 {
		total = asInt(countRows[0]["cnt"])
	}
	if total == 0 || q.limit <= 0 {
		return []ThreeLayerRow{}, total, nil
	}

	pageSQL := "SELECT layer, src, id, status, title, content, answer_text, address_uri, rel_path, anchor, abs_path, occurred_at, cited_json" +
		" FROM (" + union + ") AS u WHERE " + whereSQL + layerOrderSQL(q.sort, q.order) + " LIMIT ? OFFSET ?"
	pageArgs := append(append(append([]any{}, unionArgs...), whereArgs...), q.limit, q.offset)
	rows, err := sqliteQueryJSONArgs(dbPath, pageSQL, pageArgs...)
	if err != nil {
		return nil, 0, err
	}
	warmHydrateZCodePointersBatch(home, rows)
	warmHydrateLocalDocumentsBatch(home, rows)
	items := make([]ThreeLayerRow, 0, len(rows))
	for _, r := range rows {
		items = append(items, layerRowFromSQL(r, home))
	}
	return items, total, nil
}

// layerUnionSQL 按请求的三层子集拼装 UNION ALL 投影；占位符参数与返回的 select 顺序严格一致。
func layerUnionSQL(dbPath string, q layerListQuery) (string, []any) {
	return layerUnionSQLMode(dbPath, q, false)
}

func layerUnionSQLMode(dbPath string, q layerListQuery, excludeDocPointers bool) (string, []any) {
	var selects []string
	var args []any
	want := func(name string) bool { return q.layer == "" || q.layer == name }

	// ── L3 Noetic：Agent 提问 + 检索证据固化 ───────────────────────────────────────────
	if want(noeticLayer) && vaultTablePresent(dbPath, "thin_vault_noetic") {
		sel := `
			SELECT
				'noetic' AS layer, 'noetic' AS src,
				noetic_id AS id,
				COALESCE(NULLIF(status, ''), 'active') AS status,
				query_text AS title,
				('Q: ' || query_text || char(10) || 'A: ' || answer_text) AS content,
				answer_text AS answer_text,
				'' AS address_uri, '' AS rel_path, '' AS anchor, '' AS abs_path,
				occurred_at AS occurred_at,
				COALESCE(cited_unit_ids_json, '[]') AS cited_json
			FROM thin_vault_noetic
			WHERE (? = '' OR COALESCE(NULLIF(status, ''), 'active') = ?)`
		args = append(args, q.status, q.status)
		if q.keyword != "" {
			sel += ` AND instr(lower(noetic_id || ' ' || 'Q: ' || query_text || char(10) || 'A: ' || answer_text || ' ' || COALESCE(cited_unit_ids_json, '')), ?) > 0`
			args = append(args, q.keyword)
		}
		selects = append(selects, sel)
	}

	// ── L2 Episodic（thin_vault_docs 中的 episodic 行）与 L1 Background 独立文档 ────────
	// 只有请求明确指向这三层之一时才纳入 docs 分支：无法识别的层名一律按「无匹配」处置，
	// 绝不把未知层名静默放大成全表（与历史「未知层 → 0 行」口径一致）。
	includeDocs := q.layer == "" || q.layer == backgroundLayer || q.layer == episodicLayer
	if includeDocs && vaultTablePresent(dbPath, "thin_vault_docs") {
		sel := `
			SELECT
				CASE
					WHEN lower(trim(COALESCE(layer, ''))) = 'background' THEN 'background'
					WHEN lower(trim(COALESCE(layer, ''))) = 'noetic' THEN 'noetic'
					ELSE 'episodic'
				END AS layer,
				'doc' AS src,
				doc_id AS id,
				'active' AS status,
				raw_text AS title,
				raw_text AS content,
				'' AS answer_text,
				COALESCE(address_uri, '') AS address_uri,
				'' AS rel_path, '' AS anchor, '' AS abs_path,
				occurred_at AS occurred_at,
				'' AS cited_json
			FROM thin_vault_docs
			WHERE NOT (lower(trim(COALESCE(layer, ''))) = 'background' AND (? <> '' OR ? <> ''))`
		// 指定了挂载目录 / 相对路径时，thin_vault_docs 中的独立背景文档不属于任何 dir_id，按历史口径剔除。
		args = append(args, q.dirID, q.relPath)
		switch q.layer {
		case episodicLayer:
			sel += ` AND lower(trim(COALESCE(layer, ''))) NOT IN ('background', 'noetic')`
		case backgroundLayer:
			sel += ` AND lower(trim(COALESCE(layer, ''))) = 'background'`
		}
		if excludeDocPointers {
			sel += ` AND substr(ltrim(raw_text), 1, 5) <> '@ref:'`
		}
		if q.keyword != "" {
			sel += ` AND instr(lower(doc_id || ' ' || raw_text || ' ' || COALESCE(address_uri, '')), ?) > 0`
			args = append(args, q.keyword)
		}
		selects = append(selects, sel)
	}

	// ── L1 Background：挂载目录文件分块（目录 → 文件 → 分块 的第三级）────────────────────
	if want(backgroundLayer) && vaultTablePresent(dbPath, "thin_vault_bg_chunks") {
		sel := `
			SELECT
				'background' AS layer, 'chunk' AS src,
				unit_id AS id,
				'active' AS status,
				(rel_path || ' #' || anchor) AS title,
				('[' || rel_path || '#' || anchor || '] (' || abs_path || ')') AS content,
				'' AS answer_text,
				COALESCE(address_uri, '') AS address_uri,
				rel_path AS rel_path, anchor AS anchor, abs_path AS abs_path,
				occurred_at AS occurred_at,
				'' AS cited_json
			FROM thin_vault_bg_chunks`
		cond := []string{}
		if q.dirID != "" && vaultTablePresent(dbPath, "thin_vault_bg_files") {
			cond = append(cond, `abs_path IN (SELECT abs_path FROM thin_vault_bg_files WHERE dir_id = ?)`)
			args = append(args, q.dirID)
		}
		if q.relPath != "" {
			cond = append(cond, `instr(lower(rel_path), ?) > 0`)
			args = append(args, q.relPath)
		}
		if q.keyword != "" {
			cond = append(cond, `instr(lower(unit_id || ' ' || rel_path || ' #' || anchor || ' [' || rel_path || '#' || anchor || '] (' || abs_path || ') ' || COALESCE(address_uri, '')), ?) > 0`)
			args = append(args, q.keyword)
		}
		if len(cond) > 0 {
			sel += " WHERE " + strings.Join(cond, " AND ")
		}
		selects = append(selects, sel)
	}

	if len(selects) == 0 {
		return "", nil
	}
	return strings.Join(selects, "\n\t\t\tUNION ALL"), args
}

// layerOrderSQL 排序下推：列映射 + 与旧口径一致的稳定次序（同值时按 id 升序）。
func layerOrderSQL(sortField, sortOrder string) string {
	field := strings.ToLower(strings.TrimSpace(sortField))
	expr := "occurred_at"
	switch field {
	case "id", "layer", "status", "title", "content", "answer_text", "address_uri", "rel_path", "anchor":
		expr = field
	}
	dir := "DESC"
	if strings.EqualFold(strings.TrimSpace(sortOrder), "asc") {
		dir = "ASC"
	}
	return " ORDER BY " + expr + " " + dir + ", id ASC"
}

// layerFieldExpr 与旧 Go 侧 matchSingleTableWhere 的字段口径逐一对齐（内容 / 地址含复合列）。
func layerFieldExpr(field string) string {
	switch field {
	case "id":
		return "id"
	case "layer":
		return "layer"
	case "status":
		return "status"
	case "title":
		return "title"
	case "content":
		return "content || ' ' || title || ' ' || answer_text"
	case "answer_text":
		return "answer_text"
	case "address_uri":
		return "address_uri || ' ' || cited_json"
	case "cited_unit_ids":
		return "cited_json"
	case "rel_path":
		return "rel_path"
	case "anchor":
		return "anchor"
	case "time_text", "occurred_at":
		return "strftime('%Y-%m-%d %H:%M:%S', occurred_at, 'unixepoch', 'localtime')"
	default:
		return "id || ' ' || title || ' ' || content || ' ' || address_uri || ' ' || rel_path"
	}
}

// layerWheresSQL 把 BuildAdmin 的 wheres 分组（组间 AND、组内按 Or 决定 OR/AND）编译成 SQL。
func layerWheresSQL(groups []tableWhereGroup) (string, []any) {
	var parts []string
	var args []any
	for _, g := range groups {
		if len(g.Wheres) == 0 {
			continue
		}
		var conds []string
		for _, cond := range g.Wheres {
			expr, condArgs, ok := layerWhereCondSQL(cond)
			if !ok {
				continue
			}
			conds = append(conds, expr)
			args = append(args, condArgs...)
		}
		if len(conds) == 0 {
			continue
		}
		joiner := " AND "
		if g.Or {
			joiner = " OR "
		}
		parts = append(parts, "("+strings.Join(conds, joiner)+")")
	}
	if len(parts) == 0 {
		return "", nil
	}
	return strings.Join(parts, " AND "), args
}

// layerWhereCondSQL 单条件编译。空值一律视为「不过滤」（与历史 Go 口径一致）。
func layerWhereCondSQL(cond tableWhereCondition) (string, []any, bool) {
	field := strings.ToLower(strings.TrimSpace(cond.Field))
	op := strings.ToUpper(strings.TrimSpace(cond.Operator))
	if op == "" {
		op = "EQ"
	}
	val := strings.TrimSpace(asString(cond.Value))
	if val == "" && op != "IN" {
		return "", nil, false
	}
	expr := layerFieldExpr(field)
	switch op {
	case "EQ", "=":
		// 引证 / 地址列按历史口径为「等于或包含」（同一单元可能引证多条）。
		if field == "cited_unit_ids" || field == "address_uri" {
			return "(lower(" + expr + ") = lower(?) OR instr(lower(" + expr + "), lower(?)) > 0)", []any{val, val}, true
		}
		return "lower(" + expr + ") = lower(?)", []any{val}, true
	case "NE", "!=", "<>":
		return "lower(" + expr + ") <> lower(?)", []any{val}, true
	case "LIKE", "ILIKE":
		return "instr(lower(" + expr + "), lower(?)) > 0", []any{val}, true
	case "NOT LIKE", "NOT ILIKE":
		return "instr(lower(" + expr + "), lower(?)) = 0", []any{val}, true
	case "IN":
		if arr, ok := cond.Value.([]any); ok {
			phs := make([]string, 0, len(arr))
			args := make([]any, 0, len(arr))
			for _, it := range arr {
				if s := strings.TrimSpace(asString(it)); s != "" {
					phs = append(phs, "?")
					args = append(args, s)
				}
			}
			if len(phs) == 0 {
				return "", nil, false
			}
			return "lower(" + expr + ") IN (" + strings.Join(phs, ", ") + ")", args, true
		}
		var conds []string
		var args []any
		for _, part := range strings.Split(val, ",") {
			if s := strings.TrimSpace(part); s != "" {
				conds = append(conds, "lower("+expr+") = lower(?)")
				args = append(args, s)
			}
		}
		if len(conds) == 0 {
			return "", nil, false
		}
		return "(" + strings.Join(conds, " OR ") + ")", args, true
	case "BETWEEN":
		parts := strings.SplitN(val, ",", 2)
		if len(parts) != 2 {
			return "", nil, false
		}
		start, okStart := parseLayerTimeBound(parts[0], false)
		end, okEnd := parseLayerTimeBound(parts[1], true)
		if !okStart && !okEnd {
			return "", nil, false
		}
		if !okStart {
			start = 0
		}
		if !okEnd {
			end = int64(1) << 62
		}
		// 时间区间一律按 epoch 数值比较：纯日期输入（YYYY-MM-DD）作上界时补齐到当日 23:59:59，
		// 修掉早期实现用字符串比较导致「单日筛选查不到当天行」的缺陷。
		return "occurred_at BETWEEN ? AND ?", []any{start, end}, true
	default:
		return "instr(lower(" + expr + "), lower(?)) > 0", []any{val}, true
	}
}

// parseLayerTimeBound 解析时间边界：支持完整日期时间与纯日期；纯日期作上界时补到当日 23:59:59。
func parseLayerTimeBound(raw string, upperBound bool) (int64, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, false
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02 15:04", "2006-01-02"} {
		t, err := time.ParseInLocation(layout, s, time.Local)
		if err != nil {
			continue
		}
		if layout == "2006-01-02" && upperBound {
			t = t.Add(24*time.Hour - time.Second)
		}
		return t.Unix(), true
	}
	return 0, false
}

// layerRowFromSQL 把统一投影行还原为三层浏览行（严格按 src 分派，保持三层各自的展示口径）。
func layerRowFromSQL(r map[string]any, home string) ThreeLayerRow {
	layer := strings.ToLower(strings.TrimSpace(asString(r["layer"])))
	occ := int64(asInt(r["occurred_at"]))
	row := ThreeLayerRow{
		ID:         asString(r["id"]),
		Layer:      layer,
		Status:     asString(r["status"]),
		AddressURI: asString(r["address_uri"]),
		OccurredAt: occ,
		TimeText:   formatUnixTime(occ, false),
	}
	switch strings.ToLower(strings.TrimSpace(asString(r["src"]))) {
	case "noetic":
		// L3：提问 + 证据固化；cited_unit_ids 指回 L1/L2 单元（前端据此跳转 / 重放）。
		q := asString(r["title"])
		a := asString(r["answer_text"])
		var cited []string
		_ = json.Unmarshal([]byte(asString(r["cited_json"])), &cited)
		row.Title = q
		row.Content = "Q: " + q + "\nA: " + a
		row.AnswerText = a
		row.CitedUnitIDs = cited
	case "chunk":
		// L1：挂载目录文件分块（rel_path#anchor 是文件内定位，abs_path 是磁盘真身）。
		// 正文按源文件磁盘现场读取（守护进程正典，先经 warmHydrateLocalDocumentsBatch 批量预热）；
		// 只有源文件被移动/删除或读取引擎未就绪时才退化为位置预览，并显式标记正文不可读，
		// 绝不拿路径坐标冒充分块正文。
		relP := asString(r["rel_path"])
		anchor := asString(r["anchor"])
		row.RelPath = relP
		row.Anchor = anchor
		row.Title = fmt.Sprintf("%s #%s", relP, anchor)
		if body, ok := hydratedLocalDocumentLive(home, asString(r["id"])); ok {
			row.Content = body
		} else {
			row.Content = fmt.Sprintf("[%s#%s] (%s)", relP, anchor, asString(r["abs_path"]))
			row.BodyUnavailable = true
		}
	default:
		// L2（或 background 的独立文档）：对话记录原文；@ref: 指针按历史口径实时水合，
		// 无法水合回「来源缺口」占位，绝不把指针原样当正文上屏。
		rawTxt := asString(r["content"])
		if strings.HasPrefix(strings.TrimSpace(rawTxt), "@ref:") {
			if resolved, ok := hydratedEpisodicPointerLive(home, rawTxt); ok && resolved != "" {
				rawTxt = resolved
			} else {
				rawTxt = vaultRefGapMarker
			}
		}
		row.Title = clipRunes(rawTxt, 64)
		row.Content = rawTxt
	}
	return row
}
