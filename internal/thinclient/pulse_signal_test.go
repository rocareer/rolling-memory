// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/cloudcred"
	"github.com/rocareer/rolling-memory/internal/thinclient"
	_ "modernc.org/sqlite"
)

func openTestVault(t *testing.T) *thinclient.Vault {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "vault_signal.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	v, err := thinclient.NewVault(db, []byte("user-local-master-key-2026"))
	if err != nil {
		t.Fatalf("NewVault failed: %v", err)
	}
	return v
}

func hasWarningCode(rep thinclient.PulseSignalReport, code string) bool {
	for _, w := range rep.Warnings {
		if w.Code == code {
			return true
		}
	}
	return false
}

func TestEvaluatePulseSignal_HealthyMultilingualAndHarmonic(t *testing.T) {
	v := openTestVault(t)

	// 1. Mixed English + CJK + Compound Identifiers
	text := "Rolling-Memory Engine v2.4 uses WaveCellManifold addressing. 洛灵自知记忆引擎在端侧锁死明文，仅上传盲态整数脉冲。 Zero plaintext leaves the local SQLite vault."
	pkt, _ := v.PackBlindPacket("doc-healthy", text, 1700000000)
	rep := thinclient.EvaluatePulseSignal(pkt)

	if rep.Score < 85 || (rep.Grade != "S" && rep.Grade != "A") {
		t.Fatalf("expected healthy score >= 85 (S/A), got score=%d grade=%s warnings=%+v", rep.Score, rep.Grade, rep.Warnings)
	}
	if rep.NeedsAttention {
		t.Fatalf("expected NeedsAttention=false on healthy packet, got warnings=%+v", rep.Warnings)
	}
	if rep.FoldedBiUniSpans < 1 {
		t.Fatalf("expected CJK 2M-1 dual-track span to be recognized as FoldedBiUniSpans >= 1, got %d", rep.FoldedBiUniSpans)
	}
	if rep.CompoundHarmonicGroups < 2 {
		t.Fatalf("expected compound tokens (Rolling-Memory, WaveCellManifold) to be recognized as CompoundHarmonicGroups >= 2, got %d", rep.CompoundHarmonicGroups)
	}

	// 2. Single Chinese sentence with 2M-1 dual track (>48 raw slots, but <48 effective unfolded units) must NOT false-positive DEGENERATE_SENTENCE_FRAMES
	zhSinglePkt, _ := v.PackBlindPacket("doc-zh-single", "Rolling-Memory 现已全面切换为薄客户端架构，不再维护本地核心算法，100% 走云端 /open/memory/v1 接口，绑定 user_id=10。", 1700000005)
	zhRep := thinclient.EvaluatePulseSignal(zhSinglePkt)
	if zhRep.TotalSlots < 48 {
		t.Fatalf("expected raw slots >= 48 on CJK dual-track sentence, got %d", zhRep.TotalSlots)
	}
	if zhRep.NeedsAttention || len(zhRep.Warnings) > 0 || zhRep.Score < 95 {
		t.Fatalf("expected zero warnings and Score >= 95 on normal single CJK sentence after companion-slot unfolding, got score=%d warnings=%+v",
			zhRep.Score, zhRep.Warnings)
	}

	// 3. Second-order adversarial spoofing: fake compound parent (B=160, FlagHasConn) + 8 single-char dust tokens (B=20)
	// must NOT be accepted as a valid compound harmonic group to bypass OVER_FRAGMENTED_DUST.
	spoofSlots := []thinclient.BlindTokenAtom{
		{H: 0xABCDEF01, RawBitsX10: 160, ClassFlags: thinclient.FlagHasConn},
	}
	for i := 0; i < 8; i++ {
		spoofSlots = append(spoofSlots, thinclient.BlindTokenAtom{
			H:          uint64(0x100 + i),
			RawBitsX10: 20,
			ClassFlags: 0,
		})
	}
	spoofRep := thinclient.EvaluatePulseSignal(thinclient.BlindPulsePacket{
		UnitID:         "doc-spoof-dust",
		Slots:          spoofSlots,
		SentenceFrames: [][]thinclient.BlindTokenAtom{spoofSlots},
	})
	if spoofRep.CompoundHarmonicGroups != 0 || !hasWarningCode(spoofRep, thinclient.WarnOverFragmentedDust) {
		t.Fatalf("expected fake single-char dust compound group to be rejected (CompoundHarmonicGroups=0) and flagged as OVER_FRAGMENTED_DUST, got groups=%d warnings=%+v",
			spoofRep.CompoundHarmonicGroups, spoofRep.Warnings)
	}
}

