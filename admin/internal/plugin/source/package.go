package source

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"

	"go.yaml.in/yaml/v3"
	"golang.org/x/text/unicode/norm"
)

// 包形态常量（与 internal/plugin/cli 的导出口径一致；本包不 import cli，避免成环）。
const (
	packManifestFile     = "files.sha256"
	defaultSignatureFile = "plugin.yaml.sig"
	pluginManifestFile   = "plugin.yaml"
	iniManifestFile      = "ini.yaml"
)

// PackageMeta 包内元数据与完整性预检读数（只读；不落盘、不执行）。
type PackageMeta struct {
	Name             string `json:"name"`
	Version          string `json:"version"`
	Kind             string `json:"kind"`
	HostAPI          string `json:"host_api"`
	FrameworkReq     string `json:"framework_req"`
	Requires         string `json:"requires"`
	INIUID           string `json:"ini_uid,omitempty"`
	SignatureFile    string `json:"signature_file"`
	SignaturePresent bool   `json:"signature_present"`
	FilesVerified    bool   `json:"files_verified"`
	PackFiles        int    `json:"pack_files"`
}

// InspectPackage 打开 zip 做安装前预检（不落盘解包）：
//   - zip 条目数/单文件/解出总量/加密/压缩比/路径穿越/符号链接；
//   - 包内元数据 plugin.yaml（必需）、ini.yaml（若存在则必须与 plugin.yaml 一致）；
//   - 清单条目 uid/version/kind/host_api/requires 与包内元数据一致；
//   - files.sha256 存在且逐项比对（清单外夹带一律拒）；
//   - package.signed 与包内签名条目存在性一致。
//
// 这里只是安装前预检；真正落位仍由 cli.StageZip + cli.Install 再强制一遍。
func InspectPackage(zipPath string, entry *PluginEntry) (*PackageMeta, error) {
	if entry == nil {
		return nil, fmt.Errorf("%w: 清单条目为空", ErrInvalidInput)
	}
	info, err := os.Lstat(zipPath)
	if err != nil {
		return nil, fmt.Errorf("%w: 读取包 %s 失败: %v", ErrPackageNotFound, zipPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: 包 %s 是符号链接", ErrPathEscape, zipPath)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: 包 %s 不是普通文件", ErrPathEscape, zipPath)
	}
	if info.Size() > MaxPackageBytes {
		return nil, fmt.Errorf("%w: 包 %s 大小 %d 超过上限 %d", ErrPackageSizeMismatch, zipPath, info.Size(), MaxPackageBytes)
	}
	if entry.Package.Size > 0 && info.Size() != entry.Package.Size {
		return nil, fmt.Errorf("%w: 清单声明 %d 字节，实际 %d 字节", ErrPackageSizeMismatch, entry.Package.Size, info.Size())
	}

	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, fmt.Errorf("%w: 打开包失败: %v", ErrPackageNotFound, err)
	}
	defer reader.Close()
	if len(reader.File) > MaxEntryCount {
		return nil, fmt.Errorf("%w: 包内条目数 %d 超过上限 %d", ErrInvalidInput, len(reader.File), MaxEntryCount)
	}

	files := make(map[string]*zip.File, len(reader.File))
	seen := make(map[string]string, len(reader.File))
	var total uint64
	for _, file := range reader.File {
		if file.Flags&0x1 != 0 {
			return nil, fmt.Errorf("%w: 包内条目 %q 已加密（拒绝）", ErrInvalidInput, file.Name)
		}
		mode := file.Mode()
		if mode&os.ModeSymlink != 0 || mode&os.ModeDevice != 0 || mode&os.ModeNamedPipe != 0 ||
			mode&os.ModeSocket != 0 || mode&os.ModeCharDevice != 0 {
			return nil, fmt.Errorf("%w: 包内条目 %q 不是普通文件（%s）", ErrInvalidInput, file.Name, mode)
		}
		if file.FileInfo().IsDir() {
			return nil, fmt.Errorf("%w: 包内包含目录条目 %q（当前安装器只接受文件条目）", ErrInvalidInput, file.Name)
		}
		if err := checkZipEntryName(file.Name); err != nil {
			return nil, err
		}
		key := strings.ToLower(norm.NFC.String(file.Name))
		if previous, dup := seen[key]; dup {
			return nil, fmt.Errorf("%w: 包内条目 %q 与 %q 大小写折叠/NFC 归一化后同名", ErrInvalidInput, previous, file.Name)
		}
		seen[key] = file.Name

		if file.UncompressedSize64 > uint64(MaxEntryBytes) {
			return nil, fmt.Errorf("%w: 包内条目 %q 解出大小 %d 超过单文件上限 %d", ErrInvalidInput, file.Name, file.UncompressedSize64, MaxEntryBytes)
		}
		if file.CompressedSize64 > 0 && file.UncompressedSize64 > 1<<20 &&
			file.UncompressedSize64/file.CompressedSize64 > 100 {
			return nil, fmt.Errorf("%w: 包内条目 %q 压缩比超过 100 且解出超过 1 MiB", ErrInvalidInput, file.Name)
		}
		total += file.UncompressedSize64
		if total > uint64(MaxExtractBytes) {
			return nil, fmt.Errorf("%w: 包内声明解出总量超过上限 %d", ErrInvalidInput, MaxExtractBytes)
		}
		files[file.Name] = file
	}

	// plugin.yaml（当前宿主安装器的必需元数据）
	manifestData, ok := files[pluginManifestFile]
	if !ok {
		if _, hasINI := files[iniManifestFile]; hasINI {
			return nil, fmt.Errorf("%w: 包内只有 %s、没有 %s——标准包暂不可由当前安装器消费（待 ini.yaml 读取/1:1 落位适配）",
				ErrManifestMismatch, iniManifestFile, pluginManifestFile)
		}
		return nil, fmt.Errorf("%w: 包内缺 %s", ErrManifestMismatch, pluginManifestFile)
	}
	manifestBytes, err := readZipEntry(manifestData, MaxCatalogBytes)
	if err != nil {
		return nil, err
	}
	m, _, err := manifest.Parse(manifestBytes, manifest.Options{})
	if err != nil {
		return nil, fmt.Errorf("%w: %s 校验失败: %v", ErrManifestMismatch, pluginManifestFile, err)
	}

	meta := &PackageMeta{
		Name:          m.Name,
		Version:       m.Version,
		Kind:          m.Kind,
		HostAPI:       m.HostAPI,
		FrameworkReq:  m.FrameworkReq,
		Requires:      m.Requires,
		SignatureFile: defaultSignatureFile,
	}
	if strings.TrimSpace(m.Signature) != "" {
		meta.SignatureFile = path.Clean(strings.TrimSpace(m.Signature))
	}

	// ini.yaml（作者标准元数据）：存在则必须与 plugin.yaml 一致（interim 兼容层）
	if iniFile, hasINI := files[iniManifestFile]; hasINI {
		iniBytes, err := readZipEntry(iniFile, MaxCatalogBytes)
		if err != nil {
			return nil, err
		}
		ini, err := parseINIMeta(iniBytes)
		if err != nil {
			return nil, err
		}
		if ini.UID == "" || ini.Version == "" {
			return nil, fmt.Errorf("%w: %s 缺 uid/version", ErrManifestMismatch, iniManifestFile)
		}
		if ini.UID != m.Name {
			return nil, fmt.Errorf("%w: %s.uid=%q 与 %s.name=%q 不一致（当前兼容层要求二者相等）",
				ErrManifestMismatch, iniManifestFile, ini.UID, pluginManifestFile, m.Name)
		}
		if ini.Version != m.Version {
			return nil, fmt.Errorf("%w: %s.version=%q 与 %s.version=%q 不一致",
				ErrManifestMismatch, iniManifestFile, ini.Version, pluginManifestFile, m.Version)
		}
		if ini.Kind != "" && ini.Kind != m.Kind {
			return nil, fmt.Errorf("%w: %s.kind=%q 与 %s.kind=%q 不一致",
				ErrManifestMismatch, iniManifestFile, ini.Kind, pluginManifestFile, m.Kind)
		}
		if ini.HostAPI != "" && ini.HostAPI != m.HostAPI {
			return nil, fmt.Errorf("%w: %s.host_api=%q 与 %s.host_api=%q 不一致",
				ErrManifestMismatch, iniManifestFile, ini.HostAPI, pluginManifestFile, m.HostAPI)
		}
		if ini.FrameworkReq != "" && ini.FrameworkReq != m.FrameworkReq {
			return nil, fmt.Errorf("%w: %s.framework_req=%q 与 %s.framework_req=%q 不一致",
				ErrManifestMismatch, iniManifestFile, ini.FrameworkReq, pluginManifestFile, m.FrameworkReq)
		}
		iniRequires, err := ini.requires()
		if err != nil {
			return nil, err
		}
		if len(iniRequires) > 0 {
			_, iniMap, err := NormalizeRequires(m.Name, iniRequires)
			if err != nil {
				return nil, fmt.Errorf("%w: %s.requires: %v", ErrManifestMismatch, iniManifestFile, err)
			}
			_, manifestMap, err := NormalizeRequires(m.Name, splitRequires(m.Requires))
			if err != nil {
				return nil, fmt.Errorf("%w: %s.requires: %v", ErrManifestMismatch, pluginManifestFile, err)
			}
			if !sameRequires(iniMap, manifestMap) {
				return nil, fmt.Errorf("%w: %s.requires 与 %s.requires 语义不一致",
					ErrManifestMismatch, iniManifestFile, pluginManifestFile)
			}
		}
		meta.INIUID = ini.UID
	}

	// 清单条目 vs 包内元数据
	if entry.UID != meta.Name || entry.Version != meta.Version {
		return nil, fmt.Errorf("%w: 清单 %s@%s 与包内元数据 %s@%s 不一致",
			ErrManifestMismatch, entry.UID, entry.Version, meta.Name, meta.Version)
	}
	if entry.Kind != meta.Kind {
		return nil, fmt.Errorf("%w: 清单 kind=%q 与包内 kind=%q 不一致", ErrManifestMismatch, entry.Kind, meta.Kind)
	}
	if entry.HostAPI != meta.HostAPI {
		return nil, fmt.Errorf("%w: 清单 host_api=%q 与包内 host_api=%q 不一致", ErrManifestMismatch, entry.HostAPI, meta.HostAPI)
	}
	if entry.FrameworkReq != "" && entry.FrameworkReq != meta.FrameworkReq {
		return nil, fmt.Errorf("%w: 清单 framework_req=%q 与包内 %q 不一致", ErrManifestMismatch, entry.FrameworkReq, meta.FrameworkReq)
	}
	_, entryReq, err := NormalizeRequires(entry.UID, entry.Requires)
	if err != nil {
		return nil, err
	}
	_, manifestReq, err := NormalizeRequires(meta.Name, splitRequires(meta.Requires))
	if err != nil {
		return nil, fmt.Errorf("%w: %s.requires: %v", ErrManifestMismatch, pluginManifestFile, err)
	}
	if !sameRequires(entryReq, manifestReq) {
		return nil, fmt.Errorf("%w: 清单 requires 与包内 requires 语义不一致", ErrManifestMismatch)
	}

	// files.sha256：包源必备；逐项比对 + 清单外夹带拒绝
	packFile, ok := files[packManifestFile]
	if !ok {
		return nil, fmt.Errorf("%w: 包内缺 %s（包格式要求；用 export 生成）", ErrManifestMismatch, packManifestFile)
	}
	packBytes, err := readZipEntry(packFile, MaxCatalogBytes)
	if err != nil {
		return nil, err
	}
	listed, err := parsePackManifest(packBytes)
	if err != nil {
		return nil, err
	}
	listedPaths := make(map[string]struct{}, len(listed))
	for _, item := range listed {
		if _, dup := listedPaths[item.Path]; dup {
			return nil, fmt.Errorf("%w: %s 重复列出 %q", ErrManifestMismatch, packManifestFile, item.Path)
		}
		listedPaths[item.Path] = struct{}{}
		if item.Path == meta.SignatureFile {
			return nil, fmt.Errorf("%w: %s 不得列出签名文件 %q", ErrManifestMismatch, packManifestFile, meta.SignatureFile)
		}
		file, ok := files[item.Path]
		if !ok {
			return nil, fmt.Errorf("%w: %s 列出的 %q 不在包内（清单缺失项）", ErrManifestMismatch, packManifestFile, item.Path)
		}
		sum, err := hashZipEntry(file)
		if err != nil {
			return nil, err
		}
		if !strings.EqualFold(sum, item.SHA256) {
			return nil, fmt.Errorf("%w: %s 列出的 %q 摘要不符（清单 %s，实际 %s）",
				ErrManifestMismatch, packManifestFile, item.Path, item.SHA256, sum)
		}
	}
	for name := range files {
		if name == packManifestFile || name == meta.SignatureFile {
			continue
		}
		if _, ok := listedPaths[name]; !ok {
			return nil, fmt.Errorf("%w: 包内文件 %q 不在 %s 清单内（不得夹带）", ErrManifestMismatch, name, packManifestFile)
		}
	}
	meta.FilesVerified = true
	meta.PackFiles = len(listed)

	// 签名声明 vs 包内事实
	_, meta.SignaturePresent = files[meta.SignatureFile]
	if entry.Package.Signed == nil {
		return nil, fmt.Errorf("%w: 清单缺 package.signed", ErrInvalidInput)
	}
	if *entry.Package.Signed != meta.SignaturePresent {
		return nil, fmt.Errorf("%w: 清单 signed=%v，包内签名条目 %q 存在=%v",
			ErrPackageSignedMismatch, *entry.Package.Signed, meta.SignatureFile, meta.SignaturePresent)
	}
	return meta, nil
}

