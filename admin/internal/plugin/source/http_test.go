package source

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func response(status int, body string, headers map[string]string) *http.Response {
	header := make(http.Header)
	for key, value := range headers {
		header.Set(key, value)
	}
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestClassifyIP(t *testing.T) {
	rejected := []string{
		"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.1.1",
		"169.254.169.254", "169.254.170.2", "100.100.100.200",
		"0.0.0.0", "224.0.0.1", "255.255.255.255",
		"100.64.0.1", "192.0.2.1", "198.18.0.1", "198.51.100.1", "203.0.113.1", "240.0.0.1",
		"::", "::1", "fe80::1", "fd00::1", "ff02::1", "100::1",
		"2001::1", "2002::1", "64:ff9b::1", "fd00:ec2::254",
		"::ffff:10.0.0.1", "::ffff:127.0.0.1",
	}
	for _, raw := range rejected {
		ip := net.ParseIP(raw)
		if ip == nil {
			t.Fatalf("测试地址无法解析: %s", raw)
		}
		if err := ClassifyIP(ip, false); err == nil {
			t.Errorf("默认策略应拒绝 %s", raw)
		}
	}
	for _, raw := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		if err := ClassifyIP(net.ParseIP(raw), false); err != nil {
			t.Errorf("公网地址 %s 不应被拒: %v", raw, err)
		}
	}
	// allow_private_hosts=true 只放行显式 RFC1918/ULA/回环，链路本地与元数据始终拒绝
	for _, raw := range []string{"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.1.1", "fd00::1", "::1"} {
		if err := ClassifyIP(net.ParseIP(raw), true); err != nil {
			t.Errorf("allow_private_hosts=true 应放行 %s: %v", raw, err)
		}
	}
	for _, raw := range []string{"169.254.169.254", "169.254.170.2", "100.100.100.200", "fd00:ec2::254", "fe80::1", "100.64.0.1", "192.0.2.1"} {
		if err := ClassifyIP(net.ParseIP(raw), true); err == nil {
			t.Errorf("allow_private_hosts=true 仍应拒绝 %s", raw)
		}
	}
}

func TestValidateHTTPURL(t *testing.T) {
	if _, err := ValidateHTTPURL("https://example.com/catalog.yaml", false); err != nil {
		t.Fatalf("https URL 应合法: %v", err)
	}
	if _, err := ValidateHTTPURL("http://example.com/catalog.yaml", false); err == nil {
		t.Fatal("默认应拒绝明文 http")
	}
	if _, err := ValidateHTTPURL("http://example.com/catalog.yaml", true); err != nil {
		t.Fatalf("显式允许后 http 应合法: %v", err)
	}
	for _, raw := range []string{
		"ftp://example.com/x", "https://user:pass@example.com/x",
		"https://example.com/x?token=1", "https://example.com/x#frag",
		"https:///x", "https://example.com/bad path",
	} {
		if _, err := ValidateHTTPURL(raw, true); err == nil {
			t.Errorf("应拒绝 URL %q", raw)
		}
	}
}

func TestFetcherSuccessAndLimit(t *testing.T) {
	fetcher := NewFetcher(FetchPolicy{
		Timeout: time.Second,
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Header.Get("Authorization") != "" || req.Header.Get("Cookie") != "" {
				t.Fatal("取数请求不得携带凭据")
			}
			return response(200, "hello", nil), nil
		}),
	})
	result, err := fetcher.FetchBytes(context.Background(), "https://example.com/catalog.yaml", 10)
	if err != nil {
		t.Fatalf("取数失败: %v", err)
	}
	sum := sha256.Sum256([]byte("hello"))
	if result.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("摘要不符: %s", result.SHA256)
	}
	if _, err := fetcher.FetchBytes(context.Background(), "https://example.com/catalog.yaml", 4); err == nil {
		t.Fatal("超过 max 的响应应被拒绝")
	}
}

func TestFetcherHTTPError(t *testing.T) {
	fetcher := NewFetcher(FetchPolicy{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return response(404, "missing", nil), nil
		}),
	})
	if _, err := fetcher.FetchBytes(context.Background(), "https://example.com/x", 100); err == nil {
		t.Fatal("非 2xx 应被拒绝")
	}
}

func TestFetcherRedirectPolicy(t *testing.T) {
	fetcher := NewFetcher(FetchPolicy{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return response(302, "", map[string]string{"Location": "http://example.com/downgrade"}), nil
		}),
	})
	if _, err := fetcher.FetchBytes(context.Background(), "https://example.com/start", 100); err == nil {
		t.Fatal("https → http 重定向应被拒绝")
	}

	hops := 0
	fetcher = NewFetcher(FetchPolicy{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			hops++
			return response(302, "", map[string]string{"Location": "https://example.com/next"}), nil
		}),
	})
	if _, err := fetcher.FetchBytes(context.Background(), "https://example.com/start", 100); err == nil {
		t.Fatal("超过 3 跳的重定向应被拒绝")
	}
	if hops > 4 {
		t.Fatalf("重定向跳数异常: %d", hops)
	}

	fetcher = NewFetcher(FetchPolicy{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return response(302, "", map[string]string{"Location": "https://example.com/x?token=1"}), nil
		}),
	})
	if _, err := fetcher.FetchBytes(context.Background(), "https://example.com/start", 100); err == nil {
		t.Fatal("重定向到带 query 的地址应被拒绝")
	}
}

