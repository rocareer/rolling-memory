// plugin: kb v1.0.0
// 查询向量缓存（「查询向量缓存：按 (agent, query) 缓存并可原子落盘——先例：查询缓存实现；
// 请求路径零内联嵌入」的落点）。
//
// 口径（照既有查询缓存实现逐条对齐）：
//   - 键 = **agent + query**（NUL 分隔防拼接歧义）——换嵌入 agent/模型不得复用旧向量；
//     命名注：参数名沿用 `agent` 旧称，**调用侧实传的是嵌入身份**（`identity`，见 vector.go:65/:86
//     与「键 = (身份, query)」口径）——键语义与本保护均不变，仅别名澄清；
//   - 内存 map + 惰性文件装载（首次 resolve 时载入）；文件损坏 = 空启（stderr 提示一次，不阻断）；
//   - 落盘失败**不阻断检索**（内存仍命中，调用方告警一次）；
//   - 文件路径：viper `kb.search.query_cache_file`（显式）> 缺省 os.TempDir()/kb-qvec.json
//     （`kb.search.query_cache_persist=false` 关落盘 = 纯内存）。
//
// 多实例安全（同型缺陷先例：多实例下的缓存分叉）：
// 写路径 = 「跨进程排他锁（flock）→ 锁内重读磁盘 → 并集合并 → 唯一临时文件 + 原子改名」，
// 多实例并写不互相覆盖、文件不损坏。旧写法 = 固定 `file+".tmp"` 整文件快照覆盖：两进程同时写
// 同一临时名可交错成坏档，且后写者整文件快照把先写者的键抹掉（缺省路径是**共享载体**，见下）。
// 缺省文件名与位置**不变**（仍是 kb-qvec.json：跨进程/跨重启同一份向量的本意所在）；
// 显式文件键语义不变（仍优先于缺省路径，同样受本保护——同一显式路径的多实例亦为合并写）。
//
// 取舍（对另一候选「缺省路径按进程实例隔离：pid/hostname 后缀」）——同判：
//   - pid 后缀 ⇒ 缺省文件每次重启换名，「跨重启同查询同向量」的本意落空，且 TempDir 逐进程堆渣；
//   - hostname 后缀 ⇒ 不能隔离同机多进程（serve 进程与 CLI 同机共享缺省路径才是真实冲突面）；
//   - 故取 flock 合并写：保住缺省文件的跨进程共享语义，同时消除覆盖与并发临时名交错。
//
// 退化（如实）：flock 不可用（非 unix 构建，或锁文件不可创建/无锁支持的文件系统）⇒ 降级为无跨进程锁
// （stderr 提示一次），仍是唯一临时名 + 原子改名（单进程内正确、文件不损），但多进程并写仍可能丢键；
// 该场景请用显式独立文件（每实例一个路径）做硬隔离。
//
// 实现出处：锁与原子落盘照抄既有已落地的最小形态（
// / `querycache_lock_other.go` / `querycache.go` 的 writeFileAtomic）——**不跨包引用引擎内部**
// （其它模块的非导出符号不可用，且方向应为各自持最小实现）。
//
// 说明：本段配置键已收口到插件配置（config/kb.yaml + kbcfg）
// （缺省档 = 落盘开关随插件配置；键已在 config/kb.yaml 收口）。
package search

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ai-go-hub/ai-go-admin/plugins/kb/kbcfg"
)

// queryVecCacheVersion 落盘文件格式版本（不兼容即空启）
const queryVecCacheVersion = 1

// queryCacheDefaultFileName 缺省缓存文件名（各进程解析到同一路径 = 共享载体；并发安全由写路径 flock 合并承担）
const queryCacheDefaultFileName = "kb-qvec.json"

// queryVecCache 查询向量缓存（进程内 / 文件持久化；零值不可用，经 newQueryVecCache 构造）
type queryVecCache struct {
	mu      sync.Mutex
	mem     map[string][]float32
	file    string
	loaded  bool
	warned  bool // 文件损坏/版本不符提示只出一次（避免每次 Put 刷屏）
	warnedL bool // 锁降级提示只出一次
}

// newQueryVecCache 新建内存缓存（文件路径惰性解析）
func newQueryVecCache() *queryVecCache {
	return &queryVecCache{mem: map[string][]float32{}}
}

// cacheKey 缓存键：agent + query（NUL 分隔，避免拼接歧义）
func cacheKey(agent, q string) string {
	return strings.TrimSpace(agent) + "\x00" + q
}

// resolve 惰性解析缓存文件路径（插件配置 kb.search.query_cache_file；空且落盘开关开 =
// 系统临时目录缺省文件）；首次解析时载入已存向量。返回当前路径（"" = 纯内存）。
func (c *queryVecCache) resolve() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.loaded {
		if c.file == "" {
			c.file = strings.TrimSpace(kbcfg.Get().Search.QueryCacheFile)
		}
		if c.file == "" && queryCachePersistEnabled() {
			c.file = filepath.Join(os.TempDir(), queryCacheDefaultFileName)
		}
		if c.file != "" {
			c.loadLocked()
		}
		c.loaded = true
	}
	return c.file
}

