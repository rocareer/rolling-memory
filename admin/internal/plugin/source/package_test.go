package source

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

type zipSpec struct {
	name  string
	data  []byte
	mode  fs.FileMode
	flags uint16
}

func buildZip(t *testing.T, specs []zipSpec) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, spec := range specs {
		header := &zip.FileHeader{Name: spec.name, Method: zip.Deflate, Flags: spec.flags}
		if spec.mode != 0 {
			header.SetMode(spec.mode)
		} else {
			header.SetMode(0o644)
		}
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatalf("创建 zip 条目失败: %v", err)
		}
		if _, err := entry.Write(spec.data); err != nil {
			t.Fatalf("写入 zip 条目失败: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("关闭 zip 失败: %v", err)
	}
	return buffer.Bytes()
}

func shaHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func defaultManifest() string {
	return `name: demo
title: Demo
version: 1.0.0
kind: declarative
host_api: ">=1 <2"
`
}

func packageEntry(size int64, sha string) *PluginEntry {
	signed := false
	return &PluginEntry{
		UID: "demo", Version: "1.0.0", TitleZH: "示例", TitleEN: "Demo",
		DescZH: "简介", DescEN: "Desc", Kind: "declarative", HostAPI: ">=1 <2",
		Package: CatalogPackage{
			URL: "https://example.com/demo-1.0.0.zip", Size: size, SHA256: sha, Signed: &signed,
		},
	}
}

// packageFixture 生成一个可安装的 zip（plugin.yaml + files.sha256 + 可选 ini/签名/额外文件），
// 返回 zip 路径、字节数、摘要。
func packageFixture(t *testing.T, extra map[string][]byte, withSignature bool, ini string) (string, int64, string) {
	t.Helper()
	pluginYAML := defaultManifest()
	contents := map[string][]byte{"plugin.yaml": []byte(pluginYAML)}
	if ini != "" {
		contents["ini.yaml"] = []byte(ini)
	}
	for name, data := range extra {
		contents[name] = data
	}
	names := make([]string, 0, len(contents))
	for name := range contents {
		names = append(names, name)
	}
	sort.Strings(names)
	var manifest strings.Builder
	for _, name := range names {
		fmt.Fprintf(&manifest, "%s  %s\n", shaHex(contents[name]), name)
	}
	specs := make([]zipSpec, 0, len(contents)+2)
	for _, name := range names {
		specs = append(specs, zipSpec{name: name, data: contents[name]})
	}
	specs = append(specs, zipSpec{name: packManifestFile, data: []byte(manifest.String())})
	if withSignature {
		specs = append(specs, zipSpec{name: defaultSignatureFile, data: []byte("dummy-signature")})
	}
	data := buildZip(t, specs)
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "demo-1.0.0.zip")
	if err := os.WriteFile(zipPath, data, 0o644); err != nil {
		t.Fatalf("写入 zip 失败: %v", err)
	}
	return zipPath, int64(len(data)), shaHex(data)
}

func TestInspectPackageValid(t *testing.T) {
	zipPath, size, sum := packageFixture(t, nil, false, "")
	meta, err := InspectPackage(zipPath, packageEntry(size, sum))
	if err != nil {
		t.Fatalf("合法包被拒: %v", err)
	}
	if meta.Name != "demo" || meta.Version != "1.0.0" || meta.SignaturePresent || !meta.FilesVerified {
		t.Fatalf("包元数据不符: %+v", meta)
	}
}

func TestInspectPackageWithSignatureAndINI(t *testing.T) {
	ini := "uid: demo\nversion: 1.0.0\nkind: declarative\nhost_api: '>=1 <2'\n"
	zipPath, size, sum := packageFixture(t, nil, true, ini)
	entry := packageEntry(size, sum)
	signed := true
	entry.Package.Signed = &signed
	meta, err := InspectPackage(zipPath, entry)
	if err != nil {
		t.Fatalf("带签名与 ini 的包被拒: %v", err)
	}
	if !meta.SignaturePresent || meta.INIUID != "demo" {
		t.Fatalf("签名/ini 读数不符: %+v", meta)
	}
}

func TestInspectPackageRejects(t *testing.T) {
	zipPath, size, sum := packageFixture(t, nil, false, "")
	entry := packageEntry(size, sum)

	cases := []struct {
		name  string
		entry *PluginEntry
		want  error
	}{
		{"size mismatch", func() *PluginEntry { e := *entry; e.Package.Size = size + 1; return &e }(), ErrPackageSizeMismatch},
		{"uid mismatch", func() *PluginEntry { e := *entry; e.UID = "other"; return &e }(), ErrManifestMismatch},
		{"version mismatch", func() *PluginEntry { e := *entry; e.Version = "2.0.0"; return &e }(), ErrManifestMismatch},
		{"kind mismatch", func() *PluginEntry { e := *entry; e.Kind = "code"; return &e }(), ErrManifestMismatch},
		{"host_api mismatch", func() *PluginEntry { e := *entry; e.HostAPI = ">=2"; return &e }(), ErrManifestMismatch},
		{"signed mismatch", func() *PluginEntry { e := *entry; v := true; e.Package.Signed = &v; return &e }(), ErrPackageSignedMismatch},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := InspectPackage(zipPath, testCase.entry); !errors.Is(err, testCase.want) {
				t.Fatalf("错误类型不符：got %v, want %v", err, testCase.want)
			}
		})
	}
}

