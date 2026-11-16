package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// FetchPolicy 远程取数策略（每个来源一份；默认拒绝回环/私网/链路本地）。
type FetchPolicy struct {
	// AllowInsecureHTTP 允许明文 http（仍受私网/链路本地检查约束）
	AllowInsecureHTTP bool
	// AllowPrivateHosts 显式允许 RFC1918/ULA/回环（内部镜像用）；链路本地与云元数据地址始终拒绝
	AllowPrivateHosts bool
	// Timeout 单次取数总超时
	Timeout time.Duration
	// Transport 测试注入的 RoundTripper（nil = 使用带 SSRF 拨号器的默认 Transport）
	Transport http.RoundTripper
	// DialContext 测试注入的拨号器（nil = 安全拨号器）
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)
}

// FetchResult 一次远程取数的结果。
type FetchResult struct {
	Bytes    []byte
	SHA256   string
	FinalURL *url.URL
}

// DownloadResult 一次流式下载的结果（包体用）。
type DownloadResult struct {
	Bytes    int64
	SHA256   string
	FinalURL *url.URL
}

// Fetcher 受策略约束的 HTTP 取数器。
type Fetcher struct {
	policy FetchPolicy
	client *http.Client
}

// NewFetcher 构造取数器（策略默认值：超时 60s）。
func NewFetcher(policy FetchPolicy) *Fetcher {
	if policy.Timeout <= 0 {
		policy.Timeout = 60 * time.Second
	}
	f := &Fetcher{policy: policy}
	f.client = &http.Client{
		Timeout:       policy.Timeout,
		Transport:     f.transport(),
		CheckRedirect: f.checkRedirect,
	}
	return f
}

// FetchBytes 取一份有限大小的字节（清单）。
func (f *Fetcher) FetchBytes(ctx context.Context, rawURL string, max int64) (*FetchResult, error) {
	req, err := f.newRequest(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUpstreamError, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: HTTP %d", ErrUpstreamError, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, fmt.Errorf("%w: 读取响应失败: %v", ErrUpstreamError, err)
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%w: 响应超过 %d 字节上限", ErrUpstreamError, max)
	}
	sum := sha256.Sum256(data)
	return &FetchResult{
		Bytes:    data,
		SHA256:   hex.EncodeToString(sum[:]),
		FinalURL: finalURL(req, resp),
	}, nil
}

// Download 流式下载到 dst，边写边算 sha256；超过 max 即中止（调用方负责删除未完成件）。
func (f *Fetcher) Download(ctx context.Context, rawURL string, max int64, dst io.Writer) (*DownloadResult, error) {
	req, err := f.newRequest(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUpstreamError, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: HTTP %d", ErrUpstreamError, resp.StatusCode)
	}
	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(dst, hasher), io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, fmt.Errorf("%w: 下载失败: %v", ErrUpstreamError, err)
	}
	if written > max {
		return nil, fmt.Errorf("%w: 包体超过 %d 字节上限", ErrUpstreamError, max)
	}
	return &DownloadResult{
		Bytes:    written,
		SHA256:   hex.EncodeToString(hasher.Sum(nil)),
		FinalURL: finalURL(req, resp),
	}, nil
}

// finalURL 取重定向后的最终 URL（测试注入的 RoundTripper 可能不填 resp.Request）。
func finalURL(req *http.Request, resp *http.Response) *url.URL {
	if resp != nil && resp.Request != nil && resp.Request.URL != nil {
		return resp.Request.URL
	}
	if req != nil {
		return req.URL
	}
	return nil
}

func (f *Fetcher) newRequest(ctx context.Context, rawURL string) (*http.Request, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("%w: URL 无法解析: %v", ErrNetworkBlocked, err)
	}
	if err := f.validateURL(u); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: 构造请求失败: %v", ErrNetworkBlocked, err)
	}
	// 不携带任何凭据/Cookie；只声明可接受的媒体类型（私有源认证 v1 不支持）。
	req.Header.Set("Accept", "application/yaml, text/yaml, application/octet-stream, */*")
	return req, nil
}

