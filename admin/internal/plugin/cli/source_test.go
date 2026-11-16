package cli

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pluginSource "github.com/ai-go-hub/ai-go-admin/internal/plugin/source"

	"github.com/spf13/cobra"
)

// fakeCatalogStore 只实现预演/安装前会用到的读方法；其余方法返回空。
type fakeCatalogStore struct {
	source    pluginSource.Source
	installed map[string]pluginSource.InstalledPlugin

	// 清单列举的透传断言（TestCatalogListQueryPropagation）：记录最后一次查询体与替身返回读数。
	// 零值 = 未被调用（listCalls == 0），因此不会掩盖「命令根本没查」这一类失败。
	listCalls     int
	lastListQuery pluginSource.ListEntriesQuery
	entries       []pluginSource.CatalogEntry
	entriesTotal  int64
}

func (f *fakeCatalogStore) ListSources(context.Context, pluginSource.ListSourcesQuery) ([]pluginSource.Source, int64, error) {
	return []pluginSource.Source{f.source}, 1, nil
}
func (f *fakeCatalogStore) GetSource(context.Context, uint) (*pluginSource.Source, error) {
	return &f.source, nil
}
func (f *fakeCatalogStore) GetSourceByName(context.Context, string) (*pluginSource.Source, error) {
	return &f.source, nil
}
func (f *fakeCatalogStore) CreateSource(context.Context, *pluginSource.Source) error { return nil }
func (f *fakeCatalogStore) UpdateSource(context.Context, uint, pluginSource.SourcePatch) (bool, error) {
	return false, nil
}
func (f *fakeCatalogStore) DeleteSources(context.Context, []uint) (int64, error) { return 0, nil }
func (f *fakeCatalogStore) ReplaceCatalog(context.Context, *pluginSource.Source, *pluginSource.Catalog, string, time.Time) error {
	return nil
}
func (f *fakeCatalogStore) MarkSyncFailure(context.Context, uint, string) error { return nil }
func (f *fakeCatalogStore) ListEntries(_ context.Context, q pluginSource.ListEntriesQuery) ([]pluginSource.CatalogEntry, int64, error) {
	f.listCalls++
	f.lastListQuery = q
	return f.entries, f.entriesTotal, nil
}
func (f *fakeCatalogStore) ListEntryPeers(context.Context, []pluginSource.UIDVersion) ([]pluginSource.CatalogEntry, error) {
	return nil, nil
}
func (f *fakeCatalogStore) ListSourcesSummary(context.Context) ([]pluginSource.SourceSummary, error) {
	return nil, nil
}
func (f *fakeCatalogStore) InstalledVersions(context.Context, []string) (map[string]pluginSource.InstalledPlugin, error) {
	if f.installed == nil {
		return map[string]pluginSource.InstalledPlugin{}, nil
	}
	return f.installed, nil
}

