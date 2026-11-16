// Package source 实现插件「仓库来源」与统一清单（catalog v1）的宿主侧通道：
// 来源配置的读取/校验、清单的严格解析、按来源取包与包体校验。
//
// 边界（与安装链的分工）：
//   - 本包只做「取字节 + 校验」，不落位、不写台账、不写接线文件；
//   - 真正的安装编排在 internal/plugin/cli（复用既有 Install 管线），
//     本包不 import cli，避免包依赖成环；
//   - 清单是包外索引：不改变 plugin.yaml/ini.yaml、plugin.go 生命周期与包内落位规则。
package source

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/manifest"
)

// 来源类型（清单的运输方式；不切换字段集与解析器）
const (
	// TypeLocal 本地文件（仓库根相对路径或经显式开关允许的绝对路径）
	TypeLocal = "local"
	// TypeHTTP HTTP(S) 地址（默认只允许 https；明文与私网需逐来源显式开关）
	TypeHTTP = "http"
	// TypeGit Git 仓库（location = <仓库URL>[#<ref>][!<仓内清单路径>]；浅克隆后读仓内清单，
	// 包体按清单的 package.path 从工作副本取；默认只允许 https，明文与私网需逐来源显式开关）
	TypeGit = "git"
)

// 清单与包体上限（与 internal/plugin/cli 的 zip 防护同口径；此处是清单层预检，
// 安装链的 StageZip 仍会再次强制这些上限）。
const (
	// MaxCatalogBytes 单份清单字节上限（解析前先按此上限读，不先读全再截断）
	MaxCatalogBytes int64 = 1 << 20
	// MaxCatalogEntries 单份清单条目数上限
	MaxCatalogEntries = 1000
	// MaxPackageBytes 包体字节上限
	MaxPackageBytes int64 = 20 << 20
	// MaxEntryCount zip 条目数上限
	MaxEntryCount = 2000
	// MaxEntryBytes 包内单文件解出上限
	MaxEntryBytes int64 = 10 << 20
	// MaxExtractBytes 包内解出总量上限
	MaxExtractBytes int64 = 100 << 20
	// MaxLocationBytes 来源 location / package.url / package.path 的字符上限
	MaxLocationBytes = 2048
	// MaxSyncErrorBytes last_sync_error 入库截断长度
	MaxSyncErrorBytes = 2000
)

// 清单 schema 的固定值（其他值整份拒绝，不做降级解析）
const CatalogSchema = "ai-go-plugin-catalog/v1"

// 清单条目 kind 的闭集（与 manifest.Kinds 一致）
var CatalogKinds = []string{"code", "declarative", "mixed"}

