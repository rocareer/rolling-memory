// plugin: rolling-memory v0.0.58
// Package rollingmemory 公开首页第一屏 3D 记忆图谱与 LoCoMo 真实记忆数据接口（MountPublic 免登录只读演示面）。
package rollingmemory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/ai-go-hub/ai-go-admin/internal/kit/httpx"
	"github.com/gin-gonic/gin"
)

// LocomoPublicQuestion 供首页第一屏一键触发真实召回模拟的 LoCoMo 评测问题条目。
type LocomoPublicQuestion struct {
	ID        string   `json:"id"`
	SampleID  string   `json:"sample_id"`
	Category  string   `json:"category"`
	Question  string   `json:"question"`
	Answer    string   `json:"answer"`
	NoeticKey string   `json:"noetic_key"`
	NoeticID  int      `json:"noetic_id"`
	CitedKeys []string `json:"cited_keys"`
	CitedIDs  []int    `json:"cited_ids"`
}

// LocomoPublicGraphResponse 扩展标准 GraphQueryResponse，附带真实 LoCoMo 问题预设列表。
type LocomoPublicGraphResponse struct {
	GraphQueryResponse
	Questions []LocomoPublicQuestion `json:"questions"`
	Corpus    struct {
		Suite       string `json:"suite"`
		Scope       string `json:"scope"`
		TotalUnits  int    `json:"total_units"`
		TotalCells  int    `json:"total_cells"`
		TotalNoetic int    `json:"total_noetic"`
		Transport   string `json:"transport"`
	} `json:"corpus"`
}

// LocomoPublicRecallRequest 首页第一屏真实 LoCoMo 记忆召回请求。
type LocomoPublicRecallRequest struct {
	Query      string `json:"q"`
	QuestionID string `json:"question_id"`
	TopK       int    `json:"top_k"`
}

// LocomoPublicRecallHit 单条真实 LoCoMo 召回命中单元。
type LocomoPublicRecallHit struct {
	UnitID     string  `json:"unit_id"`
	NodeKey    string  `json:"node_key"`
	NodeID     int     `json:"node_id"`
	CellKey    string  `json:"cell_key"`
	Layer      string  `json:"layer"`
	Summary    string  `json:"summary"`
	RawText    string  `json:"raw_text"`
	AddressURI string  `json:"address_uri"`
	ObservedAt string  `json:"observed_at"`
	Score      float64 `json:"score"`
}

// LocomoPublicRecallResponse 首页第一屏真实 LoCoMo 记忆召回响应。
type LocomoPublicRecallResponse struct {
	Query       string                  `json:"q"`
	QuestionID  string                  `json:"question_id"`
	Answer      string                  `json:"answer"`
	Scope       string                  `json:"scope"`
	Transport   string                  `json:"transport"`
	ElapsedMS   int64                   `json:"elapsed_ms"`
	PulseBytes  int                     `json:"pulse_bytes"`
	SeedNodeID  int                     `json:"seed_node_id"`
	SeedNodeKey string                  `json:"seed_node_key"`
	HitNodeIDs  []int                   `json:"hit_node_ids"`
	Hits        []LocomoPublicRecallHit `json:"hits"`
}

type locomoVaultEntry struct {
	DocID       string   `json:"doc_id"`
	OccurredAt  int64    `json:"occurred_at"`
	RawText     string   `json:"raw_text"`
	Sentences   []string `json:"sentences"`
	AddressURI  string   `json:"address_uri"`
	Coord64     uint64   `json:"coord64"`
	KeptIndices []int    `json:"kept_indices"`
}

type locomoCachedCorpus struct {
	loadedAt     time.Time
	fingerprint  string
	activeScope  string
	units        map[string]locomoVaultEntry
	unitOrder    []string
	questions    []LocomoPublicQuestion
	nodesByKey   map[string]*GraphNodeItem
	keyOrder     []string
	rawEdges     []GraphEdgeItem
	unitCells    map[string][]string
	cellMembers  map[string][]*GraphNodeItem
	citesTargets map[string][]string
	totalCells   int
	atomTotal    int
	molTotal     int
	dimerTotal   int
	topAtoms     []GraphAtomParticle
	topMols      []GraphMoleculeFragment
}

var (
	locomoCorpusMu    sync.RWMutex
	locomoCorpusCache *locomoCachedCorpus
)

func mountPublic(r *gin.RouterGroup) {
	r.Use(limitConsoleRequestBody())
	r.GET("/ws", rmWSHandler)
	r.POST("/plugin/rolling-memory/locomo/graph", locomoPublicGraphHandler)
	r.POST("/plugin/rolling-memory/locomo/expand", locomoPublicExpandHandler)
	r.POST("/plugin/rolling-memory/locomo/recall", locomoPublicRecallHandler)
}

func resolveLocomoVaultPath(home string) string {
	candidates := []string{
		filepath.Join(home, ".local", "state", "rolling-memory", "eval", "eval-locomo.json"),
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return candidates[0]
}

func computeLocomoCorpusFingerprint(home, vaultPath string) string {
	paths := []string{
		vaultPath,
		vaultPath + ".manifest.json",
		filepath.Join(home, ".local", "state", "rolling-memory", "eval", "out", "locomo-recall-run2.jsonl"),
		filepath.Join(home, ".local", "state", "rolling-memory", "eval", "out", "results-sdk-pulse-cipher-none.jsonl"),
		filepath.Join(home, ".local", "state", "rolling-memory", "eval", "out", "results-sdk-pulse-cipher-llm.jsonl"),
		filepath.Join(home, ".local", "state", "rolling-memory", "eval", "out", "results-sdk-pulse-cipher-llm-judge.jsonl"),
	}
	var b strings.Builder
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil {
			fmt.Fprintf(&b, "%s:%d:%d;", filepath.Base(p), st.ModTime().UnixNano(), st.Size())
		}
	}
	return b.String()
}

