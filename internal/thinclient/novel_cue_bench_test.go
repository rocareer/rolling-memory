package thinclient

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"

	_ "modernc.org/sqlite"
)

// BenchmarkRealVaultNovelCue drives SearchClientSDKDefault with a fresh cue per
// iteration so per-hash caches never help, isolating the novel-term cost.
// Manual probe: skipped without RM_LATENCY_DB.
func BenchmarkRealVaultNovelCue(b *testing.B) {
	dbPath := os.Getenv("RM_LATENCY_DB")
	if dbPath == "" {
		b.Skip("set RM_LATENCY_DB")
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	v, err := NewVault(db, nil)
	if err != nil {
		b.Fatal(err)
	}
	if _, err := v.loadCorpusSnapshot(context.Background()); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q := fmt.Sprintf("性能 优化 检索 延迟 轮次%d 探针%d", i, i*7+3)
		_, _ = v.SearchClientSDKDefault(q, 20)
	}
}
