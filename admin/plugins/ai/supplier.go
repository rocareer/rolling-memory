// plugin: ai v0.2.0
// supplier.go 是「厂商」资源（三级配置的顶层：厂商 → 渠道 → 模型）的行模型与字段收敛。
//
// 表与列的来源：物理表 plugin_ai_supplier 由迁移 3（3_channel_driver_supplier_gate.up.sql）建立；
// 列面与源 database/migrations/20260828090852_radmin_ai_supplier.php:25-45 对齐：
//
//	name(50) / vendor(30) / base_url(255) / api_key(255) / driver(255) / timeout(60)
//	/ status / remark(255) / create_time / update_time + 索引(status)
//
// 渠道侧的新增列 supplier_id（0=独立渠道）与「渠道非空优先、空则取厂商、再无则回落插件缺省」
// 的运行期合并在 store.Candidates 落地；本文件只管厂商行本身与写入校验，不做继承合并。
//
// 与源的两处**刻意差异**（已在包级交接里登记）：
//  1. 删除保护：源的 Supplier::del 是「解除渠道挂靠 + 删主行」；本插件改为「仍被渠道引用即拒绝删除」。
//     理由是继承来源消失后渠道会退化成空凭证（调用期 fail-closed 报「渠道未配置 api_key」），
//     与其静默降级，不如让调用方显式先解除挂靠——拒绝文案给出厂商名与渠道数。
//  2. status 用渠道同款二值 smallint(0/1)，不是源的 varchar 枚举 enabled|disabled
//     （与 plugin_ai_channel.status 同一口径，也让「status 闭集」可被单测钉死）。
//
// 出口纪律：APIKey 与渠道同款——不参与 JSON 序列化、出口一律掩码、掩码永不回写。
package ai

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// 厂商列的字符上限（varchar 计字符，与迁移 3 的列宽逐字对齐）。
//
// vendor 列宽 30 不在这里单列常量：取值来自 channelVendors 闭集（最长 "moonshot"），
// 长度恒在列宽内；driver 列宽 varchar(64) 与渠道侧 driverNameMaxLen（handler_channel.go:43）同值。
const (
	supplierNameMax    = 50
	supplierBaseURLMax = 255
	supplierDriverMax  = 64
	supplierRemarkMax  = 255
)

// ErrSupplierInUse 厂商仍被渠道引用（删除保护命中的哨兵错误；errors.Is 可判定）。
var ErrSupplierInUse = errors.New("插件 ai：厂商仍被渠道引用")

