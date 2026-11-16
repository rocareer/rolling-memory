// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package ramesdk_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

func TestCloudSDKNewClientDefaultsTenantIDToUserID(t *testing.T) {
	client := ramesdk.NewClient("", "", " 10 ", ramesdk.NewMemoryStore(nil))
	if client.UserID != "10" || client.TenantID != "10" {
		t.Fatalf("single-tenant defaults: user_id=%q tenant_id=%q", client.UserID, client.TenantID)
	}
}

func TestCloudSDKVerifyAuthReturnsIdentityWithoutMutation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/auth/verify" {
			t.Errorf("unexpected auth request: method=%s path=%s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		writeCloudTestJSON(t, w, map[string]any{
			"success": true, "valid": true, "version": "v1", "engine": "autonoetic",
			"tenant_id": "user", "user_id_prefixes": []string{"user", "10", "u10"},
			"exact_namespace": false, "locked_user_id": "", "is_manager": false,
		})
	}))
	defer server.Close()
	client := ramesdk.NewClient(server.URL, "cloud-test-token", "10", ramesdk.NewMemoryStore(nil))
	client.HTTPClient = server.Client()
	if client.TenantID != "10" {
		t.Fatalf("test setup tenant_id = %q, want default 10", client.TenantID)
	}

	identity, err := client.VerifyAuth(context.Background())
	if err != nil {
		t.Fatalf("VerifyAuth: %v", err)
	}
	if !identity.Success || !identity.Valid || identity.Engine != "autonoetic" || identity.TenantID != "user" {
		t.Fatalf("unexpected verified identity: %+v", identity)
	}
	if !reflect.DeepEqual(identity.UserIDPrefixes, []string{"user", "10", "u10"}) {
		t.Fatalf("user_id_prefixes = %v", identity.UserIDPrefixes)
	}
	if identity.ExactNamespace || identity.LockedUserID != "" || identity.IsManager {
		t.Fatalf("unexpected key binding flags: %+v", identity)
	}
	if client.TenantID != "10" {
		t.Fatalf("VerifyAuth mutated Client.TenantID to %q", client.TenantID)
	}
	client.TenantID = identity.TenantID
	if client.TenantID != "user" {
		t.Fatalf("caller could not apply verified tenant: %q", client.TenantID)
	}
}

func TestCloudSDKVerifyAuthReturnsNonZeroBindingFlags(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeCloudTestJSON(t, w, map[string]any{
			"success": true, "valid": true, "engine": "autonoetic", "tenant_id": "user",
			"user_id_prefixes": []string{"10"}, "exact_namespace": true,
			"locked_user_id": "10", "is_manager": true,
		})
	}))
	defer server.Close()
	client := ramesdk.NewClient(server.URL, "cloud-test-token", "10", ramesdk.NewMemoryStore(nil))
	client.HTTPClient = server.Client()
	identity, err := client.VerifyAuth(context.Background())
	if err != nil {
		t.Fatalf("VerifyAuth: %v", err)
	}
	if !identity.ExactNamespace || identity.LockedUserID != "10" || !identity.IsManager {
		t.Fatalf("non-zero binding flags were not returned: %+v", identity)
	}
}

func TestCloudSDKVerifyAuthRejectsInvalidResponse(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "success false", status: http.StatusOK, body: `{"success":false,"valid":true,"engine":"autonoetic"}`},
		{name: "success missing", status: http.StatusOK, body: `{"valid":true,"engine":"autonoetic"}`},
		{name: "valid false", status: http.StatusOK, body: `{"success":true,"valid":false,"engine":"autonoetic"}`},
		{name: "valid missing", status: http.StatusOK, body: `{"success":true,"engine":"autonoetic"}`},
		{name: "engine mismatch", status: http.StatusOK, body: `{"success":true,"valid":true,"engine":"cloud"}`},
		{name: "http failure", status: http.StatusUnauthorized, body: `{"success":false,"valid":false}`},
		{name: "invalid json", status: http.StatusOK, body: `{"success":`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client := ramesdk.NewClient(server.URL, "cloud-test-token", "10", ramesdk.NewMemoryStore(nil))
			client.HTTPClient = server.Client()
			identity, err := client.VerifyAuth(context.Background())
			if err == nil {
				t.Fatalf("expected VerifyAuth error, got identity=%+v", identity)
			}
			if client.TenantID != "10" {
				t.Fatalf("failed VerifyAuth mutated TenantID to %q", client.TenantID)
			}
		})
	}
	client := ramesdk.NewClient("", "", "10", ramesdk.NewMemoryStore(nil))
	if _, err := client.VerifyAuth(context.Background()); err == nil {
		t.Fatal("expected Endpoint-free VerifyAuth error")
	}
}