// 来源与管理面错误（调用方用 errors.Is 做稳定映射，不解析错误字符串）
var (
	// ErrInvalidInput 输入不合法（字段级校验失败）
	ErrInvalidInput = errors.New("参数不合法")
	// ErrNotFound 来源不存在
	ErrNotFound = errors.New("来源不存在")
	// ErrNameConflict 来源名重复
	ErrNameConflict = errors.New("来源名重复")
	// ErrNameImmutable 来源名不可修改
	ErrNameImmutable = errors.New("来源名不可修改")
	// ErrVersionContentMutated 同一来源内同一 (uid, version) 的包摘要发生变化
	ErrVersionContentMutated = errors.New("同一版本的内容发生变化")
	// ErrSourceDisabled 来源已停用
	ErrSourceDisabled = errors.New("来源已停用")
	// ErrCatalogNotSynced 来源尚未同步过清单
	ErrCatalogNotSynced = errors.New("来源尚未同步清单")
	// ErrCatalogStale 来源清单已变化，需重新同步
	ErrCatalogStale = errors.New("清单已变化，需重新同步")
	// ErrEntryNotFound 清单中没有该 (uid, version)
	ErrEntryNotFound = errors.New("清单中没有该插件版本")
	// ErrCatalogNotFound 本地清单文件不存在或不可读（source/test 的配置错误，区别于包体缺失）
	ErrCatalogNotFound = errors.New("清单文件不存在或不可读")
	// ErrPackageNotFound 包不存在或不可读
	ErrPackageNotFound = errors.New("包不存在或不可读")
	// ErrPackageSizeMismatch 包体字节数与清单声明不一致
	ErrPackageSizeMismatch = errors.New("包体大小与清单声明不一致")
	// ErrPackageSHAMismatch 包体摘要与清单声明不一致
	ErrPackageSHAMismatch = errors.New("包体摘要与清单声明不一致")
	// ErrPackageSignedMismatch 清单的 signed 声明与包内签名条目事实不一致
	ErrPackageSignedMismatch = errors.New("签名声明与包内事实不一致")
	// ErrManifestMismatch 清单字段与包内元数据不一致
	ErrManifestMismatch = errors.New("清单字段与包内元数据不一致")
	// ErrPathEscape 本地路径逃逸允许根、含 symlink 上级或不是普通文件
	ErrPathEscape = errors.New("路径不安全或越出允许根")
	// ErrNetworkBlocked URL 形状或目标地址被安全策略拒绝
	ErrNetworkBlocked = errors.New("地址被安全策略拒绝")
	// ErrUpstreamError 远端取数失败
	ErrUpstreamError = errors.New("远端取数失败")
)

var (
	namePattern    = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)
	versionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	sha256Pattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// CatalogReservedNames 清单条目 uid 不得占用的宿主保留字：正本 manifest.ReservedNames 的副本
// （已含本通道新增的 source/catalog，见 reserved.go 的三处同步口径）。
// 解析期用它提前拦截，避免下载包之后才发现与宿主页面/路由相撞。
// 唯一正本是 manifest.ReservedNames；此处再取一份是为了让解析层不直接依赖安装期包的变量。
var CatalogReservedNames = append([]string{}, manifest.ReservedNames...)

// Source 一条来源配置（表 admin_plugin_sources）。
// JSON 字段即管理面/CLI 的输出形状；name 创建后不可修改。
type Source struct {
	ID                     uint       `gorm:"comment:ID;primarykey;autoIncrement" json:"id"`
	Name                   string     `gorm:"comment:来源键;type:varchar(32);not null;default:''" json:"name"`
	Title                  string     `gorm:"comment:展示名;type:varchar(64);not null;default:''" json:"title"`
	Type                   string     `gorm:"comment:运输方式:local=本地文件,http=HTTP(S);type:varchar(16);not null;default:'local'" json:"type"`
	Location               string     `gorm:"comment:本地路径或URL;type:text;not null;default:''" json:"location"`
	Enabled                int16      `gorm:"comment:启用:0=停用,1=启用;type:smallint;not null;default:1" json:"enabled"`
	AllowInsecureHTTP      int16      `gorm:"comment:允许明文http;type:smallint;not null;default:0" json:"allow_insecure_http"`
	AllowPrivateHosts      int16      `gorm:"comment:允许私网/回环主机;type:smallint;not null;default:0" json:"allow_private_hosts"`
	AllowAbsolutePaths     int16      `gorm:"comment:允许本地绝对路径;type:smallint;not null;default:0" json:"allow_absolute_paths"`
	Weigh                  int64      `gorm:"comment:排序权重;type:bigint;not null;default:0" json:"weigh"`
	LastSyncStatus         string     `gorm:"comment:最后同步状态:空=never,ok,error;type:varchar(16);not null;default:''" json:"last_sync_status"`
	LastSyncError          string     `gorm:"comment:最后同步失败原因(已脱敏,截断);type:text;not null;default:''" json:"last_sync_error"`
	LastCatalogSHA256      string     `gorm:"comment:最后成功同步的清单字节sha256;type:varchar(64);not null;default:''" json:"last_catalog_sha256"`
	LastCatalogGeneratedAt *time.Time `gorm:"comment:清单generated_at(仅展示/诊断)" json:"last_catalog_generated_at"`
	LastSyncedAt           *time.Time `gorm:"comment:最后成功同步时间" json:"last_synced_at"`
	CreatedBy              string     `gorm:"comment:创建人署名;type:varchar(64);not null;default:''" json:"created_by"`
	UpdatedBy              string     `gorm:"comment:最后修改人署名;type:varchar(64);not null;default:''" json:"updated_by"`
	Remark                 string     `gorm:"comment:备注;type:varchar(255);not null;default:''" json:"remark"`
	CreatedAt              *time.Time `json:"created_at"`
	UpdatedAt              *time.Time `json:"updated_at"`
}

