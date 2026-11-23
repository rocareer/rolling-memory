// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package localvault

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	core "github.com/rocareer/rolling-memory/internal/core"
)

// maxCSRDeltaDocs 是允许在已有 .zcsr 零拷贝基线引擎之上叠加内存增量微引擎（deltaEngine）的
// 最大新增单元数：日常守护进程运行期间，后台 autowatch 新增的对话在达到该上限前全部走
// 毫秒级、兆字节级的 deltaEngine，避免写 1 条对话就作废 .zcsr 重跑数分钟全量构建。
const maxCSRDeltaDocs = 2048

// corpusDeltaCopyLimit bounds the copy-on-write docs view used by the change-log
// delta path. Above it the reader falls back to the full canonical scan; the
// per-generation docs sidecar is the next step for very large corpora.
const corpusDeltaCopyLimit = 50_000

// 本地检索引擎的 .zcsr 磁盘簇缓存（零拷贝冷启动）。
//
// 背景与取舍：核心引擎的一次全量构建（core.BuildIndex + MaterializeClusterState）在
// 数万单元规模上以分钟计，而核心自带 .zcsr 只读磁盘段（WriteDiskClusterCSR /
// OpenDiskClusterEngine，mmap 零拷贝、毫秒级冷启动）。本适配器只做「语料新鲜度指纹 -> 磁盘簇」
// 的缓存槽与代际校验，不重写任何核心算子：
//   - 指纹一致：直接 OpenDiskClusterEngine，跳过引擎全量构建与簇物化；
//   - 纯追加漂移（≤ maxCSRDeltaDocs 个新单元）：基线 mmap + 小增量内存引擎双层检索；
//   - 其余情况：全量构建一次，随后原子写回 .zcsr 与指纹档（core 侧 tmp->fsync->rename）。
//
// 新鲜度指纹（`v4|` 行）覆盖引擎索引的全部输入维度：文档表四元聚合（行数/行号和/最大行号/
// 文本总长）+ docs_rev + 背景块四元聚合 + bg_rev + episodic_revision + episodic 索引行数 +
// 编解码键十六进制。触发器修订号是纯追加代数（Δrev == Δcnt）的唯一依据：聚合指纹对
// 「等长 UPDATE」盲视，只有修订号能证明表自上次构建以来只发生过 INSERT。
const (
	vaultCSRSuffix      = ".zcsr"
	vaultCSRGenSuffix   = ".zcsr.gen"
	vaultCSRUnitsSuffix = ".zcsr.units"
)

// vaultCSRPaths 返回磁盘簇与指纹档的固定路径（与主库同目录）。
func vaultCSRPaths(dbPath string) (string, string) {
	return dbPath + vaultCSRSuffix, dbPath + vaultCSRGenSuffix
}

// vaultCSRUnitPaths 返回单元指纹旁车路径（与指纹档同目录）。
func vaultCSRUnitPaths(dbPath string) string {
	return dbPath + vaultCSRUnitsSuffix
}

// mainDBPath 返回主数据库的文件路径（内存库、便携覆盖或查询失败时为空串）。
// 记忆化一次：召回入口每次都会问一次，不让 PRAGMA 查询落到热路径上。
func (v *Vault) mainDBPath() string {
	if v == nil || v.db == nil {
		return ""
	}
	v.dbPathOnce.Do(func() {
		var file string
		if err := v.db.QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&file); err != nil {
			return
		}
		file = strings.TrimSpace(file)
		if file == "" {
			return
		}
		if abs, err := filepath.Abs(file); err == nil {
			file = abs
		}
		v.dbPath = file
	})
	return v.dbPath
}

// corpusFreshnessLine 生成 v4 新鲜度指纹行（codecKeyHex 为编解码键的十六进制编码，避免
// 键内任意字节污染行结构）。行内字段顺序固定，比较即逐位相等比较。
func corpusFreshnessLine(
	docCnt, docRowSum, docMaxRowID, docTextLen, docsRev int64,
	bgCnt, bgMaxRowID, bgOccSum, bgSlotsLen, bgRev int64,
	episodicRevision, epiCnt int64,
	codecKeyHex string,
) string {
	return fmt.Sprintf("v4|%d|%d|%d|%d|%d|%d|%d|%d|%d|%d|%d|%d|%s",
		docCnt, docRowSum, docMaxRowID, docTextLen, docsRev,
		bgCnt, bgMaxRowID, bgOccSum, bgSlotsLen, bgRev,
		episodicRevision, epiCnt, codecKeyHex)
}