func TestFetcherDownload(t *testing.T) {
	fetcher := NewFetcher(FetchPolicy{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return response(200, "payload", nil), nil
		}),
	})
	var buffer bytes.Buffer
	result, err := fetcher.Download(context.Background(), "https://example.com/pkg.zip", 100, &buffer)
	if err != nil {
		t.Fatalf("下载失败: %v", err)
	}
	if result.Bytes != 7 || buffer.String() != "payload" {
		t.Fatalf("下载结果不符: %+v %q", result, buffer.String())
	}
	var small bytes.Buffer
	if _, err := fetcher.Download(context.Background(), "https://example.com/pkg.zip", 3, &small); err == nil {
		t.Fatal("超过 max 的下载应被拒绝")
	}
}

// ---- safeDial：SSRF 的「校验与连接用同一份解析结果」这一层 ----
//
// 注入点替换解析与拨号两侧 ⇒ 不连真实 DNS、不开真实端口即可断言：
//   - 拨号目标是**解析出的 IP**（不是原主机名）——这是防 DNS rebinding 的实质；
//   - 分类发生在**拨号那一刻**（attach 时安全、拨号时解析到内网也要拦）；
//   - 多 A 记录里混入一个被拒地址 ⇒ 整个主机拒（不挑好的用）；
//   - allowPrivateHosts 只放行 RFC1918/ULA/回环；链路本地/云元数据始终拒。

type dialRecord struct {
	network string
	addr    string
}

