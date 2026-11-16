// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package ramesdk_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

const cloudTestScope = "mem-eval-u10-locomo"

func saveCloudTestDocument(t *testing.T, store *ramesdk.MemoryStore, docID, rawText string) {
	t.Helper()
	_, sents := store.PackBlindPacket(docID, rawText, 1700000000)
	if err := store.SaveLocalPlaintext(docID, 1700000000, rawText, sents, ramesdk.CloudIngestResponse{
		UnitID: docID,
		Layer:  ramesdk.LayerEpisodic,
	}); err != nil {
		t.Fatalf("SaveLocalPlaintext(%s): %v", docID, err)
	}
}

func newCloudTestClient(server *httptest.Server, store ramesdk.PlaintextStore) *ramesdk.Client {
	client := ramesdk.NewClient(server.URL, "cloud-test-token", "10", store)
	client.TenantID = "user"
	client.Scope = cloudTestScope
	client.HTTPClient = server.Client()
	return client
}

func cloudTestEnvelope() map[string]any {
	return map[string]any{
		"success":   true,
		"tenant_id": "user",
		"scope":     cloudTestScope,
		"engine":    "autonoetic",
		"hits": []map[string]any{
			{"id": "cloud-doc", "layer": "episodic", "final_weight": 2.5},
		},
		"count": 1,
	}
}

func writeCloudTestJSON(t *testing.T, w http.ResponseWriter, body any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Errorf("encode test response: %v", err)
	}
}

