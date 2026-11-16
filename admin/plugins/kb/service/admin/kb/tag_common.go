// plugin: kb v1.0.0
package kb

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"

	"gorm.io/gorm"
)

// 标签三表管理面的共用校验（设计：词表完整性——入词表/被引用的值必须落在闭集与既有引用面上）。
//
// 自定义逻辑一律落在同包独立文件（SKILL DoD 第 4 条：生成文件不堆逻辑，再生成时不被覆盖）；
// 本文件是三个 *_custom.go 的共用小件（闭集、文本规整、引用存在性、跨库判定、唯一冲突映射）。
//
// 与 service/kb（隔离面）的关系：本包不做可读性判定（标签不扩权，「不扩权」），只做**词表与引用**
// 的完整性校验；绑定面的对象级门与跨库拒绝在 service/kb（tag_link.go）单点实现，本包不复制谓词。

// maxTagNameLen / maxTagPatternLen 文本上限（对齐 DDL：name varchar(64)、pattern varchar(255)）
const (
	maxTagNameLen    = 64
	maxTagPatternLen = 255
)

// normalizeTagText 去首尾空白并按上限校验（空/超长一律响亮拒——不静默截断）
func normalizeTagText(raw, field string, maxLen int) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", errors.New(field + "必填")
	}
	if len([]rune(value)) > maxLen {
		return "", fmt.Errorf("%s 超长（%d > %d 字符）", field, len([]rune(value)), maxLen)
	}
	return value, nil
}

// requireLibrary 引用存在性：库必须存在（软删即不存在）
func requireLibrary(ctx context.Context, db *gorm.DB, libraryID uint) error {
	if libraryID == 0 {
		return errors.New("所属库必填（> 0）")
	}
	if _, err := gorm.G[model.KBLibrary](db).Where("id = ?", libraryID).First(ctx); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("所属库不存在或已删: %d", libraryID)
		}
		return err
	}
	return nil
}

// requireTagGroup 引用存在性：标签组必须存在（软删即不存在）
func requireTagGroup(ctx context.Context, db *gorm.DB, groupID uint) error {
	if groupID == 0 {
		return errors.New("所属标签组必填（> 0）")
	}
	if _, err := gorm.G[model.KBTagGroup](db).Where("id = ?", groupID).First(ctx); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("所属标签组不存在或已删: %d", groupID)
		}
		return err
	}
	return nil
}

// tagScopeCoversLibrary 跨库引用判定（T2：库可见标签 = 平台组 ∪ 本库组；库组标签不可被其它库引用）。
// 标签的作用域经组继承（kb_tag.group_id → kb_tag_group.scope/library_id）。
func tagScopeCoversLibrary(ctx context.Context, db *gorm.DB, tagID, libraryID uint) error {
	if tagID == 0 {
		return errors.New("命中标签必填（> 0）")
	}
	tag, err := gorm.G[model.KBTag](db).Where("id = ?", tagID).First(ctx)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("标签不存在或已删: %d", tagID)
		}
		return err
	}
	group, err := gorm.G[model.KBTagGroup](db).Where("id = ?", tag.GroupID).First(ctx)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("标签所属组不存在或已删（标签 %d）", tagID)
		}
		return err
	}
	switch group.Scope {
	case model.KBTagScopePlatform:
		return nil
	case model.KBTagScopeLibrary:
		if group.LibraryID != libraryID {
			return fmt.Errorf("标签 %d 属库 %d，不可跨库引用（目标库 %d；标签不扩权、库组标签仅本库可用）", tagID, group.LibraryID, libraryID)
		}
		return nil
	default:
		return fmt.Errorf("标签作用域未登记: %q（标签 %d）", group.Scope, tagID)
	}
}

