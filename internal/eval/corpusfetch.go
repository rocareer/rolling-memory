// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package eval

// 上游语料获取（`rmem eval --fetch atm`）：按钉住的上游 revision 下载评测数据，
// 在本机逐位重建官方 pinned 形态，并复核官方 SHA-256 pin。
//
// 约束与边界：
//   - 语料不随本仓库分发（ATM-Bench 数据集为 CC BY-NC 4.0），因此只做「用户机器
//     直连上游下载 + 本地重建」，不产生任何再分发；
//   - 幂等：本地已存在且大小/哈希一致的文件跳过下载；
//   - 原子：下载先写 .part 再改名；重建产物先落临时目录，pin 复核通过后才就位；
//   - 默认零外呼：本文件仅在显式 --fetch 时被调用。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// ATMUpstreamDatasetID / ATMUpstreamRevision 钉住上游 Hugging Face 数据集与
	// 「可逐位重建出官方 pin」的 revision；实测该 revision 下重建结果与
	// PinATMCorpusSHA256 及两档题集 pin 完全一致。
	ATMUpstreamDatasetID  = "Jingbiao/ATM-Bench"
	ATMUpstreamRevision   = "78e826dc07e97466b2f54443831ef9a83ab8b27c"
	ATMUpstreamLicense    = "CC BY-NC 4.0（Creative Commons Attribution-NonCommercial 4.0 International）"
	ATMUpstreamLicenseURL = "https://creativecommons.org/licenses/by-nc/4.0/"
	ATMUpstreamDatasetURL = "https://huggingface.co/datasets/Jingbiao/ATM-Bench"
	ATMUpstreamCodeURL    = "https://github.com/JingbiaoMei/ATM-Bench"
	ATMUpstreamBaseURL    = "https://huggingface.co"

	// LoCoMo（GitHub 上游仓库）与 ATM 共用同一非商业数据许可口径。
	LoCoMoUpstreamLicense    = "CC BY-NC 4.0（Creative Commons Attribution-NonCommercial 4.0 International）"
	LoCoMoUpstreamLicenseURL = "https://creativecommons.org/licenses/by-nc/4.0/"

	upstreamHTTPTimeout = 30 * time.Minute
)

// ErrUpstreamLicenseNotAccepted 表示需要先确认上游语料许可才能开始下载。
var ErrUpstreamLicenseNotAccepted = errors.New("未确认上游语料许可（CC BY-NC 4.0）：下载前须接受上游条款")

// upstreamFile 一个上游文件：上游相对路径 → 本地相对路径 + 期望大小/SHA-256。
type upstreamFile struct {
	Upstream string
	Local    string
	Size     int64
	SHA256   string
}

// atmUpstreamFiles 是评测必需的最小文件集（约 35 MB；上游完整包远大于此，
// 其中原始图像/视频对检索评测不必要）。
var atmUpstreamFiles = []upstreamFile{
	{"data/raw_memory/email/emails.json", "raw_memory/email/emails.json", 5037231, "5c82c38e7f18923a9eba3af321663a8a5bd70e94e0d5e59e987141c71219a8af"},
	{"data/processed_memory/image_batch_results.json", "processed_memory/image_batch_results.json", 27531919, "7204e8b4ab1a0fea97f2003058213742d47589e09636941f8d83b2e77c33e0a1"},
	{"data/processed_memory/video_batch_results.json", "processed_memory/video_batch_results.json", 1742096, "88ed14ce32afde1a4d54d5b77a0e7f84305b543186307be6c9bc34119342727a"},
	{"data/atm-bench/atm-bench.json", "atm-bench/atm-bench.json", 319224, "ab6eaa9df62fb4162e0f5eecd98768a7e3ae721e32d2db2cf227ff41e3295762"},
	{"data/atm-bench/atm-bench-hard.json", "atm-bench/atm-bench-hard.json", 21342, "acd35f2a172a9741d970d2cf21184ff0af8d79a8bf59967fc8aa33d619f6af4a"},
}

// FetchATMOptions 控制一次 ATM-Bench 上游取数。
type FetchATMOptions struct {
	TargetDir     string // 本地数据目录（将写入 raw_memory/ processed_memory/ atm-bench/ 与重建件）
	Revision      string // 上游 revision（默认 ATMUpstreamRevision）
	AcceptLicense bool   // 已确认上游许可（未确认且需要下载时返回 ErrUpstreamLicenseNotAccepted）
	BaseURL       string // 上游服务基址（默认 Hugging Face）
	HTTPClient    *http.Client
	Log           func(format string, args ...any) // 进度输出（可为 nil）

	// 测试注入口（零值 = 官方文件表与官方 pin）。
	files    []upstreamFile
	expected map[string]string
}

