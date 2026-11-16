// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package ramesdk_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

func TestCloudPurgeJSONStoreDeletesAndFlushes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.json")
	store, err := ramesdk.NewJSONFileStore(path, []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"doc-1", "doc-2"} {
		if err := store.SaveLocalPlaintext(id, 1, "plaintext "+id, []string{"plaintext " + id}, ramesdk.CloudIngestResponse{UnitID: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/open/memory/v1/purge" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["tenant_id"] != "user" || body["scope"] != "u10-mem-eval-locomo" {
			t.Errorf("unexpected purge identity: %v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true, "tenant_id": "user", "scope": "u10-mem-eval-locomo",
			"mode": "units", "deleted_units": 1, "remaining_units": 1,
		})
	}))
	defer server.Close()
	client := ramesdk.NewClient(server.URL+"/open/memory/v1", "key", "10", store)
	client.TenantID = "user"
	client.Scope = "u10-mem-eval-locomo"
	if _, err := client.DeleteUnits(t.Context(), []string{"doc-1"}); err != nil {
		t.Fatalf("DeleteUnits: %v", err)
	}
	reloaded, err := ramesdk.NewJSONFileStore(path, []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := reloaded.LoadLocalDocument("doc-1"); err == nil {
		t.Fatal("purged document remained in the flushed JSON vault")
	}
	if _, _, _, err := reloaded.LoadLocalDocument("doc-2"); err != nil {
		t.Fatalf("unrelated document was removed: %v", err)
	}
}
