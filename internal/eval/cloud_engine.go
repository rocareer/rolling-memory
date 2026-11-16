// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package eval

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/rocareer/rolling-memory/internal/cloudcred"
	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/evalns"
	"github.com/rocareer/rolling-memory/internal/langpack"
	"github.com/rocareer/rolling-memory/internal/langpack/en"
	"github.com/rocareer/rolling-memory/internal/thinclient"
	"github.com/rocareer/rolling-memory/internal/zhao/rm"
	ramesdk "github.com/rocareer/rolling-memory/sdk/go"
	_ "modernc.org/sqlite"
)

const (
	// CloudTransport identifies the pulse-cipher cloud evaluation transport.
	CloudTransport = "ramesdk.pulse_cipher.open-memory-v1"
	// PulsePlainTransport identifies the pulse-plain cloud evaluation transport.
	PulsePlainTransport = "ramesdk.pulse_plain.open-memory-v1"
	// LocalPlainTransport identifies the local plaintext evaluation transport.
	LocalPlainTransport = "ramesdk.local_plain.offline-v1"

	// 三种 100% 对称评测模式（独立隔离运行，互不清库、互不覆写）：
	//   - PulseModeLocalPlain  ("local_plain"):  本地明文离线引擎（SQLite FTS5/BM25 + 香农连续增益）
	//   - PulseModePulsePlain  ("pulse_plain"):  脉冲明文云端模式（独立 scope *-mem-eval-<suite>-pulse-plain[-<世代>]）
	//   - PulseModePulseCipher ("pulse_cipher"): 脉冲密文云端模式（独立 scope *-mem-eval-<suite>[-<世代>]）
	PulseModeLocalPlain  = ramesdk.ModeLocalPlain
	PulseModePulsePlain  = ramesdk.ModePulsePlain
	PulseModePulseCipher = ramesdk.ModePulseCipher

	defaultCloudPort     = "8081"
	defaultCloudEndpoint = "http://127.0.0.1:" + defaultCloudPort + "/open/memory/v1"
	defaultCloudUserID   = "10"
)

// NormalizePulseMode 归一化评测脉冲模式：仅接受 local_plain / pulse_plain / pulse_cipher；
// 空值默认 local_plain；其它值原样返回，由上层 fail-closed 拒绝（不再兼容历史别名）。
func NormalizePulseMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", PulseModeLocalPlain:
		return PulseModeLocalPlain
	case PulseModePulseCipher:
		return PulseModePulseCipher
	case PulseModePulsePlain:
		return PulseModePulsePlain
	default:
		return strings.ToLower(strings.TrimSpace(raw))
	}
}

// IsValidPulseMode reports whether raw is empty (default) or one of the three
// canonical pulse mode names. Legacy aliases are deliberately rejected.
func IsValidPulseMode(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", PulseModeLocalPlain, PulseModePulsePlain, PulseModePulseCipher:
		return true
	default:
		return false
	}
}

// IsValidEvalTransport 判定 manifest / summary 的 transport 是否属于合法的评测传输通道。
func IsValidEvalTransport(tr string) bool {
	switch strings.TrimSpace(tr) {
	case CloudTransport, PulsePlainTransport, LocalPlainTransport:
		return true
	default:
		return false
	}
}

// TransportForPulseMode maps the three canonical pulse modes onto their
// provenance transport labels. local_plain must never be reported as a cloud
// transport; pulse_plain is a distinct cloud path.
func TransportForPulseMode(raw string) string {
	switch NormalizePulseMode(raw) {
	case PulseModeLocalPlain:
		return LocalPlainTransport
	case PulseModePulsePlain:
		return PulsePlainTransport
	default:
		return CloudTransport
	}
}

// 盲态脉冲的切词器（三种运行模式里的 Cloud 臂，可选三种切词）：
//   - PulseTokenizerFTS5BM25（默认）：端侧结合本地 FTS5/BM25 词项流、128k Byte-Level BPE 先验自信息与
//     香农比例词干谐波（Exact + Stem + Morph 奇偶配对脉冲 + 日历月份同态谐波），排序 100% 由云端完成；
//   - PulseTokenizerStandard：SDK 原生表面词 + 子词/谐波切词（对照验证臂）；
//   - PulseTokenizerFTS5Trigram：按本地 FTS5 索引真实令牌流（`tokenize='trigram'` 的 3 字符滑窗）切词。
//
// 三臂都只消费云端返回的 UnitID：绝不与本地 FTS/BM25 结果做并集、融合或补足。
const (
	PulseTokenizerFTS5BM25    = "fts5-bm25"
	PulseTokenizerFTS5Trigram = "fts5-trigram"
	PulseTokenizerStandard    = "standard"
)

// 本地明文索引/召回面：
//   - product：唯一默认面，与用户日常使用的产品金库完全同源（langpack.ConfigureDefault
//     最大能力装配 + thinclient.RememberEpisodic 摄入 + thinclient.RecallWithOptions 召回）；
//   - eval：仅用于显式 `--pulse-tokenizer standard|fts5-trigram` 的对照切词实验。
const (
	LocalIndexSurfaceProduct = "product"
	LocalIndexSurfaceEval    = "eval"
)

// CloudEngineOption 是构造 CloudEngine 的可选项（保持既有调用点源码兼容）。
type CloudEngineOption func(*cloudEngineConfig)

type cloudEngineConfig struct {
	pulseTokenizer string
	pulseMode      string
}

// WithPulseTokenizer 指定盲态脉冲切词器（空/未知值回落到 FTS5BM25 默认臂）。
func WithPulseTokenizer(mode string) CloudEngineOption {
	return func(cfg *cloudEngineConfig) {
		cfg.pulseTokenizer = normalizePulseTokenizer(mode)
	}
}

// WithPulseMode 指定评测脉冲运行模式（local_plain | pulse_plain | pulse_cipher）。
func WithPulseMode(mode string) CloudEngineOption {
	return func(cfg *cloudEngineConfig) {
		cfg.pulseMode = NormalizePulseMode(mode)
	}
}

func normalizePulseTokenizer(mode string) string {
	low := strings.ToLower(strings.TrimSpace(mode))
	switch low {
	case PulseTokenizerStandard, "native", "default":
		return PulseTokenizerStandard
	case PulseTokenizerFTS5Trigram, "trigram":
		return PulseTokenizerFTS5Trigram
	case "en", "zh", "zh-cn", "zh-tw", "ja", "ko", "sea", "th", "lo", "km", "de", "nl", "sv", "multi":
		return low
	default:
		return PulseTokenizerFTS5BM25
	}
}

// packBPELexemeHarmonicSentence 将单句明文编码为全量最大能力脉冲帧：
// 启用全部客户端谐波表（common 128 + multi 208 全语种计量/统计单位表 + ISO 3166 L0-L3 地理表）
// 与全部规范外挂（128k BPE 先验宽度、Exact/Stem/Morph 奇偶配对词族谐波、内联粘连符号提取、
// 无空格 SEA 单位提取与 ISO 日历同态信标及英文自然月份对齐）。
func packBPELexemeHarmonicSentence(enc *ramesdk.BlindEncoder, sentence string, sentIdx int) []ramesdk.BlindTokenAtom {
	return en.PackSentence(enc, sentence, sentIdx)
}

// packLocalOfflineShannonSentence aliases the shared max-capability packer.
// Single source lives in internal/langpack so the product
// vault and the evaluator can never drift into two atomization schemes.
var packLocalOfflineShannonSentence = langpack.PackMaxCapabilitySentence

func applyPulseTokenizer(store *ramesdk.JSONFileStore, mode string) {
	if store == nil || store.BlindEncoder == nil {
		return
	}
	norm := normalizePulseTokenizer(mode)
	switch norm {
	case PulseTokenizerStandard:
		store.BlindEncoder.SetCanonicalHarmonics(ramesdk.CanonicalHarmonicOptions{})
		store.BlindEncoder.SetSegmenterOptions(ramesdk.SegmenterPackOptions{})
	case PulseTokenizerFTS5Trigram:
		store.BlindEncoder.SetCanonicalHarmonics(ramesdk.CanonicalHarmonicOptions{EnableStructuredDates: true})
		store.BlindEncoder.SetSegmenterOptions(ramesdk.SegmenterPackOptions{
			Segmenter:       ramesdk.FTS5TrigramSegmenter,
			HarmonicOverlay: true,
		})
	case "en", "zh", "zh-cn", "zh-tw", "ja", "ko", "sea", "th", "lo", "km", "de", "nl", "sv", "multi":
		store.BlindEncoder.SetCanonicalHarmonics(ramesdk.CanonicalHarmonicOptions{EnableStructuredDates: true})
		_ = langpack.Configure(store.BlindEncoder, norm)
	default:
		store.BlindEncoder.SetCanonicalHarmonics(ramesdk.CanonicalHarmonicOptions{EnableStructuredDates: true})
		store.BlindEncoder.SetSegmenterOptions(ramesdk.SegmenterPackOptions{
			Segmenter:          en.SegmentSentence,
			SentenceAtomPacker: packBPELexemeHarmonicSentence,
			HarmonicOverlay:    true,
		})
	}
}

// CloudDocument is one source-faithful benchmark unit sent through the public SDK.
//
// Beyond the four mandatory fields it may carry plaintext-free spatial/structural
// metadata (speaker, tri-temporal, 4D spatial coordinates) plus high-resolution free-block
// slices. All optional fields are omitempty: a document without them digests and
// packs exactly as before, so historical manifests keep their meaning.
//
// 一个 CloudDocument 恒定对应云端恰好一个 UnitID：高分辨率子块（SubUnits /
// FileParse）在打包父单元时并入其句帧，绝不另发子 UnitID——这样 /units 权威全集
// 与 corpus 一一相等（VerifyPrepared 精确通过、零 foreign unit），且 top_k 名额
// 不会被同一文档的子块重复挤占。
type CloudDocument struct {
	ID         string `json:"id"`
	Text       string `json:"text"`
	OccurredAt int64  `json:"occurred_at"`
	Layer      string `json:"layer"`
	// Author 是说话人/署名（仅用于本地盲态作者承诺 author_h，不发明文外泄）。
	Author string `json:"author,omitempty"`
	// NarratedAt 是叙述时刻（与 OccurredAt 分离的三时态之二）。
	NarratedAt int64 `json:"narrated_at,omitempty"`
	// DocKey / FileURL 标识同源物理文档（同文档切片共享同一条 DocHash 承诺）。
	DocKey  string `json:"doc_key,omitempty"`
	FileURL string `json:"file_url,omitempty"`
	// BlockKey / BlockID 标识文档内的逻辑块（条目级/章节级标识）。
	BlockKey string `json:"block_key,omitempty"`
	BlockID  string `json:"block_id,omitempty"`
	// LineStart / LineEnd 是该切片在源文档中的 1-based 物理行跨度。
	LineStart int `json:"line_start,omitempty"`
	LineEnd   int `json:"line_end,omitempty"`
	// FileParse 是解析器给出的附加文本（图片描述/观察/附件），随父单元一并编码。
	FileParse string `json:"file_parse,omitempty"`
	// SubUnits 是同一单元内的高分辨率自由块切片（结构化子字段），并入父单元句帧。
	SubUnits []ramesdk.SubUnit `json:"sub_units,omitempty"`
}