// Supplier 是厂商行的领域快照，同时是 plugin_ai_supplier 的 GORM 行模型。
//
// 物理表名由 model.go 的 table() 单点拼接；列与迁移 3（3_channel_driver_supplier_gate.up.sql）逐列对齐。
// 与渠道模型的两点刻意不同（都是踩过的坑的规避）：
//   - Status/Timeout **不带 gorm 的 default 标签**：带 default 标签时 GORM 会省略「零值」字段，
//     于是显式 status=0（停用）落不下去（store_db_test.go:210-216 已登记渠道侧这个坑）；
//     这里按「显式零值就落零值」的口径，DDL 的 DEFAULT 只作用于真正省略该列的插入。
//   - Timeout=0 表示「未配」：继承合并里渠道空 → 取厂商值，厂商也空/零 → 回落插件配置。
//
// APIKey 是上游明文凭证：**不参与 JSON 序列化**（json:"-"），日志与错误文案都不得包含它。
type Supplier struct {
	ID         int64  `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	Name       string `gorm:"column:name;type:varchar(50);not null" json:"name"`
	Vendor     string `gorm:"column:vendor;type:varchar(30);not null" json:"vendor"`
	BaseURL    string `gorm:"column:base_url;type:varchar(255);not null" json:"base_url"`
	APIKey     string `gorm:"column:api_key;type:varchar(255);not null" json:"-"`
	Driver     string `gorm:"column:driver;type:varchar(64);not null" json:"driver"`
	Timeout    int    `gorm:"column:timeout;not null" json:"timeout"`
	Status     int16  `gorm:"column:status;not null" json:"status"`
	Remark     string `gorm:"column:remark;type:varchar(255);not null" json:"remark"`
	CreateTime int64  `gorm:"column:create_time;not null" json:"create_time"`
	UpdateTime int64  `gorm:"column:update_time;not null" json:"update_time"`
}

// TableName 物理表名（唯一出口 model.go 的 table()）。
func (Supplier) TableName() string { return table("plugin_ai_supplier") }

// Enabled 是否启用（status=1）。
func (s Supplier) Enabled() bool { return s.Status == StatusEnabled }

// ===== 入参 =====

// supplierSaveRequest 是厂商新增/编辑的入参；指针字段把「缺省」与「显式零值」分开
// （api_key 的三态同样靠它：缺省=不改 / ""=仍未配 / 含 '*'=掩码回填）。
type supplierSaveRequest struct {
	Name    *string `json:"name"`
	Vendor  *string `json:"vendor"`
	BaseURL *string `json:"base_url"`
	APIKey  *string `json:"api_key"`
	Driver  *string `json:"driver"`
	Timeout *int    `json:"timeout"`
	Status  *int16  `json:"status"`
	Remark  *string `json:"remark"`
}

// supplier 组装新行：必填校验 + 闭集收敛 + 数值 clamp（create）。
//
// 未配凭证允许（继承期 fail-closed 报「渠道未配置 api_key」）；name 必填（列表与继承文案都用它）。
func (r supplierSaveRequest) supplier() (*Supplier, error) {
	name, err := requiredText(r.Name, "厂商名", supplierNameMax)
	if err != nil {
		return nil, err
	}
	vendor, err := normalizeVendor(r.Vendor)
	if err != nil {
		return nil, err
	}
	baseURL, err := supplierBaseURL(r.BaseURL)
	if err != nil {
		return nil, err
	}
	apiKey, err := supplierNewAPIKey(r.APIKey)
	if err != nil {
		return nil, err
	}
	driver, err := normalizeSupplierDriver(r.Driver)
	if err != nil {
		return nil, err
	}
	status, err := supplierStatusValue(r.Status)
	if err != nil {
		return nil, err
	}
	return &Supplier{
		Name:    name,
		Vendor:  vendor,
		BaseURL: baseURL,
		APIKey:  apiKey,
		Driver:  driver,
		Timeout: supplierTimeout(r.Timeout),
		Status:  status,
		Remark:  truncateRunes(strings.TrimSpace(derefString(r.Remark)), supplierRemarkMax),
	}, nil
}

// patch 组装编辑补丁：只带出现过的字段；api_key 空串或含 '*' 一律不带（掩码永不回写）。
func (r supplierSaveRequest) patch() (SupplierPatch, error) {
	patch := SupplierPatch{}
	if r.Name != nil {
		name, err := requiredText(r.Name, "厂商名", supplierNameMax)
		if err != nil {
			return patch, err
		}
		patch.Name = &name
	}
	if r.Vendor != nil {
		vendor, err := normalizeVendor(r.Vendor)
		if err != nil {
			return patch, err
		}
		patch.Vendor = &vendor
	}
	if r.BaseURL != nil {
		baseURL, err := supplierBaseURL(r.BaseURL)
		if err != nil {
			return patch, err
		}
		patch.BaseURL = &baseURL
	}
	if r.APIKey != nil && !looksMasked(*r.APIKey) {
		key := strings.TrimSpace(*r.APIKey)
		patch.APIKey = &key
	}
	if r.Driver != nil {
		driver, err := normalizeSupplierDriver(r.Driver)
		if err != nil {
			return patch, err
		}
		patch.Driver = &driver
	}
	if r.Timeout != nil {
		timeout := supplierTimeout(r.Timeout)
		patch.Timeout = &timeout
	}
	if r.Status != nil {
		status, err := supplierStatusValue(r.Status)
		if err != nil {
			return patch, err
		}
		patch.Status = &status
	}
	if r.Remark != nil {
		remark := truncateRunes(strings.TrimSpace(*r.Remark), supplierRemarkMax)
		patch.Remark = &remark
	}
	return patch, nil
}

// ===== 单字段收敛（纯函数，可无库单测）=====

// supplierBaseURL 收敛 base_url：空串 = 未配（继承合并后仍为空则由渠道/插件缺省兜底）；
// 非空必须过 ValidateBaseURL（http/https、无 userinfo/query/fragment）——与渠道写入期同口径。
func supplierBaseURL(raw *string) (string, error) {
	value := strings.TrimSpace(derefString(raw))
	if value == "" {
		return "", nil
	}
	if err := ValidateBaseURL(value); err != nil {
		return "", fmt.Errorf("base_url 不合法: %w", err)
	}
	if len([]rune(value)) > supplierBaseURLMax {
		return "", fmt.Errorf("base_url 最长 %d 个字符", supplierBaseURLMax)
	}
	return value, nil
}

// supplierNewAPIKey 收敛新建时的 api_key。
//
// 空串 = 未配（允许）；含 '*' 的**掩码回填值一律拒**——新建时库里没有旧密钥可继承，
// 把掩码当密钥落库会让这个厂商永久带着一串星号去调用（源 add 是静默忽略，本插件选择显式报错）。
func supplierNewAPIKey(raw *string) (string, error) {
	value := strings.TrimSpace(derefString(raw))
	if value == "" {
		return "", nil
	}
	if strings.Contains(value, "*") {
		return "", errors.New(`api_key 含 "*"（疑似掩码回填）：新建时请填写完整密钥，或留空待后续配置`)
	}
	if len([]rune(value)) > 255 {
		return "", errors.New("api_key 最长 255 个字符")
	}
	return value, nil
}

// supplierTimeout 收敛 timeout：缺省 60（源列默认值/插件 request_timeout 同值）、clamp 0..600。
// 显式 0 = 未配（继承链继续往回落），与渠道 timeout 的语义一致。
func supplierTimeout(value *int) int {
	return clampInt(derefInt(value, DefaultRequestTimeout), 0, 600)
}

// supplierStatusValue 状态闭集：缺省启用；非 0/1 一律拒（插件二值口径，非源的字符串枚举）。
func supplierStatusValue(value *int16) (int16, error) {
	if value == nil {
		return StatusEnabled, nil
	}
	if !validStatus(*value) {
		return 0, errors.New("status 只能是 0（停用）或 1（启用）")
	}
	return *value, nil
}

// supplierDriver 名称白名单：形状为注册表键（DriverSpec.name，如 openai/responses/dashscope_tts）。
//
// 空串 = 未配（继承链继续往回落，最终缺省 OpenAI 兼容）。**不在此处判定「驱动是否存在」**：
// 存在的判定属注册表解析期（未知 driver 应报 local 错误，而不是在写入期硬编码一份名单），
// 这里只拦「语义上不可能是驱动名的值」（空白/路径分隔/引号），避免脏值进库后污染日志与配置面。
var supplierDriverSafe = regexp.MustCompile(`^[A-Za-z0-9_.\-]+$`)

// normalizeSupplierDriver 收敛 driver：去空白、长度上限、字符集复核。
//
// 与渠道侧的 normalizeDriverName（handler_channel.go:741-761）是**同一条规则**（同一个注册表键、
// 同一个列宽 64），此处刻意不互相调用：渠道侧那个函数名当时正被驱动注册表文件用同名符号占用
// （装配面在解冲突），供应商面自成一份可免于那场改名的连带。两处规则必须同步——
// 改动 driver 名口径（字符集/长度）时要一起改，否则同一渠道在「渠道自身填」与「继承厂商」两条
// 路径上会得到不同的合法性判定。
func normalizeSupplierDriver(raw *string) (string, error) {
	value := strings.TrimSpace(derefString(raw))
	if value == "" {
		return "", nil
	}
	if len([]rune(value)) > supplierDriverMax {
		return "", fmt.Errorf("driver 最长 %d 个字符", supplierDriverMax)
	}
	if !supplierDriverSafe.MatchString(value) {
		return "", fmt.Errorf("driver %q 含非法字符（只允许字母/数字/下划线/点/连字符）", value)
	}
	return value, nil
}

// ===== 删除保护 =====

// supplierReference 一个厂商的引用读数：渠道表里 supplier_id 指向它的行数（>0 才算被引用）。
type supplierReference struct {
	ID       int64
	Name     string
	Channels int64
}

// SupplierInUseError 删除保护命中：厂商仍被渠道引用。
//
// 文案是可读的**调用方行动指引**（谁被引用、多少个渠道、下一步该做什么），
// handler 直接把 Error() 放进 code=1 的回执（不另拼文案，避免两处口径漂移）。
type SupplierInUseError struct {
	Refs []supplierReference
}

// Error 实现 error（列表最多把所有命中项都列出来；厂商数受批量删除的 pks 上限约束）。
func (e *SupplierInUseError) Error() string {
	parts := make([]string, 0, len(e.Refs))
	var total int64
	for _, ref := range e.Refs {
		total += ref.Channels
		parts = append(parts, fmt.Sprintf("%s（%d 个渠道）", ref.Name, ref.Channels))
	}
	return fmt.Sprintf("厂商 %s 仍被渠道引用（共 %d 个）：删除会让这些渠道的继承来源消失、调用时用到空凭证，"+
		"请先解除挂靠或删除这些渠道", strings.Join(parts, "、"), total)
}

// Unwrap 让 errors.Is(err, ErrSupplierInUse) 可判定。
func (e *SupplierInUseError) Unwrap() error { return ErrSupplierInUse }

// supplierDeleteRefusal 删除保护的判定（纯函数：无库可测）。
// refs 里渠道数 >0 的厂商 ⇒ 返回 *SupplierInUseError；全为 0（或空）⇒ nil。
func supplierDeleteRefusal(refs []supplierReference) error {
	inUse := make([]supplierReference, 0, len(refs))
	for _, ref := range refs {
		if ref.Channels > 0 {
			inUse = append(inUse, ref)
		}
	}
	if len(inUse) == 0 {
		return nil
	}
	return &SupplierInUseError{Refs: inUse}
}

// isSupplierInUse 判定删除保护命中（handler 用它决定回执文案）。
func isSupplierInUse(err error) bool { return errors.Is(err, ErrSupplierInUse) }
