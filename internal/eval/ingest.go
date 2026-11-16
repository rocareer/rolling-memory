// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package eval

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// LoadCorpus reads the JSONL corpus, keeps the first row per id and honours
// the smoke limit.
func LoadCorpus(path string, limit int) ([]map[string]any, error) {
	handle, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	rows := []map[string]any{}
	seen := map[string]struct{}{}
	scanner := bufio.NewScanner(handle)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := pyStrip(scanner.Text())
		if line == "" {
			continue
		}
		// UseNumber keeps JSON integers distinct from floats so source IDs retain
		// their exact value when the deterministic cloud unit ID is derived.
		decoder := json.NewDecoder(strings.NewReader(line))
		decoder.UseNumber()
		row := map[string]any{}
		if err := decoder.Decode(&row); err != nil {
			return nil, fmt.Errorf("corpus line is not JSON: %w", err)
		}
		rowID := pyStr(firstTruthy(row["id"], ""))
		if rowID != "" {
			if _, ok := seen[rowID]; !ok {
				seen[rowID] = struct{}{}
				rows = append(rows, row)
			}
		}
		if limit != 0 && len(rows) >= limit {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return rows, nil
}

func pyInt(value any) (int64, error) {
	switch typed := value.(type) {
	case json.Number:
		parsed, err := strconv.ParseInt(typed.String(), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid int %q", typed.String())
		}
		return parsed, nil
	case int:
		return int64(typed), nil
	case int64:
		return typed, nil
	case float64:
		return int64(typed), nil
	case string:
		parsed, err := strconv.ParseInt(pyStrip(typed), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid int %q", typed)
		}
		return parsed, nil
	}
	return 0, fmt.Errorf("invalid int %v", value)
}
