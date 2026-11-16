// plugin: kb v1.0.0
// kb 查询向量缓存多实例安全测试（条目 kb-qcache；零 DB 零 LLM）。
//
// 修复面（service/kb/search/querycache.go）：缺省文件仍是共享载体（os.TempDir()/kb-qvec.json，
// 常量 queryCacheDefaultFileName），写路径改「跨进程排他锁（flock）→ 锁内重读磁盘 → 并集合并 →
// 唯一临时名 + 原子改名」——多实例并发写不互相覆盖、文件不损坏；显式文件键语义不变。
// 先例：多实例缓存分叉的同型修复。
//
// 跑法：
//
//	go test ./plugins/kb/service/kb/search/ -run 'TestQueryVecCacheMultiInstance|TestQueryVecCacheNegativeControl' -count=1 -v
//	go test -race ./plugins/kb/service/kb/search/ -run 'TestQueryVecCacheMultiInstance' -count=1
package search

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// kbQvecDisk 落盘形态（读回校验用；与 querycache.go 内联结构同形）
type kbQvecDisk struct {
	Version int                  `json:"version"`
	Vectors map[string][]float32 `json:"vectors"`
}

// readKbQvecFile 读并解析缓存文件（坏档即失败）
func readKbQvecFile(t *testing.T, path string) kbQvecDisk {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读缓存文件失败: %v", err)
	}
	var disk kbQvecDisk
	if err := json.Unmarshal(b, &disk); err != nil {
		t.Fatalf("缓存文件坏档（%d 字节）: %v", len(b), err)
	}
	return disk
}

// TestQueryVecCacheMultiInstanceConcurrentSameFile 两实例（两 goroutine = 两进程同缺省文件）并发写
// 同一缺省路径：各自读回正确 / 第三实例见两侧并集（Put 未互相覆盖）/ 全程并发读不见坏档。
func TestQueryVecCacheMultiInstanceConcurrentSameFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, queryCacheDefaultFileName) // 同路径 = 同缺省文件（多实例共享载体）

	c1, c2 := newQueryVecCache(), newQueryVecCache()
	c1.file, c1.loaded = f, true // 直接注入 = 等价 config 解析后的缺省路径（与 search_unit_test.go 同法）
	c2.file, c2.loaded = f, true

	const n = 40
	var wg sync.WaitGroup
	errCh := make(chan error, 2*n)
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			if err := c1.Put("kb-embed", fmt.Sprintf("q1-%d", i), []float32{1, float32(i)}); err != nil {
				errCh <- fmt.Errorf("c1.Put(q1-%d): %w", i, err)
			}
		}(i)
		go func(i int) {
			defer wg.Done()
			if err := c2.Put("kb-embed", fmt.Sprintf("q2-%d", i), []float32{2, float32(i)}); err != nil {
				errCh <- fmt.Errorf("c2.Put(q2-%d): %w", i, err)
			}
		}(i)
	}

	// 并发读侧（模拟第三个实例/守护进程持续读）：写入全程不得读到坏档（原子改名保证）
	stop := make(chan struct{})
	var reads int64
	readErr := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			b, err := os.ReadFile(f)
			if err != nil {
				if os.IsNotExist(err) {
					continue // 首写前
				}
				readErr <- err
				return
			}
			var disk kbQvecDisk
			if err := json.Unmarshal(b, &disk); err != nil {
				readErr <- fmt.Errorf("并发读见坏档（%d 字节）: %w", len(b), err)
				return
			}
			atomic.AddInt64(&reads, 1)
		}
	}()

	wg.Wait()
	close(stop)
	close(errCh)
	for err := range errCh {
		t.Fatalf("并发 Put 失败: %v", err)
	}
	select {
	case err := <-readErr:
		t.Fatalf("并发读失败: %v", err)
	default:
	}
	if atomic.LoadInt64(&reads) == 0 {
		t.Fatal("并发读侧未实际读到文件（测例无效）")
	}

	// ① 各自读回自己的键（内存面）
	for i := 0; i < n; i++ {
		if v, ok := c1.Get("kb-embed", fmt.Sprintf("q1-%d", i)); !ok || len(v) != 2 || v[1] != float32(i) {
			t.Fatalf("c1 读回 q1-%d 异常：ok=%v v=%v", i, ok, v)
		}
		if v, ok := c2.Get("kb-embed", fmt.Sprintf("q2-%d", i)); !ok || len(v) != 2 || v[1] != float32(i) {
			t.Fatalf("c2 读回 q2-%d 异常：ok=%v v=%v", i, ok, v)
		}
	}
	// ② 第三实例（模拟新进程/重启载入）：文件是并集——两侧键都在（Put 不得互相覆盖）
	c3 := newQueryVecCache()
	c3.file = f
	for i := 0; i < n; i++ {
		if _, ok := c3.Get("kb-embed", fmt.Sprintf("q1-%d", i)); !ok {
			t.Fatalf("文件缺 c1 键 q1-%d（被 c2 覆盖）", i)
		}
		if _, ok := c3.Get("kb-embed", fmt.Sprintf("q2-%d", i)); !ok {
			t.Fatalf("文件缺 c2 键 q2-%d（被 c1 覆盖）", i)
		}
	}
	// ③ 文件不损坏 + 版本 + 键数 = 2n（无覆盖、无丢键）
	disk := readKbQvecFile(t, f)
	if disk.Version != queryVecCacheVersion {
		t.Fatalf("文件版本不符：%d≠%d", disk.Version, queryVecCacheVersion)
	}
	if len(disk.Vectors) != 2*n {
		t.Fatalf("文件键数不符：%d≠%d（并发写互相覆盖或丢键）", len(disk.Vectors), 2*n)
	}
	// ④ 无临时件残留（唯一临时名 + 改名后即清）
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp.") {
			t.Fatalf("残留临时件: %s", e.Name())
		}
	}
}

