// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/eval"
	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

func TestCloudEvalRecallUsesSDKAndRejectsRetiredScope(t *testing.T) {
	newEnv(t)
	storePath := filepath.Join(t.TempDir(), "eval-locomo.json")
	store, err := ramesdk.NewJSONFileStore(storePath, []byte("mem_test_cloud_eval"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveLocalPlaintext("unit-cloud-1", 1700000000, "I moved from Sweden last year.", []string{"I moved from Sweden last year."}, ramesdk.CloudIngestResponse{}); err != nil {
		t.Fatal(err)
	}
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/open/memory/v1/health":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "engine": "autonoetic", "version": "v1"})
		case "/open/memory/v1/auth/verify":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true, "valid": true, "engine": "autonoetic", "tenant_id": "user",
				"user_id_prefixes": []string{"user", "10", "u10"},
			})
		case "/open/memory/v1/query":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true, "tenant_id": "user", "scope": "u10-mem-eval-locomo",
				"engine": "autonoetic", "count": 1,
				"hits": []map[string]any{{"id": "unit-cloud-1", "final_weight": 1.0}},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	t.Setenv("ROLLING_MEMORY_MODE", "cloud")
	t.Setenv("ROLLING_MEMORY_BRAIN_URL", server.URL)
	t.Setenv("ROLLING_MEMORY_BRAIN_KEY", "mem_test_cloud_eval")
	t.Setenv("ROLLING_MEMORY_USER_ID", "10")
	result := run(t, "recall", "--db", storePath, "--scope", "u10-mem-eval-locomo", "--q", "Where did I move from?", "--json")
	if result.code != 0 {
		t.Fatalf("cloud eval recall failed: %d %s %s", result.code, result.stdout, result.stderr)
	}
	payload := jsonObject(t, result.stdout)
	meta := payload["meta"].(map[string]any)
	if meta["engine_transport"] != eval.CloudTransport || meta["engine_scope"] != "u10-mem-eval-locomo" || meta["local_fallback"] != false {
		t.Fatalf("cloud provenance = %v", meta)
	}
	hits := payload["raw_hits"].([]any)
	if len(hits) != 1 || !strings.Contains(result.stdout, "Sweden") {
		t.Fatalf("cloud raw hits = %v", payload)
	}
	retired := run(t, "recall", "--scope", "mem:eval:ice200", "--q", "must not run", "--json")
	if retired.code == 0 || !strings.Contains(retired.stderr+retired.stdout, "retired") {
		t.Fatalf("retired eval scope must fail closed: %+v", retired)
	}
}
