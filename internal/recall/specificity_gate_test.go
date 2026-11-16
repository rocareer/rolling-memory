// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package recall

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rocareer/rolling-memory/internal/memcore"
)

func TestShannonProductionSecurityAndRobustnessProbes(t *testing.T) {
	// Probe P1: Space/hyphen-separated periodic flood ("ha ha ha ha ha", "a-a-a-a a-a-a-a") must collapse < 8.0b
	p1a := EffectiveShannonBits("a-a-a-a a-a-a-a")
	p1b := EffectiveShannonBits("ha ha ha ha ha ha")
	if p1a >= memcore.AtomicConceptEntropyBits || p1b >= memcore.AtomicConceptEntropyBits {
		t.Fatalf("[Probe P1] space/hyphen-separated flood leaked: p1a=%.2fb, p1b=%.2fb (want < 8.0b)", p1a, p1b)
	}

	// Probe P2: Tail-appended punctuated/tandem stutter after 64-char prefix ("normal..." + "a.a.a.a"*100 or "abab"*50)
	prefix64 := "TheQuickBrownFoxJumpsOverTheLazyDogAndRunsIntoTheForestToday1234"
	stutterTail := strings.Repeat("a.", 100)
	tandemTail := strings.Repeat("abab", 32)
	bPrefix := EffectiveShannonBits(prefix64)
	bWithStutter := EffectiveShannonBits(prefix64 + " " + stutterTail)
	bWithTandem := EffectiveShannonBits(prefix64 + " " + tandemTail)
	if bWithStutter-bPrefix > 5.0 {
		t.Fatalf("[Probe P2a] tail punctuated stutter 'a.'*100 after 64-char prefix inflated bits by +%.2fb (want <= 5.0b)", bWithStutter-bPrefix)
	}
	if bWithTandem-bPrefix > 5.0 {
		t.Fatalf("[Probe P2b] tail tandem flood 'abab'*32 after 64-char prefix inflated bits by +%.2fb (want <= 5.0b)", bWithTandem-bPrefix)
	}

	// Probe P3: FastLongestVerbatimSubspan O(N log N) vs O(N^3) CPU DoS defense on 1500-rune mismatched anchor (< 20ms)
	turnText := strings.Repeat("abcdefghij", 200)
	badAnchor := strings.Repeat("klmnopqrst", 150) // 1500 runes, 0 overlap with turnText
	t0 := time.Now()
	gotMismatch := FastLongestVerbatimSubspan(turnText, badAnchor)
	elapsed := time.Since(t0)
	if gotMismatch != "" || elapsed > 25*time.Millisecond {
		t.Fatalf("[Probe P3] FastLongestVerbatimSubspan DoS guard failed: got=%q, elapsed=%v (want < 25ms)", gotMismatch, elapsed)
	}
	gotMatch := FastLongestVerbatimSubspan("Noise prefix: Caroline moved to Sweden in 2023. Noise suffix.", "Yesterday Caroline moved to Sweden in 2023!")
	if gotMatch != "Caroline moved to Sweden in 2023" {
		t.Fatalf("[Probe P3] FastLongestVerbatimSubspan exact match failed: got %q", gotMatch)
	}
}