// freshnessLine 以快照自身记录的语料维度重建 v4 指纹行（落盘与复用校验用）。
func (s *vaultCorpusSnapshot) freshnessLine() string {
	if s == nil {
		return ""
	}
	if s.freshnessSignature != "" {
		return s.freshnessSignature
	}
	return corpusFreshnessLine(
		s.docCnt, s.docRowSum, s.maxRowID, s.docTextLen, s.docsRev,
		s.bgCnt, s.bgMaxRowID, s.bgOccSum, s.bgSlotsLen, s.bgRev,
		s.episodicRevision, s.epiCnt, hex.EncodeToString([]byte(s.episodicCodecKey)),
	)
}

// csrFreshness 是 v4 指纹行的解析结果。
type csrFreshness struct {
	docCnt           int64
	docRowSum        int64
	docMaxRowID      int64
	docTextLen       int64
	docsRev          int64
	bgCnt            int64
	bgMaxRowID       int64
	bgOccSum         int64
	bgSlotsLen       int64
	bgRev            int64
	episodicRevision int64
	epiCnt           int64
	codecKeyHex      string
}

// parseCSRFreshnessLine 解析 v4 指纹行；历史 v3 签名档与损坏内容一律判否（回退全量构建）。
func parseCSRFreshnessLine(line string) (csrFreshness, bool) {
	parts := strings.Split(strings.TrimSpace(line), "|")
	if len(parts) != 14 || parts[0] != "v4" {
		return csrFreshness{}, false
	}
	nums := make([]int64, 12)
	for i := range nums {
		n, err := strconv.ParseInt(parts[i+1], 10, 64)
		if err != nil {
			return csrFreshness{}, false
		}
		nums[i] = n
	}
	return csrFreshness{
		docCnt: nums[0], docRowSum: nums[1], docMaxRowID: nums[2], docTextLen: nums[3], docsRev: nums[4],
		bgCnt: nums[5], bgMaxRowID: nums[6], bgOccSum: nums[7], bgSlotsLen: nums[8], bgRev: nums[9],
		episodicRevision: nums[10], epiCnt: nums[11],
		codecKeyHex: parts[13],
	}, true
}

// acquireCachedCSREngine 打开或复用与主库同目录的 .zcsr 零拷贝基线引擎（按文件 mtime+size
// 缓存句柄，避免在增量写入后重复 mmap 同一个数 GB 的 .zcsr 文件）。
func (v *Vault) acquireCachedCSREngine(dbPath string) (*core.Engine, string, func(), bool) {
	if v == nil || strings.TrimSpace(dbPath) == "" {
		return nil, "", nil, false
	}
	csrPath, genPath, _, _, _, resolved := resolvePublishedSnapshotPaths(dbPath)
	if !resolved {
		return nil, "", nil, false
	}
	rawGen, err := os.ReadFile(genPath)
	if err != nil {
		return nil, "", nil, false
	}
	genLine := strings.TrimSpace(string(rawGen))
	if _, ok := parseCSRFreshnessLine(genLine); !ok && signatureCodecKey(genLine) == "" {
		return nil, "", nil, false
	}
	st, err := os.Stat(csrPath)
	if err != nil || st.Size() <= 0 {
		return nil, "", nil, false
	}
	mtimeNS, fsize := st.ModTime().UnixNano(), st.Size()
	if v.slotCacheMu != nil && v.snapRef != nil {
		v.slotCacheMu.Lock()
		cachedEng := v.snapRef.csrEngine
		sameFile := cachedEng != nil && cachedEng.Index != nil && cachedEng.Index.N > 0 &&
			v.snapRef.csrFileMTimeNS == mtimeNS && v.snapRef.csrFileSize == fsize &&
			v.snapRef.csrFilePath == csrPath
		if sameFile {
			// Lease the cached handle before releasing the lock: a concurrent
			// cache drop must not unmap it between the hit and the caller's use.
			v.retainSnapshotEngineLocked(cachedEng)
			v.slotCacheMu.Unlock()
			return cachedEng, genLine, func() { v.releaseSnapshotEngine(cachedEng) }, true
		}
		v.slotCacheMu.Unlock()
	}
	eng, err := core.OpenDiskClusterEngine(csrPath)
	if err != nil || eng == nil || eng.Index == nil || eng.Index.N <= 0 {
		if eng != nil {
			_ = eng.Close()
		}
		return nil, "", nil, false
	}
	hold := func() { v.releaseSnapshotEngine(eng) }
	if v.slotCacheMu != nil && v.snapRef != nil {
		v.slotCacheMu.Lock()
		if v.snapRef.runtimeClosed {
			v.retainSnapshotEngineLocked(eng)
			v.slotCacheMu.Unlock()
			return eng, genLine, hold, true
		}
		previous := v.snapRef.csrEngine
		if previous != eng {
			v.retainSnapshotEngineLocked(eng)
		}
		v.retainSnapshotEngineLocked(eng)
		v.snapRef.csrEngine = eng
		v.snapRef.csrFileMTimeNS = mtimeNS
		v.snapRef.csrFileSize = fsize
		v.snapRef.csrFilePath = csrPath
		v.slotCacheMu.Unlock()
		if previous != nil && previous != eng {
			v.releaseSnapshotEngine(previous)
		}
		return eng, genLine, hold, true
	}
	v.retainSnapshotEngine(eng)
	return eng, genLine, hold, true
}