func TestEvaluatePulseSignal_DetectsPathologicalCustomSegmenters(t *testing.T) {
	v := openTestVault(t)
	sample := "Distributed consensus protocols require deterministic state machine replication across fault-tolerant storage nodes. Leader election timers prevent split-brain partitions."

	// Case 1: Coarse unsegmented segmenter with auto-harmonic explicitly disabled (raw pass-through)
	coarsePkt, _ := v.PackBlindPacketWithSegmenter("doc-coarse", sample, 1700000001, thinclient.SegmenterPackOptions{
		Segmenter: func(sentence string) []string {
			return []string{strings.ReplaceAll(sentence, " ", "")}
		},
		DisableAutoHarmonic: true,
	})
	coarseRep := thinclient.EvaluatePulseSignal(coarsePkt)
	if !coarseRep.NeedsAttention || !hasWarningCode(coarseRep, thinclient.WarnCoarseUnsegmentedCarrier) {
		t.Fatalf("expected COARSE_UNSEGMENTED_CARRIER warning on raw glued segmenter, got score=%d warnings=%+v", coarseRep.Score, coarseRep.Warnings)
	}
	if coarseRep.GluedSlotPermille < 900 {
		t.Fatalf("expected GluedSlotPermille >= 900, got %d", coarseRep.GluedSlotPermille)
	}

	// Case 1b: Exact same coarse unsegmented segmenter with DEFAULT options (Adaptive Harmonic ON by default!)
	// must automatically synthesize V13-conserving sub-carrier harmonics and score >= 90 (S) with 0 warnings!
	autoHealedPkt, _ := v.PackBlindPacketWithSegmenter("doc-coarse-autohealed", sample, 1700000001, thinclient.SegmenterPackOptions{
		Segmenter: func(sentence string) []string {
			return []string{strings.ReplaceAll(sentence, " ", "")}
		},
	})
	autoHealedRep := thinclient.EvaluatePulseSignal(autoHealedPkt)
	if autoHealedRep.Score < 90 || autoHealedRep.NeedsAttention || len(autoHealedRep.Warnings) != 0 || autoHealedRep.CompoundHarmonicGroups < 2 {
		t.Fatalf("expected default Adaptive Harmonic mode to heal glued segmenter to Score>=90 (0 warnings, CompoundHarmonicGroups>=2), got score=%d groups=%d warnings=%+v",
			autoHealedRep.Score, autoHealedRep.CompoundHarmonicGroups, autoHealedRep.Warnings)
	}

	// Case 2: Over-fragmented single-character dust segmenter (raw pass-through)
	dustPkt, _ := v.PackBlindPacketWithSegmenter("doc-dust", strings.ToLower(sample), 1700000002, thinclient.SegmenterPackOptions{
		Segmenter: func(sentence string) []string {
			var chars []string
			for _, r := range sentence {
				if r >= 'a' && r <= 'z' {
					chars = append(chars, string(r))
				}
			}
			return chars
		},
		DisableAutoHarmonic: true,
	})
	dustRep := thinclient.EvaluatePulseSignal(dustPkt)
	if !dustRep.NeedsAttention || !hasWarningCode(dustRep, thinclient.WarnOverFragmentedDust) {
		t.Fatalf("expected OVER_FRAGMENTED_DUST warning on single-char segmenter, got score=%d warnings=%+v", dustRep.Score, dustRep.Warnings)
	}
	if dustRep.Score >= 75 {
		t.Fatalf("expected degraded score < 75 on single-char dust segmenter, got %d", dustRep.Score)
	}

	// Case 3: Degenerate sentence framing (60+ slots jammed into 1 single sentence frame)
	var longWords []string
	for i := 0; i < 60; i++ {
		longWords = append(longWords, fmt.Sprintf("module_%02d", i))
	}
	unframedPkt, _ := v.PackBlindPacket("doc-unframed", strings.Join(longWords, " "), 1700000003)
	unframedRep := thinclient.EvaluatePulseSignal(unframedPkt)
	if !hasWarningCode(unframedRep, thinclient.WarnDegenerateSentenceFrames) {
		t.Fatalf("expected DEGENERATE_SENTENCE_FRAMES warning when 60+ slots have only 1 frame, got %+v", unframedRep.Warnings)
	}

	// Case 4: Tandem repetition noise
	var tandemWords []string
	for i := 0; i < 24; i++ {
		tandemWords = append(tandemWords, "sep", "sep", "sep")
	}
	tandemPkt, _ := v.PackBlindPacket("doc-tandem", strings.Join(tandemWords, " "), 1700000004)
	tandemRep := thinclient.EvaluatePulseSignal(tandemPkt)
	if !hasWarningCode(tandemRep, thinclient.WarnHighTandemRedundancy) {
		t.Fatalf("expected HIGH_TANDEM_REDUNDANCY warning, got %+v", tandemRep.Warnings)
	}

	// Case 5: Co-occurrence horizon truncation (>96 unique tokens in one unit)
	var manySents []string
	for s := 0; s < 12; s++ {
		var w []string
		for k := 0; k < 10; k++ {
			w = append(w, fmt.Sprintf("concept_%02d_%02d", s, k))
		}
		manySents = append(manySents, strings.Join(w, " ")+".")
	}
	overflowPkt, _ := v.PackBlindPacket("doc-overflow", strings.Join(manySents, " "), 1700000005)
	overflowRep := thinclient.EvaluatePulseSignal(overflowPkt)
	if overflowRep.CooccurCoveredUnique != 64 || overflowRep.CooccurTruncatedUnique <= 32 {
		t.Fatalf("expected CooccurCoveredUnique=64 and CooccurTruncatedUnique>32, got covered=%d trunc=%d",
			overflowRep.CooccurCoveredUnique, overflowRep.CooccurTruncatedUnique)
	}
	if !hasWarningCode(overflowRep, thinclient.WarnCooccurHorizonTruncated) {
		t.Fatalf("expected COOCCUR_HORIZON_TRUNCATED warning, got %+v", overflowRep.Warnings)
	}
}

