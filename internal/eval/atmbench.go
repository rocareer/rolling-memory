package eval

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/langpack"
	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
)

const (
	// SuiteLoCoMo 是第一套默认评测体系（LoCoMo ACL 2024 长对话记忆基准）。
	SuiteLoCoMo = "locomo"
	// SuiteATM 是第二套官方评测体系（剑桥大学 ATM-Bench 多模态个人指称记忆基准）。
	SuiteATM = "atm"

	// ATMScope 是默认开发租户的 ATM-Bench 云端专属评测作用域，与真实用户数据物理隔离。
	ATMScope = "u10-mem-eval-atmbench"

	// TierATMHard 是 ATM-Bench-Hard 31 道高难度跨年/多跳/冲突/拒答题档位。
	TierATMHard = "hard"
	// TierATM1013 是 ATM-Bench 主榜 1,013 题全量档位。
	TierATM1013 = "1013"

	// ATMProtocolText 是 ATM-Bench 官方评测协议标识。
	ATMProtocolText = "atmbench-official-v1"

	// ATMSystemPrompt 是 ATM-Bench 官方 MMRag 答题系统提示词（逐字一致，不可篡改）。
	ATMSystemPrompt = "You are a memory QA assistant. Use ONLY the provided evidence to answer. " +
		"If the evidence is insufficient, answer 'Unknown'. Respond with only the answer. " +
		"If the question asks to recall or list items (photos/emails/videos), respond with the corresponding evidence IDs only, comma-separated, with no extra text."

	// ATMEvidencePromptTPL 是 ATM-Bench 官方 MMRag 答题 user prompt 模板（逐字一致，不可篡改）。
	ATMEvidencePromptTPL = "Question: {question}\n\nEvidence:\n{evidence}\n\nProvide the answer based solely on the evidence."

	// ATMJudgePromptTPL 是 ATM-Bench 官方 LLM_JUDGE_PROMPT 评判提示词模板（逐字一致，不可篡改）。
	ATMJudgePromptTPL = "You are an evaluator, and you are given a task to evaluate a model predictions " +
		"with a given question. Let's follow the instructions step by step to make a " +
		"judgement.  " +
		"1. As the first step, you need to check whether the prediction was really " +
		"answering the question.  " +
		"2. If the model prediction does provide a meaningful answer, judge whether " +
		"the model Prediction matches the ground truth answer by reasoning according " +
		"to the following steps:  " +
		"2.1: Always assume the ground truth is correct.  " +
		"2.2: Pay attention to theses special cases:  " +
		"a. If the ground truth answer contains numbers, the value of \"accuracy\" " +
		"is true only if numbers in ground truth and numbers in model predictions " +
		"match very well; in case of math questions, \"accuracy\" is true only if " +
		"the numbers in model predictions EXACTLY matches the numbers in ground truth;  " +
		"b. If the ground truth answer contains time, and/or time range, \"accuracy\" " +
		"is \"true\" only if if times and time ranges in ground truth and model " +
		"predictions match very well.  " +
		"c. If the ground truth answer contains a set of objects, \"accuracy\" is " +
		"\"true\" if the model prediction covers most of the objects in the ground truth; " +
		"however, \"accuracy\" if \"false\" if the  model prediction has a lot of objects " +
		"that are not in the ground truth.  " +
		"d. If the ground truth is something similar to \"I don't know\", \"accuracy\" " +
		"is \"true\" only if the model prediction also implies the similar thing.  " +
		"2.3: Even if the prediction statement is reasonable, if it conflicts with or does " +
		"not match the ground truth, \"accuracy\" should be \"false\".  " +
		"2.4. \"Accuracy\" is true if the ground truth information is covered by the prediction. " +
		"The prediction is allowed to provide more information but should not be against " +
		"the ground truth. If it is hard to  decide whether the prediction matches ground " +
		"truth, \"accuracy\" should be \"false\".  " +
		"Think step by step following the instructions above, and then make a judgment. " +
		"Respond with only a single JSON blob with an \"explanation\" field that has your " +
		"short(less than 100 word) reasoning  steps and an \"accuracy\" field which is " +
		"\"true\" or \"false\".  " +
		"Question: {{question}}  " +
		"Ground truth: {{answer}}  " +
		"Prediction: {{prediction}}"
)

// ATM-Bench 官方协议 SHA-1 钉（运行期自检 fail-closed）。
const (
	PinATMSystemSHA1         = "a6b5c7dc183ecd842460536037e83bfdd7ea9c33"
	PinATMEvidencePromptSHA1 = "d10f252e6a131025696100551493932d8377b24c"
	PinATMJudgeTPLSHA1       = "324ac8a5a9f3b8b3a1a87be370bf9dfbf8da91f1"
)

// VerifyATMProtocol 校验 ATM-Bench 官方提示词常量未被篡改。
func VerifyATMProtocol() error {
	if got := Sha1Hex(ATMSystemPrompt); got != PinATMSystemSHA1 {
		return fmt.Errorf("ATM-Bench 答题 system sha1 不符：期望 %s，实际 %s", PinATMSystemSHA1, got)
	}
	if got := Sha1Hex(ATMEvidencePromptTPL); got != PinATMEvidencePromptSHA1 {
		return fmt.Errorf("ATM-Bench 答题 user 模板 sha1 不符：期望 %s，实际 %s", PinATMEvidencePromptSHA1, got)
	}
	if got := Sha1Hex(ATMJudgePromptTPL); got != PinATMJudgeTPLSHA1 {
		return fmt.Errorf("ATM-Bench 评分模板 sha1 不符：期望 %s，实际 %s", PinATMJudgeTPLSHA1, got)
	}
	return nil
}