func TestCloudSDKDeleteUnitsDeletesLocalOnlyAfterVerifiedCloudSuccess(t *testing.T) {
	store := ramesdk.NewMemoryStore([]byte("cloud-test-secret"))
	saveCloudTestDocument(t, store, "cloud-doc", "cloud fallback marker remote")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/purge" {
			t.Errorf("unexpected purge request: method=%s path=%s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		if _, _, _, err := store.LoadLocalDocument("cloud-doc"); err != nil {
			t.Errorf("local plaintext was deleted before the cloud response: %v", err)
		}
		var req struct {
			TenantID string   `json:"tenant_id"`
			UserID   string   `json:"user_id"`
			Scope    string   `json:"scope"`
			Mode     string   `json:"mode"`
			UnitIDs  []string `json:"unit_ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode purge request: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.TenantID != "user" || req.UserID != "10" || req.Scope != cloudTestScope || req.Mode != "units" || !reflect.DeepEqual(req.UnitIDs, []string{"cloud-doc"}) {
			t.Errorf("unexpected purge payload: %+v", req)
		}
		if got := r.Header.Get("X-Rolling-Memory-Tenant"); got != "user" {
			t.Errorf("tenant header = %q, want user", got)
		}
		writeCloudTestJSON(t, w, map[string]any{
			"success": true, "tenant_id": "user", "scope": cloudTestScope,
			"mode": "units", "deleted_units": 1, "remaining_units": 0,
		})
	}))
	defer server.Close()

	resp, err := newCloudTestClient(server, store).DeleteUnits(context.Background(), []string{"cloud-doc"})
	if err != nil {
		t.Fatalf("DeleteUnits: %v", err)
	}
	if !resp.Success || resp.TenantID != "user" || resp.Scope != cloudTestScope || resp.DeletedUnits != 1 {
		t.Fatalf("unexpected purge response: %+v", resp)
	}
	if _, _, _, err := store.LoadLocalDocument("cloud-doc"); err == nil {
		t.Fatal("local plaintext remains after verified cloud deletion")
	}
}

func TestCloudSDKDeleteUnitsFailuresPreserveLocalPlaintext(t *testing.T) {
	tests := []struct {
		name   string
		status int
		mutate func(map[string]any)
	}{
		{name: "http forbidden", status: http.StatusForbidden, mutate: func(resp map[string]any) { resp["success"] = false }},
		{name: "success false", status: http.StatusOK, mutate: func(resp map[string]any) { resp["success"] = false }},
		{name: "success missing", status: http.StatusOK, mutate: func(resp map[string]any) { delete(resp, "success") }},
		{name: "tenant mismatch", status: http.StatusOK, mutate: func(resp map[string]any) { resp["tenant_id"] = "10" }},
		{name: "tenant missing", status: http.StatusOK, mutate: func(resp map[string]any) { delete(resp, "tenant_id") }},
		{name: "scope mismatch", status: http.StatusOK, mutate: func(resp map[string]any) { resp["scope"] = "mem-eval-u10-atm" }},
		{name: "scope missing", status: http.StatusOK, mutate: func(resp map[string]any) { delete(resp, "scope") }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := ramesdk.NewMemoryStore([]byte("cloud-test-secret"))
			saveCloudTestDocument(t, store, "cloud-doc", "cloud fallback marker remote")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if _, _, _, err := store.LoadLocalDocument("cloud-doc"); err != nil {
					t.Errorf("local plaintext was deleted before the cloud response: %v", err)
				}
				resp := map[string]any{
					"success": true, "tenant_id": "user", "scope": cloudTestScope,
					"mode": "units", "deleted_units": 1,
				}
				tc.mutate(resp)
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(resp)
			}))
			defer server.Close()

			resp, err := newCloudTestClient(server, store).DeleteUnits(context.Background(), []string{"cloud-doc"})
			if err == nil {
				t.Fatalf("expected purge failure, got response=%+v", resp)
			}
			if resp.Success {
				t.Fatalf("failed purge masqueraded as success: %+v", resp)
			}
			if _, _, _, loadErr := store.LoadLocalDocument("cloud-doc"); loadErr != nil {
				t.Fatalf("failed cloud purge deleted local plaintext: %v", loadErr)
			}
		})
	}
}

func TestCloudSDKDeleteUnitsWithoutEndpointKeepsLocalDeletion(t *testing.T) {
	store := ramesdk.NewMemoryStore([]byte("cloud-test-secret"))
	saveCloudTestDocument(t, store, "local-doc", "cloud fallback marker local")
	client := ramesdk.NewClient("", "", "10", store)
	resp, err := client.DeleteUnits(context.Background(), []string{"local-doc"})
	if err != nil || !resp.Success || resp.DeletedUnits != 1 {
		t.Fatalf("local DeleteUnits: response=%+v err=%v", resp, err)
	}
	if _, _, _, err := store.LoadLocalDocument("local-doc"); err == nil {
		t.Fatal("local-only deletion did not remove plaintext")
	}
}