// FetchATMFileReport 单个文件的取数结果。
type FetchATMFileReport struct {
	Path       string `json:"path"`
	Local      string `json:"local"`
	Bytes      int64  `json:"bytes"`
	SHA256     string `json:"sha256"`
	Downloaded bool   `json:"downloaded"`
}

// FetchATMReport 一次 ATM-Bench 上游取数的完整回执。
type FetchATMReport struct {
	DatasetID      string               `json:"dataset_id"`
	DatasetURL     string               `json:"dataset_url"`
	Revision       string               `json:"revision"`
	TargetDir      string               `json:"target_dir"`
	License        string               `json:"license"`
	LicenseURL     string               `json:"license_url"`
	Files          []FetchATMFileReport `json:"files"`
	Rebuilt        bool                 `json:"rebuilt"`
	PinsVerified   bool                 `json:"pins_verified"`
	CorpusSHA256   string               `json:"corpus_sha256"`
	TierSHA256     map[string]string    `json:"tier_sha256"`
	ProvenancePath string               `json:"provenance_path"`
}

// UpstreamATMNotice 返回下载前展示给用户的上游语料说明。
func UpstreamATMNotice() string {
	return fmt.Sprintf(`上游语料说明（下载前请确认）：
  数据集：%s（%s）
  数据许可：%s
  许可全文：%s
  上游代码：%s（MIT；该许可只覆盖代码，语料数据按上面的数据许可发布）
  取数只发生在本机：从上游下载必要文件（约 35 MB）并在本地重建 pinned 形态，不触碰你的本地记忆数据。
`, ATMUpstreamDatasetID, ATMUpstreamDatasetURL, ATMUpstreamLicense, ATMUpstreamLicenseURL, ATMUpstreamCodeURL)
}

// ResolveATMFetchTarget 决定 --fetch 的目标目录：
// 显式 --eval-data → 环境变量 ROLLING_MEMORY_ATM_DATA → 已存在的评测数据目录 → 用户数据目录默认位置。
func ResolveATMFetchTarget(explicitDataDir string) string {
	if explicit := strings.TrimSpace(explicitDataDir); explicit != "" {
		return filepath.Join(expandUser(explicit), "atmbench")
	}
	if env := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_ATM_DATA")); env != "" {
		return expandUser(env)
	}
	if dir, err := ResolveDataDir(""); err == nil {
		return filepath.Join(dir, "atmbench")
	}
	if xdgData := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); xdgData != "" {
		return filepath.Join(expandUser(xdgData), "rolling-memory", DataDirName, "atmbench")
	}
	if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
		return filepath.Join(localAppData, "rolling-memory", DataDirName, "atmbench")
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".local", "share", "rolling-memory", DataDirName, "atmbench")
	}
	return ""
}

