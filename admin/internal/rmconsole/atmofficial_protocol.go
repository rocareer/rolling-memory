package rollingmemory

// ATM-Bench official protocol pins and verification (admin module copy).
//
// The admin module is a separate Go module; its normalizer/scoring port stays
// byte-identical to internal/eval. This file is the admin-side adapter for the
// root-only helpers (data-dir resolution and the two prompt constants already
// present in this package).

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// ErrATMTierRetiredAdmin 与根模块 eval.ErrATMTierRetired 同口径。
// admin 是独立 Go 模块，不得跨模块复用符号，故在此声明等价错误。
var ErrATMTierRetiredAdmin = errors.New("ATM-Bench 官方题集只有 Main-1013 与 Hard-31；自造的 100 题前缀档已于 2026-11-09 废止，请改用 tier=1013 或 tier=hard")

const (
	ATMOfficialScoringProtocol = "atmbench-official-v1"
	ATMOfficialScoringVersion  = "2026-11-09"

	PinATMOfficialScoringFixtureSHA256 = "a9feda7cd9af9d8af2620f526edf84e1b5d717a82140bb0c842ef9c8e67a2795"

	PinATMOfficialAnswerPayloadFixtureSHA256 = "0c2f3bb860fec690abf939c14acca0ea1d759083114463c997481734d0b9ea5e"

	PinATMOfficialNormalizerSHA1 = "ab703050dc18c0b96a8ea042f56b4210e4774a64"
	PinATMOfficialEvaluateQASHA1 = "bf940c0ba32c888033e22b6ceba8b42e9b759533"
	PinATMOfficialQTypeUtilsSHA1 = "dc888169c8c5f925a47a5e7fbeab4ec2b0e25ba8"
	PinATMOfficialConfigSHA1     = "a7126e059ce6745e84453f1fc1dd183656479807"

	PinATMCorpusSHA256 = "c9c4cc55dd3be147638121765b5dc7b8321a5e292b90909b68ae7fe8a57ee3a7"
	PinATM1013SHA256   = "3c4cbf84deeb158c78d515626a540908878d304ce0cda5c8e2ae7e52c6b878a2"
	PinATMHardSHA256   = "f6f63ff196a9f1de5715e35b328d4e80ca80311f2e2eae8028e7cb60e87ad37e"
)

//go:embed testdata/atm_official_scoring_vectors.json
var atmOfficialScoringFixtureJSON []byte

//go:embed testdata/atm_official_answer_payload_vectors.json
var atmOfficialAnswerPayloadFixtureJSON []byte

func atmOfficialScoringFixture() []byte {
	return append([]byte(nil), atmOfficialScoringFixtureJSON...)
}

func atmOfficialAnswerPayloadFixture() []byte {
	return append([]byte(nil), atmOfficialAnswerPayloadFixtureJSON...)
}

func verifyATMOfficialProtocolAdmin() error {
	if sha1Hex(ATMSystemPrompt) != PinATMSystemSHA1 {
		return fmt.Errorf("ATM-Bench 答题 system sha1 不符")
	}
	if sha1Hex(ATMEvidencePromptTPL) != PinATMEvidencePromptSHA1 {
		return fmt.Errorf("ATM-Bench 答题 user 模板 sha1 不符")
	}
	if sha1Hex(ATMJudgePromptTPL) != PinATMJudgeTPLSHA1 {
		return fmt.Errorf("ATM-Bench 评分模板 sha1 不符")
	}
	sum := sha256.Sum256(atmOfficialScoringFixtureJSON)
	if got := hex.EncodeToString(sum[:]); got != PinATMOfficialScoringFixtureSHA256 {
		return fmt.Errorf("ATM-Bench official scoring fixture sha256 不符：期望 %s，实际 %s", PinATMOfficialScoringFixtureSHA256, got)
	}
	payloadSum := sha256.Sum256(atmOfficialAnswerPayloadFixtureJSON)
	if got := hex.EncodeToString(payloadSum[:]); got != PinATMOfficialAnswerPayloadFixtureSHA256 {
		return fmt.Errorf("ATM-Bench official answer payload fixture sha256 不符：期望 %s，实际 %s", PinATMOfficialAnswerPayloadFixtureSHA256, got)
	}
	return nil
}

func atmOfficialSourceSHA1s() map[string]string {
	return map[string]string{
		"normalizer.py":  PinATMOfficialNormalizerSHA1,
		"evaluate_qa.py": PinATMOfficialEvaluateQASHA1,
		"qtype_utils.py": PinATMOfficialQTypeUtilsSHA1,
		"config.py":      PinATMOfficialConfigSHA1,
	}
}