func TestPackBlindPacketWithSegmenter_HarmonicOverlayPreservesRecallAndConservation(t *testing.T) {
	v := openTestVault(t)

	// User custom domain segmenter configured directly on Vault via SetWordSegmenter (Adaptive Harmonic ON by default)
	customSeg := func(sentence string) []string {
		return []string{"zero plaintext vault", "protects", "blind pulse stream"}
	}
	v.SetWordSegmenter(customSeg)
	pkt, _ := v.PackBlindPacket(
		"doc-harmonic",
		"zero plaintext vault protects blind pulse stream.",
		1700000010,
	)
	v.SetWordSegmenter(nil)

	// Also verify default SplitWords path automatically synthesizes sub-carrier harmonics on runaway unspaced Kana/glued runs (>42 bits)
	kanaPkt, _ := v.PackBlindPacket(
		"doc-runaway-kana",
		"ありがとうございますよろしくお願い致します。",
		1700000011,
	)
	kanaRep := thinclient.EvaluatePulseSignal(kanaPkt)
	if kanaRep.GluedSlotCount != 0 || kanaRep.CompoundHarmonicGroups < 1 || kanaRep.Score < 90 {
		t.Fatalf("expected default PackBlindPacket to auto-harmonize long Kana run (GluedSlotCount=0, CompoundHarmonicGroups>=1, Score>=90), got %+v", kanaRep)
	}

	rep := thinclient.EvaluatePulseSignal(pkt)
	if rep.CompoundHarmonicGroups < 2 {
		t.Fatalf("expected >= 2 V13-compatible CompoundHarmonicGroups from HarmonicOverlay, got %d", rep.CompoundHarmonicGroups)
	}
	if rep.Score < 85 || rep.NeedsAttention {
		t.Fatalf("expected high signal score >= 85 with zero warnings on HarmonicOverlay packet, got score=%d warnings=%+v", rep.Score, rep.Warnings)
	}

	// Verify both the custom phrase hash ("zero plaintext vault") AND standard subword hashes ("zero", "plaintext", "vault")
	// exist in the emitted slots so both custom-segmented queries and standard SplitWords queries match!
	slotHashes := make(map[uint64]bool, len(pkt.Slots))
	for _, s := range pkt.Slots {
		slotHashes[s.H] = true
	}
	for _, token := range []string{"zero plaintext vault", "zero", "plaintext", "vault", "blind pulse stream", "blind", "pulse", "stream"} {
		wantH := v.KeyedHash64(token)
		if !slotHashes[wantH] {
			t.Fatalf("expected slotHashes to contain commitment for %q (H=%x)", token, wantH)
		}
	}
}

