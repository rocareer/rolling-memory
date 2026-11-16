package source

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ai-go-hub/ai-go-admin/internal/plugin/host"

	"go.yaml.in/yaml/v3"
)

// Catalog 统一清单（schema: ai-go-plugin-catalog/v1）。
// 所有来源（本地文件 / HTTP）只接受这一份结构；来源类型只影响“怎么取字节”。
type Catalog struct {
	Schema      string        `yaml:"schema" json:"schema"`
	GeneratedAt string        `yaml:"generated_at" json:"generated_at"`
	Plugins     []PluginEntry `yaml:"plugins" json:"plugins"`
}

// PluginEntry 清单条目（包外索引；不携带落位指令/价格/下载量等字段）。
type PluginEntry struct {
	UID          string         `yaml:"uid" json:"uid"`
	Version      string         `yaml:"version" json:"version"`
	TitleZH      string         `yaml:"title_zh" json:"title_zh"`
	TitleEN      string         `yaml:"title_en" json:"title_en"`
	DescZH       string         `yaml:"desc_zh" json:"desc_zh"`
	DescEN       string         `yaml:"desc_en" json:"desc_en"`
	Kind         string         `yaml:"kind" json:"kind"`
	HostAPI      string         `yaml:"host_api" json:"host_api"`
	FrameworkReq string         `yaml:"framework_req,omitempty" json:"framework_req,omitempty"`
	Requires     []string       `yaml:"requires,omitempty" json:"requires,omitempty"`
	License      string         `yaml:"license,omitempty" json:"license,omitempty"`
	Package      CatalogPackage `yaml:"package" json:"package"`
}

// CatalogPackage 包位置与完整性声明。
type CatalogPackage struct {
	URL    string `yaml:"url,omitempty" json:"url,omitempty"`
	Path   string `yaml:"path,omitempty" json:"path,omitempty"`
	Size   int64  `yaml:"size" json:"size"`
	SHA256 string `yaml:"sha256" json:"sha256"`
	Signed *bool  `yaml:"signed" json:"signed"`
}

// catalogMaxDepth YAML 节点深度上限（防止深嵌套拖垮解析）。
const catalogMaxDepth = 20

// ParseCatalog 严格解析一份清单：UTF-8 YAML、单文档、键集闭集、拒绝锚点/别名/merge key/
// 自定义 tag/重复键/超深嵌套；字段级校验失败即整份拒绝。
func ParseCatalog(data []byte) (*Catalog, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: 清单为空", ErrInvalidInput)
	}
	if int64(len(data)) > MaxCatalogBytes {
		return nil, fmt.Errorf("%w: 清单超过 %d 字节", ErrInvalidInput, MaxCatalogBytes)
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("%w: 清单不是合法 UTF-8", ErrInvalidInput)
	}

	// 第一遍：结构扫描（锚点/别名/merge key/自定义 tag/重复键/深度/单文档）
	var root yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&root); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%w: 清单为空", ErrInvalidInput)
		}
		return nil, fmt.Errorf("%w: 解析清单失败: %v", ErrInvalidInput, err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("%w: 清单含多个 YAML 文档（只接受一份）", ErrInvalidInput)
		}
		return nil, fmt.Errorf("%w: 解析清单失败: %v", ErrInvalidInput, err)
	}
	if err := scanNode(&root, 0, "$"); err != nil {
		return nil, err
	}
	if err := validateNodeTypes(&root); err != nil {
		return nil, err
	}

	// 第二遍：结构体解码（KnownFields 拒绝未知键；其余严格性由第一遍与字段校验承担）
	dec = yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var catalog Catalog
	if err := dec.Decode(&catalog); err != nil {
		return nil, fmt.Errorf("%w: 解析清单失败（键集闭集，未知键拒绝）: %v", ErrInvalidInput, err)
	}
	if err := catalog.Validate(); err != nil {
		return nil, err
	}
	return &catalog, nil
}

