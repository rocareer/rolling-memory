package rollingmemory

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestConsoleGuardActualMountAvailability(t *testing.T) {
	gin.SetMode(gin.TestMode)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ROLLING_MEMORY_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("ROLLING_MEMORY_SCOPE", "")
	t.Setenv("ROLLING_MEMORY_DB", filepath.Join(home, "missing.sqlite"))
	router := gin.New()
	mount(router.Group("/admin/plugin/rolling-memory"))

	for _, tc := range []struct {
		path string
		want int
	}{
		{"/ai/config", http.StatusOK},
		{"/ai/save", http.StatusOK},
		{"/background/select", http.StatusOK},
		{"/status", http.StatusForbidden},
		{"/eval/overview", http.StatusOK},
		{"/cloud/account/state", http.StatusOK},
	} {
		t.Setenv("ROLLING_MEMORY_MODE", "local")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/admin/plugin/rolling-memory"+tc.path, strings.NewReader(`{}`)))
		if tc.want == http.StatusForbidden {
			if w.Code != tc.want || !strings.Contains(w.Body.String(), "local_plain vault scope verification failed") {
				t.Errorf("%s: status=%d body=%s", tc.path, w.Code, w.Body.String())
			}
		} else if strings.Contains(w.Body.String(), "local_plain vault scope verification failed") {
			t.Errorf("%s unexpectedly hit vault guard", tc.path)
		}
	}

	for _, pulseMode := range []string{"pulse_plain", "pulse_cipher"} {
		t.Setenv("ROLLING_MEMORY_MODE", "cloud")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/admin/plugin/rolling-memory/graph", strings.NewReader(`{"pulse_mode":"`+pulseMode+`"}`)))
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "local_plain vault scope verification failed") {
			t.Errorf("%s graph: status=%d body=%s", pulseMode, w.Code, w.Body.String())
		}
	}
}

func TestConsoleVaultScopeGuardAdmitsBoundLegacyScope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ROLLING_MEMORY_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("ROLLING_MEMORY_SCOPE", "")
	t.Setenv("ROLLING_MEMORY_MODE", "local")

	cfgDir := filepath.Join(home, ".config", "rolling-memory")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(cfgDir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[core]\nscope = \"mem:self\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(home, "legacy.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE thin_vault_meta (k TEXT PRIMARY KEY, v); INSERT INTO thin_vault_meta VALUES ('local_plain_scope', 'mem:self')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	t.Setenv("ROLLING_MEMORY_DB", dbPath)

	if err := checkConsoleVaultScope(home); err != nil {
		t.Fatalf("bound legacy scope should pass: %v", err)
	}
	if got := resolveBoundVaultScope(dbPath, UserScope); got != LegacyUserScope {
		t.Fatalf("resolveBoundVaultScope(dbPath, %q) = %q, want %q", UserScope, got, LegacyUserScope)
	}

	router := gin.New()
	group := router.Group("/admin/plugin/rolling-memory")
	group.Use(consoleVaultScopeGuard())
	group.POST("/dashboard", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	for _, body := range []string{`{}`, `{"scope":"default"}`, `{"scope":"mem:self"}`} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/admin/plugin/rolling-memory/dashboard", strings.NewReader(body)))
		if w.Code != http.StatusOK {
			t.Fatalf("request %s should be admitted on bound legacy vault: status=%d body=%s", body, w.Code, w.Body.String())
		}
	}

	// A vault bound to a different scope than the configured one stays rejected.
	db2, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db2.Exec(`UPDATE thin_vault_meta SET v = 'default' WHERE k = 'local_plain_scope'`); err != nil {
		t.Fatal(err)
	}
	db2.Close()
	if err := checkConsoleVaultScope(home); err == nil {
		t.Fatal("binding/config mismatch must stay rejected")
	}
}

