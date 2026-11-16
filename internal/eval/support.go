// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package eval

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"

	"github.com/rocareer/rolling-memory/internal/ids"
)

// 本文件收拢 runner 需要的小工具（与 kit 里已有者不重复）。

func questionText(question map[string]any) string { return Str(question["question"]) }

func goldAnswer(question map[string]any) string { return Str(firstTruthy(question["answer"], "")) }

func evidenceTexts(question map[string]any) []string {
	raw, _ := question["evidence_texts"].([]any)
	texts := []string{}
	for _, item := range raw {
		if Truthy(item) {
			texts = append(texts, Str(item))
		}
	}
	return texts
}

func rowValue(row map[string]any, key string) float64 {
	switch typed := row[key].(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	}
	return 0
}

func joinLines(lines []string) string { return strings.Join(lines, "\n") }

func expandUser(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "~" || strings.HasPrefix(trimmed, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(trimmed[1:], "/"))
		}
	}
	return path
}

// sliceStart / sliceStop 复刻 standard 切片边界（start 过界夹紧、stop 不早于 start）。
func sliceStart(offset, length int) int {
	start := offset
	if start < 0 {
		start += length
		if start < 0 {
			start = 0
		}
	}
	if start > length {
		start = length
	}
	return start
}

func sliceStop(start, limit, length int) int {
	stop := start + limit
	if stop < 0 {
		stop += length
	}
	if stop < start {
		stop = start
	}
	if stop > length {
		stop = length
	}
	return stop
}

// sha256Hex 原始字节 sha256（小写十六进制；数据件核验用）。
func sha256Hex(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func hostOf(rawURL string) string {
	if parsed, ok := ids.URLHost(rawURL); ok && parsed != "" {
		return parsed
	}
	return "remote"
}
