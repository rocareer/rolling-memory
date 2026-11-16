// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package eval

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

// TestLiveUpstreamEvaluationScopeIsolation is an opt-in, read-only cross-scope
// probe. It compares Unit IDs only and never prints real-user plaintext.
func TestLiveUpstreamEvaluationScopeIsolation(t *testing.T) {
	if os.Getenv("ROLLING_MEMORY_LIVE_EVAL") != "1" {
		t.Skip("set ROLLING_MEMORY_LIVE_EVAL=1 and the two isolated store paths to run")
	}
	locomoPath := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_LIVE_LOCOMO_STORE"))
	atmPath := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_LIVE_ATM_STORE"))
	if locomoPath == "" || atmPath == "" {
		t.Fatal("live isolation probe requires ROLLING_MEMORY_LIVE_LOCOMO_STORE and ROLLING_MEMORY_LIVE_ATM_STORE")
	}
	// 凭据按端侧唯一合法顺序解析（环境变量 → 显式配置 → 本机账户态签名钥），
	// 客户端不内置任何开发密钥。
	apiKey, err := cloudAPIKey(nil, defaultCloudEndpoint)
	if err != nil {
		t.Fatalf("live isolation probe requires a data-plane credential: %v", err)
	}
	t.Setenv("ROLLING_MEMORY_BRAIN_URL", defaultCloudEndpoint)
	t.Setenv("ROLLING_MEMORY_BRAIN_KEY", apiKey)
	t.Setenv("ROLLING_MEMORY_USER_ID", defaultCloudUserID)

	query := func(storePath, scope, text string) []string {
		store, err := ramesdk.NewJSONFileStore(storePath, []byte(evalVaultSecretFromKey(apiKey)))
		if err != nil {
			t.Fatalf("open isolated store: %v", err)
		}
		client := ramesdk.NewClient(defaultCloudEndpoint, apiKey, defaultCloudUserID, store)
		client.Scope = scope
		client.HTTPClient = &http.Client{Timeout: 120 * time.Second}
		if err := client.Health(t.Context()); err != nil {
			t.Fatalf("health: %v", err)
		}
		identity, err := client.VerifyAuth(t.Context())
		if err != nil {
			t.Fatalf("verify auth: %v", err)
		}
		client.TenantID = identity.TenantID
		hits, err := client.Recall(t.Context(), text, 100)
		if err != nil {
			t.Fatalf("recall scope %s: %v", scope, err)
		}
		ids := make([]string, 0, len(hits))
		for _, hit := range hits {
			ids = append(ids, hit.UnitID)
		}
		return ids
	}

	locomoIDs := query(locomoPath, Scope, "Where did I move from?")
	atmIDs := query(atmPath, ATMScope, "List all emails")
	if len(locomoIDs) == 0 || len(atmIDs) == 0 {
		t.Fatalf("live probe requires non-empty evaluation hits: locomo=%d atm=%d", len(locomoIDs), len(atmIDs))
	}
	assertDisjoint := func(leftName string, left []string, rightName string, right []string) {
		t.Helper()
		rightSet := make(map[string]bool, len(right))
		for _, id := range right {
			rightSet[id] = true
		}
		for _, id := range left {
			if rightSet[id] {
				t.Fatalf("scope isolation violated: %s unit %q also appears in %s", leftName, id, rightName)
			}
		}
	}
	assertDisjoint("locomo", locomoIDs, "atm", atmIDs)

	store, err := ramesdk.NewJSONFileStore(locomoPath, []byte(evalVaultSecretFromKey(apiKey)))
	if err != nil {
		t.Fatal(err)
	}
	packet, _ := store.PackBlindPacket("QUERY", "Where did I move from?", 0)
	payload := map[string]any{
		"tenant_id": "user", "user_id": defaultCloudUserID, "scope": "mem:self",
		"query": packet, "top_k": 100,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, defaultCloudEndpoint+"/query?envelope=1", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Rolling-Memory-Tenant", "user")
	req.Header.Set("X-Rolling-Memory-User", defaultCloudUserID)
	req.Header.Set("X-Rolling-Memory-Scope", "mem:self")
	resp, err := (&http.Client{Timeout: 120 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("real-user scope probe returned HTTP %d", resp.StatusCode)
	}
	var envelope struct {
		Success bool                    `json:"success"`
		Tenant  string                  `json:"tenant_id"`
		Scope   string                  `json:"scope"`
		Engine  string                  `json:"engine"`
		Count   int                     `json:"count"`
		Hits    []ramesdk.CloudQueryHit `json:"hits"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.Success || envelope.Tenant != "user" || envelope.Scope != "mem:self" || envelope.Engine != "autonoetic" || envelope.Count != len(envelope.Hits) {
		t.Fatalf("invalid real-user scope envelope: %+v", envelope)
	}
	realIDs := make([]string, 0, len(envelope.Hits))
	for _, hit := range envelope.Hits {
		realIDs = append(realIDs, hit.UnitID)
	}
	assertDisjoint("evaluation", append(append([]string{}, locomoIDs...), atmIDs...), "mem:self", realIDs)
	t.Logf("scope isolation verified: locomo=%d atm=%d mem:self=%d; no plaintext inspected", len(locomoIDs), len(atmIDs), len(realIDs))
}