// ATMSGMItem 是 ATM-Bench 单条个人多模态记忆条目（邮件 / 照片 / 视频）。
type ATMSGMItem struct {
	ItemID     string   `json:"id"`
	Modality   string   `json:"m"`
	Timestamp  string   `json:"ts,omitempty"`
	OccurredAt int64    `json:"occ,omitempty"`
	Location   string   `json:"loc,omitempty"`
	City       string   `json:"city,omitempty"`
	ShortCap   string   `json:"sc,omitempty"`
	Caption    string   `json:"cap,omitempty"`
	OCRText    string   `json:"ocr,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	Subject    string   `json:"subj,omitempty"`
	Summary    string   `json:"sum,omitempty"`
	Detail     string   `json:"det,omitempty"`
	FullText   string   `json:"-"`
	TraceHash  string   `json:"-"`
	CorpusIdx  int      `json:"-"`
}

// ATMQAItem 是 ATM-Bench 评测问题条目。
type ATMQAItem struct {
	ID          string   `json:"id"`
	Question    string   `json:"question"`
	Answer      string   `json:"answer"`
	QType       string   `json:"qtype"`
	Notes       string   `json:"notes,omitempty"`
	EvidenceIDs []string `json:"evidence_ids"`
}

var (
	reATMSubjectLine = regexp.MustCompile(`(?m)^Subject:\s*(.+)$`)
	reATMMediaID     = regexp.MustCompile(`\b(\d{8}_\d{6})\b`)
	reATMEmailID     = regexp.MustCompile(`(?i)\b(email\d+)\b`)
	reATMListSep     = regexp.MustCompile(`[,;\n/]+`)
	reATMNumberToken = regexp.MustCompile(`[-+]?\d+(?:\.\d+)?`)
)

// ParseATMTimestamp 将 ATM-Bench 时间戳解析为 UTC Unix 秒。
func ParseATMTimestamp(raw string) int64 {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05-07:00",
		"2006-01-02 15:04:05+00:00",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05Z07:00",
		"2006-01-02T15:04:05",
		"2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Unix()
		}
	}
	if len(s) >= 19 {
		if t, err := time.Parse("2006-01-02 15:04:05", s[:19]); err == nil {
			return t.UTC().Unix()
		}
	}
	if len(s) >= 10 {
		if t, err := time.Parse("2006-01-02", s[:10]); err == nil {
			return t.UTC().Unix()
		}
	}
	return 0
}

// HydrateATMSGMItem 按 ATM-Bench 官方格式（format_email_text / format_media_text）填充 FullText 与 TraceHash。
func HydrateATMSGMItem(scope string, it *ATMSGMItem) {
	if scope == "" {
		scope = ATMScope
	}
	if it.OccurredAt <= 0 && it.Timestamp != "" {
		it.OccurredAt = ParseATMTimestamp(it.Timestamp)
	}
	if it.Modality == "email" {
		if it.Subject == "" && it.Detail != "" {
			if m := reATMSubjectLine.FindStringSubmatch(it.Detail); len(m) == 2 {
				it.Subject = strings.TrimSpace(m[1])
			}
		}
		it.FullText = strings.Join([]string{
			"ID: " + it.ItemID,
			"Timestamp: " + it.Timestamp,
			"Summary: " + it.Summary,
			"Detail: " + it.Detail,
		}, "\n")
	} else {
		it.FullText = strings.Join([]string{
			"ID: " + it.ItemID,
			"Type: " + it.Modality,
			"Timestamp: " + it.Timestamp,
			"Location: " + it.Location,
			"Short Caption: " + it.ShortCap,
			"Caption: " + it.Caption,
			"OCR: " + it.OCRText,
			"Tags: " + strings.Join(it.Tags, ", "),
		}, "\n")
	}
	it.TraceHash = ids.TraceHash(scope, it.ItemID, 1, it.FullText)
}

// ResolveATMDataDir 解析随包 ATM-Bench 数据目录（默认 evaldata/atmbench）。
func ResolveATMDataDir(explicit string) (string, error) {
	candidates := []string{}
	if strings.TrimSpace(explicit) != "" {
		exp := expandUser(explicit)
		candidates = append(candidates, exp, filepath.Join(exp, "atmbench"))
	}
	if env := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_ATM_DATA")); env != "" {
		candidates = append(candidates, expandUser(env))
	}
	if baseDir, err := ResolveDataDir(""); err == nil {
		candidates = append(candidates, filepath.Join(baseDir, "atmbench"))
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), DataDirName, "atmbench"))
	}
	if cwd, err := os.Getwd(); err == nil {
		// 包内 `go test` 的 cwd 是包目录（如 internal/eval），真实语料在模块根 evaldata/，
		// 因此按祖先链逐级探测；只探 cwd 与 cwd/.. 会让随包 ATM 集成测试默认静默跳过。
		for depth := 0; depth <= 3; depth++ {
			base := cwd
			for step := 0; step < depth; step++ {
				base = filepath.Dir(base)
			}
			candidates = append(candidates, filepath.Join(base, DataDirName, "atmbench"))
		}
	}
	// 用户数据目录候选：`rmem eval --fetch atm` 默认取数到 <数据目录>/atmbench，
	// 这里与 ResolveDataDir 的用户目录候选保持同源，保证新装用户无需额外参数可被发现。
	if xdgData := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); xdgData != "" {
		candidates = append(candidates, filepath.Join(expandUser(xdgData), "rolling-memory", DataDirName, "atmbench"))
	}
	if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
		candidates = append(candidates, filepath.Join(localAppData, "rolling-memory", DataDirName, "atmbench"))
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		candidates = append(candidates, filepath.Join(home, ".local", "share", "rolling-memory", DataDirName, "atmbench"))
	}
	candidates = append(candidates, "/tmp/atmbench_workspace/data")
	for _, cand := range candidates {
		if cand == "" {
			continue
		}
		if info, err := os.Stat(filepath.Join(cand, "MANIFEST.json")); err == nil && !info.IsDir() {
			return cand, nil
		}
		if info, err := os.Stat(filepath.Join(cand, "raw_memory", "email", "emails.json")); err == nil && !info.IsDir() {
			return cand, nil
		}
	}
	return "", fmt.Errorf("找不到 ATM-Bench 评测数据目录（evaldata/atmbench/MANIFEST.json）：可运行 `rmem eval --fetch atm` 从上游获取并本地重建（约 35 MB），或用 --eval-data <dir> 指向自备数据目录")
}

// LoadATMSGMItems 从随包 corpus.jsonl.gz（或原始 raw_memory/processed_memory 目录）加载全部 11,034 条 SGM 记忆。
func LoadATMSGMItems(dataDir, scope string, corpusLimit int) ([]*ATMSGMItem, map[string]*ATMSGMItem, map[string]*ATMSGMItem, error) {
	if scope == "" {
		scope = ATMScope
	}
	gzPath := filepath.Join(dataDir, "corpus.jsonl.gz")
	if info, err := os.Stat(gzPath); err == nil && !info.IsDir() {
		f, err := os.Open(gzPath)
		if err != nil {
			return nil, nil, nil, err
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, nil, nil, err
		}
		defer gz.Close()
		items := make([]*ATMSGMItem, 0, 11034)
		byID := make(map[string]*ATMSGMItem, 11034)
		byHash := make(map[string]*ATMSGMItem, 11034)
		scanner := bufio.NewScanner(gz)
		scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var it ATMSGMItem
			if err := json.Unmarshal([]byte(line), &it); err != nil {
				return nil, nil, nil, err
			}
			if it.ItemID == "" {
				continue
			}
			HydrateATMSGMItem(scope, &it)
			it.CorpusIdx = len(items)
			ptr := &it
			items = append(items, ptr)
			byID[ptr.ItemID] = ptr
			byHash[ptr.TraceHash] = ptr
			if corpusLimit > 0 && len(items) >= corpusLimit {
				break
			}
		}
		if err := scanner.Err(); err != nil {
			return nil, nil, nil, err
		}
		return items, byID, byHash, nil
	}

	// 回退：从原始 ATM-Bench 3 个 JSON 文件加载
	readList := func(rel string) ([]map[string]any, error) {
		raw, err := os.ReadFile(filepath.Join(dataDir, rel))
		if err != nil {
			return nil, err
		}
		var out []map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, err
		}
		return out, nil
	}
	emails, err := readList("raw_memory/email/emails.json")
	if err != nil {
		return nil, nil, nil, err
	}
	images, err := readList("processed_memory/image_batch_results.json")
	if err != nil {
		return nil, nil, nil, err
	}
	videos, err := readList("processed_memory/video_batch_results.json")
	if err != nil {
		return nil, nil, nil, err
	}

	total := len(emails) + len(images) + len(videos)
	items := make([]*ATMSGMItem, 0, total)
	byID := make(map[string]*ATMSGMItem, total)
	byHash := make(map[string]*ATMSGMItem, total)

	asStr := func(v any) string {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
		return ""
	}

	for _, e := range emails {
		id := asStr(e["id"])
		if id == "" {
			continue
		}
		it := &ATMSGMItem{
			ItemID:    id,
			Modality:  "email",
			Timestamp: asStr(e["timestamp"]),
			Subject:   asStr(e["subject"]),
			Summary:   asStr(e["short_summary"]),
			Detail:    asStr(e["detail"]),
		}
		HydrateATMSGMItem(scope, it)
		items = append(items, it)
		byID[id] = it
		byHash[it.TraceHash] = it
		if corpusLimit > 0 && len(items) >= corpusLimit {
			return items, byID, byHash, nil
		}
	}

	parseMedia := func(entries []map[string]any, pathKey, modality string) {
		for _, m := range entries {
			if corpusLimit > 0 && len(items) >= corpusLimit {
				return
			}
			rawPath := asStr(m[pathKey])
			if rawPath == "" {
				continue
			}
			base := filepath.Base(rawPath)
			id := strings.TrimSuffix(base, filepath.Ext(base))
			if id == "" {
				continue
			}
			var tags []string
			if rawTags, ok := m["tags"].([]any); ok {
				for _, rt := range rawTags {
					if t := asStr(rt); t != "" {
						tags = append(tags, t)
					}
				}
			}
			it := &ATMSGMItem{
				ItemID:    id,
				Modality:  modality,
				Timestamp: asStr(m["timestamp"]),
				Location:  asStr(m["location_name"]),
				City:      asStr(m["city"]),
				ShortCap:  asStr(m["short_caption"]),
				Caption:   asStr(m["caption"]),
				OCRText:   asStr(m["ocr_text"]),
				Tags:      tags,
			}
			HydrateATMSGMItem(scope, it)
			items = append(items, it)
			byID[id] = it
			byHash[it.TraceHash] = it
		}
	}
	parseMedia(images, "image_path", "image")
	parseMedia(videos, "video_path", "video")

	return items, byID, byHash, nil
}

// ErrATMTierRetired 表示请求了已废止的自造 100 题前缀档。
// 官方 ATM-Bench 只有 Main（1,013 题）与 Hard（31 题）两套题集，外加 NIAH 池（池大小 25/50/100/200），
// 自造的 `questions-100.json`（Main 前 100 题前缀）已于 2026-11-09 删除，禁止静默降级为 hard。
var ErrATMTierRetired = errors.New("ATM-Bench 官方题集只有 Main-1013 与 Hard-31；自造的 100 题前缀档已于 2026-11-09 废止，请改用 --tier 1013 或 --tier hard")

// NormalizeATMTier 归一化 ATM-Bench 官方档位名（hard | 1013）。
// 已废止的 100 题前缀档返回空串，调用方必须 fail-closed 拒跑。
func NormalizeATMTier(tier string) string {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "hard", "31", "atm-hard":
		return TierATMHard
	case "1013", "full", "main", "all":
		return TierATM1013
	case "100", "main-100", "main100", "atm-100", "atm100":
		return ""
	default:
		return TierATMHard
	}
}

// LoadATMQAItems 加载指定官方档位（hard | 1013）的 ATM-Bench 问题集。
func LoadATMQAItems(dataDir, tier string, offset, limit int) ([]ATMQAItem, error) {
	normTier := NormalizeATMTier(tier)
	if normTier == "" {
		return nil, ErrATMTierRetired
	}
	candidates := []string{
		filepath.Join(dataDir, "tiers", "questions-"+normTier+".json"),
	}
	if normTier == TierATMHard {
		candidates = append(candidates, filepath.Join(dataDir, "atm-bench", "atm-bench-hard.json"))
	} else {
		candidates = append(candidates, filepath.Join(dataDir, "atm-bench", "atm-bench.json"))
	}
	var raw []byte
	var err error
	for _, p := range candidates {
		raw, err = os.ReadFile(p)
		if err == nil {
			break
		}
	}
	if err != nil {
		return nil, fmt.Errorf("无法读取 ATM-Bench 题集 (%s): %w", normTier, err)
	}

	var list []ATMQAItem
	if err := json.Unmarshal(raw, &list); err != nil {
		var wrapper struct {
			Questions []ATMQAItem `json:"questions"`
			QAs       []ATMQAItem `json:"qas"`
		}
		if wErr := json.Unmarshal(raw, &wrapper); wErr != nil {
			return nil, err
		}
		if len(wrapper.Questions) > 0 {
			list = wrapper.Questions
		} else {
			list = wrapper.QAs
		}
	}
	for i := range list {
		list[i].QType = NormalizeATMQType(list[i].QType, list[i].Answer)
	}
	if offset > 0 {
		if offset >= len(list) {
			return []ATMQAItem{}, nil
		}
		list = list[offset:]
	}
	if limit > 0 && limit < len(list) {
		list = list[:limit]
	}
	return list, nil
}

// NormalizeATMQType 归一化 ATM-Bench 题型分类（list_recall | number | open_end）。
func NormalizeATMQType(rawQType, answer string) string {
	v := strings.ToLower(strings.TrimSpace(rawQType))
	switch v {
	case "list_recall", "list-recall", "list", "listrecall", "1":
		return "list_recall"
	case "number", "num", "numeric", "2":
		return "number"
	case "open_end", "open-end", "open", "openended", "open_ended", "3", "4":
		return "open_end"
	}
	if len(extractATMEvidenceIDsFromText(answer)) > 0 {
		return "list_recall"
	}
	return "open_end"
}

// atmSessionID derives the `session_id` an SGM item is written under.
//
// The remote gateway takes a trace's session from its locator prefix
// (`ExtractSessionKeyFromLocator`) and builds episode
// containers per session, so this value *is* the episode boundary of the memory
// structure. Writing one session per item yields one episode per item.
//
// The corpus is therefore sessionised on its own structured fields, following the
// AML shape where one session carries a series of related turns:
//   - media: one calendar day at one place (city, falling back to the first
//     location component) — a day out, a conference day, a venue visit;
//   - mail: one calendar day — a day's correspondence.
//
// The key is content-derived (not sequence-derived), so re-ingesting the same
// corpus targets the same sessions and the same locators.
// ATMSessionID 暴露 ATM 语料的会话分桶口径（同日邮件 / 同日同城的媒体），
// 供外部记忆系统对照评测复用同一会话边界；与内部 atmSessionID 同源。
func ATMSessionID(it *ATMSGMItem) string { return atmSessionID(it) }

func atmSessionID(it *ATMSGMItem) string {
	if it == nil {
		return "ev:undated:noloc"
	}
	day := "undated"
	if it.OccurredAt > 0 {
		day = time.Unix(it.OccurredAt, 0).UTC().Format("2006-01-02")
	}
	if strings.EqualFold(strings.TrimSpace(it.Modality), "email") {
		return "mail:" + day
	}
	place := strings.TrimSpace(strings.SplitN(it.City, ",", 2)[0])
	if place == "" {
		place = strings.TrimSpace(strings.SplitN(it.Location, ",", 2)[0])
	}
	if place == "" {
		return "ev:" + day + ":noloc"
	}
	return "ev:" + day + ":" + ids.SHA256Hex(strings.ToLower(place))[:10]
}

// atmFullTextLineIndex 返回 FullText 中首个以前缀开头的 1-based 行号（无匹配返回 0）。
// 纯形式定位：前缀是 HydrateATMSGMItem 写入的固定字段名，不含任何语义推断。
func atmFullTextLineIndex(fullText, prefix string) int {
	if fullText == "" || prefix == "" {
		return 0
	}
	for index, line := range strings.Split(fullText, "\n") {
		if strings.HasPrefix(line, prefix) {
			return index + 1
		}
	}
	return 0
}

// atmHighResolutionSubUnits 把 SGM 条目中的高价值结构化字段切成自由块切片：
//   - 邮件：Subject+Summary 标题块 + Detail 的多段（空行分段）正文块；
//   - 照片/视频：Location+City+ShortCap(Caption) 场域块 + OCRText+Tags 文本块。
//
// 每块的行号锚定父文档 FullText 的真实行位置（ID/Timestamp/Summary/Detail 固定头部
// 与媒体字段行序），绝不从 1 起合成，也不越出父单元行区间。
// 全部为纯形式切分（固定字段名与空行边界，无语义词表、无字符数硬截断）。
// 这些块并入同一父单元的句帧，不另发子 UnitID；其 BlockID/LineStart/LineEnd 仅端侧
// 使用（本地金库与高分辨率交付定位），不进入上行脉冲。
func atmHighResolutionSubUnits(item *ATMSGMItem) []ramesdk.SubUnit {
	if item == nil {
		return nil
	}
	var subs []ramesdk.SubUnit
	add := func(kind, text string, lineStart, lineEnd int) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		subs = append(subs, ramesdk.SubUnit{
			Kind:      kind,
			ID:        item.ItemID + "#" + kind,
			BlockID:   item.ItemID + "#" + kind,
			Text:      text,
			FileParse: text,
			LineStart: lineStart,
			LineEnd:   lineEnd,
		})
	}
	if strings.EqualFold(strings.TrimSpace(item.Modality), "email") {
		var header []string
		if v := strings.TrimSpace(item.Subject); v != "" {
			header = append(header, "Subject: "+v)
		}
		if v := strings.TrimSpace(item.Summary); v != "" {
			header = append(header, "Summary: "+v)
		}
		// Subject 是条目元数据（不进入 FullText）；标题块锚定 FullText 中真实存在的 Summary 行。
		headerLine := atmFullTextLineIndex(item.FullText, "Summary: ")
		if headerLine == 0 {
			headerLine = atmFullTextLineIndex(item.FullText, "ID: ")
		}
		if headerLine == 0 {
			headerLine = 1
		}
		add("header", strings.Join(header, "\n"), headerLine, headerLine)
		detail := item.Detail
		if strings.TrimSpace(detail) != "" {
			// Detail 正文在 FullText 中从固定的 Detail 行开始（前三行是 ID/Timestamp/Summary）。
			detailBase := atmFullTextLineIndex(item.FullText, "Detail: ")
			if detailBase == 0 {
				detailBase = 1
			}
			paragraphs := strings.Split(detail, "\n\n")
			offset := 0
			for index, para := range paragraphs {
				body := strings.TrimSpace(para)
				if body != "" {
					lead := strings.Index(para, body)
					if lead < 0 {
						lead = 0
					}
					lineStart := detailBase + strings.Count(detail[:offset+lead], "\n")
					span := strings.Count(body, "\n") + 1
					kind := "detail"
					if len(paragraphs) > 1 {
						kind = fmt.Sprintf("detail:%d", index)
					}
					add(kind, body, lineStart, lineStart+span-1)
				}
				offset += len(para) + len("\n\n")
			}
		}
		return subs
	}
	locationLine := atmFullTextLineIndex(item.FullText, "Location: ")
	shortCapLine := atmFullTextLineIndex(item.FullText, "Short Caption: ")
	ocrLine := atmFullTextLineIndex(item.FullText, "OCR: ")
	tagsLine := atmFullTextLineIndex(item.FullText, "Tags: ")
	sceneStart := locationLine
	if sceneStart == 0 {
		sceneStart = 1
	}
	sceneEnd := sceneStart
	if strings.TrimSpace(item.ShortCap) != "" && shortCapLine > 0 {
		sceneEnd = shortCapLine
	}
	var scene []string
	if v := strings.TrimSpace(item.Location); v != "" {
		scene = append(scene, "Location: "+v)
	}
	if v := strings.TrimSpace(item.City); v != "" {
		scene = append(scene, "City: "+v)
	}
	if v := strings.TrimSpace(item.ShortCap); v != "" {
		scene = append(scene, "Short Caption: "+v)
	} else if v := strings.TrimSpace(item.Caption); v != "" {
		scene = append(scene, "Caption: "+v)
	}
	add("scene", strings.Join(scene, "\n"), sceneStart, sceneEnd)
	ocrStart := ocrLine
	if strings.TrimSpace(item.OCRText) == "" {
		ocrStart = tagsLine
	}
	if ocrStart == 0 {
		ocrStart = 1
	}
	ocrEnd := ocrStart
	if len(item.Tags) > 0 && tagsLine > 0 {
		ocrEnd = tagsLine
	}
	var ocr []string
	if v := strings.TrimSpace(item.OCRText); v != "" {
		ocr = append(ocr, "OCR: "+v)
	}
	if len(item.Tags) > 0 {
		ocr = append(ocr, "Tags: "+strings.Join(item.Tags, ", "))
	}
	add("ocr_tags", strings.Join(ocr, "\n"), ocrStart, ocrEnd)
	return subs
}

func atmCloudDocuments(items []*ATMSGMItem) []CloudDocument {
	documents := make([]CloudDocument, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		// FileURL 取同源场域（同日邮件 / 同日同城媒体）⇒ 同场域条目共享同一 DocHash，
		// 引擎据此做同文档凝聚与连续行共振；BlockID 是条目级标识。
		session := atmSessionID(item)
		totalLines := strings.Count(item.FullText, "\n") + 1
		if totalLines < 1 {
			totalLines = 1
		}
		documents = append(documents, CloudDocument{
			// 评测原始语料统一作为 L2 episodic 写入：引擎只理解 background / episodic / noetic
			// 三个记忆层，不理解 ATM 业务；题目需要的 ID 标注由评测模块自己维护。
			ID:         item.ItemID,
			Text:       item.FullText,
			OccurredAt: item.OccurredAt,
			NarratedAt: item.OccurredAt,
			Layer:      ramesdk.LayerEpisodic,
			DocKey:     session,
			FileURL:    session,
			BlockKey:   item.ItemID,
			BlockID:    item.ItemID,
			LineStart:  1,
			LineEnd:    totalLines,
			SubUnits:   atmHighResolutionSubUnits(item),
		})
	}
	return documents
}

// IngestATMCorpus writes source-faithful SGM units through the strict cloud SDK.
// buildATMCorpusOnlineDF 预扫描全语料构建词级文档频次表（df, totalDocs），供在线 DF 共轭洗脱注入：
// 与 maxcap 打包器同源的分句/分词（ramesdk.SplitSentences + ramesdk.SplitWords，词面小写去空白），
// 每文档内按词面去重（文档频次定义），零高级拓扑算子依赖。
func buildATMCorpusOnlineDF(items []*ATMSGMItem) (map[string]int, int) {
	df := make(map[string]int, 1<<16)
	for _, it := range items {
		if it == nil {
			continue
		}
		seen := make(map[string]bool, 64)
		for _, sent := range ramesdk.SplitSentences(it.FullText) {
			for _, w := range ramesdk.SplitWords(sent) {
				lw := strings.ToLower(strings.TrimSpace(w))
				if lw == "" || seen[lw] {
					continue
				}
				seen[lw] = true
				df[lw]++
			}
		}
	}
	return df, len(items)
}

func IngestATMCorpus(engine *CloudEngine, items []*ATMSGMItem, forceRebuild, reuse bool, logf func(string, ...any)) (CloudManifest, float64, error) {
	start := time.Now()
	documents := atmCloudDocuments(items)
	manifest, err := engine.Prepare(context.Background(), documents, forceRebuild, reuse, 8, logf)
	return manifest, time.Since(start).Seconds(), err
}

// RecallATMQuestion maps remote unit IDs directly back to the official SGM identifiers.
// Unknown or duplicate IDs are protocol errors, never silently discarded.
func RecallATMQuestion(engine *CloudEngine, byID map[string]*ATMSGMItem, question string, rawLimit int) ([]string, []ramesdk.ResolvedHit, error) {
	if rawLimit <= 0 {
		rawLimit = 50
	}
	hits, err := engine.Recall(context.Background(), question, rawLimit)
	if err != nil {
		return nil, nil, err
	}
	retIDs := make([]string, 0, len(hits))
	seen := make(map[string]bool, len(hits))
	for _, hit := range hits {
		if byID[hit.UnitID] == nil {
			return nil, nil, fmt.Errorf("cloud evaluation returned foreign ATM unit %q in scope %s", hit.UnitID, engine.Scope)
		}
		if seen[hit.UnitID] {
			return nil, nil, fmt.Errorf("cloud evaluation returned duplicate ATM unit %q", hit.UnitID)
		}
		seen[hit.UnitID] = true
		retIDs = append(retIDs, hit.UnitID)
	}
	return retIDs, hits, nil
}

// ComputeATMRecallMap computes the ATM-Bench official Per-Question GT Coverage
// Recall@k (|set(GT) ∩ set(Top-k)| / |set(GT)|). Both gtIDs and retIDs[:k] are
// deduplicated as mathematical sets so duplicate IDs in retIDs can never
// inflate coverage.
func ComputeATMRecallMap(gtIDs, retIDs []string) map[string]float64 {
	ks := []int{1, 5, 10, 25, 50, 100}
	out := make(map[string]float64, len(ks))
	gtSet := make(map[string]bool, len(gtIDs))
	for _, id := range gtIDs {
		if id != "" {
			gtSet[id] = true
		}
	}
	if len(gtSet) == 0 {
		for _, k := range ks {
			out[fmt.Sprintf("R@%d", k)] = 0.0
		}
		return out
	}
	for _, k := range ks {
		lim := k
		if len(retIDs) < lim {
			lim = len(retIDs)
		}
		seen := make(map[string]bool, lim)
		hits := 0
		for i := 0; i < lim; i++ {
			id := retIDs[i]
			if seen[id] {
				continue
			}
			seen[id] = true
			if gtSet[id] {
				hits++
			}
		}
		out[fmt.Sprintf("R@%d", k)] = float64(hits) / float64(len(gtSet))
	}
	return out
}

// ComputeATMOfficialCompositeBps computes the strict ATM-Bench official composite
// retrieval score in basis points (0..10000) solely from official Per-Question
// GT Coverage Recall@10 (gold-set-mean): 10000 * (0.70*main1013R10 + 0.30*hard31R10).
// Binary Any-Hit@10 is strictly excluded from the optimization objective.
func ComputeATMOfficialCompositeBps(main1013R10, hard31R10 float64) float64 {
	return PyRound(10000.0*(0.70*main1013R10+0.30*hard31R10), 2)
}

// FormatATMEvidencePayload 按 ATM-Bench 官方 MMRag 规范将 Top-K 证据格式化为答题输入。
func FormatATMEvidencePayload(question string, retIDs []string, byID map[string]*ATMSGMItem, retrieveK int) (string, []string) {
	if retrieveK <= 0 {
		retrieveK = 10
	}
	topK := retrieveK
	if len(retIDs) < topK {
		topK = len(retIDs)
	}
	chunks := make([]string, 0, topK)
	formattedLines := make([]string, 0, topK)
	for rank := 0; rank < topK; rank++ {
		id := retIDs[rank]
		if it := byID[id]; it != nil {
			chunks = append(chunks, fmt.Sprintf("Evidence %d:\n%s", rank+1, it.FullText))
			oneLine := strings.ReplaceAll(it.FullText, "\n", " | ")
			formattedLines = append(formattedLines, fmt.Sprintf("%d. %s · %s", rank+1, it.ItemID, oneLine))
		}
	}
	evidenceText := strings.Join(chunks, "\n")
	userPrompt := strings.NewReplacer("{question}", question, "{evidence}", evidenceText).Replace(ATMEvidencePromptTPL)
	return userPrompt, formattedLines
}

func extractATMEvidenceIDsFromText(text string) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	type posMatch struct {
		pos int
		id  string
	}
	var matches []posMatch
	for _, loc := range reATMMediaID.FindAllStringSubmatchIndex(text, -1) {
		if len(loc) >= 4 && loc[2] >= 0 {
			matches = append(matches, posMatch{pos: loc[2], id: text[loc[2]:loc[3]]})
		}
	}
	for _, loc := range reATMEmailID.FindAllStringSubmatchIndex(text, -1) {
		if len(loc) >= 4 && loc[2] >= 0 {
			matches = append(matches, posMatch{pos: loc[2], id: strings.ToLower(text[loc[2]:loc[3]])})
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].pos < matches[j].pos
	})
	var idsOut []string
	seen := map[string]bool{}
	for _, m := range matches {
		if !seen[m.id] {
			seen[m.id] = true
			idsOut = append(idsOut, m.id)
		}
	}
	return idsOut
}

func splitATMListItems(text string) []string {
	if idsOut := extractATMEvidenceIDsFromText(text); len(idsOut) > 0 {
		return idsOut
	}
	var items []string
	seen := map[string]bool{}
	for _, part := range reATMListSep.Split(text, -1) {
		clean := strings.ToLower(strings.Trim(strings.TrimSpace(part), "\"'` .,:;"))
		if clean != "" && !seen[clean] {
			seen[clean] = true
			items = append(items, clean)
		}
	}
	return items
}

