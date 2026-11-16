package source

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"strings"
	"time"
)

// CatalogDocument 一次取到的清单字节（含来源基准：HTTP 最终 URL 或本地目录）。
type CatalogDocument struct {
	Catalog  *Catalog
	Bytes    []byte
	SHA256   string
	FinalURL *url.URL
	BaseDir  string
}

// PackageSource 取到并通过大小/摘要校验的包文件（TempDir 非空时由调用方 Cleanup）。
type PackageSource struct {
	Path    string
	Size    int64
	SHA256  string
	TempDir string
}

// Cleanup 删除下载临时目录（本地包无操作）。
func (p *PackageSource) Cleanup() {
	if p != nil && p.TempDir != "" {
		_ = os.RemoveAll(p.TempDir)
		p.TempDir = ""
	}
}

// FetcherForSource 按来源的安全开关构造清单取数器（60s）。
func FetcherForSource(src *Source) *Fetcher {
	return newFetcherForSource(src, 60*time.Second)
}

// packageFetcherForSource 包体下载取数器（300s；与前端 install 的 per-request timeout 对齐）。
func packageFetcherForSource(src *Source) *Fetcher {
	return newFetcherForSource(src, 300*time.Second)
}

func newFetcherForSource(src *Source, timeout time.Duration) *Fetcher {
	policy := FetchPolicy{
		AllowInsecureHTTP: src != nil && src.Type == TypeHTTP && src.AllowInsecureHTTP == 1,
		AllowPrivateHosts: src != nil && src.Type == TypeHTTP && src.AllowPrivateHosts == 1,
		Timeout:           timeout,
	}
	return NewFetcher(policy)
}

// LoadCatalog 按来源读取并严格解析清单（不写库；source/test 与 catalog sync 共用）。
func LoadCatalog(ctx context.Context, src *Source, repoRoot string) (*CatalogDocument, error) {
	if src == nil {
		return nil, fmt.Errorf("%w: 来源为空", ErrInvalidInput)
	}
	switch src.Type {
	case TypeLocal:
		full, err := ResolveLocalFile(src.Location, repoRoot, src.AllowAbsolutePaths == 1)
		if err != nil {
			if errors.Is(err, ErrPackageNotFound) {
				return nil, fmt.Errorf("%w: %v", ErrCatalogNotFound, err)
			}
			return nil, err
		}
		data, err := ReadFileLimited(full, MaxCatalogBytes)
		if err != nil {
			if errors.Is(err, ErrPackageNotFound) {
				return nil, fmt.Errorf("%w: %v", ErrCatalogNotFound, err)
			}
			return nil, err
		}
		catalog, err := ParseCatalog(data)
		if err != nil {
			return nil, err
		}
		if err := catalog.ValidateForOrigin(false); err != nil {
			return nil, err
		}
		sum, err := sha256Hex(data)
		if err != nil {
			return nil, err
		}
		return &CatalogDocument{
			Catalog: catalog,
			Bytes:   data,
			SHA256:  sum,
			BaseDir: path.Dir(full),
		}, nil
	case TypeGit:
		return FetchGitCatalog(ctx, src)
	case TypeHTTP:
		u, err := ValidateHTTPURL(src.Location, src.AllowInsecureHTTP == 1)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidInput, err)
		}
		fetcher := FetcherForSource(src)
		result, err := fetcher.FetchBytes(ctx, u.String(), MaxCatalogBytes)
		if err != nil {
			return nil, err
		}
		catalog, err := ParseCatalog(result.Bytes)
		if err != nil {
			return nil, err
		}
		if err := catalog.ValidateForOrigin(true); err != nil {
			return nil, err
		}
		return &CatalogDocument{
			Catalog:  catalog,
			Bytes:    result.Bytes,
			SHA256:   result.SHA256,
			FinalURL: result.FinalURL,
		}, nil
	default:
		return nil, fmt.Errorf("%w: type %q 不在闭集内", ErrInvalidInput, src.Type)
	}
}

// ResolvePackage 按清单条目取包并校验大小/摘要：
//   - HTTP 清单：package.url 可以是绝对 URL 或相对清单最终 URL 的引用；
//   - 本地清单：package.url 只允许绝对 URL（本地没有“最终 URL”基准）；
//   - package.path 只允许本地清单，相对清单文件所在目录；绝对路径需来源开启 allow_absolute_paths。
func ResolvePackage(ctx context.Context, src *Source, doc *CatalogDocument, entry *PluginEntry) (*PackageSource, error) {
	if src == nil || doc == nil || entry == nil {
		return nil, fmt.Errorf("%w: 来源/清单/条目为空", ErrInvalidInput)
	}
	if strings.TrimSpace(entry.Package.URL) != "" {
		return resolveRemotePackage(ctx, src, doc, entry)
	}
	if src.Type == TypeGit {
		// git 来源的工作副本在临时目录里：取包前按 location 重新克隆一次，
		// 保证 BaseDir 可用且不与远端漂移（package.path 相对仓内清单目录解析，绝不接受仓外路径）。
		refreshed, err := FetchGitCatalog(ctx, src)
		if err != nil {
			return nil, err
		}
		doc = refreshed
	}
	return resolveLocalPackage(src, doc, entry)
}