func TestInspectVaultPulseSignalAndCloudIngestTelemetryWireRoundTrip(t *testing.T) {
	v := openTestVault(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var pkt thinclient.BlindPulsePacket
		if err := json.Unmarshal(body, &pkt); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if pkt.AtomsB64 != "" {
			atoms, offsets, err := thinclient.DecodePulsePayloadB64(pkt.AtomsB64)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			pkt.Slots = atoms
			prev := 0
			for _, end := range offsets {
				pkt.SentenceFrames = append(pkt.SentenceFrames, atoms[prev:end])
				prev = end
			}
		}
		sigRep := thinclient.EvaluatePulseSignal(pkt)
		resp := thinclient.CloudIngestResponse{
			UnitID:              pkt.UnitID,
			Layer:               "episodic",
			Epoch:               1,
			ConservedMass:       4.2,
			QualityMult:         1.0,
			AddressURI:          "C6:L3:B001/C6:L2:B001/C6:L1:B001#00",
			AddressCoord64:      0x0100040000100100,
			PulseSignalScore:    sigRep.Score,
			PulseSignalGrade:    sigRep.Grade,
			PulseSignalWarnings: sigRep.Warnings,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	tr := thinclient.NewHTTPBrainTransport(srv.URL, "test-token")
	cli := thinclient.NewClient(v, tr)

	ingResp, err := cli.Remember(
		context.Background(),
		"DOC-SIG-1",
		"Rolling-Memory Engine computes wave resonance over blind integer pulses. Local SQLite vault retains full plaintext sovereignty.",
		1700000100,
	)
	if err != nil {
		t.Fatalf("Remember failed: %v", err)
	}
	if ingResp.PulseSignalScore < 85 || (ingResp.PulseSignalGrade != "S" && ingResp.PulseSignalGrade != "A") {
		t.Fatalf("expected wire-returned PulseSignalScore >= 85 (S/A), got score=%d grade=%q",
			ingResp.PulseSignalScore, ingResp.PulseSignalGrade)
	}

	if !v.HasFTS5BM25() {
		t.Fatalf("expected SQLite FTS5 + BM25 to be enabled by default on Vault")
	}
	bm25Map, err := v.QueryFTS5BM25("blind integer pulses", 10)
	if err != nil || bm25Map["DOC-SIG-1"] <= 0 {
		t.Fatalf("expected non-zero SQLite FTS5 BM25 milli-score for DOC-SIG-1, got map=%v err=%v", bm25Map, err)
	}

	vaultRep, err := v.InspectVaultPulseSignal(16, thinclient.SegmenterPackOptions{})
	if err != nil {
		t.Fatalf("InspectVaultPulseSignal failed: %v", err)
	}
	if vaultRep.Score < 85 || vaultRep.TotalSlots == 0 || !vaultRep.FTS5BM25Enabled {
		t.Fatalf("expected InspectVaultPulseSignal Score >= 85 and FTS5BM25Enabled=true, got %+v", vaultRep)
	}
}

func TestLiveUpstreamPulseSignalScoreAndWarnings_UserID10(t *testing.T) {
	baseURL := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_BRAIN_URL"))
	if baseURL == "" {
		t.Skip("ROLLING_MEMORY_BRAIN_URL not set; skipping live upstream pulse-signal test in local mode")
	}
	apiKey := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_BRAIN_KEY"))
	if apiKey == "" {
		accountURL, accountKey, accountErr := cloudcred.Ensure(context.Background())
		if accountErr != nil {
			t.Skipf("live data-plane credentials not configured (%v); skipping live pulse-signal test", accountErr)
		}
		if baseURL == "" {
			baseURL = accountURL
		}
		apiKey = accountKey
	}
	healthURL := strings.TrimRight(baseURL, "/")
	if !strings.HasSuffix(healthURL, "/open/memory/v1") {
		healthURL += "/open/memory/v1"
	}
	resp, err := http.Get(healthURL + "/health")
	if err != nil {
		t.Skipf("live upstream %s not reachable (%v)", healthURL, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Skipf("live upstream %s returned status %d", healthURL, resp.StatusCode)
	}

	v := openTestVault(t)
	tr := thinclient.NewHTTPBrainTransport(baseURL, apiKey)
	tr.UserID = thinclient.DefaultUpstreamDevUserID
	ctx := context.Background()

	// 1. Live healthy CJK + English sentence -> verify client-cloud exact parity (Score=100, Grade="S", 0 warnings)
	zhText := "Rolling-Memory 现已全面切换为薄客户端架构，不再维护本地核心算法，100% 走云端 /open/memory/v1 接口，绑定 user_id=10。"
	healthyPkt, _ := v.PackBlindPacket("U10-SIG-HEALTHY", zhText, 1730800100)
	localHealthy := thinclient.EvaluatePulseSignal(healthyPkt)
	cloudHealthy, err := tr.IngestBlindPulse(ctx, healthyPkt)
	if err != nil {
		t.Fatalf("live IngestBlindPulse(healthy): %v", err)
	}
	if cloudHealthy.PulseSignalScore != localHealthy.Score || cloudHealthy.PulseSignalGrade != localHealthy.Grade {
		t.Fatalf("client-cloud signal parity mismatch on healthy packet: cloud=(%d,%q) vs local=(%d,%q)",
			cloudHealthy.PulseSignalScore, cloudHealthy.PulseSignalGrade, localHealthy.Score, localHealthy.Grade)
	}
	if cloudHealthy.PulseSignalScore < 95 || len(cloudHealthy.PulseSignalWarnings) != 0 {
		t.Fatalf("expected live cloud PulseSignalScore >= 95 with 0 warnings on normal CJK sentence, got score=%d warnings=%+v",
			cloudHealthy.PulseSignalScore, cloudHealthy.PulseSignalWarnings)
	}

	// 2. Live pathological glued custom segmentation (with DisableAutoHarmonic: true) -> ingested without rejection (Zero Gate), returns exact warning parity
	gluedPkt, _ := v.PackBlindPacketWithSegmenter(
		"U10-SIG-GLUED",
		"Distributed consensus protocols require deterministic state machine replication across fault-tolerant storage nodes.",
		1730800200,
		thinclient.SegmenterPackOptions{
			Segmenter: func(s string) []string {
				return []string{strings.ReplaceAll(s, " ", "")}
			},
			DisableAutoHarmonic: true,
		},
	)
	localGlued := thinclient.EvaluatePulseSignal(gluedPkt)
	cloudGlued, err := tr.IngestBlindPulse(ctx, gluedPkt)
	if err != nil {
		t.Fatalf("live IngestBlindPulse(glued): %v", err)
	}
	if cloudGlued.AddressURI == "" || cloudGlued.ConservedMass <= 0 {
		t.Fatalf("zero-gate violation on live upstream: glued packet was not addressed/ingested: %+v", cloudGlued)
	}
	if cloudGlued.PulseSignalScore != localGlued.Score || cloudGlued.PulseSignalGrade != localGlued.Grade {
		t.Fatalf("client-cloud signal parity mismatch on glued packet: cloud=(%d,%q) vs local=(%d,%q)",
			cloudGlued.PulseSignalScore, cloudGlued.PulseSignalGrade, localGlued.Score, localGlued.Grade)
	}
	if len(cloudGlued.PulseSignalWarnings) == 0 || cloudGlued.PulseSignalWarnings[0].Code != thinclient.WarnCoarseUnsegmentedCarrier {
		t.Fatalf("expected live cloud warning %s, got %+v", thinclient.WarnCoarseUnsegmentedCarrier, cloudGlued.PulseSignalWarnings)
	}
	t.Logf("✓ Live Upstream Client-Cloud Pulse Signal Parity Verified: Healthy=%d(%s, 0 warnings) | Glued=%d(%s, warn=%s)",
		cloudHealthy.PulseSignalScore, cloudHealthy.PulseSignalGrade,
		cloudGlued.PulseSignalScore, cloudGlued.PulseSignalGrade, cloudGlued.PulseSignalWarnings[0].Code)
}