type iniMeta struct {
	UID          string `yaml:"uid"`
	Version      string `yaml:"version"`
	Kind         string `yaml:"kind"`
	HostAPI      string `yaml:"host_api"`
	FrameworkReq string `yaml:"framework_req"`
	Requires     any    `yaml:"requires"`
}

func parseINIMeta(data []byte) (*iniMeta, error) {
	var ini iniMeta
	if err := yaml.Unmarshal(data, &ini); err != nil {
		return nil, fmt.Errorf("%w: 解析 %s 失败: %v", ErrManifestMismatch, iniManifestFile, err)
	}
	return &ini, nil
}

// requires 把 ini.yaml 的 requires（字符串逗号分隔或字符串数组）转为 []string。
func (i *iniMeta) requires() ([]string, error) {
	switch value := i.Requires.(type) {
	case nil:
		return nil, nil
	case string:
		return splitRequires(value), nil
	case []any:
		items := make([]string, 0, len(value))
		for idx, raw := range value {
			text, ok := raw.(string)
			if !ok {
				return nil, fmt.Errorf("%w: %s.requires[%d] 不是字符串", ErrManifestMismatch, iniManifestFile, idx)
			}
			items = append(items, text)
		}
		return items, nil
	case []string:
		return value, nil
	default:
		return nil, fmt.Errorf("%w: %s.requires 不是字符串数组", ErrManifestMismatch, iniManifestFile)
	}
}

