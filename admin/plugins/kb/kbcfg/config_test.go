// plugin: kb v1.0.0
// 配置口径用例（无库，纯函数面）：把「哪些键允许被 configs 行覆盖、哪些只读内嵌缺省」写死成断言。
//
// 为什么必须落成用例：这张对照表是**会静默漂移**的约定面——安装了插件却不生效的覆盖键、或
// 悄悄改了 build_hash 身份键（chunk.levels / chunk.window / 表头重复…），都不会让任何门禁变红。
package kbcfg

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// TestOverridableKeyWhitelist 覆盖键闭集 = 14 个（顺序也冻结：与 plugin.yaml 的 configs 段逐项对齐）
func TestOverridableKeyWhitelist(t *testing.T) {
	want := []string{
		"embed_enabled", "embed_provider", "embed_model", "embed_dim",
		"chunk_target", "chunk_overlap", "chunk_hard_cap",
		"fts_config", "search_scan_cap", "prune_retention_days", "log_query_text_max",
		"media_ffmpeg_bin", "limits_max_pages", "limits_max_minutes",
	}
	got := append([]string(nil), OverridableKeys...)
	sort.Strings(got)
	sorted := append([]string(nil), want...)
	sort.Strings(sorted)
	if strings.Join(got, ",") != strings.Join(sorted, ",") {
		t.Fatalf("可覆盖键闭集不符：\n实际 %v\n期望 %v", got, sorted)
	}
}

// TestOverridableKeysMatchManifest 覆盖键闭集与插件清单的 configs 段**逐项一致**（防两处漂移）
func TestOverridableKeysMatchManifest(t *testing.T) {
	dir := repoRootOf(t)
	raw, err := os.ReadFile(filepath.Join(dir, "plugins", "kb", "plugin.yaml"))
	if err != nil {
		t.Fatalf("读取插件清单失败: %v", err)
	}
	var mf struct {
		Configs []struct {
			Name string `yaml:"name"`
		} `yaml:"configs"`
	}
	if err := yaml.Unmarshal(raw, &mf); err != nil {
		t.Fatalf("解析插件清单失败: %v", err)
	}
	declared := make([]string, 0, len(mf.Configs))
	for _, item := range mf.Configs {
		declared = append(declared, item.Name)
	}
	got := append([]string(nil), OverridableKeys...)
	sort.Strings(declared)
	sort.Strings(got)
	if strings.Join(declared, ",") != strings.Join(got, ",") {
		t.Fatalf("清单 configs 段与 OverridableKeys 不一致：\n清单 %v\n代码 %v", declared, got)
	}
}

// TestApplyOverrides 覆盖语义：行存在即以其值为准（含空串）；缺失回落内嵌缺省；非数值回落当前值
func TestApplyOverrides(t *testing.T) {
	base := Defaults()

	// 内嵌缺省的关键读数（改档即改口径，故在此钉住出厂值）
	if base.Embed.Enabled {
		t.Fatal("内嵌缺省 embed.enabled 必须为 false（无密钥环境下开它会让每次导入在嵌入阶段响亮失败）")
	}
	if base.Search.ScanCap != 5000 || base.Prune.RetentionDays != 180 {
		t.Fatalf("内嵌缺省读数不符：scan_cap=%d retention=%d", base.Search.ScanCap, base.Prune.RetentionDays)
	}
	if base.Chunk["target"] == nil || base.Chunk["hard_cap"] == nil {
		t.Fatal("内嵌缺省的 chunk 段必须带 target/hard_cap")
	}

	cfg := Defaults()
	ApplyOverrides(&cfg, map[string]string{
		"embed_enabled":        "1",
		"embed_provider":       "custom-provider",
		"embed_model":          "", // 空串是合法值：向量身份不可用 ⇒ 嵌入面 fail-closed
		"chunk_target":         "256",
		"chunk_overlap":        "0",
		"fts_config":           "", // 空 = 回落 simple（由 ingest.ResolveFTSConfig 单点判定）
		"search_scan_cap":      "1234",
		"log_query_text_max":   "0", // 0 = 关（不落查询原文）
		"limits_max_pages":     "42",
		"limits_max_minutes":   "7.5",
		"prune_retention_days": "30",
		"media_ffmpeg_bin":     "custom-ffmpeg",
		"embed_dim":            "未配", // 非数值 ⇒ 回落当前值（不把配置写坏读成 0）
		"unknown_key":          "ignored",
	})
	if !cfg.Embed.Enabled || cfg.Embed.Provider != "custom-provider" || cfg.Embed.Model != "" {
		t.Fatalf("嵌入档覆盖不符：%+v", cfg.Embed)
	}
	if cfg.Embed.Dim != base.Embed.Dim {
		t.Fatalf("非数值的 embed_dim 应回落 %d，实际 %d", base.Embed.Dim, cfg.Embed.Dim)
	}
	if cfg.Chunk["target"] != 256 || cfg.Chunk["overlap"] != 0 {
		t.Fatalf("chunk 覆盖不符：%v", cfg.Chunk)
	}
	if cfg.FTS.Config != "" {
		t.Fatalf("fts_config 空串应按值落地（空 = 回落 simple 由读取点判定），实际 %q", cfg.FTS.Config)
	}
	if cfg.Search.LogQueryTextMax != 0 {
		t.Fatalf("log_query_text_max=0 应按值落地（关），实际 %d", cfg.Search.LogQueryTextMax)
	}
	if cfg.Limits.MaxPages != 42 || cfg.Limits.MaxMinutes != 7.5 {
		t.Fatalf("limits 覆盖不符：%+v", cfg.Limits)
	}

	// 未列出的键（只读内嵌缺省）不受覆盖层影响
	untouched := Defaults()
	ApplyOverrides(&untouched, map[string]string{"chunk_levels": "[1,2]"})
	if len(untouched.Chunk) != len(base.Chunk) {
		t.Fatalf("非白名单键不应改动配置：%v", untouched.Chunk)
	}
}

// TestDefaultsIndependentCopies Defaults 返回副本（调用方改 Chunk 不污染进程内缺省）
func TestDefaultsIndependentCopies(t *testing.T) {
	first := Defaults()
	first.Chunk["target"] = 999
	second := Defaults()
	if second.Chunk["target"] == 999 {
		t.Fatal("Defaults 必须返回副本（否则用例间的改动会互相污染）")
	}
}

// repoRootOf 定位仓库根（配置键与清单对账需要读插件清单）
func repoRootOf(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("未找到 go.mod（仓库根）")
		}
		dir = parent
	}
}