func TestInspectPackageRequiresMismatch(t *testing.T) {
	manifest := `name: demo
title: Demo
version: 1.0.0
kind: declarative
host_api: ">=1 <2"
requires: dep@>=1
`
	files := map[string][]byte{"plugin.yaml": []byte(manifest)}
	manifestData := fmt.Sprintf("%s  plugin.yaml\n", shaHex(files["plugin.yaml"]))
	data := buildZip(t, []zipSpec{
		{name: "plugin.yaml", data: files["plugin.yaml"]},
		{name: packManifestFile, data: []byte(manifestData)},
	})
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "demo.zip")
	if err := os.WriteFile(zipPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	entry := packageEntry(int64(len(data)), shaHex(data))
	entry.Requires = []string{"dep@<2"}
	if _, err := InspectPackage(zipPath, entry); !errors.Is(err, ErrManifestMismatch) {
		t.Fatalf("依赖语义不一致应拒绝，got %v", err)
	}
}

func TestInspectPackageZipSafety(t *testing.T) {
	validManifest := defaultManifest()
	validFiles := fmt.Sprintf("%s  plugin.yaml\n", shaHex([]byte(validManifest)))

	cases := []struct {
		name  string
		specs []zipSpec
	}{
		{"encrypted", []zipSpec{
			{name: "plugin.yaml", data: []byte(validManifest), flags: 0x1},
			{name: packManifestFile, data: []byte(validFiles)},
		}},
		{"symlink", []zipSpec{
			{name: "plugin.yaml", data: []byte(validManifest)},
			{name: packManifestFile, data: []byte(validFiles)},
			{name: "link", data: []byte("/etc/passwd"), mode: os.ModeSymlink | 0o777},
		}},
		{"traversal", []zipSpec{
			{name: "plugin.yaml", data: []byte(validManifest)},
			{name: packManifestFile, data: []byte(validFiles)},
			{name: "../evil", data: []byte("x")},
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			data := buildZip(t, testCase.specs)
			dir := t.TempDir()
			zipPath := filepath.Join(dir, "pkg.zip")
			if err := os.WriteFile(zipPath, data, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := InspectPackage(zipPath, packageEntry(int64(len(data)), shaHex(data))); err == nil {
				t.Fatalf("%s 应被拒绝", testCase.name)
			}
		})
	}
}

func TestInspectPackageFilesManifest(t *testing.T) {
	manifest := defaultManifest()
	pluginSHA := shaHex([]byte(manifest))

	missing := buildZip(t, []zipSpec{{name: "plugin.yaml", data: []byte(manifest)}})
	dir := t.TempDir()
	missingPath := filepath.Join(dir, "missing.zip")
	if err := os.WriteFile(missingPath, missing, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectPackage(missingPath, packageEntry(int64(len(missing)), shaHex(missing))); !errors.Is(err, ErrManifestMismatch) {
		t.Fatalf("缺 files.sha256 应拒绝，got %v", err)
	}

	extra := buildZip(t, []zipSpec{
		{name: "plugin.yaml", data: []byte(manifest)},
		{name: packManifestFile, data: []byte(pluginSHA + "  plugin.yaml\n")},
		{name: "ghost.go", data: []byte("package ghost")},
	})
	extraPath := filepath.Join(dir, "extra.zip")
	if err := os.WriteFile(extraPath, extra, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectPackage(extraPath, packageEntry(int64(len(extra)), shaHex(extra))); !errors.Is(err, ErrManifestMismatch) {
		t.Fatalf("清单外夹带应拒绝，got %v", err)
	}

	listedMissing := buildZip(t, []zipSpec{
		{name: "plugin.yaml", data: []byte(manifest)},
		{name: packManifestFile, data: []byte(pluginSHA + "  plugin.yaml\n" + testSHA + "  ghost.go\n")},
	})
	listedPath := filepath.Join(dir, "listed.zip")
	if err := os.WriteFile(listedPath, listedMissing, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectPackage(listedPath, packageEntry(int64(len(listedMissing)), shaHex(listedMissing))); !errors.Is(err, ErrManifestMismatch) {
		t.Fatalf("清单缺失项应拒绝，got %v", err)
	}
}

func TestInspectPackageINIRules(t *testing.T) {
	iniOnly := buildZip(t, []zipSpec{{name: "ini.yaml", data: []byte("uid: demo\nversion: 1.0.0\n")}})
	dir := t.TempDir()
	path := filepath.Join(dir, "ini-only.zip")
	if err := os.WriteFile(path, iniOnly, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectPackage(path, packageEntry(int64(len(iniOnly)), shaHex(iniOnly))); err == nil || !strings.Contains(err.Error(), "标准包暂不可") {
		t.Fatalf("仅 ini.yaml 的包应给出明确拒绝，got %v", err)
	}

	zipPath, size, sum := packageFixture(t, nil, false, "uid: other\nversion: 1.0.0\n")
	if _, err := InspectPackage(zipPath, packageEntry(size, sum)); !errors.Is(err, ErrManifestMismatch) {
		t.Fatalf("ini.uid 与 plugin.yaml.name 不一致应拒绝，got %v", err)
	}
}

func TestResolveLocalFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "catalog"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "catalog", "catalog.yaml")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ResolveLocalFile("catalog/catalog.yaml", root, false)
	if err != nil || got != resolvedTarget {
		t.Fatalf("合法相对路径解析失败: %v %s", err, got)
	}
	if _, err := ResolveLocalFile("../catalog.yaml", root, false); !errors.Is(err, ErrPathEscape) {
		t.Fatalf("向上穿越应拒绝，got %v", err)
	}
	if _, err := ResolveLocalFile(target, root, false); !errors.Is(err, ErrPathEscape) {
		t.Fatalf("未开启开关的绝对路径应拒绝，got %v", err)
	}
	if _, err := ResolveLocalFile(target, root, true); err != nil {
		t.Fatalf("显式允许的绝对路径应接受: %v", err)
	}

	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "outside.yaml")
	if err := os.WriteFile(outsideFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("当前平台不支持符号链接: %v", err)
	}
	if _, err := ResolveLocalFile("link/outside.yaml", root, false); !errors.Is(err, ErrPathEscape) {
		t.Fatalf("父目录符号链接应拒绝，got %v", err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(root, "catalog", "leaf.yaml")); err != nil {
		t.Skipf("当前平台不支持符号链接: %v", err)
	}
	if _, err := ResolveLocalFile("catalog/leaf.yaml", root, false); !errors.Is(err, ErrPathEscape) {
		t.Fatalf("最终目标符号链接应拒绝，got %v", err)
	}
}

func TestLoadCatalogAndResolveLocalPackage(t *testing.T) {
	dir := t.TempDir()
	zipPath, size, sum := packageFixture(t, nil, false, "")
	zipBytes, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	pkgPath := filepath.Join(dir, "demo-1.0.0.zip")
	if err := os.WriteFile(pkgPath, zipBytes, 0o644); err != nil {
		t.Fatal(err)
	}
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
`, size, sum)
	catalogPath := filepath.Join(dir, "catalog.yaml")
	if err := os.WriteFile(catalogPath, []byte(catalogYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	src := &Source{Name: "local", Type: TypeLocal, Location: catalogPath, AllowAbsolutePaths: 1}
	if err := src.NormalizeAndValidate(); err != nil {
		t.Fatalf("来源校验失败: %v", err)
	}
	doc, err := LoadCatalog(t.Context(), src, dir)
	if err != nil {
		t.Fatalf("读取清单失败: %v", err)
	}
	pkg, err := ResolvePackage(t.Context(), src, doc, &doc.Catalog.Plugins[0])
	if err != nil {
		t.Fatalf("取包失败: %v", err)
	}
	if pkg.Size != size || pkg.SHA256 != sum {
		t.Fatalf("包读数不符: %+v", pkg)
	}
	if _, err := InspectPackage(pkg.Path, &doc.Catalog.Plugins[0]); err != nil {
		t.Fatalf("包预检失败: %v", err)
	}

	// 摘要不符
	badEntry := doc.Catalog.Plugins[0]
	badEntry.Package.SHA256 = strings.Repeat("b", 64)
	if _, err := ResolvePackage(t.Context(), src, doc, &badEntry); !errors.Is(err, ErrPackageSHAMismatch) {
		t.Fatalf("摘要不符应拒绝，got %v", err)
	}

	// 远程清单不得使用 path
	remote := &CatalogDocument{FinalURL: nil}
	remote.FinalURL = mustParse("https://example.com/catalog.yaml")
	if _, err := ResolvePackage(t.Context(), src, remote, &doc.Catalog.Plugins[0]); err == nil {
		t.Fatal("远程清单使用 path 应拒绝")
	}
}

func TestReadFileLimited(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if data, err := ReadFileLimited(path, 5); err != nil || string(data) != "hello" {
		t.Fatalf("读取失败: %v %q", err, data)
	}
	if _, err := ReadFileLimited(path, 4); err == nil {
		t.Fatal("超限读取应拒绝")
	}
}

func mustParse(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return u
}

func TestInspectPackageSignatureListed(t *testing.T) {
	manifest := defaultManifest()
	listed := fmt.Sprintf("%s  plugin.yaml\n%s  %s\n", shaHex([]byte(manifest)), testSHA, defaultSignatureFile)
	data := buildZip(t, []zipSpec{
		{name: "plugin.yaml", data: []byte(manifest)},
		{name: packManifestFile, data: []byte(listed)},
		{name: defaultSignatureFile, data: []byte("sig")},
	})
	dir := t.TempDir()
	path := filepath.Join(dir, "sig.zip")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	signed := true
	entry := packageEntry(int64(len(data)), shaHex(data))
	entry.Package.Signed = &signed
	if _, err := InspectPackage(path, entry); err == nil {
		t.Fatal("files.sha256 列出签名文件应拒绝")
	}
}
