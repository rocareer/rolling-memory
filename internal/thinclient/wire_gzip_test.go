// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocareer/rolling-memory/internal/thinclient"
	_ "modernc.org/sqlite"
)

// largeWirePacket 生成足够大的盲脉冲包（JSON > 512B），用于触发请求体 gzip。
func largeWirePacket(atoms int) thinclient.BlindPulsePacket {
	slots := make([]thinclient.BlindTokenAtom, 0, atoms)
	for i := 0; i < atoms; i++ {
		slots = append(slots, thinclient.BlindTokenAtom{
			H:          0x1000000000000000 + uint64(i)*0x0102030405060708,
			RawBitsX10: uint16(100 + i%900),
			ClassFlags: uint8(i % 16),
		})
	}
	return thinclient.BlindPulsePacket{
		UnitID:         "bg:big#0",
		Layer:          "background",
		Slots:          slots,
		SentenceFrames: [][]thinclient.BlindTokenAtom{slots},
	}
}

func decodeMaybeGzip(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var reader io.Reader = r.Body
	if r.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			t.Fatalf("gzip.NewReader: %v", err)
		}
		defer zr.Close()
		reader = zr
	}
	var body map[string]any
	if err := json.NewDecoder(reader).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return body
}

// gzipAwareServer 构造一个 /health 广告 gzip 能力的测试服务端；其余路径交给 handle。
func gzipAwareServer(handle func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/health") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok","wire_proto":[1,2],"wire_encodings":["gzip"]}`))
			return
		}
		handle(w, r)
	}))
}

// TestHTTPIngestSendsGzipWhenLarge 验证 auto 模式下（服务端 /health 广告 gzip）大请求体带
// Content-Encoding: gzip，服务端解压后拿到与未压缩一致的 v2 载荷。
func TestHTTPIngestSendsGzipWhenLarge(t *testing.T) {
	var gzipRequests, plainRequests atomic.Int32
	srv := gzipAwareServer(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Encoding") == "gzip" {
			gzipRequests.Add(1)
		} else {
			plainRequests.Add(1)
		}
		body := decodeMaybeGzip(t, r)
		if s, _ := body["atoms_b64"].(string); s == "" {
			t.Fatalf("want compact atoms_b64 payload, got %v", body["atoms_b64"])
		}
		_, _ = w.Write([]byte(`{"id":"bg:big#0","epoch":1}`))
	})
	defer srv.Close()

	tr := thinclient.NewHTTPBrainTransport(srv.URL, "mem_test")
	if _, err := tr.IngestBlindPulse(context.Background(), largeWirePacket(64)); err != nil {
		t.Fatalf("IngestBlindPulse: %v", err)
	}
	if gzipRequests.Load() != 1 || plainRequests.Load() != 0 {
		t.Fatalf("gzip=%d plain=%d, want gzip=1 plain=0", gzipRequests.Load(), plainRequests.Load())
	}
}

// TestHTTPIngestNoGzipAdvertisedStaysPlain 验证未广告 gzip 的服务端（旧服务端/静默 200 型）
// 绝不会收到压缩体——auto 模式必须以 /health 广告为准，不做盲探。
func TestHTTPIngestNoGzipAdvertisedStaysPlain(t *testing.T) {
	var gzipRequests, plainRequests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/health") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok","wire_proto":[1,2]}`)) // 无 wire_encodings 广告
			return
		}
		if r.Header.Get("Content-Encoding") == "gzip" {
			gzipRequests.Add(1)
			// 模拟「静默吞掉无法解析的体并回 200」的旧服务端：若客户端盲探压缩，本测试即红。
			_, _ = w.Write([]byte(`{"id":"","epoch":0}`))
			return
		}
		plainRequests.Add(1)
		_ = decodeMaybeGzip(t, r)
		_, _ = w.Write([]byte(`{"id":"bg:big#0","epoch":1}`))
	}))
	defer srv.Close()

	tr := thinclient.NewHTTPBrainTransport(srv.URL, "mem_test")
	if _, err := tr.IngestBlindPulse(context.Background(), largeWirePacket(64)); err != nil {
		t.Fatalf("IngestBlindPulse: %v", err)
	}
	if gzipRequests.Load() != 0 || plainRequests.Load() != 1 {
		t.Fatalf("gzip=%d plain=%d, want gzip=0 plain=1 (no advertisement ⇒ no blind probe)",
			gzipRequests.Load(), plainRequests.Load())
	}
}