// ComputeATMListJaccard 计算 ATM-Bench list_recall 题型的官方集合 Jaccard 分数（0.0 ~ 1.0）。
// 语义逐字委托官方 list_jaccard_score（见 atmofficial_scoring.go）。
func ComputeATMListJaccard(groundTruth, prediction string) float64 {
	return ATMOfficialListJaccard(groundTruth, prediction)
}

// ComputeATMNumberEM 保留旧签名，按官方确定性判分（无 question，相对日期不解析）。
// Deprecated: 评测路径必须调用 ComputeATMNumberEMWithQuestion 以对齐官方相对日期语义。
func ComputeATMNumberEM(groundTruth, prediction string) (bool, string) {
	return ComputeATMNumberEMWithQuestion(groundTruth, prediction, "")
}

// ComputeATMNumberEMWithQuestion 按官方 _deterministic_accuracy_core 判分 number 题型。
// question 用于官方相对日期解析（today/tomorrow/yesterday）。
func ComputeATMNumberEMWithQuestion(groundTruth, prediction, question string) (bool, string) {
	return ATMOfficialNumberAccuracy(groundTruth, prediction, question)
}

// BuildATMJudgePrompt 构造 ATM-Bench 官方 LLM_JUDGE_PROMPT 输入。
func BuildATMJudgePrompt(question, groundTruth, prediction string) string {
	r := strings.NewReplacer(
		"{{question}}", strings.TrimSpace(question),
		"{{answer}}", strings.TrimSpace(groundTruth),
		"{{prediction}}", strings.TrimSpace(prediction),
	)
	return r.Replace(ATMJudgePromptTPL)
}

