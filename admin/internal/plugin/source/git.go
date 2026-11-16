package source

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// git 来源：location 语法 <仓库URL>[#<ref>][!<仓内清单路径>]
//   - 仓库 URL：http(s) URL（明文 http 需 allow_insecure_http；回环/私网需 allow_private_hosts）；
//   - #ref：分支或标签名（浅克隆 --depth 1），缺省用远端默认分支；
//   - !path：仓内清单路径，缺省 catalog.yaml；必须落在仓库内（禁止 .. 与绝对路径）；
//   - 凭据型仓库（userinfo / ssh / token）不在 v1 范围——与 http 来源同口径，只支持公开仓库。
const (
	// DefaultGitCatalogPath 缺省仓内清单路径（仓库根下）。
	DefaultGitCatalogPath = "catalog.yaml"
	// maxGitRefLen ref 长度上限。
	maxGitRefLen = 200
	// gitCloneTimeout 单次克隆超时（浅克隆单分支；超时即失败，不留未完成件）。
	gitCloneTimeout = 180 * time.Second
	// gitCacheRoot 工作副本缓存根（每次取清单/取包重新克隆，避免陈旧副本）。
	gitCacheRoot = "aigo-plugin-git-src"
)

// GitLocation 解析后的 git 来源定位。
type GitLocation struct {
	// URL 仓库地址（http/https，无 userinfo/query/fragment）。
	URL string
	// Ref 分支或标签名；空 = 远端默认分支。
	Ref string
	// CatalogPath 仓内清单路径（/ 分隔的相对路径）。
	CatalogPath string
}

// ParseGitLocation 解析 location 语法；只做形状校验，URL 的 scheme/明文/私网判定在
// ValidateGitLocation 与 FetchGitCatalog 里按来源开关执行。
func ParseGitLocation(raw string) (*GitLocation, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil, errors.New("git 来源必须给 location（<仓库URL>[#<ref>][!<仓内清单路径>]）")
	}
	if len(value) > MaxLocationBytes {
		return nil, fmt.Errorf("location 超过 %d 字符", MaxLocationBytes)
	}
	loc := &GitLocation{CatalogPath: DefaultGitCatalogPath}

	// !path：仓内清单路径（最后一个 ! 之后）
	if idx := strings.Index(value, "!"); idx >= 0 {
		pathPart := strings.TrimSpace(value[idx+1:])
		value = strings.TrimSpace(value[:idx])
		if pathPart == "" {
			return nil, errors.New("! 后缺仓内清单路径")
		}
		if strings.Contains(pathPart, "\\") {
			return nil, errors.New("仓内清单路径含反斜杠（分隔符统一用 /）")
		}
		if strings.HasPrefix(pathPart, "/") {
			return nil, errors.New("仓内清单路径必须是仓内相对路径")
		}
		clean := path.Clean(pathPart)
		if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
			return nil, fmt.Errorf("仓内清单路径 %q 不是允许根内的规范化相对路径", pathPart)
		}
		loc.CatalogPath = clean
	}

	// #ref：分支/标签（第一个 # 之后）
	if idx := strings.Index(value, "#"); idx >= 0 {
		ref := strings.TrimSpace(value[idx+1:])
		value = strings.TrimSpace(value[:idx])
		if ref == "" {
			return nil, errors.New("# 后缺 ref（分支或标签名）")
		}
		if len(ref) > maxGitRefLen {
			return nil, fmt.Errorf("ref 超过 %d 字符", maxGitRefLen)
		}
		for _, r := range ref {
			ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
				r == '.' || r == '_' || r == '-' || r == '/'
			if !ok {
				return nil, fmt.Errorf("ref 含不允许的字符 %q（只允许字母数字与 . _ - /）", string(r))
			}
		}
		if strings.Contains(ref, "..") || strings.HasPrefix(ref, "-") {
			return nil, fmt.Errorf("ref %q 不合法", ref)
		}
		loc.Ref = ref
	}

	if value == "" {
		return nil, errors.New("git 来源缺仓库 URL")
	}
	loc.URL = value
	return loc, nil
}

// ValidateGitLocation 形状 + scheme/明文校验（不做 DNS 解析；返回解析结果供调用方复用）。
func ValidateGitLocation(raw string, allowInsecure bool) (*GitLocation, error) {
	loc, err := ParseGitLocation(raw)
	if err != nil {
		return nil, err
	}
	if _, err := ValidateHTTPURL(loc.URL, allowInsecure); err != nil {
		return nil, err
	}
	return loc, nil
}

// gitCheckoutDir 单来源的固定工作副本目录（同名来源并发取数时以最后一次克隆为准）。
func gitCheckoutDir(name string) string {
	label := strings.TrimSpace(name)
	if label == "" {
		label = "default"
	}
	return filepath.Join(os.TempDir(), gitCacheRoot, label)
}

// gitEnv 克隆用的环境：禁止一切交互式凭据提示（公开仓库不需要凭据），不读系统 config，
// 跳过 LFS 取数（包体不进 LFS）。
func gitEnv() []string {
	return append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/bin/true",
		"SSH_ASKPASS=/bin/true",
		"GCM_INTERACTIVE=never",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_LFS_SKIP_SMUDGE=1",
	)
}

