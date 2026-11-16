// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/rocareer/rolling-memory/internal/thinclient"
	_ "modernc.org/sqlite"
)

// TestClientStampsPersonaIdentity verifies the persona coordinate contract for unlimited
// parallel incarnations: whatever the caller sets on the client reaches BOTH the wire pulse
// (assistant_id / incarnation_id / locus, plus matching headers) and the locally saved row,
// so any incarnation can later answer when / where / what without a wider envelope.
func TestClientStampsPersonaIdentity(t *testing.T) {
	var mu sync.Mutex
	var body map[string]any
	var headers http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		_ = json.NewDecoder(r.Body).Decode(&body)
		headers = r.Header.Clone()
		mu.Unlock()
		_, _ = w.Write([]byte(`{"id":"ep_idtest","layer":"episodic","epoch":1,"address_uri":"C6:#1"}`))
	}))
	defer srv.Close()

	db, err := sql.Open("sqlite", "file:identity_test?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	vault, err := thinclient.NewVault(db, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}
	tr := thinclient.NewHTTPBrainTransport(srv.URL, "mem_test")
	tr.WireGzip = thinclient.WireGzipOff
	client := thinclient.NewClient(vault, tr)
	client.SetIdentity("reviewer", "inc-07", "Rolling-Memory")

	if _, err := client.RememberWithLayer(
		context.Background(), "ep_idtest", "并行分身身份坐标写入验证。", 1793102400, thinclient.LayerEpisodic, nil,
	); err != nil {
		t.Fatalf("RememberWithLayer: %v", err)
	}

	mu.Lock()
	gotAssistant, _ := body["assistant_id"].(string)
	gotIncarnation, _ := body["incarnation_id"].(string)
	gotLocus, _ := body["locus"].(string)
	hAssistant := headers.Get("X-Rolling-Memory-Assistant")
	hIncarnation := headers.Get("X-Rolling-Memory-Incarnation")
	hLocus := headers.Get("X-Rolling-Memory-Locus")
	mu.Unlock()

	if gotAssistant != "reviewer" || gotIncarnation != "inc-07" || gotLocus != "Rolling-Memory" {
		t.Fatalf("wire persona = assistant=%q incarnation=%q locus=%q", gotAssistant, gotIncarnation, gotLocus)
	}
	if hAssistant != "reviewer" || hIncarnation != "inc-07" || hLocus != "Rolling-Memory" {
		t.Fatalf("header persona = assistant=%q incarnation=%q locus=%q", hAssistant, hIncarnation, hLocus)
	}

	var storedAssistant, storedIncarnation, storedLocus string
	if err := db.QueryRow(
		`SELECT assistant_id, incarnation_id, locus FROM thin_vault_docs WHERE doc_id = 'ep_idtest'`,
	).Scan(&storedAssistant, &storedIncarnation, &storedLocus); err != nil {
		t.Fatalf("local row persona: %v", err)
	}
	if storedAssistant != "reviewer" || storedIncarnation != "inc-07" || storedLocus != "Rolling-Memory" {
		t.Fatalf("local persona = assistant=%q incarnation=%q locus=%q", storedAssistant, storedIncarnation, storedLocus)
	}
}

// TestNoeticRecordKeepsIncarnation verifies the L3 Noetic row keeps the crystallizing
// incarnation, so two parallel incarnations answering the same question stay distinguishable.
func TestNoeticRecordKeepsIncarnation(t *testing.T) {
	db, err := sql.Open("sqlite", "file:noetic_identity_test?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	vault, err := thinclient.NewVault(db, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}
	client := thinclient.NewClient(vault, thinclient.NewLocalVaultTransport(vault))
	client.SetIdentity("reviewer", "inc-09", "Rolling-Memory")

	if err := client.RememberNoetic("nt_idtest", "何时改了 vault.go", nil, nil, "已确认。", 1793102400); err != nil {
		t.Fatalf("RememberNoetic: %v", err)
	}
	var storedAssistant, storedIncarnation string
	if err := db.QueryRow(
		`SELECT assistant_id, incarnation_id FROM thin_vault_noetic WHERE noetic_id = 'nt_idtest'`,
	).Scan(&storedAssistant, &storedIncarnation); err != nil {
		t.Fatalf("noetic persona: %v", err)
	}
	if storedAssistant != "reviewer" || storedIncarnation != "inc-09" {
		t.Fatalf("noetic persona = assistant=%q incarnation=%q", storedAssistant, storedIncarnation)
	}
}