// ParseATMJudgeResponse 解析 ATM-Bench 官方判官输出。
//
// 逐字委托官方 parse_judge_response：JSON 载荷优先；JSON 失败或缺少 accuracy
// 时按官方宽松回退（"accuracy": true/false 子串，再按 accurate/correct/matches/true/yes
// 关键词）。官方解析器没有 JUDGE_UNPARSED 语义，因此本函数不再返回解析错误。
func ParseATMJudgeResponse(raw string) (bool, string, error) {
	acc, exp, _ := ATMOfficialParseJudgeResponse(raw)
	return acc, exp, nil
}

// applyATMUnscoredAccounting 把「答不上 ≠ 答错」写进 ATM 汇总。
//
// 语义：qs_score = scored 分母口径（链路最终失败的题移出分母），同时无条件给出
// qs_score_all_denominator（全量分母，失败题按 0 计）与 scored/unscored 计数——
// 两个读数并排出现，只出其中一个即视为不合规报告。
func applyATMUnscoredAccounting(summary map[string]any, results []map[string]any, qsSum float64, qsCnt int) {
	summary["scored_count"] = qsCnt
	summary["unscored_count"] = len(results) - qsCnt
	if qsCnt > 0 {
		summary["qs_score_scored_only"] = PyRound(qsSum/float64(qsCnt), 4)
	}
	if len(results) > 0 {
		summary["qs_score_all_denominator"] = PyRound(qsSum/float64(len(results)), 4)
	}
}

