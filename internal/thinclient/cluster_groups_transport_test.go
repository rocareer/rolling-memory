package thinclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHTTPBrainTransportClusterGroupsDeliveryView pins protocol slice S4a
// (noetic-cluster-delivery-protocol-v1): requesting the cognitive-cluster
// delivery view switches the query to envelope mode, parses cluster_groups and
// ordinal_annotation off the object shell, exposes them via DeliveryView(),
// and keeps the default bare-array mode (plus view clearing) untouched for
// every other caller.
func TestHTTPBrainTransportClusterGroupsDeliveryView(t *testing.T) {
	var gotEnvelope bool
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotBody = map[string]any{}
		_ = json.Unmarshal(raw, &gotBody)
		gotEnvelope = r.URL.Query().Get("envelope") == "1"
		w.Header().Set("Content-Type", "application/json")
		if gotEnvelope {
			_, _ = w.Write([]byte(`{
				"success": true,
				"count": 2,
				"hits": [
					{"id": "u-1", "final_weight": 9.5, "address_uri": "", "address_coord64": 0, "waveform_bar": "", "wave_score": 0, "cell_wormhole": 0, "manifold_gain": 0},
					{"id": "u-2", "final_weight": 7.25, "address_uri": "", "address_coord64": 0, "waveform_bar": "", "wave_score": 0, "cell_wormhole": 0, "manifold_gain": 0}
				],
				"cluster_groups": [
					{"group_id": 1, "day": "2024-05-03", "unit_ids": ["u-1", "u-2"], "peak_unit_id": "u-1", "joint_energy_nats": 16.75}
				],
				"ordinal_annotation": {"polarity": "begin", "resolved_day": "2024-05-03", "single_run": true, "run_span_days": 4, "anchor_df": 59}
			}`))
			return
		}
		_, _ = w.Write([]byte(`[{"id": "u-1", "final_weight": 9.5, "address_uri": "", "address_coord64": 0, "waveform_bar": "", "wave_score": 0, "cell_wormhole": 0, "manifold_gain": 0}]`))
	}))
	defer srv.Close()

	tr := NewHTTPBrainTransport(srv.URL, "test-key")
	tr.WireGzip = WireGzipOff

	// 1. Envelope mode: flag forwarded, hits parsed off the shell, view captured.
	hits, err := tr.QueryBlindManifoldWithOptions(context.Background(),
		BlindPulsePacket{UnitID: "q", AtomsB64: "AA"},
		BlindQueryOptions{TopK: 5, ClusterGroups: true})
	if err != nil {
		t.Fatalf("envelope query: %v", err)
	}
	if !gotEnvelope {
		t.Fatalf("cluster_groups request must use envelope mode")
	}
	if flag, _ := gotBody["cluster_groups"].(bool); !flag {
		t.Fatalf("cluster_groups flag not forwarded: %#v", gotBody["cluster_groups"])
	}
	if len(hits) != 2 || hits[0].UnitID != "u-1" || hits[1].FinalWeight != 7.25 {
		t.Fatalf("envelope hits parse drift: %+v", hits)
	}
	view := tr.DeliveryView()
	if len(view.ClusterGroups) != 1 || view.ClusterGroups[0].PeakUnitID != "u-1" || len(view.ClusterGroups[0].UnitIDs) != 2 {
		t.Fatalf("cluster view parse drift: %+v", view.ClusterGroups)
	}
	if view.OrdinalAnnotation == nil || view.OrdinalAnnotation.Polarity != "begin" ||
		view.OrdinalAnnotation.ResolvedDay != "2024-05-03" || !view.OrdinalAnnotation.SingleRun {
		t.Fatalf("ordinal annotation parse drift: %+v", view.OrdinalAnnotation)
	}

	// 2. Bare-array mode stays byte-compatible and clears the stale view.
	hits, err = tr.QueryBlindManifoldWithOptions(context.Background(),
		BlindPulsePacket{UnitID: "q", AtomsB64: "AA"},
		BlindQueryOptions{TopK: 5})
	if err != nil {
		t.Fatalf("bare query: %v", err)
	}
	if gotEnvelope {
		t.Fatalf("default mode must not request envelope")
	}
	if _, present := gotBody["cluster_groups"]; present {
		t.Fatalf("omitempty flag leaked into default request: %#v", gotBody["cluster_groups"])
	}
	if len(hits) != 1 || hits[0].UnitID != "u-1" {
		t.Fatalf("bare hits parse drift: %+v", hits)
	}
	if view := tr.DeliveryView(); len(view.ClusterGroups) != 0 || view.OrdinalAnnotation != nil {
		t.Fatalf("stale delivery view leaked across calls: %+v", view)
	}
}
