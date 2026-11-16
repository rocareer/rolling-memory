package rollingmemory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEvalScopeGenerationFormParity 钉住 Admin 与 rmem（internal/eval 的 SplitEvalScope）同源的
// scope 形态闭集：<user>-mem-eval-<locomo|atmbench>[-<世代号>]。
// 两侧测试表逐字相同：改一处必须同改另一处（跨模块 parity 的唯一守卫）。
func TestEvalScopeGenerationFormParity(t *testing.T) {
	cases := []struct {
		scope      string
		wantSuite  string
		wantGen    string
		wantAccept bool
	}{
		{"u10-mem-eval-locomo", SuiteLoCoMo, "", true},
		{"u10-mem-eval-atmbench", SuiteATM, "", true},
		{"u10-mem-eval-locomo-pulse-plain", SuiteLoCoMo, "", true},
		{"u10-mem-eval-atmbench-pulse-plain", SuiteATM, "", true},
		{"u10-mem-eval-locomo-local-plain", SuiteLoCoMo, "", true},
		{"u10-mem-eval-atmbench-local-plain", SuiteATM, "", true},
		{"u10-mem-eval-locomo-20261107t153012z-7f3c", SuiteLoCoMo, "20261107t153012z-7f3c", true},
		{"u10-mem-eval-atmbench-20261107t153012z-7f3c", SuiteATM, "20261107t153012z-7f3c", true},
		{"u10-mem-eval-locomo-pulse-plain-20261107t153012z-7f3c", SuiteLoCoMo, "20261107t153012z-7f3c", true},
		{"u10-mem-eval-atmbench-pulse-plain-20261107t153012z-7f3c", SuiteATM, "20261107t153012z-7f3c", true},
		{"u10-mem-eval-locomo-local-plain-20261107t153012z-7f3c", SuiteLoCoMo, "20261107t153012z-7f3c", true},
		{"u10-mem-eval-atmbench-local-plain-20261107t153012z-7f3c", SuiteATM, "20261107t153012z-7f3c", true},
		{"user-mem-eval-locomo-gen1", SuiteLoCoMo, "gen1", true},
		{"u10-mem-eval-locomo-", "", "", false},
		{"u10-mem-eval-atmbenchx", "", "", false},
		{"mem-eval-u10-locomo", "", "", false},
		{"mem-eval-u10-full-locomo-100", "", "", false},
		{"-mem-eval-locomo", "", "", false},
		{"mem:self", "", "", false},
		{"default", "", "", false},
		{"u10-mem-eval-locomo-2026 1107", "", "", false},
		{"u10-mem-eval-locomo-x:", "", "", false},
	}
	for _, testCase := range cases {
		suite, ok := evalScopeSuiteName(testCase.scope)
		if ok != testCase.wantAccept || suite != testCase.wantSuite {
			t.Errorf("evalScopeSuiteName(%q) = (%q, %v)，期望 (%q, %v)",
				testCase.scope, suite, ok, testCase.wantSuite, testCase.wantAccept)
		}
		if got := evalScopeGeneration(testCase.scope); got != testCase.wantGen {
			t.Errorf("evalScopeGeneration(%q) = %q，期望 %q", testCase.scope, got, testCase.wantGen)
		}
	}
}

// writeEvalCloudTestManifest 在 store 上写一份合法的评测 manifest（默认带世代 scope），
// 供「本 store 的当前世代」类断言使用。
func writeEvalCloudTestManifest(t *testing.T, storePath, scope, suite string) {
	t.Helper()
	manifest := `{"transport":"ramesdk.pulse_cipher.open-memory-v1","endpoint":"http://127.0.0.1:18081/open/memory/v1",` +
		`"tenant_id":"user","user_id":"10","pulse_mode":"pulse_cipher","suite":"` + suite + `","scope":"` + scope + `",` +
		`"corpus_sha256":"` + strings.Repeat("a", 64) + `","documents":2,"local_fallback":false}`
	if err := os.WriteFile(storePath+".manifest.json", []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

// TestReadCloudEvalManifestAcceptsGenerationScope 世代 scope 的 manifest 必须被 Admin 接受
// （每次新评测自动分配独立 scope），而套件不匹配 / 命名空间外的一律拒绝。
func TestReadCloudEvalManifestAcceptsGenerationScope(t *testing.T) {
	base := `{"transport":"ramesdk.pulse_cipher.open-memory-v1","endpoint":"http://127.0.0.1:18081/open/memory/v1",` +
		`"tenant_id":"user","user_id":"10","pulse_mode":"pulse_cipher","suite":"locomo","corpus_sha256":"` + strings.Repeat("a", 64) + `",` +
		`"documents":2,"local_fallback":false`
	write := func(t *testing.T, scope string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "vault.json")
		if err := os.WriteFile(path+".manifest.json", []byte(base+`,"scope":"`+scope+`"}`), 0o644); err != nil {
			t.Fatalf("write manifest: %v", err)
		}
		return path
	}
	if manifest, err := readCloudEvalManifest(write(t, "u10-mem-eval-locomo-20261107t153012z-7f3c"), SuiteLoCoMo); err != nil {
		t.Fatalf("世代 scope manifest 必须被接受: %v", err)
	} else if manifest.Scope != "u10-mem-eval-locomo-20261107t153012z-7f3c" {
		t.Fatalf("manifest scope 读回不符: %q", manifest.Scope)
	}
	if _, err := readCloudEvalManifest(write(t, "u10-mem-eval-atmbench"), SuiteLoCoMo); err == nil {
		t.Fatal("跨套件 scope 必须被拒绝")
	}
	if _, err := readCloudEvalManifest(write(t, "mem:self"), SuiteLoCoMo); err == nil {
		t.Fatal("评测命名空间外的 scope 必须被拒绝")
	}
}
