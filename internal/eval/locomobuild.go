// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package eval

// 官方基准重建（`rmem eval --fetch locomo`）：从上游 locomo10.json 确定性重建评测所需的
// corpus.jsonl 与官方题集（cat1-4 全集），并写入自洽清单与来源记录。
//
// 口径：LoCoMo 题集以上游官方为准（cat1-4 = 282+321+96+841 = 1,540，含官方证据 dia_id 与
// 证据文本）；历史便捷档 100/200 不再提供、不再生成。历史打包件（含构建时间戳与依赖未公开
// pool 的逐轮秒偏移）不承诺逐位复刻，因此重建件不冒充历史 pin：
//   - 重建确定性（无时间戳、固定顺序），同输入输出逐字节一致；
//   - 清单 pins = 本次重建哈希，运行器照常校验；来源记录 revision 与上游文件哈希。

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	LoCoMoUpstreamRepoURL  = "https://github.com/snap-research/locomo"
	LoCoMoUpstreamRevision = "3eb6f2c585f5e1699204e3c3bdf7adc5c28cb376"
	LoCoMoUpstreamPath     = "data/locomo10.json"
	LoCoMoUpstreamFileSize = 2805274
	LoCoMoUpstreamSHA256   = "79fa87e90f04081343b8c8debecb80a9a6842b76a7aa537dc9fdf651ea698ff4"
	LoCoMoRawBaseURL       = "https://raw.githubusercontent.com"
	LoCoMoUpstreamRepoSlug = "snap-research/locomo"
)

var (
	reLoCoMoSessionKey = regexp.MustCompile(`^session_([0-9]+)$`)
	reLoCoMoDateTime   = regexp.MustCompile(`^\s*([0-9]{1,2}):([0-9]{2})(?::([0-9]{2}))?\s*([apAP][mM])\s+on\s+([0-9]{1,2})\s+([A-Za-z]+),\s*([0-9]{4})\s*$`)
	reLoCoMoDiaID      = regexp.MustCompile(`^D([0-9]+):([0-9]+)$`)
)

var locomoMonths = map[string]time.Month{
	"january": time.January, "february": time.February, "march": time.March, "april": time.April,
	"may": time.May, "june": time.June, "july": time.July, "august": time.August,
	"september": time.September, "october": time.October, "november": time.November, "december": time.December,
}

type locomoUpstreamTurn struct {
	Speaker string `json:"speaker"`
	DiaID   string `json:"dia_id"`
	Text    string `json:"text"`
}

type locomoUpstreamQA struct {
	Question string          `json:"question"`
	Answer   json.RawMessage `json:"answer"`
	Evidence []string        `json:"evidence"`
	Category json.RawMessage `json:"category"`
}

type locomoUpstreamConversation struct {
	QA           []locomoUpstreamQA         `json:"qa"`
	Conversation map[string]json.RawMessage `json:"conversation"`
	SampleID     string                     `json:"sample_id"`
}

type locomoCorpusRow struct {
	Author     string `json:"author"`
	ID         string `json:"id"`
	Locator    string `json:"locator"`
	OccurredAt string `json:"occurred_at"`
	Session    string `json:"session"`
	Text       string `json:"text"`
}

type locomoEvidenceMessage struct {
	DiaID        string `json:"dia_id"`
	SessionIndex int    `json:"session_index"`
	MessageIndex int    `json:"message_index"`
	Speaker      string `json:"speaker"`
	Text         string `json:"text"`
}

type locomoOfficialQuestion struct {
	ID               string                  `json:"id"`
	SampleID         string                  `json:"sample_id"`
	Question         string                  `json:"question"`
	Category         string                  `json:"category"`
	Answer           string                  `json:"answer"`
	Evidence         []string                `json:"evidence,omitempty"`
	EvidenceTexts    []string                `json:"evidence_texts,omitempty"`
	EvidenceMessages []locomoEvidenceMessage `json:"evidence_messages,omitempty"`
}

