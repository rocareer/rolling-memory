package eval

import (
	"errors"
	"testing"
)

// TestATMOfficialTiersOnlyAndRetired100FailClosed 钉住官方题集口径不变量：
// 官方 ATM-Bench 只有 Main-1013 与 Hard-31 两档；自造的 100 题前缀档必须 fail-closed 拒跑，
// 严禁静默降级为 hard（否则旧脚本/旧 URL 会以「hard 读数」冒充「100 档读数」）。
func TestATMOfficialTiersOnlyAndRetired100FailClosed(t *testing.T) {
	if got := NormalizeATMTier("hard"); got != TierATMHard {
		t.Fatalf("NormalizeATMTier(hard) = %q, want %q", got, TierATMHard)
	}
	if got := NormalizeATMTier("1013"); got != TierATM1013 {
		t.Fatalf("NormalizeATMTier(1013) = %q, want %q", got, TierATM1013)
	}
	for _, retired := range []string{"100", "main-100", "main100", "atm-100", "atm100", "100 "} {
		if got := NormalizeATMTier(retired); got != "" {
			t.Fatalf("NormalizeATMTier(%q) = %q, want empty (fail-closed)", retired, got)
		}
	}
	if _, err := LoadATMQAItems(t.TempDir(), "100", 0, 0); !errors.Is(err, ErrATMTierRetired) {
		t.Fatalf("LoadATMQAItems(tier=100) err = %v, want ErrATMTierRetired", err)
	}
}

// TestATMOfficialCorpusPinsCoverOnlyOfficialTiers 钉住 pin 表只覆盖官方两档题集 + 语料，
// 防止 100 档文件被重新引入（删档后若误留 pin 会让全量 ATM 评测在 pin 校验处 fail-closed）。
func TestATMOfficialCorpusPinsCoverOnlyOfficialTiers(t *testing.T) {
	pins := ATMOfficialCorpusSHA256s()
	want := map[string]bool{
		"corpus.jsonl.gz":           true,
		"tiers/questions-1013.json": true,
		"tiers/questions-hard.json": true,
	}
	if len(pins) != len(want) {
		t.Fatalf("ATMOfficialCorpusSHA256s() has %d entries, want %d: %v", len(pins), len(want), pins)
	}
	for rel := range want {
		if _, ok := pins[rel]; !ok {
			t.Fatalf("ATMOfficialCorpusSHA256s() missing %q: %v", rel, pins)
		}
	}
	for rel := range pins {
		if rel == "tiers/questions-100.json" {
			t.Fatalf("已废止的 100 档 pin 重新出现：%v", pins)
		}
		if !want[rel] {
			t.Fatalf("ATMOfficialCorpusSHA256s() contains unexpected entry %q", rel)
		}
	}
}