// queryCachePersistEnabled 落盘开关（插件配置 kb.search.query_cache_persist）：
// 缺省关（无向量腿时查询缓存无收益，且落盘路径不确定）；显式打开才落盘。
func queryCachePersistEnabled() bool {
	return kbcfg.Get().Search.QueryCachePersist
}

// loadLocked 载入磁盘向量（调用方持锁）；文件不存在 = 空启；损坏/版本不符 = 空启 + 提示一次
func (c *queryVecCache) loadLocked() { c.mergeDiskLocked() }

// mergeDiskLocked 读磁盘并入内存（调用方持锁）：**只补内存缺失的键**——本地已载入的值不被磁盘覆盖
// （本地为准）；文件不存在 = 空转；损坏/版本不符 = 空启不阻断（stderr 提示一次）。锁内调用 =
// 并入他进程已落盘的键（Put 的并集合并面）。
func (c *queryVecCache) mergeDiskLocked() {
	b, err := os.ReadFile(c.file)
	if err != nil {
		return
	}
	var disk struct {
		Version int                  `json:"version"`
		Vectors map[string][]float32 `json:"vectors"`
	}
	if err := json.Unmarshal(b, &disk); err != nil {
		if !c.warned {
			c.warned = true
			fmt.Fprintf(os.Stderr, "[kb] 查询向量缓存文件损坏，按空启（%s）：%v\n", c.file, err)
		}
		return
	}
	if disk.Version != queryVecCacheVersion {
		if !c.warned {
			c.warned = true
			fmt.Fprintf(os.Stderr, "[kb] 查询向量缓存版本不符（%d≠%d），按空启（%s）\n", disk.Version, queryVecCacheVersion, c.file)
		}
		return
	}
	for k, v := range disk.Vectors {
		if _, ok := c.mem[k]; !ok {
			c.mem[k] = v
		}
	}
}

// lockFile 取跨进程排他锁（flock LOCK_EX；同进程多实例亦互斥——flock 锁随打开文件描述独立）。
// 不可用 = 降级为无锁（stderr 提示一次），返回的释放函数恒非 nil。
func (c *queryVecCache) lockFile(path string) func() {
	release, err := lockQueryCacheFile(path)
	if err == nil {
		return release
	}
	c.mu.Lock()
	first := !c.warnedL
	c.warnedL = true
	c.mu.Unlock()
	if first {
		fmt.Fprintf(os.Stderr, "[kb] 查询向量缓存跨进程锁不可用，降级为无锁写（%s）：%v\n", path, err)
	}
	return func() {}
}

// Get 查询向量命中（内存 ∪ 已载入文件）
func (c *queryVecCache) Get(agent, q string) ([]float32, bool) {
	c.resolve()
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.mem[cacheKey(agent, q)]
	return v, ok
}

// Put 入缓存并（若启用文件）跨进程安全持久化：排他锁内重读磁盘 → 并集合并 → 唯一临时文件 + 原子改名
// （多实例并写不互相覆盖；同键已有值以本地最新值为准——同键向量语义等价，见 cacheKey）。
// 落盘错误返回（调用方仅告警，不阻断检索）；锁不可用 = 降级（提示一次），写入仍原子。
func (c *queryVecCache) Put(agent, q string, v []float32) error {
	file := c.resolve()
	c.mu.Lock()
	c.mem[cacheKey(agent, q)] = v
	c.mu.Unlock()
	if file == "" {
		return nil
	}
	release := c.lockFile(file)
	defer release()
	c.mu.Lock()
	c.mergeDiskLocked() // 锁内重读：并入他进程已落盘的键（并集合并 = 不覆盖）
	snapshot := make(map[string][]float32, len(c.mem))
	for k, vv := range c.mem {
		snapshot[k] = vv
	}
	c.mu.Unlock()
	b, err := json.Marshal(struct {
		Version int                  `json:"version"`
		Vectors map[string][]float32 `json:"vectors"`
	}{queryVecCacheVersion, snapshot})
	if err != nil {
		return err
	}
	return writeFileAtomic(file, b)
}

// writeFileAtomic 原子落盘：同目录唯一临时名（os.CreateTemp）→ 写入 → 改名替换；任一步失败清理临时件。
// 唯一临时名是并发要点：旧写法固定 `file+".tmp"` 时，两进程同时写会交错成坏档（读侧解析即失败）。
func writeFileAtomic(file string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(file), filepath.Base(file)+".tmp.*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil { // 权限口径与早期实现（WriteFile 0644）一致
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, file); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}
