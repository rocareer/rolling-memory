package rollingmemory

import (
	"testing"
)

func TestATMRecallMetricSpecContract(t *testing.T) {
	spec := buildATMProtocolParitySpec()
	if want := "官方 Recall@10 = 逐题金标证据覆盖率的平均值；Any-Hit@10（至少命中一条）与 Recall@kGT（全部命中）为辅助指标。"; spec.RecallMetricSpec != want {
		t.Errorf("RecallMetricSpec = %q, want %q", spec.RecallMetricSpec, want)
	}
	if want := "Official Recall@10 is the mean per-question gold evidence coverage; Any-Hit@10 (at least one hit) and Recall@kGT (all evidence hit) are auxiliary metrics."; spec.RecallMetricSpecEn != want {
		t.Errorf("RecallMetricSpecEn = %q, want %q", spec.RecallMetricSpecEn, want)
	}
}
