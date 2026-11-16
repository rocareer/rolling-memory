package eval

// Byte-for-byte parity gate for the official MMRag answerer payload.
//
// Oracle: memqa/qa_agent_baselines/MMRag/{config.py::PROMPTS,
// mmrag_utils.py::build_text_messages} (upstream ATM-Bench, fetched 2026-11-09).
// The fixture is pinned by PinATMOfficialAnswerPayloadFixtureSHA256; this test
// fails on any byte drift in the system prompt, user template, evidence joining,
// or FormatATMEvidencePayload output.

import (
	"encoding/json"
	"fmt"
	"testing"
)

type atmAnswerPayloadCase struct {
	ID             string   `json:"id"`
	Question       string   `json:"question"`
	EvidenceChunks []string `json:"evidence_chunks"`
	System         string   `json:"system"`
	User           string   `json:"user"`
	SystemSHA256   string   `json:"system_sha256"`
	UserSHA256     string   `json:"user_sha256"`
}

type atmAnswerPayloadFixture struct {
	Version            string                 `json:"version"`
	Source             string                 `json:"source"`
	SystemPrompt       string                 `json:"system_prompt"`
	UserTemplate       string                 `json:"user_template"`
	SystemSHA256       string                 `json:"system_sha256"`
	UserTemplateSHA256 string                 `json:"user_template_sha256"`
	Cases              []atmAnswerPayloadCase `json:"cases"`
}

func TestATMOfficialAnswerPayloadParity(t *testing.T) {
	var fixture atmAnswerPayloadFixture
	if err := json.Unmarshal(ATMOfficialAnswerPayloadFixture(), &fixture); err != nil {
		t.Fatalf("decode embedded answer payload fixture: %v", err)
	}
	if fixture.Version != ATMOfficialScoringProtocol {
		t.Fatalf("fixture version = %q, want %q", fixture.Version, ATMOfficialScoringProtocol)
	}
	if ATMSystemPrompt != fixture.SystemPrompt {
		t.Fatalf("answer system prompt byte drift:\n got %q\nwant %q", ATMSystemPrompt, fixture.SystemPrompt)
	}
	if ATMEvidencePromptTPL != fixture.UserTemplate {
		t.Fatalf("answer user template byte drift:\n got %q\nwant %q", ATMEvidencePromptTPL, fixture.UserTemplate)
	}
	if len(fixture.Cases) == 0 {
		t.Fatal("fixture has no cases")
	}
	for _, c := range fixture.Cases {
		ids := make([]string, 0, len(c.EvidenceChunks))
		byID := make(map[string]*ATMSGMItem, len(c.EvidenceChunks))
		for i, chunk := range c.EvidenceChunks {
			id := fmt.Sprintf("ev-%d", i+1)
			ids = append(ids, id)
			byID[id] = &ATMSGMItem{ItemID: id, FullText: chunk}
		}
		user, _ := FormatATMEvidencePayload(c.Question, ids, byID, len(ids))
		if user != c.User {
			t.Fatalf("case %s user payload byte drift:\n got %q\nwant %q", c.ID, user, c.User)
		}
		if ATMSystemPrompt != c.System {
			t.Fatalf("case %s system prompt byte drift", c.ID)
		}
	}
	t.Logf("ATM official answer payload parity: %d cases byte-identical (system %dB, user template %dB)",
		len(fixture.Cases), len(ATMSystemPrompt), len(ATMEvidencePromptTPL))
}

func TestATMOfficialAnswerPayloadFixtureIsPinned(t *testing.T) {
	status := ATMOfficialPinSnapshot("")
	if !status.AnswerPayloadVerified {
		t.Fatalf("answer payload fixture sha256 mismatch: pin=%s", PinATMOfficialAnswerPayloadFixtureSHA256)
	}
}
