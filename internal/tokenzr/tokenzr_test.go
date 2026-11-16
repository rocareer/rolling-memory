// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package tokenzr

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestTokenzRCountAndEncode(t *testing.T) {
	ctr, err := Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}
	if ctr.Encoding() != Encoding {
		t.Fatalf("Encoding=%q, want %q", ctr.Encoding(), Encoding)
	}
	ids, err := ctr.Encode("Hello!")
	if err != nil || len(ids) != 2 || ids[0] != 19923 || ids[1] != 3 {
		t.Fatalf("Encode(\"Hello!\") = %v, err=%v, want [19923 3]", ids, err)
	}
	if n := Count("hello world"); n != 2 {
		t.Fatalf("Count(\"hello world\") = %d, want 2", n)
	}
	if n := Count("记忆系统全链路分词器"); n <= 0 || n >= 15 {
		t.Fatalf("unexpected Chinese token count: %d", n)
	}
	for _, banned := range []string{"cl100k_base", "o200k_base", "p50k_base", "tiktoken"} {
		if _, err := NewCounter(banned); err == nil {
			t.Fatalf("NewCounter(%q) should be rejected", banned)
		}
	}
	for _, sample := range []string{
		"Where did Caroline move from 4 years ago?",
		"我去了成都地铁站，他涂了扶他林之后皮肤过敏",
		"昨日東京に行く、ラーメンを食べる",
		"epinephrine autoinjector",
	} {
		subs := SplitSubwords(sample)
		if strings.Join(subs, "") != sample {
			t.Fatalf("SplitSubwords(%q) lossless roundtrip failed: got %v", sample, subs)
		}
	}
	if TokenPriorBits("epinephrine") <= TokenPriorBits("the") || TokenPriorBits("扶他林") <= TokenPriorBits("的") {
		t.Fatalf("TokenPriorBits monotonic entropy failed: epinephrine=%.2f vs the=%.2f; 扶他林=%.2f vs 的=%.2f",
			TokenPriorBits("epinephrine"), TokenPriorBits("the"), TokenPriorBits("扶他林"), TokenPriorBits("的"))
	}
	// Pre-Space MDL invariant: mid-sentence auxiliary/function words ("did", "were", "have") must have lower prior bits than content nouns ("Sweden", "pottery", "May").
	if TokenPriorBits("did") >= TokenPriorBits("Sweden") || TokenPriorBits("were") >= TokenPriorBits("pottery") || TokenPriorBits("may") >= TokenPriorBits("May") {
		t.Fatalf("Pre-Space MDL prior failed: did=%.2f vs Sweden=%.2f, were=%.2f vs pottery=%.2f, may=%.2f vs May=%.2f",
			TokenPriorBits("did"), TokenPriorBits("Sweden"), TokenPriorBits("were"), TokenPriorBits("pottery"), TokenPriorBits("may"), TokenPriorBits("May"))
	}
	// 2nd-order BPE inflectional valence & EffectiveLexicalBits invariant:
	// closed-class function words ("what", "where", "when", "since", "here", "because", "from") have valence == 0,
	// whereas open-class content words ("go", "like", "make", "work", "move", "use") have valence >= 2 and EffectiveLexicalBits > 12.0b.
	for _, fw := range []string{"what", "where", "when", "why", "how", "who", "which", "whether", "although", "because", "from", "since", "here", "there"} {
		if v := BPEInflectionValence(fw); v != 0 {
			t.Fatalf("expected BPEInflectionValence(%q) == 0 for closed-class function word, got %d", fw, v)
		}
	}
	for _, cw := range []string{"go", "like", "make", "work", "move", "use", "want", "know", "take", "play", "book", "place", "state"} {
		if v := BPEInflectionValence(cw); v < 2 {
			t.Fatalf("expected BPEInflectionValence(%q) >= 2 for open-class content word, got %d", cw, v)
		}
		if eff := EffectiveLexicalBits(cw); eff < 12.0 {
			t.Fatalf("expected EffectiveLexicalBits(%q) >= 12.0 bits, got %.2f", cw, eff)
		}
	}
	for _, prop := range []string{"caroline", "sweden", "paris", "alice", "melanie", "john", "july", "august", "monday"} {
		if !IsBPEProperNounPrior(prop) {
			t.Fatalf("expected IsBPEProperNounPrior(%q) == true", prop)
		}
	}
	for _, fw := range []string{"what", "where", "when", "why", "how", "who", "whom", "whose", "which", "whether", "although", "because", "unless", "from", "with", "did", "was", "were", "have"} {
		if IsBPEProperNounPrior(fw) {
			t.Fatalf("expected IsBPEProperNounPrior(%q) == false for function word", fw)
		}
	}
	large64k := strings.Repeat("def itrs_to_observed_mat(time, loc): return matrix\n", 1400)
	if n1, n2 := Count(large64k), Count(large64k); n1 <= 0 || n1 != n2 {
		t.Fatalf("expected consistent cached Count on >64KB input: n1=%d n2=%d", n1, n2)
	}
	if !HasCompetingBPESubroot([]rune("reach"), []rune("reacted"), 4) {
		t.Fatalf("expected HasCompetingBPESubroot(reach, reacted) == true")
	}
	if !HasCompetingBPESubroot([]rune("child"), []rune("chilled"), 4) {
		t.Fatalf("expected HasCompetingBPESubroot(child, chilled) == true")
	}
	if !HasCompetingBPESubroot([]rune("cure"), []rune("curated"), 3) {
		t.Fatalf("expected HasCompetingBPESubroot(cure, curated) == true")
	}
	if !HasCompetingBPESubroot([]rune("place"), []rune("placated"), 4) {
		t.Fatalf("expected HasCompetingBPESubroot(place, placated) == true")
	}
	if HasCompetingBPESubroot([]rune("study"), []rune("studied"), 4) {
		t.Fatalf("expected HasCompetingBPESubroot(study, studied) == false")
	}
	if HasCompetingBPESubroot([]rune("play"), []rune("played"), 4) {
		t.Fatalf("expected HasCompetingBPESubroot(play, played) == false")
	}
	if IsTerminalInflectedExtension([]rune("cure"), []rune("curated")) {
		t.Fatalf("expected IsTerminalInflectedExtension(cure, curated) == false")
	}
	if !IsTerminalInflectedExtension([]rune("move"), []rune("moved")) {
		t.Fatalf("expected IsTerminalInflectedExtension(move, moved) == true")
	}
	if IsTerminalInflectedExtension([]rune("part"), []rune("party")) {
		t.Fatalf("expected IsTerminalInflectedExtension(part, party) == false")
	}
	if !IsAlreadyInflectedVerbToken("rusted") || IsAlreadyInflectedVerbToken("rust") {
		t.Fatalf("expected IsAlreadyInflectedVerbToken(rusted)==true and IsAlreadyInflectedVerbToken(rust)==false")
	}
	for _, nonVerb := range []string{"across", "actress", "academical", "accidental", "accidently", "acidly", "absorber", "acceptor", "accuser", "achiever", "actional", "actioner", "abuser", "abandoner", "accorder"} {
		if IsAlreadyInflectedVerbToken(nonVerb) {
			t.Fatalf("expected IsAlreadyInflectedVerbToken(%q) == false for non-inflected/derivational token", nonVerb)
		}
	}
	for _, trueVerb := range []string{"moved", "played", "studied", "studies", "watches", "lives", "burned", "fixed"} {
		if !IsAlreadyInflectedVerbToken(trueVerb) {
			t.Fatalf("expected IsAlreadyInflectedVerbToken(%q) == true", trueVerb)
		}
	}
	if !IsTerminalInflectedExtension([]rune("stop"), []rune("stopped")) || !IsTerminalInflectedExtension([]rune("run"), []rune("running")) {
		t.Fatalf("expected IsTerminalInflectedExtension to recognize CVC geminate inflections (stop->stopped, run->running)")
	}
	for _, openStem := range []string{"study", "hike", "mentor", "bake"} {
		if v := BPEInflectionValence(openStem); v < 2 {
			t.Fatalf("expected BPEInflectionValence(%q) >= 2 via full-vocab paradigm & nucleus alternation, got %d", openStem, v)
		}
	}
	if subs := BPEContainedSubblocks("study"); len(subs) == 0 || subs[0] != "stud" {
		t.Fatalf("expected BPEContainedSubblocks(\"study\") to include shared BPE leftChild stem \"stud\", got %v", subs)
	}
	if subs := BPEContainedSubblocks("笔记本电脑"); len(subs) < 2 || subs[0] != "笔记本" || subs[1] != "电脑" {
		t.Fatalf("expected orthogonal CJK subblocks [笔记本 电脑] for \"笔记本电脑\", got %v", subs)
	}
	if subs := BPEContainedSubblocks("東京都"); len(subs) < 2 || subs[0] != "東京" || subs[1] != "京都" {
		t.Fatalf("expected occluded + merge-tree subblocks [東京 京都] for \"東京都\", got %v", subs)
	}
	if subs := BPEContainedSubblocks("한국어"); len(subs) == 0 || subs[0] != "한국" {
		t.Fatalf("expected Hangul compound subblock [한국] for \"한국어\", got %v", subs)
	}
	for _, cjkContent := range []string{"成都", "地铁", "过敏", "小提琴", "心理学"} {
		for _, cjkGlue := range []string{"的", "了", "什么", "怎么"} {
			if BPETreeCohesionBits(cjkContent) <= BPETreeCohesionBits(cjkGlue) {
				t.Fatalf("expected BPETreeCohesionBits(%q)=%.2f > BPETreeCohesionBits(%q)=%.2f",
					cjkContent, BPETreeCohesionBits(cjkContent), cjkGlue, BPETreeCohesionBits(cjkGlue))
			}
		}
	}
	if btRaw, err := os.ReadFile("bpe_topology.go"); err == nil && (strings.Contains(string(btRaw), "bpePrefixByCarryBits") || strings.Contains(string(btRaw), `case "is"`)) {
		t.Fatalf("bpe_topology.go must not contain bpePrefixByCarryBits or hardcoded 'case \"is\"'")
	}
}

