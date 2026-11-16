// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package eval

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

type cloudTestServer struct {
	*httptest.Server
	mu             sync.Mutex
	ingests        []ramesdk.BlindPulsePacket
	ingestsByScope map[string][]ramesdk.BlindPulsePacket
	purges         map[string]int
	queryHitIDs    []string
	graphUnitIDs   []string
	queryStatus    int
	responseScope  string
	responseEngine string
}

func newCloudTestServer(t *testing.T) *cloudTestServer {
	t.Helper()
	s := &cloudTestServer{
		queryStatus: http.StatusOK, responseEngine: "autonoetic",
		ingestsByScope: map[string][]ramesdk.BlindPulsePacket{}, purges: map[string]int{},
	}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/open/memory/v1/health":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "ok", "engine": "autonoetic", "version": "v1",
				"layers": []string{"background", "episodic", "noetic"},
			})
		case "/open/memory/v1/auth/verify":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true, "valid": true, "engine": "autonoetic", "tenant_id": "user",
				"user_id_prefixes": []string{"user", "10", "u10"}, "exact_namespace": false,
			})
		case "/open/memory/v1/ingest":
			var pkt ramesdk.BlindPulsePacket
			if err := json.NewDecoder(r.Body).Decode(&pkt); err != nil {
				t.Errorf("decode ingest: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_ = pkt.NormalizeWire()
			s.mu.Lock()
			s.ingests = append(s.ingests, pkt)
			s.ingestsByScope[pkt.Scope] = append(s.ingestsByScope[pkt.Scope], pkt)
			s.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": pkt.UnitID, "unit_id": pkt.UnitID, "tenant_id": "user",
				"scope": pkt.Scope, "layer": pkt.Layer, "kept_sentence_indices": []int{0},
			})
		case "/open/memory/v1/units":
			var body struct {
				Scope string `json:"scope"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			s.mu.Lock()
			unitIDs := append([]string(nil), s.graphUnitIDs...)
			if unitIDs == nil {
				seen := map[string]bool{}
				for _, pkt := range s.ingestsByScope[body.Scope] {
					if !seen[pkt.UnitID] {
						seen[pkt.UnitID] = true
						unitIDs = append(unitIDs, pkt.UnitID)
					}
				}
			}
			s.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true, "tenant_id": "user", "scope": body.Scope,
				"count": len(unitIDs), "unit_ids": unitIDs,
			})
		case "/open/memory/v1/purge":
			var body struct {
				Scope   string   `json:"scope"`
				Mode    string   `json:"mode"`
				UnitIDs []string `json:"unit_ids"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode purge: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			s.mu.Lock()
			dropped := map[string]bool{}
			for _, id := range body.UnitIDs {
				dropped[id] = true
			}
			kept := make([]ramesdk.BlindPulsePacket, 0, len(s.ingestsByScope[body.Scope]))
			for _, pkt := range s.ingestsByScope[body.Scope] {
				if !dropped[pkt.UnitID] {
					kept = append(kept, pkt)
				}
			}
			deleted := len(s.ingestsByScope[body.Scope]) - len(kept)
			s.ingestsByScope[body.Scope] = kept
			s.purges[body.Scope] += deleted
			remaining := len(kept)
			s.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true, "tenant_id": "user", "scope": body.Scope, "mode": body.Mode,
				"deleted_units": deleted, "remaining_units": remaining,
			})
		case "/open/memory/v1/query":
			var body struct {
				UserID string                   `json:"user_id"`
				Scope  string                   `json:"scope"`
				Query  ramesdk.BlindPulsePacket `json:"query"`
				TopK   int                      `json:"top_k"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode query: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_ = body.Query.NormalizeWire()
			s.mu.Lock()
			status := s.queryStatus
			scope := s.responseScope
			if scope == "" {
				scope = body.Scope
			}
			engine := s.responseEngine
			hitIDs := append([]string(nil), s.queryHitIDs...)
			if hitIDs == nil {
				for _, pkt := range s.ingestsByScope[body.Scope] {
					hitIDs = append(hitIDs, pkt.UnitID)
				}
			}
			s.mu.Unlock()
			if status != http.StatusOK {
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "forced query failure"})
				return
			}
			hits := make([]map[string]any, 0, len(hitIDs))
			for index, id := range hitIDs {
				hits = append(hits, map[string]any{"id": id, "final_weight": float64(len(hitIDs) - index)})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true, "tenant_id": "user", "scope": scope,
				"engine": engine, "count": len(hits), "hits": hits,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

// scopeUnitIDs 返回某个 scope 在当前假引擎里的权威 Unit 全集。
func (s *cloudTestServer) scopeUnitIDs(scope string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	ids := []string{}
	for _, pkt := range s.ingestsByScope[scope] {
		if !seen[pkt.UnitID] {
			seen[pkt.UnitID] = true
			ids = append(ids, pkt.UnitID)
		}
	}
	return ids
}

func newTestCloudEngine(t *testing.T, server *cloudTestServer, suite string) *CloudEngine {
	t.Helper()
	return newTestCloudEngineAt(t, server, suite, filepath.Join(t.TempDir(), "vault.json"))
}

func newTestCloudEngineAt(t *testing.T, server *cloudTestServer, suite, storePath string) *CloudEngine {
	t.Helper()
	t.Setenv("ROLLING_MEMORY_BRAIN_URL", server.URL)
	t.Setenv("ROLLING_MEMORY_BRAIN_KEY", "mem_test_cloud_eval")
	t.Setenv("ROLLING_MEMORY_USER_ID", defaultCloudUserID)
	cfg := testConfig(t)
	cfg.Mode = "cloud"
	engine, err := NewCloudEngine(cfg, suite, storePath, WithPulseMode(PulseModePulseCipher))
	if err != nil {
		t.Fatalf("NewCloudEngine: %v", err)
	}
	return engine
}

func TestCloudEngineRejectsForeignScopeAndFailedQuery(t *testing.T) {
	server := newCloudTestServer(t)
	engine := newTestCloudEngine(t, server, SuiteLoCoMo)
	if _, err := NewCloudEngineForScope(testConfig(t), "mem:self", filepath.Join(t.TempDir(), "foreign.json")); err == nil {
		t.Fatal("real user scope must be rejected for evaluation")
	}
	if _, err := NewCloudEngineForScope(testConfig(t), "mem-eval-other-locomo", filepath.Join(t.TempDir(), "foreign.json")); err == nil {
		t.Fatal("another tenant's evaluation scope must be rejected")
	}
	server.queryStatus = http.StatusInternalServerError
	if _, err := engine.Recall(t.Context(), "Where did I move from?", 5); err == nil {
		t.Fatal("remote query failure must not fall back to local recall")
	}
}

func TestCloudScopeIsolation(t *testing.T) {
	locomo := ScopeForSuite(SuiteLoCoMo, defaultCloudUserID)
	atm := ScopeForSuite(SuiteATM, defaultCloudUserID)
	if locomo != "u10-mem-eval-locomo" || atm != "u10-mem-eval-atmbench" {
		t.Fatalf("unexpected isolated scopes: %q %q", locomo, atm)
	}
	if locomo == atm || locomo == "mem:self" || atm == "mem:self" {
		t.Fatal("evaluation and real user scopes must be disjoint")
	}
}

func TestCloudEngineRejectsEmptyCorpus(t *testing.T) {
	server := newCloudTestServer(t)
	engine := newTestCloudEngine(t, server, SuiteLoCoMo)
	if _, err := engine.Ingest(t.Context(), nil, false, 1, nil); err == nil {
		t.Fatal("empty corpus must not produce a successful evaluation manifest")
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.ingests) != 0 {
		t.Fatalf("empty corpus triggered %d remote writes", len(server.ingests))
	}
}

func TestCloudPrepareRequiresExactRemoteSet(t *testing.T) {
	server := newCloudTestServer(t)
	engine := newTestCloudEngine(t, server, SuiteLoCoMo)
	documents := []CloudDocument{
		{ID: "doc-a", Text: "alpha memory", OccurredAt: 1},
		{ID: "doc-b", Text: "beta memory", OccurredAt: 2},
	}
	if _, err := engine.Ingest(t.Context(), documents, false, 2, nil); err != nil {
		t.Fatalf("initial ingest: %v", err)
	}
	server.graphUnitIDs = []string{"doc-b"}
	if _, err := engine.Prepare(t.Context(), documents, false, true, 2, nil); err == nil {
		t.Fatal("partial remote corpus must not be reused")
	}
	server.graphUnitIDs = []string{"doc-b", "doc-a"}
	manifest, err := engine.Prepare(t.Context(), documents, false, true, 2, nil)
	if err != nil || !manifest.Reused {
		t.Fatalf("exact remote set was not reused: %+v err=%v", manifest, err)
	}
	manifest.Endpoint = "http://127.0.0.1:9999/open/memory/v1"
	if err := writeJSON(engine.StorePath+".manifest.json", manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Prepare(t.Context(), documents, false, true, 2, nil); err == nil {
		t.Fatal("manifest from another endpoint must not be reused")
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.ingests) != 2 {
		t.Fatalf("verification triggered another ingest: %d", len(server.ingests))
	}
}

// TestCloudCredentialsRequireNoCompiledInKey pins the credential contract: the client ships no key
// of its own, so evaluation resolves it from the environment → explicit configuration → this
// machine's account state (data-plane signed key), and fails loudly when none exists.
func TestCloudCredentialsRequireNoCompiledInKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home) // isolate the machine account state (the other credential source)
	t.Setenv("ROLLING_MEMORY_BRAIN_URL", "")
	t.Setenv("ROLLING_MEMORY_BRAIN_KEY", "")
	t.Setenv("ROLLING_MEMORY_CLOUD_KEY", "")
	t.Setenv("ROLLING_MEMORY_OPEN_MEMORY_USER", "")
	t.Setenv("ROLLING_MEMORY_USER_ID", defaultCloudUserID)
	t.Setenv("ROLLING_MEMORY_TEST_SYNC_KEY", "")
	cfg := testConfig(t)
	cfg.Cloud.APIKeyEnv = "ROLLING_MEMORY_TEST_SYNC_KEY"
	cfg.Cloud.APIKeyFile = filepath.Join(t.TempDir(), "missing-cloud.key")
	if _, err := cloudAPIKey(cfg, ""); err == nil {
		t.Fatal("a credential-free environment must fail loudly instead of falling back to a compiled-in key")
	}

	// 第二优先：显式配置（cfg.SyncKey）——即使本机账户态可用也先取它。
	t.Setenv("ROLLING_MEMORY_TEST_SYNC_KEY", "cfg.sync.key")
	if key, err := cloudAPIKey(cfg, ""); err != nil || key != "cfg.sync.key" {
		t.Fatalf("explicit configured credential not honoured: key=%q err=%v", key, err)
	}
	t.Setenv("ROLLING_MEMORY_TEST_SYNC_KEY", "")
	cfg.Cloud.APIKeyFile = ""

	// 第三优先：本机账户态里未过期的 mem2.* 数据面签名钥（无需网络续签）。
	const accountKey = "mem2.eyJ0aWQiOiJ1c2VyIiwidWlkIjoiMTAifQ.account_sig"
	accountDir := filepath.Join(home, ".config", "rolling-memory")
	if err := os.MkdirAll(accountDir, 0o700); err != nil {
		t.Fatal(err)
	}
	accountRaw, err := json.Marshal(map[string]any{
		"signed_key":            accountKey,
		"signed_key_expires_at": time.Now().Add(2 * time.Hour).Unix(),
		"data_plane_url":        "http://127.0.0.1:8082/open/memory/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(accountDir, "cloud-account.json"), accountRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if key, err := cloudAPIKey(cfg, ""); err != nil || key != accountKey {
		t.Fatalf("machine account signed key not adopted: key=%q err=%v", key, err)
	}
	cfg.Cloud.BaseURL = ""
	if ep := cloudEndpoint(cfg); ep != "http://127.0.0.1:8082/open/memory/v1" {
		t.Fatalf("machine account data_plane_url not adopted: got %q", ep)
	}
	cfg.Cloud.BaseURL = "http://127.0.0.1:8083"
	if ep := cloudEndpoint(cfg); ep != "http://127.0.0.1:8083/open/memory/v1" {
		t.Fatalf("explicit cfg.Cloud.BaseURL not honoured over account state: got %q", ep)
	}
	t.Setenv("ROLLING_MEMORY_BRAIN_URL", "http://127.0.0.1:8084")
	if ep := cloudEndpoint(cfg); ep != "http://127.0.0.1:8084/open/memory/v1" {
		t.Fatalf("explicit ROLLING_MEMORY_BRAIN_URL not honoured: got %q", ep)
	}

	// 第一优先：环境变量——任何端点都按显式凭据走（客户端不再做端点限定的密钥回退）。
	t.Setenv("ROLLING_MEMORY_BRAIN_KEY", "mem2.explicit.sig")
	if key, err := cloudAPIKey(cfg, ""); err != nil || key != "mem2.explicit.sig" {
		t.Fatalf("explicit environment credential not honoured: key=%q err=%v", key, err)
	}
}

// TestEvalVaultPepperSurvivesMem2Rotation pins the rotation-invariance contract: the vault pepper
// is derived from the signed key's tenant claims, so a renewed `mem2.*` key maps to the same
// blind pulse while a different tenant never does.
func TestEvalVaultPepperSurvivesMem2Rotation(t *testing.T) {
	keyA := "mem2.eyJ0aWQiOiJ1c2VyIiwidWlkIjoiMTAiLCJpYXQiOjE3MDAwMDAwMDAsImV4cCI6MTcwMDA4NjQwMH0.sig_rotation_alpha_1111"
	keyB := "mem2.eyJ0aWQiOiJ1c2VyIiwidWlkIjoiMTAiLCJpYXQiOjE3MDAwODAwMDAsImV4cCI6MTcwMDE2NjQwMH0.sig_rotation_beta_2222"
	keyOther := "mem2.eyJ0aWQiOiJvdGhlciIsInVpZCI6Ijk5IiwiaWF0IjoxNzAwMDAwMDAwLCJleHAiOjE3MDAwODY0MDB9.sig_other_3333"

	vaultA, vaultB, vaultOther := evalVaultSecretFromKey(keyA), evalVaultSecretFromKey(keyB), evalVaultSecretFromKey(keyOther)
	if vaultA != "rame-tenant-vault:user:10" || vaultB != vaultA {
		t.Fatalf("renewed mem2 key changed the vault pepper: A=%q B=%q", vaultA, vaultB)
	}
	if vaultOther == vaultA {
		t.Fatalf("different tenant must not share the vault pepper: %q", vaultOther)
	}
	if static := evalVaultSecretFromKey("static-shared-secret"); static != "static-shared-secret" {
		t.Fatalf("static secret must pass through unchanged: %q", static)
	}

	fpA, fpB := evalKeyFingerprint(keyA), evalKeyFingerprint(keyB)
	if len(fpA) != 8 || fpA == fpB {
		t.Fatalf("key fingerprint must be a short, rotation-sensitive digest: A=%q B=%q", fpA, fpB)
	}
	if strings.Contains(fpA, "mem2") || strings.Contains(fpA, "sig_rotation") {
		t.Fatalf("key fingerprint leaks key material: %q", fpA)
	}
	if again := evalKeyFingerprint(keyA); again != fpA {
		t.Fatalf("key fingerprint must be deterministic: %q != %q", again, fpA)
	}
}

func TestNormalizePulseTokenizerDefaultsToFTS5BM25(t *testing.T) {
	for _, in := range []string{"", " ", "fts5-bm25", "FTS5-BM25", "unknown"} {
		if got := normalizePulseTokenizer(in); got != PulseTokenizerFTS5BM25 {
			t.Fatalf("normalizePulseTokenizer(%q) = %q, want %q", in, got, PulseTokenizerFTS5BM25)
		}
	}
	for _, in := range []string{"standard", "STANDARD", "native", "default"} {
		if got := normalizePulseTokenizer(in); got != PulseTokenizerStandard {
			t.Fatalf("normalizePulseTokenizer(%q) = %q, want %q", in, got, PulseTokenizerStandard)
		}
	}
	if got := normalizePulseTokenizer("fts5-trigram"); got != PulseTokenizerFTS5Trigram {
		t.Fatalf("normalizePulseTokenizer(fts5-trigram) = %q, want %q", got, PulseTokenizerFTS5Trigram)
	}
	for _, lang := range []string{"en", "zh", "ja", "ko", "sea", "de", "multi"} {
		if got := normalizePulseTokenizer(lang); got != lang {
			t.Fatalf("normalizePulseTokenizer(%q) = %q, want %q", lang, got, lang)
		}
	}
}

func TestDefaultEvalPulseTokenizerEnablesEnglishSegmenterAndHarmonicTable(t *testing.T) {
	server := newCloudTestServer(t)
	eng := newTestCloudEngine(t, server, SuiteATM)

	hasHash := func(slots []ramesdk.BlindTokenAtom, target uint64) bool {
		for _, s := range slots {
			if s.H == target {
				return true
			}
		}
		return false
	}

	// 1. 量纲/单位谐波表生效："50 kg" 与 "50 kilograms" 共享 "@unit:mass:kg" / "@dim:mass" 谐波哈希
	pktSymbol, _ := eng.Store.PackBlindPacket("U_SYM", "50 kg", 1700000000)
	pktWord, _ := eng.Store.PackBlindPacket("U_WRD", "50 kilograms", 1700000000)
	unitHash := eng.Store.BlindEncoder.KeyedHash64("@unit:mass:kg")
	dimHash := eng.Store.BlindEncoder.KeyedHash64("@dim:mass")
	if !hasHash(pktSymbol.Slots, unitHash) || !hasHash(pktWord.Slots, unitHash) {
		t.Fatalf("expected default ATM eval tokenizer to emit @unit:mass:kg harmonic for both '50 kg' and '50 kilograms'")
	}
	if !hasHash(pktSymbol.Slots, dimHash) || !hasHash(pktWord.Slots, dimHash) {
		t.Fatalf("expected default ATM eval tokenizer to emit @dim:mass harmonic for both '50 kg' and '50 kilograms'")
	}

	// 2. 内联货币符号提取与谐波对齐："£655.59" 与 "655.59 GBP" 共享 "@unit:curr:gbp" 谐波
	pktInlineCurr, _ := eng.Store.PackBlindPacket("C_INLINE", "Total paid £655.59", 1700000000)
	pktCodeCurr, _ := eng.Store.PackBlindPacket("C_CODE", "Total paid 655.59 GBP", 1700000000)
	gbpUnitHash := eng.Store.BlindEncoder.KeyedHash64("@unit:curr:gbp")
	if !hasHash(pktInlineCurr.Slots, gbpUnitHash) || !hasHash(pktCodeCurr.Slots, gbpUnitHash) {
		t.Fatalf("expected inline £ symbol and GBP code to share @unit:curr:gbp harmonic")
	}

	// 3. 结构化 ISO 日期与英文自然月份默认对齐："2025-02-25" 产出 "february" 与 "cal:2025-02-25" 谐波
	pktDate, _ := eng.Store.PackBlindPacket("D_ISO", "Booked hotel on 2025-02-25", 1700000000)
	febHash := eng.Store.BlindEncoder.KeyedHash64("february")
	calHash := eng.Store.BlindEncoder.KeyedHash64("cal:2025-02-25")
	if !hasHash(pktDate.Slots, febHash) || !hasHash(pktDate.Slots, calHash) {
		t.Fatalf("expected default ATM eval tokenizer to emit February and cal:2025-02-25 harmonics")
	}

	// 4. ISO 3166 L0-L3 地理谐波外挂默认生效："Porto" 与 "Portugal" 共享 "@geo:cc:pt" 与 "@geo:cont:eu" 谐波
	pktCity, _ := eng.Store.PackBlindPacket("G_CITY", "Hotel in Porto", 1700000000)
	pktCountry, _ := eng.Store.PackBlindPacket("G_COUNTRY", "Hotel in Portugal", 1700000000)
	ptHash := eng.Store.BlindEncoder.KeyedHash64("@geo:cc:pt")
	euHash := eng.Store.BlindEncoder.KeyedHash64("@geo:cont:eu")
	if !hasHash(pktCity.Slots, ptHash) || !hasHash(pktCountry.Slots, ptHash) {
		t.Fatalf("expected 'Porto' and 'Portugal' to share @geo:cc:pt harmonic")
	}
	if !hasHash(pktCity.Slots, euHash) || !hasHash(pktCountry.Slots, euHash) {
		t.Fatalf("expected 'Porto' and 'Portugal' to share @geo:cont:eu harmonic")
	}

	// 5. 全语种计量/统计单位谐波表与无空格 SEA 单位提取默认全开："50 千克"、"50 キログラム"、"50 กิโลกรัม" 均共享 "@unit:mass:kg"
	pktZH, _ := eng.Store.PackBlindPacket("U_ZH", "精密仪器净重 50 千克", 1700000000)
	pktJA, _ := eng.Store.PackBlindPacket("U_JA", "重量は50キログラムです", 1700000000)
	pktSEA, _ := eng.Store.PackBlindPacket("U_SEA", "น้ำหนัก50กิโลกรัม", 1700000000)
	if !hasHash(pktZH.Slots, unitHash) || !hasHash(pktJA.Slots, unitHash) || !hasHash(pktSEA.Slots, unitHash) {
		t.Fatalf("expected default ATM eval tokenizer to enable all multilingual harmonic tables (zh/ja/sea @unit:mass:kg)")
	}
}

func TestTransportForPulseMode(t *testing.T) {
	cases := []struct {
		mode string
		want string
	}{
		{"", LocalPlainTransport},
		{PulseModePulseCipher, CloudTransport},
		{PulseModePulsePlain, PulsePlainTransport},
		{PulseModeLocalPlain, LocalPlainTransport},
	}
	for _, tc := range cases {
		if got := TransportForPulseMode(tc.mode); got != tc.want {
			t.Fatalf("TransportForPulseMode(%q) = %q, want %q", tc.mode, got, tc.want)
		}
	}
}

// TestLocalPlainManifestTransportMatchesMode 锁死本地明文臂落盘 manifest 的传输标签：
// summary 与逐题行的 engine_transport 早就是 ramesdk.local_plain.offline-v1，但 Ingest 写盘的 manifest
// 曾把 transport 硬写成 pulse_cipher 常量 —— 同一份工件里两个传输标签自相矛盾（provenance 张冠李戴）。
func TestLocalPlainManifestTransportMatchesMode(t *testing.T) {
	engine, err := NewCloudEngine(testConfig(t), SuiteATM, filepath.Join(t.TempDir(), "vault.json"),
		WithPulseMode(PulseModeLocalPlain))
	if err != nil {
		t.Fatalf("NewCloudEngine(local_plain): %v", err)
	}
	if engine.Endpoint != "local://sqlite-fts5-bm25" {
		t.Fatalf("local_plain engine must stay offline, got endpoint %q", engine.Endpoint)
	}
	documents := []CloudDocument{
		{ID: "doc-a", Text: "alpha memory", OccurredAt: 1},
		{ID: "doc-b", Text: "beta memory", OccurredAt: 2},
	}
	manifest, err := engine.Ingest(t.Context(), documents, false, 2, nil)
	if err != nil {
		t.Fatalf("local ingest: %v", err)
	}
	if manifest.Transport != LocalPlainTransport {
		t.Fatalf("local_plain manifest transport = %q, want %q", manifest.Transport, LocalPlainTransport)
	}
	if manifest.PulseMode != PulseModeLocalPlain || manifest.LocalFallback {
		t.Fatalf("local_plain manifest provenance = pulse_mode %q local_fallback %v", manifest.PulseMode, manifest.LocalFallback)
	}
}
