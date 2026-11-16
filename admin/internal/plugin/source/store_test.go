package source

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"
)

type memorySourceStore struct {
	source        Source
	replaced      *Catalog
	replacedSHA   string
	replaceErr    error
	failureMsg    string
	replaceCalled bool
	failureCalled bool
}

func (m *memorySourceStore) ListSources(context.Context, ListSourcesQuery) ([]Source, int64, error) {
	return []Source{m.source}, 1, nil
}
func (m *memorySourceStore) GetSource(context.Context, uint) (*Source, error) { return &m.source, nil }
func (m *memorySourceStore) GetSourceByName(context.Context, string) (*Source, error) {
	return &m.source, nil
}
func (m *memorySourceStore) CreateSource(context.Context, *Source) error { return nil }
func (m *memorySourceStore) UpdateSource(context.Context, uint, SourcePatch) (bool, error) {
	return false, nil
}
func (m *memorySourceStore) DeleteSources(context.Context, []uint) (int64, error) { return 0, nil }
func (m *memorySourceStore) ReplaceCatalog(_ context.Context, _ *Source, catalog *Catalog, sha string, _ time.Time) error {
	m.replaceCalled = true
	m.replaced = catalog
	m.replacedSHA = sha
	return m.replaceErr
}
func (m *memorySourceStore) MarkSyncFailure(_ context.Context, _ uint, message string) error {
	m.failureCalled = true
	m.failureMsg = message
	return nil
}
func (m *memorySourceStore) ListEntries(context.Context, ListEntriesQuery) ([]CatalogEntry, int64, error) {
	return nil, 0, nil
}
func (m *memorySourceStore) ListEntryPeers(context.Context, []UIDVersion) ([]CatalogEntry, error) {
	return nil, nil
}
func (m *memorySourceStore) ListSourcesSummary(context.Context) ([]SourceSummary, error) {
	return nil, nil
}
func (m *memorySourceStore) InstalledVersions(context.Context, []string) (map[string]InstalledPlugin, error) {
	return map[string]InstalledPlugin{}, nil
}

func writeCatalogFile(t *testing.T, content string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, path
}

func simpleCatalog() string {
	return `schema: ai-go-plugin-catalog/v1
plugins:
  - uid: demo
    version: 1.0.0
    title_zh: 示例
    title_en: Demo
    desc_zh: 简介
    desc_en: Desc
    kind: code
    host_api: '>=1 <2'
    package:
      url: https://example.com/demo-1.0.0.zip
      size: 1
      sha256: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
      signed: false
`
}

func TestSyncSourceSuccess(t *testing.T) {
	dir, path := writeCatalogFile(t, simpleCatalog())
	src := Source{ID: 7, Name: "local", Type: TypeLocal, Location: path, Enabled: 1, AllowAbsolutePaths: 1}
	store := &memorySourceStore{source: src}
	result := SyncSource(context.Background(), store, &src, dir, false)
	if result.Status != "ok" || result.Entries != 1 || result.CatalogSHA256 == "" {
		t.Fatalf("同步结果不符: %+v", result)
	}
	if !store.replaceCalled || store.replaced == nil || len(store.replaced.Plugins) != 1 {
		t.Fatal("成功同步应替换快照")
	}
}

func TestSyncSourceFailureKeepsOldSnapshot(t *testing.T) {
	dir, path := writeCatalogFile(t, "schema: wrong\nplugins: []\n")
	src := Source{ID: 7, Name: "local", Type: TypeLocal, Location: path, Enabled: 1, AllowAbsolutePaths: 1}
	store := &memorySourceStore{source: src}
	result := SyncSource(context.Background(), store, &src, dir, false)
	if result.Status != "error" || result.Error == "" {
		t.Fatalf("非法清单应失败: %+v", result)
	}
	if store.replaceCalled {
		t.Fatal("解析失败不应替换快照")
	}
	if !store.failureCalled || store.failureMsg == "" {
		t.Fatal("失败应记录 last_sync_error")
	}
}

func TestSyncSourceVersionMutation(t *testing.T) {
	dir, path := writeCatalogFile(t, simpleCatalog())
	src := Source{ID: 7, Name: "local", Type: TypeLocal, Location: path, Enabled: 1, AllowAbsolutePaths: 1}
	store := &memorySourceStore{source: src, replaceErr: ErrVersionContentMutated}
	result := SyncSource(context.Background(), store, &src, dir, false)
	if result.Status != "error" || !strings.Contains(result.Error, "同一版本") {
		t.Fatalf("版本内容变化应报错: %+v", result)
	}
	if !store.failureCalled {
		t.Fatal("应记录失败")
	}
}

