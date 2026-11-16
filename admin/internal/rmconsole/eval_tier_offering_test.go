// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package rollingmemory

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestNormalizeTierDefaultsToOfferedLoCoMoSet(t *testing.T) {
	for _, testCase := range []struct {
		tier string
		want string
	}{
		{"", "1540"},
		{"unknown", "1540"},
		{"100", "1540"}, // 便捷档不再提供：归一到上游官方 cat1-4 全集
		{"200", "1540"},
		{"1540", "1540"},
	} {
		if got := normalizeTier(testCase.tier); got != testCase.want {
			t.Errorf("normalizeTier(%q) = %q, want %q", testCase.tier, got, testCase.want)
		}
	}
}

func TestEvalOverviewQuestionFailureKeepsWorkspaceAvailable(t *testing.T) {
	home := t.TempDir()
	dataDir := filepath.Join(home, "evaldata")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir evaldata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "MANIFEST.json"), []byte(`{"version":"test"}`), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("ROLLING_MEMORY_EVAL_DATA", dataDir)

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/admin/plugin/rolling-memory/eval/overview", strings.NewReader(`{"suite":"locomo"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	evalOverviewHandler(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("overview status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Data EvalOverviewResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode overview: %v", err)
	}
	if response.Data.Tier != "1540" {
		t.Fatalf("default tier = %q, want 1540", response.Data.Tier)
	}
	if response.Data.QuestionError == "" {
		t.Fatal("missing question file must be reported in the workspace")
	}
	if response.Data.TotalQuestions != 0 || len(response.Data.Questions) != 0 {
		t.Fatalf("missing question file returned synthetic questions: total=%d rows=%d", response.Data.TotalQuestions, len(response.Data.Questions))
	}
}

func TestLoCoMoTier100RemainsArchivedHistory(t *testing.T) {
	found := false
	for _, record := range referenceBaselines() {
		if record.Tier == "100" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("retiring the offered tier must not erase archived baselines")
	}
}