// parseLoCoMoDateTime 解析上游会话时间串（如 `1:56 pm on 8 May, 2023`，秒可选）为 UTC 秒。
func parseLoCoMoDateTime(raw string) (int64, error) {
	m := reLoCoMoDateTime.FindStringSubmatch(raw)
	if m == nil {
		return 0, fmt.Errorf("无法解析会话时间 %q", raw)
	}
	hour, _ := strconv.Atoi(m[1])
	minute, _ := strconv.Atoi(m[2])
	second := 0
	if m[3] != "" {
		second, _ = strconv.Atoi(m[3])
	}
	if strings.EqualFold(m[4], "pm") && hour != 12 {
		hour += 12
	}
	if strings.EqualFold(m[4], "am") && hour == 12 {
		hour = 0
	}
	day, _ := strconv.Atoi(m[5])
	month, ok := locomoMonths[strings.ToLower(m[6])]
	if !ok {
		return 0, fmt.Errorf("无法解析会话月份 %q", m[6])
	}
	year, _ := strconv.Atoi(m[7])
	return time.Date(year, month, day, hour, minute, second, 0, time.UTC).Unix(), nil
}

func stringifyLoCoMoValue(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	return strings.Trim(trimmed, `"`)
}

// BuildLoCoMoOfficialBundle 从上游 locomo10.json 确定性重建 corpus.jsonl 与官方题集
// （cat1-4 全集，随附官方证据），并写出自洽 MANIFEST.json；返回清单。
func BuildLoCoMoOfficialBundle(upstreamPath, outDir string) (map[string]any, error) {
	raw, err := os.ReadFile(upstreamPath)
	if err != nil {
		return nil, fmt.Errorf("读取上游语料失败: %w", err)
	}
	var conversations []locomoUpstreamConversation
	if err := json.Unmarshal(raw, &conversations); err != nil {
		return nil, fmt.Errorf("解析上游语料失败: %w", err)
	}
	if len(conversations) == 0 {
		return nil, errors.New("上游语料不含任何会话")
	}
	if err := os.MkdirAll(filepath.Join(outDir, "tiers"), 0o755); err != nil {
		return nil, err
	}

	corpusPath := filepath.Join(outDir, "corpus.jsonl")
	corpusFile, err := os.Create(corpusPath)
	if err != nil {
		return nil, err
	}
	writer := bufio.NewWriterSize(corpusFile, 1<<20)

	type sessionView struct {
		index int
		epoch int64
		turns []locomoUpstreamTurn
	}
	sessionKeySeen := map[string]int{}
	epochUsed := map[int64]int64{}
	var questions []locomoOfficialQuestion
	categoryCounts := map[string]int{}
	evidenceResolved, evidenceUnresolved := 0, 0
	corpusRows := 0

	fail := func(err error) (map[string]any, error) {
		corpusFile.Close()
		return nil, err
	}

	for _, conv := range conversations {
		// 会话按编号升序；时间串解析为 UTC 秒。
		sessions := []sessionView{}
		for key, rawTurns := range conv.Conversation {
			m := reLoCoMoSessionKey.FindStringSubmatch(key)
			if m == nil {
				continue
			}
			index, _ := strconv.Atoi(m[1])
			dateRaw := ""
			if date, ok := conv.Conversation[fmt.Sprintf("session_%d_date_time", index)]; ok {
				_ = json.Unmarshal(date, &dateRaw)
			}
			epoch, err := parseLoCoMoDateTime(dateRaw)
			if err != nil {
				return fail(fmt.Errorf("会话 %s/%s: %w", conv.SampleID, key, err))
			}
			var turns []locomoUpstreamTurn
			if err := json.Unmarshal(rawTurns, &turns); err != nil {
				return fail(fmt.Errorf("会话 %s/%s 轮次解析失败: %w", conv.SampleID, key, err))
			}
			sessions = append(sessions, sessionView{index: index, epoch: epoch, turns: turns})
		}
		sort.Slice(sessions, func(i, j int) bool { return sessions[i].index < sessions[j].index })

		// dia_id → 证据消息（供题集证据解析）。
		evidenceByDia := map[string]locomoEvidenceMessage{}
		for _, session := range sessions {
			for turnIndex, turn := range session.turns {
				if turn.DiaID == "" {
					continue
				}
				evidenceByDia[turn.DiaID] = locomoEvidenceMessage{
					DiaID:        turn.DiaID,
					SessionIndex: session.index,
					MessageIndex: turnIndex + 1,
					Speaker:      strings.TrimSpace(turn.Speaker),
					Text:         turn.Text,
				}
			}
		}

		for _, session := range sessions {
			sessionKey := "s" + strconv.FormatInt(session.epoch, 10)
			if used := sessionKeySeen[sessionKey]; used > 0 {
				// 上游存在同一时间戳的会话（如 conv-43/conv-49 的 session_14）：按出现顺序加后缀。
				sessionKeySeen[sessionKey] = used + 1
				sessionKey = fmt.Sprintf("%s#%d", sessionKey, used)
			} else {
				sessionKeySeen[sessionKey] = 1
			}
			offset := epochUsed[session.epoch]
			for turnIndex, turn := range session.turns {
				row := locomoCorpusRow{
					Author:     strings.TrimSpace(turn.Speaker),
					ID:         turn.DiaID,
					Locator:    fmt.Sprintf("%s#%d", sessionKey, turnIndex),
					OccurredAt: strconv.FormatInt(session.epoch+offset+int64(turnIndex), 10),
					Session:    sessionKey,
					Text:       turn.Text,
				}
				if row.ID == "" {
					row.ID = fmt.Sprintf("%s#%d", sessionKey, turnIndex)
				}
				line, err := json.Marshal(row)
				if err != nil {
					return fail(err)
				}
				if _, err := writer.Write(append(line, '\n')); err != nil {
					return fail(err)
				}
				corpusRows++
			}
			epochUsed[session.epoch] = offset + int64(len(session.turns))
		}

		kept := 0
		for _, qa := range conv.QA {
			category := strings.TrimSpace(stringifyLoCoMoValue(qa.Category))
			if category != "1" && category != "2" && category != "3" && category != "4" {
				continue
			}
			item := locomoOfficialQuestion{
				ID:       fmt.Sprintf("%s#%d", conv.SampleID, kept),
				SampleID: conv.SampleID,
				Question: qa.Question,
				Category: category,
				Answer:   stringifyLoCoMoValue(qa.Answer),
			}
			for _, dia := range qa.Evidence {
				item.Evidence = append(item.Evidence, dia)
				message, ok := evidenceByDia[dia]
				if !ok {
					evidenceUnresolved++
					continue
				}
				evidenceResolved++
				item.EvidenceTexts = append(item.EvidenceTexts, message.Text)
				item.EvidenceMessages = append(item.EvidenceMessages, message)
			}
			questions = append(questions, item)
			categoryCounts[category]++
			kept++
		}
	}
	if err := writer.Flush(); err != nil {
		return fail(err)
	}
	if err := corpusFile.Close(); err != nil {
		return nil, err
	}

	questionsPath := filepath.Join(outDir, "tiers", "questions-1540.json")
	questionsDoc := map[string]any{
		"_meta": map[string]any{
			"version":        "1",
			"generated_by":   "rmem eval --fetch locomo",
			"official_basis": true,
			"upstream": map[string]any{
				"repo":     LoCoMoUpstreamRepoURL,
				"revision": LoCoMoUpstreamRevision,
				"file":     LoCoMoUpstreamPath,
				"sha256":   LoCoMoUpstreamSHA256,
			},
			"corpus":    map[string]any{"rows": corpusRows},
			"questions": map[string]any{"count": len(questions), "categories": categoryCounts, "evidence_resolved": evidenceResolved, "evidence_unresolved": evidenceUnresolved},
			"note":      "LoCoMo 题集以上游官方为准（cat1-4 = 282+321+96+841）；历史便捷档 100/200 不再提供、不再生成。",
		},
		"questions": questions,
	}
	questionBytes, err := json.MarshalIndent(questionsDoc, "", " ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(questionsPath, append(questionBytes, '\n'), 0o644); err != nil {
		return nil, err
	}

	corpusSHA, err := sha256FileHex(corpusPath)
	if err != nil {
		return nil, err
	}
	questionsSHA, err := sha256FileHex(questionsPath)
	if err != nil {
		return nil, err
	}
	manifest := map[string]any{
		"version":  "1",
		"protocol": "local-qa-judge (locomo-frozen-2026-10-11)",
		"source": map[string]string{
			"corpus":         "LoCoMo10 standard 5,882-turn benchmark snapshot (upstream official, revision " + LoCoMoUpstreamRevision[:12] + ")",
			"questions-1540": "LoCoMo official cat1-4 question set (282+321+96+841) with official evidence, rebuilt locally from upstream",
		},
		"corpus": map[string]any{"path": "corpus.jsonl", "sha256": corpusSHA, "rows": corpusRows},
		"tiers": map[string]any{
			Tier1540: map[string]any{
				"file":   "tiers/questions-1540.json",
				"sha256": questionsSHA,
				"count":  len(questions),
				"note":   "LoCoMo 官方 cat1-4 全集（随附官方证据文本）；100/200 便捷档不再提供",
			},
		},
		"upstream": map[string]any{
			"repo":     LoCoMoUpstreamRepoURL,
			"revision": LoCoMoUpstreamRevision,
			"file":     LoCoMoUpstreamPath,
			"sha256":   LoCoMoUpstreamSHA256,
		},
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", " ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(outDir, "MANIFEST.json"), append(manifestBytes, '\n'), 0o644); err != nil {
		return nil, err
	}
	return manifest, nil
}

