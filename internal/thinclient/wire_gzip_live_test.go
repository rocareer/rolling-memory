// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocareer/rolling-memory/internal/cloudcred"
	"github.com/rocareer/rolling-memory/internal/thinclient"
)

// TestLiveUpstreamWireGzipByteSavings_UserID10 在真实链路上实测 gzip 传输层压缩的字节收益：
// 同一大载荷分别以「强制 gzip」与「关闭压缩」经本地计数代理上行到 /open/memory/v1/ingest，
// 对比客户端→服务端方向的实际字节数（含 HTTP 头，即真实传输成本）。
func TestLiveUpstreamWireGzipByteSavings_UserID10(t *testing.T) {
	// 端侧唯一凭据 = 数据面签名钥（`mem2.*`）：仅在显式提供 ROLLING_MEMORY_BRAIN_URL 时才执行远端集成测试。
	baseURL := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_BRAIN_URL"))
	if baseURL == "" {
		t.Skip("ROLLING_MEMORY_BRAIN_URL not set; skipping live upstream wire-gzip test in local mode")
	}
	apiKey := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_BRAIN_KEY"))
	if apiKey == "" {
		_, accountKey, credErr := cloudcred.Ensure(context.Background())
		if credErr != nil {
			t.Skipf("live data-plane credentials not configured (%v); skipping", credErr)
		}
		apiKey = accountKey
	}
	health := strings.TrimRight(baseURL, "/")
	if !strings.HasSuffix(health, "/open/memory/v1") {
		health += "/open/memory/v1"
	}
	resp, err := http.Get(health + "/health")
	if err != nil {
		t.Skipf("live upstream %s not reachable (%v); skipping", health, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Skipf("live upstream %s returned %d; skipping", health, resp.StatusCode)
	}
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		t.Fatalf("parse base url: %v", err)
	}
	upstreamAddr := u.Host

	// 本地计数代理：只统计客户端→服务端方向的字节。
	var clientToServer atomic.Int64
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen proxy: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				up, err := net.Dial("tcp", upstreamAddr)
				if err != nil {
					return
				}
				defer up.Close()
				go func() {
					_, _ = io.Copy(up, io.TeeReader(c, countingWriter{&clientToServer}))
					_ = up.(*net.TCPConn).CloseWrite()
				}()
				_, _ = io.Copy(c, up)
			}(conn)
		}
	}()

	// 足够大的载荷：2000 atom（v2 裸流 22KB，base64 后约 29KB，超过 512B 压缩阈值）。
	slots := make([]thinclient.BlindTokenAtom, 0, 2000)
	for i := 0; i < 2000; i++ {
		slots = append(slots, thinclient.BlindTokenAtom{
			H:          0x1000000000000000 + uint64(i)*0x0102030405060708,
			RawBitsX10: uint16(100 + i%900),
			ClassFlags: uint8(i % 16),
		})
	}
	pkt := thinclient.BlindPulsePacket{
		UnitID:         "u10-gzip-bytes#0",
		Layer:          "background",
		Slots:          slots,
		SentenceFrames: [][]thinclient.BlindTokenAtom{slots},
	}
	ctx := context.Background()

	measure := func(wireGzip int) int64 {
		tr := thinclient.NewHTTPBrainTransport("http://"+ln.Addr().String(), apiKey)
		tr.UserID = thinclient.DefaultUpstreamDevUserID
		tr.WireGzip = wireGzip
		before := clientToServer.Load()
		if _, err := tr.IngestBlindPulse(ctx, pkt); err != nil {
			t.Fatalf("ingest (wireGzip=%d): %v", wireGzip, err)
		}
		return clientToServer.Load() - before
	}

	plainBytes := measure(thinclient.WireGzipOff)
	gzipBytes := measure(thinclient.WireGzipForce)
	saving := 100 * (1 - float64(gzipBytes)/float64(plainBytes))
	if gzipBytes >= plainBytes {
		t.Fatalf("gzip must shrink the request: plain=%dB gzip=%dB", plainBytes, gzipBytes)
	}
	t.Logf("live wire bytes (client→server): plain=%dB gzip=%dB saving=%.1f%%", plainBytes, gzipBytes, saving)

	// 清理探针单元，不留残余。
	cleanup := thinclient.NewHTTPBrainTransport(baseURL, apiKey)
	cleanup.UserID = thinclient.DefaultUpstreamDevUserID
	cleanupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := cleanup.DeleteUnits(cleanupCtx, []string{pkt.UnitID}); err != nil {
		t.Fatalf("purge probe unit: %v", err)
	}
}

type countingWriter struct {
	n *atomic.Int64
}

func (w countingWriter) Write(p []byte) (int, error) {
	w.n.Add(int64(len(p)))
	return len(p), nil
}