// PackageFileName 下载临时 zip 的安全文件名：来源标签（catalog:<来源>:<uid>@<版本>）含冒号，
// 不能直接做文件名（Windows/某些工具会截断）。
// **唯一实现处**：远程取包落临时文件（resolveRemotePackage）与展示/日志都必须调它，
// 不要在别处再写一份格式串（两份会漂移，出错时对不上账）。
func PackageFileName(srcName, uid, version string) string {
	return fmt.Sprintf("catalog-%s-%s@%s.zip", srcName, uid, version)
}

func resolveRemotePackage(ctx context.Context, src *Source, doc *CatalogDocument, entry *PluginEntry) (*PackageSource, error) {
	ref, err := url.Parse(strings.TrimSpace(entry.Package.URL))
	if err != nil {
		return nil, fmt.Errorf("%w: package.url 无法解析: %v", ErrInvalidInput, err)
	}
	var target *url.URL
	if doc.FinalURL != nil {
		target = doc.FinalURL.ResolveReference(ref)
	} else {
		if !ref.IsAbs() {
			return nil, fmt.Errorf("%w: 本地清单的 package.url 必须是绝对 URL（本地没有 URL 基准）", ErrInvalidInput)
		}
		target = ref
	}
	checked, err := ValidateHTTPURL(target.String(), src.AllowInsecureHTTP == 1)
	if err != nil {
		return nil, fmt.Errorf("%w: package.url: %v", ErrInvalidInput, err)
	}

	tmpDir, err := os.MkdirTemp("", "aigo-catalog-pkg-")
	if err != nil {
		return nil, fmt.Errorf("%w: 创建包临时目录失败: %v", ErrUpstreamError, err)
	}
	label := PackageFileName(src.Name, entry.UID, entry.Version)
	zipPath := path.Join(tmpDir, label)
	file, err := os.OpenFile(zipPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("%w: 创建包临时文件失败: %v", ErrUpstreamError, err)
	}
	fetcher := packageFetcherForSource(src)
	result, downloadErr := fetcher.Download(ctx, checked.String(), MaxPackageBytes, file)
	closeErr := file.Close()
	if downloadErr != nil {
		_ = os.RemoveAll(tmpDir)
		return nil, downloadErr
	}
	if closeErr != nil {
		_ = os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("%w: 写入包临时文件失败: %v", ErrUpstreamError, closeErr)
	}
	if err := verifyPackageIntegrity(result.Bytes, result.SHA256, entry); err != nil {
		_ = os.RemoveAll(tmpDir)
		return nil, err
	}
	return &PackageSource{Path: zipPath, Size: result.Bytes, SHA256: result.SHA256, TempDir: tmpDir}, nil
}

func resolveLocalPackage(src *Source, doc *CatalogDocument, entry *PluginEntry) (*PackageSource, error) {
	if doc.FinalURL != nil {
		return nil, fmt.Errorf("%w: 远程清单不得使用 package.path（只允许 package.url）", ErrInvalidInput)
	}
	if doc.BaseDir == "" {
		return nil, fmt.Errorf("%w: 本地清单缺少基准目录", ErrInvalidInput)
	}
	full, err := ResolveLocalFile(entry.Package.Path, doc.BaseDir, src.AllowAbsolutePaths == 1)
	if err != nil {
		return nil, err
	}
	size, sum, err := FileSizeAndSHA256(full)
	if err != nil {
		return nil, err
	}
	if err := verifyPackageIntegrity(size, sum, entry); err != nil {
		return nil, err
	}
	return &PackageSource{Path: full, Size: size, SHA256: sum}, nil
}

func verifyPackageIntegrity(size int64, sum string, entry *PluginEntry) error {
	if size > MaxPackageBytes {
		return fmt.Errorf("%w: 包体 %d 字节超过上限 %d", ErrPackageSizeMismatch, size, MaxPackageBytes)
	}
	if size != entry.Package.Size {
		return fmt.Errorf("%w: 清单声明 %d 字节，实际 %d 字节", ErrPackageSizeMismatch, entry.Package.Size, size)
	}
	if !strings.EqualFold(sum, entry.Package.SHA256) {
		return fmt.Errorf("%w: 清单声明 %s，实际 %s", ErrPackageSHAMismatch, entry.Package.SHA256, sum)
	}
	return nil
}

