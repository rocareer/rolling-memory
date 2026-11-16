// plugin: kb v1.0.0
package kb

import (
	"context"
	"errors"
	"fmt"

	"github.com/ai-go-hub/ai-go-admin/internal/kit/bindx"
	"github.com/ai-go-hub/ai-go-admin/internal/service"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
	repoKb "github.com/ai-go-hub/ai-go-admin/plugins/kb/repository/admin/kb"
	svcKB "github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb"
)

// 标签规则（kb_tag_rule）管理面自定义逻辑（同包独立文件——SKILL DoD 第 4 条）。
// 写面校验：库存在性、match_type 闭集（代码闸，加值零迁移）、匹配式规整、命中标签的**跨库引用拒绝**
// （T2：库组标签不可被其它库引用；平台标签跨库可用）——规则随库作用，tag_id 必须落在该库可见词表内。

// tagRuleMatchTypes match_type 闭集（path_glob|filename_regex|suffix|meta_eq；与模型常量单点同源）
var tagRuleMatchTypes = map[string]bool{
	model.KBTagRuleMatchPathGlob:      true,
	model.KBTagRuleMatchFilenameRegex: true,
	model.KBTagRuleMatchSuffix:        true,
	model.KBTagRuleMatchMetaEq:        true,
}

// List 列表（浏览面上限：limit 截断到 MaxBrowseLimit=100；Count 不受分页影响）。
func (s *KBTagRuleService) List(ctx context.Context, opts service.Options) ([]model.KBTagRule, error) {
	opts.Limit = svcKB.NormalizeBrowseLimit(opts.Limit)
	return s.IService.List(ctx, opts)
}

// Create 新建规则：库存在 + 闭集 + 匹配式 + 命中标签在该库可见（跨库引用响亮拒）。
func (s *KBTagRuleService) Create(ctx context.Context, tri *bindx.Tri[model.KBTagRule], opts service.Options) error {
	name, err := normalizeTagText(tri.Model.Name, "规则名", maxTagNameLen)
	if err != nil {
		return err
	}
	tri.Model.Name = name
	if err := validateTagRule(ctx, s.repo, tri.Model.LibraryID, tri.Model.MatchType, tri.Model.Pattern, tri.Model.TagID); err != nil {
		return err
	}
	if tri.Model.Enabled != nil && *tri.Model.Enabled > 1 {
		return errors.New("启用取值须为 0/1")
	}
	if err := s.IService.Create(ctx, tri, opts); err != nil {
		return tagWriteError(err, "同库内规则名已存在（软删豁免）")
	}
	return nil
}

// Update 更新规则：库/匹配类型/匹配式/命中标签按合并结果校验（任一改动都要重判跨库引用）。
func (s *KBTagRuleService) Update(ctx context.Context, tri *bindx.Tri[model.KBTagRule], opts service.Options) error {
	row, err := s.IService.Get(ctx, opts)
	if err != nil {
		return err
	}
	libraryID, matchType, pattern, tagID := row.LibraryID, row.MatchType, row.Pattern, row.TagID
	if v, ok, isNull := mapValue(tri.Map, "library_id"); ok {
		if isNull {
			return errors.New("所属库不可为 null")
		}
		if libraryID, err = mapUint(v); err != nil {
			return err
		}
	}
	if v, ok, isNull := mapValue(tri.Map, "match_type"); ok {
		if isNull {
			return errors.New("匹配类型不可为 null")
		}
		if matchType, err = mapString(v); err != nil {
			return err
		}
	}
	if v, ok, isNull := mapValue(tri.Map, "pattern"); ok {
		if isNull {
			return errors.New("匹配式不可为 null")
		}
		raw, err := mapString(v)
		if err != nil {
			return err
		}
		if pattern, err = normalizeTagText(raw, "匹配式", maxTagPatternLen); err != nil {
			return err
		}
		tri.Map["pattern"] = pattern
	}
	if v, ok, isNull := mapValue(tri.Map, "tag_id"); ok {
		if isNull {
			return errors.New("命中标签不可为 null")
		}
		if tagID, err = mapUint(v); err != nil {
			return err
		}
	}
	if v, ok, isNull := mapValue(tri.Map, "name"); ok {
		if isNull {
			return errors.New("规则名不可为 null")
		}
		raw, err := mapString(v)
		if err != nil {
			return err
		}
		name, err := normalizeTagText(raw, "规则名", maxTagNameLen)
		if err != nil {
			return err
		}
		tri.Map["name"] = name
	}
	if v, ok, _ := mapValue(tri.Map, "enabled"); ok {
		if _, err := mapStatus(v); err != nil {
			return err
		}
	}
	if err := validateTagRule(ctx, s.repo, libraryID, matchType, pattern, tagID); err != nil {
		return err
	}
	if err := s.IService.Update(ctx, tri, opts); err != nil {
		return tagWriteError(err, "同库内规则名已存在（软删豁免）")
	}
	return nil
}

// validateTagRule 规则引用面校验单点（Create/Update 共用；创建与更新不得走出两套口径）
func validateTagRule(ctx context.Context, repo *repoKb.KBTagRuleRepository, libraryID uint, matchType, pattern string, tagID uint) error {
	if err := requireLibrary(ctx, repo.DB(), libraryID); err != nil {
		return err
	}
	if !tagRuleMatchTypes[matchType] {
		return fmt.Errorf("匹配类型取值未登记: %q（闭集 path_glob|filename_regex|suffix|meta_eq）", matchType)
	}
	if _, err := normalizeTagText(pattern, "匹配式", maxTagPatternLen); err != nil {
		return err
	}
	return tagScopeCoversLibrary(ctx, repo.DB(), tagID, libraryID)
}
