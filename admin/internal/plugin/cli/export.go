package cli

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"
)

// PackManifestFile 包内文件清单的固定文件名（签名对象 = 该文件的字节）
const PackManifestFile = "files.sha256"

// DefaultSignatureFile 默认签名文件名（manifest.signature 可覆盖；签名对象 = files.sha256）
const DefaultSignatureFile = "plugin.yaml.sig"

// PackFile 包内一个文件（相对路径 + 字节数 + sha256）
type PackFile struct {
	Path   string
	Bytes  int64
	SHA256 string
}

// CollectPackFiles 遍历插件目录收集文件清单（跳过 files.sha256 自身与签名文件；按路径字节序排序）。
// exclude 为额外排除的相对路径（如 manifest 声明的签名文件名）。
func CollectPackFiles(dir string, exclude ...string) ([]PackFile, error) {
	skipped := map[string]bool{PackManifestFile: true}
	for _, item := range exclude {
		if item = path.Clean(filepath.ToSlash(item)); item != "." {
			skipped[item] = true
		}
	}

	var files []PackFile
	err := filepath.WalkDir(dir, func(full string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, full)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if skipped[rel] {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			// 符号链接与设备文件不进包（zip 防护口径：只收普通文件）
			return fmt.Errorf("插件目录含非普通文件 %q（%s），拒绝打包", rel, info.Mode().String())
		}
		sum, err := fileSHA256(full)
		if err != nil {
			return err
		}
		files = append(files, PackFile{Path: rel, Bytes: info.Size(), SHA256: sum})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("遍历插件目录 %s 失败: %w", dir, err)
	}

	slices.SortFunc(files, func(a, b PackFile) int { return strings.Compare(a.Path, b.Path) })
	return files, nil
}

// RenderPackManifest 生成 files.sha256 内容（每行 "<sha256>  <相对路径>"，按路径字节序、LF 结尾、UTF-8；
// 自身不列入清单——）
func RenderPackManifest(files []PackFile) []byte {
	var b strings.Builder
	for _, file := range files {
		fmt.Fprintf(&b, "%s  %s\n", file.SHA256, file.Path)
	}
	return []byte(b.String())
}

// ParsePackManifest 解析 files.sha256（两空格分隔；忽略空行与 # 注释行）
func ParsePackManifest(data []byte) ([]PackFile, error) {
	var files []PackFile
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		sum, rel, found := strings.Cut(line, "  ")
		if !found || len(sum) != 64 || !isHex(sum) {
			return nil, fmt.Errorf("%s 第 %d 行不是 \"<sha256>  <相对路径>\" 形式: %q", PackManifestFile, i+1, line)
		}
		rel = path.Clean(filepath.ToSlash(strings.TrimSpace(rel)))
		if path.IsAbs(rel) || strings.HasPrefix(rel, "../") || rel == ".." {
			return nil, fmt.Errorf("%s 第 %d 行的路径 %q 不是包内相对路径", PackManifestFile, i+1, rel)
		}
		files = append(files, PackFile{Path: rel, SHA256: strings.ToLower(sum)})
	}
	return files, nil
}

// VerifyPackFiles逐项比对：清单缺失项（盘上没有）、比对不一致、以及
// 「清单之外还存在任何文件」三种情况一律拒装（否则多带一个文件会随目录复制进 plugins/<名>/ 并被编译）。
func VerifyPackFiles(dir string, listed []PackFile, exclude ...string) error {
	listedPaths := make(map[string]bool, len(listed))
	for _, item := range listed {
		listedPaths[item.Path] = true
		full := filepath.Join(dir, filepath.FromSlash(item.Path))
		sum, err := fileSHA256(full)
		if err != nil {
			return fmt.Errorf("%s 列出的文件 %q 不可读（清单缺失项）: %w", PackManifestFile, item.Path, err)
		}
		if !strings.EqualFold(sum, item.SHA256) {
			return fmt.Errorf("%s 列出的文件 %q 与包内实际不一致（清单 sha256 %s，实际 %s）", PackManifestFile, item.Path, item.SHA256, sum)
		}
	}

	actual, err := CollectPackFiles(dir, exclude...)
	if err != nil {
		return err
	}
	for _, file := range actual {
		if !listedPaths[file.Path] {
			return fmt.Errorf("文件 %q 不在 %s 清单内（清单之外不得夹带文件）", file.Path, PackManifestFile)
		}
	}
	return nil
}