// UpstreamLoCoMoNotice 返回下载前展示给用户的上游语料说明。
func UpstreamLoCoMoNotice() string {
	return fmt.Sprintf(`上游语料说明（下载前请确认）：
  数据集：LoCoMo10（%s）
  数据许可：%s
  许可全文：%s
  取数只发生在本机：从上游下载 10 组会话标注（约 2.8 MB）并在本地重建官方题集，不触碰你的本地记忆数据。
`, LoCoMoUpstreamRepoURL, LoCoMoUpstreamLicense, LoCoMoUpstreamLicenseURL)
}

// ResolveLoCoMoFetchTarget 决定 `--fetch locomo` 的目标目录（LoCoMo 数据位于数据目录根）。
func ResolveLoCoMoFetchTarget(explicitDataDir string) string {
	if explicit := strings.TrimSpace(explicitDataDir); explicit != "" {
		return expandUser(explicit)
	}
	if env := strings.TrimSpace(os.Getenv(DataDirEnv)); env != "" {
		return expandUser(env)
	}
	if dir, err := ResolveDataDir(""); err == nil {
		return dir
	}
	if xdgData := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); xdgData != "" {
		return filepath.Join(expandUser(xdgData), "rolling-memory", DataDirName)
	}
	if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
		return filepath.Join(localAppData, "rolling-memory", DataDirName)
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".local", "share", "rolling-memory", DataDirName)
	}
	return ""
}