// UnitMeta 把 CloudDocument 的可选元数据转换为 SDK 的 UnitMeta。
// 空值保持为零值，只有真正提供的字段才会改变脉冲包（与历史行为逐位兼容）。
func (d CloudDocument) UnitMeta() ramesdk.UnitMeta {
	fileURL := strings.TrimSpace(d.FileURL)
	if fileURL == "" {
		fileURL = strings.TrimSpace(d.DocKey)
	}
	blockID := strings.TrimSpace(d.BlockID)
	if blockID == "" {
		blockID = strings.TrimSpace(d.BlockKey)
	}
	lineEnd := d.LineEnd
	if d.LineStart > 0 && lineEnd < d.LineStart {
		lineEnd = d.LineStart
	}
	return ramesdk.UnitMeta{
		Author:     strings.TrimSpace(d.Author),
		OccurredAt: d.OccurredAt,
		NarratedAt: d.NarratedAt,
		FileURL:    fileURL,
		BlockID:    blockID,
		LineStart:  d.LineStart,
		LineEnd:    lineEnd,
	}
}

// HasStructuredMeta reports whether the document carries any optional spatial,
// speaker, tri-temporal or free-block metadata beyond the four mandatory fields.
func (d CloudDocument) HasStructuredMeta() bool {
	if strings.TrimSpace(d.Author) != "" || d.NarratedAt > 0 || d.LineStart > 0 || d.LineEnd > 0 ||
		strings.TrimSpace(d.FileParse) != "" || len(d.SubUnits) > 0 {
		return true
	}
	return strings.TrimSpace(d.FileURL) != "" || strings.TrimSpace(d.DocKey) != "" ||
		strings.TrimSpace(d.BlockID) != "" || strings.TrimSpace(d.BlockKey) != ""
}

// PackedText 返回真正进入父单元脉冲的文本：源文本 + 解析附件 + 高分辨率自由块。
// 去重判据是纯字符串包含（不含任何语义词表），已出现在正文/前序块中的片段不重复追加。
func (d CloudDocument) PackedText() string {
	base := strings.TrimSpace(d.Text)
	blocks := make([]string, 0, len(d.SubUnits)+2)
	blocks = append(blocks, base)
	appendBlock := func(raw string) {
		text := strings.TrimSpace(raw)
		if text == "" {
			return
		}
		kept := make([]string, 0, len(blocks)+1)
		for _, existing := range blocks {
			if strings.Contains(existing, text) {
				// 已被现有块完整覆盖：不重复追加。
				return
			}
			if strings.Contains(text, existing) {
				// 新块覆盖了现有块：用新块替换，避免同段内容出现两次。
				continue
			}
			kept = append(kept, existing)
		}
		// 若多行块整体未出现，但其中部分行已存在于正文中（如 header 块含 Subject+Summary，而 Summary 已在正文），
		// 仅提取尚未出现在现有块中的新行追加，防止把已存在的长行（Location/Short Caption/Summary）重复编码两遍。
		if strings.Contains(text, "\n") {
			var novelLines []string
			for _, line := range strings.Split(text, "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				alreadyPresent := false
				for _, existing := range blocks {
					if strings.Contains(existing, line) {
						alreadyPresent = true
						break
					}
				}
				if !alreadyPresent {
					novelLines = append(novelLines, line)
				}
			}
			if len(novelLines) == 0 {
				return
			}
			text = strings.Join(novelLines, "\n")
		}
		blocks = append(kept, text)
	}
	appendBlock(d.FileParse)
	for _, sub := range d.SubUnits {
		if strings.TrimSpace(sub.Text) != "" {
			appendBlock(sub.Text)
			continue
		}
		appendBlock(sub.FileParse)
	}
	return strings.Join(blocks, "\n")
}

// CloudManifest proves which plaintext vault, tenant scope and corpus generation a run used.
type CloudManifest struct {
	Transport     string `json:"transport"`
	Endpoint      string `json:"endpoint"`
	TenantID      string `json:"tenant_id"`
	UserID        string `json:"user_id"`
	Scope         string `json:"scope"`
	Suite         string `json:"suite"`
	CorpusSHA256  string `json:"corpus_sha256"`
	Documents     int    `json:"documents"`
	Reused        bool   `json:"reused,omitempty"`
	LocalFallback bool   `json:"local_fallback"`
	// PurgedUnits 是本次「重算记忆」在重灌前清空的旧记忆条数（按 /units 权威全集分批删除）。
	// 0 = 该 scope 原本为空；只有 rebuild 路径才会写非零值。
	PurgedUnits int `json:"purged_units,omitempty"`
	// InsertedUnits 是本次 ingest 真实写入（全量重灌、断点补灌、正文自愈重灌合计）的单元数；
	// 暖库续灌命中封条且零补灌时为 0。审计据此区分「全量重灌」与「暖库复用」，
	// 不再以 corpus 大小冒充写入数。
	InsertedUnits int `json:"inserted_units,omitempty"`
	// Generation 是本语料所属的评测世代号（scope = <user>-mem-eval-<suite>-<generation>）。
	// 同一世代可承载多次批次读取；每次新评测（新语料重建）都分配独立世代。
	Generation string `json:"generation,omitempty"`
	// RetiredScopes 是本次回收的过期世代回执（含条数与残留错误；失败不阻塞本次运行）。
	RetiredScopes []EvalGenerationPurge `json:"retired_scopes,omitempty"`
	// PulseTokenizer 是本次使用的盲态脉冲切词臂（fts5-bm25 | standard）。缺省空值
	// 只出现在历史卷的 manifest 里（那时全链是 standard 单臂）。
	PulseTokenizer string `json:"pulse_tokenizer,omitempty"`
	// PulseMode 是本次使用的评测脉冲模式（local_plain | pulse_plain | pulse_cipher）。
	PulseMode string `json:"pulse_mode,omitempty"`
}

// CloudEngine is the benchmark-only strict SDK data plane. The JSON vault stores
// plaintext locally; ranking and memory computation are performed by the remote engine
// (or local Vault in PulseModeLocalPlain).
type CloudEngine struct {
	Client    *ramesdk.Client
	Store     *ramesdk.JSONFileStore
	Endpoint  string
	TenantID  string
	UserID    string
	Scope     string
	Suite     string
	StorePath string
	// Generation 是当前绑定的评测世代号（历史固定 scope 形态下为空）。
	Generation string
	// PulseTokenizer 是本臂使用的盲态脉冲切词器（fts5-bm25 | standard），随 manifest 落盘。
	PulseTokenizer string
	// PulseMode 是本引擎绑定的脉冲运行模式（local_plain | pulse_plain | pulse_cipher）。
	PulseMode string

	cfg *config.Config
	// retired 是本次回收的过期世代回执，随 manifest 落盘供审计。
	retired []EvalGenerationPurge
	// credentialFP 是本次凭据的不可逆短指纹（仅审计定位，绝不含密钥明文）。
	credentialFP string
	// ownsGeneration 与 ownerToken 标记本引擎实例是否拥有当前世代的独占写租约。
	ownsGeneration bool
	ownerToken     string

	identityOnce sync.Once
	identityErr  error

	localVaultMu      sync.Mutex
	localVaultDB      *sql.DB
	localVault        *thinclient.Vault
	localVaultCount   int
	zhaoQueryMu       sync.Mutex
	zhaoEngine        *rm.Engine
	zhaoTermExtractor *rm.TermExtractor
}

// ScopeForSuite derives a dedicated benchmark scope below the configured tenant.
func ScopeForSuite(suite, userID string) string {
	return ScopeForSuiteAndMode(suite, userID, PulseModePulseCipher)
}

// ScopeForSuiteAndMode derives a dedicated benchmark scope isolated by both suite and pulse mode:
//   - pulse_cipher: <user>-mem-eval-<locomo|atmbench>
//   - pulse_plain:  <user>-mem-eval-<locomo|atmbench>-pulse-plain
//   - local_plain:  <user>-mem-eval-<locomo|atmbench>-local-plain
//
// 三种模式拥有各自独立的 scope 命名空间，互不清库、互不干扰，可独立并行或交替运行。
func ScopeForSuiteAndMode(suite, userID, pulseMode string) string {
	part := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return '-'
	}, strings.TrimSpace(userID))
	part = strings.Trim(part, "-")
	if part == "" {
		part = defaultCloudUserID
	}
	numericTenant := true
	for _, r := range part {
		if !unicode.IsDigit(r) {
			numericTenant = false
			break
		}
	}
	if numericTenant {
		part = "u" + part
	}
	scopeSuite := strings.ToLower(strings.TrimSpace(suite))
	if scopeSuite == SuiteATM {
		scopeSuite = "atmbench"
	}
	base := part + "-mem-eval-" + scopeSuite
	switch NormalizePulseMode(pulseMode) {
	case PulseModePulsePlain:
		return base + "-pulse-plain"
	case PulseModeLocalPlain:
		return base + "-local-plain"
	default:
		return base
	}
}

// ScopeForGeneration derives this run's dedicated generation scope:
// <user>-mem-eval-<suite>-<generation>. 每次新评测独立 scope，天然与其它运行（含上一次评测
// 的残留）隔离；不带世代号时退化为固定 scope 的历史形态。
func ScopeForGeneration(suite, userID, generation string) string {
	return ScopeForModeGeneration(suite, userID, PulseModePulseCipher, generation)
}

// ScopeForModeGeneration derives a dedicated generation scope isolated by pulse mode:
//   - pulse_cipher: <user>-mem-eval-<suite>-<generation>
//   - pulse_plain:  <user>-mem-eval-<suite>-pulse-plain-<generation>
//   - local_plain:  <user>-mem-eval-<suite>-local-plain-<generation>
func ScopeForModeGeneration(suite, userID, pulseMode, generation string) string {
	base := ScopeForSuiteAndMode(suite, userID, pulseMode)
	segment := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return '-'
	}, strings.TrimSpace(generation))
	segment = strings.Trim(segment, "-")
	if segment == "" {
		return base
	}
	return base + "-" + segment
}

