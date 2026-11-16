// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient

import (
	"bytes"
	"compress/gzip"
	"errors"
	"strconv"
	"time"
)

// Wire gzip：把 ingest 请求体（JSON + base64 原子流）做传输层 gzip 压缩。
//
// 原理（实测，真仓语料）：
//   - 载荷里 base64 只用了 64/256 个字节值（25% 字母表冗余），且同一文档内重复
//     token 序列结构可被 DEFLATE 进一步回收。
//   - 真仓文档实测：整文 gzip 省 48~52%，生产分块（386 字符/块）逐块省 26~35%，
//     批量单流省 47~52%；服务端解压后与未压缩载荷逐字节等价。
//
// 兼容：旧服务端无法解压 gzip 请求体（JSON 解析失败 → 400/500），传输层在 auto 模式下
// 回退为未压缩并记忆该结果（同 wire v2 的协商模式），24h 后自动重探。
const (
	// WireGzipOff disables request-body compression.
	WireGzipOff = 1
	// WireGzipForce always compresses and surfaces failures (no silent downgrade).
	WireGzipForce = 2
	// wireGzipMinBytes 是触发压缩的最小请求体字节数：更小的请求体压缩收益无法覆盖
	// gzip 头（约 20B）与解压开销。
	wireGzipMinBytes = 512
	// wireGzipMetaKey remembers when the connected server rejected gzip request bodies.
	wireGzipMetaKey = "server_wire_gzip_unsupported"
	// wireGzipProbeTTL bounds that memory: after 24h the client re-probes once.
	wireGzipProbeTTL = 24 * time.Hour
)

// EncodeWireGzip compresses a request body with the gzip container format.
func EncodeWireGzip(body []byte) ([]byte, error) {
	if len(body) == 0 {
		return nil, errors.New("thinclient: wire gzip requires a non-empty body")
	}
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := zw.Write(body); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// MarkServerWireGzipUnsupported records that the connected server rejected gzip request bodies.
func (v *Vault) MarkServerWireGzipUnsupported() error {
	if v == nil || v.db == nil {
		return errors.New("thinclient: nil Vault db")
	}
	_, err := v.db.Exec(`INSERT OR REPLACE INTO thin_vault_meta(k, v) VALUES (?, ?)`,
		wireGzipMetaKey, strconv.FormatInt(time.Now().Unix(), 10))
	return err
}

// ServerWireGzipUnsupported reports whether a recent sync already fell back to uncompressed
// request bodies. The flag expires after wireGzipProbeTTL so a server upgrade is re-probed.
func (v *Vault) ServerWireGzipUnsupported() bool {
	if v == nil || v.db == nil {
		return false
	}
	var raw string
	if err := v.db.QueryRow(`SELECT v FROM thin_vault_meta WHERE k = ?`, wireGzipMetaKey).Scan(&raw); err != nil {
		return false
	}
	rejectedAt, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return false
	}
	return time.Since(time.Unix(rejectedAt, 0)) < wireGzipProbeTTL
}