// TestResult 来源只读测试的结果（不写库）。
type TestResult struct {
	OK            bool     `json:"ok"`
	Schema        string   `json:"schema"`
	GeneratedAt   string   `json:"generated_at"`
	Entries       int      `json:"entries"`
	CatalogSHA256 string   `json:"catalog_sha256"`
	Errors        []string `json:"errors,omitempty"`
}

// TestSource 只读测试：读取并严格解析清单，返回 schema/条目数/摘要/错误；不联网写库之外无副作用。
func TestSource(ctx context.Context, src *Source, repoRoot string) *TestResult {
	result := &TestResult{}
	if err := src.ValidateLocation(); err != nil {
		result.Errors = []string{sanitizeText(err.Error(), repoRoot, src.Location)}
		return result
	}
	doc, err := LoadCatalog(ctx, src, repoRoot)
	if err != nil {
		result.Errors = []string{sanitizeText(err.Error(), repoRoot, src.Location)}
		return result
	}
	result.OK = true
	result.Schema = doc.Catalog.Schema
	result.GeneratedAt = doc.Catalog.GeneratedAt
	result.Entries = len(doc.Catalog.Plugins)
	result.CatalogSHA256 = doc.SHA256
	return result
}

// SyncResult 单个来源的同步结果（catalog sync 的逐来源回执）。
type SyncResult struct {
	SourceID      uint   `json:"source_id"`
	Name          string `json:"name"`
	Status        string `json:"status"`
	CatalogSHA256 string `json:"catalog_sha256"`
	Entries       int    `json:"entries"`
	GeneratedAt   string `json:"generated_at"`
	Error         string `json:"error,omitempty"`
}

// SyncSource 同步一个来源：读清单 → 严格解析 → 替换快照（dryRun 只读解析）。
// 失败时记录 last_sync_error，旧快照保留。
func SyncSource(ctx context.Context, store Store, src *Source, repoRoot string, dryRun bool) SyncResult {
	if src == nil {
		return SyncResult{Status: "error", Error: "来源为空"}
	}
	result := SyncResult{SourceID: src.ID, Name: src.Name}
	if !dryRun && store == nil {
		result.Status = "error"
		result.Error = "来源存储不可用"
		return result
	}
	if src.Enabled != 1 {
		result.Status = "error"
		result.Error = "来源已停用"
		return result
	}
	doc, err := LoadCatalog(ctx, src, repoRoot)
	if err != nil {
		result.Status = "error"
		result.Error = sanitizeText(err.Error(), repoRoot, src.Location)
		if store != nil && !dryRun {
			_ = store.MarkSyncFailure(ctx, src.ID, result.Error)
		}
		return result
	}
	result.CatalogSHA256 = doc.SHA256
	result.Entries = len(doc.Catalog.Plugins)
	result.GeneratedAt = doc.Catalog.GeneratedAt
	if dryRun {
		result.Status = "ok"
		return result
	}
	if err := store.ReplaceCatalog(ctx, src, doc.Catalog, doc.SHA256, time.Now()); err != nil {
		result.Status = "error"
		result.Error = sanitizeText(err.Error(), repoRoot, src.Location)
		_ = store.MarkSyncFailure(ctx, src.ID, result.Error)
		return result
	}
	result.Status = "ok"
	return result
}

// SanitizePaths 把仓库根/临时目录/额外绝对路径替换为占位符（不截断；管理面回执与报告用）。
func SanitizePaths(text, repoRoot string, extra ...string) string {
	if repoRoot != "" {
		text = strings.ReplaceAll(text, repoRoot, "<repo>")
	}
	for _, item := range extra {
		if item != "" && filepathIsAbs(item) {
			text = strings.ReplaceAll(text, item, "<source>")
		}
	}
	return strings.ReplaceAll(text, os.TempDir(), "<tmp>")
}

// SanitizeMessage 供入库字段（last_sync_error）使用：先脱敏再按 rune 边界截断。
func SanitizeMessage(text, repoRoot string, extra ...string) string {
	return TruncateSyncError(SanitizePaths(text, repoRoot, extra...))
}

// sanitizeText 包内同步/测试路径的统一入口（入库语义，带截断）。
func sanitizeText(text, repoRoot string, extra ...string) string {
	return SanitizeMessage(text, repoRoot, extra...)
}
