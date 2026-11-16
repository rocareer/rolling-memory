package cli

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// 包与解包防护上限（「zip 防护」逐条）。头部声明值只作预检，实际大小一律以**解出字节数**计量：
// 声明值可以被伪造，计数不会。
const (
	// MaxPackageBytes 整包上限（对 zip 文件本体 stat）
	MaxPackageBytes int64 = 20 << 20
	// MaxExtractBytes 解包总量上限（逐块累计，超限即中止并清理）
	MaxExtractBytes int64 = 100 << 20
	// MaxEntryBytes 单文件上限
	MaxEntryBytes int64 = 10 << 20
	// MaxEntryCount 条目数上限
	MaxEntryCount = 2000
	// zipFlagEncrypted zip 通用目的标志位 0x1（加密条目）：一律拒（本项目不处理加密包，
	// 且加密条目在 Go 的 zip 读取层不做判定，会以 CRC 不符的姿态晚失败 ⇒ 在强制点直接拒）
	zipFlagEncrypted = 0x1
	// MaxCompressionRatio 单条目解出/压缩比上限；与 ratioCheckFloorBytes 同时命中才拒
	MaxCompressionRatio int64 = 100
	// ratioCheckFloorBytes 压缩比检查的起算阈值：解出量不超过它时比值噪声大（小文件天然高比值），不拒
	ratioCheckFloorBytes int64 = 1 << 20
)

// Staged 一次包源的解包结果（临时目录；用完必须 Cleanup）
type Staged struct {
	// Dir 解包出的插件目录（含 plugin.yaml，可直接当目录源用）
	Dir string
	// Report 解包读数（dry-run 与审计的字节计量口径）
	Report  StageReport
	cleanup func()
}

// Cleanup 删除解包临时目录（幂等）
func (s *Staged) Cleanup() {
	if s != nil && s.cleanup != nil {
		s.cleanup()
		s.cleanup = nil
	}
}

// StageReport 解包读数（大小以实际解出字节数计量）
type StageReport struct {
	// Zip 包文件（本机路径）
	Zip string
	// Bytes 整包字节数（stat）
	Bytes int64
	// Checksum 整包 sha256（落台账 checksum 列；来源认证由签名承载）
	Checksum string
	// Entries 实际解出的条目数
	Entries int
	// ExtractedBytes 实际解出的总字节数
	ExtractedBytes int64
}

// StageZip 把插件包解包到临时目录（zip 防护逐条）：
//   - 拒绝绝对路径与 `..`；拒绝反斜杠分隔与盘符前缀（路径穿越的 Windows 形态）；
//   - 文件名必须 UTF-8 且不含 NUL；重复条目判重含**大小写折叠 + NFC 归一化**
//     （APFS/NTFS 大小写不敏感 ⇒ 否则「校验看一条、落盘被另一条覆盖」）；
//   - 拒绝符号链接与设备/管道/套接字（按条目 Mode 判定，非普通文件一律拒）；
//   - 拒绝**加密条目**（通用标志位 0x1）与**压缩炸弹形态**（解出 >1 MiB 且解出/压缩 >100 倍）；
//     这两道是安装链的强制点：catalog 层的包预检（internal/plugin/source.InspectPackage）只是提前拦截，
//     任何走 `aigo plugin install <zip>` 的路径都必须在这里被拦住（第 3 项）；
//   - 大小以实际解出字节计量（逐块计数），整包/单文件/总量/条目数四道上限；
//   - 解包到临时目录（不进工作树）→ 校验（files.sha256，由调用方执行）→ 才落位。
//
// 落位目标的**每一级目录不得是既有符号链接**由落位侧逐级检查（deploy.go）；
// 解包目标是刚建的临时目录，其祖先（os.TempDir 之下）不属攻击者可控面，故不逐级 Lstat。
func StageZip(zipPath string) (*Staged, error) {
	info, err := os.Stat(zipPath)
	if err != nil {
		return nil, fmt.Errorf("读取插件包 %s 失败: %w", zipPath, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("插件包 %s 不是普通文件（%s）：拒装", zipPath, info.Mode().String())
	}
	if info.Size() > MaxPackageBytes {
		return nil, fmt.Errorf("插件包 %s 大小 %d 字节超过整包上限 %d", zipPath, info.Size(), MaxPackageBytes)
	}
	checksum, err := fileSHA256(zipPath)
	if err != nil {
		return nil, err
	}

	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, fmt.Errorf("打开插件包 %s 失败: %w", zipPath, err)
	}
	defer reader.Close()

	if len(reader.File) > MaxEntryCount {
		return nil, fmt.Errorf("插件包条目数 %d 超过上限 %d", len(reader.File), MaxEntryCount)
	}

	dir, err := os.MkdirTemp("", "aigo-plugin-stage-")
	if err != nil {
		return nil, fmt.Errorf("创建解包临时目录失败: %w", err)
	}
	staged := &Staged{Dir: dir, cleanup: func() { _ = os.RemoveAll(dir) }}

	report := StageReport{Zip: zipPath, Bytes: info.Size(), Checksum: checksum}
	seen := make(map[string]string, len(reader.File)) // 折叠键 → 首次出现的原始名（报错用）
	for _, entry := range reader.File {
		if err := checkEntryName(entry.Name); err != nil {
			staged.Cleanup()
			return nil, err
		}
		key := strings.ToLower(norm.NFC.String(entry.Name))
		if previous, dup := seen[key]; dup {
			staged.Cleanup()
			return nil, fmt.Errorf("插件包含判重冲突的条目 %q 与 %q（大小写折叠 / NFC 归一化后同名）", previous, entry.Name)
		}
		seen[key] = entry.Name

		if entry.Flags&zipFlagEncrypted != 0 {
			staged.Cleanup()
			return nil, fmt.Errorf("插件包条目 %q 是加密条目（zip 通用标志位 0x1）：一律拒装（不处理加密包）", entry.Name)
		}

		mode := entry.Mode()
		if mode&os.ModeSymlink != 0 || mode&os.ModeDevice != 0 || mode&os.ModeNamedPipe != 0 ||
			mode&os.ModeSocket != 0 || mode&os.ModeCharDevice != 0 {
			staged.Cleanup()
			return nil, fmt.Errorf("插件包条目 %q 不是普通文件（%s）：符号链接与设备文件一律拒", entry.Name, mode.String())
		}

		target := filepath.Join(dir, filepath.FromSlash(entry.Name))
		if !strings.HasPrefix(target, dir+string(os.PathSeparator)) {
			staged.Cleanup()
			return nil, fmt.Errorf("插件包条目 %q 解出路径越出解包目录", entry.Name)
		}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				staged.Cleanup()
				return nil, fmt.Errorf("创建目录 %s 失败: %w", target, err)
			}
			continue
		}

		written, err := extractEntry(entry, target)
		if err != nil {
			staged.Cleanup()
			return nil, err
		}
		report.Entries++
		report.ExtractedBytes += written
		if report.ExtractedBytes > MaxExtractBytes {
			staged.Cleanup()
			return nil, fmt.Errorf("插件包解出总量超过上限 %d 字节（已解出 %d）", MaxExtractBytes, report.ExtractedBytes)
		}
	}

	staged.Report = report
	return staged, nil
}

