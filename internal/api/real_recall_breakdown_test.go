package api

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/rocareer/rolling-memory/internal/config"
)

// TestRealEndToEndRecallBreakdown times Memory.Recall end-to-end on a real
// vault copy (RM_LATENCY_DB), the exact path the MCP tool serves, so the gap
// between thinclient-internal time and the user-facing elapsed_ms becomes
// attributable. Manual probe: skipped without the env var.
func TestRealEndToEndRecallBreakdown(t *testing.T) {
	dbPath := os.Getenv("RM_LATENCY_DB")
	if dbPath == "" {
		t.Skip("set RM_LATENCY_DB to a real vault copy")
	}
	cfg, err := config.Load(config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	cfg.DB = dbPath
	m, err := New(cfg)
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	defer m.Close()

	run := func(label, cue string, novel bool) {
		for i := 0; i < 4; i++ {
			q := cue
			if novel {
				// Fresh cue per iteration defeats the per-hash posting cache,
				// matching real MCP usage where every recall carries new terms.
				q = fmt.Sprintf("%s 轮次%d 探针%d", cue, i, i*7+3)
			}
			t0 := time.Now()
			res, err := m.Recall(RecallRequest{Cue: q})
			wall := time.Since(t0)
			if err != nil {
				t.Fatalf("%s recall: %v", label, err)
			}
			inner := 0
			if res != nil && res.Meta != nil {
				if v, ok := res.Meta["elapsed_ms"].(int); ok {
					inner = v
				}
			}
			bands := 0
			if res != nil {
				bands = len(res.Bands)
			}
			t.Logf("%-10s #%d wall=%-12v inner=%dms bands=%d", label, i, wall, inner, bands)
		}
	}
	run("probeA", "性能 优化 检索 延迟", false)
	run("probeB", "插件 召回 快照 指纹", false)
	run("novelA", "性能 优化 检索 延迟", true)
	run("novelB", "插件 召回 快照 指纹", true)
	fmt.Fprintln(os.Stderr, "done")
}