func TestSyncSourceDisabled(t *testing.T) {
	src := Source{ID: 7, Name: "local", Type: TypeLocal, Location: "x", Enabled: 0}
	store := &memorySourceStore{source: src}
	result := SyncSource(context.Background(), store, &src, t.TempDir(), false)
	if result.Status != "error" || !strings.Contains(result.Error, "停用") || store.replaceCalled {
		t.Fatalf("停用来源不应同步: %+v", result)
	}
}

func TestSyncSourceDryRun(t *testing.T) {
	dir, path := writeCatalogFile(t, simpleCatalog())
	src := Source{ID: 7, Name: "local", Type: TypeLocal, Location: path, Enabled: 1, AllowAbsolutePaths: 1}
	store := &memorySourceStore{source: src}
	result := SyncSource(context.Background(), store, &src, dir, true)
	if result.Status != "ok" || store.replaceCalled {
		t.Fatalf("dry-run 不应写库: %+v", result)
	}
}

func TestTestSource(t *testing.T) {
	dir, path := writeCatalogFile(t, simpleCatalog())
	src := &Source{Name: "local", Type: TypeLocal, Location: path, AllowAbsolutePaths: 1}
	result := TestSource(context.Background(), src, dir)
	if !result.OK || result.Schema != CatalogSchema || result.Entries != 1 || result.CatalogSHA256 == "" {
		t.Fatalf("来源测试应通过: %+v", result)
	}

	_, badPath := writeCatalogFile(t, "schema: wrong\nplugins: []\n")
	bad := &Source{Name: "bad", Type: TypeLocal, Location: badPath, AllowAbsolutePaths: 1}
	result = TestSource(context.Background(), bad, dir)
	if result.OK || len(result.Errors) == 0 {
		t.Fatalf("非法清单应给出错误: %+v", result)
	}
}

func TestSourceValidation(t *testing.T) {
	valid := Source{Name: "local", Title: "Local", Type: TypeLocal, Location: "catalog/catalog.yaml", Enabled: 1}
	if err := valid.NormalizeAndValidate(); err != nil {
		t.Fatalf("合法来源被拒: %v", err)
	}
	cases := []Source{
		{Name: "Bad", Type: TypeLocal, Location: "x", Enabled: 1},
		{Name: "a", Type: TypeLocal, Location: "x", Enabled: 1},
		{Name: "local", Type: "ftp", Location: "x", Enabled: 1},
		{Name: "local", Type: TypeLocal, Location: "", Enabled: 1},
		{Name: "local", Type: TypeLocal, Location: "../x", Enabled: 1},
		{Name: "local", Type: TypeLocal, Location: "/abs/x", Enabled: 1},
		{Name: "local", Type: TypeLocal, Location: "x", Enabled: 2},
		{Name: "local", Type: TypeHTTP, Location: "http://example.com/x", Enabled: 1},
		{Name: "local", Type: TypeHTTP, Location: "https://user:pass@example.com/x", Enabled: 1},
	}
	for i, testCase := range cases {
		if err := testCase.NormalizeAndValidate(); err == nil {
			t.Errorf("第 %d 个非法来源未被拒绝: %+v", i, testCase)
		}
	}
}