// TableName 固定逻辑表名；物理前缀由 GORM 命名策略承担（与 model.Plugin 同款）。
func (Source) TableName() string {
	return "admin_plugin_sources"
}

// CatalogEntry 一条清单快照行（表 admin_plugin_catalog_entries）。
// 快照只用于展示/筛选/冲突检测；安装时仍重新读取来源清单并比对摘要。
type CatalogEntry struct {
	ID            uint       `gorm:"comment:ID;primarykey;autoIncrement" json:"id"`
	SourceID      uint       `gorm:"comment:归属来源ID;type:bigint;not null" json:"source_id"`
	SourceName    string     `gorm:"comment:归属来源名(不可变,冗余展示);type:varchar(32);not null;default:''" json:"source_name"`
	UID           string     `gorm:"comment:插件身份键;type:varchar(32);not null;default:''" json:"uid"`
	Version       string     `gorm:"comment:版本X.Y.Z;type:varchar(32);not null;default:''" json:"version"`
	VersionMajor  int        `gorm:"comment:主版本;type:integer;not null;default:0" json:"version_major"`
	VersionMinor  int        `gorm:"comment:次版本;type:integer;not null;default:0" json:"version_minor"`
	VersionPatch  int        `gorm:"comment:修订版本;type:integer;not null;default:0" json:"version_patch"`
	TitleZH       string     `gorm:"comment:中文展示名;type:varchar(64);not null;default:''" json:"title_zh"`
	TitleEN       string     `gorm:"comment:英文展示名;type:varchar(64);not null;default:''" json:"title_en"`
	DescZH        string     `gorm:"comment:中文简介;type:varchar(256);not null;default:''" json:"desc_zh"`
	DescEN        string     `gorm:"comment:英文简介;type:varchar(256);not null;default:''" json:"desc_en"`
	Kind          string     `gorm:"comment:插件形态;type:varchar(16);not null;default:''" json:"kind"`
	HostAPI       string     `gorm:"comment:机制接口版本域;type:varchar(64);not null;default:''" json:"host_api"`
	FrameworkReq  string     `gorm:"comment:框架线版本域;type:varchar(64);not null;default:''" json:"framework_req"`
	Requires      string     `gorm:"comment:依赖(规范化uid@range,逗号分隔);type:text;not null;default:''" json:"requires"`
	License       string     `gorm:"comment:许可证(展示用);type:varchar(32);not null;default:''" json:"license"`
	PackageURL    string     `gorm:"comment:包URL;type:text;not null;default:''" json:"package_url"`
	PackagePath   string     `gorm:"comment:包本地路径;type:text;not null;default:''" json:"package_path"`
	PackageSize   int64      `gorm:"comment:包体字节数;type:bigint;not null;default:0" json:"package_size"`
	PackageSHA256 string     `gorm:"comment:包体sha256;type:varchar(64);not null;default:''" json:"package_sha256"`
	PackageSigned int16      `gorm:"comment:声明包内是否有分离签名;type:smallint;not null;default:0" json:"package_signed"`
	CatalogSHA256 string     `gorm:"comment:快照来自的清单字节sha256;type:varchar(64);not null;default:''" json:"catalog_sha256"`
	SyncedAt      time.Time  `gorm:"comment:同步时间;type:timestamptz;not null;default:NOW()" json:"synced_at"`
	CreatedAt     *time.Time `json:"created_at"`
	UpdatedAt     *time.Time `json:"updated_at"`
}