// oldKbSnapshotPut 修复前写法复刻（整文件快照覆盖：固定临时名 file+".tmp"、无锁、无合并）——
// 仅负控用：证明「同文件 + 冷内存/并发 Put」在该写法下确会丢键（本测因此能区分修复前后）。
func oldKbSnapshotPut(t *testing.T, f string, mem map[string][]float32) {
	t.Helper()
	b, err := json.Marshal(kbQvecDisk{Version: queryVecCacheVersion, Vectors: mem})
	if err != nil {
		t.Fatal(err)
	}
	tmp := f + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, f); err != nil {
		t.Fatal(err)
	}
}

// TestQueryVecCacheNegativeControlSameAndExplicitFiles 负控（显式同文件；行为如实记录）：
//
//	a) 修复前写法（测试内复刻 oldKbSnapshotPut）：第二个实例整文件快照覆盖 ⇒ 第一实例的键丢失（负控成立）；
//	b) 修复后（本实现）：冷内存实例（未载入文件）直接 Put ⇒ 锁内重读合并，磁盘既有键全保留；
//	c) 显式文件键语义不变（隔离档）：两实例各指一路径 ⇒ 互不可见；且显式路径同样受本保护（b 面即显式路径）。
func TestQueryVecCacheNegativeControlSameAndExplicitFiles(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "explicit-kb-qvec.json") // 显式文件键：两实例显式指向同一路径

	c1 := newQueryVecCache()
	c1.file, c1.loaded = f, true
	if err := c1.Put("kb-embed", "k1", []float32{1}); err != nil {
		t.Fatal(err)
	}

	// a) 负控：修复前写法（另一个冷实例的整文件快照）会把 k1 抹掉——如实证明覆盖风险真实存在
	oldKbSnapshotPut(t, f, map[string][]float32{cacheKey("kb-embed", "k2"): {2}})
	if _, ok := readKbQvecFile(t, f).Vectors[cacheKey("kb-embed", "k1")]; ok {
		t.Fatal("负控失效：修复前写法下 k1 竟未丢（测例无法区分覆盖风险）")
	}

	// b) 修复后：重写 k1，再由「冷内存实例」（loaded=true 但内存空，等价未载入的另一进程）Put k3
	if err := c1.Put("kb-embed", "k1", []float32{1}); err != nil {
		t.Fatal(err)
	}
	cold := newQueryVecCache()
	cold.file, cold.loaded = f, true // 冷内存：未载入既有文件
	if err := cold.Put("kb-embed", "k3", []float32{3}); err != nil {
		t.Fatal(err)
	}
	disk := readKbQvecFile(t, f)
	for _, k := range []string{"k1", "k2", "k3"} {
		if _, ok := disk.Vectors[cacheKey("kb-embed", k)]; !ok {
			t.Fatalf("显式同文件下键 %s 丢失（合并未生效）", k)
		}
	}

	// c) 显式独立文件（硬隔离档：多实例各一路径）——互不可见，语义不变
	fa, fb := filepath.Join(dir, "a.json"), filepath.Join(dir, "b.json")
	ca, cb := newQueryVecCache(), newQueryVecCache()
	ca.file, ca.loaded = fa, true
	cb.file, cb.loaded = fb, true
	if err := ca.Put("kb-embed", "only-a", []float32{1}); err != nil {
		t.Fatal(err)
	}
	if err := cb.Put("kb-embed", "only-b", []float32{2}); err != nil {
		t.Fatal(err)
	}
	if _, ok := readKbQvecFile(t, fb).Vectors[cacheKey("kb-embed", "only-a")]; ok {
		t.Fatal("显式独立文件之间不应互相可见")
	}
	if _, ok := readKbQvecFile(t, fa).Vectors[cacheKey("kb-embed", "only-b")]; ok {
		t.Fatal("显式独立文件之间不应互相可见")
	}
}