// Validate 字段级校验（schema/条目/包位置/版本/依赖）。
func (c *Catalog) Validate() error {
	if c == nil {
		return fmt.Errorf("%w: 清单为空", ErrInvalidInput)
	}
	if c.Schema != CatalogSchema {
		return fmt.Errorf("%w: schema %q 不是 %q", ErrInvalidInput, c.Schema, CatalogSchema)
	}
	if c.GeneratedAt != "" {
		if _, err := time.Parse(time.RFC3339, c.GeneratedAt); err != nil {
			return fmt.Errorf("%w: generated_at %q 不是 RFC3339", ErrInvalidInput, c.GeneratedAt)
		}
	}
	if len(c.Plugins) > MaxCatalogEntries {
		return fmt.Errorf("%w: 条目数 %d 超过上限 %d", ErrInvalidInput, len(c.Plugins), MaxCatalogEntries)
	}
	seen := make(map[string]struct{}, len(c.Plugins))
	for i := range c.Plugins {
		entry := &c.Plugins[i]
		if err := entry.Validate(); err != nil {
			return fmt.Errorf("plugins[%d]: %w", i, err)
		}
		key := entry.UID + "@" + entry.Version
		if _, dup := seen[key]; dup {
			return fmt.Errorf("%w: 条目 %s 重复（同一清单内 (uid, version) 必须唯一）", ErrInvalidInput, key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// ValidateForOrigin 按清单来源形态做结构性校验（同步期执行，不下载包）：
//   - 远程清单不得出现 package.path（远程没有本地基准目录）；
//   - 本地清单的 package.url 必须是绝对 URL（本地没有“最终 URL”基准，禁止相对引用）。
func (c *Catalog) ValidateForOrigin(remote bool) error {
	for i := range c.Plugins {
		entry := &c.Plugins[i]
		if remote && strings.TrimSpace(entry.Package.Path) != "" {
			return fmt.Errorf("%w: plugins[%d]（%s@%s）远程清单不得使用 package.path",
				ErrInvalidInput, i, entry.UID, entry.Version)
		}
		if !remote && strings.TrimSpace(entry.Package.URL) != "" {
			u, err := url.Parse(strings.TrimSpace(entry.Package.URL))
			if err != nil || !u.IsAbs() {
				return fmt.Errorf("%w: plugins[%d]（%s@%s）本地清单的 package.url 必须是绝对 URL",
					ErrInvalidInput, i, entry.UID, entry.Version)
			}
		}
	}
	return nil
}

// Validate 单条目的字段级校验。
func (e *PluginEntry) Validate() error {
	if !namePattern.MatchString(e.UID) {
		return fmt.Errorf("%w: uid %q 不合法（^[a-z][a-z0-9-]{1,31}$）", ErrInvalidInput, e.UID)
	}
	if IsReservedUID(e.UID) {
		return fmt.Errorf("%w: uid %q 命中宿主保留字（%s）", ErrInvalidInput, e.UID, strings.Join(CatalogReservedNames, "/"))
	}
	if _, _, _, err := ParseVersion(e.Version); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	if err := checkText("title_zh", e.TitleZH, 1, 64); err != nil {
		return err
	}
	if err := checkText("title_en", e.TitleEN, 1, 64); err != nil {
		return err
	}
	if err := checkText("desc_zh", e.DescZH, 1, 256); err != nil {
		return err
	}
	if err := checkText("desc_en", e.DescEN, 1, 256); err != nil {
		return err
	}
	if !containsString(CatalogKinds, e.Kind) {
		return fmt.Errorf("%w: kind %q 不在闭集内（%s）", ErrInvalidInput, e.Kind, strings.Join(CatalogKinds, "|"))
	}
	if err := checkText("host_api", e.HostAPI, 1, 64); err != nil {
		return err
	}
	if _, err := host.InRange(e.HostAPI, "0.0.0"); err != nil {
		return fmt.Errorf("%w: host_api 域 %q 无法解析: %v", ErrInvalidInput, e.HostAPI, err)
	}
	if e.FrameworkReq != "" {
		if err := checkText("framework_req", e.FrameworkReq, 1, 64); err != nil {
			return err
		}
		if _, err := host.InRange(e.FrameworkReq, "0.0.0"); err != nil {
			return fmt.Errorf("%w: framework_req 域 %q 无法解析: %v", ErrInvalidInput, e.FrameworkReq, err)
		}
	}
	if e.License != "" {
		if err := checkText("license", e.License, 1, 32); err != nil {
			return err
		}
	}
	if len(e.Requires) > 64 {
		return fmt.Errorf("%w: requires 超过 64 项", ErrInvalidInput)
	}
	if _, _, err := NormalizeRequires(e.UID, e.Requires); err != nil {
		return err
	}
	return e.Package.Validate()
}

// Validate 包位置与完整性声明的字段级校验。
func (p *CatalogPackage) Validate() error {
	p.URL = strings.TrimSpace(p.URL)
	p.Path = strings.TrimSpace(p.Path)
	hasURL := p.URL != ""
	hasPath := p.Path != ""
	if hasURL == hasPath {
		return fmt.Errorf("%w: package.url 与 package.path 必须恰好一个非空", ErrInvalidInput)
	}
	if len(p.URL) > MaxLocationBytes || len(p.Path) > MaxLocationBytes {
		return fmt.Errorf("%w: package.url/path 超过 %d 字符", ErrInvalidInput, MaxLocationBytes)
	}
	if p.Size <= 0 || p.Size > MaxPackageBytes {
		return fmt.Errorf("%w: package.size 必须在 (0, %d] 内", ErrInvalidInput, MaxPackageBytes)
	}
	if !sha256Pattern.MatchString(p.SHA256) {
		return fmt.Errorf("%w: package.sha256 必须是 64 位小写十六进制", ErrInvalidInput)
	}
	if p.Signed == nil {
		return fmt.Errorf("%w: package.signed 必填（布尔）", ErrInvalidInput)
	}
	if hasURL {
		if err := validatePackageURLShape(strings.TrimSpace(p.URL)); err != nil {
			return err
		}
	}
	if hasPath {
		if err := validatePackagePathShape(strings.TrimSpace(p.Path)); err != nil {
			return err
		}
	}
	return nil
}

// Entry 查 (uid, version)（调用方应先用 Validate 保证唯一）。
func (c *Catalog) Entry(uid, version string) (*PluginEntry, bool) {
	for i := range c.Plugins {
		if c.Plugins[i].UID == uid && c.Plugins[i].Version == version {
			return &c.Plugins[i], true
		}
	}
	return nil, false
}

// NormalizeRequires 规范化依赖列表为「uid@range 逗号分隔（按 uid 排序）」并返回 uid→range。
// 规则：每项 ≤64 字符、总数 ≤64、不得自依赖、不得重复 uid、版本域语法必须可解析。
func NormalizeRequires(selfUID string, items []string) (string, map[string]string, error) {
	if len(items) > 64 {
		return "", nil, fmt.Errorf("%w: requires 超过 64 项", ErrInvalidInput)
	}
	out := make(map[string]string, len(items))
	for _, raw := range items {
		item := strings.TrimSpace(raw)
		if item == "" {
			return "", nil, fmt.Errorf("%w: requires 含空项", ErrInvalidInput)
		}
		if len(item) > 64 {
			return "", nil, fmt.Errorf("%w: requires 项 %q 超过 64 字符", ErrInvalidInput, item)
		}
		uid, domain, found := strings.Cut(item, "@")
		uid, domain = strings.TrimSpace(uid), strings.TrimSpace(domain)
		if !found || uid == "" || domain == "" {
			return "", nil, fmt.Errorf("%w: requires 项 %q 不是 uid@range 形式", ErrInvalidInput, item)
		}
		if !namePattern.MatchString(uid) {
			return "", nil, fmt.Errorf("%w: requires 项 %q 的 uid 不合法", ErrInvalidInput, item)
		}
		if IsReservedUID(uid) {
			return "", nil, fmt.Errorf("%w: requires 项 %q 命中宿主保留字", ErrInvalidInput, item)
		}
		if uid == selfUID {
			return "", nil, fmt.Errorf("%w: requires 不得自依赖（%s）", ErrInvalidInput, uid)
		}
		if _, dup := out[uid]; dup {
			return "", nil, fmt.Errorf("%w: requires 重复声明 uid %q", ErrInvalidInput, uid)
		}
		if _, err := host.InRange(domain, "0.0.0"); err != nil {
			return "", nil, fmt.Errorf("%w: requires 项 %q 的版本域无法解析: %v", ErrInvalidInput, item, err)
		}
		out[uid] = domain
	}
	keys := make([]string, 0, len(out))
	for uid := range out {
		keys = append(keys, uid)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, uid := range keys {
		parts = append(parts, uid+"@"+out[uid])
	}
	return strings.Join(parts, ","), out, nil
}

// ParseRequiresMap 解析 manifest 的逗号分隔 requires 为 uid→range（用于清单与包内元数据语义比较）。
func ParseRequiresMap(raw string) (map[string]string, error) {
	items := make([]string, 0, 4)
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	_, out, err := NormalizeRequires("", items)
	return out, err
}

// checkText 长度（按 rune）与控制字符校验。
func checkText(field, value string, min, max int) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%w: %s 不是合法 UTF-8", ErrInvalidInput, field)
	}
	n := utf8.RuneCountInString(value)
	if n < min || n > max {
		return fmt.Errorf("%w: %s 长度 %d 不在 [%d, %d] 内", ErrInvalidInput, field, n, min, max)
	}
	for _, r := range value {
		if r == 0 || unicode.IsControl(r) {
			return fmt.Errorf("%w: %s 含控制字符", ErrInvalidInput, field)
		}
	}
	return nil
}

func containsString(set []string, value string) bool {
	for _, item := range set {
		if item == value {
			return true
		}
	}
	return false
}

// validatePackageURLShape URL 字段：绝对 URL 必须是 http(s) 且无 userinfo/query/fragment；
// 相对引用（仅 HTTP 清单可用，解析时会再判定）不得是协议相对、不得含反斜杠/NUL。
func validatePackageURLShape(raw string) error {
	if strings.ContainsRune(raw, 0) || strings.Contains(raw, "\\") {
		return fmt.Errorf("%w: package.url 含 NUL 或反斜杠", ErrInvalidInput)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: package.url 无法解析: %v", ErrInvalidInput, err)
	}
	if u.IsAbs() {
		if _, err := ValidateHTTPURL(raw, true); err != nil {
			return fmt.Errorf("%w: package.url: %v", ErrInvalidInput, err)
		}
		return nil
	}
	if strings.HasPrefix(raw, "//") {
		return fmt.Errorf("%w: package.url 不得是协议相对引用", ErrInvalidInput)
	}
	return nil
}

// validatePackagePathShape 相对/绝对路径形状：拒绝 NUL、反斜杠与 `..` 段。
// 绝对路径是否放行由来源的 allow_absolute_paths 在取包时判定。
func validatePackagePathShape(raw string) error {
	if strings.ContainsRune(raw, 0) {
		return fmt.Errorf("%w: package.path 含 NUL", ErrInvalidInput)
	}
	if strings.Contains(raw, "\\") {
		return fmt.Errorf("%w: package.path 含反斜杠（路径分隔符统一用 /）", ErrInvalidInput)
	}
	if filepathIsAbs(raw) {
		return nil
	}
	clean := path.Clean(raw)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("%w: package.path %q 不是规范化相对路径", ErrInvalidInput, raw)
	}
	for _, seg := range strings.Split(raw, "/") {
		if seg == ".." {
			return fmt.Errorf("%w: package.path 含 .. 段", ErrInvalidInput)
		}
	}
	return nil
}

// ---- YAML Node 严格性 ----

func scanNode(n *yaml.Node, depth int, at string) error {
	if depth > catalogMaxDepth {
		return fmt.Errorf("%w: %s 嵌套深度超过 %d", ErrInvalidInput, at, catalogMaxDepth)
	}
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) != 1 {
			return fmt.Errorf("%w: %s 文档节点数不是 1", ErrInvalidInput, at)
		}
		return scanNode(n.Content[0], depth+1, at)
	case yaml.MappingNode:
		if len(n.Content)%2 != 0 {
			return fmt.Errorf("%w: %s 映射节点不完整", ErrInvalidInput, at)
		}
		seen := make(map[string]struct{}, len(n.Content)/2)
		for i := 0; i < len(n.Content); i += 2 {
			key, value := n.Content[i], n.Content[i+1]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return fmt.Errorf("%w: %s 含非字符串键", ErrInvalidInput, at)
			}
			if key.Value == "<<" {
				return fmt.Errorf("%w: %s 含 merge key <<（拒绝）", ErrInvalidInput, at)
			}
			if _, dup := seen[key.Value]; dup {
				return fmt.Errorf("%w: %s 键 %q 重复", ErrInvalidInput, at, key.Value)
			}
			seen[key.Value] = struct{}{}
			child := at + "." + key.Value
			if err := checkNodeTag(key, child); err != nil {
				return err
			}
			if err := checkNodeTag(value, child); err != nil {
				return err
			}
			if err := scanNode(value, depth+1, child); err != nil {
				return err
			}
		}
		return nil
	case yaml.SequenceNode:
		if err := checkNodeTag(n, at); err != nil {
			return err
		}
		for i := range n.Content {
			if err := checkNodeTag(n.Content[i], fmt.Sprintf("%s[%d]", at, i)); err != nil {
				return err
			}
			if err := scanNode(n.Content[i], depth+1, fmt.Sprintf("%s[%d]", at, i)); err != nil {
				return err
			}
		}
		return nil
	case yaml.ScalarNode:
		return checkNodeTag(n, at)
	case yaml.AliasNode:
		return fmt.Errorf("%w: %s 含 YAML 别名（拒绝）", ErrInvalidInput, at)
	default:
		return fmt.Errorf("%w: %s 含不支持的 YAML 节点类型", ErrInvalidInput, at)
	}
}