// SplitEvalScopeWithMode 解析评测 scope：返回 (suite, pulseMode, generation, ok)。
// 解析正本在 `internal/evalns`：产品侧（`internal/api`）用同一份判定把自动 L3 Noetic
// 结晶挡在评测空间之外，两边共用一份 grammar 才不会随代际漂移。
// 形态闭集：
//   - <prefix>-mem-eval-<locomo|atmbench>[-<世代号>]              => pulseMode = "pulse_cipher"
//   - <prefix>-mem-eval-<locomo|atmbench>-pulse-plain[-<世代号>]  => pulseMode = "pulse_plain"
//   - <prefix>-mem-eval-<locomo|atmbench>-local-plain[-<世代号>]  => pulseMode = "local_plain"
//
// 本函数只把线名 `atmbench` 映射回内部套件名 `atm`，其余语义一律委托。
func SplitEvalScopeWithMode(scope string) (string, string, string, bool) {
	suite, pulseMode, generation, ok := evalns.Split(scope)
	if !ok {
		return "", "", "", false
	}
	if suite == evalns.SuiteATMWire {
		suite = SuiteATM
	}
	return suite, pulseMode, generation, true
}

// SplitEvalScope 解析评测 scope：返回 (suite, generation, ok)。
// 保持向后兼容签名；模式轴可通过 EvalScopePulseMode 或 SplitEvalScopeWithMode 获取。
func SplitEvalScope(scope string) (string, string, bool) {
	suite, _, generation, ok := SplitEvalScopeWithMode(scope)
	return suite, generation, ok
}

// EvalScopePulseMode 解析评测 scope 所属的脉冲模式（pulse_cipher | pulse_plain | local_plain）。
func EvalScopePulseMode(scope string) string {
	_, mode, _, ok := SplitEvalScopeWithMode(scope)
	if !ok {
		return PulseModePulseCipher
	}
	return mode
}

// EvalScopeSuite 解析评测 scope 的套件（"locomo" / "atm"）。
func EvalScopeSuite(scope string) (string, bool) {
	suite, _, ok := SplitEvalScope(scope)
	return suite, ok
}

// EvalScopeGeneration 取评测 scope 的世代号（历史固定 scope 返回空串）。
func EvalScopeGeneration(scope string) string {
	_, generation, _ := SplitEvalScope(scope)
	return generation
}

// IsCloudEvalScope reports whether scope belongs to the isolated benchmark namespace.
func IsCloudEvalScope(scope string) bool {
	_, ok := EvalScopeSuite(scope)
	return ok
}

func cloudUserID() string {
	for _, key := range []string{"ROLLING_MEMORY_OPEN_MEMORY_USER", "ROLLING_MEMORY_USER_ID"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return defaultCloudUserID
}

func normalizeCloudEndpoint(raw string) string {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	if base == "" {
		return defaultCloudEndpoint
	}
	for _, suffix := range []string{"/open/memory/v1", "/brain/v1"} {
		if strings.HasSuffix(base, suffix) {
			return base
		}
	}
	if strings.HasSuffix(base, "/open/memory") {
		return base + "/v1"
	}
	return base + "/open/memory/v1"
}

func cloudEndpoint(cfg *config.Config) string {
	if value := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_BRAIN_URL")); value != "" {
		return normalizeCloudEndpoint(value)
	}
	if cfg != nil && strings.TrimSpace(cfg.Cloud.BaseURL) != "" {
		return normalizeCloudEndpoint(cfg.Cloud.BaseURL)
	}
	if accountURL, _, err := cloudcred.Ensure(context.Background()); err == nil && strings.TrimSpace(accountURL) != "" {
		return normalizeCloudEndpoint(accountURL)
	}
	return defaultCloudEndpoint
}

// cloudAPIKey 按显式程度解析评测凭据（Fail-Closed：三者皆无即报错，绝不回退任何编译内置密钥）：
//  1. 环境变量 ROLLING_MEMORY_BRAIN_KEY；
//  2. 显式配置 cloud.sync（cfg.SyncKey：APIKeyEnv / APIKeyFile）；
//  3. 本机账户态（cloudcred.Ensure：向本机控制面申请 / 续签的 mem2.* 数据面签名钥）。
//
// 端点参数只用于错误定位，不参与凭据选择；端点本身仍按 env → 配置 → 默认独立解析。
func cloudAPIKey(cfg *config.Config, endpoint string) (string, error) {
	if value := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_BRAIN_KEY")); value != "" {
		return value, nil
	}
	var cfgErr error
	if cfg != nil {
		value, err := cfg.SyncKey()
		if err == nil {
			if value = strings.TrimSpace(value); value != "" {
				return value, nil
			}
		} else {
			cfgErr = err
		}
	}
	_, accountKey, accountErr := cloudcred.Ensure(context.Background())
	if accountKey = strings.TrimSpace(accountKey); accountErr == nil && accountKey != "" {
		return accountKey, nil
	}
	if endpoint == "" {
		endpoint = defaultCloudEndpoint
	}
	if accountErr == nil {
		accountErr = errors.New("本机账户态未提供数据面签名钥")
	}
	if cfgErr != nil {
		return "", fmt.Errorf("cloud evaluation credential for %s is unresolved: set ROLLING_MEMORY_BRAIN_KEY, configure cloud.sync (%v), or sign in to the local control plane for a data-plane signed key (%v)", endpoint, cfgErr, accountErr)
	}
	return "", fmt.Errorf("cloud evaluation credential for %s is unresolved: set ROLLING_MEMORY_BRAIN_KEY, configure cloud.sync, or sign in to the local control plane for a data-plane signed key (%v)", endpoint, accountErr)
}

// evalVaultSecretFromKey 返回本地明文金库的盲化 pepper：对短 TTL 的 `mem2.<payload>.<sig>`
// 数据面签名钥，提取与续签 / 轮换无关的恒定租户身份 `rame-tenant-vault:<tid>:<uid>`；
// 非 mem2 静态密钥原样返回。委托 SDK 的单一实现，端侧不另写第二份 claims 解析。
func evalVaultSecretFromKey(apiKey string) string {
	return string(ramesdk.DeriveBlindPepper([]byte(strings.TrimSpace(apiKey))))
}

// evalKeyFingerprint 返回凭据的不可逆短指纹（SHA-256 前 4 字节 = 8 位十六进制），
// 只用于回执 / 诊断定位「本次用的是哪把钥、是否已轮换」，绝不落密钥明文。
func evalKeyFingerprint(apiKey string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(apiKey)))
	return hex.EncodeToString(sum[:4])
}

// NewCloudEngine constructs the strict SDK data plane for one benchmark suite.
func NewCloudEngine(cfg *config.Config, suite, storePath string, options ...CloudEngineOption) (*CloudEngine, error) {
	opt := cloudEngineConfig{
		pulseTokenizer: PulseTokenizerFTS5BM25,
		pulseMode:      NormalizePulseMode(os.Getenv("ROLLING_MEMORY_EVAL_PULSE_MODE")),
	}
	for _, apply := range options {
		if apply != nil {
			apply(&opt)
		}
	}
	if !IsValidPulseMode(opt.pulseMode) {
		return nil, fmt.Errorf("invalid pulse_mode %q (must be %q, %q, or %q)", opt.pulseMode, PulseModeLocalPlain, PulseModePulsePlain, PulseModePulseCipher)
	}
	pulseMode := NormalizePulseMode(opt.pulseMode)
	// 本地模式 = 纯本地发布面（零脉冲 · 零云端请求）：评测只允许 local_plain 臂。
	// 脉冲臂（pulse_plain / pulse_cipher）的协议、SDK 与引擎实现完整保留；
	// 但它们必须显式切换到 cloud 模式才能运行——此处先于任何端点/凭据解析 fail-closed，
	// 确保不会因为磁盘上残留凭据而把本地模式的评测打成远端请求。
	if pulseMode != PulseModeLocalPlain && cfg != nil &&
		strings.EqualFold(strings.TrimSpace(cfg.Mode), "local") {
		return nil, fmt.Errorf("本地模式仅支持 local_plain 评测臂（纯本地 · 零脉冲 · 零云端请求）；%s 需切换到 cloud 模式（脉冲协议与 SDK 实现保留）", pulseMode)
	}
	suite = strings.ToLower(strings.TrimSpace(suite))
	if suite != SuiteLoCoMo && suite != SuiteATM {
		return nil, fmt.Errorf("unsupported cloud evaluation suite %q", suite)
	}
	if strings.TrimSpace(storePath) == "" {
		return nil, errors.New("cloud evaluation requires an isolated plaintext store path")
	}
	userID := cloudUserID()
	endpoint := "local://sqlite-fts5-bm25"
	apiKey := "local-eval-vault-key-" + userID
	tenantID := "user"
	if pulseMode != PulseModeLocalPlain {
		endpoint = cloudEndpoint(cfg)
		var err error
		apiKey, err = cloudAPIKey(cfg, endpoint)
		if err != nil {
			return nil, err
		}
	}
	// 本地金库的盲化 pepper 取恒定租户身份（mem2.* 续签不改变它），使既有明文金库
	// 在签名钥轮换后仍与云端脉冲逐位对齐（不触发 rigid_vacuum 式假空）。
	store, err := ramesdk.NewJSONFileStore(storePath, []byte(evalVaultSecretFromKey(apiKey)))
	if err != nil {
		return nil, err
	}
	applyPulseTokenizer(store, opt.pulseTokenizer)
	client := ramesdk.NewClient(endpoint, apiKey, userID, store)
	client.Mode = pulseMode
	client.PlaintextMode = pulseMode == PulseModePulsePlain
	if pulseMode == PulseModeLocalPlain {
		client.Endpoint = ""
		client.TenantID = tenantID
	}
	client.Scope = ScopeForSuiteAndMode(suite, userID, pulseMode)
	client.HTTPClient = &http.Client{Timeout: 5 * time.Minute}
	// 评测必须锁死单一分身空间：引擎默认（cross_incarnation=nil）会把同租户下同 Assistant
	// 的兄弟 scope 以 0.78x 并入候选池，等于让并行评测套件 / 历史残留语料参与本卷排序，
	// 读数随同库无关分区的存在而漂移。隔离 scope 只能读自己。
	lockedSingleIncarnation := false
	client.CrossIncarnation = &lockedSingleIncarnation
	return &CloudEngine{
		Client: client, Store: store, Endpoint: endpoint, TenantID: client.TenantID, UserID: userID,
		Scope: client.Scope, Suite: suite, StorePath: storePath, PulseTokenizer: opt.pulseTokenizer,
		PulseMode: pulseMode, cfg: cfg,
		credentialFP: evalKeyFingerprint(apiKey),
	}, nil
}

