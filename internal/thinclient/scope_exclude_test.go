// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestQueryPayloadCarriesExcludeScopeTokens pins that ordinary cloud queries declare
// the dedicated-scope exclude list (evaluation generations "*-mem-eval-*") so the
// engine can keep sibling-incarnation resonance away from them; clearing the list
// omits the wire field entirely.
func TestQueryPayloadCarriesExcludeScopeTokens(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got = map[string]any{}
		_ = json.Unmarshal(raw, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	tr := NewHTTPBrainTransport(srv.URL, "test-key")
	tr.WireGzip = WireGzipOff
	if _, err := tr.QueryBlindManifold(context.Background(), BlindPulsePacket{UnitID: "q", AtomsB64: "AA"}, 5); err != nil {
		t.Fatalf("query: %v", err)
	}
	tokens, _ := got["exclude_scope_tokens"].([]any)
	want := DefaultSiblingExcludeScopeTokens()
	if len(tokens) != len(want) {
		t.Fatalf("exclude_scope_tokens = %#v, want %#v", got["exclude_scope_tokens"], want)
	}
	for i, value := range tokens {
		if value != want[i] {
			t.Fatalf("exclude_scope_tokens[%d] = %#v, want %q", i, value, want[i])
		}
	}
	if got := []string{tokens[0].(string), tokens[1].(string)}; got[0] != "-mem-eval-locomo" || got[1] != "-mem-eval-atmbench" {
		t.Fatalf("default tokens = %#v, want marker+suite shapes", got)
	}

	tr.ExcludeScopeTokens = nil
	if _, err := tr.QueryBlindManifold(context.Background(), BlindPulsePacket{UnitID: "q", AtomsB64: "AA"}, 5); err != nil {
		t.Fatalf("query: %v", err)
	}
	if _, ok := got["exclude_scope_tokens"]; ok {
		t.Fatalf("cleared tokens still sent: %#v", got["exclude_scope_tokens"])
	}
}