// TableName 固定逻辑表名（物理前缀由 GORM 命名策略承担）。
func (CatalogEntry) TableName() string {
	return "admin_plugin_catalog_entries"
}

// UIDVersion 一组 (uid, version) 键（跨来源冲突检测用）。
type UIDVersion struct {
	UID     string `json:"uid"`
	Version string `json:"version"`
}

// SourceInput 来源创建/更新的字段白名单（id、last_*、created_by/updated_by 由服务写）。
type SourceInput struct {
	Name               string `json:"name"`
	Title              string `json:"title"`
	Type               string `json:"type"`
	Location           string `json:"location"`
	Enabled            *int16 `json:"enabled"`
	AllowInsecureHTTP  *int16 `json:"allow_insecure_http"`
	AllowPrivateHosts  *int16 `json:"allow_private_hosts"`
	AllowAbsolutePaths *int16 `json:"allow_absolute_paths"`
	Weigh              *int64 `json:"weigh"`
	Remark             string `json:"remark"`
}

// NewSource 由输入构造来源（创建路径；name 必填）。
func NewSource(in SourceInput, operator string) (*Source, error) {
	now := time.Now()
	src := &Source{
		Name:      strings.TrimSpace(in.Name),
		Title:     strings.TrimSpace(in.Title),
		Type:      strings.ToLower(strings.TrimSpace(in.Type)),
		Location:  strings.TrimSpace(in.Location),
		Enabled:   1,
		Weigh:     0,
		Remark:    strings.TrimSpace(in.Remark),
		CreatedBy: operator,
		UpdatedBy: operator,
		CreatedAt: &now,
		UpdatedAt: &now,
	}
	if in.Enabled != nil {
		src.Enabled = *in.Enabled
	}
	if in.AllowInsecureHTTP != nil {
		src.AllowInsecureHTTP = *in.AllowInsecureHTTP
	}
	if in.AllowPrivateHosts != nil {
		src.AllowPrivateHosts = *in.AllowPrivateHosts
	}
	if in.AllowAbsolutePaths != nil {
		src.AllowAbsolutePaths = *in.AllowAbsolutePaths
	}
	if in.Weigh != nil {
		src.Weigh = *in.Weigh
	}
	if src.Title == "" {
		src.Title = src.Name
	}
	if err := src.NormalizeAndValidate(); err != nil {
		return nil, err
	}
	return src, nil
}

// NormalizeAndValidate 规范化类型/开关并做词法校验（不联网、不读文件）。
func (s *Source) NormalizeAndValidate() error {
	if s == nil {
		return fmt.Errorf("%w: 来源为空", ErrInvalidInput)
	}
	s.Name = strings.TrimSpace(s.Name)
	s.Title = strings.TrimSpace(s.Title)
	if !namePattern.MatchString(s.Name) {
		return fmt.Errorf("%w: name %q 不合法（^[a-z][a-z0-9-]{1,31}$）", ErrInvalidInput, s.Name)
	}
	if utf8.RuneCountInString(s.Title) > 64 {
		return fmt.Errorf("%w: title 超过 64 字符", ErrInvalidInput)
	}
	if strings.ContainsRune(s.Title, 0) {
		return fmt.Errorf("%w: title 含 NUL", ErrInvalidInput)
	}
	if strings.ContainsRune(s.Remark, 0) {
		return fmt.Errorf("%w: remark 含 NUL", ErrInvalidInput)
	}
	if s.Enabled != 0 && s.Enabled != 1 {
		return fmt.Errorf("%w: enabled 只能是 0/1", ErrInvalidInput)
	}
	if s.Weigh < -1<<40 || s.Weigh > 1<<40 {
		return fmt.Errorf("%w: weigh 超出范围", ErrInvalidInput)
	}
	if utf8.RuneCountInString(s.Remark) > 255 {
		return fmt.Errorf("%w: remark 超过 255 字符", ErrInvalidInput)
	}
	return s.ValidateLocation()
}