// NewCloudEngineForScope reconstructs a suite data plane for an Admin/CLI recall worker.
// 接受评测命名空间内的任意 scope：历史固定 scope（u10-mem-eval-locomo）、明文/本地独立 scope
// 与世代 scope 都允许；套件与脉冲模式由 scope 自身解析，凭据与端点仍按配置解析。
func NewCloudEngineForScope(cfg *config.Config, scope, storePath string, options ...CloudEngineOption) (*CloudEngine, error) {
	suite, pulseMode, _, ok := SplitEvalScopeWithMode(scope)
	if !ok {
		return nil, fmt.Errorf("scope %q is not an isolated cloud evaluation scope", scope)
	}
	checkOpt := cloudEngineConfig{pulseMode: pulseMode}
	for _, apply := range options {
		if apply != nil {
			apply(&checkOpt)
		}
	}
	if !IsValidPulseMode(checkOpt.pulseMode) || NormalizePulseMode(checkOpt.pulseMode) != NormalizePulseMode(pulseMode) {
		return nil, fmt.Errorf("evaluation scope %q pulse mode %s does not match requested option mode %s", scope, pulseMode, checkOpt.pulseMode)
	}
	opts := append([]CloudEngineOption{WithPulseMode(pulseMode)}, options...)
	engine, err := NewCloudEngine(cfg, suite, storePath, opts...)
	if err != nil {
		return nil, err
	}
	if err := engine.BindScope(scope); err != nil {
		return nil, err
	}
	return engine, nil
}

// BindScope 把引擎绑定到指定评测 scope（必须属于同一套件的评测命名空间）。
func (e *CloudEngine) BindScope(scope string) error {
	if e == nil || e.Client == nil {
		return errors.New("cloud evaluation engine is not initialized")
	}
	suite, pulseMode, generation, ok := SplitEvalScopeWithMode(scope)
	if !ok {
		return fmt.Errorf("scope %q is not an isolated cloud evaluation scope", scope)
	}
	if e.Suite != "" && suite != e.Suite {
		return fmt.Errorf("evaluation scope %q does not belong to suite %q", scope, e.Suite)
	}
	if e.cfg != nil && strings.EqualFold(strings.TrimSpace(e.cfg.Mode), "local") && pulseMode != PulseModeLocalPlain {
		return fmt.Errorf("本地模式仅支持 local_plain 评测臂（纯本地 · 零脉冲 · 零云端请求）；%s 需切换到 cloud 模式", pulseMode)
	}
	if pulseMode != NormalizePulseMode(e.PulseMode) {
		return fmt.Errorf("evaluation scope %q pulse mode %s does not match engine mode %s", scope, pulseMode, e.PulseMode)
	}
	e.Client.Scope = scope
	e.Scope = scope
	e.Generation = generation
	e.PulseMode = pulseMode
	e.Client.Mode = pulseMode
	e.Client.PlaintextMode = pulseMode == PulseModePulsePlain
	return nil
}

// CheckHealth verifies the live remote engine and adopts its authoritative tenant identity once.
func (e *CloudEngine) CheckHealth(ctx context.Context) error {
	if e == nil || e.Client == nil {
		return errors.New("cloud evaluation engine is not initialized")
	}
	if NormalizePulseMode(e.PulseMode) == PulseModeLocalPlain {
		if e.TenantID == "" {
			e.TenantID = "user"
		}
		e.Client.TenantID = e.TenantID
		return nil
	}
	e.identityOnce.Do(func() {
		if err := e.Client.Health(ctx); err != nil {
			e.identityErr = err
			return
		}
		identity, err := e.Client.VerifyAuth(ctx)
		if err != nil {
			e.identityErr = fmt.Errorf("cloud evaluation auth verification failed: %w", err)
			return
		}
		if identity.TenantID == "" {
			e.identityErr = errors.New("cloud evaluation auth verification returned an empty tenant_id")
			return
		}
		if identity.LockedUserID != "" && e.UserID != identity.LockedUserID && !strings.HasPrefix(e.UserID, identity.LockedUserID+"-") {
			e.identityErr = fmt.Errorf("cloud evaluation user_id %q is outside the locked user namespace", e.UserID)
			return
		}
		allowed := false
		for _, prefix := range identity.UserIDPrefixes {
			prefix = strings.TrimSpace(prefix)
			if prefix != "" && (e.UserID == prefix || strings.HasPrefix(e.UserID, prefix+"-")) {
				allowed = true
				break
			}
		}
		if len(identity.UserIDPrefixes) == 0 && !identity.IsManager {
			allowed = e.UserID == identity.TenantID || strings.HasPrefix(e.UserID, identity.TenantID+"-")
		}
		if !allowed && !identity.IsManager {
			e.identityErr = fmt.Errorf("cloud evaluation user_id %q is not authorized for tenant %q", e.UserID, identity.TenantID)
			return
		}
		e.Client.TenantID = identity.TenantID
		e.TenantID = identity.TenantID
	})
	return e.identityErr
}

// Metadata returns non-secret runtime provenance persisted with every summary.
func (e *CloudEngine) Metadata() map[string]any {
	meta := map[string]any{
		"transport":              TransportForPulseMode(e.PulseMode),
		"endpoint":               e.Endpoint,
		"tenant_id":              e.TenantID,
		"user_id":                e.UserID,
		"scope":                  e.Scope,
		"generation":             e.Generation,
		"suite":                  e.Suite,
		"store_path":             e.StorePath,
		"credential_fingerprint": e.credentialFP,
		"pulse_tokenizer":        normalizePulseTokenizer(e.PulseTokenizer),
		"pulse_mode":             NormalizePulseMode(e.PulseMode),
		"local_fallback":         false,
	}
	if e.localIndexSurface() == LocalIndexSurfaceProduct {
		language := evalLangpackName()
		indexSurface := "product-maxcap"
		switch {
		case strings.EqualFold(language, "off"):
			indexSurface = "product-plain"
		case !strings.EqualFold(language, langpack.MaxCapabilityLanguage):
			indexSurface = "product-langpack-" + language
		}
		meta["local_index_surface"] = indexSurface
		meta["local_recall_surface"] = "thinclient.recall-with-options"
		meta["product_path_equivalent"] = true
	} else if NormalizePulseMode(e.PulseMode) == PulseModeLocalPlain {
		// 历史评测诊断面：与产品面同一 max-capability packer，但走直接插入 + 直接
		// SearchClientSDKDefault，仅用于对照，不得当作产品成绩。
		meta["local_index_surface"] = "eval-" + normalizePulseTokenizer(e.PulseTokenizer)
		meta["local_recall_surface"] = "vault.search-client-sdk-default"
		meta["product_path_equivalent"] = false
	}
	return meta
}

// CloudCorpusDigest returns the canonical digest of source-faithful cloud documents.
func CloudCorpusDigest(documents []CloudDocument) (string, error) {
	corpusJSON, err := json.Marshal(documents)
	if err != nil {
		return "", fmt.Errorf("cloud evaluation corpus digest failed: %w", err)
	}
	digest := sha256.Sum256(corpusJSON)
	return hex.EncodeToString(digest[:]), nil
}

func (e *CloudEngine) validateManifestPulseMode(manifest CloudManifest) error {
	if strings.TrimSpace(manifest.PulseMode) == "" || !IsValidPulseMode(manifest.PulseMode) || manifest.PulseMode != e.PulseMode {
		return fmt.Errorf("cloud evaluation manifest pulse mode %q does not match the active client %q (rebuild required)",
			manifest.PulseMode, e.PulseMode)
	}
	return nil
}

// VerifyPrepared proves exact manifest, corpus and remote-unit-set equality.
func (e *CloudEngine) VerifyPrepared(ctx context.Context, documents []CloudDocument) (CloudManifest, error) {
	manifest, err := ReadCloudManifest(e.StorePath)
	if err != nil {
		return manifest, err
	}
	if err := e.validateManifestPulseMode(manifest); err != nil {
		return manifest, err
	}
	if e.Generation == "" && manifest.Scope != "" && manifest.Suite == e.Suite {
		_, _ = e.adoptStoredGeneration()
	}
	digest, err := CloudCorpusDigest(documents)
	if err != nil {
		return manifest, err
	}
	if manifest.Scope != e.Scope || manifest.Suite != e.Suite || manifest.CorpusSHA256 != digest || manifest.Documents != len(documents) {
		return manifest, errors.New("cloud evaluation manifest does not match the requested corpus")
	}
	if len(documents) == 0 {
		return manifest, errors.New("cloud evaluation cannot reuse an empty corpus")
	}
	if err := e.CheckHealth(ctx); err != nil {
		return manifest, err
	}
	if manifest.Endpoint != e.Endpoint || manifest.TenantID != e.TenantID || manifest.UserID != e.UserID {
		return manifest, errors.New("cloud evaluation manifest endpoint/tenant/user does not match the active engine")
	}
	// 切词臂必须与金库脉冲逐位同源：UnitID 相同但脉冲词项不同（fts5-bm25 vs standard）
	// 时复用会静默用错词表检索，必须 fail-closed 要求重建。历史卷 manifest 无该字段
	// （当年全链 standard 单臂），按 standard 对账。
	manifestTokenizer := strings.TrimSpace(manifest.PulseTokenizer)
	if manifestTokenizer == "" {
		manifestTokenizer = PulseTokenizerFTS5BM25
	}
	if normalizePulseTokenizer(manifestTokenizer) != normalizePulseTokenizer(e.PulseTokenizer) {
		return manifest, fmt.Errorf("cloud evaluation manifest pulse tokenizer %q does not match the active client %q (rebuild required)",
			manifestTokenizer, normalizePulseTokenizer(e.PulseTokenizer))
	}
	// 世代在用：读者心跳续期（Admin 的 prepare 探针也走这里），保证并发回收不会清掉正在读的世代。
	touchEvalGeneration(e.Scope, evalGenerationRoleReader)
	var remoteIDs []string
	if NormalizePulseMode(e.PulseMode) == PulseModeLocalPlain {
		remoteIDs = e.localStoredUnitIDs()
	} else {
		remoteIDs, err = e.Client.ListCloudUnitIDs(ctx)
		if err != nil {
			return manifest, fmt.Errorf("cloud evaluation full-scope verification failed: %w", err)
		}
	}
	if len(remoteIDs) != len(documents) {
		return manifest, fmt.Errorf("cloud evaluation remote unit count %d does not match corpus %d", len(remoteIDs), len(documents))
	}
	expected := make(map[string]bool, len(documents))
	for _, doc := range documents {
		if expected[doc.ID] {
			return manifest, fmt.Errorf("cloud evaluation corpus contains duplicate unit ID %q", doc.ID)
		}
		expected[doc.ID] = true
	}
	for _, id := range remoteIDs {
		if !expected[id] {
			return manifest, fmt.Errorf("cloud evaluation remote scope contains foreign unit %q", id)
		}
		delete(expected, id)
	}
	if len(expected) != 0 {
		return manifest, fmt.Errorf("cloud evaluation remote scope is missing %d corpus units", len(expected))
	}
	if NormalizePulseMode(e.PulseMode) == PulseModeLocalPlain {
		// ID 集合相等只证明「有哪些单元」，不证明「单元正文是什么」；本地索引由 store 正文
		// 重建，正文被替换时代码会静默用错语料检索，必须在这里 fail-closed。
		if err := e.verifyLocalPlainStoreBodies(documents); err != nil {
			return manifest, err
		}
	}
	manifest.Reused = true
	return manifest, nil
}