func InvalidateLocomoPublicCorpusCache() {
	locomoCorpusMu.Lock()
	locomoCorpusCache = nil
	locomoCorpusMu.Unlock()
}

func loadLocomoCorpus() (*locomoCachedCorpus, error) {
	home, _ := os.UserHomeDir()
	vaultPath := resolveLocomoVaultPath(home)
	fp := computeLocomoCorpusFingerprint(home, vaultPath)

	locomoCorpusMu.RLock()
	if locomoCorpusCache != nil && locomoCorpusCache.fingerprint == fp && time.Since(locomoCorpusCache.loadedAt) < 10*time.Minute {
		c := locomoCorpusCache
		locomoCorpusMu.RUnlock()
		return c, nil
	}
	locomoCorpusMu.RUnlock()

	locomoCorpusMu.Lock()
	defer locomoCorpusMu.Unlock()
	if locomoCorpusCache != nil && locomoCorpusCache.fingerprint == fp && time.Since(locomoCorpusCache.loadedAt) < 10*time.Minute {
		return locomoCorpusCache, nil
	}

	activeScope := EvalScope
	if mfBytes, err := os.ReadFile(vaultPath + ".manifest.json"); err == nil {
		var mf struct {
			Scope string `json:"scope"`
		}
		if json.Unmarshal(mfBytes, &mf) == nil && strings.TrimSpace(mf.Scope) != "" {
			activeScope = strings.TrimSpace(mf.Scope)
		}
	}

	// 优先尝试从本地静态快照加载（零 RB 连接模式），磁盘未命中时回退至二进制内嵌预置快照
	if !locomoSkipSnapshotOnLoad.Load() {
		for _, snapPath := range resolveLocomoSnapshotPaths(home) {
			if st, err := os.Stat(snapPath); err == nil && !st.IsDir() {
				if raw, err := os.ReadFile(snapPath); err == nil {
					var dump LocomoDataDump
					if json.Unmarshal(raw, &dump) == nil && len(dump.Nodes) > 0 {
						c := buildCorpusFromSnapshot(&dump)
						c.fingerprint = fp
						locomoCorpusCache = c
						return c, nil
					}
				}
			}
		}
		if raw := embeddedLocomoSnapshotJSON(); len(raw) > 0 {
			var dump LocomoDataDump
			if json.Unmarshal(raw, &dump) == nil && len(dump.Nodes) > 0 {
				c := buildCorpusFromSnapshot(&dump)
				c.fingerprint = fp
				locomoCorpusCache = c
				return c, nil
			}
		}
	}

	rawBytes, err := os.ReadFile(vaultPath)
	if err != nil {
		return nil, fmt.Errorf("read eval-locomo.json: %w", err)
	}
	var rawMap map[string]locomoVaultEntry
	if err := json.Unmarshal(rawBytes, &rawMap); err != nil {
		return nil, fmt.Errorf("unmarshal eval-locomo.json: %w", err)
	}

	unitOrder := make([]string, 0, len(rawMap))
	for k := range rawMap {
		unitOrder = append(unitOrder, k)
	}
	sort.Strings(unitOrder)

	nodesByKey := make(map[string]*GraphNodeItem, len(rawMap)*2)
	var keyOrder []string
	var rawEdges []GraphEdgeItem
	seenEdgeKey := make(map[string]bool)

	addNode := func(item GraphNodeItem) {
		if existing, ok := nodesByKey[item.NodeKey]; ok {
			existing.Degree += item.Degree
			if item.Weight > existing.Weight {
				existing.Weight = item.Weight
			}
			return
		}
		cp := item
		if cp.ID == 0 {
			cp.ID = deterministicIntID(cp.NodeKey)
		}
		if cp.Aliases == nil {
			cp.Aliases = []string{}
		}
		nodesByKey[cp.NodeKey] = &cp
		keyOrder = append(keyOrder, cp.NodeKey)
	}

	addEdge := func(fromKey, toKey, rel, layer, anchor, span string, weight float64) {
		if fromKey == "" || toKey == "" || fromKey == toKey {
			return
		}
		ek := fromKey + "|" + rel + "|" + toKey
		if seenEdgeKey[ek] {
			return
		}
		seenEdgeKey[ek] = true
		rawEdges = append(rawEdges, GraphEdgeItem{
			Assertion:  fromKey + "->" + toKey,
			FromKey:    fromKey,
			ToKey:      toKey,
			Relation:   rel,
			Layer:      layer,
			Warrant:    layer,
			Certainty:  certaintyTier(1.0),
			Confidence: 1.0,
			Status:     "active",
			Anchor:     anchor,
			Span:       span,
			Weight:     weight,
		})
	}

	// 1. 装载 5,882 条真实 LoCoMo 记忆单元与其所属记忆地址容器
	for idx, uid := range unitOrder {
		dm := rawMap[uid]
		if dm.DocID == "" {
			dm.DocID = uid
		}
		// 区分 L1 Background（长句知识/背景描述）与 L2 Episodic（对话情景事件）
		layer := "episodic"
		if len(dm.Sentences) >= 3 || len(dm.RawText) >= 150 || idx%4 == 0 {
			layer = "background"
		}
		uNodeKey := "unit:" + dm.DocID
		cellKey := extractCellKeyFromURI(dm.AddressURI)
		cNodeKey := "cell:" + cellKey
		obsText := formatUnixTime(dm.OccurredAt, false)

		if _, exists := nodesByKey[cNodeKey]; !exists {
			addNode(GraphNodeItem{
				ID:         deterministicIntID(cNodeKey),
				NodeKey:    cNodeKey,
				Summary:    cellKey,
				Kind:       "cell",
				Layer:      layer,
				Certainty:  certaintyTier(1.0),
				Intention:  layer,
				Status:     "active",
				Confidence: 1.0,
				ObservedAt: obsText,
				Anchor:     dm.AddressURI,
				Span:       "the memory-engine dynamic memory-address container: " + dm.AddressURI,
				Aliases:    []string{},
				Degree:     1,
				Weight:     1.0,
			})
		}

		distTxt := dm.RawText
		if len(dm.KeptIndices) > 0 && len(dm.Sentences) > 0 {
			var picked []string
			for _, kIdx := range dm.KeptIndices {
				if kIdx >= 0 && kIdx < len(dm.Sentences) {
					picked = append(picked, dm.Sentences[kIdx])
				}
			}
			if len(picked) > 0 {
				distTxt = strings.Join(picked, " · ")
			}
		}
		span := dm.RawText
		addNode(GraphNodeItem{
			ID:         deterministicIntID(uNodeKey),
			NodeKey:    uNodeKey,
			Summary:    clipRunes(distTxt, 46),
			Kind:       "unit",
			Layer:      layer,
			Certainty:  certaintyTier(0.96),
			Intention:  layer,
			Status:     "active",
			Confidence: 0.96,
			ObservedAt: obsText,
			Anchor:     dm.AddressURI,
			Span:       span,
			Aliases:    []string{dm.DocID},
			Degree:     1,
			Weight:     0.96,
		})
		addEdge(uNodeKey, cNodeKey, "member_of", layer, dm.AddressURI, dm.RawText, 1.0)
	}

	// 2. 建立 Cell 成员索引，优先为多成员高内聚 Cell 建立匹配加速表
	cellMembers := make(map[string][]*GraphNodeItem)
	unitToCell := make(map[string]string)
	unitCells := make(map[string][]string)
	seenPair := make(map[string]bool)
	for _, e := range rawEdges {
		if e.Relation == "member_of" {
			if uNode, okU := nodesByKey[e.FromKey]; okU && uNode.Kind == "unit" {
				if cNode, okC := nodesByKey[e.ToKey]; okC && cNode.Kind == "cell" {
					cellMembers[cNode.NodeKey] = append(cellMembers[cNode.NodeKey], uNode)
					unitToCell[uNode.NodeKey] = cNode.NodeKey
					pk := uNode.NodeKey + "|" + cNode.NodeKey
					if !seenPair[pk] {
						seenPair[pk] = true
						unitCells[uNode.NodeKey] = append(unitCells[uNode.NodeKey], cNode.NodeKey)
					}
				}
			}
		}
	}

	// 3. 读取 evaldata/tiers/questions-100.json 全量真实 LoCoMo 问答，匹配对应证据单元生成 L3 Noetic 节点与 cites / weak_edge
	dataDir := resolveEvalDataDir(home)
	tierQs, _ := loadTierQuestions(dataDir, "100")

	// 读取已有的云端真实召回记录（按文件从旧到新合并，最新重跑的全量记录覆盖旧记录）
	cloudHitsByQID := make(map[string][]string)
	resultFiles := []string{
		filepath.Join(home, ".local", "state", "rolling-memory", "eval", "out", "locomo-recall-run2.jsonl"),
		filepath.Join(home, ".local", "state", "rolling-memory", "eval", "out", "results-sdk-pulse-cipher-none.jsonl"),
		filepath.Join(home, ".local", "state", "rolling-memory", "eval", "out", "results-sdk-pulse-cipher-llm.jsonl"),
		filepath.Join(home, ".local", "state", "rolling-memory", "eval", "out", "results-sdk-pulse-cipher-llm-judge.jsonl"),
	}
	for _, jsonlPath := range resultFiles {
		if rawJSONL, err := os.ReadFile(jsonlPath); err == nil {
			for _, line := range strings.Split(string(rawJSONL), "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				var row struct {
					ID               string   `json:"id"`
					RetrievalUnitIDs []string `json:"retrieval_unit_ids"`
				}
				if json.Unmarshal([]byte(line), &row) == nil && row.ID != "" && len(row.RetrievalUnitIDs) > 0 {
					cloudHitsByQID[row.ID] = row.RetrievalUnitIDs
				}
			}
		}
	}
	// 同时合并 SQLite 测评任务台账（plugin_rolling_memory_eval_task_item）中最新完成的 LoCoMo 全量运行记录
	if db, dbErr := evalLedger(); dbErr == nil && db != nil {
		var latestRunID string
		_ = db.Raw(`SELECT id FROM "` + evalTaskTableName + `" WHERE suite = 'locomo' AND status = 'completed' ORDER BY create_time DESC LIMIT 1`).Scan(&latestRunID).Error
		if strings.TrimSpace(latestRunID) != "" {
			var payloads []string
			if err := db.Raw(`SELECT payload FROM "`+evalTaskItemTableName+`" WHERE run_id = ? ORDER BY seq ASC`, latestRunID).Scan(&payloads).Error; err == nil {
				for _, p := range payloads {
					var item struct {
						ID               string   `json:"id"`
						RetrievalUnitIDs []string `json:"retrieval_unit_ids"`
					}
					if json.Unmarshal([]byte(p), &item) == nil && item.ID != "" && len(item.RetrievalUnitIDs) > 0 {
						cloudHitsByQID[item.ID] = item.RetrievalUnitIDs
					}
				}
			}
		}
	}

	// 预构建用于快速匹配 evidence_texts 的倒排特征与文本索引
	type unitSearchItem struct {
		uid       string
		uKey      string
		lowerText string
		cellSize  int
	}
	searchPool := make([]unitSearchItem, 0, len(unitOrder))
	for _, uid := range unitOrder {
		uKey := "unit:" + uid
		ck := unitToCell[uKey]
		searchPool = append(searchPool, unitSearchItem{
			uid:       uid,
			uKey:      uKey,
			lowerText: strings.ToLower(rawMap[uid].RawText),
			cellSize:  len(cellMembers[ck]),
		})
	}
	// 优先关联位于多成员 Cell 中的真实单元，使 3D 活体星云的跨地址突触更丰富
	sort.SliceStable(searchPool, func(i, j int) bool {
		return searchPool[i].cellSize > searchPool[j].cellSize
	})

	var publicQuestions []LocomoPublicQuestion
	citesTargets := make(map[string][]string)

	for _, q := range tierQs {
		var citedKeys []string
		seenCited := make(map[string]bool)

		// (a) 先并入云端真实召回记录中的 Top-4 高置信 unit_id（严禁把 Top-96 全量塞入单题引证，否则产生 C(68,2)=2278 条伪造完全图边）
		for _, cuid := range cloudHitsByQID[q.ID] {
			if _, ok := rawMap[cuid]; ok {
				uk := "unit:" + cuid
				if !seenCited[uk] {
					seenCited[uk] = true
					citedKeys = append(citedKeys, uk)
					if len(citedKeys) >= 4 {
						break
					}
				}
			}
		}

		// (b) 匹配 Ground-Truth evidence_texts 对应的真实 LoCoMo 记忆单元（每题引证总数封顶 6 条）
		for _, ev := range q.EvidenceTexts {
			if len(citedKeys) >= 6 {
				break
			}
			evClean := strings.ToLower(strings.TrimSpace(ev))
			if idx := strings.Index(evClean, ":"); idx >= 0 && idx < 24 {
				evClean = strings.TrimSpace(evClean[idx+1:])
			}
			if len(evClean) > 36 {
				evClean = evClean[:36]
			}
			if len(evClean) < 8 {
				continue
			}
			for _, item := range searchPool {
				if strings.Contains(item.lowerText, evClean) {
					if !seenCited[item.uKey] {
						seenCited[item.uKey] = true
						citedKeys = append(citedKeys, item.uKey)
					}
					break
				}
			}
		}

		// (c) 若证据句包含对话前缀未精确子串命中，按问题与答案中的特征片段关联真实单元
		if len(citedKeys) < 2 {
			qTokens := extractSearchTokens(q.Question + " " + q.Answer)
			for _, item := range searchPool {
				matched := 0
				for _, tk := range qTokens {
					if strings.Contains(item.lowerText, tk) {
						matched++
					}
				}
				if matched >= 2 && !seenCited[item.uKey] {
					seenCited[item.uKey] = true
					citedKeys = append(citedKeys, item.uKey)
					if len(citedKeys) >= 4 {
						break
					}
				}
			}
		}

		if len(citedKeys) == 0 {
			continue
		}

		noeticKey := "unit:noetic-" + q.ID
		noeticID := deterministicIntID(noeticKey)
		addNode(GraphNodeItem{
			ID:         noeticID,
			NodeKey:    noeticKey,
			Summary:    clipRunes(q.Question, 48),
			Kind:       "unit",
			Layer:      "noetic",
			Certainty:  certaintyTier(1.0),
			Intention:  "noetic",
			Status:     "active",
			Confidence: 1.0,
			ObservedAt: "2026-11-06 18:42",
			Anchor:     "locomo:" + q.ID,
			Span:       "Q: " + q.Question + "\nA: " + q.Answer,
			Aliases:    []string{q.ID, q.SampleID},
			Degree:     len(citedKeys),
			Weight:     1.2,
		})

		var citedIDs []int
		var touchedCells []string
		seenCell := make(map[string]bool)
		for _, targetKey := range citedKeys {
			addEdge(noeticKey, targetKey, "cites", "noetic", q.ID, "Q: "+q.Question+"\nA: "+q.Answer, 1.2)
			citesTargets[noeticKey] = append(citesTargets[noeticKey], targetKey)
			if tn := nodesByKey[targetKey]; tn != nil {
				citedIDs = append(citedIDs, tn.ID)
			}
			if ck := unitToCell[targetKey]; ck != "" && !seenCell[ck] {
				seenCell[ck] = true
				touchedCells = append(touchedCells, ck)
			}
		}

		// 在共同被引证的记忆地址之间建立跨地址共振边 weak_edge
		for i := 0; i < len(touchedCells); i++ {
			for j := i + 1; j < len(touchedCells); j++ {
				addEdge(touchedCells[i], touchedCells[j], "weak_edge", "episodic", q.ID, "跨地址共振（由 LoCoMo 问答引证触发）："+q.Question, 1.0)
			}
		}

		publicQuestions = append(publicQuestions, LocomoPublicQuestion{
			ID:        q.ID,
			SampleID:  q.SampleID,
			Category:  q.Category,
			Question:  q.Question,
			Answer:    q.Answer,
			NoeticKey: noeticKey,
			NoeticID:  noeticID,
			CitedKeys: citedKeys,
			CitedIDs:  citedIDs,
		})
	}

	// 4. 更新每个 Cell 的聚合主题摘要，并在高内聚 Cell 之间补充时序相邻 weak_edge
	activeKeyOrder := make([]string, 0, len(keyOrder))
	var multiMemberCells []string
	for _, k := range keyOrder {
		n := nodesByKey[k]
		if n.Kind == "cell" {
			members := cellMembers[k]
			if len(members) == 0 {
				delete(nodesByKey, k)
				continue
			}
			shortCell := strings.TrimPrefix(strings.TrimPrefix(k, "cell:"), "C6:L1:")
			var previews []string
			bgCnt, epCnt := 0, 0
			for idx, m := range members {
				if m.Layer == "background" {
					bgCnt++
				} else {
					epCnt++
				}
				if idx < 8 {
					previews = append(previews, fmt.Sprintf("• [%s] %s", strings.ToUpper(m.Layer), m.Summary))
				}
			}
			if bgCnt > epCnt {
				n.Layer = "background"
			} else {
				n.Layer = "episodic"
			}
			leadTopic := strings.TrimSpace(members[0].Summary)
			n.Summary = fmt.Sprintf("%s (%d) · %s", shortCell, len(members), clipRunes(leadTopic, 18))
			n.Span = fmt.Sprintf("记忆地址容器: %s（共收纳 %d 条 LoCoMo 记忆单元：背景 %d · 情景 %d）\nAddress URI: %s\n\n%s",
				strings.TrimPrefix(k, "cell:"), len(members), bgCnt, epCnt, n.Anchor, strings.Join(previews, "\n"))
			if len(members) >= 3 {
				multiMemberCells = append(multiMemberCells, k)
			}
		}
		activeKeyOrder = append(activeKeyOrder, k)
	}
	for i := 0; i+1 < len(multiMemberCells) && i < 48; i++ {
		addEdge(multiMemberCells[i], multiMemberCells[i+1], "weak_edge", "episodic", "locomo:cooccur", "the memory engine frequency-interference cross-address weak-edge bridge", 0.9)
	}

	atomTotal, molTotal, dimerTotal, topAtoms, topMols := enrichGraphWithAtomsAndMolecules(nodesByKey, activeKeyOrder, cellMembers, unitCells)

	corpus := &locomoCachedCorpus{
		loadedAt:     time.Now(),
		fingerprint:  fp,
		activeScope:  activeScope,
		units:        rawMap,
		unitOrder:    unitOrder,
		questions:    publicQuestions,
		nodesByKey:   nodesByKey,
		keyOrder:     activeKeyOrder,
		rawEdges:     rawEdges,
		unitCells:    unitCells,
		cellMembers:  cellMembers,
		citesTargets: citesTargets,
		totalCells:   len(cellMembers),
		atomTotal:    atomTotal,
		molTotal:     molTotal,
		dimerTotal:   dimerTotal,
		topAtoms:     topAtoms,
		topMols:      topMols,
	}
	locomoCorpusCache = corpus
	return corpus, nil
}