// ValidateLocation 只校验运输方式/路径/安全开关（name 不参与；source/test 的临时来源用）。
func (s *Source) ValidateLocation() error {
	if s == nil {
		return fmt.Errorf("%w: 来源为空", ErrInvalidInput)
	}
	s.Type = strings.ToLower(strings.TrimSpace(s.Type))
	s.Location = strings.TrimSpace(s.Location)
	if len(s.Location) > MaxLocationBytes {
		return fmt.Errorf("%w: location 超过 %d 字符", ErrInvalidInput, MaxLocationBytes)
	}
	if strings.ContainsRune(s.Location, 0) {
		return fmt.Errorf("%w: location 含 NUL", ErrInvalidInput)
	}

	switch s.Type {
	case TypeLocal:
		s.AllowInsecureHTTP = 0
		s.AllowPrivateHosts = 0
		if s.AllowAbsolutePaths != 0 && s.AllowAbsolutePaths != 1 {
			return fmt.Errorf("%w: allow_absolute_paths 只能是 0/1", ErrInvalidInput)
		}
		if s.Location == "" {
			return fmt.Errorf("%w: local 来源必须给 location", ErrInvalidInput)
		}
		if strings.Contains(s.Location, "\\") {
			return fmt.Errorf("%w: location 含反斜杠（路径分隔符统一用 /）", ErrInvalidInput)
		}
		if strings.Contains(s.Location, "://") {
			return fmt.Errorf("%w: local 来源的 location 是 URL（应把 type 改为 http）", ErrInvalidInput)
		}
		if filepathIsAbs(s.Location) && s.AllowAbsolutePaths != 1 {
			return fmt.Errorf("%w: location 是绝对路径但未开启 allow_absolute_paths", ErrInvalidInput)
		}
		if !filepathIsAbs(s.Location) {
			clean := path.Clean(s.Location)
			if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
				return fmt.Errorf("%w: location %q 不是允许根内的规范化相对路径", ErrInvalidInput, s.Location)
			}
		}
	case TypeHTTP:
		s.AllowAbsolutePaths = 0
		if s.AllowInsecureHTTP != 0 && s.AllowInsecureHTTP != 1 {
			return fmt.Errorf("%w: allow_insecure_http 只能是 0/1", ErrInvalidInput)
		}
		if s.AllowPrivateHosts != 0 && s.AllowPrivateHosts != 1 {
			return fmt.Errorf("%w: allow_private_hosts 只能是 0/1", ErrInvalidInput)
		}
		if _, err := ValidateHTTPURL(s.Location, s.AllowInsecureHTTP == 1); err != nil {
			return fmt.Errorf("%w: location: %v", ErrInvalidInput, err)
		}
	case TypeGit:
		s.AllowAbsolutePaths = 0
		if s.AllowInsecureHTTP != 0 && s.AllowInsecureHTTP != 1 {
			return fmt.Errorf("%w: allow_insecure_http 只能是 0/1", ErrInvalidInput)
		}
		if s.AllowPrivateHosts != 0 && s.AllowPrivateHosts != 1 {
			return fmt.Errorf("%w: allow_private_hosts 只能是 0/1", ErrInvalidInput)
		}
		if _, err := ValidateGitLocation(s.Location, s.AllowInsecureHTTP == 1); err != nil {
			return fmt.Errorf("%w: location: %v", ErrInvalidInput, err)
		}
	default:
		return fmt.Errorf("%w: type %q 不在闭集内（%s|%s|%s）", ErrInvalidInput, s.Type, TypeLocal, TypeHTTP, TypeGit)
	}
	return nil
}