func (f *Fetcher) validateURL(u *url.URL) error {
	if u == nil {
		return fmt.Errorf("%w: URL 为空", ErrNetworkBlocked)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: scheme %q 不允许（只接受 http/https）", ErrNetworkBlocked, u.Scheme)
	}
	if u.Scheme == "http" && !f.policy.AllowInsecureHTTP {
		return fmt.Errorf("%w: 明文 http 未开启 allow_insecure_http", ErrNetworkBlocked)
	}
	if u.User != nil {
		return fmt.Errorf("%w: 不允许 userinfo", ErrNetworkBlocked)
	}
	if u.RawQuery != "" || u.ForceQuery {
		return fmt.Errorf("%w: 不允许 query", ErrNetworkBlocked)
	}
	if u.Fragment != "" {
		return fmt.Errorf("%w: 不允许 fragment", ErrNetworkBlocked)
	}
	host := u.Hostname()
	if host == "" || strings.ContainsRune(host, 0) {
		return fmt.Errorf("%w: 主机名为空或不合法", ErrNetworkBlocked)
	}
	if ip := net.ParseIP(host); ip != nil {
		return ClassifyIP(ip, f.policy.AllowPrivateHosts)
	}
	return nil
}

func (f *Fetcher) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > 3 {
		return fmt.Errorf("%w: 重定向超过 3 跳", ErrNetworkBlocked)
	}
	if len(via) > 0 {
		prev := via[len(via)-1]
		if prev.URL.Scheme == "https" && req.URL.Scheme != "https" {
			return fmt.Errorf("%w: 禁止 https → %s 降级重定向", ErrNetworkBlocked, req.URL.Scheme)
		}
	}
	return f.validateURL(req.URL)
}

func (f *Fetcher) transport() http.RoundTripper {
	if f.policy.Transport != nil {
		return f.policy.Transport
	}
	dial := f.policy.DialContext
	if dial == nil {
		dial = f.safeDial
	}
	return &http.Transport{
		Proxy:                 nil, // 不读取环境代理：避免请求被转发到不可控出口
		DialContext:           dial,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
	}
}

// 安全拨号的两个可注入点（包内变量；只有 safeDial 使用）。
//
// 为什么留缝：SSRF 的关键性质是「**校验与连接使用同一份解析结果**」（防 DNS rebinding），
// 断言它必须能替换「解析」与「拨号」两侧——否则要么连真实 DNS、要么真开端口，测试既不确定也不可靠。
// 生产路径不替换（默认即 net.DefaultResolver 与真实 Dialer）。
var (
	// lookupIPAddr 名称解析（默认系统解析器）
	lookupIPAddr = net.DefaultResolver.LookupIPAddr
	// dialContextFn 拨号（默认 10s 超时 + keepalive 的 Dialer）
	dialContextFn = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
)

