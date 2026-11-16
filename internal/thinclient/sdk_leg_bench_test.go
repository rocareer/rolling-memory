package thinclient

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "modernc.org/sqlite"
)

// BenchmarkRealVaultSDKLeg profiles SearchClientSDKDefault on a real vault
// copy (RM_LATENCY_DB), isolating the steady-state ~15ms leg for CPU
// attribution. Manual probe: skipped without the env var.
func BenchmarkRealVaultSDKLeg(b *testing.B) {
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
	queries := []string{
		"性能 优化 检索 延迟",
		"插件 召回 快照 指纹",
		"snapshot revision trigger latency",
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = v.SearchClientSDKDefault(queries[i%len(queries)], 20)
	}
}
