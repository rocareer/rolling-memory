// plugin: kb v1.0.0
// Package kbcfg 是本插件配置的**唯一读取口**：内嵌缺省（config/kb.yaml）▷ `configs` 表
// `plugin.kb` 分组行覆盖。
//
// 为什么不要宿主配置体系：宿主配置的缺省源是**宿主模块根**的 `//go:embed config/*.yaml`
// （宿主读不到插件目录里的 yaml），而把插件的配置散落进宿主 `config/` 又违反
// 「只新增不覆盖宿主既有文件 + 归属可清理」的插件纪律（卸载不会清理宿主的散落文件）。
// 故本插件自带缺省并把读取口收在本包：宿主 `internal/infra/config` 一行不改。
//
// 口径（逐条）：
//   - **覆盖语义 = 行存在即以其值为准（含空串），行缺失才回落内嵌缺省**。空串是合法值
//     （如 embed_model 空 = 向量身份不可用 ⇒ 嵌入面 fail-closed），不得当作「未配置」吞掉。
//   - **可覆盖键是闭集 = 14 个**（OverridableKeys，与 plugin.yaml 的 `configs:` 段逐项一致）：
//     其余键（chunk.levels / chunk.window / media.frame_seconds / budget.* 系数 / tag.material_chars 等）
//     **只读内嵌缺省**——它们要么参与 build_hash/产物身份（改了会让存量切片与向量对不上），要么属调优面。
//     对照表见本插件 README；kbcfg_test.go 断言该闭集与清单一致（防漂移）。
//   - 缓存：进程内 10s TTL（越小越实时、越大越省查询）；`Get()` 是唯一入口。
//   - DB 不可用（未初始化 / 查询失败）⇒ 回落内嵌缺省并记一次日志：本包**不**因配置读取失败而让业务面
//     报错（缺省即出厂档）；但嵌入/模型类开关的缺省是「关」，所以降级方向始终是 fail-closed。
package kbcfg

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/infra/database"
	"github.com/ai-go-hub/ai-go-admin/internal/model"
	kbconfig "github.com/ai-go-hub/ai-go-admin/plugins/kb/config"

	"go.yaml.in/yaml/v3"
	"gorm.io/gorm"
)

// Group 本插件在 `configs` 表里的配置分组（安装器写 plugin.<插件名>）
const Group = "plugin.kb"

// keyPrefix 本插件配置键的落库名前缀（安装器 ConfigKey：plugin_<名>_<键>）
const keyPrefix = "plugin_kb_"

// OverridableKeys 允许被 `configs` 行覆盖的键闭集（与 plugin.yaml 的 configs 段逐项一致；
// 其余内嵌键只读——理由见包注释）。顺序 = 清单顺序，便于人眼对照。
var OverridableKeys = []string{
	"embed_enabled",
	"embed_provider",
	"embed_model",
	"embed_dim",
	"chunk_target",
	"chunk_overlap",
	"chunk_hard_cap",
	"fts_config",
	"search_scan_cap",
	"prune_retention_days",
	"log_query_text_max",
	"media_ffmpeg_bin",
	"limits_max_pages",
	"limits_max_minutes",
}

// cacheTTL 覆盖层缓存上限（10s：变更后最多 10s 生效，无需重启）
const cacheTTL = 10 * time.Second

// FTSConfig 词面腿 tsv 查询配置（读取点 ingest.ResolveFTSConfig 单点；只接受空/simple）
type FTSConfig struct {
	Config string `yaml:"config"`
}

// SearchConfig 检索面配置（扫描帽 / 融合常数 / 相似度阈值 / 查询缓存 / 日志原文帽）
type SearchConfig struct {
	ScanCap           int     `yaml:"scan_cap"`            // 腿窗帽（≤0 回落代码缺省）
	RRFK              int     `yaml:"rrf_k"`               // RRF 融合常数（≤0 回落 60）
	TrgmThreshold     float64 `yaml:"trgm_threshold"`      // trgm 腿相似度阈值（域 (0,1)）
	LogQueryTextMax   int     `yaml:"log_query_text_max"`  // 查询原文落库帽（0/负 = 关；封顶 512）
	QueryCacheFile    string  `yaml:"query_cache_file"`    // 查询向量缓存文件（空 = 缺省路径）
	QueryCachePersist bool    `yaml:"query_cache_persist"` // 落盘开关（缺省关）
}

// PruneConfig 检索日志保留期（kb:prune 作业按日清理）
type PruneConfig struct {
	RetentionDays int `yaml:"retention_days"`
}

// EmbedConfig 嵌入档配置（kb:ingest 作业内的向量生产）
type EmbedConfig struct {
	Enabled  bool   `yaml:"enabled"`   // 关 = 只切不嵌（检索面自动降级词面腿）
	Batch    int    `yaml:"batch"`     // 单次嵌入调用文本条数
	MaxBatch int    `yaml:"max_batch"` // 单批硬上限
	MaxChars int    `yaml:"max_chars"` // 单条嵌入文本字符帽
	Provider string `yaml:"provider"`  // 嵌入端口实现名（未注册 ⇒ 响亮失败，不回落）
	Model    string `yaml:"model"`     // 嵌入模型（**向量身份 = 该值**；空 = 身份不可用）
	Dim      int    `yaml:"dim"`       // 期望向量维度（须与 kb_chunk_vectors 的 halfvec 列一致）
}