func TestCloudSDKRecallUsesStrictEnvelope(t *testing.T) {
	store := ramesdk.NewMemoryStore([]byte("cloud-test-secret"))
	saveCloudTestDocument(t, store, "cloud-doc", "cloud fallback marker remote")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/query" || r.URL.Query().Get("envelope") != "1" {
			t.Errorf("unexpected query request: method=%s path=%s query=%s", r.Method, r.URL.Path, r.URL.RawQuery)
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		var req struct {
			TenantID string                   `json:"tenant_id"`
			UserID   string                   `json:"user_id"`
			Scope    string                   `json:"scope"`
			Query    ramesdk.BlindPulsePacket `json:"query"`
			TopK     int                      `json:"top_k"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode query request: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Query.AtomsB64 == "" || len(req.Query.Slots) != 0 || len(req.Query.SentenceFrames) != 0 {
			t.Errorf("expected canonical atoms_b64-only query wire payload, got: %+v", req.Query)
		}
		if err := req.Query.NormalizeWire(); err != nil {
			t.Errorf("NormalizeWire on query: %v", err)
		}
		if req.TenantID != "user" || req.UserID != "10" || req.Scope != cloudTestScope || req.TopK != 3 || len(req.Query.Slots) == 0 {
			t.Errorf("unexpected query payload: %+v", req)
		}
		if got := r.Header.Get("X-Rolling-Memory-Tenant"); got != "user" {
			t.Errorf("tenant header = %q, want user", got)
		}
		writeCloudTestJSON(t, w, cloudTestEnvelope())
	}))
	defer server.Close()

	hits, err := newCloudTestClient(server, store).Recall(context.Background(), "cloud fallback marker", 3)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(hits) != 1 || hits[0].UnitID != "cloud-doc" || hits[0].RawPlaintext != "cloud fallback marker remote" {
		t.Fatalf("unexpected resolved hits: %+v", hits)
	}
}

// TestCloudSDKRecallCrossIncarnationKnob 查询请求默认不下发 cross_incarnation（保持引擎的
// 跨分身共振默认行为）；调用方显式设置时才透传，false = 锁死单一分身空间（评测隔离用）。
func TestCloudSDKRecallCrossIncarnationKnob(t *testing.T) {
	cases := []struct {
		name     string
		value    *bool
		wantKey  bool
		wantBool bool
	}{
		{name: "default omits key", value: nil, wantKey: false},
		{name: "lock to own incarnation", value: boolPtr(false), wantKey: true, wantBool: false},
		{name: "explicit resonance", value: boolPtr(true), wantKey: true, wantBool: true},
	}
	for _, tc := range cases {
		store := ramesdk.NewMemoryStore([]byte("cloud-test-secret"))
		saveCloudTestDocument(t, store, "cloud-doc", "cloud fallback marker remote")
		var seen map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&seen)
			writeCloudTestJSON(t, w, cloudTestEnvelope())
		}))
		client := newCloudTestClient(server, store)
		client.CrossIncarnation = tc.value
		if _, err := client.Recall(context.Background(), "cloud fallback marker", 3); err != nil {
			server.Close()
			t.Fatalf("%s: Recall: %v", tc.name, err)
		}
		server.Close()
		value, present := seen["cross_incarnation"]
		if present != tc.wantKey {
			t.Fatalf("%s: cross_incarnation present=%v want %v (payload=%v)", tc.name, present, tc.wantKey, seen)
		}
		if tc.wantKey {
			if asBool, ok := value.(bool); !ok || asBool != tc.wantBool {
				t.Fatalf("%s: cross_incarnation=%v want %v", tc.name, value, tc.wantBool)
			}
		}
	}
}

func boolPtr(value bool) *bool { return &value }

func TestCloudSDKRecallRejectsEnvelopeMismatch(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "success false", mutate: func(env map[string]any) { env["success"] = false }},
		{name: "success missing", mutate: func(env map[string]any) { delete(env, "success") }},
		{name: "engine mismatch", mutate: func(env map[string]any) { env["engine"] = "cloud" }},
		{name: "engine missing", mutate: func(env map[string]any) { delete(env, "engine") }},
		{name: "scope mismatch", mutate: func(env map[string]any) { env["scope"] = "mem-eval-u10-atm" }},
		{name: "scope whitespace mismatch", mutate: func(env map[string]any) { env["scope"] = " " + cloudTestScope }},
		{name: "scope missing", mutate: func(env map[string]any) { delete(env, "scope") }},
		{name: "tenant mismatch", mutate: func(env map[string]any) { env["tenant_id"] = "10" }},
		{name: "tenant empty", mutate: func(env map[string]any) { env["tenant_id"] = "" }},
		{name: "tenant missing", mutate: func(env map[string]any) { delete(env, "tenant_id") }},
		{name: "count mismatch", mutate: func(env map[string]any) { env["count"] = 2 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := ramesdk.NewMemoryStore([]byte("cloud-test-secret"))
			saveCloudTestDocument(t, store, "cloud-doc", "cloud fallback marker remote")
			saveCloudTestDocument(t, store, "local-doc", "cloud fallback marker local")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				env := cloudTestEnvelope()
				tc.mutate(env)
				writeCloudTestJSON(t, w, env)
			}))
			defer server.Close()

			hits, err := newCloudTestClient(server, store).Recall(context.Background(), "cloud fallback marker", 5)
			if err == nil {
				t.Fatalf("expected strict envelope error, got hits=%+v", hits)
			}
			if len(hits) != 0 {
				t.Fatalf("envelope failure returned local or partial hits: %+v", hits)
			}
		})
	}
}

// TestCloudSDKRecallAcceptsCanonicalScopeForm 引擎会把请求 scope 归一到分身坐标规范形
// `ast:<assistant>:inc:<scope>` 回显（/query 如此，/units 回显原样）。客户端接受本客户端
// 分身坐标下的同一 scope；其他 assistant 或尾部不同的回显仍必须拒绝（隔离不放宽）。
func TestCloudSDKRecallAcceptsCanonicalScopeForm(t *testing.T) {
	accepted := []string{
		cloudTestScope,
		"ast:default:inc:" + cloudTestScope,
	}
	for _, scope := range accepted {
		store := ramesdk.NewMemoryStore([]byte("cloud-test-secret"))
		saveCloudTestDocument(t, store, "cloud-doc", "cloud fallback marker remote")
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			env := cloudTestEnvelope()
			env["scope"] = scope
			writeCloudTestJSON(t, w, env)
		}))
		hits, err := newCloudTestClient(server, store).Recall(context.Background(), "cloud fallback marker", 3)
		server.Close()
		if err != nil || len(hits) != 1 || hits[0].UnitID != "cloud-doc" {
			t.Fatalf("scope %q must be accepted: hits=%+v err=%v", scope, hits, err)
		}
	}
	rejected := []string{
		"ast:other:inc:" + cloudTestScope,          // 其他 assistant 的分身空间
		"ast:default:inc:" + cloudTestScope + "-x", // 尾部不同（超集）
		"ast:default:inc: " + cloudTestScope,       // 规范形内嵌空白
		"AST:default:inc:" + cloudTestScope,        // 前缀大小写不兜底
	}
	for _, scope := range rejected {
		store := ramesdk.NewMemoryStore([]byte("cloud-test-secret"))
		saveCloudTestDocument(t, store, "cloud-doc", "cloud fallback marker remote")
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			env := cloudTestEnvelope()
			env["scope"] = scope
			writeCloudTestJSON(t, w, env)
		}))
		hits, err := newCloudTestClient(server, store).Recall(context.Background(), "cloud fallback marker", 3)
		server.Close()
		if err == nil || len(hits) != 0 {
			t.Fatalf("scope %q must be rejected: hits=%+v err=%v", scope, hits, err)
		}
	}
}

func TestCloudSDKRecallFailuresNeverFallBack(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "http failure", status: http.StatusServiceUnavailable, body: `{"success":false}`},
		{name: "invalid json", status: http.StatusOK, body: `{"success":`},
		{name: "legacy hit array", status: http.StatusOK, body: `[{"id":"local-doc"}]`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := ramesdk.NewMemoryStore([]byte("cloud-test-secret"))
			saveCloudTestDocument(t, store, "local-doc", "cloud fallback marker local")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			hits, err := newCloudTestClient(server, store).Recall(context.Background(), "cloud fallback marker", 5)
			if err == nil || len(hits) != 0 {
				t.Fatalf("cloud failure must not use local fallback: hits=%+v err=%v", hits, err)
			}
		})
	}
}

func TestCloudSDKRecallDoesNotSupplementShortCloudResult(t *testing.T) {
	for _, cloudHits := range []int{0, 1} {
		t.Run(fmt.Sprintf("cloud_hits_%d", cloudHits), func(t *testing.T) {
			store := ramesdk.NewMemoryStore([]byte("cloud-test-secret"))
			saveCloudTestDocument(t, store, "cloud-doc", "cloud fallback marker remote")
			saveCloudTestDocument(t, store, "local-doc-a", "cloud fallback marker local a")
			saveCloudTestDocument(t, store, "local-doc-b", "cloud fallback marker local b")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				env := cloudTestEnvelope()
				if cloudHits == 0 {
					env["hits"] = []map[string]any{}
					env["count"] = 0
				}
				writeCloudTestJSON(t, w, env)
			}))
			defer server.Close()

			hits, err := newCloudTestClient(server, store).Recall(context.Background(), "cloud fallback marker", 5)
			if err != nil {
				t.Fatalf("Recall: %v", err)
			}
			if len(hits) != cloudHits {
				t.Fatalf("cloud returned %d hits but SDK delivered %d: %+v", cloudHits, len(hits), hits)
			}
			for _, hit := range hits {
				if hit.UnitID != "cloud-doc" {
					t.Fatalf("local supplement leaked into cloud result: %+v", hits)
				}
			}
		})
	}
}

func TestCloudSDKRecallRejectsUnresolvableUnitID(t *testing.T) {
	for _, badID := range []string{"missing-doc", ""} {
		t.Run("bad_"+badID, func(t *testing.T) {
			store := ramesdk.NewMemoryStore([]byte("cloud-test-secret"))
			saveCloudTestDocument(t, store, "cloud-doc", "cloud fallback marker remote")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				env := cloudTestEnvelope()
				env["hits"] = []map[string]any{{"id": "cloud-doc"}, {"id": badID}}
				env["count"] = 2
				writeCloudTestJSON(t, w, env)
			}))
			defer server.Close()

			hits, err := newCloudTestClient(server, store).Recall(context.Background(), "cloud fallback marker", 5)
			if err == nil || len(hits) != 0 {
				t.Fatalf("unresolvable UnitID must fail the whole recall: hits=%+v err=%v", hits, err)
			}
		})
	}
}

func TestCloudSDKRecallWithoutEndpointFailsClosed(t *testing.T) {
	store := ramesdk.NewMemoryStore([]byte("cloud-test-secret"))
	saveCloudTestDocument(t, store, "local-doc", "cloud fallback marker local")
	client := ramesdk.NewClient("", "cloud-test-token", "10", store)
	client.Scope = cloudTestScope

	hits, err := client.Recall(context.Background(), "cloud fallback marker", 5)
	if err == nil || len(hits) != 0 {
		t.Fatalf("Endpoint-free Recall must return an explicit error: hits=%+v err=%v", hits, err)
	}
}

func TestCloudSDKRecallRejectsEmptyPulseBeforeRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		writeCloudTestJSON(t, w, cloudTestEnvelope())
	}))
	defer server.Close()
	client := newCloudTestClient(server, ramesdk.NewMemoryStore([]byte("cloud-test-secret")))

	hits, err := client.Recall(context.Background(), " \n\t ", 5)
	if err == nil || len(hits) != 0 || requests != 0 {
		t.Fatalf("empty pulse must fail before HTTP: hits=%+v requests=%d err=%v", hits, requests, err)
	}
}

func TestCloudSDKHealth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/health" {
			t.Errorf("unexpected health request: method=%s path=%s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		writeCloudTestJSON(t, w, map[string]any{"status": "ok", "engine": "autonoetic", "version": "v1"})
	}))
	defer server.Close()
	if err := newCloudTestClient(server, ramesdk.NewMemoryStore(nil)).Health(context.Background()); err != nil {
		t.Fatalf("Health: %v", err)
	}
}

func TestCloudSDKHealthRejectsUnhealthyResponse(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "status", status: http.StatusOK, body: `{"status":"error","engine":"autonoetic","version":"v1"}`},
		{name: "engine", status: http.StatusOK, body: `{"status":"ok","engine":"cloud","version":"v1"}`},
		{name: "version", status: http.StatusOK, body: `{"status":"ok","engine":"autonoetic","version":"v2"}`},
		{name: "missing fields", status: http.StatusOK, body: `{}`},
		{name: "http failure", status: http.StatusServiceUnavailable, body: `{"status":"error"}`},
		{name: "invalid json", status: http.StatusOK, body: `{"status":`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			if err := newCloudTestClient(server, ramesdk.NewMemoryStore(nil)).Health(context.Background()); err == nil {
				t.Fatal("expected Health error")
			}
		})
	}
	client := ramesdk.NewClient("", "", "10", ramesdk.NewMemoryStore(nil))
	if err := client.Health(context.Background()); err == nil {
		t.Fatal("expected Endpoint-free Health error")
	}
}

func TestCloudSDKRememberValidatesRealIngestResponse(t *testing.T) {
	store := ramesdk.NewMemoryStore([]byte("cloud-test-secret"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/ingest" {
			t.Errorf("unexpected ingest request: method=%s path=%s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		var pkt ramesdk.BlindPulsePacket
		if err := json.NewDecoder(r.Body).Decode(&pkt); err != nil {
			t.Errorf("decode ingest request: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if pkt.TenantID != "user" || pkt.UserID != "10" || pkt.Scope != cloudTestScope || pkt.UnitID != "cloud-doc" {
			t.Errorf("unexpected ingest packet: %+v", pkt)
		}
		if got := r.Header.Get("X-Rolling-Memory-Tenant"); got != "user" {
			t.Errorf("tenant header = %q, want user", got)
		}
		writeCloudTestJSON(t, w, map[string]any{
			"id": "cloud-doc", "tenant_id": "user", "scope": cloudTestScope,
			"layer": "episodic", "kept_sentence_indices": []int{0},
		})
	}))
	defer server.Close()

	resp, err := newCloudTestClient(server, store).Remember(context.Background(), "cloud-doc", "first sentence. second sentence.", 1700000000)
	if err != nil {
		t.Fatalf("Remember: %v", err)
	}
	if resp.UnitID != "cloud-doc" || resp.TenantID != "user" || resp.Scope != cloudTestScope {
		t.Fatalf("real ingest response was not decoded: %+v", resp)
	}
	raw, distilled, kept, err := store.LoadLocalDocument("cloud-doc")
	if err != nil || raw != "first sentence. second sentence." || distilled != "first sentence" || len(kept) != 1 {
		t.Fatalf("ingest plaintext was not restored: raw=%q distilled=%q kept=%v err=%v", raw, distilled, kept, err)
	}
}

func TestCloudSDKRememberRejectsWrongOrMissingSpace(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "tenant mismatch", mutate: func(resp map[string]any) { resp["tenant_id"] = "10" }},
		{name: "tenant missing", mutate: func(resp map[string]any) { delete(resp, "tenant_id") }},
		{name: "scope mismatch", mutate: func(resp map[string]any) { resp["scope"] = "mem-eval-u10-atm" }},
		{name: "scope missing", mutate: func(resp map[string]any) { delete(resp, "scope") }},
		{name: "both missing", mutate: func(resp map[string]any) { delete(resp, "tenant_id"); delete(resp, "scope") }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := ramesdk.NewMemoryStore([]byte("cloud-test-secret"))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				resp := map[string]any{"id": "cloud-doc", "tenant_id": "user", "scope": cloudTestScope, "layer": "episodic"}
				tc.mutate(resp)
				writeCloudTestJSON(t, w, resp)
			}))
			defer server.Close()

			_, err := newCloudTestClient(server, store).Remember(context.Background(), "cloud-doc", "cloud fallback marker remote", 1700000000)
			if err == nil {
				t.Fatal("expected ingest space validation error")
			}
			if _, _, _, loadErr := store.LoadLocalDocument("cloud-doc"); loadErr == nil {
				t.Fatal("rejected cloud write must not be saved locally")
			}
		})
	}
}

func TestCloudSDKRememberWithoutEndpointKeepsExplicitLocalStorage(t *testing.T) {
	store := ramesdk.NewMemoryStore([]byte("cloud-test-secret"))
	client := ramesdk.NewClient("", "", "10", store)
	client.Scope = cloudTestScope
	if _, err := client.Remember(context.Background(), "local-doc", "cloud fallback marker local", 1700000000); err != nil {
		t.Fatalf("local Remember: %v", err)
	}
	if _, _, _, err := store.LoadLocalDocument("local-doc"); err != nil {
		t.Fatalf("local document was not stored: %v", err)
	}
}

func TestFormatPlaintextWithMetaAndTriTemporalSymmetry(t *testing.T) {
	// 1. 用户不设置任何元数据时，原文逐位不变（零前缀、零默认污染）
	raw := "Hello world, no metadata here."
	if got := ramesdk.FormatPlaintextWithMeta(raw, ramesdk.UnitMeta{}); got != raw {
		t.Fatalf("unset UnitMeta must keep rawText unchanged: got %q, want %q", got, raw)
	}
	// 2. 仅设置说话人
	if got := ramesdk.FormatPlaintextWithMeta("I moved from Sweden.", ramesdk.UnitMeta{Author: "Caroline"}); got != "Caroline: I moved from Sweden." {
		t.Fatalf("author-only meta mismatch: got %q", got)
	}
	// 3. 设置发生时 + 说话人（同日叙述时与发生时合并为单一日期标签，幂等防重）
	ts := int64(1683554160) // 2023-05-08 UTC
	meta := ramesdk.UnitMeta{Author: "Caroline", NarratedAt: ts, OccurredAt: ts}
	formatted := ramesdk.FormatPlaintextWithMeta("Hey Mel!", meta)
	if formatted != "[2023-05-08] Caroline: Hey Mel!" {
		t.Fatalf("same-day tri-temporal format mismatch: got %q", formatted)
	}
	if idemp := ramesdk.FormatPlaintextWithMeta(formatted, meta); idemp != formatted {
		t.Fatalf("FormatPlaintextWithMeta must be idempotent: got %q", idemp)
	}
	// 4. 三时态（发生时、叙述时、入库时）跨日不同时对称展现
	triMeta := ramesdk.UnitMeta{
		Author:     "Caroline",
		OccurredAt: 1683504000, // 2023-05-08
		NarratedAt: 1683590400, // 2023-05-09
		IngestedAt: 1683676800, // 2023-05-10
	}
	triFormatted := ramesdk.FormatPlaintextWithMeta("Went to the LGBTQ support group.", triMeta)
	wantTri := "[2023-05-08|narrated:2023-05-09|ingested:2023-05-10] Caroline: Went to the LGBTQ support group."
	if triFormatted != wantTri {
		t.Fatalf("tri-temporal format mismatch: got %q, want %q", triFormatted, wantTri)
	}
	enc := ramesdk.NewBlindEncoder([]byte("test-secret"))
	pkt, _ := enc.PackBlindPacketWithMeta("doc-tri", "Went to the LGBTQ support group.", triMeta)
	if pkt.OccurredAt != triMeta.OccurredAt || pkt.NarratedAt != triMeta.NarratedAt || pkt.IngestedAt != triMeta.IngestedAt || pkt.AuthorHash == 0 {
		t.Fatalf("PackBlindPacketWithMeta did not populate tri-temporal/author fields: %+v", pkt)
	}

	// 5. Client.RememberWithMeta 同样必须在网络包中携带非零 AuthorHash 与正典 AtomsB64
	store := ramesdk.NewMemoryStore([]byte("test-secret"))
	var seenIngest ramesdk.BlindPulsePacket
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&seenIngest)
		writeCloudTestJSON(t, w, map[string]any{
			"id": "doc-tri", "tenant_id": "user", "scope": cloudTestScope, "layer": "episodic",
		})
	}))
	defer srv.Close()
	cli := newCloudTestClient(srv, store)
	if _, err := cli.RememberWithMeta(context.Background(), "doc-tri", "Went to the LGBTQ support group.", ramesdk.LayerEpisodic, triMeta); err != nil {
		t.Fatalf("RememberWithMeta: %v", err)
	}
	if seenIngest.AuthorHash != pkt.AuthorHash || seenIngest.NarratedAt != triMeta.NarratedAt || seenIngest.IngestedAt != triMeta.IngestedAt || seenIngest.AtomsB64 == "" {
		t.Fatalf("RememberWithMeta wire packet mismatch: got %+v, want AuthorHash=%d", seenIngest, pkt.AuthorHash)
	}
}

func TestBlindPepperSurvivesMem2KeyRotation(t *testing.T) {
	keyA := "mem2.eyJ0aWQiOiJ1c2VyIiwidWlkIjoiMTAiLCJpYXQiOjE3MDAwMDAwMDAsImV4cCI6MTcwMDA4NjQwMH0.sig_rotation_alpha_1111"
	keyB := "mem2.eyJ0aWQiOiJ1c2VyIiwidWlkIjoiMTAiLCJpYXQiOjE3MDAwODAwMDAsImV4cCI6MTcwMDE2NjQwMH0.sig_rotation_beta_2222"
	keyOther := "mem2.eyJ0aWQiOiJvdGhlciIsInVpZCI6Ijk5IiwiaWF0IjoxNzAwMDAwMDAwLCJleHAiOjE3MDAwODY0MDB9.sig_other_3333"

	encA := ramesdk.NewBlindEncoder([]byte(keyA))
	encB := ramesdk.NewBlindEncoder([]byte(keyB))
	encOther := ramesdk.NewBlindEncoder([]byte(keyOther))

	pktA, _ := encA.PackBlindPacket("doc-rot", "Glasgow hotel reservation for 6 nights.", 1700000100)
	pktB, _ := encB.PackBlindPacket("doc-rot", "Glasgow hotel reservation for 6 nights.", 1700000100)
	pktOther, _ := encOther.PackBlindPacket("doc-rot", "Glasgow hotel reservation for 6 nights.", 1700000100)

	if len(pktA.Slots) == 0 || len(pktA.Slots) != len(pktB.Slots) {
		t.Fatalf("unexpected slot count: A=%d B=%d", len(pktA.Slots), len(pktB.Slots))
	}
	for i := range pktA.Slots {
		if pktA.Slots[i] != pktB.Slots[i] {
			t.Fatalf("mem2 key rotation broke blind atom at slot %d: keyA=%+v != keyB=%+v", i, pktA.Slots[i], pktB.Slots[i])
		}
		if pktA.Slots[i].H == pktOther.Slots[i].H {
			t.Fatalf("different tenant must not share blind hash at slot %d", i)
		}
	}
}

func TestCloudSDKVerifyCodecAndHealthCanary(t *testing.T) {
	if err := ramesdk.VerifyCodecLocal(); err != nil {
		t.Fatalf("VerifyCodecLocal: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			AtomsB64 string `json:"atoms_b64"`
		}
		if r.Method == http.MethodPost {
			_ = json.NewDecoder(r.Body).Decode(&body)
		} else {
			body.AtomsB64 = r.URL.Query().Get("atoms_b64")
		}
		resp := map[string]any{
			"status":       "ok",
			"engine":       "autonoetic",
			"version":      "v1",
			"spec_version": ramesdk.SpecVersion,
			"codec_canary": map[string]any{
				"spec":       ramesdk.SpecVersion,
				"slot_count": ramesdk.CanarySlotCount,
				"sent_count": ramesdk.CanarySentCount,
				"atoms_b64":  ramesdk.CanaryAtomsB64,
			},
		}
		if body.AtomsB64 != "" {
			slots, frames, err := ramesdk.DecodeWirePayload(body.AtomsB64)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			resp["codec_probe"] = map[string]any{
				"verified":   true,
				"slot_count": len(slots),
				"sent_count": len(frames),
				"atoms_b64":  body.AtomsB64,
			}
		}
		writeCloudTestJSON(t, w, resp)
	}))
	defer srv.Close()

	cli := newCloudTestClient(srv, ramesdk.NewMemoryStore([]byte("s")))
	if err := cli.Health(context.Background()); err != nil {
		t.Fatalf("Health with canary: %v", err)
	}
	rep, err := cli.VerifyCodec(context.Background())
	if err != nil {
		t.Fatalf("VerifyCodec: %v", err)
	}
	if !rep.LocalVerified || !rep.RemoteCanaryOK || !rep.RemoteProbeOK || rep.SlotCount != ramesdk.CanarySlotCount {
		t.Fatalf("unexpected VerifyCodec report: %+v", rep)
	}
}

func TestCloudSDKDefaultEmptyScopeAndIncarnationScopeMatching(t *testing.T) {
	store := ramesdk.NewMemoryStore([]byte("cloud-test-secret"))
	saveCloudTestDocument(t, store, "cloud-doc", "cloud fallback marker remote")
	// 1. Default Scope="" when server echoes "ast:default" or "ast:default:inc:10"
	for _, echoed := range []string{"ast:default", "ast:default:inc:10", "10"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			env := cloudTestEnvelope()
			env["scope"] = echoed
			writeCloudTestJSON(t, w, env)
		}))
		cli := ramesdk.NewClient(srv.URL, "tok", "10", store)
		cli.TenantID = "user"
		cli.AssistantID = "default"
		cli.IncarnationID = ""
		cli.HTTPClient = srv.Client()
		hits, err := cli.Recall(context.Background(), "cloud fallback marker", 3)
		srv.Close()
		if err != nil || len(hits) != 1 {
			t.Fatalf("default empty Scope must accept server-composed %q: err=%v", echoed, err)
		}
	}
	// 2. Explicit IncarnationID="sess-99" when server echoes "ast:coder:inc:sess-99"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		env := cloudTestEnvelope()
		env["scope"] = "ast:coder:inc:sess-99"
		writeCloudTestJSON(t, w, env)
	}))
	defer srv.Close()
	cli := newCloudTestClient(srv, store)
	cli.AssistantID = "coder"
	cli.IncarnationID = "sess-99"
	if _, err := cli.Recall(context.Background(), "cloud fallback marker", 3); err != nil {
		t.Fatalf("IncarnationID-composed scope must be accepted: %v", err)
	}
}

func TestSpatialCoordinatesAndCompositeFreeBlocks(t *testing.T) {
	enc := ramesdk.NewBlindEncoder([]byte("spatial-secret"))
	meta := ramesdk.UnitMeta{
		Author:     "Alice",
		OccurredAt: 1700000100,
		FileURL:    "file:///docs/architecture.md",
		BlockID:    "sec-wave-cell",
		LineStart:  10,
		LineEnd:    42,
	}
	pkt, _ := enc.PackBlindPacketWithMeta("doc-spatial", "Wave cell resonance.", meta)
	wantDocH := enc.KeyedHash64("file:///docs/architecture.md")
	wantBlkH := enc.KeyedHash64("sec-wave-cell")
	if pkt.DocHash != wantDocH || pkt.BlockHash != wantBlkH || pkt.LineStart != 10 || pkt.LineEnd != 42 {
		t.Fatalf("PackBlindPacketWithMeta spatial mismatch: got doc_h=%d block_h=%d lines=%d..%d, want doc_h=%d block_h=%d",
			pkt.DocHash, pkt.BlockHash, pkt.LineStart, pkt.LineEnd, wantDocH, wantBlkH)
	}
	wirePkt, err := pkt.WirePacket()
	if err != nil || wirePkt.DocHash != wantDocH || wirePkt.BlockHash != wantBlkH || wirePkt.LineStart != 10 || wirePkt.LineEnd != 42 {
		t.Fatalf("WirePacket lost spatial fields: %+v err=%v", wirePkt, err)
	}

	subUnits := []ramesdk.SubUnit{
		{Kind: "ocr", BlockID: "blk-ocr-1", Text: "Receipt total £45.49", LineStart: 1, LineEnd: 4},
		{Kind: "caption", BlockID: "blk-cap-1", FileParse: "Grocery delivery confirmation", LineStart: 5, LineEnd: 8},
	}
	compPkts, subMap := enc.PackCompositePackets("parent-doc-1", "Full document body", 1700000200, subUnits)
	if len(compPkts) != 3 {
		t.Fatalf("expected 3 composite packets (1 parent + 2 sub), got %d", len(compPkts))
	}
	parentDocH := enc.KeyedHash64("parent-doc-1")
	for i, cp := range compPkts {
		if cp.DocHash != parentDocH {
			t.Fatalf("packet[%d] DocHash=%d, want %d", i, cp.DocHash, parentDocH)
		}
		if i > 0 {
			if len(cp.CitedUnitIDs) != 1 || cp.CitedUnitIDs[0] != "parent-doc-1" {
				t.Fatalf("sub-packet[%d] missing CitedUnitIDs: %+v", i, cp.CitedUnitIDs)
			}
			if cp.BlockHash == 0 || cp.LineStart <= 0 || cp.LineEnd < cp.LineStart {
				t.Fatalf("sub-packet[%d] spatial fields not populated: %+v", i, cp)
			}
		}
	}
	if subMap["parent-doc-1#ocr"] != "parent-doc-1" || subMap["parent-doc-1#caption"] != "parent-doc-1" {
		t.Fatalf("unexpected subToParent map: %+v", subMap)
	}

	var ingested []ramesdk.BlindPulsePacket
	store := ramesdk.NewMemoryStore([]byte("spatial-secret"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p ramesdk.BlindPulsePacket
		_ = json.NewDecoder(r.Body).Decode(&p)
		ingested = append(ingested, p)
		writeCloudTestJSON(t, w, map[string]any{
			"id": p.UnitID, "tenant_id": "user", "scope": cloudTestScope, "layer": p.Layer,
		})
	}))
	defer srv.Close()
	cli := newCloudTestClient(srv, store)
	resps, err := cli.RememberComposite(context.Background(), "parent-doc-1", "Full document body", 1700000200, ramesdk.LayerEpisodic, subUnits)
	if err != nil || len(resps) != 3 || len(ingested) != 3 {
		t.Fatalf("RememberComposite failed: resps=%d ingested=%d err=%v", len(resps), len(ingested), err)
	}
	for i, p := range ingested {
		if p.DocHash != parentDocH {
			t.Fatalf("ingested[%d] DocHash=%d, want %d", i, p.DocHash, parentDocH)
		}
	}
	if gotParent := store.ResolveCanonicalUnitID("parent-doc-1#ocr"); gotParent != "parent-doc-1" {
		t.Fatalf("ResolveCanonicalUnitID = %q, want parent-doc-1", gotParent)
	}
}
