package source

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ResolveLocalFile 把来源 location / package.path 解析为可读的普通文件路径。
//
// 安全口径（与安装链的 ensureNoSymlink 同向）：
//   - 相对路径必须留在 base 允许根内（拒绝 `..`、盘符与反斜杠）；
//   - 解析后对**每一级父目录** Lstat，符号链接一律拒绝；
//   - 最终目标本身不得是符号链接，且必须是普通文件；
//   - 绝对路径只在 allowAbsolute=true（来源显式开启 allow_absolute_paths）时接受；
//     绝对路径是显式高危开关，仅要求最终目标不是符号链接（系统祖先目录可能是符号链接，
//     例如部分平台的临时目录），不做逐级父目录拒绝。
func ResolveLocalFile(raw, base string, allowAbsolute bool) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("%w: 路径为空", ErrPathEscape)
	}
	if strings.ContainsRune(raw, 0) {
		return "", fmt.Errorf("%w: 路径含 NUL", ErrPathEscape)
	}
	if strings.Contains(raw, "\\") {
		return "", fmt.Errorf("%w: 路径含反斜杠（分隔符统一用 /）", ErrPathEscape)
	}

	if filepathIsAbs(raw) {
		if !allowAbsolute {
			return "", fmt.Errorf("%w: 绝对路径未开启 allow_absolute_paths", ErrPathEscape)
		}
		full := filepath.Clean(filepath.FromSlash(raw))
		info, err := os.Lstat(full)
		if err != nil {
			return "", fmt.Errorf("%w: 读取 %s 失败: %v", ErrPackageNotFound, full, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%w: 目标 %s 是符号链接", ErrPathEscape, full)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("%w: 目标 %s 不是普通文件（%s）", ErrPathEscape, full, info.Mode())
		}
		resolved, err := filepath.EvalSymlinks(full)
		if err != nil {
			return "", fmt.Errorf("%w: 解析 %s 失败: %v", ErrPathEscape, full, err)
		}
		return resolved, nil
	}

	clean := path.Clean(raw)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%w: 相对路径 %q 不是规范化路径", ErrPathEscape, raw)
	}
	// 允许 "./x" 这类显式相对写法（path.Clean 已折叠）；折叠后的任何 "." / ".." 段都拒绝。
	for _, seg := range strings.Split(clean, "/") {
		if seg == "." || seg == ".." {
			return "", fmt.Errorf("%w: 相对路径 %q 含 . / .. 段", ErrPathEscape, raw)
		}
	}

	root, err := filepath.Abs(base)
	if err != nil {
		return "", fmt.Errorf("%w: 解析允许根失败: %v", ErrPathEscape, err)
	}
	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("%w: 允许根 %s 不可用: %v", ErrPathEscape, root, err)
	}
	full := filepath.Join(rootResolved, filepath.FromSlash(clean))
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("%w: 文件 %s 不存在", ErrPackageNotFound, clean)
		}
		return "", fmt.Errorf("%w: 解析 %s 失败: %v", ErrPathEscape, clean, err)
	}
	rel, err := filepath.Rel(rootResolved, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: 路径 %q 逃出允许根", ErrPathEscape, raw)
	}

	// 逐级父目录不得是符号链接（目标用 Lstat 单独判定）
	segments := strings.Split(rel, string(os.PathSeparator))
	cur := rootResolved
	for i := 0; i < len(segments)-1; i++ {
		cur = filepath.Join(cur, segments[i])
		info, err := os.Lstat(cur)
		if err != nil {
			return "", fmt.Errorf("%w: 检查父目录 %s 失败: %v", ErrPathEscape, cur, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%w: 父目录 %s 是符号链接", ErrPathEscape, cur)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("%w: 父路径 %s 不是目录", ErrPathEscape, cur)
		}
	}
	info, err := os.Lstat(full)
	if err != nil {
		return "", fmt.Errorf("%w: 检查目标 %s 失败: %v", ErrPathEscape, clean, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%w: 目标 %s 是符号链接", ErrPathEscape, clean)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: 目标 %s 不是普通文件（%s）", ErrPathEscape, clean, info.Mode())
	}
	return resolved, nil
}

// ReadFileLimited 读取不超过 max 字节的普通文件（先按上限截断读取，不先读全再判）。
func ReadFileLimited(filename string, max int64) ([]byte, error) {
	info, err := os.Lstat(filename)
	if err != nil {
		return nil, fmt.Errorf("%w: 读取 %s 失败: %v", ErrPackageNotFound, filename, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: 文件 %s 是符号链接", ErrPathEscape, filename)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: 文件 %s 不是普通文件", ErrPathEscape, filename)
	}
	f, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("%w: 打开 %s 失败: %v", ErrPackageNotFound, filename, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, fmt.Errorf("%w: 读取 %s 失败: %v", ErrPackageNotFound, filename, err)
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%w: 文件超过 %d 字节上限", ErrInvalidInput, max)
	}
	return data, nil
}

// FileSizeAndSHA256 计算普通文件字节数与 sha256（先 Lstat 拒绝符号链接/非普通文件）。
func FileSizeAndSHA256(filename string) (int64, string, error) {
	info, err := os.Lstat(filename)
	if err != nil {
		return 0, "", fmt.Errorf("%w: 读取 %s 失败: %v", ErrPackageNotFound, filename, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return 0, "", fmt.Errorf("%w: 文件 %s 是符号链接", ErrPathEscape, filename)
	}
	if !info.Mode().IsRegular() {
		return 0, "", fmt.Errorf("%w: 文件 %s 不是普通文件", ErrPathEscape, filename)
	}
	if info.Size() > MaxPackageBytes {
		return 0, "", fmt.Errorf("%w: 文件 %s 大小 %d 超过上限 %d", ErrPackageSizeMismatch, filename, info.Size(), MaxPackageBytes)
	}
	f, err := os.Open(filename)
	if err != nil {
		return 0, "", fmt.Errorf("%w: 打开 %s 失败: %v", ErrPackageNotFound, filename, err)
	}
	defer f.Close()
	sum, err := hashReader(f)
	if err != nil {
		return 0, "", fmt.Errorf("%w: 计算 %s 摘要失败: %v", ErrPackageNotFound, filename, err)
	}
	return info.Size(), sum, nil
}