// FetchATMUpstream 下载上游文件、本地重建 pinned 形态并复核官方 pin。
// 仅当确实需要下载且有文件缺失时要求 AcceptLicense。
func FetchATMUpstream(ctx context.Context, opts FetchATMOptions) (*FetchATMReport, error) {
	target := expandUser(strings.TrimSpace(opts.TargetDir))
	if target == "" {
		return nil, errors.New("取数目录为空：请用 --eval-data <dir> 指定，或设置 ROLLING_MEMORY_ATM_DATA")
	}
	files := opts.files
	if len(files) == 0 {
		files = atmUpstreamFiles
	}
	expected := opts.expected
	useOfficialPins := len(expected) == 0
	if useOfficialPins {
		expected = ATMOfficialCorpusSHA256s()
	}
	revision := strings.TrimSpace(opts.Revision)
	if revision == "" {
		revision = ATMUpstreamRevision
	}
	base := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if base == "" {
		base = ATMUpstreamBaseURL
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: upstreamHTTPTimeout}
	}
	logf := func(format string, args ...any) {
		if opts.Log != nil {
			opts.Log(format, args...)
		}
	}

	if err := os.MkdirAll(target, 0o755); err != nil {
		return nil, fmt.Errorf("无法创建取数目录 %s: %w", target, err)
	}
	report := &FetchATMReport{
		DatasetID:  ATMUpstreamDatasetID,
		DatasetURL: ATMUpstreamDatasetURL,
		Revision:   revision,
		TargetDir:  target,
		License:    ATMUpstreamLicense,
		LicenseURL: ATMUpstreamLicenseURL,
	}
	for _, f := range files {
		localPath := filepath.Join(target, filepath.FromSlash(f.Local))
		if fileMatchesDigest(localPath, f.Size, f.SHA256) {
			logf("语料已就位（哈希一致，跳过下载）：%s", f.Local)
			report.Files = append(report.Files, FetchATMFileReport{Path: f.Upstream, Local: f.Local, Bytes: f.Size, SHA256: f.SHA256})
			continue
		}
		if !opts.AcceptLicense {
			return nil, ErrUpstreamLicenseNotAccepted
		}
		logf("下载 %s（%s）...", f.Local, humanBytes(f.Size))
		if err := downloadUpstreamFile(ctx, client, base, revision, f, localPath); err != nil {
			return nil, err
		}
		report.Files = append(report.Files, FetchATMFileReport{Path: f.Upstream, Local: f.Local, Bytes: f.Size, SHA256: f.SHA256, Downloaded: true})
	}

	logf("本地重建 pinned 语料（上游 raw → corpus.jsonl.gz + tiers）...")
	tmpDir, err := os.MkdirTemp(target, ".rebuild-")
	if err != nil {
		return nil, fmt.Errorf("无法创建重建临时目录: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	manifest, err := ExportBundledATMDataset(target, tmpDir)
	if err != nil {
		return nil, fmt.Errorf("重建 pinned 语料失败: %w", err)
	}
	corpusSHA, tierSHAs, err := manifestDigests(manifest)
	if err != nil {
		return nil, err
	}
	for rel, want := range expected {
		got, ok := digestForRel(rel, corpusSHA, tierSHAs)
		if !ok {
			return nil, fmt.Errorf("重建产物缺少官方 pin 对应文件 (%s)", rel)
		}
		if got != want {
			return nil, fmt.Errorf("重建产物与官方 pin 不符 (%s)：期望 %s，实际 %s；上游数据可能已变更，请保留现场反馈", rel, want, got)
		}
	}
	if useOfficialPins {
		if err := VerifyATMCorpusPins(tmpDir); err != nil {
			return nil, fmt.Errorf("重建产物未通过官方 pin 复核: %w", err)
		}
	}
	// 既有清单同 pin 时保留原文件：冻结基线目录只做逐位重建，不改动既有 MANIFEST。
	keepManifest := existingManifestMatchesPins(target, corpusSHA, tierSHAs)
	if err := installRebuiltArtifacts(target, tmpDir, []string{"corpus.jsonl.gz", "MANIFEST.json"}, keepManifest); err != nil {
		return nil, err
	}
	if keepManifest {
		logf("既有 MANIFEST.json 与本轮重建同 pin，予以保留")
	}
	provPath, err := writeUpstreamProvenance(target, report, corpusSHA, tierSHAs)
	if err != nil {
		return nil, err
	}
	logf("完成：语料已重建并就位（官方 pin 复核通过）→ %s", target)
	return &FetchATMReport{
		DatasetID:      report.DatasetID,
		DatasetURL:     report.DatasetURL,
		Revision:       report.Revision,
		TargetDir:      target,
		License:        report.License,
		LicenseURL:     report.LicenseURL,
		Files:          report.Files,
		Rebuilt:        true,
		PinsVerified:   true,
		CorpusSHA256:   corpusSHA,
		TierSHA256:     tierSHAs,
		ProvenancePath: provPath,
	}, nil
}

func downloadUpstreamFile(ctx context.Context, client *http.Client, base, revision string, f upstreamFile, localPath string) error {
	url := fmt.Sprintf("%s/datasets/%s/resolve/%s/%s", base, ATMUpstreamDatasetID, revision, f.Upstream)
	return downloadVerifiedURL(ctx, client, url, localPath, f.Size, f.SHA256)
}

// downloadVerifiedURL 带原子落盘与大小/哈希校验的下载（.part 写入后校验再改名）。
func downloadVerifiedURL(ctx context.Context, client *http.Client, url, localPath string, size int64, sha string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("构造下载请求失败: %w", err)
	}
	req.Header.Set("User-Agent", "rolling-memory-eval-fetch")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("下载失败 (%s): %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("下载失败 (%s): HTTP %d %s", url, resp.StatusCode, strings.TrimSpace(string(detail)))
	}
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return fmt.Errorf("无法创建目录 %s: %w", filepath.Dir(localPath), err)
	}
	tmp := localPath + ".part"
	fh, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("无法写入 %s: %w", tmp, err)
	}
	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(fh, hasher), resp.Body)
	closeErr := fh.Close()
	if copyErr != nil {
		os.Remove(tmp)
		return fmt.Errorf("下载中断 (%s): %w", url, copyErr)
	}
	if closeErr != nil {
		os.Remove(tmp)
		return fmt.Errorf("写入失败 %s: %w", tmp, closeErr)
	}
	if size > 0 && written != size {
		os.Remove(tmp)
		return fmt.Errorf("上游文件大小不符 (%s)：期望 %d，实际 %d", filepath.Base(localPath), size, written)
	}
	got := hex.EncodeToString(hasher.Sum(nil))
	if sha != "" && got != sha {
		os.Remove(tmp)
		return fmt.Errorf("上游文件哈希不符 (%s)：期望 %s，实际 %s；请保留现场反馈", filepath.Base(localPath), sha, got)
	}
	if err := os.Rename(tmp, localPath); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("落盘失败 %s: %w", localPath, err)
	}
	return nil
}