func atmOfficialCorpusSHA256s() map[string]string {
	return map[string]string{
		"corpus.jsonl.gz":           PinATMCorpusSHA256,
		"tiers/questions-1013.json": PinATM1013SHA256,
		"tiers/questions-hard.json": PinATMHardSHA256,
	}
}

func verifyATMCorpusPinsAdmin(atmDir string) error {
	if atmDir == "" {
		return fmt.Errorf("ATM-Bench 数据目录未解析")
	}
	for rel, want := range atmOfficialCorpusSHA256s() {
		path := filepath.Join(atmDir, filepath.FromSlash(rel))
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("ATM-Bench 官方语料 pin 读取失败 (%s): %w", rel, err)
		}
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != want {
			return fmt.Errorf("ATM-Bench 官方语料 pin 不符 (%s)：期望 %s，实际 %s", rel, want, got)
		}
	}
	return nil
}

type ATMOfficialPinStatus struct {
	Protocol              string            `json:"protocol"`
	Version               string            `json:"version"`
	PromptPins            map[string]string `json:"prompt_pins"`
	PromptPinsVerified    bool              `json:"prompt_pins_verified"`
	FixturePin            string            `json:"fixture_pin"`
	FixtureVerified       bool              `json:"fixture_verified"`
	AnswerPayloadPin      string            `json:"answer_payload_fixture_pin"`
	AnswerPayloadVerified bool              `json:"answer_payload_fixture_verified"`
	SourcePins            map[string]string `json:"source_pins"`
	CorpusPins            map[string]string `json:"corpus_pins"`
	CorpusPinsVerified    bool              `json:"corpus_pins_verified"`
	CorpusPinsError       string            `json:"corpus_pins_error,omitempty"`
}

func atmOfficialPinSnapshotAdmin(atmDir string) ATMOfficialPinStatus {
	status := ATMOfficialPinStatus{
		Protocol:         ATMOfficialScoringProtocol,
		Version:          ATMOfficialScoringVersion,
		PromptPins:       map[string]string{"answer_system_sha1": PinATMSystemSHA1, "answer_user_template_sha1": PinATMEvidencePromptSHA1, "judge_template_sha1": PinATMJudgeTPLSHA1},
		FixturePin:       PinATMOfficialScoringFixtureSHA256,
		AnswerPayloadPin: PinATMOfficialAnswerPayloadFixtureSHA256,
		SourcePins:       atmOfficialSourceSHA1s(),
		CorpusPins:       atmOfficialCorpusSHA256s(),
	}
	status.PromptPinsVerified = sha1Hex(ATMSystemPrompt) == PinATMSystemSHA1 && sha1Hex(ATMEvidencePromptTPL) == PinATMEvidencePromptSHA1 && sha1Hex(ATMJudgePromptTPL) == PinATMJudgeTPLSHA1
	sum := sha256.Sum256(atmOfficialScoringFixtureJSON)
	status.FixtureVerified = hex.EncodeToString(sum[:]) == PinATMOfficialScoringFixtureSHA256
	payloadSum := sha256.Sum256(atmOfficialAnswerPayloadFixtureJSON)
	status.AnswerPayloadVerified = hex.EncodeToString(payloadSum[:]) == PinATMOfficialAnswerPayloadFixtureSHA256
	if err := verifyATMCorpusPinsAdmin(atmDir); err != nil {
		status.CorpusPinsError = err.Error()
	} else {
		status.CorpusPinsVerified = true
	}
	return status
}

func atmOfficialPinIndexAdmin() []string {
	lines := make([]string, 0, 12)
	for name, value := range atmOfficialSourceSHA1s() {
		lines = append(lines, "evaluator/"+name+"="+value)
	}
	for name, value := range atmOfficialCorpusSHA256s() {
		lines = append(lines, "corpus/"+name+"="+value)
	}
	lines = append(lines, "fixture/sha256="+PinATMOfficialScoringFixtureSHA256)
	lines = append(lines, "fixture/answer_payload_sha256="+PinATMOfficialAnswerPayloadFixtureSHA256)
	lines = append(lines,
		"prompt/answer_system="+PinATMSystemSHA1,
		"prompt/answer_user="+PinATMEvidencePromptSHA1,
		"prompt/judge="+PinATMJudgeTPLSHA1,
	)
	sort.Strings(lines)
	return lines
}