func TestTokenzRZeroLeakAndZeroHostDepsGate(t *testing.T) {
	forbiddenPatterns := []*regexp.Regexp{
		regexp.MustCompile(`/inter` + `nal/(?:service|repository|model|handler|infra|engine|kit|dto|router|middleware|plugin|mcp|gateway)`),
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		relPath := filepath.Join(".", entry.Name())
		data, err := os.ReadFile(relPath)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", relPath, err)
		}
		content := string(data)
		for _, re := range forbiddenPatterns {
			if m := re.FindString(content); m != "" {
				t.Fatalf("leak gate violation in %s: matched %q", relPath, m)
			}
		}
	}
}

func TestSplitBPEDirectPiecesPureSubstringInvariant(t *testing.T) {
	probes := []string{
		"hotels", "images", "videos", "visited", "travelled", "submitted",
		"screenshot", "timelapse", "rainfall", "somewhere", "newcastle", "malamutes",
		"removed", "migrating", "retired", "retirement", "measured", "measurement",
		"visted", "visting", "fried", "during", "recent", "visit", "bridge", "decode",
		"porto", "getting", "wedding", "hidding",
		"笔记本电脑", "東京都", "한국어",
	}
	for _, w := range probes {
		pieces := SplitBPEDirectPieces(w)
		for _, p := range pieces {
			if p == "" || p == w || !strings.Contains(w, p) {
				t.Fatalf("SplitBPEDirectPieces(%q) emitted non-substring or self piece %q in %v (pure tokenizer must only segment, never fabricate characters)", w, p, pieces)
			}
		}
	}
	if got := SplitBPEDirectPieces("porto"); len(got) != 0 {
		t.Fatalf("SplitBPEDirectPieces(\"porto\") must not split 1-letter coda into %v", got)
	}
	for _, p := range SplitBPEDirectPieces("hilarious") {
		if p == "hilariou" {
			t.Fatalf("SplitBPEDirectPieces(\"hilarious\") must not strip -s from -ous into %q", p)
		}
	}
	for _, hiatusWord := range [][2]string{
		{"queued", "queue"},
		{"glued", "glue"},
		{"issued", "issue"},
		{"argued", "argue"},
	} {
		pieces := SplitBPEDirectPieces(hiatusWord[0])
		if strings.HasSuffix(hiatusWord[0], "ued") && hiatusWord[0] == "queued" {
			for _, p := range pieces {
				if p == hiatusWord[1] {
					t.Fatalf("SplitBPEDirectPieces(%q) must not trigger vowel-hiatus elidedCand %q on vowel-ending stem", hiatusWord[0], hiatusWord[1])
				}
			}
		}
	}
	sharedPairs := [][2]string{
		{"removed", "remove"},
		{"measured", "measure"},
		{"measurement", "measure"},
		{"retired", "retire"},
		{"retirement", "retire"},
		{"malamutes", "malamute"},
		{"located", "locate"},
		{"cared", "care"},
		{"studied", "studi"},
		{"studies", "stud"},
		{"study", "stud"},
		{"happily", "happi"},
		{"happily", "happ"},
		{"company", "compan"},
		{"companies", "compan"},
		{"family", "famil"},
		{"families", "famil"},
		{"familiar", "famil"},
		{"early", "earl"},
		{"earlier", "earl"},
		{"submit", "subm"},
		{"submission", "subm"},
		{"celebrates", "celebrat"},
		{"promotes", "promot"},
		{"dishes", "dish"},
		{"bunches", "bunch"},
		{"volcanoes", "volcano"},
		{"volcano", "volcan"},
		{"potatoes", "potato"},
		{"effective", "effect"},
		{"pasties", "past"},
		{"pasty", "past"},
		{"vegs", "veg"},
		{"rainy", "rain"},
		{"salty", "salt"},
		{"photo", "phot"},
		{"collection", "collect"},
		{"location", "locat"},
		{"celebration", "celebrat"},
		{"exploration", "explor"},
		{"define", "defin"},
		{"remove", "remov"},
		{"observe", "observ"},
		{"welcome", "welcom"},
		{"locate", "locat"},
		{"celebrate", "celebrat"},
		{"migrating", "migrat"},
		{"heavy", "heav"},
		{"include", "includ"},
		{"tried", "tri"},
		{"pinky", "pink"},
		{"sushimi", "sushi"},
		{"occasional", "occasion"},
		{"travelled", "travel"},
	}
	for _, sp := range sharedPairs {
		pieces := SplitBPEDirectPieces(sp[0])
		found := false
		for _, x := range pieces {
			if x == sp[1] {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected SplitBPEDirectPieces(%q)=%v to include pure prefix root %q", sp[0], pieces, sp[1])
		}
	}
	for _, noLowDegCoda := range []string{"shelf", "tablet", "paint", "problem", "thank", "crowd", "court", "crisp", "burger", "letter", "travel", "display", "outside"} {
		if got := SplitBPEDirectPieces(noLowDegCoda); len(got) != 0 {
			t.Fatalf("SplitBPEDirectPieces(%q) must not split low-rightInDeg or base word into %v", noLowDegCoda, got)
		}
	}
	if tzRaw, err := os.ReadFile("tokenzr.go"); err == nil {
		if strings.Contains(string(tzRaw), "tryRestoreNucleus") || strings.Contains(string(tzRaw), "hasOpenParadigm") {
			t.Fatal("tokenzr.go must not contain language-bound vowel restoration heuristics (tryRestoreNucleus / hasOpenParadigm)")
		}
	}
}

func BenchmarkBPEInflectionValenceAndSubroot(b *testing.B) {
	_, _ = Default()
	rReach, rReacted := []rune("reach"), []rune("reacted")
	rMove, rMoved := []rune("move"), []rune("moved")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = BPEInflectionValence("move")
		_ = BPEInflectionValence("because")
		_ = HasCompetingBPESubroot(rReach, rReacted, 4)
		_ = IsTerminalInflectedExtension(rMove, rMoved)
	}
}
