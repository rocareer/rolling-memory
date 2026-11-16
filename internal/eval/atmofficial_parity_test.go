package eval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type atmOfficialNumberCase struct {
	ID         string `json:"id"`
	Gold       string `json:"gold"`
	Prediction string `json:"prediction"`
	Question   string `json:"question"`
	Expected   bool   `json:"expected"`
}

type atmOfficialListCase struct {
	ID         string  `json:"id"`
	Gold       string  `json:"gold"`
	Prediction string  `json:"prediction"`
	Expected   float64 `json:"expected"`
}

type atmOfficialJudgeCase struct {
	ID               string `json:"id"`
	Raw              string `json:"raw"`
	ExpectedAccuracy bool   `json:"expected_accuracy"`
	ExpectedFallback bool   `json:"expected_parse_fallback"`
}

type atmOfficialFixture struct {
	Version     string                  `json:"version"`
	NumberCases []atmOfficialNumberCase `json:"number_cases"`
	ListCases   []atmOfficialListCase   `json:"list_cases"`
	JudgeCases  []atmOfficialJudgeCase  `json:"judge_cases"`
}

// TestATMOfficialJudgePromptFileMatchesConstant locks the exported official
// judge prompt (shared with the official re-judge tooling) to the pinned constant.
func TestATMOfficialJudgePromptFileMatchesConstant(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "atm_official_judge_prompt.txt"))
	if err != nil {
		t.Fatalf("read official judge prompt: %v", err)
	}
	if string(raw) != ATMJudgePromptTPL {
		t.Fatalf("official judge prompt file drift: file %dB, constant %dB", len(raw), len(ATMJudgePromptTPL))
	}
	if got := Sha1Hex(string(raw)); got != PinATMJudgeTPLSHA1 {
		t.Fatalf("official judge prompt sha1 = %s, want %s", got, PinATMJudgeTPLSHA1)
	}
}

func TestATMOfficialScoringParity(t *testing.T) {
	var fixture atmOfficialFixture
	if err := json.Unmarshal(ATMOfficialScoringFixture(), &fixture); err != nil {
		t.Fatalf("decode embedded ATM official fixture: %v", err)
	}
	if fixture.Version != ATMOfficialScoringProtocol {
		t.Fatalf("fixture version = %q, want %q", fixture.Version, ATMOfficialScoringProtocol)
	}

	numberMismatches := 0
	for _, c := range fixture.NumberCases {
		got, _ := ATMOfficialNumberAccuracy(c.Gold, c.Prediction, c.Question)
		if got != c.Expected {
			numberMismatches++
			if numberMismatches <= 12 {
				t.Errorf("number[%s] gold=%q pred=%q question=%q: got %v want %v", c.ID, c.Gold, c.Prediction, c.Question, got, c.Expected)
			}
		}
	}
	listMismatches := 0
	for _, c := range fixture.ListCases {
		got := ATMOfficialListJaccard(c.Gold, c.Prediction)
		if diff := got - c.Expected; diff > 1e-9 || diff < -1e-9 {
			listMismatches++
			if listMismatches <= 12 {
				t.Errorf("list[%s] gold=%q pred=%q: got %.6f want %.6f", c.ID, c.Gold, c.Prediction, got, c.Expected)
			}
		}
	}
	judgeMismatches := 0
	for _, c := range fixture.JudgeCases {
		acc, _, fallback := ATMOfficialParseJudgeResponse(c.Raw)
		if acc != c.ExpectedAccuracy || fallback != c.ExpectedFallback {
			judgeMismatches++
			if judgeMismatches <= 12 {
				t.Errorf("judge[%s] raw=%q: got accuracy=%v fallback=%v want accuracy=%v fallback=%v",
					c.ID, c.Raw, acc, fallback, c.ExpectedAccuracy, c.ExpectedFallback)
			}
		}
	}
	if numberMismatches+listMismatches+judgeMismatches > 0 {
		t.Fatalf("ATM official scoring parity failed: number=%d list=%d judge=%d (fixture cases: %d/%d/%d)",
			numberMismatches, listMismatches, judgeMismatches,
			len(fixture.NumberCases), len(fixture.ListCases), len(fixture.JudgeCases))
	}
	t.Logf("ATM official scoring parity: number=%d list=%d judge=%d all match",
		len(fixture.NumberCases), len(fixture.ListCases), len(fixture.JudgeCases))
}

func TestATMOfficialProtocolPins(t *testing.T) {
	if err := VerifyATMOfficialProtocol(); err != nil {
		t.Fatalf("official protocol pin gate: %v", err)
	}
	// The corpus is bring-your-own and is not redistributed in the public tree;
	// mirror the bundled-dataset convention and skip when it is absent.
	if _, err := ResolveATMDataDir(""); err != nil {
		t.Skipf("bundled ATM dataset not found: %v", err)
	}
	if err := VerifyATMCorpusPins(""); err != nil {
		t.Fatalf("official corpus pin gate: %v", err)
	}
	status := ATMOfficialPinSnapshot("")
	if !status.PromptPinsVerified || !status.FixtureVerified || !status.CorpusPinsVerified {
		t.Fatalf("official pin snapshot not fully verified: %+v", status)
	}
	for _, line := range ATMOfficialPinIndex() {
		if line == "" {
			t.Fatal("empty pin index line")
		}
	}
}

// TestATMOfficialScoringFixtureIsPinned makes accidental fixture edits fail
// before the parity comparison can hide them behind regenerated expectations.
func TestATMOfficialScoringFixtureIsPinned(t *testing.T) {
	fixture := ATMOfficialScoringFixture()
	if len(fixture) == 0 {
		t.Fatal("embedded fixture is empty")
	}
	status := ATMOfficialPinSnapshot("")
	if !status.FixtureVerified {
		t.Fatalf("fixture sha256 mismatch: pin=%s", PinATMOfficialScoringFixtureSHA256)
	}
}