func sha256Text(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func writePreflightFixture(t *testing.T) (pluginSource.Source, pluginSource.PluginEntry, *Env) {
	t.Helper()
	dir := t.TempDir()
	pluginYAML := []byte(`name: demo
title: Demo
version: 1.0.0
kind: declarative
host_api: ">=1 <2"
`)
	var zipBuffer bytes.Buffer
	writer := zip.NewWriter(&zipBuffer)
	for name, data := range map[string][]byte{
		"plugin.yaml":  pluginYAML,
		"files.sha256": []byte(sha256Text(pluginYAML) + "  plugin.yaml\n"),
	} {
		entry, err := writer.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	zipBytes := zipBuffer.Bytes()
	zipPath := filepath.Join(dir, "demo-1.0.0.zip")
	if err := os.WriteFile(zipPath, zipBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	zipSHA := sha256Text(zipBytes)

	catalogYAML := fmt.Sprintf(`schema: ai-go-plugin-catalog/v1
plugins:
  - uid: demo
    version: 1.0.0
    title_zh: 示例
    title_en: Demo
    desc_zh: 简介
    desc_en: Desc
    kind: declarative
    host_api: '>=1 <2'
    package:
      path: ./demo-1.0.0.zip
      size: %d
      sha256: %s
      signed: false
`, len(zipBytes), zipSHA)
	catalogPath := filepath.Join(dir, "catalog.yaml")
	if err := os.WriteFile(catalogPath, []byte(catalogYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog, err := pluginSource.ParseCatalog([]byte(catalogYAML))
	if err != nil {
		t.Fatalf("测试清单不合法: %v", err)
	}
	src := pluginSource.Source{
		ID: 1, Name: "local", Title: "Local", Type: pluginSource.TypeLocal,
		Location: catalogPath, Enabled: 1, AllowAbsolutePaths: 1,
		LastCatalogSHA256: sha256Text([]byte(catalogYAML)),
		LastSyncStatus:    "ok",
	}
	env := &Env{Root: t.TempDir(), Prefix: "", HostVersion: "1.0.0", Out: io.Discard}
	return src, catalog.Plugins[0], env
}

func TestPreflightCatalogSuccess(t *testing.T) {
	src, entry, env := writePreflightFixture(t)
	store := &fakeCatalogStore{source: src}
	result, err := PreflightCatalog(context.Background(), env, store, src.ID, entry.UID, entry.Version, "tester")
	if err != nil {
		t.Fatalf("预演失败: %v", err)
	}
	if !result.OK || !result.Installable {
		t.Fatalf("预演应可安装: %+v", result)
	}
	if !result.AllowUnsignedRequired || result.SignaturePresent {
		t.Fatalf("未签名包应要求显式确认: %+v", result)
	}
	if !strings.Contains(result.Report, "未落盘、未写库") {
		t.Fatalf("预演报告应明确非破坏: %s", result.Report)
	}
	if result.CatalogSHA256 != src.LastCatalogSHA256 {
		t.Fatalf("清单摘要不符: %s", result.CatalogSHA256)
	}
}

func TestPreflightCatalogAlreadyInstalled(t *testing.T) {
	src, entry, env := writePreflightFixture(t)
	store := &fakeCatalogStore{
		source:    src,
		installed: map[string]pluginSource.InstalledPlugin{"demo": {Version: "1.0.0", Status: "installed"}},
	}
	result, err := PreflightCatalog(context.Background(), env, store, src.ID, entry.UID, entry.Version, "tester")
	if err != nil {
		t.Fatalf("预演失败: %v", err)
	}
	if result.Installable || result.Reason != "already_installed" {
		t.Fatalf("已安装应不可装: %+v", result)
	}
}

func TestPreflightCatalogStaleAndNotSynced(t *testing.T) {
	src, entry, env := writePreflightFixture(t)
	store := &fakeCatalogStore{source: src}

	store.source.LastCatalogSHA256 = strings.Repeat("b", 64)
	_, err := PreflightCatalog(context.Background(), env, store, src.ID, entry.UID, entry.Version, "tester")
	var catalogErr *CatalogError
	if !errors.As(err, &catalogErr) || catalogErr.Code != "catalog_stale" {
		t.Fatalf("过期快照应返回 catalog_stale: %v", err)
	}

	store.source.LastCatalogSHA256 = ""
	_, err = PreflightCatalog(context.Background(), env, store, src.ID, entry.UID, entry.Version, "tester")
	if !errors.As(err, &catalogErr) || catalogErr.Code != "catalog_not_synced" {
		t.Fatalf("未同步应返回 catalog_not_synced: %v", err)
	}
}

func TestPreflightCatalogPackageMismatch(t *testing.T) {
	src, entry, env := writePreflightFixture(t)
	store := &fakeCatalogStore{source: src}

	// 篡改清单里的 sha256：快照摘要仍是原清单，因此会走到取包校验失败
	catalogPath, err := pluginSource.ResolveLocalFile(src.Location, env.Root, true)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(data), entry.Package.SHA256, strings.Repeat("c", 64), 1)
	if err := os.WriteFile(catalogPath, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	store.source.LastCatalogSHA256 = sha256Text([]byte(tampered))
	_, err = PreflightCatalog(context.Background(), env, store, src.ID, entry.UID, entry.Version, "tester")
	var catalogErr *CatalogError
	if !errors.As(err, &catalogErr) || catalogErr.Code != "package_sha_mismatch" {
		t.Fatalf("包摘要不符应返回 package_sha_mismatch: %v", err)
	}
}

func TestInstallCatalogPreconditions(t *testing.T) {
	src, entry, env := writePreflightFixture(t)
	store := &fakeCatalogStore{source: src}
	ctx := context.Background()

	_, err := InstallCatalog(ctx, env, store, CatalogInstallRequest{
		SourceID: src.ID, UID: entry.UID, Version: entry.Version, ConfirmSHA256: entry.Package.SHA256,
	})
	var catalogErr *CatalogError
	if !errors.As(err, &catalogErr) || catalogErr.Code != "unsigned_not_allowed" {
		t.Fatalf("未签名未确认应拒绝: %v", err)
	}

	_, err = InstallCatalog(ctx, env, store, CatalogInstallRequest{
		SourceID: src.ID, UID: entry.UID, Version: entry.Version,
		AllowUnsigned: true, ConfirmSHA256: strings.Repeat("d", 64),
	})
	if !errors.As(err, &catalogErr) || catalogErr.Code != "package_sha_mismatch" {
		t.Fatalf("confirm_sha256 不符应拒绝: %v", err)
	}

	store.installed = map[string]pluginSource.InstalledPlugin{"demo": {Version: "1.0.0", Status: "installed"}}
	_, err = InstallCatalog(ctx, env, store, CatalogInstallRequest{
		SourceID: src.ID, UID: entry.UID, Version: entry.Version,
		AllowUnsigned: true, ConfirmSHA256: entry.Package.SHA256,
	})
	if !errors.As(err, &catalogErr) || catalogErr.Code != "already_installed" {
		t.Fatalf("已安装应拒绝: %v", err)
	}

	store.installed = map[string]pluginSource.InstalledPlugin{"demo": {Version: "0.9.0", Status: "installed"}}
	_, err = InstallCatalog(ctx, env, store, CatalogInstallRequest{
		SourceID: src.ID, UID: entry.UID, Version: entry.Version,
		AllowUnsigned: true, ConfirmSHA256: entry.Package.SHA256,
	})
	if !errors.As(err, &catalogErr) || catalogErr.Code != "upgrade_not_supported" {
		t.Fatalf("升级应拒绝: %v", err)
	}

	store.installed = map[string]pluginSource.InstalledPlugin{"demo": {Version: "2.0.0", Status: "installed"}}
	_, err = InstallCatalog(ctx, env, store, CatalogInstallRequest{
		SourceID: src.ID, UID: entry.UID, Version: entry.Version,
		AllowUnsigned: true, ConfirmSHA256: entry.Package.SHA256,
	})
	if !errors.As(err, &catalogErr) || catalogErr.Code != "downgrade_not_supported" {
		t.Fatalf("降级应拒绝: %v", err)
	}
}

func TestSourceAndCatalogCommands(t *testing.T) {
	sourceCmd := SourceCommand()
	for _, name := range []string{"list", "show", "add", "update", "remove", "enable", "disable", "test"} {
		if findSubCommand(sourceCmd, name) == nil {
			t.Errorf("source 缺少子命令 %s", name)
		}
	}
	catalogCmd := CatalogCommand()
	for _, name := range []string{"sync", "list", "preflight", "install", "verify"} {
		if findSubCommand(catalogCmd, name) == nil {
			t.Errorf("catalog 缺少子命令 %s", name)
		}
	}
	installCmd := findSubCommand(catalogCmd, "install")
	if installCmd == nil {
		t.Fatal("缺 catalog install")
	}
	if installCmd.Flags().Lookup("force") != nil {
		t.Fatal("catalog install 不得暴露 --force")
	}
	if installCmd.Flags().Lookup("allow-unsigned") == nil {
		t.Fatal("catalog install 应暴露 --allow-unsigned")
	}
}

func findSubCommand(parent *cobra.Command, name string) *cobra.Command {
	for _, child := range parent.Commands() {
		if child.Name() == name {
			return child
		}
	}
	return nil
}

func TestCatalogHelpers(t *testing.T) {
	// 台账/审计标签是方案定死的 catalog:<来源>:<uid>@<版本>。
	if label := catalogSourceLabel("local", "demo", "1.0.0"); label != "catalog:local:demo@1.0.0" {
		t.Fatalf("台账来源标签不符: %s", label)
	}
	// 临时 zip 文件名只有一处实现（source.PackageFileName，远程取包落盘时使用），此处只钉住
	// 两条格式的对应关系：标签含冒号、文件名用 - 与 @ 且带 .zip 后缀，二者不得各写一份格式串。
	if name := pluginSource.PackageFileName("local", "demo", "1.0.0"); name != "catalog-local-demo@1.0.0.zip" {
		t.Fatalf("临时文件名不符: %s", name)
	}
	if strings.ContainsAny(pluginSource.PackageFileName("local", "demo", "1.0.0"), ":") {
		t.Fatal("临时文件名不得含冒号")
	}
	if cmp, err := compareVersion("1.2.3", "1.3.0"); err != nil || cmp >= 0 {
		t.Fatalf("版本比较不符: %d %v", cmp, err)
	}
	err := fmt.Errorf("%w: 测试", pluginSource.ErrNetworkBlocked)
	mapped := mapCatalogSourceError(err)
	if mapped.Code != "network_blocked" || mapped.Status != 502 {
		t.Fatalf("错误映射不符: %+v", mapped)
	}
	tempPath := filepath.Join(os.TempDir(), "abc")
	text := sanitizeCatalogText("/repo/plugins/x "+tempPath, &Env{Root: "/repo"}, nil, nil)
	if strings.Contains(text, "/repo") || strings.Contains(text, os.TempDir()) {
		t.Fatalf("脱敏失败: %s", text)
	}
}

// TestCatalogListQueryPropagation 清单列举的过滤 / 分页旗标必须原样落进 ListEntriesQuery。
//
// 为什么需要这条：仓库出现过「清单列表过滤参数看着全部失效」的观测。该观测的两种可能成因——
// ①查询体被实现静默丢弃（internal/plugin/source/store.go 的 ListEntries 注释记的就是那次的形态：
// 条件预挂在 *gorm.DB 上，泛型入口的 NewDB 会话把它丢了，SQL 无 WHERE 也不报错）；
// ②命令侧没把旗标传下去——都被本用例覆盖：命令侧固定 NewSourceEnv() + sourceStore(env) 无法注入
// 替身，故走 runCatalogList seam（与命令侧逐行等价），替身记录最后一次查询体。
//
// 本用例只钉**传参没被丢**；「SQL 里真带 WHERE」由 internal/plugin/source 的库级用例承担
// （store_db_test.go 的 TestStoreListEntriesFiltersDB，CATALOG_TEST_DSN 门控、默认 SKIP），两者不可互替。
func TestCatalogListQueryPropagation(t *testing.T) {
	source := pluginSource.Source{ID: 7, Name: "alpha", Type: pluginSource.TypeLocal, Location: "alpha.yaml", Enabled: 1}
	cases := []struct {
		name    string
		opts    catalogListOptions
		want    pluginSource.ListEntriesQuery
		rows    []pluginSource.CatalogEntry
		total   int64
		outputs []string
	}{
		{
			name: "未过滤",
			opts: catalogListOptions{Page: 1, Limit: 40},
			want: pluginSource.ListEntriesQuery{Page: 1, Limit: 40},
			rows: []pluginSource.CatalogEntry{
				{UID: "demo", Version: "1.0.0"},
				{UID: "env-badge", Version: "1.0.0"},
			},
			total:   3,
			outputs: []string{"demo", "env-badge", "共 3 条"},
		},
		{
			name: "过滤命中",
			opts: catalogListOptions{Source: source.Name, UID: "demo", Version: "1.0.0", Page: 2, Limit: 5},
			want: pluginSource.ListEntriesQuery{SourceID: source.ID, UID: "demo", Version: "1.0.0", Page: 2, Limit: 5},
			rows: []pluginSource.CatalogEntry{
				{UID: "demo", Version: "1.0.0"},
			},
			// 过滤后读数（2）必须与未过滤读数（3）不同：丢了 UID/Version 就会打印 3，本条即转红。
			total:   2,
			outputs: []string{"demo", "共 2 条"},
		},
		{
			name: "过滤无命中",
			opts: catalogListOptions{UID: "no-such", Page: 1, Limit: 40},
			want: pluginSource.ListEntriesQuery{UID: "no-such", Page: 1, Limit: 40},
			// 替身按「无命中」回 0/0（模拟真库的过滤结果）；未过滤时库里是 3 条 ⇒ 这条在丢参时会是「共 3 条」。
			total:   0,
			outputs: []string{"共 0 条"},
		},
		{
			name: "过滤+分页越界",
			opts: catalogListOptions{UID: "demo", Page: 3, Limit: 1},
			want: pluginSource.ListEntriesQuery{UID: "demo", Page: 3, Limit: 1},
			// 越界页：过滤后共 2 条、第 3 页空；total 仍是**过滤后**的 2 而不是未过滤的 3。
			total:   2,
			outputs: []string{"共 2 条"},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			out := &bytes.Buffer{}
			env := &Env{Root: t.TempDir(), Out: out}
			store := &fakeCatalogStore{source: source, entries: testCase.rows, entriesTotal: testCase.total}
			if err := runCatalogList(context.Background(), env, store, testCase.opts); err != nil {
				t.Fatalf("列举失败: %v", err)
			}
			if store.listCalls != 1 {
				t.Fatalf("ListEntries 调用次数应为 1，实际 %d（命令没查即失败）", store.listCalls)
			}
			if store.lastListQuery != testCase.want {
				t.Fatalf("过滤/分页传参被丢弃或被改写：got %+v want %+v", store.lastListQuery, testCase.want)
			}
			for _, fragment := range testCase.outputs {
				if !strings.Contains(out.String(), fragment) {
					t.Fatalf("输出缺 %q：\n%s", fragment, out.String())
				}
			}
		})
	}
}
