// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// EngineName is the canonical unified engine name across client, cloud, and evaluation lab.
	EngineName = "Rolling Autonoetic Memory Engine"
	// EngineNameAutonoetic is the canonical full name of the memory engine.
	EngineNameAutonoetic = "Rolling Autonoetic Memory Engine"
	// EngineNameZH is the canonical Chinese engine name.
	EngineNameZH = "洛灵自知记忆引擎"

	// DefaultUpstreamPort is the default local loopback service port.
	DefaultUpstreamPort = "8081"
	// DefaultDataPlanePort is the loopback port serving the signed data-plane keys.
	DefaultDataPlanePort = "8081"
	// DefaultUpstreamBaseURL is the default Autonoetic Engine /open/memory/v1 endpoint for the built-in local demo.
	DefaultUpstreamBaseURL = "http://127.0.0.1:" + DefaultUpstreamPort + "/open/memory/v1"
	// DefaultBrainDataPlanePort is the default loopback port of the data plane, which verifies
	// control-plane-issued signed keys (`mem2.*`) offline.
	DefaultBrainDataPlanePort = "8082"
	// DefaultBrainDataPlaneBaseURL is the default data-plane /open/memory/v1 endpoint of the built-in
	// local demo. Its client credential is a per-machine signed key issued by the control plane:
	// no key material is compiled into this client.
	DefaultBrainDataPlaneBaseURL = "http://127.0.0.1:" + DefaultBrainDataPlanePort + "/open/memory/v1"
	// DefaultUpstreamDevUserID is the default user_id ("10") for the built-in demo tenant.
	DefaultUpstreamDevUserID = "10"

	// EvalScopeMarker 与评测命名空间正本 internal/evalns.Marker 同源；套件取线名。
	EvalScopeMarker      = "-mem-eval-"
	evalScopeSuiteLoCoMo = "locomo"
	evalScopeSuiteATM    = "atmbench"

	// Canary* constants pin the built-in codec self-test vector shared across all SDKs and `/open/memory/v1/health`.
	CanarySecret    = "rame-codec-canary-key-v1"
	CanaryDocID     = "CANARY_V1"
	CanaryText      = "RollingMemory v1.0.1 WaveEngine_256 洛灵记忆. Zero plaintext!"
	CanaryTS        = int64(1700000000)
	CanarySlotCount = 21
	CanarySentCount = 2
	CanaryAtomsB64  = "Uk1CUAEAPA0AFAAAABUAAhQhUFBjZHiLAYwBtAHIAYQCtQJAEATBATAPB0jHISlR96QHZAI40Tt3T0fPAxxcTN+orGsN87QYgKG+M3wAaBUxyxkJx8f8zRbMt024NoH4VZEtimANzWUASZ6GWiX3Z7rb64Eq7inVwwMYS98I2I7VbHWwUFW4WszbtBQnTCgoPkEZy9pjGebXDmmqB9vwbo0DeZvaEa9TH6Ti6fHUlg3MbBKkCYTXBbqHJcBSS+ORCU+K9G0qrLWQNLboQC4oYxzk6gIUARCElBIB"
)

// VerifyCodecLocal executes an in-process round-trip self-test of the thinclient encoder and
// RMBP wire codec against the pinned canary vector without network I/O.
func VerifyCodecLocal() error {
	enc := NewBlindEncoder([]byte(CanarySecret))
	pkt, sents := enc.PackBlindPacket(CanaryDocID, CanaryText, CanaryTS)
	if len(sents) != CanarySentCount || len(pkt.Slots) != CanarySlotCount {
		return fmt.Errorf("thinclient: local codec canary count mismatch (got slots=%d sents=%d, want slots=%d sents=%d)",
			len(pkt.Slots), len(sents), CanarySlotCount, CanarySentCount)
	}
	b64, err := EncodePulsePayload(pkt)
	if err != nil {
		return fmt.Errorf("thinclient: local codec canary encode failed: %w", err)
	}
	if b64 != CanaryAtomsB64 {
		return fmt.Errorf("thinclient: local codec canary atoms_b64 drift (got %q, want %q)", b64, CanaryAtomsB64)
	}
	decAtoms, decOffsets, err := DecodePulsePayloadB64(b64)
	if err != nil {
		return fmt.Errorf("thinclient: local codec canary decode failed: %w", err)
	}
	if len(decAtoms) != len(pkt.Slots) || len(decOffsets) != len(pkt.SentenceFrames) {
		return fmt.Errorf("thinclient: local codec canary round-trip size mismatch")
	}
	for i := range pkt.Slots {
		if decAtoms[i] != pkt.Slots[i] {
			return fmt.Errorf("thinclient: local codec canary atom[%d] mismatch", i)
		}
	}
	return nil
}

