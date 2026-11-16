// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package ramesdk_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

func TestListCloudUnitIDsRequiresExactEnvelopeAndStats(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/open/memory/v1/units" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true, "tenant_id": "user", "scope": "u10-mem-eval-locomo",
			"count": 2, "unit_ids": []string{"doc-b", "doc-a"},
		})
	}))
	defer server.Close()
	client := ramesdk.NewClient(server.URL+"/open/memory/v1", "key", "10", ramesdk.NewMemoryStore([]byte("secret")))
	client.TenantID = "user"
	client.Scope = "u10-mem-eval-locomo"
	ids, err := client.ListCloudUnitIDs(t.Context())
	if err != nil {
		t.Fatalf("ListCloudUnitIDs: %v", err)
	}
	if len(ids) != 2 || ids[0] != "doc-a" || ids[1] != "doc-b" {
		t.Fatalf("unexpected sorted unit IDs: %v", ids)
	}
}

func TestListCloudUnitIDsRejectsCountMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true, "tenant_id": "user", "scope": "u10-mem-eval-locomo",
			"count": 2, "unit_ids": []string{"doc-a"},
		})
	}))
	defer server.Close()
	client := ramesdk.NewClient(server.URL+"/open/memory/v1", "key", "10", ramesdk.NewMemoryStore([]byte("secret")))
	client.TenantID = "user"
	client.Scope = "u10-mem-eval-locomo"
	if _, err := client.ListCloudUnitIDs(t.Context()); err == nil {
		t.Fatal("unit list count mismatch must fail closed")
	}
}
