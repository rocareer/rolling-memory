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

// 标签（kb_tag）管理面自定义逻辑（同包独立文件——SKILL DoD 第 4 条）。
// 写面校验：组必须存在（引用存在性）、标签名规整、状态 0/1 值域（与列渲染/表单控件同一值域口径）。

// List 列表（浏览面上限：limit 截断到 MaxBrowseLimit=100；Count 不受分页影响）。
func (s *KBTagService) List(ctx context.Context, opts service.Options) ([]model.KBTag, error) {
	opts.Limit = svcKB.NormalizeBrowseLimit(opts.Limit)
	return s.IService.List(ctx, opts)
}

// Create 新建标签：组存在性 + 名规整 + 状态值域（显式给值时校验；缺省由 DDL default 1 生效）。
func (s *KBTagService) Create(ctx context.Context, tri *bindx.Tri[model.KBTag], opts service.Options) error {
	name, err := normalizeTagText(tri.Model.Name, "标签名", maxTagNameLen)
	if err != nil {
		return err
	}
	tri.Model.Name = name
	if err := requireTagGroup(ctx, s.repo.DB(), tri.Model.GroupID); err != nil {
		return err
	}
	if tri.Model.Status != nil && *tri.Model.Status > 1 {
		return errors.New("状态取值须为 0/1")
	}
	if err := s.IService.Create(ctx, tri, opts); err != nil {
		return tagWriteError(err, "组内标签名已存在（软删豁免；组间可同名）")
	}
	return nil
}

// Update 更新标签：组/名/状态按合并结果校验（禁用标签不可新绑定由绑定面单点拦截，本处只保词表一致）。
func (s *KBTagService) Update(ctx context.Context, tri *bindx.Tri[model.KBTag], opts service.Options) error {
	if v, ok, isNull := mapValue(tri.Map, "group_id"); ok {
		if isNull {
			return errors.New("所属标签组不可为 null")
		}
		groupID, err := mapUint(v)
		if err != nil {
			return err
		}
		if err := requireTagGroup(ctx, s.repo.DB(), groupID); err != nil {
			return err
		}
	}
	if v, ok, isNull := mapValue(tri.Map, "name"); ok {
		if isNull {
			return errors.New("标签名不可为 null")
		}
		raw, err := mapString(v)
		if err != nil {
			return err
		}
		name, err := normalizeTagText(raw, "标签名", maxTagNameLen)
		if err != nil {
			return err
		}
		tri.Map["name"] = name
	}
	if v, ok, _ := mapValue(tri.Map, "status"); ok {
		if _, err := mapStatus(v); err != nil {
			return err
		}
	}
	if err := s.IService.Update(ctx, tri, opts); err != nil {
		return tagWriteError(err, "组内标签名已存在（软删豁免；组间可同名）")
	}
	return nil
}
