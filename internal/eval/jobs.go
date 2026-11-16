// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package eval

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// EvalJobSummary 是评测台账的只读投影：历史 `jobs/<id>/job.json` 全量状态机台账，
// 或直接落在 out-dir 的 `summary-*.json` 运行产物，统一映射为同一条运行记录。
type EvalJobSummary struct {
	JobID     string           `json:"job_id"`
	Suite     string           `json:"suite,omitempty"`
	Tier      string           `json:"tier,omitempty"`
	State     string           `json:"state,omitempty"`
	CreatedAt string           `json:"created_at,omitempty"`
	UpdatedAt string           `json:"updated_at,omitempty"`
	Stages    []map[string]any `json:"stages,omitempty"`
	Summary   map[string]any   `json:"summary,omitempty"`
}

// ListJobs 读取 out-dir 评测台账（只读，绝不触发任何评测阶段）：
//  1. 首选 out-dir/jobs/<id>/job.json（全量状态机台账）；
//  2. 兼容散落在 out-dir 的 summary-*.json（直接产物），逐份投影为一条已完成运行；
//  3. 两者同名时以 job.json 优先，返回按更新时刻倒序。
func ListJobs(outDir string) ([]EvalJobSummary, error) {
	outDir = strings.TrimSpace(outDir)
	if outDir == "" {
		return nil, errors.New("eval ledger: empty out-dir")
	}
	jobs := []EvalJobSummary{}
	seen := map[string]bool{}
	if entries, err := os.ReadDir(filepath.Join(outDir, "jobs")); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(outDir, "jobs", entry.Name(), "job.json"))
			if err != nil {
				continue
			}
			var job EvalJobSummary
			if err := json.Unmarshal(raw, &job); err != nil {
				continue
			}
			if strings.TrimSpace(job.JobID) == "" {
				job.JobID = entry.Name()
			}
			seen[job.JobID] = true
			jobs = append(jobs, job)
		}
	}
	if entries, err := os.ReadDir(outDir); err == nil {
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasPrefix(name, "summary-") || !strings.HasSuffix(name, ".json") {
				continue
			}
			jobID := strings.TrimSuffix(strings.TrimPrefix(name, "summary-"), ".json")
			if seen[jobID] {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(outDir, name))
			if err != nil {
				continue
			}
			var summary map[string]any
			if err := json.Unmarshal(raw, &summary); err != nil || summary == nil {
				continue
			}
			job := EvalJobSummary{
				JobID:   jobID,
				State:   "completed",
				Summary: summary,
				Suite:   evalText(summary["suite"]),
				Tier:    evalText(summary["tier"]),
				Stages:  []map[string]any{{"name": "all", "status": "completed"}},
			}
			if st, statErr := entry.Info(); statErr == nil {
				job.UpdatedAt = st.ModTime().UTC().Format(time.RFC3339)
			}
			seen[jobID] = true
			jobs = append(jobs, job)
		}
	}
	sort.SliceStable(jobs, func(i, j int) bool {
		if jobs[i].UpdatedAt != jobs[j].UpdatedAt {
			return jobs[i].UpdatedAt > jobs[j].UpdatedAt
		}
		return jobs[i].JobID > jobs[j].JobID
	})
	return jobs, nil
}

// SelectJob 返回 --job 过滤（精确优先、再子串）后的运行记录；job 为空时取最新一条（列表已倒序）。
func SelectJob(jobs []EvalJobSummary, want string) *EvalJobSummary {
	want = strings.TrimSpace(want)
	if len(jobs) == 0 {
		return nil
	}
	if want == "" {
		return &jobs[0]
	}
	for i := range jobs {
		if jobs[i].JobID == want {
			return &jobs[i]
		}
	}
	for i := range jobs {
		if strings.Contains(jobs[i].JobID, want) {
			return &jobs[i]
		}
	}
	return nil
}

// FilterJobs 返回 --job 过滤后的台账子集（空串返回全量）。
func FilterJobs(jobs []EvalJobSummary, want string) []EvalJobSummary {
	want = strings.TrimSpace(want)
	if want == "" {
		return jobs
	}
	out := make([]EvalJobSummary, 0, len(jobs))
	for _, job := range jobs {
		if job.JobID == want || strings.Contains(job.JobID, want) {
			out = append(out, job)
		}
	}
	return out
}

func evalText(v any) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}