// gitClone 浅克隆仓库到 dir（dir 必须不存在或为空目录）。rawURL 走形状校验后的地址；
// 单元测试用本地仓库路径直调本函数（不经过 http(s) 校验）。
// 不留未完成件：任何失败都清空 dir。
func gitClone(ctx context.Context, rawURL, ref, dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("清理旧工作副本失败: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return fmt.Errorf("创建工作副本父目录失败: %w", err)
	}
	args := []string{
		"-c", "core.hooksPath=/dev/null",
		"-c", "advice.detachedHead=false",
		"clone", "--depth", "1", "--single-branch", "--no-tags", "--no-recurse-submodules", "--quiet",
	}
	if strings.TrimSpace(ref) != "" {
		args = append(args, "--branch", strings.TrimSpace(ref))
	}
	args = append(args, rawURL, dir)

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		_ = os.RemoveAll(dir)
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("克隆超时（%s）：%s", gitCloneTimeout, trimGitOutput(msg))
		}
		return fmt.Errorf("克隆失败: %s", trimGitOutput(msg))
	}
	return nil
}

// trimGitOutput 截断 git 输出（进审计/错误前先收敛长度，避免把整段 stderr 带进库）。
func trimGitOutput(msg string) string {
	if len(msg) > 500 {
		return msg[:500] + "…"
	}
	return msg
}

// checkGitHost 主机防线：URL 主机是 IP 字面量时直接判定；否则解析全部 A/AAAA 记录，
// 任一命中回环/私网/链路本地即拒绝（除非 allowPrivate）。
// 说明：git 客户端自行解析域名，本检查与真正连接之间存在 TOCTOU 窗口；v1 先按
// 「解析期拦截 + 显式开关」处理，与 http 来源的拨号期拦截相比更弱，见 CATALOG-STANDARD 的安全节。
func checkGitHost(rawURL string, allowPrivate bool) error {
	if allowPrivate {
		return nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("URL 无法解析: %w", err)
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("URL 缺少主机名")
	}
	ips := []net.IP{}
	if ip := net.ParseIP(host); ip != nil {
		ips = append(ips, ip)
	} else {
		lookupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		addrs, err := net.DefaultResolver.LookupIPAddr(lookupCtx, host)
		if err != nil {
			return fmt.Errorf("主机 %s 解析失败: %w", host, err)
		}
		for _, addr := range addrs {
			ips = append(ips, addr.IP)
		}
	}
	if len(ips) == 0 {
		return fmt.Errorf("主机 %s 没有可用地址", host)
	}
	for _, ip := range ips {
		if isDisallowedFetchIP(ip) {
			return fmt.Errorf("主机 %s 解析到不可取数地址 %s（回环/私网/链路本地需开启 allow_private_hosts）", host, ip)
		}
	}
	return nil
}

// fetchGitCatalogInto 克隆 + 读取仓内清单（不校验 URL 形状；供正式路径与单元测试共用）。
func fetchGitCatalogInto(ctx context.Context, srcName, rawURL, ref, catalogPath string) (*CatalogDocument, error) {
	cleanPath := path.Clean(strings.TrimSpace(catalogPath))
	if strings.TrimSpace(catalogPath) == "" {
		cleanPath = DefaultGitCatalogPath
	}
	if strings.HasPrefix(cleanPath, "/") || cleanPath == "." || cleanPath == ".." || strings.HasPrefix(cleanPath, "../") {
		return nil, fmt.Errorf("%w: 仓内清单路径 %q 不合法（必须落在仓库内的相对路径）", ErrInvalidInput, catalogPath)
	}
	dir := gitCheckoutDir(srcName)
	cloneCtx, cancel := context.WithTimeout(ctx, gitCloneTimeout)
	defer cancel()
	if err := gitClone(cloneCtx, rawURL, ref, dir); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUpstreamError, err)
	}
	full := filepath.Join(dir, filepath.FromSlash(cleanPath))
	// 仓内路径已规范化，这里再确认一次落点仍在工作副本内（防符号链接跳出）
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, fmt.Errorf("%w: 工作副本不可读: %v", ErrUpstreamError, err)
	}
	realFull, err := filepath.EvalSymlinks(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: 仓库内找不到清单 %s（缺省 %s；用 !<仓内路径> 指定）", ErrCatalogNotFound, catalogPath, DefaultGitCatalogPath)
		}
		return nil, fmt.Errorf("%w: 清单不可读: %v", ErrUpstreamError, err)
	}
	rel, err := filepath.Rel(realDir, realFull)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("%w: 仓内清单路径 %s 越出工作副本", ErrInvalidInput, catalogPath)
	}
	data, err := ReadFileLimited(realFull, MaxCatalogBytes)
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
		BaseDir: filepath.Dir(realFull),
	}, nil
}

// FetchGitCatalog 读取 git 来源的清单：形状校验 → 主机防线 → 浅克隆 → 读仓内清单。
// 每次调用重新克隆（--depth 1 单分支），保证不与远端漂移；工作副本固定落在临时缓存目录。
func FetchGitCatalog(ctx context.Context, src *Source) (*CatalogDocument, error) {
	if src == nil {
		return nil, fmt.Errorf("%w: 来源为空", ErrInvalidInput)
	}
	loc, err := ValidateGitLocation(src.Location, src.AllowInsecureHTTP == 1)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	if err := checkGitHost(loc.URL, src.AllowPrivateHosts == 1); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	return fetchGitCatalogInto(ctx, src.Name, loc.URL, loc.Ref, loc.CatalogPath)
}

// isDisallowedFetchIP 与 http 取数器共用同一 ClassifyIP 判据（回环/私网/链路本地/云元数据/CGNAT/NAT64/未指定/组播）。
func isDisallowedFetchIP(ip net.IP) bool {
	return ClassifyIP(ip, false) != nil
}