// safeDial 解析全部 A/AAAA 并逐条分类；命中拒绝段即拒绝，全部通过后直接拨号到已校验 IP
// （避免 DNS rebinding：校验与连接使用同一份解析结果）。
func (f *Fetcher) safeDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("%w: 目标地址 %q 不合法: %v", ErrNetworkBlocked, addr, err)
	}
	ips, err := lookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("%w: 解析主机 %q 失败: %v", ErrUpstreamError, host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("%w: 主机 %q 没有解析结果", ErrNetworkBlocked, host)
	}
	allowed := make([]net.IP, 0, len(ips))
	for _, item := range ips {
		if err := ClassifyIP(item.IP, f.policy.AllowPrivateHosts); err != nil {
			return nil, fmt.Errorf("%w: 主机 %q 解析到被拒绝的地址 %s: %v", ErrNetworkBlocked, host, item.IP, err)
		}
		allowed = append(allowed, item.IP)
	}
	var lastErr error
	for _, ip := range allowed {
		conn, err := dialContextFn(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("%w: 连接 %q 失败: %v", ErrUpstreamError, host, lastErr)
}

// ---- SSRF 地址分类 ----

var (
	alwaysBlockedV4 = mustPrefixes(
		"0.0.0.0/8",       // 本网络
		"100.64.0.0/10",   // CGNAT
		"169.254.0.0/16",  // 链路本地（含云元数据）
		"192.0.0.0/24",    // IETF 协议分配
		"192.0.2.0/24",    // 文档网段
		"198.18.0.0/15",   // benchmark
		"198.51.100.0/24", // 文档网段
		"203.0.113.0/24",  // 文档网段
		"240.0.0.0/4",     // 保留
	)
	alwaysBlockedV6 = mustPrefixes(
		"::/128",       // 未指定
		"fe80::/10",    // 链路本地
		"ff00::/8",     // 组播
		"100::/64",     // discard-only
		"2001::/32",    // Teredo
		"2002::/16",    // 6to4
		"64:ff9b::/96", // NAT64
	)
	privateV4 = mustPrefixes(
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"127.0.0.0/8",
	)
	privateV6 = mustPrefixes(
		"fc00::/7", // ULA
		"::1/128",  // 回环
	)
	metadataAddrs = mustAddrs(
		"169.254.169.254", // 常见云元数据
		"169.254.170.2",   // 容器凭据端点
		"100.100.100.200", // 部分云厂商元数据
		"fd00:ec2::254",   // 云厂商 IPv6 元数据
	)
)

func mustPrefixes(values ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		out = append(out, netip.MustParsePrefix(value))
	}
	return out
}

func mustAddrs(values ...string) []netip.Addr {
	out := make([]netip.Addr, 0, len(values))
	for _, value := range values {
		out = append(out, netip.MustParseAddr(value))
	}
	return out
}

// ClassifyIP 判断一个解析结果是否允许访问：
//   - 默认拒绝回环/RFC1918/ULA/链路本地/组播/未指定/CGNAT/文档网段/benchmark/6to4/NAT64/Teredo/discard；
//   - allowPrivate=true 只放行显式 RFC1918/ULA/回环；链路本地与云元数据地址始终拒绝；
//   - IPv4-mapped IPv6 先 Unmap 再分类。
func ClassifyIP(ip net.IP, allowPrivate bool) error {
	if ip == nil {
		return fmt.Errorf("%w: 地址为空", ErrNetworkBlocked)
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return fmt.Errorf("%w: 地址 %v 不是合法 IP", ErrNetworkBlocked, ip)
	}
	addr = addr.Unmap()
	for _, item := range metadataAddrs {
		if addr == item {
			return fmt.Errorf("%w: 云元数据地址 %s 始终拒绝", ErrNetworkBlocked, addr)
		}
	}
	for _, prefix := range alwaysBlockedV4 {
		if prefix.Contains(addr) {
			return fmt.Errorf("%w: 地址 %s 命中拒绝网段 %s", ErrNetworkBlocked, addr, prefix)
		}
	}
	for _, prefix := range alwaysBlockedV6 {
		if prefix.Contains(addr) {
			return fmt.Errorf("%w: 地址 %s 命中拒绝网段 %s", ErrNetworkBlocked, addr, prefix)
		}
	}
	for _, prefix := range privateV4 {
		if prefix.Contains(addr) {
			if !allowPrivate {
				return fmt.Errorf("%w: 私网/回环地址 %s 命中 %s（未开启 allow_private_hosts）", ErrNetworkBlocked, addr, prefix)
			}
			return nil
		}
	}
	for _, prefix := range privateV6 {
		if prefix.Contains(addr) {
			if !allowPrivate {
				return fmt.Errorf("%w: 私网/回环地址 %s 命中 %s（未开启 allow_private_hosts）", ErrNetworkBlocked, addr, prefix)
			}
			return nil
		}
	}
	if addr.IsUnspecified() || addr.IsMulticast() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() {
		return fmt.Errorf("%w: 地址 %s 属于始终拒绝类别", ErrNetworkBlocked, addr)
	}
	return nil
}

// IsPublicIP 供测试与诊断使用：等价于 ClassifyIP(ip,false)==nil。
func IsPublicIP(ip net.IP) bool {
	return ClassifyIP(ip, false) == nil
}
