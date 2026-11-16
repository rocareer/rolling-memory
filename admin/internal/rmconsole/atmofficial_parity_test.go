package rollingmemory

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestATMOfficialTiersOnlyAdminFailClosed 钉住 admin 侧官方题集口径不变量：
// ATM-Bench 官方只有 hard / 1013 两档；自造的 100 题前缀档必须 fail-closed 拒跑，
// 严禁静默降级为 hard（否则旧 URL/旧台账会以 hard 读数冒充 100 档读数）。
func TestATMOfficialTiersOnlyAdminFailClosed(t *testing.T) {
	if got := normalizeATMTier("hard"); got != "hard" {
		t.Fatalf("normalizeATMTier(hard) = %q, want hard", got)
	}
	if got := normalizeATMTier("1013"); got != "1013" {
		t.Fatalf("normalizeATMTier(1013) = %q, want 1013", got)
	}
	for _, retired := range []string{"100", "main-100", "main100", "atm-100", "atm100"} {
		if got := normalizeATMTier(retired); got != "" {
			t.Fatalf("normalizeATMTier(%q) = %q, want empty (fail-closed)", retired, got)
		}
	}
	if _, err := loadATMTierQuestions("", t.TempDir(), "100"); !errors.Is(err, ErrATMTierRetiredAdmin) {
		t.Fatalf("loadATMTierQuestions(tier=100) err = %v, want ErrATMTierRetiredAdmin", err)
	}
	if _, err := validateEvalRunRequest(evalRunRequest{Suite: SuiteATM, Tier: "100", ArmMode: EvalArmSDKPulseCipher}); !errors.Is(err, ErrATMTierRetiredAdmin) {
		t.Fatalf("validateEvalRunRequest(suite=atm, tier=100) err = %v, want ErrATMTierRetiredAdmin", err)
	}
}

type atmOfficialNumberCaseAdmin struct {
	ID         string `json:"id"`
	Gold       string `json:"gold"`
	Prediction string `json:"prediction"`
	Question   string `json:"question"`
	Expected   bool   `json:"expected"`
}

type atmOfficialListCaseAdmin struct {
	ID         string  `json:"id"`
	Gold       string  `json:"gold"`
	Prediction string  `json:"prediction"`
	Expected   float64 `json:"expected"`
}

type atmOfficialJudgeCaseAdmin struct {
	ID               string `json:"id"`
	Raw              string `json:"raw"`
	ExpectedAccuracy bool   `json:"expected_accuracy"`
	ExpectedFallback bool   `json:"expected_parse_fallback"`
}

type atmOfficialFixtureAdmin struct {
	Version     string                       `json:"version"`
	NumberCases []atmOfficialNumberCaseAdmin `json:"number_cases"`
	ListCases   []atmOfficialListCaseAdmin   `json:"list_cases"`
	JudgeCases  []atmOfficialJudgeCaseAdmin  `json:"judge_cases"`
}

func TestATMOfficialScoringParityAdmin(t *testing.T) {
	var fixture atmOfficialFixtureAdmin
	if err := json.Unmarshal(atmOfficialScoringFixture(), &fixture); err != nil {
		t.Fatalf("decode admin ATM official fixture: %v", err)
	}
	if fixture.Version != ATMOfficialScoringProtocol {
		t.Fatalf("fixture version = %q, want %q", fixture.Version, ATMOfficialScoringProtocol)
	}
	numberMismatches, listMismatches, judgeMismatches := 0, 0, 0
	for _, c := range fixture.NumberCases {
		got, _ := ATMOfficialNumberAccuracy(c.Gold, c.Prediction, c.Question)
		if got != c.Expected {
			numberMismatches++
			if numberMismatches <= 8 {
				t.Errorf("number[%s] got %v want %v", c.ID, got, c.Expected)
			}
		}
	}
	for _, c := range fixture.ListCases {
		got := ATMOfficialListJaccard(c.Gold, c.Prediction)
		if diff := got - c.Expected; diff > 1e-9 || diff < -1e-9 {
			listMismatches++
			if listMismatches <= 8 {
				t.Errorf("list[%s] got %.6f want %.6f", c.ID, got, c.Expected)
			}
		}
	}
	for _, c := range fixture.JudgeCases {
		acc, _, fallback := ATMOfficialParseJudgeResponse(c.Raw)
		if acc != c.ExpectedAccuracy || fallback != c.ExpectedFallback {
			judgeMismatches++
			if judgeMismatches <= 8 {
				t.Errorf("judge[%s] got accuracy=%v fallback=%v want %v/%v", c.ID, acc, fallback, c.ExpectedAccuracy, c.ExpectedFallback)
			}
		}
	}
	if numberMismatches+listMismatches+judgeMismatches > 0 {
		t.Fatalf("admin ATM official scoring parity failed: number=%d list=%d judge=%d", numberMismatches, listMismatches, judgeMismatches)
	}
	t.Logf("admin ATM official scoring parity: number=%d list=%d judge=%d all match", len(fixture.NumberCases), len(fixture.ListCases), len(fixture.JudgeCases))
}