func splitRequires(raw string) []string {
	items := make([]string, 0, 4)
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}

func sameRequires(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for uid, domain := range a {
		if b[uid] != domain {
			return false
		}
	}
	return true
}

func sha256Hex(data []byte) (string, error) {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func hashReader(r io.Reader) (string, error) {
	hasher := sha256.New()
	if _, err := io.Copy(hasher, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func hashZipEntry(file *zip.File) (string, error) {
	rc, err := file.Open()
	if err != nil {
		return "", fmt.Errorf("%w: 打开包内 %q 失败: %v", ErrPackageNotFound, file.Name, err)
	}
	defer rc.Close()
	sum, err := hashReader(io.LimitReader(rc, MaxEntryBytes+1))
	if err != nil {
		return "", fmt.Errorf("%w: 读取包内 %q 失败: %v", ErrPackageNotFound, file.Name, err)
	}
	return sum, nil
}

func readZipEntry(file *zip.File, max int64) ([]byte, error) {
	rc, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("%w: 打开包内 %q 失败: %v", ErrPackageNotFound, file.Name, err)
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, max+1))
	if err != nil {
		return nil, fmt.Errorf("%w: 读取包内 %q 失败: %v", ErrPackageNotFound, file.Name, err)
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%w: 包内 %q 超过 %d 字节上限", ErrInvalidInput, file.Name, max)
	}
	return data, nil
}

func checkZipEntryName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: 包内含空条目名", ErrInvalidInput)
	}
	if strings.ContainsRune(name, 0) {
		return fmt.Errorf("%w: 包内条目名含 NUL: %q", ErrInvalidInput, name)
	}
	if !utf8.ValidString(name) {
		return fmt.Errorf("%w: 包内条目名不是合法 UTF-8: %q", ErrInvalidInput, name)
	}
	if strings.Contains(name, "\\") {
		return fmt.Errorf("%w: 包内条目名 %q 含反斜杠", ErrInvalidInput, name)
	}
	if len(name) >= 2 && name[1] == ':' {
		return fmt.Errorf("%w: 包内条目名 %q 带盘符前缀", ErrInvalidInput, name)
	}
	if path.IsAbs(name) {
		return fmt.Errorf("%w: 包内条目名 %q 是绝对路径", ErrInvalidInput, name)
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == "." || segment == ".." {
			return fmt.Errorf("%w: 包内条目名 %q 含 . / .. 段", ErrInvalidInput, name)
		}
	}
	if path.Clean(name) != name {
		return fmt.Errorf("%w: 包内条目名 %q 不是规范化相对路径", ErrInvalidInput, name)
	}
	return nil
}

type packFile struct {
	Path   string
	SHA256 string
}

func parsePackManifest(data []byte) ([]packFile, error) {
	var files []packFile
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		sum, rel, found := strings.Cut(line, "  ")
		if !found || !sha256Pattern.MatchString(strings.ToLower(sum)) {
			return nil, fmt.Errorf("%w: %s 第 %d 行不是 \"<sha256>  <相对路径>\" 形式", ErrManifestMismatch, packManifestFile, i+1)
		}
		rel = path.Clean(strings.TrimSpace(rel))
		if rel == "." || path.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../") {
			return nil, fmt.Errorf("%w: %s 第 %d 行的路径 %q 不是包内相对路径", ErrManifestMismatch, packManifestFile, i+1, rel)
		}
		files = append(files, packFile{Path: rel, SHA256: strings.ToLower(sum)})
	}
	return files, nil
}