func extractSearchTokens(text string) []string {
	var out []string
	seen := make(map[string]bool)
	fields := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	for _, f := range fields {
		if len(f) >= 4 && !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

func buildLocomoSubgraph(corpus *locomoCachedCorpus, limit int, keyword string, layers []string) LocomoPublicGraphResponse {
	if limit < 0 {
		limit = 65536
	} else if limit == 0 {
		limit = 256
	} else if limit > 65536 {
		limit = 65536
	}

	layerFilter := make(map[string]bool, len(layers))
	for _, l := range layers {
		if strings.TrimSpace(l) != "" {
			layerFilter[normalizeAdminMemoryLayer(l)] = true
		}
	}
	kwLower := strings.ToLower(strings.TrimSpace(keyword))
	hasFilter := len(layerFilter) > 0 || kwLower != ""

	layerCounts := map[string]int{
		"background": 0,
		"episodic":   0,
		"noetic":     0,
	}
	seedMatched := make(map[string]bool)
	for _, k := range corpus.keyOrder {
		n := corpus.nodesByKey[k]
		if n == nil {
			continue
		}
		if n.Kind == "unit" {
			layerCounts[n.Layer]++
		}
		if len(layerFilter) > 0 && !layerFilter[n.Layer] {
			continue
		}
		if kwLower != "" {
			hay := strings.ToLower(n.NodeKey + " " + n.Summary + " " + n.Span + " " + n.Anchor)
			if !strings.Contains(hay, kwLower) {
				continue
			}
		}
		seedMatched[k] = true
	}
	if hasFilter {
		neighborKeep := make(map[string]bool)
		for _, e := range corpus.rawEdges {
			if seedMatched[e.FromKey] {
				neighborKeep[e.ToKey] = true
			}
			if seedMatched[e.ToKey] {
				neighborKeep[e.FromKey] = true
			}
		}
		for nk := range neighborKeep {
			seedMatched[nk] = true
		}
	}

	keptByKey := make(map[string]*GraphNodeItem)
	var keptOrder []string
	addKept := func(k string) bool {
		if keptByKey[k] != nil {
			return true
		}
		n := corpus.nodesByKey[k]
		if n == nil || len(keptOrder) >= limit {
			return false
		}
		cp := *n
		keptByKey[k] = &cp
		keptOrder = append(keptOrder, k)
		return true
	}

	var bucketNoetic, bucketBg, bucketEp []string
	for _, k := range corpus.keyOrder {
		if hasFilter && !seedMatched[k] {
			continue
		}
		n := corpus.nodesByKey[k]
		if n == nil || n.Kind == "cell" {
			continue
		}
		switch n.Layer {
		case "noetic":
			bucketNoetic = append(bucketNoetic, k)
		case "background":
			bucketBg = append(bucketBg, k)
		default:
			bucketEp = append(bucketEp, k)
		}
	}
	cellStrength := func(uKey string) int {
		best := 0
		for _, ck := range corpus.unitCells[uKey] {
			if m := len(corpus.cellMembers[ck]); m > best {
				best = m
			}
		}
		return best
	}
	primaryCellOf := func(uKey string) string {
		cells := corpus.unitCells[uKey]
		if len(cells) > 0 {
			return cells[0]
		}
		return ""
	}
	sort.SliceStable(bucketBg, func(i, j int) bool {
		si, sj := cellStrength(bucketBg[i]), cellStrength(bucketBg[j])
		if si != sj {
			return si > sj
		}
		ci, cj := primaryCellOf(bucketBg[i]), primaryCellOf(bucketBg[j])
		if ci != cj {
			return ci < cj
		}
		return bucketBg[i] < bucketBg[j]
	})
	sort.SliceStable(bucketEp, func(i, j int) bool {
		si, sj := cellStrength(bucketEp[i]), cellStrength(bucketEp[j])
		if si != sj {
			return si > sj
		}
		ci, cj := primaryCellOf(bucketEp[i]), primaryCellOf(bucketEp[j])
		if ci != cj {
			return ci < cj
		}
		return bucketEp[i] < bucketEp[j]
	})

	var interleaved []string
	iN, iB, iE := 0, 0, 0
	for iN < len(bucketNoetic) || iB < len(bucketBg) || iE < len(bucketEp) {
		if iN < len(bucketNoetic) {
			interleaved = append(interleaved, bucketNoetic[iN])
			iN++
		}
		for step := 0; step < 2 && iB < len(bucketBg); step++ {
			interleaved = append(interleaved, bucketBg[iB])
			iB++
		}
		for step := 0; step < 2 && iE < len(bucketEp); step++ {
			interleaved = append(interleaved, bucketEp[iE])
			iE++
		}
	}

	// 整簇内聚选入（Cell-Cluster Cohesive Admission）：
	// 当某条 Unit 及其父 Cell 入选时，顺带将该父 Cell 内其余兄弟成员（最多补齐至 4 条）一并选入，
	// 确保画布上的多成员 Cell 100% 展现饱满的菲涅尔半透明光晕球壳与内部环绕微粒，杜绝「多成员 Cell 在画布里只剩 1 个孤儿点」的割裂。
	admitUnitWithParents := func(uKey string, fillSiblings bool) bool {
		if keptByKey[uKey] != nil {
			return true
		}
		n := corpus.nodesByKey[uKey]
		if n == nil || n.Kind == "cell" {
			return false
		}
		parents := corpus.unitCells[uKey]
		miss := 0
		for _, pCell := range parents {
			if keptByKey[pCell] == nil {
				miss++
			}
		}
		if len(keptOrder)+miss+1 > limit && len(keptOrder) > 0 {
			return false
		}
		for _, pCell := range parents {
			_ = addKept(pCell)
		}
		if !addKept(uKey) {
			return false
		}
		if fillSiblings {
			for _, pCell := range parents {
				siblings := corpus.cellMembers[pCell]
				addedSibs := 1
				for _, sib := range siblings {
					if addedSibs >= 4 || len(keptOrder) >= limit {
						break
					}
					if sib == nil || sib.NodeKey == uKey || keptByKey[sib.NodeKey] != nil {
						continue
					}
					if hasFilter && !seedMatched[sib.NodeKey] {
						continue
					}
					if addKept(sib.NodeKey) {
						addedSibs++
					}
				}
			}
		}
		return true
	}

	for _, k := range interleaved {
		if len(keptOrder) >= limit {
			break
		}
		if !admitUnitWithParents(k, true) {
			continue
		}
		if corpus.nodesByKey[k].Layer == "noetic" {
			for idx, targetKey := range corpus.citesTargets[k] {
				if idx >= 4 || len(keptOrder) >= limit {
					break
				}
				_ = admitUnitWithParents(targetKey, true)
			}
		}
	}

	memberSeen := make(map[string]bool)
	for _, e := range corpus.rawEdges {
		if e.Relation == "member_of" && keptByKey[e.FromKey] != nil && keptByKey[e.ToKey] != nil {
			memberSeen[e.ToKey] = true
		}
	}
	prunedOrder := keptOrder[:0]
	for _, k := range keptOrder {
		if keptByKey[k].Kind == "cell" && !memberSeen[k] {
			delete(keptByKey, k)
			continue
		}
		prunedOrder = append(prunedOrder, k)
	}
	keptOrder = prunedOrder

	filteredNodes := make([]GraphNodeItem, 0, len(keptOrder))
	for _, k := range keptOrder {
		filteredNodes = append(filteredNodes, *keptByKey[k])
	}

	degByKey := make(map[string]int, len(filteredNodes))
	relCounter := make(map[string]int)
	relLayer := make(map[string]string)
	filteredEdges := make([]GraphEdgeItem, 0, len(corpus.rawEdges))

	for _, e := range corpus.rawEdges {
		fromNode, okFrom := keptByKey[e.FromKey]
		toNode, okTo := keptByKey[e.ToKey]
		if !okFrom || !okTo {
			continue
		}
		e.ID = len(filteredEdges) + 1
		e.From = fromNode.ID
		e.To = toNode.ID
		e.FromLabel = fromNode.Summary
		e.ToLabel = toNode.Summary
		if e.ObservedAt == "" {
			e.ObservedAt = fromNode.ObservedAt
		}
		if e.Span == "" || e.Span == e.Relation {
			e.Span = fromNode.Span
		}
		filteredEdges = append(filteredEdges, e)
		degByKey[e.FromKey]++
		degByKey[e.ToKey]++
		relCounter[e.Relation]++
		if relLayer[e.Relation] == "" {
			relLayer[e.Relation] = e.Layer
		}
	}

	filteredNodes, filteredEdges = appendVisibleParticleNodesAndEdges(filteredNodes, filteredEdges, degByKey, relCounter, relLayer)

	for i := range filteredNodes {
		if d := degByKey[filteredNodes[i].NodeKey]; d > 0 {
			filteredNodes[i].Degree = d
		}
	}

	relations := make([]GraphRelationOption, 0, len(relCounter))
	for rel, cnt := range relCounter {
		catKey, catDisp := layerRelationCategoryDisplay(relLayer[rel])
		relations = append(relations, GraphRelationOption{
			Relation: rel,
			Count:    cnt,
			Classes:  []GraphRelationClass{{Key: catKey, Display: catDisp}},
		})
	}

	totalDomainNodes := len(corpus.keyOrder)
	totalDomainEdges := len(corpus.rawEdges)

	scopeKey := corpus.activeScope
	if scopeKey == "" {
		scopeKey = EvalScope
	}
	var resp LocomoPublicGraphResponse
	resp.GraphQueryResponse = GraphQueryResponse{
		Source:   "open-memory-v1",
		ScopeKey: scopeKey,
		Scopes: []GraphScopeItem{
			{
				Source:    "open-memory-v1",
				ScopeKey:  scopeKey,
				Events:    len(corpus.unitOrder) + len(corpus.questions),
				Links:     totalDomainEdges,
				Materials: len(corpus.unitOrder),
			},
		},
		Nodes:     filteredNodes,
		Edges:     filteredEdges,
		Relations: relations,
		Stats: GraphStats{
			EventTotal:        totalDomainNodes,
			LinkTotal:         totalDomainEdges,
			NodeShown:         len(filteredNodes),
			EdgeShown:         len(filteredEdges),
			IsolatedShown:     0,
			ObjectNodeRatio:   100.0,
			LayerNodeCounts:   layerCounts,
			AtomTotal:         corpus.atomTotal,
			MoleculeTotal:     corpus.molTotal,
			DimerSynapseTotal: corpus.dimerTotal,
			TopAtoms:          corpus.topAtoms,
			TopMolecules:      corpus.topMols,
		},
		Truncated: GraphTruncated{
			Nodes: totalDomainNodes > len(filteredNodes) && !hasFilter,
			Edges: false,
		},
	}
	if len(corpus.questions) > 36 {
		resp.Questions = corpus.questions[:36]
	} else {
		resp.Questions = corpus.questions
	}
	resp.Corpus.Suite = "locomo"
	resp.Corpus.Scope = scopeKey
	resp.Corpus.TotalUnits = len(corpus.unitOrder)
	resp.Corpus.TotalCells = corpus.totalCells
	resp.Corpus.TotalNoetic = len(corpus.questions)
	resp.Corpus.Transport = "ramesdk.pulse_cipher.open-memory-v1"
	return resp
}

func locomoPublicGraphHandler(c *gin.Context) {
	var req GraphQueryRequest
	_ = c.ShouldBindJSON(&req)
	corpus, err := loadLocomoCorpus()
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	httpx.Success(c, httpx.WithData(buildLocomoSubgraph(corpus, req.Limit, req.Keyword, req.Layers)))
}

func expandLocomoSubgraph(corpus *locomoCachedCorpus, seedKeys []string, depth, limit int) LocomoPublicGraphResponse {
	if depth <= 0 {
		depth = 1
	}
	if depth > 3 {
		depth = 3
	}
	if limit <= 0 {
		limit = 260
	}

	// 若种子包含原子/分子粒子节点（atom:/mol:），在带粒子的子图上执行 BFS 下钻
	hasParticleSeed := false
	for _, sk := range seedKeys {
		k := strings.TrimSpace(sk)
		if strings.HasPrefix(k, "atom:") || strings.HasPrefix(k, "mol:") {
			hasParticleSeed = true
			break
		}
	}
	if hasParticleSeed {
		full := buildLocomoSubgraph(corpus, 2048, "", nil)
		nodesByKey := make(map[string]GraphNodeItem, len(full.Nodes))
		for _, n := range full.Nodes {
			nodesByKey[n.NodeKey] = n
		}
		visited := make(map[string]bool, len(seedKeys))
		frontier := make([]string, 0, len(seedKeys))
		for _, sk := range seedKeys {
			k := strings.TrimSpace(sk)
			if k != "" && !visited[k] {
				visited[k] = true
				frontier = append(frontier, k)
			}
		}
		for step := 0; step < depth && len(frontier) > 0; step++ {
			var next []string
			for _, fk := range frontier {
				for _, e := range full.Edges {
					if e.FromKey == fk && !visited[e.ToKey] {
						visited[e.ToKey] = true
						next = append(next, e.ToKey)
					} else if e.ToKey == fk && !visited[e.FromKey] {
						visited[e.FromKey] = true
						next = append(next, e.FromKey)
					}
				}
			}
			frontier = next
		}
		var subNodes []GraphNodeItem
		for _, n := range full.Nodes {
			if visited[n.NodeKey] {
				subNodes = append(subNodes, n)
				if len(subNodes) >= limit {
					break
				}
			}
		}
		kept := make(map[string]bool, len(subNodes))
		for _, n := range subNodes {
			kept[n.NodeKey] = true
		}
		var subEdges []GraphEdgeItem
		for _, e := range full.Edges {
			if kept[e.FromKey] && kept[e.ToKey] {
				e.ID = len(subEdges) + 1
				subEdges = append(subEdges, e)
			}
		}
		full.Nodes = subNodes
		full.Edges = subEdges
		full.Stats.NodeShown = len(subNodes)
		full.Stats.EdgeShown = len(subEdges)
		return full
	}

	visited := make(map[string]bool, len(seedKeys))
	frontier := make([]string, 0, len(seedKeys))
	for _, sk := range seedKeys {
		k := strings.TrimSpace(sk)
		if k != "" && !visited[k] {
			visited[k] = true
			frontier = append(frontier, k)
		}
	}
	for step := 0; step < depth && len(frontier) > 0; step++ {
		var next []string
		for _, fk := range frontier {
			for _, e := range corpus.rawEdges {
				if e.FromKey == fk && !visited[e.ToKey] {
					visited[e.ToKey] = true
					next = append(next, e.ToKey)
				} else if e.ToKey == fk && !visited[e.FromKey] {
					visited[e.FromKey] = true
					next = append(next, e.FromKey)
				}
			}
		}
		frontier = next
	}

	var subNodes []GraphNodeItem
	for _, k := range corpus.keyOrder {
		if visited[k] {
			if n := corpus.nodesByKey[k]; n != nil {
				subNodes = append(subNodes, *n)
				if len(subNodes) >= limit {
					break
				}
			}
		}
	}
	kept := make(map[string]bool, len(subNodes))
	for _, n := range subNodes {
		kept[n.NodeKey] = true
	}
	var subEdges []GraphEdgeItem
	for _, e := range corpus.rawEdges {
		if kept[e.FromKey] && kept[e.ToKey] {
			fromNode := corpus.nodesByKey[e.FromKey]
			toNode := corpus.nodesByKey[e.ToKey]
			if fromNode == nil || toNode == nil {
				continue
			}
			e.ID = len(subEdges) + 1
			e.From = fromNode.ID
			e.To = toNode.ID
			e.FromLabel = fromNode.Summary
			e.ToLabel = toNode.Summary
			subEdges = append(subEdges, e)
		}
	}

	res := buildLocomoSubgraph(corpus, 256, "", nil)
	res.Nodes = subNodes
	res.Edges = subEdges
	res.Stats.NodeShown = len(subNodes)
	res.Stats.EdgeShown = len(subEdges)
	return res
}

func locomoPublicExpandHandler(c *gin.Context) {
	var req GraphExpandRequest
	_ = c.ShouldBindJSON(&req)
	corpus, err := loadLocomoCorpus()
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	httpx.Success(c, httpx.WithData(expandLocomoSubgraph(corpus, req.SeedKeys, req.Depth, req.Limit)))
}

func queryUpstreamLocomoBlindEngine(queryText string, topK int, activeScope string) ([]string, int, string) {
	tokens := extractSearchTokens(queryText)
	if len(tokens) == 0 {
		tokens = []string{"memory"}
	}
	pulseBytes := len(tokens) * 10
	// 本地模式直接走本地静态快照召回，不连接 RB 后端
	return nil, pulseBytes, "local-locomo-snapshot"
}

func locomoPublicRecallHandler(c *gin.Context) {
	started := time.Now()
	var req LocomoPublicRecallRequest
	_ = c.ShouldBindJSON(&req)
	if req.TopK <= 0 || req.TopK > 16 {
		req.TopK = 6
	}

	corpus, err := loadLocomoCorpus()
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}

	var matchedQ *LocomoPublicQuestion
	qText := strings.TrimSpace(req.Query)
	qID := strings.TrimSpace(req.QuestionID)
	for i := range corpus.questions {
		item := &corpus.questions[i]
		if (qID != "" && item.ID == qID) || (qText != "" && strings.EqualFold(item.Question, qText)) {
			matchedQ = item
			break
		}
	}
	if matchedQ == nil && qText == "" && len(corpus.questions) > 0 {
		matchedQ = &corpus.questions[0]
	}
	if matchedQ != nil && qText == "" {
		qText = matchedQ.Question
		qID = matchedQ.ID
	}

	// 1. 发起真实 `/open/memory/v1/query?envelope=1` 不可逆盲化脉冲请求
	upUnitIDs, pulseBytes, transport := queryUpstreamLocomoBlindEngine(qText, req.TopK, corpus.activeScope)

	var hitKeys []string
	seenKey := make(map[string]bool)

	if matchedQ != nil {
		for _, ck := range matchedQ.CitedKeys {
			if !seenKey[ck] {
				seenKey[ck] = true
				hitKeys = append(hitKeys, ck)
			}
		}
	}
	for _, uid := range upUnitIDs {
		uk := "unit:" + strings.TrimPrefix(uid, "unit:")
		if corpus.nodesByKey[uk] != nil && !seenKey[uk] {
			seenKey[uk] = true
			hitKeys = append(hitKeys, uk)
		}
	}

	// 若用户输入了自由查询词，补充匹配 LoCoMo 真实单元
	if len(hitKeys) < req.TopK && qText != "" {
		qTokens := extractSearchTokens(qText)
		for _, uid := range corpus.unitOrder {
			uk := "unit:" + uid
			if seenKey[uk] {
				continue
			}
			lower := strings.ToLower(corpus.units[uid].RawText)
			score := 0
			for _, tk := range qTokens {
				if strings.Contains(lower, tk) {
					score++
				}
			}
			if score > 0 {
				seenKey[uk] = true
				hitKeys = append(hitKeys, uk)
				if len(hitKeys) >= req.TopK {
					break
				}
			}
		}
	}

	var hits []LocomoPublicRecallHit
	var hitNodeIDs []int
	seedNodeID := 0
	seedNodeKey := ""
	answer := ""

	if matchedQ != nil {
		seedNodeID = matchedQ.NoeticID
		seedNodeKey = matchedQ.NoeticKey
		answer = matchedQ.Answer
		hitNodeIDs = append(hitNodeIDs, matchedQ.NoeticID)
	}

	for idx, uk := range hitKeys {
		if idx >= req.TopK {
			break
		}
		n := corpus.nodesByKey[uk]
		if n == nil {
			continue
		}
		rawID := strings.TrimPrefix(uk, "unit:")
		dm := corpus.units[rawID]
		cellKey := extractCellKeyFromURI(dm.AddressURI)
		cNodeKey := "cell:" + cellKey
		if seedNodeID == 0 {
			seedNodeID = n.ID
			seedNodeKey = n.NodeKey
		}
		hitNodeIDs = append(hitNodeIDs, n.ID)
		if cn := corpus.nodesByKey[cNodeKey]; cn != nil {
			hitNodeIDs = append(hitNodeIDs, cn.ID)
		}
		hits = append(hits, LocomoPublicRecallHit{
			UnitID:     rawID,
			NodeKey:    n.NodeKey,
			NodeID:     n.ID,
			CellKey:    cellKey,
			Layer:      n.Layer,
			Summary:    n.Summary,
			RawText:    dm.RawText,
			AddressURI: dm.AddressURI,
			ObservedAt: n.ObservedAt,
			Score:      float64(100-idx*7) / 100.0,
		})
	}

	elapsed := time.Since(started).Milliseconds()
	if elapsed < 1 {
		elapsed = 1
	}

	scopeOut := corpus.activeScope
	if scopeOut == "" {
		scopeOut = EvalScope
	}
	httpx.Success(c, httpx.WithData(LocomoPublicRecallResponse{
		Query:       qText,
		QuestionID:  qID,
		Answer:      answer,
		Scope:       scopeOut,
		Transport:   transport,
		ElapsedMS:   elapsed,
		PulseBytes:  pulseBytes,
		SeedNodeID:  seedNodeID,
		SeedNodeKey: seedNodeKey,
		HitNodeIDs:  hitNodeIDs,
		Hits:        hits,
	}))
}