func TestHostReservedNamesExtended(t *testing.T) {
	for _, name := range HostPageReserved() {
		found := false
		for _, reserved := range manifest.ReservedNames {
			if reserved == name {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("manifest.ReservedNames 应包含 %q", name)
		}
	}
	manifestYAML := []byte("name: source\ntitle: Source\nversion: 1.0.0\nkind: code\nhost_api: '>=1 <2'\n")
	if _, _, err := manifest.Parse(manifestYAML, manifest.Options{}); err == nil {
		t.Fatal("安装期 manifest 校验应拒绝名为 source 的插件")
	}
	// 两个词都要过安装期校验（不是只靠门禁脚本拦）
	for _, name := range HostPageReserved() {
		raw := []byte("name: " + name + "\ntitle: X\nversion: 1.0.0\nkind: code\nhost_api: '>=1 <2'\n")
		if _, _, err := manifest.Parse(raw, manifest.Options{}); err == nil {
			t.Fatalf("安装期 manifest 校验应拒绝名为 %q 的插件", name)
		}
	}
	if err := CheckReservedSync("", ""); err != nil {
		t.Fatalf("正本自检失败（空说明侧）: %v", err)
	}
}

// TestReservedNamesThreeWaySync 保留字三处同步的哨兵：正本（manifest.ReservedNames）、
// 静态门禁脚本（校验脚本 的 RESERVED_NAMES）与说明
// （plugins/README.md 的保留字条目）三处的词表必须逐词相等——任一处漏改即报红。
func TestReservedNamesThreeWaySync(t *testing.T) {
	root := repoRootForTest(t)
	read := func(rel string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("读取 %s 失败: %v", rel, err)
		}
		return string(data)
	}
	readmeText := read("plugins/README.md")
	scriptRel := filepath.Join("scripts", "governance", "check-plugin-manifest"+".sh")
	scriptBytes, scriptErr := os.ReadFile(filepath.Join(root, scriptRel))
	if os.IsNotExist(scriptErr) {
		if err := CheckReservedSync("", readmeText); err != nil {
			t.Fatalf("保留字不同步: %v", err)
		}
		return
	}
	if scriptErr != nil {
		t.Fatalf("读取 %s 失败: %v", scriptRel, scriptErr)
	}
	scriptText := string(scriptBytes)

	if err := CheckReservedSync(scriptText, readmeText); err != nil {
		t.Fatalf("保留字三处不同步: %v", err)
	}
	// 负例：任一处少一个词/多一个词/被删都必须被抓到（否则哨兵本身是摆设）。
	const anchor = "disable source catalog"
	if err := CheckReservedSync(strings.Replace(scriptText, anchor, "disable source", 1), readmeText); err == nil {
		t.Fatal("脚本少词时应报漂移")
	}
	if err := CheckReservedSync(strings.Replace(scriptText, anchor, anchor+" extra", 1), readmeText); err == nil {
		t.Fatal("脚本多词时应报漂移")
	}
	if err := CheckReservedSync("#!/bin/sh\n", readmeText); err == nil {
		t.Fatal("脚本缺 RESERVED_NAMES 行时应报漂移")
	}
	if err := CheckReservedSync(scriptText, strings.Replace(readmeText, "、`catalog`", "", 1)); err == nil {
		t.Fatal("README 少词时应报漂移")
	}
	if err := CheckReservedSync(scriptText, strings.Replace(readmeText, "、`catalog`", "、`catalog`、`extra`", 1)); err == nil {
		t.Fatal("README 多词时应报漂移")
	}
	if err := CheckReservedSync(scriptText, "# README 里没有保留字条目\n"); err == nil {
		t.Fatal("README 缺保留字条目行时应报漂移")
	}
	// 说明行只认反引号词表：同一行若改成纯文字（词表被抹掉）也要报，不能静默通过
	if err := CheckReservedSync(scriptText, "- **宿主保留字**（插件名不得命中）：见 manifest。\n"); err == nil {
		t.Fatal("README 保留字条目没有词表时应报漂移")
	}
}

// repoRootForTest 从测试工作目录向上找含 go.mod 的仓库根（与 cli.FindRoot 同口径；
// 不复用 cli.FindRoot 是为了避免 source → cli 的循环依赖）。
func repoRootForTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("取工作目录失败: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("未找到仓库根（go.mod）")
		}
		dir = parent
	}
}

func TestTruncateSyncError(t *testing.T) {
	long := strings.Repeat("x", MaxSyncErrorBytes+10)
	if got := TruncateSyncError(long); len(got) != MaxSyncErrorBytes {
		t.Fatalf("截断长度不符: %d", len(got))
	}
	if got := TruncateSyncError("short"); got != "short" {
		t.Fatalf("短消息不应截断: %s", got)
	}
	if got := TruncateSyncError(strings.Repeat("错", MaxSyncErrorBytes)); !utf8.ValidString(got) || len(got) > MaxSyncErrorBytes {
		t.Fatalf("多字节截断必须是合法 UTF-8 且不超上限: len=%d", len(got))
	}
}

func TestSyncSourceStoreFailure(t *testing.T) {
	dir, path := writeCatalogFile(t, simpleCatalog())
	src := Source{ID: 7, Name: "local", Type: TypeLocal, Location: path, Enabled: 1, AllowAbsolutePaths: 1}
	store := &memorySourceStore{source: src, replaceErr: errors.New("db down")}
	result := SyncSource(context.Background(), store, &src, dir, false)
	if result.Status != "error" || !store.failureCalled {
		t.Fatalf("存储失败应上报: %+v", result)
	}
}
