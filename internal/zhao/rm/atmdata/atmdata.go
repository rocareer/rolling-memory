// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package atmdata

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CorpusDoc 是 ATM-Bench 语料单元（文本按官方 SGM 记忆格式化后的形态）。
type CorpusDoc struct {
	ID         string
	Text       string
	OccurredAt int64
}

// ATMItem 与官方 corpus.jsonl 字段逐字对齐。
type ATMItem struct {
	ID         string   `json:"id"`
	Modality   string   `json:"m"`
	Timestamp  string   `json:"ts"`
	OccurredAt int64    `json:"occ"`
	Location   string   `json:"loc"`
	Subject    string   `json:"subj"`
	ShortCap   string   `json:"sc"`
	Caption    string   `json:"cap"`
	OCRText    string   `json:"ocr"`
	Tags       []string `json:"tags"`
	Summary    string   `json:"sum"`
	Detail     string   `json:"det"`
}

// ATMFullText 复刻官方语料侧格式化（邮件与多模态两条路径）。
func ATMFullText(item ATMItem) string {
	if strings.EqualFold(strings.TrimSpace(item.Modality), "email") {
		return strings.Join([]string{
			"ID: " + item.ID,
			"Timestamp: " + item.Timestamp,
			"Subject: " + item.Subject,
			"Summary: " + item.Summary,
			"Detail: " + item.Detail,
		}, "\n")
	}
	return strings.Join([]string{
		"ID: " + item.ID,
		"Type: " + item.Modality,
		"Timestamp: " + item.Timestamp,
		"Location: " + item.Location,
		"Short Caption: " + item.ShortCap,
		"Caption: " + item.Caption,
		"OCR: " + item.OCRText,
		"Tags: " + strings.Join(item.Tags, ", "),
	}, "\n")
}

// LoadATMCorpus 读取官方 ATM-Bench SGM 语料（gz JSONL）。limit<=0 表示全量。
func LoadATMCorpus(dataRoot string, limit int) ([]CorpusDoc, error) {
	path := filepath.Join(dataRoot, "atmbench", "corpus.jsonl.gz")
	handle, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	zr, err := gzip.NewReader(handle)
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	docs := make([]CorpusDoc, 0, 12000)
	seen := make(map[string]struct{}, 12000)
	scanner := bufio.NewScanner(zr)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var item ATMItem
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			return nil, fmt.Errorf("ATM 语料行非法 JSON：%w", err)
		}
		if item.ID == "" {
			continue
		}
		if _, ok := seen[item.ID]; ok {
			continue
		}
		seen[item.ID] = struct{}{}
		docs = append(docs, CorpusDoc{ID: item.ID, Text: ATMFullText(item), OccurredAt: item.OccurredAt})
		if limit > 0 && len(docs) >= limit {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return docs, nil
}

// ATMQuestion 与官方题集字段逐字对齐。
type ATMQuestion struct {
	ID          string   `json:"id"`
	Question    string   `json:"question"`
	Answer      string   `json:"answer"`
	QType       string   `json:"qtype"`
	EvidenceIDs []string `json:"evidence_ids"`
}

// LoadATMQuestions 读取题集（裸 JSON 数组）。limit<=0 表示全量。
func LoadATMQuestions(dataRoot, tier string, limit int) ([]ATMQuestion, error) {
	file := filepath.Join(dataRoot, "atmbench", "tiers", "questions-"+tier+".json")
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var questions []ATMQuestion
	if err := json.Unmarshal(raw, &questions); err != nil {
		return nil, fmt.Errorf("解析题集 %s 失败：%w", file, err)
	}
	if limit > 0 && len(questions) > limit {
		questions = questions[:limit]
	}
	return questions, nil
}
