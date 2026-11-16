// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package eval

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 题集（协议口径）：1540 = LoCoMo 上游官方 cat1-4 全集（282+321+96+841，随附官方证据）。
// 便捷档 100（200 件的确定性前缀）与 200（LoCoMo10 十会话 200 题）不再提供：题集以上游官方
// 为准，仅作为归档历史保留在旧台账与图表里，不再提供发起新评测。
const (
	Tier100  = "100"
	Tier200  = "200"
	Tier1540 = "1540"
)

// TierNames 是当前可用档位的固定顺序（报告与帮助文本用）。
var TierNames = []string{Tier1540}

// ErrRetiredLoCoMoTier 表示请求的是不再提供的 LoCoMo 便捷档。
var ErrRetiredLoCoMoTier = errors.New("LoCoMo 便捷档 100/200 不再提供：题集以上游官方为准（cat1-4 全集），请使用 --tier 1540")

// CheckLoCoMoTier 拒绝不再提供的档位（新评测发起前调用；归档历史读数不受影响）。
func CheckLoCoMoTier(tier string) error {
	switch strings.TrimSpace(tier) {
	case Tier100, Tier200:
		return ErrRetiredLoCoMoTier
	}
	return nil
}

// DataDirEnv 指向随包发布的评测数据目录（未设时按「可执行文件同级 → 当前目录」找）。
const DataDirEnv = "ROLLING_MEMORY_EVAL_DATA"

// DataDirName 是随包目录名。
const DataDirName = "evaldata"

// FileEntry 描述一个随包数据件。
type FileEntry struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Rows   int    `json:"rows"`
}

// TierEntry 描述一档题集。
type TierEntry struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
	Count  int    `json:"count"`
	Note   string `json:"note"`
}

// Manifest 是 evaldata/MANIFEST.json：语料 + 三档题集 + 来源。
type Manifest struct {
	Version  string               `json:"version"`
	Protocol string               `json:"protocol"`
	Source   map[string]string    `json:"source"`
	Corpus   FileEntry            `json:"corpus"`
	Tiers    map[string]TierEntry `json:"tiers"`
}

// ResolveDataDir 按 显式参数 → 环境变量 → 可执行文件同级/父级（含解析符号链接） → 当前目录及祖先链 → 用户数据目录（~/.local/share/rolling-memory/evaldata） → 家目录常见克隆位置 解析数据目录。
func ResolveDataDir(explicit string) (string, error) {
	candidates := []string{}
	addExeRel := func(exePath string) {
		if strings.TrimSpace(exePath) == "" {
			return
		}
		dir := filepath.Dir(exePath)
		candidates = append(candidates,
			filepath.Join(dir, DataDirName),
			filepath.Join(filepath.Dir(dir), DataDirName),
			filepath.Join(filepath.Dir(filepath.Dir(dir)), DataDirName),
		)
	}
	if strings.TrimSpace(explicit) != "" {
		cand := expandUser(explicit)
		if info, err := os.Stat(filepath.Join(cand, "MANIFEST.json")); err == nil && !info.IsDir() {
			return cand, nil
		}
		return "", fmt.Errorf("找不到评测数据目录（%s/MANIFEST.json）：用 --eval-data <dir> 指定，或设 %s", cand, DataDirEnv)
	}
	if env := strings.TrimSpace(os.Getenv(DataDirEnv)); env != "" {
		candidates = append(candidates, expandUser(env))
	}
	if exe, err := os.Executable(); err == nil {
		addExeRel(exe)
		if realExe, lErr := filepath.EvalSymlinks(exe); lErr == nil && realExe != exe {
			addExeRel(realExe)
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		base := cwd
		for depth := 0; depth <= 4; depth++ {
			candidates = append(candidates, filepath.Join(base, DataDirName))
			parent := filepath.Dir(base)
			if parent == base {
				break
			}
			base = parent
		}
	}
	if xdgData := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); xdgData != "" {
		candidates = append(candidates, filepath.Join(expandUser(xdgData), "rolling-memory", DataDirName))
	}
	if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
		candidates = append(candidates, filepath.Join(localAppData, "rolling-memory", DataDirName))
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		candidates = append(candidates,
			filepath.Join(home, ".local", "share", "rolling-memory", DataDirName),
			filepath.Join(home, "Rolling-Memory", DataDirName),
			filepath.Join(home, "rolling-memory", DataDirName),
		)
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(filepath.Join(candidate, "MANIFEST.json")); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("找不到评测数据目录（%s/MANIFEST.json）：用 --eval-data <dir> 指定，或设 %s 指向自备数据目录（评测语料不随仓库分发）",
		DataDirName, DataDirEnv)
}

// LoadManifest 读数据目录清单。
func LoadManifest(dataDir string) (Manifest, error) {
	raw, err := os.ReadFile(filepath.Join(dataDir, "MANIFEST.json"))
	if err != nil {
		return Manifest{}, err
	}
	manifest := Manifest{}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("%s/MANIFEST.json 解析失败: %w", dataDir, err)
	}
	if manifest.Corpus.Path == "" || len(manifest.Tiers) == 0 {
		return Manifest{}, fmt.Errorf("%s/MANIFEST.json 缺少 corpus 或 tiers", dataDir)
	}
	return manifest, nil
}

// Tier 取一档题集（空 = 默认 200 档）。
func (m Manifest) Tier(name string) (TierEntry, error) {
	key := strings.TrimSpace(name)
	if key == "" {
		key = Tier1540
	}
	entry, ok := m.Tiers[key]
	if !ok {
		return TierEntry{}, fmt.Errorf("档位 %q 不在清单里（可用：%s）", key, strings.Join(TierNames, " / "))
	}
	return entry, nil
}