func checkNodeTag(n *yaml.Node, at string) error {
	if n.Anchor != "" {
		return fmt.Errorf("%w: %s 使用 YAML 锚点（拒绝）", ErrInvalidInput, at)
	}
	switch n.Kind {
	case yaml.MappingNode:
		if n.Tag != "" && n.Tag != "!!map" {
			return fmt.Errorf("%w: %s 使用自定义 tag %q（只允许标准 tag）", ErrInvalidInput, at, n.Tag)
		}
	case yaml.SequenceNode:
		if n.Tag != "" && n.Tag != "!!seq" {
			return fmt.Errorf("%w: %s 使用自定义 tag %q（只允许标准 tag）", ErrInvalidInput, at, n.Tag)
		}
	case yaml.ScalarNode:
		if n.Anchor != "" {
			return fmt.Errorf("%w: %s 使用 YAML 锚点（拒绝）", ErrInvalidInput, at)
		}
		switch n.Tag {
		case "!!str", "!!int", "!!bool", "!!null", "!!timestamp":
		default:
			return fmt.Errorf("%w: %s 使用自定义/不支持 tag %q", ErrInvalidInput, at, n.Tag)
		}
	default:
		return fmt.Errorf("%w: %s 含不支持的 YAML 节点类型", ErrInvalidInput, at)
	}
	return nil
}