// BudgetLimits 预算系数与上限（0 = 不设限；系数供估算口径）
type BudgetLimits struct {
	MaxDocsPerJob      int     `yaml:"max_docs_per_job"`
	MaxChunksPerJob    int     `yaml:"max_chunks_per_job"`
	MaxCharsPerJob     int64   `yaml:"max_chars_per_job"`
	MaxEstTokensPerJob int64   `yaml:"max_est_tokens_per_job"`
	CJKTokenPerChar    float64 `yaml:"cjk_token_per_char"`
	NonCJKTokenPerChar float64 `yaml:"non_cjk_token_per_char"`
	OCRPricePerPage    float64 `yaml:"ocr_price_per_page"`
	ASRPricePerMinute  float64 `yaml:"asr_price_per_minute"`
}

// MediaChannel 云渠道（OCR/ASR/caption；同一形态三渠道）
type MediaChannel struct {
	Enabled  bool   `yaml:"enabled"`
	Provider string `yaml:"provider"`
}

// MediaConfig 媒体管道配置（驱动登记表为空 ⇒ 渠道缺失一律 fail-closed）
type MediaConfig struct {
	OCR                   MediaChannel `yaml:"ocr"`
	ASR                   MediaChannel `yaml:"asr"`
	Caption               MediaChannel `yaml:"caption"`
	FFmpegBin             string       `yaml:"ffmpeg_bin"`
	FrameSeconds          int          `yaml:"frame_seconds"`
	TimeoutSeconds        int          `yaml:"timeout_seconds"`
	TimeoutPerMediaMinute int          `yaml:"timeout_per_media_minute_seconds"`
	TimeoutPerPDFPage     int          `yaml:"timeout_per_page_seconds"`
}

// LimitsConfig 来源上限（0/未配 = 代码缺省；**闸始终武装**）
type LimitsConfig struct {
	MaxPages   int     `yaml:"max_pages"`
	MaxMinutes float64 `yaml:"max_minutes"`
}

// TagConfig 标签候选档（模型候选缺省关）
type TagConfig struct {
	ModelEnabled  bool   `yaml:"model_enabled"`
	Agent         string `yaml:"agent"`
	Provider      string `yaml:"provider"`
	Model         string `yaml:"model"`
	MaterialChars int    `yaml:"material_chars"`
	MaxCandidates int    `yaml:"max_candidates"`
	VocabularyCap int    `yaml:"vocabulary_cap"`
}

// Config 本插件的生效配置（内嵌缺省已被覆盖层叠加后的结果）
type Config struct {
	Chunk  map[string]any `yaml:"chunk"` // 切片全局层：键闭集与值域由 chunk 包校验（未知键响亮拒绝）
	FTS    FTSConfig      `yaml:"fts"`
	Search SearchConfig   `yaml:"search"`
	Prune  PruneConfig    `yaml:"prune"`
	Embed  EmbedConfig    `yaml:"embed"`
	Budget BudgetLimits   `yaml:"budget"`
	Media  MediaConfig    `yaml:"media"`
	Limits LimitsConfig   `yaml:"limits"`
	Tag    TagConfig      `yaml:"tag"`
}

// file 内嵌缺省 YAML 的顶层结构（与 config/kb.yaml 的 `kb:` 段同形）
type file struct {
	KB Config `yaml:"kb"`
}

var (
	defaultsOnce sync.Once
	defaults     Config
)

// errDBUnavailable 数据库未初始化（单测/工具进程）——覆盖层不可读，回落内嵌缺省
var errDBUnavailable = errors.New("数据库未初始化")

// loadDefaults 解析内嵌缺省（进程内一次）。解析失败是**打包错误**（YAML 内嵌在二进制里、
// 取值与文件逐字节绑定）⇒ panic 响亮暴露，而不是让业务面拿一份空配置继续跑。
func loadDefaults() {
	var f file
	if err := yaml.Unmarshal(kbconfig.DefaultYAML, &f); err != nil {
		panic(fmt.Sprintf("kbcfg: 解析内嵌缺省配置 config/kb.yaml 失败（打包错误）: %v", err))
	}
	defaults = f.KB
	if defaults.Chunk == nil {
		defaults.Chunk = map[string]any{}
	}
}

// Defaults 内嵌缺省（调用方拿副本，不暴露内部状态）
func Defaults() Config {
	defaultsOnce.Do(loadDefaults)
	out := defaults
	if defaults.Chunk != nil {
		out.Chunk = make(map[string]any, len(defaults.Chunk))
		for k, v := range defaults.Chunk {
			out.Chunk[k] = v
		}
	}
	return out
}