// Export 把已装源码（唯一事实源）打包为 <名>-<版本>.zip（含 files.sha256），并**出包即自校验**
// （staging 解包 + 清单逐项比对 + 签名判定，见 selfCheckPackage）。
//   - 输出目录默认 plugins/.store/（安装包留档目录，.gitignore 已忽略）；
//   - **不代签**：签名（plugin.yaml.sig）由作者产出并随源码摆放，export 原样收进包；
//     校验用 `aigo plugin verify <包>`（ed25519 + 配置键 plugin.trusted_keys 的受信公钥，见 signature.go）；
//   - zip 的 archive comment 载 **files.sha256 的内容摘要**（自引用不可行：EOCD 的 comment 在文件尾部，
//     无法承载自身文件哈希），整包 sha256 由本命令打印，作为交接时的完整性提示（不作信任依据）。
func Export(env *Env, name, out string) (string, error) {
	dir, err := env.PluginSource(name)
	if err != nil {
		return "", err
	}
	sigFile := DefaultSignatureFile
	var m *manifest.Manifest
	if m, _, err = manifest.Load(dir, manifest.Options{Prefix: env.Prefix, HostVersion: env.HostVersion}); err != nil {
		return "", fmt.Errorf("校验 manifest 失败: %w", err)
	}
	if m.Signature != "" {
		sigFile = m.Signature
	}
	// 兜底（manifest 已核过同一约束）：把签名路径挡在 filepath.Join 之前——否则 `..` 段会把插件目录
	// 之外的文件收进包条目（「signature 是相对路径」）
	if err := ValidateSignatureFile(sigFile); err != nil {
		return "", err
	}

	files, err := CollectPackFiles(dir, sigFile)
	if err != nil {
		return "", err
	}
	packManifest := RenderPackManifest(files)
	manifestSum := sha256.Sum256(packManifest)

	target := out
	if target == "" {
		target = path.Join(StoreDir, fmt.Sprintf("%s-%s.zip", m.Name, m.Version))
	}
	if !filepath.IsAbs(target) {
		target = env.Abs(target)
	}
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		target = filepath.Join(target, fmt.Sprintf("%s-%s.zip", m.Name, m.Version))
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", fmt.Errorf("创建输出目录失败: %w", err)
	}

	// 打包：源码全部文件（含文件头归属标记的原始形态）+ 生成的 files.sha256
	if err := writeZip(dir, target, files, packManifest, sigFile, "aigo-plugin-export v1; files.sha256="+hex.EncodeToString(manifestSum[:])); err != nil {
		return "", err
	}
	pkgSum, err := fileSHA256(target)
	if err != nil {
		return "", err
	}

	env.Printf("已打包：%s\n", target)
	env.Printf("  文件数：%d（含 %s，不含签名自身）\n", len(files)+1, PackManifestFile)
	env.Printf("  files.sha256 摘要：%s\n", hex.EncodeToString(manifestSum[:]))
	env.Printf("  整包 sha256：%s\n", pkgSum)
	if _, sigErr := os.Stat(filepath.Join(dir, filepath.FromSlash(sigFile))); sigErr == nil {
		env.Printf("  签名：%s 已原样收进包（签名对象 = %s 的字节；校验用 `aigo plugin verify <包>`，本机需在配置键 %s 登记作者公钥）\n",
			sigFile, PackManifestFile, TrustedKeysConfigKey)
	} else {
		env.Printf("  签名：无（export 不代签；未签名包安装时须显式 --allow-unsigned）\n")
	}

	// 出包即自校验（清单缺失项 / 不一致 / 清单之外夹带一律拒——刚出的包先自己过一遍，不交给下游才发现）
	report, checkErr := selfCheckPackage(env, target, sigFile)
	if checkErr != nil {
		return "", fmt.Errorf("包已写出 %s，但自校验未通过（该包不可分发，请排查后重出）: %w", target, checkErr)
	}
	env.Printf("  ✓ 自校验：解包 %d 个条目 / %d 字节，%s 逐项比对通过（%d 项）\n",
		report.Entries, report.ExtractedBytes, PackManifestFile, report.Files)
	if report.Signature.Verified {
		env.Printf("  ✓ 自校验：签名有效（公钥指纹 %s）\n", report.Signature.Fingerprint)
	}
	return target, nil
}

// writeZip 写出插件包（entries = 清单文件；packManifest 作为独立条目追加；comment 写入 archive comment）
func writeZip(dir, target string, files []PackFile, packManifest []byte, sigFile, comment string) error {
	tmp := target + ".tmp"
	handle, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("创建包文件失败: %w", err)
	}
	writer := zip.NewWriter(handle)
	writer.SetComment(comment)

	appendFile := func(rel string, content []byte, info fs.FileInfo) error {
		header := &zip.FileHeader{Name: rel, Method: zip.Deflate}
		if info != nil {
			header.Modified = info.ModTime()
		}
		entry, err := writer.CreateHeader(header)
		if err != nil {
			return err
		}
		_, err = entry.Write(content)
		return err
	}

	for _, file := range files {
		full := filepath.Join(dir, filepath.FromSlash(file.Path))
		content, err := os.ReadFile(full)
		if err != nil {
			_ = writer.Close()
			_ = handle.Close()
			return fmt.Errorf("读取 %s 失败: %w", file.Path, err)
		}
		info, _ := os.Stat(full)
		if err := appendFile(file.Path, content, info); err != nil {
			_ = writer.Close()
			_ = handle.Close()
			return fmt.Errorf("写入包条目 %s 失败: %w", file.Path, err)
		}
	}

	// 签名文件若有则原样收进包（不进 files.sha256：签名对象正是该清单，列入即成环）
	if sigFile != "" {
		full := filepath.Join(dir, filepath.FromSlash(sigFile))
		if content, err := os.ReadFile(full); err == nil {
			info, _ := os.Stat(full)
			if err := appendFile(sigFile, content, info); err != nil {
				_ = writer.Close()
				_ = handle.Close()
				return fmt.Errorf("写入签名条目 %s 失败: %w", sigFile, err)
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			_ = writer.Close()
			_ = handle.Close()
			return fmt.Errorf("读取签名 %s 失败: %w", sigFile, err)
		}
	}

	if err := appendFile(PackManifestFile, packManifest, nil); err != nil {
		_ = writer.Close()
		_ = handle.Close()
		return fmt.Errorf("写入 %s 失败: %w", PackManifestFile, err)
	}

	if err := writer.Close(); err != nil {
		_ = handle.Close()
		return fmt.Errorf("关闭包写入器失败: %w", err)
	}
	if err := handle.Close(); err != nil {
		return fmt.Errorf("关闭包文件失败: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		return fmt.Errorf("落盘包文件失败: %w", err)
	}
	return nil
}

// fileSHA256 计算文件 sha256（十六进制小写）
func fileSHA256(path string) (string, error) {
	handle, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer handle.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, handle); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// isHex 判定字符串是否全为十六进制字符
func isHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}