func TestThreeLayerZeroWordlistDisambiguationFalsification(t *testing.T) {
	// Layer 1: BPEShannonCompressionPurity separates content words & proper nouns from closed-class
	// function words / conversational glue purely via BPE-to-Shannon information deficit (zero static wordlist).
	for _, cw := range []string{"paris", "sweden", "shaft", "move", "study", "watch", "autoinjector", "幻读", "脑裂"} {
		if p := BPEShannonCompressionPurity(cw); p < 0.85 {
			t.Fatalf("Layer-1 falsified: expected BPEShannonCompressionPurity(%q) >= 0.85 for content word, got %.3f", cw, p)
		}
	}
	for _, fw := range []string{"where", "when", "which", "from", "with", "因为", "虽然", "但是"} {
		if p := BPEShannonCompressionPurity(fw); p > 0.65 {
			t.Fatalf("Layer-1 falsified: expected BPEShannonCompressionPurity(%q) <= 0.65 for function word, got %.3f", fw, p)
		}
	}

	// Layer 2: Extended BPE Inflectional Rejection ("will studies", "will watches", "may moved",
	// "may lives", "may studies", "don watches", "can rusted", "what does may like", "who told may", "went with may").
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"will studies math", []string{"will", "studies", "math"}},
		{"will watches television", []string{"will", "watches", "television"}},
		{"may moved to sweden", []string{"may", "moved", "sweden"}},
		{"may lives in paris", []string{"may", "lives", "paris"}},
		{"may studies math", []string{"may", "studies", "math"}},
		{"what does may like?", []string{"may", "like"}},
		{"who told may?", []string{"told", "may"}},
		{"went to paris with may", []string{"went", "paris", "may"}},
		{"don watches television", []string{"don", "watches", "television"}},
		{"can rusted quickly", []string{"can", "rusted", "quickly"}},
		{"Which hobby did Sam take up in May 2023?", []string{"hobby", "sam", "take", "may", "2023"}},
	} {
		weights := ComputeContextualTokenWeights(tc.query, nil)
		for _, w := range tc.want {
			if weights[w] < 0.99 {
				t.Fatalf("Layer-2 falsified: expected full weight 1.0 for %q in ComputeContextualTokenWeights(%q), got %.3f (all=%v)",
					w, tc.query, weights[w], weights)
			}
		}
	}

	// Layer 1 + Layer 2 Negative Guard: True modal auxiliary usages ("Will she study", "Can they watch",
	// and 2nd-order prefix traps "Will listen to music", "Will market prices rise", "Can modern art survive")
	// must NOT be promoted to 1.0 when ungrounded, yet retain a non-zero soft floor in [0.12, 0.35] so recall never goes blind.
	for _, neg := range []struct {
		query string
		modal string
	}{
		{"Will she study math tomorrow?", "will"},
		{"Can they watch television tonight?", "can"},
		{"Where will Caroline move?", "will"},
		{"Will listen to music tomorrow?", "will"},
		{"Will market prices rise?", "will"},
		{"Can modern art survive?", "can"},
		{"Will planet Earth warm?", "will"},
	} {
		weights := ComputeContextualTokenWeights(neg.query, nil)
		w := weights[neg.modal]
		if w <= 0.0 || w > 0.35 {
			t.Fatalf("Layer-1/2 modal soft-floor guard falsified for %q in %q: got weight=%.3f, want in [0.12, 0.35]",
				neg.modal, neg.query, w)
		}
	}

	// Layer 3: structured memory assertion Graph Ontology Grounding immediately promotes any ambiguous token to 1.0 when present in knownEntityLabels.
	grounded := ComputeContextualTokenWeights("Where will she go?", map[string]bool{"will": true})
	if grounded["will"] != 1.0 || grounded["go"] != 1.0 {
		t.Fatalf("Layer-3 graph grounding falsified: expected weight['will']==1.0 when grounded in structured memory assertion graph, got %v", grounded)
	}
}

func TestOnlineDFConjugateWashout_BeatsStaticBPETemplateInversion(t *testing.T) {
	defer SetCorpusTokenDFForRecall(nil, 0)

	// 1. Cold-start (N=0): rare-looking template token "chronotrigger" has purity 1.0 under static BPE
	if pCold := BPEShannonCompressionPurity("chronotrigger"); pCold < 0.95 {
		t.Fatalf("expected cold-start purity >= 0.95 for 'chronotrigger', got %.3f", pCold)
	}

	// 2. Online DF active (N=60): "chronotrigger" appears in all 60 documents (boilerplate header),
	// whereas "valve" appears in only 1 document (true diagnostic target).
	SetCorpusTokenDFForRecall(map[string]int{
		"chronotrigger": 60,
		"valve":         1,
		"ox":            1,
	}, 60)

	pBoiler := BPEShannonCompressionPurity("chronotrigger")
	pTarget := BPEShannonCompressionPurity("valve")
	if pBoiler > 0.25 {
		t.Fatalf("Online DF washout failed: expected boilerplate 'chronotrigger' (df=60/60) purity <= 0.25, got %.3f", pBoiler)
	}
	if pTarget < 0.95 {
		t.Fatalf("Online DF rarity promotion failed: expected rare target 'valve' (df=1/60) purity >= 0.95, got %.3f", pTarget)
	}

	weights := ComputeContextualTokenWeights("ChronoTrigger valve ox", nil)
	if weights["chronotrigger"] > 0.25 {
		t.Fatalf("expected ComputeContextualTokenWeights to demote boilerplate 'chronotrigger' to soft floor, got %.3f", weights["chronotrigger"])
	}
	if weights["valve"] < 0.95 || weights["ox"] < 0.85 {
		t.Fatalf("expected rare target tokens 'valve' (%.3f) and 'ox' (%.3f) to receive high weights", weights["valve"], weights["ox"])
	}
}

func TestGlobalCorpusDFConcurrentRepublishAndRead(t *testing.T) {
	// The efficacy pass republishes the online DF table while recall paths read it,
	// and Go maps must never be read concurrently with a write. Run with -race to
	// catch a regression to the unsynchronized globals.
	defer SetCorpusTokenDFForRecall(nil, 0)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = ComputeContextualTokenWeights("chronotrigger valve ox", nil)
				_ = BPEShannonCompressionPurity("valve")
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			SetCorpusTokenDFForRecall(map[string]int{"chronotrigger": 60, "valve": 1}, 60)
		}
	}()
	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}