// Prepare 选择本次评测的世代，并保证云端语料与请求语料完全一致：
//   - 本 store 既有 manifest 的 scope 是「这个 store 当前是谁」的真源，先采纳；
//   - 该世代在云端已被清空（/units = 0）⇒ 复用无意义，自动新开独立世代并全量重灌
//     （「新测评清旧记忆、建新 scope」；这也修掉了「本地金库还在、云端已空 ⇒ 静默 0 命中」的旧洞）；
//   - 显式 reuse 时按 manifest + 语料摘要 + 远端 Unit 全集三者精确相等复用；不相等一律 fail-closed，
//     绝不盲灌（网络抖动不得被读成空库）；
//   - rebuild 永远新开世代：不动任何在用世代，过期世代按心跳周期自动回收。
func (e *CloudEngine) Prepare(ctx context.Context, documents []CloudDocument, rebuild, reuse bool, workers int, logf func(string, ...any)) (CloudManifest, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if len(documents) == 0 {
		return CloudManifest{}, errors.New("cloud evaluation requires at least one source document")
	}
	if !rebuild {
		manifest, err := ReadCloudManifest(e.StorePath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return manifest, err
		}
		if err == nil {
			if err := e.validateManifestPulseMode(manifest); err != nil {
				return manifest, err
			}
		}
		if adopted, ok := e.adoptStoredGeneration(); ok {
			empty, err := e.remoteScopeEmpty(ctx)
			if err != nil {
				return CloudManifest{}, fmt.Errorf("云端评测世代探测失败（不盲灌、不复用）: %w", err)
			}
			if !empty {
				// 世代在用：读者心跳续期，保证并发回收不会把正在读的世代清掉。
				touchEvalGeneration(e.Scope, evalGenerationRoleReader)
				if reuse {
					manifest, err := e.VerifyPrepared(ctx, documents)
					if err == nil {
						manifest.Reused = true
						manifest.RetiredScopes = nil
						return manifest, nil
					}
					return manifest, fmt.Errorf("cloud evaluation reuse verification failed; refusing blind re-ingest: %w", err)
				}
				// 暖库续灌的正文完整性闸：续灌只按「ID 是否存在」补缺，从不改写已存在单元；
				// store 正文一旦被替换（手工编辑、脚本、历史语料残留），索引会静默用错语料检索。
				// 这里按语料逐单元校验正文，被替换的单元删除后交回正常续灌重灌（写入侧同一
				// SDK 拼装函数，重灌即自愈）；语料外的孤儿单元 fail-closed 要求 --rebuild。
				if NormalizePulseMode(e.PulseMode) == PulseModeLocalPlain {
					if err := e.repairLocalPlainStoreBodies(documents, logf); err != nil {
						return CloudManifest{}, err
					}
				}
				return e.Ingest(ctx, documents, false, workers, logf)
			}
			logf("[eval] 世代 %s 在云端为空（旧记忆已回收）：本次评测重建新世代", adopted.Scope)
		}
	}
	if err := e.startNewGeneration(ctx, logf); err != nil {
		return CloudManifest{}, err
	}
	manifest, err := e.Ingest(ctx, documents, true, workers, logf)
	if err != nil {
		e.finishGeneration()
	}
	return manifest, err
}

// retryTransient 以指数退避执行一次写入尝试：只重试瞬时传输类故障，
// 鉴权/scope/未知 Unit/破坏性闸门等契约与策略错误原样上抛（fail-closed）。
func retryTransient(ctx context.Context, attempt func(ctx context.Context) error) error {
	const maxAttempts = 8
	delay := 200 * time.Millisecond
	var err error
	for try := 1; try <= maxAttempts; try++ {
		err = attempt(ctx)
		if err == nil {
			return nil
		}
		if !isRetryableUpstreamError(err) || try == maxAttempts {
			break
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		delay *= 2
		if delay > 5*time.Second {
			delay = 5 * time.Second
		}
	}
	return err
}

// rememberWithRetry 在引擎限流或瞬时不可用（HTTP 429/503、连接拒绝/重置、EOF）时按指数退避重试。
// 语料 ingest 是多 worker 并发写穿；引擎侧有 IP×URL 令牌桶限流，引擎重启也会短暂拒连。
// 单次瞬时故障不应让整份语料准备失败；鉴权/scope/未知 Unit 等语义错误一律原样上抛（fail-closed）。
func rememberWithRetry(ctx context.Context, client *ramesdk.Client, id, text string, occurredAt int64, layer string) error {
	return retryTransient(ctx, func(ctx context.Context) error {
		_, err := client.RememberWithLayer(ctx, id, text, occurredAt, layer)
		return err
	})
}

// rememberDocumentWithRetry 写入一个 CloudDocument：恒发一个父单元。
//   - 无结构化元数据 ⇒ 走 RememberWithLayer（与历史逐位一致）；
//   - 有元数据 ⇒ 走 RememberWithMeta，把说话人/三时态/四维空间坐标
//     （doc_h / block_h / line_start / line_end）与高分辨率自由块一并编入同一脉冲包。
func rememberDocumentWithRetry(ctx context.Context, client *ramesdk.Client, doc CloudDocument) error {
	layer := strings.TrimSpace(doc.Layer)
	if layer == "" {
		layer = ramesdk.LayerEpisodic
	}
	text := doc.PackedText()
	if !doc.HasStructuredMeta() {
		return rememberWithRetry(ctx, client, doc.ID, text, doc.OccurredAt, layer)
	}
	meta := doc.UnitMeta()
	return retryTransient(ctx, func(ctx context.Context) error {
		_, err := client.RememberWithMeta(ctx, doc.ID, text, layer, meta)
		return err
	})
}

// deleteUnitsWithRetry 与 rememberWithRetry 同口径：rebuild 的清库按 100 条一批连续调用，
// 引擎令牌桶会直接 429；瞬时故障重试，语义错误（越权/未知 Unit）原样上抛。
func deleteUnitsWithRetry(ctx context.Context, client *ramesdk.Client, unitIDs []string) (ramesdk.CloudPurgeResponse, error) {
	const maxAttempts = 8
	delay := 200 * time.Millisecond
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, err := client.DeleteUnits(ctx, unitIDs)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !isRetryableUpstreamError(err) || attempt == maxAttempts {
			break
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return resp, ctx.Err()
		case <-timer.C:
		}
		delay *= 2
		if delay > 5*time.Second {
			delay = 5 * time.Second
		}
	}
	return ramesdk.CloudPurgeResponse{}, lastErr
}

// evalDestructiveGateMarker 是数据面「破坏性操作闸门」的拒绝签名：signed 模式下 purge /
// rebuild / calibrate 等不可逆操作要求控制面撤销清单新鲜（引擎 server/auth.go 的
// guardDestructive）。这是策略性拒绝（控制面责任面），不是瞬时故障：必须快速失败并把
// 解锁条件说清楚，不做退避重试、也不绕过。
const evalDestructiveGateMarker = "revocation list stale"

// evalPurgeBatchSize 是 scope 清空的单批 Unit 数（对齐引擎 purge `mode:"units"` 单批闭集上限 100）：
// 每次 purge 在引擎侧会触发一次状态持久化，单批取满可把写盘次数压到最少。
const evalPurgeBatchSize = 100