// Get 生效配置（内嵌缺省 ▷ configs 行覆盖；10s 进程内缓存）
func Get() Config {
	cacheMu.Lock()
	if cacheValid && time.Since(cacheAt) < cacheTTL {
		out := cache
		cacheMu.Unlock()
		return out
	}
	cacheMu.Unlock()

	cfg := Defaults()
	if rows, err := loadRows(); err != nil {
		logRowsErr(err)
	} else {
		ApplyOverrides(&cfg, rows)
	}

	cacheMu.Lock()
	cache, cacheAt, cacheValid = cfg, time.Now(), true
	cacheMu.Unlock()
	return cfg
}

// Invalidate 使缓存立即失效（用例与运维端点复用；生产由 TTL 收敛）
func Invalidate() {
	cacheMu.Lock()
	cacheValid = false
	cacheMu.Unlock()
}

var (
	cacheMu    sync.Mutex
	cache      Config
	cacheAt    time.Time
	cacheValid bool
	warnedOnce sync.Once
)

// loadRows 读 `plugin.kb` 分组的配置行；键名去掉 `plugin_kb_` 前缀后返回（未配置的键不出现）。
// DB 未初始化（单测/工具）⇒ 错误，由调用方回落内嵌缺省。
func loadRows() (map[string]string, error) {
	db := database.DB()
	if db == nil {
		return nil, errDBUnavailable
	}
	type row struct {
		Name  string `gorm:"column:name"`
		Value string `gorm:"column:value"`
	}
	rows := make([]row, 0, len(OverridableKeys))
	err := db.WithContext(context.Background()).
		Table(tableOf(db)).
		Select("name, value").
		Where(`"group" = ?`, Group).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		key := strings.TrimPrefix(r.Name, keyPrefix)
		if key == r.Name { // 前缀不符（非本插件的键）⇒ 不收
			continue
		}
		out[key] = r.Value
	}
	return out, nil
}

// tableOf `configs` 表名（与模型 TableName() 同源，表前缀由 GORM NamingStrategy 注入）
func tableOf(db *gorm.DB) string {
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(&model.Config{}); err == nil && stmt.Schema != nil && stmt.Schema.Table != "" {
		return stmt.Schema.Table
	}
	return "configs"
}

// ApplyOverrides 把覆盖行叠加到配置上（**纯函数**：供用例断言键闭集与覆盖语义，不触库）。
// rows 的键 = 清单里的 configs[].name（已去 plugin_kb_ 前缀）。
func ApplyOverrides(cfg *Config, rows map[string]string) {
	if cfg == nil || len(rows) == 0 {
		return
	}
	for _, key := range OverridableKeys {
		value, ok := rows[key]
		if !ok {
			continue // 行缺失 ⇒ 回落内嵌缺省
		}
		applyKey(cfg, key, value)
	}
}

// applyKey 单键覆盖（行存在即以其值为准：空串也是值）
func applyKey(cfg *Config, key, value string) {
	switch key {
	case "embed_enabled":
		cfg.Embed.Enabled = truthy(value)
	case "embed_provider":
		cfg.Embed.Provider = value
	case "embed_model":
		cfg.Embed.Model = value
	case "embed_dim":
		cfg.Embed.Dim = atoiOr(value, cfg.Embed.Dim)
	case "chunk_target":
		setChunk(cfg, "target", value)
	case "chunk_overlap":
		setChunk(cfg, "overlap", value)
	case "chunk_hard_cap":
		setChunk(cfg, "hard_cap", value)
	case "fts_config":
		cfg.FTS.Config = value
	case "search_scan_cap":
		cfg.Search.ScanCap = atoiOr(value, cfg.Search.ScanCap)
	case "prune_retention_days":
		cfg.Prune.RetentionDays = atoiOr(value, cfg.Prune.RetentionDays)
	case "log_query_text_max":
		cfg.Search.LogQueryTextMax = atoiOr(value, cfg.Search.LogQueryTextMax)
	case "media_ffmpeg_bin":
		cfg.Media.FFmpegBin = value
	case "limits_max_pages":
		cfg.Limits.MaxPages = atoiOr(value, cfg.Limits.MaxPages)
	case "limits_max_minutes":
		cfg.Limits.MaxMinutes = atofOr(value, cfg.Limits.MaxMinutes)
	}
}

// setChunk 切片全局层的单键覆盖（值为整数；非数值回落当前值——切片键的值域校验仍在 chunk 包）
func setChunk(cfg *Config, key, value string) {
	if cfg.Chunk == nil {
		cfg.Chunk = map[string]any{}
	}
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return
	}
	cfg.Chunk[key] = n
}

// truthy 开关值判定（switch 类的值形如 0/1；兼容 true/false/on/off 的书写）
func truthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "on", "yes", "enable", "enabled":
		return true
	}
	return false
}

// atoiOr 整数解析（非法回落 fallback）
func atoiOr(value string, fallback int) int {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return fallback
	}
	return n
}

// atofOr 浮点解析（非法回落 fallback）
func atofOr(value string, fallback float64) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return fallback
	}
	return f
}

// logRowsErr 覆盖层读取失败只提示一次（不刷屏；缺省档即出厂档，业务面不因此报错）
func logRowsErr(err error) {
	warnedOnce.Do(func() {
		log.Printf("[kb] 读取插件配置覆盖层失败（按内嵌缺省运行）: %v", err)
	})
}