// RunATM 执行 ATM-Bench 完整评测（与 eval.Run 同签名并输出兼容 summary + ATM 专属扩展指标）。
func RunATM(cfg *config.Config, opts Options) (map[string]any, error) {
	logf := opts.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	started := time.Now()
	if !IsValidPulseMode(opts.PulseMode) {
		return nil, fmt.Errorf("invalid pulse_mode %q (must be %q, %q, or %q)", opts.PulseMode, PulseModeLocalPlain, PulseModePulsePlain, PulseModePulseCipher)
	}
	if err := VerifyATMOfficialProtocol(); err != nil {
		return nil, err
	}

	dataDir, err := ResolveATMDataDir(opts.DataDir)
	if err != nil {
		return nil, err
	}
	officialCorpusPinsVerified := false
	if !opts.NoVerify {
		if err := VerifyATMCorpusPins(dataDir); err != nil {
			return nil, err
		}
		officialCorpusPinsVerified = true
	}
	tier := NormalizeATMTier(opts.Tier)
	if tier == "" {
		return nil, ErrATMTierRetired
	}
	opts.PulseMode = NormalizePulseMode(opts.PulseMode)
	scope := ScopeForSuiteAndMode(SuiteATM, cloudUserID(), opts.PulseMode)
	opts.Scope = scope
	var engine *CloudEngine
	if !opts.Oracle {
		engine, err = NewCloudEngine(cfg, SuiteATM, expandUser(opts.StorePath), WithPulseTokenizer(opts.PulseTokenizer), WithPulseMode(opts.PulseMode))
		if err != nil {
			return nil, err
		}
		scope = engine.Scope
		opts.Scope = scope
	}

	items, byID, _, err := LoadATMSGMItems(dataDir, scope, opts.CorpusLimit)
	if err != nil {
		return nil, err
	}
	qas, err := LoadATMQAItems(dataDir, tier, opts.Offset, opts.Limit)
	if err != nil {
		return nil, err
	}

	prepared := CloudManifest{Transport: "oracle-gold", Scope: scope, Suite: SuiteATM, LocalFallback: false}
	ingestSec := 0.0
	if opts.VerifyPrepare {
		verified, verifyErr := engine.VerifyPrepared(context.Background(), atmCloudDocuments(items))
		if verifyErr != nil {
			return nil, verifyErr
		}
		// VerifyPrepared 会将 engine.Scope 同步为 manifest 中的实际世代 scope；
		// 顶层回执的 scope 必须与 verified.Scope / engine.Scope 一致，避免与 manifest 世代失配。
		scope = engine.Scope
		opts.Scope = scope
		verification := map[string]any{
			"verified": true, "transport": TransportForPulseMode(opts.PulseMode), "scope": scope,
			"local_fallback": false, "engine": engine.Metadata(),
			"ingest": map[string]any{"manifest": verified, "reused": true, "inserted": 0},
		}
		if err := os.MkdirAll(opts.OutDir, 0o755); err != nil {
			return nil, err
		}
		if err := writeJSON(filepath.Join(opts.OutDir, "verification-atm.json"), verification); err != nil {
			return nil, err
		}
		return verification, nil
	}
	// 在线 DF 单向高频套话消光律（默认开启；RM 端零高级拓扑算子的在线 DF 修正，ROLLING_MEMORY_ONLINE_DF=0 可关闭作对照臂）：
	// 在语料打包前以两遍预扫描构建全语料在线文档频次 (df, totalDocs) 注入 langpack.PackMaxCapabilitySentence：
	// 遵循「提问分布与陈述库分布非对称性定律」，严禁将 !isHigh 提问语法词因陈述库低 df 误提振，
	// 仅对 df/N > 1/(e^2+e) 的高频泛滥词施加连续对数消光、对 df/N > 1/e 的极端刷屏模板词执行贝叶斯停时冲突洗脱。
	if !opts.Oracle && strings.TrimSpace(os.Getenv("ROLLING_MEMORY_ONLINE_DF")) != "0" {
		dfMap, totalDocs := buildATMCorpusOnlineDF(items)
		langpack.SetMaxCapOnlineCorpusDF(dfMap, totalDocs)
		defer langpack.SetMaxCapOnlineCorpusDF(nil, 0)
		logf("[atm-eval] online DF injected before packing: vocab=%d total_docs=%d corpus_limit=%d",
			len(dfMap), totalDocs, opts.CorpusLimit)
	}
	if !opts.Oracle {
		prepared, ingestSec, err = IngestATMCorpus(engine, items, opts.Rebuild, opts.ReusePrepare, logf)
		if err != nil {
			return nil, err
		}
		defer engine.CloseGeneration(context.Background(), logf)
		// Prepare 可能已把引擎绑定到本次评测自己的世代 scope：provenance 一律以绑定后的真值为准。
		scope = engine.Scope
		opts.Scope = scope
	}

	workers := opts.Workers
	if workers < 1 {
		workers = 8
	}
	if workers > 32 {
		workers = 32
	}

	rawLimit := opts.RawLimit
	if rawLimit <= 0 {
		rawLimit = 50
	}
	retrieveK := 10

	var ansClient, jdgClient *ChatClient
	if opts.Answerer == "llm" || opts.Judge == "llm" {
		evalCfg := *cfg
		if opts.BaseURL != "" || cfg.Eval.BaseURL != "" {
			evalCfg.Mode = "cloud"
		}
		if opts.Answerer == "llm" {
			ansClient, err = NewChatClient(&evalCfg, ChatOptions{
				BaseURL:         opts.BaseURL,
				Model:           opts.Model,
				APIKey:          opts.APIKey,
				Temperature:     opts.Temperature,
				MaxTokens:       opts.MaxTokens,
				TimeoutSec:      opts.TimeoutSec,
				Thinking:        opts.Thinking,
				ReasoningEffort: opts.ReasoningEffort,
			})
			if err != nil {
				return nil, err
			}
		}
		if opts.Judge == "llm" {
			jdgClient, err = NewChatClient(&evalCfg, ChatOptions{
				BaseURL:     opts.BaseURL,
				Model:       opts.Model,
				APIKey:      opts.APIKey,
				Temperature: opts.JudgeTemp,
				MaxTokens:   opts.MaxTokens,
				TimeoutSec:  opts.TimeoutSec,
			})
			if err != nil {
				return nil, err
			}
		}
	}

	armMode := ArmModeForPulseMode(opts.PulseMode)
	if opts.Oracle {
		armMode = "oracle"
	}
	arm := "atm-" + tier + "-" + armMode + "-" + answererName(opts)
	logf("[atm-eval] suite=atm pulse_mode=%s tier=%s questions=%d sgm_items=%d raw_limit=%d answerer=%s judge=%s",
		opts.PulseMode, tier, len(qas), len(items), rawLimit, answererName(opts), judgeName(opts))

	results := make([]map[string]any, len(qas))
	var doneCount int64
	recallErrs := make([]error, len(qas))
	jobs := make(chan int, len(qas))
	for i := range qas {
		jobs <- i
	}
	close(jobs)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				q := qas[idx]
				t0 := time.Now()
				var retIDs []string
				cloudHits := 0
				if opts.Oracle {
					retIDs = append([]string{}, q.EvidenceIDs...)
				} else {
					var recallErr error
					retIDs, _, recallErr = RecallATMQuestion(engine, byID, q.Question, rawLimit)
					if recallErr != nil {
						recallErrs[idx] = recallErr
						continue
					}
					cloudHits = len(retIDs)
				}
				recallSec := time.Since(t0).Seconds()
				recMap := ComputeATMRecallMap(q.EvidenceIDs, retIDs)
				r10 := recMap["R@10"]
				r25 := recMap["R@25"]
				r50 := recMap["R@50"]
				evHit := len(q.EvidenceIDs) > 0 && r10 > 0
				allHit10 := len(q.EvidenceIDs) > 0 && r10 >= 1.0-1e-9

				userPayload, formattedLines := FormatATMEvidencePayload(q.Question, retIDs, byID, retrieveK)
				contextText := strings.Join(formattedLines, "\n")
				aic := TokenRecall(q.Answer, contextText) >= 0.8

				prediction := ""
				answerErr := ""
				if ansClient != nil {
					ans, aErr := ansClient.CompleteWithRetry(ATMSystemPrompt, userPayload)
					if aErr != nil {
						answerErr = aErr.Error()
					} else {
						prediction = strings.TrimSpace(ans)
					}
				}

				f1 := TokenF1(prediction, q.Answer)
				exact := NormalizeAnswer(prediction) != "" && NormalizeAnswer(prediction) == NormalizeAnswer(q.Answer)

				var judgeCorrect any
				judgeReason := ""
				unscoredReason := ""
				qsScore := 0.0
				qsScored := false

				if answerErr != "" {
					unscoredReason = "ANSWER_UNAVAILABLE: " + answerErr
					judgeReason = unscoredReason
				} else if opts.Judge == "llm" || (opts.Answerer == "llm" && (q.QType == "list_recall" || q.QType == "number")) {
					switch q.QType {
					case "list_recall":
						qsScore = ComputeATMListJaccard(q.Answer, prediction)
						qsScored = true
						isCor := qsScore >= 0.5
						judgeCorrect = isCor
						judgeReason = fmt.Sprintf("ATM official list_recall Jaccard = %.3f", qsScore)
					case "number":
						emOK, reason := ComputeATMNumberEMWithQuestion(q.Answer, prediction, q.Question)
						if emOK {
							qsScore = 1.0
						}
						qsScored = true
						judgeCorrect = emOK
						judgeReason = reason
					default:
						if jdgClient != nil {
							rawJdg, jErr := jdgClient.CompleteWithRetry("", BuildATMJudgePrompt(q.Question, q.Answer, prediction))
							if jErr == nil {
								acc, exp, pErr := ParseATMJudgeResponse(rawJdg)
								if pErr == nil {
									judgeCorrect = acc
									if acc {
										qsScore = 1.0
									}
									qsScored = true
									judgeReason = exp
								} else {
									judgeReason = "JUDGE_UNPARSED"
								}
							} else {
								unscoredReason = "JUDGE_UNAVAILABLE: " + jErr.Error()
								judgeReason = unscoredReason
							}
						}
					}
				}

				qCat := "4"
				switch q.QType {
				case "list_recall":
					qCat = "1"
				case "number":
					qCat = "2"
				case "open_end":
					qCat = "3"
				}

				verdictStr := ""
				if b, ok := judgeCorrect.(bool); ok {
					if b {
						verdictStr = "correct"
					} else {
						verdictStr = "wrong"
					}
				}
				failureBucket := ClassifyFailureBucket(
					evHit, aic || r25 >= 0.5, r50, f1, r25, verdictStr,
				)
				rowTransport := TransportForPulseMode(opts.PulseMode)
				if opts.Oracle {
					rowTransport = "oracle-gold"
				}

				results[idx] = map[string]any{
					"id":                   q.ID,
					"sample_id":            q.QType,
					"category":             qCat,
					"qtype":                q.QType,
					"question":             q.Question,
					"gold":                 q.Answer,
					"evidence_texts":       q.EvidenceIDs,
					"retrieval_ids":        retIDs,
					"retrieval_recall":     recMap,
					"prediction":           prediction,
					"f1":                   PyRound(f1, 4),
					"exact":                exact,
					"evidence_recall":      PyRound(r10, 4),
					"best_evidence_recall": PyRound(r10, 4),
					"evidence_hit":         evHit,
					"all_hit_10":           allHit10,
					"full_evidence_recall": PyRound(r50, 4),
					"full_evidence_hit":    r50 > 0,
					"answer_token_recall":  PyRound(r25, 4),
					"answer_in_context":    aic,
					"failure_bucket":       failureBucket,
					"judge":                verdictStr,
					"judge_correct":        judgeCorrect,
					"judge_reason":         judgeReason,
					"qs_score":             PyRound(qsScore, 4),
					"qs_scored":            qsScored,
					"answer_error":         answerErr,
					"unscored_reason":      unscoredReason,
					"context_chars":        len(contextText),
					"recall_seconds":       PyRound(recallSec, 4),
					"bands":                0,
					"nodes":                0,
					"cloud_hits":           cloudHits,
					"engine_transport":     rowTransport,
					"engine_scope":         scope,
					"local_fallback":       false,
					"raw":                  len(retIDs),
					"raw_hits":             len(retIDs),
					"formatted_items":      formattedLines,
				}
				done := atomic.AddInt64(&doneCount, 1)
				if opts.ProgressEach > 0 && (int(done)%opts.ProgressEach == 0 || int(done) == len(qas)) {
					logf("[atm-eval] %d/%d R@10=%.3f R@50=%.3f", done, len(qas), r10, r50)
				}
			}
		}()
	}
	wg.Wait()
	for _, recallErr := range recallErrs {
		if recallErr != nil {
			return nil, recallErr
		}
	}

	summary := Aggregate(results, arm)
	ks := []int{1, 5, 10, 25, 50, 100}
	recSums := make(map[string]float64, len(ks))
	allHit10Sum := 0.0
	qsSum := 0.0
	qsCnt := 0
	for _, r := range results {
		if rm, ok := r["retrieval_recall"].(map[string]float64); ok {
			for _, k := range ks {
				key := fmt.Sprintf("R@%d", k)
				recSums[key] += rm[key]
			}
		}
		if ah, _ := r["all_hit_10"].(bool); ah {
			allHit10Sum += 1.0
		}
		if scored, _ := r["qs_scored"].(bool); scored {
			qsCnt++
			if sc, ok := r["qs_score"].(float64); ok {
				qsSum += sc
			}
		}
	}
	recAvg := make(map[string]float64, len(ks))
	for _, k := range ks {
		key := fmt.Sprintf("R@%d", k)
		if len(results) > 0 {
			recAvg[key] = PyRound(recSums[key]/float64(len(results)), 4)
		}
	}
	summary["suite"] = SuiteATM
	summary["protocol"] = ATMProtocolText
	summary["scoring_protocol"] = ATMOfficialScoringProtocol
	summary["scoring_version"] = ATMOfficialScoringVersion
	summary["scoring_fixture_sha256"] = PinATMOfficialScoringFixtureSHA256
	summary["scoring_number"] = "official deterministic_accuracy"
	summary["scoring_list"] = "official list_jaccard_score"
	summary["scoring_judge_parser"] = "official parse_judge_response"
	summary["recall_metric"] = "gold-set-mean"
	summary["official_corpus_pins_verified"] = officialCorpusPinsVerified
	summary["official_corpus_pins_skipped"] = opts.NoVerify
	summary["recall_at_k"] = recAvg
	summary["r_at_10"] = recAvg["R@10"]
	summary["r_at_25"] = recAvg["R@25"]
	summary["r_at_50"] = recAvg["R@50"]
	summary["any_hit_10"] = summary["evidence_hit"]
	if len(results) > 0 {
		summary["all_hit_10"] = PyRound(allHit10Sum/float64(len(results)), 4)
	} else {
		summary["all_hit_10"] = 0.0
	}
	if qsCnt > 0 {
		summary["qs_score"] = PyRound(qsSum/float64(qsCnt), 4)
		summary["judge_rate"] = summary["qs_score"]
	}
	// unscored 分母显式化：答案/判官链路最终失败（重试耗尽）的题不计入 qs_score 分母，
	// 但必须同时给出全量分母读数与逐题原因，禁止用「未判分」掩盖「答不上」。
	applyATMUnscoredAccounting(summary, results, qsSum, qsCnt)
	// inserted = 真实写入数（CloudManifest.InsertedUnits：全量重灌 / 断点补灌 / 正文自愈重灌），
	// 不得用 corpus 大小冒充；VerifyPrepared 复用为零写入。
	inserted := prepared.InsertedUnits
	if prepared.Reused {
		inserted = 0
	}
	summary["ingest"] = map[string]any{
		"corpus_rows": len(items), "inserted": inserted, "reused": prepared.Reused,
		"ingest_seconds": PyRound(ingestSec, 2), "scope": scope,
		"corpus_sha256": prepared.CorpusSHA256, "manifest": prepared,
		"generation": prepared.Generation,
		// purged_units = 本次「重算记忆」在本世代 scope 内清空后重灌（世代 scope 下恒为 0 或极小）。
		"purged_units": prepared.PurgedUnits,
		// retired_scopes = 本次回收的过期世代回执（独立世代 scope，回收不阻塞本次运行）。
		"retired_scopes": prepared.RetiredScopes,
	}
	summary["mode"] = ArmModeForPulseMode(opts.PulseMode)
	summary["pulse_mode"] = NormalizePulseMode(opts.PulseMode)
	summary["transport"] = TransportForPulseMode(opts.PulseMode)
	summary["scope"] = scope
	summary["local_fallback"] = false
	if opts.Oracle {
		summary["mode"] = "oracle"
		summary["pulse_mode"] = "oracle"
		summary["transport"] = "oracle-gold"
		summary["engine"] = map[string]any{"transport": "oracle-gold", "scope": scope, "local_fallback": false}
	} else {
		summary["engine"] = engine.Metadata()
	}
	summary["elapsed_seconds"] = PyRound(time.Since(started).Seconds(), 1)
	// 模型面 provenance：答题器与判官各自的线上模型 / 端点（与 LoCoMo runner 同名口径）。
	// 判分口径（尤其 open_end 判官模型）是榜单直比的前提，工件必须自证，不能只靠调用方口述。
	summary["answerer"] = answererName(opts)
	summary["judge"] = judgeName(opts)
	summary["model"] = ""
	summary["judge_model"] = ""
	summary["answer_base_url"] = ""
	summary["judge_base_url"] = ""
	if ansClient != nil {
		summary["model"] = ansClient.Model()
		summary["answer_base_url"] = ansClient.URL()
	}
	if jdgClient != nil {
		summary["judge_model"] = jdgClient.Model()
		summary["judge_base_url"] = jdgClient.URL()
	}
	summary["reasoning_effort"] = opts.ReasoningEffort
	summary["thinking"] = opts.Thinking
	if err := os.MkdirAll(opts.OutDir, 0o755); err != nil {
		return nil, err
	}
	_ = writeResults(filepath.Join(opts.OutDir, "results-"+arm+".jsonl"), results)
	_ = writeJSON(filepath.Join(opts.OutDir, "summary-"+arm+".json"), summary)
	return summary, nil
}