// TestHTTPIngestSmallBodyStaysPlain 验证小于阈值的小请求体不压缩（gzip 头开销不划算）。
func TestHTTPIngestSmallBodyStaysPlain(t *testing.T) {
	var gzipRequests atomic.Int32
	srv := gzipAwareServer(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Encoding") == "gzip" {
			gzipRequests.Add(1)
		}
		_ = decodeMaybeGzip(t, r)
		_, _ = w.Write([]byte(`{"id":"bg:abc#0","epoch":1}`))
	})
	defer srv.Close()

	tr := thinclient.NewHTTPBrainTransport(srv.URL, "mem_test")
	if _, err := tr.IngestBlindPulse(context.Background(), sampleWirePacket()); err != nil {
		t.Fatalf("IngestBlindPulse: %v", err)
	}
	if gzipRequests.Load() != 0 {
		t.Fatalf("small body must not be compressed, gzip requests = %d", gzipRequests.Load())
	}
}

// TestHTTPIngestFallsBackToPlainWhenGzipRejected 验证广告了 gzip 但实际拒收压缩体的服务端：
// 自动回退未压缩，且回退结果只上报一次、后续请求直接走未压缩。
func TestHTTPIngestFallsBackToPlainWhenGzipRejected(t *testing.T) {
	var gzipRequests, plainRequests atomic.Int32
	srv := gzipAwareServer(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Encoding") == "gzip" {
			gzipRequests.Add(1)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"cannot decode gzip body"}`))
			return
		}
		plainRequests.Add(1)
		_ = decodeMaybeGzip(t, r)
		_, _ = w.Write([]byte(`{"id":"bg:big#0","epoch":1}`))
	})
	defer srv.Close()

	var fallbacks atomic.Int32
	tr := thinclient.NewHTTPBrainTransport(srv.URL, "mem_test")
	tr.WireGzipFallback = func() { fallbacks.Add(1) }

	if _, err := tr.IngestBlindPulse(context.Background(), largeWirePacket(64)); err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	if _, err := tr.IngestBlindPulse(context.Background(), largeWirePacket(64)); err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	if gzipRequests.Load() != 1 || plainRequests.Load() != 2 {
		t.Fatalf("gzip=%d plain=%d, want gzip=1 plain=2", gzipRequests.Load(), plainRequests.Load())
	}
	if fallbacks.Load() != 1 {
		t.Fatalf("fallback callback = %d, want 1", fallbacks.Load())
	}
}

// TestHTTPIngestForcedGzipDoesNotFallBack 验证显式 gzip 模式失败可见，不静默降级。
func TestHTTPIngestForcedGzipDoesNotFallBack(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"old server"}`))
	}))
	defer srv.Close()

	tr := thinclient.NewHTTPBrainTransport(srv.URL, "mem_test")
	tr.WireGzip = thinclient.WireGzipForce
	if _, err := tr.IngestBlindPulse(context.Background(), largeWirePacket(64)); err == nil {
		t.Fatal("forced gzip must surface the server error")
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1 (no fallback)", requests.Load())
	}
}

// TestEncodeWireGzipRoundTrip 验证 gzip 编码可被标准库无损还原。
func TestEncodeWireGzipRoundTrip(t *testing.T) {
	raw := bytes.Repeat([]byte(`{"wire_proto":2,"atoms_b64":"AQIDBAUGBwgJCgs="}`), 40)
	gz, err := thinclient.EncodeWireGzip(raw)
	if err != nil {
		t.Fatalf("EncodeWireGzip: %v", err)
	}
	if len(gz) >= len(raw) {
		t.Fatalf("gzip(%dB) = %dB, want compression", len(raw), len(gz))
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	back, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Equal(back, raw) {
		t.Fatal("gzip roundtrip mismatch")
	}
}

// TestVaultRemembersWireGzipFallback 验证 gzip 回退结果持久化到本地金库，供后续进程复用。
func TestVaultRemembersWireGzipFallback(t *testing.T) {
	db, err := sql.Open("sqlite", "file:wire_gzip_meta?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	vault, err := thinclient.NewVault(db, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}
	if vault.ServerWireGzipUnsupported() {
		t.Fatal("fresh vault must not be marked as gzip-incapable")
	}
	if err := vault.MarkServerWireGzipUnsupported(); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if !vault.ServerWireGzipUnsupported() {
		t.Fatal("fallback flag was not persisted")
	}
	// The flag must expire so a later server upgrade is re-probed.
	if _, err := db.Exec(`UPDATE thin_vault_meta SET v = ? WHERE k = 'server_wire_gzip_unsupported'`,
		strconv.FormatInt(time.Now().Add(-25*time.Hour).Unix(), 10)); err != nil {
		t.Fatalf("age fallback flag: %v", err)
	}
	if vault.ServerWireGzipUnsupported() {
		t.Fatal("expired fallback flag must not pin the client to uncompressed bodies")
	}
}