// validateNodeTypes 校验必填字段的 YAML 标量类型（KnownFields 只拒绝未知键，不阻止
// 把整数/布尔值解码到 string 字段；host_api 必须加引号写成字符串即由此保证）。
func validateNodeTypes(root *yaml.Node) error {
	if root.Kind != yaml.DocumentNode || len(root.Content) != 1 {
		return fmt.Errorf("%w: 清单必须是单个 YAML 映射", ErrInvalidInput)
	}
	top := root.Content[0]
	if top.Kind != yaml.MappingNode {
		return fmt.Errorf("%w: 清单顶层必须是映射", ErrInvalidInput)
	}
	if err := requireString(top, "schema", "$"); err != nil {
		return err
	}
	if node, ok := mappingValue(top, "generated_at"); ok {
		// RFC3339 字符串；未加引号的 YAML 时间戳（!!timestamp）同样接受。
		if node.Kind != yaml.ScalarNode || (node.Tag != "!!str" && node.Tag != "!!timestamp") {
			return fmt.Errorf("%w: $.generated_at 必须是 RFC3339 字符串", ErrInvalidInput)
		}
	}
	plugins, ok := mappingValue(top, "plugins")
	if !ok || plugins.Kind != yaml.SequenceNode {
		return fmt.Errorf("%w: $.plugins 必须是数组", ErrInvalidInput)
	}
	for i, item := range plugins.Content {
		at := fmt.Sprintf("$.plugins[%d]", i)
		if item.Kind != yaml.MappingNode {
			return fmt.Errorf("%w: %s 必须是映射", ErrInvalidInput, at)
		}
		for _, key := range []string{"uid", "version", "title_zh", "title_en", "desc_zh", "desc_en", "kind", "host_api"} {
			if err := requireString(item, key, at); err != nil {
				return err
			}
		}
		if node, ok := mappingValue(item, "framework_req"); ok {
			if err := requireScalarTag(node, "!!str", at+".framework_req"); err != nil {
				return err
			}
		}
		if node, ok := mappingValue(item, "license"); ok {
			if err := requireScalarTag(node, "!!str", at+".license"); err != nil {
				return err
			}
		}
		if node, ok := mappingValue(item, "requires"); ok {
			if node.Kind != yaml.SequenceNode {
				return fmt.Errorf("%w: %s.requires 必须是字符串数组", ErrInvalidInput, at)
			}
			for j, req := range node.Content {
				if err := requireScalarTag(req, "!!str", fmt.Sprintf("%s.requires[%d]", at, j)); err != nil {
					return err
				}
			}
		}
		pkg, ok := mappingValue(item, "package")
		if !ok || pkg.Kind != yaml.MappingNode {
			return fmt.Errorf("%w: %s.package 必须是映射", ErrInvalidInput, at)
		}
		for _, key := range []string{"url", "path", "sha256"} {
			if node, ok := mappingValue(pkg, key); ok {
				if err := requireScalarTag(node, "!!str", at+".package."+key); err != nil {
					return err
				}
			}
		}
		if node, ok := mappingValue(pkg, "size"); ok {
			if err := requireScalarTag(node, "!!int", at+".package.size"); err != nil {
				return err
			}
		}
		signed, ok := mappingValue(pkg, "signed")
		if !ok {
			return fmt.Errorf("%w: %s.package.signed 必填（布尔）", ErrInvalidInput, at)
		}
		if err := requireScalarTag(signed, "!!bool", at+".package.signed"); err != nil {
			return err
		}
	}
	return nil
}

func requireString(mapping *yaml.Node, key, at string) error {
	node, ok := mappingValue(mapping, key)
	if !ok {
		return fmt.Errorf("%w: %s.%s 必填", ErrInvalidInput, at, key)
	}
	return requireScalarTag(node, "!!str", at+"."+key)
}

func requireScalarTag(node *yaml.Node, tag, at string) error {
	if node.Kind != yaml.ScalarNode || node.Tag != tag {
		return fmt.Errorf("%w: %s 必须是 %s（YAML 类型不符）", ErrInvalidInput, at, tag)
	}
	return nil
}

func mappingValue(mapping *yaml.Node, key string) (*yaml.Node, bool) {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil, false
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1], true
		}
	}
	return nil, false
}