func fileMatchesDigest(path string, size int64, sha string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	if size > 0 && info.Size() != size {
		return false
	}
	if sha == "" {
		return true
	}
	fh, err := os.Open(path)
	if err != nil {
		return false
	}
	defer fh.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, fh); err != nil {
		return false
	}
	return hex.EncodeToString(hasher.Sum(nil)) == sha
}

// manifestDigests 从 ExportBundledATMDataset 的清单里取出语料与各档题集的 SHA-256。
func manifestDigests(manifest map[string]any) (string, map[string]string, error) {
	corpus, _ := manifest["corpus"].(map[string]any)
	corpusSHA, _ := corpus["sha256"].(string)
	if corpusSHA == "" {
		return "", nil, errors.New("重建清单缺少 corpus.sha256")
	}
	tiers, _ := manifest["tiers"].(map[string]any)
	tierSHAs := map[string]string{}
	for _, value := range tiers {
		entry, _ := value.(map[string]any)
		file, _ := entry["file"].(string)
		sha, _ := entry["sha256"].(string)
		if file != "" && sha != "" {
			tierSHAs[file] = sha
		}
	}
	return corpusSHA, tierSHAs, nil
}

func digestForRel(rel, corpusSHA string, tierSHAs map[string]string) (string, bool) {
	if rel == "corpus.jsonl.gz" {
		return corpusSHA, corpusSHA != ""
	}
	got, ok := tierSHAs[rel]
	return got, ok
}

// installRebuiltArtifacts 把重建产物从临时目录搬运到目标目录（覆盖旧产物；keepManifest 时保留既有清单）。
func installRebuiltArtifacts(target, tmpDir string, files []string, keepManifest bool) error {
	for _, rel := range files {
		if rel == "MANIFEST.json" && keepManifest {
			continue
		}
		if err := os.Rename(filepath.Join(tmpDir, rel), filepath.Join(target, rel)); err != nil {
			return fmt.Errorf("就位 %s 失败: %w", rel, err)
		}
	}
	if err := os.RemoveAll(filepath.Join(target, "tiers")); err != nil {
		return fmt.Errorf("清理旧 tiers 失败: %w", err)
	}
	if err := os.Rename(filepath.Join(tmpDir, "tiers"), filepath.Join(target, "tiers")); err != nil {
		return fmt.Errorf("就位 tiers 失败: %w", err)
	}
	return nil
}

// existingManifestMatchesPins 报告目标目录里既有 MANIFEST.json 是否与重建产物同 pin。
// 同 pin 时保留原清单（例如冻结的基线目录只做逐位重建，不改动既有文件）。
func existingManifestMatchesPins(target, corpusSHA string, tierSHAs map[string]string) bool {
	raw, err := os.ReadFile(filepath.Join(target, "MANIFEST.json"))
	if err != nil {
		return false
	}
	var manifest struct {
		Corpus struct {
			SHA256 string `json:"sha256"`
		} `json:"corpus"`
		Tiers map[string]struct {
			File   string `json:"file"`
			SHA256 string `json:"sha256"`
		} `json:"tiers"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return false
	}
	if manifest.Corpus.SHA256 == "" || manifest.Corpus.SHA256 != corpusSHA {
		return false
	}
	onDisk := map[string]string{}
	for _, entry := range manifest.Tiers {
		if entry.File != "" {
			onDisk[entry.File] = entry.SHA256
		}
	}
	for rel, sha := range tierSHAs {
		if onDisk[rel] != sha {
			return false
		}
	}
	return true
}

// writeUpstreamProvenance 记录本次取数的来源、revision、逐文件哈希与重建结论。
func writeUpstreamProvenance(target string, report *FetchATMReport, corpusSHA string, tierSHAs map[string]string) (string, error) {
	record := map[string]any{
		"dataset_id":  report.DatasetID,
		"dataset_url": report.DatasetURL,
		"code_url":    ATMUpstreamCodeURL,
		"revision":    report.Revision,
		"license":     report.License,
		"license_url": report.LicenseURL,
		"fetched_at":  time.Now().Format(time.RFC3339),
		"files":       report.Files,
		"rebuild": map[string]any{
			"corpus_sha256": corpusSHA,
			"tier_sha256":   tierSHAs,
			"pins_verified": true,
		},
		"note": "obtained from the upstream dataset on this machine and rebuilt locally; corpora are not redistributed with the project",
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

func humanBytes(n int64) string {
	if n >= 1<<20 {
		return fmt.Sprintf("%.1f MB", float64(n)/float64(1<<20))
	}
	if n >= 1<<10 {
		return fmt.Sprintf("%.1f KB", float64(n)/float64(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