// TestQueryVecCacheHelperProcess 子进程专用入口（被 TestQueryVecCacheMultiProcessSameFile 以本测试二进制重入）：
// 读 env 注入的路径/agent/条数，直接 Put，完成即退。
//
// **独立执行时必然 SKIP**（缺 PLUGIN_KB_QVEC_MULTIPROC_HELPER=1 就无事可做）——该 SKIP 是设计形态而非
// 覆盖丢失：真正的多实例语义由父用例 `TestQueryVecCacheMultiProcessSameFile` 断言（它会拉起本入口）。
// 故聚合读数应写成「1 条子进程专用 SKIP，不计覆盖丢失」，不得简化成「SKIP=0」；
// 除本入口外的任何 SKIP 都算覆盖丢失（判据见 plugins/kb/README.md 的 core 段）。
func TestQueryVecCacheHelperProcess(t *testing.T) {
	if os.Getenv("PLUGIN_KB_QVEC_MULTIPROC_HELPER") != "1" {
		t.Skip("子进程专用入口（父用例以 PLUGIN_KB_QVEC_MULTIPROC_HELPER=1 重入本二进制）；独立执行必然跳过，不计覆盖丢失")
	}
	file := os.Getenv("PLUGIN_KB_QVEC_MULTIPROC_FILE")
	agent := os.Getenv("PLUGIN_KB_QVEC_MULTIPROC_AGENT")
	n, err := strconv.Atoi(os.Getenv("PLUGIN_KB_QVEC_MULTIPROC_N"))
	if file == "" || agent == "" || err != nil || n <= 0 {
		t.Fatalf("子进程入参不全：file=%q agent=%q n=%q", file, agent, os.Getenv("PLUGIN_KB_QVEC_MULTIPROC_N"))
	}
	c := newQueryVecCache()
	c.file, c.loaded = file, true
	for i := 0; i < n; i++ {
		if err := c.Put(agent, fmt.Sprintf("q-%d", i), []float32{1, float32(i)}); err != nil {
			t.Fatalf("子进程 Put 失败: %v", err)
		}
	}
}

// TestQueryVecCacheMultiProcessSameFile 真多进程面（缺省路径共享载体）：K 个**真实进程**并发写同一文件
// ⇒ 文件为并集（各进程键全在、无覆盖）、不损坏。goroutine 面的等价性见上条测试；本条把「多实例」
// 钉到 OS 进程级（flock 随打开文件描述，跨进程互斥）。
func TestQueryVecCacheMultiProcessSameFile(t *testing.T) {
	const (
		procs = 3
		per   = 25
	)
	dir := t.TempDir()
	f := filepath.Join(dir, queryCacheDefaultFileName)

	var wg sync.WaitGroup
	errCh := make(chan error, procs)
	for p := 0; p < procs; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestQueryVecCacheHelperProcess$", "-test.v")
			cmd.Env = append(os.Environ(),
				"PLUGIN_KB_QVEC_MULTIPROC_HELPER=1",
				"PLUGIN_KB_QVEC_MULTIPROC_FILE="+f,
				fmt.Sprintf("PLUGIN_KB_QVEC_MULTIPROC_AGENT=agent-p%d", p),
				fmt.Sprintf("PLUGIN_KB_QVEC_MULTIPROC_N=%d", per),
			)
			if out, err := cmd.CombinedOutput(); err != nil {
				errCh <- fmt.Errorf("子进程 %d 失败: %v\n%s", p, err, out)
			}
		}(p)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("多进程并发写失败: %v", err)
	}
	disk := readKbQvecFile(t, f)
	if len(disk.Vectors) != procs*per {
		t.Fatalf("文件键数 %d ≠ %d（跨进程互相覆盖或丢键）", len(disk.Vectors), procs*per)
	}
	for p := 0; p < procs; p++ {
		for i := 0; i < per; i++ {
			if _, ok := disk.Vectors[cacheKey(fmt.Sprintf("agent-p%d", p), fmt.Sprintf("q-%d", i))]; !ok {
				t.Fatalf("缺进程 p%d 的键 q-%d（被其它进程覆盖）", p, i)
			}
		}
	}
}