// VerifyCodec runs a full bidirectional codec check against `/open/memory/v1/health`,
// sending the canonical `CanaryAtomsB64` probe so the server decodes and re-encodes it live.
func (t *HTTPBrainTransport) VerifyCodec(ctx context.Context) error {
	if err := VerifyCodecLocal(); err != nil {
		return err
	}
	if t == nil || strings.TrimSpace(t.BaseURL) == "" {
		return nil
	}
	payload, _ := json.Marshal(map[string]string{"atoms_b64": CanaryAtomsB64})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpointURL("health"), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	t.applyHeaders(req)
	httpClient := t.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("thinclient: health codec probe status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out struct {
		Status      string `json:"status"`
		CodecCanary *struct {
			SlotCount int    `json:"slot_count"`
			AtomsB64  string `json:"atoms_b64"`
		} `json:"codec_canary"`
		CodecProbe *struct {
			Verified  bool   `json:"verified"`
			SlotCount int    `json:"slot_count"`
			AtomsB64  string `json:"atoms_b64"`
		} `json:"codec_probe"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return err
	}
	if out.Status != "ok" {
		return fmt.Errorf("thinclient: unhealthy status %q", out.Status)
	}
	if out.CodecCanary != nil && out.CodecCanary.AtomsB64 != "" {
		if out.CodecCanary.AtomsB64 != CanaryAtomsB64 || out.CodecCanary.SlotCount != CanarySlotCount {
			return fmt.Errorf("thinclient: remote codec canary mismatch")
		}
	}
	if out.CodecProbe != nil {
		if !out.CodecProbe.Verified || out.CodecProbe.AtomsB64 != CanaryAtomsB64 || out.CodecProbe.SlotCount != CanarySlotCount {
			return fmt.Errorf("thinclient: remote codec probe verification failed")
		}
	}
	return nil
}

// BrainTransport abstracts the communication channel between the local Ultra-Thin Client
// and the remote Cloud Blind Brain (`/open/memory/v1/{ingest,query,distill}`).
type BrainTransport interface {
	IngestBlindPulse(ctx context.Context, pkt BlindPulsePacket) (CloudIngestResponse, error)
	QueryBlindManifold(ctx context.Context, queryPkt BlindPulsePacket, topK int) ([]CloudQueryHit, error)
}

// HTTPBrainTransport connects the local Ultra-Thin Client to a remote Cloud Blind Brain over HTTP/HTTPS.
type HTTPBrainTransport struct {
	BaseURL    string
	APIKey     string
	TenantID   string
	UserID     string
	Scope      string
	HTTPClient *http.Client
	// WireGzip selects request-body compression:
	//   0 = auto (gzip only when /health advertises `wire_encodings: ["gzip"]`;
	//       a rejection of a compressed body still falls back to plain),
	//   1 = force off,
	//   2 = force gzip (no probe, no fallback; a server without gzip decoding fails loudly).
	WireGzip int
	// WireGzipFallback is called once when auto negotiation discovers a server that
	// cannot decode gzip request bodies, so the vault can remember it.
	WireGzipFallback func()
	// AssistantID / IncarnationID / Locus identify the answering persona, its concrete
	// lifetime instance, and the workspace locus. They are stamped onto every pulse and
	// request header so unlimited parallel incarnations remain attributable across layers.
	AssistantID   string
	IncarnationID string
	Locus         string
	// CrossIncarnation opts into sibling-incarnation fan-out. Nil and false both
	// lock recall to the addressed scope; unrelated evaluation generations and
	// parallel workspaces must never be hydrated by an ordinary Harness recall.
	CrossIncarnation *bool
	// ExcludeScopeTokens lists substrings that disqualify a scope from sibling fan-out
	// (default: DefaultSiblingExcludeScopeTokens — the evaluation namespace's live shapes).
	// Passed to the engine as `exclude_scope_tokens`; nil/empty sends nothing.
	ExcludeScopeTokens []string

	wireGzipRejected atomic.Bool

	// wireGzip capability probe state (auto mode only), fetched lazily from /health.
	wireGzipMu      sync.Mutex
	wireGzipProbed  bool
	wireGzipAllowed bool

	// deliveryView holds the cognitive-cluster delivery artifacts of the most
	// recent envelope-mode query (BlindQueryOptions.ClusterGroups).
	deliveryMu   sync.Mutex
	deliveryView CloudDeliveryView
}

// parseSignedKeyClaims extracts the canonical (tenant_id, user_id) embedded in a
// `mem2.<payload_b64>.<sig>` data-plane signed key so every `/open/memory/v1/*`
// endpoint (ingest, query, units, graph, purge) resolves the identical composed scope.
func parseSignedKeyClaims(apiKey string) (tenantID, userID string) {
	trimmed := strings.TrimSpace(apiKey)
	if !strings.HasPrefix(trimmed, "mem2.") {
		return "", ""
	}
	parts := strings.Split(trimmed, ".")
	if len(parts) != 3 {
		return "", ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", ""
	}
	var claims struct {
		Tid string `json:"tid"`
		UID string `json:"uid"`
	}
	if json.Unmarshal(raw, &claims) != nil {
		return "", ""
	}
	return strings.TrimSpace(claims.Tid), strings.TrimSpace(claims.UID)
}

// DefaultSiblingExcludeScopeTokens returns the default sibling-resonance exclude list:
// 「标记 + 合法套件名」双令牌，与评测命名空间正本 internal/evalns（Marker + SuiteLoCoMo /
// SuiteATMWire）逐字对齐 —— 覆盖全部现行评测 scope 形态（`<user>-mem-eval-<suite>[-模式][-世代]`，
// 引擎落库后为 `ast:default:inc:...`），同时不误伤含 "eval" 字样的用户空间
// （`u10-mem-eval-team`）。早期裸前缀形态（`mem-eval-<x>`）不在子串令牌语义内。
func DefaultSiblingExcludeScopeTokens() []string {
	return []string{EvalScopeMarker + evalScopeSuiteLoCoMo, EvalScopeMarker + evalScopeSuiteATM}
}

// NewHTTPBrainTransport creates an HTTP transport targeting a remote Cloud Blind Brain server.
func NewHTTPBrainTransport(baseURL string, apiKey string) *HTTPBrainTransport {
	tid, uid := parseSignedKeyClaims(apiKey)
	if uid == "" {
		uid = DefaultUpstreamDevUserID
	}
	return &HTTPBrainTransport{
		BaseURL:            baseURL,
		APIKey:             apiKey,
		TenantID:           tid,
		UserID:             uid,
		ExcludeScopeTokens: DefaultSiblingExcludeScopeTokens(),
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (t *HTTPBrainTransport) effectiveUserID() string {
	if u := strings.TrimSpace(t.UserID); u != "" {
		return u
	}
	if tn := strings.TrimSpace(t.TenantID); tn != "" {
		return tn
	}
	return DefaultUpstreamDevUserID
}

func (t *HTTPBrainTransport) endpointURL(action string) string {
	base := strings.TrimRight(strings.TrimSpace(t.BaseURL), "/")
	if strings.HasSuffix(base, "/open/memory/v1") || strings.HasSuffix(base, "/brain/v1") {
		return base + "/" + action
	}
	if strings.HasSuffix(base, "/open/memory") {
		return base + "/v1/" + action
	}
	return base + "/open/memory/v1/" + action
}

func (t *HTTPBrainTransport) applyHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if t.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+t.APIKey)
	}
	if strings.TrimSpace(t.TenantID) != "" {
		req.Header.Set("X-Rolling-Memory-Tenant", strings.TrimSpace(t.TenantID))
	}
	if u := t.effectiveUserID(); u != "" {
		req.Header.Set("X-Rolling-Memory-User", u)
	}
	if strings.TrimSpace(t.Scope) != "" {
		req.Header.Set("X-Rolling-Memory-Scope", strings.TrimSpace(t.Scope))
	}
	if strings.TrimSpace(t.AssistantID) != "" {
		req.Header.Set("X-Rolling-Memory-Assistant", strings.TrimSpace(t.AssistantID))
	}
	if strings.TrimSpace(t.IncarnationID) != "" {
		req.Header.Set("X-Rolling-Memory-Incarnation", strings.TrimSpace(t.IncarnationID))
	}
	if strings.TrimSpace(t.Locus) != "" {
		req.Header.Set("X-Rolling-Memory-Locus", strings.TrimSpace(t.Locus))
	}
}

// stampIdentity fills the persona coordinate on a pulse that does not carry one yet.
func (t *HTTPBrainTransport) stampIdentity(pkt *BlindPulsePacket) {
	if strings.TrimSpace(pkt.AssistantID) == "" {
		pkt.AssistantID = strings.TrimSpace(t.AssistantID)
	}
	if strings.TrimSpace(pkt.IncarnationID) == "" {
		pkt.IncarnationID = strings.TrimSpace(t.IncarnationID)
	}
	if strings.TrimSpace(pkt.Locus) == "" {
		pkt.Locus = strings.TrimSpace(t.Locus)
	}
}

func (t *HTTPBrainTransport) IngestBlindPulse(ctx context.Context, pkt BlindPulsePacket) (CloudIngestResponse, error) {
	resp, _, err := t.ingestAttempt(ctx, pkt)
	return resp, err
}

// gzipMode returns the effective compression mode without any network I/O.
func (t *HTTPBrainTransport) gzipMode() int {
	if t.WireGzip == WireGzipOff {
		return WireGzipOff
	}
	if t.WireGzip == WireGzipForce {
		return WireGzipForce
	}
	if t.wireGzipRejected.Load() {
		return WireGzipOff
	}
	return 0 // auto
}

// gzipAdvertised reports whether the server advertises gzip request-body decoding via /health.
// Probed lazily (once per transport, only when a large body is actually about to be sent) and
// cached; any error degrades to plain bodies — compression is a pure optimization.
func (t *HTTPBrainTransport) gzipAdvertised(ctx context.Context) bool {
	t.wireGzipMu.Lock()
	defer t.wireGzipMu.Unlock()
	if !t.wireGzipProbed {
		t.wireGzipAllowed = t.probeWireEncodings(ctx)
		t.wireGzipProbed = true
	}
	return t.wireGzipAllowed
}

// probeWireEncodings fetches /health once and reports whether the server advertises gzip
// request-body decoding.
func (t *HTTPBrainTransport) probeWireEncodings(ctx context.Context) bool {
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, t.endpointURL("health"), nil)
	if err != nil {
		return false
	}
	t.applyHeaders(req)
	httpClient := t.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var health struct {
		WireEncodings []string `json:"wire_encodings"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&health); err != nil {
		return false
	}
	for _, enc := range health.WireEncodings {
		if strings.EqualFold(strings.TrimSpace(enc), "gzip") {
			return true
		}
	}
	return false
}

// ingestAttempt performs one ingest, negotiating gzip request-body compression: when a
// *compressed* body is rejected, it is re-tried uncompressed, and only when the plain retry
// succeeds is the server remembered as gzip-incapable. Uncompressed attempts never trigger this
// retry, so rejections that stem from the payload itself stay visible to the caller.
func (t *HTTPBrainTransport) ingestAttempt(ctx context.Context, pkt BlindPulsePacket) (CloudIngestResponse, int, error) {
	mode := t.gzipMode()
	if mode == WireGzipOff {
		resp, status, _, err := t.ingestOnce(ctx, pkt, WireGzipOff)
		return resp, status, err
	}
	resp, status, compressed, err := t.ingestOnce(ctx, pkt, mode)
	if err == nil {
		return resp, status, nil
	}
	// Only auto mode degrades; forced gzip keeps the failure visible.
	if t.WireGzip == WireGzipForce {
		return resp, status, err
	}
	if !compressed || !isBodyRejection(status) {
		return resp, status, err
	}
	plainResp, plainStatus, _, plainErr := t.ingestOnce(ctx, pkt, WireGzipOff)
	if plainErr != nil {
		return plainResp, plainStatus, plainErr
	}
	t.wireGzipRejected.Store(true)
	if t.WireGzipFallback != nil {
		t.WireGzipFallback()
	}
	return plainResp, plainStatus, nil
}

// isBodyRejection reports whether an ingest status means "this server cannot decode the request
// body" rather than a genuine validation failure.
func isBodyRejection(status int) bool {
	switch status {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusMethodNotAllowed,
		http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity,
		http.StatusInternalServerError, http.StatusNotImplemented:
		return true
	}
	return false
}

func (t *HTTPBrainTransport) ingestOnce(ctx context.Context, pkt BlindPulsePacket, gzipMode int) (CloudIngestResponse, int, bool, error) {
	if strings.TrimSpace(pkt.UserID) == "" {
		pkt.UserID = t.effectiveUserID()
	}
	if strings.TrimSpace(pkt.TenantID) == "" && strings.TrimSpace(t.TenantID) != "" {
		pkt.TenantID = strings.TrimSpace(t.TenantID)
	}
	if strings.TrimSpace(pkt.Scope) == "" && strings.TrimSpace(t.Scope) != "" {
		pkt.Scope = strings.TrimSpace(t.Scope)
	}
	t.stampIdentity(&pkt)
	// The pulse payload is the single on-wire encoding: atoms plus sentence boundaries travel as
	// one compact, lossless, zero-plaintext column that the server expands back to the same slots.
	payloadB64, err := EncodePulsePayload(pkt)
	if err != nil {
		return CloudIngestResponse{}, 0, false, err
	}
	pkt.AtomsB64 = payloadB64
	pkt.Slots = nil
	pkt.SentenceFrames = nil
	body, err := json.Marshal(pkt)
	if err != nil {
		return CloudIngestResponse{}, 0, false, err
	}
	compressed := false
	if gzipMode != WireGzipOff && len(body) >= wireGzipMinBytes {
		allow := gzipMode == WireGzipForce || t.gzipAdvertised(ctx)
		if allow {
			if gz, err := EncodeWireGzip(body); err == nil {
				body = gz
				compressed = true
			}
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpointURL("ingest"), bytes.NewReader(body))
	if err != nil {
		return CloudIngestResponse{}, 0, false, err
	}
	t.applyHeaders(req)
	if compressed {
		req.Header.Set("Content-Encoding", "gzip")
	}
	httpClient := t.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return CloudIngestResponse{}, 0, compressed, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return CloudIngestResponse{}, resp.StatusCode, compressed, fmt.Errorf("thinclient: cloud ingest status %d: %s", resp.StatusCode, string(b))
	}
	var out CloudIngestResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return CloudIngestResponse{}, resp.StatusCode, compressed, err
	}
	return out, resp.StatusCode, compressed, nil
}

// DeleteUnits purges remote units in bounded batches using /open/memory/v1/purge.
func (t *HTTPBrainTransport) DeleteUnits(ctx context.Context, unitIDs []string) error {
	if len(unitIDs) == 0 {
		return nil
	}
	if t == nil {
		return fmt.Errorf("thinclient: nil HTTPBrainTransport")
	}
	const maxBatchSize = 100
	for start := 0; start < len(unitIDs); start += maxBatchSize {
		end := start + maxBatchSize
		if end > len(unitIDs) {
			end = len(unitIDs)
		}
		reqPayload := struct {
			TenantID string   `json:"tenant_id,omitempty"`
			Scope    string   `json:"scope,omitempty"`
			UserID   string   `json:"user_id"`
			Mode     string   `json:"mode"`
			UnitIDs  []string `json:"unit_ids"`
		}{
			TenantID: strings.TrimSpace(t.TenantID),
			Scope:    strings.TrimSpace(t.Scope),
			UserID:   t.effectiveUserID(),
			Mode:     "units",
			UnitIDs:  unitIDs[start:end],
		}
		body, err := json.Marshal(reqPayload)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpointURL("purge"), bytes.NewReader(body))
		if err != nil {
			return err
		}
		t.applyHeaders(req)
		httpClient := t.HTTPClient
		if httpClient == nil {
			httpClient = http.DefaultClient
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			return err
		}
		responseBody, readErr := io.ReadAll(resp.Body)
		closeErr := resp.Body.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			return fmt.Errorf("thinclient: cloud purge status %d: %s", resp.StatusCode, string(responseBody))
		}
		if len(bytes.TrimSpace(responseBody)) == 0 {
			continue
		}
		var out struct {
			Success *bool `json:"success"`
		}
		if err := json.Unmarshal(responseBody, &out); err != nil {
			return fmt.Errorf("thinclient: decode cloud purge response: %w", err)
		}
		if out.Success != nil && !*out.Success {
			return fmt.Errorf("thinclient: cloud purge failed: %s", string(responseBody))
		}
	}
	return nil
}

func (t *HTTPBrainTransport) DistillBlindDocument(ctx context.Context, reqPayload BlindDistillRequest) (CloudIngestResponse, error) {
	if strings.TrimSpace(reqPayload.UserID) == "" {
		reqPayload.UserID = t.effectiveUserID()
	}
	if strings.TrimSpace(reqPayload.TenantID) == "" && strings.TrimSpace(t.TenantID) != "" {
		reqPayload.TenantID = strings.TrimSpace(t.TenantID)
	}
	if strings.TrimSpace(reqPayload.Scope) == "" && strings.TrimSpace(t.Scope) != "" {
		reqPayload.Scope = strings.TrimSpace(t.Scope)
	}
	t.stampIdentity(&reqPayload.Document)
	if reqPayload.Document.AtomsB64 == "" && (len(reqPayload.Document.Slots) > 0 || len(reqPayload.Document.SentenceFrames) > 0) {
		b64, err := EncodePulsePayload(reqPayload.Document)
		if err != nil {
			return CloudIngestResponse{}, err
		}
		reqPayload.Document.AtomsB64 = b64
		reqPayload.Document.Slots = nil
		reqPayload.Document.SentenceFrames = nil
	}
	if reqPayload.Query != nil {
		q := *reqPayload.Query
		t.stampIdentity(&q)
		if q.AtomsB64 == "" && (len(q.Slots) > 0 || len(q.SentenceFrames) > 0) {
			b64, err := EncodePulsePayload(q)
			if err != nil {
				return CloudIngestResponse{}, err
			}
			q.AtomsB64 = b64
			q.Slots = nil
			q.SentenceFrames = nil
		}
		reqPayload.Query = &q
	}
	body, err := json.Marshal(reqPayload)
	if err != nil {
		return CloudIngestResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpointURL("distill"), bytes.NewReader(body))
	if err != nil {
		return CloudIngestResponse{}, err
	}
	t.applyHeaders(req)
	resp, err := t.HTTPClient.Do(req)
	if err != nil {
		return CloudIngestResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return CloudIngestResponse{}, fmt.Errorf("thinclient: cloud distill status %d: %s", resp.StatusCode, string(b))
	}
	var out CloudIngestResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return CloudIngestResponse{}, err
	}
	return out, nil
}

func (t *HTTPBrainTransport) QueryBlindManifold(ctx context.Context, queryPkt BlindPulsePacket, topK int) ([]CloudQueryHit, error) {
	return t.QueryBlindManifoldWithOptions(ctx, queryPkt, BlindQueryOptions{TopK: topK})
}

func (t *HTTPBrainTransport) QueryBlindManifoldWithOptions(ctx context.Context, queryPkt BlindPulsePacket, opts BlindQueryOptions) ([]CloudQueryHit, error) {
	if strings.TrimSpace(queryPkt.UserID) == "" {
		queryPkt.UserID = t.effectiveUserID()
	}
	t.stampIdentity(&queryPkt)
	if queryPkt.AtomsB64 == "" && (len(queryPkt.Slots) > 0 || len(queryPkt.SentenceFrames) > 0) {
		payloadB64, err := EncodePulsePayload(queryPkt)
		if err != nil {
			return nil, err
		}
		queryPkt.AtomsB64 = payloadB64
		queryPkt.Slots = nil
		queryPkt.SentenceFrames = nil
	}
	reqPayload := struct {
		TenantID           string             `json:"tenant_id,omitempty"`
		Scope              string             `json:"scope,omitempty"`
		UserID             string             `json:"user_id"`
		AssistantID        string             `json:"assistant_id,omitempty"`
		IncarnationID      string             `json:"incarnation_id,omitempty"`
		Query              BlindPulsePacket   `json:"query"`
		Layers             []string           `json:"layers,omitempty"`
		LayerWeights       map[string]float64 `json:"layer_weights,omitempty"`
		DeliveryBudget     string             `json:"delivery_budget,omitempty"`
		TopK               int                `json:"top_k"`
		SpreadRadius       *int               `json:"spread_radius,omitempty"`
		SpreadGain         *float64           `json:"spread_gain,omitempty"`
		DenoiseRatio       *float64           `json:"denoise_ratio,omitempty"`
		MinKeepSentences   int                `json:"min_keep_sentences,omitempty"`
		AsOf               int64              `json:"as_of,omitempty"`
		TimeRef            int64              `json:"time_ref,omitempty"`
		WaveRadix          int                `json:"wave_radix,omitempty"`
		CrossIncarnation   bool               `json:"cross_incarnation"`
		ExcludeScopeTokens []string           `json:"exclude_scope_tokens,omitempty"`
		ClusterGroups      bool               `json:"cluster_groups,omitempty"`
	}{
		TenantID:           strings.TrimSpace(t.TenantID),
		Scope:              strings.TrimSpace(t.Scope),
		UserID:             t.effectiveUserID(),
		AssistantID:        strings.TrimSpace(t.AssistantID),
		IncarnationID:      strings.TrimSpace(t.IncarnationID),
		Query:              queryPkt,
		Layers:             opts.Layers,
		LayerWeights:       opts.LayerWeights,
		DeliveryBudget:     strings.TrimSpace(opts.DeliveryBudget),
		TopK:               opts.TopK,
		SpreadRadius:       opts.SpreadRadius,
		SpreadGain:         opts.SpreadGain,
		DenoiseRatio:       opts.DenoiseRatio,
		MinKeepSentences:   opts.MinKeepSentences,
		AsOf:               opts.AsOf,
		TimeRef:            opts.TimeRef,
		WaveRadix:          opts.WaveRadix,
		CrossIncarnation:   t.CrossIncarnation != nil && *t.CrossIncarnation,
		ExcludeScopeTokens: t.ExcludeScopeTokens,
		ClusterGroups:      opts.ClusterGroups,
	}
	body, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, err
	}
	endpoint := t.endpointURL("query")
	if opts.ClusterGroups {
		// The delivery view lives on the envelope object shell; the default
		// bare-array mode stays untouched for every other caller.
		endpoint += "?envelope=1"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	t.applyHeaders(req)
	resp, err := t.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("thinclient: cloud query status %d: %s", resp.StatusCode, string(b))
	}
	var out []CloudQueryHit
	if opts.ClusterGroups {
		var env struct {
			Hits              []CloudQueryHit         `json:"hits"`
			ClusterGroups     []CloudHitClusterGroup  `json:"cluster_groups,omitempty"`
			OrdinalAnnotation *CloudOrdinalAnnotation `json:"ordinal_annotation,omitempty"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
			return nil, err
		}
		t.setDeliveryView(CloudDeliveryView{ClusterGroups: env.ClusterGroups, OrdinalAnnotation: env.OrdinalAnnotation})
		return env.Hits, nil
	}
	t.setDeliveryView(CloudDeliveryView{})
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// setDeliveryView records the cluster delivery artifacts of the most recent
// envelope-mode query (cleared on bare-array queries so stale views never leak
// across calls).
func (t *HTTPBrainTransport) setDeliveryView(v CloudDeliveryView) {
	t.deliveryMu.Lock()
	t.deliveryView = v
	t.deliveryMu.Unlock()
}

// DeliveryView returns the cognitive-cluster delivery view captured by the most
// recent query issued with BlindQueryOptions.ClusterGroups.
func (t *HTTPBrainTransport) DeliveryView() CloudDeliveryView {
	t.deliveryMu.Lock()
	defer t.deliveryMu.Unlock()
	return t.deliveryView
}

// BuildBlindGraph calls the remote `/open/memory/v1/graph` endpoint to fetch source-agnostic topology
// (dynamic Cell containers, units across background/episodic/noetic layers, and cross-cell/cites edges).
func (t *HTTPBrainTransport) BuildBlindGraph(ctx context.Context, reqPayload BlindGraphRequest) (CloudGraphResponse, error) {
	if strings.TrimSpace(reqPayload.UserID) == "" {
		reqPayload.UserID = t.effectiveUserID()
	}
	if strings.TrimSpace(reqPayload.TenantID) == "" && strings.TrimSpace(t.TenantID) != "" {
		reqPayload.TenantID = strings.TrimSpace(t.TenantID)
	}
	if strings.TrimSpace(reqPayload.Scope) == "" && strings.TrimSpace(t.Scope) != "" {
		reqPayload.Scope = strings.TrimSpace(t.Scope)
	}
	body, err := json.Marshal(reqPayload)
	if err != nil {
		return CloudGraphResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpointURL("graph"), bytes.NewReader(body))
	if err != nil {
		return CloudGraphResponse{}, err
	}
	t.applyHeaders(req)
	resp, err := t.HTTPClient.Do(req)
	if err != nil {
		return CloudGraphResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return CloudGraphResponse{}, fmt.Errorf("thinclient: cloud graph status %d: %s", resp.StatusCode, string(b))
	}
	var out CloudGraphResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return CloudGraphResponse{}, err
	}
	return out, nil
}

// ListUnits fetches the authoritative, complete list of unit IDs in the scope
// for integrity verification, bypassing topological graph limits.
func (t *HTTPBrainTransport) ListUnits(ctx context.Context, scope string, layers []string) ([]string, error) {
	if strings.TrimSpace(scope) == "" {
		scope = strings.TrimSpace(t.Scope)
	}
	reqPayload := struct {
		TenantID string   `json:"tenant_id,omitempty"`
		UserID   string   `json:"user_id"`
		Scope    string   `json:"scope,omitempty"`
		Layers   []string `json:"layers,omitempty"`
	}{
		TenantID: strings.TrimSpace(t.TenantID),
		UserID:   t.effectiveUserID(),
		Scope:    scope,
		Layers:   layers,
	}
	body, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpointURL("units"), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	t.applyHeaders(req)
	httpClient := t.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("thinclient: cloud units status %d: %s", resp.StatusCode, string(b))
	}
	var out struct {
		UnitIDs []string `json:"unit_ids"`
		Units   []string `json:"units"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.UnitIDs) > 0 {
		return out.UnitIDs, nil
	}
	return out.Units, nil
}

// Client is the top-level Ultra-Thin Client coordinating any user-chosen PlaintextStore
// (SQLite Vault by default, or JSONFileStore, DirTextStore, MySQL, PostgreSQL, etc.)
// with the remote Cloud Blind Brain.
type Client struct {
	Vault     *Vault
	Store     PlaintextStore
	Transport BrainTransport
	// AssistantID / IncarnationID / Locus are the persona coordinate stamped onto every
	// pulse and onto each locally saved row. Unlimited parallel incarnations therefore stay
	// attributable without widening the memory envelope; empty values simply inherit the
	// transport-level identity.
	AssistantID   string
	IncarnationID string
	Locus         string

	// recallMu guards lastRecall, the outcome of the most recent Recall call, so a caller
	// can distinguish "memory holds nothing" from "the cloud manifold was unavailable".
	recallMu   sync.Mutex
	lastRecall RecallDiagnostics
}

// SetIdentity records the persona coordinate on the client, its local vault and (when the
// transport supports it) the transport, so pulses, headers and local rows agree.
func (c *Client) SetIdentity(assistantID, incarnationID, locus string) {
	if c == nil {
		return
	}
	c.AssistantID = strings.TrimSpace(assistantID)
	c.IncarnationID = strings.TrimSpace(incarnationID)
	c.Locus = strings.TrimSpace(locus)
	if c.Vault != nil {
		c.Vault.SetIdentity(VaultIdentity{
			AssistantID: c.AssistantID, IncarnationID: c.IncarnationID, Locus: c.Locus,
		})
	}
	if transport, ok := c.Transport.(*HTTPBrainTransport); ok {
		transport.AssistantID = c.AssistantID
		transport.IncarnationID = c.IncarnationID
		transport.Locus = c.Locus
	}
}

// stampIdentity fills the persona coordinate on a pulse that does not carry one yet.
func (c *Client) stampIdentity(pkt *BlindPulsePacket) {
	if strings.TrimSpace(pkt.AssistantID) == "" {
		pkt.AssistantID = c.AssistantID
	}
	if strings.TrimSpace(pkt.IncarnationID) == "" {
		pkt.IncarnationID = c.IncarnationID
	}
	if strings.TrimSpace(pkt.Locus) == "" {
		pkt.Locus = c.Locus
	}
}

// NewClient constructs a production Ultra-Thin Client backed by the default local SQLite Vault and a BrainTransport.
func NewClient(vault *Vault, transport BrainTransport) *Client {
	return &Client{
		Vault:     vault,
		Store:     vault,
		Transport: transport,
	}
}

// NewLocalClient constructs a local_plain client backed by the local SQLite Vault
// and its in-process LocalVaultTransport (zero network egress).
func NewLocalClient(vault *Vault) *Client {
	return NewClient(vault, NewLocalVaultTransport(vault))
}

// NewClientWithStore constructs an Ultra-Thin Client backed by ANY custom PlaintextStore
// (e.g., JSONFileStore, DirTextStore, MySQL, PostgreSQL, Redis, or custom business tables).
func NewClientWithStore(store PlaintextStore, transport BrainTransport) *Client {
	v, _ := store.(*Vault)
	return &Client{
		Vault:     v,
		Store:     store,
		Transport: transport,
	}
}

func (c *Client) activeStore() PlaintextStore {
	if c.Store != nil {
		return c.Store
	}
	return c.Vault
}

// Remember stores a user Harness dialogue turn in `LayerEpisodic` ("episodic", the default conversational stream),
// transmits ONLY the blind `{h, b, f}` integer frame to the Cloud Blind Brain, and records the cloud-assigned
// C6 address & dry-sentence indices locally.
func (c *Client) Remember(ctx context.Context, docID string, plaintext string, occurredAt int64) (CloudIngestResponse, error) {
	return c.RememberWithLayer(ctx, docID, plaintext, occurredAt, LayerEpisodic, nil)
}

// RememberEpisodic explicitly stores a user Harness conversation turn in `LayerEpisodic` ("episodic").
func (c *Client) RememberEpisodic(ctx context.Context, docID string, plaintext string, occurredAt int64) (CloudIngestResponse, error) {
	return c.RememberWithLayer(ctx, docID, plaintext, occurredAt, LayerEpisodic, nil)
}

// localIngestResponse preserves every sentence before any network write. The
// local plaintext vault is the source of truth; the cloud address is a derived
// index that may be backfilled on a later reconcile.
func localIngestResponse(docID, layer string, sentenceCount int) CloudIngestResponse {
	kept := make([]int, sentenceCount)
	for i := range kept {
		kept[i] = i
	}
	return CloudIngestResponse{UnitID: docID, Layer: layer, KeptSentenceIndices: kept}
}

// RememberEpisodicByRef transmits the blind `{h, b, f}` pulse of `livePlaintext` to `/open/memory/v1/ingest`
// (`layer="episodic"`) and records a verified zero-copy pointer in local SQLite.
// If the pointer cannot be verified, the supplied plaintext is preserved locally instead.
//
// `turnRef` is the provenance of the unit (a content-addressed unit can be said in
// several turns): the vault row keeps one pointer, the lineage table keeps every
// occurrence. Chunked units pass an empty refPointer, so their text is stored locally.
func (c *Client) RememberEpisodicByRef(ctx context.Context, docID, refPointer, turnRef, livePlaintext string, occurredAt int64) (CloudIngestResponse, error) {
	st := c.activeStore()
	pkt, sents := st.PackBlindPacket(docID, livePlaintext, occurredAt)
	pkt.Layer = LayerEpisodic
	c.stampIdentity(&pkt)
	usePointer := c.Vault != nil && strings.HasPrefix(strings.TrimSpace(refPointer), "@ref:")
	invalidPointer := false
	saveLocal := func(resp CloudIngestResponse) error {
		if usePointer {
			if err := c.Vault.SaveLocalEpisodicPointerIndexed(docID, occurredAt, refPointer, turnRef, livePlaintext, pkt, resp); err == nil {
				return nil
			} else if verifyEpisodicPointerSource(refPointer, livePlaintext) == nil {
				var sameInline int
				checkErr := c.Vault.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM thin_vault_docs d WHERE d.doc_id = ? AND d.raw_text = ? AND (d.layer = 'episodic' OR d.layer = '') AND NOT EXISTS (SELECT 1 FROM thin_vault_episodic_index i WHERE i.doc_id = d.doc_id) AND NOT EXISTS (SELECT 1 FROM thin_vault_episodic_lineage l WHERE l.doc_id = d.doc_id AND l.turn_ref = d.raw_text))`, docID, livePlaintext).Scan(&sameInline)
				if checkErr != nil {
					return checkErr
				}
				if sameInline != 0 && livePlaintext != strings.TrimSpace(refPointer) {
					return nil
				}
				return err
			}
			usePointer = false
			invalidPointer = true
			turnRef = ""
		}
		if invalidPointer {
			sentsJSON, err := json.Marshal(sents)
			if err != nil {
				return err
			}
			keptJSON, err := json.Marshal(resp.KeptSentenceIndices)
			if err != nil {
				return err
			}
			// Never replace a different source, even if another writer claims this ID.
			result, err := c.Vault.db.Exec(`INSERT INTO thin_vault_docs(doc_id, layer, occurred_at, inserted_at, raw_text, sentences_json, address_uri, coord64, waveform_bar, doc_macro_wave, kept_indices_json, assistant_id, incarnation_id, locus) VALUES (?, 'episodic', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(doc_id) DO UPDATE SET occurred_at=excluded.occurred_at, inserted_at=CASE WHEN thin_vault_docs.inserted_at > 0 THEN thin_vault_docs.inserted_at ELSE excluded.inserted_at END, sentences_json=excluded.sentences_json, address_uri=excluded.address_uri, coord64=excluded.coord64, waveform_bar=excluded.waveform_bar, doc_macro_wave=excluded.doc_macro_wave, kept_indices_json=excluded.kept_indices_json, assistant_id=excluded.assistant_id, incarnation_id=excluded.incarnation_id, locus=excluded.locus WHERE thin_vault_docs.raw_text=excluded.raw_text`, docID, occurredAt, occurredAt, livePlaintext, string(sentsJSON), resp.AddressURI, int64(resp.AddressCoord64), resp.WaveformBar, resp.DocMacroWaveform, string(keptJSON), c.Vault.identity.AssistantID, c.Vault.identity.IncarnationID, c.Vault.identity.Locus)
			if err != nil {
				return err
			}
			changed, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if changed == 0 {
				return fmt.Errorf("thinclient: episodic document already exists with different source")
			}
			c.Vault.clearSlotCache()
			if c.Vault.hasFTS5 {
				if ftsErr := ftsReplaceRow(c.Vault.db, docID, livePlaintext); ftsErr != nil {
					return fmt.Errorf("thinclient: FTS mirror write for %s: %w", docID, ftsErr)
				}
			}
			return nil
		}
		if err := st.SaveLocalPlaintext(docID, occurredAt, livePlaintext, sents, resp); err != nil {
			return err
		}
		if c.Vault != nil {
			_ = c.Vault.RecordEpisodicLineage(turnRef, docID, occurredAt)
		}
		return nil
	}
	localResp := localIngestResponse(docID, LayerEpisodic, len(sents))
	if err := saveLocal(localResp); err != nil {
		return CloudIngestResponse{}, err
	}
	if c.Transport == nil {
		return localResp, nil
	}
	resp, err := c.Transport.IngestBlindPulse(ctx, pkt)
	if err != nil {
		return localResp, err
	}
	if strings.TrimSpace(resp.Layer) == "" {
		resp.Layer = LayerEpisodic
	}
	if err := saveLocal(resp); err != nil {
		return CloudIngestResponse{}, err
	}
	return resp, nil
}

// RememberBackground explicitly stores a knowledge-base / codebase / document substrate item in `LayerBackground` ("background").
func (c *Client) RememberBackground(ctx context.Context, docID string, plaintext string, occurredAt int64) (CloudIngestResponse, error) {
	return c.RememberWithLayer(ctx, docID, plaintext, occurredAt, LayerBackground, nil)
}

// RememberWithLayer stores the plaintext in the user's chosen PlaintextStore and transmits the blind `{h, b, f}`
// frame to the `/open/memory/v1/ingest` endpoint with an explicit cognitive layer
// (`"background" | "episodic" | "noetic"`).
func (c *Client) RememberWithLayer(ctx context.Context, docID string, plaintext string, occurredAt int64, layer string, citedUnitIDs []string) (CloudIngestResponse, error) {
	st := c.activeStore()
	pkt, sents := st.PackBlindPacket(docID, plaintext, occurredAt)
	if strings.TrimSpace(layer) != "" {
		pkt.Layer = strings.TrimSpace(layer)
	} else {
		pkt.Layer = "episodic"
	}
	if len(citedUnitIDs) > 0 {
		pkt.CitedUnitIDs = citedUnitIDs
	}
	c.stampIdentity(&pkt)
	localResp := localIngestResponse(docID, pkt.Layer, len(sents))
	if err := st.SaveLocalPlaintext(docID, occurredAt, plaintext, sents, localResp); err != nil {
		return CloudIngestResponse{}, err
	}
	if c.Transport == nil {
		return localResp, nil
	}
	resp, err := c.Transport.IngestBlindPulse(ctx, pkt)
	if err != nil {
		return localResp, err
	}
	if strings.TrimSpace(resp.Layer) == "" {
		resp.Layer = pkt.Layer
	}
	if err := st.SaveLocalPlaintext(docID, occurredAt, plaintext, sents, resp); err != nil {
		return CloudIngestResponse{}, err
	}
	return resp, nil
}

// RecallDiagnostics reports how the last Recall on this client was served. Degraded means
// the cloud manifold was unavailable and the answer came from the local lexical fallback,
// so a caller must never read an empty result as "memory holds nothing".
type RecallDiagnostics struct {
	Degraded bool
	Reason   string
}

// RecallDiagnostics returns the outcome of the most recent Recall call.
func (c *Client) RecallDiagnostics() RecallDiagnostics {
	if c == nil {
		return RecallDiagnostics{}
	}
	c.recallMu.Lock()
	defer c.recallMu.Unlock()
	return c.lastRecall
}

func (c *Client) setRecallDiagnostics(d RecallDiagnostics) {
	if c == nil {
		return
	}
	c.recallMu.Lock()
	c.lastRecall = d
	c.recallMu.Unlock()
}

// Recall converts the query into a blind `{h, b, f}` frame, asks the Cloud Blind Brain for ranked UnitIDs
// and weights (via Wave resonance + C6 Cell Wormhole + KNN Manifold), fuses/supplements with the built-in
// Client SDK Default Recall (`SearchClientSDKDefault` over local SQLite FTS5 + temporal window), and resolves
// the plaintext locally from the user's PlaintextStore!
func (c *Client) Recall(ctx context.Context, queryPlaintext string, topK int) ([]LocalResolvedHit, error) {
	return c.RecallWithOptions(ctx, queryPlaintext, BlindQueryOptions{TopK: topK})
}

// RecallWithOptions executes a full cloud + local fallback recall while forwarding explicit
// temporal horizon (AsOf / TimeRef) and distillation options to the Cloud Blind Brain.
func (c *Client) RecallWithOptions(ctx context.Context, queryPlaintext string, opts BlindQueryOptions) ([]LocalResolvedHit, error) {
	topK := opts.TopK
	if topK <= 0 {
		topK = 10
	}
	opts.TopK = topK
	st := c.activeStore()
	if c.Transport == nil {
		c.setRecallDiagnostics(RecallDiagnostics{Degraded: true, Reason: "no_transport"})
		return c.RecallDefault(queryPlaintext, topK)
	}
	qPkt, _ := st.PackBlindPacket("QUERY", queryPlaintext, opts.TimeRef)
	var cloudHits []CloudQueryHit
	var err error
	if httpTr, ok := c.Transport.(*HTTPBrainTransport); ok {
		cloudHits, err = httpTr.QueryBlindManifoldWithOptions(ctx, qPkt, opts)
	} else if localTr, ok := c.Transport.(*LocalVaultTransport); ok {
		localQPkt, _ := st.PackBlindPacket("QUERY", queryPlaintext, 0)
		localQPkt.Scope = qPkt.Scope
		cloudHits, err = localTr.QueryBlindManifold(ctx, localQPkt, 0)
	} else {
		cloudHits, err = c.Transport.QueryBlindManifold(ctx, qPkt, topK)
	}
	if err != nil {
		if _, local := c.Transport.(*LocalVaultTransport); local {
			return nil, err
		}
		if c.Vault != nil {
			if fallback, fErr := c.RecallDefault(queryPlaintext, topK); fErr == nil && len(fallback) > 0 {
				c.setRecallDiagnostics(RecallDiagnostics{Degraded: true, Reason: "engine_error_fallback_local"})
				return fallback, nil
			}
		}
		c.setRecallDiagnostics(RecallDiagnostics{Degraded: true, Reason: "engine_error"})
		return nil, err
	}
	c.setRecallDiagnostics(RecallDiagnostics{})
	_, local := c.Transport.(*LocalVaultTransport)
	if local && c.Vault != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		localCap := topK
		if localCap < 20 {
			localCap = 20
		}
		localHits, _ := c.Vault.SearchClientSDKDefault(queryPlaintext, localCap)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(localHits) > 0 {
			maxCloudWeight := 1.0
			for _, ch := range cloudHits {
				if ch.FinalWeight > maxCloudWeight {
					maxCloudWeight = ch.FinalWeight
				}
			}
			localWeightByID := make(map[string]float64, len(localHits))
			topLocalScore := localHits[0].FinalWeight
			if topLocalScore <= 0 {
				topLocalScore = 1.0
			}
			for _, lh := range localHits {
				localWeightByID[lh.UnitID] = lh.FinalWeight
			}
			seenCand := make(map[string]bool, len(cloudHits)+len(localHits))
			mergedCands := make([]CloudQueryHit, 0, len(cloudHits)+len(localHits))
			for _, ch := range cloudHits {
				seenCand[ch.UnitID] = true
				if localW, inLocal := localWeightByID[ch.UnitID]; inLocal {
					sdkNorm := localW / topLocalScore
					energyBoost := 1.0 + 0.368*sdkNorm
					ch.FinalWeight = maxCloudWeight * (1.0 + sdkNorm) * energyBoost
				} else {
					ch.FinalWeight *= 0.65
				}
				mergedCands = append(mergedCands, ch)
			}
			addedLocal := 0
			for _, lh := range localHits {
				if seenCand[lh.UnitID] || addedLocal >= topK {
					continue
				}
				seenCand[lh.UnitID] = true
				addedLocal++
				sdkNorm := lh.FinalWeight / topLocalScore
				energyBoost := 1.0 + 0.368*sdkNorm
				lh.FinalWeight = maxCloudWeight * (1.0 + sdkNorm) * energyBoost
				mergedCands = append(mergedCands, lh)
			}
			const headSortCap = 256
			if len(mergedCands) > headSortCap && topK <= headSortCap/2 {
				sortTopCloudHits(mergedCands, headSortCap)
			} else {
				sort.SliceStable(mergedCands, func(i, j int) bool {
					return cloudHitLess(mergedCands[i], mergedCands[j])
				})
			}
			cloudHits = mergedCands
		}
		resolved := make([]LocalResolvedHit, 0, topK+8)
		usableLocal := 0
		tailSorted := false
		hydrateStart := time.Now()
		maxProbes := topK * 4
		if maxProbes < 64 {
			maxProbes = 64
		}
		for i := 0; i < len(cloudHits) && i < maxProbes; i++ {
			if i == 256 && !tailSorted && len(cloudHits) > 256 {
				tailSorted = true
				sort.SliceStable(cloudHits[256:], func(a, b int) bool {
					return cloudHitLess(cloudHits[256+a], cloudHits[256+b])
				})
			}
			if usableLocal >= 4 && time.Since(hydrateStart) > 1500*time.Millisecond {
				break
			}
			ch := cloudHits[i]
			if err := ctx.Err(); err != nil {
				if usableLocal > 0 {
					break
				}
				return nil, err
			}
			rawTxt, distTxt, keptSents, err := st.LoadLocalDocument(ch.UnitID)
			if err != nil {
				resolved = append(resolved, LocalResolvedHit{CloudQueryHit: ch, SourceUnavailable: true})
				continue
			}
			usableLocal++
			if len(ch.KeptSentenceIndices) > 0 {
				allSents := SplitSentences(rawTxt)
				if len(allSents) > len(ch.KeptSentenceIndices) {
					distTxt, keptSents = ReconstructDistilled(rawTxt, allSents, ch.KeptSentenceIndices)
				}
			}
			resolved = append(resolved, LocalResolvedHit{
				CloudQueryHit:      ch,
				RawPlaintext:       rawTxt,
				DistilledPlaintext: distTxt,
				KeptSentences:      keptSents,
				Authors:            RoleTaggedAuthors(rawTxt),
			})
			if i+1 >= topK && usableLocal >= topK {
				break
			}
		}
		return limitHydratedLocalHits(resolved, topK), nil
	}

	seen := make(map[string]bool, len(cloudHits)*2)
	resolved := make([]LocalResolvedHit, 0, len(cloudHits)*2)
	cloudRankByID := make(map[string]int, len(cloudHits))
	maxCloudWeight := 1.0
	usableLocal := 0
	// Missing sources remain in the ranked prefix for gap reporting, but do not
	// consume its usable-hit budget. The transport has already sorted all matches.
	for i, ch := range cloudHits {
		if local {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		rawTxt, distTxt, keptSents, err := st.LoadLocalDocument(ch.UnitID)
		if err != nil {
			resolved = append(resolved, LocalResolvedHit{CloudQueryHit: ch, SourceUnavailable: true})
			seen[ch.UnitID] = true
			continue
		}
		if local {
			usableLocal++
		}
		if len(ch.KeptSentenceIndices) > 0 {
			allSents := SplitSentences(rawTxt)
			if len(allSents) > len(ch.KeptSentenceIndices) {
				distTxt, keptSents = ReconstructDistilled(rawTxt, allSents, ch.KeptSentenceIndices)
			}
		}
		seen[ch.UnitID] = true
		cloudRankByID[ch.UnitID] = len(resolved) + 1
		if ch.FinalWeight > maxCloudWeight {
			maxCloudWeight = ch.FinalWeight
		}
		resolved = append(resolved, LocalResolvedHit{
			CloudQueryHit:      ch,
			RawPlaintext:       rawTxt,
			DistilledPlaintext: distTxt,
			KeptSentences:      keptSents,
			Authors:            RoleTaggedAuthors(rawTxt),
		})
		if local && i+1 >= topK && usableLocal >= topK {
			break
		}
	}
	if c.Vault != nil {
		localCap := topK
		if !local && localCap > 6 {
			localCap = 6
		} else if local && localCap < 20 {
			localCap = 20
		}
		if local {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if localHits, lErr := c.Vault.SearchClientSDKDefault(queryPlaintext, localCap); lErr == nil && len(localHits) > 0 {
			if local {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			localWeightByID := make(map[string]float64, len(localHits))
			topLocalScore := localHits[0].FinalWeight
			if topLocalScore <= 0 {
				topLocalScore = 1.0
			}
			addedLocalOnly := 0
			maxLocalAdmit := 2
			if local {
				maxLocalAdmit = topK
			}
			for _, lh := range localHits {
				if local {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
				}
				localWeightByID[lh.UnitID] = lh.FinalWeight
				if seen[lh.UnitID] {
					continue
				}
				// When remote cloud hits exist, only admit up to 2 high-confidence local-only L1/L2 units
				// (within 75% of the top local score) so local background chunks supplement rather than flood.
				// In local_plain mode, SearchClientSDKDefault is the primary Shannon/BM25/event-chain ranker.
				if !local && len(cloudRankByID) > 0 && (addedLocalOnly >= maxLocalAdmit || lh.FinalWeight < topLocalScore*0.75) {
					continue
				}
				if local && addedLocalOnly >= maxLocalAdmit {
					continue
				}
				rawTxt, distTxt, keptSents, err := st.LoadLocalDocument(lh.UnitID)
				if err != nil {
					resolved = append(resolved, LocalResolvedHit{CloudQueryHit: lh, SourceUnavailable: true})
					seen[lh.UnitID] = true
					continue
				}
				seen[lh.UnitID] = true
				addedLocalOnly++
				resolved = append(resolved, LocalResolvedHit{
					CloudQueryHit:      lh,
					RawPlaintext:       rawTxt,
					DistilledPlaintext: distTxt,
					KeptSentences:      keptSents,
					Authors:            RoleTaggedAuthors(rawTxt),
				})
			}
			// Dual-path Amplitude-Preserving Reciprocal Rank Fusion:
			// - In remote cloud mode, the Cloud the memory engine manifold is primary and local FTS5/slots
			//   provide a bounded consensus boost.
			// - In local_plain mode (`local == true`), `SearchClientSDKDefault` (Shannon surprisal +
			//   co-occurrence coherence + sentence wave-packet + FTS5 RRF + event-chain resonance) is
			//   the authoritative relevance ranker, while `QueryBlindManifold` supplies exhaustive
			//   candidate recall and starvation/gap resilience.
			for i := range resolved {
				uid := resolved[i].UnitID
				_, inCloud := cloudRankByID[uid]
				_, inLocal := localWeightByID[uid]
				if local {
					if inLocal {
						sdkNorm := localWeightByID[uid] / topLocalScore
						energyBoost := 1.0 + 0.368*sdkNorm
						resolved[i].FinalWeight = maxCloudWeight * (1.0 + sdkNorm) * energyBoost
					} else {
						resolved[i].FinalWeight *= 0.65
					}
				} else if inCloud && inLocal {
					sdkNorm := localWeightByID[uid] / topLocalScore
					resolved[i].FinalWeight *= (1.0 + 0.368*sdkNorm)
				} else if !inCloud && inLocal {
					resolved[i].FinalWeight = maxCloudWeight * (0.368 * (localWeightByID[uid] / topLocalScore))
				}
			}
			sort.SliceStable(resolved, func(i, j int) bool {
				if resolved[i].FinalWeight != resolved[j].FinalWeight {
					return resolved[i].FinalWeight > resolved[j].FinalWeight
				}
				return resolved[i].UnitID < resolved[j].UnitID
			})
			if local {
				resolved = limitHydratedLocalHits(resolved, topK)
			} else if len(resolved) > topK {
				resolved = resolved[:topK]
			}
		}
	}
	if _, local := c.Transport.(*LocalVaultTransport); local {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return resolved, nil
}

// RecallDefault executes the built-in Client SDK Default Recall (`SearchClientSDKDefault`:
// SQLite FTS5 trigram + surface/hash commitments + temporal window + same-day continuity)
// directly against the local Vault without requiring any cloud call.
func (c *Client) RecallDefault(queryPlaintext string, topK int) ([]LocalResolvedHit, error) {
	if c.Vault == nil {
		return nil, fmt.Errorf("thinclient: RecallDefault requires a local SQLite Vault")
	}
	if topK <= 0 {
		topK = 10
	}
	hits, err := c.Vault.SearchClientSDKDefault(queryPlaintext, topK)
	if err != nil {
		return nil, err
	}
	st := c.activeStore()
	out := make([]LocalResolvedHit, 0, len(hits))
	for _, h := range hits {
		rawTxt, distTxt, keptSents, lErr := st.LoadLocalDocument(h.UnitID)
		if lErr != nil {
			out = append(out, LocalResolvedHit{CloudQueryHit: h, SourceUnavailable: true})
			continue
		}
		out = append(out, LocalResolvedHit{
			CloudQueryHit:      h,
			RawPlaintext:       rawTxt,
			DistilledPlaintext: distTxt,
			KeptSentences:      keptSents,
			Authors:            RoleTaggedAuthors(rawTxt),
		})
	}
	return out, nil
}

// RememberNoetic stores an L3 Noetic memory record (Query + Retrieved Sentences + Answer)
// directly in the client's own local SQLite table (`thin_vault_noetic`).
func (c *Client) RememberNoetic(noeticID string, queryText string, citedUnitIDs []string, citedSentences []string, answerText string, occurredAt int64) error {
	return c.Vault.SaveLocalNoetic(LocalNoeticRecord{
		NoeticID:       noeticID,
		OccurredAt:     occurredAt,
		Status:         "active",
		QueryText:      queryText,
		AnswerText:     answerText,
		CitedUnitIDs:   citedUnitIDs,
		CitedSentences: citedSentences,
		AssistantID:    c.AssistantID,
		IncarnationID:  c.IncarnationID,
	})
}

// blindPacketDice scores the weighted unique-atom overlap of two blind query
// packets. Only the original retrieval cues participate: a long cited answer
// must never manufacture a match from an incidental token deep in its evidence.
func blindPacketDice(a, b BlindPulsePacket) float64 {
	mass := func(slots []BlindTokenAtom) (map[uint64]uint16, uint64) {
		byHash := make(map[uint64]uint16, len(slots))
		for _, slot := range slots {
			if slot.RawBitsX10 > byHash[slot.H] {
				byHash[slot.H] = slot.RawBitsX10
			}
		}
		var total uint64
		for _, width := range byHash {
			total += uint64(width)
		}
		return byHash, total
	}
	aMass, aTotal := mass(a.Slots)
	bMass, bTotal := mass(b.Slots)
	if aTotal == 0 || bTotal == 0 {
		return 0
	}
	var intersection uint64
	for h, width := range aMass {
		other, ok := bMass[h]
		if !ok {
			continue
		}
		if other < width {
			width = other
		}
		intersection += uint64(width)
	}
	return float64(intersection) / float64(aTotal+bTotal) * 2
}

func (v *Vault) loadActiveNoeticRecords() ([]LocalNoeticRecord, error) {
	if v == nil || v.db == nil {
		return nil, nil
	}
	v.checkExternalDBMutation()
	if v.slotCacheMu != nil && v.snapRef != nil {
		v.slotCacheMu.RLock()
		ready := v.snapRef.noeticReady
		hasLargeSnap := v.snapRef.snap != nil && len(v.snapRef.snap.docs) >= 2048
		cached := v.snapRef.activeNoetics
		lastTime := v.snapRef.lastNoeticTime
		refreshing := v.snapRef.noeticRefreshing
		v.slotCacheMu.RUnlock()
		if ready && hasLargeSnap {
			if time.Since(lastTime) >= 5*time.Minute && !refreshing {
				v.slotCacheMu.Lock()
				if !v.snapRef.noeticRefreshing {
					v.snapRef.noeticRefreshing = true
					v.snapRef.lastNoeticTime = time.Now()
					go func() {
						defer func() {
							v.slotCacheMu.Lock()
							v.snapRef.noeticRefreshing = false
							v.slotCacheMu.Unlock()
						}()
						_, _ = v.queryAndCacheActiveNoeticRecords()
					}()
				}
				v.slotCacheMu.Unlock()
			}
			return cached, nil
		}
	}
	return v.queryAndCacheActiveNoeticRecords()
}

func (v *Vault) queryAndCacheActiveNoeticRecords() ([]LocalNoeticRecord, error) {
	if v == nil || v.db == nil {
		return nil, nil
	}
	var cnt, maxRowID, occSum int64
	hasFP := false
	if v.slotCacheMu != nil && v.snapRef != nil {
		if v.db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(rowid), 0), COALESCE(SUM(occurred_at), 0) FROM thin_vault_noetic WHERE status = 'active'`).Scan(&cnt, &maxRowID, &occSum) == nil {
			hasFP = true
			v.slotCacheMu.RLock()
			if v.snapRef.noeticReady &&
				v.snapRef.noeticCnt == cnt &&
				v.snapRef.noeticMaxRowID == maxRowID &&
				v.snapRef.noeticOccSum == occSum {
				cached := v.snapRef.activeNoetics
				v.slotCacheMu.RUnlock()
				v.slotCacheMu.Lock()
				v.snapRef.lastNoeticTime = time.Now()
				v.slotCacheMu.Unlock()
				return cached, nil
			}
			v.slotCacheMu.RUnlock()
			if cnt == 0 {
				v.slotCacheMu.Lock()
				v.snapRef.noeticReady = true
				v.snapRef.lastNoeticTime = time.Now()
				v.snapRef.noeticCnt = 0
				v.snapRef.noeticMaxRowID = 0
				v.snapRef.noeticOccSum = 0
				v.snapRef.activeNoetics = nil
				v.slotCacheMu.Unlock()
				return nil, nil
			}
		}
	}
	rows, err := v.db.Query(`
			SELECT noetic_id, occurred_at, status, query_text, answer_text, cited_unit_ids_json, cited_sentences_json,
			       COALESCE(address_uri, ''), COALESCE(coord64, 0), COALESCE(waveform_bar, ''),
			       COALESCE(assistant_id, ''), COALESCE(incarnation_id, '')
			FROM thin_vault_noetic
			WHERE status = 'active'
			ORDER BY occurred_at DESC
		`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []LocalNoeticRecord
	for rows.Next() {
		var rec LocalNoeticRecord
		var uidsJSON, sentsJSON string
		if err := rows.Scan(&rec.NoeticID, &rec.OccurredAt, &rec.Status, &rec.QueryText, &rec.AnswerText,
			&uidsJSON, &sentsJSON, &rec.AddressURI, &rec.Coord64, &rec.WaveformBar, &rec.AssistantID, &rec.IncarnationID); err != nil {
			continue
		}
		_ = json.Unmarshal([]byte(uidsJSON), &rec.CitedUnitIDs)
		_ = json.Unmarshal([]byte(sentsJSON), &rec.CitedSentences)
		_ = v.noeticBlindPacket(rec.NoeticID, rec.QueryText, rec.OccurredAt)
		list = append(list, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if hasFP && v.slotCacheMu != nil && v.snapRef != nil {
		v.slotCacheMu.Lock()
		v.snapRef.noeticReady = true
		v.snapRef.lastNoeticTime = time.Now()
		v.snapRef.noeticCnt = cnt
		v.snapRef.noeticMaxRowID = maxRowID
		v.snapRef.noeticOccSum = occSum
		v.snapRef.activeNoetics = list
		v.slotCacheMu.Unlock()
	}
	return list, nil
}

// RecallNoetic searches the client's local `thin_vault_noetic` table using keyed HMAC root commitments.
func (c *Client) RecallNoetic(queryPlaintext string, topK int) ([]LocalNoeticRecord, error) {
	if c.Vault == nil || c.Vault.db == nil {
		return nil, nil
	}
	qPkt, _ := c.Vault.PackBlindPacket("Q-NOETIC", queryPlaintext, 0)
	if len(qPkt.Slots) == 0 {
		return nil, nil
	}

	records, err := c.Vault.loadActiveNoeticRecords()
	if err != nil || len(records) == 0 {
		return nil, err
	}

	var hits []LocalNoeticRecord
	for i := range records {
		rec := records[i]
		nPkt := c.Vault.noeticBlindPacket(rec.NoeticID, rec.QueryText, rec.OccurredAt)
		rec.MatchScore = blindPacketDice(qPkt, nPkt)
		if rec.MatchScore > 0 {
			hits = append(hits, rec)
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].MatchScore != hits[j].MatchScore {
			return hits[i].MatchScore > hits[j].MatchScore
		}
		return hits[i].NoeticID < hits[j].NoeticID
	})
	// Noetic is only a retrieval shortcut, so deliver the strongest handle
	// plateau rather than padding the result with weaker incidental overlaps.
	if len(hits) > 0 {
		best := hits[0].MatchScore
		plateau := 1
		for plateau < len(hits) && hits[plateau].MatchScore == best {
			plateau++
		}
		hits = hits[:plateau]
	}
	if topK > 0 && len(hits) > topK {
		hits = hits[:topK]
	}
	return hits, nil
}

// LocalVaultTransport provides an offline/local BrainTransport over the local SQLite Vault
// using strictly integer token-commitment matching (zero floating-point transcendental math).
type LocalVaultTransport struct {
	Vault *Vault
}

// NewLocalVaultTransport creates a local fallback transport backed by the SQLite Vault.
func NewLocalVaultTransport(vault *Vault) *LocalVaultTransport {
	return &LocalVaultTransport{Vault: vault}
}

func (v *Vault) verifyTransportScope(scope string) error {
	if v == nil || v.db == nil {
		return nil
	}
	reqScope := strings.TrimSpace(scope)
	if v.slotCacheMu != nil && v.snapRef != nil {
		v.slotCacheMu.RLock()
		bound := v.snapRef.boundScope
		hasLargeSnap := v.snapRef.snap != nil && len(v.snapRef.snap.docs) >= 2048
		v.slotCacheMu.RUnlock()
		if bound != "" && hasLargeSnap {
			if reqScope != "" && reqScope != bound && reqScope != strings.ReplaceAll(bound, ":", "-") {
				return fmt.Errorf("thinclient: requested scope %q does not match local_plain vault binding %q", reqScope, bound)
			}
			return nil
		}
	}
	var bound, storageType string
	err := v.db.QueryRow(`SELECT v, typeof(v) FROM thin_vault_meta WHERE k = 'local_plain_scope'`).Scan(&bound, &storageType)
	if err == nil {
		bound = strings.TrimSpace(bound)
		if storageType != "text" || bound == "" {
			return fmt.Errorf("thinclient: invalid local_plain vault scope binding")
		}
		if v.slotCacheMu != nil && v.snapRef != nil {
			v.slotCacheMu.Lock()
			v.snapRef.boundScope = bound
			v.slotCacheMu.Unlock()
		}
		if reqScope != "" && reqScope != bound && reqScope != strings.ReplaceAll(bound, ":", "-") {
			return fmt.Errorf("thinclient: requested scope %q does not match local_plain vault binding %q", reqScope, bound)
		}
		return nil
	}
	if reqScope != "" {
		var populated int
		_ = v.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM thin_vault_docs LIMIT 1) OR EXISTS(SELECT 1 FROM thin_vault_noetic LIMIT 1)`).Scan(&populated)
		if populated != 0 {
			return fmt.Errorf("thinclient: local_plain vault scope is unbound; cannot serve explicit scope %q", reqScope)
		}
	}
	return nil
}

func (l *LocalVaultTransport) IngestBlindPulse(_ context.Context, pkt BlindPulsePacket) (CloudIngestResponse, error) {
	if l.Vault != nil {
		if err := l.Vault.verifyTransportScope(pkt.Scope); err != nil {
			return CloudIngestResponse{}, err
		}
	}
	var keptIdx []int
	seenSentSig := make(map[uint64]int, len(pkt.SentenceFrames))
	for sIdx, sf := range pkt.SentenceFrames {
		var sig uint64
		for _, a := range sf {
			sig = (sig * 1099511628211) ^ a.H
		}
		seenSentSig[sig]++
		if seenSentSig[sig] <= 2 {
			keptIdx = append(keptIdx, sIdx)
		}
	}
	return CloudIngestResponse{
		UnitID:              pkt.UnitID,
		Epoch:               1,
		WaveformHex:         "F",
		WaveformBar:         "█",
		ConservedMass:       1.0,
		QualityMult:         1.0,
		AddressURI:          "C6:L1:LOCAL#" + pkt.UnitID,
		AddressCoord64:      0x1000000000000001,
		ContainerID:         0x1100000000000001,
		KeptSentenceIndices: keptIdx,
	}, nil
}

// DeleteUnits is a local no-op because LocalVaultTransport has no remote unit store.
func (l *LocalVaultTransport) DeleteUnits(_ context.Context, _ []string) error {
	return nil
}

func (l *LocalVaultTransport) QueryBlindManifold(ctx context.Context, queryPkt BlindPulsePacket, topK int) ([]CloudQueryHit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if l.Vault == nil || l.Vault.db == nil {
		return nil, nil
	}
	if err := l.Vault.verifyTransportScope(queryPkt.Scope); err != nil {
		return nil, err
	}
	qHashes := make(map[uint64]uint16, len(queryPkt.Slots))
	clockExcl := localQueryClockHeaderExclusiveHashes(queryPkt)
	for _, s := range queryPkt.Slots {
		w := s.RawBitsX10
		if clockExcl[s.H] {
			w = 18
		}
		if w > qHashes[s.H] {
			qHashes[s.H] = w
		}
	}
	// Load the shared corpus snapshot first: when available, inline rows are scored
	// from `snap.docs[:snap.inlineCount]` (via `allHashes`) and episodic + background
	// rows are scored via the inverted index `snap.queryPostings(qHashes)` in O(|Q|*df)
	// instead of scanning 66k `termCounts` maps on every query.
	snap, snapErr := l.Vault.loadCorpusSnapshot(ctx)
	var hits []CloudQueryHit
	if snapErr == nil && snap != nil {
		for idx := 0; idx < snap.inlineCount && idx < len(snap.docs); idx++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			doc := snap.docs[idx]
			var matchedWidth uint32
			seenH := make(map[uint64]bool, len(qHashes))
			for _, h := range doc.allHashes {
				if w, ok := qHashes[h]; ok && !seenH[h] {
					seenH[h] = true
					matchedWidth += uint32(w)
				}
			}
			if matchedWidth > 0 {
				wScore := float64(matchedWidth) / 10.0
				hits = append(hits, CloudQueryHit{
					UnitID:         doc.docID,
					Layer:          doc.layer,
					OccurredAt:     doc.occ,
					AddressURI:     doc.addrURI,
					AddressCoord64: doc.coord64,
					WaveformBar:    doc.waveBar,
					WaveScore:      wScore,
					FinalWeight:    wScore,
				})
			}
		}
		qPostings := snap.queryPostings(qHashes)
		matchedByDoc := make([]uint32, len(snap.docs))
		var matchedIndices []int
		for h, qw := range qHashes {
			if qw == 0 {
				continue
			}
			for _, docIdx := range qPostings[h] {
				if docIdx >= snap.inlineCount && docIdx < len(matchedByDoc) {
					if matchedByDoc[docIdx] == 0 {
						matchedIndices = append(matchedIndices, docIdx)
					}
					matchedByDoc[docIdx] += uint32(qw)
				}
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if cap(hits)-len(hits) < len(matchedIndices) && len(matchedIndices) <= 512 {
			grown := make([]CloudQueryHit, len(hits), len(hits)+len(matchedIndices))
			copy(grown, hits)
			hits = grown
		}
		// When tens of thousands of documents match a common query token in a large vault,
		// pre-filter to the top 512 widths before allocating CloudQueryHit structs.
		var widthFloor uint32
		if len(matchedIndices) > 512 {
			widths := make([]uint32, len(matchedIndices))
			for i, docIdx := range matchedIndices {
				widths[i] = matchedByDoc[docIdx]
			}
			l, r, target := 0, len(widths)-1, 512
			for l < r {
				piv := widths[l+(r-l)/2]
				widths[l+(r-l)/2], widths[r] = widths[r], widths[l+(r-l)/2]
				stIdx := l
				for i := l; i < r; i++ {
					if widths[i] > piv {
						widths[stIdx], widths[i] = widths[i], widths[stIdx]
						stIdx++
					}
				}
				widths[stIdx], widths[r] = widths[r], widths[stIdx]
				if stIdx == target {
					break
				} else if stIdx < target {
					l = stIdx + 1
				} else {
					r = stIdx - 1
				}
			}
			widthFloor = widths[target]
		}
		admittedEqual := 0
		for _, docIdx := range matchedIndices {
			matchedWidth := matchedByDoc[docIdx]
			if widthFloor > 0 {
				if matchedWidth < widthFloor {
					continue
				}
				if matchedWidth == widthFloor {
					admittedEqual++
					if admittedEqual > 512 {
						continue
					}
				}
			}
			doc := snap.docs[docIdx]
			wScore := float64(matchedWidth) / 10.0
			hits = append(hits, CloudQueryHit{
				UnitID:         doc.docID,
				Layer:          doc.layer,
				OccurredAt:     doc.occ,
				AddressURI:     doc.addrURI,
				AddressCoord64: doc.coord64,
				WaveformBar:    doc.waveBar,
				WaveScore:      wScore,
				FinalWeight:    wScore,
			})
		}
	} else {
		rows, err := l.Vault.db.QueryContext(ctx, `SELECT doc_id, layer, occurred_at, raw_text, address_uri, coord64, waveform_bar FROM thin_vault_docs`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		for rows.Next() {
			var docID, layer, rawText, addrURI, waveBar string
			var occ, coord64 int64
			if err := rows.Scan(&docID, &layer, &occ, &rawText, &addrURI, &coord64, &waveBar); err != nil {
				continue
			}
			// A pointer row carries a reference (`@ref:zcode:...`), not content: packing it
			// would only match the pointer's own path words. The corpus snapshot skips the
			// same rows, and delivery resolves them live on read.
			if strings.HasPrefix(strings.TrimSpace(rawText), "@ref:") {
				continue
			}
			docHashes := l.Vault.docSlotHashes(docID, rawText)
			var matchedWidth uint32
			seenH := make(map[uint64]bool, len(docHashes))
			for _, h := range docHashes {
				if w, ok := qHashes[h]; ok && !seenH[h] {
					seenH[h] = true
					matchedWidth += uint32(w)
				}
			}
			if matchedWidth > 0 {
				wScore := float64(matchedWidth) / 10.0
				hits = append(hits, CloudQueryHit{
					UnitID:         docID,
					Layer:          layer,
					OccurredAt:     occ,
					AddressURI:     addrURI,
					AddressCoord64: uint64(coord64),
					WaveformBar:    waveBar,
					WaveScore:      wScore,
					FinalWeight:    wScore,
				})
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		episodicDocs, err := l.Vault.indexedEpisodicDocsContext(ctx)
		if err != nil {
			return nil, err
		}
		for _, doc := range episodicDocs {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			var matchedWidth uint32
			for h := range doc.termCounts {
				matchedWidth += uint32(qHashes[h])
			}
			if matchedWidth > 0 {
				wScore := float64(matchedWidth) / 10.0
				hits = append(hits, CloudQueryHit{UnitID: doc.docID, Layer: doc.layer, OccurredAt: doc.occ, AddressURI: doc.addrURI, AddressCoord64: doc.coord64, WaveformBar: doc.waveBar, WaveScore: wScore, FinalWeight: wScore})
			}
		}
		// The chunk atoms are pre-packed at sync time and stay resident behind the vault's
		// fingerprint-keyed cache, so a query scans bytes instead of re-reading the table.
		seenAtoms := make(map[uint64]struct{}, 256)
		for _, chunk := range l.Vault.backgroundSlotRows() {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if matchedWidth := atomSlotMatchWidth(chunk.slots, qHashes, seenAtoms); matchedWidth > 0 {
				wScore := float64(matchedWidth) / 10.0
				hits = append(hits, CloudQueryHit{
					UnitID:         chunk.unitID,
					Layer:          LayerBackground,
					OccurredAt:     chunk.occ,
					AddressURI:     chunk.addrURI,
					AddressCoord64: chunk.coord64,
					WaveformBar:    chunk.waveBar,
					WaveScore:      wScore,
					FinalWeight:    wScore,
				})
			}
		}
	}
	if topK > 0 {
		sortTopCloudHits(hits, topK)
		if len(hits) > topK {
			hits = hits[:topK]
		}
	} else if len(hits) > 256 {
		sortTopCloudHits(hits, 256)
	} else {
		sort.SliceStable(hits, func(i, j int) bool {
			return cloudHitLess(hits[i], hits[j])
		})
	}
	return hits, nil
}

// ResolveGraph fetches the source-agnostic topology from the remote `/open/memory/v1/graph` endpoint
// (or synthesizes it from the local Vault in offline mode) and resolves plaintext summaries for every
// unit and Noetic node on the client!
func (c *Client) ResolveGraph(ctx context.Context, req BlindGraphRequest) (LocalResolvedGraph, error) {
	var cloudResp CloudGraphResponse
	if gt, ok := c.Transport.(interface {
		BuildBlindGraph(context.Context, BlindGraphRequest) (CloudGraphResponse, error)
	}); ok {
		var err error
		cloudResp, err = gt.BuildBlindGraph(ctx, req)
		if err != nil {
			return LocalResolvedGraph{}, err
		}
	} else if c.Vault != nil {
		localTr := NewLocalVaultTransport(c.Vault)
		cloudResp, _ = localTr.BuildBlindGraph(ctx, req)
	}

	layerFilter := make(map[string]bool, len(req.Layers))
	for _, l := range req.Layers {
		trimmed := strings.ToLower(strings.TrimSpace(l))
		if trimmed != "" {
			layerFilter[trimmed] = true
		}
	}
	allowNoetic := len(layerFilter) == 0 || layerFilter["noetic"]
	weights := map[string]float64{"background": 1.0, "episodic": 1.0, "noetic": 1.0}
	for k, v := range cloudResp.LayerWeights {
		weights[k] = v
	}
	for k, v := range req.LayerWeights {
		weights[strings.ToLower(strings.TrimSpace(k))] = v
	}

	st := c.activeStore()
	noeticByID := make(map[string]LocalNoeticRecord)
	if c.Vault != nil && c.Vault.db != nil {
		rows, err := c.Vault.db.Query(`
				SELECT noetic_id, occurred_at, status, query_text, answer_text, cited_unit_ids_json,
				       COALESCE(address_uri, ''), COALESCE(coord64, 0), COALESCE(waveform_bar, '')
				FROM thin_vault_noetic WHERE status = 'active' ORDER BY occurred_at DESC
			`)
		if err == nil {
			for rows.Next() {
				var r LocalNoeticRecord
				var uidsJSON string
				if err := rows.Scan(&r.NoeticID, &r.OccurredAt, &r.Status, &r.QueryText, &r.AnswerText, &uidsJSON,
					&r.AddressURI, &r.Coord64, &r.WaveformBar); err == nil {
					_ = json.Unmarshal([]byte(uidsJSON), &r.CitedUnitIDs)
					noeticByID[r.NoeticID] = r
				}
			}
			rows.Close()
		}
	}

	resolvedNodes := make([]LocalResolvedGraphNode, 0, len(cloudResp.Nodes)+len(noeticByID))
	seenUnitRaw := make(map[string]bool, len(cloudResp.Nodes))
	cellIdxByNodeID := make(map[string]int, len(cloudResp.Nodes))
	unitCellByRawID := make(map[string]string, len(cloudResp.Nodes))
	edges := append([]CloudGraphEdge(nil), cloudResp.Edges...)
	counts := map[string]int{"background": 0, "episodic": 0, "noetic": 0}
	cellCnt, unitCnt := 0, 0

	for _, cn := range cloudResp.Nodes {
		rn := LocalResolvedGraphNode{
			CloudGraphNode: cn,
			Summary:        cn.RawID,
		}
		if cn.NodeType == "cell" || cn.NodeType == "super_cell" {
			cellCnt++
			cellIdxByNodeID[cn.ID] = len(resolvedNodes)
			rn.Summary = cn.RawID + " (" + cn.Layer + ")"
		} else {
			unitCnt++
			seenUnitRaw[cn.RawID] = true
			if cn.ParentID != "" {
				unitCellByRawID[cn.RawID] = cn.ParentID
			}
			if nr, isNoetic := noeticByID[cn.RawID]; isNoetic {
				rn.Layer = "noetic"
				rn.RawPlaintext = "Q: " + nr.QueryText + "\nA: " + nr.AnswerText
				rn.DistilledPlaintext = nr.AnswerText
				rn.Summary = nr.QueryText
				if len(rn.CitedUnitIDs) == 0 {
					rn.CitedUnitIDs = nr.CitedUnitIDs
				}
				if rn.AddressURI == "" && nr.AddressURI != "" {
					rn.AddressURI = nr.AddressURI
				}
				if rn.WaveformBar == "" && nr.WaveformBar != "" {
					rn.WaveformBar = nr.WaveformBar
				}
			} else if st != nil {
				if rawTxt, distTxt, _, err := st.LoadLocalDocument(cn.RawID); err == nil {
					rn.RawPlaintext = rawTxt
					rn.DistilledPlaintext = distTxt
					if strings.TrimSpace(distTxt) != "" {
						rn.Summary = distTxt
					} else if strings.TrimSpace(rawTxt) != "" {
						rn.Summary = rawTxt
					}
				}
				if c.Vault != nil && (rn.Layer == "" || rn.Layer == "episodic") {
					if vLayer := c.Vault.LoadLocalDocumentLayer(cn.RawID); vLayer != "" {
						rn.Layer = vLayer
					}
				}
			}
		}
		if rn.Layer == "" {
			rn.Layer = "episodic"
		}
		counts[rn.Layer]++
		resolvedNodes = append(resolvedNodes, rn)
	}

	// Merge any local Noetic records not yet present in cloud nodes when Noetic layer is enabled
	if allowNoetic && weights["noetic"] > 0 {
		for nid, nr := range noeticByID {
			if seenUnitRaw[nid] {
				continue
			}
			nodeID := "unit:" + nid
			w := weights["noetic"]
			addrURI := strings.TrimSpace(nr.AddressURI)
			parentCellNodeID := ""
			if addrURI != "" {
				ck := extractLocalCellKey(addrURI)
				if ck != "" {
					parentCellNodeID = "cell:" + ck
					if cIdx, ok := cellIdxByNodeID[parentCellNodeID]; ok {
						resolvedNodes[cIdx].MemberCount++
					} else {
						cellIdxByNodeID[parentCellNodeID] = len(resolvedNodes)
						resolvedNodes = append(resolvedNodes, LocalResolvedGraphNode{
							CloudGraphNode: CloudGraphNode{
								ID:          parentCellNodeID,
								RawID:       ck,
								NodeType:    "cell",
								Layer:       "noetic",
								LayerWeight: w,
								AddressURI:  ck,
								Weight:      w,
								MemberCount: 1,
							},
							Summary: ck + " (noetic)",
						})
						cellCnt++
						counts["noetic"]++
					}
				}
			} else {
				for _, cited := range nr.CitedUnitIDs {
					if pc := unitCellByRawID[strings.TrimSpace(cited)]; pc != "" {
						parentCellNodeID = pc
						if cIdx, ok := cellIdxByNodeID[parentCellNodeID]; ok {
							resolvedNodes[cIdx].MemberCount++
						}
						break
					}
				}
			}
			deg := len(nr.CitedUnitIDs)
			if parentCellNodeID != "" {
				deg++
			}
			resolvedNodes = append(resolvedNodes, LocalResolvedGraphNode{
				CloudGraphNode: CloudGraphNode{
					ID:           nodeID,
					RawID:        nid,
					NodeType:     "unit",
					Layer:        "noetic",
					LayerWeight:  w,
					ParentID:     parentCellNodeID,
					AddressURI:   addrURI,
					WaveformBar:  nr.WaveformBar,
					Weight:       w,
					OccurredAt:   nr.OccurredAt,
					CitedUnitIDs: nr.CitedUnitIDs,
					Degree:       deg,
				},
				Summary:            nr.QueryText,
				RawPlaintext:       "Q: " + nr.QueryText + "\nA: " + nr.AnswerText,
				DistilledPlaintext: nr.AnswerText,
			})
			unitCnt++
			counts["noetic"]++
			if parentCellNodeID != "" {
				edges = append(edges, CloudGraphEdge{
					Source: nodeID,
					Target: parentCellNodeID,
					Kind:   "member_of",
					Layer:  "noetic",
					Weight: w,
				})
			}
			for _, cited := range nr.CitedUnitIDs {
				edges = append(edges, CloudGraphEdge{
					Source: nodeID,
					Target: "unit:" + cited,
					Kind:   "cites",
					Layer:  "noetic",
					Weight: w,
				})
			}
		}
	}

	return LocalResolvedGraph{
		Layers:       cloudResp.Layers,
		LayerWeights: weights,
		Nodes:        resolvedNodes,
		Edges:        edges,
		Stats: CloudGraphStats{
			TotalNodes:      len(resolvedNodes),
			TotalEdges:      len(edges),
			CellCount:       cellCnt,
			UnitCount:       unitCnt,
			LayerNodeCounts: counts,
		},
	}, nil
}

func extractLocalCellKey(addrURI string) string {
	trimmed := strings.TrimSpace(addrURI)
	if trimmed == "" {
		return ""
	}
	if parts := strings.Split(trimmed, "/"); len(parts) >= 3 {
		if idx := strings.IndexByte(parts[2], '#'); idx > 0 {
			return parts[2][:idx]
		}
		return parts[2]
	} else if idx := strings.IndexByte(trimmed, '#'); idx > 0 {
		return trimmed[:idx]
	}
	return trimmed
}

// BuildBlindGraph synthesizes a local fallback CloudGraphResponse from `thin_vault_docs` and `thin_vault_noetic`.
func (l *LocalVaultTransport) BuildBlindGraph(_ context.Context, req BlindGraphRequest) (CloudGraphResponse, error) {
	if l.Vault == nil || l.Vault.db == nil {
		return CloudGraphResponse{Success: true, Version: "v1"}, nil
	}
	if err := l.Vault.verifyTransportScope(req.Scope); err != nil {
		return CloudGraphResponse{}, err
	}
	layerFilter := make(map[string]bool, len(req.Layers))
	for _, ly := range req.Layers {
		t := strings.ToLower(strings.TrimSpace(ly))
		if t != "" {
			layerFilter[t] = true
		}
	}
	weights := map[string]float64{"background": 1.0, "episodic": 1.0, "noetic": 1.0}
	for k, v := range req.LayerWeights {
		weights[strings.ToLower(strings.TrimSpace(k))] = v
	}

	rows, err := l.Vault.db.Query(`SELECT doc_id, layer, occurred_at, address_uri, waveform_bar FROM thin_vault_docs ORDER BY occurred_at DESC`)
	if err != nil {
		return CloudGraphResponse{}, err
	}
	defer rows.Close()

	var nodes []CloudGraphNode
	var edges []CloudGraphEdge
	counts := map[string]int{"background": 0, "episodic": 0, "noetic": 0}
	cellSet := make(map[string]int)

	for rows.Next() {
		var docID, layer, addrURI, waveBar string
		var occ int64
		if err := rows.Scan(&docID, &layer, &occ, &addrURI, &waveBar); err != nil {
			continue
		}
		if layer == "" {
			layer = "episodic"
		}
		if len(layerFilter) > 0 && !layerFilter[layer] {
			continue
		}
		w := weights[layer]
		if w <= 0 {
			continue
		}
		cellKey := extractLocalCellKey(addrURI)
		if cellKey == "" {
			cellKey = "C6:L1:LOCAL"
		}
		cellNodeID := "cell:" + cellKey
		if _, exists := cellSet[cellNodeID]; !exists {
			cellSet[cellNodeID] = len(nodes)
			nodes = append(nodes, CloudGraphNode{
				ID:          cellNodeID,
				RawID:       cellKey,
				NodeType:    "cell",
				Layer:       layer,
				LayerWeight: w,
				AddressURI:  cellKey,
				Weight:      w,
				MemberCount: 1,
			})
			counts[layer]++
		} else {
			nodes[cellSet[cellNodeID]].MemberCount++
		}

		unitNodeID := "unit:" + docID
		nodes = append(nodes, CloudGraphNode{
			ID:          unitNodeID,
			RawID:       docID,
			NodeType:    "unit",
			Layer:       layer,
			LayerWeight: w,
			ParentID:    cellNodeID,
			AddressURI:  addrURI,
			WaveformBar: waveBar,
			Weight:      w,
			OccurredAt:  occ,
		})
		counts[layer]++
		edges = append(edges, CloudGraphEdge{
			Source: unitNodeID,
			Target: cellNodeID,
			Kind:   "member_of",
			Layer:  layer,
			Weight: w,
		})
	}

	return CloudGraphResponse{
		Success:      true,
		Version:      "v1",
		Layers:       req.Layers,
		LayerWeights: weights,
		Nodes:        nodes,
		Edges:        edges,
		Stats: CloudGraphStats{
			TotalNodes:      len(nodes),
			TotalEdges:      len(edges),
			CellCount:       len(cellSet),
			UnitCount:       len(nodes) - len(cellSet),
			LayerNodeCounts: counts,
		},
	}, nil
}