func TestPublicLocomoRoutesIsolateVaultButTrustSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ROLLING_MEMORY_MODE", "local")
	t.Setenv("ROLLING_MEMORY_EVAL_DATA", filepath.Join(home, "empty-eval"))
	dbPath := filepath.Join(home, "user.sqlite")
	t.Setenv("ROLLING_MEMORY_DB", dbPath)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE thin_vault_docs (doc_id TEXT, raw_text TEXT); INSERT INTO thin_vault_docs VALUES ('private-id', 'vault-secret-marker')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	snapshotDir := filepath.Join(home, ".local", "state", "rolling-memory", "eval")
	if err := os.MkdirAll(snapshotDir, 0o700); err != nil {
		t.Fatal(err)
	}
	snapshot := `{"active_scope":"default","units":{"demo":{"doc_id":"demo","raw_text":"public-demo-marker","address_uri":"cell#1"}},"nodes":[{"node_key":"unit:demo","kind":"unit","layer":"episodic","summary":"public-demo-marker","span":"public-demo-marker"}]}`
	if err := os.WriteFile(filepath.Join(snapshotDir, "locomo-static-snapshot.json"), []byte(snapshot), 0o600); err != nil {
		t.Fatal(err)
	}
	// Avoid repo snapshots in the process working directory; all paths remain in t.TempDir.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(home); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd); InvalidateLocomoPublicCorpusCache() })
	InvalidateLocomoPublicCorpusCache()
	router := gin.New()
	mountPublic(router.Group(""))
	for _, tc := range []struct{ path, body string }{
		{"/graph", `{"limit":10}`},
		{"/expand", `{"seed_keys":["unit:demo"],"depth":1}`},
		{"/recall", `{"q":"public-demo-marker","top_k":1}`},
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/plugin/rolling-memory/locomo"+tc.path, strings.NewReader(tc.body)))
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "vault-secret-marker") {
			t.Fatalf("%s leaked vault or failed: status=%d body=%s", tc.path, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "public-demo-marker") {
			t.Fatalf("%s did not read isolated snapshot: %s", tc.path, w.Body.String())
		}
	}

	// A user-controlled snapshot is not authenticated as benchmark data.
	snapshot = strings.ReplaceAll(snapshot, "public-demo-marker", "user-content-in-snapshot")
	if err := os.WriteFile(filepath.Join(snapshotDir, "locomo-static-snapshot.json"), []byte(snapshot), 0o600); err != nil {
		t.Fatal(err)
	}
	InvalidateLocomoPublicCorpusCache()
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/plugin/rolling-memory/locomo/graph", strings.NewReader(`{"limit":10}`)))
	if !strings.Contains(w.Body.String(), "user-content-in-snapshot") {
		t.Fatalf("expected unverified snapshot content to be exposed: %s", w.Body.String())
	}
}

func TestLocalPlainEvalAgentsTransmitContextToConfiguredModel(t *testing.T) {
	t.Setenv("ROLLING_MEMORY_MODE", "local")
	var requests []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requests = append(requests, body)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"yes"}}]}`))
	}))
	defer srv.Close()
	question := EvalQuestionItem{ID: "test", Question: "question-marker", Answer: "answer-marker", EvidenceTexts: []string{"eval-context-marker"}}
	cfg := AIModelConfig{BaseURL: srv.URL, AnswerModel: "model", JudgeModel: "model", TimeoutSec: 2}
	_, _, err := executeSingleQuestionEval(context.Background(), "", "", question, 0, EvalArmOracle, 1, true, cfg)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(requests)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || !strings.Contains(string(encoded), "eval-context-marker") || !strings.Contains(string(encoded), "question-marker") {
		t.Fatalf("unexpected model requests: %s", encoded)
	}
}

func TestCloudAccountMemoryProxyTransmitsRequestPlaintext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ROLLING_MEMORY_MODE", "cloud")
	var body map[string]any
	var auth, apiKey, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		auth, apiKey = r.Header.Get("Authorization"), r.Header.Get("X-Api-Key")
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"ok":true}}`))
	}))
	defer srv.Close()
	if err := saveCloudAccountState(CloudAccountState{BaseURL: srv.URL, Token: "test-token", APIKey: "test-key"}); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/cloud/account/memory", strings.NewReader(`{"action":"remember","content":"request-secret-marker","cue":"query-marker","sub_namespace":"private-scope"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	cloudAccountMemoryHandler(c)
	if path != "/user/memory/remember" || body["content"] != "request-secret-marker" || body["cue"] != "query-marker" || body["sub_namespace"] != "private-scope" || auth != "Bearer test-token" || apiKey != "test-key" {
		t.Fatalf("unexpected proxy request: path=%s body=%v auth=%q key=%q", path, body, auth, apiKey)
	}
}