// snapshotQueryWorker is one per-caller engine copy. Its query scratch carries
// the day-cosine memo across the queries served by this worker. base pins the
// exact engine the copy came from, so a derived engine rebuild (for example a
// new noetic co-citation set) cannot be served by a stale copy.
type snapshotQueryWorker struct {
	eng  core.Engine
	base *core.Engine
}

func (s *vaultCorpusSnapshot) acquireQueryWorker(eng *core.Engine) *snapshotQueryWorker {
	if raw := s.coreWorkerPool.Get(); raw != nil {
		w := raw.(*snapshotQueryWorker)
		if w.base == eng && w.eng.Index == eng.Index {
			return w
		}
	}
	w := &snapshotQueryWorker{eng: *eng, base: eng}
	w.eng.ResetScratch()
	return w
}

func (s *vaultCorpusSnapshot) releaseQueryWorker(w *snapshotQueryWorker, eng *core.Engine) {
	if w == nil || eng == nil || w.base != eng || w.eng.Index != eng.Index {
		return
	}
	s.coreWorkerPool.Put(w)
}

// searchSnapshotCoreEngines 委托给 core.Engine 原生检索（含基线 .zcsr 与 core.Engine 内建的
// Delta Overlay 自动融合与遮蔽）。
func (s *vaultCorpusSnapshot) searchSnapshotCoreEngines(v *Vault, queryText string, queryPkt *BlindPulsePacket, topK int) []core.Hit {
	if s == nil || len(s.docs) == 0 {
		return nil
	}
	eng := s.ensureSnapshotCoreEngine(v)
	if eng == nil {
		return nil
	}
	// Fold the active recollections' declared citation records into the
	// co-citation CSR. The derived engine is immutable and cached by the noetic
	// fingerprint, so the base engine and concurrent readers are untouched.
	eng = s.coreEngineWithNoeticCitations(v, eng)
	// The first search materialises the lazily built cluster state, so it runs
	// under the lock; two cold callers must not build that state twice. After it
	// returns, the state is immutable for this snapshot and callers no longer
	// contend.
	s.coreMu.Lock()
	if !s.coreWarm {
		hits := searchCoreEngine(eng, v, queryText, queryPkt, topK)
		s.coreWarm = true
		s.coreMu.Unlock()
		return hits
	}
	s.coreMu.Unlock()

	// A delta overlay carries a child engine whose query scratch is shared, so
	// that path keeps serialising. The product read path opens the immutable disk
	// cluster and never carries a delta.
	if eng.HasDelta() {
		s.coreMu.Lock()
		defer s.coreMu.Unlock()
		return searchCoreEngine(eng, v, queryText, queryPkt, topK)
	}

	// One Engine instance owns one query scratch. A per-caller copy gives that
	// scratch its own day-cosine memo; pooling the copies keeps the memo warm
	// across the queries served by the same caller instead of rebuilding it from
	// zero every query.
	worker := s.acquireQueryWorker(eng)
	defer s.releaseQueryWorker(worker, eng)
	return searchCoreEngine(&worker.eng, v, queryText, queryPkt, topK)
}