// filepathIsAbs 判断是否为绝对路径（同时识别 / 与盘符形态；不在本包 import filepath 的额外状态）。
func filepathIsAbs(p string) bool {
	return strings.HasPrefix(p, "/") || (len(p) >= 2 && p[1] == ':')
}

// ValidateHTTPURL 校验 http(s) URL 的形状：只接受 http/https、拒绝 userinfo/query/fragment、
// 主机名非空、无控制字符。明文 http 需 allowInsecure=true；SSRF（回环/私网）在取数时判定。
func ValidateHTTPURL(raw string, allowInsecure bool) (*url.URL, error) {
	if raw == "" {
		return nil, errors.New("URL 为空")
	}
	if len(raw) > MaxLocationBytes {
		return nil, fmt.Errorf("URL 超过 %d 字符", MaxLocationBytes)
	}
	for _, r := range raw {
		if unicode.IsControl(r) || r == 0 {
			return nil, errors.New("URL 含控制字符")
		}
	}
	if strings.ContainsAny(raw, " \t\r\n") {
		return nil, errors.New("URL 含空白字符")
	}
	if strings.Contains(raw, "\\") {
		return nil, errors.New("URL 含反斜杠")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("URL 无法解析: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("URL scheme %q 不允许（只接受 http/https）", u.Scheme)
	}
	if u.Scheme == "http" && !allowInsecure {
		return nil, errors.New("明文 http 未开启 allow_insecure_http")
	}
	if u.User != nil {
		return nil, errors.New("URL 不允许 userinfo（v1 不支持私有源认证）")
	}
	if u.RawQuery != "" || u.ForceQuery {
		return nil, errors.New("URL 不允许 query")
	}
	if u.Fragment != "" {
		return nil, errors.New("URL 不允许 fragment")
	}
	if u.Hostname() == "" {
		return nil, errors.New("URL 缺少主机名")
	}
	if u.Hostname() == "" || strings.TrimSpace(u.Hostname()) != u.Hostname() {
		return nil, errors.New("URL 主机名不合法")
	}
	return u, nil
}

// ParseVersion 解析 X.Y.Z 为三段整数，并做 2^31-1 上限校验。
func ParseVersion(v string) (major, minor, patch int, err error) {
	if !versionPattern.MatchString(v) {
		return 0, 0, 0, fmt.Errorf("版本 %q 不是 X.Y.Z 形式", v)
	}
	var parts [3]int
	for i, seg := range strings.Split(v, ".") {
		n := 0
		for _, r := range seg {
			if r < '0' || r > '9' {
				return 0, 0, 0, fmt.Errorf("版本 %q 含非数字段", v)
			}
			if n > (1<<31-1)/10 {
				return 0, 0, 0, fmt.Errorf("版本 %q 的段超过 2^31-1", v)
			}
			n = n*10 + int(r-'0')
			if n > 1<<31-1 {
				return 0, 0, 0, fmt.Errorf("版本 %q 的段超过 2^31-1", v)
			}
		}
		parts[i] = n
	}
	return parts[0], parts[1], parts[2], nil
}

// IsReservedUID 是否为宿主保留字（catalog 条目不得占用）。
func IsReservedUID(uid string) bool {
	for _, reserved := range CatalogReservedNames {
		if uid == reserved {
			return true
		}
	}
	return false
}

// TruncateSyncError 把同步错误截断到入库上限；调用方应先把仓库根/临时目录替换为占位符。
// 按 UTF-8 rune 边界回退，避免截断半个多字节字符（PostgreSQL text 不接受非法 UTF-8）。
func TruncateSyncError(msg string) string {
	if len(msg) <= MaxSyncErrorBytes {
		return msg
	}
	cut := MaxSyncErrorBytes
	for cut > 0 && !utf8.RuneStart(msg[cut]) {
		cut--
	}
	return msg[:cut]
}