// FetchLoCoMoOptions 控制一次 LoCoMo 上游取数。
type FetchLoCoMoOptions struct {
	TargetDir     string
	Revision      string
	AcceptLicense bool
	BaseURL       string // 默认 https://raw.githubusercontent.com
	HTTPClient    *http.Client
	Log           func(format string, args ...any)

	// 测试注入口（零值 = 官方常量）。
	fileSize int64
	fileSHA  string
}

// FetchLoCoMoReport 一次 LoCoMo 上游取数的完整回执。
type FetchLoCoMoReport struct {
	Repo           string               `json:"repo"`
	RepoURL        string               `json:"repo_url"`
	Revision       string               `json:"revision"`
	TargetDir      string               `json:"target_dir"`
	License        string               `json:"license"`
	LicenseURL     string               `json:"license_url"`
	Files          []FetchATMFileReport `json:"files"`
	Rebuilt        bool                 `json:"rebuilt"`
	CorpusSHA256   string               `json:"corpus_sha256"`
	TierSHA256     map[string]string    `json:"tier_sha256"`
	ProvenancePath string               `json:"provenance_path"`
	ManifestPath   string               `json:"manifest_path"`
}

// FetchLoCoMoUpstream 下载上游 locomo10.json、本地重建官方题集并写入自洽清单。
func FetchLoCoMoUpstream(ctx context.Context, opts FetchLoCoMoOptions) (*FetchLoCoMoReport, error) {
	target := expandUser(strings.TrimSpace(opts.TargetDir))
	if target == "" {
		return nil, errors.New("取数目录为空：请用 --eval-data <dir> 指定，或设置 ROLLING_MEMORY_EVAL_DATA")
	}
	revision := strings.TrimSpace(opts.Revision)
	if revision == "" {
		revision = LoCoMoUpstreamRevision
	}
	base := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if base == "" {
		base = LoCoMoRawBaseURL
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: upstreamHTTPTimeout}
	}
	fileSize := opts.fileSize
	if fileSize == 0 {
		fileSize = LoCoMoUpstreamFileSize
	}
	fileSHA := strings.TrimSpace(opts.fileSHA)
	if fileSHA == "" {
		fileSHA = LoCoMoUpstreamSHA256
	}
	logf := func(format string, args ...any) {
		if opts.Log != nil {
			opts.Log(format, args...)
		}
	}
	report := &FetchLoCoMoReport{
		Repo:       LoCoMoUpstreamRepoSlug,
		RepoURL:    LoCoMoUpstreamRepoURL,
		Revision:   revision,
		TargetDir:  target,
		License:    LoCoMoUpstreamLicense,
		LicenseURL: LoCoMoUpstreamLicenseURL,
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return nil, fmt.Errorf("无法创建取数目录 %s: %w", target, err)
	}
	upstreamPath := filepath.Join(target, "locomo10.json")
	if fileMatchesDigest(upstreamPath, fileSize, fileSHA) {
		logf("上游语料已就位（哈希一致，跳过下载）：locomo10.json")
		report.Files = append(report.Files, FetchATMFileReport{Path: LoCoMoUpstreamPath, Local: "locomo10.json", Bytes: fileSize, SHA256: fileSHA})
	} else {
		if !opts.AcceptLicense {
			return nil, ErrUpstreamLicenseNotAccepted
		}
		logf("下载 locomo10.json（%s）...", humanBytes(fileSize))
		url := fmt.Sprintf("%s/%s/%s/%s", base, LoCoMoUpstreamRepoSlug, revision, LoCoMoUpstreamPath)
		if err := downloadVerifiedURL(ctx, client, url, upstreamPath, fileSize, fileSHA); err != nil {
			return nil, err
		}
		report.Files = append(report.Files, FetchATMFileReport{Path: LoCoMoUpstreamPath, Local: "locomo10.json", Bytes: fileSize, SHA256: fileSHA, Downloaded: true})
	}

	logf("本地重建官方题集（上游 locomo10.json → corpus.jsonl + tiers/questions-1540.json）...")
	tmpDir, err := os.MkdirTemp(target, ".rebuild-")
	if err != nil {
		return nil, fmt.Errorf("无法创建重建临时目录: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	manifest, err := BuildLoCoMoOfficialBundle(upstreamPath, tmpDir)
	if err != nil {
		return nil, fmt.Errorf("重建官方题集失败: %w", err)
	}
	corpusSHA, tierSHAs, err := manifestDigests(manifest)
	if err != nil {
		return nil, err
	}
	keepManifest := existingManifestMatchesPins(target, corpusSHA, tierSHAs)
	if err := installRebuiltArtifacts(target, tmpDir, []string{"corpus.jsonl", "MANIFEST.json"}, keepManifest); err != nil {
		return nil, err
	}
	if keepManifest {
		logf("既有 MANIFEST.json 与本轮重建同 pin，予以保留")
	}
	provPath, err := writeLoCoMoProvenance(target, report, corpusSHA, tierSHAs)
	if err != nil {
		return nil, err
	}
	logf("完成：官方题集已重建并就位 → %s", target)
	return &FetchLoCoMoReport{
		Repo:           report.Repo,
		RepoURL:        report.RepoURL,
		Revision:       report.Revision,
		TargetDir:      target,
		License:        report.License,
		LicenseURL:     report.LicenseURL,
		Files:          report.Files,
		Rebuilt:        true,
		CorpusSHA256:   corpusSHA,
		TierSHA256:     tierSHAs,
		ProvenancePath: provPath,
		ManifestPath:   filepath.Join(target, "MANIFEST.json"),
	}, nil
}

// writeLoCoMoProvenance 记录本次取数的来源、revision、上游文件哈希与重建结论。
func writeLoCoMoProvenance(target string, report *FetchLoCoMoReport, corpusSHA string, tierSHAs map[string]string) (string, error) {
	record := map[string]any{
		"dataset":     report.Repo,
		"repo_url":    report.RepoURL,
		"revision":    report.Revision,
		"license":     report.License,
		"license_url": report.LicenseURL,
		"fetched_at":  time.Now().Format(time.RFC3339),
		"files":       report.Files,
		"rebuild": map[string]any{
			"official_basis": true,
			"corpus_sha256":  corpusSHA,
			"tier_sha256":    tierSHAs,
		},
		"note": "obtained from the upstream repository on this machine and rebuilt locally; corpora are not redistributed with the project; historical convenience tiers 100/200 are retired",
	}
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(target, "UPSTREAM-PROVENANCE.json")
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		return "", fmt.Errorf("写入来源记录失败: %w", err)
	}
	return path, nil
}