// PurgeScopeUnits 清空本引擎 scope 内的全部旧记忆（「每次运行都重算记忆」的清库半边）：
//   - 以 /units 的 scope 全量为权威清单（引擎侧真实持有的 Unit 集合），不做任何猜测，也不借用
//     /graph 局部节点或单条命中替代全集；
//   - 分批删除 + 全局节拍 + 瞬时故障重试（delete-units 免销毁票据；scope / tenant_all 全删
//     才需要 pt2.* 票据，本条不走票据路径）；
//   - 语义错误（越权 / 未知 Unit / 空间回显不符 / 破坏性闸门拒绝）一律原样上抛 fail-closed；
//     删除成功由 SDK 同步清理本地明文金库对应文档（云端成功后才删本地）。
//
// 返回实际删除的 Unit 数；scope 原本为空时返回 0（幂等）。
func (e *CloudEngine) PurgeScopeUnits(ctx context.Context, logf func(string, ...any)) (int, error) {
	if e == nil || e.Client == nil {
		return 0, errors.New("cloud evaluation engine is not initialized")
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if NormalizePulseMode(e.PulseMode) == PulseModeLocalPlain {
		e.localVaultMu.Lock()
		if e.localVaultDB != nil {
			_ = e.localVaultDB.Close()
			e.localVaultDB = nil
			e.localVault = nil
			e.localVaultCount = 0
		}
		_ = os.Remove(e.StorePath + ".local-vault.sqlite")
		_ = os.Remove(e.StorePath + ".local-vault.sqlite.meta.json")
		e.localVaultMu.Unlock()
		unitIDs := e.localStoredUnitIDs()
		deleted := e.Store.DeleteLocalUnits(unitIDs)
		return deleted, nil
	}
	if err := e.CheckHealth(ctx); err != nil {
		return 0, fmt.Errorf("cloud evaluation health check failed: %w", err)
	}
	deleted, err := purgeScopeUnitsWithClient(ctx, e.Client, e.Scope, logf)
	if err != nil {
		return deleted, fmt.Errorf("cloud evaluation scope purge failed after %d units: %w",
			deleted, describeDestructiveGate(err))
	}
	return deleted, nil
}

// purgeScopeUnitsWithClient 是清库原语：按 /units 权威全集分批删除某个 scope 的全部 Unit。
// 既服务「本世代重算」的清库，也服务对过期世代的回收（后者用另一个 scope 的客户端）。
func purgeScopeUnitsWithClient(ctx context.Context, client *ramesdk.Client, scope string, logf func(string, ...any)) (int, error) {
	if client == nil {
		return 0, errors.New("cloud evaluation engine is not initialized")
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	unitIDs, err := client.ListCloudUnitIDs(ctx)
	if err != nil {
		return 0, fmt.Errorf("cloud evaluation scope purge requires the authoritative /units set: %w", err)
	}
	if len(unitIDs) == 0 {
		logf("[eval] scope %s is already empty; nothing to purge", scope)
		return 0, nil
	}
	deleted := 0
	paceC, stopPace := evalIngestPaceGate()
	defer stopPace()
	for start := 0; start < len(unitIDs); start += evalPurgeBatchSize {
		end := start + evalPurgeBatchSize
		if end > len(unitIDs) {
			end = len(unitIDs)
		}
		if waitErr := waitIngestPace(ctx, paceC); waitErr != nil {
			return deleted, waitErr
		}
		resp, err := deleteUnitsWithRetry(ctx, client, unitIDs[start:end])
		if err != nil {
			return deleted, fmt.Errorf("purge failed after %d/%d units: %w", deleted, len(unitIDs), err)
		}
		deleted += int(resp.DeletedUnits)
	}
	logf("[eval] scope %s purged: %d/%d units deleted before full re-ingest", scope, deleted, len(unitIDs))
	return deleted, nil
}

// describeDestructiveGate 把破坏性闸门拒绝翻译成可执行文案（含解锁条件）；非该类错误原样返回。
func describeDestructiveGate(err error) error {
	if err == nil || !strings.Contains(err.Error(), evalDestructiveGateMarker) {
		return err
	}
	return fmt.Errorf("云端拒绝销毁操作（数据面破坏性闸门：revocation list stale）：signed 模式下数据面要求撤销清单新鲜，"+
		"需控制面在数据面轮询地址提供 GET /open/memory/v1/auth/revocations 的 rev2.* 签名清单并被数据面轮询到后重试；"+
		"客户端不绕过该闸门（原始错误：%w）", err)
}

// isRetryableUpstreamError 只识别瞬时传输类故障（限流/暂时不可用/连接级错误）；
// 鉴权、scope 越界、未知 Unit、破坏性闸门拒绝等契约/策略错误不得重试。
func isRetryableUpstreamError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	if strings.Contains(msg, evalDestructiveGateMarker) {
		return false
	}
	for _, marker := range []string{
		"HTTP 429", "HTTP 503", "请求频繁",
		"connection refused", "connection reset", "unexpected EOF", "EOF",
		"Client.Timeout exceeded",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// evalResumeState 是本地明文金库的 engine/corpus 身份封条：只有 endpoint、scope 与
// 语料摘要都与当前准备完全一致时，才允许按本地已有文档跳过 ingest。
// 缺少该封条或身份不一致（例如切换了云端引擎）时必须全量重灌，
// 否则会写出一份「documents=N 但云端只有 N-k 条」的假回执。
type evalResumeState struct {
	Endpoint     string `json:"endpoint"`
	Scope        string `json:"scope"`
	CorpusSHA256 string `json:"corpus_sha256"`
	Documents    int    `json:"documents"`
}

func evalResumeStatePath(storePath string) string {
	return storePath + ".resume.json"
}

func readEvalResumeState(storePath string) evalResumeState {
	var state evalResumeState
	raw, err := os.ReadFile(evalResumeStatePath(storePath))
	if err != nil {
		return state
	}
	_ = json.Unmarshal(raw, &state)
	return state
}

func writeEvalResumeState(storePath string, state evalResumeState) {
	_ = writeJSON(evalResumeStatePath(storePath), state)
}

// Ingest writes source-faithful units through the SDK, optionally replacing only
// the explicit unit IDs belonging to this corpus and isolated scope.
func (e *CloudEngine) Ingest(ctx context.Context, documents []CloudDocument, rebuild bool, workers int, logf func(string, ...any)) (CloudManifest, error) {
	manifest := CloudManifest{
		Transport: TransportForPulseMode(e.PulseMode), Endpoint: e.Endpoint, UserID: e.UserID,
		Scope: e.Scope, Suite: e.Suite, Documents: len(documents), LocalFallback: false,
		Generation: e.Generation, RetiredScopes: e.retired,
		PulseTokenizer: normalizePulseTokenizer(e.PulseTokenizer),
		PulseMode:      NormalizePulseMode(e.PulseMode),
	}
	if len(documents) == 0 {
		return manifest, errors.New("cloud evaluation requires at least one source document")
	}
	corpusDigest, err := CloudCorpusDigest(documents)
	if err != nil {
		return manifest, err
	}
	manifest.CorpusSHA256 = corpusDigest
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if err := e.CheckHealth(ctx); err != nil {
		return manifest, fmt.Errorf("cloud evaluation health check failed: %w", err)
	}
	manifest.TenantID = e.TenantID
	if rebuild && len(documents) > 0 {
		// 「每次运行都重算记忆」：rebuild = 先清空该 scope 的全部旧记忆（历次运行 / 历份语料的残留
		// 一并清掉），再全量重灌当前语料。只删“本语料自己的 Unit ID”会把历史残留留在 scope 里，
		// 污染后续检索。清库前先撤销续灌封条：本地金库与云端的对应关系即刻失效，
		// 避免中途失败后下一次 Prepare 误用已失效的本地集合跳过 ingest。
		// 世代 scope 下这本就是新 scope（构造上为空）；此步是「复核并给回执」而不是猜测。
		_ = os.Remove(evalResumeStatePath(e.StorePath))
		purged, err := e.PurgeScopeUnits(ctx, logf)
		if err != nil {
			return manifest, fmt.Errorf("cloud evaluation isolated scope reset failed: %w", err)
		}
		manifest.PurgedUnits = purged
	}
	if workers < 1 {
		workers = 1
	}
	// 断点续灌：仅当本地金库封条（endpoint + scope + 语料摘要）与当前准备一致时，
	// 才把本地已有文档视为已成功写穿（SDK 只在云端 ingest 成功后落本地），只补缺失子集。
	// 引擎重启/限流导致的中断因此可以低成本续跑；切换引擎或语料变化则必须全量重灌。
	resumeState := evalResumeState{
		Endpoint: e.Endpoint, Scope: e.Scope,
		CorpusSHA256: manifest.CorpusSHA256, Documents: len(documents),
	}
	resumable := false
	if !rebuild {
		previous := readEvalResumeState(e.StorePath)
		resumable = previous.Endpoint == resumeState.Endpoint &&
			previous.Scope == resumeState.Scope &&
			previous.CorpusSHA256 == resumeState.CorpusSHA256
	}
	targets := documents
	if resumable {
		pending := make([]CloudDocument, 0, len(documents))
		for _, doc := range documents {
			if _, _, _, lookupErr := e.Store.LoadLocalDocument(doc.ID); lookupErr == nil {
				continue
			}
			pending = append(pending, doc)
		}
		if len(pending) < len(documents) {
			logf("[eval] resume local vault: %d/%d documents already present, ingesting %d",
				len(documents)-len(pending), len(documents), len(pending))
		}
		targets = pending
	}
	paceC, stopPace := evalIngestPaceGate()
	defer stopPace()
	errs := ParallelMap(len(targets), workers, func(index int) error {
		if waitErr := waitIngestPace(ctx, paceC); waitErr != nil {
			return waitErr
		}
		doc := targets[index]
		if strings.TrimSpace(doc.ID) == "" || strings.TrimSpace(doc.Text) == "" {
			return fmt.Errorf("invalid cloud evaluation document at index %d", index)
		}
		// 长时 ingest 期间持续续期世代租约：并发的「清旧记忆」据此判定本世代在用而跳过。
		if index%evalGenerationHeartbeatEveryDocs == 0 {
			touchEvalGeneration(e.Scope, evalGenerationRoleWriter)
		}
		return rememberDocumentWithRetry(ctx, e.Client, doc)
	})
	for index, err := range errs {
		if err != nil {
			_ = e.Store.Flush()
			// 失败也要封条：本地金库此刻确实持有本引擎 × 本语料的部分文档，重试方可续灌。
			writeEvalResumeState(e.StorePath, resumeState)
			return manifest, fmt.Errorf("cloud evaluation ingest %q failed: %w", targets[index].ID, err)
		}
	}
	// inserted_units = 真实写入数（全量重灌 = len(documents)；断点续灌 = 缺失补灌数；
	// 正文自愈删除的单元会回到 pending，因此同样计入 targets）。
	manifest.InsertedUnits = len(targets)
	if err := e.Store.Flush(); err != nil {
		return manifest, fmt.Errorf("cloud evaluation plaintext vault flush failed: %w", err)
	}
	writeEvalResumeState(e.StorePath, resumeState)
	if err := writeJSON(e.StorePath+".manifest.json", manifest); err != nil {
		return manifest, err
	}
	// 租约登记本世代语料身份：Admin/审计据此判断「这个 scope 里装的是哪份语料、有多少条」。
	recordGenerationCorpus(e.Scope, e.StorePath, manifest)
	logf("[eval] SDK ingest complete (%s): suite=%s scope=%s documents=%d", NormalizePulseMode(e.PulseMode), e.Suite, e.Scope, len(documents))
	return manifest, nil
}

func (e *CloudEngine) localStoredUnitIDs() []string {
	if e == nil || e.Store == nil {
		return nil
	}
	raw, err := os.ReadFile(e.StorePath)
	if err != nil {
		return nil
	}
	var docs map[string]json.RawMessage
	if json.Unmarshal(raw, &docs) != nil {
		return nil
	}
	out := make([]string, 0, len(docs))
	for id := range docs {
		out = append(out, id)
	}
	return out
}

// verifyLocalPlainStoreBodies 证明本地 store 中影响索引的正文与请求语料逐字一致。
// ID 集合相等会接受「ID 不变、正文被替换」的 store，而 ensureLocalPlainVault 正是
// 用 store 正文重建 SQLite 索引；期望正文按写入时同一 SDK 拼装函数
// （FormatPlaintextWithMeta(PackedText, UnitMeta)）重建，避免另造一套变换。
func (e *CloudEngine) verifyLocalPlainStoreBodies(documents []CloudDocument) error {
	raw, err := os.ReadFile(e.StorePath)
	if err != nil {
		return fmt.Errorf("cloud evaluation local store unreadable: %w", err)
	}
	var stored map[string]struct {
		DocID      string `json:"doc_id"`
		OccurredAt int64  `json:"occurred_at"`
		RawText    string `json:"raw_text"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		return fmt.Errorf("cloud evaluation local store decode failed: %w", err)
	}
	if len(stored) != len(documents) {
		return fmt.Errorf("cloud evaluation local store has %d units, corpus has %d", len(stored), len(documents))
	}
	for _, doc := range documents {
		rec, ok := stored[doc.ID]
		if !ok {
			return fmt.Errorf("cloud evaluation local store is missing unit %q", doc.ID)
		}
		if rec.DocID != "" && rec.DocID != doc.ID {
			return fmt.Errorf("cloud evaluation local store unit %q carries doc_id %q", doc.ID, rec.DocID)
		}
		meta := doc.UnitMeta()
		occurredAt := meta.OccurredAt
		if occurredAt <= 0 && meta.NarratedAt > 0 {
			occurredAt = meta.NarratedAt
		}
		if rec.OccurredAt != occurredAt {
			return fmt.Errorf("cloud evaluation local store unit %q occurred_at %d does not match corpus %d",
				doc.ID, rec.OccurredAt, occurredAt)
		}
		// 写入侧有两条路径：无结构化元数据时 store 正文就是 PackedText；
		// 有元数据时才经 FormatPlaintextWithMeta 追加日期/作者/块前缀。
		expected := doc.PackedText()
		if doc.HasStructuredMeta() {
			expected = ramesdk.FormatPlaintextWithMeta(expected, meta)
		}
		if rec.RawText != expected {
			return fmt.Errorf("cloud evaluation local store unit %q raw_text does not match corpus body", doc.ID)
		}
	}
	return nil
}

// repairLocalPlainStoreBodies 是暖库续灌路径的正文完整性闸（verifyLocalPlainStoreBodies 的自愈变体）。
// 断点续灌按「ID 是否存在」补缺，从不改写已存在单元；store 正文一旦被替换（手工编辑、脚本、
// 历史语料残留），续灌会静默用错语料检索。这里按语料逐单元校验正文：被替换的单元从 store
// 删除并交回正常续灌重灌（写回侧是同一 SDK 拼装函数，重灌即自愈）；缺单元属断点续灌常态，
// 照常放行；语料外的孤儿单元不属于本语料，fail-closed 要求 --rebuild，绝不静默带入索引。
func (e *CloudEngine) repairLocalPlainStoreBodies(documents []CloudDocument, logf func(string, ...any)) error {
	raw, err := os.ReadFile(e.StorePath)
	if err != nil {
		return fmt.Errorf("cloud evaluation local store unreadable: %w", err)
	}
	var stored map[string]struct {
		DocID      string `json:"doc_id"`
		OccurredAt int64  `json:"occurred_at"`
		RawText    string `json:"raw_text"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		return fmt.Errorf("cloud evaluation local store decode failed: %w", err)
	}
	inCorpus := make(map[string]bool, len(documents))
	tampered := make([]string, 0)
	for _, doc := range documents {
		inCorpus[doc.ID] = true
		rec, ok := stored[doc.ID]
		if !ok {
			continue // 缺单元：断点续灌本来就会补灌
		}
		bad := rec.DocID != "" && rec.DocID != doc.ID
		meta := doc.UnitMeta()
		occurredAt := meta.OccurredAt
		if occurredAt <= 0 && meta.NarratedAt > 0 {
			occurredAt = meta.NarratedAt
		}
		if !bad && rec.OccurredAt != occurredAt {
			bad = true
		}
		if !bad {
			expected := doc.PackedText()
			if doc.HasStructuredMeta() {
				expected = ramesdk.FormatPlaintextWithMeta(expected, meta)
			}
			bad = rec.RawText != expected
		}
		if bad {
			tampered = append(tampered, doc.ID)
		}
	}
	for id := range stored {
		if !inCorpus[id] {
			return fmt.Errorf("cloud evaluation local store carries orphan unit %q (not in current corpus); rerun with --rebuild", id)
		}
	}
	if len(tampered) == 0 {
		return nil
	}
	if deleted := e.Store.DeleteLocalUnits(tampered); deleted != len(tampered) {
		return fmt.Errorf("cloud evaluation local store body repair deleted %d/%d tampered units", deleted, len(tampered))
	}
	if err := e.Store.Flush(); err != nil {
		return fmt.Errorf("cloud evaluation plaintext vault flush failed: %w", err)
	}
	logf("[eval] resume body integrity: %d unit(s) carried tampered/stale bodies, re-ingesting from corpus", len(tampered))
	return nil
}

// localIndexSurface 决定本地明文评测使用产品真实面还是历史评测诊断面。
// 默认 fts5-bm25 臂走产品面；standard / fts5-trigram / eval-shannon 保留为诊断对照。
func (e *CloudEngine) localIndexSurface() string {
	if NormalizePulseMode(e.PulseMode) != PulseModeLocalPlain {
		return ""
	}
	if normalizePulseTokenizer(e.PulseTokenizer) == PulseTokenizerFTS5BM25 {
		return LocalIndexSurfaceProduct
	}
	return LocalIndexSurfaceEval
}

// evalLangpackName mirrors the product vault's assembly selection. Empty means the
// shared strongest default (langpack.MaxCapabilityLanguage); an explicit language
// pack or "off" remains a diagnostic override.
func evalLangpackName() string {
	language := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_LANGPACK"))
	if language == "" {
		return langpack.MaxCapabilityLanguage
	}
	return language
}

// localVaultMeta binds the derived SQLite vault to the exact store bytes and index surface.
// Admin 的逐题 recall 会为每题启动一个进程；没有该元数据时每个进程都会重灌 11k 篇。
type localVaultMeta struct {
	Schema       int    `json:"schema"`
	StoreSHA256  string `json:"store_sha256"`
	IndexSurface string `json:"index_surface"`
	Langpack     string `json:"langpack,omitempty"`
	Tokenizer    string `json:"tokenizer"`
	Ingestion    string `json:"ingestion"`
	Documents    int    `json:"documents"`
}

const localVaultMetaSchema = 1

func (e *CloudEngine) localVaultMetaFor(storeSHA string, documents int) localVaultMeta {
	meta := localVaultMeta{
		Schema:       localVaultMetaSchema,
		StoreSHA256:  storeSHA,
		IndexSurface: e.localIndexSurface(),
		Tokenizer:    normalizePulseTokenizer(e.PulseTokenizer),
		Documents:    documents,
	}
	meta.Ingestion = "direct-insert"
	if meta.IndexSurface == LocalIndexSurfaceProduct {
		meta.Langpack = evalLangpackName()
		meta.Ingestion = "product-client"
	}
	return meta
}

// configureLocalVault applies the index configuration of the selected surface.
// 产品面与 internal/api 的唯一 Vault 装配点共用 langpack.ConfigureDefault：默认即
// 最强装配（max-capability packer + 结构化日期信标）；显式语言包/`off` 仅作诊断覆盖。
func (e *CloudEngine) configureLocalVault(v *thinclient.Vault) error {
	if e.localIndexSurface() == LocalIndexSurfaceProduct {
		return langpack.ConfigureDefault(v, evalLangpackName())
	}
	switch normalizePulseTokenizer(e.PulseTokenizer) {
	case PulseTokenizerStandard:
		v.SetCanonicalHarmonics(ramesdk.CanonicalHarmonicOptions{})
		v.SetSegmenterOptions(ramesdk.SegmenterPackOptions{})
	case PulseTokenizerFTS5Trigram:
		v.SetCanonicalHarmonics(ramesdk.CanonicalHarmonicOptions{EnableStructuredDates: true})
		v.SetSegmenterOptions(ramesdk.SegmenterPackOptions{
			Segmenter:       ramesdk.FTS5TrigramSegmenter,
			HarmonicOverlay: true,
		})
	default:
		v.SetCanonicalHarmonics(ramesdk.CanonicalHarmonicOptions{EnableStructuredDates: true})
		v.SetSegmenterOptions(ramesdk.SegmenterPackOptions{
			SentenceAtomPacker: packLocalOfflineShannonSentence,
			HarmonicOverlay:    true,
		})
	}
	return nil
}

func (e *CloudEngine) ensureLocalPlainVault() (*thinclient.Vault, error) {
	e.localVaultMu.Lock()
	defer e.localVaultMu.Unlock()

	raw, err := os.ReadFile(e.StorePath)
	if err != nil {
		return nil, err
	}
	var docs map[string]struct {
		DocID      string `json:"doc_id"`
		OccurredAt int64  `json:"occurred_at"`
		RawText    string `json:"raw_text"`
		AddressURI string `json:"address_uri"`
		Coord64    uint64 `json:"coord64"`
	}
	if err := json.Unmarshal(raw, &docs); err != nil {
		return nil, err
	}
	if len(docs) == 0 {
		return nil, nil
	}
	if e.localVault != nil && e.localVaultCount == len(docs) {
		return e.localVault, nil
	}
	if e.localVaultDB != nil {
		_ = e.localVaultDB.Close()
		e.localVaultDB = nil
		e.localVault = nil
	}
	sqlitePath := e.StorePath + ".local-vault.sqlite"
	metaPath := sqlitePath + ".meta.json"
	secret := []byte("local-eval-vault-key-" + e.UserID)
	storeSHA := sha256.Sum256(raw)
	wantMeta := e.localVaultMetaFor(hex.EncodeToString(storeSHA[:]), len(docs))

	// 复用派生金库：store 字节、索引面、语言包、切词臂与文档数全部一致才打开；
	// 任一不符即整库重建，避免「配置换了但检索仍跑旧索引」的静默漂移。
	if metaRaw, readErr := os.ReadFile(metaPath); readErr == nil {
		var have localVaultMeta
		if json.Unmarshal(metaRaw, &have) == nil && have == wantMeta {
			if db, openErr := sql.Open("sqlite", sqlitePath); openErr == nil {
				_, _ = db.Exec(`PRAGMA synchronous = OFF; PRAGMA journal_mode = MEMORY;`)
				var count int
				countErr := db.QueryRow(`SELECT COUNT(*) FROM thin_vault_docs`).Scan(&count)
				v, vere := thinclient.NewVault(db, secret)
				if countErr == nil && count == len(docs) && vere == nil && e.configureLocalVault(v) == nil {
					e.localVaultDB = db
					e.localVault = v
					e.localVaultCount = len(docs)
					return v, nil
				}
				_ = db.Close()
			}
		}
	}
	_ = os.Remove(sqlitePath)
	_ = os.Remove(metaPath)
	db, err := sql.Open("sqlite", sqlitePath)
	if err != nil {
		return nil, err
	}
	_, _ = db.Exec(`PRAGMA synchronous = OFF; PRAGMA journal_mode = MEMORY;`)
	v, err := thinclient.NewVault(db, secret)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := e.configureLocalVault(v); err != nil {
		_ = db.Close()
		return nil, err
	}
	ids := make([]string, 0, len(docs))
	for id := range docs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if e.localIndexSurface() == LocalIndexSurfaceProduct {
		// 产品面必须走产品摄入链（thinclient.RememberEpisodic）：写入句子帧、kept 索引
		// 与产品切词后的 FTS，与 MCP/CLI 保存记忆同一路径。裸插入会丢句子帧，使
		// 本地排序的事件链/句窗能力失效，等于又把产品能力阉掉一半。
		tc := thinclient.NewClient(v, nil)
		for _, id := range ids {
			rec := docs[id]
			if _, err := tc.RememberEpisodic(context.Background(), id, rec.RawText, rec.OccurredAt); err != nil {
				_ = db.Close()
				return nil, fmt.Errorf("product-surface vault ingest %q: %w", id, err)
			}
		}
	} else {
		tx, err := db.Begin()
		if err != nil {
			_ = db.Close()
			return nil, err
		}
		stmtDoc, _ := tx.Prepare(`INSERT OR REPLACE INTO thin_vault_docs(doc_id, layer, occurred_at, raw_text, sentences_json, address_uri, coord64, waveform_bar, doc_macro_wave, kept_indices_json, assistant_id, incarnation_id, locus) VALUES (?, 'episodic', ?, ?, '[]', ?, ?, '', '', '[]', 'default', '', '')`)
		stmtMap, _ := tx.Prepare(`INSERT OR REPLACE INTO thin_vault_fts_map(rowid, doc_id) VALUES (?, ?)`)
		stmtFTS, _ := tx.Prepare(`INSERT INTO thin_vault_docs_fts(rowid, raw_text) VALUES (?, ?)`)
		for idx, id := range ids {
			rec := docs[id]
			rowID := int64(idx + 1)
			if stmtDoc != nil {
				_, _ = stmtDoc.Exec(id, rec.OccurredAt, rec.RawText, rec.AddressURI, int64(rec.Coord64))
			}
			if stmtMap != nil {
				_, _ = stmtMap.Exec(rowID, id)
			}
			if stmtFTS != nil {
				_, _ = stmtFTS.Exec(rowID, rec.RawText)
			}
		}
		if stmtDoc != nil {
			_ = stmtDoc.Close()
		}
		if stmtMap != nil {
			_ = stmtMap.Close()
		}
		if stmtFTS != nil {
			_ = stmtFTS.Close()
		}
		if err := tx.Commit(); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	if metaJSON, err := json.Marshal(wantMeta); err != nil {
		_ = db.Close()
		return nil, err
	} else if err := os.WriteFile(metaPath, metaJSON, 0o600); err != nil {
		_ = db.Close()
		return nil, err
	}
	e.localVaultDB = db
	e.localVault = v
	e.localVaultCount = len(docs)
	return v, nil
}

// recallLocalProduct 走产品真实召回合成：本地候选 × 本地排序
// （thinclient.RecallWithOptions），与 MCP/CLI 产品 recall 同一条 SDK 链。
func (e *CloudEngine) recallLocalProduct(ctx context.Context, v *thinclient.Vault, query string, topK int) ([]ramesdk.ResolvedHit, error) {
	tc := thinclient.NewLocalClient(v)
	// 评测查询不注入墙钟 TimeRef：产品 API 默认用 now，但该读数不可复现；Hard-31 对照
	// TimeRef=0 与 now 同分，故固定 0（相对日期题如需 now 需显式登记）。
	hits, err := tc.RecallWithOptions(ctx, query, thinclient.BlindQueryOptions{TopK: topK})
	if err != nil {
		return nil, err
	}
	if len(hits) > topK {
		hits = hits[:topK]
	}
	out := make([]ramesdk.ResolvedHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, ramesdk.ResolvedHit{
			CloudQueryHit: ramesdk.CloudQueryHit{
				UnitID:         h.UnitID,
				Layer:          h.Layer,
				OccurredAt:     h.OccurredAt,
				AddressURI:     h.AddressURI,
				AddressCoord64: h.AddressCoord64,
				WaveScore:      h.WaveScore,
				CellWormhole:   h.CellWormhole,
				ManifoldGain:   h.ManifoldGain,
				FinalWeight:    h.FinalWeight,
			},
			RawPlaintext:       h.RawPlaintext,
			DistilledPlaintext: h.DistilledPlaintext,
			KeptSentences:      h.KeptSentences,
		})
	}
	return out, nil
}

func (e *CloudEngine) ensureZhaoRMEngine() (*rm.Engine, *rm.TermExtractor, error) {
	e.localVaultMu.Lock()
	defer e.localVaultMu.Unlock()
	if e.zhaoEngine != nil && e.zhaoTermExtractor != nil {
		return e.zhaoEngine, e.zhaoTermExtractor, nil
	}
	raw, rErr := os.ReadFile(e.StorePath)
	if rErr != nil {
		return nil, nil, rErr
	}
	var storedDocs map[string]struct {
		DocID      string `json:"doc_id"`
		OccurredAt int64  `json:"occurred_at"`
		RawText    string `json:"raw_text"`
	}
	if err := json.Unmarshal(raw, &storedDocs); err != nil || len(storedDocs) == 0 {
		return nil, nil, errors.New("empty or invalid local store")
	}
	if len(storedDocs) < 64 {
		return nil, nil, errors.New("small synthetic fixture store uses standard vault path")
	}
	te := rm.NewTermExtractor(rm.DemoSecret)
	zcsrPath := e.StorePath + ".zhao-v2.zcsr"
	if eng, err := rm.OpenDiskClusterEngine(zcsrPath); err == nil && eng != nil && eng.Index != nil && eng.Index.N == len(storedDocs) {
		e.zhaoEngine = eng
		e.zhaoTermExtractor = te
		return eng, te, nil
	}
	if e.Suite == SuiteATM {
		if atmDir, err := ResolveATMDataDir(""); err == nil {
			if corpusDocs, cErr := rm.LoadATMCorpus(filepath.Dir(atmDir), 0); cErr == nil && len(corpusDocs) == len(storedDocs) {
				rmDocs := make([]rm.Doc, len(corpusDocs))
				workers := 8
				var wg sync.WaitGroup
				step := (len(corpusDocs) + workers - 1) / workers
				for w := 0; w < workers; w++ {
					start := w * step
					if start >= len(corpusDocs) {
						break
					}
					end := start + step
					if end > len(corpusDocs) {
						end = len(corpusDocs)
					}
					wg.Add(1)
					go func(s, eIdx int) {
						defer wg.Done()
						localTe := rm.NewTermExtractor(rm.DemoSecret)
						for i := s; i < eIdx; i++ {
							d := corpusDocs[i]
							rmDocs[i] = localTe.ExtractDoc(d.ID, d.OccurredAt, d.Text)
						}
					}(start, end)
				}
				wg.Wait()
				idx := rm.BuildIndex(rmDocs)
				eng := rm.NewEngine(idx)
				_ = rm.WriteDiskClusterCSR(zcsrPath, eng)
				e.zhaoEngine = eng
				e.zhaoTermExtractor = te
				return eng, te, nil
			}
		}
	}
	ids := make([]string, 0, len(storedDocs))
	for id := range storedDocs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	rmDocs := make([]rm.Doc, len(ids))
	workers := 8
	var wg sync.WaitGroup
	step := (len(ids) + workers - 1) / workers
	for w := 0; w < workers; w++ {
		start := w * step
		if start >= len(ids) {
			break
		}
		end := start + step
		if end > len(ids) {
			end = len(ids)
		}
		wg.Add(1)
		go func(s, eIdx int) {
			defer wg.Done()
			localTe := rm.NewTermExtractor(rm.DemoSecret)
			for i := s; i < eIdx; i++ {
				id := ids[i]
				rec := storedDocs[id]
				rmDocs[i] = localTe.ExtractDoc(id, rec.OccurredAt, rec.RawText)
			}
		}(start, end)
	}
	wg.Wait()
	idx := rm.BuildIndex(rmDocs)
	eng := rm.NewEngine(idx)
	_ = rm.WriteDiskClusterCSR(zcsrPath, eng)
	e.zhaoEngine = eng
	e.zhaoTermExtractor = te
	return eng, te, nil
}

func (e *CloudEngine) recallLocalPlaintext(ctx context.Context, query string, topK int) ([]ramesdk.ResolvedHit, error) {
	if topK <= 0 {
		topK = 10
	}
	if e.localIndexSurface() == LocalIndexSurfaceProduct {
		if eng, az, zErr := e.ensureZhaoRMEngine(); zErr == nil && eng != nil && az != nil {
			e.zhaoQueryMu.Lock()
			zHits := eng.Query(az, query, topK)
			e.zhaoQueryMu.Unlock()
			out := make([]ramesdk.ResolvedHit, 0, len(zHits))
			for _, zh := range zHits {
				raw, dist, kept, lErr := e.Store.LoadLocalDocument(zh.UnitID)
				if lErr != nil {
					continue
				}
				out = append(out, ramesdk.ResolvedHit{
					CloudQueryHit: ramesdk.CloudQueryHit{
						UnitID:      zh.UnitID,
						Layer:       ramesdk.LayerEpisodic,
						WaveScore:   zh.Energy,
						FinalWeight: zh.Energy,
					},
					RawPlaintext:       raw,
					DistilledPlaintext: dist,
					KeptSentences:      kept,
				})
			}
			return out, nil
		}
	}
	v, err := e.ensureLocalPlainVault()
	if err != nil || v == nil {
		return nil, err
	}
	if e.localIndexSurface() == LocalIndexSurfaceProduct {
		return e.recallLocalProduct(ctx, v, query, topK)
	}
	hits, err := v.SearchClientSDKDefault(query, topK)
	if err != nil {
		return nil, err
	}
	out := make([]ramesdk.ResolvedHit, 0, len(hits))
	for _, h := range hits {
		raw, dist, kept, err := e.Store.LoadLocalDocument(h.UnitID)
		if err != nil {
			continue
		}
		out = append(out, ramesdk.ResolvedHit{
			CloudQueryHit: ramesdk.CloudQueryHit{
				UnitID:         h.UnitID,
				Layer:          h.Layer,
				CitedUnitIDs:   h.CitedUnitIDs,
				WaveScore:      h.WaveScore,
				FinalWeight:    h.FinalWeight,
				AddressURI:     h.AddressURI,
				AddressCoord64: h.AddressCoord64,
			},
			RawPlaintext:       raw,
			DistilledPlaintext: dist,
			KeptSentences:      kept,
		})
	}
	return out, nil
}

// Recall executes one remote query (or local offline query in PulseModeLocalPlain).
// No cross-mode fallback or supplementation is allowed.
func (e *CloudEngine) Recall(ctx context.Context, query string, topK int) ([]ramesdk.ResolvedHit, error) {
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("cloud evaluation recall requires a non-empty query")
	}
	if err := e.CheckHealth(ctx); err != nil {
		return nil, fmt.Errorf("cloud evaluation health check failed: %w", err)
	}
	// 世代在用：读者心跳续期（Admin 批次逐题 recall 也走这里），保证并发「清旧记忆」跳过本世代。
	touchEvalGeneration(e.Scope, evalGenerationRoleReader)

	if NormalizePulseMode(e.PulseMode) == PulseModeLocalPlain {
		return e.recallLocalPlaintext(ctx, query, topK)
	}

	const maxAttempts = 5
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		hits, err := e.Client.Recall(ctx, query, topK)
		if err == nil {
			return hits, nil
		}
		// 瞬时故障退避重试（EOF / 网络重置 / 超时），语义错误（鉴权/scope）原样上抛。
		if isTransient(err) {
			lastErr = err
			time.Sleep(time.Duration(attempt*attempt) * 500 * time.Millisecond)
			continue
		}
		return nil, fmt.Errorf("cloud evaluation SDK recall failed: %w", err)
	}
	return nil, fmt.Errorf("cloud evaluation SDK recall exhausted retries (last=%v): %w", lastErr, describeDestructiveGate(lastErr))
}

func isTransient(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "EOF") || strings.Contains(s, "connection reset") ||
		strings.Contains(s, "timeout") || strings.Contains(s, "503") || strings.Contains(s, "502")
}

// ReadCloudManifest reads the isolated preparation receipt used by Admin readiness checks.
func ReadCloudManifest(storePath string) (CloudManifest, error) {
	var manifest CloudManifest
	raw, err := os.ReadFile(storePath + ".manifest.json")
	if err != nil {
		return manifest, err
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return manifest, err
	}
	if !IsValidEvalTransport(manifest.Transport) || manifest.LocalFallback || !IsCloudEvalScope(manifest.Scope) {
		return manifest, errors.New("invalid cloud evaluation manifest")
	}
	return manifest, nil
}