// searchCoreEngine runs one search on the given engine instance. It owns no
// shared state beyond the engine it is handed, so the caller decides whether
// that engine is the shared instance or a per-query copy.
func searchCoreEngine(eng *core.Engine, v *Vault, queryText string, queryPkt *BlindPulsePacket, topK int) []core.Hit {
	if queryPkt != nil {
		qTerms, qPos := v.extractQueryFromBlindPacket(eng.Index, *queryPkt)
		if len(qTerms) == 0 {
			return nil
		}
		return eng.SearchWithPositions(qTerms, qPos, topK)
	}
	if v != nil && v.segOpts.Segmenter != nil {
		qPkt, _ := v.PackBlindPacket("Q-DEFAULT", queryText, 0)
		qTerms, qPos := v.extractQueryFromBlindPacket(eng.Index, qPkt)
		if len(qTerms) == 0 {
			return nil
		}
		return eng.SearchWithPositions(qTerms, qPos, topK)
	}
	te := v.newVaultTermExtractor()
	return eng.Query(te, queryText, topK)
}

// openSnapshotCoreEngineFromCSR 尝试按指纹行复用磁盘簇；任何缺失/不一致/损坏都返回 false，
// 由调用方回退全量构建（fail-open 到慢路径，绝不 fail-closed 掉召回）。
func openSnapshotCoreEngineFromCSR(dbPath, freshnessLine string) (*core.Engine, bool) {
	if strings.TrimSpace(dbPath) == "" || strings.TrimSpace(freshnessLine) == "" {
		return nil, false
	}
	csrPath, genPath, _, _, _, resolved := resolvePublishedSnapshotPaths(dbPath)
	if !resolved {
		return nil, false
	}
	raw, err := os.ReadFile(genPath)
	if err != nil || strings.TrimSpace(string(raw)) != strings.TrimSpace(freshnessLine) {
		return nil, false
	}
	eng, err := core.OpenDiskClusterEngine(csrPath)
	if err != nil || eng == nil || eng.Index == nil || eng.Index.N <= 0 {
		return nil, false
	}
	return eng, true
}

// persistSnapshotCoreCSR 原子写回磁盘簇、单元指纹旁车与指纹档（core 侧 tmp->fsync->rename 已原子），
// 返回磁盘簇路径与是否落盘成功（失败静默降级：下一次仍走全量构建，正确性不受影响）。
// 旁车写失败不阻断落盘（退化为「仅指纹逐位一致可复用」，保守但正确）。
//
// 同步调用：写回发生在引擎构建（分钟级）之后、发布之前；CLI 等短命进程也会把缓存落盘，
// 而长驻守护进程的后续构建同样受益。
func persistSnapshotCoreCSR(dbPath, freshnessLine string, eng *core.Engine, v *Vault, codecKey []byte) (string, bool) {
	if strings.TrimSpace(dbPath) == "" || strings.TrimSpace(freshnessLine) == "" || eng == nil {
		return "", false
	}
	// Serialization must see the complete index: the corpus digest is computed
	// from term bits/flags/DF as well as the unit metadata, and
	// ReleaseTransientUnitMaps clears those maps. AttachDiskClusterCSR(..., true)
	// releases them after the segment is written and mounted.
	csrPath, genPath := vaultCSRPaths(dbPath)
	if err := core.WriteDiskClusterCSR(csrPath, eng); err != nil {
		return "", false
	}
	if v != nil {
		_ = v.persistCanonicalUnitRevisions(dbPath)
	}
	if err := os.WriteFile(genPath, []byte(strings.TrimSpace(freshnessLine)+"\n"), 0o600); err != nil {
		return "", false
	}
	return csrPath, true
}