func TestATMOfficialProtocolPinsAdmin(t *testing.T) {
	if err := verifyATMOfficialProtocolAdmin(); err != nil {
		t.Fatalf("admin official protocol pin gate: %v", err)
	}
	home, _ := os.UserHomeDir()
	atmDir := resolveATMDataDir(home, "")
	if atmDir == "" {
		t.Skip("ATM evaldata directory not found in this environment")
	}
	if _, err := os.Stat(filepath.Join(atmDir, "corpus.jsonl.gz")); err != nil {
		t.Skipf("ATM evaldata corpus not found in this environment (%s): %v", atmDir, err)
	}
	if err := verifyATMCorpusPinsAdmin(atmDir); err != nil {
		t.Fatalf("admin official corpus pin gate: %v", err)
	}
	status := atmOfficialPinSnapshotAdmin(atmDir)
	if !status.PromptPinsVerified || !status.FixtureVerified || !status.CorpusPinsVerified {
		t.Fatalf("admin pin snapshot not fully verified: %+v", status)
	}
	if len(atmOfficialPinIndexAdmin()) == 0 {
		t.Fatal("empty admin pin index")
	}
}

func TestATMOfficialScorerSyncParity(t *testing.T) {
	// The admin copies are generated from internal/eval/atmofficial_*.go.
	// This test protects the fixture sha and the embedded copy at runtime.
	if !atmOfficialPinSnapshotAdmin("").FixtureVerified {
		t.Fatalf("admin embedded fixture sha mismatch: pin=%s", PinATMOfficialScoringFixtureSHA256)
	}
}

func TestATMOfficialProtocolSpecAdmin(t *testing.T) {
	spec := buildATMProtocolParitySpec()
	if !spec.OfficialScoringAligned {
		t.Fatalf("official scoring adapter is not aligned: %+v", spec)
	}
	if !spec.AllPinsVerified {
		t.Fatalf("prompt/fixture pin gate did not pass: %+v", spec)
	}
	if spec.ScoringFixtureSHA256 != PinATMOfficialScoringFixtureSHA256 {
		t.Fatalf("spec fixture sha = %q, want %q", spec.ScoringFixtureSHA256, PinATMOfficialScoringFixtureSHA256)
	}
	if spec.NumberScorer == "" || spec.ListScorer == "" || spec.JudgeParser == "" {
		t.Fatalf("scoring semantics not described in spec: %+v", spec)
	}
	if len(spec.EvaluatorSourcePins) != 4 || len(spec.CorpusPins) != 3 {
		t.Fatalf("pin maps incomplete: evaluator=%d corpus=%d", len(spec.EvaluatorSourcePins), len(spec.CorpusPins))
	}
	if spec.RecallMetricSpec == "" || spec.RecallMetricSpecEn == "" {
		t.Fatal("recall metric spec missing")
	}
	if spec.AnswerPayloadSHA1 != PinATMEvidencePromptSHA1 {
		t.Fatalf("answer payload sha = %q, want %q", spec.AnswerPayloadSHA1, PinATMEvidencePromptSHA1)
	}
}

type atmAnswerPayloadCaseAdmin struct {
	ID             string   `json:"id"`
	Question       string   `json:"question"`
	EvidenceChunks []string `json:"evidence_chunks"`
	System         string   `json:"system"`
	User           string   `json:"user"`
}

type atmAnswerPayloadFixtureAdmin struct {
	Version      string                      `json:"version"`
	SystemPrompt string                      `json:"system_prompt"`
	UserTemplate string                      `json:"user_template"`
	Cases        []atmAnswerPayloadCaseAdmin `json:"cases"`
}

func TestATMOfficialAnswerPayloadParityAdmin(t *testing.T) {
	var fixture atmAnswerPayloadFixtureAdmin
	if err := json.Unmarshal(atmOfficialAnswerPayloadFixture(), &fixture); err != nil {
		t.Fatalf("decode admin answer payload fixture: %v", err)
	}
	if ATMSystemPrompt != fixture.SystemPrompt {
		t.Fatal("admin answer system prompt byte drift")
	}
	if ATMEvidencePromptTPL != fixture.UserTemplate {
		t.Fatal("admin answer user template byte drift")
	}
	for _, c := range fixture.Cases {
		chunks := make([]string, 0, len(c.EvidenceChunks))
		for i, chunk := range c.EvidenceChunks {
			chunks = append(chunks, fmt.Sprintf("Evidence %d:\n%s", i+1, chunk))
		}
		fullContext := strings.Join(chunks, "\n")
		user := strings.NewReplacer("{question}", c.Question, "{evidence}", fullContext).Replace(ATMEvidencePromptTPL)
		if user != c.User {
			t.Fatalf("case %s admin answer payload byte drift:\n got %q\nwant %q", c.ID, user, c.User)
		}
	}
	t.Logf("admin ATM official answer payload parity: %d cases byte-identical", len(fixture.Cases))
}

func TestATMOfficialAnswerPayloadFixtureIsPinnedAdmin(t *testing.T) {
	if !atmOfficialPinSnapshotAdmin("").AnswerPayloadVerified {
		t.Fatalf("admin answer payload fixture sha mismatch: pin=%s", PinATMOfficialAnswerPayloadFixtureSHA256)
	}
}
