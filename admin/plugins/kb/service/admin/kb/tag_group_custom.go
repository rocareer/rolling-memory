// plugin: kb v1.0.0
package kb

import (
	"context"
	"errors"

	"github.com/ai-go-hub/ai-go-admin/internal/kit/bindx"
	"github.com/ai-go-hub/ai-go-admin/internal/service"
	"github.com/ai-go-hub/ai-go-admin/plugins/kb/model"
	svcKB "github.com/ai-go-hub/ai-go-admin/plugins/kb/service/kb"
)

// 标签组（kb_tag_group）管理面自定义逻辑（同包独立文件——SKILL DoD 第 4 条）。
// 生成器产物的补齐项：浏览面上限（唯一入口 NormalizeBrowseLimit）与写面词表校验（闭集 + 引用存在性）。

// List 列表（浏览面上限：limit 截断到 MaxBrowseLimit=100；Count 不受分页影响）。
func (s *KBTagGroupService) List(ctx context.Context, opts service.Options) ([]model.KBTagGroup, error) {
	opts.Limit = svcKB.NormalizeBrowseLimit(opts.Limit)
	return s.IService.List(ctx, opts)
}

// Create 新建标签组：作用域闭集 + library_id 一致性（T2：0=平台，platform 恒 0）。
func (s *KBTagGroupService) Create(ctx context.Context, tri *bindx.Tri[model.KBTagGroup], opts service.Options) error {
	name, err := normalizeTagText(tri.Model.Name, "组名", maxTagNameLen)
	if err != nil {
		return err
	}
	tri.Model.Name = name
	if err := scopeLibraryPair(tri.Model.Scope, tri.Model.LibraryID); err != nil {
		return err
	}
	if tri.Model.Scope == model.KBTagScopeLibrary {
		if err := requireLibrary(ctx, s.repo.DB(), tri.Model.LibraryID); err != nil {
			return err
		}
	}
	if err := s.IService.Create(ctx, tri, opts); err != nil {
		return tagWriteError(err, "同作用域内组名已存在（软删豁免）")
	}
	return nil
}

// Update 更新标签组：作用域 × library_id 按合并结果校验（防「platform + library_id>0」类不一致落库——
// 作用域是跨库可见性的开关，静默写坏会把库组标签暴露成跨库可见）。
func (s *KBTagGroupService) Update(ctx context.Context, tri *bindx.Tri[model.KBTagGroup], opts service.Options) error {
	row, err := s.IService.Get(ctx, opts)
	if err != nil {
		return err
	}
	scope, libraryID := row.Scope, row.LibraryID
	if v, ok, isNull := mapValue(tri.Map, "scope"); ok {
		if isNull {
			return errors.New("作用域不可为 null")
		}
		if scope, err = mapString(v); err != nil {
			return err
		}
	}
	if v, ok, isNull := mapValue(tri.Map, "library_id"); ok {
		if isNull {
			return errors.New("所属库不可为 null（平台组填 0）")
		}
		if libraryID, err = mapUint(v); err != nil {
			return err
		}
	}
	if v, ok, isNull := mapValue(tri.Map, "name"); ok {
		if isNull {
			return errors.New("组名不可为 null")
		}
		raw, err := mapString(v)
		if err != nil {
			return err
		}
		name, err := normalizeTagText(raw, "组名", maxTagNameLen)
		if err != nil {
			return err
		}
		tri.Map["name"] = name
	}
	if err := scopeLibraryPair(scope, libraryID); err != nil {
		return err
	}
	if scope == model.KBTagScopeLibrary {
		if err := requireLibrary(ctx, s.repo.DB(), libraryID); err != nil {
			return err
		}
	}
	if err := s.IService.Update(ctx, tri, opts); err != nil {
		return tagWriteError(err, "同作用域内组名已存在（软删豁免）")
	}
	return nil
}