// checkEntryName 条目名防护：非空、UTF-8、无 NUL、非绝对路径、无 `.`/`..` 段、
// 无反斜杠与盘符前缀、不做 `.` 归一化放行（要求条目名已是 path.Clean 形态）。
//
// 注意必须先做**段级** `.`/`..` 检查：path.Clean 不折叠**开头**的 `..`
// （Clean("../evil.txt") == "../evil.txt"），只靠 Clean 比对会漏掉「上级穿越」形态。
func checkEntryName(name string) error {
	if name == "" {
		return errors.New("插件包含空条目名")
	}
	if strings.ContainsRune(name, 0) {
		return fmt.Errorf("插件包条目名含 NUL: %q", name)
	}
	if !utf8.ValidString(name) {
		return fmt.Errorf("插件包条目名不是合法 UTF-8: %q", name)
	}
	if strings.Contains(name, "\\") {
		return fmt.Errorf("插件包条目名 %q 含反斜杠（zip 分隔符应为 /；拒装防路径穿越的 Windows 形态）", name)
	}
	if len(name) >= 2 && name[1] == ':' {
		return fmt.Errorf("插件包条目名 %q 带盘符前缀（拒装）", name)
	}
	if path.IsAbs(name) {
		return fmt.Errorf("插件包条目名 %q 是绝对路径（拒装）", name)
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == "." || segment == ".." {
			return fmt.Errorf("插件包条目名 %q 含 %q 段：不是规范化相对路径（绝对路径与向上穿越一律拒装）", name, segment)
		}
	}
	if path.Clean(name) != name {
		return fmt.Errorf("插件包条目名 %q 不是规范化相对路径（拒装：`.`/`..` 段一律不接受）", name)
	}
	return nil
}

// extractEntry 解出单个条目（逐块计数；超单文件上限即中止）
func extractEntry(entry *zip.File, target string) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return 0, fmt.Errorf("创建目录 %s 失败: %w", filepath.Dir(target), err)
	}
	src, err := entry.Open()
	if err != nil {
		return 0, fmt.Errorf("打开包内 %s 失败: %w", entry.Name, err)
	}
	defer src.Close()

	dst, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, fmt.Errorf("写入 %s 失败: %w", target, err)
	}
	written, copyErr := io.Copy(dst, io.LimitReader(src, MaxEntryBytes+1))
	closeErr := dst.Close()
	if copyErr != nil {
		return written, fmt.Errorf("解出 %s 失败: %w", entry.Name, copyErr)
	}
	if closeErr != nil {
		return written, fmt.Errorf("关闭 %s 失败: %w", target, closeErr)
	}
	if written > MaxEntryBytes {
		return written, fmt.Errorf("包内 %s 解出字节数超过单文件上限 %d", entry.Name, MaxEntryBytes)
	}
	// 压缩炸弹形态：解出量超过 1 MiB 且解出/压缩 >100 倍 ⇒ 拒（第 3 项）。
	// 用**实际解出字节**而非头部声明值；CompressedSize64 取 zip 中央目录的记录值。
	// 注意本检查在解出之后（解出量只能实测），临时目录由调用方 Cleanup 兜底清理。
	if written > ratioCheckFloorBytes && written > int64(entry.CompressedSize64)*MaxCompressionRatio {
		return written, fmt.Errorf("包内 %s 解出/压缩比异常（解出 %d 字节 / 压缩 %d 字节 > %d 倍且解出超过 %d 字节）：拒装（压缩炸弹防护）",
			entry.Name, written, entry.CompressedSize64, MaxCompressionRatio, ratioCheckFloorBytes)
	}
	return written, nil
}