// ExportBundledATMDataset 从原始 ATM-Bench 数据目录生成随包可复现的 evaldata/atmbench 资产包。
func ExportBundledATMDataset(rawDir, outDir string) (map[string]any, error) {
	items, _, _, err := LoadATMSGMItems(rawDir, ATMScope, 0)
	if err != nil {
		return nil, err
	}
	hardQAs, err := LoadATMQAItems(rawDir, TierATMHard, 0, 0)
	if err != nil {
		return nil, err
	}
	fullQAs, err := LoadATMQAItems(rawDir, TierATM1013, 0, 0)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(filepath.Join(outDir, "tiers"), 0o755); err != nil {
		return nil, err
	}

	corpusPath := filepath.Join(outDir, "corpus.jsonl.gz")
	cf, err := os.Create(corpusPath)
	if err != nil {
		return nil, err
	}
	gz, err := gzip.NewWriterLevel(cf, gzip.BestCompression)
	if err != nil {
		cf.Close()
		return nil, err
	}
	gz.ModTime = time.Unix(0, 0)
	for _, it := range items {
		line, err := json.Marshal(it)
		if err != nil {
			gz.Close()
			cf.Close()
			return nil, err
		}
		if _, err := gz.Write(append(line, '\n')); err != nil {
			gz.Close()
			cf.Close()
			return nil, err
		}
	}
	if err := gz.Close(); err != nil {
		cf.Close()
		return nil, err
	}
	if err := cf.Close(); err != nil {
		return nil, err
	}

	writeTierFile := func(rel string, qas []ATMQAItem) (string, error) {
		full := filepath.Join(outDir, rel)
		raw, err := json.MarshalIndent(qas, "", "  ")
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(full, append(raw, '\n'), 0o644); err != nil {
			return "", err
		}
		return sha256FileHex(full)
	}

	corpusSHA, err := sha256FileHex(corpusPath)
	if err != nil {
		return nil, err
	}
	hardSHA, err := writeTierFile("tiers/questions-hard.json", hardQAs)
	if err != nil {
		return nil, err
	}
	fullSHA, err := writeTierFile("tiers/questions-1013.json", fullQAs)
	if err != nil {
		return nil, err
	}

	officialCorpusPinsVerified := VerifyATMCorpusPins(filepath.Dir(corpusPath)) == nil
	manifest := map[string]any{
		"version":                       "1",
		"suite":                         SuiteATM,
		"protocol":                      ATMProtocolText,
		"scoring_protocol":              ATMOfficialScoringProtocol,
		"scoring_version":               ATMOfficialScoringVersion,
		"official_corpus_pins_verified": officialCorpusPinsVerified,
		"official_corpus_pins_skipped":  false,
		"pins": map[string]string{
			"answer_system_sha1":             PinATMSystemSHA1,
			"judge_tpl_sha1":                 PinATMJudgeTPLSHA1,
			"scoring_fixture_sha256":         PinATMOfficialScoringFixtureSHA256,
			"evaluator_normalizer_sha1":      PinATMOfficialNormalizerSHA1,
			"evaluator_evaluate_qa_sha1":     PinATMOfficialEvaluateQASHA1,
			"evaluator_qtype_utils_sha1":     PinATMOfficialQTypeUtilsSHA1,
			"evaluator_config_sha1":          PinATMOfficialConfigSHA1,
			"official_corpus_sha256":         PinATMCorpusSHA256,
			"official_questions_1013_sha256": PinATM1013SHA256,
			"official_questions_hard_sha256": PinATMHardSHA256,
		},
		"source": map[string]string{
			"benchmark": "University of Cambridge ATM-Bench (https://atmbench.github.io/leaderboard.html)",
			"corpus":    "SGM Multimodal Personal Memory Corpus (6,742 emails + 3,759 photos + 533 videos = 11,034 items)",
		},
		"corpus": map[string]any{
			"path":   "corpus.jsonl.gz",
			"sha256": corpusSHA,
			"rows":   len(items),
		},
		"tiers": map[string]any{
			TierATMHard: map[string]any{
				"file":   "tiers/questions-hard.json",
				"sha256": hardSHA,
				"count":  len(hardQAs),
				"note":   "ATM-Bench-Hard 31 high-difficulty multi-hop, temporal & abstention questions",
			},
			TierATM1013: map[string]any{
				"file":   "tiers/questions-1013.json",
				"sha256": fullSHA,
				"count":  len(fullQAs),
				"note":   "ATM-Bench Main full 1,013-question benchmark",
			},
		},
	}
	mBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(outDir, "MANIFEST.json"), append(mBytes, '\n'), 0o644); err != nil {
		return nil, err
	}
	return manifest, nil
}

func sha256FileHex(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