// scopeLibraryPair 作用域闭集 + library_id 一致性（T2：0=平台，platform 恒 0）
func scopeLibraryPair(scope string, libraryID uint) error {
	switch scope {
	case model.KBTagScopePlatform:
		if libraryID != 0 {
			return fmt.Errorf("平台组 library_id 恒 0（得到 %d）", libraryID)
		}
		return nil
	case model.KBTagScopeLibrary:
		if libraryID == 0 {
			return errors.New("库组必填所属库（library_id > 0）")
		}
		return nil
	default:
		return fmt.Errorf("作用域取值未登记: %q（闭集 platform|library）", scope)
	}
}

// mapValue 按字段名取三绑定 map 值（未传时 ok=false；显式 null 时 isNull=true）
func mapValue(m map[string]any, key string) (value any, ok bool, isNull bool) {
	v, present := m[key]
	if !present {
		return nil, false, false
	}
	return v, true, isNilLike(v)
}

// isNilLike 判断类型化 map 值是否为 null（指针类字段显式传 null 时值为 nil 指针）
func isNilLike(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case *uint8:
		return t == nil
	case *uint16:
		return t == nil
	case *uint:
		return t == nil
	case *int64:
		return t == nil
	case *string:
		return t == nil
	default:
		return false
	}
}

// mapUint 从类型化 map 值取 uint（uint 族与整型；负数与带小数的浮点一律拒）
func mapUint(v any) (uint, error) {
	switch t := v.(type) {
	case uint:
		return t, nil
	case uint8:
		return uint(t), nil
	case uint16:
		return uint(t), nil
	case uint32:
		return uint(t), nil
	case uint64:
		return uint(t), nil
	case int:
		if t < 0 {
			return 0, errors.New("取值不可为负")
		}
		return uint(t), nil
	case int8:
		if t < 0 {
			return 0, errors.New("取值不可为负")
		}
		return uint(t), nil
	case int16:
		if t < 0 {
			return 0, errors.New("取值不可为负")
		}
		return uint(t), nil
	case int32:
		if t < 0 {
			return 0, errors.New("取值不可为负")
		}
		return uint(t), nil
	case int64:
		if t < 0 {
			return 0, errors.New("取值不可为负")
		}
		return uint(t), nil
	case float64:
		if t < 0 || t != float64(uint64(t)) {
			return 0, errors.New("取值须为非负整数")
		}
		return uint(t), nil
	default:
		return 0, fmt.Errorf("取值类型非法: %T", v)
	}
}

// mapString 从类型化 map 值取 string
func mapString(v any) (string, error) {
	if s, ok := v.(string); ok {
		return s, nil
	}
	return "", fmt.Errorf("取值类型非法（应为字符串）: %T", v)
}

// mapStatus 取 0/1 轴值（*uint8 字段：显式 null 拒；越界拒——状态列值域唯一写法 0/1）
func mapStatus(v any) (uint8, error) {
	switch t := v.(type) {
	case uint8:
		if t > 1 {
			return 0, fmt.Errorf("状态取值须为 0/1（得到 %d）", t)
		}
		return t, nil
	case *uint8:
		if t == nil {
			return 0, errors.New("状态不可为 null（0/1 值域）")
		}
		if *t > 1 {
			return 0, fmt.Errorf("状态取值须为 0/1（得到 %d）", *t)
		}
		return *t, nil
	default:
		return 0, fmt.Errorf("状态取值类型非法: %T", v)
	}
}

// isUniqueViolation 唯一键冲突判定（管理员面友好文案用；GORM 未开 TranslateError，按驱动文案兜底）。
// 注：service/kb 侧有同型私有判定（隔离面单点）；本处为管理面文案映射，不承载隔离语义。
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return errors.Is(err, gorm.ErrDuplicatedKey) || strings.Contains(msg, "23505") || strings.Contains(msg, "duplicate key")
}

// tagWriteError 写面错误文案映射：唯一冲突换成面内可读文案（软删部分唯一的复用语义要让人看得懂）；
// 其余错误原样回显（响亮失败，不吞原因）。
func tagWriteError(err error, uniqueMsg string) error {
	if isUniqueViolation(err) {
		return errors.New(uniqueMsg)
	}
	return err
}
