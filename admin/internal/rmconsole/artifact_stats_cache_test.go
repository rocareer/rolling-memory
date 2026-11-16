// plugin: rolling-memory v0.0.58
package rollingmemory

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// seedVaultFixture 建最小金库夹具：docs/chunks 两表只保留 address_uri 列
// （全库去重统计只读该列），空串与 NULL 行一并写入以钉住过滤口径。
func seedVaultFixture(t *testing.T, dbPath string, docURIs, chunkURIs []string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS thin_vault_docs (address_uri TEXT);`,
		`CREATE TABLE IF NOT EXISTS thin_vault_bg_chunks (address_uri TEXT);`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	for _, uri := range docURIs {
		if _, err := db.Exec(`INSERT INTO thin_vault_docs (address_uri) VALUES (` + sqlString(uri) + `);`); err != nil {
			t.Fatalf("insert doc %q: %v", uri, err)
		}
	}
	for _, uri := range chunkURIs {
		if _, err := db.Exec(`INSERT INTO thin_vault_bg_chunks (address_uri) VALUES (` + sqlString(uri) + `);`); err != nil {
			t.Fatalf("insert chunk %q: %v", uri, err)
		}
	}
}

// sqlString 只服务测试夹具的字面量拼接（固定语料，无外部输入）。
func sqlString(s string) string {
	if s == "" {
		return `''`
	}
	return `'` + s + `'`
}

func expireArtifactCellsCache(t *testing.T, dbPath string) {
	t.Helper()
	artifactCellsMu.Lock()
	defer artifactCellsMu.Unlock()
	if e := artifactCellsCache[dbPath]; e != nil {
		e.at = time.Now().Add(-2 * artifactCellsCacheTTL)
	}
}

func dropArtifactCellsCache(t *testing.T, dbPath string) {
	t.Helper()
	artifactCellsMu.Lock()
	defer artifactCellsMu.Unlock()
	delete(artifactCellsCache, dbPath)
}

// TestCachedArtifactCellsReuseAndRefresh 钉住 TTL 缓存的三段契约：
// ① 首次现算；② TTL 内复用（金库新增行不触发重算）；③ 过期后重算取到新值。
func TestCachedArtifactCellsReuseAndRefresh(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "memory.db")
	seedVaultFixture(t, dbPath,
		[]string{"cell-a#1", "cell-a#2", "cell-b#1", ""},
		[]string{"cell-b#9", "cell-c#3"})
	dropArtifactCellsCache(t, dbPath)
	t.Cleanup(func() { dropArtifactCellsCache(t, dbPath) })

	// NULL 行：docs 里显式写入一条 NULL 以钉住 `address_uri != ''` 对 NULL 的排除。
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO thin_vault_docs (address_uri) VALUES (NULL);`); err != nil {
		t.Fatalf("insert null: %v", err)
	}
	_ = db.Close()

	// docs 去重 {cell-a, cell-b} ∪ chunks 去重 {cell-b, cell-c} = 3。
	if got := cachedArtifactCells(dbPath); got != 3 {
		t.Fatalf("first compute = %d, want 3", got)
	}

	// TTL 内新增一行：仍复用缓存值。
	db, err = sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO thin_vault_docs (address_uri) VALUES ('cell-d#1');`); err != nil {
		t.Fatalf("insert new doc: %v", err)
	}
	_ = db.Close()
	if got := cachedArtifactCells(dbPath); got != 3 {
		t.Fatalf("within TTL = %d, want cached 3", got)
	}

	// 过期后重算：拿到新值 4。
	expireArtifactCellsCache(t, dbPath)
	if got := cachedArtifactCells(dbPath); got != 4 {
		t.Fatalf("after expiry = %d, want 4", got)
	}
}

// TestCachedArtifactCellsFailureFallback 钉住失败口径：
// ① 从未成功（库不可读）= 0（查询失败保持零值）；
// ② 有历史成功读数时，过期重算失败回退上次读数而非归零。
func TestCachedArtifactCellsFailureFallback(t *testing.T) {
	absent := filepath.Join(t.TempDir(), "absent.db")
	dropArtifactCellsCache(t, absent)
	if got := cachedArtifactCells(absent); got != 0 {
		t.Fatalf("first failure = %d, want 0", got)
	}

	dbPath := filepath.Join(t.TempDir(), "memory.db")
	seedVaultFixture(t, dbPath, []string{"cell-a#1", "cell-b#1"}, nil)
	dropArtifactCellsCache(t, dbPath)
	t.Cleanup(func() { dropArtifactCellsCache(t, dbPath) })
	if got := cachedArtifactCells(dbPath); got != 2 {
		t.Fatalf("prime = %d, want 2", got)
	}

	if err := os.Remove(dbPath); err != nil {
		t.Fatalf("remove: %v", err)
	}
	expireArtifactCellsCache(t, dbPath)
	if got := cachedArtifactCells(dbPath); got != 2 {
		t.Fatalf("stale fallback = %d, want previous 2", got)
	}
}