// withFakeResolver 注入静态解析表与拨号记录器（用例结束自动还原）。
// failAddr 非空时，命中该地址的拨号返回错误（用于多条 A 记录的回落用例）。
func withFakeResolver(t *testing.T, table map[string][]string, failAddr string) (*[]dialRecord, func(string) error) {
	t.Helper()
	originalLookup, originalDial := lookupIPAddr, dialContextFn
	t.Cleanup(func() { lookupIPAddr, dialContextFn = originalLookup, originalDial })

	dials := &[]dialRecord{}
	lookupIPAddr = func(_ context.Context, host string) ([]net.IPAddr, error) {
		raw, ok := table[host]
		if !ok {
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
		out := make([]net.IPAddr, 0, len(raw))
		for _, item := range raw {
			ip := net.ParseIP(item)
			if ip == nil {
				t.Fatalf("用例解析表里的地址非法: %s", item)
			}
			out = append(out, net.IPAddr{IP: ip})
		}
		return out, nil
	}
	dialContextFn = func(_ context.Context, network, addr string) (net.Conn, error) {
		*dials = append(*dials, dialRecord{network: network, addr: addr})
		if failAddr != "" && addr == failAddr {
			return nil, &net.OpError{Op: "dial", Net: network, Err: errors.New("connection refused")}
		}
		return nil, nil // conn=nil 且 err=nil：用例只看「拨到哪」，不需要真连接
	}
	assertDialed := func(addr string) error {
		for _, item := range *dials {
			if item.addr == addr {
				return nil
			}
		}
		return &net.AddrError{Err: "未拨号", Addr: addr}
	}
	return dials, assertDialed
}

func TestSafeDialDialsResolvedIP(t *testing.T) {
	dials, assertDialed := withFakeResolver(t, map[string][]string{
		"packages.example.com": {"93.184.216.34"},
	}, "")
	fetcher := NewFetcher(FetchPolicy{})

	if _, err := fetcher.safeDial(context.Background(), "tcp", "packages.example.com:443"); err != nil {
		t.Fatalf("公网地址应放行: %v", err)
	}
	if len(*dials) != 1 {
		t.Fatalf("应恰好拨号一次，实际 %+v", *dials)
	}
	// 关键断言：拨的是解析出的 IP，而不是原主机名（否则第二次解析可能被换成内网地址）
	if err := assertDialed("93.184.216.34:443"); err != nil {
		t.Fatalf("必须拨到已校验的 IP 而非主机名: %+v", *dials)
	}
	if (*dials)[0].network != "tcp" {
		t.Fatalf("network 透传不符: %+v", (*dials)[0])
	}
}

func TestSafeDialRejectsAtDialTime(t *testing.T) {
	dials, _ := withFakeResolver(t, map[string][]string{
		// 解析结果里混入一个内网地址：即使另一个是公网，也必须整机拒绝（不挑好的用）
		"rebind.example.com":   {"93.184.216.34", "10.1.2.3"},
		"internal.example.com": {"192.168.1.10"},
		"metadata.example.com": {"169.254.169.254"},
		"empty.example.com":    {},
	}, "")
	fetcher := NewFetcher(FetchPolicy{})

	for _, host := range []string{"rebind.example.com", "internal.example.com", "metadata.example.com", "empty.example.com"} {
		if _, err := fetcher.safeDial(context.Background(), "tcp", host+":443"); err == nil {
			t.Fatalf("主机 %s 应被拒绝", host)
		}
	}
	if len(*dials) != 0 {
		t.Fatalf("被拒主机不得发生任何拨号，实际 %+v", *dials)
	}

	// 解析失败 → upstream 语义（取数失败），不是被策略拦住
	if _, err := fetcher.safeDial(context.Background(), "tcp", "nx.example.com:443"); !errors.Is(err, ErrUpstreamError) {
		t.Fatalf("解析失败应映射为 ErrUpstreamError，实际 %v", err)
	}
}

func TestSafeDialPrivatePolicy(t *testing.T) {
	dials, assertDialed := withFakeResolver(t, map[string][]string{
		"mirror.lan":     {"10.0.0.9"},
		"loopback.test":  {"127.0.0.1"},
		"metadata.test":  {"100.100.100.200"},
		"linklocal.test": {"169.254.1.1"},
	}, "")
	allowPrivate := NewFetcher(FetchPolicy{AllowPrivateHosts: true})

	if _, err := allowPrivate.safeDial(context.Background(), "tcp", "mirror.lan:8080"); err != nil {
		t.Fatalf("allow_private_hosts=true 时应放行 RFC1918: %v", err)
	}
	if _, err := allowPrivate.safeDial(context.Background(), "tcp", "loopback.test:8080"); err != nil {
		t.Fatalf("allow_private_hosts=true 时应放行回环: %v", err)
	}
	if err := assertDialed("10.0.0.9:8080"); err != nil {
		t.Fatalf("内网镜像应拨到解析出的 IP: %+v", *dials)
	}
	// 即使放行私网，链路本地与云元数据地址仍必须拒
	for _, host := range []string{"metadata.test", "linklocal.test"} {
		if _, err := allowPrivate.safeDial(context.Background(), "tcp", host+":80"); err == nil {
			t.Fatalf("allow_private_hosts=true 也不得放行 %s", host)
		}
	}
}

func TestSafeDialFallsThroughMultipleIPs(t *testing.T) {
	// 首条 IP 拨号失败 ⇒ 必须继续试下一条
	dials, assertDialed := withFakeResolver(t, map[string][]string{
		"multi.example.com": {"93.184.216.34", "93.184.216.35"},
	}, "93.184.216.34:443")
	fetcher := NewFetcher(FetchPolicy{})

	if _, err := fetcher.safeDial(context.Background(), "tcp", "multi.example.com:443"); err != nil {
		t.Fatalf("首条失败后应回落到下一条: %v", err)
	}
	if len(*dials) != 2 {
		t.Fatalf("应尝试两条解析结果，实际 %+v", *dials)
	}
	if err := assertDialed("93.184.216.35:443"); err != nil {
		t.Fatalf("第二条 IP 应被尝试: %+v", *dials)
	}
}

// TestSafeDialAllCandidatesFail：全部候选都拨不通 ⇒ upstream 语义（不是被策略拦住）。
func TestSafeDialAllCandidatesFail(t *testing.T) {
	_, _ = withFakeResolver(t, map[string][]string{
		"down.example.com": {"93.184.216.34"},
	}, "93.184.216.34:443")
	fetcher := NewFetcher(FetchPolicy{})
	if _, err := fetcher.safeDial(context.Background(), "tcp", "down.example.com:443"); !errors.Is(err, ErrUpstreamError) {
		t.Fatalf("全部拨号失败应映射为 ErrUpstreamError，实际 %v", err)
	}
}

// TestTransportUsesSafeDialByDefault：默认 Transport 必须挂 safeDial（而不是裸 Dialer），
// 注入 Transport/DialContext 时才让位——这条是「SSRF 生效」的接线断言。
func TestTransportUsesSafeDialByDefault(t *testing.T) {
	fetcher := NewFetcher(FetchPolicy{})
	transport, ok := fetcher.transport().(*http.Transport)
	if !ok {
		t.Fatalf("默认应为 *http.Transport，实际 %T", fetcher.transport())
	}
	if transport.Proxy != nil {
		t.Fatal("不得读取环境代理（请求会被转发到不可控出口）")
	}
	_, _ = withFakeResolver(t, map[string][]string{"host.example.com": {"10.0.0.5"}}, "")
	if _, err := transport.DialContext(context.Background(), "tcp", "host.example.com:443"); err == nil {
		t.Fatal("默认拨号器必须走 safeDial（内网地址应被拒）")
	}
}
